package auth_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/webtest"
)

// staleAdmin is a browser signed in as an admin ten minutes ago: no
// longer fresh under the five-minute window.
func staleAdmin(t *testing.T, s *site) *webtest.Browser {
	t.Helper()
	s.idp.SetClaims(groups("admins"))
	b := s.browser(t)
	me := who(t, b.Get(s.url+"/items"))
	require.True(t, me.Admin, "an admin is fresh at sign-in")
	s.clock.advance(10 * time.Minute)
	me = who(t, b.Get(s.url+"/items"))
	require.False(t, me.Admin, "ten minutes on, not fresh")
	require.True(t, me.Responder)
	return b
}

// freshPath is the fresh authentication that returns to the close form.
const freshPath = "/auth/fresh?return=%2Fitems%2Fx%2Fclose"

// A fresh authentication asks the IdP to authenticate again, max_age=0
// and prompt=login, and a token that says it did makes the admin fresh.
func TestFreshAuthenticationMakesAnAdminFresh(t *testing.T) {
	s := newSite(t, nil)
	b := staleAdmin(t, s)
	p := b.Get(s.url + freshPath)
	me := who(t, p)
	assert.True(t, me.Admin)
	assert.Equal(t, s.clock.Now(), me.AuthTime)
	q := s.idp.Authorizations()
	require.Len(t, q, 2)
	assert.Equal(t, "0", q[1].Get("max_age"))
	assert.Equal(t, "login", q[1].Get("prompt"))
	s.requireNoSecretLogged(t, s.sessionCookie(b))
}

// A fresh authentication whose token carries no auth_time is refused, and
// the session is kept as it was.
func TestFreshAuthenticationRefusesAMissingAuthTime(t *testing.T) {
	s := newSite(t, nil)
	b := staleAdmin(t, s)
	before := s.sessionCookie(b)
	s.idp.OmitAuthTime(true)

	p := b.Get(s.url + freshPath)
	assert.Equal(t, http.StatusForbidden, p.Resp.StatusCode, string(p.Body))
	assert.Contains(t, string(p.Body), "did not say when you signed in")
	assert.Equal(t, before.Value, s.sessionCookie(b).Value, "the session is unchanged")
	assert.False(t, who(t, b.Get(s.url+"/items")).Admin)
}

// A fresh authentication whose auth_time is older than the window plus 60
// s is refused, as from an IdP that ignored prompt=login; one within it is
// taken.
func TestFreshAuthenticationRefusesAnOldAuthTime(t *testing.T) {
	t.Run("older than the window plus 60 s", func(t *testing.T) {
		s := newSite(t, nil)
		s.idp.IgnorePromptLogin(true)
		b := staleAdmin(t, s)
		p := b.Get(s.url + freshPath)
		assert.Equal(t, http.StatusForbidden, p.Resp.StatusCode, string(p.Body))
		assert.Contains(t, string(p.Body), "did not ask you to sign in again")
		assert.False(t, who(t, b.Get(s.url+"/items")).Admin)
	})
	t.Run("within the window plus 60 s", func(t *testing.T) {
		s := newSite(t, nil)
		s.idp.IgnorePromptLogin(true)
		s.idp.SetClaims(groups("admins"))
		b := s.browser(t)
		who(t, b.Get(s.url+"/items"))
		s.clock.advance(5*time.Minute + 59*time.Second)
		me := who(t, b.Get(s.url+freshPath))
		assert.True(t, me.Admin)
	})
}

// A fresh authentication as another subject is refused: freshness is the
// signed-in person's own, and the session is kept as it was.
func TestFreshAuthenticationRefusesAnotherSubject(t *testing.T) {
	s := newSite(t, nil)
	b := staleAdmin(t, s)
	before := s.sessionCookie(b)
	s.idp.SetClaims(map[string]any{"sub": "mallory", "groups": []string{"admins"}})

	p := b.Get(s.url + freshPath)
	assert.Equal(t, http.StatusForbidden, p.Resp.StatusCode, string(p.Body))
	assert.Contains(t, string(p.Body), "another person")
	assert.Equal(t, before.Value, s.sessionCookie(b).Value)
	me := who(t, b.Get(s.url+"/items"))
	assert.Equal(t, "alice", me.Subject)
	assert.False(t, me.Admin)
}

// The fresh token's roles replace the session's: an admin demoted at the
// IdP holds no admin role after a fresh authentication, in the session
// and in the actor (n11).
func TestFreshAuthenticationReplacesTheSessionsRoles(t *testing.T) {
	s := newSite(t, nil)
	b := staleAdmin(t, s)
	s.idp.SetClaims(groups("responders"))

	me := who(t, b.Get(s.url+freshPath))
	assert.Equal(t, []auth.Role{auth.Responder}, me.Roles, "the session holds the fresh token's roles alone")
	assert.False(t, me.Admin, "the actor is no admin")
	assert.True(t, me.Responder)
	assert.Equal(t, s.clock.Now(), me.AuthTime)
}

// A fresh authentication returns to the act's form as a GET, never
// replaying a POST: the form page is asked for once, by GET, and the fresh
// route itself answers nothing but a GET.
func TestFreshAuthenticationReturnsToTheFormAsAGetAndNeverReplaysThePost(t *testing.T) {
	s := newSite(t, nil)
	b := staleAdmin(t, s)

	p := b.Get(s.url + freshPath)
	me := who(t, p)
	assert.Equal(t, http.MethodGet, me.Method)
	assert.Equal(t, "/items/x/close", p.Resp.Request.URL.Path)
	assert.Equal(t, []string{http.MethodGet}, s.forms.messages(t), "the form page was asked for once, by GET")

	a := s.agent(t)
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		req, err := http.NewRequestWithContext(t.Context(), method, s.url+freshPath, strings.NewReader(url.Values{"x": {"1"}}.Encode()))
		require.NoError(t, err)
		resp, err := a.c.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, method)
	}

	t.Run("without a session, it signs in first", func(t *testing.T) {
		resp, _ := s.agent(t).get(s.url + freshPath)
		require.Equal(t, http.StatusSeeOther, resp.StatusCode)
		assert.Equal(t, auth.SignInPath+"?return=%2Fitems%2Fx%2Fclose", resp.Header.Get("Location"))
	})
	t.Run("an unsafe return path returns to the start page", func(t *testing.T) {
		s := newSite(t, nil)
		b := staleAdmin(t, s)
		p := b.Get(s.url + "/auth/fresh?return=" + url.QueryEscape("//evil.example.com/x"))
		who(t, p)
		assert.Equal(t, "/", p.Resp.Request.URL.Path)
	})
}
