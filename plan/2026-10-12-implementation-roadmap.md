# Implementation Roadmap

| | |
|---|---|
| Status | Approved on 2026-10-12. Amended on 2026-10-13: no team has adopted the platform, so Plan 3 is a mechanism check on a public benchmark, and Plans 4–10 wait for a team (spec Section 1.3). Amended on 2026-10-14: the author decided to build Plans 4–10 before a team adopts the platform (ADR-016). |
| Date | 2026-10-12 |
| Spec | `2026-10-11-agent-swarm-platform-spec.md` (approved 2026-10-12) |
| Deployment profile | Kept by each team outside this repository (spec Section 0); `2026-10-13-example-deployment-profile.md` is a fictional example |
| First plan | `2026-10-12-plan-1-foundation-and-blackboard.md` |
| Plan 2 | `2026-10-13-plan-2-runtime-spike.md` (executed) |
| Plan 3a | `2026-10-13-plan-3a-mechanism-check-data.md` (executed) |
| Plan 3b | `2026-10-14-plan-3b-mechanism-check-runs.md` (executed; the mechanism check passed) |
| Plan 4 | `2026-10-14-plan-4-registry.md` (executed) |
| Plan 5a | `2026-10-14-plan-5a-gateway-read-path.md` (executed) |
| Plan 5b | `2026-10-15-plan-5b-gateway-board-and-proposals.md` (executed) |
| Plan 6a | `2026-10-15-plan-6a-audit-chain.md` (executed) |
| Plan 6b | `2026-10-17-plan-6b-llm-gateway.md` (executed) |
| Plan 7a | `2026-10-18-plan-7a-coordinator-core.md` (executed) |

## 1. How the work is split

Phase 0 is estimated at 19–25 person-weeks (spec Section 14.2). One plan for all of it would go stale before half of it ran, and two of its early steps can stop the project (spec Section 14.1). So the work is split into plans. Each plan leaves working, tested software, and each is written in detail only when its turn comes, from what the earlier plans taught.

Plan 1 is written in full. The other plans are outlined below.

## 2. Plans and gates

| # | Plan | Delivers | Needs | Person-weeks |
|---|---|---|---|---|
| 1 | Foundation and the blackboard | The build, lint, test and release toolchain; the Work context's core: hypothesis keys, board entries with provenance, retraction and rulings, stances, convergence and the conclusion; the Postgres event store; the `conclude` command | Nothing | about 2 |
| 2 | Runtime spike (ADR-006) | Spike C: the candidate runtimes against the seven criteria of ADR-006; the decision written into ADR-006 | Nothing; runs alongside Plan 1 | about 1 |
| 3 | Mechanism check | In two parts. 3a, which needs no model: the benchmark's scenarios, prepared for read-only MCP tools that cannot see the injections; the frozen keys, the baseline and the scorer; the pass criteria. 3b: the rest. The retrospective check command: the planned agents run without the coordinator on a set of scenarios, each on its own; their hypotheses are aggregated by key as in the independent run (Plan 1's assessment) and scored against ground truth and the baseline. Its first input is a public benchmark's fault scenarios, read through read-only MCP servers that cannot see the injections; the runs set a first token budget default (spec Section 14.1). Each team later runs the same command on its own retrospective set (spec Section 14.4). | Plans 1 and 2; the benchmark running on the builders' machines; model access | 1–2 |
| **Gate A** | **The mechanism check** | Passed on 2026-10-14. The approved path then waited for a team before Plans 4–10; on 2026-10-14 the author decided to build them first (ADR-016). A team's census, its retrospective check and the commitments of spec Section 14.3 still decide, before its measurement window, whether its deployment goes ahead, is reshaped or stops (spec Section 14.1). | | |
| 4 | Registry | Configuration kinds and their schema validation, loader rules, locking with content hashes, the yank list, CEL conditions and JSONPath selectors (ADR-014, ADR-015) | Plan 1 | 2–3 |
| 5a | MCP gateway: the read path | The agent endpoint; tool lists; the read path with tool policy, pinning, bounds, `partialWhen`, redaction, audit before the call and server limits; platform reads (spec Sections 3.3, 6.1 and 6.2) | Plans 1 and 4 | about 2 |
| 5b | MCP gateway: board tools and proposals | Record-only proposals; the blackboard tools that depend on rounds: entry limits, idempotent repeats, tasks and claims, `end_turn`, `read_board` and the wrap-up rules (spec Sections 5.2, 5.3 and 6.3) | Plan 5a | 1.5–2 |
| 6a | Audit chain | The hash-chained audit store, its hourly anchor, which the anchor store's owner takes, and verification (spec Section 9.4) | Plans 4 and 5 | about 1 |
| 6b | LLM gateway | The request allowlist, per-run budgets, cost attribution and prompt logging into the chained store (ADR-007) | Plan 6a | 1.5–2 |
| 7a | Coordinator: one item end to end | Plan 6b's carried fixes; work records; the board's stop entry; session tokens; the lease, timers from records; rounds and stop rules with the daily cap; takeover (spec Sections 4.1, 5.2, 5.6 and 8.2) | Plans 1 and 4–6 | about 1.5 |
| 7d | Runner and serve | The runner in sandboxes, `agentswarm serve` and `migrate`, the gateways under the lease, `Drain`, model calls left without an outcome (spec Sections 2 and 8.2; ADR-006) | Plan 7a | about 1.5 |
| **Checkpoint** | **After Plan 7d** | The coordinator runs agents across rounds. The author decides again whether to keep building or to look for a team with what exists (ADR-016). | | |
| 7b | Coordinator: the lifecycle's breadth | The queue and promotion, the kill switch, close, reopen and auto-close, extensions, links between items, the use cases Plan 9 calls (spec Sections 4.1, 5.1 and 8.3) | Plans 7a and 7d | about 1 |
| 7c | Inbound events | Trigger mappings, dedup and correlation, principals, responders and key values read at open (spec Sections 3.6 and 5.1) | Plans 7a and 7d | about 1 |
| 8 | Measurement | Arms, the measurement window, finality, shadow runs, the independent run, the baseline, cutoffs, two-stage rating packets, metrics and the gate (spec Sections 7 and 13) | Plans 7b and 7c | 2–3 |
| 9 | Human interface and notifications | Sign-in and roles, the items view, the live board, rulings, close-out and adoption, the kill switch, channels and accessibility (spec Section 10) | Plans 7b and 7c | 3–4 |
| 10 | Hardening | Red team, load trial, retention jobs, the backup and restore drill, release images (spec Sections 8.4, 9.4 and 12.1) | Plans 1–9 | 1.5–2 |
| **Exit** | **Three months after Plan 10** | If no team has adopted the platform by then, the author decides between stopping and re-scoping (ADR-016). | | |
| 11 | Window builds | Monitoring after the gate, version history, the release record and probation runs, behind flags (spec Sections 12.3 and 13.8) | Plan 8; built during the measurement window | about 2 |

The plans before Gate A add up to about 4–5 person-weeks, and Plans 4–10 to about 18–24. Together that is in line with spec Section 14.2. The first team's retrospective check, about 1 more, runs the Plan 3 command once a team adopts the platform.

**Why Plan 1 comes first.** Every path needs it. The mechanism check and every team's retrospective check aggregate hypotheses by key and pick a conclusion exactly as a run does, and the Phase 0 build stands on the same rules. If Gate A stops the project, about two person-weeks of reusable core is all that was spent.

## 3. What the deploying team must provide, and by when

The platform never provides these; each team brings its own (spec Section 0). Questions are numbered as in Section 3 of the example deployment profile. As of 2026-10-14 no team has adopted the platform. Plans 4–10 are built on the assumptions ADR-016 records, and a team's answers replace them when it onboards.

| Before | What | Profile question |
|---|---|---|
| The team's retrospective check | The historical census, including root causes and milestone times | 1 and 2 |
| The team's retrospective check | A runbook for each investigator, released as a skill | 4 |
| The team's retrospective check | Read access to the systems the census needs, through read-only MCP servers | 5; the MCP server census |
| The team's retrospective check | Model vendor access under data terms the team has confirmed | 6 |
| The team's measurement window | The team lead's consent to hide agent output at random | 7; spec Section 14.3 |
| The team's measurement window | A co-signer and two raters, none of them builders | 8 |
| The team's measurement window | A team outside the builders that owns an agent and writes its skill | 9 |
| The team's measurement window | An owner, outside the builders, for the store that anchors the audit chain, who also takes the hourly anchors and verifies the chain | 14 |
| The team's measurement window; Plan 7c builds on an assumption | The alerting source's payload and authentication; the milestone path | 3 and 2 |
| The team's measurement window; Plan 9 builds on an assumption | Single sign-on and group claims; the message channels; phone access at night | 12 and 13 |

## 4. Implementation decisions

These apply to every plan. Each plan may refine them, and says so.

- **Module and binary.** The module is `github.com/jchen0426/agent-swarm`. There is one binary, `agentswarm`, with subcommands; later plans add `serve` and `migrate`.
- **Bounded contexts.** The contexts of spec Section 2.2 are confirmed: Work and Actions (core), Registry, Measurement and LLM (supporting), Access and Audit (generic). Plan 6b added LLM.
- **Layout.** Each context lives under `internal/<context>/`, with:
  - domain packages named after their concept (`internal/work/key`, `internal/work/board`);
  - one `app` package for its use cases and the ports they declare (`internal/work/app`);
  - one package per adapter technology (`internal/work/postgres`).

  Shared infrastructure sits in `internal/db` (connection and migrations) and `internal/ids` (identifiers). Wiring lives in `cmd/agentswarm`. Black-box tests of the built binary live in `test/e2e`.
- **Database.** One Postgres database with one migration sequence, embedded in the binary. Tests run against Postgres 17 in containers.
- **Libraries.** pgx v5 for Postgres. River for jobs that must outlive the records, from Plan 9 (ADR-004). cel-go and an RFC 9535 JSONPath library, from Plan 4 (ADR-015). The official MCP Go SDK, from Plan 5. No web framework: the standard library's HTTP server.

## 5. Status of this roadmap

Plan 1 was executed on 2026-10-12, and Plan 2 on 2026-10-13: ADR-006 chose a thin loop on the Messages API, and the live runs, on Claude Haiku 5.5, confirmed it on the same day. Plan 3a was executed on 2026-10-13: the mechanism check's data, tools, answers, baseline and scorer, with its pass criteria fixed. Plan 3b, on 2026-10-14, built the retrospective check command, piloted three investigators on the development set, and ran the preregistered test: the mechanism check passed, C1 in every run and C2 in two of three trials. Plan 4, on 2026-10-14, built the registry: every configuration kind with its loader rules, CEL conditions, JSONPath selectors, the Git and directory sources, the revision record and the lock, and the `agentswarm registry` commands. Plan 5a, on 2026-10-14, built the MCP gateway's read path: tool lists, pinning at run time, strict arguments and bounds, one canonical form per result, redaction with rules that apply always, server limits, the audit record before every call, platform reads and the agent-facing endpoint. Plan 5b, on 2026-10-15, gave agents the board through the gateway: the board tools with the round's rules, record-only proposals with their checks, candidates, facts and summaries, diagnosis-only items, and notices of refused calls. Plan 6a, on 2026-10-17, built the audit chain: every audited row is chained as it is inserted, the anchor store's owner takes the anchors, and verification shows a change, a deletion or a restore. Plan 6b, on 2026-10-18, built the LLM gateway: it forwards only what the platform's runtime sends, redacted, records every model call in the chained audit store, and holds runs to their budgets and the deployment to its daily cap, priced as the vendor charges. Plan 7a, on 2026-10-19, built the coordinator's core: one item runs its primary run through rounds to a stop, every decision recorded before it acts, with session tokens both gateways know, the daily cap, and one lease that fences an instance that lost it. Plan 7d, the runner and `serve`, comes next, so that a real runner tries the turns' contract before Plans 7b and 7c add the lifecycle's breadth and the inbound events; the checkpoint follows it. No team has adopted the platform. On 2026-10-14 the author decided to build Plans 4–10 before one does (ADR-016); each is written when its turn comes, in order. Plan 11 is still built during the first team's measurement window.
