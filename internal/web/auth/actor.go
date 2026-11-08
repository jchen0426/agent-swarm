package auth

import (
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// leeway is the fixed allowance for clocks on freshness and on a session's
// issue time (design D4).
const leeway = 60 * time.Second

// SubjectPrefix prefixes a person's subject claim in the subject the
// coordinator records, keeping it apart from the loopback token's admin.
const SubjectPrefix = "sso:"

// Actor is the actor a session acts as (R76): its subject as sso:<value>;
// a responder if it holds the responder or the admin role, since every
// admin is a responder (I1); and an admin only while fresh, its auth_time
// within web.freshWindow plus the fixed leeway of now. The coordinator
// decides with it through app.May; the web keeps no copy of the policy.
//
//nolint:gocritic // hugeParam: the plan's contract passes the session by value.
func (a *Authenticator) Actor(s Session, now time.Time) app.Actor {
	admin := s.Has(Admin)
	return app.Actor{
		Subject:   SubjectPrefix + s.Subject,
		Responder: s.Has(Responder) || admin,
		Admin:     admin && a.fresh(s.AuthTime, now),
	}
}

// fresh says whether a person who authenticated at authTime is fresh at
// now: within web.freshWindow plus the leeway, and not later than the
// leeway past now. A zero authTime, the provider having given none, is not.
func (a *Authenticator) fresh(authTime, now time.Time) bool {
	if authTime.IsZero() {
		return false
	}
	age := now.Sub(authTime)
	return age >= -leeway && age <= a.web.FreshWindow+leeway
}
