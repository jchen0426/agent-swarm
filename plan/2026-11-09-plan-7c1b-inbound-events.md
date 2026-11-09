# Plan 7c-1b: inbound events — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Approved by the author on 2026-11-09 at v2, with A3'', A6 (c) and A7 as recommended; for execution subagent-driven on main. v2 answered the Opus plan review v1 (0C/11I/10M), `plan/2026-11-09-plan-7c1b-plan-review-v1.md`, with the controller's rulings on each finding (see "Plan review"); this plan's v1 is `plan/backups/plan7c1b/plan-v1.md`. Written against HEAD 441ad56. |
| Date | 2026-11-09 |
| Roadmap | `plan/2026-10-12-implementation-roadmap.md`, row 7c-1b, Inbound events. Depends on Plan 7c-1a (executed, a22c265). |
| Spec | `plan/2026-10-11-agent-swarm-platform-spec.md`, Sections 3.6, 4.1, 4.2, 5.1, 5.6, 8.2, 9.3 and 9.4, and Section 15's defaults (the rows near line 1090), as amended by Plans 7b-1, 7b-2, 9a-1 and 7c-1a; binding where it speaks. |
| Design | `plan/2026-11-09-plan-7c1b-inbound-design.md`, approved at v2.1; its Section 3 rulings 1 to 12 and Section 4 carries are decided and bind this plan. Its reviews, `plan/2026-11-09-plan-7c1b-design-review-v1.md` and `plan/2026-11-09-plan-7c1b-design-rereview-v2.md`, give the reasons; a task's brief cites them where a reason keeps an implementer from a wrong simplification. |
| Size | **Raw, about 16–17.5 days, about 3.2–3.5 person-weeks** (recorded only; the design's 15–16.5 plus the plan review's fixes). **Calendar guess about 1.5–2 days, stop line 5 calendar days. Spend guess about $30–40, stop line $80, recalibrated after Task 6 with the guess as a floor (R93's rule).** See "Size". |
| Decided by the author | 2026-11-09: the protocol is a generic webhook, a JSON body signed with HMAC, mapped by the existing `TriggerMapping` kind; no vendor format; format-independent rules first. The design approved at v2.1 the same day. This plan's own calls are R109 to R135 ("Plan-time rulings"), each with its cost if wrong; approving the plan approves them. |

**Goal:** Let an alerting source open work items. A fifth listener takes signed requests; the existing `TriggerMapping` kind turns each into events; an inbound event store keeps the source-level truth; each event is a repeat, late, matched into an open item, an unmatched resolution, or the opening of a new item under a source actor through `Open`'s queue, kill switch and cap. A trigger resolves only when no instance appended to the item fires; its resolution stops a visible run at the round's end and starts the auto-close timer; an item whose trigger fires never closes by itself. A firing for an item awaiting its close resumes its run at once or is declined with a code; a firing after the close opens a new item linked into the old incident in the same append. Trigger updates are posted to running boards as platform evidence, which agents read (A6). The interleaving test drives inbound events with nine new oracles, three existing ones adjusted, and a mutation check of eleven mutants; scenarios 29 to 32 drive the listener end to end.

**Architecture:**
- **Pure first.** `internal/work/item` gains `item_triggered`, `Opened.Trigger`, `Extended.Cause` and the fold's keys, instances and resolution (Task 2). `internal/inbound/mapping`, a new pure package, evaluates a mapping over a body (Task 3). `internal/inbound` checks signatures (Task 4).
- **One transaction per event** (design D4, review O2). Migration 0012's `inbound_events` table is written by the records stores themselves: `Records.AppendInbound` commits an item's records and the event's row together, and a lone `Put` writes a row that touches no item, both under the holding's guard (Task 1).
- **The coordinator** gains a source actor and `openLocked` (Task 5), then `Ingest` (Tasks 6 and 7), which holds the lock through one event. Trigger updates follow commits as platform evidence on running boards, best effort (Task 8).
- **Serve** binds `listeners.inbound` and serves `internal/inbound`'s handler through serve's own bounded server (Task 9).
- **The order.** The store; the records; the evaluator; the signature; the actor; `Ingest`; the firing resume and the flap; trigger evidence and the carried `scope()` fix; the listener; the interleaving test; the scenarios and the documents.

**Tech Stack:** Go 1.26 (`go 1.26.0`); pgx v5.10.0, testify v1.12.1, testcontainers-go v0.44.0, github.com/theory/jsonpath v0.12.1 (through `internal/registry/selector`), go.yaml.in/yaml/v3; Postgres 17. **No module joins the graph:** HMAC is `crypto/hmac` and `crypto/sha256`, constant-time comparison `hmac.Equal`.

**How a task runs.** As in Plans 7b-1 and 7c-1a: a light plan, test first. Each task gives its files, the interfaces it consumes and produces, its tests by name with what each asserts and why it fails first, its gate and its commit. Code blocks fix only a contract's exact form or an algorithm the design leaves open. Each task's brief carries, verbatim, the Global Constraints, the rulings it names, and the design sections it cites. Each task is reviewed by a fresh reviewer on Opus 5.5; Tasks 6, 7 and 10 by two (the adversarial one reads the design's reviews C1, C2, I1 and N1 to N4 first). The whole branch is reviewed at its end. The controller keeps the spend ledger, `plan/backups/plan7c1b/spend.md` (R93), and the calendar count from Task 1's dispatch.

## Decisions for the author

**A3'' (Task 10's timebox).** Task 10, the interleaving extension and its mutation check, is timeboxed at **5 hours of calendar time or $4 of spend, whichever comes first, from its dispatch** (7c-1a's A3'; 7b-1's interleaving tasks ran at 1.5 to 2 times their box). **Recommendation:** when it fires, the controller lands what holds (every oracle, driver event and must-pass row that is green, and every mutant caught so far), records each unresolved seed and each uncaught mutant as a residual with its seed and schedule, tells the author at once in the ledger and the final message, and goes on to Task 11. It stops for the author only on a **safety fault**, which for this plan is any of:
1. a run going while the kill switch is on, or for a closed item;
2. a run spending past its budget and every extension's, or the daily cap overspent;
3. more than the limit and one items investigating;
4. one inbound event applied twice to the item fold (two `item_triggered` records, on any items, of one `(source, eventId)`, or two rows of one key);
5. an item opened by an event not later than its instance's last known status (C2's class);
6. a `resolved_unmatched` row for an instance that an item not closed holds firing (C1's class: the item's trigger then fires for ever; invariant 14);
7. an item closed automatically while its trigger fires (invariant 13).

Cost if wrong: a fault of the C1/C2 class found only after release, as 7b-1's I1 and I2 were found late.

**A6 (how a trigger update reaches the board).** Design D11 posts each `item_triggered` as a platform `notice` on the item's running board, carrying "the redacted title, the status, the instance and the time", so that agents learn of trigger updates. Two facts of the code bear on it:
- A notice is **invisible to every agent**: `shown` (internal/work/board/view.go:80-85) hides each notice whose `Agent` is not the viewing agent's, and a trigger notice names no agent; spec 4.2 says the same ("An agent sees only its own"). A notice reaches people only, through the web's board (9a-2).
- A notice "holds names, ids and codes only, never text an agent or a server wrote" (board.go:403-408), checked by `checkNoticeShape` on a comparable struct (board/state.go:390-412); an alert's title is source-written, untrusted text (spec 3.6).

The ways:
- (a) **A `trigger` notice of trusted values only** (event id, instance, status, `late`, `resolved`, time; no title). Agents never see it; people see it once 9a-2 renders it. *Cost if wrong:* agents investigate on, unaware that a correlated alert joined or that the alert resolved, until the round's end stops them.
- (c) **Platform evidence** (`Boards.PostPlatformEvidence`, provenance `from_platform`, `Ref` `trigger:<eventId>`, `Source` the inbound source; spec 4.2's "a platform record: a trigger event"), whose claim is composed by rule and redacted as `triggerClaim` composes the opening's (coordinator.go:1492-1505): the status, the instance, the time, late or not, the trigger resolved or not, and the redacted title. Agents read it in their view and snapshot, as they read the opening claim (rounds.go:35). *Cost if wrong:* a source-written title, redacted and cut, reaches agents' prompts as a platform fact, as the opening's title already does; and each update takes a place among the board's entries.

**Recommendation: (c)**, best effort after the commit, to a running, primary, visible board only (N2), as Task 8 is written. (a) stays the alternative; choosing it changes Task 8 alone (the notice code, its shape and repeat rules in `internal/work/board`, and the post in `Ingest`), at about the same size.

**A7 (a declined firing resume, from a source whose event ids are stable).** D7 (re-review N1) retries a declined firing resume on "the next applied firing of any of its instances, a re-notification included". A source that keeps one event id per firing (as R130's example does, `{fingerprint}:{status}:{startsAt}`) re-notifies under the same id, which step 1 drops as a repeat: such a source never retries a declined resume, and an item declined `limit_reached` in an alert storm stays awaiting its close, its trigger firing and no auto-close armed, for the alert's whole life. Ruling 1's stated cost ("until the next firing") understates it for such sources.
- **Recommendation: accept it for 7c-1b,** and carry it to Plan 7c-2 and ADR-016's question 3. The item is not lost: it stays awaiting its close, visible in the items list with its run stopped and its trigger firing; a person's extension resumes it (7c-1a), and the alert's resolution lets it close. *Cost if wrong:* an alert-triggered incident is left uninvestigated during a storm until a person looks, exactly when the platform is full.
- **Alternative:** retry a declined firing resume when its decline's cause clears (a slot frees, the kill switch turns off, the day rolls over), from the walk or the off. *Cost if wrong:* it reintroduces part of the slot wait that ruling 1 (O1) removed: an owed state derived from records, a change to the walk and its invariant 9 excuse, about 1.5–2.5 raw days, and the I2 class of review v1's findings.

## Global Constraints

- **Module and Go version.** `github.com/jchen0426/agent-swarm`, `go 1.26.0`.
- **Vocabulary.** The spec's words: work item, run, round, board, entry, notice, stop reason, lease, holding, epoch, trigger, instance, dedup key, correlation key. Agents are "AI agents" or "agents", and no other word for them.
- **Domain purity.** `internal/work/item`, `internal/work/board` and `internal/inbound/mapping` import no database, network or framework package; `internal/inbound/mapping` imports `internal/registry/config`, `internal/registry/selector`, `internal/registry/tmpl`, `internal/actions/redact` and `internal/work/item` only. No vendor name or payload shape appears in platform code (the design's anti-goal).
- **Ports.** A port is declared by the package that uses it; wiring lives in `cmd/agentswarm`.
- **Decide, record, act.** Every decision is a work record, or an inbound row, written before its effects; every effect can be repeated.
- **The holding.** Every write to `work_records` and `inbound_events` runs under a context carrying its holding's epoch, checked by the store's `Guard` in its transaction (design D4, re-review N4).
- **Roles.** `app.May` decides openings and item acts under the coordinator's lock. A source actor opens and does nothing else (design D6, ruling 2). The firing resume and the flap link are platform acts, written by coordinator paths that never call `May`.
- **Words.** Every new `Code*` constant in `internal/work/app` or `internal/admin/switching` needs admin text and web words in the task that adds it (`TestEveryRefusalCodeHasWebWords`, internal/web/pages/security_test.go:128). **This plan adds none:** `Ingest`'s outcomes and reasons are `Ingest*` constants and plain strings, never `Code*`.
- **Secrets and untrusted text.** No log line, error or answer quotes a request's body, a secret, a signature, a routing value refused by redaction, or an event id that redaction would change. Untrusted fields are evidence and never route (spec 3.6).
- **Migrations.** Expand-only. Migration 0012 has no `-- agentswarm:min_reader` line, so its readers are of its own level, 12: it adds a table with triggers, which no older build reads, and this plan's new record kind, `Opened.Trigger`, `Extended.Cause` and the board's `firing` resume cause need older builds kept off the schema.
- **Record bodies stay at version 1** (Plan 7d-1's R12, until the first release): the new kind `item_triggered`, `Opened.Trigger`, `Extended.Cause` and the board's `firing` resume cause ride under migration 0012's own level, not a new body version.
- **Values.** A value fixed in code is written without brackets; a bracketed value is a deployment's default (spec Section 0). This plan's fixed values: a body of at most 4 MiB, at most 500 events after the split, a stored payload of at most 16 KiB, a clock skew of 2 min (spec 15's [2 min]; fixed in code until a deployment key is asked for), the header names, and the listener's 10 s and 30 s.
- **Test first.** Every listed test is written before its implementation and seen to fail for the reason given, except a test marked **guard**, which keeps today's behaviour, passes at Step 1, and must not be "fixed" into failing. A test's name may change to the code's vocabulary, with a ruling in the ledger. Tests this plan adds beyond the design are marked "(the plan's)".
- **Language.** Code, comments and documents in English; no Chinese anywhere in the repository.
- **Docker labels.** Every container a test or a Makefile target starts carries `agentswarm.project=agent-swarm`.
- **The gates.** Every task ends green on the Mac: `make lint` at `0 issues.` twice (`golangci-lint run ./...` and `GOOS=linux golangci-lint run ./...`); its own packages' tests under `-race`, integration-tagged ones included, as its Step 4 gives them; from Task 6 on, the interleaving test's short default run once, within its 20-second budget (R46). Serve is touched (Task 9), so **`make test-linux` runs at the branch's end, before and after the final review's fix wave, with a 40-minute soak each time**; `make test-linux` and `make test-docker` are one target (Makefile:205), so the Docker path is in it and is not run separately (7c-1a's M12). No middle task runs either.
- **Commits.** Stage by path; never `git add -A` or `git add .`. Commit as `4158857+jchen0426@users.noreply.github.com`. No `Co-Authored-By` and no `Claude-Session` line. Conventional prefixes. Push only at the branch's end.
- **The trigger.** A plan past 5 calendar days from Task 1's dispatch (time waiting for the author not counted), or past twice its spend guess, stops for the author (ADR-016, R-f). Person-weeks stop nothing.

## Plan-time rulings

Where the design is silent, ambiguous or infeasible against the code, the smallest decision consistent with the spec and the design. Each is the planner's call; approving the plan approves it.

- **R109 (D4).** `Records` gains `AppendInbound`; the new port `app.Inbound` (`Seen`, `Put`, `LastStatus`, `LastItem`) is implemented by the records stores themselves, `postgres.Records` and `memory.Records`, as `app.Incidents` is (R102). `Put` then shares `Records.Guarded`'s guard and `AppendInbound` shares `Append`'s transaction, which D4 and N4 require. *Cost:* the records stores grow by five methods; splitting them later is mechanical.
- **R110 (D5).** `State.ResolvedAt() (time.Time, bool)` and `State.AutoCloseAt(delay, offAt) (time.Time, bool)`: `false` is "never", while the trigger fires. Callers: autoclose.go:93 and :183 arm or record nothing; reopen.go:195 gives no `ClosesAt`; views.go:502 gives no time and `unread` false; the interleaving invariants read the pair. *Cost:* four call sites and their tests.
- **R111 (D5, D7 step 4).** The fold also keeps every `(source, correlationKey)` an item took, applied or late, beside the dedup keys and instances D5 names; correlation matches any of them, with `OpenedAt` within the window. D5 lists two sets and D7 matches the correlation key without saying whose; matching the opening's alone would repeat C1 for correlation. *Cost:* one more set in the fold.
- **R112 (D7 step 5, review M5).** The firing resume reuses `mayExtend` and `mayResume` (internal/work/app/extend.go:144-198) with the run's remaining budget (an amount of zero). Its decline codes are those functions' codes, plus two: `kill_switch_on`, `unsettled` (`ErrUnsettled`), `daily_cap_reached`, `no_agents_left`, `would_stop_at_once`, `limit_reached` and `no_run` (`CodeNotStarted`'s case). D7's "no budget left" is `mayResume`'s `WouldOpen` check under a zero amount, so it is `would_stop_at_once`, one code. A failed read of the day's spend is an error, not a decline: the event is answered `503` and nothing is written. *Cost:* one word.
- **R113 (D1; review M8).** `internal/inbound` builds the handler only (`inbound.NewHandler`); serve's `newServers` (cmd/agentswarm/serve.go:773-787) gains an `inbound http.Handler` parameter, nil without the key, and makes `servers.inbound` with its own `server` closure (serve.go:775), so the inbound server has exactly the admin API's bounds (`ReadHeaderTimeout` 10 s, `ReadTimeout` 30 s, `ErrorLog` at Warn) and no third copy of them exists. Unlike the web's server (internal/web/server.go:119-150), it sets no `IdleTimeout` or `MaxHeaderBytes` of its own, as the admin API's does not; the body is bounded by the handler (`MaxBody`). *Cost if wrong:* the inbound listener takes Go's default idle timeout and header size, as the admin API does.
- **R114 (D1).** `worstDrain` counts one `stopWait` for each server `close` shuts down, configured or not: today's formula counts three servers (serve.go:1017, "6*stopWait": three servers, the refresh, two pools) while `close` shuts four since Plan 9a-1 (serve.go:1029); with the inbound server it is five, so `8*stopWait`, and the defaults give **1m37s**, not 1m27s. `TestTheWorstDrainCountsEachTerm` (serve_test.go:539) and `TestServeStartedNamesTheWorstDrain` (serve_integration_test.go:246-260) change their constants (review M2). *Cost:* a supervisor's stop timeout set 10 s higher than needed where no web or inbound block exists.
- **R115 (D1, N6).** An authenticated body that is not JSON answers `400 bad_body` and records one `refused` row keyed `sha256:` of the body, with reason `body_unparsed`, as N6 records `too_many_events`. *Cost:* one reason code.
- **R116 (D6; review I1).** A source actor's opening names no principal and no responders until Plan 7c-2: `checkOpener` refuses one that names either, `ErrInvalid`. D6's "requires no principal and no responders" is read as "names none", so nothing confers authority before 7c-2's identity reads. Conversely, an opening whose `OpenInput` carries a `Trigger` or a `FlapOf` is refused `ErrInvalid` unless its actor is a source: no person or admin forges trigger evidence. *Cost:* none for Ingest, which names none.
- **R117 (D3).** The Warn that `principal`, `responders` and `fallbackPrincipal` are not applied until Plan 7c-2 is logged by the inbound handler the first time it serves a mapping under a revision (once per mapping name and commit), not by the registry at load: the registry's refresh has no logger hook for mappings. *Cost:* the line comes at the first request, not at the revision's load.
- **R118 (D4).** `payload` is stored as `text`: the element, redacted with `Redactor.Value`, encoded as JSON, then cut to 16 KiB at a rune boundary with the cut mark, `[withheld]` when `EscapedValue` reports an escape. A cut value is no longer JSON, so the column is not `jsonb`. *Cost:* a reader parses nothing from a cut payload.
- **R119 (D7 step 5).** The board's `resumeCauses` (internal/work/board/state.go:303) gains `firing`; `resumeBoard` (rounds.go:55) posts the cause of the run's latest resume, through a new `Run.LatestResumeCause() ExtendCause`. D7 says the trigger update follows the board's `resume` entry but not the entry's cause; today's only cause is `extension`. *Cost:* one value and one method.
- **R120 (D7, D8).** A firing resume's `Request` is `{Key: sha256:hex("firing:"+eventId), Digest: the same}`; a flap link's is `{Key: sha256:hex("flap:"+eventId), Digest: the same}`. `requestIn` (acts.go:313) returns nil for an `Extended` whose `Cause` is `firing` and for a `Linked` whose `By` starts with `source:`, so `findKey` never matches a person's key against them (a person could send the literal key `flap:<id>`), and `extendedBudget` skips firing resumes. *Cost:* none known.
- **R121 (D7; review M5).** `Ingest` takes the opening's item id from its caller: the handler derives `app.ItemID(tenant, "inbound:"+source+":"+eventId)`, since the coordinator holds no tenant (`Options` has none). The admin API's keys are raw (admin.go:293), so an admin may have opened an item under such an id; `openLocked`, for an opening that carries a `Trigger`, answers `ErrKeyReused` when `repeated` finds the id opened (whatever its digest), never the item, so `Ingest` records `refused` `key_reused` and never answers `opened` without its row and trigger. *Cost:* an inbound event whose derived id an admin took is refused, not opened.
- **R122 (D3).** A declared `eventId` template that lacks a value on an event is treated as absent: the id is derived. *Cost:* such an event's retries dedup by content, which they do anyway.
- **R123 (D2).** A valid secret line is non-empty after its trailing `\r` is cut, and holds no space and no character `config.Unseen` reports; the file holds one or two such lines and nothing else. *Cost:* a secret with a space is refused as unreadable (`503`).
- **R124 (D2).** A request without both headers, or with a timestamp that is not 1 to 12 decimal digits, or with no `v1=` entry of 64 lowercase hex characters, answers `401 bad_signature`. The signature is checked before the window, so an unsigned caller learns nothing of the instance's clock; a signed request outside the window answers `401 stale`. *Cost:* none known.
- **R125 (D5).** The fold refuses an applied `item_triggered` not later than its instance's last applied status on the same item, by D7 step 3's rule (at equal time `resolved` wins): the item-level mirror of the source-level order, which the coordinator keeps. *Cost:* a record the coordinator should never write is refused rather than folded.
- **R126 (D5; review I2).** **One record order for an inbound opening, stated once:** `item_opened` (with its `Trigger`), `item_triggered` (applied, firing, of the opening's source and dedup key), `item_linked` for a flap, then `item_awaiting_close` when the opening is not staffed. `openLocked` writes them in that order in one append, from its `OpenInput` (Task 5). The fold refuses anything else after an opening whose `Trigger` is set: its second record must be that `item_triggered` (a flap's `item_linked`, third, is admitted while queued as any link is, and invariant 15 checks it is there). `item_triggered` and `item_linked` are both admitted while queued (state.go:276-281). *Cost:* none; Ingest always writes them together.
- **R127 (D12).** "Later than the run's latest start or resume" is judged by record sequence, not time: the fold keeps the `seq` of the resolving `item_triggered` and of the run's `run_created` and latest resume. Records checked before their commit carry a sequence in `record` (coordinator.go:1259) but no time. *Cost:* none known.
- **R128 (D14).** The interleaving world drives `Ingest` with evaluated events directly, not through the HTTP handler; the handler has its own tests (Task 9). *Cost:* a handler-level fault is out of the world's reach, as gateways are (7b-1's R35).
- **R129 (e2e).** The e2e harness derives an inbound item's id itself (`harness.InboundItemID`): code under `test/e2e` imports the standard library, testify and testcontainers-go only (depguard), so it cannot call `app.ItemID`. A harness test pins the derivation with a vector computed once from `app.ItemID`. *Cost:* a vector to update if the derivation ever changes.
- **R130 (example configuration).** `examples/config/trigger-mappings/alerts.yaml` gains `routing: {service: labels.service}`, and its `eventId` becomes `"{fingerprint}:{status}:{startsAt}"`: with `"{fingerprint}:{startsAt}"` a resolved alert, which keeps its start time, would have its firing's id and be dropped as a repeat, so the example's items would never resolve. A comment says its `checks` refuse its events until Plan 7c-2 (ruling 3). *Cost:* an example's text.
- **R131 (D10; review I4).** `Ingest` maps `Open`'s refusals by `errors.Is`, never by an error's text. Task 5 adds three exported sentinels in `internal/work/app`, each wrapped together with `ErrInvalid` (`fmt.Errorf("%w: %w: …", ErrInvalid, ErrX)`), so every caller that tests `ErrInvalid` is unchanged: `ErrTypeNotRunnable` (from `runnable`, coordinator.go:1292) → `type_not_runnable`; `ErrRoutingRefused` (from `checkRouting`, :1379) → `routing_refused`; `ErrOpenerRefused` (every `checkOpener` refusal but `May`'s, :1421, and the source check, :344) → `invalid`. `ErrKeyReused` → `key_reused`; `ErrSkipped` → `skipped`; a `*Refused` → `invalid`; any other `ErrInvalid` → `invalid`. `ErrNoRevision` and store errors are transient: returned, answered `503`. *Cost:* three names.
- **R132 (D1; review M4).** The loader checks a mapping's `spec.source` against `Open`'s form, `^[a-z][a-z0-9-]{0,62}$` (coordinator.go:263), not only `objectName` (internal/registry/config/names.go:19), which admits a leading digit: a source `9am` would load and every request to it be answered `404`. *Cost:* a configuration with such a source no longer loads.
- **R133 (D7; review I5).** **An item pending reload.** When an append of `Ingest`'s (an opening's or a matched event's) fails with an outcome that may have landed, `Ingest` marks the item pending reload in a new `c.pending map[item.ID]string`, by the event's source, arms its retry with `c.retry(id)` (coordinator.go:875), as `unrecorded` (coordinator.go:413-438) and `carryOn` (acts.go:350) do, and answers the error (`503`). Every successful `c.load` of the item clears the mark (in `refresh`, queue.go:110), so the retry's advance clears it. At the start of each event `Ingest` loads every pending item of the event's source (the account's stale entry cannot say which keys landed); while one fails to load, an event of that source whose order, match or `LastItem` would read that item, which is every event of that source, answers `503` with nothing written. Separately, step 7's `LastItem` naming an item that is not closed and not in the account (its opening's answer lost, its lookup failed) loads it: if it folds and is not closed, it enters the account and step 4's match runs again; if it does not fold or a later build holds it, `refused` `item_unreadable`. *Cost:* a source's events wait on one item's reload, a retry wait at most while its store answers.
- **R134 (D3; review I10).** The event id, the instance, the dedup key and the correlation key are each 1 to 256 runes with no character `config.Unseen` reports; otherwise the event is `Unroutable` with `field_invalid:<field>` (`field_invalid:eventId`, `field_invalid:instanceId`, `field_invalid:dedupKey`, `field_invalid:correlate.key`). The id's case stores only `sha256:` of the raw id and the reason, as N3's `id_redacted`; the others store the id and the reason, no key columns. *Cost:* a source whose fingerprints exceed 256 characters opens nothing until its mapping shortens them.
- **R135 (D14; review I9).** The interleaving driver gives each `(source, instance)` one fixed dedup key and one fixed correlation key, as fingerprinting sources do: rule 1 (match by instance) adds the event's dedup key to the item, so an instance whose events carried different keys would let two open items share a key with no reopen, which the coordinator does rightly and invariant 17 would flag. *Cost:* the world never drives a source whose instances change keys; such a source is accepted by the coordinator and judged by no oracle.

## Tasks

### Task 1: Migration 0012, the inbound event store, and `AppendInbound`

**Design:** D4; rulings 6 and 7. **Rulings:** R109, R118.

**Files:**
- Create: `internal/db/migrations/0012_inbound_events.sql`
- Modify: `internal/db/migrations_test.go` (a test for 0012; `TestMigration11ReadersAreOfItsOwnLevel` drops its "this build's last migration" assertion, `Level() == 11`, which moves to 0012's test)
- Modify: `internal/work/app/ports.go` (`Records.AppendInbound`, `Inbound`, `InboundEvent`, `InboundOutcome`, `ErrSeen`)
- Modify: `internal/work/postgres/records.go` (`AppendInbound`); Create: `internal/work/postgres/inbound.go` (`Seen`, `Put`, `LastStatus`, `LastItem`), `internal/work/postgres/inbound_integration_test.go`
- Modify: `internal/work/memory/records.go` (the inbound rows beside the records, under the same lock, `AppendInbound`); Create: `internal/work/memory/inbound.go`, `internal/work/memory/inbound_test.go`
- Modify (compile only, no behaviour): `internal/work/app/coordinator_internal_test.go` (`oneItem.AppendInbound` returns an error), `internal/work/app/interleave_world_test.go` (`recordsPort.AppendInbound` passes through `w.recordStore` with `Append`'s guard, note and faults; the inbound reads pass through unfaulted until Task 10)

**Interfaces:**
- Consumes: `postgres.Guard`, `loadRecords`, `due` (records.go); `audit_records_guard()` (migration 0003).
- Produces:

```go
// package app (ports.go)
type InboundOutcome string

const (
	InboundOpened            InboundOutcome = "opened"
	InboundAppended          InboundOutcome = "appended"
	InboundLate              InboundOutcome = "late"
	InboundResolvedUnmatched InboundOutcome = "resolved_unmatched"
	InboundUnroutable        InboundOutcome = "unroutable"
	InboundRefused           InboundOutcome = "refused"
)

// InboundEvent is one row of the inbound event store (design D4).
type InboundEvent struct {
	Source, EventID                      string
	InstanceID, DedupKey, CorrelationKey string             // "" when not stored
	Status                               item.TriggerStatus // "" when not stored
	OccurredAt                           time.Time          // clamped; zero when not stored
	Item                                 item.ID            // "" when the row touches no item
	Applied                              bool
	Outcome                              InboundOutcome
	Reason, Title, Payload               string
	ReceivedAt                           time.Time          // the store's; ignored on write
}

// ErrSeen refuses a row whose (source, event id) the store holds already.
var ErrSeen = errors.New("app: the inbound event was seen already")

// Records gains, beside Append:
//   AppendInbound is Append, whose decide also returns the event's row, which
//   commits in the same transaction as the drafts, under the same guard, with
//   Item set to id; a row whose key is held fails with ErrSeen and commits
//   nothing.
AppendInbound(ctx context.Context, id item.ID, decide func(history []item.Record) ([]item.Draft, *InboundEvent, error)) ([]item.Record, error)

// Inbound is the inbound event store's reads and its lone write (D4; R109).
type Inbound interface {
	Seen(ctx context.Context, source, eventID string) (bool, error)
	// Put writes a row that touches no item, under the guard; ErrSeen as above.
	Put(ctx context.Context, e *InboundEvent) error
	// LastStatus is the instance's latest applied row: by occurred_at, and at
	// equal time resolved over firing; ok false with none.
	LastStatus(ctx context.Context, source, instanceID string) (status item.TriggerStatus, at time.Time, ok bool, err error)
	// LastItem is the item of the latest row, by received_at, of the dedup key
	// that names an item; ok false with none.
	LastItem(ctx context.Context, source, dedupKey string) (id item.ID, ok bool, err error)
}
```

`item.TriggerStatus` is declared in Task 2; Task 1 declares it first in `internal/work/item/item.go` (`type TriggerStatus string`; `Firing`, `Resolved`), with no other item change.

**The migration's shape** (header comment in 0010's voice: what the table is, the spec and design it serves, that its rows are append-only evidence of the "event logs" class [30 days], deleted only by the retention job (Plan 10a), not chained (ruling 7), and that no older build reads it, so its readers are of its own level):

```sql
CREATE TABLE inbound_events (
    tenant_id       text        NOT NULL,
    source          text        NOT NULL,
    event_id        text        NOT NULL CHECK (event_id <> ''),
    received_at     timestamptz NOT NULL,
    instance_id     text        NOT NULL,
    status          text        NOT NULL CHECK (status IN ('firing', 'resolved', '')),
    occurred_at     timestamptz,
    dedup_key       text        NOT NULL,
    correlation_key text        NOT NULL,
    item_id         text,
    applied         boolean     NOT NULL,
    outcome         text        NOT NULL CHECK (outcome IN ('opened', 'appended', 'late', 'resolved_unmatched', 'unroutable', 'refused')),
    reason          text        NOT NULL,
    title           text        NOT NULL,
    payload         text        NOT NULL,
    PRIMARY KEY (tenant_id, source, event_id),
    CHECK (applied = (outcome IN ('opened', 'appended', 'resolved_unmatched'))),
    CHECK (outcome NOT IN ('opened', 'appended') OR item_id IS NOT NULL),
    CHECK (outcome NOT IN ('resolved_unmatched', 'unroutable', 'refused') OR item_id IS NULL)
);
-- An instance's last known status (design D4, review O3).
CREATE INDEX inbound_events_by_instance ON inbound_events (tenant_id, source, instance_id, occurred_at DESC);
-- The last item a dedup key took (design D8).
CREATE INDEX inbound_events_by_dedup_key ON inbound_events (tenant_id, source, dedup_key, received_at DESC);
CREATE TRIGGER inbound_events_append_only BEFORE UPDATE OR DELETE ON inbound_events
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();
CREATE TRIGGER inbound_events_no_truncate BEFORE TRUNCATE ON inbound_events
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();
```

**Notes for the implementer:** `received_at` is `clock_timestamp()` at the insert. `Put` is one read-committed transaction: the guard's check, then the insert; a unique violation (SQLSTATE `23505` on the key) becomes `ErrSeen`. `AppendInbound` is `Append`'s transaction with the row inserted after the drafts, `item_id = id`; `Append` and `AppendInbound` share one unexported body. `LastStatus` reads `WHERE applied ORDER BY occurred_at DESC, (status = 'resolved') DESC LIMIT 1`. `LastItem` reads `WHERE item_id IS NOT NULL ORDER BY received_at DESC LIMIT 1`. The memory store keeps rows in insert order beside its records, under `Records.mu`, and stamps `ReceivedAt` from `now`. Integration tests use a tenant of their own per test (7b-1 Task 11's pattern).

**Tests (write first):**
- `TestMigration12ReadersAreOfItsOwnLevel` (`internal/db`, unit): `0012_inbound_events.sql` is embedded at level 12 with `min_reader` 12 and no declaration, and `Level()` is 12. Fails first: no such migration.
- `TestTheInboundEventsAreAppendOnly` (`internal/work/postgres`, integration): an `UPDATE`, a `DELETE` and a `TRUNCATE` of `inbound_events` are refused with "inbound_events is append-only"; a `DELETE` under `SET LOCAL agentswarm.retention = 'on'` succeeds. Fails first: no table.
- `TestAnInboundEventIsSeenOnceBySourceAndID` (integration): after `Put` of `(alerting, e1)`, `Seen(alerting, e1)` is true and `Seen(paging, e1)` false; a second `Put` of `(alerting, e1)` fails with `ErrSeen` and the first row is unchanged.
- `TestAnInstancesLastStatusIsItsLatestAppliedRow` (integration): rows for instance `i`: `appended` firing at t1, `late` firing at t0 (not applied), `resolved_unmatched` at t2, `refused` with no status: `LastStatus` is `(resolved, t2, true)`; with an `appended` firing and an `appended` resolved both at t3, it is `(resolved, t3)`; an unknown instance gives `ok` false.
- `TestTheLastItemAKeyTookIsTheLatestRowWithAnItem` (integration): rows of key `k`: `opened` item A, then `appended` item B, then `unroutable` with no item: `LastItem(k)` is B; an unknown key gives `ok` false.
- `TestAppendInboundCommitsTheRecordsAndTheRowTogether` (integration): a decide that returns two drafts and a row commits both, the row's `item_id` the item; a decide that fails commits neither; a row whose key is held fails `ErrSeen` and commits no record.
- `TestEveryInboundInsertIsGuardedByTheHolding` (integration): under an earlier epoch, `Put` and `AppendInbound` fail wrapping `holding.ErrLeaseLost` and write nothing; a context without an epoch fails wrapping `holding.ErrNoEpoch`.
- `TestAStoredInboundEventKeepsEveryColumn` (integration, the plan's): a full row and an `unroutable` row with only id and reason round-trip, empty strings as `''`, no status as `''`, no time as `NULL`.
- `TestTheMemoryInboundStoreAnswersAsThePostgresOne` (`internal/work/memory`, unit): every case of the five integration tests above but the guard and the triggers, on `memory.Records`.
- Changed: `TestMigration11ReadersAreOfItsOwnLevel` without its `Level()` assertion.

- [ ] **Step 1: Write the tests.** Run `go test -race -count=1 ./internal/db/ ./internal/work/...` and `go test -race -count=1 -tags integration ./internal/work/postgres/ ./internal/db/`. Expected: FAIL, each for its reason.
- [ ] **Step 2: Implement** the migration, the types, both stores, and the compile-only fakes.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/db/... ./internal/work/...`; `go test -race -count=1 -tags integration ./internal/work/postgres/ ./internal/db/`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/db internal/work/app/ports.go internal/work/app/coordinator_internal_test.go internal/work/app/interleave_world_test.go internal/work/item/item.go internal/work/postgres internal/work/memory`; `git commit -m "feat(work): the inbound event store, migration 0012 at its own level, and AppendInbound"`.

### Task 2: Records: `item_triggered`, `Opened.Trigger`, `Extended.Cause`, and the fold's trigger

**Design:** D5, D12's fold half; review C1, M6, I3, I8; plan review I1, I2, M3, M7. **Rulings:** R110, R120, R125, R126, R127.

**Files:**
- Modify: `internal/work/item/item.go` (`KindTriggered`, `Triggered`, `Trigger`, `Opened.Trigger`, `Extended.Cause`, `ExtendCause`, `Body.Triggered`, `Body.Kind`)
- Modify: `internal/work/item/state.go` (the fold: admitted kinds, `triggered`, `closeItem`'s refusal, `extend`'s firing case, the run's start sequence)
- Modify: `internal/work/item/lifecycle.go` (`ResolvedAt`, `AutoCloseAt`, `TriggerFiring`, `TriggerResolvedSince`, `FiringResumeDeclined`, the holds queries); `internal/work/item/extend.go` (`LatestResumeCause`)
- Create: `internal/work/item/trigger_test.go`; Modify: `internal/work/item/lifecycle_test.go`, `extend_test.go`
- Modify: `internal/work/app/autoclose.go`, `reopen.go`, `views.go` (R110's callers), `acts.go` (`requestIn`, R120), `extend.go` (`extendedBudget` skips firing resumes); `internal/work/app/autoclose_test.go`, `acts_test.go`; `internal/work/app/interleave_invariants_test.go` (the four `AutoCloseAt` reads, lines 1421, 2252, 2263 and 2275, take the pair; at 1421, invariant 8's check of an automatic close, `ok == false` itself breaks invariant 8)

**Interfaces:**
- Consumes: `State.apply`, `admitted`, `closeItem`, `extend`, `Run.resume` (state.go).
- Produces:

```go
// package item
const KindTriggered Kind = "item_triggered"

// TriggerStatus, Firing and Resolved exist (Task 1); not declared again.

// Triggered is an inbound event appended to an item (design D5): trigger
// evidence, applied or late.
type Triggered struct {
	Source         string        `json:"source"`
	EventID        string        `json:"eventId"`
	InstanceID     string        `json:"instanceId"`
	DedupKey       string        `json:"dedupKey"`
	CorrelationKey string        `json:"correlationKey,omitempty"`
	Status         TriggerStatus `json:"status"`
	OccurredAt     time.Time     `json:"occurredAt"` // clamped
	Applied        bool          `json:"applied"`
	// Resume is "", "resumed" or "declined:<code>" (D7).
	Resume string `json:"resume,omitempty"`
}

// Trigger is an inbound opening's trigger (D5). Task 5 carries it in from
// OpenInput.Trigger; the opening records it as Opened.Trigger.
type Trigger struct {
	Source         string `json:"source"`
	DedupKey       string `json:"dedupKey"`
	CorrelationKey string `json:"correlationKey,omitempty"`
}
// Opened gains:   Trigger *Trigger `json:"trigger,omitempty"`
// Body gains:     Triggered *Triggered `json:"triggered,omitempty"`

type ExtendCause string
const ExtendFiring ExtendCause = "firing"
// Extended gains: Cause ExtendCause `json:"cause,omitempty"`

func (s *State) ResolvedAt() (at time.Time, resolved bool)
func (s *State) AutoCloseAt(delay time.Duration, offAt time.Time) (at time.Time, ok bool)
func (s *State) TriggerFiring() bool
func (s *State) TriggerResolvedSince() bool   // D12: resolved, by a record after the run's latest start or resume
func (s *State) FiringResumeDeclined() bool   // N1: the latest Resume since the run's latest stop is declined:*
func (s *State) HoldsInstance(source, instanceID string) bool
func (s *State) HoldsDedupKey(source, key string) bool
func (s *State) HoldsCorrelationKey(source, key string) bool
func (s *State) Instance(source, instanceID string) (status TriggerStatus, at time.Time, ok bool) // last applied on the item
func (r *Run) LatestResumeCause() ExtendCause
```

**The fold's rules:**
- `item_triggered` is admitted in `queued`, `investigating` and `awaiting_close`, never `closed`. Its body needs a source, an event id, an instance, a dedup key and a status of `firing` or `resolved`; `Resume` is `""`, `resumed`, or `declined:` and a code of `^[a-z][a-z_]{0,31}$`, and is non-empty only on an applied firing while the item awaits its close.
- Every `item_triggered` adds its `(source, dedupKey)`, `(source, instanceId)` and, when set, `(source, correlationKey)` to the item's sets (R111). An applied one also sets its instance's last applied `(occurredAt, status)`, refused when not later than the one before by D7 step 3's rule (R125); the trigger's resolution is the record time and sequence of the applied record after which no instance fires, cleared by an applied firing.
- An opening whose `Trigger` is set is followed, as its second record, by an applied firing `item_triggered` of the same source and dedup key (R126's order: `item_opened`, `item_triggered`, [`item_linked`], [`item_awaiting_close`]).
- `closeItem` refuses `Auto` while `TriggerFiring()` (review M6): "an item closes by itself only while its trigger does not fire".
- `extend`: `Cause` is `""` or `firing`; `firing` needs a stopped run while the item awaits its close and an amount of zero, and resumes the run; `""` keeps today's rules (an amount above zero in one dimension).
- `run_created` and a resume set the run's start sequence; `run_stopped` clears the item's latest `Resume` (N1's "since the run's stop").
- `ResolvedAt`: an item with no `item_triggered` was resolved at its opening (`OpenedAt, true`); while any instance fires, `(zero, false)`; otherwise the resolving record's time. `AutoCloseAt` is `false` exactly when `ResolvedAt` is.

**Tests (write first):**
- `TestAnItemTriggeredIsAdmittedWhileTheItemIsNotClosed` (item, table): admitted queued, investigating and awaiting its close; refused closed, `ErrInvalid`.
- `TestAnInboundOpeningIsFollowedByItsTrigger`: an opening with a `Trigger` then `item_awaiting_close` is refused; R126's order, `item_opened`, `item_triggered`, `item_linked`, `item_awaiting_close`, folds whole; an `item_triggered` of another source or dedup key, or unapplied, or resolved, is refused.
- `TestTheFoldKeepsEveryKeyAndInstanceAnItemTook`: after an applied firing (dedup `d1`, instance `i1`, correlation `c`) and a late one (`d2`, `i2`), `HoldsDedupKey` is true for `d1` and `d2`, `HoldsInstance` for `i1` and `i2`, `HoldsCorrelationKey` for `c`, all under their source only. Fails first: no methods.
- `TestAnAppliedTriggerFollowsItsInstancesOrderOnTheItem`: an applied firing at t0 after one at t1 is refused; a resolved at t1 after a firing at t1 folds; a firing at t1 after a resolved at t1 is refused; a late (`Applied` false) at t0 folds.
- `TestTheTriggerFiresWhileAnyInstanceFires`: two instances firing, one resolved: firing; both resolved: not.
- `TestResolvedAtIsWhenTheLastFiringInstanceResolved`: a person's item gives `(OpenedAt, true)`; while firing `(zero, false)`; two instances resolved at record times r1 < r2 give r2; a new firing then its resolution at r3 give r3.
- `TestAnItemWhoseTriggerFiresNeverClosesByItself`: an automatic `item_closed` while firing is refused; a person's close is not.
- `TestAutoCloseAtIsNeverWhileTheTriggerFires`: firing gives `ok` false; once resolved at r, `AutoCloseAt(d, zero)` is the latest of r and `AwaitingSince`, plus d.
- `TestAFiringResumeAdmitsAZeroAmountOnAStoppedRunOnly` (table): `Cause` firing with zero on a stopped run awaiting its close resumes it; on a running run refused; firing with an amount refused; an unknown cause refused; zero without a cause still refused.
- `TestTheTriggerResolvesARunOnlyAfterItsStartOrResume`: resolution before `run_created` gives `TriggerResolvedSince` false; after it, true; after a later resume, false until a new resolution; a person's item, false.
- `TestAFiringResumeDeclinedSinceTheRunsStopIsRemembered`: `declined:limit_reached` after the run's stop gives `FiringResumeDeclined` true; a later `run_stopped` clears it; a `resumed` after it gives false.
- `TestTheTriggerRecordsRoundTripAtVersionOne`: a body with each new field marshals to the JSON names above at `"v":1` and decodes back equal; `Kind()` is `item_triggered`.
- `TestALatestResumeKnowsItsCause` (extend_test, the plan's): an extension's resume gives `""`, a firing's `firing`.
- `TestAFiringResumeOrAPlatformLinkHoldsNoPersonsKey` (app, acts_test, the plan's): an item holding a firing resume keyed `sha256(firing:e1)` and a flap link keyed `sha256(flap:e1)`; a close under the literal key `flap:e1` is not `ErrKeyReused`, and an extension's repeat answer skips the firing resume.
- `TestAnAwaitingItemWhoseTriggerFiresArmsNoAutoClose` (app, autoclose_test): an item whose records end awaiting its close with an instance firing, taken over, arms no timer, and its view gives no closing time; once a resolution is appended and the item advanced, the timer is armed at the resolution plus the delay.
- Changed: `TestAutoCloseCountsFromTheLatestEntryIntoAwaitingClose` and `TestAnItemOpenedByAPersonClosesByItselfFromItsEntryIntoAwaitingClose` read the pair; the interleaving invariants' four reads take `ok`.

- [ ] **Step 1: Write the tests.** `go test -race -count=1 ./internal/work/item/ ./internal/work/app/`. Expected: FAIL (build, then each reason).
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run the tests.** Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./internal/web/...`. Expected: `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/item internal/work/app`; `git commit -m "feat(item): item_triggered, an opening's trigger, a firing resume's cause, and the trigger's resolution in the fold"`.

### Task 3: The mapping's `routing`, the evaluator and its redaction

**Design:** D3; review I6, M3, M7; re-review N3; rulings 3 and 12; plan review I10. **Rulings:** R118, R122, R130, R134.

**Files:**
- Modify: `internal/registry/config/kinds.go` (`TriggerMappingSpec.Routing map[string]string \`yaml:"routing"\``), `mapping.go` (its checks), `copy.go` (`maps.Clone`), `mapping_test.go`
- Create: `internal/inbound/mapping/mapping.go`, `internal/inbound/mapping/mapping_test.go`
- Modify: `examples/config/trigger-mappings/alerts.yaml` (`routing`, `eventId`, R130); `cmd/agentswarm/validate_test.go` only if it pins the example's text

**Interfaces:**
- Consumes: `config.TriggerMapping` and its helpers (mapping.go:18-47); `selector.Parse`/`Select`; `tmpl.Parse`/`Render`; `redact.Redactor` (`Text`, `Escaped`, `Value`, `EscapedValue`); `config.Unseen`.
- Produces:

```go
// package mapping (internal/inbound/mapping)
const (
	MaxEvents       = 500
	MaxPayloadBytes = 16 << 10
	Skew            = 2 * time.Minute
)
var (
	ErrBody          = errors.New("mapping: the body is not one JSON document")
	ErrTooManyEvents = errors.New("mapping: the body splits into more events than the limit")
)

// Event is one normalized inbound event (spec 3.6), as stored and ingested.
type Event struct {
	Source, Mapping, Owner, Type string        // the mapping's source, metadata.name, metadata.owner, workItemType
	Within                       time.Duration // the correlation window; 0 without correlate
	EventID                      string
	InstanceID, DedupKey, CorrelationKey string
	Status                       item.TriggerStatus
	Occurred, Clamped            time.Time
	Title                        string
	Routing                      map[string]string
	Payload                      string
	Unroutable                   string // a reason, or ""
	Refused                      string // checks_unsupported, or ""
}

// Redactor is the rules that apply always, then the mapping's redact rules.
func Redactor(rev *config.Revision, m *config.TriggerMapping) *redact.Redactor
func Evaluate(m *config.TriggerMapping, body []byte, received time.Time, r *redact.Redactor) ([]Event, error)
```

**The loader:** `routing` holds at most 16 entries; each key is 1 to 64 runes with no character `config.Unseen` reports; each value is one of `trustedFields`. Problems: `spec.routing` "holds at most 16 keys"; `spec.routing.<key>` "names <value>, which is not a trusted field".

**The evaluator, in order, per element after the split** (`split` absent: the document is one element):
1. Decode with `json.Decoder.UseNumber` (`ErrBody` otherwise). A split selecting more than `MaxEvents` fails `ErrTooManyEvents` before any element is evaluated.
2. Trusted values: for each trusted field path, its value in the element: a string as is, a `json.Number` or a boolean as its JSON text, anything else absent.
3. The event id: the `eventId` template rendered over trusted values; absent or lacking a value (R122), derived:

```go
h := sha256.New()
write := func(s string) { h.Write([]byte(s)); h.Write([]byte{0}) }
write(m.Spec.Source)
for _, name := range slices.Sorted(maps.Keys(trusted)) { write(name); write(trusted[name]) }
write(string(status))                    // "" when unmapped
write(occurred.UTC().Format(time.RFC3339Nano)) // unclamped (review M3); zero when unparsed
eventID := hex.EncodeToString(h.Sum(nil))
```

   If the id is empty, longer than 256 runes, or holds a character `config.Unseen` reports, the event is `Unroutable: "field_invalid:eventId"` (R134); if `r.Text(id) != id` or `r.Escaped(id)`, it is `Unroutable: "id_redacted"`; either way its `EventID` is `"sha256:"` and the hex SHA-256 of the raw id, and nothing else is kept (N3, ruling 12). A derived id (64 hex characters) always passes the form check.
4. Status: `status.value` rendered; missing, `missing:status`; mapped through `FiringValues`/`ResolvedValues`; otherwise `status_unknown`.
5. `occurredAt.firing` or `.resolved` rendered; missing, `missing:occurredAt`; not RFC 3339, `time_unparsed`. `Clamped` is the earlier of `Occurred` and `received + Skew`.
6. `instanceId`, `dedupKey`, a declared `correlate.key` and each `routing` entry, rendered or looked up; missing, `missing:instanceId`, `missing:dedupKey`, `missing:correlate.key`, `missing:routing.<key>`. The first missing field in this order names the reason.
7. The instance, the dedup key and a declared correlation key are each 1 to 256 runes with no character `config.Unseen` reports, else `Unroutable: "field_invalid:instanceId"`, `"field_invalid:dedupKey"` or `"field_invalid:correlate.key"` (R134), with the id and reason kept. Then, if redaction would change, or an escape could hide a match in, the instance, a key or a routing value, `Unroutable: "routing_refused"` and only the id and reason are kept.
8. The title: the template rendered over every field of the element, line breaks and tabs folded to spaces (7b-1's carry), `[withheld]` when `r.Escaped`, else `r.Text`.
9. The payload: `r.Value(element)`; `[withheld]` when `r.EscapedValue`; else its JSON, cut to `MaxPayloadBytes` at a rune boundary with "…" (R118).
10. A mapping with `checks` marks every event `Refused: "checks_unsupported"` (ruling 3), after 3 to 9.

**Tests (write first):**
- `TestAMappingsRoutingNamesTrustedFields` (config): a value that is not a trusted field, an empty key, a key of 65 runes, a key with a tab, and 17 entries each give a problem; a valid map gives none; a revision's mapping is a copy whose `Routing` the caller cannot change.
- `TestTheExampleMappingLoads` (config or cmd, the plan's): `examples/config` loads with no problem and its mapping routes `service`. Guard after the edit.
- `TestAGroupSplitsIntoOneEventPerElement` (mapping): a body of three alerts under `$.alerts[*]` gives three events in order; with no `split`, one.
- `TestOnlyTrustedFieldsFillTheRoutedTemplates`: a trusted number renders as its JSON text (`5` not `5.0`), a boolean as `true`, an object is absent (`missing:dedupKey`); an untrusted field of the same element never fills a routed template; the title may use it.
- `TestStatusIsMappedThroughItsLists`: `firing`, `resolved`, a configured `active`; an unknown value is `status_unknown`.
- `TestOccurredAtIsClampedButTheIDKeepsTheParsedTime`: an event dated a day ahead has `Clamped` = received + 2 min and `Occurred` the parsed time; evaluated at two receipt times, its derived id is the same.
- `TestAnAbsentEventIDIsDerivedFromTheTrustedFields`: a fixed input gives a fixed hex id (the vector computed once and pinned); the element's keys reordered give the same id; another status gives another.
- `TestAnEventIDTheRulesWouldChangeMakesTheEventUnroutable`: an id matching an always-rule gives `id_redacted`, `EventID` `sha256:` of the raw id, every other field empty.
- `TestAMissingRoutedFieldMakesTheEventUnroutable` (table): each `missing:` reason above, and `time_unparsed`.
- `TestAnIDOrKeyOutOfFormIsUnroutable` (I10, R134, table): an event id of 257 runes, or with a line feed, gives `field_invalid:eventId` stored under `sha256:` of the raw id with nothing else; an instance with a zero-width space, a dedup key of 257 runes and a correlation key with a tab give `field_invalid:instanceId`, `field_invalid:dedupKey` and `field_invalid:correlate.key`, with no key columns; each at exactly 256 runes passes.
- `TestARoutedValueTheRulesWouldChangeIsRefusedWithoutItsValue`: a trusted `labels.service` of `password=hunter2` gives `routing_refused`, with no instance, key or routing kept.
- `TestStoredTextIsRedactedFirstAndCutAfter`: a payload member `"password": "x"` is redacted; a match straddling the 16 KiB boundary is redacted before the cut; a payload with an escaped secret is `[withheld]`; a title with `\n` and `\t` reads with spaces; the mapping's own `redact` rule applies.
- `TestAMappingWithChecksRefusesItsEvents`: every event is `Refused: "checks_unsupported"` with its id kept.
- `TestABodyOverTheEventLimitOrNotJSONIsAnError`: 501 elements fail `ErrTooManyEvents`, 500 do not; `{` fails `ErrBody`.

- [ ] **Step 1: Write the tests.** `go test -race -count=1 ./internal/registry/... ./internal/inbound/... ./cmd/agentswarm/ -run 'Mapping|Example|Validate'`. Expected: FAIL.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run the tests.** Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/registry/... ./internal/inbound/... ./cmd/agentswarm/...`. Expected: `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/registry/config internal/inbound/mapping examples/config/trigger-mappings/alerts.yaml` (and `cmd/agentswarm/validate_test.go` if changed); `git commit -m "feat(inbound): the trigger mapping's routing, and the evaluator with its redaction"`.

### Task 4: HMAC signatures and rollover

**Design:** D2. **Rulings:** R123, R124.

**Files:**
- Create: `internal/inbound/signature.go`, `internal/inbound/signature_test.go`

**Interfaces:**
- Produces:

```go
// package inbound
const (
	HeaderTimestamp = "X-Agentswarm-Timestamp"
	HeaderSignature = "X-Agentswarm-Signature"
)
var (
	ErrBadSignature = errors.New("inbound: the request is not signed by a current secret")
	ErrStale        = errors.New("inbound: the request's timestamp is outside its maximum age")
	ErrSecretFile   = errors.New("inbound: the secret file does not hold one or two secrets")
)
// Secrets parses a secret file's text, as files.Store.Secret returns it.
func Secrets(text string) ([]string, error)
// Verify checks h's signature over "<timestamp>.<body>" against secrets, then
// the timestamp against now, within maxAge either way.
func Verify(secrets []string, h http.Header, body []byte, now time.Time, maxAge time.Duration) error
```

**The algorithm:** the timestamp header matches `^[0-9]{1,12}$`; the signature header splits on `,`, each part trimmed; a part `v1=` followed by 64 lowercase hex characters is a candidate, any other part ignored; no candidate is `ErrBadSignature`. For each secret, `mac := hmac.New(sha256.New, []byte(secret))` over `timestamp + "." + body`; a candidate matches when `hmac.Equal(decoded, mac)`; every pair is compared (no early exit by secret order). No match is `ErrBadSignature`; then `|now - time.Unix(ts, 0)| > maxAge` is `ErrStale`. Errors never hold a header, a body or a secret.

**Tests (write first):**
- `TestASignatureOverTheTimestampAndBodyIsAccepted`: a request signed with the secret at now verifies.
- `TestAnySignatureListedMayMatchAnyCurrentSecret`: with secrets `[new, old]`, a header `v1=<by old>`, one `v1=<garbage>,v1=<by new>`, and one with a `v0=` part before a good `v1=` each verify.
- `TestABadOrMissingSignatureIsRefused` (table): no headers; a timestamp `12a`; a signature over another body; uppercase hex; only `v0=`; 63 hex characters: each `ErrBadSignature`.
- `TestATimestampOutsideMaxAgeEitherWayIsStale`: a good signature at now − 5 min − 1 s and now + 5 min + 1 s is `ErrStale`; at exactly 5 min either way it verifies.
- `TestTheSecretFileHoldsOneOrTwoValidLines` (table): `a` and `a\r\nb` parse; `""`, `a\n\nb`, `a\nb\nc`, `a b`, `a\x00` fail `ErrSecretFile`; the error never holds the text.

- [ ] **Step 1–3.** `go test -race -count=1 ./internal/inbound/`: FAIL, then PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/inbound/...`. Expected: `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/inbound/signature.go internal/inbound/signature_test.go`; `git commit -m "feat(inbound): HMAC signatures over a timestamp, with two secrets during rollover"`.

### Task 5: The source actor, `openLocked` and the policy

**Design:** D6; D7's lock (review M10); D8's one append (review I1); 9a-1's carry (R78, m10); plan review I1, I2, I4, M5. **Rulings:** R116, R121, R126, R131.

**Files:**
- Modify: `internal/work/app/acts.go` (`Actor.Source`), `policy.go` (`May`), `coordinator.go` (`Open` split into `Open` and `openLocked`; `checkOpener`)
- Modify: `internal/work/app/policy_test.go`, `coordinator_test.go`, `export_test.go` (`OpenLockedForTest`, which takes the lock and calls `openLocked`)

**Interfaces:**
- Consumes: `Open`'s body (coordinator.go:312-375), `checkOpener` (:1421), `record`, `unrecorded`.
- Produces:

```go
// Actor gains:
//   Source says the actor is an inbound source (design D6): it opens items,
//   and nothing else. Its subject is "source:<name>".
Source bool

// OpenInput gains:
//   Trigger is an inbound opening's trigger, recorded as Opened.Trigger
//   (R126). Only a source actor gives one (R116).
Trigger *item.Trigger
//   Triggered is the opening's own item_triggered, applied and firing, of
//   Trigger's source and dedup key, which follows item_opened in its append.
//   Set exactly when Trigger is.
Triggered *item.Triggered
//   FlapOf is the closed item a flap's opening is linked to (D8): the
//   opening's append then holds item_linked into Incident, third (R126).
FlapOf *Flap
//   Row is the inbound row that commits with the opening's append (D4).
Row *InboundEvent

// Flap names the closed item whose incident a flap's new item joins (D8).
type Flap struct {
	Item     item.ID
	Incident item.IncidentID
	EventID  string // for the link's request (R120)
}

// openLocked is Open under the lock, with the holding checked and ctx bound
// to the epoch. With Trigger set it writes, in one append through
// recordInbound (Task 6) and in R126's order: item_opened with
// Opened.Trigger = in.Trigger; in.Triggered as item_triggered; item_linked{Left: the new
// incident, With: FlapOf.Item, By: the actor's subject, Reason: nil,
// Request: R120's} into FlapOf.Incident when FlapOf is set; then
// item_awaiting_close when the opening is not staffed; Row commits with them.
// Without Trigger it is Open's body today, through record.
func (c *Coordinator) openLocked(ctx context.Context, in *OpenInput) (item.ID, error)

// Sentinels, each wrapped with ErrInvalid (R131).
var (
	ErrTypeNotRunnable = errors.New("app: the type cannot run")          // runnable
	ErrRoutingRefused  = errors.New("app: the routing cannot be recorded") // checkRouting
	ErrOpenerRefused   = errors.New("app: the opener's checks refused it") // checkOpener, the source check
)
```

`item.Trigger` and `item.Triggered` are Task 2's types; the caller fills `Triggered` from the event, `Applied: true`, `Status: firing`; `openLocked` refuses `ErrInvalid` a `Triggered` whose source or dedup key differ from `Trigger`'s, or one without the other. A `repeated` hit for an opening with `Trigger` set answers `ErrKeyReused` (R121), never the item.

`May`: `ActOpen` admits `Responder || Admin || Source`; `ActSwitchOn` keeps `Responder || Admin` (its case is split from `ActOpen`'s); item acts are unchanged, so a source is refused `CodeNotItemResponder`, as any non-member. `checkOpener`: a source actor is exempt from the own-principal rule (coordinator.go:1448-1450), and names no principal and no responders (R116); an opening with a `Trigger` or a `FlapOf` whose actor is not a source is refused. `runnable`, `checkRouting`, `checkOpener` and the source check (coordinator.go:344) wrap R131's sentinels beside `ErrInvalid`. In Task 5 the trigger path records through `record` (no row); Task 6 records through `recordInbound` when `Row` is set. Without `Trigger`, `openLocked` is exactly `Open` today.

**Tests (write first):**
- `TestMayDecidesEveryActForEveryRole` (changed): rows for an actor `{Subject: "source:alerting", Source: true}`: `ActOpen` `Unrestricted`; `ActSwitchOn` `CodeResponderOnly`; `ActSwitchOff` `CodeAdminOnly`; each item act `CodeNotItemResponder`.
- `TestASourceOpensWithoutAPrincipal`: `Open` by the source actor with source `alerting`, no principal and no responders opens the item; its `item_opened` has `By` `source:alerting` and `Source` `alerting`.
- `TestASourceNamesNoPrincipalOrResponderUntilPlan7c2` (the plan's): the same with a principal, or a responder, is refused `ErrInvalid`.
- `TestAResponderActorOpensForThemselvesAndTurnsTheSwitchOn` and `TestAStrangerResponderCannotPromoteCloseOrReopen`: guards.
- `TestASourceCannotActOnTheItemItOpened` (the plan's): the source actor's close, promote, reopen, extend and link of its item are each refused `not_item_responder`.
- `TestAnInboundOpeningWritesItsRecordsInOneOrder` (the plan's, I1, I2; through `export_test.go`'s `OpenLockedForTest`): with `Trigger` set, records 1 and 2 are `item_opened` (its `Trigger` from the input) and `item_triggered`, in one append; with `FlapOf` too, record 3 is `item_linked` into `FlapOf.Incident`; with the kill switch on, the last is `item_awaiting_close`, records 1 to 4 by kind.
- `TestOnlyASourceGivesATrigger` (the plan's, I1): an admin's or a responder's opening with a `Trigger` or a `FlapOf` is refused `ErrInvalid`; a source's with a `Trigger` and no `Triggered`, or mismatched ones, too.
- `TestAnInboundOpeningOfAnIDTakenByAKeyIsKeyReused` (the plan's, M5): an admin opened `ItemID(tenant, "inbound:alerting:e1")` by key; the source's opening of that id answers `ErrKeyReused`, whatever the digest.
- `TestEachOpeningRefusalNamesItsSentinel` (the plan's, I4, table): a type whose key reads at open is `ErrTypeNotRunnable`; routing of 17 keys `ErrRoutingRefused`; a responder naming another principal `ErrOpenerRefused`; each `errors.Is(err, ErrInvalid)` too.

- [ ] **Step 1–3.** `go test -race -count=1 ./internal/work/app/ -run 'May|Source|Responder|Stranger|Open'`: FAIL, then PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./internal/admin/... ./internal/web/...`. Expected: `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app`; `git commit -m "feat(work): the source actor, which opens items and does nothing else, and openLocked"`.

### Task 6: `Ingest`: order, match, append, unmatched, refused, and the resolution at round end

**Design:** D7 steps 1 to 4, 5's plain append, 6 and 8; D8's opening (without the link, Task 7); D9's error contract; D10; D12; reviews C1, C2, I1, I4, I8, M1, M4; plan review I1, I5, M6. **Rulings:** R109, R111, R121, R127, R131, R133.

**Files:**
- Create: `internal/work/app/ingest.go`, `internal/work/app/ingest_test.go`
- Modify: `internal/work/app/coordinator.go` (`Ports.Inbound`, `missing()`, `recordInbound` beside `record`), `rounds.go` (`ending()`, rounds.go:387-417, which builds the `item.Ending`, sets `TriggerResolved` from the state `endRound` loaded, passed in), `queue.go` (`refresh` clears the pending mark, R133), `harness_test.go` (`Inbound: h.records`; `failing.AppendInbound` and `failing.Put` share `failing`'s fault and load hooks), `interleave_world_test.go` (`Inbound` wired to the world's records, unfaulted until Task 10), `restart_test.go` (a takeover row)
- Modify: `cmd/agentswarm/serve.go` (`Inbound: st.records` in `app.Ports`, so the build stays green; no listener yet)

**Interfaces:**
- Consumes: `openLocked`, `OpenInput.Trigger`, `OpenInput.Row` and R131's sentinels (Task 5), `Records.AppendInbound` and `Inbound` (Task 1), the fold's queries (Task 2), `mapping.Event` (Task 3), `armAutoClose`, `advance`, `countAsStarting`, the account `c.items`, `c.retry` (coordinator.go:875), `unrecorded` (coordinator.go:413-438).
- Produces:

```go
type IngestOutcome string
const (
	IngestRepeat            IngestOutcome = "repeat"
	IngestOpened            IngestOutcome = "opened"
	IngestAppended          IngestOutcome = "appended"
	IngestLate              IngestOutcome = "late"
	IngestResolvedUnmatched IngestOutcome = "resolved_unmatched"
	IngestUnroutable        IngestOutcome = "unroutable"
	IngestRefused           IngestOutcome = "refused"
)
type IngestInput struct {
	Event *mapping.Event
	// ID is the item an opening by this event takes:
	// ItemID(tenant, "inbound:"+source+":"+eventID) (R121).
	ID item.ID
}
type IngestResult struct {
	Outcome IngestOutcome
	Reason  string
	Item    item.ID
}
// Ingest takes one event under the coordinator's lock, held through it. It
// answers ErrNotHeld when not held, or any store's error, an unknown
// outcome's included, with nothing more written: the caller answers 503 and
// the source retries (D9).
func (c *Coordinator) Ingest(ctx context.Context, in *IngestInput) (IngestResult, error)
```

**The order, under the lock** (the design's steps, made exact):
0. **Pending reloads (R133).** Each item pending reload of the event's source is loaded; a load that succeeds clears its mark; while one fails, the event answers the load's error (`503`), nothing written.
1. `Seen(source, eventID)`: true is `repeat`, nothing written. `ErrSeen` from any later write is also `repeat`.
2. `Unroutable` or `Refused`: `Put` with that outcome and reason (columns as the evaluator kept them).
3. `LastStatus(source, instance)`. The event is **late** when `Clamped` is before the last time, or equal to it unless the event is `resolved` and the last `firing`. A late event: among account items that `HoldsInstance(source, instance)`, the oldest takes `item_triggered{Applied: false}` with the row (`late`, its item); with none, `Put` as `late`. A late event opens nothing.
4. **Match**, among the account's items (`c.items`, none closed), the first rule that matches any item, ties to the oldest by the opening's time then the id: `HoldsInstance`; then `HoldsDedupKey`; then `HoldsCorrelationKey` with `OpenedAt` not before `now − Within` (`Within` zero skips it). A person's item holds no key (review M4).
5. **Matched:** `recordInbound` appends `item_triggered{Applied: true}` with the row (`appended`). Task 7 adds the firing resume here. Then the item is advanced, which re-arms or cancels its auto-close (D5). An append that fails with an outcome that may have landed (any error but the guard's refusal and a decide's error) marks the item pending reload and arms `c.retry` (R133), then returns the error; the same holds in step 3's late append and step 7's opening, whose `unrecorded` lookup, when it fails too, already arms the retry (coordinator.go:428-433).
6. **Unmatched, resolved:** `Put` as `resolved_unmatched`, applied.
7. **Unmatched, firing:** `LastItem(source, dedupKey)`, when it names an item, loads it: one that does not fold or is held by a later build is `refused` `item_unreadable` (review M1); one that folds, is not closed and was not in the account (an opening whose answer and lookup were lost) enters the account through the load, and step 4's match runs again, so the event is matched, never opened twice (R133); a closed one is Task 7's flap. Otherwise `openLocked` with `ID: in.ID`, `Digest: OpeningDigest`, the event's type, title, routing and source, `Actor{Subject: "source:"+source, Source: true}`, `Trigger` `{Source, DedupKey, CorrelationKey}`, `Triggered` the applied firing `item_triggered` of the event, and `Row` the `opened` row. Refusals map as R131 by `errors.Is`; `ErrNoRevision` and store errors are returned.
- `recordInbound(ctx, s, row, recs...)` is `record` through `AppendInbound`; `record` and `recordInbound` share one body.
- **D12:** `ending()` sets `TriggerResolved` to `s.TriggerResolvedSince()` of the state `endRound` loaded (rounds.go:358), for a run whose creation says primary and visible; `decide`'s order already puts it after the cap and before `not_staffed` (decide.go:158-170).

**Tests (write first), with the harness's memory stores:**
- `TestARepeatedEventWritesNothing`: the same event twice: `appended`, then `repeat`, with one record and one row.
- `TestAnUnroutableOrRefusedEventIsRecordedAndOpensNothing`: `missing:instanceId` and `checks_unsupported` each give one row of their outcome and reason, no item.
- `TestAnEventNotLaterThanItsInstancesLastStatusIsLate` (table): an older firing with an item holding the instance is appended `Applied: false`, outcome `late` with the item; with none, a `late` row; a firing at the time of the last resolved is late; a resolved at the time of the last firing applies.
- `TestARetriedOldFiringOpensNoZombieItem` (review C2): `resolved_unmatched` of `i` at t2, then its firing at t1: `late`, no item.
- `TestAnEventMatchesByInstanceThenDedupKeyThenCorrelationKey` (table): an item holding the instance wins over an older one holding the dedup key; with two items holding the key, the older; correlation only within the window.
- `TestACorrelatedAlertsResolutionStillFindsItsItem` (review C1): A opens X; B correlates into X at +10 min; B's resolution at +60 min is `appended` to X, never `resolved_unmatched`; B's next firing matches X by dedup key.
- `TestAPersonsItemTakesNoInboundEvent`: a person's item routed `service=cart` and a firing routed the same: a new item opens.
- `TestAnUnmatchedResolutionIsRecordedAndOpensNothing`: one applied `resolved_unmatched` row; a later older firing of that instance is late.
- `TestAnUnmatchedFiringOpensAnItemUnderTheSourceActor`: the item id is `ItemID(tenant, "inbound:alerting:<id>")`; records 1 and 2 are `item_opened` (with `Trigger`, `By` `source:alerting`, the mapping's routing) and `item_triggered`, in one append, with the `opened` row; with the kill switch on, records 1 to 3 are `item_opened`, `item_triggered`, `item_awaiting_close`, in one append; past the limit it waits queued.
- `TestTwoEventsOfOneKeyOpenOneItem` (review M10): two `Ingest` calls at once, one dedup key, two event ids: one `opened`, one `appended`, one item.
- `TestAnOpeningRefusedByOpenIsRecordedWithItsReason` (review I4, table): a type whose key reads at open gives `type_not_runnable`; a routing value of 257 runes gives `routing_refused`; the next event of the request is taken.
- `TestAnItemOutsideTheAccountIsNotOpenedAgain` (review M1): `LastItem` names a not-closed item whose records do not fold: `refused` `item_unreadable`, no new item.
- `TestAnEventWhoseAppendFailsIsTriedAgainWhole` (review I1): an `AppendInbound` that fails before its commit returns the error, with no record and no row; the retry is `appended` once. One whose answer is lost after its commit returns the error; the retry is `repeat`.
- `TestAnEventCommittedBeforeATakeoverIsARepeatOnTheNewLeader` (restart_test, Review Focus 4): an event committed with its answer lost, then a takeover by another coordinator over the same stores: the retry there is `repeat`, and the item holds one `item_triggered`.
- `TestTwoSourcesWithTheSameInstanceIDAreKeptApart` (Review Focus 3): instance `i` from `alerting` and from `paging`: neither's order, match or last status reads the other's.
- `TestAFutureDatedFiringIsClampedAndItsResolutionStillApplies` (Review Focus 2): a firing dated an hour ahead is stored at receipt + 2 min; a resolved dated receipt + 5 min applies; one dated receipt + 1 min is late.
- `TestATriggerResolvedAfterTheRunStartedStopsItAtTheRoundsEnd` (D12): the visible run's round ends `trigger_resolved`; a run whose trigger resolved before `run_created` runs to its budget (ruling 10); a person's extension of the stopped run runs a round that does not end `trigger_resolved`.
- `TestTheDailyCapStopsARoundBeforeATriggersResolution`: at the cap, `cut`.
- `TestEachMatchedEventRearmsTheAutoClose`: an item awaiting its close, resolved: its timer at the resolution plus the delay; a new firing cancels it.
- `TestIngestAnswersNotHeldOnAFencedCoordinator`.
- `TestAnOpeningWhoseLookupFailedIsMatchedNotOpenedAgain` (I5a): an inbound opening whose answer is lost and whose `unrecorded` lookup fails: `503`, the item pending reload; a second event of the same dedup key while the reload fails answers `503` and writes nothing; once the load succeeds (or, with the item out of the account, through `LastItem`'s load), it is `appended` to that item, and one item holds the key.
- `TestAnAppendWhoseAnswerWasLostIsReloadedBeforeTheNextMatch` (I5b): a correlated event's append lands with its answer lost (`503`, the item pending, its retry armed); the instance's resolution, sent past the correlation window, finds the item by instance after the reload and is `appended`, never `resolved_unmatched`.
- `TestAPendingItemsRetryClearsItsMark` (the plan's): the retry's advance loads the item, which clears the mark, and the next event reads no store for it.

- [ ] **Step 1: Write the tests.** `go test -race -count=1 ./internal/work/app/ -run 'Ingest|Event|Firing|Trigger|Correlat|Unmatched|Opening|Source|Takeover'`. Expected: FAIL.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run the tests.** Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./cmd/agentswarm/...`, the interleaving default run among them (within 20 s). Expected: `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app cmd/agentswarm/serve.go`; `git commit -m "feat(work): Ingest: order at the source level, match, append, the unmatched and the refused, and a resolution at the round's end"`.

### Task 7: The firing resume, and a flap after a close with its link

**Design:** D7 step 5 (the firing resume), D8; reviews I2 (removed by O1), I3, M5, M8, M9; re-review N1; rulings 1, 5 and 9; plan review I1, I2, I3. **Rulings:** R112, R119, R120, R126, R133. **Decision:** A7.

**Files:**
- Modify: `internal/work/app/ingest.go` (the resume in step 5; the flap in step 7), `extend.go` (an unexported `mayFiringResume(s) (code string, err error)`: an item with no run gives `no_run` (`CodeNotStarted`'s case of `mayExtend`); otherwise `mayExtend`, then `mayResume` with `s.Run.Budget()`; a `*Refused` gives its code, `ErrUnsettled` gives `unsettled`, any other error is returned), `rounds.go` (`resumeBoard` posts `LatestResumeCause`), `internal/work/board/state.go` (`resumeCauses` gains `firing`)
- Create: `internal/work/app/firing_test.go`; Modify: `internal/work/board/resume_test.go`

**The resume.** In step 5, when the event is an applied `firing`, the item awaits its close and its run is absent or stopped (review I3: an item opened not staffed has no run), and either `Instance(source, instance)` is `resolved` or not held, or `FiringResumeDeclined()`: `mayFiringResume` decides. Allowed: the append holds `item_triggered{Resume: "resumed"}` then `run_extended{Amount: zero, Cause: firing, KillSwitchSeen: c.killSwitch.Seq, By: "source:"+source, Request: R120's}`, with the row; then the item advances, which posts the board's `resume` entry with cause `firing` and opens the next round. Refused: `item_triggered{Resume: "declined:<code>"}`, R112's codes, the item staying awaiting its close with its trigger firing. An append whose outcome is unknown calls `countAsStarting` (7b-2's carry), marks the item pending reload (R133) and returns the error. No wait for a slot (O1). A retry on re-notification works only where re-notifications carry new event ids (A7).

**The flap.** In step 7, when `LastItem` names a closed item P: `openLocked` with `FlapOf: &Flap{Item: P, Incident: P's incident, EventID}` beside `Trigger`, `Triggered` and `Row`, so the opening, its trigger and its link land in one append in R126's order (review I1), whatever P's close time (ruling 5); no `May`, no preset reason, no window.

**Tests (write first):**
- `TestAFiringWhileAwaitingTheCloseResumesTheRunAtOnce`: a run stopped `trigger_resolved`, then a new firing of the instance: `Resume` `resumed`; `run_extended` with zero amount, cause `firing`, `By` `source:alerting`, the kill switch's latest row; the item investigates; the board holds a `resume` entry with cause `firing`, then the next round.
- `TestAFiringResumeIsDeclinedWithItsCode` (table, each with the item left awaiting its close and no auto-close armed): kill switch on, `declined:kill_switch_on`; view unsettled, `declined:unsettled`; daily cap, `declined:daily_cap_reached`; no agents left, `declined:no_agents_left`; budget used, `declined:would_stop_at_once`; limit and one reached, `declined:limit_reached`; an item opened not staffed with no run, `declined:no_run` (ruling 9).
- `TestAReNotificationRetriesADeclinedResume` (N1): declined `limit_reached`; a slot frees; the next applied firing of the same instance, a later time and a new id, is `resumed`.
- `TestAReNotificationWithNoDeclineTriesNoResume`: an instance firing since before the stop, re-notified, with no decline since the stop: `Resume` empty, the run stays stopped.
- `TestAFiringResumeWhoseOutcomeIsUnknownCountsItsItem`: an `AppendInbound` whose answer and lookup are lost: the error is returned and `Lifecycle().Investigating` counts the item until its next load.
- `TestAFiringAfterTheCloseOpensANewItemLinkedInOneAppend`: P closed 100 hours ago (past the 72-hour rulings window); a firing of its dedup key opens N whose records 1 to 3 are `item_opened`, `item_triggered` and `item_linked` (`By` `source:alerting`, `Reason` nil, `Request.Key` `sha256:` of `flap:<id>`), N's incident is P's, and `IncidentItems` names both.
- `TestAFlapOpenedNotStaffedStillHoldsItsLink`: with the kill switch on, records 1 to 4 are `item_opened`, `item_triggered`, `item_linked`, `item_awaiting_close`, in one append.
- `TestAResumeEntryTakesTheFiringCause` (board): `Resume` with cause `firing` posts; `other` is refused as today.

- [ ] **Step 1–3.** `go test -race -count=1 ./internal/work/app/ ./internal/work/board/ -run 'Firing|Flap|Resume|ReNotification'`: FAIL, then PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/...`, the interleaving default run among them. Expected: `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app internal/work/board`; `git commit -m "feat(work): a firing resumes a stopped run at once or is declined with its code, and a firing after a close opens a linked item"`.

### Task 8: Trigger evidence on running boards, and `scope()` refusing `other`

**Design:** D11 (re-review N2), as A6 (c) carries it; D13 (Plan 5b:14780, 7b-1's carry); plan review I6. **Decision:** A6 (recommended (c); (a) the alternative, which changes this task alone).

**Files:**
- Modify: `internal/work/app/ingest.go` (the post after each commit; `triggerUpdateClaim`), `internal/work/app/ingest_test.go`
- Modify: `internal/work/board/view_test.go` (a test that agents see it; no change to `view.go`)
- Modify: `internal/actions/app/write.go` (`scope`), `internal/actions/app/write_test.go`

**Interfaces:**
- Consumes: `Boards.PostPlatformEvidence` (internal/work/app/boards.go:170), `board.PlatformEvidenceInput{Claim, Ref, Source}`, `always` and `cut` (coordinator.go:1338, :1508), `board.MaxClaimRunes`; the revision the item locked (`c.p.Revisions.At`), or the current one.
- Produces:

```go
// triggerUpdateClaim is the evidence a trigger update posts, composed by rule
// as triggerClaim composes the opening's (coordinator.go:1492-1505):
// "Trigger <status> from <source>: instance <instance> at <RFC 3339 UTC>"
// then "; late, not applied" for a late event and "; the trigger is resolved"
// once no instance fires, then ": " and the event's title; redacted with the
// rules that apply always, trimmed, cut to board.MaxClaimRunes; a claim whose
// escapes may hide what a rule removes is replaced whole by the same text
// without its title.
func triggerUpdateClaim(rev *config.Revision, t *item.Triggered, title string, resolved bool) string
```

After each commit of an `item_triggered` (matched, late or a firing resume), `Ingest` posts `PlatformEvidenceInput{Claim: triggerUpdateClaim(...), Ref: "trigger:" + eventID, Source: board.Source(source)}` to the item's run when the run is running (`s.Running()`), primary and visible; the board repeats a post of the same `Ref` (board/round.go:390-395), so a retry posts once. A firing resume's evidence comes after the advance that posts the `resume` entry and the round's start, since a stopped board takes nothing but rulings (`board.ErrStopped`). A failed post is a Warn line naming the item and the event id, and the event's outcome stands (N2). An item with no run, or a stopped one, gets none. Platform evidence is shown in every agent's view and snapshot (board/view.go:80-85 hides only other agents' notices), as the opening claim is (rounds.go:35). The title is the event's, redacted by the evaluator (Task 3) and again by the always-rules here.

`scope()` (write.go:276): a binding that resolves to `key.Other` refuses the call with "<arg> is outside this action's scope: the cited hypothesis names no value for it", before the comparison.

**Tests (write first):**
- `TestATriggerUpdateClaimIsComposedByRuleAndRedacted` (app, table): a firing, a late resolved and the last resolution give the claim's three forms; a title holding `password=x` is redacted; a title whose escape hides a match drops the title; a long title is cut to `MaxClaimRunes`.
- `TestEachTriggerUpdatePostsEvidenceToARunningBoard` (app): an applied correlated firing, a late event and the last resolution each post one platform evidence entry, `Ref` `trigger:<id>`, `Source` the inbound source; a retried event posts nothing more.
- `TestAnAgentSeesTriggerEvidence` (board, view_test, the plan's, I6): an agent's view of a board holding a trigger update's platform evidence shows it, as it shows the opening claim. Guard: passes at Step 1, pinning the reason (c) was chosen.
- `TestAStoppedOrMissingBoardGetsNoTriggerEvidence` (app): a queued item and an item awaiting its close get none.
- `TestAFailedTriggerPostHoldsNothing` (app): a board store that fails: the outcome is `appended`, one Warn line, no error.
- `TestAFiringResumesEvidenceFollowsItsResumeEntry` (app): the board holds the `resume` entry, the round's start, then the evidence.
- `TestAScopeBindingThatResolvesToOtherIsRefused` (actions): a hypothesis whose key value is `other` for the bound argument: the call is refused with the sentence above and records no proposal. Fails first: today the argument `"other"` passes.

- [ ] **Step 1–3.** `go test -race -count=1 ./internal/work/board/ ./internal/work/app/ ./internal/actions/app/ -run 'Trigger|Evidence|Scope'`: FAIL, then PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/work/... ./internal/actions/... ./cmd/agentswarm/ -run Conclude`. Expected: `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/board/view_test.go internal/work/app internal/actions/app`; `git commit -m "feat(work): trigger updates as platform evidence on running boards after the commit, and a write's scope binding to other refused"`.

### Task 9: The listener, the handler, the deployment and serve

**Design:** D1, D2's answers, D9, D10's owner line; re-review N3, N6; Contract C2 (Plan 7d-1); plan review M2, M4, M8, M10. **Rulings:** R113, R114, R115, R117, R121, R124, R132.

**Files:**
- Create: `internal/inbound/handler.go` (`NewHandler`, the routes), `internal/inbound/handler_test.go`
- Modify: `internal/registry/config/mapping.go` and `mapping_test.go` (R132: `spec.source` of `Open`'s form)
- Modify: `internal/deploy/deploy.go` (`Listeners.Inbound`, its loopback check), `deploy_test.go`
- Modify: `cmd/agentswarm/listeners.go` (`listeners.inbound`, bound only with its key, under either runner; `addresses.Inbound \`json:"inbound,omitempty"\``), `serve.go` (`newServers` gains the inbound handler and makes `servers.inbound` with its `server` closure, R113; `serve`'s loop and `close`'s list; `worstDrain`), `serve_test.go`, `serve_integration_test.go` (`TestServeStartedNamesTheWorstDrain`'s `"1m27s"` becomes `"1m37s"`, lines 246-260)
- Modify: `examples/deployment.yaml` (a commented `inbound` key)

**Interfaces:**

```go
// package inbound
type Coordinator interface {
	Ingest(ctx context.Context, in *app.IngestInput) (app.IngestResult, error)
}
type Leader interface{ Status() (epoch int64, held, draining bool) }
type Revisions interface{ Current() *config.Revision }
type Secrets interface{ Secret(ctx context.Context, ref string) (string, error) }
type Ports struct {
	Coordinator Coordinator
	Leader      Leader
	Revisions   Revisions
	Secrets     Secrets
	Clock       func() time.Time // the database's time as the instance estimates it (clock.DB)
	Tenant      string
	Log         *slog.Logger
}
const MaxBody = 4 << 20
func NewHandler(p *Ports) http.Handler // serve's newServers wraps it in its bounded server (R113)
```

**The routes and their answers**, in the order they are checked; a refusal's body is `{"error":"<code>"}`, a `503` carries `Retry-After: 1`, nothing is recorded unless the row says so, and no log line or answer echoes the body:

| Request | Status | Code | Recorded | Log |
|---|---|---|---|---|
| `GET /healthz` | 200 | `ok` (text) | | Debug |
| any other path | 404 | `no_such_route` | | |
| another method on `/inbound/{source}` | 405, `Allow: POST` | `method_not_allowed` | | |
| no current revision | 503 | `no_revision` | | Warn |
| a source not of `^[a-z][a-z0-9-]{0,62}$`, or no mapping | 404 | `unknown_source` | | Warn naming a well-formed source |
| a bearer mapping | 400 | `scheme_unsupported` | | Warn naming the source |
| a body over `MaxBody`, never read whole | 413 | `too_large` | | Warn naming the source and the limit |
| the secret unreadable, absent or malformed | 503 | `secret_unavailable` | | Warn naming the source and the reference |
| no valid signature (R124) | 401 | `bad_signature` | | Warn naming the source |
| outside `maxAge` | 401 | `stale` | | Warn naming the source |
| the leader drains | 503 | `draining` | | |
| this instance does not hold | 503 | `not_held` | | |
| the body is not JSON | 400 | `bad_body` | one `refused` row, `sha256:` of the body, `body_unparsed` (R115) | Warn |
| more than 500 events | 413 | `too_many_events` | one `refused` row, `sha256:` of the body, `too_many_events` (N6) | Warn naming the source and the count |
| an event's `Ingest` fails with `ErrNotHeld` | 503 | `draining` or `not_held` | the events before it | |
| an event's `Ingest` fails otherwise | 503 | `unavailable` | the events before it | Warn |
| every event has an outcome | 200 | `{"opened":n,"appended":n,"late":n,"repeat":n,"resolved_unmatched":n,"unroutable":n,"refused":n}` | each event | Info with the counts; Warn per `unroutable` or `refused` naming the mapping, its owner, the event id and the reason (D10) |

Each event is ingested in order with `ID: app.ItemID(p.Tenant, "inbound:"+source+":"+eventID)`. The secret file is read at each request (spec 9.3). The handler logs R117's line, and the line that a mapping's rules made every event of a request unroutable (N3), once per mapping and commit. `deploy.Check` refuses a set `listeners.inbound` that is not on loopback ("the inbound listener is on loopback only, behind the deployment's TLS proxy"); an absent key gives no listener. Serve wires `inbound.Ports{Coordinator: coord, Leader: s.leader, Revisions: revs, Secrets: s.secrets, Clock: dbClock.Now, Tenant: d.Tenant, Log: s.log}`, serves it beside the others, and shuts it down with them, before the pools, within `stopWait`. `worstDrain` is `... + 8*stopWait + ...` (R114).

**Tests (write first):**
- `TestTheInboundServerIsBoundedInTime` (cmd, serve_test): `newServers` given an inbound handler makes `servers.inbound` with `ReadHeaderTimeout` 10 s and `ReadTimeout` 30 s, as the admin API's; given nil, `servers.inbound` is nil.
- `TestTheInboundRoutesAnswerAsTheTableSays` (inbound, table, one row per line of the table, with fake ports): status, code, `Retry-After`, the ingest calls made, and the rows asked.
- `TestARequestIsAcknowledgedOnlyOnceEveryEventHasAnOutcome`: three events whose second `Ingest` fails: `503`, the third never ingested.
- `TestAGroupRetriedAfterAFailureMidwayTakesEachEventOnce` (Review Focus 1, the plan's): against a real coordinator and memory stores, a group whose third event's append fails once: `503`; the same request again: `200` with the first two `repeat` and the rest taken; each event has one row.
- `TestAnOverLongBodyIsRefusedUnreadAndUnrecorded`: 4 MiB + 1 byte: `413`, no `Ingest`, no secret read.
- `TestTooManyEventsLeaveOneRefusedRow`: 501 events: `413` and one `Ingest` of a `refused` `too_many_events` event under `sha256:` of the body.
- `TestNoAnswerOrLogLineEchoesTheBody`: a body holding `password=hunter2` and a bad signature, and the same signed but unroutable: neither the answers nor the log hold `hunter2`, the signature or the secret.
- `TestASecretRolledOverBetweenRequestsIsReadAtEachRequest` (Review Focus 5, the plan's): a request signed with the old secret passes while the file holds `new\nold`, and is refused `bad_signature` once the file holds `new` alone, with no restart.
- `TestTheOwnerIsToldOfAnUnroutableEventInALogLine`: one Warn line naming the mapping, its owner, the event id and `missing:instanceId`.
- `TestTheUnappliedReadsAreWarnedOncePerMappingAndRevision`: two requests under one commit log the line once; a new commit logs it again.
- `TestARequestWhoseEventsTheRulesAllRefuseIsWarnedOnce` (M10, N3): two requests whose every event is `id_redacted` or `routing_refused` under one mapping and commit log the line once, naming the mapping, never a value; a request with one routable event logs none.
- `TestAMappingSourceTakesOpensForm` (config, M4, R132): `spec.source` `9am` is a problem; `alerting` and `a-1` are not.
- `TestTheInboundListenerIsOnLoopbackWhenSet` (deploy): `0.0.0.0:7404` is refused; `127.0.0.1:0` and an absent key are taken.
- `TestTheInboundListenerIsBoundOnlyWithItsKey` (cmd): with the key, `listen` binds it and the addresses file names `inbound`; without, it has no `inbound` key.
- `TestTheWorstDrainCountsEachTerm` (cmd, serve_test.go:539, changed): its constant `worst` becomes 1m37s, and the takeWait row's close term 40 s (eight `stopWait`); `TestServeStartedNamesTheWorstDrain` (integration, changed): `"1m37s"`.

- [ ] **Step 1–3.** `go test -race -count=1 ./internal/inbound/... ./internal/deploy/... ./cmd/agentswarm/...`: FAIL, then PASS.
- [ ] **Step 4: Gate.** `make lint`; `go test -race -count=1 ./internal/inbound/... ./internal/deploy/... ./internal/work/... ./cmd/agentswarm/...`; `go test -race -count=1 -tags integration ./cmd/agentswarm/`. Expected: `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/inbound internal/deploy internal/registry/config cmd/agentswarm examples/deployment.yaml`; `git commit -m "feat(serve): the inbound listener, its handler, and listeners.inbound on loopback"`.

### Task 10: The interleaving extension and the mutation check

**Design:** D14; 7c-1a's carried note on the interleaving test; plan review I7, I8, I9, M7. **Decision:** A3'' (timebox 5 h or $4). **Rulings:** R128, R133, R135.

**Files:**
- Modify: `internal/work/app/interleave_world_test.go` (an inbound store fault, `storeInbound`, for the lone `Put`; `AppendInbound` is one commit and spends the records' write fault, `storeRecords`, as `Append` does, never both (M7); read faults for `Seen`, `LastStatus` and `LastItem` through the existing `readFault` (line 885), which needs no change; `w.wrote(storeInbound, epoch)` per lone row), `interleave_test.go` (driver events), `interleave_invariants_test.go` (invariants 11 to 19, the changes to invariants 6, 8 and 9 below, and their must-pass rows)

**Driver events** (new `eventKinds`, total weight about 10, the world's sources `alerting` and `paging`, instances drawn from a small pool so they collide). **The key model (R135, I9):** each `(source, instance)` has one fixed dedup key and one fixed correlation key, as fingerprinting sources do, since a match by instance adds the event's dedup key to the item and two open items would otherwise share a key with no reopen, rightly. The events: a **firing** of a new or known instance; a **resolution** of a known instance; a **re-notification** (a firing of an instance already firing, a later time and a new id, which reaches N1's retry); a **repeat** (an event id sent before, re-sent); a **late** event (a known instance with an older time, a retried old firing among them); an **equal-time** event (a firing or resolution dated exactly the instance's last applied time, a quarter of the late draws, I8); a **correlated** second alert (a new instance, so a new dedup key, a known correlation key); a **flap** (a firing of a closed item's instance); and, with the existing faults, an `Ingest` whose append's answer is lost. **Every event whose `Ingest` returned an error is sent again** by the driver, whole, at its next inbound draw, until it gets an outcome, as a source re-sends after a `503` (I8). Each builds a `mapping.Event` directly and calls `in.c.Ingest` (R128); its `Clamped` is drawn around the world's database time, so clock steps reach the order rule.

**Changes to existing invariants** (I7, M7), each with rows proving no false positive and rows proving it still bites:
- **Invariant 6** (`inFlightFault`, interleave_invariants_test.go:310-321). `requestHeld` (line 1239), the test copy of `requestIn`, takes R120's exclusions: it returns nil for an `Extended` whose `Cause` is `firing` and for a `Linked` whose `By` starts with `source:`. An `item_opened` whose `Trigger` is set is exempt from the openings-in-flight check: an inbound opening is no person's keyed opening, and invariants 12 and 18 judge it. Rows: a firing resume's `run_extended` and a flap's `item_linked`, keys in flight under no act, do not break 6; an inbound opening's digest not in `openingsInFlight` does not break 6; a person's extension under a key not in flight still breaks 6; a person's keyed opening with another digest still breaks 6.
- **Invariant 9** (`admittedByTheWalk` and `admission`, lines 1522-1527 and 1577-1580). The queue exit of an item is the first record after the records of its opening's append, not `records[1]`: an inbound opening's `item_triggered` and a flap's `item_linked` land in that append (R126) and are no exit. Rows: an inbound item admitted by the walk in its order passes; an inbound item started ahead of an older waiting item breaks 9 (before the change it was silently never judged); a person's item is judged as before.
- **Invariant 8** (line 1421). `AutoCloseAt`'s pair: an automatic close recorded while the before-state's `ok` is false breaks 8. Row: a close due under `ok` true passes; one recorded while firing breaks 8 (and 13).

**New oracles** (each a numbered invariant with must-pass rows in `TestEachInterleavingInvariantCatchesItsViolation`, a row breaking it and a row it must allow):
- **11.** An instance's last applied time never decreases at the source level: per `(source, instance)`, applied rows in insert order have non-decreasing `occurred_at`, equal only for a `resolved` after a `firing`.
- **12.** No item is opened by an event not later than its instance's last known status at its insert.
- **13.** An item whose trigger fires never closes automatically (judged from its `item_triggered` records, not the fold's own refusal).
- **14.** No `resolved_unmatched` row for an instance that an item not closed holds firing when the row is written, judged on the world's committed records, whether the item is in the writing instance's account or not (R133).
- **15.** Every flap item's opening append holds its `item_linked` into the incident of the item `LastItem` named.
- **16.** A firing resume is resumed or declined, never pending: every applied firing on an item awaiting its close whose run is absent or stopped (I3), whose instance was resolved or new or whose latest resume since the stop was declined, carries a `Resume`; an item with no run carries `declined:no_run`; `resumed` is followed in its append by `run_extended` with cause `firing`.
- **17.** Two items not closed share a dedup key only when a person's reopen made it so (review M4).
- **18.** One event, one effect: each `(source, eventId)` has at most one row and at most one `item_triggered` across all items.
- **19.** An un-faulted `Ingest` never errors (I8): an `Ingest` called on the leading instance while no write or read fault is armed, no lease loss is pending, no drain is under way and no item of its source is pending reload, returns an outcome, never an error. This catches any disagreement between the fold and the coordinator, which would make an event fail on every retry.

Invariant 5 counts `storeInbound`'s writes as it counts every guarded store's, so a lone row written under an epoch that no longer holds breaks it.

**The mutation check, before the soak** (I8). Each mutant is a temporary patch in the scratch workspace, never committed; the default run, then a 5-minute targeted run (`-interleave.for=5m`) if the default misses, must break the named invariant. The ledger records each mutant, the invariant that broke and its seed. Every mutant below is reachable by the driver as specified; none is equivalent to the code:
1. **C1:** match by the opening's keys only (`Opened.Trigger`), ignoring keys appended since: 14 or 17.
2. **C2:** order per item (the item's `Instance` in place of `LastStatus`): 12 and 11.
3. **A split transaction:** the row written by `Put` after a plain `Append`, so a lost answer between them leaves records with no row: 18, through the driver's re-send.
4. **The tie flipped:** at equal time, `firing` wins: 11, through the equal-time draws.
5. **The flap link in a second append** after the opening: 15.
6. **Auto-close while firing:** `AutoCloseAt` ignoring the trigger and the fold's refusal removed: 13 and 8.
7. **A pending resume:** a declined resume recorded with an empty `Resume`: 16.
8. **The flap ignores `LastItem`:** a firing of a closed item's key opens an unlinked item: 15.
9. **`FiringResumeDeclined` ignored:** a re-notification after a decline tries no resume: 16, through the re-notification draws.
10. **A late event applied:** a late event appended `Applied: true`, which R125's fold refuses, so the event errors on every re-send: 19.
11. **No reload of a pending item (R133 skipped):** an append whose answer was lost leaves the account stale: 14 or 17 under lost answers.

At least eight must be caught for the task to be done; an uncaught one past the timebox is a residual (A3'').

**Tests:** the existing `TestRandomSchedulesKeepTheLifecyclesInvariants`, `TestConcurrentSchedulesKeepTheLifecyclesInvariants` and `TestASeedReplaysItsSchedule` (guards, extended); `TestEachInterleavingInvariantCatchesItsViolation` gains rows for 11 to 19 and the changed 6, 8 and 9; `TestTheInterleavingFakesInjectEachFault` gains rows for `storeInbound`, for `AppendInbound` spending `storeRecords`'s fault once, and for the three inbound reads.

- [ ] **Step 1: Write** the invariants' rows and the fakes' rows. `go test -race -count=1 -run 'TestEachInterleavingInvariantCatchesItsViolation|TestTheInterleavingFakesInjectEachFault' ./internal/work/app/`. Expected: FAIL.
- [ ] **Step 2: Implement** the driver events, the fakes, the changes to invariants 6, 8 and 9, and the oracles.
- [ ] **Step 3: Run** the rows and the default schedules: `go test -race -count=1 -run 'Interleaving|Schedules|Seed' ./internal/work/app/`. Expected: PASS, the default run within 20 s.
- [ ] **Step 4: The mutation check** as above, results in the ledger. Then `make lint`; `go test -race -count=1 ./internal/work/...`. Expected: `ok`; `0 issues.` twice; at least eight of the eleven mutants caught.
- [ ] **Step 5: Commit:** `git add internal/work/app/interleave_world_test.go internal/work/app/interleave_test.go internal/work/app/interleave_invariants_test.go`; `git commit -m "test(work): the interleaving test drives inbound events, with nine new invariants and a mutation check"`.

### Task 11: Scenarios 29 to 32, and the documents

**Design:** D15; Contract C2's amendment. **Rulings:** R114, R129, R130.

**Files:**
- Create: `test/e2e/harness/inbound.go` (the signing client, `InboundItemID`), `test/e2e/inbound_test.go`
- Modify: `test/e2e/harness/serve.go` (`Addresses.Inbound`), `deployment.go` (`DeploymentSpec.Inbound bool` writes `listeners.inbound: 127.0.0.1:0`), `config.go` (`ConfigSpec.Mappings []MappingSpec`, each with source, bearer or HMAC, an identity source on a scripted read tool, `routing: {service: labels.service}`; its secret written to the secrets directory), `harness_internal_test.go` (`TestTheHarnessDerivesAnInboundItemsIDAsServeDoes`, R129's vector)
- Documents: `plan/2026-11-09-plan-7c1b-inbound-events.md` (this plan's record), `plan/2026-10-12-implementation-roadmap.md` (row 7c-1b's size as planned and executed), `plan/adr/ADR-016-delivery-build-before-team.md` (History, R-f; under question 3, A7's carry: stable-id sources and declined firing resumes), `plan/2026-10-11-agent-swarm-platform-spec.md` (amendments below), `plan/2026-10-20-plan-7d1-serve-and-harness.md` (C1's `listeners.inbound`, C2's `inbound` key), `README.md` (the inbound listener, its headers and signed string, the 1m37s worst drain), `examples/config/trigger-mappings/alerts.yaml` (R130's comment)

**The harness:** `(*Instance).Inbound() *Inbound`; `(*Inbound).Send(t, source string, body []byte, secret string, at time.Time) (status int, answer InboundAnswer)` signs `<unix>.<body>` with `crypto/hmac`; `(*Inbound).SendRaw(t, source string, body []byte, header http.Header) (int, InboundAnswer)`; `InboundAnswer` holds the counts and `Error`; `harness.InboundItemID(tenant, source, eventID string) string` derives UUIDv8 of SHA-256 over the tenant, a zero byte and `inbound:<source>:<eventID>` (opening.go:19-26).

**The scenarios** (process runner, `quick` timings, the work type's `autoClose` 5 s):
- **29, `TestASignedGroupOpensOneItemAndTakesItsCorrelatedRepeatedLateAndPoisonEvents`:** (1) alert A (`fp-a`, `cart`, rule `r1`, firing at t0): `200 {"opened":1}`; the item, by `InboundItemID`, investigates. (2) One request with B (`fp-b`, `cart`, `r2`, firing t0 + 1 min), an alert with no fingerprint, A again with its event id, and A's firing with t0 − 10 min: `200 {"appended":1,"unroutable":1,"repeat":1,"late":1}`. (3) `inspect item` shows three `item_triggered` (A applied, B applied, the old A `applied: false`) and no second item; `inspect board` of its run holds platform evidence with `ref` `trigger:<B's id>` (A6 (c)); serve's log holds one Warn naming `alerts`, its owner and `missing:instanceId`, and no body.
- **30, `TestTheResolutionOfEveryAlertEndsTheRunAndTheItemClosesByItself`:** after 29's first two steps, (1) A and B resolved: `{"appended":2}`; (2) the run stops `trigger_resolved` at its round's end, and the item awaits its close; (3) the admin extends the run one round: the resumed round ends with another reason than `trigger_resolved`; (4) the item closes by itself 5 s after it awaits its close again.
- **31, `TestAFiringResumesTheRunAndAFiringAfterTheCloseOpensALinkedItem`:** (1) A opens, A resolves, the run stops `trigger_resolved`; (2) A fires again (a new start time): `{"appended":1}`, the item investigates, `inspect item` shows `run_extended` with cause `firing` and `inspect board` a `resume` entry with cause `firing`; (3) A resolves, the run stops, the admin closes the item; (4) A fires again: `{"opened":1}`; the new item's view has the old item's incident, and its records 1 to 3 are `item_opened`, `item_triggered` and `item_linked` with `by` `source:alerting`.
- **32, `TestUnsignedStaleUnknownAndBearerRequestsAreRefused`:** a bad signature `401 bad_signature`; a timestamp ten minutes old `401 stale`; source `nosuch` `404 unknown_source`; a bearer mapping `pager` `400 scheme_unsupported`; afterwards no item exists for any of their event ids and serve's log holds none of their bodies.

**The spec's amendments** (Task 11's documents): 3.6: the headers and signed string; two secrets as one or two lines of the secret file; bearer and client certificates refused until the onboarding build; the limits fixed in code (4 MiB, 500 events, 16 KiB stored payload); the event id never redacted, an event whose id the rules would change unroutable; `routing`; a mapping with `checks` refused until 7c-2; D7's order at the source level and match order. 4.2: trigger updates are platform evidence ("a platform record: a trigger event"), composed by rule, posted to running visible boards after the commit, best effort (A6 (c)). 5.6: "the current round" is the round during which the resolution was recorded, counting only a resolution after the run's latest start or resume (D12, ruling 10). 8.2: the flap row: a firing resumes at once or is declined with a code, no slot wait (O1), no run declined (ruling 9), the flap link with no time bound. Section 15: the three fixed limits and the `inbound` listener's timeouts.

- [ ] **Step 1: Write** the harness, its vector test and the four scenarios. `make build && AGENTSWARM_BIN=$PWD/bin/agentswarm go test -count=1 -timeout 30m -tags e2e -run 'Inbound|Signed|Resolution|Flap|Firing|Unsigned' ./test/e2e/...`. Expected: FAIL first (no harness), then PASS once Tasks 1 to 9 are in.
- [ ] **Step 2: Gate.** `make lint`; `make test-e2e` (scenarios 1 to 32, the process runner). Expected: `ok`; `0 issues.` twice.
- [ ] **Step 3: Commit the scenarios:** `git add test/e2e`; `git commit -m "test(e2e): scenarios 29 to 32: inbound groups, resolution, flaps and refusals"`.
- [ ] **Step 4: The documents**, read by the controller against the diff.
- [ ] **Step 5: Commit the documents:** `git add plan/2026-11-09-plan-7c1b-inbound-events.md plan/2026-10-12-implementation-roadmap.md plan/adr/ADR-016-delivery-build-before-team.md plan/2026-10-11-agent-swarm-platform-spec.md plan/2026-10-20-plan-7d1-serve-and-harness.md README.md examples/config/trigger-mappings/alerts.yaml`; `git commit -m "docs: Plan 7c-1b's amendments to the spec, the roadmap, ADR-016, Plan 7d-1's contracts and the README"`.

## Review Focus

The design says what each rule must do, not every input it meets. These five are the inputs most likely to bite that the design's tests would not reach; each has its test in the task that owns the code.

1. **A group retried after a failure midway.** A source re-sends the whole request after a `503`: the events committed before the failure must come back `repeat`, the rest be taken, and nothing applied twice. (`TestAGroupRetriedAfterAFailureMidwayTakesEachEventOnce`, Task 9; invariant 18, Task 10)
2. **A source whose clock runs ahead.** A firing dated past the skew is clamped, and a resolution dated within the clamped window is late, while one after it applies: a person must be able to tell why a resolution changed nothing. (`TestAFutureDatedFiringIsClampedAndItsResolutionStillApplies`, Task 6)
3. **Two sources with colliding instance ids.** Every key is per source; one source's resolution must never resolve, order or match another's alert. (`TestTwoSourcesWithTheSameInstanceIDAreKeptApart`, Task 6)
4. **A takeover between an event's commit and its answer.** The retry reaches the next leader, whose account was rebuilt from records: it must answer `repeat` from the event store, not open or append again. (`TestAnEventCommittedBeforeATakeoverIsARepeatOnTheNewLeader`, Task 6)
5. **A secret rolled over between requests.** The file is read at each request; a source still signing with the old secret passes while both lines stand and is refused once the old line goes, with no restart. (`TestASecretRolledOverBetweenRequestsIsReadAtEachRequest`, Task 9)

## Size

| # | Task | Raw days |
|---|---|---|
| 1 | Migration 0012, the event store, `AppendInbound` | 1.25 |
| 2 | Records: `item_triggered`, `Opened.Trigger`, `Extended.Cause`, the fold's keys, instances, `ResolvedAt` and the refusal of an automatic close while firing | 1.75 |
| 3 | The mapping's `routing`, the evaluator and its redaction, and R134's bounds | 1.85 |
| 4 | HMAC signatures and rollover | 0.75 |
| 5 | The source actor, `openLocked` and the policy | 0.75 |
| 6 | `Ingest`: order, match, append, unmatched, refused, the resolution at round end, and R133's pending reloads | 2.25–2.5 |
| 7 | The firing resume, and a flap after a close with its link | 1.25 |
| 8 | Trigger evidence on running boards, and `scope()` refusing `other` | 0.75 |
| 9 | The listener, the handler, the deployment and serve | 1.25 |
| 10 | The interleaving extension, invariants 6, 8 and 9 adjusted, and the mutation check | 2–3 |
| 11 | e2e scenarios 29–32 and the documents | 1.5 |

- **Total:** about 15.35–16.6 raw days by the table; with the design's allowance for v2.1's fixes, **about 16–17.5 raw days, about 3.2–3.5 person-weeks**, recorded only. Version 1 kept the design's 15–16.5; version 2 adds about 0.75–1 day for the plan review's fixes: R133's pending reloads and their tests (Task 6, about 0.25–0.5), invariants 6, 8 and 9 adjusted, oracle 19, the re-send and equal-time draws and three more mutants (Task 10, about 0.5), R134's bounds (Task 3, about 0.1); Task 8's switch to platform evidence is the same size. The rest of the rulings add under a tenth of a day each. Still inside the roadmap row's 2.7–3.65. At 7c-1a's executed ratio (about 1.3), about 4.2–4.6 person-weeks as executed.
- **Calendar guess:** about 1.5–2 days (7c-1a ran about 2 person-weeks raw in 6.75 hours of execution; Task 10's kind runs 1.5–2 times its box). **Stop line: 5 calendar days** from Task 1's dispatch, the later of 5 days and twice the guess.
- **Spend guess:** about $30–40 (review M9: 7c-1a cost about $18.1, review included, for about 8.75 raw days, about $2.07 per raw day; 16–17.5 days is $33–36 before any fix round, and Tasks 6, 7 and 10 each have two reviewers). **Stop line: $80,** twice the high guess, recalibrated after Task 6 from the ledger, never below the guess.

## Not in this plan

- **The design's Section 4 carries:** the leader's liveness residual and the epoch-scoped `Release` (to 10a, ruling 8; if the soak hits the residual, it is recorded under A3'' and stops nothing); the walk's admission-order hook (to the plan that next extends invariant 9; this plan adds no walk change); the owner's notification (9b); principal, responders, `fallbackPrincipal`, `checks`, milestones and `incidentRef` (7c-2); bearer and client certificates (the onboarding build).
- **7c-1a's carries to 7c-1b that this plan does not take:** the concurrent soak's liveness failure (seed 1794139272505700854) and `instLease.Release`/`Lease.Release` swapping out the current epoch, both **carried to 10a**; the invariant-9 admission hook, **carried to the next plan that extends invariant 9**; resumed runs killed soon after resuming in the world, and per-run token spend out of the world's reach, **carried with the interleaving test**, unchanged.
- **7b-1's carries to "Plan 7c-1" changed by the design:** "a firing resume waits for a free slot… the queue's items and the owed resumes share one order" is **reversed** (ruling 1, O1), not carried; "a trigger later correlated into an item a person opened makes the item follow that trigger's resolution" is **moot** under D7, since a person's item takes no key and so no inbound event; the fold follows any `item_triggered` whatever the opener, should a later plan (7c-2's `incidentRef`) append one.
- **7c-1a's residuals for the author** stand as recorded there (the rulings window on clock steps, an older item held while a younger one's failed start keeps its slot, R103's preview under a failed read, R73's board after resume, close, reopen, resume, lost extensions, invariant 6 in a concurrent batch). The flap link takes no window, so the first does not reach it; R73's case reaches a firing resume as it reaches a person's.
- **The web's words for trigger states and trigger evidence** (spec 4.2, and spec 10.2's board states "Alert resolved", "Reopened after a flap", "Received late, not applied"), and the item view's trigger fields: 9a-2.
- **Retention of `inbound_events`** (the "event logs" class, [30 days]): 10a.
- **A deployment key for the clock skew** (fixed at 2 min in code here) and for the three limits.

## Constraints carried to later plans

- **Everything under "Not in this plan"**, with its plan named.
- **Plan 9a-2:** words for `item_triggered` (applied, late, resolution), trigger updates, and the board states of spec 10.2; the item view's instances and firing state; and how the web shows trigger updates' platform evidence (A6 (c)), or, had the author chosen A6 (a), the `trigger` notice, which only people see.
- **Plan 9b:** the mapping owner's notification, which is a Warn line here (D10); a firing resume's decline shown to a person (the `declined:<code>` on the record).
- **Plan 7c-2:** apply `principal`, `responders` and `fallbackPrincipal`, and drop R117's Warn; lift ruling 3's refusal of a mapping with `checks` and make such items diagnosis-only; widen item acts on inbound items to their responders (ruling 2); a source actor's principal and responders (R116); milestones and `incidentRef` (spec 3.6), whose events follow this plan's order and dedup rules; A7's case, carried as recommended: a source whose re-notifications keep their event id never retries a declined firing resume (R130's example is such a source); settle it with ADR-016's question 3, by the source's ids or by retrying when a decline's cause clears.
- **The onboarding build:** bearer tokens and client certificates (spec 3.6), which the loader accepts and the listener refuses `400 scheme_unsupported`.
- **Plan 10a:** retention of `inbound_events` under `agentswarm.retention`; the leader's liveness residual and the epoch-scoped release (above).
- **Plan 8a:** shadow and invisible-arm runs ignore trigger resolution (spec 5.6): `TriggerResolved` is set only for a visible primary run here; trigger evidence goes to no shadow board; a firing resume resumes the primary run only.
- **The interleaving test** (any plan that extends it): invariants 11 to 19 judge the world's inbound store and records as a whole, and invariants 6 and 9 now skip platform acts and inbound openings' own records (Task 10); the driver's key model is fixed per instance (R135); the world drives `Ingest` below the handler (R128).
- **Every later plan:** a start of work waits while the kill switch's view is unsettled (R37), which the firing resume does by declining `unsettled`; a plan that adds record kinds, notice codes or resume causes carries a migration at its own level while R12 keeps bodies at version 1; plans that touch serve or a runner keep `make test-linux` (with its Docker path) in their gates.

## Plan review

Version 1 had one review on Opus 5.5, fresh and read-only, against HEAD 441ad56: **approve with fixes, 0 Critical, 11 Important, 10 Minor.** Version 2 answers every finding as the controller ruled; version 1 is `plan/backups/plan7c1b/plan-v1.md`.

| Finding | The controller's ruling | Where in v2 |
|---|---|---|
| I1 `Opened.Trigger` had no path into the opening | `OpenInput` gains `Trigger`, `Triggered`, `FlapOf` and `Row`; `openLocked` writes the opening's `item_triggered` and the flap's `item_linked` in its own append | Task 5's Interfaces; Tasks 2, 6 and 7; R116 |
| I2 the record order was not fixed | One order, stated once: `item_opened`, `item_triggered`, [`item_linked`], then `item_awaiting_close` when unstaffed | R126; Task 2's fold rule and test; Tasks 5, 6 and 7 assert records by kind |
| I3 `declined:no_run` unreachable | The resume's condition reads "its run is absent or stopped"; `mayFiringResume` maps `CodeNotStarted` to `no_run` | Task 7; invariant 16 |
| I4 refusals told apart only by text | `ErrTypeNotRunnable`, `ErrRoutingRefused`, `ErrOpenerRefused`, each wrapped with `ErrInvalid`; R131 maps by `errors.Is` | R131; Task 5's Interfaces and `TestEachOpeningRefusalNamesItsSentinel` |
| I5 a stale account after an unknown outcome | An unknown outcome answers `503`, marks the item pending reload and arms `c.retry`, as `unrecorded` and `carryOn` do; a source's events wait on its reload; `LastItem` naming a not-closed item outside the account loads it and matches, or refuses `item_unreadable` | R133; Task 6 step 0, 5 and 7, two tests (and a third, the plan's) |
| I6 notices invisible to agents | A6 reframed: (a) a notice, invisible to agents (board/view.go:80-85); (c) platform evidence with the redacted title, visible; (c) recommended, best effort after the commit, running boards only | A6; Task 8 rewritten for (c) |
| I7 invariants 6 and 9 misjudge inbound records | Both changes listed with rows proving no false positive and rows proving they still bite; invariant 8 reads the pair | Task 10, "Changes to existing invariants" |
| I8 three mutants uncatchable | Mutants 8 to 10 replaced, an eleventh added; oracle 19, "an un-faulted `Ingest` never errors"; the driver re-sends what erred and draws equal-time events | Task 10 |
| I9 oracle 17 unsound without a key model | Each instance has one fixed dedup key and correlation key, with the reason | R135; Task 10 |
| I10 ids and keys unbounded | 1 to 256 runes, no `config.Unseen` character, else `field_invalid:<field>`, the id's case stored under `sha256:` | R134; Task 3 |
| I11 the re-notification retry for stable-id sources | A7 for the author: accept for 7c-1b and carry, or retry when a decline's cause clears | A7; carried to 7c-2 |
| M1 the safety list missed C1's class | Faults 6 and 7 added (invariants 14 and 13) | A3'' |
| M2 a test pinning 1m27s missed | `serve_integration_test.go` in Task 9; the existing table test kept by name | R114; Task 9 |
| M3 `TriggerStatus` declared twice | Task 2 marks it Task 1's | Task 2 |
| M4 a source the loader takes and the handler refuses | The loader checks `Open`'s form | R132; Task 9 |
| M5 an admin key taking an inbound id | A `repeated` hit for an opening with a `Trigger` answers `ErrKeyReused` | R121; Task 5's test |
| M6 `ending()`, not `endRound` | Named | Task 6 |
| M7 four `AutoCloseAt` reads; the write fault of `AppendInbound` | Four reads, line 1421 breaking invariant 8 on `ok == false`; `AppendInbound` spends `storeRecords`'s fault once | Tasks 2 and 10 |
| M8 the inbound server's other bounds | `internal/inbound` builds the handler; serve's `newServers` closure makes the server | R113; Task 9 |
| M9 the spend floor | $30–40, stop line $80 | Header; Size |
| M10 N3's Warn had no test | `TestARequestWhoseEventsTheRulesAllRefuseIsWarnedOnce` | Task 9 |

The author approves the plan, A3'', A6 and A7, then execution starts, subagent-driven on main. The review's last note is for the final message: ruling 2 with R116 means no one but an admin acts on an alert item before Plan 7c-2.

## The branch's end (the controller's)

- [ ] **`make test-linux`,** before the final review: `make test` and `make test-integration` on Linux, the whole e2e suite under the Docker runner, scenarios 29 to 32 among it, and the process runner's scenarios that check a process is gone. Expected: every package `ok`.
- [ ] **The soak,** before the final review: `go test -race -count=1 -v -timeout 60m -run '^(TestRandomSchedulesKeepTheLifecyclesInvariants|TestConcurrentSchedulesKeepTheLifecyclesInvariants)$' ./internal/work/app/ -args -interleave.for=40m`. Expected: PASS. A failing seed becomes a unit test of the coordinator, then its fix, or, if it is the carried liveness residual, a recorded residual (ruling 8).
- [ ] **The final review,** by a fresh reviewer on Opus 5.5, of the whole branch against this plan; **one fix wave**; then `make test-linux` and the soak again.
- [ ] **The record:** "Changes made during execution" (the controller's rulings, what the soaks found, residuals for the author), "Size as executed" (calendar, spend against $30–40 and its $80 stop line, person-weeks), and this plan's status line.
- [ ] **Push** to origin main.
- [ ] **The workspace** backed up to `plan/backups/plan7c1b/` and deleted; the ledger kept there.
- [ ] **The final message to the author, in Chinese:** every ruling (R109 to R135, A3'', A6, A7, and the controller's) with its cost if wrong, every deferred minor, every residual, the mutants caught and missed, and the calendar days and spend against their guesses.
