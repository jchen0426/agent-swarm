//go:build e2e

package harness

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/web" //nolint:depguard // the board's real policy, web.BoardPolicy, which the fixtures serve; a constant, not the system under test.
)

// The browser tests drive Chrome for Testing (R179) and skip unless
// ChromeEnv names its binary:
//
//	AGENTSWARM_BROWSER_GATE_CHROME="$HOME/Library/Caches/agentswarm/chrome-for-testing/157.0.8103.78/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing" \
//	  go test -tags e2e -count=1 -run 'TestChrome|TestPinsOf|TestStartChrome|TestAPage|TestOnlyLoopback|TestOffLoopback|TestWindows|TestTheRecorder|TestBack|TestKeys|TestEmulation|TestReplayOlder|TestClose' -v ./test/e2e/harness/
//
// Each serves its pages from loopback TLS servers under a throwaway CA.

// browserTestTimeout bounds each browser test's own waits.
const browserTestTimeout = 2 * time.Minute

// chromeBinary is the binary ChromeEnv names; the test skips without it.
func chromeBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv(ChromeEnv)
	if bin == "" {
		t.Skip("the browser tests run only when " + ChromeEnv + " names Chrome for Testing's binary")
	}
	return bin
}

// server is a loopback TLS server of a test's.
type server struct {
	base, addr string
	leaf       *x509.Certificate
}

// testServer serves h on 127.0.0.1 with TLS, under a leaf of its own signed
// by ca.
func testServer(t *testing.T, ca *testCA, h http.Handler) server {
	t.Helper()
	cfg, err := ca.serverConfig()
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	require.NoError(t, err)
	s := httptest.NewUnstartedServer(h)
	s.TLS = cfg
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	t.Cleanup(s.Close)
	return server{base: s.URL, addr: strings.TrimPrefix(s.URL, "https://"), leaf: leaf}
}

// launch starts Chrome for Testing in a run directory of the test's,
// trusting the leaves of the pinned servers alone (R180) and allowing
// connects to the allowed ones (R185).
func launch(t *testing.T, ca *testCA, pinned []string, allowed ...string) *Chrome {
	t.Helper()
	bin := chromeBinary(t)
	var pins []string
	if len(pinned) > 0 {
		pool := x509.NewCertPool()
		pool.AddCert(ca.cert)
		var err error
		pins, err = PinsOf(pool, pinned...)
		require.NoError(t, err)
	}
	c, err := StartChrome(t, ChromeOptions{Binary: bin, Pins: pins, RunDir: t.TempDir(), Allowed: allowed})
	require.NoError(t, err)
	return c
}

func newCA(t *testing.T) *testCA {
	t.Helper()
	ca, err := newTestCA()
	require.NoError(t, err)
	return ca
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), browserTestTimeout)
	t.Cleanup(cancel)
	return ctx
}

// servePage writes a page under the board's policy, with the layout's
// viewport and its scripts deferred, as the board's own.
func servePage(w http.ResponseWriter, title, body string, scripts ...string) {
	w.Header().Set("Content-Security-Policy", web.BoardPolicy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1"><title>` + title + `</title>`)
	for _, s := range scripts {
		b.WriteString(`<script src="` + s + `" defer></script>`)
	}
	b.WriteString(`</head><body>` + body + `</body></html>`)
	_, _ = io.WriteString(w, b.String())
}

func serveScript(w http.ResponseWriter, js string) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = io.WriteString(w, js)
}

// plainPage is a page with a heading, a link and two buttons.
func plainPage(w http.ResponseWriter, _ *http.Request) {
	servePage(w, "plain", `<h1>plain</h1><p><a id="link" href="/plain">again</a></p><button type="button" id="one">one</button><button type="button" id="two">two</button>`)
}

// evalString evaluates expr in the gate world and returns its string.
func evalString(ctx context.Context, t *testing.T, b *Tab, expr string) string {
	t.Helper()
	var s string
	require.NoError(t, b.Eval(ctx, expr, &s), "eval %s", expr)
	return s
}

// rec is one of the recorder's records (R183).
type rec struct {
	Seq    int
	Kind   string
	Path   string
	Detail map[string]any
}

// records are the tab's recorder records of the kind after seq, in order.
func records(b *Tab, kind string, after int) []rec {
	var out []rec
	for _, e := range b.Events(after, "record") {
		var r pageRecord
		if json.Unmarshal(e.Detail, &r) != nil || r.Kind != kind {
			continue
		}
		var d map[string]any
		_ = json.Unmarshal(r.Detail, &d)
		out = append(out, rec{Seq: e.Seq, Kind: r.Kind, Path: r.Path, Detail: d})
	}
	return out
}

// waitRecord waits for a record of the kind after seq that match accepts,
// any when match is nil.
func waitRecord(t *testing.T, b *Tab, kind string, after int, within time.Duration, match func(rec) bool) rec {
	t.Helper()
	var found rec
	require.Eventually(t, func() bool {
		for _, r := range records(b, kind, after) {
			if match == nil || match(r) {
				found = r
				return true
			}
		}
		return false
	}, within, 20*time.Millisecond, "no %s record after %d", kind, after)
	return found
}

// waitEvent waits for an event of the kind after seq that match accepts.
func waitEvent(t *testing.T, b *Tab, kind string, after int, within time.Duration, match func(map[string]any) bool) Event {
	t.Helper()
	var found Event
	require.Eventually(t, func() bool {
		for _, e := range b.Events(after, kind) {
			var d map[string]any
			_ = json.Unmarshal(e.Detail, &d)
			if match == nil || match(d) {
				found = e
				return true
			}
		}
		return false
	}, within, 20*time.Millisecond, "no %s event after %d", kind, after)
	return found
}

func detailOf(t *testing.T, e Event) map[string]any {
	t.Helper()
	var d map[string]any
	require.NoError(t, json.Unmarshal(e.Detail, &d))
	return d
}

// The control: with no pin, the harness CA's server is refused for its
// certificate, and the refusal is recorded.
func TestChromeRefusesAnUnpinnedCertificate(t *testing.T) {
	chromeBinary(t)
	ca := newCA(t)
	s := testServer(t, ca, http.HandlerFunc(plainPage))
	c := launch(t, ca, nil, s.addr)
	ctx := testContext(t)
	b, err := c.Tab(ctx, "a", false)
	require.NoError(t, err)
	err = b.Navigate(ctx, s.base+"/plain")
	require.ErrorIs(t, err, ErrCertificate)
	assert.Contains(t, err.Error(), "ERR_CERT_AUTHORITY_INVALID")
	certs := b.Events(0, "certificate")
	require.NotEmpty(t, certs)
	assert.Contains(t, string(certs[0].Detail), s.base+"/plain")
}

// The pin is the presented leaf's alone: its server loads, and a sibling
// under the same CA is still refused.
func TestChromeTrustsOnlyThePinnedLeaves(t *testing.T) {
	chromeBinary(t)
	ca := newCA(t)
	one := testServer(t, ca, http.HandlerFunc(plainPage))
	two := testServer(t, ca, http.HandlerFunc(plainPage))
	c := launch(t, ca, []string{one.addr}, one.addr, two.addr)
	ctx := testContext(t)
	b, err := c.Tab(ctx, "a", false)
	require.NoError(t, err)
	require.NoError(t, b.Navigate(ctx, one.base+"/plain"))
	assert.Equal(t, "plain", evalString(ctx, t, b, "document.title"))
	require.ErrorIs(t, b.Navigate(ctx, two.base+"/plain"), ErrCertificate)
}

// PinsOf pins what each server presents, its leaf, and refuses a chain the
// pool does not verify, and any address off loopback, which it never dials.
func TestPinsOfRefusesAForeignChain(t *testing.T) {
	chromeBinary(t)
	ca, other := newCA(t), newCA(t)
	own := testServer(t, ca, http.HandlerFunc(plainPage))
	foreign := testServer(t, other, http.HandlerFunc(plainPage))
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)

	pins, err := PinsOf(pool, own.addr)
	require.NoError(t, err)
	require.Equal(t, []string{spkiPin(own.leaf)}, pins, "the presented leaf's SPKI, SHA-256 in base64")
	assert.Len(t, pins[0], 44)

	_, err = PinsOf(pool, own.addr, foreign.addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), foreign.addr)
	_, err = PinsOf(pool, "192.0.2.1:443")
	require.ErrorContains(t, err, "loopback")
	_, err = PinsOf(pool, "localhost:"+strings.Split(own.addr, ":")[1])
	require.ErrorContains(t, err, "loopback", "a name, not an address")
	_, err = PinsOf(pool)
	require.Error(t, err)
}

// writePlist writes an Info.plist with the bundle's identifier and
// executable.
func writePlist(t *testing.T, contents, id, exe string) {
	t.Helper()
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>` + id + `</string>
<key>CFBundleExecutable</key><string>` + exe + `</string>
<key>CFBundleShortVersionString</key><string>157.0.8103.78</string>
</dict></plist>
`
	require.NoError(t, os.MkdirAll(filepath.Join(contents, "MacOS"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o600))
}

// StartChrome refuses, before it runs anything, a binary outside a Chrome
// for Testing bundle: one in no bundle, one in another browser's bundle, as
// the person's own Google Chrome's, and one that is not its bundle's
// executable. Each would write a marker file if it ran.
func TestStartChromeRefusesAnotherBundle(t *testing.T) {
	chromeBinary(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := []byte("#!/bin/sh\necho ran > '" + marker + "'\n")

	loose := filepath.Join(dir, "chrome")
	require.NoError(t, os.WriteFile(loose, script, 0o700)) //nolint:gosec // G306: a script the test runs, were the refusal to fail.

	other := filepath.Join(dir, "Google Chrome.app", "Contents")
	writePlist(t, other, "com.google.Chrome", "Google Chrome")
	otherBin := filepath.Join(other, "MacOS", "Google Chrome")
	require.NoError(t, os.WriteFile(otherBin, script, 0o700)) //nolint:gosec // G306: as above.

	cft := filepath.Join(dir, "Google Chrome for Testing.app", "Contents")
	writePlist(t, cft, chromeForTestingID, "Google Chrome for Testing")
	notExe := filepath.Join(cft, "MacOS", "helper")
	require.NoError(t, os.WriteFile(notExe, script, 0o700)) //nolint:gosec // G306: as above.

	for _, bin := range []string{"", loose, otherBin, notExe} {
		_, err := StartChrome(t, ChromeOptions{Binary: bin, RunDir: t.TempDir()})
		require.Error(t, err, "%q is refused", bin)
		assert.NoFileExists(t, marker, "%q never ran", bin)
	}
	_, err := StartChrome(t, ChromeOptions{Binary: otherBin, RunDir: t.TempDir()})
	assert.ErrorContains(t, err, chromeForTestingID)
}

// A certificate error in a navigation the page starts, a link clicked to a
// server not pinned, is recorded as a certificate event (R180).
func TestAPageInitiatedCertificateErrorIsReported(t *testing.T) {
	chromeBinary(t)
	ca := newCA(t)
	unpinned := testServer(t, ca, http.HandlerFunc(plainPage))
	pinned := testServer(t, ca, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		servePage(w, "link", `<p><a id="away" href="`+unpinned.base+`/plain">away</a></p>`)
	}))
	c := launch(t, ca, []string{pinned.addr}, pinned.addr, unpinned.addr)
	ctx := testContext(t)
	b, err := c.Tab(ctx, "a", false)
	require.NoError(t, err)
	require.NoError(t, b.Navigate(ctx, pinned.base+"/link"))
	assert.Empty(t, b.Events(0, "certificate"))

	require.NoError(t, b.Click(ctx, "#away"))
	e := waitEvent(t, b, "certificate", 0, 15*time.Second, nil)
	d := detailOf(t, e)
	assert.Equal(t, unpinned.base+"/plain", d["url"])
	assert.Contains(t, d["error"], "ERR_CERT_AUTHORITY_INVALID")
}

// Only 127.0.0.1 resolves (R182): localhost and an address off the machine
// fail with ERR_NAME_NOT_RESOLVED, which is no certificate error, while
// 127.0.0.1 loads; and the launch carries the rule and no proxy.
func TestOnlyLoopbackResolves(t *testing.T) {
	chromeBinary(t)
	ca := newCA(t)
	s := testServer(t, ca, http.HandlerFunc(plainPage))
	c := launch(t, ca, []string{s.addr}, s.addr)
	assert.Contains(t, c.cmd.Args, "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1")
	assert.Contains(t, c.cmd.Args, "--no-proxy-server")
	ctx := testContext(t)
	b, err := c.Tab(ctx, "a", false)
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(s.addr)
	require.NoError(t, err)
	for _, u := range []string{"https://localhost:" + port + "/plain", "https://192.0.2.1/"} {
		err := b.Navigate(ctx, u)
		require.ErrorContains(t, err, "ERR_NAME_NOT_RESOLVED", u)
		require.NotErrorIs(t, err, ErrCertificate, u)
	}
	require.NoError(t, b.Navigate(ctx, s.base+"/plain"))
	assert.Equal(t, "plain", evalString(ctx, t, b, "document.title"))
	assert.Empty(t, b.Events(0, "certificate"), "a name that does not resolve is no certificate error")
}

// After a load, Close, then the complete NetLog shows no finding and at
// least one connect to the server's port (R185); before Close it cannot be
// read as complete.
func TestOffLoopbackPassesAfterALoad(t *testing.T) {
	chromeBinary(t)
	ca := newCA(t)
	s := testServer(t, ca, http.HandlerFunc(plainPage))
	c := launch(t, ca, []string{s.addr}, s.addr)
	ctx := testContext(t)
	b, err := c.Tab(ctx, "a", false)
	require.NoError(t, err)
	require.NoError(t, b.Navigate(ctx, s.base+"/plain"))
	_, err = c.OffLoopback()
	require.Error(t, err, "the NetLog is complete only once Chrome has exited")

	require.NoError(t, c.Close())
	findings, err := c.OffLoopback()
	require.NoError(t, err)
	assert.Empty(t, findings)
	_, connects, err := ReadNetLog(filepath.Join(c.runDir, "netlog.json"), []string{s.addr})
	require.NoError(t, err)
	assert.Positive(t, connects)
}

// Four windows are all visible at once (R182's
// --disable-backgrounding-occluded-windows); Hide minimises one's window,
// which hides its page for real, and Show makes it visible again.
func TestWindowsStayVisibleAndHideByMinimising(t *testing.T) {
	chromeBinary(t)
	ca := newCA(t)
	s := testServer(t, ca, http.HandlerFunc(plainPage))
	c := launch(t, ca, []string{s.addr}, s.addr)
	ctx := testContext(t)
	tabs := make([]*Tab, 4)
	for i := range tabs {
		b, err := c.Tab(ctx, "w"+strconv.Itoa(i), true)
		require.NoError(t, err)
		require.NoError(t, b.Navigate(ctx, s.base+"/plain"))
		tabs[i] = b
	}
	windows := map[int]bool{}
	for _, b := range tabs {
		windows[b.window] = true
		assert.Equal(t, "visible", evalString(ctx, t, b, "document.visibilityState"), b.name)
	}
	assert.Len(t, windows, 4, "each in a window of its own")

	hidden := tabs[1]
	require.NoError(t, hidden.Hide(ctx))
	assert.Equal(t, "hidden", evalString(ctx, t, hidden, "document.visibilityState"))
	for _, b := range []*Tab{tabs[0], tabs[2], tabs[3]} {
		assert.Equal(t, "visible", evalString(ctx, t, b, "document.visibilityState"), b.name)
	}
	require.NoError(t, hidden.Show(ctx))
	assert.Equal(t, "visible", evalString(ctx, t, hidden, "document.visibilityState"))
	states := []string{}
	for _, r := range records(hidden, "visibility", 0) {
		states = append(states, fmt.Sprint(r.Detail["state"]))
	}
	assert.Equal(t, []string{"hidden", "visible"}, states, "the recorder saw the page hide and show")

	require.NoError(t, tabs[3].Close(ctx))
	require.NoError(t, tabs[3].Close(ctx), "closing a closed tab")
	require.Error(t, tabs[3].Eval(ctx, "1", nil), "a closed tab")
	again, err := c.Tab(ctx, "w3", true)
	require.NoError(t, err, "its name is free again")
	require.NoError(t, again.Navigate(ctx, s.base+"/plain"))
	_, err = c.Tab(ctx, "w3", true)
	require.Error(t, err, "a name in use")
}

// The recorder (R183) records one start per document, with #board's order;
// Eval straight after Navigate answers in the new document; a reload the
// page itself makes gives a new start; the page's main world never sees the
// binding; and nothing of it shows as a violation or an issue.
func TestTheRecorderIsUnseenAndFollowsDocuments(t *testing.T) {
	chromeBinary(t)
	ca := newCA(t)
	mux := http.NewServeMux()
	board := func(order string) string {
		return `<div id="board" data-etag="&#34;` + order + `&#34;" data-order="` + order + `"><p>board</p></div>`
	}
	mux.HandleFunc("/a", func(w http.ResponseWriter, _ *http.Request) { servePage(w, "a", board("1.2")) })
	mux.HandleFunc("/b", func(w http.ResponseWriter, _ *http.Request) { servePage(w, "b", board("2.3")) })
	mux.HandleFunc("/reloads", func(w http.ResponseWriter, _ *http.Request) {
		servePage(w, "reloads", board("3.4"), "/reload.js")
	})
	mux.HandleFunc("/reload.js", func(w http.ResponseWriter, _ *http.Request) {
		serveScript(w, `if (!sessionStorage.getItem("reloaded")) { sessionStorage.setItem("reloaded", "1"); setTimeout(function () { location.reload(); }, 300); }`)
	})
	s := testServer(t, ca, mux)
	c := launch(t, ca, []string{s.addr}, s.addr)
	ctx := testContext(t)
	b, err := c.Tab(ctx, "a", false)
	require.NoError(t, err)

	require.NoError(t, b.Navigate(ctx, s.base+"/a"))
	assert.Equal(t, "/a", evalString(ctx, t, b, "location.pathname"))
	require.NoError(t, b.Navigate(ctx, s.base+"/b"))
	assert.Equal(t, "/b", evalString(ctx, t, b, "location.pathname"), "Eval answers in the new document")
	require.NoError(t, b.Navigate(ctx, s.base+"/reloads"))
	first := waitRecord(t, b, "start", 0, 10*time.Second, func(r rec) bool { return r.Path == "/reloads" })
	waitRecord(t, b, "start", first.Seq, 10*time.Second, func(r rec) bool { return r.Path == "/reloads" })
	starts := records(b, "start", 0)
	var got []string
	for _, r := range starts {
		got = append(got, r.Path+" "+fmt.Sprint(r.Detail["order"]))
	}
	assert.Equal(t, []string{"/a 1.2", "/b 2.3", "/reloads 3.4", "/reloads 3.4"}, got, "one start a document, the page's own reload's included")
	assert.Equal(t, "/reloads", evalString(ctx, t, b, "location.pathname"))
	mark := lastSeq(b)
	require.NoError(t, b.Reload(ctx))
	assert.Len(t, records(b, "start", mark), 1, "the driver's reload, a document of its own")
	assert.Equal(t, "/reloads", evalString(ctx, t, b, "location.pathname"))

	assert.Equal(t, "function", evalString(ctx, t, b, "typeof gateRecord"), "the gate world has the binding")
	var mainSees string
	require.NoError(t, b.evalMain(ctx, "typeof gateRecord", &mainSees))
	assert.Equal(t, "undefined", mainSees, "the page's main world does not")

	for _, e := range b.Events(0, "log") {
		assert.NotEqual(t, "violation", detailOf(t, e)["source"], "%s", e.Detail)
	}
	assert.Empty(t, b.Events(0, "issue"))
	assert.Empty(t, b.Events(0, "exception"))
}

// pollerPage polls its data three times by XHR, 200 ms apart.
const pollerJS = `(function () {
  var n = 0;
  function poll() {
    var x = new XMLHttpRequest();
    x.open("GET", location.pathname + "/data");
    x.onloadend = function () { if (++n < 3) setTimeout(poll, 200); };
    x.send();
  }
  poll();
})();`

// The inverted canary (R192's premise): a page whose polls are no-store is
// never restored from the back-forward cache, and Chrome says why; the same
// page with cacheable polls is restored, and Back returns on the restore,
// which fires no load. Each page is no-store itself, as the board is
// (web.Authenticated): Chrome 157 restores a cacheable page whatever its
// polls, and a no-store one unless a script of its received a no-store
// answer, so that the polls' header is all that differs.
func TestBackNeverRestoresAPageWhosePollsAreNoStore(t *testing.T) {
	chromeBinary(t)
	ca := newCA(t)
	mux := http.NewServeMux()
	for _, p := range []string{"/nostore", "/cacheable"} {
		mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			servePage(w, p, "<h1>"+p+"</h1>", "/poller.js")
		})
	}
	mux.HandleFunc("/poller.js", func(w http.ResponseWriter, _ *http.Request) { serveScript(w, pollerJS) })
	mux.HandleFunc("/nostore/data", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "data")
	})
	mux.HandleFunc("/cacheable/data", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = io.WriteString(w, "data")
	})
	mux.HandleFunc("/other", plainPage)
	s := testServer(t, ca, mux)
	c := launch(t, ca, []string{s.addr}, s.addr)
	ctx := testContext(t)
	b, err := c.Tab(ctx, "a", false)
	require.NoError(t, err)

	// visit loads path, waits for its third poll's answer, then leaves it
	// and goes back, returning the seq Back started after. The page left
	// may still record its own pageshow after that seq, as pageshow follows
	// the load Navigate returns on: each record is matched by its path.
	visit := func(path string) int {
		mark := lastSeq(b)
		require.NoError(t, b.Navigate(ctx, s.base+path))
		require.Eventually(t, func() bool {
			n := 0
			for _, e := range b.Events(mark, "response") {
				if d := detailOf(t, e); d["url"] == s.base+path+"/data" && d["status"] == float64(200) {
					n++
				}
			}
			return n == 3
		}, 10*time.Second, 20*time.Millisecond, "three polls answered on %s", path)
		require.NoError(t, b.Navigate(ctx, s.base+"/other"))
		mark = lastSeq(b)
		require.NoError(t, b.Back(ctx))
		assert.Equal(t, path, evalString(ctx, t, b, "location.pathname"), "back on %s", path)
		return mark
	}

	on := func(path string) func(rec) bool { return func(r rec) bool { return r.Path == path } }
	navigatedTo := func(path string) func(map[string]any) bool {
		return func(d map[string]any) bool { return d["main"] == true && d["url"] == s.base+path }
	}

	mark := visit("/nostore")
	waitRecord(t, b, "start", mark, 10*time.Second, on("/nostore"))
	shown := waitRecord(t, b, "pageshow", mark, 10*time.Second, on("/nostore"))
	assert.Equal(t, false, shown.Detail["persisted"], "a new document, not a restore")
	nav := waitEvent(t, b, "navigated", mark, 5*time.Second, navigatedTo("/nostore"))
	assert.Equal(t, "Navigation", detailOf(t, nav)["type"])
	e := waitEvent(t, b, "bfcache", mark, 10*time.Second, nil)
	assert.Contains(t, detailOf(t, e)["reasons"], "JsNetworkRequestReceivedCacheControlNoStoreResource")

	mark = visit("/cacheable")
	shown = waitRecord(t, b, "pageshow", mark, 10*time.Second, on("/cacheable"))
	assert.Equal(t, true, shown.Detail["persisted"], "restored from the back-forward cache")
	nav = waitEvent(t, b, "navigated", mark, 5*time.Second, navigatedTo("/cacheable"))
	assert.Equal(t, "BackForwardCacheRestore", detailOf(t, nav)["type"])
	assert.Empty(t, slices.DeleteFunc(records(b, "start", mark), func(r rec) bool { return r.Path != "/cacheable" }),
		"the same document, not a new one")
	assert.Empty(t, b.Events(mark, "bfcache"), "nothing kept it out of the cache")
}

// lastSeq is the seq of the tab's latest event, 0 before any.
func lastSeq(b *Tab) int {
	if es := b.Events(0); len(es) > 0 {
		return es[len(es)-1].Seq
	}
	return 0
}

// Enter, a keyDown with text, and Space each press a button; Tab and
// Shift+Tab move the focus; Escape and the arrows reach the page; Type
// fills a text field.
func TestKeysPressButtonsAndTypeText(t *testing.T) {
	chromeBinary(t)
	ca := newCA(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		servePage(w, "keys", `<button type="button" id="toggle" aria-pressed="false">Toggle</button>`+
			`<label for="field">Field</label><input id="field" name="field" type="text"><p id="last"></p>`, "/keys.js")
	})
	mux.HandleFunc("/keys.js", func(w http.ResponseWriter, _ *http.Request) {
		serveScript(w, `(function () {
  var t = document.getElementById("toggle"), last = document.getElementById("last");
  t.addEventListener("click", function () { t.setAttribute("aria-pressed", String(t.getAttribute("aria-pressed") !== "true")); });
  document.addEventListener("keydown", function (e) { last.dataset.key = e.key; });
})();`)
	})
	s := testServer(t, ca, mux)
	c := launch(t, ca, []string{s.addr}, s.addr)
	ctx := testContext(t)
	b, err := c.Tab(ctx, "a", true)
	require.NoError(t, err)
	require.NoError(t, b.Navigate(ctx, s.base+"/keys"))
	active := func() string { return evalString(ctx, t, b, "document.activeElement.id") }
	pressed := func() string {
		return evalString(ctx, t, b, `document.getElementById("toggle").getAttribute("aria-pressed")`)
	}

	require.NoError(t, b.Key(ctx, "Tab"))
	require.Equal(t, "toggle", active())
	require.NoError(t, b.Key(ctx, "Enter"))
	assert.Equal(t, "true", pressed(), "Enter presses the button")
	require.NoError(t, b.Key(ctx, "Space"))
	assert.Equal(t, "false", pressed(), "Space presses it again")

	require.NoError(t, b.Key(ctx, "Tab"))
	require.Equal(t, "field", active())
	require.NoError(t, b.Type(ctx, "hello gate"))
	assert.Equal(t, "hello gate", evalString(ctx, t, b, `document.getElementById("field").value`))
	require.NoError(t, b.Key(ctx, "Shift+Tab"))
	assert.Equal(t, "toggle", active())
	focus := records(b, "focusin", 0)
	require.NotEmpty(t, focus)
	assert.Equal(t, "toggle", focus[len(focus)-1].Detail["id"], "the recorder saw the focus move")

	for _, k := range []string{"Escape", "ArrowDown", "ArrowUp", "ArrowLeft", "ArrowRight"} {
		require.NoError(t, b.Key(ctx, k))
		assert.Equal(t, k, evalString(ctx, t, b, `document.getElementById("last").dataset.key`))
	}
	require.Error(t, b.Key(ctx, "F13"), "a key it does not know")
}

// Emulation darkens the media, sets the browser's font size and a phone's
// width; a screenshot writes a PNG; the zero Emulation clears it all; and
// the browser's cookies can be cleared.
func TestEmulationAndScreenshots(t *testing.T) {
	chromeBinary(t)
	ca := newCA(t)
	s := testServer(t, ca, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cookie" {
			http.SetCookie(w, &http.Cookie{Name: "probe", Value: "1", Path: "/", Secure: true, SameSite: http.SameSiteLaxMode})
		}
		plainPage(w, r)
	}))
	c := launch(t, ca, []string{s.addr}, s.addr)
	assert.True(t, strings.HasPrefix(c.Version(), "Chrome/"), "the browser's product: %q", c.Version())
	ctx := testContext(t)
	b, err := c.Tab(ctx, "a", true)
	require.NoError(t, err)
	require.NoError(t, b.Navigate(ctx, s.base+"/cookie"))
	assert.Equal(t, "probe=1", evalString(ctx, t, b, "document.cookie"))
	require.NoError(t, c.ClearCookies(ctx))
	assert.Empty(t, evalString(ctx, t, b, "document.cookie"), "the cookie is gone")
	require.NoError(t, b.Navigate(ctx, s.base+"/plain"))

	require.NoError(t, b.Emulate(ctx, Emulation{Width: 390, Height: 844, DPR: 3, Mobile: true, Dark: true, FontSize: 32}))
	var dark bool
	require.NoError(t, b.Eval(ctx, "matchMedia('(prefers-color-scheme: dark)').matches", &dark))
	assert.True(t, dark)
	assert.Equal(t, "32px", evalString(ctx, t, b, "getComputedStyle(document.documentElement).fontSize"))
	var width int
	require.NoError(t, b.Eval(ctx, "innerWidth", &width))
	assert.Equal(t, 390, width)

	shot := filepath.Join(t.TempDir(), "screenshots", "phone-dark.png")
	require.NoError(t, b.Screenshot(ctx, shot))
	raw, err := os.ReadFile(shot)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")), "a PNG")

	require.NoError(t, b.Emulate(ctx, Emulation{}))
	require.NoError(t, b.Eval(ctx, "matchMedia('(prefers-color-scheme: dark)').matches", &dark))
	assert.False(t, dark)
	assert.Equal(t, "16px", evalString(ctx, t, b, "getComputedStyle(document.documentElement).fontSize"))
	require.NoError(t, b.Eval(ctx, "innerWidth", &width))
	assert.Equal(t, 1280, width)
}

// The board fixture's region markup: the update control each region begins
// with, as the board's.
const fixtureUpdate = `<button type="button" class="update" hidden>New entries: update</button>`

// fixtureLayoutRegions are the layout's two regions, the kill switch's and
// the standby's, which stay as they are.
func fixtureLayoutRegions() string {
	return `<header id="switch" class="switch" data-v="s1" tabindex="-1">` + fixtureUpdate +
		`<p class="state">Kill switch: off; it was never turned on</p></header>` +
		`<section id="standby" data-v="b1" tabindex="-1">` + fixtureUpdate + `</section>`
}

// fixturePageRegions are the page's five regions as of answer n: the
// header, the summary, whose round is n, and the feed change with each.
func fixturePageRegions(n int) string {
	return fmt.Sprintf(`<section id="board-header" data-v="h%d" tabindex="-1" data-stale="reconnecting, last updated at 12:%02d">%s<p>Item a, answer %d</p></section>`, n, n, fixtureUpdate, n) +
		fmt.Sprintf(`<section id="summary" data-v="m%d" tabindex="-1" data-round="%d">%s<p>Round %d of the summary.</p></section>`, n, n, fixtureUpdate, n) +
		`<section id="strip" data-v="t1" tabindex="-1">` + fixtureUpdate + `</section>` +
		`<section id="hypotheses" data-v="y1" tabindex="-1">` + fixtureUpdate + `</section>` +
		fmt.Sprintf(`<section id="feed" data-v="f%d" tabindex="-1">%s<details id="feed-list" open><summary><h2>Feed</h2></summary><ol class="feed"><li id="line-%d">line %d</li></ol></details></section>`, n, fixtureUpdate, n, n)
}

// boardFixture is a board-shaped page at /items/a, under the board's
// policy, running the repository's own live.js, and its regions at
// /items/a/board, whose order rises with each answer and whose regions
// change. The page carries what live.js reads: #board with its ETag and
// order and the bar first; #live-state, #live-pause, #feed-list, #announce
// and #live-signin; #summary with its round and a p; and the seven regions,
// each with data-v.
type boardFixture struct {
	live []byte
	mu   sync.Mutex
	n    int
}

func (f *boardFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	switch r.URL.Path {
	case "/static/live.js":
		serveScript(w, string(f.live))
	case "/items/a":
		h.Set("Content-Security-Policy", web.BoardPolicy)
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8">`+
			`<meta name="viewport" content="width=device-width, initial-scale=1"><title>Item a</title></head><body>`+
			fixtureLayoutRegions()+`<main><h1>Item a</h1>`+
			`<div id="board" data-etag="&#34;e1&#34;" data-order="1.1">`+
			`<div class="live" data-live="live"><p class="note" id="live-state">This page does not refresh itself: reload it to see more.</p>`+
			`<button type="button" id="live-pause" aria-pressed="false" hidden>Pause updates</button>`+
			`<a id="live-signin" href="/auth/signin" hidden>Sign in again</a></div>`+
			fixturePageRegions(1)+
			`<div id="announce" class="sr-only" aria-live="polite"></div></div>`+
			`<script src="/static/live.js" defer></script></main></body></html>`)
	case "/items/a/board":
		f.mu.Lock()
		f.n++
		n := f.n + 1
		f.mu.Unlock()
		h.Set("Content-Security-Policy", web.BoardPolicy)
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		h.Set("ETag", fmt.Sprintf(`"e%d"`, n))
		h.Set("X-Board-Order", fmt.Sprintf("%d.%d", n, n))
		_, _ = fmt.Fprintf(w, "<!doctype html>\n"+`<div id="board" data-etag="&#34;e%d&#34;" data-order="%d.%d">%s%s</div>`,
			n, n, n, fixtureLayoutRegions(), fixturePageRegions(n))
	default:
		http.NotFound(w, r)
	}
}

// domRegions are the page's regions' data-v, read in the gate world.
func domRegions(ctx context.Context, t *testing.T, b *Tab) map[string]string {
	t.Helper()
	var v map[string]string
	require.NoError(t, b.Eval(ctx, `(() => { const v = {}; for (const id of `+mustJSON(t, regionIDs)+`) { const e = document.getElementById(id); v[id] = e ? (e.dataset.v || "") : "absent"; } return v; })()`, &v))
	return v
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

// orderParts reads an order, "<record seq>.<board seq>".
func orderParts(t *testing.T, order string) (record, board int) {
	t.Helper()
	a, b, ok := strings.Cut(order, ".")
	require.True(t, ok, "order %q", order)
	r, err := strconv.Atoi(a)
	require.NoError(t, err)
	s, err := strconv.Atoi(b)
	require.NoError(t, err)
	return r, s
}

// ReplayOlder answers the next poll with an earlier 200 of a strictly lower
// order whose regions differ from the page's (R190), and the repository's
// live.js drops it: no region changes, the bar still reads live, and the
// next real answer applies; and nothing of it shows as a security log.
func TestReplayOlderIsDroppedByLiveJS(t *testing.T) {
	chromeBinary(t)
	live, err := os.ReadFile(filepath.Join("..", "..", "..", "internal", "web", "pages", "static", "live.js"))
	require.NoError(t, err)
	require.Contains(t, string(live), `var names = ["`+strings.Join(regionIDs, `", "`)+`"];`, "the harness's regions are live.js's")
	ca := newCA(t)
	s := testServer(t, ca, &boardFixture{live: live})
	c := launch(t, ca, []string{s.addr}, s.addr)
	ctx := testContext(t)
	b, err := c.Tab(ctx, "a", true)
	require.NoError(t, err)
	require.NoError(t, b.Navigate(ctx, s.base+"/items/a"))
	start := waitRecord(t, b, "start", 0, 10*time.Second, nil)
	assert.Equal(t, "1.1", start.Detail["order"], "the document's own order")
	waitRecord(t, b, "bar", 0, 10*time.Second, func(r rec) bool { return r.Detail["text"] == "live" })

	// Two answers applied: the page is at 3.3.
	waitEvent(t, b, "body", 0, 30*time.Second, func(d map[string]any) bool { return d["order"] == "3.3" })
	require.Eventually(t, func() bool { return domRegions(ctx, t, b)["board-header"] == "h3" }, 10*time.Second, 50*time.Millisecond)
	before := domRegions(ctx, t, b)

	replay, err := b.ReplayOlder(ctx)
	require.NoError(t, err)
	assert.Equal(t, "3.3", replay.PageOrder)
	gotRecord, gotBoard := orderParts(t, replay.Replayed)
	pageRecord, pageBoard := orderParts(t, replay.PageOrder)
	assert.True(t, gotRecord <= pageRecord && gotBoard <= pageBoard && (gotRecord < pageRecord || gotBoard < pageBoard),
		"the replayed order %s is strictly below the page's %s", replay.Replayed, replay.PageOrder)
	require.NotEmpty(t, replay.Differing, "a region whose data-v differs, so that applying it would show")
	for _, id := range replay.Differing {
		assert.Contains(t, regionIDs, id)
	}
	rp := b.Events(0, "replay")
	require.Len(t, rp, 1)
	assert.Equal(t, replay.Replayed, detailOf(t, rp[0])["replayed"])
	from, ok := detailOf(t, rp[0])["from"].(float64)
	require.True(t, ok, "the replay names the body it replayed")
	var source map[string]any
	for _, e := range b.Events(0, "body") {
		if e.Seq == int(from) {
			source = detailOf(t, e)
		}
	}
	require.NotNil(t, source, "body event %v", from)
	assert.Equal(t, replay.Replayed, source["order"])
	assert.NotEqual(t, true, source["replayed"], "an answer the server gave, not a replay")

	// live.js has taken the replayed answer once it writes the bar again.
	bar := waitRecord(t, b, "bar", rp[0].Seq, 15*time.Second, nil)
	assert.Equal(t, "live", bar.Detail["text"], "the bar still reads live")
	assert.Equal(t, before, domRegions(ctx, t, b), "no region changed")
	for _, r := range records(b, "region", rp[0].Seq) {
		assert.Greater(t, r.Seq, bar.Seq, "no region moved in for the replayed answer: %v", r.Detail)
	}
	assert.Empty(t, records(b, "announce", rp[0].Seq), "nor was its round announced")
	assert.Equal(t, "live", evalString(ctx, t, b, `document.getElementById("live-state").textContent`))

	// The next real answer applies.
	waitEvent(t, b, "body", rp[0].Seq, 30*time.Second, func(d map[string]any) bool { return d["order"] == "4.4" && d["replayed"] != true })
	require.Eventually(t, func() bool { return domRegions(ctx, t, b)["board-header"] == "h4" }, 10*time.Second, 50*time.Millisecond)
	assert.NotEmpty(t, records(b, "region", rp[0].Seq), "the recorder saw it move in")

	replayedBody := waitEvent(t, b, "body", rp[0].Seq, 5*time.Second, func(d map[string]any) bool { return d["replayed"] == true })
	assert.Equal(t, replay.Replayed, detailOf(t, replayedBody)["order"], "the replayed answer's body is kept, marked as replayed")

	for _, e := range b.Events(0, "log") {
		assert.NotEqual(t, "security", detailOf(t, e)["source"], "%s", e.Detail)
	}
	assert.Empty(t, b.Events(0, "exception"))
	bodies, err := os.ReadDir(filepath.Join(c.runDir, "bodies"))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(bodies), 4, "each 200 poll's body is kept")

	// Every event is in events.jsonl too, in order (R184).
	events := b.Events(0)
	raw, err := os.ReadFile(filepath.Join(c.runDir, "events.jsonl"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), len(events))
	for i, e := range events {
		var line struct {
			Tab  string `json:"tab"`
			Seq  int    `json:"seq"`
			Kind string `json:"kind"`
		}
		require.NoError(t, json.Unmarshal([]byte(lines[i]), &line))
		require.Equal(t, "a", line.Tab)
		require.Equal(t, e.Seq, line.Seq)
		require.Equal(t, e.Kind, line.Kind)
	}
}

// Close leaves nothing running (R182): the crash handlers are recorded, by
// their executable and the run's own resolved --database, with no metrics
// directory outside the run; after Close neither Chrome's group nor a
// handler is alive; and a browser whose pipe alone closes exits by itself.
func TestCloseLeavesNothingRunning(t *testing.T) {
	bin := chromeBinary(t)
	runDir := t.TempDir()
	c, err := StartChrome(t, ChromeOptions{Binary: bin, RunDir: runDir})
	require.NoError(t, err)
	resolved, err := filepath.EvalSymlinks(runDir)
	require.NoError(t, err)
	handlers := c.recorded()
	require.NotEmpty(t, handlers, "at least one crash handler is recorded")
	for _, h := range handlers {
		assert.Contains(t, h.args, "--database="+filepath.Join(resolved, "crash"), "the resolved run directory: %q", h.args)
		for _, a := range h.args {
			if dir, ok := strings.CutPrefix(a, "--metrics-dir="); ok {
				assert.True(t, strings.HasPrefix(dir, resolved+string(filepath.Separator)), "a metrics directory outside the run: %s", dir)
			}
		}
	}
	require.NoError(t, c.Close())
	require.ErrorIs(t, syscall.Kill(-c.pgid, 0), syscall.ESRCH, "Chrome's process group is gone")
	for _, h := range handlers {
		require.ErrorIs(t, syscall.Kill(h.pid, 0), syscall.ESRCH, "crash handler %d is gone", h.pid)
	}
	require.NoError(t, c.Close(), "Close is idempotent")

	c, err = StartChrome(t, ChromeOptions{Binary: bin, RunDir: t.TempDir()})
	require.NoError(t, err)
	handlers = c.recorded()
	require.NotEmpty(t, handlers)
	require.NoError(t, c.conn.close())
	select {
	case <-c.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("Chrome still runs 10 s after its pipe closed")
	}
	require.NoError(t, c.Close())
	require.ErrorIs(t, syscall.Kill(-c.pgid, 0), syscall.ESRCH)
	for _, h := range handlers {
		require.ErrorIs(t, syscall.Kill(h.pid, 0), syscall.ESRCH, "crash handler %d is gone", h.pid)
	}
}
