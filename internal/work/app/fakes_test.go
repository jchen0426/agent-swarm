package app_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// memStore is an in-memory EventStore. It serializes appends with one lock,
// as the Postgres store does per run, and says when it holds it.
type memStore struct {
	mu      sync.Mutex
	events  map[board.RunID][]board.Event
	now     time.Time
	locking atomic.Bool
	// locked, when set, runs once the store holds a run's lock, before
	// decide: as another writer that took the lock first.
	locked func(run board.RunID)
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
	m.locking.Store(true)
	defer m.locking.Store(false)
	if m.locked != nil {
		m.locked(run)
	}
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

// unlockedRuns is a Runs that refuses to be read while the store holds a
// run's lock: on one database pool, a read there would wait for a
// connection that the posts waiting for the lock hold.
type unlockedRuns struct {
	memRuns
	store *memStore
}

func (r unlockedRuns) BoardContext(ctx context.Context, run board.RunID) (board.Context, error) {
	if r.store.locking.Load() {
		return board.Context{}, errors.New("the run was read while its board was locked")
	}
	return r.memRuns.BoardContext(ctx, run)
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
