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

func newLife(t *testing.T, rc *item.RunCreated) *life {
	t.Helper()
	l := &life{t: t, s: &item.State{}, now: t0}
	l.write("", item.Body{Opened: &item.Opened{Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow}})
	l.write("", item.Body{Started: &item.Started{Commit: "c1", Digest: "d1", Images: map[string]string{"thinloop": "sha256:aa"}}})
	if rc != nil {
		l.write("run-1", item.Body{RunCreated: rc})
	}
	return l
}

// write commits one record of the item, as the store does: the next
// sequence number and a later time.
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

// busy is a round that ends by its turns, with entries on the board and no
// hypothesis converged.
func busy() *item.Ending {
	return &item.Ending{EndedBy: item.EndedByTurns, Turns: all(item.TurnEnded, release, runtime), DailyCap: 1_000_000, Settled: true,
		Report: board.Report{Facts: board.RoundFacts{Ended: []string{release, runtime}, Entries: 3}}}
}

// quiet is a round that ends by its turns with nothing on the board.
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
