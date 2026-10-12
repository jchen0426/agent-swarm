# ADR-003 Agent identity: versioned configuration with a human owner

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-10; revised 2026-10-12 |
| Related | Spec Sections 1.4, 3.1, 3.2, 3.7 and 12.3; ADR-014 |

## Context

- An earlier target-state design kept a stable agent identity across changes of model and host. That causes two problems: evidence of competence no longer matches the actor that produced it, and a model cannot be held accountable.
- The author asked for agents, skills and grants to be declared in reviewed, versioned configuration (ADR-014).

## Options

- **A.** A stable agent identity, independent of its configuration.
- **B.** The agent is an immutable, versioned configuration. Its name points to the current version, and a human owner is required.
- **C.** No identity: each run only records the configuration it used.

## Decision

B.

- **Version.** An agent's version is the content hash of its file, the skill versions it pins and the tool set its patterns resolve to (spec Section 3.1). A newly registered tool that one of its patterns matches is therefore an agent change.
- **Name.** The name points to the current version. Moving back is a reviewed change that points the name at the earlier version.
- **Owner.** Required, and a human. The owner answers for the agent, as a skill's owner answers for the skill (spec Section 3.2).
- **Evidence.** Every work item locks its configuration when it starts (spec Section 3), so results can be listed per agent and skill version (spec Sections 12.3 and 13.8). A change to any component is a new version.
- **Independence.** Agents that share a model family, a prompt lineage or one retrieval context are not independent approvers. Phase 0 has no approvals; Phase 1 inherits the rule.
- **Yank list (added 2026-10-12).** A skill's owner or a platform admin may withdraw a skill version by adding it to the yank list in the configuration repository (spec Section 3.2). Agents that pin a yanked version are not invited to new items until their owners pin another version. Because the list lives in Git, a database restore cannot undo a yank.
- **Release record (added 2026-10-12).** The record of candidate and current versions, and the probation runs the coordinator generates, are built during the first measurement window (spec Section 12.3).

## Rationale

- Behavior follows configuration, so identity should follow configuration too.
- Rollback, audit and invalidating old evidence then follow naturally.
- Accountability lands on a person.

## Rejected options

- **A:** evidence of competence gets distorted, and it imports personnel processes that mean nothing for agents.
- **C:** people get no stable name, and nothing says which version is current.

## Consequences

- An agent has no memory or knowledge space of its own. Knowledge changes only through reviewed skill versions (spec Section 1.4).
- Handing an agent to another team is a change of owner.

## Reversibility

High. Names stay stable, and changes concentrate in configuration and evidence records.

## Revisit when

- A compliance requirement needs history accumulated across configuration versions.
- More than two versions of one agent must run at the same time, for a gradual rollout.

## History

- **2026-10-10:** accepted.
- **2026-10-12:** rewritten in English; the yank list and the release record added (spec Section 16).
