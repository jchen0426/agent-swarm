package pages_test

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	"github.com/jchen0426/agent-swarm/internal/work/board"
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
// refused POST and standby, a raw output, with its stored form, and its
// refusal, and a board, its member's, one whose feed holds every line its
// tables give (see everyLine), one the seam hides and a standby's. Each is
// checked to carry Referrer-Policy: same-origin.
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
	w.boards.set("b1", richRead("b1", ""))
	shown := w.browser(t, "carol", auth.Responder).Get(w.public + "/items/b1")
	require.Equal(t, http.StatusOK, shown.Resp.StatusCode)
	keep("a board, its member's", shown.Resp, shown.Body)
	fed, _ := everyLine()
	w.boards.set(fed.id, fed.read())
	shown = w.browser(t, "carol", auth.Responder).Get(w.public + "/items/" + string(fed.id) + "?feed=all")
	require.Equal(t, http.StatusOK, shown.Resp.StatusCode)
	keep("a board's feed of every line", shown.Resp, shown.Body)
	restore := pages.SetVisible(func(*app.View) bool { return false })
	shown = b.Get(w.public + "/items/b1")
	restore()
	require.Equal(t, http.StatusOK, shown.Resp.StatusCode)
	keep("a board the seam hides", shown.Resp, shown.Body)
	w.leader.set(false, false)
	page = b.Get(w.public + "/items")
	keep("a standby's items", page.Resp, page.Body)
	page = b.Get(w.public + "/kill-switch")
	keep("a standby's kill switch", page.Resp, page.Body)
	page = b.Get(w.public + "/items/b1")
	keep("a standby's board", page.Resp, page.Body)
	return out
}

// inertAction is a template action that calls inertText or inertLine, in
// any form.
var inertAction = regexp.MustCompile(`\{\{[^{}]*\binert(Text|Line)\b[^{}]*\}\}`)

// inertCalls counts the actions of a template's text that call inertText or
// inertLine, and returns those not written <bdi>{{inertText .X}}</bdi> or
// <bdi>{{inertLine .X}}</bdi>, the call alone inside its own <bdi>.
func inertCalls(s string) (n int, outside []string) {
	for _, at := range inertAction.FindAllStringIndex(s, -1) {
		n++
		call := s[at[0]:at[1]]
		alone := strings.HasPrefix(call, "{{inertText ") || strings.HasPrefix(call, "{{inertLine ")
		if !alone || !strings.HasSuffix(s[:at[0]], "<bdi>") || !strings.HasPrefix(s[at[1]:], "</bdi>") {
			outside = append(outside, call)
		}
	}
	return n, outside
}

// hostilePages are the bodies of the pages that show stored text, by a
// name, each of whose stored texts holds s: the items list, with the
// layout's switch, signed-in subject and standby notice; an act's form, with
// its fixed reason after an unknown outcome; an act's, the opening form's,
// the kill switch's page's and its POST's refusals on a standby, each naming
// the leader; a board, its member's; and a raw output.
func hostilePages(t *testing.T, s string) map[string]string {
	t.Helper()
	w := newWorld(t)
	out := map[string]string{}
	w.coord.open("a1", "sso:alice")
	w.views.rows = []app.Row{{
		ID: "a1", Title: "Disk full " + s, Incident: "inc-a", Service: "checkout" + s, Principal: "sso:alice" + s,
		Responders: []string{"sso:erin" + s}, State: "investigating", PositionKnown: true,
	}}
	w.views.setSwitch(&app.KillSwitch{On: true, Seq: 2, OnSeq: 2, By: "sso:bob" + s, Since: time.Now(), Reason: board.Reason{Code: "other", Text: "spend " + s}})
	alice := w.browser(t, "alice", auth.Responder)
	out["the items list, its layout"] = string(w.browser(t, "eve"+s, auth.Responder).Get(w.public + "/items").Body)

	w.coord.queue(false, errors.New("down"))
	form := alice.Get(w.public + "/items/a1/close")
	retry := alice.Submit(form, "/items/a1/close", url.Values{"reason": {"other"}, "text": {"the disk " + s}})
	require.Equal(t, http.StatusServiceUnavailable, retry.Resp.StatusCode)
	out["an act's form, its reason fixed"] = string(retry.Body)

	w.boards.set("b1", richRead("b1", s))
	out["a board, its member's"] = string(w.browser(t, "carol", auth.Responder).Get(w.public + "/items/b1").Body)
	w.boards.set("o1", citing("o1", map[string][]string{"call-1": {"x"}}))
	call := readCall("call-1", outputRun, `{"q":"`+strings.ReplaceAll(s, `"`, `\"`)+`"}`, `"x"`)
	call.Agent = "agent-a" + s
	w.calls.set(call)
	raw := w.browser(t, "carol", auth.Responder).Get(w.public + outputPath("o1", "call-1"))
	require.Equal(t, http.StatusOK, raw.Resp.StatusCode)
	out["a raw output"] = string(raw.Body)

	w.leader.set(false, false)
	w.holder.name = "instance-b" + s
	out["a standby's act"] = string(alice.Get(w.public + "/items/a1/close").Body)
	out["a standby's opening form"] = string(alice.Get(w.public + "/items/new").Body)
	out["a standby's kill switch"] = string(alice.Get(w.public + "/kill-switch").Body)
	c := w.session(t, "alice", 0, time.Hour, auth.Responder)
	resp, body := w.post(t, "/kill-switch", c, url.Values{"switch": {"off"}, "after": {"2"}, auth.TokenField: {w.token(t, c)}})
	require.Equal(t, http.StatusConflict, resp.StatusCode, body)
	out["a standby's refused POST to the kill switch"] = body
	return out
}

// Every call of the two display functions in the templates is written alone
// inside its own <bdi> (design D15; R154), as a check over the templates'
// files finds, and finds one written outside; and each page whose stored
// texts hold a script renders it escaped, as text.
func TestEveryInertCallSiteIsIsolatedAndEscaped(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("templates", "*.html"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	calls := 0
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // G304: the package's own templates, by a glob of its directory
		require.NoError(t, err)
		n, outside := inertCalls(string(b))
		calls += n
		assert.Empty(t, outside, "%s: a display function's call outside its own <bdi>", f)
	}
	assert.Greater(t, calls, 15, "the templates' calls were found")
	n, outside := inertCalls(`<p>{{inertText .X}}</p> <bdi>{{inertLine .Y}}</bdi> <bdi>{{.Z | inertLine}}</bdi> <bdi>a {{inertText .W}}</bdi>`)
	assert.Equal(t, 4, n)
	assert.Equal(t, []string{"{{inertText .X}}", "{{.Z | inertLine}}", "{{inertText .W}}"}, outside, "the check finds a call outside its <bdi>")

	const script = "<script>alert(1)</script>"
	for name, body := range hostilePages(t, script) {
		assert.NotContains(t, strings.ToLower(body), "<script", name)
		assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;", name)
	}
}

// The items list, the layout, and 9a-1's act, opening and kill switch pages
// show their stored texts through the display functions, each inside its
// <bdi> (R154; plan review M12(b)): a title, a service, a principal and a
// responder; the switch's By and its reason's text, the signed-in subject
// and the standby notice's leader; the leader's name on each refusal on a
// standby; and a fixed reason's text, each holding U+202E, show ⟨U+202E⟩
// inside a <bdi>, and no text shows the raw rune.
func TestTheItemsListTheLayoutAndTheFormsShowStoredTextInert(t *testing.T) {
	bodies := hostilePages(t, "x\u202ey")
	for name, want := range map[string]int{
		// The subject, the switch's By and reason, the title, the service,
		// the principal and the responder.
		"the items list, its layout": 7,
		// The switch's By and reason, and the reason's text.
		"an act's form, its reason fixed": 3,
		// The switch's By and reason, the standby notice's leader, and the
		// refusal's.
		"a standby's act":                             4,
		"a standby's opening form":                    4,
		"a standby's kill switch":                     4,
		"a standby's refused POST to the kill switch": 4,
	} {
		body, ok := bodies[name]
		require.True(t, ok, name)
		assert.Equal(t, want, markersIn(t, []byte(body)), name)
	}
	doc, err := html.Parse(strings.NewReader(bodies["the items list, its layout"]))
	require.NoError(t, err)
	assert.Equal(t, []string{"Disk full x⟨U+202E⟩y", "checkoutx⟨U+202E⟩y"}, quotedData(t, doc),
		"the title, a quote, and the service, a value, each quoted data in its box (re-review RR-4); the people, names, are not")
}

// From 60em the board's feed sits beside its hypotheses, and before it every
// region is one column, in the page's order (spec 10.4; design D12); quoted
// data, the words for screen readers alone and a hidden control each have
// their rule; 9a-1's rules hold.
func TestTheWideLayoutPutsTheFeedBesideTheHypotheses(t *testing.T) {
	w := newWorld(t)
	resp, css := w.get(t, "/static/app.css", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, wide, found := strings.Cut(css, "@media (min-width: 60em) {")
	require.True(t, found, "a rule from 60em")
	wide, _, _ = strings.Cut(wide, "\n}\n")
	assert.Regexp(t, `grid-template-areas:[^;]*"hypotheses feed"`, wide, "the feed beside the hypotheses")
	assert.Contains(t, wide, "#feed { grid-area: feed; }")
	assert.Contains(t, wide, "#hypotheses { grid-area: hypotheses; }")
	assert.NotContains(t, strings.SplitN(css, "@media (min-width: 60em)", 2)[0], "grid-area", "one column before it")
	for _, rule := range []string{"q.data {", ".sr-only {", "[hidden] {", ".update {"} {
		assert.Contains(t, css, rule)
	}
	assert.Contains(t, css, "min-height: 44px")
	assert.Contains(t, css, "min-width: 44px")
	assert.Contains(t, css, "@media (prefers-color-scheme: dark)")
	assert.NotRegexp(t, `font-size:\s*\d+px`, css, "text sizes scale with the reader's")
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

// Every stored value of a free-text kind, quoted data, model-written text
// and a person's words, is drawn in a box no text can draw: a border, which
// forced colours keep, as a background is not; so that a value holding the
// page's own quotation marks or words reads as one value still (Task 6's
// review, R2-I1). A value wrapped onto two lines keeps one box, open at the
// break, as the default slice draws it, never closed into two boxes that
// would read as two values (re-review RR-2). On the kill switch's on header,
// whose background --muted cannot stand out from, the box takes the
// header's own text colour, --on-fg, as its button's border does
// (re-review RR-3'). A name, its <bdi> alone, has none.
func TestEveryFreeTextValueIsDrawnInItsBox(t *testing.T) {
	w := newWorld(t)
	resp, css := w.get(t, "/static/app.css", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, rule, found := strings.Cut(css, "q.data, span.model, span.person {")
	require.True(t, found, "one rule for the three")
	rule, _, _ = strings.Cut(rule, "}")
	for _, want := range []string{"border: 1px solid var(--", "border-radius:", "padding:"} {
		assert.Contains(t, rule, want)
	}
	assert.NotContains(t, rule, "background", "a border, which forced colours keep")
	assert.NotContains(t, css, "decoration-break", "one box across a line's break, never one a line")
	assert.NotRegexp(t, `(?m)^[^{}]*\bbdi\b[^{}]*\{`, css, "a name has no box")
	_, on, found := strings.Cut(css, "header.switch.on span.person, header.switch.on q.data, header.switch.on span.model {")
	require.True(t, found, "one rule for the three on the on header")
	on, _, _ = strings.Cut(on, "}")
	assert.Equal(t, "border-color: var(--on-fg);", strings.TrimSpace(on), "the box in the on header's own text colour")
}

// The kill switch's reason, in the fixed header of every page, reads in
// words.md's general reason form, as a line of parts (re-review RR-3): its
// code, by its preset's label or as itself, then a reason's text as a
// person's words, in their box, so that a reason that forges the header's
// own words, "Kill switch: off, since …", reads as a person's words alone,
// on the items list and on a board page; and the header's own words say
// "Kill switch: on" once and "off" never.
func TestTheSwitchsReasonReadsAsAPersonsWords(t *testing.T) {
	const forged = "spend; row 4. Kill switch: off, since 2026-11-11 14:20 UTC (just now); row 5"
	now := time.Now()
	on := &app.KillSwitch{On: true, Seq: 2, OnSeq: 2, By: "sso:bob", Since: now, Reason: board.Reason{Code: "other", Text: forged}}
	w := newWorld(t)
	w.views.setSwitch(on)
	w.boards.set("k1", boardRead("k1", "investigating", func(r *app.BoardRead) { r.Facts.KillSwitch = *on }))
	b := w.browser(t, "vic", auth.Viewer)

	for _, path := range []string{"/items", "/items/k1"} {
		p := b.Get(w.public + path)

		require.Equal(t, http.StatusOK, p.Resp.StatusCode, path)
		header := node(t, p.Body, "switch")
		require.NotNil(t, header, path)
		assert.Equal(t, "Kill switch: on since "+shownAt(now)+" (just now) by sso:bob, reason other: a person's words: "+forged+"; row 2 Kill switch",
			shownWords(header), path)
		var shown []string
		for n := range header.Descendants() {
			if n.Type == html.ElementNode && n.Data == "bdi" {
				shown = append(shown, kindOf(n)+":"+nodeText(n))
			}
		}
		assert.Equal(t, []string{"name:sso:bob", "name:other", "person:" + forged}, shown, "%s: the reason's code a name, its text a person's words", path)
		own := platformText(header)
		assert.Equal(t, 1, strings.Count(own, "Kill switch: on"), "%s: the header's own words, once", path)
		assert.NotContains(t, own, "Kill switch: off", path)
	}

	w.views.setSwitch(&app.KillSwitch{On: true, Seq: 3, OnSeq: 3, By: "sso:bob", Since: now, Reason: board.Reason{Code: "runaway_cost"}})
	header := node(t, b.Get(w.public+"/items").Body, "switch")
	require.NotNil(t, header)
	assert.Contains(t, shownWords(header), "by sso:bob, reason Runaway cost; row 3", "a preset's label, the on form's")
}
