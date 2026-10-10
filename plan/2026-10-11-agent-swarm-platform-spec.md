# Agent Swarm Platform Specification

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-11 |
| Normative | Yes. Supersedes `2026-10-10-v1-implementation-spec.md`. |
| Companion | `2026-10-11-first-deployment-ops-incidents.md` holds all domain content for the first deployment. |
| Reference only | `2026-10-10-team-agent-architecture-design.md` (target-state notes, not normative). |
| Decisions | `adr/`. Section 16 lists the ADR changes this spec requires. |

## 0. Conventions

- **MUST**, **MUST NOT**, **SHOULD** and **MAY** are used as in RFC 2119.
- **Defaults.** Bracketed values such as [15 min] are defaults that a deployment may change. Section 15 lists every one.
- **Examples.** They use neutral names such as `release-manager` and `get_release`. Real servers, tools and policies belong in the companion document.
- **Litmus test.** Replacing any domain technology, such as a deployment tool, a code host or a metrics system, changes the companion document and never this one.

## 1. Purpose and scope

### 1.1 What the platform is

Agent Swarm is a governance system for digital employees, not another agent framework. It decides:

- who each employee is and which human owns it;
- what each employee knows (skills) and what it may touch (MCP tools);
- how employees work together on one work item;
- which actions need human approval, and how every action is recorded.

Employees work through two kinds of assets:

| Asset | What it is | Credentials | Grants tools |
|---|---|---|---|
| Skill | Knowledge: instructions, reference files and optional scripts. Scripts run only inside the employee's sandbox. A runbook is a skill. | None | No |
| MCP server | Capability: the only way to read from or change an external system | Held behind the MCP gateway | Only through tool policy |

Every external side effect is therefore an MCP tool call, and every MCP tool call passes through the platform's MCP gateway.

### 1.2 Why several employees

Several employees work each item. The reason is governance, not diagnostic accuracy (ADR-012):

- each employee has a human owner, who can change it without going through the platform builders;
- each employee gets least privilege: only the tools its job needs.

So this phase does not compare a team with a single employee. It does compare collaboration with independent work, because the number of rounds is a configuration choice (Section 13.3).

### 1.3 Goal of this phase

The goal is internal value validation on the first deployment described in the companion document: do employees reach correct conclusions often enough, safely, and at an acceptable cost? Section 13 applies the measurement design of ADR-011.

### 1.4 Out of scope

- **Multi-tenancy.** ADR-008 keeps one deployment per organization; records still carry a `tenant_id`.
- **Writes without human approval.**
- **Learning across work items,** self-improvement and knowledge publishing.
- **A synthetic evaluation bench,** meaning fault injection into non-production environments.
- **An external policy engine.** Policy rules stay inside the platform, behind a clean boundary between decision and enforcement.
- **Comparing a team with a single employee** (Section 1.2).

## 2. Architecture

```
 trigger (webhook) --> [coordinator] --creates--> [blackboard] <-- humans (web UI)
                           | invites, budgets          ^
                           v                           | blackboard tools
  [employee sandbox] x N --MCP--> [MCP gateway] -------+
    agent runtime + skills          | per-employee tool policy
    no credentials                  | write -> proposal -> approval inbox -> execute
         |                          +--> registered MCP servers (hold credentials)
         +--> [LLM gateway]         +--> [audit and trajectory store]
```

| Component | Responsibility |
|---|---|
| Coordinator | A deterministic program that never calls a model. It opens work items, creates runs and their blackboards, invites employees by subscription and paces rounds. It also enforces budgets, convergence and stop rules, assigns ADR-011 arms and starts shadow runs. |
| Blackboard | One per run, append-only. Its entries are typed, and each has an author and references (Section 4.2). |
| Employee runner | One disposable sandbox per employee session. It loads the employee's skills and runs its agent runtime, which is swappable (default per ADR-006). Egress goes only to the MCP gateway and the LLM gateway, and the sandbox holds no credentials. |
| MCP gateway | The only MCP endpoint employees see, and the only place tool policy is enforced (Section 6). |
| LLM gateway | The only route to models (ADR-007). It enforces token budgets per run and attributes cost. |
| Human interface | The platform's web UI: live board, suggestions, approval inbox, rulings and administration (Section 10). Notifications go out through configurable channels. |
| Registry and configuration | Employees, skills, MCP server registrations with tool policies, approval policies, work item types and trigger mappings, kept in a Git repository that requires review (Section 3). |
| Audit and trajectory store | An append-only record of tool calls with redacted outputs, board entries, approvals, rulings and model-call metadata. |
| Database | Postgres, the single source of truth for all state (ADR-004). |

Each employee session gets two short-lived tokens, one for each gateway. Both are scoped to its run and employee, and both expire when the run stops.

### 2.1 Invariants

1. Employee sandboxes hold no credentials. Every tool call passes through the MCP gateway, the only place tool policy is enforced.
2. A human approves every write, and the approval covers only that exact call (Section 4.4). Approval policy decides who may approve.
3. Only the coordinator's rules decide when work stops. No employee can stop, extend or close a run.
4. This specification names no domain technology (Section 0).
5. The platform is never on a trigger's own notification path. When the platform fails, people fall back to their existing process.
6. A shadow run never sends a write to an MCP server, and humans never see it before its work item closes (Section 7).

### 2.2 Implementation constraints

- **Language.** The platform is written in Go. That covers the coordinator, the MCP gateway, runner control, LLM gateway integration and the human interface backend. The agent runtime inside a sandbox is swappable and may use another language (ADR-006).
- **Domain-driven design.** Code is organized by bounded context, and each context uses the vocabulary of this spec. The proposed contexts below are to be confirmed in the implementation plan:

| Context | Holds | Kind |
|---|---|---|
| Work | Work items, runs, blackboards, rounds, convergence and stop rules | Core |
| Actions | Tool policy decisions, proposals, approvals, target locks, execution outcomes | Core |
| Registry | Configuration objects and their versions (Section 3) | Supporting |
| Measurement | Arms, shadow-run scoring, rating packets, metrics | Supporting |
| Access | Humans, roles, sign-in | Generic |
| Audit | Append-only records | Generic |

- **Interface-driven.** The code follows ports and adapters:
  - Domain and application code depend only on Go interfaces they declare themselves (ports). Infrastructure packages implement them (adapters).
  - Dependencies point inward: infrastructure depends on application, and application on domain. Domain packages import no database, network or framework packages.
  - Ports exist only at real boundaries: persistence, job queue, MCP servers, model gateway, sandboxes, notifications, clock, identifiers, condition evaluation and secrets. Each interface is small and declared by its consumer.
  - Wiring happens in `cmd/`.
- **Testing.** The fakes in Section 12 (fake MCP servers, scripted employees, a fake clock) are adapters of these same ports. The deterministic test layers therefore need no network or model calls.

## 3. Configuration

All configuration lives in one Git repository, and every change requires review (Section 3.7). The platform loads a reviewed revision. When a work item opens, it locks the configuration version (commit and content hashes) and keeps it until it closes.

### 3.1 Employee

```yaml
kind: Employee
metadata:
  name: release-investigator
  owner: alice@example.com              # required; a human
spec:
  runtime: claude-agent-sdk              # swappable (ADR-006)
  model: claude-sonnet-5-5
  skills: [release-diagnosis@1.2.0]      # knowledge only; versions pinned
  tools: ["release-manager.*", "metrics.query_*"]   # narrows tool policy, never widens it
  subscribe:
    workItemTypes: [incident]
    taskTags: [release]
```

An employee's version is the content hash of its file together with the skill versions it pins (ADR-003). The employee's name points to the current version.

### 3.2 Skill

- **Contents.** A skill is a versioned bundle of instructions, reference files and optional scripts.
- **Scripts.** They run only inside the employee's sandbox, which can reach nothing but the two gateways.
- **No tools.** A skill grants no tools. A runbook for an MCP server is a skill.

### 3.3 MCP server registration and tool policy

```yaml
kind: McpServer
metadata: { name: release-manager, owner: platform-team }
spec:
  endpoint: https://release-mcp.internal
  credentialRef: secret://mcp/release-manager    # never reaches a sandbox
  toolFingerprint: "sha256:<recorded at review>" # hash of tool names, descriptions and input schemas
  tools:                                         # deny by default: unlisted tools are hidden
    get_release:
      effect: read
      redact: [default-secrets]
    list_recent_changes:
      effect: read
    rollback_release:
      effect: write
      target: "{args.service}"
      approval: second-human
      timeout: 120s
      idempotent: false
      errorMeansNoEffect: false
      candidates:
        change_id: { tool: list_recent_changes, args: { service: "{args.service}" }, field: "$[*].id" }
      fingerprint: { tool: get_release, args: { service: "{args.service}" }, field: "$.current" }
      check: { tool: get_release, args: { service: "{args.service}" }, condition: "result.status == 'healthy'", after: 120s }
```

| Field | Applies to | Meaning |
|---|---|---|
| `effect` | all | `read` or `write`, set by the platform. Server annotations, such as read-only or destructive hints, are never trusted. |
| `redact` | all | Named redaction rules. They apply to the output before any employee sees it and before it is stored. |
| `target` | write | Template for the lock key. Each target has at most one unfinished proposal, across all work items (Section 4.4). |
| `approval` | write | Name of an approval policy. |
| `timeout` | write | Execution timeout. On timeout the outcome is `unknown`. |
| `idempotent` | write | A reviewed claim that repeating the call with the same arguments has the same effect. It allows one automatic retry after `unknown`. |
| `errorMeansNoEffect` | write | A reviewed claim that a tool execution error means nothing changed. Such errors become `failed` instead of `unknown`. |
| `candidates` | write | For each listed argument, a read call whose output is the set of allowed values. The gateway rejects values outside the set. |
| `fingerprint` | write | A read call made when the proposal is created and again just before execution. If the field differs, the proposal is `voided`. |
| `check` | write | A post-execution check, defined in reviewed configuration. An employee may add an outcome check of its own (Section 5.3). |

The gateway makes candidate, fingerprint and check reads under the platform's own identity, and audits them like any other call. Conditions in `check` and `_check` use one sandboxed, side-effect-free expression language. ADR-015 chooses it.

**Server pinning.** The gateway computes a fingerprint over each server's tool names, descriptions and input schemas. If it differs from the registered fingerprint:

- the changed tools are hidden at once, including from running work items, until the registration is reviewed again;
- the registration's owner is notified.

### 3.4 Approval policy

```yaml
kind: ApprovalPolicy
metadata: { name: second-human }
spec:
  approver: { human: true, notPrincipal: true, groups: [responders-secondary] }
  expiresAfter: 60m
```

Groups are resolved through the organization's directory, either by a read tool or by single sign-on group claims. The companion document says which.

### 3.5 Work item type

```yaml
kind: WorkItemType
metadata: { name: incident }
spec:
  hypothesisKey:                               # Section 4.3
    - { name: service, values: { tool: catalog.list_services, field: "$[*].name" } }
    - { name: failureMode, values: [] }        # closed list, filled in by the deployment
  budget: { wallClock: 15m, tokens: 2000000, rounds: 6 }
  round: { timeLimit: 150s }
  team: { maxEmployees: 4 }
  limits: { entriesPerRound: 5, openClaims: 2, leaseRounds: 2, proposalsPerTargetPerHour: 3 }
  convergence: { minContributors: 2, minSources: 2 }
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
| `status` | `firing` or `resolved` |
| `title`, `labels`, `payload` | Content. Untrusted unless the mapping marks a field as trusted. |
| `occurredAt` | Source timestamp |
| `milestone` | Optional: a milestone name and time for an existing work item |

```yaml
kind: TriggerMapping
metadata: { name: alerts }
spec:
  source: alerting
  authentication: { hmacSecretRef: secret://inbound/alerting }
  trustedFields: [labels.rule_id, labels.service]
  workItemType: incident
  dedupKey: "{labels.service}:{labels.rule_id}"
  principal: { tool: directory.responsible_for, args: { service: "{labels.service}" }, field: "$.user" }
```

- **Authentication.** Inbound requests MUST be authenticated.
- **Routing uses trusted fields only.** This covers the work item type, the dedup key, the principal, and any hypothesis key value taken from the trigger. Untrusted fields appear as trigger evidence and never route.
- **Diagnosis-only items.** A mapping MAY declare consistency checks between trusted and untrusted fields. If a check fails, the item is diagnosis-only, and write tools are hidden from all of its runs.
- **Principal.** The principal is the human on whose behalf the work item runs. It is resolved through a read tool, via the gateway, under the platform's own identity. For an item opened in the UI, the principal is the person who opened it.

### 3.7 Configuration governance

| Change | Phase 0 | Before Phase 1 starts |
|---|---|---|
| Any change | Protected branch; schema validation; content hashes recorded; affected owners notified | Also signed commits |
| An employee | Reviewed by its owner | Same |
| Server registration, tool policy, approval policy, trigger mapping | Reviewed by a platform admin | Two reviewers, at least one of them not a platform builder |

The loader MUST refuse configuration that fails schema validation, references an unregistered server or tool, or gives an employee a tool that tool policy does not allow. The repository's review rules enforce who must review; the companion document says how.

## 4. Runtime objects

### 4.1 Work items and runs

A **work item** is one unit of work, opened by a trigger or by a human. It has:

- a type, trigger source, dedup key, principal, locked configuration version and `tenant_id`;
- an ADR-011 arm, `visible` or `invisible`, drawn at open from a recorded seed;
- milestone timestamps (Section 11);
- one primary run and zero or more shadow runs.

A **run** is one team configuration working the item. Each run has its own blackboard and budget. In the visible arm, humans see the primary run. In the invisible arm, the primary run executes in shadow mode (Section 7).

```
queued --> investigating --> awaiting_close --> closed
               ^                   |
               +-------------------+   extend budget, or a new firing trigger with the same dedup key
```

- **Queued.** An item waits here when the concurrency limit is reached. A responder may promote a queued item.
- **Stop reasons.** When the primary run stops, the item moves to `awaiting_close` and records why: `converged`, `quiet`, `budget_exhausted`, `trigger_resolved` or `not_staffed` (kill switch or daily token cap).
- **Closing.** A human may close an item from any state. An item in `awaiting_close` also closes automatically [30 min] after its trigger resolved.
- **On close.** Closing withdraws the item's pending proposals and stops a visible primary run. Shadow runs, and a primary run executing in shadow mode, continue to their own end (Section 7).

### 4.2 Blackboard entries

| Type | Written by | Content |
|---|---|---|
| `evidence` | employee, human, platform | A claim with provenance, and optionally a stance on a hypothesis: `supports` or `refutes` |
| `hypothesis` | employee | Key, statement and at least one evidence reference |
| `task` | employee | Title and tags; can be claimed |
| `proposal` | gateway | Created when an employee calls a write tool (Section 5.3) |
| `result` | gateway | Outcome of an executed proposal |
| `ruling` | human | Reject a hypothesis, close, extend budget, resolve `unknown`, mark a suggestion adopted, set a milestone |
| `notice` | platform | Trigger updates, failures and stop summaries |

Entries are append-only. Each carries its author, time, run and references.

**Evidence provenance.** Each evidence entry MUST have exactly one of:

- the id of a gateway read call made by the same employee in the same run, plus a quote that appears verbatim, after whitespace normalization, in that call's stored, redacted output;
- a human author, in the visible arm only;
- a platform record: a trigger event or a check result.

The platform rejects evidence that breaks these rules. Provenance proves that the call happened and that the quoted text exists. It does not prove the interpretation, which is why approvers and raters can open the raw output.

**Tasks.** `claim_task` is atomic, so a task has one holder at a time. A claim is released if no `complete_task` arrives within [2] rounds. An employee holds at most [2] open claims.

**Entry limit.** An employee posts at most [5] new entries per round.

### 4.3 Hypothesis keys, convergence and the conclusion

**Keys and merging**
- **Keys.** A work item type MAY define key fields. Each field has a closed value set, either given inline or produced by a read tool when the item opens (and then locked with the configuration). Every field also accepts `other`.
- **Merging.** Hypotheses with equal keys are the same hypothesis, so the platform merges them. The later one becomes an alias, and its evidence counts for the merged hypothesis. A key containing `other` never merges.

**Terms**
- **Support** is provenance-backed evidence with stance `supports`.
- **Contributor** is an employee or a human.
- **Source** is a (server, tool) pair, or "human observation".

**Converged.** A hypothesis has converged when all of these hold:
- support from at least [2] distinct contributors and at least [2] distinct sources;
- no provenance-backed evidence with stance `refutes`;
- no human has rejected it.

**Score.** A hypothesis's score is its distinct supporting contributors minus its distinct refuting contributors. Ties are broken by distinct supporting sources.

**Conclusion.** A run's conclusion, used for scoring in Section 13, is chosen in this order:
1. the highest-scoring converged hypothesis;
2. if none has converged, the highest-scoring hypothesis that no human rejected;
3. if the top two are still tied, the run has no conclusion.

### 4.4 Proposals and approvals

A proposal records:
- the tool and the exact arguments;
- the target key;
- the cited hypothesis;
- the policy check and any employee check;
- the fingerprint snapshot;
- the proposer.

| State | Reached from | When |
|---|---|---|
| `recorded` | (created) | Record-only mode: Phase 0 and shadow runs |
| `pending_approval` | (created) | Normal mode |
| `approved` | `pending_approval` | An approver approves |
| `rejected`, `expired`, `withdrawn` | `pending_approval` | An approver rejects; approval expiry passes; the cited hypothesis is rejected or the item closes |
| `voided` | `pending_approval`, `approved` | The fingerprint changed before execution, or the database was restored (Section 8.4) |
| `executing` | `approved` | Write-ahead persisted (Section 6.4) |
| `succeeded`, `failed`, `unknown` | `executing` | Outcome mapping (Section 6.4) |

- **Approval binding.** An approval binds to a hash of the tool, arguments, target, checks and fingerprint snapshot. Execution uses the stored arguments only.
- **Target lock.** At most one unfinished proposal per target, across all work items. Unfinished means `pending_approval`, `approved`, `executing`, or `unknown` until a human resolves it.
- **Resolving `unknown`.** A human resolves it as effective or not effective:
  - effective: the state becomes `succeeded` and the checks run;
  - not effective: the state becomes `failed` and the target is released.
- **Rate limit.** More than [3] proposals for one target within an hour pause new proposals on that target and notify its responders.

## 5. Work item lifecycle

### 5.1 Open

1. An authenticated inbound event arrives, or a human opens an item in the UI.
2. If an item that is not closed has the same dedup key, the event is appended to it as trigger evidence. A `resolved` event marks its trigger resolved. No new item is created.
3. Otherwise the coordinator creates the item. It draws the arm, locks the configuration, resolves the principal, and creates the primary run and the configured shadow runs.
4. If [3] items are already investigating, the new item waits in `queued`. If the kill switch is on or the daily token cap has been reached, the item opens with stop reason `not_staffed`.
5. Employees whose subscriptions match the work item type are invited, in configuration order, up to [4] per run.
6. In the visible arm only, one message with a link to the live board goes to the responders' channel (Section 10.3).

### 5.2 Rounds

**Pacing**
- **Rounds.** Runs advance in bulk-synchronous rounds. At the start of each round the coordinator freezes a snapshot. Each active employee receives the entries added since its previous snapshot.
- **Turns.** An employee works until it calls `end_turn` or the round time limit [150 s] passes.
- **Visibility.** Humans see each entry as soon as it is committed. Other employees see it only from the next round. Claims and target locks take effect in real time.
- **Round end.** When every active employee has ended its turn, or the time limit has passed, the coordinator works through, in order: lease expiry, convergence, stop conditions, joins, then the next round.
- **Joins.** When a task with a tag appears, employees subscribed to that tag join the run in the next round, up to the team limit.
- **No assignment.** Rounds set the pace; they do not assign work. Each employee chooses its own actions.
- **Sessions.** A session persists across the rounds of its run. If a session is lost, the runner starts a new one from the current snapshot.

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

### 5.3 Proposals

Employees call write tools as their runbooks describe. The gateway adds two arguments to every write tool's schema:
- `_hypothesis` (required): the hypothesis the action addresses;
- `_check` (optional): a read call plus a condition.

Both are stored with the proposal and never sent to the MCP server.

The gateway validates the arguments against the tool's input schema and its `candidates`. It then confirms that:
- the cited hypothesis exists and is not rejected;
- the proposer has itself posted supporting evidence for it (Section 9.2);
- the target is not locked, and its rate limit is not exceeded.

If every check passes, the gateway reads the fingerprint (if declared), creates the proposal and returns at once:
- in normal mode, `{"status": "pending_approval", "proposal": "P1"}`;
- in record-only mode, `"recorded"`.

### 5.4 Approval (Phase 1)

- **Approval page.** It shows platform facts separately from model text.
  - Facts: the exact tool and arguments, target, candidates, fingerprint snapshot, checks, the cited hypothesis with its evidence chain and raw outputs, live support and refutation counts, and the expiry.
  - Model-written explanations are shown as plain text marked "unverified".
- **Withdrawal.** When a human rejects a hypothesis, every pending proposal citing it is withdrawn.
- **Notifications** follow Section 10.3.

### 5.5 Execution and checks (Phase 1)

- **Before executing.** On approval the gateway reads the fingerprint again. If it changed, the proposal is `voided`. Otherwise the gateway executes with the stored arguments (Section 6.4) and posts a `result`.
- **Checks.** After success, the coordinator runs the policy check and the employee's check at their `after` delays. It runs them through the gateway under the platform's identity, and posts each outcome as platform evidence.
- **No check.** If the proposal has no check, a human confirms whether the action took effect.

### 5.6 Stop

A run stops when any of these happens:

- **Converged.** A hypothesis converged and one more wrap-up round has finished.
  - In the wrap-up round the gateway allows only two things: refuting evidence on the converged hypothesis, and write calls that cite it.
  - A provenance-backed refutation revokes convergence. If budget remains, normal rounds resume.
- **Quiet.** A whole round produced no new entries, claims or completions.
- **Budget exhausted.** Wall clock, tokens or rounds ran out.
- **Trigger resolved.** This takes effect at the end of the current round.

When a run stops, the coordinator posts a summary generated by rules, not by a model: hypotheses by score, the conclusion, open tasks, proposals, the stop reason and the budget used.

Budgets cover employee work only. Approval and execution have their own expiry.

### 5.7 Close and label

- **Closing.** An item closes by a human ruling or automatically (Section 4.1).
- **Labels.** Ground-truth labels are collected afterwards and blind. They do not come from responders during the work (Section 13.4).

## 6. MCP gateway

### 6.1 Employee-facing endpoint

- **One endpoint.** The gateway exposes a single MCP endpoint. Each session authenticates with its gateway token (Section 2).
- **Tool list.** `tools/list` returns the blackboard tools plus every tool allowed by both the employee's `tools` patterns and tool policy. Write tools are extended as in Section 5.3, and hidden in diagnosis-only items (Section 3.6). A hidden tool cannot be called, even by name.
- **Wrap-up round.** The tool list and argument rules narrow as Section 5.6 describes.

### 6.2 Read path

1. Check tool policy. Reject the call if it is not allowed.
2. Write the audit record: call id, run, employee, tool and arguments. If the audit write fails, reject the call.
3. Forward the call to the server with the server's credentials.
4. Redact the output, store the redacted output, and return it to the employee with the call id.

A read error goes back to the employee and into the audit record. It cannot be cited as evidence.

### 6.3 Record-only mode

In Phase 0 and in every shadow run, a write call goes through the checks in Section 5.3 and ends as `recorded`. Nothing is sent to any server, so no write credentials are needed. In the visible arm, recorded proposals appear as suggestions, and a responder can mark one as adopted.

### 6.4 Execution (Phase 1)

- **Write-ahead.** The gateway persists `executing` before it sends the request. If it cannot persist, it does not send.
- **Restart.** After a gateway restart, every proposal found in `executing` becomes `unknown`.
- **Outcome mapping:**

| Situation | Outcome |
|---|---|
| The connection was never established (refused, name resolution failed) | Retry automatically, at most [3] times while the approval is valid; then `failed` |
| A protocol-level rejection proving the call did not run, for example an unknown tool | `failed` |
| A tool execution error (`isError: true`) | `unknown`; or `failed` if tool policy declares `errorMeansNoEffect` |
| Any other protocol error, a timeout, or a lost connection | `unknown` |

- **Idempotent tools.** They get one automatic retry after `unknown`. After that, the outcome stands.
- **Resolving `unknown`.** A human resolves it as Section 4.4 describes.

## 7. Shadow mode

A shadow run is a candidate configuration working a live item. It:

- is hidden from humans until the item closes, and sends no notifications;
- ends every write call it makes as `recorded`;
- receives no human entries;
- keeps running when the item closes, until its own stop conditions are met, so its time to a correct conclusion is observed rather than cut off;
- is scored blind against the same labels as the primary run (Section 13.4).

Uses:

- **ADR-011 invisible arm.** The primary run executes in shadow mode.
- **Comparisons.** These are listed in `shadowRuns` (Section 13.3) and scored on invisible-arm items only, where no human input reaches any run.
- **Probation for changes.** A new model, skill version, employee or team configuration runs in shadow before it replaces the current one (Section 12.3).

Each shadow run has its own budget, by default equal to the primary run's. Shadow runs count toward the daily token cap, and they are cut first when the cap is reached.

## 8. Failure handling

### 8.1 Principles

1. **Doing too little beats doing too much.** Anything uncertain about a write becomes `unknown` and goes to a human. Only `idempotent` tools retry.
2. **Platform failure does not affect responders.** The platform only subscribes to triggers. When it fails, people fall back to their existing process.
3. **Nothing fails silently.** Every failure is audited and shown on the board as a `notice`.

### 8.2 Failure table

| Failure | Platform behavior |
|---|---|
| An employee session or sandbox crashes | The turn counts as ended. The next round restarts the session in a new sandbox and rebuilds its context from the board. After [2] consecutive failures, the employee leaves the run. |
| The round time limit is reached | The employee is interrupted. Committed entries stay; results of reads still in flight are discarded. |
| The LLM gateway is unavailable or rate-limited | The employee's turn ends, and the next round retries with backoff. The wall-clock budget keeps running. |
| A read call fails | The error goes back to the employee and is audited. It cannot be cited. |
| An MCP server's fingerprint changes | Changed tools are hidden at once, including in running items, and the owner is notified. |
| The coordinator crashes | All state is in the database. On restart the coordinator resumes rounds, leases, budgets and proposals. |
| The database or audit store is unavailable | The gateway rejects every call, because it audits before it calls. The platform stops; responders are unaffected. |
| A notification fails | The platform retries, then uses the fallback channel, then flags the failure in the live view. |
| The trigger flaps | A new firing with the same dedup key sends an item in `awaiting_close` back to `investigating` with the remaining budget. After the item has closed, a new item is opened and linked to the previous one. |
| Costs run away | The LLM gateway enforces a hard token limit per run. Once the daily cap is reached, new items are not staffed. Shadow runs are cut first. |
| A tool output contains sensitive data | The gateway redacts it before any employee sees it and before it is stored. |

### 8.3 Kill switch

An admin control that at once:

- stops staffing new items;
- cancels running sessions;
- rejects proposal executions, while pending proposals stay pending.

If the platform itself may be compromised, the companion document gives the deployment-specific steps for revoking write credentials.

### 8.4 Database restore

After the database is restored from a backup:

1. Freeze new proposals.
2. Void every proposal that is `pending_approval` or `approved`.
3. Mark every `executing` proposal `unknown`.
4. Start a new epoch, so that nothing approved before the restore can execute.
5. Reconcile with the target systems' own records where they exist (companion document).
6. Unfreeze.

## 9. Security

### 9.1 Threats and controls

| Threat | Main controls |
|---|---|
| Instructions injected through tool output (logs, tickets, trigger text) | Human approval of exact calls; least privilege per employee; argument candidates; data boundaries (Section 9.3) |
| Contagion between employees through the board | A proposer must hold its own evidence; convergence needs independent sources; entry limits |
| Fabricated evidence | Provenance and the verbatim quote check |
| Forged trigger fields | Authenticated inbound events; routing from trusted fields only |
| Tool poisoning, or silent changes to an MCP server's tools | Deny by default; fingerprint pinning; server annotations not trusted |
| Credential theft or data exfiltration from a sandbox | No credentials in sandboxes; egress only to the two gateways; redaction |
| Approval fatigue | Approval pages that separate facts from model text; approval-quality metrics (Section 13.6) |
| An insider changing configuration | Reviewed configuration (Section 3.7); owners notified; audit |
| Data sent to the model vendor | All model calls go through the LLM gateway; redaction happens before the model sees output; vendor terms (ADR-007) |

### 9.2 Deterministic rules

1. Evidence carries a verbatim quote of the output it cites (Section 4.2).
2. Convergence needs [2] contributors and [2] sources (Section 4.3).
3. A proposer must itself have posted supporting evidence for the hypothesis it cites. Without this rule, an employee whose context was injected could steer another employee that holds write tools, getting around least privilege.
4. An employee posts at most [5] new entries per round and holds at most [2] open claims.
5. Write arguments must fall within their declared candidates (Section 3.3).

### 9.3 Data boundaries and sandboxes

- **Data boundaries.** Board entries and tool outputs given to an employee are wrapped in explicit delimiters, labeled with author and provenance, and marked as data rather than instructions. This lowers injection risk; it does not remove it.
- **Sandboxes.** Runtime built-in tools that reach outside the sandbox are disabled, and host or workspace configuration never enters prompts (ADR-006).

### 9.4 Retention

Audit and trajectory data are encrypted at rest and kept for [30 days] by default. Raw outputs are stored only in redacted form.

## 10. Human interface

### 10.1 Roles

| Role | Can |
|---|---|
| Viewer | See boards of visible-arm items for their teams |
| Responder (the principal and the responder group) | Post evidence; reject hypotheses; extend budget; close; mark suggestions adopted; set a missing milestone; promote a queued item |
| Approver (per approval policy) | Approve or reject proposals; resolve `unknown` |
| Employee owner | Change their employee's configuration (Section 3.7); see its cost and metrics |
| Rater | Score blind packets (Section 13.4). A rater MUST NOT be a platform builder. |
| Admin | Use the kill switch; change platform configuration |

Sign-in uses the organization's single sign-on (companion document).

### 10.2 Views

- **Live board.** Entries in order, with author, provenance links, support and refutation counts, the conclusion, the stop reason and the budget used.
- **Suggestions** (Phase 0) and **approval inbox** (Phase 1).
- **Target locks.** Who holds each lock, and why.
- **Invisible-arm items** expose no board.

### 10.3 Notifications

- **Visible arm.** One message when the item opens, with a link to the board. After that, pushes go out only for:
  - decisions: proposals (Phase 1), `unknown` outcomes (Phase 1), and a run that stopped without converging;
  - conclusions: convergence.

  Everything else stays in the live view.
- **Invisible arm.** No notifications.
- **Channels** are configurable outbound integrations (companion document).

## 11. Milestones

- **Source.** A work item type lists its milestones (Section 3.5). Milestone times come from the deployment's milestone source, through inbound events. The same source MUST serve both arms.
- **Fallback.** A responder may set a missing milestone in the UI. The ruling records where the time came from.

## 12. Testing

### 12.1 Layers

| Layer | What it covers | How | When |
|---|---|---|---|
| Gateway contract | Every rule in Sections 3.3, 4, 5.3, 6 and 9.2 | A programmable fake MCP server records every request and injects timeouts, dropped connections, errors and tool-list changes. In Phase 1, the tests also kill the gateway at each step of execution. | Every commit; no model calls |
| Coordinator rules | Rounds, snapshots, convergence, quiet rounds, budgets, leases, key merging, limits, joins, dedup, stop and close | Scripted fake employees post entries from a script; the tests assert the coordinator's sequence of decisions | Every commit; no model calls |
| Employee smoke | Whether an employee version reaches a known cause within budget without overstepping | Fake MCP servers return canned data; 1–2 scenarios per employee, each run [5] times; the pass rate is reported | When an employee's configuration (model, skill version) changes, for that employee only |
| Red team | Injection, contagion between employees, entry flooding, claim hoarding, forged quotes, and sandbox probes for credentials or outbound access | Attack content planted in fake server outputs; probe scripts run inside the sandbox | Once before Phase 0 measurement starts; again whenever a security rule changes; write-path cases before Phase 1 |

### 12.2 Gateway state-machine properties (Phase 1)

Property-based tests generate random sequences of proposals, approvals, rejections, expiries, crashes, restarts and every error class. After each step they assert:

1. every write the fake server receives matches an approval with the same hash;
2. each approval executes at most once, apart from the single retry allowed for `idempotent` tools;
3. when a request reaches the fake server, its audit record already exists (the fake server checks on receipt);
4. at most one unfinished proposal exists per target at any time;
5. shadow runs and record-only mode never send a write.

In Phase 0, properties 3 and 5 are tested on their own.

### 12.3 Replay and probation

- **Replay.** Recorded items serve as regression tests. Because the coordinator is deterministic, any closed item can be replayed from its event log. Before a rule change is merged, all past items are replayed, and the decisions that would change are listed for review.
- **Probation.** A changed employee or team configuration replaces the current one only after:
  - its smoke scenarios pass;
  - it has run in shadow on at least [5] invisible-arm items without a safety violation;
  - its blind scores have gone to its owner and a platform admin, who decide and record the decision.
- **Freeze.** During an ADR-011 measurement window, the primary configuration is frozen. Promotions wait until the window ends.

## 13. Value validation

### 13.1 Arms

At open, the coordinator draws each item's arm from a recorded seed, at the preregistered invisible share [50%]:

- **Visible:** the normal flow.
- **Invisible:** the primary run executes in shadow mode, with no board, notifications or approvals.

### 13.2 Metrics

All definitions, thresholds and sample sizes are preregistered and signed before any data is seen (ADR-011).

| Metric | Role | Definition |
|---|---|---|
| Hit rate | Gate | Invisible arm only, where agent output cannot contaminate the labels. The share of items whose primary run's conclusion (Section 4.3) matches the ground-truth label. |
| False-convergence rate | Guardrail | Invisible arm only. The share of items whose primary run converged on a hypothesis that does not match the label. Being confidently wrong is worse for responders than reaching no conclusion. |
| Milestone durations | Direction only | The durations the deployment designates, compared between arms. There are too few items to gate on them; a clearly worse visible arm triggers an investigation. |
| Safety floor | Hard gate | No write reaches a server without a matching approval. In Phase 0, no write reaches a server at all. Where target systems keep their own records, reconcile against them weekly. |
| Responder feedback | Reported | Visible arm: an optional one-click rating of the conclusion. |
| Headroom | Phase 1 entry | The median of the duration the deployment designates as "waiting to act". In the first deployment this runs from cause identified to mitigation started. |

The hit rate counts only the conclusion. "Some hypothesis on the board was right" does not count: several employees can post a dozen hypotheses between them, and counting any of them would inflate the rate.

### 13.3 Comparisons

- **Collaboration versus independent work.** The `independent` shadow run (Section 3.5) is the same team limited to a single round with the full time budget. Employees never see each other's entries, and the platform aggregates their hypotheses by key.
  - It runs on invisible-arm items with the primary run's budget.
  - Paired, blind-scored results show whether multi-round collaboration earns its cost and complexity.
  - It is reported, not gated.
- **External comparators** (ADR-011). Their conclusions enter blind scoring as anonymous outputs. The comparators themselves are first-deployment content.

### 13.4 Blind scoring

- **Packets.** For each closed item the platform exports a rating packet. It holds a pointer to the deployment's ground-truth record, such as a review document, and every output for the item: primary run, shadow runs and external comparators.
- **Format.** Outputs are shuffled, stripped of their source and rendered in one format: hypothesis key plus one line of rationale. Without a single format, raters could tell sources apart by writing style.
- **Raters.** At least two raters who are not platform builders score each packet independently. The platform imports their scores, reports inter-rater agreement and computes the metrics.
- **Phase 0 tooling.** Phase 0 uses file export and import, not a rating UI.

### 13.5 Sample and stopping

- **Stopping rule.** Stop on sample size, not on time: evaluate once [20] invisible-arm items have been labeled, waiting at most [8 weeks].
- **Hits needed.** The observed hit rate needed for a 90% Wilson lower bound of at least 50%:

| Invisible items | Hits needed | Observed rate |
|---|---|---|
| 10 | 8 | 80% |
| 20 | 14 | 70% |
| 40 | 26 | 65% |

- **Invisible share.** It sets how long reaching [20] takes: at [50%] it needs about 40 in-scope items, at 70% about 29. A higher share shortens the window but leaves fewer visible items, and those carry responder feedback and the milestone comparison.
- **Builder-handled items** are excluded or reported separately (ADR-011).
- **Shortfall.** If [20] items are not reached within [8 weeks], the preregistered rule applies: evaluate the available items as directional, and decide on responder feedback and the safety record.

### 13.6 Cost and governance metrics

- **Cost.** The LLM gateway records tokens and spend per run, employee and arm. The MCP gateway records tool calls on the same dimensions.
  - Reported: cost per item, cost per hit and the shadow share.
  - A ceiling on average cost per item is preregistered as a guardrail.
  - Cost is shown per owning team. It is the price of the governance this platform exists for.
- **Owner autonomy.** Tracked figures:
  - the number of employees owned by teams other than the builders (target: at least 1);
  - the configuration changes those owners made themselves;
  - the time from request to live.
- **Least privilege in practice.** Tools granted versus tools used, per employee. Grants unused for [30 days] are flagged for removal.
- **Approval quality** (Phase 1). Tracked figures:
  - the distribution of time to approve;
  - the rejection rate;
  - the share of approvals where the approver opened the evidence first.

  Approving in seconds, never rejecting and never opening evidence is a rubber stamp.

### 13.7 Phase 0 gate

| Outcome | Decision |
|---|---|
| Safety floor broken | Stop measuring, fix the cause, restart the window |
| Hit-rate lower bound below [50%] | Iterate on skills and configuration for up to [3 weeks], then measure again. If it still fails, stop, or try an off-the-shelf diagnosis tool as an employee runtime (ADR-001). |
| Sample not reached by the deadline | Apply the preregistered rule (Section 13.5) |
| Gate passed; headroom median below [5 min] | Read-only is the end state for this phase; the write path is not built |
| Gate passed; headroom exists | Start Phase 1 |

## 14. Delivery plan

### 14.1 Phases

| Phase | Platform scope |
|---|---|
| Spike | Runtime isolation (ADR-006); a census of available read-only MCP servers |
| Phase 0 (read-only, full collaboration) | Section 3 in full, with approval policies loaded but unused; Section 4 in full, with proposals only `recorded`; Sections 5.1–5.3 and 5.6–5.7; Sections 6.1–6.3; Section 7; Sections 8.1–8.3; Section 9; Section 10 without the approval inbox; Section 11; Section 12 except write-path properties; Section 13 |
| Measurement | Until [20] invisible-arm items are labeled, at most [8 weeks]. Phase 1 design may proceed in parallel; building waits for the gate. |
| Phase 1 (write path) | Sections 5.4–5.5; Section 6.4; Section 8.4; the approval inbox; Section 12.2; write-path red team; first-deployment write tools |

### 14.2 Estimate

| Phase | Person-weeks |
|---|---|
| Spike | about 1 |
| Phase 0 | 15–21 |
| Phase 1 | 6–8 |

These are rough estimates. They assume an experienced engineer making heavy use of AI coding assistants, and they include integration and testing.

- **2 people:** spike and Phase 0 take about 3–4 months, and measurement takes 1–2 months. The gate falls around month 4–6, and the write path goes live around month 5–8.
- **1 person:** Phase 0 alone takes about 5–7 months.
- **Capacity.** 2 people for 2 months is about 16 nominal and 12 effective person-weeks. A timeline of 1–2 months cannot produce a value decision: measurement alone needs 4–8 weeks of wall-clock time after Phase 0 is built.
- **Compared with the superseded v1 spec.** It estimated 8–11 person-weeks for a single-agent design, and the third document review raised that to 14–20. Full collaboration in Phase 0 adds about 3–4 person-weeks and delays the gate by about a month.

### 14.3 What others must provide

- The team lead's consent to hide agent output at random (ADR-011);
- someone who is not a builder, to co-sign the preregistration;
- at least two raters who are not builders, for about 10 minutes per item;
- at least one other team to own an employee (Section 13.6).

## 15. Parameters

| Parameter | Default | Section |
|---|---|---|
| Run budget: wall clock / tokens / rounds | [15 min] / [2,000,000] / [6] | 3.5 |
| Round time limit | [150 s] | 5.2 |
| Employees per run | [4] | 5.1 |
| Items investigating at once | [3] | 5.1 |
| New entries per employee per round | [5] | 4.2 |
| Open claims per employee | [2] | 4.2 |
| Claim lease | [2] rounds | 4.2 |
| Convergence: contributors / sources | [2] / [2] | 4.3 |
| Proposals per target per hour | [3] | 4.4 |
| Approval expiry | [60 min] | 3.4 |
| Connection retries before `failed` | [3] | 6.4 |
| Consecutive session failures before removal | [2] | 8.2 |
| Auto-close after the trigger resolved | [30 min] | 4.1 |
| Daily token cap | Set by the deployment | 8.2 |
| Retention | [30 days] | 9.4 |
| Unused-grant flag | [30 days] | 13.6 |
| Smoke runs per scenario | [5] | 12.1 |
| Shadow items before promotion | [5] | 12.3 |
| Invisible share | [50%] | 13.1 |
| Invisible items for the gate / maximum window | [20] / [8 weeks] | 13.5 |
| Hit-rate gate | 90% lower bound ≥ [50%] | 13.7 |
| Headroom threshold | Median ≥ [5 min] | 13.7 |
| Iteration after a failed gate | [3 weeks] | 13.7 |

## 16. Documents and ADRs

These changes are carried out after this spec is approved.

| ADR | Action |
|---|---|
| 001 Wedge: ops incidents | Keep the decision, and move its technology facts to the companion document. Candidate E (off-the-shelf tools) becomes a candidate employee runtime, not a replacement for the platform. |
| 002 Single agent by default | Superseded by ADR-012. |
| 003 Identity is versioned configuration | Keep. Shadow runs execute several versions at once, which meets its re-review trigger; confirm that content hashes support this. |
| 004 Postgres plus a task library | Keep. The gateway now executes writes in Phase 1, so the premise "no fencing, no separate authorization store" no longer holds. Add the restore procedure (Section 8.4). |
| 005 MCP plus effect adapters | Superseded by ADR-013. Untrusted annotations, hash pinning and candidate arguments carry over. |
| 006 Agent runtime spike | Keep. Add three criteria: round-based turns, gateway-only MCP, and built-in tools that reach outside the sandbox disabled. |
| 007 One model vendor behind an LLM gateway | Keep. Add hard per-run token budgets and cost attribution. |
| 008 Single-organization deployment | Keep. Drop the rationale "the platform holds no cluster write access"; that is now a first-deployment choice. |
| 009 Capability packs | Superseded by ADR-014. |
| 010 Draft PR plus required check | Leaves the platform layer and becomes candidate hardening for first-deployment write tools in Phase 1. Its core idea, approval bound to exact content, is now invariant 2. |
| 011 Measurement | Keep. Rebase it on shadow mode, and add the strict hit-rate definition, the false-convergence guardrail and stopping on sample size. |
| 012 (new) | Multiple employees on a structured blackboard. The rationale is governance, so this phase has no team-versus-single comparison. |
| 013 (new) | The platform MCP gateway as the single policy enforcement point: tool policy, approvals bound to exact calls, audit before call, and `unknown` outcomes. |
| 014 (new) | The configuration model: employees, skills (runbooks included), MCP server registrations with tool policy, work item types, trigger mappings and approval policies. |
| 015 (new) | Implementation: Go, domain-driven design, interface-driven ports and adapters (Section 2.2). This settles the open implementation-language question. It constrains ADR-004 (a Go task library), ADR-006 (how the runtime fits a Go platform) and the condition language (Section 3.3). |

Other documents:

- **Superseded documents.** The superseded v1 spec and the target-state document get a "superseded" banner; their bodies stay as they are.
- **Facts and spikes.** `drafts/2026-10-10-facts-and-spikes.md` gets a status note:
  - spike C stays in the spike phase;
  - spike A and the code-host part of spike B move to Phase 1;
  - the read-only part of spike B stays in the spike phase.
- **Language.** New and revised ADRs are written in English.

## Appendix: Glossary

| Term | Meaning |
|---|---|
| Employee | A digital employee: a versioned configuration (runtime, model, skills, tool grants) with a human owner |
| Work item | One unit of work, opened by a trigger or a human |
| Run | One team configuration working one item, with its own blackboard and budget |
| Primary run / shadow run | The run of record / a candidate run hidden from humans (Section 7) |
| Principal | The human on whose behalf an item runs |
| Responder | A human handling the item: the principal and the responder group |
| Proposal | A write call held by the gateway until approved, or only recorded |
| Target | The lock key of a write, taken from tool policy |
| Conclusion | The hypothesis a run is scored on (Section 4.3) |
| Arm | `visible` or `invisible` (ADR-011) |
