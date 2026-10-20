package app

import (
	"sync/atomic"
	"time"
)

// Holding is the leader's account of its holding, which anyone may read
// without a lock. It implements holding.Holding: the runner and the gateways
// ask it before each turn's start and each request to the model vendor or to
// an MCP server (Decision 4). Serve makes it before the leader, so that it can
// hand it to them before the coordinator and the leader exist (R4); one
// leader publishes into it.
type Holding struct {
	ttl, margin time.Duration
	wall        func() time.Time
	snap        atomic.Pointer[snapshot]
}

// snapshot is what the leader publishes of its holding: its epoch, whether
// it holds the lease and whether it drains, and when the last good renewal,
// or the take, was sent, by the leader's clock, which the snapshot carries,
// and by the wall clock.
type snapshot struct {
	epoch    int64
	held     bool
	draining bool
	clock    Clock
	mono     time.Time
	wall     time.Time
}

// NewHolding takes the lease's TTL and margin, and the wall clock; nil means
// time.Now().Round(0).
func NewHolding(ttl, margin time.Duration, wall func() time.Time) *Holding {
	if wall == nil {
		wall = func() time.Time { return time.Now().Round(0) }
	}
	return &Holding{ttl: ttl, margin: margin, wall: wall}
}

// Valid reports whether the holding of epoch holds: it is the leader's
// current one, the leader holds the lease, and the last good renewal, or the
// take, was sent less than the lease's TTL less the margin ago, by the larger
// of the times elapsed on the leader's clock and on the wall clock. The
// leader's clock is monotonic; Darwin's stops while the host sleeps, and the
// wall clock runs on. Valid reads the leader's snapshot, and takes no lock.
func (h *Holding) Valid(epoch int64) bool {
	s := h.snap.Load()
	if s == nil || !s.held || s.epoch != epoch {
		return false
	}
	elapsed := max(s.clock.Now().Sub(s.mono), h.wall().Sub(s.wall))
	return elapsed < h.ttl-h.margin
}

// publish replaces the snapshot.
func (h *Holding) publish(s *snapshot) { h.snap.Store(s) }

// status returns the snapshot's epoch, and whether the leader holds the
// lease and drains.
func (h *Holding) status() (epoch int64, held, draining bool) {
	s := h.snap.Load()
	if s == nil {
		return 0, false, false
	}
	return s.epoch, s.held, s.draining
}
