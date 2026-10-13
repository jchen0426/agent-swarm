# ADR-001 First target: ops incident handling, not a general platform

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-10; revised 2026-10-12 and 2026-10-13 |
| Related | Spec Sections 0, 1.3, 13.3, 13.5 and 13.7; ADR-006 |

## Context

- The original scope was "any computer work done through authorized digital interfaces". Without a concrete domain, no acceptance checks, risk rules or evaluation set can be defined.
- The goal of this phase is internal value validation on a first deployment (spec Section 1.3). As of 2026-10-13 no team has adopted the platform; the first team that does is its first deployment.
- The domain facts behind this choice came from the team that first planned to deploy the platform. They stay in that team's deployment profile, outside this repository (spec Section 0).

## Options

- **A.** A general platform across many domains at once.
- **B.** Software engineering: code changes and pull requests.
- **C.** Customer support.
- **D.** Ops incident handling: alert diagnosis, mitigation suggestions and, later, controlled production changes.
- **E.** No platform: off-the-shelf tools, such as an incident platform's built-in AI features or an open-source diagnosis agent.

## Decision

D. The platform itself stays technology-agnostic (spec invariant 4), and each deployment's domain content lives in its deployment profile.

E is no longer an alternative to the platform. It has three roles:

- a candidate agent runtime (ADR-006);
- an external comparator, run as a shadow run through both gateways and scored blind like any run (spec Section 13.3);
- a read-only end state. The earlier rule "integrate an existing tool if the build is not clearly better" becomes the baseline margin of the gate and the read-only end state of spec Section 13.7.

## Rationale

- **The governance layer matters here.** Production changes are real and sometimes irreversible.
- **Acceptance is objective.** Root causes are recorded, milestones are timed, and the baseline is a deterministic rule.
- **Volume is reachable.** A team with about 5 or more in-scope incidents a week reaches the [20]-incident gate sample within the [8-week] window at the [50%] invisible share (spec Section 13.5).
- **Existing processes stay.** Second-person approval stays where people already practice it.

## Rejected options

- **A:** no acceptance checks, risk rules or evaluation set can be defined, and the team cannot afford it.
- **B:** code hosts and CI already provide most of this governance, so the platform's difference would be hard to test. It is a good second domain.
- **C:** acceptance is subjective; customer messages are the riskiest channel for injected instructions; and it needs external customers' data.
- **E as the end state:** it cannot test the platform's governance hypothesis. It stays as runtime candidate, comparator and read-only end state.

## Consequences

- Each deployment brings its own skills, read servers and tool policies for the ops domain, in its deployment profile.
- Replacing a domain technology, such as a deployment tool or a code host, changes only a deployment profile (spec Section 0).
- The write path is designed only after the Phase 0 gate (spec Section 14.1).
- Generality across domains is not tested in this phase.

## Reversibility

Medium. The platform core (configuration, gateways, blackboard, measurement) is domain-agnostic. A new domain needs its own skills, read servers and tool policies, estimated on 2026-10-10 at [3–6] person-weeks per domain.

## Revisit when

- The Phase 0 gate ends in "stop" or in "integrate an off-the-shelf tool" (spec Section 13.7). The author checks at the end of Phase 0.
- A second domain, or a design partner with a clear user, appears.

## History

- **2026-10-10:** accepted. Revised twice that day: the first write channel became pull requests on the deployment repository, and the goal of the phase changed from product validation to internal value validation.
- **2026-10-12:** rewritten in English for spec Section 16. The technology facts moved to the companion document, and candidate E became a runtime candidate, a comparator and the read-only end state.
- **2026-10-13:** the companion document left this repository with the organization-specific facts in it, and the volume rationale became a criterion any team can check. No team has adopted the platform yet.
