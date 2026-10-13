# Plan 3b: The Mechanism Check's Agent Runs — Plan, Pilot and Preregistration

| | |
|---|---|
| Status | Executed on 2026-10-14. The author approved the preregistration, and the test run passed the mechanism check: C1 in every run, C2 in two of three trials. |
| Date | 2026-10-14 |
| Roadmap | `2026-10-12-implementation-roadmap.md`, Plan 3, the agent runs. Plan 3a built the data, the answers and the scorer, and fixed the pass criteria. |
| Decided by the author | On 2026-10-13: pilot on all 20 development scenarios first; agents run in-process; three investigators (changes, runtime, platform); C2 stays at two of three trials. Every agent run uses Claude Haiku 5.5. |

**Goal:** Run the planned agents on the benchmark through the platform's retrospective check command. Pilot them on the development set, freeze their skills and budgets, and fix everything the test run depends on before it runs.

**Architecture:** `agentswarm check` (platform) runs each case of a set once per trial. A run is one round: the agents work at once and alone, each through its own toolbox, which holds the case's read-only MCP tools, recorded as read calls, and the board tools. Plan 1's board rules and assessment run over in-memory ports, so hypotheses are aggregated by key as in the independent run. The conclusion is scored against the case's labels and baseline, and every run is recorded and can be resumed. On the benchmark side, `itbench serve --datasets` serves every scenario at `/<id>/mcp`, and `itbench checkset` writes the set the command reads.

**Tech Stack:** Go 1.26; anthropic-sdk-go v1.80.0; the MCP Go SDK v1.8.0; cobra v1.10.2; testify v1.12.0. Model: `claude-haiku-5-5`.

**Spec:** `plan/2026-10-11-agent-swarm-platform-spec.md`, Sections 3.5 (the `independent` shadow run: one round, the full time budget, no agent sees another's entries), 4.2–4.3 (entries, provenance, convergence, the conclusion), 5.6 (cost-weighted token budgets), 13.2 (false convergence), 13.4 (matching and what raters judge), 14.1 (the mechanism check), 14.4 (the retrospective check command) and 15 (parameters).

**Verified:** `make lint test-all` (unit, integration and end-to-end) and the benchmark module's tests and lint pass. Mutation checks broke each of the check's guarantees in turn and confirmed that a test fails. A fresh review of the whole change found no path for labels or the baseline to reach an agent and no channel between agents; its other findings are fixed (see the last section). Real runs: one smoke run, four pilot rounds (152 runs) and a regression run after the fixes.

## What this plan built

| Commit | What |
|---|---|
| `174a91f` | The retrospective check command: `internal/runner` (one agent's turn as a thin loop on the Messages API, ADR-006), `internal/work/memory` (in-memory board ports), `internal/measurement/check` and `agentswarm check` |
| `720fb7e` | Benchmark: `serve --datasets`, `checkset`, and a fix to `prepare` for the one scenario that keeps its trigger file beside its tables |
| `142641d` | Benchmark: the development set (`dev.yaml`) and the stand-in team's agents (`agents/`): three skills and the failure modes' meanings |
| `202809f`, `33924e8`, `7b8ac53` | Skills revisions 1–3, each with the pilot round that prompted it |
| `68cd69e`, `e71481f` | The review's fixes |

## How a check runs

1. `agentswarm check --set set.json --agents agents.json --out DIR` reads the set and the agents, and claims `DIR`: a new directory, or one that holds a check with the same inputs, made by a clean build.
2. For each trial and each case, one run: the agents start at once. Each connects to the case's servers, lists the tools that declare `readOnlyHint`, and gets two board tools (`post_evidence`, `post_hypothesis`) and `end_turn`. Its system prompt is the platform's rules of the board and then its skill. Its first message is the case's task: the alert, the services, the failure modes with their meanings, and what the servers say of themselves.
3. Each read is recorded with an id that only its agent can cite. Posts go through Plan 1's board rules; a refused post goes back to the agent with the rule it broke.
4. When every turn has ended, the board is assessed as the independent run's is. The conclusion is a hit when its key equals one of the case's labels. The report marks what Section 13.4 would leave to raters, and checks every piece of evidence again against the recorded calls.
5. `DIR` gets each run's board (in the `conclude` command's form), its read calls, the agents' transcripts and the run's report, then `report.json` and, for each trial, `answers-t<n>.json` in the scorer's form.

## Decisions this plan makes where the spec is silent

| Decision | Choice | Why | Pinned by |
|---|---|---|---|
| A run | One round; every turn ends by the round limit, 15 minutes | The independent shadow run's override (Section 3.5) | `TestTheDeadlineEndsTheTurn` (runner) |
| Token budget | The run's budget, 2,000,000 cost-weighted tokens, split evenly among the agents. No call starts whose worst case, its request written to the cache in full and a reply of `max_tokens`, could pass the turn's share, so no turn passes it | Section 15's provisional default; Section 5.6 counts cost-weighted tokens | `TestNoCallStartsThatCouldPassTheBudget`, `TestATurnOverItsShareOfTheBudgetIsAViolation` |
| Cost weights | Uncached input 1, cache writes 1.25, cache reads 0.1, output 5 | The vendor's prices relative to uncached input, for requests under 100,000 tokens | `TestTheTokenBudgetCountsCostWeightedTokens` |
| Prompt limit | 90,000 input tokens a request, at most 100,000 | Keeps the weights true, and bounds a turn's context | `TestThePromptLimitStopsTheTurnBeforeABiggerRequest`, `TestThePromptLimitMustKeepTheWeightsTrue` |
| Entries | At most 5 an agent | Section 15's default for a round; the independent run keeps it | `TestTheEntryLimitRefusesExtraEntries` |
| What an agent sees | The alert, the services, the failure modes with meanings, and the servers' own instructions. Labels and the baseline sit in a part of each case that no prompt or tool reads | The labels must never reach an agent | `TestEachAgentGetsItsShareOfTheBudgetAndTheTask` |
| Independence | A post's answer names only its new entry, entry ids are random, and call ids are each agent's own; another agent's call id gets the answer an unknown one gets | Section 3.5: agents never see each other's entries | `TestAPostRevealsNothingOfTheOtherAgents`, `TestCitingAnotherAgentsReadSaysNoMoreThanCitingNothing` |
| Read-only | Only tools that declare `readOnlyHint` are offered; any other name is refused before a server sees it | C1's third clause holds by construction | `TestOnlyReadOnlyToolsAreOffered` |
| Long reads | Cut at 40,000 bytes on a character boundary, with a note; only what the agent saw can be quoted, and a cut JSON document keeps its whole strings for matching | Bounds the context; escaping must not decide whether evidence counts (Section 4.2) | `TestALongReadIsCutAndOnlyWhatTheAgentSawCanBeQuoted`, `TestAQuoteMatchesAStringOfAJSONDocumentCutShort` |
| Replies cut or refused | A reply stopped by `max_tokens` or a refusal ends the turn with its own outcome, and its tool calls are not run | Its tool calls may be partial | `TestACutOrRefusedReplyEndsTheTurnWithoutItsToolCalls` |
| Infrastructure failures | A model call that fails after six retries, a lost server or a read with no answer in a minute ends the turn and marks the run as failed. Running the same command again redoes exactly the failed runs, and moves their records aside | They test the vendor and the network, not the mechanisms | `TestAnUnreachableServerIsAnErrorNotAMiss`, `TestAReadThatDoesNotAnswerIsAnErrorNotTheEndOfTheRound`, `TestARerunKeepsFinishedRunsAndRedoesFailedOnes` |
| Resuming | Only with the same inputs (the set, the skills' hashes, the model and every limit), from a clean build, and only if every run of a case read the same servers and data | A check must never join runs made by other code or against other data | `TestARerunRefusesOtherInputs`, `TestOnlyACleanBuildCanResumeACheck`, `TestACaseRunAgainstOtherServersCannotJoinItsEarlierRuns` |
| C1's recheck | After each run, every agent's evidence is checked against the recorded calls: the call exists, is the author's, did not fail, holds the quote, and is the evidence's source | The board checked it at post time; this is the check's own record | `TestRecheckFindsEvidenceThatTheRecordsDoNotBackUp` |
| Scoring | A hit is the conclusion's key equal to a label. The 13.4 marks: a key with `other`, or a miss involving `dependency-failure` or another service | Plan 3a's criteria and its clarification | `TestScoringLeavesToRatersWhatSection134LeavesToThem` |
| False convergence | A run in which any hypothesis that misses converged | Section 13.2's guardrail | `TestAnyConvergedMissIsAFalseConvergence` |
| Failure modes' meanings | Part of the set, written by the team; here `agents/failure-modes.yaml` | Agents choose a mode from what it means | `TestLoadSetRefusesASetThatCannotBeScored` |

## The development set

`bench/itbench/dev.yaml`: the ITBench-Lite SRE scenarios that are not in the test set, 19 of 20. Its keys follow the test set's conventions and were committed in `142641d`, before any agent ran on these scenarios.

- **S38 is excluded.** It is a cost scenario: its fault is every service's autoscaler, which a key naming one service cannot express, and its tables hold nothing in the 30 minutes before its trigger. The author chose all 20; this one cannot be run.
- **S1's keys follow the data, not the ground truth's text.** The ground truth says the load generator was set to more users; the data shows the flag `loadGeneratorFloodHomepage` set to 100.
- **S18's ground truth contradicts itself:** its groups name a product-catalog delay, while its fault and propagations name the checkout pod. The keys follow the fault. The pod's image was swapped for a pause image, which its spec shows, so `bad-release` counts as well as `infrastructure-failure`.
- **S29 has no fitting mode.** A method of the ad service was made to return null. The key is `ad/bad-release`, the mode the ground truth's category points to.
- **The data** comes from Plan 3a's pipeline. A manual search of the 19 datasets for injection traces (stress tools, JVM agents, traffic shaping, the chaos resources' names) found only the pause image in S18, which the pod's spec shows to any operator, so it stays.
- **What the development set cannot show.** Its 10 change scenarios are all flag changes, on which the baseline is perfect (10 of 10). The test set's 9 change scenarios are 2 flag changes and 7 changes to Deployments: an environment variable, an image, a command, a node selector, a quota. The pilot says little about C2.

## The pilot

**Procedure.** Rounds on the development set, each with one version of the skills. At most three revisions, each made only from development results and general practice, never from test data, and each committed with the round that prompted it. Rounds 0 and 1 ran one trial; one trial cannot tell a revision from chance, so rounds 2 and 3 ran three. The rule that chose the skills to freeze was committed before round 3 ran: of revisions 2 and 3, the one with more hits over three trials; on a tie, the one with fewer false convergences; on a further tie, revision 3.

**Before round 0**, one smoke run on S2 showed agents citing call ids as evidence ids and inventing hypothesis ids. The board tools' descriptions now say which id goes where, and an unknown id's refusal says which ids to cite. This changed the platform, not the skills, and preceded the commit of the development set.

| Round | Skills | Trials | Hits | Changes (of 10) | Others (of 9) | No conclusion | False convergence | Cost-weighted tokens a run: median / p90 / max | Seconds a run: median / max | Cost |
|---|---|---|---|---|---|---|---|---|---|---|
| Baseline | | | 10 | 10 | 0 | 9 | | | | |
| 0 | `142641d` | 1 | 12 | 8 | 4 | 1 | 2 | 196,628 / 294,175 / 310,191 | 28 / 46 | $0.39 |
| 1 | `202809f` | 1 | 10 | 5 | 5 | 2 | 3 | 151,174 / 270,847 / 350,562 | 25 / 36 | $0.33 |
| 2 | `33924e8` | 3 | 12, 13, 13 | 8, 8, 8 | 4, 5, 5 | 1, 2, 1 | 2, 2, 1 | 171,589 / 257,153 / 283,314 | 25 / 42 | $0.99 |
| 3 | `7b8ac53` | 3 | 13, 13, 13 | 8, 8, 8 | 5, 5, 5 | 1, 1, 2 | 2, 1, 3 | 162,829 / 264,742 / 362,929 | 27 / 79 | $1.00 |

No run broke a board rule and no turn failed. No reply in any round was cut by `max_tokens`; one turn in round 0 reached the prompt limit. Revision 3 has 39 hits to revision 2's 38, so **revision 3 is frozen**.

**The revisions.**

1. After round 0: judge a change by what got worse right after it, not by whether the alert fired before it (the benchmark's alerts fire on a trickle of errors); allow for changes that act through traffic; check the callee before blaming the network, and compare client with server latency; post evidence and the hypothesis early.
2. After round 1: the investigators framed one cause with different keys, so their hypotheses never combined (`ad/flag-change` against `flagd/flag-change`, both labels, tied). All three skills now share one Keys section: key the cause, not its symptoms; key a flag change to the service it affects; key a network fault to the service whose calls fail. The changes investigator backs a change with more read tools, and the others read the change log.
3. After round 2: set aside a condition that held all along, such as the front end's CPU throttling in this environment, unless it got worse when the failure began.

**Round 3, case by case** (Y hit, - miss, n no conclusion):

| Case | Change | Trials | Conclusions |
|---|---|---|---|
| S2, S6, S7, S8, S11, S13, S14, S15 | yes | YYY | the flag's service, `flag-change` |
| S1 | yes | --- | `frontend/resource-exhaustion`: the front end's CPU is pinned from the first minute, 23 minutes before the flag |
| S9 | yes | nnn | the flag's effect is one or two errors a minute; the changes investigator declined to blame it |
| S19, S22, S25, S35, S83 | no | YYY | |
| S17 | no | --- | `frontend/infrastructure-failure` (the delay was on product-catalog) |
| S18 | no | --- | `checkout/config-change`: the agents read the swapped image as a configuration change |
| S29 | no | --- | `ad/flag-change` |
| S91 | no | --n | the partition is off the alert's call path |

**What the pilot says about budgets.** Across the four rounds, the largest run took 362,929 cost-weighted tokens and the largest turn 173,248, against a share of 666,666. The most steps in a turn was 16, against 50. The longest run took 79 seconds, against 15 minutes. Nothing bound but the prompt limit, once. The test run keeps these limits, so that they protect against a runaway turn without shaping the agents' work.

**After the review's fixes**, a regression run of four development cases with the frozen skills hit all four, with no errors and no violations (`e71481f`).

## Preregistration of the test run

Fixed here, before the test run. Changing any of it after the run starts voids the check.

**Inputs.**
- Code, skills and failure modes: the commit that adds this document, built from a clean checkout. The report's `inputs.json` names the build.
- The skills: revision 3 (`7b8ac53`), unchanged since.
- The answers: `bench/itbench/scenarios.yaml` as Plan 3a froze it.
- The datasets: `~/itbench/datasets`, prepared by Plan 3a. Prepared again with this plan's code, all 130 files are identical, compared by hash only.

**Commands**, from `bench/itbench`:

```bash
./itbench serve --datasets ~/itbench/datasets --addr 127.0.0.1:8080
./itbench checkset --datasets ~/itbench/datasets > ~/itbench/test-set.json
agentswarm check --set ~/itbench/test-set.json --agents agents/agents.json --out ~/itbench/check-test --concurrency 3
./itbench score --answers ~/itbench/check-test/answers-t1.json --baseline ~/itbench/baseline-service.json
```

`score` runs once per trial. Every other parameter is `agentswarm check`'s default: model `claude-haiku-5-5`, 3 trials, `max-tokens` 4,096, `max-steps` 50, `prompt-limit` 90,000, `run-budget` 2,000,000, `round-limit` 15m, `read-timeout` 1m, `entry-limit` 5, `max-output` 40,000 bytes, 6 retries.

**Pass criteria:** C1 and C2 as Plan 3a fixed them, read as follows.

- **C1, every run.**
  - "Ends with a conclusion or with none, within its budget": every turn ends with an outcome, never an infrastructure error, and no turn passes its share of the budget.
  - "Every evidence quote matches the output of the read it cites": the board refuses any other evidence, and the recheck finds no exception.
  - "Every call goes to the read-only tools": by construction.
  - C1 holds when the final `report.json`, after any reruns, shows `"violations": 0` and `"errors": 0`.
  - A turn ended by a cut or refused reply is not a violation, and is reported.
- **Infrastructure failures.** Running the same command again redoes the failed runs, and each rerun is reported. A run that fails three times voids the check: it neither passes nor fails, and the author decides.
- **C2.** In at least two of the three trials, `changeHits` is at least `changeBaselineHits`, which is 8 of 9.

**Reported, not gated:**
- hits on all 13 scenarios and on the 4 without a change;
- the service-scoped baseline, through `itbench score`;
- false convergence, and the cases that Section 13.4 would leave to raters;
- cost-weighted tokens per run and per turn (median, p90, max), seconds per run, turns cut or refused, and cost.

**The first token budget default** (Section 14.1): the test runs' largest cost-weighted tokens per run, rounded up to the next 100,000. It is proposed for a one-round run of three agents. Section 15's [2,000,000] stays until the author changes it.

**After the run.** A pass meets the mechanism check's half of Gate A; it is not evidence of value (Section 1.4), and Plans 4–10 still wait for a team. A failure pauses the build to rethink the skills, the runtime or an off-the-shelf tool (Section 14.1). A rerun with changed skills is a new check, with a new preregistration, reported next to this one.

## Results of the test run

Run on 2026-10-14 exactly as preregistered, from a clean build of `94d541e`; the records are in `~/itbench/check-test`, outside the repository. No run failed, so nothing was rerun.

| | Hits (of 13) | Changes (of 9) | Others (of 4) | No conclusion | False convergence | Rater cases | Cost-weighted tokens |
|---|---|---|---|---|---|---|---|
| Trial 1 | 9 | 7 | 2 | 2 | 1 | 1 | 1,935,878 |
| Trial 2 | 10 | 9 | 1 | 0 | 2 | 3 | 1,581,250 |
| Trial 3 | 10 | 9 | 1 | 0 | 2 | 3 | 1,658,558 |
| Namespace baseline | 8 | 8 | 0 | | | | |
| Service baseline | 1 | 1 | 0 | | | | |

- **C1 passes.** No violation and no error in any run. Every turn ended by `end_turn` (34) or with a final answer (83); no reply was cut or refused.
- **C2 passes.** The conclusions hit 7, 9 and 9 of the 9 change scenarios against the baseline's 8: two of three trials.
- **The mechanism check passes.** It is not evidence of value (Section 1.4).

| Case | Change | Trials | Conclusions |
|---|---|---|---|
| S5, S16, S20, S24, S33, S102, S105 | yes | YYY | |
| S4 | yes | nYY | In trial 1, `product-catalog/flag-change` and `frontend-proxy/infrastructure-failure` tied: the runtime investigator blamed the steady upstream resets |
| S23 | yes | -YY | In trial 1, the agents converged on `checkout/config-change`, reading the unparsable image name as configuration: a false convergence |
| S81 | no | YYY | |
| S21 | no | Y-- | `valkey-cart/dependency-failure` twice, which raters would judge |
| S34 | no | n-- | `cart/infrastructure-failure` twice, another service |
| S80 | no | --- | `kafka/dependency-failure`, which raters would judge |

**Budgets.** A run took 93,112 cost-weighted tokens at the median, 242,170 at p90 and 255,710 at most; a turn 32,971, 88,903 and 126,457. A run took 21 seconds at the median and 39 at most. The 39 runs cost $0.52.

**The first token budget default** is 300,000 cost-weighted tokens, for a one-round run of three agents: the largest run, rounded up as preregistered. Section 15's [2,000,000] is for a run of up to six rounds and four agents, and stays until the author changes it.

**What the result says.** The mechanisms work end to end on a public benchmark: reads through read-only tools, evidence with checked provenance, hypotheses aggregated by key, and a conclusion, in 117 turns without a violation. On change faults the agents matched or beat the "most recent change" baseline in two of three trials; on the faults that are not changes, where the baseline has no answer, they hit one or two of four. Their misses are framings, not mechanics: two investigators keying one cause differently (S4, S23), and `dependency-failure` against `infrastructure-failure` for a partition and a password (S80, S34, S21).

## Limits of the check

- **One author wrote both the skills and the keys, and knows the test faults.** The skills were revised only on development data; every revision is a commit with its reason, and no skill names a test scenario's service or fault. The commits can be audited.
- **The pilot cannot vouch for C2:** the development set's changes are all flag changes, and the test set's are mostly changes to Deployments.
- **The benchmark is public, and its environment, the OpenTelemetry demo, is well known.** The data is stripped of the benchmark's own names, but a model may know the demo.
- **One trial per case and round is noisy**, and three trials are few. The check's verdict rests on Plan 3a's thresholds, not on statistical significance.

## Changes made during execution

| Change | Why |
|---|---|
| S38 excluded; 19 development scenarios instead of 20 | It cannot be expressed as a key, and holds no data before its trigger |
| `prepare` finds a trigger file beside the tables | S11 keeps it there; the test datasets are unchanged |
| The board tools' descriptions and the unknown-id hint | The smoke run, before the development set's commit |
| Rounds 2 and 3 ran three trials | One trial could not tell a revision from chance |
| The review's fixes (`68cd69e`, `e71481f`) | Resuming pinned to clean builds and the same data; cut and refused replies; the budget enforced and reported; quotes in cut JSON; false convergence as Section 13.2 defines it; read timeouts; one answer for unknown and foreign call ids; locking and whole writes. None changes what an agent is told or offered, and none bound in the pilot |
| Deferred | Telling agents their limits (it would change their conditions after the pilot; Plan 5's wrap-up rules), and alert polls for S11, a development scenario with none |
