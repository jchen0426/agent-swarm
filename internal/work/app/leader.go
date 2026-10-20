package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// LeaderOptions are how a leader holds the lease (spec Section 15).
type LeaderOptions struct {
	// RenewEvery is how often the leader renews its holding [5 s], from
	// when the last renewal was sent; a renewal that has not answered by
	// then failed.
	RenewEvery time.Duration
	// Margin is how long before its holding could expire the leader fences
	// its coordinator [5 s]: once the last renewal that succeeded, or,
	// before any, the take, was sent the lease's TTL less Margin ago.
	Margin time.Duration
	// TakeEvery is how often the leader tries to take a lease another
	// instance holds [5 s].
	TakeEvery time.Duration
	// Log receives the leader's account of its holdings, which serve's log
	// carries (Contract C3), and what failed; nil logs nothing.
	Log *slog.Logger
	// Holding is where the leader publishes its holding, which the runner
	// and the gateways ask; its TTL and margin are the lease's and Margin.
	// Nil: the leader makes its own.
	Holding *Holding
	// Sampler samples the database's clock, the coordinator's, before each
	// take, after a take before the takeover, and after each good renewal;
	// Schema checks, after each take and each good renewal, that this build
	// runs on the database's schema. Nil: none; serve gives both.
	Sampler ClockSampler
	Schema  Schema
}

// The reasons for which a holding ends other than by its release, which
// "lease lost" logs.
const (
	lostByRenewal = "a renewal found the lease lost"
	lostByWatch   = "no renewal succeeded in time: the holding could have expired"
	lostByWrite   = "a write found the lease gone"
)

// wallFormat is RFC 3339 in nanoseconds, in which "lease taken" logs the
// process's wall time at the take.
const wallFormat = "2006-01-02T15:04:05.000000000Z07:00"

// Leader takes the platform's lease and holds it, and has its coordinator
// act only while it does (spec Section 8.2): the coordinator takes over
// each time the leader takes the lease, and is fenced once a renewal finds
// the lease lost, once the last renewal that succeeded is too old for the
// holding to be sure, or once a write finds the lease gone. A fenced
// leader tries to take the lease again. A draining leader keeps its holding
// for the gateways' calls, with its coordinator fenced, and takes the lease
// no more (Decision 6). The leader stops for good when it is stopped, when
// the context it leads under ends, when a drain's holding is lost, or when a
// take or a renewal finds a schema this build cannot run on (Decision 18);
// Done and Err say so. Records and board events of a later version do not
// stop it: its coordinator skips their items (Decision 19). The leader
// publishes its holding into a Holding, which anyone may read without a lock.
// Its lock comes before the coordinator's: nothing the coordinator runs under
// its own lock calls the leader, but Holding.Valid, which takes no lock.
type Leader struct {
	lease  Lease
	coord  *Coordinator
	clock  Clock
	timers Timers
	opts   LeaderOptions
	ttl    time.Duration
	log    *slog.Logger
	hold   *Holding
	// done is closed, with mu held, once the leader stopped for good and is
	// quiet (settle).
	done chan struct{}
	// sampled says a sample of the database's clock has succeeded.
	sampled atomic.Bool

	mu       sync.Mutex
	ctx      context.Context
	held     bool
	draining bool
	stopped  bool
	closed   bool
	// err is what stopped the leader for good, when a refusal did.
	err error
	// busy counts the takes, renewals and releases in flight, which Done
	// waits for.
	busy int
	// holdings counts the leader's holdings, so that a renewal or a watch
	// of an earlier holding changes nothing; term is the latest's.
	holdings int
	term     Term
	// good is when the last renewal that succeeded was sent, or, before
	// any, when the take was sent, by the leader's clock, which is
	// monotonic: the watch reads the same clock, so that no offset to the
	// database's clock and no step of the wall clock makes the watch late.
	// The database starts or extends a holding no earlier than the send.
	// goodWall is the wall clock's reading at that send, which Valid reads
	// as well: the monotonic clock stops while the host sleeps.
	good, goodWall time.Time
	release        context.CancelFunc
	// renewing, watching and taking cancel the timer of each kind.
	renewing, watching, taking func()
}

// NewLeader returns a leader of coord over lease.
func NewLeader(lease Lease, coord *Coordinator, clock Clock, timers Timers, opts *LeaderOptions) (*Leader, error) {
	ttl := lease.TTL()
	if opts.RenewEvery <= 0 || opts.Margin <= 0 || opts.TakeEvery <= 0 || ttl-opts.Margin <= opts.RenewEvery {
		return nil, errors.New("app: a leader renews its lease more often than the lease's time less the margin")
	}
	hold := opts.Holding
	switch {
	case hold == nil:
		hold = NewHolding(ttl, opts.Margin, nil)
	case hold.ttl != ttl || hold.margin != opts.Margin:
		return nil, errors.New("app: a holding counts by the lease's time and the leader's margin")
	}
	l := &Leader{lease: lease, coord: coord, clock: clock, timers: timers, opts: *opts, ttl: ttl, log: opts.Log, hold: hold, done: make(chan struct{}), ctx: context.Background()}
	if l.log == nil {
		l.log = slog.New(slog.DiscardHandler)
	}
	coord.OnLost(l.lost)
	return l, nil
}

// Lead starts leading: it tries to take the lease now, and again every
// TakeEvery while another instance holds it. It stops leading, as Stop does,
// once ctx ends. It returns once its first take is over, with an error only
// when that take stopped the leader for good: the schema is one this build
// cannot run on. The lease is released then, and the leader leads no more. A
// later take, or a renewal, may stop it so too, after Lead has returned: Done
// and Err tell the owner of that stop, and of this first one as well.
func (l *Leader) Lead(ctx context.Context) error {
	l.mu.Lock()
	l.ctx = ctx
	l.mu.Unlock()
	context.AfterFunc(ctx, func() { l.Stop(ctx) })
	return l.take()
}

// Done is closed once the leader stopped for good and has released the
// lease, or found it lost, with no take, renewal or release in flight: Stop
// was called, the context Lead leads under ended, a drain's holding was lost,
// or a take or a renewal found a schema this build cannot run on. A leader
// fenced for any other reason takes the lease again, and has not stopped. Err
// says what stopped it.
func (l *Leader) Done() <-chan struct{} { return l.done }

// Err is nil until Done is closed. After that it is the refusal that stopped
// the leader, which wraps ErrSchema, or nil when Stop, the end of the
// leader's context or the loss of a drain's holding stopped it. The first
// stop stands: Err never changes once Done is closed.
func (l *Leader) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		return nil
	}
	return l.err
}

// Status reports the leader's holding: its epoch, the latest holding's,
// whether the leader holds the lease, and whether it drains. It reads what
// the leader published, and takes no lock, so that it never waits for a
// takeover.
func (l *Leader) Status() (epoch int64, held, draining bool) {
	return l.hold.status()
}

// Drain starts draining, as the instance shuts down (Decision 6): the
// holding's context ends and the coordinator is fenced, but the leader keeps
// the holding, renews and watches it, so that the gateways' calls under it
// finish, until Stop releases it. A draining leader never takes the lease: a
// take in flight that succeeds is released at once, and a takeover in
// progress that fails because of the drain keeps its holding for the drain. A
// holding lost while the leader drains ends the drain at once, with nothing
// left to release: the leader stops, and Done closes.
func (l *Leader) Drain() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped || l.draining {
		return
	}
	if l.release != nil {
		l.release()
		l.release = nil
	}
	l.draining = true
	l.publish()
	if l.taking != nil {
		l.taking()
		l.taking = nil
	}
	var epoch int64
	if l.held {
		epoch = l.term.Epoch
		l.coord.Fence()
		l.log.InfoContext(l.ctx, "fenced", "epoch", epoch)
	}
	l.log.InfoContext(l.ctx, "draining", "epoch", epoch)
}

// Stop stops leading, for good: the coordinator is fenced and the lease
// released before Stop returns. A take in flight is let finish, and what it
// took is released then: Done closes once all is.
func (l *Leader) Stop(ctx context.Context) {
	l.mu.Lock()
	held, epoch := l.held, l.term.Epoch
	l.end(nil)
	if held {
		l.busy++
		l.lose(ctx)
	}
	l.settle()
	l.mu.Unlock()
	if held {
		l.giveUp(ctx, epoch)
		l.idle()
	}
}

// take takes the lease, and has the coordinator take over under the new
// holding, which is renewed and watched from before the takeover starts: a
// takeover may take long. It samples the database's clock before the take,
// and again after it, so that the coordinator takes over on a fresh clock,
// and checks the schema before the takeover. A clock that no sample has ever
// set gives the lease up, to be taken again. The holding counts from when
// the take was sent, as a renewal counts from its own send, never from its
// time in the database, which the leader's clock cannot place: a holding that
// Valid refuses when its takeover would start ends at once, unless a renewal
// answered first, and the leader takes the lease again once it expired. A
// leader stopped, or draining, meanwhile gives up what it took; one draining
// keeps a holding it took before, and does not take over.
func (l *Leader) take() error {
	l.mu.Lock()
	if l.stopped || l.draining || l.held {
		l.mu.Unlock()
		return nil
	}
	ctx := l.ctx
	l.busy++
	l.mu.Unlock()
	defer l.idle()

	l.sample(ctx)
	sent, wall := l.clock.Now(), l.hold.wall()
	term, took, err := l.lease.Take(ctx)
	if err != nil || !took {
		if err != nil {
			l.log.ErrorContext(ctx, "the lease could not be taken", "err", err)
		}
		l.mu.Lock()
		l.retake()
		l.mu.Unlock()
		return nil
	}
	l.log.InfoContext(ctx, "lease taken", "epoch", term.Epoch, "wall", wall.UTC().Format(wallFormat))
	holding, release := context.WithCancel(ctx)
	l.mu.Lock()
	if l.stopped || l.draining {
		l.mu.Unlock()
		release()
		l.giveUp(ctx, term.Epoch)
		return nil
	}
	l.holdings++
	n := l.holdings
	l.held, l.term, l.good, l.goodWall, l.release = true, term, sent, wall, release
	l.publish()
	l.renewAt(n, sent)
	l.watchAt(n)
	l.mu.Unlock()

	sampled := l.sample(ctx)
	checked := l.check(ctx)
	if errors.Is(checked, ErrSchema) {
		l.incompatible(ctx, n, checked)
		return checked
	}
	takeOver, abandoned := l.mayTakeOver(ctx, n, sampled, checked)
	if abandoned != 0 {
		l.giveUp(ctx, abandoned)
	}
	if !takeOver {
		return nil
	}
	if err = l.coord.TakeOver(holding, term); err != nil {
		l.tookOver(ctx, n, err)
	}
	return nil
}

// mayTakeOver decides whether the coordinator takes over holding n, once the
// database's clock was sampled after the take, and the schema check returned
// err, which is not ErrSchema's. A holding that ended meanwhile, or that a
// drain keeps, is not taken over. A clock that no sample has ever set, or a
// check that could not read, gives the lease up, to be taken again after
// TakeEvery: it returns the epoch to release. A holding that Valid refuses, by
// the leader's monotonic clock or by the wall clock, ends at once.
func (l *Leader) mayTakeOver(ctx context.Context, n int, sampled bool, err error) (takeOver bool, abandoned int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case !l.held || l.holdings != n:
		return false, 0
	case l.draining:
		if err != nil {
			l.log.ErrorContext(ctx, "the schema could not be checked; the next renewal checks it again", "err", err)
		}
		return false, 0
	case !sampled:
		l.log.ErrorContext(ctx, "the database's clock was never sampled; the lease is given up")
	case err != nil:
		l.log.ErrorContext(ctx, "the schema could not be checked; the lease is given up", "err", err)
	case !l.hold.Valid(l.term.Epoch):
		l.loseFor(ctx, lostByWatch)
		return false, 0
	default:
		return true, 0
	}
	l.lose(ctx)
	return false, l.term.Epoch
}

// tookOver acts on a takeover of holding n that failed with err. A write that
// found the lease gone ends the holding, with nothing to release; a drain that
// made the takeover fail keeps it; any other failure gives the lease up, to
// be taken again. No failure stops the leader for good: records or board
// events of a later version do not fail a takeover (Decision 19).
func (l *Leader) tookOver(ctx context.Context, n int, err error) {
	l.mu.Lock()
	switch {
	case !l.held || l.holdings != n:
		// The holding ended while the coordinator took over: Stop gave
		// the lease up, or the holding was lost.
		l.mu.Unlock()
		return
	case errors.Is(err, ErrLeaseLost):
		l.loseFor(ctx, lostByWrite)
		l.mu.Unlock()
		return
	case l.draining:
		l.mu.Unlock()
		return
	case ctx.Err() == nil:
		l.log.ErrorContext(ctx, "the coordinator could not take over", "err", err)
	}
	epoch := l.term.Epoch
	l.lose(ctx)
	l.mu.Unlock()
	l.giveUp(ctx, epoch)
}

// incompatible stops the leader for good over a schema this build cannot run
// on, which the check after holding n's take or renewal found: it fences the
// coordinator and releases the lease, with no drain, since the build must not
// write into a schema it cannot run on (Decision 18).
func (l *Leader) incompatible(ctx context.Context, n int, err error) {
	l.mu.Lock()
	l.log.ErrorContext(ctx, "schema incompatible", "reason", err.Error())
	l.end(err)
	ours, epoch := l.held && l.holdings == n, l.term.Epoch
	if ours {
		l.lose(ctx)
	}
	l.mu.Unlock()
	if ours {
		l.giveUp(ctx, epoch)
	}
}

// giveUp releases holding epoch, under a context of its own, which ctx's end
// does not end: the leader's context may have ended, as when its instance
// stops, and a release refused would leave the lease held until it expires.
func (l *Leader) giveUp(ctx context.Context, epoch int64) {
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.opts.RenewEvery)
	defer cancel()
	if err := l.lease.Release(release); err != nil {
		l.log.ErrorContext(ctx, "the lease could not be released; it expires", "err", err)
		return
	}
	l.log.InfoContext(ctx, "lease released", "epoch", epoch)
}

// renew renews holding n. The next renewal is armed RenewEvery after this
// one is sent, so that a slow renewal delays none after it. A renewal that
// finds the lease lost ends the holding; one that fails otherwise is left to
// the next, and the watch ends the holding if none succeeds in time. After a
// good renewal, the leader samples the database's clock and checks the
// schema: a schema this build cannot run on stops it for good, and a check
// that cannot read is tried again at the next renewal.
func (l *Leader) renew(n int) {
	l.mu.Lock()
	if !l.held || l.holdings != n {
		l.mu.Unlock()
		return
	}
	ctx := l.ctx
	sent, wall := l.clock.Now(), l.hold.wall()
	l.renewAt(n, sent)
	l.busy++
	l.mu.Unlock()
	defer l.idle()

	renewal, cancel := context.WithTimeout(ctx, l.opts.RenewEvery)
	err := l.lease.Renew(renewal)
	cancel()
	if !l.renewed(ctx, n, sent, wall, err) {
		return
	}
	l.sample(ctx)
	switch err := l.check(ctx); {
	case errors.Is(err, ErrSchema):
		l.incompatible(ctx, n, err)
	case err != nil:
		l.log.ErrorContext(ctx, "the schema could not be checked; the next renewal checks it again", "err", err)
	}
}

// renewed takes in a renewal of holding n, sent at sent by the leader's clock
// and at wall by the wall clock, that returned err, and reports whether it
// succeeded and the holding still holds.
func (l *Leader) renewed(ctx context.Context, n int, sent, wall time.Time, err error) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held || l.holdings != n {
		return false
	}
	switch {
	case errors.Is(err, ErrLeaseLost):
		l.loseFor(ctx, lostByRenewal)
		return false
	case err != nil:
		l.log.ErrorContext(ctx, "the lease could not be renewed; the next renewal tries again", "err", err)
		return false
	}
	if sent.After(l.good) {
		l.good, l.goodWall = sent, wall
		l.publish()
	}
	return true
}

// watch ends holding n once its last good renewal, or, before any, its take,
// was sent TTL less Margin ago, before the holding could expire, whatever the
// offset between the leader's clock and the database's (see good).
func (l *Leader) watch(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held || l.holdings != n {
		return
	}
	if !l.fresh(l.clock.Now()) {
		l.loseFor(l.ctx, lostByWatch)
		return
	}
	l.watchAt(n)
}

// fresh reports whether the holding's last good renewal, or its take, was
// sent less than TTL less Margin before now, by the leader's clock. It is
// called with the leader's lock held.
func (l *Leader) fresh(now time.Time) bool {
	return now.Before(l.good.Add(l.ttl - l.opts.Margin))
}

// lost is what the coordinator calls once a write of its found the lease
// gone, under the holding of epoch: once the leader holds the lease again,
// under another epoch, it changes nothing.
func (l *Leader) lost(epoch int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held && l.term.Epoch == epoch {
		l.loseFor(l.ctx, lostByWrite)
	}
}

// loseFor ends a holding found lost, or that could have expired, for reason,
// which "lease lost" logs. A drain ends with it, at once: nothing is left to
// release. It is called with the leader's lock held.
func (l *Leader) loseFor(ctx context.Context, reason string) {
	l.log.ErrorContext(ctx, "lease lost", "epoch", l.term.Epoch, "reason", reason)
	if l.draining {
		l.end(nil)
	}
	l.lose(ctx)
	l.settle()
}

// end stops the leader for good, once: it takes the lease no more. err is
// the refusal that stopped it, or nil. A leader that stopped already stays
// so, with what it stopped with. Done closes once the leader is quiet
// (settle). It is called with the leader's lock held.
func (l *Leader) end(err error) {
	if l.stopped {
		return
	}
	l.stopped, l.err = true, err
	if l.taking != nil {
		l.taking()
		l.taking = nil
	}
}

// lose ends the holding: Valid says no from here on, its timers are
// cancelled, its context ends, and the coordinator is fenced, in that order,
// unless a drain fenced it already. Ending the context, which the coordinator
// acts under, lets a write waiting on a store that hangs give way, so that the
// fence, which waits for the coordinator's lock, does not wait for the store.
// Then, unless the leader stopped or drains, it tries to take the lease
// again. It is called with the leader's lock held; the coordinator never
// calls the leader under its own.
func (l *Leader) lose(ctx context.Context) {
	l.held = false
	l.publish()
	for _, cancel := range []func(){l.renewing, l.watching, l.taking} {
		if cancel != nil {
			cancel()
		}
	}
	l.renewing, l.watching, l.taking = nil, nil, nil
	if l.release != nil {
		l.release()
		l.release = nil
	}
	if !l.draining {
		l.coord.Fence()
		l.log.InfoContext(ctx, "fenced", "epoch", l.term.Epoch)
	}
	l.retake()
}

// settle closes Done once the leader stopped for good and is quiet: it holds
// nothing, and no take, renewal or release is in flight. It is called with
// the leader's lock held.
func (l *Leader) settle() {
	if l.stopped && !l.held && l.busy == 0 && !l.closed {
		l.closed = true
		close(l.done)
	}
}

// idle ends a take, a renewal or a release in flight.
func (l *Leader) idle() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.busy--
	l.settle()
}

// publish publishes the holding as it stands, for Holding.Valid and Status.
// It is called with the leader's lock held, whenever the holding changes.
func (l *Leader) publish() {
	l.hold.publish(&snapshot{epoch: l.term.Epoch, held: l.held, draining: l.draining, clock: l.clock, mono: l.good, wall: l.goodWall})
}

// sample samples the database's clock, when the leader has a sampler, for
// RenewEvery at most. A sample that fails leaves the clock as it was. It
// reports whether a sample has ever succeeded, this one or an earlier one; a
// leader with no sampler has no clock to set, and always reports so.
func (l *Leader) sample(ctx context.Context) bool {
	if l.opts.Sampler == nil {
		return true
	}
	sampling, cancel := context.WithTimeout(ctx, l.opts.RenewEvery)
	defer cancel()
	if err := l.opts.Sampler.Sample(sampling); err != nil {
		l.log.WarnContext(ctx, "the database's clock could not be sampled", "err", err)
	} else {
		l.sampled.Store(true)
	}
	return l.sampled.Load()
}

// check checks the schema, when the leader has a check, for RenewEvery at
// most.
func (l *Leader) check(ctx context.Context) error {
	if l.opts.Schema == nil {
		return nil
	}
	checking, cancel := context.WithTimeout(ctx, l.opts.RenewEvery)
	defer cancel()
	return l.opts.Schema.Check(checking)
}

// retake tries to take the lease again after TakeEvery, unless the leader
// stopped or drains. It is called with the leader's lock held.
func (l *Leader) retake() {
	if l.stopped || l.draining {
		return
	}
	if l.taking != nil {
		l.taking()
	}
	l.taking = l.timers.At(l.clock.Now().Add(l.opts.TakeEvery), func() { _ = l.take() })
}

// renewAt arms holding n's next renewal RenewEvery after sent, and watchAt
// its watch at the time its last good renewal says. Both are called with
// the leader's lock held.
func (l *Leader) renewAt(n int, sent time.Time) {
	l.renewing = l.timers.At(sent.Add(l.opts.RenewEvery), func() { l.renew(n) })
}

func (l *Leader) watchAt(n int) {
	l.watching = l.timers.At(l.good.Add(l.ttl-l.opts.Margin), func() { l.watch(n) })
}
