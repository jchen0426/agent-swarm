package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// LeaderOptions are how a leader holds the lease (spec Section 15).
type LeaderOptions struct {
	// RenewEvery is how often the leader renews its holding [5 s], from
	// when the last renewal was sent; a renewal that has not answered by
	// then failed.
	RenewEvery time.Duration
	// Margin is how long before its holding could expire the leader fences
	// its coordinator [5 s]: once the last renewal that succeeded was sent
	// the lease's TTL less Margin ago.
	Margin time.Duration
	// TakeEvery is how often the leader tries to take a lease another
	// instance holds [5 s].
	TakeEvery time.Duration
	// Log receives what failed; nil logs nothing.
	Log *slog.Logger
}

// Leader takes the platform's lease and holds it, and has its coordinator
// act only while it does (spec Section 8.2): the coordinator takes over
// each time the leader takes the lease, and is fenced once a renewal finds
// the lease lost, once the last renewal that succeeded is too old for the
// holding to be sure, or once a write finds the lease gone. A fenced
// leader tries to take the lease again. It stops for good when it is
// stopped, when the context it leads under ends, or when a takeover of its
// coordinator is refused because the records hold a version this build cannot
// read; Done and Err say so.
type Leader struct {
	lease  Lease
	coord  *Coordinator
	clock  Clock
	timers Timers
	opts   LeaderOptions
	ttl    time.Duration
	log    *slog.Logger
	// done is closed, with mu held, once the leader stops for good.
	done chan struct{}

	mu      sync.Mutex
	ctx     context.Context
	held    bool
	stopped bool
	// err is the refusal that stopped the leader for good, when one did.
	err error
	// holding counts the leader's holdings, so that a renewal or a watch
	// of an earlier holding changes nothing; term is the latest's.
	holding int
	term    Term
	// good is when the last renewal that succeeded was sent, or the lease
	// taken.
	good    time.Time
	release context.CancelFunc
	// renewing, watching and taking cancel the timer of each kind.
	renewing, watching, taking func()
}

// NewLeader returns a leader of coord over lease.
func NewLeader(lease Lease, coord *Coordinator, clock Clock, timers Timers, opts *LeaderOptions) (*Leader, error) {
	ttl := lease.TTL()
	if opts.RenewEvery <= 0 || opts.Margin <= 0 || opts.TakeEvery <= 0 || ttl-opts.Margin <= opts.RenewEvery {
		return nil, errors.New("app: a leader renews its lease more often than the lease's time less the margin")
	}
	l := &Leader{lease: lease, coord: coord, clock: clock, timers: timers, opts: *opts, ttl: ttl, log: opts.Log, done: make(chan struct{}), ctx: context.Background()}
	if l.log == nil {
		l.log = slog.New(slog.DiscardHandler)
	}
	coord.OnLost(l.lost)
	return l, nil
}

// Lead starts leading: it tries to take the lease now, and again every
// TakeEvery while another instance holds it. It stops leading, as Stop
// does, once ctx ends. It returns once its first take is over, with an error
// only when its coordinator refused the records it would act on because they
// hold a version this build cannot read: it released the lease and leads no
// more. A later take may be refused so too, after Lead has returned: Done
// and Err tell the owner of that refusal, and of this first one as well.
func (l *Leader) Lead(ctx context.Context) error {
	l.mu.Lock()
	l.ctx = ctx
	l.mu.Unlock()
	context.AfterFunc(ctx, func() { l.Stop(ctx) })
	return l.take()
}

// Done is closed once the leader stops for good: Stop was called, the context
// Lead leads under ended, or a takeover was refused because the records hold
// a version this build cannot read. A leader fenced for any other reason
// takes the lease again, and has not stopped. Err says what stopped it.
func (l *Leader) Done() <-chan struct{} { return l.done }

// Err is nil until Done is closed. After that it is the refusal that stopped
// the leader, which wraps item.ErrVersion, or nil when Stop or the end of the
// leader's context stopped it. The first stop stands: Err never changes once
// Done is closed.
func (l *Leader) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Stop stops leading, for good: the coordinator is fenced, the lease released
// and Done closed.
func (l *Leader) Stop(ctx context.Context) {
	l.mu.Lock()
	held := l.held
	l.end(nil)
	l.lose()
	l.mu.Unlock()
	if held {
		l.giveUp(ctx)
	}
}

// take takes the lease, and has the coordinator take over under the new
// holding, which is renewed and watched from before the takeover starts:
// a takeover may take long. A leader stopped meanwhile gives the lease up.
func (l *Leader) take() error {
	l.mu.Lock()
	if l.stopped || l.held {
		l.mu.Unlock()
		return nil
	}
	ctx := l.ctx
	l.mu.Unlock()
	sent := l.clock.Now()
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
	holding, release := context.WithCancel(ctx)
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		release()
		l.giveUp(ctx)
		return nil
	}
	l.holding++
	n := l.holding
	l.held, l.term, l.good, l.release = true, term, sent, release
	l.renewAt(n, sent)
	l.watchAt(n)
	l.mu.Unlock()
	err = l.coord.TakeOver(holding, term)
	if err == nil {
		return nil
	}
	version := errors.Is(err, item.ErrVersion)
	l.mu.Lock()
	if version {
		l.end(err)
	}
	ours := l.held && l.holding == n
	if ours {
		l.lose()
	}
	l.mu.Unlock()
	if !ours && !version {
		// The holding ended while the coordinator took over: Stop gave
		// the lease up, or the holding was lost.
		return nil
	}
	l.giveUp(ctx)
	switch {
	case version:
		l.log.ErrorContext(ctx, "the records hold a version this build cannot read; it leads no more", "err", err)
		return err
	case ctx.Err() == nil:
		l.log.ErrorContext(ctx, "the coordinator could not take over", "err", err)
	}
	return nil
}

// giveUp releases the lease, under a context of its own, which ctx's end
// does not end: the leader's context may have ended, as when its instance
// stops, and a release refused would leave the lease held until it expires.
func (l *Leader) giveUp(ctx context.Context) {
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.opts.RenewEvery)
	defer cancel()
	if err := l.lease.Release(release); err != nil {
		l.log.ErrorContext(ctx, "the lease could not be released; it expires", "err", err)
	}
}

// renew renews holding n. The next renewal is armed RenewEvery after this
// one is sent, so that a slow renewal delays none after it. A renewal that
// finds the lease lost fences the coordinator; one that fails otherwise is
// left to the next, and the watch fences the coordinator if none succeeds
// in time.
func (l *Leader) renew(n int) {
	l.mu.Lock()
	if !l.held || l.holding != n {
		l.mu.Unlock()
		return
	}
	ctx := l.ctx
	sent := l.clock.Now()
	l.renewAt(n, sent)
	l.mu.Unlock()
	renewal, cancel := context.WithTimeout(ctx, l.opts.RenewEvery)
	err := l.lease.Renew(renewal)
	cancel()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held || l.holding != n {
		return
	}
	switch {
	case errors.Is(err, ErrLeaseLost):
		l.log.ErrorContext(ctx, "the lease was lost; the coordinator stops", "err", err)
		l.lose()
	case err != nil:
		l.log.ErrorContext(ctx, "the lease could not be renewed; the next renewal tries again", "err", err)
	case sent.After(l.good):
		l.good = sent
	}
}

// watch fences the coordinator once the last renewal of holding n that
// succeeded was sent TTL less Margin ago, before the holding could expire.
func (l *Leader) watch(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held || l.holding != n {
		return
	}
	if !l.clock.Now().Before(l.good.Add(l.ttl - l.opts.Margin)) {
		l.log.ErrorContext(l.ctx, "the lease could not be renewed in time; the coordinator stops")
		l.lose()
		return
	}
	l.watchAt(n)
}

// lost is what the coordinator calls once a write of its found the lease
// gone, under the holding of epoch: once the leader holds the lease again,
// under another epoch, it changes nothing.
func (l *Leader) lost(epoch int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held && l.term.Epoch == epoch {
		l.lose()
	}
}

// end stops the leader for good, once: it takes the lease no more, and Done
// is closed. err is the refusal that stopped it, or nil. A leader that
// stopped already stays so, with the error it stopped with. It is called with
// the leader's lock held.
func (l *Leader) end(err error) {
	if l.stopped {
		return
	}
	l.stopped, l.err = true, err
	close(l.done)
}

// lose ends the holding and fences the coordinator, in that order, then,
// unless the leader stopped, tries to take the lease again. Ending the
// holding cancels the context the coordinator acts under, so that a write
// waiting on a store that hangs gives way, and the fence, which waits for the
// coordinator's lock, does not wait for the store. It is called with the
// leader's lock held; the coordinator never calls the leader under its own.
func (l *Leader) lose() {
	l.held = false
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
	l.coord.Fence()
	l.retake()
}

// retake tries to take the lease again after TakeEvery, unless the leader
// stopped. It is called with the leader's lock held.
func (l *Leader) retake() {
	if l.stopped {
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
