# Plan 7c-1b design: inbound events

| | |
|---|---|
| Status | Approved by the author at v2.1 on 2026-11-09. v2 answered the Opus review v1 (plan/2026-11-09-plan-7c1b-design-review-v1.md: 2 Critical, 9 Important, 10 Minor). v2.1 answers its scoped re-review (plan/2026-11-09-plan-7c1b-design-rereview-v2.md: 17 addressed, 4 new Important N1–N4, 2 new Minor N5–N6). v1 and v2 are in plan/backups/plan7c1b/ |
| Roadmap row | 7c-1b, Inbound events (plan/2026-10-12-implementation-roadmap.md) |
| Spec | plan/2026-10-11-agent-swarm-platform-spec.md, Sections 3.6, 4.1, 4.2, 5.1, 5.6, 8.2, 9.3, 9.4 |
| Depends on | Plan 7c-1a (executed, a22c265) |
| The author's decision (2026-11-09) | The protocol is a generic webhook: a JSON body signed with HMAC, mapped by the existing `TriggerMapping` kind. No vendor format. Format-independent rules come first. |

## 1. Intent

Before this plan, items open only by hand. Plan 7c-1b lets an alerting source open them. Repeats, late events, resolutions and flaps then behave as spec Section 3.6 says. The source's payload is a guess (ADR-016, question 3). The guess is kept in configuration and in one small adapter, the signature check.

**Goals**
- A fifth listener for signed requests.
- An evaluator for `TriggerMapping`.
- An inbound event store that holds the source-level truth: what each event was, and each instance's last known status.
- Correlation into open items, by every key an item has taken.
- A trigger's resolution as records.
- Flaps:
  - a firing for an item awaiting its close resumes its run at once;
  - a firing after a close opens a new item, linked in the same append.
- Trigger updates posted to the item's board.
- Openings through `Open`, under a source actor.

**Non-goals**
- Principal, responders and key values read at open; diagnosis-only checks; milestones; incident references (7c-2).
- Notifications to a mapping's owner (9b).
- The web's words for trigger states (9a-2).
- Retention of the event store (10a).
- The bearer and client-certificate schemes (onboarding build).
- The leader's liveness residual (10a, see Section 4).

**Anti-goals**
- Any vendor name or payload shape in platform code.
- A routing value from an untrusted field.
- A source retry that double-applies an event.
- A state that a takeover rebuilds differently from the records.
- One bad event that blocks or loses its group.

## 2. Decisions

### D1. The listener

- **Deployment.** An optional `listeners.inbound` key, `host:port`, on loopback only, as `web` is. It sits behind the deployment's proxy, which terminates TLS.
- **Lifecycle.** With no key, no listener exists. The addresses file gains `inbound`, omitted when empty (Contract C2).
- **Timeouts and drain.** The server uses serve's `server()` helper, with the fixed 10 s and 30 s. It closes with the other listeners. From the fence on, it answers `503 draining`.
- **Routes.** `POST /inbound/{source}` and `GET /healthz`.
- **Limits, fixed in code, as spec amendments:**
  - a body of at most [4 MiB];
  - at most [500] events after the split;
  - each stored payload cut to [16 KiB], after redaction.

  Over either of the first two limits, the answer is `413`, with a Warn log line naming the source and the count. Over the event limit, an authenticated request also records one `refused` row, keyed by `sha256` of the body with reason `too_many_events`, so the loss leaves evidence (re-review N6). A body over 4 MiB is never read whole, and gets only the log line.

### D2. Authentication

- **Schemes.** HMAC-SHA256 only. A `bearer` mapping gets `400 scheme_unsupported` and a Warn log line. A `4xx`, so the source does not retry. The loader still accepts bearer.
- **Signed string and headers.** The signed string is `<timestamp> "." <raw body>`, with two headers:
  - `X-Agentswarm-Timestamp`: decimal Unix seconds;
  - `X-Agentswarm-Signature`: one or more `v1=<lowercase hex>`, comma-separated.
- **Acceptance.** Any listed signature may match any current secret, compared in constant time.
- **Timestamp window.** A timestamp more than `maxAge` [5 min] from the instance's estimate of database time, either way, gets `401 stale`.
- **Rollover.** The secret file holds one or two non-empty lines, each valid (spec 9.3). The file is read at each request.
- **Answers:**
  - an unknown source gets `404`;
  - a bad signature gets `401`;
  - a secret-store read failure, or no current revision, gets `503`, since both are transient;
  - nothing is recorded for these, and no log line echoes the body.

### D3. The mapping additions and the evaluator

**Config, per Plan 4:174, which lets Plan 7 add mapping fields:**
- `routing: { <key>: "<trusted field path>" }`, optional, at most 16 entries:
  - each key is checked like a routing key (1–64 runes);
  - each value is a trusted field path;
  - absent means no routing.

  This replaces v1's "every trusted field". So the items list's `service` works when a mapping names it.
- `principal` and `responders` stay required, as the loader has them. Fixtures register an identity source. At load, serve logs once per mapping a Warn: `principal`, `responders` and `fallbackPrincipal` are not applied until Plan 7c-2.
- **A mapping with `checks`.** Its events are refused with the outcome `refused`, reason `checks_unsupported`, until 7c-2. A mapping that marks alerts diagnosis-only never opens a full investigation by mistake. **Cost if wrong:** such a source opens nothing until 7c-2.

**The evaluator.** A pure package, `internal/inbound/mapping`, with `Evaluate(m, body, received, r *redact.Redactor) ([]Event, error)`.
- **Parsing and the split.** JSON with `UseNumber`, split by `split`, or one event.
- **Trusted values.** Trusted values come only from `trustedFields`. A string is taken as is; a number or boolean as its JSON text. Anything else is absent.
- **Template fields.** `eventId`, `instanceId`, `dedupKey` and `correlate.key` are rendered over trusted values.
- **`status`.** Mapped through the status lists.
- **`occurredAt`.** Parsed as RFC 3339, and kept twice: `Occurred` is the parsed value, and `Clamped` is the parsed value clamped to the receipt time plus [2 min].
- **An absent `eventId`.** The lowercase hex of SHA-256 over the source, the trusted name and value pairs sorted, the status and `Occurred` (unclamped, so a retry keeps the id), joined with NUL.
- **The title.** Rendered, then line breaks and tabs are folded to spaces (7b-1's carry). `Open`'s `title()` still applies.
- **The event id is never redacted** (re-review N3), since it is the dedup key. An event whose id the rules would change is `Unroutable` with `id_redacted`, and is stored under `sha256:` of the raw id, with no other columns. Serve warns once per mapping when its rules turn every event of a request unroutable.
- **Redaction.** Every other string that will be stored goes through the always-rules plus the mapping's `redact` rules, redacted first and cut after:
  - the instance, keys, title and payload (the whole element);
  - an escape that could hide a match stores `[withheld]` (spec 9.4).
- **Unroutable.** An event missing `status`, `occurredAt`, `instanceId` or `dedupKey`, a declared `correlate.key`, or a declared routing entry is marked `Unroutable` with the field's name. If redaction changes a key, an instance or a routing value, the event is `Unroutable` with `routing_refused`, and only its event id and reason are stored (no key columns).

### D4. The inbound event store

- **Migration 0012, at its own level.** The table `inbound_events`, with the primary key `(tenant_id, source, event_id)`:

  ```
  received_at, instance_id, status, occurred_at (clamped), dedup_key,
  correlation_key, item_id NULL, applied bool,
  outcome CHECK IN ('opened','appended','late','resolved_unmatched','unroutable','refused'),
  reason, title, payload
  ```

  Two indexes:
  - `(tenant_id, source, instance_id, occurred_at DESC)`: an instance's last known status;
  - `(tenant_id, source, dedup_key, received_at DESC)`: the last item a key took.
- **Protection.** The rows are append-only, through the guard triggers, and not hash-chained. They are evidence in the "event logs" class [30 days]. The work records that change items are chained.
- **The source-level truth (review O3).** An instance's last known status is its latest row with `applied` set: `opened`, `appended` or `resolved_unmatched`, closed items included.
- **One transaction (review O2).**
  - An event that touches an item is written in the same database transaction as the item's records, through `Records.AppendInbound(ctx, id, decide)`, whose `decide` returns the drafts and the event row.
  - An event that touches no item is a lone `Put`.
  - So the store and the records never disagree, and the event store's key is the only dedup.
- **The holding's guard (re-review N4).** Every insert into `inbound_events`, the lone `Put` included, runs under the same `Guard` as `Records.Guarded` (internal/work/postgres/records.go:39-46), for the epoch the context carries. An instance whose lease lapsed writes no source-level truth.
- **The port `app.Inbound`:**
  - `Seen(source, eventID) (bool, error)`;
  - `Put(*InboundEvent) error`;
  - `LastStatus(source, instanceID) (Status, time.Time, bool, error)`;
  - `LastItem(source, dedupKey) (item.ID, bool, error)`.

  The postgres adapter and the memory adapter both have it.

### D5. Records

Under 0012's own level:

- **`item_triggered`.** Admitted in `queued`, `investigating` and `awaiting_close`. Its body:

  ```
  {Source, EventID, InstanceID, DedupKey, CorrelationKey, Status,
   OccurredAt, Applied, Resume}
  ```

  `Resume` is `""`, `resumed` or `declined:<code>` (D7).
  - **What the fold keeps.**
    - The set of every `(source, dedupKey)` and `(source, instanceId)` the item has taken, applied or late (review O4).
    - For each instance, its last applied `(occurredAt, status)`.
- **`Opened.Trigger`.** `*Trigger{Source, DedupKey, CorrelationKey}`, optional. The opening's `item_triggered`, and a flap's `item_linked`, follow in the same append. `State.move` already accepts a link right after `item_opened` (state.go:236–257).
- **`State.TriggerFiring()`.** True when any instance's last applied status is `firing`.
- **`State.ResolvedAt()`.**
  - An item with no `item_triggered` was resolved at its opening.
  - While any instance fires, the trigger is unresolved, and `AutoCloseAt` returns "never".
  - Otherwise, it is the record time of the event that resolved the last firing instance.

  Every `item_triggered` re-arms or cancels the auto-close timer (`armAutoClose`).
- **The fold refuses an automatic `item_closed` while `TriggerFiring()`** (review M6). An automatic close cannot happen while an alert fires, by construction.
- **`Extended.Cause`.** `""` or `firing`. On a stopped run, `Cause: firing` admits a zero amount; the fold today refuses an extension with no amount (state.go:216–221). Its `Request` is `sha256:` of `firing:<eventId>`. `extendedBudget` and `findKey` skip firing resumes.

### D6. The source actor

- **The actor.** `Actor.Source bool`. Inbound openings pass `Actor{Subject: "source:<name>", Source: true}`.
- **`May`.** `May(ActOpen)` admits it. Item acts refuse it with `CodeNotItemResponder`, as they refuse any non-member.
- **`checkOpener`.** It exempts a source from the own-principal rule, and requires no principal and no responders until 7c-2.
- **Designer's call.** Before 7c-2, only an admin acts on an inbound item. Widening item acts to every responder would change authority. **Cost if wrong:** a responder on call cannot act on alert items until 7c-2.
- **The firing resume** (D7) and the flap link (D8) are platform acts. They are written by coordinator paths that never call `May`.

### D7. `Ingest`: one event

`Coordinator.Ingest(ctx, ev)` takes the lock and holds it through the whole event. `Open`'s body is split into `openLocked`, which `Open` and `Ingest` both call, so two requests with one dedup key cannot both open (review M10). The steps:

1. **Repeat.** If `Seen`, the outcome is `repeat`, and nothing is written. One transaction per event (D4) makes this safe.
2. **Unroutable or refused by the mapping.** The event is `Put` with its outcome and reason.
3. **Order at the source level (review C2).** `LastStatus(source, instance)` is read. If the event's `Clamped` is not later than the instance's last known time (at equal time, `resolved` wins), the event is late:
   - it is appended to the item holding that instance, if one is open, as `Applied: false`;
   - otherwise it is `Put` as `late`;
   - it opens nothing, ever. A retried old firing cannot open a zombie item.
4. **Match.** Among the items in the account that are not closed, matching goes in this order:
   - an item that holds the event's instance;
   - then the event's dedup key;
   - then the correlation key, with `OpenedAt` within `within` [30 min];
   - ties go to the oldest.
   
   A person-opened item has no keys until one is appended.
5. **Matched.** `item_triggered` is appended with the row, in one transaction.
   - **A firing resume.** Suppose the event is an applied `firing`, the item is `awaiting_close` with a stopped visible run, and either the instance was `resolved` or new to the item, or the latest firing resume since the run's stop was declined (re-review N1). Then the run is resumed at once, in the same append, through `mayResume`'s rules with the remaining budget, writing `run_extended{Cause: firing}` and `Resume: resumed`.
     - **A refusal.** The refusal codes are: kill switch on, daily cap, no agents, would stop at once, over the limit, no budget left, and no run (review M5). Each is recorded as `Resume: declined:<code>`. The item stays awaiting its close with its trigger firing, so it does not auto-close. The next applied firing of any of its instances, a re-notification included, tries again.
     - **An unsettled kill-switch view.** The resume is declined with `unsettled`, never left pending (R37).
   - **No slot wait (review O1).** This reverses 7b-1's carried "a firing resume waits for a free slot", which was the controller's call. There is no owed state to derive, no change to the walk's order, and no new invariant 9 excuse. Spec 8.2 asks only "back to investigating with the remaining budget". **Cost if wrong:** a firing that comes while the platform is full resumes nothing until the next firing or a person's extension.
   - **An unknown outcome.** It counts the item as starting (`countAsStarting`), as 7b-2 carries.
6. **Unmatched, resolved.** `Put` as `resolved_unmatched`, applied, which the instance's later events order against.
7. **Unmatched, firing.** This goes to D8.
8. **An item outside the account** (review M1). If `LastItem` names an item that is not closed but is outside the account (does not fold, or is held by a later build), the event is `refused` with reason `item_unreadable`, with a Warn log line. It does not open a duplicate.

### D8. A firing after a close, and the opening

- **The flap lookup.** `LastItem(source, dedupKey)` gives the last item that took the key. If that item is closed, the new item's append holds `item_opened`, its `item_triggered` and `item_linked` into the previous item's incident, in one transaction with the event row (review I1).
- **No time bound.** The flap link has none, as spec 8.2 says (review M9). The event store's 30-day retention bounds it in practice.
- **The link record.** `item_linked{By: "source:<name>", Reason: nil, Request: sha256:"flap:"+eventId}`. It skips `May`, the preset reason and R64's window. 7b-2 v3 left that choice to 7c-1, and spec 8.2's flap row has no window.
- **The opening.** `openLocked` with:
  - `ID: ItemID(tenant, "inbound:"+source+":"+eventId)`;
  - `Digest` over the trusted fields;
  - the type, title, routing and `Trigger`, under the source actor.

  `Open`'s queue, kill switch and daily cap apply unchanged.
- **Refusals (review I4).** `runnable` (a key read at open), `ErrKeyReused`, `ErrSkipped`, and the actor checks each give the outcome `refused` with a reason code, and the next event is taken.

### D9. Acknowledgement

- **One request.** Its events are taken in order.
- **`200`.** It comes when every event has an outcome, with a body of counts only.
- **`503`.** `ErrNotHeld`, draining, a store failure or an unknown outcome stops the request with `Retry-After: 1`. On the retry, committed events are repeats.
- **A non-leader instance** answers `503 not_held`. The deployment docs say a source behind a proxy retries.

### D10. Unroutable and refused events

- **Recording.** They are recorded with reason codes:
  - `missing:<field name>`, `routing_refused`, `status_unknown`, `time_unparsed`;
  - `checks_unsupported`, `type_not_runnable`, `item_unreadable`, `key_reused`, `skipped`.
- **The owner.** "The mapping's owner is notified" is a Warn log line naming the mapping, its `metadata.owner`, the event id and the reason. A real notification is carried to 9b.

### D11. The board

- **What is posted.** Each `item_triggered` posts a platform `notice` on the item's visible run board: applied, late or resolution. It carries the redacted title, the status, the instance and the time (spec 4.2, `notice`: "Trigger updates").
- **Only to a running board, after the commit, best effort (re-review N2).** A stopped board refuses everything but rulings (`board.ErrStopped`), and the board is a different store from `AppendInbound`.
  - For a firing resume, the notice follows the board's `resume` entry.
  - A failed post is a Warn log line. It does not hold the event or its group.
  - The item's records stay the evidence of record.
- **No board.** An item with no run, or a stopped one, posts nothing. Shadow boards are 8a's concern.
- **Cost if wrong:** an agent misses a trigger update when the board store fails at that moment.

### D12. A trigger's resolution at round end

- **When it counts.** `Evidence.TriggerResolved` is true when the trigger is resolved and the record that resolved it is later than the run's latest start or resume (review I8). A person's extension after a resolution then runs as asked, and a run started after the resolution is not cut to one round.
- **Order.** It is decided after the daily cap and before `not_staffed` (spec 5.6). It comes from records only (ADR-004).
- **A spec 5.6 clarification.** "The current round" means the round during which the resolution was recorded.

### D13. Taken from the carries

- **Plan 5b:14780, 7b-1's carry.** `scope()` in `internal/actions/app/write.go` refuses a binding that resolves to `other`, with the test `write_test.go` avoids. This is small, and it is in 7c-1b's row by inheritance.

### D14. The interleaving test

- **Driver events:** inbound firing, resolved, repeat, late (a retried old firing among them), correlation by a second alert, and a flap after a close.
- **Oracles:**
  - an instance's last applied time never decreases, at the source level;
  - no item is opened by an event not later than its instance's last known status;
  - an item whose trigger fires never closes automatically;
  - no `resolved_unmatched` for an instance an open item holds firing;
  - every flap item's opening append holds its link;
  - a firing resume is resumed or declined, and never pending;
  - two open items share a dedup key only when a person's reopen made it so (review M4).
- **A mutation check, before the soak.** At least eight planted faults, each caught. The planted faults include C1 (match the opening's keys only), C2 (order per item), and a split transaction.
- **Timebox, as A3':** land what holds, record residuals.

### D15. Scenarios and gates

- **New e2e scenarios.**
  - 29: a signed group opens one item. A second alert correlates into it. A repeat is dropped. A late event changes nothing. One poison event in mid-group is refused, and the rest are taken.
  - 30: the resolution of both alerts ends the run at round end, and the item auto-closes. A person's extension after that runs.
  - 31: a firing while awaiting the close resumes the run. A firing after the close opens a new item linked into the same incident.
  - 32: a bad signature, a stale timestamp, an unknown source and a bearer mapping are refused.
- **Gates.** `make test-linux`, `make test-docker`, the mutation check, and the 40-minute soak before and after the final review.

## 3. Rulings this version makes (cost if wrong)

1. **O1: the firing resume does not wait for a slot**, reversing 7b-1's carried controller's call. *Cost:* a firing while full resumes nothing until the next one.
2. **Only admins act on inbound items before 7c-2.** *Cost:* responders wait for 7c-2.
3. **A mapping with `checks` is refused until 7c-2.** *Cost:* such a source opens nothing.
4. **`principal` and `responders` stay required in the loader**, with fixtures, and are not applied. *Cost:* fixture weight.
5. **The flap link has no time bound**, as spec 8.2 has none. *Cost:* a monthly recurrence joins the old incident.
6. **Events that touch items share one transaction with the event row**, through `AppendInbound`. *Cost:* a port wider by one method.
7. **The event store is not hash-chained.** *Cost:* evidence rows that can be deleted, though not edited, by a database role. The chained work records carry what changed.
8. **The leader's liveness residual goes to 10a**, as told to the author on 2026-11-09 without objection. If the soak hits it, it is recorded under A3', and it stops nothing.
9. **A firing for an awaiting-close item with no run is declined (`no_run`)**, though spec 8.2 says "back to investigating". Such an item was opened not staffed, and a person's promotion starts it. *Cost:* a firing does not staff an item the kill switch or the cap left unstaffed.
10. **A queued item whose trigger resolved before it started runs its full budget** (D12, an amendment to spec 5.6). *Cost:* tokens spent on an alert already gone, bounded by the budget.
11. **Board notices are best effort, after the commit**, to running boards only. *Cost:* a missed update when the board store fails.
12. **The event id is never redacted.** An id the rules would change makes the event unroutable. *Cost:* a source whose ids look like secrets opens nothing until its mapping or the rules change.

## 4. Carried on

- **To 10a:** the leader's liveness residual and the epoch-scoped `Release` (7c-1a's carry).
- **To the plan that next extends invariant 9:** the walk's admission-order hook. O1 adds no walk change.
- **To 9b:** the owner's notification.
- **To 7c-2:** principal, responders, `fallbackPrincipal`, `checks`, milestones and `incidentRef`.
- **To the onboarding build:** bearer and client certificates.

## 5. Size (recorded only; the trigger is calendar and spend, R-f)

| # | Task | Raw days |
|---|---|---|
| 1 | Migration 0012, the event store, `AppendInbound` | 1.25 |
| 2 | Records: `item_triggered`, `Opened.Trigger`, `Extended.Cause`, the fold's keys, instances, `ResolvedAt` and the refusal of an automatic close while firing | 1.75 |
| 3 | The mapping additions (`routing`), the evaluator and its redaction | 1.75 |
| 4 | HMAC authentication and rollover | 0.75 |
| 5 | The source actor, `openLocked` and the policy | 0.75 |
| 6 | `Ingest`: order, match, append, unmatched, refused, and the resolution at round end | 2 |
| 7 | The firing resume, and a flap after a close with its link | 1.25 |
| 8 | Board notices, and `scope()` refusing `other` | 0.75 |
| 9 | The listener, the handler, the deployment and serve | 1.25 |
| 10 | The interleaving extension and the mutation check | 1.5–2.5 |
| 11 | e2e scenarios 29–32 and the docs | 1.5 |

- **Total:** about 15–16.5 raw days, with v2.1's fixes and task 10's spread: about 3.0–3.3 person-weeks raw, inside the row's 2.7–3.65. At 7c-1a's ratio, about 3.9–4.3 executed.
- **Calendar guess:** about 1.5–2 days. The stop line is 5 calendar days.
- **Spend guess:** about $25–35. The stop line is $70.
