# ADR-008 Deployment: one deployment per organization

| | |
|---|---|
| Status | Accepted for this phase. The product form is proposed. |
| Date | 2026-10-10; revised 2026-10-12 and 2026-10-13 |
| Related | Spec Sections 0, 1.4 and 2.1 |

## Context

- As of 2026-10-13 no team has adopted the platform; the first team that does is its first deployment.
- A product that sits in front of production systems is usually expected to be self-hosted, or at least single-tenant.

## Options

- **A.** Multi-tenant software as a service.
- **B.** One deployment per customer, self-hosted or dedicated.
- **C.** One self-run deployment for a single organization (this phase).

## Decision

- **This phase:** C.
- **Product form:** B is preferred (proposed).
- **`tenant_id`:** records carry it in both forms. It costs little now and avoids a migration later.
- **Deferred until A is chosen:** retrieval isolation between tenants, per-tenant resource pools and tenant isolation tests.

## Rationale

Isolation at the deployment level removes a whole class of cross-tenant risks.

The earlier rationale "the platform holds no cluster write access" was dropped on 2026-10-12. Whether a deployment's MCP servers hold write credentials is now that deployment's choice, made in its deployment profile. The platform's own boundary is the MCP gateway and invariant 7.

## Rejected options

- **A:** much isolation work with no users yet, and lower acceptance for a product that touches production.

## Consequences

- Installation, upgrades and configuration distribution are designed per customer, but this phase supports one deployment.
- If A is ever chosen, the threat model is reviewed again with a malicious tenant as an attacker.

## Reversibility

Medium. `tenant_id` is kept, but isolation would have to be built.

## Revisit when

- A second design partner or customer appears.
- A customer asks for software as a service.

## History

- **2026-10-10:** accepted for this phase; the product form proposed.
- **2026-10-12:** rewritten in English. The "no cluster write access" rationale dropped (spec Section 16).
- **2026-10-13:** the design-partner fact updated, and references moved from the companion document to deployment profiles (spec Section 0).
