package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/jchen0426/agent-swarm/internal/clock"
	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/ids"
	"github.com/jchen0426/agent-swarm/internal/web"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/webtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/key"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
)

// renderedBoards serve the web interface's pages as serve wires them, over
// the real views and board watch on the memory stores (R162): the records,
// the inbound events' titles, the board entries, the runs, the read calls
// and the kill switch's table the test writes, the views the items list
// reads, and the pages a viewer signed in, sso:vic, gets.
type renderedBoards struct {
	t        *testing.T
	records  *memory.Records
	events   *memory.Store
	runs     *memory.Runs
	calls    *memory.Calls
	controls *memory.Controls
	views    *app.Views
	srv      *http.Server
	cookie   *http.Cookie
}

// renderedSecret is the session key every server of renderedBoards reads,
// as both instances of a deployment share it.
var renderedSecret = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

func newRenderedBoards(t *testing.T) *renderedBoards {
	t.Helper()
	return renderBoards(t, webLeader{}, webLease{})
}

// renderBoards are renderedBoards on an instance whose leader is leader,
// and whose lease's holder is lease's.
func renderBoards(t *testing.T, leader web.Leader, lease web.LeaseHolder) *renderedBoards {
	t.Helper()
	records, events, controls := memory.NewRecords(time.Now), memory.NewStore(time.Now), memory.NewControls(time.Now)
	runs, calls := memory.NewRuns(), memory.NewCalls()
	r := &renderedBoards{t: t, records: records, events: events, runs: runs, calls: calls, controls: controls}
	r.views = app.NewViews(records, app.NewBoards(events, calls, runs, ids.UUIDv7{}), &app.ViewPorts{
		Controls: controls, ReopenWindow: 24 * time.Hour, ClosedRulings: 72 * time.Hour,
		Closed: records, Incidents: records, Titles: records, Entries: events, Siblings: records,
	})
	r.srv = r.serve(r.watch(memory.NewProbe(records, events), nil), leader, lease)
	keys, err := auth.NewKeys(renderedSecret, "https://idp.example.com", "agentswarm")
	require.NoError(t, err)
	r.cookie = webtest.SignedIn(t, keys, []auth.Role{auth.Viewer}, "vic")
	return r
}

// watch is an instance's board watch over r's views: probe, its probe of
// the boards' versions, and mono, the clock its windows run on, nil for the
// process's monotonic clock.
func (r *renderedBoards) watch(probe app.Probe, mono func() time.Time) *app.BoardWatch {
	return app.NewBoardWatch(r.views, &app.WatchPorts{
		Probe: probe, Controls: r.controls, Held: func() bool { return true }, Now: time.Now, Monotonic: mono, BuildWait: 10 * time.Second,
	})
}

// serve is the web server serve wires over watch, on an instance whose
// leader is leader and whose lease's holder is lease's, with r's views and
// read calls; the viewer's cookie opens on each such server.
func (r *renderedBoards) serve(watch *app.BoardWatch, leader web.Leader, lease web.LeaseHolder) *http.Server {
	r.t.Helper()
	d := processDeployment(r.t, true)
	d.Web = &deploy.Web{
		PublicURL: "https://agents.example.com",
		OIDC: deploy.OIDC{
			Issuer: "https://idp.example.com", ClientID: "agentswarm", ClientSecretRef: "secret://web/client",
			SubjectClaim: "sub", GroupsClaim: "groups",
		},
		Groups:        deploy.Groups{Viewers: "v", Responders: "r", Admins: "a"},
		SessionKeyRef: "secret://web/session", SessionLength: 8 * time.Hour, FreshWindow: 5 * time.Minute, TimeZone: "UTC",
	}
	log := slog.New(slog.DiscardHandler)
	// The pages' clock is the database's, never sampled here, which reads
	// the host's time, as the memory stores and the watch do.
	p := webPorts(d, fakeWebCoordinator{}, leader, compatibleSchema{}, r.views, lease, webRevisions{}, watch, newWebStores(nil, d), clock.NewDB(nil, nil), log)
	p.ReadCalls = r.calls
	srv, err := newWebServer(d, &p, webSecrets{key: renderedSecret}, log)
	require.NoError(r.t, err)
	return srv
}

// opened is a person's opening of an incident for sso:carol.
var opened = item.Body{Opened: &item.Opened{
	Type: "incident", Title: "Checkout fails", Source: "person", Principal: "sso:carol", Arm: item.ArmVisible, How: item.NoWindow, By: "sso:carol",
}}

// draft is a record the test writes: its body; the run it is of, "" for
// none; and the incident its column holds, "" for the item's own, inc-<id>.
type draft struct {
	body     item.Body
	run      board.RunID
	incident item.IncidentID
}

// write appends bodies, in order, as item id's next records, of its
// incident inc-<id>, and returns them as committed.
func (r *renderedBoards) write(id item.ID, bodies ...item.Body) []item.Record {
	r.t.Helper()
	drafts := make([]draft, len(bodies))
	for i := range bodies {
		drafts[i] = draft{body: bodies[i]}
	}
	return r.append(id, drafts...)
}

// append appends drafts, in order, as item id's next records, and returns
// them as committed.
func (r *renderedBoards) append(id item.ID, drafts ...draft) []item.Record {
	r.t.Helper()
	committed, err := r.records.Append(r.t.Context(), id, func(history []item.Record) ([]item.Draft, error) {
		return drafted(id, history, drafts), nil
	})
	require.NoError(r.t, err)
	return committed
}

// event is an inbound event as ingest commits it: its item_triggered, or an
// inbound opening's item_opened, and its row in the inbound store, which
// holds its title (app.InboundEvent; see rowOf, ingest.go).
type event struct {
	body item.Body
	row  *app.InboundEvent
}

// ingest appends e's record, then extra, as item id's next records, with
// e's row, in one append, as ingest commits an event
// (app.Coordinator.recordInbound).
func (r *renderedBoards) ingest(id item.ID, e *event, extra ...draft) {
	r.t.Helper()
	drafts := append([]draft{{body: e.body}}, extra...)
	_, err := r.records.AppendInbound(r.t.Context(), id, func(history []item.Record) ([]item.Draft, *app.InboundEvent, error) {
		return drafted(id, history, drafts), e.row, nil
	})
	require.NoError(r.t, err)
}

// drafted is drafts as item id's next records after history, each of the
// records' version and of its incident, inc-<id> unless it names another.
func drafted(id item.ID, history []item.Record, drafts []draft) []item.Draft {
	out := make([]item.Draft, len(drafts))
	for i := range drafts {
		b, incident := drafts[i].body, drafts[i].incident
		b.V = item.Version
		if incident == "" {
			incident = item.IncidentID("inc-" + id)
		}
		out[i] = item.Draft{ID: fmt.Sprintf("%s-rec-%d", id, len(history)+i+1), Item: id, Incident: incident, Run: drafts[i].run, Body: b}
	}
	return out
}

// post appends entries, in order, as run's next board entries, each of the
// board's version.
func (r *renderedBoards) post(run board.RunID, entries ...board.Draft) {
	r.t.Helper()
	_, err := r.events.Append(r.t.Context(), run, func([]board.Event) ([]board.Draft, error) {
		out := slices.Clone(entries)
		for i := range out {
			out[i].Run, out[i].Body.V = run, board.Version
		}
		return out, nil
	})
	require.NoError(r.t, err)
}

// get is the body of path as the viewer gets it, which answers 200.
func (r *renderedBoards) get(path string) []byte {
	r.t.Helper()
	rec := r.on(r.srv, path)
	require.Equal(r.t, http.StatusOK, rec.Code, "%s: %s", path, rec.Body.String())
	return rec.Body.Bytes()
}

// on is srv's answer to the viewer's GET of path, with an If-None-Match line
// for each of etags, as live.js polls.
func (r *renderedBoards) on(srv *http.Server, path string, etags ...string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequestWithContext(r.t.Context(), http.MethodGet, path, http.NoBody)
	req.Host = "agents.example.com"
	req.AddCookie(r.cookie)
	for _, e := range etags {
		req.Header.Add("If-None-Match", e)
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

// words are the words item id's board page shows to the viewer, as a reader
// reads them: its text, each element that is not inline apart, the controls
// the hidden attribute hides left out, its spaces folded. The page answers
// 200.
func (r *renderedBoards) words(id item.ID) string {
	r.t.Helper()
	doc, err := html.Parse(bytes.NewReader(r.get("/items/" + string(id))))
	require.NoError(r.t, err)
	inline := map[string]bool{"bdi": true, "q": true, "span": true, "a": true, "strong": true}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		hidden := false
		for _, a := range n.Attr {
			hidden = hidden || a.Key == "hidden"
		}
		switch {
		case n.Type == html.TextNode:
			b.WriteString(n.Data)
		case hidden:
		case n.Type == html.ElementNode && !inline[n.Data]:
			b.WriteByte(' ')
			for c := range n.ChildNodes() {
				walk(c)
			}
			b.WriteByte(' ')
		default:
			for c := range n.ChildNodes() {
				walk(c)
			}
		}
	}
	walk(doc)
	return strings.Join(strings.Fields(b.String()), " ")
}

// shownAt is t as the pages show it in UTC, to the minute.
func shownAt(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 MST") }

// The rows that rest on the record-determined lifecycle facts (R169; plan
// review I1) render through the real views and board watch on the memory
// stores, behind the web server serve wires (R162): row 7, an item not
// staffed by its routing, with its cause; row 9, an item a person closed,
// with when and by whom, and until when it may be reopened; and row 14, an
// item the kill switch's on-row left not staffed, its banner shown with the
// switch since turned off.
func TestTheRecordRowsRenderThroughTheRealViews(t *testing.T) {
	r := newRenderedBoards(t)
	routing := item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseRouting, AutoClose: 2 * time.Hour}}
	r.write("n1", opened, routing)
	closed := r.write("c1", opened, routing, item.Body{Closed: &item.Closed{By: "sso:rita", Reason: &board.Reason{Code: "resolved"}}})
	_, _, err := r.controls.SetKillSwitch(t.Context(), true, "sso:bob", board.Reason{Code: "security"}, 0)
	require.NoError(t, err)
	r.write("k1", opened, item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed, UnstaffedBy: 1, AutoClose: 2 * time.Hour}})
	_, _, err = r.controls.SetKillSwitch(t.Context(), false, "sso:bob", board.Reason{}, 1)
	require.NoError(t, err)

	row7, row9, row14 := r.words("n1"), r.words("c1"), r.words("k1")

	assert.Contains(t, row7, "Not staffed: handle with your usual process")
	assert.Contains(t, row7, "Cause: its routing failed its check")
	assert.NotContains(t, row7, "Stopped by the kill switch")
	at := closed[len(closed)-1].At
	assert.Contains(t, row9, "Closed at "+shownAt(at)+" (just now) by sso:rita; may be reopened until "+shownAt(at.Add(24*time.Hour))+".")
	assert.Contains(t, row14, "Stopped by the kill switch")
	assert.Contains(t, row14, "Kill switch: off, since")
	assert.Contains(t, row14, "Cause: the kill switch was on")
}

// standbyLeader is an instance that does not hold the lease, which shows
// the standby's notice on every page.
type standbyLeader struct{}

func (standbyLeader) Status() (epoch int64, held, draining bool) { return 3, false, false }

// namedLease is a lease held by the instance named name.
type namedLease struct{ name string }

func (l namedLease) Holder(context.Context) (string, time.Time, error) {
	return l.name, time.Now().Add(time.Minute), nil
}

// hostile is path n's tagged value (Review Focus 1): a script, a right-to-
// left override, tag-<n>, a zero-width space and a line feed.
func hostile(n int) string { return "<script>x</script>\u202etag-" + strconv.Itoa(n) + "\u200b\n" }

// short is path n's tagged value without its script, for a field the page
// cuts short: a right-to-left override, tag-<n>, a zero-width space and a
// line feed, 9 runes at most to tag-999.
func short(n int) string { return strings.TrimPrefix(hostile(n), "<script>x</script>") }

// shownAs is how the pages show a path's value: a single-line value,
// through inertLine, its line feed marked; multi-line text, through
// inertText, its line feed kept; a raw output's, through inertText a line
// at a time, its line feed ending its line; a hypothesis's key's value, in
// the key's canonical form (key.Key.String), its runes and its line feed
// escaped by its quoting; or a value no page shows at all, as the stored
// claim of the platform's evidence (design D8; review I8).
type shownAs int

const (
	singleLine shownAs = iota
	multiLine
	rawLine
	inKey
	neverShown
)

// hostilePath is a stored field the test writes its own tagged value into:
// its name, its tag's number, how the pages show it, and the pages that
// show it.
type hostilePath struct {
	name  string
	n     int
	as    shownAs
	pages []string
}

// marker is how the pages show p's tag: inert, ⟨U+202E⟩tag-<n>⟨U+200B⟩; or,
// in a key, quoted, \u202etag-<n>\u200b\n.
func (p *hostilePath) marker() string {
	if p.as == inKey {
		return `\u202etag-` + strconv.Itoa(p.n) + `\u200b\n`
	}
	return "⟨U+202E⟩tag-" + strconv.Itoa(p.n) + "⟨U+200B⟩"
}

// hostilePaths are the paths a test writes.
type hostilePaths []hostilePath

// value is the tagged value of a new path, name, shown as as on pages.
func (h *hostilePaths) value(name string, as shownAs, pages ...string) string {
	n := len(*h) + 1
	*h = append(*h, hostilePath{name: name, n: n, as: as, pages: pages})
	return hostile(n)
}

// short is the short tagged value of a new path, name, a single-line value
// shown on pages (see short).
func (h *hostilePaths) short(name string, pages ...string) string {
	h.value(name, singleLine, pages...)
	return short(len(*h))
}

// jsonValue is the tagged value of a new path, name, as a fact keeps it,
// JSON text of at most board.MaxValueRunes characters: the value, its line
// feed left out, as a JSON string, then the line feed, which JSON takes as
// white space.
func (h *hostilePaths) jsonValue(name string, pages ...string) string {
	v := strings.TrimSuffix(h.value(name, singleLine, pages...), "\n")
	return `"` + v + `"` + "\n"
}

// document is a JSON document whose one member, member, is the tagged value
// of a new path, name, shown as a raw output's line on pages.
func (h *hostilePaths) document(t *testing.T, name, member string, pages ...string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{member: h.value(name, rawLine, pages...)})
	require.NoError(t, err)
	return b
}

// inertPage is a page the hostile test reads: its name; the name of the
// page whose paths it shows, its own, or, a regions document, its board
// page's, whose regions it holds; whether it holds live.js's script
// element, as a board page does; and its body.
type inertPage struct {
	name, shows string
	scripted    bool
	body        []byte
}

// liveSrc is live.js's path as a board page names it: /static/live.<12
// hex>.js.
const liveSrc = `^/static/live\.[0-9a-f]{12}\.js$`

// assertInert asserts that page renders every path it shows inert (Review
// Focus 1): no raw U+202E or U+200B anywhere; no script element but, on a
// board page, one, live.js's, its hashed src, deferred and empty; no on*
// attribute, and no href or src starting javascript:; each path's tag
// shown, each time in a text of its own <bdi>, a single-line value's line
// feed marked ⟨U+000A⟩ and a multi-line text's kept; and the tag of a value
// no page shows nowhere.
func assertInert(t *testing.T, page *inertPage, paths hostilePaths) {
	t.Helper()
	assert.False(t, bytes.ContainsRune(page.body, '\u202e'), "%s: a raw U+202E", page.name)
	assert.False(t, bytes.ContainsRune(page.body, '\u200b'), "%s: a raw U+200B", page.name)
	doc, err := html.Parse(bytes.NewReader(page.body))
	require.NoError(t, err)
	var scripts []*html.Node
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		if n.Data == "script" {
			scripts = append(scripts, n)
		}
		for _, a := range n.Attr {
			assert.False(t, strings.HasPrefix(strings.ToLower(a.Key), "on"), "%s: %s has %s", page.name, n.Data, a.Key)
			if a.Key == "href" || a.Key == "src" {
				assert.False(t, strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.Val)), "javascript:"), "%s: %s=%q", page.name, a.Key, a.Val)
			}
		}
	}
	if !page.scripted {
		assert.Empty(t, scripts, "%s: a script element", page.name)
	} else if assert.Len(t, scripts, 1, "%s: live.js's script element alone", page.name) {
		s := scripts[0]
		require.Len(t, s.Attr, 2, "%s: src and defer alone", page.name)
		assert.Equal(t, "src", s.Attr[0].Key, page.name)
		assert.Regexp(t, liveSrc, s.Attr[0].Val, page.name)
		assert.Equal(t, html.Attribute{Key: "defer"}, s.Attr[1], page.name)
		assert.Nil(t, s.FirstChild, "%s: no content", page.name)
	}
	for i := range paths {
		p := &paths[i]
		if !slices.Contains(p.pages, page.shows) {
			continue
		}
		shown := 0
		for n := range doc.Descendants() {
			if n.Type != html.TextNode || !strings.Contains(n.Data, p.marker()) {
				continue
			}
			shown++
			if !assert.True(t, n.Parent != nil && n.Parent.Data == "bdi", "%s: %s outside its <bdi>: %q", page.name, p.name, n.Data) {
				continue
			}
			switch p.as {
			case singleLine:
				assert.True(t, strings.Contains(n.Data, "⟨U+000A⟩") && !strings.Contains(n.Data, "\n"), "%s: %s's line feed marked: %q", page.name, p.name, n.Data)
			case multiLine:
				assert.Contains(t, n.Data, p.marker()+"\n", "%s: %s's line feed kept", page.name, p.name)
			}
		}
		if p.as == neverShown {
			assert.Zero(t, shown, "%s: %s never shown", page.name, p.name)
			continue
		}
		assert.Positive(t, shown, "%s: %s shown", page.name, p.name)
	}
}

// A hostile string through every path renders inert (Review Focus 1; plan
// review I3; Task 6's review, R2-M1): the records of four items and their
// boards' entries are written straight to the memory stores, past the
// gateway's and the coordinator's input checks, as a later build or a
// corrupt row could, each inbound event with its row as ingest commits it,
// each stored field its own tagged value, and read through the real views
// and board watch behind the web server serve wires, by a responder of the
// item, on a standby. The paths: every record kind's stored fields (the
// item's type and title, routing keys and values, a By, a reason's code and
// text, the start's commit, an invited agent, a round's way of ending, each
// event's instance, source and title through the inbound store's titles);
// every board entry kind's (an agent's and a person's names and claims, a
// quote, a source, a statement, an alias's, a wrap-up round's key, a task's
// title, a ruling's reason, a dismissed refutation's author, another kind of
// evidence ref, a proposer, a target's argument and value, a summary's text
// and value parts, a fact's name and value); every notice's names; a strip
// sibling's key; a raw output and its arguments; the layout's switch By,
// reason and leader's name; and the items list's title, service, principal
// and responders. The platform's evidence's stored claims never show (design
// D8). A path that skips inertText, inertLine or <bdi> fails. Each board's
// regions document is read too, as its poll reads it, with the same
// assertions; a board page holds one script element, live.js's, by its
// hash, and no other page any (Task 8). The fields that hold
// no tag, which the fold or the board's replay refuses one in, are Task 6's
// report's: a task's tags, a resume entry's cause, a stop's reason, a
// notice's code and reason, a declined resume's code and a trigger's status.
func TestAHostileStringThroughEveryPathRendersInert(t *testing.T) {
	const (
		boardA, boardB, boardC = "item a1's board, every line", "item b1's board", "item c1's board"
		newest                 = "item a1's board, its newest lines"
		rawOutput, itemsList   = "a1's raw output", "the items list"
	)
	every := []string{boardA, newest, boardB, boardC, rawOutput, itemsList}
	var h hostilePaths
	r := renderBoards(t, standbyLeader{}, namedLease{name: h.value("the standby notice's leader", singleLine, every...)})
	_, _, err := r.controls.SetKillSwitch(t.Context(), true, h.value("the kill switch's By", singleLine, every...),
		board.Reason{Code: "security", Text: h.value("the kill switch's reason", multiLine, every...)}, 0)
	require.NoError(t, err)
	reason := func(name string, pages ...string) *board.Reason {
		return &board.Reason{Code: "other", Text: h.value(name, multiLine, pages...)}
	}
	never := func(name string) string { return h.value(name, neverShown, every...) }
	// trigger is an inbound event of a new source and instance, as ingest
	// commits it, its row holding its title (rowOf, ingest.go).
	trigger := func(name, eventID string, applied bool, status item.TriggerStatus, resume string, shows ...string) *event {
		tr := &item.Triggered{Source: h.value(name+"'s source", singleLine, shows...), EventID: eventID,
			InstanceID: h.value(name+"'s instance", singleLine, shows...), DedupKey: "d-" + eventID, Status: status, OccurredAt: time.Now(), Applied: applied,
			Resume: resume}
		outcome := app.InboundAppended
		if !applied {
			outcome = app.InboundLate
		}
		return &event{body: item.Body{Triggered: tr}, row: &app.InboundEvent{
			Source: tr.Source, EventID: eventID, InstanceID: tr.InstanceID, DedupKey: tr.DedupKey, Status: status, OccurredAt: tr.OccurredAt,
			Applied: applied, Outcome: outcome, Title: h.value(name+"'s title", multiLine, shows...),
		}}
	}

	// Item a1: every record kind of an item with a run, every entry kind of
	// its board, and its read call.
	conclusion, wrapUp := h.value("a hypothesis's key", inKey, boardA), h.value("a wrap-up round's key", inKey, boardA)
	settings := board.Settings{Schema: key.Schema{Fields: []key.Field{{Name: "service", Values: []string{conclusion, wrapUp}}}}, Convergence: board.DefaultConvergence}
	r.runs.Add(&board.Context{Run: "run-a1", HumanEntries: true, Settings: settings})
	now := time.Now()
	r.append("a1",
		draft{body: item.Body{Opened: &item.Opened{
			Type: h.value("the item's type", singleLine, boardA), Title: h.value("the opening's title", multiLine, boardA, itemsList), Source: "person",
			Routing: map[string]string{
				h.value("a routing key", singleLine, boardA): h.value("its routing value", singleLine, boardA),
				"service": h.value("the routing's service", singleLine, boardA, itemsList),
			},
			Principal: h.value("the principal", singleLine, itemsList), Responders: []string{"sso:vic", h.value("a responder", singleLine, itemsList)},
			Arm: item.ArmVisible, How: item.NoWindow, By: h.value("the opening's By", singleLine, boardA),
		}}},
		draft{body: item.Body{Started: &item.Started{Commit: h.short("the start's commit", boardA)}}},
		draft{run: "run-a1", body: item.Body{RunCreated: &item.RunCreated{
			Class: item.ClassPrimary, Mode: item.ModeVisible, Settings: settings, Budget: item.Budget{Rounds: 5, WallClock: 30 * time.Minute, Tokens: 50000},
			RoundLimit: 150 * time.Second, Agents: []string{h.value("an invited agent", singleLine, boardA), "agent-b"},
		}}},
		draft{run: "run-a1", body: item.Body{RoundOpened: &item.RoundOpened{Round: 1, OpenedAt: now, Deadline: now.Add(150 * time.Second), Budget: 50000}}},
		draft{run: "run-a1", body: item.Body{Extended: &item.Extended{Amount: item.Budget{Rounds: 1}, By: h.value("an extension's By", singleLine, boardA),
			Reason: reason("an extension's reason", boardA)}}},
	)
	firing := trigger("a firing", "ev-fire", true, item.Firing, "", boardA)
	r.ingest("a1", firing)
	r.ingest("a1", trigger("an applied resolution", "ev-resolved", true, item.Resolved, "", boardA))
	r.ingest("a1", trigger("a late event", "ev-late", false, item.Resolved, "", boardA))
	r.append("a1",
		draft{run: "run-a1", body: item.Body{RoundEnded: &item.RoundEnded{Round: 1, EndedBy: item.EndedBy(h.value("a round's way of ending", singleLine, boardA)),
			EndedAt: now, Rounds: 1, Quiet: true, Decision: item.Decision{Next: item.NextStop, Stop: item.StopQuiet}}}},
		draft{run: "run-a1", body: item.Body{RunStopped: &item.RunStopped{Reason: item.StopQuiet, Rounds: 1}}},
		draft{body: item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopQuiet, AutoClose: 2 * time.Hour}}},
	)
	r.ingest("a1", trigger("a declined resume", "ev-declined", true, item.Firing, item.ResumeDeclined+app.CodeKillSwitchOn, boardA))
	r.append("a1",
		draft{body: item.Body{Closed: &item.Closed{By: h.value("a close's By", singleLine, boardA),
			Reason: &board.Reason{Code: h.value("a close's reason's code", singleLine, boardA), Text: h.value("a close's reason", multiLine, boardA)}}}},
		draft{body: item.Body{Reopened: &item.Reopened{By: h.value("a reopen's By", singleLine, boardA), Reason: reason("a reopen's reason", boardA),
			AwaitingClose: item.AwaitingClose{Reason: item.StopQuiet}}}},
	)
	r.ingest("a1", trigger("a firing resume", "ev-resumed", true, item.Firing, item.ResumeResumed, boardA),
		draft{run: "run-a1", body: item.Body{Extended: &item.Extended{Cause: item.ExtendFiring, By: "source:alerting"}}})
	r.append("a1",
		draft{incident: "inc-x", body: item.Body{Linked: &item.Linked{Left: "inc-a1", With: item.ID(h.value("a link's item", singleLine, boardA)),
			By: h.value("a link's By", singleLine, boardA), Reason: reason("a link's reason", boardA)}}},
		draft{incident: "inc-y", body: item.Body{Unlinked: &item.Unlinked{Left: "inc-x", By: h.value("an unlink's By", singleLine, boardA),
			Reason: reason("an unlink's reason", boardA)}}},
	)
	agent := board.Author{Kind: board.AuthorAgent, Name: h.value("an agent's name", singleLine, boardA)}
	refuter := board.Author{Kind: board.AuthorAgent, Name: h.value("a refuting agent's name", singleLine, boardA)}
	person := board.Author{Kind: board.AuthorHuman, Name: h.value("a person's name", singleLine, boardA)}
	ruler := board.Author{Kind: board.AuthorHuman, Name: h.value("a ruler's name", singleLine, boardA)}
	entry := func(id board.EntryID, by board.Author, b *board.Body) board.Draft {
		return board.Draft{ID: id, Round: 1, Author: by, Body: *b}
	}
	refutes := func(id board.EntryID, call string) board.Draft {
		return entry(id, refuter, &board.Body{Evidence: &board.Evidence{Claim: "no deploy", Hypothesis: "hyp", Stance: board.Refutes,
			Provenance: board.Provenance{Kind: board.FromReadCall, CallID: call, Quote: "no deploy", Source: "deploys/list"}}})
	}
	platform := func(id board.EntryID, ref string, source board.Source, claim string) board.Draft {
		return entry(id, board.Coordinator, &board.Body{Evidence: &board.Evidence{Claim: claim,
			Provenance: board.Provenance{Kind: board.FromPlatform, Ref: ref, Source: source}}})
	}
	notice := func(id board.EntryID, n *board.Notice) board.Draft {
		return entry(id, board.Coordinator, &board.Body{Notice: n})
	}
	ruling := func(id board.EntryID, kind board.RulingKind, name string) board.Draft {
		rl := &board.Ruling{Kind: kind, Hypothesis: "hyp", Reason: reason(name, boardA)}
		if kind == board.RulingDismiss {
			rl.Hypothesis, rl.Refutation = "", "ref-2"
		}
		return entry(id, ruler, &board.Body{Ruling: rl})
	}
	notices := func(name string) string { return h.value("a notice's "+name, singleLine, boardA) }
	r.post("run-a1",
		entry("round-1", board.Coordinator, &board.Body{Round: &board.RoundStart{Round: 1}}),
		entry("logs", agent, &board.Body{Evidence: &board.Evidence{Claim: h.value("an agent's claim", multiLine, boardA),
			Provenance: board.Provenance{Kind: board.FromReadCall, CallID: "call-1", Quote: h.value("a quote", multiLine, boardA),
				Source: board.Source(h.value("a source", singleLine, boardA))}}}),
		entry("hyp", agent, &board.Body{Hypothesis: &board.Hypothesis{Key: key.Key{"service": conclusion},
			Statement: h.value("a statement", multiLine, boardA), Evidence: []board.EntryID{"logs"}}}),
		entry("alias", refuter, &board.Body{Hypothesis: &board.Hypothesis{Key: key.Key{"service": conclusion},
			Statement: h.value("an alias's statement", multiLine, boardA), Evidence: []board.EntryID{"logs"}, AliasOf: "hyp"}}),
		entry("hyp-2", agent, &board.Body{Hypothesis: &board.Hypothesis{Key: key.Key{"service": wrapUp}, Statement: "the second hypothesis",
			Evidence: []board.EntryID{"logs"}}}),
		refutes("ref-1", "call-2"), refutes("ref-2", "call-3"),
		entry("person", person, &board.Body{Evidence: &board.Evidence{Claim: h.value("a person's claim", multiLine, boardA),
			Provenance: board.Provenance{Kind: board.FromHuman, Source: board.HumanObservation}, Hypothesis: "hyp", Stance: board.Supports,
			Dismisses: []board.EntryID{"ref-1"}, Reason: reason("a person's reason", boardA)}}),
		platform("of-item", "item:a1", "person", never("the opening's platform evidence's claim")),
		platform("of-trigger", "trigger:ev-fire", board.Source(firing.body.Triggered.Source), never("the trigger update's platform evidence's claim")),
		platform("of-other", h.value("another kind of evidence ref", singleLine, boardA), "probes", never("another platform evidence's claim")),
		entry("round-2", board.Coordinator, &board.Body{Round: &board.RoundStart{Round: 2, WrapUp: "hyp-2"}}),
		entry("retract", agent, &board.Body{Retraction: &board.Retraction{Evidence: "logs"}}),
		entry("task", agent, &board.Body{Task: &board.Task{Title: h.value("a task's title", multiLine, boardA), Tags: []string{"deploys"}}}),
		entry("claim", refuter, &board.Body{Claim: &board.Claim{Task: "task"}}),
		entry("complete", refuter, &board.Body{Completion: &board.Completion{Task: "task", Evidence: []board.EntryID{"ref-2"}}}),
		entry("proposal", board.Gateway, &board.Body{Proposal: &board.Proposal{
			Proposer: h.value("a proposer", singleLine, boardA), Tool: "deploy.rollback", SchemaHash: "sha256:s", ArgsHash: "sha256:a", CallID: "w-1", Hypothesis: "hyp",
			Target: []board.TargetValue{{Arg: h.value("a target's argument", singleLine, boardA), Value: h.value("a target's value", singleLine, boardA)}},
			Summary: []board.SummaryPart{
				{Text: h.value("a summary's text part", multiLine, boardA)}, {Text: h.value("a summary's value", singleLine, boardA), Value: true},
			},
			Facts: map[string]board.Fact{h.value("a fact's name", singleLine, boardA): {Values: []string{h.jsonValue("a fact's value", boardA)}}},
		}}),
		notice("refused", &board.Notice{Code: board.NoticeRefusedCall, Agent: notices("agent, refused"), Tool: notices("tool, refused"), Rule: notices("rule")}),
		notice("failed", &board.Notice{Code: board.NoticeFailedRead, Agent: notices("agent, failed"), Tool: notices("tool, failed")}),
		notice("model-refused", &board.Notice{Code: board.NoticeModelRefused, Agent: notices("agent, model refused"), CallID: "m-1"}),
		notice("model-failed", &board.Notice{Code: board.NoticeModelFailed, Agent: notices("agent, model failed"), CallID: "m-2"}),
		notice("model-unknown", &board.Notice{Code: board.NoticeModelUnknown, Agent: notices("agent, model unknown"), CallID: "m-3"}),
		notice("unstarted", &board.Notice{Code: board.NoticeTurnUnstarted, Agent: notices("agent, unstarted"), Reason: "engine"}),
		notice("yanked", &board.Notice{Code: board.NoticeSkillYanked, Agent: notices("agent, yanked"), Skill: notices("skill")}),
		entry("stop", board.Coordinator, &board.Body{Stop: &board.Stop{Reason: "quiet", Conclusion: "hyp", Rounds: 1}}),
		ruling("reject", board.RulingReject, "a rejection's reason"),
		ruling("unreject", board.RulingUnreject, "an un-rejection's reason"),
		ruling("dismiss", board.RulingDismiss, "a dismissal's reason"),
		entry("resume", board.Coordinator, &board.Body{Resume: &board.Resume{Cause: "extension", Rounds: 1}}),
	)
	r.calls.Add(&board.ReadCall{
		ID: "call-1", Run: "run-a1", Agent: h.value("a read call's agent", singleLine, rawOutput),
		Server: h.value("a read call's server", singleLine, rawOutput), Tool: h.value("a read call's tool", singleLine, rawOutput),
		Args: h.document(t, "a read call's arguments", "q", rawOutput), Output: h.document(t, "a raw output", "m", rawOutput),
	})

	// Item s1: a sibling of a1's incident, its run stopped with a conclusion.
	r.append("s1",
		draft{incident: "inc-y", body: opened},
		draft{incident: "inc-y", body: item.Body{Started: &item.Started{Commit: "c0ffee"}}},
		draft{incident: "inc-y", run: "run-s1", body: item.Body{RunCreated: &item.RunCreated{Class: item.ClassPrimary, Mode: item.ModeVisible,
			Settings: board.Settings{Convergence: board.DefaultConvergence}, Budget: item.Budget{Rounds: 5}, Agents: []string{"agent-s"}}}},
		draft{incident: "inc-y", run: "run-s1", body: item.Body{RoundOpened: &item.RoundOpened{Round: 1, OpenedAt: now, Deadline: now.Add(time.Minute)}}},
		draft{incident: "inc-y", run: "run-s1", body: item.Body{RoundEnded: &item.RoundEnded{Round: 1, EndedBy: item.EndedByTurns, EndedAt: now, Rounds: 1,
			Decision: item.Decision{Next: item.NextStop, Stop: item.StopQuiet}}}},
		draft{incident: "inc-y", run: "run-s1", body: item.Body{RunStopped: &item.RunStopped{Reason: item.StopQuiet, Rounds: 1, Conclusion: "s1-hyp"}}},
	)
	r.post("run-s1", board.Draft{ID: "s1-hyp", Round: 1, Author: board.Author{Kind: board.AuthorAgent, Name: "agent-s"}, Body: board.Body{Hypothesis: &board.Hypothesis{
		Key: key.Key{"service": h.value("a sibling's conclusion's key", inKey, boardA)}, Statement: "the sibling's", Evidence: []board.EntryID{"x"},
	}}})

	// Item b1: queued, then promoted.
	r.write("b1", opened, item.Body{Promoted: &item.Promoted{By: h.value("a promotion's By", singleLine, boardB), Reason: reason("a promotion's reason", boardB)}})

	// Item c1: opened by a source's event, as ingest opens it, its trigger's
	// firing and its flap's link in the opening's append.
	source := h.value("an inbound opening's source", singleLine, boardC)
	r.ingest("c1", &event{
		body: item.Body{Opened: &item.Opened{Type: "incident", Title: "Disk alert", Source: source, Arm: item.ArmVisible, How: item.NoWindow,
			By: app.SourceSubjectPrefix + source, Trigger: &item.Trigger{Source: source, DedupKey: "d-c"}}},
		row: &app.InboundEvent{Source: source, EventID: "ev-c", InstanceID: "i-c", DedupKey: "d-c", Status: item.Firing, OccurredAt: now, Applied: true,
			Outcome: app.InboundOpened, Title: h.value("an inbound opening's title", multiLine, boardC)},
	},
		draft{body: item.Body{Triggered: &item.Triggered{Source: source, EventID: "ev-c", InstanceID: "i-c", DedupKey: "d-c", Status: item.Firing,
			OccurredAt: now, Applied: true}}},
		draft{incident: "inc-z", body: item.Body{Linked: &item.Linked{Left: "inc-c1", With: "a1",
			By: app.SourceSubjectPrefix + h.value("a flap's link's source", singleLine, boardC), Reason: &board.Reason{Code: "flap"}}}},
	)

	// Each board page, and its regions document, which holds the page's
	// regions, as its poll reads it, ?feed=all kept.
	var pages []*inertPage
	for name, path := range map[string]string{boardA: "/items/a1?feed=all", newest: "/items/a1", boardB: "/items/b1", boardC: "/items/c1"} {
		base, query, _ := strings.Cut(path, "?")
		if query != "" {
			query = "?" + query
		}
		pages = append(pages,
			&inertPage{name: name, shows: name, scripted: true, body: r.get(path)},
			&inertPage{name: name + ", its regions document", shows: name, body: r.get(base + "/board" + query)})
	}
	pages = append(pages,
		&inertPage{name: rawOutput, shows: rawOutput, body: r.get("/items/a1/calls/call-1")},
		&inertPage{name: itemsList, shows: itemsList, body: r.get("/items")})

	shown := map[string]bool{}
	for _, page := range pages {
		shown[page.shows] = true
		if page.shows == boardA && page.scripted {
			assert.Contains(t, string(page.body), `id="feed-list"`, "a1's board shows its feed")
		}
		assertInert(t, page, h)
	}
	for _, page := range every {
		assert.True(t, shown[page], "%s is read", page)
	}

	// The board page's script is live.js, served by its hash.
	doc, err := html.Parse(bytes.NewReader(r.get("/items/a1")))
	require.NoError(t, err)
	served := 0
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.Data != "script" || len(n.Attr) == 0 {
			continue
		}
		served++
		src := n.Attr[0].Val
		require.Regexp(t, liveSrc, src)
		script := r.get(src)
		sum := sha256.Sum256(script)
		assert.Equal(t, "/static/live."+hex.EncodeToString(sum[:])[:12]+".js", src, "named by its hash")
		assert.Contains(t, string(script), "XMLHttpRequest", "live.js")
	}
	assert.Equal(t, 1, served, "the board page's script was fetched")
}

// heldProbe is a probe that, once held, answers the versions it gave last,
// as the probe of an instance whose tick lags a tick behind another's.
type heldProbe struct {
	probe app.Probe
	mu    sync.Mutex
	held  bool
	last  map[item.ID]app.BoardVersion
}

func (p *heldProbe) Versions(ctx context.Context, items []item.ID) (map[item.ID]app.BoardVersion, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held {
		return maps.Clone(p.last), nil
	}
	v, err := p.probe.Versions(ctx, items)
	if err == nil {
		p.last = maps.Clone(v)
	}
	return v, err
}

// hold holds the probe at the versions it gave last.
func (p *heldProbe) hold() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = true
}

// movedClock is a monotonic clock the test moves, which the watches' windows
// run on.
type movedClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *movedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *movedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// A lagging instance's poll carries the lower order (Review Focus 3;
// re-review N3): two instances serve one store, each through its own board
// watch, the second's probe held a tick behind. Once a record and an entry
// land past the second's last probe, the first's page shows them, and the
// second's answer to that page's poll, its regions document's #board and
// its X-Board-Order, carries an order below the page's in both parts, which
// live.js drops; the first's own poll carries the page's order, and answers
// 304 to its ETag.
func TestALaggingInstancesPollCarriesTheLowerOrder(t *testing.T) {
	r := newRenderedBoards(t)
	mono := &movedClock{at: time.Now()}
	held := &heldProbe{probe: memory.NewProbe(r.records, r.events)}
	first := r.serve(r.watch(memory.NewProbe(r.records, r.events), mono.now), webLeader{}, webLease{})
	second := r.serve(r.watch(held, mono.now), webLeader{}, webLease{})
	settings := board.Settings{Schema: key.Schema{Fields: []key.Field{{Name: "service", Values: []string{"checkout"}}}}, Convergence: board.DefaultConvergence}
	r.runs.Add(&board.Context{Run: "run-l1", Settings: settings})
	now := time.Now()
	r.append("l1",
		draft{body: opened},
		draft{body: item.Body{Started: &item.Started{Commit: "c0ffee"}}},
		draft{run: "run-l1", body: item.Body{RunCreated: &item.RunCreated{Class: item.ClassPrimary, Mode: item.ModeVisible, Settings: settings,
			Budget: item.Budget{Rounds: 5, WallClock: 30 * time.Minute, Tokens: 50000}, RoundLimit: 150 * time.Second, Agents: []string{"agent-a"}}}},
	)
	r.post("run-l1", board.Draft{ID: "round-1", Round: 1, Author: board.Coordinator, Body: board.Body{Round: &board.RoundStart{Round: 1}}})
	lagging := r.on(second, "/items/l1/board")
	require.Equal(t, http.StatusOK, lagging.Code, lagging.Body.String())
	require.Equal(t, "3.1", lagging.Header().Get("X-Board-Order"), "the second instance's last probe")
	held.hold()

	r.append("l1", draft{run: "run-l1", body: item.Body{RoundOpened: &item.RoundOpened{Round: 1, OpenedAt: now, Deadline: now.Add(150 * time.Second), Budget: 50000}}})
	r.post("run-l1", board.Draft{ID: "task", Round: 1, Author: board.Author{Kind: board.AuthorAgent, Name: "agent-a"},
		Body: board.Body{Task: &board.Task{Title: "check the deploys", Tags: []string{"deploys"}}}})
	mono.advance(2 * time.Second)
	page := r.on(first, "/items/l1")
	require.Equal(t, http.StatusOK, page.Code, page.Body.String())
	doc, err := html.Parse(bytes.NewReader(page.Body.Bytes()))
	require.NoError(t, err)
	var order, etag string
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == "board" {
				for _, b := range n.Attr {
					switch b.Key {
					case "data-order":
						order = b.Val
					case "data-etag":
						etag = b.Val
					}
				}
			}
		}
	}
	require.Equal(t, "4.2", order, "the first instance's page shows the record and the entry")

	polled := r.on(second, "/items/l1/board", etag)
	require.Equal(t, http.StatusOK, polled.Code, "the lagging instance's regions are not the page's")
	behind := polled.Header().Get("X-Board-Order")
	assert.Equal(t, "3.1", behind, "a tick behind")
	assert.Contains(t, polled.Body.String(), `data-order="3.1"`, "its regions document carries the same")
	pr, pb, _ := strings.Cut(order, ".")
	br, bb, _ := strings.Cut(behind, ".")
	for name, part := range map[string][2]string{"its records'": {br, pr}, "its entries'": {bb, pb}} {
		lower, err := strconv.ParseInt(part[0], 10, 64)
		require.NoError(t, err)
		upper, err := strconv.ParseInt(part[1], 10, 64)
		require.NoError(t, err)
		assert.Less(t, lower, upper, "%s part is below the page's", name)
	}

	own := r.on(first, "/items/l1/board", etag)
	assert.Equal(t, http.StatusNotModified, own.Code, "the first instance's poll of its own page")
	assert.Equal(t, order, own.Header().Get("X-Board-Order"))
}
