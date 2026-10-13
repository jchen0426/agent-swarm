# ADR-006 Agent runtime: a thin loop on the Messages API

| | |
|---|---|
| Status | Accepted (provisional). Spike C (roadmap Plan 2) decided it; the live runs it skipped are confirmed at the start of Plan 3. |
| Date | 2026-10-10; revised 2026-10-12; decided 2026-10-13 |
| Related | Spec Sections 2, 5.2, 5.6 and 9.3; roadmap Plan 2; `spike/runtime/results/RESULTS.md`; ADR-001; ADR-007; ADR-013; ADR-015 |

## Context

- Third-party agent runtimes ship built-in tools for shell, files and the web. Used as they come, those tools would bypass the gateways, the budgets and the audit.
- The platform must control how context is built (redaction, data boundaries, round snapshots) and keep it bounded between rounds. In SDK-style runtimes the runtime manages much of this itself.
- Spike C tested the claims about SDK defaults in a sandbox, with canaries in the working directory, the home directory, settings, git and the environment.
- The platform is written in Go (ADR-015). The runtime inside the sandbox may use another language (spec Section 2.2).

## Options

- **A.** Build a full agent framework.
- **B.** The Claude Agent SDK, with the platform controlling the tool surface and egress.
- **C.** Run a coding-agent command-line tool directly.
- **D.** A thin loop on the model API: tool calls and structured output, with a step limit.
- **E.** An off-the-shelf diagnosis tool used as a runtime (ADR-001), where one can meet the criteria below.

## Decision

D: a thin loop on the Messages API, written in Go, one process per agent per round.

Spike C ran B and D in a sandbox that reached only stand-ins for the two gateways. The decision rule of roadmap Plan 2 chose D: it meets all seven criteria by construction. B meets criteria 1–6 only through configuration, and fails criterion 7 on its own.

The criteria still bind any future runtime:

1. **Round-based turns.** A turn ends when the agent calls `end_turn` or the round time limit passes.
2. **Gateway-only MCP.** The platform's MCP gateway is the only MCP endpoint.
3. **No reach outside.** Built-in tools that reach outside the sandbox are disabled.
4. **Egress denied by default.** Only the two gateways are reachable.
5. **No host content.** Host or workspace configuration never enters prompts.
6. **Bounded context.** The session context is kept bounded between rounds.
7. **Tool lists change at round boundaries** only.

## Results

| Criterion | D: thin loop | B: Claude Agent SDK 0.2.165, hardened |
|---|---|---|
| 1. Round-based turns | Met. It ends at `end_turn`, and its turn ends at the round limit; its process exits about 5.5 s later, while the MCP Go SDK closes the session | Met, with a `PostToolUse` hook on `end_turn`. Its process exits about 6 s after the round limit |
| 2. Gateway-only MCP | Met by construction | Met only with `strict_mcp_config`, `setting_sources=[]` and a private `CLAUDE_CONFIG_DIR`. With its defaults, the project's and the user's MCP servers started |
| 3. No reach outside | Met: the model sees only the gateway's tools | Met with `tools=[]` |
| 4. Egress denied | Met | Met, with nonessential traffic switched off |
| 5. No host content | Met: requests hold only the skill, the round's prompt and the turn | Met for content only when hardened, with twelve environment switches. With its defaults, CLAUDE.md content reached the model. Hardened, it still sends its working directory |
| 6. Bounded context | Met by construction: a new process each round | Met: no session persistence and a new configuration directory each run |
| 7. Tool lists change at round boundaries | Met: it lists tools once per turn | Not met: it refreshes on `tools/list_changed` within a turn |

Also measured, in `spike/runtime/results/RESULTS.md`:
- **Code for one diagnosis flow:** 211 lines of Go for D; 92 lines of Python for B, plus its hardening.
- **Release cadence over 90 days:** 46 releases of claude-agent-sdk, each bundling a new CLI; 27 of anthropic-sdk-go.
- **Live runs** on the real model: not run. The decision is provisional until they run at the start of Plan 3.
- **The desk check of E**, HolmesGPT 0.43.0: see the rejected options.

Whatever the runtime:

- the sandbox holds no model key; the LLM gateway adds it outside the sandbox (ADR-007);
- only tools registered with the platform are exposed;
- a lost session restarts from the current board snapshot, never from the runtime's own session store (spec Section 5.2);
- the runtime version is pinned in the runtime image, which every work item locks (spec Section 3);
- the MCP gateway holds each session's tool list until its round ends, and sends no `tools/list_changed` within a round (criterion 7, as a gateway rule).

## Rejected options

- **A:** expensive, and not where the platform differs.
- **B:** it meets criteria 2 and 5 only through about fifteen settings, which its frequent releases can change silently. With its defaults it leaks host content into prompts. It fails criterion 7 on its own. It adds Python and a bundled CLI of about 240 MB to a Go platform.
- **C:** it loads project configuration automatically and updates itself, so it is the least controllable.
- **E** (HolmesGPT 0.43.0, desk-checked): an `end_turn` call from the gateway cannot stop an investigation, and it has no deadline, so it fails criterion 1 on its own. Its outward-reaching tools are switched off one by one, a deny-list that a later release can extend. It stays an external comparator (spec Section 13.3).

## Consequences

- The platform owns the loop. Plan 7's runner builds it test-driven, including what the SDK would have supplied: retries on model errors, prompt caching, and step and token budgets.
- The runner is Go, like the platform (ADR-015), and the runtime image needs neither Python nor Node.
- The spike's sandbox suite, `spike/runtime/criteria`, is the conformance test for the runner. Plan 7 ports it.
- The runner ends a round by killing the sandbox at the limit, not by waiting for the runtime to exit: both candidates took 5–6 s to exit after the limit.
- The platform's contract with any runtime stays "MCP tools plus the LLM gateway". The Go runner starts the runtime inside a sandbox (ADR-015).

## Reversibility

Medium. The runtime is swappable behind that contract, and the suite tells whether a candidate meets the criteria.

## Revisit when

- The loop needs a capability that would cost more to build than to adopt, such as managing context within a long round.
- An SDK offers a documented locked-down mode that passes the suite with its defaults.
- A second model vendor is needed (ADR-007).

## History

- **2026-10-10:** proposed: spike C to choose between the Claude Agent SDK and a thin loop.
- **2026-10-12:** rewritten in English. The seven criteria added, and off-the-shelf tools added as candidates (spec Section 16).
- **2026-10-13:** decided by spike C, provisionally: D. B failed criterion 7 on its own, and met criteria 2 and 5 only through configuration. The live runs were skipped; they are confirmed at the start of Plan 3.
