# Plan 7a: The Coordinator, One Work Item End to End — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Executed on 2026-10-19. See "Changes made during execution" at the end. |
| Date | 2026-10-18 |
| Roadmap | `2026-10-12-implementation-roadmap.md`, Plan 7, first of four parts |
| Decided by the author | On 2026-10-14: Phase 0 is built before a team adopts the platform, on the assumptions ADR-016 records |

**Goal:** Build the coordinator's core: one work item, opened by a person or a test, runs its primary run through rounds of agent turns to a stop, with every decision recorded before it acts, sessions the two gateways know by token, the daily cap, and one lease that fences an instance that lost it, so that another instance takes over from the records.

**Architecture:**
- **Plan 7 in four parts.** The roadmap's Plan 7 is about five person-weeks; one plan for all of it would go stale before half of it ran. This plan, 7a, builds one item end to end. 7d, next, adds the runner in sandboxes and `agentswarm serve`, so that a real runner tries the turns' contract early; then 7b adds the lifecycle's breadth (the queue, the kill switch, close and reopen, extensions, links), and 7c the inbound events and trigger mappings. Arms, the measurement window, finality and shadow runs move to Plan 8.
- **The domain.** `internal/work/item` holds a work item's records, the state they fold into, and the rules that decide how a round ends. It is pure: the coordinator reads the board, the spend and the turns, and `item` decides.
- **The use cases.** `internal/work/app.Coordinator` opens an item, starts it, opens and ends its rounds and stops its run, writing each decision as a work record before it acts on it; every action can be repeated, so a restart carries an item on from its records. `internal/work/app.Leader` takes and holds the platform's lease and has the coordinator act only while it does (Task 9).
- **The board.** Plan 1's board gains a stop entry, after which it takes rulings only; evidence the platform posts once per record; notices of model calls; and a round's report, which the coordinator reads when a round ends.
- **Sessions.** `internal/work/tokens` issues the tokens that name an agent's session at each gateway, and `internal/work/sessions` keeps the live sessions in memory, for the coordinator and both gateways' `Sessions` ports. Both gateways redact any session token.
- **The adapters.** Migration 0007 adds `work_records`, chained under their incident; migration 0008 adds `leases`. `internal/work/postgres` gains the records, the runs' board contexts and the lease; the board and records stores write only under the lease. `internal/work/memory` gains records, for tests.
- **First, Plan 6b's carried fixes** in the LLM gateway (Task 1), which its final review handed to this plan.

**Tech Stack:**
- Go 1.26
- pgx v5.10.0
- testify v1.12.1
- testcontainers-go v0.44.0, with Postgres 17
- No new module.

**Spec:** `plan/2026-10-11-agent-swarm-platform-spec.md`. Read these before the tasks that implement them:
- Section 4.1 and 4.2: items, runs, stop reasons, entries;
- Section 5.1 to 5.2: opening, invitations, rounds and sessions;
- Section 5.6: stop rules and the budget;
- Section 8.2: failures, the coordinator's crash and the lease;
- Section 9.3 and 9.4: tokens, redaction and the audit chain;
- Section 15: the parameters.

Also read ADR-004 (durable execution on Postgres), ADR-006 (the runtime: a thin loop, which Decision 13 gives a fresh context for each turn), and the design this plan was written from, whose decisions are below.

**Reviewed before it was written:**
- **The design** was read by a fresh reviewer on Opus 5.5 against the spec, the code and every constraint earlier plans handed to Plan 7. It found 3 Critical, 18 Important and 17 Minor problems and recommended 15 decisions. The Critical ones: sessions lived in an instance's memory with nothing to end them when it lost its lease, so a stale instance went on authenticating its sandboxes after a takeover (Decision 3: one lease, which fences its holder); decisions were not recorded, so a restart decided again (Decision 5: decide, record, act); and every turn without `end_turn` counted as a failure, so a vendor's brownout would stop every run quiet and remove every agent (Decision 6). Its probes on Postgres shaped the lease: writers take the row in key share, a renewal changes the expiry only, a takeover changes the epoch.
- **The built code** was read by two more, one for security and one for correctness and concurrency. Between them they found 2 Critical, 7 Important and 16 Minor problems. The Critical ones were in the takeover: a round ended by an outage whose end failed once left its item waiting for ever, and one whose board entry never landed made every later round end by an outage. The Important ones: the record read texts again with their protocol keys, so a tool's output could make a turn's every request unrecordable; the admission lock could not be left and could be wedged; the leader took over before it armed its renewals, and could be stopped while it took the lease and act on; a turn's late end could end the turn that replaced it; a runner out of reach removed every agent; and the ports' calls under the coordinator's lock had no contract. Three background security reviews of the fixes found a takeover that went on after it found the lease gone, and the caller's own token kept where other agents could read it. A first mutation run found 9 mutations that did not build or changed nothing and 3 tests too weak to fail. Every finding is fixed, test first, in the task that owns the code, or ruled below; the mutation run was redone on the fixed code.
- **The fixes** were read by a fourth, for what they broke. It found no Critical problem, 1 Important and 8 Minor ones, and three tests weaker than their names. The Important one came with the fixes: a round no holding acted on, ended after a handover, counted its turns as interrupted and could stop its run quiet; such a round is now unseen, never quiet, and concludes no wrap-up (Decision 9). The Minor ones were a release under a context that had ended, a takeover that marked its coordinator held after its holding ended, a record that added an empty content, a vendor's slot a panic kept, a wrap-up round in which no turn started stopping its run converged, the caller's own token in the names of refused tools, and an opening a fence had to wait for; each is fixed test first. One more, a thinking block that cannot be recorded, cannot happen through the gateway, as a probe showed, and is ruled. Its stress probe of 1,800 runs, with timers on goroutines of their own, a lease that flaps and a stop at any point, found no deadlock, no race, and nothing done after a stop.

Every finding is answered below: by a decision with its test, by a ruling, or by a constraint on a later plan.

**Verified:** Every code block in this plan was compiled, tested and linted in a scratch copy. It was then replayed, task by task, in a fresh clone of ccb5610:
- each task's tests fail before its implementation exists, and pass after it;
- each change to an existing file is given as a patch, and the replay applied every patch with `git apply`;
- `golangci-lint run ./...` stays at `0 issues.` after every task;
- the end state matches the scratch copy, file for file;
- `make lint test-all` passes on the end state.

Each rule the plan relies on was also checked by mutation, 148 in all: the test that names the rule passes; with the rule removed from the code, the code still builds and that test fails.

## Global Constraints

- **Module and Go version.** The module path is `github.com/jchen0426/agent-swarm`, with `go 1.26.0`.
- **Vocabulary.** Names come from the spec: work item, incident, run, round, turn, session, board, entry, stop reason, budget, daily cap, lease. Never "employee".
- **Domain purity.** `internal/work/item`, `internal/work/board` and `internal/work/tokens` import no database, network or framework package.
- **Ports.** The coordinator's ports (`Records`, `Spend`, `Lease`, `Clock`, `Timers`, `Turns`, `Revisions`, `Sessions`) are declared by it, in `internal/work/app`. Wiring lives in `cmd/`, in Plan 7d.
- **Records.** `work_records` are audit records from their first migration: append-only, deleted only by the retention job, chained with form 1, and read by verification (Plan 6a's constraint).
- **Decide, record, act.** Every decision the coordinator makes is a work record written before its effects, and every effect can be repeated.
- **Secrets.** A session token never appears in a record, a log, an error or a board entry: it is held as `tokens.Secret`, stored nowhere, and redacted wherever an agent could write it.
- **Libraries.** No new module.
- **Lint.** Every task ends with `golangci-lint run ./...` at `0 issues.`.
- **Docker.** The integration tests of Tasks 5 and 6 start Postgres, so Docker must run.
- **Language.** Code, comments and documents are in English.

## Decisions this plan makes where the spec is silent

Review these first. Each is pinned by the named tests. Decisions 2, 3 and 6 to 13 amend the spec, and Decisions 3, 4 and 13 amend ADR-004 and ADR-006, in Task 10; approving this plan approves those amendments.

1. **Plan 7 in four parts.** 7a (this plan) runs one item end to end; 7d, next, the runner and `serve`, which wires what 7a built and tries the turns' contract with a real runner early; then 7b adds the lifecycle's breadth and 7c the inbound events, which `serve` wires as they come. The roadmap's checkpoint follows 7d. Arms, the measurement window, finality and shadow runs move to Plan 8: until then every item is visible, its record says `arm: visible, how: no_window`, and its one run is its primary run. The roadmap is amended in Task 10.

2. **Plan 6b's carried fixes** (Task 1).
   - **Each client value quoted on its own.** A refusal's reason quotes a value of the client's only on its own, never a string made from it, so that the session's rules check the value alone: a model, a block's type or a tool's type that is not a string is given by its kind (`a list`, `an object`, `a number`, `true or false`, `none`); of several anthropic-beta values, only the first. The path, the query and an anthropic-beta value that holds a list are quoted by their parts: when the session's rules hide any part, the whole value is given by its length alone, `a value of N bytes`, since a rule anchored at a word's start may miss a key inside a longer value. (`TestARefusalQuotesAValueOnlyOnItsOwn`, `TestARefusalQuotesNoValueTheSessionsRulesHide`, `TestARefusedPathIsQuotedOnlyWhole`)
   - **A record only of a request Redact passed, as it was forwarded.** `Request.Record(red)` is `""` unless the last `Redact` that passed used `red`. The record holds each text Redact passed as the gateway forwards it, and reads the client's JSON member by member: a text passes on its own, and a record that read it again with its protocol key, as `content=...`, would refuse a text both gateways passed, a tool's output that blocked the agent's turns for the rest of the run. A tool's result sent without content is recorded without it. A request whose record still cannot be made is refused, 400, never forwarded with an empty record; no request Redact passed reaches that refusal now, and it stays as a backstop: a thinking block, which Redact passes unchanged since the vendor signs it, comes back only as the gateway gave it, from an answer it recorded under the same rules, and an answer it could not record is not returned (Plan 6b). (`TestARecordIsMadeOnlyOfARequestRedactPassed`, `TestARecordIsNotMadeOfARequestRedactRefused`, `TestATextRedactPassedIsRecordedAsForwarded`, `TestATextBothGatewaysPassedIsForwardedWithItsRecord`)
   - **One call past the budget at most.** The admission that lets a call start and the commit of the call's record are made one call at a time, under one lock, so each admission counts every call admitted before it: a run passes its budget, and the day its cap, by one call at most, while each call's estimate covers its outcome. Plan 6b's budget investigation measured it: 400 trials in memory and 60 on Postgres. The lock is held for two reads and one insert. A call waits for it only while its request, its round and its run go on: one that ends while it waits is answered at once, and its record counts nothing. A panic gives the lock up, and the vendor's slot the call took, and a refusal is recorded once the lock is given up, so that recording it holds no other call back. (`TestARunPassesItsBudgetByOneCallAtMost`, `TestTheDayPassesItsCapByOneCallAtMost`, `TestARequestThatEndsWhileWaitingToBeAdmittedNeverBegins`, `TestAPanicUnderTheAdmissionLockGivesItUp`, `TestARefusalIsRecordedOnceTheAdmissionLockIsGivenUp`)
   - **A call counts in the day it was admitted against**: its record takes the time of the admission that let it start, not of its arrival. (`TestACallCountsInTheDayItWasAdmittedAgainst`)
   - **The estimate covers the outcome.** Cache writes the vendor reports without a TTL breakdown, or beyond it, are priced as five-minute writes: the only kind the allowlist lets a request ask for, and the kind a call's estimate counts. A card's long rates may not be below its base rates, since an estimate counts more tokens than its outcome and may pass the threshold where the outcome does not. (`TestCacheWritesNeverCountBelowTheTotal`, `TestCardsMustPrice`)
   - **Board text as the snapshot holds it.** `read_board` writes each claim, statement, title and quote as a member of its snapshot, which the LLM gateway checks as `key=value` too. Text whose member has an escape that hides what a rule that applies always removes is refused when it is posted, though the text alone holds nothing a rule matches: stored, it would make the LLM gateway refuse every later snapshot, for every agent of the run. (`TestATextWhoseMemberHidesWhatTheRulesRemoveIsRefused`)

   Spec Sections 5.6, 8.2 and 15 are amended: the bound is one call, and the daily cap counts the calls admitted each day.

3. **One lease, which fences an instance that lost it.** One lease, `platform`, covers the coordinator and both gateways, which run in one process (Plan 7d).
   - **The epoch.** A holding has an epoch, which only grows. Every write transaction of the holder checks, after its own locks and before it reads or writes, that its epoch holds the lease, unexpired, and takes the lease's row in key share. A renewal changes the expiry only, and waits for no write; a takeover reads the expired holding for update, then changes the epoch, a column of a unique key, so it waits for the writes in flight, and once it commits no write of the last holding passes the check. A takeover also reads when the last holding expired, which the coordinator's takeover reads (Decision 9). This plan guards the work records and the board's appends, the coordinator's and the gateways'; Plan 7d guards the gateways' audit writes. (`TestTheLeaseIsHeldByOneInstanceAtATime`, `TestAnExpiredLeaseIsTakenOverAndItsHolderFenced`, `TestATakeoverWaitsForWritesInFlight`, `TestRenewalWaitsForNoWrite`, `TestAGuardedStoreWritesNothingBeforeTheLeaseIsTaken`, `TestAHoldingKnowsWhenTheOneBeforeItExpired`)
   - **Self-fencing.** The leader renews the lease, of [15 s], every [5 s], each renewal armed [5 s] after the last was *sent* and given [5 s] to answer, so a slow renewal delays none after it. It fences its coordinator once a renewal finds the lease lost; once the last renewal that succeeded was sent [10 s] ago, the lease's time less a margin of [5 s], before the holding could expire; or once a write of the coordinator finds the lease gone. Renewals and watches belong to their holding, and the coordinator's word of a lost lease names its epoch: what an earlier holding finds changes nothing once the leader holds the lease again. The leader renews from before its coordinator takes over, which may take long. A fenced coordinator stops its turns, ends its sessions and cancels its timers, and acts again only after a takeover; its leader tries to take the lease again every [5 s]. A leader stopped, or whose context ends, at any moment, as while it takes the lease, gives the lease up, under a context of its own, which the ended one does not end, and its coordinator acts no more. (`TestALostRenewalFencesTheCoordinator`, `TestALeaderFencesBeforeItsLeaseCouldExpire`, `TestAWriteThatFindsTheLeaseGoneFencesTheCoordinator`, `TestABoardAppendThatFindsTheLeaseGoneFencesTheCoordinator`, `TestALeaderTakesTheLeaseAgainAfterAWriteFoundItGone`, `TestAFencedCoordinatorActsNoMore`, `TestALeaderThatStopsReleasesTheLease`, `TestSlowRenewalsKeepTheLease`, `TestALateRenewalOfAnEarlierHoldingChangesNothing`, `TestALateWordOfAnEarlierHoldingChangesNothing`, `TestALeaderRenewsWhileItsCoordinatorTakesOver`, `TestALeaderStoppedWhileItTakesTheLeaseActsNoMore`, `TestALeaderStoppedWhileItsCoordinatorTakesOverActsNoMore`, `TestALeaderStopsWhenItsContextEnds`, `TestALeaderWhoseContextEndsWhileItTakesOverReleasesTheLease`)

   Spec Section 8.2 and ADR-004 are amended.

4. **Timers from records; River waits.** Every deadline is in a record: a round's is in its `round_opened`. The coordinator arms its timers while it holds the lease, through a `Timers` port, and arms them again from the records when it takes over; its tests drive them with a clock moved by hand, and with timers that run on goroutines of their own, as Plan 7d's will. The coordinator calls `Timers` and `Turns` under its one lock: a timer's function never runs before `At` returns, a cancel never waits for a function that runs, no `Turns` method reports a turn's end before it returns or waits for a turn, and `Start` returns once the runner has the turn. No job outlives the records in this plan, so River is not used yet: ADR-004 is amended, and River comes with jobs that must outlive the records (notifications, retention). (`TestARealClocksTimersEndEachRoundOnce`)

5. **The item is the aggregate; decide, record, act.** Records are keyed by item, and name the item's incident (in this plan a new incident per item; Plan 7c correlates). Every decision is a record written before its effects, and the coordinator checks each record against the item's records with the lifecycle's rules (`item.State.Apply`) before it appends it, so a decision made twice is refused. Every effect repeats safely: the board's round entry, the platform's evidence and the stop entry return the earlier entry; an instance gives each session new tokens each round and moves it into the round; it starts a turn once per round, by the turn's id. A restart carries an item on from its records. (`TestRecordsFoldInTheLifecyclesOrderOnly`, `TestACrashAtAnyStepIsCarriedOnFromTheRecords`)

6. **How turns end.** The runner reports each turn's end once, naming the turn, as one class: `ended` (the agent called `end_turn`), `finished` (the runtime ended without it: no tool called, a step or token limit, a reply cut short), `interrupted` (the deadline or a stop), `refused` (the LLM gateway answered 4xx), `vendor` (5xx after the gateway's retries) or `crashed` (the sandbox or the runtime failed). A turn never reported counts as `interrupted`; an end of another turn of the agent, or of a class the runner does not report, changes nothing. A turn the runner could not start is tried again every [5 s], with the same id, while its round lasts; one that never started by the deadline is `unstarted`, the coordinator's own class. Only crashes count toward the [2] in a row that remove an agent; a round that ended by an outage, and a turn that never started, leave the count as it was, so that a runner out of reach removes no agent. A round whose every turn failed at the vendor ends at its deadline, never earlier, so a vendor's outage cannot spend the rounds in seconds; so does one whose turns could not all start. (`TestTwoConsecutiveCrashesRemoveAnAgent`, `TestATurnNeverHeardFromCountsAsInterrupted`, `TestARoundOfVendorFailuresEndsAtItsDeadline`, `TestAnAgentThatCrashesTwiceLeavesTheRun`, `TestTheDeadlineEndsTheTurnsStillRunning`, `TestALateOrRepeatedExitChangesNothing`, `TestATurnThatCouldNotStartIsTriedAgain`, `TestATurnThatNeverStartsIsUnstarted`, `TestATurnThatNeverStartedIsNeitherQuietNorACrash`)

   Spec Section 8.2 is amended.

   **Amended by Plan 7d-1** (`2026-10-20-plan-7d1-serve-and-harness.md`, Decisions 9, 12 and 13). A new class of the runner's, `unavailable`, is a turn the platform could not serve: a gateway's own failure, a holding no longer valid, a reply without the gateway's mark, no answer at all, or a spec the runner could not deliver. It is not a crash, it ends a run of crashes, and a round whose every turn ended `vendor` or `unavailable` ends at its deadline. A turn is `ended` only when its `end_turn` call succeeded. A launch that fails, the holding refused included, returns its error, so the turn is tried again and counts as unstarted. The runner's classes advise only: quiet and taking part rest on the agents' own turn ends on the board (Decision 7's note).

7. **A round's end.** Once every turn ended, or the deadline passed, the coordinator waits the round's write calls' grace [5 s], then the run's model calls in flight, looked at every [1 s] for up to [30 s]. It then notices on the board each model call refused, failed or of unknown outcome, once per call; reads the board's report; and decides, in this order:
   1. a wrap-up round whose hypothesis is still converged stops the run `converged`, if an agent took part in it as the coordinator saw it: a wrap-up round that ended by an outage, one the instance ending it did not see (Decision 9), or one none of whose turns started, does not count, and uses up no wrap-up;
   2. the day's spend at the daily cap stops it `cut`;
   3. a resolved trigger stops it `trigger_resolved` (Plan 7c);
   4. no agent left stops it `not_staffed`;
   5. a converged hypothesis, the top one, gets a wrap-up round, even with the budget spent, while the run has had fewer than [2];
   6. the budget used stops it `budget_exhausted`: its rounds, its wall clock, less than a third of a round's limit of wall clock left, or its tokens;
   7. a quiet round stops it `quiet`;
   8. otherwise the next normal round opens.

   **Rounds and wall clock used** count the normal rounds that did not end by an outage, each from its opening to its end. **A quiet round** left no entry of an agent or a person but turn ends, no proposal, and every turn ended, finished or was interrupted, and the instance ending it saw its turns; the platform's entries are never activity, and an entry that lands after the round's report was read, in the moment before the next round opens, counts in no round. **A normal round's deadline** is its limit, cut to the wall clock left; **the wrap-up round** has its whole limit, and its sessions may spend one round's share of the token budget above the run's spend at its opening. (`TestTheRoundEndsDecisionsInOrder`, `TestAConvergedHypothesisGetsAWrapUpRoundThenTheRunStops`, `TestARevokedConvergenceResumesNormalRoundsAndWrapUpsAreCapped`, `TestAWrapUpRoundCountsAsItEnds`, `TestAWrapUpRoundLostToAnOutageUsesUpNoWrapUp`, `TestAWrapUpRoundNoAgentTookPartInConcludesNothing`, `TestARoundTheCoordinatorDidNotSeeIsNeverQuiet`, `TestTheWrapUpRoundFollowsAnExhaustedBudget`, `TestARunStopsWhenItsBudgetIsUsed`, `TestAnOutageRoundCountsTowardNoBudget`, `TestARoundWithEntriesOrTroubleIsNeverQuiet`, `TestTheNextRoundsDeadlineAndBudget`, `TestARoundsEndWaitsForTheRunsCallsInFlight`, `TestAFailedModelCallIsNoticedOnce`, `TestAQuietRoundStopsTheRun`, `TestAConvergedHypothesisGetsItsWrapUpRoundAndTheRunStops`)

   Spec Section 5.6 is amended.

   **Amended by Plan 7d-1** (`2026-10-20-plan-7d1-serve-and-harness.md`, Decisions 8, 9 and 20). Quiet and taking part rest on the platform's own records, not on the runner's classes. A round is quiet only if the instance saw it, it left no failure, no entry of an agent or a person but turn ends, at least one agent was in it, and every agent of it has its own turn end, its `end_turn` call, on the board in it. A wrap-up round concludes only if the instance saw it, it left no failure, and at least one agent has its own turn end on the board in it: an interrupted turn never takes part. The failures are a model call noticed in the round as refused, failed or of unknown outcome; a failed tool call of the round, by the side the MCP gateway records (Decision 20); and a model call of the round, not of the run, still in flight when the settling wait ends. `round_ended` records the evidence its decision read. The day's spend is counted from the database's UTC midnight, against the one daily cap the LLM gateway enforces too.

8. **Stop reasons, conclusions, and the item's reason.** A run stops `converged`, `quiet`, `budget_exhausted`, `trigger_resolved`, `cut` or `not_staffed`; Plan 7b adds `killed` and `closed`. A converged run's conclusion is its wrap-up round's hypothesis, which stayed converged through it; another run's is the board's when the stop is recorded; the stop entry holds the conclusion the stop record holds, so that a ruling between the two cannot make them disagree. The item's reason is its primary run's, with `cut` read as `not_staffed` (spec Section 4.1); Plan 8 reads the run's own. The fold refuses a converged stop with another conclusion, and an item's close with a reason that is not its run's, or `not_staffed` without a run. An item opened once the day's spend reached the cap awaits its close `not_staffed`, with no run; a run with no agent to invite stops `not_staffed`, with a `no_agents` notice on its board. (`TestACutRunLeavesItsItemNotStaffed`, `TestAnItemOpenedOverTheDailyCapIsNotStaffed`, `TestARunIsCutAtTheDailyCap`, `TestAnItemWithNoAgentIsNotStaffed`, `TestAConvergedRunConcludesWithItsWrapUpHypothesis`, `TestAnItemAwaitsItsCloseWithItsReason`, `TestTheStopEntryHoldsTheRecordedConclusion`)

   Spec Section 4.1 is amended.

9. **Takeover.** An instance that takes the lease stops every turn and ends every session it holds, refuses the lease when a record has a version it cannot read, and carries every item that does not await its close on from its records: a round in progress gets fresh sessions and turns for the agents that have not ended theirs on the board; a decision recorded is carried out. A takeover whose holding ended before it began acts on nothing and leaves the coordinator fenced; one that finds the lease gone, at any item, acts on no item after it: a step that writes nothing, as a round's entry the board holds already, would otherwise start turns for an instance that holds no lease. A round whose deadline passed before the instance acted on it gets its board entry first, if its holder died before posting it, and then ends:
   - by an *outage*, when its deadline passed between the last holding's expiry and this one's start, while no instance held the lease: no turn counts, it counts toward no budget, it is never quiet, and it stays in the record for Plan 8;
   - otherwise by its deadline, when a holder held the lease then: the turns that ended on the board ended, and the others were interrupted, by the last holder's deadline, or never started, when this holding could not act on the round before its deadline, as through an outage of the board's store.

   Such a round is *unseen*: the instance ending it did not see its turns, which cannot tell a round no agent ran from one that found nothing, so it is never quiet and, as a wrap-up round, concludes nothing (Decision 7). A round whose end could not be recorded is ended again on the next try. (`TestATakeoverStartsFreshTurnsForTheRoundInProgress`, `TestARoundWhoseDeadlinePassedWithoutAHolderEndsByOutage`, `TestARoundWhoseHolderDiedAfterItsDeadlineEndsByItsDeadline`, `TestARoundThisHoldingCouldNotActOnEndsByItsDeadline`, `TestARoundEndedByAnOutageGetsItsBoardEntryFirst`, `TestAnOutageRoundWhoseEndFailsIsEndedOnTheRetry`, `TestARoundNoHoldingActedOnIsNeverQuiet`, `TestAMissedRoundOnTheEdgesOfTheHoldings`, `TestATakeoverActsOnNothingOnceItFindsTheLeaseGone`, `TestATakeoverWhoseHoldingEndedActsOnNothing`, `TestAnExitOfAnEarlierTurnChangesNothing`, `TestATakeoverRefusesRecordsItCannotRead`, `TestALeaderStopsOverRecordsItCannotRead`)

   Spec Section 8.2 is amended.

   **Amended by Plan 7d-1** (`2026-10-20-plan-7d1-serve-and-harness.md`, Decisions 7, 18 and 19). A takeover no longer refuses the lease over records of a version it cannot read. It skips each active item whose work records, or whose run's board events, are of a later version: it logs the item once, counts it in the instance's status, answers it as held by a later build, and carries the other items on. A step that meets a later version during a holding ends the run's sessions, stops its turns, cancels its timers, and leaves the item alone until the next takeover. A skipped item stays held until a build that can read it leads. Only a schema this build cannot run on stops a leader for good, without a drain. Once the schema is checked, a takeover gives every model call that an earlier holding left without an outcome one: `unknown`, priced at its stored estimate. The tests of the version refusal above now test the skip.

10. **Work records.** Table `work_records`, append-only and chained with form 1: an item's records numbered from 1, its incident, its run when the record is about one, a kind, a JSON body with a version, and the database's time of the commit. A record's scope in the chain is `incident:<id>`, so anchors name the incidents whose records they cover, and verification names the incidents that lost records; the fold refuses a run id with a colon, which could pass for an incident's scope. A body of another version decodes to its version alone, so that the fold refuses it with `ErrVersion`, though a later version may write a body this one cannot decode. Free text stays out of the records but the item's title, redacted with the rules that apply always; a title whose escapes may hide what a rule matches is replaced whole. (`TestWorkRecordsAreAppendOnlyAndChainedUnderTheirIncident`, `TestEveryAuditedRowIsChained`, `TestTheTitleIsRedactedBeforeItIsRecorded`, `TestARunsIDHoldsNoColon`, `TestABodyOfAnotherVersionDecodesToItsVersion`)

    Spec Sections 8.4 and 9.4 are amended.

11. **The board's additions.**
    - **The stop entry**, the coordinator's, sums up the run in ids and numbers: its reason, the conclusion its stop record holds, which must be a hypothesis of the board, the hypotheses by score, the tasks left open and the proposals made (each at most [50]), and the rounds, wall clock and tokens used. After it the board takes rulings only: every other post, proposal, notice and round is refused, a write call before any read is made for it, and the MCP gateway answers such a call as a refusal, not as a board it could not reach. (`TestTheStopEntrySumsUpTheRun`, `TestTheStopEntryHoldsTheRecordedConclusion`, `TestAStoppedBoardTakesRulingsOnly`, `TestAWriteCallIsNotProposableOnceTheRunStops`, `TestOnlyTheCoordinatorStopsARun`, `TestABoardCallAfterTheRunStoppedIsRefused`, `TestAStoppedRunsBoardTakesRulingsOnly`)
    - **Platform evidence** names its record and its source, and is posted once per record. The evidence an item opens with is composed by rule: who opened it, its title and its routing values, redacted, at most [2,000] characters with a mark when cut. (`TestPlatformEvidenceNamesItsRecordOnce`, `TestPlatformEvidenceIsPostedOnce`, `TestAnItemStartsItsFirstRoundAtOnce`)
    - **Notices** of model calls, `model_refused`, `model_failed` and `model_unknown`, name their call and agent, one per call, in any round; the cap of [10] notices of an agent's calls in a round counts the gateway's alone, so that none crowds a model call's out. A turn ends at its first failed model call (Plan 7d), which bounds them. `no_agents` names nothing. (`TestModelNoticesAreOnePerCall`, `TestAModelCallsNoticeIsNeverCrowdedOut`)
    - **A round's report** says which agents ended their turn and how many entries count as activity, and which model calls the board holds a notice of. (`TestARoundsReportSaysWhatTheRoundProduced`)

    Spec Section 4.2 is amended.

12. **Session tokens and sessions.**
    - **Tokens.** Two per agent session, one per gateway: `aswm_` or `aswl_` and 43 letters and digits from 32 random bytes, every token the same width; held as `tokens.Secret`, which prints as `[session token]` however it is formatted, written as JSON or logged, and as no more than an address as a field whose methods fmt and slog cannot call; known only by their hashes, in the memory of the instance that issued them. A token of one gateway is unknown to the other. (`TestATokenIsItsPrefixAndFortyThreeLettersAndDigits`, `TestASecretNeverPrints`, `TestASecretInAFieldNeverPrints`, `TestATokenOfOneGatewayIsUnknownToTheOther`)
    - **Sessions** are known to the gateways from the moment their round's board entry is committed, never in round 0, until their run stops, their agent leaves the run, or their instance is fenced. Each round's sessions have tokens of their own: a session gets new tokens each round, so that a turn of an earlier round still running cannot act in the next, and keeps its channel, which ends the calls of either tokens when the session ends. A stopped run's sessions end, and its turns stop, before its stop entry is posted, which may take tries. The MCP gateway refuses a call past its round's deadline. (`TestASessionIsKnownOnceItsRoundOpened`, `TestASessionMovesFromRoundToRoundUntilItEnds`, `TestIssuingAgainGivesFreshTokensAndKeepsTheChannel`, `TestEachRoundsSessionsHaveTokensOfTheirOwn`, `TestAStoppedRunsSessionsAndTurnsEndBeforeItsStopEntry`, `TestACallAfterItsRoundsDeadlineIsRefused`, `TestARunsSessionsEndWithItAndAllEndAtOnce`, `TestLookupsRunWhileSessionsChange`)
    - **The token rule.** Both gateways redact any session token, before the configuration's rules, with `asw[ml]_[A-Za-z0-9]{43}`: no word boundaries, which text glued to a token would defeat. The MCP gateway also hides its caller's own token's letters and digits, in any case and with any prefix, wherever it keeps text: a tool's output, the arguments it records, the names of the tools it refuses, the agent's text on the board. It refuses a read call whose arguments hold a token, or its caller's own, since it sends a server the arguments as written, and one whose escapes may hide a token, with a reason of its own; and it refuses a quote that spans the token's replacement. It decodes JSON and backslash escapes only: a token split, or written in an encoding a server decodes, is not found, which Decision 13 and ADR-006 make harmless. (`TestEveryTokenMatchesTheRuleThatHidesIt`, `TestASessionTokenIsHiddenWhereverTheGatewayKeepsText`, `TestTheCallersOwnTokenIsHiddenWhereverTheGatewayKeepsText`, `TestAReadCallWhoseArgumentsHoldASessionTokenIsRefused`, `TestAReadCallWhoseArgumentsHoldTheCallersTokenIsRefused`, `TestAQuoteSpanningASessionTokensReplacementIsRefused`, `TestAnySessionTokenIsRedactedBeforeTheVendorReadsIt`)

    Spec Section 2 is amended.

13. **A fresh context for each turn.** Every turn starts from a fresh context: the round's prompt, the agent's skills and the board, read through `read_board` (ADR-006, runtime D). A session's identity persists across the run's rounds; its tokens are new each round, and the model's context does not persist, so Plan 6b's "drop earlier rounds' thinking blocks" holds by construction. The runtime holds the tokens and sends them as headers only: no token is ever in the model's context. The round's prompt holds the platform's words and ids only, and says how many rounds the budget allows after it: a round's number does not, once an outage round counted none. (`TestAnItemStartsItsFirstRoundAtOnce`, `TestAConvergedHypothesisGetsItsWrapUpRoundAndTheRunStops`, `TestARunStopsWhenItsRoundsAreUsed`, `TestARoundWhoseDeadlinePassedWithoutAHolderEndsByOutage`)

    Spec Section 5.2 and ADR-006 are amended.

14. **Invitations.** The agents subscribed to the item's type, in the revision's order, but those that pin a yanked skill version, up to the type's `team.maxAgents`, under the revision the item locks when it starts, though the registry moves on. (`TestAnItemStartsItsFirstRoundAtOnce`, `TestAnAgentThatPinsAYankedSkillIsNotInvited`, `TestAnItemRunsUnderTheRevisionItLocksAtStart`)

15. **Deadlines in database time.** Records and deadlines are in the database's time, so an instance that takes over reads them as the instance that wrote them did. The `Clock` port gives that time and converts a deadline to the process's clock, in which the gateways compare it; Plan 7d's clock measures the offset when it takes the lease.

16. **A step that fails is tried again.** A step of an item that fails, say a write the database refused, is logged and tried again after [5 s], from the records; a write that finds the lease gone fences the coordinator at once, and nothing is tried again until a takeover. An item's opening ends with its request, or with the holding, so that a fence waits for no store an opening waits on; its start is the coordinator's, which a request that ends does not stop. (`TestAnItemStartsThoughTheRequestThatOpenedItEnded`, `TestAnOpeningGivesWayOnceTheHoldingEnds`)

## Rulings on the reviews' findings

These findings are answered without a change, or without a test of their own; each says why, and what it costs if it is wrong.

- **Thinking blocks and the record's backstop.** A request whose record cannot be made is refused (Decision 2), and no request reaches that refusal through the gateway: an answer whose thinking could not be recorded is not returned, and a request may echo only the blocks the gateway gave, which it recorded under the same rules. A probe showed both. The refusal stays, with no test that reaches it. If the session's rules could change between an answer and its echo, a turn would end `refused`, and the next round starts from a fresh context.
- **The admission lock's place in the record.** The lock is held for two reads and one insert per call; Plan 10's load trial measures it.
- **Active items and the runs' board contexts** (`work_records_by_kind`, `run_id <> ''`) were changed for their cost alone, with no failing test: `TestActiveItemsAreThoseNotAwaitingClose` and `TestARunsBoardContextComesFromItsCreation` pin what they read. Plan 10 measures them.
- **The leader's timers** keep one cancel per kind, where they kept one per arming; nothing outside the leader can see the difference, so no test pins it.
- **The leader stopped while its coordinator takes over.** `TestALeaderStoppedWhileItsCoordinatorTakesOverActsNoMore` checks the outcome, which holds whether the stop comes during the takeover or after it; showing which came first would need a hook into the leader.
- **A failed model call noticed once** rests on the board, which keeps one notice per call in any round (Decision 11); the coordinator's own check only saves appends.
- **Session tokens in other encodings.** The MCP gateway finds a token through JSON and backslash escapes only; a token split, percent-encoded, or written in characters a server normalizes is not found. No token is ever in a model's context (Decision 13; Plan 7d), and servers cannot reach the gateways (ADR-006), so a token found nowhere is one no model had.
- **One escape check for the three sites** (refusal reasons, records, board text), with a property test across them, was suggested. Each site keeps its own check, pinned by its own tests; the runner's traffic in Plan 7d is where a shared property test would find inputs worth drawing.
- **Turns that never started count toward the budget**, as a round of vendor failures does, so that a runner out of reach ends a run by its budget rather than holding it for ever.
- **A missed round's holder.** A takeover knows the expiry of the holding just before its own. A round whose deadline fell in an earlier gap between holdings, which the holding in between never ended, ends by its deadline: that holding held the lease after the deadline, and should have ended it.
- **The wall clock** counts each normal round from its opening to its end; the grace and the settling between rounds are not counted, a few seconds a round.
- **Calls left in flight** by an instance that died count toward the run's calls in flight until Plan 7d closes them at start, so a round's end waits the settling wait for them; a session's class is primary until Plan 8.

## Assumptions taken for the team (ADR-016)

| Profile question | Assumption | Where |
|---|---|---|
| 3: the alerting source | None yet: a person or a test opens an item, with the type, title and trusted routing values it gives; Plan 7c reads them from inbound events | Task 7 |
| 4: runbooks | None: agents are scripted in the tests; the runner and its runtime come with Plan 7d | Tasks 7 and 8 |

## Review Focus

The spec says what the coordinator must do, not every condition it will meet. These five are the conditions most likely to bite a person running it that the plan's other tests do not reach; each has its test in the task that owns the code.

1. **Timers that fire on other goroutines**, as Plan 7d's will, while turns end and a deadline passes at once. A person expects one end per round, by the turns or by the deadline, whichever comes first, no deadlock, and nothing done after a fence. (`TestARealClocksTimersEndEachRoundOnce`, Task 8)
2. **A turn's end reported late**, by a runner whose turn was stopped before the instance took the lease again and started a new turn for the same agent. A person expects the late report to change nothing. (`TestAnExitOfAnEarlierTurnChangesNothing`, Task 8)
3. **A leader stopped while it takes the lease**, as a deploy stops an instance at any moment. A person expects the stopped instance's coordinator to act no more, and the lease to be released. (`TestALeaderStoppedWhileItTakesTheLeaseActsNoMore`, Task 9)
4. **A configuration that changes between an item's opening and its start**, as the registry refreshes. A person expects the item to run under the revision it locks when it starts, and to be not staffed if that revision cannot run its type. (`TestAnItemRunsUnderTheRevisionItLocksAtStart`, Task 7)
5. **Agents that pin a yanked skill version.** A person expects them not to be invited (spec Section 5.1). (`TestAnAgentThatPinsAYankedSkillIsNotInvited`, Task 7)

## Not in this plan

- **The queue, the kill switch, close, reopen, auto-close, budget extensions, links between items, and the use cases Plan 9's interface calls** (Plan 7b).
- **Inbound events, trigger mappings, dedup and correlation, principals and responders read at open, and hypothesis-key values read at open** (Plan 7c). Until then the coordinator refuses an item type whose key reads its values from a tool.
- **The runner in sandboxes, `agentswarm serve` and `migrate`, the production `Clock` and `Timers`, the gateways' audit writes under the lease, `Drain`, and the model calls left without an outcome** (Plan 7d).
- **Arms, the measurement window, finality and shadow runs** (Plan 8). Until then every item is visible, with no window, and its run is its primary run.
- **The message to the responders' channel at start** (Plan 9).
- **A log of the sessions issued.** The design kept one; sessions live in the memory of the instance that issued them, end with it, and an instance that lost the lease ends them at once, so the log would record what no one reads.

## Constraints carried to later plans

- **Plan 7b:**
  - stop a run `killed` (the kill switch) or `closed` (a person's close), mid-round: record the stop, then stop its turns, end its sessions and post its stop entry, as `close` does;
  - the queue, promotion, reopen and auto-close, whose timers come from the records as rounds' do (Decision 4); a budget extension resumes a stopped run from its records;
  - rulings on a closed item's board for [72 h]: a stopped board takes rulings only, as it does now;
  - links between items change an item's incident only through link records (Decision 5).
- **Plan 7c:**
  - read hypothesis-key values, principals and responders when an item opens, and lift the refusal of a type whose key reads its values;
  - post the trigger's evidence as the platform's, naming the event's record, and record a resolved trigger on the item, which the round's end reads (`item.Ending.TriggerResolved`);
  - hand `Open` a title already redacted with the mapping's rules as well as the ones that apply always; correlate items into incidents.
- **Plan 7d:**
  - the runner implements `app.Turns` as its contracts say (Task 7): it reports each turn's end once, naming the turn's id, as one of Decision 6's classes, through `Coordinator.Exited`, never before `Start` returns; `Start` returns once the runner has the turn, without waiting for its sandbox, fails only when the runner cannot be reached, a sandbox that fails to start being a crashed turn, and starts a turn repeated with the same id once; `Stop` and `StopAll` never wait for a turn; it kills a sandbox at its deadline; it gives the runtime the turn's tokens, the gateways' addresses and the prompt, and the runtime's client `option.WithoutEnvironmentDefaults()` and `option.WithMaxRetries(0)`; it gives the model each text block of an MCP gateway result as its own text block (Plan 6b);
    - **Amended by Plan 7d-1** (Decision 12): a launch that fails, the holding refused included, returns its error, so that the turn is tried again and counts as unstarted, not crashed; a spec the runner cannot deliver is reported `unavailable`, never `crashed`.
  - the runtime ends its turn at its first failed model call, which bounds the model calls' notices (Decision 11), and holds the session's tokens in memory, sending them as request headers only: no token ever reaches the model's context, a log or an error (Decision 13);
  - a production `Clock`, which measures the database's offset when the lease is taken, and a `Timers` whose functions run on other goroutines, never before `At` returns, and whose cancel never waits for a function that runs;
  - `serve` wires the leader, the coordinator and both gateways on one `sessions.Registry`, with `Records` and the board's `EventStore` guarded by the lease, and guards the gateways' audit writes (`tool_calls`, `model_calls`) the same way; on shutdown, `Leader.Stop`, then the LLM gateway's `Drain`; at start, an outcome for each model call left without one (Plan 6b);
  - the runner and the gateways reach each other only through the sandbox's network (ADR-006).
- **Plan 8:** arms, the window, finality and shadow runs; an outage round is in its `round_ended` record; a run's own stop reason, not its item's, is what measurement reads.
- **Plan 9:** the message to the responders' channel at start; the interface shows the stop entry and takes rulings after it.
- **Plan 10:** measure in the load trial the coordinator's one lock, which serializes every item's decisions and their I/O, a takeover's steps over every item included; the LLM gateway's admission lock, held for two reads and one insert per call; and a takeover's read of the active items, which reads one opening per item ever opened.

## File Structure

| File | Task | Responsibility |
|---|---|---|
| `internal/llm/prompt/prompt.go` | 1 | Quote each client value on its own and by its parts; a record only of a request Redact passed, holding its texts as forwarded |
| `internal/llm/app/gateway.go` | 1, 4 | One admission at a time to the record's commit, under a lock a request can leave; the session tokens' rule |
| `internal/llm/completion/completion.go`, `internal/llm/cost/cost.go` | 1 | Cache writes without a TTL priced as five-minute writes; long rates never below base rates |
| `internal/work/board/board.go`, `state.go`, `round.go`, `report.go` | 2 | The stop entry, platform evidence, notices of model calls, a round's report |
| `internal/work/app/boards.go` | 2 | The board's new use cases |
| `internal/actions/app/board.go` | 1, 2, 4 | Board text checked as the snapshot holds it; a stopped board refuses; the session tokens' rule and markers; the caller's own token hidden |
| `internal/actions/app/call.go`, `platform.go`, `write.go` | 4 | A call past its round's deadline refused; arguments that hold a token refused; the caller's own token hidden |
| `internal/work/item/item.go`, `state.go`, `decide.go` | 3, 5 | Work records, their fold, and the round's end; a body of another version |
| `internal/work/tokens/tokens.go` | 4 | Session tokens |
| `internal/work/sessions/sessions.go`, `internal/work/app/sessions.go` | 4 | Live sessions for the coordinator and both gateways |
| `internal/registry/config/redaction.go`, `internal/actions/redact/redact.go` | 4 | The session tokens' pattern and rule |
| `internal/db/migrations/0007_work_records.sql`, `0008_leases.sql` | 5, 6 | Work records, chained; leases |
| `internal/work/postgres/records.go`, `lease.go`, `events.go` | 5, 6 | Records, runs' board contexts, the lease, stores that write under it |
| `internal/work/memory/records.go` | 5 | Records in memory |
| `internal/audit/postgres/chain.go`, `models.go` | 5 | Verification reads work records; model calls in flight and failed |
| `internal/work/app/ports.go` | 5, 6, 7, 8 | The coordinator's ports and their contracts |
| `internal/work/app/coordinator.go` | 7, 8 | The coordinator; takeover and fencing |
| `internal/work/app/leader.go` | 9 | The leader |
| `plan/...`, `README.md` | 10 | The spec's amendments, ADR-004 and ADR-006, the roadmap |

---

## Tasks

Each change to a file that exists is given as a patch: a unified diff against the file as the task before left it, which `git apply` takes as written (save the block to a file, then run `git apply <file>`). A new file is given in full.

### Task 1: Plan 6b's carried fixes, in the LLM gateway

Decision 2. Plan 6b's final review handed these to Plan 7: its refusal reasons still quoted strings composed from a client's values, `Request.Record` would record a JSON result unredacted if called before `Redact`, and a request could be forwarded with an empty record. Its budget investigation found that a run could pass its budget by one call per agent, and the day its cap by one call per vendor slot, that a call admitted just after midnight could count in the day before, and that an outcome could cost more than its estimate. The review of this plan's code found three more: the record read each text again with its protocol key, so that a tool's output could make every later request of a turn unrecordable; a request that ended while it waited for the admission lock still began its call, and a panic under the lock wedged the gateway; and the board took text whose member in `read_board`'s snapshot hid what a rule removes, which made the LLM gateway refuse every later snapshot.

**Files:**
- Modify: `internal/llm/prompt/prompt.go`, `internal/llm/prompt/prompt_test.go`
- Modify: `internal/llm/app/gateway.go`, `internal/llm/app/gateway_test.go`
- Modify: `internal/llm/completion/completion.go`, `internal/llm/completion/completion_test.go`
- Modify: `internal/llm/cost/cost.go`, `internal/llm/cost/cost_test.go`
- Modify: `internal/actions/app/board.go`, `internal/actions/app/board_test.go`, `internal/actions/app/snapshot_internal_test.go`

**Interfaces:**
- Consumes: the LLM gateway (Plan 6b); the MCP gateway's board tools (Plan 5b).
- Produces:
  - `prompt.QuoteParts(s, separators string, hides func(string) bool) string`, which gives a value by its length alone when the rules hide any of its parts;
  - `Request.Record(red)` is `""` unless the last `Redact` that passed used `red`, and holds the texts Redact passed as they are forwarded; `Gateway.Messages` answers 400 `invalid_request_error` for a request it cannot record, a backstop no request Redact passed reaches.

- [ ] **Step 1: Write the failing tests**

Apply this change to `internal/llm/prompt/prompt_test.go`:

```diff
diff --git a/internal/llm/prompt/prompt_test.go b/internal/llm/prompt/prompt_test.go
index 7d849bf..8af5b97 100644
--- a/internal/llm/prompt/prompt_test.go
+++ b/internal/llm/prompt/prompt_test.go
@@ -405,10 +405,48 @@ func TestARefusalQuotesNoValueARuleHides(t *testing.T) {
 	}
 	_, err := prompt.Check(headers(), []byte(set(t, "metadata", raw(t, `{}`))), p)
 	require.ErrorContains(t, err, `"metadata"`, "a value no rule hides is quoted whole")
 }
 
+// A refusal quotes a value of the client's on its own, never a string made
+// from it, so that the session's rules check the value alone: a model, a
+// block's type or a tool's type that is not a string is given by its kind,
+// and of several anthropic-beta values only the first is quoted. A rule
+// anchored to a whole value, as ^AKIA[A-Z0-9]{16}$ is, matches the key alone,
+// but not inside a list written out or values joined with commas.
+func TestARefusalQuotesAValueOnlyOnItsOwn(t *testing.T) {
+	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`^AKIA[A-Z0-9]{16}$`)}, Replacement: "[redacted:keys]"})
+	p := policy(t)
+	p.Hides = func(s string) bool { return red.Text(s) != s || red.Escaped(s) }
+	const key = "AKIAABCDEFGHIJKLMNOP"
+	for name, c := range map[string]struct {
+		body string
+		beta []string
+		says string
+	}{
+		"a model in a list":         {body: set(t, "model", []any{key}), says: "the model is not the agent's: a list"},
+		"a model in an object":      {body: set(t, "model", map[string]any{"m": key}), says: "the model is not the agent's: an object"},
+		"a model that is a number":  {body: set(t, "model", 5), says: "the model is not the agent's: a number"},
+		"a model that is true":      {body: set(t, "model", true), says: "the model is not the agent's: true or false"},
+		"a kind of block in a list": {body: set(t, "messages", raw(t, `[{"role": "user", "content": [{"type": ["`+key+`"]}]}]`)), says: "of a kind not allowed: a list"},
+		"a block with no kind":      {body: set(t, "messages", raw(t, `[{"role": "user", "content": [{"text": "`+key+`"}]}]`)), says: "of a kind not allowed: none"},
+		"a tool's type in a list":   {body: set(t, "tools", raw(t, `[{"type": ["`+key+`"], "name": "x", "input_schema": {}}]`)), says: "as a tool the vendor runs does: a list"},
+		"two anthropic-beta values": {body: turn, beta: []string{key, "x"}, says: "no anthropic-beta is allowed: a value of 20 bytes"},
+		"a list in one value":       {body: turn, beta: []string{"x, " + key}, says: "no anthropic-beta is allowed: a value of 23 bytes"},
+	} {
+		h := headers()
+		if c.beta != nil {
+			h["Anthropic-Beta"] = c.beta
+		}
+
+		_, err := prompt.Check(h, []byte(c.body), p)
+
+		require.ErrorContains(t, err, c.says, name)
+		assert.NotContains(t, err.Error(), "AKIA", name)
+	}
+}
+
 // Redaction changes everything the model reads, each part on its own, and
 // nothing that makes the request work: ids, names, types, cache control,
 // numbers and thinking blocks stay as written.
 func TestRedactionChangesWhatTheModelReadsOnly(t *testing.T) {
 	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
@@ -713,18 +751,19 @@ func TestWhatTheMCPGatewayShowsOfJSONReachesTheModelAsShown(t *testing.T) {
 		"a keyword before an escaped quote": `{"lines": ["level=info", "api_key\": \"abcd1234efgh"]}`,
 		"a member split by a value's quote": `{"x": "password\": \"hunter2hunter2"}`,
 		"eight backslashes":                 `{"path": "` + strings.Repeat(`\\`, 8) + `n"}`,
 	} {
 		shown := shownByMCP(t, red, raw)
+		llm := red.Before(sessionToken)
 		for _, inline := range []bool{true, false} {
 			req, err := check(t, asResult(t, shown, inline))
 			require.NoError(t, err, name)
 
-			require.NoError(t, req.Redact(red.Before(sessionToken)), name)
+			require.NoError(t, req.Redact(llm), name)
 
 			assert.Equal(t, shown, result(t, req), name)
-			assert.Equal(t, string(req.Encode()), req.Record(red.Before(sessionToken)), "the record holds what is forwarded: %s", name)
+			assert.Equal(t, string(req.Encode()), req.Record(llm), "the record holds what is forwarded: %s", name)
 		}
 	}
 }
 
 // A board snapshot reaches the model as read_board shows it. The board keeps
@@ -810,10 +849,46 @@ func TestTheRecordHoldsWhatIsForwardedRedacted(t *testing.T) {
 		assert.Equal(t, strings.Replace(string(req.Encode()), c.thought, c.recorded, 1), record, name)
 		assert.Contains(t, record, `{\"lines\":[\"say \\\"[redacted:keys]\\\" twice\"]}`, name)
 	}
 }
 
+// A record is made only of a request that Redact passed with the same
+// redactor. A record keeps a tool's JSON result as it is forwarded, which
+// only Redact redacts: made before, or after a redaction by other rules, it
+// would hold the result as the client sent it.
+func TestARecordIsMadeOnlyOfARequestRedactPassed(t *testing.T) {
+	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
+	other := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`pk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
+	req, err := check(t, asResult(t, `{"lines": ["say \"sk-aaaaaaaa\" twice"]}`, false))
+	require.NoError(t, err)
+
+	before := req.Record(red)
+	require.NoError(t, req.Redact(red))
+	withOther := req.Record(other)
+	after := req.Record(red)
+
+	assert.Empty(t, before, "before Redact")
+	assert.Empty(t, withOther, "with a redactor Redact did not use")
+	assert.Equal(t, string(req.Encode()), after)
+	assert.NotContains(t, after, "sk-aaaaaaaa")
+}
+
+// A request Redact refused is as the client sent it, so no record is made of
+// it: here a tool's name holds what a rule hides, and the tool's JSON result
+// would be recorded unredacted.
+func TestARecordIsNotMadeOfARequestRedactRefused(t *testing.T) {
+	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
+	body := strings.Replace(asResult(t, `{"lines": ["say \"sk-aaaaaaaa\" twice"]}`, false),
+		`"tools": [{"name": "metrics__query"`, `"tools": [{"name": "sk-abcdefgh"`, 1)
+	req, err := check(t, body)
+	require.NoError(t, err)
+
+	require.Error(t, req.Redact(red))
+
+	assert.Empty(t, req.Record(red))
+}
+
 // pieces are what drawn texts are made of: keywords, the separators of
 // members, quotes, backslashes and line breaks, values, a rule's replacement
 // and HTML characters, as logs and configuration dumps hold them.
 var pieces = []string{"password", "secret", "api_key", "user", `"`, `": "`, `":"`, ": ", "=", " ", ", ", "hunter2hunter2",
 	"abcd1234efgh", "x", `\`, "\n", "{", "}", `\"`, "level=info", "Bearer ", "AKIA", "0123456789ABCDEF", "<", "&",
@@ -997,5 +1072,33 @@ func FuzzCheckNeverFails(f *testing.F) {
 		if _, err := prompt.Check(headers(), req.Encode(), p); err != nil {
 			t.Fatalf("a redacted request is refused: %v", err)
 		}
 	})
 }
+
+// A text Redact passed is recorded as it is forwarded: the record reads no
+// member that a key of the request's own shape, such as "text" or
+// "content", would make of it. Here the text decodes, after such a key, to
+// what a rule for assignments matches, though on its own it holds nothing a
+// rule matches, as the MCP gateway showed it.
+func TestATextRedactPassedIsRecordedAsForwarded(t *testing.T) {
+	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`(?i)\b[a-z_]+=[A-Za-z0-9+/]{24,}`)}, Replacement: "[redacted:assignments]"})
+	const text = `QUJDREVGR0hJSktMTU5PUFF\/S is the key id`
+	require.False(t, red.Escaped(text), "the MCP gateway shows it")
+	quoted, err := json.Marshal(text)
+	require.NoError(t, err)
+	for name, body := range map[string]string{
+		"a tool's result":                 asResult(t, text, true),
+		"a tool's result block":           asResult(t, text, false),
+		"a person's text":                 strings.Replace(turn, `"Find why checkout fails."`, string(quoted), 1),
+		"a text block":                    strings.Replace(turn, `"Querying."`, string(quoted), 1),
+		"the system prompt":               strings.Replace(turn, `"You investigate incidents."`, string(quoted), 1),
+		"a tool's description":            strings.Replace(turn, `"Query metrics."`, string(quoted), 1),
+		"a tool's result without content": strings.Replace(turn, `, "content": [{"type": "text", "text": "errors rose at 10:02"}]`, "", 1),
+	} {
+		req, err := check(t, body)
+		require.NoError(t, err, name)
+		require.NoError(t, req.Redact(red), name)
+
+		assert.Equal(t, string(req.Encode()), req.Record(red), name)
+	}
+}
```

Apply this change to `internal/llm/app/gateway_test.go`:

```diff
diff --git a/internal/llm/app/gateway_test.go b/internal/llm/app/gateway_test.go
index 229baf0..7837972 100644
--- a/internal/llm/app/gateway_test.go
+++ b/internal/llm/app/gateway_test.go
@@ -308,22 +308,23 @@ func TestARefusedRequestIsRecordedShort(t *testing.T) {
 	assert.NotContains(t, rec.Request, "sk-")
 	assert.Equal(t, int64(len(body)), rec.RequestBytes)
 	assert.Len(t, rec.RequestDigest, 64)
 }
 
-// A refusal of a path quotes the path and query only when they are whole, at
-// most 64 bytes and plain; otherwise it says how long they are and quotes
-// none of them. The record redacts the reason as written, and a cut copy
-// leaves most of a secret in it, as does an escaped one.
+// A refusal of a path quotes the path, and the query apart, each only when
+// it is whole, at most 64 bytes, plain and hides nothing on its own;
+// otherwise it says how long it is. The record redacts the reason as
+// written, and a cut copy leaves most of a secret in it, as does an escaped
+// one.
 func TestARefusedPathIsQuotedOnlyWhole(t *testing.T) {
 	for name, c := range map[string]struct{ path, query, says string }{
-		"a short path":  {"/v1/files", "", `"/v1/files?"`},
-		"a short query": {"/v1/messages", "beta=true", `"/v1/messages?beta=true"`},
+		"a short path":  {"/v1/files", "", `not "POST" "/v1/files"`},
+		"a short query": {"/v1/messages", "beta=true", `not "POST" "/v1/messages" with the query "beta=true"`},
 		// A cut at 64 bytes leaves sk-aaaaaaa, which no rule matches.
-		"a long path":     {"/v1/" + strings.Repeat("x", 50) + "sk-aaaaaaaa", "", "a value of 66 bytes"},
-		"a long query":    {"/v1/messages", strings.Repeat("y", 41) + "sk-bbbbbbbb", "a value of 65 bytes"},
-		"an escaped path": {`/v1/s\u006b-cccccccc`, "", "a value of 21 bytes"},
+		"a long path":     {"/v1/" + strings.Repeat("x", 50) + "sk-aaaaaaaa", "", "a value of 65 bytes"},
+		"a long query":    {"/v1/messages", strings.Repeat("y", 41) + "sk-bbbbbbbb", `"/v1/messages" with the query a value of 52 bytes`},
+		"an escaped path": {`/v1/s\u006b-cccccccc`, "", "a value of 20 bytes"},
 	} {
 		e := newEnv(t)
 
 		r := e.gw.Messages(t.Context(), &app.Inbound{Method: "POST", Path: c.path, Query: c.query, Token: token, Header: headers(), Body: []byte(request)})
 
@@ -344,11 +345,11 @@ func TestARefusedPathIsQuotedOnlyWhole(t *testing.T) {
 // can send a method as long as a request line may be, and the reason is
 // recorded, so a method written whole would put a megabyte into the audit
 // store for a request that costs nothing.
 func TestARefusedMethodIsQuotedOnlyWhole(t *testing.T) {
 	for name, c := range map[string]struct{ method, says string }{
-		"a short method":     {"GET", `not "GET" "/v1/messages?"`},
+		"a short method":     {"GET", `not "GET" "/v1/messages"`},
 		"a long method":      {strings.Repeat("X", 60) + "sk-aaaaaaaa", "a value of 71 bytes"},
 		"a very long method": {strings.Repeat("Y", 1<<20) + "sk-bbbbbbbb", "a value of 1048587 bytes"},
 		"an escaped method":  {"GET\n", "a value of 4 bytes"},
 	} {
 		e := newEnv(t)
@@ -378,30 +379,65 @@ func TestARefusedMethodIsQuotedOnlyWhole(t *testing.T) {
 func TestARefusalQuotesNoValueTheSessionsRulesHide(t *testing.T) {
 	e := newEnv(t)
 	e.configure(t, anchoredKeys)
 	const key = "AKIAABCDEFGHIJKLMNOP"
 	for name, in := range map[string]*app.Inbound{
-		"a field":   {Method: "POST", Path: app.Path, Body: []byte(strings.Replace(request, `{"model"`, `{"`+key+`": 1, "model"`, 1))},
-		"the model": {Method: "POST", Path: app.Path, Body: []byte(strings.Replace(request, `"model": "claude-haiku-5-5"`, `"model": "`+key+`"`, 1))},
-		"a method":  {Method: key, Path: app.Path},
+		"a field":          {Method: "POST", Path: app.Path, Body: []byte(strings.Replace(request, `{"model"`, `{"`+key+`": 1, "model"`, 1))},
+		"the model":        {Method: "POST", Path: app.Path, Body: []byte(strings.Replace(request, `"model": "claude-haiku-5-5"`, `"model": "`+key+`"`, 1))},
+		"a method":         {Method: key, Path: app.Path},
+		"a query":          {Method: "POST", Path: app.Path, Query: key},
+		"a key in a query": {Method: "POST", Path: app.Path, Query: "id=" + key},
+		"a key in a path":  {Method: "POST", Path: "/v1/" + key},
 	} {
 		in.Token, in.Header = token, headers()
 
 		r := e.gw.Messages(t.Context(), in)
 
 		assert.NotContains(t, string(r.Body), key, name)
 		rec, o := e.last(t)
 		assert.Equal(t, app.StatusRefused, o.Status, name)
-		assert.Contains(t, o.Reason, "a value of 20 bytes", name)
+		assert.Regexp(t, "a value of 2[0-4] bytes", o.Reason, name)
 		assert.NotContains(t, o.Reason, key, name)
 		assert.NotContains(t, rec.Request, key, name)
 	}
 	e.post(t, strings.Replace(request, `{"model"`, `{"metadata": {}, "model"`, 1))
 	_, o := e.last(t)
 	assert.Contains(t, o.Reason, `"metadata"`, "a value no rule hides is quoted whole")
 }
 
+// A text both gateways passed reaches the vendor with its record, however a
+// rule for assignments reads it after a key of the request's shape: a
+// tool's output the MCP gateway showed never stops an agent's turn. Here a
+// person's text is 25 escapes that decode to A, and a tool's output decodes,
+// after "content=", to what the rule matches.
+func TestATextBothGatewaysPassedIsForwardedWithItsRecord(t *testing.T) {
+	e := newEnv(t)
+	e.configure(t, map[string][]byte{
+		"redaction-rules/assignments.yaml": []byte(`kind: RedactionRule
+metadata: {name: assignments, owner: security@example.com}
+spec:
+  always: true
+  patterns: ['(?i)\b[a-z_]+=[A-Za-z0-9+/]{24,}']
+`),
+		"agents/flag-investigator.yaml": files["agents/flag-investigator.yaml"],
+	})
+	escapes := `"` + strings.Repeat(`\\u0041`, 25) + `"`
+	shown, err := json.Marshal(`QUJDREVGR0hJSktMTU5PUFF\/S is the key id`)
+	require.NoError(t, err)
+
+	first := e.post(t, strings.Replace(request, `"Find why checkout fails."`, escapes, 1))
+	second := e.post(t, strings.Replace(strings.Replace(next, `"Find why checkout fails."`, escapes, 1), `"errors rose"`, string(shown), 1))
+
+	require.Equal(t, 200, first.Status, string(first.Body))
+	require.Equal(t, 200, second.Status, string(second.Body))
+	assert.Equal(t, 2, e.vendor.attempts())
+	for _, rec := range e.calls.Records() {
+		assert.NotEmpty(t, rec.Request, "every call has its record")
+	}
+	assert.Equal(t, e.vendor.bodies[1], e.calls.Records()[1].Request, "the record holds what was forwarded")
+}
+
 // A session refused too often in a round is shut out until the next: one
 // more refusal is recorded, then its requests are refused unrecorded.
 func TestASessionRefusedTooOftenIsShutOutForTheRound(t *testing.T) {
 	e := newEnv(t, func(o *app.Options) { o.MaxRefusals = 3 })
 	bad := strings.Replace(request, `{"model"`, `{"metadata": {}, "model"`, 1)
@@ -470,10 +506,314 @@ func TestARunsBudgetStopsItsCalls(t *testing.T) {
 	other.Run, other.Budget = "run-2", 1_000_000
 	e.sessions.Put("other-token", &other)
 	assert.Equal(t, 200, e.send(t, "other-token", request).Status)
 }
 
+// withAgents is a configuration with n agents, agent-1 to agent-n, on the
+// gateway's model.
+func withAgents(n int) map[string][]byte {
+	fs := map[string][]byte{"redaction-rules/api-keys.yaml": files["redaction-rules/api-keys.yaml"]}
+	for a := 1; a <= n; a++ {
+		fs[fmt.Sprintf("agents/agent-%d.yaml", a)] = fmt.Appendf(nil, `kind: Agent
+metadata: {name: agent-%d, owner: alice@example.com}
+spec:
+  runtime: thinloop
+  model: claude-haiku-5-5
+  tools: []
+`, a)
+	}
+	return fs
+}
+
+// atOnce sends request with each token at once, while the record of the
+// first call admitted waits to be committed, and returns the replies. No
+// other call is admitted meanwhile: the admission that lets a call start
+// and the commit of its record are made one call at a time.
+func (e *env) atOnce(t *testing.T, tokens ...string) []*app.Reply {
+	t.Helper()
+	release := e.calls.HoldBegins()
+	replies := make([]*app.Reply, len(tokens))
+	var wg sync.WaitGroup
+	for i, tok := range tokens {
+		wg.Go(func() { replies[i] = e.send(t, tok, request) })
+	}
+	require.Eventually(t, func() bool { return e.calls.Held() >= 1 }, 5*time.Second, time.Millisecond)
+	require.Never(t, func() bool { return e.calls.Held() > 1 }, 200*time.Millisecond, time.Millisecond,
+		"another call was admitted while the record of the first waited")
+	release()
+	wg.Wait()
+	return replies
+}
+
+// A run passes its budget by one call at most, however many of its agents
+// ask at once: each admission counts every call admitted before it. Every
+// agent here asks at a spend of nothing, under the budget of 1; once the
+// first call's record is committed, the others are refused.
+func TestARunPassesItsBudgetByOneCallAtMost(t *testing.T) {
+	const n = 4
+	e := newEnv(t)
+	e.configure(t, withAgents(n))
+	tokens := make([]string, n)
+	for a := range n {
+		s := e.session
+		s.Agent, s.Budget = fmt.Sprintf("agent-%d", a+1), 1
+		tokens[a] = fmt.Sprintf("token-%d", a+1)
+		e.sessions.Put(tokens[a], &s)
+	}
+
+	replies := e.atOnce(t, tokens...)
+
+	assert.Equal(t, 1, e.vendor.attempts())
+	refused := 0
+	for _, r := range replies {
+		if r.Status == 403 {
+			refused++
+			assert.Contains(t, string(r.Body), "budget")
+		}
+	}
+	assert.Equal(t, n-1, refused)
+}
+
+// heldCalls is the record of model calls, whose Begin of a call of the run
+// hold names waits until gate is closed, and whose End of a refused call
+// waits until ends is closed, counting those that wait. It counts the reads
+// of a run's spend in spends.
+type heldCalls struct {
+	app.Calls
+	hold    string
+	gate    chan struct{}
+	blocked atomic.Int32
+	ends    chan struct{}
+	waiting atomic.Int32
+	spends  atomic.Int32
+	// panics, when set, makes every second read of that run's spend panic:
+	// the one a call makes under the admission lock.
+	panics string
+	reads  atomic.Int32
+}
+
+func (h *heldCalls) Begin(ctx context.Context, rec *app.CallRecord) error {
+	if rec.Run == h.hold {
+		h.blocked.Add(1)
+		select {
+		case <-h.gate:
+		case <-ctx.Done():
+		}
+	}
+	return h.Calls.Begin(ctx, rec)
+}
+
+func (h *heldCalls) End(ctx context.Context, o *app.Outcome) error {
+	if o.Status == app.StatusRefused && h.ends != nil {
+		h.waiting.Add(1)
+		defer h.waiting.Add(-1)
+		select {
+		case <-h.ends:
+		case <-ctx.Done():
+		}
+	}
+	return h.Calls.End(ctx, o)
+}
+
+func (h *heldCalls) Spent(ctx context.Context, run string) (int64, error) {
+	h.spends.Add(1)
+	if run == h.panics && h.reads.Add(1)%2 == 0 {
+		panic("the store failed")
+	}
+	return h.Calls.Spent(ctx, run)
+}
+
+// held gives the gateway h as its record of model calls.
+func (e *env) held(t *testing.T, h *heldCalls) {
+	t.Helper()
+	h.Calls = e.calls
+	e.ports.Calls = h
+	e.restart(t)
+}
+
+// A call refused when it is admitted again is recorded once the admission
+// lock is given up: recording it holds no other call's admission back. Here
+// four agents of one run ask at once, under the budget of 1; three are
+// admitted again once the first call's record is committed, and their
+// refusals wait to be recorded at once.
+func TestARefusalIsRecordedOnceTheAdmissionLockIsGivenUp(t *testing.T) {
+	const n = 4
+	e := newEnv(t)
+	e.configure(t, withAgents(n))
+	h := &heldCalls{ends: make(chan struct{})}
+	e.held(t, h)
+	tokens := make([]string, n)
+	for a := range n {
+		s := e.session
+		s.Agent, s.Budget = fmt.Sprintf("agent-%d", a+1), 1
+		tokens[a] = fmt.Sprintf("token-%d", a+1)
+		e.sessions.Put(tokens[a], &s)
+	}
+	release := e.calls.HoldBegins()
+	replies := make([]*app.Reply, n)
+	var wg sync.WaitGroup
+	for i, tok := range tokens {
+		wg.Go(func() { replies[i] = e.send(t, tok, request) })
+	}
+	// The first call admitted waits for its record, under the lock, having
+	// read its run's spend twice; the others, each admitted once, wait for
+	// the lock.
+	require.Eventually(t, func() bool { return e.calls.Held() == 1 && h.spends.Load() == n+1 }, 5*time.Second, time.Millisecond)
+	release()
+
+	require.Eventually(t, func() bool { return h.waiting.Load() == n-1 }, 5*time.Second, time.Millisecond,
+		"the refusals are recorded at once, none of them under the lock")
+	close(h.ends)
+	wg.Wait()
+
+	refused := 0
+	for _, r := range replies {
+		if r.Status == 403 {
+			refused++
+		}
+	}
+	assert.Equal(t, n-1, refused)
+}
+
+// A request that ends while it waits for the admission lock never begins:
+// it is answered at once, and its record counts nothing.
+func TestARequestThatEndsWhileWaitingToBeAdmittedNeverBegins(t *testing.T) {
+	e := newEnv(t)
+	h := &heldCalls{hold: "run-a", gate: make(chan struct{})}
+	e.held(t, h)
+	for _, run := range []string{"run-a", "run-b"} {
+		s := e.session
+		s.Run = run
+		e.sessions.Put(run, &s)
+	}
+	var wg sync.WaitGroup
+	wg.Go(func() { e.send(t, "run-a", request) })
+	require.Eventually(t, func() bool { return h.blocked.Load() == 1 }, 5*time.Second, time.Millisecond, "run-a's call holds the lock")
+	ctx, cancel := context.WithCancel(t.Context())
+	answered := make(chan *app.Reply, 1)
+	go func() { answered <- e.sendWith(ctx, "run-b", request) }()
+	// run-a read its run's spend twice, and run-b once, as it was first
+	// admitted: run-b goes on to the lock, which it waits for.
+	require.Eventually(t, func() bool { return h.spends.Load() == 3 }, 5*time.Second, time.Millisecond, "run-b was first admitted")
+	time.Sleep(20 * time.Millisecond)
+
+	cancel()
+	var r *app.Reply
+	select {
+	case r = <-answered:
+	case <-time.After(2 * time.Second):
+	}
+	close(h.gate)
+	wg.Wait()
+
+	require.NotNil(t, r, "the request was answered while the lock was held")
+	assert.Equal(t, 502, r.Status, string(r.Body))
+	for _, rec := range e.calls.Records() {
+		if rec.Run == "run-b" {
+			assert.Zero(t, rec.Estimate, "its record counts nothing")
+			_, o, _, _ := e.calls.Record(rec.ID)
+			assert.Contains(t, o.Reason, "before the call was admitted")
+		}
+	}
+	assert.Equal(t, 1, e.vendor.attempts())
+}
+
+// A panic while a call holds the admission lock gives the lock up, and the
+// vendor's slot the call took: the gateway goes on admitting and making
+// other calls, here after two panics with two slots at the vendor.
+func TestAPanicUnderTheAdmissionLockGivesItUp(t *testing.T) {
+	e := newEnv(t, func(o *app.Options) { o.Vendor.Slots = 2 })
+	h := &heldCalls{panics: "run-p"}
+	e.held(t, h)
+	for _, run := range []string{"run-p", "run-q"} {
+		s := e.session
+		s.Run = run
+		e.sessions.Put(run, &s)
+	}
+	for range 2 {
+		func() {
+			defer func() { _ = recover() }()
+			e.send(t, "run-p", request)
+		}()
+	}
+
+	answered := make(chan *app.Reply, 1)
+	go func() { answered <- e.send(t, "run-q", request) }()
+
+	select {
+	case r := <-answered:
+		assert.Equal(t, 200, r.Status, string(r.Body))
+	case <-time.After(3 * time.Second):
+		t.Fatal("the admission lock, or a slot at the vendor, was never given up")
+	}
+}
+
+// The day passes its cap by one call at most, however many runs ask at once.
+func TestTheDayPassesItsCapByOneCallAtMost(t *testing.T) {
+	const n = 4
+	e := newEnv(t, func(o *app.Options) { o.Limits.DailyCap = 1 })
+	tokens := make([]string, n)
+	for a := range n {
+		s := e.session
+		s.Run = fmt.Sprintf("run-at-once-%d", a+1)
+		tokens[a] = fmt.Sprintf("token-%d", a+1)
+		e.sessions.Put(tokens[a], &s)
+	}
+
+	replies := e.atOnce(t, tokens...)
+
+	assert.Equal(t, 1, e.vendor.attempts())
+	refused := 0
+	for _, r := range replies {
+		if r.Status == 403 {
+			refused++
+			assert.Contains(t, string(r.Body), "daily token cap")
+		}
+	}
+	assert.Equal(t, n-1, refused)
+}
+
+// A call counts in the day it was admitted against: one that waits for a
+// slot across midnight is admitted again in the new day, and its record says
+// so, so that the new day's spend counts it.
+func TestACallCountsInTheDayItWasAdmittedAgainst(t *testing.T) {
+	late := time.Date(2026, 10, 17, 23, 59, 59, 0, time.UTC)
+	var now atomic.Pointer[time.Time]
+	now.Store(&late)
+	e := newEnv(t, func(o *app.Options) { o.Vendor.Slots = 2 })
+	e.ports.Now = func() time.Time { return *now.Load() }
+	e.restart(t)
+	e.vendor.block = make(chan struct{})
+	for _, run := range []string{"run-shadow-1", "run-shadow-2"} {
+		shadow := e.session
+		shadow.Run, shadow.Class = run, cost.Shadow
+		e.sessions.Put(run, &shadow)
+	}
+	var wg sync.WaitGroup
+	wg.Go(func() { e.send(t, "run-shadow-1", request) })
+	require.Eventually(t, func() bool { return e.vendor.sent.Load() == 1 }, 5*time.Second, time.Millisecond, "the first holds the one slot comparison runs may hold")
+	answered := make(chan *app.Reply, 1)
+	go func() { answered <- e.send(t, "run-shadow-2", request) }()
+	time.Sleep(100 * time.Millisecond)
+	after := late.Add(2 * time.Second)
+	now.Store(&after)
+	close(e.vendor.block)
+	wg.Wait()
+	require.Equal(t, 200, (<-answered).Status)
+
+	var rec app.CallRecord
+	for _, r := range e.calls.Records() {
+		if r.Run == "run-shadow-2" {
+			rec = r
+		}
+	}
+	assert.Equal(t, cost.DayStart(after), cost.DayStart(rec.At), "the call arrived before midnight and was admitted after")
+	spent, err := e.calls.SpentSince(t.Context(), cost.DayStart(after))
+	require.NoError(t, err)
+	assert.Positive(t, spent)
+}
+
 // Comparison and probation runs are cut at 80% of the daily cap; primary
 // runs go on until the cap (spec Section 7).
 func TestShadowRunsAreCutFirst(t *testing.T) {
 	e := newEnv(t, func(o *app.Options) { o.Limits.DailyCap = 12_000 })
 	require.Equal(t, 200, e.post(t, request).Status, "5,000 spent today")
```

Apply this change to `internal/llm/completion/completion_test.go`:

```diff
diff --git a/internal/llm/completion/completion_test.go b/internal/llm/completion/completion_test.go
index c845207..b8ee9fc 100644
--- a/internal/llm/completion/completion_test.go
+++ b/internal/llm/completion/completion_test.go
@@ -101,16 +101,18 @@ func TestAnAnswerGivesNoIdNoRequestCouldSendBack(t *testing.T) {
 	require.NoError(t, err)
 	assert.Equal(t, []string{edge, "toolu_10"}, c.Calls)
 }
 
 // Cache writes never count below what the vendor reports in all: without
-// a breakdown, or beyond it, they count as one-hour writes, the dearer.
+// a breakdown, or beyond it, they count as five-minute writes, the only
+// kind a request may ask for (prompt.Check), and so as a call's estimate
+// counts them; one-hour writes the vendor reports count as such.
 func TestCacheWritesNeverCountBelowTheTotal(t *testing.T) {
 	for usage, want := range map[string]cost.Usage{
-		`"cache_creation_input_tokens": 50`: {Input: 1, CacheWrite1h: 50, Output: 2},
+		`"cache_creation_input_tokens": 50`: {Input: 1, CacheWrite5m: 50, Output: 2},
 		`"cache_creation_input_tokens": 9, "cache_creation": {"ephemeral_5m_input_tokens": 2, "ephemeral_1h_input_tokens": 3}`: {
-			Input: 1, CacheWrite5m: 2, CacheWrite1h: 7, Output: 2},
+			Input: 1, CacheWrite5m: 6, CacheWrite1h: 3, Output: 2},
 		`"cache_creation_input_tokens": 9, "cache_creation": {"ephemeral_5m_input_tokens": 5, "ephemeral_1h_input_tokens": 5}`: {
 			Input: 1, CacheWrite5m: 5, CacheWrite1h: 5, Output: 2},
 	} {
 		c, err := completion.Read([]byte(`{"content": [], "usage": {"input_tokens": 1, "output_tokens": 2, `+usage+`}}`), red)
 
```

Apply this change to `internal/llm/cost/cost_test.go`:

```diff
diff --git a/internal/llm/cost/cost_test.go b/internal/llm/cost/cost_test.go
index 50620a8..806f67f 100644
--- a/internal/llm/cost/cost_test.go
+++ b/internal/llm/cost/cost_test.go
@@ -66,12 +66,15 @@ func TestALostAnswerCountsItsWorstCase(t *testing.T) {
 	assert.Equal(t, cost.Usage{CacheWrite5m: cost.MaxCount, Output: cost.MaxCount}, cost.Estimate(1<<62, 1<<62), "never more than a card prices")
 }
 
 // Cards that cannot price are refused: every rate is above zero, and none
 // is dearer than the dearest a card may hold; long rates only come with a
-// threshold; and a five-minute cache write costs at least the input and the
-// cache read it stands for.
+// threshold, and none is below its base rate; and a five-minute cache write
+// costs at least the input and the cache read it stands for. A call's
+// estimate counts more tokens than its outcome and may pass the threshold
+// where the outcome does not: with a long rate below its base rate, the
+// outcome could cost more than its estimate.
 func TestCardsMustPrice(t *testing.T) {
 	require.NoError(t, card.Check())
 	require.NoError(t, (&cost.Card{Base: card.Base}).Check())
 	require.Error(t, (&cost.Card{Threshold: 100_000, Base: card.Base}).Check(), "no long rates")
 	require.Error(t, (&cost.Card{Base: cost.Rates{Input: -1}}).Check())
@@ -95,10 +98,22 @@ func TestCardsMustPrice(t *testing.T) {
 	require.Error(t, (&cost.Card{Threshold: 100_000, Base: card.Base, Long: longBelow}).Check(), "a long write below the long input")
 	longReadDearer := card.Long
 	longReadDearer.CacheRead = longReadDearer.CacheWrite5m + 1
 	require.Error(t, (&cost.Card{Threshold: 100_000, Base: card.Base, Long: longReadDearer}).Check(), "a long read above the long write")
 	require.NoError(t, (&cost.Card{Base: cost.Rates{Input: 10, Output: 20, CacheWrite5m: 10, CacheWrite1h: 10, CacheRead: 10}}).Check(), "a write equal to the input and the read")
+	for name, lower := range map[string]func(*cost.Rates){
+		"input":    func(r *cost.Rates) { r.Input = card.Base.Input - 1 },
+		"output":   func(r *cost.Rates) { r.Output = card.Base.Output - 1 },
+		"5m write": func(r *cost.Rates) { r.CacheWrite5m = card.Base.CacheWrite5m - 1 },
+		"1h write": func(r *cost.Rates) { r.CacheWrite1h = card.Base.CacheWrite1h - 1 },
+		"read":     func(r *cost.Rates) { r.CacheRead = card.Base.CacheRead - 1 },
+	} {
+		cheaper := card.Long
+		lower(&cheaper)
+		require.Error(t, (&cost.Card{Threshold: card.Threshold, Base: card.Base, Long: cheaper}).Check(), "a long %s below its base rate", name)
+	}
+	require.NoError(t, (&cost.Card{Threshold: card.Threshold, Base: card.Base, Long: card.Base}).Check(), "long rates equal to the base rates")
 }
 
 // The dearest call a card can price fits in its spend: no count and no
 // rate can overflow it.
 func TestTheDearestCallFits(t *testing.T) {
```

Apply this change to `internal/actions/app/board_test.go`:

```diff
diff --git a/internal/actions/app/board_test.go b/internal/actions/app/board_test.go
index e91443c..11a2dba 100644
--- a/internal/actions/app/board_test.go
+++ b/internal/actions/app/board_test.go
@@ -420,10 +420,54 @@ func useLiveKeyRule(t *testing.T, e *env) {
 	files := revisionFiles(t, defaultPins(), "reviewed")
 	files["redaction-rules/live-keys.yaml"] = []byte("kind: RedactionRule\nmetadata: {name: live-keys, owner: security@example.com}\nspec:\n  always: true\n  patterns: ['\\bsk_live_[a-z0-9]{8}\\b']\n")
 	e.use(t, files, "11e11e11e11e11e11e11e11e11e11e11e11e11e1")
 }
 
+// useAssignmentsRule adds a rule that applies always to a name assigned a
+// long value, as name=value.
+func useAssignmentsRule(t *testing.T, e *env) {
+	files := revisionFiles(t, defaultPins(), "reviewed")
+	files["redaction-rules/assignments.yaml"] = []byte("kind: RedactionRule\nmetadata: {name: assignments, owner: security@example.com}\nspec:\n  always: true\n  patterns: ['(?i)\\b[a-z_]+=[A-Za-z0-9+/]{24,}']\n")
+	e.use(t, files, "a55a55a55a55a55a55a55a55a55a55a55a55a55a")
+}
+
+// Text an agent writes for the board shows in read_board's snapshot as a
+// member, such as "claim": "...", and the LLM gateway checks each member as
+// key=value too (Plan 6b). Text whose member has an escape that hides what a
+// rule that applies always removes is refused, though the text on its own
+// holds nothing a rule matches: stored, it would make the LLM gateway refuse
+// every later snapshot, for every agent of the run.
+func TestATextWhoseMemberHidesWhatTheRulesRemoveIsRefused(t *testing.T) {
+	e := newEnv(t)
+	useAssignmentsRule(t, e)
+	ev := e.evidence(t)
+	const hides = `QUJDREVGR0hJSktMTU5PUFF\/S is the key id`
+	// An element of a list is checked on its own, as no member's value.
+	e.up.set(func(u *upstream) {
+		u.replies["get_flag"] = text(`{"flag":"x","on":true,"notes":["QUJDREVGR0hJSktMTU5PUFF\\/S is the key id"],"truncated":false}`)
+	})
+	read := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)
+	require.Equal(t, app.StatusOK, read.Status, read.Text)
+	args := func(v map[string]any) string {
+		raw, err := json.Marshal(v)
+		require.NoError(t, err)
+		return string(raw)
+	}
+
+	for name, call := range map[string][2]string{
+		"a claim":     {"post_evidence", args(map[string]any{"claim": hides, "call_id": read.CallID, "quote": "the key id"})},
+		"a quote":     {"post_evidence", args(map[string]any{"claim": "a key id", "call_id": read.CallID, "quote": hides})},
+		"a statement": {"post_hypothesis", args(map[string]any{"key": map[string]string{"flag": "x"}, "statement": hides, "evidence_ids": []string{ev}})},
+		"a title":     {"post_task", args(map[string]any{"title": hides})},
+	} {
+		r := e.call(t, call[0], call[1])
+
+		assert.Equal(t, app.StatusRefused, r.Status, name)
+		assert.Contains(t, r.Text, "escape", name)
+	}
+}
+
 // A quote is stored as the agent wrote it, so it cannot be redacted: a quote
 // the rules that apply always would change is refused. A part of an output
 // those rules left alone can still hold what they remove.
 func TestAQuoteTheRedactionRulesWouldChangeIsRefused(t *testing.T) {
 	e := newEnv(t)
```

Apply this change to `internal/actions/app/snapshot_internal_test.go`:

```diff
diff --git a/internal/actions/app/snapshot_internal_test.go b/internal/actions/app/snapshot_internal_test.go
index 7da0542..d07ab33 100644
--- a/internal/actions/app/snapshot_internal_test.go
+++ b/internal/actions/app/snapshot_internal_test.go
@@ -85,19 +85,19 @@ func TestASnapshotOfAcceptedPostsPassesTheLLMGateway(t *testing.T) {
 	for range 1000 {
 		posts = append(posts, draw(rng))
 	}
 	snapshots := 0
 	for i, post := range posts {
-		claim, err := agentText(red, post)
+		claim, err := agentText(red, "claim", post)
 		if err != nil {
 			continue
 		}
 		quote := draw(rng)
 		if agentQuote(red, nil, quote) != nil {
 			quote = ""
 		}
-		statement, err := agentText(red, posts[(i+1)%len(posts)])
+		statement, err := agentText(red, "statement", posts[(i+1)%len(posts)])
 		if err != nil {
 			statement = "the rollout broke checkout"
 		}
 		view := board.View{
 			Round: 2, Snapshot: 2,
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -race ./internal/llm/... ./internal/actions/app/`
Expected: FAIL: `TestARefusedPathIsQuotedOnlyWhole` fails, among others

- [ ] **Step 3: Quote each value on its own and by its parts, and record the texts as forwarded**

Apply this change to `internal/llm/prompt/prompt.go`:

```diff
diff --git a/internal/llm/prompt/prompt.go b/internal/llm/prompt/prompt.go
index d791901..2fb8166 100644
--- a/internal/llm/prompt/prompt.go
+++ b/internal/llm/prompt/prompt.go
@@ -18,10 +18,11 @@ import (
 	"io"
 	"regexp"
 	"slices"
 	"strconv"
 	"strings"
+	"unicode"
 	"unicode/utf8"
 
 	"github.com/jchen0426/agent-swarm/internal/actions/redact"
 )
 
@@ -66,10 +67,13 @@ func refuse(format string, args ...any) error {
 // Request is a request that passed every check.
 type Request struct {
 	doc       map[string]any
 	maxTokens int64
 	called    func(id string) bool
+	// redactedBy is the redactor of the last Redact that passed, which
+	// Record needs.
+	redactedBy *redact.Redactor
 }
 
 // Model returns the model the request names.
 func (r *Request) Model() string { return r.doc["model"].(string) }
 
@@ -113,11 +117,11 @@ func Check(header map[string][]string, body []byte, p *Policy) (*Request, error)
 // fields are the top-level fields a request may hold, each with its check:
 // the fields the runtime sends.
 var fields = map[string]func(r *Request, p *Policy, v any) error{
 	"model": func(_ *Request, p *Policy, v any) error {
 		if s, ok := v.(string); !ok || s != p.Model {
-			return refuse("the model is not the agent's: %s", Quote(fmt.Sprint(v), p.Hides))
+			return refuse("the model is not the agent's: %s", quoteValue(v, p.Hides))
 		}
 		return nil
 	},
 	"max_tokens": func(r *Request, p *Policy, v any) error {
 		n, ok := v.(json.Number)
@@ -197,11 +201,11 @@ func checkContent(v any, where string, kinds blocks, p *Policy) error {
 		if !ok {
 			return refuse("block %d of %s is not an object", i, where)
 		}
 		typ, _ := block["type"].(string)
 		if !slices.Contains(kinds, typ) {
-			return refuse("block %d of %s is of a kind not allowed: %s", i, where, Quote(fmt.Sprint(block["type"]), p.Hides))
+			return refuse("block %d of %s is of a kind not allowed: %s", i, where, quoteValue(block["type"], p.Hides))
 		}
 		if err := checkBlock(block, typ, fmt.Sprintf("block %d of %s", i, where), p); err != nil {
 			return err
 		}
 	}
@@ -358,11 +362,11 @@ func checkTools(v any, p *Policy) error {
 		if !ok {
 			return refuse("tool %d is not an object", i)
 		}
 		where := fmt.Sprintf("tool %d", i)
 		if typ, ok := tool["type"]; ok {
-			return refuse("tool %d has a type, as a tool the vendor runs does: %s", i, Quote(fmt.Sprint(typ), p.Hides))
+			return refuse("tool %d has a type, as a tool the vendor runs does: %s", i, quoteValue(typ, p.Hides))
 		}
 		if err := only(tool, where, p, "name", "description", "input_schema", "cache_control"); err != nil {
 			return err
 		}
 		if !named(tool["name"]) {
@@ -417,21 +421,59 @@ func Quote(s string, hides func(string) bool) string {
 		}
 	}
 	return fmt.Sprintf("a value of %d bytes", len(s))
 }
 
+// QuoteParts writes text a client sent that is made of parts, as a path, a
+// query or a header's list is, for a refusal: as Quote writes it, and only
+// when hides finds nothing in any part on its own, since a rule anchored to
+// a whole value matches the part, not the text the parts make. Parts are
+// what separators and white space divide.
+func QuoteParts(s, separators string, hides func(string) bool) string {
+	if hides != nil {
+		parts := strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(separators, r) })
+		if slices.ContainsFunc(parts, hides) {
+			return fmt.Sprintf("a value of %d bytes", len(s))
+		}
+	}
+	return Quote(s, hides)
+}
+
+// quoteValue writes a JSON value of the client's, for a refusal: a string as
+// Quote writes it, and any other value by its kind only. A value written out
+// whole, such as a list of strings, is a string composed from the client's
+// values, and a rule anchored to a whole value would check it, not each
+// value on its own.
+func quoteValue(v any, hides func(string) bool) string {
+	switch v := v.(type) {
+	case string:
+		return Quote(v, hides)
+	case bool:
+		return "true or false"
+	case json.Number:
+		return "a number"
+	case []any:
+		return "a list"
+	case map[string]any:
+		return "an object"
+	}
+	return "none"
+}
+
 func checkHeaders(header map[string][]string, p *Policy) error {
 	for name, values := range header {
 		switch {
 		case strings.EqualFold(name, "anthropic-version"):
 			for _, v := range values {
 				if v != p.Version {
 					return refuse("anthropic-version %s is not the gateway's, %s", Quote(v, p.Hides), p.Version)
 				}
 			}
-		case strings.EqualFold(name, "anthropic-beta"):
-			return refuse("no anthropic-beta is allowed: %s", Quote(strings.Join(values, ","), p.Hides))
+		case strings.EqualFold(name, "anthropic-beta") && len(values) > 0:
+			// Only the first value is quoted, part by part: values joined
+			// would be a string composed from them (quoteValue).
+			return refuse("no anthropic-beta is allowed: %s", QuoteParts(values[0], ",", p.Hides))
 		case strings.EqualFold(name, "content-encoding"):
 			for _, v := range values {
 				if !strings.EqualFold(strings.TrimSpace(v), "identity") {
 					return refuse("a request body is sent as it is, not as %s", Quote(v, p.Hides))
 				}
@@ -494,11 +536,11 @@ func (r *Request) Redact(red *redact.Redactor) error {
 				return refuse("the input schema of tool %d holds escapes that may hide what a redaction rule matches, and cannot be redacted", i)
 			}
 			tool["input_schema"] = schema
 		}
 	}
-	r.doc = doc
+	r.doc, r.redactedBy = doc, red
 	return nil
 }
 
 // checkNames refuses an id or a name that holds what red hides: tools'
 // names, and the ids and names of tool calls and tool results. It lets
@@ -695,52 +737,69 @@ func encode(v any) []byte {
 
 // Record returns what the record of a forwarded request holds, once Redact
 // has passed it and Pin has pinned it: the request in the gateway's encoding,
 // with every string, key and number redacted as Redacted redacts a body, so
 // that its thinking blocks, and ids the gateway gave the session, are
-// redacted too, though they reach the vendor as the gateway gave them. A text
-// of a tool's result that is one JSON document is kept as it is forwarded:
-// Redact read it value by value, and checked each value for escapes that may
-// hide a match. Read again as text, its escapes, which are the JSON's own,
-// would be checked a level above its values (redactOutput), and the request
-// would be forwarded with no record of it. Record is "" when an escape may
-// hide a match anywhere else, as Redacted is, so that no record holds one.
+// redacted too, though they reach the vendor as the gateway gave them. The
+// texts Redact read on their own (the system prompt, a message's text, every
+// text block, a tool's result and a tool's description) are kept as they
+// are forwarded: Redact redacted each, and checked it for escapes that may
+// hide a match. Read again as values of the request's JSON, each would be
+// checked with its key, as "text=..." or "content=...", which is the
+// request's shape, not the client's text, and a text both gateways passed
+// could not be recorded. Record is "" when an escape may hide a match
+// anywhere else, as Redacted is, so that no record holds one; the gateway
+// refuses such a request. It is "" too unless the last Redact to pass the
+// request used red: the texts are kept as Redact left them, unredacted
+// before Redact runs.
 func (r *Request) Record(red *redact.Redactor) string {
+	if red == nil || r.redactedBy != red {
+		return ""
+	}
 	doc := cloneJSON(r.doc).(map[string]any)
+	if s, ok := doc["system"]; ok {
+		doc["system"] = forwardedTexts(s)
+	}
 	for _, m := range doc["messages"].([]any) {
-		blocks, _ := m.(map[string]any)["content"].([]any)
-		for _, b := range blocks {
-			block := b.(map[string]any)
-			if block["type"] != "tool_result" {
-				continue
-			}
-			switch c := block["content"].(type) {
-			case string:
-				block["content"] = recorded(c)
-			case []any:
-				for _, t := range c {
-					text := t.(map[string]any)
-					text["text"] = recorded(text["text"].(string))
-				}
+		msg := m.(map[string]any)
+		msg["content"] = forwardedTexts(msg["content"])
+	}
+	if tools, ok := doc["tools"].([]any); ok {
+		for _, t := range tools {
+			tool := t.(map[string]any)
+			if d, ok := tool["description"].(string); ok {
+				tool["description"] = forwarded(d)
 			}
 		}
 	}
 	redacted := red.Value(doc)
 	if red.EscapedValue(redacted) {
 		return ""
 	}
 	return string(encode(redacted))
 }
 
-// recorded returns a text of a tool's result as a record holds it: one that
-// is one JSON document as it is forwarded, and any other for redaction to
-// read as text.
-func recorded(s string) any {
-	if _, ok := document(s); ok {
-		return forwarded(s)
+// forwardedTexts marks the texts of a content as forwarded: the content when
+// it is text, each text block's text, and each tool's result.
+func forwardedTexts(content any) any {
+	switch c := content.(type) {
+	case string:
+		return forwarded(c)
+	case []any:
+		for _, b := range c {
+			block := b.(map[string]any)
+			switch block["type"] {
+			case "text":
+				block["text"] = forwarded(block["text"].(string))
+			case "tool_result":
+				if content, ok := block["content"]; ok {
+					block["content"] = forwardedTexts(content)
+				}
+			}
+		}
 	}
-	return s
+	return content
 }
 
 // forwarded is a text that a record keeps as it was forwarded. Redaction
 // reads only what a decoded JSON value holds, so it leaves this one as it is,
 // and encode writes it as the string it is.
```

- [ ] **Step 4: Admit one call at a time, under a lock a request can leave**

Apply this change to `internal/llm/app/gateway.go`:

```diff
diff --git a/internal/llm/app/gateway.go b/internal/llm/app/gateway.go
index 3458a6f..6745fa6 100644
--- a/internal/llm/app/gateway.go
+++ b/internal/llm/app/gateway.go
@@ -138,10 +138,14 @@ type Reply struct {
 type Gateway struct {
 	p      Ports
 	opts   Options
 	vendor *limits.Server
 
+	// admitting is the admission lock: a call holds it from the admission
+	// that lets it start until its record is committed (begin).
+	admitting chan struct{}
+
 	mu       sync.Mutex
 	lanes    map[sessionKey]*lane
 	sessions map[sessionKey]*sessionState
 	tick     uint64
 	// busy counts the requests in the gateway's hands and the calls that
@@ -202,10 +206,11 @@ func New(p *Ports, opts *Options) (*Gateway, error) {
 	vendor := opts.Vendor
 	vendor.AgentsOnly = true
 	return &Gateway{
 		p: *p, opts: *opts, vendor: limits.New(vendor, nil, p.Now).Server("model vendor"),
 		lanes: map[sessionKey]*lane{}, sessions: map[sessionKey]*sessionState{}, drained: make(chan struct{}),
+		admitting: make(chan struct{}, 1),
 	}, nil
 }
 
 // check reports options that cannot hold.
 func (o *Options) check() error {
@@ -342,11 +347,17 @@ func (g *Gateway) Messages(ctx context.Context, in *Inbound) *Reply {
 		return refusal(http.StatusServiceUnavailable, "api_error", "the configuration could not be read; try again", true)
 	}
 	c.red = always(rev).Before(redact.Rule{Literals: []string{in.Token}, Replacement: config.SessionTokenReplacement})
 	switch {
 	case in.Method != http.MethodPost || in.Path != Path || in.Query != "":
-		return c.refuse(ctx, http.StatusNotFound, "not_found_error", fmt.Sprintf("only POST %s is offered, not %s %s", Path, prompt.Quote(in.Method, c.hides), prompt.Quote(in.Path+"?"+in.Query, c.hides)))
+		// The path and the query are quoted apart, part by part, so that
+		// the session's rules check each part on its own.
+		reason := fmt.Sprintf("only POST %s is offered, not %s %s", Path, prompt.Quote(in.Method, c.hides), prompt.QuoteParts(in.Path, "/", c.hides))
+		if in.Query != "" {
+			reason += " with the query " + prompt.QuoteParts(in.Query, "&=;", c.hides)
+		}
+		return c.refuse(ctx, http.StatusNotFound, "not_found_error", reason)
 	case len(in.Body) > g.opts.MaxBodyBytes:
 		return c.refuse(ctx, http.StatusRequestEntityTooLarge, "request_too_large", fmt.Sprintf("a request is at most %d bytes", g.opts.MaxBodyBytes))
 	case !s.Deadline.IsZero() && !time.Now().Before(s.Deadline):
 		return c.refuse(ctx, http.StatusForbidden, "permission_error", "the session's round has ended")
 	}
@@ -379,45 +390,47 @@ func (c *call) serve(ctx context.Context, rev *config.Revision, in *Inbound) *Re
 	if err != nil {
 		c.recordRefused(in.Body)
 		return c.refuse(ctx, http.StatusBadRequest, "invalid_request_error", err.Error())
 	}
 	c.rec.Model = req.Model()
+	req.Pin(serviceTier, g.opts.InferenceGeo)
+	record := req.Record(c.red)
+	if record == "" {
+		// The record reads each member with its key, and an escape that a
+		// part passed on its own may hide a match there.
+		c.recordRefused(in.Body)
+		return c.refuse(ctx, http.StatusBadRequest, "invalid_request_error", "the request holds escapes that may hide what a redaction rule matches, so it cannot be recorded")
+	}
 	if reply := c.admit(ctx, in.Body); reply != nil {
 		return reply
 	}
-	req.Pin(serviceTier, g.opts.InferenceGeo)
 	body := req.Encode()
-	record := req.Record(c.red)
 	slot, reply := c.acquire(ctx, in.Body)
 	if reply != nil {
 		return reply
 	}
+	// The slot is given back however the call ends before it is forwarded,
+	// a panic's way included; a call forwarded gives it back once its
+	// attempts end.
+	forwarded := false
+	defer func() {
+		if !forwarded {
+			slot()
+		}
+	}()
 	// The wait for a slot can be long, as a comparison run's call waits out
 	// the vendor's cool-down, and the run and the day go on spending
 	// meanwhile: the call is admitted again before it starts.
-	if reply := c.admit(ctx, in.Body); reply != nil {
-		slot()
+	estimate := cost.Weighted(card.Spend(cost.Estimate(int64(len(body)), req.MaxTokens())), g.opts.Reference)
+	if reply := c.begin(ctx, in.Body, record, int64(len(body)), estimate); reply != nil {
 		return reply
 	}
-	sum := sha256.Sum256([]byte(record))
-	c.rec.Request, c.rec.RequestBytes, c.rec.RequestDigest = record, int64(len(body)), hex.EncodeToString(sum[:])
-	c.rec.Estimate = cost.Weighted(card.Spend(cost.Estimate(c.rec.RequestBytes, req.MaxTokens())), g.opts.Reference)
-	if !c.store(ctx, func(ctx context.Context) error { return g.p.Calls.Begin(ctx, c.rec) }) {
-		slot()
-		// The record may have been committed though Begin failed, as when
-		// the database's answer is lost. An outcome that says the call was
-		// not made keeps it from counting its worst case for good; where no
-		// record was committed, there is no call to end, and the outcome is
-		// refused, which is harmless.
-		c.end(ctx, &Outcome{ID: c.rec.ID, Status: StatusError, Reason: "the call was not made: it could not be recorded"})
-		return refusal(http.StatusServiceUnavailable, "api_error", "the call could not be recorded, so it was not made", true)
-	}
 	if err := c.over(ctx); err != nil {
-		slot()
 		c.notMade(ctx, "the call was not made: "+err.Error())
 		return c.cut("before the call was made")
 	}
+	forwarded = true
 	return c.forward(ctx, slot, body, req.MaxTokens(), &card)
 }
 
 // notMade records that a call recorded was not made, which costs nothing,
 // trying again apart from the request while the record fails. It counts
@@ -437,27 +450,88 @@ func (c *call) notMade(ctx context.Context, reason string) {
 }
 
 // admit reports, as a refusal, a call its run's budget or the day's spend
 // does not allow.
 func (c *call) admit(ctx context.Context, body []byte) *Reply {
+	reply, refused := c.spending(ctx)
+	if refused != nil {
+		c.recordRefused(body)
+		return c.refuse(ctx, http.StatusForbidden, "permission_error", refused.Error())
+	}
+	return reply
+}
+
+// spending reads the run's spend and the day's. It returns a reply when
+// either cannot be read, and, as an error, the refusal of a call they do not
+// allow. The call counts in the day it is admitted against: its record
+// takes the time of the admission.
+func (c *call) spending(ctx context.Context) (*Reply, error) {
 	var run, today int64
+	now := c.g.p.Now()
+	c.rec.At = now
 	if !c.store(ctx, func(ctx context.Context) (err error) { run, err = c.g.p.Calls.Spent(ctx, c.s.Run); return err }) {
-		return refusal(http.StatusServiceUnavailable, "api_error", "the run's spend could not be read; try again", true)
+		return refusal(http.StatusServiceUnavailable, "api_error", "the run's spend could not be read; try again", true), nil
 	}
 	if !c.store(ctx, func(ctx context.Context) (err error) {
-		today, err = c.g.p.Calls.SpentSince(ctx, cost.DayStart(c.g.p.Now()))
+		today, err = c.g.p.Calls.SpentSince(ctx, cost.DayStart(now))
 		return err
 	}) {
-		return refusal(http.StatusServiceUnavailable, "api_error", "the day's spend could not be read; try again", true)
+		return refusal(http.StatusServiceUnavailable, "api_error", "the day's spend could not be read; try again", true), nil
+	}
+	return nil, cost.Admit(run, c.s.Budget, today, c.s.Class, c.g.opts.Limits)
+}
+
+// begin admits the call again and commits its record, one call at a time
+// across the gateway: each such admission counts every call admitted
+// before it, so that a run passes its budget, and the day its cap, by one
+// call at most. A call waits for the admission lock only while its request,
+// its round and its run go on, and gives the lock up however it returns. A
+// refusal is recorded once the lock is given up, so that recording it holds
+// no other call back. begin returns nil once the call's record is
+// committed.
+func (c *call) begin(ctx context.Context, body []byte, record string, size, estimate int64) *Reply {
+	bounded, cancel := c.bounded(ctx)
+	defer cancel()
+	select {
+	case c.g.admitting <- struct{}{}:
+	case <-bounded.Done():
+		c.recordRefused(body)
+		return c.decline(ctx, StatusError, "the request ended before the call was admitted: "+bounded.Err().Error(), c.cut("before the call was admitted"))
 	}
-	if err := cost.Admit(run, c.s.Budget, today, c.s.Class, c.g.opts.Limits); err != nil {
+	reply, begun, refused := c.admitted(ctx, record, size, estimate)
+	switch {
+	case refused != nil:
 		c.recordRefused(body)
-		return c.refuse(ctx, http.StatusForbidden, "permission_error", err.Error())
+		return c.refuse(ctx, http.StatusForbidden, "permission_error", refused.Error())
+	case reply != nil:
+		return reply
+	case !begun:
+		// The record may have been committed though Begin failed, as when
+		// the database's answer is lost. An outcome that says the call was
+		// not made keeps it from counting its worst case for good; where no
+		// record was committed, there is no call to end, and the outcome is
+		// refused, which is harmless.
+		c.end(ctx, &Outcome{ID: c.rec.ID, Status: StatusError, Reason: "the call was not made: it could not be recorded"})
+		return refusal(http.StatusServiceUnavailable, "api_error", "the call could not be recorded, so it was not made", true)
 	}
 	return nil
 }
 
+// admitted admits the call while it holds the admission lock, which it
+// gives up however it returns, and commits the call's record, with its
+// worst case, when its run and the day allow it. It returns what spending
+// returns, and whether the record was committed.
+func (c *call) admitted(ctx context.Context, record string, size, estimate int64) (reply *Reply, begun bool, refused error) {
+	defer func() { <-c.g.admitting }()
+	if reply, refused = c.spending(ctx); reply != nil || refused != nil {
+		return reply, false, refused
+	}
+	sum := sha256.Sum256([]byte(record))
+	c.rec.Request, c.rec.RequestBytes, c.rec.RequestDigest, c.rec.Estimate = record, size, hex.EncodeToString(sum[:]), estimate
+	return nil, c.store(ctx, func(ctx context.Context) error { return c.g.p.Calls.Begin(ctx, c.rec) }), nil
+}
+
 // acquire takes a slot at the vendor for a call, unless the vendor is
 // failing, or the request, the round or the run ends first; a call that
 // does not start is declined.
 func (c *call) acquire(ctx context.Context, body []byte) (func(), *Reply) {
 	bounded, cancel := c.bounded(ctx)
```

- [ ] **Step 5: Price what the estimate covers**

Apply this change to `internal/llm/completion/completion.go`:

```diff
diff --git a/internal/llm/completion/completion.go b/internal/llm/completion/completion.go
index f9090a9..62f4268 100644
--- a/internal/llm/completion/completion.go
+++ b/internal/llm/completion/completion.go
@@ -90,11 +90,13 @@ func Read(body []byte, red *redact.Redactor) (*Completion, error) {
 	return c, nil
 }
 
 // read reads the token counts. Cache writes never count below the total
 // the vendor reports: without a breakdown, and beyond it, they count as
-// one-hour writes, the dearer.
+// five-minute writes, the only kind a request may ask for, and so as the
+// call's estimate counts them (cost.Estimate). One-hour writes the vendor
+// reports count as such.
 func read(usage map[string]any) (cost.Usage, error) {
 	var u cost.Usage
 	var written int64
 	counts := []struct {
 		name     string
@@ -111,22 +113,22 @@ func read(usage map[string]any) (cost.Usage, error) {
 		}
 		*c.to = n
 	}
 	breakdown, ok := usage["cache_creation"].(map[string]any)
 	if !ok {
-		u.CacheWrite1h = written
+		u.CacheWrite5m = written
 		return u, nil
 	}
 	m5, err := count(breakdown, "ephemeral_5m_input_tokens", false)
 	if err != nil {
 		return cost.Usage{}, err
 	}
 	h1, err := count(breakdown, "ephemeral_1h_input_tokens", false)
 	if err != nil {
 		return cost.Usage{}, err
 	}
-	u.CacheWrite5m, u.CacheWrite1h = m5, h1+max(written-m5-h1, 0)
+	u.CacheWrite5m, u.CacheWrite1h = m5+max(written-m5-h1, 0), h1
 	return u, nil
 }
 
 // count reads one token count: a whole number from 0 to cost.MaxCount.
 func count(obj map[string]any, name string, required bool) (int64, error) {
```

Apply this change to `internal/llm/cost/cost.go`:

```diff
diff --git a/internal/llm/cost/cost.go b/internal/llm/cost/cost.go
index 91015d4..5c6937d 100644
--- a/internal/llm/cost/cost.go
+++ b/internal/llm/cost/cost.go
@@ -68,15 +68,18 @@ type Card struct {
 	Base, Long Rates
 }
 
 // Check reports a card that cannot price a call as the vendor charges it: a
 // rate missing or past the bounds; long rates, which a card with a threshold
-// needs and one without may not have; and a five-minute cache write that
-// costs less than the input or the cache read it stands for. Estimate counts
-// a lost call's request as five-minute cache writes, its worst case, so a
-// write cheaper than either would price that worst case below what the same
-// tokens cost.
+// needs and one without may not have, and which may be no lower than the
+// base rates; and a five-minute cache write that costs less than the input
+// or the cache read it stands for. Estimate counts a lost call's request as
+// five-minute cache writes, its worst case, so a write cheaper than either
+// would price that worst case below what the same tokens cost. An estimate
+// counts more tokens than the call's outcome, and may pass the threshold
+// where the outcome does not, so a long rate below its base rate would
+// price the outcome above its estimate.
 func (c *Card) Check() error {
 	switch {
 	case !c.Base.valid():
 		return fmt.Errorf("cost: a card needs every rate above zero and at most %d pico-USD a token", MaxRate)
 	case !c.Base.writeCovers():
@@ -87,14 +90,22 @@ func (c *Card) Check() error {
 		return errors.New("cost: a card with a threshold needs long rates")
 	case c.Threshold > 0 && !c.Long.writeCovers():
 		return errors.New("cost: a card's long five-minute cache write costs less than its long input or its long cache read")
 	case c.Threshold == 0 && c.Long != (Rates{}):
 		return errors.New("cost: a card without a threshold has no long rates")
+	case c.Threshold > 0 && !c.Long.covers(c.Base):
+		return errors.New("cost: a card's long rates cost less than its base rates")
 	}
 	return nil
 }
 
+// covers reports whether every rate of r is at least base's.
+func (r Rates) covers(base Rates) bool {
+	return r.Input >= base.Input && r.Output >= base.Output && r.CacheWrite5m >= base.CacheWrite5m &&
+		r.CacheWrite1h >= base.CacheWrite1h && r.CacheRead >= base.CacheRead
+}
+
 // Spend returns what usage costs, in pico-USD.
 func (c *Card) Spend(u Usage) int64 {
 	r := c.Base
 	if c.Threshold > 0 && u.Prompt() > c.Threshold {
 		r = c.Long
```

- [ ] **Step 6: Check board text as the snapshot holds it**

Apply this change to `internal/actions/app/board.go`:

```diff
diff --git a/internal/actions/app/board.go b/internal/actions/app/board.go
index 6c2790d..20dd7b6 100644
--- a/internal/actions/app/board.go
+++ b/internal/actions/app/board.go
@@ -214,11 +214,11 @@ func (g *Gateway) finishBoard(ctx context.Context, rec *CallRecord, status Statu
 }
 
 // post makes one board post for the session's agent, in its round.
 func (g *Gateway) post(ctx context.Context, s *Session, rev *config.Revision, red *redact.Redactor, name string, v map[string]any) (board.Posted, error) {
 	me := board.Author{Kind: board.AuthorAgent, Name: s.Agent}
-	text := func(field string) (string, error) { return agentText(red, str(v, field)) }
+	text := func(field string) (string, error) { return agentText(red, field, str(v, field)) }
 	switch name {
 	case postEvidence:
 		claim, err := text("claim")
 		if err != nil {
 			return board.Posted{}, err
@@ -264,18 +264,22 @@ func (g *Gateway) post(ctx context.Context, s *Session, rev *config.Revision, re
 	default:
 		return g.p.Board.EndTurn(ctx, s.Run, s.Round, me)
 	}
 }
 
-// agentText redacts text an agent wrote for the board with the rules that
-// apply always. Text with a hidden character, or with an escape that hides
-// what the rules remove, is refused, so the agent writes it plainly.
-func agentText(red *redact.Redactor, s string) (string, error) {
+// agentText redacts text an agent wrote for the board, as field, with the
+// rules that apply always. Text with a hidden character, or with an escape
+// that hides what the rules remove, is refused, so the agent writes it
+// plainly. read_board's snapshot holds the text as the member field, which
+// the LLM gateway also checks as field=text (redact.EscapedValue): text whose
+// member has such an escape is refused too, or it would make the gateway
+// refuse every later snapshot.
+func agentText(red *redact.Redactor, field, s string) (string, error) {
 	if strings.ContainsFunc(s, config.Hidden) {
 		return "", fmt.Errorf("%w: %w", board.ErrInvalid, errHiddenText)
 	}
-	if red.Escaped(s) {
+	if red.Escaped(s) || red.Escaped(field+"="+s) {
 		return "", fmt.Errorf("%w: %w", board.ErrInvalid, errEscapedText)
 	}
 	return red.Text(s), nil
 }
 
@@ -285,15 +289,19 @@ func agentText(red *redact.Redactor, s string) (string, error) {
 // alone can still hold what they remove, as when a pattern needs a word
 // boundary the output does not have. A quote with one of marks, what a
 // rule put in place of text it hid, inside it, between text on either side,
 // is refused as well: the hidden text could change what the quote says, as
 // when an agent hides words of an answer by writing them into a hidden
-// argument of its read. A quote may still begin or end with a mark.
+// argument of its read. A quote may still begin or end with a mark. The
+// snapshot holds a quote as the member quote, checked as agentText's are.
 func agentQuote(red *redact.Redactor, marks []string, quote string) error {
 	if strings.ContainsFunc(quote, config.Hidden) {
 		return fmt.Errorf("%w: the quote holds a character a reader could miss; quote text without it", board.ErrInvalid)
 	}
+	if red.Escaped("quote=" + quote) {
+		return fmt.Errorf("%w: the quote, as the board shows it, holds an escape that hides what the redaction rules remove; quote text without it", board.ErrInvalid)
+	}
 	if red.Escaped(quote) || red.Text(quote) != quote {
 		return fmt.Errorf("%w: the quote holds text the redaction rules remove; quote text without it", board.ErrInvalid)
 	}
 	q := board.NormalizeSpace(quote)
 	for _, m := range marks {
```

- [ ] **Step 7: Run the tests to see them pass**

Run: `go test -race ./internal/llm/... ./internal/actions/... && golangci-lint run ./...`
Expected: every package `ok`; `0 issues.`

- [ ] **Step 8: Commit**

```bash
git add internal/llm internal/actions/app
git commit -m "fix(llm): quote each client value on its own and by its parts, record the texts Redact passed as forwarded, hold a run and the day to one call past their limits under a lock a request can leave, and refuse board text whose member hides what the rules remove"
```

### Task 2: The board's stop entry, platform evidence, notices of model calls, and a round's report

Decision 11. The coordinator ends a run's board with a stop entry, which holds the conclusion the run's stop record holds, opens it with the evidence the item opened with, notices model calls on it, one per call in any round, and reads a round's report from it when the round ends. A stopped board refuses a write call before any read is made for it.

**Files:**
- Create: `internal/work/board/report.go`, `internal/work/board/stop_test.go`
- Modify: `internal/work/board/board.go`, `internal/work/board/state.go`, `internal/work/board/round.go`
- Modify: `internal/work/app/boards.go`, `internal/work/app/boards_test.go`
- Modify: `internal/actions/app/board.go`, `internal/actions/app/board_test.go`

**Interfaces:**
- Consumes: the board (Plans 1 and 5b).
- Produces:
  - `board.KindStop`, `board.Stop{Reason string; Conclusion EntryID; Hypotheses, OpenTasks, Proposals []EntryID; Omitted int; Rounds int; WallClock time.Duration; Tokens int64}`, `board.MaxStopListed = 50`, `board.StopInput{Reason string; Conclusion EntryID; Rounds int; WallClock time.Duration; Tokens int64}`, `board.Body.Stop`;
  - `board.ErrStopped`, which `(*State).Proposable` returns too once the run stopped;
  - `board.PlatformEvidenceInput{Claim string; Ref string; Source Source}`;
  - `board.NoticeModelRefused`, `board.NoticeModelFailed`, `board.NoticeModelUnknown`, `board.NoticeNoAgents`; `Notice.Agent` is omitted from JSON when empty;
  - `(*State).Stop(c, id, author, in *StopInput) (Posting, error)`, `(*State).PostPlatformEvidence(c, id, author, in *PlatformEvidenceInput) (Posting, error)`, `(*State).Report(round int) Report`, `(*State).Stopped() (Event, bool)`;
  - `board.Report{Assessment Assessment; Facts RoundFacts; Stopped bool}` with `(*Report).Noticed(callID string) bool`, and `board.RoundFacts{Ended []string; Entries int}`;
  - `(*app.Boards).PostPlatformEvidence(ctx, run, in) (board.Posted, error)`, `(*app.Boards).Stop(ctx, run, in) (board.Posted, error)`, `(*app.Boards).Report(ctx, run, round) (board.Report, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/work/board/stop_test.go`:

```go
package board_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// stopInput is how the coordinator stops the fixture's run.
func stopInput() *board.StopInput {
	return &board.StopInput{Reason: "quiet", Rounds: 2, WallClock: 3 * time.Minute, Tokens: 1234}
}

// The stop entry sums up how the run ended in ids and numbers: the
// conclusion the coordinator recorded, the hypotheses by score, the tasks
// left open and the proposals made, with the budget the coordinator says the
// run used.
func TestTheStopEntrySumsUpTheRun(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.evidence(runtime, "logs", "search", "NullPointerException", h, board.Supports)
	open := f.task(release, "check the pool")
	done := f.task(runtime, "check the deploy")
	_, err := f.claim(runtime, done)
	require.NoError(t, err)
	_, err = f.complete(runtime, done, e1)
	require.NoError(t, err)
	p, err := f.propose(proposal(release, h, `{"service":"checkout"}`))
	require.NoError(t, err)

	in := stopInput()
	in.Conclusion = h
	stop := f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, in))

	assert.Equal(t, board.Coordinator, stop.Author)
	assert.Equal(t, f.ctx.Round, stop.Round)
	assert.Equal(t, &board.Stop{
		Reason: "quiet", Conclusion: h, Hypotheses: []board.EntryID{h}, OpenTasks: []board.EntryID{open},
		Proposals: []board.EntryID{p.ID}, Rounds: 2, WallClock: 3 * time.Minute, Tokens: 1234,
	}, stop.Body.Stop)
	got, stopped := f.state.Stopped()
	require.True(t, stopped)
	assert.Equal(t, stop.ID, got.ID)
}

// The stop entry holds the conclusion the coordinator recorded when it
// decided to stop, not the board's as it stands: a ruling between the two
// cannot make the board and the record disagree. A conclusion that names no
// hypothesis of the board is refused.
func TestTheStopEntryHoldsTheRecordedConclusion(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.evidence(runtime, "logs", "search", "NullPointerException", h, board.Supports)
	require.Equal(t, h, f.state.Assess().Conclusion)
	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})
	require.Empty(t, f.state.Assess().Conclusion)

	for name, in := range map[string]*board.StopInput{
		"not a hypothesis": {Reason: "converged", Conclusion: e1},
		"no such entry":    {Reason: "converged", Conclusion: "e-99"},
	} {
		_, err := f.posted(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, in))
		require.ErrorIs(t, err, board.ErrInvalid, name)
	}
	stop := f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, &board.StopInput{Reason: "converged", Conclusion: h}))

	assert.Equal(t, h, stop.Body.Stop.Conclusion)
	_, err := board.Replay(f.ctx.Settings, f.events)
	require.NoError(t, err)
}

// A write call is refused before any read is made for it once the run
// stops, as its proposal would be.
func TestAWriteCallIsNotProposableOnceTheRunStops(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, stopInput()))

	_, err := f.state.Proposable(&f.ctx, proposal(release, h, `{"service":"checkout"}`))

	require.ErrorIs(t, err, board.ErrStopped)
}

// Once the run stops, its board takes rulings only: no agent's post, no
// proposal, no notice, no round, no platform evidence and no human evidence.
// Stopping it again repeats the stop entry.
func TestAStoppedBoardTakesRulingsOnly(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.evidence(runtime, "logs", "search", "NullPointerException", h, board.Supports)
	task := f.task(release, "check the pool")
	stop := f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, stopInput()))
	callID := f.call(deps, "deps", "list", "redis 7.2")

	for name, post := range map[string]func() (board.Posting, error){
		"evidence": func() (board.Posting, error) {
			return f.state.PostEvidence(&f.ctx, f.nextID("ev"), deps, &board.EvidenceInput{Claim: "redis", CallID: callID, Quote: "redis 7.2"}, f.calls[callID])
		},
		"a hypothesis": func() (board.Posting, error) {
			return f.state.PostHypothesis(&f.ctx, f.nextID("hyp"), deps, &board.HypothesisInput{Key: checkoutDeploy(), Statement: "v42", Evidence: []board.EntryID{e1}})
		},
		"a retraction": func() (board.Posting, error) { return f.state.Retract(&f.ctx, f.nextID("ret"), release, e1) },
		"a task": func() (board.Posting, error) {
			return f.state.PostTask(&f.ctx, f.nextID("task"), deps, &board.TaskInput{Title: "t"})
		},
		"a claim":    func() (board.Posting, error) { return f.state.Claim(&f.ctx, f.nextID("claim"), deps, task) },
		"a turn end": func() (board.Posting, error) { return f.state.EndTurn(&f.ctx, f.nextID("end"), deps) },
		"a proposal": func() (board.Posting, error) {
			return f.state.Propose(&f.ctx, f.nextID("prop"), proposal(release, h, `{"service":"checkout"}`))
		},
		"a notice": func() (board.Posting, error) {
			return f.state.Notify(&f.ctx, f.nextID("n"), board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: deps.Name})
		},
		"a round": func() (board.Posting, error) {
			return f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: 1})
		},
		"platform evidence": func() (board.Posting, error) {
			return f.state.PostPlatformEvidence(&f.ctx, f.nextID("pe"), board.Coordinator, &board.PlatformEvidenceInput{Claim: "resolved", Ref: "rec-9", Source: "alertmanager"})
		},
		"human evidence": func() (board.Posting, error) {
			d, err := f.state.NewEvidence(&f.ctx, f.nextID("ev"), alice, &board.EvidenceInput{Claim: "seen"}, nil)
			return board.Posting{Draft: &d}, err
		},
	} {
		_, err := f.posted(post())

		require.ErrorIs(t, err, board.ErrStopped, name)
	}
	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})
	again := f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, &board.StopInput{Reason: "cut"}))
	assert.Equal(t, stop.ID, again.ID)
	_, err := board.Replay(f.ctx.Settings, f.events)
	require.NoError(t, err, "a ruling after the stop replays")
}

// Only the coordinator stops a run, with a reason written as a code.
func TestOnlyTheCoordinatorStopsARun(t *testing.T) {
	f := newFixture(t)

	_, err := f.posted(f.state.Stop(&f.ctx, f.nextID("stop"), release, stopInput()))
	require.ErrorIs(t, err, board.ErrNotAllowed)
	for _, reason := range []string{"", "Quiet", "the run went quiet", strings.Repeat("q", 33)} {
		_, err = f.posted(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, &board.StopInput{Reason: reason}))
		require.ErrorIs(t, err, board.ErrInvalid, reason)
	}
}

// The platform posts evidence that names the record behind it and its
// source, and posts it once per record: posting it again repeats it.
func TestPlatformEvidenceNamesItsRecordOnce(t *testing.T) {
	f := newFixture(t)
	in := &board.PlatformEvidenceInput{Claim: "Opened by alertmanager: checkout errors above 5%", Ref: "rec-1", Source: "alertmanager"}

	first := f.must(f.state.PostPlatformEvidence(&f.ctx, f.nextID("pe"), board.Coordinator, in))
	again := f.must(f.state.PostPlatformEvidence(&f.ctx, f.nextID("pe"), board.Coordinator, &board.PlatformEvidenceInput{Claim: "other", Ref: "rec-1", Source: "alertmanager"}))

	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, &board.Evidence{Claim: in.Claim, Provenance: board.Provenance{Kind: board.FromPlatform, Ref: "rec-1", Source: "alertmanager"}}, first.Body.Evidence)
	for name, c := range map[string]struct {
		author board.Author
		in     board.PlatformEvidenceInput
		err    error
	}{
		"an agent":     {release, board.PlatformEvidenceInput{Claim: "x", Ref: "rec-2", Source: "s"}, board.ErrNotAllowed},
		"a person":     {alice, board.PlatformEvidenceInput{Claim: "x", Ref: "rec-2", Source: "s"}, board.ErrNotAllowed},
		"no claim":     {board.Coordinator, board.PlatformEvidenceInput{Claim: " ", Ref: "rec-2", Source: "s"}, board.ErrInvalid},
		"a long claim": {board.Coordinator, board.PlatformEvidenceInput{Claim: strings.Repeat("x", board.MaxClaimRunes+1), Ref: "rec-2", Source: "s"}, board.ErrInvalid},
		"no record":    {board.Coordinator, board.PlatformEvidenceInput{Claim: "x", Source: "s"}, board.ErrProvenance},
		"no source":    {board.Coordinator, board.PlatformEvidenceInput{Claim: "x", Ref: "rec-2"}, board.ErrProvenance},
	} {
		_, err := f.posted(f.state.PostPlatformEvidence(&f.ctx, f.nextID("pe"), c.author, &c.in))

		require.ErrorIs(t, err, c.err, name)
	}
}

// A notice of a model call names the call, and each call has its own
// notice; a notice that no agent is left names nothing.
func TestModelNoticesAreOnePerCall(t *testing.T) {
	f := newFixture(t)
	failed := func(call string) *board.Notice {
		return &board.Notice{Code: board.NoticeModelFailed, Agent: release.Name, CallID: call}
	}

	first := f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, failed("m-1")))
	second := f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, failed("m-2")))
	again := f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, failed("m-1")))
	none := f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, &board.Notice{Code: board.NoticeNoAgents}))
	f.round(2)
	later := f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, &board.Notice{Code: board.NoticeModelRefused, Agent: release.Name, CallID: "m-1"}))

	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, first.ID, later.ID, "a call has one notice, in any round")
	assert.Equal(t, board.NoticeNoAgents, none.Body.Notice.Code)
	for name, n := range map[string]*board.Notice{
		"a model notice without its call": {Code: board.NoticeModelRefused, Agent: release.Name},
		"a model notice without an agent": {Code: board.NoticeModelUnknown, CallID: "m-3"},
		"no agents, naming one":           {Code: board.NoticeNoAgents, Agent: release.Name},
		"an unknown code":                 {Code: "model_slow", Agent: release.Name, CallID: "m-3"},
	} {
		_, err := f.posted(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, n))

		require.ErrorIs(t, err, board.ErrInvalid, name)
	}
}

// The gateway's notices of an agent's calls cannot crowd out the notice of
// its model call: the cap of notices in a round counts the gateway's only,
// and a turn ends at its first failed model call (Plan 7d), which bounds the
// model's notices.
func TestAModelCallsNoticeIsNeverCrowdedOut(t *testing.T) {
	f := newFixture(t)
	for i := range board.MaxNotices + 1 {
		f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: release.Name, Tool: fmt.Sprintf("tool_%d", i)}))
	}
	before := len(f.events)

	n := f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, &board.Notice{Code: board.NoticeModelFailed, Agent: release.Name, CallID: "m-1"}))

	assert.Len(t, f.events, before+1)
	assert.Equal(t, "m-1", n.Body.Notice.CallID)
	report := f.state.Report(f.ctx.Round)
	assert.True(t, report.Noticed("m-1"))
}

// A round's report says what the round produced, as the coordinator reads it
// when the round ends: the agents that ended their turn, and how many entries
// count as activity. Notices, turn ends, round entries, platform evidence
// and the stop entry do not; a proposal does, for its proposer. It also
// names the model calls the board holds a notice of, in any round.
func TestARoundsReportSaysWhatTheRoundProduced(t *testing.T) {
	f := newFixture(t)
	f.must(f.state.PostPlatformEvidence(&f.ctx, f.nextID("pe"), board.Coordinator, &board.PlatformEvidenceInput{Claim: "opened", Ref: "rec-1", Source: "person"}))
	f.must(f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: 1}))
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), release))
	f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, &board.Notice{Code: board.NoticeModelFailed, Agent: runtime.Name, CallID: "m-1"}))
	f.round(2)
	f.must(f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: 2}))
	f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), runtime))
	f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), release))
	f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: release.Name}))
	f.round(3)
	f.must(f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: 3}))
	_, err := f.propose(proposal(release, h, `{"service":"checkout"}`))
	require.NoError(t, err)
	f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, stopInput()))

	one, two, three := f.state.Report(1), f.state.Report(2), f.state.Report(3)

	assert.Equal(t, board.RoundFacts{Ended: []string{release.Name}, Entries: 2}, one.Facts)
	assert.Equal(t, board.RoundFacts{Ended: []string{release.Name, runtime.Name}}, two.Facts)
	assert.Equal(t, board.RoundFacts{Ended: []string{}, Entries: 1}, three.Facts)
	assert.Equal(t, h, one.Assessment.Hypotheses[0].ID)
	assert.True(t, three.Noticed("m-1"))
	assert.False(t, three.Noticed("m-2"))
	assert.True(t, three.Stopped)
}
```

Apply this change to `internal/work/app/boards_test.go`:

```diff
diff --git a/internal/work/app/boards_test.go b/internal/work/app/boards_test.go
index e65e85f..bcd8e97 100644
--- a/internal/work/app/boards_test.go
+++ b/internal/work/app/boards_test.go
@@ -220,5 +220,55 @@ func TestAPostThatWaitedWhileANewRoundOpenedIsRefused(t *testing.T) {
 	bc, err := boards.Context(ctx, "run-1")
 	require.NoError(t, err)
 	assert.Equal(t, 2, bc.Round, "the board's round entries say the round, whatever the run said before")
 	assert.Equal(t, int64(1), bc.Snapshot)
 }
+
+// The coordinator posts the evidence an item opens with once, whatever
+// happens after: a post repeated after a crash returns the first.
+func TestPlatformEvidenceIsPostedOnce(t *testing.T) {
+	ctx := context.Background()
+	boards, store := newBoards(t)
+	in := &board.PlatformEvidenceInput{Claim: "Opened by a person: checkout fails", Ref: "rec-1", Source: "person"}
+
+	first, err := boards.PostPlatformEvidence(ctx, "run-1", in)
+	require.NoError(t, err)
+	again, err := boards.PostPlatformEvidence(ctx, "run-1", in)
+	require.NoError(t, err)
+
+	assert.Equal(t, board.Coordinator, first.Event.Author)
+	assert.True(t, again.Repeat)
+	assert.Equal(t, first.Event.ID, again.Event.ID)
+	events, _ := store.Load(ctx, "run-1")
+	assert.Len(t, events, 1)
+}
+
+// Once the coordinator stops a run, its board takes no post or notice, and
+// people's rulings still land; the round's report says it stopped.
+func TestAStoppedRunsBoardTakesRulingsOnly(t *testing.T) {
+	ctx := context.Background()
+	boards, _ := newBoards(t)
+	e1, err := boards.PostEvidence(ctx, "run-1", 1, release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42"})
+	require.NoError(t, err)
+	h, err := boards.PostHypothesis(ctx, "run-1", 1, release, &board.HypothesisInput{
+		Key: key.Key{"service": "checkout", "failureMode": "bad-deploy"}, Statement: "v42 broke checkout", Evidence: []board.EntryID{e1.Event.ID},
+	})
+	require.NoError(t, err)
+
+	stop, err := boards.Stop(ctx, "run-1", &board.StopInput{Reason: "quiet", Rounds: 1})
+	require.NoError(t, err)
+	again, err := boards.Stop(ctx, "run-1", &board.StopInput{Reason: "quiet", Rounds: 1})
+	require.NoError(t, err)
+
+	assert.Equal(t, []board.EntryID{h.Event.ID}, stop.Event.Body.Stop.Hypotheses)
+	assert.True(t, again.Repeat)
+	_, err = boards.EndTurn(ctx, "run-1", 1, release)
+	require.ErrorIs(t, err, board.ErrStopped)
+	_, err = boards.Notice(ctx, "run-1", board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: release.Name})
+	require.ErrorIs(t, err, board.ErrStopped)
+	_, err = boards.Rule(ctx, "run-1", alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h.Event.ID, Reason: &board.Reason{Code: "wrong-cause"}})
+	require.NoError(t, err)
+	report, err := boards.Report(ctx, "run-1", 1)
+	require.NoError(t, err)
+	assert.True(t, report.Stopped)
+	assert.Equal(t, 3, report.Facts.Entries, "the evidence, the hypothesis and the ruling, made in the run's round")
+}
```

Apply this change to `internal/actions/app/board_test.go`:

```diff
diff --git a/internal/actions/app/board_test.go b/internal/actions/app/board_test.go
index 11a2dba..a7df416 100644
--- a/internal/actions/app/board_test.go
+++ b/internal/actions/app/board_test.go
@@ -397,10 +397,24 @@ func TestAPostThatArrivesAfterItsRoundIsRefused(t *testing.T) {
 	assert.Equal(t, app.StatusRefused, r.Status)
 	assert.Contains(t, r.Text, "the round ended")
 	assert.Contains(t, e.rules(t), board.Notice{Code: board.NoticeRefusedCall, Agent: "flag-investigator", Tool: "end_turn", Rule: "round_ended"})
 }
 
+// A board call made after the run stopped is refused, as a call its round no
+// longer allows is, not answered as a board that could not be reached, which
+// the agent would try again.
+func TestABoardCallAfterTheRunStoppedIsRefused(t *testing.T) {
+	e := newEnv(t)
+	_, err := e.boards.Stop(t.Context(), e.bc.Run, &board.StopInput{Reason: "quiet"})
+	require.NoError(t, err)
+
+	r := e.call(t, "end_turn", `{}`)
+
+	assert.Equal(t, app.StatusRefused, r.Status)
+	assert.Contains(t, r.Text, "the run has stopped")
+}
+
 // A quote with a character a reader could miss is refused: the board shows
 // quotes as platform facts.
 func TestAQuoteWithAHiddenCharacterIsRefused(t *testing.T) {
 	e := newEnv(t)
 	e.up.set(func(u *upstream) {
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/work/... ./internal/actions/app/`
Expected: FAIL: the build of `internal/work/app`, `internal/work/board`, `internal/actions/app` fails (undefined: board.PlatformEvidenceInput)

- [ ] **Step 3: The stop entry, platform evidence and the notices**

Apply this change to `internal/work/board/board.go`:

```diff
diff --git a/internal/work/board/board.go b/internal/work/board/board.go
index dca1f0c..eeb5284 100644
--- a/internal/work/board/board.go
+++ b/internal/work/board/board.go
@@ -34,10 +34,13 @@ var (
 	// ErrHeld marks a claim on a task that another agent holds.
 	ErrHeld = errors.New("board: the task is held")
 	// ErrRoundEnded marks an agent's post that arrives after the round it was
 	// made in ended.
 	ErrRoundEnded = errors.New("board: the round ended")
+	// ErrStopped marks an entry other than a ruling on the board of a run
+	// that stopped.
+	ErrStopped = errors.New("board: the run has stopped")
 )
 
 // Size limits that keep entries readable and bound flooding. The spec sets
 // none; these are the implementation's choices.
 const (
@@ -98,10 +101,11 @@ const (
 	KindCompletion Kind = "completion"
 	KindTurnEnd    Kind = "turn_end"
 	KindProposal   Kind = "proposal"
 	KindNotice     Kind = "notice"
 	KindRound      Kind = "round"
+	KindStop       Kind = "stop"
 )
 
 // Stance is a contributor's position on a hypothesis.
 type Stance string
 
@@ -222,10 +226,53 @@ type TurnEnd struct{}
 type RoundStart struct {
 	Round  int     `json:"round"`
 	WrapUp EntryID `json:"wrapUp,omitempty"`
 }
 
+// Stop ends a run's board (spec Section 5.6): the coordinator's summary of
+// how the run ended, in codes, numbers and ids, never text an agent wrote.
+// After it the board takes rulings only.
+type Stop struct {
+	// Reason is the run's stop reason, as a code.
+	Reason     string  `json:"reason"`
+	Conclusion EntryID `json:"conclusion,omitempty"`
+	// Hypotheses are the board's hypotheses by score, OpenTasks the tasks
+	// no one completed and Proposals the proposals made, each at most
+	// MaxStopListed; Omitted counts the ids left out.
+	Hypotheses []EntryID `json:"hypotheses"`
+	OpenTasks  []EntryID `json:"openTasks"`
+	Proposals  []EntryID `json:"proposals"`
+	Omitted    int       `json:"omitted,omitempty"`
+	// Rounds, WallClock and Tokens are the budget the run used.
+	Rounds    int           `json:"rounds"`
+	WallClock time.Duration `json:"wallClock"`
+	Tokens    int64         `json:"tokens"`
+}
+
+// MaxStopListed caps each list of ids a stop entry holds.
+const MaxStopListed = 50
+
+// StopInput is what the coordinator says of a run it stops; the board adds
+// what it holds.
+type StopInput struct {
+	Reason string
+	// Conclusion is the hypothesis the coordinator recorded as the run's
+	// conclusion when it decided to stop; empty when it recorded none.
+	Conclusion EntryID
+	Rounds     int
+	WallClock  time.Duration
+	Tokens     int64
+}
+
+// PlatformEvidenceInput is evidence the platform posts: a claim it composed
+// by rule, the record behind it and its source (spec Section 4.2).
+type PlatformEvidenceInput struct {
+	Claim  string
+	Ref    string
+	Source Source
+}
+
 // Proposal is a write call the gateway recorded for a person and never sent
 // (spec Sections 4.4, 5.3 and 6.3). The gateway writes it; Proposer names the
 // agent that made the call. Values that came from servers are stored as JSON
 // text, so that storage never rewrites them.
 type Proposal struct {
@@ -294,18 +341,33 @@ type NoticeCode string
 const (
 	// NoticeRefusedCall reports a call the gateway refused.
 	NoticeRefusedCall NoticeCode = "refused_call"
 	// NoticeFailedRead reports a read that ended in a read error.
 	NoticeFailedRead NoticeCode = "failed_read"
+	// NoticeModelRefused, NoticeModelFailed and NoticeModelUnknown report a
+	// model call the LLM gateway refused, one that failed, and one whose
+	// outcome is unknown.
+	NoticeModelRefused NoticeCode = "model_refused"
+	NoticeModelFailed  NoticeCode = "model_failed"
+	NoticeModelUnknown NoticeCode = "model_unknown"
+	// NoticeNoAgents reports a run left with no agent to invite.
+	NoticeNoAgents NoticeCode = "no_agents"
 )
 
+// modelNotice reports whether a notice reports a model call, which it
+// names: each call has its own notice.
+func modelNotice(c NoticeCode) bool {
+	return c == NoticeModelRefused || c == NoticeModelFailed || c == NoticeModelUnknown
+}
+
 // Notice is the platform's report of a failure on the board (spec Sections
 // 4.2 and 8.1). It holds names, ids and codes only, never text an agent or a
-// server wrote: people read it as a platform fact.
+// server wrote: people read it as a platform fact. A no_agents notice names
+// nothing.
 type Notice struct {
 	Code  NoticeCode `json:"code"`
-	Agent string     `json:"agent"`
+	Agent string     `json:"agent,omitempty"`
 	// Tool is the name the agent called, when it is a tool of the agent's
 	// list; empty otherwise.
 	Tool string `json:"tool,omitempty"`
 	// CallID names the call's record in the audit store, if it has one.
 	CallID string `json:"callId,omitempty"`
@@ -324,10 +386,11 @@ type Body struct {
 	Completion *Completion `json:"completion,omitempty"`
 	TurnEnd    *TurnEnd    `json:"turnEnd,omitempty"`
 	Proposal   *Proposal   `json:"proposal,omitempty"`
 	Notice     *Notice     `json:"notice,omitempty"`
 	Round      *RoundStart `json:"round,omitempty"`
+	Stop       *Stop       `json:"stop,omitempty"`
 }
 
 // Kind returns the kind of entry the body holds, or "" when it holds none or
 // more than one.
 func (b *Body) Kind() Kind {
@@ -340,11 +403,11 @@ func (b *Body) Kind() Kind {
 		{b.Evidence != nil, KindEvidence}, {b.Hypothesis != nil, KindHypothesis},
 		{b.Retraction != nil, KindRetraction}, {b.Ruling != nil, KindRuling},
 		{b.Task != nil, KindTask}, {b.Claim != nil, KindClaim},
 		{b.Completion != nil, KindCompletion}, {b.TurnEnd != nil, KindTurnEnd},
 		{b.Proposal != nil, KindProposal}, {b.Notice != nil, KindNotice},
-		{b.Round != nil, KindRound},
+		{b.Round != nil, KindRound}, {b.Stop != nil, KindStop},
 	} {
 		if f.set {
 			kind, n = f.kind, n+1
 		}
 	}
```

Apply this change to `internal/work/board/state.go`:

```diff
diff --git a/internal/work/board/state.go b/internal/work/board/state.go
index 5d45e0f..2a40613 100644
--- a/internal/work/board/state.go
+++ b/internal/work/board/state.go
@@ -25,10 +25,11 @@ type State struct {
 	rejected  map[EntryID]bool    // canonical hypothesis → rejected by a human
 	dismissed map[EntryID]bool    // refuting evidence id → dismissed by a human
 	lastClaim map[EntryID]EntryID // task → its latest claim
 	completed map[EntryID]EntryID // task → the completion that ended it
 	round     int                 // position of the latest round entry in events, or -1
+	stop      int                 // position of the stop entry in events, or -1
 }
 
 // newState returns an empty board. Replay is the only way in from outside the
 // package, so every board has checked settings.
 func newState(settings Settings) *State {
@@ -41,10 +42,11 @@ func newState(settings Settings) *State {
 		rejected:  make(map[EntryID]bool),
 		dismissed: make(map[EntryID]bool),
 		lastClaim: make(map[EntryID]EntryID),
 		completed: make(map[EntryID]EntryID),
 		round:     -1,
+		stop:      -1,
 	}
 }
 
 // Replay folds committed events into a State. It checks the run's convergence
 // settings, and that the events are in commit order, uniquely identified and
@@ -109,15 +111,23 @@ func (s *State) checkNewID(id EntryID) error {
 	}
 	return nil
 }
 
 // check applies the rules that every entry satisfies, both when it is posted
-// and when it is replayed.
+// and when it is replayed. A stopped board takes rulings only.
 func (s *State) check(author Author, b *Body) error {
 	if author.Name == "" {
 		return fmt.Errorf("%w: entry without an author", ErrInvalid)
 	}
+	if s.stop >= 0 && b.Kind() != KindRuling {
+		return fmt.Errorf("%w: its board takes rulings only", ErrStopped)
+	}
+	return s.checkBody(author, b)
+}
+
+// checkBody applies the rules of the entry's kind.
+func (s *State) checkBody(author Author, b *Body) error {
 	switch b.Kind() {
 	case KindEvidence:
 		return s.checkEvidence(author, b.Evidence)
 	case KindHypothesis:
 		return s.checkHypothesis(author, b.Hypothesis)
@@ -137,10 +147,12 @@ func (s *State) check(author Author, b *Body) error {
 		return s.checkProposal(author, b.Proposal)
 	case KindNotice:
 		return checkNotice(author, b.Notice)
 	case KindRound:
 		return s.checkRound(author, b.Round)
+	case KindStop:
+		return s.checkStop(author, b.Stop)
 	default:
 		return fmt.Errorf("%w: an entry holds exactly one body", ErrInvalid)
 	}
 }
 
@@ -244,18 +256,44 @@ func (s *State) checkRound(author Author, r *RoundStart) error {
 		}
 	}
 	return nil
 }
 
+// stopReason is a stop reason's code.
+var stopReason = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)
+
+// checkStop checks a stop entry: the coordinator's, with its reason as a
+// code.
+func (s *State) checkStop(author Author, st *Stop) error {
+	switch {
+	case author.Kind != AuthorPlatform:
+		return fmt.Errorf("%w: only the coordinator stops a run", ErrNotAllowed)
+	case !stopReason.MatchString(st.Reason):
+		return fmt.Errorf("%w: a stop reason is a code of lowercase letters and underscores", ErrInvalid)
+	case st.Conclusion != "":
+		if e, ok := s.event(st.Conclusion); !ok || e.Body.Hypothesis == nil {
+			return fmt.Errorf("%w: the conclusion %s is no hypothesis of the board", ErrInvalid, st.Conclusion)
+		}
+	}
+	return nil
+}
+
 func checkNotice(author Author, n *Notice) error {
 	switch {
 	case author.Kind != AuthorPlatform:
 		return fmt.Errorf("%w: only the platform posts notices", ErrNotAllowed)
-	case n.Code != NoticeRefusedCall && n.Code != NoticeFailedRead:
+	case n.Code == NoticeNoAgents:
+		if *n != (Notice{Code: NoticeNoAgents}) {
+			return fmt.Errorf("%w: a no_agents notice names nothing", ErrInvalid)
+		}
+		return nil
+	case n.Code != NoticeRefusedCall && n.Code != NoticeFailedRead && !modelNotice(n.Code):
 		return fmt.Errorf("%w: unknown notice %q", ErrInvalid, n.Code)
 	case n.Agent == "":
 		return fmt.Errorf("%w: a notice names its agent", ErrInvalid)
+	case modelNotice(n.Code) && n.CallID == "":
+		return fmt.Errorf("%w: a notice of a model call names the call", ErrInvalid)
 	}
 	return nil
 }
 
 func (s *State) checkEvidence(author Author, ev *Evidence) error {
@@ -454,19 +492,25 @@ func (s *State) record(e *Event) {
 		s.lastClaim[b.Claim.Task] = e.ID
 	case b.Completion != nil:
 		s.completed[b.Completion.Task] = e.ID
 	case b.Round != nil:
 		s.round = len(s.events) - 1
+	case b.Stop != nil:
+		s.stop = len(s.events) - 1
 	case b.Ruling != nil:
-		switch b.Ruling.Kind {
-		case RulingReject:
-			s.rejected[s.canonical[b.Ruling.Hypothesis]] = true
-		case RulingUnreject:
-			delete(s.rejected, s.canonical[b.Ruling.Hypothesis])
-		case RulingDismiss:
-			s.dismissed[b.Ruling.Refutation] = true
-		}
+		s.recordRuling(b.Ruling)
+	}
+}
+
+func (s *State) recordRuling(r *Ruling) {
+	switch r.Kind {
+	case RulingReject:
+		s.rejected[s.canonical[r.Hypothesis]] = true
+	case RulingUnreject:
+		delete(s.rejected, s.canonical[r.Hypothesis])
+	case RulingDismiss:
+		s.dismissed[r.Refutation] = true
 	}
 }
 
 func (s *State) event(id EntryID) (*Event, bool) {
 	i, ok := s.index[id]
```

Apply this change to `internal/work/board/round.go`:

```diff
diff --git a/internal/work/board/round.go b/internal/work/board/round.go
index ba2e31f..20aa44c 100644
--- a/internal/work/board/round.go
+++ b/internal/work/board/round.go
@@ -292,10 +292,13 @@ func (s *State) Propose(c *Context, id EntryID, p *Proposal) (Posting, error) {
 func (s *State) proposable(c *Context, p *Proposal) (EntryID, error) {
 	proposer := Author{Kind: AuthorAgent, Name: p.Proposer}
 	if p.Proposer == "" {
 		return "", fmt.Errorf("%w: a proposal names its proposer", ErrInvalid)
 	}
+	if s.stop >= 0 {
+		return "", fmt.Errorf("%w: its board takes rulings only", ErrStopped)
+	}
 	if err := s.admit(c, proposer, KindProposal, p.Hypothesis, ""); err != nil {
 		return "", err
 	}
 	h, err := s.canonicalOf(p.Hypothesis)
 	if err != nil {
@@ -319,23 +322,33 @@ func (s *State) earlierProposal(p *Proposal) *Event {
 			old.ArgsHash == p.ArgsHash && old.Hypothesis == p.Hypothesis
 	})
 }
 
 // Notify checks a platform notice. One notice per round, agent, tool, code
-// and rule is enough, and an agent's calls leave at most MaxNotices in a
+// and rule is enough; an agent's calls leave at most MaxNotices of them in a
 // round: a later one repeats the latest, so calls that loop cannot flood the
-// board.
+// board. A model call has one notice, in any round, which the cap does not
+// count: a turn ends at its first failed model call (Plan 7d), and no
+// notice of the gateway's crowds a model call's out.
 func (s *State) Notify(c *Context, id EntryID, author Author, n *Notice) (Posting, error) {
 	var latest *Event
 	count := 0
 	for i := range s.events {
 		e := &s.events[i]
 		old := e.Body.Notice
-		if old == nil || e.Round != c.Round || old.Agent != n.Agent {
+		switch {
+		case old == nil || modelNotice(old.Code) != modelNotice(n.Code):
 			continue
-		}
-		if old.Code == n.Code && old.Tool == n.Tool && old.Rule == n.Rule {
+		case modelNotice(n.Code):
+			if old.CallID == n.CallID {
+				found := *e
+				return repeated(&found), nil
+			}
+			continue
+		case e.Round != c.Round || old.Agent != n.Agent:
+			continue
+		case old.Code == n.Code && old.Tool == n.Tool && old.Rule == n.Rule:
 			found := *e
 			return repeated(&found), nil
 		}
 		latest, count = e, count+1
 	}
@@ -349,10 +362,69 @@ func (s *State) Notify(c *Context, id EntryID, author Author, n *Notice) (Postin
 		return Posting{}, err
 	}
 	return drafted(&d), nil
 }
 
+// PostPlatformEvidence checks evidence the platform posts. It repeats the
+// author's evidence that names the same record.
+func (s *State) PostPlatformEvidence(c *Context, id EntryID, author Author, in *PlatformEvidenceInput) (Posting, error) {
+	if e := s.earlier(author, func(e *Event) bool {
+		ev := e.Body.Evidence
+		return ev != nil && ev.Provenance.Kind == FromPlatform && ev.Provenance.Ref == in.Ref
+	}); e != nil && in.Ref != "" {
+		return repeated(e), nil
+	}
+	if author.Kind != AuthorPlatform {
+		return Posting{}, fmt.Errorf("%w: only the platform posts platform evidence", ErrNotAllowed)
+	}
+	ev := &Evidence{Claim: strings.TrimSpace(in.Claim), Provenance: Provenance{Kind: FromPlatform, Ref: in.Ref, Source: in.Source}}
+	d, err := s.prepare(c, id, author, &Body{Evidence: ev})
+	if err != nil {
+		return Posting{}, err
+	}
+	return drafted(&d), nil
+}
+
+// Stop checks the coordinator's stop entry, which ends the run's board: it
+// sums up the board as it stands, with the conclusion the coordinator
+// recorded and the budget it says the run used. Stopping the board again
+// repeats its stop entry.
+func (s *State) Stop(c *Context, id EntryID, author Author, in *StopInput) (Posting, error) {
+	if e, stopped := s.Stopped(); stopped && e.Author == author {
+		return repeated(&e), nil
+	}
+	a := s.Assess()
+	st := &Stop{
+		Reason: in.Reason, Conclusion: in.Conclusion, Hypotheses: []EntryID{}, OpenTasks: []EntryID{}, Proposals: []EntryID{},
+		Rounds: in.Rounds, WallClock: in.WallClock, Tokens: in.Tokens,
+	}
+	list := func(to *[]EntryID, id EntryID) {
+		if len(*to) < MaxStopListed {
+			*to = append(*to, id)
+		} else {
+			st.Omitted++
+		}
+	}
+	for i := range a.Hypotheses {
+		list(&st.Hypotheses, a.Hypotheses[i].ID)
+	}
+	for i := range s.events {
+		e := &s.events[i]
+		if _, done := s.completed[e.ID]; e.Body.Task != nil && !done {
+			list(&st.OpenTasks, e.ID)
+		}
+		if e.Body.Proposal != nil {
+			list(&st.Proposals, e.ID)
+		}
+	}
+	d, err := s.prepare(c, id, author, &Body{Stop: st})
+	if err != nil {
+		return Posting{}, err
+	}
+	return drafted(&d), nil
+}
+
 // admit applies the round's rules to an agent's post: nothing after it ended
 // its turn in the round, only what the wrap-up round allows, and no more
 // entries than the round's limit.
 func (s *State) admit(c *Context, author Author, kind Kind, hypothesis EntryID, stance Stance) error {
 	if author.Kind != AuthorAgent {
```

- [ ] **Step 4: A round's report**

Create `internal/work/board/report.go`:

```go
package board

import "sort"

// RoundFacts is what a round produced, as the board tells it.
type RoundFacts struct {
	// Ended holds the agents that ended their turn in the round, sorted.
	Ended []string
	// Entries counts the round's entries of agents and people, turn ends
	// aside, and the proposals the gateway recorded for agents. Notices,
	// round entries, platform evidence and the stop entry are the
	// platform's own, and do not count: a round without entries is quiet as
	// far as the board can tell (spec Section 5.6).
	Entries int
}

// Report is what the coordinator reads of a run's board when one of its
// rounds ends: the board's assessment, what the round produced, the model
// calls the board holds a notice of, and whether the run stopped.
type Report struct {
	Assessment Assessment
	Facts      RoundFacts
	Stopped    bool
	noticed    map[string]bool
}

// Noticed reports whether the board holds a notice of the model call with
// the given id, in any round.
func (r *Report) Noticed(callID string) bool { return r.noticed[callID] }

// Report returns the board's report for round.
func (s *State) Report(round int) Report {
	r := Report{Assessment: s.Assess(), Facts: RoundFacts{Ended: []string{}}, Stopped: s.stop >= 0, noticed: map[string]bool{}}
	for i := range s.events {
		e := &s.events[i]
		if n := e.Body.Notice; n != nil && modelNotice(n.Code) {
			r.noticed[n.CallID] = true
		}
		if e.Round != round {
			continue
		}
		switch {
		case e.Body.TurnEnd != nil:
			r.Facts.Ended = append(r.Facts.Ended, e.Author.Name)
		case e.Author.IsContributor(), e.Body.Proposal != nil:
			r.Facts.Entries++
		}
	}
	sort.Strings(r.Facts.Ended)
	return r
}

// Stopped returns the board's stop entry, if the run stopped.
func (s *State) Stopped() (Event, bool) {
	if s.stop < 0 {
		return Event{}, false
	}
	return s.events[s.stop], true
}
```

- [ ] **Step 5: The use cases, and the gateway's refusal**

Apply this change to `internal/work/app/boards.go`:

```diff
diff --git a/internal/work/app/boards.go b/internal/work/app/boards.go
index 02c0d17..2619900 100644
--- a/internal/work/app/boards.go
+++ b/internal/work/app/boards.go
@@ -156,10 +156,36 @@ func (b *Boards) StartRound(ctx context.Context, run board.RunID, in *board.Roun
 	return b.post(ctx, run, 0, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
 		return s.StartRound(bc, id, board.Coordinator, in)
 	})
 }
 
+// PostPlatformEvidence commits evidence the coordinator posts, once per
+// record it names: posting it again returns the first entry.
+func (b *Boards) PostPlatformEvidence(ctx context.Context, run board.RunID, in *board.PlatformEvidenceInput) (board.Posted, error) {
+	return b.post(ctx, run, 0, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
+		return s.PostPlatformEvidence(bc, id, board.Coordinator, in)
+	})
+}
+
+// Stop commits the coordinator's stop entry, which ends the run's board:
+// from then on it takes rulings only. Stopping it again returns the entry.
+func (b *Boards) Stop(ctx context.Context, run board.RunID, in *board.StopInput) (board.Posted, error) {
+	return b.post(ctx, run, 0, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
+		return s.Stop(bc, id, board.Coordinator, in)
+	})
+}
+
+// Report returns what the coordinator reads of a run's board when round
+// ends.
+func (b *Boards) Report(ctx context.Context, run board.RunID, round int) (board.Report, error) {
+	_, s, err := b.state(ctx, run)
+	if err != nil {
+		return board.Report{}, err
+	}
+	return s.Report(round), nil
+}
+
 // View returns what an agent sees of its run's board in the current round.
 func (b *Boards) View(ctx context.Context, run board.RunID, agent string, since int64) (board.View, error) {
 	bc, s, err := b.state(ctx, run)
 	if err != nil {
 		return board.View{}, err
```

Apply this change to `internal/actions/app/board.go`:

```diff
diff --git a/internal/actions/app/board.go b/internal/actions/app/board.go
index 20dd7b6..7fa45c4 100644
--- a/internal/actions/app/board.go
+++ b/internal/actions/app/board.go
@@ -376,11 +376,11 @@ func ruleOf(err error) string {
 	for _, r := range []struct {
 		err  error
 		name string
 	}{
 		{board.ErrLimit, "limit"}, {board.ErrTurnEnded, "turn_ended"}, {board.ErrWrapUp, "wrap_up"}, {board.ErrHeld, "held"},
-		{board.ErrRoundEnded, "round_ended"}, {board.ErrProvenance, "provenance"}, {board.ErrNotFound, "not_found"},
+		{board.ErrRoundEnded, "round_ended"}, {board.ErrStopped, "stopped"}, {board.ErrProvenance, "provenance"}, {board.ErrNotFound, "not_found"},
 		{board.ErrNotAllowed, "not_allowed"}, {board.ErrReasonRequired, "invalid"}, {board.ErrInvalid, "invalid"},
 	} {
 		if errors.Is(err, r.err) {
 			return r.name
 		}
```

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test ./internal/work/... ./internal/actions/... && golangci-lint run ./...`
Expected: every package `ok`; `0 issues.`

- [ ] **Step 7: Commit**

```bash
git add internal/work/board internal/work/app internal/actions/app
git commit -m "feat(work): a stop entry that ends a run's board, platform evidence posted once, notices of model calls, and a round's report"
```

### Task 3: Work items: records, state and the round's end

Decisions 5 to 8. A work item's records, the state they fold into, and the pure rules of a round's end. Nothing here reads a store or a clock: the coordinator (Task 7) gives each function what it read.

**Files:**
- Create: `internal/work/item/item.go`, `internal/work/item/state.go`, `internal/work/item/decide.go`
- Create: `internal/work/item/fixture_test.go`, `internal/work/item/decide_test.go`, `internal/work/item/state_test.go`

**Interfaces:**
- Consumes: `board.RunID`, `board.EntryID`, `board.Settings`, `board.Report` (Task 2).
- Produces:
  - `item.ID`, `item.IncidentID`, `item.Version = 1`, `item.ErrInvalid`, `item.ErrVersion`;
  - `item.Record{Seq int64; ID string; Item ID; Incident IncidentID; Run board.RunID; At time.Time; Body Body}`, `item.Draft{ID string; Item ID; Incident IncidentID; Run board.RunID; Body Body}`;
  - `item.Body{V int; Opened; Started; RunCreated; RoundOpened; RoundEnded; RunStopped; AwaitingClose}` with `(*Body).Kind() Kind`, and the kinds `KindOpened` (`item_opened`), `KindStarted`, `KindRunCreated`, `KindRoundOpened`, `KindRoundEnded`, `KindRunStopped`, `KindAwaitingClose`;
  - the bodies `Opened`, `Started`, `RunCreated` (with `Budget`), `RoundOpened`, `RoundEnded` (with `Decision`), `RunStopped`, `AwaitingClose`, the constants `ArmVisible`, `NoWindow`, `ClassPrimary`, `ModeVisible`, and the enums `EndedBy`, `TurnClass` (with `TurnUnstarted`, the coordinator's own class), `StopReason`, `Next`;
  - `item.ItemReason(StopReason) StopReason`;
  - `item.State{ID; Incident; Opened; Started; Run *Run; Closing *AwaitingClose}` with `Fold([]Record) (*State, error)`, `(*State).Apply(*Record) error`, `(*State).Status() Status`; the fold refuses a run id with a colon, an item's close with a reason that is not its run's, and a converged stop whose conclusion is not its wrap-up round's hypothesis;
  - `item.Run{ID; Created; Rounds []Round; Stopped}` with `Latest() *Round`, `Agents() []string`, `End(*Ending) RoundEnded`, `Open(now time.Time) RoundOpened`, `Stop(conclusion board.EntryID) RunStopped`, whose conclusion is the wrap-up round's hypothesis for a converged run;
  - `item.Ending{EndedBy; EndedAt; Turns; Spent; Settled; DaySpent; DailyCap; Report board.Report; TriggerResolved; Unseen bool}`, whose `Unseen`, kept in `RoundEnded.Unseen`, marks a round the instance ending it did not act on: never quiet, and a wrap-up round that concludes nothing; `item.MaxWrapUps = 2`, `item.MaxCrashes = 2`.

- [ ] **Step 1: Write the failing tests**

Create `internal/work/item/fixture_test.go`, which drives an item through the package's own rules, record by record:

```go
package item_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

var t0 = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

const (
	release = "release-investigator"
	runtime = "runtime-investigator"
	hyp     = board.EntryID("hyp-1")
	other   = board.EntryID("hyp-2")
)

// created is a run's creation as the coordinator records it: 3 rounds of at
// most 2 minutes, 10 minutes of wall clock and 90,000 tokens, two agents.
func created() *item.RunCreated {
	return &item.RunCreated{
		Class: item.ClassPrimary, Mode: item.ModeVisible, Settings: board.Settings{Convergence: board.DefaultConvergence},
		Budget:     item.Budget{WallClock: 10 * time.Minute, Tokens: 90_000, Rounds: 3},
		RoundLimit: 2 * time.Minute, Agents: []string{release, runtime},
	}
}

// life drives an item through the package's own rules, record by record, as
// the coordinator does, and keeps the records it wrote.
type life struct {
	t       *testing.T
	s       *item.State
	records []item.Record
	now     time.Time
}

func newLife(t *testing.T, rc *item.RunCreated) *life {
	t.Helper()
	l := &life{t: t, s: &item.State{}, now: t0}
	l.write("", item.Body{Opened: &item.Opened{Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow}})
	l.write("", item.Body{Started: &item.Started{Commit: "c1", Digest: "d1", Images: map[string]string{"thinloop": "sha256:aa"}}})
	if rc != nil {
		l.write("run-1", item.Body{RunCreated: rc})
	}
	return l
}

// write commits one record of the item, as the store does: the next
// sequence number and a later time.
func (l *life) write(run board.RunID, body item.Body) item.Record {
	l.t.Helper()
	body.V = item.Version
	r := item.Record{Seq: int64(len(l.records) + 1), ID: fmt.Sprintf("rec-%d", len(l.records)+1), Item: "item-1", Incident: "inc-1", Run: run, At: l.now, Body: body}
	require.NoError(l.t, l.s.Apply(&r))
	l.records = append(l.records, r)
	return r
}

// try applies one more record of the item, and returns its refusal; a record
// applied is kept.
func (l *life) try(run board.RunID, body item.Body) error {
	body.V = item.Version
	r := item.Record{Seq: int64(len(l.records) + 1), ID: fmt.Sprintf("rec-%d", len(l.records)+1), Item: "item-1", Incident: "inc-1", Run: run, At: l.now, Body: body}
	if err := l.s.Apply(&r); err != nil {
		return err
	}
	l.records = append(l.records, r)
	return nil
}

// open opens the run's next round as decided, at the life's time.
func (l *life) open() item.RoundOpened {
	l.t.Helper()
	o := l.s.Run.Open(l.now)
	l.write("run-1", item.Body{RoundOpened: &o})
	return o
}

// end ends the open round after d, as e says, and records the decision.
func (l *life) end(d time.Duration, e *item.Ending) item.RoundEnded {
	l.t.Helper()
	l.now = l.now.Add(d)
	if e.EndedAt.IsZero() {
		e.EndedAt = l.now
	}
	re := l.s.Run.End(e)
	l.write("run-1", item.Body{RoundEnded: &re})
	return re
}

// all gives each of the run's agents in the round the same turn class.
func all(class item.TurnClass, agents ...string) map[string]item.TurnClass {
	turns := map[string]item.TurnClass{}
	for _, a := range agents {
		turns[a] = class
	}
	return turns
}

// busy is a round that ends by its turns, with entries on the board and no
// hypothesis converged.
func busy() *item.Ending {
	return &item.Ending{EndedBy: item.EndedByTurns, Turns: all(item.TurnEnded, release, runtime), DailyCap: 1_000_000, Settled: true,
		Report: board.Report{Facts: board.RoundFacts{Ended: []string{release, runtime}, Entries: 3}}}
}

// quiet is a round that ends by its turns with nothing on the board.
func quiet() *item.Ending {
	e := busy()
	e.Report.Facts.Entries = 0
	return e
}

// converging is busy with h converged.
func converging(h ...board.EntryID) *item.Ending {
	e := busy()
	for _, id := range h {
		e.Report.Assessment.Hypotheses = append(e.Report.Assessment.Hypotheses, board.HypothesisView{ID: id, Converged: true})
	}
	return e
}
```

Create `internal/work/item/decide_test.go`:

```go
package item_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// A round whose turns all ended with nothing on the board is quiet, and the
// run stops quiet.
func TestARoundWithNothingOnTheBoardStopsTheRunQuiet(t *testing.T) {
	l := newLife(t, created())
	l.open()

	ended := l.end(time.Minute, quiet())

	assert.True(t, ended.Quiet)
	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopQuiet}, ended.Decision)
}

// A round is never quiet when it left an entry, when a turn failed or was
// refused, or when no instance held the lease at its deadline.
func TestARoundWithEntriesOrTroubleIsNeverQuiet(t *testing.T) {
	for name, change := range map[string]func(*item.Ending){
		"an entry":         func(e *item.Ending) { e.Report.Facts.Entries = 1 },
		"a crash":          func(e *item.Ending) { e.Turns[runtime] = item.TurnCrashed },
		"a refused call":   func(e *item.Ending) { e.Turns[runtime] = item.TurnRefused },
		"a vendor failure": func(e *item.Ending) { e.Turns = all(item.TurnVendor, release, runtime) },
		"an outage":        func(e *item.Ending) { e.EndedBy, e.Turns = item.EndedByOutage, nil },
	} {
		l := newLife(t, created())
		l.open()
		e := quiet()
		change(e)

		ended := l.end(time.Minute, e)

		assert.False(t, ended.Quiet, name)
		assert.Equal(t, item.Decision{Next: item.NextRound}, ended.Decision, name)
	}
	l := newLife(t, created())
	l.open()
	e := quiet()
	e.Turns = map[string]item.TurnClass{release: item.TurnFinished, runtime: item.TurnInterrupted}
	assert.True(t, l.end(time.Minute, e).Quiet, "turns that finished without end_turn, or were interrupted, are quiet")
}

// A converged hypothesis gets one more round, the wrap-up round, in which it
// may be refuted; if it is still converged after it, the run stops
// converged (spec Section 5.6).
func TestAConvergedHypothesisGetsAWrapUpRoundThenTheRunStops(t *testing.T) {
	l := newLife(t, created())
	l.open()

	first := l.end(time.Minute, converging(hyp, other))
	wrapUp := l.open()
	last := l.end(time.Minute, converging(hyp))

	assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: hyp}, first.Decision, "the top hypothesis")
	assert.Equal(t, hyp, wrapUp.WrapUp)
	assert.Equal(t, 2, wrapUp.Round)
	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopConverged}, last.Decision)
	assert.Equal(t, 1, last.Rounds, "the wrap-up round counts toward no budget")
	assert.Equal(t, time.Minute, last.WallClock)
}

// A refutation in the wrap-up round revokes the convergence: normal rounds
// resume while the budget lasts. A run has at most two wrap-up rounds.
func TestARevokedConvergenceResumesNormalRoundsAndWrapUpsAreCapped(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 6
	l := newLife(t, rc)
	l.open()
	l.end(time.Minute, converging(hyp))
	l.open()

	revoked := l.end(time.Minute, busy())
	l.open()
	again := l.end(time.Minute, converging(hyp))
	l.open()
	l.end(time.Minute, busy())
	l.open()
	capped := l.end(time.Minute, converging(hyp))

	assert.Equal(t, item.Decision{Next: item.NextRound}, revoked.Decision)
	assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: hyp}, again.Decision)
	assert.Equal(t, item.Decision{Next: item.NextRound}, capped.Decision, "two wrap-ups were had")
}

// The wrap-up round follows a convergence even when a budget is spent, and
// its own result decides; a revoked convergence then stops the run on its
// budget.
func TestTheWrapUpRoundFollowsAnExhaustedBudget(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 1
	l := newLife(t, rc)
	l.open()

	converged := l.end(time.Minute, converging(hyp))
	l.open()
	revoked := l.end(time.Minute, busy())

	assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: hyp}, converged.Decision)
	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopBudgetExhausted}, revoked.Decision)
}

// The precedence of a round's end: the wrap-up round's result, the daily
// cap, a resolved trigger, no agents left, a convergence, the budget, then
// a quiet round.
func TestTheRoundEndsDecisionsInOrder(t *testing.T) {
	cut := func(e *item.Ending) *item.Ending { e.DaySpent = e.DailyCap; return e }
	resolved := func(e *item.Ending) *item.Ending { e.TriggerResolved = true; return e }

	l := newLife(t, created())
	l.open()
	l.end(time.Minute, converging(hyp))
	l.open()
	assert.Equal(t, item.StopConverged, l.end(time.Minute, cut(converging(hyp))).Decision.Stop, "the wrap-up's result first")

	l = newLife(t, created())
	l.open()
	assert.Equal(t, item.StopCut, l.end(time.Minute, resolved(cut(converging(hyp)))).Decision.Stop, "the cap before a resolved trigger")

	l = newLife(t, created())
	l.open()
	assert.Equal(t, item.StopTriggerResolved, l.end(time.Minute, resolved(converging(hyp))).Decision.Stop, "a resolved trigger before a convergence")

	l = newLife(t, created())
	l.open()
	crashed := busy()
	crashed.Turns = all(item.TurnCrashed, release, runtime)
	l.end(time.Minute, crashed)
	l.open()
	crashedConverging := converging(hyp)
	crashedConverging.Turns = all(item.TurnCrashed, release, runtime)
	gone := l.end(time.Minute, crashedConverging)
	assert.Equal(t, item.StopNotStaffed, gone.Decision.Stop, "no agents left before a convergence")
	assert.Equal(t, []string{release, runtime}, gone.Removed)

	rc := created()
	rc.Budget.Tokens = 100
	l = newLife(t, rc)
	l.open()
	spent := quiet()
	spent.Spent = 100
	assert.Equal(t, item.StopBudgetExhausted, l.end(time.Minute, spent).Decision.Stop, "the budget before a quiet round")
}

// A run stops when it used its budget: its rounds, its wall clock, so much
// of it that less than a third of a round's limit is left, or its tokens.
func TestARunStopsWhenItsBudgetIsUsed(t *testing.T) {
	for name, c := range map[string]struct {
		rounds int
		after  []time.Duration
		spent  int64
	}{
		"rounds":            {rounds: 2, after: []time.Duration{time.Second, time.Second}},
		"wall clock":        {rounds: 9, after: []time.Duration{2 * time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute}},
		"a third of round":  {rounds: 9, after: []time.Duration{2 * time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute, 81 * time.Second}},
		"tokens":            {rounds: 9, after: []time.Duration{time.Second}, spent: 90_000},
		"tokens, past them": {rounds: 9, after: []time.Duration{time.Second}, spent: 95_000},
	} {
		rc := created()
		rc.Budget.Rounds = c.rounds
		l := newLife(t, rc)
		var last item.RoundEnded
		for i, d := range c.after {
			if i > 0 {
				require.Equal(t, item.NextRound, last.Decision.Next, name)
			}
			l.open()
			e := busy()
			e.Spent = c.spent
			last = l.end(d, e)
		}

		assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopBudgetExhausted}, last.Decision, name)
	}
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	for range 4 {
		l.open()
		l.end(2*time.Minute, busy())
	}
	l.open()
	assert.Equal(t, item.NextRound, l.end(79*time.Second, busy()).Decision.Next, "41 s left of a 2-minute round's limit is more than a third")
}

// A round that ended by an outage counts toward neither the rounds nor the
// wall clock, and its turns are not counted.
func TestAnOutageRoundCountsTowardNoBudget(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 1
	l := newLife(t, rc)
	l.open()

	outage := l.end(2*time.Minute, &item.Ending{EndedBy: item.EndedByOutage, DailyCap: 1_000_000})
	l.open()
	normal := l.end(time.Minute, busy())

	assert.Equal(t, 0, outage.Rounds)
	assert.Zero(t, outage.WallClock)
	assert.Empty(t, outage.Turns)
	assert.Equal(t, item.Decision{Next: item.NextRound}, outage.Decision)
	assert.Equal(t, 1, normal.Rounds)
	assert.Equal(t, time.Minute, normal.WallClock)
}

// A wrap-up round counts as it ends: one that revoked its hypothesis while
// another converged uses up a wrap-up itself, so that the run's second
// wrap-up round is its last.
func TestAWrapUpRoundCountsAsItEnds(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	l.open()
	l.end(time.Minute, converging(hyp))
	l.open()
	first := l.end(time.Minute, converging(other))
	l.open()

	second := l.end(time.Minute, converging(hyp))

	assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: other}, first.Decision, "the first wrap-up round counts one")
	assert.Equal(t, item.Decision{Next: item.NextRound}, second.Decision, "the second counts two as it ends")
}

// A wrap-up round that ended by an outage revokes nothing, so it uses up no
// wrap-up, neither when its own end is decided nor later: a hypothesis still
// converged gets another. Here the first wrap-up round revoked the
// convergence, and the second is lost to an outage.
func TestAWrapUpRoundLostToAnOutageUsesUpNoWrapUp(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	lost := converging(hyp)
	lost.EndedBy, lost.Turns = item.EndedByOutage, nil
	l.open()
	l.end(time.Minute, converging(hyp))
	l.open()
	require.Equal(t, item.Decision{Next: item.NextRound}, l.end(time.Minute, busy()).Decision, "the first wrap-up revoked it")
	l.open()
	l.end(time.Minute, converging(hyp))
	l.open()

	ended := l.end(2*time.Minute, lost)

	assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: hyp}, ended.Decision)
	l.open()
	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopConverged}, l.end(time.Minute, converging(hyp)).Decision)
}

// A round the instance that ended it did not see, as one a takeover found
// past its deadline, is never quiet: its turns are what the board and the
// deadline say, which cannot tell a round no agent ran from one that found
// nothing.
func TestARoundTheCoordinatorDidNotSeeIsNeverQuiet(t *testing.T) {
	l := newLife(t, created())
	l.open()
	unseen := quiet()
	unseen.EndedBy, unseen.Turns, unseen.Report.Facts.Ended, unseen.Unseen = item.EndedByDeadline, all(item.TurnInterrupted, release, runtime), nil, true

	ended := l.end(2*time.Minute, unseen)

	assert.True(t, ended.Unseen)
	assert.False(t, ended.Quiet)
	assert.Equal(t, item.Decision{Next: item.NextRound}, ended.Decision)
	assert.Equal(t, 1, ended.Rounds, "it counts toward the budget")
}

// A wrap-up round in which no agent took part as the coordinator saw it,
// because no turn started, or because the instance that ended it did not see
// it, concludes nothing and uses up no wrap-up: a hypothesis still converged
// gets another, as after an outage.
func TestAWrapUpRoundNoAgentTookPartInConcludesNothing(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	unstarted := converging(hyp)
	unstarted.EndedBy, unstarted.Turns = item.EndedByDeadline, all(item.TurnUnstarted, release, runtime)
	unseen := converging(hyp)
	unseen.EndedBy, unseen.Turns, unseen.Unseen = item.EndedByDeadline, all(item.TurnInterrupted, release, runtime), true
	l.open()
	l.end(time.Minute, converging(hyp))

	for _, e := range []*item.Ending{unstarted, unseen} {
		l.open()
		ended := l.end(2*time.Minute, e)
		require.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: hyp}, ended.Decision)
	}
	l.open()

	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopConverged}, l.end(time.Minute, converging(hyp)).Decision)
}

// A turn that never started, as when the runner could not be reached all
// round, is neither quiet nor a crash: it leaves the agent's count of
// crashes as it was, as a round that ended by an outage does.
func TestATurnThatNeverStartedIsNeitherQuietNorACrash(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	crash := func() *item.Ending {
		e := busy()
		e.Turns[runtime] = item.TurnCrashed
		return e
	}
	l.open()
	l.end(time.Second, crash())
	l.open()
	unstarted := quiet()
	unstarted.EndedBy, unstarted.Turns, unstarted.Report.Facts.Ended = item.EndedByDeadline, all(item.TurnUnstarted, release, runtime), nil
	never := l.end(2*time.Minute, unstarted)
	l.open()

	second := l.end(time.Second, crash())

	assert.False(t, never.Quiet)
	assert.Empty(t, never.Removed)
	assert.Equal(t, item.Decision{Next: item.NextRound}, never.Decision)
	assert.Equal(t, []string{runtime}, second.Removed)
}

// Two consecutive crashed turns remove an agent after the round; any other
// turn in between resets its count, and a round without counted turns
// leaves it as it was.
func TestTwoConsecutiveCrashesRemoveAnAgent(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 9
	l := newLife(t, rc)
	crash := func() *item.Ending {
		e := busy()
		e.Turns[runtime] = item.TurnCrashed
		return e
	}
	l.open()
	first := l.end(time.Second, crash())
	l.open()
	l.end(time.Second, busy())
	l.open()
	l.end(time.Second, crash())
	l.open()
	l.end(time.Second, &item.Ending{EndedBy: item.EndedByOutage, DailyCap: 1_000_000})
	l.open()

	second := l.end(time.Second, crash())
	next := l.open()

	assert.Empty(t, first.Removed)
	assert.Equal(t, []string{runtime}, second.Removed)
	assert.Equal(t, []string{release}, next.Agents)
	refolded, err := item.Fold(l.records)
	require.NoError(t, err)
	assert.Equal(t, []string{release}, refolded.Run.Agents(), "a restart keeps the count: it is in the records")
}

// A turn the coordinator never heard from counts as interrupted.
func TestATurnNeverHeardFromCountsAsInterrupted(t *testing.T) {
	l := newLife(t, created())
	l.open()
	e := quiet()
	delete(e.Turns, runtime)

	ended := l.end(time.Minute, e)

	assert.Equal(t, item.TurnInterrupted, ended.Turns[runtime])
	assert.True(t, ended.Quiet)
}

// A normal round's deadline is its limit, cut to the wall clock left; a
// wrap-up round has its whole limit, and a token allowance of one round's
// share of the budget above the spend at its opening.
func TestTheNextRoundsDeadlineAndBudget(t *testing.T) {
	rc := created()
	rc.Budget.WallClock = 5 * time.Minute
	rc.Budget.Rounds = 9
	l := newLife(t, rc)

	first := l.open()
	l.end(2*time.Minute, busy())
	l.open()
	l.end(2*time.Minute, busy())
	short := l.open()
	e := converging(hyp)
	e.Spent = 89_000
	l.end(30*time.Second, e)
	wrapUp := l.open()

	assert.Equal(t, t0.Add(2*time.Minute), first.Deadline)
	assert.Equal(t, int64(90_000), first.Budget)
	assert.Equal(t, l.now.Add(-30*time.Second).Add(time.Minute), short.Deadline, "a minute of wall clock was left")
	assert.Equal(t, l.now.Add(2*time.Minute), wrapUp.Deadline)
	assert.Equal(t, int64(89_000+10_000), wrapUp.Budget, "one round's share, 90,000 over 9 rounds, above the spend")
	assert.Equal(t, l.now, wrapUp.OpenedAt)
}

// A run's stop record carries the budget its last round says it used.
func TestARunsStopRecordCarriesTheBudgetUsed(t *testing.T) {
	l := newLife(t, created())
	l.open()
	e := quiet()
	e.Spent = 500
	l.end(time.Minute, e)

	stopped := l.s.Run.Stop("hyp-9")

	assert.Equal(t, item.RunStopped{Reason: item.StopQuiet, Rounds: 1, WallClock: time.Minute, Spent: 500, Settled: true, Conclusion: "hyp-9"}, stopped)
}

// A converged run's conclusion is the hypothesis of its wrap-up round, which
// stayed converged through it, whatever the board's conclusion is when the
// run stops, as when two hypotheses tie. The fold refuses another.
func TestAConvergedRunConcludesWithItsWrapUpHypothesis(t *testing.T) {
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, converging(hyp, other))
	l.open()
	l.end(time.Minute, converging(other, hyp))

	stopped := l.s.Run.Stop("")

	assert.Equal(t, hyp, stopped.Conclusion)
	stopped.Conclusion = other
	require.ErrorIs(t, l.try("run-1", item.Body{RunStopped: &stopped}), item.ErrInvalid)
}
```

Create `internal/work/item/state_test.go`:

```go
package item_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// An item's records fold into its state, and a record that breaks the
// lifecycle's order is refused: each kind once where it is once, rounds in
// order, each ended once, and nothing after the run stops but the item's
// close.
func TestRecordsFoldInTheLifecyclesOrderOnly(t *testing.T) {
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, quiet())
	stop := l.s.Run.Stop("")
	l.write("run-1", item.Body{RunStopped: &stop})
	l.write("", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopQuiet}})

	s, err := item.Fold(l.records)

	require.NoError(t, err)
	assert.Equal(t, item.StatusAwaitingClose, s.Status())
	assert.Equal(t, item.StopQuiet, s.Closing.Reason)
	assert.Len(t, s.Run.Rounds, 1)
	for name, records := range map[string][]item.Record{
		"no opening":          l.records[1:],
		"started twice":       {l.records[0], l.records[1], l.records[1]},
		"a run before start":  {l.records[0], l.records[2]},
		"a round twice":       {l.records[0], l.records[1], l.records[2], l.records[3], l.records[3]},
		"an end before open":  {l.records[0], l.records[1], l.records[2], l.records[4]},
		"ended twice":         {l.records[0], l.records[1], l.records[2], l.records[3], l.records[4], l.records[4]},
		"a stop before end":   {l.records[0], l.records[1], l.records[2], l.records[3], l.records[5]},
		"closed while open":   {l.records[0], l.records[1], l.records[2], l.records[6]},
		"anything after stop": {l.records[0], l.records[1], l.records[2], l.records[3], l.records[4], l.records[5], l.records[3]},
	} {
		_, err := item.Fold(renumbered(records))

		require.ErrorIs(t, err, item.ErrInvalid, name)
	}
}

// renumbered gives records the sequence numbers a store would.
func renumbered(records []item.Record) []item.Record {
	out := slices.Clone(records)
	for i := range out {
		out[i].Seq = int64(i + 1)
	}
	return out
}

// A record of another version is refused: an older binary never acts on a
// record it cannot read.
func TestARecordOfAnotherVersionIsRefused(t *testing.T) {
	l := newLife(t, nil)
	r := l.records[0]
	r.Body.V = item.Version + 1

	_, err := item.Fold([]item.Record{r})

	require.ErrorIs(t, err, item.ErrVersion)
}

// Records fold in commit order: a record out of order, or of another item,
// is refused.
func TestRecordsOfOneItemFoldInCommitOrder(t *testing.T) {
	l := newLife(t, created())
	stray := l.records[1]
	stray.Item = "item-2"
	late := l.records[1]
	late.Seq = 0

	_, err := item.Fold([]item.Record{l.records[0], stray})
	require.ErrorIs(t, err, item.ErrInvalid)
	_, err = item.Fold([]item.Record{l.records[0], late})
	require.ErrorIs(t, err, item.ErrInvalid)
}

// An item opened while the day's spend has reached the cap waits for a
// person to close it, not staffed, without a run.
func TestAnItemNotStaffedAwaitsCloseWithoutARun(t *testing.T) {
	l := newLife(t, nil)
	l.records = l.records[:1]
	l.s, _ = item.Fold(l.records)

	l.write("", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}})

	assert.Equal(t, item.StatusAwaitingClose, l.s.Status())
	assert.Nil(t, l.s.Run)
}

// An item awaits its close with its reason: its run's, as ItemReason gives
// it, or not staffed when it has no run.
func TestAnItemAwaitsItsCloseWithItsReason(t *testing.T) {
	closing := func(reason item.StopReason) item.Body {
		return item.Body{AwaitingClose: &item.AwaitingClose{Reason: reason}}
	}
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, quiet())
	stop := l.s.Run.Stop("")
	l.write("run-1", item.Body{RunStopped: &stop})
	require.ErrorIs(t, l.try("", closing(item.StopConverged)), item.ErrInvalid)
	require.NoError(t, l.try("", closing(item.StopQuiet)))

	cut := newLife(t, created())
	cut.open()
	e := busy()
	e.DaySpent, e.DailyCap = 10, 10
	cut.end(time.Minute, e)
	stop = cut.s.Run.Stop("")
	cut.write("run-1", item.Body{RunStopped: &stop})
	require.ErrorIs(t, cut.try("", closing(item.StopCut)), item.ErrInvalid)
	require.NoError(t, cut.try("", closing(item.StopNotStaffed)))

	unstaffed := newLife(t, nil)
	unstaffed.records = unstaffed.records[:1]
	unstaffed.s, _ = item.Fold(unstaffed.records)
	require.ErrorIs(t, unstaffed.try("", closing(item.StopQuiet)), item.ErrInvalid)
	require.NoError(t, unstaffed.try("", closing(item.StopNotStaffed)))
}

// A run's id holds no colon: a record's scope in the audit chain is its run,
// or incident:<id> for the records of no run, and a run id could pass for
// an incident's scope.
func TestARunsIDHoldsNoColon(t *testing.T) {
	l := newLife(t, nil)

	require.ErrorIs(t, l.try("incident:inc-1", item.Body{RunCreated: created()}), item.ErrInvalid)
	require.NoError(t, l.try("run-1", item.Body{RunCreated: created()}))
}

// A run with no agent to invite stops not staffed, with no round.
func TestARunWithNoAgentsStopsWithoutARound(t *testing.T) {
	rc := created()
	rc.Agents = []string{}
	l := newLife(t, rc)

	stop := item.RunStopped{Reason: item.StopNotStaffed}
	l.write("run-1", item.Body{RunStopped: &stop})

	assert.Equal(t, item.StopNotStaffed, l.s.Run.Stopped.Reason)
	assert.Equal(t, item.StatusInvestigating, l.s.Status())
}

// A run cut at the daily cap leaves its item not staffed.
func TestACutRunLeavesItsItemNotStaffed(t *testing.T) {
	assert.Equal(t, item.StopNotStaffed, item.ItemReason(item.StopCut))
	assert.Equal(t, item.StopQuiet, item.ItemReason(item.StopQuiet))
}

// A record's body names its kind once, and its JSON keeps its version.
func TestARecordsBodyHoldsOneKindAndItsVersion(t *testing.T) {
	body := item.Body{V: item.Version, AwaitingClose: &item.AwaitingClose{Reason: item.StopQuiet}}

	raw, err := json.Marshal(body)

	require.NoError(t, err)
	assert.JSONEq(t, `{"v": 1, "awaitingClose": {"reason": "quiet"}}`, string(raw))
	assert.Equal(t, item.KindAwaitingClose, body.Kind())
	assert.Equal(t, item.Kind(""), (&item.Body{}).Kind())
	two := body
	two.Opened = &item.Opened{}
	assert.Equal(t, item.Kind(""), two.Kind())
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/work/item/`
Expected: FAIL: the build of `internal/work/item` fails (the package has no code yet)

- [ ] **Step 3: The records**

Create `internal/work/item/item.go`:

```go
// Package item holds a work item's lifecycle as the coordinator keeps it: the
// records it writes before it acts, the state they fold into, and the rules
// that decide how a round ends and a run stops (spec Sections 4.1, 5.2 and
// 5.6).
package item

import (
	"errors"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Errors returned by the record rules. Callers match them with errors.Is.
var (
	// ErrInvalid marks a record that breaks the lifecycle's order.
	ErrInvalid = errors.New("item: invalid record")
	// ErrVersion marks a record of a version this code does not read: an
	// older binary never acts on records it cannot read.
	ErrVersion = errors.New("item: unknown record version")
)

// Version is the version of the record bodies this code writes and reads.
const Version = 1

// ID identifies a work item, and IncidentID its incident.
type (
	ID         string
	IncidentID string
)

// Kind is a record's type.
type Kind string

// Record kinds.
const (
	KindOpened        Kind = "item_opened"
	KindStarted       Kind = "item_started"
	KindRunCreated    Kind = "run_created"
	KindRoundOpened   Kind = "round_opened"
	KindRoundEnded    Kind = "round_ended"
	KindRunStopped    Kind = "run_stopped"
	KindAwaitingClose Kind = "item_awaiting_close"
)

// Record is one committed work record. Seq orders a tenant's records, and At
// is the database time of the commit.
type Record struct {
	Seq      int64
	ID       string
	Item     ID
	Incident IncidentID
	// Run is the run the record is about, or empty.
	Run  board.RunID
	At   time.Time
	Body Body
}

// Draft is a record that passed the rules, not committed yet. The store
// assigns its Seq and At.
type Draft struct {
	ID       string
	Item     ID
	Incident IncidentID
	Run      board.RunID
	Body     Body
}

// Body holds a record's content: its version and exactly one kind.
type Body struct {
	V             int            `json:"v"`
	Opened        *Opened        `json:"opened,omitempty"`
	Started       *Started       `json:"started,omitempty"`
	RunCreated    *RunCreated    `json:"runCreated,omitempty"`
	RoundOpened   *RoundOpened   `json:"roundOpened,omitempty"`
	RoundEnded    *RoundEnded    `json:"roundEnded,omitempty"`
	RunStopped    *RunStopped    `json:"runStopped,omitempty"`
	AwaitingClose *AwaitingClose `json:"awaitingClose,omitempty"`
}

// Kind returns the kind of record the body holds, or "" when it holds none
// or more than one.
func (b *Body) Kind() Kind {
	var kind Kind
	n := 0
	for _, f := range []struct {
		set  bool
		kind Kind
	}{
		{b.Opened != nil, KindOpened}, {b.Started != nil, KindStarted}, {b.RunCreated != nil, KindRunCreated},
		{b.RoundOpened != nil, KindRoundOpened}, {b.RoundEnded != nil, KindRoundEnded},
		{b.RunStopped != nil, KindRunStopped}, {b.AwaitingClose != nil, KindAwaitingClose},
	} {
		if f.set {
			kind, n = f.kind, n+1
		}
	}
	if n != 1 {
		return ""
	}
	return kind
}

// Arms and how an item was assigned one (spec Section 13.1). Until the
// measurement window exists (Plan 8), every item is visible, with no window.
const (
	ArmVisible = "visible"
	NoWindow   = "no_window"
)

// Opened opens a work item (spec Section 4.1). Its title is redacted before
// it is recorded, and Routing holds trusted values only.
type Opened struct {
	// Type is the item's work item type.
	Type  string `json:"type"`
	Title string `json:"title"`
	// Source says who opened the item: "person", or an inbound source.
	Source     string            `json:"source"`
	Routing    map[string]string `json:"routing,omitempty"`
	Principal  string            `json:"principal,omitempty"`
	Responders []string          `json:"responders,omitempty"`
	Arm        string            `json:"arm"`
	How        string            `json:"how"`
}

// Started locks the configuration an item runs under (spec Section 3.4): the
// revision, and the runtime images its agents start from.
type Started struct {
	Commit string            `json:"commit"`
	Digest string            `json:"digest"`
	Images map[string]string `json:"images"`
}

// Classes and modes of runs. Until Plan 8 an item has one run, the primary,
// which people see.
const (
	ClassPrimary = "primary"
	ModeVisible  = "visible"
)

// Budget is a run's budget (spec Section 5.6).
type Budget struct {
	WallClock time.Duration `json:"wallClock"`
	// Tokens counts cost-weighted tokens, as the LLM gateway prices them.
	Tokens int64 `json:"tokens"`
	Rounds int   `json:"rounds"`
}

// RunCreated creates a run with its settings, budget and invited agents.
type RunCreated struct {
	Class      string         `json:"class"`
	Mode       string         `json:"mode"`
	Settings   board.Settings `json:"settings"`
	Budget     Budget         `json:"budget"`
	RoundLimit time.Duration  `json:"roundLimit"`
	// Agents are the agents invited, in the revision's order.
	Agents []string `json:"agents"`
}

// RoundOpened opens a round. The coordinator writes it before the round's
// board entry, and repeats what follows it after a crash.
type RoundOpened struct {
	Round int `json:"round"`
	// WrapUp names the converged hypothesis in a wrap-up round.
	WrapUp board.EntryID `json:"wrapUp,omitempty"`
	// OpenedAt and Deadline are in database time.
	OpenedAt time.Time `json:"openedAt"`
	Deadline time.Time `json:"deadline"`
	// Budget is the run's token budget as the LLM gateway holds its sessions
	// to it in the round: the run's, or in a wrap-up round its allowance.
	Budget int64 `json:"budget"`
	// Agents are the agents the round invites.
	Agents []string `json:"agents"`
}

// EndedBy says how a round ended.
type EndedBy string

// How rounds end.
const (
	// EndedByTurns: every turn ended.
	EndedByTurns EndedBy = "turns"
	// EndedByDeadline: the round's deadline passed.
	EndedByDeadline EndedBy = "deadline"
	// EndedByOutage: the deadline passed while no instance held the lease.
	// No turn is counted, and the round counts toward no budget.
	EndedByOutage EndedBy = "outage"
)

// TurnClass says how an agent's turn ended.
type TurnClass string

// Turn classes.
const (
	// TurnEnded: the agent called end_turn.
	TurnEnded TurnClass = "ended"
	// TurnFinished: the runtime ended without end_turn: no tool called, a
	// step or token limit, a reply cut short.
	TurnFinished TurnClass = "finished"
	// TurnInterrupted: the deadline or a stop ended the turn.
	TurnInterrupted TurnClass = "interrupted"
	// TurnRefused: the LLM gateway refused a model call (4xx).
	TurnRefused TurnClass = "refused"
	// TurnVendor: the model vendor failed after the gateway's retries (5xx).
	TurnVendor TurnClass = "vendor"
	// TurnCrashed: the sandbox or the runtime failed. Only crashes count
	// toward an agent's removal (spec Section 8.2).
	TurnCrashed TurnClass = "crashed"
	// TurnUnstarted: the coordinator could not start the turn before the
	// round's deadline, as when the runner could not be reached. Such a
	// turn is not quiet, and leaves the agent's count of crashes as it was.
	TurnUnstarted TurnClass = "unstarted"
)

// StopReason says why a run stopped (spec Section 4.1).
type StopReason string

// Stop reasons.
const (
	StopConverged       StopReason = "converged"
	StopQuiet           StopReason = "quiet"
	StopBudgetExhausted StopReason = "budget_exhausted"
	StopTriggerResolved StopReason = "trigger_resolved"
	// StopCut: the day's spend reached the daily cap.
	StopCut StopReason = "cut"
	// StopNotStaffed: no agent to invite, or none left.
	StopNotStaffed StopReason = "not_staffed"
)

// Next says what follows a round.
type Next string

// What follows a round.
const (
	NextRound  Next = "round"
	NextWrapUp Next = "wrap_up"
	NextStop   Next = "stop"
)

// Decision is what the coordinator decided when a round ended.
type Decision struct {
	Next   Next          `json:"next"`
	WrapUp board.EntryID `json:"wrapUp,omitempty"`
	Stop   StopReason    `json:"stop,omitempty"`
}

// RoundEnded records how a round ended and what follows it. The coordinator
// writes it before any effect of its decision.
type RoundEnded struct {
	Round   int     `json:"round"`
	EndedBy EndedBy `json:"endedBy"`
	// EndedAt is when the round ended, in database time: the last turn's
	// end, or the deadline.
	EndedAt time.Time `json:"endedAt"`
	// Turns holds how each invited agent's turn ended.
	Turns map[string]TurnClass `json:"turns"`
	// Spent and DaySpent are the run's spend and the day's, in
	// cost-weighted tokens, as read once the round's calls settled or the
	// wait for them ended; Settled says whether no call of the run was
	// still in flight.
	Spent    int64 `json:"spent"`
	Settled  bool  `json:"settled"`
	DaySpent int64 `json:"daySpent"`
	// Rounds and WallClock are the budget used so far, this round included:
	// normal rounds that did not end by an outage.
	Rounds    int           `json:"rounds"`
	WallClock time.Duration `json:"wallClock"`
	// Converged lists the hypotheses converged when the round ended, by
	// score; Entries counts the round's entries (board.RoundFacts).
	Converged []board.EntryID `json:"converged,omitempty"`
	Entries   int             `json:"entries"`
	Quiet     bool            `json:"quiet"`
	// Removed lists the agents removed after this round: two consecutive
	// crashed turns (spec Section 8.2).
	Removed []string `json:"removed,omitempty"`
	// Unseen says the instance that ended the round did not act on it
	// (Ending.Unseen).
	Unseen   bool     `json:"unseen,omitempty"`
	Decision Decision `json:"decision"`
}

// RunStopped stops a run, with the budget it used and its conclusion.
type RunStopped struct {
	Reason     StopReason    `json:"reason"`
	Rounds     int           `json:"rounds"`
	WallClock  time.Duration `json:"wallClock"`
	Spent      int64         `json:"spent"`
	Settled    bool          `json:"settled"`
	Conclusion board.EntryID `json:"conclusion,omitempty"`
}

// AwaitingClose records that an item waits for a person to close it, with
// the item's reason (spec Section 4.1).
type AwaitingClose struct {
	Reason StopReason `json:"reason"`
}

// ItemReason is an item's stop reason given its primary run's: a run cut at
// the daily cap leaves the item not staffed (spec Section 4.1).
func ItemReason(run StopReason) StopReason {
	if run == StopCut {
		return StopNotStaffed
	}
	return run
}
```

- [ ] **Step 4: The fold**

Create `internal/work/item/state.go`:

```go
package item

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Status is an item's state (spec Section 4.1).
type Status string

// Item states. An item is queued from its opening until it starts; until
// Plan 7b queues items, it starts at once.
const (
	StatusQueued        Status = "queued"
	StatusInvestigating Status = "investigating"
	StatusAwaitingClose Status = "awaiting_close"
)

// State is a work item, folded from its records in commit order.
type State struct {
	ID       ID
	Incident IncidentID
	Opened   *Opened
	Started  *Started
	// Run is the item's primary run, once created.
	Run *Run
	// Closing is set once the item awaits its close.
	Closing *AwaitingClose
	last    int64
}

// Run is one of an item's runs, folded from its records.
type Run struct {
	ID      board.RunID
	Created RunCreated
	Rounds  []Round
	Stopped *RunStopped
}

// Round is one of a run's rounds: how it opened, and how it ended once it
// has.
type Round struct {
	Opened RoundOpened
	Ended  *RoundEnded
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

// Fold folds an item's records, in commit order, into its state.
func Fold(records []Record) (*State, error) {
	s := &State{}
	for i := range records {
		if err := s.Apply(&records[i]); err != nil {
			return nil, fmt.Errorf("record %d: %w", records[i].Seq, err)
		}
	}
	return s, nil
}

// Apply adds one committed record to the item, after checking its version
// and that it follows the lifecycle's order.
func (s *State) Apply(r *Record) error {
	if r.Body.V != Version {
		return fmt.Errorf("%w: record %s is of version %d", ErrVersion, r.ID, r.Body.V)
	}
	if s.Opened != nil && (r.Item != s.ID || r.Seq <= s.last) {
		return invalid("record %s is not the item's next", r.ID)
	}
	if err := s.apply(r); err != nil {
		return err
	}
	s.last = r.Seq
	return nil
}

func (s *State) apply(r *Record) error {
	b := &r.Body
	kind := b.Kind()
	if err := s.admits(kind); err != nil {
		return err
	}
	switch kind {
	case KindOpened:
		s.ID, s.Incident, s.Opened = r.Item, r.Incident, b.Opened
	case KindStarted:
		if s.Started != nil {
			return invalid("the item started already")
		}
		s.Started = b.Started
	case KindRunCreated:
		return s.create(r.Run, b.RunCreated)
	case KindAwaitingClose:
		return s.close(b.AwaitingClose)
	default:
		if s.Run == nil || r.Run != s.Run.ID {
			return invalid("record %s names a run the item does not have", r.ID)
		}
		return s.Run.apply(b)
	}
	return nil
}

// create adds the item's run, once, after the item started, under an id that
// holds no colon: a record's scope in the audit chain is its run, or
// incident:<id> for the records of no run.
func (s *State) create(run board.RunID, rc *RunCreated) error {
	if s.Started == nil || s.Run != nil || run == "" {
		return invalid("a run is created once, after the item started")
	}
	if strings.Contains(string(run), ":") {
		return invalid("run id %q holds a colon", run)
	}
	s.Run = &Run{ID: run, Created: *rc}
	return nil
}

// close has the item await its close once its run stopped, with its run's
// reason as ItemReason gives it, or not staffed when it has no run.
func (s *State) close(c *AwaitingClose) error {
	if (s.Run == nil) != (s.Started == nil) || s.Run != nil && s.Run.Stopped == nil {
		return invalid("an item awaits its close once its run stopped, or when it is not staffed")
	}
	want := StopNotStaffed
	if s.Run != nil {
		want = ItemReason(s.Run.Stopped.Reason)
	}
	if c.Reason != want {
		return invalid("the item awaits its close %s, not %s", want, c.Reason)
	}
	s.Closing = c
	return nil
}

// admits checks what every record needs: one kind, an item that does not
// await its close, and the item's opening first, once.
func (s *State) admits(kind Kind) error {
	switch {
	case kind == "":
		return invalid("a record holds exactly one body")
	case s.Closing != nil:
		return invalid("the item awaits its close")
	case (s.Opened == nil) != (kind == KindOpened):
		return invalid("an item opens with its first record, once")
	}
	return nil
}

func (r *Run) apply(b *Body) error {
	if r.Stopped != nil {
		return invalid("run %s stopped", r.ID)
	}
	latest := r.Latest()
	switch {
	case b.RoundOpened != nil:
		return r.open(latest, b.RoundOpened)
	case b.RoundEnded != nil:
		if latest == nil || latest.Ended != nil || b.RoundEnded.Round != latest.Opened.Round {
			return invalid("a round ends once, after it opened")
		}
		latest.Ended = b.RoundEnded
	default:
		st := b.RunStopped
		decided := latest != nil && latest.Ended != nil && latest.Ended.Decision == Decision{Next: NextStop, Stop: st.Reason}
		if !decided && (latest != nil || st.Reason != StopNotStaffed) {
			return invalid("a run stops as its last round decided, or not staffed before its first")
		}
		if st.Reason == StopConverged && st.Conclusion != latest.Opened.WrapUp {
			return invalid("a converged run concludes with its wrap-up round's hypothesis")
		}
		r.Stopped = st
	}
	return nil
}

func (r *Run) open(latest *Round, o *RoundOpened) error {
	if o.Round != len(r.Rounds)+1 {
		return invalid("round %d cannot follow round %d", o.Round, len(r.Rounds))
	}
	want := Decision{Next: NextRound}
	if latest != nil {
		if latest.Ended == nil {
			return invalid("round %d is still open", latest.Opened.Round)
		}
		want = latest.Ended.Decision
	}
	if want.Next == NextStop || want.WrapUp != o.WrapUp {
		return invalid("round %d opens as round %d decided", o.Round, len(r.Rounds))
	}
	r.Rounds = append(r.Rounds, Round{Opened: *o})
	return nil
}

// Status returns the item's state.
func (s *State) Status() Status {
	switch {
	case s.Closing != nil:
		return StatusAwaitingClose
	case s.Started != nil:
		return StatusInvestigating
	}
	return StatusQueued
}

// Latest returns the run's latest round, or nil before its first.
func (r *Run) Latest() *Round {
	if len(r.Rounds) == 0 {
		return nil
	}
	return &r.Rounds[len(r.Rounds)-1]
}

// Agents returns the agents the run's next round invites: its latest round's,
// but those removed after it, or the run's invited agents before its first
// round.
func (r *Run) Agents() []string {
	l := r.Latest()
	if l == nil {
		return append(make([]string, 0, len(r.Created.Agents)), r.Created.Agents...)
	}
	agents := make([]string, 0, len(l.Opened.Agents))
	for _, a := range l.Opened.Agents {
		if l.Ended == nil || !slices.Contains(l.Ended.Removed, a) {
			agents = append(agents, a)
		}
	}
	return agents
}

// usage returns the budget the run used, as its latest ended round says.
func (r *Run) usage() (rounds int, wallClock time.Duration) {
	for i := len(r.Rounds) - 1; i >= 0; i-- {
		if e := r.Rounds[i].Ended; e != nil {
			return e.Rounds, e.WallClock
		}
	}
	return 0, 0
}

// wrapUps counts the run's wrap-up rounds, the open one ending as re says,
// but those no agent took part in, as the coordinator saw them: they revoked
// nothing.
func (r *Run) wrapUps(re *RoundEnded) int {
	n := 0
	for i := range r.Rounds {
		ended := r.Rounds[i].Ended
		if ended == nil {
			ended = re
		}
		if r.Rounds[i].Opened.WrapUp != "" && ended.tookPart() {
			n++
		}
	}
	return n
}

// crashes counts an agent's crashed turns in a row in the run's rounds before
// round i, rounds that ended by an outage, and turns that never started,
// aside.
func (r *Run) crashes(agent string, i int) int {
	n := 0
	for i--; i >= 0; i-- {
		e := r.Rounds[i].Ended
		if e == nil || e.EndedBy == EndedByOutage || e.Turns[agent] == TurnUnstarted {
			continue
		}
		if e.Turns[agent] != TurnCrashed {
			break
		}
		n++
	}
	return n
}
```

- [ ] **Step 5: The round's end**

Create `internal/work/item/decide.go`:

```go
package item

import (
	"slices"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

const (
	// MaxWrapUps caps a run's wrap-up rounds: a convergence revoked twice
	// gets no third (spec Section 5.6).
	MaxWrapUps = 2
	// MaxCrashes is how many crashed turns in a row remove an agent from a
	// run (spec Section 8.2).
	MaxCrashes = 2
	// roundShare is the part of a round's limit that must be left of the
	// wall clock for a normal round to open: a third.
	roundShare = 3
)

// Ending is what the coordinator knows when a round ends.
type Ending struct {
	EndedBy EndedBy
	// EndedAt is when the round ended, in database time.
	EndedAt time.Time
	// Turns holds how each agent's turn ended, as the runner reported it; a
	// turn it never reported counts as interrupted.
	Turns map[string]TurnClass
	// Spent, Settled and DaySpent are as RoundEnded holds them, and
	// DailyCap is the deployment's.
	Spent    int64
	Settled  bool
	DaySpent int64
	DailyCap int64
	// Report is the run's board as the round ended.
	Report board.Report
	// TriggerResolved says the item's trigger resolved (Plan 7c).
	TriggerResolved bool
	// Unseen says the instance ending the round did not act on it, as a
	// takeover finds a round past its deadline: its turns are what the board
	// and the deadline say, which cannot tell a round no agent ran from one
	// that found nothing.
	Unseen bool
}

// tookPart reports whether an agent took part in a round, as the coordinator
// saw it: a round that ended by an outage, one the instance that ended it did
// not see, and one none of whose turns started, show none.
func (re *RoundEnded) tookPart() bool {
	if re.EndedBy == EndedByOutage || re.Unseen {
		return false
	}
	for _, c := range re.Turns {
		if c != TurnUnstarted {
			return true
		}
	}
	return false
}

// End decides how the run's open round ends: the record the coordinator
// writes before any of its effects. In this order, the run stops when the
// round was the wrap-up round of a hypothesis still converged, in which an
// agent took part (converged), when the day's spend reached the cap (cut),
// when the trigger resolved, or when no agent is left (not staffed).
// Otherwise a converged hypothesis gets a wrap-up round, whatever the
// budget, while the run has had fewer than MaxWrapUps that an agent took
// part in; then the run stops when its budget is used, or when the round was
// quiet; else a normal round follows.
func (r *Run) End(e *Ending) RoundEnded {
	i := len(r.Rounds) - 1
	o := &r.Rounds[i].Opened
	re := RoundEnded{
		Round: o.Round, EndedBy: e.EndedBy, EndedAt: e.EndedAt, Turns: map[string]TurnClass{},
		Spent: e.Spent, Settled: e.Settled, DaySpent: e.DaySpent, Entries: e.Report.Facts.Entries, Unseen: e.Unseen,
	}
	re.Rounds, re.WallClock = r.usage()
	counted := e.EndedBy != EndedByOutage
	if counted {
		for _, a := range o.Agents {
			c, ok := e.Turns[a]
			if !ok {
				c = TurnInterrupted
			}
			re.Turns[a] = c
			if c == TurnCrashed && r.crashes(a, i)+1 >= MaxCrashes {
				re.Removed = append(re.Removed, a)
			}
		}
		if o.WrapUp == "" {
			re.Rounds++
			re.WallClock += max(e.EndedAt.Sub(o.OpenedAt), 0)
		}
	}
	for i := range e.Report.Assessment.Hypotheses {
		if h := &e.Report.Assessment.Hypotheses[i]; h.Converged {
			re.Converged = append(re.Converged, h.ID)
		}
	}
	re.Quiet = counted && !e.Unseen && re.Entries == 0 && !slices.ContainsFunc(o.Agents, func(a string) bool {
		c := re.Turns[a]
		return c != TurnEnded && c != TurnFinished && c != TurnInterrupted
	})
	re.Decision = r.decide(o, &re, e)
	return re
}

func (r *Run) decide(o *RoundOpened, re *RoundEnded, e *Ending) Decision {
	stop := func(reason StopReason) Decision { return Decision{Next: NextStop, Stop: reason} }
	switch {
	case o.WrapUp != "" && re.tookPart() && slices.Contains(re.Converged, o.WrapUp):
		return stop(StopConverged)
	case e.DaySpent >= e.DailyCap:
		return stop(StopCut)
	case e.TriggerResolved:
		return stop(StopTriggerResolved)
	case len(o.Agents) == len(re.Removed):
		return stop(StopNotStaffed)
	case len(re.Converged) > 0 && r.wrapUps(re) < MaxWrapUps:
		return Decision{Next: NextWrapUp, WrapUp: re.Converged[0]}
	case r.exhausted(re):
		return stop(StopBudgetExhausted)
	case re.Quiet:
		return stop(StopQuiet)
	}
	return Decision{Next: NextRound}
}

// exhausted reports whether the run used its budget: its rounds, its wall
// clock, so much of it that less than a third of a round's limit is left,
// or its tokens.
func (r *Run) exhausted(re *RoundEnded) bool {
	b := r.Created.Budget
	left := b.WallClock - re.WallClock
	return re.Rounds >= b.Rounds || left < r.Created.RoundLimit/roundShare || left <= 0 || re.Spent >= b.Tokens
}

// Open returns the run's next round as decided, opening at now, in database
// time. A normal round's deadline is its limit, cut to the wall clock left;
// a wrap-up round, outside the rounds and wall-clock budgets, has its whole
// limit, and its sessions may spend one round's share of the token budget
// above the run's spend when it opens (spec Section 5.6).
func (r *Run) Open(now time.Time) RoundOpened {
	b := r.Created.Budget
	o := RoundOpened{Round: len(r.Rounds) + 1, OpenedAt: now, Budget: b.Tokens, Agents: r.Agents()}
	limit := r.Created.RoundLimit
	if l := r.Latest(); l != nil && l.Ended != nil && l.Ended.Decision.Next == NextWrapUp {
		o.WrapUp = l.Ended.Decision.WrapUp
		o.Budget = max(b.Tokens, l.Ended.Spent+b.Tokens/int64(max(b.Rounds, 1)))
	} else {
		_, used := r.usage()
		limit = min(limit, b.WallClock-used)
	}
	o.Deadline = now.Add(limit)
	return o
}

// Stop returns the run's stop record: the reason its latest round decided,
// with the budget it says the run used, or not staffed before its first
// round. A converged run's conclusion is the hypothesis of its wrap-up
// round, which stayed converged through it; another run's is the conclusion
// given, the board's as the round ended.
func (r *Run) Stop(conclusion board.EntryID) RunStopped {
	st := RunStopped{Reason: StopNotStaffed, Conclusion: conclusion}
	if l := r.Latest(); l != nil && l.Ended != nil {
		e := l.Ended
		st.Reason, st.Rounds, st.WallClock, st.Spent, st.Settled = e.Decision.Stop, e.Rounds, e.WallClock, e.Spent, e.Settled
		if st.Reason == StopConverged {
			st.Conclusion = l.Opened.WrapUp
		}
	}
	return st
}
```

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test ./internal/work/item/ && golangci-lint run ./...`
Expected: `ok`; `0 issues.`

- [ ] **Step 7: Commit**

```bash
git add internal/work/item
git commit -m "feat(work): a work item's records, the state they fold into, and how a round's end is decided"
```

### Task 4: Session tokens, live sessions, and the token rule

Decision 12. The coordinator issues each agent session two tokens, one per gateway, new each round, and keeps the live sessions in memory, where both gateways look them up. Both gateways redact any session token. The MCP gateway also hides its caller's own token wherever it keeps text, refuses arguments that hold a token, and refuses a call once its round has ended.

**Files:**
- Create: `internal/work/tokens/tokens.go`, `internal/work/tokens/tokens_test.go`, `internal/work/tokens/export_test.go`
- Create: `internal/work/app/sessions.go`, `internal/work/sessions/sessions.go`, `internal/work/sessions/sessions_test.go`
- Create: `internal/actions/app/token_test.go`
- Modify: `internal/registry/config/redaction.go`, `internal/actions/redact/redact.go`
- Modify: `internal/actions/app/call.go`, `internal/actions/app/board.go`, `internal/actions/app/platform.go`, `internal/actions/app/write.go`
- Modify: `internal/actions/app/fixture_test.go`, `internal/actions/app/board_test.go`
- Modify: `internal/llm/app/gateway.go`, `internal/llm/app/gateway_test.go`

**Interfaces:**
- Consumes: both gateways' `Sessions` ports and `ErrNoSession` (Plans 5a and 6b); `config.SessionTokenReplacement` (Plan 6b).
- Produces:
  - `config.SessionTokenPattern = "asw[ml]_[A-Za-z0-9]{43}"`, `redact.SessionTokens() redact.Rule`;
  - `tokens.Gateway` (`tokens.MCP`, `tokens.LLM`), `tokens.New(Gateway) Secret`, `tokens.Secret` with `Reveal() string` and `Hash() [32]byte`, `tokens.Hash(string) [32]byte`;
  - `app.Session{Run board.RunID; Agent string; Commit string; Item map[string]string}`, `app.Tokens{MCP, LLM tokens.Secret}`, and the port `app.Sessions{Issue(*Session) Tokens; Move(run, agent, round, deadline, budget) error; End(run, agent); EndRun(run); EndAll()}`: `Issue` gives a session new tokens and forgets its earlier ones, and the session keeps its channel until it ends;
  - `sessions.New() *Registry`, which implements `app.Sessions`, with `MCP() actionsapp.Sessions` and `LLM() llmapp.Sessions`.

- [ ] **Step 1: Write the failing tests**

Create `internal/work/tokens/tokens_test.go`:

```go
package tokens_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/tokens"
)

// A token is its gateway's prefix and 43 letters and digits, from 32 random
// bytes, so every token has the same width; no two are alike.
func TestATokenIsItsPrefixAndFortyThreeLettersAndDigits(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range []struct {
		gateway tokens.Gateway
		form    string
	}{{tokens.MCP, `^aswm_[A-Za-z0-9]{43}$`}, {tokens.LLM, `^aswl_[A-Za-z0-9]{43}$`}} {
		for range 500 {
			s := tokens.New(c.gateway)

			assert.Regexp(t, c.form, s.Reveal())
			assert.False(t, seen[s.Reveal()])
			seen[s.Reveal()] = true
		}
	}
	assert.Equal(t, strings.Repeat("0", 43), tokens.Encode([32]byte{}), "leading zeros keep the width")
	assert.Equal(t, "0000000000000000000000000000000000000000001", tokens.Encode([32]byte{31: 1}))
	full := [32]byte{}
	for i := range full {
		full[i] = 0xff
	}
	assert.Len(t, tokens.Encode(full), 43, "2^256 - 1 fits in 43 digits of base 62")
}

// A token never prints: as a Secret it shows as [session token] however it
// is formatted, written as JSON, or logged.
func TestASecretNeverPrints(t *testing.T) {
	s := tokens.New(tokens.LLM)
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("issued", "token", s)
	raw, err := json.Marshal(map[string]any{"token": s})
	require.NoError(t, err)

	for _, shown := range []string{
		fmt.Sprintf("%s %v %+v %#v %q", s, s, s, s, s), string(raw), logged.String(), s.String(),
	} {
		assert.NotContains(t, shown, s.Reveal())
		assert.Contains(t, shown, "[session token]")
	}
	assert.Equal(t, tokens.Hash(s.Reveal()), s.Hash())
}

// A token never prints as a field of another struct either, exported or
// not: fmt and slog print a field they cannot call methods on, an
// unexported one, by its own fields, which hold no more than an address.
func TestASecretInAFieldNeverPrints(t *testing.T) {
	s := tokens.New(tokens.MCP)
	held := struct {
		agent string
		token tokens.Secret
		Token tokens.Secret
	}{"release-investigator", s, s}
	body := s.Reveal()[len("aswm_"):]
	var text, logged bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("held", "value", held)
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("held", "value", held)

	for _, shown := range []string{
		fmt.Sprintf("%v", held), fmt.Sprintf("%+v", held), fmt.Sprintf("%#v", held),
		fmt.Sprintf("%v", []any{held}), fmt.Sprintf("%v", &held), text.String(), logged.String(),
	} {
		assert.NotContains(t, shown, body)
	}
}

// Every token matches the rule both gateways redact with, glued to other
// text or not, and the replacement is the one no rule may change.
func TestEveryTokenMatchesTheRuleThatHidesIt(t *testing.T) {
	red := redact.New(redact.SessionTokens())
	for _, g := range []tokens.Gateway{tokens.MCP, tokens.LLM} {
		s := tokens.New(g)

		assert.Equal(t, "key "+config.SessionTokenReplacement+" here", red.Text("key "+s.Reveal()+" here"))
		assert.Equal(t, "x"+config.SessionTokenReplacement+"y", red.Text("x"+s.Reveal()+"y"), "no word boundary to defeat")
	}
}
```

Create `internal/work/tokens/export_test.go`:

```go
package tokens

// Encode writes 32 bytes as a token's 43 letters and digits.
var Encode = encode
```

Create `internal/work/sessions/sessions_test.go`:

```go
package sessions_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/sessions"
)

var _ app.Sessions = (*sessions.Registry)(nil)

var deadline = time.Date(2026, 10, 18, 9, 2, 30, 0, time.UTC)

func issue(t *testing.T, r *sessions.Registry, run board.RunID, agent string) app.Tokens {
	t.Helper()
	return r.Issue(&app.Session{Run: run, Agent: agent, Commit: "c1", Item: map[string]string{"service": "checkout"}})
}

// A session is known to the gateways only once its round opened: from Move
// on, until it ends. Each gateway sees it as its own Session.
func TestASessionIsKnownOnceItsRoundOpened(t *testing.T) {
	r := sessions.New()
	tok := issue(t, r, "run-1", "release-investigator")
	ctx := t.Context()

	_, beforeMCP := r.MCP().Session(ctx, tok.MCP.Reveal())
	_, beforeLLM := r.LLM().Session(ctx, tok.LLM.Reveal())
	require.NoError(t, r.Move("run-1", "release-investigator", 1, deadline, 90_000))
	mcp, err := r.MCP().Session(ctx, tok.MCP.Reveal())
	require.NoError(t, err)
	llm, err := r.LLM().Session(ctx, tok.LLM.Reveal())
	require.NoError(t, err)

	require.ErrorIs(t, beforeMCP, actionsapp.ErrNoSession)
	require.ErrorIs(t, beforeLLM, llmapp.ErrNoSession)
	done := mcp.Done
	mcp.Done = nil
	assert.Equal(t, actionsapp.Session{
		Run: "run-1", Agent: "release-investigator", Commit: "c1", Round: 1, Deadline: deadline, Class: limits.Primary,
		Item: map[string]string{"service": "checkout"},
	}, mcp)
	assert.Equal(t, done, llm.Done, "one channel for both gateways")
	llm.Done = nil
	assert.Equal(t, llmapp.Session{
		Run: "run-1", Agent: "release-investigator", Commit: "c1", Round: 1, Deadline: deadline, Class: cost.Primary, Budget: 90_000,
	}, llm)
}

// A token of one gateway is unknown to the other, and a token no session
// holds is unknown to both.
func TestATokenOfOneGatewayIsUnknownToTheOther(t *testing.T) {
	r := sessions.New()
	tok := issue(t, r, "run-1", "release-investigator")
	require.NoError(t, r.Move("run-1", "release-investigator", 1, deadline, 1))

	_, mcpAtLLM := r.LLM().Session(t.Context(), tok.MCP.Reveal())
	_, llmAtMCP := r.MCP().Session(t.Context(), tok.LLM.Reveal())
	_, none := r.MCP().Session(t.Context(), "aswm_"+string(make([]byte, 43)))

	require.ErrorIs(t, mcpAtLLM, llmapp.ErrNoSession)
	require.ErrorIs(t, llmAtMCP, actionsapp.ErrNoSession)
	require.ErrorIs(t, none, actionsapp.ErrNoSession)
}

// A session moves from round to round with the same channel; once it ends,
// its tokens are unknown and its channel is closed, which ends its calls.
func TestASessionMovesFromRoundToRoundUntilItEnds(t *testing.T) {
	r := sessions.New()
	tok := issue(t, r, "run-1", "release-investigator")
	require.NoError(t, r.Move("run-1", "release-investigator", 1, deadline, 1))
	first, err := r.MCP().Session(t.Context(), tok.MCP.Reveal())
	require.NoError(t, err)

	require.NoError(t, r.Move("run-1", "release-investigator", 2, deadline.Add(time.Minute), 2))
	second, err := r.LLM().Session(t.Context(), tok.LLM.Reveal())
	require.NoError(t, err)
	r.End("run-1", "release-investigator")
	_, gone := r.MCP().Session(t.Context(), tok.MCP.Reveal())

	assert.Equal(t, 2, second.Round)
	assert.Equal(t, int64(2), second.Budget)
	assert.Equal(t, first.Done, second.Done)
	require.ErrorIs(t, gone, actionsapp.ErrNoSession)
	assert.True(t, closed(first.Done))
	require.Error(t, r.Move("run-1", "release-investigator", 3, deadline, 1), "an ended session moves no more")
	r.End("run-1", "release-investigator")
}

// Issuing a session's tokens again, as the coordinator does for each round,
// forgets the earlier ones: a turn of an earlier round still running cannot
// act in the next. The new tokens are known once Move puts the session in
// its round, and the session keeps its channel, which ends the calls of
// either tokens when the session ends.
func TestIssuingAgainGivesFreshTokensAndKeepsTheChannel(t *testing.T) {
	r := sessions.New()
	old := issue(t, r, "run-1", "release-investigator")
	require.NoError(t, r.Move("run-1", "release-investigator", 1, deadline, 1))
	before, err := r.MCP().Session(t.Context(), old.MCP.Reveal())
	require.NoError(t, err)

	fresh := issue(t, r, "run-1", "release-investigator")
	_, early := r.MCP().Session(t.Context(), fresh.MCP.Reveal())
	require.NoError(t, r.Move("run-1", "release-investigator", 2, deadline.Add(time.Minute), 1))

	_, err = r.MCP().Session(t.Context(), old.MCP.Reveal())
	require.ErrorIs(t, err, actionsapp.ErrNoSession)
	_, err = r.LLM().Session(t.Context(), old.LLM.Reveal())
	require.ErrorIs(t, err, llmapp.ErrNoSession)
	require.ErrorIs(t, early, actionsapp.ErrNoSession, "known once Move puts the session in its round")
	after, err := r.MCP().Session(t.Context(), fresh.MCP.Reveal())
	require.NoError(t, err)
	assert.Equal(t, 2, after.Round)
	assert.NotEqual(t, old.MCP.Reveal(), fresh.MCP.Reveal())
	assert.False(t, closed(before.Done), "the earlier tokens' calls in flight go on")
	assert.Equal(t, before.Done, after.Done)
	r.End("run-1", "release-investigator")
	assert.True(t, closed(before.Done))
}

// A run's sessions end with it; EndAll ends every session, as a holder that
// lost its lease does.
func TestARunsSessionsEndWithItAndAllEndAtOnce(t *testing.T) {
	r := sessions.New()
	a := issue(t, r, "run-1", "release-investigator")
	b := issue(t, r, "run-1", "runtime-investigator")
	c := issue(t, r, "run-2", "release-investigator")
	for _, s := range []struct {
		run   board.RunID
		agent string
	}{{"run-1", "release-investigator"}, {"run-1", "runtime-investigator"}, {"run-2", "release-investigator"}} {
		require.NoError(t, r.Move(s.run, s.agent, 1, deadline, 1))
	}

	r.EndRun("run-1")
	_, errA := r.MCP().Session(t.Context(), a.MCP.Reveal())
	_, errB := r.LLM().Session(t.Context(), b.LLM.Reveal())
	kept, errC := r.MCP().Session(t.Context(), c.MCP.Reveal())
	r.EndAll()
	_, afterAll := r.MCP().Session(t.Context(), c.MCP.Reveal())

	require.ErrorIs(t, errA, actionsapp.ErrNoSession)
	require.ErrorIs(t, errB, llmapp.ErrNoSession)
	require.NoError(t, errC)
	require.ErrorIs(t, afterAll, actionsapp.ErrNoSession)
	assert.True(t, closed(kept.Done))
}

// Lookups run while the coordinator moves and ends sessions.
func TestLookupsRunWhileSessionsChange(t *testing.T) {
	r := sessions.New()
	tok := issue(t, r, "run-1", "release-investigator")
	require.NoError(t, r.Move("run-1", "release-investigator", 1, deadline, 1))
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				_, _ = r.MCP().Session(t.Context(), tok.MCP.Reveal())
				_, _ = r.LLM().Session(t.Context(), tok.LLM.Reveal())
			}
		})
	}
	for round := 2; round < 200; round++ {
		_ = r.Move("run-1", "release-investigator", round, deadline, 1)
	}
	r.EndAll()
	wg.Wait()
}

func closed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
```

Create `internal/actions/app/token_test.go`:

```go
package app_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// sessionToken is a token of a session at the LLM gateway, as an agent could
// paste one anywhere.
const sessionToken = "aswl_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefg"

// A session's token is hidden wherever the gateway keeps text: a tool's
// output, shown and stored, and an agent's text on the board, whatever
// rules the configuration holds.
func TestASessionTokenIsHiddenWhereverTheGatewayKeepsText(t *testing.T) {
	e := newEnv(t)
	e.up.set(func(u *upstream) {
		u.replies["get_flag"] = text(`{"flag":"x","on":true,"note":"` + sessionToken + `","truncated":false}`)
	})

	read := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)
	post := e.call(t, "post_task", `{"title":"check `+sessionToken+`"}`)

	require.Equal(t, app.StatusOK, read.Status, read.Text)
	assert.Contains(t, read.Text, config.SessionTokenReplacement)
	_, out, _, _ := e.audit.Record(read.CallID)
	require.Equal(t, app.StatusOK, post.Status, post.Text)
	for _, kept := range []string{read.Text, out.Output, e.events(t)[0].Body.Task.Title} {
		assert.NotContains(t, kept, sessionToken)
	}
	assert.Equal(t, "check "+config.SessionTokenReplacement, e.events(t)[0].Body.Task.Title)
}

// A read call whose arguments hold a session token is refused: the gateway
// sends a server the arguments as the agent wrote them, and an escape does
// not hide the token.
func TestAReadCallWhoseArgumentsHoldASessionTokenIsRefused(t *testing.T) {
	e := newEnv(t)
	for _, args := range []string{
		`{"flag":"` + sessionToken + `","window":"1h"}`,
		`{"flag":"aswl_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefg","window":"1h"}`,
	} {
		r := e.call(t, "flags__get_flag", args)

		assert.Equal(t, app.StatusRefused, r.Status, args)
		assert.Contains(t, r.Text, "session token", args)
		rec, _, _, _ := e.audit.Record(r.CallID)
		assert.NotContains(t, string(rec.Args), sessionToken, args)
	}
	assert.Empty(t, e.up.sent())
}

// A read call whose arguments hold the caller's own token is refused, in any
// case, without its prefix, or with its prefix written otherwise; one whose
// escapes go deeper than the gateway decodes is refused for what they may
// hide.
func TestAReadCallWhoseArgumentsHoldTheCallersTokenIsRefused(t *testing.T) {
	e := newEnv(t)
	body := token[len("aswm_"):]
	for _, args := range []string{
		`{"flag":"` + body + `","window":"1h"}`,
		`{"flag":"ASWM_` + body + `","window":"1h"}`,
		`{"flag":"` + strings.ToLower(body) + `","window":"1h"}`,
		`{"flag":"aswm%5F` + body + `","window":"1h"}`,
	} {
		r := e.call(t, "flags__get_flag", args)

		assert.Equal(t, app.StatusRefused, r.Status, args)
		assert.Contains(t, r.Text, "hold a session token", args)
	}
	deep := e.call(t, "flags__get_flag", `{"flag":"x`+strings.Repeat(`\\`, 16)+`n","window":"1h"}`)
	assert.Equal(t, app.StatusRefused, deep.Status)
	assert.Contains(t, deep.Text, "an escape that may hide a session token")
	assert.Empty(t, e.up.sent())
}

// The caller's own token is hidden wherever the gateway keeps text, in any
// case and without its prefix: a tool's output, the arguments of a read it
// refused and of a write call, the name of a tool it refused, before and
// after the round's deadline, and the agent's text on the board, which every
// agent of the run reads.
func TestTheCallersOwnTokenIsHiddenWhereverTheGatewayKeepsText(t *testing.T) {
	e := newEnv(t)
	body := token[len("aswm_"):]
	e.up.set(func(u *upstream) {
		u.replies["get_flag"] = text(`{"flag":"x","on":true,"note":"` + strings.ToLower(body) + `","truncated":false}`)
	})

	read := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)
	refused := e.call(t, "flags__get_flag", `{"flag":"`+body+`","window":"1h"}`)
	post := e.call(t, "post_task", `{"title":"check ASWM_`+body+`"}`)
	write := e.call(t, "flags__set_flag", `{"flag":"`+body+`","on":false,"_hypothesis":"h-1"}`)
	unknown := e.call(t, "tool_"+body, `{}`)
	e.clock.advance(151 * time.Second)
	late := e.call(t, "tool_"+body, `{}`)

	require.Equal(t, app.StatusOK, read.Status, read.Text)
	require.Equal(t, app.StatusRefused, refused.Status, refused.Text)
	require.Equal(t, app.StatusOK, post.Status, post.Text)
	_, out, _, _ := e.audit.Record(read.CallID)
	rec, _, _, _ := e.audit.Record(refused.CallID)
	written, _, _, _ := e.audit.Record(write.CallID)
	named, _, _, _ := e.audit.Record(unknown.CallID)
	named2, _, _, _ := e.audit.Record(late.CallID)
	kept := []string{read.Text, out.Output, string(rec.Args), string(written.Args), named.Tool, named2.Tool}
	for _, ev := range e.events(t) {
		if ev.Body.Task != nil {
			kept = append(kept, ev.Body.Task.Title)
		}
	}
	require.Len(t, kept, 7)
	for _, k := range kept {
		assert.NotContains(t, strings.ToLower(k), strings.ToLower(body))
	}
}

// A quote that spans what replaced a session token is refused, as one that
// spans any rule's replacement: the token could have been anything.
func TestAQuoteSpanningASessionTokensReplacementIsRefused(t *testing.T) {
	e := newEnv(t)
	e.up.set(func(u *upstream) {
		u.replies["get_flag"] = text(`{"flag":"x","on":true,"note":"key ` + sessionToken + ` used","truncated":false}`)
	})
	read := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)
	require.Equal(t, app.StatusOK, read.Status, read.Text)

	r := e.call(t, "post_evidence", fmt.Sprintf(`{"claim":"a key","call_id":%q,"quote":"key %s used"}`, read.CallID, config.SessionTokenReplacement))

	assert.Equal(t, app.StatusRefused, r.Status)
	assert.Contains(t, r.Text, "spans text the redaction rules hid")
}
```

Apply this change to `internal/actions/app/fixture_test.go`, which gives the session a token of a session token's form:

```diff
diff --git a/internal/actions/app/fixture_test.go b/internal/actions/app/fixture_test.go
index 66a9fc1..32948db 100644
--- a/internal/actions/app/fixture_test.go
+++ b/internal/actions/app/fixture_test.go
@@ -405,11 +405,12 @@ type env struct {
 	runs     *workmemory.Runs
 	bc       board.Context
 	boards   *workapp.Boards
 }
 
-const token = "tok"
+// token is the session's token at this gateway, in a session token's form.
+const token = "aswm_QRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456"
 
 func newEnv(t *testing.T, change ...func(*app.Ports, *app.Options)) *env {
 	t.Helper()
 	cc, err := celeval.New()
 	require.NoError(t, err)
```

Apply this change to `internal/actions/app/board_test.go`:

```diff
diff --git a/internal/actions/app/board_test.go b/internal/actions/app/board_test.go
index a7df416..d3acbe7 100644
--- a/internal/actions/app/board_test.go
+++ b/internal/actions/app/board_test.go
@@ -3,10 +3,11 @@ package app_test
 import (
 	"encoding/json"
 	"fmt"
 	"strings"
 	"testing"
+	"time"
 
 	"github.com/stretchr/testify/assert"
 	"github.com/stretchr/testify/require"
 
 	"github.com/jchen0426/agent-swarm/internal/actions/app"
@@ -384,10 +385,33 @@ func TestABoardCallThatCannotBeRecordedLeavesANotice(t *testing.T) {
 		{Code: board.NoticeRefusedCall, Agent: "flag-investigator", Tool: "post_task"},
 		{Code: board.NoticeRefusedCall, Agent: "flag-investigator", Tool: "post_task", Rule: "arguments"},
 	}, e.rules(t))
 }
 
+// A call made once its session's round is past its deadline is refused,
+// whatever its tool: what it did would count in no round, and the round's
+// end may be recorded already. Nothing reaches a server or the board.
+func TestACallAfterItsRoundsDeadlineIsRefused(t *testing.T) {
+	e := newEnv(t)
+	e.clock.advance(151 * time.Second)
+
+	for _, c := range [][2]string{
+		{"flags__get_flag", `{"flag":"x","window":"1h"}`},
+		{"post_task", `{"title":"check the pool"}`},
+		{"end_turn", `{}`},
+	} {
+		r := e.call(t, c[0], c[1])
+
+		assert.Equal(t, app.StatusRefused, r.Status, c[0])
+		assert.Contains(t, r.Text, "the round has ended", c[0])
+	}
+	assert.Empty(t, e.up.sent())
+	for _, ev := range e.events(t) {
+		assert.NotNil(t, ev.Body.Notice, "nothing but notices is posted")
+	}
+}
+
 // A post made in a round that has ended is refused, never counted in the
 // run's next round.
 func TestAPostThatArrivesAfterItsRoundIsRefused(t *testing.T) {
 	e := newEnv(t)
 	e.openRound(t, 2, "")
```

Apply this change to `internal/llm/app/gateway_test.go`:

```diff
diff --git a/internal/llm/app/gateway_test.go b/internal/llm/app/gateway_test.go
index 7837972..95a9abe 100644
--- a/internal/llm/app/gateway_test.go
+++ b/internal/llm/app/gateway_test.go
@@ -1737,5 +1737,20 @@ func TestANotMadeOutcomeIsTriedAgain(t *testing.T) {
 	require.Eventually(t, func() bool { _, _, _, ended := e.calls.Record("m-1"); return ended }, 5*time.Second, 5*time.Millisecond)
 	spent, err := e.calls.Spent(t.Context(), "run-1")
 	require.NoError(t, err)
 	assert.Zero(t, spent)
 }
+
+// A token of any session, at either gateway, is redacted before the vendor
+// reads a request, as the session's own is.
+func TestAnySessionTokenIsRedactedBeforeTheVendorReadsIt(t *testing.T) {
+	e := newEnv(t)
+	other := "aswm_" + strings.Repeat("Z", 43)
+
+	r := e.post(t, strings.Replace(request, "Find why checkout fails.", "Use "+other+" to call the tools.", 1))
+
+	require.Equal(t, 200, r.Status, string(r.Body))
+	assert.NotContains(t, e.vendor.bodies[0], other)
+	assert.Contains(t, e.vendor.bodies[0], "Use [redacted:session-token] to call the tools.")
+	rec, _ := e.last(t)
+	assert.NotContains(t, rec.Request, other)
+}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -race ./internal/work/... ./internal/actions/... ./internal/llm/... ./internal/registry/...`
Expected: FAIL: the build of `internal/work/sessions`, `internal/work/tokens` fails; `TestACallAfterItsRoundsDeadlineIsRefused` fails, among others

- [ ] **Step 3: The pattern and the rule**

Apply this change to `internal/registry/config/redaction.go`:

```diff
diff --git a/internal/registry/config/redaction.go b/internal/registry/config/redaction.go
index f94b63b..48dca03 100644
--- a/internal/registry/config/redaction.go
+++ b/internal/registry/config/redaction.go
@@ -10,15 +10,21 @@ import (
 	"unicode"
 
 	"github.com/jchen0426/agent-swarm/internal/registry/selector"
 )
 
-// SessionTokenReplacement is what the LLM gateway puts in place of a
-// session's token, wherever a request holds it. No rule that applies always
-// may change it.
+// SessionTokenReplacement is what the gateways put in place of a session's
+// token, wherever text they store or forward holds one. No rule that applies
+// always may change it.
 const SessionTokenReplacement = "[redacted:session-token]" //nolint:gosec // G101: what replaces a token, not one
 
+// SessionTokenPattern matches a token of either gateway's sessions: a prefix
+// for the gateway and 43 letters and digits (internal/work/tokens). It has no
+// word boundaries, which text glued to a token would defeat. Both gateways
+// redact with it before the configuration's rules.
+const SessionTokenPattern = `asw[ml]_[A-Za-z0-9]{43}` //nolint:gosec // G101: the form of a token, not one
+
 // toolCallIDs are 10,000 ids in the model vendor's form for a tool call,
 // which a request carries back with each call and its result: "toolu_01"
 // and 22 letters and digits, drawn from a fixed seed. A sample of random ids
 // can miss a run of one character class, such as eight digits, that a
 // pattern would match in a real id, so five more follow, one for each class
```

Apply this change to `internal/actions/redact/redact.go`:

```diff
diff --git a/internal/actions/redact/redact.go b/internal/actions/redact/redact.go
index c7d9d11..c1e860f 100644
--- a/internal/actions/redact/redact.go
+++ b/internal/actions/redact/redact.go
@@ -50,10 +50,19 @@ type Rule struct {
 // FromConfig returns the rule a configured redaction rule holds.
 func FromConfig(r *config.RedactionRule) Rule {
 	return Rule{Patterns: r.Patterns(), Paths: r.Paths(), Replacement: r.Replacement()}
 }
 
+// sessionTokens matches a token of either gateway's sessions.
+var sessionTokens = regexp.MustCompile(config.SessionTokenPattern)
+
+// SessionTokens returns the rule that hides the gateways' session tokens,
+// which both gateways apply before the configuration's rules.
+func SessionTokens() Rule {
+	return Rule{Patterns: []*regexp.Regexp{sessionTokens}, Replacement: config.SessionTokenReplacement}
+}
+
 // Redactor applies rules in order.
 type Redactor struct {
 	rules []Rule
 	// needles holds each rule's literals, folded once, when the redactor
 	// is made.
```

- [ ] **Step 4: The tokens**

Create `internal/work/tokens/tokens.go`:

```go
// Package tokens issues the tokens that name an agent's sessions at the two
// gateways (spec Section 2): a prefix for each gateway and 43 letters and
// digits from 32 random bytes. Every token has the same form, so a rule that
// applies always finds it whole, wherever an agent writes it
// (config.SessionTokenPattern).
package tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"math/big"
)

// Gateway names the gateway a token is for. A token of one gateway is
// unknown to the other.
type Gateway string

// The gateways.
const (
	MCP Gateway = "mcp"
	LLM Gateway = "llm"
)

const (
	// width is how many base-62 digits 32 bytes take: 62^43 > 2^256.
	width    = 43
	alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// shown is what a Secret prints as.
	shown = "[session token]"
)

// Secret is a token. It prints as [session token] however it is formatted,
// written as JSON or logged, so that no log, error or record holds it. As a
// field whose methods fmt and slog cannot call, an unexported one, it prints
// by its own field, an address. Reveal gives the token itself, for the
// runtime that sends it.
type Secret struct{ p *string }

// New issues a token for a gateway.
func New(g Gateway) Secret {
	var b [32]byte
	// crypto/rand.Read never returns an error: it fills b or crashes.
	_, _ = rand.Read(b[:])
	prefix := "aswm_"
	if g == LLM {
		prefix = "aswl_"
	}
	token := prefix + encode(b)
	return Secret{p: &token}
}

// encode writes 32 bytes as 43 base-62 digits, zeros first where the number
// is small.
func encode(b [32]byte) string {
	n, base, digit := new(big.Int).SetBytes(b[:]), big.NewInt(int64(len(alphabet))), new(big.Int)
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		n.DivMod(n, base, digit)
		out[i] = alphabet[digit.Int64()]
	}
	return string(out)
}

// Reveal returns the token itself, or nothing for the zero Secret.
func (s Secret) Reveal() string {
	if s.p == nil {
		return ""
	}
	return *s.p
}

// Hash returns the token's SHA-256, by which the gateways look it up.
func (s Secret) Hash() [32]byte { return Hash(s.Reveal()) }

// String prints the token as [session token].
func (s Secret) String() string { return shown }

// Format prints the token as [session token], whatever the verb.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, shown) }

// MarshalJSON writes the token as [session token].
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + shown + `"`), nil }

// LogValue logs the token as [session token].
func (s Secret) LogValue() slog.Value { return slog.StringValue(shown) }

// Hash returns a token's SHA-256.
func Hash(token string) [32]byte { return sha256.Sum256([]byte(token)) }
```

- [ ] **Step 5: The live sessions**

Create `internal/work/app/sessions.go`:

```go
package app

import (
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/tokens"
)

// Session is an agent's session in a run, as the coordinator issues it.
type Session struct {
	Run   board.RunID
	Agent string
	// Commit is the configuration revision the item locked.
	Commit string
	// Item holds the item's trusted routing values (spec Section 3.3).
	Item map[string]string
}

// Tokens are a session's two tokens, one for each gateway.
type Tokens struct {
	MCP, LLM tokens.Secret
}

// Sessions holds the live sessions, which the gateways look up by token
// (spec Section 2).
type Sessions interface {
	// Issue gives an agent's session in a run two new tokens, which the
	// gateways know once Move puts the session in a round, and forgets the
	// session's earlier tokens. The session keeps its channel until it
	// ends.
	Issue(s *Session) Tokens
	// Move puts a session in a round, which counts from 1, until deadline,
	// in the process's clock, with the token budget the LLM gateway holds
	// the run to in that round.
	Move(run board.RunID, agent string, round int, deadline time.Time, budget int64) error
	// End ends an agent's session in a run, EndRun every session of a run,
	// and EndAll every session: their tokens are forgotten and their calls
	// end at once.
	End(run board.RunID, agent string)
	EndRun(run board.RunID)
	EndAll()
}
```

Create `internal/work/sessions/sessions.go`:

```go
// Package sessions keeps the live sessions of the runs one instance
// coordinates, in memory. The coordinator issues, moves and ends them; the
// gateways look a request's session up by its token, two or three times per
// tool call (spec Section 2). Only the tokens' hashes are kept, and a
// session lives no longer than the instance that issued it.
package sessions

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/tokens"
)

// Registry holds the live sessions. It implements app.Sessions for the
// coordinator, and each gateway's Sessions port.
type Registry struct {
	mu       sync.RWMutex
	sessions map[key]*live
	byToken  map[[32]byte]named
}

type key struct {
	run   board.RunID
	agent string
}

// live is a session as the registry holds it. Its round is 0 until the
// coordinator moves it into one.
type live struct {
	app.Session
	mcp, llm [32]byte
	round    int
	deadline time.Time
	budget   int64
	done     chan struct{}
}

// named is the session a token's hash names, and the token's gateway.
type named struct {
	s       *live
	gateway tokens.Gateway
}

// New returns a registry with no session.
func New() *Registry {
	return &Registry{sessions: map[key]*live{}, byToken: map[[32]byte]named{}}
}

// Issue gives an agent's session in a run two new tokens, which the
// gateways know once Move puts the session in a round. The session's earlier
// tokens, if it has any, are forgotten, so that a turn of an earlier round
// still running cannot act in the next; the session keeps its channel, which
// ends the calls of either tokens when the session ends.
func (r *Registry) Issue(s *app.Session) app.Tokens {
	t := app.Tokens{MCP: tokens.New(tokens.MCP), LLM: tokens.New(tokens.LLM)}
	l := &live{Session: *s, mcp: t.MCP.Hash(), llm: t.LLM.Hash(), done: make(chan struct{})}
	l.Item = maps.Clone(s.Item)
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key{s.Run, s.Agent}
	if old := r.sessions[k]; old != nil {
		delete(r.byToken, old.mcp)
		delete(r.byToken, old.llm)
		l.done = old.done
	}
	r.sessions[k] = l
	r.byToken[l.mcp] = named{l, tokens.MCP}
	r.byToken[l.llm] = named{l, tokens.LLM}
	return t
}

// Move puts a session in a round, which counts from 1, until deadline, in
// the process's clock, with the run's token budget as the LLM gateway holds
// it in that round. The coordinator moves a session once the round's board
// entry is committed, so that no session holds a round the board has not
// opened.
func (r *Registry) Move(run board.RunID, agent string, round int, deadline time.Time, budget int64) error {
	if round < 1 || deadline.IsZero() {
		return fmt.Errorf("sessions: a session moves into a round from 1, with a deadline, not round %d", round)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.sessions[key{run, agent}]
	if l == nil {
		return fmt.Errorf("sessions: %s has no session in run %s", agent, run)
	}
	l.round, l.deadline, l.budget = round, deadline, budget
	return nil
}

// End ends an agent's session in a run: its tokens are forgotten and its
// channel is closed, which ends its calls at once.
func (r *Registry) End(run board.RunID, agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.end(key{run, agent})
}

// EndRun ends every session of a run.
func (r *Registry) EndRun(run board.RunID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.sessions {
		if k.run == run {
			r.end(k)
		}
	}
}

// EndAll ends every session, as an instance that lost its lease does.
func (r *Registry) EndAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.sessions {
		r.end(k)
	}
}

func (r *Registry) end(k key) {
	l := r.sessions[k]
	if l == nil {
		return
	}
	delete(r.sessions, k)
	delete(r.byToken, l.mcp)
	delete(r.byToken, l.llm)
	close(l.done)
}

// lookup returns a copy of the session a token of gateway g names, once the
// session is in a round.
func (r *Registry) lookup(tok string, g tokens.Gateway) (live, bool) {
	h := tokens.Hash(tok)
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.byToken[h]
	if !ok || n.gateway != g || n.s.round == 0 {
		return live{}, false
	}
	return *n.s, true
}

// MCP returns the sessions as the MCP gateway looks them up.
func (r *Registry) MCP() actionsapp.Sessions { return mcpSessions{r} }

// LLM returns the sessions as the LLM gateway looks them up.
func (r *Registry) LLM() llmapp.Sessions { return llmSessions{r} }

type mcpSessions struct{ r *Registry }

func (m mcpSessions) Session(_ context.Context, tok string) (actionsapp.Session, error) {
	l, ok := m.r.lookup(tok, tokens.MCP)
	if !ok {
		return actionsapp.Session{}, actionsapp.ErrNoSession
	}
	return actionsapp.Session{
		Run: l.Run, Agent: l.Agent, Commit: l.Commit, Round: l.round, Deadline: l.deadline, Class: limits.Primary,
		Done: l.done, Item: maps.Clone(l.Item),
	}, nil
}

type llmSessions struct{ r *Registry }

func (m llmSessions) Session(_ context.Context, tok string) (llmapp.Session, error) {
	l, ok := m.r.lookup(tok, tokens.LLM)
	if !ok {
		return llmapp.Session{}, llmapp.ErrNoSession
	}
	return llmapp.Session{
		Run: string(l.Run), Agent: l.Agent, Commit: l.Commit, Round: l.round, Deadline: l.deadline, Class: cost.Primary,
		Budget: l.budget, Done: l.done,
	}, nil
}
```

- [ ] **Step 6: Both gateways redact any token; the MCP gateway hides its caller's own and keeps to the round**

Apply this change to `internal/actions/app/call.go`:

```diff
diff --git a/internal/actions/app/call.go b/internal/actions/app/call.go
index 6f28af6..14b2b36 100644
--- a/internal/actions/app/call.go
+++ b/internal/actions/app/call.go
@@ -4,10 +4,12 @@ import (
 	"cmp"
 	"context"
 	"encoding/json"
 	"errors"
 	"fmt"
+	"reflect"
+	"regexp"
 	"slices"
 	"strings"
 	"time"
 	"unicode/utf16"
 	"unicode/utf8"
@@ -49,21 +51,27 @@ func (g *Gateway) Call(ctx context.Context, token, name string, args json.RawMes
 	}
 	rev, err := g.p.Revisions.At(ctx, s.Commit)
 	if err != nil {
 		return nil, fmt.Errorf("app: revision %s: %w", s.Commit, err)
 	}
+	if !s.Deadline.IsZero() && !g.p.Now().Before(s.Deadline) {
+		// What the call did would count in no round, and the round's end
+		// may be recorded already.
+		rec := &CallRecord{ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Tool: cut(always(rev, ownToken(token)...).Text(name), maxNameBytes), Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now()}
+		return g.end(ctx, rec, StatusRefused, "the round has ended: your turn is over"), nil
+	}
 	ctx, cancel := g.callContext(ctx, s.Deadline, s.Done)
 	defer cancel()
 	list, err := g.list(ctx, &s)
 	if err != nil && ctx.Err() == nil {
 		return nil, err
 	}
 	i := slices.IndexFunc(list, func(t Tool) bool { return t.Name == name })
 	if i < 0 {
 		// The name is the agent's own text, so it is stored redacted, like
 		// any argument.
-		rec := &CallRecord{ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Tool: cut(always(rev).Text(name), maxNameBytes), Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now()}
+		rec := &CallRecord{ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Tool: cut(always(rev, ownToken(token)...).Text(name), maxNameBytes), Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now()}
 		if err != nil {
 			return g.end(ctx, rec, StatusReadError, ended(ctx)), nil
 		}
 		release, busy := g.flights.take(ctx, token, unknownFlight, g.opts.SessionCalls, g.opts.SessionCalls)
 		if busy != nil {
@@ -83,11 +91,11 @@ func (g *Gateway) Call(ctx context.Context, token, name string, args json.RawMes
 	if err != nil {
 		rec := &CallRecord{ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Server: ref.Server, Tool: ref.Tool, Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now()}
 		return g.end(ctx, rec, StatusReadError, "your other calls did not finish before the round ended"), nil
 	}
 	defer release()
-	return g.read(ctx, &read{rev: rev, ref: ref, class: s.Class, run: s.Run, agent: s.Agent, round: s.Round, args: args}), nil
+	return g.read(ctx, &read{rev: rev, ref: ref, class: s.Class, run: s.Run, agent: s.Agent, round: s.Round, args: args, token: token}), nil
 }
 
 // callContext ends at the earliest of the round's end, the call timeout and
 // the session's end, which it gives as the cause.
 func (g *Gateway) callContext(ctx context.Context, roundEnd time.Time, done <-chan struct{}) (context.Context, context.CancelFunc) {
@@ -128,19 +136,21 @@ type read struct {
 	run   board.RunID
 	agent string
 	round int
 	args  json.RawMessage
 	cause string // the write call a platform read is made for
+	// token is the token of the session the read is made for, or is empty.
+	token string
 }
 
 // read runs the read path in the order of spec Section 6.2: policy, server
 // limits, the audit record, forwarding, partialWhen and redaction, the
 // stored outcome, then the answer.
 func (g *Gateway) read(ctx context.Context, r *read) *Response {
 	srv := r.rev.Server(r.ref.Server)
 	policy := srv.Spec.Tools[r.ref.Tool]
-	red := redactor(r.rev, policy)
+	red := redactor(r.rev, policy, ownToken(r.token)...)
 	rec := &CallRecord{
 		ID: g.p.IDs.NewID(), Run: r.run, Agent: r.agent, Server: r.ref.Server, Tool: r.ref.Tool,
 		SchemaHash: policy.SchemaHash, Args: json.RawMessage("null"), Round: r.round, At: g.p.Now(), Cause: r.cause,
 	}
 	if policy.Effect != config.EffectRead {
@@ -165,10 +175,13 @@ func (g *Gateway) read(ctx context.Context, r *read) *Response {
 		err = v.schema.CheckBounds(args, policy.Bounds)
 	}
 	if err != nil {
 		return g.end(ctx, rec, StatusRefused, red.Text(err.Error()))
 	}
+	if why := tokenRefusal(args.Values(), r.token); why != "" {
+		return g.end(ctx, rec, StatusRefused, why)
+	}
 	if r.agent != "" {
 		if red, err = repeats(red, args.Values()); err != nil {
 			return g.end(ctx, rec, StatusRefused, err.Error())
 		}
 	}
@@ -366,14 +379,15 @@ func (g *Gateway) record(ctx context.Context, o *Outcome) bool {
 		}
 	}
 	return false
 }
 
-// redactor returns the redaction rules for a tool: the ones its policy
-// names, then the ones that apply always.
-func redactor(rev *config.Revision, policy *config.ToolPolicy) *redact.Redactor {
-	var rules []redact.Rule
+// redactor returns the redaction rules for a tool: the session tokens' rule
+// and those of extra, the ones its policy names, then the ones that apply
+// always.
+func redactor(rev *config.Revision, policy *config.ToolPolicy, extra ...redact.Rule) *redact.Redactor {
+	rules := append([]redact.Rule{redact.SessionTokens()}, extra...)
 	named := map[string]bool{}
 	for _, name := range policy.Redact {
 		if r := rev.RedactionRule(name); r != nil {
 			rules = append(rules, redact.FromConfig(r))
 			named[name] = true
@@ -469,14 +483,50 @@ func source(ref config.ToolRef) string { return ref.Server + "/" + ref.Tool }
 
 // maxNameBytes caps the stored name of a tool an agent asked for but cannot
 // see.
 const maxNameBytes = 128
 
+// tokenForm matches a session token whole.
+var tokenForm = regexp.MustCompile(`^` + config.SessionTokenPattern + `$`)
+
+// ownToken returns the rule that hides the letters and digits of the
+// caller's own token, in any case, which no prefix written otherwise hides;
+// none for a token not in a session token's form.
+func ownToken(token string) []redact.Rule {
+	if !tokenForm.MatchString(token) {
+		return nil
+	}
+	own := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(token[len("aswm_"):]))
+	return []redact.Rule{{Patterns: []*regexp.Regexp{own}, Replacement: config.SessionTokenReplacement}}
+}
+
+// tokensOf returns the rules that find a session token in arguments: any
+// session's, and the caller's own, as ownToken finds it.
+func tokensOf(token string) *redact.Redactor {
+	return redact.New(append([]redact.Rule{redact.SessionTokens()}, ownToken(token)...)...)
+}
+
+// tokenRefusal says why arguments are not sent when they hold a session
+// token, in any string, key or number, or an escape that may hide one: the
+// gateway sends a server the arguments as the agent wrote them, unredacted.
+// It returns nothing for arguments that hold neither; token is the caller's.
+func tokenRefusal(v any, token string) string {
+	red := tokensOf(token)
+	switch {
+	case !reflect.DeepEqual(red.Value(v), v):
+		return "the arguments hold a session token, which no server is sent"
+	case red.EscapedValue(v):
+		return "the arguments hold an escape that may hide a session token; write them plainly"
+	}
+	return ""
+}
+
 // always returns the redaction rules that apply always, for text that no
-// tool's rules cover.
-func always(rev *config.Revision) *redact.Redactor {
-	var rules []redact.Rule
+// tool's rules cover: the session tokens' rule and those of extra, then the
+// configuration's.
+func always(rev *config.Revision, extra ...redact.Rule) *redact.Redactor {
+	rules := append([]redact.Rule{redact.SessionTokens()}, extra...)
 	for _, r := range rev.AlwaysRedactionRules() {
 		rules = append(rules, redact.FromConfig(r))
 	}
 	return redact.New(rules...)
 }
```

Apply this change to `internal/actions/app/board.go`:

```diff
diff --git a/internal/actions/app/board.go b/internal/actions/app/board.go
index 7fa45c4..516d8e6 100644
--- a/internal/actions/app/board.go
+++ b/internal/actions/app/board.go
@@ -151,11 +151,11 @@ func openClaims(l board.Limits) int {
 // audited as every call is, with no server, so evidence can never cite it.
 // Text the agent writes for the board passes the rules that apply always
 // before it is stored (spec Section 9.4). A refusal is answered and leaves a
 // notice.
 func (g *Gateway) boardCall(ctx context.Context, token string, s *Session, rev *config.Revision, t *Tool, raw json.RawMessage) *Response {
-	red := always(rev)
+	red := always(rev, ownToken(token)...)
 	rec := &CallRecord{ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Tool: t.Name, Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now()}
 	release, err := g.flights.take(ctx, token, boardFlight, g.opts.SessionCalls, g.opts.SessionCalls)
 	if err != nil {
 		return g.endBoard(ctx, rec, StatusReadError, "", "your other calls did not finish before the round ended")
 	}
@@ -327,13 +327,14 @@ func inside(q, m string) bool {
 	}
 	return false
 }
 
 // markers returns what the revision's redaction rules put in place of the
-// text they hide, in each spelling an output may give it.
+// text they hide, and what replaces a session token, in each spelling an
+// output may give it.
 func markers(rev *config.Revision) []string {
-	var out []string
+	out := spellings(config.SessionTokenReplacement)
 	for _, o := range rev.Objects() {
 		if o.Kind != config.KindRedactionRule {
 			continue
 		}
 		if r := rev.RedactionRule(o.Name); r != nil {
```

Apply this change to `internal/actions/app/platform.go`:

```diff
diff --git a/internal/actions/app/platform.go b/internal/actions/app/platform.go
index b997067..b873508 100644
--- a/internal/actions/app/platform.go
+++ b/internal/actions/app/platform.go
@@ -130,11 +130,11 @@ func (g *Gateway) platformRead(ctx context.Context, c *platformCall) (*PlatformR
 	}
 	args, err := json.Marshal(v.schema.Coerce(rendered))
 	if err != nil {
 		return nil, fmt.Errorf("app: platform read: %w", err)
 	}
-	resp := g.read(ctx, &read{rev: c.rev, ref: ref, class: c.class, run: c.run, args: args, cause: c.cause})
+	resp := g.read(ctx, &read{rev: c.rev, ref: ref, class: c.class, run: c.run, args: args, cause: c.cause, token: c.token})
 	if resp.Status != StatusOK {
 		return nil, fmt.Errorf("%w: %s: %s", ErrReadFailed, ref, resp.Text)
 	}
 	doc, ok := output.Decode(resp.Text)
 	if !ok {
```

Apply this change to `internal/actions/app/write.go`:

```diff
diff --git a/internal/actions/app/write.go b/internal/actions/app/write.go
index c8bc93f..1d35b4c 100644
--- a/internal/actions/app/write.go
+++ b/internal/actions/app/write.go
@@ -106,11 +106,11 @@ type writeCall struct {
 // platform and charged to the session; the facts, until a reserve before the
 // call's end; the summary; then the proposal, committed apart from the
 // call's time, unless the run stopped. The call ends refused or recorded.
 func (w *writeCall) run(ctx context.Context, policy *config.ToolPolicy, raw json.RawMessage) *Response {
 	g, s := w.g, w.s
-	red := redactor(w.rev, policy)
+	red := redactor(w.rev, policy, ownToken(w.token)...)
 	v, refused := w.gate(ctx, red)
 	if refused != nil {
 		return refused
 	}
 	args, cited, err := v.schema.ReadWithout(raw, g.opts.MaxArgsBytes, hypothesisArg)
```

Apply this change to `internal/llm/app/gateway.go`:

```diff
diff --git a/internal/llm/app/gateway.go b/internal/llm/app/gateway.go
index 6745fa6..51c9a2e 100644
--- a/internal/llm/app/gateway.go
+++ b/internal/llm/app/gateway.go
@@ -1116,13 +1116,14 @@ func classOf(c cost.Class) limits.Class {
 func refusal(status int, kind, message string, retry bool) *Reply {
 	body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}})
 	return &Reply{Status: status, Header: map[string]string{"Content-Type": "application/json", "X-Should-Retry": strconv.FormatBool(retry)}, Body: body}
 }
 
-// always returns the redaction rules that apply always.
+// always returns the redaction rules that apply always: the session tokens'
+// rule, then the configuration's.
 func always(rev *config.Revision) *redact.Redactor {
-	var rules []redact.Rule
+	rules := []redact.Rule{redact.SessionTokens()}
 	for _, r := range rev.AlwaysRedactionRules() {
 		rules = append(rules, redact.FromConfig(r))
 	}
 	return redact.New(rules...)
 }
```

- [ ] **Step 7: Run the tests to see them pass**

Run: `go test -race ./internal/work/... ./internal/actions/... ./internal/llm/... ./internal/registry/... && golangci-lint run ./...`
Expected: every package `ok`; `0 issues.`

- [ ] **Step 8: Commit**

```bash
git add internal/work internal/actions internal/llm internal/registry
git commit -m "feat(work): session tokens, a registry of live sessions for both gateways, and a rule both gateways apply to any session's token"
```

### Task 5: Work records in Postgres, and the model calls a round's end reads

Decision 10. Work records in their own chained table, the runs' board contexts read from them, records in memory for the coordinator's tests, and two reads of the LLM gateway's records that a round's end needs. A record's body read from the table decodes to its version alone when it is of another version, so that the fold refuses it with `ErrVersion` rather than the read failing.

**Files:**
- Create: `internal/work/app/ports.go`, `internal/db/migrations/0007_work_records.sql`
- Create: `internal/work/postgres/records.go`, `internal/work/postgres/records_integration_test.go`
- Create: `internal/work/memory/records.go`, `internal/work/memory/records_test.go`
- Modify: `internal/work/item/item.go`, `internal/work/item/state_test.go`
- Modify: `internal/audit/postgres/chain.go`, `internal/audit/postgres/chain_integration_test.go`
- Modify: `internal/audit/postgres/models.go`, `internal/audit/postgres/models_integration_test.go`

**Interfaces:**
- Consumes: `item.Record`, `item.Draft`, `item.Body` (Task 3); the audit chain's functions (Plan 6a); `postgres.ModelCalls` (Plan 6b).
- Produces:
  - the ports `app.Records{Load(ctx, item.ID) ([]item.Record, error); Active(ctx) ([]item.ID, error); Append(ctx, item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error)}` and `app.Spend{Spent(ctx, run) (int64, error); SpentSince(ctx, since) (int64, error); InFlight(ctx, run) (int, error); Failed(ctx, run) ([]ModelCall, error)}`, with `app.ModelCall{ID, Agent, Status string}`;
  - `postgres.NewRecords(pool, tenant) *Records` and `postgres.NewRuns(pool, tenant) *Runs` (the board's `app.Runs`) in `internal/work/postgres`; `memory.NewRecords(now) *Records`, which is both `app.Records` and `app.Runs`;
  - `(*item.Body).UnmarshalJSON`;
  - `(*postgres.ModelCalls).InFlight` and `Failed` in `internal/audit/postgres`, which make it an `app.Spend`.

- [ ] **Step 1: Write the failing tests**

Create `internal/work/postgres/records_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var (
	_ app.Records = (*postgres.Records)(nil)
	_ app.Runs    = (*postgres.Runs)(nil)
)

// opened is an item's first record.
func opened(id item.ID, rec string) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Body: item.Body{V: item.Version, Opened: &item.Opened{
		Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow,
	}}}
}

// started is the record that starts an item.
func started(id item.ID, rec string) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Body: item.Body{V: item.Version, Started: &item.Started{
		Commit: "c1", Digest: "d1", Images: map[string]string{"thinloop": "sha256:aa"},
	}}}
}

// runCreated is the record that creates run for an item.
func runCreated(id item.ID, rec string, run board.RunID) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Run: run, Body: item.Body{V: item.Version, RunCreated: &item.RunCreated{
		Class: item.ClassPrimary, Mode: item.ModeVisible, Settings: board.Settings{Convergence: board.DefaultConvergence, Limits: board.Limits{EntriesPerRound: 3}},
		Budget: item.Budget{WallClock: 15 * time.Minute, Tokens: 2_000_000, Rounds: 6}, RoundLimit: 150 * time.Second, Agents: []string{"release-investigator"},
	}}}
}

func appendDrafts(t *testing.T, store *postgres.Records, id item.ID, drafts ...item.Draft) []item.Record {
	t.Helper()
	committed, err := store.Append(t.Context(), id, func([]item.Record) ([]item.Draft, error) { return drafts, nil })
	require.NoError(t, err)
	return committed
}

// An item's records come back as they were committed, numbered from 1 in
// commit order, with the database's time of their commit.
func TestRecordsRoundTrip(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-a")

	committed := appendDrafts(t, store, "item-1", opened("item-1", "r1"), started("item-1", "r2"))
	more := appendDrafts(t, store, "item-1", runCreated("item-1", "r3", "run-1"))
	loaded, err := store.Load(t.Context(), "item-1")

	require.NoError(t, err)
	require.Len(t, loaded, 3)
	assert.Equal(t, []int64{1, 2, 3}, []int64{loaded[0].Seq, loaded[1].Seq, loaded[2].Seq})
	assert.Equal(t, committed[1].Body, loaded[1].Body)
	assert.Equal(t, more[0].Body, loaded[2].Body)
	assert.Equal(t, item.IncidentID("inc-item-1"), loaded[2].Incident)
	assert.Equal(t, board.RunID("run-1"), loaded[2].Run)
	assert.True(t, committed[0].At.Equal(loaded[0].At))
	assert.False(t, loaded[2].At.Before(loaded[1].At))
	s, err := item.Fold(loaded)
	require.NoError(t, err)
	assert.Equal(t, item.StatusInvestigating, s.Status())
}

// A decision that fails commits nothing.
func TestAppendingCommitsNothingWhenDecideFails(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-a")
	refused := errors.New("refused by the rules")

	_, err := store.Append(t.Context(), "item-2", func([]item.Record) ([]item.Draft, error) { return nil, refused })

	require.ErrorIs(t, err, refused)
	loaded, err := store.Load(t.Context(), "item-2")
	require.NoError(t, err)
	assert.Empty(t, loaded)
}

// Appends to one item are serialized: each sees every record committed
// before it.
func TestAppendsToOneItemAreSerialized(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-a")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			_, err := store.Append(t.Context(), "item-3", func(history []item.Record) ([]item.Draft, error) {
				d := opened("item-3", fmt.Sprintf("c-%d", i))
				if len(history) > 0 {
					d.Body = item.Body{V: item.Version, AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}}
				}
				return []item.Draft{d}, nil
			})
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	loaded, err := store.Load(t.Context(), "item-3")
	require.NoError(t, err)
	require.Len(t, loaded, 20)
	assert.NotNil(t, loaded[0].Body.Opened, "only the first saw no history")
	for i := range loaded {
		assert.Equal(t, int64(i+1), loaded[i].Seq)
	}
}

// The coordinator drives the items that do not await their close.
func TestActiveItemsAreThoseNotAwaitingClose(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-active")
	appendDrafts(t, store, "item-a", opened("item-a", "a1"))
	appendDrafts(t, store, "item-b", opened("item-b", "b1"), started("item-b", "b2"))
	closing := opened("item-c", "c2")
	closing.Body = item.Body{V: item.Version, AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}}
	appendDrafts(t, store, "item-c", opened("item-c", "c1"), closing)

	active, err := store.Active(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []item.ID{"item-a", "item-b"}, active)
}

// A run's board context comes from its creation record: its settings, and
// human entries for the primary run that people see.
func TestARunsBoardContextComesFromItsCreation(t *testing.T) {
	pool := pgtest.Pool(t)
	store := postgres.NewRecords(pool, "records-runs")
	appendDrafts(t, store, "item-r", opened("item-r", "r1"), started("item-r", "r2"), runCreated("item-r", "r3", "run-r"))
	runs := postgres.NewRuns(pool, "records-runs")

	bc, err := runs.BoardContext(t.Context(), "run-r")
	require.NoError(t, err)
	_, unknown := runs.BoardContext(t.Context(), "run-404")

	assert.Equal(t, board.Context{Run: "run-r", HumanEntries: true, Settings: board.Settings{
		Convergence: board.DefaultConvergence, Limits: board.Limits{EntriesPerRound: 3},
	}}, bc)
	require.Error(t, unknown)
}

// Work records are audit records: append-only, and chained under their
// incident, so that an anchor names the incidents whose records it covers.
func TestWorkRecordsAreAppendOnlyAndChainedUnderTheirIncident(t *testing.T) {
	pool := pgtest.Pool(t)
	store := postgres.NewRecords(pool, "records-chain")
	appendDrafts(t, store, "item-x", opened("item-x", "x1"))

	for _, stmt := range []string{
		`UPDATE work_records SET kind = 'x' WHERE tenant_id = 'records-chain'`,
		`DELETE FROM work_records WHERE tenant_id = 'records-chain'`,
	} {
		_, err := pool.Exec(t.Context(), stmt)
		require.Error(t, err, stmt)
	}
	assert.Equal(t, []string{`work_records ["item-x", 1] incident:inc-item-x`}, chained(t, pool, "records-chain"))
}

// chained lists a tenant's chain as source, key and scope.
func chained(t *testing.T, pool *pgxpool.Pool, tenant string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT source || ' ' || key || ' ' || run_id FROM audit_chain WHERE tenant_id = $1 ORDER BY seq`, tenant)
	require.NoError(t, err)
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}
```

Create `internal/work/memory/records_test.go`:

```go
package memory_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
)

var _ app.Records = (*memory.Records)(nil)

func body(b item.Body) item.Body {
	b.V = item.Version
	return b
}

// An item's records come back numbered from 1, in commit order, with times
// that never go back; a decision that fails commits nothing.
func TestRecordsAreNumberedAndNeverGoBackInTime(t *testing.T) {
	clock := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	records := memory.NewRecords(func() time.Time { return clock })
	opened := item.Draft{ID: "r1", Item: "item-1", Incident: "inc-1", Body: body(item.Body{Opened: &item.Opened{Type: "incident"}})}

	first, err := records.Append(t.Context(), "item-1", func(history []item.Record) ([]item.Draft, error) {
		assert.Empty(t, history)
		return []item.Draft{opened}, nil
	})
	require.NoError(t, err)
	clock = clock.Add(-time.Hour)
	_, err = records.Append(t.Context(), "item-1", func(history []item.Record) ([]item.Draft, error) {
		assert.Len(t, history, 1)
		return nil, errors.New("refused")
	})
	require.Error(t, err)
	second, err := records.Append(t.Context(), "item-1", func([]item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: "r2", Item: "item-1", Incident: "inc-1", Body: body(item.Body{Started: &item.Started{}})}}, nil
	})
	require.NoError(t, err)

	loaded, err := records.Load(t.Context(), "item-1")
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, []int64{loaded[0].Seq, loaded[1].Seq})
	assert.True(t, second[0].At.After(first[0].At))
	_, err = records.Append(t.Context(), "item-1", func([]item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: "r3", Item: "item-2"}}, nil
	})
	require.Error(t, err, "a record of another item")
}

// The items that do not await their close are active, and a run's board
// context comes from its creation record.
func TestActiveItemsAndRunsComeFromTheRecords(t *testing.T) {
	records := memory.NewRecords(time.Now)
	add := func(id item.ID, run board.RunID, b item.Body) {
		_, err := records.Append(t.Context(), id, func(history []item.Record) ([]item.Draft, error) {
			return []item.Draft{{ID: string(id) + "-" + string(rune('a'+len(history))), Item: id, Incident: "inc", Run: run, Body: body(b)}}, nil
		})
		require.NoError(t, err)
	}
	add("item-b", "", item.Body{Opened: &item.Opened{}})
	add("item-a", "", item.Body{Opened: &item.Opened{}})
	add("item-a", "run-a", item.Body{RunCreated: &item.RunCreated{Class: item.ClassPrimary, Mode: item.ModeVisible,
		Settings: board.Settings{Convergence: board.DefaultConvergence}}})
	add("item-c", "", item.Body{Opened: &item.Opened{}})
	add("item-c", "", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}})

	active, err := records.Active(t.Context())
	require.NoError(t, err)
	bc, err := records.BoardContext(t.Context(), "run-a")
	require.NoError(t, err)
	_, unknown := records.BoardContext(t.Context(), "run-404")

	assert.Equal(t, []item.ID{"item-a", "item-b"}, active)
	assert.Equal(t, board.Context{Run: "run-a", HumanEntries: true, Settings: board.Settings{Convergence: board.DefaultConvergence}}, bc)
	require.Error(t, unknown)
}
```

Apply this change to `internal/work/item/state_test.go`:

```diff
diff --git a/internal/work/item/state_test.go b/internal/work/item/state_test.go
index 458de9a..6781d9b 100644
--- a/internal/work/item/state_test.go
+++ b/internal/work/item/state_test.go
@@ -66,10 +66,24 @@ func TestARecordOfAnotherVersionIsRefused(t *testing.T) {
 	_, err := item.Fold([]item.Record{r})
 
 	require.ErrorIs(t, err, item.ErrVersion)
 }
 
+// A body of another version decodes to its version alone, though the rest
+// of it no longer decodes, so that the fold refuses it with ErrVersion; a
+// body of this version that does not decode is an error.
+func TestABodyOfAnotherVersionDecodesToItsVersion(t *testing.T) {
+	var later item.Body
+	require.NoError(t, json.Unmarshal([]byte(`{"v": 2, "opened": "a later form"}`), &later))
+	assert.Equal(t, item.Body{V: 2}, later)
+	_, err := item.Fold([]item.Record{{Seq: 1, ID: "rec-1", Item: "item-1", Incident: "inc-1", Body: later}})
+	require.ErrorIs(t, err, item.ErrVersion)
+
+	var broken item.Body
+	require.Error(t, json.Unmarshal([]byte(`{"v": 1, "opened": "not an opening"}`), &broken))
+}
+
 // Records fold in commit order: a record out of order, or of another item,
 // is refused.
 func TestRecordsOfOneItemFoldInCommitOrder(t *testing.T) {
 	l := newLife(t, created())
 	stray := l.records[1]
```

Apply this change to `internal/audit/postgres/chain_integration_test.go`:

```diff
diff --git a/internal/audit/postgres/chain_integration_test.go b/internal/audit/postgres/chain_integration_test.go
index 8160ee5..ae18519 100644
--- a/internal/audit/postgres/chain_integration_test.go
+++ b/internal/audit/postgres/chain_integration_test.go
@@ -23,10 +23,11 @@ import (
 	"github.com/jchen0426/agent-swarm/internal/audit/dirstore"
 	"github.com/jchen0426/agent-swarm/internal/audit/postgres"
 	"github.com/jchen0426/agent-swarm/internal/db"
 	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
 	"github.com/jchen0426/agent-swarm/internal/work/board"
+	"github.com/jchen0426/agent-swarm/internal/work/item"
 	workpostgres "github.com/jchen0426/agent-swarm/internal/work/postgres"
 )
 
 var investigator = board.Author{Kind: board.AuthorAgent, Name: "flag-investigator"}
 
@@ -39,10 +40,21 @@ func post(t *testing.T, pool *pgxpool.Pool, tenant string, run board.RunID, id b
 	require.NoError(t, err)
 }
 
 func task(title string) *board.Body { return &board.Body{Task: &board.Task{Title: title}} }
 
+// workRecord opens an item of incident with its first work record.
+func workRecord(t *testing.T, pool *pgxpool.Pool, tenant string, id item.ID, incident item.IncidentID) {
+	t.Helper()
+	_, err := workpostgres.NewRecords(pool, tenant).Append(t.Context(), id, func([]item.Record) ([]item.Draft, error) {
+		return []item.Draft{{ID: "rec-" + string(id), Item: id, Incident: incident, Body: item.Body{V: item.Version, Opened: &item.Opened{
+			Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow,
+		}}}}, nil
+	})
+	require.NoError(t, err)
+}
+
 // call records a call of run and its outcome, through the audit store.
 func call(t *testing.T, pool *pgxpool.Pool, tenant, id string, run board.RunID, args, output string) {
 	t.Helper()
 	calls := postgres.NewCalls(pool, tenant)
 	rec := record(id)
@@ -116,35 +128,39 @@ func TestEveryAuditedRowIsChained(t *testing.T) {
 	const tenant = "chain-every"
 	post(t, pool, tenant, "run-a", "every-1", task("check the pool"))
 	post(t, pool, tenant, "run-a", "every-2", task("check the cache"))
 	call(t, pool, tenant, "every-c1", "run-b", `{"flag":"x"}`, `{"on":true}`)
 	hide(t, pool, tenant, "get_flag")
+	workRecord(t, pool, tenant, "item-1", "inc-1")
 	c := newChain(pool, tenant)
 
 	links := readChain(t, c)
 
 	var got []string
 	for i := range links {
 		got = append(got, links[i].Source+" "+links[i].Run)
 	}
-	assert.Equal(t, []string{"board_events run-a", "board_events run-a", "tool_calls run-b", "tool_call_outcomes run-b", "tool_hides "}, got)
+	assert.Equal(t, []string{"board_events run-a", "board_events run-a", "tool_calls run-b", "tool_call_outcomes run-b", "tool_hides ",
+		"work_records incident:inc-1"}, got)
 	assert.Equal(t, `["run-a", 2]`, links[1].Key)
-	assert.Equal(t, 5, checkRows(t, c))
+	assert.Equal(t, `["item-1", 1]`, links[5].Key)
+	assert.Equal(t, 6, checkRows(t, c))
 	read(t, c, func(v app.View) {
 		head, err := v.Head(t.Context())
 		require.NoError(t, err)
-		assert.Equal(t, links[4].Hash, head.Hash)
-		runs, err := v.Runs(t.Context(), 0, 5)
+		assert.Equal(t, links[5].Hash, head.Hash)
+		runs, err := v.Runs(t.Context(), 0, 6)
 		require.NoError(t, err)
-		assert.Equal(t, []string{"run-a", "run-b"}, runs)
+		assert.Equal(t, []string{"incident:inc-1", "run-a", "run-b"}, runs, "a work record's scope is its incident")
 		runs, err = v.Runs(t.Context(), 3, 5)
 		require.NoError(t, err)
 		assert.Equal(t, []string{"run-b"}, runs, "an outcome is its call's run")
 		counts, err := v.Counts(t.Context())
 		require.NoError(t, err)
 		assert.Equal(t, map[string]int64{"board_events": 2, "tool_calls": 1, "tool_call_outcomes": 1, "tool_hides": 1,
-			"config_revisions": 0, "config_objects": 0, "model_calls": 0, "model_call_contents": 0, "model_call_outcomes": 0}, counts)
+			"config_revisions": 0, "config_objects": 0, "model_calls": 0, "model_call_contents": 0, "model_call_outcomes": 0,
+			"work_records": 1}, counts)
 	})
 }
 
 // An empty chain has no head.
 func TestAnEmptyChainHasNoHead(t *testing.T) {
@@ -503,11 +519,11 @@ func TestARogueRecordLeavesAnchorsReadable(t *testing.T) {
 
 	an, err := a.Anchor(t.Context())
 
 	require.NoError(t, err)
 	assert.Equal(t, map[string]int64{"board_events": 1, "tool_calls": 0, "tool_call_outcomes": 0, "tool_hides": 0, "config_revisions": 0, "config_objects": 0,
-		"model_calls": 0, "model_call_contents": 0, "model_call_outcomes": 0}, an.Records)
+		"model_calls": 0, "model_call_contents": 0, "model_call_outcomes": 0, "work_records": 0}, an.Records)
 	assert.Equal(t, []string{"unknown_form X"}, findings(t, a))
 }
 
 // A trigger that takes the chain trigger's name, but fires before the row
 // is inserted and rewrites it, shows: the chain trigger is known by its
```

Apply this change to `internal/audit/postgres/models_integration_test.go`:

```diff
diff --git a/internal/audit/postgres/models_integration_test.go b/internal/audit/postgres/models_integration_test.go
index 0c66ede..323a6a8 100644
--- a/internal/audit/postgres/models_integration_test.go
+++ b/internal/audit/postgres/models_integration_test.go
@@ -13,12 +13,15 @@ import (
 
 	"github.com/jchen0426/agent-swarm/internal/audit/postgres"
 	"github.com/jchen0426/agent-swarm/internal/llm/app"
 	"github.com/jchen0426/agent-swarm/internal/llm/cost"
 	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
+	workapp "github.com/jchen0426/agent-swarm/internal/work/app"
 )
 
+var _ workapp.Spend = (*postgres.ModelCalls)(nil)
+
 const modelRequest = `{"max_tokens":1,"messages":[{"content":"hi","role":"user"}],"model":"claude-haiku-5-5"}`
 
 // modelRecord is a model call of run that began at start, with an
 // estimate of 5 weighted tokens.
 func modelRecord(id, run string, start time.Time) *app.CallRecord {
@@ -144,5 +147,37 @@ func TestModelCallsAreChained(t *testing.T) {
 	assert.Equal(t, []string{`model_calls run-m ["c-1"]`, `model_call_contents run-m ["c-1", "request"]`,
 		`model_call_outcomes run-m ["c-1"]`, `model_call_contents run-m ["c-1", "completion"]`,
 		`model_calls run-m ["c-2"]`, `model_call_outcomes run-m ["c-2"]`}, got)
 	assert.Equal(t, 6, checkRows(t, c))
 }
+
+// The coordinator reads how many of a run's calls have no outcome yet, and
+// the calls it notices on the run's board: those refused, failed or whose
+// outcome is unknown, in the order they began.
+func TestARunsCallsInFlightAndFailedAreRead(t *testing.T) {
+	pool := pgtest.Pool(t)
+	calls := postgres.NewModelCalls(pool, "models-failed")
+	ended := func(id, run string, status app.Status, start time.Time) {
+		require.NoError(t, calls.Begin(t.Context(), modelRecord(id, run, start)))
+		o := modelOutcome(id, 1, start.Add(time.Minute))
+		o.Status = status
+		require.NoError(t, calls.End(t.Context(), o))
+	}
+	ended("m-1", "run-1", app.StatusOK, at)
+	ended("m-2", "run-1", app.StatusRefused, at.Add(time.Second))
+	require.NoError(t, calls.Begin(t.Context(), modelRecord("m-3", "run-1", at.Add(2*time.Second))))
+	ended("m-4", "run-1", app.StatusError, at.Add(3*time.Second))
+	ended("m-5", "run-1", app.StatusUnknown, at.Add(4*time.Second))
+	ended("m-6", "run-2", app.StatusRefused, at.Add(5*time.Second))
+
+	inFlight, err := calls.InFlight(t.Context(), "run-1")
+	require.NoError(t, err)
+	failed, err := calls.Failed(t.Context(), "run-1")
+	require.NoError(t, err)
+
+	assert.Equal(t, 1, inFlight)
+	assert.Equal(t, []workapp.ModelCall{
+		{ID: "m-2", Agent: "flag-investigator", Status: "refused"},
+		{ID: "m-4", Agent: "flag-investigator", Status: "error"},
+		{ID: "m-5", Agent: "flag-investigator", Status: "unknown"},
+	}, failed)
+}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/work/memory/ ./internal/work/item/ && go test -tags integration ./internal/work/postgres/ ./internal/audit/postgres/ ./internal/db/`
Expected: FAIL: the build of `internal/work/memory` fails; `TestABodyOfAnotherVersionDecodesToItsVersion` fails, among others

- [ ] **Step 3: The ports, and a body of another version**

Create `internal/work/app/ports.go`:

```go
package app

import (
	"context"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Records keeps work items' records.
type Records interface {
	// Load returns an item's records in commit order.
	Load(ctx context.Context, id item.ID) ([]item.Record, error)
	// Active returns the items the coordinator drives: those that do not
	// await their close.
	Active(ctx context.Context) ([]item.ID, error)
	// Append serializes writers per item. decide sees every committed
	// record of the item and returns the records to commit; an error from
	// decide commits nothing and is returned as is.
	Append(ctx context.Context, id item.ID, decide func(history []item.Record) ([]item.Draft, error)) ([]item.Record, error)
}

// Spend is what the LLM gateway's records say of model calls, in
// cost-weighted tokens: a call counts what its outcome says it cost, and
// its worst case until its outcome is recorded.
type Spend interface {
	// Spent returns a run's spend.
	Spent(ctx context.Context, run string) (int64, error)
	// SpentSince returns the spend of the calls admitted at or after since.
	SpentSince(ctx context.Context, since time.Time) (int64, error)
	// InFlight counts a run's calls without an outcome.
	InFlight(ctx context.Context, run string) (int, error)
	// Failed returns a run's calls whose outcome is refused, error or
	// unknown, in the order they began.
	Failed(ctx context.Context, run string) ([]ModelCall, error)
}

// ModelCall is a model call as the coordinator notices it on the board.
type ModelCall struct {
	ID    string
	Agent string
	// Status is the call's outcome: refused, error or unknown.
	Status string
}
```

Apply this change to `internal/work/item/item.go`:

```diff
diff --git a/internal/work/item/item.go b/internal/work/item/item.go
index 80d982e..91b5bd7 100644
--- a/internal/work/item/item.go
+++ b/internal/work/item/item.go
@@ -3,10 +3,11 @@
 // that decide how a round ends and a run stops (spec Sections 4.1, 5.2 and
 // 5.6).
 package item
 
 import (
+	"encoding/json"
 	"errors"
 	"time"
 
 	"github.com/jchen0426/agent-swarm/internal/work/board"
 )
@@ -76,10 +77,33 @@ type Body struct {
 	RoundEnded    *RoundEnded    `json:"roundEnded,omitempty"`
 	RunStopped    *RunStopped    `json:"runStopped,omitempty"`
 	AwaitingClose *AwaitingClose `json:"awaitingClose,omitempty"`
 }
 
+// UnmarshalJSON decodes a body of this version whole, and of another only
+// its version, so that the fold refuses it with ErrVersion: a later version
+// may write a body this one cannot decode.
+func (b *Body) UnmarshalJSON(raw []byte) error {
+	var v struct {
+		V int `json:"v"`
+	}
+	if err := json.Unmarshal(raw, &v); err != nil {
+		return err
+	}
+	if v.V != Version {
+		*b = Body{V: v.V}
+		return nil
+	}
+	type body Body
+	var out body
+	if err := json.Unmarshal(raw, &out); err != nil {
+		return err
+	}
+	*b = Body(out)
+	return nil
+}
+
 // Kind returns the kind of record the body holds, or "" when it holds none
 // or more than one.
 func (b *Body) Kind() Kind {
 	var kind Kind
 	n := 0
```

- [ ] **Step 4: The table**

Create `internal/db/migrations/0007_work_records.sql`:

```sql
-- Work items' records (spec Sections 4.1 and 5): what the coordinator
-- decided, written before it acts, so that a restart resumes from them
-- (ADR-004). An item's records are numbered from 1 in commit order, and
-- recorded_at, the database's time of the commit, never goes back within an
-- item. A record names the item's incident as the item had it when the
-- record was written, and its run when it is about one ('' otherwise). Its
-- body is JSON with a version. Work records are audit records from their
-- first row: append-only, deleted only by the retention job, and chained
-- under their incident, so that an anchor names the incidents whose records
-- the chain gained (spec Section 9.4).
CREATE TABLE work_records (
    tenant_id   text        NOT NULL,
    item_id     text        NOT NULL,
    seq         bigint      NOT NULL CHECK (seq > 0),
    id          text        NOT NULL,
    incident_id text        NOT NULL CHECK (incident_id <> ''),
    run_id      text        NOT NULL,
    kind        text        NOT NULL,
    body        jsonb       NOT NULL,
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, item_id, seq),
    UNIQUE (tenant_id, id)
);

-- A run's records, as a board reads its run's settings.
CREATE INDEX work_records_by_run ON work_records (tenant_id, run_id, kind) WHERE run_id <> '';

-- Records by kind, as a takeover finds the items that do not await their
-- close: one item_opened row per item, and no item_awaiting_close row.
CREATE INDEX work_records_by_kind ON work_records (tenant_id, kind, item_id);

CREATE TRIGGER work_records_append_only
    BEFORE UPDATE OR DELETE ON work_records
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER work_records_no_truncate
    BEFORE TRUNCATE ON work_records
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();

-- The table's key, and its columns in form 1, as migration 0005 renders the
-- other audited tables. Verification sends the same expressions itself.
CREATE FUNCTION audit_key(r work_records) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.item_id, r.seq)::text $$;

CREATE FUNCTION audit_form1(r work_records) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.item_id, r.seq, r.id, r.incident_id, r.run_id, r.kind, r.body,
                               (extract(epoch FROM r.recorded_at) * 1000000)::bigint)::text $$;

-- A work record's scope in the chain is its incident. Run ids hold no colon,
-- so no run's scope is an incident's.
CREATE FUNCTION audit_run(r work_records) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT 'incident:' || r.incident_id $$;

CREATE FUNCTION audit_chain_work_records() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'work_records', audit_key(NEW), audit_run(NEW), 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

-- Every function resolves names in the system catalog first, then in the
-- schema that holds these tables, whatever the caller's search path.
DO $$
DECLARE
    f regprocedure;
BEGIN
    FOR f IN
        SELECT p.oid::regprocedure FROM pg_proc p
        WHERE p.pronamespace = (SELECT n.oid FROM pg_namespace n WHERE n.nspname = current_schema())
          AND (p.proname = 'audit_chain_work_records'
               OR p.proname IN ('audit_key', 'audit_form1', 'audit_run') AND p.proargtypes[0] = 'work_records'::regtype)
    LOOP
        EXECUTE format('ALTER FUNCTION %s SET search_path = pg_catalog, %I', f, current_schema());
    END LOOP;
END;
$$;

CREATE TRIGGER work_records_chain AFTER INSERT ON work_records
    FOR EACH ROW EXECUTE FUNCTION audit_chain_work_records();
```

- [ ] **Step 5: The stores**

Create `internal/work/postgres/records.go`:

```go
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Records keeps work items' records in the work_records table. It
// implements app.Records.
type Records struct {
	pool   *pgxpool.Pool
	tenant string
}

// NewRecords returns the store of one tenant's work records.
func NewRecords(pool *pgxpool.Pool, tenant string) *Records {
	return &Records{pool: pool, tenant: tenant}
}

const selectRecords = `
SELECT seq, id, incident_id, run_id, recorded_at, body
FROM work_records
WHERE tenant_id = $1 AND item_id = $2
ORDER BY seq`

const insertRecord = `
INSERT INTO work_records (tenant_id, item_id, seq, id, incident_id, run_id, kind, body, recorded_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, greatest(clock_timestamp(), $9::timestamptz))
RETURNING recorded_at`

// Load returns an item's records in commit order.
func (s *Records) Load(ctx context.Context, id item.ID) ([]item.Record, error) {
	return loadRecords(ctx, s.pool, s.tenant, id)
}

// Active returns the items that do not await their close, by id: each
// item's opening, read by kind, without a record that it awaits its close.
func (s *Records) Active(ctx context.Context) ([]item.ID, error) {
	rows, err := s.pool.Query(ctx, `
SELECT o.item_id FROM work_records o
WHERE o.tenant_id = $1 AND o.kind = $2 AND NOT EXISTS (
    SELECT 1 FROM work_records c WHERE c.tenant_id = o.tenant_id AND c.kind = $3 AND c.item_id = o.item_id)
ORDER BY o.item_id`, s.tenant, string(item.KindOpened), string(item.KindAwaitingClose))
	if err != nil {
		return nil, fmt.Errorf("query active items: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[item.ID])
	if err != nil {
		return nil, fmt.Errorf("read active items: %w", err)
	}
	return ids, nil
}

// Append commits the records that decide returns, after the item's last.
// A transaction-scoped advisory lock serializes appends per item, so decide
// always sees the latest history, and record times come from the database
// clock, never going back within an item. The transaction runs at read
// committed, as the board's appends do.
func (s *Records) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) (_ []item.Record, err error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin append: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "work_records/"+s.tenant+"/"+string(id)); err != nil {
		return nil, fmt.Errorf("lock item %s: %w", id, err)
	}
	history, err := loadRecords(ctx, tx, s.tenant, id)
	if err != nil {
		return nil, err
	}
	drafts, err := decide(history)
	if err != nil {
		return nil, err
	}
	var lastSeq int64
	var lastAt time.Time
	if n := len(history); n > 0 {
		lastSeq, lastAt = history[n-1].Seq, history[n-1].At
	}
	committed := make([]item.Record, 0, len(drafts))
	for i := range drafts {
		d := &drafts[i]
		if d.Item != id {
			return nil, fmt.Errorf("record %s belongs to item %s, not %s", d.ID, d.Item, id)
		}
		r := item.Record{Seq: lastSeq + 1, ID: d.ID, Item: id, Incident: d.Incident, Run: d.Run, Body: d.Body}
		err = tx.QueryRow(ctx, insertRecord, s.tenant, string(id), r.Seq, r.ID, string(r.Incident), string(r.Run),
			string(r.Body.Kind()), r.Body, lastAt).Scan(&r.At)
		if err != nil {
			return nil, fmt.Errorf("insert record %s: %w", d.ID, err)
		}
		lastSeq, lastAt = r.Seq, r.At
		committed = append(committed, r)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit append: %w", err)
	}
	return committed, nil
}

func loadRecords(ctx context.Context, q querier, tenant string, id item.ID) ([]item.Record, error) {
	rows, err := q.Query(ctx, selectRecords, tenant, string(id))
	if err != nil {
		return nil, fmt.Errorf("query records of item %s: %w", id, err)
	}
	records, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (item.Record, error) {
		r := item.Record{Item: id}
		var incident, run string
		if err := row.Scan(&r.Seq, &r.ID, &incident, &run, &r.At, &r.Body); err != nil {
			return item.Record{}, err
		}
		r.Incident, r.Run = item.IncidentID(incident), board.RunID(run)
		return r, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read records of item %s: %w", id, err)
	}
	return records, nil
}

// Runs tells the board rules what a run's records say of it. It implements
// the Work application's Runs port.
type Runs struct {
	pool   *pgxpool.Pool
	tenant string
}

// NewRuns returns the runs of one tenant's work records.
func NewRuns(pool *pgxpool.Pool, tenant string) *Runs {
	return &Runs{pool: pool, tenant: tenant}
}

// BoardContext returns a run's board context: the settings its creation
// record holds, and human entries for a primary run people see. Its round
// is the board's to say.
func (r *Runs) BoardContext(ctx context.Context, run board.RunID) (board.Context, error) {
	var raw []byte
	// run_id <> '' lets a generic plan use the partial index on runs.
	err := r.pool.QueryRow(ctx, `SELECT body FROM work_records WHERE tenant_id = $1 AND run_id = $2 AND run_id <> '' AND kind = $3`,
		r.tenant, string(run), string(item.KindRunCreated)).Scan(&raw)
	if err != nil {
		return board.Context{}, fmt.Errorf("read the creation of run %s: %w", run, err)
	}
	var b item.Body
	if err := json.Unmarshal(raw, &b); err != nil || b.RunCreated == nil {
		return board.Context{}, fmt.Errorf("read the creation of run %s: not a run's creation", run)
	}
	rc := b.RunCreated
	return board.Context{Run: run, HumanEntries: rc.Class == item.ClassPrimary && rc.Mode == item.ModeVisible, Settings: rc.Settings}, nil
}
```

Create `internal/work/memory/records.go`:

```go
package memory

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Records is an in-memory app.Records, and the app.Runs its run records
// make. One lock serializes appends, and record times never go backwards
// within an item, as in the Postgres store.
type Records struct {
	mu      sync.Mutex
	now     func() time.Time
	records map[item.ID][]item.Record
}

// NewRecords returns an empty store whose record times come from now.
func NewRecords(now func() time.Time) *Records {
	return &Records{now: now, records: map[item.ID][]item.Record{}}
}

// Load returns an item's records in commit order.
func (s *Records) Load(_ context.Context, id item.ID) ([]item.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records[id]), nil
}

// Active returns the items that do not await their close, by id.
func (s *Records) Active(context.Context) ([]item.ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []item.ID
	for id, records := range s.records {
		if !slices.ContainsFunc(records, func(r item.Record) bool { return r.Body.AwaitingClose != nil }) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Append commits the records that decide returns, after the item's last.
func (s *Records) Append(_ context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.records[id]
	drafts, err := decide(slices.Clone(history))
	if err != nil {
		return nil, err
	}
	var last time.Time
	if len(history) > 0 {
		last = history[len(history)-1].At
	}
	committed := make([]item.Record, 0, len(drafts))
	for i := range drafts {
		d := &drafts[i]
		if d.Item != id {
			return nil, fmt.Errorf("record %s belongs to item %s, not %s", d.ID, d.Item, id)
		}
		at := s.now().UTC()
		if !at.After(last) {
			at = last.Add(time.Microsecond)
		}
		last = at
		committed = append(committed, item.Record{
			Seq: int64(len(history) + len(committed) + 1), ID: d.ID, Item: id, Incident: d.Incident, Run: d.Run, At: at, Body: d.Body,
		})
	}
	s.records[id] = append(history, committed...)
	return committed, nil
}

// BoardContext returns a run's board context from its creation record.
func (s *Records) BoardContext(_ context.Context, run board.RunID) (board.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, records := range s.records {
		for i := range records {
			if rc := records[i].Body.RunCreated; rc != nil && records[i].Run == run {
				return board.Context{Run: run, HumanEntries: rc.Class == item.ClassPrimary && rc.Mode == item.ModeVisible, Settings: rc.Settings}, nil
			}
		}
	}
	return board.Context{}, fmt.Errorf("no run %s", run)
}
```

- [ ] **Step 6: Verification reads work records, and the round's end reads model calls**

Apply this change to `internal/audit/postgres/chain.go`:

```diff
diff --git a/internal/audit/postgres/chain.go b/internal/audit/postgres/chain.go
index 7b86bcf..8974365 100644
--- a/internal/audit/postgres/chain.go
+++ b/internal/audit/postgres/chain.go
@@ -73,10 +73,15 @@ var sources = []source{
 		key: `jsonb_build_array(t.call_id)::text`,
 		row: `jsonb_build_array(t.tenant_id, t.call_id, t.status, t.reason, t.input_tokens, t.cache_write_5m_tokens,
 			t.cache_write_1h_tokens, t.cache_read_tokens, t.output_tokens, t.spend, t.weighted,
 			t.attempts, t.details, (extract(epoch FROM t.ended_at) * 1000000)::bigint)::text`,
 	}}},
+	{"work_records", map[int16]form{1: {
+		key: `jsonb_build_array(t.item_id, t.seq)::text`,
+		row: `jsonb_build_array(t.tenant_id, t.item_id, t.seq, t.id, t.incident_id, t.run_id, t.kind, t.body,
+			(extract(epoch FROM t.recorded_at) * 1000000)::bigint)::text`,
+	}}},
 }
 
 // Chain reads one tenant's audit chain and the rows it covers, in the
 // schema that holds the platform's tables. It implements the Audit
 // application's Chain port.
```

Apply this change to `internal/audit/postgres/models.go`:

```diff
diff --git a/internal/audit/postgres/models.go b/internal/audit/postgres/models.go
index 83e3d93..d70a11c 100644
--- a/internal/audit/postgres/models.go
+++ b/internal/audit/postgres/models.go
@@ -7,10 +7,11 @@ import (
 
 	"github.com/jackc/pgx/v5"
 	"github.com/jackc/pgx/v5/pgxpool"
 
 	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
+	workapp "github.com/jchen0426/agent-swarm/internal/work/app"
 )
 
 // ModelCalls records model calls in model_calls, their requests and
 // completions in model_call_contents, and how they ended in
 // model_call_outcomes, and counts what they spent. It implements the LLM
@@ -131,5 +132,34 @@ WHERE c.tenant_id = $1 AND c.started_at >= $2`, s.tenant, since).Scan(&n)
 	if err != nil {
 		return 0, fmt.Errorf("count the spend since %s: %w", since.Format(time.RFC3339), err)
 	}
 	return n, nil
 }
+
+// InFlight counts a run's calls that have no outcome yet.
+func (s *ModelCalls) InFlight(ctx context.Context, run string) (int, error) {
+	var n int
+	err := s.pool.QueryRow(ctx, `
+SELECT count(*) FROM model_calls c LEFT JOIN model_call_outcomes o USING (tenant_id, call_id)
+WHERE c.tenant_id = $1 AND c.run_id = $2 AND o.call_id IS NULL`, s.tenant, run).Scan(&n)
+	if err != nil {
+		return 0, fmt.Errorf("count the calls of run %s in flight: %w", run, err)
+	}
+	return n, nil
+}
+
+// Failed returns a run's calls whose outcome is refused, error or unknown,
+// in the order they began, as the coordinator notices them on the board.
+func (s *ModelCalls) Failed(ctx context.Context, run string) ([]workapp.ModelCall, error) {
+	rows, err := s.pool.Query(ctx, `
+SELECT c.call_id, c.agent, o.status FROM model_calls c JOIN model_call_outcomes o USING (tenant_id, call_id)
+WHERE c.tenant_id = $1 AND c.run_id = $2 AND o.status <> 'ok'
+ORDER BY c.started_at, c.call_id`, s.tenant, run)
+	if err != nil {
+		return nil, fmt.Errorf("read the failed calls of run %s: %w", run, err)
+	}
+	calls, err := pgx.CollectRows(rows, pgx.RowToStructByPos[workapp.ModelCall])
+	if err != nil {
+		return nil, fmt.Errorf("read the failed calls of run %s: %w", run, err)
+	}
+	return calls, nil
+}
```

- [ ] **Step 7: Run the tests to see them pass**

Run: `go test ./internal/work/memory/ ./internal/work/item/ && go test -tags integration ./internal/work/postgres/ ./internal/audit/postgres/ ./internal/db/ && golangci-lint run ./...`
Expected: every package `ok`; `0 issues.`

- [ ] **Step 8: Commit**

```bash
git add internal/work internal/db/migrations internal/audit/postgres
git commit -m "feat(work): work records in Postgres, chained under their incident, a run's board context from them, and the model calls a round's end reads"
```

### Task 6: The lease

Decision 3. The platform's lease in the database, and the stores that write under it: each write checks, in its transaction, that its holding's epoch still holds the lease. A takeover reads when the holding before it expired, which the coordinator's takeover reads (Task 8).

**Files:**
- Create: `internal/db/migrations/0008_leases.sql`
- Create: `internal/work/postgres/lease.go`, `internal/work/postgres/lease_integration_test.go`
- Modify: `internal/work/app/ports.go`, `internal/work/postgres/records.go`, `internal/work/postgres/events.go`

**Interfaces:**
- Consumes: `postgres.Records` (Task 5), `postgres.EventStore` (Plan 1).
- Produces:
  - `app.ErrLeaseLost`; the port `app.Lease{Take(ctx) (Term, bool, error); Renew(ctx) error; Release(ctx) error; TTL() time.Duration}`, with `app.Term{Epoch int64; At time.Time; Previous time.Time}`: `Previous` is when the holding before expired or was released, zero for the lease's first;
  - `postgres.Guard{Check(ctx, pgx.Tx) error}`; `postgres.NewLease(pool, tenant, name, holder string, ttl time.Duration) *Lease`, which is an `app.Lease` and a `Guard`;
  - `(*postgres.Records).Guarded(Guard) *Records` and `(*postgres.EventStore).Guarded(Guard) *EventStore`.

- [ ] **Step 1: Write the failing tests**

Create `internal/work/postgres/lease_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var _ app.Lease = (*postgres.Lease)(nil)

func take(t *testing.T, l *postgres.Lease) app.Term {
	t.Helper()
	term, took, err := l.Take(t.Context())
	require.NoError(t, err)
	require.True(t, took)
	return term
}

// One instance holds the lease at a time. Releasing it lets another take
// it at once, with a new epoch.
func TestTheLeaseIsHeldByOneInstanceAtATime(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-one", "platform", "a", time.Minute)
	b := postgres.NewLease(pool, "lease-one", "platform", "b", time.Minute)

	first := take(t, a)
	_, took, err := b.Take(t.Context())
	require.NoError(t, err)
	require.NoError(t, a.Renew(t.Context()))
	require.NoError(t, a.Release(t.Context()))
	second := take(t, b)

	assert.False(t, took, "a holds it")
	assert.Equal(t, int64(1), first.Epoch)
	assert.Equal(t, int64(2), second.Epoch)
	assert.WithinDuration(t, time.Now(), first.At, time.Minute, "the database's time")
	require.ErrorIs(t, a.Renew(t.Context()), app.ErrLeaseLost, "a released it")
}

// A holding that expired is taken over with a new epoch, and the instance
// that held it can neither renew it nor write.
func TestAnExpiredLeaseIsTakenOverAndItsHolderFenced(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-expired", "platform", "a", 300*time.Millisecond)
	b := postgres.NewLease(pool, "lease-expired", "platform", "b", time.Minute)
	take(t, a)
	time.Sleep(400 * time.Millisecond)

	_, err := postgres.NewRecords(pool, "lease-expired").Guarded(a).Append(t.Context(), "item-1", drafting(opened("item-1", "r0")))
	require.ErrorIs(t, err, app.ErrLeaseLost, "an expired holding writes nothing, even before a takeover")
	term := take(t, b)

	assert.Equal(t, int64(2), term.Epoch)
	require.ErrorIs(t, a.Renew(t.Context()), app.ErrLeaseLost)
	_, err = postgres.NewRecords(pool, "lease-expired").Guarded(a).Append(t.Context(), "item-1", drafting(opened("item-1", "r1")))
	require.ErrorIs(t, err, app.ErrLeaseLost)
	_, err = postgres.NewEventStore(pool, "lease-expired").Guarded(a).Append(t.Context(), "run-1", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{evidenceDraft("run-1", "le-1", "v42")}, nil
	})
	require.ErrorIs(t, err, app.ErrLeaseLost)
	_, err = postgres.NewRecords(pool, "lease-expired").Guarded(b).Append(t.Context(), "item-1", drafting(opened("item-1", "r2")))
	require.NoError(t, err)
	records, err := postgres.NewRecords(pool, "lease-expired").Load(t.Context(), "item-1")
	require.NoError(t, err)
	assert.Len(t, records, 1, "only the holder's record")
}

// A holding knows when the one before it expired, in the database's time,
// so that a takeover tells a round whose deadline passed while no instance
// held the lease from one whose holder died after its deadline; the first
// holding has none. The lease says how long a holding lasts.
func TestAHoldingKnowsWhenTheOneBeforeItExpired(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-previous", "platform", "a", 300*time.Millisecond)
	b := postgres.NewLease(pool, "lease-previous", "platform", "b", time.Minute)
	first := take(t, a)
	time.Sleep(400 * time.Millisecond)

	second := take(t, b)
	require.NoError(t, b.Release(t.Context()))
	third := take(t, a)

	assert.True(t, first.Previous.IsZero())
	assert.WithinDuration(t, first.At.Add(300*time.Millisecond), second.Previous, 50*time.Millisecond)
	assert.True(t, second.Previous.Before(second.At))
	assert.True(t, third.Previous.After(second.At), "a release ends the holding at once")
	assert.True(t, third.Previous.Before(third.At))
	assert.Equal(t, time.Minute, b.TTL())
}

// A takeover waits for the writes of the last holding still in flight, and
// fences the rest: a write that checked the lease before the takeover
// commits first, and none after it does.
func TestATakeoverWaitsForWritesInFlight(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-flight", "platform", "a", 300*time.Millisecond)
	b := postgres.NewLease(pool, "lease-flight", "platform", "b", time.Minute)
	take(t, a)
	inside, release := make(chan struct{}), make(chan struct{})
	appended := make(chan error, 1)
	go func() {
		_, err := postgres.NewRecords(pool, "lease-flight").Guarded(a).Append(context.Background(), "item-1", func([]item.Record) ([]item.Draft, error) {
			close(inside)
			<-release
			return []item.Draft{opened("item-1", "r1")}, nil
		})
		appended <- err
	}()
	<-inside
	time.Sleep(400 * time.Millisecond)
	taken := make(chan app.Term, 1)
	go func() {
		term, _, _ := b.Take(context.Background())
		taken <- term
	}()

	require.Never(t, func() bool { return len(taken) > 0 }, 300*time.Millisecond, 10*time.Millisecond, "the takeover waits for the write in flight")
	close(release)
	require.NoError(t, <-appended)
	term := <-taken

	assert.Equal(t, int64(2), term.Epoch)
	_, err := postgres.NewRecords(pool, "lease-flight").Guarded(a).Append(t.Context(), "item-1", drafting(opened("item-1", "r2")))
	require.ErrorIs(t, err, app.ErrLeaseLost)
}

// Renewing the lease waits for no write: writes take the lease's row in
// key share, which a renewal does not wait for.
func TestRenewalWaitsForNoWrite(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-renew", "platform", "a", time.Minute)
	take(t, a)
	inside, release := make(chan struct{}), make(chan struct{})
	appended := make(chan error, 1)
	go func() {
		_, err := postgres.NewEventStore(pool, "lease-renew").Guarded(a).Append(context.Background(), "run-1", func([]board.Event) ([]board.Draft, error) {
			close(inside)
			<-release
			return []board.Draft{evidenceDraft("run-1", "rn-1", "v42")}, nil
		})
		appended <- err
	}()
	<-inside

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err := a.Renew(ctx)
	close(release)

	require.NoError(t, err, "the renewal did not wait for the write")
	require.NoError(t, <-appended)
}

// A store that writes under the lease writes nothing before it is taken.
func TestAGuardedStoreWritesNothingBeforeTheLeaseIsTaken(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-none", "platform", "a", time.Minute)

	_, err := postgres.NewRecords(pool, "lease-none").Guarded(a).Append(t.Context(), "item-1", drafting(opened("item-1", "r1")))

	require.ErrorIs(t, err, app.ErrLeaseLost)
	assertNoRecords(t, pool, "lease-none")
}

func drafting(drafts ...item.Draft) func([]item.Record) ([]item.Draft, error) {
	return func([]item.Record) ([]item.Draft, error) { return drafts, nil }
}

func assertNoRecords(t *testing.T, pool *pgxpool.Pool, tenant string) {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM work_records WHERE tenant_id = $1`, tenant).Scan(&n))
	assert.Zero(t, n)
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -tags integration ./internal/work/postgres/ ./internal/db/`
Expected: FAIL: the build of `internal/work/postgres` fails (undefined: app.Lease)

- [ ] **Step 3: The port**

Apply this change to `internal/work/app/ports.go`:

```diff
diff --git a/internal/work/app/ports.go b/internal/work/app/ports.go
index 5eca703..477e85a 100644
--- a/internal/work/app/ports.go
+++ b/internal/work/app/ports.go
@@ -1,9 +1,10 @@
 package app
 
 import (
 	"context"
+	"errors"
 	"time"
 
 	"github.com/jchen0426/agent-swarm/internal/work/item"
 )
 
@@ -40,5 +41,39 @@ type ModelCall struct {
 	ID    string
 	Agent string
 	// Status is the call's outcome: refused, error or unknown.
 	Status string
 }
+
+// ErrLeaseLost marks a write refused, or a renewal failed, because the
+// instance no longer holds the lease: it expired, or another instance took
+// it over.
+var ErrLeaseLost = errors.New("app: the lease is lost")
+
+// Lease is the platform's lease: one instance holds it at a time, and only
+// the holder acts (spec Section 8.2; ADR-004). Every write of the holder
+// checks it in its transaction, so that a holder that lost it writes
+// nothing more.
+type Lease interface {
+	// Take takes the lease when no one holds it, and reports whether it
+	// did. A new holding has a new epoch, and taking it waits for the
+	// writes of the last holding still in flight.
+	Take(ctx context.Context) (Term, bool, error)
+	// Renew extends the holding. It fails with ErrLeaseLost once the
+	// holding expired or was taken over.
+	Renew(ctx context.Context) error
+	// Release gives the lease up, so that another instance may take it at
+	// once.
+	Release(ctx context.Context) error
+	// TTL is how long a holding lasts from its last renewal.
+	TTL() time.Duration
+}
+
+// Term is a holding of the lease: its epoch, and the database's time when
+// it was taken.
+type Term struct {
+	Epoch int64
+	At    time.Time
+	// Previous is when the holding before it expired, or was released, in
+	// the database's time; zero for the lease's first holding.
+	Previous time.Time
+}
```

- [ ] **Step 4: The table and the lease**

Create `internal/db/migrations/0008_leases.sql`:

```sql
-- Leases (spec Section 8.2; ADR-004): one instance holds a lease at a time,
-- and only the holder acts. A holding has an epoch, which only grows, and
-- an expiry the holder renews. A lease's row is not an audit record: it
-- changes with every renewal.
--
-- Every write transaction of the holder takes the row in key share, as it
-- checks that its epoch still holds the lease. A renewal changes the expiry
-- only, which takes the row in no-key update: it never waits for a write. A
-- takeover changes the epoch, a column of a unique key, which takes the row
-- for update: it waits for the writes in flight, and once it commits, no
-- write of the last holding passes the check.
CREATE TABLE leases (
    tenant_id  text        NOT NULL,
    name       text        NOT NULL,
    epoch      bigint      NOT NULL CHECK (epoch > 0),
    holder     text        NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, name),
    UNIQUE (tenant_id, name, epoch)
);
```

Create `internal/work/postgres/lease.go`:

```go
package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// Guard checks, inside a write transaction, that the writer may write.
type Guard interface {
	Check(ctx context.Context, tx pgx.Tx) error
}

// Lease is a lease in the leases table, as one instance holds it. It
// implements app.Lease, and is the Guard of the stores that write under it.
type Lease struct {
	pool   *pgxpool.Pool
	tenant string
	name   string
	holder string
	ttl    time.Duration
	// epoch is the epoch of the instance's holding, or 0 when it holds
	// none.
	epoch atomic.Int64
}

// NewLease returns lease name of a tenant, as holder takes it for ttl at a
// time.
func NewLease(pool *pgxpool.Pool, tenant, name, holder string, ttl time.Duration) *Lease {
	return &Lease{pool: pool, tenant: tenant, name: name, holder: holder, ttl: ttl}
}

// Take takes the lease when it was never taken or its holding expired, and
// reads when that holding expired. A takeover waits for the writes of the
// last holding in flight: reading it for update, as changing the epoch
// does, takes the row in a mode their key share holds off.
func (l *Lease) Take(ctx context.Context) (_ app.Term, _ bool, err error) {
	tx, err := l.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return app.Term{}, false, fmt.Errorf("begin taking the lease: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var term app.Term
	err = tx.QueryRow(ctx, `
INSERT INTO leases (tenant_id, name, epoch, holder, expires_at) VALUES ($1, $2, 1, $3, clock_timestamp() + $4::interval)
ON CONFLICT DO NOTHING RETURNING epoch, clock_timestamp()`, l.tenant, l.name, l.holder, l.ttl).Scan(&term.Epoch, &term.At)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
SELECT expires_at FROM leases WHERE tenant_id = $1 AND name = $2 AND expires_at <= clock_timestamp() FOR UPDATE`,
			l.tenant, l.name).Scan(&term.Previous)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.Term{}, false, nil
		}
		if err == nil {
			err = tx.QueryRow(ctx, `
UPDATE leases SET epoch = epoch + 1, holder = $3, expires_at = clock_timestamp() + $4::interval
WHERE tenant_id = $1 AND name = $2
RETURNING epoch, clock_timestamp()`, l.tenant, l.name, l.holder, l.ttl).Scan(&term.Epoch, &term.At)
		}
	}
	if err != nil {
		return app.Term{}, false, fmt.Errorf("take the lease: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Term{}, false, fmt.Errorf("commit taking the lease: %w", err)
	}
	l.epoch.Store(term.Epoch)
	return term, true, nil
}

// Renew extends the holding by the lease's ttl from now, unless it expired
// or another instance took it over. It changes the expiry only, so it waits
// for no write.
func (l *Lease) Renew(ctx context.Context) error {
	epoch := l.epoch.Load()
	tag, err := l.pool.Exec(ctx, `
UPDATE leases SET expires_at = clock_timestamp() + $4::interval
WHERE tenant_id = $1 AND name = $2 AND epoch = $3 AND expires_at > clock_timestamp()`, l.tenant, l.name, epoch, l.ttl)
	if err != nil {
		return fmt.Errorf("renew the lease: %w", err)
	}
	if epoch == 0 || tag.RowsAffected() == 0 {
		l.epoch.CompareAndSwap(epoch, 0)
		return fmt.Errorf("renew the lease: %w", app.ErrLeaseLost)
	}
	return nil
}

// TTL is how long a holding lasts from its last renewal.
func (l *Lease) TTL() time.Duration { return l.ttl }

// Release gives the holding up: it expires at once.
func (l *Lease) Release(ctx context.Context) error {
	epoch := l.epoch.Swap(0)
	if epoch == 0 {
		return nil
	}
	_, err := l.pool.Exec(ctx, `UPDATE leases SET expires_at = clock_timestamp() WHERE tenant_id = $1 AND name = $2 AND epoch = $3`,
		l.tenant, l.name, epoch)
	if err != nil {
		return fmt.Errorf("release the lease: %w", err)
	}
	return nil
}

// Check checks, in a write transaction, that the instance's holding still
// holds the lease, and takes the lease's row in key share until the
// transaction ends, so that no takeover commits before the write does.
func (l *Lease) Check(ctx context.Context, tx pgx.Tx) error {
	epoch := l.epoch.Load()
	if epoch == 0 {
		return fmt.Errorf("check the lease: %w", app.ErrLeaseLost)
	}
	var one int
	err := tx.QueryRow(ctx, `
SELECT 1 FROM leases WHERE tenant_id = $1 AND name = $2 AND epoch = $3 AND expires_at > clock_timestamp() FOR KEY SHARE`,
		l.tenant, l.name, epoch).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check the lease: %w", app.ErrLeaseLost)
	}
	if err != nil {
		return fmt.Errorf("check the lease: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Stores that write under it**

Apply this change to `internal/work/postgres/records.go`:

```diff
diff --git a/internal/work/postgres/records.go b/internal/work/postgres/records.go
index 78692a8..ad47336 100644
--- a/internal/work/postgres/records.go
+++ b/internal/work/postgres/records.go
@@ -16,10 +16,11 @@ import (
 // Records keeps work items' records in the work_records table. It
 // implements app.Records.
 type Records struct {
 	pool   *pgxpool.Pool
 	tenant string
+	guard  Guard
 }
 
 // NewRecords returns the store of one tenant's work records.
 func NewRecords(pool *pgxpool.Pool, tenant string) *Records {
 	return &Records{pool: pool, tenant: tenant}
@@ -34,10 +35,18 @@ ORDER BY seq`
 const insertRecord = `
 INSERT INTO work_records (tenant_id, item_id, seq, id, incident_id, run_id, kind, body, recorded_at)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, greatest(clock_timestamp(), $9::timestamptz))
 RETURNING recorded_at`
 
+// Guarded returns the store, writing only while guard allows it: each
+// append checks it after taking the item's lock, before it reads or writes.
+func (s *Records) Guarded(guard Guard) *Records {
+	g := *s
+	g.guard = guard
+	return &g
+}
+
 // Load returns an item's records in commit order.
 func (s *Records) Load(ctx context.Context, id item.ID) ([]item.Record, error) {
 	return loadRecords(ctx, s.pool, s.tenant, id)
 }
 
@@ -75,10 +84,15 @@ func (s *Records) Append(ctx context.Context, id item.ID, decide func([]item.Rec
 		}
 	}()
 	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "work_records/"+s.tenant+"/"+string(id)); err != nil {
 		return nil, fmt.Errorf("lock item %s: %w", id, err)
 	}
+	if s.guard != nil {
+		if err := s.guard.Check(ctx, tx); err != nil {
+			return nil, err
+		}
+	}
 	history, err := loadRecords(ctx, tx, s.tenant, id)
 	if err != nil {
 		return nil, err
 	}
 	drafts, err := decide(history)
```

Apply this change to `internal/work/postgres/events.go`:

```diff
diff --git a/internal/work/postgres/events.go b/internal/work/postgres/events.go
index 6258225..fb5cda7 100644
--- a/internal/work/postgres/events.go
+++ b/internal/work/postgres/events.go
@@ -16,10 +16,11 @@ import (
 // EventStore keeps board events in the board_events table. It implements
 // app.EventStore.
 type EventStore struct {
 	pool   *pgxpool.Pool
 	tenant string
+	guard  Guard
 }
 
 // NewEventStore returns an event store for one tenant's rows. A deployment
 // serves one organization, but its records still carry a tenant id (spec
 // Section 1.4).
@@ -37,10 +38,18 @@ const insertEvent = `
 INSERT INTO board_events
     (tenant_id, run_id, seq, id, round, kind, author_kind, author_name, committed_at, body)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, greatest(clock_timestamp(), $9::timestamptz), $10)
 RETURNING committed_at`
 
+// Guarded returns the store, writing only while guard allows it: each
+// append checks it after taking the run's lock, before it reads or writes.
+func (s *EventStore) Guarded(guard Guard) *EventStore {
+	g := *s
+	g.guard = guard
+	return &g
+}
+
 // Load returns a run's events in commit order.
 func (s *EventStore) Load(ctx context.Context, run board.RunID) ([]board.Event, error) {
 	return loadEvents(ctx, s.pool, s.tenant, run)
 }
 
@@ -63,10 +72,15 @@ func (s *EventStore) Append(ctx context.Context, run board.RunID, decide func([]
 		}
 	}()
 	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", s.tenant+"/"+string(run)); err != nil {
 		return nil, fmt.Errorf("lock run %s: %w", run, err)
 	}
+	if s.guard != nil {
+		if err := s.guard.Check(ctx, tx); err != nil {
+			return nil, err
+		}
+	}
 	history, err := loadEvents(ctx, tx, s.tenant, run)
 	if err != nil {
 		return nil, err
 	}
 	drafts, err := decide(history)
```

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test -tags integration ./internal/work/postgres/ ./internal/db/ && golangci-lint run ./...`
Expected: every package `ok`; `0 issues.`

- [ ] **Step 7: Commit**

```bash
git add internal/work internal/db/migrations
git commit -m "feat(work): the platform's lease, with an epoch that fences a holder that lost it, and stores that write only under it"
```

### Task 7: The coordinator

Decisions 4 to 8 and 11 to 16. The coordinator opens an item, starts it, opens and ends its rounds and stops its run, writing each decision as a record first. Its tests run it over stores in memory that can fail an append, with a clock moved by hand that is also its timers, a registry whose current revision can move on, and agents whose turns are scripted and act through the real MCP gateway, with the tokens their turns are given.

**Files:**
- Create: `internal/work/app/coordinator.go`
- Create: `internal/work/app/harness_test.go`, `internal/work/app/coordinator_test.go`
- Modify: `internal/work/app/ports.go`

**Interfaces:**
- Consumes: the board's use cases (Task 2), `item` (Task 3), `app.Sessions` and `sessions.Registry` (Task 4), `app.Records`, `app.Spend` and `memory.Records` (Task 5).
- Produces:
  - the ports `app.Clock{Now() time.Time; Local(time.Time) time.Time}`, `app.Timers{At(t time.Time, fn func()) (cancel func())}`, `app.Turns{Start(ctx, *Turn) error; Stop(run, agent); StopAll()}`, `app.Revisions{Current() *config.Revision; At(ctx, commit) (*config.Revision, error); Lock(*config.Revision) (config.Lock, error)}`, with their contracts: a timer's function never runs before `At` returns, a cancel never waits for one that runs; no `Turns` method reports a turn's end before it returns or waits for a turn; `Start` returns once the runner has the turn, fails only when the runner cannot be reached, and starts a turn repeated with the same id once;
  - `app.Turn{ID; Run; Agent; Round; WrapUp; Deadline; MCP, LLM tokens.Secret; Image; Model; Skills; Prompt}`, `app.Exit{Run; Agent; Round; Turn string; Class item.TurnClass}`;
  - `app.Ports{Records; Boards *Boards; Spend; Revisions; Sessions; Turns; Clock; Timers; IDs}`, `app.Options{DailyCap int64; Grace, SettleWait, SettlePoll, Retry time.Duration; Log *slog.Logger}`, `app.NewCoordinator(*Ports, *Options) (*Coordinator, error)`, `app.ErrInvalid`;
  - `app.OpenInput{Type, Title, Source string; Routing map[string]string; Principal string; Responders []string}`, `(*Coordinator).Open(ctx, *OpenInput) (item.ID, error)`, `(*Coordinator).Exited(ctx, Exit)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/work/app/harness_test.go`:

```go
package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	actionsmemory "github.com/jchen0426/agent-swarm/internal/actions/memory"
	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
	"github.com/jchen0426/agent-swarm/internal/work/sessions"
)

const commit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"

// image is the runtime image the deployment pins for thinloop.
var image = "sha256:" + strings.Repeat("a", 64)

// search is the logs server's tool, and logLines what it answers.
var search = config.ToolDefinition{
	Name: "search", Description: "Search the logs.",
	InputSchema: []byte(`{"type":"object","properties":{"q":{"type":"string"}}}`),
}

const logLines = `{"lines":["checkout v42 deployed at 10:02","errors rose at 10:03"]}`

// configuration is a revision with an incident type of three rounds of at
// most two minutes, ten minutes of wall clock and 90,000 tokens, where two
// agents that read one source converge; two agents subscribed to it, who
// read the logs, and a third that is not; a change type no agent subscribes
// to; and a rule that applies always.
func configuration(t *testing.T) map[string][]byte {
	t.Helper()
	searchHash, err := config.ToolSchemaHash(&search)
	require.NoError(t, err)
	return map[string][]byte{
		"work-item-types/incident.yaml": []byte(`kind: WorkItemType
metadata: {name: incident, owner: sre-leads}
spec:
  hypothesisKey:
    - {name: service, values: [checkout, payments]}
    - {name: failureMode, values: [bad-release, config-change]}
  budget: {wallClock: 10m, tokens: 90000, rounds: 3}
  round: {timeLimit: 2m}
  convergence: {minContributors: 2, minSources: 1, refutationMargin: 2}
`),
		"work-item-types/change.yaml": []byte(`kind: WorkItemType
metadata: {name: change, owner: sre-leads}
spec:
  hypothesisKey:
    - {name: service, values: [checkout]}
`),
		"work-item-types/catalogued.yaml": []byte(`kind: WorkItemType
metadata: {name: catalogued, owner: sre-leads}
spec:
  hypothesisKey:
    - {name: service, values: {tool: catalog.list_services, field: "$[*].name"}}
`),
		"servers/catalog.yaml": []byte(`kind: McpServer
metadata: {name: catalog, owner: platform}
spec:
  endpoint: https://catalog.internal/mcp
  image: "sha256:` + strings.Repeat("c", 64) + `"
  tools:
    list_services: {effect: read, schemaHash: "sha256:` + strings.Repeat("b", 64) + `"}
`),
		"servers/logs.yaml": []byte(`kind: McpServer
metadata: {name: logs, owner: platform}
spec:
  endpoint: https://logs.internal/mcp
  image: "sha256:` + strings.Repeat("d", 64) + `"
  tools:
    search: {effect: read, schemaHash: "` + searchHash + `"}
`),
		"agents/release-investigator.yaml":  agent("release-investigator", "incident"),
		"agents/runtime-investigator.yaml":  agent("runtime-investigator", "incident"),
		"agents/capacity-investigator.yaml": agent("capacity-investigator", ""),
		"redaction-rules/api-keys.yaml": []byte(`kind: RedactionRule
metadata: {name: api-keys, owner: security@example.com}
spec:
  always: true
  patterns: ['sk-[a-z0-9]{8}']
`),
	}
}

// agent is an agent of the platform's runtime and model that reads the
// logs, subscribed to one type, or to none.
func agent(name, subscribed string) []byte {
	return fmt.Appendf(nil, `kind: Agent
metadata: {name: %s, owner: alice@example.com}
spec:
  runtime: thinloop
  model: claude-haiku-5-5
  tools: [logs.search]
  subscribe: {workItemTypes: [%s]}
`, name, subscribed)
}

// revisions is the registry: the revisions it holds by commit, and the
// current one, which those of then replace one by one, a call of Current
// after another, as when the registry refreshes while an item opens and
// starts.
type revisions struct {
	mu   sync.Mutex
	rev  *config.Revision
	then []*config.Revision
	all  map[string]*config.Revision
}

func (r *revisions) Current() *config.Revision {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.rev
	if len(r.then) > 0 {
		r.rev, r.then = r.then[0], r.then[1:]
	}
	return current
}

func (r *revisions) At(_ context.Context, c string) (*config.Revision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rev := r.all[c]; rev != nil {
		return rev, nil
	}
	return nil, fmt.Errorf("no revision %s", c)
}

// set makes rev the current revision; next makes revs the current ones, one
// by one, from Current's next call on.
func (r *revisions) set(rev *config.Revision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rev = rev
}

func (r *revisions) next(revs ...*config.Revision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.then = revs
}

func (r *revisions) Lock(rev *config.Revision) (config.Lock, error) {
	return rev.Lock(map[string]string{"thinloop": image})
}

// clock is the Clock and the Timers, moved by hand: Advance fires the
// timers that come due on the way, in order, outside its lock. It starts at
// the real time, which the MCP gateway's call deadlines are measured in.
type clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*timer
	n      int
}

type timer struct {
	at        time.Time
	n         int
	fn        func()
	cancelled bool
}

func newClock() *clock { return &clock{now: time.Now().UTC().Truncate(time.Second)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Local(t time.Time) time.Time { return t }

func (c *clock) At(t time.Time, fn func()) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	tm := &timer{at: t, n: c.n, fn: fn}
	c.timers = append(c.timers, tm)
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		tm.cancelled = true
	}
}

// Advance moves the clock d on, firing each timer as its time comes.
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.SliceStable(c.timers, func(i, j int) bool {
			return c.timers[i].at.Before(c.timers[j].at) || c.timers[i].at.Equal(c.timers[j].at) && c.timers[i].n < c.timers[j].n
		})
		var due *timer
		for len(c.timers) > 0 && (c.timers[0].cancelled || !c.timers[0].at.After(end)) {
			tm := c.timers[0]
			c.timers = c.timers[1:]
			if !tm.cancelled {
				due = tm
				break
			}
		}
		if due == nil {
			c.now = end
			c.mu.Unlock()
			return
		}
		if due.at.After(c.now) {
			c.now = due.at
		}
		c.mu.Unlock()
		due.fn()
	}
}

// pending counts the timers armed and not cancelled.
func (c *clock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, tm := range c.timers {
		if !tm.cancelled {
			n++
		}
	}
	return n
}

// spend is the LLM gateway's records, as the round's ends read them.
type spend struct {
	mu       sync.Mutex
	spent    map[string]int64
	day      int64
	inFlight map[string]int
	failed   map[string][]app.ModelCall
}

func newSpend() *spend {
	return &spend{spent: map[string]int64{}, inFlight: map[string]int{}, failed: map[string][]app.ModelCall{}}
}

func (s *spend) Spent(_ context.Context, run string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spent[run], nil
}

func (s *spend) SpentSince(context.Context, time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.day, nil
}

func (s *spend) InFlight(_ context.Context, run string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight[run], nil
}

func (s *spend) Failed(_ context.Context, run string) ([]app.ModelCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.failed[run]), nil
}

func (s *spend) set(f func(s *spend)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// script is what an agent does in its turn, through the MCP gateway; it
// returns how the turn ended, or "" for a turn that never ends by itself.
type script func(h *harness, t *app.Turn) item.TurnClass

// endTurn ends the turn at once.
func endTurn(h *harness, t *app.Turn) item.TurnClass {
	h.call(t, "end_turn", `{}`)
	return item.TurnEnded
}

// ending ends the turn as class says, without end_turn.
func ending(class item.TurnClass) script {
	return func(*harness, *app.Turn) item.TurnClass { return class }
}

// hypothesis reads the logs, posts what they show as evidence, and posts
// that checkout's bad release is the cause, citing it, then ends the turn:
// two agents that do so make the hypothesis converge.
func hypothesis(h *harness, t *app.Turn) item.TurnClass {
	read := h.call(t, "logs__search", `{"q":"checkout"}`)
	require.Equal(h.t, actionsapp.StatusOK, read.Status, read.Text)
	ev := h.call(t, "post_evidence", fmt.Sprintf(`{"claim":"v42 went out a minute before the errors","call_id":%q,"quote":"checkout v42 deployed at 10:02"}`, read.CallID))
	require.Equal(h.t, actionsapp.StatusOK, ev.Status, ev.Text)
	r := h.call(t, "post_hypothesis", fmt.Sprintf(`{"key":{"service":"checkout","failureMode":"bad-release"},"statement":"v42 broke checkout","evidence_ids":[%q]}`,
		strings.TrimPrefix(ev.Text, "posted evidence ")))
	require.Equal(h.t, actionsapp.StatusOK, r.Status, r.Text)
	return endTurn(h, t)
}

// turns is the Turns port, scripted: a turn started waits until the test
// plays it. fail, when set, says why the runner cannot start a turn.
type turns struct {
	mu      sync.Mutex
	queued  []app.Turn
	started []app.Turn
	stopped []string
	all     int
	fail    func(t *app.Turn) error
}

func (s *turns) Start(_ context.Context, t *app.Turn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		if err := s.fail(t); err != nil {
			return err
		}
	}
	s.queued = append(s.queued, *t)
	s.started = append(s.started, *t)
	return nil
}

func (s *turns) Stop(run board.RunID, agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued = slices.DeleteFunc(s.queued, func(t app.Turn) bool { return t.Run == run && t.Agent == agent })
	s.stopped = append(s.stopped, fmt.Sprintf("%s/%s", run, agent))
}

func (s *turns) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued = nil
	s.all++
}

func (s *turns) take() []app.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.queued
	s.queued = nil
	return out
}

// harness is a coordinator over stores in memory, the clock, and agents
// that act through the MCP gateway with the tokens their turns were given.
type harness struct {
	t        *testing.T
	c        *app.Coordinator
	clock    *clock
	cc       *celeval.Conditions
	records  *failing
	store    *failingStore
	boards   *app.Boards
	registry *sessions.Registry
	gw       *actionsapp.Gateway
	turns    *turns
	spend    *spend
	revs     *revisions
	opts     app.Options
	scripts  map[string]script
}

// failing is the records store, which can fail an append: fail returns the
// error for the nth append, counting from 1, or nil. As a database does, it
// refuses an append whose context ended.
type failing struct {
	*memory.Records
	mu   sync.Mutex
	n    int
	fail func(n int) error
}

func (f *failing) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	f.mu.Lock()
	f.n++
	n, fail := f.n, f.fail
	f.mu.Unlock()
	if fail != nil {
		if err := fail(n); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.Records.Append(ctx, id, decide)
}

func (f *failing) set(fail func(n int) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

// failingStore is the boards' store, which can fail an append as failing
// does.
type failingStore struct {
	*memory.Store
	mu   sync.Mutex
	n    int
	fail func(n int) error
}

func (f *failingStore) Append(ctx context.Context, run board.RunID, decide func([]board.Event) ([]board.Draft, error)) ([]board.Event, error) {
	f.mu.Lock()
	f.n++
	n, fail := f.n, f.fail
	f.mu.Unlock()
	if fail != nil {
		if err := fail(n); err != nil {
			return nil, err
		}
	}
	return f.Store.Append(ctx, run, decide)
}

func (f *failingStore) set(fail func(n int) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

// ids issues readable sequential ids for one harness.
type ids struct {
	mu sync.Mutex
	n  int
}

func (i *ids) NewID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("id-%d", i.n)
}

// logs is the logs server: it offers its search tool, which answers
// logLines.
type logs struct{}

func (logs) Offered(_ context.Context, s *actionsapp.Server) (map[string]actionsapp.Offer, error) {
	if s.Name != "logs" {
		return nil, errors.New("no such server")
	}
	return map[string]actionsapp.Offer{"search": {Def: search}}, nil
}

func (logs) Call(context.Context, *actionsapp.Conn, string, json.RawMessage) (*output.Result, error) {
	return &output.Result{Blocks: []output.Block{{Kind: output.Text, Text: logLines}}}, nil
}

func newHarness(t *testing.T, change ...func(*app.Options)) *harness {
	t.Helper()
	cc, err := celeval.New()
	require.NoError(t, err)
	fsys, err := memfs.New(configuration(t))
	require.NoError(t, err)
	rev, err := config.Build(fsys, commit, cc)
	require.NoError(t, err)
	h := &harness{
		t: t, clock: newClock(), cc: cc, registry: sessions.New(), turns: &turns{}, spend: newSpend(),
		revs: &revisions{rev: rev, all: map[string]*config.Revision{commit: rev}}, scripts: map[string]script{},
	}
	h.records = &failing{Records: memory.NewRecords(h.clock.Now)}
	h.store = &failingStore{Store: memory.NewStore(h.clock.Now)}
	audit := actionsmemory.NewAudit()
	idgen := &ids{}
	h.boards = app.NewBoards(h.store, audit, h.records, idgen)
	h.gw, err = actionsapp.New(t.Context(), &actionsapp.Ports{
		Sessions: h.registry.MCP(), Revisions: h.revs, Upstream: logs{}, Audit: audit, Hides: &actionsmemory.Hides{},
		Conditions: cc, IDs: idgen, Limits: limits.New(limits.Defaults, nil, h.clock.Now), Now: h.clock.Now, Board: h.boards,
	}, &actionsapp.Defaults)
	require.NoError(t, err)
	h.opts = app.Options{DailyCap: 1_000_000, Grace: 5 * time.Second, SettleWait: 30 * time.Second, SettlePoll: time.Second, Retry: 5 * time.Second}
	for _, f := range change {
		f(&h.opts)
	}
	h.c, err = app.NewCoordinator(&app.Ports{
		Records: h.records, Boards: h.boards, Spend: h.spend, Revisions: h.revs, Sessions: h.registry, Turns: h.turns,
		Clock: h.clock, Timers: h.clock, IDs: idgen,
	}, &h.opts)
	require.NoError(t, err)
	return h
}

// revision builds a revision of the harness's configuration, as change
// edits its files, under commit, and keeps it in the registry.
func (h *harness) revision(commit string, change func(files map[string][]byte)) *config.Revision {
	h.t.Helper()
	files := configuration(h.t)
	change(files)
	fsys, err := memfs.New(files)
	require.NoError(h.t, err)
	rev, err := config.Build(fsys, commit, h.cc)
	require.NoError(h.t, err)
	h.revs.mu.Lock()
	defer h.revs.mu.Unlock()
	h.revs.all[commit] = rev
	return rev
}

// open opens an incident, as a person does.
func (h *harness) open(title string) item.ID {
	h.t.Helper()
	id, err := h.c.Open(h.t.Context(), &app.OpenInput{Type: "incident", Title: title, Source: "person", Routing: map[string]string{"service": "checkout"}})
	require.NoError(h.t, err)
	return id
}

// play plays every turn started, each by its agent's script (end_turn by
// default), and reports how each ended; it returns the turns played.
func (h *harness) play() []app.Turn {
	h.t.Helper()
	played := h.turns.take()
	for i := range played {
		t := &played[i]
		s := h.scripts[t.Agent]
		if s == nil {
			s = endTurn
		}
		if class := s(h, t); class != "" {
			h.c.Exited(h.t.Context(), app.Exit{Run: t.Run, Agent: t.Agent, Round: t.Round, Turn: t.ID, Class: class})
		}
	}
	return played
}

// round plays the turns started, then lets the round's end be decided.
func (h *harness) round() []app.Turn {
	h.t.Helper()
	played := h.play()
	h.clock.Advance(h.opts.Grace)
	return played
}

// call makes a tool call with a turn's MCP token.
func (h *harness) call(t *app.Turn, name, args string) *actionsapp.Response {
	h.t.Helper()
	r, err := h.gw.Call(h.t.Context(), t.MCP.Reveal(), name, json.RawMessage(args))
	require.NoError(h.t, err)
	return r
}

// state folds an item's records.
func (h *harness) state(id item.ID) *item.State {
	h.t.Helper()
	records, err := h.records.Load(h.t.Context(), id)
	require.NoError(h.t, err)
	s, err := item.Fold(records)
	require.NoError(h.t, err)
	return s
}

// kinds lists an item's records by kind.
func (h *harness) kinds(id item.ID) []item.Kind {
	h.t.Helper()
	records, err := h.records.Load(h.t.Context(), id)
	require.NoError(h.t, err)
	out := make([]item.Kind, 0, len(records))
	for i := range records {
		out = append(out, records[i].Body.Kind())
	}
	return out
}

// events returns a run's board.
func (h *harness) events(run board.RunID) []board.Event {
	h.t.Helper()
	events, err := h.store.Load(h.t.Context(), run)
	require.NoError(h.t, err)
	return events
}
```

Create `internal/work/app/coordinator_test.go`:

```go
package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// An item opens and starts at once: it locks the configuration, creates its
// run with the agents subscribed to its type, posts the evidence it opened
// with, opens round 1 on the board, and starts a turn for each agent, with
// sessions the gateways know from then on.
func TestAnItemStartsItsFirstRoundAtOnce(t *testing.T) {
	h := newHarness(t)

	id := h.open("checkout fails")

	s := h.state(id)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindStarted, item.KindRunCreated, item.KindRoundOpened}, h.kinds(id))
	assert.Equal(t, &item.Started{Commit: commit, Digest: h.revs.rev.Digest(), Images: map[string]string{"thinloop": image}}, s.Started)
	assert.Equal(t, []string{release.Name, runtime.Name}, s.Run.Created.Agents, "the subscribed agents, in the revision's order")
	assert.Equal(t, item.Budget{WallClock: 10 * time.Minute, Tokens: 90_000, Rounds: 3}, s.Run.Created.Budget)
	events := h.events(s.Run.ID)
	require.Len(t, events, 2)
	assert.Equal(t, &board.Evidence{Claim: "Opened by person: checkout fails; service=checkout", Provenance: board.Provenance{
		Kind: board.FromPlatform, Ref: "item:" + string(id), Source: "person",
	}}, events[0].Body.Evidence)
	assert.Equal(t, &board.RoundStart{Round: 1}, events[1].Body.Round)
	started := h.turns.started
	require.Len(t, started, 2)
	assert.NotEqual(t, started[0].ID, started[1].ID, "each turn has its own id")
	opened := s.Run.Latest().Opened
	for i, name := range []string{release.Name, runtime.Name} {
		turn := started[i]
		assert.NotEmpty(t, turn.ID)
		assert.Equal(t, name, turn.Agent)
		assert.Equal(t, 1, turn.Round)
		assert.Equal(t, opened.Deadline, turn.Deadline)
		assert.Equal(t, image, turn.Image)
		assert.Equal(t, "claude-haiku-5-5", turn.Model)
		assert.Contains(t, turn.Prompt, "Round 1 of this investigation: at most 2 more rounds follow it")
		mcp, err := h.registry.MCP().Session(t.Context(), turn.MCP.Reveal())
		require.NoError(t, err)
		assert.Equal(t, actionsapp.Session{Run: s.Run.ID, Agent: name, Commit: commit, Round: 1, Deadline: opened.Deadline,
			Item: map[string]string{"service": "checkout"}, Done: mcp.Done}, mcp)
		llm, err := h.registry.LLM().Session(t.Context(), turn.LLM.Reveal())
		require.NoError(t, err)
		assert.Equal(t, int64(90_000), llm.Budget)
	}
}

// A round whose turns all ended leaves nothing on the board but turn ends:
// once the round's write calls had their grace, the run stops quiet. Its
// board ends with the stop entry, its sessions end, and the item awaits its
// close.
func TestAQuietRoundStopsTheRun(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID

	played := h.play()
	h.clock.Advance(h.opts.Grace - time.Second)
	require.Len(t, h.kinds(id), 4, "the round's end waits for its write calls' grace")
	h.clock.Advance(time.Second)

	s := h.state(id)
	assert.Equal(t, item.StatusAwaitingClose, s.Status())
	assert.Equal(t, item.StopQuiet, s.Closing.Reason)
	assert.Equal(t, item.StopQuiet, s.Run.Stopped.Reason)
	ended := s.Run.Rounds[0].Ended
	assert.Equal(t, item.EndedByTurns, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, ended.Turns)
	stop, stopped := lastStop(h.events(run))
	require.True(t, stopped)
	assert.Equal(t, "quiet", stop.Reason)
	assert.Equal(t, 1, stop.Rounds)
	_, err := h.registry.MCP().Session(t.Context(), played[0].MCP.Reveal())
	require.ErrorIs(t, err, actionsapp.ErrNoSession)
	_, err = h.registry.LLM().Session(t.Context(), played[1].LLM.Reveal())
	require.ErrorIs(t, err, llmapp.ErrNoSession)
	assert.Zero(t, h.clock.pending(), "no timer is left")
}

func lastStop(events []board.Event) (*board.Stop, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if st := events[i].Body.Stop; st != nil {
			return st, true
		}
	}
	return nil, false
}

// Two agents that post the same hypothesis make it converge; the next
// round is its wrap-up round, in which the agents may refute it, and the
// run stops converged with it as the conclusion. The wrap-up round's
// sessions may spend a round's share of the budget above the run's spend.
func TestAConvergedHypothesisGetsItsWrapUpRoundAndTheRunStops(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name], h.scripts[runtime.Name] = hypothesis, hypothesis
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.spend.set(func(s *spend) { s.spent[string(run)] = 89_000 })

	h.round()
	wrapUp := h.turns.take()
	h.turns.queued = append(h.turns.queued, wrapUp...)
	h.scripts[release.Name], h.scripts[runtime.Name] = endTurn, endTurn
	llm, err := h.registry.LLM().Session(t.Context(), wrapUp[0].LLM.Reveal())
	require.NoError(t, err)
	h.round()

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	h1 := s.Run.Rounds[0].Ended.Decision.WrapUp
	assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: h1}, s.Run.Rounds[0].Ended.Decision)
	assert.Equal(t, h1, wrapUp[0].WrapUp)
	assert.Contains(t, wrapUp[0].Prompt, "wrap-up round")
	assert.Equal(t, int64(89_000+30_000), llm.Budget)
	assert.Equal(t, item.StopConverged, s.Run.Stopped.Reason)
	assert.Equal(t, h1, s.Run.Stopped.Conclusion)
	stop, _ := lastStop(h.events(run))
	assert.Equal(t, h1, stop.Conclusion)
	assert.Equal(t, &board.RoundStart{Round: 2, WrapUp: h1}, roundEntries(h.events(run))[1])
}

func roundEntries(events []board.Event) []*board.RoundStart {
	var out []*board.RoundStart
	for i := range events {
		if r := events[i].Body.Round; r != nil {
			out = append(out, r)
		}
	}
	return out
}

// The deadline ends a round's turns: those still running are stopped and
// count as interrupted.
func TestTheDeadlineEndsTheTurnsStillRunning(t *testing.T) {
	h := newHarness(t)
	h.scripts[runtime.Name] = ending("")
	id := h.open("checkout fails")
	run := h.state(id).Run.ID

	h.play()
	h.clock.Advance(2 * time.Minute)
	require.Contains(t, h.turns.stopped, string(run)+"/"+runtime.Name, "the deadline stops it, before the round's end is decided")
	require.Nil(t, h.state(id).Run.Rounds[0].Ended)
	h.clock.Advance(h.opts.Grace)

	ended := h.state(id).Run.Rounds[0].Ended
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnInterrupted}, ended.Turns)
}

// A round whose every turn failed at the model vendor ends at its
// deadline, not earlier, and is never quiet: a vendor outage cannot spend
// the run's rounds in seconds.
func TestARoundOfVendorFailuresEndsAtItsDeadline(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name], h.scripts[runtime.Name] = ending(item.TurnVendor), ending(item.TurnVendor)
	id := h.open("checkout fails")

	h.play()
	h.clock.Advance(h.opts.Grace)
	require.Len(t, h.state(id).Run.Rounds, 1, "every turn ended, but the round goes on")
	h.clock.Advance(2*time.Minute - h.opts.Grace)
	h.clock.Advance(h.opts.Grace)

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	ended := s.Run.Rounds[0].Ended
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.False(t, ended.Quiet)
	assert.Equal(t, item.TurnVendor, ended.Turns[release.Name])
}

// A round's end waits for the run's model calls in flight, up to the
// settling wait, so that their cost counts: it reads the spend again once
// they ended, or once the wait is over, and says whether they settled.
func TestARoundsEndWaitsForTheRunsCallsInFlight(t *testing.T) {
	for name, c := range map[string]struct {
		after   time.Duration
		settled bool
	}{
		"they end":    {after: 3 * time.Second, settled: true},
		"they go on":  {after: time.Hour, settled: false},
		"none at all": {after: 0, settled: true},
	} {
		h := newHarness(t)
		id := h.open("checkout fails")
		run := string(h.state(id).Run.ID)
		if c.after > 0 {
			h.spend.set(func(s *spend) { s.inFlight[run] = 1 })
		}

		h.play()
		h.clock.Advance(h.opts.Grace)
		if c.after > 0 {
			assert.Nil(t, h.state(id).Run.Rounds[0].Ended, name)
			h.spend.set(func(s *spend) { s.spent[run] = 700 })
			if c.after < h.opts.SettleWait {
				h.clock.Advance(c.after - time.Second)
				h.spend.set(func(s *spend) { s.inFlight[run] = 0 })
			}
			h.clock.Advance(h.opts.SettleWait)
		}

		ended := h.state(id).Run.Rounds[0].Ended
		require.NotNil(t, ended, name)
		assert.Equal(t, c.settled, ended.Settled, name)
		if c.after > 0 {
			assert.Equal(t, int64(700), ended.Spent, name)
		}
	}
}

// Each model call refused, failed or of unknown outcome is noticed on the
// run's board once, at the end of the round in which its outcome is first
// read, naming the call and its agent.
func TestAFailedModelCallIsNoticedOnce(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name] = ending(item.TurnRefused)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.spend.set(func(s *spend) {
		s.failed[string(run)] = []app.ModelCall{{ID: "m-1", Agent: release.Name, Status: "refused"}}
	})

	h.round()
	h.spend.set(func(s *spend) {
		s.failed[string(run)] = append(s.failed[string(run)], app.ModelCall{ID: "m-2", Agent: runtime.Name, Status: "unknown"},
			app.ModelCall{ID: "m-3", Agent: runtime.Name, Status: "error"})
	})
	h.round()

	var notices []string
	for _, e := range h.events(run) {
		if n := e.Body.Notice; n != nil {
			notices = append(notices, string(n.Code)+" "+n.Agent+" "+n.CallID+" "+strings.Repeat("r", e.Round))
		}
	}
	assert.Equal(t, []string{"model_refused release-investigator m-1 r", "model_unknown runtime-investigator m-2 rr", "model_failed runtime-investigator m-3 rr"}, notices)
}

// An item opened once the day's spend reached the daily cap is not staffed:
// it awaits its close, with no run and no turn.
func TestAnItemOpenedOverTheDailyCapIsNotStaffed(t *testing.T) {
	h := newHarness(t)
	h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })

	id := h.open("checkout fails")

	s := h.state(id)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(id))
	assert.Equal(t, item.StopNotStaffed, s.Closing.Reason)
	assert.Empty(t, h.turns.started)
}

// A run whose round ends once the day's spend reached the cap stops cut,
// and its item is not staffed.
func TestARunIsCutAtTheDailyCap(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name], h.scripts[runtime.Name] = hypothesis, hypothesis
	id := h.open("checkout fails")
	h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })

	h.round()

	s := h.state(id)
	assert.Equal(t, item.StopCut, s.Run.Stopped.Reason)
	assert.Equal(t, item.StopNotStaffed, s.Closing.Reason)
}

// An item whose type no agent subscribes to is not staffed: its run stops
// at once, with a no_agents notice on its board.
func TestAnItemWithNoAgentIsNotStaffed(t *testing.T) {
	h := newHarness(t)

	id, err := h.c.Open(t.Context(), &app.OpenInput{Type: "change", Title: "a change", Source: "person"})
	require.NoError(t, err)

	s := h.state(id)
	assert.Equal(t, item.StopNotStaffed, s.Run.Stopped.Reason)
	assert.Equal(t, item.StopNotStaffed, s.Closing.Reason)
	events := h.events(s.Run.ID)
	require.Len(t, events, 2)
	assert.Equal(t, &board.Notice{Code: board.NoticeNoAgents}, events[0].Body.Notice)
	assert.Equal(t, "not_staffed", events[1].Body.Stop.Reason)
	assert.Empty(t, h.turns.started)
}

// An agent whose turn crashed in two rounds in a row leaves the run: its
// session ends, and the next round invites the others.
func TestAnAgentThatCrashesTwiceLeavesTheRun(t *testing.T) {
	h := newHarness(t)
	h.scripts[runtime.Name] = ending(item.TurnCrashed)
	h.scripts[release.Name] = func(h *harness, t *app.Turn) item.TurnClass {
		r := h.call(t, "post_task", `{"title":"check the deploy"}`)
		require.Equal(h.t, actionsapp.StatusOK, r.Status, r.Text)
		return endTurn(h, t)
	}
	id := h.open("checkout fails")

	h.round()
	second := h.round()
	third := h.turns.take()

	s := h.state(id)
	assert.Equal(t, []string{runtime.Name}, s.Run.Rounds[1].Ended.Removed)
	require.Len(t, third, 1)
	assert.Equal(t, release.Name, third[0].Agent)
	for _, turn := range second {
		if turn.Agent == runtime.Name {
			_, err := h.registry.MCP().Session(t.Context(), turn.MCP.Reveal())
			require.ErrorIs(t, err, actionsapp.ErrNoSession)
		}
	}
}

// A run stops when its budget is used: here its three rounds.
func TestARunStopsWhenItsRoundsAreUsed(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name] = func(h *harness, t *app.Turn) item.TurnClass {
		r := h.call(t, "post_task", `{"title":"check round `+strings.Repeat("i", t.Round)+`"}`)
		require.Equal(h.t, actionsapp.StatusOK, r.Status, r.Text)
		return endTurn(h, t)
	}
	id := h.open("checkout fails")

	var last []app.Turn
	for range 3 {
		last = h.round()
	}

	s := h.state(id)
	assert.Len(t, s.Run.Rounds, 3)
	assert.Equal(t, item.StopBudgetExhausted, s.Run.Stopped.Reason)
	assert.Contains(t, last[0].Prompt, "Round 3 of this investigation: it is the last")
}

// The title is redacted with the rules that apply always before it is
// recorded or posted.
func TestTheTitleIsRedactedBeforeItIsRecorded(t *testing.T) {
	h := newHarness(t)

	id := h.open("key sk-abcdefgh leaked")

	s := h.state(id)
	assert.Equal(t, "key [redacted:api-keys] leaked", s.Opened.Title)
	assert.NotContains(t, h.events(s.Run.ID)[0].Body.Evidence.Claim, "sk-abcdefgh")
}

// An item of a type that does not exist, or whose key reads its values
// from a tool when the item opens, which Plan 7c builds, is refused, and
// nothing is recorded.
func TestAnItemOfATypeTheCoordinatorCannotRunIsRefused(t *testing.T) {
	h := newHarness(t)
	for _, typ := range []string{"outage", "catalogued"} {
		_, err := h.c.Open(t.Context(), &app.OpenInput{Type: typ, Title: "t", Source: "person"})

		require.ErrorIs(t, err, app.ErrInvalid, typ)
	}
	active, err := h.records.Active(t.Context())
	require.NoError(t, err)
	assert.Empty(t, active)
}

// A turn's end reported twice, late, for a round that is over, of another
// turn, or as a class the runner does not report changes nothing.
func TestALateOrRepeatedExitChangesNothing(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	turn := h.turns.started[0]
	require.Equal(t, release.Name, turn.Agent)

	for _, e := range []app.Exit{
		{Run: run, Agent: release.Name, Round: 1, Turn: "another-turn", Class: item.TurnEnded},
		{Run: run, Agent: release.Name, Round: 1, Class: item.TurnEnded},
		{Run: run, Agent: release.Name, Round: 1, Turn: turn.ID, Class: "lost"},
		{Run: run, Agent: release.Name, Round: 1, Turn: turn.ID, Class: item.TurnUnstarted},
		{Run: run, Agent: release.Name, Round: 1, Turn: turn.ID, Class: item.TurnCrashed},
		{Run: run, Agent: release.Name, Round: 1, Turn: turn.ID, Class: item.TurnEnded},
		{Run: run, Agent: release.Name, Round: 2, Turn: turn.ID, Class: item.TurnEnded},
		{Run: "run-404", Agent: release.Name, Round: 1, Turn: turn.ID, Class: item.TurnEnded},
		{Run: run, Agent: "stranger", Round: 1, Turn: turn.ID, Class: item.TurnEnded},
	} {
		h.c.Exited(t.Context(), e)
	}
	h.play()
	h.clock.Advance(h.opts.Grace)

	assert.Equal(t, item.TurnCrashed, h.state(id).Run.Rounds[0].Ended.Turns[release.Name])
}

// Each round's sessions have tokens of their own: a turn of an earlier round
// still running cannot act in the next.
func TestEachRoundsSessionsHaveTokensOfTheirOwn(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name] = func(h *harness, t *app.Turn) item.TurnClass {
		r := h.call(t, "post_task", `{"title":"check round `+strings.Repeat("i", t.Round)+`"}`)
		require.Equal(h.t, actionsapp.StatusOK, r.Status, r.Text)
		return endTurn(h, t)
	}
	h.open("checkout fails")

	first := h.round()
	second := h.turns.take()

	require.Len(t, second, 2)
	for i := range second {
		require.Equal(t, first[i].Agent, second[i].Agent)
		assert.NotEqual(t, first[i].MCP.Reveal(), second[i].MCP.Reveal())
		assert.NotEqual(t, first[i].LLM.Reveal(), second[i].LLM.Reveal())
		_, err := h.registry.MCP().Session(t.Context(), first[i].MCP.Reveal())
		require.ErrorIs(t, err, actionsapp.ErrNoSession)
		_, err = h.registry.LLM().Session(t.Context(), first[i].LLM.Reveal())
		require.ErrorIs(t, err, llmapp.ErrNoSession)
		s, err := h.registry.MCP().Session(t.Context(), second[i].MCP.Reveal())
		require.NoError(t, err)
		assert.Equal(t, 2, s.Round)
	}
}

// A stopped run's sessions and turns end before its stop entry is posted,
// which may take tries: no agent acts on a run that stopped.
func TestAStoppedRunsSessionsAndTurnsEndBeforeItsStopEntry(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	played := h.play()
	h.store.set(func(int) error { return errors.New("the board's store is down") })

	h.clock.Advance(h.opts.Grace)

	require.Equal(t, item.KindRunStopped, h.kinds(id)[len(h.kinds(id))-1], "the stop entry is not posted yet")
	for _, turn := range played {
		_, err := h.registry.MCP().Session(t.Context(), turn.MCP.Reveal())
		require.ErrorIs(t, err, actionsapp.ErrNoSession)
		assert.Contains(t, h.turns.stopped, string(run)+"/"+turn.Agent)
	}
	h.store.set(nil)
	h.clock.Advance(h.opts.Retry)
	_, stopped := lastStop(h.events(run))
	assert.True(t, stopped)
	assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
}

// A turn the runner could not start is tried again after the retry wait,
// while its round lasts, with the same id, so that a runner that started it
// after all starts it once.
func TestATurnThatCouldNotStartIsTriedAgain(t *testing.T) {
	h := newHarness(t)
	var tried []string
	h.turns.fail = func(t *app.Turn) error {
		if t.Agent != runtime.Name || len(tried) == 2 {
			return nil
		}
		tried = append(tried, t.ID)
		return errors.New("the runner cannot be reached")
	}
	id := h.open("checkout fails")
	require.Len(t, h.turns.started, 1)
	h.clock.Advance(h.opts.Retry)
	require.Len(t, h.turns.started, 1)

	h.clock.Advance(h.opts.Retry)

	require.Len(t, h.turns.started, 2)
	assert.Equal(t, runtime.Name, h.turns.started[1].Agent)
	assert.Equal(t, []string{h.turns.started[1].ID, h.turns.started[1].ID}, tried)
	h.round()
	ended := h.state(id).Run.Rounds[0].Ended
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, ended.Turns)
}

// A turn that never started by its round's deadline counts as unstarted:
// it is not quiet, and not a crash, so that a runner out of reach removes
// no agent; the round ends at its deadline.
func TestATurnThatNeverStartsIsUnstarted(t *testing.T) {
	h := newHarness(t)
	h.turns.fail = func(t *app.Turn) error {
		if t.Agent == runtime.Name {
			return errors.New("the runner cannot be reached")
		}
		return nil
	}
	id := h.open("checkout fails")

	for range 2 {
		h.play()
		h.clock.Advance(2 * time.Minute)
		h.clock.Advance(h.opts.Grace)
	}

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 3)
	for _, r := range s.Run.Rounds[:2] {
		assert.Equal(t, item.EndedByDeadline, r.Ended.EndedBy)
		assert.Equal(t, item.TurnUnstarted, r.Ended.Turns[runtime.Name])
		assert.False(t, r.Ended.Quiet)
		assert.Empty(t, r.Ended.Removed)
	}
	assert.Equal(t, []string{release.Name, runtime.Name}, s.Run.Rounds[2].Opened.Agents)
}

// An item's start does not depend on the request that opened it: once its
// opening is recorded, the request may end, as when a person's browser
// closes, and the item still starts at once.
func TestAnItemStartsThoughTheRequestThatOpenedItEnded(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	h.records.set(func(n int) error {
		if n == 2 {
			cancel()
		}
		return nil
	})

	id, err := h.c.Open(ctx, &app.OpenInput{Type: "incident", Title: "checkout fails", Source: "person"})

	require.NoError(t, err)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindStarted, item.KindRunCreated, item.KindRoundOpened}, h.kinds(id))
}

// An item runs under the revision it locks when it starts, though the
// registry moved on between its opening and its start, and moves on again
// before its run is created: its run, its sessions and its turns are the
// locked revision's. A revision that cannot run the item's type leaves it
// not staffed.
func TestAnItemRunsUnderTheRevisionItLocksAtStart(t *testing.T) {
	h := newHarness(t)
	moved := h.revision(strings.Repeat("b", 40), func(files map[string][]byte) {
		files["agents/capacity-investigator.yaml"] = agent("capacity-investigator", "incident")
	})
	later := h.revision(strings.Repeat("c", 40), func(files map[string][]byte) {
		delete(files, "agents/capacity-investigator.yaml")
	})
	h.revs.next(moved, later)
	id := h.open("checkout fails")
	started := h.turns.started

	s := h.state(id)
	assert.Equal(t, moved.Commit(), s.Started.Commit)
	assert.Equal(t, []string{"capacity-investigator", release.Name, runtime.Name}, s.Run.Created.Agents, "the locked revision's agents")
	require.Len(t, started, 3)
	mcp, err := h.registry.MCP().Session(t.Context(), started[0].MCP.Reveal())
	require.NoError(t, err)
	assert.Equal(t, moved.Commit(), mcp.Commit)
	h.round()
	assert.Equal(t, item.StopQuiet, h.state(id).Run.Stopped.Reason, "its round ran under the locked revision")

	h.revs.next(h.revision(strings.Repeat("d", 40), func(files map[string][]byte) {
		files["work-item-types/incident.yaml"] = []byte(strings.Replace(string(files["work-item-types/incident.yaml"]),
			"{name: service, values: [checkout, payments]}", `{name: service, values: {tool: catalog.list_services, field: "$[*].name"}}`, 1))
	}))
	unrunnable := h.open("payments fail")
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(unrunnable))
	assert.Equal(t, item.StopNotStaffed, h.state(unrunnable).Closing.Reason)
}

// An agent that pins a skill version the yank list withdraws is not invited
// (spec Section 5.1).
func TestAnAgentThatPinsAYankedSkillIsNotInvited(t *testing.T) {
	h := newHarness(t)
	h.revs.set(h.revision(strings.Repeat("e", 40), func(files map[string][]byte) {
		files["skills/release-diagnosis/skill.yaml"] = []byte(`kind: Skill
metadata: {name: release-diagnosis, owner: alice@example.com}
spec:
  versions:
    - version: 1.1.0
      source: {repository: https://git.example.com/runbooks, commit: 3f2a9c1, path: release}
`)
		files["skills/release-diagnosis/1.1.0/SKILL.md"] = []byte("# Release diagnosis\n\nRead the release history first.\n")
		files["yanks.yaml"] = []byte(`kind: YankList
yanks:
  - skill: release-diagnosis@1.1.0
    by: alice@example.com
    at: 2026-10-14T09:00:00Z
    reason: it reads the wrong history
`)
		files["agents/runtime-investigator.yaml"] = []byte(strings.Replace(string(agent("runtime-investigator", "incident")),
			"  tools:", "  skills: [release-diagnosis@1.1.0]\n  tools:", 1))
	}))

	id := h.open("checkout fails")

	assert.Equal(t, []string{release.Name}, h.state(id).Run.Created.Agents)
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -race ./internal/work/...`
Expected: FAIL: the build of `internal/work/app` fails (undefined: app.Turn)

- [ ] **Step 3: The ports**

Apply this change to `internal/work/app/ports.go`:

```diff
diff --git a/internal/work/app/ports.go b/internal/work/app/ports.go
index 477e85a..eb28284 100644
--- a/internal/work/app/ports.go
+++ b/internal/work/app/ports.go
@@ -3,11 +3,14 @@ package app
 import (
 	"context"
 	"errors"
 	"time"
 
+	"github.com/jchen0426/agent-swarm/internal/registry/config"
+	"github.com/jchen0426/agent-swarm/internal/work/board"
 	"github.com/jchen0426/agent-swarm/internal/work/item"
+	"github.com/jchen0426/agent-swarm/internal/work/tokens"
 )
 
 // Records keeps work items' records.
 type Records interface {
 	// Load returns an item's records in commit order.
@@ -75,5 +78,82 @@ type Term struct {
 	At    time.Time
 	// Previous is when the holding before it expired, or was released, in
 	// the database's time; zero for the lease's first holding.
 	Previous time.Time
 }
+
+// Clock tells the coordinator the time as the database tells it: records
+// and deadlines are in database time, so that an instance that takes over
+// reads them as the instance that wrote them did.
+type Clock interface {
+	Now() time.Time
+	// Local returns a time of the database's in the process's clock, in
+	// which the gateways compare deadlines.
+	Local(t time.Time) time.Time
+}
+
+// Timers runs a function at a time of the Clock's. The coordinator arms its
+// timers from the records, so that an instance that takes over arms them
+// again. It arms and cancels them under its lock, which fn takes: At never
+// runs fn before it returns, and cancel never waits for a fn that runs.
+type Timers interface {
+	// At runs fn at t, or as soon as it can when t has passed, and returns
+	// a function that cancels it.
+	At(t time.Time, fn func()) (cancel func())
+}
+
+// Turn is an agent's turn in a round, as the runner starts it (ADR-006):
+// every turn starts from a fresh context, the round's prompt, the agent's
+// skills and the board, read through read_board.
+type Turn struct {
+	// ID names the turn: the runner reports its end with it, and a Start
+	// repeated with it starts the turn once at most.
+	ID    string
+	Run   board.RunID
+	Agent string
+	Round int
+	// WrapUp names the converged hypothesis in a wrap-up round.
+	WrapUp board.EntryID
+	// Deadline is the round's end, in the process's clock.
+	Deadline time.Time
+	// MCP and LLM are the session's tokens at the two gateways.
+	MCP, LLM tokens.Secret
+	// Image is the agent's runtime image, by digest, as the item locked it.
+	Image  string
+	Model  string
+	Skills []string
+	Prompt string
+}
+
+// Turns runs agents' turns: the runner, in sandboxes (Plan 7d). A turn's
+// end is reported once, with its ID, through Coordinator.Exited. The
+// coordinator calls Turns under its lock, which Exited takes: no method calls
+// Exited before it returns, or waits for a turn to end.
+type Turns interface {
+	// Start starts a turn. It fails only when the runner cannot be reached:
+	// a turn whose sandbox fails to start ends crashed.
+	Start(ctx context.Context, t *Turn) error
+	// Stop ends a turn at once.
+	Stop(run board.RunID, agent string)
+	// StopAll ends every turn the instance runs.
+	StopAll()
+}
+
+// Exit is how an agent's turn in a round ended.
+type Exit struct {
+	Run   board.RunID
+	Agent string
+	Round int
+	// Turn is the ID of the turn that ended.
+	Turn  string
+	Class item.TurnClass
+}
+
+// Revisions are the configuration's revisions, as the registry keeps them.
+type Revisions interface {
+	// Current returns the revision new items start from.
+	Current() *config.Revision
+	// At returns the revision of a commit, as an item locked it.
+	At(ctx context.Context, commit string) (*config.Revision, error)
+	// Lock locks a revision with the deployment's runtime images.
+	Lock(rev *config.Revision) (config.Lock, error)
+}
```

- [ ] **Step 4: The coordinator**

Create `internal/work/app/coordinator.go`:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// ErrInvalid marks a request the coordinator refuses, such as an item of a
// type it cannot run.
var ErrInvalid = errors.New("app: invalid request")

// Ports are what the coordinator acts through.
type Ports struct {
	Records   Records
	Boards    *Boards
	Spend     Spend
	Revisions Revisions
	Sessions  Sessions
	Turns     Turns
	Clock     Clock
	Timers    Timers
	IDs       IDs
}

// Options are the coordinator's settings (spec Section 15).
type Options struct {
	// DailyCap is the deployment's daily token cap, in cost-weighted
	// tokens.
	DailyCap int64
	// Grace is how long a round's end waits for the round's write calls,
	// which commit apart from the call's time: the gateways' record
	// timeout [5 s].
	Grace time.Duration
	// SettleWait is how long a round's end waits for the run's model calls
	// in flight [30 s], and SettlePoll how often it looks again [1 s].
	SettleWait time.Duration
	SettlePoll time.Duration
	// Retry is how long a step that failed waits before it is tried again
	// [5 s].
	Retry time.Duration
	// Log receives the steps that failed; nil logs nothing.
	Log *slog.Logger
}

// Coordinator drives work items through their lifecycle (spec Sections 4.1
// and 5): it opens and starts items, opens and ends rounds, and stops runs.
// It writes each decision as a record before it acts on it, and every
// action can be repeated, so that it resumes from the records after a crash.
// One lock serializes its decisions.
type Coordinator struct {
	p    Ports
	opts Options
	log  *slog.Logger
	ctx  context.Context

	mu      sync.Mutex
	runs    map[board.RunID]*running
	retries map[item.ID]func()
}

// running is what the coordinator holds in memory of a run's open round:
// the sessions it issued, the turns that run, by agent, with their ids, the
// turns the runner could not start yet, how the others ended, and the
// round's timers.
type running struct {
	item     item.ID
	round    int
	deadline time.Time
	tokens   map[string]Tokens
	turns    map[string]string
	starting map[string]*Turn
	exits    map[string]item.TurnClass
	ended    *ended
	timers   []func()
}

// ended is how and when a round ended, until its end is recorded.
type ended struct {
	by item.EndedBy
	at time.Time
}

// maxSteps bounds the steps one advance takes, so that a mistake in the
// records cannot loop forever.
const maxSteps = 32

// NewCoordinator returns a coordinator over p.
func NewCoordinator(p *Ports, opts *Options) (*Coordinator, error) {
	if p.missing() {
		return nil, errors.New("app: the coordinator needs every port")
	}
	if opts.DailyCap <= 0 || opts.Grace <= 0 || opts.SettleWait < 0 || opts.SettlePoll <= 0 || opts.Retry <= 0 {
		return nil, errors.New("app: the coordinator needs a daily cap, a grace, a settling poll and a retry above zero")
	}
	c := &Coordinator{p: *p, opts: *opts, log: opts.Log, ctx: context.Background(), runs: map[board.RunID]*running{}, retries: map[item.ID]func(){}}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	return c, nil
}

// missing reports whether a port is unset.
func (p *Ports) missing() bool {
	return p.Records == nil || p.Boards == nil || p.Spend == nil || p.Revisions == nil || p.Sessions == nil ||
		p.Turns == nil || p.Clock == nil || p.Timers == nil || p.IDs == nil
}

// OpenInput is a work item to open.
type OpenInput struct {
	Type  string
	Title string
	// Source says who opens the item: "person", or an inbound source.
	Source string
	// Routing holds trusted values only, which write tools' scope bindings
	// see as the item's (spec Section 3.3).
	Routing    map[string]string
	Principal  string
	Responders []string
}

// source is who opens an item: a lowercase word, as inbound sources are
// named.
var source = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// maxTitleRunes caps an item's title.
const maxTitleRunes = 200

// Open opens a work item and starts it at once (Plan 7b queues items). An
// item opened once the day's spend reached the daily cap is not staffed. Its
// title is redacted with the rules that apply always before it is recorded.
// Once the item's opening is recorded, Open returns its id: what the item's
// start does next is tried again until it succeeds.
func (c *Coordinator) Open(ctx context.Context, in *OpenInput) (item.ID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rev := c.p.Revisions.Current()
	if rev == nil {
		return "", errors.New("app: the registry has no configuration yet")
	}
	if err := runnable(rev, in.Type); err != nil {
		return "", err
	}
	if !source.MatchString(in.Source) {
		return "", fmt.Errorf("%w: an item's source is a lowercase word", ErrInvalid)
	}
	day, err := c.p.Spend.SpentSince(ctx, dayStart(c.p.Clock.Now()))
	if err != nil {
		return "", fmt.Errorf("read the day's spend: %w", err)
	}
	s := &item.State{ID: item.ID(c.p.IDs.NewID()), Incident: item.IncidentID(c.p.IDs.NewID())}
	bodies := []item.Body{{Opened: &item.Opened{
		Type: in.Type, Title: title(rev, in.Title), Source: in.Source, Routing: maps.Clone(in.Routing),
		Principal: in.Principal, Responders: slices.Clone(in.Responders), Arm: item.ArmVisible, How: item.NoWindow,
	}}}
	if day >= c.opts.DailyCap {
		bodies = append(bodies, item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}})
	}
	if err := c.record(ctx, s, "", bodies...); err != nil {
		return "", err
	}
	// The item's start is the coordinator's from here on, not the request's.
	c.advance(c.ctx, s.ID)
	return s.ID, nil
}

// Exited reports how an agent's turn ended. An end reported twice, late, for
// a round that is over, of another turn, or as a class the runner does not
// report changes nothing. A round ends once every turn ended, unless each
// ended at the model vendor: such a round ends at its deadline, so that a
// vendor's outage cannot spend the run's rounds in seconds.
func (c *Coordinator) Exited(ctx context.Context, e Exit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[e.Run]
	if r == nil || r.round != e.Round || r.ended != nil {
		return
	}
	if id, ok := r.turns[e.Agent]; !ok || id != e.Turn {
		return
	}
	if !reported(e.Class) {
		c.log.WarnContext(ctx, "a turn's end of a class the runner does not report is ignored", "run", e.Run, "agent", e.Agent, "class", e.Class)
		return
	}
	delete(r.turns, e.Agent)
	r.exits[e.Agent] = e.Class
	c.whenAllEnded(e.Run, r)
}

// reported reports whether the runner reports a turn's end as class.
func reported(class item.TurnClass) bool {
	switch class {
	case item.TurnEnded, item.TurnFinished, item.TurnInterrupted, item.TurnRefused, item.TurnVendor, item.TurnCrashed:
		return true
	}
	return false
}

func (c *Coordinator) whenAllEnded(run board.RunID, r *running) {
	if len(r.turns) > 0 || len(r.starting) > 0 {
		return
	}
	for _, class := range r.exits {
		if class != item.TurnVendor {
			c.end(run, r, item.EndedByTurns, c.p.Clock.Now())
			return
		}
	}
}

// deadline ends a round at its deadline: the turns still running are
// stopped, and count as interrupted; those the runner never started count
// as unstarted.
func (c *Coordinator) deadline(run board.RunID, round int, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[run]
	if r == nil || r.round != round || r.ended != nil {
		return
	}
	for agent := range r.turns {
		c.p.Turns.Stop(run, agent)
		r.exits[agent] = item.TurnInterrupted
	}
	for agent := range r.starting {
		c.p.Turns.Stop(run, agent)
		r.exits[agent] = item.TurnUnstarted
	}
	clear(r.turns)
	clear(r.starting)
	c.end(run, r, item.EndedByDeadline, at)
}

// end marks a round ended, and lets its write calls have their grace before
// its end is decided.
func (c *Coordinator) end(run board.RunID, r *running, by item.EndedBy, at time.Time) {
	r.ended = &ended{by: by, at: at}
	round := r.round
	c.arm(r, c.p.Clock.Now().Add(c.opts.Grace), func() { c.settle(run, round) })
}

// settle waits for the run's model calls in flight, up to the settling
// wait, then decides the round's end.
func (c *Coordinator) settle(run board.RunID, round int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[run]
	if r == nil || r.round != round || r.ended == nil {
		return
	}
	again := func(after time.Duration) { c.arm(r, c.p.Clock.Now().Add(after), func() { c.settle(run, round) }) }
	n, err := c.p.Spend.InFlight(c.ctx, string(run))
	if err != nil {
		c.failed(c.ctx, r.item, err)
		again(c.opts.Retry)
		return
	}
	if n > 0 && c.p.Clock.Now().Before(r.ended.at.Add(c.opts.Grace+c.opts.SettleWait)) {
		again(c.opts.SettlePoll)
		return
	}
	if err := c.decide(c.ctx, run, r, n == 0); err != nil {
		c.failed(c.ctx, r.item, err)
		again(c.opts.Retry)
	}
}

// decide records how a round ended and what follows it, then acts on it.
// Before it decides, it notices on the board each model call of the run
// that was refused, failed or whose outcome is unknown, once per call.
func (c *Coordinator) decide(ctx context.Context, run board.RunID, r *running, settled bool) error {
	s, err := c.load(ctx, r.item)
	if err != nil {
		return err
	}
	if l := s.Run.Latest(); l == nil || l.Ended != nil || l.Opened.Round != r.round {
		c.advance(ctx, s.ID)
		return nil
	}
	spent, err := c.p.Spend.Spent(ctx, string(run))
	if err != nil {
		return fmt.Errorf("read the run's spend: %w", err)
	}
	day, err := c.p.Spend.SpentSince(ctx, dayStart(c.p.Clock.Now()))
	if err != nil {
		return fmt.Errorf("read the day's spend: %w", err)
	}
	failed, err := c.p.Spend.Failed(ctx, string(run))
	if err != nil {
		return fmt.Errorf("read the run's failed model calls: %w", err)
	}
	report, err := c.p.Boards.Report(ctx, run, r.round)
	if err != nil {
		return err
	}
	for _, call := range failed {
		if report.Noticed(call.ID) {
			continue
		}
		n := &board.Notice{Code: noticeCode(call.Status), Agent: call.Agent, CallID: call.ID}
		if _, err := c.p.Boards.Notice(ctx, run, board.Coordinator, n); err != nil {
			return err
		}
	}
	re := s.Run.End(&item.Ending{
		EndedBy: r.ended.by, EndedAt: r.ended.at, Turns: maps.Clone(r.exits), Spent: spent, Settled: settled,
		DaySpent: day, DailyCap: c.opts.DailyCap, Report: report,
	})
	if err := c.record(ctx, s, run, item.Body{RoundEnded: &re}); err != nil {
		return err
	}
	c.cancel(r)
	c.advance(ctx, s.ID)
	return nil
}

// noticeCode is the notice of a model call's outcome.
func noticeCode(status string) board.NoticeCode {
	switch status {
	case "refused":
		return board.NoticeModelRefused
	case "error":
		return board.NoticeModelFailed
	}
	return board.NoticeModelUnknown
}

// advance takes an item through every step its records allow now, until it
// waits for a round's end or awaits its close. A step that fails is tried
// again after the retry wait.
func (c *Coordinator) advance(ctx context.Context, id item.ID) {
	if cancel := c.retries[id]; cancel != nil {
		cancel()
		delete(c.retries, id)
	}
	for range maxSteps {
		s, err := c.load(ctx, id)
		if err == nil {
			var done bool
			if done, err = c.step(ctx, s); err == nil && !done {
				continue
			}
		}
		if err != nil {
			c.failed(ctx, id, err)
			c.retries[id] = c.p.Timers.At(c.p.Clock.Now().Add(c.opts.Retry), func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				c.advance(c.ctx, id)
			})
		}
		return
	}
	c.failed(ctx, id, errors.New("app: the item took more steps at once than an item can"))
}

func (c *Coordinator) failed(ctx context.Context, id item.ID, err error) {
	c.log.ErrorContext(ctx, "a step of a work item failed; it is tried again", "item", id, "err", err)
}

// step takes an item's next step, and reports whether it must wait: for a
// round's end, or for the item's close.
func (c *Coordinator) step(ctx context.Context, s *item.State) (wait bool, err error) {
	switch {
	case s.Closing != nil:
		c.forget(s)
		return true, nil
	case s.Started == nil:
		return false, c.start(ctx, s)
	case s.Run == nil:
		return false, c.create(ctx, s)
	case s.Run.Stopped != nil:
		return false, c.close(ctx, s)
	}
	l := s.Run.Latest()
	switch {
	case l == nil && len(s.Run.Created.Agents) == 0:
		return false, c.unstaffed(ctx, s)
	case l == nil || l.Ended != nil && l.Ended.Decision.Next != item.NextStop:
		return false, c.open(ctx, s)
	case l.Ended == nil:
		return true, c.hold(ctx, s)
	}
	a, err := c.p.Boards.Assess(ctx, s.Run.ID, time.Time{})
	if err != nil {
		return false, err
	}
	stop := s.Run.Stop(a.Conclusion)
	return false, c.record(ctx, s, s.Run.ID, item.Body{RunStopped: &stop})
}

// start locks the configuration the item runs under; an item whose type
// the current revision cannot run is not staffed.
func (c *Coordinator) start(ctx context.Context, s *item.State) error {
	rev := c.p.Revisions.Current()
	if rev == nil {
		return errors.New("app: the registry has no configuration yet")
	}
	if runnable(rev, s.Opened.Type) != nil {
		return c.record(ctx, s, "", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}})
	}
	lock, err := c.p.Revisions.Lock(rev)
	if err != nil {
		return fmt.Errorf("lock revision %s: %w", rev.Commit(), err)
	}
	return c.record(ctx, s, "", item.Body{Started: &item.Started{Commit: lock.Commit, Digest: lock.Digest, Images: lock.RuntimeImages}})
}

// create creates the item's run, the primary run that people see, with the
// settings and budget of its type, inviting the agents subscribed to it.
func (c *Coordinator) create(ctx context.Context, s *item.State) error {
	rev, err := c.p.Revisions.At(ctx, s.Started.Commit)
	if err != nil {
		return fmt.Errorf("read revision %s: %w", s.Started.Commit, err)
	}
	ws := rev.WorkItemType(s.Opened.Type).Settings()
	return c.record(ctx, s, board.RunID(c.p.IDs.NewID()), item.Body{RunCreated: &item.RunCreated{
		Class: item.ClassPrimary, Mode: item.ModeVisible, Settings: boardSettings(&ws),
		Budget:     item.Budget{WallClock: ws.Budget.WallClock, Tokens: ws.Budget.Tokens, Rounds: ws.Budget.Rounds},
		RoundLimit: ws.RoundLimit, Agents: invited(rev, s.Opened.Type, ws.MaxAgents),
	}})
}

// unstaffed stops a run with no agent to invite, and says so on its board.
func (c *Coordinator) unstaffed(ctx context.Context, s *item.State) error {
	if _, err := c.p.Boards.Notice(ctx, s.Run.ID, board.Coordinator, &board.Notice{Code: board.NoticeNoAgents}); err != nil {
		return err
	}
	stop := s.Run.Stop("")
	return c.record(ctx, s, s.Run.ID, item.Body{RunStopped: &stop})
}

// open opens the run's next round as its last decided, or its first after
// the evidence the item opened with.
func (c *Coordinator) open(ctx context.Context, s *item.State) error {
	if s.Run.Latest() == nil {
		rev, err := c.p.Revisions.At(ctx, s.Started.Commit)
		if err != nil {
			return fmt.Errorf("read revision %s: %w", s.Started.Commit, err)
		}
		in := &board.PlatformEvidenceInput{Claim: triggerClaim(rev, s.Opened), Ref: "item:" + string(s.ID), Source: board.Source(s.Opened.Source)}
		if _, err := c.p.Boards.PostPlatformEvidence(ctx, s.Run.ID, in); err != nil {
			return err
		}
	}
	o := s.Run.Open(c.p.Clock.Now())
	return c.record(ctx, s, s.Run.ID, item.Body{RoundOpened: &o})
}

// hold acts on the run's open round, once per round on this instance: it
// opens the round on the board, then gives the agents' sessions new tokens
// and puts them in it, ends those of agents that left, starts the turns of
// the agents that have not ended theirs, and arms the round's deadline.
func (c *Coordinator) hold(ctx context.Context, s *item.State) error {
	run := s.Run
	o := &run.Latest().Opened
	r := c.runs[run.ID]
	if r == nil {
		r = &running{item: s.ID, tokens: map[string]Tokens{}}
		c.runs[run.ID] = r
	}
	if r.round == o.Round {
		return nil
	}
	if _, err := c.p.Boards.StartRound(ctx, run.ID, &board.RoundStart{Round: o.Round, WrapUp: o.WrapUp}); err != nil {
		return err
	}
	report, err := c.p.Boards.Report(ctx, run.ID, o.Round)
	if err != nil {
		return err
	}
	rev, err := c.p.Revisions.At(ctx, s.Started.Commit)
	if err != nil {
		return fmt.Errorf("read revision %s: %w", s.Started.Commit, err)
	}
	for agent := range r.tokens {
		if !slices.Contains(o.Agents, agent) {
			c.p.Sessions.End(run.ID, agent)
			delete(r.tokens, agent)
		}
	}
	for _, agent := range o.Agents {
		// Each round's sessions have tokens of their own: a turn of an
		// earlier round still running cannot act in this one.
		r.tokens[agent] = c.p.Sessions.Issue(&Session{Run: run.ID, Agent: agent, Commit: s.Started.Commit, Item: s.Opened.Routing})
		if err := c.p.Sessions.Move(run.ID, agent, o.Round, c.p.Clock.Local(o.Deadline), o.Budget); err != nil {
			return err
		}
	}
	c.cancel(r)
	r.round, r.deadline, r.ended = o.Round, o.Deadline, nil
	r.turns, r.starting, r.exits = map[string]string{}, map[string]*Turn{}, map[string]item.TurnClass{}
	for _, agent := range o.Agents {
		if slices.Contains(report.Facts.Ended, agent) {
			r.exits[agent] = item.TurnEnded
			continue
		}
		c.begin(ctx, run.ID, r, c.turn(s, rev, o, agent, r.tokens[agent]))
	}
	round, deadline := o.Round, o.Deadline
	c.arm(r, deadline, func() { c.deadline(run.ID, round, deadline) })
	c.whenAllEnded(run.ID, r)
	return nil
}

// begin starts a turn. A turn the runner could not start is tried again
// after the retry wait, with the same id, while its round lasts.
func (c *Coordinator) begin(ctx context.Context, run board.RunID, r *running, t *Turn) {
	if err := c.p.Turns.Start(ctx, t); err != nil {
		c.failed(ctx, r.item, fmt.Errorf("start the turn of %s: %w", t.Agent, err))
		r.starting[t.Agent] = t
		round, agent := r.round, t.Agent
		c.arm(r, c.p.Clock.Now().Add(c.opts.Retry), func() { c.startAgain(run, round, agent) })
		return
	}
	delete(r.starting, t.Agent)
	r.turns[t.Agent] = t.ID
}

// startAgain tries again to start a turn the runner could not start, while
// its round lasts.
func (c *Coordinator) startAgain(run board.RunID, round int, agent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[run]
	if r == nil || r.round != round || r.ended != nil || r.starting[agent] == nil || !c.p.Clock.Now().Before(r.deadline) {
		return
	}
	c.begin(c.ctx, run, r, r.starting[agent])
}

// close acts on a run that stopped: its sessions and turns end, then the
// stop entry, with the conclusion its record holds, ends its board, which
// may take tries, and the item awaits its close, with the run's reason as
// the item's.
func (c *Coordinator) close(ctx context.Context, s *item.State) error {
	run := s.Run
	st := run.Stopped
	c.p.Sessions.EndRun(run.ID)
	if l := run.Latest(); l != nil {
		for _, agent := range l.Opened.Agents {
			c.p.Turns.Stop(run.ID, agent)
		}
	}
	in := &board.StopInput{Reason: string(st.Reason), Conclusion: st.Conclusion, Rounds: st.Rounds, WallClock: st.WallClock, Tokens: st.Spent}
	if _, err := c.p.Boards.Stop(ctx, run.ID, in); err != nil {
		return err
	}
	return c.record(ctx, s, "", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.ItemReason(st.Reason)}})
}

// forget drops what the coordinator holds of an item that awaits its close.
func (c *Coordinator) forget(s *item.State) {
	if s.Run == nil {
		return
	}
	if r := c.runs[s.Run.ID]; r != nil {
		c.cancel(r)
		delete(c.runs, s.Run.ID)
	}
}

// turn is an agent's turn in a round.
func (c *Coordinator) turn(s *item.State, rev *config.Revision, o *item.RoundOpened, agent string, t Tokens) *Turn {
	turn := &Turn{
		ID: c.p.IDs.NewID(), Run: s.Run.ID, Agent: agent, Round: o.Round, WrapUp: o.WrapUp, Deadline: c.p.Clock.Local(o.Deadline),
		MCP: t.MCP, LLM: t.LLM,
	}
	if a := rev.Agent(agent); a != nil {
		turn.Image, turn.Model, turn.Skills = s.Started.Images[a.Spec.Runtime], a.Spec.Model, slices.Clone(a.Spec.Skills)
	}
	turn.Prompt = prompt(turn, roundsLeft(s.Run))
	return turn
}

// roundsLeft is how many normal rounds the run's budget allows after its open
// round: rounds that ended by an outage count toward no budget, so a round's
// number does not say it.
func roundsLeft(run *item.Run) int {
	used := 0
	for i := len(run.Rounds) - 1; i >= 0; i-- {
		if e := run.Rounds[i].Ended; e != nil {
			used = e.Rounds
			break
		}
	}
	return max(run.Created.Budget.Rounds-used-1, 0)
}

// prompt is a turn's opening message. It holds the platform's words and ids
// only: what agents and people wrote reaches the model through read_board,
// which marks it as data.
func prompt(t *Turn, left int) string {
	if t.WrapUp != "" {
		return fmt.Sprintf("Round %d is the wrap-up round of this investigation: hypothesis %s converged. Read the board with read_board. "+
			"Post evidence only if it refutes that hypothesis, then call end_turn.", t.Round, t.WrapUp)
	}
	next := fmt.Sprintf("at most %d more rounds follow it", left)
	switch left {
	case 0:
		next = "it is the last"
	case 1:
		next = "at most one more round follows it"
	}
	return fmt.Sprintf("Round %d of this investigation: %s. Read the board with read_board, investigate with your tools, "+
		"post what you find, and call end_turn when you are done.", t.Round, next)
}

// load folds an item's records.
func (c *Coordinator) load(ctx context.Context, id item.ID) (*item.State, error) {
	records, err := c.p.Records.Load(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load item %s: %w", id, err)
	}
	return item.Fold(records)
}

// record appends records of the item, each checked first against the
// item's records as committed: the lifecycle's rules refuse a record out of
// order, as a decision made twice would be.
func (c *Coordinator) record(ctx context.Context, s *item.State, run board.RunID, bodies ...item.Body) error {
	_, err := c.p.Records.Append(ctx, s.ID, func(history []item.Record) ([]item.Draft, error) {
		check, err := item.Fold(history)
		if err != nil {
			return nil, err
		}
		drafts := make([]item.Draft, 0, len(bodies))
		for i := range bodies {
			b := bodies[i]
			b.V = item.Version
			d := item.Draft{ID: c.p.IDs.NewID(), Item: s.ID, Incident: s.Incident, Run: run, Body: b}
			r := item.Record{Seq: int64(len(history) + i + 1), ID: d.ID, Item: d.Item, Incident: d.Incident, Run: run, Body: b}
			if err := check.Apply(&r); err != nil {
				return nil, err
			}
			drafts = append(drafts, d)
		}
		return drafts, nil
	})
	if err != nil {
		return fmt.Errorf("record item %s: %w", s.ID, err)
	}
	return nil
}

func (c *Coordinator) arm(r *running, at time.Time, fn func()) {
	r.timers = append(r.timers, c.p.Timers.At(at, fn))
}

func (c *Coordinator) cancel(r *running) {
	for _, cancel := range r.timers {
		cancel()
	}
	r.timers = nil
}

// runnable refuses a type the coordinator cannot run: one that does not
// exist, or whose key reads its values from a tool when an item opens,
// which Plan 7c builds.
func runnable(rev *config.Revision, typ string) error {
	wt := rev.WorkItemType(typ)
	if wt == nil {
		return fmt.Errorf("%w: the configuration has no such work item type", ErrInvalid)
	}
	for _, f := range wt.Settings().Key {
		if f.Values.Read != nil {
			return fmt.Errorf("%w: the type's key reads its values when an item opens, which the coordinator does not do yet", ErrInvalid)
		}
	}
	return nil
}

// boardSettings are a run's board settings, from its type's.
func boardSettings(ws *config.WorkSettings) board.Settings {
	schema := key.Schema{Fields: make([]key.Field, 0, len(ws.Key))}
	for _, f := range ws.Key {
		schema.Fields = append(schema.Fields, key.Field{Name: f.Name, Values: slices.Clone(f.Values.List)})
	}
	return board.Settings{
		Schema: schema,
		Convergence: board.Convergence{
			MinContributors: ws.Convergence.MinContributors, MinSources: ws.Convergence.MinSources, RefutationMargin: ws.Convergence.RefutationMargin,
		},
		Limits: board.Limits{EntriesPerRound: ws.Limits.EntriesPerRound, OpenClaims: ws.Limits.OpenClaims, LeaseRounds: ws.Limits.LeaseRounds},
	}
}

// invited returns the agents an item of type typ invites: those subscribed
// to the type, in the revision's order, but those that pin a yanked skill
// version, up to max.
func invited(rev *config.Revision, typ string, maxAgents int) []string {
	out := []string{}
	for _, a := range rev.Agents() {
		if len(out) == maxAgents {
			break
		}
		if slices.Contains(a.Spec.Subscribe.WorkItemTypes, typ) && len(rev.YankedPins(a.Metadata.Name)) == 0 {
			out = append(out, a.Metadata.Name)
		}
	}
	return out
}

// always returns the redaction rules that apply always: the session tokens'
// rule, then the configuration's.
func always(rev *config.Revision) *redact.Redactor {
	rules := []redact.Rule{redact.SessionTokens()}
	for _, r := range rev.AlwaysRedactionRules() {
		rules = append(rules, redact.FromConfig(r))
	}
	return redact.New(rules...)
}

// title redacts an item's title as it is recorded, cut to maxTitleRunes. A
// title whose escapes may hide what a rule matches is replaced whole.
func title(rev *config.Revision, raw string) string {
	red := always(rev)
	if red.Escaped(raw) {
		return "[a title the redaction rules could not read]"
	}
	return cut(red.Text(strings.TrimSpace(raw)), maxTitleRunes)
}

// triggerClaim is the evidence an item opens with, composed by rule: who
// opened it, its title and its routing values, redacted.
func triggerClaim(rev *config.Revision, o *item.Opened) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Opened by %s: %s", o.Source, o.Title)
	for _, k := range slices.Sorted(maps.Keys(o.Routing)) {
		fmt.Fprintf(&b, "; %s=%s", k, o.Routing[k])
	}
	return cut(always(rev).Text(b.String()), board.MaxClaimRunes)
}

// cut cuts s to at most n characters, the last of them a mark that it was
// cut.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// dayStart is the start of t's day, UTC, from which the daily cap counts.
func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test -race ./internal/work/... && golangci-lint run ./...`
Expected: every package `ok`; `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add internal/work/app
git commit -m "feat(work): the coordinator: an item opened and started, its rounds opened and ended as recorded decisions, and its run stopped"
```

### Task 8: Takeover and fencing

Decisions 3 and 9. The coordinator acts only from a takeover until it is fenced, and a takeover carries every item on from its records, under the holding it is given. The tests' harness splits what the database keeps from what an instance loses when it crashes, and adds timers that run on goroutines of their own.

**Files:**
- Create: `internal/work/app/restart_test.go`
- Modify: `internal/work/app/coordinator.go`, `internal/work/app/ports.go`, `internal/work/app/harness_test.go`

**Interfaces:**
- Consumes: the coordinator (Task 7), `app.Term` (Task 6).
- Produces: `app.ErrNotHeld`; `(*Coordinator).TakeOver(ctx, Term) error`, which returns `ErrLeaseLost` once it finds the lease gone; `(*Coordinator).Fence()`; `(*Coordinator).OnLost(func(epoch int64))`.

- [ ] **Step 1: Write the failing tests**

Apply this change to `internal/work/app/harness_test.go`, which starts each instance with a takeover and can crash it:

```diff
diff --git a/internal/work/app/harness_test.go b/internal/work/app/harness_test.go
index 6e27e0e..70a659c 100644
--- a/internal/work/app/harness_test.go
+++ b/internal/work/app/harness_test.go
@@ -7,10 +7,11 @@ import (
 	"fmt"
 	"slices"
 	"sort"
 	"strings"
 	"sync"
+	"sync/atomic"
 	"testing"
 	"time"
 
 	"github.com/stretchr/testify/require"
 
@@ -232,10 +233,46 @@ func (c *clock) Advance(d time.Duration) {
 		c.mu.Unlock()
 		due.fn()
 	}
 }
 
+// goTimers runs each timer's function on a goroutine of its own, as a real
+// clock's timers do, at the clock's times; wait waits until none runs and
+// none is due.
+type goTimers struct {
+	c  *clock
+	wg sync.WaitGroup
+}
+
+func (g *goTimers) At(t time.Time, fn func()) func() {
+	return g.c.At(t, func() {
+		g.wg.Go(fn)
+	})
+}
+
+func (g *goTimers) wait() {
+	for {
+		g.wg.Wait()
+		if !g.c.due() {
+			return
+		}
+		g.c.Advance(0)
+	}
+}
+
+// due reports whether a timer armed and not cancelled is due.
+func (c *clock) due() bool {
+	c.mu.Lock()
+	defer c.mu.Unlock()
+	for _, tm := range c.timers {
+		if !tm.cancelled && !tm.at.After(c.now) {
+			return true
+		}
+	}
+	return false
+}
+
 // pending counts the timers armed and not cancelled.
 func (c *clock) pending() int {
 	c.mu.Lock()
 	defer c.mu.Unlock()
 	n := 0
@@ -365,53 +402,92 @@ func (s *turns) take() []app.Turn {
 	return out
 }
 
 // harness is a coordinator over stores in memory, the clock, and agents
 // that act through the MCP gateway with the tokens their turns were given.
+// The stores, the clock, the spend and the registry of configurations are
+// the database's and the world's; the coordinator, the live sessions, the
+// gateway and the turns are one instance's, which a crash loses.
 type harness struct {
-	t        *testing.T
+	t       *testing.T
+	clock   *clock
+	records *failing
+	store   *failingStore
+	boards  *app.Boards
+	audit   *actionsmemory.Audit
+	spend   *spend
+	revs    *revisions
+	cc      *celeval.Conditions
+	ids     *ids
+	opts    app.Options
+	scripts map[string]script
+	epoch   int64
+	// expired is when the last instance's holding of the lease expired:
+	// when it crashed.
+	expired time.Time
+	// timers are the instances' timers: the clock's, unless a test gives
+	// others.
+	timers app.Timers
+
 	c        *app.Coordinator
-	clock    *clock
-	cc       *celeval.Conditions
-	records  *failing
-	store    *failingStore
-	boards   *app.Boards
 	registry *sessions.Registry
 	gw       *actionsapp.Gateway
 	turns    *turns
-	spend    *spend
-	revs     *revisions
-	opts     app.Options
-	scripts  map[string]script
 }
 
 // failing is the records store, which can fail an append: fail returns the
 // error for the nth append, counting from 1, or nil. As a database does, it
 // refuses an append whose context ended.
 type failing struct {
 	*memory.Records
 	mu   sync.Mutex
 	n    int
 	fail func(n int) error
+	// hold, when set, makes an append wait until it is closed or its
+	// context ends, as a store that hangs; waiting counts those that wait.
+	hold    chan struct{}
+	waiting atomic.Int32
 }
 
 func (f *failing) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
 	f.mu.Lock()
 	f.n++
-	n, fail := f.n, f.fail
+	n, fail, hold := f.n, f.fail, f.hold
 	f.mu.Unlock()
 	if fail != nil {
 		if err := fail(n); err != nil {
 			return nil, err
 		}
 	}
+	if hold != nil {
+		f.waiting.Add(1)
+		select {
+		case <-hold:
+		case <-ctx.Done():
+		}
+		f.waiting.Add(-1)
+	}
 	if err := ctx.Err(); err != nil {
 		return nil, err
 	}
 	return f.Records.Append(ctx, id, decide)
 }
 
+// hang makes appends wait, until the returned function lets them go.
+func (f *failing) hang() (letGo func()) {
+	f.mu.Lock()
+	defer f.mu.Unlock()
+	hold := make(chan struct{})
+	f.hold = hold
+	return sync.OnceFunc(func() {
+		f.mu.Lock()
+		f.hold = nil
+		f.mu.Unlock()
+		close(hold)
+	})
+}
+
 func (f *failing) set(fail func(n int) error) {
 	f.mu.Lock()
 	defer f.mu.Unlock()
 	f.fail = fail
 }
@@ -478,33 +554,21 @@ func newHarness(t *testing.T, change ...func(*app.Options)) *harness {
 	require.NoError(t, err)
 	fsys, err := memfs.New(configuration(t))
 	require.NoError(t, err)
 	rev, err := config.Build(fsys, commit, cc)
 	require.NoError(t, err)
-	h := &harness{
-		t: t, clock: newClock(), cc: cc, registry: sessions.New(), turns: &turns{}, spend: newSpend(),
-		revs: &revisions{rev: rev, all: map[string]*config.Revision{commit: rev}}, scripts: map[string]script{},
-	}
+	h := &harness{t: t, clock: newClock(), spend: newSpend(), revs: &revisions{rev: rev, all: map[string]*config.Revision{commit: rev}}, cc: cc, ids: &ids{}, scripts: map[string]script{}}
+	h.timers = h.clock
 	h.records = &failing{Records: memory.NewRecords(h.clock.Now)}
 	h.store = &failingStore{Store: memory.NewStore(h.clock.Now)}
-	audit := actionsmemory.NewAudit()
-	idgen := &ids{}
-	h.boards = app.NewBoards(h.store, audit, h.records, idgen)
-	h.gw, err = actionsapp.New(t.Context(), &actionsapp.Ports{
-		Sessions: h.registry.MCP(), Revisions: h.revs, Upstream: logs{}, Audit: audit, Hides: &actionsmemory.Hides{},
-		Conditions: cc, IDs: idgen, Limits: limits.New(limits.Defaults, nil, h.clock.Now), Now: h.clock.Now, Board: h.boards,
-	}, &actionsapp.Defaults)
-	require.NoError(t, err)
+	h.audit = actionsmemory.NewAudit()
+	h.boards = app.NewBoards(h.store, h.audit, h.records, h.ids)
 	h.opts = app.Options{DailyCap: 1_000_000, Grace: 5 * time.Second, SettleWait: 30 * time.Second, SettlePoll: time.Second, Retry: 5 * time.Second}
 	for _, f := range change {
 		f(&h.opts)
 	}
-	h.c, err = app.NewCoordinator(&app.Ports{
-		Records: h.records, Boards: h.boards, Spend: h.spend, Revisions: h.revs, Sessions: h.registry, Turns: h.turns,
-		Clock: h.clock, Timers: h.clock, IDs: idgen,
-	}, &h.opts)
-	require.NoError(t, err)
+	require.NoError(t, h.boot())
 	return h
 }
 
 // revision builds a revision of the harness's configuration, as change
 // edits its files, under commit, and keeps it in the registry.
@@ -520,10 +584,41 @@ func (h *harness) revision(commit string, change func(files map[string][]byte))
 	defer h.revs.mu.Unlock()
 	h.revs.all[commit] = rev
 	return rev
 }
 
+// boot starts an instance: a coordinator, its sessions, its MCP gateway and
+// its turns, which takes the lease over.
+func (h *harness) boot() error {
+	h.t.Helper()
+	h.registry, h.turns = sessions.New(), &turns{}
+	var err error
+	h.gw, err = actionsapp.New(h.t.Context(), &actionsapp.Ports{
+		Sessions: h.registry.MCP(), Revisions: h.revs, Upstream: logs{}, Audit: h.audit, Hides: &actionsmemory.Hides{},
+		Conditions: h.cc, IDs: h.ids, Limits: limits.New(limits.Defaults, nil, h.clock.Now), Now: h.clock.Now, Board: h.boards,
+	}, &actionsapp.Defaults)
+	require.NoError(h.t, err)
+	h.c, err = app.NewCoordinator(h.ports(), &h.opts)
+	require.NoError(h.t, err)
+	h.epoch++
+	return h.c.TakeOver(h.t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.expired})
+}
+
+func (h *harness) ports() *app.Ports {
+	return &app.Ports{
+		Records: h.records, Boards: h.boards, Spend: h.spend, Revisions: h.revs, Sessions: h.registry, Turns: h.turns,
+		Clock: h.clock, Timers: h.timers, IDs: h.ids,
+	}
+}
+
+// crash loses the instance: its timers, its sessions and its turns die with
+// it.
+func (h *harness) crash() {
+	h.c.Fence()
+	h.expired = h.clock.Now()
+}
+
 // open opens an incident, as a person does.
 func (h *harness) open(title string) item.ID {
 	h.t.Helper()
 	id, err := h.c.Open(h.t.Context(), &app.OpenInput{Type: "incident", Title: title, Source: "person", Routing: map[string]string{"service": "checkout"}})
 	require.NoError(h.t, err)
```

Create `internal/work/app/restart_test.go`:

```go
package app_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// An instance that takes over a round in progress stops the turns and ends
// the sessions it finds, then starts fresh ones for the agents that have
// not ended their turn on the board: a token of the last instance is
// unknown to the new one.
func TestATakeoverStartsFreshTurnsForTheRoundInProgress(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	first := h.turns.take()
	h.turns.queued = append(h.turns.queued, first[0])
	h.play()

	h.crash()
	require.NoError(t, h.boot())

	restarted := h.turns.started
	require.Len(t, restarted, 1, "the agent that ended its turn gets none")
	assert.Equal(t, runtime.Name, restarted[0].Agent)
	assert.Equal(t, 1, restarted[0].Round)
	assert.NotEqual(t, first[1].MCP.Reveal(), restarted[0].MCP.Reveal())
	_, err := h.registry.MCP().Session(t.Context(), first[1].MCP.Reveal())
	require.ErrorIs(t, err, actionsapp.ErrNoSession)
	_, err = h.registry.MCP().Session(t.Context(), restarted[0].MCP.Reveal())
	require.NoError(t, err)
	h.round()
	s := h.state(id)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, s.Run.Rounds[0].Ended.Turns)
	assert.Len(t, roundEntries(h.events(s.Run.ID)), 1, "the board's round entry once")
}

// A round whose deadline passed while no instance held the lease ends by
// an outage: no turn counts, it counts toward no budget, it is never quiet,
// and the next round opens.
func TestARoundWhoseDeadlinePassedWithoutAHolderEndsByOutage(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	h.crash()
	h.clock.Advance(3 * time.Minute)

	require.NoError(t, h.boot())

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	ended := s.Run.Rounds[0].Ended
	assert.Equal(t, item.EndedByOutage, ended.EndedBy)
	assert.Empty(t, ended.Turns)
	assert.Zero(t, ended.Rounds)
	assert.Zero(t, ended.WallClock)
	assert.False(t, ended.Quiet)
	assert.Len(t, h.turns.started, 2)
	assert.Equal(t, 2, h.turns.started[0].Round)
	assert.Contains(t, h.turns.started[0].Prompt, "Round 2 of this investigation: at most 2 more rounds follow it", "the outage round counts toward no budget")
}

// A round whose deadline passed while its holder still held the lease, as
// when the holder died in its write calls' grace, ends by its deadline on
// the next instance: the turns that ended on the board ended, the others
// were interrupted, and the round counts toward the budget.
func TestARoundWhoseHolderDiedAfterItsDeadlineEndsByItsDeadline(t *testing.T) {
	h := newHarness(t)
	h.scripts[runtime.Name] = ending("")
	id := h.open("checkout fails")
	h.play()
	h.clock.Advance(2*time.Minute + 2*time.Second)
	h.crash()
	h.clock.Advance(time.Minute)

	require.NoError(t, h.boot())

	ended := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnInterrupted}, ended.Turns)
	assert.Equal(t, 1, ended.Rounds)
	assert.Equal(t, 2*time.Minute, ended.WallClock)
	assert.True(t, ended.Unseen, "this instance did not see its turns")
	assert.False(t, ended.Quiet)
}

// A round no holding acted on, whose holding lost the lease only after its
// deadline, ends by its deadline on the next instance, but is never quiet:
// the instance that ends it did not see its turns, which here never ran, so
// the run goes on. The board's store failed from the round's entry on.
func TestARoundNoHoldingActedOnIsNeverQuiet(t *testing.T) {
	h := newHarness(t)
	h.store.set(func(n int) error {
		if n >= 2 {
			return errors.New("the board's store is down")
		}
		return nil
	})
	id := h.open("checkout fails")
	h.clock.Advance(3 * time.Minute)
	h.crash()
	h.store.set(nil)
	h.clock.Advance(time.Minute)

	require.NoError(t, h.boot())

	s := h.state(id)
	ended := s.Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.True(t, ended.Unseen)
	assert.False(t, ended.Quiet)
	assert.Nil(t, s.Run.Stopped, "the run goes on")
	require.Len(t, s.Run.Rounds, 2)
	assert.Len(t, h.turns.started, 2, "round 2's turns started")
}

// A round whose deadline falls on the last holding's expiry ended under that
// holding, by its deadline; one whose deadline falls on this holding's start
// ended while no instance held the lease, by an outage.
func TestAMissedRoundOnTheEdgesOfTheHoldings(t *testing.T) {
	onExpiry := newHarness(t)
	id := onExpiry.open("checkout fails")
	onExpiry.clock.Advance(2 * time.Minute)
	onExpiry.crash()
	onExpiry.clock.Advance(time.Minute)
	require.NoError(t, onExpiry.boot())
	onStart := newHarness(t)
	other := onStart.open("checkout fails")
	onStart.clock.Advance(time.Minute)
	onStart.crash()
	onStart.clock.Advance(time.Minute)

	require.NoError(t, onStart.boot())

	assert.Equal(t, item.EndedByDeadline, onExpiry.state(id).Run.Rounds[0].Ended.EndedBy)
	assert.Equal(t, item.EndedByOutage, onStart.state(other).Run.Rounds[0].Ended.EndedBy)
}

// A takeover whose holding ended before it began, as when the leader lost the
// lease while the coordinator waited for its lock, acts on nothing, and the
// coordinator stays fenced.
func TestATakeoverWhoseHoldingEndedActsOnNothing(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")
	h.crash()
	started := len(h.turns.started)
	ended, cancel := context.WithCancel(t.Context())
	cancel()

	err := h.c.TakeOver(ended, app.Term{Epoch: 2, At: h.clock.Now(), Previous: h.clock.Now()})

	require.ErrorIs(t, err, context.Canceled)
	_, err = h.c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Len(t, h.turns.started, started)
}

// An item's opening that waits on a store gives way once the holding ends,
// so that the fence waits for nothing: the instance's sessions and turns end
// with the lease, though the request goes on.
func TestAnOpeningGivesWayOnceTheHoldingEnds(t *testing.T) {
	h := newHarness(t)
	h.crash()
	holding, release := context.WithCancel(t.Context())
	require.NoError(t, h.c.TakeOver(holding, app.Term{Epoch: 2, At: h.clock.Now(), Previous: h.clock.Now()}))
	letGo := h.records.hang()
	t.Cleanup(letGo)
	opened := make(chan error, 1)
	go func() {
		_, err := h.c.Open(context.WithoutCancel(t.Context()), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
		opened <- err
	}()
	require.Eventually(t, func() bool { return h.records.waiting.Load() == 1 }, 5*time.Second, time.Millisecond)

	release()

	select {
	case err := <-opened:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("the opening waited for the store once the holding ended")
	}
	h.c.Fence()
}

// A round whose board entry never landed before its holder died, and whose
// deadline then passed, gets its entry before it ends, so that the board
// takes the next round's entry after it.
func TestARoundEndedByAnOutageGetsItsBoardEntryFirst(t *testing.T) {
	h := newHarness(t)
	h.store.set(func(n int) error {
		if n == 2 {
			return errors.New("the instance died")
		}
		return nil
	})
	id := h.open("checkout fails")
	h.crash()
	h.store.set(nil)
	h.clock.Advance(3 * time.Minute)

	require.NoError(t, h.boot())

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	assert.Equal(t, item.EndedByOutage, s.Run.Rounds[0].Ended.EndedBy)
	assert.Len(t, roundEntries(h.events(s.Run.ID)), 2, "both rounds' entries, in order")
	assert.Len(t, h.turns.started, 2, "round 2's turns started")
}

// A round whose deadline passes while this instance holds the lease but
// cannot act on it, as when the board's store is down, ends by its deadline
// once the store is back: its turns never started, and it counts toward the
// budget, so that a long outage of the board ends the run by its budget,
// not by rounds that cost nothing, for ever.
func TestARoundThisHoldingCouldNotActOnEndsByItsDeadline(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	h.crash()
	h.clock.Advance(time.Second)
	h.store.set(func(int) error { return errors.New("the board's store is down") })
	require.NoError(t, h.boot())
	h.clock.Advance(3 * time.Minute)
	h.store.set(nil)

	h.clock.Advance(h.opts.Retry)

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	ended := s.Run.Rounds[0].Ended
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnUnstarted, runtime.Name: item.TurnUnstarted}, ended.Turns)
	assert.Equal(t, 1, ended.Rounds)
}

// A round ended by an outage whose end could not be recorded at first is
// ended on the next try.
func TestAnOutageRoundWhoseEndFailsIsEndedOnTheRetry(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	h.crash()
	h.clock.Advance(3 * time.Minute)
	h.records.set(func(n int) error {
		if n == 5 {
			return errors.New("the database blinked")
		}
		return nil
	})
	require.NoError(t, h.boot())
	require.Len(t, h.state(id).Run.Rounds, 1)

	h.clock.Advance(h.opts.Retry)

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	assert.Equal(t, item.EndedByOutage, s.Run.Rounds[0].Ended.EndedBy)
	assert.Len(t, h.turns.started, 2)
}

// A takeover that finds the lease gone, at any item, acts on no item after
// it: it stops, fenced, and says so. A step that wrote nothing, as a
// round's entry the board holds already, would otherwise start turns for
// an instance that holds no lease.
func TestATakeoverActsOnNothingOnceItFindsTheLeaseGone(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")
	h.open("payments fail")
	h.crash()
	found := false
	h.store.set(func(int) error {
		if !found {
			found = true
			return fmt.Errorf("append: %w", app.ErrLeaseLost)
		}
		return nil
	})

	err := h.boot()

	require.ErrorIs(t, err, app.ErrLeaseLost)
	assert.Empty(t, h.turns.started, "no turn starts once the lease is found gone")
	assert.Zero(t, h.clock.pending())
}

// A turn's end reported late, by a runner whose turn was stopped before
// the instance took the lease again and started a new turn for the same
// agent, changes nothing: the new turn's end is the one that counts.
func TestAnExitOfAnEarlierTurnChangesNothing(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	earlier := h.turns.take()
	h.c.Fence()
	require.NoError(t, h.c.TakeOver(t.Context(), app.Term{Epoch: 2, At: h.clock.Now(), Previous: h.clock.Now()}))
	fresh := h.turns.take()
	require.Len(t, fresh, 2)

	for _, turn := range earlier {
		h.c.Exited(t.Context(), app.Exit{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID, Class: item.TurnCrashed})
	}
	h.turns.queued = fresh
	h.round()

	ended := h.state(id).Run.Rounds[0].Ended
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, ended.Turns)
}

// Timers that run on goroutines of their own, as Plan 7d's will, while
// turns end and deadlines pass at once: each round ends once, by its turns
// or by its deadline, whichever comes first, the run stops by its budget,
// and a fenced coordinator acts no more. Each round has an entry on the
// board, so that none is quiet.
func TestARealClocksTimersEndEachRoundOnce(t *testing.T) {
	h := newHarness(t)
	g := &goTimers{c: h.clock}
	h.timers = g
	h.crash()
	require.NoError(t, h.boot())
	id := h.open("checkout fails")

	for i := 0; i < 10 && h.state(id).Status() != item.StatusAwaitingClose; i++ {
		if s := h.state(id); s.Run.Latest().Ended == nil {
			round := s.Run.Latest().Opened.Round
			_, err := h.boards.PostTask(t.Context(), s.Run.ID, round, release, &board.TaskInput{Title: fmt.Sprintf("check round %d", round)})
			require.NoError(t, err)
		}
		var wg sync.WaitGroup
		for _, turn := range h.turns.take() {
			wg.Go(func() {
				h.c.Exited(t.Context(), app.Exit{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID, Class: item.TurnRefused})
			})
		}
		h.clock.Advance(2 * time.Minute)
		wg.Wait()
		g.wait()
		h.clock.Advance(h.opts.Grace)
		g.wait()
	}

	s := h.state(id)
	require.Equal(t, item.StatusAwaitingClose, s.Status())
	assert.Equal(t, item.StopBudgetExhausted, s.Run.Stopped.Reason)
	assert.Len(t, s.Run.Rounds, 3)
	other := h.open("payments fail")
	var wg sync.WaitGroup
	wg.Go(func() { h.clock.Advance(2 * time.Minute) })
	h.c.Fence()
	fenced := len(h.kinds(other))
	wg.Wait()
	g.wait()
	h.clock.Advance(10 * time.Minute)
	g.wait()
	assert.Len(t, h.kinds(other), fenced, "nothing is recorded once the coordinator is fenced")
}

// A crash at any step of an item's life leaves the item to the next
// instance, which carries it on from the records: the item ends as it
// would have, the board holds each round's entry and the stop entry once,
// and nothing after the stop.
func TestACrashAtAnyStepIsCarriedOnFromTheRecords(t *testing.T) {
	for _, c := range []struct {
		store string
		at    int
	}{
		{"records", 2}, {"records", 3}, {"records", 4}, {"records", 5}, {"records", 6}, {"records", 7},
		{"board", 1}, {"board", 2}, {"board", 3}, {"board", 5},
	} {
		name := fmt.Sprintf("%s append %d", c.store, c.at)
		h := newHarness(t)
		crashed := false
		fail := func(n int) error {
			if n == c.at && !crashed {
				crashed = true
				return errors.New("the instance died")
			}
			return nil
		}
		if c.store == "records" {
			h.records.fail = fail
		} else {
			h.store.fail = fail
		}
		id := h.open("checkout fails")
		for range 8 {
			if crashed && h.epoch == 1 {
				h.crash()
				require.NoError(t, h.boot(), name)
			}
			h.round()
		}

		s := h.state(id)
		require.True(t, crashed, name)
		assert.Equal(t, item.StatusAwaitingClose, s.Status(), name)
		assert.Equal(t, item.StopQuiet, s.Closing.Reason, name)
		events := h.events(s.Run.ID)
		assert.Len(t, roundEntries(events), len(s.Run.Rounds), name)
		_, stopped := lastStop(events)
		assert.True(t, stopped, name)
		assert.NotNil(t, events[len(events)-1].Body.Stop, "%s: nothing after the stop entry", name)
		evidence := 0
		for i := range events {
			if events[i].Body.Evidence != nil {
				evidence++
			}
		}
		assert.Equal(t, 1, evidence, "%s: the evidence the item opened with, once", name)
	}
}

// An instance never acts on records it cannot read: it refuses to take
// over, and acts on nothing.
func TestATakeoverRefusesRecordsItCannotRead(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	_, err := h.records.Records.Append(t.Context(), id, func(history []item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: "from-a-later-version", Item: id, Incident: history[0].Incident, Body: item.Body{V: item.Version + 1}}}, nil
	})
	require.NoError(t, err)
	h.crash()

	err = h.boot()

	require.ErrorIs(t, err, item.ErrVersion)
	assert.Empty(t, h.turns.started)
	_, err = h.c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
	require.ErrorIs(t, err, app.ErrNotHeld)
}

// A coordinator that is fenced acts no more: its turns stop, its sessions
// end, its timers are cancelled, and it opens nothing and decides nothing
// until it takes over again.
func TestAFencedCoordinatorActsNoMore(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	played := h.turns.take()
	kinds := h.kinds(id)
	stops := h.turns.all

	h.c.Fence()
	for i := range played {
		h.c.Exited(t.Context(), app.Exit{Run: played[i].Run, Agent: played[i].Agent, Round: 1, Class: item.TurnEnded})
	}
	h.clock.Advance(10 * time.Minute)
	_, err := h.c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})

	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Equal(t, kinds, h.kinds(id))
	assert.Equal(t, stops+1, h.turns.all, "every turn stopped")
	_, err = h.registry.MCP().Session(t.Context(), played[0].MCP.Reveal())
	require.ErrorIs(t, err, actionsapp.ErrNoSession)
	assert.Zero(t, h.clock.pending())
}

// A write that finds the lease gone fences the coordinator at once, and
// tells its leader, which takes the lease again.
func TestAWriteThatFindsTheLeaseGoneFencesTheCoordinator(t *testing.T) {
	h := newHarness(t)
	lost := 0
	var epoch int64
	h.c.OnLost(func(e int64) { lost, epoch = lost+1, e })
	h.records.fail = func(n int) error {
		if n == 5 {
			return fmt.Errorf("record: %w", app.ErrLeaseLost)
		}
		return nil
	}
	h.open("checkout fails")
	played := h.play()
	stops := h.turns.all

	h.clock.Advance(h.opts.Grace)

	assert.Equal(t, 1, lost)
	assert.Equal(t, h.epoch, epoch, "the holding it acted under")
	assert.Equal(t, stops+1, h.turns.all)
	_, err := h.registry.LLM().Session(t.Context(), played[0].LLM.Reveal())
	require.Error(t, err)
	_, err = h.c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Zero(t, h.clock.pending(), "no retry of a fenced coordinator's step")
}

// A board append that finds the lease gone fences the coordinator as a
// record does.
func TestABoardAppendThatFindsTheLeaseGoneFencesTheCoordinator(t *testing.T) {
	h := newHarness(t)
	h.store.fail = func(n int) error {
		if n == 2 {
			return fmt.Errorf("append: %w", app.ErrLeaseLost)
		}
		return nil
	}

	_, err := h.c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})

	require.NoError(t, err, "the item's opening was recorded")
	_, err = h.c.Open(t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Empty(t, h.turns.started)
	assert.Zero(t, h.clock.pending(), "no retry of a fenced coordinator's step")
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -race ./internal/work/...`
Expected: FAIL: the build of `internal/work/app` fails (undefined: app.ErrNotHeld)

- [ ] **Step 3: Takeover and fencing**

Apply this change to `internal/work/app/coordinator.go`:

```diff
diff --git a/internal/work/app/coordinator.go b/internal/work/app/coordinator.go
index 11a507c..7f6b654 100644
--- a/internal/work/app/coordinator.go
+++ b/internal/work/app/coordinator.go
@@ -18,13 +18,18 @@ import (
 	"github.com/jchen0426/agent-swarm/internal/work/board"
 	"github.com/jchen0426/agent-swarm/internal/work/item"
 	"github.com/jchen0426/agent-swarm/internal/work/key"
 )
 
-// ErrInvalid marks a request the coordinator refuses, such as an item of a
-// type it cannot run.
-var ErrInvalid = errors.New("app: invalid request")
+var (
+	// ErrInvalid marks a request the coordinator refuses, such as an item
+	// of a type it cannot run.
+	ErrInvalid = errors.New("app: invalid request")
+	// ErrNotHeld marks a request to a coordinator whose instance does not
+	// hold the lease.
+	ErrNotHeld = errors.New("app: this instance does not hold the lease")
+)
 
 // Ports are what the coordinator acts through.
 type Ports struct {
 	Records   Records
 	Boards    *Boards
@@ -59,18 +64,23 @@ type Options struct {
 
 // Coordinator drives work items through their lifecycle (spec Sections 4.1
 // and 5): it opens and starts items, opens and ends rounds, and stops runs.
 // It writes each decision as a record before it acts on it, and every
 // action can be repeated, so that it resumes from the records after a crash.
-// One lock serializes its decisions.
+// One lock serializes its decisions. It acts only while its instance holds
+// the lease: from TakeOver until it is fenced.
 type Coordinator struct {
 	p    Ports
 	opts Options
 	log  *slog.Logger
-	ctx  context.Context
 
-	mu      sync.Mutex
+	mu   sync.Mutex
+	held bool
+	// term is the holding the coordinator acts under, from TakeOver on.
+	term    Term
+	ctx     context.Context
+	lost    func(epoch int64)
 	runs    map[board.RunID]*running
 	retries map[item.ID]func()
 }
 
 // running is what the coordinator holds in memory of a run's open round:
@@ -91,10 +101,12 @@ type running struct {
 
 // ended is how and when a round ended, until its end is recorded.
 type ended struct {
 	by item.EndedBy
 	at time.Time
+	// unseen says this instance did not act on the round (item.Ending).
+	unseen bool
 }
 
 // maxSteps bounds the steps one advance takes, so that a mistake in the
 // records cannot loop forever.
 const maxSteps = 32
@@ -106,10 +118,11 @@ func NewCoordinator(p *Ports, opts *Options) (*Coordinator, error) {
 	}
 	if opts.DailyCap <= 0 || opts.Grace <= 0 || opts.SettleWait < 0 || opts.SettlePoll <= 0 || opts.Retry <= 0 {
 		return nil, errors.New("app: the coordinator needs a daily cap, a grace, a settling poll and a retry above zero")
 	}
 	c := &Coordinator{p: *p, opts: *opts, log: opts.Log, ctx: context.Background(), runs: map[board.RunID]*running{}, retries: map[item.ID]func(){}}
+	// A coordinator acts once it takes over.
 	if c.log == nil {
 		c.log = slog.New(slog.DiscardHandler)
 	}
 	return c, nil
 }
@@ -146,10 +159,18 @@ const maxTitleRunes = 200
 // Once the item's opening is recorded, Open returns its id: what the item's
 // start does next is tried again until it succeeds.
 func (c *Coordinator) Open(ctx context.Context, in *OpenInput) (item.ID, error) {
 	c.mu.Lock()
 	defer c.mu.Unlock()
+	if !c.held {
+		return "", ErrNotHeld
+	}
+	// The opening ends with its request, or with the holding: a fence waits
+	// for no store an opening waits on.
+	ctx, cancel := context.WithCancel(ctx)
+	defer cancel()
+	defer context.AfterFunc(c.ctx, cancel)()
 	rev := c.p.Revisions.Current()
 	if rev == nil {
 		return "", errors.New("app: the registry has no configuration yet")
 	}
 	if err := runnable(rev, in.Type); err != nil {
@@ -169,25 +190,102 @@ func (c *Coordinator) Open(ctx context.Context, in *OpenInput) (item.ID, error)
 	}}}
 	if day >= c.opts.DailyCap {
 		bodies = append(bodies, item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}})
 	}
 	if err := c.record(ctx, s, "", bodies...); err != nil {
+		c.failed(ctx, s.ID, err)
 		return "", err
 	}
 	// The item's start is the coordinator's from here on, not the request's.
 	c.advance(c.ctx, s.ID)
 	return s.ID, nil
 }
 
+// OnLost sets what the coordinator calls once it fenced itself because a
+// write found the lease gone, with the epoch of the holding it acted under,
+// so that its leader takes the lease again. It is called through a timer,
+// never under the coordinator's lock: it may come once the leader holds the
+// lease again, under another epoch.
+func (c *Coordinator) OnLost(f func(epoch int64)) {
+	c.mu.Lock()
+	defer c.mu.Unlock()
+	c.lost = f
+}
+
+// TakeOver has the coordinator act under term, the lease's new holding
+// (spec Section 8.2), until it is fenced: it stops the turns and ends the
+// sessions of any earlier holding, refuses records it cannot read, and
+// carries on every item that does not await its close, from its records. A
+// round in progress gets fresh sessions and turns for the agents that have
+// not ended theirs; a round whose deadline passed before this instance
+// acted on it ends as missed says; a decision recorded is carried out.
+// Steps that timers run later use ctx, which the holding ends. A takeover
+// whose holding ended before it began, or that finds the lease gone, acts on
+// no item after it, and says so.
+func (c *Coordinator) TakeOver(ctx context.Context, term Term) error {
+	c.mu.Lock()
+	defer c.mu.Unlock()
+	c.fence()
+	ids, err := c.p.Records.Active(ctx)
+	if err != nil {
+		return fmt.Errorf("take over: %w", err)
+	}
+	for _, id := range ids {
+		if _, err := c.load(ctx, id); errors.Is(err, item.ErrVersion) {
+			return fmt.Errorf("take over: %w", err)
+		}
+	}
+	if err := ctx.Err(); err != nil {
+		return fmt.Errorf("take over: %w", err)
+	}
+	c.held, c.term, c.ctx = true, term, ctx
+	for _, id := range ids {
+		if !c.held || ctx.Err() != nil {
+			break
+		}
+		c.advance(ctx, id)
+	}
+	if !c.held {
+		return fmt.Errorf("take over: %w", ErrLeaseLost)
+	}
+	return ctx.Err()
+}
+
+// Fence stops the coordinator acting, as an instance that lost the lease
+// must: its turns stop, its sessions end and its timers are cancelled. It
+// acts again only once it takes over.
+func (c *Coordinator) Fence() {
+	c.mu.Lock()
+	defer c.mu.Unlock()
+	c.fence()
+}
+
+func (c *Coordinator) fence() {
+	c.held = false
+	c.p.Turns.StopAll()
+	c.p.Sessions.EndAll()
+	for _, r := range c.runs {
+		c.cancel(r)
+	}
+	for _, cancel := range c.retries {
+		cancel()
+	}
+	clear(c.runs)
+	clear(c.retries)
+}
+
 // Exited reports how an agent's turn ended. An end reported twice, late, for
 // a round that is over, of another turn, or as a class the runner does not
 // report changes nothing. A round ends once every turn ended, unless each
 // ended at the model vendor: such a round ends at its deadline, so that a
 // vendor's outage cannot spend the run's rounds in seconds.
 func (c *Coordinator) Exited(ctx context.Context, e Exit) {
 	c.mu.Lock()
 	defer c.mu.Unlock()
+	if !c.held {
+		return
+	}
 	r := c.runs[e.Run]
 	if r == nil || r.round != e.Round || r.ended != nil {
 		return
 	}
 	if id, ok := r.turns[e.Agent]; !ok || id != e.Turn {
@@ -228,11 +326,11 @@ func (c *Coordinator) whenAllEnded(run board.RunID, r *running) {
 // as unstarted.
 func (c *Coordinator) deadline(run board.RunID, round int, at time.Time) {
 	c.mu.Lock()
 	defer c.mu.Unlock()
 	r := c.runs[run]
-	if r == nil || r.round != round || r.ended != nil {
+	if !c.held || r == nil || r.round != round || r.ended != nil {
 		return
 	}
 	for agent := range r.turns {
 		c.p.Turns.Stop(run, agent)
 		r.exits[agent] = item.TurnInterrupted
@@ -258,40 +356,41 @@ func (c *Coordinator) end(run board.RunID, r *running, by item.EndedBy, at time.
 // wait, then decides the round's end.
 func (c *Coordinator) settle(run board.RunID, round int) {
 	c.mu.Lock()
 	defer c.mu.Unlock()
 	r := c.runs[run]
-	if r == nil || r.round != round || r.ended == nil {
+	if !c.held || r == nil || r.round != round || r.ended == nil {
 		return
 	}
 	again := func(after time.Duration) { c.arm(r, c.p.Clock.Now().Add(after), func() { c.settle(run, round) }) }
 	n, err := c.p.Spend.InFlight(c.ctx, string(run))
-	if err != nil {
-		c.failed(c.ctx, r.item, err)
-		again(c.opts.Retry)
-		return
-	}
-	if n > 0 && c.p.Clock.Now().Before(r.ended.at.Add(c.opts.Grace+c.opts.SettleWait)) {
-		again(c.opts.SettlePoll)
-		return
+	if err == nil {
+		if n > 0 && c.p.Clock.Now().Before(r.ended.at.Add(c.opts.Grace+c.opts.SettleWait)) {
+			again(c.opts.SettlePoll)
+			return
+		}
+		if err = c.endRound(c.ctx, run, r, n == 0); err == nil {
+			c.advance(c.ctx, r.item)
+			return
+		}
 	}
-	if err := c.decide(c.ctx, run, r, n == 0); err != nil {
-		c.failed(c.ctx, r.item, err)
+	c.failed(c.ctx, r.item, err)
+	if c.held {
 		again(c.opts.Retry)
 	}
 }
 
-// decide records how a round ended and what follows it, then acts on it.
-// Before it decides, it notices on the board each model call of the run
-// that was refused, failed or whose outcome is unknown, once per call.
-func (c *Coordinator) decide(ctx context.Context, run board.RunID, r *running, settled bool) error {
+// endRound records how a round ended and what follows it. Before it
+// decides, it notices on the board each model call of the run that was
+// refused, failed or whose outcome is unknown, once per call. A round whose
+// end is recorded already is left as it is.
+func (c *Coordinator) endRound(ctx context.Context, run board.RunID, r *running, settled bool) error {
 	s, err := c.load(ctx, r.item)
 	if err != nil {
 		return err
 	}
 	if l := s.Run.Latest(); l == nil || l.Ended != nil || l.Opened.Round != r.round {
-		c.advance(ctx, s.ID)
 		return nil
 	}
 	spent, err := c.p.Spend.Spent(ctx, string(run))
 	if err != nil {
 		return fmt.Errorf("read the run's spend: %w", err)
@@ -317,17 +416,16 @@ func (c *Coordinator) decide(ctx context.Context, run board.RunID, r *running, s
 			return err
 		}
 	}
 	re := s.Run.End(&item.Ending{
 		EndedBy: r.ended.by, EndedAt: r.ended.at, Turns: maps.Clone(r.exits), Spent: spent, Settled: settled,
-		DaySpent: day, DailyCap: c.opts.DailyCap, Report: report,
+		DaySpent: day, DailyCap: c.opts.DailyCap, Report: report, Unseen: r.ended.unseen,
 	})
 	if err := c.record(ctx, s, run, item.Body{RoundEnded: &re}); err != nil {
 		return err
 	}
 	c.cancel(r)
-	c.advance(ctx, s.ID)
 	return nil
 }
 
 // noticeCode is the notice of a model call's outcome.
 func noticeCode(status string) board.NoticeCode {
@@ -356,23 +454,36 @@ func (c *Coordinator) advance(ctx context.Context, id item.ID) {
 				continue
 			}
 		}
 		if err != nil {
 			c.failed(ctx, id, err)
-			c.retries[id] = c.p.Timers.At(c.p.Clock.Now().Add(c.opts.Retry), func() {
-				c.mu.Lock()
-				defer c.mu.Unlock()
-				c.advance(c.ctx, id)
-			})
+			if c.held {
+				c.retries[id] = c.p.Timers.At(c.p.Clock.Now().Add(c.opts.Retry), func() {
+					c.mu.Lock()
+					defer c.mu.Unlock()
+					if c.held {
+						c.advance(c.ctx, id)
+					}
+				})
+			}
 		}
 		return
 	}
 	c.failed(ctx, id, errors.New("app: the item took more steps at once than an item can"))
 }
 
+// failed logs a step that failed. A write that found the lease gone fences
+// the coordinator at once, and tells its leader.
 func (c *Coordinator) failed(ctx context.Context, id item.ID, err error) {
-	c.log.ErrorContext(ctx, "a step of a work item failed; it is tried again", "item", id, "err", err)
+	c.log.ErrorContext(ctx, "a step of a work item failed", "item", id, "err", err)
+	if errors.Is(err, ErrLeaseLost) && c.held {
+		c.fence()
+		if c.lost != nil {
+			lost, epoch := c.lost, c.term.Epoch
+			c.p.Timers.At(c.p.Clock.Now(), func() { lost(epoch) })
+		}
+	}
 }
 
 // step takes an item's next step, and reports whether it must wait: for a
 // round's end, or for the item's close.
 func (c *Coordinator) step(ctx context.Context, s *item.State) (wait bool, err error) {
@@ -392,10 +503,13 @@ func (c *Coordinator) step(ctx context.Context, s *item.State) (wait bool, err e
 	case l == nil && len(s.Run.Created.Agents) == 0:
 		return false, c.unstaffed(ctx, s)
 	case l == nil || l.Ended != nil && l.Ended.Decision.Next != item.NextStop:
 		return false, c.open(ctx, s)
 	case l.Ended == nil:
+		if r := c.runs[s.Run.ID]; (r == nil || r.round != l.Opened.Round) && !c.p.Clock.Now().Before(l.Opened.Deadline) {
+			return false, c.missed(ctx, s)
+		}
 		return true, c.hold(ctx, s)
 	}
 	a, err := c.p.Boards.Assess(ctx, s.Run.ID, time.Time{})
 	if err != nil {
 		return false, err
@@ -542,10 +656,69 @@ func (c *Coordinator) startAgain(run board.RunID, round int, agent string) {
 		return
 	}
 	c.begin(c.ctx, run, r, r.starting[agent])
 }
 
+// missed ends a round whose deadline passed before this instance acted on
+// it, once its board entry is posted, as its holder may have died before it
+// was. Such a round is unseen: never quiet, and, as a wrap-up round, it
+// concludes nothing (item.Ending). A round whose deadline passed between the last holding's expiry and
+// this holding's start, while no instance held the lease, ends by an
+// outage: none of its turns counts, and it counts toward no budget. Any
+// other ends by its deadline: the turns that ended on the board ended; the
+// others were interrupted by the last holding's deadline, or never started,
+// when this holding could not act on the round before its deadline.
+func (c *Coordinator) missed(ctx context.Context, s *item.State) error {
+	run, o := s.Run.ID, &s.Run.Latest().Opened
+	if _, err := c.p.Boards.StartRound(ctx, run, &board.RoundStart{Round: o.Round, WrapUp: o.WrapUp}); err != nil {
+		return err
+	}
+	n, err := c.p.Spend.InFlight(ctx, string(run))
+	if err != nil {
+		return fmt.Errorf("read the run's model calls in flight: %w", err)
+	}
+	r := &running{
+		item: s.ID, round: o.Round, deadline: o.Deadline, tokens: map[string]Tokens{}, turns: map[string]string{}, starting: map[string]*Turn{},
+		exits: map[string]item.TurnClass{}, ended: &ended{by: item.EndedByDeadline, at: o.Deadline, unseen: true},
+	}
+	if o.Deadline.After(c.term.Previous) && !o.Deadline.After(c.term.At) {
+		r.ended.by = item.EndedByOutage
+	} else {
+		report, err := c.p.Boards.Report(ctx, run, o.Round)
+		if err != nil {
+			return err
+		}
+		rest := item.TurnInterrupted
+		if o.Deadline.After(c.term.At) {
+			rest = item.TurnUnstarted
+		}
+		for _, agent := range o.Agents {
+			r.exits[agent] = rest
+			if slices.Contains(report.Facts.Ended, agent) {
+				r.exits[agent] = item.TurnEnded
+			}
+		}
+	}
+	old := c.runs[run]
+	if old != nil {
+		c.cancel(old)
+		r.tokens = old.tokens
+	}
+	c.runs[run] = r
+	if err := c.endRound(ctx, run, r, n == 0); err != nil {
+		// The round is left as this instance found it, so that the retry
+		// ends it again.
+		if old != nil {
+			c.runs[run] = old
+		} else {
+			delete(c.runs, run)
+		}
+		return err
+	}
+	return nil
+}
+
 // close acts on a run that stopped: its sessions and turns end, then the
 // stop entry, with the conclusion its record holds, ends its board, which
 // may take tries, and the item awaits its close, with the run's reason as
 // the item's.
 func (c *Coordinator) close(ctx context.Context, s *item.State) error {
```

Apply this change to `internal/work/app/ports.go`:

```diff
diff --git a/internal/work/app/ports.go b/internal/work/app/ports.go
index eb28284..b03d69f 100644
--- a/internal/work/app/ports.go
+++ b/internal/work/app/ports.go
@@ -127,12 +127,13 @@ type Turn struct {
 // Turns runs agents' turns: the runner, in sandboxes (Plan 7d). A turn's
 // end is reported once, with its ID, through Coordinator.Exited. The
 // coordinator calls Turns under its lock, which Exited takes: no method calls
 // Exited before it returns, or waits for a turn to end.
 type Turns interface {
-	// Start starts a turn. It fails only when the runner cannot be reached:
-	// a turn whose sandbox fails to start ends crashed.
+	// Start starts a turn, and returns once the runner has it, without
+	// waiting for its sandbox. It fails only when the runner cannot be
+	// reached: a turn whose sandbox fails to start ends crashed.
 	Start(ctx context.Context, t *Turn) error
 	// Stop ends a turn at once.
 	Stop(run board.RunID, agent string)
 	// StopAll ends every turn the instance runs.
 	StopAll()
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test -race ./internal/work/... && golangci-lint run ./...`
Expected: every package `ok`; `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal/work/app
git commit -m "feat(work): a takeover that carries items on from their records, and a coordinator fenced once its lease is lost"
```

### Task 9: The leader

Decision 3. The leader takes and holds the lease, has its coordinator take over under each holding, and fences the coordinator when it can no longer be sure of the lease. Its tests run it over a lease in memory, whose takes and renewals can wait, fail or take time.

**Files:**
- Create: `internal/work/app/leader.go`, `internal/work/app/leader_test.go`
- Modify: `internal/work/app/harness_test.go`

**Interfaces:**
- Consumes: the coordinator's `TakeOver`, `Fence` and `OnLost` (Task 8), `app.Lease` (Task 6).
- Produces: `app.LeaderOptions{RenewEvery, Margin, TakeEvery time.Duration; Log *slog.Logger}`, `app.NewLeader(Lease, *Coordinator, Clock, Timers, *LeaderOptions) (*Leader, error)`, `(*Leader).Lead(ctx) error`, `(*Leader).Stop(ctx)`.

- [ ] **Step 1: Write the failing tests**

Apply this change to `internal/work/app/harness_test.go`, whose clock no longer goes back once a timer's function moved it on:

```diff
diff --git a/internal/work/app/harness_test.go b/internal/work/app/harness_test.go
index 70a659c..33caf1a 100644
--- a/internal/work/app/harness_test.go
+++ b/internal/work/app/harness_test.go
@@ -221,11 +221,14 @@ func (c *clock) Advance(d time.Duration) {
 				due = tm
 				break
 			}
 		}
 		if due == nil {
-			c.now = end
+			// A timer's function may have moved the clock on past end.
+			if c.now.Before(end) {
+				c.now = end
+			}
 			c.mu.Unlock()
 			return
 		}
 		if due.at.After(c.now) {
 			c.now = due.at
```

Create `internal/work/app/leader_test.go`:

```go
package app_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// lease is the Lease port in memory: another instance holds it while held
// is set; renew, when set, is what a renewal returns. A renewal takes slow
// on the clock; hook, when set, runs while a renewal is in flight, and says
// what it returns. A take waits until taking, when set, is closed, once it
// said so on waiting.
type lease struct {
	mu       sync.Mutex
	clock    *clock
	held     bool
	mine     bool
	epoch    int64
	renew    error
	slow     time.Duration
	hook     func() error
	taking   chan struct{}
	waiting  chan struct{}
	takes    int
	renewals int
	releases int
}

func (l *lease) Take(context.Context) (app.Term, bool, error) {
	l.mu.Lock()
	if l.taking != nil {
		taking := l.taking
		l.mu.Unlock()
		l.waiting <- struct{}{}
		<-taking
		l.mu.Lock()
	}
	defer l.mu.Unlock()
	l.takes++
	if l.held {
		return app.Term{}, false, nil
	}
	l.held, l.mine = true, true
	l.epoch++
	return app.Term{Epoch: l.epoch, At: l.clock.Now()}, true, nil
}

func (l *lease) Renew(context.Context) error {
	l.mu.Lock()
	slow, hook := l.slow, l.hook
	l.mu.Unlock()
	if slow > 0 {
		l.clock.Advance(slow)
	}
	if hook != nil {
		return hook()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.renewals++
	if l.renew != nil {
		if errors.Is(l.renew, app.ErrLeaseLost) {
			l.mine = false
		}
		return l.renew
	}
	return nil
}

func (l *lease) Release(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releases++
	if l.mine {
		l.held, l.mine = false, false
	}
	return nil
}

func (l *lease) TTL() time.Duration { return leaseTTL }

func (l *lease) set(f func(l *lease)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f(l)
}

// leaderOptions renew every 5 s the lease of 15 s, fence 5 s before it
// would expire, and try to take it every 5 s.
var leaderOptions = app.LeaderOptions{RenewEvery: 5 * time.Second, Margin: 5 * time.Second, TakeEvery: 5 * time.Second}

const leaseTTL = 15 * time.Second

// unled is a harness whose instance has not taken over: a leader takes
// over for it.
func unled(t *testing.T) (*harness, *lease, *app.Leader) {
	t.Helper()
	h := newHarness(t)
	h.crash()
	var err error
	h.c, err = app.NewCoordinator(h.ports(), &h.opts)
	require.NoError(t, err)
	l := &lease{clock: h.clock}
	leader, err := app.NewLeader(l, h.c, h.clock, h.clock, &leaderOptions)
	require.NoError(t, err)
	return h, l, leader
}

func opens(h *harness) error {
	_, err := h.c.Open(h.t.Context(), &app.OpenInput{Type: "incident", Title: "t", Source: "person"})
	return err
}

// A leader takes the lease when it is free, and its coordinator takes over;
// it renews the lease while it holds it.
func TestALeaderTakesTheLeaseAndItsCoordinatorTakesOver(t *testing.T) {
	h, l, leader := unled(t)
	l.set(func(l *lease) { l.held = true })

	leader.Lead(t.Context())
	require.ErrorIs(t, opens(h), app.ErrNotHeld, "another instance holds the lease")
	l.set(func(l *lease) { l.held = false })
	h.clock.Advance(leaderOptions.TakeEvery)
	require.NoError(t, opens(h))
	h.clock.Advance(3 * leaderOptions.RenewEvery)

	assert.Equal(t, 2, l.takes)
	assert.Equal(t, 3, l.renewals)
	require.NoError(t, opens(h), "it still holds the lease")
}

// A renewal that finds the lease lost fences the coordinator at once; the
// leader then tries to take the lease again.
func TestALostRenewalFencesTheCoordinator(t *testing.T) {
	h, l, leader := unled(t)
	leader.Lead(t.Context())
	require.NoError(t, opens(h))
	l.set(func(l *lease) { l.renew, l.held = fmt.Errorf("renew: %w", app.ErrLeaseLost), true })
	stops := h.turns.all

	h.clock.Advance(leaderOptions.RenewEvery)
	fenced := opens(h)
	stopped := h.turns.all
	l.set(func(l *lease) { l.held, l.renew = false, nil })
	h.clock.Advance(leaderOptions.TakeEvery)

	require.ErrorIs(t, fenced, app.ErrNotHeld)
	assert.Equal(t, stops+1, stopped, "its turns stopped")
	require.NoError(t, opens(h), "it took the lease again")
	assert.Equal(t, int64(2), l.epoch)
}

// A leader whose renewals fail fences its coordinator before the lease
// could expire: once its last good renewal is older than the lease's time
// less the margin, whatever the database answers.
func TestALeaderFencesBeforeItsLeaseCouldExpire(t *testing.T) {
	h, l, leader := unled(t)
	leader.Lead(t.Context())
	l.set(func(l *lease) { l.renew = errors.New("the database does not answer") })

	h.clock.Advance(leaseTTL - leaderOptions.Margin - time.Second)
	before := opens(h)
	h.clock.Advance(time.Second)
	after := opens(h)

	require.NoError(t, before)
	require.ErrorIs(t, after, app.ErrNotHeld)
}

// A coordinator whose write found the lease gone fences itself, and its
// leader takes the lease again.
func TestALeaderTakesTheLeaseAgainAfterAWriteFoundItGone(t *testing.T) {
	h, l, leader := unled(t)
	leader.Lead(t.Context())
	h.records.set(func(n int) error {
		if n == 2 {
			return fmt.Errorf("record: %w", app.ErrLeaseLost)
		}
		return nil
	})
	l.set(func(l *lease) { l.mine = false })
	require.NoError(t, opens(h))
	require.ErrorIs(t, opens(h), app.ErrNotHeld)

	l.set(func(l *lease) { l.held = false })
	h.clock.Advance(leaderOptions.TakeEvery)

	require.NoError(t, opens(h))
}

// A leader never leads over records it cannot read: it releases the lease
// and takes it no more.
func TestALeaderStopsOverRecordsItCannotRead(t *testing.T) {
	h, l, leader := unled(t)
	_, err := h.records.Records.Append(t.Context(), "item-x", func([]item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: "later", Item: "item-x", Incident: "inc-x", Body: item.Body{V: item.Version + 1}}}, nil
	})
	require.NoError(t, err)

	err = leader.Lead(t.Context())
	h.clock.Advance(time.Minute)

	require.ErrorIs(t, err, item.ErrVersion)
	assert.Equal(t, 1, l.takes)
	assert.Equal(t, 1, l.releases)
}

// A leader that stops releases the lease, and its coordinator acts no
// more.
func TestALeaderThatStopsReleasesTheLease(t *testing.T) {
	h, l, leader := unled(t)
	leader.Lead(t.Context())

	leader.Stop(t.Context())
	h.clock.Advance(time.Minute)

	assert.Equal(t, 1, l.releases)
	assert.Equal(t, 1, l.takes)
	require.ErrorIs(t, opens(h), app.ErrNotHeld)
}

// led is a harness with an item whose round is open, left by an instance
// that crashed, and a leader of a new coordinator over the lease.
func led(t *testing.T) (*harness, *lease, *app.Leader) {
	t.Helper()
	h := newHarness(t)
	h.open("checkout fails")
	h.crash()
	var err error
	h.c, err = app.NewCoordinator(h.ports(), &h.opts)
	require.NoError(t, err)
	l := &lease{clock: h.clock}
	leader, err := app.NewLeader(l, h.c, h.clock, h.clock, &leaderOptions)
	require.NoError(t, err)
	return h, l, leader
}

// blockStarts makes the runner's starts wait until the returned function
// lets them go; started says when the first waits.
func blockStarts(h *harness) (started chan struct{}, letGo func()) {
	started, gate := make(chan struct{}, 4), make(chan struct{})
	h.turns.fail = func(*app.Turn) error {
		started <- struct{}{}
		<-gate
		return nil
	}
	return started, func() { close(gate) }
}

// A leader renews its holding while its coordinator takes over, which may
// take long: its renewals are armed before the takeover starts.
func TestALeaderRenewsWhileItsCoordinatorTakesOver(t *testing.T) {
	h, l, leader := led(t)
	started, letGo := blockStarts(h)
	led := make(chan error, 1)
	go func() { led <- leader.Lead(t.Context()) }()
	<-started

	h.clock.Advance(3 * leaderOptions.RenewEvery)
	var renewals int
	l.set(func(l *lease) { renewals = l.renewals })
	letGo()

	require.NoError(t, <-led)
	assert.Equal(t, 3, renewals, "renewed while the coordinator took over")
	require.NoError(t, opens(h))
}

// A leader stopped while it takes the lease, as a deploy stops an instance
// at any moment, acts no more: its coordinator never takes over, and the
// lease it took is released.
func TestALeaderStoppedWhileItTakesTheLeaseActsNoMore(t *testing.T) {
	h, l, leader := unled(t)
	l.set(func(l *lease) { l.taking, l.waiting = make(chan struct{}), make(chan struct{}, 1) })
	led := make(chan error, 1)
	go func() { led <- leader.Lead(t.Context()) }()
	<-l.waiting

	leader.Stop(t.Context())
	l.set(func(l *lease) { close(l.taking) })

	require.NoError(t, <-led)
	h.clock.Advance(time.Minute)
	require.ErrorIs(t, opens(h), app.ErrNotHeld)
	l.set(func(l *lease) {
		assert.Equal(t, 1, l.takes)
		assert.Equal(t, 1, l.releases, "the lease it took is released")
		assert.Zero(t, l.renewals)
	})
}

// A leader stopped while its coordinator takes over acts no more: its
// coordinator is fenced once the takeover ends, and the lease is released
// and renewed no more.
func TestALeaderStoppedWhileItsCoordinatorTakesOverActsNoMore(t *testing.T) {
	h, l, leader := led(t)
	started, letGo := blockStarts(h)
	led := make(chan error, 1)
	go func() { led <- leader.Lead(t.Context()) }()
	<-started

	stopped := make(chan struct{})
	go func() {
		leader.Stop(t.Context())
		close(stopped)
	}()
	time.Sleep(50 * time.Millisecond)
	letGo()
	<-stopped
	require.NoError(t, <-led)
	h.clock.Advance(time.Minute)

	require.ErrorIs(t, opens(h), app.ErrNotHeld)
	l.set(func(l *lease) {
		assert.Equal(t, 1, l.releases)
		assert.Zero(t, l.renewals, "renewed no more")
	})
}

// Renewals are armed RenewEvery apart from when each was sent: renewals
// that each take a while, under RenewEvery, never let the holding look too
// old to be sure.
func TestSlowRenewalsKeepTheLease(t *testing.T) {
	h, l, leader := unled(t)
	l.set(func(l *lease) { l.slow = 3 * time.Second })
	require.NoError(t, leader.Lead(t.Context()))

	h.clock.Advance(time.Minute)

	require.NoError(t, opens(h), "it holds the lease")
}

// A renewal of an earlier holding that finds the lease lost once the leader
// holds it again, under a new epoch, changes nothing: here the holding grew
// too old while the renewal was in flight, and the leader took the lease
// again.
func TestALateRenewalOfAnEarlierHoldingChangesNothing(t *testing.T) {
	h, l, leader := unled(t)
	require.NoError(t, leader.Lead(t.Context()))
	l.set(func(l *lease) {
		l.hook = func() error {
			l.set(func(l *lease) { l.hook, l.held = nil, false })
			h.clock.Advance(leaderOptions.TakeEvery + leaseTTL)
			return fmt.Errorf("renew: %w", app.ErrLeaseLost)
		}
	})

	h.clock.Advance(leaderOptions.RenewEvery)

	require.NoError(t, opens(h), "the later holding acts")
	l.set(func(l *lease) { assert.Equal(t, int64(2), l.epoch) })
}

// lagging is the clock's timers, but that one armed for a time already come
// waits until flush runs it, as a busy process delays a timer's goroutine.
type lagging struct {
	c    *clock
	mu   sync.Mutex
	late []func()
}

func (g *lagging) At(t time.Time, fn func()) func() {
	if t.After(g.c.Now()) {
		return g.c.At(t, fn)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.late = append(g.late, fn)
	return func() {}
}

func (g *lagging) flush() {
	g.mu.Lock()
	late := g.late
	g.late = nil
	g.mu.Unlock()
	for _, fn := range late {
		fn()
	}
}

// A coordinator's word that a write found the lease gone, which reaches the
// leader once it learned of the loss otherwise and took the lease again,
// changes nothing.
func TestALateWordOfAnEarlierHoldingChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.crash()
	lag := &lagging{c: h.clock}
	h.timers = lag
	var err error
	h.c, err = app.NewCoordinator(h.ports(), &h.opts)
	require.NoError(t, err)
	l := &lease{clock: h.clock}
	leader, err := app.NewLeader(l, h.c, h.clock, lag, &leaderOptions)
	require.NoError(t, err)
	require.NoError(t, leader.Lead(t.Context()))
	gone := true
	h.records.set(func(int) error {
		if gone {
			gone = false
			return fmt.Errorf("record: %w", app.ErrLeaseLost)
		}
		return nil
	})
	require.Error(t, opens(h), "the write found the lease gone")
	l.set(func(l *lease) { l.renew, l.mine = fmt.Errorf("renew: %w", app.ErrLeaseLost), false })
	h.clock.Advance(leaderOptions.RenewEvery)
	l.set(func(l *lease) { l.held, l.renew = false, nil })
	h.clock.Advance(leaderOptions.TakeEvery)
	require.NoError(t, opens(h), "the leader took the lease again")

	lag.flush()

	require.NoError(t, opens(h), "the late word changes nothing")
}

// A leader whose context ends while its coordinator takes over releases the
// lease all the same, under a context of its own: as a database would, the
// lease refuses a release under the context that ended, and would stay held
// until it expired.
func TestALeaderWhoseContextEndsWhileItTakesOverReleasesTheLease(t *testing.T) {
	h, l, leader := led(t)
	started, letGo := blockStarts(h)
	ctx, cancel := context.WithCancel(t.Context())
	led := make(chan error, 1)
	go func() { led <- leader.Lead(ctx) }()
	<-started

	cancel()
	letGo()

	require.NoError(t, <-led)
	require.Eventually(t, func() bool {
		released := 0
		l.set(func(l *lease) { released = l.releases })
		return released == 1
	}, 5*time.Second, time.Millisecond)
	require.ErrorIs(t, opens(h), app.ErrNotHeld)
}

// A leader whose context ends stops, as Stop does: its coordinator is
// fenced and the lease released.
func TestALeaderStopsWhenItsContextEnds(t *testing.T) {
	h, l, leader := unled(t)
	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, leader.Lead(ctx))

	cancel()

	require.Eventually(t, func() bool {
		released := 0
		l.set(func(l *lease) { released = l.releases })
		return released == 1
	}, 5*time.Second, time.Millisecond)
	require.ErrorIs(t, opens(h), app.ErrNotHeld)
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -race ./internal/work/...`
Expected: FAIL: the build of `internal/work/app` fails (undefined: app.LeaderOptions)

- [ ] **Step 3: The leader**

Create `internal/work/app/leader.go`:

```go
package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// LeaderOptions are how a leader holds the lease (spec Section 15).
type LeaderOptions struct {
	// RenewEvery is how often the leader renews its holding [5 s], from
	// when the last renewal was sent; a renewal that has not answered by
	// then failed.
	RenewEvery time.Duration
	// Margin is how long before its holding could expire the leader fences
	// its coordinator [5 s]: once the last renewal that succeeded was sent
	// the lease's TTL less Margin ago.
	Margin time.Duration
	// TakeEvery is how often the leader tries to take a lease another
	// instance holds [5 s].
	TakeEvery time.Duration
	// Log receives what failed; nil logs nothing.
	Log *slog.Logger
}

// Leader takes the platform's lease and holds it, and has its coordinator
// act only while it does (spec Section 8.2): the coordinator takes over
// each time the leader takes the lease, and is fenced once a renewal finds
// the lease lost, once the last renewal that succeeded is too old for the
// holding to be sure, or once a write finds the lease gone. A fenced
// leader tries to take the lease again.
type Leader struct {
	lease  Lease
	coord  *Coordinator
	clock  Clock
	timers Timers
	opts   LeaderOptions
	ttl    time.Duration
	log    *slog.Logger

	mu      sync.Mutex
	ctx     context.Context
	held    bool
	stopped bool
	// holding counts the leader's holdings, so that a renewal or a watch
	// of an earlier holding changes nothing; term is the latest's.
	holding int
	term    Term
	// good is when the last renewal that succeeded was sent, or the lease
	// taken.
	good    time.Time
	release context.CancelFunc
	// renewing, watching and taking cancel the timer of each kind.
	renewing, watching, taking func()
}

// NewLeader returns a leader of coord over lease.
func NewLeader(lease Lease, coord *Coordinator, clock Clock, timers Timers, opts *LeaderOptions) (*Leader, error) {
	ttl := lease.TTL()
	if opts.RenewEvery <= 0 || opts.Margin <= 0 || opts.TakeEvery <= 0 || ttl-opts.Margin <= opts.RenewEvery {
		return nil, errors.New("app: a leader renews its lease more often than the lease's time less the margin")
	}
	l := &Leader{lease: lease, coord: coord, clock: clock, timers: timers, opts: *opts, ttl: ttl, log: opts.Log, ctx: context.Background()}
	if l.log == nil {
		l.log = slog.New(slog.DiscardHandler)
	}
	coord.OnLost(l.lost)
	return l, nil
}

// Lead starts leading: it tries to take the lease now, and again every
// TakeEvery while another instance holds it. It stops leading, as Stop
// does, once ctx ends. It returns an error only when its coordinator
// refused the records it would act on, which it does not read: it released
// the lease and leads no more.
func (l *Leader) Lead(ctx context.Context) error {
	l.mu.Lock()
	l.ctx = ctx
	l.mu.Unlock()
	context.AfterFunc(ctx, func() { l.Stop(ctx) })
	return l.take()
}

// Stop stops leading: the coordinator is fenced and the lease released.
func (l *Leader) Stop(ctx context.Context) {
	l.mu.Lock()
	held := l.held
	l.stopped = true
	l.lose()
	l.mu.Unlock()
	if held {
		l.giveUp(ctx)
	}
}

// take takes the lease, and has the coordinator take over under the new
// holding, which is renewed and watched from before the takeover starts:
// a takeover may take long. A leader stopped meanwhile gives the lease up.
func (l *Leader) take() error {
	l.mu.Lock()
	if l.stopped || l.held {
		l.mu.Unlock()
		return nil
	}
	ctx := l.ctx
	l.mu.Unlock()
	sent := l.clock.Now()
	term, took, err := l.lease.Take(ctx)
	if err != nil || !took {
		if err != nil {
			l.log.ErrorContext(ctx, "the lease could not be taken", "err", err)
		}
		l.mu.Lock()
		l.retake()
		l.mu.Unlock()
		return nil
	}
	holding, release := context.WithCancel(ctx)
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		release()
		l.giveUp(ctx)
		return nil
	}
	l.holding++
	n := l.holding
	l.held, l.term, l.good, l.release = true, term, sent, release
	l.renewAt(n, sent)
	l.watchAt(n)
	l.mu.Unlock()
	err = l.coord.TakeOver(holding, term)
	if err == nil {
		return nil
	}
	version := errors.Is(err, item.ErrVersion)
	l.mu.Lock()
	if version {
		l.stopped = true
	}
	ours := l.held && l.holding == n
	if ours {
		l.lose()
	}
	l.mu.Unlock()
	if !ours && !version {
		// The holding ended while the coordinator took over: Stop gave
		// the lease up, or the holding was lost.
		return nil
	}
	l.giveUp(ctx)
	switch {
	case version:
		l.log.ErrorContext(ctx, "the records hold a version this build cannot read; it leads no more", "err", err)
		return err
	case ctx.Err() == nil:
		l.log.ErrorContext(ctx, "the coordinator could not take over", "err", err)
	}
	return nil
}

// giveUp releases the lease, under a context of its own, which ctx's end
// does not end: the leader's context may have ended, as when its instance
// stops, and a release refused would leave the lease held until it expires.
func (l *Leader) giveUp(ctx context.Context) {
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.opts.RenewEvery)
	defer cancel()
	if err := l.lease.Release(release); err != nil {
		l.log.ErrorContext(ctx, "the lease could not be released; it expires", "err", err)
	}
}

// renew renews holding n. The next renewal is armed RenewEvery after this
// one is sent, so that a slow renewal delays none after it. A renewal that
// finds the lease lost fences the coordinator; one that fails otherwise is
// left to the next, and the watch fences the coordinator if none succeeds
// in time.
func (l *Leader) renew(n int) {
	l.mu.Lock()
	if !l.held || l.holding != n {
		l.mu.Unlock()
		return
	}
	ctx := l.ctx
	sent := l.clock.Now()
	l.renewAt(n, sent)
	l.mu.Unlock()
	renewal, cancel := context.WithTimeout(ctx, l.opts.RenewEvery)
	err := l.lease.Renew(renewal)
	cancel()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held || l.holding != n {
		return
	}
	switch {
	case errors.Is(err, ErrLeaseLost):
		l.log.ErrorContext(ctx, "the lease was lost; the coordinator stops", "err", err)
		l.lose()
	case err != nil:
		l.log.ErrorContext(ctx, "the lease could not be renewed; the next renewal tries again", "err", err)
	case sent.After(l.good):
		l.good = sent
	}
}

// watch fences the coordinator once the last renewal of holding n that
// succeeded was sent TTL less Margin ago, before the holding could expire.
func (l *Leader) watch(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held || l.holding != n {
		return
	}
	if !l.clock.Now().Before(l.good.Add(l.ttl - l.opts.Margin)) {
		l.log.ErrorContext(l.ctx, "the lease could not be renewed in time; the coordinator stops")
		l.lose()
		return
	}
	l.watchAt(n)
}

// lost is what the coordinator calls once a write of its found the lease
// gone, under the holding of epoch: once the leader holds the lease again,
// under another epoch, it changes nothing.
func (l *Leader) lost(epoch int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held && l.term.Epoch == epoch {
		l.lose()
	}
}

// lose fences the coordinator and ends the holding, then, unless the
// leader stopped, tries to take the lease again. It is called with the
// leader's lock held; the coordinator never calls the leader under its own.
func (l *Leader) lose() {
	l.held = false
	for _, cancel := range []func(){l.renewing, l.watching, l.taking} {
		if cancel != nil {
			cancel()
		}
	}
	l.renewing, l.watching, l.taking = nil, nil, nil
	if l.release != nil {
		l.release()
		l.release = nil
	}
	l.coord.Fence()
	l.retake()
}

// retake tries to take the lease again after TakeEvery, unless the leader
// stopped. It is called with the leader's lock held.
func (l *Leader) retake() {
	if l.stopped {
		return
	}
	if l.taking != nil {
		l.taking()
	}
	l.taking = l.timers.At(l.clock.Now().Add(l.opts.TakeEvery), func() { _ = l.take() })
}

// renewAt arms holding n's next renewal RenewEvery after sent, and watchAt
// its watch at the time its last good renewal says. Both are called with
// the leader's lock held.
func (l *Leader) renewAt(n int, sent time.Time) {
	l.renewing = l.timers.At(sent.Add(l.opts.RenewEvery), func() { l.renew(n) })
}

func (l *Leader) watchAt(n int) {
	l.watching = l.timers.At(l.good.Add(l.ttl-l.opts.Margin), func() { l.watch(n) })
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test -race ./internal/work/... && golangci-lint run ./...`
Expected: every package `ok`; `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal/work/app
git commit -m "feat(work): the leader that holds the lease and has its coordinator act only while it does"
```

### Task 10: Documentation

The decisions that amend the spec, ADR-004 and ADR-006 are written into them; the roadmap splits Plan 7 in four; the README and Plan 6b say what this plan did.

**Files:**
- Modify: `plan/2026-10-11-agent-swarm-platform-spec.md`, `plan/adr/ADR-004-durable-execution-postgres.md`, `plan/adr/ADR-006-agent-runtime-sdk-sandbox.md`, `plan/2026-10-12-implementation-roadmap.md`, `README.md`, `plan/2026-10-17-plan-6b-llm-gateway.md`, and this plan

Write DATE as the date the plan runs.

- [ ] **Step 1: Amend the spec**

In `plan/2026-10-11-agent-swarm-platform-spec.md`:

1. In the status row of the header table, after "the LLM gateway (Sections 2.2, 5.6, 8.2, 9.3, 9.4, 15 and 16; Plan 6b).", add " Amended on DATE: the coordinator's core (Sections 2, 4.1, 4.2, 5.2, 5.6, 8.2, 8.4, 9.4, 15 and 16; Plan 7a)."
2. In Section 2, replace "Each agent session gets two short-lived tokens, one for each gateway. Both are scoped to its run and agent, and both expire when the run stops." with "Each agent session gets two short-lived tokens, one for each gateway. Both are scoped to its run and agent and last one round; they expire when the run stops, when the agent leaves the run, or when the instance that issued them loses the lease (Section 8.2). A token is a prefix for its gateway and 43 letters and digits; both gateways redact any token wherever they keep or forward text, and the MCP gateway refuses a call whose arguments hold one, or that comes once its round has ended."
3. In Section 4.1, after the bullet that begins "- **Stop reasons.**", add a sentence at its end: " A run records its own reason too, which may also be `cut`, for a run stopped once the day's spend reached the daily cap; its item's reason is then `not_staffed`. A run with no agent to invite, or none left, stops `not_staffed`, with a notice on its board."
4. In Section 4.2's table, replace the row of `notice` with these two rows:

   ```markdown
   | `notice` | platform | Trigger updates, failures, refused calls, and model calls refused, failed or of unknown outcome, one notice per call |
   | `stop` | platform | The run's stop summary (Section 5.6). After it the board takes rulings only |
   ```

5. In Section 5.2, replace the bullet that begins "- **Sessions.** A session persists across the rounds of its run" with: "- **Sessions.** A session's identity persists across the rounds of its run; its tokens are new each round. Each turn starts from a fresh context: the round's prompt, the agent's skills and the board, read through `read_board` (ADR-006). If a session is lost, the runner starts a new one from the current snapshot. Tool-list changes take effect at the next round boundary."
6. In Section 5.6:
   1. At the end of the first sub-bullet of **Converged**, after "the stop reason is `converged`.", add " During it the run's sessions may spend one round's share of the token budget above the run's spend at its opening. A run has at most [2] wrap-up rounds; a convergence revoked twice gets no third."
   2. Replace "- **Quiet.** A whole round produced no new entries, claims or completions." with "- **Quiet.** A whole round produced no entry of an agent or a person but turn ends, no claim, completion or proposal, none of its turns failed, was refused, failed at the model vendor, or could not start, and the coordinator saw its turns: a round a takeover ends after its time limit is never quiet."
   3. Replace "- **Budget exhausted.** Wall clock, tokens or rounds ran out." with "- **Budget exhausted.** Wall clock, tokens or rounds ran out, or less than a third of a round's time limit of wall clock is left. Rounds and wall clock count the normal rounds that did not end by an outage (Section 8.2); a normal round's time limit is cut to the wall clock left."
   4. After the list of stop conditions, before "When a run stops,", add this paragraph: "**A round's end.** Once every turn ended, or the round's time limit passed, the coordinator waits [5 s] for the round's write calls, then up to [30 s] for the run's model calls in flight; it notices each model call refused, failed or of unknown outcome once on the board, and decides in this order: a wrap-up round whose hypothesis is still converged stops the run `converged`; the daily cap reached stops it `cut`; a resolved trigger; no agent left, `not_staffed`; a converged hypothesis gets its wrap-up round; an exhausted budget; a quiet round; otherwise the next round. It records each decision before it acts on it."
   5. In the paragraph that begins "**Budget.**", replace "A run may pass its budget by the model calls its agents start while it is under it, one per agent at most." with "A run passes its budget, and the day its cap, by one model call at most: the LLM gateway admits one call at a time, up to the commit of its record, so each admission counts every call admitted before it, while each call's estimate covers its outcome." and replace "The runtime keeps each session's context bounded between rounds (ADR-006)." with "Each turn starts from a fresh context (Section 5.2)."
7. In Section 8.2's table:
   1. In the row "An agent session or sandbox crashes", after "After [2] consecutive failures, the agent leaves the run.", add " Only crashes count: a turn the LLM gateway refused, the model vendor failed, or the runner could not start, does not; a turn that could not start is tried again while its round lasts."
   2. In the row "A model call fails or is rate-limited", after "The wall-clock budget keeps running.", add " A round whose every turn failed at the vendor ends at its time limit, never earlier."
   3. Replace the cell after "The coordinator crashes" with: "All state is in the database: every decision is a record written before the coordinator acts on it. On restart the coordinator carries each item on from its records: a round in progress gets fresh sessions and turns; a round whose time limit passed while no instance held the lease ends by an outage, which counts toward no budget and is never quiet; one whose time limit passed while an instance held the lease ends by its time limit, and is never quiet, since the instance ending it did not see its turns."
   4. Replace the cell after "Two coordinator or two gateway instances run at once, for example during a deploy" with: "One lease covers the coordinator and both gateways, which run in one process. Only its holder acts, and every write checks the holding's epoch in its own transaction; a takeover waits for the writes in flight, and fences the rest. The holder renews the lease from when each renewal is sent, and fences itself, stopping its turns and ending its sessions, once a renewal finds the lease lost, or once its last renewal that succeeded is too old for the holding to be sure; what an earlier holding finds changes nothing once the instance holds the lease again. Server limits and degraded marks stay in the holder's memory. The other instance waits."
   5. In the row "Costs run away", replace "a model call starts only while the run is under it, so a run passes it by at most the calls its agents start while under it, one per agent (Section 5.6)." with "the LLM gateway admits one model call at a time, so a run passes its budget, and the day its cap, by one call at most (Section 5.6)."
8. In Section 8.4, replace "and names the runs that lost records" with "and names the runs and incidents that lost records".
9. In Section 9.4, in the bullet that begins "- **Audit integrity.**", replace "the runs whose records the chain gained since then" with "the runs and incidents whose records the chain gained since then (a work record's scope is its incident)" and "a restore names the runs that lost records" with "a restore names the runs and incidents that lost records".
10. In Section 15's table, replace the row of the daily token cap with these rows:

    ```markdown
    | Daily token cap | Set by the deployment, in cost-weighted tokens, counting the calls admitted each UTC day | 7, 8.2 |
    | Round's end: wait for write calls / for model calls in flight, looked at every | [5 s] / [30 s], [1 s] | 5.6 |
    | Wrap-up rounds per run | [2] | 5.6 |
    | Lease: time / renewed every / the holder fences itself before it could expire by / taken again every | [15 s] / [5 s] / [5 s] / [5 s] | 8.2 |
    | Coordinator: a step that failed is tried again after | [5 s] | 8.2 |
    ```

11. In Section 16's table, replace the action of ADR-004 with "Keep. Add one lease for the coordinator and both gateways, which fences a holder that lost it (Section 8.2). Timers come from the records; River waits for jobs that must outlive them (Plan 7a). Its write-path premises are revisited with the Phase 1 design." and, in the action of ADR-006, replace "a session context kept bounded between rounds" with "a fresh context for each turn (Plan 7a)".

- [ ] **Step 2: Amend ADR-004 and ADR-006**

In `plan/adr/ADR-004-durable-execution-postgres.md`:

1. Replace the status row with `| Status | Accepted. The lease is built (Plan 7a); the task library, River, waits for jobs that must outlive the records. |`.
2. Replace the bullet that begins "- **Leases (added 2026-10-12).**" with: "- **The lease (added 2026-10-12, built DATE).** One lease covers the coordinator and both gateways, which run in one process. A holding has an epoch; every write takes the lease's row in key share as it checks that its epoch holds the lease, so a takeover, which changes the epoch, waits for the writes in flight and fences the rest. The holder renews from when each renewal is sent, and fences itself once its last good renewal is too old. Server limits and degraded marks stay in the holder's memory. A second instance waits (spec Section 8.2)."
3. After the bullet that begins "- **Restart.**", add: "- **Timers (added DATE).** Every deadline is in a record; the coordinator arms its timers from the records while it holds the lease, and again when it takes over. River comes with the first jobs that must outlive the records: notifications and retention (Plans 9 and 10)."
4. At the end of its History, add: "- **DATE:** the lease built in Plan 7a, as one lease for the coordinator and both gateways; timers from the records, River deferred to the first jobs that must outlive them."

In `plan/adr/ADR-006-agent-runtime-sdk-sandbox.md`, at the end of its History, add: "- **DATE:** Plan 7a amends spec Section 5.2 to a fresh context per turn: a session's identity persists across rounds, its tokens are new each round, and the model's context does not persist. The runtime sends the tokens as headers only; no token is ever in the model's context."

- [ ] **Step 3: Split Plan 7 in the roadmap**

In `plan/2026-10-12-implementation-roadmap.md`:

1. In the header table, after the row of Plan 6b, add `| Plan 7a | \`2026-10-18-plan-7a-coordinator-core.md\` (executed) |`.
2. In Section 2's table, replace the row of Plan 7 with these four rows, in the order they are built: the runner and `serve` come second, so that a real runner tries the turns' contract early.

   ```markdown
   | 7a | Coordinator: one item end to end | Plan 6b's carried fixes; work records; the board's stop entry; session tokens; the lease, timers from records; rounds and stop rules with the daily cap; takeover (spec Sections 4.1, 5.2, 5.6 and 8.2) | Plans 1 and 4–6 | about 1.5 |
   | 7d | Runner and serve | The runner in sandboxes, `agentswarm serve` and `migrate`, the gateways under the lease, `Drain`, model calls left without an outcome (spec Sections 2 and 8.2; ADR-006) | Plan 7a | about 1.5 |
   | 7b | Coordinator: the lifecycle's breadth | The queue and promotion, the kill switch, close, reopen and auto-close, extensions, links between items, the use cases Plan 9 calls (spec Sections 4.1, 5.1 and 8.3) | Plans 7a and 7d | about 1 |
   | 7c | Inbound events | Trigger mappings, dedup and correlation, principals, responders and key values read at open (spec Sections 3.6 and 5.1) | Plans 7a and 7d | about 1 |
   ```

3. In Section 2's table, in the row of Plan 8, replace "Shadow runs, the independent run," with "Arms, the measurement window, finality, shadow runs, the independent run,".
4. In the row of the checkpoint, replace "**After Plan 7**" with "**After Plan 7d**".
5. In Section 4, replace "River for jobs, from Plan 7 (ADR-004)." with "River for jobs that must outlive the records, from Plan 9 (ADR-004)."
6. In Section 5, after the sentence on Plan 6b, add: "Plan 7a, on DATE, built the coordinator's core: one item runs its primary run through rounds to a stop, every decision recorded before it acts, with session tokens both gateways know, the daily cap, and one lease that fences an instance that lost it. Plan 7d, the runner and `serve`, comes next, so that a real runner tries the turns' contract before Plans 7b and 7c add the lifecycle's breadth and the inbound events; the checkpoint follows it."

- [ ] **Step 4: Update the README**

In `README.md`, replace "Plan 6a the audit chain, and Plan 6b the LLM gateway; Plan 7, the coordinator, is next." with "Plan 6a the audit chain, Plan 6b the LLM gateway, and Plan 7a the coordinator's core; Plan 7d, the runner and `serve`, is next.", and after the line for Plan 6b in the list of plans add `- [Plan 7a: the coordinator's core](plan/2026-10-18-plan-7a-coordinator-core.md) (executed)`.

- [ ] **Step 5: Say in Plan 6b that its residuals are done**

In `plan/2026-10-17-plan-6b-llm-gateway.md`, under "Residuals after the fix wave", at the end of each of the three items that begin "**Plan 7:**", add " Done in Plan 7a, Task 1."

- [ ] **Step 6: Mark this plan executed**

In this plan's header table, set the status to "Executed on DATE. See "Changes made during execution" at the end.", and add that section at the end: the rulings made during execution, and, after the final review, its findings and what became of each.

- [ ] **Step 7: Commit**

```bash
git add plan/2026-10-11-agent-swarm-platform-spec.md plan/adr/ADR-004-durable-execution-postgres.md plan/adr/ADR-006-agent-runtime-sdk-sandbox.md plan/2026-10-12-implementation-roadmap.md README.md plan/2026-10-17-plan-6b-llm-gateway.md plan/2026-10-18-plan-7a-coordinator-core.md
git commit -m "docs: Plan 7a executed; spec amended for the coordinator's core"
```

## Changes made during execution

Tasks 1 to 9 ran on main on 2026-10-19, each task by a fresh implementer and checked by a fresh reviewer. The implementers ran on Haiku and wrote each task's code from this plan's blocks with a helper that applies them as written; each task's review ran on Opus. Every task's change matched the replay-verified scratch copy's before its review: Task 5's and Task 8's once the index lines and hunk positions that an earlier fix had moved are set aside, and Task 9's but for one test, amended by a ruling (below). Each task's tests failed before its implementation, as its Step 2 expects, and passed after it, and lint stayed at `0 issues.`.

Task 1's review approved it, with minor findings only, which were deferred. Every later task's review found at least one issue to fix in this plan's code, or caused by it. The fixes were made test first: each test failed before its fix, or, where this plan's code already kept the rule, failed once a mutant took the rule out. The fix rounds of Tasks 2, 5 and 6 resumed the task's implementer; those of Tasks 3, 4, 7, 8 and 9 went to a fresh implementer on Sonnet (below). Each fix round's scoped re-review ran on Sonnet, until one found the task clean: Task 8 took two rounds, every other task one. Other minor findings were deferred. After each fix, the controller checked that the later tasks' blocks still applied and passed, on the scratch chain with main's fixes, or on a clone of main; one test changed, in Task 9 (below). This plan's code blocks are left as they were approved and replayed, so main differs from them where the changes below are. The work is the twenty-one commits from dd0fd2e to 07fbae1.

**Decisions that changed.** The decisions above stand as approved; where an item below amends one, the item governs.

- **Decision 7, step 1: a wrap-up round in which no turn started (Task 3).** Step 1 said that such a round, like one that ended by an outage or one the instance ending it did not see, concludes nothing and uses up no wrap-up. Wrap-up rounds count toward no budget, and step 5 comes before the budget, so a runner out of reach held a converged run in wrap-up rounds for ever, against the ruling that turns that never started count toward the budget. Such a round still concludes nothing, but it now uses up a wrap-up, so that the [2] run out and the budget stops the run; only a wrap-up round that ended by an outage, or that the instance ending it did not see, uses up none. A converged run whose two wrap-up rounds both found the runner out of reach now stops by its budget, or quiet, not converged. A property test draws 500 runs and requires each to stop within its budget's rounds and two wrap-up rounds; 88 of them failed before the fix. (df42889; `TestAWrapUpRoundNoTurnStartedInUsesUpAWrapUp`, `TestARunStopsWithinItsRoundsAndItsWrapUps`)
- **Decision 12, escapes in a call's arguments (Task 4).** The MCP gateway refused a session token hidden behind an escape in a read call's arguments, in an agent's text for the board and in a tool's output, but it recorded the escaped text: in the arguments of read, board and write calls, in the names of tools it refused, and in refusal reasons that quoted the agent's text, all in the audit store, which is append-only. It now refuses a read or write call whose arguments hold an escape that may hide what any of the call's redaction rules matches, the session tokens' rule included, with "the arguments hold an escape that may hide a secret; write them plainly": a read call after the token check, which keeps its own reason, and a write call once its record is begun, before any read is made for it. A call's record keeps such arguments as null; the name of a tool it refused that holds such an escape is kept as `[redacted:escaped]`; and a refusal's reason that would quote one is that fixed reason. The first fix only recorded such arguments as null. Its implementer found that a read was then still forwarded, with a null record that blinds the board's check that a quote is not the agent's own words, and that a write still made a proposal no person could read, so the ruling was amended to refuse both: a null record never stands for a call that was forwarded or a proposal that was made. A proposal's hash is still taken from the redacted arguments, not from the null, so that two write calls that differ never pass for a repeat. The fix also closes the same gap, older than this plan, for the configuration's rules. The plan's `TestAReadCallWhoseArgumentsHoldASessionTokenIsRefused` held one case twice, byte for byte; the copy is now a token with a JSON escape, and another with a backslash escape. (6fb2aa1, 756fc44; `TestAReadCallWhoseArgumentsHideAMatchBehindAnEscapeIsRefused`, `TestAWriteCallWhoseArgumentsHideAMatchBehindAnEscapeIsRefused`, `TestAQuoteOfWhatARefusedReadWasGivenIsNoEvidence`, `TestAReadCallWhoseArgumentsHideATokenBehindAnEscapeKeepsNoArguments`, `TestABoardCallWhoseArgumentsHideATokenBehindAnEscapeKeepsNoArguments`, `TestAWriteCallWhoseArgumentsHideATokenBehindAnEscapeKeepsNoArguments`, `TestTheNameOfAToolHoldingATokenBehindAnEscapeIsNotStored`, `TestARefusalNeverQuotesATokenBehindAnEscape`, `TestArgsHashTellsApartArgumentsThatHoldAnEscape`, `TestAnEscapeThatHidesNothingKeepsTheRecordWhole`)
- **Decision 3, a release after the holding expired (Task 6).** `Release` found the holding by its epoch alone, so releasing a holding that had already expired moved its expiry to the release, and the next takeover read the release as when the holding before it ended: about 500 ms late in the review's probes. A round whose deadline fell between the real expiry and the release then ended by its deadline under Decision 9, and counted toward the budget, where an outage counts toward none. Releasing a holding that already expired now changes nothing. (f5069a0; `TestReleasingAnExpiredHoldingLeavesItsEnd`)
- **Decision 6, a turn whose start failed (Task 7).** When `Start` failed but the runner had the turn after all, its answer being what was lost, the turn's end was dropped as the end of another turn, and the retry, with the same id, started nothing: the turn counted as interrupted at the deadline, and the round could be quiet. In the review's probe, the run stopped quiet. `Exited` now takes the end of a turn held for a retry when it names that turn's id, and the retry starts it no more. (ae65fd4; `TestTheEndOfATurnWhoseStartFailedIsTaken`)
- **Decisions 10 and 11, an opening's routing and its claim (Task 7).** Routing was recorded as given, and composed into the evidence the item opens with, with no check, so a routing value with an escape, or with a long run of backslashes, landed raw in `item_opened` and on the board: the LLM gateway then refused every snapshot that held it, every turn ended refused, and the run spent its budget. `Open` now refuses, with `ErrInvalid`, routing of more than 16 keys, a key that is empty or of more than 64 characters, a value of more than 256, a key or value that the rules that apply always would change, each tested alone, and a key or value whose escapes may hide what they match, alone or as the member key=value: write tools' scope bindings read the values as recorded, and a value that redaction changed is not a value (spec Section 3.3). Its refusal quotes no routing. The opening claim is checked as the board holds it, trimmed, alone and as the member `claim=…`, and is replaced whole by `Opened by <source>: [an opening the redaction rules could not read]` when its escapes may hide a match: a backstop for records `Open` did not check. The principal and the responders are still recorded as given. Tests now also pin the title's replacement and both cuts, which this plan's code already made. (ae65fd4; `TestRoutingThatHidesAMatchBehindAnEscapeIsRefused`, `TestRoutingThatTheAlwaysRulesWouldChangeIsRefused`, `TestRoutingIsBounded`, `TestTheOpeningClaimIsReplacedWhenItsEscapesMayHideAMatch`, `TestATitleThatHidesAMatchBehindAnEscapeIsReplaced`, `TestALongTitleIsRecordedCut`, `TestALongOpeningClaimIsPostedCut`)
- **Decision 9, a round taken up late (Task 8).** A round this instance first acts on with less than a third of its span left, from its opening to its deadline, as after a database's failover, had its turns interrupted at once and could be decided quiet: in the review's probe, the board's store was down until 5 s before the deadline, and the run stopped quiet. Such a round is now held as usual, so that its agents use the time left, but it ends unseen, however it ends: it is never quiet, and, as a wrap-up round, it concludes nothing and uses up no wrap-up. Exactly a third left counts as seen. Ending it as a missed round instead would have recorded an end in the future. It costs one more round after a late takeover, even when the agents had ended their turns. (a588899; `TestARoundHeldWithLessThanAThirdOfItsTimeLeftIsNeverQuiet`, `TestARoundHeldWithAThirdOfItsTimeLeftOrMoreIsSeen`, `TestARoundTakenOverWithLessThanAThirdLeftIsNeverQuietThoughItsTurnsEnded`)
- **Decision 9, records a takeover cannot read (Task 8).** A takeover refused the lease for a record of a version it cannot read only when a load returned one, so one failed load hid such a record: the takeover went on, and that item's steps logged `ErrVersion` every [5 s]. A takeover now refuses the lease, and holds nothing, when it fails to load an active item's records, or they hold a version it cannot read; its leader gives the lease up and takes it again, or, for a version, stops for good (below). It lets by only an item whose records are invalid, so that one corrupt item cannot block every takeover: that item's steps fail and are tried again, and the other items go on. (a588899, f13ab9d; `TestATakeoverThatCannotLoadAnItemsRecordsRefusesTheLease`, `TestATakeoverCarriesOnPastAnItemWhoseRecordsAreInvalid`, `TestATakeoverCarriesOnPastAnItemWhoseRecordsDoNotDecode`)
- **Decision 10, records that do not decode (Task 8).** A record of this version whose body did not decode failed the Postgres store's `Load`, and so, under the ruling above, refused the lease at every takeover. `Load` now reports it as invalid, `item.ErrInvalid`, keeping the decode error, so that a takeover lets its item by; a read that fails is still an error. A body whose version is missing, null or below 1 does not decode either: versions start at 1, so such a body is corrupt, not of a later version, which would have stopped the leader on every instance. (f13ab9d, ac42ed7; `TestARecordOfThisVersionThatDoesNotDecodeIsInvalid`, `TestARecordOfAnotherVersionLoadsAsItsVersionAlone`, `TestAFailedReadOfRecordsIsNotInvalid`, `TestABodyWithNoVersionDoesNotDecode`, `TestARecordWithNoVersionIsInvalid`, `TestABodyOfThisVersionDecodesWhole`)
- **Decisions 6 and 7, a turn started too late (Task 8).** The re-review found the late round's defect by another way in: with the runner out of reach from a round's start and back 5 s before its deadline, the retry started both turns, the deadline interrupted them, and the run stopped quiet. A turn this instance starts with less than a third of the round's span left, and that still runs at the deadline, now counts as unstarted: the round is never quiet, and a wrap-up round all of whose turns so count concludes nothing and uses up a wrap-up. A turn that ends by itself keeps the class it reports. The round is not marked unseen for it: an unseen wrap-up round uses up none, so a runner late in every round would repeat wrap-up rounds for ever. In a round taken up late, unseen already, turns count as they did. Where `tookPart` and `wrapUps` say that a turn started, they now mean that it started in time. (2175807; `TestATurnStartedWithLessThanAThirdOfTheRoundLeftCountsAsNotStarted`, `TestATurnStartedWithAThirdOfTheRoundLeftOrMoreIsInterrupted`, `TestATurnStartedLateThatEndsBeforeTheDeadlineKeepsItsClass`)
- **Decision 3, a leader that stops for good (Task 9).** `Lead` returned only its first take's refusal, so a later takeover that found records of a later version stopped the leader with one line in its log and no word to its owner. The leader now has `Done`, closed once it stops for good, when it is stopped, when the context it leads under ends, or when a takeover is refused for records of a version it cannot read, and `Err`, which is that refusal, or nil; the first stop stands. Tests now pin what this plan's code already did: the watch and `Stop` end the holding before they fence the coordinator, as `lose`'s doc now says, where it said the reverse; a takeover whose read fails, or whose write finds the lease gone, gives the lease up and takes it again; and the watch counts from when the last good renewal was sent, as this decision says. (07fbae1; `TestALeaderWhoseLaterTakeoverFindsALaterVersionSaysSoOnDone`, `TestALeaderThatStopsSaysSoOnDone`, `TestALeaderWhoseContextEndsSaysSoOnDone`, `TestALeaderThatTakesTheLeaseAgainHasNotStoppedForGood`, `TestTheWatchEndsTheHoldingBeforeItFences`, `TestStopEndsTheHoldingBeforeItFences`, `TestATakeoverWhoseReadFailsGivesTheLeaseUpAndTakesItAgain`, `TestATakeoverWhoseWriteFindsTheLeaseGoneTakesItAgain`, `TestTheWatchCountsFromWhenTheLastGoodRenewalWasSent`)

**Other changes.**

- **A person's ruling in its round (Task 2).** `Boards.Rule` stamped a ruling with the round its run's board context held, not with the board's latest round, as every other post is stamped. A round's report never counted a ruling, so a round whose only activity was a ruling read as quiet, against Decision 7. A ruling now carries the board's round. (0547971; `TestAPersonsRulingIsStampedWithTheBoardsRound`)
- **The fold's refusals of order (Task 3).** Six of the fold's refusals had no test: a round opened against the last decision, a round opened while one is open, an end that names another round, a stop for a reason its round did not decide, any record once the item awaits its close, and a record that names another run. Seven cases of `TestRecordsFoldInTheLifecyclesOrderOnly` now pin them, each failing alone when its refusal is taken out. (df42889)
- **The memory store's active items (Task 5).** `memory.Records.Active` listed every item it held, one whose append committed nothing and one with only a record of a later version among them, where Postgres lists the items that opened and do not await their close. The memory store is the coordinator tests' substrate, and must not hide what Postgres does: it now lists the same items, an append that commits nothing stores nothing, and a test of each store pins it. Task 9's `TestALeaderStopsOverRecordsItCannotRead` leaned on the difference, and now writes the item's opening before its record of a later version, as Task 8's `TestATakeoverRefusesRecordsItCannotRead` does: the one later block that changed. (b526799, 42226d7; `TestActiveItemsAreOpenedAndNotAwaitingClose`)

The spec's text follows these changes. Sections 2 and 9.4 say that the MCP gateway refuses a read or write call whose arguments hold an escape that may hide what a redaction rule matches, and records no arguments, tool name or reason that holds one. Section 4.1 bounds an item's routing values, and refuses an opening when the rules that apply always would change one of its routing keys or values, or an escape in them may hide what those rules match. Section 5.6 says which wrap-up rounds stop a run `converged` and which count toward the [2], that a round an instance takes up with less than a third of its time limit left is never quiet, and that a turn started that late, and still running at the limit, counts as one that could not start. Section 8.2 says what a takeover does with such a round, with records it fails to read or of a version it cannot read, and with an item whose records are corrupt. ADR-004 says that releasing a holding that already expired changes nothing.

**Rulings that stand without a code change.**

- **Fix rounds on Sonnet (Tasks 3, 4, 7, 8 and 9).** These fix rounds went to a fresh implementer on Sonnet, not to the implementer on Haiku that transcribed the task: their fixes were new tests and rules written from a reviewer's prose, not blocks to transcribe, and a mid-tier model is the floor for that work.
- **Later blocks kept applicable (Tasks 7 and 8).** Task 7's fix kept intact the lines of `coordinator.go` that Task 8's patches take as their context, and Task 8's fixes left alone `harness_test.go`, which Task 9 patches; they could change `coordinator.go`, which Task 9 does not patch. Each fix was checked with `git apply --check` of the later blocks, and by running them, so that Tasks 8 and 9 applied as this plan wrote them.
- **The plan's duplicate test case (Task 4).** The case that `TestAReadCallWhoseArgumentsHoldASessionTokenIsRefused` held twice was a token with a JSON escape, which the tool that wrote this plan had decoded. A scan of this plan's code for repeated lines near each other found no other case lost so, and the plan's other escapes survived. Task 4's fix replaced the copy (above).
- **A parser differential (Task 4).** A background security scan of Task 4 named two ways a session token could leave through `call.go`. One, incomplete redaction, is the finding fixed above. The other, a parser differential, does not hold: the gateway decodes a call's arguments once, strictly, and sends that decoding re-encoded, so a server reads what the gateway checked. What passes is a token in an encoding beyond JSON and backslash escapes, which the ruling on session tokens in other encodings accepts (above).

**Constraints added for later plans.** They add to "Constraints carried to later plans" above.

- **Plan 7b:**
  - Treat `board.ErrStopped` from `Boards.Notice` as done when a run stops mid-round, killed or closed: a board that holds its stop entry takes no notice, and a step that posts notices would be tried again for ever.
  - Check a person's reason, in `Boards.Rule` or in Plan 7b's interface, as `agentText` checks an agent's text, alone and as its member in the snapshot: it gets only a length check, and `read_board` shows whole entries, so a reason whose escapes may hide what a rule removes would make the LLM gateway refuse every later snapshot of the run.
  - After an append error, have `Open` load the item by its id and advance it if its opening landed: an opening whose commit answer was lost is left alone until the next takeover, since `Open` returns the error and arms no retry, so the item the records hold waits queued, and the person who got the error likely opens another (the final review's probe G). Guard `step` against a state with no opening, on which it would panic.
  - Check an item's routing again under the revision it locks when it starts, and leave the item not staffed if the check fails: `Open` checks it under the revision current at the opening, and the locked revision may add a rule that applies always and matches a routing value, which then reaches sessions and scope bindings as given. The window between an opening and its start is milliseconds today; the queue makes it real.
  - Write the record kinds Plan 7b adds at a new body version (`item.Version`): after a rollback, an older build reads a body of version 1 that holds a kind it does not know as corrupt, and tries its item again every [5 s] for ever, where a body of a later version makes its leader stop for good and say why.
    - **Amended by Plan 7d-1** (`2026-10-20-plan-7d1-serve-and-harness.md`, Decisions 18 and 19): a body of a later version no longer stops the leader. The older build skips each item that holds one, and the item stays held until a build that can read it leads. A new kind still comes at a new body version, so that older builds skip its items rather than try them again as corrupt. A platform-wide control, such as the kill switch or the queue's limit, comes with a migration whose `min_reader` is its own level, so that an older build cannot ignore it.
  - Post the notice spec Section 3.2 gives each item already started when a yank withdraws a skill version its locked revision pins: such an item keeps its locked version, and no plan owned the notice.
- **Plan 7c:** routing read from inbound events reaches `Open`, which tests whether the rules that apply always would change a key or a value alone, not the member key=value. A rule that matches only a member, such as one for `password=…`, does not refuse the routing `{"password": "…"}`, which `item_opened` then records as given, though the opening claim shows it redacted. Test `Text(k+"="+v)` as well. Done in the final review's fix wave (bb0a878): `Open` refuses routing whose member key=value those rules would change.
- **Plan 7d:**
  - Keep the coordinator's `Grace` at least the gateways' record timeout: a write call commits its proposal apart from the call's time, up to that timeout, and one that commits after its round's end is refused, and may be reported as "the run stopped", since the next round's tokens replaced its session's.
  - Bound a takeover that waits for the write in flight of a holder that died or was cut off: today only `idle_in_transaction_session_timeout`, 60 s (`internal/db/db.go`), about four times the lease's time, bounds it, and `Take` has no lock timeout under the leader's long-lived context. Choose the bound deliberately.
  - Consider a guard bound to a holding when `serve` wires the gateways: `Check` validates the instance's current epoch, not the one a write was issued under, and the gateways' guarded board appends are not serialized with `Fence`.
  - Make the runner return once its context ends, and never block in `Stop` or `StopAll`: `Fence` waits for the coordinator's lock, under which the coordinator calls `Turns.Start`, `Turns.Stop`, `Turns.StopAll` and `Sessions.EndAll`, the last three with no context, and the leader holds its own lock across `Fence`, so a runner that blocks stalls the lease's renewals.
  - Kill a sandbox at its round's deadline, as a backstop to the coordinator's own deadline, and report its turn interrupted. Which comes first does not matter: the coordinator counts a turn that it started with less than a third of the round's span left, or never learned had started, as unstarted when it is reported interrupted, as its own deadline counts it (the final review's fix wave, 4cd820c).
  - Expect a turn that starts in time but waits out its round on a slow or hanging model vendor to be interrupted at the deadline. Its call keeps the round from being quiet only if it is still running once the wait for the run's calls ends, the grace and the settling wait after the deadline, or was noticed as refused, failed or of unknown outcome (2bc260e). The LLM gateway runs the vendor's call apart from the client's request, and records it ok when the answer comes, so a slow call that answers ok within that wait settles with nothing noticed: the interrupted turns take part, and the round can be quiet, though no agent had an answer in time to act on (the fix wave's re-review, probe RR2). Make a quiet round, and a wrap-up round's taking part, rest on positive evidence instead, such as each agent of the round having had an ok answer to a model call of the round before its time limit (the final review's Recommendation 3).
  - Have `serve` select on `Leader.Done()` and exit with `Leader.Err()`, or records of a later version, as after a rollback, leave the platform leaderless and silent. `Done` closes when the leader decides to stop, before the lease is released, and `Stop` does not wait for a take in flight, so `Done` is no receipt of the release: give the leader a way to wait for its take and its release, or exit and let the holding expire. The leader runs `TakeOver` on a timer's goroutine, and `Timers` does not say that its functions run concurrently.
  - Choose the bound on a take's wait, which a constraint above asks for, below the lease's time less the margin, [10 s], for example with a lock timeout: a take blocked that long or longer, as behind the writes in flight of a holder cut off by a partition, fences its coordinator at once, unless a renewal answers first, since the leader counts a holding from the take's send (318984e), and the leader takes the lease again only once that holding expired.
  - Measure the `Clock`'s offset to the database before a take is sent, or give the leader a clock that never steps: the leader's watch compares the reading of its `Clock` taken when a take or a renewal was sent with later readings, and the offset cancels out only while it stays the same between them. A `Clock` that measures the offset as the lease is taken changes it after the take's send is read, and a change that sets the clock back makes the watch late by as much, so that past the margin the coordinator would act after its holding expired.
  - Make the takeover's version check cover board events too: a takeover refuses the lease when an active item's work records hold a version this build cannot read, but reads no board event, and board events carry no version, so an entry a later build writes that this one cannot read fails that run's board reads at every step instead of stopping the leader.
  - Wire one daily cap into the coordinator and the LLM gateway: today `app.Options.DailyCap` and `cost.Limits.DailyCap` are set apart, and the coordinator counts a day by the database's time, through its `Clock`, while the gateway counts it by the `Now` it is given.

**The final review.** A fresh reviewer on Opus 5.5 read the whole branch, 882cf1e to 1c3be9a: this plan, the spec sections it amends, the ledger, and every Go and SQL file on main, whole. It probed what it found, with a probe for each finding, and triaged the minor findings the tasks had deferred. Its verdict was "with fixes": no Critical issue, one Important one and four Minor ones, and its triage raised five deferred minor findings to fix now, all as Important, two of them with tests only. It found the lease, decide-record-act and fencing sound: a seeded stress of 150 runs and 394 rounds, with timers on goroutines of their own, a runner that failed at random, exits of every class, and fences and takeovers at random moments, found no race, no deadlock, nothing recorded while fenced, and every run stopped. The controller ruled that one fix wave on main take the six Important findings and two of the four Minor ones, the cap on wrap-up rounds and the take's holding; Plan 7b takes the other two (above). The wave made a commit for each, test first: each code fix's test, or the probe it ports, failed on 1c3be9a for the reason its finding names, and passed after the fix. The tests of the lease and of the leader's guards pin code that was already right, and were checked instead against the mutants they are to catch: each fails on its mutant, and the rest of its package passes on it. One fix, of the take's holding, was reverted in the wave (below). A re-review of the wave confirmed its fixes, and found three things this part had not said: what the coordinator cannot tell of a slow vendor, that the leader's watch is late if its `Clock` steps back after a take's send, and that a wrap-up round on a vendor that hangs still concludes; each is stated below, and carried to Plan 7d. Where an item below amends a decision above, the item governs. `make lint test-all` passed after the wave.

- **Important, fixed: a late-started turn reported interrupted (4cd820c).** The rule that a turn started with less than a third of its round's span left, and still running at the deadline, counts as unstarted lived only on the deadline's path. A runner that kills a sandbox at its deadline, as Plan 7d's must, reports the turn interrupted, and its report can reach the coordinator before the coordinator's own timer fires: the turn kept its class, and in the review's probe two turns started with 5 s left were reported interrupted a millisecond before the deadline, and the run stopped quiet. The Plan 7d constraint to leave that stop to the coordinator's timer could not hold, since the runner cannot know when the timer fires. `Exited` now counts a turn reported interrupted as unstarted when this instance started it that late, or never learned that it started, as a turn held for a retry of its start; every other class is kept as reported. Decision 6 is amended. (`TestATurnStartedLateThatTheRunnerReportsInterruptedCountsAsNotStarted`, `TestATurnWhoseStartFailedThatTheRunnerReportsInterruptedCountsAsNotStarted`)
- **Important, raised from Task 8's deferral, fixed: a model vendor that hangs (2bc260e).** Task 8 deferred "a slow vendor's turn is interrupted and can make a round quiet" to Plan 7d, as something the coordinator cannot tell. It can tell a vendor that hangs or fails: the LLM gateway answers its client at the deadline and lets the attempt run on, up to its call timeout, so the round's end found the run's calls still in flight, recorded the round unsettled, and decided it quiet (probe I2); calls that failed, or whose outcome was unknown, were noticed on the board in the same step that decided quiet (probe I). It was the design review's Critical, a vendor's brownout stopping every run quiet, in its hanging form. A round is now quiet only if its model calls settled and none was noticed in it as refused, failed or of unknown outcome, counting those its report holds, which an earlier try of its end may have posted, and those noticed at its end: the round's report counts its notices of model calls (`board.RoundFacts.FailedCalls`), and `item.Ending` carries the count, so that `item` stays pure. A round with one stray failed call runs one more round. It cannot tell a slow vendor: the gateway runs the vendor's call apart from the client's request, and records it ok when the answer comes, so a call that answers ok within the grace and the settling wait settles with nothing noticed, the turns its agent waited out, interrupted at the deadline, take part, and the round can be quiet (the fix wave's re-review, probe RR2). Plan 7d carries it, with a rule resting on positive evidence (above). Decision 7's quiet round is amended. (`TestARoundWhoseModelCallsFailedOrDidNotSettleIsNeverQuiet`, `TestTheCallsNoticedBeforeARoundsEndFailedCountWhenItIsDecidedAgain`, `TestARoundWithEntriesOrTroubleIsNeverQuiet`, `TestARoundsReportSaysWhatTheRoundProduced`)
- **Important, raised from Task 3's deferral, fixed: who took part (8a23048).** `tookPart` counted every turn but an unstarted one, so a wrap-up round in which no agent could make a model call, its turns all failed at the vendor, refused or crashed, stopped its run converged; with the daily cap reached, it stopped the run converged instead of cut, and the item's reason was converged instead of not_staffed, which Plan 8's measurement reads (probe D). A turn now takes part only if it ended, finished or was interrupted, counted after the rule above. A wrap-up round no agent took part in concludes nothing and still uses up a wrap-up, so the run's wrap-ups stay bounded, and with the cap reached it stops the run cut. A converged hypothesis waits one more wrap-up round after a vendor's outage. Decision 7, step 1, is amended. (`TestAWrapUpRoundNoAgentCouldActInConcludesNothing`, `TestOnlyATurnThatEndedFinishedOrWasInterruptedTookPart`)
- **Important, raised from Task 7's deferral and the Plan 7c constraint, fixed: routing's member key=value (bb0a878).** `checkRouting` tested each routing key and value alone against the rules that apply always, but not the member key=value they form, so a rule that matches only a member, such as one for `password=…`, let the routing `{"password": "…"}` through, and `item_opened`, an append-only audit record, kept its value while the board's claim showed it redacted (probe B). People reach `Open` through Plans 7b and 9 before Plan 7c. `checkRouting` now refuses routing whose member those rules would change, and its doc, which overstated what it checked (a Task 10 minor finding), says what it does. Task 7's change to Decisions 10 and 11 is amended. (`TestRoutingThatTheAlwaysRulesWouldChangeIsRefused`)
- **Important, raised from Task 6's deferral, tests only: the lease's safety predicates (45ae49c).** No test pinned Release's `epoch = $3`, Renew's `expires_at > clock_timestamp()`, or the compare-and-swap on the local epoch: a mutant that took out any one of them passed every test of the package. Without the first, an instance whose holding was taken over, and that gives the lease up, as the leader does after a takeover that found the lease gone, would end the new holder's holding (probe F). Each predicate now has a test that fails on its mutant, the rest of the package passing; the third holds a renewal's answer, with a query tracer, while the instance takes the lease again. (`TestAReleaseOfAnEarlierHoldingLeavesTheNewOne`, `TestAnExpiredHoldingIsNotRenewed`, `TestALateRenewalOfAnExpiredHoldingLeavesTheNextOne`)
- **Important, raised from Task 9's deferral, tests only: the leader's guards (1f64fa8).** With real timers, a renewal or a watch whose timer had fired can run after `lose()` cancelled it. Without the guard before Renew, such a renewal renews a holding the leader no longer acts under and arms the next one, a chain that keeps the holding from expiring, so that no instance, this one included, can take the lease; without the guard in the watch, a late watch arms another watch and fences the coordinator again. Neither guard had a test. Timers that run each function on a goroutine of its own, and hold back on request those whose time came, let a test run a cancelled timer's function after the loss; each test fails with its guard removed. (`TestARenewalThatRunsAfterItsHoldingWasLostRenewsNothing`, `TestAWatchThatRunsAfterItsHoldingWasLostDoesNothing`)
- **Minor, fixed: wrap-up rounds without bound (e10742a).** Wrap-up rounds that ended unseen or by an outage use up no wrap-up and count toward no budget, and the converged check comes before the budget, so a platform that kept failing during wrap-up rounds, through late takeovers or crash loops, held a run in them until the daily cap, each raising the sessions' token allowance by one round's share: in the probe, twelve wrap-up rounds raised it from 90,000 to 390,000 (probe C), where Task 8's ruling had priced an unseen wrap-up round at one more round. A run now opens at most twice [2] wrap-up rounds in all, counted or not (`item.MaxWrapUpRounds`); once it has, a converged hypothesis gets no more, and the budget and quiet rules decide. A second property test draws unseen rounds and wrap-up rounds lost to an outage, and requires each run to stop within its budget's rounds and those wrap-up rounds; 113 of its 500 runs failed on 1c3be9a. The first property test stands as it was. Decision 7, step 5, is amended. (`TestARunOpensAtMostTwiceItsWrapUpsInWrapUpRounds`, `TestARunStopsWithinItsRoundsAndAllItsWrapUpRounds`)
- **Minor, fixed and then reverted: a take that waited (8e7ae8b, 318984e).** The leader's watch counts a holding's age from `good`, which a take sets to the time it was sent. A take that waited the lease's time less the margin or more, as behind the writes in flight of a holder cut off by a partition, so fences its coordinator at once, while its first renewal is in flight and the holding has its whole time left, and the leader then waits for its own holding to expire before it takes the lease again (probe E). The controller's first ruling set `good` from the take's `term.At`, the holding's time in the database (8e7ae8b). A security scan of that commit found that it fails open: the watch compares `good` with the leader's `Clock`, which reads the database's time only as well as Plan 7d's estimate of the offset holds, so with an estimate off by more than the margin the coordinator would act after its holding expired in the database. Counting from the send compares two readings of one clock, in which the offset to the database cancels out as long as the clock does not step between them, and the database starts the holding no earlier than the send. Fencing must not depend on the `Clock`'s offset to the database, so the ruling was reversed and the change reverted (318984e). A `Clock` that measures its offset as the lease is taken, as Plan 7d's was to, would step after the take's send is read: Plan 7d measures the offset before a take is sent, or gives the leader a clock that never steps (above). The defect costs liveness only: a take that waited that long fences its coordinator at once, unless a renewal answers first, and the leader takes the lease again once its own holding expired. Plan 7d bounds how long a take can wait (above). (`TestATakeThatWaitedTheLeasesTimeLessTheMarginFencesAtOnce`)
- **Minor, carried to Plan 7b: an opening whose commit answer was lost (probe G), and routing checked under the opening's revision only.** Both are among the Plan 7b constraints above.
- **From the fix wave's re-review, carried to Plan 7d: a wrap-up round on a vendor that hangs or fails.** A wrap-up round whose agents all waited out the round on a model vendor that hangs, or keeps failing while the gateway retries, still stops its run `converged`: their turns are interrupted at the deadline, an interrupted turn takes part, and the model calls still in flight or noticed as failed decide only whether a round is quiet. It is as it was before the wave. Plan 7d makes a wrap-up round's taking part rest on the same positive evidence as a quiet round (above).

**The deferred minor findings it raised.** Five, each above: Task 8's slow vendor, with the Plan 7d constraint that the coordinator cannot tell; Task 3's turns that took part; Task 7's member key=value, with the Plan 7c constraint and Task 10's finding on `checkRouting`'s doc; Task 6's lease predicates; and Task 9's guards.

**What it left, by group.** The other deferred minor findings stay deferred:

- Task 1, all nine: quoting parts, `Record`'s redundant parameter, comments, test sleeps and commit hygiene; no path stores or forwards a secret.
- Task 2, all seven: cosmetics of the stop entry and the notices, a replay that trusts derived content, and test gaps; only the platform writes the board, and sessions end before the stop.
- Task 3, the rest: stale comments, gaps in the fold's defence in depth, `Run.End`'s precondition, the allowance's doc, and `RoundEnded`'s missing cap; no path of the coordinator reaches them.
- Task 4, all seven: the prefix's case for other sessions' tokens, redactors without the caller's own token, `tokens.New`'s default, a regular expression's cost, and test gaps; no token reaches text an agent reads.
- Task 5, all eight: the text of decode errors, a schema's uniqueness, a panic in `decide` (no path in `record`'s closure panics), the memory store's fidelity (it changes no folded state), DISTINCT and collation, and docs.
- Task 6, the rest: test hygiene and nits; the carry on `Check`'s epoch stays with Plan 7d.
- Task 7, the rest: `ErrNoSession`'s wording (a Plan 7d carry), test gaps, the principal and the responders (ruled in Task 7; Plan 7c reads them), `maxSteps`, nits and citations.
- Task 8, the rest: `TakeOver` holding the lease when cancelled (the leader fences), timers that do not name their holding (the stress found nothing), hides per boot, log levels, `Append`'s version, a version below 1 in `Apply`, and test gaps.
- Task 9, the rest: the 50 ms sleep, `Stop` not waiting for a take (a Plan 7d carry), naming, `Err`'s lock, and a release made twice, which is harmless.
- Task 10, all four: Section 15's rows for the routing bounds and the third-of-a-limit rule, and ADR-006's citations, stay deferred; `checkRouting`'s doc is fixed (above), and the spec's list of the sections amended on 2026-10-19 now names Section 12.1, which Task 10's fix round amended.

**What it declined to judge.** The controller ruled on each:

- `Revisions.Lock` failing at an item's start would be tried again for ever; it cannot happen, since the registry refuses a revision it cannot lock.
- Normal rounds that end by an outage count toward no budget, so a platform in a crash loop can stretch a run; spec Section 8.2 accepts it, and the daily cap bounds the spend.
- A round whose turns are all refused at once ends at once: it fails fast, with notices, and counts toward the budget.
- The coordinator's and the LLM gateway's daily caps, set apart, and their days, counted by two clocks: carried to Plan 7d (above).
- The principal and the responders, recorded unchecked: ruled in Task 7; Plan 7c reads them.
- A corrupt item, tried again every [5 s], at Error, for ever: spec Section 8.2 as amended.
- Board events of a version this build cannot read, outside the takeover's version check: carried to Plan 7d (above).
- A take blocked up to 60 s behind a holder cut off by a partition, and `Check` validating the instance's current epoch: Plan 7d's carries already.
- Record kinds that Plan 7b adds, written at version 1, read as corrupt after a rollback: carried to Plan 7b (above).
- One failed renewal fences at good + [10 s]: Decision 3's parameters.
- Spec Section 3.2's notice to the items already started when a skill version they pin is yanked: no plan owned it; carried to Plan 7b (above).
- The cost of the coordinator's lock and of the LLM gateway's admission lock: Plan 10.

The spec's text follows the wave. Section 4.1 refuses routing whose member key=value a rule that applies always would change. Section 5.6 says that a turn takes part only if it ended, finished or was interrupted; that a wrap-up round no agent took part in concludes nothing, counts toward the [2], and with the daily cap reached stops the run `cut`; that a run opens at most twice [2] wrap-up rounds in all; that a round is quiet only if the run's model calls ended by the end of the wait for them and none was noticed in it as refused, failed or of unknown outcome; and that a late-started turn the runner reports interrupted, or one whose start the coordinator never learned of, counts as one that could not start. Section 8.2 says that a model call so noticed, or calls still in flight, as on a vendor that hangs, keep a round from being quiet, and Section 15 gives the wrap-up rounds opened in all. The Status row names Section 12.1 among the sections amended on 2026-10-19. No spec text changed for the take's holding, which the spec does not tie to the database's time.
