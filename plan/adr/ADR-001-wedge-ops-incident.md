# ADR-001 First target: ops incident handling, not a general platform

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-10; revised 2026-10-12 |
| Related | Spec Sections 1.3, 13.3 and 13.7; companion Sections 1 and 2; ADR-006 |

## Context

- The original scope was "any computer work done through authorized digital interfaces". Without a concrete domain, no acceptance checks, risk rules or evaluation set can be defined.
- The goal of this phase is internal value validation (spec Section 1.3). The first design partner is the author's own team, in one organization. There is no second partner yet.
- The domain facts behind this choice (incident volume, the usual mitigations, how deployments and second-person approval work today) are recorded in companion Section 2.

## Options

- **A.** A general platform across many domains at once.
- **B.** Software engineering: code changes and pull requests.
- **C.** Customer support.
- **D.** Ops incident handling: alert diagnosis, mitigation suggestions and, later, controlled production changes.
- **E.** No platform: off-the-shelf tools, such as an incident platform's built-in AI features or an open-source diagnosis agent.

## Decision

D. The platform itself stays technology-agnostic (spec invariant 4), and the first deployment's domain content lives in the companion document.

E is no longer an alternative to the platform. It has three roles:

- a candidate agent runtime (ADR-006);
- an external comparator, run as a shadow run through both gateways and scored blind like any run (spec Section 13.3);
- a read-only end state. The earlier rule "integrate an existing tool if the build is not clearly better" becomes the baseline margin of the gate and the read-only end state of spec Section 13.7.

## Rationale

- **The governance layer matters here.** Production changes are real and sometimes irreversible.
- **Acceptance is objective.** Root causes are recorded, milestones are timed, and the baseline is a deterministic rule.
- **There is enough volume.** More than 10 incidents a week need human handling, enough for a gate within a measurement window of at most 8 weeks (companion Section 2).
- **Existing processes stay.** Second-person approval stays where people already practice it.

## Rejected options

- **A:** no acceptance checks, risk rules or evaluation set can be defined, and the team cannot afford it.
- **B:** code hosts and CI already provide most of this governance, so the platform's difference would be hard to test. It is a good second domain.
- **C:** acceptance is subjective; customer messages are the riskiest channel for injected instructions; and it needs external customers' data.
- **E as the end state:** it cannot test the platform's governance hypothesis. It stays as runtime candidate, comparator and read-only end state.

## Consequences

- The first deployment builds skills, read servers and tool policies for the ops domain only (companion Section 4).
- Replacing a domain technology, such as a deployment tool or a code host, changes only the companion document (spec Section 0).
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
