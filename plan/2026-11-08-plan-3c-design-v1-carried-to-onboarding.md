> Carried to the onboarding build on 2026-11-08 (roadmap row 3c; ADR-016): the author moved Plan 3c out of the core after this draft and its review (`2026-11-08-plan-3c-design-review-v1.md`) sized it at about 2.3–3.0 person-weeks as executed even when simplified, and found its per-case, alert-time reads (D5) to depend on the shape of a team's servers. Plan 3c starts from this draft, read with the review's findings; the review's simplifications S1–S7 are its expected direction.

# Plan 3c design, v1: the retrospective check through the gateways

Status: draft for an Opus design review, 2026-11-08. Not approved.

## 1. Intent

Roadmap row 3c: Plan 3b's retrospective check command, run on a team's own retrospective set, with its reads through the MCP gateway and its model calls through the LLM gateway. The team's data is then redacted before it reaches the model vendor, and every read and model call is recorded (spec Sections 9.4 and 14.4; ADR-016, question 6).

Why now, before any team exists: the retrospective check is the first thing a team does with the platform (spec Section 14.4, step 3). With 3c in place, a prospective team can run its own agents on its own past incidents with redaction and an audit trail, before it commits to anything. 3c is therefore also the outreach demonstration.

Acceptance has to stand in for a team's data. It does so in two tiers:
- **Offline:** a fake vendor and fake MCP servers, inside `make test-linux` and `make test-docker`. This tier proves that every read and model call goes through a gateway, is recorded and is redacted.
- **Live, optional:** Claude Haiku 5.5 on the ITBench development set through the gateways, with spend capped at about $1. The author decides when to run it.

## 2. The conflict this design resolves

Spec Section 14.1 says the retrospective check's agents "run without the coordinator, each on its own and aggregated by key as in the independent run". The gateways, though, accept only what the coordinator provides today:
- **Sessions:** tokens live only in serve's in-memory `sessions.Registry`, and are valid only after `Issue` and `Move` into a round (sessions.go:65, :88, :143-152).
- **Run records:** a gateway call needs the run's `run_created` record in `work_records` (`Board.Context`, records.go:208-228). Board posts also need an open round.
- **The holding:** every audit write is checked against the issuing holding's epoch (`Lease.Check`, lease.go:156-175).
- **Configuration:**
  - the agent's tools, model and redaction come from a registry revision (`Session.Commit`);
  - the model must have a price card;
  - MCP servers must be registered at `https` endpoints with pinned schema hashes.

**The author's ruling (2026-11-08):** serve hosts check runs outside the work-item lifecycle. There is no queue, no promotion, no rounds after the first, no item and no auto-close. "Without the coordinator" is read as "without the coordinator's lifecycle": the check host reuses its ports but is a component of its own. The kill switch still stops check runs. This is a safety floor.

## 3. Decisions

### D1. A check host in serve, beside the coordinator

`internal/work/app` gains a `Checks` use case (name to settle). It holds its own lock, separate from the coordinator's, and these ports, all of which exist today:
- `Records`, which writes the check run's records;
- `Boards`, for `StartRound` and the run's board;
- `Sessions`, for `Issue`, `Move` and `EndRun`;
- `Revisions`;
- `Holding`;
- `Clock`;
- a kill-switch reader.

A check run is:
- **Opening.** A `run_created` record with class `check`. It goes under a synthetic item id `check-<run>` and incident `check-<run>`, so it is audited and chained like any work record. No `item_opened` record is ever written, so a takeover's load never finds a check run (the `work_records_by_kind` scan is keyed on `item_opened`).
- **One round.** A round that `Boards.StartRound` starts, with the deadline the request asks for, capped by the run budget's wall clock. Each agent's session is issued and moved into round 1 with the run's token budget.
- **Ending.** The run ends when every agent's turn has called `end_turn` or the deadline passes, whichever comes first. The host then ends the round, ends the sessions and records `check_run_ended` with its reason: `done`, `deadline`, `killed` or `fenced`.

**Kill switch:**
- The host refuses to open a check run while the switch is on, reading the switch's table, not a cached flag.
- The coordinator's `carryOutKill` calls the host's `StopAll` after it records `on`. This closes the race between that read and an on.
- A check run that the switch stops ends `killed`.

**Losing the holding:**
- A fence, drain or lost lease calls `StopAll`.
- Check runs are never resumed after a takeover or restart. The new holder does not see them, their sessions died with the old instance, and the client reruns them; 3b's command already retries runs that ended in an error.
- An abandoned run has no `check_run_ended` record. The admin view reports it as `abandoned` once its deadline has passed and no instance holds it.

### D2. One run per agent per case per trial

"Each on its own" is taken literally: each agent works a case in a run of its own. The gateway always lists `read_board`, so if several agents shared a run, each could read the others' posts. That is collaboration the check must not have. In 3b this held because the local toolbox offered no `read_board`.

Aggregation by key happens in the client.
- The client fetches each run's board events and read calls, then replays them into one local in-memory board, the same `app.Boards` type that 3b uses.
- It then runs `Assess` and `Recheck` there, so 3b's scoring code is reused unchanged.
- **For the reviewer:** this replay must keep provenance. Evidence quotes are checked against the read calls the gateway recorded, under their redacted outputs and gateway call ids. Is replaying into a memory board sound, or should serve aggregate across runs itself?

### D3. The turn loop stays in the client (a refinement of the author's option A, to be confirmed)

The option as put to the author said that serve runs the turns with its own runner. This design proposes instead:
- serve provides runs, rounds, sessions and the gateways;
- the client drives each turn with 3b's own loop, `runner.Run`;
- the client points that loop at the LLM gateway, as base URL with the LLM token as the key and no retries, and at the MCP gateway through `mcptools.Client`, which settles Plan 5a's carried item.

**Why:**
- *Comparability.* 3b's loop, prompt and nudge setting (off, R21) stay the same, so a 3c result differs from a 3b result only in the transport and the redaction.
- *Size.* There is no runner integration, no images and no sandbox lifecycle. That saves about 3–4 days.
- *Spec.* It is closer to "the agents run without the coordinator".

**What it costs:**
- The agent loop runs on the operator's machine, not in a sandbox. Everything it sees has already passed the gateways' redaction, and every tool is read-only, so the sandbox protects little here.
- The admin API must return the session tokens to the client. The admin API is loopback-only and protected by the admin token, and it can already turn on the kill switch and close items. Tokens returned this way are scoped to one check round.

**If the reviewer or the author prefers serve's runner,** D3 flips: serve starts the turns with `Turns.Start`, and the client only submits work and collects results. Add about 3–4 days.

### D4. Admin routes

All routes sit under the existing auth (loopback peers and the admin bearer token).
- `POST /v1/checks`, with an `Idempotency-Key`.
  - The body holds the case id, the agent, the trial, the routing values (D5), the deadline and the token budget.
  - The answer holds the run id, the commit the run pinned, the MCP and LLM gateway URLs, and the two tokens.
  - It is refused while the kill switch is on, when serve is not the holder, or when the agent is not in the revision.
- `GET /v1/checks/{run}` returns the run's state, the board events and the read calls with their redacted outputs as the gateway recorded them.
- `POST /v1/checks/{run}/end` lets the client end a run early, for instance on Ctrl-C.

### D5. Case selection and the alert-time bound

Spec Section 12.3: a retrospective set's reads are "bounded to the alert time". The gateway reaches only registered servers at fixed endpoints, but each case needs its own data: in ITBench, a dataset per scenario, cut at the alert.

**Proposal:**
- A check run carries two trusted routing values, `check.case` and `check.asOf` (the case's alert time).
- An `McpServer` may declare `spec.retrospective: true`. The gateway's upstream client then sends `Agentswarm-Case` and `Agentswarm-As-Of` headers on check runs only, never on live runs, and only to such servers.
- A server that is not declared retrospective is unreachable from a check run, because its reads cannot be bounded. The check host refuses a run whose agent is granted tools on such a server.
- `bench/itbench serve --datasets` gains a route that picks the dataset from `Agentswarm-Case`. TLS already exists there (`--tls-ca`).
- A team's own servers implement the two headers, or the team runs a snapshot server; the docs say so.

**For the reviewer:** this adds a field to the registry schema and two headers to the upstream path. The alternative is one registered server per case. It needs no schema change, but every case needs its own agent grants, which makes it unworkable beyond a handful of cases.

### D6. The client: `agentswarm check --serve ADDR`

- **Gateway mode.** `--serve` selects it, and the admin token is read from a file flag.
  - The agents file is replaced by agent names, which must exist in the pinned revision. Their skills and model come from the registry, so `--model` is refused in this mode.
  - The case's `Task.Servers` is ignored, with a warning. The servers are the agents' granted servers.
  - Everything else is kept: the outputs in `--out` and their layout, retrying failed runs, trials and concurrency.
- **Direct mode** (no `--serve`) is unchanged, so that Plan 3's runs stay reproducible (R21). Its help says it sends data to the vendor unredacted and is meant for public data only.
- **Budget.** The run budget is held by the LLM gateway (`Session.Budget`), not split locally. A turn that the gateway refuses for budget ends that turn, as in 3b.

### D7. What is recorded, and the proof

Every read is a `tool_calls` row with its redacted output, and every model call is a `model_calls` row. Both are chained and both carry the check run's id. The offline harness proves the following:
1. A case whose MCP output holds a string an "always" rule redacts reaches the fake vendor redacted, and is stored redacted.
2. Each read and model call of a check run has its audit row, and the anchor names the check run's incident.
3. The kill switch, turned on mid-run, ends every check run as `killed` within a bound, and no call succeeds after that.
4. A takeover leaves the old check runs abandoned, and a rerun completes.
5. A server not declared retrospective is refused at open.
6. The process runner and the Docker runner do not matter here (D3), but `make test-docker` stays in the gate because serve changes (Plan 7d-2's carried rule).

## 4. Out of scope

- Serve's runner for check turns (D3's alternative).
- Choosing a team's token budget default from the runs: the report shows tokens per run, as 3b's did.
- Replay of convergence settings (spec Section 5.6).
- Any change to scoring.

## 5. Size, against the row's 0.75–1.5 person-weeks

| Part | Days |
|---|---|
| The check host (D1), with kill, fence and drain | 3 |
| Admin routes (D4) | 1.5 |
| The retrospective flag and headers (D5), registry and upstream | 1–1.5 |
| bench/itbench header routing | 0.5 |
| The client's gateway mode (D2, D6) | 2–2.5 |
| The offline harness proofs (D7) | 2 |
| Docs, and the live run's script | 1 |
| **Total** | **11–12 days, about 2.2–2.4 person-weeks** |

**This crosses ADR-016's trigger:** the row's upper bound is 1.5, and 1.5 × 1.5 = 2.25. It must be reported to the author with the plan.

Cuts that would bring it down:
- **Drop D5's headers**, and with them the ITBench live run through the gateways. The offline proof uses one fake server, and a team with snapshot servers per case waits for a change. Saves about 1.5 days.
- **Drop the client-side replay (D2).** Agents share one run, and `read_board` is filtered to the agent's own entries on check runs. That is a change in the gateway, and it saves about 0.5 days.

## 6. Questions for the review

1. Is D1's synthetic item id safe? Specifically: takeover and load, views, `GET /v1/status`, retention, and any query that scans `work_records` by `run_id` or `item_id`.
2. D3: returning tokens over the admin API. Is it acceptable?
3. D5: is the `retrospective` flag the right shape for a bound the spec puts on the set, not on the server?
4. D2: replaying events and redacted calls into a local board, with provenance intact.
5. Is anything in the kill switch's or the fence's interplay with a second lock (D1) a new instance of the stale-account class?
