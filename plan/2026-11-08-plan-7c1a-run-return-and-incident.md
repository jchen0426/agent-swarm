# Plan 7c-1a: a run's return and the incident — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Draft of 2026-11-08, version 2 (answers the scoped plan review of version 1, `plan/2026-11-08-plan-7c1a-plan-review-v1.md`: 0 Critical, 6 Important, 16 Minor, all taken), approved by the author on 2026-11-08, with A3' and A4 as recommended. |
| Date | 2026-11-08 |
| Spec | `plan/2026-10-11-agent-swarm-platform-spec.md`, Sections 4.1, 4.2, 5.6, 8.2, 9.1 and 10.1, as amended by Plans 7b-1, 7b-2 and 9a-1; binding where it speaks. |
| Base plan | `plan/2026-11-07-plan-7b2-v3-carried-to-7c1.md` (v3), Tasks 5 to 13 and its branch's end, with its Decisions 16 to 18, Contracts C1, C3, C4, C9, C16 to C19, rulings R52 to R74 where they concern those tasks, and Appendix A's rows for Tasks 5 to 12. v3 was written against 5a1321a and reviewed twice on Opus 5.5; its text binds this plan **except where an amendment below changes it**. |
| Drift record | `plan/2026-11-08-plan-7c1a-drift-from-v3.md`: a read-only check of v3 against HEAD 6f641f5 on Opus 5.5, 40 numbered items (D1 to D40 below), six of which block execution. Every item is answered under "Amendments". |
| Roadmap | `2026-10-12-implementation-roadmap.md`, row 7c-1, split on 2026-11-08 by the author into 7c-1a (this plan: the resume, extensions, links, migration 0011, the admin routes, the interleaving test's second half, scenarios 24 and 25) and 7c-1b (the inbound events), 7c-1a first. |
| Size | **Raw, about 7.75–9.75 days, about 1.55–1.95 person-weeks** (recorded only); executed at the record's 1.3–1.5, about 2.0–2.9. **Calendar guess, about 2 days; stop line 5 calendar days. Spend guess, about $10–18; stop line twice the guess, recalibrated after Task 8 with the guess as a floor (R93).** See "Size". |
| How it was made | Rebased from v3 onto 6f641f5 by the drift record; v3's two reviews stand for the parts the amendments leave unchanged. Version 1 had one scoped plan review on Opus 5.5, of the amendments and of v3's Tasks 5 to 13 read through them: approve with fixes; version 2 answers every finding ("Plan review"). |
| Decided by the author | 2026-11-07: links stay with the resume and extensions (v3's A1); Plan 7b-2 cut to the structural group. 2026-11-08: ADR-016's trigger becomes calendar time and spend (R-f), which supersedes v3's A2; role checks live in the coordinator through `app.May` (R-e); row 7c-1 split, 7c-1a first; this plan approved, A3' (land what holds, record residuals, go on; stop only for a safety fault) and A4 (`May` on both items for a link) as recommended. This plan's own calls are R98 to R108, each marked "(the planner's call; cost if wrong: ...)"; approving the plan approves them. |

**Goal:** As v3's goal, without its structural half (executed as Plan 7b-2): a person may extend a run's budget, while it runs or once it stopped, and a stopped run resumes on its own board; a person may link an item into another item's incident, and unlink it. Each act is decided by `app.May`, shows its consequences first, and is carried on safely through a retry or a lost answer. The admin API serves them under both runners, through a takeover and a drain; the interleaving test drives them among its acts; scenarios 24 and 25 drive them end to end.

**Architecture:** v3's, Tasks 5 to 13: the records and migration 0011; the board's `resume` entry; extending a running run; the resume of a stopped run; links and unlinks; the admin API; the interleaving test's second half; the harness and scenarios; the documents; the branch's end. Two layers that 9a-1 built are threaded through every act: the coordinator's role policy (`app.May`) and the shared refusal mapping (`internal/admin/switching`, with the web's words in `internal/web/pages/words.go`). No web form offers the new acts; that is Plan 9b's (R107).

**Tech Stack:** unchanged from v3 and Plan 9a-1: Go 1.26; pgx v5, testify, testcontainers-go, the MCP Go SDK, yaml v3; Postgres 17. No module joins the graph.

**How a task runs.** As in v3 and Plan 9a-1: light plan, test first. Each task's brief is **v3's task text verbatim, followed by every amendment below that names the task**, with this plan's Global Constraints. Where they conflict, the amendment wins. Each task is reviewed by a fresh reviewer on Opus 5.5 (Tasks 8 and 11 by two, as in v3), the whole branch at its end. The controller keeps the spend ledger (R93) and the calendar count.

## Decisions for the author

**A3'. When Task 11's timebox fires.** v3's A3 was answered for its Task 1 only. Task 11 (the interleaving test's second half) is timeboxed at 5 hours of calendar time or $4 of spend, whichever comes first, from its dispatch (I5: a day would barely fire against v3's 1.5 raw days); 7b-1's interleaving tasks ran at 1.5 to 2 times their box. **Recommendation:** when it fires, the controller lands what holds (every invariant change and must-pass row that is green), records each unresolved seed as a residual with its seed and schedule, tells the author at once in the ledger and the final message, and goes on to Task 12; it stops for the author only if a found fault is of safety: a run going while the kill switch is on or for a closed item, a run spending past its budget and every extension's, or more than the limit and one items investigating (I6). Cost if wrong: a fault of the stale-account class found late, as 7b-1's I1 and I2 were.

**A4. Who may link.** A link moves item `id` into the incident of item `with`. `May` takes one item. **Recommendation:** `Link` checks `May` on both items, so the actor must be the principal or a responder of the destination item `with` as well as of `id` (one member of the destination incident, as the check reads it, not all its members); an admin passes on both (spec 9.1 makes joining another person's item an admin's act). `Unlink` checks `May` on the moving item only, since it takes the item out, never into, someone else's incident. Cost if wrong: one check too strict, which an admin overrides; or, the other way, a responder joining items they do not handle.

The order against Plan 9a-2 (the drift record's A5) is settled by the split: 7c-1a goes first, so 9a-2's incident strip reads `IncidentItems`.

## Global Constraints

v3's Global Constraints, with these changes:
- **Roles (D1).** Every act this plan adds is decided by `app.May` under the coordinator's lock, right after its item is read and before its reason, its key and its state checks. `policy.go` gains `ActExtend`, `ActLink` and `ActUnlink`, member acts with the admin override, as promote, close and reopen are. The admin API passes `adminActor` (subject `admin`, the loopback break-glass of 9a-1's R-d; no fresh authentication).
- **The gates (D2).** Commit by path; never `git add -A`; no `Co-Authored-By` or `Claude-Session` line; commit as 4158857+jchen0426@users.noreply.github.com. `make test-linux`, which already runs the Docker path (`Makefile:200-211`), at the branch's end, before and after the fix wave, with a 40-minute soak each time; `make test-docker` is not run separately (M12). The spend ledger (`plan/backups/plan7c1a/spend.md`, R93) and the calendar count are kept from Task 5; a plan past 5 calendar days or twice its spend guess stops for the author (ADR-016, R-f).
- **Migrations (D3).** 0011 at level 11, as v3 says; 0010 is still the latest. `run_extended`, `item_linked` and `item_unlinked` are new kinds, so they need it; an optional body field (as 9a-1's `Opened.By`) does not.
- **Words.** Every new `Code*` constant in `internal/work/app` or `internal/admin/switching` gets admin text and web words in the task that adds it, so `TestEveryRefusalCodeHasWebWords` stays green at every commit (D13).

## Amendments

Each line is: the drift item it answers, the tasks it binds, and the change. The drift record carries the evidence (file and line at 6f641f5).

**Relies on (v3 "What this plan relies on from Plan 7b-1"):**
- **D4.** Row 5 also names `Actor{Subject, Responder, Admin}` (`Admin` only while fresh, R76), `May`, `ActKind`, `Grounds` and the codes `not_item_responder` and `responder_only` (R77). 9a-1 changed 7b-1's division of labour; v3's "changes no contract of 7b-1's" is dropped.
- **D5.** Row 6 also names 7b-2's reload in `steps` after an unknown outcome (R50), `settle`'s advance after a lost round end, and `missedEnd`/`ending`, which read `Run.Budget()` once Task 5 lands.
- **D6.** Row 10 names both packages: `internal/admin/lifecycle.go` keeps `act`, `readStrictly`, `consequencesOf`, `itemAfter`, `lifecycleRefused` and `asked.text`; the refusal mapping, `standing`, the codes and `refusedTexts` are in `internal/admin/switching` (`switching.Act`, unexported `refusal`).
- **D8.** v3's carried structural work (its Tasks 1 to 4, R48 to R51, R63's Task 1 half, R65, R66, R72) is executed: read it in `plan/2026-11-07-plan-7b2-stale-account-closure.md` as amended in its execution, not in v3. Its narrowed exemptions bind Task 11.

**Decisions and contracts:**
- **D9 (Tasks 7, 9).** Decision 17's and 18's "Who" become: `May(a, ActExtend|ActLink|ActUnlink, s.Opened)`; the item's principal or a responder (`Member`), or an admin (`AdminOverride`); refused `not_item_responder` otherwise. Web forms are Plan 9b's.
- **D10 (Task 9).** `Link` calls `May` on `id` after `target`, and on `with` once `with` is loaded, before R60's other link checks; `Unlink` on `id` only (A4). Test of the plan's: `TestALinkNeedsTheActorOnBothItems` (a responder of `id` alone is refused `not_item_responder`; an admin is not).
- **D11 (Tasks 7, 9, 11). R98.** `requestOf` gains the act's parameters: an extension's `rounds`, `wallClock` and `tokens`, each as the body gives it and only when the body names it (I1), and a link's `with`, as extra keys of its canonical JSON. Promote, close and reopen keep exactly their three keys, so their digests do not move (test of the plan's: `TestTheDigestsOfPromoteCloseAndReopenDoNotMove`, against digests fixed from HEAD). The interleaving test's `actDigest` and `flying` take the act's extra keys, invariant 6's `requestHeld` learns `Extended`, `Linked` and `Unlinked`, and the driver's same-key path (`pickAct`) expects `ErrKeyReused` for another amount (M10). (The planner's call; cost if wrong: a key retried with another amount counts as a repeat, which `TestARetriedExtensionWithItsKeyExtendsOnce` already forbids.)
- **D12 (Task 10). R99.** C4's codes go through `switching.refusal`: `self` → `400`; `ErrStopping` → `503` `stopping` with `Retry-After: 1`, logged at Warn beside `ErrUnsettled`, not Error (M1); `asked.text` gives `stopping` its own text ("the run is stopping; try again in a second") and `refusedTexts` gains `self` and the four 409 codes (M2); `not_started`, `no_agents_left`, `would_stop_at_once`, `rulings_window_passed` and `not_item_responder` → `409`. `switching` gains `CodeStopping`. A drain's first moment is not `stopping`: it answers `409 draining`, as today (I2). (The planner's call; cost if wrong: status codes, a route-table row each.)
- **D13 (the task that adds each code; I4).** Tasks 7, 8 and 9 add `internal/web/pages/words.go`, `internal/admin/switching/switching.go` and `switching_test.go` to their Files and their commit's paths, and `go test ./internal/web/... ./internal/admin/...` to their gates. Web words in `internal/web/pages/words.go` for `not_started`, `no_agents_left`, `would_stop_at_once`, `rulings_window_passed`, `self` and `stopping`, though no web form offers the acts yet. The words must not contain the substring "after", which `TestEveryRefusalCodeHasWebWords` refuses (M3).
- **D14 (Task 12). R100.** The harness's `Admin.Act` becomes a thin wrapper over an unexported `act(t, id, act, key string, body any, preview bool)`, which `Extend`, `Link` and `Unlink` share. (The planner's call; cost if wrong: a test helper's shape.)
- **D15 (Task 9). R102.** `IncidentItems` goes on a narrow port, `app.Incidents`, in `app.Ports` (the coordinator reads it for Unlink's "alone" check) and in `ViewPorts`; unlike R92's `ClosedLister`, which only the views take. The memory and postgres stores implement it; the interleaving world gains an `incidentsPort` type sharing `recordsPort`'s per-instance read faults (7b-2's rule). v3's Task 9 Files ("`ports.go` (`Records.IncidentItems`)", "every fake of `Records`") and C16 ("Records gains") read accordingly (M9). (The planner's call; cost if wrong: three fakes left alone now, one port to fold into `Records` later.)
- **D16 (Task 10).** `View` gains `IncidentItems`, `Budget` and `ExtensionOffered`, `ViewPorts` gains `Extension` and `Incidents` (M16); `Row` gains nothing (R107).
- **R101 (Tasks 5, 7, 9; drift "new work" 4).** v3's C19 already gives `Extended`, `Linked` and `Unlinked` a `By`; it records `a.Actor.Subject`, as `Opened.By` and `Promoted.By` do, so a web caller later records `sso:<subject>` (M4). (The planner's call; cost if wrong: none beyond v3's.)

**Tasks:**
- **Link's order (M6; Task 9).** `target(id)`; `May(id)`; `self`; `target(with)` (`404`, or `held_by_later_build` through `ErrSkipped`); `May(with)`; `checkReason` (`reason_required`); `requestOf` with `with`, and `findKey`; `closed` and the window (`rulings_window_passed`); `changed: false`; preview; record. Unlink: `target(id)`, `May(id)`, then v3's order.
- **D23, D24, D25 (Tasks 7, 8, 9).** The act order is HEAD's: lock and `holds`; `checkAct`; `target`; `May`; `checkReason`; `requestOf` and `findKey`; the state checks; the act's own checks; preview; record; carry on. A `May` refusal comes before R60's `closed` and every other refusal of R60's table. Each task's files add `internal/work/app/policy.go` and `policy_test.go`; its Consumes add `May` and the widened `requestOf`.
- **D27 (Task 8).** R54's raise in `runStep` goes after the `c.unsettled` case.
- **D28 (Task 5).** `missedDecidesStop` and `finishing()` also read a run's stop and need no change once reviewed; the task's review says so.
- **D29 (Task 6).** A `resume` sets `s.stop = -1` (C18), so `proposable` (`round.go:304`) passes on its own; Task 6 adds a test row: a proposal after a resume is taken (M5).
- **D18 (Task 8).** No test names `countAsPromoted`; its rename to `countAsStarting` touches `queue.go`, `promote.go` and their comments only.
- **D21 (Task 11).** Task 11 adds, beside v3's: (a) invariant 1's narrowed exemptions read the latest decided round within the run's current running interval, so a decision before a resume never exempts a resumed run; (b) a resume whose outcome is unknown calls `noteUnsure`, with a must-pass row; (c) invariant 10's must-pass rows for an extension refused after a close, and for reopen, resume, then a late close retry (R10's residual, `TestALateCloseRetryAfterAReopenAndAResumeLeavesTheRunGoing`'s schedule); (d) **R103 (I3):** 7b-2's carried residual, a run with no agent stopping `not_staffed` ahead of the kill check, so an on's preview names a run the answer leaves out, and invariant 1's `namesEveryRun` has no exemption for it: `runStep`'s two cases swap, so a run that owes a kill stops `killed` before the no-agent stop is considered. Preview, answer and invariant 1 then agree with no exemption. Such a run's board gets the `kill_switch` notice, not `no_agents`, and the item is `not_staffed` either way (spec 4.1). The world always has three agents and cannot reach the case, so the pin is a unit test of the plan's in `killswitch_test.go`, `TestAnOnKillsARunWithNoAgentAndNamesIt`, in Task 8, which touches `runStep`. (The planner's call; cost if wrong: one notice's kind on a board that had no agents anyway.)
- **D22 (Task 11).** The timebox is 5 hours or $4, whichever first (A3').
- **D32, D33, D34 (Task 10).** Files: `internal/admin/admin.go` (routes in `New`), `lifecycle.go` (`actOn` builds `&app.Act{Actor: adminActor}`), `internal/admin/switching/switching.go` and `switching_test.go`, `internal/web/pages/words.go`, `internal/work/app/views.go`, `internal/deploy` (`lifecycle.extension`). Consumes: `act`, `readStrictly`, `switching.Act`, `lifecycleRefused`, `asked.text`, `itemAfter`, `consequencesOf`. The web's fakes are untouched: `web.Coordinator` does not widen.
- **D35 (Task 12).** The gate is `make test-e2e` on the Mac, the process runner, scenarios 1 to 28; the Docker runner's pass is the branch's end, through `make test-linux` (M11).
- **D36 (Task 13).** The documents: this plan; roadmap row 7c-1 split into 7c-1a and 7c-1b, each with its size, calendar guess and spend; ADR-016's History under R-f; spec 4.2 and 10.1: extend, link and unlink are a member's rulings with an admin override, and spec 9.1's admin act reads, for a link, either item (M8); 7d-1's C1 and C4 notes beside 9a-1's; the README (the admin token as break-glass, subject `admin`, now also extending, linking and unlinking). Task 13's commit: `git add plan/2026-11-08-plan-7c1a-run-return-and-incident.md plan/2026-10-12-implementation-roadmap.md plan/adr/ADR-016-delivery-build-before-team.md plan/2026-10-11-agent-swarm-platform-spec.md plan/2026-10-20-plan-7d1-serve-and-harness.md README.md` (M15).
- **D17 (earlier tests).** v3's "Earlier tests this plan changes" drops its Task 1 and Task 3 rows, and gains: `TestMayDecidesEveryActForEveryRole` rows for the three kinds; a stranger-responder refusal test for extend, link and unlink beside `TestAStrangerResponderCannotPromoteCloseOrReopen`; `switching_test.go`'s rows for `self` and `stopping`; the admin fixture's fake coordinator gains `Extend`, `Link` and `Unlink`.
- **D39.** Appendix A's footer about A1 is dropped; every row stays, each test name still free at HEAD.

**Not in this plan, carried (D37). R107.**
- **Plan 7c-1b:** the firing resume through this plan's resume, flap links through `item_linked` (the platform's own record, not the person's checks), a source actor for inbound openings that `May`'s principal rule must admit (9a-1's carry), and everything else v3 and 7b-1 carry to "Plan 7c-1".
- **Plan 9a-2:** the incident's items in the incident strip, from `View.IncidentItems`; the strip shows nothing of an invisible-arm item's run, since a link can join arms and ADR-011 blinds the hidden run (M13).
- **Plan 8b:** beside cross-arm links, link and unlink can move an incident's first item, on which spec 4.1 scores it; Unlink is checked on the moving item alone (M14).
- **Plan 9b:** the extension offered in the interface, and the extend, link and unlink forms, through C16; and 9a-1's latent offer of Promote on an item whose run stopped, which answers "Nothing changed": 9b offers Extend there, which needs `Row` to say the item has a stopped run; and Link's existence oracle: a non-admin learns whether an item id exists (`404` against `not_item_responder` on `with`), so 9b answers `not_item_responder` for an unknown `with` to a non-admin (M7).

(The planner's call; cost if wrong: the acts exist only behind the admin API until 9b, which is the case for every item ruling today.)

## Size

- **Raw (D38).** v3's own per-task figures for Tasks 5 to 13 and the branch's end, about 6.3–7.5 days, plus the drift's deltas, about 1.45–2.25 days (roles, the digest, the refusal mapping and words, the port, Task 11's four additions, documents, this rebase and its review): **about 7.75–9.75 raw days, 1.55–1.95 person-weeks**, recorded only.
- **Calendar. R104.** Guess about 2 days (7b-2 ran about 0.65 raw person-weeks in 2.5 hours; 9a-1 about 5 in one day; Task 11's kind of work ran 1.5–2 times its box in 7b-1). Stop line: the later of 5 calendar days and twice the guess, so 5 days.
- **Spend. R105.** Guess about $10–18 (9a-1: about $20 for about 25 raw days, about $0.8 per raw day, raised for two soaks, two-reviewer tasks and fix rounds). Stop line: twice the guess, recalibrated after Task 8 from the ledger, never below the guess (Task 5 alone, the records, would under-predict).
- **R106.** v3's A2 and its person-week trigger arithmetic are dropped; under R-f they stop nothing.

(R104 to R106: the planner's calls; cost if wrong: a stop line drawn too wide or too tight, which the author sees in the ledger.)

## Plan review

Version 1's scoped review (`plan/2026-11-08-plan-7c1a-plan-review-v1.md`): approve with fixes. I1 (the digest's `wallClock`), I2 (draining against stopping), I3 (R103 now swaps `runStep`'s cases, with a unit test, since no invariant could pin the preview filter and the world never lacks agents), I4 (words and switching in Tasks 7 to 9's paths and gates), I5 (Task 11's box in hours or dollars) and I6 (the safety stops of A3') are fixed as proposed; M1 to M16 are all taken, each named in place. A new ruling, R108: R103's swap is decided by the planner rather than asked, since the item's state is the same either way and only a notice's kind changes (cost if wrong: one case swapped back).

## Review Focus

v3's Review Focus stands, plus:
1. **A retry under the same key with another amount or another `with`** — refused `key_reused`, never treated as a repeat (R98).
2. **A role refusal racing a state refusal** — a stranger to a closed item is told `not_item_responder`, not `closed`, so the state of an item they cannot act on does not leak through the order (D23).
3. **A link across two items with different responders** — refused unless the actor is on both, or an admin (A4).
4. **A resumed run under the kill switch's on** — the preview and the answer name the same runs, including a run with no agent (R103).
5. **An extension retried around a drain and around a stopping run** — a drain's first moment answers `409 draining` and the retry on the next leader extends once; a run mid-stop answers `503 stopping` with `Retry-After: 1`, and the retry a second later extends once (R99, I2).

## The branch's end (the controller's)

v3's, with D2's gates: `make test-linux` and a 40-minute soak before and after the final review's single fix wave; push to origin main; the workspace backed up to `plan/backups/` and deleted; the final message to the author in Chinese with every ruling and its cost if wrong, every deferred minor, the calendar days and the spend against their guesses.

## Constraints carried to later plans

Filled at the branch's end. Starting content: the "Not in this plan" list above; v3's carries to Plans 8a, 8b, 9b and 10a stand, with "Plan 7c-1" read as "Plan 7c-1b".

## Changes made during execution

None yet.
