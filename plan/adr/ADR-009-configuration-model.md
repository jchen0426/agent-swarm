# ADR-009 Configuration model: capability packs

| | |
|---|---|
| Status | Superseded by ADR-014 |
| Date | 2026-10-10; superseded 2026-10-12 |
| Related | ADR-014; spec Section 3 |

## The original decision

Three layers: skills (how to do something), permissions (whether an agent may) and governed effect and observation adapters (whether the constraints still hold after acting). They were organized as capability packs:

- a skill installed on an agent;
- an adapter registered with the platform, which had to pass contract tests;
- environment settings in a service ownership map.

Deployment tooling was a property of the service, and only an observation adapter could decide that a change had taken effect.

## Why it was superseded

The platform became technology-agnostic. It registers MCP servers with tool policy instead of owning adapters, and its spec names no domain technology (spec invariant 4). The configuration model is now ADR-014: agents, skills, MCP server registrations with tool policy, work item types, trigger mappings and identity sources.

## What carries over into ADR-014

- Skills never decide safety; the gateway does.
- An agent's grants narrow tool policy and never widen it.
- All configuration is reviewed in Git on a protected branch, with schema validation, and every work item locks the configuration it started with.
- Whether a write took effect is never decided by model output (Phase 1 backlog).

## History

- **2026-10-10:** accepted, then revised the same day into capability packs.
- **2026-10-12:** superseded by ADR-014.
