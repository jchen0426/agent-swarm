# Plan 7c-1a, version 1: scoped plan review

Reviewer: fresh, Opus 5.5, read-only, 2026-11-08. Code read at HEAD 1df57a7 (code identical to 6f641f5).
Scope: the 40 drift items against the amendments; v3 Tasks 5-13 read through the amendments; A3' and A4; the spec conflicts; the size, calendar and spend.

**Verdict: approve with fixes.** No Critical findings. Six Important findings, each a small edit to the plan text. Fix them before execution. None needs a redesign.

---

## 1. Drift items against the amendments

Every one of the 40 items has an answer or is correctly left alone:

| Items | Answer |
|---|---|
| 1 | GC "Roles" |
| 2 | GC "The gates" |
| 3 | GC "Migrations" |
| 4 | D4 |
| 5 | D5 |
| 6 | D6 |
| 7 | left alone, correct |
| 8 | D8 |
| 9, 10 | D9, D10, A4 |
| 11 | D11 / R98 |
| 12 | D12 / R99 |
| 13 | D13 and GC "Words" |
| 14 | D14 |
| 15 | D15 / R102 |
| 16 | D16 |
| 17 | D17 |
| 18 | D18 |
| 19 | D17 |
| 20 | D32 |
| 21 | D21 |
| 22 | D22 / A3' |
| 23-25 | D23-25 |
| 26 | left alone, correct |
| 27 | D27 |
| 28 | D28 |
| 29 | D29 |
| 30 | D25 + D15 |
| 31 | D10 / D23 |
| 32-34 | D32-34 |
| 35 | D35 |
| 36 | D36 |
| 37 | R107 |
| 38 | Size |
| 39 | D39 |
| 40 | header |

I checked the following against the code:
- `May` and its kinds: `internal/work/app/policy.go:15-22, 53-76`.
- The act order, `target` → `May` → `checkReason` → `requestOf`/`findKey` → state checks: `promote.go:89-120`, `close.go:79-100`, `reopen.go:75-82`.
- `requestOf` builds `map[string]string{"kind","item","reason"}`: `acts.go:219-227`.
- `switching.refusal` gives only `reason_required` a 400 among the `*Refused` codes, and every other `*Refused` a 409; it has no `Retry-After` but `taking_over`'s: `switching.go:290-313`, `271-282`.
- `lifecycleRefused` honours `Refusal.RetryAfter` generically: `admin/lifecycle.go:137-142`.
- `runStep`'s case order: `coordinator.go:964-981`.
- `owing`, `wouldKill` and `carryOutKill`: `killswitch.go:489-726`.
- `proposable` reads `s.stop`: `board/round.go:304`.
- 0010 is still the latest migration.
- `TestEveryRefusalCodeHasWebWords`: `internal/web/pages/security_test.go:128-147`.
- `Admin.Act` compares `body != (ActBody{})`: `test/e2e/harness/admin.go:425-442`.

Each amendment matches the code, except the ones below.

---

## Important

### I1. R98 leaves the wall clock out of the digest, and "default" can never happen (D11)
**Evidence.**
- D11 says the extension's digest gains "`tokens` and `rounds` as given, or `"default"` when the body names none". `wallClock` is not named.
- Under that text, an extension retried under K with `wallClock: 10m` instead of `5m` digests the same. `findKey` (`acts.go:240-254`) then answers it as a repeat. That is exactly the case `TestARetriedExtensionWithItsKeyExtendsOnce` and Review Focus 1 forbid. The test can still pass if it varies tokens or rounds, so the hole would ship.
- `"default"` can never happen:
  - the admin API refuses a body with no amount (v3 Decision 17);
  - `ExtensionAmount` refuses one all absent or zero (v3 Task 5).
- R70 makes "named as zero" differ from "left out", per dimension. A single body-level "default" does not express that.

**On the question asked, whether promote/close/reopen digests stay fixed:** yes. `requestOf` marshals a `map[string]string` whose keys sort. If the extra keys are added only when the act has them, the three-key map, and so its bytes, are unchanged.

**Fix.** Rewrite D11:
- `requestOf(kind, id, a, extra map[string]string)`.
- An extension adds `rounds`, `wallClock` and `tokens`, each only when the person named it (a non-nil pointer), as decimal strings. `wallClock` is the parsed `time.Duration` in nanoseconds, so `5m` and `300s` agree.
- A link adds `with`.
- Promote, close and reopen pass none.
- Keep `TestTheDigestsOfPromoteCloseAndReopenDoNotMove`.
- Add a row to `TestARetriedExtensionWithItsKeyExtendsOnce`: the same key with another `wallClock` answers `ErrKeyReused`.

### I2. Review Focus 5 confuses the drain with `stopping` (R99)
**Evidence.**
- Review Focus 5 reads: "An extension retried after a drain's first moment — `503 stopping` with `Retry-After: 1`, and the retry on the next leader extends once."
- After a drain's first moment, `holds()` is false (`acts.go:120-122`), so the act answers `ErrNotHeld`. `switching.Act` then gives `409 draining` (`switching.go:187-193, 252-263`). v3 Decision 17 also says "Drain: `409` `draining`".
- `ErrStopping` is a different case: a run whose stop is recorded and whose `finish` has not ended, or R69's decided stop not yet recorded. It has nothing to do with a drain.
- An implementer following Review Focus 5 would write a test that contradicts both the code and v3.

**Fix.** Split it into two items:
- (a) an extension at a drain's first moment answers `409 draining`, and its retry with the same key on the next leader extends once;
- (b) an extension while the run is stopping answers `503 stopping` with `Retry-After: 1`.

### I3. R103 fixes the preview but not invariant 1, and its must-pass row has nothing to pin (D21 d)
**Evidence.**
- The residual: a run with no agent and no round stops `not_staffed` (`coordinator.go:967-968`) before the owed-kill case (`:971-972`).
  - `owing` lists it as killed (`killswitch.go:540-556`): `runStop(s, StopKilled)` gives `killed`, since no round decided anything.
  - `carryOutKill` then leaves it out of `Stopped`, since its recorded stop is `not_staffed` (`:704-708`).
- Making the preview "apply `runStep`'s order" fixes only the preview half.
- The answer half still breaks invariant 1. `namesEveryRun` (`interleave_invariants_test.go:585-614`) exempts a run stopped after the on only when `decidedOrder` finds a decided `round_ended`. A run with no round has none, the item is not in `Owed`, so the check fails.
- A "must-pass row that pins preview and answer equal" has no invariant to sit in. No interleaving invariant compares an on's preview with its answer: `pickSwitch`/`previewThenSet` only note results.
- The world can never make such a run. Its revision always subscribes `worldAgents = 3` agents (`interleave_world_test.go:101-103, 161-163`), which is why "no soak reached it".

**Fix.** Pick one of two:
- **(i) Cheaper, and my recommendation.** Swap the two cases in `runStep`, so the owed kill comes before the no-agent stop. Preview, answer and invariant 1 then agree with no exemption.
  - The cost: such a run's board gets the `kill_switch` notice instead of `no_agents`, and the run stops `killed`; the item is `not_staffed` either way.
  - Pin it with a unit test in `killswitch_test.go`: a zero-agent run created, then an on; the preview's `Stops` equals the answer's `Stopped`.
- **(ii)** Filter the preview, in `owing` or `wouldKill`, through one predicate shared with `runStep`, and add a matching exemption to `namesEveryRun` with a "keeps" row built by hand, since the world cannot reach it.

In both cases, add `killswitch.go` (and `coordinator.go` for i) to Task 11's Files. As written they are not listed. This is a choice for the author (see Section 3).

### I4. The words land in tasks whose commit paths and gates do not cover them (GC "Words", D13)
**Evidence.**
- The amendment requires web words and admin text "in the task that adds" each code. Tasks 7, 8 and 9 add `app.Code*` constants: `not_started` (R62, Task 7), `no_agents_left` and `would_stop_at_once` (Task 8), `self` and `rulings_window_passed` (Task 9).
- v3's commit commands for those tasks are `git add internal/work/app` (Tasks 7, 8) and `git add internal/work cmd/agentswarm/serve.go` (Task 9). Their gates are `go test ./internal/work/...`.
  - `internal/web/pages/words.go` and `internal/admin/switching/switching.go` (`refusedTexts`) are in neither.
  - Because commits go by path, those edits would stay uncommitted.
  - `TestEveryRefusalCodeHasWebWords` (`internal/web/pages`) would not run until Task 10's gate, and Task 10's commit path omits `internal/web` too.

**Fix.** Amend Tasks 7, 8, 9 and 10:
- Step 5 adds `internal/web/pages/words.go internal/admin/switching`.
- Step 4 adds `go test -race -count=1 ./internal/web/pages/ ./internal/admin/...`.

### I5. A3': a 1-calendar-day timebox is roughly ten times looser than v3's 1.5 raw days (D22)
**Evidence.**
- v3's R63 boxed Task 11 at 1.5 days on the yardstick.
- The draft converts that to 1 calendar day. At the record's pace that is about 10 raw days or more:
  - 9a-1 ran about 22.5-27.5 raw days in one calendar day (`plan/2026-11-08-plan-9a1-sign-in-and-stop.md:12, 609`);
  - 7b-1 ran about 17.5-20 raw days in two (ADR-016 History, 2026-11-03).
- A 1-day box is half the plan's calendar guess. On its own it nearly reaches the guess, so it can never fire before the plan is already late. It also counts the soaks and the Linux runs inside Task 11's wall clock.

**Fix.** Box Task 11 in units that can fire:
- either wall-clock hours of Task 11 work, about 4-6 hours, which is roughly 1.5 raw days scaled by the record's pace with 7b-1's 1.5-2x overrun;
- or Task 11's spend, for example twice its share of the guess, about $3-5.

Keep A3''s "what happens when it fires" as recommended, with I6's addition.

### I6. A3''s stop list leaves out cost safety
**Evidence.**
- A3' stops for the author only on "a run going while the kill switch is on, or for a closed item".
- This plan's new risk class is budget: a resumed or extended run spending past `Budget()` plus its wrap-up. Spec 5.6, and R56's anti-goal, a resume that stops at once.
- The property test `TestARunStopsWithinItsBudgetAndItsExtensions` exists, but the interleaving world has no budget invariant.

**Fix.** Add to A3''s stop list "a run opening a round or moving sessions past its budget and extensions", and "the limit and one exceeded" (invariant 4, which v3's R53 exists to protect). Everything else continues as residuals.

---

## Minor

- **M1.** `refused()` logs every 503 at Error except `ErrUnsettled` (`switching.go:197-207`). As mapped, `ErrStopping` would log an expected retry at Error. Fix: log it at Warn beside `ErrUnsettled`, with a `switching_test.go` row at the level.
- **M2.** Admin text for the new codes. `asked.text` (`admin/lifecycle.go:96-131`) falls back to "the act is not allowed now" for any code with no text. For a 503 that tells the client to retry, that is wrong. Fix:
  - give `stopping` its own case in `text()` ("the run is stopping; try again in a second");
  - give `self` and the four 409 codes `refusedTexts` entries.
- **M3.** A trap in the web words test. `TestEveryRefusalCodeHasWebWords` asserts the words do **not contain the substring "after"** (`security_test.go:142`). Natural wordings for `would_stop_at_once` ("…stop again right after…") or `rulings_window_passed` ("…afterwards…") fail it. Fix: warn the implementer in D13.
- **M4.** R101 is already v3's. v3's C19 already has `By string json:"by"` on `Extended`, `Linked` and `Unlinked` (v3, lines 300, 307, 313), and it is not optional. R101's "one field each, optional", and GC Migrations' "an optional body field (`By`, R101) does not [need a migration]", misdescribe it. Fix: R101 says only that `By` is `a.Actor.Subject`, as v3's C19 has it. Drop the migration remark.
- **M5.** D29 needs no code. A `resume` sets `s.stop = -1` (C18), so `proposable`'s `s.stop >= 0` (`round.go:304`) passes on its own. Fix: say "a test row: a proposal after a resume is taken"; `round.go` is already in Task 6's Files.
- **M6.** The order of a link's checks is still ambiguous.
  - D23's generic order puts `checkReason` before "the act's own checks". R60 puts `reason_required` last, after `rulings_window_passed`.
  - D10 says `with` is loaded "before R60's other link checks", but not where that falls relative to `checkReason` and `findKey`.
  - Fix: spell out Link's order once: `target(id)`, `May(id)`, `self`, `target(with)` (404, or `held_by_later_build` via `ErrSkipped`), `May(with)`, `checkReason`, `reason_required`, `requestOf(with)`+`findKey`, `closed`/window, `changed:false`, preview, record.
- **M7.** An existence oracle. A responder of `id` learns whether any item id exists: `404` versus `not_item_responder` on `with`. It is harmless behind the admin token today; 9b's web forms will expose it. Fix: carry it to 9b in R107. 9b may answer `not_item_responder` for an unknown `with` to a non-admin.
- **M8.** A4's wording. "An incident whose items are not theirs" overstates the check: `May` on `with` checks one member of the destination incident, not all of them. That is fine as a rule, but state it as written. And spec 9.1's definition of an admin action ("an act on an item where the person is neither its principal nor a responder") should say, for a link, either item. Add this to D36's spec edits.
- **M9.** R102 is not R92's case. `ClosedLister` went to the views only. Here the coordinator takes `Incidents` too, in `app.Ports`, for Unlink's "alone" check, so the interleaving world needs a new `Incidents` type that shares `recordsPort`'s per-instance read faults. v3's Task 9 Files ("`ports.go` (`Records.IncidentItems`)", "every fake of `Records`") and C16 ("Records gains") still say the old thing. Fix: amend Task 9's Files and C16 explicitly, naming the world's new type.
- **M10.** Interleaving helpers beyond `actDigest`.
  - `flying(id, act, key)` and invariant 6's `requestHeld` (`interleave_invariants_test.go:340-350, 693-705`) know only promote, close and reopen, and `actDigest` takes no parameters.
  - Fix: D11/D21 also name `requestHeld` (learns `Extended`, `Linked`, `Unlinked`), and `flying`/`actDigest` taking the act's extra keys.
  - The driver's "same key, another act" path (`pickAct`, `interleave_test.go:449-456`) must then expect `ErrKeyReused` for another amount.
- **M11.** D35's "under both runners" in Task 12's gate. `make test-e2e` on the Mac is the process runner only (`Makefile:64-65`); Docker is the branch's end, as v3 said. Fix the wording.
- **M12.** D2 runs `make test-linux` and `make test-docker` both. They share one recipe, and `test-linux` already includes the Docker path (`Makefile:200-211`; `build/test/run.sh`). Running both costs about an hour per pass. Fix: run `make test-linux` alone, or say why both.
- **M13.** Something to carry to 9a-2. The incident strip (spec 10.2) shows other items' state and conclusion. A link between an invisible-arm item and a visible one would show a hidden run's conclusion on a visible board, which breaks ADR-011's blinding. v3 carries only "cross-arm links" to 8b. Fix: R107's 9a-2 line adds "the strip shows nothing of an invisible-arm item's run".
- **M14.** Measurement. Spec 4.1 scores an incident "on its first item". Link and unlink move first items between incidents, and Unlink is checked on the moving item alone. A responder of only the first item can change what the others' incident is scored on. Fix: carry it to 8b beside cross-arm links. No change now.
- **M15.** Task 13's commit command still names `plan/2026-11-07-plan-7b2-run-return-and-incident.md`. D36 replaces the list but not the `git add` line. Fix: give the command.
- **M16.** D16 lists `ViewPorts` gaining `Extension` only. D15 adds `Incidents` too. Make D16 say both.

---

## 2. v3's Tasks 5-13 read through the amendments: the specific questions

- **Roles (`policy.go`).** Adding `ActExtend`, `ActLink` and `ActUnlink` to the `ActPromote, ActClose, ActReopen` case is a one-line change. It is correct, and it keeps `ErrInvalid` for a nil `o`. The interleaving world acts as `administrator` (subject `admin`, `Admin: true`), so roles never refuse there, which is fine.
- **The digest widening (R98).** It works mechanically; see I1 for the wall clock gap.
- **The refusal mapping (R99).**
  - Of the listed codes, only `self` (400) and `ErrStopping` (503 plus `RetryAfter`) need code in `refusal`. `not_started`, `no_agents_left`, `would_stop_at_once`, `rulings_window_passed` and `not_item_responder` already fall to the generic `*Refused` → 409 case.
  - `lifecycleRefused` honours `RetryAfter` generically.
  - The remaining gaps are M1, M2 and I2.
- **The narrow port (R102).** Workable; see M9.
- **The preview ordering (R103).** See I3. The proposed fix covers half of the case, and the proposed test has nothing to pin.
- **D21's interleaving additions.**
  - (a) is right. At HEAD, `decidedOrder` takes the latest `round_ended` that decided a stop, so a resumed run halted before its new round would wrongly keep the pre-resume exemption.
  - (b) is right. `noteUnsure` is called only for promote (`interleave_test.go:486`).
  - (c) is right.
  - (d): see I3.
- **The rest of v3 at HEAD.** Nothing contradictory beyond the findings above. `missedEnd`/`ending`/`End` reach `Run.Budget()` through `exhausted` once Task 5 lands (D28 holds). R54's raise placement after `c.unsettled` (D27) fits the switch at `coordinator.go:973-977`.

## 3. The author questions

- **A3'.** "Land what holds, record residuals with seeds, tell the author at once, continue" is sound and matches 7b-2's answer for its Task 1. But the box's unit makes it nearly inert (I5), and the stop list misses budget and limit safety (I6).
- **A4.** `May` on both items for Link, and on `id` alone for Unlink, is sound and fits spec 9.1 and 10.1. Fix the wording (M8). An admin passes both checks, so the strict side is cheap.
- **The author must also decide:**
  1. R103's route: swap `runStep`'s two cases (my recommendation), or filter the preview and add an invariant exemption (I3).
  2. Task 11's box unit: hours or dollars (I5).
  3. Whether the 9a-2 strip constraint (M13) and the measurement note (M14) are carried as proposed.

## 4. Spec conflicts

- **Spec 10.1** gives Responders "the rulings of Section 4.2", and extend, link and unlink are 4.2 rulings. So they are member acts with the admin override of 9.1. No conflict. D36's edit to 10.1 is a clarification, not an amendment.
- **Spec 9.1.**
  - The admin API's acts are the loopback break-glass exception, so no fresh authentication, which matches `adminActor`.
  - The definition of an admin action needs the two-item case (M8).
- **Spec 4.2.**
  - "Inputs" names link but not unlink as needing a reason.
  - "Closed items" does not name link among the rulings that stay available for 72 h.
  - v3's Task 13 already amends both. Keep them in D36's list explicitly: D36 currently lists only "spec 4.2 and 10.1: member rulings".

## 5. Size, calendar, spend

- **Raw, 7.75-9.75 days.** Plausible. This review's fixes add about 0.25 day (I1, I3 and I4 are small).
- **Calendar, about 2 days.** Plausible on the record:
  - 9a-1 did about 25 raw days in 1 day;
  - 7c-1a is about a third of that, but coordinator-dense, with two soaks and two Linux passes (about 2-3 hours of wall clock each pass) and a Task 11 tail.
  - I would say 1.5-3. The 5-day stop line is right.
- **Spend, about $8-14.** On the low side:
  - 9a-1's $0.8 per raw day was web-heavy, and still ran 25-50 % over its calibrated $13-16 in fix rounds;
  - this plan is coordinator and interleaving work of 7b-1's kind, with two reviewers on Tasks 8 and 11.
  - I would guess about $10-18.
  - Calibrating after Task 5, a pure-domain task in `internal/work/item`, will under-predict Tasks 8 and 11. Fix: calibrate after Task 5, but never below the stated guess, and recalibrate once after Task 8.
