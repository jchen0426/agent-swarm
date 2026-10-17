package chain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// take returns the anchor an hourly job takes at record l, at hour h after
// t0, naming base as the latest anchor the chain still held.
func take(l *chain.Link, h int, base *chain.Anchor, runs ...string) chain.Anchor {
	a := chain.Anchor{Tenant: "tenant-a", Seq: l.Seq, Hash: l.Hash, ChainedAt: l.ChainedAt, At: t0.Add(time.Duration(h) * time.Hour), Runs: runs}
	if base != nil {
		a.Base, a.After = base.Key(), base.Seq
	}
	return a
}

// walk runs a walk over records against anchors, at now.
func walk(records []chain.Link, anchors []chain.Anchor, now time.Time) []chain.Finding {
	w := chain.NewWalk(anchors)
	for i := range records {
		w.Next(&records[i])
	}
	return w.End(now, time.Hour)
}

func kinds(findings []chain.Finding) []chain.Kind {
	out := []chain.Kind{}
	for i := range findings {
		out = append(out, findings[i].Kind)
	}
	return out
}

// rewrite returns records with every one from index i on changed and linked
// again, consistently, as an operator who rewrites history would, or as a
// chain written again after a restore is.
func rewrite(records []chain.Link, i int, run string) []chain.Link {
	out := append([]chain.Link(nil), records[:i]...)
	var prev chain.Sum
	if i > 0 {
		prev = records[i-1].Hash
	}
	for j := i; j < len(records); j++ {
		l := records[j]
		l.Run, l.Digest, l.Prev = run, chain.Digest("rewritten "+run), prev
		l.Hash = chain.LinkHash(prev, &l)
		out = append(out, l)
		prev = l.Hash
	}
	return out
}

// A chain whose records follow one another from the first, and match every
// anchor, anchored within the hour, verifies clean.
func TestACleanChainVerifies(t *testing.T) {
	ls := links(5)
	a1 := take(&ls[1], 1, nil, "run-1")
	a2 := take(&ls[4], 2, &a1, "run-1")

	assert.Empty(t, walk(ls, []chain.Anchor{a1, a2}, t0.Add(150*time.Minute)))
}

// A record that was changed, re-linked or removed shows.
func TestARecordThatWasTamperedWithShows(t *testing.T) {
	ls := links(5)
	edited := append([]chain.Link(nil), ls...)
	edited[2].Digest = chain.Digest("other")
	relinked := append([]chain.Link(nil), ls...)
	relinked[2].Prev = chain.Digest("x")
	relinked[2].Hash = chain.LinkHash(relinked[2].Prev, &relinked[2])
	removed := append(append([]chain.Link(nil), ls[:2]...), ls[3:]...)
	a := take(&ls[4], 0, nil)
	now := t0.Add(time.Minute)

	assert.Equal(t, []chain.Kind{chain.BrokenLink}, kinds(walk(edited, []chain.Anchor{a}, now)), "edited")
	assert.Equal(t, []chain.Kind{chain.BrokenLink, chain.BrokenLink}, kinds(walk(relinked, []chain.Anchor{a}, now)), "relinked")
	assert.Equal(t, []chain.Kind{chain.Gap}, kinds(walk(removed, []chain.Anchor{a}, now)), "removed")
}

// A chain rewritten consistently from a record on shows at each anchor
// after it, each a finding of its own that names its runs.
func TestARewrittenChainShowsAtItsAnchors(t *testing.T) {
	ls := links(6)
	a1 := take(&ls[1], 0, nil, "run-1")
	a2 := take(&ls[3], 1, &a1, "run-2")
	a3 := take(&ls[5], 2, &a2, "run-3")

	findings := walk(rewrite(ls, 2, "run-x"), []chain.Anchor{a1, a2, a3}, t0.Add(2*time.Hour))

	require.Equal(t, []chain.Kind{chain.Diverged, chain.Diverged}, kinds(findings))
	assert.Equal(t, int64(4), findings[0].Seq)
	assert.Equal(t, []string{"run-2"}, findings[0].Runs)
	assert.Equal(t, []string{"run-3"}, findings[1].Runs)
}

// restored returns a chain of 12 records restored to its first 7, then
// written again to 14, and the anchors an hourly job took: four before the
// restore, at seq 3, 6, 9 and 12, and two after it, at seq 10 and 14. The
// first after the restore names the anchor at seq 6 as its base: the chain
// no longer held those at seq 9 and 12.
func restored() (records []chain.Link, anchors []chain.Anchor) {
	before := links(12)
	a1 := take(&before[2], 1, nil, "run-a")
	a2 := take(&before[5], 2, &a1, "run-b")
	a3 := take(&before[8], 3, &a2, "run-c")
	a3.Counts = map[string]int64{"board_events": 9, "tool_calls": 3}
	a3.Records = map[string]int64{"board_events": 9, "tool_calls": 3}
	a4 := take(&before[11], 4, &a3, "run-d")
	records = rewrite(append(append([]chain.Link(nil), before[:7]...), links(14)[7:]...), 7, "run-new")
	a5 := take(&records[9], 5, &a2, "run-new")
	a6 := take(&records[13], 6, &a5, "run-new")
	return records, []chain.Anchor{a1, a2, a3, a4, a5, a6}
}

// After a restore, the anchor taken next names the records the chain no
// longer held, and the runs whose records went, also once new writes pass
// the old anchors.
func TestARestoreShowsWhatWasLost(t *testing.T) {
	records, anchors := restored()

	findings := walk(records, anchors, t0.Add(6*time.Hour))

	require.Equal(t, []chain.Kind{chain.Lost}, kinds(findings))
	assert.Equal(t, []int64{9, 12}, []int64{findings[0].Seq, findings[0].Through})
	assert.Equal(t, []string{"run-c", "run-d"}, findings[0].Runs)
	assert.Equal(t, anchors[4].Key(), findings[0].Anchor)
}

// Tampering after a restore shows apart from it: a rewrite of the history
// before the restore, or of the first hour after it, makes a finding at each
// anchor it changed, and, once an anchor is taken after it, one finding of
// its own, seen by that anchor.
func TestTamperingAfterARestoreShowsApartFromIt(t *testing.T) {
	records, anchors := restored()
	for name, tc := range map[string]struct {
		from    int
		changed []chain.Anchor
		held    *chain.Anchor
	}{
		"before the restore": {4, []chain.Anchor{anchors[1], anchors[4], anchors[5]}, &anchors[0]},
		"after the restore":  {8, []chain.Anchor{anchors[4], anchors[5]}, &anchors[1]},
	} {
		tampered := rewrite(records, tc.from, "run-x")
		var diverged []string
		for _, a := range tc.changed {
			diverged = append(diverged, a.Key())
		}

		findings := walk(tampered, anchors, t0.Add(6*time.Hour))

		want := []chain.Kind{}
		for range tc.changed {
			want = append(want, chain.Diverged)
		}
		assert.Equal(t, append(want, chain.Lost), kinds(findings), name)
		var gotDiverged []string
		for i := range findings {
			if findings[i].Kind == chain.Diverged {
				gotDiverged = append(gotDiverged, findings[i].Anchor)
			}
		}
		assert.Equal(t, diverged, gotDiverged, name)

		after := take(&tampered[len(tampered)-1], 7, tc.held, "run-x")
		findings = walk(tampered, append(append([]chain.Anchor(nil), anchors...), after), t0.Add(7*time.Hour))

		var lostBy []string
		for i := range findings {
			require.Equal(t, chain.Lost, findings[i].Kind, "%s: %s", name, findings[i].String())
			lostBy = append(lostBy, findings[i].Anchor)
		}
		assert.Equal(t, []string{anchors[4].Key(), after.Key()}, lostBy, name)
	}
}

// An anchor whose base is no earlier anchor in the store sees nothing lost,
// and shows.
func TestAnAnchorWithoutItsBaseShows(t *testing.T) {
	ls := links(4)
	a1 := take(&ls[1], 0, nil)
	elsewhere := take(&ls[0], 1, nil)
	a2 := take(&ls[3], 1, &elsewhere)

	findings := walk(ls, []chain.Anchor{a1, a2}, t0.Add(time.Hour))

	assert.Equal(t, []chain.Kind{chain.BadAnchor}, kinds(findings))
}

// A chain whose first record is not the first ever written was cut: the
// records before it were deleted, and anchors of them show too.
func TestAChainThatDoesNotStartAtItsFirstRecordWasCut(t *testing.T) {
	ls := links(6)
	a1 := take(&ls[2], 0, nil, "run-1")
	a2 := take(&ls[5], 1, &a1, "run-2")

	findings := walk(ls[3:], []chain.Anchor{a1, a2}, t0.Add(time.Hour))

	assert.Equal(t, []chain.Kind{chain.Truncated, chain.Diverged}, kinds(findings))
	assert.Equal(t, []int64{1, 3}, []int64{findings[0].Seq, findings[0].Through})
}

// An anchor that counted more rows of a table than the chain held records
// of it shows rows that were written unchained, even once they are gone and
// the chain no longer holds the anchor: both counts are the anchor's own.
func TestRowsAnAnchorCountedButTheChainNeverHeldShow(t *testing.T) {
	ls := links(3)
	empty := chain.Anchor{Tenant: "tenant-a", At: t0.Add(-time.Hour), Counts: map[string]int64{"tool_hides": 1}}
	honest := take(&ls[1], 0, &empty, "run-1")
	honest.Counts = map[string]int64{"board_events": 2, "tool_calls": 0}
	honest.Records = map[string]int64{"board_events": 2}
	padded := take(&ls[2], 1, &honest, "run-1")
	padded.Counts = map[string]int64{"board_events": 3, "tool_calls": 4}
	padded.Records = map[string]int64{"board_events": 3}
	anchors := []chain.Anchor{empty, honest, padded}

	for name, records := range map[string][]chain.Link{"held": ls, "rewritten": rewrite(ls, 1, "run-x")} {
		var unchained []string
		for _, f := range walk(records, anchors, t0.Add(time.Hour)) {
			if f.Kind == chain.UnchainedRows {
				unchained = append(unchained, f.Source)
			}
		}
		assert.Equal(t, []string{"tool_hides", "tool_calls"}, unchained, name)
	}
}

// Anchoring that stopped, or never began, shows.
func TestStoppedAnchoringShows(t *testing.T) {
	ls := links(3)
	first := take(&ls[0], 0, nil)
	late := []chain.Anchor{first, take(&ls[2], 3, &first)}

	assert.Equal(t, []chain.Kind{chain.AnchorGap}, kinds(walk(ls, late, t0.Add(3*time.Hour))))
	assert.Equal(t, []chain.Kind{chain.AnchorStale}, kinds(walk(ls, late[:1], t0.Add(71*time.Minute))))
	assert.Empty(t, walk(ls, late[:1], t0.Add(69*time.Minute)))
	assert.Equal(t, []chain.Kind{chain.AnchorStale}, kinds(walk(ls, nil, t0)))
}

// An anchor dated after now is a finding, but its seq and hash still count:
// records it alone covers that the chain no longer holds still show.
func TestAnAnchorDatedAheadStillCountsAsEvidence(t *testing.T) {
	ls := links(4)
	a1 := take(&ls[1], 0, nil, "run-1")
	ahead := take(&ls[3], 24*365, &a1, "run-2")

	findings := walk(rewrite(ls, 2, "run-x"), []chain.Anchor{a1, ahead}, t0.Add(time.Hour))

	assert.ElementsMatch(t, []chain.Kind{chain.BadAnchor, chain.Diverged}, kinds(findings))
}

// An anchor that left out runs shows, whether the chain still holds it or
// not: a loss among its records may involve runs that no finding names.
func TestAnAnchorThatLeftOutRunsShows(t *testing.T) {
	ls := links(2)
	a := take(&ls[1], 0, nil, "run-1")
	a.Omitted = 2

	findings := walk(ls, []chain.Anchor{a}, t0.Add(time.Hour))

	assert.Equal(t, []chain.Kind{chain.RunsOmitted}, kinds(findings))
}
