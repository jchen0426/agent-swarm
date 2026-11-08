package auth_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// The web's actor is the session's subject as sso:<value>; every admin is a
// responder, fresh or not; and Admin holds only while the sign-in is within
// web.freshWindow plus a fixed 60 s leeway (R76; I1).
func TestTheActorIsAdminOnlyInsideFreshnessAndEveryAdminIsAResponder(t *testing.T) {
	w := newWorld(t, nil) // freshWindow 5 min
	now := w.clock.Now()
	for _, tc := range []struct {
		name  string
		roles []auth.Role
		since time.Duration // how long ago the person authenticated
		zero  bool          // no auth_time
		want  app.Actor
	}{
		{name: "no role", want: app.Actor{Subject: "sso:alice"}},
		{name: "viewer", roles: []auth.Role{auth.Viewer}, want: app.Actor{Subject: "sso:alice"}},
		{name: "responder", roles: []auth.Role{auth.Responder}, want: app.Actor{Subject: "sso:alice", Responder: true}},
		{
			name: "fresh admin", roles: []auth.Role{auth.Admin}, since: time.Minute,
			want: app.Actor{Subject: "sso:alice", Responder: true, Admin: true},
		},
		{
			name: "admin at the window and its leeway", roles: []auth.Role{auth.Admin}, since: 6 * time.Minute,
			want: app.Actor{Subject: "sso:alice", Responder: true, Admin: true},
		},
		{
			name: "admin past the leeway", roles: []auth.Role{auth.Admin}, since: 6*time.Minute + time.Second,
			want: app.Actor{Subject: "sso:alice", Responder: true},
		},
		{
			name: "stale admin and responder", roles: []auth.Role{auth.Responder, auth.Admin}, since: time.Hour,
			want: app.Actor{Subject: "sso:alice", Responder: true},
		},
		{
			name: "admin with no auth_time", roles: []auth.Role{auth.Admin}, zero: true,
			want: app.Actor{Subject: "sso:alice", Responder: true},
		},
		{
			name: "admin authenticated in the future past the leeway", roles: []auth.Role{auth.Admin}, since: -2 * time.Minute,
			want: app.Actor{Subject: "sso:alice", Responder: true},
		},
		{
			name: "an unknown role gives nothing", roles: []auth.Role{"owner"},
			want: app.Actor{Subject: "sso:alice"},
		},
	} {
		s := w.session(tc.roles...)
		s.AuthTime = now.Add(-tc.since)
		if tc.zero {
			s.AuthTime = time.Time{}
		}
		assert.Equal(t, tc.want, w.a.Actor(s, now), tc.name)
	}

	// The window is the deployment's.
	short := newWorld(t, func(b *deploy.Web) { b.FreshWindow = time.Minute })
	s := short.session(auth.Admin)
	s.AuthTime = now.Add(-2*time.Minute - time.Second)
	assert.False(t, short.a.Actor(s, now).Admin)
	s.AuthTime = now.Add(-2 * time.Minute)
	assert.True(t, short.a.Actor(s, now).Admin)
}
