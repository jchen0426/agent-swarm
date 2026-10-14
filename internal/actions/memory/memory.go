// Package memory keeps the gateway's sessions, call records and hides in
// memory, for tests and for commands that run in one process. It implements
// the ports of package app, and the Work context's ReadCalls.
package memory

import (
	"context"
	"sync"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	workapp "github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Sessions maps tokens to sessions. It implements app.Sessions.
type Sessions struct {
	mu       sync.Mutex
	sessions map[string]app.Session
	done     map[string]chan struct{}
}

// NewSessions returns no sessions.
func NewSessions() *Sessions {
	return &Sessions{sessions: map[string]app.Session{}, done: map[string]chan struct{}{}}
}

// Put makes token authenticate s, or s's next round.
func (m *Sessions) Put(token string, s *app.Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done[token] == nil {
		m.done[token] = make(chan struct{})
	}
	m.sessions[token] = *s
}

// Stop ends the session of token, and its calls in flight.
func (m *Sessions) Stop(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.done[token]; d != nil {
		close(d)
		delete(m.done, token)
	}
	delete(m.sessions, token)
}

// Session returns the session token authenticates, or app.ErrNoSession.
func (m *Sessions) Session(_ context.Context, token string) (app.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok {
		return app.Session{}, app.ErrNoSession
	}
	s.Done = m.done[token]
	return s, nil
}

// Audit keeps call records and outcomes. It implements app.Audit and the Work
// context's ReadCalls.
type Audit struct {
	mu       sync.Mutex
	records  map[string]app.CallRecord
	outcomes map[string]app.Outcome
	order    []string
}

// NewAudit returns an empty audit store.
func NewAudit() *Audit {
	return &Audit{records: map[string]app.CallRecord{}, outcomes: map[string]app.Outcome{}}
}

// Begin records a call.
func (m *Audit) Begin(_ context.Context, rec *app.CallRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[rec.ID] = *rec
	m.order = append(m.order, rec.ID)
	return nil
}

// End records how a call ended.
func (m *Audit) End(_ context.Context, o *app.Outcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outcomes[o.ID] = *o
	return nil
}

// Record returns a call's record and outcome, and whether each exists.
func (m *Audit) Record(id string) (rec app.CallRecord, o app.Outcome, began, ended bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, began = m.records[id]
	o, ended = m.outcomes[id]
	return rec, o, began, ended
}

// Calls returns the ids of every recorded call, in the order they began.
func (m *Audit) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.order...)
}

// ReadCall returns a call as the board's provenance check reads it. A call
// that has not ended, was refused or ended in a read error has failed: none
// of them can be cited.
func (m *Audit) ReadCall(_ context.Context, id string) (board.ReadCall, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, began := m.records[id]
	if !began {
		return board.ReadCall{}, workapp.ErrCallNotFound
	}
	o, ended := m.outcomes[id]
	call := board.ReadCall{ID: id, Run: rec.Run, Agent: rec.Agent, Server: rec.Server, Tool: rec.Tool, Failed: true}
	if ended && o.Status == app.StatusOK {
		call.Failed, call.Output = false, []byte(o.Output)
	}
	return call, nil
}

// Hides keeps hides. It implements app.Hides.
type Hides struct {
	mu    sync.Mutex
	hides []app.Hide
}

// Hide records a hide once.
func (m *Hides) Hide(_ context.Context, h *app.Hide) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.hides {
		old := &m.hides[i]
		if old.Endpoint == h.Endpoint && old.Tool == h.Tool && old.PinnedHash == h.PinnedHash && old.Registration == h.Registration {
			return nil
		}
	}
	m.hides = append(m.hides, *h)
	return nil
}

// Hidden returns every recorded hide.
func (m *Hides) Hidden(context.Context) ([]app.Hide, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]app.Hide(nil), m.hides...), nil
}
