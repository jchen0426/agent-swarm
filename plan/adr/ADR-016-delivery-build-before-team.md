# ADR-016 Delivery: build Phase 0 before a team adopts the platform

| | |
|---|---|
| Status | Accepted. Decided by the author on 2026-10-14; narrowed on 2026-11-01. |
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

- **Narrowed on 2026-11-01.** The core of Phase 0 is built before a team: Plans 7b-1, 7b-2, 9a, 7c-1, 8a and 10a, with Plan 3c, which runs the retrospective check through the gateways so that a team can run it on its own data. The onboarding build, Plans 7c-2, 8b, 9b and 10b, is built once a team adopts the platform, before its measurement window, from the team's answers and with its content; an assumption below holds for those plans only until the team answers. The author looks for a team from 2026-11-01, in parallel with the core.
- **Assumptions.** Where the build needs an answer only a team can give, it takes the assumption in the table below. Each sits behind a port that spec Section 2.2 already names, so a team's different answer is a new adapter, not a redesign. The plan that relies on an assumption states it again, with what a different answer would cost.
- **What does not change.** Nothing built counts toward the gate (spec Section 1.4). A team's census, its retrospective check and the commitments of spec Section 14.3 still come before its measurement window, and the census can still stop or reshape its deployment (spec Section 14.1). Phase 1 still waits for the gate, and Plan 11 is still built during the first measurement window.

| Question | What it decides | What the build assumes | Plan |
|---|---|---|---|
| 2 | Milestones, incident ids and the recorded root cause | The incident platform is read through a registered MCP server that only the platform holds, as the example profile's `incidents` server is, and it is polled | 7c-2, 8b |
| 3 | The alerting source's payload and authentication | A webhook that carries a group of alerts, each with its own identity, status, start time and end time, mapped by configuration as in the example profile's Section 4.4; the authentication schemes of spec Section 3.6 | 7c-1 |
| 4 | Runbooks as skills | Skills are released from a Git repository (spec Section 3.2) | 4 |
| 5 | The read servers | Any MCP server registered with tool policy. The bench's read servers and programmable fakes stand in for a team's. | 5 |
| 6 | Production logs sent to the model vendor | One vendor behind the LLM gateway (ADR-007); redaction as spec Section 9.4 requires, tested with the example profile's rules | 5, 6, 3c |
| 7 | Hiding agent output at random | Built as spec Section 13.1 designs it. If the first team refuses, the measurement design is revisited before its window. | 8a for the arm, 8b |
| 8 | The co-signer and the raters | Built as spec Sections 13.4 and 14.3 require; nobody is needed to build it | 8b |
| 10 | Identity sources | The catalog is a registered MCP server whose fields are registered as an identity source, as in the example profile | 7c-2 |
| 11 | Review rules of the configuration repository | The configuration source is a Git repository. Its host's review rules are checked when a team onboards, not built. | 4 |
| 12 | Sign-in, roles and data entitlements | OpenID Connect with a groups claim; every responder is entitled to all data in scope | 9a |
| 13 | Message channels and phone access | A generic outbound webhook channel, with a fallback channel of the same kind; the web interface is built phone-first (spec Section 10.4) | 9a for the interface, 9b for the channels |
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

- **The cheapest exit is gone.** The census, which can stop or reshape a deployment, comes after the core, about 29.5–37 person-weeks of build in all (Plans 4–7d as executed, the core and Plan 3c), instead of before it, unless a team adopts the platform while the core is built.
- **Assumptions can be wrong.** Each wrong one is rework: at best a new adapter, at worst a changed design in the plan that relied on it. Since 2026-11-01 the onboarding build waits for a team's answers, so fewer plans rest on assumptions.
- **Evidence of value comes late.** Re-estimated on 2026-11-01, Phase 0 is about 37–48 person-weeks: about 12.5–16 months for one person on spec Section 14.2's yardstick, against the 6.5–8.5 months first estimated. AI agents build it, and in calendar time the record ran several times faster than that yardstick, so the yardstick sizes the work rather than dates it; the core may be executed by about December 2026 to January 2027 (a guess). A team's onboarding, its retrospective check, the onboarding build and a measurement window of 4–8 weeks follow.
- **The governance reason stays untested** until someone outside the builders owns an agent (spec Section 1.2).

## Reversibility

High for the plan; the time spent does not come back. A team that adopts the platform during the build returns the project to the approved path at once: its answers replace the assumptions, and the plans not yet written start from them.

## Revisit when

- **A team adopts the platform, or is ready to.** Return to the approved path.
- **Plan 7d is executed.** The coordinator then runs agents across rounds, the first new thing the bench can show teams. The author decides again whether to keep building or to look for a team with what exists.
- **A plan overruns its estimate by more than half,** in person-weeks, as its design sizes it or as executed. The plans after it are re-estimated too.
- **Exit.** Three months after Plan 10a is executed, or on 2027-04-30, whichever comes first, no team has adopted the platform. The author decides between stopping and re-scoping, and records the decision here.

## History

- **2026-10-14:** accepted, after the mechanism check passed.
- **2026-10-17:** the store's owner, not the platform, takes the anchors (question 14; Plan 6a).
- **2026-10-19:** Plan 7 is split in four, 7a to 7d (roadmap; Plan 7a). The checkpoint moves to after Plan 7d, the runner and `serve`, which is built second: the coordinator then runs agents across rounds with a real runner. The assumptions that Plan 7 relied on, questions 2, 3 and 10, are now Plan 7c's, which reads inbound events.
- **2026-11-01:** at the checkpoint after Plan 7d (7d-1 executed 2026-10-21, 7d-2 executed 2026-11-01), the author decided to keep building: Plans 7b, 7c and 8 to 10 follow, before a team, Plan 7b next. Put to the author at the checkpoint: looking for a team in parallel with the build, with the live gate's run of S16 through the sandboxes as a demonstration; and running the platform on a system the author operates. The revisit triggers above stand.
- **2026-11-01, re-estimate:** the revisit trigger "a plan overruns its estimate by more than half" had fired on size, and the checkpoint did not weigh it: Plan 7d, estimated at about 1.5 person-weeks, delivered what would take about 7–9 on the roadmap's yardstick, though in calendar time it ran on its estimate, in 13 days. Plan 7b's design then came to about 2.5 against 1. The author split Plan 7b into 7b-1 and 7b-2 and had Plans 7c to 10 re-estimated: about 2.5–3.5, 4.5–6, 5–7 and 3.5–5 person-weeks, against 1, 2–3, 3–4 and 1.5–2, so Phase 0 came to about 37–48 against 19–25. The author then narrowed the decision to the core of Phase 0, with Plan 3c added, and moved the second parts of Plans 7c, 8, 9 and 10 to an onboarding build; began looking for a team; and tied the exit to 2027-04-30 at the latest, since three months after Plan 10 would never come once Plan 10b waits for a team. The overrun trigger is judged in person-weeks, at design time as well as after execution, and the roadmap records each plan's size and its calendar time.
