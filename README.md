# agent-swarm

Status: the platform specification was approved on 2026-10-12. Plan 1 (the toolchain and the core of the blackboard) is implemented.

## Documents

- [Platform specification](plan/2026-10-11-agent-swarm-platform-spec.md) (approved)
- [First deployment: ops incident handling](plan/2026-10-11-first-deployment-ops-incidents.md) (approved; open facts still open)
- [Phase 1 write path: design backlog](plan/2026-10-11-phase-1-write-path-backlog.md) (not normative; designed after the Phase 0 gate)
- [Architecture decision records](plan/adr/README.md)
- [Implementation roadmap](plan/2026-10-12-implementation-roadmap.md) (approved)
- [Plan 1: foundation and the blackboard](plan/2026-10-12-plan-1-foundation-and-blackboard.md) (executed)

## Implementation

Go, with domain-driven design and interface-driven ports and adapters. See Section 2.2 of the platform specification.

Build and test with `make build`, `make test`, `make test-integration` (needs Docker) and `make test-e2e`.
