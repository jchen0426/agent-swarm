# Plan 7d-1: `serve`, Its Clocks and Timers, and a Fault-Injection Harness — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Approved on 2026-10-20, for subagent-driven execution, with a live probe of the nudge after Task 12. |
| Date | 2026-10-20 |
| Roadmap | `2026-10-12-implementation-roadmap.md`, Plan 7, the second of its parts to run: 7a (executed), 7d-1 (this plan), 7d-2, 7b, 7c |
| Decided by the author | On 2026-10-20: a light plan, the harness first, Plan 7d split in two, and the answers listed under "The author's decisions" |

**Goal:** Run the platform as one process: `agentswarm serve` holds the coordinator, the leader and both gateways, binds every write and every external request to the lease's holding, runs each turn as a host process of the `agentswarm turn` runtime, and decides whether a round is quiet, and whether a wrap-up round concludes, from the platform's own records; a black-box fault-injection harness, built first, shows that it holds under faults of the model vendor, the MCP servers, the database, the processes and a rollback.

**Architecture:**
- **One process, three listeners** (Decision 1). `cmd/agentswarm/serve.go` wires the coordinator, the leader, both gateways, the registry, the runner and a loopback admin API from one deployment file. `migrate`, `turn` and `inspect` are commands of the same binary.
- **Bound to the holding** (Decision 4). A context carries the epoch of the holding it acts under; every guarded store checks it in the write's transaction, and every request to the model vendor, to an MCP server, or to start a turn is preceded by a check of the holding that takes no lock.
- **Decided from records** (Decisions 9 and 20). A round is quiet, and a wrap-up round concludes, only on positive evidence the platform recorded: each agent's own turn end on the board, and no failed model call, failed tool call, or unsettled call. A runtime's exit code advises only.
- **Turns as host processes** (Decisions 12 and 13). A process runner, for development and tests only, runs `agentswarm turn`: the thin loop, which talks MCP to the MCP gateway and the Messages API to the LLM gateway, nudges a model that answers without a tool once, ends its turn only on a successful `end_turn`, and exits with a code that names how the turn ended. Plan 7d-2 replaces the process with a Docker sandbox.
- **The harness** (Decision 17). `test/e2e/harness` imports no platform code: it starts Postgres and a toxiproxy in containers, a scripted model vendor and scripted MCP servers in the test process, writes a configuration repository and deployments, and drives `serve` from outside, through signals, its admin API, its log and `inspect`.
- **The order.** The harness's foundation comes first (Tasks 1 and 2). Then the schema's levels, the holding, the clocks and the leader, the coordinator's changes, the records of failed tool calls and the rules that read them, the gateways, the runtime and the runner, the deployment and the admin API, `serve`, the fault scenarios, and the documents.

**Tech Stack:**
- Go 1.26
- pgx v5.10.0
- testify v1.12.1
- testcontainers-go v0.44.0 (its core module, `network` package included), with `postgres:17-alpine` and `ghcr.io/shopify/toxiproxy:2.12.0`, the latter driven through its HTTP API
- anthropic-sdk-go v1.80.0, modelcontextprotocol/go-sdk v1.8.0, go.yaml.in/yaml/v3 v3.0.5
- No new module.

**Spec:** `plan/2026-10-11-agent-swarm-platform-spec.md`. Read these before the tasks that implement them:
- Section 2.2: implementation constraints, and where wiring lives;
- Section 5.6: stop rules, quiet, wrap-up rounds and the budget;
- Section 8.2: the failure table, the lease, and the coordinator's crash;
- Section 9.3 and 9.4: tokens, the LLM gateway's request rules, redaction and the audit chain;
- Section 15: the parameters.

Also read ADR-004 (durable execution on Postgres), ADR-006 (the agent runtime and its seven criteria), and Plan 7a (`plan/2026-10-18-plan-7a-coordinator-core.md`): its Decisions 6, 7 and 9, which this plan amends, its "Constraints carried to later plans", and its "Changes made during execution", whose Plan 7d items this plan discharges.

**How this plan was made.** Plan 7a's final review handed Plan 7d a list of constraints. The author split Plan 7d in two on 2026-10-20 and chose a light plan for 7d-1. Its design went through four reviews by a fresh reviewer on Opus 5.5, each with probes on Postgres or Go:
- **Review 1** (1 Critical, 12 Important, 18 Minor). The Critical one: positive evidence that replaced Plan 7a's conditions instead of adding to them would let a vendor's brownout stop runs quiet. The author then decided that an interrupted turn never takes part, that migrations are expand-only with a declared oldest reader, that shutdown drains under the held lease, and that the admin API listens on loopback only.
- **Review 2** (1 Critical, 6 Important, 13 Minor). The Critical one: exit code 11, "finished", mixed limits, unreadable answers and refusals, so quiet could not rest on the runtime's account. Quiet and taking part now rest on the platform's records alone. The author decided that a round with a failed read is not quiet, and that after a rollback the older build skips the items it cannot read.
- **Review 3** (0 Critical, 4 Important, 12 Minor). Among them: the holding check could deadlock against the coordinator's lock (it is now an atomic snapshot), and Plan 3b's runs had ended only 34 of 117 turns by `end_turn`, so quiet was nearly unreachable. The author decided that the runtime nudges a model that answers without a tool once, that a wrap-up round with failures concludes nothing, and that skipped items stay held until the deploy rolls forward.
- **Review 4** (0 Critical, 2 Important, 5 Minor), both Important ones in the records of failed reads: MCP servers report a backend's outage as a tool's own error, and a server down at the first listing vanished silently. The author decided that a failed read, a tool's own error included, counts unless the same agent's later read of the same tool in the round succeeded.

Every finding is answered by a decision below, by a resolution this plan adds, or by a constraint on a later plan.

**The plan was reviewed too.** A fifth reviewer on Opus 5.5 read this plan against the design, the spec, Plan 7a and the code: 0 Critical, 12 Important and 10 Minor findings. Two of the plan's own resolutions had reversed the design: board calls do have audit rows today, so amendment A4 stands as written (Decision 20), and `max_tokens` is sized to the round again (R14). It also found the write path unclassified (Decision 20, R2); five contracts wrong or missing (the deployment file's image and secret forms, the log's producers, the skills' check, the admin's 503, and the errors `recordLater` stops at); a ported test that could not fail; a harness that could leave processes running; an `audit verify` that cannot pass on a file store; and two tasks too large for one brief, now split. Each finding is fixed in the text below.

**How a task runs.** The plan is light: it gives each task its decisions, the interfaces it consumes and produces, and its tests, by name, with what each asserts and why it fails before the implementation. It gives no implementation code. At execution, each task's code is written test first: the implementer writes the listed tests, watches each fail for the reason given, then writes the code. The listed tests are the minimum; more may be added. A test's name may change to match the code's vocabulary, with a ruling in the ledger. Each task's brief carries, verbatim, the Global Constraints, the Decisions and the Contracts it names. Each task is reviewed by a fresh reviewer on Opus 5.5, and the whole branch at the end.

## The author's decisions (2026-10-20)

- **The plan's form:** decisions, interfaces and test lists; code written test first at execution; an Opus review of each task and of the whole branch.
- **The split:** 7d-1 is this plan. 7d-2 is the Docker sandbox runner, on one host.
- **The order:** the fault-injection harness is built first.
- **Quiet and taking part:**
  - they rest on positive evidence, added to Plan 7a's conditions, never replacing them;
  - an interrupted turn never takes part;
  - a round with a read that failed on the server's side, or with a tool's own error, is not quiet, unless the same agent's later read of the same tool in the round succeeded;
  - a wrap-up round with a failed read, a failed model call, or calls that did not settle concludes nothing;
  - when the model answers without calling a tool, the runtime nudges it once, with a fixed platform message, to call `end_turn`.
- **Migrations and rollbacks:**
  - migrations are expand-only, and each declares the oldest build that can read the schema it leaves (`min_reader`), so that a rollback works without down-migrations;
  - after a rollback, the older build skips the items it cannot read, rather than refusing to lead;
  - skipped items stay held until the deploy is rolled forward.
- **Shutdown:** drain under the held lease, then release.
- **The admin API:** loopback only, until Plan 9's sign-in.
- **Plan 7a's M2:** kept as shipped.
- **Test containers:** this project labels its containers, and `make test-clean` removes its labelled containers older than an hour; other projects' containers are left alone.
- **A live probe of the nudge** after Task 12, before the runner and `serve` are built on it (see "The gate after Task 12").

## Global Constraints

- **Module and Go version.** The module path is `github.com/jchen0426/agent-swarm`, with `go 1.26.0`.
- **Vocabulary.** Names come from the spec: work item, incident, run, round, turn, session, board, entry, stop reason, budget, daily cap, lease, holding, epoch. Agents are "AI agents" or "agents"; never "employee".
- **Domain purity.** `internal/work/item`, `internal/work/board`, `internal/work/tokens`, `internal/holding` and `internal/outcome` import no database, network or framework package.
- **Ports.** A port is declared by the package that uses it. Wiring lives in `cmd/agentswarm`.
- **Decide, record, act.** Every decision the coordinator makes is a work record written before its effects, and every effect can be repeated (Plan 7a).
- **Audit records.** Append-only, chained, and written with the database's `clock_timestamp()`.
- **The holding.** Every guarded write runs under a context that carries the epoch of the holding it acts for. Every request to the model vendor, to an MCP server, and every turn's start is preceded by `Holding.Valid` for that epoch.
- **Secrets.** A session token never appears in a record, a log, an error, a board entry, a turn's output file, a process's arguments or environment, or anything the model vendor or an MCP server receives. The runtime holds its tokens in memory and sends them only in request headers.
- **Migrations.** Expand-only. Each declares its `min_reader`, by default its own level; every migration of this plan is at its own level.
- **Libraries.** No new module. Code under `test/e2e` imports the standard library, testify and testcontainers-go only (the depguard rule `e2e`).
- **Test first.** Every test a task lists is written before its implementation and seen to fail for the reason the task gives.
- **Each task ends green:** `golangci-lint run ./...` at `0 issues.`, `make test`, `make test-integration`, and `make test-e2e`.
- **Docker.** Integration and end-to-end tests start containers, so Docker must run. Every container a test starts carries the label `agentswarm.project=agent-swarm`.
- **Language.** Code, comments and documents are in English.

## Review Focus

The design says what `serve` must do, not every condition it will meet. These five are the conditions most likely to bite a person running it that the scenarios do not reach; each has its test in the task that owns the code.

1. **A deployment file with a misspelt key, or a duration without a unit** ("15" for "15s"). A person expects `serve` to refuse to start and name the key, not to run on a default. (`TestADeploymentWithAnUnknownKeyIsRefused` and `TestADurationWithoutAUnitIsRefused`, Task 14)
2. **The host sleeps while `serve` holds the lease**, as a laptop does when its lid closes; Darwin's monotonic clock stops while it sleeps. A person expects that, on waking, nothing is sent or written under the old holding. (`TestAHoldingIsInvalidOnceWallTimePassesItsMarginThoughMonotonicTimeDidNot`, Task 5)
3. **A turn that starts processes of its own**, which ignore their parent's end. A person expects the turn's kill to take them too. (`TestAKilledTurnTakesItsChildrenWithIt`, Task 13)
4. **A turns' output directory that cannot be written**: a full disk, or a wrong permission. A person expects the turn's launch to fail, so the turn is tried again and counts as unstarted, and no agent to be removed for it. (`TestAnOutputFileThatCannotBeCreatedFailsTheLaunch`, Task 13)
5. **A person who retries an opening after a timeout**, while the lease moved to another instance. A person expects one item, not two. (`TestARetriedOpeningAfterATakeoverAdvancesAnItemWhoseOpeningLanded`, Task 8; `TestOpeningAnItemByKey`, Task 14)

## Decisions

The design's decisions, as the four reviews and the author's answers left them. Values in brackets are defaults, and every one that is new goes into spec Section 15 (Task 18).

### Decision 1. One process, three listeners, one deployment file

`agentswarm serve --deployment FILE` runs the coordinator, the leader and both gateways in one process. The wiring is in `cmd/agentswarm/serve.go` (spec Section 2.2).

- **Listeners.**
  - **MCP** and **LLM** are for agents. LLM serves `POST /v1/messages` at its root.
  - **Admin** is on loopback only. A non-loopback address is refused.
  - Every server sets `ReadHeaderTimeout` [10 s] and `ReadTimeout` [30 s], fixed in code. The LLM listener's body cap is the LLM gateway's `MaxBodyBytes`, from one setting.
  - Addresses may be `:0`. Once all three are bound, serve writes them, with its process id, to the addresses file (Contract C2).
  - TLS is Plan 7d-2's.
- **The deployment file** is strict YAML (`go.yaml.in/yaml/v3`, unknown keys refused), as Contract C1 gives it.
- **Serve refuses to start** when:
  - the database server is older than Postgres 17, since `transaction_timeout` needs 17. Serve checks it on a connection that sets no timeout: a server older than 17 refuses a connection that sets one;
  - the coordinator's `Grace` is below either gateway's record timeout;
  - the lease's `TTL − Margin` is not above `RenewEvery`;
  - the take's wait is not below `TTL − Margin`;
  - either pool's transaction or idle timeout is above `TTL − Margin`;
  - the schema is behind this build or incompatible with it (Decision 18);
  - the runner's kind is `process` without `unsafeNoSandbox: true`.
- **Starting without a configuration.** Serve may start with no current revision; an opening then answers 503.
  - A revision becomes current only if every agent's model has a price card and every skill passes the LLM gateway's checks as a system prompt.
  - Otherwise it is refused, logged, and refreshed again.
- **Clocks for each part.**
  - Each gateway's `Now` is the process clock, and deadlines reach the gateways through `Clock.Local`.
  - Every audit record is written with `clock_timestamp()`: tool calls and their outcomes, hides, and model calls, their contents and their outcomes.
- **Signals.**
  - SIGINT and SIGTERM start the drain (Decision 6).
  - A second signal, or the end of serve's standard input under `--exit-with-stdin` (R6), kills every turn's process group (`StopAll`) and exits 1 at once.
  - Listener shutdowns are bounded.
- **`agentswarm migrate`:**
  - it takes the database URL from libpq's environment (`PGHOST` and the rest), or from `--secrets DIR --database REF`;
  - it sets `lock_timeout` [5 s] (`--lock-timeout`);
  - it applies the migrations and records each one's `min_reader`;
  - it refuses a migration whose `min_reader` is above the level of the migration before it while the platform's lease is held and unexpired, unless given `--force`;
  - it prints each migration it applied, then the schema's level.

### Decision 2. Two clocks

- **The leader's clock** is the process's monotonic clock, so fencing depends on no offset. Darwin's monotonic clock stops while the system sleeps, so the holding check (Decision 4) also measures wall time.
- **The coordinator's clock** is the database's time, as this process estimates it.
  - **Sampling.** The offset comes from `SELECT clock_timestamp()` on the lease's pool, a statement that never waits on a lock. It is sampled before each take attempt, the first at start, right after a take and before `TakeOver` reads `Now`, and after each renewal.
  - **Choosing the sample.** It keeps the sample with the shortest round trip within the last [1 min]. A sample's offset is the database's time less the moment halfway through the round trip.
  - **Never backwards.** `Now` never goes back: a correction that would move it back holds `Now` until real time catches up.
  - **No retroactive corrections.** Timers already armed, and deadlines already issued, keep their offset.
- **`Local(t) = time.Now().Add(t.Sub(c.Now()))`**, which carries a monotonic reading.
- **ADR-004's "time base"** is amended:
  - records and the coordinator's deadlines are judged by the database's time;
  - the leader's watch by monotonic time;
  - the gateways' deadlines by the process clock, through `Local`.

### Decision 3. Timers

`At(t, fn)` uses `time.AfterFunc(t − clock.Now())`, with a gate that `At` opens before it returns. `cancel` never waits, and `fn`s run concurrently, on goroutines of their own: the `Timers` port's doc says so (a Plan 7a carry). There is one instance per clock: the coordinator's runs on the database's clock, the leader's on the monotonic one.

### Decision 4. The lease, and writes and external requests bound to their holding

- **The epoch in the context.** `internal/holding`, a package with no dependencies, provides `WithEpoch`, `EpochFrom`, `ErrNoEpoch` and the `Holding` interface.
  - Every guarded store checks `epoch = EpochFrom(ctx)` and the holding's expiry, `FOR KEY SHARE`, in the write's transaction, before its insert. The guarded stores are work records, board events, tool calls and their outcomes, and model calls, their contents and their outcomes.
  - A context without an epoch fails with `ErrNoEpoch`, which never fences and is logged as a bug ("write without an epoch").
  - The coordinator's contexts carry `term.Epoch`, its timers and retries included. `Open` adds the current term's epoch to the request's context.
  - The gateways' contexts carry the session's epoch, their detached writes included.
- **The holding, checked before every external request.**
  - Before each `Turns.Start`, and before every request to the model vendor or to an MCP server, the caller asks `Holding.Valid(epoch)`. That covers each attempt and retry; connects, initializes and tool listings (`Pool.Offered`); and the retry after a 401.
  - **The answer.** `Valid(epoch)` holds only if the epoch is the leader's current one, and the last good renewal, or the take, was sent less than `TTL − Margin` ago, by the larger of the elapsed monotonic and wall times.
  - **How the leader answers.** The leader publishes an atomic snapshot (the epoch, whether it holds, and the last good send's monotonic and wall readings) into an `app.Holding` that serve creates before the leader (R4). `Valid` reads only that snapshot and takes no lock.
  - **Lock order.** The leader comes before the coordinator. Nothing that runs under the coordinator's lock calls into the leader except `Valid`, which takes no lock.
  - **A refusal never fences.** Each refusal is logged ("holding refused").
    - At `Start`, it is a launch failure: the turn is tried again and counts as unstarted.
    - In the LLM gateway, it is an `unavailable` reply.
    - In the MCP gateway, it is an error from `Call` or `Tools`, which the endpoint answers with the `unavailable` code. It is not a read error, so it leaves no failed-read record.
  - **Closing sessions.** Upstream sessions are closed (a DELETE) only while the holding is valid. Afterwards they are dropped unclosed.
  - **What remains.** A window is left between a check and its send. Phase 1's writes need fencing at the executor too (ADR-004, "fencing at dispatch"; carried to the Phase 1 backlog).
- **Not guarded.** Hides and configuration revisions.
- **Bounds.**
  - Both of serve's pools set `transaction_timeout` and `idle_in_transaction_session_timeout` to at most `TTL − Margin` [10 s]. That bounds every statement serve runs, which Plan 10's load trial must keep in mind.
  - The take sets `lock_timeout` to the take's wait [5 s]. A take that waits past it has not taken the lease (R9).
  - A takeover comes at most TTL + `TakeEvery` + one take after the holder goes silent.
  - The audit commands set their own timeouts.
- **A dedicated pool.** The lease's pool serves the takes, the renewals, the release, the clock samples and the schema checks.

### Decision 5. The gateways under the lease

- Sessions exist only while the instance holds the lease.
- The MCP gateway reloads hides on each look, and when it first sees a session of a new epoch.
- After a retake by the same instance, per-run state carries over, and a lane may make the first call wait up to `LastCallWait`. This is accepted.

### Decision 6. Shutdown: drain under the held lease

- **Draining is a state of the leader.** While draining, the leader:
  - cancels the holding's context and fences the coordinator;
  - keeps the holding, the renewals and the watch;
  - never takes the lease;
  - keeps the holding for the drain when a takeover in progress fails because of the drain;
  - ends the drain at once, with nothing released, if the holding is lost.
- **Serve leads under a context that the signals do not cancel.**
- **On a signal:**
  1. The admin API stops taking requests.
  2. The leader starts draining.
  3. The MCP gateway is drained: no new call is taken, and its detached writes finish, within `3 × RecordTimeout`.
  4. The LLM gateway `Drain`s within the drain bound [30 s].
  5. The leader releases the lease and stops.
  6. The listeners and pools close, bounded.
- **Recording later.** The LLM gateway's `recordLater` stops at `holding.ErrLeaseLost`, and at `llmapp.ErrConflict`, which `ModelCalls.End` wraps when the call already has another outcome, as when a sweep settled it (R18).
- **`Done`** closes only after the leader has released the lease, or found it lost.
- **A leader that stops for good** (an incompatible schema, Decision 18) does not drain: the old build must not write into a schema it cannot run on. It fences, releases, closes `Done` and stops; serve exits 1. Calls left without an outcome are swept by the next holding (Decision 7).
- **A second signal** kills every turn's process group and exits at once. The holding then expires, and its calls' outcomes are swept at the next takeover.
- **Failover waits:**
  - after a drain, at most the MCP gateway's drain (`3 × RecordTimeout`) + the drain bound + `TakeEvery` + one take;
  - after a second signal, TTL + `TakeEvery` + one take.

### Decision 7. Model calls left without an outcome

At each takeover, after the schema check, every model call without an outcome that started before `Term.At` gets one:
- the status is `unknown`;
- `weighted` is the stored estimate;
- the usage is estimated as Plan 6b estimates a lost call's, with `max_tokens` read from the call's stored request;
- `spend` is the estimate times the deployment's price reference;
- the reason names who stopped first: another instance, by the holder's name the take read (`Term.PreviousHolder`, R11), or this instance's earlier holding.

The sweep is best effort, call by call, and failures are logged. A sweep that finds the lease gone stops the takeover. The sweep is the LLM gateway's, since the estimate is (R10).

### Decision 8. One daily cap, one day

The deployment's cap feeds the coordinator and the LLM gateway. Both count `SpentToday` from the database's UTC midnight, and `SpentSince` goes.

### Decision 9. Quiet and taking part, from the platform's records

- **The failures a round can leave.** Each is read from the gateways' records, never from notices that can be dropped:
  - **failed model calls:** the round's model calls recorded as refused, failed or of unknown outcome. This is Plan 7a's existing condition;
  - **failed tool calls:** the round's tool calls that Decision 20 counts (reads, writes, board calls and listings), through a new port, `ToolCalls.Failed(run, round)`;
  - **unsettled calls:** a model call of the round (`model_calls.round`) still in flight when the settling wait ends. Calls of earlier rounds do not count: `Spend.InFlight` takes the round. A call hung from an earlier round still holds that round's spend, but no longer bars a later round.
- **Quiet.** A round is quiet only if all of these hold:
  - the instance saw the round;
  - it left none of the failures above;
  - it left no entry of an agent or a person but turn ends;
  - at least one agent was in the round;
  - every agent of the round has its own turn end on the board in that round.
- **Taking part, in a wrap-up round.** The round concludes only if all of these hold:
  - the instance saw the round;
  - it left none of the failures above;
  - at least one agent has its own turn end on the board in it.

  An interrupted turn never takes part.
- **What a turn end is.** It is the agent's `end_turn` call, which the board recorded.
  - The runtime ends its turn only when that call succeeds (Decision 13).
  - A runtime that ends a turn without a successful `end_turn` leaves no turn end.
  - That a runtime calls `end_turn` when its model does is a conformance criterion of every runtime (Decision 13), not something the platform can check from its own records.
- **The record.** `round_ended` records the evidence the decision read:
  - the agents with a turn end;
  - the failed model calls and failed tool calls counted;
  - whether the calls settled.

  The runner's classes may then disagree with the board, and both can be seen.
- **Amendments.**
  - Spec Section 5.6 and Section 8.2, and Plan 7a's Decisions 6 and 7, are amended.
  - Section 8.2 gains `unavailable`, which is not a crash, and whose rounds wait for their time limit when every turn ended so.
  - Section 8.2 also says that a failed tool call, as Decision 20 counts it, leaves its round not quiet.
  - The Plan 7a tests this inverts are listed below ("Plan 7a's tests this plan changes").
- **The property tests** of `internal/work/item` draw the turn ends apart from the classes, `unavailable` included.
- **What follows.** A run's wrap-up rounds that cannot conclude still use up their wrap-ups, so every run stops by its budget, its daily cap, quiet or convergence.

### Decision 10. Board events carry a version

- **The field.** A board event's body gains `v`.
  - A body without `v` reads as version 1.
  - A `v` present and below 1 is invalid.
  - A later version decodes to its version alone.
- **The error.** `board.ErrVersion` is its own error. The coordinator reads it as an item it cannot read (Decision 19).
- This build writes 1.

### Decision 11. The registry at runtime

- **The adapter.** `internal/registry/current` joins the registry with `config.Revision.Lock` and the runtime images, as the coordinator's and both gateways' `Revisions` ports need.
- **Refreshes** run every [1 min]. A revision that fails Decision 1's checks is refused, logged, and refreshed again.
- **Off the coordinator's lock.** Skills are read off the coordinator's lock (Decision 12).
- **A missing commit.** A commit the local repository lacks fails its step, which is tried again. A gateway call that cannot read its revision answers `unavailable`.
- **Keeping the repository current** is the deployment's job.

### Decision 12. The process runner (development and tests only)

`internal/runner/process` implements `app.Turns`: it runs `agentswarm turn` as a host process, one per turn. Serve allows it only with `unsafeNoSandbox: true`.

- **`Start`.**
  - It checks `Holding.Valid` for the context's epoch, creates the turn's output file, calls `cmd.Start` and returns. A launch that fails (the holding refused, the output file, the exec) returns its error, so the turn is retried and counts as unstarted.
  - A goroutine reads the skills from the revision of `Turn.Commit`, a new field, off the coordinator's lock. It then writes the turn's spec (Contract C6) to the child's standard input.
  - A spec that cannot be delivered (the skills unreadable, or the pipe failed) is reported `unavailable`, never `crashed`.
  - A repeated `Start` with the same id starts nothing.
  - The `Turns` port's doc, and the Plan 7a constraint that says a sandbox that fails to start "ends crashed", are amended.
- **Isolation.**
  - Each turn runs in its own process group.
  - Its environment is minimal, with a temporary home and working directory, and the runtime's transport has `Proxy: nil`.
  - Its output goes to a file per turn, in the output directory. The runner drains the pipe and drops the bytes past the cap [1 MiB], so a chatty turn never fails for it.
- **Parent death.** The child's standard input stays open for the turn's life, and the runtime exits on end-of-file.
- **Classes**, which are advisory:
  - a process that ends at or after its deadline is `interrupted`;
  - before it, the exit code gives the class (Decision 13), and any code outside the table is `crashed`;
  - a turn end on the board means `ended`, whatever the runner reports;
  - the class is reported through `Exited` once, never before `Start` returns.
- **Kills.** At the deadline, on `Stop` and on `StopAll`, the runner sends SIGKILL to the process group, and never waits.
- **The binary** is serve's own executable, unless the deployment names another (R5).

### Decision 13. The runtime: `agentswarm turn`, and the gateways' markers

**The runtime** is the thin loop (`internal/runner`), run by `agentswarm turn`, which reads its spec (Contract C6) from standard input.

- **Its toolbox** (`internal/runner/mcpclient`) speaks MCP to the MCP gateway. Tools are listed once per turn, and `Call` returns content blocks, each text block on its own.
- **Its client** is Anthropic's, built with:
  - `option.WithoutEnvironmentDefaults()` and `option.WithMaxRetries(0)`;
  - the gateway's URL, with the LLM token as the key;
  - a request timeout no shorter than the round.
- **Its limits.** `max_tokens` is sized so that the longest answer fits in a round and in the call timeout on the agents' slowest model (Plan 6b's constraint): it is the deployment's `runner.maxTokens` [4,096], the value the mechanism check ran at, which the deployment's check refuses above the LLM gateway's cap [16,384] (R14). Its step limit is [50]. It keeps no token budget or prompt limit of its own: the LLM gateway enforces the run's budget and the request's size.
- **Tokens** are held in memory and sent only in headers.
- **The nudge.** When the model answers without calling a tool:
  - the runtime sends one fixed platform message: "You answered without calling a tool. If you are done, call end_turn; otherwise, continue.";
  - it does so once per turn;
  - a second answer without a tool ends the turn `finished`.

  The text is the platform's, so criterion 5 holds, and it needs no change to the LLM gateway's allowlist. The mechanism check (`agentswarm check`) runs without the nudge unless given `--nudge`, so that its measurements stay comparable with Plan 3's (R21).
- **`end_turn`.** The turn ends `ended` only when the `end_turn` call succeeds. On `unavailable` it is tried once more: a second failure exits 15. "The round has ended" exits 14.

**The LLM gateway's markers.** Every non-2xx reply carries `X-Agentswarm-Outcome`, set on the reply actually sent, including the ones `decline` rewrites. Each reply site is listed and tested:

| Reply | Marker |
|---|---|
| Its own refusals: the allowlist, the budget, escapes, the refusals' cap and its 429, the daily cap; a body over the cap (413) | `refused` |
| A vendor failure passed through after its retries; the vendor marked degraded, failing fast; an answer withheld, lost (502) or unreadable (502) | `vendor` |
| The round's deadline (403), the gateway's cut (504), the session's end, and a token with no live session (401) | `cut` |
| Its own failures: stopping; session, configuration or spend unreadable; a call, answer or refusal not recorded; a wrong path (404); one call at a time (429); an agent missing from the configuration, or a model with no price (403); a holding no longer valid | `unavailable` |

**The MCP endpoint's answers.**
- HTTP 401, `ErrNoSession` from `Call`, and a board tool's "the round has ended" answer with the session-gone code (-32001).
- Its own failures, a holding's refusal, and a board tool's platform failure (the board unreachable, which the call's row records as `platform`, Decision 20) answer with the `unavailable` code (-32002).
- Arguments over its cap get HTTP 413, marked `refused`.
- A tool's error goes back to the model as `isError`.

**An unmarked reply.** A non-2xx reply without a marker reads as `unavailable`, never as `crashed`.

**Exit codes:**

| Code | Class | When |
|---|---|---|
| 10 | ended | the `end_turn` call succeeded |
| 11 | finished | a second answer without a tool, after the nudge; the step limit; the token budget; the prompt limit; `max_tokens` |
| 12 | refused | the LLM gateway's `refused`; the MCP endpoint's `refused`; the model's own refusal (`stop_reason: refusal`) |
| 13 | vendor | the LLM gateway's `vendor` |
| 14 | interrupted | the LLM gateway's `cut`; the MCP session gone; the runtime's own deadline; an `end_turn` refused because the round ended |
| 15 | unavailable | the LLM gateway's `unavailable`, or an unmarked reply; the MCP endpoint's `unavailable`; no HTTP answer at all |
| any other | crashed | a panic; an answer the runtime cannot read (malformed content, an unknown block type, an unknown stop reason); any other error |

**`unavailable` is a new class** (`item.TurnUnavailable`). It is not a crash, and a round whose every turn ended so waits for its time limit. Each outcome has a test of the runtime.

**Conformance.** ADR-006's criteria 1, 2, 3, 5, 6 and 7 are tests of the runtime run as a host process. The suite also checks that a conforming runtime calls `end_turn` only when its model asks for it. Criterion 4, egress, is Plan 7d-2's.

### Decision 14. The admin API

Loopback only (Contract C4). A bearer token, referenced as a secret, is resolved on each request.

- **`POST /v1/items`**, with bodies capped [64 KiB].
  - An `Idempotency-Key` is required. It derives the item's id (R15), and holds for the item's whole life.
  - The opening stores a digest of the request, so a retry with the same key and the same request returns the item, advancing it if its opening landed but it never advanced. The same key with another request answers 409.
  - It answers 409 when this instance does not hold the lease, 409 "held by a later build" for an item this build skips (Decision 19), and 503 while no revision is current.
- **`GET /v1/items/{id}`**: the folded state, the rounds with their decisions and the evidence recorded, and the board's report per round. For a skipped item, it reports the item as held by a later build. It reads from the stores, off the coordinator's lock.
- **`GET /v1/status`** (token): whether this instance leads, its epoch, the schema's state, whether it is draining, and the count of skipped items.
- **`GET /healthz`** (the process is up) and **`GET /readyz`** (up, the schema compatible, not draining) need no token. A standby is ready.

### Decision 15. TLS roots for MCP servers

The deployment's CA bundle is trusted besides the system roots, and `registry hash-tools` gains `--ca`.

### Decision 16. Test containers

`pgtest` and the harness label their containers: `agentswarm.project=agent-swarm` on each, and `agentswarm.data=<path>` on a container whose data is worth keeping. `make test-clean` removes this project's labelled containers that have run for over an hour, after it saves each one's `docker inspect` output and its labelled data (R16).

### Decision 17. The fault-injection harness, first and independent of the runner

The harness lives in `test/e2e/harness`, under tag `e2e`, and imports no platform code.

**Setup.** It:
- uses the binary `make test-e2e` builds;
- starts Postgres and a toxiproxy on one network, both labelled, with one proxy in front of Postgres per `serve` instance;
- runs a scripted model vendor (HTTP on loopback) and scripted MCP servers (HTTPS with a test CA), which tell each request's instance by its credential (R3);
- runs `migrate`;
- writes a configuration repository, and a deployment for each instance;
- runs `serve` with `--exit-with-stdin`, under a small parent process that reads its own standard input from the test process and exits when it closes, so that a test binary that dies takes its parents, their serves and their turns with it (R20).

**Reading state.** `inspect` (Contract C5), `audit verify`, `GET /v1/status` and `GET /v1/items/{id}`, and serve's JSON log on stderr (Contract C3), in which each take carries its epoch and the process's wall time.

**Injecting rows.** `psql`, through `docker exec`.

Scenarios, each with what a person expects:
1. **Convergence.** A converged hypothesis gets its wrap-up round, which the agents end by `end_turn`, and the run stops `converged`. The chain verifies: `audit verify` finds nothing but the file store's `store_unlocked` (R22).
2. **Quiet.** The agents answer, post nothing and call `end_turn`, and the run stops `quiet` after one round.
3. **A nudged turn.** Half the scripted turns answer without a tool. The nudge brings them to `end_turn`, and the round is quiet. A scripted model that ignores the nudge leaves its round not quiet.
4. **A hanging vendor.** The rounds are not quiet, and the run stops by its budget.
5. **A slow vendor.** Answers come after the round's time limit, so the rounds are not quiet.
6. **Vendor failures:**
   - (a) 529s and 500s;
   - (b) a first answer that is ok, then 529s;
   - (c) a 401 or 429 storm, where the turns are `vendor` and each round waits for its limit;
   - (d) an answer the runtime cannot read, where the turns crash and two rounds remove the agents;
   - (e) the step limit or `max_tokens` before `end_turn`.

   None of these rounds is quiet.
7. **The deadline kill.** A turn that never ends, run by a script that ignores its deadline, its standard input and SIGTERM, and sleeps for at most two minutes (R5), is killed at its deadline, and its round is not quiet.
8. **A crash.**
   - SIGKILL the leader.
   - The standby takes over within Decision 4's bound.
   - The dead instance's calls get outcomes.
   - No turn process survives it, and nothing is recorded twice.
9. **A partition.**
   - SIGSTOP the leader beyond the lease's time.
   - The standby takes over within Decision 4's bound.
   - On SIGCONT, the vendor and the MCP servers see no request from the old leader after the standby's take.
   - The old leader's log shows `lease lost` and `fenced`.
10. **The database cut.** The leader fences within its margin, takes the lease again within Decision 4's bound once the database is back, and carries its items on.
11. **A rollback:**
    - (a) a later-version record or board event is injected into one active item, then a takeover: that item is skipped and shown held, and the others go on;
    - (b) a compatible later migration: serve starts;
    - (c) an incompatible one: serve refuses to start, and a running leader stops at its next renewal, without draining, and exits 1. `migrate`'s refusal while a holder is live needs a pending migration the binary does not have, so Task 3 tests it.
12. **The daily cap.** New items are not staffed, and runs stop `cut`.
13. **Shutdown.**
    - SIGTERM with calls in flight.
    - Their outcomes are recorded under the held lease, and the lease is released.
    - The standby takes over within the drain bound + `TakeEvery` + one take.
14. **Tokens.** No session token appears in serve's output, the turns' files, anything the vendor or the MCP servers receive, or `inspect`'s output.
15. **A bad configuration commit.** It is refused, running items go on, a restart starts, and an opening answers 503 until a good commit arrives.
16. **Orphans.** Kill the harness's parent process. Nothing of the run survives.
17. **Failed tool calls:**
    - (a) every read fails on the server's side, and the agents call `end_turn` with nothing to post: not quiet;
    - (b) an agent's read is too broad and is told to narrow it, then the agent ends its turn: the round can still be quiet;
    - (c) a wrap-up round whose reads fail concludes nothing;
    - (d) a server whose backend is down answers `isError` to every read: not quiet;
    - (e) an agent's first read has bad arguments and comes back `isError`; it reads again with fixed arguments, and the read succeeds: the round can be quiet.
    - (f) an agent's write fails because its server is down, and the agent then ends its turn: not quiet.
18. **A server down at the start.** The MCP server is down when `serve` starts, so its tools are missing from the first listing. The round is not quiet.

Clock steps are tested at the component level (Task 5).

### Decision 18. Schema compatibility

- **Levels.** A migration's level is its number. The build's level is its last migration's. The schema's level is the level of its last applied migration, and its `min_reader` the largest `min_reader` of its applied migrations.
- **Compatible.** A build runs on a schema whose level is at least its own (otherwise the schema is behind) and whose `min_reader` is at most its own level (otherwise it is incompatible).
- **The allowlist.** A migration may declare a `min_reader` below its own level (Contract C8) only if every statement in it is on an allowlist:
  - a column added, nullable or with a constant default, to a table no audit trigger chains;
  - a table added that no audit trigger chains;
  - an index added that is not unique.

  A column added to an audited table is off the allowlist, because a chain form lists its columns and a new column would go unchained. So are constraints, triggers, chain forms, changed defaults, backfills, rewrites and dynamic SQL. Every such migration's `min_reader` is its own level.
- **A test** classifies every statement of every migration and enforces the allowlist.
- **Compatibility is checked** at start, at each take and at each renewal: one query on the lease's pool.
  - At start, a schema behind or incompatible refuses the start.
  - At a take or a renewal, it makes the leader fence, release the lease, close `Done` and stop for good, without draining (Decision 6). Serve exits 1.
  - `/readyz` and `/v1/status` show it.
- **`migrate`'s guard** (Decision 1) keeps a live older holder from being migrated under.
- **The N−1 test.** `make test-compat` runs the previous release's harness scenarios 1, 2 and 8, from a worktree at the previous tag, against a schema this build migrated. It then has this build verify the chain and inspect the items they left: the roll-forward check. Until a release is tagged, it says so and passes.
- **The deploy order.** For an incompatible migration: stop the older instances, then migrate, then start the new build.
- **This plan's own migrations.** Every one has a `min_reader` equal to its own level, so none is compatible: no release precedes them. They include Decision 20's.

### Decision 19. Items this build cannot read are skipped, one by one (amends Plan 7a's Decision 9)

- **At a takeover**, an active item whose work records, or whose run's board events, are of a later version is skipped:
  - it is logged once ("item skipped");
  - it is counted in `/v1/status`;
  - it is answered as held by a later build (Decision 14);
  - it is left alone.

  The other items are carried on.
- **During a holding**, a step that meets a later version:
  - ends the run's sessions (`Sessions.EndRun`);
  - stops its agents' turns (`Turns.Stop`);
  - cancels the run's timers, `settle`'s own retry included;
  - arms no retry, and leaves the item alone until the next takeover.

  `record`'s fold refuses the item's next record for the same reason.
- **Skipped items stay held** until a build that can read them leads. A permanent rollback that never rolls forward is not supported.
- **Only a schema this build cannot run on** stops a leader for good (Decision 18).
- **Amendments.**
  - Spec Section 8.2's row for a coordinator crash, Plan 7a's Decision 9, and its tests of the version refusal become the skip.
  - Plan 7a's Plan 7b constraint on new record kinds is amended: a new kind makes older builds skip the items that hold it.
  - A rule is added for later plans: a platform-wide control, such as a kill switch or Plan 7b's queue limit, comes with a migration whose `min_reader` is its own level, so that an older build cannot ignore it.

### Decision 20. Tool calls that failed, recorded by their side

- **What the outcome records.** A tool call's outcome gains `failure`, the side its failure was on:

  | `failure` | The call's failure |
  |---|---|
  | `server` | **A read:** a connection failure; a degraded server failing fast; an upstream timeout; an HTTP 5xx; a JSON-RPC error other than invalid parameters; no capacity (the server's slots full until the call's time ends); a tool needing input (`ErrNeedsInput`); a tool's error that cannot be read or redacted; a look that runs out of time; a look's `ErrTooLarge`. **A listing:** the server unreachable when a session first lists its tools. **A write:** its look at its server fails; a candidate read the write needs fails on the server's side, so the write is refused. |
  | `platform` | A hides reload that fails. **A write:** the board unreachable when the write is checked; the session not checked again; the proposal, or its summary, not recorded. **A board call:** the board unreachable. |
  | `tool` | A tool's own error (`isError`), which is how MCP servers report both a backend's outage and an agent's bad arguments (R1). |
  | `agent` | A partial or oversized output ("narrow the query"); JSON-RPC invalid parameters; the session's calls in flight at their cap, for reads, writes and board calls alike; a write refused by its policy, its scope, or the cap on refused writes. |
  | empty | A call that did not fail, or one cut short by its round's deadline or its run's stop. |

  `ServerError` keeps the JSON-RPC code, to tell invalid parameters apart.
- **The platform's own reads**, which the gateway makes for a write's candidates and facts with no agent, record their side the same way, for the audit. They carry no round, so they are not counted: the write each serves records the side that refused it. A candidate read that fails, or whose look fails before it reads, refuses its write, which records that side; a fact that cannot be read leaves its write `recorded`, and a recorded write is an entry, so its round is not quiet anyway.
- **What makes no record.** A revision that cannot be read, a `Begin` that fails, and a holding's refusal make no record. The call is answered `unavailable`, so its turn ends (exit 15) without a turn end.
- **A server unreachable when a session first lists its tools.** Its tools are left out of the list, and the gateway records a failed call for the session's round: a `tool_calls` row whose server is that server and whose tool is `tools/list`, with an outcome `read_error` whose `failure` is `server`. The list holds for the round (criterion 7), so nothing in the round recovers it. A holding's refusal inside a listing answers the listing `unavailable`.
- **Board calls** keep their `tool_calls` rows, with no server, as today (`internal/actions/app/board.go`), and amendment A4 holds as written: a board call's platform failure records `platform` and is answered `unavailable`; the runtime tries `end_turn` once more, and a retry that succeeds recovers the failed first try. A board call whose outcome cannot be stored cannot record `platform` either: its row stays without an outcome, which counts at the round's end with no recovery, so the round is not quiet. That is the safe side, for a database that fails in the middle of a call.
- **Which failed calls count**, through `ToolCalls.Failed(run, round)`:
  - a call of the round whose `failure` is `server`, `platform` or `tool`, unless a later call of the same tool (the same server, or none, and the same tool name) by the same agent in the same round succeeded (`ok`, or `recorded` for a write): an agent that fixes its arguments and calls again recovers, and a backend that stays down does not;
  - each of the round's tool calls still without an outcome when the coordinator reads them, after the round's grace, which counts as `platform`.

  Agent-side failures and cut calls never count.
- **The chain.** `failure` is a new column of `tool_call_outcomes`, chained by a new form, chain form 2, so this migration's `min_reader` is its own level. The migration sets `search_path` again on every function it replaces or creates, as migration 0005 does, since `CREATE OR REPLACE FUNCTION` drops the setting. The audit's `Tables` check also reads each chain function's `proconfig`, and reports a function whose search path is not pinned.
- **Every failure site of the MCP gateway** (reads, writes, board calls, listings and the platform's own reads) is classified, and each site has a test.
- **The board's `failed_read` notices** stay as they are, for people to see. They no longer decide anything.

### Resolutions this plan adds to the design

The design left these open; each is a choice made here, and what it costs if it is wrong.

- **R1. A tool's own error gets its own `failure`, `tool`.** The design had it on the agent's side, which never counts, and the author's answer counts it. Cost if wrong: a value of the chain's column to migrate later.
- **R2. Writes and board calls count as reads do.** The design classified reads only: a write that failed on an outage, followed by a turn end, would leave an outage round quiet, which the author's decision on failed reads exists to prevent. So the port is `ToolCalls`, and the evidence `failedToolCalls`. The platform's own reads carry no round and are not counted: the write each serves records the side that refused it. Cost if wrong: a round whose only failure was a write that did not matter to the run is not quiet.
- **R3. The harness tells instances apart by credentials, not paths.** This amends the design's "with one path per instance" (Decision 17): an MCP server's endpoint lives in the shared configuration, so both instances use the same URL; each instance's secrets directory holds its own vendor key and server credential instead. Cost if wrong: none outside the tests.
- **R4. The holding's snapshot is its own object, `app.Holding`, made before the leader,** so that serve can hand it to the runner and the gateways before the coordinator and the leader exist. Cost if wrong: a wiring change.
- **R5. The deployment may name the process runner's binary** (`runner.binary`, default: serve's own executable), so that scenario 7 can run a turn that ignores its deadline. It runs only with `unsafeNoSandbox: true`. Cost if wrong: a test-only setting a person could misuse on a host where turns already run unsandboxed.
- **R6. `--exit-with-stdin`** makes serve kill every turn's process group and exit at once, as on a second signal, when its standard input closes. Cost if wrong: none outside the harness.
- **R7. The wire's constants live in `internal/outcome`**: the marker's header and values, the MCP endpoint's two codes, and the exit codes, shared by the gateways, the runtime and the runner. Cost if wrong: a package to fold elsewhere.
- **R8. The MCP endpoint's codes** are -32001 for a session gone and -32002 for `unavailable`, in JSON-RPC's range for servers. Today every error answers -32600 "the session has ended". Cost if wrong: a code to change in two places.
- **R9. A take that waits past its wait reports the lease not taken,** with no error: only a holding's writers hold its row. The leader tries again after `TakeEvery`. Cost if wrong: a slow take reads as a busy lease in the log.
- **R10. The sweep is the LLM gateway's** (`llmapp.Gateway.Sweep`), which the coordinator's takeover calls through a port, since the estimate of a lost call is the gateway's. Cost if wrong: a port to move.
- **R11. `Term` gains `PreviousHolder`,** the holder's name the take read, for the sweep's reason. Cost if wrong: one field.
- **R12. Bodies keep version 1.** `item.Opened` gains the request's digest, `item.RoundEnded` its evidence, and turns the `unavailable` class, at version 1: no release has been tagged, so no older build reads them. Decision 19's rule applies from the first release on. Cost if wrong: none until a release.
- **R13. A migration declares an older reader on its first line,** `-- agentswarm:min_reader N` (Contract C8); without it, its `min_reader` is its own level. Cost if wrong: a syntax to change before any migration uses it.
- **R14. `max_tokens` is the deployment's `runner.maxTokens` [4,096],** the value the mechanism check ran at (`agentswarm check --max-tokens`), which fits a [150 s] round on a model that writes 60 tokens a second. The deployment's check refuses one above the LLM gateway's cap [16,384]. Fitting it to the slowest model's speed stays the deployment's job, and Plan 7d-2's live checks measure it (Plan 6b's constraint, carried). The step limit is [50], the mechanism check's default. Cost if wrong: answers cut at 4,096 tokens, or a value to retune.
- **R15. An item's id from its key** is a UUID of version 8 whose bits are the first 128 of SHA-256 over the tenant, a zero byte, and the key, with the version and variant bits set: it has the form of the ids the platform makes (UUIDv7), and cannot be one of them. Cost if wrong: ids to migrate.
- **R16. `make test-clean` backs up what it removes**: each container's `docker inspect` output, and the path its `agentswarm.data` label names, under `.test-clean/` (git-ignored). The author's standing rule asks for a backup of every deletion. Cost if wrong: disk space.
- **R17. `migrate` and the harness for `make test-compat`.** The harness runs `migrate` with `AGENTSWARM_MIGRATE_BIN` when it is set, and uses the database `AGENTSWARM_E2E_DATABASE` names instead of starting its own, so that a later build can migrate the schema and read the rows this build's scenarios leave. Cost if wrong: the first release's compatibility test needs a harness change.
- **R18. Shared sentinels.** `ErrLeaseLost` moves to `internal/holding`, and `internal/work/app` keeps it as an alias: every guard's refusal wraps it, and both gateways can test for it without importing the coordinator. `internal/llm/app` declares `ErrConflict`, which `ModelCalls.End` wraps when a call already has another outcome, beside the `postgres.ErrConflict` it returns today, so that both match. Cost if wrong: an alias to drop.
- **R19. `app.ErrNoRevision`.** The coordinator's `Open` and its start return it while no revision is current, so that the admin API can answer 503. Cost if wrong: none.
- **R20. The harness's parent reads its own standard input** from the test process, and exits when it closes: a test binary that dies, as on go test's timeout, takes its parents, their serves and their turns with it. `make test-e2e` gets an explicit timeout [60 min]. Cost if wrong: processes left running, which `make test-clean` does not find.
- **R21. The nudge is an option of the runtime's loop** (`runner.Config.Nudge`), on in `agentswarm turn`, and off in the mechanism check unless `--nudge` is given (the gate after Task 12), so that a rerun of Plan 3's check measures what it measured. `runner.Config.System` becomes a list, each skill its own system block. Cost if wrong: a rerun of the check differs from Plan 3's.
- **R22. `audit verify` in the harness** runs `audit anchor` into a file store first. A file store cannot lock its objects, so verification always reports `store_unlocked` there; "the chain verifies" means that finding and no other. Cost if wrong: none outside the tests.

## Contracts between tasks

Tasks that never see each other's code meet here. A task that produces one of these follows it exactly; a task that consumes one relies on nothing else.

### C1. The deployment file (Task 2 writes it, Task 14 reads it, Task 15 runs it)

Strict YAML: an unknown key is refused, by its path. Durations are Go durations with a unit (`15s`, `1m`); a bare number is refused. Sizes are integers, in bytes. A secret reference has the form `secret://<path>` and names a file under the secrets directory (`internal/secrets/files`); any other form is refused. An image is `sha256:` and 64 lowercase hexadecimal digits, as `config.Revision.Lock` takes it; any other form is refused.

```yaml
tenant: example                     # required
holder: instance-a                  # required: this instance's name, as the lease records it
secrets: /etc/agentswarm/secrets    # required: the secrets directory
database: secret://database/url     # required: the database's URL
configuration:
  repository: /srv/agentswarm/config  # required: a local Git repository
  ref: refs/heads/main                # required: a full reference; the registry's Git source refuses a short name
listeners:
  mcp: 127.0.0.1:7401               # required
  llm: 127.0.0.1:7402               # required
  admin: 127.0.0.1:7403             # required, loopback only
  addresses: /run/agentswarm/addresses.json  # required (C2)
admin:
  token: secret://admin/token       # required
vendor:
  baseURL: https://api.anthropic.com  # required
  key: secret://vendor/key          # required
  apiVersion: "2023-06-01"          # required
prices:
  reference: 3000000                # required: pico-USD per cost-weighted token
  dailyCap: 20000000                # required: cost-weighted tokens per UTC day
  cards:                            # required: one per model an agent uses (cost.Card)
    claude-sonnet-5-5:
      threshold: 0
      base: {input: 1000000, output: 5000000, cacheWrite5m: 1250000, cacheWrite1h: 2000000, cacheRead: 100000}
      # long: {...} when threshold > 0
runner:
  kind: process                     # required: "process" in this plan
  unsafeNoSandbox: true             # required with kind process
  binary: ""                        # optional (R5): default, serve's own executable
  output: /var/lib/agentswarm/turns # required: one file per turn
  outputCap: 1048576                # optional [1 MiB]
  maxTokens: 4096                   # optional [4,096] (R14): at most the LLM gateway's cap [16,384]
  images:                           # required: each runtime the agents name -> its image
    thinloop: sha256:0000000000000000000000000000000000000000000000000000000000000000
mcp:
  caBundle: /etc/agentswarm/mcp-ca.pem  # optional: PEM, trusted besides the system roots
timing:                             # optional; each key defaults as below
  lease: {ttl: 15s, renewEvery: 5s, margin: 5s, takeEvery: 5s, takeWait: 5s}
```

The `timing` keys, each optional:

| Key | Default | What it sets |
|---|---|---|
| `lease.ttl`, `lease.renewEvery`, `lease.margin`, `lease.takeEvery` | [15 s], [5 s], [5 s], [5 s] | The lease (spec Section 8.2) |
| `lease.takeWait` | [5 s] | The take's `lock_timeout` (new) |
| `database.transactionTimeout`, `database.idleTimeout` | [10 s], [10 s] | Both pools' `transaction_timeout` and `idle_in_transaction_session_timeout` (new) |
| `coordinator.grace`, `coordinator.settleWait`, `coordinator.settlePoll`, `coordinator.retry` | [5 s], [30 s], [1 s], [5 s] | `app.Options` |
| `llm.recordTimeout`, `llm.lastCallWait`, `llm.vendorTimeout`, `llm.coolDown` | [5 s], [15 s], [10 min], [60 s] | The LLM gateway's `Options` |
| `mcp.recordTimeout`, `mcp.callTimeout`, `mcp.verifyEvery`, `mcp.lookTimeout`, `mcp.coolDown` | [5 s], [60 s], [30 s], [10 s], [60 s] | The MCP gateway's `Options` and its servers' limits |
| `runtime.slack` | [2 s] | Added to a turn's deadline in its spec (new) |
| `drain` | [30 s] | The drain bound (new) |
| `registry.refresh` | [1 min] | The registry's refresh (new) |

Every other parameter is fixed in serve. The MCP gateway's parameters take the values of its package's `Defaults`. The LLM gateway's `Options` have no defaults, so serve sets them: to spec Section 15's values for the request size [512 KiB], the `max_tokens` cap [16,384], the shadow cut [80%], the refused requests per session per round [10], and the vendor calls at once [10] and degraded after [3] failures; and the vendor's retry backoff to [1 s, 2 s, 4 s], which Section 15 gains (Task 18).

### C2. The addresses file (Task 15 writes it, the harness reads it)

Written once, atomically (a temporary file renamed), after all three listeners are bound and before the leader starts:

```json
{"pid": 4242, "mcp": "127.0.0.1:53001", "llm": "127.0.0.1:53002", "admin": "127.0.0.1:53003"}
```

### C3. Serve's log (Tasks 5, 6, 11, 13, 14 and 15 write it; the harness reads it)

JSON lines on stderr (`log/slog`'s JSON handler, one logger that serve hands to every component). Every line has `time`, `level` and `msg`. The harness reads these messages:

| `msg` | Attributes | Logged by |
|---|---|---|
| `serve started` | `holder`, `level` (the build's schema level) | serve (Task 15) |
| `revision current` | `commit` | the registry's adapter (Task 14) |
| `revision refused` | `commit`, `reason` | the registry's adapter (Task 14) |
| `lease taken` | `epoch`, `wall` (RFC 3339, nanoseconds, the process's wall clock at the take) | the leader (Task 5) |
| `lease lost` | `epoch`, `reason` | the leader (Task 5), once at every end of a holding other than its release: a renewal that found the lease lost, the watch, a write that found the lease gone |
| `fenced` | `epoch` | the leader (Task 5), whenever it fences its coordinator: at every end of a holding, and at a drain |
| `holding refused` | `epoch`, `request`: `turn`, `vendor` or `mcp` | the runner (Task 13); the LLM and MCP gateways (Task 11) |
| `item skipped` | `item`, `reason` | the coordinator (Task 6) |
| `turn started` | `turn`, `pid` | the runner (Task 13) |
| `draining` | `epoch` | the leader (Task 5) |
| `lease released` | `epoch` | the leader (Task 5) |
| `schema incompatible` | `reason` (the error's text, which names the levels) | serve at start (Task 15); the leader at a take or a renewal (Task 5) |
| `serve stopped` | `code` | serve (Task 15) |

No line carries a session token.

### C4. The admin API (Task 14 serves it, the harness calls it)

Every `/v1/` route needs `Authorization: Bearer <admin token>`; a missing or wrong token answers 401. Errors are `{"error": "<text>"}`.

- `POST /v1/items`, with `Idempotency-Key: <1 to 128 printable ASCII characters>` and a body of at most [64 KiB]:
  ```json
  {"type": "incident", "title": "checkout fails", "routing": {"service": "checkout"}, "principal": "", "responders": []}
  ```
  - 201 `{"id": "<uuid>", "state": "<state>"}` for a new item; 200 with the same body for a retry with the same key and request;
  - 400 for a missing key, a body that does not decode, or an opening the coordinator refuses as invalid (`app.ErrInvalid`);
  - 409 for the same key with another request (`app.ErrKeyReused`), when this instance does not hold the lease (`app.ErrNotHeld`), and, with `"error": "held by a later build"`, for a skipped item (`app.ErrSkipped`);
  - 413 for a body over the cap;
  - 503 while no revision is current (`app.ErrNoRevision`).
- `GET /v1/items/{id}`:
  ```json
  {"id": "...", "state": "...", "stop": "quiet", "heldByLaterBuild": false,
   "rounds": [{"round": 1, "wrapUp": false, "ended": "turns", "quiet": true,
               "evidence": {"turnEnds": ["alpha", "beta"], "failedModelCalls": 0, "failedToolCalls": 0, "settled": true},
               "report": {}}]}
  ```
  `stop` is empty while the run goes on; `report` is the board's report of the round, as the board marshals it. A skipped item answers `{"id": "...", "heldByLaterBuild": true}`. An unknown id answers 404.
- `GET /v1/status`: `{"leading": true, "epoch": 3, "schema": "compatible", "draining": false, "skipped": 0}`, where `schema` is `compatible`, `behind` or `incompatible`.
- `GET /healthz`: 200 `ok`. `GET /readyz`: 200 `ready` when the schema is compatible and serve is not draining, 503 with the reason otherwise.

### C5. `agentswarm inspect` (Task 15 writes it, the harness reads it)

`agentswarm inspect --deployment FILE item ID | board RUN | calls RUN` reads the database and prints JSON lines:
- `item ID`: one line per work record, `{"seq": 1, "kind": "item_opened", "v": 1, "at": "...", "body": {...}}`;
- `board RUN`: one line per board event, in the same form;
- `calls RUN`: one line per model call, `{"call": "model", "id": "...", "agent": "...", "round": 1, "model": "...", "started": "...", "status": "...", "reason": "..."}`, and one per tool call, `{"call": "tool", "id": "...", "agent": "...", "round": 1, "server": "...", "tool": "...", "name": "...", "started": "...", "status": "...", "failure": "...", "reason": "..."}`, whose `name` is `<server>/<tool>`, or `<tool>` for a call with no server.

A record or event of a later version prints `"raw": "<its body as stored>"` in place of `body`. Bodies print as stored, and are already redacted.

### C6. A turn's spec (Task 13 writes it, Task 12 reads it)

One JSON object on the runtime's standard input, at most [1 MiB], followed by nothing until the runner closes the pipe:

```json
{
  "turn": "0193...", "run": "0193...", "agent": "alpha", "round": 2,
  "mcp": {"url": "http://127.0.0.1:53001/", "token": "aswm_..."},
  "llm": {"url": "http://127.0.0.1:53002", "token": "aswl_..."},
  "model": "claude-sonnet-5-5",
  "system": ["<skill text>", "..."],
  "prompt": "<the round's prompt>",
  "deadline": "2026-10-20T10:00:02.5+11:00",
  "maxTokens": 4096
}
```

`system` holds the agent's skills, each sent as its own system text block. `deadline` is the round's deadline, in the process's clock, plus the runtime's slack, in RFC 3339 with nanoseconds. `maxTokens` is the deployment's `runner.maxTokens`. Nothing in the spec is written anywhere else.

### C7. `internal/outcome` (Task 11 makes it; Tasks 12 and 13 use it)

```go
// Package outcome holds the wire's constants between the gateways, the
// runtime and the runner.
package outcome

// Header carries a gateway's account of a reply that is not a success.
const Header = "X-Agentswarm-Outcome"

// The values of Header.
const (
	Refused     = "refused"
	Vendor      = "vendor"
	Cut         = "cut"
	Unavailable = "unavailable"
)

// The MCP endpoint's JSON-RPC error codes.
const (
	CodeSessionGone = -32001
	CodeUnavailable = -32002
)

// The exit codes of agentswarm turn. Any other code is a crash.
const (
	ExitEnded       = 10
	ExitFinished    = 11
	ExitRefused     = 12
	ExitVendor      = 13
	ExitInterrupted = 14
	ExitUnavailable = 15
)
```

### C8. A migration's reader (Task 3 reads it)

A migration that may be read by older builds says so on its first line:

```sql
-- agentswarm:min_reader 9
```

`N` is at least 1 and at most the migration's own level. Without the line, its `min_reader` is its own level.

### C9. The harness's API (Task 2 makes it; Tasks 3 and 15 add to it; Tasks 12, 16, 17 and 18 use it)

Package `harness`, in `test/e2e/harness`, under tag `e2e`. The names below are binding; their bodies are the tasks'.

```go
// Made by Task 2.

// New starts a harness: Postgres and, with Options.Toxiproxy, a toxiproxy,
// on one network; the test CA; the scripted vendor; and the scripted MCP
// servers. With AGENTSWARM_E2E_DATABASE set, it starts no Postgres and uses
// that database (R17). t.Cleanup stops all of it.
func New(t *testing.T, opts Options) *Env

type Options struct {
	Postgres   string       // image; "" means postgres:17-alpine
	Toxiproxy  bool
	NoDatabase bool         // no Postgres, for tests of the scripted servers alone (added by Task 2)
	Servers    []ServerSpec // the scripted MCP servers
}

type ServerSpec struct {
	Name  string
	Tools []ToolSpec // name, description, input schema
	// Plain serves HTTP without TLS: a stand-in for the MCP gateway in
	// Task 12's conformance tests.
	Plain bool
}

func (e *Env) Psql(t *testing.T, sql string) string           // runs psql in the Postgres container
func (e *Env) DatabaseURL(instance string) string             // through the instance's proxy when there is one
func (e *Env) Cut(t *testing.T, instance string)              // the instance's database connections fail
func (e *Env) Restore(t *testing.T, instance string)
func (e *Env) Vendor() *Vendor
func (e *Env) Server(name string) *MCPServer
func (e *Env) CA() string                                     // the test CA's PEM file
func (e *Env) Config(t *testing.T, c ConfigSpec) *Repo        // writes and commits the configuration repository
func (r *Repo) Commit(t *testing.T, change func(dir string)) string
func (e *Env) Deployment(t *testing.T, instance string, d DeploymentSpec) string // writes the deployment and its secrets (C1), returns its path
// StartParent runs argv under a parent process (R20): the parent holds the
// child's standard input open, and exits, closing it, when its own standard
// input, a pipe the test process holds, closes.
func (e *Env) StartParent(t *testing.T, argv, env []string) *Parent
func (p *Parent) Kill(t *testing.T)  // SIGKILL to the parent
func (p *Parent) Close(t *testing.T) // closes the parent's own standard input
func ParentMain()                   // the parent mode, which e2e's TestMain runs when AGENTSWARM_E2E_PARENT is set

func (v *Vendor) Script(fn func(r *VendorRequest) VendorReply)
func (v *Vendor) Requests(instance string) []VendorRequest // every request, headers and body, with its arrival time
func (v *Vendor) All() []VendorRequest                     // every request, whichever key it carried

func (s *MCPServer) Script(tool string, fn func(c *ToolCall) ToolReply)
func (s *MCPServer) Down()
func (s *MCPServer) Up()
func (s *MCPServer) Requests(instance string) []MCPRequest
func (s *MCPServer) All() []MCPRequest

// Added by Task 2's review: the scripted servers answer a missing or
// unknown key or bearer with 401, and keep the request under "". A test
// that uses them as stand-ins for the gateways (Task 12) accepts its
// session tokens for a named instance. Pinning goes through a door of
// its own, which asks for no credential and refuses tools/call.
func (v *Vendor) AcceptKey(t *testing.T, instance, key string)
func (s *MCPServer) AcceptCredential(t *testing.T, instance, credential string)
func (s *MCPServer) PinURL() string

// Added by Task 3.
func (e *Env) Migrate(t *testing.T) string        // runs agentswarm migrate, or AGENTSWARM_MIGRATE_BIN's (R17); returns its output
func (e *Env) AuditVerify(t *testing.T) []string // runs audit anchor into a file store, then audit verify; returns the findings' kinds (R22)

// Added by Task 15.
func (e *Env) Serve(t *testing.T, instance, deployment string) *Instance // serve --exit-with-stdin under StartParent; waits for the addresses file (C2)
func (i *Instance) Addresses() Addresses
func (i *Instance) Log() *Log
func (i *Instance) Signal(t *testing.T, sig syscall.Signal) // to serve itself
func (i *Instance) KillParent(t *testing.T)
func (i *Instance) Wait(t *testing.T, timeout time.Duration) int // serve's exit code
func (i *Instance) Admin() *Admin                                // C4
func (i *Instance) Inspect(t *testing.T, args ...string) []map[string]any // C5
func (l *Log) WaitFor(t *testing.T, msg string, match func(map[string]any) bool, timeout time.Duration) map[string]any
func (l *Log) Lines() []map[string]any
```

`ConfigSpec` holds the agents (name, model, skill text), the scripted servers they are granted, and the work item type (its round's time limit, its budget, and its convergence thresholds; the wrap-ups a run may open are the platform's constant, not configuration); its agents name the runtime `thinloop`, which every deployment the harness writes maps to an image, and its repository's ref is `refs/heads/main` (`Repo.Ref()`). `DeploymentSpec` holds what a scenario changes in C1: the `timing` keys, the daily cap, `runner.binary`, `runner.maxTokens`, and whether the MCP CA is given; the harness fills in the rest. `VendorReply` can answer a Messages API body, a status with a body, a delay, a hang until the test ends, or a dropped connection; helpers build the answers (`ToolUse`, `Text`, `EndTurn`, `Usage`). `ToolReply` can answer a result, an `isError` result, a JSON-RPC error with a code, an HTTP status, a delay, or a dropped connection. Every request is kept from the moment its handler starts, with its headers, the body read so far, and a flag when the body broke off; an MCP server tracks its sessions, answers 404 to an unknown one, and ends them on DELETE and on `Down` or `Up`; a script that panics fails its test. The scripted vendor tells agents apart by a line each agent's skill carries ("You are alpha."), which `ConfigSpec` writes. The go-sdk client's first request to an MCP server is `server/discover`, which a scripted server answers -32601 before the client falls back to `initialize`: a scenario that counts a server's requests counts from `initialize`.

## Plan 7a's tests this plan changes

Each is changed in the task named, and the change is in that task's report:
- **Decision 8, Task 4:** `TestACallCountsInTheDayItWasAdmittedAgainst` counts the day through `SpentToday`.
- **Decision 19, Task 6:** `TestATakeoverRefusesRecordsItCannotRead` (restart), `TestALeaderStopsOverRecordsItCannotRead` and `TestALeaderWhoseLaterTakeoverFindsALaterVersionSaysSoOnDone` (leader) become the skip. `TestARecordOfAnotherVersionIsRefused` (item) stands: the fold still refuses.
- **Decision 9, Task 10:** `TestARoundWithNothingOnTheBoardStopsTheRunQuiet`, `TestOnlyATurnThatEndedFinishedOrWasInterruptedTookPart` (inverted: an interrupted turn never takes part), `TestAWrapUpRoundNoAgentTookPartInConcludesNothing`, `TestATurnNeverHeardFromCountsAsInterrupted`, `TestAConvergedHypothesisGetsAWrapUpRoundThenTheRunStops`, `TestAConvergedRunConcludesWithItsWrapUpHypothesis` and `TestAWrapUpRoundCountsAsItEnds` (item); `TestAQuietRoundStopsTheRun`, `TestARoundWhoseModelCallsFailedOrDidNotSettleIsNeverQuiet`, `TestAnItemRunsUnderTheRevisionItLocksAtStart` (coordinator), and `TestARoundHeldWithAThirdOfItsTimeLeftOrMoreIsSeen`, `TestATurnStartedWithAThirdOfTheRoundLeftOrMoreIsInterrupted` and `TestACrashAtAnyStepIsCarriedOnFromTheRecords` (restart). A quiet round and a concluding wrap-up round now need their agents' turn ends, so the coordinator's test harness posts an agent's `end_turn` on the board for each turn it ends `ended`.
- **Decision 13 and R8, Task 11:** the MCP endpoint's tests that expect -32600 "the session has ended".

## Not in this plan

- **Plan 7d-2:** the Docker sandbox runner and the runtime image; criterion 4 (egress) and the sandbox's network; TLS between sandboxes and gateways; the harness on Docker; Plan 6b's live checks, which also measure how often turns end by `end_turn` after the nudge; the topology of two instances and their repositories.
- **Plan 7b:** the queue, the kill switch, close, reopen, auto-close, budget extensions and links.
- **Plan 7c:** inbound events and trigger mappings.
- **Plan 8:** arms, the measurement window, finality and shadow runs.
- **Plan 9:** sign-in for the admin API, and the interface.

## Constraints carried to later plans

- **Plan 7d-2:**
  - the Docker runner implements `app.Turns` as Decision 12 says, the holding check and `unavailable` for an undeliverable spec included;
  - criterion 4, the network, and TLS between sandboxes and gateways;
  - the harness's scenarios run on Docker;
  - Plan 6b's live checks, the rate of turns that end by `end_turn` after the nudge, and the output rate of the agents' slowest model, against which `runner.maxTokens` is checked to fit a round and the call timeout (Plan 6b's constraint);
  - the topology of two instances.
- **Plan 7b:**
  - new record kinds make older builds skip their items (Decision 19);
  - platform-wide controls come with a migration whose `min_reader` is its own level;
  - the holding is checked before every external request.
- **Plans 7c and 9:** the holding is checked before every external request, including open-time reads and notifications.
- **The Phase 1 backlog:** fencing at the executor for writes.
- **Plan 10:** the bound of [10 s] on every statement serve runs; an epoch column on guarded rows, if the audit should show which holding wrote each row.
- **The first release:** `make test-compat` runs for real once a tag exists (Decision 18).
- **The first migration with an older reader:** before it, each holder records its build's level on its lease, and `migrate`'s guard compares a pending migration's `min_reader` with the live holders' levels. Today the guard compares with the schema's level, which a holder's build may be below once compatible migrations have run (Task 3's review). The allowlist is hardened then too: a column of a domain type that is NOT NULL or has a volatile default, and a new table whose `REFERENCES` put foreign-key triggers on an existing table, pass it today.

Plan 7a's Plan 7d constraints are discharged here: the runner's contracts (Decision 12), the production `Clock` and `Timers` (Decisions 2 and 3), serve's wiring and shutdown (Decisions 1 and 6), the sweep (Decision 7), `Grace` against the record timeouts and the take's bound (Decisions 1 and 4), the guards bound to a holding (Decision 4), `Done` and `Err` (Decisions 6 and 18), board events' versions (Decisions 10 and 19), one daily cap (Decision 8), positive evidence (Decision 9), and `ErrNoSession`'s wording at a write's commit (Task 11).

## The gate after Task 12: the nudge, measured live

The author decided on 2026-10-20 to measure the nudge on a real model as soon as the runtime exists, rather than in Plan 7d-2: a quiet round needs every agent's own turn end (Decision 9), and Plan 3b's runs ended only 34 of 117 turns by `end_turn`.

- **What runs.** Plan 3b's test run again, with the nudge on: from `bench/itbench`, `agentswarm check --set ~/itbench/test-set.json --agents agents/agents.json --out ~/itbench/check-test-nudge --concurrency 3 --nudge`, every other parameter at the check's default (model `claude-haiku-5-5`, 3 trials). The model vendor's key comes from the keychain into that command's environment alone, and is never printed. Plan 3b's run of the same 39 runs cost $0.52.
- **Who runs it.** The controller, not an implementer: it spends money and uses the key.
- **What it measures.** How each turn ended, counted from the records as Plan 3b counted them: by `end_turn`, by a second answer without a tool after the nudge, or otherwise; and the check's hits and false convergences beside Plan 3b's.
- **The bar.** At least [90%] of turns end by `end_turn`. With the spec's [4] agents a round, that leaves about two rounds in three in which every agent ended its own turn. Below the bar, execution stops before Task 13, and the numbers go to the author: Decision 9 would need another design.
- **The record.** The result goes into "Changes made during execution", beside Plan 3b's 34 of 117.

## File Structure

| File | Task | Responsibility |
|---|---|---|
| `internal/testsupport/pgtest/pgtest.go` | 1 | Labels its container |
| `test/testclean/main.go` | 1 | `make test-clean`: removes this project's containers over an hour old, after a backup |
| `Makefile`, `.gitignore` | 1, 15, 18 | `test-clean`; `test-e2e`'s timeout; `test-compat`; `.test-clean/` ignored |
| `internal/actions/upstream/upstream.go` | 1, 9, 11 | The CA bundle; a server error's code; the holding before each request |
| `cmd/agentswarm/registry.go` | 1 | `registry hash-tools --ca` |
| `test/e2e/harness/*.go` | 2, 3, 15 | The harness (Contract C9) |
| `test/e2e/main_test.go`, `harness_test.go` | 2 | The parent mode, and the harness's own tests |
| `internal/db/db.go`, `schema.go`, `allowlist.go` | 3, 4 | `min_reader`, levels, compatibility, the allowlist; the pools' timeouts and the server's version |
| `cmd/agentswarm/migrate.go` | 3 | `migrate` |
| `internal/holding/holding.go` | 4 | The epoch in a context; `Holding`; `ErrLeaseLost` |
| `internal/work/postgres/lease.go`, `events.go` | 4, 5, 6, 7 | The guard by the context's epoch; the take's wait; clock samples; board events' versions; the previous holder |
| `internal/audit/postgres/calls.go`, `models.go`, `chain.go` | 4, 7, 9, 10, 11 | Guarded audit writes in the database's time; `SpentToday`; unsettled calls; `failure` and chain form 2; failed tool calls; calls in flight per round; the conflict's sentinel |
| `internal/work/sessions/sessions.go`, `internal/work/app/sessions.go` | 4 | Sessions carry their epoch |
| `internal/clock/db.go`, `timers.go` | 5 | The database's clock, the monotonic clock, timers |
| `internal/work/app/holding.go`, `leader.go` | 5, 6 | The holding's snapshot; draining; `Done` after the release; the schema's checks; the leader's log |
| `internal/work/app/ports.go` | 4–10 | The coordinator's ports and their contracts |
| `internal/work/board/board.go` | 6 | Board events' versions |
| `internal/work/item/*.go` | 8, 10 | The opening's digest; a round's evidence; the `unavailable` class; quiet and taking part |
| `internal/work/app/coordinator.go` | 4, 6, 7, 8, 10 | Epochs in contexts; skipped items; the sweep; openings by key; a turn's commit; failed tool calls and turn ends at a round's end |
| `internal/work/app/views.go` | 14 | An item as the admin API shows it |
| `internal/llm/app/*.go`, `internal/llm/endpoint/endpoint.go`, `internal/llm/memory/memory.go` | 4, 7, 11 | Epochs; `SpentToday`; the sweep; markers; the holding before each vendor attempt; `ErrConflict`; the skills' check |
| `internal/db/migrations/0009_tool_call_failure.sql` | 9 | `failure`, chain form 2 |
| `internal/actions/app/*.go`, `internal/actions/endpoint/endpoint.go` | 4, 9, 11 | Epochs; failures by side; listing failures; the endpoint's codes; the holding before each request; the drain; hides reloaded |
| `internal/outcome/outcome.go` | 11 | Contract C7 |
| `internal/runner/runner.go`, `internal/runner/mcpclient/mcpclient.go` | 12 | The runtime's loop, nudge and exit codes; its MCP toolbox |
| `internal/measurement/check/toolbox.go`, `cmd/agentswarm/check.go` | 12 | The mechanism check on the changed toolbox, without the nudge |
| `cmd/agentswarm/turn.go` | 12 | `turn` |
| `test/e2e/conformance_test.go` | 12 | ADR-006's criteria 1, 2, 3, 5, 6 and 7 |
| `internal/runner/process/process.go` | 13 | The process runner |
| `internal/deploy/deploy.go` | 14 | The deployment file |
| `internal/registry/current/current.go` | 14 | The current revision, its checks and its refresh |
| `internal/admin/admin.go` | 14 | The admin API |
| `cmd/agentswarm/serve.go`, `inspect.go` | 15 | `serve`, `inspect` |
| `test/e2e/serve_test.go`, `vendor_faults_test.go`, `holding_faults_test.go` | 15, 16, 17 | The scenarios |
| `test/compat/main.go` | 18 | `make test-compat` |
| `plan/...`, `README.md` | 18 | The spec's amendments, ADR-004 and ADR-006, the roadmap, Plan 7a's amendments |

---

## Tasks

Each task lists its tests first. A test's line says what it asserts and why it fails before the implementation. "A build failure" is the expected first failure only where the test calls a name that does not exist yet; every other test must fail on its assertion. Commands are run from the repository's root. Commit by path, never with `git add -A`.

### Task 1: Labelled test containers, `make test-clean`, and MCP roots

**Decisions:** 15, 16, R16. **Contracts:** none.

**Files:**
- Modify: `internal/testsupport/pgtest/pgtest.go` (labels; `ContainerID() string`, for its test)
- Create: `internal/testsupport/pgtest/pgtest_integration_test.go`
- Create: `test/testclean/main.go`, `test/testclean/main_test.go`
- Modify: `Makefile` (`test-clean`), `.gitignore` (`.test-clean/`)
- Modify: `internal/actions/upstream/upstream.go`, `upstream_test.go` (`Options.RootCAs`)
- Modify: `cmd/agentswarm/registry.go`, `registry_test.go` (`registry hash-tools --ca FILE`)

**Interfaces:**
- Consumes: nothing new.
- Produces: `upstream.Options.RootCAs *x509.CertPool` (nil: the system roots alone; otherwise the system roots and these); `go run ./test/testclean [-age 1h] [-backups .test-clean] [-dry-run]`, as `make test-clean`.

**Notes for the implementer:** `test/testclean` shells out to the `docker` CLI: `docker ps --filter label=agentswarm.project=agent-swarm --format '{{.ID}}|{{.CreatedAt}}|{{.Label "agentswarm.data"}}'`; for each container older than `-age`, `docker inspect` into `<backups>/<id>.inspect.json`, `docker cp <id>:<data path> -` into `<backups>/<id>-data.tar` when the data label is set, then `docker rm -f -v <id>`. A container whose backup fails is not removed, and the program exits 1. It takes its `docker` runner as a function, for its test.

**Tests (write first):**
- `TestMainLabelsItsContainer` (`internal/testsupport/pgtest`, integration): `docker inspect` of `ContainerID()` shows `agentswarm.project=agent-swarm` and `agentswarm.data=/var/lib/postgresql/data`. Fails first: `ContainerID` does not exist, then the labels are missing.
- `TestTestcleanRemovesOnlyItsOldContainersAfterABackup` (`test/testclean`, unit, with a fake runner): of two labelled containers, created 2 h and 10 min ago, only the first is backed up (its inspect output and its data tar, in that order) and then removed with `docker rm -f -v`; the listing filtered on the project's label. Fails first: no program.
- `TestTestcleanRemovesNothingWhoseBackupFailed`: the data copy fails; the container is not removed, and the program reports it and exits 1.
- `TestTestcleanDryRunRemovesNothing`: `-dry-run` lists the old container and runs no `inspect`, `cp` or `rm`.
- `TestTheUpstreamTrustsTheDeploymentsCABesidesTheSystemRoots` (`internal/actions/upstream`, unit): against an MCP server on `httptest.NewTLSServer`, `Offered` fails with an unknown-authority error when `RootCAs` is nil, and lists the server's tools with a pool that holds its certificate. Fails first: `Options` has no `RootCAs`.
- `TestHashToolsTakesACABundle` (`cmd/agentswarm`, unit): `registry hash-tools --endpoint URL` against a TLS test server exits non-zero naming the certificate's authority; with `--ca FILE` it prints one hash per tool. Fails first: the flag is unknown.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./test/testclean/ ./internal/actions/upstream/ ./cmd/agentswarm/` and `go test -race -count=1 -tags integration -run TestMainLabelsItsContainer ./internal/testsupport/pgtest/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Verify.** Run the commands of Step 1, then `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`, and `make test-clean`. Expected: every package `ok`; `0 issues.`; `make test-clean` lists what it removed, if anything.
- [ ] **Step 4: Commit**, in two commits by path: `test: label this project's test containers, and add make test-clean`; `feat(registry): trust a CA bundle for MCP servers`.

### Task 2: The harness's foundation

**Decisions:** 16, 17 (its setup), R3, R17 (the database it is given), R20. **Contracts:** C1 (the harness writes it), C9 (Task 2's part).

**Files:**
- Create: `test/e2e/harness/env.go`, `containers.go`, `toxiproxy.go`, `ca.go`, `vendor.go`, `mcpserver.go`, `config.go`, `deployment.go`, `parent.go`
- Modify: `test/e2e/main_test.go` (its `TestMain` runs `harness.ParentMain` when `AGENTSWARM_E2E_PARENT` is set)
- Create: `test/e2e/harness_test.go`

**Interfaces:**
- Consumes: the binary's `registry hash-tools --ca` (Task 1) and `registry validate --repo`.
- Produces: Contract C9's part made by Task 2.

**Notes for the implementer:**
- The scripted MCP server speaks MCP's streamable HTTP with the standard library only: `initialize`, `notifications/initialized`, `ping`, `tools/list` and `tools/call`, answered with `application/json`; a GET answers 405, a DELETE 200. It does not use the go-sdk, so that a protocol fault in the platform's SDK cannot hide behind the same SDK on the other side, and so that it can fail in ways an SDK server will not. It serves HTTPS with a certificate from the test CA, or plain HTTP when `Plain` is set.
- The scripted vendor speaks the Messages API's `POST /v1/messages` (no streaming), over HTTP on loopback. It tells instances apart by `x-api-key`, and MCP servers tell them apart by their credential header (R3).
- Toxiproxy is driven through its HTTP API (`POST /proxies`, and `enabled: false` to cut). The harness's own check of a proxy sends Postgres's SSLRequest (8 bytes: length 8, code 80877103) and expects one byte, `S` or `N`, back.
- The parent mode re-executes the test binary with `AGENTSWARM_E2E_PARENT=1`. It starts the command it is given with a pipe on the child's standard input that it holds open, sends the child's output to files, and exits when its own standard input, a pipe the test process holds, closes (R20). Killing it with SIGKILL, or ending the test process, closes the child's pipe.
- `Deployment` writes C1 with `secret://` references, its secrets directory holding the instance's own vendor key and server credential (R3), and `runner.images` mapping `thinloop` to an image of the `sha256:` form. `ConfigSpec`'s agents name the runtime `thinloop`.

**Tests (write first):** each is e2e, under `test/e2e/harness_test.go`.
- `TestTheHarnessContainersCarryTheProjectLabel`: the harness's Postgres and toxiproxy containers carry `agentswarm.project=agent-swarm`, and Postgres `agentswarm.data`. Fails first: no harness.
- `TestTheToxiproxyCutsAndRestoresAnInstancesDatabase`: the SSLRequest through instance `a`'s proxy is answered; after `Cut("a")` it is not, while instance `b`'s still is; after `Restore("a")` it is again.
- `TestTheScriptedVendorAnswersByScriptAndKeepsEveryRequest`: a request with instance `a`'s key gets the scripted `tool_use` answer; `Requests("a")` holds it with its headers and body; `Requests("b")` is empty; `All()` holds it.
- `TestTheScriptedVendorFailsHangsAndDelaysOnScript`: a scripted 529 comes back with its body; a 1 s delay takes at least 1 s; a hang is ended by a client timeout of 200 ms; a dropped connection gives the client an EOF.
- `TestTheScriptedMCPServerSpeaksMCPOverTLS`: a standard-library client that trusts `CA()` initializes, lists, and calls a tool, and gets the scripted text; then an `isError` result, a JSON-RPC error -32602, and an HTTP 500, each as scripted; after `Down()` a connection is refused, and after `Up()` the same address serves again; each request is held under the instance its credential names.
- `TestAPlainScriptedMCPServerServesHTTP`: with `Plain`, the same exchange over HTTP.
- `TestKillingTheParentClosesItsChildsStandardInput`: a parent running `sh -c 'cat >/dev/null; echo eof > MARKER'` is killed with SIGKILL, and `MARKER` holds `eof` within 2 s.
- `TestClosingTheParentsOwnInputEndsItAndItsChild`: the same parent, its own standard input closed by the test, exits, and `MARKER` holds `eof` within 2 s.
- `TestTheHarnessConfigurationValidates`: a configuration with one scripted server, its tools pinned through `agentswarm registry hash-tools --endpoint URL --ca CA()`, and two agents, passes `agentswarm registry validate --repo DIR`.
- `TestTheHarnessDeploymentNamesEverySecretItUses`: each instance's deployment names the keys C1 requires, every reference has the form `secret://…`, the image has the form `sha256:` and 64 hexadecimal digits, and its secrets directory holds the instance's own vendor key and server credential.
- `TestTheHarnessUsesTheDatabaseItIsGiven`: with `AGENTSWARM_E2E_DATABASE` set to another labelled Postgres, the harness starts no Postgres of its own, and `Psql` reaches the given one.

- [ ] **Step 1: Write the tests above.** Run `make test-e2e`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the harness, until every test passes.
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `test(e2e): the fault-injection harness's foundation`.

### Task 3: Schema levels, `min_reader`, and `agentswarm migrate`

**Decisions:** 1 (`migrate`), 18, R13, R17, R22. **Contracts:** C8, C9 (`Migrate`, `AuditVerify`).

**Files:**
- Modify: `internal/db/db.go` (`MigrateFS`, `MigrateOptions`, `MigrateResult`; `schema_migrations` gains `min_reader`)
- Create: `internal/db/schema.go` (`Level`, `Schema`, `State`, `ReadSchema`, `Checker`, `ErrIncompatible`)
- Create: `internal/db/allowlist.go`, `internal/db/allowlist_test.go`, `internal/db/testdata/allowlist/allowed/*.sql`, `internal/db/testdata/allowlist/refused/*.sql`
- Modify: `internal/db/db_integration_test.go`
- Create: `cmd/agentswarm/migrate.go`, `cmd/agentswarm/migrate_integration_test.go`; modify `cmd/agentswarm/run.go` to add the command
- Modify: `test/e2e/harness/env.go` (`Migrate`, honouring `AGENTSWARM_MIGRATE_BIN`; `AuditVerify`); create `test/e2e/migrate_test.go`

**Interfaces:**
- Consumes: the `leases` table of migration 0008 (`tenant_id`, `name`, `holder`, `epoch`, `expires_at`); the binary's `audit anchor` and `audit verify`.
- Produces:

```go
// MigrateOptions are a migration run's.
type MigrateOptions struct {
	// LockTimeout bounds each wait for a lock; zero means [5 s].
	LockTimeout time.Duration
	// Force applies a migration older builds cannot read while the
	// platform's lease is held.
	Force bool
}

// MigrateResult says what a migration run did.
type MigrateResult struct {
	Applied []string // the migrations applied, in order
	Schema  Schema   // the schema afterwards
}

func MigrateFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, opts MigrateOptions) (MigrateResult, error)
func Migrate(ctx context.Context, pool *pgxpool.Pool) error // the embedded migrations, default options

func Level() int // this build's level: its last migration's number

// Schema is the database's schema as its migrations left it.
type Schema struct {
	Level     int    // its last applied migration's number
	Last      string // that migration's file name
	MinReader int    // the largest min_reader of its applied migrations
}

type State string

const (
	Compatible   State = "compatible"
	Behind       State = "behind"
	Incompatible State = "incompatible"
)

func (s Schema) State(build int) State
func ReadSchema(ctx context.Context, pool *pgxpool.Pool) (Schema, error)

var ErrIncompatible = errors.New("db: this build does not run on the schema")

// Checker checks the schema against this build, in one query.
type Checker struct{ /* the lease's pool */ }

func NewChecker(pool *pgxpool.Pool) *Checker
func (c *Checker) Check(ctx context.Context) error          // nil, or an error wrapping ErrIncompatible that names the state and the levels
func (c *Checker) State(ctx context.Context) (State, error) // for /readyz and /v1/status
```

**Notes for the implementer:**
- `MigrateFS` reads and checks every file's declaration (C8) before it applies any.
- The guard reads `leases` only when the table exists: a fresh database has no holder.
- The allowlist test splits a migration into statements (semicolons outside quotes, dollar-quoted bodies and comments), and takes the audited tables to be every table a `CREATE TRIGGER ... ON <table>` names in any migration.

**Tests (write first):**
- `TestMigrateRecordsEachMigrationsReader` (`internal/db`, integration): on a fresh database every `schema_migrations` row has `min_reader` equal to its level, and `ReadSchema` gives `Level()` for both the level and the reader. Fails first: no column.
- `TestAMigrationMayDeclareAnOlderReader` (integration, `fstest.MapFS`): `0001` creates a table; `0002` declares `-- agentswarm:min_reader 1` and adds a nullable column to it. The row of `0002` holds 1, and the schema has level 2 and reader 1. Fails first: the declaration is not read.
- `TestADeclarationAboveItsOwnLevelIsRefused` (integration): `0002` declaring 3 fails `MigrateFS` with an error naming `0002`, and nothing is applied, `0001` included.
- `TestAnOldMigrationsTableGainsItsReaders` (integration): a `schema_migrations` table as Plan 7a's `Migrate` made it, with three rows, gains `min_reader`, each row at its own level.
- `TestASchemasStateAgainstABuild` (unit, a table): level 8 and reader 8 against build 8 is compatible; level 7 against 8 is behind; level 9, reader 8 against 8 is compatible; level 9, reader 9 against 8 is incompatible.
- `TestMigrateRefusesWhatOlderBuildsCannotReadWhileTheLeaseIsHeld` (integration): the embedded migrations, then a further `0099` at its own level, with a `leases` row of holder `instance-a` that expires in a minute. `MigrateFS` fails naming `instance-a` and `--force`, and `0099` is not applied; with `Force` it is. Fails first: no guard.
- `TestMigrateAppliesWhatOlderBuildsCanReadWhileTheLeaseIsHeld` (integration): a `0099` that declares the level before it and adds a nullable column to a table no trigger chains is applied under a live lease.
- `TestMigrateIgnoresAnExpiredLease` (integration).
- `TestMigrateGivesUpOnALockItCannotTake` (integration): while another connection holds `ACCESS EXCLUSIVE` on a table in an open transaction, a migration that alters it fails within 2 s with SQLSTATE `55P03` under a `LockTimeout` of 200 ms, and is not recorded.
- `TestEveryMigrationClassifiesAndThoseOnAnOlderReaderAreOnTheAllowlist` (`internal/db`, unit): every statement of every embedded migration classifies, and every migration that declares an older reader is on the allowlist.
- `TestTheAllowlistTakesWhatOlderBuildsCanRead` (unit): each file of `testdata/allowlist/allowed` passes: a nullable column on a table no trigger chains, a column with a constant default, a new table, a non-unique index.
- `TestTheAllowlistRefusesWhatOlderBuildsCannotRead` (unit): each file of `testdata/allowlist/refused` fails, naming its statement: a column on `tool_call_outcomes`; a unique index; `ALTER COLUMN ... SET DEFAULT`; `ADD CONSTRAINT`; `CREATE TRIGGER`; `CREATE OR REPLACE FUNCTION`; an `UPDATE`; a `DO` block; `ALTER COLUMN ... TYPE`; a `NOT NULL` column without a default; a column whose default is `now()`.
- `TestMigratePrintsWhatItAppliedAndTheLevel` (`cmd/agentswarm`, integration): with libpq's environment, `agentswarm migrate` prints `applied <file>` per migration and then `schema at level <N> (<file>)`; a second run prints only the last line. Fails first: no command.
- `TestMigrateTakesTheURLFromASecret` (integration): `--secrets DIR --database secret://…` works as the environment does.
- `TestMigrateSetsUpTheHarnessDatabase` (e2e): the harness's `Migrate` succeeds twice, the second time applying nothing.
- `TestAuditVerifyOfAFreshSchemaFindsOnlyTheUnlockedStore` (e2e): after `Migrate`, `AuditVerify` returns exactly `store_unlocked` (R22).

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/db/`, `go test -race -count=1 -tags integration ./internal/db/ ./cmd/agentswarm/`, `make test-e2e`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** `MigrateFS`, the schema's state, the allowlist, the guard, the command, and the harness's two additions.
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(db): migrations declare their oldest reader; agentswarm migrate`.

### Task 4: The holding: epochs in contexts, guarded audit writes in the database's time, bounded statements, and one day

**Decisions:** 1 (audit times, pools, Postgres 17), 4 (the epoch in contexts, the guards, the bounds), 8, R9, R18 (`ErrLeaseLost`). **Contracts:** none.

**Files:**
- Create: `internal/holding/holding.go`, `internal/holding/holding_test.go`
- Modify: `internal/work/postgres/lease.go`, `lease_integration_test.go` (`Check` by the context's epoch; the take's `lock_timeout`)
- Modify: `internal/audit/postgres/calls.go`, `models.go`, and their integration tests (`Guarded`; `clock_timestamp()`; `SpentToday`)
- Modify: `internal/db/db.go`, `db_integration_test.go` (`OpenWith`, `CheckServer`)
- Modify: `internal/work/sessions/sessions.go`, `internal/work/app/sessions.go`, `internal/actions/app/ports.go`, `internal/llm/app/ports.go` (sessions carry `Epoch`; `Calls.SpentToday`)
- Modify: `internal/actions/app/*.go`, `internal/llm/app/gateway.go`, `internal/llm/memory/memory.go` (the gateways' contexts carry the session's epoch)
- Modify: `internal/work/app/coordinator.go`, `ports.go`, `internal/work/memory/*.go`, and the coordinator's fakes and tests (`Spend.SpentToday`; contexts carry `term.Epoch`; `ErrLeaseLost` becomes an alias)

**Interfaces:**
- Consumes: Plan 7a's `postgres.Lease`, `Records.Guarded`, `EventStore.Guarded`.
- Produces:

```go
// package holding
var (
	ErrNoEpoch   = errors.New("holding: the context carries no epoch")
	ErrLeaseLost = errors.New("holding: the lease is lost") // work/app: var ErrLeaseLost = holding.ErrLeaseLost
)

func WithEpoch(ctx context.Context, epoch int64) context.Context
// EpochFrom returns the context's epoch, or ErrNoEpoch when it has none,
// or one below 1.
func EpochFrom(ctx context.Context) (int64, error)

// Holding tells whether a holding of the lease still holds.
type Holding interface {
	Valid(epoch int64) bool
}

// package postgres (internal/work/postgres)
func NewLease(pool *pgxpool.Pool, tenant, name, holder string, ttl, takeWait time.Duration) *Lease
// Check checks the epoch EpochFrom(ctx) gives, and fails wrapping
// holding.ErrLeaseLost when that holding is over; a context without an
// epoch fails wrapping holding.ErrNoEpoch, and not ErrLeaseLost.
// Take sets lock_timeout to takeWait; a take that waits past it returns
// (Term{}, false, nil) (R9).

// package postgres (internal/audit/postgres)
type Guard interface {
	Check(ctx context.Context, tx pgx.Tx) error
}

func (s *Calls) Guarded(guard Guard) *Calls
func (s *ModelCalls) Guarded(guard Guard) *ModelCalls
// SpentToday counts the tenant's calls admitted since the database's UTC midnight.
func (s *ModelCalls) SpentToday(ctx context.Context) (int64, error)

// package db
type PoolOptions struct {
	TransactionTimeout time.Duration // transaction_timeout
	IdleTimeout        time.Duration // idle_in_transaction_session_timeout
}

func OpenWith(ctx context.Context, url string, o PoolOptions) (*pgxpool.Pool, error)
// CheckServer connects once, setting no timeout, and refuses a server older
// than Postgres 17, which would refuse a connection that sets one.
func CheckServer(ctx context.Context, url string) error
```

  `app.Spend` and `llmapp.Calls` replace `SpentSince` with `SpentToday(ctx) (int64, error)`. `actionsapp.Session`, `llmapp.Session` and the sessions registry gain `Epoch int64`, the epoch of the term that issued the session.

**Tests (write first):**
- `TestAContextCarriesItsEpoch`, `TestAContextWithoutAnEpochHasNone`, `TestAnEpochBelowOneIsNone` (`internal/holding`, unit). Fail first: no package.
- `TestAGuardedWriteUnderAnEarlierEpochIsRefused` (`internal/work/postgres`, integration): after a release and a second take, a write whose context carries the first epoch fails wrapping `holding.ErrLeaseLost`, which `app.ErrLeaseLost` also matches, and nothing is written. Fails first: `Check` reads the instance's epoch, not the context's.
- `TestAGuardedWriteWithoutAnEpochIsABugNotALostLease` (integration): the error wraps `holding.ErrNoEpoch` and not `holding.ErrLeaseLost`; nothing is written.
- `TestATakeBehindAWriterGivesUpAfterItsWait` (integration): while a writer's transaction holds the lease's row in key share, and the holding has expired, a take with a wait of 300 ms returns not taken, with no error, within 1 s. Fails first: the take waits for the writer.
- `TestATakeoverComesWithinItsBoundBehindWritersQueuedOnTheChain` (integration; review 2's chain-lock probe, as plan review 1 measured it). The holding's TTL is 1 s. On a pool from `OpenWith` with both timeouts at 1 s, writer 1 passes the guard, inserts a board event, which takes its chain's lock, and idles in its transaction; writers 2 and 3 pass the guard and block inserting into the same chain. Another instance, taking every 200 ms with a wait of 300 ms, takes the lease in under 2 s, measured from writer 1's start (plan review 1 measured about 1.1 s), and each writer's commit then fails. With the idle timeout alone, writers 2 and 3 are not idle, and the take comes after about 2.9 s, so the test fails without `transaction_timeout`.
- Plan 7a's tests of the guarded stores and the lease give their contexts the holding's epoch, and its callers of `NewLease` the take's wait. `TestACallCountsInTheDayItWasAdmittedAgainst` counts through `SpentToday`.
- `TestToolCallsAndTheirOutcomesAreWrittenOnlyUnderTheHolding` and `TestModelCallsTheirContentsAndOutcomesAreWrittenOnlyUnderTheHolding` (`internal/audit/postgres`, integration): guarded by the lease, a write under a stale epoch fails wrapping `holding.ErrLeaseLost` and leaves no row; one under the current epoch lands. Fail first: no `Guarded`.
- `TestAuditRecordsAreWrittenInTheDatabasesTime` (integration): a tool call, its outcome, a hide, a model call and its outcome, each given `At` = 2000-01-01, are stored with times between `clock_timestamp()` read just before and just after.
- `TestSpentTodayCountsFromTheDatabasesUTCMidnight` (integration): of two calls inserted one second either side of the database's UTC midnight, only the later one counts.
- `TestServesPoolsBoundTheirTransactionsAndIdleTransactions` (`internal/db`, integration): a pool from `OpenWith` with 10 s and 10 s shows both settings at `10s`.
- `TestAServerOlderThan17IsRefused` (integration): against a labelled `postgres:16-alpine` container, `CheckServer` fails naming the version; against pgtest's Postgres 17 it passes.
- `TestASessionCarriesTheEpochItWasIssuedUnder` (`internal/work/sessions`, unit).
- `TestEveryWriteOfTheCoordinatorCarriesItsTermsEpoch` (`internal/work/app`, unit): fake records and board stores note `EpochFrom(ctx)` on each append. After a takeover at epoch 3, an opening, a start, a round's opening and its end by its timer, and a retry all carry 3; after a fence and a takeover at epoch 4, 4.
- `TestAWriteWithoutAnEpochIsLoggedAndDoesNotFence` (unit): a store that fails with `holding.ErrNoEpoch` leaves the coordinator held; the step is logged as "write without an epoch" and retried.
- `TestTheMCPGatewaysRecordsCarryTheSessionsEpoch` (`internal/actions/app`, unit) and `TestTheLLMGatewaysRecordsCarryTheSessionsEpoch` (`internal/llm/app`, unit): a fake audit store notes `EpochFrom(ctx)` on every begin and end, the writes made after the call's own context ended included; each is the session's epoch.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/holding/ ./internal/work/... ./internal/actions/... ./internal/llm/...` and `go test -race -count=1 -tags integration ./internal/work/postgres/ ./internal/audit/postgres/ ./internal/db/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(work): bind every guarded write to its holding's epoch, in the database's time`.

### Task 5: Clocks, timers and the leader

**Decisions:** 2, 3, 4 (the holding's snapshot and `Valid`), 6 (draining, `Done`), 18 (the checks at a take and a renewal), R4. **Contracts:** C3 (the leader's messages).

**Files:**
- Create: `internal/clock/db.go`, `internal/clock/timers.go`, `internal/clock/db_test.go`, `internal/clock/timers_test.go`
- Create: `internal/work/app/holding.go`, `internal/work/app/holding_test.go`
- Modify: `internal/work/app/leader.go`, `ports.go`, `leader_test.go`, `harness_test.go`
- Modify: `internal/work/postgres/lease.go`, `lease_integration_test.go` (`Sample`)

**Interfaces:**
- Consumes: `holding.Holding` and `holding.ErrLeaseLost` (Task 4); `postgres.Lease` (Task 4).
- Produces:

```go
// package clock
// Sampler reads the database's time.
type Sampler interface {
	Sample(ctx context.Context) (time.Time, error)
}

// DB is the database's time as this process estimates it: app.Clock for
// the coordinator.
type DB struct{ /* ... */ }

// NewDB takes the process clock; nil means time.Now.
func NewDB(s Sampler, now func() time.Time) *DB
func (c *DB) Sample(ctx context.Context) error
func (c *DB) Now() time.Time
func (c *DB) Local(t time.Time) time.Time

// Monotonic is the leader's app.Clock: the process's clock, whose readings
// carry its monotonic time.
type Monotonic struct{}

func (Monotonic) Now() time.Time
func (Monotonic) Local(t time.Time) time.Time // t itself

// Timers is app.Timers on a clock.
type Timers struct{ /* ... */ }

func NewTimers(c interface{ Now() time.Time }) *Timers
func (t *Timers) At(at time.Time, fn func()) (cancel func())

// package app
// Holding is the leader's account of its holding, which anyone may read
// without a lock. It implements holding.Holding.
type Holding struct{ /* an atomic snapshot */ }

// NewHolding takes the lease's TTL and margin, and the wall clock; nil means
// time.Now().Round(0).
func NewHolding(ttl, margin time.Duration, wall func() time.Time) *Holding
func (h *Holding) Valid(epoch int64) bool

// ClockSampler samples the database's clock (clock.DB).
type ClockSampler interface {
	Sample(ctx context.Context) error
}

// Schema checks that this build runs on the database's schema. An error
// that wraps ErrSchema stops the leader for good; any other is a failed
// read, tried again.
type Schema interface {
	Check(ctx context.Context) error
}

var ErrSchema = errors.New("app: this build does not run on the schema")

// LeaderOptions gains Holding *Holding (nil: the leader makes its own),
// Sampler ClockSampler and Schema Schema (nil: none; serve gives both).
func (l *Leader) Drain()
func (l *Leader) Status() (epoch int64, held, draining bool)

// package postgres (internal/work/postgres)
// Sample reads clock_timestamp() on the lease's pool.
func (l *Lease) Sample(ctx context.Context) (time.Time, error)
```

  `Timers`' doc in `ports.go` says that functions run on goroutines of their own, concurrently, never before `At` returns, and that `cancel` never waits. The leader logs C3's `lease taken`, `lease lost`, `fenced`, `draining`, `lease released` and `schema incompatible`, on `LeaderOptions.Log`.

**Tests (write first):**
- `TestTheDatabaseClockKeepsTheSampleWithTheShortestRoundTrip` (`internal/clock`, unit, with a fake sampler and process clock): of a sample with a 50 ms round trip and one with 5 ms, the offset comes from the second; a minute later, a new sample replaces it whatever its round trip. Fails first: no package.
- `TestTheDatabaseClockNeverGoesBack` (unit): after a correction of −2 s, a thousand reads of `Now` never decrease, and `Now` moves again once real time passes the old reading.
- `TestLocalCarriesAMonotonicReading` (unit): `Local(c.Now().Add(5*time.Second))` is about 5 s from `time.Now()` and carries a monotonic reading.
- `TestATimerArmedBeforeACorrectionKeepsItsOffset` (unit): a timer armed for 1 s ahead fires about 1 s later although a sample in between moved the offset by 500 ms.
- `TestAtNeverRunsItsFunctionBeforeItReturns` (unit): over a thousand timers armed in the past, each function sees that `At` returned.
- `TestCancelNeverWaitsForARunningFunction` (unit): `cancel` returns within 10 ms while the function blocks.
- `TestFunctionsRunConcurrently` (unit): two functions due at once each wait for the other to start, and both finish.
- `TestValidHoldsOnlyForTheCurrentEpochWhileTheLastGoodSendIsFresh` (`internal/work/app`, unit): `Valid` is false for another epoch, false when not held, true just before `TTL − Margin` from the last good send, and false after. Fails first: no `Holding`.
- `TestAHoldingIsInvalidOnceWallTimePassesItsMarginThoughMonotonicTimeDidNot` (unit; Review Focus 2): with 1 s of monotonic time and 20 s of wall time since the last good send, `Valid` is false.
- `TestValidNeverWaitsForTheLeadersLock` (unit; review 3's deadlock probe): while the leader fences, holding its lock and waiting for the coordinator's, a turn's start under the coordinator's lock calls `Valid`, and everything finishes within 2 s.
- `TestADrainingLeaderKeepsRenewingAndNeverTakes` (unit): after `Drain`, the coordinator is fenced, renewals go on, no take is made even once the holding is lost, and `Stop` releases.
- `TestADrainEndsAtOnceWhenTheHoldingIsLost` (unit): a renewal that fails wrapping `ErrLeaseLost` while draining closes `Done`, and nothing is released.
- `TestATakeoverFailingBecauseOfTheDrainKeepsTheHolding` (unit): `Drain` during a takeover in progress leaves the lease held, unreleased, until `Stop`.
- `TestDoneClosesOnlyAfterTheRelease` (unit): with `Release` blocked, `Done` stays open after `Stop`, and closes once `Release` returns.
- `TestALeaderStoppedDuringATakeReleasesWhatItTook` (unit): `Stop` while a take is in flight; the take then succeeds, the lease is released, and `Done` closes after.
- `TestASchemaThisBuildCannotRunOnAtATakeReleasesAndStopsForGood` (unit): the schema check after a take fails wrapping `ErrSchema`; the lease is released, no takeover runs, `Done` closes, `Err` wraps `ErrSchema`, and no take follows.
- `TestASchemaThisBuildCannotRunOnAtARenewalStopsWithoutDraining` (unit): after a good renewal, the check fails wrapping `ErrSchema`; the coordinator is fenced, the lease released, `Done` closed, with no drain.
- `TestASchemaCheckThatCannotReadDoesNotStopTheLeader` (unit): a check that fails with another error at a take releases the lease and takes it again later.
- `TestTheLeaderSamplesTheDatabasesClockAroundEachTakeAndRenewal` (unit): a sample before each take attempt, the first included, one after a take and before the takeover, and one after each good renewal.
- `TestTheLeaderLogsWhatTheHarnessReads` (unit, a table, reading the JSON log): a take logs `lease taken` with its epoch and `wall`; a renewal that found the lease lost, the watch, and a write that found the lease gone each log `lease lost` with a reason, and `fenced`, once each, while a renewal that failed for another reason logs neither; a drain logs `fenced`, `draining` and then `lease released`; a schema this build cannot run on, at a renewal, logs `schema incompatible`, `fenced` and `lease released`, and no `draining`.
- `TestTheLeasesSampleIsTheDatabasesTime` (`internal/work/postgres`, integration): `Sample` lies between `clock_timestamp()` read before and after.
- Plan 7a's `TestARealClocksTimersEndEachRoundOnce` runs on `clock.NewTimers` in place of its own real timers.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/clock/ ./internal/work/app/` and `go test -race -count=1 -tags integration ./internal/work/postgres/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(work): the production clocks and timers; the leader's holding, drain and schema checks`.

### Task 6: Versioned board events, and items skipped one by one

**Decisions:** 10, 19. **Contracts:** C3 (`item skipped`).

**Files:**
- Modify: `internal/work/board/board.go`; create `internal/work/board/version_test.go`
- Modify: `internal/work/postgres/events.go`, `events_integration_test.go`, `internal/work/memory/memory.go`
- Modify: `internal/work/app/coordinator.go`, `ports.go`, `leader.go`, `coordinator_test.go`, `restart_test.go`, `leader_test.go`

**Interfaces:**
- Consumes: the guarded stores (Task 4); the leader (Task 5).
- Produces:

```go
// package board
var ErrVersion = errors.New("board: unknown event version")
// Body gains V int `json:"v"`: missing reads 1, below 1 is invalid, above 1
// decodes to its version alone and loads wrapped with ErrVersion.

// package app (internal/work/app)
var ErrSkipped = errors.New("app: the item is held by a later build")

// Skipped returns the items this holding skips (Decision 19).
func (c *Coordinator) Skipped() []item.ID
```

**Notes for the implementer:** the leader's stop on `item.ErrVersion` goes: a takeover no longer fails on a later version. The coordinator logs C3's `item skipped` once per item and holding.

**Tests (write first):**
- `TestAnEventBodyWithoutAVersionReadsAsVersion1`, `TestAnEventVersionBelow1IsInvalid`, `TestAnEventOfALaterVersionDecodesToItsVersionAlone`, `TestThisBuildWritesEventsAtVersion1` (`internal/work/board`, unit). Fail first: `Body` has no `V`.
- `TestAnEventOfALaterVersionFailsItsRunsLoadWithErrVersion` (`internal/work/postgres`, integration): a board event inserted by SQL with `"v": 2` makes `Load` fail wrapping `board.ErrVersion`.
- `TestATakeoverSkipsAnItemWithALaterRecordAndCarriesTheOthersOn` (`internal/work/app`, unit; replaces `TestATakeoverRefusesRecordsItCannotRead`): of two active items, the one with a record of version 2 is skipped and logged `item skipped` once, `Skipped` names it, and the other's round goes on. Fails first: the takeover refuses the lease.
- `TestATakeoverSkipsAnItemWhoseBoardHoldsALaterEvent` (unit).
- `TestAStepThatMeetsALaterVersionEndsItsRoundAndArmsNothing` (unit): mid-holding, a step meets a later version; the run's sessions are ended, its agents' turns stopped, its timers cancelled, `settle`'s retry among them, and no retry is armed.
- `TestASkippedItemIsSkippedAgainAtTheNextTakeover` (unit).
- `TestALeaderLeadsOverRecordsItCannotRead` (unit; replaces `TestALeaderStopsOverRecordsItCannotRead` and `TestALeaderWhoseLaterTakeoverFindsALaterVersionSaysSoOnDone`): the leader holds the lease, `Done` stays open, and the readable items go on.

- [ ] **Step 1: Write the tests above, and change the three Plan 7a tests as named.** Run `go test -race -count=1 ./internal/work/...` and `go test -race -count=1 -tags integration ./internal/work/postgres/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(work): board events carry a version; skip the items of a later version one by one`.

### Task 7: Model calls left without an outcome

**Decisions:** 7, R10, R11. **Contracts:** none.

**Files:**
- Modify: `internal/work/postgres/lease.go`, `lease_integration_test.go` (the previous holder's name)
- Modify: `internal/work/app/coordinator.go`, `ports.go`, `restart_test.go`, `fakes_test.go` (`Term.PreviousHolder`; the `Sweeper` port; the takeover sweeps first)
- Modify: `internal/llm/app/gateway.go`, `ports.go`, `gateway_test.go`, `internal/llm/memory/memory.go` (`Sweep`; `Calls.Unsettled`)
- Modify: `internal/audit/postgres/models.go`, `models_integration_test.go` (`Unsettled`)

**Interfaces:**
- Consumes: the guarded stores and the epoch in the takeover's context (Task 4); the leader (Task 5).
- Produces:

```go
// package app (internal/work/app)
type Term struct {
	Epoch    int64
	At       time.Time
	Previous time.Time
	// PreviousHolder is the holder of the holding before this one, as the
	// take read it; empty for the lease's first holding.
	PreviousHolder string
}

// Sweeper gives the model calls an instance left without an outcome one
// (Decision 7).
type Sweeper interface {
	Sweep(ctx context.Context, before time.Time, reason string) (int, error)
}

// Ports gains Sweeper Sweeper, which NewCoordinator requires.

// package app (internal/llm/app)
// Unsettled is a model call without an outcome, with what Plan 6b's
// estimate of a lost call reads.
type Unsettled struct {
	ID           string
	Model        string
	Estimate     int64 // the stored worst case, in cost-weighted tokens
	RequestBytes int64 // the stored request's size
	MaxTokens    int64 // read from the call's stored request
}

// Calls gains Unsettled(ctx context.Context, before time.Time) ([]Unsettled, error).
func (g *Gateway) Sweep(ctx context.Context, before time.Time, reason string) (int, error)
```

**Notes for the implementer:** the takeover sweeps first, before it carries any item on, so that a carried round's end sees its calls settled. Its reason is "the instance <PreviousHolder> stopped before the call's outcome was recorded", or, when `PreviousHolder` is this instance's own holder name, "this instance's earlier holding ended before the call's outcome was recorded".

**Tests (write first):**
- `TestATakeReadsThePreviousHoldersName` (`internal/work/postgres`, integration): after instance `a`'s holding expires, instance `b`'s take returns `PreviousHolder` `a`. Fails first: no field.
- `TestATakeoverSweepsTheCallsTheLastHoldingLeft` (`internal/work/app`, unit): the fake sweeper is called once per takeover, before any item is carried on, with `before` = `Term.At` and the reason naming `PreviousHolder`, or this instance's earlier holding.
- `TestASweepThatFindsTheLeaseGoneStopsTheTakeover` (unit): the takeover fails wrapping `ErrLeaseLost`, and no item is carried on.
- `TestASweptCallIsPricedFromItsStoredRequest` (`internal/llm/app`, unit): status `unknown`, `weighted` the stored estimate, usage estimated from the stored request's size and `max_tokens` as Plan 6b estimates a lost call's (`cost.Estimate`), `spend` the estimate times the price reference, and the given reason.
- `TestASweepRepeatedChangesNothing` (unit): a second sweep finds no call and writes nothing.
- `TestASweepThatCannotRecordOneCallSweepsTheOthers` (unit): the failure is logged; the others get outcomes.
- `TestUnsettledCallsAreThoseWithoutAnOutcomeThatBeganBefore` (`internal/audit/postgres`, integration).

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/... ./internal/llm/...` and `go test -race -count=1 -tags integration ./internal/work/postgres/ ./internal/audit/postgres/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(work): sweep the model calls an earlier holding left without an outcome`.

### Task 8: Openings by key, a turn's commit, and no current revision

**Decisions:** 12 (`Turn.Commit`), 14 (the coordinator's part of an opening by key), R12, R19. **Contracts:** none.

**Files:**
- Modify: `internal/work/item/item.go` (`Opened.Digest`)
- Modify: `internal/work/app/coordinator.go`, `ports.go`, `coordinator_test.go`, `restart_test.go`

**Interfaces:**
- Consumes: the skip and `ErrSkipped` (Task 6).
- Produces:

```go
// package app (internal/work/app)
// OpenInput gains ID item.ID (empty: a new id) and Digest string (the request's).
// Turn gains Commit string: the configuration's commit the item locked.

var (
	ErrKeyReused  = errors.New("app: the key opened another item")
	ErrNoRevision = errors.New("app: the registry has no configuration yet") // Open and the start return it
)

// package item
// Opened gains Digest string `json:"digest,omitempty"` (R12: body version 1).
```

**Tests (write first):**
- `TestAnOpeningByKeyReturnsTheSameItemForTheSameRequest` (`internal/work/app`, unit): two openings with the same `ID` and `Digest` give one item. Fails first: `OpenInput` has no `ID`.
- `TestAnOpeningByKeyWithAnotherRequestIsRefused` (unit): the same `ID` with another `Digest` fails with `ErrKeyReused`.
- `TestARetriedOpeningAfterATakeoverAdvancesAnItemWhoseOpeningLanded` (unit; Review Focus 5): an opening whose record landed under epoch 1, though its caller saw an error, is retried under epoch 2 with the same `ID` and `Digest`; the item is returned and started, and no second item exists.
- `TestAnOpeningOfASkippedItemIsRefused` (unit): `ErrSkipped`.
- `TestOpeningAndStartingWithNoCurrentRevisionFailWithErrNoRevision` (unit): an opening fails wrapping `ErrNoRevision`, and a start waits and retries, its step failing wrapping it.
- `TestATurnCarriesItsItemsLockedCommit` (unit).

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/...`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(work): open items by key; give turns their commit; name the missing revision`.

### Task 9: Tool calls that failed, recorded by their side

**Decisions:** 20, R1, R2. **Contracts:** none.

**Files:**
- Create: `internal/db/migrations/0009_tool_call_failure.sql`
- Modify: `internal/audit/postgres/calls.go`, `chain.go`, `calls_integration_test.go`, `chain_integration_test.go`, and `internal/audit/chain` where chain forms are defined
- Modify: `internal/actions/app/ports.go`, `call.go`, `write.go`, `board.go`, `platform.go`, `gateway.go`, and their tests; create `internal/actions/app/failure_test.go`
- Modify: `internal/actions/upstream/upstream.go`, `upstream_test.go`
- Modify: `internal/work/app/ports.go` (`ToolCalls`)

**Interfaces:**
- Consumes: `db.MigrateFS` (Task 3), for the test of rows of both chain forms.
- Produces:

```go
// package app (internal/actions/app)
// Failure is the side a tool call failed on (Decision 20).
type Failure string

const (
	FailureServer   Failure = "server"
	FailurePlatform Failure = "platform"
	FailureTool     Failure = "tool"
	FailureAgent    Failure = "agent"
)

// Outcome gains Failure Failure; ServerError gains Code int64, the JSON-RPC
// code, zero when the failure had none.

// ErrUnavailable answers a call the gateway cannot serve now: its revision
// unreadable, its record not begun, its holding refused, or, for a board
// call, the board unreachable, which is recorded.
var ErrUnavailable = errors.New("app: the gateway cannot serve this call now")

// package app (internal/work/app)
// ToolCalls is what the MCP gateway's records say of a round's tool calls.
type ToolCalls interface {
	// Failed counts the round's failed tool calls that Decision 20 counts.
	Failed(ctx context.Context, run string, round int) (int, error)
}

// package postgres (internal/audit/postgres)
func (s *Calls) Failed(ctx context.Context, run string, round int) (int, error)
```

**Notes for the implementer:** until Task 11, the MCP endpoint answers `ErrUnavailable` as it answers every error today; Task 11 gives it its own code.

**Tests (write first):**
- `TestEveryFailureSiteRecordsItsSide` (`internal/actions/app`, unit, a table with one row per site of Decision 20's table, for reads, writes, board calls, listings and the platform's own reads, but for the hides reload, whose row is Task 11's, which builds the reload): each records its `failure`; a read cut by its round's deadline, and one cut by its run's stop, record none; a board call whose outcome cannot be stored leaves its row without one. Fails first: `Outcome` has no `Failure`.
- `TestARevisionThatCannotBeReadAnswersUnavailableAndRecordsNothing` and `TestABeginThatFailsAnswersUnavailableAndRecordsNothing` (unit): `Call` fails wrapping `ErrUnavailable`, and the fake audit store holds nothing. Today a `Begin` that fails answers "the call could not be recorded, so it was not made".
- `TestABoardCallsPlatformFailureIsRecordedAndAnsweredUnavailable` (unit): with the board unreachable, a board call's row records `platform`, and `Call` fails wrapping `ErrUnavailable`.
- `TestServerErrorsKeepTheirJSONRPCCode` (`internal/actions/upstream`, unit).
- `TestAServerUnreachableAtTheListingIsRecordedAsAFailedCall` (`internal/actions/app`, unit): a session's listing with one granted server down leaves that server's tools out, and records, for the session's round, a `tool_calls` row of that server and tool `tools/list`, with an outcome `read_error` and failure `server`.
- `TestAToolCallOutcomesFailureIsChained` (`internal/audit/postgres`, integration): outcomes of each failure value verify; one whose `failure` is changed by SQL, with the trigger set aside, is named by verification. Fails first: no column.
- `TestOutcomesOfBothChainFormsVerify` (integration): migrations to 0008, an outcome, then 0009 and another outcome; both verify.
- `TestEveryChainFunctionPinsItsSearchPath` (integration): the `Tables` check finds none unpinned on a fresh schema, and names a chain function after `ALTER FUNCTION ... RESET search_path`.
- `TestFailedToolCallsCountWhatTheRoundCannotTrust` (integration, a table, each row its own round): a read's `server` failure, not recovered, counts 1; a `tool` failure and then the same agent's successful read of the same tool, 0; a `tool` failure and then another agent's success, 1; a `tool` failure and then the same agent's success of another tool, 1; an `agent` failure, 0; a cut call, 0; a tool call without an outcome, 1; a failure in an earlier round, 0; a listing's failure, 1; a write's `platform` failure, 1; a failed `end_turn` and then a successful one by the same agent, 0; a failed `read_board` and then a successful `end_turn`, 1; a write's `server` failure and then the same write by the same agent `recorded`, 0; a platform read's `server` failure, which has no round, 0, its write's own row counting it.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/actions/...` and `go test -race -count=1 -tags integration ./internal/audit/postgres/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** migration 0009 (chain form 2, `search_path` pinned on every function it replaces or creates), the sites, the listing's record, `Failed`, and the `Tables` check.
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(actions): record each failed tool call's side, chained by form 2; count the failures a round cannot trust`.

### Task 10: Quiet and taking part, from the platform's records

**Decisions:** 9, 13 (the `unavailable` class), R2, R12. **Contracts:** none.

**Files:**
- Modify: `internal/work/item/item.go`, `decide.go`, `state.go`, `decide_test.go`, `state_test.go`, and the property tests
- Modify: `internal/work/app/coordinator.go`, `ports.go`, `coordinator_test.go`, `restart_test.go`, `harness_test.go`, `fakes_test.go`
- Modify: `internal/audit/postgres/models.go`, `models_integration_test.go` (`InFlight` per round)

**Interfaces:**
- Consumes: `app.ToolCalls` and `Calls.Failed` (Task 9); the board's report, whose `Facts.Ended` holds the agents that ended their own turn in the round.
- Produces:

```go
// package item
const TurnUnavailable TurnClass = "unavailable"
// RoundEnded records the evidence its decision read: TurnEnds []string,
// FailedCalls int (model calls), FailedToolCalls int and Settled bool
// (R12: body version 1). Ending carries TurnEnds and FailedToolCalls beside
// what it holds.

// package app (internal/work/app)
// Spend.InFlight(ctx context.Context, run string, round int) (int, error)
// Ports gains ToolCalls ToolCalls, which NewCoordinator requires: a round's
// end with no record of its tool calls could not tell a quiet round.
```

**Notes for the implementer:**
- `reported` takes `TurnUnavailable`, and a round whose every turn ended `vendor` or `unavailable` waits for its time limit, as one of `vendor` turns does today. `unavailable` never counts toward an agent's removal.
- The agents of a round are those the round invited; quiet needs each of them in `Facts.Ended`.
- The coordinator's test harness posts an agent's `end_turn` on the board for each turn it ends `ended`.

**Tests (write first):**
- `TestARoundIsQuietOnlyWhenEveryAgentEndedItsOwnTurn` (`internal/work/item`, unit): two agents, one turn end: not quiet; two: quiet. Fails first: quiet reads no turn ends.
- `TestARoundWithNoAgentIsNeverQuiet` (unit).
- `TestARoundWithAFailedToolCallIsNeverQuiet` (unit).
- `TestAWrapUpRoundConcludesOnlyWithATurnEndAndNoFailure` (unit): a turn end and no failure concludes; a turn end with a failed tool call, a failed model call, or calls unsettled concludes nothing; no turn end concludes nothing; each uses up a wrap-up.
- `TestAnInterruptedTurnNeverTakesPart` (unit; inverts `TestOnlyATurnThatEndedFinishedOrWasInterruptedTookPart`).
- `TestAnUnavailableTurnIsNoCrash` (unit): two rounds of `unavailable` turns remove no agent.
- `TestRoundEndedRecordsTheEvidenceItDecidedOn` (unit): the record holds the turn ends, the counts and the settledness the decision read.
- `TestQuietAndTakingPartFollowTheTurnEndsWhateverTheClasses` (property): for turn ends and classes drawn apart, `unavailable` among the classes, a round is quiet exactly when Decision 9's conditions hold, and a wrap-up concludes exactly when they do.
- `TestARoundsEndReadsItsFailedToolCalls` (`internal/work/app`, unit): the fake `ToolCalls` is asked for the round's run and number, after the grace, and a count above zero leaves the round not quiet.
- `TestACallHungFromAnEarlierRoundDoesNotKeepALaterRoundUnsettled` (unit): a call of round 1 still in flight leaves round 2 settled.
- `TestARoundWhoseEveryTurnWasUnavailableWaitsForItsLimit` (unit).
- `TestInFlightCountsTheRoundsCallsOnly` (`internal/audit/postgres`, integration).
- The Plan 7a tests named under "Plan 7a's tests this plan changes" for Decision 9, each changed to give the agents' turn ends, or inverted, as that list says.

- [ ] **Step 1: Write the tests above and change Plan 7a's as listed.** Run `go test -race -count=1 ./internal/work/...` and `go test -race -count=1 -tags integration ./internal/audit/postgres/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(work): quiet and a wrap-up's taking part rest on the agents' turn ends and the gateways' records`.

### Task 11: The gateways' answers, and their requests bound to the holding

**Decisions:** 1 (the skills' check, which Task 14 uses), 4 (the holding before each external request; sessions closed while valid), 5, 6 (the MCP gateway's drain; the LLM gateway's `recordLater`), 13 (the markers and codes), R7, R8, R18 (`ErrConflict`). **Contracts:** C3 (`holding refused`), C7.

**Files:**
- Create: `internal/outcome/outcome.go`
- Modify: `internal/llm/endpoint/endpoint.go`, `endpoint_test.go`; `internal/llm/app/gateway.go`, `ports.go`, `gateway_test.go`
- Modify: `internal/audit/postgres/models.go`, `models_integration_test.go` (`ModelCalls.End` wraps `llmapp.ErrConflict`)
- Modify: `internal/actions/endpoint/endpoint.go`, `endpoint_test.go`, `contract_test.go`; `internal/actions/app/*.go` and their tests; `internal/actions/upstream/upstream.go`, `upstream_test.go`

**Interfaces:**
- Consumes: `holding.Holding`, `holding.EpochFrom` and `holding.ErrLeaseLost` (Task 4); `actionsapp.ErrUnavailable` (Task 9).
- Produces: Contract C7, and:

```go
// package app (internal/actions/app): Ports gains Holding holding.Holding;
// Options gains Log *slog.Logger.
// Drain stops taking calls, which it answers ErrUnavailable, and waits for
// the detached writes, up to 3 × RecordTimeout.
func (g *Gateway) Drain(ctx context.Context) error

// package upstream: Options gains Holding holding.Holding. Every request,
// each retry included, is preceded by Valid(EpochFrom(ctx)); a refusal is
// an error wrapping actionsapp.ErrUnavailable, and a context without an
// epoch is refused.

// package app (internal/llm/app): Ports gains Holding holding.Holding;
// Options gains Log *slog.Logger.

// ErrConflict marks an outcome refused because the call already has
// another, as when a sweep settled it (R18). ModelCalls.End wraps it
// beside postgres.ErrConflict, which it returns today.
var ErrConflict = errors.New("llmapp: the call already has another outcome")

// CheckSkill applies to a skill the checks the gateway applies to a
// request's system prompt under rev: the session tokens' rule and the
// revision's rules that apply always, refusing a text whose escape may hide
// a match. Task 14's registry adapter takes it.
func CheckSkill(rev *config.Revision, text string) error
```

**Notes for the implementer:**
- The LLM gateway sets the marker where a reply is written, so that a reply `decline` rewrites carries the marker of the reply it sends.
- A write whose session ended at its commit answers "your round ended or the run stopped; nothing was recorded" (`ErrNoSession`'s wording, a Plan 7a carry).
- Each gateway logs C3's `holding refused` with the session's epoch and `request` `vendor` or `mcp`.

**Tests (write first):**
- `TestEveryNonSuccessReplyCarriesItsMarker` (`internal/llm/endpoint`, unit, a table with one row per reply site of Decision 13's table). Fails first: no marker.
- `TestADeclinedReplyKeepsItsMarker` (unit).
- `TestNoVendorAttemptIsSentOnceTheHoldingEnds` (`internal/llm/app`, unit): a first attempt fails retriably, the holding then becomes invalid, and no second request reaches the fake vendor; the reply is `unavailable`, and `holding refused` is logged with `request` `vendor`.
- `TestRecordingLaterStopsAtALostLeaseOrAConflict` (`internal/llm/app`, unit): `recordLater` stops after one attempt that fails wrapping `holding.ErrLeaseLost`, and after one that fails wrapping `ErrConflict`.
- `TestTheStoresWrapTheSharedSentinels` (`internal/audit/postgres`, integration): `ModelCalls.End` of a call that already has another outcome fails wrapping `llmapp.ErrConflict`; a guarded write under a stale epoch fails wrapping `holding.ErrLeaseLost`.
- `TestASkillWhoseEscapeHidesAnAlwaysRuleMatchIsRefused` and `TestAPlainSkillPassesTheSystemPromptCheck` (`internal/llm/app`, unit).
- `TestTheMCPEndpointAnswersWithItsCodes` (`internal/actions/endpoint`, unit, a table): `ErrNoSession` and a board call's "the round has ended" answer -32001; `ErrUnavailable`, a holding's refusal and a board call's platform failure answer -32002; a dead token gets 401; arguments over the cap get 413 marked `refused`. Fails first: every error answers -32600.
- `TestNoUpstreamRequestIsSentOnceTheHoldingEnds` (`internal/actions/upstream`, unit, a table): a connect, an initialize, a listing (`Offered`), a call, the retry after a connection failure, and the retry after a 401, each with the holding invalid, send nothing to the test server, and fail wrapping `ErrUnavailable`.
- `TestAHoldingRefusalLeavesNoFailedCall` (`internal/actions/app`, unit), which also checks `holding refused` is logged with `request` `mcp`.
- `TestAHoldingRefusalInAListingAnswersUnavailable` (unit).
- `TestUpstreamSessionsAreClosedOnlyWhileTheHoldingIsValid` (`internal/actions/upstream`, unit): the test server sees the DELETE while the holding is valid, and none after.
- `TestHidesAreReloadedOnEachLookAndForASessionOfANewEpoch` (`internal/actions/app`, unit).
- `TestAHidesReloadThatFailsRecordsPlatform` (unit): a call whose hides reload fails records `failure` `platform` (Decision 20's row, which this task's reload makes).
- `TestADrainedGatewayTakesNoNewCallAndFinishesItsWrites` (unit): after `Drain`, a new call fails wrapping `ErrUnavailable`, and the write in flight lands within 3 × `RecordTimeout`.
- `TestAWriteWhoseSessionEndedAtItsCommitSaysSo` (unit): the answer's text is exactly "your round ended or the run stopped; nothing was recorded".
- The MCP endpoint's existing tests that expect -32600 expect -32001.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/outcome/ ./internal/llm/... ./internal/actions/...` and `go test -race -count=1 -tags integration ./internal/audit/postgres/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(gateways): mark every answer that is not a success; send nothing once the holding ends; drain the MCP gateway`.

### Task 12: The runtime: `agentswarm turn`

**Decisions:** 9 (what a turn end is), 13 (the runtime, its exit codes, conformance), R14, R21. **Contracts:** C6, C7, C9 (the scripted servers as stand-ins for the gateways).

**Files:**
- Modify: `internal/runner/runner.go`, `runner_test.go`
- Create: `internal/runner/mcpclient/mcpclient.go`, `mcpclient_test.go`
- Modify: `internal/measurement/check/toolbox.go`, `cmd/agentswarm/check.go`, and their tests (the changed toolbox; the nudge off unless `--nudge`)
- Create: `cmd/agentswarm/turn.go`, `turn_test.go`; modify `cmd/agentswarm/run.go` to add the command
- Create: `test/e2e/conformance_test.go`

**Interfaces:**
- Consumes: Contract C7 (Task 11); the MCP endpoint's codes and the LLM gateway's markers (Task 11); the harness's scripted servers (Task 2).
- Produces:

```go
// package runner
type Toolbox interface {
	Tools(ctx context.Context) ([]Tool, error)
	// Call returns the result's content, each text block on its own.
	Call(ctx context.Context, name string, input json.RawMessage) (blocks []string, isError bool, err error)
}

// Config gains Nudge bool (R21), and its System becomes []string: each
// skill, sent as its own system text block.

// The ways a gateway can fail a turn, which the toolbox and the client
// return wrapped.
var (
	ErrSessionGone = errors.New("runner: the session is gone")
	ErrUnavailable = errors.New("runner: the gateway is unavailable")
	ErrRefused     = errors.New("runner: the gateway refused the request")
	ErrVendor      = errors.New("runner: the model vendor failed")
	ErrCut         = errors.New("runner: the gateway cut the call")
)

// Nudge is the platform's message to a model that answered without a tool.
const Nudge = "You answered without calling a tool. If you are done, call end_turn; otherwise, continue."

// ExitCode gives a turn's exit code (Decision 13; Contract C7).
func ExitCode(res Result, err error) int

// agentswarm check gains --nudge (default off), which sets the turns'
// Config.Nudge, for the gate after Task 12.

// package mcpclient
// New returns a Toolbox that speaks MCP to url, with token as its bearer.
func New(url, token string, client *http.Client) *Toolbox
```

**Notes for the implementer:**
- The client reads `outcome.Header` from an `*anthropic.Error`'s response; an error with no HTTP answer is `ErrUnavailable`.
- `agentswarm turn` reads its spec (C6), at most 1 MiB, then keeps reading its standard input; end-of-file cancels the turn, which then exits 14.
- Its HTTP transport sets `Proxy: nil`, and its client's request timeout runs to the spec's deadline.
- The mechanism check keeps `Nudge` off unless it is given `--nudge`.

**Tests (write first):**
- `TestAnAnswerWithoutAToolIsNudgedOnce` (`internal/runner`, unit, with an `httptest` model and a fake toolbox): with `Nudge` on, the request after a first answer without a tool ends with a user message whose text is exactly `Nudge`; the model then calls `end_turn`, and the turn ends `ended`. Fails first: no nudge.
- `TestASecondAnswerWithoutAToolFinishesTheTurn` (unit): exit 11.
- `TestWithoutTheNudgeAnAnswerWithoutAToolEndsTheTurn` (unit): with `Nudge` off, as the mechanism check runs, the first such answer ends the turn as today.
- `TestTheTurnEndsOnlyWhenEndTurnSucceeds` (unit): an `end_turn` answered `ErrSessionGone` ends the turn interrupted, exit 14.
- `TestEndTurnIsTriedOnceMoreWhenUnavailable` (unit): `unavailable` and then success ends it `ended`; `unavailable` twice exits 15.
- `TestEachOutcomeHasItsExitCode` (unit, a table with one row per line of Decision 13's exit table, each cause on its own row).
- `TestEachTextBlockOfAToolResultReachesTheModelOnItsOwn` (unit).
- `TestEachSkillIsItsOwnSystemBlock` (unit).
- `TestAnUnmarkedFailureReadsAsUnavailable` (unit): a 500 with no marker exits 15, not as a crash.
- `TestCheckTakesTheNudgeOnRequest` (`cmd/agentswarm`, unit): without `--nudge` the check's turns run with `Nudge` off, and with it on.
- `TestTheToolboxListsItsToolsOncePerTurn` (`internal/runner/mcpclient`, unit, against `internal/actions/endpoint` over a fake gateway).
- `TestTheToolboxMapsTheEndpointsAnswers` (unit, a table): -32001 and HTTP 401 to `ErrSessionGone`; -32002, another non-2xx, and a refused connection to `ErrUnavailable`; HTTP 413 to `ErrRefused`; `isError` to a result with `isError` set.
- `TestTheTokenTravelsOnlyInTheAuthorizationHeader` (unit).
- `TestTurnTakesItsSpecFromStandardInput` and `TestTurnExitsWhenItsStandardInputCloses` (`cmd/agentswarm`, unit): a spec over 1 MiB is refused, and an end-of-file mid-turn exits 14 within 1 s.
- `TestTheRuntimesTransportIgnoresProxies` and `TestTheClientsTimeoutRunsToTheDeadline` (`cmd/agentswarm`, unit): the transport's `Proxy` is nil; the request timeout is no shorter than the time to the spec's deadline.
- Conformance (`test/e2e/conformance_test.go`, e2e). Each runs `agentswarm turn` as a host process, with the scripted vendor standing in for the LLM gateway and a `Plain` scripted MCP server for the MCP gateway:
  - `TestConformanceRoundBasedTurns` (criterion 1): a model that keeps calling tools is stopped by the spec's deadline, and the process exits 14.
  - `TestConformanceToolsOnlyThroughTheGateway` (criterion 2): with `ANTHROPIC_BASE_URL` and `ANTHROPIC_API_KEY` pointing at a trap server, the trap receives nothing, and only the spec's two URLs see requests.
  - `TestConformanceNoBuiltInToolsReachOutside` (criterion 3): each request's tools are exactly the MCP server's listed tools.
  - `TestConformanceNoHostConfigurationInPrompts` (criterion 5): with a `CLAUDE.md` in the home directory, an `AGENTS.md` in the working directory, and marker values in the environment, no request holds any of them; the system prompt is the spec's skills, and the only platform text is `Nudge`.
  - `TestConformanceAFreshContextEachTurn` (criterion 6): the second turn's first request holds its own prompt and nothing of the first turn.
  - `TestConformanceToolsListedOncePerTurn` (criterion 7).
  - `TestConformanceEndTurnOnlyWhenTheModelAsks`: a turn whose model never names `end_turn` makes no `end_turn` call.
  - `TestNoTokenAppearsInTheRuntimesOutput`: neither token is in the process's standard output or error.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/runner/... ./internal/measurement/... ./cmd/agentswarm/` and `make test-e2e`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(runner): agentswarm turn, with the nudge, end_turn's retry, exit codes, and ADR-006's conformance tests`.

### Task 13: The process runner

**Decisions:** 4 (the holding before a start), 12, R5. **Contracts:** C3 (`turn started`, `holding refused`), C6, C7.

**Files:**
- Create: `internal/runner/process/process.go`, `process_test.go` (its `TestMain` re-executes the test binary as a fake turn when `AGENTSWARM_FAKE_TURN` is set)

**Interfaces:**
- Consumes: `app.Turns`, `app.Turn` with `Commit` (Task 8), `app.Exit`, `item.TurnUnavailable` (Task 10), `holding.Holding` and `holding.EpochFrom` (Task 4), Contract C7.
- Produces:

```go
// package process
type Options struct {
	Binary    string        // the executable run as `<Binary> turn`; "" means os.Executable()
	Output    string        // the directory of the turns' output files
	OutputCap int64         // [1 MiB]
	Slack     time.Duration // [2 s], added to a turn's deadline in its spec
	MCP, LLM  string        // the gateways' URLs, as a turn reaches them
	MaxTokens int64         // the deployment's runner.maxTokens [4,096]
}

type Ports struct {
	Holding holding.Holding
	// Skills reads an agent's skills at a commit, off the coordinator's lock.
	Skills func(ctx context.Context, commit, agent string) ([]string, error)
	// Ended reports whether the board holds the agent's own turn end in the round.
	Ended func(ctx context.Context, run board.RunID, round int, agent string) (bool, error)
	// Exited is the coordinator's Exited, bound once the coordinator exists.
	Exited func(app.Exit)
	Log    *slog.Logger
}

func New(p Ports, o Options) (*Runner, error)
func (r *Runner) Start(ctx context.Context, t *app.Turn) error
func (r *Runner) Stop(run board.RunID, agent string)
func (r *Runner) StopAll() // SIGKILL to every turn's process group; serve's exit at once calls it
```

**Notes for the implementer:** the runner logs C3's `turn started` with the turn's id and its process id, and `holding refused` with `request` `turn` when the holding refuses a start.

**Tests (write first):** each runs the fake turn, which reads its spec and does what its prompt says.
- `TestStartChecksTheHoldingFirst`: with the holding invalid, `Start` fails, nothing is launched, and `holding refused` is logged. Fails first: no package.
- `TestStartReturnsBeforeTheTurnRuns`: a turn that sleeps 5 s; `Start` returns within 200 ms, and `turn started` is logged with its process id.
- `TestALaunchThatFailsReturnsItsError`: a missing binary fails `Start`, and `Exited` is never called.
- `TestAnOutputFileThatCannotBeCreatedFailsTheLaunch` (Review Focus 4): with the output directory read-only, `Start` fails, and `Exited` is never called.
- `TestTheSpecReachesTheTurnOnItsStandardInput`: the fake writes the spec it read to a side file; it matches C6, its deadline is the turn's plus the slack, its `maxTokens` the option's, and the tokens appear nowhere else: not in the arguments, the environment or the output file.
- `TestASpecThatCannotBeDeliveredEndsTheTurnUnavailable`: `Skills` fails; `Exited` reports `unavailable`, not `crashed`.
- `TestARepeatedStartStartsNothing`.
- `TestExitCodesGiveTheClassBeforeTheDeadline` (a table): 10 `ended`, 11 `finished`, 12 `refused`, 13 `vendor`, 14 `interrupted`, 15 `unavailable`, 3 `crashed`, killed by a signal `crashed`.
- `TestAProcessEndingAtOrAfterItsDeadlineIsInterrupted`.
- `TestATurnEndOnTheBoardMeansEnded`: the fake exits 3, `Ended` says true, and `Exited` reports `ended`.
- `TestExitedIsReportedOnceAndNeverBeforeStartReturns`.
- `TestTheDeadlineKillsTheProcessGroup` and `TestAKilledTurnTakesItsChildrenWithIt` (Review Focus 3): a fake that starts a child of its own, which ignores its parent's end; at the deadline both are gone.
- `TestStopAndStopAllNeverWait`: each returns within 50 ms while turns run, and after `StopAll` no turn's process group is left.
- `TestATurnsOutputIsCappedAndTheTurnNeverFailsForIt`: a fake that prints 3 MiB and exits 10 leaves a 1 MiB file, and is `ended`.
- `TestTheTurnsEnvironmentIsMinimal`: the fake records its environment and working directory; it has `PATH`, a temporary `HOME` and `TMPDIR`, a temporary working directory, and nothing else of the parent's environment.
- `TestATurnsStandardInputStaysOpenForItsLife`: the fake sees no end-of-file until it is stopped.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/runner/process/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(runner): the process runner, for development and tests`.

### Task 14: The deployment, the registry at runtime, and the admin API

**Decisions:** 1 (the deployment file, its refusals, starting without a configuration), 11, 14, R14, R15, R19. **Contracts:** C1, C3 (`revision current`, `revision refused`), C4.

**Files:**
- Create: `internal/deploy/deploy.go`, `deploy_test.go`
- Create: `internal/registry/current/current.go`, `current_test.go`
- Create: `internal/work/app/views.go`, `views_test.go` (an item as the admin API shows it)
- Create: `internal/admin/admin.go`, `admin_test.go`

**Interfaces:**
- Consumes: `registryapp.Registry` (`Refresh`, `Current`, `At`); `config.Revision.Lock(images map[string]string)`; `llmapp.CheckSkill` (Task 11); `app.Coordinator.Open`, `ErrInvalid`, `ErrNotHeld` (Plan 7a), `ErrSkipped`, `Skipped` (Task 6), `ErrKeyReused`, `ErrNoRevision` (Task 8); `app.Leader.Status` (Task 5); `db.State` (Task 3).
- Produces:

```go
// package deploy
// Deployment is a deployment file (Contract C1), its defaults filled in.
type Deployment struct{ /* the fields of C1 */ }

// Load reads a deployment file: strictly, durations with their units, and
// defaults filled in; then Check.
func Load(path string) (*Deployment, error)

// Check refuses what Decision 1 refuses that needs no database, and what
// C1 says has the wrong form.
func (d *Deployment) Check() error

// package current
type Options struct {
	Refresh    time.Duration     // [1 min]
	Images     map[string]string // the runtime images
	Priced     func(model string) bool
	CheckSkill func(rev *config.Revision, text string) error // llmapp.CheckSkill
	Log        *slog.Logger
}

// Revisions is the registry at runtime: the current revision, and those
// items locked.
type Revisions struct{ /* ... */ }

func New(reg *registryapp.Registry, o Options) *Revisions
func (r *Revisions) Run(ctx context.Context) // refreshes until ctx ends
func (r *Revisions) Current() *config.Revision
func (r *Revisions) At(ctx context.Context, commit string) (*config.Revision, error)
func (r *Revisions) Lock(rev *config.Revision) (config.Lock, error)
func (r *Revisions) Skills(ctx context.Context, commit, agent string) ([]string, error)
// and the methods the two gateways' Revisions ports name.

// package app (internal/work/app)
// View is an item as the admin API shows it (Contract C4), read from the
// stores, off the coordinator's lock.
type View struct {
	ID               item.ID
	State            string
	Stop             string // empty while the run goes on
	HeldByLaterBuild bool
	Rounds           []RoundView
}

type RoundView struct {
	Round           int
	WrapUp          bool
	Ended           string // how the round ended, as RoundEnded records it
	Quiet           bool
	TurnEnds        []string
	FailedCalls     int
	FailedToolCalls int
	Settled         bool
	Report          board.Report
}

var ErrNoItem = errors.New("app: no such item")

func NewViews(records Records, boards *Boards) *Views
// Item folds an item's records and reads its board's report for each round.
// An item with a record or board event of a later version is a View with
// HeldByLaterBuild alone.
func (v *Views) Item(ctx context.Context, id item.ID) (View, error)

// package admin
type Ports struct {
	Coordinator interface {
		Open(ctx context.Context, in *app.OpenInput) (item.ID, error)
		Skipped() []item.ID
	}
	Leader interface {
		Status() (epoch int64, held, draining bool)
	}
	Schema interface {
		State(ctx context.Context) (db.State, error)
	}
	Items interface { // app.Views
		Item(ctx context.Context, id item.ID) (app.View, error)
	}
	Secrets interface {
		Secret(ctx context.Context, ref string) (string, error)
	}
}

type Options struct {
	Tenant   string
	TokenRef string // the admin token's secret reference
	MaxBody  int64  // [64 KiB]
	Log      *slog.Logger
}

func New(p Ports, o Options) http.Handler
// ItemID derives an item's id from its opening's key (R15).
func ItemID(tenant, key string) item.ID
```

**Notes for the implementer:**
- `deploy` decodes into its own types, with YAML tags and a duration type that refuses a bare number, and converts to the packages' types; every key of C1's example has a test.
- A revision becomes current only if `Priced` holds for every agent's model and `CheckSkill` passes every skill; otherwise it is refused, logged `revision refused`, and the last current revision, or none, stays. A revision that becomes current is logged `revision current`.
- The admin handler also refuses a request whose peer is not loopback, beside serve's refusal of a non-loopback address.
- The request's digest is SHA-256 over the canonical JSON of the opening (`type`, `title`, `routing` with its keys sorted, `principal`, `responders`).

**Tests (write first):**
- `TestADeploymentLoadsWithItsDefaults` (`internal/deploy`, unit): C1's required keys alone give every default of C1's table. Fails first: no package.
- `TestADeploymentWithAnUnknownKeyIsRefused` (Review Focus 1): `timing.lease.tll` is refused, and the error names `timing.lease.tll`.
- `TestADurationWithoutAUnitIsRefused` (Review Focus 1): `ttl: 15` is refused, naming the key.
- `TestMissingRequiredKeysAreNamed` (a table, one row per required key).
- `TestAReferenceWithoutItsSchemeIsRefused` and `TestAnImageThatIsNotADigestIsRefused`: `database: database-url` and `thinloop: registry.example/runtime:1` are refused, naming their keys.
- `TestAMaxTokensAboveTheGatewaysCapIsRefused`: `runner.maxTokens: 16385` is refused.
- `TestAShortConfigurationRefIsRefused`: `configuration.ref: main` is refused, naming the key and the full form; `refs/heads/main` is not.
- `TestANonLoopbackAdminAddressIsRefused`: `0.0.0.0:7403` and `192.0.2.1:7403` are refused; `127.0.0.1:0` and `[::1]:0` are not.
- `TestTheTimingsThatCannotHoldAreRefused` (a table): `Grace` below either record timeout; `TTL − Margin` not above `RenewEvery`; `takeWait` not below `TTL − Margin`; either database timeout above `TTL − Margin`.
- `TestAProcessRunnerNeedsUnsafeNoSandbox`.
- `TestARevisionBecomesCurrentOnlyIfEveryModelIsPricedAndEverySkillPasses` (`internal/registry/current`, unit): a revision with an unpriced model, and one with a skill `CheckSkill` refuses for that revision, are refused and logged `revision refused`; a good one becomes current and is logged `revision current`.
- `TestARefusedRevisionKeepsTheLastCurrentOne` (unit).
- `TestRefreshesRunEveryPeriodUntilTheContextEnds` (unit).
- `TestAMissingCommitFailsAt` (unit): `At` for a commit the repository lacks fails.
- `TestAnItemsViewFoldsItsRecordsAndReadsItsBoard` (`internal/work/app`, unit): an item with two ended rounds gives each round's evidence, as `RoundEnded` recorded it, and its board's report; an unknown id fails with `ErrNoItem`.
- `TestAViewOfAnItemOfALaterVersionSaysHeld` (unit): a record of version 2, or a board event of version 2, gives `HeldByLaterBuild` and nothing else.
- `TestHealthzAndReadyzNeedNoToken` (`internal/admin`, unit): `/readyz` is 200 when compatible and not draining, and 503 with the reason when the schema is behind, incompatible, or serve drains.
- `TestEveryV1RouteNeedsTheToken` (unit).
- `TestTheTokenIsResolvedOnEachRequest` (unit): after the secret changes, the old token is refused and the new one accepted.
- `TestOpeningAnItemByKey` (unit; Review Focus 5): 201 with an id equal to `ItemID(tenant, key)`; the same key and request, 200 with the same id; the same key with another title, 409.
- `TestAnOpeningWithoutAKeyIsRefused` (unit): 400.
- `TestOpeningAnswersByTheCoordinatorsRefusal` (unit, a table): `ErrNotHeld` 409; `ErrSkipped` 409 "held by a later build"; `ErrKeyReused` 409; `ErrInvalid` 400; `ErrNoRevision` 503.
- `TestABodyOverTheCapIsRefused` (unit): 413.
- `TestAnItemsViewShowsItsRoundsEvidenceAndReports` and `TestASkippedItemsViewSaysHeldByALaterBuild` (unit): the JSON of C4; an unknown id answers 404.
- `TestStatusShowsLeadingEpochSchemaDrainingAndSkipped` (unit).
- `TestARequestFromAPeerNotOnLoopbackIsRefused` (unit): 403.
- `TestItemIDsAreUUIDsOfVersion8` (unit): `ItemID` gives the same id for the same tenant and key, another for another key, and a UUID string of version 8 and the RFC 4122 variant.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/deploy/ ./internal/registry/current/ ./internal/admin/ ./internal/work/app/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(serve): the deployment file, the registry at runtime, and the admin API`.

### Task 15: `serve` and `inspect`, and the first scenarios

**Decisions:** 1, 2, 5, 6, 16, 17 (scenarios 1, 2, 3, 14 and 16), 18 (the check at start), R4, R6, R20, R22. **Contracts:** C1, C2, C3, C4, C5, C9 (Task 15's part).

**Files:**
- Create: `cmd/agentswarm/serve.go`, `cmd/agentswarm/inspect.go`; modify `cmd/agentswarm/run.go` to add both
- Create: `cmd/agentswarm/serve_test.go` (unit), `cmd/agentswarm/serve_integration_test.go` (refusals that need a database)
- Create: `test/e2e/harness/serve.go`, `log.go`, `admin.go`, `inspect.go` (C9's part made by Task 15)
- Create: `test/e2e/serve_test.go`
- Modify: `Makefile` (`test-e2e` runs with `-timeout 60m`, R20)

**Interfaces:**
- Consumes: everything Tasks 1 to 14 produce.
- Produces: `agentswarm serve --deployment FILE [--exit-with-stdin]`; `agentswarm inspect --deployment FILE (item ID | board RUN | calls RUN)`; Contracts C2, C3 (serve's messages) and C5; C9's part made by Task 15.

**Notes for the implementer:**
- **The order of wiring.** Load the deployment and open the secrets. Check the server's version with `db.CheckServer`, on a connection that sets no timeout, then open the main pool and the lease's pool with `db.OpenWith`. Refuse a schema behind or incompatible (`db.ReadSchema`), logging `schema incompatible`. Bind the three listeners, refusing a non-loopback admin address, so that their addresses are known before anything that needs them. Then make, in this order: the `app.Holding` (R4); the database's clock on the lease; one `clock.Timers` per clock; the stores, guarded by the lease (hides unguarded); the registry and `current.Revisions`, with `llmapp.CheckSkill`, refreshing; the sessions' registry; the MCP gateway (its upstream with the holding, and with the CA bundle as a pool that `mcptools.SystemRootsWith` builds, which keeps the system roots: `upstream.Options.RootCAs` replaces them, as Task 1 built it); the LLM gateway (with the holding); the process runner, with the gateways' bound URLs (its `Exited` bound after the coordinator exists); the coordinator, with the LLM gateway as its `Sweeper` and the tool calls' store as its `ToolCalls`; the leader, with the holding, the database's clock as its sampler, and an adapter that wraps `db.ErrIncompatible` from `db.Checker` with `app.ErrSchema`; and the admin API, with `app.Views` over the records and the boards. Hand every component the one JSON logger. Write the addresses file (C2), log `serve started`, serve the listeners, then lead under a context the signals do not cancel.
- **On the first signal**, Decision 6's six steps, in order. On a second signal, or the end of standard input under `--exit-with-stdin`, call the runner's `StopAll` and exit 1 at once. When `Leader.Done` closes with `Err` set, exit 1 without draining; after a drain, exit 0. Log `serve stopped` with the code.
- The harness's `Serve` runs `serve --deployment FILE --exit-with-stdin` under `StartParent`, and waits for the addresses file.

**Tests (write first):**
- `TestServeRefusesWhatDecision1Refuses` (`cmd/agentswarm`, integration, a table): a schema behind this build (no `migrate` run) is refused naming `agentswarm migrate`; a schema of level 99 with `min_reader` 99 is refused naming both levels. Fails first: no command.
- `TestServesHTTPServersBoundTheirReads` (`cmd/agentswarm`, unit): each of the three servers has `ReadHeaderTimeout` 10 s and `ReadTimeout` 30 s, and the LLM listener's body cap is the gateway's request size.
- `TestServeRefusesAPostgresOlderThan17` (e2e, the harness on `postgres:16-alpine`): exit 1, the version on stderr.
- `TestServeWritesItsAddressesAndBecomesReady` (e2e): the addresses file has the process id and three addresses; `/healthz` and `/readyz` answer 200; `/v1/status` says leading, with an epoch; the log has `serve started` and `lease taken`.
- `TestAConvergedRunStopsConvergedAfterItsWrapUpRound` (e2e; scenario 1): the item's view shows a wrap-up round whose evidence holds every agent's turn end, the stop `converged`, and `AuditVerify` returns exactly `store_unlocked`.
- `TestARunWhoseAgentsPostNothingStopsQuietAfterOneRound` (e2e; scenario 2).
- `TestANudgedTurnReachesEndTurn` and `TestAModelThatIgnoresTheNudgeLeavesItsRoundNotQuiet` (e2e; scenario 3): the vendor's requests show the nudge's text once per nudged turn.
- `TestNoSessionTokenLeavesThePlatform` (e2e; scenario 14): no match of `asw[ml]_[A-Za-z0-9]{43}` in serve's output, the turns' files, any request the vendor or the MCP servers received, headers included, or `inspect`'s output for the item, its board and its calls.
- `TestKillingTheHarnessParentLeavesNothingRunning` (e2e; scenario 16): after SIGKILL to the parent, serve's process and every process `turn started` named are gone within 5 s.
- `TestASecondSignalKillsEveryTurnAndExitsAtOnce` (e2e): with a turn waiting on a delayed vendor, a second SIGTERM makes serve exit 1 within 2 s, and the turn's process is gone.
- `TestInspectPrintsALaterVersionRaw` (e2e): a work record inserted with `"v": 2` prints with `raw` and no `body`.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./cmd/agentswarm/`, `go test -race -count=1 -tags integration ./cmd/agentswarm/` and `make test-e2e`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement.** A scenario that fails on a component's fault gets a component test that reproduces it first, in the component's package, then the fix.
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `feat(serve): agentswarm serve and inspect, with the first scenarios`, and each fix in its own commit before it.

### Task 16: Faults at the model vendor and the MCP servers

**Decisions:** 9, 12, 13, 17 (scenarios 4, 5, 6, 7, 12, 15, 17 and 18), 20, R5. **Contracts:** C1, C3, C4, C5, C9.

**Files:**
- Create: `test/e2e/vendor_faults_test.go`
- Modify: `test/e2e/harness/*.go` where a scenario needs more of it
- Modify: any component whose fault a scenario finds, with its component test

**Interfaces:**
- Consumes: `serve`, `inspect` and the harness (Task 15).
- Produces: the scenarios.

**Tests (write first):** each reads its item's view (C4) and asserts what Decision 17 says a person expects. "Not quiet" means the round's `quiet` is false and the run did not stop `quiet`.
- `TestAHangingVendorLeavesRoundsNotQuietAndTheBudgetStopsTheRun` (scenario 4): every round not quiet, its evidence unsettled or with failed model calls; the stop is the budget's.
- `TestASlowVendorsLateAnswersLeaveRoundsNotQuiet` (scenario 5): answers delayed past the round's time limit; no turn ends; not quiet.
- `TestVendorFailuresNeverMakeARoundQuiet` (scenario 6, subtests (a) to (e)): (a) 529s and 500s; (b) one ok answer, then 529s; (c) a 401 storm and a 429 storm, where the turns are `vendor` and each round lasts its whole time limit; (d) an answer with an unknown content block, where the turns crash and two rounds remove the agents; (e) a model that never stops calling tools reaches the step limit, and one that stops on `max_tokens`. No round is quiet.
- `TestATurnThatNeverEndsIsKilledAtItsDeadline` (scenario 7): with `runner.binary` a script that ignores its standard input and SIGTERM and sleeps for two minutes, the process `turn started` named is gone soon after the deadline, and the round is not quiet.
- `TestTheDailyCapStopsRunsAndStaffsNoNewItem` (scenario 12): with a small cap, the running run stops `cut`, and a new item is not staffed.
- `TestABadConfigurationCommitIsRefusedAndRunningItemsGoOn` (scenario 15): the bad commit is logged `revision refused`; a running item reaches its stop; a restart starts; an opening answers 503 until a good commit is made and refreshed, then 201.
- `TestFailedToolCallsKeepRoundsFromBeingQuiet` (scenario 17, subtests (a) to (f)): (a) every read answers HTTP 500, and the agents call `end_turn`: not quiet, `failedToolCalls` above zero; (b) a read whose output is over the cap, then `end_turn`: the round can be quiet; (c) a wrap-up round whose reads fail concludes nothing; (d) every read answers `isError`: not quiet; (e) a first read `isError`, then the same tool read again by the same agent with fixed arguments succeeds: the round can be quiet; (f) a write whose server is down, then `end_turn`: not quiet.
- `TestAServerDownAtStartLeavesItsRoundNotQuiet` (scenario 18): `inspect calls` shows a tool call named `<server>/tools/list` with failure `server`, and the round is not quiet.

- [ ] **Step 1: Write the tests above.** Run `make test-e2e`. Expected: each scenario either passes at once, which the report says, or fails on a component's fault, which then gets a component test that fails first, and a fix.
- [ ] **Step 2: Fix** what the scenarios find, each fix test first in its component.
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `test(e2e): faults at the model vendor and the MCP servers`, and each fix in its own commit before it.

### Task 17: Faults of the holding: a crash, a partition, the database, a rollback and a shutdown

**Decisions:** 2, 4, 6, 7, 17 (scenarios 8, 9, 10, 11 and 13), 18, 19, R22. **Contracts:** C2, C3, C4, C5, C9.

**Files:**
- Create: `test/e2e/holding_faults_test.go`
- Modify: `test/e2e/harness/*.go` where a scenario needs more of it
- Modify: any component whose fault a scenario finds, with its component test

**Interfaces:**
- Consumes: `serve`, `inspect` and the harness (Task 15); the toxiproxy (Task 2).
- Produces: the scenarios.

**Tests (write first):** each runs two instances, `a` and `b`, unless it says otherwise, with the lease's timing shortened in their deployments (`ttl: 3s`, `renewEvery: 1s`, `margin: 1s`, `takeEvery: 1s`, `takeWait: 500ms`, both database timeouts `2s`). A bound "within D4's bound" is TTL + `TakeEvery` + `takeWait`, plus 2 s for the processes.
- `TestAStandbyTakesOverFromAKilledLeader` (scenario 8): SIGKILL to `a`'s serve, with a model call in flight. `b` logs `lease taken` within D4's bound; `inspect calls` shows `a`'s call with status `unknown` and a reason naming `a`; every process `a`'s `turn started` named is gone; `AuditVerify` returns exactly `store_unlocked`, and `inspect item` shows no round opened twice.
- `TestAPartitionedLeaderSendsNothingAfterTheStandbysTake` (scenario 9): SIGSTOP to `a` for TTL + 2 s; `b` takes the lease within D4's bound; after SIGCONT, no request the vendor or the MCP servers got with `a`'s credentials arrived after `b`'s `lease taken` wall time, and `a` logs `lease lost` and `fenced`.
- `TestALeaderCutFromTheDatabaseFencesAndTakesTheLeaseAgain` (scenario 10; one instance): `Cut("a")`; `a` logs `fenced` within `TTL − Margin` of its last good renewal; after `Restore("a")`, `a` logs `lease taken` with a later epoch within D4's bound, and its item reaches a stop.
- `TestARollbackSkipsTheItemsALaterBuildWrote` (scenario 11(a)): of two active items, one gets a work record (then, in a subtest, a board event) with `"v": 2` by `psql`; after SIGKILL to `a`, `b` takes the lease; the item's view says `heldByLaterBuild`, `/v1/status` counts one skipped item, `b` logs `item skipped` once, and the other item reaches its stop.
- `TestServeStartsOnACompatibleLaterSchema` (scenario 11(b); one instance): a `schema_migrations` row of level 99 whose `min_reader` is this build's level; serve starts, and `/readyz` answers 200.
- `TestAnIncompatibleSchemaStopsTheLeaderWithoutADrain` (scenario 11(c); one instance): a row of level 99 with `min_reader` 99 makes a new serve exit 1 at start, logging `schema incompatible`; inserted while `a` leads, it makes `a` log `schema incompatible` at its next renewal, then `fenced` and `lease released`, with no `draining` line, and exit 1.
- `TestShutdownDrainsUnderTheHeldLeaseThenReleases` (scenario 13): SIGTERM to `a` while the vendor delays a call; `a` logs `draining` and then `lease released`, in that order, and exits 0; the delayed call has an outcome; `b` logs `lease taken` within `3 × mcp.recordTimeout` + the drain bound + `TakeEvery` + `takeWait` + 2 s of the signal.

- [ ] **Step 1: Write the tests above.** Run `make test-e2e`. Expected: each scenario either passes at once, which the report says, or fails on a component's fault, which then gets a component test that fails first, and a fix.
- [ ] **Step 2: Fix** what the scenarios find, each fix test first in its component.
- [ ] **Step 3: Verify.** `make test`, `make test-integration`, `make test-e2e`, `golangci-lint run ./...`. Expected: every package `ok`; `0 issues.`
- [ ] **Step 4: Commit:** `test(e2e): faults of the holding`, and each fix in its own commit before it.

### Task 18: The previous release's scenarios, and the documents

**Decisions:** every decision and resolution, since this task writes their amendments to the spec, the ADRs and Plan 7a: its brief carries the whole Decisions section; 18 (the N−1 test), R16, R17 and R22 in particular. **Contracts:** C9 (`AGENTSWARM_MIGRATE_BIN`, `AGENTSWARM_E2E_DATABASE`).

**Files:**
- Create: `test/compat/main.go`, `test/compat/main_test.go`; modify `Makefile` (`test-compat`)
- Modify: `plan/2026-10-11-agent-swarm-platform-spec.md` (Sections 5.6, 8.2 and 15)
- Modify: `plan/adr/ADR-004-durable-execution-postgres.md` (the time base; fencing at dispatch carried), `plan/adr/ADR-006-agent-runtime-sdk-sandbox.md` (the process runner for development; the conformance tests; the nudge; criterion 4 to Plan 7d-2)
- Modify: `plan/2026-10-12-implementation-roadmap.md` (Plan 7d split into 7d-1 and 7d-2; status)
- Modify: `plan/2026-10-18-plan-7a-coordinator-core.md` (a note under its Decisions 6, 7 and 9, and under its Plan 7b constraint on new record kinds, naming this plan's amendment)
- Modify: `README.md` (`serve`, `migrate`, `turn`, `inspect`; `make test-clean`, whose backups are `<id>-<UTC start>.inspect.json` and `<id>-<UTC start>-data.tar` under `.test-clean/`, as Task 1 built it, and `make test-compat`)
- Modify: this plan's status line

**Interfaces:**
- Consumes: the harness's scenarios 1, 2 and 8 (Tasks 15 and 17); `AGENTSWARM_E2E_DATABASE` (Task 2) and `AGENTSWARM_MIGRATE_BIN` (Task 3).
- Produces: `go run ./test/compat`, as `make test-compat`.

**Tests (write first):**
- `TestCompatSaysSoAndPassesWithoutATag` (`test/compat`, unit, with a fake runner): no tag, so it prints `no release tagged; nothing to check` and exits 0. Fails first: no program.
- `TestCompatRunsThePreviousScenariosAgainstASchemaThisBuildMigrated` (unit, with a fake runner): with the tag `v0.1.0`, the commands, in order, are a worktree at the tag, the old binary built there, a labelled Postgres started, this build's `migrate`, the old tree's scenarios 1, 2 and 8 with `AGENTSWARM_BIN` the old binary, `AGENTSWARM_MIGRATE_BIN` this one and `AGENTSWARM_E2E_DATABASE` the database, then this build's `audit anchor` and `audit verify` (passing when the only finding is `store_unlocked`, R22) and `inspect` over each item they left; and the worktree and the container are removed after a backup (R16).

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./test/compat/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** `test/compat` and the target; then write the documents: the spec's Section 5.6 (quiet and taking part, Decision 9), Section 8.2 (`unavailable`; failed tool calls; the coordinator's crash row as Decision 19's skip; the lease's bounds and the drain), and Section 15 (this plan's parameters: the runtime's slack, the drain bound, the refresh, the database timeouts, the take's wait, the output cap, `runner.maxTokens`, the vendor's retry backoff, and the admin body's cap); ADR-004's time base (Decision 2); ADR-006 (Decisions 12 and 13); the roadmap; Plan 7a's notes; the README.
- [ ] **Step 3: Verify.** `make test-compat` prints `no release tagged; nothing to check`; `make lint test-all` passes; `grep -rn -i "employee" README.md plan/*.md plan/adr/*.md` finds no new line. Expected: as stated.
- [ ] **Step 4: Commit:** `test: make test-compat`; `docs: Plan 7d-1's amendments to the spec, the ADRs, the roadmap and Plan 7a`.

## Changes made during execution

- **Task 1** (caf22a3, f452b9b; fix round 0a9c910, 9fac9d1):
  - `upstream.Options.RootCAs` replaces the system roots, as `tls.Config.RootCAs` does, since an `x509.CertPool` cannot be merged after the fact; `mcptools.SystemRootsWith(pem)` builds the pool of the system roots and a bundle, which `registry hash-tools --ca` uses and serve must use (Task 15's text says so). A test shows that a configured pool still refuses a server it does not vouch for.
  - `make test-clean` names its backups `<id>-<UTC start>.inspect.json` and `<id>-<UTC start>-data.tar`, and never replaces an earlier backup; the S3 store test's MinIO carries `agentswarm.data=/data`.
  - `mcptools.List` takes the roots it checks a server against.
