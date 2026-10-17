# ADR-016 Delivery: build Phase 0 before a team adopts the platform

| | |
|---|---|
| Status | Accepted. Decided by the author on 2026-10-14. |
| Date | 2026-10-14 |
| Related | Spec Sections 1.2, 1.3, 1.4, 2.2 and 14; implementation roadmap, Gate A; example deployment profile, Section 3 |

## Context

- The approved spec made the Phase 0 build wait for a team that adopts the platform and makes the commitments of spec Section 14.3. Until then, only the spike and the mechanism check were to run (spec Sections 1.3 and 14.2).
- Both have run, and the mechanism check passed on 2026-10-14 (roadmap Plan 3b). As of 2026-10-14 no team has adopted the platform.
- Much of Phase 0's design depends on answers only a team can give. Fifteen of the sixteen onboarding questions were due before the build (example deployment profile, Section 3).

## Options

- **A.** Keep the gate. Approach teams with a demonstration built from the mechanism check's records, and build Phase 0 once a team makes the commitments.
- **B.** Build only the registry (roadmap Plan 4), the part least tied to a team, while looking for a team, and stop at a set date if none has adopted the platform.
- **C.** Build Phase 0 (roadmap Plans 4–10) before any team, then look for teams.
- **D.** If no team can be reached, re-scope the project.

## Decision

C.

- **Assumptions.** Where the build needs an answer only a team can give, it takes the assumption in the table below. Each sits behind a port that spec Section 2.2 already names, so a team's different answer is a new adapter, not a redesign. The plan that relies on an assumption states it again, with what a different answer would cost.
- **What does not change.** Nothing built counts toward the gate (spec Section 1.4). A team's census, its retrospective check and the commitments of spec Section 14.3 still come before its measurement window, and the census can still stop or reshape its deployment (spec Section 14.1). Phase 1 still waits for the gate, and Plan 11 is still built during the first measurement window.

| Question | What it decides | What the build assumes | Plan |
|---|---|---|---|
| 2 | Milestones, incident ids and the recorded root cause | The incident platform is read through a registered MCP server that only the platform holds, as the example profile's `incidents` server is, and it is polled | 7, 8 |
| 3 | The alerting source's payload and authentication | A webhook that carries a group of alerts, each with its own identity, status, start time and end time, mapped by configuration as in the example profile's Section 4.4; the authentication schemes of spec Section 3.6 | 7 |
| 4 | Runbooks as skills | Skills are released from a Git repository (spec Section 3.2) | 4 |
| 5 | The read servers | Any MCP server registered with tool policy. The bench's read servers and programmable fakes stand in for a team's. | 5 |
| 6 | Production logs sent to the model vendor | One vendor behind the LLM gateway (ADR-007); redaction as spec Section 9.4 requires, tested with the example profile's rules | 5, 6 |
| 7 | Hiding agent output at random | Built as spec Section 13.1 designs it. If the first team refuses, the measurement design is revisited before its window. | 8 |
| 8 | The co-signer and the raters | Built as spec Sections 13.4 and 14.3 require; nobody is needed to build it | 8 |
| 10 | Identity sources | The catalog is a registered MCP server whose fields are registered as an identity source, as in the example profile | 7 |
| 11 | Review rules of the configuration repository | The configuration source is a Git repository. Its host's review rules are checked when a team onboards, not built. | 4 |
| 12 | Sign-in, roles and data entitlements | OpenID Connect with a groups claim; every responder is entitled to all data in scope | 9 |
| 13 | Message channels and phone access | A generic outbound webhook channel, with a fallback channel of the same kind; the web interface is built phone-first (spec Section 10.4) | 9 |
| 14 | The store that anchors the audit chain | An object store with write-once retention, behind the audit anchor port. Its owner takes the anchors and verifies the chain, with its own credential and a database role that can only read (Plan 6a) | 6a |

Questions 1, 9, 15 and 16 need no assumption. They decide the gate, the governance reason, the estimate and Phase 1, not the build.

## Rationale

The author's call. A team is asked for its census, two raters, its consent to hidden output and an agent owner. The author expects a built platform to win those commitments more easily than a specification.

## Rejected options

- **A:** rejected by the author. It would have kept the cheapest exit: a census that can stop the project within weeks.
- **B:** rejected by the author.
- **D:** not needed while the author expects to reach teams.

## Consequences

The author accepted these costs:

- **The cheapest exit is gone.** The census, which can stop or reshape a deployment, now comes after about 18–24 person-weeks of build instead of before it.
- **Assumptions can be wrong.** Each wrong one is rework: at best a new adapter, at worst a changed design in the plan that relied on it.
- **Evidence of value comes late.** One person builds Phase 0 in about 6.5–8.5 months (spec Section 14.2). A team's onboarding, its retrospective check and a measurement window of 4–8 weeks follow.
- **The governance reason stays untested** until someone outside the builders owns an agent (spec Section 1.2).

## Reversibility

High for the plan; the time spent does not come back. A team that adopts the platform during the build returns the project to the approved path at once: its answers replace the assumptions, and the plans not yet written start from them.

## Revisit when

- **A team adopts the platform, or is ready to.** Return to the approved path.
- **Plan 7 is executed.** The coordinator then runs agents across rounds, the first new thing the bench can show teams. The author decides again whether to keep building or to look for a team with what exists.
- **A plan overruns its estimate by more than half.**
- **Exit.** Three months after Plan 10 is executed, no team has adopted the platform. The author decides between stopping and re-scoping, and records the decision here.

## History

- **2026-10-14:** accepted, after the mechanism check passed.
- **2026-10-17:** the store's owner, not the platform, takes the anchors (question 14; Plan 6a).
