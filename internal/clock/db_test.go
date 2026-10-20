package clock_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/clock"
)

// process is a process clock moved by hand.
type process struct {
	mu  sync.Mutex
	now time.Time
}

func newProcess() *process {
	return &process{now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
}

func (p *process) Now() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.now
}

func (p *process) Advance(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = p.now.Add(d)
}

// database is the database's clock as a sample reads it: offset from the
// process's clock, and read halfway through a round trip of rtt. On a process
// clock moved by hand, the round trip moves it; on the real one (p nil), it
// is slept, and the time read carries no monotonic reading, as the
// database's does not. A sample fails with err, when it is set.
type database struct {
	p      *process
	mu     sync.Mutex
	offset time.Duration
	rtt    time.Duration
	err    error
}

// set makes the next samples take rtt, and read the database offset from the
// process's clock.
func (d *database) set(rtt, offset time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rtt, d.offset = rtt, offset
}

// fail makes the next samples fail with err, or succeed when it is nil.
func (d *database) fail(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.err = err
}

func (d *database) Sample(context.Context) (time.Time, error) {
	d.mu.Lock()
	rtt, offset, err := d.rtt, d.offset, d.err
	d.mu.Unlock()
	if err != nil {
		return time.Time{}, err
	}
	if d.p == nil {
		time.Sleep(rtt / 2)
		at := time.Now().Add(offset).Round(0)
		time.Sleep(rtt - rtt/2)
		return at, nil
	}
	d.p.Advance(rtt / 2)
	at := d.p.Now().Add(offset)
	d.p.Advance(rtt - rtt/2)
	return at, nil
}

// The database's clock takes its offset from the sample with the shortest
// round trip within the last minute, whose error is the smallest: a sample
// of 50 ms gives way to one of 5 ms, which a later one of 40 ms does not
// replace. Once a minute has passed, a new sample replaces it whatever its
// round trip.
func TestTheDatabaseClockKeepsTheSampleWithTheShortestRoundTrip(t *testing.T) {
	p := newProcess()
	db := &database{p: p}
	c := clock.NewDB(db, p.Now)

	db.set(50*time.Millisecond, time.Second)
	require.NoError(t, c.Sample(t.Context()))
	db.set(5*time.Millisecond, 2*time.Second)
	require.NoError(t, c.Sample(t.Context()))
	db.set(40*time.Millisecond, 5*time.Second)
	require.NoError(t, c.Sample(t.Context()))
	shortest := c.Now().Sub(p.Now())
	p.Advance(time.Minute)
	db.set(80*time.Millisecond, 3*time.Second)
	require.NoError(t, c.Sample(t.Context()))
	later := c.Now().Sub(p.Now())

	assert.Equal(t, 2*time.Second, shortest, "the offset is the 5 ms sample's")
	assert.Equal(t, 3*time.Second, later, "a minute later, the new sample's, whatever its round trip")
}

// A correction back holds the database's clock where it was until the
// process's time catches up: Now never goes back.
func TestTheDatabaseClockNeverGoesBack(t *testing.T) {
	p := newProcess()
	db := &database{p: p}
	c := clock.NewDB(db, p.Now)
	db.set(10*time.Millisecond, 2*time.Second)
	require.NoError(t, c.Sample(t.Context()))
	before := c.Now()

	db.set(time.Millisecond, 0) // a shorter round trip: a correction of -2 s
	require.NoError(t, c.Sample(t.Context()))
	last := before
	for range 1000 {
		p.Advance(time.Millisecond)
		now := c.Now()
		require.False(t, now.Before(last), "Now went back")
		last = now
	}
	held := c.Now()
	p.Advance(time.Second)
	moved := c.Now()

	assert.Equal(t, before, held, "held until the process's time catches up")
	assert.True(t, moved.After(before), "Now moves again once the process's time passes the old reading")
}

// Readings of the database's clock before its first good sample are the
// process's time, which says nothing of the database's, and do not count: the
// first good sample sets the clock where the database is, though that is
// behind them. From there on, Now never goes back.
func TestTheDatabaseClockCountsNoReadingBeforeItsFirstGoodSample(t *testing.T) {
	p := newProcess()
	db := &database{p: p}
	c := clock.NewDB(db, p.Now)
	early := c.Now()
	db.fail(errors.New("the database does not answer"))
	require.Error(t, c.Sample(t.Context()))
	unsampled := c.Now()

	db.fail(nil)
	db.set(10*time.Millisecond, -2*time.Second)
	require.NoError(t, c.Sample(t.Context()))
	first, process := c.Now(), p.Now()
	db.set(time.Millisecond, -3*time.Second) // a shorter round trip: a correction of -1 s
	require.NoError(t, c.Sample(t.Context()))
	held := c.Now()

	assert.Equal(t, early, unsampled, "a failed sample sets nothing")
	assert.Equal(t, -2*time.Second, first.Sub(process), "the first good sample sets the clock 2 s behind the readings before it")
	assert.Equal(t, first, held, "from there on, Now never goes back")
}

// Local gives a time of the database's in the process's clock, with a
// monotonic reading, in which the gateways compare deadlines: whatever the
// offset, five seconds ahead in the database's time is five seconds ahead in
// the process's.
func TestLocalCarriesAMonotonicReading(t *testing.T) {
	db := &database{}
	db.set(0, time.Hour)
	c := clock.NewDB(db, nil)
	require.NoError(t, c.Sample(t.Context()))

	local := c.Local(c.Now().Add(5 * time.Second))

	assert.WithinDuration(t, time.Now().Add(5*time.Second), local, 100*time.Millisecond)
	assert.Contains(t, local.String(), " m=", "it carries a monotonic reading")
}

// A timer armed on the database's clock waits the time it was armed for,
// although a sample in between moves the clock's offset: no correction is
// retroactive.
func TestATimerArmedBeforeACorrectionKeepsItsOffset(t *testing.T) {
	db := &database{}
	db.set(20*time.Millisecond, 0)
	c := clock.NewDB(db, nil)
	require.NoError(t, c.Sample(t.Context()))
	timers := clock.NewTimers(c)
	fired := make(chan time.Time, 1)
	armed := time.Now()
	timers.At(c.Now().Add(time.Second), func() { fired <- time.Now() })

	db.set(0, 500*time.Millisecond) // a shorter round trip: the database is 500 ms ahead
	require.NoError(t, c.Sample(t.Context()))
	moved := c.Now()

	assert.WithinDuration(t, time.Now().Add(500*time.Millisecond), moved, 50*time.Millisecond, "the sample moved the offset")
	select {
	case at := <-fired:
		// A timer that took the correction would fire half a second early;
		// a busy machine may make one late.
		assert.WithinRange(t, at, armed.Add(950*time.Millisecond), armed.Add(1500*time.Millisecond), "the timer kept its offset")
	case <-time.After(3 * time.Second):
		t.Fatal("the timer never fired")
	}
}
