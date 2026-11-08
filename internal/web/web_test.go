package web_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/web"
)

// publicURL is the deployment's web.publicURL in these tests.
const publicURL = "https://agents.example.com"

// host is publicURL's host, as the proxy forwards it.
const host = "agents.example.com"

// The security headers every response carries (design D3; R87).
const csp = "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// leader is a leader's status, as the health routes read it.
type leader struct{ held, draining bool }

func (l leader) Status() (epoch int64, held, draining bool) { return 7, l.held, l.draining }

// schema is the schema's state, as /readyz reads it.
type schema struct {
	state string
	err   error
}

func (s schema) State(context.Context) (compatible bool, state string, err error) {
	return s.state == "compatible", s.state, s.err
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

// records are the log's JSON lines.
func (l *lines) records(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(l.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var r map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &r), line)
		out = append(out, r)
	}
	return out
}

func (l *lines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// fixture makes a web server over a leading, compatible instance, with
// change applied to its options, and the log it writes to, at Debug.
func fixture(t *testing.T, change func(p *web.Ports, o *web.Options)) (*http.Server, *lines) {
	t.Helper()
	out := &lines{}
	p := web.Ports{Leader: leader{held: true}, Schema: schema{state: "compatible"}}
	o := web.Options{PublicURL: publicURL, Log: slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	if change != nil {
		change(&p, &o)
	}
	return web.NewServer(&p, o), out
}

// get serves a GET of target with the given Host through the server's
// handler.
func get(srv *http.Server, target, hostHeader string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	r.Host = hostHeader
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)
	return w
}

// page is a stub page that answers 200 "page".
var page = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "page") })

// Every route, the health routes, a refused Host and a route that does not
// exist among them, carries the security headers; a page marked
// authenticated also carries no-store, and a health route does not (m13).
func TestEveryRouteSetsTheSecurityHeaders(t *testing.T) {
	srv, _ := fixture(t, func(_ *web.Ports, o *web.Options) {
		mux := http.NewServeMux()
		mux.Handle("/signed-in", web.Authenticated(page))
		mux.Handle("/public", page)
		o.Pages = mux
	})
	for _, tc := range []struct {
		target, host string
		status       int
		noStore      bool
	}{
		{target: "/healthz", host: host, status: http.StatusOK},
		{target: "/readyz", host: host, status: http.StatusOK},
		{target: "/leaderz", host: host, status: http.StatusOK},
		{target: "/public", host: host, status: http.StatusOK},
		{target: "/signed-in", host: host, status: http.StatusOK, noStore: true},
		{target: "/no-such-page", host: host, status: http.StatusNotFound},
		{target: "/public", host: "evil.example.com", status: http.StatusBadRequest},
	} {
		w := get(srv, tc.target, tc.host)

		assert.Equal(t, tc.status, w.Code, tc.target)
		h := w.Header()
		assert.Equal(t, csp, h.Get("Content-Security-Policy"), tc.target)
		assert.Equal(t, "DENY", h.Get("X-Frame-Options"), tc.target)
		assert.Equal(t, "same-origin", h.Get("Referrer-Policy"), tc.target)
		assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"), tc.target)
		if tc.noStore {
			assert.Equal(t, "no-store", h.Get("Cache-Control"), tc.target)
		} else {
			assert.Empty(t, h.Get("Cache-Control"), tc.target)
		}
	}
}

// selfSigned is a TLS certificate for 127.0.0.1.
func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// serveOn serves srv on a loopback listener until the test ends, over TLS
// when cert is given, and returns its address.
func serveOn(t *testing.T, srv *http.Server, cert *tls.Certificate) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	if cert != nil {
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12}
	}
	go func() {
		if cert != nil {
			_ = srv.ServeTLS(ln, "", "")
			return
		}
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// The web server speaks HTTP/1 alone: a client that offers HTTP/2 over TLS
// is answered in HTTP/1.1, and one that speaks HTTP/2 in the clear with prior
// knowledge is not served (Go's HTTP/2 server has no per-request header
// timeout).
func TestTheWebServerSpeaksHTTP1Only(t *testing.T) {
	cert := selfSigned(t)
	srv, _ := fixture(t, nil)
	require.NotNil(t, srv.Protocols)
	assert.True(t, srv.Protocols.HTTP1())
	assert.False(t, srv.Protocols.HTTP2())
	assert.False(t, srv.Protocols.UnencryptedHTTP2())

	addr := serveOn(t, srv, &cert)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: true}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr}).Get("https://" + addr + "/healthz")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, 1, resp.ProtoMajor, "HTTP/2 was offered over TLS, and refused")

	plain, _ := fixture(t, nil)
	plainAddr := serveOn(t, plain, nil)
	var h2c http.Protocols
	h2c.SetUnencryptedHTTP2(true)
	tr2 := &http.Transport{Protocols: &h2c}
	defer tr2.CloseIdleConnections()
	resp, err = (&http.Client{Transport: tr2, Timeout: 5 * time.Second}).Get("http://" + plainAddr + "/healthz")
	if err == nil {
		require.NoError(t, resp.Body.Close())
		assert.NotEqual(t, 2, resp.ProtoMajor, "HTTP/2 in the clear is not served")
	}
}

// A client that sends its header slowly is cut at the header timeout, which a
// test sets short.
func TestASlowHeaderIsCut(t *testing.T) {
	srv, _ := fixture(t, func(_ *web.Ports, o *web.Options) { o.HeaderTimeout = 100 * time.Millisecond })
	addr := serveOn(t, srv, nil)
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()
	_, err = io.WriteString(conn, "GET /healthz HTTP/1.1\r\nHost: "+host+"\r\n")
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))

	start := time.Now()
	_, err = io.Copy(io.Discard, conn)

	require.NoError(t, err, "the server closed the connection before the read's deadline")
	assert.Less(t, time.Since(start), 2*time.Second)
}

// The server bounds a request's header at 64 KiB, its body at 64 KiB, and an
// idle connection at 120 s; the header timeout is 10 s unless set.
func TestTheWebServerBoundsItsHeadersBodiesAndIdleConnections(t *testing.T) {
	var readErr error
	srv, _ := fixture(t, func(_ *web.Ports, o *web.Options) {
		o.Pages = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, readErr = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusNoContent)
		})
	})

	assert.Equal(t, 10*time.Second, srv.ReadHeaderTimeout)
	assert.Equal(t, 64<<10, srv.MaxHeaderBytes)
	assert.Equal(t, 120*time.Second, srv.IdleTimeout)
	r := httptest.NewRequest(http.MethodPost, "/form", bytes.NewReader(make([]byte, 64<<10+1)))
	r.Host = host
	srv.Handler.ServeHTTP(httptest.NewRecorder(), r)
	var tooLarge *http.MaxBytesError
	require.ErrorAs(t, readErr, &tooLarge, "a body past 64 KiB is cut")
	assert.Equal(t, int64(64<<10), tooLarge.Limit)
}

// The Host check compares normalized: case folded, a trailing dot dropped, a
// default port stripped and IPv6 in its canonical form, in brackets (m15). A
// Host that differs otherwise is refused with 400.
func TestAForeignHostIsRefused(t *testing.T) {
	for _, tc := range []struct {
		publicURL         string
		accepted, refused []string
	}{
		{
			publicURL: publicURL,
			accepted:  []string{host, "AGENTS.Example.COM", "agents.example.com.", "agents.example.com:443", "Agents.Example.Com.:443"},
			refused:   []string{"evil.example.com", "agents.example.com:8443", "agents.example.com.evil", "agents.example.com:80", "", "agents.example.com..", "user@agents.example.com"},
		},
		{
			publicURL: "https://agents.example.com:8443/",
			accepted:  []string{"agents.example.com:8443", "AGENTS.example.com.:8443"},
			refused:   []string{host, "agents.example.com:443"},
		},
		{
			publicURL: "https://[2001:db8::1]",
			accepted:  []string{"[2001:db8::1]", "[2001:DB8::1]", "[2001:db8:0:0::1]:443"},
			refused:   []string{"[2001:db8::2]", "2001:db8::1", "[2001:db8::1]:8443"},
		},
		{
			// Case is folded for ASCII alone: the Kelvin sign, U+212A, which
			// Unicode folds to k, and any other byte past ASCII, is refused,
			// never folded onto the public URL's host (Task 4's m1).
			publicURL: "https://key.example.com",
			accepted:  []string{"key.example.com", "KEY.example.com"},
			refused: []string{
				"Key.example.com", "KEY.EXAMPLE.COM", "keу.example.com", "kéy.example.com",
				"key.example.com。", "key.exаmple.com", "ſey.example.com",
			},
		},
	} {
		srv, _ := fixture(t, func(_ *web.Ports, o *web.Options) { o.PublicURL, o.Pages = tc.publicURL, page })
		for _, h := range tc.accepted {
			assert.Equal(t, http.StatusOK, get(srv, "/", h).Code, "%s: %q is the public URL's host", tc.publicURL, h)
		}
		for _, h := range tc.refused {
			assert.Equal(t, http.StatusBadRequest, get(srv, "/", h).Code, "%s: %q is foreign", tc.publicURL, h)
		}
	}
}

// A refused Host's answer names the setting it is held to, and serve logs the
// first such refusal at Warn, with the host it received, and no other (I11):
// a proxy that rewrites Host shows once, not on each request.
func TestAForeignHostsRefusalNamesTheSettingAndIsLoggedOnce(t *testing.T) {
	srv, out := fixture(t, func(_ *web.Ports, o *web.Options) { o.Pages = page })

	first := get(srv, "/", "127.0.0.1:7404")
	second := get(srv, "/items", "evil.example.com")

	for _, w := range []*httptest.ResponseRecorder{first, second} {
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, "Host does not match web.publicURL", strings.TrimSpace(w.Body.String()))
		assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	}
	var warned []map[string]any
	for _, r := range out.records(t) {
		if r["level"] == "WARN" {
			warned = append(warned, r)
		}
	}
	require.Len(t, warned, 1, "the first refusal alone is logged at Warn: %s", out)
	assert.Equal(t, "127.0.0.1:7404", warned[0]["host"])
	assert.Contains(t, warned[0]["msg"], "web.publicURL")
}

// The health routes are exempt from the Host check (N5): a load balancer's
// probe carries the target's address as its Host.
func TestHealthRoutesSkipTheHostCheck(t *testing.T) {
	srv, out := fixture(t, func(_ *web.Ports, o *web.Options) { o.Pages = page })
	for _, target := range []string{"/healthz", "/readyz", "/leaderz"} {
		w := get(srv, target, "10.0.0.5:7404")

		assert.Equal(t, http.StatusOK, w.Code, target)
		assert.Empty(t, w.Header().Values("Set-Cookie"), "%s sets no cookie", target)
	}
	assert.Equal(t, http.StatusBadRequest, get(srv, "/", "10.0.0.5:7404").Code, "a page is held to the Host check")
	requests := 0
	for _, r := range out.records(t) {
		if r["msg"] == "web request" {
			requests++
		}
	}
	assert.Equal(t, 4, requests, "each request is logged once, the probes among them")
}

// keyUnavailable is a stand-in for the session key's middleware when the key
// cannot be read (R86): every page it wraps answers 503.
func keyUnavailable(next http.Handler) http.Handler {
	_ = next
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sign-in is unavailable", http.StatusServiceUnavailable)
	})
}

// The health routes never answer the key's 503 (m16): they are served beside
// the pages, never through the middleware that guards them, nor through a
// handler marked authenticated.
func TestHealthRoutesNeverAnswerTheKeys503(t *testing.T) {
	var reached []string
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	srv, _ := fixture(t, func(_ *web.Ports, o *web.Options) { o.Pages = keyUnavailable(web.Authenticated(stub)) })

	assert.Equal(t, http.StatusServiceUnavailable, get(srv, "/", host).Code, "a page answers the key's 503")
	for _, target := range []string{"/healthz", "/readyz", "/leaderz"} {
		w := get(srv, target, host)

		assert.Equal(t, http.StatusOK, w.Code, target)
		assert.NotContains(t, w.Body.String(), "sign-in is unavailable", target)
		assert.Empty(t, w.Header().Get("Cache-Control"), "%s never passes through a handler marked authenticated", target)
	}
	assert.Empty(t, reached, "no health route reached the pages")
}

// /leaderz answers 200 while this instance holds the lease, the drain
// included, so that a load balancer keeps routing to the draining leader
// until the next one takes over; and 503 on a standby.
func TestLeaderzAnswers200OnADrainingLeaderAnd503OnAStandby(t *testing.T) {
	for _, tc := range []struct {
		name   string
		leader leader
		status int
	}{
		{name: "the leader", leader: leader{held: true}, status: http.StatusOK},
		{name: "the draining leader", leader: leader{held: true, draining: true}, status: http.StatusOK},
		{name: "a standby", leader: leader{}, status: http.StatusServiceUnavailable},
		{name: "a draining standby", leader: leader{draining: true}, status: http.StatusServiceUnavailable},
	} {
		srv, _ := fixture(t, func(p *web.Ports, _ *web.Options) { p.Leader = tc.leader })

		assert.Equal(t, tc.status, get(srv, "/leaderz", host).Code, tc.name)
	}
}

// /readyz answers as the admin API's does: 503 while serve drains or the
// schema is not compatible, or cannot be read; 200 otherwise, a standby's
// included. /healthz answers 200 while the process is up.
func TestReadyzAnswersAsTheAdminAPIs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		leader leader
		schema schema
		status int
		body   string
	}{
		{name: "a leader", leader: leader{held: true}, schema: schema{state: "compatible"}, status: http.StatusOK, body: "ready"},
		{name: "a standby", schema: schema{state: "compatible"}, status: http.StatusOK, body: "ready"},
		{name: "draining", leader: leader{held: true, draining: true}, schema: schema{state: "compatible"}, status: http.StatusServiceUnavailable, body: "draining"},
		{name: "a schema behind", schema: schema{state: "behind"}, status: http.StatusServiceUnavailable, body: "the schema is behind"},
		{name: "a schema unread", schema: schema{err: errors.New("connection refused")}, status: http.StatusServiceUnavailable, body: "the schema could not be read"},
	} {
		srv, _ := fixture(t, func(p *web.Ports, _ *web.Options) { p.Leader, p.Schema = tc.leader, tc.schema })

		w := get(srv, "/readyz", host)

		assert.Equal(t, tc.status, w.Code, tc.name)
		assert.Equal(t, tc.body, w.Body.String(), tc.name)
		assert.Equal(t, http.StatusOK, get(srv, "/healthz", host).Code, tc.name)
	}
}

// A request's log line holds its method, its path without the query, and its
// status, and never the query, a cookie or the body (M10): the callback's
// query holds the code and state.
func TestRequestLogsHoldNoQueryCookieOrBody(t *testing.T) {
	const secret = "s3cr3t-c0de"
	srv, out := fixture(t, func(_ *web.Ports, o *web.Options) {
		o.Pages = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusSeeOther)
		})
	})
	r := httptest.NewRequest(http.MethodPost, "/auth/callback?code="+secret+"&state="+secret, strings.NewReader("token="+secret))
	r.Host = host
	r.Header.Set("Cookie", "__Host-session="+secret)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	srv.Handler.ServeHTTP(httptest.NewRecorder(), r)

	assert.NotContains(t, out.String(), secret)
	var logged map[string]any
	for _, rec := range out.records(t) {
		if rec["path"] == "/auth/callback" {
			logged = rec
		}
	}
	require.NotNil(t, logged, "the request is logged: %s", out)
	assert.Equal(t, "POST", logged["method"])
	assert.InDelta(t, http.StatusSeeOther, logged["status"], 0)
}

// readStatus reads a raw HTTP/1 response's status line.
func readStatus(t *testing.T, conn net.Conn) string {
	t.Helper()
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	return strings.TrimSpace(line)
}

// A header past 64 KiB is refused, by the server, with 431.
func TestAHeaderPast64KiBIsRefused(t *testing.T) {
	srv, _ := fixture(t, nil)
	addr := serveOn(t, srv, nil)
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()
	_, err = io.WriteString(conn, "GET /healthz HTTP/1.1\r\nHost: "+host+"\r\nX-Big: "+strings.Repeat("a", 80<<10)+"\r\n\r\n")
	require.NoError(t, err)

	assert.Contains(t, readStatus(t, conn), "431")
}
