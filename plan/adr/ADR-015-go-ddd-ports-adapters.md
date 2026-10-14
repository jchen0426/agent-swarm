# ADR-015 Implementation: Go, domain-driven design, ports and adapters

| | |
|---|---|
| Status | Accepted. |
| Date | 2026-10-12 |
| Related | Spec Sections 2.2, 3.3, 3.6, 9.1 and 12; ADR-004; ADR-006; ADR-007 |

## Context

- On 2026-10-11 the author chose Go, and domain-driven design with interface-driven ports and adapters.
- The spec leaves the condition language to this ADR. It must be one sandboxed, side-effect-free expression language for all conditions, with values bound as typed variables and never spliced into condition text (spec Section 3.3).
- The earlier records left the implementation language open, which blocked the choice of task library (ADR-004) and part of the runtime question (ADR-006).

## Decision

1. **Go** for the platform: the coordinator, the MCP gateway, runner control, the LLM gateway and the human interface backend. The agent runtime inside a sandbox may use another language (ADR-006).
2. **Bounded contexts,** as spec Section 2.2 proposes: Work and Actions (core), Registry and Measurement (supporting), Access and Audit (generic). Each uses the spec's vocabulary; the implementation plan confirms the split.
3. **Ports and adapters.**
   - Domain and application code depend only on interfaces they declare themselves. Infrastructure adapters implement them.
   - Dependencies point inward. Domain packages import no database, network or framework packages.
   - Ports exist only at real boundaries: persistence, job queue, configuration source, identity provider, MCP servers, model gateway, sandboxes, notifications, clock, identifiers, condition evaluation, secrets and the audit anchor. Each interface is small and declared by its consumer.
   - Wiring happens in `cmd/`.
4. **Fakes are adapters.** Fake MCP servers, scripted agents and a fake clock implement the same ports, so the deterministic test layers need no network or model calls (spec Section 12).
5. **Conditions in CEL.** Conditions such as `partialWhen`, `scope` bindings and trigger consistency checks are CEL expressions. They are type-checked against declared variables when configuration loads, evaluated with a cost limit, and receive values only as bound variables.
6. **Selectors and templates.** Fields are picked from tool output and inbound payloads with JSONPath (RFC 9535), as in `field: "$[*].name"` and `split`. Strings are built with `{name}` templates over whole, validated values. Neither can call functions.
7. **Libraries,** confirmed in the implementation plans: River for jobs (ADR-004), pgx for Postgres, and the official MCP Go SDK on both sides of the gateway.

## Rationale for CEL

- **Typed.** An expression is checked against its declared variables when configuration loads, so the loader can refuse a bad condition (spec Section 3.7).
- **Bounded.** It is side-effect-free and not Turing-complete, and evaluation has a cost limit.
- **Mature in Go.** The reference implementation is in Go, and Kubernetes uses it for admission policies.
- **Bound, never spliced.** Values arrive as variables, which is what spec Section 9.1 requires.

## Rejected options for conditions

- **An external policy engine:** out of scope (spec Section 1.4).
- **A general scripting language** (Lua, JavaScript, Starlark): more power than conditions need, and harder to bound.
- **A hand-written language:** a parser and a type checker to own and test.
- **expr-lang:** capable, but CEL's static typing and cost model fit the loader rules better.

## Consequences

- The domain vocabulary is settled before code, because the spec's terms become type and package names: "Agent", never "Employee".
- A new port needs a real boundary behind it; a single implementation inside the process stays a concrete type.

## Reversibility

Low for Go. Medium for CEL, because conditions are short and few.

## Revisit when

- A condition the spec needs cannot be expressed in CEL.
- CEL evaluation shows up in the gateway's latency.

## History

- **2026-10-12:** accepted with the spec; CEL proposed (spec Section 16).
- **2026-10-14:** CEL accepted with Plan 4: cel.dev/cel-go v0.32.0, three typed environments and a cost limit of 100,000.
