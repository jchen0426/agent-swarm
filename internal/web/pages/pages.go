// Package pages is the web interface's pages (Plan 9a-1, design D7, D8 and
// D10): the layout with the kill switch's fixed header, the items list, the
// item acts, the standby, and the routes of sign-in mounted beside them. The
// pages are rendered by html/template, hold no script, and post native
// forms. Every page is held to the session key's availability, a person
// signed in, and, for every POST, the CSRF gate (R86, R96).
package pages

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jchen0426/agent-swarm/internal/web"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
)

//go:embed templates/*.html static/app.css
var files embed.FS

// site serves the pages through the ports, as the authenticator lets them.
type site struct {
	p    *web.Ports
	a    *auth.Authenticator
	zone *time.Location
	log  *slog.Logger
	now  func() time.Time
}

// route is a route of the pages: its pattern, whether it takes a POST, and
// its handler.
type route struct {
	pattern string
	post    bool
	h       http.Handler
}

// New is the pages' handler, which web.Options.Pages takes: every route
// behind the authenticator's Available, which answers 503 while the
// session key cannot be read (R86; Task 5's m1); every page behind
// SignedIn and marked web.Authenticated, never cached; every POST behind
// the CSRF gate, then SignedIn, so that no POST opens a session without
// SignedIn's expiry check (Task 5's m3), and none is answered with a
// redirect (I8). Times are shown in zone. Sign-in's own routes, and the
// stylesheet, are served beside them.
func New(p *web.Ports, a *auth.Authenticator, zone *time.Location) http.Handler {
	s := newSite(p, a, zone)
	mux := http.NewServeMux()
	for _, r := range s.routes() {
		mux.Handle(r.pattern, r.h)
	}
	return a.Available(mux)
}

func newSite(p *web.Ports, a *auth.Authenticator, zone *time.Location) *site {
	s := &site{p: p, a: a, zone: zone, log: p.Log, now: p.Clock}
	if s.zone == nil {
		s.zone = time.UTC
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// routes are the pages' routes. Sign-in's routes are mounted with no
// method, as each refuses the methods it does not take itself; sign-out
// takes a POST under its own CSRF gate (R97).
func (s *site) routes() []route {
	page := func(h http.HandlerFunc) http.Handler { return web.Authenticated(s.a.SignedIn(h)) }
	form := func(h http.HandlerFunc) http.Handler { return web.Authenticated(s.a.CSRF(s.a.SignedIn(h))) }
	return []route{
		{pattern: "GET /{$}", h: page(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/items", http.StatusSeeOther)
		})},
		{pattern: "GET /items", h: page(s.items)},
		{pattern: "GET /items/{id}/{act}", h: page(s.actForm)},
		{pattern: "POST /items/{id}/{act}", post: true, h: form(s.actCommit)},
		{pattern: "GET " + switchPath, h: page(s.switchControl)},
		{pattern: "POST " + switchPath, post: true, h: form(s.switchCommit)},
		{pattern: "GET /static/app.css", h: http.HandlerFunc(stylesheet)},
		{pattern: auth.SignInPath, h: s.a.SignIn()},
		{pattern: auth.CallbackPath, h: s.a.Callback()},
		{pattern: auth.FreshPath, h: s.a.Fresh()},
		{pattern: "/auth/signout", post: true, h: s.a.SignOut()},
	}
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
