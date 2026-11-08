// Package web is the web interface for people (Plan 9a-1): its listener's
// server, the security headers every response carries, the Host check, the
// health routes and the request logs (Task 4), and, in later tasks, sign-in
// and the pages. It listens on loopback, plain HTTP, behind the deployment's
// TLS-terminating proxy, which forwards the client's Host unchanged (design
// D2; R95). It never imports a store or the admin API: what it needs reaches
// it as ports that serve wires.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The server's bounds (design D2): a request's header within the header
// timeout [10 s] and MaxHeaderBytes [64 KiB], its whole read within
// readTimeout, its body within maxBody [64 KiB], as the admin API's, and an
// idle connection within idleTimeout [120 s].
const (
	DefaultHeaderTimeout = 10 * time.Second
	readTimeout          = 30 * time.Second
	maxHeaderBytes       = 64 << 10
	maxBody              = 64 << 10
	idleTimeout          = 120 * time.Second
)

// Leader is the leader's status: whether this instance holds the lease, and
// whether it drains (app.Leader).
type Leader interface {
	Status() (epoch int64, held, draining bool)
}

// Schema says how the schema stands to this build: whether the build runs on
// it, and its state's word, such as behind, for a refusal.
type Schema interface {
	State(ctx context.Context) (compatible bool, state string, err error)
}

// Coordinator is the coordinator's item acts, which the pages ask through
// it alone (app.Coordinator).
type Coordinator interface {
	Promote(ctx context.Context, id item.ID, a *app.Act) (app.Outcome, error)
	Close(ctx context.Context, id item.ID, a *app.Act) (app.Outcome, error)
	Reopen(ctx context.Context, id item.ID, a *app.Act) (app.Outcome, error)
	Open(ctx context.Context, in *app.OpenInput) (item.ID, error)
}

// Views are the reads the pages show (app.Views): the items list; one item
// with its board's report, for the board page; the
// kill switch's table, which the fixed header reads, never a preview (M6);
// and the items an on-row left not staffed, which the off's page shows as
// its preview (M7).
type Views interface {
	List(ctx context.Context) ([]app.Row, error)
	Item(ctx context.Context, id item.ID) (app.View, error)
	KillSwitch(ctx context.Context) (app.KillSwitch, error)
	UnstaffedBy(ctx context.Context, seq int64) ([]item.ID, error)
}

// LeaseHolder reads the lease's holder (R90): the leader's name, d.Holder,
// which a standby shows, and when its hold expires; an empty name for a
// lease nobody took (postgres.Lease.Holder).
type LeaseHolder interface {
	Holder(ctx context.Context) (name string, expires time.Time, err error)
}

// Revisions give the configuration's current revision (the registry's).
type Revisions interface {
	Current() *config.Revision
}

// Ports are what the web interface reads and acts through, which serve
// wires (I6): the health routes' leader and schema; the coordinator's item
// acts; the views; the kill switch's routing (switching.Ports); the lease's
// holder, for a standby; the revisions; the tenant, of an opening's id
// (R85); the clock pages show times by; and the log.
type Ports struct {
	Leader      Leader
	Schema      Schema
	Coordinator Coordinator
	Views       Views
	Switching   switching.Ports
	LeaseHolder LeaseHolder
	Revisions   Revisions
	Tenant      string
	// Clock is the time pages show times against; nil is time.Now.
	Clock func() time.Time
	Log   *slog.Logger
}

// Options are the web interface's settings.
type Options struct {
	// PublicURL is the deployment's web.publicURL: a page's request whose
	// Host is not its host is refused (R95).
	PublicURL string
	// HeaderTimeout bounds a request's header [DefaultHeaderTimeout].
	HeaderTimeout time.Duration
	// Log is serve's log, which the requests and the server's errors go to.
	Log *slog.Logger
	// Pages are the routes but the health routes, served behind the Host
	// check: nil, which answers 404 to all of them, until sign-in and the
	// pages come (Tasks 5 to 9).
	Pages http.Handler
}

// NewServer makes the web listener's server (design D2): HTTP/1 alone, since
// Go's HTTP/2 server has no per-request header timeout, with the header
// timeout, MaxHeaderBytes of 64 KiB, an IdleTimeout of 120 s and a body cap
// of 64 KiB. Its handler logs each request, sets the security headers on
// every response, serves the health routes beside the pages, never through
// them (R88), and holds the pages to the Host check (R95).
func NewServer(p *Ports, o Options) *http.Server {
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	timeout := o.HeaderTimeout
	if timeout <= 0 {
		timeout = DefaultHeaderTimeout
	}
	pages := o.Pages
	if pages == nil {
		pages = http.NotFoundHandler()
	}
	h := &health{p: *p, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
	mux.HandleFunc("GET /leaderz", h.leaderz)
	mux.Handle("/", newHostCheck(o.PublicURL, log).wrap(pages))
	srv := &http.Server{
		Handler:           logged(log, secured(http.MaxBytesHandler(mux, maxBody))),
		ReadHeaderTimeout: timeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		Protocols:         new(http.Protocols),
	}
	srv.Protocols.SetHTTP1(true)
	return srv
}

// healthRoutes are the health routes' paths, which a load balancer probes
// often: their request logs are at Debug.
var healthRoutes = map[string]bool{"/healthz": true, "/readyz": true, "/leaderz": true}

// logged logs each request once it is answered: its method, its path
// without the query, and its status, and never the query, a cookie, a header
// or the body (M10). The health routes' logs are at Debug, so that a load
// balancer's probes do not flood serve's log; the rest are at Info.
func logged(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		level := slog.LevelInfo
		if healthRoutes[r.URL.Path] {
			level = slog.LevelDebug
		}
		log.Log(r.Context(), level, "web request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"took", time.Since(start).String())
	})
}

// recorder keeps the status a handler answers with.
type recorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *recorder) WriteHeader(status int) {
	if !r.wrote {
		r.status, r.wrote = status, true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(p []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(p)
}

// Unwrap gives http.ResponseController the writer underneath.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
