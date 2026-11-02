package app

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// revisionOfRules is a revision of nothing but redaction rules that apply
// always, each named and matching its pattern.
func revisionOfRules(t *testing.T, patterns map[string]string) *config.Revision {
	t.Helper()
	files := map[string][]byte{}
	for name, pattern := range patterns {
		files["redaction-rules/"+name+".yaml"] = fmt.Appendf(nil, "kind: RedactionRule\nmetadata: {name: %s, owner: security@example.com}\nspec:\n  always: true\n  patterns: ['%s']\n", name, pattern)
	}
	fsys, err := memfs.New(files)
	require.NoError(t, err)
	cc, err := celeval.New()
	require.NoError(t, err)
	rev, err := config.Build(fsys, strings.Repeat("a", 40), cc)
	require.NoError(t, err)
	return rev
}

// The claim an opening composes is replaced whole when its escapes may hide
// what a rule that applies always removes, whether the rule reads the claim
// alone or as the member claim=<claim>, and as the board holds it, trimmed:
// the board holds it as that member of its snapshot, and a claim the LLM
// gateway cannot read makes it refuse every model call that reads the
// snapshot. Open checks the routing it records; this is the backstop for
// records it did not check, and for text only the claim as a whole reads. An
// opening that hides nothing is composed and redacted as ever.
func TestTheOpeningClaimIsReplacedWhenItsEscapesMayHideAMatch(t *testing.T) {
	rev := revisionOfRules(t, map[string]string{
		"api-keys": `sk-[a-z0-9]{8}`, "openings": `^Opened by person: yyy`, "claims": `claim=Opened by person: xxx`, "endings": `zzz$`,
	})
	replaced := "Opened by person: [an opening the redaction rules could not read]"
	for name, c := range map[string]struct {
		opened *item.Opened
		claim  string
	}{
		"a routing value":  {opened: &item.Opened{Source: "person", Title: "checkout fails", Routing: map[string]string{"service": "sk-\\u0061bcdefgh"}}, claim: replaced},
		"a routing key":    {opened: &item.Opened{Source: "person", Title: "checkout fails", Routing: map[string]string{"sk-\\u0061bcdefgh": "checkout"}}, claim: replaced},
		"the claim alone":  {opened: &item.Opened{Source: "person", Title: "\\u0079yy"}, claim: replaced},
		"the claim member": {opened: &item.Opened{Source: "person", Title: "\\u0078xx"}, claim: replaced},
		"the claim trimmed": {
			opened: &item.Opened{Source: "person", Title: "checkout fails", Routing: map[string]string{"service": "zz\\u007a "}},
			claim:  replaced,
		},
		"a plain secret": {
			opened: &item.Opened{Source: "person", Title: "checkout fails", Routing: map[string]string{"service": "sk-abcdefgh"}},
			claim:  "Opened by person: checkout fails; service=[redacted:api-keys]",
		},
		"nothing to hide": {
			opened: &item.Opened{Source: "person", Title: "checkout fails", Routing: map[string]string{"service": "checkout"}},
			claim:  "Opened by person: checkout fails; service=checkout",
		},
	} {
		assert.Equal(t, c.claim, triggerClaim(rev, c.opened), name)
	}
}

// counting is the Turns port of TestStartAgainSkipsALaunchingTurn: it counts
// the starts it is asked for, and leaves each launch pending.
type counting struct{ starts int }

func (c *counting) Start(context.Context, *Turn) (bool, error) {
	c.starts++
	return true, nil
}

func (*counting) Stop(board.RunID, string) {}

func (*counting) StopAll() {}

// still is a Clock whose time stands still.
type still struct{ now time.Time }

func (s still) Now() time.Time { return s.now }

func (still) Local(t time.Time) time.Time { return t }

// startAgain skips a turn whose launch is pending (Decision 4): the runner
// has it, and reports its launch through Launched. The test pins a defensive
// skip that no path reaches today, since a launching turn arms no retry; a
// later path that armed one would otherwise launch one agent's turn twice.
// Here a coordinator built by its fields holds a run whose agent's turn
// launches, within its round, and startAgain runs for that agent.
func TestStartAgainSkipsALaunchingTurn(t *testing.T) {
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	runner := &counting{}
	turn := &Turn{ID: "turn-1", Run: "run-1", Agent: "alpha", Round: 1, Deadline: now.Add(time.Minute)}
	c := &Coordinator{p: Ports{Turns: runner, Clock: still{now: now}}, held: true, ctx: t.Context(), runs: map[board.RunID]*running{
		"run-1": {
			item: "item-1", round: 1, opened: now.Add(-time.Minute), deadline: now.Add(time.Minute), startedLate: map[string]bool{},
			turns: map[string]string{}, starting: map[string]*Turn{"alpha": turn}, launching: map[string]string{"alpha": "turn-1"},
			exits: map[string]item.TurnClass{},
		},
	}}

	c.startAgain("run-1", 1, "alpha")

	assert.Zero(t, runner.starts, "the launching turn was started again")
}

// arming is the Timers of the begin tests: it counts the timers armed, and
// runs none.
type arming struct{ arms int }

func (a *arming) At(time.Time, func()) func() {
	a.arms++
	return func() {}
}

// appending is the EventStore of the begin tests: it counts its appends, and
// commits what decide returns on an empty board.
type appending struct{ appends int }

func (*appending) Load(context.Context, board.RunID) ([]board.Event, error) { return nil, nil }

func (a *appending) Append(_ context.Context, run board.RunID, decide func([]board.Event) ([]board.Draft, error)) ([]board.Event, error) {
	a.appends++
	drafts, err := decide(nil)
	if err != nil {
		return nil, err
	}
	committed := make([]board.Event, 0, len(drafts))
	for i := range drafts {
		committed = append(committed, board.Event{Seq: int64(i + 1), ID: drafts[i].ID, Run: run, Round: drafts[i].Round, Author: drafts[i].Author, Body: drafts[i].Body})
	}
	return committed, nil
}

// anyRun is the Runs of the begin tests: every run's board has the default
// convergence settings.
type anyRun struct{}

func (anyRun) BoardContext(_ context.Context, run board.RunID) (board.Context, error) {
	return board.Context{Run: run, Settings: board.Settings{Convergence: board.DefaultConvergence}}, nil
}

// beginning is a coordinator of the begin tests, built by its fields, as
// TestStartAgainSkipsALaunchingTurn builds its own: held, its view of the
// kill switch view, with a run in round 1 whose creation knew the row seen,
// and whose agent's turn is starting. Its runner counts its starts, its clock
// stands still, its timers count their arms, and its boards are over an event
// store that counts its appends.
type beginning struct {
	c      *Coordinator
	r      *running
	turn   *Turn
	runner *counting
	timers *arming
	events *appending
}

func newBeginning(t *testing.T, view *KillSwitch, seen int64) *beginning {
	t.Helper()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	b := &beginning{
		turn:   &Turn{ID: "turn-1", Run: "run-1", Agent: "alpha", Round: 1, Deadline: now.Add(time.Minute)},
		runner: &counting{}, timers: &arming{}, events: &appending{},
	}
	b.r = &running{
		item: "item-1", round: 1, opened: now.Add(-time.Minute), deadline: now.Add(time.Minute), killSwitchSeen: seen, startedLate: map[string]bool{},
		turns: map[string]string{}, starting: map[string]*Turn{"alpha": b.turn}, launching: map[string]string{}, exits: map[string]item.TurnClass{},
	}
	b.c = &Coordinator{
		p: Ports{
			Turns: b.runner, Clock: still{now: now}, Timers: b.timers, Boards: NewBoards(b.events, nil, anyRun{}, &numbered{}),
		},
		opts: Options{Retry: 5 * time.Second}, log: slog.New(slog.DiscardHandler), held: true, ctx: t.Context(), killSwitch: *view,
		runs: map[board.RunID]*running{"run-1": b.r},
	}
	return b
}

// requireBegunNothing fails unless begin started no turn, kept the turn among
// the round's starting turns, armed no timer and posted no notice.
func (b *beginning) requireBegunNothing(t *testing.T) {
	t.Helper()
	assert.Zero(t, b.runner.starts, "no Start")
	assert.Same(t, b.turn, b.r.starting["alpha"], "the turn stays among the round's starting turns")
	assert.Empty(t, b.r.launching, "no launch is pending")
	assert.Zero(t, b.timers.arms, "no timer is armed")
	assert.Zero(t, b.events.appends, "no notice is posted")
}

// begin starts no turn while the kill switch is on (Decision 3, the second
// line): the run is to be halted, so the turn stays among the round's
// starting turns, with no retry armed and no notice posted.
func TestBeginStartsNoTurnWhileTheKillSwitchIsOn(t *testing.T) {
	b := newBeginning(t, &KillSwitch{On: true, Seq: 1, OnSeq: 1}, 0)

	b.c.begin(t.Context(), "run-1", b.r, b.turn)

	b.requireBegunNothing(t)
}

// begin starts no turn of a run that owes a kill (Decision 3, the second
// line), whatever the switch says now: the view is off, its latest on-row 2,
// above the row 1 the run's creation knew.
func TestBeginStartsNoTurnOfARunThatOwesAKill(t *testing.T) {
	b := newBeginning(t, &KillSwitch{Seq: 3, OnSeq: 2}, 1)

	b.c.begin(t.Context(), "run-1", b.r, b.turn)

	b.requireBegunNothing(t)
}

// The rule of the limit and one (Decision 4): one more item is over the limit
// once the items investigating, as the limit counts them, are past the limit.
// Items investigating count, and so do queued items the walk admitted or a
// person promoted, whose start is due; items waiting in the queue, or
// awaiting their close, do not. Here the limit is 1. (The plan's.)
func TestOverLimitIsPastTheLimitAndOne(t *testing.T) {
	investigating := entry{status: item.StatusInvestigating}
	for _, c := range []struct {
		name  string
		items map[item.ID]entry
		over  bool
	}{
		{"none investigating", map[item.ID]entry{}, false},
		{"one investigating, at the limit", map[item.ID]entry{"a": investigating}, false},
		{"one investigating and one admitted", map[item.ID]entry{"a": investigating, "b": {status: item.StatusQueued, admitted: true}}, true},
		{"one investigating and one promoted", map[item.ID]entry{"a": investigating, "b": {status: item.StatusQueued, promoted: true}}, true},
		{"one investigating, one waiting and one awaiting its close", map[item.ID]entry{
			"a": investigating, "b": {status: item.StatusQueued}, "c": {status: item.StatusAwaitingClose},
		}, false},
	} {
		coord := &Coordinator{opts: Options{Limit: 1}, items: c.items}

		assert.Equal(t, c.over, coord.overLimit(), c.name)
	}
}

// oneItem is the Records of TestARecordsIncidentIsTheFoldsInsideTheAppend:
// one item's records, whose Append calls decide with them and commits what it
// returns, a second apart, as the stores do. internal/work/memory imports
// this package, so an internal test cannot use it.
type oneItem struct {
	records []item.Record
	now     time.Time
}

func (o *oneItem) Load(context.Context, item.ID) ([]item.Record, error) {
	return slices.Clone(o.records), nil
}

func (*oneItem) Active(context.Context) ([]item.ID, error) { return nil, nil }

func (*oneItem) UnstaffedBy(context.Context, int64) ([]item.ID, error) { return nil, nil }

func (o *oneItem) Append(_ context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	drafts, err := decide(slices.Clone(o.records))
	if err != nil {
		return nil, err
	}
	committed := make([]item.Record, 0, len(drafts))
	for i := range drafts {
		d := &drafts[i]
		o.now = o.now.Add(time.Second)
		committed = append(committed, item.Record{
			Seq: int64(len(o.records) + i + 1), ID: d.ID, Item: id, Incident: d.Incident, Run: d.Run, At: o.now, Body: d.Body,
		})
	}
	o.records = append(o.records, committed...)
	return committed, nil
}

// numbered is an IDs that numbers the ids it gives.
type numbered struct{ n int }

func (n *numbered) NewID() string {
	n.n++
	return fmt.Sprintf("id-%d", n.n)
}

// A record's incident is the item's as the fold of its history and of the
// records before it in the append has it, inside the append, not the one the
// caller's state gives, which may be stale (Decision 2); each record is
// checked against that fold, so a reopen follows the close before it. record
// returns the item's state after the commit, its times from the committed
// records.
func TestARecordsIncidentIsTheFoldsInsideTheAppend(t *testing.T) {
	opened := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	store := &oneItem{now: opened, records: []item.Record{{
		Seq: 1, ID: "rec-1", Item: "item-1", Incident: "inc-1", At: opened,
		Body: item.Body{V: item.Version, Opened: &item.Opened{Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow}},
	}}}
	c := &Coordinator{p: Ports{Records: store, IDs: &numbered{}}}
	request := item.Request{Key: "sha256:01", Digest: "sha256:02"}

	s, err := c.record(t.Context(), &item.State{ID: "item-1", Incident: "inc-stale"},
		rec{body: item.Body{Closed: &item.Closed{By: "admin", Request: &request}}},
		rec{body: item.Body{Reopened: &item.Reopened{By: "admin", Request: request, AwaitingClose: item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseClosed}}}})

	require.NoError(t, err)
	require.Len(t, store.records, 3)
	for _, r := range store.records[1:] {
		assert.Equal(t, item.IncidentID("inc-1"), r.Incident, "record %s", r.ID)
	}
	require.NotNil(t, s)
	assert.Equal(t, item.IncidentID("inc-1"), s.Incident)
	assert.Equal(t, item.StatusAwaitingClose, s.Status())
	assert.Equal(t, opened, s.OpenedAt)
	assert.Equal(t, store.records[1].At, s.ClosedAt)
	assert.Equal(t, store.records[2].At, s.AwaitingSince)
}
