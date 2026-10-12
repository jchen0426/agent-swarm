package app_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// memStore is an in-memory EventStore. It serializes appends with one lock,
// as the Postgres store does per run.
type memStore struct {
	mu     sync.Mutex
	events map[board.RunID][]board.Event
	now    time.Time
}

func newMemStore() *memStore {
	return &memStore{events: make(map[board.RunID][]board.Event), now: time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)}
}

func (m *memStore) Load(_ context.Context, run board.RunID) ([]board.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]board.Event(nil), m.events[run]...), nil
}

func (m *memStore) Append(_ context.Context, run board.RunID, decide func([]board.Event) ([]board.Draft, error)) ([]board.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	history := m.events[run]
	drafts, err := decide(append([]board.Event(nil), history...))
	if err != nil {
		return nil, err
	}
	committed := make([]board.Event, 0, len(drafts))
	for i := range drafts {
		d := &drafts[i]
		m.now = m.now.Add(time.Second)
		committed = append(committed, board.Event{
			Seq: int64(len(history) + len(committed) + 1), ID: d.ID, Run: run, Round: d.Round,
			Author: d.Author, At: m.now, Body: d.Body,
		})
	}
	m.events[run] = append(history, committed...)
	return committed, nil
}

// memCalls is an in-memory ReadCalls.
type memCalls map[string]board.ReadCall

func (m memCalls) ReadCall(_ context.Context, id string) (board.ReadCall, error) {
	c, ok := m[id]
	if !ok {
		return board.ReadCall{}, app.ErrCallNotFound
	}
	return c, nil
}

// memRuns is an in-memory Runs.
type memRuns map[board.RunID]board.Context

func (m memRuns) BoardContext(_ context.Context, run board.RunID) (board.Context, error) {
	c, ok := m[run]
	if !ok {
		return board.Context{}, fmt.Errorf("no run %s", run)
	}
	return c, nil
}

// seqIDs issues readable sequential ids.
type seqIDs struct {
	mu sync.Mutex
	n  int
}

func (s *seqIDs) NewID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return fmt.Sprintf("e%d", s.n)
}
