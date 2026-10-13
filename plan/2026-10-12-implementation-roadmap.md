# Implementation Roadmap

| | |
|---|---|
| Status | Approved on 2026-10-12. Amended on 2026-10-13: no team has adopted the platform, so Plan 3 is a mechanism check on a public benchmark, and Plans 4–10 wait for a team (spec Section 1.3). |
| Date | 2026-10-12 |
| Spec | `2026-10-11-agent-swarm-platform-spec.md` (approved 2026-10-12) |
| Deployment profile | Kept by each team outside this repository (spec Section 0); `2026-10-13-example-deployment-profile.md` is a fictional example |
| First plan | `2026-10-12-plan-1-foundation-and-blackboard.md` |
| Plan 2 | `2026-10-13-plan-2-runtime-spike.md` (executed) |

## 1. How the work is split

Phase 0 is estimated at 19–25 person-weeks (spec Section 14.2). One plan for all of it would go stale before half of it ran, and two of its early steps can stop the project (spec Section 14.1). So the work is split into plans. Each plan leaves working, tested software, and each is written in detail only when its turn comes, from what the earlier plans taught.

Plan 1 is written in full. The other plans are outlined below.

## 2. Plans and gates

| # | Plan | Delivers | Needs | Person-weeks |
|---|---|---|---|---|
| 1 | Foundation and the blackboard | The build, lint, test and release toolchain; the Work context's core: hypothesis keys, board entries with provenance, retraction and rulings, stances, convergence and the conclusion; the Postgres event store; the `conclude` command | Nothing | about 2 |
| 2 | Runtime spike (ADR-006) | Spike C: the candidate runtimes against the seven criteria of ADR-006; the decision written into ADR-006 | Nothing; runs alongside Plan 1 | about 1 |
| 3 | Mechanism check | The retrospective check command: the planned agents run without the coordinator on a set of scenarios, each on its own; their hypotheses are aggregated by key as in the independent run (Plan 1's assessment) and scored against ground truth and the baseline. Its first input is a public benchmark's fault scenarios, read through read-only MCP servers that cannot see the injections; the runs set a first token budget default (spec Section 14.1). Each team later runs the same command on its own retrospective set (spec Section 14.4). | Plans 1 and 2; the benchmark running on the builders' machines; model access | 1–2 |
| **Gate A** | **The mechanism check, then a team that adopts the platform** | Without a team, Plans 4–10 wait. With one, its census, its retrospective check and the commitments of spec Section 14.3 decide whether Phase 0 is built, reshaped or stopped (spec Section 14.1). | | |
| 4 | Registry | Configuration kinds and their schema validation, loader rules, locking with content hashes, the yank list, CEL conditions and JSONPath selectors (ADR-014, ADR-015) | Plan 1 | 2–3 |
| 5 | MCP gateway | The agent endpoint; the read path with tool policy, pinning, bounds, `partialWhen`, redaction, audit before the call, server limits and platform reads; record-only proposals; the blackboard tools that depend on rounds: entry limits, idempotent repeats, tasks and claims, `end_turn`, `read_board` and the wrap-up rules (spec Sections 5.2, 5.3 and 6) | Plans 1 and 4 | 3–4 |
| 6 | LLM gateway and audit chain | The request allowlist, per-run budgets, cost attribution and prompt logging (ADR-007); the hash-chained audit store and its hourly anchor (spec Section 9.4) | Plans 4 and 5 | 2–3 |
| 7 | Coordinator | Inbound events and trigger mappings, incidents and work items, arms, runs and rounds, sessions through the runner, budgets, stop and close, finality, leases and timers on River, the kill switch (spec Sections 3.6, 4.1, 5, 8 and 13.1) | Plans 1 and 4–6; the ADR-006 decision | 4–5 |
| 8 | Measurement | Shadow runs, the independent run, the baseline, cutoffs, two-stage rating packets, metrics and the gate (spec Sections 7 and 13) | Plan 7 | 2–3 |
| 9 | Human interface and notifications | Sign-in and roles, the items view, the live board, rulings, close-out and adoption, the kill switch, channels and accessibility (spec Section 10) | Plan 7 | 3–4 |
| 10 | Hardening | Red team, load trial, retention jobs, the backup and restore drill, release images (spec Sections 8.4, 9.4 and 12.1) | Plans 1–9 | 1.5–2 |
| 11 | Window builds | Monitoring after the gate, version history, the release record and probation runs, behind flags (spec Sections 12.3 and 13.8) | Plan 8; built during the measurement window | about 2 |

The plans before Gate A add up to about 4–5 person-weeks, and Plans 4–10 to about 18–24. Together that is in line with spec Section 14.2. The first team's retrospective check, about 1 more, runs the Plan 3 command once a team adopts the platform.

**Why Plan 1 comes first.** Every path needs it. The mechanism check and every team's retrospective check aggregate hypotheses by key and pick a conclusion exactly as a run does, and the Phase 0 build stands on the same rules. If Gate A stops the project, about two person-weeks of reusable core is all that was spent.

## 3. What the deploying team must provide, and by when

The platform never provides these; each team brings its own (spec Section 0). Questions are numbered as in Section 3 of the example deployment profile. As of 2026-10-13 no team has adopted the platform, so nothing here is due yet; Plan 3 needs none of it.

| Before | What | Profile question |
|---|---|---|
| The team's retrospective check | The historical census, including root causes and milestone times | 1 and 2 |
| The team's retrospective check | A runbook for each investigator, released as a skill | 4 |
| The team's retrospective check | Read access to the systems the census needs, through read-only MCP servers | 5; the MCP server census |
| The team's retrospective check | Model vendor access under data terms the team has confirmed | 6 |
| Plan 4 | The team lead's consent to hide agent output at random | 7; spec Section 14.3 |
| Plan 4 | A co-signer and two raters, none of them builders | 8 |
| Plan 4 | A team outside the builders that owns an agent and writes its skill | 9 |
| Plan 4 | An owner, outside the builders, for the store that anchors the audit chain | 14 |
| Plan 7 | The alerting source's payload and authentication; the milestone path | 3 and 2 |
| Plan 9 | Single sign-on and group claims; the message channels; phone access at night | 12 and 13 |

## 4. Implementation decisions

These apply to every plan. Each plan may refine them, and says so.

- **Module and binary.** The module is `github.com/jchen0426/agent-swarm`. There is one binary, `agentswarm`, with subcommands; later plans add `serve` and `migrate`.
- **Bounded contexts.** The six contexts of spec Section 2.2 are confirmed: Work and Actions (core), Registry and Measurement (supporting), Access and Audit (generic).
- **Layout.** Each context lives under `internal/<context>/`, with:
  - domain packages named after their concept (`internal/work/key`, `internal/work/board`);
  - one `app` package for its use cases and the ports they declare (`internal/work/app`);
  - one package per adapter technology (`internal/work/postgres`).

  Shared infrastructure sits in `internal/db` (connection and migrations) and `internal/ids` (identifiers). Wiring lives in `cmd/agentswarm`. Black-box tests of the built binary live in `test/e2e`.
- **Database.** One Postgres database with one migration sequence, embedded in the binary. Tests run against Postgres 17 in containers.
- **Libraries.** pgx v5 for Postgres. River for jobs, from Plan 7 (ADR-004). cel-go and an RFC 9535 JSONPath library, from Plan 4 (ADR-015). The official MCP Go SDK, from Plan 5. No web framework: the standard library's HTTP server.

## 5. Status of this roadmap

Plan 1 was executed on 2026-10-12, and Plan 2 on 2026-10-13: ADR-006 chose a thin loop on the Messages API, provisionally until the live runs at the start of Plan 3. Plan 3 is written next. Plans 4–11 are written after Gate A, in order, once a team has adopted the platform.
