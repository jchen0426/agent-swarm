# Agent Swarm Platform Specification

| | |
|---|---|
| Status | Approved on 2026-10-12. Amended on 2026-10-13: any team that handles incidents may adopt the platform, and none has yet (Sections 1.3, 1.4 and 14). Amended on 2026-10-14: Phase 0 is built before a team adopts the platform (ADR-016; Sections 1.3 and 14); a tool's schema hash covers its whole definition (Section 3.3; Plan 4); the MCP gateway's read path (Sections 3.3, 6.1, 6.2, 9.4 and 15; Plan 5a). Amended on 2026-10-15: the MCP gateway's board tools and record-only proposals (Sections 3.3, 4.2, 4.3, 4.4, 5.2, 5.3, 6.1, 8.1, 9.4 and 15; Plan 5b). Amended on 2026-10-17: the audit chain and its anchor (Sections 2, 3.3, 8.4 and 9.4; Plan 6a). Amended on 2026-10-18: the LLM gateway (Sections 2.2, 5.6, 8.2, 9.3, 9.4, 15 and 16; Plan 6b). Amended on 2026-10-19: the coordinator's core (Sections 2, 4.1, 4.2, 5.2, 5.6, 8.2, 8.4, 9.4, 12.1, 15 and 16; Plan 7a). Amended on 2026-10-21: one process under the lease's holding, its drain and rollbacks, quiet and taking part from the platform's records, failed tool calls, turns the platform could not serve, and openings by key (Sections 5.1, 5.6, 8.2, 15 and 16; Plan 7d-1). |
| Date | 2026-10-11 |
| Normative | Yes. Supersedes the earlier v1 implementation spec. |
| Deployment profiles | Each team keeps the domain content it brings to a deployment in its own deployment profile, outside this repository (Section 0). `2026-10-13-example-deployment-profile.md` is a fictional example. |
| Phase 1 | `2026-10-11-phase-1-write-path-backlog.md` holds the write-path draft and its open issues. It is not normative until it is reviewed after the gate. |
| Decisions | `adr/`. Section 16 lists the ADR changes this spec requires. |

## 0. Conventions

- **MUST**, **MUST NOT**, **SHOULD** and **MAY** are used as in RFC 2119.
- **Defaults.** Bracketed values in this spec, such as [15 min], are defaults that a deployment may change. Section 15 lists every one. Bracketed values in a deployment profile are that deployment's settings.
- **Examples.** They use neutral names such as `release-manager` and `get_release`. Real servers, tools and policies belong in a deployment profile.
- **Litmus test.** Replacing any domain technology, such as a deployment tool, a code host or a metrics system, changes a deployment profile and never this one.
- **Deployment profile.** The domain content one team brings to a deployment: its facts and onboarding answers, MCP servers, agents, work item types, trigger mappings, redaction rules, smoke and red-team content, preregistration draft and checks. Each team keeps its own profile, outside this repository, and a deployment may hold several teams (Section 14.4). The platform supplies none of this content, neither incident history nor runbooks. `2026-10-13-example-deployment-profile.md` shows the shape with a fictional team.
- **Phase 1.** This spec states only the principles the write path must keep (Section 4.4). Its design waits for the gate and the census, because both decide whether and how it is built.

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

The governance reason holds only if teams other than the builders own agents. Section 14.3 therefore asks for a named owner outside the builders by a set date. If none exists by then, the team records its decision: stop, or run Phase 0 as an accuracy test only, with the governance reason untested.

### 1.3 Goal of this phase

The goal is internal value validation on the first deployment, described in its deployment profile:

- **Phase 0:** do agents reach correct conclusions often enough, clearly better than a simple baseline, and record the right actions, safely and at an acceptable cost (Section 13)?
- **Phase 1, if built:** designed after the gate. It must show that acting through the platform shortens the time to act, without more failed or reversed actions.

The platform is meant for any team that handles incidents, and each team joins with its own deployment profile (Section 14.4). As of 2026-10-14 no team has adopted it. The spike and the mechanism check have run (Section 14.1). On 2026-10-14 the author decided to build Phase 0 before a team adopts the platform, on the assumptions ADR-016 records. A team's census, its retrospective check and the commitments of Section 14.3 still come before its measurement window.

### 1.4 Out of scope

- **Multi-tenancy.** ADR-008 keeps one deployment per organization; records still carry a `tenant_id`.
- **Writes without human approval.**
- **Autonomous learning.** Agents keep no memory across work items and never change skills or configuration themselves. Knowledge changes only through reviewed skill versions (Section 3.2).
- **A synthetic evaluation bench as evidence of value.** Faults injected into non-production environments are cleaner than real incidents, so they never count toward the gate. Before any team adopts the platform, a public benchmark of this kind MAY serve the mechanism check (Section 14.1), which tests that the mechanisms work, not that agents add value. A retrospective set built from a team's past incidents is allowed for checks and tuning (Section 12.3), never for the gate.
- **Domains other than incident handling** (ADR-001). Any team that handles incidents may adopt the platform (Section 14.4); other kinds of work are not designed for in this phase.
- **An external policy engine.** Policy rules stay inside the platform, behind a clean boundary between decision and enforcement.
- **Comparing a team with a single agent** (Section 1.2).
- **A challenger role** that gives one agent the job of disagreeing (Section 5.6).

## 2. Architecture

```
 trigger (webhook) --> [coordinator] --creates--> [blackboard] <-- humans (web UI)
                           | invites, budgets          ^
                           v                           | blackboard tools
  [agent sandbox] x N -----MCP--> [MCP gateway] -------+
    agent runtime + skills          | per-agent tool policy
    no credentials                  | write -> recorded proposal (Phase 0)
         |                          +--> registered MCP servers (hold credentials)
         +--> [LLM gateway]         +--> [audit and trajectory store]
```

| Component | Responsibility |
|---|---|
| Coordinator | A deterministic program that never calls a model. It opens work items, correlates alerts, creates runs and their blackboards, invites agents by subscription and paces rounds. It also enforces budgets, convergence and stop rules, assigns ADR-011 arms and starts shadow runs. One instance is active at a time (Section 8.2). |
| Blackboard | One per run, append-only. Its entries are typed, and each has an author and references (Section 4.2). |
| Agent runner | One disposable sandbox per agent session. It loads the agent's skills and runs its agent runtime, which is swappable (default per ADR-006). Egress goes only to the MCP gateway and the LLM gateway, and the sandbox holds no credentials. |
| MCP gateway | The only MCP endpoint agents see, and the only place tool policy is enforced (Section 6). One instance is active at a time (Section 8.2). |
| LLM gateway | The only route to models (ADR-007). It forwards only allowlisted requests (Section 9.3), enforces token budgets per run and attributes cost. |
| Human interface | The platform's web UI: items, live board with suggestions, close-out forms, rulings and administration (Section 10). Notifications go out through configurable channels. |
| Registry and configuration | Agents, skills, the yank list, MCP server registrations with tool policies, work item types, trigger mappings and identity sources, kept in a Git repository that requires review (Section 3). |
| Audit and trajectory store | An append-only, hash-chained record of tool calls with redacted outputs, board entries, rulings, configuration loads, model-call metadata, and redacted prompts and completions. Its chain head is anchored outside the database by the anchor store's owner (Section 9.4). |
| Database | Postgres, the single source of truth for all other state (ADR-004). |

Each agent session gets two short-lived tokens, one for each gateway. Both are scoped to its run and agent and last one round; they expire when the run stops, when the agent leaves the run, or when the instance that issued them loses the lease (Section 8.2). A token is a prefix for its gateway and 43 letters and digits; both gateways redact any token wherever they keep or forward text, and the MCP gateway refuses a read call whose arguments hold one, a read or write call whose arguments hold an escape that may hide one (Section 9.4), or any call that comes after its round's time limit.

### 2.1 Invariants

1. Agent sandboxes hold no credentials for any external system; their only secrets are the two short-lived gateway tokens (Section 2). Every tool call passes through the MCP gateway, the only place tool policy is enforced.
2. A human approves every write, and the approval covers only that exact call. In Phase 0 no write reaches any server (Section 6.3); Section 4.4 states what Phase 1 must keep.
3. No agent can stop, extend or close a run. Runs stop by the coordinator's rules, a human ruling or the kill switch.
4. This specification names no domain technology (Section 0).
5. The platform is never on a trigger's own notification path. When the platform fails, people fall back to their existing process.
6. A shadow run, or any run in shadow mode, never sends a write to an MCP server, and no human sees its output before its release (Section 7).
7. No write tool can change the platform's own configuration, skill sources or deployment, its MCP servers' deployments, or their access and network policies. Registration review verifies this.

### 2.2 Implementation constraints

- **Language.** The platform is written in Go. That covers the coordinator, the MCP gateway, runner control, LLM gateway integration and the human interface backend. The agent runtime inside a sandbox is swappable and may use another language (ADR-006).
- **Domain-driven design.** Code is organized by bounded context, and each context uses the vocabulary of this spec. The proposed contexts below are to be confirmed in the implementation plan:

| Context | Holds | Kind |
|---|---|---|
| Work | Incidents, work items, runs, blackboards, rounds, convergence and stop rules | Core |
| Actions | Tool policy decisions and recorded proposals; in Phase 1 also approvals, locks, effects and execution outcomes | Core |
| Registry | Configuration objects, their versions and the yank list (Section 3) | Supporting |
| Measurement | Arms, finality, shadow-run scoring, rating packets, metrics | Supporting |
| LLM | The LLM gateway's request policy, the models' prices, and the token budgets and daily cap it enforces | Supporting |
| Access | Humans, roles, sign-in, identity sources | Generic |
| Audit | Append-only records | Generic |

- **Interface-driven.** The code follows ports and adapters:
  - Domain and application code depend only on Go interfaces they declare themselves (ports). Infrastructure packages implement them (adapters).
  - Dependencies point inward: infrastructure depends on application, and application on domain. Domain packages import no database, network or framework packages.
  - Ports exist only at real boundaries: persistence, job queue, configuration source, identity provider (sign-in and groups), MCP servers, model gateway, sandboxes, notifications, clock, identifiers, condition evaluation, secrets and the audit anchor. Each interface is small and declared by its consumer.
  - Wiring happens in `cmd/`.
- **Testing.** The fakes in Section 12 (fake MCP servers, scripted agents, a fake clock) are adapters of these same ports. The deterministic test layers therefore need no network or model calls.

## 3. Configuration

All configuration lives in one Git repository, and every change requires review (Section 3.7). The platform loads a reviewed revision. When a work item starts, it locks its configuration: the commit, the content hashes and the runtime image version. It keeps that lock until it closes.

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

- **Version.** An agent's version is the content hash of its file, the skill versions it pins and the tool set its patterns resolve to (ADR-003). A newly registered tool that a pattern would match is therefore an agent change, which follows the rules of Section 12.3. The agent's name points to the current version.
- **Grants.** `tools` narrows tool policy and never widens it. A pattern matches server and tool separately, and `*` matches only within a tool name. Patterns match read tools only; write tools are granted by exact name. No grant may resolve to a tool whose `audience` is `platform` (Section 3.3).

### 3.2 Skill

- **Contents.** A skill is a versioned bundle of instructions, reference files and optional scripts. A version is a snapshot of its source at a pinned commit, wherever people edit that source.
- **Owner.** Every skill has a human owner, who answers for its correctness, as an agent's owner does for the agent.
- **Scripts.** They run only inside the agent's sandbox, which can reach nothing but the two gateways. Their dependencies are part of the runtime image.
- **No tools.** A skill grants no tools. A runbook for an MCP server is a skill.
- **One source for people and agents.** Where people already keep a runbook for a system, its released snapshot SHOULD be the agents' skill. People and agents then share one source, and agents receive its changes through releases. A separate knowledge base for agents would drift away from the people's.
- **Know-how, not facts.** Skills hold durable know-how. Facts that change over time, such as known ongoing problems or change freezes, are read from their systems of record through read tools and never copied into skills.
- **Secrets.** Every release passes a blocking scan for secrets and personal data, and skill content passes the redaction rules (Section 9.4) before it reaches the LLM gateway.

**Versions**
- **Pins.** An agent pins exact skill versions (`release-diagnosis@1.2.0`).
- **Changes.** A new version goes live as Section 12.3 describes.
- **Yank.** The skill's owner or a platform admin MAY yank a version by adding it to the yank list in the configuration repository, a change that needs no other review. A yank removes knowledge; it never restores older content:
  - agents that pin the yanked version are not invited to items that start afterwards, until their owners pin another version; moving back to the version that was current before needs only its review (Section 12.3);
  - items already started keep their locked version, and each gets a notice;
  - during a measurement window, a yank applies to visible-arm items only; the preregistration says what a yank of a skill in the invisible arm's configuration does to the window;
  - the owners of the affected agents are notified.

  Because the yank list lives in Git, a database restore cannot undo a yank.
- **Why fast removal is safe.** Safety is enforced by the gateway, never by skills. A yank can leave an item with fewer agents, but it cannot enable a write or bring back instructions nobody reviewed. So knowledge is slow to add and fast to remove.
- **Later.** One fast-fix mechanism for skill changes, either owner-chosen patch lines or corrections, comes after the gate (Section 14.1).

### 3.3 MCP server registration and tool policy

```yaml
kind: McpServer
metadata: { name: release-manager, owner: platform-team }
spec:
  endpoint: https://release-mcp.internal
  image: "sha256:<pinned digest>"                # a new image is a registration change
  credentialRef: secret://mcp/release-manager    # never reaches a sandbox
  tools:                                         # deny by default: unlisted tools are hidden
    get_release:
      schemaHash: "sha256:<recorded at review>"  # name, description, input and output schemas
      effect: read
      redact: [default-secrets]
      partialWhen: "result.truncated == true"
      bounds: { window: { max: 6h } }            # caps on arguments that drive query cost
    list_recent_changes:
      schemaHash: "sha256:<recorded at review>"
      effect: read
    rollback_release:
      schemaHash: "sha256:<recorded at review>"
      effect: write
      target: [service]                          # every argument here needs candidates
      scope: { service: hypothesis.service }     # must act on the cited hypothesis's service
      candidates:
        service: { tool: catalog.list_services, field: "$[*].name" }
        change_id: { tool: list_recent_changes, args: { service: "{args.service}" }, field: "$[*].id" }
      summary: "Roll back {args.service} by reverting change {args.change_id}"
      facts:
        current_release: { tool: get_release, args: { service: "{args.service}" }, field: "$.current" }
```

| Field | Applies to | Meaning |
|---|---|---|
| `schemaHash` | all | Hash of the tool's whole definition, as the reviewer reads it: name, title, description, input schema, declared output schema and annotations, recorded at review (server pinning, below). |
| `effect` | all | `read` or `write`, set by the platform. Server annotations, such as read-only or destructive hints, are never trusted. |
| `audience` | all | `agents` (default) or `platform`. A `platform` tool serves only the platform's own reads, such as routing, candidates, facts, milestones and identity. It is never listed to agents, and the loader refuses any agent grant that resolves to it. |
| `redact` | all | Named redaction rules. They apply to the output, and to errors, before any agent sees them and before they are stored, besides the rules that apply always (Section 9.4). |
| `partialWhen` | read | A condition on the output. When it holds, the output becomes a read error, so partial or truncated data never becomes evidence. |
| `bounds` | read | Caps on arguments that drive query cost, such as a time range or a result size. The gateway refuses a call beyond them, one that leaves a bounded argument out, and one that writes it in a form with more than one reading: a duration is a positive integer with s, m or h, and a number is a plain number. |
| `target` | write | The arguments that name what the write changes. Every one MUST have candidates. The key is the tuple of matched candidate values, so one real target has one key, and a key covers every longer key that extends it. |
| `scope` | write | Required. Bindings from arguments to the cited hypothesis's key or to the item's trusted routing values. The gateway refuses a call outside them. |
| `candidates` | write | For each listed argument, a read call whose output is the set of allowed values. The gateway rejects values outside the set, and refuses the call if the read fails or returns nothing. |
| `summary` | write | Required. A sentence template over arguments and facts. Screens show it as the action, generated by rules. |
| `facts` | write | Named reads whose fields screens show as platform facts. |

Phase 1 adds fields for approval, execution, effects and checks (Phase 1 backlog).

**Platform reads.** The gateway makes candidate, fact, routing and identity reads under the platform's own identity, and audits them like any other call. The candidate and fact reads an agent's write call makes are paid for by that agent's session: its share of each server, its calls in flight, and its failing fast on a degraded server. Their records name the write call. Only an inbound event's trusted fields and arguments validated against their candidates fill a read's arguments, and a selected value that redaction changed anywhere is not a value.

**Conditions.** All conditions use one sandboxed, side-effect-free expression language, chosen in ADR-015. Values from arguments, the hypothesis and the item are bound as typed variables, never spliced into condition text, and only whole, candidate-validated values are substituted into read arguments.

**Names for agents.** Agents call a tool by its server's name and its own, joined by two underscores, such as `release-manager__get_release`. The loader refuses a grant whose name for agents is longer than 64 characters or holds anything but letters, digits, `_` and `-`.

**Loader rules.** The loader refuses a write tool without `scope` or `summary`, and a `target` that uses an argument without candidates.

**Server pinning.** The gateway hashes each listed tool's definition: its name, title, description, input schema, declared output schema and annotations. It lists to agents only those fields, built from the definition it hashed and never from the server's raw bytes. It hashes and lists the definition as its MCP client decodes it, so every reader sees the same text however the server wrote its JSON, and an agent sees what was reviewed. If a hash differs from the registered `schemaHash`:

- that tool is hidden at once, including from running work items, until the registration is reviewed again: any reviewed change to the registration's file lifts the hide, and the gateway hides the tool again if the server still offers another definition;
- the registration's owner is notified.

For each registration in use, the gateway looks at the server's definitions on a session of its own, with the registration's credential, at most [30 s] apart; only that registration's own looks hide its tools. A look ends after [10 s], so a server that does not answer in time costs agents only that server's tools. A tool is also hidden when its definition holds characters a reader could miss, or when the gateway cannot check its arguments, as with an input schema whose references loop.

Tools that a server adds are hidden anyway, because unlisted tools are denied by default.

**Partial results.** Review of a registration confirms that the server reports partial or truncated results as errors, or that tool policy declares `partialWhen` for each read tool that can return them. A partial result that looks complete is the most misleading evidence there is, because an incomplete answer reads as "nothing found".

**Echoes.** Review of a registration also notes each read tool that repeats its arguments in its output, as a search that answers "no results for …" does. A quote that lies within one of a call's own argument values never counts as evidence (Section 4.2), but an echo inside a larger answer still can, so raters see a call's arguments beside its output. Where an agent's read repeats an argument value that redaction changed, the gateway shows it in the output as the stored arguments do: raters see the same value in both, and the agent cannot quote it as it wrote it.

**Server deployment.** A registration pins the server's image by digest; a new image is a registration change. Its endpoint is https (Section 9.3). Each server holds one least-privilege credential, and its egress is limited to its target system.

**No self-change.** Review of a registration confirms invariant 7: no write tool can reach the platform's configuration, skill sources or deployment, its MCP servers' deployments, or their access and network policies.

### 3.4 Approval policy (Phase 1)

Approval policies decide who may approve a write. They are drafted in the Phase 1 backlog and are not loaded in Phase 0.

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
  limits: { entriesPerRound: 5, openClaims: 2, leaseRounds: 2 }
  convergence: { minContributors: 2, minSources: 2, refutationMargin: 2 }
  milestones: [acknowledged, cause_identified, mitigation_started, recovered]
  autoClose: { after: 30m }                    # after trigger resolution and entry into awaiting_close (Section 4.1)
  measurement: { invisibleShare: 0.5 }          # ADR-011; fixed by preregistration
  shadowRuns:                                   # Section 7
    - name: independent
      arms: [invisible]
      override: { budget: { rounds: 1 }, round: { timeLimit: 15m } }
```

A shadow run's `override` MAY also name a different agent set or runtime, so that an external comparator can run as a shadow run through both gateways (Section 13.3).

### 3.6 Trigger mapping and inbound events

The platform accepts one normalized inbound event. How each source's payload translates into it is configuration.

| Field | Meaning |
|---|---|
| `source` | A registered inbound source |
| `eventId` | The source's id for the event. If the source supplies none, the mapping derives one from trusted fields and the timestamp. |
| `instanceId` | The source's identity for one firing instance, such as one host or region |
| `status` | `firing` or `resolved` |
| `title`, `labels`, `payload` | Content. Untrusted unless the mapping marks a field as trusted. Redacted before storage (Section 9.4). |
| `occurredAt` | Source timestamp: the start time for `firing`, the end time for `resolved` |
| `milestone` | Optional: a milestone name and time for an existing incident (Section 11) |
| `incidentRef` | Optional: an incident id from the deployment's incident source |

```yaml
kind: TriggerMapping
metadata: { name: alerts }
spec:
  source: alerting
  authentication: { hmacSecretRef: secret://inbound/alerting, maxAge: 5m }
  split: "$.events[*]"                           # one request may carry several events
  trustedFields: [labels.rule_id, labels.service, instance, startTime, endTime, status]
  occurredAt: { firing: "{startTime}", resolved: "{endTime}" }
  workItemType: incident
  dedupKey: "{labels.service}:{labels.rule_id}"
  instanceId: "{instance}"
  correlate: { key: "{labels.service}", within: 30m }
  principal: { tool: directory.responsible_for, args: { service: "{labels.service}" }, field: "$.user" }
  responders: { tool: directory.responders_for, args: { service: "{labels.service}" }, field: "$.group" }
  fallbackPrincipal: { group: on-call-primary }  # resolved to the group's current on-call person
  redact: [default-secrets]
```

- **Authentication.** Inbound requests MUST be authenticated. Schemes that sign the request body MUST sign a timestamp, and requests older than `maxAge` [5 min] are refused. Schemes without a signed timestamp, such as bearer tokens or client certificates, rely on transport authentication plus the deduplication and ordering rules below.
- **Trusted fields for ordering and milestones.** `eventId`, `instanceId`, `status`, `occurredAt` and `milestone` MUST come from trusted fields, and milestone events are accepted only from the registered milestone source.
- **Splitting.** A mapping MAY split one request into several events. The request is acknowledged only after all of them are committed, so the source's own retries cover platform failures.
- **Repeats and order.** Sources retry, so events repeat and can arrive out of order.
  - An event whose `eventId` was already seen from the same source is dropped.
  - A `status` applies only if its `occurredAt` is later than that of the last status applied for the same instance; at the same time, `resolved` wins. `occurredAt` is clamped to the receipt time plus [2 min] of skew.
  - An older event is kept on the item as trigger evidence and changes nothing.
- **Correlation.** A new event whose dedup key matches an open item, or whose correlation key matches an item opened within `within` [30 min] that is still open, is appended to that item as trigger evidence instead of opening a new item. An item's trigger resolves only when no instance of any alert appended to it is still firing.
- **Missing routing fields.** An event that lacks a trusted field its routing needs opens no item; it is recorded, and the mapping's owner is notified.
- **Routing uses trusted fields only.** This covers the work item type, the dedup and correlation keys, the principal, the responders, and any hypothesis key value taken from the trigger. Untrusted fields appear as trigger evidence and never route.
- **Diagnosis-only items.** A mapping MAY declare consistency checks between trusted and untrusted fields. If a check fails, the item is diagnosis-only, and write tools are hidden from all of its runs.
- **Principal and responders.** The principal is the human on whose behalf the work item runs; the responders are the group that handles it. Both are resolved through read tools, via the gateway, under the platform's own identity. For an item opened in the UI, the principal is the person who opened it.
- **Identity sources.** A read tool whose output confers authority, such as the principal, the responders or a service-to-path mapping, MUST be registered as an identity source: owned by the identity owners or the system's owners, authenticated, and with its changes audited. Group claims from the identity provider need no such registration.

### 3.7 Configuration governance

| Change | Phase 0 | Before Phase 1 starts |
|---|---|---|
| Any change | Protected branch; schema validation; content hashes recorded; affected owners notified; nobody reviews their own change, except for a yank | Also signed commits |
| An agent | Reviewed by its owner, or by a co-owner or a platform admin when the owner wrote it. A grant that adds a server's tools also needs that server owner's review. | Same |
| A skill version | Reviewed the same way by the skill's owner; scripts are reviewed as code; the release passes the scan of Section 3.2 | Same |
| A yank (Section 3.2) | The skill's owner or a platform admin, alone | Same |
| Server registration, tool policy, trigger mapping, identity source | Reviewed by a platform admin | Two reviewers, at least one of them not a platform builder |

The loader MUST refuse configuration that fails schema validation, references an unregistered server or tool, gives an agent a tool that tool policy does not allow, grants a write tool by pattern or a `platform` tool at all, or breaks a loader rule of Section 3.3. The repository's review rules enforce who must review; the deployment profile says how. Admins change configuration only through this repository.

## 4. Runtime objects

### 4.1 Incidents, work items and runs

An **incident** groups the work items that one underlying problem causes. Most incidents have one item, because correlated alerts join an open item (Section 3.6). A later `incidentRef`, or a responder's link ruling, can join items into one incident. Measurement counts incidents and scores each on its first item (Section 13.2). Milestones and ground-truth records attach to the incident, also after its items close.

A **work item** is one unit of work, opened by a trigger or by a human. It has:

- a type, trigger source, dedup and correlation keys, incident, principal, responders, locked configuration (Section 3) and `tenant_id`;
- an ADR-011 arm, `visible` or `invisible`, taken at open (Section 13.1);
- one primary run and zero or more shadow runs.

An item's routing values, which write tools' scope bindings read (Section 3.3), are recorded as given. There are at most 16, each of at most 256 characters, under a key of 1 to 64 characters; an opening is refused when a redaction rule that applies always would change a key, a value, or the two as the member key=value, as a rule for `password=...` changes the routing `{"password": "..."}`, or when an escape in them may hide what such a rule matches (Section 9.4).

A **run** is one team configuration working the item. Each run has its own blackboard and budget. In the visible arm, humans see the primary run. In the invisible arm, the primary run executes in shadow mode (Section 7).

```
queued --> investigating --> awaiting_close --> closed
               ^                   |               |
               +-------------------+               |   extend budget, or a new firing for the item
                                   ^---------------+   reopen ruling, within [24 h]
```

- **Queued.** An item waits here when the concurrency limit is reached. It locks its configuration when it starts. A responder may promote a queued item, which starts it even if that exceeds the limit by one.
- **Stop reasons.** When the primary run stops, the item moves to `awaiting_close` and records why: `converged`, `quiet`, `budget_exhausted`, `trigger_resolved` or `not_staffed` (kill switch or daily token cap). A run records its own reason too, which may also be `cut`, for a run stopped once the day's spend reached the daily cap; its item's reason is then `not_staffed`. A run with no agent to invite stops `not_staffed`, with a `no_agents` notice on its board; a run whose agents have all left it stops `not_staffed` too, and its stop entry (Section 4.2) shows it.
- **Closing.** A human may close an item from any state, and may reopen a closed item within [24 h]; it returns to `awaiting_close`. An item in `awaiting_close` also closes automatically [30 min] after the later of its trigger's resolution and its most recent entry into `awaiting_close`.
- **On close.** Closing stops a visible primary run. Shadow-mode runs continue to their own end (Section 7).
- **Finality.** An incident is final when all its items are closed, the reopen window has passed, all its runs have stopped, and its cutoff milestones are present or [24 h] have passed since its last item closed. Rating packets and the gate sample use the incident's state at finality.

### 4.2 Blackboard entries

| Type | Written by | Content |
|---|---|---|
| `evidence` | agent, human, platform | A claim with provenance, and optionally a stance on a hypothesis: `supports` or `refutes` |
| `hypothesis` | agent | Key, statement and at least one evidence reference |
| `task` | agent | Title and tags; can be claimed |
| `claim` | agent | A claim on a task, which holds through its lease (below) |
| `completion` | agent | The holder's completion of a task, citing evidence |
| `turn_end` | agent | The end of the agent's turn in a round (Section 5.2) |
| `round` | platform | The start of a round: its number, and in the wrap-up round the converged hypothesis (Section 5.2) |
| `proposal` | gateway | Created when an agent calls a write tool (Section 5.3) |
| `ruling` | human | See rulings, below |
| `notice` | platform | Trigger updates, failures, refused calls, and model calls refused, failed or of unknown outcome, one notice per call |
| `stop` | platform | The run's stop summary (Section 5.6). After it the board takes rulings only |

Entries are append-only. Each carries its author, time, run and references.

**Evidence provenance.** Each evidence entry MUST have exactly one of:

- the id of a gateway read call made by the same agent in the same run, plus a quote that appears verbatim, after whitespace normalization, in that call's stored, redacted output;
- a human author, in the visible arm only;
- a platform record: a trigger event or a platform read.

Quotes are matched against a canonical form of the stored output; for structured output, that is its decoded text, so escaping does not decide whether evidence counts. The platform rejects evidence that breaks these rules. Provenance proves that the call happened and that the quoted text exists. It does not prove the interpretation, which is why raters can open the stored output.

A quote that lies within one of the call's own argument values is the agent's own text, and the platform rejects it too.

**Retraction.** An agent may retract its own earlier evidence with a new entry; the retracted entry stops counting.

**Tasks.** `claim_task` is atomic, so a task has one holder at a time. A claim holds through [2] rounds, its own round included, unless `complete_task` arrives first; an agent whose claim lapsed leaves the task to the others for one round. An agent holds at most [2] open claims. `complete_task` is the holder's, and cites live evidence of any author.

**Entry limit.** An agent posts at most [5] new entries per round: evidence, hypotheses and tasks, and the proposals the gateway records for it. Retractions, claims, completions and turn ends act on entries that were counted, and do not count; neither do the platform's notices.

**Notices.** A notice holds codes, names and ids only, never text an agent or a server wrote. A refused call or a failed read leaves one notice per round, agent, tool, code and rule, and an agent's calls leave at most [10] in a round. An agent sees only its own.

**Rulings.** Humans act on a board only through rulings:

| Ruling | Effect |
|---|---|
| Reject, un-reject | A rejected hypothesis cannot converge until it is un-rejected. |
| Dismiss a refutation | The refutation no longer blocks convergence (Section 4.3). Visible arm only. |
| Extend budget | Adds an amount, by default [1 round and 5 min]. |
| Close, reopen | Section 4.1. |
| Record adoption | Section 6.3. |
| Set a milestone | Section 11. |
| Link or unlink items | Joins items into one incident, or separates them (Section 4.1). |

Phase 1 adds rulings for approvals and effects (Phase 1 backlog).

- **Consequences first.** Each ruling shows its consequences before it is confirmed, for example "completes convergence; the run stops after one more round".
- **Inputs.** Reject, dismiss and link require a preset reason with optional text; extend requires an amount.
- **Refute or reject.** Refute a hypothesis by posting evidence against it; that counts like any other evidence. Reject it when you know it is wrong without posting evidence.
- **Human support.** A human's supporting evidence counts as a contributor. If it would lift a standing refutation, it counts as a dismissal and needs a reason. If it completes convergence, the confirmation says so.
- **Closed items.** Adoption, feedback and milestone rulings stay available on a closed item for [72 h] without reopening it.

### 4.3 Hypothesis keys, convergence and the conclusion

**Keys and merging**
- **Keys.** A work item type MAY define key fields. Each field has a closed value set, either given inline or produced by a read tool when the item opens (and then locked with the configuration). Every field also accepts `other`. A key's service names the service where the cause lies, not the service that alerted.
- **Merging.** Hypotheses with equal keys are the same hypothesis, so the platform merges them. The later one becomes an alias, and its evidence counts for the merged hypothesis. A key containing `other` never merges.

**Terms**
- **Support** is provenance-backed evidence with stance `supports`.
- **Contributor** is an agent or a human.
- **Source** is a (server, tool) pair, or "human observation".
- **Stance.** Only each contributor's latest stance on a hypothesis counts, and retracted evidence does not count. A hypothesis gives its author support only through the author's evidence that does not refute it.

**Converged.** A hypothesis has converged when all of these hold:
- support from at least [2] distinct contributors and at least [2] distinct sources;
- no standing refutation;
- no human has rejected it.

**Standing refutation.**
- A provenance-backed refutation by a human stands until a responder dismisses it.
- A refutation by an agent stands unless a responder dismissed it, or the hypothesis has at least [2] more distinct supporting contributors than distinct refuting contributors.
- In a run of three agents and no humans, such as the invisible arm of the first deployment, one agent's standing refutation therefore blocks convergence unless that agent changes its stance. The conclusion is then the leading hypothesis (below).

**Score.** A hypothesis's score is its distinct supporting contributors minus its distinct refuting contributors. Ties are broken by distinct supporting sources.

**Conclusion.** A run's conclusion, used for scoring in Section 13, is chosen in this order:
1. the highest-scoring converged hypothesis;
2. if none has converged, the highest-scoring hypothesis that no human rejected;
3. if the top two are still tied, the run has no conclusion.

**As of a moment.** Because the coordinator is deterministic and the board append-only, the conclusion can be recomputed as of any moment. Section 13 scores it as of a cutoff.

### 4.4 Proposals and the Phase 1 principles

A proposal records the tool, its schema hash, a hash of the arguments, the target key, the cited hypothesis and the proposer; the gateway writes it, and the write call's audit record keeps the arguments, redacted. In Phase 0 and in shadow runs every proposal is `recorded` (Section 6.3). A proposal Phase 1 executes needs its exact arguments, kept where secrets may be.

Phase 1 adds approval, execution and effect states, target locks and held targets. Its design is drafted in the Phase 1 backlog. Whatever it becomes, it MUST keep these principles:

- an approval covers one exact call, bound to its arguments and the tool's schema hash, and to the rendered change where the tool can preview it;
- the gateway persists its intent before it sends a write, and anything uncertain about a write becomes `unknown` and goes to a human;
- a write may take effect later through someone else's action; until it does, its target stays locked, and the deployment MUST make an abandoned operation unable to take effect;
- checks run after the effect starts, and a failed check holds the target until a human releases it;
- one target has at most one unfinished write across all work items.

## 5. Work item lifecycle

### 5.1 Open

1. An authenticated inbound event arrives, or a human with the right (Section 10.1) opens an item in the UI.
2. If an item that is not closed matches the event's dedup key, or its correlation key within the window, the event is appended to it as trigger evidence, and status changes apply as Section 3.6 describes. No new item is created. A `resolved` event that matches no open item is recorded and opens nothing.
3. Otherwise the coordinator creates the item, takes its arm (Section 13.1), resolves the principal and responders, and creates the primary run and the configured shadow runs.
4. Items opened by a human are visible and outside measurement. If an in-scope invisible-arm item is open for the same service, no new item opens: the request is recorded on that item without reaching its hidden runs, and the person sees that item's row (Section 10.2).
5. Open-time reads (principal, responders, hypothesis-key values) are retried for up to [2 min]. If they still fail, the item opens with a notice, uses the last successful key values, resolves the mapping's fallback group to its current on-call person through an identity source, and is marked degraded. If even that fails, the item has no principal, and its messages go to the responders' channel only. The preregistration says how degraded items count.
6. If [3] items are already investigating, the new item waits in `queued`. If the kill switch is on or the daily token cap has been reached, the item opens with stop reason `not_staffed`.
7. When the item starts, it locks its configuration, and agents whose subscriptions match the work item type are invited, in configuration order, up to [4] per run. Agents that pin a yanked skill version are not invited, except in the invisible arm during a measurement window (Section 3.2).
8. In the visible arm only, one message with a link to the live board goes to the responders' channel (Section 10.3).

**Openings by key.** Until the human interface and its sign-in exist (Section 10), an admin opens items through the admin API, which listens on loopback only and takes a bearer token. An opening carries an `Idempotency-Key` of 1 to 128 printable ASCII characters and a body of at most 64 KiB, both fixed in code.
- **The item's id.** The key names the item for its whole life. The id is a UUID of version 8 whose bits are the first 128 of SHA-256 over the tenant, a zero byte and the key, with the version and variant bits set, so it is never one of the ids the platform makes, which are of version 7.
- **The digest.** The opening records a digest of its request: `sha256:` and the hexadecimal SHA-256 of the canonical JSON of its `principal`, `responders`, `routing` and `type`. That JSON is one object, without whitespace, with every object's keys sorted, `routing`'s included; an absent `routing` or `responders` is written as empty; and strings are escaped as Go's `encoding/json` escapes them, `<`, `>` and `&` included. The title is left out, since redaction may change it.
- **Retries.** A retry with the same key and request gets the item, and moves it on if it is still queued, as when its opening landed and its start did not. The same key with another request is refused. Every build computes the id and the digest this way, so that a retry across an upgrade is still a retry.
- **Refusals.** An opening is refused while the instance does not hold the lease, and for an item a later build holds (Section 8.2). A draining instance, which still holds the lease, refuses it too, and says that the item opens on the instance that leads next. Once the instance has taken the lease, and while it takes over, an opening is refused to be tried again a moment later, as the answer's `Retry-After` says; a new item's opening is also refused, to be tried again, while no configuration revision is current.

### 5.2 Rounds

**Pacing**
- **Rounds.** Runs advance in bulk-synchronous rounds. At the start of each round the coordinator freezes a snapshot by appending a round entry: the snapshot holds every entry before it, and in the wrap-up round the entry names the converged hypothesis (Section 5.6). A post carries the round it was made in, which the board checks against its latest round entry when the post commits. Each active agent receives the entries added since its previous snapshot.
- **Turns.** An agent works until it calls `end_turn` or the round time limit [150 s] passes.
- **Visibility.** Humans see each entry as soon as it is committed. Other agents see it only from the next round. Claims take effect in real time.
- **Round end.** When every active agent has ended its turn, or the time limit has passed, the coordinator works through, in order: lease expiry, convergence, stop conditions, joins, then the next round. Section 5.6 says how a round's end waits for the round's calls and decides.
- **Joins** (after Phase 0, Section 14.1). When a task with a tag appears, agents subscribed to that tag join the run in the next round, up to the team limit.
- **No assignment.** Rounds set the pace; they do not assign work. Each agent chooses its own actions.
- **Sessions.** A session's identity persists across the rounds of its run; its tokens are new each round. Each turn starts from a fresh context: the round's prompt, the agent's skills and the board, read through `read_board` (ADR-006). If a session is lost, the runner starts a new one from the current snapshot. Tool-list changes take effect at the next round boundary.

**Blackboard tools**

The gateway provides these tools:

```
read_board(since)                        # current snapshot plus the caller's own entries
post_evidence(claim, call_id, quote, hypothesis_id?, stance?)    # stance: supports | refutes
retract_evidence(evidence_id)            # the caller's own evidence only
post_hypothesis(key, statement, evidence_ids)                    # at least one evidence
post_task(title, tags)
claim_task(task_id)
complete_task(task_id, evidence_ids)
end_turn()
```

There is no hypothesis without evidence. A guess becomes a task for someone to check.

`read_board` shows the snapshot from the caller's cursor, in pages of at most [64 KiB], with the next cursor; its [50] best hypotheses and its first [50] open tasks; and the caller's own entries since the snapshot, without the positions that would tell how many others posted. A merge into an entry the caller cannot see yet does not name it. A proposal shows its tool, hypothesis and target, never its facts or summary. What agents and people wrote shows as data, never instructions (Section 9.3).

After `end_turn`, an agent posts nothing more in that round. A post made in a round that has ended is refused, never counted in the next round. Every board call is audited, as every call is, and none can be cited as evidence.

**Repeats.** Blackboard tools are idempotent, so a call repeated after a lost response does no harm:
- a post whose author, run and arguments match an existing entry returns that entry, and does not count toward the entry limit; for `post_task` this holds only while the matching task is open;
- `claim_task` on a task the caller already holds, `complete_task` on a task the caller already completed, a repeated `retract_evidence` and a repeated `end_turn` succeed without effect.

A post is a repeat only when posting it again would change nothing: one that would change its author's stance is a new entry.

### 5.3 Proposals

Agents call write tools as their runbooks describe. The gateway adds a required `_hypothesis` argument, the hypothesis the action addresses, to every write tool's schema. It is stored with the proposal and never sent to any server.

The gateway validates the arguments against the tool's input schema, its `candidates` and its `scope`. It then confirms that:
- the cited hypothesis exists and is not rejected;
- the proposer has itself posted supporting evidence for it (Section 9.2).

If every check passes, the gateway reads the proposal's facts, records the proposal, and returns `"recorded"` (Section 6.3).

The board's checks come before any read, and a repeat of the proposer's earlier proposal returns it without reads. Every argument with candidates must be present, a string, number or boolean of at most [200] characters with no character a reader could miss, and equal, as JSON, to one selected value; a refusal never repeats what a candidate read answered. A fact that cannot be read in time, selects nothing, or holds a value a reader could not see whole, is recorded as unavailable; it does not refuse the call. The summary is kept in parts, the policy's text apart from the values; each value shows at most [200] characters and the summary at most [1,000], so display data never refuses an action. The agent learns only the proposal's id: facts may come from tools agents cannot call. The proposal is committed apart from the call's time, so a slow server never loses it. A session's refused write calls are capped at [10] a round.

### 5.4 Approval (Phase 1)

Drafted in the Phase 1 backlog.

### 5.5 Execution, effect and checks (Phase 1)

Drafted in the Phase 1 backlog.

### 5.6 Stop

A run stops when any of these happens:

- **Converged.** A hypothesis converged and one more wrap-up round has finished.
  - The wrap-up round is outside the rounds and wall-clock budgets, with one round time limit of its own. If convergence and budget exhaustion coincide, the run still gets its wrap-up round, and the stop reason is `converged`. During it the run's sessions may spend one round's share of the token budget above the run's spend at its opening. A run has at most [2] wrap-up rounds; a convergence revoked twice gets no third. A wrap-up round stops the run `converged` only if an agent took part in it, as the platform's own records show: the instance ending it saw it, it left none of the failures that keep a round from being quiet (below), and at least one agent has its own turn end on the board in it. A turn end is the agent's `end_turn` call, which the board recorded; a turn that ended in any other way, an interrupted one included, never takes part. A wrap-up round no agent took part in concludes nothing but counts toward the [2], so that the budget stops a run whose runner or model vendor stays out of reach, and with the daily cap reached the run stops `cut`; one that ended by an outage, or whose turns the instance ending it did not see (Section 8.2), concludes nothing and does not count. Counted or not, a run opens at most twice [2] wrap-up rounds in all: once it has, a converged hypothesis gets no more, and the budget and quiet rules decide.
  - In the wrap-up round the gateway allows reads, `end_turn` and only three other things: refuting evidence on the converged hypothesis, retractions, and write calls that cite it.
  - A standing refutation (Section 4.3) revokes convergence. If budget remains, normal rounds resume.
- **Quiet.** A round is quiet only if the platform's own records show that its agents had their chance and found nothing:
  - the coordinator saw its turns: a round a takeover ends after its time limit, or that an instance takes up with less than a third of its time limit left, is never quiet;
  - it left none of the failures below;
  - it produced no entry of an agent or a person but turn ends, no claim, completion or proposal;
  - at least one agent was in it, and every agent of the round has its own turn end on the board in it.

  The failures a round can leave are read from the gateways' records, never from notices that can be dropped: a model call the LLM gateway recorded as refused, failed or of unknown outcome, which the coordinator notices on the board once, in the round it first finds it; a model call of the round still in flight when the wait for them ends (below); and a failed tool call of the round, as Section 8.2 counts them. A model call recorded as failed only after its own round's wait is noticed in the next round, which is then neither quiet nor concluding. A call of an earlier round still in flight holds that round's spend, but no later round's end. How the runner says a turn ended advises only: a turn that waits on a model vendor that hangs is interrupted at the limit, and leaves no turn end, as a runtime that ends its turn without a successful `end_turn` leaves none. That a runtime calls `end_turn` when its model does is a conformance criterion of every runtime (ADR-006), which the platform cannot check from its records.
- **Budget exhausted.** Wall clock, tokens or rounds ran out, or less than a third of a round's time limit of wall clock is left. Rounds and wall clock count the normal rounds that did not end by an outage (Section 8.2); a normal round's time limit is cut to the wall clock left.
- **Trigger resolved.** A visible primary run stops at the end of the current round. Shadow-mode runs, including the invisible-arm primary run, ignore trigger resolution; it is recorded on the item only.

**A round's end.** Once every turn ended, or the round's time limit passed, the coordinator waits [5 s] for the round's write calls, then up to [30 s] for the round's model calls in flight. It notices each model call of the run refused, failed or of unknown outcome once on the board, and reads the round's failed tool calls. The round's end records the evidence its decision read: the agents with a turn end, the failed model calls and failed tool calls counted, and whether the round's model calls settled. It records how the runner said each turn ended beside it, so that where the two disagree, both can be seen. It decides in this order: a wrap-up round whose hypothesis is still converged, and in which an agent took part (above), stops the run `converged`; the daily cap reached stops it `cut`; a resolved trigger; no agent left, `not_staffed`; a converged hypothesis gets its wrap-up round, while the run has wrap-up rounds left (above); an exhausted budget; a quiet round; otherwise the next round. It records each decision before it acts on it.

When a run stops, the coordinator posts a summary generated by rules, not by a model: hypotheses by score, the conclusion, open tasks, proposals, the stop reason and the budget used.

**Budget.** Budgets cover agent work only. The token budget counts cost-weighted tokens: what a call costs at its model's prices, cache writes and reads included, at the price of one reference token, so that a budget bounds money whichever model an agent uses. The reference token is the price of one token at which spend is counted, which the deployment sets (Section 15). A call counts at its worst case until its cost is known. A run passes its budget, and the day its cap, by one model call at most: the LLM gateway admits one call at a time, up to the commit of its record, so each admission counts every call admitted before it, while each call's estimate covers its outcome. The default is set from the retrospective check and smoke runs before preregistration (Section 14.1). Each turn starts from a fresh context (Section 5.2).

**No challenger in this phase.** Agents that share a model agree easily, and a challenger role would give one agent the job of disagreeing. It is out of this phase because no data path could evaluate it in time. If the false-convergence guardrail shows a problem, the first step is to replay recorded boards under stricter convergence settings (Section 12.3). A challenger is built only if that fails, and only with a data source that can evaluate it.

### 5.7 Close and label

- **Closing.** An item closes by a human ruling or automatically (Section 4.1).
- **Labels.** Ground truth comes from the deployment's ground-truth record. Raters turn it into a label without seeing agent output (Section 13.4); responders never label during the work.

## 6. MCP gateway

### 6.1 Agent-facing endpoint

- **One endpoint.** The gateway exposes a single MCP endpoint. Each session authenticates with its gateway token (Section 2).
- **Tool list.** `tools/list` returns the blackboard tools plus every tool allowed by both the agent's `tools` patterns and tool policy, never a `platform` tool. Write tools are extended as in Section 5.3, and hidden in diagnosis-only items (Section 3.6) and when their input schema could refuse the added argument. A hidden tool cannot be called, even by name.
- **Answers.** The endpoint answers only `tools/list` and `tools/call`, besides the protocol's own initialize and ping. A session has at most [4] calls in flight, of every kind, [2] of them to one server; the reads a write call makes count only among its calls to their server.
- **Wrap-up round.** The tool list and argument rules narrow as Section 5.6 describes.

### 6.2 Read path

1. Check tool policy, including `bounds`. Reject the call if it is not allowed.
2. Check the server's limits.
   - At most [10] calls run at once per server, unless the load trial before the window sets another value (Section 12.1).
   - Comparison and probation runs may use at most [50%] of them. The primary run of either arm is never limited as a shadow run.
   - Platform reads (candidates, facts, routing and identity) have reserved capacity.
   - After [3] consecutive calls that end in connection failures, a server is marked degraded for [60 s]: agent calls to it fail fast, and comparison and probation runs pause. A call counts once, after its retries; a slow answer, an answer too large to read or a refused credential does not count. Platform reads are exempt from failing fast.
3. Write the audit record: call id, run, agent, tool and arguments. If the audit write fails, reject the call.
4. Forward the call to the server with the server's credentials. Connection failures are retried [2] times with backoff, within the round time limit and only while the server is not degraded: reads have no side effects, but they do add load.
5. Read the output's canonical form: the structured content of a tool that declares an output schema, and the text of any other. Apply `partialWhen` to it; a match, or a condition that cannot be evaluated, turns the output into a read error. Redact the output or error, store it, and only then return it to the agent with the call id.

A read error goes back to the agent and into the audit record. It cannot be cited as evidence.

### 6.3 Record-only mode (Phase 0)

In Phase 0 and in every shadow run, a write call ends as `recorded`, and no write reaches any server. The gateway applies the checks of Section 5.3. Recorded proposals take no lock and never notify anyone.

**Suggestions.** In the visible arm, the primary run's recorded proposals appear as suggestions on the board, under the hypothesis they cite (Section 10.2):
- each shows the tool's `summary` and `facts`, generated by rules, and the live status of the cited hypothesis;
- a suggestion whose scoped service differs from the item's routing service is marked "outside this item's service";
- suggestions under one hypothesis are grouped by target;
- a suggestion whose hypothesis was rejected or lost convergence is marked superseded, and can be adopted only after a warning.

**Adoption.** For each suggestion, a responder may answer:
- used as suggested, or used with changes: with when it was applied (Section 10.4) and, optionally, a reference to the change made;
- not used: with a preset reason (wrong cause, too risky, already done, not needed).

The answers are reported as adoption (Section 13.2). They never enter rating packets, and the platform runs no checks for them, because it cannot tell what the person actually changed.

### 6.4 Execution (Phase 1)

Drafted in the Phase 1 backlog.

## 7. Shadow mode

A shadow run is a candidate configuration working a live item. It:

- is hidden from humans until its release (below), and sends no notifications;
- ends every write call it makes as `recorded`;
- receives no human entries, and its board gets no human-operated events such as milestones, incident updates or trigger resolution;
- keeps running when the trigger resolves or the item closes, until convergence, quiet or budget, so its time to a correct conclusion is observed rather than cut off;
- is scored blind against the same labels as the primary run (Section 13.4).

**Release.** Shadow-mode output, including the invisible-arm primary run's board, becomes visible when the incident's label is imported or, for an incident that will not be labeled, [14 days] after its last item closes. Until then no view shows it, version history included. Whether an incident will be labeled is decided at its finality, before any release. People who write the ground-truth record must not see agent conclusions first.

**Uses**
- **ADR-011 invisible arm.** The primary run executes in shadow mode.
- **Comparisons.** These are listed in `shadowRuns` (Section 13.3), including external comparators, and scored on invisible-arm items only, where no human input reaches any run.
- **Probation for changes,** after the gate (Section 12.3). A shadow run on a visible-arm item is still hidden and receives no human input. Its labels may lean toward the current primary run's conclusions, which rewards candidates that repeat the primary's mistakes and penalizes those that correct them. Changes meant to correct systematic errors are therefore judged on invisible-arm items, or on disagreement items labeled by two raters.

**Budget and cuts.** Each shadow run has its own budget, by default equal to the primary run's. Shadow runs count toward the daily token cap. Comparison and probation runs are cut first, once spend reaches 80% of the cap, fixed in code. The primary run of either arm is never cut before the hard cap. Each cut is recorded, and the preregistration says how cut and `not_staffed` items count.

## 8. Failure handling

### 8.1 Principles

1. **Doing too little beats doing too much.** Anything uncertain about a write goes to a human (Section 4.4).
2. **The platform never degrades responders' own tools.** It only subscribes to triggers, and when it fails, people fall back to their existing process. Its read load is bounded (Section 6.2).
3. **Nothing fails silently.** Every failure is audited and shown as a `notice`: on the board in the visible arm, and on the item record in the invisible arm. In the invisible arm, notices of agents' refused calls and failed reads stay on the run's board, which no person sees before its release (Section 7).

### 8.2 Failure table

| Failure | Platform behavior |
|---|---|
| An agent session or sandbox crashes | The turn counts as ended. The next round restarts the session in a new sandbox and rebuilds its context from the board. After [2] consecutive failures, the agent leaves the run. Only crashes count: a turn the LLM gateway refused, the model vendor failed or the platform could not serve does not, and ends a run of crashes; a turn the runner could not start does not count either, and leaves the count as it was. A turn the platform could not serve is `unavailable`, not a crash: a gateway failed on its own account or found its holding no longer valid, a reply came without the gateway's mark of its kind, or no answer came at all; so is a turn whose spec the runner could not deliver. A round whose every turn failed at the vendor, or was `unavailable`, waits for its time limit. A turn that could not start, as when its launch failed, is tried again while its round lasts. One started with less than a third of its round's time limit left counts as one that could not start if it still runs at the limit, or if the runner reports it interrupted; so does a turn the runner reports interrupted whose start the coordinator never learned of. The runner keeps each turn's output in a file of its own, of at most [1 MiB], so that a turn that writes much never fails for it. |
| The round time limit is reached | The agent is interrupted: the runner kills its turn at the limit, and the runtime stops at its own deadline, [2 s] later, as a backstop. Committed entries stay; results of reads still in flight are discarded. A turn makes at most 50 model calls, fixed in code, each asking for at most [4,096] tokens: the deployment sets that number, up to the LLM gateway's cap, so that the longest answer fits in a round, and in the call timeout, on its agents' slowest model. |
| A model call fails or is rate-limited | The LLM gateway retries transient errors with backoff, waiting 1 s, 2 s and 4 s between attempts, fixed in code, within the round time limit; a model call has no side effects. If the errors persist, the agent's turn ends and the next round tries again. The wall-clock budget keeps running. A round whose every turn failed at the vendor ends at its time limit, never earlier. A round in which a model call was noticed as refused, failed or of unknown outcome, or whose own model calls were still in flight when the wait for them ended, as on a vendor that hangs, is never quiet, and as a wrap-up round concludes nothing (Section 5.6). |
| A tool call fails | A read's connection failures are retried [2] times while the server is not degraded (Section 6.2). Other errors, and failures that persist, go back to the agent, redacted, and are audited. They cannot be cited. Each failed call's outcome records the side its failure was on. Failures on the server's side include: for a read, a connection failure, a degraded server failing fast, an upstream timeout, an HTTP 5xx, recorded as "<server> could not be reached", a JSON-RPC error but invalid parameters, the server's slots full until the call's time ends, a tool needing input, an output or a tool's error that the gateway cannot read, check or redact, a look at the server that runs out of time or whose answer is too large, and a call to a hidden tool; for a listing, the server out of reach when a session first lists its tools; for a write, a look at its server that fails, and a candidate read it needs that fails on the server's side, which refuses it. Failures on the platform's side include: a reload of the hidden tools that fails; for a write, the board out of reach when the write is checked, its session not checked again, its proposal, or the proposal's summary, not recorded, a candidate read it needs that fails on the platform's side, which refuses it, and candidates whose order cannot be made; and for a board call, the board out of reach. A tool's own error (`isError`) is the tool's side: it is how MCP servers report a backend's outage as well as bad arguments. Failures on the agent's side include an output partial or too large ("narrow the query"), arguments the tool's schema or bounds refuse or the server finds invalid, a tool not in the agent's list, the session's calls in flight at their cap, a write that its policy, its scope or the cap on refused writes refused, and a candidate read that selects nothing. A failure on the server's or the platform's side, or a tool's own error, in a read, a write, a board call or a listing, leaves its round not quiet, and a wrap-up round concluding nothing, unless a later call of the same tool by the same agent in the round succeeded. The agent's own failures, and calls cut by the round's end or the run's stop, never count; a call of the round still without an outcome when the round's end reads them counts as the platform's. A server that cannot be reached when a session first lists its tools has its tools left out of that session's list for the round (Section 6.1), and the gateway records the listing as a failed call on the server's side. |
| A server keeps failing or slows down | It is marked degraded: agent calls fail fast, and comparison and probation runs pause until it recovers (Section 6.2). |
| A blackboard call's response is lost | The agent repeats the call, and the repeat returns the original result (Section 5.2). |
| A listed tool's schema hash changes | That tool is hidden at once, including in running items, and the owner is notified. |
| Open-time reads fail | The item opens degraded (Section 5.1). |
| The coordinator crashes | All state is in the database: every decision is a record written before the coordinator acts on it. On restart the coordinator carries each item on from its records: a round in progress gets fresh sessions and turns; a round whose time limit passed while no instance held the lease ends by an outage, which counts toward no budget and is never quiet; one whose time limit passed while an instance held the lease ends by its time limit, and is never quiet, since the instance ending it did not see its turns. Nor does an instance see the turns of a round it takes up with less than a third of its time limit left: the round's agents use the time left, but the round is never quiet (Section 5.6). At each takeover, once the schema is checked (below), every model call without an outcome that began before the holding gets one: `unknown`, priced at its stored estimate as a lost call is, with a reason that names who stopped first, another instance by its name or this instance's earlier holding. A takeover that fails to read an item's records gives the lease up and tries to take it again. An item whose work records, or whose run's board events, are of a version this build cannot read, as a later build may write before a rollback, is skipped: it is logged once, counted in the instance's status, answered as held by a later build, and left alone, while the other items are carried on. A step that meets such a record during a holding ends the run's sessions, stops its turns, cancels its timers, and leaves the item alone until the next takeover. A skipped item stays held until a build that can read it leads, so a rollback must be rolled forward. An item whose records are corrupt holds up neither the takeover nor the other items: its steps fail, and are tried again every [5 s]. |
| Two coordinator or two gateway instances run at once, for example during a deploy | One lease covers the coordinator and both gateways, which run in one process. Only its holder acts, and sessions exist only while it holds the lease. Every guarded write carries the epoch of the holding it acts for, and checks it in its own transaction: the work records, the board's events, and the tool calls and model calls with their contents and outcomes. A takeover waits for the writes in flight, and fences the rest. Hides, which only narrow what agents may call, and configuration revisions are not guarded. Before each turn's start, and before every request to the model vendor or an MCP server, retries included, the instance checks its holding, without a lock: its epoch must be current, and its last good renewal, or its take, sent less than the lease's time less its margin ago, by the larger of the monotonic and the wall time elapsed, so that a host that slept counts its sleep. A refusal never fences: the turn is tried again, or the call is answered as one the platform could not serve. Upstream sessions are closed only while the holding is valid, and dropped otherwise. The holder renews the lease from when each renewal is sent, and fences itself, stopping its turns and ending its sessions, once a renewal finds the lease lost, once a write of the coordinator finds it gone, or once its last renewal that succeeded is too old for the holding to be sure; what an earlier holding finds changes nothing once the instance holds the lease again. The fence stops the coordinator acting, not the gateways' writes of outcomes already under way: until the holding expires, the database still takes them, so the margin protects the next holding, whose take waits for them. Every transaction of the instance ends within [10 s], and so does one left idle for [10 s], each at most the lease's time less its margin; a take waits at most [5 s] for the lease, and one that waits longer has not taken it. A take ends within that wait and the lease's renewal period, which together stay below the lease's time, whatever the database answers; the instance's pools ping a connection that has idled within 1 s, fixed in code, and end each connect, to each of the host's addresses, within the renewal period, unless the database's URL sets a `connect_timeout` of its own, so that a database whose address drops its packets, rather than refusing them, holds up no take, nor a pool's slot, once it is back. A takeover comes at most the lease's time, a wait to take it again and one take after the holder goes silent. Records and the coordinator's deadlines are in the database's time, which the instance estimates from samples of it, the one with the shortest round trip in the last minute, and never sets back; the leader's watch is on the monotonic clock, and the gateways' deadlines on the process's. Server limits and degraded marks stay in the holder's memory. The other instance waits. |
| An instance is stopped, for example for a deploy | A stop signal drains it under the lease it holds. The admin API takes no new work: once the coordinator is fenced, an opening answers that the instance is draining, and that the item opens on the instance that leads next, while its reads, `/healthz` and `/readyz`, which answers `draining`, go on until the listeners close. The leader fences its coordinator but keeps its holding, its renewals and its watch, and takes the lease no more. The MCP gateway takes no new call and finishes the writes of its calls within three times its record timeout; the LLM gateway drains within the drain bound [30 s], and the upstream MCP sessions close beside it, under the holding. Then the lease is released, and the listeners and the database's pools close, each within a bound. A drain whose holding is lost ends at once, releasing nothing. A standby takes over within the MCP gateway's drain, the drain bound, a wait to take the lease and one take. A second signal kills every turn and exits at once: the holding then expires, and the next takeover gives its calls outcomes, within the lease's time, a wait to take it and one take. |
| A deploy rolls back to an earlier build | Migrations only add to the schema, and each declares the oldest build that can read the schema it leaves, its `min_reader`, by default its own level. A migration's level is its number: its file is named `NNNN_name.sql`, and so is its row in `schema_migrations`. A build's level is its last migration's, and it runs on a schema whose level is at least its own and whose largest `min_reader` is at most its own. Only a migration whose every statement an older build can ignore, a column that is nullable or has a constant default on a table no audit trigger chains, such a table, or an index that is not unique, may declare an older reader. The instance checks the schema at start, at each take and at each renewal: a schema behind it, or one it cannot run on, refuses its start, and stops a leader for good, which fences, releases the lease and exits without draining, since it must not write into a schema it cannot run on. A check that cannot read the schema, as with a row whose name is no migration's file name, refuses a start, and makes a take give the lease up and try again; a leader that meets it at a renewal logs it and leads on, since a check that failed is no incompatible schema. The migration command waits at most [5 s] for each lock, and refuses a migration whose `min_reader` is above the schema's level, as the run found it, while a holding is live, unless forced. For a migration that older builds cannot read, stop them, then migrate, then start the new build. An older build skips the items it cannot read (above). A compatibility test runs the previous release's scenarios against a schema the new build migrated, then has the new build verify the chain and read the items they left. It checks the promise that the previous release can read that schema: when the schema's `min_reader` is above the previous release's level, the new build made no such promise, so the test says so, naming both levels and the deploy order above, and passes without the scenarios. |
| A configuration revision cannot run | The instance refreshes its configuration every [1 min]. A revision becomes current only if every agent's model has a price card and every skill passes the LLM gateway's checks as a system prompt; otherwise it is refused and logged, the current one stays, and the next refresh tries again. Running items keep the revision they locked. A commit the instance's repository lacks fails its step, which is tried again, and a gateway call that cannot read its revision is answered as one the platform could not serve: keeping the repository current is the deployment's job. Until a revision is current, a new item's opening is refused, to be tried again (Section 5.1). |
| The MCP gateway crashes or restarts | It runs in one process with the coordinator and the LLM gateway, so they stop together, and their turns with them. Calls in flight are left without an outcome: the next takeover gives the model calls theirs (above), and a round's end counts each tool call of the round still without one as the platform's failure. The lease passes to another instance once it expires. |
| The database or audit store is unavailable | The gateway rejects every call, because it audits before it calls. The platform stops; responders are unaffected. The holder's renewals fail, so it fences itself before its lease could expire; once the database is back, it takes the lease again and carries its items on. |
| A client holds a connection open | Each listener ends a request whose header has not arrived within 10 s, or that it has not read whole within 30 s, both fixed in code. |
| A notification fails | The platform retries, then uses the fallback channel, then flags the failure in the live view. |
| The trigger flaps | A new firing for an item in `awaiting_close` sends it back to `investigating` with the remaining budget. After the item has closed, a new item is opened and linked to the previous one. |
| Inbound events repeat or arrive out of order | Repeats are dropped by event id. An event older than the last applied status changes nothing (Section 3.6). |
| Costs run away | The LLM gateway holds each run to its token budget: it admits one model call at a time, so a run passes its budget, and the day its cap, by one call at most (Section 5.6). Comparison and probation runs are cut first (Section 7). Once the daily cap is reached, new items are not staffed. The coordinator and the LLM gateway count one daily cap, over the calls admitted since the database's UTC midnight. |
| Sensitive data appears in any input | It is redacted before any agent, model, store or notification sees it (Section 9.4). |

### 8.3 Kill switch

A control that at once stops staffing new items and cancels running sessions, with a notice on each board.

- **Where.** It sits in one fixed place, reachable from every board and from the items view.
- **Turning it on.** It shows its consequences ("stops N runs on M items") and asks for a reason, but needs no fresh authentication, so stopping stays fast. Responders and approvers may turn it on.
- **Turning it off.** Only an admin may, with fresh authentication.
- **Messages.** The admins and the responders of every open visible-arm item get a message when it is turned on, and again when it is turned off, listing the items that stayed unstaffed.

Phase 1 adds what the kill switch does to writes (Phase 1 backlog). If the platform itself may be compromised, the deployment profile gives the deployment-specific steps.

### 8.4 Database restore

In Phase 0 a restore loses the entries written after the backup. Incidents that lost entries are excluded from measurement and reported; the anchored audit chain (Section 9.4) shows what was lost, and names the runs and incidents that lost records. The yank list lives in Git and survives the restore (Section 3.2). Phase 1 adds the restore procedure for writes (Phase 1 backlog).

## 9. Security

### 9.1 Threats and controls

| Threat | Main controls |
|---|---|
| Instructions injected through tool output (logs, tickets, trigger text) | Least privilege per agent; argument candidates and scope; values bound as typed variables, never spliced into conditions; data boundaries (Section 9.3); in Phase 1, human approval of exact calls |
| Suggestions that mislead responders, possibly steered by injected text | Candidates and scope; summaries and facts generated by rules; the "outside this item's service" mark; model text marked as model-written (Sections 6.3 and 10.2) |
| Content that attacks the human interface: markup or script in logs, trigger fields or model text | Everything is rendered as inert text with contextual encoding under a strict content security policy. State-changing requests carry cross-site request forgery protection. Turning the kill switch off and every admin action require fresh authentication; in Phase 1, so do approvals. |
| Contagion between agents through the board | A proposer must hold its own evidence; convergence needs independent sources; entry limits |
| Poisoned knowledge: injected text kept as a lasting lesson that every agent then reads | Agents keep no memory across items (Section 1.4); skills change only through reviewed versions with an owner, and nobody reviews their own change (Section 3.7); the yank list removes at once (Section 3.2) |
| Fabricated evidence | Provenance and the verbatim quote check |
| Forged trigger fields, or replayed inbound requests | Authenticated inbound events; routing, ordering and milestones from trusted fields only (Section 3.6) |
| Authorization data supplied by an untrusted server | Identity sources only (Section 3.6) |
| Tool poisoning, or silent changes to an MCP server's tools | Deny by default; per-tool schema hashes; server annotations not trusted |
| A compromised MCP server, which holds production credentials | Images pinned by digest; one least-privilege credential per server; egress limited to its target system (Section 3.3) |
| Credential theft or data exfiltration from a sandbox | No credentials in sandboxes; egress only to the two gateways; the LLM gateway's allowlist (Section 9.3); redaction |
| People reading data through agents that they could not read themselves | In Phase 0, everyone who can view a board is entitled to all the data its item's agents can read (Section 10.1) |
| An insider changing configuration | Reviewed configuration (Section 3.7); owners notified; audit; invariant 7 |
| Data sent to the model vendor | All model calls go through the LLM gateway; redaction happens before the model sees anything, skill content included (Section 9.4); vendor terms (ADR-007) |
| Unbounded copies of sensitive data | Retention classes, which cover exports and backups (Section 9.4) |

### 9.2 Deterministic rules

1. Evidence carries a verbatim quote of the output it cites (Section 4.2).
2. Convergence needs [2] contributors and [2] sources, and no standing refutation (Section 4.3).
3. A proposer must itself have posted supporting evidence for the hypothesis it cites. Without this rule, an agent whose context was injected could steer another agent that holds write tools, getting around least privilege.
4. An agent posts at most [5] new entries per round and holds at most [2] open claims.
5. Write arguments must fall within their declared candidates and scope (Section 3.3).

### 9.3 Data boundaries, sandboxes, gateways and secrets

- **Data boundaries.** Board entries and tool outputs given to an agent are wrapped in explicit delimiters, labeled with author and provenance, and marked as data rather than instructions. This lowers injection risk; it does not remove it.
- **Sandboxes.** Runtime built-in tools that reach outside the sandbox are disabled, and host or workspace configuration never enters prompts (ADR-006).
- **LLM gateway allowlist.** The LLM gateway checks the whole request and rejects, and audits, anything outside this list:
  - the model named in the agent's configuration;
  - tool definitions that the client executes, and no tool type that the vendor executes, such as web fetch, code execution or remote connectors;
  - content blocks with inline sources only, never a source the vendor fetches by URL;
  - the permitted endpoints, top-level fields and feature headers, and no stateful vendor APIs such as file or batch storage.

  In Phase 0 the list is what the platform's runtime sends; the thinking blocks of the model's own answers come back unchanged, and only to the session that got them; a name, or an id the gateway did not give the session, that holds what a redaction rule hides is refused, and so is a request in which an escape hides what a rule matches. It logs prompts and completions, redacted, only into the audit store (Section 9.4), and returns a completion to the agent as the vendor sent it; an answer whose record would hide what a rule matches behind an escape is withheld.
- **Secrets.** Each component resolves only its own secret references, at the time of use, from the secret store: the inbound handler its inbound secrets, the MCP gateway the server credentials, the LLM gateway the vendor key.
  - Secrets never appear in configuration, the database, audit records or logs.
  - Rotation needs no downtime; an inbound source has two valid secrets during rollover.
- **Transport.** Every hop is authenticated and encrypted: sandbox to gateways, gateways to servers and vendor, and inbound sources to the platform.

### 9.4 Redaction and retention

- **Redaction.** Everything that reaches an agent, the LLM gateway, a store or a notification first passes the deployment's redaction rules: tool outputs and tool errors, inbound event content, human entries, what agents write for the board, and skill content. A model's own completion is the exception: it reaches the agent as the vendor sent it, and is stored redacted (Section 9.3). Text an agent writes for the board with a character a reader could miss is refused, and so is a quote that the rules would change: a quote must match its output as written. A quote with what a rule put in place of hidden text inside it, between text on either side, is refused too: the hidden text could change what the quote says. The deployment marks the rules that apply always. They apply to everything the MCP gateway stores or shows, besides the rules a tool names, and a configuration that registers an MCP server must have one. Patterns match decoded text: a pattern written for `password=...` also finds a member written as `"password": "..."`, in JSON and in other text, and output in which an escape could hide a match is a read error, never shown. The MCP gateway refuses a read or write call whose arguments hold such an escape, and records no arguments, tool name or reason that holds one. A member whose value is already a rule's replacement is shown all the same: the replacement holds nothing to hide, so the member is not checked as its key and value together, and its key is checked on its own. The platform's own operational logs never contain tool outputs, error bodies or inbound payloads.
- **Retention.** All stored data is encrypted at rest.

| Data | Kept for |
|---|---|
| Ruling, configuration, access and model-call records, without output content | [1 year] |
| Measurement items: the rating-packet snapshot of run event logs and cited redacted outputs, taken at finality | Until [30 days] after the label is imported, or [30 days] after finality for an incident that is not labeled |
| Exported rating packets | Until the rater's label import. They sit in a per-rater location with access control, and every export and import is logged. |
| Database backups | As long as the data classes they hold, and no longer than [35 days] for content |
| Everything else: raw outputs, trajectories, redacted prompts and completions, and the boards and event logs of items outside measurement | [30 days] |

- **Replay and fixtures.** Replay covers the items still within retention. Recorded items become smoke fixtures only after a redaction review.
- **Audit integrity.** Audit records are hash-chained: each row of an audited table is chained, in the transaction that inserts it, after the row chained before it, and in Phase 0 no record is ever deleted. Every [1 h] the chain head is anchored in a store outside the database that the platform's operators cannot write, owned by someone other than the builders. The owner takes the anchors itself, with its own credential for the store and a database role that can only read; the platform holds no credential for the store. An anchor holds the chain's head, the latest earlier anchor the chain still holds, the runs and incidents whose records the chain gained since then (a work record's scope is its incident), as many as it can name, with a count of the rest, the rows each audited table held, and the tables whose state could not be trusted then. The owner verifies the chain against the rows and the anchors, so a change, a deletion or a restore shows, and a restore names the runs and incidents that lost records. Phase 1 adds a full external audit sink (Phase 1 backlog).

## 10. Human interface

### 10.1 Roles

| Role | Can |
|---|---|
| Viewer | See boards of visible-arm items, under the data rule below |
| Responder (the item's principal and responders) | Open items for their services; post evidence; issue the rulings of Section 4.2; promote a queued item; turn the kill switch on |
| Agent owner | Change their agent's configuration (Section 3.7); see its cost, metrics and version history |
| Skill owner | Review, release and yank their skill's versions (Section 3.2); see the skill's version history |
| Rater | Export their own rating packets and import their labels (Section 13.4). A rater MUST NOT be a platform builder. |
| Admin | Use the kill switch, including turning it off; operate the platform. Admins change configuration only through the repository (Section 3.7). |

Phase 1 adds approvers (Phase 1 backlog).

- **Data access.** In Phase 0, everyone who can view a board must be entitled to all the data its item's agents can read, and roles are sized to match; per-service filtering is later work. Raw outputs are visible only to the item's responders and raters.
- **Sign-in** uses the organization's single sign-on (deployment profile).

### 10.2 Views

- **Items.** Grouped by incident. Each row shows service, title, principal, milestones and a state.
  - Visible-arm rows link to their board.
  - Invisible-arm rows show fixed copy: "Agents are working on this alert for measurement. Their output stays hidden until it is rated. Handle it with your usual process." Their state shows only queued, open (investigating or awaiting close) or closed. They offer close, reopen, link or unlink, and set milestone; none of these reaches the hidden runs. After release (Section 7), they link to their board.
  - From here a responder also opens an item or promotes a queued one.
- **Live board.**
  - A header generated by rules and refreshed as entries commit: the item state and stop reason; the conclusion with its status; the budget used; and, in Phase 0, the number of suggestions.
  - An incident strip, when the incident has other items: their state and conclusion, flagged when they disagree.
  - Hypotheses ranked by score. Each shows its key and up to [2] quotes per side, chosen by a fixed rule (one per distinct source; platform evidence first, then human, then agent; newest first), with a count of the rest. Its suggestions sit beneath it (Section 6.3).
  - Ruled-out hypotheses: rejected, or with a standing refutation.
  - Last and collapsed, the chronological entry feed with authors and provenance links.
  - A connection indicator: "live", or "reconnecting, last updated at T".
- **Model text.** Hypothesis statements and evidence claims are model-written. They appear as plain text marked "model-written"; keys, quotes, counts, summaries and facts are platform facts.
- **Status words.** One set on every surface: converged, confirming (in the wrap-up round), leading, trailing, refuted (a standing refutation) and rejected.
- **Version history** (Section 14.1). For each agent and skill version, the closed items it ran on, with their conclusions and labels. Invisible-arm conclusions appear only after their release (Section 7).

Phase 1 adds the approval inbox and target-lock views (Phase 1 backlog).

**Board states.** The run state renders in the header. Lasting conditions (degraded, diagnosis-only, kill switch on) render as a banner above it.

| State | Board text | Message |
|---|---|---|
| Queued | Its position, and what promoting it does | No |
| Investigating, nothing yet | "Agents are reading. A message comes when the run stops, by T at the latest." | No |
| Converged, confirming | The conclusion, marked "confirming until T" | No |
| Converged | The conclusion | Yes, when the run stops |
| Stopped without converging | The leading hypothesis, if any, marked "not converged" | Yes |
| Trigger resolved | "Alert resolved", with the stop summary | No |
| Not staffed | "Not staffed: handle with your usual process" | Yes |
| Awaiting close | The stop summary, and what stays open until when | No |
| Closed | The close-out form while it is open (Section 10.3) | No |
| Reopened after a flap | A divider with the new firing time | No |
| Late trigger event | "Received late, not applied" | No |
| Banner: degraded | Which open-time read failed | No |
| Banner: diagnosis-only | Which consistency check failed; no suggestions | No |
| Banner: kill switch on | "Stopped by the kill switch" | Yes |

### 10.3 Notifications

- **Visible arm.** One message when the item opens, with a link to the board. After that, messages go out only for:
  - convergence, sent when the run stops;
  - a run that stopped without converging;
  - `not_staffed`;
  - the kill switch, on and off (Section 8.3);
  - the close-out and reversal messages below.

  Everything not listed here stays in the live view. Phase 1 adds pages (Phase 1 backlog).
- **Close-out.** When an incident's last item closes, one message goes to its principal, held until working hours if the deployment says so. It links a short form with the feedback question, the adoption answers and any missing milestones (Section 4.2).
- **Reversals.** If a human rejects a hypothesis after its convergence was announced, the same recipients get a message.
- **After close.** Messages about a closed item go to its principal and responders, resolved again at sending time.
- **Content.** Message text is generated by rules from platform facts only: enums, numbers, names from trusted fields, and platform links. It never contains model-written text or untrusted fields, and channel markup in it is escaped.
- **Invisible arm.** No notifications, except a rule-generated request to set a missing milestone, which reveals nothing about agent output.
- **Channels.** Each message class maps to a configurable outbound channel, with a fallback channel (deployment profile).

### 10.4 Devices and access

- **Phones first.** The items view, the board, suggestions and the close-out form are designed for phone portrait first, with a wider layout that shows the feed beside the hypotheses.
- **Deep links.** Every message links to the exact item or form, and signing in again returns the user to that link.
- **Accessibility.** The target is WCAG 2.2 AA:
  - status is never shown by color alone, and every status has a text label;
  - every action works from the keyboard;
  - the board announces one polite summary per round to screen readers, not one per entry;
  - layouts hold at 200% text size, and a dark theme is available;
  - touch targets are at least 44 points;
  - raw outputs wrap, with line numbers, and scroll only inside their own block.
- **Time entry.** A form that asks for a past time has no default. It offers quick picks (now, 5, 15 or 30 minutes ago) and a field, shows the viewer's time zone by name, stores UTC, and refuses times before the item opened or in the future. A later ruling with a reason corrects an entry.

## 11. Milestones

- **Source.** A work item type lists its milestones (Section 3.5). Milestone times come from the deployment's milestone source through inbound events, and attach to the incident (Section 4.1). The same source MUST serve both arms.
- **Fallback.** A responder may set a missing milestone in the items view (Section 10.2), in either arm. The ruling records where the time came from.
- **Precedence.** A time from the milestone source wins over a manual entry, unless a responder overrides it with a reason. Rating packets show each milestone's origin.

## 12. Testing

### 12.1 Layers

| Layer | What it covers | How | When |
|---|---|---|---|
| Gateway contract | Every rule in Sections 3.3, 4, 5.2–5.3, 6.1–6.3 and 9.2, including repeated calls, bounds, server limits and record-only behavior | A programmable fake MCP server records every request and injects timeouts, dropped connections, errors, slow responses and tool-list changes | Every commit; no model calls |
| Coordinator rules | Rounds, snapshots, convergence, stances and retraction, standing refutations, the wrap-up round, quiet rounds, budgets, leases, key merging, limits, dedup, correlation, event order and instances, finality, stop and close, and the one lease that covers the coordinator and both gateways | Scripted fake agents post entries from a script; the tests assert the coordinator's sequence of decisions | Every commit; no model calls |
| Agent smoke | Whether an agent version reaches a known cause within budget without overstepping | Fake MCP servers return canned data; 1–2 scenarios per agent, each run [5] times; the pass rate is reported | When an agent's configuration (model, skill version) changes, for that agent only |
| Red team | Injection, misleading suggestions, contagion between agents, entry flooding, claim hoarding, forged quotes, interface payloads (Section 9.1), requests that break the LLM gateway's allowlist, and sandbox probes for credentials or outbound access | Attack content planted in fake server outputs; probe scripts run inside the sandbox | Once before Phase 0 measurement starts; again whenever a security rule changes |
| Load | An incident storm: [3] items with all their runs, against slow fake servers and a rate-limited fake model. Server limits hold, comparison and probation runs pause first, and responders' tools keep their capacity. | Fake servers with injected latency; then a trial against the real read backends, which sets each server's limit | Before Phase 0 measurement starts |

### 12.2 Properties

In Phase 0, two properties are tested on their own: when a request reaches a fake server, its audit record already exists (the fake server checks on receipt); and record-only mode never sends a write. Phase 1 adds the write-path properties (Phase 1 backlog).

### 12.3 Replay, changes and versions

- **Replay.** Recorded items serve as regression tests. Because the coordinator is deterministic, any closed item within retention can be replayed from its event log. Before a rule change is merged, past items are replayed, and the first decision that would change in each is listed for review; after that point, agent behavior is counterfactual.
- **Changes before the first gate.** Outside a measurement window, a changed agent or skill version goes live after its review (Section 3.7), its smoke scenarios, and a preregistered result on the retrospective set: no worse than the current version. Moving back to the version that was current before needs only its review, before and after the gate.
- **Probation after the gate.** From the gate on, a change, other than a move back to the previously current version, also runs in shadow on at least [5] items of either arm without a safety violation (Section 7). Its blind scores go to its owner and a platform admin, who decide and record the decision. The release record that tracks candidate and current versions, and the probation runs the coordinator generates, are built during the first measurement window (Section 14.1).
- **Freeze.** During a measurement window, the invisible arm's configuration is frozen. Changes wait until the window ends, and yanks apply to the visible arm only (Section 3.2).
- **Builds during a window.** They ship behind flags that stay off until the window ends. Before any deploy, the window's closed items are replayed, and no coordinator decision may change. Each deploy is recorded as a deviation.
- **Tracing.** Every item locks its configuration, so the platform can list the items that ran with any agent or skill version, with their conclusions and labels. After a yank, the skill's owner reviews those items and records what they found.
- **Retrospective set.** Past incidents with recorded root causes, with reads bounded to the alert time. The set serves the team's retrospective check (Section 14.1), the change rule above, and replays of convergence settings after a window (Section 5.6). It never counts toward the gate. Live state that cannot be replayed makes it pessimistic for some agents.

## 13. Value validation

### 13.1 Arms

At open, an item takes the arm of any in-scope item that opened in the previous [30 min] and is still open, whatever its service. Otherwise the coordinator draws a new arm from a recorded seed, at the preregistered invisible share [50%]. Concurrent alerts therefore share an arm, and a cascade across services stays in one arm.

- If a later link joins items with different arms, the incident is excluded from measurement and reported; the preregistration caps such exclusions.
- Items opened by a human are visible and outside measurement (Section 5.1).
- **Visible:** the normal flow.
- **Invisible:** the primary run executes in shadow mode, with no board and no notifications until its release (Section 7).

### 13.2 Metrics

All definitions, thresholds and sample sizes are preregistered and signed before any data is seen (ADR-011).

| Metric | Role | Definition |
|---|---|---|
| Hit rate | Gate | Invisible arm only, one result per incident, from the primary run of its first item: the share of incidents whose conclusion as of the cutoff matches the label. |
| Baseline margin | Gate | On the same incidents, counted on discordant incidents only: those where exactly one of the primary run and the preregistered baseline is right. The preregistration sets the minimum number of agent-only hits and their minimum ratio to baseline-only hits. |
| False-convergence rate | Guardrail | Invisible arm only. The share of incidents whose primary run converged before the cutoff on a hypothesis that does not match the label. Being confidently wrong is worse for responders than reaching no conclusion. |
| Action accuracy | Phase 1 entry | Invisible arm. Among incidents whose recorded mitigation the deployment's write tools could perform, the share where the primary run recorded the action that worked before `mitigation_started`, as raters judge it. Raters also note any recorded action that would have made things worse. |
| Milestone durations | Direction only | The durations the deployment designates, compared between arms. There are too few incidents to gate on them; a clearly worse visible arm triggers an investigation. |
| Safety floor | Hard gate | In Phase 0, no write reaches a server at all. Where target systems keep their own records, reconcile against them weekly. |
| Responder feedback | Reported | Visible arm, asked at convergence and in the close-out form: "correct, partly, wrong, too early to tell, or already knew". The preregistration says whose answer counts, by default the principal's. |
| Adoption | Reported | Visible arm: the adoption answers (Section 6.3). |
| Headroom | Phase 1 entry | The median of the part of the "waiting to act" duration that the write path can remove, over incidents whose recorded mitigation the deployment's write tools could perform (deployment profile). |
| Builder operating time | Reported | Hours the builders spend operating and maintaining the platform during the window, not building it, reported next to token cost. |

**The cutoff.** A run's conclusion is scored as of the earliest of:
- the run's stop;
- the human `cause_identified` and `mitigation_started` times;
- the first human change on the item's service that the deployment can see after the alert.

The cutoff is never earlier than the end of the run's first round. Incidents whose cause was identified within the first round are reported separately. The baseline is evaluated with the same information: what was known at the alert.

**A hit.** Only the conclusion counts. "Some hypothesis on the board was right" does not: several agents can post a dozen hypotheses between them, and counting any of them would inflate the rate. The preregistration defines a hit: the key alone, or the key plus the specific change or component that the label names.

### 13.3 Comparisons

- **Baseline.** A deterministic rule that needs no model, such as "the most recent change", evaluated by the coordinator as of the alert, through the gateway under the platform's identity. The deployment profile maps its result to a key. It decides the baseline margin (Section 13.2).
- **Collaboration versus independent work.** The `independent` shadow run (Section 3.5) is the same team limited to a single round with the full time budget. Agents never see each other's entries, and the platform aggregates their hypotheses by key.
  - It runs on invisible-arm items with the primary run's wall-clock and token budget, limited to one round. It has no wrap-up round: its conclusion is taken when its single round ends.
  - Paired, blind-scored results show whether multi-round collaboration earns its cost and complexity.
  - It is reported, not gated.
- **External comparators** (ADR-011). An off-the-shelf tool runs as a shadow run inside a sandbox, through both gateways (Section 3.5), and is scored blind like any run.
- **Sample size.** At [20] incidents per window these comparisons are case studies. A comparison may change the primary configuration, or adopt a tool as the runtime, only after a preregistered minimum number of discordant incidents, pooled across windows if needed.

### 13.4 Blind scoring

- **Packets.** At an incident's finality, the platform builds its rating packet: a pointer to the deployment's ground-truth record; every run's output (primary run, shadow runs, comparators and the baseline); and the invisible arm's recorded proposals. The cited redacted outputs are snapshotted into it. Adoption answers never enter packets.
- **Two stages.** A rater's first export holds only the pointer to the ground-truth record and the key form. The rater records the ground truth as one key, or as a set of equivalent keys where the record supports several framings, or marks it "insufficient". Only after that import does the platform generate the rater's second export, with the shuffled outputs.
- **Matching.** The platform computes key matches against the equivalent-key set. Raters judge only what matching cannot decide:
  - outputs whose key contains `other`;
  - mismatches that involve `dependency-failure` or a different service;
  - recorded actions (Section 13.2);
  - where the preregistration requires it, whether a matching output names the specific change or component.

  Each judgment is match, partial or no match. A run with no conclusion appears as a fixed row, and identical keys are merged.
- **Adjudication.** Key disagreements between raters are settled at the first stage, before anyone sees outputs, by a preregistered rule such as a third rater. Judgment disagreements follow a preregistered rule, such as the stricter judgment counting.
- **Format.** Outputs are shuffled, stripped of their source and rendered in one format: hypothesis key plus one line of rationale. Without a single format, raters could tell sources apart by writing style.
- **Raters.** At least two raters who are not platform builders score each packet independently. The platform imports their scores, ties each score to an eligible rater, reports inter-rater agreement and computes the metrics.
- **Order.** Within the gate sample (Section 13.5), incidents whose runs disagree are labeled first. Order never changes which incidents are in the sample.
- **Phase 0 tooling.** Phase 0 uses file export and import, not a rating UI.

### 13.5 Sample and stopping

- **Gate sample.** The first [20] in-scope incidents of the invisible arm to reach finality, all labeled, whatever the labeling order. Wait at most [8 weeks].
- **Hits needed.** The observed hit rate needed for a 90% Wilson lower bound of at least 50%:

| Invisible incidents | Hits needed | Observed rate |
|---|---|---|
| 10 | 8 | 80% |
| 20 | 14 | 70% |
| 40 | 26 | 65% |

- **Funnel.** Before preregistration, the deployment estimates from history how many incidents survive each step: in scope, invisible, not builder-handled, not excluded by a cross-arm link, with a recorded root cause, and staffed (deployment profile). If the estimate falls below [20] in [8 weeks], the preregistration picks the path now: a higher invisible share, a longer window, or the 10-incident row.
- **Invisible share.** It sets how long reaching [20] takes: at [50%] it needs about 40 in-scope incidents, at 70% about 29. A higher share shortens the window but leaves fewer visible incidents, and those carry responder feedback and the milestone comparison.
- **Builder-handled incidents** are those where a platform builder was the principal or set `cause_identified`. They are excluded or reported separately (ADR-011).
- **Degraded, cut and not-staffed incidents** count as the preregistration says (Sections 5.1 and 7).
- **Shortfall.** If [20] incidents are not reached within [8 weeks], the available incidents are evaluated with the same bound and the same discordant-incident rule. The preregistration then chooses only between a new window and staying read-only. Phase 1 never starts on directional accuracy or responder feedback alone.

### 13.6 Cost and governance metrics

- **Cost.** The LLM gateway records tokens and spend per run, agent and arm. The MCP gateway records tool calls on the same dimensions.
  - Reported: cost per incident, cost per hit and the shadow share.
  - A ceiling on the primary runs' average cost per incident is preregistered as a guardrail; shadow spend is reported separately.
  - Builder operating time is reported next to cost (Section 13.2).
  - Cost is shown per owning team. It is the price of the governance this platform exists for.
- **Owner autonomy.** Reported outside measurement windows only, because changes wait during a window:
  - the number of agents owned by teams other than the builders (target: at least 1);
  - the configuration changes those owners made themselves;
  - the time from request to live. Before the first window, the owner outside the builders releases one skill version through the full path without builder help, and that time is recorded.
- **Least privilege in practice.** Tools granted versus tools used, per agent. Grants unused for [30 days] are flagged for removal.

### 13.7 Phase 0 gate

| Outcome | Decision |
|---|---|
| Safety floor broken | Stop measuring, fix the cause, restart the window |
| False-convergence guardrail breached | Treat as a failed gate |
| Cost guardrail breached | The gate result stands, but Phase 1 waits until the primary runs' average cost per incident is under the ceiling |
| Hit-rate lower bound below [50%] | Iterate on skills and configuration for up to [3 weeks], then measure again in a new window that is evaluated on its own. If it still fails, stop, or try an off-the-shelf diagnosis tool as an agent runtime (ADR-001). |
| Baseline margin not met | The agents show no value over the baseline. Do not start Phase 1. The read-only end state is the baseline's answer delivered to responders, or an off-the-shelf tool (ADR-001). |
| Sample not reached by the deadline | Apply the shortfall rule (Section 13.5) |
| Gate passed; action accuracy below its preregistered minimum | Read-only is the end state for now; improve the recorded actions before Phase 1 is designed |
| Gate passed; headroom median below [5 min] | Read-only is the end state for this phase; the write path is not built |
| Gate passed; action accuracy and headroom met | Design Phase 1 from the backlog and the census facts, review it, preregister its evaluation, then build it |

### 13.8 After the gate

Measurement turns into monitoring; it does not stop.

- **Labels.** Every in-scope incident whose runs reached different conclusions is labeled, plus a random [20%] of the rest, drawn at finality (Section 7). One rater who is not a builder is enough.
- **Rolling figures.** They are computed on the random sample only, over its last [20] labeled incidents; disagreement incidents are reported separately as case lists. Alarms on the hit rate and the false-convergence rate follow a preregistered rule with a known false-alarm rate, and notify the owners of the agents involved and a platform admin.
- **Second runs.** After the gate, probation and comparison shadow runs may run on either arm (Section 7), so disagreement still has something to compare.
- **Contamination.** After the gate, responders see agent output, and their records may follow it, so labels can lean toward the agents. A small invisible share [10%] MAY continue, with the team lead's consent, as an uncontaminated reference. Without it, the rolling figures show trends, not proof.
- **Per version.** Results are also listed per agent and skill version (Section 12.3). At the first deployment's volume these are case lists for owners to review, not rates.

### 13.9 Phase 1 evaluation

Drafted in the Phase 1 backlog. It is preregistered before Phase 1 is built.

## 14. Delivery plan

### 14.1 Phases

| Phase | Platform scope |
|---|---|
| Spike | Runtime isolation (ADR-006) |
| Mechanism check | Needs no team. The planned agents run without the coordinator on a public benchmark's fault scenarios, each on its own and aggregated by key as in the independent run, through read-only MCP servers that cannot see how the faults were injected. It shows that the mechanisms work end to end (reads, evidence with provenance, keys and the conclusion), and its runs give a first token budget default. Its pass criteria are fixed before the runs, against the benchmark's ground truth and, where its faults include changes, the "most recent change" baseline. A failure pauses the build to rethink the skills, the runtime or an off-the-shelf tool. A pass is not evidence of value and never counts toward the gate (Section 1.4). |
| Phase 0 (read-only, full collaboration) | Section 3 (no approval policies); Section 4, with `recorded` proposals only; Sections 5.1–5.3 and 5.6–5.7, without joins; Sections 6.1–6.3; Section 7, without probation; Section 8; Section 9; Section 10, without version history; Section 11; Section 12; Sections 13.1–13.7. Built before a team adopts the platform, on the assumptions ADR-016 records. |
| Adoption | A team adopts the platform (Section 14.4) and provides its census and the commitments of Section 14.3, then the decisions they drive. A historical headroom below [5 min] drops all Phase 1 preparation. If the census makes the gate look out of reach, the team stops, reshapes the deployment (fewer services, more read servers), or delivers the baseline's answer to responders first. Out of reach means that, given the agents' read coverage and the incidents diagnosed within one round, 14 hits in 20 (Section 13.5) or the preregistered baseline margin is implausible. The team also checks the build's assumptions (ADR-016); a different answer is a new adapter or a change, made before its measurement window. |
| Retrospective check | The team's planned agents run without the coordinator, each on its own and aggregated by key as in the independent run, on its census incidents with reads bounded to the alert time, through the platform's retrospective check command (Section 14.4). They must at least match the baseline on that subset; otherwise the team's deployment pauses to rethink the skills, the runtime, or an off-the-shelf tool. The same runs set the team's token budget default. |
| Measurement window | Until the gate sample is final and labeled, at most [8 weeks]. Built during the window, behind flags (Section 12.3): Section 13.8, the version history view, the release record and generated probation runs. |
| After the gate | Joins; probation in shadow; one fast-fix mechanism for skills |
| Phase 1 (write path) | Designed after the gate from the Phase 1 backlog and the census facts, reviewed, then built |

### 14.2 Estimate

| Phase | Person-weeks |
|---|---|
| Spike | about 1 |
| Mechanism check | about 1–2, depending on the benchmark |
| Retrospective check, with the first team | about 1 |
| Phase 0 | 19–25 |
| Measurement window, in parallel | about 2 |
| Phase 1 | Estimated with its design; the backlog draft suggests 10–12 |

These are rough estimates. They assume an experienced engineer making heavy use of AI coding assistants, and they include integration and testing.

- **Without a team.** On 2026-10-14 the author decided to build Phase 0 before a team makes the commitments of Section 14.3 (ADR-016). The build's timeline starts that day; a team's retrospective check and measurement start when it adopts the platform.
- **2 people:** spike, retrospective check and Phase 0 take about 3.5–4.5 months, and measurement takes 1–2 months. The gate falls around month 4.5–6.5.
- **1 person:** Phase 0 alone takes about 6.5–8.5 months.
- **Capacity.** 2 people for 2 months is about 16 nominal and 12 effective person-weeks. A timeline of 1–2 months cannot produce a value decision: measurement alone needs 4–8 weeks of wall-clock time after Phase 0 is built.
- **Compared with the superseded v1 spec.** It estimated 8–11 person-weeks for a single-agent design, and the third document review raised that to 14–20. Full collaboration in Phase 0 adds about 3–4 person-weeks and delays the gate by about a month.
- **Reviews of 2026-10-11.** Three review rounds added correctness and usability work that earlier drafts lacked, such as incidents, finality, the board, inbound handling, server limits and retention. They also moved Phase 1 off the critical path, and moved probation and the release record into the measurement window, so Phase 0 stays near its earlier size.

### 14.3 What the first team must provide, before its measurement window

As of 2026-10-14 no team has made these commitments. Phase 0 is built without them (ADR-016), but no measurement window opens until they are made. The deployment profile and the census come first, before the team's retrospective check (Section 14.4).

- Its deployment profile, with the onboarding questions answered (Section 14.4);
- the team lead's consent to hide agent output at random for part of the incidents (ADR-011), and optionally a small share after the gate (Section 13.8);
- someone who is not a builder, to co-sign the preregistration;
- at least two named raters who are not builders, for about 10 minutes per incident, and one after the gate for the monitoring sample (Section 13.8);
- a named team outside the builders that owns an agent and writes its skill, by a set date (Section 1.2);
- an owner outside the builders for the store where the audit chain head is anchored (Section 9.4);
- the historical census of the deployment profile: headroom, the baseline, the sample funnel, alerts per incident and the time to identify the cause.

### 14.4 Onboarding a team

The platform serves any team that handles incidents, and never supplies a team's content (Section 0). A team joins in this order:

1. **Profile.** It writes its deployment profile: its facts, its answers to the onboarding questions and its configuration plan. The example profile shows the shape.
2. **Owners and content.** It names a human owner for each of its agents, skills and MCP servers, releases its runbooks as skills (Section 3.2), and registers its read-only MCP servers with tool policy (Section 3.3).
3. **Retrospective check.** It builds a retrospective set from its own past incidents (Section 12.3), and runs its agents on it with the platform's retrospective check command, the same command the mechanism check uses (Section 14.1). Its agents go live only if they at least match its baseline there. The set and the results stay with the team.
4. **Measurement.** The first team's incidents are the Phase 0 measurement (Section 13). How later teams are measured is decided after the gate, with Section 13.8.

## 15. Parameters

| Parameter | Default | Section |
|---|---|---|
| Run budget: wall clock / cost-weighted tokens / rounds | [15 min] / [2,000,000], provisional until the mechanism check, then the first team's retrospective check / [6] | 3.5, 5.6 |
| Round time limit | [150 s] | 5.2 |
| Agents per run | [4] | 5.1 |
| Items investigating at once | [3] | 5.1 |
| Admin API, fixed in code: an opening's body / its `Idempotency-Key` | 64 KiB / 1 to 128 printable ASCII characters | 5.1 |
| New entries per agent per round | [5] | 4.2 |
| Open claims per agent | [2] | 4.2 |
| Claim lease, its own round included | [2] rounds | 4.2 |
| Budget extension | [1 round and 5 min] | 4.2 |
| Rulings on a closed item | [72 h] | 4.2 |
| Reopen window after close | [24 h] | 4.1 |
| Finality wait for cutoff milestones | [24 h] | 4.1 |
| Convergence: contributors / sources | [2] / [2] | 4.3 |
| Refutation margin for agent refutations | [2] | 4.3 |
| Correlation window | [30 min] | 3.6 |
| Arm-sharing window | [30 min] | 13.1 |
| Inbound request maximum age / clock skew | [5 min] / [2 min] | 3.6 |
| Open-time read retries | up to [2 min] | 5.1 |
| Calls at once per server / comparison and probation share | [10] / [50%] | 6.2 |
| Degraded server: consecutive failures / cool-down | [3] / [60 s] | 6.2 |
| Read retries on connection failure | [2] | 6.2 |
| Gateway: calls in flight per session / per session and server | [4] / [2] | 6.1 |
| Gateway: server definitions looked at again after / a look ends after | [30 s] / [10 s] | 3.3 |
| Gateway: call timeout / argument size / output size | [60 s] / [64 KiB] / [256 KiB] | 6.2 |
| Gateway: `read_board` page / hypotheses and open tasks shown | [64 KiB] / [50] | 5.2 |
| Gateway: refused write calls per session per round / notices per agent per round | [10] / [10] | 4.2, 5.3 |
| Gateway: proposal facts, values kept / characters per value / time left for recording | [5] / [200] / [5 s] | 5.3 |
| Quotes per side on a hypothesis | [2] | 10.2 |
| Consecutive session failures before removal | [2] | 8.2 |
| Turns: the runtime's own deadline after the round's / model calls per turn / max_tokens per call, at most the LLM gateway's | [2 s] / 50, fixed in code / [4,096] | 8.2 |
| Turns: output kept per turn | [1 MiB] | 8.2 |
| Auto-close: after the later of trigger resolution and entry into `awaiting_close` | [30 min] | 4.1 |
| Daily token cap | Set by the deployment, in cost-weighted tokens, counting the calls admitted each UTC day, by the database's clock | 7, 8.2 |
| Round's end: wait for write calls / for model calls in flight, looked at every | [5 s] / [30 s], [1 s] | 5.6 |
| Wrap-up rounds per run: that count / opened in all, counted or not | [2] / twice that | 5.6 |
| Lease: time / renewed every / the holder fences itself before it could expire by / taken again every | [15 s] / [5 s] / [5 s] / [5 s] | 8.2 |
| Lease: a take's wait for the lease, which with the renewal period stays below the lease's time | [5 s] | 8.2 |
| Database: a pool's ping of a connection that has idled | 1 s, fixed in code | 8.2 |
| Database: a pool's connect, unless the database's URL sets `connect_timeout` | The lease's renewal period [5 s] | 8.2 |
| Database: a transaction's time / its time idle, each at most the lease's time less its margin | [10 s] / [10 s] | 8.2 |
| Database's time: a sample of it counts for | 1 min, fixed in code | 8.2 |
| Drain: the LLM gateway's, with the upstream sessions' close / the MCP gateway's | [30 s] / three times its record timeout | 8.2 |
| Configuration refreshed every | [1 min] | 8.2 |
| Migrations: a wait for a lock | [5 s] | 8.2 |
| Listeners, fixed in code: a request's header / its whole read | 10 s / 30 s | 8.2 |
| Coordinator: a step that failed is tried again after | [5 s] | 8.2 |
| Reference token: the price of one token at which spend is counted as cost-weighted tokens | Set by the deployment | 5.6 |
| LLM gateway, fixed in code: request size / max_tokens / requests at once per session / refused requests per session per round | 512 KiB / 16,384 / 1 / 10 | 9.3 |
| LLM gateway, fixed in code: thinking blocks kept per session / tool call ids kept per session / sessions kept | 1,024 / 1,024 / 1,024 | 9.3 |
| LLM gateway: wait for a session's call that outlived its request / record timeout | [15 s] / [5 s] | 8.2 |
| LLM gateway: vendor calls at once, none kept for platform reads / degraded after failures / for / call timeout | 10, fixed in code / 3, fixed in code / [60 s] / [10 min] | 8.2 |
| LLM gateway, fixed in code: waits between attempts at the vendor | 1 s, 2 s, 4 s | 8.2 |
| Shadow cuts start at | 80% of the daily cap, fixed in code | 7 |
| Shadow release for unlabeled incidents | [14 days] after close | 7 |
| Retention: rulings, configuration, access and model-call records / measurement items / everything else | [1 year] / [30 days] after labeling / [30 days] | 9.4 |
| Database backups: content | At most [35 days] | 9.4 |
| Audit chain anchor interval | [1 h] | 9.4 |
| Unused-grant flag | [30 days] | 13.6 |
| Smoke runs per scenario | [5] | 12.1 |
| Shadow items before promotion, after the gate | [5] | 12.3 |
| Invisible share | [50%] | 13.1 |
| Invisible incidents for the gate / maximum window | [20] / [8 weeks] | 13.5 |
| Hit-rate gate | 90% lower bound ≥ [50%] | 13.7 |
| Headroom threshold | Median ≥ [5 min] | 13.7 |
| Iteration after a failed gate | [3 weeks] | 13.7 |
| Labels after the gate: random share of incidents without disagreement | [20%] | 13.8 |
| Rolling window after the gate | [20] labeled incidents | 13.8 |
| Invisible share after the gate (optional) | [10%] | 13.8 |

The baseline margin and the action-accuracy minimum have no platform default; the preregistration sets them.

## 16. Documents and ADRs

These changes are carried out after this spec is approved.

| ADR | Action |
|---|---|
| 001 Wedge: ops incidents | Keep the decision, and move its technology facts to the deployment profile. Candidate E (off-the-shelf tools) becomes a candidate agent runtime and an external comparator, not a replacement for the platform. Its "integrate if the build is not clearly better" rule becomes the baseline margin and the read-only end state of the gate (Section 13.7). |
| 002 Single agent by default | Superseded by ADR-012. |
| 003 Identity is versioned configuration | Keep. Add the yank list in Git, and the release record built during the first window (Section 12.3). |
| 004 Postgres plus a task library | Keep. Add one lease for the coordinator and both gateways, which fences a holder that lost it (Section 8.2). Timers come from the records; River waits for jobs that must outlive them (Plan 7a). Its time base: records, the lease and the coordinator's deadlines in the database's time, the leader's watch in monotonic time, and the gateways' deadlines in the process's (Plan 7d-1). Its write-path premises are revisited with the Phase 1 design. |
| 005 MCP plus effect adapters | Superseded by ADR-013. Untrusted annotations, hash pinning and candidate arguments carry over. |
| 006 Agent runtime spike | Keep. Add seven criteria: round-based turns, in which the runtime calls `end_turn` when, and only when, its model asks (Plan 7d-1); gateway-only MCP; built-in tools that reach outside the sandbox disabled; egress denied by default; host or workspace configuration kept out of prompts; a fresh context for each turn (Plan 7a); and tool lists that change at round boundaries. The runtime Plan 7d-1 built also nudges, once, a model that answers without calling a tool: an option of that runtime, not a criterion every runtime must meet. |
| 007 One model vendor behind an LLM gateway | Keep. Add per-run, cost-weighted token budgets, cost attribution and the request allowlist (Section 9.3). Decided: the LLM gateway is built, not an existing proxy extended (Plan 6b). |
| 008 Single-organization deployment | Keep. Drop the rationale "the platform holds no cluster write access"; that is now each deployment's choice. |
| 009 Capability packs | Superseded by ADR-014. |
| 010 Draft PR plus required check | Leaves the platform layer. Whether it becomes required hardening is decided with the Phase 1 design (Phase 1 backlog). |
| 011 Measurement | Keep. Rebase it on shadow mode and incidents, and add time-clustered arms, the cutoff, finality, discordant-incident margins, action accuracy, the fixed gate sample, the shortfall rule and monitoring after the gate. |
| 012 (new) | Multiple agents on a structured blackboard, with latest stances, retraction and standing refutations, and no challenger in this phase. The rationale is governance, so this phase has no team-versus-single comparison. |
| 013 (new) | The platform MCP gateway as the single policy enforcement point: tool policy, audit before call, record-only proposals, server limits and bounds. The write path is drafted in the Phase 1 backlog. |
| 014 (new) | The configuration model: agents, skills (runbooks included), MCP server registrations with tool policy, work item types, trigger mappings and identity sources. Skills have owners and a yank list in Git: knowledge is slow to add and fast to remove. |
| 015 (new) | Implementation: Go, domain-driven design, interface-driven ports and adapters (Section 2.2). This settles the open implementation-language question and chooses the condition language (Section 3.3). It constrains ADR-004 (a Go task library) and ADR-006 (how the runtime fits a Go platform). |
| 016 (new, 2026-10-14) | Delivery: Phase 0 is built before a team adopts the platform, on recorded assumptions, with a checkpoint after roadmap Plan 7d and an exit date. Nothing built counts toward the gate. |

Other documents:

- **Older documents.** The v1 spec, the target-state document and the facts-and-spikes draft stay outside this repository and are not translated. This spec, the Phase 1 backlog and the implementation roadmap carry everything still in force. The runtime spike is roadmap Plan 2 (ADR-006); the other checks belong to each deployment (example deployment profile, Section 5).
- **Phase 1 backlog.** `2026-10-11-phase-1-write-path-backlog.md` holds the write-path draft and the open issues of review round 2. It becomes a spec only after its own review, after the gate.
- **Language.** New and revised ADRs are written in English.

## Appendix: Glossary

| Term | Meaning |
|---|---|
| Agent | A versioned configuration (runtime, model, skills, tool grants) with a human owner |
| Deployment profile | The domain content one team brings to a deployment, kept by that team outside this repository (Section 0) |
| Incident | The work items that one underlying problem causes; measurement counts incidents (Section 4.1) |
| Work item | One unit of work, opened by a trigger or a human |
| Run | One team configuration working one item, with its own blackboard and budget |
| Primary run / shadow run | The run of record / a candidate run hidden from humans until its release (Section 7) |
| Shadow mode | How a shadow run works: hidden until release, record-only and without human input. The invisible arm's primary run uses it too (Section 7). |
| Principal | The human on whose behalf an item runs |
| Platform builder | Anyone who wrote or reviewed platform code, configuration or skills for this deployment during the phase |
| In-scope incident | An alert-triggered incident on the service list signed in the preregistration (Section 13.5) |
| Responders | The group handling the item, together with the principal |
| Proposal | A write call recorded by the gateway; in Phase 1, one held until approved |
| Target | The tuple of arguments that names what a write changes, taken from tool policy |
| Stance | A contributor's latest support or refutation of a hypothesis (Section 4.3) |
| Standing refutation | Refuting evidence that blocks convergence (Section 4.3) |
| Yank | Withdrawing a skill version: agents that pin it are not invited until their owners choose another (Section 3.2) |
| Conclusion | The hypothesis a run is scored on (Section 4.3) |
| Cutoff | The moment a conclusion is scored (Section 13.2) |
| Finality | The point at which an incident's rating packet and gate-sample membership are fixed; later rulings do not change them (Section 4.1) |
| Arm | `visible` or `invisible` (ADR-011), shared by items that open close together (Section 13.1) |
