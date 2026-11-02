package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/control"
)

// Controls is an in-memory app.Controls, the twin of the Postgres store for
// the coordinator's tests (R22): the kill switch's rows, numbered from 1, with
// their times from now. One lock serializes its requests, as the store's lock
// of the tenant does. It checks no holding.
type Controls struct {
	mu    sync.Mutex
	now   func() time.Time
	state control.KillSwitch
}

// NewControls returns controls whose kill switch was never set, and whose
// rows' times come from now.
func NewControls(now func() time.Time) *Controls {
	return &Controls{now: now}
}

// KillSwitch returns the kill switch's state: off, with no row, until it is
// first set.
func (s *Controls) KillSwitch(context.Context) (control.KillSwitch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}

// SetKillSwitch turns the kill switch on or off, as the Postgres store does:
// a request whose state holds already appends nothing, whatever after says;
// one whose after is not the latest row's number appends nothing either, and
// fails with control.ErrStale; any other appends the next row, at now. It
// returns the state after it, and whether it appended a row, whatever it did:
// the state comes with ErrStale too.
func (s *Controls) SetKillSwitch(_ context.Context, on bool, by string, reason board.Reason, after int64) (control.KillSwitch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.state.On == on:
		return s.state, false, nil
	case after != s.state.Seq:
		return s.state, false, fmt.Errorf("the kill switch's latest row is %d, not %d: %w", s.state.Seq, after, control.ErrStale)
	}
	next := control.KillSwitch{On: on, Seq: s.state.Seq + 1, OnSeq: s.state.OnSeq, OffAt: s.state.OffAt, By: by, Reason: reason, Since: s.now().UTC()}
	if on {
		next.OnSeq = next.Seq
	} else {
		next.OffAt = next.Since
	}
	s.state = next
	return next, true, nil
}

// LatestKillSwitch returns the number of the kill switch's latest row, 0
// before the first.
func (s *Controls) LatestKillSwitch(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Seq, nil
}
