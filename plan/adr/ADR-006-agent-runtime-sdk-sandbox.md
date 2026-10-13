# ADR-006 Agent runtime, chosen by a spike

| | |
|---|---|
| Status | Proposed. Spike C decides it, and its result is written back here. |
| Date | 2026-10-10; revised 2026-10-12 |
| Related | Spec Sections 2, 5.2, 5.6 and 9.3; roadmap Plan 2; ADR-001; ADR-007; ADR-015 |

## Context

- Third-party agent runtimes ship built-in tools for shell, files and the web. Used as they come, those tools would bypass the gateways, the budgets and the audit.
- The platform must control how context is built (redaction, data boundaries, round snapshots) and keep it bounded between rounds. In SDK-style runtimes the runtime manages much of this itself.
- Claims about SDK defaults, such as loading host configuration, the meaning of tool switches and automatic memory, are not verified.
- The platform is written in Go (ADR-015). The runtime inside the sandbox may use another language (spec Section 2.2).

## Options

- **A.** Build a full agent framework.
- **B.** The Claude Agent SDK, with the platform controlling the tool surface and egress.
- **C.** Run a coding-agent command-line tool directly.
- **D.** A thin loop on the model API: tool calls and structured output, with a step limit.
- **E.** An off-the-shelf diagnosis tool used as a runtime (ADR-001), where one can meet the criteria below.

## Decision

Spike C (roadmap Plan 2) chooses between B and D, and checks E where a candidate exists. The chosen runtime must meet seven criteria (added 2026-10-12, spec Section 16):

1. **Round-based turns.** A turn ends when the agent calls `end_turn` or the round time limit passes.
2. **Gateway-only MCP.** The platform's MCP gateway is the only MCP endpoint.
3. **No reach outside.** Built-in tools that reach outside the sandbox are disabled.
4. **Egress denied by default.** Only the two gateways are reachable.
5. **No host content.** Host or workspace configuration never enters prompts.
6. **Bounded context.** The session context is kept bounded between rounds.
7. **Tool lists change at round boundaries** only.

The spike also records how much code one diagnosis flow takes, and how often the runtime's version changes.

Whatever the spike chooses:

- the sandbox holds no model key; the LLM gateway adds it outside the sandbox (ADR-007);
- only tools registered with the platform are exposed;
- a lost session restarts from the current board snapshot, never from the runtime's own session store (spec Section 5.2);
- the runtime version is pinned in the runtime image, which every work item locks (spec Section 3).

## Rejected options

- **A:** expensive, and not where the platform differs.
- **C:** it loads project configuration automatically and updates itself, so it is the least controllable.

## Consequences

- The platform's contract with any runtime is "MCP tools plus the LLM gateway". The Go runner starts the chosen runtime inside a sandbox (ADR-015).
- When the spike ends, its result and the measured criteria are written here, and the status changes to Accepted.

## Reversibility

Medium. The runtime is swappable behind that contract.

## Revisit when

- A new version of the chosen runtime fails any criterion, especially criterion 5.
- A second model vendor is needed (ADR-007).

## History

- **2026-10-10:** proposed: spike C to choose between the Claude Agent SDK and a thin loop.
- **2026-10-12:** rewritten in English. The seven criteria added, and off-the-shelf tools added as candidates (spec Section 16).
