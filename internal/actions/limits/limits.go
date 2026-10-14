// Package limits keeps each MCP server's limits (spec Section 6.2): how many
// calls run at once, the share that comparison and probation runs may hold,
// the capacity reserved for platform reads, and the degraded mark. While a
// server is degraded, primary runs' calls fail fast, comparison and probation
// runs pause, and platform reads go on.
package limits

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrDegraded is returned for a primary run's call to a degraded server.
var ErrDegraded = errors.New("limits: the server is degraded")

// minSlots is the fewest slots a server has: one reserved for platform
// reads, and one for agents.
const minSlots = 2

// Class says whose call takes a slot.
type Class int

// Classes of calls.
const (
	// Primary is a call by an agent of a primary run, of either arm.
	Primary Class = iota
	// Shadow is a call by an agent of a comparison or probation run.
	Shadow
	// Platform is one of the platform's own reads: candidates, facts,
	// routing and identity.
	Platform
)

// Settings are the limits of spec Section 15.
type Settings struct {
	// Slots is how many calls run at once on one server.
	Slots int
	// Failures is how many consecutive connection failures degrade a server.
	Failures int
	// CoolDown is how long a server stays degraded.
	CoolDown time.Duration
}

// Defaults are the spec's defaults: 10 calls at once, degraded after 3
// consecutive connection failures for 60 s.
var Defaults = Settings{Slots: 10, Failures: 3, CoolDown: 60 * time.Second}

// Limits holds the limits of every server.
type Limits struct {
	settings Settings
	slots    map[string]int
	now      func() time.Time

	mu      sync.Mutex
	servers map[string]*Server
}

// New returns limits with the given settings. slots overrides Slots for the
// servers it names, as a load trial may set them; now is the clock.
func New(settings Settings, slots map[string]int, now func() time.Time) *Limits {
	return &Limits{settings: settings, slots: slots, now: now, servers: map[string]*Server{}}
}

// Server returns the limits of the named server.
func (l *Limits) Server(name string) *Server {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.servers[name]
	if s == nil {
		n := l.settings.Slots
		if v, ok := l.slots[name]; ok {
			n = v
		}
		n = max(n, minSlots)
		s = &Server{
			slots: n, reserved: max(1, n/5), failuresToDegrade: l.settings.Failures, coolDown: l.settings.CoolDown,
			now: l.now, inUse: map[Class]int{}, freed: make(chan struct{}),
		}
		l.servers[name] = s
	}
	return s
}

// Server is one server's limits.
//
// Platform reads may use every slot. Agents may use all but the reserved
// ones: a fifth of the slots, at least one. Comparison and probation runs may
// hold at most half of the slots, within the agents' share.
type Server struct {
	slots             int
	reserved          int
	failuresToDegrade int
	coolDown          time.Duration
	now               func() time.Time

	mu            sync.Mutex
	inUse         map[Class]int
	freed         chan struct{} // closed when a slot is released
	failures      int
	degradedUntil time.Time
}

// Acquire takes a slot for a call of class c, waiting until one is free or
// ctx ends. While the server is degraded, a primary run's call fails at once
// with ErrDegraded, a comparison or probation run's call waits for the
// cool-down to end, and a platform read goes on. Call release when the call
// ends.
func (s *Server) Acquire(ctx context.Context, c Class) (release func(), err error) {
	for {
		s.mu.Lock()
		degraded := s.now().Before(s.degradedUntil)
		if degraded && c == Primary {
			s.mu.Unlock()
			return nil, ErrDegraded
		}
		paused := degraded && c == Shadow
		if !paused && s.free(c) {
			s.inUse[c]++
			s.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { s.release(c) }) }, nil
		}
		freed, wait := s.freed, s.degradedUntil.Sub(s.now())
		s.mu.Unlock()
		if err := s.wait(ctx, freed, paused, wait); err != nil {
			return nil, err
		}
	}
}

// wait waits until a slot is freed, a pause ends or ctx ends.
func (s *Server) wait(ctx context.Context, freed <-chan struct{}, paused bool, pause time.Duration) error {
	var coolDown <-chan time.Time
	if paused {
		t := time.NewTimer(pause)
		defer t.Stop()
		coolDown = t.C
	}
	select {
	case <-freed:
	case <-coolDown:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// free reports whether a call of class c may take a slot now.
func (s *Server) free(c Class) bool {
	total := s.inUse[Primary] + s.inUse[Shadow] + s.inUse[Platform]
	agents := s.inUse[Primary] + s.inUse[Shadow]
	switch c {
	case Platform:
		return total < s.slots
	case Shadow:
		return total < s.slots && agents < s.slots-s.reserved && s.inUse[Shadow] < s.slots/2
	default:
		return total < s.slots && agents < s.slots-s.reserved
	}
}

func (s *Server) release(c Class) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inUse[c]--
	close(s.freed)
	s.freed = make(chan struct{})
}

// Failed records a call that could not reach the server, once per call after
// its retries. Enough of them in a row degrade the server for the cool-down.
func (s *Server) Failed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	if s.failures >= s.failuresToDegrade {
		s.failures = 0
		s.degradedUntil = s.now().Add(s.coolDown)
	}
}

// Succeeded records a call that reached the server, which ends a run of
// failures.
func (s *Server) Succeeded() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = 0
}

// Degraded reports whether the server is degraded now.
func (s *Server) Degraded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now().Before(s.degradedUntil)
}
