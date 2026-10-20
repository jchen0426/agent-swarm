//go:build e2e

package harness

import (
	"bytes"
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// endpoint is the loopback server the scripted vendor and the scripted MCP
// servers run on: HTTP, or HTTPS when it has a TLS configuration. It can go
// down, so that connections to its address are refused, and come up again on
// the same address.
type endpoint struct {
	t       *testing.T
	handler http.Handler
	tls     *tls.Config
	addr    string
	// stop is closed when the test ends. A handler that waits, to delay its
	// answer or to hang, stops waiting then.
	stop     chan struct{}
	stopOnce sync.Once

	mu  sync.Mutex
	srv *http.Server
}

// newEndpoint starts a server for handler on a loopback port, and stops it
// when the test ends.
func newEndpoint(t *testing.T, handler http.Handler, tlsConfig *tls.Config) *endpoint {
	t.Helper()
	e := &endpoint{t: t, handler: handler, tls: tlsConfig, stop: make(chan struct{})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("harness: listen on the loopback: %v", err)
	}
	e.addr = ln.Addr().String()
	e.serve(ln)
	t.Cleanup(func() {
		e.stopOnce.Do(func() { close(e.stop) })
		e.down()
	})
	return e
}

func (e *endpoint) serve(ln net.Listener) {
	if e.tls != nil {
		ln = tls.NewListener(ln, e.tls)
	}
	srv := &http.Server{
		Handler:           e.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(&serverLog{t: e.t, stop: e.stop}, "", 0),
		// No HTTP/2, which cannot drop a connection.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	e.mu.Lock()
	e.srv = srv
	e.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
}

// serverLog carries what the HTTP server logs to the test's log: a handler
// that panicked, such as a script with a bug, or a request the server could not
// read. A failed TLS handshake is left out, because the test makes one when it
// checks that a client that does not trust the CA is refused.
type serverLog struct {
	t    *testing.T
	stop <-chan struct{}
}

func (l *serverLog) Write(p []byte) (int, error) {
	select {
	case <-l.stop:
		// The test is ending: it may be over by the time the line is logged.
	default:
		if !bytes.Contains(p, []byte("TLS handshake error")) {
			l.t.Logf("harness: the HTTP server logged: %s", bytes.TrimSpace(p))
		}
	}
	return len(p), nil
}

// down closes the listener and every connection. It does nothing when the
// server is down already.
func (e *endpoint) down() {
	e.mu.Lock()
	srv := e.srv
	e.srv = nil
	e.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

// up listens again on the address the server had.
func (e *endpoint) up() {
	e.t.Helper()
	e.mu.Lock()
	running := e.srv != nil
	e.mu.Unlock()
	if running {
		return
	}
	// The address was just released; the old server may still hold it for a
	// moment.
	deadline := time.Now().Add(5 * time.Second)
	ln, err := net.Listen("tcp", e.addr)
	for err != nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		ln, err = net.Listen("tcp", e.addr)
	}
	if err != nil {
		e.t.Fatalf("harness: listen again on %s: %v", e.addr, err)
	}
	e.serve(ln)
}

// url returns the address of path on the server.
func (e *endpoint) url(path string) string {
	scheme := "http"
	if e.tls != nil {
		scheme = "https"
	}
	return scheme + "://" + e.addr + path
}

// wait waits for d, and reports whether it passed: it does not when the
// client gave up or the test ended first.
func (e *endpoint) wait(r *http.Request, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-r.Context().Done():
		return false
	case <-e.stop:
		return false
	}
}

// hang holds the request until the client gives up or the test ends.
func (e *endpoint) hang(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-e.stop:
	}
}

// dropConnection closes the request's connection without an answer.
func dropConnection(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		// The connection cannot be taken over: end the request abruptly.
		panic(http.ErrAbortHandler)
	}
	_ = conn.Close()
}
