// Package pages is the web interface's pages (Plan 9a-1, design D7, D8 and
// D10): the layout with the kill switch's fixed header, the items list, the
// item acts, the standby, the boards (Plan 9a-2), and the routes of sign-in
// mounted beside them. The pages are rendered by html/template and post
// native forms; they hold no script but the board page's live.js, which
// polls its board's regions and which the page does without (design D1).
// Every page is held to the session key's availability, a person signed in,
// and, for every POST, the CSRF gate (R86, R96).
package pages

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jchen0426/agent-swarm/internal/web"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

//go:embed templates/*.html static/app.css static/live.js
var files embed.FS

// monotonic is the clock of the windows a site keeps, the process's
// monotonic clock (time.Now's reading), which tests set: the board pages'
// read of the lease's holder (see boardLeader), and a board read's failure
// logged once a minute (see failureDue), so that a page clock that stands
// still, as the database's time as the instance estimates it may, holds
// neither.
var monotonic = time.Now

// site serves the pages through the ports, as the authenticator lets them.
type site struct {
	p    *web.Ports
	a    *auth.Authenticator
	zone *time.Location
	log  *slog.Logger
	now  func() time.Time
	// previewLog is the log of the kill switch's previews (see preview).
	previewLog *slog.Logger
	// previewing is set while an on's preview waits on the coordinator, so
	// that at most one does (see preview).
	previewing atomic.Bool
	// warned holds the items whose invalid records were logged, once each.
	warned sync.Map
	// mono is the clock of the windows the site keeps (monotonic).
	mono func() time.Time
	// mu guards the board pages' read of the lease's holder on a standby kept,
	// when it ended, and the one that runs (see boardLeader); and failed, when
	// a board read's failure of each item was last logged (see failureDue).
	mu       sync.Mutex
	holder   *standbyView
	holderAt time.Time
	reading  *holderRead
	failed   map[item.ID]time.Time
}

// route is a route of the pages: its pattern, whether it takes a POST, and
// its handler.
type route struct {
	pattern string
	post    bool
	// board says that the route shows a board, and so asks visible (D9).
	board bool
	h     http.Handler
}

// New is the pages' handler, which web.Options.Pages takes: every route
// behind the authenticator's Available, which answers 503 while the
// session key cannot be read (R86; Task 5's m1); every page behind
// SignedIn and marked web.Authenticated, never cached; every POST behind
// the CSRF gate, then SignedIn, so that no POST opens a session without
// SignedIn's expiry check (Task 5's m3), and none is answered with a
// redirect (I8); the act's and the opening's forms behind CSRFOr instead,
// so that a form whose token a new sign-in left behind is rendered again
// with its key, under SignedIn as well (R157); a board's polls behind
// Polled, which answers 401 rather than send a script to sign-in (R161).
// Times are shown in zone. Sign-in's own routes, the stylesheet and live.js
// are served beside them, outside SignedIn.
func New(p *web.Ports, a *auth.Authenticator, zone *time.Location) http.Handler {
	s := newSite(p, a, zone)
	mux := http.NewServeMux()
	for _, r := range s.routes() {
		mux.Handle(r.pattern, r.h)
	}
	return a.Available(mux)
}

func newSite(p *web.Ports, a *auth.Authenticator, zone *time.Location) *site {
	s := &site{p: p, a: a, zone: zone, log: p.Log, now: p.Clock, mono: monotonic, failed: map[item.ID]time.Time{}}
	if s.zone == nil {
		s.zone = time.UTC
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if p.Switching.Log != nil {
		s.previewLog = slog.New(demoted{p.Switching.Log.Handler()})
	}
	return s
}

// routes are the pages' routes. Sign-in's routes are mounted with no
// method, as each refuses the methods it does not take itself; sign-out
// takes a POST under its own CSRF gate (R97).
func (s *site) routes() []route {
	page := func(h http.HandlerFunc) http.Handler { return web.Authenticated(s.a.SignedIn(h)) }
	form := func(h http.HandlerFunc) http.Handler { return web.Authenticated(s.a.CSRF(s.a.SignedIn(h))) }
	// retried is form for a form that carries its key (R85), which a person
	// may send again after a new sign-in left its token behind: that form goes
	// to again (R157).
	retried := func(h, again http.HandlerFunc) http.Handler {
		return web.Authenticated(s.a.CSRFOr(s.a.SignedIn(h), s.a.SignedIn(again)))
	}
	return []route{
		{pattern: "GET /{$}", h: page(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/items", http.StatusSeeOther)
		})},
		{pattern: "GET /items", h: page(s.items)},
		{pattern: "GET " + openPath, h: page(s.openForm)},
		{pattern: "POST " + openPath, post: true, h: retried(s.openCommit, s.openAgain)},
		{pattern: "GET /items/{id}", board: true, h: boardPolicy(page(s.board))},
		{pattern: "GET /items/{id}/board", board: true, h: boardPolicy(web.Authenticated(s.a.Polled(http.HandlerFunc(s.boardRegions))))},
		{pattern: "GET /items/{id}/calls/{callID}", board: true, h: page(s.output)},
		{pattern: "GET /items/{id}/{act}", h: page(s.actForm)},
		{pattern: "POST /items/{id}/{act}", post: true, h: retried(s.actCommit, s.actAgain)},
		{pattern: "GET " + switchPath, h: page(s.switchControl)},
		{pattern: "POST " + switchPath, post: true, h: form(s.switchCommit)},
		{pattern: "GET /static/app.css", h: http.HandlerFunc(stylesheet)},
		{pattern: "GET /static/{file}", h: http.HandlerFunc(liveJS)},
		{pattern: auth.SignInPath, h: s.a.SignIn()},
		{pattern: auth.CallbackPath, h: s.a.Callback()},
		{pattern: auth.FreshPath, h: s.a.Fresh()},
		{pattern: "/auth/signout", post: true, h: s.a.SignOut()},
	}
}

// boardPolicy is h under the board pages' policy, web.BoardPolicy, whatever
// h answers: the board page and its polls alone, the routes whose page runs
// live.js (design D1). It is chosen by route, never by the board flag: the
// raw output's page shows a board too, and keeps 9a-1's policy (Task 4's
// review).
func boardPolicy(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", web.BoardPolicy)
		h.ServeHTTP(w, r)
	})
}

// stylesheet serves the one stylesheet (design D10).
func stylesheet(w http.ResponseWriter, _ *http.Request) {
	css, err := fs.ReadFile(files, "static/app.css")
	if err != nil {
		http.Error(w, "the stylesheet could not be read", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=300")
	_, _ = w.Write(css)
}
