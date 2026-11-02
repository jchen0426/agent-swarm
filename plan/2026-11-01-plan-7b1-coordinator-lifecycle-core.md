# Plan 7b-1: the coordinator's lifecycle core — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Draft of 2026-11-01, revised three times on 2026-11-02: with the author's decisions of that day and the controller's calls on the planner's notes; with the controller's rulings on R37 (a kill switch write whose outcome is unknown fails safe) and R39 (clock steps both ways); and with the fixes of a fresh plan review on Opus 5.5 (approve with fixes: 1 Critical, 6 Important, 14 Minor), and the controller's ruling on its Critical (R42). Approved by the author on 2026-11-02, with its size of about 2.95 to 3.25 person-weeks. Subagent-driven execution on main: a fresh implementer for each task, a fresh reviewer on Opus 5.5 for each task, two for Task 12, and a whole-branch review on Opus 5.5 at the branch's end. Tasks 1-22 executed (2026-11-02 to 2026-11-03); the branch's end (`make test-linux`, the soak run, the final review and its fix wave) pending. See "Changes made during execution". |
| Date | 2026-11-01 |
| Roadmap | `2026-10-12-implementation-roadmap.md`, row 7b-1: the first plan of the core of Phase 0 (ADR-016), built in the order 7b-1 (this plan), 7b-2, 3c, 9a, 7c-1, 8a and 10a. Plans 7c-2, 8b, 9b and 10b form the onboarding build, made once a team adopts the platform. |
| Size | The final design sized 7b-1 at about 12.2 days, about 2.45 person-weeks: 1.63 times the roadmap's estimate of 7.5 days (1.5 person-weeks), above ADR-016's revisit trigger of one and a half times (11.25 days) by about 0.95 days, and no scope moved to stay under the trigger. **The author accepted that size on 2026-11-02 (ADR-016):** the trigger's firing is recorded, and the plans after 7b-1 are corrected by a factor of about 1.3–1.45, Plan 7b's growth through its three design reviews, from about 2.15 to 3.1 person-weeks for both parts, rather than re-estimated. The same day the author added a randomized interleaving test, Tasks 17 and 18, at about 1.5 to 2.5 days, which brought 7b-1 to about 13.7 to 14.7 days, about 2.75 to 2.95 person-weeks, as the roadmap's row now records it (2fc0b9d). The controller's rulings of the same day on R37 and R39, and the rules they bring (R40, R41), add about 0.75 to 1.25 days: R37's mark and its checks, about 0.25 to 0.5 (Task 14); R41's two rules, in both stores, about 0.2 to 0.25 (Task 6); R40's test, under 0.1 (Task 9); the world's three clocks and invariant 9, about 0.25 to 0.4 (Tasks 17 and 18). The plan review's fixes add about 0.25 day of work the plan had not sized: Task 3's two changed tests, Task 12's test hooks, R43's fallback, Task 11's tenants and the soak's duration. **7b-1 now comes to about 14.75 to 16.25 days, about 2.95 to 3.25 person-weeks**, which the author accepted with this plan's approval on 2026-11-02; the roadmap's row records it. Tasks 17 and 18 are timeboxed at their high end (R46). |
| How it was made | The design went through four versions. Three fresh reviewers on Opus 5.5 reviewed version 1 (1 Critical, 8 Important, 22 Minor), version 2 (1 Critical, 2 Important, 16 Minor) and, scoped to its changes, version 3 (0 Critical, 1 Important, 9 Minor). Version 4 answers every finding and is final. After review 1, the author split Plan 7b in two, let the kill switch's on through a drain, and approved three spec amendments. This plan, revised on 2026-11-02 with the author's two decisions of that day, the controller's calls and rulings, and a plan review's fixes, carries 7b-1's half of version 4: Decisions 1 to 15, their contracts and carries, each of the 185 tests version 4 names for 7b-1, every one in exactly one task, and the five earlier tests it names as changing or standing. Of the design's other names, 36 tests are 7b-2's and one is carried to Plan 9b. |
| Decided by the author | On 2026-10-14, 2026-11-01 and 2026-11-02, as "The author's decisions" lists. The design's other calls are marked in each decision, "the designer's call" or "the controller's call"; this plan's own are R11 to R46, under "My calls, for the author to check". Approving this plan approves them. |

**Goal:** Give every work item the lifecycle around its run: a queue past the limit of items investigating at once, with promotion; close from any state, which stops a running run mid-round; reopen within a window; auto-close; and a kill switch that stops every run at once and staffs no item until an admin turns it off. Each is a use case of the Work context that shows its consequences before it acts, and that a retry, or a lost answer, carries on safely. Serve's admin API exposes them now, under both runners, through a takeover and a drain. A randomized interleaving test drives the coordinator through many seeded schedules of acts, faults, takeovers and clock steps both ways, and checks the lifecycle's invariants after every step.

**Architecture:**
- **A pure move first** (Decision 1). The turn's lifecycle moves to `turns.go`, and the round's to `rounds.go`, unchanged; then one rename, before any change of behaviour.
- **Four states** (Decisions 2 and 3). The fold gains `closed`, three records, the stops `killed` and `closed`, rounds that ended `stopped`, and a table of what each state admits. A pure `Run.Halt` gives the records that stop a run now.
- **Acts** (Decisions 6, 7 and 13). Close, reopen and promotion share one shape: an actor; a reason checked as an agent's text is; a key kept hashed on the act's record; a preview that records nothing; and a failed append that loads the item and carries on from its state now.
- **Halt and the drop** (Decision 3). Every halt drops the run's state in memory, ends its sessions and turns, and closes its upstream MCP sessions. `finish`, the old step `close`, ends every stopped run.
- **The queue** (Decision 4). The coordinator keeps an account of its items, with admission marks. A walk at the end of each advance admits queued items, in their order, while a slot is free; a promotion may take the items investigating to the limit and one.
- **Auto-close** (Decision 8): timers from the records, paused while the kill switch is on, and never closing an item before its deadline on the database's time (R41).
- **The kill switch** (Decisions 5 and 12). An audited, chained table of its own, in migration 0010. The coordinator's view follows the table's every answer, and acts on any change by two rules. A write whose outcome is unknown leaves the view unsettled, and the coordinator begins no work until a read settles it (R37). A run, or an item with no run, owes a kill by row numbers, so a halt that failed is carried out later. The leader compares the view with the table at each renewal. An on during a drain is recorded under the draining holding and deferred to the next takeover.
- **The admin API** (Decision 14) gains the lifecycle's routes, and the item view and the status their fields. The deployment file gains a `lifecycle` block (Decision 15).
- **The interleaving test** (the author's decision, 2026-11-02). Two instances, each the real leader over the real coordinator, share one set of in-memory fakes that own every timer and port and inject faults: lost answers, failed writes, a lease lost mid-act, failed renewals, drains, takeovers, steps of the database's and the processes' clocks forward and back, kill switch rows written out of band, failed and cancelled launches, and concurrent acts. Many seeded schedules run under `-race`, the lifecycle's invariants are checked after every step and once quiet, and a seed replays its schedule's order of port events.
- **The order.** The move; the domain; the carried fixes; the deployment block; close and the halt; auto-close; the acts' framework; reopen; the queue; promotion; the kill switch, in six tasks; the interleaving test, in two; the admin API; the harness and its five scenarios, in two tasks; the documents.

**Tech Stack:**
- Go 1.26 (`go 1.26.0`)
- pgx v5.10.0, testify v1.12.1, testcontainers-go v0.44.0, modelcontextprotocol/go-sdk v1.8.0, go.yaml.in/yaml/v3 v3.0.5, as before; no module joins the graph
- Postgres 17 (`postgres:17-alpine`) in labelled containers, as before

**Spec:** `plan/2026-10-11-agent-swarm-platform-spec.md`, binding where it speaks; the design is its argument. Read these before the tasks that implement them:
- Section 4.1: the lifecycle, the queue, closing, reopening and auto-close; Section 4.2: rulings, consequences first, a ruling's inputs, closed items;
- Section 5.1: openings, the limit at open, the kill switch and the cap at open, openings by key; Section 5.6: stops; Section 5.7: close;
- Section 8.2: the coordinator's crash, the lease, the drain; Section 8.3: the kill switch;
- Section 15, and Section 0's rule on bracketed values.

Also read ADR-004 (timers from the records), ADR-016 (its revisit triggers), Plan 7a (`plan/2026-10-18-plan-7a-coordinator-core.md`: Decision 8, which left `killed` and `closed` to 7b, and its constraints for 7b), Plan 7d-1 (`plan/2026-10-20-plan-7d1-serve-and-harness.md`: Decisions 14, 18 and 19, and Contracts C1, C3, C4 and C9) and Plan 7d-2 (`plan/2026-10-28-plan-7d2-docker-runner.md`: Contract C10, R5, Decision 7, and its carried constraints). The design and its three reviews are kept in the author's backups, `plan/backups/plan7b/design-v4.md` and `review-1.md` to `review-3.md` (git-ignored), with `plan7-carry-sweep.md`, the sweep of the constraints Plans 1 to 6b handed to "Plan 7". This plan is self-contained: it carries every decision, and the design's reason wherever the reason keeps an implementer from a wrong simplification.

**How a task runs.** The plan is light: it gives each task its decisions, the interfaces it consumes and produces, and its tests by name, with what each asserts and why it fails before the implementation. It gives no implementation code; a code block appears only where a contract's exact form is fixed. At execution each task's code is written test first: the implementer writes the listed tests, watches each fail for the reason given, then writes the code. The listed tests are the minimum; more may be added, and a test's name may change to match the code's vocabulary, with a ruling in the ledger. Tests the design does not name, which this plan adds, are marked "(the plan's)". Each task's brief carries, verbatim, the Global Constraints, the Decisions and the Contracts it names. Each task is reviewed by a fresh reviewer on Opus 5.5, Task 12 by two, and the whole branch at its end.

## The author's decisions

- **2026-10-14** (ADR-016): Phase 0 is built before a team adopts the platform.
- **2026-11-01** (ADR-016, History): at the checkpoint after Plan 7d, keep building, Plan 7b next. The brainstorming questions were skipped, and the design asked for directly.
- **2026-11-01, after review 1:**
  - **Split Plan 7b in two,** each part tested and reviewed on its own: 7b-1, this plan; 7b-2, the resume, extensions and links, with its own migration, 0011. The boundary is "Constraints carried to later plans", Plan 7b-2.
  - **The kill switch accepts "on" during a drain,** under the draining leader's holding, which is still valid; a standby refuses it. Off stays the leader's alone, and an admin's.
  - **Three spec amendments:** extend, close, reopen and link are recorded on the item, not as board entries, because of spec Section 10.2's invisible arm; a new `resume` board entry (7b-2); the items a person opened close by themselves, their trigger treated as resolved at their opening.
- **2026-11-01, the re-estimate** (5082373; roadmap Section 2; ADR-016):
  - Plans 7c, 8, 9 and 10 split in two, and Plan 3c added;
  - the core built before a team, in the order 7b-1, 7b-2, 3c, 9a, 7c-1, 8a, 10a; Plans 7c-2, 8b, 9b and 10b in the onboarding build;
  - the items list goes to Plan 9a, and the board rulings' use case and its preview to Plan 9b; the previews of 7b's acts stay in 7b;
  - the roadmap records 7b-1 at "about 1.5 (design: about 1.9)"; a design that comes to more than one and a half times its estimate is a revisit trigger.
- **2026-11-02:**
  - **7b-1's size is accepted** (ADR-016): the trigger's firing is recorded, and the plans after 7b-1 are corrected by a factor of about 1.3–1.45, Plan 7b's growth through its three design reviews, rather than re-estimated. The roadmap and ADR-016 record it, with its History entry of 2026-11-02 (2fc0b9d).
  - **A randomized interleaving test** of the kill switch, the lease and the queue joins 7b-1, at about 1.5 to 2.5 days: the real coordinator against in-memory fakes of its ports, which inject the faults its decision lists; many seeded schedules under `-race`, each seed printed on a failure and replayable; the lifecycle's invariants checked after every step and once quiet. It is Tasks 17 and 18.
- **2026-11-02, at approval:** this plan approved, for subagent-driven execution on main, with its size of about 2.95 to 3.25 person-weeks, which the roadmap's row records with the plan.

## My calls, for the author to check

The design marks its own calls in each decision below; approving this plan approves them. Three kinds need the author's eye: the controller's calls the design records, the controller's calls and rulings on the planner's notes, and the planner's resolutions.

**The controller's calls, which the design records (2026-11-01).** Each is marked in its decision, with what it costs if wrong:
1. Auto-close pauses while the kill switch is on, and turning it off lists every item it left unstaffed (Decisions 5 and 8).
2. Queued items are not staffed when the kill switch turns on (Decision 5).
3. A promotion starts an item that never ran, under the rule of the limit and one (Decision 4).
4. At each renewal, the leader compares the coordinator's view of the kill switch with the table, in one indexed read, and has the coordinator act on a newer row. It adds no on from a standby (Decision 5).
5. The sweep's rows 16, 19, 24 (its inline half) and 42 are 7b-1's (Decisions 3, 11 and 15).
6. A halt by the step drops the run's state in memory, as Close does; `begin`'s guard does not cover every case, and stays the second line (Decision 3).
7. The kill switch's on during a drain survives the drain's cancellation of the holding's context, is bounded in time, and stays guarded by the lease's holding (Decision 5).
8. No scope moved to stay under ADR-016's trigger; the author then accepted the size (2026-11-02).

**The controller's calls and rulings on the planner's notes (2026-11-02):**
- R29, `make lint` in every task's gate: accepted.
- `TestAHaltIsCarriedOnFromItsRecordsAfterACrash` moved from Task 5 to Task 12, since a closed item is no longer carried on at a takeover (Decision 6): accepted.
- Task 20's gate also runs the full `make test-e2e` under the process runner, the first gate that runs the existing scenarios against the lifecycle.
- The kill switch's on from a standby is not in 7b-1. The question is carried to Plan 9a, where the author decides.
- The design and its reviews are in `plan/backups/plan7b/`.
- R33 (sorted loops), R34 (the interleaving test after Task 16), R35 (what a seed replays), R36 ("no model call" at the coordinator's boundary) and R38 (three more invariants): accepted as proposed.
- **R37, a ruling: a kill switch write whose outcome is unknown fails safe,** keeping the design's anti-goal, with one residual: a turn already running when the row commits keeps its session until the coordinator acts on the row. While a write's outcome is unknown, the view counts as on for starting work, admission and promotion staff nothing, and a read that succeeds settles it. Invariant 1 counts from the commit of an on-row a coordinator writes; one written out of band is honoured within a renewal period, a residual.
- **R39, a ruling: clock steps both ways.** Every invariant holds under steps forward and back but the queue's order, which tolerates R9's residual and nothing more; auto-close never closes an item before its deadline on the database's time; no safety invariant depends on the clock going forward.
- The factor, about 1.3–1.45, is in ADR-016 and the roadmap (2fc0b9d); Task 22 adds no History entry for it.
- **R42, a ruling on the plan review's C1: an act's `Idempotency-Key` is scoped to its item.** A key reused for another act on the same item is refused; the same key on another item is that item's own act.

**The planner's resolutions,** R11 to R46 (Decisions, "Resolutions"), where the design left a choice to the builder, or a ruling or the plan review left one to the plan. The ones most worth a look:
- **R11.** A halt whose read of the run's spend fails records the last recorded spend, and goes on.
- **R12.** A reopen of an item with no run returns it to `not_staffed` with the cause it had before its close, or `closed` when it never awaited its close.
- **R13.** `item_promoted` carries the reason, which C19 left out though Decision 9 has a promotion take one.
- **R14.** An act repeated with its key answers `changed: true`, with the limit and the items investigating now; its other effects are in its records.
- **R15.** Error codes on the lifecycle's routes alone: the opening route keeps Plan 7d-1's `{"error"}`.
- **R32.** Before Task 7, a close records its reason after a check of its form alone; Task 7 adds the check of a person's text.
- **R37's details,** which the ruling left to the plan: every unknown outcome marks the view unsettled, an ended context's included; the step waits rather than open or hold a round, so no session is issued; a refused turn arms its retry; a promotion answers `503` (`ErrUnsettled`) rather than `kill_switch_on`; auto-close pauses too; and the residual the ruling does not close: a turn already running when the row commits keeps its session until the coordinator learns of the row.
- **R39's bound.** The database's clock steps forward by less than the lease's margin within a lease term: a larger step breaks Plan 7d-1's lease, not 7b-1's rules.
- **R40.** The queue orders by its rank, the opening's time then the item's id, within a holding as after a takeover, so R9's swap can show within a holding too; invariant 9, the queue's order, tolerates that swap and no other.
- **R41.** A timer that fires before its deadline on the coordinator's clock arms itself again, and the records store refuses an automatic close it would stamp before its `due` (`item.ErrNotDue`), since the coordinator's clock is only an estimate of the database's.
- **R43.** An item that never started, whose type the current revision no longer holds, takes the default auto-close delay [30 min] rather than trying for ever.
- **R46.** The interleaving test's budget: a world whose type closes by itself after 1 minute, the default schedules run once per gate within 20 seconds, a soak run for 40 minutes, and a timebox on Tasks 17 and 18 at their high end.

## Questions only the author can answer

None remain open. The size, about 2.95 to 3.25 person-weeks, was accepted with this plan's approval on 2026-11-02, and the roadmap's row records it.

The kill switch's on from a standby is carried to Plan 9a, where the author decides ("Constraints carried to later plans").

## Global Constraints

- **Module and Go version.** The module path is `github.com/jchen0426/agent-swarm`, with `go 1.26.0`.
- **Vocabulary.** Names come from the spec: work item, run, round, turn, session, board, entry, notice, stop reason, lease, holding, epoch, runner, sandbox. Agents are "AI agents" or "agents"; never "employee". This plan adds the spec's words for the lifecycle: queue, promotion, close, reopen, auto-close, kill switch.
- **Domain purity.** `internal/work/item`, `internal/work/board`, `internal/work/tokens`, `internal/holding` and `internal/outcome` import no database, network or framework package. This plan's new `internal/work/control` is as pure (Contract C16).
- **Ports.** A port is declared by the package that uses it. Wiring lives in `cmd/agentswarm`. The Docker runner reaches the engine only through its own `Engine` port (Contract C14).
- **Decide, record, act.** Every decision the coordinator makes is a work record written before its effects, and every effect can be repeated (Plan 7a).
- **Audit records.** Append-only, chained, and written with the database's `clock_timestamp()`.
- **The holding.** Every guarded write runs under a context that carries the epoch of the holding it acts for. Every request to the model vendor and to an MCP server, every turn's start and every sandbox's start is preceded by `Holding.Valid` for that epoch.
- **Secrets.** A session token never appears in a record, a log, an error, a board entry, a turn's output file, a process's arguments or environment, a container's configuration (its command, environment or labels), the engine's logs, or anything the model vendor or an MCP server receives: it travels only in the spec on the turn's standard input, and the runtime sends it only in request headers. The vendor's key enters a test container only as a file mounted read-only, never as an environment variable or an argument, which the engine keeps in the container's configuration.
- **Migrations.** Expand-only. Each declares its `min_reader`, by default its own level; every migration of this plan is at its own level. This plan adds one, 0010, at level 10 (Decision 2).
- **Libraries.** No new module. Code under `test/e2e` imports the standard library, testify and testcontainers-go only (the depguard rule `e2e`): the harness checks containers with the `docker` command line.
- **Values.** A value fixed in code is written without brackets; a bracketed value is a default a deployment may change (spec Section 0).
- **Test first.** Every test a task lists is written before its implementation and seen to fail for the reason the task gives, except a test the task marks as a guard: a guard keeps a behaviour of today's code that the task must not lose, passes at Step 1, and is not to be "fixed" into failing.
- **Language.** Code, comments and documents are in English.
- **Docker labels.** Every container a test or a Makefile target starts carries the label `agentswarm.project=agent-swarm`; sandboxes get it through `runner.docker.labels`.
- **A person's text.** A reason's text, or any text a person gives, is never stored or shown unredacted, and never quoted back in an error (Decision 9).
- **The gates.** Every task ends green on the Mac: `make lint`, which runs `golangci-lint run ./...` and `GOOS=linux golangci-lint run ./...`, each at `0 issues.` (R29, accepted); its own packages' tests with `-race`, the integration-tagged ones of those packages included, as its Step 4 gives them, which from Task 17 on include the interleaving test's short default run, once, within its budget of 20 seconds (R46); and the e2e scenarios it adds, under the process runner, Task 20 running the whole `make test-e2e` under the process runner besides. The full `make test-linux`, which runs the whole suite in the Linux test image, `make test-docker`'s Docker path included (Plan 7d-2's carried rule for plans that touch serve), and the interleaving test's soak run each run twice: at the branch's end, before the final review, and after the final review's fixes. No task in the middle runs either. **This is the controller's rule, and departs from the design's Section 3,** which ran `make test`, `make test-integration`, `make test-e2e` and `make test-docker` in every task, and `make test-linux` in the task that wires serve: each task runs what its change can break, and the branch's end runs the whole suite, Docker included, before the final review, so Docker-only failures are fixed before that review reads the branch. Commit by path, never with `git add -A`, and with no `Co-Authored-By` line.

## Review Focus

The design says what each feature must do, not every condition it will meet. These five are the conditions most likely to bite a person using 7b-1; each has its tests in the task that owns the code.

1. **A halt or a close that fails partway, or that a client cuts off by disconnecting, while a launch is pending.** A person expects no turn to start after the answer, and the kill switch's off still to come. (`TestACloseWhoseAdvanceFailsStartsNoTurn`, Task 5; `TestACloseWhoseOutcomeIsUnknownStartsNoTurnAfterItsAnswer`, the final review's fix wave (amended in execution, 2026-11-03: the final review's I1); `TestAKillWhoseHaltFailsStartsNoTurn` and `TestAKillWhoseRequestEndsAfterItsRowStillHaltsEveryRun`, Task 12; `TestTheKillSwitchTurnsOffThoughAnItemsBoardDoesNotReplay`, Task 13; `TestRandomSchedulesKeepTheLifecyclesInvariants` and `TestConcurrentSchedulesKeepTheLifecyclesInvariants`, Task 18, invariants 1 and 2)
2. **A lost answer, the kill switch's own included, and a retry, including a delayed one that lands after the inverse act, or an off that reveals the lost on.** A person expects each act once, its effects though its answer was lost, no older act to undo a newer one, R10's residual aside, and no work started while a kill switch write's outcome is unknown (R37). (`TestARepeatedCloseAfterAReopenAnswersAsTheFirstAndClosesNothing`, Task 8; `TestAKillSwitchRowWhoseCommitAnswerIsLostStillStopsEveryRun`, `TestAnOffThatRevealsALostOnHaltsEveryRunThatOwesIt`, `TestAStaleKillSwitchOffAfterANewerOnIsRefused`, `TestNoTurnStartsWhileAKillSwitchWritesOutcomeIsUnknown` and `TestAFailedKillSwitchWriteThatDidNotCommitResumesStaffingOnceARereadSettlesIt`, Task 14; `TestRandomSchedulesKeepTheLifecyclesInvariants`, Task 18, invariants 1 and 6)
3. **Timers that race people, and clocks that step:** an auto-close armed before a reopen; a close at the instant a round decided its stop; the kill switch turned off while a halt it owes failed; a clock stepped back under an armed auto-close. A person expects the later act to win, nothing the kill switch stopped to run again, and no item closed by itself before its deadline. (`TestACloseAfterAStopWasDecidedKeepsTheDecidedReasonAndClosesTheItem`, Task 5; `TestAnAutoCloseTimerThatFiresBeforeItsDeadlineArmsAgain` and `TestTheRecordsRefuseAnAutomaticCloseStampedBeforeItsDue`, Task 6; `TestAnAutoCloseArmedBeforeAReopenClosesNothing`, Task 8; `TestARunThatOwesAKillIsHaltedAfterTheKillSwitchIsOff`, Task 13; `TestRandomSchedulesKeepTheLifecyclesInvariants`, Task 18, invariants 2 and 8)
4. **A kill switch held longer than the auto-close delay.** A person expects the off to list every item it left unstaffed, none of them closed by itself. (`TestTheKillSwitchHeldPastTheAutoCloseDelayListsEveryItemItLeftUnstaffed` and `TestNoItemClosesByItselfWhileTheKillSwitchIsOn`, Task 13; `TestRandomSchedulesKeepTheLifecyclesInvariants`, Task 18, invariant 8)
5. **The queue when its head leaves without investigating, or its start fails, through a takeover and a gap in the configuration, with promotions at the same time.** A person expects the next item to start while a slot is free, an item whose start failed to start on its retry, never more than the limit and one investigating, and the same order and count from a new leader. (`TestAQueueHeadLeftNotStaffedLetsTheNextItemStart`, `TestAnAdmittedItemWhoseStartFailedOnceStartsOnItsRetry`, `TestATakeoverKeepsTheQueuesOrder` and `TestTheQueuesOrderIsTheOpeningsTimesWithinAHoldingAndAfterATakeover`, Task 9; `TestAnAdmittedItemCountsTowardTheLimitAndOne`, Task 10; `TestAPromotionWhoseOutcomeIsUnknownCountsTowardTheLimit`, the final review's fix wave (amended in execution, 2026-11-03: the final review's I2); `TestRandomSchedulesKeepTheLifecyclesInvariants` and `TestConcurrentSchedulesKeepTheLifecyclesInvariants`, Task 18, invariants 3, 4 and 9)

## Decisions

The design's Decisions 1 to 15, as the three reviews and the author's answers left them, with their numbering, so that the reviews' references stay valid. Values in brackets are defaults, and every new one goes into spec Section 15 (Task 22). The carries they discharge are numbered as the design's Section 1.3 numbers them.

### Decision 1. A pure move first: the turn's lifecycle to `turns.go`, the round's to `rounds.go` (carry 16)

Plan 7d-2's final review asked the next plan that touches the coordinator, one file of 1,483 lines, to move the turn's lifecycle out of `coordinator.go` by a pure move. It is the first task, so that every later diff lands in a file that holds one concern.
- **`internal/work/app/turns.go`** takes, unchanged: the types `running`, with its methods `tooLate` and `agents`, and `ended`, and the constant `lateShare`; `Exited`, `Launched`, `reported`, `whenAllEnded`, `waits` and `deadline`; `begin`, `started`, `unstarted`, `launchFailed` and `startAgain`; `turn`, `roundsLeft` and `prompt`.
- **`internal/work/app/rounds.go`** takes, unchanged: `open`, `hold`, `missed`, `end`, `settle`, `endRound` and `noticeCode` (the designer's call: the review asked for the turns only; cost if wrong, one file more than asked for).
- **`coordinator.go` keeps** the rest, about 870 lines: the coordinator's type, options and ports; `Open`, `TakeOver`, the fence, `advance` and `step`; `start`, `create`, `unstaffed`, `close` and `forget`; `load`, `record`, `arm` and `cancel`; the openings' checks.
- **Proof that the move is pure.** A throwaway program, kept out of the repository, prints every top-level declaration of the package's non-test files, by name, with its source text; its output before and after the move is the same, byte for byte. Only import lists change. No test file changes in the move's commit; every test passes; lint stays at `0 issues.`. The move is its own commit.
- **One rename, in a commit of its own,** after the move and before any change of behaviour: the step `close`, which finishes a stopped run, becomes `finish`, and the fold's own `close` (`internal/work/item/state.go`) becomes `awaitClose`. 7b-1 adds a public `Close`, and two private ones would invite the wrong call.

### Decision 2. Four states, and 7b-1's records

The item's lifecycle (spec Section 4.1) gains its fourth state: `queued --> investigating --> awaiting_close --> closed`, with these transitions:

| From | To | By |
|---|---|---|
| — | queued | an opening |
| queued | investigating | admission or a promotion, which starts the item |
| queued | awaiting_close | a start not staffed (the kill switch, the daily cap, its routing, its type), or the kill switch |
| investigating | awaiting_close | its run's stop, through `finish`; or not staffed while it has no run (the kill switch) |
| awaiting_close | queued or investigating | a promotion of an item with no run (Decision 4) |
| queued, investigating or awaiting_close | closed | a person's close (Decision 6) |
| awaiting_close | closed | auto-close (Decision 8) |
| closed | awaiting_close | a reopen within [24 h] (Decision 7) |

- **The states,** as `State.Status` folds them: `closed`, while the item's latest close has no reopen after it (`StatusClosed`, new); `awaiting_close`, while it awaits its close; `investigating`, once it started; `queued`, before that. An item **has no run** until its run is created: while it is queued, once it started and before its run's creation, and when it awaits its close not staffed without one.
- **7b-1's records** are Contract C19's: `item_promoted`, `item_closed` and `item_reopened`; `killSwitchSeen` on `item_opened`, `item_started` and `run_created`, the latest kill switch row the coordinator knew when it recorded them; `run_stopped.killed`, the on-row that stopped a run; `item_awaiting_close.autoClose`, `.cause` and `.unstaffedBy`; the stop reasons `killed` and `closed`, and the round's end `stopped`.
- **Times.** The fold keeps, from each committed record's time (`Record.At`), when the item opened, when it last entered `awaiting_close` (a reopen included), and when it last closed. A record that `record` checks before its commit has no time yet, and no rule of order reads one.
- **What each state admits,** besides what Plan 7a's rules already admit. This replaces `admits`' rule that an item awaiting its close takes nothing:

| State | Admits |
|---|---|
| queued | `item_promoted`; `item_started`; `item_awaiting_close` not staffed; `item_closed` |
| investigating | a `round_ended` that ended `stopped`, then `run_stopped` `killed` or `closed`, after a round that did not decide a stop, or before the run's first round (Decision 3); `item_awaiting_close` once the run stopped, or not staffed while the item has no run (Decision 5); `item_closed` while no run runs, which a halt's stop in the same append makes so (Decision 6) |
| awaiting_close | `item_closed`, a person's or an automatic one; `item_promoted` while the item has no run (Decision 4) |
| closed | `item_reopened` |

- **Windows in time** ([24 h] to reopen, [72 h] for late rulings) are judged by the use cases, against the database's time (`Clock.Now`), before they record. The fold cannot judge them: a record it checks before the commit has no time.
- **A record's incident** is the item's incident after the record, as the fold of the history and of the drafts before it gives it, inside the append; an opening's is the new incident the opening gives. No record of 7b-1 changes an item's incident; 7b-2's link record relies on the rule.
- **Bodies stay at version 1** (the designer's call, resolving carries 9 and 11). Migration 0010 (Decision 5) declares no older reader, so it raises the schema's `min_reader` to 10: every build of level 9 or below refuses to start on the schema, and one that runs stops at its next renewal (Plan 7d-1, Decision 18). No build that cannot read 7b-1's records can meet one. 0010 cannot declare an older reader anyway: it adds a table with triggers. Plan 7d-1's R12 keeps bodies at version 1 until the first release, and a version 2 would break scenario 11(a), whose fixture stands for a later build's record at version 2. **What it costs a rollback:** once 0010 is applied, no build before 7b-1 runs on the database; a 7b-1 that misbehaves is fixed forward, or the database is restored from a backup, losing what was written since (Plan 7d-1's "a rollback must be rolled forward"; nothing until a release exists).
- **`record` returns the item's state after its commit,** folded from the history and the records committed with their times; the coordinator's account of its items follows it (Decision 4), and every load besides.

### Decision 3. A run stopped mid-round: halt, then finish (carries 1, 5, 14 and 19)

- **`item.Run.Halt(reason, e)`**, pure, returns the records that stop a run now:
  - **when the run's latest round decided its stop**, and the stop is not recorded yet: that decided stop (`Run.Stop`), whatever `reason` asked, since the round's own decision is what measurement reads (the designer's call; cost if wrong, a close at that instant records `quiet` rather than `closed`, and still closes the item);
  - **when a round is open:** a `round_ended` with `EndedBy: stopped` and the decision to stop for `reason`, then `run_stopped` for `reason`. The stopped round records what the instance knew at the stop, and reads nothing more: the turn classes `e` gives (an agent without one `interrupted`), and the turn ends `e` gives; no failed call, and `Settled` false; it is never quiet, concludes nothing, converges nothing and removes no agent. A normal round counts toward the rounds and the wall clock, to the stop; a wrap-up round uses up a wrap-up and counts toward the wrap-up rounds opened, as a wrap-up round no agent took part in does (the designer's call; cost if wrong, a run resumed in 7b-2 gets one wrap-up round fewer);
  - **when no round is open:** `run_stopped` for `reason`.

  `run_stopped`'s rounds and wall clock are the latest ended round's, the stopped one included; its spend is the run's at the halt (`e.Spent`, R11), with `Settled` false; its conclusion is the board's assessment at the halt (`e.Report`); for `killed`, `killed` names the kill switch's on-row (`e.KilledBy`).
- **The fold admits** a `killed` or `closed` stop after a round that did not decide a stop, before the run's first round, and after a round that ended `stopped` with that decision; and a round that ended `stopped` only with a decision to stop `killed` or `closed`.
- **What the halt reads.** The turn classes come from a snapshot of the run the coordinator held, which the drop takes, or Close takes before its append; the kill switch's on drops every run at its step 3 and keeps their snapshots for the halts of its step 5; a drop that finds no entry takes none. A snapshot lives until the halt that reads it commits, a failed halt reading it again on its retry, and the fence clears them all (R45). A halt with no snapshot, as the step's after a takeover or after a failed attempt, classes the round's agents from the board: `ended` for those with a turn end on it, `interrupted` for the rest. The conclusion is the board's assessment. **A board that cannot be read does not stop the halt:** its conclusion is then empty, and its classes the snapshot's, or `interrupted` (the designer's call; cost if wrong, a run whose board could not be read stops with no conclusion).
- **Context.** Every halt runs under the coordinator's own context (`c.ctx`), which only the holding ends: a client that disconnects cannot cancel a halt.
- **Every halt drops the run's state in memory** (the controller's call). `drop(run)` does for one run what the fence does for every run, and closes its upstream sessions besides, which the fence does not: it snapshots the run's turn classes, cancels the run's timers (`startAgain`'s, the deadline's and `settle`'s included), and deletes its entry in `c.runs`; then it ends the run's sessions (`Sessions.EndRun`), stops the turns of its latest round (`Turns.Stop`), and closes its upstream sessions (`UpstreamSessions.CloseRun`). Each part repeats safely, and none waits (amended in execution, 2026-11-03: the fence closes no upstream session, a drain closing the whole pool, so "what the fence does" was wrong about `CloseRun`; Decision 6 says what a close that ends no longer held drops). With the entry gone, `Launched`, `Exited`, `startAgain`, `deadline` and `settle` find no run: a launch that a stop cancelled reports `cancelled` to nothing, and nothing arms its retry.
  - **The step's halt for `killed` drops before it records:** the kill is owed whatever its record does, and the step tries a failed halt again (Decision 5).
  - **Close drops once its record has committed** (Decision 6). It takes its snapshot first, without dropping: until the commit the close may not have happened, and a close whose record did not land leaves the run going.
  - **The kill switch's on drops every run** at its step 3, and the fence forgets every run, as today, once it has stopped every turn and ended every session, closing no upstream session (amended in execution, 2026-11-03: "forgotten", not "dropped", since a drop closes upstream sessions and the fence does not). A run that stopped as its round decided needs no drop: its round has ended in memory, and `Launched` and `startAgain` already ignore a round that ended.
- **`begin`'s guard, the second line.** `begin` starts no turn while the kill switch is on, nor for a run that owes the latest on-row: `running` keeps the run's `killSwitchSeen`, which `hold` copies from the run's records. It leaves the turn among the round's starting turns, arms no retry, and posts no notice. While a kill switch write's outcome is unknown, `begin` starts no turn either, but arms the turn's retry, since the outcome may be that nothing changed (R37). That covers every kill, whatever the switch says now, but not a close, whose run owes nothing; and a guard at `begin` acts too late, since `unstarted` has already armed its retry, and the round's deadline and settling stay armed. The drop covers every halt.
- **Where a halt is recorded:** the kill switch's, by the step, once it has dropped the run, as one append of `Halt`'s records, which `finish` follows; a person's close, as one append of `Halt`'s records and `item_closed`, after which Close drops the run, and the closed step follows (Decision 6).
- **`finish`** (the step `close`, renamed) acts on a stopped run whose item is not closed, each effect repeatable:
  1. `Sessions.EndRun`, `Turns.Stop` for each agent of the latest round, and `UpstreamSessions.CloseRun`. Under Docker a pending launch is cancelled and reports `Launched` with `cancelled`, which the coordinator ignores: a halted run was dropped, and a decided stop's round has ended (carry 14);
  2. for `killed`, the board's `kill_switch` notice; `board.ErrStopped` from that post counts as done (carry 5);
  3. the board's stop entry, for the run's reason;
  4. the item's final record: `item_awaiting_close`, with the item's reason (`ItemReason`, which reads `killed` as `not_staffed`, as it reads `cut`, and leaves `closed` as it is), its auto-close delay (Decision 8), and, for `killed`, `unstaffedBy`.

  The item stays active until that record, so a crash before it is carried on.
- **A run's upstream sessions** (carry 19, the sweep's row 19). The coordinator declares a port, `UpstreamSessions { CloseRun(run board.RunID) }`, which the MCP gateway's pool implements: under its own lock it lets go of every pooled session of that run, of every agent and server; then, its lock released, it closes each in the background, as `Pool.Close` does at a drain. A session is closed only while its holding is valid, and dropped unclosed otherwise, as today: one `DELETE` per pooled session, which the pool asks the holding before, as it does before every request (carry 13). `CloseRun` never waits, since the coordinator calls it under its lock. `finish` and the closed step call it after `Sessions.EndRun`, so every stop closes them: a decided stop, a halt, a close and the kill switch. A run halted or closed just as the holding ends is the residual, which spec Section 8.2 states: it can keep its upstream sessions until the pool sweeps its idle ones, or, at a crash, until the MCP server expires them (amended in execution, 2026-11-03: the fence closes no upstream session, Decision 3's drop above).

### Decision 4. The queue and promotion

Spec Sections 4.1 and 5.1: "If [3] items are already investigating, the new item waits in `queued`"; "A responder may promote a queued item, which starts it even if that exceeds the limit by one"; an item locks its configuration when it starts.
- **The limit, L,** is the deployment's `lifecycle.investigating` [3] (Decision 15).
- **The items investigating, I,** count every item investigating, and every queued item that is admitted or promoted and has not started yet. An item is investigating from its start until it awaits its close, its run's stop recorded or not.
- **The order:** first opened, first admitted, by the opening record's time, then by the item's id (the designer's call; R9). A work record's `seq` counts within its item, so it cannot order items. Within a holding, openings commit in order under the coordinator's lock. The account keeps that order, its rank, within a holding as after a rebuild (R40).
- **The coordinator's account of its items.** It keeps in memory, under its lock, each item that is not closed: its state, when it opened, whether it was promoted, whether the walk admitted it, and when it last entered `awaiting_close`, with what its `item_awaiting_close` says.
  - Every takeover rebuilds it from the records, without admission marks (Decision 12).
  - Every load refreshes it: each load in `advance` and `step`, each record committed, and each key lookup (Decision 13). A refresh keeps an item's admission mark while the item stays queued, and drops it once the item starts or leaves the queue.
  - **Items whose records cannot be read are not in it:** those held by a later build, and those whose records do not fold (`item.ErrInvalid`). The walk never reaches them, so such an item holds up no other, as spec Section 8.2 requires; its steps fail and are tried again, as today, and the first load that succeeds puts it in the account. An item whose load merely fails keeps its entry, which its retry refreshes.
  - **An item whose promotion's outcome is unknown,** its append failed and the lookup after it failed too (Decision 13), counts as promoted, and waits in no queue, until its next load or record refreshes its entry, its retry loading it a retry wait later: the promotion may have landed, and an account that held the item as it was, waiting or awaiting its close, neither of which counts, would let a second promotion, or the walk, take I past L + 1 meanwhile (amended in execution, 2026-11-03: the final review's I2).
- **Admission is derived, not recorded, and the walk admits:**
  - **The walk** takes the queued items in their order, passes those admitted or promoted already, and, while I < L and no kill switch write's outcome is unknown (R37), admits the next one: it marks the item, which counts it in I, and advances it once. An admitted item that starts is investigating, and stays counted. One that its start leaves not staffed, or that its load finds closed, loses its mark, which frees its count, and the walk goes on. One whose start fails, as when the day's spend cannot be read or no revision is current, keeps its mark, its count and its place, and the walk goes on; the item starts on its own retry, or on any later advance.
  - **When the walk runs:** at the end of every advance that is part of neither a walk nor a takeover, and once at the end of every takeover, after it has advanced every item. Every change of the queue, or of the items investigating, passes through an advance, or through a walk armed at once on the timers when a read of an item outside an advance or a walk finds that it no longer counts toward the limit, as a preview's read after a lost answer may: one such walk is armed at most, a walk that runs takes its place, and the fence cancels it (amended in execution, 2026-11-03: the interleaving test found such a read freeing a slot that nothing filled, d1f4f2c). A walk inside the act would let a preview start items, against Decision 13. Such a walk is armed too when a counted item leaves the account outside an advance or a walk, by either of two paths: such a read that finds the item's records unreadable, which do not fold or are of a later version (`unreadable`, then `leave`), and the round's settling timer that meets a record or an event of a later version (`skip`, then `leave`). Both need such records mid-holding (amended in execution, 2026-11-03: the final review's M2; both armed none, a gap carried to that review). The walk never runs inside itself: the advances it makes are part of its loop, which ends once I reaches L or the queue ends.
  - **The step starts a queued item that is admitted or promoted, on any advance,** its own retry included. A queued item with neither mark waits.
  - **The same records give the same admissions:** a takeover's walk admits as the walk before it would have, and `item_started` records the start, so an admission needs no record of its own (the designer's call).
  - **The order of starts.** Admissions follow the order. An admitted item whose start fails keeps its slot, so an item admitted after it may start first, but none takes its slot (the designer's call).
- **The rule of the limit and one,** `overLimit()`: an act that would take I past L + 1 is refused, `409` (`limit_reached`). A promotion calls it, and so will 7b-2's resume. Admissions and promotions both count until their items start, so "exceeds the limit by one" holds however many act, and whatever start fails (the designer's call).
- **Promotion** (`Coordinator.Promote`) records `item_promoted`, with who promoted the item, `killSwitchSeen`, the request and the reason if any (R13), then advances it; its start then ignores the queue. It applies to **any item with no run** (the controller's call): a queued item; and an item awaiting its close not staffed without a run, such as one opened at the daily cap or while the kill switch was on, or one that started and whose run was never created. The record lifts its `awaiting_close`: an item that never started then starts, and one that started gets its run.
  - It is refused, `409`, in R23's order: on a closed item (`closed`: reopen it first); while the kill switch is on (`kill_switch_on`); once the day's spend reached the daily cap (`daily_cap_reached`); past the limit and one (`limit_reached`). While a kill switch write's outcome is unknown, with the view off, it answers `503` instead, after the checks of a closed item and of its effect (R37).
  - A promotion of an item that has a run, and is not closed, changes nothing; so does a promotion of an investigating item with no run, whose step creates its run, and one of a queued item that is admitted or promoted already, whose start is due and counted. A promotion that changes nothing is never refused for the limit: the check of its effect comes first.
  - A promotion is allowed as often as the item has no run (R6).
- **The start** (`start`) of an admitted or promoted item records `item_awaiting_close` `not_staffed`, with a cause, instead of `item_started`, when, checked in this order: the kill switch is on, or the item owes the latest on-row (`kill_switch`, with `unstaffedBy`; Decision 5); the day's spend reached the daily cap (`daily_cap`); the item's routing fails its check under the revision it is about to lock (`routing`; carry 8); that revision cannot run its type (`type`). Otherwise it locks the configuration, as today, and `item_started` records `killSwitchSeen`. Each check lands with its task: the routing's and the type's in Task 3, the cap's in Task 9, the kill switch's in Task 12.
- **The queue at the daily cap.** A queued item whose turn comes at the cap is not staffed, as an opening at the cap is (spec Section 5.1, step 6). So when a slot frees while the day's spend is at the cap, the walk leaves every queued item it reaches not staffed, and the whole queue drains to `not_staffed` at once (the designer's call: this is the behaviour, not a side effect; cost if wrong, a person promotes such items after UTC midnight).
- **A retried opening** (`repeated`) advances a queued item as far as the queue allows, and no further: a retry never jumps the queue. Spec Section 5.1's "moves it on if it is still queued" is amended to say so.
- **The snapshot.** After each change the coordinator publishes, for readers that take no lock, as it does `Skipped`: the kill switch's view, the limit, the items investigating as the limit counts them, and the queue in order (`Coordinator.Lifecycle`). The views and `/v1/status` read it. A fenced coordinator publishes an empty one.
- **Effects:** none on running rounds; a start is the usual one. **Notices:** none; a queued item has no board. **Idempotency:** a promotion repeated with its key answers as it first did, and advances the item (Decision 13). **Takeover:** the account and the queue are rebuilt from the records. **Drain:** a promotion answers `409` draining. **Audit:** `item_promoted` and `item_started` are work records, chained. **Migration:** none.

### Decision 5. The kill switch

Spec Section 8.3: "A control that at once stops staffing new items and cancels running sessions, with a notice on each board." Turning it on shows its consequences ("stops N runs on M items"), asks for a reason, and needs no fresh authentication; only an admin turns it off. Section 5.1, step 6: while it is on, an item opens `not_staffed`.
- **Its state is a table of its own,** `kill_switch`, in migration 0010 (Contract C17): one row each time it is turned on or off, numbered from 1 (`seq`), with who, the reason's code, its redacted text and the database's time; append-only and chained like every audit record, with the scope `''`, as configuration revisions have; verification reads it as a new source. Its state is its latest row; no row means off. Migration 0010 declares no older reader (carry 12). Not work records: they belong to an item, and their scope in the chain is an incident.
- **The store** (`workpg.Controls`, behind the coordinator's `Controls` port; Contract C16):
  - `KillSwitch(ctx)` reads the switch's state, a `control.KillSwitch`: whether it is on; the latest row's number (`Seq`), 0 before the first; the latest on-row's number (`OnSeq`), 0 before the first on; the latest off-row's time (`OffAt`); and the latest row's actor, reason and time.
  - `SetKillSwitch(ctx, on, by, reason, after)` writes under the holding whose epoch `ctx` carries, guarded by the lease in its own transaction, after an advisory lock of the tenant. In that transaction it first finds whether the state holds already, and then appends nothing, whatever `after` says; otherwise it refuses with `ErrStale` when `after` is not the latest row's number; only then does it append. It returns the switch's state after it, and whether it appended a row (`changed`), whatever it did: `ErrStale` comes with the state. The guard's refusal wraps `ErrLeaseLost`, as every guarded write's does.
  - `LatestKillSwitch(ctx)` reads the latest row's number alone, 0 before the first: one read on the table's primary key. The leader's renewal check makes it, through a port of the leader's own, on the lease's pool.
  - `Records.UnstaffedBy(ctx, seq)`, a new method of the records' port, finds the items whose `item_awaiting_close` names the on-row `seq`.
- **The coordinator's view** of the kill switch, in memory: whether it is on, the latest row's number, the latest on-row's number, and the latest off-row's time. It follows the store's answer to every request, changed or not, every read of the switch, and every takeover (Decision 12).
- **A change of the view acts, whatever caused it:** the coordinator's own row, a store's answer, a read after an unknown outcome, the leader's renewal. One function, `applyKillSwitch(view)`, does it under the coordinator's lock, by two rules (a takeover sets the view itself, in the same block as it takes up its holding, and its single advance of every item acts on it, R47; amended in execution, 2026-11-02: through `applyKillSwitch`, a takeover's fenced view would read any past on-row as a raise, advance every item twice and list an off's items at every takeover):
  - **a raise of the latest on-row runs the on's steps 2 to 5 below,** whatever else changed: an off that turns on, a newer on while on, and an off whose answer reveals an on that was never applied;
  - **then the view's state decides the rest:** on runs step 6; off runs the off's steps, when the view was on before or the latest on-row was raised.

  The third case comes from a lost answer. An on whose answer is lost, and whose second read fails, leaves the view off for up to [5 s] while the table holds the on, as row 5; the view is unsettled meanwhile, so nothing starts (R37). An off sent in that window, by someone who read the table, names row 5 and lands as row 6. Its answer raises the view's latest on-row to 5 and leaves the view off: the on's steps halt every run that owes row 5, and the off's steps follow.
- **A run owes a kill** when the kill switch's latest on-row has a number above the run's `killSwitchSeen` (the latest row the coordinator knew when it created the run) and the run has not stopped. **An item with no run owes it too:** when the latest on-row has a number above the `killSwitchSeen` of the latest of its opening, its start and its promotion. The step halts a run that owes it, and leaves not staffed an item with no run that owes it, before it opens a round, starts a turn, starts an item or creates a run, whatever the switch says now (Decision 12). So a halt, or a record of not staffed, that failed is carried out later, the switch off or not, and turning it off never waits. Row numbers, not times, so that no clock decides it (the designer's call).
- **What is owed.** The on's and the off's answers and log lines carry `owed`, which names only what the rule acts on: the items whose run has not stopped and owes the kill; the items whose killed run's `finish` has not ended; and the items queued, or started with no run, that owe the latest on-row. Items awaiting their close, or closed, are not in it, though their opening's `killSwitchSeen` may be older.
- **Turning it on** (`Coordinator.SetKillSwitch(ctx, true, act)`) on the instance that leads, under the coordinator's lock:
  1. **The row.** Once the request reached the coordinator, under its lock, with the request still live, the row is written under the coordinator's own context, so that the client's leaving cannot cancel it. The store's answer updates the view and acts, changed or not, and with `ErrStale` too, which carries the state; a stale request answers `409` (`stale`). **The guard's refusal is no unknown outcome:** an error that wraps `ErrLeaseLost` is handled as `failed` handles it, which fences the coordinator and tells its leader, and the act answers `ErrNotHeld` (`not_held`). **Any other store error is an unknown outcome,** but one that comes because the coordinator's context ended, which answers `ErrNotHeld`: while the instance holds, the coordinator reads the switch again, under its lock, and applies what it finds. If that read fails too, it arms a retry of the read after [5 s], each read that fails arming the next, and the act answers `503` (`unavailable`). **From the error until a read succeeds, the view is unsettled, and the coordinator begins no work** (R37: the controller's ruling, which fails safe). If it succeeds, the act answers by what it found, against the state asked and `after`:
     - the state asked, by a row after `after`: `200`, `changed: true`, since a row written after the sender's view set it, this request's or another's to the same effect;
     - the state asked, with no row after `after`: `200`, `changed: false`, since it held already;
     - not the state asked, with no row after `after`: `503` (`unavailable`): the row did not land and nothing changed, so the request may be sent again as it was;
     - not the state asked, with a row after `after`: `409` (`stale`), since a newer row stands.
  2. From the view's change on, under the coordinator's own context: `Turns.StopAll` and `Sessions.EndAll`. Every turn stops and every session ends at once (carry 14), and `Session.Done` closes for each (the sweep's row 13); calls in flight end at both gateways, except that a vendor attempt already sent finishes and is paid for, within the daily cap.
  3. Every run dropped (Decision 3), while the coordinator still holds: a launch that `StopAll` cancelled then reports `cancelled` to a run the coordinator no longer holds, so nothing arms its retry.
  4. Every auto-close timer cancelled (Decision 8).
  5. Each active item advanced, under the coordinator's context: the step halts each run that owes the kill, with the snapshot's classes, and leaves not staffed each item with no run, queued or started, with `unstaffedBy` the on-row's number (the controller's call: queued items are not staffed at once; cost if wrong, a person promotes them after the switch is off, rather than their starting by themselves). The answer tells each item that owed the kill before that advance by its state once the advance is done: a run whose stop is recorded is named among `Stopped`, and an item that awaits its close with no run among `Unstaffed`. So a run whose creation landed with its answer lost, which the account held as an item with no run, is named among the stops, not as an item left not staffed, as invariant 1 requires (amended in execution, 2026-11-03: the interleaving test found such a run named only as its item, 7f1e83e).
  6. Publish the snapshot, and log `kill switch set` (Contract C3).
- **The leader's renewal check** (the controller's call). After each renewal that succeeded, the leader reads `LatestKillSwitch` on the lease's pool, one indexed read, and compares it with the coordinator's view, which `Coordinator.KillSwitchSeen()` gives without a lock. On a newer row it has the coordinator read the switch again and apply it (`RefreshKillSwitch`). **The refresh runs on a timer of its own:** set at now on the leader's timers, outside the count of the leader's work in flight that `Done` waits for, never under the leader's lock, and at most one pending, so that a renewal that finds one pending sets none (R25). The coordinator acts under its own context, and a fenced coordinator does nothing. The refresh reads the table again under the coordinator's lock rather than applying the number the renewal read, so a renewal that read late never moves the view back.
  - **Where it runs.** The read comes last in the renewal, after the clock's sample and the schema check, within `RenewEvery` and under the leader's context, as they run, and only when the schema check passed, or failed only to read: a check that found `ErrSchema` stops the leader instead. A read that fails is logged and changes nothing: the renewal stands.
  - **A draining leader makes no such read:** its coordinator is fenced, and the next leader reads the switch at its takeover. The leader checks that it is not draining just before the read, so a drain's stop waits at most for a renewal's sample and check, as today, or for one read already in flight; `worstDrain` stays as it is (R26).
  - It heals any divergence of the view from the table within about one renewal period [5 s], a row inserted by hand included; within two at most. It adds no on from a standby: a standby still refuses one (carried to Plan 9a).
- **During a drain** (the author's decision; the controller's constraints on its context):
  - **The drain's first moment.** `Leader.Drain` ends the holding's context, from which `c.ctx` derives, before it publishes that it drains and fences the coordinator. **Every act, the kill switch's included, answers `ErrNotHeld` once the coordinator is not held or its context has ended** (Decision 13).
  - **The admin API** calls `SetKillSwitch` first. On `ErrNotHeld` it reads the leader's status: if the leader holds the lease and drains, it calls `Coordinator.RecordKillSwitchOn(ctx, epoch, act)` with the draining holding's epoch; if the leader holds and does not drain yet, it answers `503` with `Retry-After: 1`, and the retry finds the drain; otherwise it answers `409` (`not_held`). A preview answered `ErrNotHeld` never reaches `RecordKillSwitchOn`, since a fenced coordinator holds no account to preview from: it answers by the leader's status, `409` `draining` while the leader holds and drains, `503` `taking_over` with `Retry-After: 1` while it holds and does not drain yet, and `409` `not_held` otherwise (amended in execution, 2026-11-03: Task 19's ruling; the next leader's takeover carries the on, which the client can still send).
  - **`RecordKillSwitchOn` takes the coordinator's lock only to make sure the coordinator is fenced.** A coordinator still held at that point has had its context ended, and the drain is about to fence it: `RecordKillSwitchOn` fences it first, as the fence would, every turn stopped, every session ended and every run forgotten, so that no step can start a turn once the on-row commits (amended in execution, 2026-11-03: "forgotten", as Decision 3 now names what the fence does to a run). It then releases the lock before it writes, so that the drain's fence, which waits for that lock under the leader's own, never waits on the write (R24 for a coordinator held with a live context).
  - **Its write's context is its own:** the request's values without its cancellation (`context.WithoutCancel`), the draining holding's epoch (`holding.WithEpoch`), and a deadline of the database's transaction timeout [10 s]. The drain's ending of the holding's context does not end the write, nor does a client that leaves; the timeout bounds it; and the store's guard still checks the epoch against the lease in the write's own transaction, so a holding that ended refuses it (`not_held`).
  - **It acts on no run.** It answers `changed` as the store says, `deferred: true`, and no `owed`: the fenced coordinator holds no account. A lost answer needs no second read: it answers `503` (`unavailable`); the next leader's takeover reads the table, and the client reads the switch on the next leader before it sends the on again.
  - The next leader's takeover halts every run that owes the kill before that run's next round.
  - Off during a drain is refused (`draining`). On or off on a standby is refused (`not_held`).
- **While it is on:** an opening records `item_awaiting_close` `not_staffed`, cause `kill_switch`, with `unstaffedBy`, beside `item_opened`, as at the daily cap; a start is not staffed; a promotion is refused (`kill_switch_on`); close, reopen and rulings go on; auto-close pauses (the controller's call; Decision 8).
- **Turning it off** (`SetKillSwitch(ctx, false, act)`) on the instance that leads: it is refused without `act.Actor.Admin` (`admin_only`), and `stale` as above; off while off changes nothing; its row is written, and its answer and errors handled, as the on's are. The view's change publishes the snapshot, and arms the auto-close timers again, each counting from the off (Decision 8). It never waits, and answers:
  - `stayedUnstaffed`: every item the latest on left unstaffed (the controller's call), found by `Records.UnstaffedBy(ctx, onSeq)` over the `item_awaiting_close` records that name that on-row, each with its state now: still awaiting its close, or closed by a person since. Auto-close was paused, so none closed by itself. The list is computed whenever this answer's own off steps ran, that is, whenever this answer turned the view off, which can be a `changed: false` answer, as an off that reveals a lost on; it is not computed, and empty, only when the view was already off and no on-row was raised, as on a retry. A list that could not be read in full says so, `StayedUnstaffedUnread`, logged at Error: `UnstaffedBy` failed, and no item is given; or an item's load failed, and that item keeps its id with an empty state. The off has happened all the same (amended in execution, 2026-11-02: a list it could not read, answered as none, would hide the operator's only signal of what to promote, Review Focus 4). When a read settles a view whose write's outcome was unknown, the list is read after the advance that settling makes (R37), so it holds the items that advance left not staffed, with their states now (amended in execution, 2026-11-03: such an item had been in none of the off's lists, or among its stops, 6351cd7);
  - `owed`, as defined above, so that an operator sees what is still to stop.

  Nothing restarts by itself: a person promotes the items without a run.
- **A request's form:** `after` is the latest row's number its sender saw, 0 before the first row, and is required (R8); the on needs a reason; the off takes one if given. Both answers carry `owed`, but a deferred on's.
- **The board's notice** `kill_switch` (Contract C18) names nothing, and the board's rule for repeats keeps it to one per round.
- **Who:** the admin API's token holder now, as an admin; Plan 9a's control later (responders and approvers turn it on, admins turn it off with fresh authentication).
- **Idempotency:** by its state, and by the latest row its sender saw. A request whose effect holds answers `changed: false`, and still brings the view up to the table; a request whose `after` is not the latest row's number is refused `stale`, so that a delayed off cannot undo a newer on. It takes no key.
- **Without a configuration,** the kill switch works with no current revision: its reason's free text is then refused, `400`, and its code alone taken, since the rules that redact text are the configuration's (the designer's call).

### Decision 6. Close, from any state

Spec Section 4.1: "A human may close an item from any state"; "Closing stops a visible primary run."
- **`Coordinator.Close(ctx, id, act)`** is total:
  - **queued, awaiting its close, or investigating with no run running** (no run yet, or a run whose stop is recorded while its `finish` has not ended): one append, `item_closed`, with who, why and the request;
  - **investigating with a run that has not stopped:** one append of `Halt`'s records for `closed` and `item_closed`, with the turn classes of a snapshot Close takes first. Once the append commits, Close drops the run (Decision 3), then advances the item;
  - **closed:** changes nothing.
- **An append whose answer is lost** (Decision 13). An append that fails is not taken as nothing done: while the instance holds, Close loads the item. If its `item_closed` landed, Close answers as a repeat and carries on: the item being closed now, it drops the run and advances the item, whose step runs the closed step. If it did not land, nothing changed, while the coordinator still holds: the run goes on with its timers, and Close answers `503` (`unavailable`) (amended in execution, 2026-11-03: "nothing changed" holds only while the coordinator still holds, since a close that ends no longer held drops its run, as the next bullet says). If the load fails too, the close's outcome is unknown, and the close may have landed: Close drops the run before it answers, as the kill's halt drops before it records, so that no turn of a closed item starts after the answer (Review Focus 1); then it arms a retry of the load and the carrying-on after [5 s], and answers `503`. If such a close did not land, that retry carries the run on, as a takeover carries a run this instance does not hold: the item still investigates, and its step holds the run's round again, with fresh sessions and turns. The snapshot the drop kept keeps the round from reading as missed, for a halt that follows, as the close sent again, to read (R45); so a round whose deadline passed meanwhile is held all the same, and its deadline, past, ends it at once, unseen, the gateways refusing every call of the turns it started (amended in execution, 2026-11-03: the final review's I1: the drop came only with that retry, so the run's own timers could start a turn of the closed item, and a pending launch complete, after the close answered).
- **A close that ends with the coordinator no longer holding,** whose append the guard did not refuse, drops the run, whatever came of the close: as when a drain's first moment ends the coordinator's context as the append commits, as the check reads the item, or as the advance runs. Such a close may have landed with nothing left to carry it on, since no takeover carries a closed item on, so no closed step would ever drop its run. A drop repeats safely. That of a run whose close did not land ends only what this instance's fence ends, and closes upstream sessions that its ended holding can no longer use; the run goes on under the next leader, and the act answers `ErrNotHeld` (Decision 13) (amended in execution, 2026-11-03: the interleaving test found a closed run whose upstream sessions were never closed, ec926ad and 8155f65).
- **The closed step,** which the advance after a close runs: when the item has a run, it drops the run (Decision 3), which ends its sessions, stops its turns, closes its upstream sessions and forgets its state in memory, all repeatable. Then, when the run's board lacks its stop entry, it posts, once and best effort, the `kill_switch` notice of a killed run and the stop entry, and logs a failure rather than trying again for ever (the designer's call; cost if wrong, a closed item whose board refused its stop entry keeps a board without one; its sessions, turns and upstream sessions end regardless). Plan 9b's human posts check the item's state, not only the board's stop.
- **No state of an item refuses a close.** An item whose `finish` cannot end, as on a board that does not replay, is closed by a person, and its board effects are the closed step's best effort.
- **Until Plan 8a,** every run is visible and primary, so a close stops the item's run.
- **Effects:** Decision 3's, for a run that has not stopped; the closed step's otherwise. A close of an item that was investigating or queued moves the queue: the walk runs. **Notices:** none of its own. **Who:** the admin API now. A reason is optional. **Idempotency:** by its key; a close of a closed item changes nothing (Decision 13). **Takeover:** nothing to carry on once the append committed. **Drain:** `409` draining. **Audit:** `round_ended`, `run_stopped` and `item_closed` are work records; the stop entry is a board event.

### Decision 7. Reopen

Spec Section 4.1: a human "may reopen a closed item within [24 h]; it returns to `awaiting_close`".
- **`Coordinator.Reopen(ctx, id, act)`** records `item_reopened`, which carries the `awaiting_close` the item returns to: its reason, its run's latest stop by `ItemReason` (`closed` included), or `not_staffed` for an item with no run, with the cause R12 gives; and its auto-close delay (Decision 8). With no revision readable, the delay is recorded as zero and read when the timer is armed, so that a reopen never answers `no_revision`.
- **The window** is `lifecycle.reopenWindow` [24 h], from the time of the item's latest close, judged by the database's time (`Clock.Now`). Past it, the reopen is refused, `409` (`reopen_window_passed`).
- **After it,** the item awaits its close: its auto-close counts again from the reopen, and a person may close or promote it. A started item without a run, closed and reopened, is staffed by a promotion.
- **An append whose answer is lost:** as Close's. If its `item_reopened` landed, Reopen answers as a repeat and advances the item, which arms its auto-close; if the load fails too, it arms a retry after [5 s].
- **Effects:** none on the run, which stopped; no board entry. **Idempotency:** by its key, a repeat advancing the item; a reopen of an item that is not closed changes nothing. **Takeover:** the item is active again, so a takeover arms its auto-close. **Drain:** `409`.

### Decision 8. Auto-close, and its pause

Spec Section 4.1: "An item in `awaiting_close` also closes automatically [30 min] after the later of its trigger's resolution and its most recent entry into `awaiting_close`." Plan 7a: timers from the records.
- **The delay** is the work item type's `autoClose.after` [30 min], from the revision the item locked, or from the revision current when it never started. It is recorded in each `item_awaiting_close` and `item_reopened` (`autoClose`), so the timer is a function of the records alone (the designer's call; cost if wrong, a change of the type reaches only the items that enter `awaiting_close` after it). A record written before 7b-1, or with a zero delay, reads the type's setting the same way when its timer is armed. **A delay that cannot be read,** as when the item's locked commit is missing from the repository, or no revision is current for an item that never started: the timer's arming is tried again after [5 s], and the item view leaves `closesAt` out. An item that never started, whose type the current revision no longer holds, takes the default of spec Section 15, [30 min], instead of trying for ever (R43).
- **An item opened by a person has no trigger** (spec Section 5.1). Its trigger is read as resolved when it opened (the author's decision, a spec amendment), so it closes the delay after its latest entry into `awaiting_close`. Plan 7c-1 gives an item with a trigger its trigger's resolution (`State.ResolvedAt`, R19).
- **The pause** (the controller's call). While the kill switch is on, no item closes by itself, and turning it off starts every delay again. So the deadline is the latest of the trigger's resolution, the item's latest entry into `awaiting_close`, and the kill switch's latest turning off, plus the delay.
- **The timer.** The coordinator keeps one per item awaiting its close, at its deadline, in the database's time; a new arming cancels the earlier one. It arms one when the item enters `awaiting_close` or is reopened, at each takeover for every such item, and for every such item when the kill switch turns off; it arms none while the switch is on, and cancels them all when it turns on. It fires under the lock, loads the item, and records `item_closed` (`auto: true`, with its due time beside the record's own time; R7) only if the item still awaits its close, the deadline passed, and the switch is off, with no write's outcome unknown (R37): **a timer that a promotion, a close or a reopen made stale records nothing.** A record that fails is tried again after [5 s]. **Judged on the database's time** (R41): a timer that fires before its deadline on the coordinator's clock records nothing and arms itself again at the deadline, but no sooner than a retry wait [5 s] from now (amended in execution, 2026-11-03: a clock that holds its reading would fire it again and again, fe231d4); and the records store refuses an automatic close that it would stamp before its `due` (`item.ErrNotDue`), a record that fails like any other. The first takeover after 7b-1 finds every item ever left awaiting its close: it arms their timers, which fire after the takeover, one at a time under the lock, and closes none inside it.
- **Effects and notices:** none. **Who:** the coordinator. **Idempotency:** the fold's rule, an item closes once until it is reopened. **Drain:** the fence cancels the timers, and the instance that leads next arms them again.

### Decision 9. A person's text, and where rulings act

- **The rulings that act on the item are its records, never board entries** (the author's decision): close and reopen in 7b-1; extend, link and unlink in 7b-2. The item, not the board, is where the coordinator decides them. The board shows their effects: a stop entry for `closed`.
- **The board rulings' use case is Plan 9b's** (the author's decision): reject, un-reject and dismiss, with their preview and route. 7b-1 keeps what that use case needs:
  - **`PersonText(red, field, text)`**, exported, the person's twin of the MCP gateway's `agentText` (carry 6): a hidden character (`config.Hidden`) is refused; an escape that may hide what a rule that applies always removes is refused, in the text alone and in its member, as the snapshot and the record hold it (`text=<text>`); otherwise the text is redacted with those rules (R1). A refusal wraps `ErrInvalid`, says why, and never quotes the text. The rules are those of the item's locked revision, or else of the current one; with neither, free text is refused, `400`, and a code alone is taken. Every reason 7b's acts take passes it.
  - **The gate `item.State.RulingsOpen(kind, now, window)`:** reject, un-reject and dismiss are open while the item is not closed (reopen it first); adoption, feedback and milestone rulings, which Plans 8b and 9b add, are open for `lifecycle.closedRulings` [72 h] after the close (spec Section 4.2; R30). A stopped board takes rulings only (Plan 7a's rule holds).
  - **`Boards.Rule`'s doc** says that its caller checks the reason with `PersonText` and the item's state with `RulingsOpen`. Plan 9b's use case is that caller.
- **A reason's form:** its code is `^[a-z][a-z_]{0,31}$`, its text at most 500 characters (`board.MaxReasonRunes`). The lists of preset codes are Plans 9a's and 9b's, and 7b takes any code of that form (the designer's call).
- **Which acts need a reason** (spec Sections 4.2 and 8.3): the kill switch's on needs one; its off, close, reopen and promote take one if given.

### Decision 10. The notice of a yank to items already started (carry 10)

Spec Section 3.2: a yanked skill version is not invited to items that start afterwards, and "items already started keep their locked version, and each gets a notice".
- **When:** at each round's start (`hold`), for each agent of the round that pins, in the item's locked revision, a skill version that the current revision yanks (`Revision.Yanked`).
- **What:** the board's `skill_yanked` notice (Contract C18), naming the agent and the skill version, once per run, agent and version, whatever the round.
- **A post that fails** is logged as a failed step, and `hold` goes on: the round's turns start all the same.
- **Items whose run stopped** get none, since their board takes no notice (the designer's call). **Delay:** up to one round after the yank reaches the current revision; none while no revision is current.

### Decision 11. The carried fixes: openings, routing, key values and titles

- **A lost commit answer on an opening** (carry 7; Plan 7a's final review, probe G). From 7b-1, after any failed append of an opening, while the instance holds the lease, `Open` loads the item by its id: if its opening landed, it answers as a repeated opening does, for an opening by key, or returns the id, for one without a key, and advances the item; if the load fails too, it arms a retry of the load and the advance after [5 s], and returns the error.
- **The step's guard:** `step` on a state with no opening waits and does nothing, where today `start` would dereference the missing opening.
- **Routing at the start** (carry 8): `start` checks the item's routing with `checkRouting`, under the revision it is about to lock, and records `not_staffed` with the cause `routing` when the check fails. The queue makes the gap between an opening and its start minutes, not milliseconds.
- **Characters a reader could miss** (carry 20; the sweep's row 24, its inline half). `internal/registry/config` gains `Unseen(r rune) bool`, beside `Hidden`: a control character, below U+0020 or U+007F, or one that `Hidden` reports. It is the write path's rule for values a reader must see whole, and the write path's own `unseen` (`internal/actions/app/write.go`) becomes a call to it. Unlike `Hidden` alone, it refuses tab, line feed and carriage return; like it, it lets every space separator through.
  - **Key values listed inline:** the registry's `checkValueList` refuses a hypothesis-key value that holds a character `Unseen` reports, beside the empty, repeated and `other` values it refuses today.
  - **Routing** (the designer's call): `checkRouting` refuses a routing key or value that holds such a character: a value such as `checkout\nservice=payments` would show on the opening's claim as a second line that reads as another routing member.
  - **The title** (the designer's call): `title` replaces whole a title that holds a character `Unseen` reports, with `[a title with characters a reader could miss]`, as it already replaces one whose escapes may hide what a rule matches. Replaced, not refused, so that an opening never fails for its title. The check comes after `strings.TrimSpace`, so a title whose only such characters lead or trail it is trimmed, not replaced (R44).
  - **What a stricter rule costs.** A configuration that holds a value the new rule refuses makes its revision invalid, and an item locked at such a commit before the upgrade cannot rebuild its revision (the sweep's row 6, carried to Plan 10a). Nothing in the repository holds such a value. The README says so, and tells an operator to remove such values before upgrading.

### Decision 12. Takeover and drain

- **The items a takeover carries on are those not closed,** where today they are those that do not await their close: an item awaiting its close needs its auto-close timer, and may be promoted. `Records.Active` returns, by kind, on the index `work_records_by_kind`, the items that have an opening and no close that a reopen did not follow; the memory store lists the same items.
- **The takeover's order:**
  1. the fence, and the sweep, as today;
  2. **the kill switch, read** into the coordinator's view; a read that fails gives the lease up;
  3. the active items, each loaded with its board, and skipped when of a later version, as today;
  4. **the account of the items and the queue, rebuilt** from what step 3 loaded, without admission marks; items skipped, and items whose records do not fold, are not in it;
  5. each item advanced, under the takeover's context, which carries its epoch: a run that owes a kill is halted, and an item with no run that owes it left not staffed, before that item's next round or start. This holds per item: a takeover advances items one at a time, so another item's run, which owes nothing, may open its round first. An item awaiting its close arms its auto-close, unless the switch is on;
  6. the queue's walk, which admits again.
- **The step,** in order:
  1. no opening: wait (Decision 11);
  2. closed: the closed step (Decision 6), which drops the run;
  3. awaiting its close: forget its run, and arm its auto-close, unless the switch is on;
  4. no run: the kill switch on, or the item owing the latest on-row, records `item_awaiting_close` `not_staffed`, cause `kill_switch`, with `unstaffedBy` (a started item needs no run created first); not started, it starts if promoted or admitted (Decision 4), and waits otherwise; started, it creates its run, recording `killSwitchSeen`;
  5. its run stopped: `finish`;
  6. no agent invited: `unstaffed`;
  7. a round past its deadline that this instance did not hold: `missed`, as today;
  8. **its run owes a kill: drop the run, then halt it `killed`** (Decision 3);
  9. then `open`, `hold` or the decided stop, as today.

  While a kill switch write's outcome is unknown, with the view off, the step waits where rule 4 would start the item or create its run and where rule 9 would open or hold a round, and records nothing on that account; the other rules, which stop work, go on (R37).
- **The drain.** The coordinator is fenced at the drain's start, which cancels the auto-close timers and empties the account. Every lifecycle act answers `409` `draining`, as openings do, but the kill switch's on, which is recorded under the draining holding once the coordinator is fenced (Decision 5); the reads go on. The new routes' text says, besides, that the act is not confirmed, and what to do on the instance that leads next, while openings keep their text (Decision 14; amended in execution, 2026-11-03: an act drained after its commit answers so too, so its answer cannot say it did nothing). An act that comes after the drain ended the holding's context, and before its fence, answers `ErrNotHeld` as well, which the admin API answers as the drain's. The instance that leads next rebuilds everything from the records.

### Decision 13. Acts: who, why, the key, the preview, a lost answer

The use cases share one shape (Contract C16). **Every act answers `ErrNotHeld` when the coordinator is not held or its context has ended,** before it reads or writes anything. An act whose context ends while it acts answers `ErrNotHeld` too, and does not load the item again; what landed is found by its key on the instance that leads next.
- **The actor** is `Actor{Subject, Admin}`: the person Plan 9a signs in, or, through the admin API, the subject `admin` with `Admin` true, since its token is an admin's. Records keep the subject. 7b checks only what the lifecycle itself requires: turning the kill switch off needs an admin; roles per item and fresh authentication are Plan 9a's (the designer's call).
- **The reason** is `Act.Reason`, the one reason an act carries.
- **The key.** Every act on an item carries an `Idempotency-Key` of 1 to 128 printable ASCII characters, fixed in code, as an opening does, and the coordinator checks its form too (R20); the kill switch takes `After` instead.
  - Its record keeps `sha256:` and the hexadecimal SHA-256 of the key, never the key itself, and a digest of the act: its kind, its target, its amounts and its reason's code, never its text, which redaction may change.
  - Before it decides, the act looks for the key among the item's records' requests; a close's lives in its `item_closed`, the one record of a person's close. The same digest answers as the act did (R14); another digest is refused, `409` (`key_reused`). **A key names one act of its item** (R42, the controller's ruling): a key reused for another act on the same item is refused, and the same key on another item is that item's own act.
  - **A key lookup that finds its act refreshes the item in the account and advances the item, and does nothing else on its own:** the step decides from the item's state now (the closed step for an item closed, the start for one queued and promoted, the auto-close for one awaiting its close). One thing Close adds: when the item is closed now, it drops the run first, so that a failed advance cannot leave a turn to start. So a close's key found after a reopen drops nothing and stops nothing.
- **A failed append**, while the instance holds, loads the item, as `Open` does (Decision 11): if the act's record landed, found by its key, it answers as a repeat and carries on as a found key does, from the item's state now; if it did not land, nothing changed, and the act answers `503` (`unavailable`); if the load fails too, it arms a retry of the load and the carrying-on after [5 s], and answers `503`, and acts on its outcome, unknown, as its record may have landed: a close drops its run (Decision 6), and a promotion counts its item until the item's next load (Decision 4) (amended in execution, 2026-11-03: the final review's I1 and I2).
- **An act whose effect holds already** changes nothing, and answers `changed: false`: a close of a closed item, a reopen of an item not closed, a promotion of an item that has a run, of an investigating item without one, or of a queued item admitted or promoted, and the kill switch set to its state. An act the state forbids is refused with a code (Contract C4). **Such an act records nothing, so its key is kept nowhere** (R10): a close of a closed item, retried late after a reopen, closes it again; a reopen of an item not closed, retried late after a close, within the window, reopens it (the designer's call: it needs a lost answer and another person's inverse act between the try and its retry, and each act is undone by its inverse within its window; Plan 9b may make the item's latest record each act's `after`).
- **The preview.** Every act takes `Preview`: it runs the same checks on the same state, under the same lock, and returns the consequences it would have, recording nothing and acting on nothing (spec Section 4.2, "consequences first"; Section 8.3, "stops N runs on M items").
  - A preview takes no key, and the key lookup skips it. The kill switch's preview reads the switch from its table (`Controls.KillSwitch`), not from the view, and gives the latest row's number, which its act then names: a view one renewal behind would give a number the act finds stale.
  - Consequences are codes, ids and numbers (C16's `Consequences`): the runs that stop, with their round and reason; the items that start, close or are not staffed; when an item closes by itself, or that its auto-close is paused; the items investigating and the limit.
  - A preview needs the lease, as the act does (the designer's call: one code path, so the preview cannot drift from the act).

### Decision 14. The admin API, in 7b-1

Contract C4 is amended with 7b-1's routes.
- **As today:** loopback only, the admin token, bodies capped at 64 KiB, fixed in code, and decoded strictly. The answers to a standby, to a draining instance, during a takeover, and for an item a later build holds, are those openings get today (Plan 7d-1, Task 14 and its final review), but for their texts: the new routes' `409` `draining` and `not_held` say that the act is not confirmed, and what to do on the instance that leads next, while openings keep theirs (amended in execution, 2026-11-03: an act fenced or drained after its commit answers `ErrNotHeld`, Decision 13, so its answer cannot say it did nothing).
- **No act answers `no_revision`:** a promotion records, and its start waits for a revision; close, reopen and the kill switch need none; free text with no configuration is refused, `400` (Decision 9). Openings keep their `503` with no current revision.
- **An act's body over the cap** answers `413` (`too_large`), as an opening's does.
- **Errors** of the lifecycle's routes are `{"error": "<text>", "code": "<code>"}` (R15). The code is new and machine-readable; the text never quotes the request.
- **The kill switch during a drain** (Decision 5): the admin API calls `SetKillSwitch` first; on `ErrNotHeld`, it calls `RecordKillSwitchOn` if the leader holds and drains, answers `503` with `Retry-After: 1` if the leader holds and does not drain yet, and answers `409` (`not_held`) otherwise. An on recorded so answers `200` with `deferred: true` and no `owed`; an off answers `409` draining; a preview answers by the leader's status, and never reaches `RecordKillSwitchOn` (Decision 5; amended in execution, 2026-11-03: Task 19's ruling).
- **`GET /v1/items/{id}`** gains the item's incident, its type, when it opened, its place in the queue, why it is not staffed, when it closes by itself, or that its auto-close is paused, and its close. **The views read the kill switch from its table,** as the status does, so a standby shows the pause too, and take `Revisions`, for a delay recorded as zero; a delay they cannot read leaves `closesAt` out.
- **`GET /v1/status`** gains the kill switch, read from its table, so that a standby shows it too, and the queue, from the coordinator's snapshot, empty on a standby.

### Decision 15. The deployment's lifecycle, its tenant, and serve's wiring

- **Contract C1** gains an optional block, `lifecycle`: `investigating` [3], at least 1; `reopenWindow` [24 h] and `closedRulings` [72 h], positive durations with units. 7b-2 adds `extension`. Two instances set the block alike: the leader's values apply (the designer's call, as for the daily cap).
- **The tenant** (carry 17; the sweep's row 42). `Deployment.Check` refuses a `tenant` that `chain.ValidTenant` refuses (`^[a-z0-9][a-z0-9-]{0,62}$`), naming the key, so that serve never runs for a tenant whose chain can never be anchored.
- **`MCPGODEBUG`** (carry 18; the sweep's row 16). Serve refuses to start while the environment gives `MCPGODEBUG` a non-empty value, naming it, beside its other checks at start (`checkSystem`): the MCP SDK reads it, once, at its package's init, and its flags change the SDK's behaviour for the whole process, both gateways' included. An empty value is taken, since the SDK ignores it; a malformed one never reaches the check, since the SDK panics at init with its own message.
- **Serve wires,** each in the task that needs it: the coordinator's new ports, `Controls`, guarded by the lease, and `UpstreamSessions`, the MCP gateway's pool; the leader's renewal check, its read given from the kill switch's store on the lease's pool, as `Sampler` and `Schema` are given; the lifecycle's options; the admin API's access to the leader's status for the drain's kill switch; and `NewViews`, with the coordinator's snapshot, the windows, the kill switch's reads and `Revisions`.
- **`examples/deployment.yaml` and `examples/two-instances/`** gain the block, commented, and a tenant of the anchors' form.

### The interleaving test (the author's decision, 2026-11-02; Tasks 17 and 18)

A randomized test of the kill switch, the lease and the queue, at about 1.5 to 2.5 days. The coordinator's unit tests pin each rule alone; this test runs the rules together, under faults, in orders no one wrote down. It is not one of the design's decisions, which keep their numbers 1 to 15; 7b-2's are 16 to 18.
- **The run.** Two instances, `a` and `b`, each the real `app.Leader` over the real `app.Coordinator`, as serve wires them, share one world of in-memory fakes:
  - the stores: the memory records, board events and kill switch (`memory.Records`, `memory.Store`, `memory.Controls`), each behind a guard that checks the writer's epoch against the fake lease in the write's own step, as the Postgres stores check it in their transactions, and notes the epoch of every write it commits;
  - a fake lease both leaders take, renew and release, whose expiry the database's clock judges, as Postgres does; and each instance's holding (`app.NewHolding`), on its leader's monotonic clock and its own wall clock;
  - three kinds of clock (R39): one monotonic clock, which only goes forward and owns every timer, the coordinators' and the leaders'; the database's clock, which stamps every record and kill switch row, judges the lease, and answers the leaders' samples; and each instance's wall clock. Each coordinator reads the real `clock.DB` over the database's clock and its instance's wall clock, as serve wires it, so its readings follow a step as serve's do and never go back; a coordinator's timer waits, in monotonic time, from the coordinator's clock at its arming to its time, as `clock.Timers` does;
  - a fake runner, which refuses a start whose holding is not valid, as the runners do (`ReasonHolding`); fake sessions; a fake upstream pool; the registry's three revisions, which the driver makes current in turn, each with three agents and a work item type that closes by itself after 1 minute (R46): the base one, one whose rule that applies always matches a routing value, and one whose type the coordinator cannot run; the day's spend, zero, or the daily cap while the driver says so; and sequential ids (amended in execution, 2026-11-03: the plan had the registry's one revision, under which invariant 9 never met an admission that its start left not staffed for the daily cap, its routing or its type);
  - nothing else: no gateway, no network and no real store.
- **What the fakes inject,** each an event a schedule may pick:
  - a store append that commits, then loses its answer, and a store error before the commit, for the records, the board events and the kill switch's writes; a failed read of the kill switch;
  - the lease lost or expired in the middle of an act: the lease's expiry, or the other instance's take after it, lands before the act's next guarded write. The losing instance's lease expires on the clocks first, so its holding is no longer valid when the other takes, as the lease's premise has it, and its next guarded write is refused and fences it;
  - a failed renewal;
  - a drain of the leading instance, and its stop; an instance's crash;
  - a takeover by the second coordinator over the same stores;
  - time passing: monotonic time advances, and the database's and the wall clocks with it, firing each timer it passes, in order;
  - a clock step (R39): the database's clock, or one instance's wall clock, jumps forward or back, and moves no timer and no monotonic time. The database's clock steps forward by less than the lease's margin in all within a lease's term, the premise of Plan 7d-1's lease; other steps go up to 10 minutes either way, a bound of the test's;
  - a kill switch row, on or off, that the driver writes out of band, as scenario 22's `psql`, a restore or a writer that crashed would leave it;
  - launches that fail, for each reason, and launches a stop cancels; turns that end in each class, with or without their agent's turn end on the board;
  - concurrent acts: the kill switch on and off, with fresh, stale and previewed `after`s; close; promote; reopen; previews of each; openings with keys, and their retries with the same request and with another.
- **Schedules.** A schedule is a seed. A random source seeded with it (`math/rand/v2`) picks each next event among those enabled, until the schedule's length. The schedule then settles in two stages, with faults and steps off and a leader holding: first time passes by the retry wait and two renewal periods while the runner reports nothing, and the invariants that need the coordinator to catch up with the table are checked; then the runner answers every launch and ends every turn at once, time passes, timer by timer, until no coordinator timer is armed, and the invariants "once quiet" are checked. In each stage the invariants are checked at its end, not after each timer (R46). A schedule whose coordinators still have timers armed after two hours of virtual time, a bound of the test's, fails, as one that does not settle. Two modes:
  - **sequential,** the default, and the one a seed replays: the driver's goroutine runs every event to its end, the world's timers fire on it, and the runner reports from it. The coordinator holds its lock for each act, step, timer and report whole, so any concurrent run of them is equivalent to some order of whole calls, which the schedules explore. The one call that lets go of the lock while it writes, `RecordKillSwitchOn`, runs on a goroutine of its own, its write held by the fake store while the driver picks other events, until the driver lets it go;
  - **concurrent:** timers fire each on a goroutine, several acts run at once, and the runner reports from goroutines, so that `-race`, and a deadlock's timeout, see the leaders' and the coordinators' locks under real contention. Its invariants are checked when a batch of events has ended, and in the settling stages.
- **What a seed replays** (R35). In the sequential mode a seed replays its schedule, and the order of every port event in it, event for event: every timer and port is the world's, and the coordinator reaches its ports in an order no map decides (R33). `TestASeedReplaysItsSchedule` asserts it. Four conditions keep it so:
  - `RecordKillSwitchOn`'s goroutine reaches ports before it blocks, through the fence's `StopAll`, `EndAll` and drops: the driver waits until it blocks at the store's write, and, once it lets the write go, until the call returns, so no other goroutine reaches a port while the driver picks;
  - the world never ends the context a leader leads under, since `Lead` runs `Stop` on a goroutine of its own when it ends (`context.AfterFunc`); a crash is modelled by discarding the instance with its timers;
  - timer cancels are not port events, and are not logged;
  - the lease lost in the middle of an act fences the losing instance at its refused write, before the other instance's takeover acts. A seed cannot replay the concurrent mode's timing: there it replays the choice of events and faults, not the order in which the Go scheduler runs them; a failure there prints its seed and its event log, and is chased by running the seed many times, or by writing its order as a sequential schedule. Nor does the run test a bound measured in real time, such as a leader's `RenewEvery` on a call, or the drain's write's `WriteTimeout`: the fakes answer at once, and those bounds have tests of their own.
- **A failure** prints the schedule's seed, the tail of its event log, the invariant it broke, and the command that replays it, `go test -race -count=1 -run '^<test>$' ./internal/work/app/ -args -interleave.seed=<seed>`, which runs that one schedule in the test's own mode, sequential or concurrent, logging every event; a concurrent failure's command runs it 20 times, `-count=20`, since there a seed replays the choice of events and faults, not their timing (amended in execution, 2026-11-03: the plan ran a seed sequentially alone, which cannot reach a concurrent failure).
- **Runs** (R46). The default run, in every gate from Task 17 on, starts from a base seed fixed in code: about 200 sequential schedules of 200 events, and 20 concurrent ones, the same schedules in every gate, run once in each gate. Its budget is 20 seconds on the Mac under `-race`, which Tasks 17 and 18 measure; a run over it lowers the counts, the flags' defaults, rather than the checks. The default run takes about 7 seconds, so no count was lowered (amended in execution, 2026-11-03: as Task 18 measured it, the sequential schedules 6.2 to 6.4 seconds and the concurrent ones about 0.6). The soak run, at the branch's end, runs for a duration, `-interleave.for=40m`, which the sequential and the concurrent tests share half and half, under `-timeout 60m`, from a base seed of the clock's, which each test logs with `t.Log` and the soak shows under `-v`; `-interleave.base` gives one. A test runs its schedules in its own function, not as subtests, so `-v` adds no line per schedule. The flags are `-interleave.seed`, `-interleave.base`, `-interleave.schedules`, `-interleave.concurrent`, `-interleave.events` and `-interleave.for`, declared in the package's tests.
- **The invariants,** each checked as its line says. The controller's four are the first five here, the first split in two; 6 to 8 are this plan's (R38); 9, the queue's order, comes with the controller's ruling on clock steps (R39, R40):
  1. **No work while the switch is on.** While the table's latest row is an on-row, neither instance calls `Turns.Start`, `Sessions.Issue` or `Sessions.Move` (R36): from that row's commit, when a coordinator wrote it, by `SetKillSwitch` or `RecordKillSwitchOn`, whatever its answer (R37); and, when the driver wrote it out of band, from the moment the instance's view reaches it (`KillSwitchSeen`), the residual R37 states, whose end invariant 2 checks. Only a call made while the calling instance's `Holding.Valid` holds for its epoch counts: a call made after its holding ended is refused downstream, by the runner before every turn's start and by the gateways before every request, as the fake runner refuses it too (`ReasonHolding`); in the concurrent mode such a call can come from a step already under way when the lease moved. After every event at whose end an instance's view is on, that instance has no live session and no turn the runner still runs (amended in execution, 2026-11-03: the plan judged only the events in which the view turned on, a weaker check before the soak); and an on's success answer names every run that was running when the on came in its `Stopped` or its `Owed`. Checked at every port call, and after every event.
  2. **Every run alive at an on-row is halted.** After the first settling stage, no item owes the latest on-row (`State.Owes`): every run created before it has stopped, and no item with no run that owes it has started.
  3. **No queued item waits while a slot is free.** After every event, the leading coordinator's snapshot holds a waiting item only while its items investigating are at the limit or above, or while its view is unsettled (`KillSwitchUnsettled`; R37); once quiet, the records show no queued item while fewer than the limit investigate.
  4. **The limit and one.** After every event, the items investigating, counted from the records (the items whose status is `investigating`, and those queued and promoted) and as the leading coordinator's snapshot counts them, are at most the limit and one (Decision 4).
  5. **The records fold, and every write carries its holding's epoch.** After every event, every item's records fold, and every write the guarded stores committed carried an epoch, the lease's at its commit.
  6. **A key names one act of its item** (R42): after every event, no two records of an item hold one request's key, and each key an item's records hold carries the digest of the first act under it that landed: an act the driver had in flight to that item under that key as its record committed, which the records' port judges at the commit. An act refused before its record, or whose append did not land, leaves the key free for the next. The check assumes that an act's record lands within the act's call. The same key on two items names two acts, one on each (amended in execution, 2026-11-03: "the first act the driver sent" failed correct schedules, since a key whose first act landed nothing is free).
  7. **A stopped run leaves nothing running:** once quiet, no run that stopped, and no closed item's run, has a live session or a turn the runner still runs, and each such run's upstream sessions were closed.
  8. **Auto-close keeps its pause and its deadline:** after every event, no automatic close was recorded while the table's latest row was an on, a row the driver wrote out of band counting from the moment the recording instance's view reached it, as in invariant 1; and each automatic close's `due` is the deadline its item's state before it gives (R19), with the off time of the recording instance's view (`Lifecycle().KillSwitch.OffAt`), not the table's latest off-row's, which a row written out of band and not yet seen can move; and the time its record is stamped with, the database's, is no earlier (R41).
  9. **The queue's order** (R40). After every event, every item the walk admitted in it ranks ahead of every item still waiting in the leading coordinator's snapshot. The order judges admissions by the walk alone, each by the record that took the item out of the queue, the first after its opening: an item the walk admitted is a queued item, waiting in the snapshot before the event or opened during it, that leaves the waiting list during the event with no such record, admitted and its start due, or with one the event committed, its start or its not staffed at its start for the daily cap, its routing or its type. A promotion, a close, or a not staffed for the kill switch is no admission, and neither is a not staffed in the opening's own append, nor a record that committed before the event with its answer lost, which the item's next load found, not the walk (amended in execution, 2026-11-03: the check had misjudged such an item, a fault of the test's, not of the coordinator). A promotion past older waiting items is correct, and no admission (Decision 4). An item ranks by its opening record's time, then its id. Against the order in which the openings committed, that tolerates an inversion only between two items whose opening records' times are out of their commit order, which a step back of the database's clock between them makes: R9's residual, and no other. The snapshot before an event is the last one a leading coordinator published. An item absent from the snapshots, whose records the coordinator could not read, holds up no other (spec Section 8.2).

  **Under clock steps** (R39), every invariant must hold, invariant 9 with its one tolerance. None depends on the clock going forward: invariants 1 and 2, and 8's pause, follow the table's row numbers and the views; 3 to 7 follow counts, records, keys, sessions and turns; 8's deadline compares two of the database's times; 9 orders by the openings' recorded times.

### Resolutions

**The design's,** each the designer's call (R2 and R5 are 7b-2's):
- **R1.** A person's reason is redacted, not refused, when a rule changes it without an escape, as an agent's text is.
- **R3.** No change to `agentswarm inspect`: the kill switch shows through its route, and records of the new kinds print as any record.
- **R4.** The items that stayed unstaffed are those whose `item_awaiting_close` names the latest on-row (`unstaffedBy`): opened while the switch was on, queued or without a run when it turned on, whose run it stopped, or that owed it later. Row numbers, not times, decide it.
- **R6.** A promotion is allowed as often as an item has no run: an item whose start left it not staffed again, for its routing or its type, may be promoted again once its configuration is fixed.
- **R7.** An automatic close records its due time beside the record's own time, for Plan 8a's finality.
- **R8.** The kill switch's `after` is required, 0 before the first row, so a client that never read the switch reads it once, or previews, before it acts.
- **R9.** The queue's order after a rebuild is the openings' `recorded_at`, then the items' ids. A step back of the database's clock between two openings can swap two items' places after a takeover; within a holding, the order is the commit order.
- **R10.** An act whose effect holds keeps no key: it records nothing. Its residual is stated in Decision 13.

**This plan's,** where the design left a choice to the builder, or recording a ruling of the controller's and the rules it brings, each with what it costs if wrong:
- **R11. A halt's spend.** A halt reads the run's spend once; a read that fails gives the run's last recorded spend, the latest ended round's `Spent`, and the halt goes on, logged as a failed step: no read stops a halt, as Decision 3 says of the board. Cost: a halted run's `run_stopped.spent` lags by its last round's calls.
- **R12. A reopen with no run.** The `awaiting_close` a reopen returns an item with no run to is `not_staffed`, with the cause of its latest `item_awaiting_close` before its close, or `closed` when it had none, as when it was closed while queued, or started without a run. Cost: a reopened item shows `closed` where it had been not staffed at the daily cap, or the reverse.
- **R13. A promotion's reason.** `item_promoted` carries `reason`, omitted when none, as `item_closed` and `item_reopened` do: Decision 9 has a promotion take a reason, which C19's list of its fields left out. Cost: one field.
- **R14. A repeat's answer.** An act repeated with its key answers `changed: true`, as the act did, with the items investigating and the limit now; the stops, starts and closes it made are in its records, which the item's view shows. Cost: a client that reads a repeat's `consequences` for the act's effects finds fewer of them.
- **R15. Codes on the lifecycle's routes alone.** The refusals of the act routes and of the kill switch's carry `code` (Contract C4). The opening route, and the refusals every route shares (401, 403, an unknown route, 405), keep Plan 7d-1's `{"error"}`. Cost: Plan 9a gives the opening route codes.
- **R16. The `Active` tests' names.** Postgres' `TestActiveItemsAreThoseNotAwaitingClose` becomes `TestActiveItemsAreThoseNotClosed`, which the memory store gains too; `TestActiveItemsAreOpenedAndNotAwaitingClose`, in both stores, becomes `TestActiveItemsAreOpenedAndNotClosed`, its assertions unchanged. Cost: names.
- **R17. The queue's position** counts the queued items neither admitted nor promoted, from 1, in the queue's order. An admitted or promoted item that has not started shows no position, and counts in `investigating`. Cost: a view that shows a waiting item's place another way.
- **R18. The view's `notStaffed`** is the `awaiting_close` record's cause, or `kill_switch` when the record names an on-row (a run the kill switch stopped). A run cut or stopped not staffed shows its run's reason in `stop`, as today. Cost: one field's values.
- **R19. Auto-close's deadline is a function of the item:** `State.AutoCloseAt(delay, offAt)` is the latest of `State.ResolvedAt()`, the item's latest entry into `awaiting_close` and `offAt`, the kill switch's latest off, plus the delay. In 7b-1 `ResolvedAt` is the opening's time; Plan 7c-1 gives an item with a trigger its trigger's resolution. The coordinator's timers and the views both read it. Cost: none outside the item package.
- **R20. The coordinator checks an act's key too,** 1 to 128 printable ASCII characters, not only the admin API, since Plan 9a calls the use cases directly: a malformed or missing key, but a preview's, answers `ErrInvalid`. Cost: none.
- **R21. Board tests for the two notices.** The design names no test of the board's `kill_switch` and `skill_yanked` notices; Tasks 3 and 12 each add one, among the tests marked "(the plan's)". Cost: none.
- **R22. A memory twin of the kill switch's store,** `memory.Controls`, and `memory.Records.UnstaffedBy`, for the coordinator's tests, with a test that they answer as the Postgres store does. Cost: none outside the tests.
- **R23. A promotion's checks, in order:** a closed item (`closed`); then its effect (`changed: false`); then, while the kill switch's view is unsettled and off, `ErrUnsettled` (R37; Task 14); then `kill_switch_on`, `daily_cap_reached` and `limit_reached`. Cost: which code a request that fails two checks gets.
- **R24. `RecordKillSwitchOn` on a coordinator held with a live context** refuses with `ErrNotHeld`: under a draining leader that cannot happen (Decision 5), and the admin API's next try sends the on to `SetKillSwitch`. Cost: none.
- **R25. The refresh's pending mark** lasts from its arming until its run starts: a renewal that finds a newer row while a refresh waits to run arms none. Cost: refreshes queue on the coordinator's lock, one for each renewal that finds a newer row while the lock is held, as through a takeover, whose fenced view reads row 0 until it sets its view (R47); each then reads the table again, and changes nothing unless a newer row came since, harmlessly (amended in execution, 2026-11-02: the plan said two, but a refresh that has started no longer marks itself pending while it waits for the lock).
- **R26. `worstDrain` stays as it is,** confirmed against its doc and `Leader.renew`: the renewal's kill switch read comes after the sample and the check, and is skipped once the leader drains, so a drain's stop waits for no read that began after the drain. The comments at `Leader.renew` and `worstDrain` say so. Cost: none. A read in flight when the drain began ends within `RenewEvery` of its start, and no renewal reads once the leader drains, so the leader's stop still waits at most for a renewal's sample and check, which `worstDrain` counts (amended in execution, 2026-11-02: Task 15 found the plan's "a stop timeout one renewal period short" overstated, since no read starts once a drain began).
- **R27. Where the design names a test without its package,** the plan puts it with the rule it pins: `TestAutoCloseCountsFromTheLatestEntryIntoAwaitingClose` and `TestAnItemOpenedByAPersonClosesByItselfFromItsEntryIntoAwaitingClose` in the item package, which computes the deadline; `TestAStandbysItemViewShowsTheAutoClosePaused` in `internal/work/app`, whose views compute the field, though the design lists it under `internal/admin`; `TestAnActOnACoordinatorWhoseContextEndedAnswersNotHeld` in Task 16, with every act. Cost: a test in another file.
- **R28. A kill switch table that cannot be read:** `/v1/status` answers `503`, logged, as for a schema it cannot read; an item's view leaves `closesAt` and `autoClosePaused` out. Cost: a status that fails on one table.
- **R29. `make lint` in every task's gate,** beside the controller's rule of each task's own packages' tests and its new scenarios: lint type-checks every package, so a change that breaks another package's build shows in the task that made it. Cost: two lint runs a task.
- **R30. `RulingsOpen` and the late kinds:** reject, un-reject and dismiss are open while the item is not closed; any other kind, as Plans 8b and 9b add adoption, feedback and milestone rulings, is open while it is not closed and for the window after its close. 7b-1 defines no late kind. Cost: Plan 9b names the late kinds in `RulingsOpen`.
- **R31. The kill switch table's `id`** is unique per tenant, `UNIQUE (tenant_id, id)`, as `work_records`' is. Cost: none.
- **R32. A close's reason before Task 7.** Task 5's close records its reason once its form is checked (Decision 9's code and length); Task 7 routes every act's reason through `PersonText`. Within the branch, between the two tasks, a reason's text is recorded as given. Cost: none once Task 7 lands.
- **R33. Deterministic order at the ports.** Every loop of the coordinator over a map whose order reaches a port iterates in sorted order: the drop of every run and the advance of every item at the kill switch's on, the auto-close timers armed at the off, the advance of every item when a read settles the view (R37), the sessions `hold` ends for agents that left between rounds (its loop over `r.tokens`), and the stops of a run's turns at a deadline and at a skip. Loops that only cancel timers, as the fence's and `cancelAutoCloses`', need no order, since a cancel is no port event ("The interleaving test"). One seed then replays one order of port events (`TestASeedReplaysItsSchedule`). Cost: a sort per loop, over a few entries.
- **R34. Where the interleaving test sits:** two tasks after Task 16, where every rule its invariants judge exists, rather than fakes built early with invariants added as each feature lands. The coordinator's unit tests already pin each rule as it lands; this test's worth is in combinations, which exist only once the acts, the queue, the kill switch, the takeover and the drain do; and a driver grown over twelve tasks would spread one design across twelve reviews. Cost: an interleaving bug in an earlier task is found later, but in the same package and branch, before the end-to-end scenarios build on it, and its fix reaches back into that task's code.
- **R35. What a seed replays:** the sequential mode's schedule and order of port events, not the concurrent mode's timing, nor bounds measured in real time ("The interleaving test"). Cost: a concurrent failure may take many runs to reproduce.
- **R36. No model call, judged at the coordinator's boundary.** The run has no LLM gateway. A model call needs a session the coordinator issued and moved into a round, so invariant 1 holds the coordinator to issuing and moving none, and to leaving none live at the on's answer; that the gateways refuse a call without a live session is their own tests'. Cost: none, the boundary being where the coordinator acts.
- **R37. A kill switch write whose outcome is unknown fails safe** (the controller's ruling, 2026-11-02). The design's anti-goal stands, with one residual, the first of "The residuals" below: no run "opens a round, starts a turn or makes a new model call after the kill switch answered, or after its row committed with its answer lost". Decision 5 alone left a window: an on whose answer is lost, and whose second read fails, leaves the view off while the table holds the on, for up to the retry wait [5 s] or until a renewal's refresh.
  - **The mark.** Every store error on an on or an off that leaves the write's outcome unknown marks the view unsettled, under the coordinator's lock, before the second read: every error but the guard's refusal, which is known to have written nothing. An error that comes because the coordinator's context ended counts too: the act answers `ErrNotHeld` with no second read, and a timer may take the lock before the drain's fence does. Only a successful read of the switch, or a store's answer that carries its state, clears the mark: the second read, its retry, the leader's refresh, or the answer to a later request, each of which sets the view; `applyKillSwitch` then acts on any change, as Decision 5 says. The fence clears the mark with the view, and the next takeover reads the table. The second read and its retry go on as Decision 5 says, each read that fails arming the next after [5 s]. Clearing the mark advances every active item, in sorted order (R33), and runs the walk, whether or not the view changed, so that the work that waited goes on. What that advance carries out of a kill still owed, the runs it halts for the view's latest on-row and the items it leaves not staffed, reaches the answer that settled the view only while the view is on, among its `Stopped` and `Unstaffed`, so that an on's answer never leaves out a run that ran when its request came (amended in execution, 2026-11-03: invariant 1 found an on whose answer named such a run in neither its `Stopped` nor its `Owed`, dd37596). While the view is off, the off's list, read after that advance, gives those items with their states now, and the answer names none of them among its stops or the items it left not staffed (amended in execution, 2026-11-03: the operator promotes from that list, Review Focus 4, 6351cd7). The snapshot shows the mark (`Lifecycle.KillSwitchUnsettled`), and serve's log notes it (C3).
  - **While the view is unsettled and off, it counts as on for every start of work, and nothing is recorded on that account:**
    - `begin` starts no turn: it keeps the turn among the round's starting turns, posts no notice, and arms its retry after the retry wait, as for a launch that failed; the on's guard arms none, since its run is to be halted, but here the outcome may be that nothing changed;
    - the step waits where it would start an item, create a run, or open or hold a round (Decision 12, rules 4 and 9), so no session is issued or moved; the rules that stop work go on: the closed step, `finish`, `unstaffed`, `missed`, a decided stop, and an owed kill's halt;
    - the walk admits no item, and a start of an admitted or promoted item waits (Decision 4): admission and promotion staff nothing, and an opening records its `item_opened` alone and waits queued;
    - a promotion answers `ErrUnsettled`, which the admin API answers `503` (`unavailable`), after R23's checks of a closed item and of its effect, and records nothing: the switch may be on, and the client sends it again, as after a lost answer;
    - auto-close records no close, as while the switch is on (Decision 8): a timer that fires records nothing, and the advance that clears the mark arms it again (invariant 8).

    Close, reopen, previews and reads go on, and so does the kill switch, whose next request's answer settles the view. While the view is unsettled and on, the on's rules apply as they are.
  - **Why fail safe.** A safety control fails toward safe. While the store cannot be reached, the coordinator cannot record a turn's start, a round's opening or a run's creation anyway, so the pause costs near nothing; when nothing changed, work resumes at the first read that succeeds, at worst a retry wait after the store answers again.
  - **The window between the commit and the store's answer** is already covered by the coordinator's lock. `SetKillSwitch` holds it from before the write until `applyKillSwitch` has acted, and every step, timer, report and act takes it, so no `begin`, no session issued or moved, and no round opened comes between a row's commit and the drop of every run, on the success path and the unknown one alike. A launch in flight at the commit, which `Turns.Start` left pending before the lock was taken, is cancelled by `StopAll` (step 2), and its `Launched` report finds its run dropped (step 3), so nothing arms its retry.
  - **The residuals.**
    - A turn already running when the row commits keeps its session until the coordinator acts on the row: at once on a success answer, under the same lock; at the first read that succeeds on an unknown outcome. Its model calls in that window are not refused at the coordinator's boundary (R36): the mark stops work from starting, not work begun. Ending every session on an unknown outcome would close that too, but would cut every running turn for a write that may not have landed, against Decision 5's "nothing changed" answer; it is the controller's to weigh. The window between the commit and a success answer is "decide, record, act"'s own.
    - An on-row written out of band, as by scenario 22(b)'s `psql`, a restore or a writer that crashed, marks nothing: the coordinator learns of it at the leader's next renewal check, within one renewal period, two at most (Decision 5), or at a takeover. Invariant 1, and invariant 8's pause, allow work in that window only for rows the driver writes out of band. A refresh whose read fails leaves the row to the next renewal.
    - `RecordKillSwitchOn` sets no mark: its coordinator is fenced before the write, and the next takeover reads the table.
  - **Tests:** `TestNoTurnStartsWhileAKillSwitchWritesOutcomeIsUnknown` and `TestAFailedKillSwitchWriteThatDidNotCommitResumesStaffingOnceARereadSettlesIt` (Task 14), with two of the plan's; invariants 1, 3 and 8 (Task 18).
  - Cost: a mark on the view, and its checks at `begin`, the step, the walk, a promotion and auto-close's timer; work that would have started waits for the store; a promotion answers `503` while a write is unsettled.
- **R38. Three invariants beyond the controller's four,** which the plan numbers 1 to 5: invariants 6, 7 and 8, which pin Review Focus 2 and 3, and the anti-goal "an act whose record landed with its answer lost, left without its effects". Cost: three checks over the records and the fakes.
- **R39. Clock steps, both ways** (the controller's ruling, 2026-11-02). The world steps the database's clock and each instance's wall clock, forward and back, while one monotonic clock owns every timer and only goes forward, as a process's does ("The interleaving test"). Each coordinator reads the real `clock.DB` over them, so it sees a step as serve does: a wall clock's at once, the database's at the next sample, and, from its first sample on, never a reading that goes back. Every invariant must hold under both directions, the queue's order with the one residual R9 accepts, which invariant 9 tolerates and nothing more (R40). Auto-close never closes an item before its deadline on the database's time (R41). No safety invariant depends on the clock going forward: the kill switch decides by row numbers (Decision 5), and the holding's check takes the larger of the times elapsed on the monotonic and the wall clocks, so a step back of the wall clock extends no holding.
  - **The bound.** The database's clock steps forward by less than the lease's margin in all within one lease term. A larger step lets a standby take the lease while the holder's watch still counts its holding valid: that breaks the premise of Plan 7d-1's lease, not a rule of 7b-1's, and no lifecycle invariant could survive it. Steps back, and steps of a wall clock, have no such bound; the test keeps them within 10 minutes.
  - Cost: the world keeps three kinds of clock rather than one, and `clock.DB` in each instance.
- **R40. The queue's order is its rank, within a holding as after a takeover** (invariant 9). The account orders queued items by their opening record's time, then their id (Decision 4), at all times, so that the same records give the same admissions, before a takeover and after it (Decision 4). R9's residual then shows within a holding as well as after a takeover: two items whose openings a step back of the database's clock put out of their commit order swap places. R9's "within a holding, the order is the commit order" holds while the database's clock does not step back between two openings. The alternative, an account kept in commit order within a holding, would order such items one way before a takeover and the other way after it, and would put an opening whose answer was lost, found by its retry, behind openings that committed after it. Invariant 9 tolerates that swap and no other inversion. Test: `TestTheQueuesOrderIsTheOpeningsTimesWithinAHoldingAndAfterATakeover` (Task 9). Cost: within a holding, two openings that a step back of the database's clock puts out of order swap places, as they would after a takeover.
- **R41. Auto-close is judged on the database's time** (the controller's ruling of 2026-11-02 that it never close an item before its deadline on the database's time). Two rules beside Decision 8's:
  - **A timer that fires before its deadline on the coordinator's clock** records nothing and arms itself again at the deadline, but no sooner than one retry wait [5 s] from now. A timer waits, in monotonic time, the span from the coordinator's clock at its arming to its deadline; a correction of that clock back after the arming, as after a step back of the database's clock, makes it fire before the deadline on that clock. Without the new arming, the item would wait for a takeover, or the kill switch's off, to close. Without the floor, a clock that holds its reading, as `clock.DB` holds its last one after a correction back, would have the timer fire again at once, and again, for as long as it holds (amended in execution, 2026-11-03: the interleaving test found that hot loop, fe231d4).
  - **The records store refuses an automatic close that it would stamp before its `due`,** with `item.ErrNotDue`, in the append's own transaction, against the time it stamps the record with: in Postgres, `greatest(clock_timestamp(), the item's last record's time)`, as `insertRecord` stamps it today; in the memory store, its clock under its rule for an item's times. The coordinator's clock is an estimate of the database's: it holds its last reading after a correction back, and runs ahead after a step forward of the process's wall clock until its next sample, so the coordinator's own check cannot keep a record from preceding its `due`. The timer tries again after the retry wait [5 s], as for any record that fails (Decision 8).
  - Tests: `TestAnAutoCloseTimerThatFiresBeforeItsDeadlineArmsAgain`, `TestAnAutoCloseTheRecordsRefuseAsNotDueIsTriedAgain` and `TestTheRecordsRefuseAnAutomaticCloseStampedBeforeItsDue` (Task 6); invariant 8 (Task 18). Cost: one comparison in each records store's append, a sentinel, and a new arming; an automatic close may land up to a retry wait after its due while the clocks disagree. With the floor, an auto-close can come up to one retry wait [5 s] late when the instance's clock runs ahead of the database's: negligible against the default delay [30 min], but it is why scenarios 20 and 21, whose auto-close comes after 2 seconds, run serve with a retry of 500 ms (amended in execution, 2026-11-03: the floor's cost, which Task 20 met).
- **R42. An act's `Idempotency-Key` is scoped to its item** (the controller's ruling, 2026-11-02, on the plan review's C1). A key reused for another act on the same item is refused, `409` (`key_reused`); the same key on another item is that item's own act. An idempotency key dedupes the retries of one request, and a retry always goes to the same item, so the item's own records, where Decision 13's lookup already reads, are where a key is found. A tenant-wide scope would need a new port, a query and an index for no gain. Tests: `TestAKeyReusedForAnotherActIsRefused` (Task 7); invariant 6 (Task 18). Cost: a client that reuses one key on two items gets two acts, one on each, as it asked.
- **R43. A delay whose type is gone** (the plan review's M11). An item that never started, whose type the current revision no longer holds (as when its start left it not staffed for its `type`), cannot read its auto-close delay from any revision, and Decision 8 would try the arming again every retry wait [5 s] for ever. Its timer takes instead the default of spec Section 15, [30 min], which `internal/registry/config` exports for it (`config.DefaultAutoClose`), and logs that once. A revision that cannot be read at all, as while the registry loads, is still tried again. Test: `TestAnItemWhoseTypeIsGoneClosesByItselfAfterTheDefaultDelay` (Task 6). Cost: such an item closes after the default, not after a delay its removed type set.
- **R44. A title's characters are checked after it is trimmed** (the plan review's M12). `title` checks `Unseen` after `strings.TrimSpace`, as it redacts the trimmed title: a title whose only such characters lead or trail it, as a trailing line feed, is trimmed, not replaced. Test: `TestATitleWithACharacterAReaderCouldMissIsReplaced` (Task 3). Cost: none.
- **R45. How long a halt's snapshot lives** (the plan review's note). `drop` keeps a run's snapshot in `c.snapshots[run]` until the halt that reads it commits, and a halt that fails reads it again on its retry; the fence clears every snapshot, so a halt after a takeover classes from the board (Decision 3). Cost: none.
- **R46. The interleaving test's budget and timebox** (the plan review's I6). The world's work item type closes by itself after 1 minute, not 30, so that a schedule's settling passes minutes, not half an hour, of leader timers. The default schedules run once in each gate: Task 18's run of the nine invariants replaces Task 17's run of invariant 5. In the settling stages the invariants are checked at each stage's end, not after each timer. The default run, sequential and concurrent, stays under 20 seconds on the Mac under `-race`, which Tasks 17 and 18 measure and report; if it does not, its schedule counts, which are flags' defaults, come down until it does, and the report says so. The soak runs for a duration, `-interleave.for=40m`, shared half and half by the sequential and the concurrent tests, under `-timeout 60m`, so its size never makes it time out. **Timebox:** Tasks 17 and 18 are sized at about 1.75 to 2.9 days together. If Task 17's world or its replay does not work after its implementer's attempt and one fix round, or if Tasks 17 and 18 pass their high end, about 3 days on the roadmap's yardstick as the controller judges the work done, with coordinator faults still being found, the implementer stops and reports, and the controller takes it to the author as an ADR-016 signal. Cost: a default run's coverage bounded by its time, and a soak's by its duration.
  - **The timebox fired** (amended in execution, 2026-11-03: its outcome, for the record). Tasks 17 and 18 came to about 4 to 6 days on the yardstick, against a high end of about 3, with coordinator faults still being found, of one class: an account left stale by a lost answer. The controller took it to the author on 2026-11-03 as an ADR-016 signal, and execution went on. The faults the interleaving test found were each fixed, test first, in a commit of its own: fe231d4, dd37596, 7f1e83e, d1f4f2c, and the follow-up ec926ad with 8155f65. Each affected an answer's completeness, liveness or resources; none affected safety.
- **R47. A takeover acts on the kill switch through its own advance** (added in execution, 2026-11-02: Task 15's ruling). The takeover sets the view from the table it read after the sweep, in the same block as it takes up its holding, its term and its context, and does not go through `applyKillSwitch`. Its single advance of every item, then its walk, acts on that view as every step does. The fence before it did the on's steps 2 to 4 but the close of upstream sessions: every turn stopped, every session ended, every run forgotten and every auto-close timer cancelled. Its advance is step 5: a run that owes the kill of the view's latest on-row is halted, and an item with no run that owes it is left not staffed, before that item's next round or start; no auto-close timer is armed while the view is on, and while it is off each is armed at its deadline, which counts the latest off (Decision 8). A takeover runs the on's steps no second time, and the off's steps not at all: it advances no item twice, and lists no item that an on left not staffed, which an off answers, not a takeover. It logs `kill switch set`, cause `takeover`, only when it acts: the view is on, its advance stopped a run or left an item not staffed for an on-row, or something is still owed (Contract C3). It is equivalent in effect to `applyKillSwitch`'s two rules: through them, the fenced view, which reads row 0, would take any past on-row for a raise, stop and drop again, advance every item twice, and, the switch off, log an off and list its items at every takeover. Test: `TestATakeoverAfterAPastOnLogsNoOffAndListsNothing` (Task 15, a guard), with Task 15's tests. Cost if wrong: an off written out of band, whose on no leader applied, never gets its `stayedUnstaffed` list.

## Contracts between tasks

Tasks that never see each other's code meet here. A task that produces one of these follows it exactly; a task that consumes one relies on nothing else. Plan 7d-1's and Plan 7d-2's other contracts are unchanged. Names are the design's proposals where the code's vocabulary did not call for others.

### C1 (amended). The deployment file's lifecycle and tenant (Task 4 reads it; Task 20's harness writes it)

```yaml
tenant: example                 # refused unless ^[a-z0-9][a-z0-9-]{0,62}$, so that its chain can name anchors
lifecycle:                      # optional; each key defaults as below
  investigating: 3              # [3] items investigating at once (spec Section 5.1); at least 1
  reopenWindow: 24h             # [24 h] (spec Section 4.1); a duration with its unit, above zero
  closedRulings: 72h            # [72 h] late rulings on a closed item (spec Section 4.2); as above
```

Serve also refuses to start while the environment gives `MCPGODEBUG` a non-empty value, naming it. 7b-2 adds `lifecycle.extension`.

```go
// package deploy
// Lifecycle is the lifecycle block (Contract C1), its defaults filled in.
type Lifecycle struct {
	Investigating int           `yaml:"investigating"` // [3], at least 1
	ReopenWindow  time.Duration `yaml:"reopenWindow"`  // [24 h]
	ClosedRulings time.Duration `yaml:"closedRulings"` // [72 h]
}
// Deployment gains Lifecycle Lifecycle; Load fills in its defaults, as it does Timing's.
```

### C3 (amended). Serve's log (Tasks 4, 12, 14 and 16 write it)

| `msg` | Attributes | Logged by |
|---|---|---|
| `kill switch set` | `on`, `seq`, `onSeq`, `by`, `epoch`, `cause` (`act`: the coordinator's own row; `answer`: a store's answer to a request that appended nothing, or was stale; `reread`: a read after an unknown outcome, or its retry; `renewal`: the leader's refresh; `takeover`: a takeover's read, R47), `deferred`; once the on's steps ran, a takeover's advance among them, `stopped`, the number of runs stopped, and `unstaffed`, the items left not staffed; once the off's steps ran, `stayedUnstaffed` (items) and `stayedUnstaffedUnread`, true when that list could not be read in full; and `owed` (items). `stopped`, `unstaffed` and `owed` are left out when the on is deferred, and when the coordinator holds no account, fenced as its steps went on (amended in execution, 2026-11-02: an account a fence emptied would give them wrong) | the coordinator, at Warn, whenever its view changes and acts, and for a deferred on (Tasks 12 and 16); at a takeover, only when it acts: its view is on, its advance stopped a run or left an item not staffed for an on-row, or something is still owed, so that a takeover that finds an off with nothing owed logs nothing (R47; amended in execution, 2026-11-02: after a fence every row reads as a change, and an off's line at every takeover would mislead) |
| `kill switch unsettled` | `on` and `seq`, the view's, and `epoch` | the coordinator, at Warn, when a write's unknown outcome marks the view: it starts no work until a read settles it (R37; Task 14) |
| `kill switch settled` | `on` and `seq`, as the read found them, and `epoch` | the coordinator, at Info, when a read clears the mark; `kill switch set` follows when the view changed (Task 14) |
| `serve refused to start` | as today, with `MCPGODEBUG` named when it is set | serve (Task 4) |

A halt that fails is logged as a failed step, with its item, as every step is; so is a read of the switch that fails. An off that cannot read the list of the items the latest on left not staffed logs `the items the kill switch left unstaffed could not be read`, with `onSeq`, at Error, and an item of it that cannot be read is logged as a failed step (amended in execution, 2026-11-02: as Task 13 built the off's list, 1326201). No line carries a person's text.

### C4 (amended). The admin API (Task 19 serves it; the harness calls it)

Every route needs the bearer token, as today. Every act on an item needs an `Idempotency-Key`, of 1 to 128 printable ASCII characters, but its preview. The kill switch's act takes `"after"` instead: the latest row's number its sender saw, 0 before the first row. An act's body is optional; when given, it is one JSON object, at most 64 KiB, decoded strictly, of `"reason": {"code": "...", "text": "..."}` and `"preview": true`, each optional; the kill switch's body is required, and adds `"on"` and `"after"`, each required, a preview's too, so that a missing `on` never reads as an off. The coordinator never compares a preview's `after`, and refuses one only below 0, so a preview still teaches the seq: a client that never read the switch sends `after: 0`, or reads `GET /v1/kill-switch` first (amended in execution, 2026-11-03: as Task 19 built it).

| Route | Body, besides reason and preview | `200` answer |
|---|---|---|
| `POST /v1/items/{id}/promote` | — | `{"changed": bool, "consequences": {...}, "item": <view>}` |
| `POST /v1/items/{id}/close` | — | as above |
| `POST /v1/items/{id}/reopen` | — | as above |
| `GET /v1/kill-switch` | — | `{"on": bool, "seq": N, "since": "...", "by": "...", "reason": {"code": "...", "text": "..."}}`, read from the table; `since` and `by` left out before the first row, and `reason` whenever the latest row has none (amended in execution, 2026-11-03: as Task 19 built it) |
| `POST /v1/kill-switch` | `"on": bool, "after": N`; a reason for the on | `{"on": bool, "seq": N, "changed": bool, "deferred": bool, "stopped": [{"item": "...", "run": "...", "round": 2}], "unstaffed": ["..."], "stayedUnstaffed": [{"item": "...", "state": "awaiting_close"}], "stayedUnstaffedUnread": true, "owed": ["..."]}`; lists empty, never null; `stayedUnstaffedUnread` left out when false, and `owed` when `deferred`; an item of `stayedUnstaffed` that could not be read has the state `""` (amended in execution, 2026-11-03: Task 13's flag, as Task 19 maps it) |

A preview answers `200` with `{"preview": true, "consequences": {...}}` and records nothing; the kill switch's also gives `"seq"`. `consequences` holds only what applies: `stops`, each `{"item", "run", "round", "reason"}`; `starts`, `closes` and `unstaffed`, item ids; `closesAt`, or `autoClosePaused`; `investigating` and `limit`, where a limit applies, as it does to every act on an item and not to the kill switch (amended in execution, 2026-11-03: as Task 19 built it).

`stayedUnstaffed` is computed whenever the answer's own off steps ran, that is, whenever the answer turned the view off, which can be an answer with `changed: false`, as an off that reveals a lost on; it is empty, and not computed, only when the view was already off and no on-row was raised, as on a retry. `stayedUnstaffedUnread: true` says that the list could not be read in full: the list itself, and it is empty, or an item of it, whose state is then `""`. The off has happened all the same (amended in execution, 2026-11-02: Task 13's ruling, keyed on the off's steps, not on `changed`).

Refusals of these routes, each `{"error": "...", "code": "..."}` (R15):

| Status | Codes | From |
|---|---|---|
| 400 | `invalid`: the body, the key, a reason's form, or a person's text refused; `reason_required` | the route's own checks; `app.ErrInvalid`; `Refused{reason_required}` |
| 404 | `no_such_item` | `app.ErrNoItem` |
| 409 | `closed`, `reopen_window_passed`, `limit_reached`, `kill_switch_on`, `daily_cap_reached`, `admin_only` | `Refused{code}` |
| 409 | `key_reused` | `app.ErrKeyReused` |
| 409 | `stale` | `app.ErrStale` |
| 409 | `held_by_later_build` | `app.ErrSkipped`, `item.ErrVersion`, `board.ErrVersion` |
| 409 | `not_held`, `draining` | `app.ErrNotHeld` or `app.ErrLeaseLost`, by the leader's status, as openings'; their texts say that the act is not confirmed, and what to do on the instance that leads next: read the item, or send the act again with its key; read the kill switch, and send the request again with the seq read as `after`. Openings keep their texts (amended in execution, 2026-11-03: an act fenced or drained after its commit answers `ErrNotHeld`) |
| 413 | `too_large` | the body's cap |
| 503 | `taking_over`, with `Retry-After: 1` | `app.ErrNotHeld` while the leader holds and does not drain, as openings' |
| 503 | `unavailable` | any other error, a lost answer the act could not check yet included, and `app.ErrUnsettled`, a promotion while a kill switch write's outcome is unknown (R37); logged with the item, never quoting the request |

The kill switch's on, on `ErrNotHeld`, follows Decision 5's routing first: `RecordKillSwitchOn` when the leader holds and drains; `503` with `Retry-After: 1` when it holds and does not drain; `409` `not_held` otherwise. Its off during a drain answers `409` `draining`. A preview answered `ErrNotHeld` never reaches `RecordKillSwitchOn`: it answers `409` `draining` when the leader holds and drains, `503` `taking_over` with `Retry-After: 1` when it holds and does not drain, and `409` `not_held` otherwise (amended in execution, 2026-11-03: Task 19's ruling, a fenced coordinator holding no account to preview from).

`GET /v1/items/{id}` gains these fields, each only where it applies:

```json
{"incident": "...", "type": "incident", "openedAt": "...",
 "queue": {"position": 2},
 "notStaffed": "kill_switch",
 "closesAt": "...", "autoClosePaused": false,
 "closed": {"at": "...", "by": "admin", "auto": false, "reopenableUntil": "...", "lateRulingsUntil": "..."}}
```

`incident`, `type` and `openedAt` are every item's; `queue` a waiting item's (R17); `notStaffed` an item's that awaits its close not staffed (R18); `autoClosePaused` every item's that awaits its close, unless the kill switch's table cannot be read (R28), and `closesAt` such an item's while not paused and its delay can be read; `closed` a closed item's (amended in execution, 2026-11-03: R28's case, which `View.AutoCloseUnread` marks, C16). `state` may now be `closed`. Times are RFC 3339 with nanoseconds, in UTC.

`GET /v1/status` gains `"killSwitch": {"on": false, "seq": 4}`, read from the table, and `"queue": {"limit": 3, "investigating": 2, "queued": 0}`, from the coordinator's snapshot, where `investigating` counts as the limit counts, admitted and promoted items that have not started included, and `queued` counts the queued items that are neither. A standby's queue is the empty snapshot's, all zeros. A kill switch table that cannot be read answers `503`, logged (R28).

### C9 (amended). The harness's API (Task 20 adds to it)

```go
// package harness (test/e2e/harness), under tag e2e

// DeploymentSpec gains Lifecycle Lifecycle (Investigating int; ReopenWindow,
// ClosedRulings time.Duration): its keys left zero are left out of the file,
// and serve's defaults apply. WorkTypeSpec gains AutoClose time.Duration:
// spec.autoClose.after, left out when zero, and the platform's 30 min applies.

// The answers' types mirror C4's JSON field for field, under C4's JSON
// names: Reason; ActBody (Reason, Preview), whose zero value sends no body;
// Consequences; StoppedRun; Acted (Changed, Preview, Consequences, Item);
// KillSwitchState; KillSwitchAnswer; UnstaffedItem. Each answer type also
// carries a refusal's Error and Code, and Acted and KillSwitchAnswer the
// Retry-After header, RetryAfter, tagged json:"-". ItemView gains C4's new
// item fields, and InstanceStatus its killSwitch and queue, the same way.

// Act sends POST /v1/items/{id}/{act} under key ("" sends no key), and
// returns the answer's status and body; Promote, Close and Reopen send an
// act with no body.
func (a *Admin) Act(t *testing.T, id, act, key string, body ActBody) (int, Acted)
func (a *Admin) Promote(t *testing.T, id, key string) (int, Acted)
func (a *Admin) Close(t *testing.T, id, key string) (int, Acted)
func (a *Admin) Reopen(t *testing.T, id, key string) (int, Acted)
// KillSwitch sends GET /v1/kill-switch; SetKillSwitch sends POST
// /v1/kill-switch, and a nil reason sends none.
func (a *Admin) KillSwitch(t *testing.T) (int, KillSwitchState)
func (a *Admin) SetKillSwitch(t *testing.T, on bool, after int64, reason *Reason) (int, KillSwitchAnswer)
```

### C16. The use cases the human interface calls (`internal/work/app`; `internal/work/control`)

```go
// package control (internal/work/control): a new domain package, as pure as
// item and board, so that the store returns its types without importing
// internal/work/app, which re-exports both, as it re-exports
// holding.ErrLeaseLost (Task 11).

// KillSwitch is the kill switch's state: its latest row, and what the rules
// read of the rows before it (Decision 5).
type KillSwitch struct {
	On     bool
	Seq    int64        // the latest row's number, 0 before the first
	OnSeq  int64        // the latest on-row's number, 0 before the first on
	OffAt  time.Time    // the latest off-row's time, zero before the first off
	By     string       // who wrote the latest row
	Reason board.Reason // the latest row's reason, its text redacted
	Since  time.Time    // the latest row's time
}

// ErrStale marks a request to the kill switch whose After is not the latest
// row's number, while the state it asks does not hold. The store returns it
// with the state, which the coordinator's view follows.
var ErrStale = errors.New("control: the kill switch has a newer row")

// package app (internal/work/app)

type KillSwitch = control.KillSwitch // Task 11
var ErrStale = control.ErrStale      // Task 11

// Actor is who acts: the person Plan 9a signs in, or "admin" through the
// admin API (Task 5).
type Actor struct {
	Subject string
	Admin   bool
}

// Act is a person's request: who, why, what makes a retry safe, and whether
// to act or only to show the consequences (Task 5).
type Act struct {
	Actor   Actor
	Key     string        // the Idempotency-Key of an act on an item; empty for a preview
	After   int64         // the kill switch's latest row the sender saw; the kill switch only
	Reason  *board.Reason // the act's one reason, checked by PersonText (Task 7)
	Preview bool
}

// Outcome is what an act on an item did, or would do (Task 5).
type Outcome struct {
	Changed      bool
	Consequences Consequences
}

// Consequences are an act's effects in codes, ids and numbers (C4). 7b-2
// adds Budget, Resumes and Incident.
type Consequences struct {
	Stops           []RunStop
	Starts          []item.ID
	Closes          []item.ID
	Unstaffed       []item.ID
	ClosesAt        time.Time // zero when it does not apply
	AutoClosePaused bool
	Investigating   int
	Limit           int
}

// RunStop is a run an act stops, or would stop.
type RunStop struct {
	Item   item.ID
	Run    board.RunID
	Round  int
	Reason item.StopReason
}

// Refused carries the code of an act the state forbids; errors.As finds it
// (Task 5).
type Refused struct{ Code string }
func (r *Refused) Error() string

// The codes of Refused (C4).
const (
	CodeClosed             = "closed"
	CodeReopenWindowPassed = "reopen_window_passed"
	CodeLimitReached       = "limit_reached"
	CodeKillSwitchOn       = "kill_switch_on"
	CodeDailyCapReached    = "daily_cap_reached"
	CodeAdminOnly          = "admin_only"
	CodeReasonRequired     = "reason_required"
)

// An act whose key named another act on the item fails with ErrKeyReused,
// whose doc now says so too.

// ErrUnsettled refuses a promotion while a kill switch write's outcome is
// unknown and the view is off: the switch may be on (R37; Task 14). The admin
// API answers it 503, unavailable.
var ErrUnsettled = errors.New("app: the kill switch's state is not known yet")

func (c *Coordinator) Close(ctx context.Context, id item.ID, a *Act) (Outcome, error)   // Task 5
func (c *Coordinator) Reopen(ctx context.Context, id item.ID, a *Act) (Outcome, error)  // Task 8
func (c *Coordinator) Promote(ctx context.Context, id item.ID, a *Act) (Outcome, error) // Task 10

// KillSwitchOutcome is what a request to the kill switch did, or would do
// (C4; Task 12).
type KillSwitchOutcome struct {
	On              bool
	Seq             int64 // the latest row's number after it
	Changed         bool
	Deferred        bool // recorded during a drain; the next leader acts on it
	Stopped         []RunStop
	Unstaffed       []item.ID
	StayedUnstaffed []UnstaffedItem // at off: each item, and its state now
	// StayedUnstaffedUnread says the list could not be read in full: UnstaffedBy
	// failed, and no item is given, or an item's load failed, and that item keeps
	// its id with an empty state; logged at Error. The off happened all the same
	// (amended in execution, 2026-11-02: Task 13's ruling).
	StayedUnstaffedUnread bool
	Owed                  []item.ID // what the rule still acts on (Decision 5); nil when deferred
	Preview               bool
	Consequences          Consequences // a preview's
}

type UnstaffedItem struct {
	Item  item.ID
	State item.Status // empty when the item could not be read
}

func (c *Coordinator) SetKillSwitch(ctx context.Context, on bool, a *Act) (KillSwitchOutcome, error) // Task 12; the off's steps Task 13

// RecordKillSwitchOn records the kill switch's on during a drain, and acts
// on no run (Decision 5; Task 16). It takes the coordinator's lock only to
// fence it if the drain has not yet, and writes after releasing it, under a
// context of its own: ctx's values without its cancellation, epoch, the
// draining holding's, and Options.WriteTimeout. A failed write answers as
// unavailable, with no second read.
func (c *Coordinator) RecordKillSwitchOn(ctx context.Context, epoch int64, a *Act) (KillSwitchOutcome, error)

// KillSwitchSeen is the latest kill switch row the coordinator's view holds,
// read without a lock; RefreshKillSwitch reads the switch again under the
// lock and applies it, and does nothing while fenced (Task 15).
func (c *Coordinator) KillSwitchSeen() int64
func (c *Coordinator) RefreshKillSwitch(ctx context.Context)

// Lifecycle is the snapshot readers take without a lock (Task 9; Task 12
// adds KillSwitch, the view: On, Seq, OnSeq and OffAt; Task 14 adds
// KillSwitchUnsettled).
type Lifecycle struct {
	KillSwitch          KillSwitch
	KillSwitchUnsettled bool // a kill switch write's outcome is unknown: no work starts (R37)
	Limit               int
	Investigating       int       // as the limit counts: admitted and promoted items not started included
	Waiting             []item.ID // queued items neither admitted nor promoted, in the queue's order
}
func (c *Coordinator) Lifecycle() Lifecycle

// PersonText checks and redacts a person's text, as the board's snapshot and
// the records hold it (Decision 9; Task 7).
func PersonText(red *redact.Redactor, field, text string) (string, error)

// Options gains Limit int (Task 9), ReopenWindow time.Duration (Task 8) and
// WriteTimeout time.Duration (Task 16, the database's transaction timeout);
// NewCoordinator refuses each below 1 or not above zero.

// Ports gains UpstreamSessions (Task 5) and Controls (Task 12); NewCoordinator
// refuses a coordinator without either.

// UpstreamSessions closes a run's upstream MCP sessions when it stops, in
// the background, never waiting (Decision 3; Task 5).
type UpstreamSessions interface {
	CloseRun(run board.RunID)
}

// Controls is the coordinator's port to the platform-wide controls (Task 11).
// SetKillSwitch returns the switch's state after it, and whether it appended
// a row, whatever it did: with ErrStale, the state is returned too. Its
// guard's refusal wraps ErrLeaseLost.
type Controls interface {
	KillSwitch(ctx context.Context) (KillSwitch, error)
	SetKillSwitch(ctx context.Context, on bool, by string, reason board.Reason, after int64) (state KillSwitch, changed bool, err error)
}

// KillSwitchRows is the leader's port for its renewal check: the number of
// the kill switch's latest row, 0 before the first (Task 15). LeaderOptions
// gains KillSwitch KillSwitchRows; nil reads nothing.
type KillSwitchRows interface {
	LatestKillSwitch(ctx context.Context) (int64, error)
}

// Records gains, beside Load, Active and Append (Task 11):
//   UnstaffedBy(ctx context.Context, seq int64) ([]item.ID, error)
// the items whose item_awaiting_close names the kill switch's row seq.
// Active now returns the items that are not closed (Task 6). Append refuses
// an automatic close that it would stamp before its due, with
// item.ErrNotDue, writing nothing (R41; Task 6).

// The views (Task 19).
type ViewPorts struct {
	Lifecycle func() Lifecycle // the coordinator's snapshot; a standby's is empty
	// Controls is the kill switch's table, its reads alone: serve gives the
	// views the table unguarded, so that they have no way to write it
	// (amended in execution, 2026-11-03: the final review's M1; it was a
	// Controls, which writes too).
	Controls interface {
		KillSwitch(ctx context.Context) (KillSwitch, error)
	}
	Revisions     Revisions        // for a delay recorded as zero
	ReopenWindow  time.Duration
	ClosedRulings time.Duration
}
func NewViews(records Records, boards *Boards, p *ViewPorts) *Views
func (v *Views) Item(ctx context.Context, id item.ID) (View, error) // gains C4's fields
func (v *Views) KillSwitch(ctx context.Context) (KillSwitch, error)
// View gains Incident item.IncidentID, Type string, OpenedAt time.Time,
// QueuePosition int (0 when not waiting), NotStaffed item.Cause,
// ClosesAt time.Time, AutoClosePaused bool, AutoCloseUnread bool and
// Closed *ClosedView. AutoCloseUnread is R28's mark that the kill switch's
// table could not be read, which leaves closesAt and autoClosePaused out of
// C4's view: AutoClosePaused false with a zero ClosesAt cannot tell it from an
// auto-close not paused whose delay cannot be read (amended in execution,
// 2026-11-03: a field beyond this list, which Task 19 added for R28).
type ClosedView struct {
	At               time.Time
	By               string
	Auto             bool
	ReopenableUntil  time.Time
	LateRulingsUntil time.Time
}

// package item
func (s *State) RulingsOpen(kind board.RulingKind, now time.Time, window time.Duration) bool // Task 2
// ErrNotDue refuses an automatic close stamped before its due (R41; Task 6).
var ErrNotDue = errors.New("item: an automatic close before its due")

// package config (internal/registry/config)
func Unseen(r rune) bool // Task 3
// DefaultAutoClose is spec Section 15's default delay of auto-close, the one
// a work item type's settings start from (Task 6; R43).
const DefaultAutoClose = 30 * time.Minute
```

Each act fails with `ErrNotHeld`, which it also answers once the coordinator's context has ended (Task 16), `ErrSkipped`, `ErrNoItem`, `ErrInvalid`, `ErrKeyReused`, `ErrStale`, `ErrUnsettled` (a promotion; Task 14), a `*Refused`, or what a store returned; the admin API maps each to C4.

### C17. Migration 0010 (Task 11 makes it)

- **`kill_switch`:** `tenant_id`; `seq`, from 1 for each tenant; `id`; `state`, `on` or `off`; `actor`; `reason_code`; `reason_text`, redacted, `''` when none; `recorded_at`, the database's `clock_timestamp()`. Its primary key is `(tenant_id, seq)`, and `id` is unique per tenant (R31).
- **As every audited table:** append-only (`audit_records_guard`, on update, delete and truncate), chained in form 1 with the scope `''`, its key `jsonb_build_array(seq)` and its row every column in order, `recorded_at` in microseconds, as migration 0007 renders `work_records`; its functions' `search_path` pinned as migrations 0005 and 0007 pin theirs. Verification gains it as a source, with the same expressions. The anchor owner's read-only role needs `SELECT` on it (the README says so).
- **Its `min_reader`** is its own level, 10: the file declares no older reader.

### C18. The board's additions (`internal/work/board`)

- **`kill_switch`** (Task 12): a notice the platform posts that names nothing, once per round, by the board's rule for repeats.
- **`skill_yanked`** (Task 3): a notice that names its agent and, in a new field, `skill`, the version (`name@version`), once per run, agent and version, whatever the round.
- **Both at body version 1.** Posting is strict, and replay as Plan 7d-2's Decision 7 says. An older build's `agentswarm conclude`, which decodes strictly, refuses a board that holds a `skill_yanked` notice, for its new field, and takes a `kill_switch` notice as a notice of a code it does not know; the docs say so.

### C19. The records 7b-1 adds (`internal/work/item`; Task 2 makes them)

| Kind | Body | Of |
|---|---|---|
| `item_promoted` | `by`, `killSwitchSeen`, `reason` (R13), `request` | the item |
| `item_closed` | `by` (empty when automatic), `auto`, `due` (automatic only), `reason`, `request` (a person's close only) | the item |
| `item_reopened` | `by`, `reason`, `request`, `awaitingClose` (the reason, cause and auto-close it returns to) | the item |

- `request` is `{"key": "sha256:<hex>", "digest": "sha256:<hex>"}` (`item.Request`).
- **Fields 7b-1 adds:** `item_opened.killSwitchSeen`, `item_started.killSwitchSeen` and `run_created.killSwitchSeen`; `run_stopped.killed`; `item_awaiting_close.autoClose`, `.cause` (`kill_switch`, `daily_cap`, `routing`, `type` or `closed`, for an item not staffed without a run) and `.unstaffedBy`. Each is left out of the JSON when zero.
- **Values 7b-1 adds:** `killed` and `closed`, stop reasons of runs; `stopped`, how a round ended; `closed`, an item's state, and an item's reason.
- **All at body version 1** (Decision 2).

```go
// package item
const (
	KindPromoted Kind = "item_promoted"
	KindClosed   Kind = "item_closed"
	KindReopened Kind = "item_reopened"
)
// Body gains Promoted *Promoted `json:"promoted,omitempty"`, Closed *Closed
// `json:"closed,omitempty"` and Reopened *Reopened `json:"reopened,omitempty"`.

type Request struct {
	Key    string `json:"key"`
	Digest string `json:"digest"`
}
type Promoted struct {
	By             string        `json:"by"`
	KillSwitchSeen int64         `json:"killSwitchSeen,omitempty"`
	Reason         *board.Reason `json:"reason,omitempty"`
	Request        Request       `json:"request"`
}
type Closed struct {
	By      string        `json:"by,omitempty"`
	Auto    bool          `json:"auto,omitempty"`
	Due     time.Time     `json:"due,omitzero"`
	Reason  *board.Reason `json:"reason,omitempty"`
	Request *Request      `json:"request,omitempty"`
}
type Reopened struct {
	By            string        `json:"by"`
	Reason        *board.Reason `json:"reason,omitempty"`
	Request       Request       `json:"request"`
	AwaitingClose AwaitingClose `json:"awaitingClose"`
}

// Cause says why an item with no run is not staffed.
type Cause string
const (
	CauseKillSwitch Cause = "kill_switch"
	CauseDailyCap   Cause = "daily_cap"
	CauseRouting    Cause = "routing"
	CauseType       Cause = "type"
	CauseClosed     Cause = "closed"
)
const (
	StopKilled     StopReason = "killed"
	StopClosed     StopReason = "closed"
	EndedByStopped EndedBy    = "stopped"
	StatusClosed   Status     = "closed"
)
// Opened, Started and RunCreated gain KillSwitchSeen int64
// `json:"killSwitchSeen,omitempty"`; RunStopped gains Killed int64
// `json:"killed,omitempty"`; AwaitingClose gains AutoClose time.Duration
// `json:"autoClose,omitempty"`, Cause Cause `json:"cause,omitempty"` and
// UnstaffedBy int64 `json:"unstaffedBy,omitempty"`.
```

## Earlier tests this plan changes

Each is changed in the task named, and the change is in that task's report; no test changes in Task 1's move. A test that the rule a task changes reaches, beyond this list, changes the same way, and the task's report names it.
- **Task 2:** `TestRecordsFoldInTheLifecyclesOrderOnly` gains its rows; its one row that reaches the rule it reverses, "a start after close", still holds, since an item awaiting its close takes a start only after a promotion.
- **Task 3** (carry 7: `Open` loads the item after any failed append of its opening):
  - `TestARetriedOpeningAfterATakeoverAdvancesAnItemWhoseOpeningLanded` (`restart_test.go`) fails as it stands: its first try's opening lands with its answer lost (`answerLost`), and `Open` now finds it, answers with no error and advances the item. Its first coordinator's records store now also fails the load that follows the lost answer, once (a one-shot `blinking` load of the item), so `Open` answers the error and arms its retry; the instance crashes before that retry fires (`h.crash()` at once, the clock unmoved). The rest stands: the takeover's blinking load cannot start the item, the retry by key starts it, and the writes are noted under epochs 1, 2, 2, 2, 2.
  - `TestARetryThatReadsBeforeItsOpeningLandsGetsTheItem` (`restart_test.go`) still passes, but would no longer test what its doc says: `Open`'s own load after the failed append now spends the one load `landingLate` hides the opening from, so the retry finds the item by its key and never meets the refused second opening. `landingLate` now hides the opening from the item's first two loads, `Open`'s own and the retry's lookup, so the retry's append is refused as a second opening and carry 7's load after it finds the item; the other request under the key still gets `ErrKeyReused`. The coverage of the refused second opening stays in this test.
- **Task 4:** `TestTheContractsExampleLoads` expects the lifecycle's defaults in the `Deployment` it compares.
- **Task 5:** the coordinator's test harness (`harness_test.go`): `h.ports()` gains an `UpstreamSessions` fake that notes each `CloseRun`, which every coordinator the tests make through it gets, `leader_test.go`'s included.
- **Task 6:** `TestActiveItemsAreThoseNotAwaitingClose` (`internal/work/postgres`) becomes `TestActiveItemsAreThoseNotClosed`, and `TestActiveItemsAreOpenedAndNotAwaitingClose` (`internal/work/postgres`, `internal/work/memory`) `TestActiveItemsAreOpenedAndNotClosed` (R16); `TestActiveItemsAndRunsComeFromTheRecords` (`internal/work/memory`): its `item-c`, which awaits its close, is active; `TestAnOpeningOfASkippedItemIsRefused` (`restart_test.go`): its second item, which awaits its close and holds a record of a later version, is now read at the takeover and skipped, so `Skipped` holds both items and `activeItems` returns both, and both openings are still refused with `ErrSkipped`; `TestAQuietRoundStopsTheRun`: one timer is left, the item's auto-close; the doc comments of the tests' `activeItems` helper, of `ErrSkipped` ("unless it awaits its close" becomes "unless it is closed"), of `TakeOver` ("carries on every item that does not await its close"), of `repeated` ("as an item that awaited its close at the takeover may") and of `Records.Active`.
- **Task 8:** the harness's options gain `ReopenWindow: 24 * time.Hour`.
- **Task 9:** the harness's options gain `Limit: 3`.
- **Task 11:** `TestEveryAuditedRowIsChained` and `TestARogueRecordLeavesAnchorsReadable` (`internal/audit/postgres`) count `kill_switch` among the audited tables, at 0; `TestEveryMigrationClassifiesAndThoseOnAnOlderReaderAreOnTheAllowlist` (`internal/db`) names it among the tables a trigger chains.
- **Task 12:** `h.ports()` gains the controls fake; `TestALaunchCancelledOrRefusedByTheHoldingPostsNothing` stands as it is, a guard: a cancelled launch on a run the coordinator still holds still arms its retry, and the drop, at every halt and at the kill switch, is what leaves no run to hold.
- **Task 16:** the harness's options gain `WriteTimeout: 10 * time.Second`.
- **Task 19:** `TestAnItemsViewFoldsItsRecordsAndReadsItsBoard` (`views_test.go`) makes its views with `NewViews`' new ports, and its expected `View` gains the item's incident, type and opening time; `TestAnItemsViewShowsItsRoundsEvidenceAndReports` (`internal/admin`) expects the view's `incident`, `type` and `openedAt`; `TestStatusShowsLeadingEpochSchemaDrainingAndSkipped` expects the status's `killSwitch` and `queue`; the admin API's fixture gives `admin.New` the new ports.

## Changes made during execution

Tasks 1 to 22 ran on main from 2026-11-02 to 2026-11-03, from c43bceb: each task by a fresh implementer, reviewed by a fresh reviewer on Opus 5.5, Task 12 by two, with a fix round where a review asked for one. The ledger keeps each ruling. What execution changed in this plan's text is amended in place, marked "(amended in execution, <date>: ...)", and R47 "(added in execution, ...)". Each amendment is listed here, one line each. The controller completes this record at the branch's end, after the final review.

- **R47, added** (Task 15, 9af2f43): a takeover sets the kill switch's view with its holding, and its own advance acts on it, not `applyKillSwitch`; Decision 5's list of what changes the view, C3, and the costs of R25 and R26 amended to match.
- **C3, `kill switch set`** (9af2f43, 2f0d746, 2e35ded, 5587e23; the Error line, 1326201): a takeover logs only when it acts (9af2f43); `stopped`, `unstaffed` and `owed` are left out when the on is deferred or the coordinator holds no account (2f0d746); `stopped` is a count of runs (2e35ded); an off's line gives `stayedUnstaffedUnread` (5587e23), and an unread list its line at Error (1326201).
- **Decision 5, C4 and C16, the off's list** (Task 13, 1326201, 5587e23 and c3d5e0c; Task 19, 5d73dd1): `StayedUnstaffedUnread`, mapped as `"stayedUnstaffedUnread": true`, left out when false; an item that could not be read has the state `""`; the list is computed whenever the answer's own off steps ran, keyed on that, not on `changed`.
- **Decision 5 and R37, an off whose write's outcome is unknown** (6351cd7): once a read settles the view, its list is read after the settling advance; that advance's stops reach an answer only while the view is on.
- **Decision 5 and R37, an on whose write's outcome is unknown** (dd37596, 7f1e83e): its answer names among its stops the runs the settling advance halted, and tells each item that owed the kill by its state once the advance is done, so that a run whose creation landed with its answer lost is named among the stops.
- **C4, Decision 5 and Decision 14, as Task 19 built them** (5d73dd1): `on` required as well as `after`, a preview's too, whose `after` is never compared; `reason` left out of the switch's state when its row has none; `investigating` and `limit` only where a limit applies; a preview answered `ErrNotHeld` never reaches `RecordKillSwitchOn`; the new routes' `409` `draining` and `not_held` say that the act is not confirmed, where openings keep their texts.
- **C16, `View.AutoCloseUnread`, and C4's item view** (5d73dd1): R28's mark that the kill switch's table could not be read, a field beyond C16's list; C4's view then leaves `autoClosePaused` out, as it leaves `closesAt`.
- **R41 and Decision 8, an auto-close timer that fires early** (fe231d4): it arms again at the deadline, but no sooner than a retry wait from now; its cost, an auto-close up to a retry wait late, is why scenarios 20 and 21 run serve with a retry of 500 ms.
- **Decision 4, the walk** (d1f4f2c): a read of an item outside an advance or a walk that finds it no longer counts toward the limit arms one walk at once on the timers; an item that leaves the account unreadable, or skipped by its round's settling, arms none, a gap carried to the final review.
- **Decision 3, the drop, and Decision 6, a close that ends no longer held** (ec926ad, 8155f65): the fence closes no upstream session, so it forgets the runs it fences, where a halt drops a run (Decisions 3 and 5, R47 and Task 16's test line say "forgotten"); a run halted or closed just as the holding ends is spec Section 8.2's residual, which Decision 3 points to; such a close drops its run, whatever came of it, unless the guard refused its append; a close that did not land changed nothing only while the coordinator still holds.
- **Decision 12, the drain** (5d73dd1): the new routes' `409` `draining` says that the act is not confirmed, as Decision 14 says, while openings keep their text.
- **Task 16's `TestTheDrainsFenceNeverWaitsOnTheDrainsOn`** (d00b013): it waits up to 10 s, failing with `t.Fatal`, not 50 ms.
- **Task 5's `TestCloseRunNeverWaits`** (955fd0e): `CloseRun` returns while the server still holds the `DELETE`, within 5 s, not 50 ms.
- **The interleaving test as built** (Tasks 17 and 18): the replay runs each seed four times in a world of three agents; the world has three revisions and a spend at the daily cap or not; `-interleave.seed` runs in the test's own mode; invariant 1 judges every event at whose end a view is on; invariant 6 is the first act that landed under a key, and assumes an act's record lands within its call; invariant 9 judges an admission by the record that took the item out of the queue; the default run takes about 7 s.
- **Task 9's test lines** (4c2e04c): `TestAnOpeningStartsAtOnceWhileASlotIsFree` is a guard; `TestAnAdmittedItemWhoseStartFailedOnceStartsOnItsRetry` fails as the test as built says.
- **R46, its timebox** (2026-11-03): it fired, Tasks 17 and 18 at about 4 to 6 days against a high end of about 3, with faults of one class still found; the controller took it to the author, and execution went on.
- **The size as executed** (the controller's judgment as of Task 22): about 3.5 to 4 person-weeks on the roadmap's yardstick, against the 2.95 to 3.25 accepted with this plan; the overrun is in the interleaving test and the faults it found. The roadmap's row and ADR-016's History record it.
- **Decisions 6 and 13, a close whose outcome is unknown** (the final review's fix wave, I1): such a close, its append failed and the lookup after it failed too, drops its run before it answers, as the kill's halt drops first; the item's retry carries a run whose close did not land on, as a takeover carries a run it does not hold, the drop's snapshot kept (R45). `carryOn` reports the unknown outcome; Task 7's `carryOn` and two of its test lines are amended, `TestARetriedCloseAfterALostAnswerStopsTheRunsTurns` is renamed `TestARetriedCloseAfterALostAnswerAnswersAsARepeat`, and `TestAFoundCloseWhoseAdvanceFailsStartsNoTurn` finds its key after the close's own lost answer, so that the found key's drop is the only one.
- **Decisions 4 and 13, a promotion whose outcome is unknown** (the final review's fix wave, I2): the account counts its item as promoted, and never as waiting, until the item's next load or record refreshes its entry (`entry.unsure`, `countAsPromoted`), so that a second promotion, or the walk, cannot take the items investigating past the limit and one meanwhile.
- **C16, the views' port** (the final review's fix wave, M1): `ViewPorts.Controls` reads the kill switch alone, so that the views, which serve gives the table unguarded, have no way to write it.
- **Decision 4, the walk after an item leaves the account** (the final review's fix wave, M2): `leave` arms the walk at once on the timers when the leaving item counted, outside an advance or a walk, which closes d1f4f2c's gap for an item that leaves the account unreadable, or skipped by its round's settling; the marker of d1f4f2c moves to the armed walk's sentence it belongs to.

## Not in this plan

- **Plan 7b-2:** a run's return and the incident: the board's `resume` entry; extensions and their budgets, `lifecycle.extension`; links and unlinks, with migration 0011; the item view's budget, incident items and extension offered; scenarios 24 and 25.
- **Plan 9a:** sign-in, roles per item and fresh authentication; the items list; the kill switch's control. **Plan 9b:** the board rulings' use case, its preview and route; every message.
- **Plan 7c-1:** inbound events, a trigger's resolution, flaps and correlation. **Plan 7c-2:** the reads at open.
- **Plan 8a:** shadow runs that outlive a close, the invisible arm, finality. **Plan 8b:** measurement's exclusions. Adoption, feedback and milestone rulings: 7b-1 gives them their window only.
- **Phase 1:** what the kill switch does to writes.
- A merge of whole incidents; the kill switch's on from a standby, carried to Plan 9a, where the author decides.
- The interleaving test's reach: it drives the coordinator and the leader against fakes, not the gateways, the Postgres stores, the runners or real time (R35, R36); each of those has tests of its own. Nor does it step the database's clock forward by the lease's margin or more, which breaks Plan 7d-1's lease, not 7b-1's rules (R39).
- Ending every session when a kill switch write's outcome is unknown, which would refuse the model calls of turns already running in that window, at the cost of cutting them for a write that may not have landed (R37's residual, the controller's to weigh).

## Constraints carried to later plans

The design's Section 11, addressed to each plan; the sweep's rows are named by their numbers in `plan7-carry-sweep.md`. The build order is 7b-1, 7b-2, 3c, 9a, 7c-1, 8a and 10a; Plans 7c-2, 8b, 9b and 10b wait for a team.

- **Plan 7b-2,** which starts from this plan's merged end, relies on these and on nothing else of 7b-1's, and changes no contract of 7b-1's but by adding to it (rows to the fold's table, a case to the owed-kill rule, a key to the block, codes to C4):
  1. the fold's four states and its table of what each state admits (Decision 2), to which 7b-2 adds `run_extended` (investigating and awaiting its close) and `item_linked` and `item_unlinked` (every state);
  2. `record` taking each record's incident from the fold inside the append (Decision 2), which 7b-2's link record relies on;
  3. `killSwitchSeen` and the rule that a run owes a kill (Decision 5), which 7b-2 extends with a run's latest resume;
  4. halt, the drop of a run's state, `finish`, the closed step and the close of a run's upstream sessions (Decisions 3 and 6), which a resumed run's later stops use unchanged;
  5. the act framework (Decision 13; C16): `Actor`, `Act`, `Outcome`, `Refused`, keys kept hashed in each act's record and scoped to its item (R42), previews, the carrying-on of an act whose answer was lost, and `PersonText`; and 7b-2's case of R10's residual: a late retry of a close that changed nothing, after a reopen and an extension that resumed the run, stops the resumed run mid-round (`TestALateCloseRetryAfterAReopenAndAResumeLeavesTheRunGoing` pins that a found close key drops nothing);
  6. the account of the items, with its admission marks, its snapshot, the queue's walk and `overLimit` (Decision 4), which 7b-2's resume calls;
  7. the kill switch's view in the coordinator (Decision 5), on which every extension is refused, and its mark of a write whose outcome is unknown (R37): while it is set, a resume, a start of work, waits as `begin` and the step do;
  8. Decision 8's rule that a timer made stale records nothing, on which a resume relies;
  9. Decision 15's `lifecycle` block, to which 7b-2 adds `extension`, and `closedRulings`, which its links read;
  10. the admin API's framework for acts (Decision 14): the key, strict decoding, the preview, the codes, the item view's fields; and the harness's helper for acts;
  11. migration 0010, at level 10; 7b-2's 0011 is at level 11, though an index alone could declare an older reader;
  12. the interleaving test (Tasks 17 and 18), which 7b-2 extends to its own acts: an extension, which resumes a stopped run, and a link and an unlink, among the acts its schedules pick; the owed-kill rule read from a run's latest resume in invariants 1 and 2; a resumed run's second stop, after its board's `resume` entry, in invariant 7; and a link's incident among the fold's checks of invariant 5.
- **Plan 3c:** Plan 5a:197 (sweep row 18, its check half): build the retrospective check's MCP client with `mcptools.Client` (`internal/measurement/check/toolbox.go` builds it with `mcp.NewClient` today).
- **Plan 9a:** the items list (the author's decision); its live board merges the item's records into its feed, since item rulings are recorded on the item (the author's decision); roles per item, and fresh authentication, above C16; sign-in for the admin API; the kill switch's control, responders and approvers turning it on, admins turning it off with fresh authentication, with its preset reasons; opening and promoting items from the items view; the kill switch's on from a standby, which the author decides there: the designer recommends it only if 9a's interface cannot route the on to the leader, with a standby recording an on-row without the holding, as spec Section 8.2 leaves hides unguarded because they only narrow, which the leader finds at its next renewal, and the off staying guarded, and an admin's.
- **Plan 7c-1:**
  - record a trigger's resolution so that `State.ResolvedAt` gives it, which auto-close reads (R19);
  - a trigger later correlated into an item a person opened makes the item follow that trigger's resolution, no longer "resolved at opening" (the author's decision);
  - resume a run on a new firing through 7b-2's resume and the board's `resume` entry, with a record of its own, the remaining budget, the cause `firing`, and `killSwitchSeen`; a firing resume waits for a free slot (the controller's call), the item staying awaiting its close, owed a resume, until admitted; the queue's items and the owed resumes share one order, by when each began to wait (the designer's call), and Decision 4's walk takes both; a rule for a firing resume of a run with no budget left;
  - link the new item a flap opens after a close to the previous one, with `item_linked`;
  - fold line breaks to spaces in the titles its mappings compose, before the opening: 7b-1 replaces whole a title that holds a character `config.Unseen` reports (Decision 11);
  - send inbound openings through the queue and the kill switch, as a person's are, with `killSwitchSeen`;
  - Plan 5b:200, its first half (sweep row 25): bind only trusted routing values as the session's `Item`, only a mapping's `trustedFields` reaching `Open`'s routing;
  - Plan 5b:14780 (sweep row 33): refuse a write's scope binding that resolves to `other` (`internal/actions/app/write.go`, `scope()`), with a test of the case `write_test.go` avoids;
  - write new record kinds at a new body version, or carry a migration at its own level (below).
- **Plan 8a:**
  - keep shadow-mode runs going after their item's close, which the fold must then admit;
  - halt every run of an item at the kill switch, shadow runs included;
  - read the close and reopen records, and the reopen window, for finality; an automatic close's `due` (R7);
  - Plan 5a:194 (sweep row 15): a session's class comes from its run's class, comparison and probation runs getting `limits.Shadow` and the LLM class `shadow`, and no class but primary and shadow issued, since `model_calls` takes no other (`internal/work/sessions` hard-codes `Primary` today);
  - Plan 5b:202 (sweep row 29): the invisible arm's notices stay on the run's board, which no person sees before its release; the coordinator's own notices, `kill_switch` and `skill_yanked` among them, land there too, so 8a hides those boards in Plan 9a's views until release.
- **Plan 10a:**
  - give `kill_switch` its retention class, beside the ruling records' [1 year];
  - the restore drill: after a restore, read the kill switch and the chain's lost rows of scope `''`, and turn the switch on again if an on was lost (spec Section 8.4's yank list survives a restore because it lives in Git, and the kill switch does not);
  - Plan 6a:217, 6a:7058 and 6a:7033 (sweep rows 37, 40 and 41), as one piece of work: migrations run as the tables' owner, and the platform as a role that does not own them; the chain's trigger functions `SECURITY DEFINER`, owned by the tables' owner; `INSERT` on `audit_chain` and `EXECUTE` on `audit_chain_append` revoked from the platform's role;
  - Plan 4:179 and 4:7905 (sweep row 6): an open item must not lose its locked revision because a newer build's rules refuse it; 7b-1's stricter rule for key values and routing (Decision 11) is the first such rule, which the README covers until then;
  - Plan 4:7906 (sweep row 7): run skill scripts without relying on an executable bit, the day a script tool is added;
  - Plan 6a:7060 (sweep row 43): add a field to the anchor form only if it is left out when empty, or version the form;
  - Plan 6a:7061, its third point (sweep row 46): a migration that chains existing rows sorts them by their keys, not their renderings: fix migration 0005's backfill before a first release, or state the rule for later backfills;
  - Plan 6b:7786 (sweep row 73): verify `inference_geo` with a live call before a deployment sets it;
  - Plan 5a:197 (sweep row 18, its runtime half): build the runtime's MCP client (`internal/runner/mcpclient/mcpclient.go`) with `mcptools.Client`;
  - to settle there (sweep rows 63 and 72), which the sweep found probably discharged: Plan 6b:230's second point, the call timeout against what a hung call may cost a run's rounds; and Plan 6b:7785, a live call that echoes an assistant text block that redaction changed to a `[redacted:...]` mark, beside an unchanged thinking block.
- **Plan 7c-2:** Plan 5b:199, its read half (sweep row 24): key values read at open refuse the characters `config.Unseen` reports, tab and line feed included, and `other`; Plan 5b:200, its second half (sweep row 26): build the platform reads' values with `app.Trusted`; Plan 5b:203 (sweep row 30): a platform read cited as platform evidence is one the coordinator made itself, never one with a `cause`; Plan 5b:182–184 (sweep row 34), with the diagnosis-only checks: record a failed consistency check on the item, so that it survives a takeover, and set `Session.DiagnosisOnly` from it; Plan 6a:216, its second half (sweep row 36): the platform reads made when an item opens carry its item and incident into the chain; write new kinds at a new body version, or carry a migration at its own level.
- **Plan 8b:** read 7b-2's link records to exclude cross-arm incidents; freeze the reopen window during a measurement window, or move it to reviewed configuration, since it lives in the deployment file, set per instance and unreviewed, and finality reads it; decide which stop the cutoff reads for a run that stopped more than once; read a run's own `killed` and `closed`; Plan 6b:7784 (sweep row 71): measure the agent smoke runs' refusals and withheld answers by reason.
- **Plan 9b:**
  - the board rulings' use case and its preview (the author's decision): it calls `PersonText` and `RulingsOpen` before `Boards.Rule`; takes the one reason of its input; takes an `Idempotency-Key`, kept hashed in the ruling's entry, or has an un-reject name the reject it lifts, so that a delayed reject cannot undo an un-reject (`TestARetriedRejectAfterAnUnrejectRejectsNothing`); answers `not_started` for a ruling on an item with no board; and previews the ruling's effect on the run as well as on the board, as spec Section 4.2's example does;
  - the item rulings in the interface: close, reopen, and 7b-2's extend, link and unlink, through C16;
  - its human posts and rulings check the item's state (`RulingsOpen`, and closed for human evidence), never only the board's stop, since a closed item's board may lack its stop entry;
  - the kill switch's messages, from the lists 7b-1 answers; the close-out message when an incident's last item closes;
  - the rulings' preset reasons; adoption, feedback and milestone rulings, through `RulingsOpen`, naming the late kinds there (R30); human evidence, through `PersonText`;
  - if its interface needs it, the item's latest record as each act's `after` (R10), weighing the residual's case in 7b-2.
- **Plan 10b:** measure the coordinator's lock under the kill switch's loop and the queue's walk, `Records.Active` over closes and reopens, `Records.UnstaffedBy`, and the leader's read of the kill switch at each renewal.
- **Every later plan:** a start of work a later plan adds, as 7c-1's firing resume or 8a's shadow and independent runs, waits while the coordinator's view of the kill switch is unsettled, as `begin`, the step and the walk do (R37). A plan that adds record kinds either writes them at a new body version, or carries a migration at its own level, which keeps every older build off the schema. Before the first release, Plan 7d-1's R12 keeps bodies at version 1, so such a plan carries a migration at its own level. Later plans that touch serve or a runner keep `make test-docker` in their gates (Plan 7d-2's rule, unchanged).

## Where each carry and decision lands

The design's Section 1.3 numbers the constraints earlier plans carried to 7b; 7b-1 takes all but carry 4 and the extension's half of carry 2, which are 7b-2's.

| # | From | Constraint | Decision | Task |
|---|---|---|---|---|
| 1 | 7a | Stop a run `killed` or `closed` mid-round: record the stop, then stop its turns, end its sessions and post its stop entry | 3, 6 | 2, 5, 12 |
| 2 | 7a | The queue, promotion, reopen and auto-close, with timers from the records | 4, 7, 8 | 6, 8, 9, 10 |
| 3 | 7a | Rulings on a closed item for [72 h]; a stopped board takes rulings only | 9 | 2, 7 |
| 5 | 7a's final review | `board.ErrStopped` from `Boards.Notice` counts as done when a run stops mid-round | 3 | 12 |
| 6 | 7a's final review | A person's reason checked as `agentText` checks an agent's text, alone and as its member | 9 | 7 |
| 7 | 7a's final review | After an append error, `Open` loads the item and advances it if its opening landed; `step` guarded against a state with no opening | 11 | 3 |
| 8 | 7a's final review | Routing checked again under the revision an item locks at its start | 4, 11 | 3 |
| 9, 11 | 7a's final review; 7d-1 | New record kinds keep older builds off their items: migration 0010 at its own level | 2 | 2, 11 |
| 10 | 7a's final review | The notice of a yank to each started item | 10 | 3 |
| 12 | 7d-1 | Platform-wide controls come with a migration whose `min_reader` is its own level | 5 | 11 |
| 13 | 7d-1 | The holding checked before every external request: the close of a run's upstream sessions | 3 | 5 |
| 14 | 7d-2 | The kill switch's stops through `Turns.Stop` and `StopAll`; its own notice is 7b's | 3, 5 | 12 |
| 15 | 7d-2 | `make test-docker` in the gates of plans that touch serve or a runner | — | the branch's end |
| 16 | 7d-2's final review | The turn's lifecycle out of `coordinator.go` by a pure move | 1 | 1 |
| 17 | sweep row 42 | `Deployment.Check` refuses a tenant `chain.ValidTenant` refuses | 15 | 4 |
| 18 | sweep row 16 | Serve refuses to start while `MCPGODEBUG` is set | 15 | 4 |
| 19 | sweep row 19 | A run's upstream MCP sessions close, in the background, when it stops | 3, 6 | 5 |
| 20 | sweep row 24, its inline half | Key values listed inline refuse characters a reader could miss | 11 | 3 |

Decisions by task: 1, Task 1; 2, Tasks 2 and 11; 3, Tasks 2, 5, 12 and 14; 4, Tasks 9, 10 and 14; 5, Tasks 11 to 16; 6, Tasks 5 and 7; 7, Task 8; 8, Tasks 2, 6, 8, 13 and 14; 9, Tasks 2, 7 and 12; 10 and 11, Task 3; 12, Tasks 6, 9, 12, 14, 15 and 16; 13, Tasks 5, 7, 8, 10 and 16; 14, Task 19; 15, Task 4, and serve's wiring in Tasks 5, 8, 9, 12, 15, 16 and 19. Tasks 19 to 21 exercise Decisions 4 to 8 and 12 through the admin API and end to end. The interleaving test, the author's decision of 2026-11-02, is Tasks 17 and 18. The controller's rulings of 2026-11-02: R37's mark, Task 14; R39's clock steps, Tasks 17 and 18; R40's order, Task 9; R41's auto-close on the database's time, Task 6; R42's key scoped to its item, Tasks 7 and 18. The plan review's resolutions: R43, Task 6; R44, Task 3; R45, Task 5; R46, Tasks 17 and 18.

## File Structure

| File | Task | Responsibility |
|---|---|---|
| `internal/work/app/turns.go`, `rounds.go` | 1–3, 12, 14, 17 | The turn's and the round's lifecycles, moved; `hold`'s yank notice and its copy of `killSwitchSeen`; `begin`'s guard, and its refusal while the view is unsettled |
| `internal/work/app/coordinator.go` | 1–3, 5, 6, 9, 10, 12, 14–17 | `Open`, `TakeOver`, the fence, `advance`, `step`'s order and its waits while the view is unsettled, `start`, `create`, `finish`, `record`; the options and ports |
| `internal/work/item/item.go`, `state.go` | 1, 2, 6, 12 | C19's records; the fold's states, times and table; `ErrNotDue`; `KillSwitchSeen` and `Owes` |
| `internal/work/item/halt.go`, `lifecycle.go` | 2, 12 | `Run.Halt`; `ResolvedAt`, `AutoCloseAt`, `RulingsOpen` |
| `internal/registry/config/names.go`, `worktype.go`; `internal/actions/app/write.go` | 3, 6 | `Unseen`; key values listed inline; the write path's rule; `DefaultAutoClose` |
| `internal/work/board/board.go`, `state.go`, `round.go` | 3, 12 | The notices `skill_yanked` and `kill_switch` |
| `internal/deploy/deploy.go`; `examples/deployment.yaml`, `examples/two-instances/a.yaml`, `b.yaml` | 4 | The lifecycle block, the tenant's form; the examples |
| `cmd/agentswarm/runner.go`, `serve.go` | 4, 5, 8, 9, 12, 15, 16, 19 | `checkEnvironment`; serve's wiring of each port, option and view |
| `internal/work/app/acts.go`, `close.go` | 5, 7, 16 | The act framework; `Close`, the halt's records, the drop, the closed step |
| `internal/actions/upstream/upstream.go` | 5 | `Pool.CloseRun` |
| `internal/work/app/autoclose.go` | 6, 13, 14, 17 | Auto-close's timers, their pause, and their new arming when they fire early |
| `internal/work/memory/records.go`, `internal/work/postgres/records.go` | 6, 11 | `Active` over closes and reopens; the refusal of an automatic close before its due; `UnstaffedBy` |
| `internal/work/app/reopen.go` | 8 | `Reopen` |
| `internal/work/app/queue.go` | 9, 12, 14 | The account, its order, the walk, `overLimit`, the snapshot |
| `internal/work/app/promote.go` | 10, 14 | `Promote` |
| `internal/db/migrations/0010_kill_switch.sql` | 11 | C17 |
| `internal/work/control/control.go` | 11 | `KillSwitch`, `ErrStale` |
| `internal/work/postgres/controls.go`, `internal/work/memory/controls.go` | 11 | The kill switch's stores |
| `internal/audit/postgres/chain.go` | 11 | Verification's new source |
| `internal/work/app/ports.go` | 5, 6, 11, 12, 15 | `UpstreamSessions`, `Controls`, `KillSwitchRows`, `Records.UnstaffedBy`, the aliases |
| `internal/work/app/killswitch.go` | 12–17 | `SetKillSwitch`, `applyKillSwitch`, the view and its mark, owed, the answers, the refresh, `RecordKillSwitchOn` |
| `internal/work/app/leader.go` | 15 | The renewal check and its refresh's timer |
| `internal/work/app/export_test.go` | 12 | The hooks the external tests call: the view set, and an item advanced |
| `internal/work/app/interleave_world_test.go`, `interleave_test.go` | 17, 18 | The interleaving test's world of fakes and clocks, its driver, schedules, flags and replay; the concurrent mode |
| `internal/work/app/interleave_invariants_test.go` | 18 | The interleaving test's invariants |
| `internal/work/app/views.go`; `internal/admin/admin.go`, `lifecycle.go` | 19 | The views' fields; the lifecycle's routes, the status |
| `test/e2e/harness/admin.go`, `config.go`, `deployment.go` | 20 | C9's additions |
| `test/e2e/lifecycle_test.go`, `killswitch_test.go` | 20, 21 | Scenarios 19 to 23 |
| `plan/...`, `README.md` | 22 | The spec's amendments, ADR-004, the roadmap, Plan 7d-1's note, the README |

Test files: the coordinator's new tests go in files of their concern beside `coordinator_test.go`, which holds 1,802 lines: `close_test.go`, `autoclose_test.go`, `acts_test.go`, `reopen_test.go`, `queue_test.go`, `promote_test.go`, `killswitch_test.go`, `killswitch_answers_test.go` and `killswitch_drain_test.go`, and the interleaving test's `interleave_world_test.go`, `interleave_test.go` and `interleave_invariants_test.go`; the tests the design places "in restart" go in `restart_test.go`, the leader's in `leader_test.go`, and those that build a coordinator by its fields in `coordinator_internal_test.go`, on small fakes of their own, since `internal/work/memory` imports the package; `export_test.go` holds the hooks through which the external tests reach a state no public path makes.

---

## Tasks

Each task lists its tests first. A test's line says what it asserts and why it fails before the implementation; "a build failure" is the expected first failure only where the test calls a name that does not exist yet. A test marked a guard passes at Step 1, and the line says why. Unless a line names another, a task's tests are unit tests of the coordinator's package, `internal/work/app`, on the memory stores, with the harness's clock moved by hand (`harness_test.go`). Commands run from the repository's root.

### Task 1: The pure moves and the rename

**Decisions:** 1. **Contracts:** none.

**Files:**
- Create: `internal/work/app/turns.go`, `internal/work/app/rounds.go`
- Modify: `internal/work/app/coordinator.go` (what moves out; then the rename of `close`), `internal/work/item/state.go` (the rename of `close`)

**Interfaces:**
- Consumes: the code at HEAD.
- Produces: the same package, laid out as Decision 1 says; `func (c *Coordinator) finish(ctx context.Context, s *item.State) error`, which was `close`; `func (s *State) awaitClose(c *AwaitingClose) error`, which was `close`.

**Notes for the implementer:** the throwaway program lives outside the repository, in the session's scratchpad. It parses the package's non-test files with `go/parser`, comments kept, and prints, sorted by name, each top-level declaration's name and source text, doc comment included: a method under its receiver's type and its name, a grouped `const`, `var` or `type` block split one name each. Its output at HEAD and after the move must be the same, byte for byte. The rename changes the two functions, their callers, and the doc comments that name them, nothing else.

- [ ] **Step 1: Write the throwaway program;** run it at HEAD and save its output; run `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`.
- [ ] **Step 2: Move** Decision 1's declarations to `turns.go` and `rounds.go`, unchanged, and fix the import lists.
- [ ] **Step 3: Prove the move pure.** Run the program again and `diff` its output with the saved one: no difference. `git status --short` names `coordinator.go` modified and `turns.go` and `rounds.go` new, alone, and no `_test.go` (`git diff --stat` lists no file not yet tracked). `go test -race -count=1 ./internal/work/...`: every package `ok`. `make lint`: `0 issues.` twice.
- [ ] **Step 4: Commit the move:** `git add internal/work/app/coordinator.go internal/work/app/turns.go internal/work/app/rounds.go`; `git commit -m "refactor(work): the turn's lifecycle to turns.go and the round's to rounds.go, a pure move"`.
- [ ] **Step 5: Rename, gate and commit.** Rename the step `close` to `finish` and the fold's `close` to `awaitClose`; run `go test -race -count=1 ./internal/work/...` and `make lint`. Expected: every package `ok`; `0 issues.` twice. Commit: `git add internal/work/app internal/work/item/state.go`; `git commit -m "refactor(work): the step close becomes finish, and the fold's close awaitClose"`.

### Task 2: The item's four states, its records, and the records of a halt

**Decisions:** 2, 3 (the item's half: `Halt` and the fold's rules of a stop), 8 (the deadline's rule, R19), 9 (`RulingsOpen`, R30). **Contracts:** C19; C16 (`RulingsOpen`).

**Files:**
- Modify: `internal/work/item/item.go` (C19's kinds, bodies, fields, causes and values; `Request`; `Ending.KilledBy`; `ItemReason`; `Record.Seq`'s comment, which says it orders a tenant's records, where it counts within an item), `state.go` (the statuses, the times, the table of what each state admits, the fold of the new kinds)
- Create: `internal/work/item/halt.go` (`Run.Halt`), `lifecycle.go` (`Running`, `ResolvedAt`, `AutoCloseAt`, `RulingsOpen`), `halt_test.go`, `halt_internal_test.go`, `lifecycle_test.go`
- Modify: `internal/work/item/state_test.go`, and `fixture_test.go` where its helpers need the new kinds
- Modify: `internal/work/app/coordinator.go` (`record`, and its callers there), `rounds.go` (its callers there after the move, `open` and `endRound`), `turns.go` where a caller lives there, `coordinator_internal_test.go` (with a small `Records` fake of its own, a history and an `Append` that calls `decide`: `internal/work/memory` imports this package, so an internal test cannot use it)

**Interfaces:**
- Consumes: Task 1's `awaitClose` and `finish`.
- Produces: C19's types, fields and values; and:

```go
// package item
// State gains:
//   Promotion *Promoted // the item's latest promotion
//   Closed    *Closed   // its latest close, while no reopen follows it
//   OpenedAt, AwaitingSince, ClosedAt time.Time // from the committed records' times
// Ending gains KilledBy int64: for a halt killed, the kill switch's on-row.
func (r *Run) Halt(reason StopReason, e *Ending) []Body
func (s *State) Running() bool         // it has a run that has not stopped
func (s *State) ResolvedAt() time.Time // in 7b-1, OpenedAt (R19)
func (s *State) AutoCloseAt(delay time.Duration, offAt time.Time) time.Time
func (s *State) RulingsOpen(kind board.RulingKind, now time.Time, window time.Duration) bool

// package app, unexported, which every later task records through
type rec struct {
	run  board.RunID // "" for a record of the item
	body item.Body
}
// record appends recs in one append, each checked against the fold of the
// history and of the drafts before it, each taking its incident from that
// fold (an opening, the incident s gives), and returns the item's state after
// the commit, folded from the history and the committed records with their
// times.
func (c *Coordinator) record(ctx context.Context, s *item.State, recs ...rec) (*item.State, error)
```

**Tests (write first):** each in `internal/work/item`, unless named.
- `TestRecordsFoldInTheLifecyclesOrderOnly` (changed): gains a row for each new kind in each state, admitted or refused, each failing alone when its rule is taken out: `item_promoted` taken on a queued item and on one awaiting its close with no run, refused on one awaiting its close after its run stopped, on an investigating item and on a closed one; `item_closed` taken on a queued item, on one awaiting its close, on an investigating item with no run, and on one whose run's stop is recorded, refused on an investigating item whose run runs and on a closed item, and, automatic, on an item that does not await its close; `item_reopened` taken on a closed item alone; a `round_ended` that ended `stopped` refused with any decision but to stop `killed` or `closed`; `item_awaiting_close` not staffed taken on a started item with no run. Fails first: a build failure, no new kinds.
- `TestAnItemsStatusFollowsItsRecords`: an item queued, investigating, awaiting its close, closed, awaiting its close again after a reopen; and one not staffed, then promoted, queued again: each `Status()` as Decision 2's table says.
- `TestAClosedItemTakesOnlyAReopen`: after `item_closed`, every other kind, a run's records included, is refused with `ErrInvalid`; `item_reopened` is taken, and the item awaits its close with the reason it carries.
- `TestAKilledOrClosedStopIsRefusedAfterADecidedStop`: a round that decided `quiet` takes `run_stopped` `quiet` alone: `killed` and `closed` are refused.
- `TestAStartedItemWithoutARunAwaitsItsCloseNotStaffed`: `item_opened`, `item_started`, then `item_awaiting_close` `not_staffed`, cause `kill_switch`, is taken, and the item awaits its close; with the reason `quiet` it is refused. Fails first: today's rule refuses an item started without a run.
- `TestTheFoldKnowsWhenAnItemOpenedAwaitedItsCloseAndClosed`: records with times: `OpenedAt` is the opening's; `AwaitingSince` the latest entry into `awaiting_close`, a reopen's included; `ClosedAt` the latest close's; a reopen clears `Closed`.
- `TestAHaltWithARoundOpenEndsTheRoundStoppedThenStopsTheRun` (`halt_test.go`): with round 1 open, `Halt(closed, e)`, `e` giving alpha `ended` and nothing of beta, alpha's turn end, a spend of 500 and the board's conclusion `h-1`, returns two bodies: a `round_ended` of round 1, `stopped`, alpha `ended` and beta `interrupted`, no failed call, `Settled` false, not quiet, no agent removed, nothing converged, `Rounds` 1, its wall clock to `e.EndedAt`, deciding to stop `closed`; then `run_stopped` `closed`, rounds 1, spend 500, `Settled` false, conclusion `h-1`. Both fold. Fails first: a build failure.
- `TestAHaltBeforeTheFirstRoundStopsTheRun`: a run with no round: `Halt(killed, e)` with `e.KilledBy` 3 returns `run_stopped` `killed`, `killed` 3, alone, and it folds.
- `TestAHaltAfterADecidedStopKeepsTheDecidedReason`: a round that decided `quiet`, its stop not recorded: `Halt(closed, e)` returns the `run_stopped` `quiet` that `Run.Stop` gives.
- `TestAStoppedRoundIsNeverQuietAndCountsAsUsed`: a normal round stopped with every agent's turn end and no entry is not quiet, and its `round_ended` counts it among the rounds used and its time to the stop in the wall clock.
- `TestAStoppedWrapUpRoundUsesUpAWrapUp` (`halt_internal_test.go`, `package item`): a wrap-up round stopped leaves the rounds and the wall clock as they were, and `wrapUps` and `wrapUpRounds` each count it.
- `TestAKilledRunLeavesItsItemNotStaffed`: after `Halt(killed)`'s records, `item_awaiting_close` `not_staffed` with `unstaffedBy` is taken, and with the reason `killed` refused; `ItemReason(StopKilled)` is `not_staffed` and `ItemReason(StopClosed)` is `closed`.
- `TestRulingsOpenFollowsTheSpecsListOnAClosedItem` (`lifecycle_test.go`): reject, un-reject and dismiss are open while the item is not closed and shut once it is; a late kind, `board.RulingKind("adoption")`, is open while not closed and until `ClosedAt` plus 72 h, shut after; a reopened item takes reject again.
- `TestAutoCloseCountsFromTheLatestEntryIntoAwaitingClose` (`lifecycle_test.go`): an item that awaited its close at T1, was promoted, and awaits it again at T2: `AutoCloseAt(30 min, zero)` is T2 plus 30 min; with an `offAt` after T2, `offAt` plus 30 min.
- `TestAnItemOpenedByAPersonClosesByItselfFromItsEntryIntoAwaitingClose` (`lifecycle_test.go`): `ResolvedAt()` is `OpenedAt`; `AutoCloseAt(30 min, zero)` is `AwaitingSince` plus 30 min, since the entry follows the opening.
- `TestARecordsIncidentIsTheFoldsInsideTheAppend` (`internal/work/app`, `coordinator_internal_test.go`, on its small `Records` fake): `record`, given a state whose `Incident` is `inc-stale` for an item whose history holds `inc-1`, appends records whose incident is `inc-1`, and returns the state after the commit, its times from the committed records. Fails first: `record` takes the caller's incident, and returns no state.

- [ ] **Step 1: Write the tests above, and change `TestRecordsFoldInTheLifecyclesOrderOnly`.** Run `go test -race -count=1 ./internal/work/item/ ./internal/work/app/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** C19's records, the fold's table, `Halt`, the lifecycle's functions, and `record`.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/item internal/work/app`; `git commit -m "feat(work): four states and 7b-1's records, the records of a halt, and each record's incident from the fold"`.

### Task 3: The carried fixes: openings, characters a reader could miss, and the yank notice

**Decisions:** 10, 11, R44. **Contracts:** C16 (`Unseen`), C18 (`skill_yanked`), C19 (the causes `daily_cap`, `type` and `routing`).

**Files:**
- Modify: `internal/registry/config/names.go` (`Unseen`), `hidden_test.go`, `worktype.go` (`checkValueList`), `worktype_test.go`
- Modify: `internal/actions/app/write.go` (`unseen` gives way to `config.Unseen`)
- Modify: `internal/work/app/coordinator.go` (`Open` after a failed append; the step's guard; `start`'s routing; `checkRouting`; `title`; the causes of `Open`'s and `start`'s not-staffed records), `rounds.go` (`hold`'s yank notice), `coordinator_test.go`, `restart_test.go`
- Modify: `internal/work/board/board.go` (`NoticeSkillYanked`, `Notice.Skill`), `state.go` (its shape, posted and replayed), `round.go` (its key for repeats); create `internal/work/board/yank_test.go`

**Interfaces:**
- Consumes: Task 2's causes, `record` and `rec`.
- Produces:

```go
// package config
// Unseen reports whether a reader could miss r: a control character, below
// U+0020 or U+007F, or one Hidden reports. Unlike Hidden it refuses tab, line
// feed and carriage return; like it, it lets every space separator through.
func Unseen(r rune) bool

// package board
NoticeSkillYanked NoticeCode = "skill_yanked"
// Notice gains Skill string `json:"skill,omitempty"`: a skill_yanked notice's
// version, name@version. A skill_yanked notice names its agent and its skill,
// and nothing else; it repeats by run, agent and skill, whatever the round.
```

**Notes for the implementer:** an opening at the daily cap now records the cause `daily_cap`, and a start whose type the revision cannot run the cause `type`. A yanked pin is found as the agent's skills in the locked revision (`Revision.Agent(name).Spec.Skills`, parsed with `config.ParseSkillRef`) that the current revision's `Yanked` reports. The retry `Open` arms after a load that failed is the item's own retry in `c.retries`, as `advance` keeps it.

**Tests (write first):**
- `TestAnOpeningWhoseCommitAnswerWasLostIsAdvanced` (`restart_test.go`; ported from Plan 7a's final review, probe G): a records store that loses the opening's answer alone, committing the item's first append and then answering an error, and answering every later append, unlike `answerLost`, which loses every answer and would leave the start to three retry waits. `Open` by key returns the item's id and no error, and the item starts: its kinds then hold `item_started`, `run_created` and `round_opened`, and its turns start. Fails first: `Open` returns the error and leaves the item waiting until the next takeover.
- `TestAKeylessOpeningWhoseCommitAnswerWasLostIsAdvanced` (`restart_test.go`): the same for an opening without a key: `Open` returns the id it made, and the item starts.
- `TestAStepOnAnItemWithNoOpeningWaits`: an opening whose append fails without landing, and whose load fails too: `Open` returns the error and arms its retry; the retry's load finds no record, and its advance waits: nothing is written, and the test does not panic. Fails first: `start` dereferences the missing opening.
- `TestAnItemWhoseRoutingTheLockedRevisionRefusesIsNotStaffed`: an item opened under a revision whose rules let its routing through, and started under one whose rule that applies always matches its routing value (`revisions.next`), records `item_awaiting_close` `not_staffed`, cause `routing`, and no `item_started`.
- `TestAKeyValueWithACharacterAReaderCouldMissIsRefused` (`internal/registry/config`): a type whose inline key values hold a bidi control (U+202E), a zero-width space (U+200B), a tab or a line feed is refused, each naming its value's path, `spec.key[0].values[i]`; a value with a space loads.
- `TestUnseenIsTheWritePathsRule` (`internal/registry/config`): for every rune of a table (each control, U+007F, tab, line feed, carriage return, U+200B, U+202E, U+FE0F, space, U+00A0, U+3000, letters), `Unseen` is the write path's rule, `r < ' ' || r == 0x7f || Hidden(r)`; tab, line feed and carriage return are unseen, and every space separator is not. Fails first: a build failure.
- `TestRoutingWithACharacterAReaderCouldMissIsRefused` (`coordinator_test.go`): the routing `{"service": "checkout\nservice=payments"}`, a tab in a key, and a zero-width space in a value are each refused with `ErrInvalid`, and nothing is recorded; `{"service": "check out"}` opens.
- `TestATitleWithACharacterAReaderCouldMissIsReplaced` (`coordinator_test.go`): the titles `checkout\tfails` and `a​b` are recorded as `[a title with characters a reader could miss]`; `checkout fails` as given; and `checkout fails\n`, whose line feed only trails it, as `checkout fails` (R44).
- `TestARoundOfAnItemWhoseLockedSkillIsYankedGetsANoticeOnce`: an item locks a revision where alpha pins `diagnosis@1.0.0`; the registry's current revision then yanks it: round 1's start posts one `skill_yanked` notice naming alpha and `diagnosis@1.0.0`; round 2's start posts none; beta, which pins nothing yanked, gets none.
- `TestNoYankNoticeWhileNoRevisionIsCurrent`: with no current revision at a round's start, no notice is posted, and the round's turns start.
- `TestAYankNoticeThatFailsStallsNoRound`: the board store fails the notice's append: the failure is logged as a failed step, and the round's turns start and its deadline is armed.
- `TestASkillYankedNoticeIsPostedOncePerRunAgentAndVersion` (`internal/work/board`, the plan's): `Notify` takes `skill_yanked` naming an agent and a skill; it refuses one without either, and one that gives a tool, a call or a reason; the same agent and skill in a later round is a repeat of the first entry; another version gets an entry of its own.
- The changed tests of "Earlier tests this plan changes", Task 3.

- [ ] **Step 1: Write the tests above, and change the earlier tests named for Task 3.** Run `go test -race -count=1 ./internal/registry/config/ ./internal/actions/app/ ./internal/work/...`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** `Unseen` and its uses, the openings' fixes, the causes, and the yank notice.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/registry/... ./internal/actions/... ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/registry/config internal/actions/app/write.go internal/work`; `git commit -m "fix(work): openings whose answer was lost, routing at the start, characters a reader could miss, and the yank notice"`.

### Task 4: The deployment's lifecycle block, its tenant, and MCPGODEBUG

**Decisions:** 15 (the block, the tenant, `MCPGODEBUG`, the examples). **Contracts:** C1, C3 (`serve refused to start`).

**Files:**
- Modify: `internal/deploy/deploy.go` (`Lifecycle`, its defaults, `Check`'s rules for it and for the tenant), `deploy_test.go`
- Modify: `examples/deployment.yaml`, `examples/two-instances/a.yaml`, `examples/two-instances/b.yaml` (the block, commented, between the lines `# >>> the lifecycle` and `# <<< the lifecycle`)
- Modify: `cmd/agentswarm/runner.go` (`checkEnvironment`), `serve.go` (`start` calls it), `serve_test.go`

**Interfaces:**
- Consumes: `chain.ValidTenant`.
- Produces: C1's `deploy.Lifecycle` and `Deployment.Lifecycle`; and:

```go
// package main (cmd/agentswarm)
// checkEnvironment refuses a non-empty MCPGODEBUG, naming it: the MCP SDK
// reads it once, at its init, and its flags change both gateways. start calls
// it with os.Getenv, beside checkSystem, before it opens the database.
func checkEnvironment(getenv func(string) string) error
```

**Tests (write first):**
- `TestTheLifecycleBlockTakesItsDefaults` (`internal/deploy`): `minimal()` loads with `Lifecycle{Investigating: 3, ReopenWindow: 24h, ClosedRulings: 72h}`; a block of `investigating: 1` alone keeps the windows' defaults. Fails first: `lifecycle` is not a key.
- `TestALifecycleLimitBelowOneIsRefused` (`internal/deploy`): 0 and -1 are each refused, naming `lifecycle.investigating`.
- `TestALifecycleWindowWithoutAUnitIsRefused` (`internal/deploy`): `reopenWindow: 24` and `closedRulings: "72"` are each refused naming their keys; `0s` is refused as not above zero.
- `TestADeploymentWhoseTenantCannotNameAnAnchorIsRefused` (`internal/deploy`): `Bad/Tenant`, `UPPER`, `-lead` and 64 characters are each refused, naming `tenant`; `example`, `it-0a1b2c3d` and 63 characters load. Fails first: `Check` takes any tenant that is not empty.
- `TestTheExamplesLifecycleBlocksLoad` (`internal/deploy`): `examples/deployment.yaml` with its lifecycle lines uncommented loads, each key with its value; `a.yaml` and `b.yaml` hold the same block, and their tenant and `deployment.yaml`'s are of the anchors' form.
- `TestServeRefusesToStartWhileMCPGODEBUGIsSet` (`cmd/agentswarm`): `checkEnvironment` refuses `x` and `jsonescaping=1`, naming `MCPGODEBUG` and never the value, and takes an unset or empty one. Through `runCommand("serve", ...)` on a deployment whose database names no server, with the environment injected by `t.Setenv("MCPGODEBUG", "x")`: serve exits 1, its `serve refused to start` line names `MCPGODEBUG`, and no listener was bound; with `t.Setenv("MCPGODEBUG", "")` the refusal is the database's. Fails first: a build failure, no `checkEnvironment`.
- `TestTheContractsExampleLoads` (changed): the `Deployment` it compares holds the lifecycle's defaults.

- [ ] **Step 1: Write the tests above, and change `TestTheContractsExampleLoads`.** Run `go test -race -count=1 ./internal/deploy/ ./cmd/agentswarm/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the block, the tenant's check, the examples, and `checkEnvironment`.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/deploy/... ./cmd/agentswarm/...`; `go test -race -count=1 -tags integration ./cmd/agentswarm/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/deploy examples cmd/agentswarm`; `git commit -m "feat(deploy): the lifecycle block, a tenant that can name anchors, and serve's refusal of MCPGODEBUG"`.

### Task 5: Close, the halt, and the drop of a run's state

**Decisions:** 3 (the coordinator's half; carries 1 and 19), 6 (but a lost answer, Task 7), 13 (the actor, the act, the outcome, the request on the record), R32, R45. **Contracts:** C16 (`Actor`, `Act`, `Outcome`, `Consequences`, `RunStop`, `Refused`, the codes, `UpstreamSessions`, `Close`), C19.

**Files:**
- Create: `internal/work/app/acts.go` (C16's act types and codes; `requestOf`; a reason's form), `close.go` (`Close`, `haltRecords`, `drop`, `closedStep`), `close_test.go`
- Modify: `internal/work/app/coordinator.go` (the step's order: no opening, then closed, then awaiting its close; `finish` calls `UpstreamSessions.CloseRun`; `skip` and the fence unchanged), `ports.go` (`UpstreamSessions`; `Ports` gains it, and `missing` requires it), `harness_test.go` (an `upstreams` fake noting each `CloseRun`; `h.ports()`)
- Modify: `internal/actions/upstream/upstream.go` (`CloseRun`), `upstream_test.go`
- Modify: `cmd/agentswarm/serve.go` (`UpstreamSessions: s.upstream`)

**Interfaces:**
- Consumes: Task 2's `Halt`, `Running`, C19, `record`; Task 1's `finish`.
- Produces: C16's names marked Task 5; and, for Tasks 6 to 16:

```go
// package app, unexported
// requestOf is an act's request (Decision 13): the key's hash, and the digest
// of the canonical JSON of its kind, its item and its reason's code.
func requestOf(kind string, id item.ID, a *Act) item.Request
// drop drops a run's state in memory (Decision 3): it keeps a snapshot of its
// turn classes in c.snapshots[run], as deadline would count them now, until
// the halt that reads it commits or the fence clears it (R45); it cancels
// its timers and deletes its entry, then ends its sessions, stops its latest
// round's turns and closes its upstream sessions. A run with no entry takes no
// snapshot, and the rest is done all the same.
func (c *Coordinator) drop(run board.RunID)
// haltRecords returns the records that stop s's run now, for reason (and
// killedBy, the on-row of a halt killed): the snapshot's classes, or the
// board's; the board's assessment and turn ends; the run's spend (R11).
func (c *Coordinator) haltRecords(ctx context.Context, s *item.State, reason item.StopReason, killedBy int64) []item.Body
// closedStep acts on a closed item: it drops its run, and posts, once and best
// effort, the stop entry its board lacks.
func (c *Coordinator) closedStep(ctx context.Context, s *item.State) error

// package upstream
// CloseRun lets go of every pooled session of run, of every agent and server,
// under the pool's lock, and closes each in the background once it is
// released, as Close does: never waiting, and a session whose holding has
// ended dropped unclosed.
func (p *Pool) CloseRun(run board.RunID)
```

**Notes for the implementer:** `Close` runs under the coordinator's lock; every write and halt it makes runs under `c.ctx`, which carries the term's epoch (Decision 3: a client that leaves cancels no halt). It answers `ErrNotHeld` when the coordinator is not held; `ErrSkipped` for an item this holding skips or whose records are of a later version; `ErrNoItem` for an id with no records; `ErrInvalid` for a missing or malformed key (R20) or a reason of another form. It records the act's `request` and its reason, whose form alone it checks in this task (R32). `Consequences.Investigating` and `Limit` stay zero until Task 9. `Close`'s key lookup, preview and lost answer are Task 7's.

**Tests (write first):**
- `TestClosingAQueuedItemClosesIt` (`close_test.go`): an item left queued, its start finding no revision: `Close` answers `changed: true`, records `item_closed` by `admin` with its request, and the item is closed; once a revision is current, it never starts. Fails first: a build failure, no `Close`.
- `TestClosingAnInvestigatingItemStopsItsRunMidRound`: in round 1, alpha's turn ended and beta's running: one append of a `round_ended` `stopped` (alpha `ended`, beta `interrupted`), `run_stopped` `closed` and `item_closed`; then the turns stop, the run's sessions end, `CloseRun` is called, and the board's stop entry says `closed`. The outcome: `changed`, `stops` holding the item, its run, round 1 and `closed`, `closes` holding the item.
- `TestClosingAnItemAwaitingItsCloseClosesIt`: an item whose run stopped `quiet`: `item_closed` alone.
- `TestClosingAClosedItemChangesNothing`: a second close answers `changed: false` and records nothing.
- `TestAnItemWhoseStopEntryCannotBePostedCanStillBeClosed`: a run stopped `quiet` whose board refuses its stop entry, so its `finish` keeps failing: `Close` records `item_closed`; the closed step's post fails, is logged once, and no retry of the item is armed.
- `TestClosingAnItemWhoseRunStoppedBeforeItsFinishEndedClosesIt`: `run_stopped` recorded, its `item_awaiting_close` append failing: `Close` records `item_closed` and no halt's records; the closed step posts the stop entry.
- `TestACloseEndsTheRunsSessionsTurnsAndUpstreamSessionsBeforeItForgetsTheRun`: the fakes see, in order, `Sessions.EndRun(run)`, `Turns.Stop` for each agent, then `CloseRun(run)`; afterwards an `Exited` and a `Launched` for the run's turns record nothing and arm no retry.
- `TestACloseWhoseAdvanceFailsStartsNoTurn` (the drop, on a public path): a close commits while a turn of its run waits for its start's retry (its `Start` failed), and the advance after the close fails at its load; the clock passes the retry wait, and `Start` is not called again. Fails without the drop: the retry armed before the close fires, and `begin`'s guard does not cover a close.
- `TestACloseAfterAStopWasDecidedKeepsTheDecidedReasonAndClosesTheItem`: a round whose end decided `quiet`, its `run_stopped` not recorded, as the board's assessment failed: `Close` records `run_stopped` `quiet` and `item_closed`.
- `TestAHaltWhoseBoardCannotBeReadStillRecords`: with the run's board failing every load, `Close` records the halt with an empty conclusion and the snapshot's classes.
- `TestAHaltWithoutASnapshotClassesItsTurnsFromTheBoard`: the run not held in memory, as after a takeover whose advance of the item failed at its load, with alpha's turn end on the board: `Close`'s halt classes alpha `ended` and beta `interrupted`.
- `TestARunsUpstreamSessionsCloseWhenItStops` (a table): a run stopped `quiet`, through `finish`, and a run closed mid-round: `CloseRun` is called for the run on each path.
- `TestARecordKeepsTheKeysHashNotTheKey`: `item_closed.request.key` is `sha256:` and the hex SHA-256 of the key; the record's JSON holds the key nowhere; the digest is the act's, and changes with the reason's code but not with its text.
- `TestCloseRunClosesThatRunsSessionsAlone` (`internal/actions/upstream`): with sessions pooled for two agents of `run-a` and one of `run-b`, `CloseRun("run-a")` makes the scripted server see a `DELETE` for each of `run-a`'s sessions; `run-b`'s next call uses its pooled session, with no new `initialize`. Fails first: a build failure.
- `TestCloseRunNeverWaits` (`internal/actions/upstream`): with a server that holds a session's `DELETE`, `CloseRun` returns while that `DELETE` is still out, which the test asserts; it waits up to 5 s for the return, a wait that only ends a `CloseRun` that hangs (amended in execution, 2026-11-02: a bound of 50 ms added no power, only the risk of a false failure under `make test-linux`, 955fd0e).

- [ ] **Step 1: Write the tests above, and give `h.ports()` the `upstreams` fake.** Run `go test -race -count=1 ./internal/work/app/ ./internal/actions/upstream/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the act types, `Close`, the halt's records, the drop, the closed step, `finish`'s close of the upstream sessions, `Pool.CloseRun`, and serve's port.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./internal/actions/upstream/... ./cmd/agentswarm/...`; `go test -race -count=1 -tags integration ./cmd/agentswarm/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app internal/actions/upstream cmd/agentswarm/serve.go`; `git commit -m "feat(work): close from any state, which halts a running run mid-round, drops its state and closes its upstream sessions"`.

### Task 6: Auto-close, and the takeover of items awaiting their close

**Decisions:** 8 (but its pause, Task 13), 12 (the items not closed), R41, R43. **Contracts:** C19 (`autoClose`; `item_closed` automatic), C16 (`Records.Append`'s refusal; `item.ErrNotDue`; `config.DefaultAutoClose`).

**Files:**
- Create: `internal/work/app/autoclose.go`, `autoclose_test.go`
- Modify: `internal/work/app/coordinator.go` (the step's arming for an item awaiting its close; the fence cancels the timers; `finish`, `start` and `Open` record `autoClose`; the doc comments of `ErrSkipped`, `TakeOver` and `repeated`), `ports.go` (`Records.Active`'s and `Records.Append`'s docs), `restart_test.go`, `coordinator_test.go` (`TestAQuietRoundStopsTheRun`; `activeItems`' doc), `harness_test.go` (its clock fires a timer before its time, as a correction of the clock back would)
- Modify: `internal/work/item/item.go` (`ErrNotDue`); `internal/registry/config/worktype.go` (`DefaultAutoClose`, the default `defaultSettings` already holds, exported for R43)
- Modify: `internal/work/memory/records.go` (`Active`; `Append`'s refusal of an automatic close before its due), `records_test.go`; `internal/work/postgres/records.go` (the same), `records_integration_test.go`

**Interfaces:**
- Consumes: Task 2's `AutoCloseAt`, `AwaitingSince`, `KindClosed`; Task 5's `record` of `item_closed` and the closed step.
- Produces, for Tasks 8, 12 and 13:

```go
// package app, unexported
// autoCloseDelay is s's delay: the one its latest awaiting_close or reopen
// recorded, or, when that is zero, the work item type's in the revision s
// locked, or the current one's for an item that never started.
func (c *Coordinator) autoCloseDelay(ctx context.Context, s *item.State) (time.Duration, error)
// armAutoClose arms s's timer at s.AutoCloseAt(delay, the kill switch view's
// off time, zero until Task 12 keeps a view), cancelling the one armed
// before; a delay it cannot read tries the arming again after Options.Retry.
// cancelAutoCloses cancels every timer.
func (c *Coordinator) armAutoClose(ctx context.Context, s *item.State)
func (c *Coordinator) cancelAutoCloses()
```

**Notes for the implementer:** the timer fires under the lock, loads the item, and records `item_closed` (`auto: true`, `due` its deadline, no `by` and no request) only while the item awaits its close and its deadline, computed again from its state now, has passed on the coordinator's clock; a timer that fires before that deadline arms itself again at it, and one the item's state made stale records nothing (R41). Each `item_awaiting_close` records the delay its writer can read, or zero. `Active`'s query keeps reading by kind, on `work_records_by_kind`: an item's opening, and no `item_closed` without an `item_reopened` of a later `seq`. Each records store's `Append` compares an automatic close's `due` with the time it stamps the record with, after its rule for an item's times (Postgres: the `recorded_at` that `insertRecord` returns, `greatest(clock_timestamp(), …)`), and, when the stamp is earlier, returns `item.ErrNotDue` and writes nothing, the transaction rolled back; the timer's record that fails so is tried again after `Options.Retry`, as any record that fails is.

**Tests (write first):**
- `TestAnItemAwaitingItsCloseClosesByItselfAfterItsDelay` (`autoclose_test.go`): a run stops `quiet` at T: its `item_awaiting_close` records `autoClose` 30 min; at T plus 30 min less 1 s nothing is recorded; at T plus 30 min, `item_closed`, `auto` true, `due` T plus 30 min, with no `by`; the item is closed. Fails first: nothing closes an item.
- `TestTheDelayIsTheOneRecordedWhenTheItemAwaitedItsClose`: the item awaits its close with 30 min recorded; the registry's current revision then sets the type's `autoClose.after` to 10 min: the item closes at 30 min, not 10.
- `TestADelayThatCannotBeReadIsTriedAgain`: an `item_awaiting_close` with no `autoClose`, as a build before 7b-1 wrote it, appended by the test, and the item's locked revision unreadable (`Revisions.At` failing): no timer is armed, and the arming is tried again after the retry wait; once the revision reads, the item closes at its deadline.
- `TestATakeoverCarriesOnAnItemAwaitingItsClose`: after a crash and a takeover, `Active` lists an item awaiting its close, and the takeover advances it, arming its auto-close.
- `TestATakeoverArmsTheAutoCloseOfEveryItemAwaitingItsClose` (`restart_test.go`): three items awaiting their close, with deadlines in the future: after a crash and a takeover, each closes at its own deadline.
- `TestAnAutoCloseDueWhileNoInstanceLedClosesAfterTheTakeover` (`restart_test.go`): an item's deadline passes while no instance leads: the takeover records no `item_closed` before it returns; the timer, firing just after, closes the item.
- `TestActiveItemsAreThoseNotClosed` (`internal/work/memory`, and `internal/work/postgres` as an integration test, replacing `TestActiveItemsAreThoseNotAwaitingClose` there; R16): of an item opened, one awaiting its close, one closed, and one closed and reopened, `Active` lists all but the closed one, by id.
- `TestAnAutoCloseTimerThatFiresBeforeItsDeadlineArmsAgain` (the plan's; R41): an item's timer fires while the coordinator's clock reads a minute before its deadline, as after a correction of that clock back since the arming (the harness fires it early): nothing is recorded, and a timer of the item is armed at the deadline, which closes it. Fails first: the early fire records nothing and arms nothing, and the item never closes by itself.
- `TestAnAutoCloseTheRecordsRefuseAsNotDueIsTriedAgain` (the plan's; R41): the records store refuses the timer's close once with `item.ErrNotDue`: after the retry wait the close is tried again and lands, once.
- `TestAnItemWhoseTypeIsGoneClosesByItselfAfterTheDefaultDelay` (the plan's; R43): an item whose first start fails (the revisions' lock failing once), and whose type the next current revision drops (`revisions.next`): its retried start records `item_awaiting_close` `not_staffed`, cause `type` (Task 3), with the delay zero, as no revision gives one. Its timer is armed at that entry plus the default 30 minutes, the fallback logged once, and the item closes then; no arming is tried again every retry wait. Fails first: the arming is tried again every retry wait, and the item never closes.
- `TestTheRecordsRefuseAnAutomaticCloseStampedBeforeItsDue` (`internal/work/memory`, and `internal/work/postgres` as an integration test; the plan's; R41): an automatic close whose `due` is an hour after the store's clock is refused, `item.ErrNotDue`, and the item's records are unchanged; one whose `due` has passed is taken; a person's close, which has no `due`, is taken at any time. Fails first: the early close is written.
- The changed tests of "Earlier tests this plan changes", Task 6.

- [ ] **Step 1: Write the tests above, and change the earlier tests named for Task 6.** Run `go test -race -count=1 ./internal/work/...` and `go test -race -count=1 -tags integration ./internal/work/postgres/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the delay's record and its fallback (R43), the timers and their new arming, and, in both stores, `Active` and the refusal of an automatic close before its due.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./internal/registry/config/`; `go test -race -count=1 -tags integration ./internal/work/postgres/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work internal/registry/config/worktype.go`; `git commit -m "feat(work): items awaiting their close close by themselves after their delay, never before it on the database's time, and a takeover carries them on"`.

### Task 7: Acts: keys, lost answers, previews, and a person's text

**Decisions:** 13, 6 (its lost answer), 9 (a person's text; `Boards.Rule`'s doc), R1, R42. **Contracts:** C16 (`PersonText`; `ErrKeyReused` for an act).

**Files:**
- Modify: `internal/work/app/acts.go` (the key lookup, the carrying-on after a failed append, the preview's path, reasons through `PersonText`), `close.go` (`Close` uses them), `boards.go` (`Rule`'s doc), `coordinator.go` (`ErrKeyReused`'s doc)
- Create: `internal/work/app/person.go` (`PersonText`), `acts_test.go`, `person_test.go`

**Interfaces:**
- Consumes: Task 5's `requestOf`, `drop`, `Close`; Task 2's `item.Request` on each act's record.
- Produces: `PersonText`; and, for Tasks 8, 10 and 12, the unexported helpers that every act calls:

```go
// package app, unexported
// findKey looks for req's key among s's records' requests, the item's own
// (R42): found with req's digest, the act's repeat; with another digest,
// ErrKeyReused.
func (c *Coordinator) findKey(s *item.State, req item.Request) (found bool, err error)
// carryOn checks an append that failed with err (Decision 13): it loads the
// item; found by req's key, it answers as a repeat and advances the item from
// its state now, a close dropping the run first when the item is closed now;
// not found, it returns err, which the admin API answers 503; the load
// failing, it arms a retry of the load and the carrying-on after
// Options.Retry, through the item's c.retries entry, returns err, and reports
// the act's outcome unknown, on which a close drops its run (amended in
// execution, 2026-11-03: the final review's I1).
func (c *Coordinator) carryOn(ctx context.Context, id item.ID, req item.Request, err error) (repeat, unknown bool, _ error)
// checkReason checks r's form, then its text through PersonText with the
// rules of s's locked revision, or the current one's; with neither, a text
// is refused and a code alone taken.
func (c *Coordinator) checkReason(s *item.State, r *board.Reason) (*board.Reason, error)
```

**Notes for the implementer:** a found key, or a failed append found to have landed, refreshes the item and advances it, and does nothing else on its own; one thing a close adds: when the item is closed now, it drops the run first (Decision 13). A preview runs the act's checks under the same lock, takes no key, skips the lookup, and returns its consequences with `Changed` false, recording and acting on nothing.

**Tests (write first):**
- `TestAKeyReusedForAnotherActIsRefused` (`acts_test.go`): a close of an item under key K, then a close of the same item under K with another reason's code, answers `ErrKeyReused` and records nothing. The same key on another item is that item's own act (R42): a close of a second item under K closes it, and records its own `item_closed`. Fails first: the second close of the first item answers `changed: false`, its key unread.
- `TestAnActRepeatedWithItsKeyAnswersAsItDid`: a close under K, repeated under K: `changed: true` both times (R14), and no record more.
- `TestAFoundKeyAdvancesTheItem`: a close under K whose closed step failed at the board; the board back, the close repeated under K advances the item: the stop entry is posted.
- `TestAnActWhoseLoadAfterAFailedAppendFailsIsTriedAgain`: a close's append commits and answers an error, and the load after it fails: `Close` answers the error, and drops the run, its turns stopped, as its outcome is unknown; after the retry wait the coordinator loads the item again, finds its `item_closed`, and the closed step posts the stop entry (amended in execution, 2026-11-03: the final review's I1, whose drop comes at the answer, where the retry made it).
- `TestACloseWhoseAppendFailsLeavesTheRunGoingOn`: a close's append fails without landing: `Close` answers the error; the run's turns go on, and its deadline still ends its round.
- `TestACloseWhoseCommitAnswerWasLostStopsTheRun`: a close's append commits and answers an error: `Close` loads the item, answers `changed: true` as a repeat, drops the run, and the closed step stops its turns and ends its sessions.
- `TestARetriedCloseAfterALostAnswerAnswersAsARepeat`: a close's append commits and answers an error, and its load fails: the person retries under the same key: the key is found, and the close answers as a repeat, its first try having dropped the run (amended in execution, 2026-11-03: the final review's I1 drops the run at the first try's answer, so the test, `TestARetriedCloseAfterALostAnswerStopsTheRunsTurns` until then, is renamed for what it pins).
- `TestAPreviewRecordsNothingAndActsOnNothing`: a close's preview of an item in round 1 answers the run's stop (the item, the run, round 1, `closed`) and the item's close; nothing is recorded; no `Stop`, `EndRun` or `CloseRun` is called; the round goes on.
- `TestAPreviewTakesNoKeyAndALaterActWithTheKeyActs`: a preview without a key is answered; a close under K after it closes the item.
- `TestAPersonsTextThatHidesAMatchBehindAnEscapeIsRefusedAndNotQuoted` (`person_test.go`): with a rule `sk-[a-z0-9]{8}`, `PersonText` refuses `sk-abcdefgh`, whose JSON escape decodes to `sk-abcdefgh`, with an error that wraps `ErrInvalid`, says why, and holds no part of the text. The plain `sk-abcdefgh` is not refused but redacted (R1), as `TestAPersonsReasonIsRedactedBeforeItIsRecorded` records it, and as `agentText` treats an agent's text. Fails first: a build failure.
- `TestAPersonsTextWhoseMemberHidesAMatchIsRefused` (`person_test.go`): with a rule `text=xxx` and the field `text`, `PersonText` refuses `xxx`. Alone it decodes to `xxx`, which the rule does not match; its member, `text=xxx`, decodes to `text=xxx`, which the rule matches, as the LLM gateway reads a member (`redact.EscapedValue`). The plain `xxx` is taken unchanged.
- `TestAPersonsTextWithAHiddenCharacterIsRefused` (`person_test.go`): a zero-width space is refused.
- `TestAPersonsReasonIsRedactedBeforeItIsRecorded`: a close whose reason's text holds `sk-abcdefgh` records `[redacted:api-keys]` in its place.
- `TestFreeTextWithNoConfigurationIsRefusedAndACodeAloneIsTaken`: an item that never started, with no current revision: a close whose reason has text answers `ErrInvalid` and records nothing; one with a code alone closes it.
- `TestAReasonOfAnotherFormIsRefused` (the plan's): the codes `Bad`, `1x` and 33 letters, and a text of 501 characters, are each refused with `ErrInvalid`, and nothing is recorded.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/app/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the key lookup, the carrying-on, the preview, `PersonText` and the reason's check, and `Close` through them.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app`; `git commit -m "feat(work): acts carry hashed keys, preview their consequences, carry on after a lost answer, and check a person's text"`.

### Task 8: Reopen

**Decisions:** 7, 8 (a reopen's delay, and a stale timer), 13 (a close's key found after a reopen), R12. **Contracts:** C16 (`Reopen`, `Options.ReopenWindow`), C19 (`item_reopened`).

**Files:**
- Create: `internal/work/app/reopen.go`, `reopen_test.go`
- Modify: `internal/work/app/coordinator.go` (`Options.ReopenWindow`; `NewCoordinator` refuses it not above zero), `harness_test.go` (the harness's options: `ReopenWindow: 24 * time.Hour`)
- Modify: `cmd/agentswarm/serve.go` (`ReopenWindow: d.Lifecycle.ReopenWindow`)

**Interfaces:**
- Consumes: Task 7's `findKey`, `carryOn` and `checkReason`; Task 6's `autoCloseDelay` and `armAutoClose`; Task 2's `ClosedAt`; Task 4's `deploy.Lifecycle`.
- Produces: `func (c *Coordinator) Reopen(ctx context.Context, id item.ID, a *Act) (Outcome, error)`; `Options.ReopenWindow time.Duration`.

**Notes for the implementer:** a reopen judges its window by `Clock.Now`, the database's time, against `ClosedAt`. Its `awaitingClose` carries the reason Decision 7 gives, the cause R12 gives for an item with no run, and the delay `autoCloseDelay` reads, or zero when it cannot. Its consequences give the item's `ClosesAt`. After the record it advances the item, whose step arms its auto-close.

**Tests (write first):** each in `reopen_test.go`, unless named.
- `TestAReopenWithinTheWindowReturnsTheItemToAwaitingClose`: an item that awaited its close `quiet`, closed by a person, is reopened within 24 h: `item_reopened` by `admin`, with its request and `awaitingClose` of reason `quiet` and `autoClose` 30 min; the item awaits its close; `changed: true`, and `closesAt` the reopen plus 30 min. Fails first: a build failure.
- `TestAReopenPastTheWindowIsRefused`: 24 h and 1 s after the close, `Refused{reopen_window_passed}`, and nothing is recorded.
- `TestTheReopenWindowIsJudgedInTheDatabasesTime`: the coordinator's clock, the database's, moved 24 h and 1 s past an item's close while the process's own time has moved only seconds: the reopen is refused; on another item, at 24 h less 1 s, it is taken.
- `TestReopeningAnItemThatIsNotClosedChangesNothing`: a reopen of an item awaiting its close answers `changed: false` and records nothing.
- `TestAReopenedItemClosesByItselfFromItsReopen`: an item closed at T and reopened at T plus 1 h closes by itself at T plus 1 h 30 min, not before.
- `TestAReopenWithNoRevisionRecordsTheDelayLater`: an item that never started, closed, then reopened while no revision is current: `item_reopened` records `autoClose` zero, and the reopen does not fail; once a revision is current, the timer reads the type's 30 min and the item closes 30 min after its reopen.
- `TestAReopenWhoseCommitAnswerWasLostArmsItsAutoClose`: a reopen's append commits and answers an error: `Reopen` answers as a repeat, and the item, advanced, closes by itself at its deadline.
- `TestAnAutoCloseArmedBeforeAReopenClosesNothing`: an item awaiting its close since T, its timer due at T plus 30 min, closed by a person at T plus 10 min and reopened at T plus 20 min: at T plus 30 min nothing is recorded; at T plus 50 min, `item_closed` automatic, once.
- `TestArmingAnAutoCloseAgainCancelsTheEarlier`: a reopen repeated under its key advances the item, which arms its auto-close again: one timer of the item is pending, and at its deadline one `item_closed` is recorded.
- `TestAFoundCloseKeyActsOnTheItemsStateNow` (a table): a close under K found while the item is closed drops the run, ending its sessions and closing its upstream sessions; the same close found after a reopen drops nothing (no `CloseRun`), stops nothing, and leaves the item awaiting its close, its auto-close armed.
- `TestARepeatedCloseAfterAReopenAnswersAsTheFirstAndClosesNothing`: a close under K1, a reopen under K2, then the close repeated under K1: it answers `changed: true`, as the first did, records nothing, and the item awaits its close.

- [ ] **Step 1: Write the tests above, and give the harness's options the reopen window.** Run `go test -race -count=1 ./internal/work/app/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** `Reopen` and its option, and serve's.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./cmd/agentswarm/...`; `go test -race -count=1 -tags integration ./cmd/agentswarm/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app cmd/agentswarm/serve.go`; `git commit -m "feat(work): a person reopens a closed item within its window, and it awaits its close again"`.

### Task 9: The queue: the account, the walk, and its admissions

**Decisions:** 4 (but promotion, Task 10), 12 (the takeover's account and walk; the step's start rule), R9, R17, R40. **Contracts:** C16 (`Lifecycle`, `Coordinator.Lifecycle`, `Options.Limit`), C19 (the cause `daily_cap` at a start).

**Files:**
- Create: `internal/work/app/queue.go` (the account, the walk, `overLimit`, the snapshot), `queue_test.go`
- Modify: `internal/work/app/coordinator.go` (`Options.Limit`, which `NewCoordinator` refuses below 1; every load and record refreshes the account; the walk at the end of an advance outside a walk or a takeover; the step starts a queued item only when admitted; `start`'s cap; `repeated` bounded by the queue; `TakeOver` rebuilds the account and walks once; the fence empties it), `close.go` (a close refreshes the account), `harness_test.go` (`Limit: 3`; its clock steps back, for R40's test), `restart_test.go`
- Modify: `cmd/agentswarm/serve.go` (`Limit: d.Lifecycle.Investigating`)

**Interfaces:**
- Consumes: Task 2's `record` and states; Tasks 5 and 7's close.
- Produces: C16's `Lifecycle` (without `KillSwitch`, which Task 12 adds), `Coordinator.Lifecycle()`, `Options.Limit`; and, for Tasks 10 and 12:

```go
// package app, unexported
// walk admits queued items, in their order, while the items investigating
// are fewer than the limit (Decision 4): each admitted item is marked, and
// advanced once.
func (c *Coordinator) walk(ctx context.Context)
// investigating counts the items investigating as the limit counts them;
// overLimit reports whether one more would pass the limit and one.
func (c *Coordinator) investigating() int
func (c *Coordinator) overLimit() bool
```

**Notes for the implementer:** an admission is a mark in the account, not a record. A refresh keeps it while the item stays queued, and drops it once the item starts or leaves the queue. The walk orders queued items by their rank, the opening record's time, then the item's id, at all times, not by when the account learned of them (R40). An item whose records do not fold, or are of a later version, is not in the account. `Consequences.Investigating` and `Limit` come from the account from here on. The snapshot is published after each change, through an `atomic.Pointer`, as `Skipped` is; a fenced coordinator's is empty.

**Tests (write first):** each in `queue_test.go`, unless named.
- `TestAnItemOverTheLimitWaitsQueued`: with `Limit` 1, the first item opened starts and the second records `item_opened` alone; `Lifecycle()` gives `Limit` 1, `Investigating` 1 and `Waiting` holding the second. Fails first: a build failure, no `Options.Limit`.
- `TestAnOpeningStartsAtOnceWhileASlotIsFree`: with one item investigating of 3, an opening's own call records its `item_started`, `run_created` and `round_opened`. A guard, which passes at Step 1: today's code starts every opening at once, which the queue must keep while a slot is free (amended in execution, 2026-11-02: the line did not mark it).
- `TestQueuedItemsStartInTheOrderTheyOpened`: with `Limit` 1, A investigating and B, C and D queued, in that order of opening: as each item stops, the next one starts, B, then C, then D.
- `TestAQueuedItemStartsWhenAnItemStopsInvestigating`: A stops `quiet`; in the same advance, B starts.
- `TestAQueuedItemStartsWhenAnInvestigatingItemIsClosed`: A, in round 1, is closed: B starts.
- `TestClosingFreesASlotForTheQueue`: with `Limit` 1, an admitted item, B, whose start keeps failing (a records store that refuses B's `item_started`) holds the slot, and C waits; closing B frees it, and C starts.
- `TestAQueueHeadLeftNotStaffedLetsTheNextItemStart`: with `Limit` 1, A investigating, B and C queued, and B's type gone from the current revision: A stops; the walk admits B, which records `not_staffed` cause `type` and loses its mark, then admits C, which starts.
- `TestAQueuedItemClosedByALostAnswerNotRetriedStillLetsTheNextStart`: B, the queue's head, is closed by a close whose append commits and answers an error, which no one retries: when A stops, C starts, B being closed.
- `TestAQueuedItemClosedByAnActWhoseAnswerWasLostLeavesTheQueue`: the close of a queued item whose append commits and answers an error: the item leaves `Waiting` at once, as its close's check found its record.
- `TestTheWalkAdvancesEachQueuedItemAtMostOnce`: with every queued item's start failing, one walk loads each queued item once (a records store counting loads per item).
- `TestAQueuedItemReachingItsTurnAtTheDailyCapIsNotStaffed`: with `Limit` 1, A investigating, B and C queued, and the day's spend at the cap: A stops, and B and C both record `not_staffed`, cause `daily_cap`: the whole queue drains.
- `TestARetriedOpeningOfAQueuedItemDoesNotJumpTheQueue`: with `Limit` 1, A investigating and B queued by its key: B's opening repeated under its key leaves B queued, with no `item_started`.
- `TestAQueuedItemWhoseStartFailedKeepsItsPlace`: with `Limit` 1, A investigating and B and C queued: A stops, the walk admits B, and B's start fails once, the day's spend unreadable at that read: B keeps its mark and its place, C waits, and B starts on its retry.
- `TestAQueuedItemWhoseRecordsDoNotFoldHoldsUpNoOther`: B, the queue's head, holds records that do not fold: it is not in the account, and when A stops, C starts; B's steps fail and are tried again.
- `TestALoweredLimitStartsNothingUntilBelowIt`: three items investigating, then a crash and a takeover with `Limit` 1, and D queued: D waits while two of them stop, and starts once the third stops.
- `TestATakeoverKeepsTheQueuesOrder` (`restart_test.go`): with `Limit` 1, B, C and D queued, a crash and a takeover: as items stop, B, C and D start in that order.
- `TestTheQueuesOrderIsTheOpeningsTimesWithinAHoldingAndAfterATakeover` (`restart_test.go`, the plan's; R40): with `Limit` 1 and A investigating, B opens, the records store's clock steps back a second, and C opens, so C's opening is recorded before B's: when A stops, C starts, then B; the same records after a crash and a takeover give the same order. Fails under an account kept in the order it learned of its items: B starts first within the holding, and C first after the takeover.
- `TestAnAdmittedItemWhoseStartFailedOnceStartsOnItsRetry`: with `Limit` 1, X investigating and the queue [Q1, Q2]: X stops; the walk admits Q1, whose start fails once, as the day's spend cannot be read; with no other trigger, Q1's retry starts it, and Q2 waits. Fails under a walk that keeps no admission past its own advance of Q1: Q1's slot frees once its start fails, and Q2 starts in its place (amended in execution, 2026-11-02: the line gave the design's version 3 failure, which the walk as built cannot meet).
- `TestAQueueAdmittedWhileNoRevisionIsCurrentStartsOnceOneIs` (`restart_test.go`): a takeover's walk admits Q1 while no revision is current, as serve's registry loads beside the lead; once one is current, Q1's retry starts it, though nothing else advances it.

- [ ] **Step 1: Write the tests above, and give the harness's options `Limit: 3`.** Run `go test -race -count=1 ./internal/work/app/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the account, the walk and its marks, the snapshot, the start's cap, and serve's option.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./cmd/agentswarm/...`; `go test -race -count=1 -tags integration ./cmd/agentswarm/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app cmd/agentswarm/serve.go`; `git commit -m "feat(work): items past the limit wait in a queue, admitted in their order while a slot is free"`.

### Task 10: Promotion, and the limit and one

**Decisions:** 4 (promotion; the rule of the limit and one), 13 (a promotion's repeat, lost answer and effect), R6, R13, R23. **Contracts:** C16 (`Promote`), C19 (`item_promoted`).

**Files:**
- Create: `internal/work/app/promote.go`, `promote_test.go`
- Modify: `internal/work/app/coordinator.go` (the step starts a queued item that is promoted as well as admitted)

**Interfaces:**
- Consumes: Task 9's `overLimit`, `walk`, account; Task 7's `findKey`, `carryOn` and `checkReason`.
- Produces: `func (c *Coordinator) Promote(ctx context.Context, id item.ID, a *Act) (Outcome, error)`, its consequences giving the item in `starts` when it would start, and the items investigating after it.

**Tests (write first):** each in `promote_test.go`.
- `TestAPromotionStartsAQueuedItemOverTheLimitByOne`: with `Limit` 1, A investigating and B queued, `Promote(B)` records `item_promoted` by `admin`, with its request, then B's `item_started` (its `killSwitchSeen`, zero until Task 12 keeps a view, is Task 12's to assert); `Lifecycle().Investigating` is 2. Fails first: a build failure, no `Promote`.
- `TestAPromotionPastTheLimitAndOneIsRefused`: then `Promote(C)` answers `Refused{limit_reached}`, and records nothing.
- `TestAPromotedItemCountsTowardTheLimit`: with `Limit` 1, A investigating and B promoted, its start failing (a records store that refuses B's `item_started`): B counts, `Investigating` 2; C's promotion is refused `limit_reached`; when A stops, the walk admits no one, B holding the slot.
- `TestPromotingAnAdmittedItemChangesNothing`: an admitted item whose start failed: `Promote` answers `changed: false`, records nothing, and is not refused for the limit, though the items investigating are past it.
- `TestPromotingAnItemThatNeverRanStartsIt`: an item opened at the daily cap awaits its close not staffed; the day's spend below the cap again, `Promote` lifts its `awaiting_close`: it starts, its run is created and its first round opens.
- `TestPromotingAStartedItemWithoutARunCreatesItsRun`: an item started without a run, its `item_awaiting_close` `not_staffed` (cause `kill_switch`) appended by the test as the kill switch leaves it (Task 12): `Promote` records `item_promoted`, and the step creates its run.
- `TestPromotingAnInvestigatingItemWithoutARunChangesNothing`: a started item whose run's creation keeps failing (`Revisions.At` failing): `Promote` answers `changed: false`.
- `TestAnAdmittedItemCountsTowardTheLimitAndOne`: with `Limit` 3, two items investigating and the queue [Q1, Q3, Q4]: the walk admits Q1, whose start fails once (a records store that refuses Q1's first `item_started`); Q3's promotion starts it, Q4's is refused `limit_reached`, and Q1's retry starts it: four items investigate, the limit and one, never five. Fails under a walk whose admission does not count: Q4 starts, five investigate.
- `TestAPromotionWhoseCommitAnswerWasLostStartsTheItem`: `item_promoted`'s append commits and answers an error: `Promote` finds its record by its key, answers as a repeat, and the item starts.
- `TestAnActWhoseEffectHoldsChangesNothing` (a table): a close of a closed item; a reopen of an item awaiting its close; a promotion of an item that has a run, of an investigating item with no run, and of a queued item admitted: each answers `changed: false` and records nothing, so its key is kept nowhere: a later act under the same key with another digest is not refused `key_reused`.
- `TestAPromotionIsRefusedOnAClosedItemAndAtTheDailyCap` (the plan's): a promotion of a closed item answers `Refused{closed}`; of a queued item while the day's spend is at the cap, `Refused{daily_cap_reached}`; each records nothing.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/app/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** `Promote`, its refusals in R23's order, and the step's start of a promoted item.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app`; `git commit -m "feat(work): a person promotes an item with no run, up to the limit and one"`.

### Task 11: The kill switch's table, its stores, and its verification

**Decisions:** 5 (its table and its store), 2 (migration 0010's reader), R22, R31. **Contracts:** C17; C16 (`control`, `Controls`, the aliases, `Records.UnstaffedBy`).

**Files:**
- Create: `internal/db/migrations/0010_kill_switch.sql`
- Create: `internal/work/control/control.go`
- Create: `internal/work/postgres/controls.go`, `controls_integration_test.go`; `internal/work/memory/controls.go`, `controls_test.go`
- Modify: `internal/work/postgres/records.go` (`UnstaffedBy`), `records_integration_test.go`; `internal/work/memory/records.go` (`UnstaffedBy`)
- Modify: `internal/work/app/ports.go` (`Controls`, `KillSwitch`, `ErrStale`, `Records.UnstaffedBy`)
- Modify: `internal/audit/postgres/chain.go` (the source `kill_switch`), `chain_integration_test.go`
- Modify: `internal/db/migrations_test.go`, `allowlist_test.go`

**Interfaces:**
- Consumes: Plan 7d-1's `Guard`, `audit_chain_append`, `audit_records_guard`.
- Produces: C16's `control.KillSwitch`, `control.ErrStale`, `app.Controls`, `app.KillSwitch`, `app.ErrStale`, `Records.UnstaffedBy`; and:

```go
// package postgres (internal/work/postgres), which implements app.Controls,
// and, from Task 15, app.KillSwitchRows
func NewControls(pool *pgxpool.Pool, tenant string) *Controls
func (s *Controls) Guarded(guard Guard) *Controls // SetKillSwitch checks guard in its transaction
func (s *Controls) KillSwitch(ctx context.Context) (control.KillSwitch, error)
func (s *Controls) SetKillSwitch(ctx context.Context, on bool, by string, reason board.Reason, after int64) (control.KillSwitch, bool, error)
func (s *Controls) LatestKillSwitch(ctx context.Context) (int64, error)

// package memory: the same three methods, row times from now
func NewControls(now func() time.Time) *Controls
```

**Notes for the implementer:** `SetKillSwitch` runs one read-committed transaction: an advisory lock of the tenant (`kill_switch/<tenant>`), the guard's check, the state's read; then nothing when the state asked holds already, `ErrStale` with the state when `after` is not the latest row's number, or else the row, `seq` the latest plus one, at `clock_timestamp()`. `KillSwitch` reads the latest row, the latest on-row's number and the latest off-row's time in one query. `LatestKillSwitch` reads `max(seq)` on the primary key. `UnstaffedBy` reads `item_awaiting_close` records whose `awaitingClose.unstaffedBy` is `seq`. The migration has no `-- agentswarm:min_reader` line. The package's integration tests share one Postgres, and the kill switch's `seq` counts per tenant, so each test writes under a tenant of its own, made from `t.Name()` and a random suffix in the anchors' form (lowercase, every other character a dash, cut to 63 characters, as `chain.ValidTenant` takes), which keeps every test right under `-count` above 1 as well.

**Tests (write first):**
- `TestMigration10ReadersAreOfItsOwnLevel` (`internal/db`, unit): the embedded `0010_kill_switch.sql` has level 10 and `min_reader` 10, and the build's level is 10. Fails first: no such migration.
- `TestTheKillSwitchIsOffUntilFirstSet` (`internal/work/postgres`, integration): on a tenant of its own, `KillSwitch` gives `On` false, `Seq` and `OnSeq` 0 and zero times, and `LatestKillSwitch` 0. Fails first: a build failure.
- `TestTheKillSwitchIsWrittenOnlyUnderTheHolding` (integration): a guarded store writes under the lease's current epoch; under an earlier epoch, `SetKillSwitch` fails wrapping `holding.ErrLeaseLost` and writes no row; a context without an epoch fails wrapping `holding.ErrNoEpoch`.
- `TestTheKillSwitchsRowsAreAppendOnlyAndChained` (integration): each row is chained, scope `''`, with the next link's sequence; an `UPDATE`, a `DELETE` and a `TRUNCATE` of `kill_switch` are refused.
- `TestAKillSwitchRowNamingAnOlderRowIsStale` (integration): with rows 1 (on) and 2 (off), an on naming `after` 1 fails with `ErrStale`, writes nothing, and returns the state of row 2; one naming 2 writes row 3.
- `TestEveryKillSwitchAnswerCarriesTheStateAfterIt` (integration): a write that appends, one whose state held already, and a stale one each return the state after them, `Seq`, `OnSeq` and `OffAt` as the rows say.
- `TestSetKillSwitchReportsChangedAndStaleWithTheState` (integration, a table): on from off with `after` the latest: `changed` true, no error; on while on, whatever `after`: `changed` false, no error, nothing appended; off from on with an old `after`: `changed` false and `ErrStale`, with the state.
- `TestTheItemsAnOnLeftUnstaffedAreFoundByItsRow` (integration): of three items' `item_awaiting_close` records, two naming `unstaffedBy` 4 and one 2, `UnstaffedBy(4)` gives the two, by id.
- `TestVerificationReadsTheKillSwitch` (`internal/audit/postgres`, integration): two rows written verify with no finding, and the counts and the anchor's records hold `kill_switch` 2; a row changed behind the platform's back is found.
- `TestTheMemoryStoresAnswerAsThePostgresOnesDo` (`internal/work/memory`, the plan's; R22): `memory.Controls` answers every case of `TestSetKillSwitchReportsChangedAndStaleWithTheState` and `TestTheKillSwitchIsOffUntilFirstSet` as the Postgres store does, and `memory.Records.UnstaffedBy` that of `TestTheItemsAnOnLeftUnstaffedAreFoundByItsRow`.
- The changed tests of "Earlier tests this plan changes", Task 11.

- [ ] **Step 1: Write the tests above, and change the earlier tests named for Task 11.** Run `go test -race -count=1 ./internal/db/ ./internal/work/...` and `go test -race -count=1 -tags integration ./internal/work/postgres/ ./internal/audit/postgres/ ./internal/db/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the migration, the domain package, both stores, `UnstaffedBy`, the port and verification's source.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/db/... ./internal/work/... ./internal/audit/...`; `go test -race -count=1 -tags integration ./internal/work/postgres/ ./internal/audit/postgres/ ./internal/db/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/db internal/work internal/audit/postgres`; `git commit -m "feat(work): the kill switch's audited table, migration 0010 at its own level, and its stores"`.

### Task 12: The kill switch's on: its view, the stops, and what is owed

**Decisions:** 5 (the view; a change's two rules; turning it on, steps 2 to 6; owed kills of runs and items; what is owed; while it is on; the preview; a request's form; the notice; without a configuration), 3 (the step's halt for `killed`; `begin`'s guard; `finish`'s notice and `unstaffedBy`), 12 (the step's rules 4 and 8), 9 (the on needs a reason), R21, R45. **Contracts:** C3 (`kill switch set`), C16 (`KillSwitchOutcome`, `UnstaffedItem`, `SetKillSwitch`, `Ports.Controls`, `Lifecycle.KillSwitch`), C18 (`kill_switch`), C19 (`killSwitchSeen`, `killed`, `unstaffedBy`).

**Files:**
- Create: `internal/work/app/killswitch.go`, `killswitch_test.go`
- Modify: `internal/work/app/coordinator.go` (the step's rules 4 and 8; an opening while on; `start`'s cause `kill_switch`; `Open`, `start` and `create` record `killSwitchSeen`; `finish`'s notice and `unstaffedBy`), `promote.go` (refused while on; its `killSwitchSeen`), `close.go` (the closed step posts a killed run's notice), `turns.go` (`begin`'s guard), `rounds.go` (`hold` copies `killSwitchSeen`), `queue.go` (the snapshot's view), `ports.go` (`Ports.Controls`, required)
- Modify: `internal/work/app/harness_test.go` (a controls fake around `memory.Controls`; `h.ports()`), `coordinator_internal_test.go` (the two `begin` tests, on small fakes of their own, as `TestStartAgainSkipsALaunchingTurn` builds its coordinator), `restart_test.go`
- Create: `internal/work/app/export_test.go`, compiled only into the package's tests, as `internal/work/tokens/export_test.go` is: two hooks the external tests on the harness call, since `internal/work/memory` imports this package and an internal test cannot use it
- Modify: `internal/work/item/lifecycle.go` (`KillSwitchSeen`, `Owes`)
- Modify: `internal/work/board/board.go` (`NoticeKillSwitch`), `state.go` (its shape); create `internal/work/board/killswitch_test.go`
- Modify: `cmd/agentswarm/serve.go` (`Controls`: `workpg.NewControls(s.main, d.Tenant).Guarded(lease)`)

**Interfaces:**
- Consumes: Task 11's `Controls`, `memory.Controls`, `workpg.NewControls`; Task 5's `drop`, `haltRecords`, `closedStep`; Task 6's `cancelAutoCloses`; Task 9's account, `walk` and snapshot; Task 7's `checkReason`, for the on's reason.
- Produces: C16's names marked Task 12; and, for Tasks 13 to 16:

```go
// package item
// KillSwitchSeen is the latest kill switch row the coordinator knew for the
// item: its run's creation's; with no run, the latest of its opening's,
// start's and promotion's.
func (s *State) KillSwitchSeen() int64
// Owes reports whether the item owes the kill of the on-row onSeq (Decision
// 5): a run that has not stopped, or an item with no run that is queued or
// started, with onSeq above KillSwitchSeen.
func (s *State) Owes(onSeq int64) bool

// package board
NoticeKillSwitch NoticeCode = "kill_switch" // names nothing; once per round

// package app, unexported
// c.killSwitch is the view: On, Seq, OnSeq and OffAt.
// applyKillSwitch applies a view under the lock, by Decision 5's two rules,
// and returns what it did; cause is C3's.
func (c *Coordinator) applyKillSwitch(ctx context.Context, view KillSwitch, cause string) KillSwitchOutcome
// owed lists what the rule still acts on (Decision 5).
func (c *Coordinator) owed() []item.ID
// running gains killSwitchSeen int64, which hold copies from the run's records.

// package app, export_test.go: hooks for the tests in package app_test
// SetKillSwitchViewForTest sets the coordinator's view to v under its lock,
// and acts on nothing.
func (c *Coordinator) SetKillSwitchViewForTest(v KillSwitch)
// AdvanceForTest advances item id under the coordinator's lock, as a timer's
// retry would.
func (c *Coordinator) AdvanceForTest(ctx context.Context, id item.ID)
```

**Notes for the implementer:** in this task an off writes its row and turns the view off; the off's steps are Task 13's. A store error is returned as it is: Task 14 handles stale requests, the guard's refusal and unknown outcomes, and the check that a request is still live when it reaches the lock. The view starts off at each takeover until Task 15 reads the table there. The on's row, and every halt and record it causes, are written under `c.ctx`. Every `item_awaiting_close` this task records carries the delay its writer can read, or zero, as Task 6's rule says. The preview reads the table (`Controls.KillSwitch`) and answers its `Seq` and the consequences: the runs that would stop and the items that would not be staffed.

**Review:** Task 12 is the largest task, about twenty tests over nine files, and is reviewed by two fresh reviewers on Opus 5.5, each reading the whole diff with one half in its charge: the view, `applyKillSwitch`, the owed rules, the step's rules 4 and 8, `begin`'s guard and the drop; and the on's act and its answer, the preview, the notice, the reason and serve's wiring. It stays one task, since every test of the view's effects goes through the on's act.

**Tests (write first):** each in `killswitch_test.go`, unless named.
- `TestTheKillSwitchStopsEveryRunAtOnceWithANoticeOnEachBoard`: two items in round 1, their turns running: `SetKillSwitch(on, After 0, a reason)` answers `On`, `Seq` 1, `Changed`, and `Stopped` both runs, at round 1. The turns saw `StopAll`, the sessions `EndAll`; each run's records end with a `round_ended` `stopped` and `run_stopped` `killed`, `killed` 1, then `item_awaiting_close` `not_staffed` with `unstaffedBy` 1; each board holds one `kill_switch` notice and a stop entry `killed`; `CloseRun` is called for both runs; `kill switch set` is logged at Warn with `on`, `seq` 1, `cause` `act` and `stopped` 2. Fails first: a build failure, no `SetKillSwitch`.
- `TestTheKillSwitchLeavesQueuedItemsNotStaffed`: with `Limit` 1, one item investigating and one queued: the queued item records `not_staffed`, cause `kill_switch`, `unstaffedBy` 1, and no `item_started`; `Unstaffed` lists it.
- `TestAnItemOpenedWhileTheKillSwitchIsOnIsNotStaffed`: the switch on, an opening records `item_opened`, `killSwitchSeen` 1, and `item_awaiting_close` `not_staffed`, cause `kill_switch`, `unstaffedBy` 1.
- `TestAKillOfAStartedItemWithoutARunLeavesItNotStaffed`: an item started whose run's creation fails (`Revisions.At` failing): the on records `item_awaiting_close` `not_staffed`, cause `kill_switch`, with `unstaffedBy`, and no `run_created`.
- `TestAKillDuringARoundsSettlingStopsTheRunOnce`: a round that ended by its turns, in its grace: the on records one `round_ended`, `stopped`, and one `run_stopped` `killed`; past the grace and the settling wait, no second `round_ended`.
- `TestAKillWhileLaunchesArePendingPostsNoTurnUnstartedNotice`: turns whose launches are pending (the fake's `Start` answering pending): the on, then `Launched` with `cancelled` for each: no `turn_unstarted` notice on any board.
- `TestAKillWhoseHaltFailsStartsNoTurn` (the drop): a turn's launch pending, and the records store failing the halt's append: after the on, the runner reports the launch `cancelled`: the report arms no timer, and, the clock past the retry wait, `Start` is not called again. Fails without the drop, whatever `begin`'s guard does: the cancelled launch arms its retry.
- `TestAKillWhoseRequestEndsAfterItsRowStillHaltsEveryRun`: the controls fake cancels the request's context as soon as the row commits: every run is halted all the same, its records written under the coordinator's context.
- `TestBeginStartsNoTurnWhileTheKillSwitchIsOn` (`coordinator_internal_test.go`): a coordinator built by its fields, as `TestStartAgainSkipsALaunchingTurn` builds one, with a runner that counts its starts, a still clock, timers that count their arms, and `Boards` over an event store that counts its appends; held, its view on, and a run whose agent's turn is starting: `begin` calls no `Start`, keeps the turn among the round's starting turns, arms no timer and posts no notice.
- `TestBeginStartsNoTurnOfARunThatOwesAKill` (`coordinator_internal_test.go`): the same with the view off, its `OnSeq` 2, and the run's `killSwitchSeen` 1.
- `TestAHaltDropsTheRunsStateSoACancelledLaunchArmsNothing` (`killswitch_test.go`, on the harness, through `export_test.go`'s hooks): the harness's coordinator holds a run in round 1 with a launch pending (the runner answering pending); `SetKillSwitchViewForTest` sets the view off with its `OnSeq` above the run's `killSwitchSeen`, and `AdvanceForTest` advances the item: its step halts the run, and its `finish` fails at the board, its store failing the stop entry; the runner reports the launch `cancelled`: no retry is armed, and, the clock past the retry wait, `Start` is not called again. No public path reaches a run held in memory that owes a kill, since every raise of the latest on-row drops every run, so the test sets that state itself. Fails without the drop, whatever `begin`'s guard does.
- `TestAKillSwitchNoticeAfterTheStopEntryCountsAsDone`: a killed run's `finish` fails once after its stop entry, its `item_awaiting_close` append failing: on its retry, the `kill_switch` notice's post meets `board.ErrStopped`, which counts as done; the item awaits its close, and no failure of the notice is logged.
- `TestAHaltIsCarriedOnFromItsRecordsAfterACrash` (`restart_test.go`): the kill's halt commits its records and the instance crashes before the run's `finish`: the item, active until its `item_awaiting_close`, is carried on by the takeover, whose step runs `finish` from the records: the board's `kill_switch` notice and stop entry `killed`, then `item_awaiting_close` `not_staffed` with `unstaffedBy` from `run_stopped.killed`; no turn starts. (A closed item is no longer carried on, Decision 6, so a close's crash leaves its board to the closed step's best effort.)
- `TestTheOwedListNamesOnlyItemsTheRuleActsOn`: a running item whose halt fails, a killed item whose `finish` cannot post its stop entry, a queued item whose not-staffed record fails, an item awaiting its close, and a closed one: the on's `Owed` names the first three alone.
- `TestTheOnsAnswerNamesTheRunsThatStillOweTheKill`: of two runs, one whose halt's append fails: the answer's `Stopped` names the other, and `Owed` the first's item.
- `TestAKillSwitchPreviewReadsTheTable`: the table holds row 2, which the view lacks: a preview answers `Seq` 2 and the consequences, the runs that would stop and the items that would not be staffed, and records and acts on nothing; an on naming `after` 2 then lands.
- `TestTheKillSwitchWorksWithoutACurrentRevision`: with no current revision, an on with a code alone halts every run; one whose reason has text answers `ErrInvalid` and writes no row.
- `TestAReasonIsRequiredWhereTheSpecRequiresOne`: an on without a reason answers `Refused{reason_required}` and writes no row; a close, a reopen and a promotion without one act.
- `TestALaunchCancelledOrRefusedByTheHoldingPostsNothing` (a guard, unchanged; passes at Step 1): a cancelled launch on a run the coordinator still holds still arms its retry.
- `TestAKillSwitchNoticeNamesNothingAndIsPostedOncePerRound` (`internal/work/board`, the plan's; R21): `Notify` takes `kill_switch` naming nothing, and refuses one naming an agent, a call or a reason; a second in the same round is a repeat; the next round takes one.
- `TestAPromotionIsRefusedWhileTheKillSwitchIsOn` (the plan's): while the switch is on, a promotion of an item with no run answers `Refused{kill_switch_on}` and records nothing; once it is off, at row 2, the promotion records `item_promoted` with `killSwitchSeen` 2.

- [ ] **Step 1: Write the tests above, and give `h.ports()` the controls fake.** Run `go test -race -count=1 ./internal/work/...`. Expected: FAIL, each test for the reason its line gives; the guard passes.
- [ ] **Step 2: Implement** the view and `applyKillSwitch`, the on's steps, the owed rules, the step's rules 4 and 8, `begin`'s guard, the notice, and serve's port.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./cmd/agentswarm/...`; `go test -race -count=1 -tags integration ./cmd/agentswarm/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work cmd/agentswarm/serve.go`; `git commit -m "feat(work): the kill switch's on stops every run at once, staffs nothing, and halts every run and item that owes it"`.

### Task 13: The kill switch's off, and auto-close's pause

**Decisions:** 5 (turning it off), 8 (the pause), R4. **Contracts:** C16 (`StayedUnstaffed`, `Owed`), C3 (the off's attributes).

**Files:**
- Modify: `internal/work/app/killswitch.go` (the off's steps; only an admin), `autoclose.go` (no arming while on; every timer armed again at the off, counting from it), `killswitch_test.go`

**Interfaces:**
- Consumes: Task 12's `applyKillSwitch`, `owed`, the view's `OffAt`; Task 6's `armAutoClose`, `cancelAutoCloses`; Task 11's `Records.UnstaffedBy`.
- Produces: `SetKillSwitch`'s off whole; `applyKillSwitch`'s second rule for an off.

**Tests (write first):** each in `killswitch_test.go`.
- `TestOnlyAnAdminTurnsTheKillSwitchOff`: the switch on, an off by an actor that is not an admin answers `Refused{admin_only}` and writes no row; an admin's off writes row 2. Fails first: the off takes any actor.
- `TestSettingTheKillSwitchToItsStateChangesNothing`: an off while off, and an on while on, answer `changed: false` and append nothing; with a row in the table that the view lacked, such an answer still brings the view up to the table.
- `TestTheKillSwitchHeldPastTheAutoCloseDelayListsEveryItemItLeftUnstaffed`: the on leaves unstaffed a killed run's item, a queued item and an item opened while it is on; the clock passes the 30-minute delay, and none closes; a person closes the queued one; the off answers `StayedUnstaffed`: the three, each with its state now, two `awaiting_close` and one `closed`.
- `TestNoItemClosesByItselfWhileTheKillSwitchIsOn`: an item awaiting its close; the on cancels its timer; past its delay, no `item_closed`.
- `TestAutoCloseCountsFromTheKillSwitchsTurningOff`: an item awaiting its close since T; on at T plus 10 min; off at T plus 40 min: it closes at T plus 70 min, not before.
- `TestTheKillSwitchTurnsOffThoughAnItemsBoardDoesNotReplay`: a killed run whose board does not replay, so its `finish` cannot end: the off answers at once, and its `Owed` names the item.
- `TestARunThatOwesAKillIsHaltedAfterTheKillSwitchIsOff`: the on's halt fails at its append; the switch is turned off; the store back, the item's retry halts the run `killed`, `killed` 1, and no round opens.
- `TestAnItemWithNoRunThatOwesAKillIsNotStaffedAfterTheOff`: a queued item's not-staffed record fails under the on; the switch is turned off; its retry records `not_staffed`, cause `kill_switch`, `unstaffedBy` 1, and it does not start.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/app/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the off's steps and the pause.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app`; `git commit -m "feat(work): only an admin turns the kill switch off, which lists the items it left unstaffed; auto-close pauses while it is on"`.

### Task 14: The kill switch's lost answers, stale requests, and writes whose outcome is unknown

**Decisions:** 5 (the row, written only once its request reached the coordinator's lock still live; its answers: the store's answer followed, stale, the guard's refusal, an unknown outcome, the second read's four answers and its retry; an off that reveals a lost on), R37 (the mark of an unknown outcome, and what it holds back), 3 and 12 (`begin` and the step while the view is unsettled), 4 (the walk and a promotion while it is), 8 (auto-close while it is). **Contracts:** C16 (`ErrStale`, `ErrUnsettled`, `Lifecycle.KillSwitchUnsettled`), C3 (`answer`, `reread`; `kill switch unsettled`, `kill switch settled`).

**Files:**
- Modify: `internal/work/app/killswitch.go` (the answers; the mark, set and cleared, and the advance that clears it), `turns.go` (`begin` while the view is unsettled), `coordinator.go` (the step's waits; `ErrUnsettled`), `queue.go` (the walk; the snapshot's `KillSwitchUnsettled`), `promote.go` (`ErrUnsettled`), `autoclose.go` (no close while the view is unsettled), `harness_test.go` (the controls fake can commit a write and then fail, fail without committing, refuse wrapping `ErrLeaseLost`, fail reads, and cancel the request's context once a row commits)
- Create: `internal/work/app/killswitch_answers_test.go`

**Interfaces:**
- Consumes: Task 12's `applyKillSwitch`, `begin`'s guard; Task 13's off; Task 9's `walk`; Task 10's `Promote`; Task 6's timers; `failed`, which fences on a lost lease.
- Produces: `SetKillSwitch`'s answers as Decision 5's step 1 gives them; R37's mark; `ErrUnsettled`; `Lifecycle.KillSwitchUnsettled`.

**Notes for the implementer:** the mark lives in the view, under the lock, and in the snapshot. Every unknown outcome sets it before the second read, the one whose error comes from the coordinator's ended context included; the guard's refusal does not. A read that succeeds, or a store's answer that carries the switch's state, clears it; the fence clears it with the view. Clearing it advances every active item in sorted order (R33) and runs the walk, whether or not the view changed, and logs `kill switch settled`; setting it logs `kill switch unsettled`. While it is set and the view is off: `begin` starts no turn and arms its retry, with no notice; the step returns `wait` where it would start, create, open or hold; the walk admits none; `Promote` answers `ErrUnsettled` after its checks of a closed item and of its effect; auto-close's timer records nothing. `RecordKillSwitchOn` sets no mark.

**Tests (write first):** each in `killswitch_answers_test.go`.
- `TestAKillSwitchRowWhoseCommitAnswerIsLostStillStopsEveryRun`: the store commits the on, then answers an error: the second read finds it, the view turns on, and every run is halted; the act answers `changed: true`; no turn starts after. Fails first: the error is returned, and the runs go on.
- `TestARetriedKillSwitchOnAfterALostAnswerStopsEveryRun`: the on's answer lost and its second read failed, the act answers an error the admin API answers `503`; the on sent again as it was, `after` 0, answers `changed: false`, and every run is halted.
- `TestAReadAfterAnUnknownOutcomeThatFailsIsTriedAgain`: the write's answer lost and the read failed: after the retry wait the coordinator reads again, applies the on, and halts every run, logged with `cause` `reread`.
- `TestAKillSwitchRowIsWrittenThoughItsClientLeavesOnceItReachedTheCoordinator`: a request whose context ends while the store writes: the row lands, written under the coordinator's context, and every run is halted; a request whose context ended before it reached the coordinator's lock writes nothing.
- `TestAnOffWhoseCommitAnswerIsLostStillLetsItemsBeStaffed`: the switch on, an off committed with its answer lost: the second read finds it, the view turns off, and an opening after it starts.
- `TestAnOffThatRevealsALostOnHaltsEveryRunThatOwesIt`: an on committed as row 1, its answer lost and its read failed, so the view stays off; an off naming `after` 1 lands as row 2, and its answer raises the view's `OnSeq` to 1 with the state off: every run that owes row 1 is halted, then the off's steps run. Fails first: the view goes from off to off, and acts on nothing.
- `TestAKillSwitchWriteTheGuardRefusesFencesAndAnswersNotHeld`: the store refuses with an error wrapping `ErrLeaseLost`: the coordinator is fenced and its leader told (`OnLost`), the act answers `ErrNotHeld`, and no second read is made.
- `TestASecondReadAfterAnUnknownOutcomeAnswersAsItFinds` (a table, the write's answer lost each time): the state asked, by a row after `after`: `changed: true`; the state asked, with no row after `after`: `changed: false`; not the state asked, with no row after `after`: an error the admin API answers `503`; not the state asked, with a row after `after`: `ErrStale`.
- `TestAStaleKillSwitchOffAfterANewerOnIsRefused`: on as row 1, off as row 2, on as row 3: a delayed off naming `after` 1 answers `ErrStale`; the switch stays on, and the view follows the state the answer carries.
- `TestNoTurnStartsWhileAKillSwitchWritesOutcomeIsUnknown` (the controller's; R37): the store commits the on as row 1, then answers an error, and the second read fails: the act answers an error the admin API answers `503`, and `Lifecycle()` shows `KillSwitchUnsettled`, `On` still false. Until the retry's read: a launch's retry calls no `Start`, and arms its retry again; an item whose round ended opens no next round, and no session is issued or moved; an opening records its `item_opened` alone and waits queued. The retry's read finds row 1: the view turns on, every run is halted `killed`, and no `Start`, `Issue` or `Move` came between the row's commit and the halts. Fails first: the view stays off, and the launch's retry starts its turn after the row committed.
- `TestAFailedKillSwitchWriteThatDidNotCommitResumesStaffingOnceARereadSettlesIt` (the controller's; R37): the store fails the on before its commit, and the second read fails: with a slot free, a queued item is not admitted, and a launch's retry starts no turn. The retry's read finds the switch off, as it was: the mark clears and `kill switch settled` is logged, with no `kill switch set`; every active item is advanced and the walk runs: the queued item starts, and the turn starts on its next retry. Nothing was recorded on the mark's account: no `item_awaiting_close`, no `run_stopped`. Fails first: staffing never pauses, and the queued item starts while the outcome is unknown.
- `TestAPromotionWhileAKillSwitchWritesOutcomeIsUnknownAnswersUnavailable` (the plan's; R37): while the view is unsettled and off, a promotion of a queued item answers `ErrUnsettled` and records nothing; a promotion of a closed item still answers `Refused{closed}`; once a read settles the switch off, the same promotion starts the item.
- `TestNoItemClosesByItselfWhileAKillSwitchWritesOutcomeIsUnknown` (the plan's; R37): an item awaiting its close whose deadline passes while the view is unsettled: no `item_closed`; the read settles the switch off, the advance arms the timer again, and the item closes at once, its deadline past.

- [ ] **Step 1: Write the tests above, and give the controls fake its modes.** Run `go test -race -count=1 ./internal/work/app/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the answers of Decision 5's step 1, and R37's mark: its setting and clearing, the advance that clears it, and its checks at `begin`, the step, the walk, a promotion and auto-close's timer.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app`; `git commit -m "feat(work): the kill switch's view follows the table through lost answers, failed reads and stale requests, and no work starts while a write's outcome is unknown"`.

### Task 15: The kill switch at a takeover and at the leader's renewals

**Decisions:** 12 (the takeover reads the kill switch; owed kills before each item's next round), 5 (the leader's renewal check), R25, R26. **Contracts:** C16 (`KillSwitchSeen`, `RefreshKillSwitch`, `KillSwitchRows`, `LeaderOptions.KillSwitch`), C3 (`renewal`, `takeover`).

**Files:**
- Modify: `internal/work/app/coordinator.go` (`TakeOver` reads the switch after the sweep; a read that fails refuses the lease), `killswitch.go` (`KillSwitchSeen`, `RefreshKillSwitch`), `leader.go` (the renewal check; its refresh's timer; the comment at `renew`), `ports.go` (`KillSwitchRows`; `LeaderOptions.KillSwitch`), `leader_test.go`, `restart_test.go`
- Modify: `internal/work/postgres/controls.go` (its assertion that it implements `app.KillSwitchRows`)
- Modify: `cmd/agentswarm/serve.go` (`LeaderOptions.KillSwitch`: `workpg.NewControls(s.lease, d.Tenant)`, on the lease's pool; `worstDrain`'s comment, R26)

**Interfaces:**
- Consumes: Task 12's `applyKillSwitch`; Task 11's `LatestKillSwitch`.
- Produces: C16's names marked Task 15.

**Notes for the implementer:** the read runs after a renewal that succeeded, its sample and its check, only when the check found no `ErrSchema`, and only while the leader does not drain, within `RenewEvery`, under the leader's context. A row newer than `KillSwitchSeen()` arms the refresh at now on the leader's timers, unless one is pending (R25); the refresh is not counted among the leader's work in flight, and never runs under the leader's lock. `KillSwitchSeen` is an atomic the view's changes store. `worstDrain` stays: the read is skipped once the leader drains (R26).

**Tests (write first):**
- `TestATakeoverWithTheKillSwitchOnStartsNoRound` (`restart_test.go`): the table on, row 1, and a crash that left an item in round 1 and one queued: the takeover halts the run `killed` and leaves the queued item not staffed; no round opens and no turn starts. Fails first: the takeover reads no switch, and the round goes on.
- `TestATakeoverThatCannotReadTheKillSwitchRefusesTheLease` (`restart_test.go`): the controls fake fails its read: `TakeOver` fails, the coordinator acts on no item, and no step is tried again.
- `TestATakeoverHaltsEveryRunThatOwesAKillBeforeItsNextRound` (`restart_test.go`): two runs in round 1, created before rows 1, on, and 2, off, which another instance wrote while this one was down: the takeover halts each run `killed`, `killed` 1, before it opens its next round, and no round opens on either; the view is off, and an item opened after the takeover starts. A takeover advances items one at a time, so an item that owes nothing may open its round before an owing one is halted: the test asserts per item.
- `TestARenewalThatFindsANewerKillSwitchRowHasTheCoordinatorActOnIt` (`leader_test.go`): the rows' reader answers 1 while the coordinator's view holds 0, and the table holds the on: after the renewal the refresh runs, the view turns on, and the coordinator's runs are halted, logged with `cause` `renewal`. Fails first: a build failure, no `LeaderOptions.KillSwitch`.
- `TestARenewalWhoseKillSwitchReadFailsRenewsAllTheSame` (`leader_test.go`): the read fails: the failure is logged, the holding renewed, and no refresh armed.
- `TestADrainingLeaderMakesNoKillSwitchRead` (`leader_test.go`): after `Drain`, the renewals make no read.
- `TestAtMostOneKillSwitchRefreshIsPending` (`leader_test.go`): two renewals find a newer row while the first refresh has not run: one refresh is armed.
- `TestNoKillSwitchReadFollowsASchemaThisBuildCannotRun` (`leader_test.go`): a check that finds `ErrSchema` makes no read; one that fails only to read makes it.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/app/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the takeover's read, the renewal check and its refresh, and serve's wiring and comments.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./cmd/agentswarm/...`; `go test -race -count=1 -tags integration ./cmd/agentswarm/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work cmd/agentswarm/serve.go`; `git commit -m "feat(work): a takeover reads the kill switch before any item, and the leader checks it at each renewal"`.

### Task 16: The kill switch during a drain, and acts once the holding's context ended

**Decisions:** 5 (during a drain), 12 (the drain), 13 (every act answers `ErrNotHeld` once the coordinator's context ended), R24. **Contracts:** C16 (`RecordKillSwitchOn`, `Options.WriteTimeout`), C3 (`deferred`).

**Files:**
- Modify: `internal/work/app/killswitch.go` (`RecordKillSwitchOn`), `acts.go` (every act answers `ErrNotHeld` once `c.ctx` ended), `coordinator.go` (`Options.WriteTimeout`, which `NewCoordinator` refuses not above zero), `harness_test.go` (`WriteTimeout: 10 * time.Second`)
- Create: `internal/work/app/killswitch_drain_test.go`
- Modify: `cmd/agentswarm/serve.go` (`WriteTimeout: d.Timing.Database.TransactionTimeout`)

**Interfaces:**
- Consumes: Task 12's `SetKillSwitch` and the fence's drops; Task 7's act entry.
- Produces: C16's `RecordKillSwitchOn` and `Options.WriteTimeout`.

**Notes for the implementer:** `RecordKillSwitchOn` takes the lock; a held coordinator whose context ended is fenced, as `fence` does; one held with a live context answers `ErrNotHeld` (R24); then the lock is released, and the row written under `context.WithoutCancel(ctx)`, `holding.WithEpoch(…, epoch)` and a deadline of `WriteTimeout`. It checks the reason as the on does, acts on no run, and answers `Deferred`, `changed` as the store says, and no `Owed`; a failed write answers its error, which the admin API answers `503`, with no second read, but the guard's refusal, `ErrNotHeld`.

**Tests (write first):** each in `killswitch_drain_test.go`.
- `TestAnActOnACoordinatorWhoseContextEndedAnswersNotHeld`: the holding's context ended while the coordinator is still held, as at a drain's start: `Close`, `Reopen`, `Promote` and `SetKillSwitch` each answer `ErrNotHeld`, and record nothing. Fails first: the acts load and write under an ended context, and answer its error.
- `TestAKillSwitchOnAtTheDrainsStartIsDeferred`: the holding's context ended, the coordinator still held: `RecordKillSwitchOn` fences it first, every turn stopped, every session ended and every run forgotten (amended in execution, 2026-11-03: as Decision 3 now names it), writes the row under the given epoch, and answers `Deferred` and `Changed`, with no `Owed`; a coordinator that takes over next halts every run before its next round. Fails first: a build failure.
- `TestTheDrainsOnIsWrittenThoughTheHoldingsContextEnded`: the controls fake sees the write's context live though the holding's and the request's contexts ended, carrying the given epoch and a deadline `WriteTimeout` away.
- `TestTheDrainsOnIsRefusedOnceItsHoldingEnded`: the store refuses the epoch, wrapping `ErrLeaseLost`: `ErrNotHeld`, and no second read.
- `TestTheDrainsFenceNeverWaitsOnTheDrainsOn`: with the store's write blocked, `Fence` returns while the write is still held; the test waits up to 10 s for it, failing with `t.Fatal`, a wait that only a fence that waits on the write ever runs out (amended in execution, 2026-11-02: a bound of 50 ms bought nothing, since a fence that waited would wait until the test let the write go, and could fail falsely on a throttled Linux run).
- `TestADeferredOnAnswersWithoutOwed`: `Owed` is nil, and `kill switch set` is logged with `deferred` true and no `owed`.
- `TestAKillSwitchTurnedOnDuringADrainHaltsTheNextLeadersRunsBeforeAnyRound`: an on deferred during a drain, its runs in round 1: the next coordinator's takeover halts both before any opens a round, and no turn starts.
- `TestAKillSwitchOnDuringADrainWaitsForTheFence`: `RecordKillSwitchOn` waits for a step that holds the coordinator's lock, and the store's write comes after `StopAll`, `EndAll` and the drop of every run.

- [ ] **Step 1: Write the tests above, and give the harness's options the write timeout.** Run `go test -race -count=1 ./internal/work/app/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** `RecordKillSwitchOn`, the acts' check of the context, the option, and serve's.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./cmd/agentswarm/...`; `go test -race -count=1 -tags integration ./cmd/agentswarm/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app cmd/agentswarm/serve.go`; `git commit -m "feat(work): the kill switch's on during a drain is recorded under the draining holding and deferred to the next takeover"`.

### Task 17: The interleaving test's world, its driver, and its replay

**Decisions:** "The interleaving test" (the run and its clocks, what the fakes inject, schedules, the sequential mode, what a seed replays and its four conditions, a failure, runs, invariant 5), R33, R34, R35, R39, R46. **Contracts:** C16 (the coordinator's and the leader's public API, which the world drives), C19.

**Files:**
- Create: `internal/work/app/interleave_world_test.go` (the world: the two instances; the fake lease; the guarded stores and their faults; the monotonic clock and its timers, the database's clock, each instance's wall clock and `clock.DB`; the fake runner, sessions and upstream pool; the day's spend), `interleave_test.go` (the driver: the events and their weights, the clock steps and their bounds, the schedules, the settling stages, the flags, the event log, the replay; this task's tests)
- Modify, as `TestASeedReplaysItsSchedule` shows them: `internal/work/app/killswitch.go`, `autoclose.go`, `turns.go`, `rounds.go` (`hold`'s loop over `r.tokens`), `coordinator.go` (R33's sorted loops)
- Modify, for each fault of the coordinator the runs find: the file it is in, with a unit test that reproduces it first

**Interfaces:**
- Consumes: the coordinator's public API (Tasks 2 to 16): `Open`, `Close`, `Reopen`, `Promote`, `SetKillSwitch`, `RecordKillSwitchOn`, `Lifecycle`, `Launched`, `Exited`, `TakeOver`, `Fence`; the leader's `Lead`, `Drain`, `Stop` and `Status`; `memory.Records`, `memory.Store`, `memory.Controls`; the coordinator harness's configuration, revisions and ids (`harness_test.go`), where they fit.
- Produces, for Task 18, in the package's test files (`package app_test`):

```go
type mode int // sequential, or concurrent (Task 18)

// world is the interleaving test's world ("The interleaving test").
type world struct{ /* the instances a and b, the fakes, the clock, the log */ }
func newWorld(t testing.TB, seed int64, m mode) *world
func (w *world) step() bool         // picks and runs the next event; false once the schedule ends
func (w *world) settleKillSwitch()  // the first settling stage
func (w *world) settleQuiet()       // the second
func (w *world) log() []string      // each event, then the port calls it caused, in order
func (w *world) leading() *instance // the instance whose leader holds the lease, or nil
func (w *world) records() map[item.ID][]item.Record
func (w *world) calls() []portCall      // every port call: its instance, port, method, run and time
func (w *world) writes() []write        // every committed write: its store, its epoch, the lease's then
func (w *world) killSwitchRows() []ksRow // the table's rows: seq, on or off, time, and whether the driver wrote it out of band
// lifecycles gives the leading coordinator's snapshot before the last event,
// the last one a leader published, and after it.
func (w *world) lifecycles() (before, after app.Lifecycle)

// runSchedules runs the flags' schedules in mode m, calling check after each
// event and each settling stage; a failure reports the schedule's seed, its
// log's tail, and the command that replays it.
func runSchedules(t *testing.T, m mode, check func(w *world, stage string) error)
```

**Notes for the implementer:** a guard checks the epoch and commits in one step under its fake store's lock, as a transaction would. A fault is armed by an event and spent by the next write or read it applies to. The lease expires on the database's clock. Every timer, the coordinators', the leaders' and a read's retry, is the world's, on the monotonic clock; in the sequential mode each fires on the driver's goroutine as monotonic time passes it. Each instance's coordinator gets `clock.NewDB` over a sampler that reads the database's clock and over the instance's wall clock, and its leader samples it at each renewal, as serve wires them. A step moves only the clock it names: the driver keeps the database clock's forward steps within a lease term below the lease's margin (R39). The ids are sequential and the revisions fixed, the seed choosing which is current, so that the seed decides everything (amended in execution, 2026-11-03: three revisions, as "The interleaving test" now says). The log records each event, then each port call it caused, with its instance and the virtual time; a timer's cancel is not logged. The driver keeps the replay's four conditions ("What a seed replays"): it waits at `RecordKillSwitchOn`'s hand-offs, never ends a leader's context, models a crash by discarding the instance and its timers, and lets a lease lost mid-act fence the losing instance before the other's takeover acts. The world's work item type sets `autoClose.after: 1m`. R33's loops were written in earlier tasks; this task sorts them, where the replay test first shows them. The report gives the default sequential run's time under `-race` against R46's budget of 20 seconds. **Timebox** (R46): if the world or the replay does not work after this task's implementer's attempt and one fix round, the implementer stops and reports, and the controller takes it to the author.

**Tests (write first):** each in `internal/work/app`.
- `TestTheInterleavingFakesInjectEachFault` (a table, each row one fault on a short schedule written by hand, not drawn): a records append that commits, then loses its answer, leaves the record, and the act takes its lost-answer path (Decision 13); one that fails before its commit leaves none, and the act answers the error; the same two for a board event and a kill switch write, and a failed read of the kill switch; the other instance's take between an act's start and its first guarded write refuses the write with `ErrLeaseLost`, the coordinator fences itself and its leader hears of it, and the lease's expiry does the same; a failed renewal lets the leader's watch fence the coordinator at the lease's time less its margin; a drain fences the leading coordinator, an act then answers `ErrNotHeld`, and a `RecordKillSwitchOn` held at its write lets a `Fence` and another act through before the driver lets it go; a takeover by the second coordinator over the same stores carries the first's items on; time passing fires each timer it passes, in their order, and none it does not reach; a step of the database's clock, forward or back, fires no timer, shows in the next record's time, and reaches the coordinator's clock at the next sample, which, after a step back, holds its reading rather than go back; a step of an instance's wall clock reaches its coordinator's clock at once, and its holding's check, which a step back does not extend; a kill switch row the driver writes out of band reaches the leading coordinator at its leader's next renewal check; a launch that fails, for each reason, and one a stop cancels each reach the coordinator through `Launched`, and each class of a turn's end through `Exited`. Fails first: a build failure, no world.
- `TestASeedReplaysItsSchedule`: twenty seeds, each run four times in the sequential mode on schedules of 50 events, in a world of three agents, give equal logs of events and port calls, event for event; two different seeds give different logs. Fails, once the world exists, while a loop of the coordinator reaches a port in a map's order (R33) (amended in execution, 2026-11-03: run twice over two agents, it caught a loop in a map's order in only 12 runs of 20; unit tests over fourteen agents now pin each of R33's sorted loops besides, b2469dd).
- `TestRandomSchedulesKeepTheRecordsAndTheEpochs`: the default run's sequential schedules, each to its end and through both settling stages, keep invariant 5 after every event, and none deadlocks; a failure prints its seed and the replay command, which `-interleave.seed` runs, logging every event. Task 18's run of the nine invariants replaces it, so that each gate runs the default schedules once (R46).

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 -run '^(TestTheInterleavingFakesInjectEachFault|TestASeedReplaysItsSchedule|TestRandomSchedulesKeepTheRecordsAndTheEpochs)$' ./internal/work/app/`. Expected: FAIL, a build failure: no world.
- [ ] **Step 2: Write** the world and the driver, then run the command of Step 1. Expected: the fakes' table passes; the replay test fails while a loop reaches a port in a map's order; the run passes, or fails on a fault of the coordinator, which then gets a unit test that reproduces it first, from the seed's log, in the coordinator's tests, and its fix, in a commit of its own.
- [ ] **Step 3: Sort R33's loops,** and run the command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app`; `git commit -m "test(work): a world of fakes that drives two coordinators through seeded schedules of acts, faults and clock steps, and replays a seed"`, and each fix in its own commit before it.

### Task 18: The interleaving test's invariants, its concurrent mode, and its soak run

**Decisions:** "The interleaving test" (the concurrent mode, runs, the invariants, under clock steps), R36, R37, R38, R39, R40, R41, R42, R46. **Contracts:** C16 (`Lifecycle`, `KillSwitchSeen`, the acts' and the kill switch's outcomes), C19; `item.State.Owes` and `AutoCloseAt`.

**Files:**
- Create: `internal/work/app/interleave_invariants_test.go` (the nine invariants' checks)
- Modify: `internal/work/app/interleave_test.go` (`TestRandomSchedulesKeepTheLifecyclesInvariants` replaces Task 17's `TestRandomSchedulesKeepTheRecordsAndTheEpochs`, R46; the soak's `-interleave.for`), `interleave_world_test.go` (the concurrent mode; the checks' hooks in the fake ports, with each calling instance's holding; a schedule's real-time bound)
- Modify, for each fault of the coordinator the runs find: the file it is in, with a unit test that reproduces it first

**Interfaces:**
- Consumes: Task 17's `world`, `runSchedules`, `calls`, `writes`, `killSwitchRows`, `records`, `leading` and `lifecycles`; `item.State.Owes` and `AutoCloseAt`; `Coordinator.Lifecycle` and `KillSwitchSeen`.
- Produces: the invariants' checks, each a `func(w *world, stage string) error`; the concurrent mode; the flag `-interleave.for`, which the soak run sets.

**Notes for the implementer:** invariant 1 is checked by the fake ports themselves, at every call, against the table's latest row, whether a coordinator or the driver wrote it, and, for a row the driver wrote, the calling instance's `KillSwitchSeen()`; a call counts only while the calling instance's `Holding.Valid` holds for its epoch, as invariant 1 says. Invariant 8's pause reads the same at each automatic close. The others are checked after every event, or at the settling stage their lines name; invariant 9 compares `lifecycles()`'s before and after, and ranks items by their `item_opened` records' times and ids. Each check judges times only by comparing two of the database's, never against the order events ran in. In the concurrent mode a schedule that has not settled within 30 s of real time fails, printing its seed and every goroutine's stack. The report gives the default run's time, sequential and concurrent, on the Mac under `-race`, against R46's budget of 20 seconds, and the counts lowered if it was over. The soak run is the branch end's, not this task's gate. **Timebox** (R46): if Tasks 17 and 18 together reach about 3 days with coordinator faults still being found, the implementer stops and reports, and the controller takes it to the author as an ADR-016 signal.

**Tests (write first):** each in `internal/work/app`.
- `TestEachInterleavingInvariantCatchesItsViolation` (a table): each check, fed a world or a state that breaks its invariant, reports it, naming the invariant: a fake runner told to start a turn after an on-row a coordinator wrote committed, its answer lost (1); an item left owing the latest on-row after the first settling stage, its halt refused by a store that does not recover (2); a snapshot that holds a waiting item below the limit with its view settled, and records that hold a queued item, once quiet, with a slot free (3); records of more items investigating than the limit and one, appended by the test (4); a record that does not fold, and a write without an epoch (5); one request's key on two records (6); a stopped run with a live session (7); an automatic close recorded while the latest row is an on the instance's view has reached, and one stamped before its due (8); an item admitted while an older one, by its opening's time, stays waiting (9). Rows that must pass, so that no check is stricter than its invariant: a turn started after an on-row the driver wrote, before the instance's view reached it, and a start called by an instance whose holding had ended, which the runner refused (1); a waiting item below the limit while the view is unsettled (3); one key on two items, each its own act (6); two items admitted in the order of their openings' times, which a step back of the database's clock put out of their commit order, and the promotion of the queue's last item while older items wait (9). A check that cannot fail would let every run pass for nothing. Fails first: a build failure, no checks.
- `TestRandomSchedulesKeepTheLifecyclesInvariants`: the default run's sequential schedules, with clock steps both ways among their events, keep the nine invariants, each where its line says, after every event and at the end of each settling stage; a failure prints its seed, the invariant and the replay command. It replaces Task 17's `TestRandomSchedulesKeepTheRecordsAndTheEpochs` (R46).
- `TestConcurrentSchedulesKeepTheLifecyclesInvariants`: the default run's concurrent schedules keep the invariants at the end of each batch of events and at the end of each settling stage, under `-race`, and none deadlocks.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 -run '^(TestEachInterleavingInvariantCatchesItsViolation|TestRandomSchedulesKeepTheLifecyclesInvariants|TestConcurrentSchedulesKeepTheLifecyclesInvariants)$' ./internal/work/app/`. Expected: FAIL, a build failure: no checks.
- [ ] **Step 2: Write** the checks and the concurrent mode, then run the command of Step 1. Expected: the checks' table passes; each run passes, or fails on a fault of the coordinator, printing its seed.
- [ ] **Step 3: Fix** each fault the runs find, test first: a unit test of the coordinator that reproduces it, from the seed's log, then its fix, in a commit of its own; then run the command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app`; `git commit -m "test(work): the lifecycle's nine invariants over seeded schedules, sequential and concurrent, under clock steps both ways, with a soak run for the branch's end"`, and each fix in its own commit before it.

### Task 19: The admin API's lifecycle routes, the views, and the status

**Decisions:** 14, 5 (the drain's routing at the admin API), 12 (the drain's answers), 15 (serve's wiring of the admin API and the views), R14, R15, R17, R18, R27, R28. **Contracts:** C4; C16 (`ViewPorts`, `NewViews`, the `View`'s fields, `ClosedView`, `Views.KillSwitch`).

**Files:**
- Modify: `internal/admin/admin.go` (the ports; the status's `killSwitch` and `queue`; the view's fields); create `internal/admin/lifecycle.go` (the act routes and the kill switch's: decoding, the key, the answers, the codes) and `lifecycle_test.go`
- Modify: `internal/admin/admin_test.go` (the fixture's ports; the changed tests)
- Modify: `internal/work/app/views.go` (`ViewPorts`, `NewViews`, the view's fields, `KillSwitch`), `views_test.go`
- Modify: `cmd/agentswarm/serve.go` (the admin API's ports; `NewViews` with the coordinator's `Lifecycle`, `workpg.NewControls(s.main, d.Tenant)`, the revisions and the windows)

**Interfaces:**
- Consumes: Tasks 5, 8, 10, 12 and 16's acts; Task 9's `Lifecycle`; Task 11's `Controls`; Task 2's `AutoCloseAt`, `ClosedAt`; Task 6's delay rule.
- Produces: C4; C16's views; and:

```go
// package admin
// Ports.Coordinator gains Promote, Close, Reopen, SetKillSwitch,
// RecordKillSwitchOn and Lifecycle, as app.Coordinator has them; Ports.Items
// gains KillSwitch(ctx context.Context) (app.KillSwitch, error).
```

**Notes for the implementer:** every refusal of the new routes is `{"error", "code"}` (R15), by C4's table; the opening route and the refusals every route shares keep `{"error"}`. The act routes decode an optional body strictly; the kill switch's body is required, and its `after` must be given (R8), so it decodes into a pointer. An act's answer reads the item's view after the act, as an opening's does. On a kill switch on that answers `ErrNotHeld`, the route reads `Leader.Status()` and follows Decision 5's routing.

**Tests (write first):** each in `internal/admin`, unless named.
- `TestTheLifecycleRoutesAnswerAsTheContractSays` (`lifecycle_test.go`, a table): for each route, the fake coordinator answering each outcome and each error of C4's table, the route answers the status, body and code the contract gives: `200` with `changed`, `consequences` and `item`; a preview's `200`; `400` `invalid` and `reason_required`; `404` `no_such_item`; `409` with each code; `413`; `503` `taking_over` with `Retry-After: 1`, and `unavailable`, logged with the item; no answer quotes the request. Fails first: no such route.
- `TestALifecycleActWithoutAKeyIsRefused`: an act on an item without a key, with two, or with one of 129 characters, answers `400` `invalid` and calls no coordinator; a preview without one is taken.
- `TestALifecycleActsBodyIsDecodedStrictly`: an unknown field, two objects, or a reason of the wrong type answers `400` `invalid`; an empty body is an act with no reason.
- `TestALifecycleActsBodyOverTheCapAnswers413`: a body over 64 KiB answers `413` `too_large`.
- `TestTheKillSwitchRouteTakesAfterAndNoKey`: a body without `after` answers `400` `invalid`; an `Idempotency-Key` is not read; `after` reaches the coordinator as `Act.After`, and the actor as `admin`, an admin.
- `TestTheKillSwitchOnDuringADrainTriesTheCoordinatorFirst`: `SetKillSwitch` is called first; on `ErrNotHeld`, with the leader holding and draining, `RecordKillSwitchOn` is called with the leader's epoch.
- `TestTheKillSwitchOnDuringADrainIsRecordedAndDeferred`: that on answers `200` with `deferred: true` and no `owed` key; an off during a drain answers `409` `draining`.
- `TestAKillSwitchOnBeforeTheDrainIsPublishedAnswersTryAgain`: `ErrNotHeld` while the leader holds and does not drain yet answers `503` `taking_over` with `Retry-After: 1`, and calls no `RecordKillSwitchOn`.
- `TestALifecycleActOnADrainingInstanceAnswersDraining`: each act on an item answered `ErrNotHeld` while the leader drains answers `409` `draining`, with the drain's text.
- `TestTheStatusShowsTheKillSwitchAndTheQueue`: the status gives `killSwitch`, read from the table, and `queue`, from the snapshot, `queued` counting `Waiting`.
- `TestAStandbysStatusShowsTheKillSwitch`: a standby's status shows the table's kill switch, and a queue of zeros.
- `TestAStandbysItemViewShowsTheAutoClosePaused` (`internal/work/app`, `views_test.go`; R27): views over an empty snapshot, as a standby's, with the table on: an item awaiting its close shows `AutoClosePaused` and no `ClosesAt`; with the table off and its `OffAt` after the item's entry, `ClosesAt` is `OffAt` plus the delay; with a delay recorded as zero and `Revisions` failing, no `ClosesAt`.
- `TestAnItemsViewShowsItsLifecycle` (`internal/work/app`, the plan's): an item's view gives its incident, type and opening time; a waiting item its position (R17); an item not staffed its cause, or `kill_switch` for a killed run's item (R18); a closed item its close, with `ReopenableUntil` its close plus the reopen window and `LateRulingsUntil` its close plus the late rulings' window. The admin API's JSON of each gives C4's names, each only where it applies.
- The changed tests of "Earlier tests this plan changes", Task 19.

- [ ] **Step 1: Write the tests above, and change the earlier tests named for Task 19.** Run `go test -race -count=1 ./internal/admin/ ./internal/work/app/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the routes, the views' fields, the status's, and serve's wiring.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/admin/... ./internal/work/... ./cmd/agentswarm/...`; `go test -race -count=1 -tags integration ./cmd/agentswarm/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/admin internal/work/app cmd/agentswarm/serve.go`; `git commit -m "feat(admin): the lifecycle's routes and the kill switch's, and the item view's and the status's new fields"`.

### Task 20: The harness's lifecycle API, and scenarios 19 and 20

**Decisions:** 4, 6, 7, 8, end to end. **Contracts:** C1, C4, C9.

**Files:**
- Modify: `test/e2e/harness/admin.go` (C9's acts, kill switch and fields), `config.go` (`WorkTypeSpec.AutoClose`), `deployment.go` (`DeploymentSpec.Lifecycle`), `test/e2e/harness_deployment_test.go`
- Create: `test/e2e/lifecycle_test.go`

**Interfaces:**
- Consumes: serve with Tasks 1 to 19.
- Produces: C9's additions.

**Notes for the implementer:** "no turn after an answer" is judged by the scripted vendor receiving no request begun after it (`VendorRequest.At`), and the scripted MCP server none but a session's `DELETE`, never by serve's log: under Docker the runner logs `turn started` after its check for a stop, outside its lock, so a launch that passed the check just before `StopAll` may log just after the answer, though `StopAll` killed it. The scenarios' turns complete one tool call before they wait on a held vendor answer: a pooled session then exists to be closed, one per agent, and no call is in flight to send a cancellation after the answer. Each scenario runs under both runners; this task's gate runs them under the process runner, and the branch's end under Docker too.

**Tests (write first):** each e2e, under tag `e2e`.
- `TestTheHarnessWritesTheLifecycleBlockAndTheTypesAutoClose` (`harness_deployment_test.go`, the plan's): a deployment of `Lifecycle{Investigating: 1}` holds `lifecycle: {investigating: 1}` and neither window's key; a configuration of `AutoClose: 2 * time.Second` holds `spec.autoClose.after: 2s`; neither key appears when zero. Fails first: no such fields.
- `TestItemsPastTheLimitWaitAndStartInOrder` (scenario 19, `lifecycle_test.go`): with `lifecycle.investigating: 1`, the vendor ending the first item's turns at once and holding every later request: a second and a third item wait, at positions 1 and 2; the first stops quiet, and the second starts; a promotion starts the third at once, the status giving two investigating; a fourth item's promotion is refused, `409` `limit_reached`.
- `TestAClosedItemStopsItsRunAndReopensAndClosesByItself` (scenario 20, `lifecycle_test.go`): with an auto-close of 2 s, each turn completes one call of the scripted MCP server's tool, then waits on a held vendor answer. A close stops the run `closed` within a second; every turn it started is `Gone`; no `turn_unstarted` notice is posted; the board's stop entry says `closed`; the scripted MCP server receives a `DELETE` for each pooled session of the run, one per agent, and no other request after the answer. The close repeated with its key answers the same. A reopen returns the item to `awaiting_close`, and it closes by itself, its `closed.auto` true, about 2 s later.

- [ ] **Step 1: Write the tests above.** Run `make build`, then `AGENTSWARM_BIN=$PWD/bin/agentswarm go test -count=1 -timeout 60m -tags e2e -run '^(TestTheHarnessWritesTheLifecycleBlockAndTheTypesAutoClose|TestItemsPastTheLimitWaitAndStartInOrder|TestAClosedItemStopsItsRunAndReopensAndClosesByItself)$' ./test/e2e/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** C9's additions. A scenario that fails on a component's fault gets a component test that reproduces it first, in the component's package, then the fix, in a commit of its own.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; the commands of Step 1; and the full `make test-e2e`, under the process runner, the first gate since Plan 7d-2 that runs every earlier scenario against the lifecycle. Expected: every test `ok`, the earlier scenarios among them; `0 issues.` twice. An earlier scenario that fails gets, as Step 2 says, a component test that reproduces it first, then its fix, in a commit of its own.
- [ ] **Step 5: Commit:** `git add test/e2e`; `git commit -m "test(e2e): the harness's lifecycle API, and the queue's and the close's scenarios"`, and each fix in its own commit before it.

### Task 21: Scenarios 21 to 23: the kill switch

**Decisions:** 5, 12, end to end. **Contracts:** C4, C9.

**Files:**
- Create: `test/e2e/killswitch_test.go`

**Interfaces:**
- Consumes: Task 20's harness; `Env.Psql`, `Env.AuditVerify`, `Env.Tenant`; two instances, as scenarios 8 and 13 run them.
- Produces: the scenarios.

**Notes for the implementer:** Task 20's notes hold. Scenario 22 inserts its row with `psql`, naming `env.Tenant()`: `INSERT INTO kill_switch (tenant_id, seq, id, state, actor, reason_code, reason_text, recorded_at)`, at `clock_timestamp()`, the next `seq`, so that the chain's trigger chains it.

**Tests (write first):** each e2e, in `killswitch_test.go`.
- `TestTheKillSwitchStopsEveryRunAndStaffsNothingUntilOff` (scenario 21): with `lifecycle.investigating: 2` and an auto-close of 2 s, two items investigating, each turn having completed one tool call and waiting on a held vendor answer, and one queued: turning the switch on stops both runs `killed`, each board with one `kill_switch` notice, and leaves the queued item not staffed; the scripted vendor receives no request begun after the answer, and the scripted MCP server none but a `DELETE` for each pooled session of both runs. An opening is not staffed. The switch is held past the 2 s auto-close, and nothing closes by itself. An off naming the row before the on is refused, `409` `stale`. An off naming the latest row answers the four items that stayed unstaffed; a promotion staffs the queued one, which never ran, and about 2 s later the other three close by themselves.
- `TestAKillSwitchRowTheCoordinatorDidNotWriteStopsEveryRun` (scenario 22): the harness inserts the on-row with `psql`, a stand-in for a row whose writer crashed or lost its answer, and the chain's trigger chains it. (a) The leader is killed with runs in a round, and the row is inserted while no instance leads: the standby takes over, opens no round, and stops both runs `killed`; the vendor receives no request after its take. (b) The row is inserted while the leader leads: within two renewal periods [10 s], the read following the renewal's round trip, its sample and its check, the leader's renewal check has its coordinator stop both runs `killed`, and the vendor receives no request begun after that. In both, the chain verifies.
- `TestADrainingServeRefusesLifecycleActsButTheKillSwitchsOn` (scenario 23): during a drain held open by the turns' delayed vendor answers, each act on an item answers `409` `draining`, and the reads answer; the kill switch's on answers `200` with `deferred: true`; the standby's takeover halts every run, each of which owes the on, before any of them opens a round, and the vendor receives no request after its take.

- [ ] **Step 1: Write the tests above.** Run `make build`, then `AGENTSWARM_BIN=$PWD/bin/agentswarm go test -count=1 -timeout 60m -tags e2e -run '^(TestTheKillSwitchStopsEveryRunAndStaffsNothingUntilOff|TestAKillSwitchRowTheCoordinatorDidNotWriteStopsEveryRun|TestADrainingServeRefusesLifecycleActsButTheKillSwitchsOn)$' ./test/e2e/`. Expected: each passes, which the report says, or fails on a component's fault.
- [ ] **Step 2: Fix** what the scenarios find, each fix test first in the component's package, in a commit of its own.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; the commands of Step 1. Expected: every test `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add test/e2e/killswitch_test.go`; `git commit -m "test(e2e): the kill switch's scenarios: its stops, a row it did not write, and its on during a drain"`, and each fix in its own commit before it.

### Task 22: The documents

**Decisions:** every decision, since this task writes their amendments: its brief carries the whole Decisions section.

**Files:**
- Modify: `plan/2026-10-11-agent-swarm-platform-spec.md`:
  - Section 4.1: the four states and their transitions; the queue's order, by the opening's time then the item's id, within a holding as after a takeover (R40), its walk and its admission marks, which count toward the limit; auto-close judged on the database's time (R41); promotion by one, and of items with no run; close from every state; reopen's return, with the item reason `closed` added to the reasons; auto-close of the items a person opened, their trigger treated as resolved at their opening (approved by the author, 2026-11-01); auto-close paused while the kill switch is on; runs stop `killed` and `closed`;
  - Section 4.2: close and reopen recorded on the item, never as board entries, as extend, link and unlink will be (approved by the author, 2026-11-01); the notices `kill_switch` and `skill_yanked`; a person's text checked as an agent's; which acts need a reason; the reading of rulings on a closed item as this section's list;
  - Sections 3.5 and 5.1: key values listed inline, and routing keys and values, hold no character a reader could miss, the control characters included; an opening's title that holds one is replaced whole;
  - Section 5.1: a retried opening moves an item on only as far as the queue allows; queued items at the daily cap and at the kill switch;
  - Section 5.6: a round ended `stopped`;
  - Section 8.2: a takeover reads the kill switch, halts the runs and leaves not staffed the items that owe it, and carries on the items awaiting their close; the leader reads the kill switch at each renewal; a drain refuses lifecycle acts but the kill switch's on; a run's upstream sessions close when it stops;
  - Section 8.3: the kill switch's table, its effects, owed kills, its requests naming the latest row, its view following the table, no work started while a write's outcome is unknown (R37), and R37's residual: a turn already running when an on-row commits keeps its session until the coordinator acts on the row, at once on a success answer, at the first read that succeeds on an unknown outcome; its on during a drain, written under the draining holding and bounded by the transaction timeout, the off's list, and the admin API standing in for sign-in until Plan 9a;
  - Section 15: 7b-1's values of Decision 15 and Section 8's table, its rows fixed in code without brackets; and its status line.
- Modify: `plan/adr/ADR-004-*.md`: auto-close's timers come from the records, pause with the kill switch, and close nothing before their deadline on the database's time: a timer that fires early arms again, and the store refuses a close stamped before its due (R41).
- No change for 2026-11-02 to `plan/adr/ADR-016-delivery-build-before-team.md`: its History entry of that day, which 2fc0b9d wrote, records the trigger's firing at design time, the factor of about 1.3–1.45 for the plans after 7b-1, and the interleaving test, and its Consequences carry the corrected totals. A History line is added only if 7b-1 as executed comes to more than about 3.25 person-weeks, the size the author accepted with this plan's approval on 2026-11-02. The line states the size as executed, for the author to weigh under the trigger.
- Modify: `plan/2026-10-12-implementation-roadmap.md`: 7b-1's row executed, its size as executed beside the design's 2.45 and the 2.95–3.25 accepted with this plan, and its calendar time; Section 5's account of its execution; the status line; the sweep's four rows named among its carried fixes. 2fc0b9d already records the design's size, the trigger's firing, the author's acceptance and the factor, which this task leaves as they are.
- Modify: `plan/2026-10-20-plan-7d1-serve-and-harness.md`: a note under its Contracts C1, C3 and C4 naming this plan's amendments.
- Modify: `README.md`: the lifecycle routes and the kill switch; a tenant of the anchors' form, and serve's refusal of a non-empty `MCPGODEBUG`; the registry's refusal of key values listed inline that hold characters a reader could miss, tab and line feed included: remove any such value from the configuration repository before upgrading, since an open item locked at a commit that holds one cannot rebuild its revision under the new rule; migration 0010's deploy order (stop the older instances, migrate, then start the new build, which is downtime with two instances), and what it costs a rollback (a roll forward, or a restore that loses what was written since the backup); a restore can lose an on-row of the kill switch: read the switch after a restore, and turn it on again if an on was lost; the anchor owner's read-only role needs `SELECT` on `kill_switch`; the admin token now authorizes close, promotion and the kill switch's off, and under the process runner a turn runs as serve's user and can read the secrets directory, which the Docker runner keeps out of a turn's reach; an older build's `agentswarm conclude` refuses a board that holds a `skill_yanked` notice; while a kill switch write's outcome is unknown no work starts and a promotion answers `503`, until a read settles it, logged as `kill switch unsettled` and `kill switch settled`, though a turn already running when the on-row committed keeps its session until then (R37's residual); the interleaving test, its seeds, how to replay one, its clock steps, and its soak run.
- Modify: this plan's status line.

**Interfaces:**
- Consumes: Tasks 1 to 21.
- Produces: the documents.

**Checks:**
- Every bracketed value this plan adds appears in spec Section 15, and every value fixed in code is written without brackets.
- `grep -rniE "emplo[y]ee" README.md plan/*.md plan/adr/*.md` finds no new line but this plan's vocabulary rule.

- [ ] **Step 1: Write the documents.**
- [ ] **Step 2: Run the checks.** Expected: as stated.
- [ ] **Step 3: Gate.** `make lint`. Expected: `0 issues.` twice.
- [ ] **Step 4: Commit:** `git add plan/2026-10-11-agent-swarm-platform-spec.md plan/adr plan/2026-10-12-implementation-roadmap.md plan/2026-10-20-plan-7d1-serve-and-harness.md plan/2026-11-01-plan-7b1-coordinator-lifecycle-core.md README.md`; `git commit -m "docs: Plan 7b-1's amendments to the spec, ADR-004, the roadmap and Plan 7d-1, and the README"`.

## The branch's end (the controller's)

- [ ] **The full `make test-linux`,** before the final review: `make test` and `make test-integration` on Linux, the whole e2e suite under the Docker runner, scenarios 19 to 23 among it, and the process runner's scenarios that check a process is gone (Plan 7d-2's gate, which contains `make test-docker`). Expected: every package `ok`.
- [ ] **The interleaving test's soak run,** before the final review: `go test -race -count=1 -v -timeout 60m -run '^(TestRandomSchedulesKeepTheLifecyclesInvariants|TestConcurrentSchedulesKeepTheLifecyclesInvariants)$' ./internal/work/app/ -args -interleave.for=40m`, which the two tests share, each running schedules from a base seed of the clock's for half of it, and each logging its base seed and the count it ran (R46). Expected: PASS. A failure's seed becomes a unit test of the coordinator that reproduces it, then its fix.
- [ ] **The final review,** by a fresh reviewer on Opus 5.5, of the whole branch against this plan; then its fix wave, and the full `make test-linux` and the soak run again.
- [ ] **The record:** "Changes made during execution", and this plan's status line.
