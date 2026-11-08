package auth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
)

// The deployment the tests sign in to.
const (
	publicURL = "https://agents.example.com"
	issuer    = "https://idp.example.com"
	clientID  = "agentswarm"
	keyRef    = "secret://web/session"
)

// newSecret is a fresh session key as the secret store holds it: base64
// (std) of 32 random bytes.
func newSecret(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(b)
}

// store is a secret store whose value, or failure, the test sets, and which
// counts its reads.
type store struct {
	mu    sync.Mutex
	value string
	err   error
	reads int
	// gate, when set, holds each read until it is closed; entered is told
	// of each read held.
	gate    chan struct{}
	entered chan struct{}
	// panics is how many reads panic before the store works.
	panics int
	// others are the store's other secrets, by reference, read without
	// the gate and not counted.
	others map[string]string
}

func (s *store) Secret(ctx context.Context, ref string) (string, error) {
	s.mu.Lock()
	if v, ok := s.others[ref]; ok && ref != keyRef {
		s.mu.Unlock()
		return v, nil
	}
	gate, entered := s.gate, s.entered
	if s.panics > 0 {
		s.panics--
		s.mu.Unlock()
		panic("the secret store broke")
	}
	s.mu.Unlock()
	if gate != nil {
		entered <- struct{}{}
		select {
		case <-gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if ref != keyRef {
		return "", errors.New("no such secret")
	}
	return s.value, s.err
}

func (s *store) set(value string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value, s.err = value, err
}

// slow holds the store's reads until the returned function is called.
func (s *store) slow() (entered <-chan struct{}, release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gate, in := make(chan struct{}), make(chan struct{}, 16)
	s.gate, s.entered = gate, in
	return in, func() {
		s.mu.Lock()
		s.gate, s.entered = nil, nil
		s.mu.Unlock()
		close(gate)
	}
}

func (s *store) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// clock is a clock the test moves.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// lines is a log's output, safe for the server's goroutines.
type lines struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// messages are the log's records' messages.
func (l *lines) messages(t *testing.T) []string {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for line := range strings.SplitSeq(strings.TrimSpace(l.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var r map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &r), line)
		out = append(out, r["msg"].(string))
	}
	return out
}

func (l *lines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// webBlock is the deployment's web block in these tests.
func webBlock() deploy.Web {
	return deploy.Web{
		PublicURL: publicURL,
		OIDC: deploy.OIDC{
			Issuer: issuer, ClientID: clientID, ClientSecretRef: "secret://web/client",
			SubjectClaim: "sub", GroupsClaim: "groups",
		},
		Groups:        deploy.Groups{Viewers: "viewers", Responders: "responders", Admins: "admins"},
		SessionKeyRef: keyRef,
		SessionLength: 8 * time.Hour,
		FreshWindow:   5 * time.Minute,
		TimeZone:      "UTC",
	}
}

// world is an authenticator over a store holding a fresh key, a clock and a
// log.
type world struct {
	a      *auth.Authenticator
	store  *store
	clock  *clock
	log    *lines
	secret string
}

func newWorld(t *testing.T, change func(w *deploy.Web)) *world {
	t.Helper()
	w := &world{store: &store{}, clock: newClock(), log: &lines{}, secret: newSecret(t)}
	w.store.set(w.secret, nil)
	block := webBlock()
	if change != nil {
		change(&block)
	}
	a, err := auth.NewAuthenticator(block, w.store, w.clock.Now, slog.New(slog.NewJSONHandler(w.log, nil)))
	require.NoError(t, err)
	w.a = a
	return w
}

// keys are the keys of the world's current secret, for its deployment.
func (w *world) keys(t *testing.T) auth.Keys {
	t.Helper()
	k, err := auth.NewKeys(w.secret, issuer, clientID)
	require.NoError(t, err)
	return k
}

// session is a session signed in now with the given roles, for the length
// of the deployment's sessions.
func (w *world) session(roles ...auth.Role) auth.Session {
	now := w.clock.Now()
	return auth.Session{
		Issuer: issuer, ClientID: clientID, Subject: "alice", Roles: roles,
		AuthTime: now, IssuedAt: now, Expires: now.Add(8 * time.Hour),
	}
}

// cookie is s sealed under k, as the session cookie.
func cookie(t *testing.T, k auth.Keys, s auth.Session) *http.Cookie { //nolint:gocritic // hugeParam: a test's sessions read best by value.
	t.Helper()
	sealed, err := s.Seal(k)
	require.NoError(t, err)
	return auth.SessionCookie(sealed, s.Expires)
}

// serve serves req through h.
func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
