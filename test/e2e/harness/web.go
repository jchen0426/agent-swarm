//go:build e2e

package harness

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jchen0426/agent-swarm/internal/web/webtest"
)

// The web block's values every deployment with DeploymentSpec.Web names
// (Plan 9a-1, m18): the client the fake IdP knows, and the groups that give
// each role, which a scenario puts in the IdP's groups claim.
const (
	WebClientID     = "agentswarm-harness"
	ViewersGroup    = "agentswarm-viewers"
	RespondersGroup = "agentswarm-responders"
	AdminsGroup     = "agentswarm-admins"
)

const (
	// callbackPath is the web interface's OIDC redirect path, under
	// web.publicURL, which the IdP's client registers.
	callbackPath = "/auth/callback"
	// sessionKeyBytes is how many random bytes the session key holds, in
	// base64: at least 32, as serve reads it.
	sessionKeyBytes = 32
	// probeTimeout bounds the stub's probe of an instance's /leaderz.
	probeTimeout = time.Second
)

// Web is the web side of a harness (Plan 9a-1, m18): a fake OIDC provider,
// webtest's, served with TLS under the test CA, and a TLS stub that stands
// for the deployment's TLS-terminating proxy at web.publicURL. The stub is
// bound before any serve starts, as publicURL names its port. It forwards
// each request to the web listener of the instance whose /leaderz answers
// 200, as a load balancer would, keeping the client's Host; when none does,
// to the instance it forwarded to last, and before any, to the first that
// started. Both instances of a harness share the session key and the
// client's secret, so a person signed in through one stays signed in when
// the stub turns to the other.
type Web struct {
	url        string
	idp        *webtest.IDP
	pool       *x509.CertPool
	secret     string
	sessionKey string
	forward    http.RoundTripper
	probe      *http.Client

	mu sync.Mutex
	// upstreams are the instances' web listeners, by instance, in the order
	// they started.
	upstreams map[string]string
	order     []string
	last      string
}

// Web returns the harness's web side, starting it on first use: the TLS
// stub, bound at once, and the fake IdP, whose client's redirect URL is
// under the stub's URL. It fails the test passed to New.
func (e *Env) Web() *Web {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.web == nil {
		e.web = newWeb(e.t, e.ca)
	}
	return e.web
}

func newWeb(t testing.TB, ca *testCA) *Web {
	t.Helper()
	cfg, err := ca.serverConfig()
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("harness: bind the web's TLS stub: %v", err)
	}
	key := make([]byte, sessionKeyBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("harness: a session key: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	w := &Web{
		url:        "https://" + ln.Addr().String(),
		pool:       pool,
		secret:     "client-secret-" + randomHex(16),
		sessionKey: base64.StdEncoding.EncodeToString(key),
		forward:    &http.Transport{DisableKeepAlives: true, ResponseHeaderTimeout: forwardHeaderTimeout},
		probe:      &http.Client{Timeout: probeTimeout, Transport: &http.Transport{DisableKeepAlives: true}},
		upstreams:  map[string]string{},
	}
	srv := &http.Server{
		Handler:           w,
		TLSConfig:         cfg,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(discard{}, "", 0),
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })
	w.idp = webtest.NewIDP(t, webtest.CAOf(ca.cert, ca.key), webtest.Client{
		ID: WebClientID, Secret: w.secret, RedirectURL: w.url + callbackPath,
	})
	return w
}

// The bounds of a hung instance (Task 10 M2): a scenario fails in a minute,
// not at go test's timeout. browserTimeout bounds each of the browser's
// requests, its redirects through the stub and the IdP included;
// forwardHeaderTimeout bounds the stub's wait for the instance's answer.
const (
	browserTimeout       = 60 * time.Second
	forwardHeaderTimeout = 30 * time.Second
)

// discard drops the stub's server's own error lines, such as a client's
// closed connection.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// URL is web.publicURL: the stub's, https://127.0.0.1:<port>.
func (w *Web) URL() string { return w.url }

// IDP is the fake OIDC provider, web.oidc.issuer: a scenario sets its
// person's claims and when they last authenticated.
func (w *Web) IDP() *webtest.IDP { return w.idp }

// Browser is a new browser with an empty cookie jar, which trusts the test
// CA alone, so that it reaches the stub and the IdP.
func (w *Web) Browser(t testing.TB) *webtest.Browser {
	t.Helper()
	return webtest.NewBrowser(t, &http.Client{Timeout: browserTimeout, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: w.pool, MinVersion: tls.VersionTLS12},
	}})
}

// SignIn signs a person in through the stub, as subject, in the given
// groups of the IdP's, and returns their browser on the items list, where
// sign-in returns them. It fails the test unless the list answers 200.
func (w *Web) SignIn(t testing.TB, subject string, groups ...string) (*webtest.Browser, *webtest.Page) {
	t.Helper()
	w.idp.SetClaims(map[string]any{"sub": subject, "groups": groups})
	b := w.Browser(t)
	p := b.Get(w.url + "/items")
	if p.Resp.StatusCode != http.StatusOK || p.Resp.Request.URL.Path != "/items" {
		t.Fatalf("harness: signing %s in ended at %s with %d:\n%s", subject, p.Resp.Request.URL, p.Resp.StatusCode, p.Body)
	}
	return b, p
}

// Upstream is the instance the stub forwarded its last request to; "" before
// any.
func (w *Web) Upstream() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

// register adds an instance's web listener to the stub's upstreams, or
// replaces the address of an instance that started again.
func (w *Web) register(instance, addr string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.upstreams[instance]; !ok {
		w.order = append(w.order, instance)
	}
	w.upstreams[instance] = addr
}

// ServeHTTP forwards r to the instance pick chooses, with its Host kept, as
// the deployment's proxy does (R95), or answers 502 when no instance has
// started.
func (w *Web) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	instance, addr := w.pick(r.Context())
	if addr == "" {
		http.Error(rw, "harness: the web's TLS stub has no instance to forward to", http.StatusBadGateway)
		return
	}
	w.mu.Lock()
	w.last = instance
	w.mu.Unlock()
	target := &url.URL{Scheme: "http", Host: addr}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
		},
		Transport: w.forward,
		ErrorLog:  log.New(discard{}, "", 0),
	}
	proxy.ServeHTTP(rw, r) //nolint:gosec // G704: forwarded to an instance's own loopback web listener, as the deployment's proxy does.
}

// pick is the instance a request goes to: the first, in the order they
// started, whose /leaderz answers 200; when none does, the one the stub
// forwarded to last; before any, the first that started.
func (w *Web) pick(ctx context.Context) (instance, addr string) {
	w.mu.Lock()
	order := slices.Clone(w.order)
	upstreams := maps.Clone(w.upstreams)
	last := w.last
	w.mu.Unlock()
	for _, name := range order {
		if w.leads(ctx, upstreams[name]) {
			return name, upstreams[name]
		}
	}
	if last != "" {
		return last, upstreams[last]
	}
	if len(order) > 0 {
		return order[0], upstreams[order[0]]
	}
	return "", ""
}

// leads says whether the web listener at addr answers /leaderz with 200.
func (w *Web) leads(ctx context.Context, addr string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/leaderz", http.NoBody)
	if err != nil {
		return false
	}
	resp, err := w.probe.Do(req) //nolint:gosec // G704: an instance's own loopback listener.
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Leaderz returns the status of a GET of /leaderz at an instance's web
// listener, straight, not through the stub.
func (i *Instance) Leaderz(t testing.TB) int {
	t.Helper()
	if i.addrs.Web == "" {
		t.Fatal("harness: serve wrote no web listener's address")
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+i.addrs.Web+"/leaderz", http.NoBody)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	resp, err := (&http.Client{Timeout: probeTimeout}).Do(req) //nolint:gosec // G704: the instance's own loopback listener.
	if err != nil {
		t.Fatalf("harness: GET /leaderz: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}
