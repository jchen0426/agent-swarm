# ADR-002 Orchestration: a single agent by default

| | |
|---|---|
| Status | Superseded by ADR-012 |
| Date | 2026-10-10; superseded 2026-10-12 |
| Related | ADR-012; spec Sections 1.2 and 13.3 |

## The original decision

Phase 0 would run one agent serially. Fan-out would be a configuration switch, turned on only if data showed that agent time was a large share of the on-call wait.

The reasons were:

- multi-agent systems cost many more tokens, and their gains come mostly from breadth-first search that can run in parallel;
- agents lose implicit decisions when they hand work to each other;
- building two orchestration models in Phase 0 would crowd the schedule.

## Why it was superseded

The reason for several agents changed from accuracy to governance (ADR-012). Each agent has its own human owner and its own least-privilege grants. That reason does not depend on agents being more accurate together, so a single-agent default no longer follows.

The concerns that remain are handled elsewhere:

- **Cost:** hard per-run token budgets (ADR-007), measured per incident (spec Section 13.6).
- **Value of collaboration:** the `independent` shadow run compares multi-round collaboration with independent work (spec Section 13.3).
- **Agents that share a model are not independent:** convergence needs independent sources, and this phase has no challenger role (spec Section 5.6). ADR-003 keeps the rule that such agents are not independent approvers.

## History

- **2026-10-10:** proposed.
- **2026-10-12:** superseded by ADR-012.
