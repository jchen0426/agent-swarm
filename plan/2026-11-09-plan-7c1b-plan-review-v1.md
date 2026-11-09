# Plan 7c-1b plan review, v1

Reviewer: Opus 5.5, fresh context, read-only, against HEAD 441ad56.
Under review: `plan/2026-11-09-plan-7c1b-inbound-events.md` (draft v1).
Authority: spec, then design v2.1 (Section 3 rulings not reopened).

**Verdict: approve with fixes.** 0 Critical, 11 Important, 10 Minor. Most code claims hold. What does not hold is the hand-off between tasks and the existing interleaving oracles. Five Important findings (I1–I5) would each stall an implementer, or have one write code that a later task's test or the world refutes. Four (I6–I9) concern Task 10 and the board. Two (I10, I11) are decisions or bounds the author should see.

## What was verified and holds

- R110's callers: autoclose.go:93 and :183, views.go:502 and reopen.go:195 are the only production callers of `AutoCloseAt`. `ResolvedAt` is lifecycle.go:18. The interleaving file has four reads, not three (see M7).
- R113: serve's `server` is a closure inside `newServers` (serve.go:775). R114: `worstDrain` counts `6*stopWait` (serve.go:1017), while `close` shuts four servers (serve.go:1029). So `8*stopWait` and 1m37s are right.
- R119: `resumeCauses = []string{"extension"}` (board/state.go:303). `resumeBoard` hardcodes `resumeCause` (rounds.go:17, :62). `Run.LatestResume` exists (item/extend.go:103).
- R120: `requestIn` is acts.go:313. A person's key is hashed into `Request.Key`, so the literal `flap:<id>` collision R120 guards against is real.
- R112: `mayExtend` and `mayResume` (extend.go:147-198) give exactly the codes R112 lists. A stopped run can never hit `ErrStopping`, because `Concludes` needs `Stopped == nil` (item/extend.go:74).
- Migration shape: `audit_records_guard()` raises `'% is append-only', TG_TABLE_NAME` and honours `agentswarm.retention` (0003:48-56). `work_records.kind` has no CHECK, so `item_triggered` needs no DDL there. 0011 has no `min_reader`, and `TestMigration11ReadersAreOfItsOwnLevel` asserts `Level() == 11` (migrations_test.go:88).
- `Records.Append` takes the advisory lock, then `guard.Check`, then decide, in one read-committed transaction (postgres/records.go:149-201). `AppendInbound` fits inside it as R109 says. The memory store has no guard (memory/records.go:128). Guarding happens only in the world's fake (interleave_world_test.go:1186-1203).
- `checkOpener` is coordinator.go:1421, and its own-principal rule is :1448-1450. The `source` regex `^[a-z][a-z0-9-]{0,62}$` is coordinator.go:263. Routing bounds are 16 keys, 64 and 256 runes (coordinator.go:271-273).
- `decide`'s order is converged, then cut, then trigger, then not_staffed (decide.go:158-170). The board's `Notice` and `checkNoticeShape` are as quoted (board.go:403-425, state.go:390-412). The opening claim is rounds.go:35.
- The secret file is trimmed of trailing CR and LF by `files.Store.Secret` (secrets/files/files.go:60), so R123's one-or-two-lines rule does not trip on a final newline.
- Package placement compiles: the e2e depguard allows only std, testify and testcontainers (.golangci.yml:94-118), as R129 says. `app` importing `internal/inbound/mapping` creates no cycle with `internal/inbound` importing `app`.
- The calendar estimate is credible (see M9 for spend).

## Important

### I1. `Opened.Trigger` has no path into the opening (Tasks 5, 6, 7; cross-task)

**Failure.** Task 6 step 7 says "`Opened.Trigger` is `{Source, DedupKey, CorrelationKey}`". But `openLocked` builds `item.Opened` from `OpenInput` alone (coordinator.go:356-360), and `OpenInput` has no trigger field (coordinator.go:234-259). Task 5's `also` gives only the records that *follow* the opening. The Task 6 implementer has nowhere to put the trigger without changing Task 5's produced interface. Meanwhile Task 2's fold (R126) refuses an inbound opening's `item_triggered` unless the opening's `Trigger` is set.

**Fix.** In Task 5's Interfaces, add `OpenInput.Trigger *item.Trigger`, copied into `Opened.Trigger`. `checkOpener` refuses a `Trigger` unless `Actor.Source` is set, with a test in Task 5.

### I2. R126 ("the item's second record") contradicts the not-staffed opening (Tasks 2, 5, 6, 7)

**Failure.** `Open` appends `item_awaiting_close` right after `item_opened` (coordinator.go:362-364). Task 5 says `also`'s records "follow the opening in its append" but does not say before or after that record. If they go after, a kill-switch-on inbound opening is `opened, awaiting_close, triggered[, linked]`, and Task 2's fold refuses it. Task 6's test ("the same two records and `item_awaiting_close` in one append") and Task 7's flap test fix no order. Two implementers can each be locally right and together red.

**Fix.** Task 5 fixes the order as `item_opened`, `also`'s records, then `item_awaiting_close`. Both `item_triggered` and `item_linked` are admitted while queued. Tasks 6 and 7's tests then assert records 1 to 4 by kind.

### I3. Task 7 can never record `declined:no_run` (Task 7, ruling 9)

**Failure.** The resume fires "when … the item awaits its close **with a stopped run**". An item opened not staffed has no run, so that condition is false. No decline is recorded, and `Resume` stays empty. Yet the same task's table test expects `declined:no_run` for exactly this item, as do ruling 9 and D7. Invariant 16 inherits the same gap.

**Fix.**
- The condition becomes "awaits its close, and its run is absent or stopped".
- `mayFiringResume` maps `CodeNotStarted` to `no_run`.
- Invariant 16's text says the same.

### I4. R131's reason codes cannot be told apart (Tasks 5, 6)

**Failure.** `runnable` (coordinator.go:1287-1299), `checkRouting` (:1379-1400), the source check (:344) and every `checkOpener` refusal all return a bare `fmt.Errorf("%w: …", ErrInvalid)`. `Ingest` cannot map them to `type_not_runnable`, `routing_refused` and `invalid` without matching on strings. Task 6's `TestAnOpeningRefusedByOpenIsRecordedWithItsReason` asserts the distinct codes.

**Fix.** Task 5 adds unexported sentinels (`errNotRunnable`, `errRoutingRefused`), wrapped together with `ErrInvalid`, so callers that check `ErrInvalid` are unchanged. One test for each.

### I5. The account goes stale after an unknown outcome, and Ingest has no branch for it (Tasks 6, 7)

**(a) A duplicate opening.** Suppose an inbound opening lands with its answer lost, and `unrecorded`'s lookup also fails. Then the retry is armed only after the retry wait (coordinator.go:400-420). Meanwhile the source retries after `Retry-After: 1` with a *different* event of the same dedup key. `LastItem` names the new item: it folds and is not closed, yet it is not in `c.items`. Task 6 step 7 covers only "does not fold or is held by a later build" (refused) and "closed" (flap). Its "otherwise" opens a second item for the same key. That is C1's class, and invariant 17 will catch it in Task 10.

**(b) A missed match.** A matched `AppendInbound` whose answer is lost leaves `c.items[X]` without the new instance and keys. Nothing arms `c.retry(X)`: Task 7 calls only `countAsStarting`, and Task 6 does nothing. That instance's later resolution can then miss X (by instance, by dedup key, and by correlation past `within`) and be stored as `resolved_unmatched`. X's trigger then fires for ever and X never closes by itself; invariant 14 catches this. A firing resume that did commit also never opens its round until something else advances X.

**Fix.**
- In step 7, an item named by `LastItem` that folds, is not closed and is not in the account is loaded into the account. Step 4's match then reruns.
- On any error from `recordInbound` whose outcome may have landed, `Ingest` arms `c.retry(id)`, as `carryOn` does. It also marks the item stale. The next `Ingest` reloads every stale item before matching, and answers `503` if a reload fails.
- Add tests to Task 6: `TestAnOpeningWhoseLookupFailedIsMatchedNotOpenedAgain` and `TestAnAppendWhoseAnswerWasLostIsReloadedBeforeTheNextMatch`.

### I6. Trigger notices are invisible to every agent; A6's stated cost is wrong (Task 8, A6, D11)

**Failure.** `board/view.go:82`, `shown`, hides every notice whose `Agent` is not the viewing agent. A trigger notice names no agent, so no agent's view or prompt ever holds it. The spec says so too: "An agent sees only its own" (spec 4.2, line 409). D11 and A6 assume agents read trigger updates. A6's "cost if wrong: an agent sees that a correlated alert joined, by its instance" is false under option (a) as Task 8 is written. Only option (c), platform evidence, reaches agents. Task 8's tests check only that the board holds the notice.

**Fix.** Put this to the author inside A6 as a fourth axis. Either:
- (i) amend spec 4.2 so agents see trigger notices, add `internal/work/board/view.go` to Task 8 with `TestAnAgentSeesTriggerNotices`; or
- (ii) take (c); or
- (iii) restate D11 as "for people", with the web view coming in 9a-2.

Each has a different cost if wrong.

### I7. Existing invariants misjudge inbound records (Task 10)

**Failure: invariant 6 gives false positives.** `inFlightFault` (interleave_invariants_test.go:310-321) breaks invariant 6 for:
- every `item_opened` with a `Digest` that is not in `openingsInFlight`. Every inbound opening carries `OpeningDigest`.
- every record `requestHeld` returns whose key is not an act in flight. That covers every firing-resume `run_extended` and every flap `item_linked`.

`requestHeld` (line 1243) is a test copy of `requestIn` and does not get R120's exclusions. 7c-1a's soaks failed three times on oracle faults of exactly this kind.

**Failure: invariant 9 silently loses coverage.** `admittedByTheWalk` and `admission` read the queue exit as `records[1]` (lines 1522-1527, 1577-1580). For an inbound item that is the opening's own `item_triggered`, in the same append, so an inbound item is never judged as admitted by the walk. This is silent: no test fails.

**Fix.** Task 10 lists both changes, each with must-pass rows:
- `requestHeld` skips `Extended.Cause == firing` and `Linked.By` with prefix `source:`, as R120.
- An opening with `Trigger` is exempt from the openings-in-flight check, or the driver notes it in flight.
- The queue exit is the first record after the opening's append that is not an `item_triggered`.

### I8. Three of the ten mutants cannot be caught as written, so "at least eight" is unreachable (Task 10, A3'')

- **Mutant 8 (skip `Seen`; `ErrSeen` as success) is equivalent.** `AppendInbound` and `Put` dedup by the row's key in the same transaction and commit nothing on `ErrSeen` (Task 1). A repeated opening finds its item through `repeated` before `also` runs. No oracle can see a difference.
- **Mutant 9 (unguarded `Put`) is out of the world's reach.** The world runs memory stores, which have no guard (memory/records.go:128). Its guard is the fake port's `w.guard` (interleave_world_test.go:1186-1203). Postgres `Put`'s guard never runs in the world; `TestEveryInboundInsertIsGuardedByTheHolding` is what covers it.
- **Mutant 10 (late applied) is mostly masked by R125.** A late event goes to the item that holds its instance. The fold refuses an applied record not later than that item's last status, so the append fails, no row is written, and invariant 11 has nothing to judge. The event then errors on every retry, which no oracle watches.
- **Mutants 3 and 4 depend on the driver.** Mutant 3 shows only if an event whose `Ingest` erred is sent again. Mutant 4 shows only if events are drawn at exactly the instance's last time. "Repeat" and "late" draws make neither certain.

**Fix.**
- Add a liveness oracle: an `Ingest` sent with no fault armed never returns an error. This catches 10 and any disagreement between the fold and the coordinator.
- Replace 8 and 9 with reachable mutants: the match picks the newest item (17); a matched append skips the advance, so auto-close stays armed while firing (13); the flap ignores `LastItem` (15); `FiringResumeDeclined` is ignored (16).
- Specify that the driver re-sends every event whose `Ingest` erred, as a source does after a `503`, and that it draws some times equal to the instance's last time.

### I9. Oracle 17 is unsound unless the driver's keys follow their instance (Task 10)

**Failure.** Rule 1 matches by instance and, per D5 and R111, adds the event's dedup key to the item. Take an event `(i, d3)` matched to X by `i` while Y, not closed, opened under `d3`. Two open items now share `d3`, with no reopen involved. The coordinator is right by the plan's rules, and oracle 17 breaks. The driver as described ("instances drawn from a small pool so they collide") does not rule this out.

**Fix.** Task 10 states the driver's key model: each `(source, instance)` has one fixed dedup key and one fixed correlation key, as fingerprinting sources do. The alternative is that oracle 17 exempts keys an item took by an instance match. Oracle 14 should also say what it does with items outside the account (see I5).

### I10. Ids and keys are unbounded and unchecked for invisible characters (Task 3)

**Failure.** Only routing values are bounded and checked against `config.Unseen` (coordinator.go:1388-1393). The event id, instance, dedup key and correlation key are rendered from trusted fields of a body of up to 4 MiB with no length or character rule. They then flow into:
- the primary key;
- chained record bodies;
- board notices under A6 (a), which are "read as a platform fact";
- the Warn lines D10 names.

A one-MiB fingerprint, or one holding a line feed or a zero-width space, is accepted as is.

**Fix.** Task 3 step 7: each of the four is 1 to 256 runes with no `config.Unseen` character. Otherwise the event is unroutable: `routing_refused` for the keys, and for the id `id_redacted` (or a new `id_invalid`), stored under `sha256:`. Add a table test.

### I11. Hidden author decision: N1's re-notification retry does nothing for stable-id sources, R130's example included (Constraints carried, R130)

**Failure.** D7 (N1) says a declined firing resume is retried by "the next applied firing … a re-notification included". With `eventId "{fingerprint}:{status}:{startsAt}"`, a re-notification has the same id and is dropped as a repeat (step 1). The plan admits this only in the 7c-2 carry.

The user-visible effect: in an alert storm, a resume declined `limit_reached` leaves the item awaiting its close, its trigger firing and no auto-close, for the alert's whole life, unless a person acts. That is the reverse of what ruling 1's cost line tells the author ("until the next firing").

**Fix.** Move this into "Decisions for the author" as A7, with options and costs:
- accept it;
- retry declined resumes when a slot frees (this partly reverses O1);
- document that sources must put a notification sequence in `eventId`.

## Minor

- **M1 (A3'').** The safety-fault list cites "the C1/C2 class" but includes only C2 (item 5). Add invariant 14 (`resolved_unmatched` while an open item holds the instance firing) and invariant 13 (an automatic close while firing): both strand or lose an incident.
- **M2 (Task 9).** `cmd/agentswarm/serve_integration_test.go:246-260` asserts `"1m27s"`, and Task 9's own integration gate runs it, but the file is not in Task 9's list. The existing table test is `TestTheWorstDrainCountsEachTerm` (serve_test.go:539); renaming it needs the ledger ruling.
- **M3 (Tasks 1, 2).** `TriggerStatus` is declared in Task 1, but Task 2's Interfaces block declares it again. Mark it "exists (Task 1)" so it is not redeclared.
- **M4 (Tasks 3, 9).** The config loader accepts `spec.source` by `objectName` (registry/config/names.go:19), which allows a leading digit. The handler and `Open` require `^[a-z][a-z0-9-]{0,62}$`. A mapping with source `9am` loads, and every request to it is answered 404. Fix: the loader checks `Open`'s form.
- **M5 (R121).** Admin keys are raw, so `ItemID(tenant, "inbound:alerting:x")` is reachable through the admin API (opening.go:19). If such an item exists with the same digest, `openLocked`'s `repeated` returns it without running `also`. `Ingest` would then answer `opened` with no row and no trigger. Fix: the admin API refuses keys prefixed `inbound:`, or a `repeated` hit inside `Ingest` is refused `key_reused`.
- **M6 (Task 6).** `Ending` is built in `ending()` (rounds.go:387-417), not `endRound`. Name the function that sets `TriggerResolved`.
- **M7 (Tasks 2, 10).**
  - The interleaving file reads `AutoCloseAt` four times (lines 1421, 2252, 2263, 2275), not three. At 1421, `ok == false` must itself break invariant 8.
  - Say whether `AppendInbound` spends the records' write fault or `storeInbound`'s: it is one commit, so it should spend one.
  - `readFault` exists (line 885); no change is needed there.
- **M8 (R113).** `web.NewServer` also sets `MaxHeaderBytes`, `IdleTimeout` and `ErrorLog` (web/server.go:137-146). Pin those too, or build only the handler in `internal/inbound` and let serve's closure make the server, which also drops the third copy of 10 s and 30 s.
- **M9 (Size).** 7c-1a cost about $18.1, review included, for about 8.75 raw days: about $2.07 per day. 15 to 16.5 days is then $31–34 before any fix round, and Tasks 6, 7 and 10 each get two reviewers. A guess of $30–40 (stop line $70–80) is more honest; $25 cannot be reached by the plan's own ratio. The calendar guess of 1.5–2 days holds.
- **M10 (D3, Task 9).** The design's "warn once per mapping when its rules make every event of a request unroutable" (N3) has a sentence in Task 9 but no test. Add `TestARequestWhoseEventsTheRulesAllRefuseIsWarnedOnce`.

## Check-by-check notes

1. **Coverage.**
   - Every decision has a task: D1 and D2 in Tasks 4 and 9; D3 in Task 3; D4 in Task 1; D5 in Task 2; D6 in Task 5; D7 and D8 in Tasks 6 and 7; D9 in Tasks 6 and 9; D10 in Tasks 6 and 9; D11 and D13 in Task 8; D12 in Tasks 2 and 6; D14 in Task 10; D15 in Task 11.
   - Every ruling lands, but ruling 9 has no reachable path (I3) and ruling 4's fixtures sit in Task 11's harness.
   - Gaps: N3's Warn has no test (M10). D11's premise that agents read the notice is false against the code and the spec (I6).
   - Nothing excluded crept in.
2. **Executability.** Most tasks are tight. The gaps are I1, I2 and I4 (signatures and error identity missing) and I5 (an unhandled branch). Task 10's driver key model and retry rule are unspecified (I8, I9).
3. **Cross-task consistency.**
   - `openLocked`, `OpenInput` and `Opened.Trigger` do not connect (I1).
   - The record order between R126 and Task 5 is not fixed (I2).
   - `mayFiringResume` and the `no_run` test contradict each other (I3).
   - Task order is buildable: Task 6 wires `Ports.Inbound` into serve before Task 9.
4. **Code claims.** All line references checked are correct to within a few lines. Rulings R109–R131 are sound, except as noted in I3, I4, I5, I10, M5 and the notes below:
   - R110's callers are complete.
   - The board resume cause and the notice-shape restriction are as claimed. But the A6 framing misses `shown` (I6).
   - `worstDrain` (R114) is right.
   - `AppendInbound` inside `Append`'s transaction and guard is feasible.
   - The `openLocked` split is feasible, given I1 and I2.
   - The fold changes are coherent, and R125 is sound. It masks mutant 10 (I8).
5. **Interleaving.**
   - Oracles 11, 12, 13, 15, 16 and 18 are sound as stated.
   - Oracle 17 needs the driver's key model (I9).
   - Oracle 14 is sound, but will rightly catch I5(b) unless it is fixed first.
   - Existing invariant 6 gives false positives and invariant 9 loses coverage (I7).
   - The mutant-to-oracle map fails for 8, 9 and 10 (I8).
   - The 5 h / $4 timebox matches 7c-1a. With I7 and I8 unfixed it fires by construction, so fix them in the plan rather than in a fix round.
6. **Gates, size, spend and calendar.** The gates are right: `make test-linux` covers the Docker path. The spend floor is low (M9). The 1.5–2 day calendar is credible against 7c-1a's 6.75 h for about 9 raw days.
7. **Author-facing decisions.**
   - A3'' is well framed, but needs M1.
   - A6 needs I6's fourth axis.
   - Hidden decisions the author should see: I11 (re-notification); I6 (agents do not see notices); and, smaller, R116 together with ruling 2 means nobody but an admin can act on any alert item before 7c-2, which is already approved but worth one line in the final message.
