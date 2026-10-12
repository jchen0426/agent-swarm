# ADR-004 Durable execution: Postgres as the single source of truth, with a Postgres-native task library

| | |
|---|---|
| Status | Accepted. The task library, River, is proposed. |
| Date | 2026-10-10; revised 2026-10-12 |
| Related | Spec Sections 2, 8.2 and 8.4; ADR-013; ADR-015; Phase 1 backlog |

## Context

- Phase 0 needs durable state for incidents, work items, runs, boards, budgets and measurement. It needs timers (auto-close, the finality wait, shadow release, the hourly audit anchor, retention), bounded retries, and exactly one active coordinator and one active MCP gateway (spec Section 8.2).
- The team is 1–2 people. More than 10 incidents a week is far below what one Postgres server handles.
- An earlier review found that the reasons given against Temporal and DBOS were straw men, and that Postgres-native task libraries had been missed.
- The platform is written in Go (ADR-015).

## Options

- **A.** Self-built leases plus Temporal.
- **B.** Temporal, self-hosted or managed, as the authority, with the business database as a projection.
- **C.** Plain Postgres, with hand-written timer tables and `SKIP LOCKED`.
- **D.** DBOS: durable execution on Postgres, with workflow state in the same database as business data.
- **E.** Postgres plus a Postgres-native task library.

## Decision

E, with River as the Go task library (proposed on 2026-10-12, with ADR-015).

- **One source of truth.** All state lives in Postgres. Enqueuing a task and changing business state commit in the same transaction.
- **Leases (added 2026-10-12).** The coordinator and the MCP gateway each act only while they hold their lease in the database. Every write of that component checks the lease in the same transaction. Server limits and degraded marks are shared through the database. A second instance waits (spec Section 8.2).
- **Time base.** Every timeout and lease expiry is judged by database time.
- **Restart.** A restarted coordinator resumes rounds, leases and budgets from the database (spec Section 8.2).
- **Restore.** In Phase 0 a restore loses the entries written after the backup. The affected incidents are excluded from measurement, and the anchored audit chain shows what was lost (spec Section 8.4).

## Rationale

- There is one authority and one judge of liveness. Apart from the outbox, nothing needs reconciling between two stores.
- Timers, retries and unique tasks come ready-made. Compared with C, this saves about [1] person-week and a class of bugs.
- There is no extra cluster to operate.
- River fits ADR-015: it is Go, uses pgx, enqueues inside the caller's transaction, and supports unique and scheduled jobs.

## Rejected options

- **A:** two components judge liveness and retries, and their state must be reconciled for good.
- **B:** a managed service removes the operating cost, but Phase 0 flows are short (minutes to hours) and do not need durable replay of long workflows. It adds a second authoritative store and a new programming model, which is not worth it for 1–2 people. Revisit it for multi-day flows or compensations.
- **C:** workable, but timers, retries and uniqueness must be written and tested by hand.
- **D:** workable, and committing workflow steps with business writes is attractive, but its programming model is heavier than Phase 0 needs. It is the first candidate on revisit.

## Capacity assumptions

Suggested initial values: at most [100] incidents a day; at most [10] concurrent runs; control-plane overhead per tool call under [200 ms] at p99. Revisit this ADR if any of them is exceeded tenfold.

## Consequences

- Its write-path premises (an outbox for writes, fencing at dispatch, the restore procedure with an epoch) are revisited with the Phase 1 design (spec Section 16; Phase 1 backlog).

## Reversibility

Medium. Moving to DBOS or Temporal means rewriting the orchestration layer, about [2–3] person-weeks. The authoritative data model stays.

## Revisit when

- Multi-day flows appear, or multi-step writes that need compensation, most likely with Phase 1.
- More than [100] runs execute at once.
- The team grows beyond 5 people.

## History

- **2026-10-10:** proposed, with the library left to the implementation language.
- **2026-10-12:** rewritten in English. River chosen with ADR-015; the coordinator and gateway leases added (spec Section 16).
