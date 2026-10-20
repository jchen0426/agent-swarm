// Package memory holds the LLM gateway's sessions and its record of model
// calls in memory, for tests and development.
package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/llm/prompt"
)

// Sessions maps tokens to sessions. It implements the gateway's Sessions
// port.
type Sessions struct {
	mu       sync.Mutex
	sessions map[string]app.Session
}

// NewSessions returns no sessions.
func NewSessions() *Sessions { return &Sessions{sessions: map[string]app.Session{}} }

// Put starts, or replaces, the session token names.
func (s *Sessions) Put(token string, session *app.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[token] = *session
}

// End ends the session token names.
func (s *Sessions) End(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// Session returns the session token names, or app.ErrNoSession.
func (s *Sessions) Session(_ context.Context, token string) (app.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[token]
	if !ok {
		return app.Session{}, app.ErrNoSession
	}
	return session, nil
}

// Calls records model calls in memory. It implements the gateway's Calls
// port, and fails a call whose context has ended, as a database does. As the
// database does, it records each call and outcome in its own clock's time,
// whatever time they give, and counts the day from its clock's UTC midnight.
type Calls struct {
	mu       sync.Mutex
	now      func() time.Time
	records  []app.CallRecord
	outcomes map[string]app.Outcome
	// fail fails every method, and failEnd End only, when set. failCommitted
	// fails Begin after it has recorded the call, as a commit whose answer
	// is lost does.
	fail, failEnd, failCommitted error
	// hold, when set, holds Begin until it is closed; held counts the Begin
	// calls it holds.
	hold chan struct{}
	held int
}

// NewCalls returns an empty record, on the process's clock.
func NewCalls() *Calls { return &Calls{now: time.Now, outcomes: map[string]app.Outcome{}} }

// SetClock makes now the record's clock.
func (c *Calls) SetClock(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// SetFail makes every method fail with err, or none when err is nil.
func (c *Calls) SetFail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = err
}

// SetFailEnd makes End fail with err, or not when err is nil.
func (c *Calls) SetFailEnd(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failEnd = err
}

// SetFailCommitted makes Begin record the call and then fail with err, as a
// commit whose answer is lost does, or not when err is nil.
func (c *Calls) SetFailCommitted(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failCommitted = err
}

// HoldBegins holds every Begin until release is called, or its context
// ends.
func (c *Calls) HoldBegins() (release func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hold := make(chan struct{})
	c.hold = hold
	return func() {
		c.mu.Lock()
		c.hold = nil
		c.mu.Unlock()
		close(hold)
	}
}

// Held returns how many Begin calls are held.
func (c *Calls) Held() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held
}

// Begin records a call.
func (c *Calls) Begin(ctx context.Context, rec *app.CallRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hold := c.hold; hold != nil {
		c.held++
		c.mu.Unlock()
		select {
		case <-hold:
		case <-ctx.Done():
		}
		c.mu.Lock()
		c.held--
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.fail != nil {
		return c.fail
	}
	for i := range c.records {
		if c.records[i].ID == rec.ID {
			return errors.New("memory: the call is recorded already")
		}
	}
	recorded := *rec
	recorded.At = c.now()
	c.records = append(c.records, recorded)
	return c.failCommitted
}

// End records how a call ended. The same outcome again changes nothing; a
// different one is refused, wrapping app.ErrConflict.
func (c *Calls) End(ctx context.Context, o *app.Outcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != nil {
		return c.fail
	}
	if c.failEnd != nil {
		return c.failEnd
	}
	if prev, ok := c.outcomes[o.ID]; ok {
		if prev.Status != o.Status || prev.Reason != o.Reason || prev.Weighted != o.Weighted || prev.Completion != o.Completion {
			return fmt.Errorf("memory: %s: %w", o.ID, app.ErrConflict)
		}
		return nil
	}
	recorded := *o
	recorded.At = c.now()
	c.outcomes[o.ID] = recorded
	return nil
}

// Spent returns the weighted tokens of a run's calls: each at its outcome's
// cost once it has one, and at its estimate until then.
func (c *Calls) Spent(ctx context.Context, run string) (int64, error) {
	return c.sum(ctx, func(rec *app.CallRecord) bool { return rec.Run == run })
}

// SpentToday returns the weighted tokens of the calls recorded since the
// clock's last UTC midnight, counted as Spent counts them.
func (c *Calls) SpentToday(ctx context.Context) (int64, error) {
	c.mu.Lock()
	day := cost.DayStart(c.now())
	c.mu.Unlock()
	return c.sum(ctx, func(rec *app.CallRecord) bool { return !rec.At.Before(day) })
}

// Unsettled returns the calls that have no outcome and were recorded before
// before, by the record's clock, in the order they were recorded, with the
// max_tokens their requests' records hold.
func (c *Calls) Unsettled(ctx context.Context, before time.Time) ([]app.Unsettled, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != nil {
		return nil, c.fail
	}
	var out []app.Unsettled
	for i := range c.records {
		rec := &c.records[i]
		if _, ended := c.outcomes[rec.ID]; ended || !rec.At.Before(before) {
			continue
		}
		out = append(out, app.Unsettled{
			ID: rec.ID, Model: rec.Model, Estimate: rec.Estimate, RequestBytes: rec.RequestBytes, MaxTokens: prompt.RecordedMaxTokens(rec.Request),
		})
	}
	return out, nil
}

// sum counts the calls counts takes, with the record's lock held.
func (c *Calls) sum(ctx context.Context, counts func(*app.CallRecord) bool) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != nil {
		return 0, c.fail
	}
	var n int64
	for i := range c.records {
		if !counts(&c.records[i]) {
			continue
		}
		if o, ok := c.outcomes[c.records[i].ID]; ok {
			n += o.Weighted
		} else {
			n += c.records[i].Estimate
		}
	}
	return n, nil
}

// Record returns a call's record and its outcome, if any.
func (c *Calls) Record(id string) (rec app.CallRecord, o app.Outcome, began, ended bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.records {
		if c.records[i].ID == id {
			rec, began = c.records[i], true
		}
	}
	o, ended = c.outcomes[id]
	return rec, o, began, ended
}

// Records returns every call recorded, in order.
func (c *Calls) Records() []app.CallRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]app.CallRecord(nil), c.records...)
}
