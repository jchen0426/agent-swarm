# Plan 7c-1b design review, v1

Reviewed: `plan/2026-11-09-plan-7c1b-inbound-design.md` (Draft v1), against spec Sections 3.6, 4.1, 4.2, 5.1, 5.6, 8.2, 9.3, 9.4, the code at a22c265, and the carried constraints. Read-only review. "Verified" means I read the code or text cited; "unverified" is marked.

Summary: 2 Critical, 9 Important, 10 Minor. Verdict: **revise before the plan.** Both Criticals are in correlation and ordering, the part the design says "cannot be wrong for any source". Two Importants (I2, I3) are feasibility gaps in the firing resume. A simpler shape for the firing resume (O1) removes I2 and most of the size risk.

---

## Critical

### C1. Only the opening event's keys are matched, so an alert correlated into an item can never resolve
- **Section:** D5 (`Opened.Trigger{Source, DedupKey, CorrelationKey}`), D7 step 3.
- **Scenario:** Alert A (`cart:r1`, `correlate.key=cart`) opens item X at t0. Alert B (`cart:r2`) fires at t0+10 min. B matches X by correlation key inside the 30 min window, and instance B is appended firing. B resolves at t0+60 min. Its dedup key `cart:r2` is not X's `DedupKey`, and X's `OpenedAt` is outside `within`, so the event is `resolved_unmatched`. X's fold keeps instance B firing for ever: `TriggerFiring()` stays true, X never auto-closes, and no round-end `trigger_resolved` comes. B's later firings after t0+30 min open new items, because no open item has dedup key `cart:r2`, which duplicates the alert.
- **Evidence:** spec:323: "An item's trigger resolves only when no instance of any alert appended to it is still firing". spec:350 gives an item "dedup and correlation keys", plural. D7 step 3 matches only `Trigger.DedupKey` and `Trigger.CorrelationKey`, which the opening sets once.
- **Fix:** the fold keeps, per item, the set of `(source, dedupKey)` and `(source, instanceId)` of every applied or late `item_triggered`. Match an event when its dedup key or its instance is in an open item's set, ahead of the correlation window. Add an interleaving oracle: no `resolved_unmatched` row for an instance that an open item holds firing.

### C2. Order is kept per item, not per instance, so out-of-order delivery opens zombie items
- **Section:** D5 ("the fold keeps, for each `(source, instanceId)`, the last applied…" on the item), D7 steps 5–6, D8.
- **Scenario (spec 8.2's "arrive out of order" row):** instance I's first firing delivery fails, and the source retries it later. Meanwhile I's resolved event (occurredAt = end time) arrives with no item open, and is recorded `resolved_unmatched`. The retried firing (occurredAt = start time, earlier than the end) then arrives and is unmatched, so D8 opens item Y. Y's only instance is firing, and its resolution was already consumed. So Y never resolves and never auto-closes. Because of D6, only an admin can close it. Y also absorbs, by dedup key, every later alert of that key, so nothing new ever opens for it. The same thing happens after a close: a late older firing for a closed item's instance opens a new "flap" item, and D8 links it.
- **Evidence:** spec:320–322: a status applies "only if its `occurredAt` is later than that of the last status applied for the same instance". The rule is per instance, not per item. D4's table holds `instance_id`/`occurred_at`/`status`, but nothing reads them before applying or opening.
- **Fix:** take the instance's last known `(occurredAt, status)` across the source from the event store: `resolved_unmatched` rows, and applied rows on any item, closed items included. Do this before D7 step 4 and before D8. An event not later than it gets outcome `late` and opens nothing. Add an index `(tenant_id, source, instance_id, occurred_at DESC)`. Add an oracle: no item is opened by a firing older than its instance's last known status.

---

## Important

### I1. A partially committed event is never completed, so the flap link is lost (open point 3 is answered "no")
- **Section:** D7 step 1, D8 "linkFlap's failures", D9.
- **Scenario A:** a firing after a close opens item B. B's append holds `item_opened` and the opening `item_triggered`. Then `linkFlap` fails and the source gets `503`. On the retry, D7 step 1 sees "an active item already holds the event id", answers `repeat`, and writes nothing. D8 is never reached, so the link is never written. `Put` is never written either, so `LastItem` for the next flap still names the older item.
- **Scenario B:** a firing E is appended to item A, and its `Put` fails (`503`). A person then closes A, which leaves the account (queue.go:117–120 `enter`). The retry finds no holder and no match. D8's `LastItem` (no row for E) names A, closed within 72 h, so E opens B and is applied twice.
- **Evidence:** D7 step 1 and D8's retry sentence contradict each other. Two stores, with no shared transaction.
- **Fix:** (a) put `item_linked` in the opening's own append. `record` takes several recs (coordinator.go:1239–1264), and `State.move` accepts a link right after `item_opened` (state.go:236–257), so one append holds opening, trigger and link. (b) Write the `inbound_events` row in the same transaction as the item's append. Alternatively, make step 1's repeat finish the missing tail idempotently (`Put`, link) instead of writing nothing. See O2.

### I2. The owed-resume derivation does not reproduce the live rule (ADR-004)
- **Section:** D7 step 4 "the wait for a slot", D5. This is open point 4.
- **Scenarios where live and rebuilt state differ, or the derivation does something unintended:**
  1. A re-notification: a firing of an instance already firing, with a later `occurredAt`, is applied. The live rule (previous status `resolved` or new) owes nothing. The derivation ("last applied is a firing later than the run's last stop") owes a resume. After a takeover the run resumes.
  2. Close, then reopen: a firing arrives while the item awaits its close, and a person closes the item before admission. A reopen within 24 h brings the item back to `awaiting_close`, and the derivation still holds, so the run resumes with no new firing.
  3. Kill switch: D7 says "while the kill switch is on, there is no resume". The derivation has no kill-switch term. A run killed by the on, with a firing while it was on, resumes when the switch turns off. The off does not otherwise restaff killed runs.
  4. `mayResume`'s other refusals (extend.go:180–196): daily cap, no agents left, would stop at once. These are not in the derivation. An item with no agents left stays "owed" for ever, at the head of the shared order (oldest firing), and is retried at every walk. Invariant 9 sees it as an older waiter that was skipped.
  5. `mayResume` uses `overLimit` (limit + 1, queue.go:208–211), but the firing resume waits for a free slot (`< Limit`). These are two different limits.
- **Fix:** record the decision instead of deriving it. For example, `item_triggered.Owes bool`, set at ingest by the live rule. A later record (the `run_extended{Cause: firing}`, or a new `Declined` field) discharges it. Then every refusal is a record, and a takeover reads the same thing. Better still, see O1.

### I3. A firing resume cannot be written as an `Extended` with no amount
- **Section:** D5 `Extended.Cause`, D7.
- **Evidence (verified):** `State.extend` refuses `a == (Budget{})` with "an extension adds at least zero of each dimension, and more of one" (internal/work/item/state.go:216–221). `Extend` calls `May(a.Actor, ActExtend, …)` first (extend.go:79), which refuses a source actor (policy.go:70–79). The fold's resume also expects a `Request` whose `Key` is a hash (item.go:439–445).
- **Fix:** name the fold change: `Cause == "firing"` admits a zero amount, and only on a stopped run. Call `extendNow` from a new coordinator path, not `Extend`. Define the firing resume's `Request` (`sha256` of `firing:<eventId>`). Make `extendedBudget`/`findKey` ignore firing resumes.

### I4. `Open`'s refusals have no outcome, so one poison event blocks or loses its whole group
- **Section:** D8 "the opening", D9.
- **Scenario:** the mapping's `workItemType` has a key that reads its values at open. `runnable` refuses it with `ErrInvalid` (coordinator.go:1292–1303), which is common until 7c-2. Other refusals are `ErrKeyReused`, `ErrSkipped`, or a principal/responder check. D9 lists only `503` and `200`. If such a refusal is treated as `503`, the event comes back on every retry, and D9 stops the request at it, so every later alert in the group is blocked for ever. If it becomes `4xx`, the source drops the whole group, including the events already committed and those after.
- **Fix:** map every non-transient refusal to the outcome `refused` (it is already in the CHECK and otherwise undefined) with a reason code, continue with the next event, and answer `200`. Only `ErrNotHeld`, draining, store failures and unknown outcomes get `503`. Add a scenario with one poison event mid-group.

### I5. The loader makes every mapping declare principal and responders identity reads, and the design never says so
- **Section:** D3, D6, D13 (scenarios 29–32), Non-goals.
- **Evidence (verified):** `checkIdentityRead` adds "is required" when `principal`/`responders` is nil, and requires a registered identity source or a read tool (internal/registry/config/mapping.go:93–94, 181–196). So every unit, e2e and interleaving fixture mapping must register an `IdentitySource` and a read tool that 7c-1b never calls. `fallbackPrincipal` and `checks` (diagnosis-only, spec:326) are accepted and then silently ignored, so an alert the mapping marks diagnosis-only opens a full investigation.
- **Fix:** say it, and choose one: make the two reads optional until 7c-2, which is a loader change and a Plan 4 contract note; or keep them required, with a fixture identity source. Log at load time a Warn that `principal`, `responders`, `fallbackPrincipal` and `checks` are not applied until 7c-2. Either refuse a mapping with `checks` now, or document that it opens normally.

### I6. Trusted values are stored unredacted, including the exact values that redaction refused
- **Section:** D4 columns, D3 "Routing", D10.
- **Scenario:** a trusted `labels.service` holds `password=hunter2`. `checkRouting` refuses it because the always-rules would change it (coordinator.go:1379ff). The event is `unroutable/routing_refused`, and `Put` stores `dedup_key`/`correlation_key`/`instance_id` holding `password=hunter2` in plain text.
- **Evidence:** spec:758: everything that reaches "a store", "inbound event content" included, first passes the redaction rules. D3 redacts only `title`, `labels` and `payload`. The order of the 16 KiB cut and redaction is unspecified, and cutting first can split a match.
- **Fix:** pass every stored string column through the same redactor, and store `[withheld]` on an escape. Leave out the key columns for an event whose routing redaction refused them, keeping only the event id and reason. Redact first, then cut.

### I7. Trigger updates never reach the board
- **Section:** Goals, D5, D7. The non-goal excludes only the web's words (9a-2).
- **Evidence:** spec:388: board entry `notice` is "Trigger updates, …". spec:323 and 482: the event "is appended to that item as trigger evidence". The opening claim is the only trigger content that reaches the board today (rounds.go:35, coordinator.go:1492). Agents investigating X never learn that a correlated alert joined, that the trigger resolved, or that a late event arrived.
- **Fix:** post a platform `notice` for each `item_triggered`: applied, late, and the resolution. Use the redacted title and the trusted fields, on the visible run's board. Shadow runs are excluded per spec:637, which is 8a's concern.

### I8. Resolution as a standing condition cuts every later person's extension to one round
- **Section:** D11 (`TriggerResolved = HasTrigger && !TriggerFiring`).
- **Scenario:** the alert resolves and the run stops `trigger_resolved`. A responder extends the run to finish root cause, using 7c-1a's `Extend` on a stopped run. `mayResume` passes, the run resumes, and at the end of its first round `TriggerResolved` is still true, so it stops `trigger_resolved` again. The same happens to a queued item whose trigger resolved before it started.
- **Evidence:** spec:572: "stops at the end of the **current** round". The decision order at spec:575. `mayResume` already refuses a resume that "would stop at once" (`CodeWouldStopAtOnce`, extend.go:192), which is the analogous rule.
- **Fix:** count the resolution only when its record is later than the run's latest start or resume. Or make `mayResume` refuse with a new code, so the person is told. Record the choice as a spec 5.6 clarification.

### I9. Carried constraints dropped without a word
- **Evidence (verified):**
  - 7c-1a "Constraints carried" (plan/2026-11-08-plan-7c1a-run-return-and-incident.md, the leader bullet and the next one) assigns to "**Plan 7c-1b**, or a plan that touches the leader" two items: the concurrent soak's liveness failure (`no leader holds the lease after 2h0m0s`), and epoch-scoped `Release`. D13 gates on the same 40-minute soak, so the residual can fail the gate.
  - 7b-1:1064: "Plan 5b:14780: refuse a write's scope binding that resolves to `other` … with a test". `scope()` is unchanged (internal/actions/app/write.go:276–289).
  - 7c-1a's interleaving carry: a walk admission-order hook "would replace" invariant 9's three excuses. D12 extends invariant 9 again without it.
- **Fix:** list each one as taken (size it) or carried on, with a row. I recommend building the walk's admission hook first, if the shared order survives O1.

---

## Minor

- **M1. Items that are not closed but are missing from the account produce duplicates.** This answers open point 2. Items whose records do not fold (`ErrInvalid`) and items a later build holds leave the account (queue.go:131–137, 117–120). An event for such an item finds no match, and D8's `LastItem` names an item that is *not closed*. D8 then opens a fresh item with no link: two open items for one dedup key. **Fix:** when `LastItem` names a non-closed item outside the account, answer `503`, or `refused` with reason `item_unreadable`. Do not open.
- **M2. Transient failures get permanent answers, and oversize groups are lost.** "No current revision" gets `404`, and a secret-store read failure is unspecified (it probably falls into `401`). Sources do not retry `4xx`. They should get `503`. More than 100 events, or more than 1 MiB, gets `413` "with nothing recorded", which loses a large outage group exactly when it matters. **Fix:** `503` for anything transient. For oversize, record one `refused` row and warn, or raise the caps. `bearer` returning `501` makes a source retry for ever, so use `4xx`.
- **M3. The derived `eventId` hashes the clamped `occurredAt`.** A future-dated event gets a different id on each retry, because the clamp moves with the receipt time. **Fix:** hash the parsed, unclamped value.
- **M4. The oracle "a dedup key never has two open items" has a false positive.** A person reopens closed item A while flap item B (same key) is open. D7 must say which one matches (the older, i.e. A?), and the oracle must exempt reopen.
- **M5. A firing on an `awaiting_close` item with no run has no rule.** Such items are opened not staffed (kill switch, daily cap), or not staffed at start (`routing`/`type`, coordinator.go:1053–1066). `mayExtend` refuses `CodeNotStarted`, so the item stays firing and unstaffed. Spec 8.2 says "back to investigating". **Fix:** decide: a start (promotion-like), or nothing, and record why.
- **M6. Auto-close while firing should be refused in the fold.** `closeItem` (state.go:200–209) should refuse `Auto` while `TriggerFiring()`, which makes D12's oracle structural. `AutoCloseAt` needs a "never" result, and every `item_triggered` must re-arm or cancel the timer (`armAutoClose`). The design does not mention either.
- **M7. Routing keys are field paths.** `Event.Routing` sends every trusted field, keyed `labels.service` and so on. `views.go:369` reads `Routing["service"]`, so the items list shows no service for inbound items. Time and status fields enter the routing and the opening claim. A mapping with more than 16 trusted fields makes every event `routing_refused`, and the loader does not cap them. The `labels` member name is a payload-shape assumption (Plan 4:174 allows new mapping fields). **Fix:** an optional mapping `routing: {key: field}` subset, a loader cap of 16, and a `labels` field path.
- **M8. `linkFlap`'s `Request.Key: "flap:"+eventID` breaks `Request`'s "sha256:<hex>" contract** (item.go:439–445). Hash it, as with I3's firing request.
- **M9. The 72 h flap bound contradicts spec 8.2**, "After the item has closed, a new item is opened and linked to the previous one", which has no bound. It is labelled the designer's call but is not listed as a spec amendment. Either list it, or link unconditionally. `LastItem` is bounded by the 30-day retention anyway.
- **M10. Small inaccuracies.** D6: item acts refuse with `CodeNotItemResponder`, not `CodeResponderOnly` (policy.go:70–79). D7: `Ingest` "under the coordinator's lock" cannot call `Open`, which takes `c.mu` itself (coordinator.go:312–314). An internal `openLocked` is needed, otherwise two concurrent requests with the same dedup key both miss and both open. The walk ranks queued items by the opening record's time, not "when it was queued" (queue.go:216–231).

---

## Size

The guess is 13.5–16 raw days (2.7–3.2 pw). At 7c-1a's ratio as executed (about 1.3×), that is about 3.5–4.2 pw executed, at or above the row's top. The riskiest tasks are 7 (owed resume plus the shared walk order) and 10 (oracles and mutation). That is the same shape as 7c-1a's fix rounds, and they are budgeted at the low end. Fixing C1, C2, I1, I3, I4 and I6 adds about 2–3 raw days. Take O1 to pay for it. With O1 the guess stays credible. Without it, expect the upper end or more.

## Out-of-frame suggestions

- **O1. Drop the slot wait for a firing resume.** Resume at once under the person's resume rules: `mayResume`, with limit + 1. If it is refused (kill switch, cap, no agents, no budget, over the limit), record `item_triggered{Resume: "declined:<code>"}`, and the next firing tries again. Spec 8.2 requires only "back to investigating with the remaining budget". The wait is "the controller's call" (7b-1 carry), so the author can reverse it. This removes the owed derivation, the walk merge and the invariant-9 extension (I2 entirely), and about 2–3 raw days.
- **O2. Use one transaction per event.** Insert the `inbound_events` row in the same database transaction as the item's append (an optional side-insert in `Records.Append`), and put opening, trigger and link in one append. D9's partial-commit sequences (I1) disappear, and the event store becomes the sole dedup, using `Seen` by primary key.
- **O3. Keep instance state at the source level, in the event store**, keyed `(source, instance_id)`. One lookup serves C2's ordering and C1's match by instance.
- **O4. Match by keys appended to the item, not by the opening's keys** (C1's fix). It also gives spec 4.1's "dedup and correlation keys" a real meaning.
