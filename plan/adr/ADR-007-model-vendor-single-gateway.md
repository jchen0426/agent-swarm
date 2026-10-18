# ADR-007 Model vendor: one vendor behind the LLM gateway

| | |
|---|---|
| Status | Accepted. The gateway is built (Plan 6b). |
| Date | 2026-10-10; revised 2026-10-12 and 2026-10-13 |
| Related | Spec Sections 2, 5.6, 7, 8.2, 9.3 and 13.6; ADR-003; ADR-006 |

## Context

- Each deployment confirms whether, and on which terms, log content may be sent to a model vendor (example deployment profile, question 6). The platform redacts either way (spec Section 9.4).
- Agent loops resend long contexts at every step, so prompt caching matters a lot for cost.
- The team is 1–2 people.
- The spec needs these from the LLM gateway:
  - per-run budgets on cost-weighted tokens: what a call costs at its model's prices, cache writes and reads included, at the price of one reference token, which a run passes by at most the model calls its agents start while it is under its budget (spec Section 5.6);
  - cost recorded per run, agent and arm (spec Section 13.6);
  - a daily cap, with comparison and probation runs cut first at [80%] of it (spec Section 7);
  - an allowlist checked over the whole request (spec Section 9.3);
  - redaction before the model sees anything, and redacted prompts and completions logged only into the audit store (spec Section 9.4);
  - short-lived tokens per agent session, scoped to its run and agent (spec Section 2).

## Options

- **A.** A vendor-neutral abstraction that routes across vendors.
- **B.** One vendor, used deeply (prompt caching, tool calling, long context). The gateway's endpoint and model ids are configuration.
- **C.** Self-hosted open models.

## Decision

B, with these additions (2026-10-12, spec Section 16):

- **Budgets.** The gateway enforces the per-run budget, which a run passes only by the calls its agents start while it is under it (spec Section 5.6), on cost-weighted tokens, priced as the vendor prices them, and refuses further calls once a run's budget is spent.
- **Attribution.** It records tokens and spend per run, agent and arm.
- **Allowlist.** It checks every request as a whole, and rejects and audits anything outside the list of spec Section 9.3.
- **Build, not extend (decided; built in Plan 6b).** The gateway is a thin Go service for one vendor's API. The allowlist covers the whole request, the tokens are scoped to runs and agents, budgets must commit together with run state in Postgres, and prompts go only into the hash-chained audit store. An existing proxy would need all four as custom extensions, and would add one more component and trust boundary to operate.

## Rationale

- **Cost.** Prompt caching cuts the cost of resent context sharply; at the time of the decision, cache reads cost about 10% of the base input price.
- **Compliance.** One vendor means one set of terms, data regions and retention to review.
- **Where replacement happens.** A vendor is replaced at the gateway and checked on the retrospective set, not through an abstraction layer in code.

## Rejected options

- **A:** vendors differ a lot in tool calling and caching, so the abstraction costs now, and pays only when a second vendor is needed.
- **C:** for 1–2 people, neither the diagnosis quality nor the cost of operating models is worth it.

## Consequences

- When the vendor is down, agent turns fail and people use their usual process (spec invariant 5). Nothing switches vendors automatically.
- A model change is an agent change, with a new version, smoke scenarios and the retrospective set (spec Section 12.3; ADR-003).

## Reversibility

Medium. Changing vendors means re-running the retrospective set, agreeing new data terms and replacing the gateway's vendor adapter, about [1–2] person-weeks.

## Revisit when

- The vendor's monthly availability during on-call hours falls below [99.5%], or more than [3] incidents in a month go without agents because of the vendor.
- A design partner or customer requires another vendor, or self-hosting.

## History

- **2026-10-10:** proposed.
- **2026-10-12:** rewritten in English. Budgets, attribution and the allowlist added; building the gateway proposed (spec Section 16).
- **2026-10-13:** the first deployment's data terms left this repository with its profile; each deployment confirms its own.
- **2026-10-18:** built in Plan 6b: an allowlist of what the runtime sends, the models' prices as the vendor charges them, budgets and the daily cap in cost-weighted tokens at a reference price, and every call recorded in the chained audit store. After its final review, the Context defines the budget as spec Section 5.6 does, and building, not extending, is marked decided.
