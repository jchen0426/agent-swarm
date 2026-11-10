# Plan 9a-2 plan review, v1

| | |
|---|---|
| Reviewer | Opus 5.5, fresh context, read-only, against HEAD 0abbecc |
| Under review | `plan/2026-11-10-plan-9a2-live-board.md`, draft v1 |
| Authority | The spec, then the design at v2.1 (`plan/2026-11-10-plan-9a2-live-board-design.md`). Its Section 3 rulings are not reopened. Its reviews were read for context. |
| Method | Every code claim named below was read at HEAD. pgxpool v5.10.0 was read in the module cache. Nothing was run. Claims that could not be checked are marked **unverified** and listed in Section 9. |
| Verdict | **Approve after the Important findings are answered. 0 Critical, 7 Important, 12 Minor.** The code claims mostly hold; R136 to R168 are sound but for R153's cost line and two scope gaps (R141 with R143, R156). The weaknesses are three hand-offs between tasks that no test crosses: the lifecycle facts (I1), the regions' presence (I2) and the sentence model (I3). They are followed by a quote that counts but is never marked (I4), two resource bounds (I5, I6), and a decision the author should see before approving B (I7). |

## 1. Summary

| # | Sev | Where | Title |
|---|---|---|---|
| I1 | Important | Tasks 2, 3, 5 | The projection leaves `NotStaffed` and `Closed` zero, and `BoardFacts` lacks them: rows 7 and 9, and row 14's not-staffed case, are lost in production, while the page tests pass on hand-built fixtures |
| I2 | Important | Tasks 5, 8 | No region is required to exist when empty. `live.js` is undefined when a region is missing on either side, so a sibling linking in or a lease change throws, polling stops, and the bar keeps reading "live" |
| I3 | Important | Tasks 5, 6; Review Focus 1 | No rendering model is fixed for sentences that embed stored values. Go-built lines, 9a-1's pattern, bypass `<bdi>` and `inert`, and Review Focus 1's assertions pass vacuously for a field that bypasses `inert` |
| I4 | Important | Task 4 (R153) | A quote that counts by the raw-text rule (`quote.go:30`) and includes JSON syntax or escapes is never marked. R153's cost line covers only decoded joins |
| I5 | Important | Tasks 3, 5 (R141, R143) | The kill switch's page and its POST read through the 2-connection web pool with no bound, so stopping queues behind viewers' polls |
| I6 | Important | Task 3 (strip) | The strip's build is unbounded and not shared: every sibling is folded to sort by `OpenedAt`, failures are not cached, and all of it runs inside the read's 2 s. A large incident's viewers starve the web pool (the magnitude is unverified) |
| I7 | Important | Decision B; task order | B approves in advance running Tasks 9 and 10 and the branch's end past R-f's stop line. The text does not say so |
| M1 | Minor | Task 2 (D5) | W omits `SettlePoll`. The real-rounds subtests are not pinned at zero slack, so a W missing a term can pass |
| M2 | Minor | Task 3 (R137) | The shared probe has no context of its own and no failure rule |
| M3 | Minor | Tasks 5, 8 (R156) | Region versions are complete only if the models hold rendered words. The ETag test runs on a frozen clock |
| M4 | Minor | Task 8 | `If-None-Match` is compared strongly. A proxy that gzips weakens the ETag, after which no poll gets a 304 |
| M5 | Minor | Global Constraints vs D11 | Quotes and titles go through `inertLine` in the plan but `inert` in D11: two contradictory instructions in one brief |
| M6 | Minor | Words table | Routing values shown unquoted as `k=v, …` can read as extra members |
| M7 | Minor | Task 3 | The wiring test proves one of the web's seven store ports is on the web pool |
| M8 | Minor | R143 | A `Revisions.At` miss from a viewer reads git, builds the revision and writes it on the main pool, and evicts entries from the coordinator's 32-entry cache |
| M9 | Minor | R142, Task 10 | The examples' three "1m27s" are already stale and are not in the list |
| M10 | Minor | Tasks 1, 4, 5, 6, 8 | Executability nits (nine, listed) |
| M11 | Minor | The browser gate; R163 | Checklist gaps, artifacts of the browser tool, and the LAN option's exposure |
| M12 | Minor | Several | Citation and scope nits, a flake in scenario 33, per-poll Error lines |

## 2. What was verified and holds

- **Citations.** Every file:line I checked holds, with the two exceptions in M9 and M12(a):
  - assess.go:170-205, 224-282, 259-282, 266; state.go:427; board.go:133; status.go:19-35; quote.go:17-37;
  - decide.go:14-19, 119-122, 169-171, 200-213; state.go:662-704; extend.go:10-19; item.go:426;
  - rounds.go:167-168, 291-295, 310-337, 320; turns.go:218-238; queue.go:273-276; coordinator.go:886-888;
  - views.go:251-306, 454-511; boards.go:27-34, 242-257; autoclose.go:53; revision.go:64; names.go:51-74;
  - pages/board.go:19, 154, 129-203, 226-244; headers.go:17; server.go:153-171; standby.go:11;
  - oidc.go:357-382; csrf.go:21-52, 43-48; keys.go:131, 135, 138-144, 149; layout.go:47-62, 81-93;
  - serve.go:490, 588, 611-619, 626, 1040; serve_test.go:584-586; serve_integration_test.go:190, 248, 260;
  - security_test.go:93, 128; board_test.go:178; web_test.go:236;
  - harness/ca.go:83-84; webtest/ca.go:106-107; run.sh:33-40; Makefile:205.
- **R138's probe.** It is one statement, so one snapshot. `CROSS JOIN LATERAL` drops an item with no record. The `run_created` lateral reads the primary key or `work_records_by_kind`, and the entry lateral reads `board_events`' primary key (migrations 0007 and 0001). With no run, `coalesce` gives `''` and 0. It is sound.
- **R139.** The coordinator's view is published at queue.go:273-276 and reset at the fence (coordinator.go:886-888). Reading the table once a tick is a strict improvement, at the stated cost.
- **R141.** pgxpool v5.10.0's `Config` has no acquire bound: `MaxConns`, `MaxConnLifetime`, `MaxConnIdleTime`, `PingTimeout` and the hooks, and `Acquire` waits on its context. `MaxConns` 0 keeps `ParseConfig`'s default: the URL's `pool_max_conns`, else `max(4, NumCPU)` (`pool.go:19, 370-385`).
- **R142.** The close term goes from 8 to 9 `stopWait`, and the defaults from 97 s to 102 s. `TestTheExampleUnitStopsAfterTheWorstDrain` still passes, since `TimeoutStopSec=120` (`examples/two-instances/agentswarm@.service:31`) leaves 18 s.
- **R145.** Platform evidence takes no stance (state.go:427). An author record counts only the author's own evidence (assess.go:266), and only agents post hypotheses (state.go:472-473). So platform evidence never backs a counted record. All human evidence shares `HumanObservation` (board.go:133). Sound. The spec's "platform evidence first" therefore never applies under 4.3 as written (see Section 6).
- **R147.** `StatusOf` has one production caller (pages/board.go:154).
- **R151.** `app.Inbound` is implemented by the interleaving world's `recordsPort` (interleave_world_test.go:400) and by the harness's `failing` (harness_test.go:1244). A separate `InboundTitles` port leaves both untouched. Sound.
- **R152: no words, no switching code, no interleaving change.**
  - `TestEveryRefusalCodeHasWebWords` enumerates `Code*` constants only (security_test.go:128-145).
  - Nothing in `internal/admin`, `switching` or the interleaving test enumerates `Act*` kinds.
  - `actOf` answers 404 for an act with no words (acts.go:113-118).
  - `policy_test.go`'s explicit kind lists (`:79`, `:126-130`) need `ActReadOutput` rows, which Task 4 names. The source actor's map needs `ActReadOutput: CodeNotItemResponder` too.
- **R158.** `clock.DB.Now` is memory alone (clock/db.go:102-111). A standby samples before each take attempt (leader.go:271), so its estimate is the database's.
- **R161.** `SignedIn` answers a GET with no session with a 303 (authenticator.go:144-156). `Polled` avoids that redirect.
- **R162.** The board page is `Views.Item`'s only web caller (pages/board.go:72).
- **The CSP.** The board policy equals `contentSecurityPolicy` (headers.go:17) plus the four directives, byte for byte. `secured` sets `nosniff` on every response (headers.go:28), so under `script-src 'self'` no same-origin HTML response, a raw output's or the regions', can run as script.
- **D5's formula** is sound against decide.go and rounds.go, with M1's addition. The real-rounds test drives the real coordinator through `newHarness` (harness_test.go:1166), with `spend.inFlight` (`:382`), `failing.set` (`:898`) and `h.revision` (`:1206`).
- **The harness.** The scenario helpers exist: `alertServing`, `firing`, `marker`, `unheard`, `shutGate`, `withWeb`, `quickRetry`. The stub routes to the instance whose `/leaderz` answers 200 (harness/web.go:213-233), which matches re-review N4.
- **Design coverage.** D1 to D15 and rulings 1 to 11 each land in a task (Section 7). Nothing the design excludes crept in. R148, R163 and R168 go beyond the design, and each is flagged as a ruling.

## 3. Important

### I1. The projection drops the record-determined lifecycle facts (Tasks 2, 3, 5)

**Failure.**
- **What the interface says.** Task 2 fixes `BoardView.View` as "Item's, its lifecycle facts and IncidentItems left zero (BoardFacts, Strip)" (plan line 404). `Views.lifecycle` sets more than the facts of now (views.go:434-452):
  - `QueuePosition`, the auto-close flags and `ClosesAt`, which are facts of now;
  - `NotStaffed`, from `notStaffed(s.Closing)`, and `Closed`, with its reopen and late-ruling windows. Both are determined by the records.
- **What is lost.** `BoardFacts` (plan lines 578-587) carries neither of the last two, and `BoardView.state` is unexported (line 413), so the page cannot derive them. The 9a-1 page reads both:
  - `NotStaffed` for the kill switch banner (pages/board.go:108) and row 7's cause (`:193-195`);
  - `Closed` for row 9 (`:199-200`, `:256-268`).
  - Built as written, production loses row 7's cause, row 9's close and reopen window, and row 14 for "the item left not staffed by it".
- **Why no test sees it.** Task 5's tests build `BoardRead` fixtures by hand (R162), so `TestEveryBoardStateRowRendersItsText` sets those fields itself and passes. Task 2's `TestABoardViewHoldsItsHeadersFacts` does not assert them, and scenarios 33 and 34 reach neither row.

**Fix.**
- `BoardView.View` keeps the record-determined parts of `lifecycle`: `NotStaffed`, and `Closed` with `ReopenableUntil` and `LateRulingsUntil`.
- Only `QueuePosition`, `AutoClosePaused`, `AutoCloseUnread`, `ClosesAt` and `ClosesAtUnread` move to `BoardFacts`.
- `TestABoardViewHoldsItsHeadersFacts` gains rows for:
  - a not-staffed item by routing;
  - one left not staffed by an on-row (`kill_switch`);
  - a closed item, by a person and by itself.

### I2. Regions may be absent, and `live.js` breaks on an absent one, with the bar still reading "live" (Tasks 5, 8)

**Failure.**
- **Absent regions are allowed.** Task 5 fixes seven region names and "each region a `<section id=… data-v=…>`" (plan lines 741-749). Nothing says a region exists when it has nothing to show, and three of them naturally do not:
  - the strip shows "only when the incident has other items" (design D7);
  - the standby notice is `{{with .Standby}}` (layout.html:19), absent on a leader;
  - 9a-1's sections are conditional (board.html:13, 18), and there is no summary before round 1.
- **The script assumes both nodes exist.** Step 6 does `old.replaceWith(document.adoptNode(fresh))` (line 886). When either side lacks a region, that is a `TypeError`.
- **The trigger is common.** 7c-1b links a flapped item into a viewed item's incident, which makes the strip appear. A lease change makes the standby notice appear or go.
- **The indicator lies.** It is maintained by the poll loop (step 10, line 890). If the handler throws before it schedules the next poll, the page freezes, and its bar reads "live" for good. That is the one thing spec 10.2's indicator exists to prevent.
- **Nothing catches it before people do.** No test runs the script (R166). The gate's environment links its second item at the start (line 950), so the gate never sees a strip appear.

**Fix.**
- **Task 5.** Every name in `regionNames` renders on every board page, and in every regions document, as an empty `<section>` when it has nothing. A test asserts all seven ids on a lone item, on a leader, before round 1.
- **Task 8, step 6.** It skips a region missing on either side and counts that as an error.
- **Task 8, every handler.** Each runs in a `try`/`finally` that schedules the next poll, so a fault reads as "reconnecting", never as "live". Add the check to the gate's item 2 (open a lone item, then link a sibling into its incident).

### I3. No sentence model for words with stored values in them, and Review Focus 1 cannot see a bypass (Tasks 5, 6)

**Failure.**
- **The words embed values, and nothing says how they render.** Most rows of the words table put values inside sentences ("Closed by `<By>`, reason …", "`<agent>` posted evidence (supporting `<key>`) …"). The Global Constraint requires each value in the template as `<bdi>{{inert .X}}</bdi>` (line 56). But no task fixes how a sentence with values reaches the template. Task 6 "produces `recordWords`, `entryWords` …" with no type (line 782).
- **The precedent bypasses the rule.** 9a-1 builds lines in Go, `"Closed at " + … + "by " + c.By` rendered `{{.}}` (pages/board.go:255-268, board.html:9). That form escapes, but drops both `<bdi>` and the markers.
- **The tests miss it.** `TestEveryInertCallSiteIsIsolatedAndEscaped` greps the templates only (line 759). Review Focus 1's oracle (line 973), "every `⟨U+202E⟩` inside a `<bdi>`" and "the markup escaped", holds for a field that never went through `inert`: such a field shows a raw U+202E and no marker, and html/template escapes it anyway.
- **What it costs.** Input checks refuse hidden runes in most stored fields, but not strong right-to-left letters. Those reorder the platform's neutral words and digits around them, which is what `<bdi>` is for (D11, the design's anti-goal).

**Fix.**
- **Task 5's Interfaces.** Fix one part model, for example `type part struct{ Text, Name, Data, Model, Person string }`, and one named template that renders each kind as the Global Constraint says.
- **The words functions** return `[]part`, never a string that holds a value.
- **Review Focus 1's test** asserts that:
  - no body holds a raw U+202E or U+200B;
  - each page shows exactly as many `⟨U+202E⟩` as hostile fields it renders, each inside `<bdi>`;
  - each single-line field shows `⟨U+000A⟩`.

### I4. R153 never marks a quote that counted against the raw text (Task 4)

**Failure.**
- **How a quote counts.** `QuoteAppears` first tries the quote against the normalized *raw* output (quote.go:30), and only then against each decoded string (`:33-36`).
- **What the outline can mark.** R153 marks only the outline's decoded lines or windows (line 87). A quote of a raw JSON fragment counts at post but lies in no outline line, for example:
  - `"code": 504`, shown in the outline as `code: 504`;
  - an escaped `a\nb`, shown as two lines.
- **What the reader sees.** The raw-output page then marks nothing for evidence that counted, and a responder checking it reads the quote as absent.
- **The cost line is incomplete.** R153 says "a quote that joins a key and its value is not marked (it never counted either)". That is true of a *decoded* join alone. Agents often quote raw JSON fragments.

**Fix.**
- When a cited quote lies in no outline window but does lie in the raw output's normalized text, the page also shows the output as stored, under "As stored, where the quote lies": wrapped, numbered, and marked by the same window rule.
- Add `TestARawFormQuoteIsMarkedInTheOutputAsStored`.
- Correct R153's cost line.

### I5. The kill switch queues behind viewers (Tasks 3, 5; R141, R143)

**Failure.**
- **Where the reads go.** R143 moves "the kill switch's reads … and the lease holder" to the web pool (line 77). Task 3 gives the web's views the web's `Controls` (line 636), and serve hands those views to every page (serve.go:589, 611-619). R141 leaves "the other pages" without a bound (line 75).
- **What the POST waits for.** `switchCommit` reads the frame, `Views.KillSwitch` and the standby holder, before `switching.Set`, and again after it (killswitch.go:161-170, 185-188; layout.go:47-60).
- **What else holds the pool.** Its 2 connections are shared with every poll's probe and with the shared builds, each of which may hold a connection for up to `BuildWait`, 10 s.
- **The consequence.** During an incident, when many people watch boards, turning the switch on waits behind them.
- **Why it matters.** Spec 8.3 asks for the switch reachable from every board and for stopping to stay fast (design review I2). Ruling 2's cost line names viewers' waits, not the switch's.

**Fix.** Either of:
- **Bound the switch's reads.** The frame's kill-switch read and `UnstaffedBy` take the standby holder's 1 s bound (standby.go:11). Past it they show "could not be read", and the POST goes on to the coordinator, which writes on the main pool anyway.
- **Keep them off the web pool.** The kill switch page's reads stay on the main pool, as a ruling: they come at a person's rate, not a viewer's.

Whichever is chosen, the wiring test adds: with both web connections held, `POST /kill-switch` on reaches the coordinator within 2 s.

### I6. The strip's build is unbounded and not shared (Task 3)

**Failure.**
- **Every sibling is folded.** Task 3 folds "each other item's records", sorts by `OpenedAt` and keeps 10 (line 634). Item ids are SHA-256 hashes (opening.go:19-26), and `IncidentItems` sorts by id (postgres/records.go:108-121). So ordering by `OpenedAt` needs every sibling folded.
- **Conclusion reads are unbounded too.** An `Entry` read is made for "a visible sibling whose run stopped". Disagreement compares with "another visible item", which is not limited to the 10 shown.
- **Nothing is shared.** The projections get a single flight and a detached bound (line 633); the strip gets neither. A failed build reads `Unread` and is not cached. All of it runs inside the read's 2 s (R141).
- **The likely failure (magnitude unverified).** An alert storm gathers many items into one incident, and that is when many people watch it (design review I4). When the 30 s cache expires, each viewer's poll rebuilds the strip on 2 connections. A build that passes 2 s fails and stays uncached, so the next poll repeats it. The probe and the projections then wait behind it, and the boards answer 503.
- **The cache grows.** The per-incident cache has no eviction.

**Fix.**
- **Share the build.** One strip build per incident at a time, apart from requests (`WithoutCancel`, `BuildWait`).
- **Keep the last good strip.** A failure is cached for a few seconds, and the last good strip is served meanwhile, with its age.
- **Bound the reads.**
  - Order siblings with one read of their `item_opened` records' times.
  - Fold only the 10 shown.
  - Read conclusions for those 10 and this item, and say that disagreement is judged among them.
- **Evict.** A strip unread for 30 s is dropped.

### I7. Decision B approves more than the cut (Decision B; task order)

**Failure.**
- **What B approves.** When the stop line has passed, B cuts Task 8 and goes on: "the no-JS board ships, scenario 33 runs without its poll steps, the gate checks the no-JS board" (lines 34-36).
- **What is left after the cut.** Task 9 (2 raw days), Task 10, and the branch's end: two `make test-linux` runs, the gate, the final review and its fix wave. All of it would run after R-f's trigger fired, which the Global Constraints say stops the plan for the author (line 64). "Approving B approves that answer … in advance" approves this too, without saying so.
- **The order makes it larger than it needs to be.** Task 9 comes after Task 8, though only its step 6 needs Task 8 (R164).

**Fix.**
- **Reorder.** Move Task 9 before Task 8, with step 6 and `Poll` joining Task 8. A cut then leaves only Task 10 and the branch's end.
- **Say it in B.** State, on its own line, that after a cut the plan runs past the stop line to the branch's end. Or choose instead: after the cut, stop for the author.

## 4. Minor

**M1. W misses `SettlePoll`, and the D5 test may not bite on each term (Task 2).**
- **The gap.** `settle` polls every `SettlePoll` until `Grace + SettleWait` has passed (rounds.go:317-322), so a decision can land up to one `SettlePoll` after it, plus timer drift. A failed end then waits `Retry` (`:333-336`). W is Grace + Settle + Retry (line 476), and `SettlePoll` is deployable (deploy.go:62).
- **The test's blind spot.** Subtests 1 and 3 do not run at zero slack, so the slack in n·W or k·(L+W) can absorb a missing term.
- **Fix.**
  - `Waits` gains `Poll`, and W = Grace + SettleWait + SettlePoll + Retry.
  - Pin subtest 3's failing end to the final wrap-up of subtest 2's run, with calls in flight.
  - Add a mutation row: `StopBy` with any W term less 1 s fails some subtest.

**M2. The shared probe has no context of its own and no failure rule (Task 3, R137).**
- **Context.** A probe run under its first waiter's 2 s context fails for every waiter when that client cancels or times out.
- **Failure.** The plan says nothing of what a failed probe gives.
- **Fix.** The probe runs detached, with a 1 s bound, and each waiter waits under its own context. A failed probe keeps the last result for up to 10 s and sets `KillSwitchRead` false.

**M3. R156 holds only if the models hold rendered words (Tasks 5, 8).**
- **The trap.** If a region's model holds `Facts.Now` to the second, the ETag changes on every poll. If a template function reads the clock, or a model has unexported state, the ETag freezes while the page's text changes.
- **The test's blind spot.** `TestTheETagChanges…`'s row "nothing changed, the same" passes either way on a frozen test clock.
- **Fix.**
  - Models are plain exported data: words already rendered, times formatted to the minute. Templates call no clock.
  - Add a row: the clock moves 30 s within one minute, and the ETag is the same.

**M4. Strong ETag comparison (Task 8).**
- **The gap.** "`If-None-Match`, trimmed, equal to the `ETag`" (line 878) compares strongly. RFC 9110 §13.1.2 makes `If-None-Match` a weak comparison.
- **The consequence.** A proxy that gzips text/html commonly turns `"…"` into `W/"…"` (nginx documents this; **unverified** for the deployment's proxy). After the first 200, the script would send `W/"…"`, and every poll would answer 200.
- **Fix.** Compare weakly, and accept a list. `live.js` takes the ETag from the regions document's `#board[data-etag]` rather than the header.

**M5. Quotes and titles: `inertLine` or `inert`? (Global Constraints, words table, D11).**
- **The contradiction.**
  - The plan renders quoted data through `inertLine` (line 56), and its words table treats titles and quotes as quoted data (line 106).
  - D11, which the briefs carry verbatim, puts quotes and titles under `inert`, with line feeds kept.
- **Quotes do hold line feeds.** The board checks only a quote's length (state.go:456, 561-569).
- **Fix.** Make it a ruling, either way, with its cost.

**M6. Routing shown as `k=v, …` (words table, Tasks 2 and 6).**
- **The gap.** `checkRouting` refuses unseen runes and redaction changes, but allows `, ` and `=` in a value (coordinator.go:1521-1541). A value `checkout, region=eu-1` then reads as two members of the platform's facts.
- **Fix.** Render each member as `<k>: <q class="data">v</q>`, or one member per line, and add a routing value to Review Focus 2.

**M7. The wiring test covers one port (Task 3).**
- **The gap.** `TestAViewerWhose…` (line 658) proves that `Boards` is on the web pool. R143 also moves `List`, `KillSwitch`, `UnstaffedBy`, `LeaseHolder` and `ReadCalls`, and nothing tests them.
- **Fix.** With `s.web`'s connections held, each port fails within its bound, and `s.main`'s `AcquireCount` is unchanged across a round of web reads.

**M8. Viewers can make the registry read git and write on the main pool (R143).**
- **The gap.** "Read from its cache" holds only on a hit. A miss reads the commit from git, builds it, and records it on the main pool (registry.go:106-118; serve.go:468). The cache holds 32 entries (registry.go:34).
- **The consequence.** A viewer who opens old items' boards costs a build and a main-pool transaction for each, and evicts revisions the coordinator relies on.
- **Fix.** The web's views take a cache-only `Revisions`: a miss gives "service not compared" or "auto-close time not known". Or name the cost in R143.

**M9. Stale worst-drain text (R142, Task 10).** `examples/two-instances/a.yaml:78`, `b.yaml:70` and `README.md:213` say "1m27s", stale since 7c-1b. Add them to Task 10.

**M10. Executability nits.**
- (a) **Task 1.** "Every board of `assess_test.go`'s table": the file holds separate tests and no table (assess_test.go:14-287). Name the boards to rebuild.
- (b) **Task 4.** "Four numbered lines" for `{"message":…}` cannot be derived from R153: does the top-level object take a line, and with what text? Give the expected lines.
- (c) **Tasks 5 and 6.** `regionNames`, "in the page's order", puts `summary` after `feed`, while `TestTheFeedIsLastAndOpen` (line 792) asserts that the feed comes last.
- (d) **Task 5.**
  - Render each region from its own named template, since Task 8's `regions.html` reuses them.
  - Say where `<main id="board" data-etag data-order>` goes, since layout.html:23 has its own `<main>`.
- (e) **Tasks 4 and 5.** `boardWait` is produced by Task 5, but Task 4's route already reads "under 2 s".
- (f) **Task 1.** `SupportBy` counts an entry behind two records (case 10) twice. Count distinct backing entries.
- (g) **Task 8, step 9.** "None at start" announces the page's own round on the first replacement. Start from the page's `data-round`.
- (h) **The round summary.** Say whether its status and key come from the latest ended round's report (`View.Rounds`) or from the current assessment.
- (i) **Task 8's script route.** Serve it as the stylesheet is, outside `SignedIn` (pages.go:103). Behind it, a script load after the session expired starts a sign-in flow, which mints a flow cookie (authenticator.go:144-156; review I1.4).

**M11. The browser gate and its environment.**
- (a) **Missing items.** Each is one line:
  - `?feed=all` survives a poll;
  - back and forward (`pageshow`) polls at once;
  - `#announce` changes once per round;
  - a 503 backs off 10, 20, 40, then 60 s.
- (b) **The browser tool.** Claude-in-Chrome drives the author's own Chrome. It must pass the 127.0.0.1 certificate warning, and its injected scripts may log CSP or Trusted Types lines of their own on a board page (both **unverified**). Read item 8's console before any injection, or attribute each line to its source.
- (c) **R163's LAN option.**
  - **The exposure.** The fake IdP "signs its person in at once" (webtest/idp.go:45-46), so anyone on the Wi-Fi signs in as the gate's person, with its groups, for the gate's minutes.
  - **The CA.** The harness CA has no name constraints (harness/ca.go:42-49). Its key stays in memory, since env.go:156-157 writes the certificate alone.
  - **The database (unverified).** testcontainers may publish Postgres on all interfaces.
  - **Fix.**
    - Recommend the iOS Simulator first.
    - With a LAN address, forward only to an allowlisted client address (`AGENTSWARM_BROWSER_GATE_CLIENT`).
    - Tell the person to accept the warning, not to install the CA.
    - Name-constrain the gate's CA to the LAN address.

**M12. Citation and scope nits.**
- (a) **R139.** "Within 9a-1's 1 s bound (`standby.go:11`)": `holderTimeout` is a read timeout (standby.go:8-11), not a freshness bound. The 1 s cache is harmless.
- (b) **Scope.** The Global Constraint covers "every stored text … a page", but R154 lists the layout, the board pages, the raw output and the items list. These remain without `inert` or `<bdi>`: the leader's name in act.html:8, killswitch.html:7 and :27 and open.html:7, and the reason text in act.html:26. Widen R154, or narrow the constraint.
- (c) **Scenario 33, step 6.**
  - The 1 s tick can answer 304 just after the event, so poll for the 200 with `Eventually`.
  - A minute's rollover between the 200 and the last poll gives a 200 rather than a 304. Retry it once.
- (d) **Logging.** A poll's read error logs as the page's, at Error (pages/board.go:86-89), every 5 s for each viewer during a database fault. Rate-limit it, as the Host refusal is (headers.go:68-108).

## 5. The security lens

- **Stored text to a page.**
  - html/template escapes everything, and R154's forbidigo rule removes the typed strings; none is used today, and forbidigo is already enabled (.golangci.yml:38, 120-123).
  - The gaps are isolation and markers, not markup: I3 (the sentence model), M5 (quotes), M6 (routing), M12(b) (other pages).
- **Platform words spoofed.**
  - The typed rendering of `item:` and `trigger:` evidence is sound. It matches on `(Provenance.Source, ref)`, the refs come from rounds.go:35 and ingest.go:297, and the claim string is never shown.
  - The record's instance and event ids were refused at mapping if redaction would change them (mapping.go:261-290).
  - Routing (M6) is the remaining hole.
- **`live.js`.**
  - XHR documents plus `adoptNode`, `trusted-types 'none'` and the exact board policy are sound.
  - R166's lint over the embedded file is the only automated check. Its no-space rule still allows `matchMedia("(min-width:60em)")`.
  - The script's robustness is the gap (I2).
- **The raw-output gate.** It needs `Member` grounds (never `AdminOverride`), a cited call, and the run's check; a hidden item answers 404; every refusal has one body. Sound.
  - The "no longer kept" page is reached only after the member and citation checks, so it is no oracle.
  - A viewer-role person named as a responder gets `Member` grounds, which conforms to spec 10.1's "the item's responders".
- **The 401 and session rotation.** `Polled` gives 401 with no body, no cookie and no redirect, and `Available` answers 503 while the key cannot be read. The script reloads at most once in 60 s and treats a storage that throws as a reload just made. Review Focus 4's test is real.
- **A hidden item's leaks.** The hidden regions' models hold the copy, the state word, the switch and the standby notice; no order is sent; the call ids answer 404. Review Focus 5's test is real. The build's timing is carried to 8a, as R144 says.
- **Resource bounds.** The web pool of 2, one probe a second and the 64-projection cache hold. The strip (I6), the kill switch's reads (I5) and registry misses (M8) do not.

## 6. The author's decisions, and decisions hidden in the plan

- **Decision A.** The recommendation is sound. The cost line understates the risk: a fault particular to WebKit may leave "live" on a frozen page (I2), not merely fall back to reloading. Fold in M11(b) and (c): prefer the iOS Simulator, and have the person accept the warning rather than install a CA.
- **Decision B.** See I7: as written, B also approves running about 2.5 raw days and the branch's end after the trigger fired.
- **Hidden decisions worth a line each in the approval message:**
  1. **The switch's pool.** The kill switch page reads on the viewers' pool, with no bound (I5).
  2. **The worst drain.** `worstDrain` rises to 1m42s for every deployment, web block or not, which leaves 18 s under the example's `TimeoutStopSec=120`. A deployment with raised drain timings must check its supervisor's timeout. The README should say so as an upgrade note.
  3. **Platform evidence first.** R145 makes the spec's "platform evidence first" dead text under 4.3. The author may want a spec note in Task 10, or a later rule that quotes a hypothesis's cited platform evidence.
  4. **Quotes and titles.** They go through `inertLine`, contrary to D11 (M5).
  5. **The LAN option.** R163's LAN option puts an open fake IdP on the network (M11(c)).
  6. **Raw-form quotes.** They are not marked (I4), unless I4's fix is taken.

## 7. Design coverage

| Design | Lands in | Test that fails without it | Gap |
|---|---|---|---|
| D1 polling, regions, no sink, order, focus, indicator, sign-in, summary, phone feed | Tasks 5, 8 | 304 and 200, ETag, X-Board-Order, the board policy, the script's lint, the 401 tests; focus, indicator and summary at the gate, by design | I2, M3, M4, M10(g) |
| D2 version, probe, cache, strip, web pool, logging | Tasks 2, 3, 8, 10 | the probe in both stores, one probe a second, built once, never older, the wait, the wiring test | I5, I6, M2, M7 |
| D3 one projection | Task 2 | `TestABoardViewIsTheProjectionOfItsVersion` | I1 |
| D4 the quote rule | Task 1 | `TestQuotesFollowTheFixedRule`, the shared walk | M10(a) |
| D5 status words and times | Tasks 1, 2, 5 | the real-rounds test, confirming, superseded, rounding to the minute | M1 |
| D6 suggestions | Tasks 2, 5 | groups, superseded, the service mark, support | M10(f) |
| D7 the strip | Tasks 3, 5 | newest first, hidden siblings, disagreement within a type, unreadable, unread, cached | I6 |
| D8 the feed | Tasks 2, 6 | merge order, folds, typed platform evidence, titles, paging | I3, M6 |
| D9 notices | Tasks 2, 6 | repeats with `Skill`, every code has words | — |
| D10 raw outputs | Task 4 | members alone, decoded, cut, not kept | I4 |
| D11 inert | Tasks 4, 5, 6 | `TestInertMarksWhatItsSetHides`, the call-site grep, Review Focus 1 | I3, M5 |
| D12 layout | Task 5 | the stylesheet's 60em rule | — |
| D13 hidden items | Tasks 3, 5, 8 | Review Focus 5, the hidden sibling | — |
| D14 carries | Task 7 | subject, standby clock, stale form | — |
| D15 tests and gates | Task 9, the branch's end | scenarios 33 and 34, the gate | M11 |
| Rulings 1 to 11 | 1: Tasks 8, 10; 2: Task 3; 3 and 4: Task 8; 5: Task 3; 6, 8, 11: Task 2; 7: Task 4; 9: Task 7; 10: "Not in this plan" | as above | — |

## 8. Gates, size, spend and calendar

- **Gates.**
  - **Each task.** Lint twice, `-race`, and the interleaving default run for Tasks 1 to 4.
  - **The branch's end.** `make test-linux` before and after the fix wave.
  - **No soak.** Justified: the lifecycle does not change, and serve's change is pools and the drain's arithmetic.
  - **The Docker path.** It is in `make test-linux`: `run.sh linux` runs the whole suite (run.sh:1-6).
- **Size.** Two tasks look light, each by about half a day:
  - Task 3, at 2–2.5 days, carries about 20 tests, three of them integration tests, plus serve's wiring and a `db` option.
  - Task 6, at 1.5–2, must cover every stored field on four surfaces for Review Focus 1 and 2.
  - The fixes above add about half a day, I6 and I3 most of it.
  - About 16–20 raw days in all, about 3.2–4 pw.
- **Calendar.** About 1–1.5 days is credible against 7c-1b's precedent: about 16 raw days in about 6 hours. The iPhone half waits for the author and is not counted.
- **Spend.**
  - **The guess.** $20–28 is above both precedents per raw day: 7c-1b came to about $17 for 16–17.5 days, and 9a-1 to about $20 for 22.5–27.5. It is conservative for three doubly reviewed tasks and an untested script.
  - **The stop line.** $56 will not bind, since the ledger prices at the input rate only.
  - **The recalibration.** Task 1 is a light pure task, so R93's recalibration from it would underestimate the web tasks, but its floor at the guess protects the plan.

## 9. Unverified

- Whether Claude-in-Chrome can pass the certificate warning for 127.0.0.1, and whether its injections log CSP or Trusted Types lines (M11(b)).
- Whether the deployment's proxy weakens or drops ETags (M4).
- The strip's build time on an incident of many items (I6).
- Whether testcontainers publishes the gate's Postgres on all interfaces on this Mac (M11(c)).
- Per-engine behaviour of `adoptNode` from an XHR document, and of `<details>` state. Both are left to the gate, by design.
