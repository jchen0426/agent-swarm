# agent-swarm

Status: the platform specification was approved on 2026-10-12. Plan 1 (the toolchain and the core of the blackboard) is implemented. Plan 2 (the runtime spike) chose the agent runtime (ADR-006), and live runs confirmed it. Plan 3a built the mechanism check's data and scorer; Plan 3b built the retrospective check command, and the mechanism check passed on 2026-10-14. On 2026-10-14 the author decided to build the rest of Phase 0 before a team adopts the platform (ADR-016); Plan 4 built the registry; Plan 5, the MCP gateway, is next.

## Documents

- [Platform specification](plan/2026-10-11-agent-swarm-platform-spec.md) (approved)
- [Example deployment profile](plan/2026-10-13-example-deployment-profile.md) (fictional; each team keeps its real profile outside this repository)
- [Phase 1 write path: design backlog](plan/2026-10-11-phase-1-write-path-backlog.md) (not normative; designed after the Phase 0 gate)
- [Architecture decision records](plan/adr/README.md)
- [Implementation roadmap](plan/2026-10-12-implementation-roadmap.md) (approved)
- [Plan 1: foundation and the blackboard](plan/2026-10-12-plan-1-foundation-and-blackboard.md) (executed)
- [Plan 2: runtime spike](plan/2026-10-13-plan-2-runtime-spike.md) (executed)
- [Plan 3a: mechanism check data and scoring](plan/2026-10-13-plan-3a-mechanism-check-data.md) (executed)
- [Plan 3b: the mechanism check's agent runs](plan/2026-10-14-plan-3b-mechanism-check-runs.md) (executed; the mechanism check passed)
- [Plan 4: the registry](plan/2026-10-14-plan-4-registry.md) (executed)

## Implementation

Go, with domain-driven design and interface-driven ports and adapters. See Section 2.2 of the platform specification.

Build and test with `make build`, `make test`, `make test-integration` (needs Docker) and `make test-e2e`.

`agentswarm registry validate --dir DIR` checks a configuration repository against every loader rule; `examples/config/` is a fictional team's repository. `agentswarm registry hash-tools --endpoint URL` prints each of a server's tools with the whole definition that a server registration pins, and its schema hash.
