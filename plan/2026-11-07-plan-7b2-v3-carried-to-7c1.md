# Plan 7b-2, version 3, carried to Plan 7c-1

This is version 3 of the Plan 7b-2 plan, kept whole. Its Tasks 5 to 12 (the resume, extensions, links, migration 0011, the admin routes, the interleaving test's second half, and scenarios 24 and 25) are the work Plan 7c-1 starts from, per the author's decision of 2026-11-07. Its Tasks 1 to 4 were executed as Plan 7b-2 (`plan/2026-11-07-plan-7b2-stale-account-closure.md`). Its two reviews are summarised in that plan's own "Plan review" section. The content below is the verbatim text of `plan/backups/plan7b2/plan-7b2.v3.md`.

---

# Plan 7b-2: a run's return and the incident — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Draft of 2026-11-07, version 3 (answers the plan review of version 1 and the re-review of version 2), for the author's review. Not approved. Three questions wait for the author ("Decisions for the author"). |
| Date | 2026-11-07 |
| Spec | `plan/2026-10-11-agent-swarm-platform-spec.md`, Sections 4.1, 4.2, 5.6 and 8.2, as amended by Plan 7b-1; binding where it speaks. |
| Design | `plan/backups/plan7b/design-v4.md` (git-ignored), the final design of Plan 7b: Section 4.2 (Decisions 16 to 18), and every place it names 7b-2: Section 1.3's carries 2 (the extension's half) and 4, Section 1.4, Contracts C1, C3, C4, C16 to C19, Section 6, R2, R5, R10, Section 8's values and Section 9's tests. Plan 7b-1 (`plan/2026-11-01-plan-7b1-coordinator-lifecycle-core.md`) as executed, its "Changes made during execution", and its "Constraints carried to later plans", Plan 7b-2, items 1 to 15, which bind this plan. |
| Roadmap | `2026-10-12-implementation-roadmap.md`, row 7b-2: about 0.5 person-weeks (design: about 0.65), the second plan of the core of Phase 0 (ADR-016), built in the order 7b-1, 7b-2 (this plan), 3c, 9a, 7c-1, 8a and 10a. |
| Size | **Raw, about 9.2 days, about 1.85 person-weeks** (a range of 8.15 to 10.3 days). **With 7b-1's executed-to-plan ratio applied, 1.08 to 1.36** (executed 3.5–4 person-weeks against its plan's 2.95–3.25), **about 8.8 to 14 days, about 1.75 to 2.8 person-weeks**, about 2 to 2.5 at the raw middle. **ADR-016's trigger fires at plan time** against the baseline this plan uses, the roadmap row as ADR-016's History of 2026-11-02 raised it, 0.5 × 1.3–1.45 = 0.65–0.73 person-weeks, whose trigger line is about 0.98–1.09; it fires against the row's unraised 0.5 too. Under A1, raw about 7.8 days (1.55 person-weeks), still above that line. See "Size". |
| How it was made | Planned from the final design, version 4, which three fresh reviews on Opus 5.5 reviewed and the author's decisions of 2026-11-01 and 2026-11-02 shaped; from Plan 7b-1 as executed, whose carried items 1 to 15 bind it; and from the code at main, 5a1321a. The controller ordered the work after 7b-1: the stale-account class is closed structurally first, then the features, then the branch's end. It carries each of the 36 tests the design names for 7b-2, `TestALateCloseRetryAfterAReopenAndAResumeLeavesTheRunGoing` among them, every one in exactly one task (Appendix A). Version 1 had one plan review by a fresh reviewer on Opus 5.5 (`plan/backups/plan7b2/plan-review.md`): approve with fixes, 1 Critical, 8 Important and 13 Minor issues. Version 2 answered every one of them; a scoped re-review on Opus 5.5 (`plan/backups/plan7b2/re-review.md`) found all 22 addressed, and one new Important (N1) and five new Minors (N2 to N6); this version, 3, answers those ("Plan review"). |
| Plan review | C1, I1 to I5 and I7 fixed as the review proposes, I4 by its first fix; I6 by rewriting A1, whose recommendation flips to keep; I8 by redoing the size on the executed-to-plan ratio and ADR-016's raised baseline. All 13 Minors taken; none declined. Each is listed under "Plan review" below. |
| Decided by the author | The decisions of 2026-10-14, 2026-11-01 and 2026-11-02 that Plan 7b-1 lists. This plan's own calls are R48 to R74, each marked "(the planner's call; cost if wrong: ...)"; approving this plan approves them. |

**Goal:** Close the stale-account class structurally, so that no in-memory account of the coordinator stays apart from the records after a lost answer followed by a failed read; then give a stopped run its way back and an item its incident: a person may extend a run's budget, while it runs or once it stopped, and a stopped run resumes on its own board; and a person may link an item into another item's incident, and unlink it. Each act is a use case of the Work context that shows its consequences first and that a retry, or a lost answer, carries on safely. Serve's admin API exposes them under both runners, through a takeover and a drain, and the interleaving test drives them among its acts.

**Architecture:**
- **First, the carried structural work** (Plan 7b-1's carried items 12 to 15). The interleaving world fails reads after write failures, in every store an act reads after its write, and gains invariant 10, nothing runs for a closed item, so that the faults the final review found by hand, I1 and I2, fall to the test when their fixes are reverted. `steps` reloads an item after a step whose outcome is unknown, `walkSoon` kept as the backstop. An on's answer names among its stops only the runs it killed. A step in flight at a drain's first moment logs no Error line. A 40-minute soak run gates the features.
- **The records** (Decisions 16 to 18; C19). `run_extended`, `item_linked` and `item_unlinked`, admitted by the fold's table; a run's budget is its creation's plus every extension's; a run keeps a history of its stops; a resume's `killSwitchSeen` replaces the creation's in the owed-kill rule. Migration 0011, at its own level.
- **The board's `resume` entry** (Decision 16; C18), the coordinator's alone, which lifts a stop.
- **Extend** (Decision 17). On a run that runs, the budget rises at once and the open round's sessions move again with it. On a stopped run whose item awaits its close, the run resumes (Decision 16): `open` posts the `resume` entry, then a normal round, under the rule of the limit and one.
- **Link and unlink** (Decision 18): one item moves; a link record carries the new incident in its column and is chained under its destination; the incident's items come through migration 0011's index.
- **The admin API** gains extend, link and unlink, their codes, and the item view's budget, incident items and extension offered; the deployment's `lifecycle` block gains `extension`.
- **The interleaving test** gains extension, link and unlink among its acts, and its invariants read a resumed run's owed kill and second stop, and a link's incident.
- **The order.** The world's failing reads and invariant 10; the reload; the two small carried fixes; the checkpoint's soak (Task 4); the records and 0011; the board's entry; extensions of a running run; the resume; links; the admin API; the interleaving test's second half; the harness and scenarios 24 and 25; the documents; the branch's end.

**Tech Stack:** as Plan 7b-1: Go 1.26 (`go 1.26.0`); pgx v5.10.0, testify v1.12.1, testcontainers-go v0.44.0, modelcontextprotocol/go-sdk v1.8.0, go.yaml.in/yaml/v3 v3.0.5; no module joins the graph; Postgres 17 (`postgres:17-alpine`) in labelled containers.

**How a task runs.** As in Plan 7b-1. The plan is light: each task gives its decisions, the interfaces it consumes and produces, and its tests by name, with what each asserts and why it fails before the implementation. It gives no implementation code. At execution each task's code is written test first. The listed tests are the minimum; a test's name may change to match the code's vocabulary, with a ruling in the ledger. Tests the design does not name, which this plan adds, are marked "(the plan's)". A test marked a guard passes at Step 1. Each task is reviewed by a fresh reviewer on Opus 5.5, Tasks 1 and 8 by two, and the whole branch at its end. Each task's brief carries, verbatim, the Global Constraints, the Decisions and the Contracts it names.

## Decisions for the author

Three questions only the author can answer. Each has the planner's recommendation and its cost if wrong. Everything else is the planner's call, marked in place.

**A1. Move links and unlinks (Decision 18) to Plan 7c-1?** Plan 7c-1, three plans later in the build order, is links' first real consumer: it links the new item a flap opens after a close to the previous one, and it brings `incidentRef` and correlation. Until then nothing calls a link but the admin API: Plan 9a, before 7c-1, shows items and the kill switch, and the human link ruling is Plan 9b's, in the onboarding build.
- **No contradiction forces the move** (version 1 overstated it). Decision 18's [72 h] window is a check of `Coordinator.Link`, a person's act. 7c-1's flap link is the platform's own record, written by the coordinator when an inbound event opens the new item, as a firing resume is the platform's own: it reuses `item_linked`, the fold and `record`'s incident (R59), not the person's checks. 7c-1 must decide whether its platform link takes any window, and it must decide that whether links are built now or then. The argument for moving is size alone.
- **What moves:** Task 9 whole (the records `item_linked` and `item_unlinked`, `Link`, `Unlink`, `Records.IncidentItems`, 11 design tests); the link and unlink routes and the view's `incidentItems` from Task 10; the link and unlink acts and invariant 5's link check from Task 11; scenario 25 from Task 12. About 1.4 raw days, about 1.5 to 1.9 with the ratio.
- **What stays:** migration 0011, with its index (C17), since `run_extended` is a new kind and needs a migration at its own level anyway, and a no-op would not pass the migration classifier; 7c-1 then needs no index of its own.
- **Moving: what it costs.** It saves nothing overall: about 1.4 raw days shift to 7c-1, plus a re-entry into a design that three reviews closed, about a quarter to half a day. Plan 9a's item view shows no incident's items. 7b-2 does not get under the trigger by it (Size): raw 1.55 person-weeks under A1 against a line of about 1.09.
- **Keeping: what it costs.** About 1.4 raw days now, about 1.5 to 1.9 with the ratio, in a plan already over the trigger; links built and tested, then unused until 7c-1, about three plans later.
- **Recommendation: keep links in 7b-2.** With the contradiction gone, the move is a schedule shift that does not bring 7b-2 under the trigger and costs a context switch and 9a's incident items. Cost if wrong: about 1.5 to 1.9 days spent now on code no one calls for three plans.
- This plan is written whole either way: the tasks and parts that would move are marked "(moves to 7c-1 under A1)".

**A2. The size crosses ADR-016's trigger at plan time.** Raw, about 1.85 person-weeks (1.65 to 2.05); with 7b-1's executed-to-plan ratio, 1.08 to 1.36, about 1.75 to 2.8. Two readings, both firing:
- **Against the baseline this plan uses, the row as ADR-016's History of 2026-11-02 raised it,** 0.65–0.73 person-weeks: the trigger's line, 1.5 times it, is about 0.98–1.09; raw is about 2.3 to 3.2 times the raised estimate. Why this baseline: ADR-016 holds the trigger, and its entry of 2026-11-02 raised the plans after 7b-1 by 1.3–1.45 in place of a re-estimate; the factor was taken from Plan 7b's growth through its design reviews, so raising the design's 0.65 by it again would count that growth twice.
- **Against the row's own 0.5** (the roadmap still prints it): the line is 0.75; raw is about 3.7 times the row.
- **Against the design's 0.65 raised, 0.85–0.94,** shown so the number is not shaded: the line is about 1.27–1.41; raw still crosses it, and raw under A1 (1.35 to 1.8) straddles it.
- **Why it grew:** the structural group, about 2.3 raw days, was in no estimate; the interleaving test's second half, about 1 day, in no design size; the branch's end, about 1 day, in neither; and the review's fixes (C1, I1 to I5) add about half a day. The features' design-sized parts, about 4.8 to 5.2 raw days with the documents, are about 1.45 to 1.55 times the design's 3.35, at or just above the top of the author's factor of 1.3 to 1.45.
- **What would bring it down:** A1, by about 1.4 raw days, still above the line; dropping Task 11, by about 1 day, at the cost of running the resume and the links under no seeded schedule, the gap through which I1 and I2 escaped 7b-1; or the structural group, which the controller's ordering decided.
- **Recommendation: accept the size, about 1.85 raw person-weeks, keep Task 11 and links (A1), and record the trigger's firing in ADR-016's History, as for 7b-1.** Cost if wrong: the core's total grows by about 1 to 2 person-weeks against the 2027-04-30 exit.

**A3. What happens when the interleaving timebox fires** (R63). 7b-1's R46 fired, and the controller let execution go on and told the author after. This plan timeboxes Task 1 at 2 days and Task 11 at 1.5 days.
- **Recommendation:** when Task 1's fires before its RED reproduces I1 and I2, execution stops for the author: the structural group's claim rests on that RED. When it fires after the RED, or Task 11's fires, the controller lets execution go on, lands what holds, records each fault class still open as a named residual with its seeds, and tells the author at once. Cost if wrong: a stop of a day or two while the author answers, or a branch that ends with a residual the author would rather have closed.

## Global Constraints

As Plan 7b-1's, which hold unchanged; restated where this plan adds to them.
- **Module and Go version.** `github.com/jchen0426/agent-swarm`, `go 1.26.0`.
- **Vocabulary.** The spec's words, and Plan 7b-1's: queue, promotion, close, reopen, auto-close, kill switch; this plan adds extension, resume, link and unlink. Agents are "AI agents" or "agents", never "employee".
- **Domain purity.** `internal/work/item`, `internal/work/board`, `internal/work/control`, `internal/work/tokens`, `internal/holding` and `internal/outcome` import no database, network or framework package.
- **Ports.** A port is declared by the package that uses it. Wiring lives in `cmd/agentswarm`.
- **Decide, record, act.** Every decision the coordinator makes is a work record written before its effects, and every effect can be repeated.
- **Audit records.** Append-only, chained, written with the database's `clock_timestamp()`.
- **The holding.** Every guarded write runs under a context that carries its holding's epoch. Every request to the model vendor and to an MCP server, and every turn's start, is preceded by `Holding.Valid` for that epoch (Plan 7d-1's carry). This plan adds no external request: a `Sessions.Move` is in the process, and the `resume` entry is a board write, guarded as every board write is.
- **Secrets.** As Plan 7b-1's: a session token never appears in a record, a log, an error, a board entry, or anything the vendor or an MCP server receives.
- **Migrations.** Expand-only, each at its own level unless it declares an older reader. This plan adds one, 0011, at level 11, which declares none (Decision 2's rule; C17).
- **Libraries.** No new module. Code under `test/e2e` imports the standard library, testify and testcontainers-go only.
- **Values.** A value fixed in code is written without brackets; a bracketed value is a default a deployment may change (spec Section 0).
- **Test first.** Every listed test is written before its implementation and seen to fail for the reason its line gives, but a guard.
- **Language.** Code, comments and documents in English.
- **Docker labels.** Every container a test or a Makefile target starts carries `agentswarm.project=agent-swarm`.
- **A person's text.** A reason's text is never stored or shown unredacted, and never quoted back in an error (Plan 7b-1, Decision 9): every act here passes its reason through `checkReason`.
- **No work while unsettled.** A start of work this plan adds, the resume, waits while the coordinator's view of the kill switch is unsettled, as `begin`, the step and the walk do (Plan 7b-1's rule for every later plan; R37).
- **The gates.** As Plan 7b-1's (R29, the controller's rule): every task ends green on the Mac with `make lint` at `0 issues.` twice, its own packages' tests with `-race`, the integration-tagged ones among them, and, from Task 1 on, the interleaving test's default run once within its budget of 20 seconds; e2e tasks run their scenarios under the process runner. The full `make test-linux`, which runs the e2e suite under the Docker runner (`make test-docker`'s path, Plan 7d-2's carried rule), and the 40-minute soak run each run twice, at the branch's end, before the final review and after its fixes; the soak runs once more at Task 4, the checkpoint after Task 3. Commit by path, never `git add -A`; no `Co-Authored-By` line.

## Review Focus

The design's five conditions for 7b-2 (its Section 10) each have their tests, in the tasks Appendix A names. These five more are input classes no design test reaches, each likely to bite a person; each has a test of the plan's in the task that owns its code.

1. **A resume or an extension whose outcome is unknown,** its append's answer lost and the lookup after it failed too: the stale-account class again. A person expects the limit and one to hold meanwhile, and a raise of tokens to reach the open round once found. (`TestAResumeWhoseOutcomeIsUnknownCountsTowardTheLimit`, Task 8; `TestAnExtensionFoundAfterALostAnswerMovesTheOpenRoundsSessions`, Task 7)
2. **An extension that races its run's round.** An extension committed while the round's end is pending must be what that end's decision reads. (`TestAnExtensionBeforeARoundsEndIsReadByItsDecision`, Task 7)
3. **An amount at its edges on the wire:** a wall clock without a unit, negative, all zero, a fraction of a second, a token count past `int64`, an unknown field, two amounts in two objects. A person expects `400` `invalid`, never a silent zero. (`TestAnExtensionsAmountIsDecodedStrictly`, Task 10)
4. **A link that follows a chain:** A linked to B after B was linked to C takes C's incident, not B's first; a link with a key used for an unlink is `key_reused`. (`TestALinkTakesTheOtherItemsIncidentNowNotItsFirst`, Task 9)
5. **A resume met by a takeover or an on before its round.** A resume committed, then a takeover before its `resume` entry is posted, or an on-row after the resume and before its round opens. A person expects the next leader to post the entry and open the round, and an on to halt the run before it opens. (`TestATakeoverCarriesOnAResumeWhoseEntryWasNotPosted`, `TestAnOnAfterAResumeHaltsTheRunBeforeItsRoundOpens`, Task 8)

## What this plan relies on from Plan 7b-1

Plan 7b-1's carried items 1 to 11, each consumed by the task named; 7b-2 changes no contract of 7b-1's but by adding to it.

| # | What | Consumed in |
|---|---|---|
| 1 | The fold's four states and its table (`admitted` in `internal/work/item/state.go`) | Tasks 5, 9 |
| 2 | `record` taking each record's incident from the fold inside the append; amended by R59 for a link | Task 9 |
| 3 | `killSwitchSeen` and `State.Owes`, extended with a run's latest resume | Task 5 |
| 4 | `Run.Halt`, `drop`, `finish`, `closedStep`, `UpstreamSessions.CloseRun`, unchanged for a resumed run's later stops | Tasks 7, 8 |
| 5 | `Actor`, `Act`, `Outcome`, `Refused`, `requestOf`, `findKey`, `carryOn` returning `(repeat, unknown bool, _ error)`, `actFound`, previews, `checkReason`, `PersonText`; R42's key scoped to its item | Tasks 7, 8, 9 |
| 6 | The account (`entry`, `entry.unsure`, `countAsPromoted`, `refresh`), the walk, `walkSoon`, `overLimit`, `publish` | Tasks 2, 8 |
| 7 | The kill switch's view and its unsettled mark (`c.killSwitch`, `c.unsettled`, `ErrUnsettled`) | Tasks 7, 8 |
| 8 | Decision 8's rule that a stale timer records nothing (`autoClose` loads and checks) | Task 8 |
| 9 | The `lifecycle` block (`deploy.Lifecycle`): `extension` added, `closedRulings` read | Tasks 9, 10 |
| 10 | The admin API's act framework (`internal/admin/lifecycle.go`: `act`, `readStrictly`, `refusal`, `consequencesOf`) and the harness's `Admin.Act` | Tasks 10, 12 |
| 11 | Migration 0010 at level 10 | Task 5 |

Items 12 to 15 are this plan's work: Tasks 1 and 11 (12), Task 2 (13), Task 3 (14 and 15).

## Decisions

The design's Decisions 16 to 18, as its reviews left them, with changes marked "(the planner's call; ...)". Plan 7b-1's Decisions 1 to 15 and its R11 to R47 hold. Values in brackets are defaults, and every new one goes into spec Section 15 (Task 13).

### The carried structural work (Plan 7b-1's carried items 12 to 15)

- **Reads that fail after a write failure** (item 12, its first half). The interleaving world never failed a read, so a lost answer followed by a failed read, the final review's I1 (a close's) and I2 (a promotion's), escaped every soaked schedule. The world gains faults of reads, for every store an act or a step reads after its write: the records' `Load`, `Active` and `UnstaffedBy`; the board events' `Load`; and the kill switch's read, which exists (`faultSwitchRead`). Each is armed per instance, since the world's faults today are per world (`w.armed[f]`, spent by any instance), and spent by the next read of its store by the instance that armed it. **Pairing** (R49, the planner's call): when a write fault of a store is spent, lost or failed, the world arms a read fault of the same store on the same instance with probability one half, from the schedule's source; read faults are also among the events a schedule picks alone, at a low weight. Cost if wrong: a rarer pairing finds the class more slowly, which the soak's 40 minutes bound.
- **Invariant 10, nothing runs for a closed item** (R48, the planner's call; widened by the plan review's I1). From the commit of an `item_closed` with no `item_reopened` after it: (a) at every port call, neither instance calls `Turns.Start`, `Sessions.Issue` or `Sessions.Move` for that item's run; and (b) at the end of every event, no live instance that still holds the lease keeps a live session of that run, and no fake runner holds an unstopped turn of it, a pending launch completed after the close among them. A call or a state counts only while the instance's `Holding.Valid` holds, as invariant 1 counts it. It holds after every event as built: `closeNow` drops the run before it answers on an unknown outcome, and a takeover fences first. I1 broke exactly this: with its fix reverted, a close whose answer was lost and whose lookup failed left the run going, so the run's timers could start a turn, and a pending launch could complete into a running turn, after the close answered; part (b) sees the second half, which no port call shows. No invariant of the nine watches a close in time: invariant 7 reads a closed item's run only once quiet, after the item's retry has dropped it, so without invariant 10 the reads' faults could not show I1. Cost if wrong: a stricter invariant than the code keeps fails correct schedules, which the violation table's "must pass" rows guard against.
- **The RED** (the controller's ruling). Task 1's runs must reproduce I1 and I2 with their fixes reverted locally, in a throwaway worktree: 60443e4 (a close whose outcome is unknown drops its run) and 49ea574 (a promotion whose outcome is unknown counts its item). Reproduced means: a seed of the default run, or of a soak no longer than 40 minutes, that fails invariant 10 for I1 and invariant 4 for I2, each replayed by `-interleave.seed`. The revert is never committed. If a clean `git revert --no-commit` conflicts with later commits, the two behaviours are reverted by hand: `closeNow`'s drop on an unknown outcome, and `promoteNow`'s `countAsPromoted`.
- **The reload after an unknown outcome** (item 13). `steps` reloads the item once after a step that failed with an error that is neither `ErrLeaseLost`, nor of a later version, nor one that came once the coordinator's context ended: the append may have landed, its answer lost, and the account would hold the item as it was until the retry. The reload refreshes the account through `load`, so the walk at the end of the advance sees what landed, at once. `walkSoon` stays the backstop for loads outside an advance. A reload that fails changes nothing: the retry is armed, as today, and the fixes of I1 and I2 cover an act's own unknown outcome. **The planner's call (R50):** reload after any such failed step, not only after a failed append, since `steps` cannot tell an append's error from a read's without threading the cause through every step; cost if wrong, one read more for each failed step, at most one per retry wait per item.
- **`carryOutKill`'s filter** (item 14). An on's answer names among its `Stopped` only the runs whose recorded stop is `killed`, and its preview the same (`owing`'s stops, which `wouldKill` reads, keep only those `runStop` gives as `killed`, which goes through `Halt` and so through `decided()`, so that the preview and the answer agree, a resumed run included; the plan review's M3 and the re-review's N2). A run whose round had decided its stop, which the on's halt recorded with the decided reason (Plan 7b-1, Decision 3), is named nowhere: it stopped for itself, and the admin API's `stoppedView`, which drops the reason, would show it to an operator as stopped by the on. The items left not staffed are named as today, in `Unstaffed`. Invariant 1's check of an on's answer (`namesEveryRun`) then exempts a run whose stop, committed after the on came, is not `killed`; the not-staffed case stays as it is.
- **The drain's residual Error line** (item 15). `failed` logs an error that wraps `context.Canceled` at Info, not Error, once the coordinator's context has ended (`c.ctx.Err() != nil`), so that a step or an act in flight at a drain's first moment logs no Error line, as the timers that fire after it already log none (the planner's call, R51: Info, as an expected event of a drain; cost if wrong, a line at the wrong level).
- **The checkpoint** (the controller's ruling; Task 4). After Task 3, before any feature, the soak runs for 40 minutes (R65); a failure's seed becomes a unit test of the coordinator, then its fix, before Task 5 starts.

### Decision 16. A stopped run resumed on its board

Spec Section 4.1: an extension, or a new firing, sends an item in `awaiting_close` back to `investigating`. Spec Section 4.2's stopped board "takes rulings only"; the author approved the resolution, a `resume` entry (2026-11-01).
- **What resumes a run in 7b-2:** an extension of a stopped run whose item awaits its close (Decision 17). Plan 7c-1 adds a new firing.
- **The fold.** `run_extended` on a stopped run is a resume, told by the run's state (stopped), never by its fields: it lifts the run's stop, which it moves to the run's history of stops (`Run.Stops`), and the item's `awaiting_close` (`State.Closing` cleared, the item `investigating`). A resume records `killSwitchSeen`, the view's `Seq`, 0 included where no kill switch row was ever written; an extension of a running run records none, and the fold refuses a nonzero one there (the plan review's I3). The owed-kill rule reads `Run.KillSwitchSeen()`, the largest of the creation's and every resume's. `hold` copies it into `running.killSwitchSeen`, where it copies the creation's today.
- **Positions** (R67, the plan review's C1). The fold keeps where each extension and each resume came among the run's rounds: each extension with the number of rounds the run had when it landed, and `resumedAt`, `len(Rounds)` at the latest resume. `Run.Resumed()` is `Stopped == nil && len(Stops) > 0 && len(Rounds) == resumedAt`: resumed, and no round opened since. It holds for a run stopped between rounds, or before its first round, as for one whose round decided its stop. `SessionBudget` reads the extensions' positions.
- **One reader of a decision** (R67). `Run.decided()` gives the latest round's decision, and, while `Resumed()`, `{Next: NextRound}`. `Run.open`, `Run.Open`'s wrap-up branch, `mayStop`, `Run.Halt`'s first branch, `Run.Stop` and `runStep`'s conclude case read it, where each reads `Latest().Ended.Decision` today. So a resumed run of a round that decided `budget_exhausted` opens a round rather than concluding again, and a halt before its round, a kill or a close, records `killed` or `closed`, not the old decided reason.
- **The round after a resume.** `runStep` skips its conclude case while `Resumed()`, and the next step, `open`, posts the board's `resume` entry (C18), which lifts the board's stop; then it posts the board's `StartRound` for the latest round the records hold, a repeat when the board has it, as `missed` does for a round it ends (R68, the plan review's I4: a halt can end a round in the records whose `StartRound` never reached the board, which would leave the board one round behind and refuse round N+1 for ever); then it records the next round, numbered after the last, as a normal round, whatever the last round decided. The `resume` entry comes first, before every other post of `open`, the trigger's platform evidence of a run killed before its first round among them, since a stopped board refuses a new post of any kind but a ruling (the re-review's N3). Before that round opens, a halt is admitted as before a first round (`mayStop`, through `decided()`). The board's earlier stop entry stays; when the resumed run stops again, `finish` posts a second stop entry, and the item's second `item_awaiting_close` follows.
- **A board that never took its stop** (R73, the re-review's N1). A run can be stopped in the records while its board holds no stop entry: `closedStep` posts a closed item's stop once and best effort, and a reopen then leaves the item awaiting its close, which no step finishes again; Decision 16's own advice for a `stopping` item, close then reopen then extend, can lead there. A resume of such a run must not fail for ever on the board's refusal of a `resume` on a board never stopped. So `open`, after a resume, reads the board first (`Boards.Report`'s stop): a board with no stop entry gets no `resume` entry, since a board that never stopped takes rounds as it is; R68's `StartRound(N)` and round N+1 follow as usual. `open` never re-posts `Boards.Stop` to repair such a board: once a resume entry stands, a `Stop` would post a new stop entry, and a takeover's `open` would stop the board again. C18's rule, and the design test `TestABoardResumesOnlyAfterAStop`, stand unchanged.
- **A halt between a resume's record and its entry** (the re-review's N5). A resume whose `resume` post failed, then an on or a close before the round opens: `finish` or `closedStep` posts `Boards.Stop` on a board still stopped, which repeats the first stop entry. The board then shows the old reason, `budget_exhausted` say, while the records say `killed` or `closed`. This is harmless and stays so: the run's latest stop is its records', never its board's; only a stop after a `resume` entry has a stop entry of its own (Task 11's invariant 7), and Plan 8b reads the records. Posting the entry repeats safely, so a takeover carries a resume on.
- **What the run keeps:** its removed agents stay removed; its locked revision; its usage counts on from its last ended round.
- **The limit** (the controller's call): a resume takes the items investigating to the limit and one at most, as a promotion does (`overLimit`), and is refused beyond, `409` `limit_reached`, with its consequences.
- **Refused, `409`,** with its consequences, when it would not open a round, in this order (R60, the planner's call on the order; cost if wrong, a person sees one code where two apply): the item is closed (`closed`: reopen it first); it has no run (`not_started`: promote it); the kill switch is on (`kill_switch_on`); the day's spend reached the cap (`daily_cap_reached`); the run has no agent left (`no_agents_left`); the budget extended is still used (`would_stop_at_once`); past the limit and one (`limit_reached`). While the run's stop is recorded and its `finish` has not ended, it answers `503` with `Retry-After: 1` (`stopping`); a person closes an item stuck there, then reopens and extends it. So does an extension while the run's latest round decided a stop that is not recorded yet, whose next step concludes that decision whatever the extension says (R69, the plan review's I5).
- **While the view is unsettled** (R52, the planner's call; the design is silent, Plan 7b-1's R37 came after it): a resume answers `ErrUnsettled`, `503` `unavailable`, after the checks of a closed item and of `not_started`, as a promotion does. Cost if wrong: a person retries a few seconds later.
- **A resume whose outcome is unknown** (R53, the planner's call; the stale-account class, not in the design): its append failed and the lookup after it failed too (`carryOn`'s `unknown`). The account counts the item as investigating until its next load or record refreshes its entry, through `entry.unsure`, as a promotion's does (`countAsPromoted`, renamed `countAsStarting` and its doc widened), so that a second resume, a promotion or the walk cannot take the items investigating past the limit and one meanwhile. Cost if wrong: invariant 4 breaks under a lost answer and a failed read, as I2 did.
- **A board that refuses the `resume` entry** leaves the item investigating with no round; its step fails and is tried again; a person may close it (Plan 7b-1, Decision 6).
- **Plan 8b** decides which stop its cutoff reads for a run that stopped more than once.
- **An off's list after a resume** (the plan review's M13): `UnstaffedBy` matches any `item_awaiting_close` in an item's history, so an off's `stayedUnstaffed` may show a resumed item with its later state. It is harmless today, since a resume needs the switch off and an off's list is read before any resume; the next plan that reads the list across resumes filters it.

### Decision 17. Extensions

Spec Section 4.2: "Extend budget: adds an amount, by default [1 round and 5 min]"; "extend requires an amount".
- **`Coordinator.Extend(ctx, id, asked, act)`** records `run_extended`, a record of the run, with the amount, who and why: rounds, wall clock and cost-weighted tokens, each at least zero and one above it. A reason is optional (spec Section 4.2). `asked` is an `item.Amount`, whose dimensions are pointers, so that a dimension the person named as zero differs from one left out (R70, the plan review's I2); the record keeps the resolved `Budget`.
- **The run's budget** becomes its creation's plus every extension's (`Run.Budget()`). `exhausted`, `Run.Open`, the wrap-up allowance and `roundsLeft` read it in place of `Created.Budget`.
- **The amount** (the controller's call). An extension of N rounds adds, for each dimension it leaves out, N rounds' share of the run's own budget, its creation's (`item.ExtensionAmount`): its tokens divided by its rounds, and its wall clock divided by its rounds, each by integer division (R55, the planner's call: the creation's budget is the locked type's, so no revision is read; cost if wrong, none, since a run keeps the revision its item locked). An amount above the run's creation's budget in any dimension is refused, `400` `invalid` (the designer's call: it bounds `5h` typed for `5m`).
- **Every extension is refused while the kill switch is on** (`kill_switch_on`), and answers `ErrUnsettled` while the view is unsettled (R52): a run whose halt failed still runs in the records, and an extension must not touch its owed kill.
- **On a run that runs:** the budget rises in the records at once, unless its latest round decided a stop not yet recorded (`stopping`, R69). Rounds and wall clock take effect at the round's end, which reads them. Tokens take effect at once: the coordinator moves the open round's sessions again with `Run.SessionBudget(round)` (R5: the round's recorded budget plus the tokens extended since it opened). The open round's deadline does not move. No limit applies, since the item stays investigating.
- **How sessions move again** (R54, the planner's call): `running` keeps the budget it last moved the round's sessions with, and the step moves them again whenever `Run.SessionBudget` for the open round is above it, in `runStep` after its `c.unsettled` case, so that nothing moves while the view is unsettled (R37), and `hold` moves them with `Run.SessionBudget(o.Round)`, not `o.Budget` (the plan review's M4), so that the extension's own advance, a found key, a retry and a takeover's `hold` raise it the same way. Cost if wrong: none; one comparison per step.
- **On a stopped run:** the run resumes, by Decision 16's rules and refusals.
- **An extension whose outcome is unknown, on a run that runs,** changes nothing in memory: the sessions keep their budget until the item's retry finds the record and the step moves them (R54); it fails safe, with less budget for a retry wait.
- **`lifecycle.extension`** [1 round and 5 min] is the amount the interface offers (C1); the item view gives it as `extensionOffered`, each dimension clamped to the run's creation's budget, so that the offer is never one `Extend` refuses for a type whose wall clock is below 5 min (R71, the plan review's M8). The admin API never applies it for a person: a body without an amount is refused.
- **Who:** the admin API now; Plan 9b: the item's responders. **Idempotency:** by its key alone, since an extension's effect is not idempotent; a lost answer is carried on as Plan 7b-1's Decision 13 says. **Takeover:** carried on from the records. **Drain:** `409` `draining`. **Audit:** `run_extended` is a work record. **Migration:** 0011.

### Decision 18. Links between items, and migration 0011 (moves to 7c-1 under A1)

Spec Section 4.1: "A later `incidentRef`, or a responder's link ruling, can join items into one incident." Section 4.2: "Link or unlink items", with a preset reason.
- **A link moves one item** (the designer's call). `Coordinator.Link(ctx, id, with, act)` records, on the item `id`, `item_linked`, which gives it the incident that `with` has now. The other item's records do not change.
- **Unlink** (`Coordinator.Unlink(ctx, id, act)`) records `item_unlinked`, which gives the item a new incident of its own, a new id from `IDs.NewID()`, as `Open` gives one (R61; the designer's call: not its first incident).
- **An item's incident is its latest record's column.** A link record's column holds the new incident and its body names the incident it left (`left`), so the move is chained under its destination, and an item that records nothing after its link is among its new incident's items. Carry 4 holds: an item's incident changes only through these records.
- **Where the new incident comes from** (R59, the planner's call; the code as built differs from the design's words). `record` takes each record's incident from the fold of the records before it, and an opening's from the state it is given. A link's new incident cannot come from that fold, since the fold takes it from the record's own column, so `rec` gains an `incident` field, which a link or an unlink sets, as an opening's comes from `s`; the fold checks that `left` equals the incident before the record and sets `State.Incident` from the column. Every later record takes the new incident from the fold, as today. Cost if wrong: none; the design's rule, that a record's incident is the item's after it, holds.
- **The incident's items** are the items whose latest record is in it (`Records.IncidentItems`), read through migration 0011's index on `work_records (tenant_id, incident_id, item_id)`. The item view shows them.
- **Checks, in this order** (R60): not the item itself (`400` `self`); the other item exists (`404` `no_such_item`); neither is held by a later build (`409` `held_by_later_build`); a closed item, either one, is linked or unlinked only within `lifecycle.closedRulings` [72 h] of its close (`409` `rulings_window_passed`); a reason is required (`400` `reason_required`). Linking an item already in that incident, or unlinking one alone in its incident, changes nothing (`changed: false`). "Alone" is read from `IncidentItems` under the coordinator's lock; a read that fails answers `503` `unavailable`.
- **The window** is `Options.ClosedRulings`, which the coordinator gains (the design's C16 gave it to the views alone; R64).
- **Effects:** none on runs or boards. **Who:** the admin API; Plan 9b. **Idempotency:** by its key, and by its effect. **Takeover:** nothing to carry on. **Drain:** `409`. **Audit:** work records, chained under the incident of their column.
- **Migration 0011** adds the index at its own level, 11, declaring no older reader, so that no 7b-1 build meets 7b-2's records. Under A1, 0011 keeps the index all the same (C17): a no-op statement would fail the migration classifier.

### Resolutions

The planner's calls, numbered after Plan 7b-1's R11 to R47:
- **R48.** Invariant 10, nothing runs for a closed item (above).
- **R49.** Read faults paired with write faults, half the time, and alone at a low weight (above).
- **R50.** `steps` reloads after any failed step but a lost lease, a later version or an ended context (above).
- **R51.** `failed` logs `context.Canceled` at Info once `c.ctx` has ended (above).
- **R52.** Extensions and resumes answer `ErrUnsettled` while the view is unsettled (Decisions 16 and 17).
- **R53.** A resume whose outcome is unknown counts its item (Decision 16).
- **R54.** Sessions move again whenever `SessionBudget` exceeds what they were moved with (Decision 17).
- **R55.** An extension's shares come from the run's creation's budget, by integer division (Decision 17).
- **R56.** `would_stop_at_once` is judged by `Run.WouldOpen(after Budget)`: the latest ended round's usage, under the budget after the extension, would not be exhausted by `exhausted`'s rule; so an extension of wall clock alone, on a run whose rounds are spent, is refused (scenario 24). Cost if wrong: a resume that opens a round and stops at once, the anti-goal.
- **R57.** A resume's and an extension's consequences give `budget` (before, after, used) and `resumes`; a resume's give the items investigating and the limit, a running run's extension gives neither, as no limit applies.
- **R58.** The `resume` entry's numbers are the latest resume's amount, not a running-run extension's that landed after it, nor the run's new totals, as C18's body says (`rounds`, `wallClock`, `tokens`).
- **R59.** A link's and an unlink's incident come from the `rec`, as an opening's does (Decision 18).
- **R60.** The orders of the refusals (Decisions 16 and 18).
- **R61.** An unlinked item's new incident is a new id from `IDs`.
- **R62.** An extension of an item with no run, queued, investigating before its run's creation (the plan review's M7), or awaiting its close not staffed, is refused `not_started`, whatever the amount; of a closed item, `closed`.
- **R63.** The interleaving timebox: Task 1 at 2 days, Task 11 at 1.5 days; when it fires, the implementer stops and reports the seeds, the invariants broken and the faults found; then A3 decides.
- **R64.** `Options.ClosedRulings` on the coordinator (Decision 18).
- **R65.** The checkpoint's soak runs 40 minutes, as the branch's end's.
- **R66.** Task 1's RED runs from a throwaway `git worktree` under the session's scratchpad, never on main.
- **R67.** The fold keeps the positions of extensions and resumes, and `Run.decided()` is the one reader of a latest decision (Decision 16; the plan review's C1).
- **R68.** `open` after a resume posts `StartRound` for the latest recorded round before it records the next (Decision 16; the plan review's I4). Cost if wrong: none; a repeat when the board has it.
- **R69.** An extension while a decided stop is not yet recorded, `Run.Concludes()` (R74), answers `stopping` (Decision 16; the plan review's I5). Cost if wrong: a person retries after a second.
- **R70.** `item.Amount` carries the asked dimensions as pointers (Decision 17; the plan review's I2).
- **R71.** The view clamps the offered extension to the run's creation's budget (Decision 17; the plan review's M8).
- **R72.** Invariant 10 is a port check and a state check after every event (the plan review's I1).
- **R73.** `open` after a resume skips the `resume` entry on a board that holds no stop entry (Decision 16; the re-review's N1). Cost if wrong: none; the board takes rounds as it is.
- **R74.** `Run.Concludes()`, exported, is the app's one reader of "the latest decision is a stop not yet recorded": the latest round decided a stop, the run has not stopped, and it is not resumed (the re-review's N2). `runStep`'s conclude case and R69's `stopping` read it.

## Contracts between tasks

### C1 (amended). The deployment file's `lifecycle.extension` (Task 10)

```yaml
lifecycle:
  extension: {rounds: 1, wallClock: 5m}   # 7b-2: [1 round and 5 min], the amount the interface offers
```
`extension.rounds` at least 0, `extension.wallClock` a duration with its unit (R2), at least one above zero; refused naming the key otherwise. `deploy.Lifecycle` gains `Extension Extension`, a struct of `Rounds int` and `WallClock time.Duration`, `deploy`'s own, as its other blocks are. Two instances set it alike.

### C3. Serve's log

No new line. A failed extension, resume or link is logged as a failed step or act is, with its item. The drain's residual line moves to Info (item 15). No line carries a person's text.

### C4 (amended). The admin API (Task 10)

| Route | Body, besides `reason` and `preview` | `200` answer |
|---|---|---|
| `POST /v1/items/{id}/extend` | `"rounds": 1, "wallClock": "5m", "tokens": 0`, each optional, one above zero | `{"changed", "consequences", "item"}`, as promote's |
| `POST /v1/items/{id}/link` | `"with": "<item id>"` | as promote's (moves to 7c-1 under A1) |
| `POST /v1/items/{id}/unlink` | — | as promote's (moves to 7c-1 under A1) |

Each takes an `Idempotency-Key`, but its preview. `consequences` gains `budget`, `{"before", "after", "used"}`, each `{"rounds", "wallClock", "tokens"}`, `wallClock` a duration string; `resumes`, a boolean; and `incident`, `{"from", "to", "items"}`. Codes 7b-2 adds: `400` `self`, and `invalid` for an amount; `404` `no_such_item` for the item linked to; `409` `not_started`, `no_agents_left`, `would_stop_at_once`, `rulings_window_passed`; `503` `stopping` with `Retry-After: 1`. `GET /v1/items/{id}` gains `"incidentItems": [...]` (moves under A1), `"budget": {"rounds", "wallClock", "tokens", "used": {...}}` for an item with a run, and `"extensionOffered": {"rounds", "wallClock"}`.

### C9 (amended). The harness's API (Task 12)

`harness.Admin` gains `Extend(t, id, key string, b ExtendBody) (int, Acted)`, `Link(t, id, key, with string, reason *Reason) (int, Acted)` and `Unlink(t, id, key string, reason *Reason) (int, Acted)`, each through `Admin.Act`; `ExtendBody{Rounds *int; WallClock string; Tokens *int64; Reason *Reason; Preview bool}`; `ItemView` gains `IncidentItems []string`, `Budget *BudgetView`, `ExtensionOffered *AmountView`; `Consequences` gains `Budget *BudgetChangeView`, `Resumes bool`, `Incident *IncidentChangeView`; `harness.Lifecycle` gains `Extension *Amount`.

### C16 (amended). The use cases (`internal/work/app`)

```go
// Extend extends id's run by the amount asked (Decision 17): a running run's
// budget rises at once; a stopped run, its item awaiting its close, resumes
// (Decision 16).
func (c *Coordinator) Extend(ctx context.Context, id item.ID, asked item.Amount, a *Act) (Outcome, error)

// Link moves id into the incident with has now; Unlink gives id an incident
// of its own (Decision 18). Moves to 7c-1 under A1.
func (c *Coordinator) Link(ctx context.Context, id, with item.ID, a *Act) (Outcome, error)
func (c *Coordinator) Unlink(ctx context.Context, id item.ID, a *Act) (Outcome, error)

// Consequences gains:
//   Budget   *BudgetChange
//   Resumes  bool
//   Incident *IncidentChange
type BudgetChange struct{ Before, After, Used item.Budget }
type IncidentChange struct {
	From, To item.IncidentID
	Items    []item.ID // the items of To after the act, sorted
}

// Options gains ClosedRulings time.Duration, the window of late rulings and
// links on a closed item [72 h] (R64).

// Records gains, beside Load, Active, Append and UnstaffedBy:
//   IncidentItems(ctx context.Context, incident item.IncidentID) ([]item.ID, error)
// the items whose latest record is in incident, sorted (Decision 18).

// View gains IncidentItems []item.ID, Budget *BudgetView and
// ExtensionOffered item.Budget; ViewPorts gains Extension item.Budget.
type BudgetView struct{ Budget, Used item.Budget }
```

Each act fails as Plan 7b-1's do: `ErrNotHeld`, `ErrSkipped`, `ErrNoItem`, `ErrInvalid`, `ErrKeyReused`, `ErrUnsettled`, a `*Refused`, or a store's error. `ErrStopping` (new): an extension of a run whose stop is recorded and whose `finish` has not ended, or whose latest round decided a stop not yet recorded (R69), which the admin API answers `503` `stopping`. `requestIn` (`acts.go`), through which `findKey` finds a key, learns `Extended`, `Linked` and `Unlinked` (the plan review's M2).

### C17. Migration 0011 (Task 5)

`internal/db/migrations/0011_incident_index.sql`: `CREATE INDEX work_records_by_incident ON work_records (tenant_id, incident_id, item_id)`, no `min_reader` line, so its readers are of its own level, 11. Under A1 the file keeps its index (the plan review's I7): a no-op such as `SELECT 1;` would fail `TestEveryMigrationClassifiesAndThoseOnAnOlderReaderAreOnTheAllowlist`, whose `classify` knows no `SELECT`, and the index is one non-unique `CREATE INDEX`, which Plan 7c-1 then needs not add. The allowlist and its test classify it as declaring no older reader.

### C18. The board's `resume` entry (`internal/work/board`; Task 6)

- `KindResume Kind = "resume"`; `Body.Resume *Resume`; `Resume{Cause string; Rounds int; WallClock time.Duration; Tokens int64}`, `cause` `extension` (Plan 7c-1 adds `firing`); `ResumeInput` the same fields.
- `func (s *State) Resume(c *Context, id EntryID, author Author, in *ResumeInput) (Posting, error)`: the coordinator's alone (an author of kind `AuthorPlatform`); on a stopped board, once per stop. On a board not stopped whose latest resume follows its latest stop, it repeats that entry; on a board never stopped, it is refused (`ErrInvalid`). It lifts the stop: `State.stop` becomes -1 and `Stopped()` reports false, so the board takes posts and rounds again, numbered on; a later `Stop` posts a second stop entry. Agents see it in their snapshot; it is no round's activity.
- `func (b *Boards) Resume(ctx context.Context, run board.RunID, in *board.ResumeInput) (board.Posted, error)` in `internal/work/app/boards.go`.
- Body version 1. An older build's `agentswarm conclude` refuses a board that holds a `resume` entry, a kind it does not know (the README says so).

### C19. The records 7b-2 adds (`internal/work/item`)

| Kind | Body | Of |
|---|---|---|
| `run_extended` (`KindExtended`) | `amount` (`rounds`, `wallClock`, `tokens`), `killSwitchSeen` (a resume's only), `by`, `reason`, `request` | the run |
| `item_linked` (`KindLinked`) | `left`, `with`, `by`, `reason`, `request`; its column holds the new incident | the item (moves under A1) |
| `item_unlinked` (`KindUnlinked`) | `left`, `by`, `reason`, `request`; its column holds the new incident | the item (moves under A1) |

```go
// Amount is an extension as a person asks it: a dimension left out is nil,
// one named as zero is a pointer to zero (R70).
type Amount struct {
	Rounds    *int
	WallClock *time.Duration
	Tokens    *int64
}
type Extended struct {
	Amount         Budget         `json:"amount"` // resolved by ExtensionAmount
	KillSwitchSeen int64          `json:"killSwitchSeen,omitempty"` // a resume's; 0 when no row was ever written
	By             string         `json:"by"`
	Reason         *board.Reason  `json:"reason,omitempty"`
	Request        Request        `json:"request"`
}
type Linked struct {
	Left    IncidentID    `json:"left"`
	With    ID            `json:"with"`
	By      string        `json:"by"`
	Reason  *board.Reason `json:"reason"`
	Request Request       `json:"request"`
}
type Unlinked struct {
	Left    IncidentID    `json:"left"`
	By      string        `json:"by"`
	Reason  *board.Reason `json:"reason"`
	Request Request       `json:"request"`
}

// Run gains Stops []RunStopped, its earlier stops; its extensions, each with
// the number of rounds the run had when it landed and whether it was a resume
// (the re-review's N4), which R58's latest resume's amount reads; and
// resumedAt, len(Rounds) at its latest resume (R67).
func (r *Run) Budget() Budget                // its creation's plus every extension's
func (r *Run) SessionBudget(round int) int64 // R5: the round's recorded budget plus the tokens extended since it opened
func (r *Run) KillSwitchSeen() int64         // the largest of the creation's and every resume's
func (r *Run) Resumed() bool                 // Stopped == nil && len(Stops) > 0 && len(Rounds) == resumedAt
func (r *Run) WouldOpen(after Budget) bool   // R56; true for a run with no ended round, as one killed before its first (the re-review's N6)
func (r *Run) LatestResume() (Budget, bool)  // the latest resume's amount, which the resume entry posts (R58)
func (r *Run) Concludes() bool               // R74: the latest round decided a stop, Stopped == nil, and !Resumed()
// decided is the latest round's decision, or {Next: NextRound} while
// Resumed; Run.open, Run.Open, mayStop, Halt and Stop read it (R67). Stop is
// never called while Resumed, since conclude and Halt pass it by; before the
// first round it gives StopNotStaffed, as today (the re-review's N6).
func (r *Run) decided() Decision
func ExtensionAmount(created Budget, asked Amount) (Budget, error) // shares and bounds; ErrInvalid
```

The fold's table gains `KindExtended` under `investigating` and `awaiting_close`, and `KindLinked` and `KindUnlinked` under every state. Its rules: `run_extended` names the item's run; on a stopped run, a resume, told by that state alone, only while the item awaits its close, with any `killSwitchSeen`, 0 included; on a run that runs, with none, a nonzero one refused (the plan review's I3). All at body version 1.

## Earlier tests this plan changes

Each in the task named; a test beyond this list that the changed rule reaches changes the same way, and the task's report names it.
- **Task 1:** `TestTheInterleavingFakesInjectEachFault` gains the read faults' rows; `TestEachInterleavingInvariantCatchesItsViolation` gains invariant 10's rows.
- **Task 3:** `namesEveryRun` (`interleave_invariants_test.go`) exempts a run whose stop is not `killed`; the kill switch's preview tests, if any asserts a decided stop among its stops, change as the answer's do; any test in `killswitch_test.go` or `killswitch_answers_test.go` that asserted a decided stop among an on's `Stopped` changes to assert it absent.
- **Task 5:** `TestRecordsFoldInTheLifecyclesOrderOnly` gains a row for each new kind in each state; `TestMigration10ReadersAreOfItsOwnLevel` no longer asserts that 0010 is the build's last migration (`Level() == 10`), which `TestMigration11ReadersAreOfItsOwnLevel` asserts for 0011.
- **Task 6:** the board's tests that a stopped board takes rulings only (`stop_test.go`) stand, but for the `resume` entry, which a new row names.
- **Task 7:** `TestEveryActRedactsItsReasonBeforeItIsRecorded` (`acts_test.go`) gains an extension's row; Task 9 adds a link's and an unlink's (the plan review's M11).
- **Task 8:** `countAsPromoted`'s tests in `promote_test.go` follow its rename to `countAsStarting`.
- **Task 10:** `TestTheLifecycleRoutesAnswerAsTheContractSays` gains its routes' rows; the admin fixture's fake coordinator gains `Extend`, `Link` and `Unlink`; `TestTheLifecycleBlockTakesItsDefaults` gains `extension`.

## File Structure

| File | Task | Responsibility |
|---|---|---|
| `internal/work/app/interleave_world_test.go`, `interleave_test.go`, `interleave_invariants_test.go` | 1, 3, 11 | Read faults; invariant 10; the second half's acts and invariants |
| `internal/work/app/coordinator.go` | 2, 3, 5, 7, 8, 9 | `steps`' reload; `failed`'s level; `runStep`'s raise and its conclude case after a resume; `rec.incident`; `Options.ClosedRulings` |
| `internal/work/app/killswitch.go` | 3 | `carryOutKill`'s filter |
| `internal/work/item/item.go`, `state.go`, `decide.go`, `halt.go`, `lifecycle.go`, `extend.go` (new) | 5, 9 | C19's records, the fold's rows, the budget, resumes, `ExtensionAmount` |
| `internal/db/migrations/0011_incident_index.sql`; `internal/db/migrations_test.go`, `allowlist_test.go` | 5 | C17 |
| `internal/work/board/board.go`, `state.go`, `round.go`, `report.go`, `resume_test.go` (new) | 6 | C18 |
| `internal/work/app/boards.go` | 6 | `Boards.Resume` |
| `internal/work/app/extend.go` (new), `extend_test.go` (new), `rounds.go`, `turns.go`, `queue.go`, `acts.go` | 7, 8 | `Extend`, the resume in `open`, the sessions' raise, `countAsStarting`, the consequences |
| `internal/work/app/link.go` (new), `link_test.go` (new); `internal/work/app/ports.go` | 9 | `Link`, `Unlink`, `Records.IncidentItems` |
| `internal/work/memory/records.go`, `internal/work/postgres/records.go` and their tests | 9 | `IncidentItems` |
| `internal/work/app/views.go`; `internal/admin/admin.go`, `lifecycle.go`; `internal/deploy/deploy.go`; `cmd/agentswarm/serve.go`; `examples/deployment.yaml`, `examples/two-instances/a.yaml`, `b.yaml` | 10 | C1, C4, the views' fields, serve's wiring |
| `test/e2e/harness/admin.go`, `deployment.go`; `test/e2e/lifecycle_test.go` | 12 | C9; scenarios 24 and 25 |
| `plan/...`, `README.md` | 13 | The documents |

Test files: the coordinator's new tests go in `extend_test.go` and `link_test.go`, and those the design places "in restart" in `restart_test.go`.

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

**Notes for the implementer:** a read fault fails the read with an error that is neither `ErrLeaseLost` nor of a later version, before the store's lock is taken, and is logged as an event of the schedule, so the replay keeps it. A write fault spent arms its paired read fault from the schedule's random source, never from the clock; read faults are armed per instance, where `w.armed` is per world today (the plan review's M9). Invariant 10's part (a) is checked by the fake ports at every call, as invariant 1 is, and its part (b) after every event, each with the instance's holding. The read faults among the events change every seed's schedule, so 7b-1's recorded seeds no longer replay; the report and the ledger say so (the plan review's M10). **The RED, first:** in a throwaway worktree under the scratchpad (`git worktree add`, R66), revert 60443e4 and 49ea574 (`git revert --no-commit`, or by hand as "The RED" says), copy this task's test files in, and run the default run, then a soak of up to 40 minutes, until a seed fails invariant 10 (I1) and one fails invariant 4 (I2); replay each with `-interleave.seed`. The report gives both seeds, their invariants and the replay commands. Then remove the worktree. **Timebox** (R63): 2 days, the task's ceiling; if the RED has not reproduced both by then, stop and report (A3).

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

### Task 4: The checkpoint's soak run, before any feature (the controller's)

**Decisions:** "The carried structural work": the checkpoint; R65. **Contracts:** none.

- [ ] **Step 1: The soak.** `go test -race -count=1 -v -timeout 60m -run '^(TestRandomSchedulesKeepTheLifecyclesInvariants|TestConcurrentSchedulesKeepTheLifecyclesInvariants)$' ./internal/work/app/ -args -interleave.for=40m` (R65). Expected: PASS; the log gives each test's base seed and count. A failure's seed becomes a unit test of the coordinator, then its fix, in a commit of its own, and the soak runs again before Task 5. The report gives the counts beside 7b-1's last soak (37,911 sequential and 26,015 concurrent schedules).
- [ ] **Step 2: Record** the counts and base seeds in the ledger; no commit unless a fix landed, each in its own commit, test first.

### Task 5: The records of an extension and a resume, the fold, the budget, and migration 0011

**Decisions:** 16 (the fold, positions, `decided()`, `KillSwitchSeen`), 17 (the budget, the amount, R5), R55, R56, R67, R70; Plan 7b-1's Decision 2 (the table), Decision 5 (`Owes`). **Contracts:** C17, C19 (`run_extended`, the `Run` methods, `ExtensionAmount`).

**Files:**
- Modify: `internal/work/item/item.go` (`KindExtended`, `Amount`, `Extended`, `Body.Extended`, `Body.Kind`), `state.go` (the table's rows; `Run.apply` admitting `run_extended` on a stopped run while the item awaits its close, and keeping positions; `Run.Stops`; `Run.open` and `mayStop` through `decided()`), `decide.go` (`exhausted`, `Run.Open` and the wrap-up allowance on `Run.Budget()` and `decided()`; `Run.Stop` through `decided()`), `halt.go` (`Run.Halt`'s first branch through `decided()`), `lifecycle.go` (`KillSwitchSeen` and `Owes` through `Run.KillSwitchSeen()`)
- Create: `internal/work/item/extend.go` (`Budget()`, `SessionBudget`, `KillSwitchSeen`, `Resumed`, `decided`, `WouldOpen`, `ExtensionAmount`), `extend_test.go`
- Modify: `internal/work/app/turns.go` (`roundsLeft` on `Run.Budget()`), `rounds.go` (`hold` copies `Run.KillSwitchSeen()`)
- Create: `internal/db/migrations/0011_incident_index.sql`; modify `internal/db/migrations_test.go`, `allowlist_test.go` if its table of migrations lists them

**Interfaces:**
- Consumes: `item.Fold`, `State.apply`, `admitted`, `Run.apply`, `mayStop`, `Run.Open`, `exhausted`, `State.KillSwitchSeen`, `State.Owes`; `migration`, `embedded`, `Level`.
- Produces: C19's `Amount`, `Extended`, `KindExtended` and `Run` methods (`Concludes` and `LatestResume` among them), and `ExtensionAmount`, for Tasks 7, 8 and 11; migration 0011.

**Notes for the implementer:** a `run_extended` is a resume when the run is stopped, whatever its fields say. A resume moves `Run.Stopped` to the end of `Run.Stops` and sets it nil, sets `resumedAt` to `len(Rounds)`, and sets `State.Closing` nil, so `Status` reads `investigating`; every extension is kept with `len(Rounds)` at its landing. `Run.Budget()` sums; `exhausted` reads it. Every reader of the latest decision goes through `decided()`, which reads a resumed run as `{Next: NextRound}`: `Run.open` opens the next round as a normal one, `Run.Open` opens no wrap-up, `mayStop` admits a halt before it as before a first round, and `Run.Halt` and `Run.Stop` keep no old decided reason. The property test draws as Plan 7a's draws runs (`decide_test.go`).

**Tests (write first):** each in `internal/work/item`, but the migration's.
- `TestAnExtensionResumesAStoppedRun`: a run stopped `budget_exhausted`, its item awaiting its close; `run_extended` with `killSwitchSeen` folds: the item is `investigating`, `Run.Stopped` nil, `Run.Stops` holds the earlier stop, `Closing` nil. Fails first: a build failure.
- `TestAResumedRunOpensANormalRoundAfterItsDecidedStop`: after a stop decided `converged` and a resume, `Run.Open` gives round N+1, not a wrap-up, with the normal deadline; `round_opened` folds.
- `TestAResumedRunOwesAKillOnlyForAnOnAfterItsResume`: a run created at row 2, killed by row 3, resumed at row 4: `Owes(3)` is false, `Owes(5)` true; `KillSwitchSeen()` is 4.
- `TestAnExtensionOfARunningRunRecordsNoKillSwitchSeen`: `run_extended` with a nonzero `killSwitchSeen` on a run that runs is refused by the fold; without it, it folds, and `KillSwitchSeen()` stays the creation's. A resume with `killSwitchSeen` 0, no row ever written, folds as a resume (the plan review's I3).
- `TestAnExtensionRaisesARunningRunsBudget`: an extension of 2 rounds and 10 000 tokens raises `Budget()`; `exhausted` on a round that used the creation's rounds no longer decides a stop.
- `TestAnExtensionOfRoundsBringsTheirShareOfTokensAndWallClock`: `ExtensionAmount` with a creation of 6 rounds, 30 min and 6 000 000 tokens, asked `Amount{Rounds: 1}`, gives 1 round, 5 min and 1 000 000 tokens; asked 1 round with `Tokens` a pointer to 0, gives 0 tokens and 5 min; an amount above the creation's budget in any dimension, or all zero, or negative, wraps `ErrInvalid`.
- `TestARunStopsWithinItsBudgetAndItsExtensions`: the property test, 500 drawn runs with extensions, stops and resumes, each stopping within its creation's rounds, its extensions' rounds and its wrap-up rounds.
- `TestASessionBudgetCountsTheTokensExtendedSinceTheRoundOpened` (the plan's): R5: a round opened with 1 000, then 500 extended: `SessionBudget(round)` is 1 500; an extension before the round opened is in its recorded budget, counted once.
- `TestAResumedRunKilledBetweenRoundsOpensItsNextRound` (the plan's; the plan review's C1): round 2 ended deciding another round, then `run_stopped` `killed`, then a resume: `Resumed()` is true, `Run.Open` gives round 3 as a normal round, `round_opened` folds, and `Resumed()` is false after it; the same for a run killed before its first round, which opens round 1. Fails first: `Resumed()` cannot tell either run from one never stopped.
- `TestAHaltOfAResumedRunBeforeItsRoundRecordsItsOwnReason` (the plan's; the plan review's C1): a run whose round 2 decided `budget_exhausted`, stopped, then resumed: `Halt(killed)` and `Halt(closed)` before round 3 give `run_stopped` `killed` and `closed`, and the fold admits each; `Run.Stop` gives no `budget_exhausted`. Fails first: `Halt`'s first branch returns the old decided stop.
- `TestAnExtensionWouldOpenARoundOnlyIfItsBudgetAllowsOne` (the plan's): R56: wall clock alone on a run whose rounds are spent gives `WouldOpen` false; one round gives true.
- `TestRecordsFoldInTheLifecyclesOrderOnly` gains rows: `run_extended` admitted investigating and awaiting its close (a run), refused queued, closed, and awaiting its close with no run; each row failing alone when its rule is taken out.
- `TestMigration11ReadersAreOfItsOwnLevel` (`internal/db`): 0011 is embedded, at level 11, `minReader` 11, declares no reader, and `Level()` is 11; `TestMigration10ReadersAreOfItsOwnLevel` changes as "Earlier tests" says.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/item/ ./internal/db/`. Expected: FAIL, a build failure.
- [ ] **Step 2: Implement** the records, the fold, the budget, and 0011.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./internal/db/...`; `go test -race -count=1 -tags integration ./internal/db/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/item internal/work/app/turns.go internal/work/app/rounds.go internal/db`; `git commit -m "feat(work): run_extended, a run's budget with its extensions, its history of stops and a resume's place, and migration 0011"`.

### Task 6: The board's `resume` entry

**Decisions:** 16 (the board). **Contracts:** C18.

**Files:**
- Modify: `internal/work/board/board.go` (`KindResume`, `Resume`, `Body.Resume`, `Body.Kind`), `state.go` (`check` lets a `resume` through a stopped board; `record` lifts `stop`), `round.go` (`Resume`; `Stop` repeats only while stopped), `report.go` (`Stopped`), `view.go` (the snapshot shows it)
- Create: `internal/work/board/resume_test.go`
- Modify: `internal/work/app/boards.go` (`Boards.Resume`)

**Interfaces:**
- Consumes: `State.check`, `State.record`, `State.Stop`, `Stopped`, `prepare`, `repeated`, `Boards.post`.
- Produces: C18's `State.Resume` and `Boards.Resume`, for Task 8.

**Tests (write first):** each in `internal/work/board`.
- `TestAResumeEntryLiftsTheStop`: a stopped board takes a `resume` from the platform; then evidence, a round's start numbered on, and a turn end are taken; `Stopped()` is false. Fails first: a build failure.
- `TestABoardResumesOnlyAfterAStop`: a `resume` on a board never stopped is refused `ErrInvalid`; one from an agent, on a stopped board, is refused.
- `TestAResumeRepeatedReturnsItsEntry`: a second `resume` after the first, with no stop between, repeats the first, posting nothing.
- `TestAResumedBoardTakesASecondStopEntry`: after a resume, `Stop` posts a new stop entry, not a repeat of the first; both stay; `Stopped()` returns the second.
- `TestRoundsAfterAResumeFollowTheLast`: a resume after round 3 takes a round start of 4, and refuses one of 3 or 5.
- `TestAnAgentSeesTheResumeInItsSnapshot` (the plan's): the board's view for an agent shows the `resume` entry, with its numbers and no text.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/board/`. Expected: FAIL, a build failure.
- [ ] **Step 2: Implement** C18.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/board internal/work/app/boards.go`; `git commit -m "feat(board): the coordinator's resume entry lifts a stop, and a resumed board takes a second stop entry"`.

### Task 7: Extending a run that runs

**Decisions:** 17 (on a run that runs; the refusals of every extension; R5), R52, R54, R55, R57, R62, R69, R70, R74; Plan 7b-1's Decision 13. **Contracts:** C16 (`Extend`, `BudgetChange`, `Consequences.Budget`, `ErrStopping`), C19.

**Files:**
- Create: `internal/work/app/extend.go` (`Extend`: its checks, its record, its preview, its lost answer; the running case), `extend_test.go`
- Modify: `internal/work/app/acts.go` (`Consequences` gains `Budget`, `Resumes`, `Incident`; `BudgetChange`, `IncidentChange`; `requestIn` learns `Extended`), `turns.go` (`running` keeps the moved budget), `rounds.go` (`hold` moves with `Run.SessionBudget(o.Round)`, not `o.Budget`, and records it), `coordinator.go` (`runStep` moves the open round's sessions again when `SessionBudget` exceeds it, after its `c.unsettled` case; `ErrStopping`)

**Interfaces:**
- Consumes: `checkAct`, `target`, `requestOf`, `findKey`, `requestIn`, `carryOn`, `actFound`, `checkReason`, `heldThrough`, `record`, `advance`, `runStep`, `Sessions.Move`, Task 5's `Run` methods, `Amount` and `ExtensionAmount`.
- Produces: `Extend` (the running case; Task 8 adds the resume), `ErrStopping`, and, unexported:

```go
// extendNow records run_extended for s's run, with killSwitchSeen on a
// resume, and returns the act's outcome; repeat reports a found key.
func (c *Coordinator) extendNow(s *item.State, amount item.Budget, killSwitchSeen int64, a *Act, req item.Request) (Outcome, bool, error) // amount resolved by ExtensionAmount
// raise moves the open round's sessions again with Run.SessionBudget when it
// exceeds what they were moved with (R54).
func (c *Coordinator) raise(s *item.State, r *running) error
```

**Notes for the implementer:** the act's order is Plan 7b-1's: hold, form, key, load, checks, preview, record, carry on. A running run's extension records, then advances the item, whose step raises the sessions (R54); the answer is `changed: true` with `budget`. Until Task 8, an extension of a stopped run answers `ErrInvalid`; Task 8 gives it the resume.

**Tests (write first):**
- `TestAnExtensionWhileARoundIsOpenRaisesItsSessionsTokenBudget`: a run in round 2; an extension of 500 000 tokens: `Sessions.Move` is called for each agent of round 2 with the round's budget plus 500 000, the deadline unchanged. Fails first: a build failure.
- `TestAnExtensionLargerThanTheTypesBudgetIsRefused`: rounds, wall clock or tokens above the creation's budget answer `ErrInvalid`, record nothing, and move nothing.
- `TestEveryExtensionIsRefusedWhileTheKillSwitchIsOn`: with the view on, an extension of a running run and of a stopped one answer `kill_switch_on`, record nothing; with the view unsettled, `ErrUnsettled` (R52).
- `TestARetriedExtensionWithItsKeyExtendsOnce`: an extension under K, repeated under K, answers as it did; one `run_extended`; a third under K with another amount is `ErrKeyReused`.
- `TestATakeoverHoldsTheOpenRoundsSessionsToTheBudgetExtended` (`restart_test.go`): an extension during round 2, then a crash and a takeover: the new coordinator's `hold` moves round 2's sessions with `SessionBudget(2)`.
- `TestAnExtensionFoundAfterALostAnswerMovesTheOpenRoundsSessions` (the plan's; Review Focus 1): the append lands with its answer lost and the lookup fails: the act answers the error and moves nothing; after the retry wait, the item's advance raises the sessions. Fails first: nothing raises them after a found record.
- `TestAnExtensionBeforeARoundsEndIsReadByItsDecision` (the plan's; Review Focus 2): a run at its last round; an extension of 1 round commits before the round's end: the end decides another round, not `budget_exhausted`.
- `TestAnExtensionsPreviewShowsTheBudgetBeforeAndAfter` (the plan's): the preview gives `budget` before, after and used, `resumes` false, records nothing and moves nothing.
- `TestAnExtensionOfAnItemWithNoRunIsRefused` (the plan's; R62): queued, investigating before its run's creation, or awaiting its close with no run: `not_started`; closed: `closed`.
- `TestAnExtensionWhileADecidedStopIsUnrecordedAnswersStopping` (the plan's; the plan review's I5; R69): a round ended deciding `budget_exhausted`, `conclude` failing at `Boards.Assess`: an extension answers `ErrStopping`, records nothing, and the run stops as decided. Fails first: the extension records and answers `changed: true`, and the run stops anyway.
- `TestNoSessionMovesWhileTheViewIsUnsettled` (the plan's; the plan review's M4): an extension found by its key while the view is unsettled: no `Sessions.Move` until a read settles the view, then one.
- `TestEveryActRedactsItsReasonBeforeItIsRecorded` gains an extension's row (the plan review's M11).

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/app/`. Expected: FAIL, a build failure, then each for the reason its line gives.
- [ ] **Step 2: Implement** `Extend`'s running case, the raise and the consequences.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app`; `git commit -m "feat(work): extending a running run's budget, its sessions raised at once and after a takeover"`.

### Task 8: The resume of a stopped run

**Decisions:** 16 (whole), 17 (on a stopped run), R52, R53, R56, R57, R58, R60, R67, R68, R73, R74; Plan 7b-1's Decisions 4 (`overLimit`), 6 and 13 (a found close key drops nothing after a reopen), 8 (a stale timer records nothing). **Contracts:** C16 (`ErrStopping`), C18, C19.

**Files:**
- Modify: `internal/work/app/extend.go` (the stopped case: checks, `overLimit`, the unknown outcome), `rounds.go` (`open`, when `Run.Resumed()`: the `resume` entry, then `StartRound` for the latest recorded round, then the next round), `coordinator.go` (`runStep` skips its conclude case while `Run.Resumed()`), `queue.go` (`countAsPromoted` becomes `countAsStarting`), `promote.go` (its call), `extend_test.go`, `restart_test.go`, `promote_test.go`

**Interfaces:**
- Consumes: Task 7's `Extend`, `extendNow` and `ErrStopping`; Task 6's `Boards.Resume`; `Boards.StartRound`; Task 5's `Resumed`, `WouldOpen`, `KillSwitchSeen`; `overLimit`, `counted`, `investigatingWithout`, `entry.unsure`, `walk`, `finish`, `closedStep`, `actFound`.
- Produces: the resume; `func (c *Coordinator) countAsStarting(id item.ID)`.

**Notes for the implementer:** a resume's `run_extended` records `killSwitchSeen` as the view's `Seq`, 0 when no row was ever written. `open` posts the entry with the latest resume's amount (R58), then the board's `StartRound` for the latest recorded round (R68), then records the next round; a takeover's step does the same, since the entry repeats. A resume's outcome unknown calls `countAsStarting`, as a promotion's does. Two reviewers review this task.

**Tests (write first):**
- `TestAnExtensionThatWouldNotOpenARoundIsRefused` (a table, a row for each refusal, in R60's order): `closed`, `not_started`, `kill_switch_on`, `daily_cap_reached`, `no_agents_left`, `would_stop_at_once`, `limit_reached`; each records nothing and answers its consequences. Fails first: the stopped case answers `ErrInvalid`.
- `TestAResumePastTheLimitAndOneIsRefused`: with a limit of 1 and two items investigating, a third's resume answers `limit_reached`; with one investigating it resumes.
- `TestAResumeOfARunThatIsStoppingAnswersTryAgain`: a run whose stop is recorded and whose `finish` failed at the board: `ErrStopping`, recording nothing; a row for a round that decided a stop not yet recorded (R69).
- `TestAResumedRunWhoseBoardRefusesItsResumeCanStillBeClosed`: a board that refuses the `resume` entry: the item is investigating with no round, its step tried again; a close closes it and drops the run.
- `TestAnExtensionResumesAKilledRunOnceTheKillSwitchIsOff`: a run killed by an on; the off; an extension resumes it: the `resume` entry, then round N+1, its turns started; no owed kill.
- `TestALateCloseRetryAfterAReopenAndAResumeLeavesTheRunGoing`: a close under K that stopped the run; a reopen; an extension resuming the run into round N+1; the close retried under K answers as a repeat, drops nothing, stops nothing, and the round goes on.
- `TestAResumeWhoseOutcomeIsUnknownCountsTowardTheLimit` (the plan's; Review Focus 1): limit 1, one investigating, A awaiting its close with a stopped run, Q waiting; A's resume lands with its answer lost and the lookup fails: until A's retry, the snapshot counts A, a promotion of Q is refused `limit_reached`, and the walk admits nothing. Fails first: the account holds A as awaiting its close, and Q's promotion lands, three investigating.
- `TestATakeoverCarriesOnAResumeWhoseEntryWasNotPosted` (the plan's, `restart_test.go`; Review Focus 5): the resume commits and the instance crashes before `open`; the next leader posts the `resume` entry once and opens round N+1.
- `TestAnOnAfterAResumeHaltsTheRunBeforeItsRoundOpens` (the plan's; Review Focus 5): a resume commits; its `resume` post fails; an on-row comes; the step halts the run `killed` before any round opens, with no `round_opened` after the resume; the records' latest stop is `killed`, while the board's stop entry is still the first one, repeated, with its old reason (the re-review's N5).
- `TestAResumeOfARunWhoseBoardNeverTookItsStopOpensItsNextRound` (the plan's; the re-review's N1; R73): a close of a running run whose `closedStep` stop post fails; a reopen; an extension: the resume lands, `open` posts no `resume` entry, and round N+1 opens on the board. Fails first: the board refuses the `resume` as never stopped, and the step fails on every retry.
- `TestAResumedRunOfADecidedStopOpensARoundRatherThanConcluding` (the plan's; the plan review's C1): a run whose round decided `budget_exhausted`, stopped and resumed: its next step opens round N+1, with no second `run_stopped` and no `conclude`. Fails first: `runStep` takes its conclude case.
- `TestAResumeOfARunKilledBeforeItsFirstRoundOpensRoundOne` (the plan's; the plan review's C1): a run killed before its first round, the switch turned off, then resumed: round 1 opens.
- `TestAResumeWithNoKillSwitchRowEverWrittenResumes` (the plan's; the plan review's I3): the table empty, the view's `Seq` 0: the resume lands, its `run_extended` with no `killSwitchSeen` field, and round N+1 opens.
- `TestAResumeAfterAHaltOfARoundTheBoardNeverStartedOpensItsNextRound` (the plan's; the plan review's I4): round N's `round_opened` recorded, its `StartRound` failing; a close halts the run; a reopen and a resume: `open` posts the `resume` entry, `StartRound(N)`, then round N+1 opens and its `StartRound(N+1)` is taken. Fails first: the board refuses round N+1 for ever.
- `TestAResumedRunsSecondStopIsFinishedAndAwaitsItsClose` (the plan's): the resumed round decides `budget_exhausted`: `finish` posts a second stop entry, records a second `item_awaiting_close`, and arms its auto-close.
- `TestAnAutoCloseArmedBeforeAResumeClosesNothing` (the plan's): Plan 7b-1's Decision 8 for a resume: an item's auto-close timer armed, then a resume; the timer fires and records nothing.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/app/`. Expected: FAIL, each for the reason its line gives.
- [ ] **Step 2: Implement** the resume.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`, the interleaving test's default run among them; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app`; `git commit -m "feat(work): an extension resumes a stopped run on its board, under the limit and one, and a resume whose outcome is unknown counts"`.

### Task 9: Links and unlinks (moves to 7c-1 under A1)

**Decisions:** 18, R59, R60, R61, R64. **Contracts:** C16 (`Link`, `Unlink`, `IncidentChange`, `Records.IncidentItems`, `Options.ClosedRulings`), C17 (the index's use), C19 (`item_linked`, `item_unlinked`).

**Files:**
- Modify: `internal/work/item/item.go` (`KindLinked`, `KindUnlinked`, `Linked`, `Unlinked`, their `Body` fields), `state.go` (the table's rows; `apply` sets `Incident` from the column after checking `left`)
- Modify: `internal/work/app/coordinator.go` (`rec.incident`; `Options.ClosedRulings`), `ports.go` (`Records.IncidentItems`), `acts.go` (`requestIn` learns `Linked` and `Unlinked`)
- Create: `internal/work/app/link.go`, `link_test.go`
- Modify: `internal/work/memory/records.go`, `records_test.go`; `internal/work/postgres/records.go`, `records_integration_test.go`; every fake of `Records`: `oneItem` (`coordinator_internal_test.go`), `failing` (`harness_test.go`, which embeds `*memory.Records` and may need nothing), and `recordsPort` (`interleave_world_test.go`) (the plan review's M1)
- Modify: `cmd/agentswarm/serve.go` (`ClosedRulings` from `deploy.Lifecycle`)

**Interfaces:**
- Consumes: the act framework; `record`; `IDs.NewID`; `State.ClosedAt`; `isSkipped`, `ofLaterVersion`.
- Produces: `Link`, `Unlink`, `Records.IncidentItems` on both stores, for Tasks 10 and 11.

**Tests (write first):**
- `TestLinkingMovesTheItemIntoTheOtherItemsIncident`: B linked to A: B's `item_linked` has A's incident in its column and B's old in `left`; B's state's incident is A's; A's records are unchanged. Fails first: a build failure.
- `TestALinkRecordIsChainedUnderItsDestination` (`internal/work/postgres`, integration): the link record's chain scope is `incident:<A's>`; the chain verifies.
- `TestALinkedItemThatRecordsNothingMoreIsAmongItsNewIncidentsItems`: after the link, `IncidentItems(A's incident)` gives A and B, and `IncidentItems(B's old)` neither.
- `TestLinkingAnItemToItselfIsRefused`: `400` `self`, nothing recorded.
- `TestLinkingToAnUnknownItemAnswersNotFound`: `ErrNoItem` for `with`, nothing recorded.
- `TestLinkingAnItemAlreadyInTheIncidentChangesNothing`: `changed: false`, nothing recorded.
- `TestUnlinkingGivesTheItemAnIncidentOfItsOwn`: B unlinked has a new incident, neither A's nor its first; A's items are A alone.
- `TestALinkNeedsAPresetReason`: a link or an unlink with no reason answers `reason_required`; one with a code alone is taken.
- `TestAClosedItemIsLinkedWithinTheRulingsWindowOnly`: either item closed: within `ClosedRulings` the link lands; past it, `rulings_window_passed`.
- `TestTheItemsOfAnIncident` (`internal/work/postgres`, integration, and its memory twin in `internal/work/memory`): items with records in an incident, kept only when their latest record is in it, sorted.
- `TestALinkTakesTheOtherItemsIncidentNowNotItsFirst` (the plan's; Review Focus 4): B linked to C, then A linked to B: A takes C's incident; a link under a key used for an unlink of the same item is `ErrKeyReused`.
- `TestEveryActRedactsItsReasonBeforeItIsRecorded` gains a link's and an unlink's rows (the plan review's M11).
- `TestALinkWhoseOutcomeIsUnknownIsFoundByItsKey` (the plan's): the append lands, its answer lost, the lookup fails: the act answers the error; the retry under the key answers as a repeat, recording nothing more.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/...`; `go test -race -count=1 -tags integration ./internal/work/postgres/`. Expected: FAIL, a build failure.
- [ ] **Step 2: Implement** the records, `Link`, `Unlink` and `IncidentItems`.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; the commands of Step 1; `go test -race -count=1 ./cmd/agentswarm/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work cmd/agentswarm/serve.go`; `git commit -m "feat(work): link an item into another's incident and unlink it, chained under the destination"`.

### Task 10: The admin API's routes, the item view's fields, and `lifecycle.extension`

**Decisions:** 17 (`lifecycle.extension`, R2), 16 and 18 (the codes), R57; Plan 7b-1's Decision 14. **Contracts:** C1, C4, C16 (views).

**Files:**
- Modify: `internal/deploy/deploy.go` (`Lifecycle.Extension`, its default and checks), `deploy_test.go`; `examples/deployment.yaml`, `examples/two-instances/a.yaml`, `b.yaml`
- Modify: `internal/admin/admin.go` (`Ports.Coordinator` gains `Extend`, `Link`, `Unlink`; the routes), `lifecycle.go` (`actOn`, the extend and link bodies, the codes, `consequencesOf`), `lifecycle_test.go`, `admin_test.go`
- Modify: `internal/work/app/views.go` (`IncidentItems`, `Budget`, `ExtensionOffered`; `ViewPorts.Extension`), `views_test.go`
- Modify: `cmd/agentswarm/serve.go` (`ViewPorts.Extension` from the deployment)

**Interfaces:**
- Consumes: Tasks 7 to 9's acts; `act`, `readStrictly`, `refusal`, `lifecycleRefused`, `itemAfter`.
- Produces: C4; and, in `internal/admin`, unexported:

```go
// actOn serves an act whose body holds more than a reason and a preview:
// it decodes T strictly, then calls do with it.
func actOn[T any](a *api, kind string, do func(ctx context.Context, id item.ID, body *T, act *app.Act) (app.Outcome, error)) http.HandlerFunc
```

**Tests (write first):** each in `internal/admin`, unless named.
- `TestTheLifecycleRoutesAnswerAsTheContractSays` gains rows for extend, link and unlink: each outcome and each error of C4's 7b-2 codes, `503` `stopping` with `Retry-After: 1` among them. Fails first: no such route.
- `TestAnExtensionsAmountIsDecodedStrictly` (the plan's; Review Focus 3): `"wallClock": "5"`, `"-1m"`, `"1.5s"` taken as 1.5 s, all amounts zero or absent, a token count past `int64`, an unknown field, and two objects each answer `400` `invalid` (but `1.5s`), and call no coordinator.
- `TestTheItemViewShowsTheBudgetTheIncidentsItemsAndTheExtensionOffered` (`internal/work/app`, the plan's): an item with a run gives its budget with its extensions and its use; the incident's items; `extensionOffered` from `ViewPorts.Extension`, clamped to the run's creation's budget for a type whose wall clock is below 5 min (R71); an item with no run gives no budget. The admin API's JSON gives C4's names.
- `TestTheLifecycleBlocksExtensionTakesItsDefaultAndRefusesAWallClockWithoutAUnit` (`internal/deploy`, the plan's): no `extension` gives 1 round and 5 min; `wallClock: 5` is refused naming the key; all zero is refused; `TestTheExamplesLifecycleBlocksLoad` still passes with the examples' new key.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/admin/ ./internal/work/app/ ./internal/deploy/`. Expected: FAIL, each for the reason its line gives.
- [ ] **Step 2: Implement** the routes, the views' fields, the block's key and serve's wiring.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/admin/... ./internal/work/... ./internal/deploy/... ./cmd/agentswarm/...`; `go test -race -count=1 -tags integration ./cmd/agentswarm/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/admin internal/work/app internal/deploy cmd/agentswarm/serve.go examples`; `git commit -m "feat(admin): extend, link and unlink, and the item view's budget, incident items and extension offered"`.

### Task 11: The interleaving test's second half

**Decisions:** Plan 7b-1's carried item 12, its second half; "The interleaving test" of Plan 7b-1, whose invariants this task extends; R63. **Contracts:** C16, C19.

**Files:**
- Modify: `internal/work/app/interleave_test.go` (the acts `extend`, with fresh and repeated keys and previews, of running and stopped runs; `link` and `unlink` (moves under A1)), `interleave_invariants_test.go` (invariants 1, 2, 5 and 7, below; their violation rows), `interleave_world_test.go` (the fakes record `Sessions.Move`'s budget, and the board events hold `resume` entries)
- Modify, for each fault of the coordinator the runs find: the file it is in, with a unit test that reproduces it first

**Interfaces:**
- Consumes: Task 1's world; Tasks 5 to 9's acts and records; `State.Owes`, `Run.KillSwitchSeen`, `Run.Stops`.
- Produces: the acts among the events; the invariants' amendments.

**Notes for the implementer:** invariants 1 and 2 read the owed kill through `State.Owes`, which Task 5 bases on the latest resume, so their checks change only where they read `Run.Created.KillSwitchSeen` directly: they read `Run.KillSwitchSeen()`. `runOrders`, which reads only a run's latest stop, reads its intervals of running, between creation or resume and each stop, so that `namesEveryRun` and invariant 7 judge a resumed run by the interval around the on (the plan review's M5). Invariant 7 counts each stop of a run, `Run.Stops` and `Run.Stopped`, so that a resumed run's second stop must leave nothing running, and each stop after a `resume` entry has its own stop entry. Invariant 5's fold check adds: each record's incident column is the item's incident after it, a link's the destination's at its commit, and `IncidentItems` agrees with the folds once quiet. Invariant 4 counts a resumed item as investigating. A new "must pass" row: a resumed run that does not owe an on-row older than its resume. **Timebox** (R63): 1.5 days.

**Tests (write first):**
- `TestEachInterleavingInvariantCatchesItsViolation` gains rows: a turn of a resumed run started after an on-row newer than its resume (1, must fail) and one older (1, must pass); a resumed run left owing the latest on-row after the first settling stage (2); a link record whose column is not the destination's incident (5); a resumed run's second stop with a live session once quiet (7).
- `TestRandomSchedulesKeepTheLifecyclesInvariants` and `TestConcurrentSchedulesKeepTheLifecyclesInvariants` pass with the new acts among the events, within 20 seconds; each fault found is fixed test first, in a commit of its own.
- `TestASeedReplaysItsSchedule` still passes with the new acts.

- [ ] **Step 1: Write the rows above.** Run `go test -race -count=1 -run '^TestEachInterleavingInvariantCatchesItsViolation$' ./internal/work/app/`. Expected: FAIL, the rows' checks absent.
- [ ] **Step 2: Write** the acts and the amendments, then run `go test -race -count=1 -run '^(TestEachInterleavingInvariantCatchesItsViolation|TestRandomSchedulesKeepTheLifecyclesInvariants|TestConcurrentSchedulesKeepTheLifecyclesInvariants|TestASeedReplaysItsSchedule)$' ./internal/work/app/`. Expected: PASS, or a fault of the coordinator, with its seed.
- [ ] **Step 3: Fix** each fault found, test first, then run the command of Step 2. Expected: PASS; the report gives the default run's time.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app`; `git commit -m "test(work): the interleaving test extends, resumes, links and unlinks, and its invariants read a run's latest resume"`, each fix in its own commit before it.

### Task 12: The harness's acts, and scenarios 24 and 25

**Decisions:** 16, 17, 18, end to end. **Contracts:** C1, C4, C9.

**Files:**
- Modify: `test/e2e/harness/admin.go` (C9), `deployment.go` (`Lifecycle.Extension`), `test/e2e/harness_deployment_test.go`
- Modify: `test/e2e/lifecycle_test.go` (scenarios 24 and 25)

**Interfaces:**
- Consumes: serve with Tasks 1 to 11.
- Produces: C9's additions; the scenarios.

**Notes for the implementer:** Plan 7b-1's Task 20's notes hold: "no turn after an answer" is judged by the scripted vendor and MCP servers, never by serve's log. Each scenario runs under both runners; this task's gate runs them under the process runner, and the branch's end under Docker.

**Tests (write first):** each e2e, under tag `e2e`.
- `TestTheHarnessWritesTheLifecyclesExtension` (`harness_deployment_test.go`, the plan's): a deployment of `Lifecycle{Extension: &Amount{Rounds: 1, WallClock: 5 * time.Minute}}` holds `lifecycle.extension: {rounds: 1, wallClock: 5m0s}`, and no key when nil. Fails first: no such field.
- `TestAnExtensionResumesAStoppedRunOnItsBoard` (scenario 24): a run of one round stops `budget_exhausted`; an extension of 1 round resumes it, the board showing a `resume` entry; round 2 runs, and the run stops again, its board with a second stop entry. The extension repeated with its key answers the same and extends once. An extension of wall clock alone, on a run whose rounds are spent, is refused `409` `would_stop_at_once`. With `lifecycle.investigating: 1` and two items investigating, a third's resume is refused `409` `limit_reached`.
- `TestLinkedItemsShareAnIncidentAndUnlinkApart` (scenario 25; moves under A1): B, awaiting its close, linked to A shares A's incident, though it records nothing more, and both views list both items. B unlinked has an incident of its own. A link to an unknown item answers `404` `no_such_item`. The chain verifies (`Env.AuditVerify`).

- [ ] **Step 1: Write the tests above.** Run `make build`, then `AGENTSWARM_BIN=$PWD/bin/agentswarm go test -count=1 -timeout 60m -tags e2e -run '^(TestTheHarnessWritesTheLifecyclesExtension|TestAnExtensionResumesAStoppedRunOnItsBoard|TestLinkedItemsShareAnIncidentAndUnlinkApart)$' ./test/e2e/`. Expected: FAIL, each for the reason its line gives.
- [ ] **Step 2: Implement** C9's additions. A scenario that fails on a component's fault gets a component test that reproduces it first, then the fix, in a commit of its own.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; the commands of Step 1; the full `make test-e2e` under the process runner. Expected: every test `ok`, scenarios 1 to 23 among them; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add test/e2e`; `git commit -m "test(e2e): the harness's extend, link and unlink, and the resume's and the links' scenarios"`, each fix in its own commit before it.

### Task 13: The documents

**Decisions:** every decision of this plan; its brief carries the whole Decisions section.

**Files:**
- Modify: `plan/2026-10-11-agent-swarm-platform-spec.md`: Section 4.1, a resume's limit and its refusals, links moving one item within the [72 h] window, an item's incident as its latest record's; Section 4.2, a board resumes after a stop through the coordinator's `resume` entry (approved by the author, 2026-11-01), extend requires an amount, link and unlink require a reason; Section 5.6, budgets extended, a resumed run's normal round and second stop; Section 15, `lifecycle.extension` [1 round and 5 min], an extension's shares and its bound, fixed in code, and the [72 h] row covering links.
- Modify: `plan/2026-10-12-implementation-roadmap.md`: row 7b-2, its plan's size and, at the branch's end, its size as executed and calendar time; Section 5's account; under A1, rows 7b-2 and 7c-1 for the move.
- Modify: `plan/adr/ADR-016-delivery-build-before-team.md`: a History entry for the trigger's firing at plan time, with the author's answer to A2, and, at the branch's end, the size as executed.
- Modify: `plan/2026-10-20-plan-7d1-serve-and-harness.md`: a note under Contracts C1 and C4 naming this plan's amendments.
- Modify: `README.md`: the extend, link and unlink routes; migration 0011's deploy order and its rollback (a roll forward, or a restore); an older build's `agentswarm conclude` refuses a board that holds a `resume` entry; the admin token now authorizes extensions; the interleaving test's read faults and invariant 10.
- Modify: this plan's status line.

**Checks:** every bracketed value this plan adds is in spec Section 15, and every value fixed in code is written without brackets; `grep -rniE "emplo[y]ee" README.md plan/*.md plan/adr/*.md` finds no new line.

- [ ] **Step 1: Write the documents.**
- [ ] **Step 2: Run the checks.** Expected: as stated.
- [ ] **Step 3: Gate.** `make lint`. Expected: `0 issues.` twice.
- [ ] **Step 4: Commit:** `git add plan/2026-10-11-agent-swarm-platform-spec.md plan/2026-10-12-implementation-roadmap.md plan/adr/ADR-016-delivery-build-before-team.md plan/2026-10-20-plan-7d1-serve-and-harness.md plan/2026-11-07-plan-7b2-run-return-and-incident.md README.md`; `git commit -m "docs: Plan 7b-2's amendments to the spec, the roadmap, ADR-016 and Plan 7d-1, and the README"`.

## The branch's end (the controller's)

- [ ] **The full `make test-linux`,** before the final review: `make test` and `make test-integration` on Linux, the whole e2e suite under the Docker runner, scenarios 19 to 25 among it, and the process runner's scenarios. Expected: every package `ok`. A run that fails only on the carried `ETXTBSY` flake of `internal/runner/process` runs again, and the report says so.
- [ ] **The soak run,** before the final review: `go test -race -count=1 -v -timeout 60m -run '^(TestRandomSchedulesKeepTheLifecyclesInvariants|TestConcurrentSchedulesKeepTheLifecyclesInvariants)$' ./internal/work/app/ -args -interleave.for=40m`. Expected: PASS, each test logging its base seed and count.
- [ ] **The final review,** by a fresh reviewer on Opus 5.5, of the whole branch against this plan; then its fix wave; then `make test-linux` and the soak run again.
- [ ] **The record:** "Changes made during execution", the size as executed, and this plan's status line; ADR-016's History and the roadmap's row with the size as executed.

## Size

On the roadmap's yardstick, in days of one person. "Raw" is the planner's estimate. "Adjusted" applies the ratio Plan 7b-1 showed as executed against its *plan*, 3.5–4 person-weeks against 2.95–3.25, that is 1.08 to 1.36 (the plan review's I8): this raw is a plan-level figure, which already holds what grew between design and plan, so 7b-1's executed-against-design ratio, 1.4 to 1.6, which version 1 applied, would count that growth twice.

| Part | Task | Raw days |
|---|---|---|
| Read faults, invariant 10 in two parts, the RED | 1 | 1.25–2.0 |
| The reload after an unknown outcome | 2 | 0.25 |
| The filter, the preview's, and the Error line | 3 | 0.25 |
| The checkpoint's soak and any fix | 4 | 0.1–0.3 |
| **The structural group** | | **1.85–2.8** |
| Records, fold, positions and `decided()`, budget, `Amount`, property test, 0011 | 5 | 0.9–1.05 |
| The board's `resume` entry | 6 | 0.3 |
| Extending a running run, `stopping` and the raise | 7 | 0.6–0.7 |
| The resume, `runStep`, the board's catch-up round | 8 | 1.0–1.15 |
| Links and unlinks, the incident's items (A1) | 9 | 0.6 |
| The admin API, the view, `lifecycle.extension` | 10 | 0.4 |
| The interleaving test's second half | 11 | 0.75–1.25 |
| The harness and scenarios 24 and 25 | 12 | 0.75 |
| The documents | 13 | 0.25 |
| **The features** | | **5.55–6.45** |
| The branch's end: two Linux runs, two soaks, the final review and its fix wave | — | 0.75–1.05 |
| **Total** | | **8.15–10.3, about 9.2: 1.65–2.05 person-weeks** |

- **Adjusted:** 8.15 × 1.08 to 10.3 × 1.36, about 8.8 to 14 days, **about 1.75 to 2.8 person-weeks**; at the raw middle, 9.2 × 1.08 to 1.36, about 9.9 to 12.5 days, 2 to 2.5.
- **The baseline for ADR-016's trigger,** "a plan overruns its estimate by more than half": this plan uses the roadmap row as ADR-016's History of 2026-11-02 raised it, 0.5 × 1.3–1.45 = **0.65–0.73 person-weeks, a line of about 0.98–1.09**. ADR-016 holds the trigger, and that entry raised the plans after 7b-1 by the factor in place of a re-estimate; the factor came from Plan 7b's growth through its design reviews, so raising the design's 0.65 by it again would count that growth twice. **The trigger fires at plan time:** raw is about 2.3 to 3.2 times that baseline. Against the row's unraised 0.5 (line 0.75), raw is about 3.3 to 4.1 times it. Shown so the number is not shaded: against the design raised, 0.85–0.94 (line 1.27–1.41), raw crosses too.
- **Why it grew:** the structural group, about 1.85 to 2.8 days, was in no estimate; the interleaving test's second half, about 1 day, in no design size; the branch's end, about 1 day, in neither; the plan review's fixes add about half a day (C1, I1 to I5). The features' design-sized parts, about 4.8 to 5.2 raw days, are about 1.45 to 1.55 times the design's 3.35.
- **Under A1:** less about 1.4 raw days (Task 9's 0.6, about 0.35 of Task 11, scenario 25's 0.3, the routes' and documents' parts): about 6.75 to 8.9 raw days, about 7.8, 1.55 person-weeks; adjusted, about 7.3 to 12.1 days, 1.45 to 2.4 person-weeks. Still above the line of 1.09.
- **The timebox** (R63): Task 1 at 2 days, its ceiling; Task 11 at 1.5. 7b-1's interleaving tasks ran 1.5 to 2 times their timebox; the high end of the adjusted figure allows for it.

## Plan review

Version 1's review (`plan/backups/plan7b2/plan-review.md`, a fresh reviewer on Opus 5.5): 1 Critical, 8 Important, 13 Minor. What version 2 did with each:
- **C1** (the resume's place in the fold; three readers of a decided stop): fixed as proposed. Positions and `Run.decided()` (R67); `halt.go` in Task 5, `runStep` in Task 8; four tests.
- **I1** (invariant 10 saw half of I1): fixed as proposed. Invariant 10 is a port check and a state check after every event (R72); a violation row for a launch completed after the close.
- **I2** (a named zero): fixed as proposed, with `item.Amount` (R70).
- **I3** (a resume told by its field): fixed as proposed. A resume is told by the run's state; `KillSwitchSeen()` is the largest; R58 posts the latest resume's amount; a test with no row ever written.
- **I4** (a board one round behind): fixed by the review's first fix, `StartRound` for the latest recorded round before the next (R68), as `missed` already does; the second, a field on `ResumeInput`, would widen the board's contract for a case the coordinator can repair itself.
- **I5** (an extension during a decided, unrecorded stop): fixed as proposed, `stopping` (R69), with a test in Task 7 and a row in Task 8.
- **I6** (A1 overstated): A1 rewritten; the contradiction does not hold, the argument is size alone, and the recommendation flips to keep.
- **I7** (a no-op 0011 fails the classifier): fixed as proposed; 0011 keeps its index under A1 too.
- **I8** (ratio and baseline): the size redone with the executed-to-plan ratio and stated against ADR-016's raised baseline, with the other two baselines shown.
- **The re-review's N1 to N6** (version 3): N1, a board that never took its stop, by the fix that keeps `TestABoardResumesOnlyAfterAStop` unchanged: `open` skips the `resume` entry there (R73, a test in Task 8); N2, `Run.Concludes()` exported for `runStep` and R69, and `owing` filtered through `runStop`'s `killed` (R74); N3, the `resume` entry before every other post of `open`; N4, each kept extension says whether it was a resume, and `Run.LatestResume()`; N5, the old stop entry a halt repeats, stated in Decision 16 and pinned in Task 8's test; N6, `Run.Stop` never called while resumed, and `WouldOpen` true with no ended round (C19).
- **Minors, all taken:** M1 the actual fakes (Task 9); M2 `requestIn` (Tasks 7 and 9); M3 the preview filtered as the answer is (Task 3); M4 the raise after `c.unsettled` and `hold` on `SessionBudget` (Decision 17, Task 7); M5 `runOrders` over stop and resume intervals (Task 11); M6 invariant 7's quiet check acknowledged; M7 an investigating item with no run (R62); M8 the offered amount clamped (R71); M9 read faults per instance (Task 1); M10 old seeds no longer replay (Task 1); M11 the redaction guard's rows (Tasks 7 and 9); M12 Tasks 1 and 8 raised, Task 1's timebox its ceiling; M13 an off's list after a resume (Decision 16). None declined.

## Not in this plan

- **Plan 7c-1:** a resume on a new firing, its record and the cause `firing`; a firing resume waiting for a free slot; links of flaps; under A1, links and unlinks whole.
- **Plan 9a:** sign-in; the items list; the live board's merge of the item's records; the extension offered in the interface.
- **Plan 9b:** the item rulings in the interface, extend, link and unlink among them; the board rulings' use case.
- **Plan 8b:** which stop a run's cutoff reads; cross-arm links.
- A merge of whole incidents: a link moves one item.
- An extension's effect on a round already open beyond its tokens: its deadline does not move.

## Constraints carried to later plans

- **Plan 7c-1:** a firing resume reuses Decision 16's fold, `decided()` and `open`'s `resume` entry, with its own record or a `cause` of `firing`, its `killSwitchSeen`, and `countAsStarting` on an unknown outcome; it waits while the view is unsettled (R52). Its flap link is the platform's own `item_linked`, written by the coordinator; it decides whether that link takes any window, since Decision 18's [72 h] window is a check of a person's `Link`. Under A1, Decision 18 whole, 0011's index already in place. Its new kinds carry a migration at its own level.
- **Plan 8b:** read `Run.Stops` to decide the cutoff of a run that stopped more than once; read the link records for cross-arm incidents.
- **Plan 9b:** R10's residual in 7b-2's case, which `TestALateCloseRetryAfterAReopenAndAResumeLeavesTheRunGoing` pins for a found key: a late retry of a close that changed nothing, after a reopen and an extension that resumed the run, stops the resumed run mid-round; weigh the item's latest record as each act's `after`.
- **Plan 10b:** measure `Records.IncidentItems` over many items, and the session raise under many extensions.
- **Every later plan:** a start of work waits while the view is unsettled; an act whose outcome is unknown that may raise the items investigating counts its item (`countAsStarting`); new kinds carry a migration at its own level; plans that touch serve or a runner keep `make test-docker` in their gates.

## Changes made during execution

None yet. Each amendment is marked in place "(amended in execution, <date>: ...)" and listed here, one line each.

## Appendix A: the design's 36 tests, by task

`TestALateCloseRetryAfterAReopenAndAResumeLeavesTheRunGoing` is the 36th: the design names it under Decision 16, and Plan 7b-1's count of "36 tests that are 7b-2's" includes it.

| # | Test | Decision | Task |
|---|---|---|---|
| 1 | `TestAnExtensionResumesAStoppedRun` | 16 | 5 |
| 2 | `TestAResumedRunOpensANormalRoundAfterItsDecidedStop` | 16 | 5 |
| 3 | `TestAResumedRunOwesAKillOnlyForAnOnAfterItsResume` | 16 | 5 |
| 4 | `TestAnExtensionOfARunningRunRecordsNoKillSwitchSeen` | 16 | 5 |
| 5 | `TestAnExtensionRaisesARunningRunsBudget` | 17 | 5 |
| 6 | `TestAnExtensionOfRoundsBringsTheirShareOfTokensAndWallClock` | 17 | 5 |
| 7 | `TestARunStopsWithinItsBudgetAndItsExtensions` | 17 | 5 |
| 8 | `TestMigration11ReadersAreOfItsOwnLevel` | 18 | 5 |
| 9 | `TestAResumeEntryLiftsTheStop` | 16 | 6 |
| 10 | `TestABoardResumesOnlyAfterAStop` | 16 | 6 |
| 11 | `TestAResumeRepeatedReturnsItsEntry` | 16 | 6 |
| 12 | `TestAResumedBoardTakesASecondStopEntry` | 16 | 6 |
| 13 | `TestRoundsAfterAResumeFollowTheLast` | 16 | 6 |
| 14 | `TestAnExtensionWhileARoundIsOpenRaisesItsSessionsTokenBudget` | 17 | 7 |
| 15 | `TestAnExtensionLargerThanTheTypesBudgetIsRefused` | 17 | 7 |
| 16 | `TestEveryExtensionIsRefusedWhileTheKillSwitchIsOn` | 17 | 7 |
| 17 | `TestARetriedExtensionWithItsKeyExtendsOnce` | 17 | 7 |
| 18 | `TestATakeoverHoldsTheOpenRoundsSessionsToTheBudgetExtended` | 17 | 7 |
| 19 | `TestAnExtensionThatWouldNotOpenARoundIsRefused` | 17 | 8 |
| 20 | `TestAResumePastTheLimitAndOneIsRefused` | 16 | 8 |
| 21 | `TestAResumeOfARunThatIsStoppingAnswersTryAgain` | 16 | 8 |
| 22 | `TestAResumedRunWhoseBoardRefusesItsResumeCanStillBeClosed` | 16 | 8 |
| 23 | `TestAnExtensionResumesAKilledRunOnceTheKillSwitchIsOff` | 16 | 8 |
| 24 | `TestALateCloseRetryAfterAReopenAndAResumeLeavesTheRunGoing` | 16 | 8 |
| 25 | `TestLinkingMovesTheItemIntoTheOtherItemsIncident` | 18 | 9 |
| 26 | `TestALinkRecordIsChainedUnderItsDestination` | 18 | 9 |
| 27 | `TestALinkedItemThatRecordsNothingMoreIsAmongItsNewIncidentsItems` | 18 | 9 |
| 28 | `TestLinkingAnItemToItselfIsRefused` | 18 | 9 |
| 29 | `TestLinkingToAnUnknownItemAnswersNotFound` | 18 | 9 |
| 30 | `TestLinkingAnItemAlreadyInTheIncidentChangesNothing` | 18 | 9 |
| 31 | `TestUnlinkingGivesTheItemAnIncidentOfItsOwn` | 18 | 9 |
| 32 | `TestALinkNeedsAPresetReason` | 18 | 9 |
| 33 | `TestAClosedItemIsLinkedWithinTheRulingsWindowOnly` | 18 | 9 |
| 34 | `TestTheItemsOfAnIncident` | 18 | 9 |
| 35 | `TestAnExtensionResumesAStoppedRunOnItsBoard` (scenario 24) | 16, 17 | 12 |
| 36 | `TestLinkedItemsShareAnIncidentAndUnlinkApart` (scenario 25) | 18 | 12 |

Under A1, rows 25 to 34 and 36 move to Plan 7c-1; row 8 stays, its migration at level 11 with its index (C17).

## Appendix B: the plan's self-review

- **Coverage.** Every design test in exactly one task (Appendix A). Carries: 7a's 2 (the extension's half) in Tasks 5, 7 and 8; 7a's 4 in Task 9; 7a's 9 and 7d-1's 11 (new kinds, older builds) in Task 5's 0011; 7d-1's holding before external requests: none added (Global Constraints); 7d-2's `make test-docker` at the branch's end. Plan 7b-1's items 1 to 11 mapped ("What this plan relies on"); 12 in Tasks 1 and 11; 13 in Task 2; 14 and 15 in Task 3. The sweep's rows: none falls to 7b-2 (`plan7-carry-sweep.md`, its owners' line).
- **Types against the code at 5a1321a.** `carryOn` returns `(repeat, unknown bool, _ error)`; `countAsPromoted(id item.ID)`, `entry.unsure`, `walkSoon`, `steps`, `failed(ctx, id, err)`, `carryOutKill(advance func())`, `namesEveryRun(a *onAnswer)`, `record(ctx, s, recs ...rec)` with `rec{run, body}`, `Sessions.Move(run, agent, round, deadline, budget int64)`, `State.KillSwitchSeen`, `State.Owes(onSeq)`, `Run.Created.Budget`, `board.State.Stopped()`, `Boards.Stop`, `api.act(kind, do)`, `deploy.Lifecycle`, `migration.level`/`minReader`, `Level()`: each read in the code. New names are C16's, C18's and C19's.
- **Version 2's types,** against the code: `runStep`'s conclude case (`coordinator.go`), `Run.Halt`'s first branch (`halt.go`), `mayStop` and `Run.open` (`state.go`), `Run.Open` and `Run.Stop` (`decide.go`), `requestIn` (`acts.go`), `owing` and `wouldKill` (`killswitch.go`), `w.armed` (`interleave_world_test.go`), `runOrders` (`interleave_invariants_test.go`), `classify` (`internal/db/allowlist.go`), and `oneItem` and `failing`, the records fakes.
- **Proportion.** Thirteen tasks, the checkpoint among them, about 850 lines, against 7b-1's 2,103.
