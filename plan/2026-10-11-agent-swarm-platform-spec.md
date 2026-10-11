# Agent Swarm Platform Specification

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-11 |
| Normative | Yes. Supersedes the earlier v1 implementation spec. |
| Companion | `2026-10-11-first-deployment-ops-incidents.md` holds all domain content for the first deployment. |
| Decisions | `adr/`. Section 16 lists the ADR changes this spec requires. |

## 0. Conventions

- **MUST**, **MUST NOT**, **SHOULD** and **MAY** are used as in RFC 2119.
- **Defaults.** Bracketed values in this spec, such as [15 min], are defaults that a deployment may change. Section 15 lists every one. Bracketed values in the companion document are first-deployment settings.
- **Examples.** They use neutral names such as `release-manager` and `get_release`. Real servers, tools and policies belong in the companion document.
- **Litmus test.** Replacing any domain technology, such as a deployment tool, a code host or a metrics system, changes the companion document and never this one.

## 1. Purpose and scope

### 1.1 What the platform is

Agent Swarm is a governance system for AI agents, not another agent framework. It decides:

- who each agent is and which human owns it;
- what each agent knows (skills) and what it may touch (MCP tools);
- how agents work together on one work item;
- which actions need human approval, and how every action is recorded.

Agents work through two kinds of assets:

| Asset | What it is | Credentials | Grants tools |
|---|---|---|---|
| Skill | Knowledge: instructions, reference files and optional scripts. Scripts run only inside the agent's sandbox. A runbook is a skill. | None | No |
| MCP server | Capability: the only way to read from or change an external system | Held behind the MCP gateway | Only through tool policy |

Every external side effect is therefore an MCP tool call, and every MCP tool call passes through the platform's MCP gateway.

### 1.2 Why several agents

Several agents work each item. The reason is governance, not diagnostic accuracy (ADR-012):

- each agent has a human owner, who can change it without going through the platform builders;
- each agent gets least privilege. This matters most for write tools; read grants follow each agent's job and the data its owner may see.

So this phase does not compare a team with a single agent. It does compare collaboration with independent work, because the number of rounds is a configuration choice (Section 13.3).

The governance reason holds only if teams other than the builders own agents. Section 14.3 therefore makes a named owner outside the builders a precondition of the Phase 0 build.

### 1.3 Goal of this phase

The goal is internal value validation on the first deployment described in the companion document:

- **Phase 0:** do agents reach correct conclusions often enough, clearly better than a simple baseline, safely, and at an acceptable cost (Section 13)?
- **Phase 1, if built:** does acting through the platform shorten the time to act, without more failed or reversed actions (Section 13.9)?

### 1.4 Out of scope

- **Multi-tenancy.** ADR-008 keeps one deployment per organization; records still carry a `tenant_id`.
- **Writes without human approval.**
- **Autonomous learning.** Agents keep no memory across work items and never change skills or configuration themselves. Knowledge changes only through reviewed skill versions (Section 3.2).
- **A synthetic evaluation bench,** meaning fault injection into non-production environments. A retrospective set built from past incidents is allowed for tuning (Section 12.3), never for the gate.
- **An external policy engine.** Policy rules stay inside the platform, behind a clean boundary between decision and enforcement.
- **Comparing a team with a single agent** (Section 1.2).

## 2. Architecture

```
 trigger (webhook) --> [coordinator] --creates--> [blackboard] <-- humans (web UI)
                           | invites, budgets          ^
                           v                           | blackboard tools
  [agent sandbox] x N -----MCP--> [MCP gateway] -------+
    agent runtime + skills          | per-agent tool policy
    no credentials                  | write -> proposal -> approval inbox -> execute
         |                          +--> registered MCP servers (hold credentials)
         +--> [LLM gateway]         +--> [audit and trajectory store]
```

| Component | Responsibility |
|---|---|
| Coordinator | A deterministic program that never calls a model. It opens work items, links them to incidents, creates runs and their blackboards, invites agents by subscription and paces rounds. It also enforces budgets, convergence and stop rules, assigns ADR-011 arms and starts shadow runs. One instance is active at a time (Section 8.2). |
| Blackboard | One per run, append-only. Its entries are typed, and each has an author and references (Section 4.2). |
| Agent runner | One disposable sandbox per agent session. It loads the agent's skills and runs its agent runtime, which is swappable (default per ADR-006). Egress goes only to the MCP gateway and the LLM gateway, and the sandbox holds no credentials. |
| MCP gateway | The only MCP endpoint agents see, and the only place tool policy is enforced (Section 6). |
| LLM gateway | The only route to models (ADR-007). It forwards only allowlisted requests (Section 9.3), enforces token budgets per run and attributes cost. |
| Human interface | The platform's web UI: items, live board, suggestions, approval inbox, rulings and administration (Section 10). Notifications go out through configurable channels. |
| Registry and configuration | Agents, skills, MCP server registrations with tool policies, approval policies, work item types and trigger mappings, kept in a Git repository that requires review, plus the release record of version states (Sections 3 and 12.3). |
| Audit and trajectory store | An append-only record of tool calls with redacted outputs, board entries, approvals, rulings and model-call metadata. Audit records also go to a sink outside the restorable database (Section 9.4). |
| Database | Postgres, the single source of truth for all other state (ADR-004). |

Each agent session gets two short-lived tokens, one for each gateway. Both are scoped to its run and agent, and both expire when the run stops.

### 2.1 Invariants

1. Agent sandboxes hold no credentials for any external system; their only secrets are the two short-lived gateway tokens (Section 2). Every tool call passes through the MCP gateway, the only place tool policy is enforced.
2. A human approves every write, and the approval covers only that exact call (Section 4.4). Approval policy decides who may approve.
3. No agent can stop, extend or close a run. Runs stop by the coordinator's rules, a human ruling or the kill switch.
4. This specification names no domain technology (Section 0).
5. The platform is never on a trigger's own notification path. When the platform fails, people fall back to their existing process.
6. A shadow run never sends a write to an MCP server, and no human sees its output before its release (Section 7).
7. No write tool can change the platform's own configuration or skill sources. Registration review verifies this.

### 2.2 Implementation constraints

- **Language.** The platform is written in Go. That covers the coordinator, the MCP gateway, runner control, LLM gateway integration and the human interface backend. The agent runtime inside a sandbox is swappable and may use another language (ADR-006).
- **Domain-driven design.** Code is organized by bounded context, and each context uses the vocabulary of this spec. The proposed contexts below are to be confirmed in the implementation plan:

| Context | Holds | Kind |
|---|---|---|
| Work | Incidents, work items, runs, blackboards, rounds, convergence and stop rules | Core |
| Actions | Tool policy decisions, proposals, approvals, target locks, effects and checks, execution outcomes | Core |
| Registry | Configuration objects, their versions and the release record (Section 3) | Supporting |
| Measurement | Arms, shadow-run scoring, rating packets, metrics | Supporting |
| Access | Humans, roles, sign-in | Generic |
| Audit | Append-only records | Generic |

- **Interface-driven.** The code follows ports and adapters:
  - Domain and application code depend only on Go interfaces they declare themselves (ports). Infrastructure packages implement them (adapters).
  - Dependencies point inward: infrastructure depends on application, and application on domain. Domain packages import no database, network or framework packages.
  - Ports exist only at real boundaries: persistence, job queue, configuration source, identity provider (sign-in and groups), MCP servers, model gateway, sandboxes, notifications, clock, identifiers, condition evaluation and secrets. Each interface is small and declared by its consumer.
  - Wiring happens in `cmd/`.
- **Testing.** The fakes in Section 12 (fake MCP servers, scripted agents, a fake clock) are adapters of these same ports. The deterministic test layers therefore need no network or model calls.

## 3. Configuration

All configuration content lives in one Git repository, and every change requires review (Section 3.7). The platform loads a reviewed revision. Which version of each agent and skill is current, candidate, corrected or yanked lives in the release record (Section 12.3).

When a work item starts, it locks its configuration: the commit, the content hashes, the release-record version and the runtime image version. It keeps that lock until it closes.

### 3.1 Agent

```yaml
kind: Agent
metadata:
  name: release-investigator
  owner: alice@example.com              # required; a human
spec:
  runtime: "<chosen in ADR-006>"         # swappable
  model: "<model id>"                    # behind the LLM gateway (ADR-007)
  skills: [release-diagnosis@1.2.0]      # knowledge only; exact versions (Section 3.2)
  tools: ["release-manager.get_*", "release-manager.rollback_release", "metrics.query_*"]
  subscribe:
    workItemTypes: [incident]
    taskTags: [release]                  # used by joins, after Phase 0 (Section 5.2)
```

- **Version.** An agent's version is the content hash of its file, the skill versions it pins and the tool set its patterns resolve to (ADR-003). A newly registered tool that a pattern would match is therefore an agent change: its owner reviews it, and it goes through probation. The agent's name points to the current version.
- **Grants.** `tools` narrows tool policy and never widens it. A pattern matches server and tool separately, and `*` matches only within a tool name. Patterns match read tools only; write tools are granted by exact name.
- **Challengers.** An agent MAY also subscribe as a challenger: `subscribe.challenges` lists the work item types in which it is invited only to challenge a converged hypothesis (Section 5.6).

### 3.2 Skill

- **Contents.** A skill is a versioned bundle of instructions, reference files and optional scripts. A version is a snapshot of its source at a pinned commit, wherever people edit that source.
- **Owner.** Every skill has a human owner, who answers for its correctness, as an agent's owner does for the agent.
- **Scripts.** They run only inside the agent's sandbox, which can reach nothing but the two gateways. Their dependencies are part of the runtime image.
- **No tools.** A skill grants no tools. A runbook for an MCP server is a skill.
- **One source for people and agents.** Where people already keep a runbook for a system, its released snapshot SHOULD be the agents' skill. People and agents then share one source, and agents receive its changes through releases. A separate knowledge base for agents would drift away from the people's.
- **Know-how, not facts.** Skills hold durable know-how. Facts that change over time, such as known ongoing problems or change freezes, are read from their systems of record through read tools and never copied into skills.

**Versions**
- **Pins.** An agent pins exact skill versions (`release-diagnosis@1.2.0`).
- **Release.** A new version becomes current only after probation (Section 12.3). Each agent that should use it moves its pin through its own owner.
- **Corrections.** Outside a measurement window, a version that only corrects an existing one MAY go live after its smoke scenarios pass and its owner and a platform admin approve, without shadow probation. Agents pinned to the corrected version move to the correction in the same release, and their owners are notified.
- **Yank.** The skill's owner or a platform admin MAY yank a version. A yank removes knowledge; it never restores older content:
  - agents that pin the yanked version are not invited to items that start afterwards, until their owners pin another version;
  - items already started keep their locked version, and each gets a notice;
  - during a measurement window, a yank applies to visible-arm items only, so the invisible arm keeps the preregistered configuration;
  - the owners of the affected agents are notified.
- **Why fast removal is safe.** Safety is enforced by the gateway, never by skills. A yank can leave an item with fewer agents, but it cannot enable a write or bring back instructions nobody reviewed. So knowledge is slow to add and fast to remove.
- **Later.** Patch lines, which would let an agent accept a skill's patch releases automatically, come after the gate (Section 14.1).

### 3.3 MCP server registration and tool policy

```yaml
kind: McpServer
metadata: { name: release-manager, owner: platform-team }
spec:
  endpoint: https://release-mcp.internal
  credentialRef: secret://mcp/release-manager    # never reaches a sandbox
  writeIdentity: release-bot                     # the identity the server's writes carry
  tools:                                         # deny by default: unlisted tools are hidden
    get_release:
      schemaHash: "sha256:<recorded at review>"  # name, description, input and output schemas
      effect: read
      redact: [default-secrets]
      partialWhen: "result.truncated == true"
    list_recent_changes:
      schemaHash: "sha256:<recorded at review>"
      effect: read
    rollback_release:
      schemaHash: "sha256:<recorded at review>"
      effect: write
      target: "{args.service}"                   # every argument used here needs candidates
      scope: { service: hypothesis.service }     # must act on the cited hypothesis's service
      approval: second-human
      timeout: 120s
      operationId: request_id                    # filled by the gateway; hidden from agents
      idempotent: true                           # reviewed: the server deduplicates on request_id
      errorMeansNoEffect: false
      candidates:
        service: { tool: catalog.list_services, field: "$[*].name" }
        change_id: { tool: list_recent_changes, args: { service: "{args.service}" }, field: "$[*].id" }
      fingerprint: { tool: get_release, args: { service: "{args.service}" }, field: "$.current" }
      checks:
        - { name: healthy, tool: get_release, args: { service: "{args.service}" }, condition: "result.status == 'healthy'", after: 120s }
      reconcile: { tool: get_release, args: { service: "{args.service}" }, condition: "result.lastRequestId == operationId && result.lastRequestBy == writeIdentity && result.lastRequestDone" }
```

| Field | Applies to | Meaning |
|---|---|---|
| `schemaHash` | all | Hash of the tool's name, description, input schema and declared output schema, recorded at review (server pinning, below). |
| `effect` | all | `read` or `write`, set by the platform. Server annotations, such as read-only or destructive hints, are never trusted. |
| `audience` | all | `agents` (default) or `platform`. A `platform` tool serves only the platform's own reads, such as routing, candidates, milestones and identity. It is never listed to agents, and the loader refuses any agent grant that resolves to it. |
| `redact` | all | Named redaction rules. They apply to the output, and to errors, before any agent sees them and before they are stored. |
| `partialWhen` | read | A condition on the output. When it holds, the output becomes a read error, so partial or truncated data never becomes evidence. |
| `target` | write | Template for the lock key. Every argument it uses MUST have candidates, and the key is built from the matched candidate value, so one real target has one key. Each target has at most one unfinished proposal, across all work items (Section 4.4). |
| `scope` | write | Bindings from arguments to the cited hypothesis's key or to the item's trusted routing values. The gateway refuses a call outside them. |
| `approval` | write | Name of an approval policy. |
| `timeout` | write | Execution timeout. On timeout the outcome is `unknown`. |
| `operationId` | write | Name of an argument that the gateway fills with the proposal's operation id (Section 6.4). The argument is removed from the schema agents see, so no agent can set it. |
| `idempotent` | write | A reviewed claim that repeating the call with the same arguments, operation id included, has the same effect. It allows one automatic retry after `unknown`. A server that deduplicates on the operation id makes the claim easy to verify. |
| `errorMeansNoEffect` | write | A reviewed claim that a tool execution error means nothing changed. On a first attempt such errors become `failed` instead of `unknown` (Section 6.4). |
| `candidates` | write | For each listed argument, a read call whose output is the set of allowed values. The gateway rejects values outside the set, and refuses the call if the read fails or returns nothing. |
| `fingerprint` | write | A read call made when the proposal is created and again before every execution attempt. If the field differs, the proposal is `voided`. If the read fails or the field is absent, the proposal is refused at creation and voided at execution. |
| `takesEffect` | write | For a write whose effect starts later through someone else's action, such as a merge: a read plus a condition that holds once the effect has started, and a deadline (Section 5.5). Its result is available to checks as `effect`. |
| `checks` | write | Named post-effect checks. Each is a read plus a condition, with an `after` delay from the start of the effect, an optional `window` to poll within, and an optional `unverifiedWhen` condition (Section 5.5). |
| `reconcile` | write | A read plus a condition, run while an outcome is `unknown` (Section 6.4). The condition MUST compare the record's operation id with `operationId`, MUST check that the record carries the server's `writeIdentity`, and MUST test that the operation completed, not merely that it started. |

The gateway makes candidate, fingerprint, effect, check and reconcile reads under the platform's own identity, and audits them like any other call.

**Conditions.** All conditions use one sandboxed, side-effect-free expression language, chosen in ADR-015. Values from arguments, the hypothesis, the effect read, the operation id and the write identity are bound as typed variables, never spliced into condition text.

**Loader rules.** The loader refuses a `target` that uses an argument without candidates, and a `reconcile` condition that does not reference both `operationId` and `writeIdentity`.

**Server pinning.** The gateway hashes each listed tool's name, description, input schema and declared output schema. If a hash differs from the registered `schemaHash`:

- that tool is hidden at once, including from running work items, until the registration is reviewed again;
- its proposals that are not yet executing are voided (Section 4.4);
- the registration's owner is notified.

Tools that a server adds are hidden anyway, because unlisted tools are denied by default.

**Partial results.** Review of a registration confirms that the server reports partial or truncated results as errors, or that tool policy declares `partialWhen` for each read tool that can return them. A partial result that looks complete is the most misleading evidence there is, because an incomplete answer reads as "nothing found".

**No self-change.** Review of a registration confirms that no write tool can reach the platform's configuration repository or skill sources (invariant 7).

### 3.4 Approval policy

```yaml
kind: ApprovalPolicy
metadata: { name: second-human }
spec:
  approver: { human: true, exclude: [principal], groups: [responders-secondary] }
  escalation: [ { after: 5m, groups: [team-leads] } ]
  expiresAfter: 60m
```

- **Groups.** Approver groups come from the identity provider's group claims. A read tool MAY supply them only if it is registered as an identity source: owned by the identity owners, authenticated, and with its changes audited. The companion document says which.
- **Exclusions.** `exclude` MAY name the principal, the owner of the proposing agent, or both.
- **Escalation.** If nobody in the approver groups acts within `after`, the request also goes to the escalation groups.
- **Expiry.** `expiresAfter` counts from proposal creation. A proposal that has not started executing by then expires, whether or not it was approved (Section 4.4).
- **Related rulings.** The same policy decides who may resolve `unknown` and release a held target for a proposal that uses it (Section 4.4).

### 3.5 Work item type

```yaml
kind: WorkItemType
metadata: { name: incident }
spec:
  hypothesisKey:                               # Section 4.3
    - { name: service, values: { tool: catalog.list_services, field: "$[*].name" } }
    - { name: failureMode, values: [] }        # closed list, filled in by the deployment
  budget: { wallClock: 15m, tokens: 2000000, rounds: 6 }   # cost-weighted tokens (Section 5.6)
  round: { timeLimit: 150s }
  team: { maxAgents: 4 }
  limits: { entriesPerRound: 5, openClaims: 2, leaseRounds: 2, proposalsPerTargetPerHour: 3 }
  convergence: { minContributors: 2, minSources: 2, refutationMargin: 2 }
  challenge: { mode: off }                     # off | fork | primary (Section 5.6)
  milestones: [acknowledged, cause_identified, mitigation_started, recovered]
  autoClose: { afterTriggerResolved: 30m }
  measurement: { invisibleShare: 0.5 }          # ADR-011; fixed by preregistration
  shadowRuns:                                   # Section 7
    - name: independent
      arms: [invisible]
      override: { budget: { rounds: 1 }, round: { timeLimit: 15m } }
```

### 3.6 Trigger mapping and inbound events

The platform accepts one normalized inbound event. How each source's payload translates into it is configuration.

| Field | Meaning |
|---|---|
| `source` | A registered inbound source |
| `eventId` | The source's id for the event. If the source supplies none, the mapping derives one from trusted fields and the timestamp. |
| `instanceId` | The source's identity for one firing instance under a dedup key, such as one host or region |
| `status` | `firing` or `resolved` |
| `title`, `labels`, `payload` | Content. Untrusted unless the mapping marks a field as trusted. Redacted before storage (Section 9.4). |
| `occurredAt` | Source timestamp |
| `milestone` | Optional: a milestone name and time for an existing incident (Section 11) |
| `incidentRef` | Optional: an incident id from the deployment's incident source |

```yaml
kind: TriggerMapping
metadata: { name: alerts }
spec:
  source: alerting
  authentication: { hmacSecretRef: secret://inbound/alerting, maxAge: 5m }
  split: "$.alerts[*]"                           # one request may carry several events
  trustedFields: [labels.rule_id, labels.service, fingerprint, startsAt, status]
  workItemType: incident
  dedupKey: "{labels.service}:{labels.rule_id}"
  instanceId: "{fingerprint}"
  incident: { correlate: "{labels.service}", within: 30m }
  principal: { tool: directory.responsible_for, args: { service: "{labels.service}" }, field: "$.user" }
  responders: { tool: directory.responders_for, args: { service: "{labels.service}" }, field: "$.group" }
  fallbackPrincipal: { group: on-call-primary }
  redact: [default-secrets]
```

- **Authentication.** Inbound requests MUST be authenticated and MUST carry a signed timestamp or delivery id. Requests older than `maxAge` [5 min] are refused, so a captured request cannot be replayed later.
- **Trusted fields for ordering and milestones.** `eventId`, `instanceId`, `status`, `occurredAt` and `milestone` MUST come from trusted fields, and milestone events are accepted only from the registered milestone source.
- **Splitting.** A mapping MAY split one request into several events. The request is acknowledged only after all of them are committed, so the source's own retries cover platform failures.
- **Repeats and order.** Sources retry, so events repeat and can arrive out of order.
  - An event whose `eventId` was already seen from the same source is dropped.
  - A `status` applies only if its `occurredAt` is later than that of the last status applied for the same dedup key and instance. `occurredAt` is clamped to the receipt time plus [2 min] of skew.
  - An older event is kept on the item as trigger evidence and changes nothing.
- **Instances.** A trigger is resolved only when no instance under its dedup key is still firing.
- **Incidents.** Items belong to one incident when the mapping's correlation key matches within `within` [30 min], or when an `incidentRef` or a responder's ruling links them.
- **Routing uses trusted fields only.** This covers the work item type, the dedup key, the incident, the principal, the responders, and any hypothesis key value taken from the trigger. Untrusted fields appear as trigger evidence and never route.
- **Diagnosis-only items.** A mapping MAY declare consistency checks between trusted and untrusted fields. If a check fails, the item is diagnosis-only, and write tools are hidden from all of its runs.
- **Principal and responders.** The principal is the human on whose behalf the work item runs; the responders are the group that handles it. Both are resolved through read tools, via the gateway, under the platform's own identity. For an item opened in the UI, the principal is the person who opened it.

### 3.7 Configuration governance

| Change | Phase 0 | Before Phase 1 starts |
|---|---|---|
| Any change | Protected branch; schema validation; content hashes recorded; affected owners notified; nobody reviews their own change | Also signed commits |
| An agent | Reviewed by its owner; when the owner wrote it, by a co-owner or a platform admin | Same |
| A skill version | Reviewed the same way by the skill's owner; scripts are reviewed as code | Same |
| A correction (Section 3.2) | Smoke scenarios pass; the skill's owner and a platform admin approve | Same |
| A yank (Section 3.2) | The skill's owner or a platform admin, alone | Same |
| A promotion after probation (Section 12.3) | The owner and a platform admin, recorded in the release record | Same |
| Server registration, tool policy, approval policy, trigger mapping, identity source | Reviewed by a platform admin | Two reviewers, at least one of them not a platform builder |

The loader MUST refuse configuration that fails schema validation, references an unregistered server or tool, gives an agent a tool that tool policy does not allow, grants a write tool by pattern, or breaks a loader rule of Section 3.3. The repository's review rules enforce who must review; the companion document says how.

## 4. Runtime objects

### 4.1 Incidents, work items and runs

An **incident** groups the work items that one underlying problem causes (Section 3.6). Arms are drawn per incident, and measurement counts incidents (Section 13). Milestones and ground-truth records attach to the incident, also after its items close.

A **work item** is one unit of work, opened by a trigger or by a human. It has:

- a type, trigger source, dedup key, incident, principal, responders, locked configuration (Section 3) and `tenant_id`;
- an ADR-011 arm, `visible` or `invisible`, taken from its incident;
- one primary run and zero or more shadow runs.

A **run** is one team configuration working the item. Each run has its own blackboard and budget. In the visible arm, humans see the primary run. In the invisible arm, the primary run executes in shadow mode (Section 7).

```
queued --> investigating --> awaiting_close --> closed
               ^                   |               |
               +-------------------+               |   extend budget, or a new firing with the same dedup key
                                   ^---------------+   reopen ruling, within [24 h]
```

- **Queued.** An item waits here when the concurrency limit is reached. It locks its configuration when it starts. A responder may promote a queued item, which starts it even if that exceeds the limit by one.
- **Stop reasons.** When the primary run stops, the item moves to `awaiting_close` and records why: `converged`, `quiet`, `budget_exhausted`, `trigger_resolved` or `not_staffed` (kill switch or daily token cap).
- **Closing.** A human may close an item from any state, and may reopen a closed item within [24 h]; it returns to `awaiting_close`. An item in `awaiting_close` also closes automatically [30 min] after the later of its trigger's resolution and its most recent entry into `awaiting_close`.
- **On close.** Closing withdraws the item's proposals that are not yet executing, abandons operations still awaiting effect (Section 5.5), and stops a visible primary run. Shadow-mode runs continue to their own end (Section 7).

### 4.2 Blackboard entries

| Type | Written by | Content |
|---|---|---|
| `evidence` | agent, human, platform | A claim with provenance, and optionally a stance on a hypothesis: `supports` or `refutes` |
| `hypothesis` | agent | Key, statement and at least one evidence reference |
| `task` | agent | Title and tags; can be claimed |
| `proposal` | gateway | Created when an agent calls a write tool (Section 5.3) |
| `result` | gateway | Outcome of an executed proposal, and its effect and checks |
| `ruling` | human | See rulings, below |
| `notice` | platform | Trigger updates, failures, refused calls and stop summaries |

Entries are append-only. Each carries its author, time, run and references.

**Evidence provenance.** Each evidence entry MUST have exactly one of:

- the id of a gateway read call made by the same agent in the same run, plus a quote that appears verbatim, after whitespace normalization, in that call's stored, redacted output;
- a human author, in the visible arm only;
- a platform record: a trigger event or a check result.

The platform rejects evidence that breaks these rules. Provenance proves that the call happened and that the quoted text exists. It does not prove the interpretation, which is why approvers and raters can open the stored output.

**Tasks.** `claim_task` is atomic, so a task has one holder at a time. A claim is released if no `complete_task` arrives within [2] rounds. An agent holds at most [2] open claims.

**Entry limit.** An agent posts at most [5] new entries per round.

**Rulings.** Humans act on a board only through rulings:

| Ruling | Effect |
|---|---|
| Reject, un-reject | A rejected hypothesis cannot converge until it is un-rejected. Proposals citing it that are not yet executing are withdrawn. |
| Dismiss a refutation | The refutation no longer blocks convergence (Section 4.3). Visible arm only. |
| Extend budget | Adds an amount, by default [1 round and 5 min]. |
| Close, reopen | Section 4.1. |
| Take over | The visible primary run becomes record-only for the rest of the item. Its proposals that are not yet executing are withdrawn, its operations awaiting effect are abandoned, and agents keep recording. |
| Resolve `unknown`, release a held target | Section 4.4. |
| Record an adoption | Section 6.3. |
| Set a milestone, link items to an incident | Sections 11 and 3.6. |

- **Consequences first.** Each ruling shows its consequences before it is confirmed, for example "withdraws 2 pending proposals" or "completes convergence; the run stops after one more round".
- **Inputs.** Reject, dismiss, release and resolve require a reason; extend requires an amount.
- **Refute or reject.** Refute a hypothesis by posting evidence against it; that counts like any other evidence. Reject it when you know it is wrong without posting evidence.
- **Human support.** A human's supporting evidence counts as a contributor. If it completes convergence, the confirmation says so.

### 4.3 Hypothesis keys, convergence and the conclusion

**Keys and merging**
- **Keys.** A work item type MAY define key fields. Each field has a closed value set, either given inline or produced by a read tool when the item opens (and then locked with the configuration). Every field also accepts `other`.
- **Merging.** Hypotheses with equal keys are the same hypothesis, so the platform merges them. The later one becomes an alias, and its evidence counts for the merged hypothesis. A key containing `other` never merges.

**Terms**
- **Support** is provenance-backed evidence with stance `supports`.
- **Contributor** is an agent or a human.
- **Source** is a (server, tool) pair, or "human observation".

**Converged.** A hypothesis has converged when all of these hold:
- support from at least [2] distinct contributors and at least [2] distinct sources;
- no standing refutation;
- no human has rejected it.

**Standing refutation.** Provenance-backed evidence with stance `refutes` stands unless a responder dismissed it, or the hypothesis has at least [2] more distinct supporting contributors than distinct refuting contributors. One refutation can therefore block a two-contributor convergence, but not a hypothesis that a clear majority supports, and in the visible arm a human can overrule it.

**Score.** A hypothesis's score is its distinct supporting contributors minus its distinct refuting contributors. Ties are broken by distinct supporting sources.

**Conclusion.** A run's conclusion, used for scoring in Section 13, is chosen in this order:
1. the highest-scoring converged hypothesis;
2. if none has converged, the highest-scoring hypothesis that no human rejected;
3. if the top two are still tied, the run has no conclusion.

**As of a moment.** Because the coordinator is deterministic and the board append-only, the conclusion can be recomputed as of any moment. Section 13 scores it as of a cutoff.

### 4.4 Proposals and approvals

A proposal records:
- the tool, its schema hash and the exact arguments;
- the target key;
- the cited hypothesis;
- the policy checks and any agent check;
- the fingerprint snapshot;
- the operation id (Section 6.4) and the epoch (Section 8.4);
- the proposer.

| State | Reached from | When |
|---|---|---|
| `recorded` | (created) | Record-only mode: Phase 0, shadow runs and items taken over (Section 6.3) |
| `pending_approval` | (created) | Normal mode |
| `approved` | `pending_approval` | An approver approves |
| `rejected` | `pending_approval` | An approver rejects |
| `expired` | `pending_approval`, `approved` | The policy's expiry passed before execution started, or the kill switch was turned on |
| `withdrawn` | `pending_approval`, `approved` | The cited hypothesis was rejected, the item closed, or a responder took over |
| `voided` | `pending_approval`, `approved`; `executing` only while no request has reached the server | The fingerprint or the tool's schema hash changed, or the database was restored (Section 8.4) |
| `executing` | `approved`; `unknown` (the single retry of an idempotent tool) | Write-ahead persisted (Section 6.4) |
| `succeeded`, `failed`, `unknown` | `executing` | Outcome mapping (Section 6.4) |
| `succeeded`, `failed` | `unknown` | A reconcile condition holds (`succeeded`), or a human resolves it |

A `succeeded` proposal then has an effect (Section 5.5):

| Effect | Reached from | When |
|---|---|---|
| `pending` | `succeeded` | Tool policy declares `takesEffect` |
| `started` | `succeeded` (no `takesEffect`); `pending` | The `takesEffect` condition holds |
| `abandoned` | `pending` | Its deadline passed, the item closed, the cited hypothesis was rejected, or a responder took over |
| `confirmed` | `started` | Every policy check passed, or, where a check was unverified or there is no policy check, a human confirmed the effect |
| `held` | `started` | A policy check failed or could not run, or is waiting for a human to confirm |

- **Approval binding.** An approval binds to a hash of the tool, its schema hash, the arguments, operation id, epoch, target, checks and fingerprint snapshot. Execution uses the stored arguments only.
- **Target lock.** At most one unfinished proposal per target, across all work items. Unfinished means `pending_approval`, `approved`, `executing`, `unknown`, or `succeeded` with its effect `pending` or `started`. Recorded proposals take no lock (Section 6.3).
- **Held target.** A target whose last proposal's effect is `held` takes no new proposal until an approver releases it.
- **Refusals are visible.** When a write call is refused because its target is locked, held or rate-limited, the caller's board gets a notice naming the item that holds the target, its state and for how long. Approvers get a link to resolve or release it, and are reminded of `unknown` outcomes and held targets every [15 min].
- **Resolving `unknown` and releasing a held target.** Only a person who satisfies the proposal's approval policy, exclusions included, may do either, and the ruling shows its author. The resolve screen shows the operation id and where to look it up in the target system, the attached reconcile results, and a warning that "not effective" releases the target.
  - Effective: the state becomes `succeeded`, and its effect proceeds.
  - Not effective: the state becomes `failed`, and the target is released.
- **Rate limit.** More than [3] proposals for one target within an hour pause new proposals on that target and notify its responders.

## 5. Work item lifecycle

### 5.1 Open

1. An authenticated inbound event arrives, or a human with the right (Section 10.1) opens an item in the UI.
2. If an item that is not closed has the same dedup key, the event is appended to it as trigger evidence, and status changes apply as Section 3.6 describes. No new item is created. A `resolved` event that matches no open item is recorded and opens nothing.
3. Otherwise the coordinator creates the item. It links the item to an incident, takes the incident's arm or draws one for a new incident (Section 13.1), resolves the principal and responders, and creates the primary run and the configured shadow runs.
4. Items opened by a human are always visible and outside measurement.
5. Open-time reads (principal, responders, hypothesis-key values) are retried for up to [2 min]. If they still fail, the item opens with a notice, uses the last successful key values and the mapping's fallback principal, and is marked degraded. The preregistration says how degraded items count.
6. If [3] items are already investigating, the new item waits in `queued`. If the kill switch is on or the daily token cap has been reached, the item opens with stop reason `not_staffed`.
7. When the item starts, it locks its configuration, and agents whose subscriptions match the work item type are invited, in configuration order, up to [4] per run. Agents that pin a yanked skill version are not invited (Section 3.2).
8. In the visible arm only, one message with a link to the live board goes to the responders' channel (Section 10.3).

### 5.2 Rounds

**Pacing**
- **Rounds.** Runs advance in bulk-synchronous rounds. At the start of each round the coordinator freezes a snapshot. Each active agent receives the entries added since its previous snapshot.
- **Turns.** An agent works until it calls `end_turn` or the round time limit [150 s] passes.
- **Visibility.** Humans see each entry as soon as it is committed. Other agents see it only from the next round. Claims and target locks take effect in real time.
- **Round end.** When every active agent has ended its turn, or the time limit has passed, the coordinator works through, in order: lease expiry, convergence, stop conditions, joins, then the next round.
- **Joins** (after Phase 0, Section 14.1). When a task with a tag appears, agents subscribed to that tag join the run in the next round, up to the team limit.
- **No assignment.** Rounds set the pace; they do not assign work. Each agent chooses its own actions.
- **Sessions.** A session persists across the rounds of its run, with its context kept bounded (Section 5.6). If a session is lost, the runner starts a new one from the current snapshot.

**Blackboard tools**

The gateway provides these tools:

```
read_board(since)                        # current snapshot plus the caller's own entries
post_evidence(claim, call_id, quote, hypothesis_id?, stance?)    # stance: supports | refutes
post_hypothesis(key, statement, evidence_ids)                    # at least one evidence
post_task(title, tags)
claim_task(task_id)
complete_task(task_id, evidence_ids)
end_turn()
```

There is no hypothesis without evidence. A guess becomes a task for someone to check.

**Repeats.** Blackboard tools are idempotent, so a call repeated after a lost response does no harm:
- a post whose author, run and arguments match an existing entry returns that entry, and does not count toward the entry limit; for `post_task` this holds only while the matching task is open;
- `claim_task` on a task the caller already holds, `complete_task` on a task the caller already completed, and a repeated `end_turn` succeed without effect.

### 5.3 Proposals

Agents call write tools as their runbooks describe. The gateway removes the argument named by `operationId`, if any, and adds two arguments to every write tool's schema:
- `_hypothesis` (required): the hypothesis the action addresses;
- `_check` (optional): a read call plus a condition. Its tool MUST be a read tool in the proposer's own grant.

Both are stored with the proposal and never sent to the MCP server.

The gateway validates the arguments against the tool's input schema, its `candidates` and its `scope`. It then confirms that:
- the cited hypothesis exists and is not rejected;
- the proposer has itself posted supporting evidence for it (Section 9.2);
- the target is neither locked nor held, and its rate limit is not exceeded.

If every check passes, the gateway reads the fingerprint (if declared), creates the proposal and returns at once:
- in normal mode, `{"status": "pending_approval", "proposal": "P1"}`;
- in record-only mode, `"recorded"`; Section 6.3 says which checks apply there.

### 5.4 Approval (Phase 1)

**Approval page.** It is laid out for phones first (Section 10.4), in this order:
1. the action in one sentence generated by rules, its target, and a countdown to expiry;
2. the cited hypothesis, whether it converged, and its strongest supporting and refuting quotes;
3. the checks that will run, and when the effect is expected to start;
4. collapsed: raw outputs, opened at the quoted text with the quote highlighted; candidates; the fingerprint;
5. model-written explanations last, as plain text marked "unverified".

- **States.** If someone else already decided, the page says who and when. If the proposal was withdrawn, voided or expired, or the kill switch is on, the page says so and offers no approve button. If support or refutation counts change while the page is open, a banner asks the approver to confirm again.
- **Where.** Approval happens on this page only, never directly from a notification, and needs fresh authentication (Section 9.1).
- **Withdrawal.** When a human rejects a hypothesis, every proposal citing it that is not yet executing is withdrawn.
- **Notifications** follow Section 10.3.

### 5.5 Execution, effect and checks (Phase 1)

- **Before every attempt.** The gateway checks the epoch and re-reads the fingerprint before every execution attempt.
  - Before the first attempt, and before a retry after a connection that was never established, a change voids the proposal, because nothing has run.
  - Before the retry of an idempotent tool from `unknown`, a change, or a failed fingerprint read, cancels the retry instead, and the outcome stays `unknown` for reconcile or a human: the first attempt may itself have changed what the fingerprint reads.
  - Otherwise the gateway executes with the stored arguments (Section 6.4) and posts a `result`.
- **Effect.** For a tool without `takesEffect`, the effect starts when the call succeeds. Otherwise the gateway polls the `takesEffect` read until its condition holds, which starts the effect, or until its deadline passes. While the effect is pending, the target stays locked, and the board shows "awaiting effect" with the deadline.
- **Abandoned.** If the deadline passes, the item closes, the cited hypothesis is rejected or a responder takes over while the effect is pending, the operation is `abandoned` and the responders are notified. The deployment MUST make an abandoned operation unable to take effect (companion document).
- **Checks.** Once the effect starts, the coordinator runs each policy check at its `after` delay, polling within its `window` if one is set. Checks run through the gateway under the platform's identity, may use `args`, `operationId` and `effect` (the output of the read that started the effect), and run even if the item has closed. Each outcome is posted as platform evidence:
  - `passed`;
  - `failed`;
  - `unrunnable`: the read still failed after [3] attempts;
  - `unverified`: its `unverifiedWhen` condition held.
- **Outcome.**
  - If every policy check passed, the effect is `confirmed` and the target is released.
  - If a policy check failed or was unrunnable, the effect is `held`. The responders get a push (Section 10.3), and the target stays held until an approver releases it, so no agent can stack another write on a target whose last write misbehaved.
  - If a check was unverified, or the proposal has no policy check, a human confirms whether the action took effect, and the target is held until then.
- **Agent checks.** An agent's `_check` runs the same way, but its outcome is posted as agent-defined evidence: its condition's result, not the read output. It never confirms an effect and never holds a target.
- **Back to diagnosis.** If the item is still open when a check fails, the failure is posted as evidence, and the run resumes if budget remains.

### 5.6 Stop

A run stops when any of these happens:

- **Converged.** A hypothesis converged and one more wrap-up round has finished.
  - The wrap-up round is outside the rounds and wall-clock budgets, with one round time limit of its own. If convergence and budget exhaustion coincide, the run still gets its wrap-up round, and the stop reason is `converged`.
  - In the wrap-up round the gateway allows reads, `end_turn` and only two other things: refuting evidence on the converged hypothesis, and write calls that cite it.
  - A standing refutation (Section 4.3) revokes convergence. If budget remains, normal rounds resume.
- **Quiet.** A whole round produced no new entries, claims or completions.
- **Budget exhausted.** Wall clock, tokens or rounds ran out.
- **Trigger resolved.** A visible primary run stops at the end of the current round. Shadow-mode runs, including the invisible-arm primary run, ignore trigger resolution; it is recorded on the item only.

When a run stops, the coordinator posts a summary generated by rules, not by a model: hypotheses by score, the conclusion, open tasks, proposals, the stop reason and the budget used.

**Budget.** Budgets cover agent work only. The token budget counts cost-weighted tokens as the LLM gateway prices them: uncached input, cache reads and output. The default is set from measured smoke-run usage before preregistration. The runtime keeps each session's context bounded between rounds (ADR-006). Approvals expire (Section 3.4), and effects have their own deadline (Section 5.5).

**Challenge.** A work item type sets `challenge.mode`:

| Mode | What happens |
|---|---|
| `off` | No challenge. |
| `fork` | When an invisible-arm primary run converges, the coordinator forks a challenge-only shadow run from its board at that moment. Only the agents that subscribe as challengers for the type (Section 3.1) take part, for one round. The fork records whether they produced a standing refutation; labels later show whether the convergence was right (Section 13.3). |
| `primary` | The challengers join the primary run's wrap-up round. They do not count toward the team limit, they leave when the round ends, and their tokens come from a separate allowance of [10%] of the run's budget. While they are present, write calls that cite the converged hypothesis wait until the round ends. |

A deployment moves from `off` to `fork` to `primary` only on preregistered evidence (Section 13.3). Agents that share a model agree easily; the challenge gives one agent the job of disagreeing.

### 5.7 Close and label

- **Closing.** An item closes by a human ruling or automatically (Section 4.1).
- **Labels.** Ground truth comes from the deployment's ground-truth record. Raters turn it into a label without seeing agent output (Section 13.4); responders never label during the work.

## 6. MCP gateway

### 6.1 Agent-facing endpoint

- **One endpoint.** The gateway exposes a single MCP endpoint. Each session authenticates with its gateway token (Section 2).
- **Tool list.** `tools/list` returns the blackboard tools plus every tool allowed by both the agent's `tools` patterns and tool policy. Write tools are extended as in Section 5.3, and hidden in diagnosis-only items (Section 3.6). A hidden tool cannot be called, even by name.
- **Wrap-up round.** The tool list and argument rules narrow as Section 5.6 describes.

### 6.2 Read path

1. Check tool policy. Reject the call if it is not allowed.
2. Check the server's limits. At most [10] calls run at once per server, and shadow runs may use at most [50%] of them. After [3] consecutive connection failures a server is marked degraded for [60 s]: calls to it fail fast, and shadow runs pause.
3. Write the audit record: call id, run, agent, tool and arguments. If the audit write fails, reject the call.
4. Forward the call to the server with the server's credentials. Connection failures are retried [2] times with backoff, within the round time limit and only while the server is not degraded: reads have no side effects, but they do add load.
5. Apply `partialWhen`; a match turns the output into a read error. Redact the output or error, store it, and return it to the agent with the call id.

A read error goes back to the agent and into the audit record. It cannot be cited as evidence.

### 6.3 Record-only mode

In Phase 0, in every shadow run and after a take-over, a write call ends as `recorded`, and no write reaches any server.

- **Checks.** The gateway validates the input schema and, where declared, the candidates and scope. It confirms the cited hypothesis and the proposer's own evidence (Section 5.3).
- **No locks.** Recorded proposals take no lock, are not refused by locks, holds or rate limits, read no fingerprint, and never notify anyone.

**Suggestions.** In the visible arm, the primary run's recorded proposals appear as suggestions (Section 10.2):
- each suggestion card shows the action in one sentence generated by rules, the platform facts a reviewer would need, and the live status of the cited hypothesis: converged, leading, refuted or rejected;
- suggestions are grouped by target and ordered by hypothesis score;
- a suggestion whose hypothesis was rejected or lost convergence is marked superseded, and can be adopted only after a warning.

**Adoption.** A responder records an adoption with a short form:
- as suggested, with changes, or a different action;
- when it was applied;
- optionally, a reference to the change they made.

Raters later judge each suggestion against the ground-truth record (Section 13.4). The platform runs no checks for adopted suggestions, because it cannot tell what the person actually changed.

### 6.4 Execution (Phase 1)

- **Operation id.** Each proposal gets an operation id when it is created. It is random, globally unique and never reused, so not even a database restore can issue one twice. If tool policy names an `operationId` argument, the gateway sends the operation id in it on every attempt, and the server can deduplicate on it. This is the only way to get effectively-once execution against an external system. Without it, the platform promises at most once, and marks anything in doubt `unknown`.
- **Write-ahead.** The gateway persists `executing` before it sends the request. If it cannot persist, it does not send.
- **Restart.** After a gateway restart, every proposal found in `executing` becomes `unknown`.
- **Outcome mapping:**

| Situation | Outcome |
|---|---|
| The connection was never established (refused, name resolution failed) | Retry automatically, at most [3] times while the proposal has not expired; then `failed` |
| A protocol-level rejection proving the call did not run, for example an unknown tool | `failed` |
| A tool execution error (`isError: true`) | `unknown`; or `failed` if tool policy declares `errorMeansNoEffect` and no earlier attempt ended `unknown` |
| Any other protocol error, a timeout, or a lost connection | `unknown` |

- **After `unknown`.** Once any attempt of an operation has ended `unknown`, later errors map to `unknown`, whatever `errorMeansNoEffect` says. From then on only the idempotent retry, a reconcile condition or a human decides.
- **Idempotent tools.** They get one automatic retry after `unknown`, with the same operation id. After that, the outcome stands.
- **Reconcile.** If tool policy declares `reconcile`, the gateway runs it while the outcome is `unknown` (for an idempotent tool, after its retry), up to [3] times, [60 s] apart. If its condition holds, the outcome becomes `succeeded` and the effect proceeds. A condition that does not hold never decides the outcome, because the operation may still be in progress; its results are attached for the human.
- **Resolving `unknown`.** A human resolves whatever stays `unknown`, as Section 4.4 describes.

## 7. Shadow mode

A shadow run is a candidate configuration working a live item. It:

- is hidden from humans until its release (below), and sends no notifications;
- ends every write call it makes as `recorded`;
- receives no human entries, and its board gets no human-operated events such as milestones, incident updates or trigger resolution;
- keeps running when the trigger resolves or the item closes, until convergence, quiet or budget, so its time to a correct conclusion is observed rather than cut off;
- is scored blind against the same labels as the primary run (Section 13.4).

**Release.** Shadow-mode output, including the invisible-arm primary run's board, becomes visible when the item's label is imported or, for an item that will not be labeled, [14 days] after it closes. Until then no view shows it, version history included. People who write the ground-truth record must not see agent conclusions first.

**Uses**
- **ADR-011 invisible arm.** The primary run executes in shadow mode.
- **Comparisons.** These are listed in `shadowRuns` (Section 13.3) and scored on invisible-arm items only, where no human input reaches any run.
- **Probation for changes.** A new model, skill version, agent or team configuration runs in shadow before it replaces the current one (Section 12.3). Before the first gate, probation uses invisible-arm items only; after it, items of either arm. A shadow run on a visible-arm item is still hidden and receives no human input. Its labels may lean toward the current primary, which errs against promotion.
- **Challenge forks** (Section 5.6).

**Budget and cuts.** Each shadow run has its own budget, by default equal to the primary run's. Shadow runs count toward the daily token cap. Comparison, probation and fork runs are cut first, once spend reaches [80%] of the cap. The primary run of either arm is never cut before the hard cap. Each cut is recorded, and the preregistration says how cut and `not_staffed` items count.

## 8. Failure handling

### 8.1 Principles

1. **Doing too little beats doing too much.** Anything uncertain about a write becomes `unknown` and goes to a human. Only `idempotent` tools retry.
2. **The platform never degrades responders' own tools.** It only subscribes to triggers, and when it fails, people fall back to their existing process. Its read load is bounded (Section 6.2).
3. **Nothing fails silently.** Every failure is audited and shown as a `notice`: on the board in the visible arm, and on the item record in the invisible arm.

### 8.2 Failure table

| Failure | Platform behavior |
|---|---|
| An agent session or sandbox crashes | The turn counts as ended. The next round restarts the session in a new sandbox and rebuilds its context from the board. After [2] consecutive failures, the agent leaves the run. |
| The round time limit is reached | The agent is interrupted. Committed entries stay; results of reads still in flight are discarded. |
| A model call fails or is rate-limited | The LLM gateway retries transient errors with backoff within the round time limit; a model call has no side effects. If the errors persist, the agent's turn ends and the next round tries again. The wall-clock budget keeps running. |
| A read call fails | Connection failures are retried [2] times while the server is not degraded (Section 6.2). Other errors, and failures that persist, go back to the agent, redacted, and are audited. They cannot be cited. |
| A server keeps failing or slows down | It is marked degraded: calls fail fast, and shadow runs pause until it recovers (Section 6.2). |
| A blackboard call's response is lost | The agent repeats the call, and the repeat returns the original result (Section 5.2). |
| A listed tool's schema hash changes | That tool is hidden at once, including in running items. Its proposals that are not yet executing are voided, and the owner is notified. |
| Open-time reads fail | The item opens degraded (Section 5.1). |
| The coordinator crashes | All state is in the database. On restart the coordinator resumes rounds, leases, budgets and proposals. |
| Two coordinator instances run at once, for example during a deploy | Only the holder of the coordinator lease in the database acts, and every coordinator write checks the lease. The other instance waits. |
| The MCP gateway crashes or restarts | Proposals found in `executing` become `unknown`, and reconcile reads run where declared (Section 6.4). |
| The database or audit store is unavailable | The gateway rejects every call, because it audits before it calls. The platform stops; responders are unaffected. |
| A notification fails | The platform retries, then uses the fallback channel, then flags the failure in the live view. |
| The trigger flaps | A new firing with the same dedup key sends an item in `awaiting_close` back to `investigating` with the remaining budget. After the item has closed, a new item is opened and linked to the previous one. |
| Inbound events repeat or arrive out of order | Repeats are dropped by event id. An event older than the last applied status changes nothing (Section 3.6). |
| An effect does not start by its deadline (Phase 1) | The operation is `abandoned`, and the responders are notified (Section 5.5). |
| A post-execution check fails or cannot run (Phase 1) | It is pushed to the responders, and the target stays held until an approver releases it (Section 5.5). |
| Costs run away | The LLM gateway enforces a hard token limit per run. Shadow runs are cut first (Section 7). Once the daily cap is reached, new items are not staffed. |
| Sensitive data appears in any input | It is redacted before any agent, model, store or notification sees it (Section 9.4). |

### 8.3 Kill switch

A control that at once:

- stops staffing new items;
- cancels running sessions, with a notice on each board;
- rejects proposal executions: proposals that are pending or approved expire, and their targets are released.

Responders and approvers may turn the kill switch on; only an admin may turn it off. Its state is kept outside the restorable database, so a restore cannot turn it off.

If the platform itself may be compromised, the companion document gives the deployment-specific steps for revoking write credentials.

### 8.4 Database restore

After the database is restored from a backup:

1. Freeze new proposals.
2. For every `approved` proposal, run its reconcile read where declared. A proposal proven executed becomes `succeeded`; void every other proposal that is `pending_approval` or `approved`.
3. Mark every `executing` proposal `unknown`. Reconcile reads then run where declared (Section 6.4).
4. Start a new epoch. The epoch is derived from the restore time, kept outside the restorable database, and checked before every execution attempt, so nothing approved before the restore can execute (Section 5.5).
5. Reconcile with the target systems' own records where they exist (companion document). An operation id found there whose proposal is not `succeeded` in the restored database marks a write that the restore lost; its target is held until an approver reviews it.
6. Check audit continuity: the audit sink outside the database (Section 9.4) shows what happened between the backup and the restore.
7. Unfreeze.

## 9. Security

### 9.1 Threats and controls

| Threat | Main controls |
|---|---|
| Instructions injected through tool output (logs, tickets, trigger text) | Human approval of exact calls; least privilege per agent; argument candidates and scope; values bound as typed variables, never spliced into conditions; data boundaries (Section 9.3) |
| Content that attacks the human interface: markup or script in logs, trigger fields or model text | Everything is rendered as inert text with contextual encoding under a strict content security policy. State-changing requests carry cross-site request forgery protection. Approving, resolving `unknown`, releasing a held target and dismissing a refutation require fresh authentication. |
| Contagion between agents through the board | A proposer must hold its own evidence; convergence needs independent sources; entry limits |
| Poisoned knowledge: injected text kept as a lasting lesson that every agent then reads | Agents keep no memory across items (Section 1.4); skills change only through reviewed versions with an owner, and nobody reviews their own change (Section 3.7); yanks remove at once (Section 3.2) |
| Fabricated evidence | Provenance and the verbatim quote check |
| Agent-written checks that read beyond the proposer's grant | `_check` is limited to the proposer's read tools, and its outcome is agent evidence, never a platform fact (Sections 5.3, 5.5) |
| Writes outside the item's scope, or lock evasion through spelling | `scope` bindings; target arguments must match candidates (Section 3.3) |
| Forged trigger fields, or replayed inbound requests | Authenticated inbound events with a maximum age; routing and ordering from trusted fields only (Section 3.6) |
| Forged evidence for reconcile | Reconcile accepts only records that carry the server's own write identity (Section 3.3) |
| Authorization data supplied by an untrusted server | Approver groups come from the identity provider, or from a registered identity source only (Section 3.4) |
| Tool poisoning, or silent changes to an MCP server's tools | Deny by default; per-tool schema hashes; server annotations not trusted |
| Credential theft or data exfiltration from a sandbox | No credentials in sandboxes; egress only to the two gateways; the LLM gateway's allowlist (Section 9.3); redaction |
| People reading data through agents that they could not read themselves | Opening items and seeing raw outputs are limited by role and service (Section 10.1) |
| Approval fatigue | Phone-first approval pages that separate facts from model text; approval-quality metrics (Section 13.6) |
| An insider changing configuration | Reviewed configuration (Section 3.7); owners notified; audit; write tools cannot reach configuration (invariant 7) |
| Data sent to the model vendor | All model calls go through the LLM gateway; redaction happens before the model sees anything (Section 9.4); vendor terms (ADR-007) |
| Unbounded copies of sensitive data | Retention classes (Section 9.4) |

### 9.2 Deterministic rules

1. Evidence carries a verbatim quote of the output it cites (Section 4.2).
2. Convergence needs [2] contributors and [2] sources, and no standing refutation (Section 4.3).
3. A proposer must itself have posted supporting evidence for the hypothesis it cites. Without this rule, an agent whose context was injected could steer another agent that holds write tools, getting around least privilege.
4. An agent posts at most [5] new entries per round and holds at most [2] open claims.
5. Write arguments must fall within their declared candidates and scope (Section 3.3).

### 9.3 Data boundaries, sandboxes, gateways and secrets

- **Data boundaries.** Board entries and tool outputs given to an agent are wrapped in explicit delimiters, labeled with author and provenance, and marked as data rather than instructions. This lowers injection risk; it does not remove it.
- **Sandboxes.** Runtime built-in tools that reach outside the sandbox are disabled, and host or workspace configuration never enters prompts (ADR-006).
- **LLM gateway allowlist.** The LLM gateway forwards only requests that name the model in the agent's configuration and carry tool definitions equal to the session's MCP-gateway tool list. It rejects, and audits, vendor-hosted tools such as web fetch or code execution, remote connectors, and stateful vendor APIs such as file or batch storage. It logs prompts and completions only into the audit store.
- **Secrets.** Each component resolves only its own secret references, at the time of use, from the secret store: the inbound handler its inbound secrets, the MCP gateway the server credentials, the LLM gateway the vendor key.
  - Secrets never appear in configuration, the database, audit records or logs.
  - Rotation needs no downtime; an inbound source has two valid secrets during rollover.
  - Write-server credentials SHOULD sit in a separate executor that accepts only approval-bound proposals.
- **Transport.** Every hop is authenticated and encrypted: sandbox to gateways, gateways to servers and vendor, and inbound sources to the platform.

### 9.4 Redaction and retention

- **Redaction.** Everything that reaches an agent, the LLM gateway, a store or a notification first passes the deployment's redaction rules: tool outputs and tool errors, write results, inbound event content and human entries. The platform's own operational logs never contain tool outputs, error bodies or inbound payloads.
- **Retention.** All stored data is encrypted at rest.

| Data | Kept for |
|---|---|
| Approval, execution, configuration and access records, without output content | [1 year] |
| Measurement items: run event logs and the cited redacted outputs, snapshotted into the rating packet at close | Until [30 days] after the label is imported |
| Everything else: raw outputs, trajectories, boards and event logs | [30 days] |

- **Replay and fixtures.** Replay covers the items still within retention. Recorded items become smoke fixtures only after a redaction review.
- **Audit sink.** Audit records are also written to a sink outside the restorable database that operators cannot alter, or are chained with hashes so that tampering shows.

## 10. Human interface

### 10.1 Roles

| Role | Can |
|---|---|
| Viewer | See boards of visible-arm items for services whose data they may read |
| Responder (the item's principal and responders) | Open items for their services; post evidence; issue the rulings of Section 4.2 except those reserved to approvers; promote a queued item; turn the kill switch on |
| Approver (per approval policy) | Approve or reject proposals; resolve `unknown`; release a held target; turn the kill switch on |
| Agent owner | Change their agent's configuration (Section 3.7); see its cost, metrics and version history |
| Skill owner | Review, release, correct and yank their skill's versions (Section 3.2); see the skill's version history |
| Rater | Score blind packets (Section 13.4). A rater MUST NOT be a platform builder. |
| Admin | Use the kill switch, including turning it off; change platform configuration |

- **Data access.** Raw outputs are visible only to the item's responders, its approvers and raters. Boards and evidence are limited to people whom the identity provider entitles to the service's data.
- **Sign-in** uses the organization's single sign-on (companion document).

### 10.2 Views

- **Items.** Open, queued and recently closed items, with the same fields in both arms: service, title, state, principal and milestones. From here a responder opens an item, promotes a queued one or sets a milestone. Only visible-arm rows, and invisible-arm rows after their release (Section 7), link to a board; until then, invisible-arm rows show the same fields and the same milestone form, with no agent output.
- **Live board.**
  - A header generated by rules and refreshed each round: the item state and stop reason; the conclusion, labeled "converged", "leading, not converged" or "no conclusion"; the budget used; and a "needs you" strip for suggestions or proposals waiting on a human.
  - Below it, hypotheses ranked by score, each with its strongest supporting and refuting quotes, then the ruled-out hypotheses.
  - Last and collapsed, the chronological entry feed with authors and provenance links.
  - A connection indicator: "live", or "reconnecting, last updated at T".
- **Suggestions** (Phase 0) and **approval inbox** (Phase 1).
- **Target locks** (Phase 1). Who holds each lock or held target, and why.
- **Version history** (Section 14.1). For each agent and skill version, the closed items it ran on, with their conclusions and labels. Invisible-arm conclusions appear only after their release (Section 7).

**Board states.** What the board says in each state, and whether a push goes out:

| State | Board text | Push |
|---|---|---|
| Queued | Its position, and what promoting it does | No |
| Not staffed | "Not staffed: handle with your usual process" | Yes |
| Degraded | Which open-time read failed | No |
| Diagnosis-only | Which consistency check failed; no suggestions | No |
| Converged | The conclusion | Yes, when the run stops |
| Stopped without converging | The leading hypothesis, if any, marked "not converged" | Yes |
| Trigger resolved | "Alert resolved", with the stop summary | No |
| Reopened after a flap | A divider with the new firing time | No |
| Late trigger event | "Received late, not applied" | No |
| Taken over | "Taken over by" the responder; agents keep recording | No |
| Kill switch on | "Stopped by the kill switch" | Yes |

### 10.3 Notifications

- **Visible arm.** One message when the item opens, with a link to the board. After that, pushes go out only for:
  - pages: proposals awaiting approval, `unknown` outcomes, failed or unrunnable checks, effects that missed their deadline (all Phase 1), and the kill switch;
  - messages: convergence, sent when the run stops; a run that stopped without converging; `not_staffed`; and, in Phase 1, rate-limit pauses and effects that started.

  Everything else stays in the live view.
- **Reversals.** Any change that reverses or cancels something already pushed goes to the same recipients: revoked convergence; a pushed proposal that was voided, failed, withdrawn or expired; an abandoned operation.
- **After close.** Pushes about a closed item go to its principal and responders, resolved again at push time.
- **Content.** Push text is generated by rules from platform facts only: enums, numbers, names and platform links. It never contains model-written text.
- **Invisible arm.** No notifications.
- **Channels.** Each push class maps to a configurable outbound channel, with a fallback channel (companion document).

### 10.4 Devices and access

- The items view, board, suggestions, approval page and the resolve and release screens are designed for phone portrait first.
- Every push links to the exact item, proposal or decision, and signing in again returns the user to that link.
- Status, such as converged, refuted, held or `unknown`, is never shown by color alone. A dark theme is available, and touch targets are at least 44 points.

## 11. Milestones

- **Source.** A work item type lists its milestones (Section 3.5). Milestone times come from the deployment's milestone source through inbound events, and attach to the incident (Section 4.1). The same source MUST serve both arms.
- **Fallback.** A responder may set a missing milestone in the items view (Section 10.2), in either arm. The ruling records where the time came from.

## 12. Testing

### 12.1 Layers

| Layer | What it covers | How | When |
|---|---|---|---|
| Gateway contract | Every rule in Sections 3.3, 4, 5.2–5.5, 6 and 9.2, including repeated calls, server limits and record-only behavior | A programmable fake MCP server records every request and injects timeouts, dropped connections, errors, slow responses and tool-list changes. In Phase 1, the tests also kill the gateway at each step of execution. | Every commit; no model calls |
| Coordinator rules | Rounds, snapshots, convergence, standing refutations, challenge modes, the wrap-up round, quiet rounds, budgets, leases, key merging, limits, dedup, incidents, event order and instances, stop and close, and the coordinator lease | Scripted fake agents post entries from a script; the tests assert the coordinator's sequence of decisions | Every commit; no model calls |
| Agent smoke | Whether an agent version reaches a known cause within budget without overstepping. A challenger must refute a planted false convergence and must not refute a planted true one. | Fake MCP servers return canned data; 1–2 scenarios per agent, each run [5] times; the pass rate is reported | When an agent's configuration (model, skill version) changes, for that agent only |
| Red team | Injection, contagion between agents, a poisoned challenger, entry flooding, claim hoarding, forged quotes, interface payloads (Section 9.1), requests for vendor-hosted tools through the LLM gateway, and sandbox probes for credentials or outbound access | Attack content planted in fake server outputs; probe scripts run inside the sandbox | Once before Phase 0 measurement starts; again whenever a security rule changes; write-path cases before Phase 1 |
| Load | An incident storm: [3] items with all their runs against slow fake servers and a rate-limited fake model. Server limits hold, shadow runs pause first, and responders' tools keep their capacity. | Fake servers with injected latency | Once before Phase 0 measurement starts |

### 12.2 Gateway state-machine properties (Phase 1)

Property-based tests generate random sequences of proposals, approvals, rejections, expiries, effects, checks, crashes, restarts and every error class. After each step they assert:

1. every write the fake server receives matches an approval with the same hash;
2. each approval executes at most once, apart from the single retry allowed for `idempotent` tools;
3. when a request reaches the fake server, its audit record already exists (the fake server checks on receipt);
4. at most one unfinished proposal exists per target at any time, and none names a held target;
5. shadow runs and record-only mode never send a write;
6. every attempt for one approval carries the same operation id, and an outcome leaves `unknown` without a human only through the single idempotent retry or a reconcile condition that holds;
7. no check starts before its effect has started, and no target is released while its proposal's effect is `pending` or `started`;
8. no attempt executes with a stale epoch or a changed fingerprint.

In Phase 0, properties 3 and 5 are tested on their own.

### 12.3 Replay, versions and probation

- **Replay.** Recorded items serve as regression tests. Because the coordinator is deterministic, any closed item within retention can be replayed from its event log. Before a rule change is merged, past items are replayed, and the first decision that would change in each is listed for review; after that point, agent behavior is counterfactual.
- **Release record.** Git holds content. The release record holds each version's state (candidate, current, corrected or yanked) and the current pointers, with an audit trail. The coordinator generates probation shadow runs for candidate versions.
- **Probation.** A changed agent, skill version or team configuration replaces the current one only after:
  - its smoke scenarios pass;
  - it has run in shadow on at least [5] items without a safety violation: invisible-arm items before the first gate, items of either arm after it (Section 7);
  - its blind scores have gone to its owner and a platform admin, who decide and record the decision in the release record.

  Corrections skip shadow probation, as Section 3.2 describes.
- **Freeze.** During an ADR-011 measurement window, the invisible arm's configuration is frozen. Promotions wait until the window ends, and yanks apply to the visible arm only (Section 3.2). Platform code changes during a window are recorded as deviations.
- **Tracing.** Every item locks its configuration, so the platform can list the items that ran with any agent or skill version, with their conclusions and labels. After a yank, the skill's owner reviews those items and records what they found.
- **Retrospective set.** Past incidents with recorded root causes MAY serve as a tuning and smoke set, with reads bounded to the alert time. They never count toward the gate. Live state that cannot be replayed makes them pessimistic for some agents.

## 13. Value validation

### 13.1 Arms

At open, the coordinator draws each new incident's arm from a recorded seed, at the preregistered invisible share [50%], and every item of the incident takes that arm (Section 4.1). If a later link joins items with different arms, the incident is excluded from measurement and reported. Items opened by a human are always visible and outside measurement.

- **Visible:** the normal flow.
- **Invisible:** the primary run executes in shadow mode, with no board, notifications or approvals until its release (Section 7).

### 13.2 Metrics

All definitions, thresholds and sample sizes are preregistered and signed before any data is seen (ADR-011).

| Metric | Role | Definition |
|---|---|---|
| Hit rate | Gate | Invisible arm only, one result per incident, from the primary run of the incident's first item. The share of incidents whose conclusion as of the cutoff matches the label. The cutoff is the earlier of the run's stop and the human `cause_identified` time, so the run cannot learn the answer from people's fixes. Conclusions after the cutoff are reported separately. |
| Baseline margin | Gate | On the same incidents, the primary run's hit rate minus the preregistered baseline comparator's (Section 13.3). |
| False-convergence rate | Guardrail | Invisible arm only. The share of incidents whose primary run converged before the cutoff on a hypothesis that does not match the label. Being confidently wrong is worse for responders than reaching no conclusion. |
| Milestone durations | Direction only | The durations the deployment designates, compared between arms. There are too few incidents to gate on them; a clearly worse visible arm triggers an investigation. |
| Safety floor | Hard gate | No write reaches a server without a matching approval. In Phase 0, no write reaches a server at all. Where target systems keep their own records, reconcile against them weekly. |
| Responder feedback | Reported | Visible arm. The convergence push and the board header ask "correct, partly, wrong, too early to tell, or already knew", at convergence and again at close. |
| Suggestion accuracy | Reported | Visible arm. The share of suggestions that raters judge to match the action that worked, according to the ground-truth record, with the adoption-form answers alongside (Section 6.3). |
| Headroom | Phase 1 entry | The median "waiting to act" duration the deployment designates, over incidents whose recorded mitigation the deployment's write tools could perform, and counting only the part of the wait that the write path can remove (companion document). |
| Builder operating time | Reported | Hours the builders spend operating and maintaining the platform during the window, not building it, reported next to token cost. |

The hit rate counts only the conclusion. "Some hypothesis on the board was right" does not count: several agents can post a dozen hypotheses between them, and counting any of them would inflate the rate. The preregistration defines a hit: the key alone, or the key plus the specific change or component that the label names.

### 13.3 Comparisons

- **Baseline comparator.** The preregistration names one rule-based baseline that needs no model, such as "the most recent change". It is scored blind on the same incidents and decides the baseline margin (Section 13.7).
- **Collaboration versus independent work.** The `independent` shadow run (Section 3.5) is the same team limited to a single round with the full time budget. Agents never see each other's entries, and the platform aggregates their hypotheses by key.
  - It runs on invisible-arm items with the primary run's wall-clock and token budget, limited to one round. It has no wrap-up round: its conclusion is taken when its single round ends.
  - Paired, blind-scored results show whether multi-round collaboration earns its cost and complexity.
  - It is reported, not gated.
- **Challenge** (fork mode; Section 14.1 says when). For each convergence of an invisible-arm primary run, the fork shows whether the challengers produced a standing refutation, and the label shows whether the convergence was right. Two case counts are reported: wrong convergences caught, and right convergences blocked. Preregistered thresholds on both decide whether the primary run enables challenge in a later window.
- **External comparators** (ADR-011). Their conclusions enter blind scoring as anonymous outputs. The comparators themselves are first-deployment content.
- **Sample size.** At [20] incidents per window these comparisons are case studies. A comparison may change the primary configuration only after a preregistered minimum number of discordant pairs, pooled across windows if needed.

### 13.4 Blind scoring

- **Packets.** For each closed in-scope incident, the platform exports a rating packet. It holds a pointer to the deployment's ground-truth record; every run's output (primary run, shadow runs, comparators and the baseline); and the visible arm's suggestions. The cited redacted outputs are snapshotted into it at close.
- **Label first.** Raters first record the ground truth as a hypothesis key from the ground-truth record, without seeing any output, or mark it "insufficient". The platform then computes key matches.
- **Then judge.** Raters judge only what key matching cannot decide: outputs whose key contains `other`, suggestions, and, where the preregistration requires it, whether a matching output names the specific change or component. Each judgment is match, partial or no match. A run with no conclusion appears as a fixed row, and identical keys are merged.
- **Format.** Outputs are shuffled, stripped of their source and rendered in one format: hypothesis key plus one line of rationale. Without a single format, raters could tell sources apart by writing style.
- **Raters.** At least two raters who are not platform builders score each packet independently. The platform imports their scores, ties each score to an eligible rater, reports inter-rater agreement and computes the metrics.
- **Order.** Within the gate sample (Section 13.5), incidents whose runs disagree are labeled first. Order never changes which incidents are in the sample.
- **Phase 0 tooling.** Phase 0 uses file export and import, not a rating UI.

### 13.5 Sample and stopping

- **Gate sample.** The first [20] in-scope incidents of the invisible arm to close, all labeled, whatever the labeling order. Wait at most [8 weeks].
- **Hits needed.** The observed hit rate needed for a 90% Wilson lower bound of at least 50%:

| Invisible incidents | Hits needed | Observed rate |
|---|---|---|
| 10 | 8 | 80% |
| 20 | 14 | 70% |
| 40 | 26 | 65% |

- **Funnel.** Before preregistration, the deployment estimates from history how many incidents survive each step: in scope, invisible, not builder-handled, with a recorded root cause, and staffed (companion document). If the estimate falls below [20] in [8 weeks], the preregistration picks the path now: a higher invisible share, a longer window, or the 10-incident row.
- **Invisible share.** It sets how long reaching [20] takes: at [50%] it needs about 40 in-scope incidents, at 70% about 29. A higher share shortens the window but leaves fewer visible incidents, and those carry responder feedback and the milestone comparison.
- **Builder-handled incidents** are those where a platform builder was the principal or set `cause_identified`. They are excluded or reported separately (ADR-011).
- **Degraded, cut and not-staffed incidents** count as the preregistration says (Sections 5.1 and 7).
- **Shortfall.** If [20] incidents are not reached within [8 weeks], the preregistered rule applies: evaluate the available incidents as directional, and decide on responder feedback and the safety record.

### 13.6 Cost and governance metrics

- **Cost.** The LLM gateway records tokens and spend per run, agent and arm. The MCP gateway records tool calls on the same dimensions.
  - Reported: cost per incident, cost per hit and the shadow share.
  - A ceiling on the primary runs' average cost per incident is preregistered as a guardrail; shadow spend is reported separately.
  - Builder operating time is reported next to cost (Section 13.2).
  - Cost is shown per owning team. It is the price of the governance this platform exists for.
- **Owner autonomy.** Tracked figures:
  - the number of agents owned by teams other than the builders (target: at least 1);
  - the configuration changes those owners made themselves;
  - the time from request to live.
- **Least privilege in practice.** Tools granted versus tools used, per agent. Grants unused for [30 days] are flagged for removal.
- **Approval quality** (Phase 1). Tracked figures:
  - the distribution of time to approve;
  - the rejection rate;
  - the share of approvals where the approver expanded a raw output first.

  Approving in seconds, never rejecting and never expanding evidence is a rubber stamp.

### 13.7 Phase 0 gate

| Outcome | Decision |
|---|---|
| Safety floor broken | Stop measuring, fix the cause, restart the window |
| False-convergence guardrail breached | Treat as a failed gate |
| Hit-rate lower bound below [50%] | Iterate on skills and configuration for up to [3 weeks], then measure again in a new window that is evaluated on its own. If it still fails, stop, or try an off-the-shelf diagnosis tool as an agent runtime (ADR-001). |
| Baseline margin below [10] points | The agents show no value over the baseline. Do not start Phase 1; consider the baseline or an off-the-shelf tool as the agent runtime (ADR-001). |
| Sample not reached by the deadline | Apply the preregistered rule (Section 13.5) |
| Gate passed; headroom median below [5 min] | Read-only is the end state for this phase; the write path is not built |
| Gate passed; headroom exists | Preregister the Phase 1 evaluation (Section 13.9), then start Phase 1 |

### 13.8 After the gate

Measurement turns into monitoring; it does not stop.

- **Labels.** Every in-scope incident whose runs reached different conclusions is labeled, plus a random [20%] of the rest. One rater who is not a builder is enough.
- **Rolling figures.** They are computed on the random sample only, over its last [20] labeled incidents; disagreement incidents are reported separately as case lists. Alarms on the hit rate and the false-convergence rate follow a preregistered rule with a known false-alarm rate, and notify the owners of the agents involved and a platform admin.
- **Second runs.** After the gate, probation and comparison shadow runs may run on either arm (Section 7), so disagreement still has something to compare.
- **Contamination.** After the gate, responders see agent output, and their records may follow it, so labels can lean toward the agents. A small invisible share [10%] MAY continue, with the team lead's consent, as an uncontaminated reference. Without it, the rolling figures show trends, not proof.
- **Per version.** Results are also listed per agent and skill version (Section 12.3). At the first deployment's volume these are case lists for owners to review, not rates.

### 13.9 Phase 1 evaluation

It is preregistered before Phase 1 is built, and it compares actions proposed through the platform with manual ones:

- the designated waiting-to-act duration, and the time to recover;
- the share of executed writes whose checks failed, or that people reversed;
- approval latency, by time of day.

If the platform path is slower than the manual one, or causes more failed or reversed actions, the preregistered stop rule applies: the write path is switched off for the affected tools.

## 14. Delivery plan

### 14.1 Phases

| Phase | Platform scope |
|---|---|
| Before the build | The census and the commitments of Section 14.3. Their answers can cancel work: a historical headroom below [5 min] drops every Phase 1 preparation, and a strong baseline raises the bar the agents must clear. |
| Spike | Runtime isolation (ADR-006); a census of available read-only MCP servers |
| Phase 0 (read-only, full collaboration) | Section 3, without patch lines; Section 4, with proposals only `recorded` and without locks, holds or effects; Sections 5.1–5.3 and 5.6–5.7, without joins and challenge; Sections 6.1–6.3; Section 7, without challenge forks; Sections 8.1–8.3; Section 9; Section 10, without the approval inbox, target locks and version history; Section 11; Section 12, except write-path properties and the challenge tests, smoke scenarios and red-team case; Sections 13.1–13.7 |
| Measurement window | Until the gate sample is labeled, at most [8 weeks]. Built during the window, because none of it changes how runs execute: Section 13.8, the version history view, and challenge in fork mode for the next window. Phase 1 design proceeds in parallel; building waits for the gate. |
| After the gate | Joins; patch lines; challenge in primary mode, if the fork evidence supports it |
| Phase 1 (write path) | Section 4.4 in full; Sections 5.4–5.5; Section 6.4; Section 8.4; Section 13.9; the approval inbox and target-lock view; Section 12.2; write-path red team; first-deployment write tools |

### 14.2 Estimate

| Phase | Person-weeks |
|---|---|
| Spike | about 1 |
| Phase 0 | 20–26 |
| Measurement window, in parallel | about 2 |
| Phase 1 | 10–12 |

These are rough estimates. They assume an experienced engineer making heavy use of AI coding assistants, and they include integration and testing.

- **2 people:** spike and Phase 0 take about 3.5–4.5 months, and measurement takes 1–2 months. The gate falls around month 4.5–6.5, and the write path goes live around month 6.5–8.5.
- **1 person:** Phase 0 alone takes about 7–9 months.
- **Capacity.** 2 people for 2 months is about 16 nominal and 12 effective person-weeks. A timeline of 1–2 months cannot produce a value decision: measurement alone needs 4–8 weeks of wall-clock time after Phase 0 is built.
- **Compared with the superseded v1 spec.** It estimated 8–11 person-weeks for a single-agent design, and the third document review raised that to 14–20. Full collaboration in Phase 0 adds about 3–4 person-weeks and delays the gate by about a month.
- **Reviews of 2026-10-11.** Two review rounds added about 5 person-weeks to Phase 0 and 4 to Phase 1. That is net of the work they moved out of Phase 0: patch lines, joins, challenge, monitoring after the gate, version history and Phase 1 locks. Most of the addition is correctness and usability the earlier drafts lacked: incidents, the effect lifecycle, the board, retention, inbound handling and server limits.

### 14.3 What others must provide, before the Phase 0 build starts

- The team lead's consent to hide agent output at random for part of the incidents (ADR-011), and optionally a small share after the gate (Section 13.8);
- someone who is not a builder, to co-sign the preregistration;
- at least two named raters who are not builders, for about 10 minutes per incident, and one after the gate for the monitoring sample (Section 13.8);
- a named team outside the builders that owns an agent and writes its skill (Section 1.2);
- the historical census of the companion document: headroom, the baseline, the sample funnel and alerts per incident.

## 15. Parameters

| Parameter | Default | Section |
|---|---|---|
| Run budget: wall clock / cost-weighted tokens / rounds | [15 min] / [2,000,000] / [6] | 3.5, 5.6 |
| Round time limit | [150 s] | 5.2 |
| Agents per run | [4] | 5.1 |
| Items investigating at once | [3] | 5.1 |
| New entries per agent per round | [5] | 4.2 |
| Open claims per agent | [2] | 4.2 |
| Claim lease | [2] rounds | 4.2 |
| Budget extension | [1 round and 5 min] | 4.2 |
| Reopen window after close | [24 h] | 4.1 |
| Convergence: contributors / sources | [2] / [2] | 4.3 |
| Refutation margin | [2] | 4.3 |
| Challenge mode | [off] | 5.6 |
| Challenger token allowance (primary mode) | [10%] of the run budget | 5.6 |
| Incident correlation window | [30 min] | 3.6 |
| Inbound request maximum age / clock skew | [5 min] / [2 min] | 3.6 |
| Open-time read retries | up to [2 min] | 5.1 |
| Proposals per target per hour | [3] | 4.4 |
| Reminder for `unknown` outcomes and held targets | [15 min] | 4.4 |
| Approval expiry | [60 min] | 3.4 |
| Connection retries before `failed` | [3] | 6.4 |
| Reconcile attempts / interval | [3] / [60 s] | 6.4 |
| Calls at once per server / shadow share | [10] / [50%] | 6.2 |
| Degraded server: consecutive failures / cool-down | [3] / [60 s] | 6.2 |
| Read retries on connection failure | [2] | 6.2 |
| Attempts to run a check | [3] | 5.5 |
| Consecutive session failures before removal | [2] | 8.2 |
| Auto-close after the trigger resolved | [30 min] | 4.1 |
| Daily token cap | Set by the deployment | 8.2 |
| Shadow cuts start at | [80%] of the daily cap | 7 |
| Shadow release for unlabeled items | [14 days] after close | 7 |
| Retention: decision records / measurement items / everything else | [1 year] / [30 days] after labeling / [30 days] | 9.4 |
| Unused-grant flag | [30 days] | 13.6 |
| Smoke runs per scenario | [5] | 12.1 |
| Shadow items before promotion | [5] | 12.3 |
| Invisible share | [50%] | 13.1 |
| Invisible incidents for the gate / maximum window | [20] / [8 weeks] | 13.5 |
| Hit-rate gate | 90% lower bound ≥ [50%] | 13.7 |
| Baseline margin | ≥ [10] points | 13.7 |
| Headroom threshold | Median ≥ [5 min] | 13.7 |
| Iteration after a failed gate | [3 weeks] | 13.7 |
| Labels after the gate: random share of incidents without disagreement | [20%] | 13.8 |
| Rolling window after the gate | [20] labeled incidents | 13.8 |
| Invisible share after the gate (optional) | [10%] | 13.8 |

## 16. Documents and ADRs

These changes are carried out after this spec is approved.

| ADR | Action |
|---|---|
| 001 Wedge: ops incidents | Keep the decision, and move its technology facts to the companion document. Candidate E (off-the-shelf tools) becomes a candidate agent runtime, not a replacement for the platform. Its "integrate if the build is not clearly better" rule becomes the baseline margin of the gate (Section 13.7). |
| 002 Single agent by default | Superseded by ADR-012. |
| 003 Identity is versioned configuration | Keep. Shadow runs execute several versions at once, which meets its re-review trigger. Add the release record (Section 12.3): Git holds content, and the release record holds version states. |
| 004 Postgres plus a task library | Keep. The gateway now executes writes in Phase 1, so the premise "no fencing, no separate authorization store" no longer holds. Add the coordinator lease (Section 8.2) and the restore procedure (Section 8.4). |
| 005 MCP plus effect adapters | Superseded by ADR-013. Untrusted annotations, hash pinning and candidate arguments carry over. |
| 006 Agent runtime spike | Keep. Add four criteria: round-based turns, gateway-only MCP, built-in tools that reach outside the sandbox disabled, and a session context kept bounded between rounds. |
| 007 One model vendor behind an LLM gateway | Keep. Add hard per-run, cost-weighted token budgets, cost attribution and the request allowlist (Section 9.3). Decide whether the LLM gateway is built or an existing proxy is extended, and list the proxy features relied on. |
| 008 Single-organization deployment | Keep. Drop the rationale "the platform holds no cluster write access"; that is now a first-deployment choice. |
| 009 Capability packs | Superseded by ADR-014. |
| 010 Draft PR plus required check | Leaves the platform layer and becomes required hardening for first-deployment write tools, because two-person control in that deployment depends on its required check (companion document). Its core idea, approval bound to exact content, is invariant 2. |
| 011 Measurement | Keep. Rebase it on shadow mode and on incidents. Add the hit rate with a cutoff, the baseline margin, the false-convergence guardrail, the fixed gate sample, the Phase 1 evaluation and monitoring after the gate. |
| 012 (new) | Multiple agents on a structured blackboard, with standing refutations and challenge modes. The rationale is governance, so this phase has no team-versus-single comparison. |
| 013 (new) | The platform MCP gateway as the single policy enforcement point: tool policy, approvals bound to exact calls, audit before call, server limits, operation ids, reconcile reads, effects and checks, `unknown` outcomes and held targets. |
| 014 (new) | The configuration model: agents, skills (runbooks included), MCP server registrations with tool policy, work item types, trigger mappings, approval policies and the release record. Skills have owners, corrections and yanks: knowledge is slow to add and fast to remove. |
| 015 (new) | Implementation: Go, domain-driven design, interface-driven ports and adapters (Section 2.2). This settles the open implementation-language question and chooses the condition language (Section 3.3). It constrains ADR-004 (a Go task library) and ADR-006 (how the runtime fits a Go platform). |

Other documents:

- **Older documents.** The v1 spec, the target-state document and the facts-and-spikes draft stay outside this repository and are not translated. This spec and the companion document carry everything still in force, including the spikes (companion Section 6).
- **Language.** New and revised ADRs are written in English.

## Appendix: Glossary

| Term | Meaning |
|---|---|
| Agent | A versioned configuration (runtime, model, skills, tool grants) with a human owner |
| Incident | The work items that one underlying problem causes; arms and measurement count incidents (Section 4.1) |
| Work item | One unit of work, opened by a trigger or a human |
| Run | One team configuration working one item, with its own blackboard and budget |
| Primary run / shadow run | The run of record / a candidate run hidden from humans until its release (Section 7) |
| Principal | The human on whose behalf an item runs |
| Responders | The group handling the item, together with the principal |
| Proposal | A write call held by the gateway until approved, or only recorded |
| Effect | What a succeeded write does in the target system; it may start later (Section 5.5) |
| Target | The lock key of a write, taken from tool policy |
| Held target | A target that takes no new proposal after a failed, unrunnable or unconfirmed check, until an approver releases it (Section 4.4) |
| Standing refutation | Refuting evidence that blocks convergence (Section 4.3) |
| Challenger | An agent invited only to look for refuting evidence against a converged hypothesis (Section 5.6) |
| Operation id | A unique, never-reused id that a proposal carries on every write attempt (Section 6.4) |
| Release record | The audited record of which agent and skill versions are candidate, current, corrected or yanked (Section 12.3) |
| Yank | Withdrawing a skill version: agents that pin it are not invited until their owners choose another (Section 3.2) |
| Conclusion | The hypothesis a run is scored on (Section 4.3) |
| Cutoff | The moment a conclusion is scored: the earlier of the run's stop and the human `cause_identified` time (Section 13.2) |
| Arm | `visible` or `invisible` (ADR-011), drawn per incident |
