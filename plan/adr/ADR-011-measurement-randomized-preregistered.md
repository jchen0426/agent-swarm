# ADR-011 Measurement: randomized by incident, preregistered, scored blind

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-10; revised 2026-10-12 |
| Related | Spec Sections 4.1, 7 and 13; example deployment profile Section 4.8 |

## Context

A before-and-after comparison of about 20 incidents has four problems:

- **Too small to separate the lines.** The stop line and the target line cannot be told apart statistically. For example, 14 of 20 has a 95% Wilson interval of about [48%, 86%].
- **Thresholds set afterwards,** by the author, after seeing the baseline data.
- **Contaminated ground truth.** People who already saw the agents' conclusions would write the root causes used to score them.
- **Confounders:** seasonality, regression to the mean, the Hawthorne effect and the team's own learning.

## Options

- **A.** Before and after, read as direction only.
- **B.** Randomize, per incident, whether agent output is visible to responders; preregister the analysis; have people who are not builders score blind.

## Decision

B, rebased on incidents and shadow mode (2026-10-12):

1. **Arms by time cluster.** An item takes the arm of any in-scope item that opened in the previous [30 min] and is still open. Otherwise a new arm is drawn from a recorded seed, at the invisible share [50%]. A cascade across services stays in one arm, and an incident whose items end up in different arms is excluded (spec Section 13.1).
2. **Shadow mode.** In the invisible arm the primary run executes in shadow mode: hidden until release, record-only, and without human input (spec Section 7).
3. **Incidents and finality.** Measurement counts incidents and scores each on its first item. An incident's rating packet and sample membership are fixed at its finality (spec Section 4.1).
4. **The cutoff.** A conclusion is scored as of the earliest of the run's stop, the human `cause_identified` and `mitigation_started` times, and the first visible human change, and never before the end of the first round. The baseline sees only what was known at the alert (spec Section 13.2).
5. **Preregistration.** Before any data is seen: the metric definitions, the in-scope rule (a fixed service list), the exclusion rules, the thresholds, the sample size and the shortfall rule. Someone who is not a builder co-signs it.
6. **Blind scoring in two stages.** Raters first record the ground truth as one key or a set of equivalent keys, before seeing any output. Only then do they judge shuffled outputs in one format. At least two raters who are not builders score each packet, and their agreement is reported (spec Section 13.4).
7. **The gate.** The hit rate's 90% Wilson lower bound must be at least [50%], on a fixed sample: the first [20] invisible-arm incidents to reach finality, within [8 weeks]. The agents must also beat the baseline on discordant incidents by the preregistered margin. False convergence is a guardrail that fails the gate; cost is a guardrail that holds back Phase 1 (spec Sections 13.5 and 13.7).
8. **Phase 1 entry.** Action accuracy (raters judge the invisible arm's recorded proposals) and headroom (removable wait only) must meet their preregistered minimums (spec Section 13.2).
9. **Shortfall.** If the sample is not reached in time, the available incidents are evaluated with the same bound and margin rule. The only choices are then a new window or staying read-only. Phase 1 never starts on directional accuracy or responder feedback alone.
10. **Builders.** Incidents handled by builders are excluded or reported separately.
11. **After the gate.** Measurement becomes monitoring: disagreement incidents plus a random [20%] are labeled, rolling figures use the random sample, and a small invisible share [10%] may continue as an uncontaminated reference (spec Section 13.8).

Comparators are scored on the same incidents: the baseline rule (for example "most recent change"), the `independent` shadow run, and optional off-the-shelf tools as shadow runs (spec Section 13.3).

## Rationale

Only randomization and preregistration give a defensible result at about 20 incidents, and they leave the builders no room to confirm their own beliefs. Shadow mode lets the invisible arm run to its own end without human input, so its time to a correct conclusion is observed rather than cut off.

## Rejected options

- **A:** at about 20 incidents it cannot separate the stop line from the target line, and it is confounded in all the ways listed above.

## Consequences

- The team lead must agree in advance that some incidents will not show agent output (spec Section 14.3).
- Arms, shadow runs, cutoffs, finality and rating packets are part of the Phase 0 build. Monitoring after the gate is built during the window, behind flags (spec Section 14.1).

## Reversibility

Medium. Arms and shadow mode are built into the coordinator, but shares, thresholds and sample sizes are parameters.

## Revisit when

- The team does not accept that agent output is hidden at random.
- In-scope volume cannot reach the preregistered sample within [8 weeks] (spec Section 13.5).

## History

- **2026-10-10:** accepted.
- **2026-10-12:** rewritten in English and rebased on shadow mode and incidents. Added time-clustered arms, the cutoff, finality, discordant-incident margins, action accuracy, the fixed gate sample, the shortfall rule and monitoring after the gate (spec Section 16).
