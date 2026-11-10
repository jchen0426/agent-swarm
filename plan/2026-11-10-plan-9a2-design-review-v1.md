# Plan 9a-2 design review v1 (adversarial)

| | |
|---|---|
| Under review | `plan/2026-11-10-plan-9a2-live-board-design.md`, Draft v1 |
| Against | spec `plan/2026-10-11-agent-swarm-platform-spec.md` (4.2, 4.3, 5.2, 5.6, 6.3, 9.1, 9.4, 10.1, 10.2, 10.4, 15); roadmap row 9a-2; the carries of 9a-1, 7c-1a, 7c-1b, 5b and Plan 1; the code at HEAD 09f0adb |
| Method | Read-only. Every claim below was checked against the code or the documents. Two were also checked outside the repo: a scratch Go program for how `net/http` treats a long-lived stream, and the WHATWG HTML Standard (server-sent events) plus MDN (Trusted Types) for browser behaviour. Anything not verified is marked **unverified**. |
| Verdict | **0 Critical, 10 Important, 10 Minor.** Approve after the Important findings are answered. Most of them come from one source: the design's assumptions about what EventSource, the browser and the cache do. Section 7 describes a simpler transport, conditional polling, that makes four of them go away. |

## 1. Summary

| # | Sev | Section | Title |
|---|---|---|---|
| I1 | Important | D1, D3 | EventSource lacks the behaviour the design relies on: a non-200 answer ends it for good, `retry:` in a 503 body is never read, comment heartbeats reach no script, and an expired session loops through sign-in and churns flow cookies |
| I2 | Important | D3, profile | Over HTTP/1.1 a browser holds at most 6 connections per origin. The listener speaks HTTP/1 only. A sixth board tab therefore blocks every page, the kill switch's included |
| I3 | Important | D2, D4, D1 | (item, version) does not determine what the board shows. The kill switch, the queue, the lead state and a restore all escape the version, and the fixed kill-switch header is not a region |
| I4 | Important | D2, D8 | The per-item probes, the strip re-read on every regions fetch, and the slow tick put unbounded, board-sized load on the coordinator's 4–8 connection main pool. This breaks the design's own anti-goal |
| I5 | Important | D1, D13 | Replacing regions wholesale destroys keyboard focus and the screen reader's position on every entry, against spec 10.4 |
| I6 | Important | D6 | "By T at the latest" is not an upper bound. Rounds' end waits, firing resumes and takeover wrap-ups each break it, and the first breaks it in a plain run |
| I7 | Important | D5 | The quote candidates as worded leave out an author's own support, the hypothesis's cited evidence, which is the most common support there is |
| I8 | Important | D5, D9, D12 | Platform-evidence claims mix the platform's words with alert titles, so a title can spoof "the trigger is resolved" or a routing member under the platform's authorship |
| I9 | Important | D12 | `inert` as specified contradicts itself: `<bdi>` plus "html/template escapes as before" cannot both hold. Its rune set (`Unseen`) also marks every newline and tab |
| I10 | Important (latent until 8a) | D2, non-goals | The stream's version (`r<seq>.b<seq>`) and its nudges reveal a hidden run's progress. Nothing says what `/live` does for an item `visible` hides |
| M1 | Minor | D1 | The Trusted Types policy is a pass-through and cannot check where a string came from. Inert XHR documents with `trusted-types 'none'` are a meaningful alternative |
| M2 | Minor | D13 | CSS cannot open a closed `<details>`, so "collapsed on phones, open beside on wide, CSS only" cannot be built as written |
| M3 | Minor | D7 | The service mark keys on the argument name `service`. Scope bindings are CEL expressions per argument, so the mark goes silently missing |
| M4 | Minor | D6, D7 | `superseded` narrows the spec's "lost convergence" and costs two full replays per proposal per version |
| M5 | Minor | D8 | Strip gaps: "disagree" must leave out hidden items and items of other types, a sibling's own failure is unhandled, and where `visible` is applied is unsaid, as is Item()'s contract |
| M6 | Minor | D9, D10, D4 | Missing words: declined firing resumes, the auto-close held while the alert fires, `item:` refs, and the notice collapse key, which drops `Skill` |
| M7 | Minor | D11 | Admins are added to spec 10.1's raw-output rule without a spec amendment, and fresh-only `Admin` makes their links come and go. Raw JSON is shown in a form where the quotes cannot be found |
| M8 | Minor | D3 | The session re-check bound is 90 s, not 60. A drain does not end streams "at once". RegisterOnShutdown needs a seam. Regions and live requests flood the Info log |
| M9 | Minor | D1, D2 | Open point 4: yes, an older board can win. The fix needs the response's own version and one fetch in flight, and `?feed=all` must survive a refresh |
| M10 | Minor | D15, §5 | No test runs the script before 9b, and the size is light. Add a browser gate and a cut line |

## 2. Important findings

### I1. EventSource lacks the behaviour the design relies on (D1, D3)

**Failures.**
1. **A non-200 ends the stream for good.** The design answers the cap with `503` (D3). `Available` answers `503` while the session key cannot be read (`internal/web/auth/authenticator.go:90-101`). Most often of all, the proxy answers `502/503/504` for the moment an instance restarts in a deploy. Each of these *fails* the EventSource, and it never reconnects. The indicator then reads "reconnecting, last updated at T" for good, on every open board. "A takeover is only a reconnect" (D3) holds only when the proxy fails at TCP level. "EventSource reconnects by itself" (D1) is true for network errors and clean ends, and false here.
2. **`retry:` in a 503 body is never read.** Only a 200 `text/event-stream` body is interpreted, so "the script backs off a 503 by the server's `retry:`" cannot be built. A script sees an `error` event with no status at all.
3. **Comment heartbeats reach no script.** D3 sends "a comment heartbeat every [15 s]". D1's indicator needs "a heartbeat arrived in the last [30 s]". Comment lines dispatch no event, so a script never sees them. On every idle board the indicator flips to "reconnecting": between rounds, after the stop, and through 30 minutes of awaiting close. The e2e's "plain streaming client" reads the comment bytes and passes, while every browser shows the wrong word. A named heartbeat must also carry non-empty `data`; an event whose data buffer is empty is not dispatched either.
4. **An expired session loops through sign-in.** `/live` and `/board` are GETs behind `SignedIn`, which answers every GET without a live session with a `303` to `/auth/signin` (`authenticator.go:144-156`). `begin` then seals a new flow cookie and redirects to the IdP (`internal/web/auth/flow.go:229`). The IdP is cross-origin: `connect-src 'self'` and CORS block the redirect, which is a network error, and the HTML Standard's default for a network error is to re-establish the connection. Each retry mints another flow cookie. `capFlows` keeps 5 and drops the oldest (`flow.go:31, 139`), so a person signing in from another tab loses their flow after about five retries. A regions `fetch` follows the same redirect. This happens after any stream drop that outlasts the session. The typical case is a phone that sleeps overnight. (Whether a given browser retries or gives up after the blocked redirect is **unverified**. Either way the server side churns.)
5. **Phones first.** A locked phone, a backgrounded tab and the back-forward cache all close the stream. Nothing in D1 opens it again on wake.

**Evidence.** WHATWG HTML Standard, server-sent events processing model: "if res's status is not 200, or if res's Content-Type is not text/event-stream, then fail the connection"; "Once the user agent has failed the connection, it does not attempt to reconnect"; a line starting with a colon: "Ignore the line"; `retry` is parsed only while a stream is interpreted. Fetched and quoted this session. The code paths are cited above.

**Fix.**
- **The script.** On `error` with `readyState === CLOSED`, open a new EventSource after a jittered backoff, 5 s doubling to 60 s. Pass the page's version as `?v=`, since a new EventSource sends no `Last-Event-ID`. Open again on `visibilitychange` (visible) and on `pageshow`, and close while the tab is hidden (see I2).
- **The server.** Never answer `/live` with a non-200 the server controls:
  - the cap answers `200 text/event-stream` with `retry: 10000` and ends at once, so EventSource reconnects by itself after 10 s;
  - an expired session or a rotated key answers `200` with `event: signin` and ends;
  - a session key that cannot be read answers `200` with `retry:` and ends.
  `/live` and `/board` bypass `SignedIn`'s redirect. `/board` answers `401` with no body, and the script reloads the page on a `401` or a `signin` event, so sign-in only ever begins from a top-level navigation.
- **The heartbeat** is `event: ping` / `data: 1`, and the indicator keys on it.
- **The e2e client** parses the stream as the Standard's EventSource does and asserts what would be dispatched, not raw bytes.

### I2. A browser's six HTTP/1.1 connections are held by streams (D3; the deployment profile)

**Failure.** The web listener is HTTP/1 only (`internal/web/server.go:145-147`). The profile's proxy line names no protocol to browsers (`plan/2026-10-13-example-deployment-profile.md:169`). Over HTTP/1.1 a browser opens at most 6 connections per origin, and each open board tab's EventSource holds one for its whole life. A responder following the incident strip opens boards in tabs. With six board tabs open, streams hold all six connections, and every other request from that browser waits until one closes: the items list, a promote form, the kill switch page, and the six tabs' own regions fetches, so the live updates stall as well. Spec 8.3 wants the switch "reachable from every board" and stopping to "stay fast". The HTML Standard itself warns of trouble "when opening multiple pages" that each hold an EventSource. The browser figure of 6 is well known but **unverified here**; whether the deployment's proxy speaks HTTP/2 is unknown.

**Fix.**
- The profile requires HTTP/2 between browsers and the proxy, and the design states it as a precondition of live updates.
- The script closes its stream while the tab is hidden, so background tabs hold nothing.
- A per-session cap on streams (say 8), so that one person cannot take the instance's [200].
- Or poll (Section 7), which holds no connection at all.

### I3. (item, version) does not determine what the board shows (D2, D4, D1; open point 5)

**Failures.** The cached projection embeds 9a-1's `View` (D4), and `View` reads several things the version `r<record seq>.b<board seq>` never sees:
- **the kill switch's table**, through `AutoClosePaused`, `ClosesAt` and `AutoCloseUnread` (`internal/work/app/views.go:473-484`);
- **the coordinator's in-memory queue**, through `QueuePosition` (`views.go:434-437`). A standby's queue is empty, so it shows "not known on this instance" (`internal/web/pages/board.go:177-178`);
- **revisions**, through `ClosesAtUnread`;
- **the incident's other items**, the strip.

Concrete failures:
1. **The switch on an idle item.** An item awaits its close and the kill switch turns on. No record of the item is written and no board entry, so there is no nudge. When the 30 s slow tick re-fetches, the cache still serves "closes by itself at 10:45" while the auto-close is paused.
2. **The fixed header goes stale.** It is rendered in the layout from a per-request read (`internal/web/pages/layout.go:47-57`, `templates/layout.html:10-13`). D1's regions exclude the layout, so a live board keeps reading "Kill switch: off" until a reload, during the emergency, on the page people keep open. The standby notice (`layout.go:58-60`) goes stale the same way.
3. **The queue position never moves.** Another item's start writes no record of this item, so the position stays wrong until this item's own records change.
4. **A takeover serves stale projections.** A standby that takes the lease keeps serving its cached "position not known".
5. **A restore aliases versions.** After a database restore (spec 8.4), seqs are reused for different content, so one (item, version) names two boards. "Versions only grow" (open point 5) is false across a restore.

**Fix.**
- Cache only what the records and the board determine. Compute the lifecycle facts at each regions fetch: the queue position, the facts that depend on the kill switch, and standby. They come from memory or from one shared read.
- Put the kill switch's latest row and the lead state into the version. `max(seq)` over `kill_switch`'s primary key (`internal/db/migrations/0010_kill_switch.sql`) is one read per tick, shared by every watcher, so a flip nudges every stream within one tick.
- Make the fixed kill-switch header and the standby notice regions.
- Key the cache by the latest record's id and the latest event's id (UUIDs, never reused) beside the seqs.

### I4. Board-sized load on the coordinator's small pool, on every tick (D2, D8)

**Failure.**
- **Every store shares one pool.** `newStores(s.main, …)` (`cmd/agentswarm/serve.go:490`) puts the work records, the board events and the audit calls on the main pool. The web reads through it too. Nothing sets the pool's size, so pgx's default applies: `max(4, NumCPU)` (pgx v5.10.0 `pgxpool/pool.go:19, 380-385`).
- **What the design adds to that pool:**
  - D2 runs one goroutine per watched item, two queries every 2 s.
  - Every nudge sends every viewer to `/board`.
  - Every regions fetch re-reads the strip: "one fold and one replay per item shown", up to 10 (D2, D8). For each sibling that is the records `Load`, `BoardContext` and the events `Load`, about 3 queries plus a full replay (`internal/work/postgres/records.go:286-299`).
  - The slow tick nudges every stream every 30 s, whether or not anything changed.
- **The worst case comes when it matters most.** In an alert storm one incident gathers many items, and many people watch it. With 20 viewers of one item that has 10 siblings, and a version change every 2 s in an active round, that is about 20 × 30 / 2 ≈ 300 queries/s and about 100 full replays/s. At the cap of [200] streams the slow tick alone costs about 7 fetches/s with their strips while nothing changes.
- **The design breaks its own anti-goal**, "a poll whose cost grows with the board on every tick".
- **A slow database turns this into starvation.** The web holds the 4–8 connections, and the coordinator's steps and the gateways' audit writes queue behind it. An audit write that fails refuses its call (spec 6.2 step 3).
- **Per-item watchers are fragile.** A goroutine per item, started and stopped by reference count, invites a join-while-stopping race.

**Fix.**
- One probe per instance per tick for every watched item: `SELECT item_id, max(seq) … WHERE item_id = ANY($2) GROUP BY item_id`, the same for runs, plus the kill-switch row.
- Cache the strip per incident for the slow tick's period, shared by every viewer.
- Build sibling entries from records only: `RunStopped.Conclusion`, plus one read of that hypothesis's event through `board_events`' `UNIQUE (tenant_id, id)`. No replay per sibling.
- Singleflight the projection per (item, version). "At most once per instance" needs it.
- Bound the web's concurrent database work with a semaphore or a second small pool, so viewers can never take the coordinator's connections.
- The slow tick re-renders the strip region only, and only when the strip's own version changed.

### I5. Replacing regions breaks keyboard and screen-reader use (D1, D13; spec 10.4)

**Failure.** On each nudge, D1 replaces the header, strip, hypotheses and feed by id. It keeps `<details>` state and the scroll position, but not focus. A keyboard user tabbing through raw-output links, or through "show earlier", loses focus to `body` every few seconds of an active round. Screen readers keep a virtual buffer, and a replaced node under the cursor makes them jump or re-read (general AT behaviour, **unverified here**). Spec 10.4 asks that "every action works from the keyboard", and its "one polite summary per round, not one per entry" exists so that entries do not disturb such users. Here the disturbance comes with every entry, announcement or not. Replacing a whole region also defeats the browser's scroll anchoring.

**Fix.**
- Give each region its own version attribute, and replace only the regions that changed.
- Never replace the region that holds `document.activeElement`. Mark it stale instead ("3 new entries: update"), and replace it on blur or on request. Restore focus by id when the element survives.
- Spec 10.2 asks only the **header** to refresh "as entries commit". The hypotheses and the feed may sit behind an "update" control (Section 7, option B).

### I6. "By T at the latest" is not an upper bound (D6; open point 1)

**Facts from the code.**
- **Wall clock counts only the rounds' own spans.** It is `EndedAt − OpenedAt` of normal, counted rounds (`internal/work/item/decide.go:119-122`). `EndedAt` is the deadline or the last turn's end. After it, `end` always waits `Grace` [5 s], and `settle` waits up to `Grace + SettleWait` [5 s + 30 s] for model calls in flight (`internal/work/app/rounds.go:291-295, 320`). None of that time is counted.
- **Wrap-up rounds come before the budget check** in `decide` (`decide.go:169-171`).
- **Up to four wrap-up rounds open.** `MaxWrapUpRounds = 2 × MaxWrapUps = 4`, since two lost to an outage or a takeover do not count (`decide.go:14-19`).
- **A firing resume adds no budget.** It is "an amount of zero", resuming with the remaining budget (`internal/work/item/item.go:559-561`).

**Counterexamples.** Take L = 150 s, wall clock 15 min, 6 rounds. The design's T is start + 15 min + 2 × 150 s = start + 20 min.
- **A. A plain run, no fault.** Every normal round runs to its deadline. A hypothesis converges in round 5, so wrap-up 1 opens. A refutation revokes it, round 6 follows, the hypothesis converges again, and wrap-up 2 stops the run converged. That is 8 rounds of 150 s, each followed by 5–35 s that are not counted. The stop comes 40 s (fast vendor) to 4 min 40 s (slow vendor) after the T shown.
- **B. A firing resume.** The run stops quiet after round 1 at start + 1 min, with no hypotheses, while instance A still fires. An item never closes by itself while its alert fires (`views.go:502-507`). At start + 40 min a second instance fires, which is an applied firing, and the run resumes with 14 min and 5 rounds left. Row 2 ("Agents are reading … by T at the latest") reappears showing start + 20 min, already 20 minutes past, while the run can go on to about start + 60 min. A person's extension of a stopped run does the same.
- **C. A takeover.** A deploy during the run loses a wrap-up round. The run may then open 4 wrap-up rounds, not "[at most 2]", plus the takeover's own time.

**Fix.** Compute T forward from the records, not from the run's start:

T = D + W + max(0, B − U) + n·W + k·(L + W)

- D is the open round's recorded `RoundOpened.Deadline` (`item.go:292`), or the latest record's time between rounds;
- U is the counted wall clock with the open round taken to D;
- n is the normal rounds that can still open;
- W is Grace + SettleWait + one retry wait [5 s];
- k is the counted wrap-up rounds left (MaxWrapUps minus those that counted).

State in the copy and in the spec amendment that an outage or a takeover moves T later, and that the page then shows the new T. D15's test should drive the real `Run.End`/`Open` with the end waits and both kinds of resume, and assert the stop ≤ T. A test of the formula alone would pass vacuously.

The same applies to "confirming until T" (D6): use the recorded `Deadline`, and note that the decision lands up to Grace + SettleWait later.

### I7. The quote rule leaves out an author's own support (D5)

**Failure.** D5.1 defines the candidates as "each contributor's latest stance, without retracted evidence, the evidence of its aliases included". In the assessment, a hypothesis's author gets support through an **author record**. That record's entry is the hypothesis itself, and its backing is the evidence the hypothesis cites that the author wrote, is not retracted, and does not refute it (`internal/work/board/assess.go:259-282`). Such evidence usually carries no `Hypothesis` at all, because it was posted before the hypothesis existed. `evidenceRecord` only takes evidence whose `Hypothesis` is h (`assess.go:242-247`).

Read literally, D5 filters on evidence with a stance on h. A new hypothesis supported only by its author would then show zero quotes beside "1 supporting, 1 source", and the commonest support on any board is never quoted. Sources are also counted from **every** live supporting record of a contributor whose latest stance supports (`assess.go:179-186`), not only the latest one. "Latest stance" read as "latest entry" would undercount "and N more".

**Fix.** Define the candidates as the entries behind each counted record:
- supports: every live supporting record of each contributor whose latest stance supports. For an evidence record, its own entry. For an author record, each cited evidence entry that counted.
- refutes: the latest record of each refuting contributor.

Add the backing entry ids to `stanceRecord`, so that `Quotes` and `countStances` share one walk (open point 2: no copy is needed). Order by the evidence entry's seq, not the hypothesis entry's.

### I8. Platform claims spoofable through alert titles (D5.4, D9, D12)

**Failure.** 9a-2 is the first surface to show platform-evidence claims to people, and D9 labels them "the platform's". The claims are flat strings in which the platform's words and the source's text meet:
- the opening: `"Opened by <source>: <title>; k=v; …"` (`internal/work/app/coordinator.go:1701-1713`);
- a trigger update: `"Trigger <status> from <source>: instance <id> at <time>[; late, not applied][; the trigger is resolved]: <title>"` (`coordinator.go:1723-1743`).

**The spoof.** A title of `x; the trigger is resolved` reads, under the platform's authorship, as if the alert had resolved while it still fires. A responder may stand down. A title of `disk full; service=payments` reads as a second routing member. The code's own comment names this threat for routing ("checkout\nservice=payments would show on the opening's claim as a second line that reads as another routing member", `coordinator.go:1512`, on `checkRouting`). The opening's title is replaced only when it holds `Unseen` runes (`coordinator.go:1494-1503`). A trigger update's title has its `Unseen` runes folded to spaces at mapping (`internal/inbound/mapping/mapping.go:333-357`; posted as is, `internal/work/app/ingest.go:297`). Both stop hidden text, not plain words. `<bdi>` stops reordering, not impersonation. Spec 9.1 names "trigger fields" among content that attacks the interface. 5b's carry is "values as quoted data, never as the platform's words", and it applies here just as it does to summaries.

**Fix.** Render platform evidence from typed records, never from the stored claim string.
- **`item:<id>`:** "Opened by <source>" from the `item_opened` record, then the title as quoted data, then the routing as platform facts from `Opened.Routing`.
- **`trigger:<event id>`:** the platform's words from the `item_triggered` record with that event id (status, source, instance, time, applied, resolved). For the title:
  - recompute the claim's head by the same rule;
  - if the stored claim starts with exactly that head and ": ", show the rest as the source's quoted text;
  - otherwise (redacted, cut, or a fallback form), show the whole claim as quoted data.
  A cleaner path is an optional `Title` field on `item.Triggered`; an optional body field needs no migration.

The same spoof reaches agents through the board. That is 7c-1b's to answer; carry it.

### I9. `inert` contradicts itself, and marks every newline (D12)

**Failures.**
1. **The three bullets cannot all hold.** D12 says `inert` "wraps the text in `<bdi>`" and that "html/template escapes as before". If the function returns a string, html/template escapes the `<bdi>` tags into visible text. If it returns `template.HTML`, html/template escapes nothing, which is the classic bypass, and the 9.1 control then rests on the helper escaping by hand. The plan's briefs carry design sections verbatim, so this text would reach an implementer as written.
2. **The rune set is wrong for prose.** "Each rune `config.Hidden` or `config.Unseen` reports" includes `Unseen`, which adds tab, line feed and carriage return (`internal/registry/config/names.go:74`). Agent claims and statements may hold newlines (`agentText` refuses `Hidden` only), and raw outputs are full of tabs. Every one would show as `⟨U+000A⟩` or `⟨U+0009⟩`.
3. **The coverage is unclear.** "Every stored text" also covers the layout's kill-switch reason and `By`, and the items list. Is the rule board-only?

**Fix.**
- `inert` returns a plain string, the markers only. Templates hold the isolation: `<bdi>{{inert .X}}</bdi>`, or a class with `unicode-bidi: isolate`.
- Use `Hidden` for multi-line text (claims, statements, raw-output lines, titles). Keep `Unseen` for single-line values (keys, routing, names, summary and fact values).
- Add a test that every `inert` call site renders `<script>` escaped.
- Say whether the layout and the items list use it.

### I10. The stream leaks a hidden run's progress (latent until 8a; D2, non-goals)

**Failure.** For invisible-arm items the design says only "8a, through 9a-1's visible seam, which every new route calls". It does not say what `/live` and `/board` do when `visible` says no.
- **The version leaks counts.** `r<record seq>.b<board seq>` is sent in every `id:` and `data:`. The board seq counts the hidden run's entries. The record seq counts its rounds, since `round_opened`, `round_ended` and `run_stopped` are item records (`internal/work/item/item.go:55-57`).
- **The nudges leak timing.** Every nudge tells a responder, who must "handle it with your usual process", when the hidden agents are active. ADR-011's blinding, and with it the Phase 0 measurement, depends on that person seeing nothing of the run (7c-1a M13: "nothing of an invisible-arm item's run").

It stays latent only while `visible` returns true and every item is visible.

**Fix.** Settle it now, in one sentence carried to 8a. For an item `visible` hides:
- `/board` serves the hidden copy;
- `/live` sends heartbeats only, and nudges only when the hidden state word (queued / open / closed) changes;
- no seq from the run or the item reaches the page.

Consider an opaque version everywhere, a hash of the seqs. Ordering can come from serialized fetches instead (M9).

## 3. Minor findings

**M1. The Trusted Types policy is a pass-through (D1; open point 3).**
- **Why it is a no-op.** A policy's `createHTML` receives only the string, with no URL, caller or sink (MDN). "Passes only the response of that same-origin URL" is a property of the code that calls it, not of the policy. The CSP still blocks every other string sink and every other policy, which is worth having, but the content's safety rests wholly on html/template.
- **Support.** MDN marks Trusted Types "Baseline 2026, newly available (since February 2026)". Older phones lack `window.trustedTypes`, and the script must feature-detect or it throws (per-engine versions **unverified**).
- **A meaningful alternative.** Fetch the regions with `XMLHttpRequest`, `responseType = "document"`, which yields an inert parsed `Document`. Move the nodes in with `replaceWith(document.adoptNode(…))`. Send `require-trusted-types-for 'script'; trusted-types 'none'`. No policy can then exist and no string sink is used, and nothing needs a fallback. The XHR document is absent from MDN's sink list; **verify** in the target browsers.

**M2. CSS cannot open a closed `<details>` (D13).** The content of a closed `<details>` is not rendered, and no media query sets `open`; only the recent `::details-content` pseudo-element touches it, with partial support (**unverified**). "Collapsed on phones, open beside on wide, CSS only" therefore cannot be built as written. Fix: render `<details open>`, and let the script remove `open` below 60em. Without JS a phone sees the feed expanded, and it is last on the page anyway. Or render the feed in a plain section on wide screens.

**M3. The service mark keys on an argument name (D7, ruling 5).**
- **How scope works.** `Proposal.Scope` holds "each scoped argument and the value its binding gave" (`internal/work/board/board.go:322`). A binding is a CEL expression over `hypothesis` and `item` (`internal/registry/config/conditions.go:13-16`).
- **The silent gap.** A tool whose argument is `app`, bound to `hypothesis.service`, never gets the mark. The mark is one of 9.1's four controls against misleading suggestions, and it fails silently. It also builds a domain convention into the platform.
- **Fix.** The views already read the locked revision (`Revisions.At`, `internal/work/app/autoclose.go:59`). Mark when any scoped argument whose binding is `hypothesis.service` got a value other than `Routing["service"]`. Where neither side can be determined, show "service not compared" rather than nothing.

**M4. `superseded` narrows the spec and costs replays (D6, D7).** Spec 6.3 says "rejected or **lost convergence**". D6 marks superseded only a proposal whose hypothesis was converged *when the proposal was recorded*. A proposal made while H led, after which H converged and then lost it, stays unmarked. Mark that as a deviation, or follow the spec.
- **Cost.** `ReplayAsOf` replays the whole log twice per proposal (`assess.go:51-56`), at every new version.
- **A cheaper source.** `RoundEnded.Converged` records the converged ids at each round's end, and `RoundOpened.WrapUp` the wrap-up's id. "Was converged, is not now" is then free.
- **Ordering.** If replay stays, cut the log by seq, not by time: `At` can repeat after a clock step (`internal/work/postgres/events.go:40`).

**M5. Gaps in the strip (D8).**
- "Disagree" must be computed over visible items only. A hidden sibling's conclusion feeding the flag leaks its run (M13).
- Keys of different work item types are not comparable. A linked incident can mix types, and every such pair would show "disagree".
- A sibling's own failure (corrupt, held by a later build, board unreadable) needs its own "unreadable" entry; D8 covers only a failure to list the items.
- `visible(*app.View)` lives in `pages`, but the strip is built in `app`. Say where each sibling's visibility is decided, and that hidden siblings' boards are not replayed at all.
- `Board` "holds 9a-1's View". If it calls `Item()`, which fails on the incident read (`views.go:268-271`), D8's promise fails with it. If `Item()` changes, the admin API's C4 changes too. Say which.

**M6. Missing words (D9, D10, D4).**
- **A declined firing resume.** A firing whose resume was declined, with its 7 codes, recorded in `Triggered.Resume`, gets no words. The responder must learn that the alert fired again, the agents did not resume, and why.
- **The auto-close held while the alert fires.** For an item awaiting close while its alert fires, `closesAt` returns no time and no flag (`views.go:502-507`), so the board says nothing. Row 8's "what stays open until when" needs "stays open while the alert fires".
- **`item:` refs.** The opening claim's provenance link (`item:<id>`) is not mentioned beside `trigger:`.
- **The notice collapse key.** D10's key drops `Skill`, which is part of the board's own repeat key for `skill_yanked` (`board.go` Notice). Two yanked versions would merge.
- **The 7c-1b carry.** "The item view's instances" is not named in the header.

**M7. Raw outputs (D11; ruling 6).**
- **The admin addition.** Spec 10.1: "Raw outputs are visible only to the item's responders and raters". Adding admins is a deviation. Ruling 6's cost line does not say so, and Task 8's amendment list omits it.
- **The fresh window.** `Actor.Admin` is true only while fresh (`internal/web/auth/actor.go:25-31`), so an admin's links appear for about 6 minutes after a fresh sign-in, then vanish, and the page never says why. Before 7c-2 that is the only way anyone sees an alert item's raw outputs. Render a "re-authenticate to open" link through `FreshPath`, as the act forms do.
- **Finding the quote.** Quotes match the *decoded* text of JSON output (`internal/work/board/quote.go:25-37`). Raw JSON with escaped `\n` is one line of hundreds of KiB, in which the responder cannot find the quote. Render the canonical decoded form and mark the quote's line, and show the redacted arguments beside the output (spec 3.3, Echoes).

**M8. D3's claims and seams.**
- **The re-check bound.** A rotated key is seen within `keyCache` (1 min, `internal/web/auth/keys.go:33`) plus the re-check period (30 s): 90 s, not "within its minute". The stream carries no content and every regions fetch is checked on its own, so a fixed maximum stream life (say 10 min, then a clean end through `SignedIn`) would do the same job more simply.
- **The drain.** The hook fires at `Shutdown`, which `close` calls after the whole drain (`cmd/agentswarm/serve.go:910-912, 1050-1054`). Streams therefore live through the drain, which is harmless but not "at once". `worstDrain` is indeed unchanged, since the web's stopWait is already counted (`serve.go:1037-1040`).
- **The seam.** `pages.New` has no `*http.Server`; `web.Options` needs a shutdown seam.
- **The log.** `/board` and `/live` should log at Debug, like the health routes (`server.go:153-169`). Otherwise every viewer's fetch is an Info line.
- **The script's cache.** `live.js` served like `app.css`, with `max-age=300`, runs a stale script for up to 5 minutes after a deploy that changes the regions. Version its URL.

**M9. Open point 4: yes, an older board can win (D1, D2).** Two fetches in flight can return out of order. The nudge's version is not the response's: the server renders whatever is current. Fix:
- one fetch in flight, plus a dirty flag that fetches again once it returns;
- each region and the response carry the version they were rendered at, and the script drops a response older than the one it applied;
- the regions fetch carries the page's query (`?feed=all`), so a refresh never collapses the feed someone is reading;
- the page carries its version, so the first connect does not re-render a page just rendered.

**M10. No browser runs the script before 9b (D15).**
- **The gap.** The e2e's plain streaming client cannot see I1's dispatch rules, M1's sink, I5's focus or M9's ordering. They are the script's whole job, and none of them runs in a browser before 9b.
- **A browser gate.** Add a manual check in a real browser at the branch's end, Chromium and iOS Safari, recorded in the plan. It adds no module and runs: open, idle 2 min, deploy, sleep the phone, expire the session, open 7 tabs.
- **A cut line.** Order the tasks so that the no-JS board is complete first, and let the live task be cut at the stop line (Section 5).

## 4. Carried constraints naming 9a-2

| Source | Constraint | Where | Verdict |
|---|---|---|---|
| 9a-1, Not in this plan | Rules header | D1, D4, D6 | Addressed |
| | Incident strip | D8 | Addressed; gaps in M5 |
| | Quotes, quote rule, bidi carry (Plan 1:4964) | D5, D12 | Addressed; I7, I9 |
| | Suggestions: summary parts as data, the service mark, the proposer (5b:186-188, 203-207) | D7 | Addressed; M3 |
| | Feed merged with the item's records (7b-1, now at :1055) | D9 | Addressed |
| | Notices shown to people (5b:189) | D10 | Addressed; M6 |
| | "Confirming", the full state table, SSE and the indicator | D6, Goals, D1-D3 | Addressed: rows 12-13 deferred with reason, row 9's close-out form is 9b; I1, I6 |
| | Wide layout and raw outputs | D13, D11 | Addressed; M2, M7 |
| 9a-1, Constraints carried | Previews without a time bound or single flight (m2) | D14, carried to 9b | Explicitly carried (ruling 7) |
| | The form key's MAC (m3) | D14, carried to 9b | Carried. Note that m3 can open a duplicate item: a correctness bug, not hygiene |
| | A subject with surrounding spaces | D14, taken | Addressed |
| | The standby page's lease clock (m9) | D14, taken | Addressed. It needs the database-time clock in `web.Ports`; today `Clock: time.Now` (`serve.go:617`) |
| | "Confirming until T", the deadline, "last updated" | D6 | Addressed; I6 |
| 7c-1a | The strip from `IncidentItems`; nothing of a hidden run (M13) | D8 | Addressed; the disagree flag and hidden siblings in M5 |
| | `IncidentItemsUnread` | D8 | Addressed in `Board`; whether `Item()` (C4) changes is unsaid (M5) |
| 7c-1b | Words for `item_triggered` (applied, late, resolution); board states 6, 10, 11 | D9, Goals | Addressed |
| | Trigger updates' platform evidence (A6 (c)) | D5.4, D9 | Addressed, but spoofable (I8) |
| | The item view's instances and firing state | D4 ("the alert") | Partly: instances not named; declined resumes and the firing-held auto-close lack words (M6) |
| 5b, Plan 9 | Grouped by target, superseded, the service mark, facts and summaries as data, the proposer and their support | D7, D6 | Addressed; M3, M4 |
| | Adoption answers | 9b (non-goal) | Carried |
| Plan 1:4964 | Quotes inert, bidi neutralized | D12 | Addressed; I9 |

## 5. Open points answered

1. **Is D6's T an upper bound?** No. See I6: the end waits break it in a plain run, firing resumes and person resumes break it, and takeovers give up to 4 wrap-ups. The fix is the forward formula, computed from recorded deadlines.
2. **Does D5 fit `assess.go` without copying?** Yes: `Quotes` lives beside `countStances` and walks `records(h)`, once `stanceRecord` carries its backing entries (I7). "Newest by board seq" across aliases is sound: aliases sit on the same run's board, and `Seq` orders a run's entries. Order by the evidence entry, not the hypothesis entry.
3. **Do Trusted Types or the CSP break a browser?** `script-src`/`connect-src 'self'` are universal. `require-trusted-types-for` is ignored where unsupported, but the script must feature-detect (M1). The XHR-document route needs no fallback at all.
4. **Can a racing fetch show an older board?** Yes. M9 gives the fix.
5. **Is the cache safe across a takeover?** No. It holds facts of the instance's own state (queue, lead state) and facts that depend on the kill switch, none of which are in the version, and a restore reuses seqs (I3).

## 6. Scope and size

- **The design's figure.** 12.5–14 raw days.
- **Task 4, the board page (2.5–3).** The page carries roughly ten times 9a-1's board: about 28 feed-line kinds with words, nine notice codes, suggestions with four marks, quotes in three forms, twelve state rows, `inert`, and a wide layout. 9a-1's opening form and board together took 1.5–2. Reasonable: 3.5–4.5.
- **Task 2, the projection (2.5–3).** The forward deadline formula with a real-coordinator test, superseded, a strip with per-sibling failures, and the probe in both stores with integration tests. Reasonable: 3.5–4.
- **Task 5, live (2.5–3).** With I1–I5 and M8–M9 the fixes add about 1–1.5 days.
- **Fixtures.** The web's tests use fake `Views`, so every row and line kind needs hand-built `BoardView` fixtures, beside the app tests on memory stores.
- **Estimate.** As designed plus this review's fixes: about 16–19 raw days (3.2–3.8 pw raw). At 9a-1's executed ratio of about 1.2–1.3, about 4–4.9 pw as executed.
- **Spend.** 9a-1 overran in fix rounds, and an untested script invites more: guess $20–28 rather than $15–20. Keep the stop line set by R93 from Task 1's dollars.
- **Split.** One plan is right, provided the live task comes last and may be cut: Tasks 1–4 and 6 make a complete, useful server-rendered board on their own. A separate plan for the transport is worth its review cycle only if SSE stays and a browser test is added. With polling (Section 7) the live task shrinks to about 1.5 days, and the total to about 13.5–15.5.
- **Nothing is over-built** except D3's machinery, which Section 7 removes.

## 7. Out of frame: a simpler design that meets the spec

**SSE is not in the spec.** D1 says "SSE is in the spec". It is not: the spec names no SSE (grep finds none). It asks for a header "refreshed as entries commit" and an indicator reading "live" or "reconnecting, last updated at T" (10.2). Only roadmap row 9a-2 says "SSE", so a change of transport needs a roadmap ruling, not a spec amendment.

**Option A: conditional polling instead of a stream.**

How it works:
- `live.js` fetches `/items/{id}/board` every [5 s] while the tab is visible, sending `If-None-Match: "<version>"`.
- The server checks the version with the cheap probe, cached per item for about 1 s and shared by every viewer. It answers `304` when the version is unchanged, and otherwise renders the regions with an `ETag`.
- The indicator reads "live" while the last poll succeeded within twice the interval, and "reconnecting, last updated at T" otherwise, with backoff up to 30 s. A `401` reloads the page.
- The version includes the kill switch's row and the lead state (I3).

What it removes:
- `/live`, the watcher goroutines and their lifecycle, the stream cap, write deadlines, heartbeats;
- the session re-check loop, since each poll passes through sign-in;
- RegisterOnShutdown and its seam, `Last-Event-ID`, the proxy's buffering and idle lines;
- the six stream tests and the streaming e2e client;
- all of I1's traps and all of I2.

What it costs:
- latency up to 5 s instead of up to 2 s plus a fetch. Rounds last 150 s, so a person will not notice;
- about one cheap probe per viewer per 5 s, comparable to D2's two queries per watched item per 2 s.

**Option B: refresh the header live; let the reader update the rest.**
- The spec requires only the header to refresh as entries commit. Refresh the header and the indicator live.
- Show "N new entries: update" for the hypotheses and the feed, and update them when no focus is inside them, or on request.
- This cuts the rendering, answers I5, and keeps the polite per-round summary meaningful.

**With either option:**
- Use M1's XHR document with `trusted-types 'none'`.
- Build the strip from records (I4).
- Render platform evidence from typed records (I8).

## 8. Checked and found sound

- **The recorder.** `http.ResponseController` reaches through `logged`'s recorder (`server.go:192-193`). A scratch program confirmed that `Flush` and `SetWriteDeadline` both work through an `Unwrap`-only wrapper.
- **`ReadTimeout`.** The server's 30 s `ReadTimeout` does not cut a stream in Go 1.26: `startBackgroundRead` clears the read deadline (`net/http/server.go:697`). Confirmed with the same program, streaming past a 1 s `ReadTimeout`.
- **`worstDrain`.** It is unchanged (`serve.go:1037-1040`).
- **D2's probe** is cheap: `max(seq)` over the primary keys `(tenant_id, item_id, seq)` and `(tenant_id, run_id, seq)` (migrations 0007 and 0001).
- **D9's merge by time.** Both stores stamp `greatest(clock_timestamp(), previous)` from one database clock (`events.go:40`, `records.go:38`), and ties by each store's seq are deterministic.
- **D11's cited-only gate** is sound: provenance binds a cited call to the same run and agent at post (spec 4.2). Checking `ReadCall.Run` as well is cheap defense in depth.
- **The new GETs** need no CSRF, `web.Authenticated` makes them no-store, and versions are integers: `Last-Event-ID` is compared, never echoed, so no header injection.
- **The routes.** `GET /items/{id}/board` and `/live` outrank `GET /items/{id}/{act}` in Go's ServeMux.
- **Where `inert` matters.** Subjects are already refused with `Unseen` runes at sign-in (`internal/web/auth/oidc.go:378`), item titles are replaced (`coordinator.go:1494-1503`), and inbound titles have every `Unseen` rune folded to a space at mapping (`internal/inbound/mapping/mapping.go:344-349`). D12's "alert titles … are not [refused]" is true, but they are already cleaned. So `inert` is defense in depth everywhere except raw outputs, where it is needed.

## 9. Unverified

- Whether a given browser retries or gives up after a CSP-blocked redirect in EventSource (I1.4). The Standard's default for a network error is to retry.
- The browser connection limit of 6, and whether the deployment's proxy speaks HTTP/2 (I2).
- Screen-reader behaviour when a region is replaced (I5).
- Trusted Types support per engine and version; the XHR document not being a sink in every engine; `::details-content` support (M1, M2).
