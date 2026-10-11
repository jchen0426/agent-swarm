# First Deployment: Ops Incident Handling

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-11 |
| Role | Domain content for the first deployment of `2026-10-11-agent-swarm-platform-spec.md`. Nothing here is normative for the platform. If the two documents conflict, the platform spec wins. |
| Replaces | The domain parts of the earlier v1 spec and facts-and-spikes draft, and ADR-010. The old documents are kept outside this repository. |

## 1. Scenario

- **Workflow:** alert → diagnosis → human-approved mitigation (Phase 1) → recovery check.
- **Out of scope:** planned change requests.
- **Users:** the author's internal team, in a single organization (ADR-008).

## 2. Confirmed facts (2026-10-10)

| Fact | Consequence |
|---|---|
| More than 10 incidents a week need human handling | An 8-week window yields roughly 40–80 incidents, if half to all of them are in scope (platform spec Section 13.5) |
| The most common mitigations change configuration, feature flags, dependencies or infrastructure. Restarts and rollbacks come next; scaling is rare. | Phase 1 write tools cover rollback, restart, and configuration keys held in Git. There are no replica-count writes. |
| Deployments are managed by Argo CD with automated sync or self-heal | Direct cluster writes would be reverted, so Phase 1 write tools change Git through pull requests (Section 5.1) |
| A responder cannot roll back or restart alone; a second person approves, or the CD flow is used | Platform approval plus the existing Git review keeps two-person control |
| Logs may be sent to a model vendor under zero-retention, no-training terms | Hosted models are allowed; the gateway still redacts |

## 3. Open facts

| # | Question | How to find out | Affects |
|---|---|---|---|
| 1 | Does Flux run alongside Argo CD? Where does each production application's target revision point? Are there ApplicationSets or multi-source applications? Do applications deploy to several clusters? | Commands A | Phase 1 checks; spike A |
| 2 | Mitigation census for the last 8 weeks: for each incident, the action taken (revert, restart, configuration key in Git, flag platform, dependency, infrastructure, other) and the system it ran in | Incident platform export plus the incident channel, one incident at a time | Which Phase 1 write tool comes first; write coverage |
| 3 | How image versions reach Git (Argo CD Image Updater, Flux image automation or a CI bot), and which bots push to target branches | Commands B | Rollback semantics; whether automation must freeze during a rollback |
| 4 | Is this github.com or GitHub Enterprise Server, and on which plan? What branch protection or rulesets exist? Which merge method, and what default squash message? Are branches deleted automatically after merge? | Repository and organization settings | ADR-010 hardening: rulesets on private repositories need the Team or Enterprise plan |
| 5 | What CI runs on pull requests to the GitOps repository, and does it use secrets? | Workflow files; secrets and environments settings | Whether "only a merge has an effect" holds |
| 6 | Are the responders and the team lead code owners of the relevant paths? How does second-person approval work at night today? | CODEOWNERS; the on-call rotation; ask the team lead | Approval policy and escalation |
| 7 | Does the incident platform already record acknowledged, cause identified, mitigation started and recovered, plus a root-cause field? | Incident platform field settings | Milestone source; ground-truth source |
| 8 | Is the platform's configuration repository the same repository as the GitOps repository? | Check the repositories | Trust root for configuration governance |
| 9 | May production log content, including personal data, be stored in the trajectory store and in smoke scenarios? | Ask the security or compliance owner | Retention and redaction rules |
| 10 | Are the builders in the on-call rotation, and how many person-weeks can they really spend? | Ask the team | Estimate and timeline (platform spec Section 14.2); builder exclusion (ADR-011) |
| 11 | Does the team lead agree to hide agent output at random for part of the incidents (ADR-011)? May a small share stay hidden after the gate (platform spec Section 13.8)? | Ask the team lead | Whether the measurement design can run at all; how trustworthy monitoring is after the gate |

**Commands A (fact 1)**

```bash
kubectl get applications,applicationsets -A
kubectl get applications -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\t"}{.spec.source.targetRevision}{.spec.sources[*].targetRevision}{"\t"}{.spec.destination.server}{.spec.destination.name}{"\n"}{end}'
kubectl get crd -o name | grep fluxcd.io   # empty output means no Flux
```

**Commands B (fact 3)**

```bash
git -C <gitops-repo> log --since='8 weeks ago' --format='%an <%ae>' | sort | uniq -c | sort -rn
kubectl get deploy -A | grep -Ei 'image-updater|image-automation|image-reflector'
```

## 4. Phase 0 content (read-only)

### 4.1 MCP servers

Prefer existing MCP servers that pass review. Where none exists, write a thin server over the same read API. The census spike (Section 6) confirms this list.

Every server must report partial results as errors (platform spec Section 3.3). Examples are a metrics query that returned partial data with warnings, and a log query that hit its result limit. Review checks this before registration.

| Server | Purpose | Constraints |
|---|---|---|
| `metrics` | Query metrics and alert state | Read only |
| `logs` | Query logs | Read only; redaction (Section 4.5) |
| `cluster` | Workloads, events, rollout status | Kubernetes read-only role, without `secrets`, `pods/exec` or other sensitive subresources |
| `cd` | Argo CD application sync state, health and history | A dedicated Argo CD local account with read-only access to applications. Verify that `policy.default` grants nothing more. |
| `gitops` | Recent changes per application path | Read only |
| `catalog` | Services, owners, current on-call | Read only. Supplies hypothesis-key values and the principal. |
| `incidents` | Milestones and the post-incident root cause | Read only. Milestones also arrive as inbound events. |

### 4.2 Agents

| Agent | Owner | Knows (skills and runbooks) | Tools | Subscriptions |
|---|---|---|---|---|
| `release-investigator` | Platform builders at first | Deployments, configuration changes, rollbacks | `cd.*`, `gitops.*`, `metrics.*` | `incident`; tags `release`, `config` |
| `runtime-investigator` | Platform builders at first | Workloads, nodes, networking | `cluster.*`, `logs.*`, `metrics.*` | `incident`; tags `runtime`, `infra` |
| `dependency-investigator` | A team other than the builders, for example the database team | Databases, queues, external dependencies | `metrics.*`, `logs.*`, database read tools | `incident`; tags `dependency`, `database` |
| `challenger` | Platform builders | How to look for evidence against a converged hypothesis | `metrics.*`, `logs.*`, `cluster.*`, `cd.*`, `gitops.*` | Challenger for `incident` (platform spec Section 5.6) |

At least one agent must be owned by a team other than the builders (platform spec Section 13.6).

- **Challenger.** It runs on a different model from the investigators where the vendor offers one, so that its mistakes correlate less with theirs. In the first measurement window it works only in the `challenged` shadow run (Section 4.8).
- **Skills.** Each skill names its owner (platform spec Section 3.2). Where a team already keeps a runbook for a system, that runbook becomes the skill and keeps its owner.
- **Absence.** Every investigator's runbook says to confirm a query's scope (service, environment, time range) before posting evidence that something is absent.
- **Suggestions.** In Phase 0, `release-investigator` and `runtime-investigator` also hold record-only write tools (Section 4.9).

### 4.3 Work item type `incident`

- **Hypothesis key:** `service` × `failureMode`.
  - `service` comes from `catalog`.
  - `failureMode` is one of `bad-deploy`, `config-change`, `dependency-failure`, `resource-exhaustion`, `traffic-surge`, `infrastructure-failure`, `external-provider` or `other`.
- **Milestones:** `acknowledged`, `cause_identified`, `mitigation_started`, `recovered`.
- **Designated durations:**
  - diagnosis: acknowledged → cause identified;
  - mitigation wait: cause identified → mitigation started (this is the headroom metric);
  - time to mitigate: acknowledged → recovered.
- **Budgets and limits:** platform defaults.
- **Challenge:** off in the primary run during the first window; on in the `challenged` shadow run.

### 4.4 Trigger mapping

- **Source:** the alerting system's webhook, authenticated.
- **Trusted fields:**
  - the alert rule ID, from the versioned rule repository;
  - the namespace or service label, as rewritten by service discovery.
- **Untrusted fields:**
  - labels that the metric itself carries;
  - anything that came through Pushgateway.
- **Dedup key:** service plus rule ID.
- **Principal:** the service's current on-call, from `catalog`.
- **Mismatch:** if routing disagrees with the alert's own labels, the incident gets diagnosis only, with no proposals.

### 4.5 Redaction

- Tokens, passwords, connection strings, private keys and cloud credentials.
- Personal data, according to the answer to open fact 9.

### 4.6 Smoke scenarios (canned data)

| Scenario | Primary agent |
|---|---|
| A bad deploy raises the error rate | `release-investigator` |
| A configuration key change breaks a dependency call | `release-investigator` |
| Pods crash-loop after node pressure | `runtime-investigator` |
| A database connection pool is exhausted | `dependency-investigator` |
| Two investigators converge on a recent deploy, but the data shows a failing dependency | `challenger` |

### 4.7 Red-team content

- **Sandbox probes:** from inside a sandbox, try every credential and network path available to write a Deployment, exec into a pod, create an alert silence, read a Secret, and reach the cloud metadata endpoint. All must fail.
- **Injection:** text planted in application logs through request headers and URLs, in alert annotations, and in ticket text.
- **Contagion:** a poisoned agent posts persuasive evidence and tries to get another agent to propose a rollback.
- **Forged quotes, entry flooding and claim hoarding.**

### 4.8 ADR-011 preregistration draft

| Item | Draft value |
|---|---|
| In-scope incidents | Alert-triggered incidents for a fixed service list, signed before measurement starts |
| Sample | [20] labeled invisible-arm incidents; at most [8 weeks] |
| Gate | Hit rate: 90% lower bound ≥ [50%] |
| Guardrails | False convergence ≤ [3 of 20]; safety floor of zero; average cost per incident ≤ [set before measurement] |
| Headroom for Phase 1 | Median mitigation wait ≥ [5 min] |
| Comparators | "Most recent change": the last change touching the service's application paths before the alert. Optionally one off-the-shelf tool (HolmesGPT or k8sgpt), only if integration time allows. |
| Collaboration comparison | The `independent` shadow run on invisible-arm incidents; reported, not gated |
| Challenge comparison | The `challenged` shadow run on invisible-arm incidents; reported, not gated. Its false-convergence rate decides whether the primary run enables challenge in the next window. |
| Ground truth | The root cause recorded in the incident platform or the postmortem |
| Raters | Two people who are not builders |
| Builder-handled incidents | Reported separately |
| After the gate | Label every incident whose runs disagree, plus [20%] of the rest. A [10%] invisible share continues only with the team lead's consent (open fact 11). |
| Signatures | The team lead plus one person who is not a builder |

### 4.9 Suggestions (record-only write tools)

- **Registration.** The three write tools of Section 5.2 are registered in Phase 0 on the `gitops-pr` server, which holds no write credentials yet. Every call ends as `recorded` and appears as a suggestion (platform spec Section 6.3).
- **Grants.** `release-investigator` gets `open_rollback_pr` and `open_config_pr`; `runtime-investigator` gets `open_restart_pr`.
- **Adopted suggestions.** When a responder marks one adopted, the policy checks of Section 5.2 run, except the sync check, which finds the agent's own pull request by operation id (platform spec Section 6.3). The outcomes show whether the suggested actions work before Phase 1 is built.

## 5. Phase 1 content (write tools)

### 5.1 Why pull requests

Self-heal reverts direct cluster writes, and second-person approval already lives in Git review. So every Phase 1 write tool opens a pull request on the GitOps repository:

- platform approval approves opening the pull request;
- merging still needs the existing Git review by a second person;
- the change takes effect when Argo CD syncs the merge.

### 5.2 Write tools

The tools live on the `gitops-pr` server. It holds the pull request bot's credentials from Phase 1 on; in Phase 0 it has none (Section 4.9).

| Tool | Change | Candidates | Target | Fingerprint |
|---|---|---|---|---|
| `open_rollback_pr` | Revert one commit on the application path | Commits touching the path in the last [7] days, taken from deployment history, signed by CI or merged after review. The default is the most recent. | service × environment | Head commit of the application path |
| `open_restart_pr` | Update the pod template annotation `agentswarm/restart-op: <operation_id>`. How it is injected depends on each application's chart. | — | service × environment | Head commit of the application path |
| `open_config_pr` | Change one allowed key | Paths and keys allowed by tool policy; typed values only (boolean, enum, bounded number) | service × environment × key | Content hash of the file |

Rules that apply to all three tools:

- **Operation id.** Each tool takes an `operation_id` argument, which tool policy names as its `operationId` (platform spec Section 3.3). Only the gateway fills it.
- **Idempotency.** The branch name is deterministic, `agent/<operation_id>`, and an existing branch or pull request is reused. Commits carry the trailer `Operation-Id: <id>`. Tool policy therefore declares all three tools `idempotent`.
- **Reconcile.** A `gitops` read that looks for a branch or pull request carrying the operation id. Finding one proves that this operation ran (platform spec Section 6.4).
- **Pull request body.** It has two separate sections:
  - a platform facts section: diff summary, the author and time of any reverted commit, the model-selected arguments, and the other candidates;
  - an agent notes section, marked "model-generated, unverified" and rendered as plain text.
- **Policy checks**, run through `cd` and `metrics`:
  - the synced revision is the merge commit or a descendant of it, judged by the commit graph, not by time;
  - the affected workloads are healthy. This means only the workloads the change touches, not the application's aggregate health;
  - the rollout is complete;
  - if the service has an SLO, its SLI is back within threshold inside a [15 min] observation window. Without an SLO the result is "unverified" and a responder confirms.
- **Timeouts.**
  - If sync is not observed within [10 min] of the merge, notify the responders.
  - If the change is not fully in effect [20 min] after the merge, a responder decides what to do.
- **Sync latency.** Configure a webhook on the GitOps repository. Polling takes about 2–3 minutes, and that delay counts toward time to mitigate.
- **Image automation.** Either freeze image-updater writes on the path during a rollback, or confirm in spike A that the updater cannot revert it.

### 5.3 Hardening candidate: ADR-010

ADR-010 works like this:
- the bot opens Draft PRs;
- a platform required check passes only when the merged head matches the content the platform approved.

Required repository settings:
- review before merge;
- code-owner review;
- dismiss stale approvals when new commits arrive;
- require approval of the most recent push;
- no bypass;
- the bot is not a code owner.

Negative cases to verify (each must fail):
- the bot merges after a human approves;
- the bot pushes a non-agent branch;
- the bot creates a tag;
- the bot's own approval counts toward merge;
- the principal's own approval is enough to merge;
- a merge goes through on a stale approval after someone else made a non-conflicting change.

Reconfirm this ADR before Phase 1.

### 5.4 Approval policy

`second-human` means:
- the approver is a human who is not the principal;
- the approver is a code owner of the path on the secondary rotation;
- if no one responds within [5 min], the request escalates to the team lead.

### 5.5 Kill switch and restore

- **Kill switch:** suspend or uninstall the pull request bot's app installation. If the platform may be compromised, also delete the app's private key. Revoking an installation token alone does not stop whoever holds the key from minting new tokens.
- **After a database restore:** close every unmerged agent pull request, then reconcile merged agent commits from Git, which is the authoritative record. An `Operation-Id` trailer that the restored database does not know marks a write that the restore lost (platform spec Section 8.4).

## 6. Spikes

| Spike | Due before | Content | Pass criteria |
|---|---|---|---|
| C: runtime isolation | Phase 0 | Compare the Claude Agent SDK with a thin loop on the model API. Check: whether host configuration gets loaded; whether built-in tools can be disabled; operation with egress denied by default; control over how context is built; round-based turns; gateway-only MCP. | No host or workspace content in prompts; decision recorded in ADR-006 |
| Read-only permissions (from B) | Phase 0 | The Kubernetes read-only role excludes secrets and exec; the `cd` account is read-only | The red-team sandbox probes fail |
| MCP server census | Phase 0 | Which read servers exist, their quality, their tool fingerprints, and whether each reports partial results as errors | The list in Section 4.1 is confirmed |
| A: GitOps write semantics | Phase 1 | Run on one representative app in staging. <br>• Try a rollback, a restart and a configuration key change. <br>• Time each stage (PR, approval, merge, sync, rollout complete), once with the webhook and once with polling, and compare with today's manual path. <br>• Check that image automation does not revert a rollback. <br>• Render manifests before and after the change, with the same tool versions as Argo CD, and assert that only the intended resources and fields change. | All three writes are generated deterministically; diffs touch only the target; the Git path is no slower than today; image automation cannot undo a rollback |
| B: code host rules | Phase 1 | <br>• Configure the rules and the required check (Section 5.3), and run every negative case. <br>• Confirm that CI on agent branches gets no secrets. <br>• Test whether the bot can dismiss reviews or close other people's pull requests. Both are expected to be possible; if so, add audit alerts. <br>• Confirm that human pull requests still merge while the platform is down. <br>• Run a night-time approval drill on a phone, done by someone who is not a builder. | All negative cases fail; human pull requests do not depend on the platform |

## 7. Where the old material went

The old documents are kept outside this repository. This table records where their content went.

| Source | Destination |
|---|---|
| v1 spec Section 1: facts and open questions | Sections 2 and 3 |
| v1 spec Section 6.1 (gitops-pr) and Section 6.2 (argocd observer) | Section 5.2 |
| v1 spec Section 7.1 (threat model specifics) and Section 7.3 (alert routing) | Sections 4.4 and 4.7; the general controls are in platform spec Section 9 |
| v1 spec Section 5.3 (kill switch) and Section 9.4 (disaster recovery) | Section 5.5; the general procedure is in platform spec Sections 8.3 and 8.4 |
| v1 spec Section 9, tests 0-2 to 0-4 | Sections 4.5 and 4.7 |
| facts-and-spikes: the census and the spikes | Sections 3 and 6 |
| ADR-010 | Section 5.3 |
