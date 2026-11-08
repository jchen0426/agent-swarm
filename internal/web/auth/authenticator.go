// Package auth is the web interface's sign-in (Plan 9a-1, design D4): the
// keys derived from the session key and read at use, the sealed session
// cookie, the CSRF gate, return paths, and the actor a session acts as. Its
// pages hold no script.
package auth

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/web/origin"
)

// SignInPath is where a person signs in; a GET with ?return= names the page
// to come back to.
const SignInPath = "/auth/signin"

// Authenticator signs people in to one deployment's web interface, and
// guards its pages.
type Authenticator struct {
	web    deploy.Web
	origin string // web.publicURL's origin, normalized
	keys   *keyring
	clock  func() time.Time
	log    *slog.Logger
}

// NewAuthenticator makes the authenticator of the web block w, reading the
// session key through secrets at use. It reads no secret itself, so that
// serve starts while a key cannot be read; the pages then answer 503.
//
//nolint:gocritic // hugeParam: the plan's contract passes the web block by value.
func NewAuthenticator(w deploy.Web, secrets Secrets, clock func() time.Time, log *slog.Logger) (*Authenticator, error) {
	o := origin.Of(w.PublicURL, false)
	switch {
	case o == "":
		return nil, errors.New("web.publicURL is not an http or https URL with a host")
	case secrets == nil:
		return nil, errors.New("no secret store")
	case w.SessionKeyRef == "":
		return nil, errors.New("no web.sessionKeyRef")
	case w.FreshWindow <= 0:
		return nil, errors.New("no web.freshWindow")
	}
	if clock == nil {
		clock = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Authenticator{
		web: w, origin: o, clock: clock, log: log,
		keys: &keyring{
			secrets: secrets, ref: w.SessionKeyRef, issuer: w.OIDC.Issuer, clientID: w.OIDC.ClientID,
			clock: clock, log: log,
		},
	}, nil
}

// Keys are the deployment's keys, read at use (R86).
func (a *Authenticator) Keys(ctx context.Context) (Keys, error) { return a.keys.get(ctx) }

// Available answers every request 503 "sign-in is unavailable" while the
// session key cannot be read (R86). It wraps every page; the health routes
// are served beside the pages, never through it (R88).
func (a *Authenticator) Available(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.keys.get(r.Context()); err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("sign-in is unavailable"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Signed is a signed-in request's session, and its CSRF token, which every
// form the page renders carries as TokenField.
type Signed struct {
	Session Session
	Token   string
	Keys    Keys
}

type signedKey struct{}

// From is the session SignedIn put in ctx.
func From(ctx context.Context) (Signed, bool) {
	s, ok := ctx.Value(signedKey{}).(Signed)
	return s, ok
}

// session is the request's session: the cookie opened under the keys, for
// this deployment, or false. live says whether it has not expired.
func (a *Authenticator) session(r *http.Request) (s Session, k Keys, live, ok bool) {
	k, err := a.keys.get(r.Context())
	if err != nil {
		return Session{}, Keys{}, false, false
	}
	c, err := r.Cookie(CookieName)
	if err != nil {
		return Session{}, k, false, false
	}
	s, err = Open(k, c.Value)
	if err != nil {
		return Session{}, k, false, false
	}
	now := a.clock()
	live = now.Before(s.Expires) && !now.Before(s.IssuedAt.Add(-leeway))
	return s, k, live, true
}

// SignedIn passes a request with a live session to next, with the session
// in its context (From). Without one, a GET or HEAD is sent to sign-in with
// its path as the return path; any other method answers 401 with a page
// linking to sign-in, never a redirect, since a redirect after a form POST
// that leads to the provider is blocked by form-action (I8).
func (a *Authenticator) SignedIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, k, live, ok := a.session(r)
		if !ok || !live {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				back, safe := SafeReturn(r.URL.RequestURI())
				if !safe {
					back = "/"
				}
				w.Header().Set("Cache-Control", "no-store")
				http.Redirect(w, r, SignInPath+"?return="+url.QueryEscape(back), http.StatusSeeOther)
				return
			}
			page(w, http.StatusUnauthorized, notice{
				Title: "Signed out", Text: "Your session has ended. Sign in again, then repeat what you were doing.",
				Link: SignInPath, LinkText: "Sign in",
			})
			return
		}
		ctx := context.WithValue(r.Context(), signedKey{}, Signed{Session: s, Token: k.Token(&s), Keys: k})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// SetSession sets the session cookie holding s, sealed under the keys.
func (a *Authenticator) SetSession(w http.ResponseWriter, r *http.Request, s *Session) error {
	k, err := a.keys.get(r.Context())
	if err != nil {
		return err
	}
	sealedValue, err := s.Seal(k)
	if err != nil {
		return err
	}
	http.SetCookie(w, sessionCookie(sealedValue, s.Expires, a.clock()))
	return nil
}

// SignOut is POST /auth/signout, under the CSRF gate: it clears the session
// cookie and answers 200 with a page that says so and links to sign-in. It
// never redirects (R97; n5).
func (a *Authenticator) SignOut() http.Handler {
	return a.CSRF(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, clearedCookie())
		page(w, http.StatusOK, notice{
			Title: "Signed out", Text: "You are signed out.", Link: SignInPath, LinkText: "Sign in",
		})
	}))
}

// notice is a page of the authenticator's own: a title, a sentence and a
// link.
type notice struct {
	Title, Text, Link, LinkText string
}

var noticePage = template.Must(template.New("notice").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title></head>
<body><main><h1>{{.Title}}</h1><p>{{.Text}}</p>{{if .Link}}<p><a href="{{.Link}}">{{.LinkText}}</a></p>{{end}}</main></body>
</html>
`))

// page answers status with n, never cached.
func page(w http.ResponseWriter, status int, n notice) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = noticePage.Execute(w, n)
}
