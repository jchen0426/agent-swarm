//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The control socket's tests (R186). TestControlRefusesALongPath and
// TestControlSocketIsPrivate need no browser and always run, on a stand-in
// that no command drives; the others drive Chrome for Testing, and skip
// unless ChromeEnv names its binary:
//
//	AGENTSWARM_BROWSER_GATE_CHROME="$HOME/Library/Caches/agentswarm/chrome-for-testing/157.0.8103.78/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing" \
//	  go test -tags e2e -count=1 -run TestControl -v ./test/e2e/harness/
//
// Each binds its socket in a short private directory under /tmp, as macOS
// binds a Unix socket's path of 103 bytes at most, and drives it with a Go
// client over a Unix dialer, as curl --unix-socket does.

// controlTimeout bounds each of the tests' commands: above a tab's own
// waits, a minute.
const controlTimeout = 90 * time.Second

// socketDir is a short private directory for a test's socket, 0700, which
// the test removes.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "abg")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// controlClient drives a control socket: HTTP over its Unix socket.
type controlClient struct {
	t    *testing.T
	http *http.Client
}

func dialControl(t *testing.T, path string) *controlClient {
	t.Helper()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}}
	t.Cleanup(tr.CloseIdleConnections)
	return &controlClient{t: t, http: &http.Client{Transport: tr, Timeout: controlTimeout}}
}

// controlReply is a command's answer: its status and its JSON object.
type controlReply struct {
	status int
	body   map[string]any
	raw    string
}

// try sends a command, its body as JSON unless it is nil, and reads its
// answer, which must be a JSON object; it touches no test, so that a
// goroutine may call it.
func (cl *controlClient) try(method, path string, body any) (controlReply, error) {
	var in io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return controlReply{}, err
		}
		in = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, "http://gate"+path, in)
	if err != nil {
		return controlReply{}, err
	}
	resp, err := cl.http.Do(req)
	if err != nil {
		return controlReply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return controlReply{}, err
	}
	r := controlReply{status: resp.StatusCode, raw: string(raw)}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		return r, fmt.Errorf("%s %s answered %q, not JSON: %s", method, path, ct, raw)
	}
	if err := json.Unmarshal(raw, &r.body); err != nil {
		return r, fmt.Errorf("%s %s answered no JSON object: %w: %s", method, path, err, raw)
	}
	return r, nil
}

func (cl *controlClient) do(method, path string, body any) controlReply {
	cl.t.Helper()
	r, err := cl.try(method, path, body)
	require.NoError(cl.t, err)
	return r
}

func (cl *controlClient) post(path string, body any) controlReply {
	cl.t.Helper()
	return cl.do(http.MethodPost, path, body)
}

func (cl *controlClient) get(path string) controlReply {
	cl.t.Helper()
	return cl.do(http.MethodGet, path, nil)
}

// ok requires a command's answer to be 200, and returns its JSON.
func ok(t *testing.T, r controlReply) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status, r.raw)
	return r.body
}

// args is a command's JSON.
type args = map[string]any

func noWorld() any { return map[string]string{} }

func noEnd() {}

// standIn is a Chrome that no command drives, for the tests of the socket
// alone: they ask nothing of it but reads.
func standIn(t *testing.T) *Chrome {
	t.Helper()
	return &Chrome{runDir: t.TempDir(), tabs: map[string]*Tab{}, exited: make(chan struct{})}
}

// standInTab is an open tab of a stand-in, which holds only the events a
// test keeps in it, for the socket's reads.
func standInTab(c *Chrome, name string) *Tab {
	b := &Tab{c: c, name: name, changed: make(chan struct{}), done: make(chan struct{})}
	c.mu.Lock()
	c.tabs[name] = b
	c.mu.Unlock()
	return b
}

// keep appends events to a tab, numbered after its last, as its session
// would record them.
func keep(b *Tab, events ...Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, e := range events {
		b.seq++
		e.Seq = b.seq
		b.events = append(b.events, e)
	}
}

// served serves a control socket for c in a short private directory of the
// test's, with a World and an End that do nothing unless o gives its own,
// and returns its client.
//
//nolint:gocritic // hugeParam: as ServeControl's options.
func served(t *testing.T, c *Chrome, o ControlOptions) *controlClient {
	t.Helper()
	o.Path = filepath.Join(socketDir(t), "control.sock")
	if o.World == nil {
		o.World = noWorld
	}
	if o.End == nil {
		o.End = noEnd
	}
	require.NoError(t, ServeControl(t, c, o))
	return dialControl(t, o.Path)
}

// tabOf is c's open tab of the name.
func tabOf(t *testing.T, c *Chrome, name string) *Tab {
	t.Helper()
	c.mu.Lock()
	b := c.tabs[name]
	c.mu.Unlock()
	require.NotNil(t, b, "tab %s is open", name)
	return b
}

// eventually calls cond every 200 ms, on the test's goroutine, until it
// holds, and fails the test when it has not within d.
func eventually(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	for deadline := time.Now().Add(d); !cond(); {
		if time.Now().After(deadline) {
			require.FailNow(t, "not within "+d.String(), what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// listOf is a JSON array of objects.
func listOf(t *testing.T, v any) []map[string]any {
	t.Helper()
	items, isList := v.([]any)
	require.True(t, isList, "a JSON array: %v", v)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m, isObject := it.(map[string]any)
		require.True(t, isObject, "a JSON object: %v", it)
		out = append(out, m)
	}
	return out
}

// controlCommands are the cmds of the tab's control records, in order.
func controlCommands(t *testing.T, cl *controlClient, tab string) []map[string]any {
	t.Helper()
	return listOf(t, ok(t, cl.get("/events?tab="+tab+"&kind=control"))["events"])
}

// ServeControl refuses a socket's path over 103 bytes, saying why, and binds
// none; a path of 103 bytes, the most macOS binds, serves. It needs no
// browser.
func TestControlRefusesALongPath(t *testing.T) {
	dir := socketDir(t)
	long := filepath.Join(dir, strings.Repeat("s", 104-len(dir)-1))
	require.Len(t, long, 104)
	err := ServeControl(t, standIn(t), ControlOptions{Path: long, World: noWorld, End: noEnd})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "104 bytes", "it says how long the path is")
	assert.Contains(t, err.Error(), "103", "and how long one may be")
	_, err = os.Lstat(long)
	require.ErrorIs(t, err, os.ErrNotExist, "nothing was bound")

	most := long[:103]
	require.NoError(t, ServeControl(t, standIn(t), ControlOptions{Path: most, World: noWorld, End: noEnd}))
	ok(t, dialControl(t, most).get("/status"))
}

// The socket lives where its owner alone reaches it (R186): ServeControl
// serves in a 0700 directory, which stays 0700, and refuses one that others
// may enter, binding nothing there. It needs no browser.
func TestControlSocketIsPrivate(t *testing.T) {
	dir := socketDir(t)
	path := filepath.Join(dir, "control.sock")
	require.NoError(t, ServeControl(t, standIn(t), ControlOptions{Path: path, World: noWorld, End: noEnd}))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "the socket's directory is 0700")
	sock, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSocket, sock.Mode().Type(), "a socket is bound there")
	ok(t, dialControl(t, path).get("/status"))

	open := socketDir(t)
	require.NoError(t, os.Chmod(open, 0o755))
	err = ServeControl(t, standIn(t), ControlOptions{Path: filepath.Join(open, "control.sock"), World: noWorld, End: noEnd})
	require.ErrorContains(t, err, "0700")
	require.ErrorContains(t, err, "0755", "it says what the directory is")
	_, err = os.Lstat(filepath.Join(open, "control.sock"))
	require.ErrorIs(t, err, os.ErrNotExist, "nothing was bound")
}

// GET /polls with after answers every poll that changed after the seq, whole:
// its request, its answer, its body, or what the recorder saw follow its
// answer. A poll that a first read found unanswered is answered again by
// the next read after that read's last, with its answer. It needs no
// browser.
func TestControlPollsAfterASeqAnswerEveryPollThatChangedSince(t *testing.T) {
	c := standIn(t)
	b := standInTab(c, "a")
	cl := served(t, c, ControlOptions{})
	s, ms := time.Second, time.Millisecond

	before := &tabEvents{t: t}
	before.start(0)
	before.poll(5*s, "r1")
	before.answer(5*s+100*ms, "r1", 200, false)
	before.body(5*s+150*ms, "r1", "2.2", "1", false)
	before.poll(10*s+100*ms, "r2")
	keep(b, before.events...)
	first := ok(t, cl.get("/polls?tab=a"))
	assert.InDelta(t, len(before.events), first["last"], 0, "last is the tab's latest seq")
	polls := listOf(t, first["polls"])
	require.Len(t, polls, 2)
	assert.Nil(t, polls[1]["answered"], "r2 has no answer yet")

	after := &tabEvents{t: t}
	after.answer(10*s+200*ms, "r2", 200, false)
	after.body(10*s+250*ms, "r2", "3.3", "2", false)
	after.record(10*s+300*ms, "region", map[string]string{"region": "feed", "v": "f3", "how": "replaced"})
	keep(b, after.events...)
	again := ok(t, cl.get(fmt.Sprintf("/polls?tab=a&after=%v", first["last"])))
	assert.InDelta(t, len(before.events)+len(after.events), again["last"], 0)
	polls = listOf(t, again["polls"])
	require.Len(t, polls, 1, "r2 alone changed since: %v", polls)
	assert.Equal(t, "r2", polls[0]["requestId"])
	assert.InDelta(t, 200, polls[0]["status"], 0, "with its answer")
	assert.Equal(t, "3.3", polls[0]["order"])
	assert.Equal(t, []any{"feed"}, polls[0]["changed"])
	assert.InDelta(t, 5, polls[0]["gap"], 0.001, "its gap is from r1's answer, before the seq")
	assert.Empty(t, listOf(t, ok(t, cl.get(fmt.Sprintf("/polls?tab=a&after=%v", again["last"])))["polls"]), "nothing changed since")
}

// GET /events refuses, with the reason, a kind no event has, such as a
// misspelt one that would otherwise read as none of that kind; each kind an
// event has is read. It needs no browser.
func TestControlRefusesAKindNoEventHas(t *testing.T) {
	c := standIn(t)
	standInTab(c, "a")
	cl := served(t, c, ControlOptions{})
	for _, kinds := range []string{"issues", "log,exception,console,issues", "logs", ","} {
		r := cl.get("/events?tab=a&kind=" + kinds)
		assert.Equal(t, http.StatusBadRequest, r.status, "kind=%s: %s", kinds, r.raw)
		assert.Contains(t, r.body["error"], "kind", "kind=%s says why", kinds)
	}
	ok(t, cl.get("/events?tab=a&kind="+strings.Join(eventKinds, ",")))
	ok(t, cl.get("/events?tab=a&kind=log,exception,console,issue"))
	ok(t, cl.get("/events?tab=a"))
}

// eventKinds are the kinds the harness records, as its source names them:
// each record and recordLocked call's kind, and recordCommand's control, so
// that /events refuses no kind an event has.
func TestEventKindsAreTheKindsRecorded(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	require.NoError(t, err)
	call := regexp.MustCompile(`\brecord(?:Locked)?\("([a-z]+)"`)
	recorded := map[string]bool{}
	for _, f := range sources {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			recorded[m[1]] = true
		}
	}
	require.NotEmpty(t, recorded, "the harness records its events by record and recordLocked")
	kinds := make([]string, 0, len(recorded))
	for k := range recorded {
		kinds = append(kinds, k)
	}
	assert.ElementsMatch(t, eventKinds, kinds)
}

// refusingListener is a listener whose every accept fails, as one whose
// socket broke does.
type refusingListener struct{ net.Listener }

func (refusingListener) Accept() (net.Conn, error) { return nil, errors.New("the accept was refused") }

// A socket that stops serving mid-run, its accept failing, is reported, not
// dropped: its commands would go unanswered, with nothing in the log. It
// needs no browser.
func TestControlReportsAServeThatStops(t *testing.T) {
	listen := controlListen
	controlListen = func(network, address string) (net.Listener, error) {
		ln, err := listen(network, address)
		if err != nil {
			return nil, err
		}
		return refusingListener{ln}, nil
	}
	t.Cleanup(func() { controlListen = listen })
	test := &fakeTB{}
	t.Cleanup(test.end)
	path := filepath.Join(socketDir(t), "control.sock")
	require.NoError(t, ServeControl(test, standIn(t), ControlOptions{Path: path, World: noWorld, End: noEnd}))
	eventually(t, 5*time.Second, func() bool {
		errs, _ := test.reported()
		return slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, "the accept was refused") })
	}, "the stopped serve is reported")
}

// Each command once, with the shape of its answer, on a board-shaped page
// that runs the repository's live.js (see boardFixture) on a pinned server,
// beside a server under the same CA that is not pinned.
func TestControlServesEachCommand(t *testing.T) {
	chromeBinary(t)
	live, err := os.ReadFile(filepath.Join("..", "..", "..", "internal", "web", "pages", "static", "live.js"))
	require.NoError(t, err)
	ca := newCA(t)
	s := testServer(t, ca, &boardFixture{live: live, n: map[string]int{}})
	unpinned := testServer(t, ca, http.HandlerFunc(plainPage))
	c := launch(t, ca, []string{s.addr}, s.addr, unpinned.addr)
	var ends atomic.Int32
	cl := served(t, c, ControlOptions{
		Origins: []string{s.base, unpinned.base},
		World:   func() any { return map[string]string{"world": "the test's"} },
		End:     func() { ends.Add(1) },
	})
	board := s.base + "/items/a"

	// GET /status: the browser's version, the run directory, the socket and
	// the tabs, none yet.
	st := ok(t, cl.get("/status"))
	assert.Equal(t, c.Version(), st["version"])
	assert.Equal(t, c.runDir, st["runDir"])
	assert.NotEmpty(t, st["socket"])
	assert.Empty(t, listOf(t, st["tabs"]))

	// POST /tab: the tab, its window and where it is, in a window of its own.
	opened := ok(t, cl.post("/tab", args{"name": "a", "url": board}))
	assert.Equal(t, "a", opened["tab"])
	assert.Positive(t, opened["window"])
	assert.Equal(t, board, opened["location"])

	// POST /eval: the expression's value, in the gate world.
	assert.Equal(t, "/items/a", ok(t, cl.post("/eval", args{"tab": "a", "expr": "location.pathname"}))["value"])
	// Two answers applied, 2.2 and 3.3, so that a replay has an earlier 200
	// below the page's order.
	eventually(t, 30*time.Second, func() bool {
		v := ok(t, cl.post("/eval", args{"tab": "a", "expr": `document.getElementById("board-header").dataset.v`}))["value"]
		return v == "h3"
	}, "the page applies the answer 3.3")

	// GET /polls: each poll's time, status, gap since the last answer, order
	// and the regions its answer changed.
	read := ok(t, cl.get("/polls?tab=a"))
	assert.Positive(t, read["last"], "the seq a next read starts after")
	polls := listOf(t, read["polls"])
	require.GreaterOrEqual(t, len(polls), 2)
	first, second := polls[0], polls[1]
	assert.Equal(t, true, first["first"], "the first poll of its document")
	assert.Nil(t, first["gap"], "no answer came before the first")
	assert.InDelta(t, 200, first["status"], 0)
	assert.Equal(t, "2.2", first["order"])
	assert.Equal(t, "2", first["round"])
	assert.ElementsMatch(t, []any{"board-header", "summary", "feed"}, first["changed"], "the regions whose data-v the answer changed")
	assert.InDelta(t, 1, first["announced"], 0, "its new round was announced once")
	assert.NotEmpty(t, first["at"])
	assert.NotEmpty(t, first["answered"])
	assert.Nil(t, second["first"])
	assert.InDelta(t, 5, second["gap"], 1, "live.js polls 5 s after an answer")
	assert.Equal(t, "3.3", second["order"])

	// POST /replay: the replayed order, strictly below the page's, and the
	// regions that differ (R190).
	rp := ok(t, cl.post("/replay", args{"tab": "a"}))
	replayed, page := fmt.Sprint(rp["replayed"]), fmt.Sprint(rp["page"])
	assert.True(t, orderBelow(replayed, page), "%s is strictly below the page's %s", replayed, page)
	assert.NotEmpty(t, rp["differing"])

	// POST /key, /type and /click answer what they did.
	assert.Equal(t, "Tab", ok(t, cl.post("/key", args{"tab": "a", "key": "Tab"}))["key"])
	assert.Equal(t, "gate", ok(t, cl.post("/type", args{"tab": "a", "text": "gate"}))["text"])
	assert.Equal(t, "h1", ok(t, cl.post("/click", args{"tab": "a", "selector": "h1"}))["selector"])

	// POST /emulate: a phone, then the zero emulation, which clears it.
	phone := ok(t, cl.post("/emulate", args{"tab": "a", "width": 390, "height": 844, "dpr": 3, "mobile": true, "dark": true, "fontSize": 32}))
	assert.InDelta(t, 390, phone["width"], 0)
	assert.Equal(t, true, phone["dark"])
	assert.InDelta(t, 390, ok(t, cl.post("/eval", args{"tab": "a", "expr": "innerWidth"}))["value"], 0)
	ok(t, cl.post("/emulate", args{"tab": "a"}))
	assert.InDelta(t, 1280, ok(t, cl.post("/eval", args{"tab": "a", "expr": "innerWidth"}))["value"], 0, "the window's own width, as the clear answers")

	// POST /hide and /show: the page's visibility, as it reads it.
	assert.Equal(t, "hidden", ok(t, cl.post("/hide", args{"tab": "a"}))["visibility"])
	assert.Equal(t, "hidden", ok(t, cl.post("/eval", args{"tab": "a", "expr": "document.visibilityState"}))["value"])
	assert.Equal(t, "visible", ok(t, cl.post("/show", args{"tab": "a"}))["visibility"])

	// POST /screenshot: screenshots/<name>.png, a PNG, and its path; a name
	// taken is refused, so that no screenshot is lost.
	shot := ok(t, cl.post("/screenshot", args{"tab": "a", "name": "board"}))
	assert.Equal(t, filepath.Join(c.runDir, "screenshots", "board.png"), shot["path"])
	png, err := os.ReadFile(filepath.Join(c.runDir, "screenshots", "board.png"))
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(png, []byte("\x89PNG\r\n\x1a\n")), "a PNG")
	again := cl.post("/screenshot", args{"tab": "a", "name": "board"})
	assert.Equal(t, http.StatusConflict, again.status, again.raw)

	// POST /navigate, /back and /reload: where the tab is once each loaded.
	assert.Equal(t, board+"?x=1", ok(t, cl.post("/navigate", args{"tab": "a", "url": board + "?x=1"}))["location"])
	assert.Equal(t, board, ok(t, cl.post("/back", args{"tab": "a"}))["location"])
	assert.Equal(t, board, ok(t, cl.post("/reload", args{"tab": "a"}))["location"])
	// A navigation refused for its certificate answers 502, and says so.
	cert := cl.post("/navigate", args{"tab": "a", "url": unpinned.base + "/plain"})
	assert.Equal(t, http.StatusBadGateway, cert.status, cert.raw)
	assert.Equal(t, true, cert.body["certificate"])
	assert.Contains(t, cert.body["error"], "ERR_CERT_AUTHORITY_INVALID")

	// POST /tab with no URL: about:blank. POST /cookies/clear acts on every
	// open tab.
	blank := ok(t, cl.post("/tab", args{"name": "b"}))
	assert.Equal(t, "about:blank", blank["location"])
	assert.Equal(t, true, ok(t, cl.post("/cookies/clear", nil))["cleared"])
	assert.Equal(t, true, ok(t, cl.post("/close", args{"tab": "b"}))["closed"])
	// A closed tab refuses a command that acts, and says why; its close is
	// recorded, as each command that acts is.
	closed := cl.post("/eval", args{"tab": "b", "expr": "1"})
	assert.Equal(t, http.StatusConflict, closed.status, closed.raw)
	assert.Contains(t, closed.body["error"], "closed")
	assert.Equal(t, http.StatusConflict, cl.post("/close", args{"tab": "b"}).status, "a second close")

	// GET /events: a tab's evidence, of the kinds asked, and its last seq.
	// Each command that acted is recorded as kind control (R184), the refused
	// screenshot not, and the failed navigation with its error.
	ev := ok(t, cl.get("/events?tab=a&kind=control"))
	assert.Equal(t, "a", ev["tab"])
	assert.Positive(t, ev["last"])
	var cmds []string
	for _, e := range listOf(t, ev["events"]) {
		assert.Equal(t, "control", e["kind"])
		d, isObject := e["detail"].(map[string]any)
		require.True(t, isObject, "%v", e)
		assert.NotEmpty(t, d["began"])
		assert.Contains(t, d, "ms")
		cmds = append(cmds, fmt.Sprint(d["cmd"]))
		if d["cmd"] == "navigate" && d["error"] != nil {
			assert.Contains(t, d["error"], "ERR_CERT_AUTHORITY_INVALID")
		}
	}
	for _, cmd := range []string{"tab", "eval", "replay", "key", "type", "click", "emulate", "hide", "show", "screenshot", "navigate", "back", "reload", "cookies/clear"} {
		assert.Contains(t, cmds, cmd)
	}
	assert.Equal(t, 1, countOf(cmds, "screenshot"), "the refused screenshot is not recorded: %v", cmds)
	assert.Equal(t, 1, countOf(controlNames(t, cl, "b"), "cookies/clear"), "the clear is recorded on each open tab")
	assert.Equal(t, []string{"tab", "cookies/clear", "close"}, controlNames(t, cl, "b"), "b's commands, its close the last")

	// GET /netlog: R185's check on the NetLog so far, provisional while
	// Chrome runs.
	nl := ok(t, cl.get("/netlog"))
	assert.Equal(t, true, nl["provisional"])
	assert.IsType(t, []any{}, nl["findings"])
	assert.Contains(t, nl, "connects")

	// GET /world: the World's own answer.
	assert.Equal(t, map[string]any{"world": "the test's"}, ok(t, cl.get("/world")))

	// POST /close: the tab closes, and its evidence stays readable.
	assert.Equal(t, true, ok(t, cl.post("/close", args{"tab": "a"}))["closed"])
	var states []string
	for _, tab := range listOf(t, ok(t, cl.get("/status"))["tabs"]) {
		states = append(states, fmt.Sprintf("%v %v", tab["name"], tab["open"]))
	}
	assert.Equal(t, []string{"a false", "b false"}, states)
	assert.NotEmpty(t, listOf(t, ok(t, cl.get("/events?tab=a&kind=control"))["events"]), "a closed tab's events are still read")
	namesA := controlNames(t, cl, "a")
	assert.Equal(t, "close", namesA[len(namesA)-1], "a's close is recorded, the last of its commands")

	// POST /end: End, once.
	assert.Equal(t, true, ok(t, cl.post("/end", nil))["ending"])
	assert.Equal(t, int32(1), ends.Load())
}

// controlNames are the cmds of a tab's control records, in order.
func controlNames(t *testing.T, cl *controlClient, tab string) []string {
	t.Helper()
	var cmds []string
	for _, e := range controlCommands(t, cl, tab) {
		d, _ := e["detail"].(map[string]any)
		cmds = append(cmds, fmt.Sprint(d["cmd"]))
	}
	return cmds
}

func countOf(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return n
}

// The socket refuses, with a 4xx and the reason, an unknown command, a
// method a command does not take, a tab it does not know, a URL off its
// origins, file:/// among them, a screenshot's name off its pattern, ../x
// among them, a field a command does not take, and a tab's name the run has
// used; none of it reaches the browser or is recorded.
func TestControlRefusesUnknownCommandsTabsAndOrigins(t *testing.T) {
	chromeBinary(t)
	ca := newCA(t)
	s := testServer(t, ca, http.HandlerFunc(plainPage))
	c := launch(t, ca, []string{s.addr}, s.addr)
	cl := served(t, c, ControlOptions{Origins: []string{s.base}})
	page := s.base + "/plain"
	ok(t, cl.post("/tab", args{"name": "a", "url": page}))
	b := tabOf(t, c, "a")
	mark := tabSeq(b)

	refused := func(status int, method, path string, body any, why string) {
		t.Helper()
		r := cl.do(method, path, body)
		assert.Equal(t, status, r.status, "%s %s %v: %s", method, path, body, r.raw)
		assert.NotEmpty(t, r.body["error"], "%s %s says why", method, path)
		assert.Contains(t, r.body["error"], why, "%s %s", method, path)
	}
	refused(http.StatusNotFound, http.MethodGet, "/nope", nil, "/nope")
	refused(http.StatusNotFound, http.MethodPost, "/tabs", args{"name": "b"}, "/tabs")
	refused(http.StatusMethodNotAllowed, http.MethodGet, "/navigate", nil, "POST")
	refused(http.StatusMethodNotAllowed, http.MethodPost, "/status", nil, "GET")
	refused(http.StatusNotFound, http.MethodPost, "/navigate", args{"tab": "zz", "url": page}, "zz")
	refused(http.StatusNotFound, http.MethodPost, "/eval", args{"tab": "zz", "expr": "1"}, "zz")
	refused(http.StatusNotFound, http.MethodGet, "/events?tab=zz", nil, "zz")
	refused(http.StatusNotFound, http.MethodGet, "/polls?tab=zz", nil, "zz")
	offOrigins := []string{
		"file:///etc/hosts",
		"file:///",
		"http://" + s.addr + "/plain",
		"https://127.0.0.1:1/plain",
		"https://user@" + s.addr + "/plain",
		"https://localhost:" + strings.Split(s.addr, ":")[1] + "/plain",
		"javascript:alert(1)",
		"data:text/html,x",
		"chrome://version",
		"about:srcdoc",
		"//" + s.addr + "/plain",
		"/plain",
	}
	for _, u := range offOrigins {
		refused(http.StatusForbidden, http.MethodPost, "/navigate", args{"tab": "a", "url": u}, "R186")
		refused(http.StatusForbidden, http.MethodPost, "/tab", args{"name": "b", "url": u}, "R186")
	}
	refused(http.StatusBadRequest, http.MethodPost, "/navigate", args{"tab": "a"}, "url")
	for _, name := range []string{"../x", "X", "", strings.Repeat("x", 41), "a/b", "x.png"} {
		refused(http.StatusBadRequest, http.MethodPost, "/screenshot", args{"tab": "a", "name": name}, "name")
	}
	refused(http.StatusBadRequest, http.MethodPost, "/eval", args{"tab": "a", "expr": "1", "world": "main"}, "world")
	refused(http.StatusBadRequest, http.MethodPost, "/eval", "1", "JSON")
	refused(http.StatusBadRequest, http.MethodPost, "/eval", args{"tab": "a"}, "expr")
	refused(http.StatusBadRequest, http.MethodPost, "/eval", args{"expr": "1"}, "tab")
	refused(http.StatusBadRequest, http.MethodPost, "/tab", args{"name": "Not a name"}, "name")
	refused(http.StatusConflict, http.MethodPost, "/tab", args{"name": "a"}, "a")
	refused(http.StatusBadRequest, http.MethodGet, "/events?tab=a&after=x", nil, "after")
	refused(http.StatusBadRequest, http.MethodGet, "/events?tab=a&kinds=log", nil, "kinds")
	refused(http.StatusBadRequest, http.MethodPost, "/cookies/clear", args{"all": true}, "all")

	// Nothing of it reached the browser, and nothing was recorded.
	assert.Equal(t, page, locationOf(b), "the tab is where it was")
	assert.Empty(t, b.Events(mark, "navigated", "control"), "no refused command navigated, or was recorded")
	for _, e := range b.Events(mark, "request") {
		assert.NotContains(t, offOrigins, detailOf(t, e)["url"], "the browser asked for nothing refused")
	}
	shots, err := os.ReadDir(filepath.Join(c.runDir, "screenshots"))
	if !errors.Is(err, os.ErrNotExist) {
		require.NoError(t, err)
		assert.Empty(t, shots, "no screenshot was written")
	}
	_, err = os.Lstat(filepath.Join(c.runDir, "x.png"))
	require.ErrorIs(t, err, os.ErrNotExist, "nor ../x.png beside screenshots/")
	tabs := listOf(t, ok(t, cl.get("/status"))["tabs"])
	require.Len(t, tabs, 1, "no refused /tab opened one")
	assert.Equal(t, "a", tabs[0]["name"])

	// The contrast: about:blank, which the socket opens.
	assert.Equal(t, "about:blank", ok(t, cl.post("/navigate", args{"tab": "a", "url": "about:blank"}))["location"])
}

// POST /end calls End, once however often it is posted; afterwards no
// command acts, though reads still answer. The run then ends as the
// environment's does: its test returns, and ServeControl's cleanup stops the
// socket and cancels the commands in flight, then waits for each to answer:
// an evaluation that never settles, which then records itself, so that its
// control record, and /end's, reach events.jsonl before the browser's Close;
// and a read of a World that takes half a second, as the environment's
// probes of its instances may, which no cancellation ends, so that a stop
// that did not wait would return before it.
func TestControlEndCallsEnd(t *testing.T) {
	chromeBinary(t)
	ca := newCA(t)
	s := testServer(t, ca, http.HandlerFunc(plainPage))
	c := launch(t, ca, []string{s.addr}, s.addr)
	ctx := testContext(t)
	b, err := c.Tab(ctx, "a", true)
	require.NoError(t, err)
	require.NoError(t, b.Navigate(ctx, s.base+"/plain"))

	var ends atomic.Int32
	var worldDone atomic.Bool
	entered := make(chan struct{}, 1)
	slowWorld := func() any {
		entered <- struct{}{}
		time.Sleep(500 * time.Millisecond)
		worldDone.Store(true)
		return map[string]string{"world": "slow"}
	}
	hung, read := make(chan controlReply, 1), make(chan controlReply, 1)
	inFlight := func(cl *controlClient, method, path string, body any, into chan<- controlReply) {
		r, err := cl.try(method, path, body)
		if err != nil {
			r.raw = err.Error()
		}
		into <- r
	}
	t.Run("served", func(t *testing.T) {
		ended := make(chan struct{})
		path := filepath.Join(socketDir(t), "control.sock")
		require.NoError(t, ServeControl(t, c, ControlOptions{
			Path: path, Origins: []string{s.base}, World: slowWorld,
			End: func() {
				if ends.Add(1) == 1 {
					close(ended)
				}
			},
		}))
		cl := dialControl(t, path)
		go inFlight(cl, http.MethodPost, "/eval", args{"tab": "a", "expr": "new Promise(() => {})"}, hung)
		eventually(t, 10*time.Second, func() bool {
			c.conn.mu.Lock()
			defer c.conn.mu.Unlock()
			return len(c.conn.pending) > 0
		}, "the evaluation is in flight")

		assert.Equal(t, true, ok(t, cl.post("/end", nil))["ending"])
		assert.Equal(t, true, ok(t, cl.post("/end", nil))["ending"], "a second /end answers too")
		assert.Equal(t, int32(1), ends.Load(), "End is called once")
		late := cl.post("/navigate", args{"tab": "a", "url": s.base + "/plain"})
		assert.Equal(t, http.StatusServiceUnavailable, late.status, "no command acts once the run ends: %s", late.raw)
		ok(t, cl.get("/status"))
		go inFlight(cl, http.MethodGet, "/world", nil, read)
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("GET /world never reached World")
		}
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Fatal("End did not end the run")
		}
	})

	// The commands in flight had answered, and the evaluation recorded
	// itself, when the socket's stop, the subtest's cleanup, returned.
	assert.True(t, worldDone.Load(), "the stop waited for the read in flight")
	var cmds []string
	for _, e := range b.Events(0, "control") {
		d := detailOf(t, e)
		cmds = append(cmds, fmt.Sprint(d["cmd"]))
		if d["cmd"] == "eval" {
			assert.NotEmpty(t, d["error"], "the cancelled evaluation records its error")
		}
	}
	assert.True(t, slices.Contains(cmds, "end") && slices.Contains(cmds, "eval"),
		"the end and the cancelled evaluation are recorded by the stop's return: %v", cmds)
	assert.NotContains(t, cmds, "navigate", "the late navigation was refused")
	for what, ch := range map[string]chan controlReply{"the evaluation, cancelled": hung, "the read of the World": read} {
		select {
		case r := <-ch:
			want := http.StatusOK
			if ch == hung {
				want = http.StatusBadGateway
			}
			assert.Equal(t, want, r.status, "%s answered: %s", what, r.raw)
		case <-time.After(10 * time.Second):
			t.Fatalf("%s never answered", what)
		}
	}
	require.NoError(t, c.Close())
	assert.Equal(t, len(cmds), kindLines(t, filepath.Join(c.runDir, "events.jsonl"), "control"),
		"each control record reached events.jsonl before Close")
}
