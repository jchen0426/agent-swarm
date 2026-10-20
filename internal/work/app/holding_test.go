package app_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

var _ holding.Holding = (*app.Holding)(nil)

// A holding is valid only for the leader's current epoch, while it holds the
// lease, and while its last good renewal, or its take, was sent less than the
// lease's time less the margin ago: here the renewals fail, and the watch,
// whose time came, has not run yet, as a busy process delays it. A holding
// that is not held is invalid, though its take is recent.
func TestValidHoldsOnlyForTheCurrentEpochWhileTheLastGoodSendIsFresh(t *testing.T) {
	h := fresh(t)
	g := &heldTimers{goTimers: goTimers{c: h.clock}}
	hold := app.NewHolding(leaseTTL, leaderOptions.Margin, h.clock.Now)
	l, leader := leaderOf(t, h, g, func(o *app.LeaderOptions) { o.Holding = hold })
	unheld := hold.Valid(1)
	require.NoError(t, leader.Lead(t.Context()))
	l.set(func(l *lease) { l.renew = errors.New("the database does not answer") })

	taken, other := hold.Valid(1), hold.Valid(2)
	h.clock.Advance(leaderOptions.RenewEvery)
	g.wait()
	h.clock.Advance(leaseTTL - leaderOptions.Margin - leaderOptions.RenewEvery - time.Millisecond)
	g.wait()
	fresh := hold.Valid(1)
	late := g.holding(func() { h.clock.Advance(time.Millisecond) })
	stale := hold.Valid(1)
	for _, fn := range late {
		g.run(fn)
	}
	l.set(func(l *lease) { l.held, l.mine, l.renew = false, false, nil })
	h.clock.Advance(leaderOptions.TakeEvery)
	g.wait()
	next, earlier := hold.Valid(2), hold.Valid(1)
	l.set(func(l *lease) { l.renew = fmt.Errorf("renew: %w", app.ErrLeaseLost) })
	h.clock.Advance(leaderOptions.RenewEvery)
	g.wait()
	lost := hold.Valid(2)

	assert.False(t, unheld, "nothing is held before the take")
	assert.True(t, taken)
	assert.False(t, other, "another epoch")
	assert.True(t, fresh, "just before the lease's time less the margin from the take's send")
	assert.False(t, stale, "at the lease's time less the margin, though the watch has not run")
	assert.True(t, next, "the next holding")
	assert.False(t, earlier, "an earlier epoch")
	assert.False(t, lost, "a holding lost, though its take is recent")
}

// The host sleeps while the leader holds the lease, as a laptop does when its
// lid closes: Darwin's monotonic clock, the leader's, stops, while the wall
// clock runs on. On waking, the holding is invalid once wall time passed its
// margin, though monotonic time did not, so that nothing is sent under it
// (Review Focus 2): here 1 s of monotonic time and 20 s of wall time passed
// since the take was sent.
func TestAHoldingIsInvalidOnceWallTimePassesItsMarginThoughMonotonicTimeDidNot(t *testing.T) {
	h := fresh(t)
	var slept time.Duration
	hold := app.NewHolding(leaseTTL, leaderOptions.Margin, func() time.Time { return h.clock.Now().Add(slept) })
	_, leader := leaderOf(t, h, h.clock, func(o *app.LeaderOptions) { o.Holding = hold })
	require.NoError(t, leader.Lead(t.Context()))
	epoch, _, _ := leader.Status()
	awake := hold.Valid(epoch)

	slept = 19 * time.Second
	h.clock.Advance(time.Second)

	assert.True(t, awake)
	assert.False(t, hold.Valid(epoch), "20 s of wall time passed since the take was sent")
}

// Valid takes no lock, so that a turn's start, which asks it under the
// coordinator's lock, never waits for a leader that fences: the leader holds
// its own lock and waits for the coordinator's (review 3's deadlock probe).
// Here a renewal finds the lease lost while the coordinator takes over and
// starts a turn; the turn's start asks once the holding ended.
func TestValidNeverWaitsForTheLeadersLock(t *testing.T) {
	var hold *app.Holding
	answered := make(chan bool, 1)
	runner := &stalling{started: make(chan struct{}), ended: func(ctx context.Context) {
		epoch, err := holding.EpochFrom(ctx)
		if err != nil {
			epoch = -1
		}
		answered <- hold.Valid(epoch)
	}}
	h := leftOpen(t, func(p *app.Ports) { runner.Turns, p.Turns = p.Turns, runner })
	hold = app.NewHolding(leaseTTL, leaderOptions.Margin, h.clock.Now)
	l, leader := leaderOf(t, h, h.clock, func(o *app.LeaderOptions) { o.Holding = hold })
	led := make(chan error, 1)
	go func() { led <- leader.Lead(t.Context()) }()
	<-runner.started
	l.set(func(l *lease) { l.renew = fmt.Errorf("renew: %w", app.ErrLeaseLost) })

	renewed := make(chan struct{})
	go func() {
		h.clock.Advance(leaderOptions.RenewEvery)
		close(renewed)
	}()
	finished := make(chan struct{})
	var valid bool
	var err error
	go func() {
		valid = <-answered
		<-renewed
		err = <-led
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("Valid waited for the leader's lock, which waits for the coordinator's")
	}
	require.NoError(t, err)
	assert.False(t, valid, "the holding ended before the leader fenced")
}
