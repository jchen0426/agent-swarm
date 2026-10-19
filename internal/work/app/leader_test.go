package app_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// lease is the Lease port in memory: another instance holds it while held
// is set; renew, when set, is what a renewal returns. A renewal takes slow
// on the clock; hook, when set, runs while a renewal is in flight, and says
// what it returns. A take waits until taking, when set, is closed, once it
// said so on waiting.
type lease struct {
	mu       sync.Mutex
	clock    *clock
	held     bool
	mine     bool
	epoch    int64
	renew    error
	slow     time.Duration
	hook     func() error
	taking   chan struct{}
	waiting  chan struct{}
	takes    int
	renewals int
	releases int
}

func (l *lease) Take(context.Context) (app.Term, bool, error) {
	l.mu.Lock()
	if l.taking != nil {
		taking := l.taking
		l.mu.Unlock()
		l.waiting <- struct{}{}
		<-taking
		l.mu.Lock()
	}
	defer l.mu.Unlock()
	l.takes++
	if l.held {
		return app.Term{}, false, nil
	}
	l.held, l.mine = true, true
	l.epoch++
	return app.Term{Epoch: l.epoch, At: l.clock.Now()}, true, nil
}

func (l *lease) Renew(context.Context) error {
	l.mu.Lock()
	slow, hook := l.slow, l.hook
	l.mu.Unlock()
	if slow > 0 {
		l.clock.Advance(slow)
	}
	if hook != nil {
		return hook()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.renewals++
	if l.renew != nil {
		if errors.Is(l.renew, app.ErrLeaseLost) {
			l.mine = false
		}
		return l.renew
	}
	return nil
}

func (l *lease) Release(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releases++
	if l.mine {
		l.held, l.mine = false, false
	}
	return nil
}

func (l *lease) TTL() time.Duration { return leaseTTL }

func (l *lease) set(f func(l *lease)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f(l)
}

// leaderOptions renew every 5 s the lease of 15 s, fence 5 s before it
// would expire, and try to take it every 5 s.
var leaderOptions = app.LeaderOptions{RenewEvery: 5 * time.Second, Margin: 5 * time.Second, TakeEvery: 5 * time.Second}

const leaseTTL = 15 * time.Second

// unled is a harness whose instance has not taken over: a leader takes
// over for it.
func unled(t *testing.T) (*harness, *lease, *app.Leader) {
	t.Helper()
	h := newHarness(t)
	h.crash()
	var err error
	h.c, err = app.NewCoordinator(h.ports(), &h.opts)
	require.NoError(t, err)
	l := &lease{clock: h.clock}
	leader, err := app.NewLeader(l, h.c, h.clock, h.clock, &leaderOptions)
	require.NoError(t, err)
	return h, l, leader
}

func opens(h *harness) error {
	_, err := h.c.Open(h.t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
	return err
}

// A leader takes the lease when it is free, and its coordinator takes over;
// it renews the lease while it holds it.
func TestALeaderTakesTheLeaseAndItsCoordinatorTakesOver(t *testing.T) {
	h, l, leader := unled(t)
	l.set(func(l *lease) { l.held = true })

	leader.Lead(t.Context())
	require.ErrorIs(t, opens(h), app.ErrNotHeld, "another instance holds the lease")
	l.set(func(l *lease) { l.held = false })
	h.clock.Advance(leaderOptions.TakeEvery)
	require.NoError(t, opens(h))
	h.clock.Advance(3 * leaderOptions.RenewEvery)

	assert.Equal(t, 2, l.takes)
	assert.Equal(t, 3, l.renewals)
	require.NoError(t, opens(h), "it still holds the lease")
}

// A renewal that finds the lease lost fences the coordinator at once; the
// leader then tries to take the lease again.
func TestALostRenewalFencesTheCoordinator(t *testing.T) {
	h, l, leader := unled(t)
	leader.Lead(t.Context())
	require.NoError(t, opens(h))
	l.set(func(l *lease) { l.renew, l.held = fmt.Errorf("renew: %w", app.ErrLeaseLost), true })
	stops := h.turns.all

	h.clock.Advance(leaderOptions.RenewEvery)
	fenced := opens(h)
	stopped := h.turns.all
	l.set(func(l *lease) { l.held, l.renew = false, nil })
	h.clock.Advance(leaderOptions.TakeEvery)

	require.ErrorIs(t, fenced, app.ErrNotHeld)
	assert.Equal(t, stops+1, stopped, "its turns stopped")
	require.NoError(t, opens(h), "it took the lease again")
	assert.Equal(t, int64(2), l.epoch)
}

// A leader whose renewals fail fences its coordinator before the lease
// could expire: once its last good renewal is older than the lease's time
// less the margin, whatever the database answers.
func TestALeaderFencesBeforeItsLeaseCouldExpire(t *testing.T) {
	h, l, leader := unled(t)
	leader.Lead(t.Context())
	l.set(func(l *lease) { l.renew = errors.New("the database does not answer") })

	h.clock.Advance(leaseTTL - leaderOptions.Margin - time.Second)
	before := opens(h)
	h.clock.Advance(time.Second)
	after := opens(h)

	require.NoError(t, before)
	require.ErrorIs(t, after, app.ErrNotHeld)
}

// A coordinator whose write found the lease gone fences itself, and its
// leader takes the lease again.
func TestALeaderTakesTheLeaseAgainAfterAWriteFoundItGone(t *testing.T) {
	h, l, leader := unled(t)
	leader.Lead(t.Context())
	h.records.set(func(n int) error {
		if n == 2 {
			return fmt.Errorf("record: %w", app.ErrLeaseLost)
		}
		return nil
	})
	l.set(func(l *lease) { l.mine = false })
	require.NoError(t, opens(h))
	require.ErrorIs(t, opens(h), app.ErrNotHeld)

	l.set(func(l *lease) { l.held = false })
	h.clock.Advance(leaderOptions.TakeEvery)

	require.NoError(t, opens(h))
}

// A leader never leads over records it cannot read: it releases the lease
// and takes it no more.
func TestALeaderStopsOverRecordsItCannotRead(t *testing.T) {
	h, l, leader := unled(t)
	_, err := h.records.Records.Append(t.Context(), "item-x", func([]item.Record) ([]item.Draft, error) {
		return []item.Draft{
			{ID: "opened", Item: "item-x", Incident: "inc-x", Body: item.Body{V: item.Version, Opened: &item.Opened{Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow}}},
			{ID: "later", Item: "item-x", Incident: "inc-x", Body: item.Body{V: item.Version + 1}},
		}, nil
	})
	require.NoError(t, err)

	err = leader.Lead(t.Context())
	h.clock.Advance(time.Minute)

	require.ErrorIs(t, err, item.ErrVersion)
	assert.Equal(t, 1, l.takes)
	assert.Equal(t, 1, l.releases)
}

// A leader that stops releases the lease, and its coordinator acts no
// more.
func TestALeaderThatStopsReleasesTheLease(t *testing.T) {
	h, l, leader := unled(t)
	leader.Lead(t.Context())

	leader.Stop(t.Context())
	h.clock.Advance(time.Minute)

	assert.Equal(t, 1, l.releases)
	assert.Equal(t, 1, l.takes)
	require.ErrorIs(t, opens(h), app.ErrNotHeld)
}

// led is a harness with an item whose round is open, left by an instance
// that crashed, and a leader of a new coordinator over the lease.
func led(t *testing.T) (*harness, *lease, *app.Leader) {
	t.Helper()
	h := newHarness(t)
	h.open("checkout fails")
	h.crash()
	var err error
	h.c, err = app.NewCoordinator(h.ports(), &h.opts)
	require.NoError(t, err)
	l := &lease{clock: h.clock}
	leader, err := app.NewLeader(l, h.c, h.clock, h.clock, &leaderOptions)
	require.NoError(t, err)
	return h, l, leader
}

// blockStarts makes the runner's starts wait until the returned function
// lets them go; started says when the first waits.
func blockStarts(h *harness) (started chan struct{}, letGo func()) {
	started, gate := make(chan struct{}, 4), make(chan struct{})
	h.turns.fail = func(*app.Turn) error {
		started <- struct{}{}
		<-gate
		return nil
	}
	return started, func() { close(gate) }
}

// A leader renews its holding while its coordinator takes over, which may
// take long: its renewals are armed before the takeover starts.
func TestALeaderRenewsWhileItsCoordinatorTakesOver(t *testing.T) {
	h, l, leader := led(t)
	started, letGo := blockStarts(h)
	led := make(chan error, 1)
	go func() { led <- leader.Lead(t.Context()) }()
	<-started

	h.clock.Advance(3 * leaderOptions.RenewEvery)
	var renewals int
	l.set(func(l *lease) { renewals = l.renewals })
	letGo()

	require.NoError(t, <-led)
	assert.Equal(t, 3, renewals, "renewed while the coordinator took over")
	require.NoError(t, opens(h))
}

// A leader stopped while it takes the lease, as a deploy stops an instance
// at any moment, acts no more: its coordinator never takes over, and the
// lease it took is released.
func TestALeaderStoppedWhileItTakesTheLeaseActsNoMore(t *testing.T) {
	h, l, leader := unled(t)
	l.set(func(l *lease) { l.taking, l.waiting = make(chan struct{}), make(chan struct{}, 1) })
	led := make(chan error, 1)
	go func() { led <- leader.Lead(t.Context()) }()
	<-l.waiting

	leader.Stop(t.Context())
	l.set(func(l *lease) { close(l.taking) })

	require.NoError(t, <-led)
	h.clock.Advance(time.Minute)
	require.ErrorIs(t, opens(h), app.ErrNotHeld)
	l.set(func(l *lease) {
		assert.Equal(t, 1, l.takes)
		assert.Equal(t, 1, l.releases, "the lease it took is released")
		assert.Zero(t, l.renewals)
	})
}

// A leader stopped while its coordinator takes over acts no more: its
// coordinator is fenced once the takeover ends, and the lease is released
// and renewed no more.
func TestALeaderStoppedWhileItsCoordinatorTakesOverActsNoMore(t *testing.T) {
	h, l, leader := led(t)
	started, letGo := blockStarts(h)
	led := make(chan error, 1)
	go func() { led <- leader.Lead(t.Context()) }()
	<-started

	stopped := make(chan struct{})
	go func() {
		leader.Stop(t.Context())
		close(stopped)
	}()
	time.Sleep(50 * time.Millisecond)
	letGo()
	<-stopped
	require.NoError(t, <-led)
	h.clock.Advance(time.Minute)

	require.ErrorIs(t, opens(h), app.ErrNotHeld)
	l.set(func(l *lease) {
		assert.Equal(t, 1, l.releases)
		assert.Zero(t, l.renewals, "renewed no more")
	})
}

// Renewals are armed RenewEvery apart from when each was sent: renewals
// that each take a while, under RenewEvery, never let the holding look too
// old to be sure.
func TestSlowRenewalsKeepTheLease(t *testing.T) {
	h, l, leader := unled(t)
	l.set(func(l *lease) { l.slow = 3 * time.Second })
	require.NoError(t, leader.Lead(t.Context()))

	h.clock.Advance(time.Minute)

	require.NoError(t, opens(h), "it holds the lease")
}

// A renewal of an earlier holding that finds the lease lost once the leader
// holds it again, under a new epoch, changes nothing: here the holding grew
// too old while the renewal was in flight, and the leader took the lease
// again.
func TestALateRenewalOfAnEarlierHoldingChangesNothing(t *testing.T) {
	h, l, leader := unled(t)
	require.NoError(t, leader.Lead(t.Context()))
	l.set(func(l *lease) {
		l.hook = func() error {
			l.set(func(l *lease) { l.hook, l.held = nil, false })
			h.clock.Advance(leaderOptions.TakeEvery + leaseTTL)
			return fmt.Errorf("renew: %w", app.ErrLeaseLost)
		}
	})

	h.clock.Advance(leaderOptions.RenewEvery)

	require.NoError(t, opens(h), "the later holding acts")
	l.set(func(l *lease) { assert.Equal(t, int64(2), l.epoch) })
}

// lagging is the clock's timers, but that one armed for a time already come
// waits until flush runs it, as a busy process delays a timer's goroutine.
type lagging struct {
	c    *clock
	mu   sync.Mutex
	late []func()
}

func (g *lagging) At(t time.Time, fn func()) func() {
	if t.After(g.c.Now()) {
		return g.c.At(t, fn)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.late = append(g.late, fn)
	return func() {}
}

func (g *lagging) flush() {
	g.mu.Lock()
	late := g.late
	g.late = nil
	g.mu.Unlock()
	for _, fn := range late {
		fn()
	}
}

// A coordinator's word that a write found the lease gone, which reaches the
// leader once it learned of the loss otherwise and took the lease again,
// changes nothing.
func TestALateWordOfAnEarlierHoldingChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.crash()
	lag := &lagging{c: h.clock}
	h.timers = lag
	var err error
	h.c, err = app.NewCoordinator(h.ports(), &h.opts)
	require.NoError(t, err)
	l := &lease{clock: h.clock}
	leader, err := app.NewLeader(l, h.c, h.clock, lag, &leaderOptions)
	require.NoError(t, err)
	require.NoError(t, leader.Lead(t.Context()))
	gone := true
	h.records.set(func(int) error {
		if gone {
			gone = false
			return fmt.Errorf("record: %w", app.ErrLeaseLost)
		}
		return nil
	})
	require.Error(t, opens(h), "the write found the lease gone")
	l.set(func(l *lease) { l.renew, l.mine = fmt.Errorf("renew: %w", app.ErrLeaseLost), false })
	h.clock.Advance(leaderOptions.RenewEvery)
	l.set(func(l *lease) { l.held, l.renew = false, nil })
	h.clock.Advance(leaderOptions.TakeEvery)
	require.NoError(t, opens(h), "the leader took the lease again")

	lag.flush()

	require.NoError(t, opens(h), "the late word changes nothing")
}

// A leader whose context ends while its coordinator takes over releases the
// lease all the same, under a context of its own: as a database would, the
// lease refuses a release under the context that ended, and would stay held
// until it expired.
func TestALeaderWhoseContextEndsWhileItTakesOverReleasesTheLease(t *testing.T) {
	h, l, leader := led(t)
	started, letGo := blockStarts(h)
	ctx, cancel := context.WithCancel(t.Context())
	led := make(chan error, 1)
	go func() { led <- leader.Lead(ctx) }()
	<-started

	cancel()
	letGo()

	require.NoError(t, <-led)
	require.Eventually(t, func() bool {
		released := 0
		l.set(func(l *lease) { released = l.releases })
		return released == 1
	}, 5*time.Second, time.Millisecond)
	require.ErrorIs(t, opens(h), app.ErrNotHeld)
}

// A leader whose context ends stops, as Stop does: its coordinator is
// fenced and the lease released.
func TestALeaderStopsWhenItsContextEnds(t *testing.T) {
	h, l, leader := unled(t)
	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, leader.Lead(ctx))

	cancel()

	require.Eventually(t, func() bool {
		released := 0
		l.set(func(l *lease) { released = l.releases })
		return released == 1
	}, 5*time.Second, time.Millisecond)
	require.ErrorIs(t, opens(h), app.ErrNotHeld)
}
