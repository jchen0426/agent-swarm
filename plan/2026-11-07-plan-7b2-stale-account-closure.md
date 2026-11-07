# Plan 7b-2: closing the stale-account class — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Approved by the author on 2026-11-07, version 4: version 3 cut to the scope the author decided on 2026-11-07. |
| Date | 2026-11-07 |
| Spec | `plan/2026-10-11-agent-swarm-platform-spec.md`, Sections 4.1 and 8.2, as amended by Plan 7b-1. This plan changes no rule of the spec. |
| Design | Plan 7b-1 (`plan/2026-11-01-plan-7b1-coordinator-lifecycle-core.md`) as executed: its "The interleaving test", its "Changes made during execution", and its "Constraints carried to later plans", Plan 7b-2, items 12 (its first half), 13, 14 and 15, which this plan takes. The design of Plan 7b (`plan/backups/plan7b/design-v4.md`, Decisions 16 to 18) is no longer this plan's: it moves to Plan 7c-1 (below). |
| Roadmap | `2026-10-12-implementation-roadmap.md`, row 7b-2, re-scoped by this plan's Task 5: the structural closure of the stale-account class, before Plans 3c, 9a and 7c-1. |
| Size | **Raw, about 3.3 days, about 0.65 person-weeks** (2.6 to 4.05 days). **With 7b-1's executed-to-plan ratio, 1.08 to 1.36, about 2.8 to 5.5 days, 0.55 to 1.1 person-weeks** (0.7 to 0.9 at the raw middle). Against the baseline used for version 3, the row as ADR-016's History of 2026-11-02 raised it (0.65–0.73 person-weeks, a trigger line of about 0.98–1.09), raw does not cross; the adjusted figure reaches the line only at its top. See "Size". |
| How it was made | Version 1 was planned from the final design of Plan 7b (version 4), Plan 7b-1 as executed, and the code at 5a1321a, with the full scope of the roadmap's row: the structural group the controller ordered first, then the resume, extensions and links. A plan review on Opus 5.5 found 1 Critical, 8 Important and 13 Minor issues (`plan/backups/plan7b2/plan-review.md`); version 2 answered every one. A scoped re-review on Opus 5.5 (`plan/backups/plan7b2/re-review.md`) found all 22 addressed, and one new Important (N1) and five new Minors (N2 to N6); version 3 answered those. Version 3, kept whole at `plan/backups/plan7b2/plan-7b2.v3.md`, sized the full scope at about 1.85 raw person-weeks, over ADR-016's trigger at plan time. On 2026-11-07 the author cut Plan 7b-2 to its structural group (version 3's Tasks 1 to 4) and moved the rest to Plan 7c-1. This version, 4, is that cut: Tasks 1 to 4 as version 3 had them, reviewed twice, and a documents task. |
| Decided by the author | The decisions of 2026-10-14, 2026-11-01 and 2026-11-02 that Plan 7b-1 lists, and those of 2026-11-07 below. This plan's own calls are R48 to R51, R63, R65, R66, R72 and R75, each marked "(the planner's call; cost if wrong: ...)"; approving this plan approves them. Version 3's other rulings (R52 to R62, R64, R67 to R71, R73, R74) go to Plan 7c-1 with its work. |

**Goal:** Close the stale-account class structurally, so that no in-memory account of the coordinator stays apart from the records after a lost answer followed by a failed read, and show it: the interleaving test fails reads after write failures, catches the final review's I1 and I2 when their fixes are reverted, and keeps every invariant on main. With it, two small carried fixes: an on's answer names only the runs it killed, and a step cut off by a drain logs below Error.

**Architecture:**
- **The world fails reads.** The interleaving world gains faults of reads, for every store an act or a step reads after its write, paired with write faults, and invariant 10, nothing runs for a closed item, at every port call and after every event. Its RED: with 60443e4 and 49ea574 reverted, it finds I1 (invariant 10) and I2 (invariant 4).
- **The reload.** `steps` reloads an item after a step whose outcome is unknown, so the walk at the end of the advance sees what landed; `walkSoon` stays the backstop.
- **Two carried fixes.** `carryOutKill`, and the preview's `owing`, name only runs whose stop is `killed`; `failed` logs `context.Canceled` at Info once the coordinator's context has ended.
- **The checkpoint and the branch's end.** A 40-minute soak after Task 3; the documents; then `make test-linux` and the soak before and after the final review's fixes.

**Tech Stack:** as Plan 7b-1: Go 1.26 (`go 1.26.0`); testify v1.12.1; no module joins the graph. No migration, no record kind, no route.

**How a task runs.** As in Plan 7b-1. The plan is light: each task gives its decisions, the interfaces it consumes and produces, and its tests by name, with what each asserts and why it fails before the implementation. Tests the design does not name, which this plan adds, are marked "(the plan's)"; every test of this plan is one. A test marked a guard passes at Step 1. Each task is reviewed by a fresh reviewer on Opus 5.5, Task 1 by two, and the whole branch at its end.

## The author's decisions

- **2026-10-14, 2026-11-01 and 2026-11-02:** as Plan 7b-1 lists them; on 2026-11-03 the controller's ruling that hardening shows diminishing returns and that the stale-account class is closed structurally before new features.
- **2026-11-07, the scope.** Plan 7b-2 does only the structural group (version 3's Tasks 1 to 4), its documents and its branch's end. Version 3's Tasks 5 to 12, the resume, extensions, links, migration 0011, the admin routes, the interleaving test's second half and scenarios 24 and 25, move to Plan 7c-1, built beside its firing resume, which needs the resume anyway. Version 3 had recommended keeping the links in 7b-2 (its A1); the author's cut moves them with the rest.
- **2026-11-07, the timebox (version 3's A3).** If Task 1's timebox of 2 days fires before the read faults reproduce I1 and I2 with their fixes reverted, execution stops for the author. Otherwise execution continues, and any fault class still open is recorded as a named residual with its seeds.

## Global Constraints

As Plan 7b-1's, which hold unchanged:
- **Module and Go version.** `github.com/jchen0426/agent-swarm`, `go 1.26.0`.
- **Vocabulary.** The spec's words, and Plan 7b-1's. Agents are "AI agents" or "agents", never "employee".
- **Domain purity.** `internal/work/item`, `internal/work/board`, `internal/work/control`, `internal/work/tokens`, `internal/holding` and `internal/outcome` import no database, network or framework package.
- **Decide, record, act.** Every decision the coordinator makes is a work record written before its effects, and every effect can be repeated.
- **The holding.** Every guarded write runs under a context that carries its holding's epoch. This plan adds no write and no external request.
- **Migrations.** None in this plan.
- **Libraries.** No new module.
- **Test first.** Every listed test is written before its implementation and seen to fail for the reason its line gives, but a guard.
- **Language.** Code, comments and documents in English.
- **The gates.** As Plan 7b-1's (R29): every task ends green on the Mac with `make lint` at `0 issues.` twice, its own packages' tests with `-race`, and the interleaving test's default run once within its budget of 20 seconds. The full `make test-linux` and the 40-minute soak run each run twice at the branch's end, before the final review and after its fixes; the soak runs once more at Task 4. Commit by path, never `git add -A`; no `Co-Authored-By` line.

## Review Focus

The conditions most likely to bite, each with its tests in the task that owns its code:
1. **A lost answer followed by a failed read,** for a close (I1) and a promotion (I2): the account must not stay apart from the records. (`TestTheInterleavingFakesFailAReadAfterAWriteFailure` and the RED, Task 1; invariants 4 and 10 in `TestRandomSchedulesKeepTheLifecyclesInvariants`)
2. **A step whose own append's answer is lost,** a finish's or a start not staffed: the next item must start at once, not a retry wait later. (`TestAFinishWhoseAnswerIsLostFreesItsSlotAtOnce`, `TestAnAdmittedItemLeftNotStaffedWithItsAnswerLostLetsTheNextStartAtOnce`, Task 2)
3. **A run whose round decided its stop, met by an on:** neither the preview nor the answer may name it as the on's. (`TestAnOnsAnswerNamesAmongItsStopsOnlyTheRunsItKilled`, `TestAnOnsPreviewLeavesOutARunWhoseRoundDecidedItsStop`, Task 3)
4. **A drain's first moment with a step in flight:** no Error line. (`TestAStepFailingOnceTheContextEndedLogsBelowError`, Task 3)
5. **A launch that completes after a close:** invariant 10's part (b). (`TestEachInterleavingInvariantCatchesItsViolation`'s rows, Task 1)

## Decisions

Plan 7b-1's Decisions 1 to 15 and its R11 to R47 hold. This plan has no decision of the design's: its decisions are the carried items 12 (its first half) to 15.

### The carried structural work (Plan 7b-1's carried items 12 to 15)

- **Reads that fail after a write failure** (item 12, its first half). The interleaving world never failed a read, so a lost answer followed by a failed read, the final review's I1 (a close's) and I2 (a promotion's), escaped every soaked schedule. The world gains faults of reads, for every store an act or a step reads after its write: the records' `Load`, `Active` and `UnstaffedBy`; the board events' `Load`; and the kill switch's read, which exists (`faultSwitchRead`). Each is armed per instance, since the world's faults today are per world (`w.armed[f]`, spent by any instance), and spent by the next read of its store by the instance that armed it. **Pairing** (R49, the planner's call): when a write fault of a store is spent, lost or failed, the world arms a read fault of the same store on the same instance with probability one half, from the schedule's source; read faults are also among the events a schedule picks alone, at a low weight. Cost if wrong: a rarer pairing finds the class more slowly, which the soak's 40 minutes bound.
- **Invariant 10, nothing runs for a closed item** (R48, the planner's call; widened by the plan review's I1). From the commit of an `item_closed` with no `item_reopened` after it: (a) at every port call, neither instance calls `Turns.Start`, `Sessions.Issue` or `Sessions.Move` for that item's run; and (b) at the end of every event, no live instance that still holds the lease keeps a live session of that run, and no fake runner holds an unstopped turn of it, a pending launch completed after the close among them. A call or a state counts only while the instance's `Holding.Valid` holds, as invariant 1 counts it. It holds after every event as built: `closeNow` drops the run before it answers on an unknown outcome, and a takeover fences first. I1 broke exactly this: with its fix reverted, a close whose answer was lost and whose lookup failed left the run going, so the run's timers could start a turn, and a pending launch could complete into a running turn, after the close answered; part (b) sees the second half, which no port call shows. No invariant of the nine watches a close in time: invariant 7 reads a closed item's run only once quiet, after the item's retry has dropped it, so without invariant 10 the reads' faults could not show I1. Cost if wrong: a stricter invariant than the code keeps fails correct schedules, which the violation table's "must pass" rows guard against.
- **The RED** (the controller's ruling). Task 1's runs must reproduce I1 and I2 with their fixes reverted locally, in a throwaway worktree: 60443e4 (a close whose outcome is unknown drops its run) and 49ea574 (a promotion whose outcome is unknown counts its item). Reproduced means: a seed of the default run, or of a soak no longer than 40 minutes, that fails invariant 10 for I1 and invariant 4 for I2, each replayed by `-interleave.seed`. The revert is never committed. If a clean `git revert --no-commit` conflicts with later commits, the two behaviours are reverted by hand: `closeNow`'s drop on an unknown outcome, and `promoteNow`'s `countAsPromoted`.
- **The reload after an unknown outcome** (item 13). `steps` reloads the item once after a step that failed with an error that is neither `ErrLeaseLost`, nor of a later version, nor one that came once the coordinator's context ended: the append may have landed, its answer lost, and the account would hold the item as it was until the retry. The reload refreshes the account through `load`, so the walk at the end of the advance sees what landed, at once. `walkSoon` stays the backstop for loads outside an advance. A reload that fails changes nothing: the retry is armed, as today, and the fixes of I1 and I2 cover an act's own unknown outcome. **The planner's call (R50):** reload after any such failed step, not only after a failed append, since `steps` cannot tell an append's error from a read's without threading the cause through every step; cost if wrong, one read more for each failed step, at most one per retry wait per item.
- **`carryOutKill`'s filter** (item 14). An on's answer names among its `Stopped` only the runs whose recorded stop is `killed`, and its preview the same (`owing`'s stops, which `wouldKill` reads, keep only those `runStop` gives as `killed`, which goes through `Halt` and so through `decided()`, so that the preview and the answer agree, a resumed run included; the plan review's M3 and the re-review's N2). A run whose round had decided its stop, which the on's halt recorded with the decided reason (Plan 7b-1, Decision 3), is named nowhere: it stopped for itself, and the admin API's `stoppedView`, which drops the reason, would show it to an operator as stopped by the on. The items left not staffed are named as today, in `Unstaffed`. Invariant 1's check of an on's answer (`namesEveryRun`) then exempts a run whose stop, committed after the on came, is not `killed`; the not-staffed case stays as it is.
- **The drain's residual Error line** (item 15). `failed` logs an error that wraps `context.Canceled` at Info, not Error, once the coordinator's context has ended (`c.ctx.Err() != nil`), so that a step or an act in flight at a drain's first moment logs no Error line, as the timers that fire after it already log none (the planner's call, R51: Info, as an expected event of a drain; cost if wrong, a line at the wrong level).
- **The checkpoint** (the controller's ruling; Task 4). After Task 3, before the documents and the branch's end, the soak runs for 40 minutes (R65); a failure's seed becomes a unit test of the coordinator, then its fix, before Task 5 starts. When no commit lands between Task 4 and the branch's end, Task 4's run stands as the branch's first soak (R75).


### Resolutions

The planner's calls, numbered as in version 3, so that its reviews' references stay valid:
- **R48.** Invariant 10, nothing runs for a closed item (above).
- **R49.** Read faults paired with write faults, half the time, and alone at a low weight (above).
- **R50.** `steps` reloads after any failed step but a lost lease, a later version or an ended context (above).
- **R51.** `failed` logs `context.Canceled` at Info once `c.ctx` has ended (above).
- **R63.** The timebox: Task 1 at 2 days, its ceiling; when it fires, the implementer stops and reports the seeds, the invariants broken and the faults found; then the author's decision of 2026-11-07 applies.
- **R65.** The checkpoint's soak runs 40 minutes, as the branch's end's.
- **R66.** Task 1's RED runs from a throwaway `git worktree` under the session's scratchpad, never on main.
- **R72.** Invariant 10 is a port check and a state check after every event (the plan review's I1).
- **R75.** Task 4's soak stands as the branch's end's first when no commit lands between them (the planner's call; cost if wrong: none, the same tree is soaked).

## Earlier tests this plan changes

- **Task 1:** `TestTheInterleavingFakesInjectEachFault` gains the read faults' rows; `TestEachInterleavingInvariantCatchesItsViolation` gains invariant 10's rows.
- **Task 3:** `namesEveryRun` (`interleave_invariants_test.go`) exempts a run whose stop is not `killed`; a test of the kill switch's answer or preview that asserted a decided stop among its stops changes to assert it absent.

## File Structure

| File | Task | Responsibility |
|---|---|---|
| `internal/work/app/interleave_world_test.go`, `interleave_test.go`, `interleave_invariants_test.go` | 1, 3 | Read faults, per instance; invariant 10; `namesEveryRun`'s exemption |
| `internal/work/app/coordinator.go` | 2, 3 | `steps`' reload; `failed`'s level |
| `internal/work/app/killswitch.go` | 3 | `carryOutKill`'s and `owing`'s filter |
| `plan/...`, `README.md` | 5 | The documents |

---

## Tasks

Each task lists its tests first. Unless a line names another, a task's tests are unit tests of `internal/work/app` on the memory stores, with the harness's clock moved by hand (`harness_test.go`). Commands run from the repository's root.

### Task 1: The interleaving world fails reads after write failures, and invariant 10

**Decisions:** "The carried structural work": reads that fail, invariant 10, the RED; R48, R49, R63, R66. Plan 7b-1's "The interleaving test" and R33, R35, R46 hold. **Contracts:** none new.

**Files:**
- Modify: `internal/work/app/interleave_world_test.go` (`faultRecordsRead`, `faultEventsRead`; the pairing; `recordsPort.Load`, `Active` and `UnstaffedBy` and `eventsPort.Load` spend them; `faultNames`), `interleave_test.go` (read faults among the events, at a low weight; this task's tests), `interleave_invariants_test.go` (invariant 10's check in the fake runner and sessions, and its violation rows)
- Modify, for each fault of the coordinator the runs find: the file it is in, with a unit test that reproduces it first

**Interfaces:**
- Consumes: Plan 7b-1's `world`, `fault`, `armFault`, `spend`, `beforeWrite`, `runSchedules`, `calls`, `records`; `item.State.Status`.
- Produces: the read faults, armed per instance; `func (w *world) closedAt(id item.ID) (int, bool)`, the position in the world's log of the commit of the item's latest close without a reopen after it, which invariant 10 reads; its two checks, `func (w *world) noWorkForAClosedItem(c *portCall) error`, at every port call, and `func (w *world) nothingRunsForAClosedItem() error`, after every event, over each live instance's sessions (`in.live`) and its runner's turns (`in.turns`).

**Notes for the implementer:** a read fault fails the read with an error that is neither `ErrLeaseLost` nor of a later version, before the store's lock is taken, and is logged as an event of the schedule, so the replay keeps it. A write fault spent arms its paired read fault from the schedule's random source, never from the clock; read faults are armed per instance, where `w.armed` is per world today (the plan review's M9). Invariant 10's part (a) is checked by the fake ports at every call, as invariant 1 is, and its part (b) after every event, each with the instance's holding. The read faults among the events change every seed's schedule, so 7b-1's recorded seeds no longer replay; the report and the ledger say so (the plan review's M10). **The RED, first:** in a throwaway worktree under the scratchpad (`git worktree add`, R66), revert 60443e4 and 49ea574 (`git revert --no-commit`, or by hand as "The RED" says), copy this task's test files in, and run the default run, then a soak of up to 40 minutes, until a seed fails invariant 10 (I1) and one fails invariant 4 (I2); replay each with `-interleave.seed`. The report gives both seeds, their invariants and the replay commands. Then remove the worktree. **Timebox** (R63): 2 days, the task's ceiling; if the RED has not reproduced both by then, stop and report: execution stops for the author (the author's decision of 2026-11-07).

**Tests (write first):**
- `TestTheInterleavingFakesFailAReadAfterAWriteFailure` (the plan's): on hand-written schedules, a records append that loses its answer, with its paired read armed, has the act's lookup fail: a close answers the error and drops its run; a promotion answers the error and counts its item; the same for a board append and its `Load`, and `UnstaffedBy` failing after an off's write. Fails first: a build failure, no read fault.
- `TestTheInterleavingFakesInjectEachFault` gains a row for each read fault alone: `Load`, `Active` at a takeover, `UnstaffedBy`, and the board's `Load`, each failing once and the next read succeeding.
- `TestEachInterleavingInvariantCatchesItsViolation` gains invariant 10's rows: a fake runner told to start a turn of a run whose item's `item_closed` committed (must fail); a `Sessions.Move` after the close (must fail); a launch that completes into a running turn after the close, the instance still holding (must fail, part (b)); a live session of the run at an event's end after the close (must fail); a turn started by an instance whose holding had ended, after the close (must pass); a turn started after a close and its reopen (must pass).
- **The RED** (not a test of its own): `TestRandomSchedulesKeepTheLifecyclesInvariants` fails on invariant 10 and invariant 4 with 60443e4 and 49ea574 reverted, as the notes say, and passes without the revert.
- `TestRandomSchedulesKeepTheLifecyclesInvariants` and `TestConcurrentSchedulesKeepTheLifecyclesInvariants` pass on main with the read faults, within R46's budget of 20 seconds, the report giving the default run's time; a fault found is fixed test first, in a commit of its own.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 -run '^(TestTheInterleavingFakesFailAReadAfterAWriteFailure|TestTheInterleavingFakesInjectEachFault|TestEachInterleavingInvariantCatchesItsViolation)$' ./internal/work/app/`. Expected: FAIL, a build failure: no read fault.
- [ ] **Step 2: Write** the read faults, their pairing and invariant 10, then run the command of Step 1. Expected: PASS.
- [ ] **Step 3: The RED,** in the throwaway worktree, as the notes say. Expected: a seed failing invariant 10 and one failing invariant 4, each replayed; the worktree removed.
- [ ] **Step 4: Run** `go test -race -count=1 -run '^(TestRandomSchedulesKeepTheLifecyclesInvariants|TestConcurrentSchedulesKeepTheLifecyclesInvariants)$' ./internal/work/app/` on main. Expected: PASS, or a fault of the coordinator, which gets a unit test from its seed's log, then its fix, in a commit of its own.
- [ ] **Step 5: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 6: Commit:** `git add internal/work/app`; `git commit -m "test(work): the interleaving world fails reads after write failures, and nothing runs for a closed item"`, each fix in its own commit before it.

### Task 2: `steps` reloads the item after a step whose outcome is unknown

**Decisions:** "The carried structural work": the reload; R50. Plan 7b-1's Decision 4 (the account; `walkSoon`). **Contracts:** none new.

**Files:**
- Modify: `internal/work/app/coordinator.go` (`steps`), `queue_test.go` or `coordinator_test.go` (this task's tests)

**Interfaces:**
- Consumes: `load`, `refresh`, `failed`, `retry`, `ofLaterVersion`, `ErrLeaseLost`, `c.ctx`.
- Produces: no new name; `steps`' doc says when it reloads.

**Notes for the implementer:** the reload comes after `failed` and before `retry`, only while `c.held` and `c.ctx` is live; it calls `load` once, whose error, if any, is dropped beyond the log `load` already gives. `advance`'s walk then runs on the refreshed account. The reload never runs inside a walk's own loop twice for one item: the walk advances each item at most once.

**Tests (write first):**
- `TestAFinishWhoseAnswerIsLostFreesItsSlotAtOnce` (the plan's): with a limit of 1, X investigating and Q waiting; X's round decides `quiet`; `finish`'s `item_awaiting_close` lands with its answer lost; Q starts in the same advance, the clock unmoved. Fails first: Q starts only after the retry wait.
- `TestAnAdmittedItemLeftNotStaffedWithItsAnswerLostLetsTheNextStartAtOnce` (the plan's): with a limit of 1, the walk admits Q1, whose routing fails its check under the revision, and Q2 waits; Q1's `item_awaiting_close` `not_staffed` lands with its answer lost; Q2 starts in the same advance, the clock unmoved. Fails first: the account holds Q1 admitted and counted until its retry, and Q2 starts a retry wait later.
- `TestAStepWhoseReloadFailsTooIsTriedAgain` (the plan's, a guard): the append's answer lost and the reload failing, the item's retry is armed and, after the retry wait, carries the item on. Passes at Step 1: today's retry.
- `TestAStepThatLostTheLeaseDoesNotReload` (the plan's, a guard): a step refused with `ErrLeaseLost` fences the coordinator and loads nothing more.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/app/`. Expected: FAIL, the first two for the reasons their lines give.
- [ ] **Step 2: Implement** the reload.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app`; `git commit -m "fix(work): a step whose outcome is unknown reloads its item, so the account follows what landed at once"`.

### Task 3: `carryOutKill`'s filter, and the drain's residual Error line

**Decisions:** "The carried structural work": the filter (item 14) and the Error line (item 15); R51. **Contracts:** C3 (the level), C4 (an on's `stopped`).

**Files:**
- Modify: `internal/work/app/killswitch.go` (`carryOutKill`; `owing`'s stops, which the preview reads), `coordinator.go` (`failed`), `interleave_invariants_test.go` (`namesEveryRun`), `killswitch_test.go`, `killswitch_drain_test.go`

**Interfaces:**
- Consumes: `carryOutKill`, `owing`, `wouldKill`, `runStop`, `stopOf`, `item.StopKilled`, `failed`, `c.ctx`.
- Produces: no new name.

**Tests (write first):**
- `TestAnOnsAnswerNamesAmongItsStopsOnlyTheRunsItKilled` (the plan's): two runs in round 1; the first's round ends deciding `quiet`, its stop not yet recorded; the on comes: its halt records the first run's stop as `quiet`, the second's as `killed`; the answer's `Stopped` names the second alone, and neither run is in `Owed`. Fails first: the answer names both.
- `TestAnOnsPreviewLeavesOutARunWhoseRoundDecidedItsStop` (the plan's; the plan review's M3): the same two runs; the on's preview names the second run alone among its stops, as the answer will. Fails first: the preview names both.
- `TestAStepFailingOnceTheContextEndedLogsBelowError` (the plan's): a records store that holds an append until the test ends the holding's context, then fails it with `context.Canceled`: the step's failure is logged at Info, and no line at Error. Fails first: the line is at Error.
- `TestAStepFailingWhileHeldStillLogsAtError` (the plan's, a guard): the same failure while the context is live logs at Error.
- `TestEachInterleavingInvariantCatchesItsViolation` gains a "must pass" row: an on's answer that names neither a run whose decided stop its halt recorded as `quiet`, nor its item among the owed.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/app/`. Expected: FAIL, the first three for the reasons their lines give.
- [ ] **Step 2: Implement** the filter, the preview's, `namesEveryRun`'s exemption and `failed`'s level.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./internal/admin/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app`; `git commit -m "fix(work): an on's answer names only the runs it killed, and a step cut off by a drain logs below Error"`.

### Task 4: The checkpoint's soak run (the controller's)

**Decisions:** "The carried structural work": the checkpoint; R65. **Contracts:** none.

- [ ] **Step 1: The soak.** `go test -race -count=1 -v -timeout 60m -run '^(TestRandomSchedulesKeepTheLifecyclesInvariants|TestConcurrentSchedulesKeepTheLifecyclesInvariants)$' ./internal/work/app/ -args -interleave.for=40m` (R65). Expected: PASS; the log gives each test's base seed and count. A failure's seed becomes a unit test of the coordinator, then its fix, in a commit of its own, and the soak runs again before Task 5, the documents. The report gives the counts beside 7b-1's last soak (37,911 sequential and 26,015 concurrent schedules).
- [ ] **Step 2: Record** the counts and base seeds in the ledger; no commit unless a fix landed, each in its own commit, test first.


### Task 5: The documents

**Decisions:** the author's decisions of 2026-11-07; every decision of this plan.

**Files:**
- Modify: `plan/2026-10-12-implementation-roadmap.md`:
  - row 7b-2: its scope, "the stale-account class closed structurally: reads that fail in the interleaving test, invariant 10, the reload after an unknown outcome, and 7b-1's carried items 14 and 15", and its size, this plan's (about 0.5 to 0.8 raw person-weeks), then, at the branch's end, its size as executed and calendar time;
  - row 9a: its prerequisites become Plan 7b-1 and Plan 7b-2 as the structural closure: 9a needs neither the resume nor links, since the extension offered and the link rulings are Plan 9b's, and 9a's item view shows the item's own incident;
  - row 7c-1: it gains the resume, extensions and their budgets, links and unlinks with migration 0011, the admin routes, the interleaving test's second half and scenarios 24 and 25 (version 3's Tasks 5 to 12), and its size, stated honestly: about 2.5–3 person-weeks on the row, 3.25–4.35 as raised on 2026-11-02, plus about 1.05 to 1.25 raw person-weeks moved (1.15 to 1.7 with the ratio), so about 4.3 to 5.6; rows 8a and 10a keep 7b-2 among their prerequisites only for the structural closure, and reach the resume through 7c-1, already theirs;
  - the status line: amended on 2026-11-07, the scope cut and the move;
  - Section 5's account, at the branch's end.
- Modify: `plan/adr/ADR-016-delivery-build-before-team.md`, History, 2026-11-07: version 3 of Plan 7b-2 came to about 1.85 raw person-weeks against the row's 0.65–0.73 as raised, and the trigger fired at plan time for the full scope; the author cut 7b-2 to the structural group, about 0.65 raw person-weeks, and moved the resume, extensions and links to Plan 7c-1, which now carries about 1.05 to 1.25 raw person-weeks more and likely crosses its own trigger once its design is sized; the move shifts the work, it does not save it. At the branch's end, the size as executed.
- Modify: `plan/2026-11-01-plan-7b1-coordinator-lifecycle-core.md`, "Constraints carried to later plans", a pointer under its Plan 7b-2 entry: items 1 to 11 are now carried to Plan 7c-1; item 12's first half, and items 13, 14 and 15, to Plan 7b-2 (this plan); item 12's second half to Plan 7c-1.
- Modify: `README.md`: no plan list names 7b-2's scope (checked at 5a1321a); its account of the interleaving test gains the read faults, invariant 10, and that 7b-1's seeds no longer replay.
- Modify: this plan's status line.

**Checks:** `grep -rniE "emplo[y]ee" README.md plan/*.md plan/adr/*.md` finds no new line; the roadmap's rows 7b-2, 7c-1, 9a, 8a and 10a agree with each other and with this plan.

- [ ] **Step 1: Write the documents.**
- [ ] **Step 2: Run the checks.** Expected: as stated.
- [ ] **Step 3: Gate.** `make lint`. Expected: `0 issues.` twice.
- [ ] **Step 4: Commit:** `git add plan/2026-10-12-implementation-roadmap.md plan/adr/ADR-016-delivery-build-before-team.md plan/2026-11-01-plan-7b1-coordinator-lifecycle-core.md plan/2026-11-07-plan-7b2-stale-account-closure.md README.md`; `git commit -m "docs: Plan 7b-2 cut to the stale-account closure, the resume, extensions and links moved to Plan 7c-1"`.

## The branch's end (the controller's)

- [ ] **The full `make test-linux`,** before the final review. Expected: every package `ok`. A run that fails only on the carried `ETXTBSY` flake of `internal/runner/process` runs again, and the report says so.
- [ ] **The soak run,** before the final review, or Task 4's under R75: `go test -race -count=1 -v -timeout 60m -run '^(TestRandomSchedulesKeepTheLifecyclesInvariants|TestConcurrentSchedulesKeepTheLifecyclesInvariants)$' ./internal/work/app/ -args -interleave.for=40m`. Expected: PASS, each test logging its base seed and count.
- [ ] **The final review,** by a fresh reviewer on Opus 5.5, of the whole branch against this plan; then its fix wave; then `make test-linux` and the soak run again.
- [ ] **The record:** "Changes made during execution", the size as executed, this plan's status line, and the roadmap's row and ADR-016's History with the size as executed.

## Size

On the roadmap's yardstick, in days of one person. "Raw" is the planner's estimate; "adjusted" applies 7b-1's executed-to-plan ratio, 3.5–4 person-weeks against 2.95–3.25, that is 1.08 to 1.36, as version 3 did.

| Part | Task | Raw days |
|---|---|---|
| Read faults, invariant 10 in two parts, the RED | 1 | 1.25–2.0 |
| The reload after an unknown outcome | 2 | 0.25 |
| The filter, the preview's, and the Error line | 3 | 0.25 |
| The checkpoint's soak and any fix | 4 | 0.1–0.3 |
| The documents | 5 | 0.25–0.35 |
| The branch's end: two Linux runs, two soaks (one Task 4's under R75), the final review and its fix wave | — | 0.5–0.9 |
| **Total** | | **2.6–4.05, about 3.3: 0.5–0.8 person-weeks** |

- **Adjusted:** 2.6 × 1.08 to 4.05 × 1.36, about 2.8 to 5.5 days, **0.55 to 1.1 person-weeks**; at the raw middle, about 3.6 to 4.5 days, 0.7 to 0.9.
- **The trigger,** against the same baseline as version 3: the row as ADR-016's History of 2026-11-02 raised it, 0.65–0.73 person-weeks, a line of about 0.98–1.09. Raw, 0.5 to 0.8, does not cross; adjusted, it crosses only at its very top, 1.1. Against the row's unraised 0.5 (line 0.75), the raw top, 0.81, crosses, and the raw middle, 0.66, does not. The baseline was set for the row's old scope; Task 5 restates the row for the new one.
- **Most of the risk is Task 1:** its RED is open-ended, and 7b-1's interleaving tasks ran 1.5 to 2 times their timebox; Task 1's 2 days are its ceiling, and the author's decision of 2026-11-07 says what happens when it fires.
- **Where the rest went:** version 3's Tasks 5 to 12, about 5.3 to 6.2 raw days (1.05 to 1.25 person-weeks; 1.15 to 1.7 adjusted), go to Plan 7c-1 (below).

## Plan review

The reviews of versions 1 and 2 reviewed the full scope. Their findings on Tasks 1 to 4 are answered here as in version 3: I1 (invariant 10 in two parts, R72), M3 and N2 (the preview filtered through `runStop`'s `killed`), M6 (invariant 7's quiet check acknowledged), M9 (read faults per instance), M10 (old seeds no longer replay), M12 (Task 1 raised, its timebox its ceiling). Their findings on Tasks 5 to 12 (C1, I2 to I8, the other Minors, N1 and N3 to N6) are answered in version 3, which Plan 7c-1 starts from.

## Not in this plan

- **Plan 7c-1:** the resume, extensions, links and unlinks, migration 0011, the admin API's extend, link and unlink routes and the item view's new fields, `lifecycle.extension`, the interleaving test's second half, and scenarios 24 and 25 (the author's decision of 2026-11-07).
- **Plan 9a, 9b, 8a, 8b:** as Plan 7b-1 carried them.

## Constraints carried to later plans

- **Plan 7c-1** takes Tasks 5 to 12 of `plan/backups/plan7b2/plan-7b2.v3.md`, with its rulings R48 to R74 where they concern that work (R52 to R62, R64, R67 to R71, R73, R74), and its two reviews' findings as version 3 answered them (`plan-review.md`, `re-review.md`); the design's Decisions 16 to 18 (`plan/backups/plan7b/design-v4.md`) and its 36 tests for 7b-2, `TestALateCloseRetryAfterAReopenAndAResumeLeavesTheRunGoing` among them, each in the task version 3's Appendix A gives it; migration 0011, at its own level, with its index; Plan 7b-1's carried items 1 to 11, on which that work relies; and item 12's second half, the interleaving test's extension, link and unlink acts and its invariants' amendments. 7c-1 also decides whether its platform link of a flap takes Decision 18's [72 h] window, which is a check of a person's `Link`; and its firing resume reuses the resume's fold, `decided()`, `open`'s `resume` entry and `countAsStarting`.
- **Every later plan:** as Plan 7b-1 carried it. The interleaving test's read faults and invariant 10 hold for every act a later plan adds to it.

## Changes made during execution

None yet. Each amendment is marked in place "(amended in execution, <date>: ...)" and listed here, one line each.
