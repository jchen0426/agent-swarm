# Plan 9a-3: The Unattended Browser Gate — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Draft v2.1, written against HEAD a77d9a0. It answers:<br>- the Opus plan review v1 (`plan/2026-11-12-plan-9a3-plan-review-v1.md`, 2C/9I/9M);<br>- the scoped Opus re-review v2 (`plan/2026-11-12-plan-9a3-plan-rereview-v2.md`, 0C/7I/5M). See "Plan review".<br>- the author's two answers of 2026-11-12: **Chrome for Testing** as the throwaway browser, and the **light scope**: a throwaway browser and a control interface, through which the controller walks the checklist.<br>The approach was approved on 2026-11-12 ("好，做"), and the scope and the browser are the author's answers. Earlier drafts are `plan/backups/plan9a3/plan-v1.md` and `plan-v2.md`. For execution subagent-driven on main. |
| Goal | A UI plan's browser gate needs no person at the browser. `make browser-gate` serves the gate's world with a throwaway Chrome for Testing, already signed in, that trusts only the environment's own two certificates. The controller walks the plan's checklist through a control socket (tabs, navigation, keys, clicks, emulation, hiding, screenshots, the world's state and the browser's own evidence), then ends the run, so that its own checks give the verdict. |
| Architecture | The harness gains a launcher for Chrome for Testing and a small CDP client over `--remote-debugging-pipe`, on the standard library. `TestBrowserGateEnvironment` gains, when a browser is named, the throwaway browser and a control server on a Unix socket (HTTP and JSON), which the controller drives with `curl`. |
| Tech stack | Go 1.26; the e2e harness (testcontainers, Ryuk off); Chrome for Testing 157.0.8103.78, mac-arm64, headless, so the gate runs on macOS on Apple silicon; the Chrome DevTools Protocol. No new module dependency. `golang.org/x/net/html`, of a module already required, reads the poll bodies. |
| Spec | No product change. The plan serves the gate of design D15 (`plan/2026-11-10-plan-9a2-live-board-design.md`) and the checklist under "The browser gate" in `plan/2026-11-10-plan-9a2-live-board.md`. That checklist stays the authority on what each item means. "The gate's walk" below amends how it is run, and what it can show in Chrome. |
| Depends on | Plan 9a-2, executed (7bcd4db..a77d9a0). |
| Size | About 3–3.5 raw days, recorded only (R-f).<br>- Calendar: guess about half a day to a day, plus the first walk (about an hour). Stop line: 5 calendar days.<br>- Spend: the author was told about $12–20. The re-review puts it at $20–30 once its fixes are in. The stop line stays at $40, the one the author saw. If it is reached, the controller stops and asks.<br>See "Size". |

## Why

Plan 9a-2's gate waited about 5 hours on two clicks through certificate warnings, which the browser tool cannot reach, and two environments expired meanwhile. The gate still found what ten task reviews had not: a Pause button with no pressed look. So UI plans keep a real-browser gate, and this plan takes the person out of it.

## What the spikes showed (2026-11-12)

Two throwaway programs drove Chrome 157.0.8103.78 headless, in throwaway profiles, over `--remote-debugging-pipe`, and so did the reviews' own probes. The programs are kept at `plan/backups/plan9a3/spike/` and `spike2/` (git-ignored).
- **Certificates and the channel.**
  - Without a pin, navigation fails with `net::ERR_CERT_AUTHORITY_INVALID`.
  - With `--ignore-certificate-errors-spki-list` set to one server's leaf SPKI, that server loads, and a sibling under the same CA stays refused. A CA's SPKI helps only a server that presents the CA; the harness's servers present the leaf alone.
  - A certificate error in a page-initiated navigation shows as a Document `loadingFailed` with `net::ERR_CERT_AUTHORITY_INVALID`, and a `chrome-error://` commit.
  - Chrome exits by itself once its pipe closes.
- **Hiding and windows.** Minimising a window hides its page for real (`visibilitychange`). Overlapping windows count as occluded and hide too, unless `--disable-backgrounding-occluded-windows` is set. A tab created without a window lands in the last active window, and hides the tab there.
- **Input and emulation.** Tab keys move the focus, with `:focus-visible`. Enter presses a button only as a `keyDown` with text; Space presses it either way. Dark media, `Page.setFontSizes`, a 390 px viewport, screenshots and clearing cookies all work.
- **The recorder.** A binding exposed only to an isolated world `gate` records page events, unseen by the page's main world. A document being left never reports `pagehide`.
- **The back-forward cache.** A `no-store` page that makes no requests of its own is restored. The board is never restored: its polls are `no-store` too, and Chrome gives `JsNetworkRequestReceivedCacheControlNoStoreResource` among its reasons. live.js's restore branch is unreachable in Chrome while the polls stay `no-store`.
- **The person's machine.**
  - The installed Google Chrome, even in a throwaway profile, starts its crash handler with the person's own Chrome directory as its crash database and metrics directory, outside Chrome's process group. The first spike's four launches and the first review's probes did this. That day, the person's crash settings file and the two crash-handler metrics files were rewritten (the person's Chrome also writes one of these), and no crash report was added.
  - Chrome for Testing, with `BREAKPAD_DUMP_LOCATION` set to the run's directory, starts its handlers with that directory as their database and no metrics directory. The person's Chrome directory is left unchanged. The handlers' `--database` is the resolved path, so `/tmp` becomes `/private/tmp`.
  - The first spike ran without a resolver rule, so the throwaway browser's own background requests reached Google, as a fresh Chrome's do. They carried its locale and country, and no account.
- **The network.**
  - `--host-resolver-rules=MAP * ~NOTFOUND` blocks IP literals, `127.0.0.1` included. With `, EXCLUDE 127.0.0.1`, the environment loads, while `localhost` and `192.0.2.1` fail with `ERR_NAME_NOT_RESOLVED`.
  - With that rule, the NetLog showed the browser's own requests failing to resolve, no TCP connect off loopback, and no UDP bytes sent. The only off-loopback sockets were UDP connects to a public IPv6 address, which sent no bytes: a route probe.
  - A proxy takes requests past the resolver rule, as a proxied request never consults it. `--no-proxy-server` closes that path.
  - The NetLog is one event per line. Mid-run it lags; it is complete only once Chrome has exited.
  - The page-level network events never show the browser's own requests.

## Rulings

These are numbered after Plan 9a-2's R177. Each gives its cost if wrong.

- **R178 (the light scope, the author's).** There is no scripted walk. The controller walks the plan's checklist through the control socket, records what it measures, and ends the run. Scripted checks join later plans one at a time, on the same client, where a regression is worth guarding. *Cost:* each gate costs the controller's session about an hour of tool calls, outside the spend ledger and not measured by it. Nothing re-runs by itself.
- **R179 (Chrome for Testing, the author's).**
  - It is pinned at 157.0.8103.78, mac-arm64, at `~/Library/Caches/agentswarm/chrome-for-testing/<version>/`.
  - It was downloaded once, on 2026-11-12, from the official bucket (`storage.googleapis.com/chrome-for-testing-public`) over HTTPS. Its MD5 matched the bucket's own `x-goog-hash`.
  - It is ad-hoc signed, with no Developer ID, so its provenance is the bucket alone.
  - The Makefile names the version, and `AGENTSWARM_BROWSER_GATE_CHROME` overrides the binary.
  - `StartChrome` refuses any bundle whose `CFBundleIdentifier` is not `com.google.chrome.for.testing`, so the person's own Chrome is never launched (review C2; re-review M3).

  *Cost:* the pinned browser falls behind the stable channel. Moving it takes one download, done by the person, and one Makefile line.
- **R180 (trust: pin what the servers present).**
  - `PinsOf` dials the stub and the IdP on loopback, with a TLS client that verifies the chain against the harness CA (read from the file `Env.CA()` names). It returns each presented leaf's SPKI pin, which goes to `--ignore-certificate-errors-spki-list`.
  - Never `--ignore-certificate-errors`, never `Security.setIgnoreCertificateErrors`.
  - A certificate error on any navigation is recorded as kind `certificate` and fails the environment's run: the driver's own navigation, a page's reload or link, a redirect, or a form's post. It shows as a Document `loadingFailed` with `net::ERR_CERT_*`, or a `chrome-error://` commit.

  *Cost:* if Chrome drops the switch, the gate stops until this ruling is revisited.
- **R181 (the channel).** `--remote-debugging-pipe`: NUL-terminated JSON on file descriptors 3 and 4, with flat sessions. There is no TCP debugging port. The client is on the standard library, and `go.mod` does not change. *Cost:* the client is ours to keep.
- **R182 (the profile and its isolation).**
  - The run directory and the profile are resolved with `filepath.EvalSymlinks` before the launch. The profile is a `t.TempDir()`.
  - Flags: `--headless`, `--user-data-dir=<dir>`, `--remote-debugging-pipe`, `--ignore-certificate-errors-spki-list=<pins>`, `--no-proxy-server`, `--no-first-run`, `--no-default-browser-check`, `--disable-background-networking`, `--disable-component-update`, `--disable-sync`, `--disable-default-apps`, `--disable-extensions`, `--disable-client-side-phishing-detection`, `--disable-backgrounding-occluded-windows`, `--metrics-recording-only`, `--mute-audio`, `--use-mock-keychain`, `--window-size=1280,900`, `--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1` and `--log-net-log=<run dir>/netlog.json`.
  - The environment sets `BREAKPAD_DUMP_LOCATION=<run dir>/crash`.
  - Chrome runs in its own process group. Its crash handlers run outside it, with parent 1 and a group of their own.
  - While Chrome runs, `StartChrome` records the handlers' pids: the processes whose executable is the bundle's own `chrome_crashpad_handler` and whose arguments hold exactly `--database=<resolved run dir>/crash`.
  - `Close` sends `Browser.close`, then waits up to 5 s for Chrome and those pids. It then kills only those pids and Chrome's process group.

  *Cost:* none known. Spike 2 ran these flags but `--no-proxy-server`, and the re-review ran that one.
- **R183 (the recorder).** In every new document, in an isolated world `gate`, through a binding exposed to that world alone, each record stamped with the page's `Date.now()`:
  - the document's start, with `#board`'s `data-order` and `data-etag`;
  - `visibilitychange`, and `pageshow` with `persisted`;
  - `focusin`'s target;
  - each write to `#announce`, counted by a MutationObserver;
  - the bar's text;
  - each region that live.js replaces or updates in place.

  It writes to no DOM sink. *Cost:* a recorder bug reads as a page bug. Task 1's tests pin the recorder against pages whose behaviour is known.
- **R184 (the evidence).** Everything is appended to `<run dir>/events.jsonl`, and is readable through the socket:
  - each request, and its response's headers;
  - each `200` poll's body, through `Network.getResponseBody`, written to `<run dir>/bodies/<tab>-<seq>.html`. Its event keeps the size, `#board`'s `data-order`, `#summary`'s `data-round`, and each region's `data-v`, read with `golang.org/x/net/html`;
  - console and log entries, exceptions, and `Audits` issues;
  - `Page.backForwardCacheNotUsed`'s reasons and `frameNavigated`'s types;
  - the recorder's records;
  - each control command that acts or evaluates, as kind `control` (re-review M3).

  *Cost:* none.
- **R185 (off loopback fails).** The NetLog is read line by line, after `Close`, when it is complete. The run fails on any of these:
  - a TCP connect to any address and port but the stub's and the IdP's;
  - any `UDP_BYTES_SENT`;
  - a URL request to another host that did not end in `ERR_NAME_NOT_RESOLVED`;
  - no connect at all to the environment's ports, a positive control ("saw no connects").

  The socket's `/netlog` runs the same check mid-run, on a file that lags, and its answer says so. *Cost:* none.
- **R186 (the control socket).**
  - HTTP over a Unix socket, `<run dir>/control.sock`, at most 103 bytes long, in a 0700 directory, with JSON in and out. Commands act only on the throwaway browser and the world.
  - `/tab` and `/navigate` open only the stub's and the IdP's origins, and `about:blank`. A screenshot's name matches `^[a-z0-9-]{1,40}$`.
  - The world's faults stay as the environment prints them: signals to the instances' pids, `docker pause`, and the session key's files. `SIGSTOP` and `SIGCONT` of the standby are added (R189).
  - `GET /world` gives the instances, their pids, who leads, each lease change with its time, the database's container, the key files, and the deliveries with their times.
  - `POST /end` ends the run at once, so that the end checks and the cleanups run.
  - The controller writes to no DOM sink through `/eval`, and each `/eval` is recorded (R184).

  *Cost:* the socket is reachable by every process of the person's user, not by the test process alone, as the pipe is. It opens only the environment's origins.
- **R187 (the run directory).** `.browser-gate/<UTC time>/` in the repository, which `.gitignore` gains. It holds `events.jsonl`, `bodies/`, `netlog.json`, `screenshots/`, `chrome.log`, `crash/` and `control.sock`. *Cost:* none.
- **R188 (midnight dropped, review C1).** No zone puts midnight inside a short walk for most start times, and the feed's lines show only the time. The 9a-2 residual stays as written: a spanning feed reads its days by order alone. *Cost:* none beyond the residual.
- **R189 ("reconnecting" after an instance's stop).**
  - The environment follows both instances' logs. It records each "lease taken", "lease released" and "lease lost" with its time, and `/world` reports them (re-review I4).
  - The walk stops an instance before the database fault, which can move the lease.
  - The steps: `SIGSTOP` to the standby `/world` names; `SIGTERM` to the leader it names; the window with no leader timed from that leader's "lease released"; `SIGCONT` 20 s later.
  - The environment's cleanup sends `SIGCONT` to every instance and unpauses the database.
  - Path 2, a fresh standby taking over at once, is not run, as 9a-2's gate saw it.

  *Cost:* about two minutes of the walk.
- **R190 (a lower order, review I5; re-review I5).**
  - `ReplayOlder` arms `Fetch` for the tab's next poll. It answers that poll with an earlier recorded `200`: the answer's headers, with `Content-Type: text/html; charset=utf-8`, and its body. The body's `data-order` is strictly below the page's last applied order, and its regions differ in some `data-v`. The last applied order is the latest `200`'s, or the document's own (R183).
  - It then disables `Fetch`, and returns and records, as kind `replay`, the replayed order, the page's order, and the regions that differ.
  - The controller arms it just before an event lands, so that the next real answer is a `200`.

  *Cost:* the answer is real but replayed, not an instance's lag; live.js drops either the same way (`below`).
- **R191 (what the console check sets aside).** It fails on `javascript`, `security` and `violation` log entries, policy issues, exceptions, and console errors from the pages' scripts. It does not fail on `network` entries: the favicon's `404` and the faults' 401, 502 and 503 are expected. The favicon goes to Plan 9b as a minor, since the pages declare no icon. *Cost:* none.
- **R192 (Safari and the iPhone).** They stay a residual (Plan 9a-2's Decision A). No iOS Simulator runtime is installed, and one is about 8 GB; see "Options for the author". *Cost:* the same as 9a-2's residual.

## Global Constraints

- Code and comments are in English.
- No product code changes. `internal/` is untouched.
- The CDP client uses the standard library. `go.mod` and `go.sum` do not change.
- Only Chrome for Testing is launched, and never the person's Chrome. Never touch the person's Chrome profile, keychain or system settings.
- None of the following: a TCP debugging port, `--ignore-certificate-errors`, `Security.setIgnoreCertificateErrors`, `Page.setBypassCSP`, or a write to a DOM sink by the driver or the controller.
- Everything stays on loopback: `--no-proxy-server`, the resolver rule, and R185. The environment's checks stay: Ryuk off, and Postgres on 127.0.0.1.
- `make lint` stays clean.
- `make test` and `make test-e2e` run what they ran. Every new test that needs the browser skips without `AGENTSWARM_BROWSER_GATE_CHROME`. The CDP client's and the NetLog reader's own tests need no browser, and always run.
- Tests that bind a Unix socket do so in a short private directory under `/tmp`, as macOS allows 103 bytes.
- Commits carry no Co-Authored-By line, and are staged by path.

## Review Focus

1. **The person's machine is untouched.** The person's Chrome is never launched. The crash database is the run's own. No proxy is used, and no connect leaves loopback. The keychain is mocked. The socket opens only the environment.
2. **Evidence that cannot fail by design.** Each check states what would make it fail: the inverted back-forward canary, the replay's strictly lower order, and R185's positive control.
3. **The CDP client's navigation shapes.** The gate world's context for each document, a restore's old context ids, and page-initiated navigations with their certificate errors.
4. **A browser or crash handler left behind** after a failed or killed run, found by exact executable and argument, never by substring.
5. **Trust wider than the environment.** The pins are the presented leaves; a sibling under the same CA is refused.

## Tasks

### Task 1: Chrome for Testing and CDP in the harness

**Files:**
- Create: `test/e2e/harness/cdp.go`, `test/e2e/harness/chrome.go` and `test/e2e/harness/netlog.go`.
- Test:
  - `test/e2e/harness/cdp_internal_test.go` and `test/e2e/harness/netlog_internal_test.go`, which need no browser;
  - `test/e2e/harness/chrome_internal_test.go`, skipped without `AGENTSWARM_BROWSER_GATE_CHROME`. It is white-box, as it needs the main world and the pipe.

**Interfaces (Produces):**
```go
// PinsOf dials each loopback address, verifies the presented chain against
// pool, and returns each presented leaf's SPKI pin: SHA-256, base64 (R180).
func PinsOf(pool *x509.CertPool, addrs ...string) ([]string, error)

type ChromeOptions struct {
	Binary  string   // AGENTSWARM_BROWSER_GATE_CHROME (R179)
	Pins    []string // R180
	RunDir  string   // R187: netlog.json, chrome.log, crash/ and bodies/ go here
	Allowed []string // host:port of every connect R185 allows
}

// StartChrome launches R182's throwaway browser and registers Close with t.
// It refuses a bundle that is not Chrome for Testing (R179).
func StartChrome(t testing.TB, o ChromeOptions) (*Chrome, error)
func (c *Chrome) Version() string
func (c *Chrome) ClearCookies(ctx context.Context) error
func (c *Chrome) Close() error // idempotent
// OffLoopback reads the run's NetLog after Close: R185's findings, each one
// line.
func (c *Chrome) OffLoopback() ([]string, error)

// Tab opens a page target, in a window of its own when newWindow, attached
// in a flat session, with R183's recorder and R184's evidence on.
func (c *Chrome) Tab(ctx context.Context, name string, newWindow bool) (*Tab, error)

// ErrCertificate is a navigation refused for its certificate (R180).
var ErrCertificate error

// Emulation is a tab's emulated device; the zero value clears it.
type Emulation struct {
	Width, Height int
	DPR           float64
	Mobile, Dark  bool
	FontSize      int // the browser's standard font size, px
}

// Replay is what ReplayOlder replayed (R190).
type Replay struct {
	Replayed, PageOrder string
	Differing           []string // region ids whose data-v differ
}

func (b *Tab) Navigate(ctx context.Context, url string) error
func (b *Tab) Back(ctx context.Context) error // waits for a load or a restore
func (b *Tab) Reload(ctx context.Context) error
func (b *Tab) Eval(ctx context.Context, expr string, out any) error // in the gate world; waits for its context
func (b *Tab) Key(ctx context.Context, key string) error            // Tab, Shift+Tab, Enter (a keyDown with text), Space, Escape, arrows
func (b *Tab) Type(ctx context.Context, text string) error          // Input.insertText
func (b *Tab) Click(ctx context.Context, selector string) error     // the centre of the element's box
func (b *Tab) Emulate(ctx context.Context, e Emulation) error
func (b *Tab) Hide(ctx context.Context) error // minimise its window
func (b *Tab) Show(ctx context.Context) error
func (b *Tab) Screenshot(ctx context.Context, path string) error
func (b *Tab) ReplayOlder(ctx context.Context) (Replay, error)
func (b *Tab) Events(after int, kinds ...string) []Event // in order, after the given seq
func (b *Tab) Close(ctx context.Context) error

// Event is one piece of evidence (R184), numbered per tab.
type Event struct {
	Seq    int
	At     time.Time
	Kind   string // record, request, response, body, log, exception, console, issue, bfcache, navigated, certificate, replay, control
	Detail json.RawMessage
}

// ReadNetLog applies R185 to a NetLog file, line by line.
func ReadNetLog(path string, allowed []string) (findings []string, connects int, err error)
```

- [ ] **Step 1: Write the failing tests.**
  - `cdp_internal_test.go`, over an in-memory pipe pair:
    - `TestCDPMatchesAnswersToCalls`: two concurrent calls, answered out of order.
    - `TestCDPReaderNeverBlocks`: 10,000 unread events, then a call that answers within 1 s. Also a subscriber that makes a call from inside its own handler.
    - `TestCDPRoutesEventsBySession`.
  - `netlog_internal_test.go`, on canned NetLog files written by the test:
    - `TestNetLogFindsOffLoopbackConnects`: a `TCP_CONNECT_ATTEMPT` to `192.0.2.1:443`, a `TCP_CONNECT` whose end names `remote_address` off the allowed list, and a `TCP_STREAM_ATTEMPT_*` event, each a finding.
    - `TestNetLogFailsOnAnyUDPBytes`: a `UDP_BYTES_SENT` on a socket connected off loopback is a finding.
    - `TestNetLogAllowsOnlyUnresolvedRequestsOffLoopback`: a URL request to another host that ended in `ERR_NAME_NOT_RESOLVED` is allowed, and one that ended otherwise is a finding.
    - `TestNetLogWantsAConnect`: a log with no allowed connect is the finding "saw no connects".
  - `chrome_internal_test.go`, each test against loopback TLS servers under a throwaway CA:
    - `TestChromeRefusesAnUnpinnedCertificate` (the control): with no pins, `Navigate` returns `ErrCertificate`.
    - `TestChromeTrustsOnlyThePinnedLeaves`: server 1 loads; server 2, under the same CA, returns `ErrCertificate`.
    - `TestPinsOfRefusesAForeignChain`.
    - `TestStartChromeRefusesAnotherBundle`: a binary outside a Chrome for Testing bundle is refused before it runs.
    - `TestAPageInitiatedCertificateErrorIsReported`: a pinned page links to the unpinned server; once the link is clicked, a `certificate` event.
    - `TestOnlyLoopbackResolves`: `https://localhost:<port>` and `https://192.0.2.1/` fail with `ERR_NAME_NOT_RESOLVED`, while `127.0.0.1` loads.
    - `TestOffLoopbackPassesAfterALoad`: after a load, `Close`, then `OffLoopback` reports no findings and at least one connect to the server's port.
    - `TestWindowsStayVisibleAndHideByMinimising`: four windows are all `visible`; `Hide` makes one `hidden`, and `Show` makes it `visible`.
    - `TestTheRecorderIsUnseenAndFollowsDocuments`: one start per document, with `data-order`; `Eval` straight after `Navigate` answers in the new document; a page-initiated `location.reload()` gives a new start; `typeof gateRecord` is `"undefined"` in the main world; no `violation` log and no issue.
    - `TestBackNeverRestoresAPageWhosePollsAreNoStore`, the inverted canary: a page that polls a `no-store` endpoint by XHR; after `Back`, a new document, with reasons that include `JsNetworkRequestReceivedCacheControlNoStoreResource`. Also a page with cacheable polls, which is restored (`pageshow` with `persisted`), with `Back` returning on the restore.
    - `TestKeysPressButtonsAndTypeText`: Enter and Space each toggle a button's `aria-pressed`; `Type` fills a text field.
    - `TestEmulationAndScreenshots`: dark matches; a root font of 32 px; `innerWidth` of 390; a PNG written.
    - `TestReplayOlderIsDroppedByLiveJS`:
      - The page: a board-shaped page runs the repository's `live.js` under the board's policy, against a regions endpoint whose `data-order` rises and whose regions change. The fixture carries what live.js reads:
        - `#board[data-etag][data-order]`, with the bar first;
        - `#live-state`, `#live-pause`, `#feed-list`, `#announce` and `#live-signin`;
        - `#summary[data-round]` with a `p`;
        - the seven regions, each with `data-v`.
      - It asserts first that `ReplayOlder`'s `Replay` has a strictly lower order and at least one differing region.
      - It then asserts that no region changed, that the bar still reads "live", that the next real answer applied, and that there is no `security` log.
    - `TestCloseLeavesNothingRunning`:
      - At least one crash handler is recorded, with the resolved `--database`, and no `--metrics-dir` outside the run directory.
      - After `Close`, neither Chrome's group nor a recorded pid is alive.
      - When the pipe alone closes, the browser exits within 10 s.
- [ ] **Step 2: Run them and see them fail** (nothing is defined yet): `go test -tags e2e -count=1 -run 'TestCDP|TestNetLog' ./test/e2e/harness/`, then the browser tests with `AGENTSWARM_BROWSER_GATE_CHROME="<the binary>"`.
- [ ] **Step 3: Implement `cdp.go`, `netlog.go` and `chrome.go`** to R179–R185 and R190.
  - The reader hands events to an unbounded queue per session.
  - The gate world's context is tracked per main frame, from `executionContextCreated` and `executionContextsCleared`, and `Eval` waits for it. A restore re-announces its old context ids.
- [ ] **Step 4: Run them and see them pass,** the browser tests twice. Then `make lint`.
- [ ] **Step 5: Commit.** `git add test/e2e/harness/cdp.go test/e2e/harness/chrome.go test/e2e/harness/netlog.go test/e2e/harness/cdp_internal_test.go test/e2e/harness/netlog_internal_test.go test/e2e/harness/chrome_internal_test.go`; `git commit -m "test(harness): a throwaway Chrome for Testing over a pipe, trusting only the pinned leaves, kept off the network"`.

### Task 2: The control socket and the environment's browser

**Files:**
- Create: `test/e2e/harness/control.go` and `test/e2e/harness/control_test.go`.
- Modify: `test/e2e/gate_test.go`.

**Interfaces:**
- Consumes: Task 1's `PinsOf`, `StartChrome`, `Chrome`, `Tab`, `Event`, `Emulation`, `Replay`, `ErrCertificate`, `OffLoopback`.
- Produces:
```go
type ControlOptions struct {
	Path    string     // the socket, at most 103 bytes
	Origins []string   // what /tab and /navigate may open, beside about:blank
	World   func() any // GET /world
	End     func()     // POST /end
}

// ServeControl serves R186's commands for c until t ends. It refuses a path
// over 103 bytes, saying why.
func ServeControl(t testing.TB, c *Chrome, o ControlOptions) error
```
  The commands, each answering JSON:
  - `POST /tab {"name","url","window"}`, `POST /close {"tab"}`;
  - `POST /navigate {"tab","url"}`, `POST /back {"tab"}`, `POST /reload {"tab"}`;
  - `POST /eval {"tab","expr"}`;
  - `POST /key {"tab","key"}`, `POST /type {"tab","text"}`, `POST /click {"tab","selector"}`;
  - `POST /emulate {"tab",...}`, `POST /hide {"tab"}`, `POST /show {"tab"}`;
  - `POST /screenshot {"tab","name"}`, which writes `screenshots/<name>.png` and answers its path;
  - `GET /events?tab=&after=&kind=`;
  - `GET /polls?tab=`: each poll's time, status, gap since the last answer, order and regions changed;
  - `POST /cookies/clear` and `POST /replay {"tab"}`;
  - `GET /netlog`, provisional mid-run;
  - `GET /world`, `GET /status` (the version, the tabs and the run directory), and `POST /end`.

  An unknown command, tab or origin answers 4xx with the reason.

- [ ] **Step 1: Write the failing tests** in `control_test.go`, each with its socket in a short private directory (`os.MkdirTemp("/tmp", "abg")`, 0700, removed by the test). Each test uses a Go client with a Unix dialer, against a loopback TLS page under a throwaway CA.
  - `TestControlServesEachCommand`: each command once, with the shape of its answer.
  - `TestControlRefusesUnknownCommandsTabsAndOrigins`: also `file:///`, and a screenshot named `../x`.
  - `TestControlRefusesALongPath`: a path of 104 bytes.
  - `TestControlSocketIsPrivate`: the socket's directory is 0700.
  - `TestControlEndCallsEnd`.
- [ ] **Step 2: Run them and see them fail.**
- [ ] **Step 3: Implement `ServeControl`.** Then, in `TestBrowserGateEnvironment`, when `AGENTSWARM_BROWSER_GATE_CHROME` is set:
  - Make the run directory under `AGENTSWARM_BROWSER_GATE_OUT`.
  - Compute the pins of the stub and the IdP (R180), and the allowed connects (their ports).
  - `StartChrome`. Sign tab `a` in, in its own window, at `/items`, through the IdP, which signs in at once.
  - Follow both instances' logs for the lease's changes (R189).
  - `ServeControl` on `<run dir>/control.sock`, with `World` and `End`.
  - Print the socket, the run directory, the Chrome version and a short sheet of the commands, beside the environment's own lines.
  - Register a cleanup that sends `SIGCONT` to both instances and unpauses the database.
  - At the end, whether `/end` or the minutes: `Close` the browser, then fail on `OffLoopback`'s findings (R185) and on any `certificate` event (R180).

  Without the variable, the environment behaves as it does today.
- [ ] **Step 4: Run them and see them pass.** `go test -tags e2e -count=1 -run TestBrowserGateEnvironment ./test/e2e/` still skips without its variables. `make lint`.
- [ ] **Step 5: Commit.** `git add test/e2e/harness/control.go test/e2e/harness/control_test.go test/e2e/gate_test.go`; `git commit -m "test(e2e): the browser gate's environment drives a throwaway Chrome for Testing through a control socket"`.

### Task 3: The command, the documents, and the first walk

**Files:**
- Modify:
  - `Makefile` and `.gitignore`;
  - `README.md`, its section on tests;
  - `plan/2026-10-12-implementation-roadmap.md`: a row for 9a-3, and the status line;
  - this plan: "Changes made during execution".

- [ ] **Step 1: The Makefile.**
  - `CHROME_FOR_TESTING_VERSION ?= 157.0.8103.78`, and `CHROME_FOR_TESTING` derived from it, under `~/Library/Caches/agentswarm/chrome-for-testing/`. Quote the path, which has spaces.
  - `make browser-gate`: `build`, then `TestBrowserGateEnvironment` with `AGENTSWARM_BROWSER_GATE ?= 100`, `TESTCONTAINERS_RYUK_DISABLED=true`, `AGENTSWARM_BROWSER_GATE_CHROME`, `AGENTSWARM_BROWSER_GATE_OUT=$(CURDIR)/.browser-gate`, and a `-timeout` above the minutes. Its output is teed to `.browser-gate/last.log`.
  - `make test-browser`: the harness's browser tests.
  - Both fail at once, printing the download's commands, when the binary is missing.
  - Add `.browser-gate/` to `.gitignore`.
- [ ] **Step 2: The documents.**
  - README:
    - what the gate needs: Chrome for Testing at the pinned version, Docker, and about an hour;
    - how to download and check the browser: the bucket's URL, the MD5 check against `x-goog-hash`, and `ditto -x -k`;
    - what stays manual: Safari, the iPhone, and assistive technology.
  - The roadmap: the row for 9a-3, and the status line's amendment.
- [ ] **Step 3: The first walk, by the controller.**
  - `make browser-gate` against this plan's HEAD.
  - The controller walks "The gate's walk" below through the socket, judges every screenshot, and ends with `POST /end`.
  - It records each item as pass, fail or not exercised, with what it measured, the browser's version, the run's own verdict, and any difference from 9a-2's manual gate of 2026-11-11.
- [ ] **Step 4: Commit.** `git add Makefile .gitignore README.md plan/2026-10-12-implementation-roadmap.md plan/2026-11-12-plan-9a3-unattended-browser-gate.md`; `git commit -m "docs: the unattended browser gate, make browser-gate, and its first walk"`.

## The gate's walk

This is 9a-2's checklist, in an order that keeps the faults apart, with the bounds derived from the product's constants and the reviews' corrections.
- **Tabs.** `a` is the alert item's board, `p` the person's item's, and `s` the second item's. Each tab gets a window of its own.
- **Before each count of polls,** check that the tab is `visible`.
- **Timings** come from `/polls`, `/events` and `/world`.

| # | Item | How | Expect |
|---|---|---|---|
| 1 | 1 Opening a board | `a` at 1280×900. Then 390×844, DPR 3, mobile, with a fresh load, as live.js closes the feed only at load. Then wide again | "live" read after the first poll's answer, since live.js writes it at load, and the note gone. Wide: the feed beside the hypotheses, open. Phone: the feed last and closed. No overflow: `scrollWidth <= documentElement.clientWidth`. Screenshots of both, with the spoof title boxed |
| 2 | 13 Keyboard, 200% text, zoom, dark | Tab through `a` from the top; `FontSize` 32; 640×450 at DPR 2; `Dark`. Then clear the emulation | Every visible focusable control reached, each with a focus indicator. `<details>` states kept across two updates. No overflow. Dark: text contrast of at least 4.5:1 against the background actually shown. Screenshots |
| 3 | 8 Focus and Pause | On `p`: Tab to a raw output's link in the feed; wait for an entry; Enter on "New entries: update"; Pause by Space, two entries, un-pause; then clicks on the header's text and on a card | As in 9a-2's item 8. Pause looks different pressed and at rest. Screenshots of both |
| 4 | 9 Paging, and R2-M2's reader | "Show earlier" on `a`; then, at 390 px, scrolled to mid-feed past line 50. Then back to the board, unemulated | The lines are never fewer, and the first line stays the same, across three changed polls. The top line moves at most 1 px across an update |
| 5 | 2 Idling, a sibling, "N min ago" | Close `p`. `a` alone for 2 min while events land; then `s` opened. The link comes at the environment's minute 10, whatever row the walk is on | Each event shows within 7 s while `a` polls alone: 5 s, two polls, and the 1 s tick. "live" holds. At least one 304 that changed no region. "N min ago" moves on `s`. The strip appears on `a` within 38 s of the link's `200`, given its 30 s cache, read from `/world` and `a`'s records |
| 6 | 10 The polite summary | Throughout, from the recorder | The first poll after a load writes nothing to `#announce` while `data-round` is unchanged. At most one write per poll. None at a resume |
| 7 | 11 Seven tabs | Seven windows over the boards and the strip's items, kept 90 s or more; the items list and the kill switch's page once each; then close the extra windows | Each window `visible` and polling, with at least one changed answer, and every poll 200 or 304. The items list's first byte within 1 s; the kill switch page's within 2 s, its preview's bound. With seven pollers, an event may take up to 11 s |
| 8 | 4 A hidden tab | `hide a` for 2 min, then `show a` | `hidden` recorded. No poll while hidden. One poll within 1 s of `visible`. The page caught up within 7 s |
| 9 | 5 Back and forward | `a` to the items list, then `back` | A new document, as Chrome never restores the board, and the reasons recorded. "live" within 5 s. No write to `#announce` while `data-round` is unchanged |
| 10 | A lower order | `replay a`, just before an event lands | `Replay` strictly lower, with a differing region. No region changed, "live" held, and the next real `200` applied |
| 11 | 6 An expired session | (a) Both key files overwritten. (b) Again, at once when the page is back: the key ring reads again only from 50 s, so a late overwrite falls outside live.js's 60 s guard. (c) `cookies/clear`. After (b) and (c), "Sign in again" followed on `a` | (a) Within about 55 s, a 401 with `X-Session: ended`, then one new document through the IdP and back to the board. (b) A 401 50–55 s later shows "Sign in again", with no new document for 60 s. (c) A 401 with `X-Session: none` shows "Sign in again" at once, with no new document. (a) and (c) reach `s` too, so later rows read `a` alone |
| 12 | 3 An instance's stop | R189, with the roles from `/world` | "reconnecting" in the window with no leader, then "live" once the standby leads. No new document |
| 13 | 7 A database fault | `docker pause`; `docker unpause` after the fourth failed answer | The first 503 within about 12 s, as a probe's answer is kept 5 s. "reconnecting" at once. Gaps of 10, 20, 40 and 60 s after each failed answer, each within 2 s. "live" at the first success. No new document |
| 14 | The kill switch | On its page: a typed reason, then "Turn on: the reason above" | The header's reason boxed and labelled "a person's words". Screenshot |
| 15 | 12 The console, and R185 | `/events` over every tab, with `kind=log,exception,console,issue`; `/netlog`; then `POST /end` | Nothing R191 fails on. The run's own verdict, from R185 and R180, read from `.browser-gate/last.log` |

## Size

| Task | What | Raw days |
|---|---|---|
| 1 | Chrome for Testing, CDP and the NetLog reader in the harness, 21 tests | 1.75–2 |
| 2 | The control socket and the environment's browser | 0.75–1 |
| 3 | The command, the documents, the first walk | 0.5 |

- **Raw:** about 3–3.5 days, recorded only (R-f).
- **Calendar:** guess about half a day to a day, plus the walk's hour. Stop line: 5 calendar days from Task 1's dispatch.
- **Spend:** the author was told about $12–20. The re-review puts it at $20–30, given 9a-2's twofold overrun and this plan's added tests. It is counted at the input rate in R93's ledger (`plan/backups/plan9a3/spend.md`). Planning's own spend, about $3, is kept apart.
  - Models: Opus implementers for Tasks 1 and 2, as they need judgement on CDP and process control; Sonnet for Task 3's documents. An Opus review for each task, and an Opus whole-branch review.
  - Stop line: $40. If it is reached, the controller stops and asks.

## What stays manual

- Safari and the iPhone (R192).
- Assistive technology (Plan 9b).
- The walk itself, and the screenshots' verdicts, by the controller (R178).

## Options for the author (not in this plan)

- **The iPhone half, unattended.**
  - It needs the iOS Simulator runtime, about 8 GB, which the person downloads (`xcodebuild -downloadPlatform iOS`).
  - Then a throwaway simulator device holds the harness CA in its own trust store, and opens the board in real Mobile Safari with no click. The polls are counted at the stub.
  - Mobile Safari may also restore the board from its back-forward cache, the one place live.js's restore branch could run.
  - About 1–1.5 raw days, as a later plan.
- **A scripted walk.** v1's design, item by item, for when a plan wants a regression guard on its own items. The review of v1 says what such a walk must get right.

## Plan review

**The Opus plan review of v1** found 2 Critical, 9 Important and 9 Minor issues. The author decided two of them: C2's browser, and I9's scope. The controller ruled on the rest:

| Finding | Ruling |
|---|---|
| C1 midnight rarely fits | Dropped (R188); 9a-2's residual stands |
| C2 the crash handler in the person's Chrome directory | The author: Chrome for Testing (R179), with `BREAKPAD_DUMP_LOCATION` (R182), which spike 2 checked. The earlier runs' effect is recorded under "What the spikes showed" |
| I1 the resolver blocks 127.0.0.1 | `EXCLUDE 127.0.0.1` (R182); `TestOnlyLoopbackResolves` |
| I2 page events miss the browser's own requests | The NetLog check (R185); `TestNetLog*` and `TestOffLoopbackPassesAfterALoad` |
| I3 the board is never restored | Item 5 re-scoped (walk row 9); the inverted canary; the residual moves to Safari |
| I4 hidden windows | `--disable-backgrounding-occluded-windows`; hiding by minimising; `visible` checked before each count |
| I5 the replay | Bodies recorded (R184); R190's rules; `TestReplayOlderIsDroppedByLiveJS` |
| I6 the bounds | Derived from the constants, in the walk |
| I7 timing and coupling | The walk's order; R189; the cleanups |
| I8 navigation shapes | Context tracking; R180 for every navigation; Task 1's tests |
| I9 size and spend | The author chose the light scope |
| M1 keys and typed text | `Key`'s Enter, and `Type`; `TestKeysPressButtonsAndTypeText` |
| M2 "N min ago" | Read on `s` |
| M3 the summary's vacuous rule | MutationObserver writes; the rule on the first poll |
| M4 measurement details | In the walk |
| M5 tests that cannot catch their bug | The CDP handler's re-entrant call; `Close` by exact pids; canned NetLogs |
| M6 the spike's claims | Restated above; no `pagehide` |
| M7 harness details | `Env.CA()` read as a file; no zone field needed; the cleanups |
| M8 Review Focus | Reworked |
| M9 not worth its cost | Midnight dropped; Path 2 not run; item 5 re-scoped |

**The scoped Opus re-review of v2** found 0 Critical, 7 Important and 5 Minor issues, all ruled into this draft:

| Finding | Ruling |
|---|---|
| I1 a proxy passes the resolver rule and R185 | `--no-proxy-server` (R182); connects allowed only to the stub's and the IdP's ports, and the clause on URL requests restored (R185) |
| I2 R185's test, its empty pass, `/netlog`'s lag | Canned NetLogs; the positive control; any UDP bytes fail; the check after `Close`; line by line; `/netlog` provisional |
| I3 the crash handlers by a rewritten path, killed by substring | Resolved paths; handlers recorded by executable and exact `--database`; only those pids killed; at least one found |
| I4 row 13's leader and its log line | The environment follows the lease's changes into `/world`; the stop before the database fault (R189) |
| I5 the replay test cannot fail | `Replay` returned and recorded, asserted strictly lower with a differing region; the documents' order kept (R183); the fixture listed |
| I6 bodies in the controller's context | Bodies to files; `/polls`; `kind=` (R184, R186) |
| I7 no clean end | `POST /end`; the end checks after `Close` |
| M1 socket paths in `t.TempDir()` | A short private directory under `/tmp`; `ServeControl` refuses over 103 bytes |
| M2 missing interfaces | `Emulation`, `Replay`, `Tab.Close`, `PinsOf`'s error, `Tab(ctx, …)` with an error; white-box test files; the page-initiated reload's test |
| M3 what the socket reaches | Origins allowed; the screenshot name's pattern; `control` events; R186's cost corrected; the bundle identifier checked (R179) |
| M4 the walk's details | In the walk |
| M5 size and spend | Task 1 at 1.75–2 days; spend $20–30 likely; the stop line kept at $40 |

## Changes made during execution

(To be written during execution.)
