# Architecture Decision Records

| | |
|---|---|
| Governing spec | `../2026-10-11-agent-swarm-platform-spec.md` (normative) |
| Deployment profile | Kept by each team outside this repository; `../2026-10-13-example-deployment-profile.md` is a fictional example |
| Phase 1 draft | `../2026-10-11-phase-1-write-path-backlog.md` (not normative) |
| Language | English. The records written on 2026-10-10 stay outside this repository and are not translated (spec Section 16). |

| ADR | Decision | Status |
|---|---|---|
| [001](ADR-001-wedge-ops-incident.md) | First target: ops incident handling, not a general platform | Accepted |
| [002](ADR-002-orchestration-single-agent-default.md) | Orchestration: a single agent by default | Superseded by ADR-012 |
| [003](ADR-003-agent-identity-versioned-config.md) | Agent identity is versioned configuration with a human owner | Accepted |
| [004](ADR-004-durable-execution-postgres.md) | Postgres as the single source of truth, with a Postgres-native task library | Accepted; River proposed |
| [005](ADR-005-tool-protocol-mcp-effect-gateway.md) | MCP plus a registry of effect adapters | Superseded by ADR-013 |
| [006](ADR-006-agent-runtime-sdk-sandbox.md) | Agent runtime, chosen by a spike | Proposed until the spike |
| [007](ADR-007-model-vendor-single-gateway.md) | One model vendor behind the LLM gateway | Accepted; building the gateway proposed |
| [008](ADR-008-deployment-single-org.md) | One deployment per organization | Accepted for this phase |
| [009](ADR-009-configuration-model.md) | Capability packs | Superseded by ADR-014 |
| [010](ADR-010-approval-model-draft-pr-required-check.md) | Draft pull request plus a required check | Moved out of the platform layer |
| [011](ADR-011-measurement-randomized-preregistered.md) | Measurement: randomized by incident, preregistered, scored blind | Accepted |
| [012](ADR-012-multi-agent-blackboard.md) | Several agents on a structured blackboard | Accepted |
| [013](ADR-013-mcp-gateway-policy-enforcement.md) | The MCP gateway as the single policy enforcement point | Accepted |
| [014](ADR-014-configuration-registry.md) | The configuration model | Accepted |
| [015](ADR-015-go-ddd-ports-adapters.md) | Go, domain-driven design, ports and adapters; CEL for conditions | Accepted; CEL proposed |

## Statuses

- **Accepted:** the decision holds. Details may still change through review.
- **Proposed:** waiting for the author's confirmation or for a spike.
- **Superseded:** replaced by the named ADR, and kept for its history.
- **Moved out of the platform layer:** the decision now belongs to a deployment, not to the platform.

## Template

Each ADR has: context, options, decision, rationale, rejected options, consequences, reversibility, revisit triggers and history.

## Open questions for later ADRs

- **Policy engine, for a product form.** Policy rules stay inside the platform, behind a clean boundary between decision and enforcement (spec Section 1.4). If a product form needs an external engine, compare OPA, Cedar and the platform's own rules then.
- **Evidence storage, for a product form.** Content-addressed deduplication, per-subject encryption and crypto-shredding are deferred. This phase uses encrypted storage and the retention classes of spec Section 9.4.
