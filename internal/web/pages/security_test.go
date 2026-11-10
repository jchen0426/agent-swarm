package pages_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/pages"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// No POST is ever answered with a redirect (I8; n5): a redirect after a
// form's POST that leads to /auth/* or to the provider is blocked by
// form-action, and would replay nothing. For every POST route registered,
// /auth/signout included, an expired session, a missing one and an admin's
// lost freshness each answer 200 or 4xx, with a link to sign-in or to fresh
// authentication.
func TestAPOSTIsNeverAnsweredWithARedirectOffOrigin(t *testing.T) {
	w := newWorld(t)
	w.coord.open("a1", "sso:carol")
	routes := pages.PostRoutes()
	require.Contains(t, routes, "/auth/signout")
	require.Contains(t, routes, "/items/{id}/{act}")
	require.Contains(t, routes, "/kill-switch")
	require.Contains(t, routes, "/items/new")
	expired := w.session(t, "erin", 9*time.Hour, -time.Minute, auth.Admin)
	stale := w.session(t, "erin", 10*time.Minute, time.Hour, auth.Admin)
	for _, route := range routes {
		path := strings.NewReplacer("{id}", "a1", "{act}", "close").Replace(route)
		for name, c := range map[string]*http.Cookie{"expired": expired, "missing": nil, "lost freshness": stale} {
			form := url.Values{"reason": {"resolved"}}
			switch {
			case c != nil && route == "/items/new":
				// An opening naming another principal, which an admin whose
				// freshness lapsed is refused.
				form = url.Values{"type": {"incident"}, "title": {"t"}, "principal": {"sso:bob"}, "k": {"AAAAAAAAAAAAAAAAAAAAAA"}, auth.TokenField: {w.token(t, c)}}
				form.Set("mac", w.keys.FormMAC("sso:erin", "open", "open", "AAAAAAAAAAAAAAAAAAAAAA"))
			case c != nil && route == "/kill-switch":
				// An off, which an admin whose freshness lapsed is refused.
				form = url.Values{"switch": {"off"}, "after": {"0"}, auth.TokenField: {w.token(t, c)}}
			case c != nil:
				form = withReason(w.form("sso:erin", app.ActClose, "a1", "AAAAAAAAAAAAAAAAAAAAAA", w.token(t, c)))
			}
			resp, body := w.post(t, path, c, form)
			assert.False(t, resp.StatusCode >= 300 && resp.StatusCode < 400, "%s, %s: %d to %q", route, name, resp.StatusCode, resp.Header.Get("Location"))
			assert.Less(t, resp.StatusCode, 500, "%s, %s", route, name)
			assert.Empty(t, resp.Header.Get("Location"), "%s, %s", route, name)
			got := links(t, []byte(body))
			signIn := false
			for _, l := range got {
				signIn = signIn || l == auth.SignInPath || strings.HasPrefix(l, auth.FreshPath+"?")
			}
			assert.True(t, signIn, "%s, %s: a link to sign in or to re-authenticate: %v", route, name, got)
		}
	}
	assert.Empty(t, w.coord.records())
	for _, k := range w.coord.switchCommits() {
		assert.False(t, !k.on && k.actor.Admin, "an off was asked only as no admin, and refused")
	}
}

// Every page served carries Referrer-Policy: same-origin, and none sets
// no-referrer otherwise, by a meta tag or a form's or link's attribute, so
// that every form's POST carries the origin (N1; I8). The callback alone
// answers no-referrer, and is not a page.
func TestNoServedPageSetsANoReferrerPolicyBesidesTheCallback(t *testing.T) {
	for path, body := range servedPages(t) {
		assert.NotRegexp(t, `(?i)<meta[^>]+name=["']?referrer`, body, path)
		assert.NotRegexp(t, `(?i)referrerpolicy`, body, path)
		assert.NotRegexp(t, `(?i)noreferrer`, body, path)
	}
}

// Every page works without JavaScript: no script, no event handler, no
// javascript: URL, no inline style the policy would block; every act is a
// native form or link; and every state and the switch's are words, not a
// colour alone. The stylesheet has 44 CSS px touch targets, a dark theme,
// and sizes in rem, so that 200% text holds (D10; M16).
func TestEveryPageWorksWithoutJavaScriptAndEveryStatusHasText(t *testing.T) {
	for path, body := range servedPages(t) {
		doc, err := html.Parse(strings.NewReader(body))
		require.NoError(t, err)
		for n := range doc.Descendants() {
			if n.Type != html.ElementNode {
				continue
			}
			assert.NotEqual(t, "script", n.Data, path)
			for _, a := range n.Attr {
				assert.False(t, strings.HasPrefix(strings.ToLower(a.Key), "on"), "%s: %s has %s", path, n.Data, a.Key)
				assert.NotEqual(t, "style", strings.ToLower(a.Key), "%s: an inline style", path)
				assert.False(t, strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.Val)), "javascript:"), path)
			}
			if n.Data == "button" {
				assert.NotEmpty(t, strings.TrimSpace(nodeText(n)), "%s: a button has words", path)
			}
		}
		assert.Contains(t, body, `<link rel="stylesheet" href="/static/app.css">`, path)
		assert.Contains(t, body, `<meta name="viewport" content="width=device-width, initial-scale=1">`, path)
	}

	w := newWorld(t)
	resp, css := w.get(t, "/static/app.css", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/css; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Contains(t, css, "min-height: 44px")
	assert.Contains(t, css, "min-width: 44px")
	assert.Contains(t, css, "@media (prefers-color-scheme: dark)")
	assert.NotRegexp(t, `font-size:\s*\d+px`, css, "text sizes scale with the reader's")
}

// Every refusal code, the coordinator's and the routing's, has the web's own
// words, and none is the admin API's text, which carries the API's
// instructions (m4; R81).
func TestEveryRefusalCodeHasWebWords(t *testing.T) {
	codes := constants(t, filepath.Join("..", "..", "work", "app"), "Code")
	codes = append(codes, constants(t, filepath.Join("..", "..", "admin", "switching"), "Code")...)
	require.Contains(t, codes, app.CodeNotItemResponder)
	require.Contains(t, codes, switching.CodeTakingOver)
	for _, code := range codes {
		words, ok := pages.Words(code)
		if assert.True(t, ok, "code %s has web words", code) {
			assert.NotEmpty(t, words, code)
			admin, has := switching.RefusedText(code)
			assert.False(t, has && admin == words, "code %s is not the admin API's text", code)
			assert.NotContains(t, words, "Idempotency-Key", code)
			assert.NotContains(t, words, "after", code)
		}
	}
	_, ok := pages.Words("no_such_code")
	assert.False(t, ok)
}

// constants are the string values of the package-level constants in dir
// whose names start with prefix, the tests' files left out.
func constants(t *testing.T, dir, prefix string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			g, ok := decl.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				v := spec.(*ast.ValueSpec)
				for i, name := range v.Names {
					if !strings.HasPrefix(name.Name, prefix) || i >= len(v.Values) {
						continue
					}
					if lit, ok := v.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						s, err := strconv.Unquote(lit.Value)
						require.NoError(t, err)
						out = append(out, s)
					}
				}
			}
		}
	}
	return out
}

// servedPages are the bodies of every page the interface serves, by a name:
// the items list, each act's form, an answer, a refusal with the fresh link,
// a standby's list, a refusal of a POST, the kill switch's control, answer,
// refused POST and standby, and a raw output, with its stored form, and its
// refusal. Each is checked to carry Referrer-Policy: same-origin.
func servedPages(t *testing.T) map[string]string {
	t.Helper()
	w := newWorld(t)
	w.coord.open("a1", "sso:alice")
	w.coord.open("a2", "sso:carol")
	w.views.rows = []app.Row{
		{ID: "a1", Title: "Disk full", Incident: "inc-a", Principal: "sso:alice", State: "investigating", PositionKnown: true},
		{ID: "a2", Title: "Queue", Incident: "inc-b", Principal: "sso:carol", State: "queued", PositionKnown: true, QueuePosition: 1},
		{ID: "a3", Title: "Old", Incident: "inc-c", Principal: "sso:alice", State: "closed", PositionKnown: true,
			ClosedAt: time.Now(), ReopenableUntil: time.Now().Add(time.Hour)},
		{ID: "a4", Title: "Waiting", Incident: "inc-c", Principal: "sso:alice", State: "awaiting_close", PositionKnown: true, AutoClosePaused: true},
		{ID: "a5", HeldByLaterBuild: true},
	}
	out := map[string]string{}
	keep := func(name string, resp *http.Response, body []byte) {
		t.Helper()
		assert.Equal(t, "same-origin", resp.Header.Get("Referrer-Policy"), name)
		out[name] = string(body)
	}
	b := w.browser(t, "alice", auth.Responder)
	page := b.Get(w.public + "/")
	require.Equal(t, http.StatusOK, page.Resp.StatusCode)
	words := text(t, page.Body)
	for _, label := range []string{"Investigating", "Queued", "Closed", "Awaiting close", "held by a later build", "auto-close paused"} {
		assert.Contains(t, words, label)
	}
	keep("items", page.Resp, page.Body)
	for _, kind := range []string{"promote", "close", "reopen"} {
		form := b.Get(w.public + "/items/a1/" + kind)
		keep(kind+" form", form.Resp, form.Body)
	}
	form := b.Get(w.public + "/items/a1/close")
	answer := b.Submit(form, "/items/a1/close", url.Values{"reason": {"resolved"}})
	keep("an answer", answer.Resp, answer.Body)
	stale := w.session(t, "erin", 10*time.Minute, time.Hour, auth.Admin)
	resp, body := w.get(t, "/items/a2/close", stale)
	keep("a refusal", resp, []byte(body))
	resp, body = w.post(t, "/items/a1/close", w.session(t, "alice", 0, time.Hour, auth.Responder), url.Values{})
	keep("a refused POST", resp, []byte(body))
	control := b.Get(w.public + "/kill-switch")
	keep("the kill switch's control", control.Resp, control.Body)
	switched := b.Submit(control, "/kill-switch", url.Values{"reason": {"security"}})
	keep("the kill switch's answer", switched.Resp, switched.Body)
	resp, body = w.post(t, "/kill-switch", w.session(t, "alice", 0, time.Hour, auth.Responder), url.Values{})
	keep("a refused POST to the kill switch", resp, []byte(body))
	read := citing("a1", map[string][]string{"call-1": {`"m":"disk`}})
	read.Board.Opened.Principal = "sso:alice"
	w.boards.set("a1", read)
	w.calls.set(readCall("call-1", outputRun, `{"q":"disk"}`, `{"m":"disk full"}`))
	raw := b.Get(w.public + outputPath("a1", "call-1"))
	require.Equal(t, http.StatusOK, raw.Resp.StatusCode)
	keep("a raw output, with its stored form", raw.Resp, raw.Body)
	raw = b.Get(w.public + outputPath("a1", "call-9"))
	require.Equal(t, http.StatusNotFound, raw.Resp.StatusCode)
	keep("a raw output's refusal", raw.Resp, raw.Body)
	w.leader.set(false, false)
	page = b.Get(w.public + "/items")
	keep("a standby's items", page.Resp, page.Body)
	page = b.Get(w.public + "/kill-switch")
	keep("a standby's kill switch", page.Resp, page.Body)
	return out
}

// nodeText is the text under n.
func nodeText(n *html.Node) string {
	var b bytes.Buffer
	for c := range n.Descendants() {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}
