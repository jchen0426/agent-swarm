# ADR-014 The configuration model

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-12 |
| Supersedes | ADR-009 |
| Related | Spec Sections 0, 3 and 12.3; ADR-003; ADR-013 |

## Context

- The author asked for general configuration: nothing domain-specific in the core, and agents that work through skills, or through MCP servers with runbooks.
- Configuration is policy here. Every switch adds combinations to validate and surface to tamper with.

## Options

- **A.** Skills declare their own effect channels and capabilities, and the platform trusts them.
- **B.** Capability packs: skills, platform-owned adapters and service-map entries (ADR-009).
- **C.** A few kinds in one reviewed Git repository, with knowledge (skills) kept apart from capability (MCP servers with tool policy).

## Decision

C. The kinds are:

- **Agent:** runtime, model, pinned skill versions, tool grants and subscriptions, with a human owner (spec Section 3.1; ADR-003).
- **Skill:** a versioned bundle of instructions, reference files and optional scripts, snapshotted from wherever people edit it, with a human owner. A skill grants no tools, and a runbook is a skill (spec Section 3.2).
- **MCP server:** endpoint, image pinned by digest, credential reference and tool policy per tool (spec Section 3.3; ADR-013).
- **Work item type:** hypothesis key fields, budgets, limits, convergence settings, milestones, auto-close, the measurement share and shadow runs (spec Section 3.5).
- **Trigger mapping:** how one inbound source becomes normalized events, with trusted fields for routing (spec Section 3.6).
- **Identity source:** a read tool whose output confers authority, registered with its owners and with audited changes (spec Section 3.6).

Rules:

- **Grants narrow, never widen.** Patterns match read tools only, write tools are granted by exact name, and no grant may resolve to a `platform` tool (spec Section 3.1).
- **Knowledge is slow to add and fast to remove.** A new skill version goes live after review, smoke scenarios and the retrospective set (spec Section 12.3). The skill's owner or a platform admin may yank a version alone, through the yank list in Git, which a database restore cannot undo (spec Section 3.2).
- **Know-how, not facts.** Facts that change are read from their systems of record, never copied into skills.
- **One source for people and agents.** Where people keep a runbook, its released snapshot should be the skill.
- **Governance.** A protected branch, schema validation, recorded content hashes, notified owners, and no self-review except for yanks. A platform admin reviews server registrations, tool policy, trigger mappings and identity sources; before Phase 1 they need two reviewers, one of them not a builder (spec Section 3.7).
- **Locking.** A work item locks the commit, the content hashes and the runtime image when it starts, and keeps them until it closes (spec Section 3).

## Rationale

- Safety stays in tested gateway code, and configuration only chooses among safe options.
- The litmus test of spec Section 0 holds: replacing a domain technology changes only a deployment profile.

## Rejected options

- **A:** whoever writes a prompt would define production safety, and self-declared metadata could lower risk levels.
- **B:** platform-owned adapters tie the core to domain tools, against invariant 4.

## Consequences

- The platform needs a configuration loader with schema validation and the loader rules of spec Sections 3.3 and 3.7.
- Skill releases need a path with a blocking scan for secrets and personal data, and the yank list.

## Reversibility

Medium. Kinds can evolve, at the cost of migrating existing files.

## Revisit when

- A second deployment needs something the kinds cannot express.
- A combination of configuration options causes an incident in production.

## History

- **2026-10-12:** accepted with the spec (spec Section 16).
