package pages

import (
	"context"
	"time"
)

// holderTimeout bounds a standby's read of the lease's holder, which a
// page waits on: a read that takes longer shows the leader as not known
// (review I1). Tests set it.
var holderTimeout = time.Second

// holderWaiting is called as a board page begins to wait for the read of the
// lease's holder that runs (see boardLeader): nothing, but in tests, which
// see the waiters by it.
var holderWaiting = func() {}

// holderKept is how long the board pages keep a standby's read of the
// lease's holder (R139), so that their polls on a standby read it once a
// second at most.
const holderKept = time.Second

// standbyView is a standby's notice (R-b; R90): the leader's name, the
// lease's holder, which is the deployment's holder name, not a URL; or
// that no instance holds the lease now; or that it could not be read.
type standbyView struct {
	Leader string
	None   bool
	Unread bool
}

// leader reads the lease's holder for a standby's notice, within
// holderTimeout, so that a slow store never holds the page. A hold past
// its expiry is no one's: the next instance to take the lease leads.
func (s *site) leader(ctx context.Context) *standbyView {
	if s.p.LeaseHolder == nil {
		return &standbyView{Unread: true}
	}
	read, cancel := context.WithTimeout(ctx, holderTimeout)
	defer cancel()
	name, expires, err := s.p.LeaseHolder.Holder(read)
	switch {
	case err != nil:
		s.log.WarnContext(ctx, "the web interface could not read the lease's holder", "err", err)
		return &standbyView{Unread: true}
	case name == "" || !s.now().Before(expires):
		return &standbyView{None: true}
	}
	return &standbyView{Leader: name}
}

// holderRead is the board pages' read of the lease's holder that runs: done
// is closed once it ended, its view then the notice it gives.
type holderRead struct {
	done chan struct{}
	view *standbyView
}

// boardLeader is a standby's notice on a board page (R139): the board pages'
// own read of the lease's holder, as leader reads it, 9a-1's read on the main
// pool within holderTimeout, kept holderKept from the read's end on the
// monotonic clock, so that a slow read is not stale when it is kept (Task
// 5's review M1), a read that failed or found no holder kept as well. One
// read runs at a time: a page that finds none kept starts it, apart from its
// own context's end, which holderTimeout bounds; a page that arrives while it
// runs waits for it, within its own context, and reads as not known if that
// ends first. Each page gets its own copy.
func (s *site) boardLeader(ctx context.Context) *standbyView {
	s.mu.Lock()
	if since := s.mono().Sub(s.holderAt); s.holder != nil && since >= 0 && since < holderKept {
		v := *s.holder
		s.mu.Unlock()
		return &v
	}
	if r := s.reading; r != nil {
		s.mu.Unlock()
		holderWaiting()
		select {
		case <-r.done:
			v := *r.view
			return &v
		case <-ctx.Done():
			return &standbyView{Unread: true}
		}
	}
	r := &holderRead{done: make(chan struct{}), view: &standbyView{Unread: true}}
	s.reading = r
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.holder, s.holderAt, s.reading = r.view, s.mono(), nil
		s.mu.Unlock()
		close(r.done)
	}()
	r.view = s.leader(context.WithoutCancel(ctx))
	v := *r.view
	return &v
}
