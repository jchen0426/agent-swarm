# Plan 9a-3: Plan Review v1

| | |
|---|---|
| Plan | `plan/2026-11-12-plan-9a3-unattended-browser-gate.md`, Draft v1, at HEAD a77d9a0 |
| Reviewer | Opus 5.5, adversarial plan review, 2026-11-12 |
| Read | The plan; 9a-2's gate and its results; `test/e2e/gate_test.go`; the harness (`web.go`, `ca.go`, `serve.go`, `env.go`, `parent.go`, `deployment.go`); `webtest/ca.go`, `idp.go`; `live.js`, `board.html`, `layout.html`, `regions.html`, `app.css`; `web/headers.go`, `pages/layout.go`, `live.go`, `pages.go`, `board.go`, `feed.go`, `killswitch.go`; `auth/keys.go`, `authenticator.go`, `session.go`; `app/watch.go`, `strip.go`, `boardview.go`; `postgres/lease.go`; `cmd/agentswarm/serve.go` (drain and close); the spike, `run1.txt`, `run2.txt` |
| Probes | To check what the spike did not, I ran my own copy of the spike's CDP client against loopback TLS servers, in throwaway profiles, with R182's flags (Chrome 157.0.8103.78, headless). The board-shaped page ran the repository's `live.js` under `BoardPolicy`. It got an HttpOnly session cookie from a `/signin` redirect, and it polled a regions endpoint that answered 200/304 under `no-store`, as `GET /items/{id}/board` does (`pages.go:130`, through `web.Authenticated`). Programs and outputs are in the reviewer's session scratchpad, outside the repository. Quoted lines are verbatim. No Docker test was run. |

## Verdict

**Not ready: 2 Critical, 9 Important, 9 Minor.** The approach holds up: a throwaway headless Chrome over a pipe, trusting pinned leaves, with a recorder in an isolated world. The pins, the isolated binding, the drop of a lower order and the session key's timing all check out (see "Checked and sound"). But five premises are false in this Chrome:
- The resolver rule blocks the environment's own 127.0.0.1 (I1).
- The board can never be restored from the back-forward cache (I3).
- Six of seven new windows are hidden (I4).
- No zone list puts midnight in R188's window for most start times (C1).
- The throwaway browser writes its crash database into the person's Chrome directory (C2).

Each would surface only as a failed, or silently hollow, 35-minute run. A v2 should re-derive R182, R185, R188, R189, R191 and R192 and add a schedule of the walk.

## Findings

### Critical

**C1. R188 finds no zone for about three runs in four.** (R188, item M, Task 4 Step 3)
- The midnight must fall 8 to 20 minutes into the walk, a 13-minute window. Local midnights fall at UTC :00, and at :30 or :15 for only a few hours of the day.
- I computed this in Go with the machine's zoneinfo for 2026-11-12. Of 1,440 start minutes, a fitting zone exists for:
  - 312 with Etc/GMT-14..+12;
  - **351 (24%)** with those plus Asia/Kolkata, Asia/Kathmandu and Australia/Eucla;
  - 455 (32%) with all 598 IANA zones on the machine.
- R188 makes "no zone fits" an error. So item M fails in about 76% of runs. Task 4's "two back to back, both must pass" then succeeds about 6% of the time, on M alone.
- The criterion is also thin. Feed lines show `HH:MM:SS` alone (`feed.go:66`, `board.html:82`). What a check can compare is `atSecond`'s formatting against each line's `datetime`, which is server formatting the browser adds nothing to. 9a-2's residual ("a spanning feed reads its days by order alone") is a design limit that no driver closes.
- **Fix: drop M and keep the residual (preferred).** If the author wants M anyway, give serve a zone the test makes itself:
  - The harness writes a one-offset TZif file into a directory.
  - It starts serve with `ZONEINFO=<dir>`. Go's `time.LoadLocation` reads that first, so `web.timeZone: Gate/Midnight` passes the deploy check (`internal/deploy/web.go:221`) with no product change.
  - The offset is chosen so that midnight falls at start + 12 minutes.

**C2. The throwaway Chrome's crash handler uses the person's Chrome directory, from outside Chrome's process group.** (R182; Global Constraints, "Never touch the person's Chrome profile"; Review Focus 5; `TestCloseLeavesNoProcess`)
- **Evidence.** I ran `ps` while a throwaway Chrome ran (pid 49277, group 49277). It showed `chrome_crashpad_handler` of the 157 framework, **ppid 1, pgid 49279**, with `--database=~/Library/Application Support/Google/Chrome/Crashpad --metrics-dir=~/Library/Application Support/Google/Chrome --url=https://c…`. That is the crash database the person's own Chrome uses.
- **It writes there.** With `BREAKPAD_DUMP_LOCATION=<dir>`, the handler took `--database=<dir>` and at start created `settings.dat`, `new/`, `pending/`, `completed/` and `attachments/` in it.
- `--disable-breakpad`, `--disable-crash-reporter` and `HOME=<tmp>` neither stop it nor move it. It exited with its Chrome; no 157 handler was left afterwards.
- **Consequences:**
  - Every run touches the person's Chrome directory.
  - A crash during the gate files a dump of the gate's pages there. The dump would be uploaded under the person's consent, by a process outside Chrome's network service, which neither the resolver rule nor R185 reaches.
  - `kill(-pgid, 0) == ESRCH` passes while the handler runs in its own group.
  - The spike's runs and this review's probes have already done all of this.
- **Fix:**
  - Launch Chrome with `BREAKPAD_DUMP_LOCATION` set to a `t.TempDir()` (verified). Wait for the handler, found by its `--database=<that dir>`, to exit before the directory is removed.
  - Record the residual: `--metrics-dir` stays under `~/Library/Application Support/Google/Chrome`, as seen with the variable set.
  - Alternatively, revisit R179. Chrome for Testing has its own bundle and its own Application Support directory, and does not auto-update. The cost is one download.
  - Either way, R182's "Cost: none known" and the Global Constraint need the author's ruling.

### Important

**I1. `--host-resolver-rules=MAP * ~NOTFOUND` blocks 127.0.0.1, so nothing loads.** (R182)
- **Probe:** `resolver.ip-literal: navigateError="net::ERR_NAME_NOT_RESOLVED"` for `https://127.0.0.1:<port>/a`. With `MAP * ~NOTFOUND, EXCLUDE 127.0.0.1`, both pinned servers load and `localhost` still fails.
- The spike never passed the flag (spike `main.go:269-275`).
- Task 1's `TestChromeTrustsOnlyThePinnedLeaves` fails at once, with ERR_NAME_NOT_RESOLVED rather than ErrCertificate.
- R182's own cost line ("Chrome ignores a flag it drops silently, and the off-loopback check (R185) backs that") invites the wrong repair: dropping the rule, which opens I2's hole.
- **Fix:** `--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1`, plus a Task 1 test that `https://localhost:<port>` fails with ERR_NAME_NOT_RESOLVED while `127.0.0.1` loads.

**I2. R185 cannot see the browser's own requests.** (R185, Review Focus 4)
- **Probe:** a NetLog (`--log-net-log`) of a run of about 70 s, with the rule fixed as in I1. It shows URL requests from Chrome itself, despite `--disable-background-networking` and `--disable-component-update`:
  - `accounts.google.com/ListAccounts` (5 times);
  - `clients2.google.com/time`;
  - `optimizationguide-pa.googleapis.com/v1:GetModels`;
  - `www.google.com/async/folae?…` (carrying `client_locale` and `client_country`);
  - `www.gstatic.com/ohttp_gateway/…`.
- All of them ended in ERR_NAME_NOT_RESOLVED. The only non-loopback sockets were UDP connects to `[2001:4860:4860::8888]:443` that sent no bytes: Chrome's IPv6 reachability probe.
- The page sessions' Network events named none of them (0 matches). R185, built on those events, would pass while these requests left the machine. They likely did leave it during the spike, which ran without the rule.
- **Fix:** R185 reads a NetLog written into the run's directory, and fails on:
  - any URL request to a non-loopback host that did not end in ERR_NAME_NOT_RESOLVED;
  - any TCP connect to a non-loopback address;
  - any UDP bytes sent.

  Keep the page-level check as well, to name the page's own requests.

**I3. The board can never be restored from the back-forward cache, so item 5 cannot fail.** (R192, item 5, the spike's bfcache claim, line 39, `TestTheBackForwardCacheRestoresANoStorePage`)
- **Probe, with the real live.js, page and polls `no-store`, and a session cookie.** Going back gave a new document (`navtype=back_forward`) and `Page.backForwardCacheNotUsed`: `MainResourceHasCacheControlNoStore, JsNetworkRequestReceivedCacheControlNoStoreResource, BrowsingInstanceNotSwapped`.
- With cacheable polls, the same page gave `frameNavigated type=BackForwardCacheRestore`, a persisted `pageshow` and one poll. Without the script, it was restored.
- So the blocker is the polls' `no-store`, which every poll answer carries (`pages.go:130`, `headers.go:44-49`).
- The spike's no-store page had no cookie and no XHR (spike `main.go:418-441`), so its restore says nothing about the board. Consequences:
  - R192's allowance makes item 5 a pass that can never be anything else.
  - The canary stays green while proving nothing about the board.
  - Line 39's "the back-forward restore ... are real" is false.
  - live.js's restore branch (`live.js:153`) is unreachable in Chrome.
- **Fix:**
  - Re-scope item 5 to what Chrome does: going back makes one new document, "live" within 5 s, no write to `#announce`, and exactly these reasons recorded.
  - Invert the canary: a board-shaped page whose XHR gets `no-store` must read "not restored", with `JsNetworkRequestReceivedCacheControlNoStoreResource`. A Chrome that starts restoring such pages then flips it.
  - Tell the author that 9a-2's residual ("a real restore") cannot be closed in Chrome without cacheable polls, which is a product change. It is a question for Safari and Firefox.

**I4. Most of R189's windows are hidden, and item 4's covering tab does not cover tab A.** (R189, items 4 and 11)
- **Probe with R182's flags.** Eight windows, all `1280x900` at `0,0` except the first at `22,22`, gave `main=visible w1=visible w2=hidden … w7=hidden`.
  - Tiled at 300×200, all were visible.
  - With `--disable-backgrounding-occluded-windows`, all were visible, and minimising tab A's window still hid it.
- A tab made by `Target.createTarget` without `newWindow` lands in the last active window, not in tab A's (`lands in window …173; main's window is …159`). A `windowId` parameter is ignored. Activating the new tab left tab A `visible`.
- So while item 11's windows are open, item 4 is "not exercised".
- **Fix:**
  - Add `--disable-backgrounding-occluded-windows` to R182.
  - Assert that each window is `visible` before counting its polls.
  - Hide tab A by minimising its own window (verified by the spike and the probe). Or activate tab A first and assert that the covering tab is in tab A's window.
  - Close item 11's windows when item 11 ends.

**I5. R191 replays what R184 does not record; a replay without Content-Type stops tab A for good and fakes a CSP finding.** (R184, R191; items L, 8, 10 and 12)
- R184 keeps the status, X-Session, ETag, If-None-Match and TTFB. R191 needs the answer's body and its headers.
- **Probe.** `Fetch.fulfillRequest` answered one poll with an older regions document (order `1.0`, below the page's `1.1`):
  - With `Content-Type: text/html`: `f1 live → f1 live → f2 live`. The answer was dropped, and the next real answer was applied, as the plan says.
  - Without it: `f1 live → f1 This page does not refresh itself…`, and no poll after that. The XHR parses an untyped body as XML, and the regions document is not well-formed (`hidden` has no value). `x.response` is null, so `live.js:54` calls `stop()`.
  - Chrome also logged two `security/error` lines, "Applying inline style violates … 'style-src 'self''", against `/static/live.js`. Item 12 would then report a product CSP violation that the driver caused.
- `live.js:65` drops an answer only when its order is lower in some component. An answer with an equal order is applied, so replaying one moves regions back. And "the next real answer applies" is vacuous when that answer is a 304.
- Items 8 ("every changed region") and 10 ("an answer that ended a round", `data-round`) need the bodies of the 200s too.
- **Fix:**
  - Record every response's headers, and every 200 poll's body: `Network.getResponseBody` on `loadingFinished`, with the buffer sizes set in `Network.enable`.
  - R191 replays a 200 with `Content-Type: text/html; charset=utf-8`.
  - It picks an answer strictly below the page's current order and differing in some `data-v`. The current order is the last applied 200's; the DOM never shows it.
  - It arms the replay just before a delivery, so that the next real answer is a 200, and calls `Fetch.disable` afterwards.

**I6. Item 2's "within 5 s" is below the product's own worst case, and the strip's 35 s sits at its edge.** (items 2 and 4, Review Focus 1)
- **Mechanism:**
  - The next poll comes 5 s after the previous one *ends* (`live.js:30`).
  - A read is served from a tick up to 1 s old (`watch.go:22`, `390-420`). Any tab's read starts such a tick, and the tick covers every item read in the last 10 s.
  - So an event committed just after a tick that the next poll reuses waits a whole extra cycle. The worst case is about 5 s + two polls' durations + 1 s, roughly 6.2 s.
- **How often.** Tab A alone exceeds 5 s only by its polls' own durations. With item 11's seven windows polling during item 2, my estimate is roughly one event in ten, so item 2, with four events, fails about one run in four.
- The strip is kept 30 s (`strip.go:23`), so its worst case is about 30 + 5 + 1 s plus the polls' time, which is over 35 s.
- **Fix:** derive each bound from the constants and say so in the table.
  - An event shows by the end of the second poll that began after the delivery's 200 (11 s or less), or within 7 s with tab A alone.
  - The strip shows within 38 s of the link's 200, not of minute 4: the link is tried again each tick until it is taken (`gate_test.go:332-335`).

**I7. Faults are timed by the wall clock, and the walk has no schedule.** (R186, R190; items 3, 4, 6, 7, 11 and M)
- **Item 7.** A failed probe's waiter is answered from the last good tick while that tick is under 5 s old (`watch.go:381`, `lastGoodKept`).
  - If another tab refreshed the tick just before `docker pause`, the first poll after it is answered 200/304, and the first 503 comes up to about 12 s after the pause. That breaks the 7 s bound.
  - Even with tab A alone, the 7 s bound has about 1 s of margin (5 s + a poll + the 1 s probe).
  - The 90 s pause still leaves about 6 s for the fourth failure, but only because the backoff starts early.
- **R190 Path 1.**
  - A keeps `/leaderz` at 200 for its whole drain (`health.go:41-51`), and the stub routes to it (`web.go:221-225`), so A keeps answering polls. Serve shuts its web server only after the release (`serve.go:1036`, `1180`).
  - So, counted from SIGTERM, the "about 20 s" with no leader is 20 s less A's drain. A long drain leaves no window at all.
  - While B is stopped, each request first waits out B's 1 s probe (`web.go:43`). It then gets A's answers, then a standby notice from A between the release and the close, then the reverse proxy's 502 once A's listener closes, and the bar reads "reconnecting". That works.
  - Count the stop from A's "lease released" log line, and stop B before signalling A.
- **Coupling between items:**
  - Item 11's windows, if left open, change the tick's age in item 7.
  - They all reload at 6(a), and they all stop at 6(c): `Network.clearBrowserCookies` acts on the whole browser.
  - They break item 4 (I4).
  - M's midnight may fall in R190's no-leader window or in item 7's pause, when no event is delivered (`gate_test.go:172-185`).
  - After R190, only instance c is alive for items 6, 7 and K.
- **Fix:**
  - Add a schedule table: minute, item, tabs open, instances alive, faults active.
  - Drive the faults by events: unpause after the fourth failed answer is seen; SIGCONT B 20 s after A's "lease released"; overwrite the key again once the reloaded page is live.
  - Close the extra windows after item 11.
  - Add `t.Cleanup`s that SIGCONT B and unpause the database. Otherwise a stopped serve is killed only by its parent, after `parentGrace` (`parent.go:157`, `210`).

**I8. The CDP client's navigation shapes are untested.** (R180, R183, R184; Task 1's interfaces and tests)
- **Probe events.**
  - Each new document fires `contexts cleared` twice, then creates a new default context and a new `gate` context, with new ids.
  - A restore fires no `loadEventFired`. It announces the old ids again (`context created id=9 name="gate"`, `id=8`), and fires `frameNavigated type=BackForwardCacheRestore`.
  - An Eval sent between the commit and the gate context's creation has no context to run in.
- `Page.navigate`'s `errorText` covers only the driver's own navigations. live.js's `location.reload()` (`live.js:76`), the sign-in redirect chain, followed links and the kill switch's POST all tell the driver nothing. So R180's "fails at once" misses a certificate error there.
- **Fix:**
  - The client tracks the gate context per main frame, from `executionContextCreated` and `executionContextsCleared`, and Eval waits for it.
  - `Back` waits for either a load or a `BackForwardCacheRestore`.
  - A Document `loadingFailed` with `net::ERR_CERT_*`, or a `chrome-error://` commit, fails the test.
  - Task 1 tests each shape: Eval right after Navigate; Back with a restore and Back without one (the probe's two pages); a page-initiated reload; and a page-initiated navigation to an unpinned server, reported as ErrCertificate.

**I9. Size and spend are guessed as if the premises held.** (Size)
- Plan 9a-2 guessed $20-28 and spent about $49, mostly in reviews and fix rounds.
- Here, C1, I1, I3 and I4 would each fail a first run, and I6 and I7 make a pass depend on timing.
- Each fix round costs a run of about 35 minutes, plus reading `events.jsonl`. Tasks 2 and 3 each run the gate at least twice, before Task 4's two runs.
- Realistic as written: $35-60 and 1.5 to 3 calendar days. The $56 stop line is within reach.
- **Fix:**
  - Add an item selector, say `AGENTSWARM_BROWSER_GATE_ITEMS=3,7`, so that a fix is checked again in minutes.
  - Cut M and item 5's restore.
  - Guess again after v2, at about $30-45 and 1 to 2 days.

### Minor

**M1. Enter sent the spike's way does not press a button, and K needs typed text.**
- **Probe on `#live-pause`:**
  - Enter as `rawKeyDown` + `keyUp` left `aria-pressed` false.
  - Enter as a `keyDown` with `text:"\r"` toggled it.
  - Space toggled it either way.
- The spike sent Tab alone (spike `main.go:213-219`).
- K's "a person's words" box appears only for a typed reason sent with "Turn on: the reason above" (`killswitch.html:48-51`, `layout.go:106-107`).
- **Fix:** `Key("Enter")` sends a `keyDown` with text; add `Type(text)` (`Input.insertText`); `TestKeysEmulationAndScreenshots` checks that Enter and Space each press a button.

**M2. On tab A, "N min ago" will not change.** The header's time is the later of the latest record's and the latest entry's (`boardview.go:85`, `board.go:212`). The alert item gets a record every 30 s, so its header reads "just now". **Fix:** check it on a board idle for minutes, such as the second item's.

**M3. Item 10's first rule is vacuous.** The first poll comes 5 s after the load (`live.js:156`, `30`), so `#announce` is always empty 1 s after a new document. **Fix:**
- Assert that the first poll after a load writes nothing when `data-round` is unchanged.
- For "at most once a poll", count writes with a MutationObserver, not changes of text.

**M4. Measurement details.**
- `scrollWidth <= innerWidth` misses an overflow up to the scrollbar's width. Compare with `documentElement.clientWidth`.
- `#live-state` has a transparent background, and `contrast` refuses alpha, so the caller must resolve the background actually shown.
- Item 1's phone check needs a fresh load after the emulation: live.js closes the feed only at load (`live.js:155`).
- In item 7, the first success after the fault may be a 304, so "'live' at the first 200" should read "at the first success".
- Item 11's 1 s first byte for the kill switch's page is below the page's 1.5 s preview bound (`killswitch.go:452`). Allow 2 s.
- "At least half the polls answer 304" depends on how busy the alert item is. "At least one 304, and it changed no region" is the check 9a-2 carried.

**M5. Unit tests that cannot catch their bug.**
- `TestGapsReadAnswerToRequest` uses polls of zero length, so measuring from answer to request and from answer to answer give the same result. Give each synthetic poll the 2 s a failed read takes (`output.go:23`).
- `TestCDPReaderNeverBlocks` should also have a subscriber that makes a call from its handler, the usual CDP deadlock.
- `TestCloseLeavesNoProcess` must reap Chrome before `kill(-pgid, 0)`, and it cannot see C2's handler.
- `TestMidnightZoneFallsInTheWindow`'s three fixed instants hide C1.
- In `TestARequestOffLoopbackIsReported`, a page under `BoardPolicy` has its image blocked by CSP before any network request (`loadingFailed … blocked=csp`). Add a page without `img-src 'self'`.

**M6. The spike's claims, read exactly.**
- "Under the board's policy": the spike's policy was `default-src 'self'; script-src 'self'; require-trusted-types-for 'script'; trusted-types 'none'` (spike `main.go:34`), and its recorder touched no DOM.
- R183's `pagehide` never arrives. No navigation away recorded `pagehide` or `hidden` (spike `run1.txt:50-78`, and the same in the probe). The binding call made by a document being left is apparently dropped.
- **Fix:** drop `pagehide` from R183, or take it from CDP.

**M7. Harness details.**
- `gateWorld.names` is `a, b` (`gate_test.go:306`). Path 2's instance c must join the deliveries and the acts.
- `Env.CA()` returns a file path (`env.go:323`), so the test must build PinsOf's pool from that file.
- `DeploymentSpec` has no zone field (`deployment.go:41-77`). Task 2 will change the harness, and the change needs a harness test.
- Task 2's Step 4 runs before Task 3's items exist, so R186's "not exercised is an error" must not count those items.

**M8. Review Focus.**
- Focus 2 (the reader) is already well covered by its tests. Replace it with "items that cannot fail by design" (I3, M3, and I5's equal order).
- Widen focus 4 to "the person's machine untouched" (C2, I2).
- Add "the coupling between the walk's items" (I7).

**M9. Not worth its cost.**
- M (C1), and item 5's restore (I3).
- R190's Path 2 repeats what 9a-2's gate saw, the standby answering the very next poll. It costs a third instance and changes to the world's names. Keep it only if it stays cheap.

## Checked and sound

- **Trust.** Each harness server gets a fresh leaf key (`harness/ca.go:62-95`, `webtest/ca.go:93-118`), and the stub and the IdP present the leaf alone. Pinning their two SPKIs trusts nothing else, not even the scripted MCP servers signed by the same CA. A gate check that an MCP server's URL fails would show this on the real environment.
- **R190 and the lease.**
  - A standby's take locks the lease row only once the row has expired (`lease.go:76`). So a SIGSTOPped standby cannot block the leader's release.
  - A standby stopped in the middle of a transaction is ended by the 2 s idle timeout (`holding_faults_test.go:38-43`).
  - After SIGCONT, the take comes within TakeEvery, 1 s.
- **R191.** The drop of a lower order, and the recovery after it, are verified by the probe (I5's run with Content-Type).
- **Item 6.**
  - The key ring reads the key again from 50 s (`keys.go:197`), so the first 401 in (a) comes within about 55 s.
  - In (b), the second 401 comes 50-55 s after the first. That is inside live.js's 60 s guard (`live.js:73`), with about 5 s of margin.
  - The IdP redirects at once (`idp.go:303-340`), so a sign-in commits one document.
- **The favicon.** It gets a plain 404, since no route matches it (`pages.go:113-142`). It never goes through sign-in.

## Declined to judge

- Whether the throwaway Chrome changed the person's crash-upload consent or metrics files. That needs reading `~/Library/Application Support/Google/Chrome`, which I did not do.
- The real timings of the gate's world: the drain's length, the latency from an event to the DOM, the 503s. These come from the code and its constants, not from measurement, since no Docker test was run.
- What the spike's runs, which had no resolver rule, actually sent. Chrome or Chromium on Linux. Safari.
