//go:build e2e

package harness

import (
	"bytes"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"testing"
	"time"
)

// endpoint is the loopback server the scripted vendor and the scripted MCP
// servers run on: HTTP, or HTTPS when it has a TLS configuration. It can go
// down, so that connections to its address are refused, and come up again on
// the same address.
type endpoint struct {
	t       testing.TB
	handler http.Handler
	tls     *tls.Config
	addr    string
	// stop is closed when the test ends. A handler that waits, to delay its
	// answer or to hang, stops waiting then.
	stop     chan struct{}
	stopOnce sync.Once

	// reportMu orders what the server's goroutines report to the test against
	// the end of the test, which takes no report after it.
	reportMu sync.Mutex
	over     bool

	mu  sync.Mutex
	srv *http.Server
}

// newEndpoint starts a server for handler on a loopback port, and stops it
// when the test ends.
func newEndpoint(t testing.TB, handler http.Handler, tlsConfig *tls.Config) *endpoint {
	t.Helper()
	e := &endpoint{t: t, tls: tlsConfig, stop: make(chan struct{})}
	e.handler = e.guard(handler)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("harness: listen on the loopback: %v", err)
	}
	e.addr = ln.Addr().String()
	e.serve(ln)
	t.Cleanup(func() {
		e.stopOnce.Do(func() { close(e.stop) })
		e.down()
		e.reportMu.Lock()
		e.over = true
		e.reportMu.Unlock()
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
		ErrorLog:          log.New(serverLog{e}, "", 0),
		// No HTTP/2, which cannot drop a connection.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	e.mu.Lock()
	e.srv = srv
	e.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
}

// guard makes a panic in a handler fail the test, with the panic's stack, and
// drops the connection. A script is code of the test's, and a client that saw
// only the dropped connection would pass many a scenario that expects one for
// another reason.
func (e *endpoint) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(p) // the handler aborted on purpose
			}
			e.report("harness: a scripted server's handler panicked: %v\n%s", p, debug.Stack())
			panic(http.ErrAbortHandler)
		}()
		next.ServeHTTP(w, r)
	})
}

// report fails the test, unless it has ended.
func (e *endpoint) report(format string, args ...any) {
	e.reportMu.Lock()
	defer e.reportMu.Unlock()
	if !e.over {
		e.t.Errorf(format, args...)
	}
}

// logf writes to the test's log, unless the test has ended.
func (e *endpoint) logf(format string, args ...any) {
	e.reportMu.Lock()
	defer e.reportMu.Unlock()
	if !e.over {
		e.t.Logf(format, args...)
	}
}

// serverLog carries what the HTTP server logs to the test's log: a request it
// could not read, say. A failed TLS handshake is left out, because the tests
// make one on purpose when they check that a client that does not trust the CA
// is refused.
type serverLog struct{ e *endpoint }

func (l serverLog) Write(p []byte) (int, error) {
	if !bytes.Contains(p, []byte("TLS handshake error")) {
		l.e.logf("harness: the HTTP server logged: %s", bytes.TrimSpace(p))
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

// up listens again on the address the server had, and reports whether the
// server was down: one that is up goes on.
func (e *endpoint) up() bool {
	e.t.Helper()
	e.mu.Lock()
	running := e.srv != nil
	e.mu.Unlock()
	if running {
		return false
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
	return true
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
