package auth_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
)

// A session sealed under a deployment's keys opens under them, whole, and
// holds nothing readable; a cookie sealed under another key, or for another
// issuer or client sharing the key, or tampered with, is refused (M3).
func TestSealAndOpenRoundTripAndRefuseAnotherDeploymentsCookie(t *testing.T) {
	secret := newSecret(t)
	k, err := auth.NewKeys(secret, issuer, clientID)
	require.NoError(t, err)
	now := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	s := auth.Session{
		Issuer: issuer, ClientID: clientID, Subject: "alice@example.com",
		Roles:    []auth.Role{auth.Responder, auth.Admin},
		AuthTime: now.Add(-time.Minute), IssuedAt: now, Expires: now.Add(8 * time.Hour),
	}

	sealed, err := s.Seal(k)
	require.NoError(t, err)
	again, err := s.Seal(k)
	require.NoError(t, err)
	assert.NotEqual(t, sealed, again, "each seal has its own nonce")
	assert.NotContains(t, sealed, "alice")
	if raw, err := base64.RawURLEncoding.DecodeString(sealed); assert.NoError(t, err, "a cookie value is base64url") {
		assert.NotContains(t, string(raw), "alice", "the subject is sealed, not encoded")
	}

	got, err := auth.Open(k, sealed)
	require.NoError(t, err)
	assert.Equal(t, s, got)

	other, err := auth.NewKeys(newSecret(t), issuer, clientID)
	require.NoError(t, err)
	otherIssuer, err := auth.NewKeys(secret, "https://other-idp.example.com", clientID)
	require.NoError(t, err)
	otherClient, err := auth.NewKeys(secret, issuer, "other-client")
	require.NoError(t, err)
	tampered := []byte(sealed)
	tampered[len(tampered)/2] ^= 1
	for name, open := range map[string]func() (auth.Session, error){
		"another key":    func() (auth.Session, error) { return auth.Open(other, sealed) },
		"another issuer": func() (auth.Session, error) { return auth.Open(otherIssuer, sealed) },
		"another client": func() (auth.Session, error) { return auth.Open(otherClient, sealed) },
		"tampered":       func() (auth.Session, error) { return auth.Open(k, string(tampered)) },
		"truncated":      func() (auth.Session, error) { return auth.Open(k, sealed[:10]) },
		"empty":          func() (auth.Session, error) { return auth.Open(k, "") },
	} {
		_, err := open()
		require.Error(t, err, name)
		assert.NotContains(t, err.Error(), sealed, name)
	}

	// A session for another deployment is never sealed under these keys.
	foreign := s
	foreign.Issuer = "https://other-idp.example.com"
	_, err = foreign.Seal(k)
	require.Error(t, err)
}

// The cookie is __Host-session: Secure, HttpOnly, SameSite=Lax, Path=/, no
// Domain, and lives as long as its session (design D4; M3).
func TestTheSessionCookieIsAHostCookie(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	c := auth.SessionCookie("sealed", expires)

	assert.Equal(t, "__Host-session", c.Name)
	assert.Equal(t, "sealed", c.Value)
	assert.True(t, c.Secure)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
	assert.Equal(t, "/", c.Path)
	assert.Empty(t, c.Domain)
	assert.Positive(t, c.MaxAge)
	assert.True(t, c.Expires.Equal(expires))
}

// The secret at web.sessionKeyRef holds base64 (std) of at least 32 bytes,
// with surrounding whitespace trimmed, as the harness writes it, value+"\n"
// (m22); raw bytes, base64url, or fewer bytes are refused, with no part of
// the value in the error.
func TestTheSessionKeyIsBase64WithWhitespaceTrimmed(t *testing.T) {
	secret := newSecret(t)
	plain, err := auth.NewKeys(secret, issuer, clientID)
	require.NoError(t, err)
	padded, err := auth.NewKeys(" \t"+secret+"\n", issuer, clientID)
	require.NoError(t, err)
	now := time.Now().Truncate(time.Second)
	s := auth.Session{Issuer: issuer, ClientID: clientID, Subject: "alice", IssuedAt: now, Expires: now.Add(time.Hour)}
	sealed, err := s.Seal(padded)
	require.NoError(t, err)
	_, err = auth.Open(plain, sealed)
	require.NoError(t, err, "the trimmed value is the same key")

	short := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 31)))
	urlSafe := base64.URLEncoding.EncodeToString([]byte(strings.Repeat("\xfb\xff", 20)))
	for name, value := range map[string]string{
		"raw bytes": strings.Repeat("rawsecretbytes!!", 3),
		"31 bytes":  short,
		"base64url": urlSafe,
		"empty":     " \n",
	} {
		_, err := auth.NewKeys(value, issuer, clientID)
		require.Error(t, err, name)
		if v := strings.TrimSpace(value); v != "" {
			assert.NotContains(t, err.Error(), v, name)
		}
	}

	// Through the authenticator, the store's value+"\n" serves a page.
	w := newWorld(t, nil)
	w.store.set(w.secret+"\n", nil)
	req := httptest.NewRequest(http.MethodGet, "/items", http.NoBody)
	req.AddCookie(cookie(t, w.keys(t), w.session(auth.Viewer)))
	assert.Equal(t, http.StatusOK, serve(w.a.Available(w.a.SignedIn(ok)), req).Code)
}

// ok is a page that answers 200 "ok".
var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

// get serves a GET of target with c through the guarded page.
func (w *world) get(target string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	if c != nil {
		req.AddCookie(c)
	}
	return serve(w.a.Available(w.a.SignedIn(ok)), req)
}

// The key is read at use and cached at most a minute: rotating the secret
// signs everyone out within a minute, the emergency revoke (design D4, Q1a;
// R86).
func TestRotatingTheSessionKeySignsEveryoneOutWithinAMinute(t *testing.T) {
	w := newWorld(t, nil)
	before := cookie(t, w.keys(t), w.session(auth.Responder))
	require.Equal(t, http.StatusOK, w.get("/items", before).Code)
	reads := w.store.count()

	rotated := newSecret(t)
	w.store.set(rotated, nil)
	w.clock.advance(30 * time.Second)
	assert.Equal(t, http.StatusOK, w.get("/items", before).Code, "the key is cached")
	assert.Equal(t, reads, w.store.count(), "a cached key is not read again")

	w.clock.advance(31 * time.Second)
	out := w.get("/items", before)
	assert.Equal(t, http.StatusSeeOther, out.Code, "the old key's cookie is signed out after a minute")
	assert.True(t, strings.HasPrefix(out.Header().Get("Location"), "/auth/signin"), out.Header().Get("Location"))

	k, err := auth.NewKeys(rotated, issuer, clientID)
	require.NoError(t, err)
	after := cookie(t, k, w.session(auth.Responder))
	assert.Equal(t, http.StatusOK, w.get("/items", after).Code, "the new key's cookie opens")
}

// A key that cannot be read, or reads malformed, fails closed: every page
// answers 503 "sign-in is unavailable", even with a cached key past its
// minute, and the failure is logged at most once a minute, never with the
// value (R86; m20).
func TestAKeyThatCannotBeReadFailsClosedAndIsLoggedAtMostOnceAMinute(t *testing.T) {
	w := newWorld(t, nil)
	signedIn := cookie(t, w.keys(t), w.session(auth.Admin))
	require.Equal(t, http.StatusOK, w.get("/items", signedIn).Code)

	w.store.set("", errors.New("the secret's file is missing"))
	w.clock.advance(61 * time.Second)
	unavailable := func(target string) {
		t.Helper()
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req := httptest.NewRequest(method, target, http.NoBody)
			req.AddCookie(signedIn)
			out := serve(w.a.Available(w.a.SignedIn(ok)), req)
			assert.Equal(t, http.StatusServiceUnavailable, out.Code, "%s %s", method, target)
			assert.Contains(t, out.Body.String(), "sign-in is unavailable", "%s %s", method, target)
		}
	}
	failures := func() int {
		n := 0
		for _, m := range w.log.messages(t) {
			if strings.Contains(m, "session key could not be read") {
				n++
			}
		}
		return n
	}

	unavailable("/items")
	w.clock.advance(30 * time.Second)
	unavailable("/kill-switch")
	w.clock.advance(29 * time.Second)
	unavailable("/")
	assert.Equal(t, 1, failures(), "one log line in the first minute")

	w.clock.advance(time.Second)
	unavailable("/items")
	assert.Equal(t, 2, failures(), "the next minute logs again")

	// A key that reads, but malformed, fails closed too, and its value is
	// never logged.
	w.store.set("bm90IGEga2V5", nil)
	w.clock.advance(time.Minute)
	unavailable("/items")
	assert.Equal(t, 3, failures())
	assert.NotContains(t, w.log.String(), "bm90IGEga2V5")

	// A key that reads again serves again.
	w.store.set(w.secret, nil)
	assert.Equal(t, http.StatusOK, w.get("/items", signedIn).Code)
}

// A GET or HEAD with an expired session, or none, or one that does not
// open, is sent to sign-in with its path as the return path; a POST is never
// redirected, and answers a page with a sign-in link (I8).
func TestAnExpiredSessionOnAGetSendsToSignIn(t *testing.T) {
	w := newWorld(t, nil)
	k := w.keys(t)
	expired := w.session(auth.Responder)
	expired.Expires = w.clock.Now().Add(-time.Second)
	other, err := auth.NewKeys(newSecret(t), issuer, clientID)
	require.NoError(t, err)

	for name, c := range map[string]*http.Cookie{
		"expired":     cookie(t, k, expired),
		"missing":     nil,
		"another key": cookie(t, other, w.session(auth.Responder)),
		"garbage":     {Name: "__Host-session", Value: "garbage"},
	} {
		out := w.get("/items?incident=inc-1", c)
		assert.Equal(t, http.StatusSeeOther, out.Code, name)
		assert.Equal(t, "/auth/signin?return=%2Fitems%3Fincident%3Dinc-1", out.Header().Get("Location"), name)

		req := httptest.NewRequest(http.MethodHead, "/items", http.NoBody)
		if c != nil {
			req.AddCookie(c)
		}
		head := serve(w.a.Available(w.a.SignedIn(ok)), req)
		assert.Equal(t, http.StatusSeeOther, head.Code, name)

		req = httptest.NewRequest(http.MethodPost, "/items", http.NoBody)
		if c != nil {
			req.AddCookie(c)
		}
		post := serve(w.a.Available(w.a.SignedIn(ok)), req)
		assert.Equal(t, http.StatusUnauthorized, post.Code, name)
		assert.Empty(t, post.Header().Get("Location"), name)
		assert.Contains(t, post.Body.String(), `href="/auth/signin"`, name)
	}

	// An unsafe path returns to the top page.
	out := w.get("/auth/callback?code=x", nil)
	assert.Equal(t, "/auth/signin?return=%2F", out.Header().Get("Location"))

	// A live session reaches the page, which reads it.
	var seen auth.Session
	page := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		s, found := auth.From(r.Context())
		assert.True(t, found)
		seen = s.Session
		assert.NotEmpty(t, s.Token)
	})
	req := httptest.NewRequest(http.MethodGet, "/items", http.NoBody)
	live := w.session(auth.Responder)
	req.AddCookie(cookie(t, k, live))
	assert.Equal(t, http.StatusOK, serve(w.a.Available(w.a.SignedIn(page)), req).Code)
	assert.Equal(t, live, seen)
}

// A board's poll is guarded by Polled (R161; review I1.4): a live session
// reaches its handler, the session in its context, as SignedIn passes it;
// a request with an expired session, one issued past the leeway ahead of
// now, a cookie sealed under another key, one that does not open, or none,
// answers 401 with no body, no Location, no cookie set and no
// WWW-Authenticate challenge, which a browser would prompt on, whatever its
// method, never reaching the handler, so that a script's poll never starts
// a sign-in. Its X-Session says which: "none" when the request carries no
// session cookie at all, as after a sign-out, "ended" for every other (Task
// 8's review, R2-M1). While the key cannot be read, Available answers its
// 503 before Polled, with no redirect either.
func TestAPollWithoutALiveSessionAnswers401AndNeverARedirect(t *testing.T) {
	w := newWorld(t, nil)
	k := w.keys(t)
	expired := w.session(auth.Responder)
	expired.Expires = w.clock.Now().Add(-time.Second)
	ahead := w.session(auth.Responder)
	ahead.IssuedAt = w.clock.Now().Add(2 * time.Minute)
	other, err := auth.NewKeys(newSecret(t), issuer, clientID)
	require.NoError(t, err)
	reached := 0
	var seen auth.Session
	poll := w.a.Available(w.a.Polled(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		reached++
		s, found := auth.From(r.Context())
		assert.True(t, found)
		assert.NotEmpty(t, s.Token)
		seen = s.Session
		_, _ = rw.Write([]byte("regions"))
	})))
	for name, c := range map[string]*http.Cookie{
		"expired":      cookie(t, k, expired),
		"issued ahead": cookie(t, k, ahead),
		"another key":  cookie(t, other, w.session(auth.Responder)),
		"garbage":      {Name: "__Host-session", Value: "garbage"},
		"missing":      nil,
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
			req := httptest.NewRequest(method, "/items/a1/board?feed=all", http.NoBody)
			if c != nil {
				req.AddCookie(c)
			}
			out := serve(poll, req)

			assert.Equal(t, http.StatusUnauthorized, out.Code, "%s %s", method, name)
			assert.Empty(t, out.Body.String(), "%s %s: no body", method, name)
			assert.Empty(t, out.Header().Get("Location"), "%s %s", method, name)
			assert.Empty(t, out.Header().Values("Set-Cookie"), "%s %s", method, name)
			assert.Equal(t, "no-store", out.Header().Get("Cache-Control"), "%s %s", method, name)
			assert.Empty(t, out.Header().Values("WWW-Authenticate"), "%s %s: no challenge, which a browser would prompt on", method, name)
			session := "ended"
			if c == nil {
				session = "none"
			}
			assert.Equal(t, []string{session}, out.Header().Values("X-Session"), "%s %s", method, name)
		}
	}
	assert.Zero(t, reached, "no request without a live session reached the poll's handler")

	live := w.session(auth.Viewer)
	req := httptest.NewRequest(http.MethodGet, "/items/a1/board", http.NoBody)
	req.AddCookie(cookie(t, k, live))
	out := serve(poll, req)
	assert.Equal(t, http.StatusOK, out.Code)
	assert.Equal(t, "regions", out.Body.String())
	assert.Equal(t, 1, reached)
	assert.Equal(t, live, seen)

	w.store.set("", errors.New("the secret's file is missing"))
	w.clock.advance(61 * time.Second)
	req = httptest.NewRequest(http.MethodGet, "/items/a1/board", http.NoBody)
	req.AddCookie(cookie(t, k, w.session(auth.Viewer)))
	out = serve(poll, req)
	assert.Equal(t, http.StatusServiceUnavailable, out.Code, "the key cannot be read")
	assert.Empty(t, out.Header().Get("Location"))
	assert.Empty(t, out.Header().Values("Set-Cookie"))
	assert.Equal(t, 1, reached)
}

// The store is read outside the keyring's lock: while one slow read
// refreshes a key near the end of its minute, a page on the cached key is
// served at once; past the minute, concurrent pages share the one read in
// flight, and a page whose request ends stops waiting for it (m2).
func TestASlowKeyReadDoesNotBlockACachedRead(t *testing.T) {
	w := newWorld(t, nil)
	signedIn := cookie(t, w.keys(t), w.session(auth.Responder))
	require.Equal(t, http.StatusOK, w.get("/items", signedIn).Code)

	w.clock.advance(55 * time.Second)
	entered, release := w.store.slow()
	refreshed := make(chan int, 1)
	go func() { refreshed <- w.get("/items", signedIn).Code }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no refresh began near the end of the key's minute")
	}
	served := make(chan int, 1)
	go func() { served <- w.get("/kill-switch", signedIn).Code }()
	select {
	case code := <-served:
		assert.Equal(t, http.StatusOK, code, "the cached key serves while the refresh reads")
	case <-time.After(time.Second):
		t.Fatal("a cached read waited for the slow read")
	}
	release()
	assert.Equal(t, http.StatusOK, <-refreshed)

	// Past the minute, every page waits for the one read in flight.
	w.clock.advance(61 * time.Second)
	reads := w.store.count()
	entered, release = w.store.slow()
	codes := make(chan int, 3)
	for range 3 {
		go func() { codes <- w.get("/items", signedIn).Code }()
	}
	<-entered
	select {
	case <-entered:
		t.Fatal("a second read began while one was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := w.a.Keys(ctx)
	require.Error(t, err, "a request that ended stops waiting")
	release()
	for range 3 {
		assert.Equal(t, http.StatusOK, <-codes)
	}
	assert.Equal(t, reads+1, w.store.count(), "one read served them all")
}

// A page request that ends while it reads the key for everyone does not
// fail the pages waiting on that read: the read runs on its own bounded
// context, and the request that began it returns on its own.
func TestACancelledReaderDoesNotFailTheWaiters(t *testing.T) {
	w := newWorld(t, nil)
	signedIn := cookie(t, w.keys(t), w.session(auth.Responder))
	w.clock.advance(time.Hour) // no key cached
	entered, release := w.store.slow()

	ctx, cancel := context.WithCancel(t.Context())
	starter := make(chan error, 1)
	go func() { _, err := w.a.Keys(ctx); starter <- err }()
	<-entered
	codes := make(chan int, 2)
	for range 2 {
		go func() { codes <- w.get("/items", signedIn).Code }()
	}
	time.Sleep(50 * time.Millisecond) // let the waiters join the read
	cancel()
	select {
	case err := <-starter:
		require.Error(t, err, "the request that ended returns")
	case <-time.After(time.Second):
		t.Fatal("the cancelled starter still waits")
	}
	release()
	for range 2 {
		assert.Equal(t, http.StatusOK, <-codes, "a waiter is served by the read")
	}
	assert.NotContains(t, w.log.String(), "session key could not be read", "no failure was logged")
	assert.Equal(t, http.StatusOK, w.get("/items", signedIn).Code, "the key read is cached")
}

// A store that panics fails that read closed and leaves no read in flight:
// the next request reads again and is served.
func TestAPanickingStoreLeavesNoReadInFlight(t *testing.T) {
	w := newWorld(t, nil)
	signedIn := cookie(t, w.keys(t), w.session(auth.Responder))
	w.store.mu.Lock()
	w.store.panics = 1
	w.store.mu.Unlock()

	assert.Equal(t, http.StatusServiceUnavailable, w.get("/items", signedIn).Code)
	done := make(chan int, 1)
	go func() { done <- w.get("/items", signedIn).Code }()
	select {
	case code := <-done:
		assert.Equal(t, http.StatusOK, code)
	case <-time.After(2 * time.Second):
		t.Fatal("a read is still in flight after the panic")
	}
	assert.NotContains(t, w.log.String(), "the secret store broke", "a panic's value may hold a secret, so it is never logged")
}
