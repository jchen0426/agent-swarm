package auth

import (
	"crypto/hmac"
	"net/http"

	"github.com/jchen0426/agent-swarm/internal/web/origin"
)

// TokenField is the form field that carries the CSRF token.
const TokenField = "csrf"

// CSRF is the CSRF gate (R96; NI1). Every request but a GET or HEAD must
// carry an Origin header equal, normalized, to web.publicURL's origin: a
// missing Origin, Origin: null and any other origin are refused 403,
// whatever Sec-Fetch-Site says, since http.CrossOriginProtection would let
// Sec-Fetch-Site: same-origin, or neither header, through. Then the form's
// TokenField must be the session cookie's token. A session past its expiry
// still holds its token, so that sign-out works on it; whether the session
// is live is SignedIn's to judge. No refusal redirects.
func (a *Authenticator) CSRF(next http.Handler) http.Handler { return a.gate(next, nil) }

// CSRFOr is CSRF, but a request whose Origin passes, whose session opens and
// is live, and whose token is not the session's goes to again, as a new
// sign-in leaves an open form's token behind (R157). again acts on nothing.
func (a *Authenticator) CSRFOr(next, again http.Handler) http.Handler { return a.gate(next, again) }

// gate is the CSRF gate over next, which sends a live session's request
// whose token is not the session's to again, or, with no again, refuses it.
func (a *Authenticator) gate(next, again http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		values := r.Header.Values("Origin")
		if len(values) != 1 || origin.Of(values[0], true) != a.origin {
			page(w, http.StatusForbidden, notice{
				Title: "Refused", Text: "This form was not sent from this site's own page, so it was not acted on.",
				Link: "/", LinkText: "Back to the start page",
			})
			return
		}
		s, k, live, ok := a.session(r)
		if !ok {
			page(w, http.StatusForbidden, notice{
				Title: "Signed out", Text: "Your session has ended. Sign in again, then repeat what you were doing.",
				Link: SignInPath, LinkText: "Sign in",
			})
			return
		}
		if got := r.PostFormValue(TokenField); got == "" || !hmac.Equal([]byte(got), []byte(k.Token(&s))) {
			if again != nil && live {
				again.ServeHTTP(w, r)
				return
			}
			RefuseToken(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RefuseToken answers a form whose token is not its session's as the CSRF
// gate does (R96): 403, saying that the form is out of date or not from
// this session, never a redirect. A stale form's again refuses with it a
// form it cannot render again, so that it refuses as today (R157).
func RefuseToken(w http.ResponseWriter) {
	page(w, http.StatusForbidden, notice{
		Title: "Refused", Text: "This form is out of date or not from this session. Reload the page and try again.",
		Link: "/", LinkText: "Back to the start page",
	})
}
