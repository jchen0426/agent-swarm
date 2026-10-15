# ADR-012 Several agents on a structured blackboard

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-12 |
| Supersedes | ADR-002 |
| Related | Spec Sections 1.2, 4.2, 4.3, 5.2, 5.6, 9.2 and 13.3 |

## Context

- ADR-002 defaulted to one agent, because multi-agent systems cost more and lose decisions when agents hand work to each other.
- The platform exists for governance (spec Section 1.1): who each agent is, what it knows, what it may touch, and who answers for it.
- Agents that share a model agree easily, so their agreement is not independent confirmation.

## Options

- **A.** One agent per work item (ADR-002).
- **B.** Several agents that send each other free-form messages.
- **C.** Several agents on a shared, structured blackboard, paced by a deterministic coordinator.
- **D.** C plus a challenger role, an agent whose job is to disagree.

## Decision

C.

- **Why several agents: governance.** Each agent has its own human owner, who can change it without the platform builders, and its own least-privilege grants (spec Section 1.2). This reason holds only if teams other than the builders own agents, so spec Section 14.3 asks for one by a set date.
- **The board.** One per run, append-only. Entries are typed (evidence, hypothesis, task, claim, completion, turn end, proposal, ruling, notice, and the round entries that open rounds), and each carries its author, time, run and references (spec Section 4.2).
- **Provenance.** An agent's evidence cites a gateway read call made by the same agent in the same run, plus a verbatim quote of that call's stored, redacted output.
- **Pacing.** Runs advance in bulk-synchronous rounds with snapshots, so other agents see an entry only from the next round. A deterministic coordinator, which never calls a model, decides convergence and stops (spec Sections 2 and 5.2).
- **Stances.** Only each contributor's latest stance counts. Agents may retract their own evidence, and retracted evidence does not count.
- **Convergence.** A hypothesis converges with support from at least [2] distinct contributors and [2] distinct sources, no standing refutation and no human rejection. A human's refutation stands until a responder dismisses it. An agent's refutation stands unless it is dismissed or the hypothesis has at least [2] more supporting than refuting contributors. Human support that would lift a refutation counts as a dismissal (spec Sections 4.2 and 4.3).
- **No challenger in this phase (D).** No data path could evaluate it in time. If false convergence shows up, recorded boards are first replayed under stricter convergence settings (spec Section 5.6).
- **No team-versus-single comparison.** The rationale is governance, so this phase does not compare a team with a single agent. It does compare multi-round collaboration with independent work, through the `independent` shadow run (spec Section 13.3).

## Rationale

- Typed entries make provenance checkable and let rules, not a model, compute the conclusion.
- Rounds and snapshots keep the coordinator deterministic, so recorded items can be replayed (spec Section 12.3).
- Governance needs agents that different people own.

## Rejected options

- **A:** separately owned agents with their own least privilege cannot work one item.
- **B:** free-form messages lose decisions at hand-offs, and nothing in them can be checked or replayed.
- **D:** deferred, as above.

## Consequences

- An item costs more tokens than one agent would. Per-run budgets bound the cost, and it is measured per incident (spec Section 13.6).
- Contagion between agents is limited by three rules: a proposer must hold its own supporting evidence, convergence needs independent sources, and entries per round are limited (spec Section 9.2).

## Reversibility

Medium. The board and rounds are core, but team size is configuration (`maxAgents`), so a one-agent run stays possible.

## Revisit when

- The false-convergence guardrail is breached, and replaying boards under stricter settings does not fix it.
- No team outside the builders owns an agent by the set date (spec Section 1.2).

## History

- **2026-10-12:** accepted with the spec (spec Section 16).
- **2026-10-15:** Plan 5b added claims, completions, turn ends and round entries to the board (spec Section 4.2 as amended with Plan 5b).
