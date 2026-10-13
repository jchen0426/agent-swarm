# Phase 1 Write Path: Design Backlog

| | |
|---|---|
| Status | Backlog. Not normative. |
| Date | 2026-10-11 |
| Role | Drafts the write path that the platform spec defers (spec Section 4.4), and records its open issues. It is designed and reviewed after the gate, with the census facts, and only then becomes part of the spec. |
| Bound by | The principles of spec Section 4.4, and invariants 1, 2, 6 and 7 of spec Section 2.1 |

## 1. Why this is deferred

- **Whether** Phase 1 is built depends on the gate: action accuracy and headroom (spec Section 13.7).
- **How** it is built depends on facts that only the census and the code host can give: how long the second person takes, who merges at night, whether a merge queue exists, and how code-host accounts map to identities (a deployment's census and its Phase 1 question; example deployment profile, questions 1 and 16).
- Review round 2 found open issues in this draft in exactly those places (Section 9). Settling them now, before those facts exist, would mean guessing.

## 2. Draft: tool policy for writes

Phase 1 adds these fields to the write-tool policy of spec Section 3.3:

```yaml
    rollback_release:
      # ... Phase 0 fields: schemaHash, effect, target, scope, candidates, summary, facts
      approval: second-human
      timeout: 120s
      operationId: request_id                    # filled by the gateway; hidden from agents
      idempotent: true                           # reviewed: the server deduplicates on request_id
      errorMeansNoEffect: false
      fingerprint: { tool: get_release, args: { service: "{args.service}" }, field: "$.current" }
      checks:
        - { name: healthy, tool: get_release, args: { service: "{args.service}" }, condition: "result.status == 'healthy'", after: 120s }
      reconcile: { tool: get_release, args: { service: "{args.service}" }, condition: "result.lastRequestId == operationId && result.lastRequestBy == writeIdentity && result.lastRequestDone" }
```

| Field | Meaning |
|---|---|
| `approval` | Name of an approval policy (Section 3). |
| `timeout` | Execution timeout. On timeout the outcome is `unknown`. |
| `operationId` | Name of an argument that the gateway fills with the proposal's operation id. It is removed from the schema agents see. |
| `idempotent` | A reviewed claim that repeating the call with the same arguments, operation id included, has the same effect. It allows one automatic retry after `unknown`. |
| `errorMeansNoEffect` | A reviewed claim that a tool execution error means nothing changed. On a first attempt such errors become `failed` instead of `unknown`. |
| `fingerprint` | A read made when the proposal is created and again before execution. If its field differs, the proposal is voided. If the read fails or the field is absent, the proposal is refused at creation and voided at execution. |
| `takesEffect` | For a write whose effect starts later through someone else's action, such as a merge: a read plus a condition that holds once the effect has started, and a deadline. Its result is available to checks as `effect`. |
| `checks` | Named post-effect checks: a read plus a condition, an `after` delay from the start of the effect, an optional `window` to poll within, and an optional `unverifiedWhen` condition. |
| `reconcile` | A read plus a condition, run while an outcome is `unknown`. The condition MUST compare the record's operation id with `operationId`, check the server's `writeIdentity`, and test that the operation completed, not merely started. |
| `writeIdentity` | Server level: the identity the server's writes carry. |

Proposed additions from review round 2: `preview` (issue 3) and `awaits` (issue 4).

## 3. Draft: approval policy

A generic example. A deployment's draft policy may let the principal approve; this one excludes the principal. Issue 2 records that choice as open.

```yaml
kind: ApprovalPolicy
metadata: { name: second-human }
spec:
  approver: { human: true, exclude: [principal], groups: [responders-secondary] }
  escalation: [ { after: 5m, groups: [team-leads] } ]
  expiresAfter: 60m
```

- **Groups** come from the identity provider's claims, or from a registered identity source (spec Section 3.6).
- **Exclusions** may name the principal, the owner of the proposing agent, or both.
- **Escalation.** If nobody in the approver groups acts within `after`, the request also goes to the escalation groups. Whether they may approve is open (issue 1).
- **Expiry** counts from proposal creation. A proposal that has not started executing by then expires, approved or not.
- **Related rulings.** The same policy decides who may resolve `unknown`, release a held target and confirm an effect.

## 4. Draft: proposal and effect states

| State | Reached from | When |
|---|---|---|
| `pending_approval` | (created) | Normal mode |
| `approved` | `pending_approval` | An approver approves |
| `rejected` | `pending_approval` | An approver rejects |
| `expired` | `pending_approval`, `approved` | The policy's expiry passed before execution started, or the kill switch was turned on |
| `withdrawn` | `pending_approval`, `approved` | The cited hypothesis was rejected, the item closed, or a responder took over |
| `voided` | `pending_approval`, `approved`; `executing` only while no request has reached the server | The fingerprint or the tool's schema hash changed, or the database was restored |
| `executing` | `approved`; `unknown` (the single retry of an idempotent tool) | Write-ahead persisted |
| `succeeded`, `failed`, `unknown` | `executing` | Outcome mapping (Section 5) |
| `succeeded`, `failed` | `unknown` | A reconcile condition holds (`succeeded`), or a human resolves it |

A `succeeded` proposal then has an effect:

| Effect | Reached from | When |
|---|---|---|
| `pending` | `succeeded` | Tool policy declares `takesEffect` |
| `started` | `succeeded` (no `takesEffect`); `pending` | The `takesEffect` condition holds |
| `abandoned` | `pending` | Its deadline passed, the item closed, the cited hypothesis was rejected, or a responder took over |
| `confirmed` | `started` | Every policy check passed, or a human confirmed the effect |
| `held` | `started` | A policy check failed or could not run, or is waiting for a human to confirm |

- **Approval binding.** An approval binds to a hash of the tool, its schema hash, the arguments, operation id, epoch, target, checks and fingerprint snapshot. Execution uses the stored arguments only.
- **Target lock.** At most one unfinished proposal per target, across all work items. Unfinished means `pending_approval`, `approved`, `executing`, `unknown`, or `succeeded` with its effect `pending` or `started`. A key covers every longer key that extends it.
- **Held target.** A target whose last proposal's effect is `held` takes no new proposal until an approver releases it.
- **Refusals are visible.** A write call refused because its target is locked, held or rate-limited posts a notice on the caller's board, naming the item that holds the target. Approvers get a link to resolve or release, and a reminder every [15 min].
- **Resolving `unknown`.** Only a person who satisfies the proposal's approval policy may resolve it, as effective (`succeeded`, and the effect proceeds) or not effective (`failed`, and the target is released). The screen shows the operation id, where to look it up, the reconcile results, and a warning that "not effective" releases the target.
- **Rate limit.** More than [3] proposals for one target within an hour pause new proposals on that target and notify its responders.
- **Take over.** A responder may take over an item: its visible primary run becomes record-only, its proposals not yet executing are withdrawn, and its operations awaiting effect are abandoned.

## 5. Draft: approval page, execution, effects and checks

**Approval page.** Laid out for phones first, in this order:
1. the action as the tool's `summary`, its target, and a countdown to expiry;
2. the cited hypothesis, whether it converged, and its strongest supporting and refuting quotes;
3. the checks that will run, and when the effect is expected to start;
4. collapsed: raw outputs opened at the quoted text, candidates, the fingerprint;
5. model-written explanations last, marked "unverified".

If someone else already decided, the page says who and when. If the proposal was withdrawn, voided or expired, or the kill switch is on, it offers no approve button. If support or refutation counts change while it is open, a banner asks for confirmation again. Approval happens on this page only, never from a notification, and needs fresh authentication.

**Execution**
- **Operation id.** Each proposal gets a random, globally unique, never-reused operation id. If tool policy names an `operationId` argument, every attempt carries it, and the server can deduplicate on it. Without it, the platform promises at most once and marks anything in doubt `unknown`.
- **Write-ahead.** The gateway persists `executing` before it sends the request; if it cannot persist, it does not send. After a gateway restart, every proposal found in `executing` becomes `unknown`.
- **Before every attempt.** The gateway checks the epoch and re-reads the fingerprint.
  - Before the first attempt, and before a retry after a connection that was never established, a change voids the proposal, because nothing has run.
  - Before the retry of an idempotent tool from `unknown`, a change or a failed read cancels the retry, and the outcome stays `unknown`.

**Outcome mapping**

| Situation | Outcome |
|---|---|
| The connection was never established | Retry automatically, at most [3] times while the proposal has not expired; then `failed` |
| A protocol-level rejection proving the call did not run | `failed` |
| A tool execution error (`isError: true`) | `unknown`; or `failed` if tool policy declares `errorMeansNoEffect` and no earlier attempt ended `unknown` |
| Any other protocol error, a timeout, or a lost connection | `unknown` |

- **After `unknown`.** Later errors map to `unknown`. Only the single idempotent retry, a reconcile condition or a human decides.
- **Reconcile.** Run while the outcome is `unknown` (for an idempotent tool, after its retry), up to [3] times, [60 s] apart. A condition that holds makes the outcome `succeeded`; one that does not never decides, and its results go to the human.

**Effect and checks**
- **Effect.** Without `takesEffect`, the effect starts when the call succeeds. Otherwise the gateway polls the `takesEffect` read until its condition holds or its deadline passes. While pending, the target stays locked.
- **Abandoned.** If the deadline passes, the item closes, the cited hypothesis is rejected or a responder takes over while the effect is pending, the operation is `abandoned` and responders are notified. The deployment MUST make an abandoned operation unable to take effect.
- **Checks.** Once the effect starts, each policy check runs at its `after` delay, polling within its `window`, under the platform's identity, even if the item has closed. Outcomes: `passed`, `failed`, `unrunnable` (the read still failed after [3] attempts), or `unverified` (its `unverifiedWhen` condition held).
- **Outcome.** All passed: `confirmed`, target released. Any failed or unrunnable: `held`, responders paged, target held until released. Unverified or no policy check: a human confirms, and the target is held until then.
- **Back to diagnosis.** If the item is still open when a check fails, the failure is posted as evidence and the run resumes if budget remains.

## 6. Draft: notifications, kill switch and restore

- **Pages:** proposals awaiting approval, `unknown` outcomes, failed or unrunnable checks, effects that missed their deadline.
- **Messages:** rate-limit pauses and effects that started.
- **Reversals:** a pushed proposal that was voided, failed, withdrawn or expired; an abandoned operation.
- **Views.** An approval inbox, personal to each approver: what they may approve, ordered by time to expiry. A target-lock view: who holds each lock or held target, why, and since when.
- **Kill switch.** It rejects executions: pending and approved proposals expire, and their targets are released. Its state lives outside the restorable database.
- **Database restore:**
  1. Freeze new proposals.
  2. For every `approved` proposal, run its reconcile read; a proposal proven executed becomes `succeeded`, and every other `pending_approval` or `approved` proposal is voided.
  3. Mark every `executing` proposal `unknown`; reconcile reads then run.
  4. Start a new epoch, derived from the restore time, kept outside the restorable database, and checked before every attempt.
  5. Reconcile with the target systems' records. An operation id found there whose proposal is not `succeeded` marks a write the restore lost; its target is held until an approver reviews it.
  6. Check audit continuity against the external audit sink.
  7. Unfreeze.

## 7. Draft: properties

Property-based tests generate random sequences of proposals, approvals, rejections, expiries, effects, checks, crashes, restarts and every error class. After each step they assert:

1. every write the fake server receives matches an approval with the same hash;
2. each approval executes at most once, apart from the single retry allowed for `idempotent` tools;
3. when a request reaches the fake server, its audit record already exists;
4. at most one unfinished proposal exists per target at any time, and none names a held target;
5. shadow runs and record-only mode never send a write;
6. every attempt for one approval carries the same operation id, and an outcome leaves `unknown` without a human only through the single idempotent retry or a reconcile condition that holds;
7. no check starts before its effect has started, and no target is released while its proposal's effect is `pending` or `started`;
8. no attempt executes with a stale epoch or a changed fingerprint.

## 8. Draft: Phase 1 evaluation

Preregistered before Phase 1 is built, it compares actions proposed through the platform with manual ones: the designated waiting-to-act duration and the time to recover; the share of executed writes whose checks failed or that people reversed; and approval latency by time of day. If the platform path is slower, or causes more failed or reversed actions, the preregistered stop rule switches the write path off for the affected tools. Issue 20 questions its design.

## 9. Open issues from review round 2

**A. Approval roles and two-person control**

| # | Issue | Raised by |
|---|---|---|
| 1 | A pull-request write path may take its `open-pr` approvers, the principal and responders, from `catalog`. The approval policy cannot express item roles, and `catalog` is not yet an identity source. Either register those fields as an identity source (example deployment profile, question 10), or require each approver to be both an item responder and a member of an identity-provider approver group. Decide whether escalation groups may approve. | security, feasibility |
| 2 | Role inversion: the second person approves on the platform, where they are paged and see the evidence and the diff, and the principal approves and merges in Git. The required check still enforces two different people. This would solve issues 3 and 4, and puts the independent check where the platform can measure it. Whether the principal may approve at all is open: a deployment's draft may allow it, and the generic example of Section 3 excludes it. | adversarial, product-lens, design |
| 3 | The platform approver never sees the change that the server generates. Add a `preview` read that renders the exact change without writing; bind its hash into the approval; the server refuses different content, and the required check compares the merged tree with it. | security |
| 4 | Nobody pages or briefs the person whose action starts the effect (the Git merger). Add `awaits` to tool policy: what is awaited and from which group. Page that group when the effect becomes pending, excluding the approver, with a reminder at half the deadline. Put the approver, the deadline, the incident and a board link into the pull request's facts. | design, adversarial, feasibility |
| 5 | The two-person check needs an authoritative mapping from code-host accounts to single sign-on identities (example deployment profile, question 16), failing closed for anyone unmapped. | feasibility |

**B. The required check and fencing**

| # | Issue | Raised by |
|---|---|---|
| 6 | Who publishes the required check is undefined. Proposal: a separate, minimal check service with its own App and key, pinned as the check's expected source in the ruleset. It passes pull requests the bot did not author without contacting the platform, so human mitigations never depend on the platform, and it fails closed for bot pull requests when the platform cannot confirm. Fencing actions that can only prevent an effect (failing a status, closing an unmerged pull request) become a recorded exception to "every side effect is an approved MCP call". | security, feasibility |
| 7 | The check service and the write server need operation state that no interface exposes. Proposal: an authenticated, read-only operation-status port keyed by operation id, returning proposal and effect state, epoch validity, the digest of the approved content and the approver's identity, and failing closed. | feasibility |
| 8 | Abandonment races the human merge, and a green check stays green during outages and after restores. Proposal: two-phase abandonment. In `abandoning`, the target stays locked while the check is failed and the pull request closed; a final read then confirms closed and unmerged (`abandoned`), or finds a late merge (`started`, with a notice and checks). Evaluate the check at merge time through a merge queue if the plan allows (open fact 4); otherwise flip every open agent pull request on abandonment, at startup and before unfreezing after a restore, and accept the residual window. | adversarial, security, feasibility |
| 9 | The kill switch leaves pull requests awaiting effect mergeable. Add it to the abandonment triggers; fence open agent pull requests first and suspend the App only afterwards; give code-host admins a way to close all bot pull requests without the App. | security, adversarial, feasibility |
| 10 | Bot App permissions: contents and pull requests only, with no workflows, administration or checks permission. Reserve `agent/**` for the bot, and match restore reconciliation on bot authorship as well as the branch name. | security |

**C. State machine**

| # | Issue | Raised by |
|---|---|---|
| 11 | Human effect confirmation has no ruling, role, page or screen, and `held` has no exit. Add an `awaiting_confirmation` effect and a "confirm effect: took effect / did not" ruling under the approval policy; "did not" holds the target and resumes the run. Page the responders, naming the unverified check, and show "held: check failed" apart from "awaiting confirmation". | design, feasibility |
| 12 | The state tables lack `approved → succeeded` (restore reconcile) and `awaiting_close → investigating` (a failed check resuming the run). | feasibility |
| 13 | A deployment's targets can overlap (service × environment and service × environment × key). The spec's prefix rule now covers it; confirm the tools share the prefix. | feasibility |
| 14 | Executions need an owner lease per `executing` proposal, so a starting gateway does not mark another instance's in-flight write `unknown`. | feasibility |
| 15 | A degraded server must not void approvals or hold targets: retry a failed fingerprint read before voiding, and keep reserved capacity for platform reads (now in spec Section 6.2 for Phase 0). | adversarial |

**D. Control state and audit**

| # | Issue | Raised by |
|---|---|---|
| 16 | A control-state port backed by a store outside the database: the kill switch, the epoch and a monotonic execution counter. Fail closed for executions when it is unreachable, and compare the execution counter at startup, so that a silent rollback (replica failover, point-in-time recovery) starts the restore procedure by itself. | feasibility |
| 17 | A full external audit sink holding content-free records (ids, actors, decisions, times, hashes), owned by someone other than the builders, and covered by the retention table. | security |
| 18 | A separate executor for write credentials becomes a MUST. It verifies an approval record signed by the human interface with a key the gateway does not hold, and checks the epoch and the kill switch itself. The write server's key sits in a signing service that never releases it. | security |

**E. Measurement and evaluation**

| # | Issue | Raised by |
|---|---|---|
| 19 | Headroom counts only the time to prepare the change, minus the platform approval's own latency, unless the design also shortens the second person's wait (see issue 2). Deployment profiles should count it this way. | product-lens, adversarial |
| 20 | The Phase 1 evaluation compares self-selected actions. Keep incident-level randomization (proposals offered or not), compare by intention to treat, preregister minimum counts before the stop rule can fire, and define failure and reversal the same way for both paths. | adversarial |
| 21 | Approval quality covers only the platform approval. Measure the Git reviewer's latency and quality as well. | adversarial |

**F. Interface**

| # | Issue | Raised by |
|---|---|---|
| 22 | The approval page needs idempotent submission and result states (success with the next step; refused as expired, decided, voided or ineligible; failed re-authentication; unknown after a timeout), a refusal when counts changed since rendering, a connection indicator, an ineligible-viewer state, reject inputs with "reject the hypothesis" beside them, and a fresh-authentication window [5 min]. | design |
| 23 | When a paged request is handled, other recipients' pages are resolved with "handled by X at T", and reminders stop. | design |
| 24 | After an operation is abandoned, expired or voided, responders have no way to ask for the same mitigation again, because only agents create proposals. | product-lens |

## 10. Draft estimate

The draft above was estimated at 10–12 person-weeks. Issues 6–9 and 16–18 add a check service, a status port and a control-state store, so the estimate is redone with the design.
