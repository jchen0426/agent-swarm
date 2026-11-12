# Plan 9a-3: Plan Re-review v2

| | |
|---|---|
| Plan | `plan/2026-11-12-plan-9a3-unattended-browser-gate.md`, Draft v2, at HEAD a77d9a0. The light scope and Chrome for Testing are taken as the author's. |
| Reviewer | Opus 5.5, scoped adversarial re-review, 2026-11-12 |
| Read | v2, review v1, v1; spike 2 and its `run.txt`; `live.js`; `gate_test.go`; the harness's `web.go`, `serve.go`, `log.go` and `parent.go`; `holding_faults_test.go` (the lease's timing); `leader.go`; `auth/keys.go` and `flow.go`; `pages/feed.go`; `web/headers.go`; 9a-2's gate record |
| Probes | Chrome for Testing 157.0.8103.78 only, headless, with R182's flags, `BREAKPAD_DUMP_LOCATION` and a throwaway profile, from a copy of spike 2 in the reviewer's scratchpad. **r1:** 192.0.2.1, the NetLog mid-run and at exit, a clicked link to an unpinned server, the handlers' `--database`. **r2:** a proxy on loopback that never forwards, then a SIGKILL of Chrome alone. Also a Go test of Unix socket paths under `t.TempDir()`. No Docker. Quoted lines are verbatim. |

## Verdict

**Ready with fixes: 0 Critical, 7 Important, 5 Minor.** The light scope removes most of v1's timing risk, and Chrome for Testing holds with the variable set: both crash handlers took the run's directory, with no metrics directory. What is left is in the parts v2 added or re-derived. A proxy takes the browser's own requests past both the resolver rule and R185, and R185's test cannot make the connect it looks for. `Close` looks for the crash handlers by a path Chrome rewrites. Row 13 assumes a leader that row 12 may have moved. The replay test cannot tell a drop from an equal replay. `/events` would pour poll bodies into the controller's context. And nothing ends the walk cleanly, so when it stops early the run's own end checks never run. Each fix is a change to the text or to a few lines of code, none widens the scope, and a v2.1 with them needs only the author's read.

## v1's findings in v2

14 of v1's 20 are addressed as ruled. Six are not, or only in part:
- **I2.** v2 dropped the clause that fails a URL request to another host unless it ended in `ERR_NAME_NOT_RESOLVED`, the clause that would catch I1 below. Its test cannot pass as built (I2).
- **M5's image page.** CSP no longer blocks it; the resolver rule does, before any connect (I2).
- **C2's wait.** R182 words it right, but by a path that Chrome canonicalises (I3).
- **I5.** The replay's rules are right; its test cannot see them (I5).
- **I7.** R189 is timed from a log line the controller cannot read, and the order puts the database fault, which can move the lease, before the stop (I4).
- **I8.** Its page-initiated reload test is gone (M2).

## Findings

### Important

**I1. A proxy takes the browser's own requests past the resolver rule and R185.** (R182, R185; Global Constraints, "everything stays on loopback"; Review Focus 1)
- **Probe r2.** R182's flags plus `--proxy-server=http://127.0.0.1:<p>`, standing in for a system proxy, which Chrome on macOS follows unless told otherwise. A listener that reads one line and closes got `CONNECT www.google.com:443 HTTP/1.1 x2 | GET http://clients2.google.com/time/1/current?cup2key=… x1`. Every connect in the NetLog was on loopback: `TCP_CONNECT_ATTEMPT 127.0.0.1:65121 x2 | TCP_CONNECT_ATTEMPT 127.0.0.1:65122 x3`, where 65122 is the listener.
- **Why it passes.** `--host-resolver-rules` acts on the host resolver alone, which a proxied request never consults. R185 allows every 127.0.0.1 port, so a local proxy, the kind proxy and VPN tools set system-wide, passes the check and forwards the requests. Spike 2's NetLog shows that no proxy was set on 2026-11-12; nothing stops one being set later.
- **Fix:** add `--no-proxy-server` to R182, which overrides the system's settings. R185 allows TCP connects only to the stub's and the IdP's ports. Restore v1 I2's clause on URL requests.

**I2. R185's test cannot make a connect, its check can pass on nothing seen, and `/netlog` lags.** (R185; `TestOffLoopbackFindsAConnect`; Task 2 Step 3)
- **192.0.2.1 never connects (r1, R182's flags).** The page's `https://192.0.2.1/x.png` gave `loadingFailed type=Image errorText=net::ERR_NAME_NOT_RESOLVED`. At exit the NetLog's connects were `TCP_CONNECT_ATTEMPT 127.0.0.1:65098 x4 | TCP_CONNECT_ATTEMPT 127.0.0.1:65099 x1 | UDP_CONNECT 127.0.0.1:443 x3 | UDP_CONNECT [2001:4860:4860::8888]:443 x4`. The rule maps IP literals too (v1 I1 showed it on 127.0.0.1), so the test fails as written. The obvious repair, dropping the rule for this one test, sends a SYN off the machine.
- **Mid-run reads lag.** Mid-run, the file reads one event per line, with the last line whole each time (`events=658`, then `events=1225`). It stayed at 1,225 for the next 12 s, and at exit held 1,275, among them another IPv6 route probe and `http://connectivitycheck.gstatic.com/generate_204`. So `/netlog` is provisional, and only a read after Chrome exits is complete. A check registered as a cleanup after `StartChrome` runs before its `Close`, since cleanups run last in, first out.
- **Event names.** Connects are `TCP_CONNECT_ATTEMPT` (`address`), `TCP_CONNECT`'s end (`remote_address`) and `UDP_CONNECT` (`address`). The build also defines `TCP_STREAM_ATTEMPT_CONNECT` (its NetLog's constants). Chromium's event list says `UDP_BYTES_SENT` carries no `address` on a connected socket, so "UDP bytes to an address other than 127.0.0.1" needs a join on `source.id`.
- **Fix:**
  - Test the parser on a canned NetLog: a `TCP_CONNECT_ATTEMPT` to 192.0.2.1:443, bytes on a UDP socket connected off loopback, and a `TCP_STREAM_ATTEMPT_*` event.
  - Give the live check a positive control: at least one connect to the environment's ports, or it fails with "saw no connects".
  - Fail on any `UDP_BYTES_SENT`. Run the final check after `Close`. Parse line by line, as the gate's NetLog will run to tens of MB.

**I3. `Close` looks for the crash handlers by a path Chrome rewrites, and kills by substring.** (R182; `TestCloseLeavesNothingRunning`; Review Focus 1 and 4)
- **The path (r1).** `BREAKPAD_DUMP_LOCATION=/tmp/claude-501/…/out1/crash` gave both handlers `--database=/private/tmp/claude-501/…/out1/crash`. The tests' run directory will be a `t.TempDir()` under `/var/folders/…`, which resolves to `/private/var/…`. "Every process whose command line names `<run dir>/crash`" then finds none, so `Close` waits for nothing, and the test's check passes on no handler, or fails, as it is read. The gate's own path has no symlink, which hides the fault there.
- **Matching by substring hits bystanders.** The reviewer's scan for `Chrome for Testing`, 4 s after a SIGKILL of Chrome (r2), found one process: `/bin/zsh -c … SPIKE_CHROME=…`, the shell that ran the probe. A `curl`, `tail` or `ls` naming `<run dir>/crash` would count as "whatever remains", and be killed.
- **The wait is needed.** In r1 one process was still alive 2 s after Chrome's clean exit (`procs.after-close: 1`). After a SIGKILL of Chrome alone, all 11 of its processes were gone within 4 s (r2).
- **Fix:** `filepath.EvalSymlinks` the run directory and the profile before the launch. Pick handlers by their executable, the bundle's `chrome_crashpad_handler`, and an exact `--database=<resolved>/crash` argument, recorded while Chrome runs. Kill only those pids and Chrome's process group. The test asserts that at least one handler was found.

**I4. Row 13 assumes the leader printed at the start, and is timed from a line the controller cannot read.** (R189; walk rows 12 and 13; Task 2)
- **The leader can change at row 12.** The gate's lease has a TTL of 3 s, renewed every second with a 1 s margin (`holding_faults_test.go:31-33`). Row 12's pause makes a lose it ("lease lost", `leader.go:582`). After the unpause, a and b both try to take it every second, so either may lead. The environment prints "a leads, pid …; b stands by" once, at its start (`gate_test.go:300`). If the roles have swapped, the walk stops the leader and terminates the standby, and no "lease released" ever comes.
- **The line is out of reach.** "lease released" (`leader.go:421`) goes to serve's standard-error file. Neither the environment nor the socket names that file, and `Log`'s path is unexported (`log.go:25`).
- **Fix:** the environment follows both instances' `Log().Lines()` in `gateWorld`, and logs each "lease taken", "lease released" and "lease lost" with the instance and the time. Row 13 starts by reading the latest "lease taken". Or row 13 runs before row 12.

**I5. `TestReplayOlderIsDroppedByLiveJS` passes when the replay is not older.** (R190; Task 1; Review Focus 2)
- **Why it cannot fail.** live.js drops an answer only when `below(o, order)` holds (`live.js:58`, `65`). It applies an answer of equal order, and `take` changes nothing whose `data-v` matches (`live.js:84`). So a replay of the latest answer, or of one whose regions match the page's, gives all three of the test's facts: no region changed, the bar "live", the next answer applied. `ReplayOlder` returns only an error, and `Event.Kind` has no replay, so nothing shows what was replayed.
- **The last applied order.** After a fresh document it is the document's own (`live.js:10`), which R184 does not keep, as it keeps poll bodies alone.
- **Fix:**
  - `ReplayOlder` returns, and records as kind `replay`, the replayed order, the page's last applied order, and the regions whose `data-v` differ. The test asserts a strictly lower order and at least one such region before it asserts the drop.
  - R184 keeps each document's `#board` `data-order` and `data-etag`.
  - The fixture carries what live.js reads, or live.js reloads the page (`live.js:6-8`, `56`): `#board[data-etag][data-order]` with the bar first; `#live-state`, `#live-pause`, `#feed-list`, `#announce` and `#live-signin`; `#summary[data-round]` with a `p`; and the seven regions, each with `data-v`.

**I6. `/events` pours the poll bodies into the controller's context.** (R184, R186, R178)
- Each `200` is the board's seven regions, its feed up to 50 lines (`feed.go:16`), or every line at `?feed=all`: 1,145 to 1,202 in 9a-2's gate (its plan, line 1258). The alert board changes with every event, every 30 s, and with its run's entries.
- So an `/events?tab=a&after=` over a few minutes returns many such bodies, likely tens of KB each, which the AI controller reads into its context on most of the 15 rows.
- **Fix:** bodies go to `bodies/<tab>-<seq>.html`, and their event keeps the size, `data-order`, `data-round` and each region's `data-v`. `/events` takes `kind=`. Add `GET /polls?tab=`, giving each poll's time, status, gap, order and regions changed, which is all most rows read.

**I7. Nothing ends the walk cleanly, so the run's own verdicts never run.** (Task 2 Step 3; R180, R185, R189; Task 3)
- The environment ends only when its minutes pass (`gate_test.go:307-316`): 100 by default, for a walk of about an hour. Only that end runs R185's and R180's end checks, the cleanup that continues the instances and unpauses the database (R189), and the containers' removal.
- 9a-2's environment was stopped by a signal: "its log ends in FAIL; its instances and its database container were then removed by hand, as the signal skips the test's cleanup" (`plan/2026-11-10-plan-9a2-live-board.md:1244`).
- **Fix:** add `POST /end` to the socket. It ends the minutes at once, so that the cleanups and the end checks run, and `make browser-gate` exits with their verdict. The walk's last step calls it.

### Minor

**M1. Two of the three control tests cannot bind a socket in `t.TempDir()`.** (Task 2 Step 1)
- A Go test with their names, under this Mac's `TMPDIR`, gave `len=104 listen: … TestControlServesEachCommand2006526021/001/control.sock: bind: invalid argument`; `len=115 … bind: invalid argument` for `TestControlRefusesUnknownCommandsAndTabs`; and `len=102 ok`. macOS allows 103 bytes. The gate's own path is 79.
- **Fix:** the tests use a short private directory (`os.MkdirTemp("/tmp", "abg")`, 0700). `ServeControl` refuses a longer path and says why.

**M2. Missing interfaces.** (Task 1 Interfaces; Task 2)
- `Emulation` is used but never defined. v1's line leaving it to the implementer is gone.
- `typeof gateRecord` needs an eval in the main world, and "the pipe alone closes" needs the pipe. Name those test files `*_internal_test.go`, as the harness's other white-box tests are.
- `/close` needs a `Tab.Close`. `PinsOf` cannot be tested for a refusal without a fake `testing.TB`: have it return `([]string, error)`.
- `ServeControl` calls `Chrome.Tab(t testing.TB, …)` from HTTP handlers, where `t.FailNow` must not run. A failed `/tab` would fail the whole run and leave `curl` hanging. Make it `Tab(ctx, newWindow) (*Tab, error)`.
- v1 I8's page-initiated reload (`live.js:76`) has no test. Add one to the recorder's test.

**M3. What the socket reaches.** (R179, R186)
- `/screenshot`'s name is used as a path, so `../../x` writes outside the run directory. Allow only `^[a-z0-9-]{1,40}$`.
- `/tab` and `/navigate` take any URL, `file://` and `chrome://` included, so the person's files can end up in `/eval` answers and screenshots. Allow the stub's and the IdP's origins and `about:blank`.
- `/eval` is not recorded, so no record shows that the controller kept to "no DOM sink". Append each `/eval` and each act to `events.jsonl`.
- R186's cost line is wrong. The pipe reaches the test process alone, which was v1 R181's reason for it; the socket reaches every process of the person's user.
- Nothing enforces "never the person's Chrome": `AGENTSWARM_BROWSER_GATE_CHROME` can name it, and C2 comes back. `StartChrome` refuses any bundle whose `CFBundleIdentifier` is not `com.google.chrome.for.testing`, the pinned build's.

**M4. The walk's details.** ("The gate's walk")
- **Row 1.** live.js writes "live" itself at load (`live.js:156`, `28`, `18`), so "within 5 s" cannot fail. Read it after the first poll's answer.
- **Rows 2, 4 and 5.** Row 2 leaves `a` emulated and row 4 leaves it at `?feed=all`: clear both. For row 5's "a alone", close `p` first, as 7 s is the bound for one poller.
- **Row 7.** Keep the windows open 90 s or more, so that the idle second item's board changes at least once, by its header's minute.
- **Row 9.** "No write to `#announce`" holds only while `data-round` is unchanged, as row 6 says.
- **Row 11.** For (b), the key ring reads the key again only from 50 s (`keys.go:197`). An overwrite more than about 45 s after (a)'s 401 brings its own 401 outside live.js's 60 s guard (`live.js:73`), and the page reloads, so have the command ready. (a) and (c) act on `p` and `s` too, so rows 12 and 13 read `a` alone.
- **Row 15, and R191.** The faults' 401, 502 and 503 log `network` errors, as the favicon's 404 does (r1: `log network/error: Failed to load resource: … 404 ()`). Say that only `javascript`, `security` and `violation` entries, and console errors, fail.
- **Windows.** Each tab the walk uses gets its own window. A tab opened without one lands in the last active window and hides the tab there.

**M5. Size and spend.** (Size)
- In v1, Task 1 had 11 tests and a narrower interface, for 1–1.25 days. It now has 17 tests, with `Back`, `Reload`, `Type`, `Click`, `Hide`, `Show`, `ReplayOlder`, `Events(after)` and the NetLog, for the same 1.25 days. 1.75–2 days is likelier.
- 9a-2 overran about twofold ($49 against $20–28). The same overrun takes $12–20 to $24–40, at the stop line, before this review's fixes. The walk's hour in the controller's own session is outside the ledger, but it is not free.
- **Fix:** guess $20–30 with a $60 stop line, or say that $40 will likely be reached. Record the walk's tokens.

## Checked and sound

- **R180's navigation shapes.** A clicked link to the unpinned server gave `loadingFailed type=Document errorText=net::ERR_CERT_AUTHORITY_INVALID` and `frameNavigated url=chrome-error://chromewebdata/ unreachableUrl=https://127.0.0.1:65098/x` (r1). `TestAPageInitiatedCertificateErrorIsReported` can be built as written.
- **The NetLog at exit.** After a clean exit it parses whole, ending in `polledData` (r1).
- **The walk's bounds** match the code: the backoff of 10, 20, 40 and 60 s (`live.js:29-30`), one poll when the tab turns `visible` (`live.js:149`), and the 7, 11, 38, 12 and about 55 s bounds as v1 derived them. The order removes v1's couplings, except those in I4 and M4.
- **Row 11(a)'s sign-ins.** Several tabs signing in at once cannot clobber one another, as each flow has its own cookie (`auth/flow.go:23-24`).
- **The other browser tests.** The back-forward test can fail both ways, since `Back` must wait for a restore, which fires no load. The window and resolver tests match spike 2.

## Declined to judge

- Whether headless Chrome follows a real macOS system proxy. The probe used `--proxy-server` and changed no system setting.
- `UDP_BYTES_SENT` without an `address` on a connected socket. This comes from Chromium's source; no run sent UDP bytes.
- Whether the page's CSP and Trusted Types apply to DOM writes from the isolated world.
- The gate world's real timings, and which instance leads after a pause. These come from the code; no Docker test ran.
- The person's Chrome directory, which was not read. These probes launched only Chrome for Testing, with the variable set.
- Safari.
