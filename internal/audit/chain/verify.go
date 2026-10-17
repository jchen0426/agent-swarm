package chain

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Kind names what a verification found.
type Kind string

// What a verification can find.
const (
	// BrokenLink: a record whose prev is not the hash of the record before,
	// or whose hash does not recompute.
	BrokenLink Kind = "broken_link"
	// Gap: sequence numbers missing between records.
	Gap Kind = "gap"
	// Truncated: a chain whose first record is not the first ever written.
	// Records are never pruned in this phase, so the records before it were
	// deleted.
	Truncated Kind = "truncated"
	// ChangedRow: an audited row whose digest differs from its record's.
	ChangedRow Kind = "changed_row"
	// MissingRow: a record whose audited row is gone.
	MissingRow Kind = "missing_row"
	// UnchainedRow: an audited row that no record chains.
	UnchainedRow Kind = "unchained_row"
	// UnchainedRows: an anchor counted more rows of a table than the chain
	// held records of it, so rows were written that no record chained.
	UnchainedRows Kind = "unchained_rows"
	// UnknownForm: a record whose form this build cannot render.
	UnknownForm Kind = "unknown_form"
	// Lost: anchors whose records the chain no longer held when a later
	// anchor was taken, between that anchor's base and it: a restore, a
	// deletion or a rewrite. Only the store's owner can tell which.
	Lost Kind = "lost"
	// Diverged: an anchor whose records the chain no longer holds, which no
	// anchor taken since has seen: a restore, a deletion or a rewrite since.
	Diverged Kind = "diverged"
	// AnchorGap: consecutive anchors further apart than the interval allows.
	AnchorGap Kind = "anchor_gap"
	// AnchorStale: no anchor within the interval before the verification.
	AnchorStale Kind = "anchor_stale"
	// AnchorConflict: versions of one anchor, or anchors of one seq, that
	// disagree.
	AnchorConflict Kind = "anchor_conflict"
	// AnchorHidden: a delete marker that hides an anchor.
	AnchorHidden Kind = "anchor_hidden"
	// BadAnchor: an object in the store that is no valid anchor of the
	// tenant under its own name.
	BadAnchor Kind = "bad_anchor"
	// StoreUnlocked: an anchor store, or an object in it, that is not kept
	// write-once.
	StoreUnlocked Kind = "store_unlocked"
	// Table: an audited table that is not a plain table, filters rows by
	// row security, lets another trigger or a rule act on its inserts, or
	// does not chain what is inserted into it, now or when an anchor was
	// taken.
	Table Kind = "table"
)

// Grace is how late an anchor may be past its interval.
const Grace = 10 * time.Minute

// Finding is one thing a verification found.
type Finding struct {
	Kind Kind
	// Seq is the chain position the finding concerns, and Through the last
	// position of a range.
	Seq, Through int64
	// Source and Key name the audited row or table it concerns.
	Source, Key string
	// Anchor names the anchor object it concerns.
	Anchor string
	// Runs are the runs that may have lost records.
	Runs   []string
	Detail string
}

// String writes the finding on one line.
func (f *Finding) String() string {
	var b strings.Builder
	b.WriteString(string(f.Kind))
	switch {
	case f.Through > f.Seq:
		fmt.Fprintf(&b, " seq %d-%d", f.Seq, f.Through)
	case f.Seq > 0:
		fmt.Fprintf(&b, " seq %d", f.Seq)
	}
	if f.Source != "" {
		fmt.Fprintf(&b, " %s %s", f.Source, f.Key)
	}
	if f.Anchor != "" {
		fmt.Fprintf(&b, " anchor %s", f.Anchor)
	}
	if len(f.Runs) > 0 {
		fmt.Fprintf(&b, " runs %s", strings.Join(f.Runs, ","))
	}
	if f.Detail != "" {
		b.WriteString(": " + f.Detail)
	}
	return b.String()
}

// Walk checks a chain record by record, in sequence order, against its
// anchors.
type Walk struct {
	anchors []Anchor
	bySeq   map[int64][]int
	// held marks the anchors whose seq and hash a record matched.
	held     []bool
	first    *Link
	last     *Link
	findings []Finding
}

// NewWalk starts a walk against anchors, as CheckAnchors returns them, in
// time order.
func NewWalk(anchors []Anchor) *Walk {
	w := &Walk{anchors: anchors, bySeq: map[int64][]int{}, held: make([]bool, len(anchors))}
	for i := range anchors {
		w.bySeq[anchors[i].Seq] = append(w.bySeq[anchors[i].Seq], i)
		w.checkCounts(&anchors[i])
		for _, table := range anchors[i].Unsafe {
			w.add(&Finding{Kind: Table, Source: table, Anchor: anchors[i].Key(),
				Detail: "when the anchor was taken, verification could not trust the table's state"})
		}
	}
	for _, i := range w.bySeq[0] {
		w.held[i] = true
	}
	return w
}

// Next checks the next record: the first is the first ever written; each
// follows the one before without a gap, names its hash as prev, and hashes
// as it says. An anchor of its seq that holds its hash is held.
func (w *Walk) Next(l *Link) {
	switch {
	case w.last == nil:
		if l.Seq != 1 || l.Prev != (Sum{}) {
			w.add(&Finding{Kind: Truncated, Seq: 1, Through: l.Seq - 1, Detail: "the chain does not start at its first record: records before it were deleted"})
		}
	case l.Seq != w.last.Seq+1:
		w.add(&Finding{Kind: Gap, Seq: w.last.Seq + 1, Through: l.Seq - 1, Detail: "records are missing"})
	case l.Prev != w.last.Hash:
		w.add(&Finding{Kind: BrokenLink, Seq: l.Seq, Source: l.Source, Key: l.Key, Detail: "prev is not the hash of the record before"})
	}
	if LinkHash(l.Prev, l) != l.Hash {
		w.add(&Finding{Kind: BrokenLink, Seq: l.Seq, Source: l.Source, Key: l.Key, Detail: "the hash does not recompute"})
	}
	for _, i := range w.bySeq[l.Seq] {
		w.held[i] = w.anchors[i].Hash == l.Hash
	}
	c := *l
	if w.first == nil {
		w.first = &c
	}
	w.last = &c
}

// checkCounts reports each table of which an anchor counted more rows than
// the chain held records of it then. Both counts are the anchor's own, so
// the finding stands whatever the chain holds now.
func (w *Walk) checkCounts(a *Anchor) {
	for _, source := range slices.Sorted(maps.Keys(a.Counts)) {
		if n := a.Counts[source]; n > a.Records[source] {
			w.add(&Finding{Kind: UnchainedRows, Seq: a.Seq, Source: source, Anchor: a.Key(),
				Detail: fmt.Sprintf("the anchor counted %d rows, and the chain held %d records of them", n, a.Records[source])})
		}
	}
}

// End returns every finding: those of the records, then what only the
// whole chain shows. An anchor the chain does not hold was seen lost by a
// later anchor when it lies between that anchor and the base it named, the
// latest anchor the chain still held then. Each such later anchor makes
// one finding of what it saw lost, with the runs whose records went, and
// when they went: between its base and it. Every other anchor the chain
// does not hold makes a finding of its own. Whether records went to a
// restore, a deletion or a rewrite, only the store's owner can tell.
// Anchors further apart than every plus Grace, or none that recent, mean
// anchoring stopped. An anchor dated after now plus Grace, as a clock ahead
// writes it, is a finding; its seq and hash still count, but it neither
// sees others lost nor shows when anchoring last happened.
func (w *Walk) End(now time.Time, every time.Duration) []Finding {
	ahead := make([]bool, len(w.anchors))
	for i := range w.anchors {
		if ahead[i] = w.anchors[i].At.After(now.Add(Grace)); ahead[i] {
			w.add(&Finding{Kind: BadAnchor, Anchor: w.anchors[i].Key(),
				Detail: "the anchor is dated after now: the clock of the host that took it was ahead"})
		}
	}
	seenBy := w.seenLost(ahead)
	lost := map[int][]int{}
	for x := range w.anchors {
		switch {
		case w.held[x]:
		case seenBy[x] >= 0:
			lost[seenBy[x]] = append(lost[seenBy[x]], x)
		default:
			a := &w.anchors[x]
			w.add(&Finding{Kind: Diverged, Seq: a.Seq, Anchor: a.Key(), Runs: a.Runs,
				Detail: "the chain no longer holds the anchor's records, and no anchor taken since has seen it: a restore, a deletion or a rewrite since the anchor"})
		}
	}
	for _, by := range slices.Sorted(maps.Keys(lost)) {
		w.reportLost(by, lost[by])
	}
	w.reportStopped(now, every, ahead)
	return w.findings
}

// seenLost returns, for each anchor, the earliest later anchor that saw it
// lost, or -1. An anchor that names a base the store does not hold before
// it sees nothing, and is itself a finding; one dated ahead sees nothing.
func (w *Walk) seenLost(ahead []bool) []int {
	index := map[string]int{}
	for i := range w.anchors {
		index[w.anchors[i].Key()] = i
	}
	seenBy := make([]int, len(w.anchors))
	for i := range seenBy {
		seenBy[i] = -1
	}
	for i := range w.anchors {
		a := &w.anchors[i]
		if a.Base == "" || ahead[i] {
			continue
		}
		b, ok := index[a.Base]
		if !ok || b >= i {
			w.add(&Finding{Kind: BadAnchor, Anchor: a.Key(), Detail: "its base " + a.Base + " is no earlier anchor in the store"})
			continue
		}
		for x := b + 1; x < i; x++ {
			if !w.held[x] && seenBy[x] < 0 {
				seenBy[x] = i
			}
		}
	}
	return seenBy
}

// reportLost makes one finding of the anchors that anchor by saw lost.
func (w *Walk) reportLost(by int, anchors []int) {
	f := Finding{Kind: Lost, Anchor: w.anchors[by].Key(), Seq: w.anchors[anchors[0]].Seq,
		Detail: fmt.Sprintf("the chain no longer held the records of %d anchors when this anchor was taken: a restore, a deletion or a rewrite after %s, the latest anchor it still held", len(anchors), w.anchors[by].Base)}
	for _, x := range anchors {
		f.Seq, f.Through = min(f.Seq, w.anchors[x].Seq), max(f.Through, w.anchors[x].Seq)
		f.Runs = append(f.Runs, w.anchors[x].Runs...)
	}
	slices.Sort(f.Runs)
	f.Runs = slices.Compact(f.Runs)
	w.add(&f)
}

// reportStopped reports anchors further apart than every plus Grace, or
// none that recent, among those not dated ahead.
func (w *Walk) reportStopped(now time.Time, every time.Duration, ahead []bool) {
	var dated []*Anchor
	for i := range w.anchors {
		if !ahead[i] {
			dated = append(dated, &w.anchors[i])
		}
	}
	limit := every + Grace
	for i := 1; i < len(dated); i++ {
		if gap := dated[i].At.Sub(dated[i-1].At); gap > limit {
			w.add(&Finding{Kind: AnchorGap, Anchor: dated[i].Key(), Detail: fmt.Sprintf("%s after the anchor before", gap.Round(time.Second))})
		}
	}
	switch n := len(dated); {
	case n == 0:
		w.add(&Finding{Kind: AnchorStale, Detail: "no anchor was taken"})
	case now.Sub(dated[n-1].At) > limit:
		w.add(&Finding{Kind: AnchorStale, Anchor: dated[n-1].Key(), Detail: fmt.Sprintf("the latest anchor is %s old", now.Sub(dated[n-1].At).Round(time.Second))})
	}
}

func (w *Walk) add(f *Finding) { w.findings = append(w.findings, *f) }
