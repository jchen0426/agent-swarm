// Package webtest is the web interface's importable test support (Plan
// 9a-1, I4): a signed-in session for the server under test, a
// browser-like client that applies the Fetch standard's rule for Origin, a
// test CA, and a fake OIDC provider.
// Tests of internal/web and its packages, and the black-box harness, import
// it; production code never does.
package webtest

import (
	"net/http"
	"testing"
	"time"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
)

// sessionLength is how long a SignedIn session lasts.
const sessionLength = time.Hour

// SignedIn is a session cookie the server under test opens: subject signed
// in now, with roles, for an hour, sealed under keys, which are the
// server's (auth.NewKeys of its secret, issuer and client ID, or
// Authenticator.Keys). Its auth_time is now, so an admin is fresh.
func SignedIn(t testing.TB, keys auth.Keys, roles []auth.Role, subject string) *http.Cookie {
	t.Helper()
	now := time.Now().Truncate(time.Second)
	s := auth.Session{
		Issuer: keys.Issuer(), ClientID: keys.ClientID(), Subject: subject, Roles: roles,
		AuthTime: now, IssuedAt: now, Expires: now.Add(sessionLength),
	}
	sealed, err := s.Seal(keys)
	if err != nil {
		t.Fatalf("seal a session: %v", err)
	}
	return auth.SessionCookie(sealed, s.Expires)
}
