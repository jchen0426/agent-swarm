package item_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

var t0 = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

const (
	release = "release-investigator"
	runtime = "runtime-investigator"
	hyp     = board.EntryID("hyp-1")
	other   = board.EntryID("hyp-2")
)

// created is a run's creation as the coordinator records it: 3 rounds of at
// most 2 minutes, 10 minutes of wall clock and 90,000 tokens, two agents.
func created() *item.RunCreated {
	return &item.RunCreated{
		Class: item.ClassPrimary, Mode: item.ModeVisible, Settings: board.Settings{Convergence: board.DefaultConvergence},
		Budget:     item.Budget{WallClock: 10 * time.Minute, Tokens: 90_000, Rounds: 3},
		RoundLimit: 2 * time.Minute, Agents: []string{release, runtime},
	}
}

// life drives an item through the package's own rules, record by record, as
// the coordinator does, and keeps the records it wrote.
type life struct {
	t       *testing.T
	s       *item.State
	records []item.Record
	now     time.Time
}

// newLife is a life whose item opened and started, and, unless rc is nil,
// created its run as rc says.
func newLife(t *testing.T, rc *item.RunCreated) *life {
	t.Helper()
	l := newOpened(t)
	l.start()
	if rc != nil {
		l.write("run-1", item.Body{RunCreated: rc})
	}
	return l
}

// newOpened is a life whose item a person opened, at t0, and that waits in
// the queue.
func newOpened(t *testing.T) *life {
	t.Helper()
	l := &life{t: t, s: &item.State{}, now: t0}
	l.write("", item.Body{Opened: &item.Opened{Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow}})
	return l
}

// start starts the item: it locks its configuration.
func (l *life) start() {
	l.t.Helper()
	l.write("", item.Body{Started: &item.Started{Commit: "c1", Digest: "d1", Images: map[string]string{"thinloop": "sha256:aa"}}})
}

// write commits one record of the item, as the store does: the next
// sequence number and a later time.
//
//nolint:gocritic // hugeParam: a test builds each body as a value, and write sets its copy's version.
func (l *life) write(run board.RunID, body item.Body) item.Record {
	l.t.Helper()
	body.V = item.Version
	r := item.Record{Seq: int64(len(l.records) + 1), ID: fmt.Sprintf("rec-%d", len(l.records)+1), Item: "item-1", Incident: "inc-1", Run: run, At: l.now, Body: body}
	require.NoError(l.t, l.s.Apply(&r))
	l.records = append(l.records, r)
	return r
}

// try applies one more record of the item, and returns its refusal; a record
// applied is kept.
//
//nolint:gocritic // hugeParam: a test builds each body as a value, and try sets its copy's version.
func (l *life) try(run board.RunID, body item.Body) error {
	body.V = item.Version
	r := item.Record{Seq: int64(len(l.records) + 1), ID: fmt.Sprintf("rec-%d", len(l.records)+1), Item: "item-1", Incident: "inc-1", Run: run, At: l.now, Body: body}
	if err := l.s.Apply(&r); err != nil {
		return err
	}
	l.records = append(l.records, r)
	return nil
}

// open opens the run's next round as decided, at the life's time.
func (l *life) open() item.RoundOpened {
	l.t.Helper()
	o := l.s.Run.Open(l.now)
	l.write("run-1", item.Body{RoundOpened: &o})
	return o
}

// end ends the open round after d, as e says, and records the decision.
func (l *life) end(d time.Duration, e *item.Ending) item.RoundEnded {
	l.t.Helper()
	l.now = l.now.Add(d)
	if e.EndedAt.IsZero() {
		e.EndedAt = l.now
	}
	re := l.s.Run.End(e)
	l.write("run-1", item.Body{RoundEnded: &re})
	return re
}

// all gives each of the run's agents in the round the same turn class.
func all(class item.TurnClass, agents ...string) map[string]item.TurnClass {
	turns := map[string]item.TurnClass{}
	for _, a := range agents {
		turns[a] = class
	}
	return turns
}

// busy is a round that ends by its turns, which both agents ended on the
// board as the runner reports them, with entries on the board, no failure and
// no hypothesis converged.
func busy() *item.Ending {
	return &item.Ending{EndedBy: item.EndedByTurns, Turns: all(item.TurnEnded, release, runtime), TurnEnds: []string{release, runtime},
		DailyCap: 1_000_000, Settled: true, Report: board.Report{Facts: board.RoundFacts{Entries: 3}}}
}

// quiet is a round that ends by its turns with nothing on the board but its
// agents' turn ends.
func quiet() *item.Ending {
	e := busy()
	e.Report.Facts.Entries = 0
	return e
}

// converging is busy with h converged.
func converging(h ...board.EntryID) *item.Ending {
	e := busy()
	for _, id := range h {
		e.Report.Assessment.Hypotheses = append(e.Report.Assessment.Hypotheses, board.HypothesisView{ID: id, Converged: true})
	}
	return e
}

// notStaffed has the item await its close not staffed, for cause.
func notStaffed(cause item.Cause) *item.AwaitingClose {
	return &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: cause}
}

// promoted is a person's promotion of the item, as its record holds it.
func promoted() *item.Promoted {
	return &item.Promoted{By: "admin", Request: item.Request{Key: "sha256:01", Digest: "sha256:02"}}
}

// closedBy is a person's close of the item, as its record holds it.
func closedBy() *item.Closed {
	return &item.Closed{By: "admin", Request: &item.Request{Key: "sha256:03", Digest: "sha256:04"}}
}

// closedAuto is the item's close by itself, due at due.
func closedAuto(due time.Time) *item.Closed {
	return &item.Closed{Auto: true, Due: due}
}

// reopened is a person's reopen of the item, which returns it to awaiting its
// close as ac says.
func reopened(ac *item.AwaitingClose) *item.Reopened {
	return &item.Reopened{By: "admin", Request: item.Request{Key: "sha256:05", Digest: "sha256:06"}, AwaitingClose: *ac}
}
