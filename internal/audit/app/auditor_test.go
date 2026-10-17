package app_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

var t0 = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

// fakeChain is a chain held in memory: its records, each with a run, the
// rows verification reads, and how its tables are held. It is its own view.
type fakeChain struct {
	links     []chain.Link
	rows      []app.Row
	unchained []app.Row
	counts    map[string]int64
	tables    []app.Table
}

func (c *fakeChain) Read(_ context.Context, fn func(app.View) error) error { return fn(c) }

func (c *fakeChain) Head(context.Context) (*chain.Link, error) {
	if len(c.links) == 0 {
		return nil, nil
	}
	l := c.links[len(c.links)-1]
	return &l, nil
}

func (c *fakeChain) HashAt(_ context.Context, seq int64) (chain.Sum, bool, error) {
	for i := range c.links {
		if c.links[i].Seq == seq {
			return c.links[i].Hash, true, nil
		}
	}
	return chain.Sum{}, false, nil
}

func (c *fakeChain) Runs(_ context.Context, after, upTo int64) ([]string, error) {
	var runs []string
	for i := range c.links {
		if l := &c.links[i]; l.Seq > after && l.Seq <= upTo && l.Run != "" && !slices.Contains(runs, l.Run) {
			runs = append(runs, l.Run)
		}
	}
	slices.Sort(runs)
	return runs, nil
}

func (c *fakeChain) Counts(context.Context) (map[string]int64, error) { return c.counts, nil }

func (c *fakeChain) Records(context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for i := range c.links {
		out[c.links[i].Source]++
	}
	return out, nil
}

func (c *fakeChain) Links(_ context.Context, fn func(*chain.Link) error) error {
	for i := range c.links {
		if err := fn(&c.links[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *fakeChain) Rows(_ context.Context, fn func(*app.Row) error) error {
	for i := range c.rows {
		if err := fn(&c.rows[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *fakeChain) Unchained(context.Context) ([]app.Row, error) { return c.unchained, nil }

func (c *fakeChain) Tables(context.Context) ([]app.Table, error) { return c.tables, nil }

// add appends a record of run to the chain, and its row.
func (c *fakeChain) add(run string) {
	var prev chain.Sum
	if n := len(c.links); n > 0 {
		prev = c.links[n-1].Hash
	}
	rendered := []byte(`["row ` + run + `"]`)
	l := chain.Link{Seq: int64(len(c.links) + 1), Source: "board_events", Key: `["k"]`, Run: run, Form: 1,
		Digest: chain.Digest(string(rendered)), Prev: prev, ChainedAt: t0.Add(time.Duration(len(c.links)) * time.Minute)}
	l.Hash = chain.LinkHash(prev, &l)
	c.links = append(c.links, l)
	c.rows = append(c.rows, app.Row{Seq: l.Seq, Source: l.Source, Key: l.Key, Form: 1, Digest: l.Digest, Known: true, Rendered: rendered})
}

// fakeAnchors is a store held in memory. A locked one keeps what it writes
// under retention for a year. It counts the bodies it reads.
type fakeAnchors struct {
	objects []chain.Stored
	locked  bool
	reads   int
}

func (s *fakeAnchors) Put(_ context.Context, key string, body []byte) error {
	if slices.ContainsFunc(s.objects, func(o chain.Stored) bool { return o.Key == key }) {
		return app.ErrExists
	}
	o := chain.Stored{Key: key, Body: body}
	if s.locked {
		o.LockedUntil = t0.AddDate(1, 0, 0)
	}
	s.objects = append(s.objects, o)
	return nil
}

func (s *fakeAnchors) List(context.Context, string) ([]chain.Stored, error) {
	out := make([]chain.Stored, 0, len(s.objects))
	for _, o := range s.objects {
		out = append(out, chain.Stored{Key: o.Key, Version: o.Version, Deleted: o.Deleted})
	}
	return out, nil
}

func (s *fakeAnchors) find(o *chain.Stored) *chain.Stored {
	for i := range s.objects {
		if s.objects[i].Key == o.Key && s.objects[i].Version == o.Version {
			return &s.objects[i]
		}
	}
	return nil
}

func (s *fakeAnchors) Read(_ context.Context, o *chain.Stored) error {
	s.reads++
	o.Body = s.find(o).Body
	return nil
}

// Retention fails in a store that keeps nothing write-once, as an S3 bucket
// without object lock does.
func (s *fakeAnchors) Retention(_ context.Context, o *chain.Stored) error {
	if !s.locked {
		return errors.New("the bucket is missing its object lock configuration")
	}
	o.LockedUntil = s.find(o).LockedUntil
	return nil
}

func (s *fakeAnchors) Locked(context.Context) (locked bool, why string, err error) {
	if s.locked {
		return true, "", nil
	}
	return false, "a fake", nil
}

func newAuditor(t *testing.T, c *fakeChain, s app.Anchors, now *time.Time) *app.Auditor {
	t.Helper()
	a, err := app.New(c, s, "tenant-a", func() time.Time { return *now })
	require.NoError(t, err)
	return a
}

func kinds(findings []chain.Finding) []chain.Kind {
	out := []chain.Kind{}
	for i := range findings {
		out = append(out, findings[i].Kind)
	}
	return out
}

// An anchor holds the chain's head, and the runs whose records the chain
// gained since the anchor before, its base.
func TestAnAnchorHoldsTheHeadAndTheRunsSinceTheAnchorBefore(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	c.add("run-2")
	first, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.add("run-2")
	c.add("run-3")
	now = now.Add(time.Hour)

	second, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []string{"run-1", "run-2"}, first.Runs)
	assert.Equal(t, int64(4), second.Seq)
	assert.Equal(t, c.links[3].Hash, second.Hash)
	assert.Equal(t, first.Key(), second.Base)
	assert.Equal(t, int64(2), second.After)
	assert.Equal(t, []string{"run-2", "run-3"}, second.Runs)
	assert.Len(t, s.objects, 2)
}

// An anchor counts the rows of each audited table, and the records the chain
// holds of it, from the same snapshot as the head.
func TestAnAnchorCountsTheRowsAndRecordsOfEachTable(t *testing.T) {
	c := &fakeChain{counts: map[string]int64{"board_events": 1, "tool_calls": 0}}
	c.add("run-1")
	s := &fakeAnchors{locked: true}
	now := t0

	an, err := newAuditor(t, c, s, &now).Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"board_events": 1, "tool_calls": 0}, an.Counts)
	assert.Equal(t, map[string]int64{"board_events": 1}, an.Records)
}

// An empty chain is anchored at seq 0, so that deleting everything shows.
func TestAnEmptyChainIsAnchored(t *testing.T) {
	s := &fakeAnchors{locked: true}
	now := t0

	an, err := newAuditor(t, &fakeChain{}, s, &now).Anchor(t.Context())

	require.NoError(t, err)
	assert.Zero(t, an.Seq)
	require.Len(t, s.objects, 1)
	_, err = chain.Decode(s.objects[0].Body)
	require.NoError(t, err)
}

// After a restore, the next anchor names as its base the latest anchor the
// chain still holds, and counts its runs from there: an anchor whose seq the
// chain wrote again, with other records, is lost, not a base. It is written
// all the same, since a missing anchor would hide the restore.
func TestAnAnchorAfterARestoreNamesTheLatestAnchorTheChainHolds(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	first, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.add("run-2")
	c.add("run-3")
	now = now.Add(time.Hour)
	lost, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.links, c.rows = c.links[:1], c.rows[:1]
	c.add("run-new")
	c.add("run-new")
	c.add("run-newer")
	now = now.Add(time.Hour)

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, first.Key(), lost.Base)
	assert.Equal(t, int64(4), an.Seq)
	assert.Equal(t, first.Key(), an.Base)
	assert.Equal(t, int64(1), an.After)
	assert.Equal(t, []string{"run-new", "run-newer"}, an.Runs)
}

// An anchor reads the anchors newest first, and only until it finds its
// base: a year of hourly anchors costs one read an hour.
func TestAnAnchorReadsOnlyTheAnchorsItNeeds(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	for range 30 {
		c.add("run-1")
		_, err := a.Anchor(t.Context())
		require.NoError(t, err)
		now = now.Add(time.Hour)
	}
	s.reads = 0

	_, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 1, s.reads)
}

// An anchor passes over anchors dated after now, as a host whose clock ran
// ahead writes them: its base is always an anchor taken before it.
func TestAnAnchorPassesOverAnchorsDatedAfterNow(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	first, err := a.Anchor(t.Context())
	require.NoError(t, err)
	now = t0.Add(48 * time.Hour)
	ahead, err := a.Anchor(t.Context())
	require.NoError(t, err)
	now = t0.Add(time.Hour)

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, first.Key(), an.Base)
	assert.NotEqual(t, ahead.Key(), an.Base)
}

// The chain is never anchored empty once records were anchored, as while a
// restore is under way: the first anchor after the restore must see its
// losses.
func TestAnAnchorRefusesAnEmptyChainOnceRecordsWereAnchored(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.links, c.rows = nil, nil
	now = now.Add(time.Hour)

	_, err = a.Anchor(t.Context())

	require.ErrorContains(t, err, "anchor once a restore is done")
	assert.Len(t, s.objects, 1)
}

// An anchor is written once: a second one for the same time and head fails.
func TestAnAnchorIsNeverWrittenTwice(t *testing.T) {
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, &fakeChain{}, s, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)

	_, err = a.Anchor(t.Context())

	require.ErrorIs(t, err, app.ErrExists)
}

// A clean chain, anchored within the hour in a write-once store, verifies
// with no finding.
func TestACleanChainVerifies(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	c.add("run-2")
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	now = now.Add(30 * time.Minute)

	got, err := a.Verify(t.Context(), time.Hour)

	require.NoError(t, err)
	assert.Empty(t, got)
}

// Verification reports what the rows, the chain and the store show: a row
// changed, a row gone, a record of a form this build cannot render, a row
// no record chains, and a store that is not write-once.
func TestVerificationReportsEveryKindOfFinding(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{}
	now := t0
	a := newAuditor(t, c, s, &now)
	for range 4 {
		c.add("run-1")
	}
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.rows[0].Rendered = []byte(`["changed"]`)
	c.rows[1].Rendered = nil
	c.rows[2].Known, c.rows[2].Rendered = false, nil
	c.unchained = []app.Row{{Source: "tool_hides", Key: `["x"]`}}

	got, err := a.Verify(t.Context(), time.Hour)

	require.NoError(t, err)
	assert.ElementsMatch(t, []chain.Kind{chain.StoreUnlocked, chain.ChangedRow, chain.MissingRow, chain.UnknownForm, chain.UnchainedRow}, kinds(got))
}

// An anchor dated after now is a finding, and takes no part in the walk:
// it neither serves as a base nor hides that anchoring stopped.
func TestVerificationReportsAnAnchorDatedAfterNow(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	now = t0.AddDate(1, 0, 0)
	_, err = a.Anchor(t.Context())
	require.NoError(t, err)
	now = t0.Add(5 * time.Hour)

	got, err := a.Verify(t.Context(), time.Hour)

	require.NoError(t, err)
	assert.ElementsMatch(t, []chain.Kind{chain.BadAnchor, chain.AnchorStale}, kinds(got))
}

// unsafeTables are tables verification cannot trust: one that is no plain
// table, one that row security filters, one whose inserts are not chained,
// one whose inserts another trigger or a rule acts on, one that is missing.
var unsafeTables = []app.Table{
	{Name: "audit_chain", Plain: true, Chains: true},
	{Name: "board_events", Plain: false, Chains: true},
	{Name: "tool_calls", Plain: true, Filtered: true, Chains: true},
	{Name: "tool_hides", Plain: true},
	{Name: "config_revisions", Plain: true, Chains: true, Altered: true},
	{Name: "config_objects"},
}

// tableFindings returns the tables that table findings name, and the
// anchors that saw them, apart.
func tableFindings(got []chain.Finding) (now, anchored []string) {
	for i := range got {
		switch {
		case got[i].Kind != chain.Table:
		case got[i].Anchor == "":
			now = append(now, got[i].Source)
		default:
			anchored = append(anchored, got[i].Source)
		}
	}
	return now, anchored
}

// Rows verification cannot trust show, table by table.
func TestVerificationReportsTablesItCannotTrust(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.tables = unsafeTables

	got, err := a.Verify(t.Context(), time.Hour)

	require.NoError(t, err)
	current, anchored := tableFindings(got)
	assert.Equal(t, []string{"board_events", "tool_calls", "tool_hides", "config_revisions", "config_objects"}, current)
	assert.Empty(t, anchored)
}

// An anchor keeps the tables verification could not trust when it was
// taken, so that a trigger dropped again before verification runs still
// shows.
func TestTablesUnsafeWhenAnAnchorWasTakenStillShow(t *testing.T) {
	c := &fakeChain{tables: unsafeTables}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	an, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.tables = nil

	got, err := a.Verify(t.Context(), time.Hour)

	require.NoError(t, err)
	assert.Equal(t, []string{"board_events", "config_objects", "config_revisions", "tool_calls", "tool_hides"}, an.Unsafe)
	current, anchored := tableFindings(got)
	assert.Empty(t, current)
	assert.Equal(t, an.Unsafe, anchored)
}

// In a locked store, each anchor object must itself be under retention in
// compliance mode: one whose retention ended can be deleted.
func TestEachAnchorMustBeUnderComplianceRetention(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	s.objects[0].LockedUntil = t0.Add(-time.Minute)

	got, err := a.Verify(t.Context(), time.Hour)

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, chain.StoreUnlocked, got[0].Kind)
	assert.Equal(t, s.objects[0].Key, got[0].Anchor)
}

// A tenant id that cannot name anchors is refused.
func TestATenantIDMustNameAnchors(t *testing.T) {
	_, err := app.New(&fakeChain{}, &fakeAnchors{}, "Tenant/A", time.Now)

	require.Error(t, err)
}

// A store that cannot be read fails verification; it never verifies clean.
func TestAStoreThatCannotBeReadFailsVerification(t *testing.T) {
	now := t0
	a := newAuditor(t, &fakeChain{}, &brokenAnchors{}, &now)

	_, err := a.Verify(t.Context(), time.Hour)

	require.Error(t, err)
}

type brokenAnchors struct{ fakeAnchors }

func (*brokenAnchors) List(context.Context, string) ([]chain.Stored, error) {
	return nil, errors.New("store down")
}

// After a restore far back, the anchors past the chain's head are passed
// over unread: the chain cannot hold them, and reading a year of them would
// hold the database snapshot open.
func TestAnAnchorPassesOverAnchorsPastTheHeadUnread(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	first, err := a.Anchor(t.Context())
	require.NoError(t, err)
	for range 20 {
		now = now.Add(time.Hour)
		c.add("run-2")
		_, err := a.Anchor(t.Context())
		require.NoError(t, err)
	}
	c.links, c.rows = c.links[:1], c.rows[:1]
	now = now.Add(time.Hour)
	s.reads = 0

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, first.Key(), an.Base)
	assert.Equal(t, 2, s.reads, "the newest anchor, then the base")
}

// An anchor reads one version of each anchor it reads: versions of one key
// are verification's to compare.
func TestAnAnchorReadsOneVersionOfEachAnchor(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	c.add("run-1")
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	for _, v := range []string{"v2", "v3", "v4"} {
		o := s.objects[0]
		o.Version = v
		s.objects = append(s.objects, o)
	}
	c.links, c.rows = c.links[:1], c.rows[:1]
	c.add("run-x")
	now = now.Add(time.Hour)
	s.reads = 0

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Empty(t, an.Base, "the chain no longer holds the anchor")
	assert.Equal(t, 1, s.reads)
}

// An anchor that would not read back is never written: the job fails, so
// that the store never holds an anchor that verification would set aside.
func TestAnAnchorThatWouldNotReadBackIsNeverWritten(t *testing.T) {
	c := &fakeChain{}
	c.add("run-1")
	c.links[0].Source = "Not A Table"
	s := &fakeAnchors{locked: true}
	now := t0

	_, err := newAuditor(t, c, s, &now).Anchor(t.Context())

	require.ErrorContains(t, err, "would not read back")
	assert.Empty(t, s.objects)
}

// A run too long to name, as a record appended by hand can carry, is
// counted rather than named: anchoring goes on, and verification shows it.
func TestAnAnchorCountsRunsThatWouldNotFit(t *testing.T) {
	c := &fakeChain{}
	c.add(strings.Repeat("r", chain.MaxAnchorBytes))
	c.add("run-1")
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)

	an, err := a.Anchor(t.Context())
	require.NoError(t, err)
	findings, err := a.Verify(t.Context(), time.Hour)
	require.NoError(t, err)

	assert.Equal(t, []string{"run-1"}, an.Runs)
	assert.Equal(t, int64(1), an.Omitted)
	assert.Contains(t, kinds(findings), chain.RunsOmitted)
}
