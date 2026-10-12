# ADR-013 The MCP gateway as the single policy enforcement point

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-12 |
| Supersedes | ADR-005 |
| Related | Spec Sections 2.1, 3.3, 5.3, 6, 8.2 and 9; ADR-004; Phase 1 backlog |

## Context

- Every external side effect is an MCP tool call (spec Section 1.1).
- MCP's `tools/call` is a single call, without prepare, verify or reconcile semantics. Tool annotations are hints, and an untrusted server can set them to anything.
- Agents work with injected content in their context, so safety cannot depend on what a model decides.

## Options

- **A.** Agents connect to MCP servers directly, and each server enforces its own policy.
- **B.** Platform-owned effect adapters for writes, and MCP for reads (ADR-005).
- **C.** One platform MCP gateway between agents and every registered server: the only MCP endpoint agents see, and the only place tool policy is enforced.

## Decision

C.

- **Tool policy, per registered tool:** `schemaHash`, `effect`, `audience`, `redact`, `partialWhen` and `bounds`; for write tools also `target`, `scope`, `candidates`, `summary` and `facts` (spec Section 3.3). Unlisted tools are denied.
- **Annotations are never trusted.** The platform sets `effect`.
- **Pinning.** Each tool is pinned by a hash of its name, description and schemas. A changed hash hides the tool at once, even in running items, until it is reviewed again.
- **Audit before the call.** The audit record is written before a call is forwarded. If the write fails, the call is rejected (spec Section 6.2).
- **Record-only proposals in Phase 0.** A write call ends as `recorded` after its argument, candidate and scope checks and the proposer's own-evidence rule. No write reaches any server (spec Sections 5.3 and 6.3).
- **Server limits.** Calls at once per server, a share for comparison and probation runs, reserved capacity for platform reads, degraded marks and read retries (spec Section 6.2).
- **Credentials stay behind the gateway.** Sandboxes hold only short-lived gateway tokens (spec invariant 1).
- **Platform reads** (candidates, facts, routing, identity) run under the platform's own identity and are audited like any other call.
- **No self-change.** Registration review verifies invariant 7.
- **The write path** (approval, execution, effects, checks) is drafted in the Phase 1 backlog and designed after the gate.

## Rationale

- One enforcement point can be tested on every commit: the gateway contract layer of spec Section 12.1.
- Policy as data can be reviewed like any other configuration.
- Servers can come from the ecosystem without being trusted.

## Rejected options

- **A:** policy would depend on each server, and credentials would reach agents.
- **B:** owning adapters ties the platform to domain technology, against invariant 4.

## Consequences

- The gateway is on every tool call's path. It runs behind a lease (ADR-004). When it or the database is down, the platform stops, and responders' own tools are unaffected (spec Section 8.2).
- The gateway contract tests use a programmable fake MCP server and make no model calls.

## Reversibility

Low for the boundary itself, which is the platform's core promise. High for individual policy fields.

## Revisit when

- MCP gains native two-phase or transactional semantics.
- The gateway's latency becomes a material share of round time.

## History

- **2026-10-12:** accepted with the spec (spec Section 16).
