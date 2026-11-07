> Carried with `2026-11-08-plan-3c-design-v1-carried-to-onboarding.md` to the onboarding build on 2026-11-08 (ADR-016). Line references are to the code at e1bf8d7.

# Plan 3c design v1: review

Reviewer: Opus design review, 2026-11-08. Reviewed against `plan/backups/plan3c/design-v1.md`, the spec (Sections 4.1, 5, 8.3, 9.3, 9.4, 12.3, 14.1, 14.4), roadmap row 3c, ADR-016 and the code at `e1bf8d7`. The author's ruling (Section 2: serve hosts check runs outside the work-item lifecycle) is taken as given. Everything built on it is judged.

**Counts:** 2 Critical, 11 Important, 10 Minor.

**Verdict in one line:** the ruling holds up, but three of the design's load-bearing premises are wrong:
- D2's premise: a shared one-round run leaks other agents' posts through `read_board`. It does not.
- D3's premise: 3c differs from 3b "only in transport and redaction". It differs in at least eight ways.
- D1's kill-switch hook point: `carryOutKill`. It is the wrong function, and the second lock opens a drain-time hole.

Fixing these makes the design smaller as well as safer. Even so, the size is under-stated: it is about 3–3.5 raw person-weeks as written, not 2.2–2.4.

---

## Answers to the design's Section 6 questions

### Q1. Is D1's synthetic item id safe?

Mostly, for the paths that exist today. Not everywhere, and it needs one change of approach.

**Safe today:**
- **Takeover and load.** `Records.Active` reads only by `kind = item_opened` (`internal/work/postgres/records.go:63-69`), so a takeover never loads a check run.
- **`UnstaffedBy`.** It reads only `item_awaiting_close` (`records.go:84-88`).
- **`Runs.BoardContext`.** It reads the `run_created` row by `run_id` (`records.go:211-224`). This is the one reader the design wants to hit. Its `HumanEntries` is false for class `check`, which is correct.
- **The audit chain.** A work record is chained under the scope `incident:<incident_id>` (`internal/db/migrations/0007_work_records.sql:51-52`). `check-<run>` holds no colon, so it is chained like any record.

**Not safe, or needs handling:**
- **`GET /v1/items/check-<run>`.** `Views.Item` loads the records and calls `item.Fold` (`internal/work/app/views.go:149-161`). A history that starts with `run_created` and has no `item_opened` does not fold. The admin API then answers 503 and logs at Error, "an item could not be read" (`internal/admin/admin.go:514-526`). It should answer 404, or name a check run.
- **The compatibility harness.** `test/compat/main.go:630-643` lists `DISTINCT item_id` from `work_records` and refuses any id that does not match its item-id pattern. A check run left in the harness's tenant breaks the rollback test.
- **`check_run_ended` is a new `item.Body` kind.** It touches the lifecycle package: `Body`, `Kind()` and the fold's refusals (`internal/work/item/item.go:80-140`). The package should not learn about a thing that is not an item. It also does not need to: see I-8, where the board's own `Stop` entry carries the end.
- **Anchors.** An anchor names "the runs and incidents whose records the chain gained, as many as it can name, with a count of the rest" (spec Section 9.4). A large check (cases × trials × agents) adds hundreds of synthetic incidents to each hourly anchor. These can push real incidents into "the rest".
- **Future readers.** Plan 8 counts incidents (spec Section 13.2), and Plan 10a runs retention by data class. Both will scan `work_records` by `incident_id` or `item_id`. The roadmap lists 3c as a prerequisite of 10a. Reserve the `check-` prefix now, and write in the plan that later readers must exclude class `check`.
- **`GET /v1/status`.** Unaffected, since it reads the coordinator's snapshot. But see I-1: spend and the daily cap *are* affected.

### Q2. Returning tokens over the admin API: acceptable?

Yes, with conditions. The admin token holder can already turn the kill switch on and off and close items, so the tokens give it nothing new in kind. The conditions:
1. **Same host.** The admin API answers loopback peers only (`internal/admin/admin.go:166-178`), so the client runs on serve's host. D3's "the operator's machine" is wrong unless that machine is the host (see I-4).
2. **A replayed POST.** The registry keeps only the tokens' hashes (`internal/work/sessions/sessions.go:24-30, 65-81`), so a replayed POST cannot return the same tokens. It must re-`Issue`: new tokens, with the old ones forgotten. That is the right behavior, but D4 has to say so.
3. **Handling.** Answer with `Cache-Control: no-store`. Never log the tokens. Type them as `spec.Token`, which prints as `[session token]` (`internal/runner/spec/spec.go:76-83`).
4. **The real weakening is not the token.** It is that the client builds the prompt itself (see I-3).

### Q3. Is `spec.retrospective: true` the right shape for a bound the spec puts on the set?

**As a shape:** acceptable. It is a server's declaration that it honors the two headers, and the platform cannot enforce the bound in any case: spec Section 12.3 says that "live state that cannot be replayed makes it pessimistic".

**What it lacks:**
- It is one-sided. A snapshot-only server (ITBench) registered with the flag is still reachable from live runs, where it serves past data as if it were current.
- The platform's own tool listing does not carry the headers. Pinning (`look` and `visible` in `internal/actions/app/call.go:217-230`) connects as the platform with no run (`call.go:276-279`, `conn.Run` empty). Every retrospective server must therefore answer `tools/list` identically with no case header. This holds for ITBench, whose tool definitions are constant (`bench/itbench/server/mcp.go:27-37`), but it must be written into the contract.

**More important: it is premature.** ADR-016 builds the core on assumptions that a team later checks. The shape of a team's retrospective data is exactly such an assumption: snapshot server, adapter, or replay store. See I-7, which recommends moving D5 out of 3c.

### Q4. Replaying events and redacted calls into a local board, with provenance intact?

Don't. The replay is not needed (I-2). And as designed it breaks 3b's own checks:
- `Recheck` flags every piece of evidence whose cited call is "of another run or agent" (`internal/measurement/check/report.go:196-197`). Merging N runs into one memory board must therefore rewrite `Run` on events and calls alike. Once rewritten, the records no longer match the audited rows. That is the opposite of provenance.
- Replaying through `Boards.Post*` runs the board rules again and assigns new entry ids (`randomIDs`). The `evidence_ids` inside hypotheses then dangle.

With one shared run per case and trial, there is nothing to merge. The board serve already holds *is* the aggregated board. `Assess` runs on it, server-side through `Boards.Assess`, or client-side on the events as fetched.

### Q5. Is the interplay of the kill switch and the fence with a second lock a new instance of the stale-account class?

Yes, several. See C-1 and I-5:
- A check run opened during a drain survives an on.
- `carryOutKill` fires on paths that are not an on.
- A deadline timer that outlives a fence writes under a later holding's epoch.
- A `run_created` whose answer is lost leaves the host's account stale.

The structural fix is to have no second lock: keep the check account under the coordinator's lock, in a type of its own (C-1).

---

## Critical

### C-1. The second lock, the "is serve the holder" test and the `carryOutKill` hook leave check runs alive past the kill switch

**Claim.** D1 gives the host its own lock, refuses a run "when serve is not the holder", and has `carryOutKill` call `StopAll`. All three are unsafe as written.

**Evidence.**
1. **`carryOutKill` is not the "on" path.** It runs from three places:
   - `kill()`, the on (`internal/work/app/killswitch.go:668`);
   - the settling of an unsettled view whose read found no new on (`killswitch.go:594-595`, `settled && !raised`);
   - every takeover (`killswitch.go:723`).

   A hook there stops every healthy check run whenever a lost kill-switch answer is settled to "off".
2. **Sessions already end on every on and every fence,** through the shared registry:
   - `kill()` calls `c.p.Sessions.EndAll()` (`killswitch.go:663`);
   - `fence()` calls it as well (`coordinator.go:709`);
   - `TakeOver` fences first (`coordinator.go:494`).

   So the live hole is not "an on that misses a running check run". It is a check run *opened after* the on's `EndAll`, by a host that read the switch as off.
3. **The drain.** During a drain the coordinator is fenced, but the holding stays valid for the gateways (`internal/work/app/leader.go:192-221`). If the host tests "holder" through `Holding.Valid` or the guarded stores, which both pass during a drain, it can open a check run and issue live sessions after the drain's fence. An on recorded during that drain goes through `RecordKillSwitchOn` → `fencedForTheDrain`. For a coordinator not held, that returns at once without fencing again (`killswitch.go:312-325`, `case !c.held`), so nothing calls `EndAll`. The check run's sessions survive the kill switch. This breaks the safety floor the author named in Section 2.
4. **The unsettled view.** While a kill-switch write's outcome is unknown, the coordinator "starts no work, as if the switch were on" (spec Section 8.3; `killswitch.go:331-347`). The host reads the table instead, and would start work.
5. **Lock order.** The order today is leader → coordinator (`leader.go:79-81`). D1 adds coordinator → host, because `kill` and `fence` call `StopAll` under `c.mu`. Any call from the host into the coordinator under the host's lock deadlocks. So does any wait for one, for example to read `held`, the view or `unsettled`. And for the open-versus-on race to be closed by `StopAll` waiting on the host's lock, the host must hold its lock across its DB writes (`run_created`, `StartRound`). That puts the host's database I/O inside the coordinator's critical section on every on.

**Fix.** Keep the check runs' account under the coordinator's own lock. Use a `checks` type of its own that the coordinator owns, so the code stays apart and outside the lifecycle, as ruled, with one lock:
- **Opening.** Open under `c.mu` with the same gate every start of work uses: `c.held`, `c.ctx.Err() == nil`, `!c.killSwitch.On`, and `!c.unsettled`. This refuses during a drain, while fenced, while on and while unsettled. Opening already does database writes under `c.mu` (`Coordinator.Open`, `coordinator.go:276-334`), so this matches the existing pattern.
- **Ending on fence and on.** `fence()` and `kill()` end every check run's sessions, which `EndAll` already does, cancel its timers and clear the account. They write nothing under the lock. The `killed` stop is written by a timer under the holding's context while it lasts, and not at all once it is fenced.
- **Drop the reason `fenced`.** After a fence the holding is gone, so the guard refuses the write. A run that lost its holding simply has no end, and its board shows no `Stop`.
- **If the author insists on a separate lock:**
  - publish the coordinator's gate as an atomic snapshot, as `seen` and `lifecycle` are published (`coordinator.go:150-160`);
  - have the host re-check the snapshot's generation *after* it issues sessions, and end them if an on or a fence came between;
  - never call into the coordinator while holding the host's lock.
- **Tests.** Add both races to the D7 proofs: an on between the switch read and `Issue`, and an open during a drain followed by an on recorded during the drain.

### C-2. Case content from the client enters stores and prompts outside spec Sections 9.3 and 9.4

**Claim.** The client supplies the alert, the key schema (services, failure modes and their meanings), the case id and the as-of time. D1, D5 and D6 record or forward these without the rules every other inbound text passes.

**Evidence.**
1. **Data boundaries (Section 9.3).**
   - 3b's `taskPrompt` puts the alert text, verbatim, into the user turn: `"The alert:\n%s"` (`internal/measurement/check/prompt.go:26-27`). On a public benchmark that was harmless.
   - On a team's data, the alert is inbound content. Spec Section 9.3 requires it to be "wrapped in explicit delimiters, labeled with author and provenance, and marked as data rather than instructions".
   - The live path does this. The trigger reaches the agent as platform evidence on the board, read through `read_board`, and the opening prompt "holds the platform's words and ids only" (`internal/work/app/turns.go:376-379`; `rounds.go:17-29`, `PostPlatformEvidence`).
   - D3 keeps 3b's prompt, so the team's alert reaches the model as instructions-grade text: an injection channel the live path closes.
2. **Stores (Section 9.4).** "Everything that reaches ... a store first passes the deployment's redaction rules."
   - The run's `board.Settings` must hold the case's key schema (`check.go:279-281`, `Schema: c.set.Schema(j.cs)`). It is written into `work_records.body` by `run_created`.
   - D5's `check.case` and `check.asOf` are recorded as routing too.
   - The coordinator redacts a title with the always rules (`coordinator.go:1294`) and *refuses* routing that those rules would change (`coordinator.go:1321`, `checkRouting`; spec Section 4.1). D1 does neither.
3. **The vendor path.** The only redaction this path gets is the LLM gateway's request redaction with the always rules (`internal/llm/app/gateway.go:441, 479-481`; `internal/llm/prompt/prompt.go:502-542`). That covers what reaches the vendor, but not the stores, and not the data-boundary rule.

**Fix.**
- **Run the same checks at open.** Apply `checkRouting`'s checks to `check.case`, `check.asOf` and every key-schema value: refuse what the always rules would change, a hidden character, or an escape. Redact any free text the host records (failure-mode meanings) with `always(rev)`, as `title` does.
- **Post the alert as platform evidence.** Post it on the check run's board before `StartRound`, as `Coordinator.open` posts the trigger: redacted, with provenance, and shown through `read_board` as data. The client's prompt then follows the live form, "read the board with read_board, ...". This is also what the live runtime's agents will see, which matters for I-3.
- **Add a D7 proof.** An alert holding a string that an always rule redacts reaches the fake vendor redacted, and is stored redacted on the board. Today D7.1 covers only an MCP output.

---

## Important

### I-1. Check runs spend as primary runs, against the live daily cap

**Evidence.**
- The registry gives every session `limits.Primary` and `cost.Primary` (`internal/work/sessions/sessions.go:168, 181`).
- The LLM gateway admits a call against the run's budget *and the day's spend* (`internal/llm/app/gateway.go:566-579`; `internal/llm/cost/cost.go:167-178`).
- Once the day's spend reaches the cap, every new item opens not staffed for the cause `daily_cap` (`coordinator.go:341-346`).
- Server slots are shared, and only shadow runs are held back from the reserve (`internal/actions/limits/limits.go:122-166`).
- Spec Section 12.3 also runs the retrospective set *during operation*, under the change rule, not only before adoption.

**Consequence.** One retrospective check, for example 30 cases × 3 trials at 3b's 2,000,000 tokens a run, can use up the deployment's daily cap and leave live incidents unstaffed. Its reads also compete at primary priority with live reads, on the team's real servers if they are shared. And its spend is counted as primary spend in spec Section 13.6's cost metrics.

**Fix.**
- Run check sessions as class `shadow`. It exists, is cut at `ShadowCut`% of the cap, is lower priority at the servers, and `model_calls.class` already accepts it (`0006_model_calls.sql`, `CHECK (class IN ('primary','shadow'))`).
- Add `Class` to `app.Session` and carry it through the registry. That is about half a day.
- Say in the plan that the cost metrics must tell check runs apart by run class, not by session class alone.

### I-2. D2's premise is false; one shared run per case and trial is right and simpler

**Evidence.**
- `read_board` shows the round's *snapshot*, frozen when the round starts, plus the reader's own entries. Other authors' entries of this round "appear in the next round" (`internal/actions/app/board.go:533-534`; `internal/work/board/view.go:38-51`; `internal/work/board/round.go:243-251`).
- In a one-round check run, the round-1 snapshot holds no agent's entries. A shared run therefore lets no agent see another's posts, exactly as in 3b.
- A post's answer reveals nothing either. A repeat is reported only for the agent's own entry (`board.go:385-388`).

**What one run per agent breaks:**
- **Budget semantics.** 3b gives a run one budget split among its agents (`check.go:362-368`, `share`; the `--run-budget` help: "split evenly among the agents"). Per-agent runs give each agent a whole run budget, N times 3b's. Spec Sections 14.1 and 14.4 say "the same runs set the team's token budget default", which is a *run's* budget.
- **`Recheck`'s run check.** See Q4.
- **Replay and provenance.** The whole client-side replay, and its provenance question, exists only because of D2.

**Fix.**
- Make one check run per case and trial, inviting all of the case's agents, all moved into round 1.
- Aggregation is the server's board. Score from `Boards.Assess` on that run, returned by `GET`, or computed by the client from the events.
- The gateway holds the run's budget collectively. This differs from 3b's even split, which is one agent per share; record that as a known difference (I-3).
- This removes the replay (about 1–1.5 days) and the design's own "cut 2" alternative. No `read_board` filtering is needed.

### I-3. D3's comparability argument does not hold, and the client controls what is attributed to the registry

**Evidence of the differences between 3b and gateway mode, beyond transport and redaction:**
1. **Tool names.** The gateway lists tools under the agent's name for each one (`internal/actions/endpoint/endpoint.go:142-156`), not the server's raw name.
2. **Result blocks.** Each result is two blocks, a head block (call id, source, status) and the output (`endpoint.go:163-191`). 3b deliberately sends one block "as Plan 3's runner sent every result" (`internal/measurement/check/toolbox.go:117-119`).
3. **Board tools.** The gateway offers `read_board`, `retract_evidence`, `post_task`, `claim_task` and `complete_task` (`board.go:54-120`). 3b offers three board tools (`toolbox.go:25-30`).
4. **Server instructions.** 3b's prompt lists each server's instructions (namespace, data window, alert time; `prompt.go:31-36`; `bench/itbench/server/mcp.go:23-25`). Through the gateway the client sees none.
5. **Output caps and timeouts.** These are the gateway's (`CallTimeout`, its output limits), not `--max-output` or `--read-timeout`.
6. **Budget.** Shared per run, not split (I-2).
7. **Retries.** `--retries 6` against the gateway, which retries the vendor itself.
8. **Entry limits.** These come from the run's `board.Settings`, which the host must set from the client's `--entry-limit`.

**The attribution gap.** The LLM gateway checks the model and the request's shape, and redacts. It does not check that the system prompt is the agent's registry skills. Under D3 the client sends whatever system and user text it builds. A check result is then "the registry's agents at commit X" only on trust, yet spec Section 12.3's change rule ("a preregistered result on the retrospective set: no worse than the current version") rests on that attribution.

**Fix.**
- **Drop the comparability goal.** 3c answers a different question from 3b: a team's agents, as they would run live, against the team's baseline (spec Section 14.4: "its agents go live only if they at least match"). Measure the live configuration.
- **Have serve compose the turn.** Have `POST /v1/checks` answer with one `spec.Spec` per agent (`internal/runner/spec/spec.go:44-65`). It holds the gateways (with `Socket` and `Cert`, I-4), the tokens, the model, `System` from the registry's skills and the platform's check rules, `Prompt` in the live form (C-2), `Deadline` and `MaxTokens`. Record the digest of `System` and `Prompt` in `run_created`.
- **Run the existing runtime.** The client runs `agentswarm turn`'s runtime, `newTurnRuntime` with `runner.Run` (`cmd/agentswarm/turn.go:120-140`), in-process or as a child process per agent. It already handles sockets, the pinned certificate, tokens that never print, and no redirects.
- **Decide the nudge explicitly.** The live runtime nudges (`turn.go:132`, `Nudge: true`); 3b did not (R21). Recommend nudge on, since that is what goes live, and record the choice.
- **Size.** This replaces the "client's gateway mode" glue around `mcptools.Client` with a reuse, and is likely smaller.

### I-4. Under the Docker runner, the production one, the client cannot reach the gateways as D4 describes them

**Evidence.**
- Under the Docker runner, the gateways listen only on two Unix sockets owned by the sandboxes' group, serving TLS 1.3 with a certificate serve makes at start, whose key never leaves memory (`cmd/agentswarm/listeners.go:31-50`; `cmd/agentswarm/sockets.go:52-64, 278-284`; spec Section 9.3).
- "MCP and LLM gateway URLs" (D4) do not exist there.
- The process runner, which has TCP URLs, is "for development and tests only" (spec Section 9.3).
- D7.6 says the runner "does not matter here". It does.

**Fix.**
- The POST answer carries each gateway's socket path and the certificate PEM (`spec.Gateway.Socket` and `spec.Spec.Cert`).
- The plan states that the client runs on serve's host as a member of the sandboxes' group. The admin API's loopback rule already forces the same host.
- Under the Docker runner, D7 proves that the client reaches both sockets with the pinned certificate.
- Correct D3's "operator's machine" to "serve's host".

### I-5. Lost answers and the host's account: the stale-account class, again

**Evidence and gaps:**
- **A lost `run_created` answer.** `Records.Append` can land with its answer lost. The coordinator handles this for openings with a key-derived id and a look-up (`coordinator.go:370-421`, `unrecorded` and `landed`; Decision 11/14). D4's `Idempotency-Key` needs the same.
  - Derive the check run's id from the tenant and the key, as `admin.ItemID` does (`internal/admin/admin.go:685-693`).
  - On a lost answer, load `check-<run>` and finish the open idempotently: `StartRound` again is a no-op (`round.go:225`), then re-`Issue` and `Move`.
- **A replayed key after a takeover** returns the dead run. The client must use a new key per attempt (`<case>/<trial>/<attempt>`) and say so.
- **The end.** A duplicated end must be idempotent. With the board `Stop` (I-8), the board's rules already refuse a second stop. With a work record, `decide` must check the history first.
- **Timers after a fence.** A deadline timer armed under holding N that fires after the same instance re-took the lease as N+1 must write nothing. Otherwise it records `deadline` for a run whose sessions the fence killed. Arm timers under the holding's context and drop them in `fence()`, as `c.runs` timers are (`coordinator.go:706-731`).
- **The reason `fenced`.** It cannot be recorded (C-1). Drop it.
- **The abandoned view.** D1's "no instance holds it" cannot be told by a standby. Drop the abandoned view. A run is `ended` (it has a stop) or `not ended`, and the client, whose turns end on `session gone`, reruns.

### I-6. Pooled upstream MCP sessions of check runs are never closed

**Evidence.** The upstream pool keys a session by `app.Conn`, which holds the server, run and agent (`internal/actions/upstream/upstream.go:251-262, 271-300`). It keeps the session until it has been idle for 10 minutes (`upstream.go:76`). The coordinator closes a run's sessions through `UpstreamSessions.CloseRun` at every stop (`coordinator.go:51-55`; `upstream.go:145-160`). D1's port list omits `UpstreamSessions`.

**Consequence.** A check of 30 cases × 3 trials × 3 agents × S servers holds hundreds of open sessions to the team's servers.

**Fix.** Add `UpstreamSessions` to the host's ports, and call `CloseRun` at every end, kill included. Fences are covered by the existing epoch rule.

### I-7. D5 builds a guess at a team's data before any team exists; move it out of 3c

**Claim.** ADR-016 says assumptions that only a team can answer hold until the team answers, and the core builds only what is team-independent. How a team serves "reads bounded to the alert time" is such an assumption. It might be:
- snapshot servers;
- an adapter in front of their live observability stack;
- recorded fixtures;
- or nothing, which spec Section 12.3 accepts ("pessimistic").

**Evidence of the cost and the gaps:**
- It needs a registry schema field (`internal/registry/config/server.go`).
- It needs `Conn` to carry case and as-of, so that pooled sessions are per case.
- It needs a transport header, and the `itbench` route.
- It has the one-sided reachability of Q3.
- Platform listing must ignore the case (Q3).
- The design prices it at 1.5–2 days. Realistically it is 2–2.5 with the contract and the proofs.

**Fix.** Two options:
- **(Recommended.)** Cut D5 and the ITBench live run from 3c. Prove 3c offline with one fake retrospective server. Carry "per-case routing of retrospective reads" to the onboarding build as an adapter, decided from the team's answer.
- **Or** split it as 3c-2, about 0.5–0.7 raw person-weeks.

**If it stays:**
- Use a tri-state, `retrospective: only | also`, and refuse `only` servers from live runs.
- Carry `check.case` and `check.asOf` as `Session.Item` routing, which already exists for trusted routing values (`internal/work/app/sessions.go:16-17`).
- Record them in `run_created` after C-2's checks.
- Require identical `tools/list` with and without the headers.

### I-8. Use the board's `Stop` entry as the run's end, not a new work-record kind

**Evidence.**
- `Boards.Stop` (`internal/work/app/boards.go:178-182`) posts a chained board event with a free `Reason` string, the rounds, the wall clock and the tokens (`internal/work/board/board.go:265-273`).
- After it, the board refuses further posts.
- It also gives spec Section 8.3's "notice on each board" for a killed check run.
- A new `check_run_ended` kind instead changes `item.Body`, `Kind()`, the fold's refusals and `inspect`, and an older build reads the body as "no kind".

**Fix.** The run's records are `run_created` in `work_records` (needed by `Runs.BoardContext`), plus `round` and `stop` entries on its board. The reason is one of `done`, `deadline` or `killed`. The views answer 404 for `check-` ids, or route them to `GET /v1/checks/{run}`.

### I-9. Write tools in a check run

**Evidence.** D3 states that "every tool is read-only", but nothing enforces it. An agent granted a write tool keeps it, and its calls go down the write path to record-only proposals (`internal/actions/app/call.go:132-133`; migration `0004_proposals.sql`). Proposals of a check run would then appear beside live ones.

**Fix.** At open, refuse an agent with a write grant. Or give the session a flag that leaves write tools out of the listing. The refusal is simpler.

### I-10. The host has no signal for "every turn ended", and reads results before they settle

**Evidence.**
- The coordinator learns of a turn's end from the runner's exits (`internal/work/app/turns.go:95-147`), not from the board.
- With D3 there is no runner. The host would have to poll the board for `end_turn` entries.
- Write calls commit up to the gateways' record timeout after the call's own time. Model calls in flight settle within `SettleWait`. A round's end waits for both (`Options.Grace` and `SettleWait`, `coordinator.go:83-89`). A `GET` straight after the end can miss tool-call outcomes, so `Recheck` reads "not recorded" or "failed".

**Fix.**
- The client knows when its turns end, so it posts `POST /v1/checks/{run}/end`. The deadline timer is the backstop.
- The host ends the sessions, waits `Grace`, and then posts `Stop`.
- `GET` reports `settled` only after that.
- Drop the server-side `end_turn` detection.

### I-11. The size estimate is not honest

See the next section.

---

## Minor

- **M-1. Local files outside the platform's controls.** The client's `--out` holds the transcripts (3b's `cfg.Log`, `check.go:351`), with the model's completions as the vendor sent them and every output. These sit on the host's disk, outside retention (spec Section 9.4, 30 days) and outside encryption at rest. Either write no transcript in gateway mode (the audit store has the redacted prompts and completions), or document the files as the operator's responsibility.
- **M-2. Registration requirements.** A registered server must pin `spec.image` by digest (`internal/registry/config/server.go:39-41`), besides `https` and the tool hashes. The fake servers and `itbench` need image digests in their registrations. This is missing from Section 2's list.
- **M-3. A budget of 0.** `--run-budget 0` means "no cap" in 3b (`cmd/agentswarm/check.go:75`). The gateway refuses every call once `runSpent >= budget` (`cost.go:171`). Refuse 0 in gateway mode, or map it to the deployment's run budget.
- **M-4. Clocks.** The round deadline is in the database's time for records, and in the process's clock for `Move` and the spec (`Clock.Local`, `turns.go:351`). Say which the host uses where.
- **M-5. The `GET` route's surface.** With I-2, `GET` needs the board events, the call metadata (id, agent, server, tool, status) and, optionally, the assessment. The redacted outputs are needed only for `Recheck`. The gateway already enforces quotes at post time, so `Recheck` is a cross-check. Returning the outputs is acceptable because they are stored redacted, but the route does not have to.
- **M-6. Refused flags.** In gateway mode, refuse `--model`, `--retries`, `--max-output` and `--read-timeout` explicitly, and record that the gateway's values apply. Silently ignoring them makes `inputs.json` lie (`check.go:440-457`).
- **M-7. D7.3's wording.** "No call succeeds after that" overstates. A call already at the vendor may finish. The provable statement is that no call is *admitted* after the sessions end, and no read is forwarded.
- **M-8. Missing proofs.** D7 lacks:
  - the alert-path redaction (C-2);
  - a replayed POST under a lost answer (I-5);
  - refusal during a drain and while unsettled (C-1);
  - the shadow class and its cut (I-1);
  - upstream sessions closed at the end (I-6);
  - reaching the gateways through the Docker sockets (I-4).
- **M-9. The status view.** `GET /v1/status` could show the number of check runs open. This is optional, and helps the operator see that a check is spending.
- **M-10. Direct mode.** Keep its help text, and print a warning at start when `--serve` is absent and the set does not declare itself public. A team will copy a 3b command line and send its data to the vendor unredacted.

---

## Size (Section 5)

The design says 11–12 days, about 2.2–2.4 person-weeks. Measured against what it must actually contain:

| Part | Design | Reviewed, as written | Why |
|---|---|---|---|
| Check host, kill, fence, drain | 3 | 4–4.5 | C-1's gates and races; lost answers and idempotency (I-5); timers under the holding; `CloseRun` (I-6); refusals (I-9); settle (I-10) |
| Session class (shadow) | – | 0.5 | I-1 |
| Admin routes | 1.5 | 1.5–2 | Contract C4's text and tests; replay re-issue; 404 for `check-` items |
| D5 and `itbench` | 1.5–2 | 2–2.5 | Q3's contract gaps; `Conn` keys; one-sided reachability |
| Client gateway mode | 2–2.5 | 3–3.5 | Replay with provenance (I-2/Q4); Docker sockets and cert (I-4); refused flags |
| Offline proofs | 2 | 3 | Six more proofs (M-8); a takeover scenario in the harness; `make test-docker` |
| Docs, live script | 1 | 1 | |
| **Total** | **11–12 d** | **15–17.5 d ≈ 3.0–3.5 pw raw** | |

This project's executed-to-planned ratio has run 1.3–1.5× or more: 7b-1 executed at 3.5–4 against 2.95–3.25 accepted, 7b-2 at 0.8–0.9 against 0.5–0.8, and 7d at several times its estimate. That puts the design *as written* at about 3.9–5 person-weeks executed, 2.6–3.3× the row's 1.5. ADR-016's trigger fires by a wide margin.

**With the simplifications below:** about 9–10 raw days, 1.8–2.0 person-weeks. Executed, at 1.3–1.5×, that is about 2.3–3.0, still at or above the 2.25 line.

**Recommendation.**
- Report the size to the author with the plan. Do not shave it to fit.
- Make 3c the team-independent part (S1–S6 below), about 1.8–2.0 raw person-weeks.
- Defer D5 and the ITBench live run, about 0.5–0.7 raw, to a 3c-2 or to the onboarding build (I-7).

---

## Simplifications that keep the row's purpose

The row's purpose: a team's agents on its own retrospective set, with every read and model call through the gateways, redacted and recorded.

- **S1. One shared run per case and trial (I-2).** It removes the client replay, its provenance problem and the per-agent budget distortion. Saves about 1.5–2 days.
- **S2. Serve composes the turn spec, and the client runs the existing `turn` runtime (I-3, I-4).** No new MCP or LLM client glue, and sockets and the certificate come for free. Attribution of results to the registry becomes real. Saves about 0.5–1 day, and closes I-3.
- **S3. The client signals the end, and the deadline is the backstop (I-10).** No board polling for `end_turn`. Saves about 0.5 day.
- **S4. The board's `Stop` instead of a new work-record kind (I-8).** No change to the item package. Saves about 0.5 day.
- **S5. No abandoned view and no `fenced` reason (I-5).** A run either has a stop or it does not, and the client reruns. Saves about 0.5 day.
- **S6. Move D5 and the live ITBench run out (I-7).** Saves about 2–2.5 days. It also avoids building a contract for team servers that do not yet exist.
- **S7. One lock (C-1).** The check account under the coordinator's lock is less code than a second lock with a published gate, a generation re-check and a lock-order rule. It also removes question Q5 instead of answering it.

Together, these leave a 3c whose new surface is:
- one `checks` type under the coordinator's lock;
- three admin routes;
- a session class;
- the client's spec-driven mode;
- the offline proofs.

That is the row as the roadmap describes it.
