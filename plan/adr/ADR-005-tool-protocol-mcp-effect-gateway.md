# ADR-005 Tool access: MCP plus a registry of effect adapters

| | |
|---|---|
| Status | Superseded by ADR-013 |
| Date | 2026-10-10; superseded 2026-10-12 |
| Related | ADR-013; spec Sections 3.3, 5.3 and 6 |

## The original decision

- Agents see every tool through MCP.
- Read tools may come from existing MCP servers, behind a read proxy that records, filters, redacts and controls egress.
- Write tools are effect adapters registered with the platform. A call proposes an action and does not execute it. Adapters carry two-phase semantics (propose, then execute) and pass contract tests, and the platform maintains their metadata.

## Why it was superseded

The platform no longer owns effect adapters. Every external system is reached through a registered MCP server, and the platform's MCP gateway is the single place where tool policy is enforced (ADR-013). Owning adapters would have tied the platform to domain technology, against spec invariant 4. Write semantics beyond record-only proposals are drafted in the Phase 1 backlog.

## What carries over into ADR-013

- Server annotations, such as read-only or destructive hints, are never trusted; the platform sets each tool's effect.
- Each tool is pinned by a hash of its name, description and schemas. A changed hash hides the tool until it is reviewed again.
- Write arguments are limited to candidate values that the platform reads itself.
- Tool descriptions and outputs are untrusted input.
- The LLM gateway refuses tool types that the model vendor executes (spec Section 9.3).

## History

- **2026-10-10:** proposed.
- **2026-10-12:** superseded by ADR-013.
