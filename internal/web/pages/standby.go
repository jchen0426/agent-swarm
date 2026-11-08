package pages

import (
	"context"
	"time"
)

// holderTimeout bounds a standby's read of the lease's holder, which a
// page waits on: a read that takes longer shows the leader as not known
// (review I1).
const holderTimeout = time.Second

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
