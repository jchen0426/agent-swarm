// Package clock holds the platform's production clocks and timers (Plan 7d-1,
// Decisions 2 and 3): the database's time as this process estimates it, by
// which the coordinator judges records and deadlines; the process's monotonic
// clock, by which the leader watches its holding; and timers on either clock,
// whose functions run on goroutines of their own.
//
// The types have the shapes of the coordinator's ports, app.Clock and
// app.Timers, without importing them.
package clock

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Sampler reads the database's time.
type Sampler interface {
	Sample(ctx context.Context) (time.Time, error)
}

// window is how long a sample counts: the clock's offset is that of the
// sample with the shortest round trip within it.
const window = time.Minute

// DB is the database's time as this process estimates it: app.Clock for
// the coordinator. It is the process's time plus an offset, which comes from
// the samples it takes: of those within the last minute, the one with the
// shortest round trip, whose error is the smallest. Between samples it
// follows the process's wall clock, so a step of that clock moves it until
// the next sample. Before its first good sample it reads the process's time,
// and those readings do not count: from the first good sample on, its
// readings never go back. They carry no monotonic reading, as the database's
// time has none.
type DB struct {
	s   Sampler
	now func() time.Time

	mu      sync.Mutex
	samples []sample
	offset  time.Duration
	// sampled says a sample succeeded. last is the latest reading of Now
	// since the first that did, which no later one goes below.
	sampled bool
	last    time.Time
}

// sample is a reading of the database's clock: the moment halfway through
// its round trip, in the process's clock; the round trip; and the database's
// time less that moment.
type sample struct {
	at     time.Time
	rtt    time.Duration
	offset time.Duration
}

// NewDB takes the process clock; nil means time.Now.
func NewDB(s Sampler, now func() time.Time) *DB {
	if now == nil {
		now = time.Now
	}
	return &DB{s: s, now: now}
}

// Sample reads the database's time, and takes its offset from the sample with
// the shortest round trip within the last minute: a sample older than that
// counts no more, whatever its round trip. A sample that fails changes
// nothing. The first good sample sets the clock where the database is, behind
// the readings before it or not. A correction applies from here on: timers
// armed, and deadlines given in the process's clock (Local), before it keep
// their offset.
func (c *DB) Sample(ctx context.Context) error {
	start := c.now()
	at, err := c.s.Sample(ctx)
	end := c.now()
	if err != nil {
		return err
	}
	rtt := end.Sub(start)
	mid := start.Add(rtt / 2)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.sampled {
		c.sampled, c.last = true, time.Time{}
	}
	c.samples = slices.DeleteFunc(c.samples, func(s sample) bool { return end.Sub(s.at) >= window })
	c.samples = append(c.samples, sample{at: mid, rtt: rtt, offset: at.Sub(mid)})
	best := c.samples[0]
	for _, s := range c.samples[1:] {
		if s.rtt <= best.rtt {
			best = s
		}
	}
	c.offset = best.offset
	return nil
}

// Now returns the database's time as this process estimates it. From the
// first good sample on, it never goes back: after a correction back, it holds
// its last reading until the process's time catches up.
func (c *DB) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().Add(c.offset).Round(0)
	if now.Before(c.last) {
		return c.last
	}
	c.last = now
	return now
}

// Local returns t, a time of the database's, in the process's clock, with
// the monotonic reading the process's time carries: the gateways compare
// deadlines in it, so that neither a later correction of the offset nor a
// step of the wall clock moves them.
func (c *DB) Local(t time.Time) time.Time {
	return c.now().Add(t.Sub(c.Now()))
}

// Monotonic is the leader's app.Clock: the process's clock, whose readings
// carry its monotonic time, so that the leader's watch depends on no offset,
// and on no step of the wall clock. Darwin's monotonic clock stops while the
// host sleeps; the holding's check measures wall time as well (app.Holding).
type Monotonic struct{}

// Now returns the process's time, with its monotonic reading.
func (Monotonic) Now() time.Time { return time.Now() }

// Local returns t itself: the leader's times are the process's.
func (Monotonic) Local(t time.Time) time.Time { return t }
