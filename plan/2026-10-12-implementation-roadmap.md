# Implementation Roadmap

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-12 |
| Spec | `2026-10-11-agent-swarm-platform-spec.md` (approved 2026-10-12) |
| Domain content | `2026-10-11-first-deployment-ops-incidents.md` |
| First plan | `2026-10-12-plan-1-foundation-and-blackboard.md` |

## 1. How the work is split

Phase 0 is estimated at 19–25 person-weeks (spec Section 14.2). One plan for all of it would go stale before half of it ran, and two of its early steps can stop the project (spec Section 14.1). So the work is split into plans. Each plan leaves working, tested software, and each is written in detail only when its turn comes, from what the earlier plans taught.

Plan 1 is written in full. The other plans are outlined below.

## 2. Plans and gates

| # | Plan | Delivers | Needs | Person-weeks |
|---|---|---|---|---|
| 1 | Foundation and the blackboard | The build, lint, test and release toolchain; the Work context's core: hypothesis keys, board entries with provenance, retraction and rulings, stances, convergence and the conclusion; the Postgres event store; the `conclude` command | Nothing | about 2 |
| 2 | Runtime spike (ADR-006) | Spike C of companion Section 6: the candidate runtimes against the seven criteria of ADR-006; the decision written into ADR-006 | Nothing; runs alongside Plan 1 | about 1 |
| 3 | Retrospective check | The planned agents run without the coordinator on census incidents, with reads bounded to the alert time; their hypotheses are aggregated by key as in the independent run (Plan 1's assessment) and compared with the baseline; the runs set the token budget default (spec Section 14.1) | Plans 1 and 2; the census (open facts 2 and 7); one skill per investigator (open fact 19); the read-only MCP servers the census finds | about 1 |
| **Gate A** | **Retrospective check and the commitments of spec Section 14.3** | Build Phase 0, reshape it, or stop (spec Section 14.1) | | |
| 4 | Registry | Configuration kinds and their schema validation, loader rules, locking with content hashes, the yank list, CEL conditions and JSONPath selectors (ADR-014, ADR-015) | Plan 1 | 2–3 |
| 5 | MCP gateway | The agent endpoint; the read path with tool policy, pinning, bounds, `partialWhen`, redaction, audit before the call, server limits and platform reads; record-only proposals; the blackboard tools that depend on rounds: entry limits, idempotent repeats, tasks and claims, `end_turn`, `read_board` and the wrap-up rules (spec Sections 5.2, 5.3 and 6) | Plans 1 and 4 | 3–4 |
| 6 | LLM gateway and audit chain | The request allowlist, per-run budgets, cost attribution and prompt logging (ADR-007); the hash-chained audit store and its hourly anchor (spec Section 9.4) | Plans 4 and 5 | 2–3 |
| 7 | Coordinator | Inbound events and trigger mappings, incidents and work items, arms, runs and rounds, sessions through the runner, budgets, stop and close, finality, leases and timers on River, the kill switch (spec Sections 3.6, 4.1, 5, 8 and 13.1) | Plans 1 and 4–6; the ADR-006 decision | 4–5 |
| 8 | Measurement | Shadow runs, the independent run, the baseline, cutoffs, two-stage rating packets, metrics and the gate (spec Sections 7 and 13) | Plan 7 | 2–3 |
| 9 | Human interface and notifications | Sign-in and roles, the items view, the live board, rulings, close-out and adoption, the kill switch, channels and accessibility (spec Section 10) | Plan 7 | 3–4 |
| 10 | Hardening | Red team, load trial, retention jobs, the backup and restore drill, release images (spec Sections 8.4, 9.4 and 12.1) | Plans 1–9 | 1.5–2 |
| 11 | Window builds | Monitoring after the gate, version history, the release record and probation runs, behind flags (spec Sections 12.3 and 13.8) | Plan 8; built during the measurement window | about 2 |

The plans before Gate A add up to about 4 person-weeks, and Plans 4–10 to about 18–24. Together that is in line with spec Section 14.2.

**Why Plan 1 comes first.** Every path needs it. The retrospective check aggregates hypotheses by key and picks a conclusion exactly as a run does, and the Phase 0 build stands on the same rules. If Gate A stops the project, about two person-weeks of reusable core is all that was spent.

## 3. What others must provide, and by when

| Before | What | Source |
|---|---|---|
| Plan 3 | The historical census, including root causes and milestone times | Open facts 2 and 7 |
| Plan 3 | A runbook for each investigator, released as a skill | Open fact 19 |
| Plan 3 | Read access to the systems the census needs, through read-only MCP servers | Companion Section 4.1; the MCP server census |
| Plan 3 | Model vendor access under the confirmed data terms | Companion Section 2 |
| Plan 4 | The team lead's consent to hide agent output at random | Open fact 11; spec Section 14.3 |
| Plan 4 | A co-signer and two raters, none of them builders | Open fact 15 |
| Plan 4 | A team outside the builders that owns an agent and writes its skill | Open fact 16 |
| Plan 4 | An owner, outside the builders, for the store that anchors the audit chain | Open fact 24 |
| Plan 7 | The alerting source's payload and authentication; the milestone path | Open facts 18 and 22 |
| Plan 9 | Single sign-on and group claims; the message channels; phone access at night | Open facts 12, 13 and 21 |

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

Plan 1 is ready for review. Plans 2 and 3 are written next. Plans 4–11 are written after Gate A, in order.
