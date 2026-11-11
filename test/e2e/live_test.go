//go:build e2e

package e2e

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/web/webtest"
	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// The scenarios of Plan 9a-2 of the web interface (design D15): a person
// reads an alert item's board through its trigger's life, as they reload it
// and as live.js polls its regions, and the raw output of a read the board
// cites, which only the item's members are shown. They run under the
// process runner alone, as the web scenarios of Plan 9a-1 do (web_test.go),
// and read what a person reads, the pages and the regions' answers, beside
// the admin API's views, inspect and serve's output.

// The board's words the scenarios look for (Plan 9a-2's words of the board).
const (
	rawOutputLink = "Raw output"
	noOutputHere  = "There is no output here."
)

// tag matches an element's start or end tag.
var tag = regexp.MustCompile(`<[^>]*>`)

// wordsOf is what a person reads of a page: its body's text, its tags left
// out and its character references decoded.
func wordsOf(body string) string { return html.UnescapeString(tag.ReplaceAllString(body, "")) }

// zoned is t as the pages show a time to the minute, in the deployment's
// zone, web.timeZone, which the harness leaves at UTC.
func zoned(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 MST") }

// notYet says whether p is no answer yet: a 503, as a board's read that ran
// past its wait answers (R141), which a reader tries again. It fails the
// test at once, with p's body, on any other status but those of want.
func notYet(t *testing.T, p *webtest.Page, want ...int) bool {
	t.Helper()
	if p.Resp.StatusCode == http.StatusServiceUnavailable {
		return true
	}
	require.Contains(t, want, p.Resp.StatusCode, "%s answers %d:\n%s", p.Resp.Request.URL.Path, p.Resp.StatusCode, p.Body)
	return false
}

// showing loads the page at path in b until its words hold each of want, as
// a person reloads a board, and returns its body: a board's read may be a
// second old (R137), and a 503 is no answer yet (see notYet). It fails the
// test at once on any status but 200 and 503; and when the page does not hold
// them within 10 s, with its last answer's status and body.
func showing(t *testing.T, web *harness.Web, b *webtest.Browser, path string, want ...string) string {
	t.Helper()
	var last *webtest.Page
	shown := within(10*time.Second, func() bool {
		last = b.Get(web.URL() + path)
		if notYet(t, last, http.StatusOK) {
			return false
		}
		require.Equal(t, path, last.Resp.Request.URL.Path, "the page is %s", path)
		words := wordsOf(string(last.Body))
		for _, w := range want {
			if !strings.Contains(words, w) {
				return false
			}
		}
		return true
	})
	require.True(t, shown, "the page at %s shows %q within 10 s; its last answer, %d:\n%s", path, want, last.Resp.StatusCode, last.Body)
	return string(last.Body)
}

// boardRoot reads what live.js reads of a board page or a regions document
// (R173): its #board's data-etag, the regions' ETag, and its data-order,
// "<record seq>.<board seq>".
var boardRoot = regexp.MustCompile(`<div id="board" data-etag="([^"]*)"(?: data-order="([^"]*)")?>`)

// versionOf is a board page's or a regions document's ETag and order.
func versionOf(t *testing.T, body string) (etag, order string) {
	t.Helper()
	m := boardRoot.FindStringSubmatch(body)
	require.NotNil(t, m, "the body has a #board with its ETag:\n%s", body)
	return html.UnescapeString(m[1]), html.UnescapeString(m[2])
}

// seqsOf is an order's record seq and board seq.
func seqsOf(t *testing.T, order string) (record, board int64) {
	t.Helper()
	r, b, ok := strings.Cut(order, ".")
	require.True(t, ok, "an order is <record seq>.<board seq>: %q", order)
	record, err := strconv.ParseInt(r, 10, 64)
	require.NoError(t, err, order)
	board, err = strconv.ParseInt(b, 10, 64)
	require.NoError(t, err, order)
	return record, board
}

// stopLine is the header's stop line, 9a-1's: the rounds, the tokens and
// the wall clock the run used.
var stopLine = regexp.MustCompile(`\b\d+ rounds?, \d+ tokens?, [0-9hms.]+ of wall clock\b`)

// Scenario 33 (design D15; R164; plan review M12(c)): an alert item's board
// through its trigger's life, as a responder reloads it: the opening by its
// source, the title quoted data, the firing in the feed and in the header;
// a firing older than the instance's status, received late and not applied;
// the resolution, which stops the run, trigger_resolved, the header saying
// when the item closes by itself, a minute after; and a firing within that
// minute, which resumes the run, the feed's divider. Then live.js's poll of
// the regions: the page's own ETag answers 304 with no body; once the alert
// resolves again, a poll answers 200 within 10 s, as the board's tick may
// answer 304 for a second, with a new ETag, an order above the page's and
// the new resolution's line; and that ETag answers 304 again, at the second
// poll at the latest, as a minute's turn between two polls can change an
// "ago". Serve's output holds no alert's marker.
func TestAnAlertItemsBoardShowsItsTriggerLifeAndAPollSeesANewEntry(t *testing.T) {
	cfg := alerting()
	cfg.WorkType.AutoClose = time.Minute
	// The round the firing resumes stays open, its turns held at the shut
	// gate, until its limit, which outlasts the polls by far: nothing but
	// the resolution changes the board while they run.
	cfg.WorkType.RoundLimit = 2 * time.Minute
	env := webServing(t, cfg)
	d := withWeb(quickRetry)
	d.Inbound = true
	a := leading(t, env, d)
	g := shutGate(t)
	env.Vendor().Script(g.script)
	in := a.Inbound()
	require.NotNil(t, in, "serve binds the inbound listener and writes its address")
	web := env.Web()

	// Every time lies in the past, so that the instance clamps none.
	t0 := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
	first := firing("fp-a", "r1", t0)
	send(t, in, harness.InboundAnswer{Opened: 1}, first)
	id := harness.InboundItemID(env.Tenant(), alertSource, first.id())
	a.Admin().WaitItem(t, id, investigatingNow, runWait)

	b, _ := web.SignIn(t, "rita", harness.RespondersGroup)
	path := "/items/" + id
	board := showing(t, web, b, path, "Opened by the source alerting", "Alert firing", "Alert: 1 instance, 1 firing")
	assert.Contains(t, board, `<q class="data"><bdi>`+first.Annotations["summary"]+`</bdi></q>`, "the title, A's marker, is quoted data")

	old := firing("fp-a", "r1", t0.Add(-10*time.Minute))
	send(t, in, harness.InboundAnswer{Late: 1}, old)
	showing(t, web, b, path, "Received late, not applied")

	send(t, in, harness.InboundAnswer{Appended: 1}, first.resolved(t0.Add(time.Minute)))
	g.release()
	v := a.Admin().WaitItem(t, id, stopped, runWait)
	require.Equal(t, "trigger_resolved", v.Stop, "the resolution stops the run at its round's end")
	board = showing(t, web, b, path, "Alert resolved", "Closes by itself at")
	assert.Regexp(t, stopLine, wordsOf(board), "the header's stop line")

	g.hold()
	again := firing("fp-a", "r1", t0.Add(2*time.Minute))
	send(t, in, harness.InboundAnswer{Appended: 1}, again)
	a.Admin().WaitItem(t, id, investigatingNow, runWait)
	// The resumed round's divider shows its deadline once its records are
	// read: the board is still from then on, its turns held.
	board = showing(t, web, b, path, "Reopened after a flap: the alert fired again at "+zoned(t0.Add(2*time.Minute)), "Round 2, open until")

	etag, order := versionOf(t, board)
	require.NotEmpty(t, order, "a visible item's page carries its order")
	regions := web.URL() + path + "/board"
	// Each poll below takes a 503 as no answer yet, within its 10 s (see
	// notYet), and fails at once on any status it does not await.
	var unchanged *webtest.Page
	answered := within(10*time.Second, func() bool {
		unchanged = b.Poll(regions, etag)
		return !notYet(t, unchanged, http.StatusNotModified)
	})
	require.True(t, answered, "the page's own ETag answers 304 within 10 s; the last poll answered %d:\n%s", unchanged.Resp.StatusCode, unchanged.Body)
	assert.Empty(t, unchanged.Body, "a 304 has no body")

	resolvedAt := t0.Add(3 * time.Minute)
	newLine := "Alert resolved: instance fp-a from alerting at " + zoned(resolvedAt)
	require.NotContains(t, wordsOf(board), newLine)
	send(t, in, harness.InboundAnswer{Appended: 1}, again.resolved(resolvedAt))
	var fresh *webtest.Page
	changed := within(10*time.Second, func() bool {
		fresh = b.Poll(regions, etag)
		return !notYet(t, fresh, http.StatusOK, http.StatusNotModified) && fresh.Resp.StatusCode == http.StatusOK
	})
	require.True(t, changed, "a poll answers 200 within 10 s of the resolution; the last poll answered %d:\n%s", fresh.Resp.StatusCode, fresh.Body)
	newETag, newOrder := versionOf(t, string(fresh.Body))
	assert.NotEqual(t, etag, newETag, "a new ETag")
	assert.Equal(t, newETag, fresh.Resp.Header.Get("ETag"), "the header's ETag is the document's")
	assert.Equal(t, newOrder, fresh.Resp.Header.Get("X-Board-Order"), "the header's order is the document's")
	wasRecord, wasBoard := seqsOf(t, order)
	isRecord, isBoard := seqsOf(t, newOrder)
	assert.Greater(t, isRecord, wasRecord, "the order is above the page's: the resolution's record")
	assert.GreaterOrEqual(t, isBoard, wasBoard, "and no board seq below it")
	assert.Contains(t, wordsOf(string(fresh.Body)), newLine, "the regions hold the new resolution's line")

	// The new ETag answers 304 at the second answer at the latest, its first
	// a 200 when a minute's turn changed an "ago"; the last poll, the ETag it
	// sent and what it got are kept for the failure's words.
	var (
		last *webtest.Page
		with string
	)
	sent, turns := newETag, 0
	still := within(10*time.Second, func() bool {
		with = sent
		last = b.Poll(regions, with)
		switch {
		case notYet(t, last, http.StatusOK, http.StatusNotModified):
			return false
		case last.Resp.StatusCode == http.StatusNotModified:
			return true
		}
		turns++
		require.Equal(t, 1, turns, "the new ETag answers 304 at the second answer at the latest; a second 200, to the ETag %s, gave %s:\n%s",
			with, last.Resp.Header.Get("ETag"), last.Body)
		sent, _ = versionOf(t, string(last.Body))
		return false
	})
	require.True(t, still, "the new ETag answers 304 within 10 s; the last poll, with the ETag %s, answered %d, ETag %s:\n%s",
		with, last.Resp.StatusCode, last.Resp.Header.Get("ETag"), last.Body)
	assert.Empty(t, last.Body, "a 304 has no body")

	unheard(t, a, first.Annotations["summary"], old.Annotations["summary"], again.Annotations["summary"])
}

// alphasRawOutput reads the link of alpha's evidence to its raw output from
// a board page's feed: each feed line is a line of the page.
var alphasRawOutput = regexp.MustCompile(`<bdi>alpha</bdi> posted evidence[^\n]*?<a href="([^"]+)">` + rawOutputLink + `</a>`)

// rawBlock is the pattern of a raw output page's block, by its heading's id,
// that holds the lines given and no other, each the pattern of a line's
// markup.
func rawBlock(heading string, lines ...string) *regexp.Regexp {
	return regexp.MustCompile(`<ol class="raw"[^>]*aria-labelledby="` + heading + `">\s*` + strings.Join(lines, `\s*`) + `\s*</ol>`)
}

// Scenario 34 (design D10, D15; R152): Rita, a responder, opens an item
// through the opening form, as its principal, and its agents converge. Her
// board shows alpha's evidence with the link to its raw output; following it
// shows alpha's read, its arguments and its output, each a block of
// numbered lines, the line that holds the evidence's quote marked quoted.
// Vic, a viewer and no member of the item, reads the same evidence with no
// link, and the same URL answers him 404, the one refusal's words; so does
// a call that no board of the item cites, to Rita.
func TestARawOutputIsShownToTheItemsResponderAndHiddenFromAViewer(t *testing.T) {
	env := webServing(t, team(0))
	a := leading(t, env, withWeb(quick))
	reading(env)
	env.Vendor().Script(investigating)
	web := env.Web()

	rita, _ := web.SignIn(t, "rita", harness.RespondersGroup)
	form := rita.Get(web.URL() + "/items/new")
	assert.Contains(t, page(t, form, "/items/new"), "Principal: <bdi>sso:rita</bdi> (you)", "she opens it as its principal")
	opened := rita.Submit(form, "/items/new", url.Values{
		"type": {incident.Type}, "title": {incident.Title}, "service": {incident.Routing["service"]},
	})
	body := page(t, opened, "/items/new")
	m := openedID.FindStringSubmatch(body)
	require.NotNil(t, m, "the opening's answer names the item:\n%s", body)
	id := html.UnescapeString(m[1])
	v := a.Admin().WaitItem(t, id, stopped, runWait)
	require.Equal(t, "converged", v.Stop)

	run := runOf(t, env, id)
	var read, posted string
	calls := callsOf(t, a, run, "tool")
	for _, c := range calls {
		switch {
		case c.Agent == "alpha" && c.Name == "logs/search":
			read = c.ID
		case c.Agent == "alpha" && c.Name == "post_evidence":
			posted = c.ID
		}
	}
	require.NotEmpty(t, read, "alpha read the logs; the run's tool calls: %+v", calls)
	require.NotEmpty(t, posted, "and posted its evidence, a call that no evidence cites; the run's tool calls: %+v", calls)

	path := "/items/" + id
	board := page(t, rita.Get(web.URL()+path), path)
	m = alphasRawOutput.FindStringSubmatch(board)
	require.NotNil(t, m, "Rita's board shows alpha's evidence with its raw output's link:\n%s", board)
	link := html.UnescapeString(m[1])
	assert.Equal(t, path+"/calls/"+read, link, "the link is to alpha's read")

	output := page(t, rita.Get(web.URL()+link), link)
	words := wordsOf(output)
	assert.Contains(t, words, "Raw output of logs.search by alpha.")
	assert.Contains(t, words, searchOutput, "the read's output")
	assert.Regexp(t, rawBlock("output", `<li class="quoted"><bdi>`+regexp.QuoteMeta(searchOutput)+`</bdi> <span class="mark">quoted</span></li>`), output,
		"the output, one numbered line, marked quoted as it holds the quote")
	assert.Regexp(t, rawBlock("arguments", `<li><bdi>q: error</bdi></li>`), output, "the arguments, a numbered line")

	vic, _ := web.SignIn(t, "vic", harness.ViewersGroup)
	board = page(t, vic.Get(web.URL()+path), path)
	assert.Contains(t, board, "<bdi>alpha</bdi> posted evidence", "Vic's board shows alpha's evidence")
	assert.NotContains(t, board, "/calls/", "with no link to a raw output")
	assert.NotContains(t, strings.ToLower(wordsOf(board)), strings.ToLower(rawOutputLink), "nor its words")
	refused := vic.Get(web.URL() + link)
	assert.Equal(t, http.StatusNotFound, refused.Resp.StatusCode, "the raw output answers Vic 404:\n%s", refused.Body)
	assert.Contains(t, wordsOf(string(refused.Body)), noOutputHere)
	assert.NotContains(t, string(refused.Body), searchOutput)

	uncited := rita.Get(web.URL() + path + "/calls/" + posted)
	assert.Equal(t, http.StatusNotFound, uncited.Resp.StatusCode, "a call no board of the item cites answers Rita 404:\n%s", uncited.Body)
	assert.Contains(t, wordsOf(string(uncited.Body)), noOutputHere)
}
