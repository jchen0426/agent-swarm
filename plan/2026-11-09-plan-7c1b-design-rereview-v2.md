# Plan 7c-1b design: scoped re-review of v2

This re-reviews `plan/2026-11-09-plan-7c1b-inbound-design.md` (Draft v2) against review v1. It was read-only: no repo edits and no git changes.

**Verdict:** v1's two Criticals are fixed, and so is almost every other finding. v2 introduces **4 new Important** issues and **2 new Minor** ones, and leaves 2 v1 findings partly open (I7, M2). N2 and N3 are cheap to fix and should be fixed before the plan. N1 and N4 are one paragraph each.

## v1 findings

| # | Status | Where in v2 |
|---|---|---|
| C1 | Addressed | D5: the fold keeps every key and instance the item has taken. D7 step 4 matches the instance first, then the dedup key, then the correlation key. |
| C2 | Addressed | D4 `LastStatus` keeps order at the source level. D7 step 3 records a late event and opens nothing. |
| I1 | Addressed | D4/O2: `AppendInbound` writes the item's records and the event row in one transaction. D8 puts the opening, the trigger record and the link in one append. |
| I2 | Addressed (removed) | D7/O1: a firing resume happens at once or is declined. No owed state is derived. |
| I3 | Addressed | D5: `Cause: firing` admits a zero amount and has a hashed `Request`. D6: the resume path never calls `May`. |
| I4 | Addressed | D8 refusals and D10 reason codes: a refused event gets the outcome `refused`, and the next event is taken. |
| I5 | Addressed | D3: the reads stay required, fixtures register an identity source, load logs a Warn, and a mapping with `checks` is refused. |
| I6 | Addressed, but see N3 | D3 redacts every stored string, redacting first and cutting after. |
| I7 | Partially | D11 posts notices, but the boards they target refuse them, and they are written outside the transaction. See N2. |
| I8 | Addressed | D12: a resolution counts only when recorded after the run's latest start or resume. |
| I9 | Addressed | D13 takes 5b:14780. Section 4 carries the liveness residual and `Release` to 10a, and the invariant-9 hook onward. |
| M1 | Addressed | D7 step 8: an event for an item outside the account is refused with `item_unreadable`. |
| M2 | Partially | D2 now answers `503` for transient failures. D1's `413` still records nothing, and a source does not retry a `4xx`. See N6. |
| M3 | Addressed | D3 hashes the unclamped `Occurred`. |
| M4 | Addressed | D14's oracle exempts a person's reopen. Ties go to the oldest item. |
| M5 | Addressed as a ruling | D7: a firing on an item with no run is declined as `no_run`. Spec 8.2 says "back to investigating", so list this among Section 3's rulings. |
| M6 | Addressed | D5: the fold refuses an automatic close while the trigger fires, `AutoCloseAt` returns "never", and the timer is re-armed. |
| M7 | Addressed | D3 adds an optional `routing` mapping, capped at 16. The hard-coded `labels` member is gone. |
| M8 | Addressed | D8: the link's request key is a `sha256:` hash. |
| M9 | Addressed | D8: the flap link has no time bound, as spec 8.2 has none. It is ruling 5. |
| M10 | Addressed | D6 uses `CodeNotItemResponder`. D7 adds `openLocked`. The wording about queue rank went with O1. |

## New issues introduced by v2

### N1 (Important). A declined firing resume never retries while the alert keeps firing
- **Section:** D7 step 5, and ruling 1's stated cost.
- **What goes wrong:** a resume is tried only when the instance "was `resolved` or new to the item". Take instance I, which flaps (resolved, then firing) while the platform is full. The resume is declined with `over_limit`. Every later re-notification of I is firing after firing, so none of them tries again.
- **Consequence:** the item sits in `awaiting_close`, unstaffed. Because the trigger fires, it cannot auto-close either. This lasts until I resolves, possibly for hours of an active alert. Ruling 1's cost, "until the next firing", understates it.
- **Fix:** also try again on any applied firing while the item's latest firing resume since the run's last stop is `declined:*`. Or reword the cost and accept it.

### N2 (Important). Board notices fail on stopped boards, and sit outside the transaction
- **Section:** D11.
- **What goes wrong:** a stopped run's board takes rulings only. `ErrStopped` is defined at internal/work/board/board.go:40-42 and enforced at state.go:140 and round.go:305. Every notice D11 posts for an `awaiting_close` item is refused: late events, resolutions after the stop, and a declined firing.
- **Second problem:** board events live in a different store from `AppendInbound`. If the notice is a hard step, the event loops on `503` for ever and blocks its group. If it comes after the commit, a failure loses it silently, because the retry sees `Seen` and writes nothing.
- **Fix:**
  - post notices only to a running board;
  - post them best-effort after the commit, with a Warn on failure;
  - for a firing resume, post after the board's `resume` entry.
  - Optionally, have the next round's opening post any notice still missing, derived from the records.

### N3 (Important). Redacting the event id, instance and keys can collapse distinct events
- **Section:** D3 "Redaction", D4's primary key.
- **What goes wrong:** the event id is the primary key, and D3 stores it redacted. A derived id is 64 hex characters, which a generic secret rule (long hex or base64) can match. Every such id then redacts to the same replacement, so every event after the first is a `repeat` and is dropped. That is silent total loss.
- **Same cause:** for instances and keys, D3 makes an event unroutable when redaction changes them. A broad rule then makes every event unroutable. This is safe, but it needs a load-time or first-event Warn.
- **Fix:** treat the event id like routing. If redaction would change it, the event is refused with `id_refused` and stored under the SHA-256 of the raw id. Alternatively, key every row by `sha256(raw id)` and store the redacted id beside it.

### N4 (Important). The lone `Put` writes source-level truth without the holding's guard
- **Section:** D4, D7 steps 2, 3, 6, 8.
- **What goes wrong:** `AppendInbound` inherits the work records' epoch guard. The lone `Put` (`resolved_unmatched`, `late`, `refused`, `unroutable`) is not said to have one.
- **Scenario:** an instance whose lease has lapsed, but which is not yet fenced, still reads its stale account. It finds no open item and `Put`s `resolved_unmatched` (applied) for instance I, then answers `200`. Meanwhile the new leader has opened an item for I. That item never receives the resolution: the source does not retry after a `200`, and `Seen` would drop a retry anyway. So the item never resolves or auto-closes. `LastStatus` then marks I's later older events as late.
- **Status:** I did not confirm this in code. It assumes the guard is per statement through ctx's epoch, as the work stores do.
- **Fix:** every `inbound_events` insert carries the holding's epoch guard, a lone `Put` included.

### N5 (Minor). D12 lets a queued item whose trigger resolved before its start run its full budget
- **Section:** D12.
- **What goes wrong:** this follows from the clarification "the current round means the round during which the resolution was recorded". It is defensible, but it amends spec 5.6.
- **Fix:** list it under Section 3's rulings with its cost, which is spend on alerts that are already over.

### N6 (Minor). An over-limit group is still lost with nothing recorded
- **Section:** D1.
- **What goes wrong:** above the limits the answer is `413`, which a source does not retry, and nothing is stored.
- **Fix:** also record one `refused` row, under a synthetic id, with the source, the count and `too_large`, so that an operator can see the loss from the data and not only from logs.

### Checked, no new issue
- **Step 3 (late) against step 4 (match):** consistent. The source-level `LastStatus` is never behind any item's own view of an instance, and a late event is appended without being applied only where an open item holds the instance.
- **The fold refusing an automatic close while firing:** consistent with the timer being re-armed under the lock.
- **`routing`:** the keys pass `checkRouting`, and so does the views' `service`.
- **O1 and `mayResume`:** the limit + 1 overshoot is the person's rule.
- **`openLocked` and `AppendInbound`:** the event row commits with the opening, so `repeated` and `unrecorded` stay correct.
- **Size:** 3.8–4.3 pw executed is credible with O1. N1 through N4 add about half a day to one day.
