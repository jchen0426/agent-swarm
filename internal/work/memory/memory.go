// Package memory keeps boards, read calls and runs in memory, for a command
// that runs boards from start to finish in one process, such as the
// retrospective check. It implements the ports of package app.
package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Store is an in-memory app.EventStore. One lock serializes appends, and
// commit times never go backwards within a run, as in the Postgres store.
type Store struct {
	mu     sync.Mutex
	now    func() time.Time
	events map[board.RunID][]board.Event
}

// NewStore returns an empty store whose commit times come from now.
func NewStore(now func() time.Time) *Store {
	return &Store{now: now, events: map[board.RunID][]board.Event{}}
}

// Load returns a run's events in commit order. A run that holds an event of a
// later version fails wrapped with board.ErrVersion, as in the Postgres store.
func (s *Store) Load(_ context.Context, run board.RunID) ([]board.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := readable(run, s.events[run]); err != nil {
		return nil, err
	}
	return append([]board.Event(nil), s.events[run]...), nil
}

// Append commits the entries that decide returns, after the run's last event.
// It reads the run's events as Load does, so that it fails as Load does
// before decide sees them.
func (s *Store) Append(_ context.Context, run board.RunID, decide func([]board.Event) ([]board.Draft, error)) ([]board.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.events[run]
	if err := readable(run, history); err != nil {
		return nil, err
	}
	drafts, err := decide(append([]board.Event(nil), history...))
	if err != nil {
		return nil, err
	}
	var last time.Time
	if len(history) > 0 {
		last = history[len(history)-1].At
	}
	committed := make([]board.Event, 0, len(drafts))
	for i := range drafts {
		d := &drafts[i]
		at := s.now().UTC()
		if !at.After(last) {
			at = last.Add(time.Microsecond)
		}
		last = at
		committed = append(committed, board.Event{
			Seq: int64(len(history) + len(committed) + 1), ID: d.ID, Run: run, Round: d.Round,
			Author: d.Author, At: at, Body: d.Body,
		})
	}
	s.events[run] = append(history, committed...)
	return committed, nil
}

// readable refuses a run's events when one is of a later version.
func readable(run board.RunID, events []board.Event) error {
	for i := range events {
		if err := events[i].CheckVersion(); err != nil {
			return fmt.Errorf("read board events of run %s: %w", run, err)
		}
	}
	return nil
}

// Calls is an in-memory app.ReadCalls. It keeps the calls in the order they
// were added.
type Calls struct {
	mu    sync.Mutex
	calls []board.ReadCall
	index map[string]int
}

// NewCalls returns an empty set of read calls.
func NewCalls() *Calls { return &Calls{index: map[string]int{}} }

// Add stores a read call under its id, in place of any call with that id.
func (c *Calls) Add(call *board.ReadCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i, ok := c.index[call.ID]; ok {
		c.calls[i] = *call
		return
	}
	c.index[call.ID] = len(c.calls)
	c.calls = append(c.calls, *call)
}

// ReadCall returns the call with the given id, or app.ErrCallNotFound.
func (c *Calls) ReadCall(_ context.Context, id string) (board.ReadCall, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, ok := c.index[id]
	if !ok {
		return board.ReadCall{}, app.ErrCallNotFound
	}
	return c.calls[i], nil
}

// All returns every call, in the order the calls were added.
func (c *Calls) All() []board.ReadCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]board.ReadCall(nil), c.calls...)
}

// Runs is an in-memory app.Runs.
type Runs struct {
	mu   sync.Mutex
	runs map[board.RunID]board.Context
}

// NewRuns returns no runs.
func NewRuns() *Runs { return &Runs{runs: map[board.RunID]board.Context{}} }

// Add registers a run's board context.
func (r *Runs) Add(bc *board.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[bc.Run] = *bc
}

// BoardContext returns a run's board context.
func (r *Runs) BoardContext(_ context.Context, run board.RunID) (board.Context, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	bc, ok := r.runs[run]
	if !ok {
		return board.Context{}, fmt.Errorf("no run %s", run)
	}
	return bc, nil
}
