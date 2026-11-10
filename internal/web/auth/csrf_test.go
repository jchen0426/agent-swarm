package auth_test

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/web"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/webtest"
)

// post is a form POST of values to /act with c, and the given headers.
func post(c *http.Cookie, values url.Values, headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/act", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if c != nil {
		req.AddCookie(c)
	}
	return req
}

// Every POST must carry an Origin equal, normalized, to web.publicURL's
// origin: a missing Origin, Origin: null and any other origin are refused
// whatever Sec-Fetch-Site says, and then the form token must be the
// session's (R96; NI1; m15).
func TestCSRFRefusesAMissingNullOrForeignOriginWhateverSecFetchSiteSays(t *testing.T) {
	for _, tc := range []struct {
		publicURL         string
		accepted, refused []string
	}{
		{
			publicURL: publicURL,
			accepted: []string{
				"https://agents.example.com", "HTTPS://AGENTS.Example.COM", "https://agents.example.com:443",
				"https://agents.example.com.", "https://Agents.Example.Com.:443",
			},
			refused: []string{
				"", "null", "https://evil.example.com", "http://agents.example.com", "https://agents.example.com:8443",
				"https://agents.example.com/", "https://agents.example.com/items", "https://agents.example.com..",
				"https://user@agents.example.com", "https://agents.example.com.evil", "agents.example.com",
				"https://аgents.example.com", "https://%61gents.example.com", "null, https://agents.example.com",
			},
		},
		{
			publicURL: "https://key.example.com/",
			accepted:  []string{"https://key.example.com", "https://KEY.example.com"},
			refused:   []string{"https://Key.example.com", "https://%E2%84%AAey.example.com"},
		},
		{
			publicURL: "https://[2001:db8::1]",
			accepted:  []string{"https://[2001:db8::1]", "https://[2001:DB8::1]:443", "https://[2001:db8:0:0::1]"},
			refused:   []string{"https://[2001:db8::2]", "https://2001:db8::1"},
		},
	} {
		w := newWorld(t, func(b *deploy.Web) { b.PublicURL = tc.publicURL })
		k := w.keys(t)
		s := w.session(auth.Responder)
		c := cookie(t, k, s)
		form := url.Values{auth.TokenField: {k.Token(&s)}}
		guarded := w.a.CSRF(ok)

		for _, origin := range tc.accepted {
			out := serve(guarded, post(c, form, map[string]string{"Origin": origin, "Sec-Fetch-Site": "same-origin"}))
			assert.Equal(t, http.StatusOK, out.Code, "%s: %q is the public URL's origin", tc.publicURL, origin)
		}
		for _, origin := range tc.refused {
			for _, site := range []string{"", "same-origin", "none", "cross-site"} {
				headers := map[string]string{"Sec-Fetch-Site": site}
				if origin != "" {
					headers["Origin"] = origin
				}
				out := serve(guarded, post(c, form, headers))
				assert.Equal(t, http.StatusForbidden, out.Code, "%s: Origin %q, Sec-Fetch-Site %q", tc.publicURL, origin, site)
			}
		}
	}

	w := newWorld(t, nil)
	k := w.keys(t)
	s := w.session(auth.Responder)
	c := cookie(t, k, s)
	same := map[string]string{"Origin": publicURL, "Sec-Fetch-Site": "same-origin"}
	guarded := w.a.CSRF(ok)
	bob := s
	bob.Subject = "bob"
	later := s
	later.IssuedAt = s.IssuedAt.Add(time.Second)

	for name, form := range map[string]url.Values{
		"a missing token":          {},
		"an empty token":           {auth.TokenField: {""}},
		"another person's token":   {auth.TokenField: {k.Token(&bob)}},
		"another sign-in's token":  {auth.TokenField: {k.Token(&later)}},
		"a token in the wrong key": {auth.TokenField: {mustKeys(t, newSecret(t)).Token(&s)}},
	} {
		out := serve(guarded, post(c, form, same))
		assert.Equal(t, http.StatusForbidden, out.Code, name)
		assert.Empty(t, out.Header().Get("Location"), name)
	}
	// A token with no session to hold it to is refused, with a sign-in
	// link, never a redirect.
	out := serve(guarded, post(nil, url.Values{auth.TokenField: {k.Token(&s)}}, same))
	assert.Equal(t, http.StatusForbidden, out.Code)
	assert.Empty(t, out.Header().Get("Location"))
	assert.Contains(t, out.Body.String(), `href="/auth/signin"`)
	// The token in the query is not read.
	req := post(c, url.Values{}, same)
	req.URL.RawQuery = auth.TokenField + "=" + url.QueryEscape(k.Token(&s))
	assert.Equal(t, http.StatusForbidden, serve(guarded, req).Code)

	// A GET passes with neither.
	assert.Equal(t, http.StatusOK, serve(guarded, httptest.NewRequest(http.MethodGet, "/act", http.NoBody)).Code)
}

// The stale form's fallback (R157) is reached only past the Origin check: a
// foreign, null or missing Origin is refused 403, in the CSRF gate's words,
// before again is asked, whatever the token. From the site's own origin a
// live session's stale token goes to again, and its own token to next; a
// form with no token or an empty one, which no sign-in left behind, is
// refused as the CSRF gate refuses it, as is a stale token with no session
// or with one past its expiry, and again is not asked (review m1).
func TestTheStaleFormFallbackNeverPassesAForeignOrigin(t *testing.T) {
	w := newWorld(t, nil)
	k := w.keys(t)
	s := w.session(auth.Responder)
	c := cookie(t, k, s)
	earlier := s
	earlier.IssuedAt = s.IssuedAt.Add(-10 * time.Minute)
	stale, own := url.Values{auth.TokenField: {k.Token(&earlier)}}, url.Values{auth.TokenField: {k.Token(&s)}}
	asked := 0
	guarded := w.a.CSRFOr(ok, http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		asked++
		_, _ = io.WriteString(rw, "again")
	}))

	for _, origin := range []string{"", "null", "https://evil.example.com", "http://agents.example.com", "https://agents.example.com:8443"} {
		for name, form := range map[string]url.Values{"a stale token": stale, "the session's token": own} {
			for _, site := range []string{"", "same-origin", "cross-site"} {
				headers := map[string]string{"Sec-Fetch-Site": site}
				if origin != "" {
					headers["Origin"] = origin
				}
				out := serve(guarded, post(c, form, headers))
				assert.Equal(t, http.StatusForbidden, out.Code, "Origin %q, %s, Sec-Fetch-Site %q", origin, name, site)
				assert.Contains(t, out.Body.String(), "This form was not sent from this site&#39;s own page, so it was not acted on.", "Origin %q", origin)
				assert.Empty(t, out.Header().Get("Location"))
			}
		}
	}
	assert.Zero(t, asked, "again is never asked past a foreign or missing Origin")

	same := map[string]string{"Origin": publicURL, "Sec-Fetch-Site": "same-origin"}
	out := serve(guarded, post(c, stale, same))
	assert.Equal(t, http.StatusOK, out.Code)
	assert.Equal(t, "again", out.Body.String(), "a live session's stale token goes to again")
	assert.Equal(t, "ok", serve(guarded, post(c, own, same)).Body.String(), "the session's own token goes to next")
	require.Equal(t, 1, asked)

	for name, form := range map[string]url.Values{"no token": {}, "an empty token": {auth.TokenField: {""}}} {
		out = serve(guarded, post(c, form, same))
		assert.Equal(t, http.StatusForbidden, out.Code, "%s, under a live session", name)
		assert.Contains(t, out.Body.String(), "This form is out of date or not from this session. Reload the page and try again.", name)
	}
	assert.Equal(t, 1, asked, "no token is no form a sign-in left behind")

	out = serve(guarded, post(nil, stale, same))
	assert.Equal(t, http.StatusForbidden, out.Code, "no session")
	assert.Contains(t, out.Body.String(), `href="/auth/signin"`)
	expired := s
	expired.IssuedAt, expired.Expires = s.IssuedAt.Add(-9*time.Hour), s.IssuedAt.Add(-time.Hour)
	out = serve(guarded, post(cookie(t, k, expired), stale, same))
	assert.Equal(t, http.StatusForbidden, out.Code, "a session past its expiry")
	assert.Contains(t, out.Body.String(), "This form is out of date or not from this session. Reload the page and try again.")
	assert.Equal(t, 1, asked, "again is asked of a live session alone")

	// A GET passes with neither, as the CSRF gate lets it.
	assert.Equal(t, "ok", serve(guarded, httptest.NewRequest(http.MethodGet, "/act", http.NoBody)).Body.String())
}

func mustKeys(t *testing.T, secret string) auth.Keys {
	t.Helper()
	k, err := auth.NewKeys(secret, issuer, clientID)
	require.NoError(t, err)
	return k
}

// served is the real web server over the authenticator's pages, on a TLS
// listener whose address is web.publicURL, so that a browser's origin is the
// public URL's: /form serves a form posted to /act, which answers
// "accepted", and sign-out is served at /auth/signout. extra goes into the
// form page's head.
func served(t *testing.T, extra string) (*world, *httptest.Server) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	public := "https://" + srv.Listener.Addr().String()
	w := newWorld(t, func(b *deploy.Web) { b.PublicURL = public })
	// webtest.SignedIn signs in at the wall clock's now.
	w.clock.now = time.Now()
	mux := http.NewServeMux()
	mux.Handle("GET /form", w.a.SignedIn(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		s, _ := auth.From(r.Context())
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(rw, `<!doctype html><html><head><title>Form</title>%s</head><body>
<form method="post" action="/act"><input type="hidden" name="%s" value="%s"><button>Act</button></form>
<form method="post" action="/auth/signout"><input type="hidden" name="%s" value="%s"><button>Sign out</button></form>
</body></html>`, extra, auth.TokenField, s.Token, auth.TokenField, s.Token)
	})))
	mux.Handle("POST /act", w.a.CSRF(w.a.SignedIn(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(rw, "accepted")
	}))))
	// Sign-out is mounted on a route with no method, as a careless wiring
	// would, so that its refusal of a GET is its own (I1).
	mux.Handle("/auth/signout", w.a.SignOut())
	server := web.NewServer(&web.Ports{}, web.Options{
		PublicURL: public, Log: slog.New(slog.DiscardHandler), Pages: w.a.Available(mux),
	})
	srv.Config.Handler = server.Handler
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return w, srv
}

// A form served by a page through the real middleware, the security headers
// with Referrer-Policy: same-origin among them, and posted by a browser that
// applies the Fetch rule for Origin, carries the public URL's origin and is
// accepted; the same page under no-referrer sends Origin: null, and is
// refused, so the test fails if no-referrer comes back (N1; I8).
func TestAServedFormsPostCarriesItsOriginAndIsAccepted(t *testing.T) {
	w, srv := served(t, "")
	b := webtest.NewBrowser(t, srv.Client())
	b.SetCookie(srv.URL, webtest.SignedIn(t, w.keys(t), []auth.Role{auth.Responder}, "alice"))

	page := b.Get(srv.URL + "/form")
	require.Equal(t, http.StatusOK, page.Resp.StatusCode)
	assert.Equal(t, "same-origin", page.Resp.Header.Get("Referrer-Policy"))
	answer := b.Submit(page, "/act", nil)
	assert.Equal(t, http.StatusOK, answer.Resp.StatusCode)
	assert.Equal(t, "accepted", string(answer.Body))

	w, srv = served(t, `<meta name="referrer" content="no-referrer">`)
	b = webtest.NewBrowser(t, srv.Client())
	b.SetCookie(srv.URL, webtest.SignedIn(t, w.keys(t), []auth.Role{auth.Responder}, "alice"))
	page = b.Get(srv.URL + "/form")
	require.Equal(t, http.StatusOK, page.Resp.StatusCode)
	assert.Equal(t, http.StatusForbidden, b.Submit(page, "/act", nil).Resp.StatusCode,
		"a page under no-referrer posts Origin: null")
}

// Sign-out is a POST under the CSRF gate: a GET is not served, a POST with
// no Origin or no token is refused and keeps the session, and a good one
// clears the cookie and answers 200 with a page that says so and links to
// sign-in, never a redirect (R97; n5).
func TestSignOutIsAPostUnderCSRFAndAnswersAPage(t *testing.T) {
	w, srv := served(t, "")
	signedIn := webtest.SignedIn(t, w.keys(t), []auth.Role{auth.Responder}, "alice")
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	k := w.keys(t)
	s, err := auth.Open(k, signedIn.Value)
	require.NoError(t, err)

	do := func(method string, headers map[string]string, form url.Values) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+"/auth/signout", strings.NewReader(form.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		req.AddCookie(signedIn)
		resp, err := client.Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	token := url.Values{auth.TokenField: {k.Token(&s)}}

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete} {
		resp := do(method, map[string]string{"Origin": srv.URL, "Sec-Fetch-Site": "same-origin"}, token)
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, method)
		assert.Equal(t, http.MethodPost, resp.Header.Get("Allow"), method)
		assert.Empty(t, resp.Cookies(), "%s: the session is kept", method)
	}
	for name, resp := range map[string]*http.Response{
		"no Origin": do(http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin"}, token),
		"no token":  do(http.MethodPost, map[string]string{"Origin": srv.URL}, url.Values{}),
	} {
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, name)
		assert.Empty(t, resp.Cookies(), "%s: the session is kept", name)
	}

	resp := do(http.MethodPost, map[string]string{"Origin": srv.URL, "Sec-Fetch-Site": "same-origin"}, token)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "Signed out")
	assert.Contains(t, string(body), `href="/auth/signin"`)
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-session" {
			cleared = c.MaxAge < 0 && c.Value == "" && c.Secure && c.HttpOnly && c.Path == "/"
		}
	}
	assert.True(t, cleared, "the session cookie is cleared")

	// Through the browser: the served form signs out, and the jar no longer
	// holds a session, so the next page sends to sign-in.
	b := webtest.NewBrowser(t, srv.Client())
	b.SetCookie(srv.URL, signedIn)
	page := b.Get(srv.URL + "/form")
	out := b.Submit(page, "/auth/signout", nil)
	assert.Equal(t, http.StatusOK, out.Resp.StatusCode)
	assert.Empty(t, out.Redirects, "sign-out never redirects")
	assert.Contains(t, string(out.Body), "Signed out")
	assert.Empty(t, b.Cookies(srv.URL), "the jar holds no session")
}
