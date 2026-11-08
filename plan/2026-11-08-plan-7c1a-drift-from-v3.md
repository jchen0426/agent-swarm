# Plan 7c-1a drift analysis: v3 (written at 5a1321a) against HEAD 6f641f5

Scope: `plan/2026-11-07-plan-7b2-v3-carried-to-7c1.md`, the sections Global Constraints, What this plan relies on from Plan 7b-1, Decisions, Contracts, Earlier tests, File Structure, Tasks 5-13, Size, and Appendix A rows for Tasks 5-12. Checked against the code at HEAD and against the "Changes made during execution" and "Constraints carried to later plans" of `plan/2026-11-07-plan-7b2-stale-account-closure.md` (7b-2) and `plan/2026-11-08-plan-9a1-sign-in-and-stop.md` (9a-1). Read-only; nothing in the repository was changed.

## Baseline facts at HEAD

- Latest migration: `internal/db/migrations/0010_kill_switch.sql`. 7b-2 and 9a-1 added none (9a-1: "No migration. One optional record body field, `Opened.By`"). **0011 is free**, and `TestMigration10ReadersAreOfItsOwnLevel` still asserts `Level() == 10` (`internal/db/migrations_test.go:67-75`).
- Since 5a1321a, `internal/work/item` changed only by `Opened.By` (`item.go:176-178`). `internal/work/board` changed only by the new `status.go` (`board.StatusOf`). `turns.go` and `queue.go` did not change. So the domain halves of Tasks 5 and 6 still hold almost as written.
- No new name of Tasks 5-12 exists yet: `item.Amount`, `KindExtended`, `KindLinked`, `KindUnlinked`, `Run.Stops`, `Run.Budget`, `Run.Concludes`, `board.KindResume`, `Boards.Resume`, `Coordinator.Extend`, `Link`, `Unlink`, `ErrStopping`, `Records.IncidentItems`, `countAsStarting`, `Options.ClosedRulings` and `deploy.Lifecycle.Extension` all return no grep hits. None of the test names v3 gives to Tasks 5-12 exists yet either. **Nothing executed in 7b-2 or 9a-1 implements any part of Tasks 5-12.** Some pieces came close; see "Partial coverage" at the end.

## Amendments

1. **Global Constraints, a missing constraint: role checks.** v3 has no constraint on roles. Plan 7b-1 kept roles above C16.
   → At HEAD, the coordinator decides roles (9a-1 R-e). `app.May(a Actor, k ActKind, o *item.Opened) (Grounds, error)` is at `internal/work/app/policy.go:53`. It knows only `ActOpen, ActPromote, ActClose, ActReopen, ActSwitchOn, ActSwitchOff` (`policy.go:15-22`), and answers `ErrInvalid` for any other kind. Every item act calls it right after `target` and before `checkReason` and `findKey` (`promote.go:92-98`, `close.go:82`, `reopen.go:75`).
   → Add the constraint: "Every act this plan adds is decided by `app.May` under the coordinator's lock, right after its item is read and before its reason, its key and its state checks. `policy.go` gains `ActExtend`, `ActLink` and `ActUnlink`." Spec 10.1/4.2 make extend, link and unlink a responder's rulings, so they are member acts with an admin override, like promote/close/reopen.
   → **Blocks execution**: without this, `Extend` either skips the role check, which breaks R-e, or calls `May` with an unknown kind and is always refused `ErrInvalid`.

2. **Global Constraints, "The gates"** ("Commit by path ... no `Co-Authored-By` line").
   → 9a-1 and ADR-016 (2026-11-08, R-f) added the spend ledger (`plan/backups/<plan>/spend.md`, R93) and a calendar count. A plan now stops on calendar time or spend, not on person-weeks (`plan/adr/ADR-016-delivery-build-before-team.md:74-75`).
   → Add the R93 ledger and the R-f stop lines to the gates and to the branch's end.
   → **Would mislead an implementer.**

3. **Global Constraints, "Migrations"** ("adds one, 0011, at level 11").
   → Still true: 0010 is the last migration (`internal/db/migrations/`). Also add 9a-1's R78 rule: an optional body field needs no migration (`Opened.By`), but a new record kind does.
   → Keep, with a note that `run_extended`, `item_linked` and `item_unlinked` are new kinds and so still need 0011 at its own level.
   → **Cosmetic.**

4. **Relies on 7b-1, row 5** ("`Actor`, `Act`, `Outcome`, `Refused`, `requestOf`, ...").
   → `Actor` is now `{Subject string; Responder bool; Admin bool}` (`acts.go:25-29`). `Admin` is set only while authentication is fresh (R76). The admin API passes `adminActor = app.Actor{Subject: "admin", Admin: true}` (`internal/admin/lifecycle.go:29`). `Refused` codes gained `CodeNotItemResponder` and `CodeResponderOnly` (`acts.go:100-101`). `May`, `ActKind` and `Grounds` are new (R77).
   → Name `May`, `ActKind`, `Grounds`, `Actor.Responder` and `Actor.Admin` in row 5, citing 9a-1 R76/R77, and drop the claim that "7b-2 changes no contract of 7b-1's": 9a-1 changed the division of labour.
   → **Would mislead an implementer.**

5. **Relies on 7b-1, row 6** ("The account (`entry`, `entry.unsure`, `countAsPromoted`, `refresh`), the walk, `walkSoon`, `overLimit`, `publish`").
   → All present (`queue.go:82-110, 175-195`). 7b-2 added two things since: `steps` reloads after an unknown outcome (R50), and `settle` advances an item whose round end's answer was lost. 7b-2 also split `missedEnd`/`ending` out of `missed`/`endRound` (`rounds.go:204, 339`), which `wouldKill`/`missedDecidesStop` read (`killswitch.go:489-528`).
   → Add these as consumed facts: a resume or extension whose outcome is unknown is followed by `steps`' reload, and the preview path reads a missed round's end through `missedEnd`/`ending`, which will read `Run.Budget()` once Task 5 lands.
   → **Would mislead an implementer.**

6. **Relies on 7b-1, row 10** ("`internal/admin/lifecycle.go`: `act`, `readStrictly`, `refusal`, `consequencesOf`").
   → `refusal`, `lifecycleRefused`'s mapping, `standing`, `refusedTexts` and the codes moved to `internal/admin/switching` (9a-1 R81). `switching.Act(ctx, p, err) *Refusal` is at `switching.go:187`, unexported `refusal(err)` at `switching.go:290`, and `RefusedText` at `switching.go:57`. `admin/lifecycle.go` keeps `act` (`:176`), `readStrictly` (`:270`), `consequencesOf` (`:324`), `itemAfter` (`:211`), `lifecycleRefused(w, q, *switching.Refusal)` (`:137`) and `asked.text` (`:96`).
   → Rewrite row 10 to name both packages, and say that new codes and statuses are mapped in `switching.refusal` and worded in `switching.refusedTexts` (admin) and `internal/web/pages/words.go` (web).
   → **Blocks execution** for Task 10: the listed file is no longer where the mapping lives.

7. **Relies on 7b-1, row 11** ("Migration 0010 at level 10").
   → True (`migrations_test.go:72-75`).
   → No change.
   → **Cosmetic.**

8. **Decisions, carried structural work / C3** ("The drain's residual line moves to Info (item 15)"; R48-R51, R63 for Task 1, R65, R66, R72).
   → Executed in 7b-2. Several were also amended in execution (invariant 1's exemption narrowed, invariant 9's `unsure` exemption narrowed, `settle`'s advance).
   → In 7c-1a, cut "The carried structural work" to one line pointing at 7b-2 as executed, and cite 7b-2's amended text, not v3's. The narrowed exemptions in particular bind Task 11 (see 21).
   → **Would mislead an implementer** (v3's text of the exemption is the pre-amendment version).

9. **Decision 17, "Who"** ("the admin API now; Plan 9b: the item's responders") **and Decision 18, "Who"** ("the admin API; Plan 9b").
   → `May` now decides who may act, whoever the caller. The admin API is just an admin actor (`AdminOverride`). Any future web caller passes `Actor{Subject:"sso:..", Responder, Admin: admin && fresh}` (R76).
   → Rewrite as: "Who: `May(a, ActExtend|ActLink|ActUnlink, s.Opened)`: the item's principal or a responder (`Member`), or an admin (`AdminOverride`, fresh in the web); refused `not_item_responder` otherwise. The admin API passes `adminActor`. The web forms are Plan 9b's."
   → **Blocks execution** (same root as 1).

10. **Decision 18 / C16 `Link`: which item `May` reads** (not in v3).
    → A link touches two items. `id` moves, and `with`'s incident gains a member. `May` takes one `*item.Opened` (`policy.go:53`).
    → Decide whether `Link` checks `May` on `id` only, or on both `id` and `with`. Recommendation: both, so that a responder of B cannot join B into an incident whose items they are not on. Under spec 9.1's definition, joining another person's item is an "admin action". Add a test of the plan's (e.g. `TestALinkNeedsTheActorOnBothItems`).
    → **Would mislead an implementer**; it is a new decision for the author (see "Decisions for the author").

11. **C16 / Relies row 5: `requestOf`** (consumed unchanged in Tasks 7, 9).
    → `requestOf(kind, id, a)` digests only `{"kind","item","reason"}` (`acts.go:219-227`; the same at 5a1321a). So an extension retried under K with another amount, or a link under K to another `with`, gets the same digest. `findKey` then reports a repeat, not `ErrKeyReused`. That contradicts `TestARetriedExtensionWithItsKeyExtendsOnce` ("a third under K with another amount is `ErrKeyReused`") and makes a link's repeat ambiguous.
    → Add to C16: "`requestOf` gains the act's parameters (an extension's resolved or asked amount, a link's `with`) in its canonical JSON, keeping the existing three keys unchanged for promote/close/reopen so that their digests do not move." Also update the interleaving test's `actDigest`/`actKinds` (`interleave_invariants_test.go:327-335`).
    → **Blocks execution** (a design test cannot pass as written).

12. **C4 (amended), codes** ("`400` `self` ... `409` `not_started`, `no_agents_left`, `would_stop_at_once`, `rulings_window_passed`; `503` `stopping` with `Retry-After: 1`").
    → `switching.refusal` maps only `reason_required` among `*Refused` codes to 400. Every other `*Refused` becomes 409, so `self` would answer 409 (`switching.go:310-313`). Only `taking_over` carries `RetryAfter` (`switching.go:275-282`). `ErrStopping` would fall to `503 unavailable` with no `Retry-After`. Codes refused by `May` (`not_item_responder`) also reach these routes.
    → Say in C4 that `switching.refusal` gains `CodeSelf` → 400 and `ErrStopping` → 503 `stopping` with `RetryAfter: 1s`. Add `switching.CodeStopping`. Add C4's `403`-free list: `409 not_item_responder` now applies to extend, link and unlink.
    → **Blocks execution** for Task 10's route-table rows.

13. **C4 / Task 10, the web's words for new codes** (not in v3).
    → `TestEveryRefusalCodeHasWebWords` (`internal/web/pages/security_test.go:128-147`) parses every `Code*` constant in `internal/work/app` and `internal/admin/switching`, and fails unless `pages.Words(code)` has words for it that differ from the admin text.
    → Task 10 (or whichever task adds the constants, likely Tasks 7-9) must add web words in `internal/web/pages/words.go` for `not_started`, `no_agents_left`, `would_stop_at_once`, `rulings_window_passed`, `self` and `stopping`, even though no web form offers these acts until 9b.
    → **Blocks execution** (the gate fails at the first task that adds a code).

14. **C9 (amended)** ("`Extend(t, id, key string, b ExtendBody)` ... each through `Admin.Act`").
    → `Admin.Act(t, id, act, key string, body ActBody)` takes the concrete `ActBody{Reason, Preview}` (`test/e2e/harness/admin.go:324-327, 425-442`), so an amount or `with` cannot pass through it.
    → Say that `Admin.Act` is generalised to an unexported `act(t, id, act, key string, body any, preview bool)`, which `Act`, `Extend`, `Link` and `Unlink` share.
    → **Would mislead an implementer.**

15. **C16, `Records.IncidentItems` and its fakes** ("Records gains, beside Load, Active, Append and UnstaffedBy").
    → `Records` is unchanged (`ports.go:17-41`). 9a-1 set a precedent (R92): it put `ClosedSince` on a narrow `app.ClosedLister` (`ports.go:46-51`) so that `recordsPort` and `failing` would not change. 7b-2 made every records read in the interleaving world spend a per-instance read fault (`recordsPort`, `interleave_world_test.go:1121`; `failRead`).
    → Decide where `IncidentItems` goes: either on `Records`, with all three fakes changed and `recordsPort.IncidentItems` spending a read fault, or on a narrow port in `app.Ports` (e.g. `Incidents`), which the coordinator and `Views` take. Either way, the interleaving world's version must spend the read fault, per 7b-2's "the read faults hold for every act a later plan adds".
    → **Would mislead an implementer.**

16. **C16, `View` gains / `ViewPorts` gains `Extension`** ("View gains IncidentItems ..., Budget ... and ExtensionOffered; ViewPorts gains Extension").
    → `ViewPorts` already has `ClosedRulings`, `ReopenWindow`, `Closed ClosedLister`, `Held` and `Log` (`views.go:144-170`). `View` gained `StopEntry`, `AutoCloseUnread` and `ClosesAtUnread`, and the list's `Row` was added (`views.go:22-101`). `Row.Incident` exists (`views.go:101`).
    → Keep the addition, and say explicitly that `Row` gains nothing in 7c-1a. The web's items list and board page do not read the new fields until 9a-2 or 9b (see "New work 9a-1 implies").
    → **Cosmetic.**

17. **Earlier tests this plan changes, missing rows.**
    → `TestMayDecidesEveryActForEveryRole` (`policy_test.go:63`) must gain rows for `ActExtend`, `ActLink` and `ActUnlink`. Add a test of the plan's beside `TestAStrangerResponderCannotPromoteCloseOrReopen` (`policy_test.go:235`) for extend, link and unlink. `switching_test.go`'s refusal table gains rows for `self` and `stopping`. `TestEveryRefusalCodeHasWebWords` passes only with item 13's words. Plus any interleaving rows from item 21.
    → Add these rows to "Earlier tests".
    → **Would mislead an implementer.**

18. **Earlier tests, Task 8** ("`countAsPromoted`'s tests in `promote_test.go` follow its rename to `countAsStarting`").
    → No test names `countAsPromoted` (grep over `*_test.go` finds nothing). Its uses are `promote.go:199` and doc comments in `queue.go:29, 64, 82-93, 174` and `promote.go:58, 194`. The interleaving test's `noteUnsure` is driven by promotions only (`interleave_test.go:486`).
    → Replace with: "the rename touches `queue.go`, `promote.go` and their doc comments; no test names it. The interleaving test's `noteUnsure` learns the resume (Task 11)."
    → **Cosmetic.**

19. **Earlier tests, Task 1 and Task 3 rows.**
    → Executed in 7b-2.
    → Drop.
    → **Cosmetic.**

20. **Earlier tests, Task 10** ("the admin fixture's fake coordinator gains `Extend`, `Link` and `Unlink`").
    → True: `internal/admin/admin_test.go:117` (`coordinator`). The web's fakes (`internal/web/pages/helpers_test.go:187`, `cmd/agentswarm/web_test.go:175`) implement `web.Coordinator` (`internal/web/server.go:48-53`), which does not change if the web takes no new act.
    → Keep, and note that the web's fakes are untouched unless 7c-1a widens `web.Coordinator` (recommended not).
    → **Cosmetic.**

21. **Task 11, invariants** ("invariants 1, 2, 5 and 7 ... `runOrders` ... a new must-pass row").
    → At HEAD there are ten invariants (`interleave_invariants_test.go:982-1031`, invariant 10). Invariant 1's exemption was narrowed twice in 7b-2. It now covers only a run whose latest `round_ended` decided its stop when the on came, or whose missed round the on's own advance ended deciding a stop before the answer (7b-2 "Changes"). Invariant 9's `unsure` exemption holds only while no record took the item out of the queue. `noteUnsure` is called only for promotions (`interleave_test.go:486`). 7b-2 also carried a residual to "Plan 7c-1's interleaving extension": `runStep` stops a run with no agent `not_staffed` ahead of the kill check (`coordinator.go:967-972`), so the preview can name a run killed that the answer leaves out.
    → Task 11 must add four things:
      - (a) Invariant 1's narrowed exemptions read the latest decided round *within the current running interval*: a resumed run's pre-resume decision must not exempt it.
      - (b) A resume whose outcome is unknown calls `noteUnsure` (invariant 4/9), with a must-pass row.
      - (c) Invariant 10 rows for a resume or extension after a close, refused, and for reopen → resume → late close retry (the R10 residual), must pass.
      - (d) Take or explicitly re-carry the `not_staffed`-before-kill residual.
    → **Would mislead an implementer** (the timebox would likely be spent rediscovering these).

22. **Task 11, timebox** ("Timebox (R63): 1.5 days").
    → R-f replaced person-week triggers with calendar and spend. 7b-2's author decision on A3 covered Task 1 only.
    → Restate the timebox in calendar terms, and get the author's answer on what happens when it fires for Task 11 (see A3 below).
    → **Would mislead an implementer.**

23. **Task 7, act order** ("the act's order is Plan 7b-1's: hold, form, key, load, checks, preview, record, carry on").
    → At HEAD (`promote.go:83-128`) the order is: lock + `holds`; `checkAct`; `target` (load); **`May`**; `checkReason`; `requestOf` + `findKey`; state checks; `mayPromote`; preview; record; `counted`.
    → Replace with that order, `May` after `target`. Note that a `May` refusal comes before R60's `closed` and every other refusal in the R60 table.
    → **Would mislead an implementer.**

24. **Task 7, Interfaces, Consumes** ("`checkAct`, `target`, `requestOf`, `findKey`, `requestIn`, `carryOn`, `actFound`, `checkReason`, `heldThrough`, `record`, `advance`, `runStep`, `Sessions.Move`").
    → All exist (`acts.go:132-340`). `carryOn` still returns `(repeat, unknown bool, _ error)` (`acts.go:293`). `requestIn` still knows only `Promoted`, `Reopened` and `Closed` (`acts.go:262-273`).
    → Add `May`. Add `requestOf`'s widening (item 11).
    → **Would mislead an implementer** (only through the missing `May`).

25. **Task 7/8 files** ("Modify: `internal/work/app/acts.go` (`Consequences` gains ...)").
    → `Consequences` is unchanged since 7b-1 (`acts.go:64-75`).
    → Also list `policy.go` and `policy_test.go`.
    → **Would mislead an implementer.**

26. **Task 8, Decisions** ("Plan 7b-1's Decisions ... 13 (a found close key drops nothing after a reopen)").
    → True: `actFound` (`acts.go:321-327`).
    → No change.
    → **Cosmetic.**

27. **Task 8 / Decision 16, `runStep`** ("`runStep` skips its conclude case while `Resumed()`").
    → `runStep` at HEAD (`coordinator.go:964-981`) reads `l.Ended.Decision.Next == item.NextStop` inline at `:973`, as v3 says. The case order is unstaffed → missed → owes-kill → conclude → unsettled → open → hold.
    → Accurate. Note that R54's raise goes after the `c.unsettled` case, i.e. between `:975` and `:977`.
    → **Cosmetic.**

28. **Task 5, files and interfaces.**
    → `admitted` (`state.go:193-198`), `Run.apply` (`:214`), `mayStop` (`:249`), `Run.open` (`:267`), `exhausted`/`Run.Open`/`Run.Stop` (`decide.go:182-213`), `Halt` (`halt.go:23-26`), `State.KillSwitchSeen`/`Owes` (`lifecycle.go:55-90`), `hold`'s `run.Created.KillSwitchSeen` (`rounds.go:98`) and `roundsLeft` (`turns.go:365-373`) all match v3. Two more readers of the latest decision now exist that v3 does not list: `missedDecidesStop` reads `s.Run.End(e).Decision` (`killswitch.go:527`), which is fine because `End` reads a fresh decision; and `finishing()` (`killswitch.go:769`), which reads `Stopped` only.
    → Add one line saying these two need no change after review.
    → **Cosmetic.**

29. **Task 6.**
    → `State.check` blocks every non-ruling kind on a stopped board (`board/state.go:134`). `s.stop` is set at `:588`, read in `Stopped()` (`report.go:61-65`) and in `proposable` (`round.go:304`). `Boards.post`, `StartRound`, `Stop` and `Report` exist (`app/boards.go:162-186, 256`). The new `board/status.go` (`StatusOf`) reads assessments only and is unaffected.
    → Add `round.go:304` (`proposable` refuses on a stopped board) to the files, so that proposals work after a resume. Otherwise accurate.
    → **Would mislead an implementer** (minor).

30. **Task 9, files** ("`internal/work/postgres/records.go`, `records_integration_test.go`").
    → Both exist. `ClosedSince` (`postgres/records.go:85-104`) is the model for an SQL "latest record" query. `oneItem` (`coordinator_internal_test.go:255`), `failing` (`harness_test.go:636`, which embeds `*memory.Records`) and `recordsPort` (`interleave_world_test.go:1121`) are where v3 says.
    → Keep. Add `policy.go`, plus the port decision of item 15.
    → **Cosmetic.**

31. **Task 9, checks order** ("Checks, in this order (R60): not the item itself ... the other item exists ... held by a later build ... window ... reason").
    → `May` now comes first after `target` (item 23), and a second `May` on `with` if A4 says so, after `with` is loaded (item 10).
    → Put `May` in R60's link order.
    → **Would mislead an implementer.**

32. **Task 10, files** ("Modify: `internal/admin/admin.go` ..., `lifecycle.go` (`actOn`, ..., the codes, `consequencesOf`)").
    → Routes are registered in `admin.New` (`admin.go:149-151`). The codes and their 4xx/503 mapping live in `internal/admin/switching/switching.go` (items 6, 12). The web words live in `internal/web/pages/words.go` (item 13).
    → Add `internal/admin/switching/switching.go`, `switching_test.go`, `internal/web/pages/words.go` and `internal/work/app/policy.go`. Move "the codes" out of `lifecycle.go`.
    → **Blocks execution.**

33. **Task 10, Consumes** ("`act`, `readStrictly`, `refusal`, `lifecycleRefused`, `itemAfter`").
    → `refusal` is `switching`'s, unexported, and reached through `switching.Act`. `lifecycleRefused` now takes `*switching.Refusal`.
    → Change the list to `act`, `readStrictly`, `switching.Act`, `lifecycleRefused`, `asked.text`, `itemAfter` and `consequencesOf`.
    → **Would mislead an implementer.**

34. **Task 10, `actOn` and `adminActor`.**
    → `act` builds `&app.Act{Actor: adminActor}` (`lifecycle.go:184`).
    → Say that `actOn` uses `adminActor` too, so the admin API's extend, link and unlink are `AdminOverride` acts. Spec 9.1's loopback exception applies: no fresh authentication.
    → **Cosmetic.**

35. **Task 12, gate** ("the full `make test-e2e` ... scenarios 1 to 23 among them") **and the branch's end** ("scenarios 19 to 25").
    → 9a-1 added scenarios 26-28 (`test/e2e/web_test.go:89-205`). They run under the process runner only, and `build/test/run.sh` runs them by name in `make test-linux` (`run.sh:33-40, 181-189`). Numbers 24 and 25 are still free.
    → Say "scenarios 1 to 28, 26-28 web, under the process runner" for Task 12. The branch's end is unchanged, since `make test-linux` runs Docker e2e plus the process-only list.
    → **Cosmetic.**

36. **Task 13, documents list.**
    → It names `plan/2026-11-07-plan-7b2-run-return-and-incident.md` in its commit (that file does not exist). It asks for a roadmap row 7b-2 and "under A1, rows 7b-2 and 7c-1" (7b-2 is executed). Its ADR-016 entry is for "the trigger's firing at plan time", but the trigger is now calendar/spend (R-f). Its README line says "the admin token now authorizes extensions"; the README now presents the loopback token as break-glass, subject `admin` (9a-1 R-d).
    → Replace the list with:
      - this plan's own file;
      - the roadmap row 7c-1 split into 7c-1a and 7c-1b, with size, calendar and spend;
      - ADR-016's History under R-f (calendar and spend, person-weeks recorded);
      - spec 10.1/9.1: extend, link and unlink are a member's rulings with an admin override;
      - 7d-1's C1/C4 amendment notes beside 9a-1's (`plan/2026-10-20-plan-7d1-serve-and-harness.md:610-690`);
      - the README.
    → **Would mislead an implementer.**

37. **Not in this plan / Constraints carried** ("Plan 7c-1: a resume on a new firing ...", "Plan 9a: ... the extension offered in the interface").
    → 9a-1 did sign-in and the items list. The roadmap's 9a-2 scope (`2026-10-12-implementation-roadmap.md:51`) does not list "the extension offered in the interface", and 9b says "rulings after the stop entry", so the item is orphaned. The firing resume, flap links, source actors (9a-1 carry "Plan 7c-1: inbound openings pass a source actor") all belong to the inbound half.
    → Re-point "Plan 7c-1" to "Plan 7c-1b". Give "the extension offered, extend/link/unlink forms" explicitly to 9b, and "the incident's items in the incident strip" to 9a-2.
    → **Would mislead an implementer.**

38. **Size** ("Raw, about 9.2 days ... ADR-016's trigger fires at plan time ... 0.65-0.73 person-weeks").
    → The size covers Tasks 1-13. Tasks 1-4 are executed (7b-2: about 0.8-0.9 pw executed). The trigger is now R-f calendar and spend (ADR-016:74-75), and A2's baseline argument is moot. The roadmap row 7c-1 carries "about 1.05-1.25 raw person-weeks moved from 7b-2".
    → Rebuild the Size for Tasks 5-13 plus the branch's end: raw about 6.3-7.5 days (v3's own per-task figures), plus 9a-1's deltas (below). Give a calendar guess and a spend guess under R93/R-f, and record person-weeks only.
    → **Would mislead an implementer** (and the author).

39. **Appendix A, rows 1-34.**
    → Every test name is still free at HEAD and each is still right for its task. Row 8 (`TestMigration11ReadersAreOfItsOwnLevel`) is still valid, since 0011 is free. Its footer ("Under A1, rows 25 to 34 and 36 move to Plan 7c-1") is moot: everything is in 7c-1a.
    → Drop the A1 footer. Add the plan's new tests: May rows, digest-with-parameters, link-needs-both-items, web words.
    → **Cosmetic.**

40. **Header, Status, "How it was made", Plan review** ("Draft of 2026-11-07, version 3 ... from the code at main, 5a1321a").
    → HEAD is 6f641f5.
    → In 7c-1a's own header, say it was rebased from v3 onto 6f641f5, and that v3's two reviews stand for the unchanged parts. Re-review only the deltas listed here (a scoped review, as 7b-2 and 9a-1 did).
    → **Cosmetic.**

## v3's open "Decisions for the author"

- **A1 (move links to 7c-1): settled.** On 2026-11-07 the author cut 7b-2 to Tasks 1-4 and moved all of Tasks 5-12, links included, to 7c-1 (7b-2 plan, "The author's decisions"). Links are now 7c-1a's. The "(moves to 7c-1 under A1)" markers should simply be deleted.
- **A2 (accept the size over ADR-016's person-week trigger): superseded.** The author's cut answered it for 7b-2. For 7c-1a, R-f (2026-11-08) replaced the trigger: a plan stops past 5 calendar days or twice its calendar guess, or past twice its spend guess. Person-weeks are recorded only. A2 should be replaced by a calendar guess and a spend guess (R93 calibration after the first task).
- **A3 (when the interleaving timebox fires): partly settled.** The author decided it for Task 1 on 2026-11-07: stop if Task 1's timebox fires before the RED, otherwise continue and record residuals. Task 11's case (1.5 days) has no explicit answer, and under R-f a timebox in days interacts with the calendar stop line. Ask the author to confirm "continue, land what holds, record residuals with seeds, tell the author at once" for Task 11.
- **New, A4 (proposed): who may link.** Should `Link` check `May` on both `id` and `with`, or on `id` alone (item 10)? Recommendation: both.
- **New, A5 (proposed): order against 9a-2.** The roadmap leaves "9a-2 or 7c-1" next. 9a-2's incident strip needs `IncidentItems`, which arrive in 7c-1a. Recommendation: build 7c-1a before 9a-2, or scope 9a-2's strip to the item's own incident id until 7c-1a lands.

## New work 9a-1 implies for 7c-1a

Forced (the gates fail without it):
1. `policy.go`: `ActExtend`, `ActLink` and `ActUnlink` in `May`, as member acts with admin override; rows in `TestMayDecidesEveryActForEveryRole`; a stranger-responder refusal test for the three acts; `May` called in `Extend`, `Link` and `Unlink` after `target`.
2. `internal/admin/switching`: `CodeSelf` → 400, `ErrStopping` → 503 `stopping` with `RetryAfter` 1 s, `CodeStopping`, `refusedTexts` for every new code; rows in `switching_test.go`.
3. `internal/web/pages/words.go`: web words for every new `Code*` constant, so that `TestEveryRefusalCodeHasWebWords` passes. This is the only web change that is forced.

Implied by 9a-1's patterns (recommended):

4. `Extended.By`, `Linked.By` and `Unlinked.By` record `a.Actor.Subject`, as `Opened.By` and `Promoted.By` do. With a web caller later, that is `sso:<subject>`.
5. `IncidentItems` on a narrow port, following R92's `ClosedLister` pattern, if the author prefers not to touch the three `Records` fakes. The interleaving world's version still spends read faults.

Belongs to 9a-2 or 9b, not 7c-1a:

6. Showing budget, extension offered or incident items on the items list or board page (`internal/web/pages/board.go`, `items.go`). The incident strip is already 9a-2 scope. Extend, link and unlink forms are 9b ("rulings"). Record "the extension offered in the interface" explicitly under 9b, since no row holds it now.
7. A latent 9a-1 behaviour worth carrying to 9b: `offered()` (`items.go:132-143`) and the board page offer **Promote** on any `awaiting_close` item. For an item whose run stopped, Promote answers "Nothing changed" (`promotionHolds`: it has a run). Once 7c-1a lands, the right act there is Extend. 9b should offer Extend instead, which needs `Row` to say whether the item has a stopped run.

## Partial coverage already executed

- 7b-2 filtered `owing`/`wouldKill` through `runStop` → `Halt`, so the preview and the answer will agree for a resumed run once `decided()` exists. That part of Decision 16's preview concern is done.
- 7b-2's `steps` reload and `settle` advance already cover an extension or resume whose append's answer is lost, *inside an advance*. R53's `countAsStarting` is still needed for the account between acts.
- 9a-1 put `ClosedRulings` on `ViewPorts` (`views.go:161`) and wired it in serve (`cmd/agentswarm/serve.go:546`). Only the coordinator's `Options.ClosedRulings` (R64) is still to add.
- `Row.Incident` exists, so links will show in the items list's incident column with no extra work.
- None of the 36 design tests, nor any of the plan's own Tasks 5-12 tests, exists yet.

## How much of Tasks 5-12 is still accurate

| Task | Accurate | Why not 100% |
|---|---|---|
| 5 Records, fold, budget, 0011 | ~95% | `item` package unchanged; 0011 free; only notes 3 and 28 |
| 6 Board `resume` entry | ~90% | `proposable`'s stop check (item 29) |
| 7 Extend, running | ~75% | `May`, act order, `requestOf` digest, codes in `switching` |
| 8 Resume | ~85% | `May`; `countAsPromoted` has no tests; otherwise matches `runStep`/`open`/`hold` at HEAD |
| 9 Links | ~70% | `May` on two items (new decision), digest with `with`, `IncidentItems` port and read faults |
| 10 Admin routes, views, `lifecycle.extension` | ~55% | Refusal mapping moved to `switching`; web words forced; files list wrong |
| 11 Interleaving second half | ~60% | Ten invariants now, narrowed exemptions, `noteUnsure`, invariant 10 rows, carried `not_staffed` residual |
| 12 Harness, scenarios 24-25 | ~85% | `Admin.Act`'s concrete body; scenario count in the gate |
| 13 Documents | ~40% | Wrong target file, stale ADR-016 framing, README role wording |

Weighted by v3's raw days, about **75-80%** of Tasks 5-12's text still holds. The domain core (Tasks 5, 6 and 8's coordinator logic) is the most accurate. The edges 9a-1 rebuilt (roles, refusal routing, web words, test infrastructure) are the least.

## Recommended size delta

v3's own raw figures for Tasks 5-13 plus the branch's end: **about 6.3-7.5 raw days**, i.e. 1.25-1.5 pw. The roadmap's "1.05-1.25 moved" leaves out the branch's end. Deltas from 9a-1 and 7b-2:

| Delta | Raw days |
|---|---|
| `May` kinds, rows, per-act refusal tests (items 1, 9, 17) | +0.25 |
| Link role on two items, decision and test (item 10) | +0.1 |
| `requestOf` digest with parameters, interleaving `actDigest` (item 11) | +0.15-0.25 |
| `switching` mapping, texts, tests; web words (items 12, 13, 32) | +0.25-0.35 |
| `IncidentItems` port placement plus read faults (item 15) | +0.1-0.2 |
| Task 11: narrowed exemptions over resume intervals, `noteUnsure`, invariant 10 rows, `not_staffed` residual (item 21) | +0.25-0.5 |
| Documents re-pointed (spec 10.1/9.1, roadmap split, ADR-016 under R-f) (item 36) | +0.1 |
| Rebasing the plan onto 6f641f5 and a scoped re-review | +0.25-0.5 |
| **Total delta** | **+1.45-2.25** |

**Recommended 7c-1a raw: about 7.75-9.75 days, about 1.55-1.95 person-weeks**, recorded only. On the record's executed ratio (1.3-1.5, as 9a-1 used), that is about 2.0-2.9 pw as executed.

Under R-f, the numbers that stop the plan:
- **Calendar guess about 1.5-2.5 days.** 9a-1 ran ~4.5-5.5 raw pw in 1 day and 7b-2 ran ~0.65 in 2.5 hours, but Task 11's interleaving work ran 1.5-2× its timebox in 7b-1. The stop line is max(5 days, 2× guess) = 5 days.
- **Spend guess about $8-14** (9a-1: ~$20 for ~25 raw days, ~$0.8 per raw day; raised for soak and fix rounds). Calibrate after Task 5 per R93.
