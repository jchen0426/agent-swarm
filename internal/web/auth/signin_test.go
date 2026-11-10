package auth_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/webtest"
)

// clientSecretRef and clientSecret are the OIDC client's secret, as the
// secret store holds it.
const (
	clientSecretRef = "secret://web/client"
	clientSecret    = "the-client-secret"
)

// site is the authenticator served over TLS at its public URL, beside a
// fake IdP whose clock is the world's, both under one test CA.
type site struct {
	*world
	ca  *webtest.CA
	idp *webtest.IDP
	url string
	// forms are the methods the form page /items/x/close was asked with.
	forms *lines
}

// whoami is what the site's pages answer a signed-in person: the session
// and the actor it acts as now.
type whoami struct {
	Subject   string      `json:"subject"`
	Roles     []auth.Role `json:"roles"`
	AuthTime  time.Time   `json:"authTime"`
	Actor     string      `json:"actor"`
	Responder bool        `json:"responder"`
	Admin     bool        `json:"admin"`
	Method    string      `json:"method"`
}

func newSite(t *testing.T, change func(w *deploy.Web)) *site {
	t.Helper()
	ca := webtest.NewCA(t)
	srv := ca.UnstartedServer(t, nil)
	u := "https://" + srv.Listener.Addr().String()
	idp := webtest.NewIDP(t, ca, webtest.Client{ID: clientID, Secret: clientSecret, RedirectURL: u + "/auth/callback"})
	bundle := ca.File(t)
	w := newWorld(t, func(b *deploy.Web) {
		b.PublicURL = u
		b.OIDC.Issuer = idp.URL
		b.OIDC.ClientSecretRef = clientSecretRef
		b.OIDC.CABundle = bundle
		if change != nil {
			change(b)
		}
	})
	w.store.mu.Lock()
	w.store.others = map[string]string{clientSecretRef: clientSecret + "\n"}
	w.store.mu.Unlock()
	idp.SetClock(w.clock.Now)
	s := &site{world: w, ca: ca, idp: idp, url: u, forms: &lines{}}

	me := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		signed, ok := auth.From(r.Context())
		if !ok {
			http.Error(rw, "no session", http.StatusInternalServerError)
			return
		}
		actor := w.a.Actor(signed.Session, w.clock.Now())
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(whoami{
			Subject: signed.Session.Subject, Roles: signed.Session.Roles, AuthTime: signed.Session.AuthTime,
			Actor: actor.Subject, Responder: actor.Responder, Admin: actor.Admin, Method: r.Method,
		})
	})
	mux := http.NewServeMux()
	mux.Handle("/auth/signin", w.a.SignIn())
	mux.Handle("/auth/fresh", w.a.Fresh())
	mux.Handle("/auth/callback", w.a.Callback())
	mux.Handle("/items/x/close", w.a.SignedIn(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = s.forms.Write([]byte(`{"msg":"` + r.Method + `"}` + "\n"))
		me.ServeHTTP(rw, r)
	})))
	mux.Handle("/", w.a.SignedIn(me))
	srv.Config.Handler = mux
	srv.StartTLS()
	return s
}

// browser is a browser that trusts the site's CA.
func (s *site) browser(t *testing.T) *webtest.Browser {
	t.Helper()
	return webtest.NewBrowser(t, s.ca.Client())
}

// who decodes the page a signed-in person was answered.
func who(t *testing.T, p *webtest.Page) whoami {
	t.Helper()
	require.Equal(t, http.StatusOK, p.Resp.StatusCode, string(p.Body))
	var w whoami
	require.NoError(t, json.Unmarshal(p.Body, &w), string(p.Body))
	return w
}

// groups are the claims of alice in the given groups.
func groups(g ...string) map[string]any {
	return map[string]any{"sub": "alice", "groups": g}
}

// sessionCookie is the browser's session cookie for the site, or nil.
func (s *site) sessionCookie(b *webtest.Browser) *http.Cookie {
	for _, c := range b.Cookies(s.url) {
		if c.Name == auth.CookieName {
			return c
		}
	}
	return nil
}

// flowCookies are the browser's flow cookies for the site.
func flowCookies(cs []*http.Cookie) []*http.Cookie {
	var out []*http.Cookie
	for _, c := range cs {
		if strings.HasPrefix(c.Name, "__Host-flow-") {
			out = append(out, c)
		}
	}
	return out
}

// requireNoSecretLogged fails if the log holds any secret the IdP gave out
// or received, or any cookie's value.
func (s *site) requireNoSecretLogged(t *testing.T, cookies ...*http.Cookie) {
	t.Helper()
	out := s.log.String()
	for _, secret := range s.idp.Issued() {
		if secret != "" {
			require.NotContains(t, out, secret)
		}
	}
	for _, c := range cookies {
		if c != nil && c.Value != "" {
			require.NotContains(t, out, c.Value)
		}
	}
	require.NotContains(t, out, clientSecret)
}

// agent is a client that follows no redirect, so that a test takes each
// step of a flow itself, with a jar of its own.
type agent struct {
	t *testing.T
	c *http.Client
}

func (s *site) agent(t *testing.T) *agent {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	c := s.ca.Client()
	c.Jar = jar
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &agent{t: t, c: c}
}

// get GETs rawURL and gives the response, its body read and closed.
func (a *agent) get(rawURL string) (resp *http.Response, body string) {
	a.t.Helper()
	req, err := http.NewRequestWithContext(a.t.Context(), http.MethodGet, rawURL, http.NoBody)
	require.NoError(a.t, err)
	resp, err = a.c.Do(req)
	require.NoError(a.t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(a.t, err)
	return resp, string(raw)
}

// start begins a sign-in that returns to back, and gives the IdP's
// authorization URL it was sent to.
func (a *agent) start(s *site, path, back string) string {
	a.t.Helper()
	resp, _ := a.get(s.url + path + "?return=" + url.QueryEscape(back))
	require.Equal(a.t, http.StatusSeeOther, resp.StatusCode)
	loc := resp.Header.Get("Location")
	require.True(a.t, strings.HasPrefix(loc, s.idp.URL+"/authorize?"), loc)
	return loc
}

// finish takes an authorization URL to the IdP, and its redirect to the
// callback, and gives the callback's answer.
func (a *agent) finish(authorize string) (resp *http.Response, body string) {
	a.t.Helper()
	resp, _ = a.get(authorize)
	require.Equal(a.t, http.StatusFound, resp.StatusCode)
	return a.get(resp.Header.Get("Location"))
}

func (a *agent) cookies(rawURL string) []*http.Cookie {
	u, err := url.Parse(rawURL)
	require.NoError(a.t, err)
	return a.c.Jar.Cookies(u)
}

// A person who opens a deep link signs in at the IdP and comes back to
// it, with a session holding their subject and roles; the flow is the
// code flow with PKCE, state, nonce and response_mode=query; nothing
// secret is logged.
func TestSignInSetsASessionAndReturnsToTheDeepLink(t *testing.T) {
	s := newSite(t, nil)
	s.idp.SetClaims(groups("responders", "unrelated"))
	b := s.browser(t)

	p := b.Get(s.url + "/items/abc?tab=history")
	require.Equal(t, "/items/abc", p.Resp.Request.URL.Path)
	assert.Equal(t, "tab=history", p.Resp.Request.URL.RawQuery)
	me := who(t, p)
	assert.Equal(t, "alice", me.Subject)
	assert.Equal(t, []auth.Role{auth.Responder}, me.Roles)
	assert.Equal(t, "sso:alice", me.Actor)
	assert.Equal(t, s.clock.Now(), me.AuthTime)
	require.NotNil(t, s.sessionCookie(b))

	q := s.idp.Authorizations()
	require.Len(t, q, 1)
	assert.Equal(t, "code", q[0].Get("response_type"))
	assert.Equal(t, "query", q[0].Get("response_mode"))
	assert.Equal(t, "S256", q[0].Get("code_challenge_method"))
	assert.NotEmpty(t, q[0].Get("state"))
	assert.NotEmpty(t, q[0].Get("nonce"))
	assert.Equal(t, "openid profile email", q[0].Get("scope"))
	assert.Empty(t, q[0].Get("prompt"), "a plain sign-in asks for no new authentication")
	assert.NotContains(t, q[0].Encode(), "items", "the return path stays in the flow cookie, never in a query")
	assert.Empty(t, flowCookies(b.Cookies(s.url)), "the flow cookie is deleted at the callback")
	s.requireNoSecretLogged(t, s.sessionCookie(b))
}

// Two tabs that sign in at once each keep their own flow, and each comes
// back to its own page.
func TestTwoTabsSigningInDoNotClobberEachOther(t *testing.T) {
	s := newSite(t, nil)
	s.idp.SetClaims(groups("viewers"))
	a := s.agent(t)

	first := a.start(s, auth.SignInPath, "/items/one")
	second := a.start(s, auth.SignInPath, "/items/two")
	require.Len(t, flowCookies(a.cookies(s.url)), 2)

	resp, body := a.finish(second)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	assert.Equal(t, "/items/two", resp.Header.Get("Location"))
	resp, body = a.finish(first)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	assert.Equal(t, "/items/one", resp.Header.Get("Location"))
	assert.Empty(t, flowCookies(a.cookies(s.url)))
}

// At most five flows live at once, the oldest dropped; a flow's cookie is
// deleted at its callback, and lasts ten minutes (m21).
func TestFlowCookiesAreCappedAndDeletedAtTheCallback(t *testing.T) {
	s := newSite(t, nil)
	s.idp.SetClaims(groups("viewers"))
	a := s.agent(t)

	var flows []string
	for i := range 7 {
		flows = append(flows, a.start(s, auth.SignInPath, "/items/"+string(rune('a'+i))))
		s.clock.advance(time.Second)
	}
	require.Len(t, flowCookies(a.cookies(s.url)), 5, "the cap holds five")

	resp, body := a.finish(flows[0])
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "the oldest flow was dropped")
	assert.Contains(t, body, "Sign in again")
	resp, _ = a.finish(flows[1])
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "so was the second oldest")

	resp, body = a.finish(flows[6])
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	assert.Equal(t, "/items/g", resp.Header.Get("Location"))
	require.Len(t, flowCookies(a.cookies(s.url)), 4, "the callback deletes its flow's cookie")

	s.clock.advance(10*time.Minute + time.Second)
	resp, _ = a.finish(flows[5])
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a flow lasts ten minutes")
}

func sessionOf(cs []*http.Cookie) *http.Cookie {
	for _, c := range cs {
		if c.Name == auth.CookieName {
			return c
		}
	}
	return nil
}

// A flow cookie is sealed under a key and a binding of its own (m8): a
// session cookie's value planted as a flow cookie opens no flow, and a
// flow cookie's value planted as the session cookie opens no session.
func TestFlowAndSessionCookiesDoNotOpenAsEachOther(t *testing.T) {
	s := newSite(t, nil)
	s.idp.SetClaims(groups("viewers"))

	a := s.agent(t)
	authorize := a.start(s, auth.SignInPath, "/items/x")
	flows := flowCookies(a.cookies(s.url))
	require.Len(t, flows, 1)
	flow := flows[0]

	session := webtest.SignedIn(t, s.keys(t), []auth.Role{auth.Admin}, "alice")
	a.c.Jar.SetCookies(mustURL(t, s.url), []*http.Cookie{{Name: flow.Name, Value: session.Value, Path: "/", Secure: true}})
	resp, _ := a.finish(authorize)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a session cookie's value is no flow")
	assert.Nil(t, sessionOf(a.cookies(s.url)))

	other := s.agent(t)
	other.start(s, auth.SignInPath, "/items/x")
	value := flowCookies(other.cookies(s.url))[0].Value
	b := s.agent(t)
	b.c.Jar.SetCookies(mustURL(t, s.url), []*http.Cookie{{Name: auth.CookieName, Value: value, Path: "/", Secure: true}})
	resp, _ = b.get(s.url + "/items/x")
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode, "a flow cookie's value is no session")
	assert.True(t, strings.HasPrefix(resp.Header.Get("Location"), auth.SignInPath))
}

// httptestGet is a GET of target at the default public URL.
func httptestGet(target string) *http.Request {
	return httptest.NewRequest(http.MethodGet, publicURL+target, http.NoBody)
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

// The authenticator is made, and its pages served, while the IdP is down:
// discovery is lazy, and retried once the IdP is back (M4).
func TestServeStartsWhileTheIdPIsDown(t *testing.T) {
	ca := webtest.NewCA(t)
	idp := webtest.NewIDP(t, ca, webtest.Client{ID: clientID, Secret: clientSecret, RedirectURL: "https://agents.example.com/auth/callback"})
	idp.SetDown(true)
	w := newWorld(t, func(b *deploy.Web) {
		b.OIDC.Issuer = idp.URL
		b.OIDC.CABundle = ca.File(t)
	})
	idp.SetClock(w.clock.Now)

	page := w.a.SignedIn(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusOK) }))
	req := httptestGet("/items")
	k, err := w.a.Keys(t.Context())
	require.NoError(t, err)
	signed := w.session(auth.Viewer)
	signed.Issuer = idp.URL
	req.AddCookie(cookie(t, k, signed))
	assert.Equal(t, http.StatusOK, serve(page, req).Code, "a signed-in page needs no IdP")

	rec := serve(w.a.SignIn(), httptestGet(auth.SignInPath+"?return=/items"))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "identity provider")

	idp.SetDown(false)
	w.clock.advance(time.Minute)
	rec = serve(w.a.SignIn(), httptestGet(auth.SignInPath+"?return=/items"))
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.True(t, strings.HasPrefix(rec.Header().Get("Location"), idp.URL+"/authorize?"))
}

// Tokens the IdP should not have issued, or that an attacker made, are
// refused at the callback, with no session set.
func TestRefusedTokens(t *testing.T) {
	cases := map[string]func(s *site){
		"a wrong issuer":   func(s *site) { s.idp.SetClaims(withGroups(map[string]any{"iss": "https://evil.example.com"})) },
		"a wrong audience": func(s *site) { s.idp.SetClaims(withGroups(map[string]any{"aud": "someone-else"})) },
		"a bad nonce":      func(s *site) { s.idp.SetClaims(withGroups(map[string]any{"nonce": "not-the-flows"})) },
		"an expired token": func(s *site) {
			now := s.clock.Now()
			s.idp.SetClaims(withGroups(map[string]any{"iat": now.Add(-time.Hour).Unix(), "exp": now.Add(-time.Minute).Unix()}))
		},
		"alg none": func(s *site) { s.idp.SetClaims(groups("viewers")); s.idp.SetSigning(webtest.SignNone) },
		"HS256 signed with the public key": func(s *site) {
			s.idp.SetClaims(groups("viewers"))
			s.idp.SetSigning(webtest.SignHS256WithPublicKey)
		},
		"several aud values with a wrong azp": func(s *site) {
			s.idp.SetClaims(withGroups(map[string]any{"aud": []string{clientID, "other"}, "azp": "other"}))
		},
		"several aud values with no azp": func(s *site) {
			s.idp.SetClaims(withGroups(map[string]any{"aud": []string{clientID, "other"}}))
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSite(t, nil)
			set(s)
			a := s.agent(t)
			resp, body := a.finish(a.start(s, auth.SignInPath, "/items"))
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, body)
			assert.Contains(t, body, "Sign-in failed")
			assert.Nil(t, sessionOf(a.cookies(s.url)))
			assert.Contains(t, s.log.String(), "sign-in refused")
			s.requireNoSecretLogged(t)
		})
	}
	t.Run("several aud values with the client's azp are accepted", func(t *testing.T) {
		s := newSite(t, nil)
		s.idp.SetClaims(withGroups(map[string]any{"aud": []string{clientID, "other"}, "azp": clientID}))
		a := s.agent(t)
		resp, body := a.finish(a.start(s, auth.SignInPath, "/items"))
		assert.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	})
}

// withGroups is claims with alice in the viewers group.
func withGroups(claims map[string]any) map[string]any {
	out := groups("viewers")
	for k, v := range claims {
		out[k] = v
	}
	return out
}

// A token with no groups claim, an overage marker naming it, or none of
// the deployment's groups lands on "no role", with words naming the cause,
// and no session. Groups match as given, object ids included (R89).
func TestNoGroupsOrAnOverageLandsOnNoRole(t *testing.T) {
	cases := map[string]struct {
		claims map[string]any
		words  string
	}{
		"no groups claim": {map[string]any{"sub": "alice"}, "carries no groups claim"},
		"an overage": {map[string]any{
			"sub": "alice", "_claim_names": map[string]any{"groups": "src1"},
			"_claim_sources": map[string]any{"src1": map[string]any{"endpoint": "https://graph.example.com"}},
		}, "too many groups"},
		"an overage beside a groups claim": {map[string]any{
			"sub": "alice", "groups": []string{"viewers"}, "_claim_names": map[string]any{"groups": "src1"},
			"_claim_sources": map[string]any{"src1": map[string]any{"endpoint": "https://graph.example.com"}},
		}, "too many groups"},
		"none of the groups": {groups("Viewers", "others"), "none of this deployment"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSite(t, nil)
			s.idp.SetClaims(c.claims)
			a := s.agent(t)
			resp, body := a.finish(a.start(s, auth.SignInPath, "/items"))
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, body)
			assert.Contains(t, body, "No role")
			assert.Contains(t, body, c.words)
			assert.Nil(t, sessionOf(a.cookies(s.url)))
		})
	}
	t.Run("object ids match as given", func(t *testing.T) {
		s := newSite(t, func(w *deploy.Web) { w.Groups.Admins = "6f1c2a9e-0d3b-4c1f-9e7a-2b5d8c4f1a00" })
		s.idp.SetClaims(groups("6f1c2a9e-0d3b-4c1f-9e7a-2b5d8c4f1a00"))
		me := who(t, s.browser(t).Get(s.url+"/items"))
		assert.Equal(t, []auth.Role{auth.Admin}, me.Roles)
	})
}

// A subject is the subject claim's value, recorded as sso:<value>, and
// refused at sign-in when empty, over 256 characters, or holding a
// character a reader could miss (I9; R80).
func TestSubjectsAreRecordedAsSSOAndCheckedForUnseenCharacters(t *testing.T) {
	t.Run("recorded as sso", func(t *testing.T) {
		s := newSite(t, func(w *deploy.Web) { w.OIDC.SubjectClaim = "preferred_username" })
		s.idp.SetClaims(map[string]any{"sub": "opaque-123", "preferred_username": "alice.smith", "groups": []string{"viewers"}})
		me := who(t, s.browser(t).Get(s.url+"/items"))
		assert.Equal(t, "alice.smith", me.Subject)
		assert.Equal(t, "sso:alice.smith", me.Actor)
	})
	// A subject is held to the bound of an item's principal once prefixed
	// sso:, so that a person who signs in can always be one (m4).
	t.Run("252 characters are accepted, 256 once prefixed", func(t *testing.T) {
		s := newSite(t, nil)
		long := strings.Repeat("é", 252)
		s.idp.SetClaims(map[string]any{"sub": long, "groups": []string{"viewers"}})
		me := who(t, s.browser(t).Get(s.url+"/items"))
		assert.Equal(t, long, me.Subject)
		assert.Equal(t, 256, utf8.RuneCountInString(me.Actor))
	})
	refused := map[string]any{
		"an empty subject":       "",
		"253 characters":         strings.Repeat("é", 253),
		"a zero-width space":     "ali\u200bce",
		"a line feed":            "ali\nce",
		"a tab":                  "alice\t",
		"a right-to-left mark":   "alice\u200f",
		"a subject not a string": 42,
	}
	for name, sub := range refused {
		t.Run(name, func(t *testing.T) {
			s := newSite(t, func(w *deploy.Web) { w.OIDC.SubjectClaim = "email" })
			s.idp.SetClaims(map[string]any{"sub": "opaque-123", "email": sub, "groups": []string{"viewers"}})
			a := s.agent(t)
			resp, body := a.finish(a.start(s, auth.SignInPath, "/items"))
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, body)
			assert.Contains(t, body, "subject")
			assert.Nil(t, sessionOf(a.cookies(s.url)))
		})
	}
	t.Run("a missing subject claim", func(t *testing.T) {
		s := newSite(t, func(w *deploy.Web) { w.OIDC.SubjectClaim = "email" })
		s.idp.SetClaims(groups("viewers"))
		a := s.agent(t)
		resp, body := a.finish(a.start(s, auth.SignInPath, "/items"))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, body)
		assert.Contains(t, body, "subject")
	})
}

// A subject that begins or ends with white space, any Unicode white space
// at either end included, is refused at sign-in: Open trims an item's
// principals (R80), so it would sign in and then be refused as its own
// principal. White space within a subject is the subject's, and signs in.
func TestASubjectWithSurroundingWhiteSpaceIsRefusedAtSignIn(t *testing.T) {
	for name, sub := range map[string]string{
		"a space before":             " alice",
		"a space after":              "alice ",
		"a no-break space before":    "\u00a0alice",
		"an ideographic space after": "alice\u3000",
	} {
		t.Run(name, func(t *testing.T) {
			s := newSite(t, nil)
			s.idp.SetClaims(map[string]any{"sub": sub, "groups": []string{"responders"}})
			a := s.agent(t)
			resp, body := a.finish(a.start(s, auth.SignInPath, "/items"))
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, body)
			assert.Contains(t, body, "Your identity provider gave a subject this interface cannot record: it begins or ends with white space.")
			assert.Nil(t, sessionOf(a.cookies(s.url)), "no session is set")
			assert.Contains(t, s.log.String(), "the subject claim it begins or ends with white space")
		})
	}
	t.Run("white space within signs in", func(t *testing.T) {
		s := newSite(t, nil)
		s.idp.SetClaims(map[string]any{"sub": "al ice", "groups": []string{"responders"}})
		me := who(t, s.browser(t).Get(s.url+"/items"))
		assert.Equal(t, "al ice", me.Subject)
		assert.Equal(t, "sso:al ice", me.Actor)
	})
}

// A subject is never checked against the redaction rules: a deployment
// whose always rule redacts emails still signs people in by email (M-e;
// m20), though they cannot be named as principals.
func TestAnEmailSubjectSignsInUnderAnAlwaysRuleThatRedactsEmails(t *testing.T) {
	emails := redact.New(redact.Rule{
		Patterns: []*regexp.Regexp{regexp.MustCompile(`[a-z.]+@example\.com`)}, Replacement: "[email]",
	})
	require.NotEqual(t, "alice@example.com", emails.Text("alice@example.com"), "the rule redacts the subject")

	s := newSite(t, func(w *deploy.Web) { w.OIDC.SubjectClaim = "email" })
	s.idp.SetClaims(map[string]any{"sub": "opaque-123", "email": "alice@example.com", "groups": []string{"responders"}})
	me := who(t, s.browser(t).Get(s.url+"/items"))
	assert.Equal(t, "alice@example.com", me.Subject)
	assert.Equal(t, "sso:alice@example.com", me.Actor)
}

// The callback answers a bare 303 to the return path, with no body and
// Referrer-Policy: no-referrer (R87).
func TestTheCallbackAnswersABare303WithNoReferrer(t *testing.T) {
	s := newSite(t, nil)
	s.idp.SetClaims(groups("viewers"))
	a := s.agent(t)
	resp, body := a.finish(a.start(s, auth.SignInPath, "/items/x?y=1"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/items/x?y=1", resp.Header.Get("Location"))
	assert.Equal(t, "no-referrer", resp.Header.Get("Referrer-Policy"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Empty(t, body)
	assert.Empty(t, resp.Header.Get("Content-Type"))

	refused := s.agent(t)
	s.idp.SetClaims(map[string]any{"sub": "alice"})
	resp, _ = refused.finish(refused.start(s, auth.SignInPath, "/items"))
	assert.Equal(t, "no-referrer", resp.Header.Get("Referrer-Policy"), "a refusal at the callback leaks its URL to no link either")
}

// web.oidc.extraScopes are asked for after openid, profile and email.
func TestExtraScopesAreAskedForBesideTheDefaults(t *testing.T) {
	s := newSite(t, func(w *deploy.Web) { w.OIDC.ExtraScopes = []string{"groups"} })
	s.idp.SetClaims(groups("viewers"))
	who(t, s.browser(t).Get(s.url+"/items"))
	who(t, s.browser(t).Get(s.url+"/items"))
	q := s.idp.Authorizations()
	require.Len(t, q, 2)
	for _, one := range q {
		assert.Equal(t, "openid profile email groups", one.Get("scope"), "the defaults are never grown by the extra scopes")
	}
}

// A callback is good once: replayed after it succeeded, its flow cookie is
// gone and it is refused 400 with the session left as it was; with the old
// flow cookie planted again, the IdP refuses the used code and so does the
// callback, 401.
func TestAReplayedCallbackIsRefused(t *testing.T) {
	s := newSite(t, nil)
	s.idp.SetClaims(groups("viewers"))
	a := s.agent(t)
	authorize := a.start(s, auth.SignInPath, "/items")
	flow := flowCookies(a.cookies(s.url))[0]
	resp, _ := a.get(authorize)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	callback := resp.Header.Get("Location")

	resp, body := a.get(callback)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	require.Empty(t, flowCookies(a.cookies(s.url)))
	session := sessionOf(a.cookies(s.url))
	require.NotNil(t, session)

	s.clock.advance(time.Minute)
	resp, body = a.get(callback)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, body, "Sign in again")
	assert.Equal(t, session.Value, sessionOf(a.cookies(s.url)).Value, "the session is not issued again")

	a.c.Jar.SetCookies(mustURL(t, s.url), []*http.Cookie{{Name: flow.Name, Value: flow.Value, Path: "/", Secure: true}})
	resp, body = a.get(callback)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, body, "Sign-in failed")
	assert.Equal(t, session.Value, sessionOf(a.cookies(s.url)).Value)
	s.requireNoSecretLogged(t, session, flow)
}

// A provider that hangs is asked once at a time: a sign-in that comes
// while discovery is in flight waits on its own request alone, and none
// asks again within the retry delay of the failure's end, not its start.
// The failure is logged with its status, never the provider's body.
func TestAHungIdPIsAskedOnceAndBacksOff(t *testing.T) {
	ca := webtest.NewCA(t)
	idp := webtest.NewIDP(t, ca, webtest.Client{ID: clientID, Secret: clientSecret, RedirectURL: "https://agents.example.com/auth/callback"})
	w := newWorld(t, func(b *deploy.Web) {
		b.OIDC.Issuer = idp.URL
		b.OIDC.CABundle = ca.File(t)
	})
	idp.SetClock(w.clock.Now)
	idp.SetDown(true)
	entered, release := make(chan struct{}, 4), make(chan struct{})
	idp.OnDiscovery(func() {
		entered <- struct{}{}
		<-release
		w.clock.advance(9 * time.Second) // the call took nine seconds
	})

	first := make(chan int, 1)
	go func() { first <- serve(w.a.SignIn(), httptestGet(auth.SignInPath)).Code }()
	<-entered

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	rec := serve(w.a.SignIn(), httptestGet(auth.SignInPath).WithContext(ctx))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Less(t, time.Since(began), 2*time.Second, "a waiter gives up with its own request")

	close(release)
	assert.Equal(t, http.StatusServiceUnavailable, <-first)
	assert.Equal(t, 1, idp.Discoveries(), "one discovery for both")

	idp.OnDiscovery(nil)
	idp.SetDown(false)
	w.clock.advance(4 * time.Second)
	assert.Equal(t, http.StatusServiceUnavailable, serve(w.a.SignIn(), httptestGet(auth.SignInPath)).Code)
	assert.Equal(t, 1, idp.Discoveries(), "within the retry delay of the failure's end, the provider is not asked")

	w.clock.advance(2 * time.Second)
	assert.Equal(t, http.StatusSeeOther, serve(w.a.SignIn(), httptestGet(auth.SignInPath)).Code)
	assert.Equal(t, 2, idp.Discoveries())

	assert.NotContains(t, w.log.String(), webtest.DownBody)
	assert.Contains(t, w.log.String(), `"status":503`)
}

// The client's secret goes in the Authorization header alone
// (client_secret_basic): never a second time in the form, even when the
// provider refuses the header.
func TestTheClientSecretIsSentOnceInTheHeader(t *testing.T) {
	s := newSite(t, nil)
	s.idp.SetClaims(groups("viewers"))
	a := s.agent(t)
	resp, body := a.finish(a.start(s, auth.SignInPath, "/items"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	assert.Equal(t, []webtest.TokenRequest{{Basic: true}}, s.idp.TokenRequests())

	s.idp.RefuseBasic(true)
	b := s.agent(t)
	resp, _ = b.finish(b.start(s, auth.SignInPath, "/items"))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, []webtest.TokenRequest{{Basic: true}, {Basic: true}}, s.idp.TokenRequests(), "no retry with the secret in the form")
}
