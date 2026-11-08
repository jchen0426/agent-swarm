package auth

import (
	"net/http"
	"net/url"
)

// Fresh is GET /auth/fresh?return=: a signed-in person authenticates again
// at the provider, max_age=0 and prompt=login, so that, an admin, they act
// as one within web.freshWindow (R76). It comes back to the return path,
// the act's form, as a GET, never replaying the form's POST (I8). Without
// a live session it sends the person to sign in first. It refuses any
// method but GET.
func (a *Authenticator) Fresh() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !onlyGet(w, r) {
			return
		}
		back := returnOf(r)
		s, _, live, ok := a.session(r)
		if !ok || !live {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, SignInPath+"?return="+url.QueryEscape(back), http.StatusSeeOther)
			return
		}
		a.begin(w, r, back, &s)
	})
}

// checkFresh refuses a fresh authentication's identity id unless it is the
// flow f's person, issuer and subject, and its auth_time says that they
// authenticated within web.freshWindow plus the leeway: a provider that
// gives no auth_time, or ignored prompt=login, makes nobody fresh.
func (a *Authenticator) checkFresh(f *flow, id *identity) error {
	refuse := func(text, reason string) error {
		return &refusal{status: http.StatusForbidden, title: "Not re-authenticated", text: text, reason: reason}
	}
	switch {
	case id.issuer != f.Issuer || id.subject != f.Subject:
		return refuse("You signed in again as another person than the one signed in here, so nothing changed. Sign out first to change who you are.",
			"the fresh authentication's subject is not the session's")
	case id.authTime.IsZero():
		return refuse("Your identity provider did not say when you signed in, so admin rights cannot be given. Tell the operators.",
			"the fresh authentication's token carries no auth_time")
	case !a.fresh(id.authTime, a.clock()):
		return refuse("Your identity provider did not ask you to sign in again, so admin rights cannot be given. Try again, and if it fails again, tell the operators.",
			"the fresh authentication's auth_time is outside the fresh window")
	}
	return nil
}
