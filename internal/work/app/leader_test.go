package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
// said so on waiting; while stuck is set, it waits until its context ends,
// as a take on a pooled connection to a database whose address drops its
// packets does, and fails. releasing, when set, runs as a release begins.
// trace, when set, notes each take and renewal, and how it went.
type lease struct {
	mu        sync.Mutex
	clock     *clock
	held      bool
	mine      bool
	epoch     int64
	renew     error
	slow      time.Duration
	hook      func() error
	taking    chan struct{}
	waiting   chan struct{}
	stuck     bool
	releasing func()
	trace     *trace
	takes     int
	renewals  int
	releases  int
}

func (l *lease) Take(ctx context.Context) (app.Term, bool, error) {
	l.mu.Lock()
	if l.stuck {
		l.takes++
		l.mu.Unlock()
		<-ctx.Done()
		return app.Term{}, false, fmt.Errorf("take the lease: %w", ctx.Err())
	}
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
		l.trace.note("take refused")
		return app.Term{}, false, nil
	}
	l.held, l.mine = true, true
	l.epoch++
	l.trace.note("take")
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
		l.trace.note("renew failed")
		return l.renew
	}
	l.trace.note("renew")
	return nil
}

func (l *lease) Release(ctx context.Context) error {
	l.mu.Lock()
	releasing := l.releasing
	l.mu.Unlock()
	if releasing != nil {
		releasing()
	}
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
// would expire, and try to take it every 5 s, each take waiting 5 s at most
// for the writes of the last holding.
var leaderOptions = app.LeaderOptions{RenewEvery: 5 * time.Second, Margin: 5 * time.Second, TakeEvery: 5 * time.Second, TakeWait: 5 * time.Second}

const leaseTTL = 15 * time.Second

// successor crashes the harness's instance and builds the coordinator of the
// next over its ports, as change edits them: a leader takes over for it.
func successor(t *testing.T, h *harness, change ...func(*app.Ports)) *harness {
	t.Helper()
	h.crash()
	p := h.ports()
	for _, f := range change {
		f(p)
	}
	var err error
	h.c, err = app.NewCoordinator(p, &h.opts)
	require.NoError(t, err)
	return h
}

// fresh is a harness whose instance crashed with nothing open, and the
// coordinator of the next, over its ports as change edits them.
func fresh(t *testing.T, change ...func(*app.Ports)) *harness {
	t.Helper()
	return successor(t, newHarness(t), change...)
}

// leftOpen is a harness whose instance crashed with an item's round open,
// and the coordinator of the next, over its ports as change edits them.
func leftOpen(t *testing.T, change ...func(*app.Ports)) *harness {
	t.Helper()
	h := newHarness(t)
	h.open("checkout fails")
	return successor(t, h, change...)
}

// leaderOf is a leader of the harness's coordinator over a lease in memory,
// on the harness's clock and timers, with leaderOptions as change edits them.
func leaderOf(t *testing.T, h *harness, timers app.Timers, change ...func(*app.LeaderOptions)) (*lease, *app.Leader) {
	t.Helper()
	opts := leaderOptions
	for _, f := range change {
		f(&opts)
	}
	l := &lease{clock: h.clock}
	leader, err := app.NewLeader(l, h.c, h.clock, timers, &opts)
	require.NoError(t, err)
	return l, leader
}

// unled is a harness whose instance has not taken over: a leader takes
// over for it. change edits the ports its coordinator is built over.
func unled(t *testing.T, change ...func(*app.Ports)) (*harness, *lease, *app.Leader) {
	t.Helper()
	h := fresh(t, change...)
	l, leader := leaderOf(t, h, h.clock)
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

// The watch counts a holding's age from when its last good renewal was sent,
// not from when it answered: the database may have taken the renewal as soon
// as it was sent, so that the lease expires its time after the send, before
// its time after the answer.
func TestTheWatchCountsFromWhenTheLastGoodRenewalWasSent(t *testing.T) {
	h, l, leader := unled(t)
	l.set(func(l *lease) { l.slow = 4 * time.Second })
	require.NoError(t, leader.Lead(t.Context()))
	h.clock.Advance(leaderOptions.RenewEvery) // sent at 5 s, answers at 9 s
	l.set(func(l *lease) { l.slow, l.renew = 0, errors.New("the database does not answer") })

	h.clock.Advance(5 * time.Second) // to 14 s
	before := opens(h)
	h.clock.Advance(time.Second) // to 15 s = 5 s + the lease's 15 s less the 5 s margin
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

// flaky is the records store whose Active fails the first fails times, as a
// database does when it does not answer.
type flaky struct {
	app.Records
	mu    sync.Mutex
	fails int
}

func (f *flaky) Active(ctx context.Context) ([]item.ID, error) {
	f.mu.Lock()
	fail := f.fails > 0
	if fail {
		f.fails--
	}
	f.mu.Unlock()
	if fail {
		return nil, errors.New("the database does not answer")
	}
	return f.Records.Active(ctx)
}

// A takeover that cannot read the records gives the lease up rather than hold
// it unled, and the leader takes it again after TakeEvery: the coordinator
// acts once a takeover reads them.
func TestATakeoverWhoseReadFailsGivesTheLeaseUpAndTakesItAgain(t *testing.T) {
	h, l, leader := unled(t, func(p *app.Ports) { p.Records = &flaky{Records: p.Records, fails: 1} })

	require.NoError(t, leader.Lead(t.Context()))
	fenced := opens(h)
	var takes, releases int
	l.set(func(l *lease) { takes, releases = l.takes, l.releases })
	h.clock.Advance(leaderOptions.TakeEvery)

	require.ErrorIs(t, fenced, app.ErrNotHeld)
	assert.Equal(t, 1, takes)
	assert.Equal(t, 1, releases, "the lease was given up")
	require.NoError(t, opens(h), "it took the lease again")
	l.set(func(l *lease) {
		assert.Equal(t, 2, l.takes)
		assert.Equal(t, int64(2), l.epoch)
	})
}

// A takeover whose write finds the lease gone ends the holding, as any write
// that finds it gone does, and the log says so: the holding is over, as the
// database said, so nothing is left to release. The leader takes the lease
// again after TakeEvery: the coordinator's word of the loss, which reaches
// the leader later, changes nothing, and the second takeover starts the item.
func TestATakeoverWhoseWriteFindsTheLeaseGoneTakesItAgain(t *testing.T) {
	logs := &syncBuffer{}
	h := fresh(t)
	l, leader := leaderOf(t, h, h.clock, func(o *app.LeaderOptions) { o.Log = slog.New(slog.NewJSONHandler(logs, nil)) })
	leftItem(t, h, false)
	h.records.set(func(n int) error {
		if n == 1 {
			// The holding expired before the write.
			l.set(func(l *lease) { l.held, l.mine = false, false })
			return fmt.Errorf("record: %w", app.ErrLeaseLost)
		}
		return nil
	})

	require.NoError(t, leader.Lead(t.Context()))
	fenced := opens(h)
	var releases int
	l.set(func(l *lease) { releases = l.releases })
	h.clock.Advance(leaderOptions.TakeEvery)

	require.ErrorIs(t, fenced, app.ErrNotHeld)
	assert.Zero(t, releases, "a holding found gone is not released")
	require.NoError(t, opens(h))
	l.set(func(l *lease) {
		assert.Equal(t, 2, l.takes)
		assert.Equal(t, int64(2), l.epoch)
	})
	assert.NotNil(t, h.state("item-x").Started, "the second takeover started the item")
	lines := leaderLines(t, logs.String())
	require.Equal(t, []string{"lease taken", "lease lost", "fenced", "lease taken"}, messages(lines))
	assert.Equal(t, "a write found the lease gone", lines[1]["reason"])
	assert.InDelta(t, 1, lines[1]["epoch"], 0)
}

// leftItem commits, without the coordinator, the records of an item that an
// earlier instance left: its opening, and, when later is set, a record of a
// version this build cannot read.
func leftItem(t *testing.T, h *harness, later bool) {
	t.Helper()
	_, err := h.records.Records.Append(t.Context(), "item-x", func([]item.Record) ([]item.Draft, error) {
		drafts := []item.Draft{{ID: "opened", Item: "item-x", Incident: "inc-x", Body: item.Body{V: item.Version, Opened: &item.Opened{Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow}}}}
		if later {
			drafts = append(drafts, item.Draft{ID: "later", Item: "item-x", Incident: "inc-x", Body: item.Body{V: item.Version + 1}})
		}
		return drafts, nil
	})
	require.NoError(t, err)
}

// A leader leads over records it cannot read, at its first take as at a later
// one (Decision 19): its coordinator skips the item a later build wrote and
// carries the readable items on, while the leader holds the lease, renews it,
// and has not stopped: Done stays open, and Err says nothing. Only a schema
// this build cannot run on stops a leader for good.
func TestALeaderLeadsOverRecordsItCannotRead(t *testing.T) {
	for _, c := range []struct {
		name string
		// busy says another instance holds the lease at the leader's first
		// take, so that it takes the lease at a later one.
		busy bool
	}{{"at the first take", false}, {"at a later take", true}} {
		h := newHarness(t)
		readable := h.open("payments fail")
		leftItem(t, h, true)
		successor(t, h)
		l, leader := leaderOf(t, h, h.clock)
		l.set(func(l *lease) { l.held = c.busy })
		before := len(h.turns.started)

		require.NoError(t, leader.Lead(t.Context()), c.name)
		if c.busy {
			l.set(func(l *lease) { l.held = false })
			h.clock.Advance(leaderOptions.TakeEvery)
		}
		h.clock.Advance(3 * leaderOptions.RenewEvery)

		assert.False(t, isDone(leader), "%s: Done stays open", c.name)
		require.NoError(t, leader.Err(), c.name)
		_, held, _ := leader.Status()
		assert.True(t, held, "%s: it holds the lease", c.name)
		l.set(func(l *lease) {
			assert.Zero(t, l.releases, "%s: and keeps it", c.name)
			assert.Equal(t, 3, l.renewals, "%s: renewing it", c.name)
		})
		assert.Equal(t, []item.ID{"item-x"}, h.c.Skipped(), c.name)
		started := h.turns.started[before:]
		require.Len(t, started, 2, "%s: the readable item goes on", c.name)
		for _, turn := range started {
			assert.Equal(t, h.state(readable).Run.ID, turn.Run, c.name)
		}
	}
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
	h := leftOpen(t)
	l, leader := leaderOf(t, h, h.clock)
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

// counting is a harness whose instance crashed with nothing open, and the
// coordinator of the next, whose takeovers are counted.
func counting(t *testing.T) (*harness, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	h := fresh(t, func(p *app.Ports) { p.Records = &takingOver{Records: p.Records, f: func() { n.Add(1) }} })
	return h, &n
}

// A take counts its holding from when it was sent, as a renewal counts from
// its own send: the database started the holding no earlier, and the time the
// database says it did cannot be placed on the leader's clock, whose offset to
// the database's is only an estimate, so that counting from it could leave the
// coordinator acting after the holding expired. A take that waited the lease's
// time less the margin or more, as behind the writes in flight of a holder cut
// off by a partition, therefore ends its holding before any takeover. The
// leader takes the lease again once its own holding has expired.
func TestATakeThatWaitedTheLeasesTimeLessTheMarginFencesAtOnce(t *testing.T) {
	h, n := counting(t)
	l, leader := leaderOf(t, h, h.clock)
	l.set(func(l *lease) { l.taking, l.waiting = make(chan struct{}), make(chan struct{}, 1) })
	led := make(chan error, 1)
	go func() { led <- leader.Lead(t.Context()) }()
	<-l.waiting
	h.clock.Advance(leaseTTL - leaderOptions.Margin)
	l.set(func(l *lease) {
		close(l.taking)
		l.taking, l.renew = nil, errors.New("the database does not answer")
	})
	require.NoError(t, <-led)
	stale := n.Load()

	h.clock.Advance(0)
	fenced := opens(h)
	h.clock.Advance(leaderOptions.TakeEvery)
	stillHeld := opens(h)
	l.set(func(l *lease) { l.held, l.mine, l.renew = false, false, nil })
	h.clock.Advance(leaderOptions.TakeEvery)

	assert.Zero(t, stale, "no takeover ran under the take that waited")
	require.ErrorIs(t, fenced, app.ErrNotHeld, "the take that waited fenced its coordinator at once")
	require.ErrorIs(t, stillHeld, app.ErrNotHeld, "its own holding still held the lease")
	require.NoError(t, opens(h), "it took the lease again once its holding expired")
	assert.Equal(t, int32(1), n.Load())
	l.set(func(l *lease) { assert.Equal(t, int64(2), l.epoch) })
}

// A take that waits on a database that never answers, as one on a pooled
// connection to a database whose address drops its packets does, ends once
// the take's wait and RenewEvery have passed, not when the database answers
// (Decision 4): the leader takes the lease again TakeEvery later, and leads
// once the database answers again.
func TestATakeThatHangsEndsWithinItsBoundAndTheLeaderTakesAgain(t *testing.T) {
	const takeWait, renewEvery = 100 * time.Millisecond, 200 * time.Millisecond
	h := fresh(t)
	l, leader := leaderOf(t, h, h.clock, func(o *app.LeaderOptions) { o.TakeWait, o.RenewEvery = takeWait, renewEvery })
	l.set(func(l *lease) { l.stuck = true })
	led := make(chan error, 1)

	began := time.Now()
	go func() { led <- leader.Lead(t.Context()) }()
	select {
	case err := <-led:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the take that hangs did not end within its bound")
	}
	took := time.Since(began)
	stuck := opens(h)
	l.set(func(l *lease) { l.stuck = false })
	h.clock.Advance(leaderOptions.TakeEvery)

	assert.GreaterOrEqual(t, took, takeWait+renewEvery, "the take waited for its bound, the take's wait and RenewEvery")
	require.ErrorIs(t, stuck, app.ErrNotHeld, "the take that hung took nothing")
	require.NoError(t, opens(h), "the leader took the lease again")
	l.set(func(l *lease) { assert.Equal(t, 2, l.takes) })
}

// onTaken is a log handler that reads the leader's status as "lease taken" is
// logged.
type onTaken struct {
	leader *app.Leader
	held   []bool
}

func (o *onTaken) Enabled(context.Context, slog.Level) bool { return true }

// Handle reads the leader's status, which takes no lock, at "lease taken".
//
//nolint:gocritic // hugeParam: slog.Handler takes a record by value.
func (o *onTaken) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "lease taken" {
		_, held, _ := o.leader.Status()
		o.held = append(o.held, held)
	}
	return nil
}

func (o *onTaken) WithAttrs([]slog.Attr) slog.Handler { return o }

func (o *onTaken) WithGroup(string) slog.Handler { return o }

// A take is logged once the leader has published its holding: whoever reads
// "lease taken" in the log finds the instance's status leading, so that an
// opening sent then is answered as one sent while the instance takes over,
// to be tried again, never as one sent to a standby (C4).
func TestATakeIsLoggedOnceItsHoldingIsPublished(t *testing.T) {
	h := fresh(t)
	taken := &onTaken{}
	_, leader := leaderOf(t, h, h.clock, func(o *app.LeaderOptions) { o.Log = slog.New(taken) })
	taken.leader = leader

	require.NoError(t, leader.Lead(t.Context()))

	assert.Equal(t, []bool{true}, taken.held, "the status said the instance leads when its take was logged")
}

// A host that sleeps while a take is in flight, as a laptop does when its lid
// closes, wakes with a holding that the wall clock says is too old to be sure
// of, though the monotonic clock, which stopped, does not: no takeover runs
// under it, as Valid refuses it (Review Focus 2).
func TestATakeTheHostSleptThroughNeverTakesOver(t *testing.T) {
	h, n := counting(t)
	var slept atomic.Int64
	hold := app.NewHolding(leaseTTL, leaderOptions.Margin, func() time.Time { return h.clock.Now().Add(time.Duration(slept.Load())) })
	l, leader := leaderOf(t, h, h.clock, func(o *app.LeaderOptions) { o.Holding = hold })
	l.set(func(l *lease) { l.taking, l.waiting = make(chan struct{}), make(chan struct{}, 1) })
	led := make(chan error, 1)
	go func() { led <- leader.Lead(t.Context()) }()
	<-l.waiting

	slept.Store(int64(leaseTTL - leaderOptions.Margin))
	l.set(func(l *lease) { close(l.taking) })
	require.NoError(t, <-led)

	assert.Zero(t, n.Load(), "no takeover ran")
	require.ErrorIs(t, opens(h), app.ErrNotHeld)
	_, held, _ := leader.Status()
	assert.False(t, held, "the holding ended")
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

// hungOpening starts an opening that waits on a records store that hangs,
// and returns what the opening answers; the store is let go when the test
// ends.
func hungOpening(t *testing.T, h *harness) <-chan error {
	t.Helper()
	letGo := h.records.hang()
	t.Cleanup(letGo)
	opened := make(chan error, 1)
	go func() { opened <- opens(h) }()
	require.Eventually(t, func() bool { return h.records.waiting.Load() == 1 }, 5*time.Second, time.Millisecond)
	return opened
}

// promptly reports whether f returns within a few seconds, which a call that
// waits on a hung store does not.
func promptly(f func()) bool {
	returned := make(chan struct{})
	go func() {
		f()
		close(returned)
	}()
	select {
	case <-returned:
		return true
	case <-time.After(3 * time.Second):
		return false
	}
}

// A watch that finds the holding too old ends the holding before it fences
// the coordinator, so that an opening that waits on a store that hangs gives
// way: the fence waits for the coordinator's lock, which the opening holds,
// and not for the store.
func TestTheWatchEndsTheHoldingBeforeItFences(t *testing.T) {
	h, l, leader := unled(t)
	require.NoError(t, leader.Lead(t.Context()))
	l.set(func(l *lease) { l.renew = errors.New("the database does not answer") })
	opened := hungOpening(t, h)

	fenced := promptly(func() { h.clock.Advance(leaseTTL - leaderOptions.Margin) })

	require.True(t, fenced, "the watch's fence waited for the store an opening waits on")
	require.Error(t, <-opened)
	require.ErrorIs(t, opens(h), app.ErrNotHeld)
}

// Stop ends the holding before it fences the coordinator, as the watch does:
// it does not wait for the store an opening waits on, and releases the lease.
func TestStopEndsTheHoldingBeforeItFences(t *testing.T) {
	h, l, leader := unled(t)
	require.NoError(t, leader.Lead(t.Context()))
	opened := hungOpening(t, h)

	stopped := promptly(func() { leader.Stop(t.Context()) })

	require.True(t, stopped, "Stop waited for the store an opening waits on")
	require.Error(t, <-opened)
	l.set(func(l *lease) { assert.Equal(t, 1, l.releases) })
}

// heldTimers runs each timer's function on a goroutine of its own, as a real
// clock's timers do, but holds back those whose time comes while the test
// asks it to, until the test runs them: as a real clock may run a timer's
// function after the timer was cancelled, once its goroutine gets its turn.
type heldTimers struct {
	goTimers
	mu   sync.Mutex
	hold bool
	held []func()
}

func (g *heldTimers) At(t time.Time, fn func()) func() {
	return g.c.At(t, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.hold {
			g.held = append(g.held, fn)
			return
		}
		g.wg.Go(fn)
	})
}

// holding returns, held back, the functions whose time came while f ran, in
// the order it came.
func (g *heldTimers) holding(f func()) []func() {
	g.mu.Lock()
	g.hold = true
	g.mu.Unlock()
	f()
	g.mu.Lock()
	defer g.mu.Unlock()
	held := g.held
	g.hold, g.held = false, nil
	return held
}

// run runs a function held back on a goroutine of its own, as its timer
// would have, and waits until it, and every timer it made due, have run.
func (g *heldTimers) run(fn func()) {
	g.wg.Go(fn)
	g.wait()
}

// heldLeader is a harness whose instance has not taken over, and a leader of
// a new coordinator whose timers are held back on request.
func heldLeader(t *testing.T) (*harness, *lease, *app.Leader, *heldTimers) {
	t.Helper()
	h := fresh(t)
	g := &heldTimers{goTimers: goTimers{c: h.clock}}
	l, leader := leaderOf(t, h, g)
	return h, l, leader, g
}

// A renewal whose timer came due before its holding was lost, and whose
// function runs after the loss, as a real clock's may, neither renews the
// lease nor arms another renewal: a chain of renewals of a holding the leader
// no longer acts under would keep that holding from expiring, so that no
// instance, this one included, could take the lease. Here the renewals failed
// until the watch fenced the coordinator, and the database answers again by
// the time the late renewal runs.
func TestARenewalThatRunsAfterItsHoldingWasLostRenewsNothing(t *testing.T) {
	h, l, leader, g := heldLeader(t)
	require.NoError(t, leader.Lead(t.Context()))
	l.set(func(l *lease) { l.renew = errors.New("the database does not answer") })
	h.clock.Advance(leaderOptions.RenewEvery)
	g.wait()
	late := g.holding(func() { h.clock.Advance(leaseTTL - leaderOptions.Margin - leaderOptions.RenewEvery) })
	require.Len(t, late, 2, "the watch and the next renewal came due together")
	g.run(late[0])
	require.ErrorIs(t, opens(h), app.ErrNotHeld, "the watch fenced the coordinator")
	var renewals int
	l.set(func(l *lease) { l.renew, renewals = nil, l.renewals })

	g.run(late[1])
	var after int
	l.set(func(l *lease) { after = l.renewals })
	h.clock.Advance(time.Minute)
	g.wait()

	assert.Equal(t, renewals, after, "the late renewal renewed nothing")
	l.set(func(l *lease) { assert.Equal(t, renewals, l.renewals, "nor did it arm another renewal") })
}

// A watch whose timer came due before its holding was lost, and whose
// function runs after the loss, neither arms another watch nor fences the
// coordinator again: here a renewal found the lease lost, to another
// instance, as the watch came due.
func TestAWatchThatRunsAfterItsHoldingWasLostDoesNothing(t *testing.T) {
	h, l, leader, g := heldLeader(t)
	require.NoError(t, leader.Lead(t.Context()))
	h.clock.Advance(leaderOptions.RenewEvery)
	g.wait()
	l.set(func(l *lease) { l.renew = fmt.Errorf("renew: %w", app.ErrLeaseLost) })
	late := g.holding(func() { h.clock.Advance(leaseTTL - leaderOptions.Margin - leaderOptions.RenewEvery) })
	require.Len(t, late, 2, "the watch and the next renewal came due together")
	g.run(late[1])
	require.ErrorIs(t, opens(h), app.ErrNotHeld, "the renewal found the lease lost")
	stops, pending := h.turns.all, h.clock.pending()

	g.run(late[0])
	armed := h.clock.pending()
	h.clock.Advance(time.Minute)
	g.wait()

	assert.Equal(t, pending, armed, "the late watch armed no watch")
	assert.Equal(t, stops, h.turns.all, "nor fenced the coordinator again")
}

// isDone reports whether a leader's Done is closed.
func isDone(leader *app.Leader) bool {
	select {
	case <-leader.Done():
		return true
	default:
		return false
	}
}

// A leader that is stopped says so on Done, with no error; stopping it again
// changes nothing.
func TestALeaderThatStopsSaysSoOnDone(t *testing.T) {
	_, _, leader := unled(t)
	require.NoError(t, leader.Lead(t.Context()))
	require.False(t, isDone(leader), "it leads")

	leader.Stop(t.Context())
	leader.Stop(t.Context())

	assert.True(t, isDone(leader))
	require.NoError(t, leader.Err())
}

// A leader whose context ends says so on Done, with no error.
func TestALeaderWhoseContextEndsSaysSoOnDone(t *testing.T) {
	_, _, leader := unled(t)
	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, leader.Lead(ctx))
	require.False(t, isDone(leader), "it leads")

	cancel()

	require.Eventually(t, func() bool { return isDone(leader) }, 5*time.Second, time.Millisecond)
	require.NoError(t, leader.Err())
}

// A leader whose lease is lost takes it again, and has not stopped for good:
// it does not say so on Done.
func TestALeaderThatTakesTheLeaseAgainHasNotStoppedForGood(t *testing.T) {
	h, l, leader := unled(t)
	require.NoError(t, leader.Lead(t.Context()))
	l.set(func(l *lease) { l.renew, l.held = fmt.Errorf("renew: %w", app.ErrLeaseLost), true })
	h.clock.Advance(leaderOptions.RenewEvery)
	require.ErrorIs(t, opens(h), app.ErrNotHeld, "the lease was lost")
	require.False(t, isDone(leader), "it waits for the lease")
	l.set(func(l *lease) { l.held, l.renew = false, nil })

	h.clock.Advance(leaderOptions.TakeEvery)

	require.NoError(t, opens(h), "it took the lease again")
	assert.False(t, isDone(leader))
	require.NoError(t, leader.Err())
}

// trace notes, in order, what a leader did with its lease and its clock: its
// takes and renewals, and how each went, as the lease notes them; its samples
// of the database's clock, as the leader's ClockSampler; and its
// coordinator's takeovers, as takingOver notes them.
type trace struct {
	mu   sync.Mutex
	what []string
}

func (r *trace) note(what string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.what = append(r.what, what)
}

func (r *trace) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.what)
}

func (r *trace) Sample(context.Context) error {
	r.note("sample")
	return nil
}

// takingOver is the records store, which runs f as each takeover begins: when
// it reads the items that do not await their close.
type takingOver struct {
	app.Records
	f func()
}

func (r *takingOver) Active(ctx context.Context) ([]item.ID, error) {
	r.f()
	return r.Records.Active(ctx)
}

// schema is the Schema port: each check returns err.
type schema struct {
	mu  sync.Mutex
	err error
}

func (s *schema) Check(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *schema) set(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// errIncompatible is a schema this build cannot run on, as a check says so.
var errIncompatible = fmt.Errorf("the schema's oldest reader is level 12, above this build's level 11: %w", app.ErrSchema)

// stalling is the runner, whose first start waits until its context ends, as
// a takeover in progress waits on a slow step; started is closed once it
// waits. ended, when set, runs once that context ended, still under the
// coordinator's lock, as the rest of the start does.
type stalling struct {
	app.Turns
	started chan struct{}
	once    sync.Once
	ended   func(ctx context.Context)
}

func (s *stalling) Start(ctx context.Context, t *app.Turn) error {
	stall := false
	s.once.Do(func() { stall = true })
	if stall {
		close(s.started)
		<-ctx.Done()
		if s.ended != nil {
			s.ended(ctx)
		}
	}
	return s.Turns.Start(ctx, t)
}

// A draining leader fences its coordinator and keeps its holding: it renews
// it, and never takes the lease, until Stop releases it (Decision 6). A
// second leader, whose holding the watch ends while it drains, does not take
// the lease, free again, either.
func TestADrainingLeaderKeepsRenewingAndNeverTakes(t *testing.T) {
	h, l, leader := unled(t)
	require.NoError(t, leader.Lead(t.Context()))

	leader.Drain()
	fenced := opens(h)
	h.clock.Advance(3 * leaderOptions.RenewEvery)
	epoch, held, draining := leader.Status()
	var renewals int
	l.set(func(l *lease) { renewals = l.renewals })
	open := !isDone(leader)
	leader.Stop(t.Context())

	require.ErrorIs(t, fenced, app.ErrNotHeld, "the coordinator is fenced")
	assert.Equal(t, 3, renewals, "the holding is renewed")
	assert.Equal(t, int64(1), epoch)
	assert.True(t, held, "and kept")
	assert.True(t, draining)
	assert.True(t, open, "Done waits for the release")
	l.set(func(l *lease) {
		assert.Equal(t, 1, l.takes)
		assert.Equal(t, 1, l.releases, "Stop releases the lease")
	})
	assert.True(t, isDone(leader))

	h, l, leader = unled(t)
	require.NoError(t, leader.Lead(t.Context()))
	leader.Drain()
	l.set(func(l *lease) { l.renew = errors.New("the database does not answer") })
	h.clock.Advance(leaseTTL - leaderOptions.Margin)
	l.set(func(l *lease) { l.held, l.mine = false, false })
	h.clock.Advance(3 * leaderOptions.TakeEvery)

	l.set(func(l *lease) { assert.Equal(t, 1, l.takes, "no take, though the lease is free") })
}

// A holding lost while the leader drains ends the drain at once: nothing is
// left to release, and Done closes (Decision 6).
func TestADrainEndsAtOnceWhenTheHoldingIsLost(t *testing.T) {
	h, l, leader := unled(t)
	require.NoError(t, leader.Lead(t.Context()))
	leader.Drain()
	l.set(func(l *lease) { l.renew = fmt.Errorf("renew: %w", app.ErrLeaseLost) })

	h.clock.Advance(leaderOptions.RenewEvery)

	assert.True(t, isDone(leader), "the drain ended")
	require.NoError(t, leader.Err())
	_, held, _ := leader.Status()
	assert.False(t, held)
	leader.Stop(t.Context())
	l.set(func(l *lease) { assert.Zero(t, l.releases, "nothing is released") })
}

// A drain that begins while the coordinator takes over makes the takeover
// fail, as it ends the holding's context; the leader keeps the holding for
// the drain all the same, and renews it, until Stop releases it.
func TestATakeoverFailingBecauseOfTheDrainKeepsTheHolding(t *testing.T) {
	runner := &stalling{started: make(chan struct{})}
	h := leftOpen(t, func(p *app.Ports) { runner.Turns, p.Turns = p.Turns, runner })
	l, leader := leaderOf(t, h, h.clock)
	led := make(chan error, 1)
	go func() { led <- leader.Lead(t.Context()) }()
	<-runner.started

	leader.Drain()
	require.NoError(t, <-led)
	fenced := opens(h)
	h.clock.Advance(2 * leaderOptions.RenewEvery)
	epoch, held, draining := leader.Status()
	var releases, renewals int
	l.set(func(l *lease) { releases, renewals = l.releases, l.renewals })
	leader.Stop(t.Context())

	require.ErrorIs(t, fenced, app.ErrNotHeld, "the coordinator is fenced")
	assert.Equal(t, int64(1), epoch)
	assert.True(t, held, "the holding is kept")
	assert.True(t, draining)
	assert.Zero(t, releases, "and not released")
	assert.Equal(t, 2, renewals, "but renewed")
	l.set(func(l *lease) { assert.Equal(t, 1, l.releases, "until Stop") })
	assert.True(t, isDone(leader))
}

// Done closes only once the leader released the lease: a release in flight
// keeps it open, so that the leader's owner, which waits on Done, goes on only
// once the lease is free for another instance.
func TestDoneClosesOnlyAfterTheRelease(t *testing.T) {
	_, l, leader := unled(t)
	require.NoError(t, leader.Lead(t.Context()))
	inside, letGo := make(chan struct{}), make(chan struct{})
	l.set(func(l *lease) {
		l.releasing = func() {
			close(inside)
			<-letGo
		}
	})
	stopped := make(chan struct{})
	go func() {
		leader.Stop(t.Context())
		close(stopped)
	}()
	<-inside

	open := !isDone(leader)
	close(letGo)
	<-stopped

	assert.True(t, open, "Done stays open while the release is in flight")
	assert.True(t, isDone(leader), "and closes once it returns")
}

// A leader stopped while a take is in flight waits for the take's answer: the
// take succeeds, the lease it took is released, and only then does Done
// close.
func TestALeaderStoppedDuringATakeReleasesWhatItTook(t *testing.T) {
	_, l, leader := unled(t)
	l.set(func(l *lease) { l.taking, l.waiting = make(chan struct{}), make(chan struct{}, 1) })
	led := make(chan error, 1)
	go func() { led <- leader.Lead(t.Context()) }()
	<-l.waiting

	leader.Stop(t.Context())
	open := !isDone(leader)
	atRelease := make(chan bool, 1)
	l.set(func(l *lease) {
		l.releasing = func() { atRelease <- isDone(leader) }
		close(l.taking)
	})
	require.NoError(t, <-led)

	assert.True(t, open, "Done stays open while the take is in flight")
	select {
	case done := <-atRelease:
		assert.False(t, done, "and while the lease it took is released")
	default:
		t.Error("the lease it took was not released")
	}
	assert.True(t, isDone(leader), "it closes after")
	l.set(func(l *lease) {
		assert.Equal(t, 1, l.takes)
		assert.Equal(t, 1, l.releases)
	})
}

// A leader that takes the lease over a schema this build cannot run on
// releases it, and leads no more: its coordinator never takes over, and Done
// and Err say why (Decision 18).
func TestASchemaThisBuildCannotRunOnAtATakeReleasesAndStopsForGood(t *testing.T) {
	h, takeovers := counting(t)
	l, leader := leaderOf(t, h, h.clock, func(o *app.LeaderOptions) { o.Schema = &schema{err: errIncompatible} })

	err := leader.Lead(t.Context())
	h.clock.Advance(time.Minute)

	require.ErrorIs(t, err, app.ErrSchema)
	assert.Zero(t, takeovers.Load(), "no takeover runs")
	assert.True(t, isDone(leader))
	require.ErrorIs(t, leader.Err(), app.ErrSchema)
	l.set(func(l *lease) {
		assert.Equal(t, 1, l.takes, "no take follows")
		assert.Equal(t, 1, l.releases)
		assert.Zero(t, l.renewals)
	})
}

// A schema this build cannot run on, found at a renewal, stops the leader for
// good without a drain: the old build must not write into a schema it cannot
// run on, so its coordinator is fenced at once and the lease released.
func TestASchemaThisBuildCannotRunOnAtARenewalStopsWithoutDraining(t *testing.T) {
	sch := &schema{}
	h := fresh(t)
	l, leader := leaderOf(t, h, h.clock, func(o *app.LeaderOptions) { o.Schema = sch })
	require.NoError(t, leader.Lead(t.Context()))
	require.NoError(t, opens(h))
	sch.set(errIncompatible)

	h.clock.Advance(leaderOptions.RenewEvery)

	require.ErrorIs(t, opens(h), app.ErrNotHeld, "the coordinator is fenced")
	assert.True(t, isDone(leader))
	require.ErrorIs(t, leader.Err(), app.ErrSchema)
	_, held, draining := leader.Status()
	assert.False(t, held)
	assert.False(t, draining, "with no drain")
	l.set(func(l *lease) {
		assert.Equal(t, 1, l.renewals, "the renewal succeeded")
		assert.Equal(t, 1, l.releases)
	})
	h.clock.Advance(time.Minute)
	l.set(func(l *lease) {
		assert.Equal(t, 1, l.takes, "it leads no more")
		assert.Equal(t, 1, l.renewals)
	})
}

// A schema check that cannot read the schema is tried again: at a take, the
// leader releases the lease rather than lead over a schema it could not
// check, and takes it again later.
func TestASchemaCheckThatCannotReadDoesNotStopTheLeader(t *testing.T) {
	sch := &schema{err: errors.New("the database does not answer")}
	h := fresh(t)
	l, leader := leaderOf(t, h, h.clock, func(o *app.LeaderOptions) { o.Schema = sch })

	require.NoError(t, leader.Lead(t.Context()))
	fenced := opens(h)
	var releases int
	l.set(func(l *lease) { releases = l.releases })
	sch.set(nil)
	h.clock.Advance(leaderOptions.TakeEvery)

	require.ErrorIs(t, fenced, app.ErrNotHeld, "no takeover over a schema it could not check")
	assert.Equal(t, 1, releases, "the lease was released")
	assert.False(t, isDone(leader), "the leader goes on")
	require.NoError(t, opens(h), "it took the lease again")
	l.set(func(l *lease) { assert.Equal(t, int64(2), l.epoch) })
}

// The leader samples the database's clock, the coordinator's, before each
// take, the first included; right after a take, before the takeover reads the
// clock; and after each good renewal (Decision 2).
func TestTheLeaderSamplesTheDatabasesClockAroundEachTakeAndRenewal(t *testing.T) {
	tr := &trace{}
	h := fresh(t, func(p *app.Ports) { p.Records = &takingOver{Records: p.Records, f: func() { tr.note("takeover") }} })
	l, leader := leaderOf(t, h, h.clock, func(o *app.LeaderOptions) { o.Sampler = tr })
	l.set(func(l *lease) { l.held, l.trace = true, tr })

	require.NoError(t, leader.Lead(t.Context()))
	l.set(func(l *lease) { l.held = false })
	h.clock.Advance(leaderOptions.TakeEvery)
	h.clock.Advance(leaderOptions.RenewEvery)
	l.set(func(l *lease) { l.renew = errors.New("the database does not answer") })
	h.clock.Advance(leaderOptions.RenewEvery)

	assert.Equal(t, []string{
		"sample", "take refused",
		"sample", "take", "sample", "takeover",
		"renew", "sample",
		"renew failed",
	}, tr.seen())
}

// leaderMessages are the messages of serve's log that the leader writes, and
// the harness reads (Contract C3).
var leaderMessages = []string{"lease taken", "lease lost", "fenced", "draining", "lease released", "schema incompatible"}

// leaderLines returns the lines of a JSON log whose message is the leader's,
// in order.
func leaderLines(t *testing.T, log string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(log), "\n") {
		var fields map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &fields), line)
		if msg, _ := fields["msg"].(string); slices.Contains(leaderMessages, msg) {
			out = append(out, fields)
		}
	}
	return out
}

// messages returns the lines' messages, in order.
func messages(lines []map[string]any) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line["msg"].(string))
	}
	return out
}

// The leader logs, as JSON lines, what the harness reads of its holdings
// (Contract C3): each take with its epoch and the process's wall time; each
// end of a holding other than its release, as lost, with a reason, and the
// fence that follows; a drain; a release; and a schema this build cannot run
// on, which stops it with no drain.
func TestTheLeaderLogsWhatTheHarnessReads(t *testing.T) {
	for _, c := range []struct {
		name string
		act  func(h *harness, l *lease, leader *app.Leader, sch *schema)
		want []string
	}{
		{"a take", func(*harness, *lease, *app.Leader, *schema) {}, []string{"lease taken"}},
		{"a renewal that found the lease lost", func(h *harness, l *lease, _ *app.Leader, _ *schema) {
			l.set(func(l *lease) { l.renew = fmt.Errorf("renew: %w", app.ErrLeaseLost) })
			h.clock.Advance(leaderOptions.RenewEvery)
		}, []string{"lease taken", "lease lost", "fenced"}},
		{"the watch", func(h *harness, l *lease, _ *app.Leader, _ *schema) {
			l.set(func(l *lease) { l.renew = errors.New("the database does not answer") })
			h.clock.Advance(leaseTTL - leaderOptions.Margin)
		}, []string{"lease taken", "lease lost", "fenced"}},
		{"a write that found the lease gone", func(h *harness, _ *lease, _ *app.Leader, _ *schema) {
			h.records.set(func(int) error { return fmt.Errorf("record: %w", app.ErrLeaseLost) })
			require.ErrorIs(t, opens(h), app.ErrLeaseLost)
			h.clock.Advance(0)
		}, []string{"lease taken", "lease lost", "fenced"}},
		{"a renewal that failed for another reason", func(h *harness, l *lease, _ *app.Leader, _ *schema) {
			l.set(func(l *lease) { l.renew = errors.New("the database does not answer") })
			h.clock.Advance(leaderOptions.RenewEvery)
		}, []string{"lease taken"}},
		{"a drain", func(_ *harness, _ *lease, leader *app.Leader, _ *schema) {
			leader.Drain()
			leader.Stop(t.Context())
		}, []string{"lease taken", "fenced", "draining", "lease released"}},
		{"a schema this build cannot run on, at a renewal", func(h *harness, _ *lease, _ *app.Leader, sch *schema) {
			sch.set(errIncompatible)
			h.clock.Advance(leaderOptions.RenewEvery)
		}, []string{"lease taken", "schema incompatible", "fenced", "lease released"}},
	} {
		logs, sch := &syncBuffer{}, &schema{}
		h := fresh(t)
		l, leader := leaderOf(t, h, h.clock, func(o *app.LeaderOptions) {
			o.Log, o.Schema = slog.New(slog.NewJSONHandler(logs, nil)), sch
			o.Holding = app.NewHolding(leaseTTL, leaderOptions.Margin, h.clock.Now)
		})
		taken := h.clock.Now()
		require.NoError(t, leader.Lead(t.Context()), c.name)

		c.act(h, l, leader, sch)

		lines := leaderLines(t, logs.String())
		assert.Equal(t, c.want, messages(lines), c.name)
		for _, line := range lines {
			msg := line["msg"].(string)
			if msg != "schema incompatible" {
				assert.InDelta(t, 1, line["epoch"], 0, "%s: %s carries its epoch", c.name, msg)
			}
			switch msg {
			case "lease taken":
				wall, _ := line["wall"].(string)
				assert.Regexp(t, `^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{9}Z$`, wall, "%s: RFC 3339, in nanoseconds", c.name)
				at, err := time.Parse(time.RFC3339Nano, wall)
				require.NoError(t, err, c.name)
				assert.True(t, at.Equal(taken), "%s: the process's wall time at the take", c.name)
			case "lease lost":
				assert.NotEmpty(t, line["reason"], c.name)
			case "schema incompatible":
				assert.Equal(t, errIncompatible.Error(), line["reason"], c.name)
			}
		}
	}
}

// A drain that begins while a take is in flight takes the lease no more: a
// take that then succeeds is released at once, and no coordinator takes over
// under it; one that is refused is followed by no other (Decision 6).
func TestADrainWithATakeInFlightTakesNoMore(t *testing.T) {
	for _, c := range []struct {
		name     string
		free     bool
		releases int
	}{
		{"a take that succeeds", true, 1},
		{"a take that is refused", false, 0},
	} {
		h, n := counting(t)
		l, leader := leaderOf(t, h, h.clock)
		l.set(func(l *lease) { l.held, l.taking, l.waiting = !c.free, make(chan struct{}), make(chan struct{}, 1) })
		led := make(chan error, 1)
		go func() { led <- leader.Lead(t.Context()) }()
		<-l.waiting

		leader.Drain()
		l.set(func(l *lease) {
			close(l.taking)
			l.taking = nil
		})
		require.NoError(t, <-led, c.name)
		var releases int
		l.set(func(l *lease) { releases = l.releases })
		_, held, draining := leader.Status()
		h.clock.Advance(3 * leaderOptions.TakeEvery)

		assert.Equal(t, c.releases, releases, "%s: released at once, or nothing to release", c.name)
		assert.Zero(t, n.Load(), "%s: no takeover", c.name)
		assert.False(t, held, c.name)
		assert.True(t, draining, c.name)
		l.set(func(l *lease) { assert.Equal(t, 1, l.takes, "%s: no take follows", c.name) })
	}
}

// sampler is the leader's ClockSampler: fails says whether its nth sample,
// counting from 1, fails.
type sampler struct {
	mu    sync.Mutex
	n     int
	fails func(n int) bool
}

func (s *sampler) Sample(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	if s.fails(s.n) {
		return errors.New("the database does not answer")
	}
	return nil
}

// The coordinator judges its records by the database's clock, which only a
// good sample sets. A leader whose sample after a take fails, when no sample
// has ever succeeded, gives the lease up rather than have its coordinator
// take over on the process's clock, and takes it again after TakeEvery. A
// clock sampled before keeps its estimate: the takeover runs, though the
// sample after the take fails.
func TestALeaderWhoseClockWasNeverSampledGivesTheLeaseUp(t *testing.T) {
	h, n := counting(t)
	l, leader := leaderOf(t, h, h.clock, func(o *app.LeaderOptions) {
		o.Sampler = &sampler{fails: func(n int) bool { return n <= 2 }}
	})

	require.NoError(t, leader.Lead(t.Context()))
	unsampled, takenOver := opens(h), n.Load()
	var releases int
	l.set(func(l *lease) { releases = l.releases })
	h.clock.Advance(leaderOptions.TakeEvery)

	require.ErrorIs(t, unsampled, app.ErrNotHeld)
	assert.Zero(t, takenOver, "no takeover on a clock never sampled")
	assert.Equal(t, 1, releases, "the lease was given up")
	require.NoError(t, opens(h), "it took the lease again, its clock sampled")
	assert.Equal(t, int32(1), n.Load())
	l.set(func(l *lease) { assert.Equal(t, int64(2), l.epoch) })

	h, n = counting(t)
	l, leader = leaderOf(t, h, h.clock, func(o *app.LeaderOptions) {
		o.Sampler = &sampler{fails: func(n int) bool { return n == 2 }}
	})
	require.NoError(t, leader.Lead(t.Context()))

	require.NoError(t, opens(h), "a clock sampled before the take takes over")
	assert.Equal(t, int32(1), n.Load())
	l.set(func(l *lease) { assert.Zero(t, l.releases) })
}
