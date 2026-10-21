# ADR-006 Agent runtime: a thin loop on the Messages API

| | |
|---|---|
| Status | Accepted. Spike C (roadmap Plan 2) decided it, and its live runs, on Claude Haiku 5.5, confirmed it. Plan 7d-1 built it as `agentswarm turn`, with a process runner for development and tests (As built, below); Plan 7d-2 runs it in sandboxes. |
| Date | 2026-10-10; revised 2026-10-12; decided and confirmed 2026-10-13 |
| Related | Spec Sections 2, 5.2, 5.6, 8.2 and 9.3; roadmap Plans 2 and 7d-1; `spike/runtime/results/RESULTS.md`; ADR-001; ADR-007; ADR-013; ADR-015 |

## Context

- Third-party agent runtimes ship built-in tools for shell, files and the web. Used as they come, those tools would bypass the gateways, the budgets and the audit.
- The platform must control how context is built (redaction, data boundaries, round snapshots) and keep it bounded between rounds. In SDK-style runtimes the runtime manages much of this itself.
- Spike C tested the claims about SDK defaults in a sandbox. Canaries were planted in the working directory, the home directory, settings, auto memory and the environment.
- The platform is written in Go (ADR-015). The runtime inside the sandbox may use another language (spec Section 2.2).

## Options

- **A.** Build a full agent framework.
- **B.** The Claude Agent SDK, with the platform controlling the tool surface and egress.
- **C.** Run a coding-agent command-line tool directly.
- **D.** A thin loop on the model API: tool calls and structured output, with a step limit.
- **E.** An off-the-shelf diagnosis tool used as a runtime (ADR-001), where one can meet the criteria below.

## Decision

D: a thin loop on the Messages API, written in Go, one process per agent per round.

Spike C ran B and D in a sandbox whose only reachable endpoints were stand-ins for the two gateways. The sandbox was an internal Docker network with no gateway, checked against a listener on the host. The decision rule of roadmap Plan 2 chose D:
- D meets all seven criteria by construction, and every text in its requests comes from the platform.
- B keeps host content out of its prompts only through configuration, with at least four settings.
- Even hardened, B adds an environment block and a device id to its requests, and it refreshes its tools within a turn.

The criteria still bind any future runtime:

1. **Round-based turns.** A turn ends when the agent's `end_turn` call succeeds or the round time limit passes. Amended on 2026-10-21: the runtime calls `end_turn` when its model asks for it, and only then, since a quiet round rests on each agent's own turn end (spec Section 5.6).
2. **Gateway-only MCP.** The platform's MCP gateway is the only MCP endpoint.
3. **No reach outside.** Built-in tools that reach outside the sandbox are disabled.
4. **Egress denied by default.** Only the two gateways are reachable.
5. **No host content.** Host or workspace configuration never enters prompts.
6. **Bounded context.** The session context is kept bounded between rounds. Decided on 2026-10-19 (History): each turn starts from a fresh context, so none is kept between rounds.
7. **Tool lists change at round boundaries** only.

## Results

| Criterion | D: thin loop | B: Claude Agent SDK 0.2.165, hardened |
|---|---|---|
| 1. Round-based turns | Met. It ends at `end_turn` and at the round limit; no call starts after the limit | Met, with a `PostToolUse` hook on `end_turn` and a runner that cancels through anyio and `interrupt()` at the limit. Cancelled through asyncio instead, it kept calling for 3 s after the limit |
| 2. Gateway-only MCP | Met by construction | Met with `setting_sources=[]` and `strict_mcp_config`. Without them, the project's and the user's MCP servers and the settings hook started |
| 3. No reach outside | Met: the model sees only the gateway's tools | Met with `tools=[]` |
| 4. Egress denied | Met | Met |
| 5. No host content | Met: every text in its requests comes from the platform | Met for content with `setting_sources=[]`, `strict_mcp_config` and auto memory off; with only the first two, auto memory reached the model. Even fully hardened, it adds an identity line and an environment block, and sends a device id in its request metadata. The environment block gives the working directory, platform, shell, OS version with the host's kernel, and a git flag |
| 6. Bounded context | Met by construction: a new process each round | Met: no session persistence and a new configuration directory each run |
| 7. Tool lists change at round boundaries | Met: it lists tools once per turn | Not met: it refreshes on `tools/list_changed` within a turn |

For criterion 4, an internal Docker network alone let both candidates reach the host through the network's gateway. That was a defect of the first sandbox, not of either runtime. The sandbox now has no gateway.

Also measured, in `spike/runtime/results/RESULTS.md`:
- **Code for one diagnosis flow:** 211 lines of Go for D. B takes 111 lines of Python, including its hardening and its cancellation.
- **Request settings:** B defaults to `max_tokens` 128000 with adaptive thinking; D sends what the runner sets.
- **Release cadence over 90 days:** 46 releases of claude-agent-sdk, each bundling a new CLI; 27 of anthropic-sdk-go.
- **Live runs**, on Claude Haiku 5.5, the author's choice for cost: both candidates ended every turn with `end_turn`, 3 of 3 each, and reached the expected hypothesis in all six runs. Point 1 of the decision rule keeps both, so the decision stands. The runs were not repeated on Sonnet 5.5.
- **The desk check of E**, HolmesGPT 0.43.0: see the rejected options.

Whatever the runtime:

- the sandbox holds no model key; the LLM gateway adds it outside the sandbox (ADR-007);
- the sandbox's network has no gateway, because an internal network alone still reaches the host (spec Section 9.3);
- only tools registered with the platform are exposed;
- a lost session restarts from the current board snapshot, never from the runtime's own session store (spec Section 5.2);
- the runtime version is pinned in the runtime image, which every work item locks (spec Section 3);
- the MCP gateway holds each session's tool list until its round ends, and sends no `tools/list_changed` within a round (criterion 7, as a gateway rule).

## As built (Plan 7d-1)

Plan 7d-1 built runtime D as `agentswarm turn`, ran it as a host process, and ported the conformance suite.

**The loop** (`internal/runner`). The runtime reads its turn's spec, one JSON object, on standard input, and keeps reading it: when standard input ends, the turn ends at once. It lists its tools once per turn, gives the model each text block of a tool's result as its own, sends each of the agent's skills as a system block of its own, and holds its session tokens in memory, sending them only in request headers. Its client is built with `option.WithoutEnvironmentDefaults()` and `option.WithMaxRetries(0)`, uses no proxy and follows no redirect: the LLM gateway retries the vendor, the runtime retries no model call. It stops at 50 model calls, and asks each for at most the deployment's `runner.maxTokens` [4,096]; it keeps no token budget or prompt limit of its own, since the LLM gateway enforces the run's budget and the request's size. It ends a turn `ended` only when its `end_turn` call succeeds; an `end_turn` the gateway could not serve is tried once more, at once.

**The nudge.** When the model answers without calling a tool, the runtime sends it, once per turn, one fixed message of the platform's: "You answered without calling a tool. If you are done, call end_turn; otherwise, continue." A second answer without a tool ends the turn `finished`. The text is the platform's, so criterion 5 holds, and the LLM gateway's allowlist needs no change. A quiet round needs every agent's own turn end (spec Section 5.6), and Plan 3b's runs, without the nudge, ended 34 of 117 turns by `end_turn`. Measured live on 2026-10-21, on `claude-haiku-5-5` with Plan 3b's 13 cases and 3 trials, 117 of 117 turns ended by `end_turn`: 36 unprompted, and 81 after the nudge, 75 of them at once; none answered twice without a tool. Hits were 29 of 39, as in Plan 3b, at about the same cost. The mechanism check runs without the nudge unless given `--nudge`, so that its measurements stay comparable with Plan 3's.

**How a turn ends.** The runtime's exit code names how its turn ended, and the runner reports it as the turn's class. The LLM gateway marks every reply that is not a success with `X-Agentswarm-Outcome`, `refused`, `vendor`, `cut` or `unavailable`, and the MCP gateway answers a session gone with the JSON-RPC code -32001 and its own failures with -32002, so that the runtime tells them apart:

| Code | Class | When |
|---|---|---|
| 10 | ended | The `end_turn` call succeeded |
| 11 | finished | A second answer without a tool, after the nudge; the step limit; an answer cut at `max_tokens`, or at the model's context window (stop reason `model_context_window_exceeded`) |
| 12 | refused | A gateway refused the call; the model refused (`stop_reason: refusal`) |
| 13 | vendor | The model vendor failed, after the LLM gateway's retries |
| 14 | interrupted | The LLM gateway cut the call; the MCP session is gone; the runtime's own deadline, or the end of its standard input; an `end_turn` refused because the round ended |
| 15 | unavailable | A gateway could not serve the turn, or its reply had no mark; no HTTP answer at all; a second `end_turn` the gateway could not serve |
| any other | crashed | A panic; an answer the runtime cannot read: malformed content, an unknown block type, an unknown stop reason, or `pause_turn`, since the runtime offers no server tools; any other error |

The class advises only. Whether a round is quiet, and whether a wrap-up round concludes, is decided from the platform's own records: the agents' turn ends on the board, and the model calls and tool calls the gateways recorded (spec Section 5.6).

**The process runner, for development and tests only** (`internal/runner/process`). It runs each turn as a host process of `agentswarm turn`, and `serve` allows it only with `unsafeNoSandbox: true`. Each turn runs in its own process group, with a minimal environment and a temporary home and working directory; its output goes to a file of its own, of at most [1 MiB]; and its standard input stays open for the turn's life, so the runtime exits when its runner does. At the deadline, and on a stop, the runner kills the process group; the runtime's own deadline, [2 s] later, is a backstop. A launch that fails is tried again, and its turn counts as unstarted; a spec the runner cannot deliver ends the turn `unavailable`, never `crashed`. It does not meet criterion 4: a host process reaches what the host reaches, and a process that leaves its group escapes the kill. Plan 7d-2's runner implements the same port in a Docker sandbox.

**The conformance tests** (`test/e2e/conformance_test.go`). Criteria 1, 2, 3, 5, 6 and 7 are tests of the runtime run as a host process, against stand-ins for the two gateways, and one more checks that the runtime calls `end_turn` only when its model asks for it. Criterion 4, egress, needs the sandbox's network, and comes with Plan 7d-2.

## Rejected options

- **A:** expensive, and not where the platform differs.
- **B:**
  - Keeping host content out takes at least four settings, and auto memory leaked with three. Its frequent releases can change what those settings mean without notice.
  - Even hardened, it sends an environment block and a device id.
  - It refreshes its tools within a turn.
  - It adds Python and a bundled CLI of about 240 MB to a Go platform.
- **C:** it loads project configuration automatically and updates itself, so it is the least controllable.
- **E** (HolmesGPT 0.43.0, desk-checked): an `end_turn` call from the gateway cannot stop an investigation, and it has no deadline, so it fails criterion 1 on its own. Its outward-reaching tools are switched off one by one, a deny-list that a later release can extend. It stays an external comparator (spec Section 13.3).

## Consequences

- **The platform owns the loop.** Plan 7's runner builds it test-driven, including what the SDK would have supplied: retries on model errors, prompt caching, and step and token budgets. Plan 7d-1 built it, with the LLM gateway retrying the vendor and enforcing the budgets, and the runtime keeping a step limit (As built, above).
- **Language.** The runner is Go, like the platform (ADR-015), and the runtime image needs neither Python nor Node.
- **Context between rounds.** D starts every round from a fresh context: the full board snapshot plus the round's prompt. Spec Section 5.2 describes a session that persists across a run's rounds, with bounded context and per-round deltas. Plan 7 was to do one of two things, and Plan 7a did the first, a fresh context for each turn (History, 2026-10-19):
  - amends Section 5.2 to a fresh context per round;
  - or builds runner-managed context, keeping the agent's own exchanges within a bound, with its own test.

  B was not tested on sessions that persist across rounds.
- **The sandbox network.** An internal network whose bridge has no address on the host (`gateway_mode_ipv4=isolated`), or an equivalent default-deny, is the sandbox's egress mechanism. Plan 7d-2 makes it so, with the sandbox runner, and the suite's host probe is its check.
- **The conformance test.** The spike's sandbox suite, `spike/runtime/criteria`, is the conformance test for the runner. Plan 7d-1 ported criteria 1, 2, 3, 5, 6 and 7, and Plan 7d-2 ports criterion 4 with the sandbox (As built, above).
- **Ending a round.** The runner kills the sandbox at the limit rather than waiting for the runtime to exit. With a call still in flight, both runtimes took about 5 s more to exit, a grace period in their client libraries. Plan 7d-1's process runner kills the turn's process group at the limit.
- **The contract.** The platform's contract with any runtime stays "MCP tools plus the LLM gateway". The Go runner starts the runtime inside a sandbox (ADR-015).

## Reversibility

Medium. The runtime is swappable behind that contract, and the suite tells whether a candidate meets the criteria.

## Revisit when

- The loop needs a capability that would cost more to build than to adopt. Examples are managing context within a long round, and the persistent session of spec Section 5.2.
- An SDK offers a documented locked-down mode that passes the suite with its defaults.
- A second model vendor is needed (ADR-007).

## History

- **2026-10-10:** proposed: spike C to choose between the Claude Agent SDK and a thin loop.
- **2026-10-12:** rewritten in English. The seven criteria added, and off-the-shelf tools added as candidates (spec Section 16).
- **2026-10-13:** decided by spike C, provisionally: D. B refreshed its tools within a turn, and kept host content out only through configuration. The live runs were skipped; they are confirmed at the start of Plan 3. After the final review:
  - the sandbox gained an isolated gateway;
  - B's runner moved to anyio cancellation;
  - B's results were restated from measurements, including an ablation.
- **2026-10-13, later:** the live runs ran on Claude Haiku 5.5, chosen for cost. D and B each ended 3 of 3 turns with `end_turn`, so the decision is no longer provisional.
- **2026-10-19:** Plan 7a amends spec Section 5.2 to a fresh context per turn: a session's identity persists across rounds, its tokens are new each round, and the model's context does not persist. The runtime sends the tokens as headers only; no token is ever in the model's context.
- **2026-10-21:** Plan 7d-1 built the runtime as `agentswarm turn`, with the nudge, which a live probe measured at 117 of 117 turns ended by `end_turn`, and exit codes that name how a turn ended; a process runner runs it for development and tests. Criterion 1 now asks that a runtime call `end_turn` when its model does, and only then. Criteria 1, 2, 3, 5, 6 and 7 are tests of the runtime as a host process; criterion 4 moves to Plan 7d-2, with the sandbox.
