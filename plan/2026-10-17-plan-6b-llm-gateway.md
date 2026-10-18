# Plan 6b: The LLM Gateway — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Executed on 2026-10-18 on main, each task by a fresh implementer and checked by a fresh reviewer. See "Changes made during execution" at the end. |
| Date | 2026-10-17 |
| Roadmap | `2026-10-12-implementation-roadmap.md`, Plan 6, second half |
| Decided by the author | On 2026-10-14: Phase 0 is built before a team adopts the platform, on the assumptions ADR-016 records |

**Goal:** Build the LLM gateway, the only route from an agent's sandbox to the model vendor (spec Section 2; ADR-007). It forwards only what the platform's runtime sends; it redacts before the vendor sees anything; it records every request, refused or not, with its prompt and completion, in the chained audit store; and it holds each run to its token budget and the deployment to its daily cap, priced as the vendor charges.

**Architecture:**
- **A new bounded context, LLM** (`internal/llm`). The gateway's request policy, its costs and its budgets are neither tool policy (Actions) nor runs (Work).
- **The domain.** Three packages:
  - `prompt` reads a Messages API request strictly, checks it against the allowlist, redacts what the model reads, and writes the gateway's own encoding;
  - `completion` reads the vendor's answer: its tokens, its record and its thinking blocks;
  - `cost` prices a call on its model's card, and decides whether a call may start.
- **The use case.** `internal/llm/app.Gateway` serves one request: session, allowlist, redaction, budgets, then the record, the vendor call and the outcome. Its ports are `Sessions`, `Revisions`, `Vendor` and `Calls`.
- **The adapters.**
  - `internal/llm/anthropic` is the vendor;
  - `internal/llm/endpoint` is the HTTP endpoint the runtime's client is given as its base URL;
  - `internal/audit/postgres.ModelCalls` is the record, in three new chained tables;
  - `internal/llm/memory` holds sessions and calls in memory, for tests.
- **Wiring.** Plan 7's `serve` wires it, with the coordinator's sessions.

**Tech Stack:**
- Go 1.26
- pgx v5.10.0
- github.com/anthropics/anthropic-sdk-go v1.80.0, already a dependency: the runtime's client, used in the contract tests
- testify v1.12.1
- testcontainers-go v0.44.0, with Postgres 17

**Spec:** `plan/2026-10-11-agent-swarm-platform-spec.md`. Read these before the tasks that implement them:
- Section 2, the LLM gateway and the session tokens;
- Section 5.6, the token budget;
- Section 7, the daily cap and the cut at 80%;
- Section 8.1 and 8.2, failures;
- Section 9.1, 9.3 and 9.4: threats, the allowlist and secrets, redaction and retention;
- Section 13.6, cost;
- Section 15, the parameters.

Also read ADR-006 (the runtime, a thin loop on the Messages API) and ADR-007 (one vendor behind a built gateway).

**Reviewed before it was written:**
- **The design** was read by a fresh reviewer on Opus 5.5, who checked it against 286 recorded answers of the platform's model and the SDK's source. It found two Critical issues, eight Important and eleven Minor. The worst three:
  - The allowlist refused the thinking blocks and the `caller` field that the runtime's client sends back on every turn after the first, so the platform's own runtime could not finish a run.
  - Budgets counted every kind of token alike, while the vendor charges an output token several times an input token and a cache read a tenth of one: a budget bounded neither money nor a mix of models.
  - A call whose answer was lost, or whose request ended with the round, counted as costing nothing.
- **The built code** was read by another. It found one Critical issue, seven Important and ten Minor, each backed by a probe. It also built the 253 requests the runtime would send after the 39 recorded turns, with the SDK, and none was refused. The worst three:
  - A vendor call that went on after its request was answered freed its session. A session could then have several calls at the vendor at once, and a run spent eight times its budget.
  - A request the vendor refused cost nothing and counted against nothing, so a sandbox could fill the audit store with forwarded requests.
  - An id or a name carried the session's token, or any secret, to the vendor unredacted.

  Each finding is fixed below with a test that failed first, but one Minor finding: a replacement next to the text around it can still match a pattern, which Decision 5 records.
- **The fixes** were read by a third, looking only for what they broke. It found no Critical issue, one Important and seven Minor, each backed by a probe. Under stress, a session never had two calls at once, budgets held, and the refusal cap held at exactly its limit. The Important one: a call that hung held its session until the call timeout, [10 min], so its run lost the rounds in between, while a shorter timeout would have turned long answers into vendor failures. So a call that outlives its request now holds its session for [15 s] at most (Decision 8). The Minor ones are fixed as well:
  - calls are drained when the gateway stops;
  - records are read and written apart from the request, so a request whose client went is recorded;
  - a replacement holds no quote, and a rule that would refuse every tool call is refused;
  - thinking blocks are recorded redacted.
- **The fixes of those** were read by a fourth. It found no Critical issue, two Important and seven Minor, each backed by a probe. Under its stress, a session never had more than two calls at the vendor, and no lane, goroutine or race was left. Both Important findings came from the third review's fixes:
  - `Drain` counted only the calls that outlived their requests. It missed a request still in hand when it started, and a counter reused while it waited panicked.
  - A request that ended while its call was being recorded did not count against the session's round, so a sandbox could fill the store with such records.

  Both are fixed. So are the Minor ones:
  - board tools' names, and 10,000 sampled ids, are checked;
  - no call goes on beside one that hangs while the vendor answers no one;
  - a record's digest is of the record;
  - a call recorded and not made has its outcome tried again.

  These fixes were checked with the reviewer's own probes but not reviewed again: the final review after execution reads them first.

Every finding is answered below: by a decision with its test, by a ruling, or by a constraint on a later plan.

**Verified:** Every code block in this plan was compiled, tested and linted in a scratch copy. It was then replayed, task by task, in a fresh clone of b00fada:
- each task's tests fail before its implementation exists, and pass after it;
- `golangci-lint run ./...` stays at `0 issues.` after every task;
- the end state matches the scratch copy, file for file;
- `make lint test-all` passes on the end state.

Each rule a review asked for was also checked by mutation, 117 in all: the test that names the rule passes; with the rule removed from the code, the code still builds and that test fails.

## Global Constraints

- **Module and Go version.** The module path is `github.com/jchen0426/agent-swarm`, with `go 1.26.0`.
- **Vocabulary.** Names come from the spec: model call, prompt, completion, run, agent, session, budget, daily cap, cut, cost-weighted tokens. Never "employee".
- **Domain purity.** `internal/llm/prompt`, `internal/llm/completion` and `internal/llm/cost` import no database, network or framework package. They share the Actions context's `redact` package, a shared kernel.
- **Ports.** `Sessions`, `Revisions`, `Vendor` and `Calls` are declared by their consumer, in `internal/llm/app`. Wiring lives in `cmd/`, in Plan 7.
- **Records.** The three new tables are audit records from their first migration: append-only, deleted only by the retention job, chained with form 1, and read by verification (Plan 6a's constraint on this plan).
- **Secrets.** The vendor key is a secret reference, read at each call; it never appears in configuration, the database, records or logs (spec Section 9.3).
- **Libraries.** No new module.
- **Lint.** Every task ends with `golangci-lint run ./...` at `0 issues.`.
- **Fuzzing.** Fuzz runs pass `-fuzzminimizetime 0` (Plan 5b, "Rulings").
- **Docker.** The integration tests of Task 6 start Postgres, so Docker must run.
- **Language.** Code, comments and documents are in English.

## Decisions this plan makes where the spec is silent

Review these first. Each is pinned by the named tests. Decisions 1, 6, 7 and 10 amend the spec, in Task 8; approving this plan approves those amendments.

1. **A seventh bounded context, LLM.** The gateway's request policy, prices and budgets have their own vocabulary (request, completion, usage, card, spend) and their own external system. Folded into Actions, that context would hold everything a sandbox's traffic touches. Spec Section 2.2 and ADR-015 gain the context: Supporting, in `internal/llm`.
2. **The allowlist is what the platform's runtime sends** (ADR-006, runtime D), and nothing more:
   - **Endpoint.** `POST /v1/messages`, with no query string. Every other path, method or query is refused and recorded. A request is at most [512 KiB], sent without a content encoding.
   - **Headers.** None of the client's reach the vendor. The adapter sends its own: the key, `anthropic-version` and `content-type`. The client's `anthropic-version`, if any, must be the gateway's (`2023-06-01`). Any `anthropic-beta` is refused. Every other header is dropped: `x-api-key` and `authorization`, which carry the session's token, `anthropic-workspace-id` (Plan 2's constraint), the SDK's `x-stainless-*`, which name the host's OS, architecture and Go version, cookies.
   - **Top-level fields.** `model`, `messages`, `system`, `max_tokens` and `tools`. `model` is the agent's `spec.model` at the item's locked revision, exactly. `max_tokens` is a whole number from 1 to [16,384].
   - **Content.** A message is `user` or `assistant`; its content a string or blocks. User blocks are `text` and `tool_result`, whose content is text. Assistant blocks are `text`, `tool_use` with `caller` exactly `{"type":"direct"}`, and the answer's own `thinking` and `redacted_thinking` blocks (Decision 3). No block holds a source of any kind, and every other block type is refused: images, documents, search results, server tool calls, MCP tool calls, citations.
   - **Tools.** A tool has a name, a description, an input schema and a cache breakpoint. A tool with any `type`, `custom` included, is refused: a typed tool is one the vendor runs.
   - **Names.** An id or a name is `^[A-Za-z0-9_-]{1,128}$`, never a channel for text, and one that holds what a rule hides is refused (Decision 5).
   - **Cache.** A breakpoint is `{"type":"ephemeral"}`, with a TTL of `5m` at most.
   - **Pinned by the gateway.** `service_tier` is `standard_only`, and `inference_geo` is the deployment's when set, so that every call is billed at the rates its card holds.

   (`TestARequestTheRuntimeSendsPasses`, `TestToolsTheVendorRunsAreRefused`, `TestBlocksOtherThanTextToolsAndThinkingAreRefused`, `TestTheCallerIsTheModelItself`, `TestIdsAndNamesAreNotText`, `TestAnIdOrANameThatHoldsASecretIsRefused`, `TestFieldsOutsideTheListAreRefused`, `TestTheModelIsTheAgents`, `TestMaxTokensIsAWholeNumberWithinTheCap`, `TestCacheControlIsFiveMinutesOnly`, `TestHeaders`, `TestTheGatewayPinsHowACallIsBilled`, `TestEveryRefusalIsRecorded`, `TestTheSDKCallsTheVendorThroughTheGateway` and `TestARunnerTurnPassesTheAllowlist`)
3. **Thinking blocks come back unchanged, and only to the session that got them.** The platform's model thinks by default: 208 of 286 recorded answers begin with a `thinking` block, with no text and a signature. The runtime echoes the answer back, and the vendor needs the block unchanged, or it refuses the request.
   - So a thinking block is never redacted.
   - It is accepted only when it is, field for field, one the gateway itself gave the same session (its run and agent) in an answer it recorded and returned. A session lasts across rounds (spec Section 5.2), and so do its blocks.
   - The gateway keeps the SHA-256 of each such block in memory, over the fields a request sends back: [1,024] blocks a session, the oldest forgotten first, for [1,024] sessions, the one used least recently forgotten first. A field the vendor adds to its answers' blocks is not hashed, so it does not keep a block from coming back; a block sent back with a field beyond those is refused.
   - So no text the gateway did not redact reaches the vendor in a block it cannot read. After a restart, a block sent back is refused, which ends the turn (ADR-006); a runtime that carries context into a later round drops the earlier rounds' blocks (Plan 7).

   (`TestThinkingBlocksComeBackOnlyAsTheGatewayGaveThem`, `TestAThinkingBlockGoesBackOnlyFromTheSessionThatGotIt`, `TestAThinkingBlockGoesBackInALaterRound`, `TestASessionInUseKeepsItsThinkingBlocks`, `TestAThinkingBlockIsKnownByWhatGoesBack`, `TestAnAnswerThatCannotBeRecordedIsWithheld`)
4. **A request is read strictly, and the vendor gets the gateway's encoding of it.** The reader refuses what two JSON readers could read in two ways: a key twice in one object, a key in another case than the list's, data after the request, invalid UTF-8, nesting deeper than 64 levels. The gateway then forwards its own encoding: compact, keys sorted, numbers as written. The record holds what was forwarded, redacted (Decision 5). What passes, redacted, passes again. (`TestAmbiguousJSONIsRefused`, `TestTheVendorGetsTheGatewaysEncoding`, `FuzzCheckNeverFails`)
5. **Redaction before the model reads anything** (spec Section 9.4).
   - **What is redacted.** The rules that apply always, at the item's locked revision, redact every part the model reads, each part on its own:
     - text, the system prompt and tool results;
     - every string, key and number of a tool call's input and of a tool's input schema;
     - tools' descriptions;
     - the session's own token, wherever a request holds it.

     Redaction is deterministic, so a prefix forwarded once is forwarded again the same and prompt caching keeps working.
   - **What is not.** Types, cache breakpoints, the model and the thinking blocks of Decision 3. Ids and names neither: a conversation pairs a tool call with its result by them. So a request whose id or name holds what a rule hides, the session's token included, is refused, and the refusal does not repeat it.
   - **Completions.** A completion is returned to the runtime as the vendor sent it, and recorded redacted. The model only ever read redacted text; redacting a tool call's input before the agent makes it would change what the agent does; and thinking blocks must come back unchanged.
   - **Redacting twice.** Text the MCP gateway redacted is redacted again here, and redacting what was redacted must change nothing, or a model's quote of a tool's output would not match the output stored. So the registry now refuses a pattern of a rule that applies always when it changes a replacement: any rule's, or the LLM gateway's `[redacted:session-token]`. A pattern that turns a replacement into itself passes. A replacement holds no quote and no backslash, which redaction would read as a member or an escape. A replacement is checked on its own: where it meets the text around it, a pattern may still match across both, and the model then reads less than was stored, never more.
   - **Names the gateway forwards.** The registry also refuses a pattern of a rule that applies always when it matches a tool as agents name it, a server's (`server__tool`) or a board tool, or any of 10,000 tool-call ids in the vendor's form drawn from a fixed seed: the gateway would refuse the turns that call tools. Such a pattern is to be anchored, on word boundaries for example. The board tools' names now live in the registry, which the MCP gateway reads them from.
   - **Records.** The record of a forwarded request holds it redacted, its thinking blocks too, though they reach the vendor as the gateway gave them; its digest is of the record, so that it gives back nothing the record hides.

   (`TestRedactionChangesWhatTheModelReadsOnly`, `TestSecretsNeverReachTheVendor`, `TestAnIdOrANameThatHoldsASecretIsRefused`, `TestAPatternMayNotMatchAReplacement`, `TestAReplacementHoldsNoQuoteOrBackslash`, `TestARuleThatAppliesAlwaysSparesToolNamesAndIDs`, `TestAThinkingBlockIsRecordedRedacted`)
6. **Costs as the vendor charges them.**
   - **Cards.** Each model the agents use has a card in the gateway's configuration, copied from the vendor's list. A card holds rates per token, in pico-USD (10^-12 USD), for input, output, five-minute and one-hour cache writes, and cache reads. It holds base rates, and long rates for a call whose prompt (input, cache writes and reads) is over the card's threshold, as Haiku 5.5's is above 100,000 tokens. A model without a card is refused.
   - **Spend.** A call's usage at its card's rates, in pico-USD.
   - **Cost-weighted tokens.** Spend at the price of one reference token, rounded up. Budgets and the daily cap count these, so they bound money whichever model an agent uses: models' prices differ a hundredfold.
   - **Cache writes.** The vendor's breakdown is priced at its rates; anything beyond it, or all of it without one, at the one-hour rate.
   - **Bounds.** Each of a card's five rates is above zero and at most $1,000 per million tokens, and an answer reports at most 100,000,000 tokens of each kind, so that no call's spend overflows.

   (`TestACallCostsWhatTheVendorCharges`, `TestACardWithoutAThresholdHasOneRate`, `TestWeightedTokensAreSpendAtTheReferencePrice`, `TestCardsMustPrice`, `TestTheDearestCallFits`, `TestAnAnswerWithoutUsageIsAnError`, `TestAnAnswerGivesItsTokensRecordAndThinking`, `TestCacheWritesNeverCountBelowTheTotal`, `TestAModelWithoutACardIsRefused`, `TestACallIsRecordedBeforeItIsForwardedThenItsOutcome`)
7. **Budgets and the daily cap.**
   - **The run budget.** A call starts only while the run has spent less than the session's budget, which the coordinator sets (ADR-007). A call counts what its outcome says it cost once that is recorded, and its worst case (Decision 8) until then: while it is in flight, and for good if its outcome never is. A session has one request in the gateway's hands at a time [1], and at most two calls at the vendor, one of them a call that outlived its request (Decision 8). So a run passes its budget only by the calls its agents start while it is under it, one per agent at most. A worst-case reservation for the call about to start was rejected: it would leave much of a budget unusable on long, cached contexts. A call in flight counts at a median of eleven times what it ends up costing (recorded traffic), so a run near its budget, or the day near the cut, may be refused a call while others are in flight, and admitted once they end.
   - **The daily cap.** The deployment's cap, in cost-weighted tokens, counts the tenant's calls that began since 00:00 UTC, the same way. From [80%] of it, comparison and probation runs' calls are refused; at 100%, every call (spec Section 7). Each refusal is recorded with its reason, which is the cut's record.
   - **Fail closed.** A session with no budget is refused.

   (`TestACallStartsWhileBudgetAndCapAllowIt`, `TestTheDayTurnsAtMidnightUTC`, `TestARunsBudgetStopsItsCalls`, `TestShadowRunsAreCutFirst`, `TestASessionMakesOneCallAtATime`, `TestACallCountsItsWorstCaseUntilItsOutcomeIsRecorded`, `TestSpendIsCountedPerRunAndSinceATime`)
8. **No call the vendor charged for counts as nothing.**
   - **Calls go on past their request.** A vendor call runs apart from the request that made it, for at most [10 min]. The request is answered when the vendor answers, or at once when the round ends, the run stops or the client goes; the call goes on, and its outcome, with what it cost, is recorded when the vendor answers.
   - **The session's place.** A call that goes on holds its session's place for at most [15 s]: the session's next request, typically the next round's first, waits for it that long, then goes on beside it, once the vendor has answered some call since that call began, so that a vendor that answers no one gets no second call to hang; while two go on, it waits until one ends. So the vendor never has more than two of a session's calls at once, and the call that goes on counts at its worst case until it ends. A wait ends with the request, its round or its run; a request sent while another waits is refused. A request that ended before its call could start, or while its call was being recorded, is not sent.
   - **Not sent, or lost.** The vendor adapter tells an attempt whose request never left (retried) from one whose answer was lost after the request was written (never retried).
   - **Unknown.** A lost answer, or an answer without readable token counts, is recorded `unknown` and counted at its worst case: its request at one token a byte, with [1,024] more for what the vendor adds of its own, all written to the cache, and its whole `max_tokens` of output.
   - **Outcomes not recorded.** An outcome that cannot be recorded is tried again after each record timeout [5 s], for up to [10 min], and its answer is withheld (Decision 10). Until it is recorded, the call counts at its worst case.
   - **Stopping.** As the gateway stops, `Drain` stops it taking requests, and waits for those in its hands, the calls they made, and their outcomes. A call it leaves counts at its worst case for good; Plan 7 closes such calls when the next instance starts.

   (`TestACallGoesOnPastTheRoundUntilItsCostIsKnown`, `TestARequestIsAnsweredWhenTheRunStops`, `TestTheNextRoundWaitsForTheCallTheLastRoundLeft`, `TestANextCallGoesOnBesideACallThatHangs`, `TestANextCallWaitsWhileTheVendorAnswersNoOne`, `TestASessionHasAtMostTwoCallsAtTheVendor`, `TestARequestSentAgainWaitsForTheCallItRepeats`, `TestARequestThatEndedIsNotSent`, `TestARequestThatEndsWhileItsCallIsRecordedIsNotSent`, `TestANotMadeOutcomeIsTriedAgain`, `TestDrainWaitsForCallsThatOutliveTheirRequests`, `TestDrainWaitsForRequestsInHandAndRefusesNewOnes`, `TestARequestThatNeverLeftIsTriedAgain`, `TestALostAnswerCountsItsWorstCase`, `TestAnAnswerWithoutUsageCountsItsWorstCase`, `TestAnOutcomeThatCannotBeRecordedIsTriedAgain` and `TestFailuresToReachTheVendor`)
9. **Failures, retries and vendor pressure** (spec Sections 8.2 and 12.1).
   - **Retries.** The gateway retries a request that never left, and the vendor's 408, 429, 500, 502, 503, 504 and 529. It waits [1 s, 2 s, 4 s] with up to a fifth more at random, or the vendor's `retry-after`, read up to an hour, when that is longer. It starts no wait the round cannot hold, and ends a wait when the run stops or the call's [10 min] are up. That call timeout is longer than any call the agents' `max_tokens` allows on their slowest model, so a call it cuts means a vendor that hung, and counts as a vendor failure.
   - **Vendor pressure.** The vendor is one server of the MCP gateway's limits, serving agents only, so that no slot is kept for platform reads: [10] calls at once, at most half of them comparison and probation runs', degraded after [3] failures in a row for [60 s]. While it is degraded, primary runs' calls fail at once, and comparison and probation runs' calls pause until the cool-down ends or their round does.
   - **The vendor's refusal.** It is answered as the vendor sent it, with `x-should-retry: false`, and costs nothing. A request the vendor finds invalid or too large (400, 413) counts against the session's round, as the gateway's own refusals do (Decision 10).

   (`TestTransientFailuresAreRetriedWithinTheRound`, `TestNoAttemptOutlastsTheRound`, `TestNoWaitOutlastsTheCallsTimeout`, `TestTheWaitTheVendorAsksForIsRead`, `TestComparisonRunsPauseFirstWhenTheVendorFails`, `TestAPausedCallEndsWithItsRound`, `TestTheVendorTakesAsManyCallsAtOnceAsItHasSlots`, `TestAServerOfAgentsOnlyReservesNothing`, `TestTheVendorsRefusalIsAnsweredAsSent`, `TestTheVendorsRefusalsCountAgainstTheSession`)
10. **Records.**
   - **Three chained tables.**
     - `model_calls`: the run, agent, round, class, model, the request's size, the SHA-256 of its record, and the call's worst case in cost-weighted tokens;
     - `model_call_contents`: the request as it was forwarded and the completion, both redacted, thinking blocks included;
     - `model_call_outcomes`: the status (`ok`, `refused`, `error` or `unknown`), the reason, the tokens, the spend and the cost-weighted tokens, the attempts, and how the vendor served the call.
   - **Before the vendor.** A call and its request are committed once the call has its slot at the vendor, and before the vendor is called; with the audit store down, nothing is forwarded.
   - **Contents apart.** Prompts and completions are kept [30 days], tokens and spend longer, for the cost metrics of a measurement window (spec Sections 9.4 and 13.6). So the contents sit in their own table, which the retention job deletes from.
   - **Requests not forwarded.** Every request a live session sends is recorded, forwarded or not: the gateway reads and writes the record apart from the request, so a request whose client went is recorded and counted as any other. One the gateway does not forward (refused, dropped because it ended before its call could start, or sent while the vendor is failing) keeps at most its first 4 KiB, redacted, with its size, the size it declared when it was too large to read, and the digest of its redacted JSON in full. A call recorded and then not made, because its request ended while it was recorded, counts as well, and keeps its request in full. A session's round records [10] of them, the vendor's 400s and 413s counted among them; then the session is shut out until the next round, with one more recorded. Each is counted before it is recorded, so the cap holds for requests sent at once. So a sandbox cannot fill the audit store, or hold the chain's lock, with requests that cost nothing.
   - **Withheld answers.** An answer that cannot be recorded is not returned.

   (`TestAModelCallIsRecordedThenItsOutcome`, `TestSpendIsCountedPerRunAndSinceATime`, `TestModelCallRecordsNeverChange`, `TestModelCallsAreChained`, `TestARefusedRequestIsRecordedShort`, `TestARequestNotForwardedIsRecordedShort`, `TestARequestTooLargeIsRecordedAtItsSize`, `TestASessionRefusedTooOftenIsShutOutForTheRound`, `TestRefusalsAreCappedWhenSentAtOnce`, `TestRequestsDroppedAtOnceCountAgainstTheSession`, `TestRequestsDroppedWhileRecordedCountAgainstTheSession`, `TestNothingIsForwardedWhileTheAuditStoreIsDown`, `TestAnAnswerThatCannotBeRecordedIsWithheld`)
11. **Answers to the runtime** are in the Messages API's error form, so its client reads them:
    - 401 `authentication_error` for a token of no live session, not recorded, since nothing names a run;
    - 404 `not_found_error` for another path, method or query;
    - 413 `request_too_large`;
    - 400 `invalid_request_error` for a request outside the allowlist;
    - 403 `permission_error` for a model without a card, the budget, the daily cap, the cut, or a round that ended;
    - 429 `rate_limit_error` for a second call in flight, a request sent while another waits, or a session shut out;
    - 502 and 504 for a vendor not reached, an answer lost, or a request whose round ended, run stopped or client went first;
    - 503 for the audit store or the vendor failing, or the gateway stopping.

    Every answer carries `x-should-retry: false`, so the client does not repeat it, but the 503s of a store that could not be read or written, or of the gateway stopping, before anything was forwarded, which it may. The 503 of a vendor that is failing carries `false`: it counts against the session's round, as a refusal does.

    (`TestEveryRefusalIsRecorded`, `TestATokenOfNoLiveSessionIsRefused`, `TestACallAfterTheRoundEndedIsRefused`, `TestTheSDKDoesNotRepeatARefusal`)
12. **Sessions.** A token names a session: its run, agent, the item's revision, round, deadline, class, budget, and a channel closed when the run stops. The coordinator issues tokens, one per agent session, scoped to its run and agent (Plan 7). The endpoint reads the token from `x-api-key` or `Authorization: Bearer`, the client's two ways of sending a key. (`TestTheSDKCallsTheVendorThroughTheGateway`, `TestTheTokenMayBeABearerToken`)

## Assumptions taken for the team (ADR-016)

| Onboarding question | What this plan assumes | What a different answer costs |
|---|---|---|
| 6: production logs sent to the model vendor | They may go, redacted, to one vendor behind the LLM gateway (ADR-007), and be kept, redacted, in the audit store for [30 days] | If they may not be kept, `model_call_contents` stays empty and a call keeps only its size and digest: a change to `ModelCalls`. A second vendor is a new adapter of `Vendor` and new cards, and a new `prompt` allowlist if its request format differs (ADR-007, "Reversibility") |

**Not an onboarding question.** The gateway runs as one active instance, under the gateway lease, beside the MCP gateway (spec Section 8.2). Two instances at once would each allow a session one call in flight, and each keep their own thinking blocks and refusal counts; Plan 7's lease prevents it.

## Review Focus

These are the inputs most likely to bite a person using this software. Each has a test in the task that owns the code.

1. **The platform's own runtime, on the platform's own model.** A whole turn goes through: the answer echoed back, its thinking block and its tool call's caller included, passes the allowlist, and what a tool returned reaches the vendor redacted. (`TestARunnerTurnPassesTheAllowlist` in Task 7; `TestAThinkingBlockGoesBackOnlyFromTheSessionThatGotIt` in Task 4)
2. **A runtime that tries the vendor's own features**: a typed tool, a document by URL, a beta header, another workspace, another endpoint or model, a second call at once, a flood of refused requests. Each is refused and recorded, and none reaches the vendor. (`TestEveryRefusalIsRecorded` and `TestASessionRefusedTooOftenIsShutOutForTheRound` in Task 4; `TestToolsTheVendorRunsAreRefused` and `TestHeaders` in Task 1)
3. **A call the vendor charged for whose answer never arrives**: the round ends, the run stops, the client's connection drops and it sends the request again, the connection to the vendor breaks after the request was sent, or the answer cannot be read. Its cost is recorded, at most its worst case, never nothing, and counted at its worst case until then; the session's next call waits for it a while, then goes on beside it. (`TestACallGoesOnPastTheRoundUntilItsCostIsKnown`, `TestTheNextRoundWaitsForTheCallTheLastRoundLeft`, `TestANextCallGoesOnBesideACallThatHangs`, `TestACallCountsItsWorstCaseUntilItsOutcomeIsRecorded` and `TestALostAnswerCountsItsWorstCase` in Task 4; `TestFailuresToReachTheVendor` in Task 5; `TestARequestSentAgainWaitsForTheCallItRepeats` in Task 7)
4. **Long prompts and dear models.** A call over the card's threshold is priced at the long rates, and a budget in cost-weighted tokens bounds money on any model. (`TestACallCostsWhatTheVendorCharges` in Task 2; `TestShadowRunsAreCutFirst` in Task 4)
5. **Secrets in anything the model reads**: text, a tool's result, a tool call's input, a schema, a description, an id or a name, the session's own token. None reaches the vendor or the record. (`TestRedactionChangesWhatTheModelReadsOnly` and `TestAnIdOrANameThatHoldsASecretIsRefused` in Task 1; `TestSecretsNeverReachTheVendor` and `TestEveryRefusalIsRecorded` in Task 4)

## Not in this plan

- **Plan 7:** issuing session tokens, the coordinator's sessions, `serve`, and the notices of refused and failed model calls, derived from the records.
- **Plan 8:** cost per incident, per hit and the shadow share, from the records.
- **Plan 10:** deleting prompts and completions after [30 days], and the load trial of the chain lock with model-call rows.
- Streaming, images, documents, extended thinking set by the client, beta features and a second vendor.

## Constraints carried to later plans

- **Plan 7:**
  - Wire the gateway in `serve`, behind the gateway lease (spec Section 8.2), with the vendor's key as a secret reference, a card for every model the agents use, the parameters of spec Section 15, and vendor slots for two calls per session at most.
  - Issue each agent session its LLM gateway token, distinct from its MCP gateway token, scoped to its run and agent. Give tokens a fixed prefix and add a built-in rule that applies always to it, so that no token reaches a store or the vendor.
  - Set `Session.Budget` from the run's budget, live, so that the wrap-up round can be granted an allowance (spec Section 5.6).
  - Stop a run whose budget is spent as `budget_exhausted`, and a run cut at the daily cap as cut, from `ModelCalls.Spent` and `SpentSince`, which count a call in flight at its worst case, never as `quiet`; staff no new item once the daily cap is reached (spec Sections 5.6, 7 and 8.2).
  - Post a notice of each refused, cut or failed model call, from the records, as the MCP gateway's calls get theirs (spec Section 8.1).
  - Give the runtime the gateway as its base URL and the token as its key, and end its turn on any 4xx or 5xx the gateway answers.
  - Make the runtime's client attempt each request once (`option.WithMaxRetries(0)`), with no timeout shorter than the round: the gateway retries what may be retried, and a request the client sends again goes on beside the first call, or after it, and is paid for twice.
  - A runtime that carries context into a later round drops the thinking blocks of the earlier rounds' answers, which the vendor does not need back: the gateway's memory of them does not outlive a restart.
  - Re-derive the allowlist from recorded traffic whenever the runtime's SDK or the agents' models change: a new block or field the runtime echoes is refused until the allowlist names it.
  - On shutdown, call `Gateway.Drain`, which stops the gateway taking requests and waits for those in hand and their calls. When an instance starts under the lease, record an outcome for each model call left without one, `unknown` at its estimate, with the reason that the gateway stopped first.
  - Set the agents' `max_tokens` so that the longest answer fits in a round and in the call timeout on their slowest model, and keep the call timeout below what a hung call may cost a run's rounds.
  - Decide a run's stop for its budget, and the cut, knowing that `Spent` counts a call in flight at its worst case: read it once the run has no call in flight, or accept that they come early.
- **Plan 8:** take cost per incident, per hit and the shadow share from the model-call records, joined to runs for their arm: each call at its outcome's cost, or at its estimate when it has none, as `ModelCalls.Spent` counts it.
- **Plan 9:** apply the release rule of shadow and invisible-arm runs to model-call records, which hold agent output (spec Section 7).
- **Plan 10:**
  - delete `model_call_contents` after [30 days] with chained prune records, and keep the other two tables with the cost records;
  - include model-call rows, which can be hundreds of kilobytes, in the load trial of the chain lock and of verification;
  - note in the threat model that the vendor's prompt cache is shared within the organization: a run's cache reads can show that another run sent a guessed prefix.

## File Structure

| File | Responsibility |
|---|---|
| `internal/llm/prompt/prompt.go` | New: `Check`, the allowlist and the strict reader; `Request` with `Redact`, `Pin` and `Encode`; `ThinkingHash`; `Redacted` |
| `internal/llm/cost/cost.go` | New: `Usage`, `Card` and `Rates`, `Weighted`, `Estimate`; `Class`, `Limits`, `Admit`, `DayStart` |
| `internal/llm/completion/completion.go` | New: `Read`, an answer's tokens, record, thinking blocks and details; `Failure` |
| `internal/registry/config/redaction.go` | Modified: a pattern of a rule that applies always may not change a replacement, or match a tool's name or a tool call's id; a replacement holds no quote; `SessionTokenReplacement` |
| `internal/registry/config/names.go` | Modified: `BoardTools` |
| `internal/actions/app/board.go` | Modified: `isBoardTool` reads `config.BoardTools` |
| `internal/actions/limits/limits.go` | Modified: a server of agents only keeps no slots for platform reads |
| `internal/llm/app/ports.go` | New: the ports `Sessions`, `Revisions`, `Vendor`, `Calls` and their types |
| `internal/llm/app/gateway.go` | New: `Gateway.Messages` |
| `internal/llm/memory/memory.go` | New: sessions and calls in memory |
| `internal/llm/anthropic/anthropic.go` | New: the vendor's Messages API |
| `internal/db/migrations/0006_model_calls.sql` | New: `model_calls`, `model_call_contents`, `model_call_outcomes`, their guards and their chain triggers |
| `internal/audit/postgres/models.go` | New: `ModelCalls`, the record of model calls and their spend |
| `internal/audit/postgres/chain.go` | Modified: verification reads the three tables |
| `internal/audit/postgres/calls.go` | Modified: the package's comment |
| `internal/llm/endpoint/endpoint.go` | New: the agent-facing endpoint |

## Tasks

The tasks run in order. Tasks 1 to 3 are the rules; Task 4 the gateway; Tasks 5 to 7 its adapters; Task 8 documents them.

### Task 1: Reading and checking a request

Decisions 2 to 5, for a request: what passes, how it is read, what is redacted and how it is written.

**Files:**
- Create: `internal/llm/prompt/prompt.go`, `internal/llm/prompt/prompt_test.go`

**Interfaces:**
- Consumes: `redact.Redactor` (Plans 5a and 6a).
- Produces:
  - `type Policy struct { Model string; MaxTokens int64; Version string; Answered func(hash [32]byte) bool }`;
  - `type Refusal struct { Reason string }`, an error;
  - `func Check(header map[string][]string, body []byte, p *Policy) (*Request, error)`;
  - `func (r *Request) Model() string`, `MaxTokens() int64`, `Redact(red *redact.Redactor) error`, `Pin(serviceTier, inferenceGeo string)`, `Encode() []byte`;
  - `func ThinkingHash(block map[string]any) ([32]byte, bool)`;
  - `func Redacted(body []byte, red *redact.Redactor) string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/llm/prompt/prompt_test.go`:

```go
package prompt_test

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/llm/prompt"
)

const model = "claude-haiku-5-5"

// thinking is a thinking block as the model's answers hold one: no text,
// and a signature.
const thinking = `{"type": "thinking", "thinking": "", "signature": "EqQBCkgIBRABGAIiQL"}`

// answered holds the hashes of the thinking blocks the gateway gave the
// session.
func answered(t testing.TB, blocks ...string) func([32]byte) bool {
	t.Helper()
	known := map[[32]byte]bool{}
	for _, b := range blocks {
		var block map[string]any
		require.NoError(t, json.Unmarshal([]byte(b), &block))
		h, ok := prompt.ThinkingHash(block)
		require.True(t, ok, b)
		known[h] = true
	}
	return func(h [32]byte) bool { return known[h] }
}

func policy(t testing.TB) *prompt.Policy {
	t.Helper()
	return &prompt.Policy{Model: model, MaxTokens: 16384, Version: "2023-06-01", Answered: answered(t, thinking)}
}

// headers are what anthropic-sdk-go sends with a request.
func headers() map[string][]string {
	return map[string][]string{
		"Anthropic-Version": {"2023-06-01"}, "Content-Type": {"application/json"}, "X-Api-Key": {"session-token"},
		"X-Stainless-Os": {"Linux"}, "X-Stainless-Runtime-Version": {"go1.26.0"}, "User-Agent": {"Anthropic/Go 1.80.0"},
	}
}

// turn is a request as the runner sends one mid-turn: a cached system
// prompt, a tool, and an answer echoed back, its thinking block and the
// caller of its tool call included, with the tool's result.
const turn = `{
 "model": "claude-haiku-5-5",
 "max_tokens": 4096,
 "system": [{"type": "text", "text": "You investigate incidents.", "cache_control": {"type": "ephemeral"}}],
 "tools": [{"name": "metrics__query", "description": "Query metrics.", "input_schema": {"type": "object", "properties": {"q": {"type": "string"}}}}],
 "messages": [
  {"role": "user", "content": "Find why checkout fails."},
  {"role": "assistant", "content": [` + thinking + `, {"type": "text", "text": "Querying."},
   {"type": "tool_use", "id": "toolu_1", "name": "metrics__query", "input": {"q": "errors"}, "caller": {"type": "direct"}}]},
  {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "text", "text": "errors rose at 10:02"}], "cache_control": {"type": "ephemeral", "ttl": "5m"}}]}
 ]
}`

func check(t *testing.T, body string) (*prompt.Request, error) {
	t.Helper()
	return prompt.Check(headers(), []byte(body), policy(t))
}

// set returns turn with the value at a top-level key replaced, or removed
// when v is nil.
func set(t *testing.T, key string, v any) string {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(turn), &doc))
	if v == nil {
		delete(doc, key)
	} else {
		doc[key] = v
	}
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	return string(b)
}

func raw(t *testing.T, s string) any {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal([]byte(s), &v))
	return v
}

// assistantSays returns turn whose answer holds block instead of its text.
func assistantSays(block string) string {
	return strings.Replace(turn, `{"type": "text", "text": "Querying."}`, block, 1)
}

// A request as the runner sends one passes, and its encoding is the
// gateway's own: checking it again gives the same bytes.
func TestARequestTheRuntimeSendsPasses(t *testing.T) {
	req, err := check(t, turn)
	require.NoError(t, err)

	again, err := check(t, string(req.Encode()))

	require.NoError(t, err)
	assert.Equal(t, string(req.Encode()), string(again.Encode()))
	assert.Equal(t, model, req.Model())
	assert.Equal(t, int64(4096), req.MaxTokens())
	assert.JSONEq(t, turn, string(req.Encode()))
}

// A thinking block comes back only as the gateway gave it to the session:
// one it never gave, or one changed, is refused, so that no text the
// gateway did not redact reaches the vendor in one.
func TestThinkingBlocksComeBackOnlyAsTheGatewayGaveThem(t *testing.T) {
	for name, block := range map[string]string{
		"never given":    `{"type": "thinking", "thinking": "my key is sk-aaaaaaaa", "signature": "EqQBCkgIBRABGAIiQL"}`,
		"signature":      `{"type": "thinking", "thinking": "", "signature": "forged"}`,
		"an extra field": `{"type": "thinking", "thinking": "", "signature": "EqQBCkgIBRABGAIiQL", "note": "x"}`,
		"redacted":       `{"type": "redacted_thinking", "data": "EmwKAhgB"}`,
	} {
		_, err := check(t, assistantSays(block))

		require.Error(t, err, name)
	}
	p := policy(t)
	p.Answered = answered(t, thinking, `{"type": "redacted_thinking", "data": "EmwKAhgB"}`)
	_, err := prompt.Check(headers(), []byte(assistantSays(`{"type": "redacted_thinking", "data": "EmwKAhgB"}`)), p)
	require.NoError(t, err)
}

// Tools the vendor would run are refused: a tool with any type is one.
func TestToolsTheVendorRunsAreRefused(t *testing.T) {
	for _, typ := range []string{"web_search_20250305", "web_fetch_20250910", "code_execution_20250825", "bash_20250124",
		"text_editor_20250728", "computer_20250124", "memory_20250818", "tool_search_tool_regex_20251119", "mcp_toolset", "custom"} {
		tools := raw(t, `[{"type": "`+typ+`", "name": "x", "description": "d", "input_schema": {"type": "object"}}]`)

		_, err := check(t, set(t, "tools", tools))

		require.ErrorContains(t, err, "type", typ)
	}
}

// No block holds a source, inline or fetched, and no block the vendor
// writes for itself passes.
func TestBlocksOtherThanTextToolsAndThinkingAreRefused(t *testing.T) {
	for _, block := range []string{
		`{"type": "image", "source": {"type": "url", "url": "https://example.com/a.png"}}`,
		`{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "AAAA"}}`,
		`{"type": "document", "source": {"type": "url", "url": "https://example.com/a.pdf"}}`,
		`{"type": "document", "source": {"type": "file", "file_id": "file_1"}}`,
		`{"type": "search_result", "source": "https://example.com", "title": "t", "content": []}`,
		`{"type": "container_upload", "file_id": "file_1"}`,
		`{"type": "server_tool_use", "id": "srvtoolu_1", "name": "web_search", "input": {}}`,
		`{"type": "mcp_tool_use", "id": "mcptoolu_1", "name": "x", "server_name": "s", "input": {}}`,
		`{"type": "text", "text": "x", "citations": [{"type": "web_search_result_location", "url": "https://example.com"}]}`,
		`{"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "image", "source": {"type": "url", "url": "https://example.com/a.png"}}]}`,
	} {
		messages := raw(t, `[{"role": "user", "content": [`+block+`]}]`)

		_, err := check(t, set(t, "messages", messages))

		require.Error(t, err, block)
	}
}

// A tool call's caller is the model itself, never code the vendor runs.
func TestTheCallerIsTheModelItself(t *testing.T) {
	for _, caller := range []string{`{"type": "code_execution_20250825"}`, `{"type": "direct", "tool_id": "x"}`, `"direct"`} {
		body := strings.Replace(turn, `"caller": {"type": "direct"}`, `"caller": `+caller, 1)

		_, err := check(t, body)

		require.Error(t, err, caller)
	}
}

// Ids and names are letters, digits, _ and -, never a channel for text.
func TestIdsAndNamesAreNotText(t *testing.T) {
	for name, body := range map[string]string{
		"tool use id":   strings.Replace(turn, `"id": "toolu_1"`, `"id": "toolu 1 my key is sk"`, 1),
		"tool use name": strings.Replace(turn, `"name": "metrics__query", "input"`, `"name": "metrics/query", "input"`, 1),
		"result id":     strings.Replace(turn, `"tool_use_id": "toolu_1"`, `"tool_use_id": "`+strings.Repeat("x", 129)+`"`, 1),
		"tool name":     strings.Replace(turn, `"name": "metrics__query", "description"`, `"name": "metrics query", "description"`, 1),
	} {
		_, err := check(t, body)

		require.Error(t, err, name)
	}
}

// Fields outside the list are refused, whatever their value: the runtime
// sends model, messages, system, max_tokens and tools.
func TestFieldsOutsideTheListAreRefused(t *testing.T) {
	for key, v := range map[string]any{
		"metadata": raw(t, `{"user_id": "x"}`), "thinking": raw(t, `{"type": "enabled", "budget_tokens": 1024}`),
		"container": "c", "mcp_servers": raw(t, `[]`), "service_tier": "auto", "context_management": raw(t, `{}`),
		"stream": false, "Model": model, "inference_geo": "us", "output_config": raw(t, `{}`), "temperature": 0.5,
		"top_p": 0.9, "top_k": 5, "stop_sequences": raw(t, `["sk-aaaaaaaa"]`), "tool_choice": raw(t, `{"type": "auto"}`),
	} {
		_, err := check(t, set(t, key, v))

		require.Error(t, err, key)
	}
}

// What two JSON readers could read in two ways is refused: a key twice in
// one object, a key in another case, data after the request, invalid
// UTF-8, nesting too deep to read safely.
func TestAmbiguousJSONIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"key twice":          `{"model": "claude-haiku-5-5", "model": "claude-opus-5-5", "max_tokens": 1, "messages": []}`,
		"key twice in input": strings.Replace(turn, `"input": {"q": "errors"}`, `"input": {"q": "errors", "q": "logs"}`, 1),
		"key in other case":  strings.Replace(turn, `"tools"`, `"Tools"`, 1),
		"block key in case":  strings.Replace(turn, `{"type": "text", "text": "Querying."}`, `{"type": "text", "Text": "Querying."}`, 1),
		"data after":         turn + `{}`,
		"invalid UTF-8":      strings.Replace(turn, "Querying.", "Query\xffing.", 1),
		"not an object":      `[` + turn + `]`,
		"too deep":           strings.Replace(turn, `{"q": "errors"}`, `{"q": `+strings.Repeat(`[`, 200)+strings.Repeat(`]`, 200)+`}`, 1),
	} {
		_, err := check(t, body)

		require.Error(t, err, name)
	}
	_, err := check(t, strings.Replace(turn, `{"q": "errors"}`, `{"q": `+strings.Repeat(`[`, 50)+strings.Repeat(`]`, 50)+`}`, 1))
	require.NoError(t, err, "fifty levels are deep enough for any tool's input")
}

// The model is the agent's own, exactly.
func TestTheModelIsTheAgents(t *testing.T) {
	for _, m := range []any{"claude-opus-5-5", "Claude-Haiku-5-5", "claude-haiku-5-5 ", 5, nil} {
		_, err := check(t, set(t, "model", m))

		require.Error(t, err, m)
	}
}

// max_tokens is a whole number from 1 to the cap, written as one.
func TestMaxTokensIsAWholeNumberWithinTheCap(t *testing.T) {
	for _, v := range []string{`0`, `-1`, `1.5`, `1e3`, `16385`, `"100"`, `null`} {
		_, err := check(t, set(t, "max_tokens", json.RawMessage(v)))

		require.Error(t, err, v)
	}
	_, err := check(t, set(t, "max_tokens", nil))
	require.Error(t, err, "max_tokens is required")
	_, err = check(t, set(t, "max_tokens", json.RawMessage(`16384`)))
	require.NoError(t, err)
}

// A cache entry lives five minutes, as the runtime's do.
func TestCacheControlIsFiveMinutesOnly(t *testing.T) {
	for _, cc := range []string{`{"type": "ephemeral", "ttl": "1h"}`, `{"type": "persistent"}`, `{"type": "ephemeral", "scope": "x"}`} {
		system := raw(t, `[{"type": "text", "text": "s", "cache_control": `+cc+`}]`)

		_, err := check(t, set(t, "system", system))

		require.Error(t, err, cc)
	}
}

// The client's anthropic-version must be the gateway's, no beta header
// passes, and a body is sent as it is; every other header is the
// gateway's to drop.
func TestHeaders(t *testing.T) {
	for name, header := range map[string][2]string{
		"version":  {"Anthropic-Version", "2024-01-01"},
		"beta":     {"Anthropic-Beta", "context-1m-2025-08-07"},
		"encoding": {"Content-Encoding", "gzip"},
	} {
		h := headers()
		h[header[0]] = []string{header[1]}

		_, err := prompt.Check(h, []byte(turn), policy(t))

		require.ErrorContains(t, err, header[1], name)
	}
	h := headers()
	h["Anthropic-Workspace-Id"] = []string{"wrkspc_other"}
	_, err := prompt.Check(h, []byte(turn), policy(t))
	require.NoError(t, err, "the gateway drops it; it never reaches the vendor")
}

// Redaction changes everything the model reads, each part on its own, and
// nothing that makes the request work: ids, names, types, cache control,
// numbers and thinking blocks stay as written.
func TestRedactionChangesWhatTheModelReadsOnly(t *testing.T) {
	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
	thought := `{"type": "thinking", "thinking": "the key sk-iiiiiiii", "signature": "EqQBCkgIBRABGAIiQL"}`
	body := strings.NewReplacer(
		"You investigate incidents.", "Use sk-aaaaaaaa.",
		"Find why checkout fails.", "Key sk-bbbbbbbb leaked?",
		`"input": {"q": "errors"}`, `"input": {"q": "sk-cccccccc", "sk-dddddddd": 1}`,
		"errors rose at 10:02", "token sk-eeeeeeee",
		`"description": "Query metrics."`, `"description": "Query sk-ffffffff."`,
		`"properties": {"q": {"type": "string"}}`, `"properties": {"q": {"type": "string", "default": "sk-gggggggg"}}`,
		thinking, thought,
	).Replace(turn)
	p := policy(t)
	p.Answered = answered(t, thought)
	req, err := prompt.Check(headers(), []byte(body), p)
	require.NoError(t, err)

	require.NoError(t, req.Redact(red))
	got := string(req.Encode())

	for _, secret := range []string{"sk-aaaaaaaa", "sk-bbbbbbbb", "sk-cccccccc", "sk-dddddddd", "sk-eeeeeeee", "sk-ffffffff", "sk-gggggggg"} {
		assert.NotContains(t, got, secret)
	}
	assert.Contains(t, got, `"thinking":"the key sk-iiiiiiii"`, "a thinking block goes back as the gateway gave it")
	assert.Contains(t, got, `"id":"toolu_1"`)
	assert.Contains(t, got, `"name":"metrics__query"`)
	assert.Contains(t, got, `"tool_use_id":"toolu_1"`)
	assert.Contains(t, got, `"cache_control":{"ttl":"5m","type":"ephemeral"}`)
	assert.Contains(t, got, `"max_tokens":4096`)
}

// An id or a name cannot be redacted without breaking the conversation, so
// a request whose id or name holds what a rule hides, the session's token
// among them, is refused, and the refusal does not repeat it.
func TestAnIdOrANameThatHoldsASecretIsRefused(t *testing.T) {
	red := redact.New(redact.Rule{Literals: []string{"session-token-123456"}, Replacement: "[redacted:session-token]"},
		redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
	for name, body := range map[string]string{
		"a tool's name":      strings.Replace(turn, `"tools": [{"name": "metrics__query"`, `"tools": [{"name": "sk-abcdefgh"`, 1),
		"a tool call's id":   strings.Replace(turn, `"id": "toolu_1"`, `"id": "session-token-123456"`, 1),
		"a tool call's name": strings.Replace(turn, `"id": "toolu_1", "name": "metrics__query"`, `"id": "toolu_1", "name": "sk-abcdefgh"`, 1),
		"a result's id":      strings.Replace(turn, `"tool_use_id": "toolu_1"`, `"tool_use_id": "sk-abcdefgh"`, 1),
	} {
		req, err := check(t, body)
		require.NoError(t, err, name)

		err = req.Redact(red)

		var refusal *prompt.Refusal
		require.ErrorAs(t, err, &refusal, name)
		assert.Contains(t, err.Error(), "redaction rule", name)
		assert.NotContains(t, err.Error(), "sk-abcdefgh", name)
		assert.NotContains(t, err.Error(), "session-token-123456", name)
	}
}

// The gateway pins the service tier, and the inference region it is given,
// so that every call is billed as its card prices it.
func TestTheGatewayPinsHowACallIsBilled(t *testing.T) {
	req, err := check(t, turn)
	require.NoError(t, err)

	req.Pin("standard_only", "global")

	assert.Contains(t, string(req.Encode()), `"inference_geo":"global"`)
	assert.Contains(t, string(req.Encode()), `"service_tier":"standard_only"`)
}

// A refused request is recorded with every string, key and number redacted,
// or not at all when it cannot be read.
func TestARefusedRequestIsRecordedRedacted(t *testing.T) {
	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})

	got := prompt.Redacted([]byte(`{"metadata": {"sk-aaaaaaaa": "sk-bbbbbbbb"}, "x": [1, "sk-cccccccc"]}`), red)

	assert.JSONEq(t, `{"metadata": {"[redacted:keys]": "[redacted:keys]"}, "x": [1, "[redacted:keys]"]}`, got)
	assert.Empty(t, prompt.Redacted([]byte(`{"a": 1`), red))
	assert.Empty(t, prompt.Redacted([]byte("\xff"), red))
}

// Checking any input never fails, what passes encodes to bytes that pass
// again unchanged, and what passes, redacted, passes again.
func FuzzCheckNeverFails(f *testing.F) {
	f.Add([]byte(turn))
	f.Add([]byte(`{"model": "claude-haiku-5-5", "max_tokens": 1, "messages": [{"role": "user", "content": "hi"}]}`))
	f.Add([]byte(`{"a": [[[[{}]]]]}`))
	p := policy(f)
	red := redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})
	f.Fuzz(func(t *testing.T, body []byte) {
		req, err := prompt.Check(headers(), body, p)
		if err != nil {
			return
		}
		again, err := prompt.Check(headers(), req.Encode(), p)
		if err != nil {
			t.Fatalf("the encoding of an accepted request is refused: %v", err)
		}
		if !bytes.Equal(again.Encode(), req.Encode()) {
			t.Fatalf("the encoding is not stable")
		}
		if req.Redact(red) != nil {
			return
		}
		if _, err := prompt.Check(headers(), req.Encode(), p); err != nil {
			t.Fatalf("a redacted request is refused: %v", err)
		}
	})
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/llm/prompt/`
Expected: FAIL: `no non-test Go files in .../internal/llm/prompt`

- [ ] **Step 3: Read, check, redact and encode a request**

Create `internal/llm/prompt/prompt.go`:

```go
// Package prompt reads, checks, redacts and encodes a Messages API request,
// as the LLM gateway lets one through to the model vendor (spec Section 9.3;
// ADR-007). The allowlist is what the platform's runtime sends (ADR-006):
// text, the tools the client runs, their calls and results, the thinking
// blocks of the model's own answers, and a cache that lives five minutes.
// Everything else is refused: tools the vendor runs, content with a source
// of any kind, fields and beta headers outside the list. A request is read
// strictly, so that no two readers can read it in two ways, and the gateway
// forwards its own encoding of what it checked, never the client's bytes.
package prompt

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
)

// maxDepth is how deeply a request may nest objects and arrays.
const maxDepth = 64

// Policy is what the requests of one session may hold.
type Policy struct {
	// Model is the model the agent's configuration names.
	Model string
	// MaxTokens caps max_tokens.
	MaxTokens int64
	// Version is the anthropic-version the gateway sends.
	Version string
	// Answered reports whether the gateway gave the session a thinking
	// block of this hash, in an answer: only such a block may come back,
	// so that no text the gateway did not redact reaches the vendor in one.
	Answered func(hash [32]byte) bool
}

// Refusal says why a request is refused.
type Refusal struct {
	Reason string
}

func (r *Refusal) Error() string { return r.Reason }

func refuse(format string, args ...any) error {
	return &Refusal{Reason: fmt.Sprintf(format, args...)}
}

// Request is a request that passed every check.
type Request struct {
	doc       map[string]any
	maxTokens int64
}

// Model returns the model the request names.
func (r *Request) Model() string { return r.doc["model"].(string) }

// MaxTokens returns the request's max_tokens.
func (r *Request) MaxTokens() int64 { return r.maxTokens }

// Check reads a request and its headers and checks them against the
// allowlist and the session's policy. Headers other than anthropic-version,
// anthropic-beta and content-encoding are not checked: the gateway forwards
// none of them.
func Check(header map[string][]string, body []byte, p *Policy) (*Request, error) {
	if err := checkHeaders(header, p); err != nil {
		return nil, err
	}
	v, err := read(body)
	if err != nil {
		return nil, err
	}
	doc, ok := v.(map[string]any)
	if !ok {
		return nil, refuse("the request is not a JSON object")
	}
	r := &Request{doc: doc}
	for _, key := range []string{"model", "max_tokens", "messages"} {
		if _, ok := doc[key]; !ok {
			return nil, refuse("the request has no %s", key)
		}
	}
	for _, key := range sortedKeys(doc) {
		check, ok := fields[key]
		if !ok {
			return nil, refuse("the field %s is not allowed", quote(key))
		}
		if err := check(r, p, doc[key]); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// fields are the top-level fields a request may hold, each with its check:
// the fields the runtime sends.
var fields = map[string]func(r *Request, p *Policy, v any) error{
	"model": func(_ *Request, p *Policy, v any) error {
		if s, ok := v.(string); !ok || s != p.Model {
			return refuse("the model is not the agent's: %s", quote(fmt.Sprint(v)))
		}
		return nil
	},
	"max_tokens": func(r *Request, p *Policy, v any) error {
		n, ok := v.(json.Number)
		if !ok || !wholeNumber.MatchString(string(n)) {
			return refuse("max_tokens must be a whole number")
		}
		i, err := strconv.ParseInt(string(n), 10, 64)
		if err != nil || i > p.MaxTokens {
			return refuse("max_tokens must be at most %d", p.MaxTokens)
		}
		r.maxTokens = i
		return nil
	},
	"messages": func(_ *Request, p *Policy, v any) error { return checkMessages(v, p) },
	"system":   func(_ *Request, p *Policy, v any) error { return checkContent(v, "system", textOnly, p) },
	"tools":    func(_ *Request, _ *Policy, v any) error { return checkTools(v) },
}

var (
	// wholeNumber is a positive whole number written without a sign, a
	// point or an exponent.
	wholeNumber = regexp.MustCompile(`^[1-9]\d{0,18}$`)
	// name is what an id or a tool's name may be: never a channel for text.
	name = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

// blocks lists the content blocks each place allows.
type blocks []string

var (
	textOnly  = blocks{"text"}
	userKinds = blocks{"text", "tool_result"}
	assistant = blocks{"text", "tool_use", "thinking", "redacted_thinking"}
)

func checkMessages(v any, p *Policy) error {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return refuse("messages must be a list of messages")
	}
	for i, m := range list {
		msg, ok := m.(map[string]any)
		if !ok {
			return refuse("message %d is not an object", i)
		}
		where := fmt.Sprintf("message %d", i)
		if err := only(msg, where, "role", "content"); err != nil {
			return err
		}
		kinds := userKinds
		switch msg["role"] {
		case "user":
		case "assistant":
			kinds = assistant
		default:
			return refuse("message %d has no role of user or assistant", i)
		}
		if err := checkContent(msg["content"], where, kinds, p); err != nil {
			return err
		}
	}
	return nil
}

// checkContent checks content that is a string or a list of blocks of the
// kinds allowed.
func checkContent(v any, where string, kinds blocks, p *Policy) error {
	if _, ok := v.(string); ok {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return refuse("the content of %s must be text or a list of blocks", where)
	}
	for i, b := range list {
		block, ok := b.(map[string]any)
		if !ok {
			return refuse("block %d of %s is not an object", i, where)
		}
		typ, _ := block["type"].(string)
		if !slices.Contains(kinds, typ) {
			return refuse("block %d of %s is of a kind not allowed: %s", i, where, quote(fmt.Sprint(block["type"])))
		}
		if err := checkBlock(block, typ, fmt.Sprintf("block %d of %s", i, where), p); err != nil {
			return err
		}
	}
	return nil
}

func checkBlock(block map[string]any, typ, where string, p *Policy) error {
	var err error
	switch typ {
	case "text":
		err = checkText(block, where)
	case "tool_use":
		err = checkToolUse(block, where)
	case "tool_result":
		err = checkToolResult(block, where, p)
	case "thinking", "redacted_thinking":
		return checkThinking(block, where, p)
	}
	if err != nil {
		return err
	}
	return cacheControl(block, where)
}

func checkText(block map[string]any, where string) error {
	if err := only(block, where, "type", "text", "cache_control"); err != nil {
		return err
	}
	if !isString(block["text"]) {
		return refuse("%s has no text", where)
	}
	return nil
}

func checkToolUse(block map[string]any, where string) error {
	if err := only(block, where, "type", "id", "name", "input", "caller", "cache_control"); err != nil {
		return err
	}
	if !named(block["id"]) || !named(block["name"]) {
		return refuse("%s needs an id and a name of letters, digits, _ and -", where)
	}
	if _, ok := block["input"].(map[string]any); !ok {
		return refuse("%s needs an input object", where)
	}
	if c, ok := block["caller"]; ok {
		caller, isObject := c.(map[string]any)
		if !isObject || len(caller) != 1 || caller["type"] != "direct" {
			return refuse("the caller of %s may only be the model itself", where)
		}
	}
	return nil
}

func checkToolResult(block map[string]any, where string, p *Policy) error {
	if err := only(block, where, "type", "tool_use_id", "content", "is_error", "cache_control"); err != nil {
		return err
	}
	if !named(block["tool_use_id"]) {
		return refuse("%s needs a tool_use_id of letters, digits, _ and -", where)
	}
	if c, ok := block["content"]; ok {
		if err := checkContent(c, where, textOnly, p); err != nil {
			return err
		}
	}
	if e, ok := block["is_error"]; ok {
		if _, isBool := e.(bool); !isBool {
			return refuse("is_error of %s must be true or false", where)
		}
	}
	return nil
}

// checkThinking checks a thinking block of an answer that comes back, as
// the vendor needs it: unchanged. It must be one the gateway gave the
// session.
func checkThinking(block map[string]any, where string, p *Policy) error {
	hash, ok := ThinkingHash(block)
	if !ok || len(block) != len(thinkingFields[block["type"].(string)]) {
		return refuse("%s is not a thinking block as an answer holds one", where)
	}
	if p.Answered == nil || !p.Answered(hash) {
		return refuse("%s is not a thinking block the gateway gave this session", where)
	}
	return nil
}

// thinkingFields are the fields of a thinking block, and of a redacted
// one, that a request sends back.
var thinkingFields = map[string][]string{
	"thinking":          {"type", "thinking", "signature"},
	"redacted_thinking": {"type", "data"},
}

// ThinkingHash returns the hash of a thinking or redacted thinking block,
// over the fields a request sends back, and false for any other block. A
// field an answer holds besides them is not hashed, so that one the vendor
// adds does not keep the block from going back.
func ThinkingHash(block map[string]any) ([32]byte, bool) {
	typ, _ := block["type"].(string)
	keys, ok := thinkingFields[typ]
	if !ok {
		return [32]byte{}, false
	}
	fields := make(map[string]any, len(keys))
	for _, k := range keys {
		if !isString(block[k]) {
			return [32]byte{}, false
		}
		fields[k] = block[k]
	}
	return sha256.Sum256(encode(fields)), true
}

// cacheControl checks a cache breakpoint: an ephemeral entry that lives
// five minutes, the runtime's.
func cacheControl(obj map[string]any, where string) error {
	v, ok := obj["cache_control"]
	if !ok {
		return nil
	}
	cc, ok := v.(map[string]any)
	if !ok || cc["type"] != "ephemeral" {
		return refuse("the cache_control of %s must be ephemeral", where)
	}
	if err := only(cc, "the cache_control of "+where, "type", "ttl"); err != nil {
		return err
	}
	if ttl, ok := cc["ttl"]; ok && ttl != "5m" {
		return refuse("the cache_control of %s must live five minutes", where)
	}
	return nil
}

func checkTools(v any) error {
	list, ok := v.([]any)
	if !ok {
		return refuse("tools must be a list")
	}
	for i, t := range list {
		tool, ok := t.(map[string]any)
		if !ok {
			return refuse("tool %d is not an object", i)
		}
		where := fmt.Sprintf("tool %d", i)
		if typ, ok := tool["type"]; ok {
			return refuse("tool %d has a type, as a tool the vendor runs does: %s", i, quote(fmt.Sprint(typ)))
		}
		if err := only(tool, where, "name", "description", "input_schema", "cache_control"); err != nil {
			return err
		}
		if !named(tool["name"]) {
			return refuse("tool %d needs a name of letters, digits, _ and -", i)
		}
		if d, ok := tool["description"]; ok && !isString(d) {
			return refuse("the description of tool %d must be text", i)
		}
		if _, ok := tool["input_schema"].(map[string]any); !ok {
			return refuse("tool %d needs an input_schema object", i)
		}
		if err := cacheControl(tool, where); err != nil {
			return err
		}
	}
	return nil
}

// only refuses an object that holds a key other than those named.
func only(obj map[string]any, where string, keys ...string) error {
	for _, k := range sortedKeys(obj) {
		if !slices.Contains(keys, k) {
			return refuse("%s holds a field not allowed: %s", where, quote(k))
		}
	}
	return nil
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

// named reports an id or a name: letters, digits, _ and -.
func named(v any) bool {
	s, ok := v.(string)
	return ok && name.MatchString(s)
}

// quote quotes text a client wrote for a refusal, cut short.
func quote(s string) string {
	if len(s) > 64 {
		s = strings.ToValidUTF8(s[:64], "") + "…"
	}
	return strconv.Quote(s)
}

func checkHeaders(header map[string][]string, p *Policy) error {
	for name, values := range header {
		switch {
		case strings.EqualFold(name, "anthropic-version"):
			for _, v := range values {
				if v != p.Version {
					return refuse("anthropic-version %s is not the gateway's, %s", quote(v), p.Version)
				}
			}
		case strings.EqualFold(name, "anthropic-beta"):
			return refuse("no anthropic-beta is allowed: %s", quote(strings.Join(values, ",")))
		case strings.EqualFold(name, "content-encoding"):
			for _, v := range values {
				if !strings.EqualFold(strings.TrimSpace(v), "identity") {
					return refuse("a request body is sent as it is, not as %s", quote(v))
				}
			}
		}
	}
	return nil
}

// Redact redacts everything the model reads with red: text, the system
// prompt, tool results, every string, key and number of a tool call's input
// and of a tool's input schema, and tools' descriptions. Each part is
// redacted on its own, so that what was forwarded once is forwarded again
// the same. Types, cache control and the model stay as written, and
// thinking blocks too: the vendor needs them back unchanged, and the
// gateway gave them. Ids and names stay as written as well, since a
// conversation pairs a tool call with its result by them, so a request
// whose id or name holds what red hides is refused.
func (r *Request) Redact(red *redact.Redactor) error {
	if err := r.checkNames(red); err != nil {
		return err
	}
	if s, ok := r.doc["system"]; ok {
		r.doc["system"] = redactContent(s, red)
	}
	for _, m := range r.doc["messages"].([]any) {
		msg := m.(map[string]any)
		msg["content"] = redactContent(msg["content"], red)
	}
	if tools, ok := r.doc["tools"].([]any); ok {
		for _, t := range tools {
			tool := t.(map[string]any)
			if d, ok := tool["description"].(string); ok {
				tool["description"] = red.Text(d)
			}
			tool["input_schema"] = red.Value(tool["input_schema"])
		}
	}
	return nil
}

// checkNames refuses an id or a name that holds what red hides: tools'
// names, and the ids and names of tool calls and tool results.
func (r *Request) checkNames(red *redact.Redactor) error {
	hides := func(v any) bool {
		s, ok := v.(string)
		return ok && red.Text(s) != s
	}
	for i, m := range r.doc["messages"].([]any) {
		blocks, _ := m.(map[string]any)["content"].([]any)
		for j, b := range blocks {
			block := b.(map[string]any)
			for _, k := range []string{"id", "name", "tool_use_id"} {
				if hides(block[k]) {
					return refuse("the %s of message %d's block %d holds what a redaction rule hides, and cannot be redacted", k, i, j)
				}
			}
		}
	}
	tools, _ := r.doc["tools"].([]any)
	for i, t := range tools {
		if hides(t.(map[string]any)["name"]) {
			return refuse("the name of tool %d holds what a redaction rule hides, and cannot be redacted", i)
		}
	}
	return nil
}

func redactContent(v any, red *redact.Redactor) any {
	if s, ok := v.(string); ok {
		return red.Text(s)
	}
	for _, b := range v.([]any) {
		block := b.(map[string]any)
		switch block["type"] {
		case "text":
			block["text"] = red.Text(block["text"].(string))
		case "tool_use":
			block["input"] = red.Value(block["input"])
		case "tool_result":
			if c, ok := block["content"]; ok {
				block["content"] = redactContent(c, red)
			}
		}
	}
	return v
}

// Pin sets fields the gateway sends with every request: the service tier,
// and the inference region when one is given, so that a call is billed as
// the model's card prices it.
func (r *Request) Pin(serviceTier, inferenceGeo string) {
	r.doc["service_tier"] = serviceTier
	if inferenceGeo != "" {
		r.doc["inference_geo"] = inferenceGeo
	}
}

// Encode returns the request in the gateway's own encoding: compact JSON,
// keys sorted, numbers as written.
func (r *Request) Encode() []byte { return encode(r.doc) }

func encode(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// Redacted returns a request that was refused, for its record: its JSON
// with every string, key and number redacted, or "" when it is not one
// JSON document in valid UTF-8. A key it holds twice keeps its last value.
func Redacted(body []byte, red *redact.Redactor) string {
	if !utf8.Valid(body) {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return ""
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ""
	}
	return string(encode(red.Value(v)))
}

// read reads one JSON document strictly: valid UTF-8, no key twice in one
// object, no deeper than maxDepth, nothing after it. Numbers stay as
// written.
func read(body []byte) (any, error) {
	if !utf8.Valid(body) {
		return nil, refuse("the request is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	v, err := value(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, refuse("the request holds data after its end")
	}
	return v, nil
}

func value(dec *json.Decoder, depth int) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, refuse("the request is not JSON")
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	if depth == maxDepth {
		return nil, refuse("the request nests deeper than %d levels", maxDepth)
	}
	if d == '[' {
		list := []any{}
		for dec.More() {
			v, err := value(dec, depth+1)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		return list, end(dec)
	}
	obj := map[string]any{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, refuse("the request is not JSON")
		}
		key := tok.(string)
		if _, twice := obj[key]; twice {
			return nil, refuse("the request holds the key %s twice in one object", quote(key))
		}
		v, err := value(dec, depth+1)
		if err != nil {
			return nil, err
		}
		obj[key] = v
	}
	return obj, end(dec)
}

// end reads the delimiter that closes an object or an array.
func end(dec *json.Decoder) error {
	if _, err := dec.Token(); err != nil {
		return refuse("the request is not JSON")
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
```

- [ ] **Step 4: Run the tests to see them pass, and fuzz the reader**

Run: `go test ./internal/llm/prompt/ && go test ./internal/llm/prompt/ -run XXX -fuzz FuzzCheckNeverFails -fuzztime 20s -fuzzminimizetime 0 && golangci-lint run ./...`
Expected: `ok`, then `PASS` after millions of executions; `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal/llm/prompt
git commit -m "feat(llm): read, check, redact and encode a model request, as the LLM gateway allows one"
```

### Task 2: Costs and the vendor's answer

Decisions 6 to 8, in the domain: what a call costs, whether it may start, and what an answer holds.

**Files:**
- Create: `internal/llm/cost/cost.go`, `internal/llm/cost/cost_test.go`, `internal/llm/completion/completion.go`, `internal/llm/completion/completion_test.go`

**Interfaces:**
- Consumes: Task 1's `prompt.ThinkingHash` and `prompt.Redacted`; `redact.Redactor`.
- Produces:
  - `const MaxCount = 100_000_000`, the most tokens of one kind a call counts, and `const MaxRate = 1_000_000_000`, the dearest rate a card holds;
  - `type Usage struct { Input, CacheWrite5m, CacheWrite1h, CacheRead, Output int64 }`, with `Prompt() int64`;
  - `type Rates struct { Input, Output, CacheWrite5m, CacheWrite1h, CacheRead int64 }`; `type Card struct { Threshold int64; Base, Long Rates }`, with `(*Card) Check() error` and `(*Card) Spend(Usage) int64`;
  - `func Weighted(spend, reference int64) int64`; `func Estimate(requestBytes, maxTokens int64) Usage`;
  - `type Class string` with `Primary` and `Shadow`; `type Limits struct { DailyCap int64; ShadowCut int }`, with `Check() error`; `func Admit(runSpent, budget, today int64, class Class, l Limits) error`; `func DayStart(t time.Time) time.Time`;
  - `completion.Completion{Usage, Record, Thinking, Details}`; `func completion.Read(body []byte, red *redact.Redactor) (*Completion, error)`; `func completion.Failure(body []byte, red *redact.Redactor) string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/llm/cost/cost_test.go`:

```go
package cost_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/llm/cost"
)

// card prices a model as the vendor lists it, in pico-USD per token: $0.10
// and $0.50 per million input and output tokens, five times that for a
// prompt over 100,000 tokens.
var card = cost.Card{
	Threshold: 100_000,
	Base:      cost.Rates{Input: 100_000, Output: 500_000, CacheWrite5m: 125_000, CacheWrite1h: 200_000, CacheRead: 10_000},
	Long:      cost.Rates{Input: 500_000, Output: 2_500_000, CacheWrite5m: 625_000, CacheWrite1h: 1_000_000, CacheRead: 50_000},
}

// A call costs each kind of token at the model's rate for it, and the long
// rates once its prompt, cached or not, passes the threshold.
func TestACallCostsWhatTheVendorCharges(t *testing.T) {
	short := cost.Usage{Input: 1000, CacheWrite5m: 400, CacheWrite1h: 100, CacheRead: 20000, Output: 300}
	long := cost.Usage{Input: 1000, CacheRead: 99_001, Output: 10}

	assert.Equal(t, int64(1000*100_000+400*125_000+100*200_000+20000*10_000+300*500_000), card.Spend(short))
	assert.Equal(t, int64(1000*500_000+99_001*50_000+10*2_500_000), card.Spend(long), "100,001 prompt tokens")
	assert.Equal(t, int64(21_500), short.Prompt())
}

// A card without a threshold has one set of rates.
func TestACardWithoutAThresholdHasOneRate(t *testing.T) {
	flat := &cost.Card{Base: card.Base}

	assert.Equal(t, int64(1_000_000*100_000), flat.Spend(cost.Usage{Input: 1_000_000}))
}

// Cost-weighted tokens are spend at the reference price of one token,
// rounded up, so that budgets and the daily cap bound money whichever model
// an agent uses.
func TestWeightedTokensAreSpendAtTheReferencePrice(t *testing.T) {
	assert.Equal(t, int64(3), cost.Weighted(250_001, 100_000))
	assert.Equal(t, int64(2), cost.Weighted(200_000, 100_000))
	assert.Equal(t, int64(0), cost.Weighted(0, 100_000))
}

// A call whose cost is not known counts what it could have cost: its
// request at one token a byte, with the most the vendor adds of its own,
// all written to the cache, and its whole output.
func TestALostAnswerCountsItsWorstCase(t *testing.T) {
	assert.Equal(t, cost.Usage{CacheWrite5m: 1001 + 1024, Output: 4096}, cost.Estimate(1001, 4096))
	assert.Equal(t, cost.Usage{CacheWrite5m: cost.MaxCount, Output: cost.MaxCount}, cost.Estimate(1<<62, 1<<62), "never more than a card prices")
}

// Cards that cannot price are refused: every rate is above zero, and none
// is dearer than the dearest a card may hold.
func TestCardsMustPrice(t *testing.T) {
	require.NoError(t, card.Check())
	require.NoError(t, (&cost.Card{Base: card.Base}).Check())
	require.Error(t, (&cost.Card{Threshold: 100_000, Base: card.Base}).Check(), "no long rates")
	require.Error(t, (&cost.Card{Base: cost.Rates{Input: -1}}).Check())
	require.Error(t, (&cost.Card{}).Check(), "no rates at all")
	free := card.Base
	free.CacheRead = 0
	require.Error(t, (&cost.Card{Base: free}).Check(), "a rate of nothing")
	dear := card.Base
	dear.Output = cost.MaxRate + 1
	require.Error(t, (&cost.Card{Base: dear}).Check(), "a rate past the dearest")
}

// The dearest call a card can price fits in its spend: no count and no
// rate can overflow it.
func TestTheDearestCallFits(t *testing.T) {
	top := cost.Rates{Input: cost.MaxRate, Output: cost.MaxRate, CacheWrite5m: cost.MaxRate, CacheWrite1h: cost.MaxRate, CacheRead: cost.MaxRate}
	most := cost.Usage{Input: cost.MaxCount, CacheWrite5m: cost.MaxCount, CacheWrite1h: cost.MaxCount, CacheRead: cost.MaxCount, Output: cost.MaxCount}
	c := &cost.Card{Base: top}

	require.NoError(t, c.Check())
	assert.Equal(t, int64(5*cost.MaxCount*cost.MaxRate), c.Spend(most))
	assert.Positive(t, c.Spend(most))
}

var limits = cost.Limits{DailyCap: 1_000_000, ShadowCut: 80}

// A call starts while its run has budget left and the day's spend is under
// the cap; a comparison or probation run's call only while it is under
// 80% of the cap (spec Section 7).
func TestACallStartsWhileBudgetAndCapAllowIt(t *testing.T) {
	cases := []struct {
		name               string
		run, budget, today int64
		class              cost.Class
		refused            string
	}{
		{name: "fresh", run: 0, budget: 2000, today: 0, class: cost.Primary},
		{name: "budget nearly spent", run: 1999, budget: 2000, today: 0, class: cost.Primary},
		{name: "budget spent", run: 2000, budget: 2000, today: 0, class: cost.Primary, refused: "budget"},
		{name: "budget overspent", run: 2600, budget: 2000, today: 0, class: cost.Primary, refused: "budget"},
		{name: "no budget", run: 0, budget: 0, today: 0, class: cost.Primary, refused: "budget"},
		{name: "primary past the cut", run: 0, budget: 2000, today: 900_000, class: cost.Primary},
		{name: "shadow under the cut", run: 0, budget: 2000, today: 799_999, class: cost.Shadow},
		{name: "shadow at the cut", run: 0, budget: 2000, today: 800_000, class: cost.Shadow, refused: "80%"},
		{name: "primary at the cap", run: 0, budget: 2000, today: 1_000_000, class: cost.Primary, refused: "daily token cap"},
		{name: "unknown class", run: 0, budget: 2000, today: 0, class: "other", refused: "class"},
	}
	for _, c := range cases {
		err := cost.Admit(c.run, c.budget, c.today, c.class, limits)

		if c.refused == "" {
			require.NoError(t, err, c.name)
			continue
		}
		require.ErrorContains(t, err, c.refused, c.name)
	}
}

// The day turns at midnight UTC, whatever the clock's zone.
func TestTheDayTurnsAtMidnightUTC(t *testing.T) {
	sydney := time.FixedZone("AEDT", 11*3600)

	assert.Equal(t, time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), cost.DayStart(time.Date(2026, 10, 17, 9, 30, 0, 0, sydney)))
	assert.Equal(t, time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), cost.DayStart(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)))
}

// Limits a deployment cannot mean are refused.
func TestLimitsMustBeSet(t *testing.T) {
	require.NoError(t, limits.Check())
	require.Error(t, cost.Limits{DailyCap: 0, ShadowCut: 80}.Check())
	require.Error(t, cost.Limits{DailyCap: 1, ShadowCut: 0}.Check())
	require.Error(t, cost.Limits{DailyCap: 1, ShadowCut: 101}.Check())
}
```

Create `internal/llm/completion/completion_test.go`:

```go
package completion_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/llm/completion"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/llm/prompt"
)

var red = redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`sk-[a-z0-9]{8}`)}, Replacement: "[redacted:keys]"})

// answer is an answer as the vendor sends one: a thinking block, a text
// and a tool call, with the usage the vendor reports.
const answer = `{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
 "content": [{"type": "thinking", "thinking": "", "signature": "EqQBCkgI"},
  {"type": "text", "text": "The key sk-aaaaaaaa leaked."},
  {"type": "tool_use", "id": "toolu_2", "name": "metrics__query", "input": {"q": "sk-bbbbbbbb"}, "caller": {"type": "direct"}}],
 "stop_reason": "tool_use", "stop_sequence": null, "container": null,
 "usage": {"input_tokens": 120, "cache_creation_input_tokens": 500, "cache_read_input_tokens": 9000, "output_tokens": 80,
  "cache_creation": {"ephemeral_5m_input_tokens": 400, "ephemeral_1h_input_tokens": 100},
  "output_tokens_details": {"thinking_tokens": 24}, "service_tier": "standard", "inference_geo": "global"}}`

// An answer gives the tokens it used, its content redacted for the record,
// the hashes of its thinking blocks, and how the vendor served it.
func TestAnAnswerGivesItsTokensRecordAndThinking(t *testing.T) {
	c, err := completion.Read([]byte(answer), red)

	require.NoError(t, err)
	assert.Equal(t, cost.Usage{Input: 120, CacheWrite5m: 400, CacheWrite1h: 100, CacheRead: 9000, Output: 80}, c.Usage)
	assert.NotContains(t, c.Record, "sk-")
	assert.Contains(t, c.Record, `"stop_reason":"tool_use"`)
	assert.Contains(t, c.Record, `The key [redacted:keys] leaked.`)
	var block map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"type": "thinking", "thinking": "", "signature": "EqQBCkgI"}`), &block))
	h, _ := prompt.ThinkingHash(block)
	assert.Equal(t, [][32]byte{h}, c.Thinking)
	assert.JSONEq(t, `{"id": "msg_1", "model": "claude-haiku-5-5", "service_tier": "standard", "inference_geo": "global",
		"thinking_tokens": 24, "server_tool_use": null}`, c.Details)
}

// A thinking block is known by the fields a request sends back, so that a
// field the vendor adds to its answers does not keep the block from going
// back.
func TestAThinkingBlockIsKnownByWhatGoesBack(t *testing.T) {
	c, err := completion.Read([]byte(`{"content": [{"type": "thinking", "thinking": "", "signature": "EqQBCkgI", "added": 1}],
 "usage": {"input_tokens": 1, "output_tokens": 2}}`), red)

	require.NoError(t, err)
	var block map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"type": "thinking", "thinking": "", "signature": "EqQBCkgI"}`), &block))
	h, _ := prompt.ThinkingHash(block)
	assert.Equal(t, [][32]byte{h}, c.Thinking)
}

// Cache writes never count below what the vendor reports in all: without
// a breakdown, or beyond it, they count as one-hour writes, the dearer.
func TestCacheWritesNeverCountBelowTheTotal(t *testing.T) {
	for usage, want := range map[string]cost.Usage{
		`"cache_creation_input_tokens": 50`: {Input: 1, CacheWrite1h: 50, Output: 2},
		`"cache_creation_input_tokens": 9, "cache_creation": {"ephemeral_5m_input_tokens": 2, "ephemeral_1h_input_tokens": 3}`: {
			Input: 1, CacheWrite5m: 2, CacheWrite1h: 7, Output: 2},
		`"cache_creation_input_tokens": 9, "cache_creation": {"ephemeral_5m_input_tokens": 5, "ephemeral_1h_input_tokens": 5}`: {
			Input: 1, CacheWrite5m: 5, CacheWrite1h: 5, Output: 2},
	} {
		c, err := completion.Read([]byte(`{"content": [], "usage": {"input_tokens": 1, "output_tokens": 2, `+usage+`}}`), red)

		require.NoError(t, err)
		assert.Equal(t, want, c.Usage, usage)
	}
}

// An answer whose tokens cannot be read is an error: the budget would
// otherwise count nothing for it.
func TestAnAnswerWithoutUsageIsAnError(t *testing.T) {
	for _, body := range []string{
		`{"content": [], "stop_reason": "end_turn"}`,
		`{"content": [], "usage": {"output_tokens": 1}}`,
		`{"content": [], "usage": {"input_tokens": 1}}`,
		`{"content": [], "usage": {"input_tokens": -1, "output_tokens": 0}}`,
		`{"content": [], "usage": {"input_tokens": "1", "output_tokens": 0}}`,
		`{"content": [], "usage": {"input_tokens": 1e13, "output_tokens": 0}}`,
		`{"content": [], "usage": {"input_tokens": 100000001, "output_tokens": 0}}`,
		`not json`,
	} {
		_, err := completion.Read([]byte(body), red)

		require.Error(t, err, body)
	}
}

// A failure the vendor answered is recorded redacted, and cut short.
func TestAFailureIsRecordedRedacted(t *testing.T) {
	got := completion.Failure([]byte(`{"type": "error", "error": {"type": "invalid_request_error", "message": "bad sk-cccccccc"}}`), red)

	assert.JSONEq(t, `{"type": "error", "error": {"type": "invalid_request_error", "message": "bad [redacted:keys]"}}`, got)
	assert.Equal(t, "upstream sk [redacted:keys]", completion.Failure([]byte("upstream sk sk-dddddddd"), red))
	assert.LessOrEqual(t, len(completion.Failure([]byte(strings.Repeat("x", 10000)), red)), 4096+3)
	assert.Empty(t, completion.Failure([]byte("\xff\xfe"), red))
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/llm/cost/ ./internal/llm/completion/`
Expected: FAIL: both packages fail to build: `no non-test Go files` in `internal/llm/cost` and in `internal/llm/completion`

- [ ] **Step 3: Price a call, and decide whether it may start**

Create `internal/llm/cost/cost.go`:

```go
// Package cost prices model calls as the vendor charges for them, and holds
// the rules that let a call start: the run's token budget and the
// deployment's daily cap (spec Sections 5.6, 7 and 13.6; ADR-007). Budgets
// and the cap count cost-weighted tokens: spend at the price of one
// reference token, so that they bound money whichever model an agent uses.
package cost

import (
	"errors"
	"fmt"
	"time"
)

// MaxCount is the most tokens of one kind a call can count, a hundred times
// the longest context: an answer that reports more is not read, and no
// estimate counts more.
const MaxCount = 100_000_000

// MaxRate is the dearest rate a card may hold, in pico-USD a token: $1,000
// per million tokens. With MaxCount, no call's spend can overflow.
const MaxRate = 1_000_000_000

// framing is the most tokens the vendor adds to a request of its own, such
// as the system prompt that introduces tools, which is a few hundred.
const framing = 1024

// Usage counts one call's tokens as the vendor reports them.
type Usage struct {
	// Input is the input read neither from nor into the cache.
	Input int64
	// CacheWrite5m is input written to the five-minute cache, and
	// CacheWrite1h input written to the one-hour cache.
	CacheWrite5m, CacheWrite1h int64
	// CacheRead is input read from the cache.
	CacheRead int64
	Output    int64
}

// Prompt is the call's prompt in tokens: its input, cached or not.
func (u Usage) Prompt() int64 { return u.Input + u.CacheWrite5m + u.CacheWrite1h + u.CacheRead }

// Rates are what one token costs, in pico-USD (10^-12 USD): $3 per million
// tokens is 3,000,000.
type Rates struct {
	Input, Output, CacheWrite5m, CacheWrite1h, CacheRead int64
}

func (r Rates) valid() bool {
	for _, rate := range []int64{r.Input, r.Output, r.CacheWrite5m, r.CacheWrite1h, r.CacheRead} {
		if rate <= 0 || rate > MaxRate {
			return false
		}
	}
	return true
}

// Card is a model's prices, as the deployment copies them from the
// vendor's list: Base, and, when Threshold is set, Long for a call whose
// prompt is longer than Threshold tokens.
type Card struct {
	Threshold  int64
	Base, Long Rates
}

// Check reports a card that cannot price a call.
func (c *Card) Check() error {
	switch {
	case !c.Base.valid():
		return fmt.Errorf("cost: a card needs every rate above zero and at most %d pico-USD a token", MaxRate)
	case c.Threshold < 0:
		return errors.New("cost: a card's threshold cannot be below zero")
	case c.Threshold > 0 && !c.Long.valid():
		return errors.New("cost: a card with a threshold needs long rates")
	}
	return nil
}

// Spend returns what usage costs, in pico-USD.
func (c *Card) Spend(u Usage) int64 {
	r := c.Base
	if c.Threshold > 0 && u.Prompt() > c.Threshold {
		r = c.Long
	}
	return u.Input*r.Input + u.CacheWrite5m*r.CacheWrite5m + u.CacheWrite1h*r.CacheWrite1h + u.CacheRead*r.CacheRead + u.Output*r.Output
}

// Weighted returns spend in cost-weighted tokens: spend at the reference
// price of one token, in pico-USD, rounded up.
func Weighted(spend, reference int64) int64 { return (spend + reference - 1) / reference }

// Estimate returns the usage counted for a call whose cost is not known:
// one in flight, or one whose answer was lost or could not be read. It is
// the call's worst case, so that what the vendor may have charged is never
// counted as less: its request at one token a byte, with the most the
// vendor adds of its own, all written to the cache, and its whole output.
func Estimate(requestBytes, maxTokens int64) Usage {
	return Usage{CacheWrite5m: min(requestBytes+framing, MaxCount), Output: min(maxTokens, MaxCount)}
}

// Class says whether a run's calls are cut first as spend nears the daily
// cap.
type Class string

// Classes of runs (spec Section 7).
const (
	// Primary is a primary run of either arm: it is never cut before the
	// daily cap.
	Primary Class = "primary"
	// Shadow is a comparison or probation run: it is cut first.
	Shadow Class = "shadow"
)

// Limits are the deployment's limits on its daily spend.
type Limits struct {
	// DailyCap is the cost-weighted tokens the tenant's calls may spend in
	// one UTC day.
	DailyCap int64
	// ShadowCut is the share of the cap, in percent, from which shadow runs'
	// calls are refused [80].
	ShadowCut int
}

// Check reports limits that cannot hold: no cap, or a cut outside 1–100%.
func (l Limits) Check() error {
	if l.DailyCap <= 0 {
		return errors.New("cost: the daily token cap must be set")
	}
	if l.ShadowCut < 1 || l.ShadowCut > 100 {
		return fmt.Errorf("cost: the shadow cut is a share of the cap from 1 to 100%%, not %d%%", l.ShadowCut)
	}
	return nil
}

// Admit reports whether a call may start: its run has spent less than its
// budget, and the tenant has spent less today than the daily cap, or, for
// a shadow run, than the shadow cut. A call that starts below the budget
// may end above it.
func Admit(runSpent, budget, today int64, class Class, l Limits) error {
	switch {
	case class != Primary && class != Shadow:
		return fmt.Errorf("the session's class %q is neither primary nor shadow", class)
	case runSpent >= budget:
		return fmt.Errorf("the run's token budget of %d is spent", budget)
	case today >= l.DailyCap:
		return errors.New("the daily token cap is reached")
	case class == Shadow && today*100 >= l.DailyCap*int64(l.ShadowCut):
		return fmt.Errorf("comparison and probation runs are cut at %d%% of the daily token cap", l.ShadowCut)
	}
	return nil
}

// DayStart returns the start of t's day, UTC, from which the daily cap
// counts.
func DayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
```

- [ ] **Step 4: Read an answer**

Create `internal/llm/completion/completion.go`:

```go
// Package completion reads the model vendor's answer to a Messages API
// request: the tokens it used, which the budget counts; the completion as
// its record keeps it, redacted (spec Sections 9.3 and 9.4); the thinking
// blocks the next request may send back; and how the vendor served it.
package completion

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/llm/prompt"
)

// maxFailureBytes caps the record of a failure that is not JSON.
const maxFailureBytes = 4096

// Completion is an answer, read for the budget, the record and the next
// request.
type Completion struct {
	Usage cost.Usage
	// Record is the answer's content, stop reason and stop sequence, every
	// string, key and number redacted, as JSON.
	Record string
	// Thinking holds the hashes of the answer's thinking blocks, which the
	// next request of the session may send back unchanged.
	Thinking [][32]byte
	// Details is how the vendor served the answer, as JSON: its id, the
	// model, the service tier, the inference region, the thinking tokens
	// among the output, and any use of the vendor's own tools.
	Details string
}

// Read reads an answer the vendor sent with status 200. An answer whose
// token counts cannot be read is an error: the budget would count nothing
// for it.
func Read(body []byte, red *redact.Redactor) (*Completion, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.New("the answer is not a JSON object")
	}
	usage, ok := doc["usage"].(map[string]any)
	if !ok {
		return nil, errors.New("the answer reports no usage")
	}
	u, err := read(usage)
	if err != nil {
		return nil, err
	}
	c := &Completion{Usage: u}
	record := map[string]any{"content": doc["content"], "stop_reason": doc["stop_reason"], "stop_sequence": doc["stop_sequence"]}
	c.Record = string(encode(red.Value(record)))
	if content, ok := doc["content"].([]any); ok {
		for _, b := range content {
			if block, ok := b.(map[string]any); ok {
				if h, ok := prompt.ThinkingHash(block); ok {
					c.Thinking = append(c.Thinking, h)
				}
			}
		}
	}
	var thinkingTokens any
	if d, ok := usage["output_tokens_details"].(map[string]any); ok {
		thinkingTokens = d["thinking_tokens"]
	}
	c.Details = string(encode(map[string]any{
		"id": doc["id"], "model": doc["model"], "service_tier": usage["service_tier"], "inference_geo": usage["inference_geo"],
		"thinking_tokens": thinkingTokens, "server_tool_use": usage["server_tool_use"],
	}))
	return c, nil
}

// read reads the token counts. Cache writes never count below the total
// the vendor reports: without a breakdown, and beyond it, they count as
// one-hour writes, the dearer.
func read(usage map[string]any) (cost.Usage, error) {
	var u cost.Usage
	var written int64
	counts := []struct {
		name     string
		to       *int64
		required bool
	}{
		{"input_tokens", &u.Input, true}, {"output_tokens", &u.Output, true},
		{"cache_read_input_tokens", &u.CacheRead, false}, {"cache_creation_input_tokens", &written, false},
	}
	for _, c := range counts {
		n, err := count(usage, c.name, c.required)
		if err != nil {
			return cost.Usage{}, err
		}
		*c.to = n
	}
	breakdown, ok := usage["cache_creation"].(map[string]any)
	if !ok {
		u.CacheWrite1h = written
		return u, nil
	}
	m5, err := count(breakdown, "ephemeral_5m_input_tokens", false)
	if err != nil {
		return cost.Usage{}, err
	}
	h1, err := count(breakdown, "ephemeral_1h_input_tokens", false)
	if err != nil {
		return cost.Usage{}, err
	}
	u.CacheWrite5m, u.CacheWrite1h = m5, h1+max(written-m5-h1, 0)
	return u, nil
}

// count reads one token count: a whole number from 0 to cost.MaxCount.
func count(obj map[string]any, name string, required bool) (int64, error) {
	v, ok := obj[name]
	if !ok || v == nil {
		if required {
			return 0, errors.New("the answer reports no " + name)
		}
		return 0, nil
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, errors.New("the answer's " + name + " is not a number")
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil || i < 0 || i > cost.MaxCount {
		return 0, errors.New("the answer's " + name + " is not a count of tokens")
	}
	return i, nil
}

// Failure returns what the vendor answered a failed call with, for its
// record: JSON with every string, key and number redacted, other text
// redacted and cut short, or "" for bytes that are not text.
func Failure(body []byte, red *redact.Redactor) string {
	if s := prompt.Redacted(body, red); s != "" {
		return s
	}
	if !utf8.Valid(body) {
		return ""
	}
	s := red.Text(string(body))
	if len(s) > maxFailureBytes {
		s = strings.ToValidUTF8(s[:maxFailureBytes], "") + "…"
	}
	return s
}

func encode(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test ./internal/llm/cost/ ./internal/llm/completion/ && golangci-lint run ./...`
Expected: both `ok`; `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add internal/llm/cost internal/llm/completion
git commit -m "feat(llm): price model calls as the vendor charges, decide whether one may start, and read an answer"
```

### Task 3: Redaction rules the gateways can apply twice

Decision 5, in the registry: the LLM gateway redacts again what the MCP gateway redacted, so a pattern of a rule that applies always may not change any replacement, and a replacement holds no quote or backslash; and the LLM gateway forwards tools' names and tool calls' ids as written, so such a pattern may not match them.

**Files:**
- Modify: `internal/registry/config/redaction.go`, `internal/registry/config/names.go`, `internal/registry/config/server_test.go`, `internal/actions/app/board.go`

**Interfaces:**
- Consumes: the registry's loader (Plan 4); the board tools (Plan 5b).
- Produces:
  - `const SessionTokenReplacement = "[redacted:session-token]"`;
  - `var BoardTools []string`, the board tools' names, which `internal/actions/app` now reads;
  - problems: at `spec.patterns[i]` of a rule that applies always whose pattern changes a replacement, or matches a tool's name or a sampled tool-call id; at `spec.replacement` of a replacement that holds a quote or a backslash.

- [ ] **Step 1: Write the failing tests**

Append to `internal/registry/config/server_test.go`:

```go
// A rule that applies always redacts again what was redacted once, so its
// patterns must leave every replacement as it is: each rule's, and the LLM
// gateway's for a session's token. A pattern that turns a replacement into
// itself leaves it as it is, and a rule that does not apply always redacts
// a tool's output once.
func TestAPatternMayNotMatchAReplacement(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"redaction-rules/markers.yaml": file("kind: RedactionRule\nmetadata: {name: markers, owner: security@example.com}\n" +
			"spec: {always: true, patterns: ['zzz', 'redacted:[a-z-]+', 'session-token'], replacement: '<hidden>'}\n"),
		"redaction-rules/emails.yaml": file("kind: RedactionRule\nmetadata: {name: emails, owner: security@example.com}\n" +
			"spec: {always: true, patterns: ['[a-z]+@example[.]com'], replacement: 'user@example.com'}\n"),
		"redaction-rules/named.yaml": file("kind: RedactionRule\nmetadata: {name: named, owner: security@example.com}\n" +
			"spec: {patterns: ['hidden'], replacement: '[gone]'}\n"),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "redaction-rules/markers.yaml", "spec.patterns[1]", "changes the replacement")
	assertProblem(t, ps, "redaction-rules/markers.yaml", "spec.patterns[2]", config.SessionTokenReplacement)
	for _, p := range ps {
		assert.False(t, p.File == "redaction-rules/markers.yaml" && p.Field == "spec.patterns[0]", "a pattern that matches no replacement is fine")
		assert.NotEqual(t, "redaction-rules/emails.yaml", p.File, "a replacement a pattern turns into itself is fine")
		assert.NotEqual(t, "redaction-rules/named.yaml", p.File, "a rule that does not apply always redacts once")
	}
}

// A replacement holds no quote and no backslash: redacted again, it would be
// read as a member or an escape, and grow each time.
func TestAReplacementHoldsNoQuoteOrBackslash(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"redaction-rules/passwords.yaml": file("kind: RedactionRule\nmetadata: {name: passwords, owner: security@example.com}\n" +
			"spec: {patterns: ['password=\\S+'], replacement: '\"password\": \"***\"'}\n"),
		"redaction-rules/escapes.yaml": file("kind: RedactionRule\nmetadata: {name: escapes, owner: security@example.com}\n" +
			"spec: {patterns: ['zzz'], replacement: 'a\\b'}\n"),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "redaction-rules/passwords.yaml", "spec.replacement", "quote")
	assertProblem(t, ps, "redaction-rules/escapes.yaml", "spec.replacement", "backslash")
}

// A rule that applies always may not match what the LLM gateway forwards as
// written: a server's tool as agents name it, or a tool call's id in the
// vendor's form. The gateway refuses a request whose id or name holds what
// a rule hides, so such a rule would refuse every turn that calls a tool.
func TestARuleThatAppliesAlwaysSparesToolNamesAndIDs(t *testing.T) {
	rule := func(name, pattern string) *fstest.MapFile {
		return file("kind: RedactionRule\nmetadata: {name: " + name + ", owner: security@example.com}\n" +
			"spec: {always: true, patterns: ['" + pattern + "']}\n")
	}
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"redaction-rules/long-tokens.yaml":  rule("long-tokens", "[A-Za-z0-9]{24,}"),
		"redaction-rules/metric-names.yaml": rule("metric-names", "metrics__[a-z]+"),
		"redaction-rules/board-reads.yaml":  rule("board-reads", "read_board"),
		"redaction-rules/codes.yaml":        rule("codes", "[0-9]{6}"),
		"redaction-rules/whole-codes.yaml":  rule("whole-codes", "\\b[0-9]{6}\\b"),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "redaction-rules/long-tokens.yaml", "spec.patterns[0]", "toolu_")
	assertProblem(t, ps, "redaction-rules/metric-names.yaml", "spec.patterns[0]", "metrics__")
	assertProblem(t, ps, "redaction-rules/board-reads.yaml", "spec.patterns[0]", "read_board")
	assertProblem(t, ps, "redaction-rules/codes.yaml", "spec.patterns[0]", "toolu_")
	for _, p := range ps {
		assert.NotEqual(t, "redaction-rules/whole-codes.yaml", p.File, "a pattern anchored on word boundaries spares ids")
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/registry/config/ ./internal/actions/app/`
Expected: FAIL: `internal/registry/config` fails to build (`undefined: config.SessionTokenReplacement` in `server_test.go`); `internal/actions/app` passes

- [ ] **Step 3: List the board tools in the registry**

In `internal/registry/config/names.go`, after `AgentName`, add:

```go
// BoardTools are the tools the MCP gateway itself offers every agent (spec
// Section 5.2), named as agents see them.
var BoardTools = []string{"read_board", "post_evidence", "retract_evidence", "post_hypothesis", "post_task", "claim_task", "complete_task", "end_turn"}
```

In `internal/actions/app/board.go`, replace `isBoardTool` with this version, which reads them from there:

```go
// isBoardTool reports whether name is a board tool's, as the registry lists
// them.
func isBoardTool(name string) bool {
	return slices.Contains(config.BoardTools, name)
}
```

- [ ] **Step 4: Check the rules**

Replace `internal/registry/config/redaction.go` with this version, which adds `SessionTokenReplacement`, `checkReplacements` and `checkNamesSurvive`, and refuses a replacement that holds a quote or a backslash:

```go
package config

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/jchen0426/agent-swarm/internal/registry/selector"
)

// SessionTokenReplacement is what the LLM gateway puts in place of a
// session's token, wherever a request holds it. No rule that applies always
// may change it.
const SessionTokenReplacement = "[redacted:session-token]" //nolint:gosec // G101: what replaces a token, not one

// toolCallIDs are 10,000 ids in the model vendor's form for a tool call,
// which a request carries back with each call and its result: "toolu_01"
// and 22 letters and digits, drawn from a fixed seed.
var toolCallIDs = sync.OnceValue(func() []string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	rng := rand.New(rand.NewPCG(6, 2)) //nolint:gosec // G404: a fixed sample, not a secret
	ids := make([]string, 10_000)
	for i := range ids {
		b := []byte("toolu_01")
		for range 22 {
			b = append(b, alphabet[rng.IntN(len(alphabet))])
		}
		ids[i] = string(b)
	}
	return ids
})

// idHits remembers, for each pattern, a tool-call id it matches, or "".
var idHits sync.Map

// matchesToolCallID returns a tool-call id the pattern matches, or "".
func matchesToolCallID(re *regexp.Regexp) string {
	if hit, ok := idHits.Load(re.String()); ok {
		return hit.(string)
	}
	hit := ""
	if i := slices.IndexFunc(toolCallIDs(), re.MatchString); i >= 0 {
		hit = toolCallIDs()[i]
	}
	idHits.Store(re.String(), hit)
	return hit
}

// checkRedactionRules compiles every rule. Patterns are RE2, so matching
// takes linear time, and a pattern that matches the empty string is refused,
// because it would match everywhere.
func (ix *index) checkRedactionRules(ps *problems) {
	for _, name := range sortedKeys(ix.redactions) {
		r := ix.redactions[name]
		if len(r.Spec.Patterns) == 0 && len(r.Spec.Paths) == 0 {
			ps.add(r.file, "spec", "needs at least one pattern or path")
		}
		for i, p := range r.Spec.Patterns {
			field := fmt.Sprintf("spec.patterns[%d]", i)
			re, err := regexp.Compile(p)
			switch {
			case err != nil:
				ps.add(r.file, field, "is not a valid regular expression: %v", err)
			case re.MatchString(""):
				ps.add(r.file, field, "matches the empty string")
			default:
				r.compiled.patterns = append(r.compiled.patterns, re)
			}
		}
		for i, p := range r.Spec.Paths {
			sel, err := selector.Parse(p)
			if err != nil {
				ps.add(r.file, fmt.Sprintf("spec.paths[%d]", i), "%v", err)
				continue
			}
			r.compiled.paths = append(r.compiled.paths, sel)
		}
		if r.Spec.Always && len(r.Spec.Paths) > 0 {
			ps.add(r.file, "spec.paths", "a rule that applies always holds patterns only; paths are written for one tool's output")
		}
		if strings.ContainsAny(r.Replacement(), `"'\`) {
			ps.add(r.file, "spec.replacement", "may hold no quote and no backslash: redacted again, it would be read as a member or an escape")
		}
	}
	ix.checkReplacements(ps)
	ix.checkNamesSurvive(ps)
	ix.checkAlwaysRules(ps)
}

// checkReplacements refuses a pattern of a rule that applies always when it
// changes a replacement: any rule's, or the LLM gateway's for a session's
// token. These rules redact again what was redacted once (the LLM gateway
// redacts a tool's output the MCP gateway redacted, before the model reads
// it), and that must change nothing: a model that quotes a tool's output
// quotes it as it was stored. A pattern that turns a replacement into itself
// changes nothing. A replacement is checked on its own, not with the text
// around it.
func (ix *index) checkReplacements(ps *problems) {
	type replacement struct{ text, of string }
	replacements := []replacement{{SessionTokenReplacement, "the LLM gateway's session tokens"}}
	for _, name := range sortedKeys(ix.redactions) {
		replacements = append(replacements, replacement{ix.redactions[name].Replacement(), "rule " + name})
	}
	for _, r := range ix.alwaysRules() {
		for i, p := range r.Spec.Patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				continue
			}
			for _, rp := range replacements {
				if re.ReplaceAllLiteralString(rp.text, r.Replacement()) != rp.text {
					ps.add(r.file, fmt.Sprintf("spec.patterns[%d]", i),
						"changes the replacement %q, of %s: what was redacted once would change when redacted again", rp.text, rp.of)
				}
			}
		}
	}
}

// checkNamesSurvive refuses a pattern of a rule that applies always when it
// matches what the LLM gateway forwards as written: a tool as agents name
// it, a server's or a board tool, or a tool call's id in the vendor's form.
// The gateway refuses a request whose id or name holds what a rule hides,
// so such a pattern would refuse the turns that call a tool.
func (ix *index) checkNamesSurvive(ps *problems) {
	names := slices.Clone(BoardTools)
	for _, server := range sortedKeys(ix.servers) {
		for _, tool := range sortedKeys(ix.servers[server].Spec.Tools) {
			names = append(names, ToolRef{Server: server, Tool: tool}.AgentName())
		}
	}
	for _, r := range ix.alwaysRules() {
		for i, p := range r.Spec.Patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				continue
			}
			field := fmt.Sprintf("spec.patterns[%d]", i)
			if j := slices.IndexFunc(names, re.MatchString); j >= 0 {
				ps.add(r.file, field, "matches the tool name %q, which the LLM gateway forwards as written: every turn that offers the tool would be refused", names[j])
			} else if id := matchesToolCallID(re); id != "" {
				ps.add(r.file, field, "matches %q, a tool call's id in the vendor's form, which the LLM gateway forwards as written: turns that call tools would be refused; anchor the pattern, for example on word boundaries (\\b)", id)
			}
		}
	}
}

// checkAlwaysRules requires a rule that applies always, with patterns, once
// any server is registered: everything that reaches an agent, a store or the
// model passes the deployment's redaction rules (spec Section 9.4), also the
// output of a tool that names none.
func (ix *index) checkAlwaysRules(ps *problems) {
	if len(ix.servers) == 0 || len(ix.alwaysRules()) > 0 {
		return
	}
	first := ix.servers[sortedKeys(ix.servers)[0]]
	ps.add(first.file, "spec", "no redaction rule applies always; mark a rule of secret patterns always: true (spec Section 9.4)")
}

// alwaysRules returns the rules that apply always and have patterns, by name.
func (ix *index) alwaysRules() []*RedactionRule {
	var out []*RedactionRule
	for _, name := range sortedKeys(ix.redactions) {
		if r := ix.redactions[name]; r.Spec.Always && len(r.compiled.patterns) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// AlwaysRedactionRules returns the redaction rules that apply to everything
// the gateway stores or shows, sorted by name.
func (r *Revision) AlwaysRedactionRules() []*RedactionRule { return copyEach(r.ix.alwaysRules()) }

// checkRedactNames checks that every named rule exists, once.
func (ix *index) checkRedactNames(file, field string, names []string, ps *problems) {
	seen := map[string]bool{}
	for i, name := range names {
		switch {
		case seen[name]:
			ps.add(file, fmt.Sprintf("%s[%d]", field, i), "names %q twice", name)
		case ix.redactions[name] == nil:
			ps.add(file, fmt.Sprintf("%s[%d]", field, i), "names no redaction rule %q", name)
		}
		seen[name] = true
	}
}

// checkIdentitySources checks that each identity source names a listed read
// tool, and that no tool is registered twice.
func (ix *index) checkIdentitySources(ps *problems) {
	byTool := map[string]string{}
	for _, name := range sortedKeys(ix.identities) {
		s := ix.identities[name]
		ref, err := ParseToolRef(s.Spec.Tool)
		if err != nil {
			ps.add(s.file, "spec.tool", "%v", err)
			continue
		}
		if msg := ix.readTool(ref); msg != "" {
			ps.add(s.file, "spec.tool", "%s", msg)
		}
		if other, dup := byTool[ref.String()]; dup {
			ps.add(s.file, "spec.tool", "is already the identity source %q", other)
		}
		byTool[ref.String()] = name
	}
}

// identitySourceFor returns the identity source registered for a tool, or nil.
func (ix *index) identitySourceFor(ref ToolRef) *IdentitySource {
	for _, name := range sortedKeys(ix.identities) {
		if ix.identities[name].Spec.Tool == ref.String() {
			return ix.identities[name]
		}
	}
	return nil
}

// IdentitySource returns the identity source registered for a tool, or nil.
func (r *Revision) IdentitySource(ref ToolRef) *IdentitySource {
	return copied(r.ix.identitySourceFor(ref))
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test ./internal/registry/... ./internal/actions/... && golangci-lint run ./...`
Expected: every package `ok`; `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add internal/registry/config internal/actions/app/board.go
git commit -m "feat(registry): redaction rules the gateways can apply twice, which spare the names the LLM gateway forwards"
```

### Task 4: The gateway

Decisions 3 and 5 to 12, as the use case: one request, from the session to the recorded outcome. The vendor is a server of the MCP gateway's limits that serves agents only.

**Files:**
- Create: `internal/llm/app/ports.go`, `internal/llm/app/gateway.go`, `internal/llm/memory/memory.go`, `internal/llm/app/fixture_test.go`, `internal/llm/app/gateway_test.go`
- Modify: `internal/actions/limits/limits.go`, `internal/actions/limits/limits_test.go`

**Interfaces:**
- Consumes: Tasks 1 to 3; `config.Revision` (Plan 4); `limits.Settings` and `limits.Server` (Plan 5a); `redact` (Plans 5a and 6a).
- Produces:
  - `limits.Settings.AgentsOnly bool`: a server of agents only keeps no slots for platform reads;
  - `ErrNoSession`, `ErrNotSent`, `ErrLost`;
  - `type Session struct { Run, Agent, Commit string; Round int; Deadline time.Time; Class cost.Class; Budget int64; Done <-chan struct{} }`;
  - `type Sessions interface { Session(ctx, token string) (Session, error) }`; `type Revisions interface { At(ctx, commit string) (*config.Revision, error) }`;
  - `type Answer struct { Status int; Body []byte; RequestID string; RetryAfter time.Duration }`; `type Vendor interface { Send(ctx, body []byte) (*Answer, error) }`;
  - `type Status string` with `StatusOK`, `StatusRefused`, `StatusError`, `StatusUnknown`;
  - `type CallRecord struct { ID, Run, Agent string; Round int; Class cost.Class; Model, Request string; RequestBytes int64; RequestDigest string; Estimate int64; At time.Time }`;
  - `type Outcome struct { ID string; Status Status; Reason, Completion string; Usage cost.Usage; Spend, Weighted int64; Attempts int; Details string; At time.Time }`;
  - `type Calls interface { Begin; End; Spent(ctx, run string) (int64, error); SpentSince(ctx, since time.Time) (int64, error) }`;
  - `type Options` (with `LastCallWait`), `type Ports`, `type Inbound` (with `Length int64`, the size a request declared), `type Reply`; `const MaxSessions = 1024`; `func New(p *Ports, opts *Options) (*Gateway, error)`; `func (g *Gateway) Messages(ctx, in *Inbound) *Reply`; `func (g *Gateway) Drain(ctx) error`;
  - `memory.NewSessions()`; `memory.NewCalls()`, which fails a call whose context ended, as a database does, with `SetFail`, `SetFailEnd`, `HoldBegins`, `Held`, `Record` and `Records` for tests.

- [ ] **Step 1: Write the failing tests**

Create `internal/llm/app/fixture_test.go`:

```go
package app_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/llm/memory"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
)

const (
	model  = "claude-haiku-5-5"
	commit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	token  = "session-token-123456"
)

var t0 = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

// files are a configuration with one agent, on the gateway's model, and a
// rule of secret patterns that applies always.
var files = map[string][]byte{
	"redaction-rules/api-keys.yaml": []byte(`kind: RedactionRule
metadata: {name: api-keys, owner: security@example.com}
spec:
  always: true
  patterns: ['sk-[a-z0-9]{8}']
`),
	"agents/flag-investigator.yaml": []byte(`kind: Agent
metadata: {name: flag-investigator, owner: alice@example.com}
spec:
  runtime: thinloop
  model: claude-haiku-5-5
  tools: []
`),
}

// card prices the model at $0.10 and $0.50 per million input and output
// tokens: with the reference at the input price, a cost-weighted token is
// an uncached input token, a cache write 1.25 of one, a read 0.1 and an
// output token 5.
var card = cost.Card{Base: cost.Rates{Input: 100_000, Output: 500_000, CacheWrite5m: 125_000, CacheWrite1h: 200_000, CacheRead: 10_000}}

type revisions struct{ rev *config.Revision }

func (r *revisions) At(_ context.Context, c string) (*config.Revision, error) {
	if c != commit {
		return nil, fmt.Errorf("no revision %s", c)
	}
	return r.rev, nil
}

// vendor is the model vendor, scripted: each attempt takes the next answer
// or error, and the last answer repeats. It keeps what each attempt sent,
// and whether the call's record existed when it did.
type vendor struct {
	calls *memory.Calls

	mu       sync.Mutex
	answers  []*app.Answer
	errs     []error
	bodies   []string
	recorded []bool
	// block, when set, holds every attempt until it is closed, or until
	// the attempt's context ends.
	block chan struct{}
	sent  atomic.Int32
	// active counts the attempts under way, and most the most at once.
	active, most atomic.Int32
}

func (v *vendor) Send(ctx context.Context, body []byte) (*app.Answer, error) {
	v.mu.Lock()
	n := len(v.bodies)
	v.bodies = append(v.bodies, string(body))
	records := v.calls.Records()
	v.recorded = append(v.recorded, len(records) > 0 && records[len(records)-1].Request == string(body))
	block := v.block
	v.mu.Unlock()
	v.sent.Add(1)
	active := v.active.Add(1)
	defer v.active.Add(-1)
	for {
		most := v.most.Load()
		if active <= most || v.most.CompareAndSwap(most, active) {
			break
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			// As an HTTP client does when its request is cancelled after it
			// was written.
			return nil, fmt.Errorf("%w: %w", app.ErrLost, ctx.Err())
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if n < len(v.errs) && v.errs[n] != nil {
		return nil, v.errs[n]
	}
	if len(v.answers) == 0 {
		return ok(), nil
	}
	return v.answers[min(n, len(v.answers)-1)], nil
}

func (v *vendor) attempts() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.bodies)
}

// thinking is the thinking block of ok's answer.
const thinking = `{"type": "thinking", "thinking": "", "signature": "EqQBCkgIBRAB"}`

// ok is an answer as the model gives one: a thinking block, a text and a
// tool call, and the tokens it used: 1,000 input, 400 written to the
// five-minute cache, 20,000 read from it and 300 out, 5,000 cost-weighted.
func ok() *app.Answer {
	return &app.Answer{Status: 200, RequestID: "req_1", Body: []byte(`{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
 "content": [` + thinking + `, {"type": "text", "text": "Saw sk-zzzzzzzz in the logs."},
  {"type": "tool_use", "id": "toolu_9", "name": "metrics__query", "input": {"q": "x"}, "caller": {"type": "direct"}}],
 "stop_reason": "tool_use", "stop_sequence": null,
 "usage": {"input_tokens": 1000, "cache_creation_input_tokens": 400, "cache_read_input_tokens": 20000, "output_tokens": 300,
  "cache_creation": {"ephemeral_5m_input_tokens": 400, "ephemeral_1h_input_tokens": 0}, "service_tier": "standard"}}`)}
}

type ids struct{ n atomic.Int32 }

func (i *ids) NewID() string { return fmt.Sprintf("m-%d", i.n.Add(1)) }

type env struct {
	gw       *app.Gateway
	sessions *memory.Sessions
	calls    *memory.Calls
	vendor   *vendor
	session  app.Session
	opts     app.Options
	ports    app.Ports
}

func options() app.Options {
	return app.Options{
		MaxBodyBytes: 64 << 10, MaxTokens: 16384, Version: "2023-06-01",
		Limits: cost.Limits{DailyCap: 1_000_000, ShadowCut: 80}, Cards: map[string]cost.Card{model: card}, Reference: 100_000,
		Vendor:  limits.Settings{Slots: 10, Failures: 3, CoolDown: time.Minute},
		Backoff: []time.Duration{time.Millisecond, 2 * time.Millisecond}, VendorTimeout: 10 * time.Second,
		RecordTimeout: time.Second, MaxRefusals: 10, LastCallWait: 5 * time.Second,
	}
}

func newEnv(t *testing.T, change ...func(*app.Options)) *env {
	t.Helper()
	cc, err := celeval.New()
	require.NoError(t, err)
	fsys, err := memfs.New(files)
	require.NoError(t, err)
	rev, err := config.Build(fsys, commit, cc)
	require.NoError(t, err)
	e := &env{sessions: memory.NewSessions(), calls: memory.NewCalls(), opts: options()}
	for _, c := range change {
		c(&e.opts)
	}
	e.vendor = &vendor{calls: e.calls}
	e.session = app.Session{Run: "run-1", Agent: "flag-investigator", Commit: commit, Round: 2, Class: cost.Primary,
		Budget: 2_000_000, Deadline: time.Now().Add(time.Minute)}
	e.sessions.Put(token, &e.session)
	e.ports = app.Ports{Sessions: e.sessions, Revisions: &revisions{rev}, Vendor: e.vendor, Calls: e.calls,
		IDs: &ids{}, Now: func() time.Time { return t0 }}
	e.gw, err = app.New(&e.ports, &e.opts)
	require.NoError(t, err)
	return e
}

// restart replaces the gateway with a new one, as a restart does: what it
// kept in memory is gone, the records stay.
func (e *env) restart(t *testing.T) {
	t.Helper()
	gw, err := app.New(&e.ports, &e.opts)
	require.NoError(t, err)
	e.gw = gw
}

// headers are what anthropic-sdk-go sends.
func headers() map[string][]string {
	return map[string][]string{"Anthropic-Version": {"2023-06-01"}, "Content-Type": {"application/json"}, "X-Stainless-Os": {"Linux"}}
}

// request is a request as the runner sends one at a turn's start.
const request = `{"model": "claude-haiku-5-5", "max_tokens": 1024,
 "system": [{"type": "text", "text": "You investigate incidents.", "cache_control": {"type": "ephemeral"}}],
 "tools": [{"name": "metrics__query", "description": "Query metrics.", "input_schema": {"type": "object"}}],
 "messages": [{"role": "user", "content": "Find why checkout fails."}]}`

// next is the request the runner sends after ok's answer: the answer
// echoed back, its thinking block included, and the tool's result.
const next = `{"model": "claude-haiku-5-5", "max_tokens": 1024,
 "system": [{"type": "text", "text": "You investigate incidents.", "cache_control": {"type": "ephemeral"}}],
 "tools": [{"name": "metrics__query", "description": "Query metrics.", "input_schema": {"type": "object"}}],
 "messages": [{"role": "user", "content": "Find why checkout fails."},
  {"role": "assistant", "content": [` + thinking + `, {"type": "text", "text": "Saw sk-zzzzzzzz in the logs."},
   {"type": "tool_use", "id": "toolu_9", "name": "metrics__query", "input": {"q": "x"}, "caller": {"type": "direct"}}]},
  {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_9", "content": "errors rose"}]}]}`

func (e *env) send(t *testing.T, tok, body string) *app.Reply {
	t.Helper()
	return e.sendWith(t.Context(), tok, body)
}

// sendWith sends body with the session token tok, as a request whose
// context is ctx.
func (e *env) sendWith(ctx context.Context, tok, body string) *app.Reply {
	return e.gw.Messages(ctx, &app.Inbound{Method: "POST", Path: "/v1/messages", Token: tok, Header: headers(), Body: []byte(body)})
}

// stray sends a request the gateway refuses before reading it, with the
// session token tok.
func (e *env) stray(t *testing.T, tok string) *app.Reply {
	t.Helper()
	return e.gw.Messages(t.Context(), &app.Inbound{Method: "GET", Path: "/v1/models", Token: tok, Header: headers()})
}

func (e *env) post(t *testing.T, body string) *app.Reply {
	t.Helper()
	return e.send(t, token, body)
}

// last returns the last call's record and outcome.
func (e *env) last(t *testing.T) (app.CallRecord, app.Outcome) {
	t.Helper()
	records := e.calls.Records()
	require.NotEmpty(t, records, "no call was recorded")
	rec, o, _, ended := e.calls.Record(records[len(records)-1].ID)
	require.True(t, ended, "the call has no outcome")
	return rec, o
}
```

Create `internal/llm/app/gateway_test.go`:

```go
package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
)

// errorType reads the Messages API's error type from a reply.
func errorType(t *testing.T, r *app.Reply) string {
	t.Helper()
	var body struct {
		Error struct{ Type string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(r.Body, &body), string(r.Body))
	return body.Error.Type
}

// A call is recorded before it reaches the vendor, in the form it is sent
// in, then its outcome: the tokens it used, what they cost and how the
// vendor served it. The runtime gets the vendor's answer as it was sent.
func TestACallIsRecordedBeforeItIsForwardedThenItsOutcome(t *testing.T) {
	e := newEnv(t)

	r := e.post(t, request)

	require.Equal(t, 200, r.Status, string(r.Body))
	assert.Equal(t, string(ok().Body), string(r.Body))
	assert.Equal(t, "req_1", r.Header["Request-Id"])
	assert.Equal(t, []bool{true}, e.vendor.recorded, "the record existed when the vendor was called")
	rec, o := e.last(t)
	sum := sha256.Sum256([]byte(e.vendor.bodies[0]))
	assert.Equal(t, e.vendor.bodies[0], rec.Request, "the record holds what was sent")
	assert.Equal(t, hex.EncodeToString(sum[:]), rec.RequestDigest)
	assert.Equal(t, int64(len(e.vendor.bodies[0])), rec.RequestBytes)
	assert.Equal(t, model, rec.Model)
	assert.Equal(t, "run-1", rec.Run)
	assert.Equal(t, "flag-investigator", rec.Agent)
	assert.Equal(t, 2, rec.Round)
	assert.Equal(t, cost.Primary, rec.Class)
	assert.Equal(t, app.StatusOK, o.Status)
	assert.Equal(t, cost.Usage{Input: 1000, CacheWrite5m: 400, CacheRead: 20000, Output: 300}, o.Usage)
	assert.Equal(t, int64(500_000_000), o.Spend, "pico-USD")
	assert.Equal(t, int64(5000), o.Weighted)
	assert.Equal(t, 1, o.Attempts)
	assert.Contains(t, o.Details, `"id":"msg_1"`)
	assert.NotContains(t, o.Completion, "sk-")
}

// The vendor gets the gateway's own encoding of what it checked, never the
// client's bytes, at the standard service tier.
func TestTheVendorGetsTheGatewaysEncoding(t *testing.T) {
	e := newEnv(t)

	e.post(t, request)

	assert.NotEqual(t, request, e.vendor.bodies[0])
	assert.True(t, strings.HasPrefix(e.vendor.bodies[0], `{"max_tokens":1024,"messages":`), e.vendor.bodies[0])
	assert.Contains(t, e.vendor.bodies[0], `"service_tier":"standard_only"`)
}

// Secrets never reach the vendor, the session's own token included, and
// the record holds the completion redacted; the runtime gets the
// completion as the vendor wrote it.
func TestSecretsNeverReachTheVendor(t *testing.T) {
	e := newEnv(t)
	body := strings.NewReplacer("You investigate incidents.", "Use sk-aaaaaaaa.", "Find why checkout fails.", "My token is "+token+"?").Replace(request)

	r := e.post(t, body)

	require.Equal(t, 200, r.Status, string(r.Body))
	assert.NotContains(t, e.vendor.bodies[0], "sk-")
	assert.NotContains(t, e.vendor.bodies[0], token)
	assert.Contains(t, e.vendor.bodies[0], "[redacted:api-keys]")
	assert.Contains(t, e.vendor.bodies[0], "[redacted:session-token]")
	rec, o := e.last(t)
	assert.NotContains(t, rec.Request, "sk-")
	assert.NotContains(t, o.Completion, "sk-")
	assert.Contains(t, string(r.Body), "sk-zzzzzzzz", "the runtime gets the answer as sent")
}

// A thinking block the gateway gave a session comes back with the session's
// next request; another session, or the same after the gateway restarted,
// cannot send it.
func TestAThinkingBlockGoesBackOnlyFromTheSessionThatGotIt(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, 200, e.post(t, request).Status)
	other := e.session
	other.Run = "run-other"
	e.sessions.Put("other-token", &other)

	back := e.post(t, next)
	stolen := e.send(t, "other-token", next)
	e.restart(t)
	afterRestart := e.post(t, next)

	assert.Equal(t, 200, back.Status, string(back.Body))
	assert.Contains(t, e.vendor.bodies[1], `"signature":"EqQBCkgIBRAB"`, "the block goes back unchanged")
	assert.Equal(t, 400, stolen.Status)
	assert.Equal(t, 400, afterRestart.Status)
	assert.Contains(t, string(afterRestart.Body), "thinking block")
}

// Every request a session sends is recorded, also one the gateway refuses,
// with its reason; nothing refused reaches the vendor.
func TestEveryRefusalIsRecorded(t *testing.T) {
	cases := []struct {
		name                string
		method, path, query string
		header              map[string][]string
		body                string
		length              int64
		status              int
		kind                string
	}{
		{name: "another path", method: "POST", path: "/v1/files", body: request, status: 404, kind: "not_found_error"},
		{name: "count tokens", method: "POST", path: "/v1/messages/count_tokens", body: request, status: 404, kind: "not_found_error"},
		{name: "another method", method: "GET", path: "/v1/messages", status: 404, kind: "not_found_error"},
		{name: "a query", method: "POST", path: "/v1/messages", query: "beta=true", body: request, status: 404, kind: "not_found_error"},
		{name: "too large", method: "POST", path: "/v1/messages", body: strings.Repeat(" ", 64<<10+1), length: 1 << 20, status: 413, kind: "request_too_large"},
		{name: "a secret in an id", method: "POST", path: "/v1/messages", body: strings.Replace(request, `"content": "Find why checkout fails."}]`,
			`"content": "Find why checkout fails."}, {"role": "assistant", "content": [{"type": "tool_use", "id": "`+token+`", "name": "metrics__query", "input": {}}]},
			 {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "`+token+`", "content": "none"}]}]`, 1), status: 400, kind: "invalid_request_error"},
		{name: "another model", method: "POST", path: "/v1/messages", body: strings.Replace(request, model, "claude-opus-5-5", 1), status: 400, kind: "invalid_request_error"},
		{name: "a vendor tool", method: "POST", path: "/v1/messages", body: strings.Replace(request, `"tools": [{`, `"tools": [{"type": "web_search_20250305", `, 1), status: 400, kind: "invalid_request_error"},
		{name: "a field outside the list", method: "POST", path: "/v1/messages", body: strings.Replace(request, `{"model"`, `{"mcp_servers": [], "model"`, 1), status: 400, kind: "invalid_request_error"},
		{name: "a beta", method: "POST", path: "/v1/messages", header: map[string][]string{"Anthropic-Beta": {"files-api-2025-04-14"}}, body: request, status: 400, kind: "invalid_request_error"},
		{name: "a compressed body", method: "POST", path: "/v1/messages", header: map[string][]string{"Content-Encoding": {"gzip"}}, body: request, status: 400, kind: "invalid_request_error"},
	}
	for _, c := range cases {
		e := newEnv(t)
		h := headers()
		for k, v := range c.header {
			h[k] = v
		}

		r := e.gw.Messages(t.Context(), &app.Inbound{Method: c.method, Path: c.path, Query: c.query, Token: token, Header: h, Body: []byte(c.body), Length: c.length})

		assert.Equal(t, c.status, r.Status, c.name)
		assert.Equal(t, c.kind, errorType(t, r), c.name)
		assert.Equal(t, "false", r.Header["X-Should-Retry"], c.name)
		assert.Zero(t, e.vendor.attempts(), c.name)
		rec, o := e.last(t)
		assert.Equal(t, app.StatusRefused, o.Status, c.name)
		assert.NotEmpty(t, o.Reason, c.name)
		assert.NotContains(t, rec.Request+o.Reason, token, c.name)
		assert.Zero(t, rec.Estimate, c.name)
		if c.length > 0 {
			assert.Equal(t, c.length, rec.RequestBytes, c.name)
		}
	}
}

// A refused request's record keeps at most its first 4 KiB, redacted, with
// the size it came in and the digest of its redacted JSON in full: a
// sandbox cannot fill the audit store with refused requests.
func TestARefusedRequestIsRecordedShort(t *testing.T) {
	e := newEnv(t)
	body := strings.Replace(request, `{"model"`, `{"metadata": {"user_id": "sk-cccccccc", "zz_pad": "`+strings.Repeat("x", 20000)+`"}, "model"`, 1)

	e.post(t, body)

	rec, _ := e.last(t)
	assert.LessOrEqual(t, len(rec.Request), 4096+3)
	assert.Contains(t, rec.Request, `"user_id":"[redacted:api-keys]"`)
	assert.NotContains(t, rec.Request, "sk-")
	assert.Equal(t, int64(len(body)), rec.RequestBytes)
	assert.Len(t, rec.RequestDigest, 64)
}

// A session refused too often in a round is shut out until the next: one
// more refusal is recorded, then its requests are refused unrecorded.
func TestASessionRefusedTooOftenIsShutOutForTheRound(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.MaxRefusals = 3 })
	bad := strings.Replace(request, `{"model"`, `{"metadata": {}, "model"`, 1)
	for range 3 {
		require.Equal(t, 400, e.post(t, bad).Status)
	}

	first := e.post(t, request)
	recorded := len(e.calls.Records())
	second := e.post(t, request)
	e.session.Round++
	e.sessions.Put(token, &e.session)
	nextRound := e.post(t, request)

	assert.Equal(t, 429, first.Status)
	assert.Equal(t, 429, second.Status)
	assert.Equal(t, 4, recorded, "three refusals and the shut-out")
	assert.Equal(t, 200, nextRound.Status, string(nextRound.Body))
	assert.Equal(t, 1, e.vendor.attempts())
}

// A request sent after the session's round ended is refused.
func TestACallAfterTheRoundEndedIsRefused(t *testing.T) {
	e := newEnv(t)
	e.session.Deadline = time.Now().Add(-time.Second)
	e.sessions.Put(token, &e.session)

	r := e.post(t, request)

	assert.Equal(t, 403, r.Status)
	assert.Contains(t, string(r.Body), "round has ended")
	assert.Zero(t, e.vendor.attempts())
}

// A token that names no live session is refused, and nothing is recorded:
// nothing names a run to record it under.
func TestATokenOfNoLiveSessionIsRefused(t *testing.T) {
	e := newEnv(t)
	e.sessions.End(token)

	r := e.post(t, request)

	assert.Equal(t, 401, r.Status)
	assert.Equal(t, "authentication_error", errorType(t, r))
	assert.Empty(t, e.calls.Records())
	assert.Zero(t, e.vendor.attempts())
}

// Once a run has spent its budget, its next call is refused and recorded,
// while another run's calls go on.
func TestARunsBudgetStopsItsCalls(t *testing.T) {
	e := newEnv(t)
	e.session.Budget = 5000
	e.sessions.Put(token, &e.session)
	require.Equal(t, 200, e.post(t, request).Status)

	r := e.post(t, request)

	assert.Equal(t, 403, r.Status)
	assert.Equal(t, "permission_error", errorType(t, r))
	assert.Contains(t, string(r.Body), "budget")
	assert.Equal(t, 1, e.vendor.attempts())
	_, o := e.last(t)
	assert.Equal(t, app.StatusRefused, o.Status)
	other := e.session
	other.Run, other.Budget = "run-2", 1_000_000
	e.sessions.Put("other-token", &other)
	assert.Equal(t, 200, e.send(t, "other-token", request).Status)
}

// Comparison and probation runs are cut at 80% of the daily cap; primary
// runs go on until the cap (spec Section 7).
func TestShadowRunsAreCutFirst(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.Limits.DailyCap = 12_000 })
	require.Equal(t, 200, e.post(t, request).Status, "5,000 spent today")
	shadow := e.session
	shadow.Run, shadow.Class = "run-shadow", cost.Shadow
	e.sessions.Put("shadow-token", &shadow)
	second := e.session
	second.Run = "run-second"
	e.sessions.Put("second-token", &second)

	require.Equal(t, 200, e.send(t, "shadow-token", request).Status, "under 80%: 10,000 spent then")
	cut := e.send(t, "shadow-token", request)
	primary := e.send(t, "second-token", request)
	capped := e.send(t, "second-token", request)

	assert.Equal(t, 403, cut.Status)
	assert.Contains(t, string(cut.Body), "80%")
	assert.Equal(t, 200, primary.Status, "a primary run goes on past 80%")
	assert.Equal(t, 403, capped.Status)
	assert.Contains(t, string(capped.Body), "daily token cap")
}

// A session makes one model call at a time: another sent while one is in
// flight is refused and recorded.
func TestASessionMakesOneCallAtATime(t *testing.T) {
	e := newEnv(t)
	e.vendor.block = make(chan struct{})
	var wg sync.WaitGroup
	var first *app.Reply
	wg.Go(func() { first = e.post(t, request) })
	require.Eventually(t, func() bool { return e.vendor.sent.Load() == 1 }, 5*time.Second, time.Millisecond)

	second := e.post(t, request)
	close(e.vendor.block)
	wg.Wait()

	assert.Equal(t, 429, second.Status)
	assert.Equal(t, "rate_limit_error", errorType(t, second))
	assert.Equal(t, 200, first.Status)
	assert.Equal(t, 200, e.post(t, request).Status, "the next call goes once the first ended")
}

// A failure another attempt may get past is retried, after the wait the
// vendor asks for when it is longer, within the round.
func TestTransientFailuresAreRetriedWithinTheRound(t *testing.T) {
	e := newEnv(t)
	e.vendor.answers = []*app.Answer{{Status: 529, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`), RetryAfter: 30 * time.Millisecond}, ok()}
	start := time.Now()

	r := e.post(t, request)

	assert.Equal(t, 200, r.Status)
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond, "the vendor's wait")
	_, o := e.last(t)
	assert.Equal(t, 2, o.Attempts)
	assert.Equal(t, app.StatusOK, o.Status)
}

// When the round leaves no time for the next wait, the last failure is
// answered at once, and recorded.
func TestNoAttemptOutlastsTheRound(t *testing.T) {
	e := newEnv(t)
	e.session.Deadline = time.Now().Add(300 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	e.vendor.answers = []*app.Answer{{Status: 529, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`), RetryAfter: 2 * time.Second}}
	start := time.Now()

	r := e.post(t, request)

	assert.Less(t, time.Since(start), 200*time.Millisecond, "no wait the round cannot hold")
	assert.Equal(t, 529, r.Status)
	assert.Equal(t, "false", r.Header["X-Should-Retry"])
	_, o := e.last(t)
	assert.Equal(t, 1, o.Attempts)
	assert.Equal(t, app.StatusError, o.Status)
	assert.Contains(t, o.Reason, "overloaded_error")
}

// A request that never left is tried again: it cost nothing.
func TestARequestThatNeverLeftIsTriedAgain(t *testing.T) {
	e := newEnv(t)
	e.vendor.errs = []error{fmt.Errorf("%w: connection refused", app.ErrNotSent)}

	r := e.post(t, request)

	assert.Equal(t, 200, r.Status)
	_, o := e.last(t)
	assert.Equal(t, 2, o.Attempts)
}

// An answer lost after the request reached the vendor is never tried
// again, and counts the call's worst case: the vendor may have charged
// for it.
func TestALostAnswerCountsItsWorstCase(t *testing.T) {
	e := newEnv(t)
	e.vendor.errs = []error{fmt.Errorf("%w: connection reset", app.ErrLost)}

	r := e.post(t, request)

	assert.Equal(t, 502, r.Status)
	assert.Equal(t, 1, e.vendor.attempts())
	rec, o := e.last(t)
	assert.Equal(t, app.StatusUnknown, o.Status)
	assert.Equal(t, cost.Estimate(rec.RequestBytes, 1024), o.Usage)
	assert.Equal(t, cost.Weighted(card.Spend(o.Usage), 100_000), o.Weighted)
	assert.Positive(t, o.Weighted)
}

// An answer whose tokens cannot be read counts the call's worst case too.
func TestAnAnswerWithoutUsageCountsItsWorstCase(t *testing.T) {
	e := newEnv(t)
	e.vendor.answers = []*app.Answer{{Status: 200, Body: []byte(`{"content": [], "stop_reason": "end_turn"}`)}}

	r := e.post(t, request)

	assert.Equal(t, 502, r.Status)
	_, o := e.last(t)
	assert.Equal(t, app.StatusUnknown, o.Status)
	assert.Positive(t, o.Weighted)
}

// The vendor's refusal is answered as the vendor sent it, and recorded
// redacted; it cost nothing.
func TestTheVendorsRefusalIsAnsweredAsSent(t *testing.T) {
	e := newEnv(t)
	vendorError := `{"type":"error","error":{"type":"invalid_request_error","message":"bad sk-dddddddd"}}`
	e.vendor.answers = []*app.Answer{{Status: 400, Body: []byte(vendorError)}}

	r := e.post(t, request)

	assert.Equal(t, 400, r.Status)
	assert.Equal(t, vendorError, string(r.Body))
	_, o := e.last(t)
	assert.Equal(t, app.StatusError, o.Status)
	assert.Contains(t, o.Reason, "the vendor answered 400")
	assert.NotContains(t, o.Reason, "sk-")
	assert.Zero(t, o.Weighted)
}

// An answer that cannot be recorded is not returned: it would be model
// output the audit store does not hold. The runtime is told not to repeat
// the call, which was made, and cannot send its thinking block back.
func TestAnAnswerThatCannotBeRecordedIsWithheld(t *testing.T) {
	e := newEnv(t)
	e.calls.SetFailEnd(errors.New("the store is down"))

	r := e.post(t, request)
	e.calls.SetFailEnd(nil)
	back := e.post(t, next)

	assert.Equal(t, 503, r.Status)
	assert.NotContains(t, string(r.Body), "toolu_9")
	assert.Equal(t, "false", r.Header["X-Should-Retry"])
	assert.Equal(t, 400, back.Status, "the withheld answer's thinking block is not the session's")
}

// While the audit store is down, nothing reaches the vendor (spec Section
// 8.2), and the runtime may try again.
func TestNothingIsForwardedWhileTheAuditStoreIsDown(t *testing.T) {
	e := newEnv(t)
	e.calls.SetFail(errors.New("the store is down"))

	r := e.post(t, request)

	assert.Equal(t, 503, r.Status)
	assert.Equal(t, "true", r.Header["X-Should-Retry"])
	assert.Zero(t, e.vendor.attempts())
}

// A model the deployment set no card for is refused: its spend could not
// be counted.
func TestAModelWithoutACardIsRefused(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.Cards = map[string]cost.Card{} })

	r := e.post(t, request)

	assert.Equal(t, 403, r.Status)
	assert.Contains(t, string(r.Body), "price")
	assert.Zero(t, e.vendor.attempts())
}

// When the round ends before the vendor answers, the request is answered
// at once, and the call goes on until the vendor answers, so that what it
// cost is recorded.
func TestACallGoesOnPastTheRoundUntilItsCostIsKnown(t *testing.T) {
	e := newEnv(t)
	e.session.Deadline = time.Now().Add(100 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	e.vendor.block = make(chan struct{})

	ctx, cancel := context.WithCancel(t.Context())
	r := e.gw.Messages(ctx, &app.Inbound{Method: "POST", Path: "/v1/messages", Token: token, Header: headers(), Body: []byte(request)})
	cancel() // as an HTTP server does once it has answered
	_, _, _, endedBefore := e.calls.Record("m-1")
	close(e.vendor.block)

	assert.Equal(t, 504, r.Status)
	assert.False(t, endedBefore)
	require.Eventually(t, func() bool { _, _, _, ended := e.calls.Record("m-1"); return ended }, 5*time.Second, 5*time.Millisecond)
	_, o, _, _ := e.calls.Record("m-1")
	assert.Equal(t, app.StatusOK, o.Status)
	assert.Equal(t, int64(5000), o.Weighted)
}

// When the run stops, the request is answered at once; the call's cost is
// still recorded when the vendor answers.
func TestARequestIsAnsweredWhenTheRunStops(t *testing.T) {
	e := newEnv(t)
	done := make(chan struct{})
	e.session.Done = done
	e.sessions.Put(token, &e.session)
	e.vendor.block = make(chan struct{})
	var r *app.Reply
	var wg sync.WaitGroup
	wg.Go(func() { r = e.post(t, request) })
	require.Eventually(t, func() bool { return e.vendor.sent.Load() == 1 }, 5*time.Second, time.Millisecond)
	start := time.Now()

	close(done)
	wg.Wait()
	close(e.vendor.block)

	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Equal(t, 502, r.Status)
	require.Eventually(t, func() bool { _, _, _, ended := e.calls.Record("m-1"); return ended }, 5*time.Second, 5*time.Millisecond)
}

// When the vendor keeps failing, comparison and probation runs pause and
// primary runs fail at once (spec Sections 6.2 and 12.1).
func TestComparisonRunsPauseFirstWhenTheVendorFails(t *testing.T) {
	e := newEnv(t, func(o *app.Options) {
		o.Vendor = limits.Settings{Slots: 10, Failures: 1, CoolDown: time.Minute}
		o.Backoff = nil
	})
	e.vendor.answers = []*app.Answer{{Status: 529, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)}}
	require.Equal(t, 529, e.post(t, request).Status, "one failure degrades the vendor")
	shadow := e.session
	shadow.Run, shadow.Class = "run-shadow", cost.Shadow
	e.sessions.Put("shadow-token", &shadow)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()

	paused := e.gw.Messages(ctx, &app.Inbound{Method: "POST", Path: "/v1/messages", Token: "shadow-token", Header: headers(), Body: []byte(request)})
	waited := time.Since(start)
	failed := e.post(t, request)

	assert.GreaterOrEqual(t, waited, 100*time.Millisecond, "it waited until its request ended")
	assert.Equal(t, 502, paused.Status)
	assert.Contains(t, string(paused.Body), "before the call was made")
	assert.Equal(t, 503, failed.Status)
	assert.Equal(t, "overloaded_error", errorType(t, failed))
	assert.Equal(t, 1, e.vendor.attempts())
}

// Options that cannot hold are refused.
func TestOptionsMustHold(t *testing.T) {
	for name, change := range map[string]func(*app.Options){
		"no cap":       func(o *app.Options) { o.Limits.DailyCap = 0 },
		"no version":   func(o *app.Options) { o.Version = "" },
		"no body":      func(o *app.Options) { o.MaxBodyBytes = 0 },
		"no tokens":    func(o *app.Options) { o.MaxTokens = 0 },
		"no reference": func(o *app.Options) { o.Reference = 0 },
		"a bad card":   func(o *app.Options) { o.Cards = map[string]cost.Card{model: {}} },
		"no refusals":  func(o *app.Options) { o.MaxRefusals = 0 },
		"a bad wait":   func(o *app.Options) { o.Backoff = []time.Duration{-time.Second} },
		"a dear token": func(o *app.Options) { o.Reference = cost.MaxRate + 1 },
		"no last wait": func(o *app.Options) { o.LastCallWait = 0 },
	} {
		o := options()
		change(&o)

		_, err := app.New(&app.Ports{Now: time.Now}, &o)

		require.Error(t, err, name)
	}
}

// A call that goes on after its request was answered keeps its session's
// one call in flight: the session's next request, in the next round, waits
// for it to end, and the vendor never has two of the session's calls at
// once. A request sent while another waits is refused.
func TestTheNextRoundWaitsForTheCallTheLastRoundLeft(t *testing.T) {
	e := newEnv(t)
	e.session.Deadline = time.Now().Add(100 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	e.vendor.block = make(chan struct{})
	require.Equal(t, 504, e.post(t, request).Status, "the round ended before the model answered")
	e.session.Round, e.session.Deadline = 3, time.Now().Add(5*time.Second)
	e.sessions.Put(token, &e.session)

	var next *app.Reply
	var wg sync.WaitGroup
	wg.Go(func() { next = e.post(t, request) })
	assert.Never(t, func() bool { return e.vendor.sent.Load() > 1 }, 100*time.Millisecond, 5*time.Millisecond, "the next call waits")
	busy := e.post(t, request)
	close(e.vendor.block)
	wg.Wait()

	assert.Equal(t, 429, busy.Status, "one request waits at a time")
	assert.Equal(t, 200, next.Status, string(next.Body))
	assert.Equal(t, int32(1), e.vendor.most.Load(), "never two of the session's calls at once")
	assert.Equal(t, 2, e.vendor.attempts())
}

// A request that ended before its call could start is not sent: nobody
// would read the answer. It is recorded, and costs nothing.
func TestARequestThatEndedIsNotSent(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	r := e.sendWith(ctx, token, request)

	assert.Equal(t, 502, r.Status)
	assert.Zero(t, e.vendor.attempts())
	_, o := e.last(t)
	assert.Equal(t, app.StatusError, o.Status)
	assert.Zero(t, o.Weighted)
}

// A comparison run's call paused while the vendor is failing waits no
// longer than its round.
func TestAPausedCallEndsWithItsRound(t *testing.T) {
	e := newEnv(t, func(o *app.Options) {
		o.Vendor = limits.Settings{Slots: 10, Failures: 1, CoolDown: 3 * time.Second}
		o.Backoff = nil
	})
	e.vendor.answers = []*app.Answer{{Status: 529, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)}}
	require.Equal(t, 529, e.post(t, request).Status, "one failure degrades the vendor")
	shadow := e.session
	shadow.Run, shadow.Class, shadow.Deadline = "run-shadow", cost.Shadow, time.Now().Add(100*time.Millisecond)
	e.sessions.Put("shadow-token", &shadow)
	start := time.Now()

	r := e.send(t, "shadow-token", request)

	assert.Less(t, time.Since(start), 2*time.Second, "not the three seconds the vendor is paused for")
	assert.Equal(t, 504, r.Status)
	assert.Equal(t, 1, e.vendor.attempts())
}

// A session lasts across rounds (spec Section 5.2): a thinking block the
// gateway gave it in one round may come back in a later one.
func TestAThinkingBlockGoesBackInALaterRound(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, 200, e.post(t, request).Status)
	e.session.Round++
	e.sessions.Put(token, &e.session)

	r := e.post(t, next)

	assert.Equal(t, 200, r.Status, string(r.Body))
}

// The gateway keeps what it gave a session in use, however many other
// sessions come and go: past MaxSessions it forgets the session used least
// recently, never all at once.
func TestASessionInUseKeepsItsThinkingBlocks(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, 200, e.post(t, request).Status)
	for i := range app.MaxSessions {
		other := e.session
		other.Run = fmt.Sprintf("other-%d", i)
		tok := fmt.Sprintf("token-%d", i)
		e.sessions.Put(tok, &other)
		e.stray(t, tok)
		if i == app.MaxSessions/2 {
			e.stray(t, token)
		}
	}

	r := e.post(t, next)

	assert.Equal(t, 200, r.Status, string(r.Body))
}

// A session's refusals are counted before they are recorded, so that the
// cap holds for requests sent at once.
func TestRefusalsAreCappedWhenSentAtOnce(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.MaxRefusals = 3 })
	release := e.calls.HoldBegins()
	var answered atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			e.stray(t, token)
			answered.Add(1)
		})
	}
	require.Eventually(t, func() bool { return int(answered.Load())+e.calls.Held() == 50 }, 5*time.Second, time.Millisecond)

	release()
	wg.Wait()

	assert.Len(t, e.calls.Records(), 4, "three refusals and the shut-out")
}

// A request the vendor refuses as invalid counts against the session's
// round, as the gateway's own refusals do.
func TestTheVendorsRefusalsCountAgainstTheSession(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.MaxRefusals = 3 })
	e.vendor.answers = []*app.Answer{{Status: 400, Body: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)}}
	for range 3 {
		require.Equal(t, 400, e.post(t, request).Status)
	}

	shut := e.post(t, request)

	assert.Equal(t, 429, shut.Status)
	assert.Equal(t, 3, e.vendor.attempts())
}

// A call counts at its worst case until its outcome is recorded, in its
// run's spend and the day's, so that a budget holds while calls are in
// flight, and if an outcome is never recorded.
func TestACallCountsItsWorstCaseUntilItsOutcomeIsRecorded(t *testing.T) {
	e := newEnv(t)
	e.vendor.block = make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() { e.post(t, request) })
	require.Eventually(t, func() bool { return e.vendor.sent.Load() == 1 }, 5*time.Second, time.Millisecond)

	inFlight, err := e.calls.Spent(t.Context(), "run-1")
	require.NoError(t, err)
	today, err := e.calls.SpentSince(t.Context(), cost.DayStart(t0))
	require.NoError(t, err)
	close(e.vendor.block)
	wg.Wait()
	after, err := e.calls.Spent(t.Context(), "run-1")
	require.NoError(t, err)

	rec, _ := e.last(t)
	worst := cost.Weighted(card.Spend(cost.Estimate(rec.RequestBytes, 1024)), 100_000)
	assert.Equal(t, worst, rec.Estimate)
	assert.Equal(t, worst, inFlight)
	assert.Equal(t, worst, today)
	assert.Equal(t, int64(5000), after)
}

// An outcome that could not be recorded is tried again until it is; the
// answer stays withheld.
func TestAnOutcomeThatCannotBeRecordedIsTriedAgain(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.RecordTimeout = 10 * time.Millisecond })
	e.calls.SetFailEnd(errors.New("the store is down"))

	r := e.post(t, request)
	e.calls.SetFailEnd(nil)

	assert.Equal(t, 503, r.Status)
	require.Eventually(t, func() bool { _, _, _, ended := e.calls.Record("m-1"); return ended }, 5*time.Second, 5*time.Millisecond)
	_, o := e.last(t)
	assert.Equal(t, app.StatusOK, o.Status)
	assert.Equal(t, int64(5000), o.Weighted)
}

// A wait between attempts ends when the call's time is up, in a round with
// no end too.
func TestNoWaitOutlastsTheCallsTimeout(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.VendorTimeout = 100 * time.Millisecond })
	e.session.Deadline = time.Time{}
	e.sessions.Put(token, &e.session)
	e.vendor.answers = []*app.Answer{{Status: 529, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`), RetryAfter: 3 * time.Second}}
	start := time.Now()

	r := e.post(t, request)

	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Equal(t, 529, r.Status)
}

// The vendor takes as many calls at once as it has slots: it serves agents
// only, so none is kept for the platform's reads.
func TestTheVendorTakesAsManyCallsAtOnceAsItHasSlots(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.Vendor.Slots = 2 })
	e.vendor.block = make(chan struct{})
	other := e.session
	other.Run = "run-2"
	e.sessions.Put("other-token", &other)
	var wg sync.WaitGroup
	wg.Go(func() { e.post(t, request) })
	wg.Go(func() { e.send(t, "other-token", request) })

	require.Eventually(t, func() bool { return e.vendor.sent.Load() == 2 }, 5*time.Second, time.Millisecond)
	close(e.vendor.block)
	wg.Wait()
}

// A request the gateway does not forward is recorded short, whatever
// stopped it: the allowlist, the budget, or the request ending before its
// call could start.
func TestARequestNotForwardedIsRecordedShort(t *testing.T) {
	long := strings.Replace(request, "Find why checkout fails.", strings.Repeat("x", 20000), 1)
	e := newEnv(t)
	e.session.Budget = 0
	e.sessions.Put(token, &e.session)
	require.Equal(t, 403, e.post(t, long).Status)
	e.session.Budget = 2_000_000
	e.sessions.Put(token, &e.session)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.Equal(t, 502, e.sendWith(ctx, token, long).Status)

	records := e.calls.Records()
	require.Len(t, records, 2)
	for _, rec := range records {
		assert.LessOrEqual(t, len(rec.Request), 4096+3, rec.ID)
		assert.True(t, strings.HasPrefix(rec.Request, `{"max_tokens":1024,"messages":[{"content":"xxx`), rec.ID)
		assert.Len(t, rec.RequestDigest, 64, rec.ID)
		assert.Zero(t, rec.Estimate, rec.ID)
	}
	assert.Zero(t, e.vendor.attempts())
}

// Requests the gateway drops because they ended before their call could
// start count against the session's round, as refusals do, so that a
// sandbox cannot fill the audit store with them.
func TestRequestsDroppedAtOnceCountAgainstTheSession(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.MaxRefusals = 3 })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 3 {
		require.Equal(t, 502, e.sendWith(ctx, token, request).Status)
	}

	shut := e.sendWith(ctx, token, request)
	again := e.sendWith(ctx, token, request)

	assert.Equal(t, 429, shut.Status)
	assert.Equal(t, 429, again.Status)
	assert.Len(t, e.calls.Records(), 4, "three dropped and the shut-out")
}

// A request that ends while its call is being recorded is not sent: its
// record says the call was not made, which costs nothing.
func TestARequestThatEndsWhileItsCallIsRecordedIsNotSent(t *testing.T) {
	e := newEnv(t)
	release := e.calls.HoldBegins()
	ctx, cancel := context.WithCancel(t.Context())
	var r *app.Reply
	var wg sync.WaitGroup
	wg.Go(func() { r = e.sendWith(ctx, token, request) })
	require.Eventually(t, func() bool { return e.calls.Held() == 1 }, 5*time.Second, time.Millisecond)

	cancel()
	release()
	wg.Wait()

	assert.Equal(t, 502, r.Status)
	assert.Zero(t, e.vendor.attempts())
	rec, o := e.last(t)
	assert.Equal(t, app.StatusError, o.Status)
	assert.Zero(t, o.Weighted)
	assert.Positive(t, rec.Estimate)
	spent, err := e.calls.Spent(t.Context(), "run-1")
	require.NoError(t, err)
	assert.Zero(t, spent, "a call not made costs nothing")
}

// block makes the vendor hold every attempt from now on until hold is
// closed, or answer at once when hold is nil.
func (v *vendor) hold(hold chan struct{}) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.block = hold
}

// A call that goes on after its request was answered holds its session for
// at most LastCallWait: past it, once the vendor has answered another call
// since, the session's next request goes on beside it, and the call that
// hangs counts at its worst case until it ends.
func TestANextCallGoesOnBesideACallThatHangs(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.LastCallWait = 50 * time.Millisecond })
	e.session.Deadline = time.Now().Add(100 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	hang := make(chan struct{})
	defer close(hang)
	e.vendor.hold(hang)
	require.Equal(t, 504, e.post(t, request).Status)
	e.vendor.hold(nil)
	other := e.session
	other.Run, other.Deadline = "run-2", time.Now().Add(5*time.Second)
	e.sessions.Put("other-token", &other)
	require.Equal(t, 200, e.send(t, "other-token", request).Status, "the vendor answers others")
	e.session.Round, e.session.Deadline = 3, time.Now().Add(5*time.Second)
	e.sessions.Put(token, &e.session)
	start := time.Now()

	r := e.post(t, request)

	assert.Equal(t, 200, r.Status, string(r.Body))
	assert.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond, "it waited for the call first")
	assert.Equal(t, int32(2), e.vendor.most.Load())
	first, _, _, _ := e.calls.Record("m-1")
	spent, err := e.calls.Spent(t.Context(), "run-1")
	require.NoError(t, err)
	assert.Equal(t, first.Estimate+5000, spent, "the call that hangs counts at its worst case")
}

// While the vendor answers no call, a session's next request does not go
// on beside the call that hangs, which would only add another; an answer
// from before the call began does not count. It goes on once the vendor
// answers again.
func TestANextCallWaitsWhileTheVendorAnswersNoOne(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.LastCallWait = 50 * time.Millisecond })
	require.Equal(t, 200, e.post(t, request).Status, "an answer before the call that hangs")
	e.session.Deadline = time.Now().Add(100 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	hang := make(chan struct{})
	defer close(hang)
	e.vendor.hold(hang)
	require.Equal(t, 504, e.post(t, request).Status)
	e.session.Round, e.session.Deadline = 3, time.Now().Add(5*time.Second)
	e.sessions.Put(token, &e.session)
	other := e.session
	other.Run = "run-2"
	e.sessions.Put("other-token", &other)
	var next *app.Reply
	var wg sync.WaitGroup
	wg.Go(func() { next = e.post(t, request) })
	assert.Never(t, func() bool { return e.vendor.sent.Load() > 2 }, 300*time.Millisecond, 5*time.Millisecond, "no call beside one that hangs")

	e.vendor.hold(nil)
	require.Equal(t, 200, e.send(t, "other-token", request).Status)
	wg.Wait()

	assert.Equal(t, 200, next.Status, string(next.Body))
}

// A session never has more than two calls at the vendor: one that hangs,
// and one beside it. When both hang, its next request waits until one
// ends.
func TestASessionHasAtMostTwoCallsAtTheVendor(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.LastCallWait = 20 * time.Millisecond })
	other := e.session
	other.Run = "run-2"
	e.sessions.Put("other-token", &other)
	hang := make(chan struct{})
	for round := 3; round <= 4; round++ {
		e.vendor.hold(hang)
		e.session.Round, e.session.Deadline = round, time.Now().Add(100*time.Millisecond)
		e.sessions.Put(token, &e.session)
		require.Equal(t, 504, e.post(t, request).Status, "round %d", round)
		e.vendor.hold(nil)
		require.Equal(t, 200, e.send(t, "other-token", request).Status, "the vendor answers others")
	}
	e.session.Round, e.session.Deadline = 5, time.Now().Add(5*time.Second)
	e.sessions.Put(token, &e.session)

	var third *app.Reply
	var wg sync.WaitGroup
	wg.Go(func() { third = e.post(t, request) })
	assert.Never(t, func() bool { return e.vendor.sent.Load() > 4 }, 200*time.Millisecond, 5*time.Millisecond,
		"the session's third call waits while two hang")
	close(hang)
	wg.Wait()

	assert.Equal(t, 200, third.Status, string(third.Body))
}

// Drain waits for the calls that outlived their requests to end and their
// outcomes to be recorded, as the gateway stops, or for its context.
func TestDrainWaitsForCallsThatOutliveTheirRequests(t *testing.T) {
	e := newEnv(t)
	e.session.Deadline = time.Now().Add(100 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	e.vendor.block = make(chan struct{})
	require.Equal(t, 504, e.post(t, request).Status)
	short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	early := e.gw.Drain(short)
	close(e.vendor.block)
	err := e.gw.Drain(t.Context())

	require.ErrorIs(t, early, context.DeadlineExceeded)
	require.NoError(t, err)
	_, o, _, ended := e.calls.Record("m-1")
	require.True(t, ended)
	assert.Equal(t, app.StatusOK, o.Status)
}

// A request's record holds it redacted, a thinking block's text too, while
// the vendor gets the block as the gateway gave it; the record's digest is
// of the record, so that it cannot give back what the record hides.
func TestAThinkingBlockIsRecordedRedacted(t *testing.T) {
	e := newEnv(t)
	thought := `{"type": "thinking", "thinking": "the key is sk-hhhhhhhh", "signature": "EqQBCkgIBRAB"}`
	answer := ok()
	answer.Body = []byte(strings.Replace(string(answer.Body), thinking, thought, 1))
	e.vendor.answers = []*app.Answer{answer, ok()}
	require.Equal(t, 200, e.post(t, request).Status)

	r := e.post(t, strings.Replace(next, thinking, thought, 1))

	require.Equal(t, 200, r.Status, string(r.Body))
	assert.Contains(t, e.vendor.bodies[1], "sk-hhhhhhhh", "the vendor gets the block unchanged")
	rec, _ := e.last(t)
	assert.NotContains(t, rec.Request, "sk-hhhhhhhh")
	sum := sha256.Sum256([]byte(rec.Request))
	assert.Equal(t, hex.EncodeToString(sum[:]), rec.RequestDigest)
}

// Drain waits for the requests in the gateway's hands as well, a request
// waiting for its session's last call among them, and the gateway takes no
// new request once it drains.
func TestDrainWaitsForRequestsInHandAndRefusesNewOnes(t *testing.T) {
	e := newEnv(t)
	e.session.Deadline = time.Now().Add(100 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	first := make(chan struct{})
	e.vendor.block = first
	require.Equal(t, 504, e.post(t, request).Status)
	second := make(chan struct{})
	e.vendor.mu.Lock()
	e.vendor.block = second
	e.vendor.mu.Unlock()
	e.session.Round, e.session.Deadline = 3, time.Now().Add(5*time.Second)
	e.sessions.Put(token, &e.session)
	other := e.session
	other.Run = "run-2"
	e.sessions.Put("other-token", &other)
	var next *app.Reply
	var wg sync.WaitGroup
	wg.Go(func() { next = e.post(t, request) })
	require.Eventually(t, func() bool { return e.ports.IDs.(*ids).n.Load() == 2 }, 5*time.Second, time.Millisecond, "the next request is in hand")
	drained := make(chan error, 1)
	go func() { drained <- e.gw.Drain(t.Context()) }()
	require.Eventually(t, func() bool { return e.stray(t, "other-token").Status == 503 }, 5*time.Second, time.Millisecond, "no new request")

	close(first)
	require.Eventually(t, func() bool { return e.vendor.sent.Load() == 2 }, 5*time.Second, time.Millisecond)
	select {
	case err := <-drained:
		t.Fatalf("Drain returned (%v) with a call at the vendor", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(second)
	wg.Wait()

	require.NoError(t, <-drained)
	assert.Equal(t, 200, next.Status, string(next.Body))
}

// A request dropped while its call is being recorded counts against the
// session's round, as one dropped before does.
func TestRequestsDroppedWhileRecordedCountAgainstTheSession(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.MaxRefusals = 3 })
	for range 3 {
		release := e.calls.HoldBegins()
		ctx, cancel := context.WithCancel(t.Context())
		var r *app.Reply
		var wg sync.WaitGroup
		wg.Go(func() { r = e.sendWith(ctx, token, request) })
		require.Eventually(t, func() bool { return e.calls.Held() == 1 }, 5*time.Second, time.Millisecond)
		cancel()
		release()
		wg.Wait()
		require.Equal(t, 502, r.Status)
	}

	shut := e.post(t, request)

	assert.Equal(t, 429, shut.Status)
	assert.Zero(t, e.vendor.attempts())
}

// A call recorded and then not made, whose outcome could not be recorded,
// has its outcome tried again, so that it does not count at its worst case
// for good.
func TestANotMadeOutcomeIsTriedAgain(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.RecordTimeout = 50 * time.Millisecond })
	release := e.calls.HoldBegins()
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { e.sendWith(ctx, token, request) })
	require.Eventually(t, func() bool { return e.calls.Held() == 1 }, 5*time.Second, time.Millisecond)
	cancel()
	e.calls.SetFailEnd(errors.New("the store is down"))
	release()
	wg.Wait()
	e.calls.SetFailEnd(nil)

	require.Eventually(t, func() bool { _, _, _, ended := e.calls.Record("m-1"); return ended }, 5*time.Second, 5*time.Millisecond)
	spent, err := e.calls.Spent(t.Context(), "run-1")
	require.NoError(t, err)
	assert.Zero(t, spent)
}
```

Append to `internal/actions/limits/limits_test.go`:

```go
// A server that serves agents only, as the model vendor does, reserves no
// slots for platform reads; comparison and probation runs still hold at
// most half.
func TestAServerOfAgentsOnlyReservesNothing(t *testing.T) {
	settings := limits.Defaults
	settings.AgentsOnly = true
	s := limits.New(settings, nil, time.Now).Server("model vendor")

	take(t, s, limits.Shadow, 5)
	assert.True(t, waits(t, s, limits.Shadow))
	take(t, s, limits.Primary, 5)
	assert.True(t, waits(t, s, limits.Primary))
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/llm/app/ ./internal/actions/limits/`
Expected: FAIL: `internal/llm/app` fails to set up (`no required module provides package .../internal/llm/memory`), and `internal/actions/limits` fails to build (`settings.AgentsOnly undefined` in `limits_test.go`)

- [ ] **Step 3: Let a server serve agents only**

Replace `internal/actions/limits/limits.go` with this version, which adds `Settings.AgentsOnly`:

```go
// Package limits keeps each MCP server's limits (spec Section 6.2): how many
// calls run at once, the share that comparison and probation runs may hold,
// the capacity reserved for platform reads, and the degraded mark. While a
// server is degraded, primary runs' calls fail fast, comparison and probation
// runs pause, and platform reads go on.
package limits

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrDegraded is returned for a primary run's call to a degraded server.
var ErrDegraded = errors.New("limits: the server is degraded")

// minSlots is the fewest slots a server has: one reserved for platform
// reads, and one for agents.
const minSlots = 2

// Class says whose call takes a slot.
type Class int

// Classes of calls.
const (
	// Primary is a call by an agent of a primary run, of either arm.
	Primary Class = iota
	// Shadow is a call by an agent of a comparison or probation run.
	Shadow
	// Platform is one of the platform's own reads: candidates, facts,
	// routing and identity.
	Platform
)

// Settings are the limits of spec Section 15.
type Settings struct {
	// Slots is how many calls run at once on one server.
	Slots int
	// Failures is how many consecutive connection failures degrade a server.
	Failures int
	// CoolDown is how long a server stays degraded.
	CoolDown time.Duration
	// AgentsOnly, when set, reserves no slots for platform reads: the
	// servers serve agents' calls only, as the model vendor does.
	AgentsOnly bool
}

// Defaults are the spec's defaults: 10 calls at once, degraded after 3
// consecutive connection failures for 60 s.
var Defaults = Settings{Slots: 10, Failures: 3, CoolDown: 60 * time.Second}

// Limits holds the limits of every server.
type Limits struct {
	settings Settings
	slots    map[string]int
	now      func() time.Time

	mu      sync.Mutex
	servers map[string]*Server
}

// New returns limits with the given settings. slots overrides Slots for the
// servers it names, as a load trial may set them; now is the clock.
func New(settings Settings, slots map[string]int, now func() time.Time) *Limits {
	return &Limits{settings: settings, slots: slots, now: now, servers: map[string]*Server{}}
}

// Server returns the limits of the named server.
func (l *Limits) Server(name string) *Server {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.servers[name]
	if s == nil {
		n := l.settings.Slots
		if v, ok := l.slots[name]; ok {
			n = v
		}
		n = max(n, minSlots)
		reserved := max(1, n/5)
		if l.settings.AgentsOnly {
			reserved = 0
		}
		s = &Server{
			slots: n, reserved: reserved, failuresToDegrade: l.settings.Failures, coolDown: l.settings.CoolDown,
			now: l.now, inUse: map[Class]int{}, freed: make(chan struct{}),
		}
		l.servers[name] = s
	}
	return s
}

// Server is one server's limits.
//
// Platform reads may use every slot. Agents may use all but the reserved
// ones: a fifth of the slots, at least one, or none on a server of agents
// only. Comparison and probation runs may hold at most half of the slots,
// within the agents' share.
type Server struct {
	slots             int
	reserved          int
	failuresToDegrade int
	coolDown          time.Duration
	now               func() time.Time

	mu            sync.Mutex
	inUse         map[Class]int
	freed         chan struct{} // closed when a slot is released
	failures      int
	degradedUntil time.Time
}

// Acquire takes a slot for a call of class c, waiting until one is free or
// ctx ends. While the server is degraded, a primary run's call fails at once
// with ErrDegraded, a comparison or probation run's call waits for the
// cool-down to end, and a platform read goes on. Call release when the call
// ends.
func (s *Server) Acquire(ctx context.Context, c Class) (release func(), err error) {
	for {
		s.mu.Lock()
		degraded := s.now().Before(s.degradedUntil)
		if degraded && c == Primary {
			s.mu.Unlock()
			return nil, ErrDegraded
		}
		paused := degraded && c == Shadow
		if !paused && s.free(c) {
			s.inUse[c]++
			s.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { s.release(c) }) }, nil
		}
		freed, wait := s.freed, s.degradedUntil.Sub(s.now())
		s.mu.Unlock()
		if err := s.wait(ctx, freed, paused, wait); err != nil {
			return nil, err
		}
	}
}

// wait waits until a slot is freed, a pause ends or ctx ends.
func (s *Server) wait(ctx context.Context, freed <-chan struct{}, paused bool, pause time.Duration) error {
	var coolDown <-chan time.Time
	if paused {
		t := time.NewTimer(pause)
		defer t.Stop()
		coolDown = t.C
	}
	select {
	case <-freed:
	case <-coolDown:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// free reports whether a call of class c may take a slot now.
func (s *Server) free(c Class) bool {
	total := s.inUse[Primary] + s.inUse[Shadow] + s.inUse[Platform]
	agents := s.inUse[Primary] + s.inUse[Shadow]
	switch c {
	case Platform:
		return total < s.slots
	case Shadow:
		return total < s.slots && agents < s.slots-s.reserved && s.inUse[Shadow] < s.slots/2
	default:
		return total < s.slots && agents < s.slots-s.reserved
	}
}

func (s *Server) release(c Class) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inUse[c]--
	close(s.freed)
	s.freed = make(chan struct{})
}

// Failed records a call that could not reach the server, once per call after
// its retries. Enough of them in a row degrade the server for the cool-down.
func (s *Server) Failed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	if s.failures >= s.failuresToDegrade {
		s.failures = 0
		s.degradedUntil = s.now().Add(s.coolDown)
	}
}

// Succeeded records a call that reached the server, which ends a run of
// failures.
func (s *Server) Succeeded() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = 0
}

// Degraded reports whether the server is degraded now.
func (s *Server) Degraded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now().Before(s.degradedUntil)
}
```

- [ ] **Step 4: Declare the ports**

Create `internal/llm/app/ports.go`:

```go
// Package app holds the LLM context's use case, the LLM gateway: the only
// route from an agent's sandbox to the model vendor (spec Sections 2, 5.6,
// 7, 9.3 and 13.6; ADR-007). It declares the ports it needs.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// Errors that ports return. Callers match them with errors.Is.
var (
	// ErrNoSession is returned by Sessions for a token that names no live
	// session: unknown, expired, or of a run that stopped.
	ErrNoSession = errors.New("app: no such session")
	// ErrNotSent marks an attempt whose request never reached the vendor:
	// another attempt may succeed, and costs nothing twice.
	ErrNotSent = errors.New("app: the request was not sent")
	// ErrLost marks an attempt whose request reached the vendor and whose
	// answer did not come back: the vendor may have charged for it, so it is
	// never tried again, and its cost is counted as its worst case.
	ErrLost = errors.New("app: the answer was lost")
)

// Session is an agent's session, as the coordinator runs it (Plan 7).
type Session struct {
	Run   string
	Agent string
	// Commit is the configuration revision the item locked.
	Commit string
	// Round is the run's current round, and Deadline its end: no call
	// starts after it, and no attempt is tried again that could not end
	// before it.
	Round    int
	Deadline time.Time
	// Class says whether the run is cut first as spend nears the daily cap.
	Class cost.Class
	// Budget is the run's token budget, in cost-weighted tokens, as the
	// coordinator sets it now.
	Budget int64
	// Done is closed when the run stops or the session ends: the session's
	// requests are answered at once, and no attempt is tried again.
	Done <-chan struct{}
}

// Sessions finds the session an LLM gateway token authenticates. Tokens
// are issued by the coordinator, one per agent session, scoped to its run
// and agent, and expire when the run stops (spec Section 2).
type Sessions interface {
	Session(ctx context.Context, token string) (Session, error)
}

// Revisions returns a locked configuration revision by full commit id.
type Revisions interface {
	At(ctx context.Context, commit string) (*config.Revision, error)
}

// Answer is the vendor's answer to one attempt.
type Answer struct {
	Status int
	Body   []byte
	// RequestID is the vendor's id of the request, if it gave one.
	RequestID string
	// RetryAfter is how long the vendor asked the next attempt to wait.
	RetryAfter time.Duration
}

// Vendor sends a checked request to the vendor's Messages API, with the
// deployment's key and nothing of the client's. An attempt that fails
// before or after the request reached the vendor is ErrNotSent or ErrLost.
type Vendor interface {
	Send(ctx context.Context, body []byte) (*Answer, error)
}

// Status is how a model call ended.
type Status string

// Statuses of a model call.
const (
	StatusOK      Status = "ok"
	StatusRefused Status = "refused"
	// StatusError is a call the vendor did not answer with a completion,
	// and did not charge for.
	StatusError Status = "error"
	// StatusUnknown is a call the vendor may have charged for without an
	// answer the gateway could read: its cost is counted as its worst case.
	StatusUnknown Status = "unknown"
)

// CallRecord is what the audit store keeps of a model call before it is
// forwarded.
type CallRecord struct {
	ID    string
	Run   string
	Agent string
	Round int
	Class cost.Class
	// Model is the model the request named, "" when it could not be read.
	Model string
	// Request is the request as it was forwarded, redacted, its thinking
	// blocks too, which go to the vendor as the gateway gave them; a call
	// recorded and then not made keeps it so as well. For a request not
	// forwarded, it is at most the first 4 KiB of its JSON with every
	// string, key and number redacted, or "" when it could not be read.
	Request string
	// RequestBytes is the size of the request as it was sent or received.
	// RequestDigest is the SHA-256, in hex, of the request as Request
	// records it, in full: what was forwarded, redacted, or the redacted
	// JSON of a request not forwarded; "" when there is none. It never
	// gives back what the record hides.
	RequestBytes  int64
	RequestDigest string
	// Estimate is the call's worst case in cost-weighted tokens, which its
	// run's spend and the day's count until its outcome is recorded; 0 for
	// a request that is not forwarded.
	Estimate int64
	At       time.Time
}

// Outcome is what the audit store keeps of a model call when it ends.
type Outcome struct {
	ID     string
	Status Status
	// Reason says why a call was refused or failed, redacted.
	Reason string
	// Completion is the answer's record, redacted, for a call that
	// succeeded.
	Completion string
	Usage      cost.Usage
	// Spend is the call's cost in pico-USD, and Weighted the same in
	// cost-weighted tokens.
	Spend, Weighted int64
	// Attempts counts the attempts made to reach the vendor.
	Attempts int
	// Details is how the vendor served the answer, as JSON, "" when there
	// is no answer.
	Details string
	At      time.Time
}

// Calls is the audit store's record of model calls (spec Sections 9.3 and
// 9.4), which the budgets are counted from. A call counts what its outcome
// says it cost once that is recorded, and its estimate until then: while it
// is in flight, and for good if its outcome never is.
type Calls interface {
	// Begin records a call and its request. The record is committed when
	// Begin returns, so that it exists before the call reaches the vendor.
	Begin(ctx context.Context, rec *CallRecord) error
	// End records how a call ended, with its completion. Recording the
	// same outcome again changes nothing.
	End(ctx context.Context, o *Outcome) error
	// Spent returns the cost-weighted tokens of a run's calls.
	Spent(ctx context.Context, run string) (int64, error)
	// SpentSince returns the cost-weighted tokens of the tenant's calls
	// that began at or after since.
	SpentSince(ctx context.Context, since time.Time) (int64, error)
}
```

- [ ] **Step 5: Serve a request**

Create `internal/llm/app/gateway.go`:

```go
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/llm/completion"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/llm/prompt"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// Path is the one endpoint the gateway offers.
const Path = "/v1/messages"

// serviceTier is the tier every request is sent at: the standard one, which
// the models' cards price.
const serviceTier = "standard_only"

// maxRecordedRequest caps what the record keeps of a refused request.
const maxRecordedRequest = 4096

// MaxSessions bounds the sessions the gateway keeps state for: past it, it
// forgets the session used least recently, whose thinking blocks are then
// refused if they come back.
const MaxSessions = 1024

// maxThinking bounds the thinking blocks the gateway keeps for a session:
// past it, it forgets the oldest.
const maxThinking = 1024

// Options configure the gateway (spec Section 15).
type Options struct {
	// MaxBodyBytes caps a request, which bounds what one call can cost.
	MaxBodyBytes int
	// MaxTokens caps a request's max_tokens.
	MaxTokens int64
	// Version is the anthropic-version every request carries.
	Version string
	// Limits are the deployment's daily cap and shadow cut.
	Limits cost.Limits
	// Cards are the models' prices. A model without a card is refused.
	Cards map[string]cost.Card
	// Reference is the price of one cost-weighted token, in pico-USD.
	Reference int64
	// InferenceGeo, when set, is the region every request is served in.
	InferenceGeo string
	// Vendor are the vendor's limits: calls at once, the share of
	// comparison and probation runs, and when it is degraded. The vendor
	// serves agents only, so no slot is kept for platform reads.
	Vendor limits.Settings
	// Backoff are the waits between attempts at a failure another attempt
	// may get past; the vendor's retry-after wins when it is longer.
	Backoff []time.Duration
	// VendorTimeout bounds a call to the vendor, which goes on after its
	// request has been answered so that its cost is recorded.
	VendorTimeout time.Duration
	// RecordTimeout bounds each read and write of the record, which run
	// apart from the request's context, and is the wait before an outcome
	// that could not be recorded is tried again.
	RecordTimeout time.Duration
	// MaxRefusals caps the refused requests a session's round records;
	// past it, the session is shut out until the next round.
	MaxRefusals int
	// LastCallWait is how long a session's request waits for a call that
	// goes on after its own request was answered, before it goes on beside
	// it.
	LastCallWait time.Duration
}

// IDs makes call ids.
type IDs interface {
	NewID() string
}

// Ports are what the gateway reaches through.
type Ports struct {
	Sessions  Sessions
	Revisions Revisions
	Vendor    Vendor
	Calls     Calls
	IDs       IDs
	Now       func() time.Time
}

// Inbound is one request a runtime sent.
type Inbound struct {
	Method, Path string
	// Query is the request's query string.
	Query string
	// Token is the session's token, as the request carried it.
	Token  string
	Header map[string][]string
	Body   []byte
	// Length is the size the request declared, if it declared one: the
	// record of a request larger than the gateway reads keeps it.
	Length int64
}

// Reply is the gateway's answer.
type Reply struct {
	Status int
	Header map[string]string
	Body   []byte
}

// Gateway is the LLM gateway: the only route from an agent's sandbox to
// the model vendor (spec Section 2). It forwards a request only when the
// session is live and in its round, the request is within the allowlist
// and the agent's configuration, the run has budget left and the day's
// spend allows it (spec Sections 5.6, 7 and 9.3). It records every request
// a session sends, refused or not, and records a forwarded one before it
// reaches the vendor, redacted as it was sent (spec Section 9.4). A session
// has one request in the gateway's hands at a time, and at most two calls
// at the vendor: a call that goes on after its request was answered holds
// the session for LastCallWait. The gateway holds its state in memory, so
// one instance is active at a time, as the gateway lease ensures (spec
// Section 8.2).
type Gateway struct {
	p      Ports
	opts   Options
	vendor *limits.Server

	mu       sync.Mutex
	lanes    map[sessionKey]*lane
	sessions map[sessionKey]*sessionState
	tick     uint64
	// busy counts the requests in the gateway's hands and the calls that
	// outlived them, until their outcomes are recorded. Once closing is
	// set, the gateway takes no request, and drained is closed when busy
	// falls to zero.
	busy    int
	closing bool
	drained chan struct{}
	// heard is when the vendor last answered an attempt, in Unix
	// nanoseconds.
	heard atomic.Int64
}

type sessionKey struct{ run, agent string }

// lane is a session's place at the gateway: the request in its hands, if
// any, and the calls that go on after their requests were answered.
type lane struct {
	// held is set while a request is in the gateway's hands, and waiting
	// while one waits for a call that goes on.
	held, waiting bool
	// going counts the calls that go on after their requests were
	// answered, and since is when the latest of them started.
	going int
	since time.Time
	// ended is closed, and made anew, when one of them ends.
	ended chan struct{}
}

// flight is one request in the gateway's hands and the call it made: open
// until the request is answered, calling while the call goes on, which
// started at started.
type flight struct {
	open, calling bool
	started       time.Time
}

// sessionState is what the gateway keeps of a session: the thinking blocks
// it gave it, oldest first, and the refusals of its current round.
type sessionState struct {
	used     uint64
	thinking map[[32]byte]bool
	order    [][32]byte
	round    int
	refused  int
	noticed  bool
}

// New returns a gateway.
func New(p *Ports, opts *Options) (*Gateway, error) {
	if err := opts.check(); err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	vendor := opts.Vendor
	vendor.AgentsOnly = true
	return &Gateway{
		p: *p, opts: *opts, vendor: limits.New(vendor, nil, p.Now).Server("model vendor"),
		lanes: map[sessionKey]*lane{}, sessions: map[sessionKey]*sessionState{}, drained: make(chan struct{}),
	}, nil
}

// check reports options that cannot hold.
func (o *Options) check() error {
	if err := o.Limits.Check(); err != nil {
		return err
	}
	for model, card := range o.Cards {
		if err := card.Check(); err != nil {
			return fmt.Errorf("the card of %s: %w", model, err)
		}
	}
	if o.MaxBodyBytes <= 0 || o.MaxBodyBytes > cost.MaxCount || o.MaxTokens <= 0 || o.MaxTokens > cost.MaxCount ||
		o.Version == "" || o.Reference <= 0 || o.Reference > cost.MaxRate {
		return errors.New("the caps, the API version and the reference price must be set, within what a card prices")
	}
	return o.checkWaits()
}

// checkWaits reports timeouts, waits or a cap of refusals that cannot hold.
func (o *Options) checkWaits() error {
	if o.RecordTimeout <= 0 || o.VendorTimeout <= 0 || o.LastCallWait <= 0 || o.MaxRefusals <= 0 || anyNotPositive(o.Backoff) {
		return errors.New("the timeouts, the waits and the refusals a round records must be above zero")
	}
	return nil
}

func anyNotPositive(ds []time.Duration) bool {
	for _, d := range ds {
		if d <= 0 {
			return true
		}
	}
	return false
}

// Drain stops the gateway taking requests, and waits until those in its
// hands are answered and every call they made has ended and its outcome has
// been recorded, or ctx ends. A call it leaves going on counts at its
// estimate for good.
func (g *Gateway) Drain(ctx context.Context) error {
	g.mu.Lock()
	g.closing = true
	g.settle()
	drained := g.drained
	g.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// enter counts a request in the gateway's hands, unless the gateway is
// stopping.
func (g *Gateway) enter() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return false
	}
	g.busy++
	return true
}

// keep counts a call that may outlive its request, which is counted
// already.
func (g *Gateway) keep() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.busy++
}

// leave uncounts a request, or a call that outlived it.
func (g *Gateway) leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.busy--
	g.settle()
}

// settle closes drained once the gateway is stopping and nothing is in its
// hands. The caller holds g.mu.
func (g *Gateway) settle() {
	if !g.closing || g.busy > 0 {
		return
	}
	select {
	case <-g.drained:
	default:
		close(g.drained)
	}
}

// call is one request in the gateway's hands.
type call struct {
	g      *Gateway
	s      *Session
	red    *redact.Redactor
	rec    *CallRecord
	flight *flight
}

func (c *call) key() sessionKey { return sessionKey{c.s.Run, c.s.Agent} }

// Messages serves one request: it checks it, records it, forwards it and
// records how it ended.
func (g *Gateway) Messages(ctx context.Context, in *Inbound) *Reply {
	if !g.enter() {
		return refusal(http.StatusServiceUnavailable, "api_error", "the gateway is stopping; try again", true)
	}
	defer g.leave()
	s, err := g.p.Sessions.Session(ctx, in.Token)
	switch {
	case errors.Is(err, ErrNoSession):
		return refusal(http.StatusUnauthorized, "authentication_error", "a live session's token is required", false)
	case err != nil:
		return refusal(http.StatusServiceUnavailable, "api_error", "the session could not be read; try again", true)
	}
	c := &call{g: g, s: &s, red: redact.New(), rec: &CallRecord{
		ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Round: s.Round, Class: s.Class, At: g.p.Now(),
		RequestBytes: max(int64(len(in.Body)), in.Length),
	}}
	if g.shutOut(&s) {
		return c.refuse(ctx, http.StatusTooManyRequests, "rate_limit_error", "the session is shut out until the next round")
	}
	rev, err := g.p.Revisions.At(ctx, s.Commit)
	if err != nil {
		return refusal(http.StatusServiceUnavailable, "api_error", "the configuration could not be read; try again", true)
	}
	c.red = always(rev).Before(redact.Rule{Literals: []string{in.Token}, Replacement: config.SessionTokenReplacement})
	switch {
	case in.Method != http.MethodPost || in.Path != Path || in.Query != "":
		return c.refuse(ctx, http.StatusNotFound, "not_found_error", fmt.Sprintf("only POST %s is offered, not %s %.64q", Path, in.Method, in.Path+"?"+in.Query))
	case len(in.Body) > g.opts.MaxBodyBytes:
		return c.refuse(ctx, http.StatusRequestEntityTooLarge, "request_too_large", fmt.Sprintf("a request is at most %d bytes", g.opts.MaxBodyBytes))
	case !s.Deadline.IsZero() && !time.Now().Before(s.Deadline):
		return c.refuse(ctx, http.StatusForbidden, "permission_error", "the session's round has ended")
	}
	if reply := c.take(ctx); reply != nil {
		return reply
	}
	defer g.land(c, true)
	return c.serve(ctx, rev, in)
}

// serve checks a request against the agent's configuration, the allowlist
// and the budgets, then records and forwards it.
func (c *call) serve(ctx context.Context, rev *config.Revision, in *Inbound) *Reply {
	g := c.g
	agent := rev.Agent(c.s.Agent)
	if agent == nil {
		return c.refuse(ctx, http.StatusForbidden, "permission_error", "the session's agent is not in the configuration")
	}
	card, ok := g.opts.Cards[agent.Spec.Model]
	if !ok {
		return c.refuse(ctx, http.StatusForbidden, "permission_error", "no price is set for the agent's model")
	}
	req, err := prompt.Check(in.Header, in.Body, &prompt.Policy{
		Model: agent.Spec.Model, MaxTokens: g.opts.MaxTokens, Version: g.opts.Version, Answered: g.answered(c.s),
	})
	if err == nil {
		err = req.Redact(c.red)
	}
	if err != nil {
		c.recordRefused(in.Body)
		return c.refuse(ctx, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	c.rec.Model = req.Model()
	if reply := c.admit(ctx, in.Body); reply != nil {
		return reply
	}
	req.Pin(serviceTier, g.opts.InferenceGeo)
	body := req.Encode()
	record := prompt.Redacted(body, c.red)
	slot, reply := c.acquire(ctx, in.Body)
	if reply != nil {
		return reply
	}
	sum := sha256.Sum256([]byte(record))
	c.rec.Request, c.rec.RequestBytes, c.rec.RequestDigest = record, int64(len(body)), hex.EncodeToString(sum[:])
	c.rec.Estimate = cost.Weighted(card.Spend(cost.Estimate(c.rec.RequestBytes, req.MaxTokens())), g.opts.Reference)
	if !c.store(ctx, func(ctx context.Context) error { return g.p.Calls.Begin(ctx, c.rec) }) {
		slot()
		return refusal(http.StatusServiceUnavailable, "api_error", "the call could not be recorded, so it was not made", true)
	}
	if err := c.over(ctx); err != nil {
		slot()
		c.notMade(ctx, "the call was not made: "+err.Error())
		return c.cut("before the call was made")
	}
	return c.forward(ctx, slot, body, req.MaxTokens(), &card)
}

// notMade records that a call recorded was not made, which costs nothing,
// trying again apart from the request while the record fails. It counts
// against the session's round as a request dropped earlier does: its record
// holds its request in full.
func (c *call) notMade(ctx context.Context, reason string) {
	c.g.reserve(c.s)
	o := &Outcome{ID: c.rec.ID, Status: StatusError, Reason: reason}
	if c.end(ctx, o) {
		return
	}
	c.g.keep()
	go func() {
		defer c.g.leave()
		c.recordLater(context.WithoutCancel(ctx), o)
	}()
}

// admit reports, as a refusal, a call its run's budget or the day's spend
// does not allow.
func (c *call) admit(ctx context.Context, body []byte) *Reply {
	var run, today int64
	if !c.store(ctx, func(ctx context.Context) (err error) { run, err = c.g.p.Calls.Spent(ctx, c.s.Run); return err }) {
		return refusal(http.StatusServiceUnavailable, "api_error", "the run's spend could not be read; try again", true)
	}
	if !c.store(ctx, func(ctx context.Context) (err error) {
		today, err = c.g.p.Calls.SpentSince(ctx, cost.DayStart(c.g.p.Now()))
		return err
	}) {
		return refusal(http.StatusServiceUnavailable, "api_error", "the day's spend could not be read; try again", true)
	}
	if err := cost.Admit(run, c.s.Budget, today, c.s.Class, c.g.opts.Limits); err != nil {
		c.recordRefused(body)
		return c.refuse(ctx, http.StatusForbidden, "permission_error", err.Error())
	}
	return nil
}

// acquire takes a slot at the vendor for a call, unless the vendor is
// failing, or the request, the round or the run ends first; a call that
// does not start is declined.
func (c *call) acquire(ctx context.Context, body []byte) (func(), *Reply) {
	bounded, cancel := c.bounded(ctx)
	slot, err := c.g.vendor.Acquire(bounded, classOf(c.s.Class))
	ended := bounded.Err()
	cancel()
	if err == nil && ended != nil {
		slot()
		err = ended
	}
	if err == nil {
		return slot, nil
	}
	reply := refusal(http.StatusServiceUnavailable, "overloaded_error", "the model vendor is failing; the call was not made", false)
	reason := "the model vendor is failing: " + err.Error()
	if !errors.Is(err, limits.ErrDegraded) {
		reply, reason = c.cut("before the call was made"), "the request ended before the call was made: "+err.Error()
	}
	c.recordRefused(body)
	return nil, c.decline(ctx, StatusError, reason, reply)
}

// forward makes the call to the vendor apart from the request: the request
// is answered when the vendor answers, or when it ends, the run stops or
// the round ends, whichever comes first; the call goes on until the vendor
// answers or VendorTimeout passes, and its outcome, with what it cost, is
// recorded either way. Until the call ends and its outcome has been tried
// once, it holds the session's place (take); a later try runs apart.
func (c *call) forward(ctx context.Context, slot func(), body []byte, maxTokens int64, card *cost.Card) *Reply {
	c.g.mu.Lock()
	c.flight.calling, c.flight.started = true, time.Now()
	c.g.mu.Unlock()
	c.g.keep()
	done := make(chan *result, 1)
	go func() {
		defer c.g.leave()
		detached := context.WithoutCancel(ctx)
		r := c.attempts(detached, slot, body, maxTokens, card)
		done <- r
		c.g.land(c, false)
		if !r.recorded {
			c.recordLater(detached, r.outcome)
		}
	}()
	bounded, cancel := c.bounded(ctx)
	defer cancel()
	select {
	case r := <-done:
		c.g.remember(c.s, r.thinking)
		return r.reply
	case <-bounded.Done():
		return c.cut("before the model answered")
	}
}

// result is how a forwarded call ended: its reply, its outcome, whether
// the outcome is recorded, and the thinking blocks of an answer returned.
type result struct {
	reply    *Reply
	outcome  *Outcome
	thinking [][32]byte
	recorded bool
}

// attempts sends the request until the vendor answers it, or answers with a
// failure no other attempt gets past, or the round, the run or the call's
// time leaves no room for another; then it frees the vendor's slot and
// records how the call ended.
func (c *call) attempts(ctx context.Context, slot func(), body []byte, maxTokens int64, card *cost.Card) *result {
	ctx, cancel := context.WithTimeout(ctx, c.g.opts.VendorTimeout)
	defer cancel()
	var answer *Answer
	var err error
	n := 0
	for {
		answer, err = c.g.p.Vendor.Send(ctx, body)
		n++
		if err == nil {
			c.g.heard.Store(time.Now().UnixNano())
		}
		if !transient(answer, err) || n > len(c.g.opts.Backoff) {
			break
		}
		wait := jitter(c.g.opts.Backoff[n-1])
		if answer != nil {
			wait = max(wait, answer.RetryAfter)
		}
		if !c.wait(ctx, wait) {
			break
		}
	}
	slot()
	if err != nil || transient(answer, nil) {
		c.g.vendor.Failed()
	} else {
		c.g.vendor.Succeeded()
	}
	return c.finish(ctx, answer, n, err, body, maxTokens, card)
}

// wait waits d before another attempt, unless the round would end first,
// or the run stops or the call's time is up meanwhile.
func (c *call) wait(ctx context.Context, d time.Duration) bool {
	if !c.s.Deadline.IsZero() && time.Until(c.s.Deadline) <= d {
		return false
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-c.s.Done:
		return false
	case <-ctx.Done():
		return false
	}
}

// finish records how a forwarded call ended and answers it. A call whose
// answer was lost, or could not be read, counts its worst case. An answer
// that cannot be recorded is not returned: it would be model output the
// audit store does not hold. A request the vendor finds invalid or too
// large counts against the session's round.
func (c *call) finish(ctx context.Context, answer *Answer, attempts int, err error, body []byte, maxTokens int64, card *cost.Card) *result {
	o := &Outcome{ID: c.rec.ID, Status: StatusError, Attempts: attempts}
	var thinking [][32]byte
	var reply *Reply
	switch {
	case errors.Is(err, ErrLost):
		o.Status, o.Usage = StatusUnknown, cost.Estimate(int64(len(body)), maxTokens)
		o.Reason = c.red.Text("the answer was lost: " + err.Error())
		reply = refusal(http.StatusBadGateway, "api_error", "the model vendor's answer was lost", false)
	case err != nil:
		o.Reason = c.red.Text("the vendor could not be reached: " + err.Error())
		reply = refusal(http.StatusBadGateway, "api_error", "the model vendor could not be reached", false)
	case answer.Status != http.StatusOK:
		o.Reason = fmt.Sprintf("the vendor answered %d: %s", answer.Status, completion.Failure(answer.Body, c.red))
		reply = &Reply{Status: answer.Status, Header: map[string]string{"Content-Type": "application/json", "X-Should-Retry": "false"}, Body: answer.Body}
		if answer.Status == http.StatusBadRequest || answer.Status == http.StatusRequestEntityTooLarge {
			c.g.vendorRefused(c.s)
		}
	default:
		comp, err := completion.Read(answer.Body, c.red)
		if err != nil {
			o.Status, o.Usage = StatusUnknown, cost.Estimate(int64(len(body)), maxTokens)
			o.Reason = "the answer could not be read: " + err.Error()
			reply = refusal(http.StatusBadGateway, "api_error", "the model vendor's answer could not be read", false)
			break
		}
		o.Status, o.Completion, o.Usage, o.Details, thinking = StatusOK, comp.Record, comp.Usage, comp.Details, comp.Thinking
		reply = &Reply{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: answer.Body}
	}
	o.Spend = card.Spend(o.Usage)
	o.Weighted = cost.Weighted(o.Spend, c.g.opts.Reference)
	if answer != nil && answer.RequestID != "" {
		reply.Header["Request-Id"] = answer.RequestID
	}
	if !c.end(ctx, o) {
		return &result{reply: refusal(http.StatusServiceUnavailable, "api_error", "the answer could not be recorded, so it is not returned", false), outcome: o}
	}
	return &result{reply: reply, outcome: o, thinking: thinking, recorded: true}
}

// recordLater tries again to record an outcome that could not be, after
// each RecordTimeout, until it is or VendorTimeout has passed. Until then,
// the call counts at its estimate.
func (c *call) recordLater(ctx context.Context, o *Outcome) {
	for until := time.Now().Add(c.g.opts.VendorTimeout); time.Now().Before(until); {
		time.Sleep(c.g.opts.RecordTimeout)
		if c.end(ctx, o) {
			return
		}
	}
}

// recordRefused keeps of a refused request what its record may hold: at
// most the first maxRecordedRequest bytes of its JSON, redacted, the size
// it came in, and the digest of its redacted JSON in full.
func (c *call) recordRefused(body []byte) {
	full := prompt.Redacted(body, c.red)
	if full == "" {
		return
	}
	sum := sha256.Sum256([]byte(full))
	c.rec.RequestDigest = hex.EncodeToString(sum[:])
	if len(full) > maxRecordedRequest {
		full = strings.ToValidUTF8(full[:maxRecordedRequest], "") + "…"
	}
	c.rec.Request = full
}

// refuse records a request the gateway refuses, with its reason, and
// answers it.
func (c *call) refuse(ctx context.Context, status int, kind, reason string) *Reply {
	return c.decline(ctx, StatusRefused, reason, refusal(status, kind, reason, false))
}

// decline records a request the gateway does not forward, with its status
// and reason, and answers it with reply. It counts against the session's
// round before it is recorded, so that the cap holds for requests sent at
// once: past the cap, the session's shut-out is recorded once, and later
// requests are answered unrecorded. So a sandbox cannot fill the audit
// store with requests it has refused, drops at once, or sends while the
// vendor is failing. A request that cannot be recorded is answered as the
// audit store being unavailable.
func (c *call) decline(ctx context.Context, status Status, reason string, reply *Reply) *Reply {
	switch c.g.reserve(c.s) {
	case recordShutOut:
		status, reason = StatusRefused, "the session's requests were refused too often this round; it is shut out until the next"
		reply = refusal(http.StatusTooManyRequests, "rate_limit_error", reason, false)
	case unrecorded:
		return refusal(http.StatusTooManyRequests, "rate_limit_error", "the session is shut out until the next round", false)
	}
	if !c.record(ctx, status, reason) {
		return refusal(http.StatusServiceUnavailable, "api_error", "the request could not be recorded; try again", true)
	}
	return reply
}

// record records a request that is not forwarded, and how it ended.
func (c *call) record(ctx context.Context, status Status, reason string) bool {
	return c.store(ctx, func(ctx context.Context) error { return c.g.p.Calls.Begin(ctx, c.rec) }) &&
		c.end(ctx, &Outcome{ID: c.rec.ID, Status: status, Reason: c.red.Text(reason)})
}

// end records an outcome, even once the request's context has ended.
func (c *call) end(ctx context.Context, o *Outcome) bool {
	if o.At.IsZero() {
		o.At = c.g.p.Now()
	}
	return c.store(ctx, func(ctx context.Context) error { return c.g.p.Calls.End(ctx, o) })
}

// store makes one read or write of the record within RecordTimeout, even
// once the request's context has ended, so that a request whose client went
// is recorded and counted as any other.
func (c *call) store(ctx context.Context, do func(context.Context) error) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.g.opts.RecordTimeout)
	defer cancel()
	return do(ctx) == nil
}

// over reports why the request cannot go on, if it cannot: its run
// stopped, its round ended, or it ended itself.
func (c *call) over(ctx context.Context) error {
	select {
	case <-c.s.Done:
		return errors.New("the run stopped")
	default:
	}
	if !c.s.Deadline.IsZero() && !time.Now().Before(c.s.Deadline) {
		return errors.New("the round ended")
	}
	return ctx.Err()
}

// bounded returns ctx, ended also when the session's round ends or its run
// stops.
func (c *call) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	cancelDeadline := context.CancelFunc(func() {})
	if !c.s.Deadline.IsZero() {
		ctx, cancelDeadline = context.WithDeadline(ctx, c.s.Deadline)
	}
	ctx, cancel := context.WithCancel(ctx)
	if c.s.Done != nil {
		go func() {
			select {
			case <-c.s.Done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, func() {
		cancel()
		cancelDeadline()
	}
}

// cut answers a request that could not wait longer: its run stopped, its
// round ended, or it ended itself. what says what came first.
func (c *call) cut(what string) *Reply {
	select {
	case <-c.s.Done:
		return refusal(http.StatusBadGateway, "api_error", "the run stopped "+what, false)
	default:
	}
	if !c.s.Deadline.IsZero() && !time.Now().Before(c.s.Deadline) {
		return refusal(http.StatusGatewayTimeout, "timeout_error", "the round ended "+what, false)
	}
	return refusal(http.StatusBadGateway, "api_error", "the request ended "+what, false)
}

// take gives the request the session's place at the gateway. A call that
// goes on after its request was answered (its round ended, its run stopped
// or its client went) holds the place: the next request waits for it to
// end for at most LastCallWait, and then goes on beside it, once the vendor
// has answered some call since it started; while two go on, it waits until
// one ends. So the vendor never has more than two of a session's calls at
// once, and a vendor that answers no one gets no second call to hang. A
// wait ends when the request, its round or its run does. A request that
// finds another in the gateway's hands, or waiting, is refused: the client
// sent them at once.
func (c *call) take(ctx context.Context) *Reply {
	g, key := c.g, c.key()
	patience := time.Now().Add(g.opts.LastCallWait)
	for {
		g.mu.Lock()
		l := g.lanes[key]
		if l == nil {
			l = &lane{ended: make(chan struct{})}
			g.lanes[key] = l
		}
		if l.held || l.waiting {
			g.mu.Unlock()
			return c.refuse(ctx, http.StatusTooManyRequests, "rate_limit_error", "the session has a model call in flight; it makes one at a time")
		}
		if l.going == 0 || (l.going == 1 && !time.Now().Before(patience) && g.heard.Load() > l.since.UnixNano()) {
			l.held = true
			c.flight = &flight{open: true}
			g.mu.Unlock()
			return nil
		}
		l.waiting = true
		ended, until := l.ended, time.Time{}
		if l.going == 1 {
			until = patience
			if !time.Now().Before(patience) {
				until = time.Now().Add(g.opts.LastCallWait)
			}
		}
		g.mu.Unlock()
		waited := c.await(ctx, ended, until)
		g.mu.Lock()
		l.waiting = false
		g.tidy(key, l)
		g.mu.Unlock()
		if !waited {
			return c.decline(ctx, StatusError, "the session's last call went on for longer than the request could wait", c.cut("while the session's last call went on"))
		}
	}
}

// await waits until ended is closed, or until when it is set, and reports
// false when the request, its round or its run ended first.
func (c *call) await(ctx context.Context, ended <-chan struct{}, until time.Time) bool {
	bounded, cancel := c.bounded(ctx)
	defer cancel()
	var patience <-chan time.Time
	if !until.IsZero() {
		t := time.NewTimer(time.Until(until))
		defer t.Stop()
		patience = t.C
	}
	select {
	case <-ended:
	case <-patience:
	case <-bounded.Done():
		return false
	}
	return true
}

// land ends the request's part in its flight, or its call's: a call that
// goes on after its request was answered holds the session's place until
// it ends.
func (g *Gateway) land(c *call, request bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := c.key()
	l, f := g.lanes[key], c.flight
	if request {
		f.open, l.held = false, false
		if f.calling {
			l.going++
			if f.started.After(l.since) {
				l.since = f.started
			}
		}
	} else {
		f.calling = false
		if !f.open {
			l.going--
			close(l.ended)
			l.ended = make(chan struct{})
		}
	}
	if l.going == 0 {
		l.since = time.Time{}
	}
	g.tidy(key, l)
}

// tidy forgets a lane nothing holds. The caller holds g.mu.
func (g *Gateway) tidy(key sessionKey, l *lane) {
	if !l.held && !l.waiting && l.going == 0 {
		delete(g.lanes, key)
	}
}

// state returns what the gateway keeps of a session, marked as used, and
// moved on to the session's round when that is a later one. Past
// MaxSessions, the session used least recently is forgotten. The caller
// holds g.mu.
func (g *Gateway) state(s *Session) *sessionState {
	key := sessionKey{s.Run, s.Agent}
	g.tick++
	st := g.sessions[key]
	if st == nil {
		if len(g.sessions) >= MaxSessions {
			g.forgetLeastUsed()
		}
		st = &sessionState{round: s.Round, thinking: map[[32]byte]bool{}}
		g.sessions[key] = st
	}
	st.used = g.tick
	if s.Round > st.round {
		st.round, st.refused, st.noticed = s.Round, 0, false
	}
	return st
}

// forgetLeastUsed forgets the session used least recently. The caller
// holds g.mu.
func (g *Gateway) forgetLeastUsed() {
	var oldest sessionKey
	var least uint64
	for key, st := range g.sessions {
		if least == 0 || st.used < least {
			oldest, least = key, st.used
		}
	}
	delete(g.sessions, oldest)
}

// answered returns the check of a thinking block sent back: the gateway
// gave it to the session.
func (g *Gateway) answered(s *Session) func([32]byte) bool {
	return func(h [32]byte) bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.state(s).thinking[h]
	}
}

// remember keeps the thinking blocks the gateway gave the session; past
// maxThinking, it forgets the oldest.
func (g *Gateway) remember(s *Session, hashes [][32]byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.state(s)
	for _, h := range hashes {
		if st.thinking[h] {
			continue
		}
		st.thinking[h] = true
		st.order = append(st.order, h)
		if len(st.order) > maxThinking {
			delete(st.thinking, st.order[0])
			st.order = st.order[1:]
		}
	}
}

// refusalRecord says how a refusal is recorded.
type refusalRecord int

const (
	// recordReason records the refusal with its reason.
	recordReason refusalRecord = iota
	// recordShutOut records it as the session's shut-out.
	recordShutOut
	// unrecorded does not record it.
	unrecorded
)

// reserve counts a refusal against the session's round before it is
// recorded, and says how it is recorded: with its reason while the round
// has refusals left, as the session's shut-out once, and not at all after.
func (g *Gateway) reserve(s *Session) refusalRecord {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.state(s)
	switch {
	case st.refused < g.opts.MaxRefusals:
		st.refused++
		return recordReason
	case !st.noticed:
		st.noticed = true
		return recordShutOut
	}
	return unrecorded
}

// vendorRefused counts the vendor's refusal of a session's request against
// the session's round, as the gateway's own refusals count, unless the
// round has passed since.
func (g *Gateway) vendorRefused(s *Session) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st := g.state(s); st.round == s.Round {
		st.refused++
	}
}

// shutOut reports a session whose round has no refusals left.
func (g *Gateway) shutOut(s *Session) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state(s).refused >= g.opts.MaxRefusals
}

// transient reports a failure another attempt may get past: one whose
// request never reached the vendor, or that the vendor says is passing.
func transient(answer *Answer, err error) bool {
	if err != nil {
		return errors.Is(err, ErrNotSent)
	}
	switch answer.Status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// jitter returns d with up to a fifth more, at random, so that sessions
// that failed together do not try again together.
func jitter(d time.Duration) time.Duration {
	return d + time.Duration(rand.Int64N(int64(d)/5+1)) //nolint:gosec // G404: spreading waits needs no secret
}

func classOf(c cost.Class) limits.Class {
	if c == cost.Shadow {
		return limits.Shadow
	}
	return limits.Primary
}

// refusal answers in the Messages API's form of an error. retry says
// whether the client may send the request again.
func refusal(status int, kind, message string, retry bool) *Reply {
	body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}})
	return &Reply{Status: status, Header: map[string]string{"Content-Type": "application/json", "X-Should-Retry": strconv.FormatBool(retry)}, Body: body}
}

// always returns the redaction rules that apply always.
func always(rev *config.Revision) *redact.Redactor {
	var rules []redact.Rule
	for _, r := range rev.AlwaysRedactionRules() {
		rules = append(rules, redact.FromConfig(r))
	}
	return redact.New(rules...)
}
```

- [ ] **Step 6: Keep sessions and calls in memory**

Create `internal/llm/memory/memory.go`:

```go
// Package memory holds the LLM gateway's sessions and its record of model
// calls in memory, for tests and development.
package memory

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/llm/app"
)

// Sessions maps tokens to sessions. It implements the gateway's Sessions
// port.
type Sessions struct {
	mu       sync.Mutex
	sessions map[string]app.Session
}

// NewSessions returns no sessions.
func NewSessions() *Sessions { return &Sessions{sessions: map[string]app.Session{}} }

// Put starts, or replaces, the session token names.
func (s *Sessions) Put(token string, session *app.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[token] = *session
}

// End ends the session token names.
func (s *Sessions) End(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// Session returns the session token names, or app.ErrNoSession.
func (s *Sessions) Session(_ context.Context, token string) (app.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[token]
	if !ok {
		return app.Session{}, app.ErrNoSession
	}
	return session, nil
}

// Calls records model calls in memory. It implements the gateway's Calls
// port, and fails a call whose context has ended, as a database does.
type Calls struct {
	mu       sync.Mutex
	records  []app.CallRecord
	outcomes map[string]app.Outcome
	// fail fails every method, and failEnd End only, when set.
	fail, failEnd error
	// hold, when set, holds Begin until it is closed; held counts the Begin
	// calls it holds.
	hold chan struct{}
	held int
}

// NewCalls returns an empty record.
func NewCalls() *Calls { return &Calls{outcomes: map[string]app.Outcome{}} }

// SetFail makes every method fail with err, or none when err is nil.
func (c *Calls) SetFail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = err
}

// SetFailEnd makes End fail with err, or not when err is nil.
func (c *Calls) SetFailEnd(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failEnd = err
}

// HoldBegins holds every Begin until release is called, or its context
// ends.
func (c *Calls) HoldBegins() (release func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hold := make(chan struct{})
	c.hold = hold
	return func() {
		c.mu.Lock()
		c.hold = nil
		c.mu.Unlock()
		close(hold)
	}
}

// Held returns how many Begin calls are held.
func (c *Calls) Held() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held
}

// Begin records a call.
func (c *Calls) Begin(ctx context.Context, rec *app.CallRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hold := c.hold; hold != nil {
		c.held++
		c.mu.Unlock()
		select {
		case <-hold:
		case <-ctx.Done():
		}
		c.mu.Lock()
		c.held--
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.fail != nil {
		return c.fail
	}
	for i := range c.records {
		if c.records[i].ID == rec.ID {
			return errors.New("memory: the call is recorded already")
		}
	}
	c.records = append(c.records, *rec)
	return nil
}

// End records how a call ended. The same outcome again changes nothing; a
// different one is refused.
func (c *Calls) End(ctx context.Context, o *app.Outcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != nil {
		return c.fail
	}
	if c.failEnd != nil {
		return c.failEnd
	}
	if prev, ok := c.outcomes[o.ID]; ok {
		if prev.Status != o.Status || prev.Reason != o.Reason || prev.Weighted != o.Weighted || prev.Completion != o.Completion {
			return errors.New("memory: the call has another outcome already")
		}
		return nil
	}
	c.outcomes[o.ID] = *o
	return nil
}

// Spent returns the weighted tokens of a run's calls: each at its outcome's
// cost once it has one, and at its estimate until then.
func (c *Calls) Spent(ctx context.Context, run string) (int64, error) {
	return c.sum(ctx, func(rec *app.CallRecord) bool { return rec.Run == run })
}

// SpentSince returns the weighted tokens of the calls that began at or
// after since, counted as Spent counts them.
func (c *Calls) SpentSince(ctx context.Context, since time.Time) (int64, error) {
	return c.sum(ctx, func(rec *app.CallRecord) bool { return !rec.At.Before(since) })
}

func (c *Calls) sum(ctx context.Context, counts func(*app.CallRecord) bool) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != nil {
		return 0, c.fail
	}
	var n int64
	for i := range c.records {
		if !counts(&c.records[i]) {
			continue
		}
		if o, ok := c.outcomes[c.records[i].ID]; ok {
			n += o.Weighted
		} else {
			n += c.records[i].Estimate
		}
	}
	return n, nil
}

// Record returns a call's record and its outcome, if any.
func (c *Calls) Record(id string) (rec app.CallRecord, o app.Outcome, began, ended bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.records {
		if c.records[i].ID == id {
			rec, began = c.records[i], true
		}
	}
	o, ended = c.outcomes[id]
	return rec, o, began, ended
}

// Records returns every call recorded, in order.
func (c *Calls) Records() []app.CallRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]app.CallRecord(nil), c.records...)
}
```

- [ ] **Step 7: Run the tests to see them pass**

Run: `go test -race ./internal/llm/app/ ./internal/actions/limits/ && golangci-lint run ./...`
Expected: both `ok`; `0 issues.`

- [ ] **Step 8: Commit**

```bash
git add internal/llm/app internal/llm/memory internal/actions/limits
git commit -m "feat(llm): the LLM gateway: the allowlist, redaction, budgets, the daily cap and a record of every model call"
```

### Task 5: The vendor

Decisions 2 and 8, at the vendor: the request as the gateway encoded it, the deployment's key, nothing of the client's, and an attempt never sent told from an answer lost.

**Files:**
- Create: `internal/llm/anthropic/anthropic.go`, `internal/llm/anthropic/anthropic_test.go`

**Interfaces:**
- Consumes: Task 4's `app.Answer`, `app.ErrNotSent` and `app.ErrLost`.
- Produces: `type Config struct { BaseURL, KeyRef, Version string }`; `func New(cfg *Config, secrets Secrets) (*Vendor, error)`; `func (v *Vendor) Send(ctx, body []byte) (*app.Answer, error)`; `const MaxAnswerBytes`.

- [ ] **Step 1: Write the failing tests**

Create `internal/llm/anthropic/anthropic_test.go`:

```go
package anthropic_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/llm/anthropic"
	"github.com/jchen0426/agent-swarm/internal/llm/app"
)

// keys is a secret store that rotates the key: each read gives the next.
type keys struct{ n atomic.Int32 }

func (k *keys) Secret(_ context.Context, ref string) (string, error) {
	if ref != "secret://llm/anthropic" {
		return "", errors.New("no such secret")
	}
	return []string{"key-one", "key-two"}[k.n.Add(1)%2], nil
}

func vendor(t *testing.T, base string) *anthropic.Vendor {
	t.Helper()
	v, err := anthropic.New(&anthropic.Config{BaseURL: base, KeyRef: "secret://llm/anthropic", Version: "2023-06-01"}, &keys{})
	require.NoError(t, err)
	return v
}

// The vendor gets the body as given, the key read at each call and the
// gateway's version, and no other header that matters.
func TestARequestCarriesTheKeyAndNothingOfTheClients(t *testing.T) {
	var got []http.Header
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, bodies = append(got, r.Header.Clone()), append(bodies, string(b))
		assert.Equal(t, "/v1/messages", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("request-id", "req_1")
		_, _ = io.WriteString(w, `{"ok": true}`)
	}))
	defer srv.Close()
	v := vendor(t, srv.URL)

	a, err := v.Send(t.Context(), []byte(`{"model":"m"}`))
	require.NoError(t, err)
	_, err = v.Send(t.Context(), []byte(`{"model":"m"}`))
	require.NoError(t, err)

	assert.Equal(t, 200, a.Status)
	assert.JSONEq(t, `{"ok": true}`, string(a.Body))
	assert.Equal(t, "req_1", a.RequestID)
	assert.Equal(t, []string{`{"model":"m"}`, `{"model":"m"}`}, bodies)
	assert.Equal(t, []string{"key-two", "key-one"}, []string{got[0].Get("X-Api-Key"), got[1].Get("X-Api-Key")}, "read at each call")
	for _, h := range got {
		assert.Equal(t, "2023-06-01", h.Get("Anthropic-Version"))
		assert.Equal(t, "application/json", h.Get("Content-Type"))
		assert.Empty(t, h.Get("Authorization"))
		assert.Empty(t, h.Get("Anthropic-Workspace-Id"))
		assert.Empty(t, h.Get("Anthropic-Beta"))
		for name := range h {
			assert.False(t, strings.HasPrefix(name, "X-Stainless"), name)
		}
	}
}

// A redirect is never followed: the key would go with it to another host.
func TestARedirectIsNotFollowed(t *testing.T) {
	var reached atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v1/messages", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	a, err := vendor(t, srv.URL).Send(t.Context(), []byte(`{}`))

	require.NoError(t, err)
	assert.Equal(t, http.StatusTemporaryRedirect, a.Status)
	assert.False(t, reached.Load())
}

// The wait the vendor asks for is read in seconds or as a date, and never
// as more than an hour.
func TestTheWaitTheVendorAsksForIsRead(t *testing.T) {
	for header, want := range map[string]time.Duration{"7": 7 * time.Second, "": 0, "soon": 0, "-3": 0, "99999999999": time.Hour} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if header != "" {
				w.Header().Set("retry-after", header)
			}
			w.WriteHeader(529)
		}))

		a, err := vendor(t, srv.URL).Send(t.Context(), []byte(`{}`))

		srv.Close()
		require.NoError(t, err)
		assert.Equal(t, want, a.RetryAfter, header)
	}
}

// A request that never left is ErrNotSent, which another attempt may get
// past; one whose answer broke off after it was written, or was larger
// than the gateway reads, is ErrLost: the vendor may have charged for it.
func TestFailuresToReachTheVendor(t *testing.T) {
	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 9<<20))
	}))
	defer large.Close()
	_, err := vendor(t, large.URL).Send(t.Context(), []byte(`{}`))
	require.ErrorIs(t, err, app.ErrLost)

	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if !assert.NoError(t, err) {
			return
		}
		_ = conn.Close()
	}))
	defer cut.Close()
	_, err = vendor(t, cut.URL).Send(t.Context(), []byte(`{}`))
	require.ErrorIs(t, err, app.ErrLost)

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	_, err = vendor(t, gone.URL).Send(t.Context(), []byte(`{}`))
	require.ErrorIs(t, err, app.ErrNotSent)
}

// The vendor is reached over HTTPS, or over plain HTTP on a loopback host
// only; its key is a secret reference.
func TestTheVendorIsReachedOverHTTPS(t *testing.T) {
	for _, base := range []string{"http://api.anthropic.com", "ftp://127.0.0.1", "https://", "not a url"} {
		_, err := anthropic.New(&anthropic.Config{BaseURL: base, KeyRef: "secret://llm/anthropic", Version: "2023-06-01"}, &keys{})

		require.Error(t, err, base)
	}
	_, err := anthropic.New(&anthropic.Config{BaseURL: "https://api.anthropic.com", KeyRef: "", Version: "2023-06-01"}, &keys{})
	require.Error(t, err, "a key reference is required")
	_, err = anthropic.New(&anthropic.Config{BaseURL: "https://api.anthropic.com", KeyRef: "secret://llm/anthropic", Version: "2023-06-01"}, &keys{})
	require.NoError(t, err)
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/llm/anthropic/`
Expected: FAIL: `no non-test Go files in .../internal/llm/anthropic`

- [ ] **Step 3: Send a request to the vendor**

Create `internal/llm/anthropic/anthropic.go`:

```go
// Package anthropic is the LLM gateway's adapter of the model vendor
// (ADR-007). It sends a request the gateway checked to the vendor's
// Messages API with the deployment's key, which it reads from the secret
// store at each call so that a rotated key takes effect at once (spec
// Section 9.3), and with nothing of the client's: no header the client
// sent reaches the vendor. It never follows a redirect, which would carry
// the key to another host. It tells an attempt whose request never left
// from one whose answer was lost after the request reached the vendor,
// which the vendor may have charged for.
package anthropic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jchen0426/agent-swarm/internal/llm/app"
)

// MaxAnswerBytes caps the answer the gateway reads.
const MaxAnswerBytes = 8 << 20

// Secrets resolves secret references at the time of use.
type Secrets interface {
	Secret(ctx context.Context, ref string) (string, error)
}

// Config names the vendor's API and the deployment's key.
type Config struct {
	// BaseURL is the API's address: HTTPS, or plain HTTP to a loopback
	// host only.
	BaseURL string
	// KeyRef is the secret reference of the API key.
	KeyRef string
	// Version is the anthropic-version every request carries.
	Version string
}

// Vendor sends requests to the vendor's Messages API. It implements the
// LLM gateway's Vendor port.
type Vendor struct {
	messages string
	keyRef   string
	version  string
	secrets  Secrets
	client   *http.Client
}

// New returns the vendor at cfg.BaseURL.
func New(cfg *Config, secrets Secrets) (*Vendor, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("anthropic: %q is not the API's address", cfg.BaseURL)
	}
	switch {
	case base.Scheme == "https":
	case base.Scheme == "http" && loopback(base.Hostname()):
	default:
		return nil, fmt.Errorf("anthropic: %s is reached over HTTPS; plain HTTP is for a loopback host only", cfg.BaseURL)
	}
	if cfg.KeyRef == "" || cfg.Version == "" {
		return nil, errors.New("anthropic: the key's secret reference and the API version are required")
	}
	return &Vendor{
		messages: base.JoinPath("v1", "messages").String(),
		keyRef:   cfg.KeyRef,
		version:  cfg.Version,
		secrets:  secrets,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Send posts body to the Messages API and returns the vendor's answer,
// whatever its status. An attempt whose request never left is
// app.ErrNotSent; one whose answer broke off, or was larger than the
// gateway reads, after the request was written, is app.ErrLost.
func (v *Vendor) Send(ctx context.Context, body []byte) (*app.Answer, error) {
	key, err := v.secrets.Secret(ctx, v.keyRef)
	if err != nil {
		return nil, fmt.Errorf("%w: read the API key: %w", app.ErrNotSent, err)
	}
	var wrote atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) }})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.messages, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", app.ErrNotSent, err)
	}
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("Anthropic-Version", v.version)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "agentswarm-llm-gateway")
	resp, err := v.client.Do(req) //nolint:gosec // G704: the configured API's Messages endpoint only
	if err != nil {
		if wrote.Load() {
			return nil, fmt.Errorf("%w: %w", app.ErrLost, err)
		}
		return nil, fmt.Errorf("%w: %w", app.ErrNotSent, err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, MaxAnswerBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: the answer broke off: %w", app.ErrLost, err)
	}
	if len(answer) > MaxAnswerBytes {
		return nil, fmt.Errorf("%w: the answer is larger than %d bytes", app.ErrLost, MaxAnswerBytes)
	}
	return &app.Answer{
		Status: resp.StatusCode, Body: answer,
		RequestID: resp.Header.Get("Request-Id"), RetryAfter: retryAfter(resp.Header.Get("Retry-After")),
	}, nil
}

// maxRetryAfter bounds the wait the vendor may ask for; the gateway waits
// less, within the round and the call's timeout.
const maxRetryAfter = time.Hour

// retryAfter reads a wait given in seconds or as a date; anything else is
// no wait.
func retryAfter(v string) time.Duration {
	if s, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Duration(min(max(s, 0), int64(maxRetryAfter/time.Second))) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return min(max(time.Until(t), 0), maxRetryAfter)
	}
	return 0
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test -race ./internal/llm/anthropic/ && golangci-lint run ./...`
Expected: `ok`; `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal/llm/anthropic
git commit -m "feat(llm): the model vendor's adapter: the deployment's key, nothing of the client's, and lost answers told apart"
```

### Task 6: Model calls in the audit store

Decision 10, in Postgres, and Plan 6a's constraint on this plan: the three tables chained from their first migration, and read by verification.

**Files:**
- Create: `internal/db/migrations/0006_model_calls.sql`, `internal/audit/postgres/models.go`, `internal/audit/postgres/models_integration_test.go`
- Modify: `internal/audit/postgres/chain.go`, `internal/audit/postgres/calls.go`, `internal/audit/postgres/chain_integration_test.go`

**Interfaces:**
- Consumes: Task 4's `app.CallRecord`, `app.Outcome` and `app.Calls`; migration 0005's `audit_chain_append` and `audit_records_guard`.
- Produces: `func postgres.NewModelCalls(pool *pgxpool.Pool, tenant string) *postgres.ModelCalls`, implementing `app.Calls`: a call counts its outcome's cost, or its estimate until it has one; the tables `model_calls`, `model_call_contents` and `model_call_outcomes` among verification's sources.

- [ ] **Step 1: Write the failing tests**

Create `internal/audit/postgres/models_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/postgres"
	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
)

const modelRequest = `{"max_tokens":1,"messages":[{"content":"hi","role":"user"}],"model":"claude-haiku-5-5"}`

// modelRecord is a model call of run that began at start, with an
// estimate of 5 weighted tokens.
func modelRecord(id, run string, start time.Time) *app.CallRecord {
	return &app.CallRecord{ID: id, Run: run, Agent: "flag-investigator", Round: 1, Class: cost.Primary,
		Model: "claude-haiku-5-5", Request: modelRequest, RequestBytes: int64(len(modelRequest)), RequestDigest: "d1", Estimate: 5, At: start}
}

func modelOutcome(id string, weighted int64, end time.Time) *app.Outcome {
	return &app.Outcome{ID: id, Status: app.StatusOK, Completion: `{"content":[{"text":"ok","type":"text"}]}`,
		Usage: cost.Usage{Input: weighted}, Spend: 100_000 * weighted, Weighted: weighted, Attempts: 1, Details: `{"id":"msg_1"}`, At: end}
}

// modelCall records a model call of run that began at start, spent
// weighted tokens and ended two minutes later.
func modelCall(t *testing.T, pool *pgxpool.Pool, tenant, id, run string, weighted int64, start time.Time) {
	t.Helper()
	calls := postgres.NewModelCalls(pool, tenant)
	require.NoError(t, calls.Begin(t.Context(), modelRecord(id, run, start)))
	require.NoError(t, calls.End(t.Context(), modelOutcome(id, weighted, start.Add(2*time.Minute))))
}

// A model call is recorded with its request before it is forwarded, then
// its outcome with its completion, the contents apart from the records. The
// same outcome recorded again changes nothing; another one is refused.
func TestAModelCallIsRecordedThenItsOutcome(t *testing.T) {
	pool := pgtest.Pool(t)
	calls := postgres.NewModelCalls(pool, "models-record")
	require.NoError(t, calls.Begin(t.Context(), modelRecord("m-1", "run-1", at)))

	require.NoError(t, calls.End(t.Context(), modelOutcome("m-1", 120, at)))
	require.NoError(t, calls.End(t.Context(), modelOutcome("m-1", 120, at)), "the same outcome again")
	changed := modelOutcome("m-1", 120, at)
	changed.Completion = `{"content":[]}`
	err := calls.End(t.Context(), changed)

	require.ErrorIs(t, err, postgres.ErrConflict)
	contents := map[string]string{}
	rows, err := pool.Query(t.Context(), `SELECT part, content FROM model_call_contents WHERE tenant_id = 'models-record' AND call_id = 'm-1'`)
	require.NoError(t, err)
	for rows.Next() {
		var part, content string
		require.NoError(t, rows.Scan(&part, &content))
		contents[part] = content
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, map[string]string{"request": modelRequest, "completion": `{"content":[{"text":"ok","type":"text"}]}`}, contents)
	var weighted, spend int64
	var details string
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT weighted, spend, details FROM model_call_outcomes WHERE tenant_id = 'models-record'`).Scan(&weighted, &spend, &details))
	assert.Equal(t, int64(120), weighted)
	assert.Equal(t, int64(12_000_000), spend)
	assert.JSONEq(t, `{"id":"msg_1"}`, details)
}

// A run's spend is its own calls': what each cost once its outcome is
// recorded, and its estimate until then. The day's is the tenant's calls'
// that began since the day began, counted the same way.
func TestSpendIsCountedPerRunAndSinceATime(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "models-spend"
	calls := postgres.NewModelCalls(pool, tenant)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	modelCall(t, pool, tenant, "s-1", "run-a", 100, day.Add(-time.Minute))
	modelCall(t, pool, tenant, "s-2", "run-a", 200, day.Add(time.Hour))
	modelCall(t, pool, tenant, "s-3", "run-b", 400, day.Add(2*time.Hour))
	modelCall(t, pool, "models-other", "s-4", "run-a", 800, day.Add(time.Hour))
	inFlight := modelRecord("s-5", "run-a", day.Add(3*time.Hour))
	inFlight.Estimate = 1600
	require.NoError(t, calls.Begin(t.Context(), inFlight))

	runA, err := calls.Spent(t.Context(), "run-a")
	require.NoError(t, err)
	today, err := calls.SpentSince(t.Context(), day)
	require.NoError(t, err)
	none, err := calls.Spent(t.Context(), "run-z")
	require.NoError(t, err)

	assert.Equal(t, int64(100+200+1600), runA)
	assert.Equal(t, int64(200+400+1600), today, "the call that began yesterday counts yesterday")
	assert.Equal(t, int64(0), none)
}

// Model call records never change: only the retention job deletes them.
func TestModelCallRecordsNeverChange(t *testing.T) {
	pool := pgtest.Pool(t)
	modelCall(t, pool, "models-guard", "g-1", "run-1", 10, at)

	for _, stmt := range []string{
		`UPDATE model_calls SET request_digest = '' WHERE tenant_id = 'models-guard'`,
		`DELETE FROM model_calls WHERE tenant_id = 'models-guard'`,
		`UPDATE model_call_contents SET content = '' WHERE tenant_id = 'models-guard'`,
		`DELETE FROM model_call_contents WHERE tenant_id = 'models-guard'`,
		`UPDATE model_call_outcomes SET weighted = 0 WHERE tenant_id = 'models-guard'`,
		`DELETE FROM model_call_outcomes WHERE tenant_id = 'models-guard'`,
		`TRUNCATE model_call_contents`,
	} {
		_, err := pool.Exec(t.Context(), stmt)

		require.ErrorContains(t, err, "is append-only", stmt)
	}
}

// Model calls are chained as they are recorded, their contents and
// outcomes under their call's run, and verification renders each row to
// its record's digest.
func TestModelCallsAreChained(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "models-chain"
	modelCall(t, pool, tenant, "c-1", "run-m", 42, at)
	calls := postgres.NewModelCalls(pool, tenant)
	refused := modelRecord("c-2", "run-m", at)
	refused.Model, refused.Request, refused.RequestDigest, refused.Estimate = "", "", "", 0
	require.NoError(t, calls.Begin(t.Context(), refused))
	require.NoError(t, calls.End(t.Context(), &app.Outcome{ID: "c-2", Status: app.StatusRefused, Reason: "the field \"metadata\" is not allowed", At: at}))
	c := newChain(pool, tenant)

	links := readChain(t, c)

	var got []string
	for i := range links {
		got = append(got, fmt.Sprintf("%s %s %s", links[i].Source, links[i].Run, links[i].Key))
	}
	assert.Equal(t, []string{`model_calls run-m ["c-1"]`, `model_call_contents run-m ["c-1", "request"]`,
		`model_call_outcomes run-m ["c-1"]`, `model_call_contents run-m ["c-1", "completion"]`,
		`model_calls run-m ["c-2"]`, `model_call_outcomes run-m ["c-2"]`}, got)
	assert.Equal(t, 6, checkRows(t, c))
}
```

Replace `internal/audit/postgres/chain_integration_test.go` with this version, whose counts and records name the three new tables:

```go
//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	neturl "net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
	"github.com/jchen0426/agent-swarm/internal/audit/dirstore"
	"github.com/jchen0426/agent-swarm/internal/audit/postgres"
	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	workpostgres "github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var investigator = board.Author{Kind: board.AuthorAgent, Name: "flag-investigator"}

// post appends one board entry to run, through the board's own store.
func post(t *testing.T, pool *pgxpool.Pool, tenant string, run board.RunID, id board.EntryID, body *board.Body) {
	t.Helper()
	_, err := workpostgres.NewEventStore(pool, tenant).Append(t.Context(), run, func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{{ID: id, Run: run, Round: 1, Author: investigator, Body: *body}}, nil
	})
	require.NoError(t, err)
}

func task(title string) *board.Body { return &board.Body{Task: &board.Task{Title: title}} }

// call records a call of run and its outcome, through the audit store.
func call(t *testing.T, pool *pgxpool.Pool, tenant, id string, run board.RunID, args, output string) {
	t.Helper()
	calls := postgres.NewCalls(pool, tenant)
	rec := record(id)
	rec.Run, rec.Args = run, json.RawMessage(args)
	require.NoError(t, calls.Begin(t.Context(), rec))
	require.NoError(t, calls.End(t.Context(), &actionsapp.Outcome{ID: id, Status: actionsapp.StatusOK, Output: output, At: at}))
}

// hide records a hidden tool.
func hide(t *testing.T, pool *pgxpool.Pool, tenant, tool string) {
	t.Helper()
	require.NoError(t, postgres.NewHides(pool, tenant).Hide(t.Context(), &actionsapp.Hide{
		Endpoint: "https://flags.internal/mcp", Tool: tool, PinnedHash: "sha256:pin", Registration: "sha256:reg",
		Server: "flags", Reason: actionsapp.ReasonChanged, Observed: "sha256:other", At: at,
	}))
}

// read calls fn with one snapshot of the tenant's chain.
func read(t *testing.T, c *postgres.Chain, fn func(app.View)) {
	t.Helper()
	require.NoError(t, c.Read(t.Context(), func(v app.View) error { fn(v); return nil }))
}

// readChain returns the tenant's records, and checks that each links to the
// one before as verification recomputes it.
func readChain(t *testing.T, c *postgres.Chain) []chain.Link {
	t.Helper()
	var links []chain.Link
	read(t, c, func(v app.View) {
		require.NoError(t, v.Links(t.Context(), func(l *chain.Link) error { links = append(links, *l); return nil }))
	})
	var prev chain.Sum
	for i := range links {
		l := &links[i]
		require.Equal(t, int64(i+1), l.Seq, "the chain has no gap")
		require.Equal(t, prev, l.Prev)
		require.Equal(t, chain.LinkHash(prev, l), l.Hash, "the database's hash is the one verification recomputes")
		prev = l.Hash
	}
	return links
}

// checkRows checks that every record's row renders to its digest, and that
// no row is unchained; it returns how many rows there are.
func checkRows(t *testing.T, c *postgres.Chain) int {
	t.Helper()
	n := 0
	read(t, c, func(v app.View) {
		require.NoError(t, v.Rows(t.Context(), func(r *app.Row) error {
			n++
			require.True(t, r.Known, r.Source)
			require.NotNil(t, r.Rendered, r.Source)
			assert.Equal(t, r.Digest, chain.Digest(string(r.Rendered)), "%s %s", r.Source, r.Key)
			return nil
		}))
		unchained, err := v.Unchained(t.Context())
		require.NoError(t, err)
		assert.Empty(t, unchained)
	})
	return n
}

func newChain(pool *pgxpool.Pool, tenant string) *postgres.Chain {
	return postgres.NewChain(pool, tenant, "public")
}

// Every row of every audited table is chained as it is inserted, in order,
// and verification renders each row to the digest its record holds.
func TestEveryAuditedRowIsChained(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-every"
	post(t, pool, tenant, "run-a", "every-1", task("check the pool"))
	post(t, pool, tenant, "run-a", "every-2", task("check the cache"))
	call(t, pool, tenant, "every-c1", "run-b", `{"flag":"x"}`, `{"on":true}`)
	hide(t, pool, tenant, "get_flag")
	c := newChain(pool, tenant)

	links := readChain(t, c)

	var got []string
	for i := range links {
		got = append(got, links[i].Source+" "+links[i].Run)
	}
	assert.Equal(t, []string{"board_events run-a", "board_events run-a", "tool_calls run-b", "tool_call_outcomes run-b", "tool_hides "}, got)
	assert.Equal(t, `["run-a", 2]`, links[1].Key)
	assert.Equal(t, 5, checkRows(t, c))
	read(t, c, func(v app.View) {
		head, err := v.Head(t.Context())
		require.NoError(t, err)
		assert.Equal(t, links[4].Hash, head.Hash)
		runs, err := v.Runs(t.Context(), 0, 5)
		require.NoError(t, err)
		assert.Equal(t, []string{"run-a", "run-b"}, runs)
		runs, err = v.Runs(t.Context(), 3, 5)
		require.NoError(t, err)
		assert.Equal(t, []string{"run-b"}, runs, "an outcome is its call's run")
		counts, err := v.Counts(t.Context())
		require.NoError(t, err)
		assert.Equal(t, map[string]int64{"board_events": 2, "tool_calls": 1, "tool_call_outcomes": 1, "tool_hides": 1,
			"config_revisions": 0, "config_objects": 0, "model_calls": 0, "model_call_contents": 0, "model_call_outcomes": 0}, counts)
	})
}

// An empty chain has no head.
func TestAnEmptyChainHasNoHead(t *testing.T) {
	c := newChain(pgtest.Pool(t), "chain-empty")

	read(t, c, func(v app.View) {
		head, err := v.Head(t.Context())
		require.NoError(t, err)
		assert.Nil(t, head)
	})
	assert.Empty(t, readChain(t, c))
}

// Writers that race, some of them rolled back, leave one contiguous chain:
// records commit in the order of their seq, and a rollback leaves no gap.
func TestRacingWritersLeaveOneContiguousChain(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-race"
	var wg sync.WaitGroup
	for i := range 15 {
		wg.Go(func() { call(t, pool, tenant, fmt.Sprintf("race-c%d", i), "run-c", `{"flag":"x"}`, "ok") })
		wg.Go(func() {
			post(t, pool, tenant, board.RunID(fmt.Sprintf("run-%d", i%3)), board.EntryID(fmt.Sprintf("race-%d", i)), task("t"))
		})
		wg.Go(func() {
			err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(context.Background(), `INSERT INTO tool_calls (tenant_id, call_id, run_id, agent, server, tool, schema_hash, arguments, round, started_at)
VALUES ($1, $2, 'run-c', 'a', 's', 't', 'h', '{}', 1, now())`, tenant, fmt.Sprintf("race-rolled-back-%d", i))
				if err != nil {
					return err
				}
				return errors.New("roll back")
			})
			assert.EqualError(t, err, "roll back")
		})
	}
	wg.Wait()
	c := newChain(pool, tenant)

	links := readChain(t, c)
	assert.Len(t, links, 45)
	assert.Equal(t, 45, checkRows(t, c))
	for i := 1; i < len(links); i++ {
		assert.False(t, links[i].ChainedAt.Before(links[i-1].ChainedAt), "a record is chained no earlier than the one before")
	}
}

// The chain is written only at read committed: at any other level, a
// transaction's snapshot could predate the record it waited for, so the
// insert fails rather than forking the chain.
func TestAuditedRowsAreWrittenOnlyAtReadCommitted(t *testing.T) {
	const tenant = "chain-levels"
	for _, level := range []string{"repeatable read", "serializable"} {
		pool := pgtest.PoolWithParams(t, map[string]string{"default_transaction_isolation": level})

		err := postgres.NewCalls(pool, tenant).Begin(t.Context(), record("levels-"+level))

		require.ErrorContains(t, err, "read committed", level)
	}
}

// A row's digest is the same whatever the inserting session's settings and
// the reading connection's encoding, and whatever the text holds: quotes,
// backslashes, control characters, characters outside ASCII, and JSON
// written inside a string.
func TestTheDigestIgnoresSessionSettingsAndEncodings(t *testing.T) {
	odd := pgtest.PoolWithParams(t, map[string]string{"TimeZone": "Pacific/Chatham", "DateStyle": "SQL, DMY", "IntervalStyle": "sql_standard"})
	const tenant = "chain-odd"
	text := "q\"uote \\back\nline\ttab \x01ctl é ü {\"a\":[1,2.50]}"
	call(t, odd, tenant, "odd-c1", "run-odd", fmt.Sprintf(`{"text":%q}`, text), text+" 漢字")
	post(t, odd, tenant, "run-odd", "odd-1", &board.Body{Task: &board.Task{Title: text, Tags: []string{"a\"b", "漢"}}})

	for _, params := range []map[string]string{nil, {"client_encoding": "LATIN1"}} {
		c := newChain(pgtest.PoolWithParams(t, params), tenant)

		readChain(t, c)
		assert.Equal(t, 3, checkRows(t, c), params)
	}
}

// tamper runs statements with every trigger off, as an operator with the
// database's superuser may.
func tamper(t *testing.T, pool *pgxpool.Pool, statements ...string) {
	t.Helper()
	require.NoError(t, pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), "SET LOCAL session_replication_role = replica"); err != nil {
			return err
		}
		for _, s := range statements {
			if _, err := tx.Exec(t.Context(), s); err != nil {
				return err
			}
		}
		return nil
	}))
}

// auditor returns the auditor of tenant, with its anchors in a directory,
// and a clock tests move.
func auditor(t *testing.T, pool *pgxpool.Pool, tenant string, now *time.Time) *app.Auditor {
	t.Helper()
	store, err := dirstore.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := app.New(newChain(pool, tenant), store, tenant, func() time.Time { return *now })
	require.NoError(t, err)
	return a
}

// findings returns what a verification found, as kind and table, but for
// the directory store being unlocked.
func findings(t *testing.T, a *app.Auditor) []string {
	t.Helper()
	all, err := a.Verify(t.Context(), time.Hour)
	require.NoError(t, err)
	var out []string
	for i := range all {
		if all[i].Kind != chain.StoreUnlocked {
			out = append(out, string(all[i].Kind)+" "+all[i].Source)
		}
	}
	return out
}

// What an operator does to the database behind the platform's back shows:
// a row changed, a row deleted, a row inserted without its record.
func TestVerificationFindsWhatWasDoneBehindThePlatformsBack(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-tamper"
	post(t, pool, tenant, "run-t", "tamper-1", task("check the pool"))
	call(t, pool, tenant, "tamper-c1", "run-t", `{"flag":"x"}`, `{"on":true}`)
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	require.Empty(t, findings(t, a))

	tamper(t, pool,
		`UPDATE board_events SET body = '{"task":{"title":"check nothing"}}' WHERE tenant_id = 'chain-tamper' AND id = 'tamper-1'`,
		`DELETE FROM tool_call_outcomes WHERE tenant_id = 'chain-tamper' AND call_id = 'tamper-c1'`,
		`INSERT INTO tool_hides (tenant_id, endpoint, tool, pinned_hash, registration, server, reason, observed_hash, hidden_at)
VALUES ('chain-tamper', 'https://flags.internal/mcp', 'set_flag', 'p', 'r', 'flags', 'changed', 'o', now())`)

	assert.ElementsMatch(t, []string{"changed_row board_events", "missing_row tool_call_outcomes", "unchained_row tool_hides"}, findings(t, a))
}

// Records are never deleted, by the retention job either; a chain whose
// first records an operator deleted with the triggers off shows as cut.
func TestTheChainsStartCannotBeDeleted(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-prefix"
	post(t, pool, tenant, "run-p", "prefix-1", task("first"))
	post(t, pool, tenant, "run-p", "prefix-2", task("second"))
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)

	err = pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), "SET LOCAL agentswarm.retention = 'on'"); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), "DELETE FROM audit_chain WHERE tenant_id = 'chain-prefix' AND seq = 1")
		return err
	})
	require.ErrorContains(t, err, "append-only")
	tamper(t, pool,
		"DELETE FROM audit_chain WHERE tenant_id = 'chain-prefix' AND seq = 1",
		"DELETE FROM board_events WHERE tenant_id = 'chain-prefix' AND id = 'prefix-1'")

	assert.Equal(t, []string{"truncated "}, findings(t, a))
}

// Rows written while the chain trigger was off, and deleted since, still
// show: the anchor taken meanwhile counted them, and saw the trigger off.
func TestRowsWrittenUnchainedShowEvenOnceDeleted(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-unchained"
	hide(t, pool, tenant, "get_flag")
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := pool.Exec(t.Context(), "ALTER TABLE tool_hides DISABLE TRIGGER tool_hides_chain")
	require.NoError(t, err)
	enable := func() {
		_, err := pool.Exec(context.Background(), "ALTER TABLE tool_hides ENABLE TRIGGER tool_hides_chain")
		require.NoError(t, err)
	}
	t.Cleanup(enable)
	hide(t, pool, tenant, "set_flag")
	_, err = a.Anchor(t.Context())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"table tool_hides", "table tool_hides", "unchained_row tool_hides", "unchained_rows tool_hides"}, findings(t, a),
		"the table now, and as the anchor saw it")
	enable()

	tamper(t, pool, "DELETE FROM tool_hides WHERE tenant_id = 'chain-unchained' AND tool = 'set_flag'")

	assert.ElementsMatch(t, []string{"unchained_rows tool_hides", "table tool_hides"}, findings(t, a))
}

// A table that row security filters, or that is not a plain table, shows:
// verification could be shown other rows than raters see.
func TestTablesVerificationCannotTrustShow(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-tables"
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), "ALTER TABLE tool_hides ENABLE ROW LEVEL SECURITY")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "ALTER TABLE tool_hides DISABLE ROW LEVEL SECURITY")
		require.NoError(t, err)
	})

	assert.Equal(t, []string{"table tool_hides"}, findings(t, a))
}

// database opens a pool to a database of the package's container, by name.
func database(t *testing.T, name string) *pgxpool.Pool {
	t.Helper()
	u, err := neturl.Parse(pgtest.URL(t))
	require.NoError(t, err)
	u.Path = "/" + name
	pool, err := db.Open(t.Context(), u.String())
	require.NoError(t, err)
	return pool
}

// After a restore from a backup, the anchor taken next names what the chain
// no longer holds: its range, and the runs whose records went.
func TestARestoreFromABackupShowsWhatWasLost(t *testing.T) {
	admin := pgtest.Pool(t)
	ctx := t.Context()
	_, err := admin.Exec(ctx, "CREATE DATABASE restore_live")
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, name := range []string{"restore_live", "restore_backup"} {
			_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			assert.NoError(t, err)
		}
	})
	live := database(t, "restore_live")
	require.NoError(t, db.Migrate(ctx, live))
	store, err := dirstore.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	const tenant = "chain-restore"
	now := time.Now().Add(-3 * time.Hour)
	anchor := func(pool *pgxpool.Pool) {
		t.Helper()
		a, err := app.New(newChain(pool, tenant), store, tenant, func() time.Time { return now })
		require.NoError(t, err)
		_, err = a.Anchor(ctx)
		require.NoError(t, err)
		now = now.Add(time.Hour)
	}
	post(t, live, tenant, "run-early", "restore-1", task("before the backup"))
	anchor(live)
	live.Close()
	_, err = admin.Exec(ctx, "CREATE DATABASE restore_backup TEMPLATE restore_live")
	require.NoError(t, err)
	live = database(t, "restore_live")
	call(t, live, tenant, "restore-c1", "run-late", `{"flag":"x"}`, "ok")
	post(t, live, tenant, "run-later", "restore-2", task("after the backup"))
	anchor(live)
	live.Close()

	for _, sql := range []string{"DROP DATABASE restore_live", "CREATE DATABASE restore_live TEMPLATE restore_backup"} {
		_, err := admin.Exec(ctx, sql)
		require.NoError(t, err)
	}
	live = database(t, "restore_live")
	defer live.Close()
	for i := range 4 {
		post(t, live, tenant, "run-new", board.EntryID(fmt.Sprintf("restore-new-%d", i)), task("after the restore"))
	}
	anchor(live)
	a, err := app.New(newChain(live, tenant), store, tenant, func() time.Time { return now })
	require.NoError(t, err)

	got, err := a.Verify(ctx, time.Hour)

	require.NoError(t, err)
	var kinds []string
	for i := range got {
		if got[i].Kind == chain.StoreUnlocked {
			continue
		}
		kinds = append(kinds, string(got[i].Kind))
		if got[i].Kind == chain.Lost {
			assert.Equal(t, []int64{4, 4}, []int64{got[i].Seq, max(got[i].Through, got[i].Seq)})
			assert.Equal(t, []string{"run-late", "run-later"}, got[i].Runs)
		}
	}
	assert.Equal(t, []string{"lost"}, kinds)
}

// Another trigger or a rule that acts on an audited table's inserts, which
// could change or drop a row before it is chained, shows.
func TestATriggerOrARuleOnInsertsShows(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-altered"
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `
CREATE FUNCTION rewrite_body() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.body := '{}'; RETURN NEW; END; $$;
CREATE TRIGGER rewrite_body BEFORE INSERT ON board_events FOR EACH ROW EXECUTE FUNCTION rewrite_body();
CREATE RULE drop_hides AS ON INSERT TO tool_hides DO INSTEAD NOTHING;`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `
DROP RULE drop_hides ON tool_hides;
DROP TRIGGER rewrite_body ON board_events;
DROP FUNCTION rewrite_body();`)
		require.NoError(t, err)
	})

	assert.ElementsMatch(t, []string{"table board_events", "table tool_hides"}, findings(t, a))
}

// A table of an audited table's name in another schema, which the
// platform's search path could reach first and so take its writes
// unchained, shows.
func TestATableOfTheSameNameInAnotherSchemaShows(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-shadow"
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), "CREATE SCHEMA shadow; CREATE TABLE shadow.tool_hides (LIKE public.tool_hides)")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DROP SCHEMA shadow CASCADE")
		require.NoError(t, err)
	})

	assert.Equal(t, []string{"table tool_hides"}, findings(t, a))
}

// A record of a source no audited table has, as an operator can append by
// hand, shows as a record this build cannot render; it never makes an
// anchor unreadable, so later anchors still count.
func TestARogueRecordLeavesAnchorsReadable(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-rogue"
	post(t, pool, tenant, "run-r", "rogue-1", task("first"))
	_, err := pool.Exec(t.Context(), "SELECT audit_chain_append('chain-rogue', 'X', '[\"x\"]', '', 1::smallint, 'rogue')")
	require.NoError(t, err)
	now := time.Now()
	a := auditor(t, pool, tenant, &now)

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"board_events": 1, "tool_calls": 0, "tool_call_outcomes": 0, "tool_hides": 0, "config_revisions": 0, "config_objects": 0,
		"model_calls": 0, "model_call_contents": 0, "model_call_outcomes": 0}, an.Records)
	assert.Equal(t, []string{"unknown_form X"}, findings(t, a))
}

// A trigger that takes the chain trigger's name, but fires before the row
// is inserted and rewrites it, shows: the chain trigger is known by its
// kind and its function, never by its name alone.
func TestAChainTriggerImpostorShows(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-impostor"
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := pool.Exec(t.Context(), `
DROP TRIGGER board_events_chain ON board_events;
CREATE FUNCTION impostor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.body := '{"task":{"title":"forged"}}';
    PERFORM audit_chain_append(NEW.tenant_id, 'board_events', audit_key(NEW), NEW.run_id, 1::smallint, audit_form1(NEW));
    RETURN NEW;
END;
$$;
CREATE TRIGGER board_events_chain BEFORE INSERT ON board_events FOR EACH ROW EXECUTE FUNCTION impostor();`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `
DROP TRIGGER board_events_chain ON board_events;
DROP FUNCTION impostor();
CREATE TRIGGER board_events_chain AFTER INSERT ON board_events FOR EACH ROW EXECUTE FUNCTION audit_chain_board_events();`)
		require.NoError(t, err)
	})
	post(t, pool, tenant, "run-i", "impostor-1", task("check the pool"))
	_, err = a.Anchor(t.Context())
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"table board_events", "table board_events"}, findings(t, a), "now, and as the anchor saw it")
}

// One record appended by hand, with a run too long to name, never stops
// anchoring: the anchor counts the run rather than naming it, and
// verification shows that, beside the record's missing row.
func TestARunTooLongToNameLeavesAnchoringRunning(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-longrun"
	post(t, pool, tenant, "run-l", "longrun-1", task("first"))
	_, err := pool.Exec(t.Context(), "SELECT audit_chain_append('chain-longrun', 'board_events', '[\"x\"]', repeat('r', 5000000), 1::smallint, 'x')")
	require.NoError(t, err)
	now := time.Now()
	a := auditor(t, pool, tenant, &now)

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []string{"run-l"}, an.Runs)
	assert.Equal(t, int64(1), an.Omitted)
	assert.ElementsMatch(t, []string{"missing_row board_events", "runs_omitted "}, findings(t, a))
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -tags integration ./internal/audit/postgres/ ./internal/db/`
Expected: FAIL: `internal/audit/postgres` fails to build (`undefined: postgres.NewModelCalls` in `models_integration_test.go`); `internal/db` passes

- [ ] **Step 3: Add the tables**

Create `internal/db/migrations/0006_model_calls.sql`:

```sql
-- Model calls the LLM gateway forwarded, refused or failed (spec Sections 9.3
-- and 9.4; ADR-007). A call's record and its request are committed before
-- the call is forwarded, and its outcome and completion are added when it
-- ends. Requests and completions are stored redacted, apart from the
-- records, so that the retention job can delete them after [30 days] while
-- the tokens and spend stay for the cost metrics (spec Section 13.6). A call
-- with no outcome has not finished, or its outcome was never recorded: it
-- counts at its estimate, its worst case. The three tables are audit records
-- from their first row: append-only, deleted only by the retention job, and
-- chained.
CREATE TABLE model_calls (
    tenant_id      text        NOT NULL,
    call_id        text        NOT NULL,
    run_id         text        NOT NULL,
    agent          text        NOT NULL,
    round          integer     NOT NULL CHECK (round >= 0),
    class          text        NOT NULL CHECK (class IN ('primary', 'shadow')),
    model          text        NOT NULL,
    request_bytes  bigint      NOT NULL CHECK (request_bytes >= 0),
    request_digest text        NOT NULL,
    estimate       bigint      NOT NULL CHECK (estimate >= 0),
    started_at     timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, call_id)
);

-- A run's spend is the sum of its calls' weighted tokens, and the day's the
-- sum of the tenant's calls' that began that day.
CREATE INDEX model_calls_by_run ON model_calls (tenant_id, run_id);
CREATE INDEX model_calls_by_day ON model_calls (tenant_id, started_at);

-- A call's request, as it was forwarded, and its completion, redacted.
CREATE TABLE model_call_contents (
    tenant_id text NOT NULL,
    call_id   text NOT NULL,
    part      text NOT NULL CHECK (part IN ('request', 'completion')),
    content   text NOT NULL,
    PRIMARY KEY (tenant_id, call_id, part),
    FOREIGN KEY (tenant_id, call_id) REFERENCES model_calls (tenant_id, call_id)
);

CREATE TABLE model_call_outcomes (
    tenant_id             text        NOT NULL,
    call_id               text        NOT NULL,
    status                text        NOT NULL CHECK (status IN ('ok', 'refused', 'error', 'unknown')),
    reason                text        NOT NULL,
    input_tokens          bigint      NOT NULL CHECK (input_tokens >= 0),
    cache_write_5m_tokens bigint      NOT NULL CHECK (cache_write_5m_tokens >= 0),
    cache_write_1h_tokens bigint      NOT NULL CHECK (cache_write_1h_tokens >= 0),
    cache_read_tokens     bigint      NOT NULL CHECK (cache_read_tokens >= 0),
    output_tokens         bigint      NOT NULL CHECK (output_tokens >= 0),
    spend                 bigint      NOT NULL CHECK (spend >= 0),
    weighted              bigint      NOT NULL CHECK (weighted >= 0),
    attempts              integer     NOT NULL CHECK (attempts >= 0),
    details               text        NOT NULL,
    ended_at              timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, call_id),
    FOREIGN KEY (tenant_id, call_id) REFERENCES model_calls (tenant_id, call_id)
);

CREATE TRIGGER model_calls_append_only
    BEFORE UPDATE OR DELETE ON model_calls
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER model_calls_no_truncate
    BEFORE TRUNCATE ON model_calls
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER model_call_contents_append_only
    BEFORE UPDATE OR DELETE ON model_call_contents
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER model_call_contents_no_truncate
    BEFORE TRUNCATE ON model_call_contents
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER model_call_outcomes_append_only
    BEFORE UPDATE OR DELETE ON model_call_outcomes
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER model_call_outcomes_no_truncate
    BEFORE TRUNCATE ON model_call_outcomes
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();

-- Each table's key, and its columns in form 1, as migration 0005 renders
-- the other audited tables. Verification sends the same expressions itself.
CREATE FUNCTION audit_key(r model_calls) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.call_id)::text $$;

CREATE FUNCTION audit_form1(r model_calls) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.call_id, r.run_id, r.agent, r.round, r.class, r.model, r.request_bytes,
                               r.request_digest, r.estimate, (extract(epoch FROM r.started_at) * 1000000)::bigint)::text $$;

CREATE FUNCTION audit_key(r model_call_contents) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.call_id, r.part)::text $$;

CREATE FUNCTION audit_form1(r model_call_contents) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.call_id, r.part, r.content)::text $$;

CREATE FUNCTION audit_key(r model_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.call_id)::text $$;

CREATE FUNCTION audit_form1(r model_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.call_id, r.status, r.reason, r.input_tokens, r.cache_write_5m_tokens,
                               r.cache_write_1h_tokens, r.cache_read_tokens, r.output_tokens, r.spend, r.weighted,
                               r.attempts, r.details, (extract(epoch FROM r.ended_at) * 1000000)::bigint)::text $$;

-- A content's run, and an outcome's, is its call's.
CREATE FUNCTION audit_run(r model_call_contents) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT c.run_id FROM model_calls c WHERE c.tenant_id = r.tenant_id AND c.call_id = r.call_id $$;

CREATE FUNCTION audit_run(r model_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT c.run_id FROM model_calls c WHERE c.tenant_id = r.tenant_id AND c.call_id = r.call_id $$;

CREATE FUNCTION audit_chain_model_calls() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'model_calls', audit_key(NEW), NEW.run_id, 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_model_call_contents() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'model_call_contents', audit_key(NEW), audit_run(NEW), 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_model_call_outcomes() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'model_call_outcomes', audit_key(NEW), audit_run(NEW), 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

-- Every function resolves names in the system catalog first, then in the
-- schema that holds these tables, whatever the caller's search path.
DO $$
DECLARE
    f regprocedure;
BEGIN
    FOR f IN
        SELECT p.oid::regprocedure FROM pg_proc p
        WHERE p.pronamespace = (SELECT n.oid FROM pg_namespace n WHERE n.nspname = current_schema())
          AND (p.proname IN ('audit_chain_model_calls', 'audit_chain_model_call_contents', 'audit_chain_model_call_outcomes')
               OR p.proname IN ('audit_key', 'audit_form1', 'audit_run')
                  AND p.proargtypes[0] IN ('model_calls'::regtype, 'model_call_contents'::regtype, 'model_call_outcomes'::regtype))
    LOOP
        EXECUTE format('ALTER FUNCTION %s SET search_path = pg_catalog, %I', f, current_schema());
    END LOOP;
END;
$$;

CREATE TRIGGER model_calls_chain AFTER INSERT ON model_calls
    FOR EACH ROW EXECUTE FUNCTION audit_chain_model_calls();

CREATE TRIGGER model_call_contents_chain AFTER INSERT ON model_call_contents
    FOR EACH ROW EXECUTE FUNCTION audit_chain_model_call_contents();

CREATE TRIGGER model_call_outcomes_chain AFTER INSERT ON model_call_outcomes
    FOR EACH ROW EXECUTE FUNCTION audit_chain_model_call_outcomes();
```

- [ ] **Step 4: Record model calls**

Create `internal/audit/postgres/models.go`:

```go
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
)

// ModelCalls records model calls in model_calls, their requests and
// completions in model_call_contents, and how they ended in
// model_call_outcomes, and counts what they spent. It implements the LLM
// gateway's Calls port.
type ModelCalls struct {
	pool   *pgxpool.Pool
	tenant string
}

// NewModelCalls returns the record of one tenant's model calls.
func NewModelCalls(pool *pgxpool.Pool, tenant string) *ModelCalls {
	return &ModelCalls{pool: pool, tenant: tenant}
}

// Begin records a call and its request, in one transaction that commits
// before Begin returns.
func (s *ModelCalls) Begin(ctx context.Context, rec *llmapp.CallRecord) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
INSERT INTO model_calls (tenant_id, call_id, run_id, agent, round, class, model, request_bytes, request_digest, estimate, started_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			s.tenant, rec.ID, text(rec.Run), text(rec.Agent), rec.Round, string(rec.Class), text(rec.Model),
			rec.RequestBytes, rec.RequestDigest, rec.Estimate, rec.At); err != nil {
			return err
		}
		return s.content(ctx, tx, rec.ID, "request", rec.Request)
	})
	if err != nil {
		return fmt.Errorf("record model call %s: %w", rec.ID, err)
	}
	return nil
}

// End records how a call ended, and its completion, in one transaction.
// Recording the same outcome again changes nothing, so a write that timed
// out after it committed can be repeated; a different outcome is refused.
func (s *ModelCalls) End(ctx context.Context, o *llmapp.Outcome) error {
	reason := text(o.Reason)
	var recorded bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
INSERT INTO model_call_outcomes (tenant_id, call_id, status, reason, input_tokens, cache_write_5m_tokens,
    cache_write_1h_tokens, cache_read_tokens, output_tokens, spend, weighted, attempts, details, ended_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (tenant_id, call_id) DO NOTHING`,
			s.tenant, o.ID, string(o.Status), reason, o.Usage.Input, o.Usage.CacheWrite5m, o.Usage.CacheWrite1h,
			o.Usage.CacheRead, o.Usage.Output, o.Spend, o.Weighted, o.Attempts, text(o.Details), o.At)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		recorded = true
		return s.content(ctx, tx, o.ID, "completion", o.Completion)
	})
	if err != nil {
		return fmt.Errorf("record outcome of model call %s: %w", o.ID, err)
	}
	if recorded {
		return nil
	}
	return s.same(ctx, o, reason)
}

// same reports whether a call's recorded outcome is o.
func (s *ModelCalls) same(ctx context.Context, o *llmapp.Outcome, reason string) error {
	var status, recReason string
	var weighted int64
	var completion *string
	err := s.pool.QueryRow(ctx, `
SELECT o.status, o.reason, o.weighted, c.content
FROM model_call_outcomes o
LEFT JOIN model_call_contents c ON c.tenant_id = o.tenant_id AND c.call_id = o.call_id AND c.part = 'completion'
WHERE o.tenant_id = $1 AND o.call_id = $2`, s.tenant, o.ID).Scan(&status, &recReason, &weighted, &completion)
	if err != nil {
		return fmt.Errorf("read outcome of model call %s: %w", o.ID, err)
	}
	stored := ""
	if completion != nil {
		stored = *completion
	}
	if status != string(o.Status) || recReason != reason || weighted != o.Weighted || stored != text(o.Completion) {
		return fmt.Errorf("%w: %s", ErrConflict, o.ID)
	}
	return nil
}

// content records a part of a call, unless it is empty.
func (s *ModelCalls) content(ctx context.Context, tx pgx.Tx, id, part, content string) error {
	if content == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO model_call_contents (tenant_id, call_id, part, content) VALUES ($1, $2, $3, $4)`,
		s.tenant, id, part, text(content))
	return err
}

// Spent returns the cost-weighted tokens of a run's calls: what each cost
// once its outcome is recorded, and its estimate until then.
func (s *ModelCalls) Spent(ctx context.Context, run string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
SELECT coalesce(sum(coalesce(o.weighted, c.estimate)), 0)::bigint
FROM model_calls c LEFT JOIN model_call_outcomes o USING (tenant_id, call_id)
WHERE c.tenant_id = $1 AND c.run_id = $2`, s.tenant, run).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count the spend of run %s: %w", run, err)
	}
	return n, nil
}

// SpentSince returns the cost-weighted tokens of the tenant's calls that
// began at or after since, counted as Spent counts them.
func (s *ModelCalls) SpentSince(ctx context.Context, since time.Time) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
SELECT coalesce(sum(coalesce(o.weighted, c.estimate)), 0)::bigint
FROM model_calls c LEFT JOIN model_call_outcomes o USING (tenant_id, call_id)
WHERE c.tenant_id = $1 AND c.started_at >= $2`, s.tenant, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count the spend since %s: %w", since.Format(time.RFC3339), err)
	}
	return n, nil
}
```

Replace `internal/audit/postgres/calls.go` with this version, whose package comment names model calls:

```go
// Package postgres keeps the audit store's records in Postgres: tool calls
// and the tools the MCP gateway hid, model calls, and the audit chain over
// them. It implements the MCP gateway's Audit and Hides ports, the Work
// context's ReadCalls, which the board's provenance check reads, the LLM
// gateway's Calls port, and the Audit application's Chain port.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	workapp "github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// ErrConflict is returned when a call's outcome was recorded with another
// content.
var ErrConflict = errors.New("postgres: the call has another outcome")

// Calls records tool calls in tool_calls and tool_call_outcomes.
type Calls struct {
	pool   *pgxpool.Pool
	tenant string
}

// NewCalls returns the record of one tenant's tool calls.
func NewCalls(pool *pgxpool.Pool, tenant string) *Calls {
	return &Calls{pool: pool, tenant: tenant}
}

// Begin records a call. The insert commits before Begin returns.
func (s *Calls) Begin(ctx context.Context, rec *app.CallRecord) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO tool_calls (tenant_id, call_id, run_id, agent, server, tool, schema_hash, arguments, round, started_at, cause)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		s.tenant, rec.ID, string(rec.Run), text(rec.Agent), text(rec.Server), text(rec.Tool), rec.SchemaHash,
		text(string(rec.Args)), rec.Round, rec.At, rec.Cause)
	if err != nil {
		return fmt.Errorf("record call %s: %w", rec.ID, err)
	}
	return nil
}

// End records how a call ended. Recording the same outcome again changes
// nothing, so a write that timed out after it committed can be repeated; a
// different outcome is refused.
func (s *Calls) End(ctx context.Context, o *app.Outcome) error {
	output := text(o.Output)
	tag, err := s.pool.Exec(ctx, `
INSERT INTO tool_call_outcomes (tenant_id, call_id, status, output, ended_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, call_id) DO NOTHING`, s.tenant, o.ID, string(o.Status), output, o.At)
	if err != nil {
		return fmt.Errorf("record outcome of call %s: %w", o.ID, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var status, recorded string
	if err := s.pool.QueryRow(ctx, `SELECT status, output FROM tool_call_outcomes WHERE tenant_id = $1 AND call_id = $2`,
		s.tenant, o.ID).Scan(&status, &recorded); err != nil {
		return fmt.Errorf("read outcome of call %s: %w", o.ID, err)
	}
	if status != string(o.Status) || recorded != output {
		return fmt.Errorf("%w: %s", ErrConflict, o.ID)
	}
	return nil
}

// ReadCall returns a call as the board's provenance check reads it, with its
// stored arguments. A call that has not ended, was refused or ended in a
// read error has failed: none of them can be cited. A call without a server,
// a board tool's or a name nothing answers to, is no read call.
func (s *Calls) ReadCall(ctx context.Context, id string) (board.ReadCall, error) {
	var c board.ReadCall
	var run, args string
	var status, output *string
	err := s.pool.QueryRow(ctx, `
SELECT c.run_id, c.agent, c.server, c.tool, c.arguments, o.status, o.output
FROM tool_calls c LEFT JOIN tool_call_outcomes o USING (tenant_id, call_id)
WHERE c.tenant_id = $1 AND c.call_id = $2 AND c.server <> ''`, s.tenant, id).Scan(&run, &c.Agent, &c.Server, &c.Tool, &args, &status, &output)
	if errors.Is(err, pgx.ErrNoRows) {
		return board.ReadCall{}, workapp.ErrCallNotFound
	}
	if err != nil {
		return board.ReadCall{}, fmt.Errorf("read call %s: %w", id, err)
	}
	c.ID, c.Run, c.Failed, c.Args = id, board.RunID(run), true, []byte(args)
	if status != nil && *status == string(app.StatusOK) {
		c.Failed, c.Output = false, []byte(*output)
	}
	return c, nil
}

// Hides records the tools the gateway hid, in tool_hides.
type Hides struct {
	pool   *pgxpool.Pool
	tenant string
}

// NewHides returns the record of one tenant's hidden tools.
func NewHides(pool *pgxpool.Pool, tenant string) *Hides {
	return &Hides{pool: pool, tenant: tenant}
}

// Hide records a hide. Recording one again changes nothing.
func (s *Hides) Hide(ctx context.Context, h *app.Hide) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO tool_hides (tenant_id, endpoint, tool, pinned_hash, registration, server, reason, observed_hash, hidden_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT DO NOTHING`, s.tenant, h.Endpoint, h.Tool, h.PinnedHash, h.Registration, h.Server, string(h.Reason), h.Observed, h.At)
	if err != nil {
		return fmt.Errorf("record hide of %s at %s: %w", h.Tool, h.Endpoint, err)
	}
	return nil
}

// Hidden returns every recorded hide, oldest first.
func (s *Hides) Hidden(ctx context.Context) ([]app.Hide, error) {
	rows, err := s.pool.Query(ctx, `
SELECT endpoint, tool, pinned_hash, registration, server, reason, observed_hash, hidden_at
FROM tool_hides WHERE tenant_id = $1 ORDER BY hidden_at, endpoint, tool`, s.tenant)
	if err != nil {
		return nil, fmt.Errorf("read hides: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (app.Hide, error) {
		var h app.Hide
		var reason string
		err := row.Scan(&h.Endpoint, &h.Tool, &h.PinnedHash, &h.Registration, &h.Server, &reason, &h.Observed, &h.At)
		h.Reason = app.Reason(reason)
		return h, err
	})
}

// text makes s storable: Postgres text holds neither NUL characters nor
// invalid UTF-8, so each becomes U+FFFD.
func text(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	return strings.ReplaceAll(s, "\x00", "\uFFFD")
}
```

- [ ] **Step 5: Verify the new tables**

Replace `internal/audit/postgres/chain.go` with this version, whose sources hold the three tables:

```go
package postgres

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// form is how verification renders a row of one source in one form: its key
// and its columns, as migration 0005 writes them. Verification sends these
// expressions itself, so that it never relies on a function stored in the
// database it checks, and takes the text as UTF-8 bytes, so that the
// connection's encoding never changes what it hashes.
type form struct {
	key, row string
}

// source is an audited table with the forms this build renders.
type source struct {
	table string
	forms map[int16]form
}

// sources are the audited tables, in a fixed order.
var sources = []source{
	{"board_events", map[int16]form{1: {
		key: `jsonb_build_array(t.run_id, t.seq)::text`,
		row: `jsonb_build_array(t.tenant_id, t.run_id, t.seq, t.id, t.round, t.kind, t.author_kind, t.author_name,
			(extract(epoch FROM t.committed_at) * 1000000)::bigint, t.body)::text`,
	}}},
	{"tool_calls", map[int16]form{1: {
		key: `jsonb_build_array(t.call_id)::text`,
		row: `jsonb_build_array(t.tenant_id, t.call_id, t.run_id, t.agent, t.server, t.tool, t.schema_hash, t.arguments, t.round,
			(extract(epoch FROM t.started_at) * 1000000)::bigint, t.cause)::text`,
	}}},
	{"tool_call_outcomes", map[int16]form{1: {
		key: `jsonb_build_array(t.call_id)::text`,
		row: `jsonb_build_array(t.tenant_id, t.call_id, t.status, t.output, (extract(epoch FROM t.ended_at) * 1000000)::bigint)::text`,
	}}},
	{"tool_hides", map[int16]form{1: {
		key: `jsonb_build_array(t.endpoint, t.tool, t.pinned_hash, t.registration)::text`,
		row: `jsonb_build_array(t.tenant_id, t.endpoint, t.tool, t.pinned_hash, t.registration, t.server, t.reason, t.observed_hash,
			(extract(epoch FROM t.hidden_at) * 1000000)::bigint)::text`,
	}}},
	{"config_revisions", map[int16]form{1: {
		key: `jsonb_build_array(t.commit_id)::text`,
		row: `jsonb_build_array(t.tenant_id, t.commit_id, t.digest, (extract(epoch FROM t.loaded_at) * 1000000)::bigint)::text`,
	}}},
	{"config_objects", map[int16]form{1: {
		key: `jsonb_build_array(t.commit_id, t.kind, t.name)::text`,
		row: `jsonb_build_array(t.tenant_id, t.commit_id, t.kind, t.name, t.owner, t.file, t.hash, t.version)::text`,
	}}},
	{"model_calls", map[int16]form{1: {
		key: `jsonb_build_array(t.call_id)::text`,
		row: `jsonb_build_array(t.tenant_id, t.call_id, t.run_id, t.agent, t.round, t.class, t.model, t.request_bytes,
			t.request_digest, t.estimate, (extract(epoch FROM t.started_at) * 1000000)::bigint)::text`,
	}}},
	{"model_call_contents", map[int16]form{1: {
		key: `jsonb_build_array(t.call_id, t.part)::text`,
		row: `jsonb_build_array(t.tenant_id, t.call_id, t.part, t.content)::text`,
	}}},
	{"model_call_outcomes", map[int16]form{1: {
		key: `jsonb_build_array(t.call_id)::text`,
		row: `jsonb_build_array(t.tenant_id, t.call_id, t.status, t.reason, t.input_tokens, t.cache_write_5m_tokens,
			t.cache_write_1h_tokens, t.cache_read_tokens, t.output_tokens, t.spend, t.weighted,
			t.attempts, t.details, (extract(epoch FROM t.ended_at) * 1000000)::bigint)::text`,
	}}},
}

// Chain reads one tenant's audit chain and the rows it covers, in the
// schema that holds the platform's tables. It implements the Audit
// application's Chain port.
type Chain struct {
	pool   *pgxpool.Pool
	tenant string
	schema string
}

// NewChain returns the reader of one tenant's chain in schema.
func NewChain(pool *pgxpool.Pool, tenant, schema string) *Chain {
	return &Chain{pool: pool, tenant: tenant, schema: schema}
}

// Read calls fn with a view of one snapshot: a read-only transaction at
// repeatable read, whose search path holds only the system catalog, so
// that every name it reads is the one it means, and whose text arrives as
// UTF-8 whatever the connection asked for. Its joins hash rather than loop,
// so a whole chain is read in time linear in its size, also before the
// tables have statistics.
func (c *Chain) Read(ctx context.Context, fn func(app.View) error) error {
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin a snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, set := range []string{"SET LOCAL search_path = pg_catalog", "SET LOCAL client_encoding = 'UTF8'", "SET LOCAL enable_nestloop = off"} {
		if _, err := tx.Exec(ctx, set); err != nil {
			return fmt.Errorf("set up the snapshot: %w", err)
		}
	}
	return fn(&view{tx: tx, tenant: c.tenant, schema: c.schema})
}

type view struct {
	tx     pgx.Tx
	tenant string
	schema string
}

// table returns a table's name, qualified with the schema and quoted.
func (v *view) table(name string) string { return pgx.Identifier{v.schema, name}.Sanitize() }

func (v *view) selectLinks() string {
	return "SELECT seq, source, key, run_id, form, digest, prev, hash, chained_at FROM " + v.table("audit_chain") + " WHERE tenant_id = $1"
}

func (v *view) Head(ctx context.Context) (*chain.Link, error) {
	rows, err := v.tx.Query(ctx, v.selectLinks()+" ORDER BY seq DESC LIMIT 1", v.tenant)
	if err != nil {
		return nil, fmt.Errorf("read the chain's head: %w", err)
	}
	links, err := pgx.CollectRows(rows, scanLink)
	if err != nil {
		return nil, fmt.Errorf("read the chain's head: %w", err)
	}
	if len(links) == 0 {
		return nil, nil
	}
	return &links[0], nil
}

func (v *view) HashAt(ctx context.Context, seq int64) (chain.Sum, bool, error) {
	var hash []byte
	err := v.tx.QueryRow(ctx, "SELECT hash FROM "+v.table("audit_chain")+" WHERE tenant_id = $1 AND seq = $2", v.tenant, seq).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return chain.Sum{}, false, nil
	}
	if err != nil {
		return chain.Sum{}, false, fmt.Errorf("read the record at seq %d: %w", seq, err)
	}
	var s chain.Sum
	if len(hash) != len(s) {
		return chain.Sum{}, false, fmt.Errorf("the record at seq %d holds a hash of the wrong length", seq)
	}
	copy(s[:], hash)
	return s, true, nil
}

func (v *view) Runs(ctx context.Context, after, upTo int64) ([]string, error) {
	rows, err := v.tx.Query(ctx, "SELECT DISTINCT run_id FROM "+v.table("audit_chain")+
		" WHERE tenant_id = $1 AND seq > $2 AND seq <= $3 AND run_id <> ''", v.tenant, after, upTo)
	if err != nil {
		return nil, fmt.Errorf("read the chain's runs: %w", err)
	}
	runs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the chain's runs: %w", err)
	}
	slices.Sort(runs)
	return runs, nil
}

func (v *view) Counts(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, s := range sources {
		var n int64
		if err := v.tx.QueryRow(ctx, "SELECT count(*) FROM "+v.table(s.table)+" WHERE tenant_id = $1", v.tenant).Scan(&n); err != nil {
			return nil, fmt.Errorf("count the rows of %s: %w", s.table, err)
		}
		out[s.table] = n
	}
	return out, nil
}

// Records counts the chain's records of each audited table, and of no
// other source: a record of a source this build does not know is
// verification's to report, and must not make an anchor unreadable.
func (v *view) Records(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		out[s.table] = 0
		names = append(names, s.table)
	}
	rows, err := v.tx.Query(ctx, "SELECT source, count(*) FROM "+v.table("audit_chain")+
		" WHERE tenant_id = $1 AND source = ANY($2) GROUP BY source", v.tenant, names)
	if err != nil {
		return nil, fmt.Errorf("count the chain's records: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source string
		var n int64
		if err := rows.Scan(&source, &n); err != nil {
			return nil, fmt.Errorf("count the chain's records: %w", err)
		}
		out[source] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count the chain's records: %w", err)
	}
	return out, nil
}

func (v *view) Links(ctx context.Context, fn func(*chain.Link) error) error {
	rows, err := v.tx.Query(ctx, v.selectLinks()+" ORDER BY seq", v.tenant)
	if err != nil {
		return fmt.Errorf("read the chain: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return fmt.Errorf("read the chain: %w", err)
		}
		if err := fn(&l); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the chain: %w", err)
	}
	return nil
}

func scanLink(row pgx.CollectableRow) (chain.Link, error) {
	var l chain.Link
	var digest, prev, hash []byte
	var at time.Time
	if err := row.Scan(&l.Seq, &l.Source, &l.Key, &l.Run, &l.Form, &digest, &prev, &hash, &at); err != nil {
		return chain.Link{}, err
	}
	if len(digest) != len(l.Digest) || len(prev) != len(l.Prev) || len(hash) != len(l.Hash) {
		return chain.Link{}, fmt.Errorf("record %d holds a hash of the wrong length", l.Seq)
	}
	copy(l.Digest[:], digest)
	copy(l.Prev[:], prev)
	copy(l.Hash[:], hash)
	l.ChainedAt = at.UTC()
	return l, nil
}

// Rows renders each record's row as its form says, source by source; a
// record of a form or source this build does not know comes last.
func (v *view) Rows(ctx context.Context, fn func(*app.Row) error) error {
	var known strings.Builder
	for _, s := range sources {
		for _, n := range slices.Sorted(maps.Keys(s.forms)) {
			f := s.forms[n]
			fmt.Fprintf(&known, " AND NOT (c.source = '%s' AND c.form = %d)", s.table, n)
			query := fmt.Sprintf(`
SELECT c.seq, c.source, c.key, c.form, c.digest, t.rendered
FROM %s c
LEFT JOIN (SELECT %s AS key, convert_to(%s, 'UTF8') AS rendered FROM %s t WHERE t.tenant_id = $1) t ON t.key = c.key
WHERE c.tenant_id = $1 AND c.source = '%s' AND c.form = %d
ORDER BY c.seq`, v.table("audit_chain"), f.key, f.row, v.table(s.table), s.table, n)
			if err := v.rows(ctx, query, true, fn); err != nil {
				return err
			}
		}
	}
	return v.rows(ctx, "SELECT c.seq, c.source, c.key, c.form, c.digest, NULL::bytea FROM "+v.table("audit_chain")+
		" c WHERE c.tenant_id = $1"+known.String()+" ORDER BY c.seq", false, fn)
}

func (v *view) rows(ctx context.Context, query string, known bool, fn func(*app.Row) error) error {
	rows, err := v.tx.Query(ctx, query, v.tenant)
	if err != nil {
		return fmt.Errorf("read the chained rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r := app.Row{Known: known}
		var digest []byte
		if err := rows.Scan(&r.Seq, &r.Source, &r.Key, &r.Form, &digest, &r.Rendered); err != nil {
			return fmt.Errorf("read the chained rows: %w", err)
		}
		copy(r.Digest[:], digest)
		if err := fn(&r); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the chained rows: %w", err)
	}
	return nil
}

func (v *view) Unchained(ctx context.Context) ([]app.Row, error) {
	var out []app.Row
	for _, s := range sources {
		key := s.forms[1].key
		query := fmt.Sprintf(`
SELECT %s FROM %s t
WHERE t.tenant_id = $1 AND NOT EXISTS (
    SELECT 1 FROM %s c WHERE c.tenant_id = $1 AND c.source = '%s' AND c.key = %s)`, key, v.table(s.table), v.table("audit_chain"), s.table, key)
		rows, err := v.tx.Query(ctx, query, v.tenant)
		if err != nil {
			return nil, fmt.Errorf("read the unchained rows of %s: %w", s.table, err)
		}
		keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, fmt.Errorf("read the unchained rows of %s: %w", s.table, err)
		}
		for _, k := range keys {
			out = append(out, app.Row{Source: s.table, Key: k})
		}
	}
	return out, nil
}

// Tables reads, from the system catalog, how each audited table and the
// chain itself are held: a plain table, without row security, with no
// trigger but its chain trigger and no rule acting on its inserts, and, for
// an audited table, with its chain trigger enabled. The chain trigger is
// known by its name, its kind, after each inserted row, and its function,
// never by its name alone. A relation of the same name in another schema,
// which the platform's search path could reach before its own, counts
// against its table as a filter.
// chainTrigger is true of the trigger g that migration 0005 made for the
// table c: named <table>_chain, firing after each inserted row (tgtype 5:
// row and insert), and calling audit_chain_<table>() in the schema $1.
const chainTrigger = `g.tgname = c.relname || '_chain' AND g.tgtype = 5
             AND g.tgfoid = to_regprocedure(format('%I.%I()', $1::text, 'audit_chain_' || c.relname))`

func (v *view) Tables(ctx context.Context) ([]app.Table, error) {
	names := []string{"audit_chain"}
	for _, s := range sources {
		names = append(names, s.table)
	}
	rows, err := v.tx.Query(ctx, `
SELECT c.relname, c.relkind = 'r',
       c.relrowsecurity OR c.relforcerowsecurity OR EXISTS (
           SELECT 1 FROM pg_class o JOIN pg_namespace m ON m.oid = o.relnamespace
           WHERE o.relname = c.relname AND m.nspname <> $1 AND m.nspname NOT IN ('pg_catalog', 'information_schema')),
       c.relname = 'audit_chain' OR EXISTS (
           SELECT 1 FROM pg_trigger g
           WHERE g.tgrelid = c.oid AND g.tgenabled IN ('O', 'A') AND `+chainTrigger+`),
       EXISTS (
           SELECT 1 FROM pg_trigger g
           WHERE g.tgrelid = c.oid AND NOT g.tgisinternal AND g.tgenabled <> 'D'
             AND (g.tgtype::int & 4) <> 0 AND NOT (`+chainTrigger+`))
       OR EXISTS (SELECT 1 FROM pg_rewrite w WHERE w.ev_class = c.oid)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = ANY($2)`, v.schema, names)
	if err != nil {
		return nil, fmt.Errorf("read the audited tables: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (app.Table, error) {
		var t app.Table
		err := row.Scan(&t.Name, &t.Plain, &t.Filtered, &t.Chains, &t.Altered)
		return t, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the audited tables: %w", err)
	}
	out := make([]app.Table, 0, len(names))
	for _, name := range names {
		i := slices.IndexFunc(found, func(t app.Table) bool { return t.Name == name })
		if i < 0 {
			out = append(out, app.Table{Name: name})
			continue
		}
		out = append(out, found[i])
	}
	return out, nil
}
```

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test -tags integration ./internal/audit/postgres/ ./internal/db/ && golangci-lint run ./...`
Expected: both packages `ok`; `0 issues.`

- [ ] **Step 7: Commit**

```bash
git add internal/db/migrations/0006_model_calls.sql internal/audit/postgres
git commit -m "feat(audit): record model calls, their contents apart, chained from their first row"
```

### Task 7: The agent-facing endpoint

Decisions 11 and 12, over HTTP, and the contract with the runtime's client: the SDK, and the platform's own runtime, through the gateway, to a vendor that answers as the platform's model does.

**Files:**
- Create: `internal/llm/endpoint/endpoint.go`, `internal/llm/endpoint/endpoint_test.go`

**Interfaces:**
- Consumes: Tasks 4 and 5; `memory`; `runner.Run` (Plan 3b); anthropic-sdk-go v1.80.0.
- Produces: `func endpoint.New(gw Gateway, maxBodyBytes int) http.Handler`.

- [ ] **Step 1: Write the failing tests**

Create `internal/llm/endpoint/endpoint_test.go`:

```go
package endpoint_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	llmanthropic "github.com/jchen0426/agent-swarm/internal/llm/anthropic"
	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/llm/endpoint"
	"github.com/jchen0426/agent-swarm/internal/llm/memory"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
	"github.com/jchen0426/agent-swarm/internal/runner"
)

const (
	model  = "claude-haiku-5-5"
	commit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	token  = "session-token"
)

type revisions struct{ rev *config.Revision }

func (r *revisions) At(context.Context, string) (*config.Revision, error) { return r.rev, nil }

type key struct{}

func (key) Secret(context.Context, string) (string, error) { return "vendor-key", nil }

type ids struct{ n atomic.Int32 }

func (i *ids) NewID() string { return fmt.Sprintf("m-%d", i.n.Add(1)) }

// vendor is the model vendor's Messages API, scripted: it answers the n-th
// request with the n-th reply, the last repeating, and keeps each
// request's headers and body.
type vendor struct {
	mu      sync.Mutex
	replies []string
	headers []http.Header
	bodies  []string
	// slow holds the first answer back this long.
	slow time.Duration
	// active counts the requests under way, and most the most at once.
	active, most atomic.Int32
}

func (v *vendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	active := v.active.Add(1)
	defer v.active.Add(-1)
	for {
		most := v.most.Load()
		if active <= most || v.most.CompareAndSwap(most, active) {
			break
		}
	}
	b, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	n := len(v.bodies)
	v.headers, v.bodies = append(v.headers, r.Header.Clone()), append(v.bodies, string(b))
	reply, slow := v.replies[min(n, len(v.replies)-1)], v.slow
	v.mu.Unlock()
	if n == 0 {
		time.Sleep(slow)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Request-Id", fmt.Sprintf("req_%d", n+1))
	_, _ = io.WriteString(w, reply)
}

// signature stands in for a thinking block's signature, as long as the
// model's are.
var signature = strings.Repeat("CAQSjQUKEAgS", 73)

// message is the vendor's answer that calls tool with input, in the shape
// the platform's model answers in: a thinking block with no text and a
// signature first, a tool call with its caller, and every field of usage
// the vendor reports.
func message(n int, tool, input string) string {
	return fmt.Sprintf(`{"id": "msg_01%024d", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
 "content": [{"type": "thinking", "thinking": "", "signature": "%s%d"},
  {"type": "tool_use", "id": "toolu_01%022d", "name": "%s", "input": %s, "caller": {"type": "direct"}}],
 "stop_reason": "tool_use", "stop_sequence": null, "container": null, "diagnostics": null, "stop_details": null,
 "usage": {"input_tokens": 4, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 5228,
  "cache_creation": {"ephemeral_5m_input_tokens": 0, "ephemeral_1h_input_tokens": 0}, "output_tokens": 180,
  "output_tokens_details": {"thinking_tokens": 24}, "service_tier": "standard", "inference_geo": "global"}}`, n, signature, n, n, tool, input)
}

type env struct {
	vendor   *vendor
	calls    *memory.Calls
	sessions *memory.Sessions
	session  app.Session
	served   atomic.Int32
	url      string
	client   anthropic.Client
}

// newEnv puts the gateway between the SDK's client and a scripted vendor:
// client → endpoint → gateway → the vendor adapter → the vendor.
func newEnv(t *testing.T, replies ...string) *env {
	t.Helper()
	e := &env{vendor: &vendor{replies: replies}, calls: memory.NewCalls(), sessions: memory.NewSessions()}
	vendorSrv := httptest.NewServer(e.vendor)
	t.Cleanup(vendorSrv.Close)
	v, err := llmanthropic.New(&llmanthropic.Config{BaseURL: vendorSrv.URL, KeyRef: "secret://llm/vendor", Version: "2023-06-01"}, key{})
	require.NoError(t, err)
	cc, err := celeval.New()
	require.NoError(t, err)
	fsys, err := memfs.New(map[string][]byte{
		"redaction-rules/api-keys.yaml": []byte("kind: RedactionRule\nmetadata: {name: api-keys, owner: security@example.com}\nspec:\n  always: true\n  patterns: ['sk-[a-z0-9]{8}']\n"),
		"agents/flag-investigator.yaml": []byte("kind: Agent\nmetadata: {name: flag-investigator, owner: alice@example.com}\nspec:\n  runtime: thinloop\n  model: claude-haiku-5-5\n  tools: []\n"),
	})
	require.NoError(t, err)
	rev, err := config.Build(fsys, commit, cc)
	require.NoError(t, err)
	opts := app.Options{MaxBodyBytes: 512 << 10, MaxTokens: 16384, Version: "2023-06-01",
		Limits: cost.Limits{DailyCap: 10_000_000, ShadowCut: 80}, Reference: 100_000,
		Cards:   map[string]cost.Card{model: {Base: cost.Rates{Input: 100_000, Output: 500_000, CacheWrite5m: 125_000, CacheWrite1h: 200_000, CacheRead: 10_000}}},
		Vendor:  limits.Settings{Slots: 10, Failures: 3, CoolDown: time.Minute},
		Backoff: []time.Duration{time.Millisecond}, VendorTimeout: 10 * time.Second, RecordTimeout: time.Second, MaxRefusals: 10,
		LastCallWait: 10 * time.Second}
	gw, err := app.New(&app.Ports{Sessions: e.sessions, Revisions: &revisions{rev}, Vendor: v, Calls: e.calls, IDs: &ids{}, Now: time.Now}, &opts)
	require.NoError(t, err)
	e.session = app.Session{Run: "run-1", Agent: "flag-investigator", Commit: commit, Round: 1, Class: cost.Primary,
		Budget: 2_000_000, Deadline: time.Now().Add(time.Minute)}
	e.sessions.Put(token, &e.session)
	handler := endpoint.New(gw, opts.MaxBodyBytes)
	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.served.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(gatewaySrv.Close)
	e.url = gatewaySrv.URL
	e.client = anthropic.NewClient(option.WithBaseURL(gatewaySrv.URL), option.WithAPIKey(token))
	return e
}

// The SDK, given the gateway as its base URL and the session's token as its
// key, calls the vendor through it: the vendor gets the deployment's key and
// the gateway's version, and none of the client's headers.
func TestTheSDKCallsTheVendorThroughTheGateway(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{"q": "errors"}`))

	msg, err := e.client.Messages.New(t.Context(), anthropic.MessageNewParams{
		Model: model, MaxTokens: 1024,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("Find why checkout fails."))},
	})

	require.NoError(t, err)
	assert.Equal(t, anthropic.StopReasonToolUse, msg.StopReason)
	require.Len(t, e.vendor.headers, 1)
	h := e.vendor.headers[0]
	assert.Equal(t, "vendor-key", h.Get("X-Api-Key"))
	assert.Equal(t, "2023-06-01", h.Get("Anthropic-Version"))
	for name := range h {
		assert.False(t, strings.HasPrefix(name, "X-Stainless"), name)
	}
	assert.NotContains(t, e.vendor.bodies[0], token)
	records := e.calls.Records()
	require.Len(t, records, 1)
	_, o, _, ended := e.calls.Record(records[0].ID)
	require.True(t, ended)
	assert.Equal(t, app.StatusOK, o.Status)
}

// A client may carry the session's token as a bearer token instead of as
// its key.
func TestTheTokenMayBeABearerToken(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{}`))
	client := anthropic.NewClient(option.WithBaseURL(e.url), option.WithAuthToken(token))

	_, err := client.Messages.New(t.Context(), anthropic.MessageNewParams{
		Model: model, MaxTokens: 1024,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})

	require.NoError(t, err)
	assert.Equal(t, "vendor-key", e.vendor.headers[0].Get("X-Api-Key"))
	assert.Empty(t, e.vendor.headers[0].Get("Authorization"))
}

// The SDK does not repeat a request the gateway refused.
func TestTheSDKDoesNotRepeatARefusal(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{}`))
	e.session.Budget = 0
	e.sessions.Put(token, &e.session)

	_, err := e.client.Messages.New(t.Context(), anthropic.MessageNewParams{
		Model: model, MaxTokens: 1024,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})

	var apiErr *anthropic.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.Equal(t, int32(1), e.served.Load())
	assert.Empty(t, e.vendor.bodies)
}

// A request larger than the gateway reads is refused, and recorded at the
// size it declared.
func TestARequestTooLargeIsRecordedAtItsSize(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{}`))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.url+"/v1/messages", strings.NewReader(strings.Repeat(" ", 600<<10)))
	require.NoError(t, err)
	req.Header.Set("X-Api-Key", token)

	resp, err := http.DefaultClient.Do(req)

	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	records := e.calls.Records()
	require.Len(t, records, 1)
	assert.Equal(t, int64(600<<10), records[0].RequestBytes)
}

// A request with a query string is refused and recorded, whatever the
// query asks for: the SDK's beta methods add one.
func TestAQueryStringIsRefused(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{}`))

	_, err := e.client.Messages.New(t.Context(), anthropic.MessageNewParams{
		Model: model, MaxTokens: 1024,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	}, option.WithQuery("beta", "true"))

	var apiErr *anthropic.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.StatusCode)
	assert.Empty(t, e.vendor.bodies)
	records := e.calls.Records()
	require.Len(t, records, 1)
	_, o, _, ended := e.calls.Record(records[0].ID)
	require.True(t, ended)
	assert.Equal(t, app.StatusRefused, o.Status)
}

// A client whose connection drops while its call is under way, and which
// sends the request again, as the SDK does, waits for the first call to
// end: the vendor never has two of a session's calls at once. The second
// call is made, and paid for, too: the runtime's client is to make no
// attempts of its own (Plan 7).
func TestARequestSentAgainWaitsForTheCallItRepeats(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{}`), message(2, "metrics__query", `{}`))
	e.vendor.mu.Lock()
	e.vendor.slow = time.Second
	e.vendor.mu.Unlock()
	var dropped atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if dropped.CompareAndSwap(false, true) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
		}
		body, _ := io.ReadAll(r.Body)
		req, err := http.NewRequestWithContext(ctx, r.Method, e.url+r.URL.Path, bytes.NewReader(body))
		if !assert.NoError(t, err) {
			return
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		maps.Copy(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)
	client := anthropic.NewClient(option.WithBaseURL(proxy.URL), option.WithAPIKey(token))

	_, err := client.Messages.New(t.Context(), anthropic.MessageNewParams{
		Model: model, MaxTokens: 1024,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})

	require.NoError(t, err)
	assert.Equal(t, int32(1), e.vendor.most.Load(), "never two of the session's calls at once")
	assert.Len(t, e.calls.Records(), 2)
}

// toolbox has one tool, which answers with a secret, and the end tool.
type toolbox struct{}

func (toolbox) Tools(context.Context) ([]runner.Tool, error) {
	return []runner.Tool{
		{Name: "metrics__query", Description: "Query metrics.", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)},
		{Name: "end_turn", Description: "End your turn.", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}, nil
}

func (toolbox) Call(_ context.Context, name string, _ json.RawMessage) (output string, isError bool, err error) {
	switch name {
	case "metrics__query":
		return "errors rose at 10:02; token sk-aaaaaaaa", false, nil
	case "end_turn":
		return "your turn has ended", false, nil
	}
	return "", false, errors.New("no such tool")
}

// The platform's runtime plays a whole turn through the gateway, against
// answers in the shape the platform's model gives them: every request it
// sends, the answer it echoes back with its thinking block and its tool
// call's caller included, is within the allowlist, and what a tool
// returned reaches the vendor redacted.
func TestARunnerTurnPassesTheAllowlist(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{"q": "errors"}`), message(2, "end_turn", `{}`))

	res, err := runner.Run(t.Context(), &e.client, toolbox{}, &runner.Config{
		Model: model, MaxTokens: 1024, MaxSteps: 5, System: "You investigate incidents.",
	}, "Find why checkout fails.")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByTool, res.Outcome)
	require.Len(t, e.vendor.bodies, 2)
	assert.NotContains(t, e.vendor.bodies[1], "sk-aaaaaaaa")
	assert.Contains(t, e.vendor.bodies[1], "[redacted:api-keys]")
	for _, rec := range e.calls.Records() {
		_, o, _, _ := e.calls.Record(rec.ID)
		assert.Equal(t, app.StatusOK, o.Status, o.Reason)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/llm/endpoint/`
Expected: FAIL: `no non-test Go files in .../internal/llm/endpoint`

- [ ] **Step 3: Serve the endpoint**

Create `internal/llm/endpoint/endpoint.go`:

```go
// Package endpoint is the LLM gateway's agent-facing endpoint: the base URL
// a runtime's Messages API client is given (spec Section 2). Each request
// carries the session's token, as the client's API key or as a bearer
// token; the token names the run and the agent, and nothing in a request's
// content does. The endpoint reads a request and leaves every decision to
// the gateway, which answers it.
package endpoint

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/jchen0426/agent-swarm/internal/llm/app"
)

// Gateway is what the endpoint serves.
type Gateway interface {
	Messages(ctx context.Context, in *app.Inbound) *app.Reply
}

// New returns the endpoint's HTTP handler. It reads at most maxBodyBytes
// and one byte more of a request, so that the gateway can refuse a larger
// one.
func New(gw Gateway, maxBodyBytes int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Api-Key")
		if token == "" {
			token, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBodyBytes)+1))
		if err != nil {
			http.Error(w, "the request could not be read", http.StatusBadRequest)
			return
		}
		reply := gw.Messages(r.Context(), &app.Inbound{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Token: token, Header: r.Header, Body: body, Length: r.ContentLength,
		})
		for name, value := range reply.Header {
			w.Header().Set(name, value)
		}
		w.WriteHeader(reply.Status)
		_, _ = w.Write(reply.Body) //nolint:gosec // G705: a JSON answer to an API client, served as application/json, never a page
	})
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test -race ./internal/llm/endpoint/ && golangci-lint run ./...`
Expected: `ok`; `0 issues.`

- [ ] **Step 5: Run everything**

Run: `make lint test-all`
Expected: `0 issues.`, every package `ok`, the e2e tests `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/endpoint
git commit -m "feat(llm): the LLM gateway's endpoint, and its contract with the runtime's client"
```

### Task 8: Documentation

Decisions 1, 6, 7 and 10 amend the spec; ADR-007 records that the gateway is built, and ADR-015 the seventh context; the roadmap and the README say Plan 6b is done; and a label in Plan 6a's table of assumptions is corrected.

**Files:**
- Modify: `plan/2026-10-11-agent-swarm-platform-spec.md`, `plan/adr/ADR-007-model-vendor-single-gateway.md`, `plan/adr/ADR-015-go-ddd-ports-adapters.md`, `plan/2026-10-12-implementation-roadmap.md`, `README.md`, `plan/2026-10-15-plan-6a-audit-chain.md`, and this plan

Write DATE as the date the plan runs.

- [ ] **Step 1: Amend the spec**

In `plan/2026-10-11-agent-swarm-platform-spec.md`:

1. In the status row of the header table, after "the audit chain and its anchor (Sections 2, 3.3, 8.4 and 9.4; Plan 6a).", add " Amended on DATE: the LLM gateway (Sections 2.2, 5.6, 9.3, 9.4 and 15; Plan 6b)."
2. In Section 2.2's table of contexts, after the row of Measurement, add:

   ```markdown
   | LLM | The LLM gateway's request policy, the models' prices, and the token budgets and daily cap it enforces | Supporting |
   ```

3. In Section 5.6, in the paragraph that begins "**Budget.**", replace "The token budget counts cost-weighted tokens as the LLM gateway prices them: uncached input, cache reads and output." with "The token budget counts cost-weighted tokens: what a call costs at its model's prices, cache writes and reads included, at the price of one reference token, so that a budget bounds money whichever model an agent uses. A call counts at its worst case until its cost is known. A run may pass its budget by the model calls its agents start while it is under it, one per agent at most."
4. In Section 9.3, replace "It logs prompts and completions, redacted, only into the audit store (Section 9.4)." with "In Phase 0 the list is what the platform's runtime sends; the thinking blocks of the model's own answers come back unchanged, and only to the session that got them; an id or a name that holds what a redaction rule hides is refused. It logs prompts and completions, redacted, only into the audit store (Section 9.4), and returns a completion to the agent as the vendor sent it."
5. In Section 9.4's retention table, replace the row that begins "| Ruling, configuration and access records" with:

   ```markdown
   | Ruling, configuration, access and model-call records, without output content | [1 year] |
   ```

6. In Section 15's table, replace the row of the daily token cap with these five rows:

   ```markdown
   | Daily token cap | Set by the deployment, in cost-weighted tokens, counting the calls that began each UTC day | 7, 8.2 |
   | LLM gateway: request size / max_tokens / requests at once per session / refused requests per session per round | [512 KiB] / [16,384] / [1] / [10] | 9.3 |
   | LLM gateway: thinking blocks kept per session / sessions kept | [1,024] / [1,024] | 9.3 |
   | LLM gateway: wait for a session's call that outlived its request / record timeout | [15 s] / [5 s] | 8.2 |
   | LLM gateway: vendor calls at once, none kept for platform reads / degraded after failures / for / call timeout | [10] / [3] / [60 s] / [10 min] | 8.2 |
   ```

- [ ] **Step 2: Record the gateway in ADR-007, and the context in ADR-015**

In `plan/adr/ADR-007-model-vendor-single-gateway.md`, replace the status row with `| Status | Accepted. The gateway is built (Plan 6b). |`, and at the end of its History add: "- **DATE:** built in Plan 6b: an allowlist of what the runtime sends, the models' prices as the vendor charges them, budgets and the daily cap in cost-weighted tokens at a reference price, and every call recorded in the chained audit store."

In `plan/adr/ADR-015-go-ddd-ports-adapters.md`, in Decision 2, replace "Access and Audit (generic)." with "Access and Audit (generic), and LLM (supporting), which Plan 6b adds for the LLM gateway.", and at the end of its History add: "- **DATE:** the LLM context added by Plan 6b."

- [ ] **Step 3: Mark Plan 6b executed in the roadmap**

In `plan/2026-10-12-implementation-roadmap.md`:

1. In the header table, after the row of Plan 6a, add `| Plan 6b | \`2026-10-17-plan-6b-llm-gateway.md\` (executed) |`.
2. In Section 4, replace "The six contexts of spec Section 2.2 are confirmed: Work and Actions (core), Registry and Measurement (supporting), Access and Audit (generic)." with "The contexts of spec Section 2.2 are confirmed: Work and Actions (core), Registry, Measurement and LLM (supporting), Access and Audit (generic). Plan 6b added LLM."
3. In Section 5, after the sentence on Plan 6a, add: "Plan 6b, on DATE, built the LLM gateway: it forwards only what the platform's runtime sends, redacted, records every model call in the chained audit store, and holds runs to their budgets and the deployment to its daily cap, priced as the vendor charges."

- [ ] **Step 4: Update the README**

In `README.md`, replace "and Plan 6a the audit chain; Plan 6b, the LLM gateway, is next." with "Plan 6a the audit chain, and Plan 6b the LLM gateway; Plan 7, the coordinator, is next.", and after the line for Plan 6a in the list of plans add `- [Plan 6b: the LLM gateway](plan/2026-10-17-plan-6b-llm-gateway.md) (executed)`.

- [ ] **Step 5: Correct Plan 6a's table of assumptions**

In `plan/2026-10-15-plan-6a-audit-chain.md`, the second row of "Assumptions taken for the team (ADR-016)" is not onboarding question 13, which asks about message channels. Replace its first cell, `13: the deployment`, with `None: the deployment`.

- [ ] **Step 6: Mark this plan executed**

In this plan's header table, set the status to "Executed on DATE, inline on main. See "Changes made during execution" at the end.", and add that section at the end: the rulings made during execution, and, after the final review, its findings and what became of each.

- [ ] **Step 7: Commit**

```bash
git add plan/2026-10-11-agent-swarm-platform-spec.md plan/adr/ADR-007-model-vendor-single-gateway.md plan/adr/ADR-015-go-ddd-ports-adapters.md plan/2026-10-12-implementation-roadmap.md README.md plan/2026-10-15-plan-6a-audit-chain.md plan/2026-10-17-plan-6b-llm-gateway.md
git commit -m "docs: Plan 6b executed; spec amended for the LLM gateway"
```

## Changes made during execution

Tasks 1 to 7 ran on main on 2026-10-18, each task by a fresh implementer and checked by a fresh reviewer: each task's review ran on Opus, and each fix round's scoped re-review on Sonnet. Every file a task wrote from this plan's code matched the replay-verified scratch copy byte for byte before its review. Each task's tests failed before its implementation, as its Step 2 expects, and passed after it, and lint stayed at `0 issues.`. `make lint test-all` passed after Task 7's endpoint and again after its live check (below), the end-to-end tests included; Task 7's fix round changed only the endpoint's tests and the Makefile, and the endpoint's tests and lint passed after it.

Every task's review found at least one issue to fix in this plan's code, or caused by it. The fixes were made test first, each test failing before its fix and passing after it, in one to three rounds, until a re-review found the task clean. Two fixes had no such test: e5f89fd changed only a comment, and 90ac9db only the Makefile's guard of the live check. Other minor findings were deferred. The work is the thirty commits from ca50f9e to 90ac9db.

**Decisions that changed.**

- **Decision 5, escapes in a request (Task 1).** A request in which a JSON escape hides what a rule matches is refused, wherever the model would read it: text, or a tool's input or schema. Examples are `s\u006b-…`, where a rule matches `sk-…`, and the session's token with its dash escaped. Patterns match decoded text, and redaction cannot change what an escape hides; the MCP gateway refuses such output for the same reason (spec Section 9.4). The record of a refused request that hides one keeps none of its content. (8a81093; `TestAnEscapeThatHidesAMatchIsRefused`, `TestARecordHoldsNoMatchAnEscapeHides`)
- **Decision 10, refusal reasons (Tasks 1 and 4).** A refusal's reason quotes the client's text only when it is whole, at most 64 bytes, and needs no escape; otherwise it gives only the text's length (`prompt.Quote`, used for the method and the path too). The gateway redacts the reason it stores, and a cut or escaped copy can hide from the rules a match the whole text holds. A long method had also let a sandbox store up to about 1 MiB a refusal. (8a81093, 0defd2a, 52c029b; `TestARefusalSaysOnlyTheLengthOfALongOrEscapedValue`, `TestARefusedPathIsQuotedOnlyWhole`, `TestARefusedMethodIsQuotedOnlyWhole`)
- **Decision 10, completions and failures (Task 2).** A completion, or a vendor's failure body, whose text hides what a rule matches behind an escape is not stored. A failure body that is JSON is recorded only as redaction reads JSON, value by value: read as text, a rule anchored to a whole value would miss the values inside it. (35d7716, 42d9a81; `TestAnAnswerHidingAMatchHasNoRecord`, `TestAFailureHidingAMatchIsNotRecorded`, `TestAnAnchoredMatchInJSONHiddenByAnEscapeIsNotRecorded`)
- **The shared redaction kernel (Task 2).** `redact.EscapedValue` also checks each member written as key=value with a scalar value, as `Value` applies member rules to it, and skips only a member whose value is exactly a rule's replacement. This changes the MCP gateway too: output with a member whose key, once the JSON is read, still holds an escape that decodes to `password`, under a value the member rule matches, is now a read error, as spec Section 9.4 requires. Two earlier forms failed re-review: the first also flagged members that redaction had already hidden, under a key with an escape, and the guard added for those let a key with a decoy prefix through. (9f713e8, e0e108b, 8aee818; `TestEscapedValueChecksAMemberWrittenAsKeyEqualsValue`, `TestAMemberRedactedUnderAnEscapedKeyIsNotFlagged`, `TestADecoyBeforeAnEscapedKeywordDoesNotHideAMember`, `TestAMemberBehindADecoyPrefixIsARefusal`)
- **Decision 6, cards (Task 2).** `Card.Check` also refuses long rates on a card without a threshold, and a five-minute cache write, base or long, that costs less than the input or the cache read. A lost call's worst case counts its request as five-minute cache writes, and a cheaper write would price that worst case below what the same tokens cost. (4f3de86; `TestCardsMustPrice`)
- **Decision 5, names the gateway forwards (Task 3).** The registry also checks rules that apply always against five fixed ids, each of one character class (digits, lowercase hex, uppercase hex, lowercase letters, uppercase letters): the 10,000 sampled ids can miss a run of one class, and `\d{8,}`, which matched about 1 in 40,000 ids in the vendor's form, loaded. Its hint says to anchor on `\b`, in single quotes, and to keep `_` out of the match. A pattern that holds a control character is refused: in a double-quoted YAML string, `\b` is a backspace, and such a rule loaded and matched nothing. The board tools' names had been copied into the registry rather than moved, so a test now holds the names the MCP gateway offers to `config.BoardTools`, the registry's list. (d3de503, 77ab3d9, d475382; `TestARuleThatAppliesAlwaysSparesToolNamesAndIDs`, `TestAPatternHoldsNoControlCharacter`, `TestEveryListHoldsTheBoardTools`)
- **Decisions 10 and 11, withheld answers (Task 4).** An answer whose record would hide what a rule matches behind an escape is withheld: the runtime gets a 502, the call is recorded as `error` with the tokens the vendor reported, and its thinking blocks are not remembered. So every answer returned is recorded in full, and no thinking block the gateway gives can hide such a match. `StatusError` now says that a withheld answer counts what the vendor reported, so that Plans 7 and 8 do not take every error for a call that cost nothing. (3ac854d, e5f89fd; `TestAnAnswerWhoseRecordWouldHideAMatchIsWithheld`, `TestAnAnswerWithOrdinaryEscapesIsReturnedAndRecorded`)
- **Decisions 3 and 5, tool call ids (Task 4).** The gateway remembers the ids of the tool calls it gave a session, in answers it recorded and returned, as it remembers the answers' thinking blocks: at most 1,024 a session, and only ids a request could hold. It forwards such an id as given, whatever a rule matches in it. The vendor wrote the id, so it is no channel for the agent's text; and a rule that applies always and mixes character classes, such as `eyJ[A-Za-z0-9_-]{10,}` for JSON Web Tokens, still matched 1 in about 20,000 to 60,000 ids in the vendor's form: once such an id was in a conversation, every later request in it was refused. Any other id that holds what a rule hides is still refused, and so is any such name. A restart forgets the ids, as it forgets thinking blocks. Decision 4's "what passes, redacted, passes again" no longer holds for a request that carries such an id: its record redacts the id. (9c500af, 3d8b772; `TestAnIdTheGatewayGaveIsForwardedWhateverARuleMatchesInIt`, `TestAToolCallsIdTheGatewayGaveComesBackWhateverARuleMatchesInIt`, `TestASessionKeepsOnlyTheLastToolCallIdsItWasGiven`, `TestAnIdNoRequestCouldSendBackIsNotRemembered`)
- **Decisions 8 and 9, the vendor (Task 5).** An attempt whose request could not be written is `ErrNotSent`, and is tried again; it had been read as a lost answer, which counts at its worst case for good and is never retried. The vendor's base URL must name a host, and may hold no user information, query, fragment or opaque part: a credential there would sit in configuration and show in errors (spec Section 9.3). Errors show the URL without them. (7e91891, d464e59; `TestAWriteThatFailsIsNotSent`, `TestTheVendorsAddressNamesAHostAndNothingElse`)

**Other changes.**

- **A test made vacuous by a new migration (Task 6).** Plan 6a's `TestChainingWhileWritersRunLeavesNoRowUnchained` applied every migration but the last by hand and ran the last under live writers, so once this plan added migration 0006, it no longer ran 0005 under them. It now applies the migrations before 0005 by name, and runs 0005 and every later one under the writers. (bda3c47)
- **The live check (Task 7), added at the author's request.** `make test-live` plays a turn of the platform's runtime through the endpoint, the gateway and the vendor adapter against the model vendor, and requires a turn with no refusal that echoes a thinking block. It needs `AGENTSWARM_LIVE_KEY`, the vendor's key, and costs a few cents a run; `AGENTSWARM_LIVE_MODEL` names a model other than the default. The model thinks only when a task needs it, so the prompt asks for reasoning before the first tool call: a first run passed every call through the gateway, but the model gave no thinking block in three turns. The check records the SDK version it passed with in `internal/llm/endpoint/testdata/live-check.json`, and a test in the default suite fails when go.mod's anthropic-sdk-go version differs, so an SDK upgrade needs the live check run again and its record committed. It passed on 2026-10-18 with v1.80.0 on claude-haiku-5-5, with two calls through the gateway and one thinking block echoed, and again after Task 7's fix round. That round kept the developer's `ANTHROPIC_` variables out of the endpoint's tests, made `make test-live` stop without the key rather than pass having checked nothing, and made `TestTheSDKDoesNotRepeatARefusal` use a 429, which the SDK would otherwise retry, so that it pins `X-Should-Retry`. (3781818, 3dea3f6, eef3819, 90ac9db; `TestLiveTurnThroughTheGateway`, `TestTheLiveCheckCoversTheRuntimesSDK`)

The spec's text follows these changes. Section 9.3 says that a name, or an id the gateway did not give the session, that holds what a rule hides is refused, as is a request in which an escape hides what a rule matches, and that an answer whose record would hide one is withheld. Section 15 counts the tool call ids a session keeps with its thinking blocks.

**Rulings that stand without a code change.**

- **A field the vendor adds to its answers' blocks (Task 1).** The concern that the runtime would echo such a field, which the gateway then refuses, is not real: the runtime echoes an answer with the SDK's `ToParam`, which copies only the fields the gateway hashes. An SDK upgrade could change that, and the live check must pass again for every upgrade (above).
- **Tool names as agents call them (Task 3).** The registry already refuses a granted tool whose name as agents call it, `server__tool`, is outside `^[A-Za-z0-9_-]{1,64}$` (`internal/registry/config/agent.go`), so it admits no tool name whose form the LLM gateway refuses.

**Constraints added for later plans.** They add to "Constraints carried to later plans" above.

- **Plan 7:**
  - Build the runtime's client with `option.WithoutEnvironmentDefaults()`, so that no `ANTHROPIC_` variable in its environment adds a key or headers to its requests, or changes its base URL.
  - Before agents switch to a new model, run `make test-live` with `AGENTSWARM_LIVE_MODEL` set to it: the test in the default suite watches only the SDK's version, and agents' models are set in the deployment's configuration, which it cannot see.
  - Give `endpoint.New` the same body cap as `Options.MaxBodyBytes`, from one setting: a request between the two caps is refused as invalid, not as too large, and recorded cut short.
  - Give `serve`'s `http.Server` a `ReadHeaderTimeout` and a `ReadTimeout`: the endpoint reads a request's whole body before the gateway checks its session or its round.
  - Issue sessions of the classes `primary` and `shadow` only: `model_calls` refuses any other, and the gateway would then answer 503, with nothing recorded.
  - Expect a restart to forget the tool call ids the gateway gave, as it forgets thinking blocks: a conversation carried across a restart that holds an id a rule matches is then refused.
- **Plan 10:** `End` inserts a call's outcome before its completion, so a large completion is written and hashed inside the tenant's chain lock. In the Task 6 review's probe, an 8 MiB completion held up a concurrent small write for 66 to 116 ms, against 3 to 6 ms when the completion went first. Include it in the load trial.

**The final review.** A fresh reviewer on Opus 5.5 read the whole branch, 18b6d85 to 55ebb2f, probed what it found, and triaged the 71 minor findings the tasks had deferred. Its verdict was "with fixes": no Critical issue, two Important ones and four Minor ones. Of the deferred minor findings, the triage chose three in the code, and the documentation's, to be fixed before the push; the others stay deferred, and those that name a later plan are among the constraints below. One fix wave on main took all of them, a commit each, test first: each code fix's test failed before the fix for the reason its finding names, and passed after it. The test of `isReplacement` pins behaviour that was already right, and was checked instead against the three mutants it is to catch; the change of wording has a test that fails on the old words. `make lint test-all` passed after the wave.

- **Important, fixed: JSON tool results (c766c72).** The LLM gateway checked a tool's result for escapes as text, a level above where the MCP gateway and the board check JSON. An escaped quote in a value then closed a key, so that `api_key` and the text after it read as a member that neither level holds, and a run of backslashes gained a level and nested too deep to check. The gateway refused 1,094 of 199,989 keyword-heavy JSON outputs that the MCP gateway showed, and any `read_board` snapshot holding an accepted post such as `grep shows password": "hunter2hunter2" in app.yaml`, which would have ended every agent's turn after `read_board` for the rest of the run. A tool's result that is exactly one JSON object or array is now redacted as the MCP gateway redacts JSON output: decoded, value by value, each string, key and number checked for escapes at its own level, and forwarded in the gateway's encoding, which is what the MCP gateway showed. Any other text is redacted as text, as before. The record of a forwarded request keeps such a result as it was forwarded (`Request.Record`): read again as text, the record would have held nothing of a request the gateway forwarded. After the fix, the reviewer's probe refuses 5 of the 199,989 outputs (below), and no snapshot of accepted posts. (`TestWhatTheMCPGatewayShowsOfJSONReachesTheModelAsShown`, `TestWhatTheMCPGatewayShowsOfJSONPassesTheLLMGateway`, `TestABoardSnapshotReachesTheModelUnchanged`, `TestASnapshotOfAcceptedPostsPassesTheLLMGateway`, `TestAJSONResultReachesTheModelDecodedAndRedacted`, `TestAJSONResultWhoseValueHidesAMatchIsRefused`, `TestTheRecordHoldsWhatIsForwardedRedacted`, `TestAJSONOutputIsForwardedAndRecordedAsShown`)
- **Important, partly fixed: client values in refusals (1472704).** A refusal quoted a short, plain value of the client's, and the gateway redacts the reason it stores as a whole sentence, in a record kept for a year. A rule anchored to a whole value, such as `^AKIA[A-Z0-9]{16}$`, matches the value alone but not inside the sentence, so a field name or a model that held such a key was stored in clear in the reason while the request's record hid it. A refusal now quotes a value only when the session's rules leave it as it is on its own (`Policy.Hides`, which `Check` asks for every value it quotes; the gateway asks the same for the method and the path), and otherwise gives its length. With no `Hides`, a refusal quotes nothing. (`TestARefusalQuotesNoValueARuleHides`, `TestARefusalQuotesNoValueTheSessionsRulesHide`, `TestQuoteWritesTextOnlyWhole`) The re-review of the fix wave found five places that quote a string composed from the client's value, so the session's rules check the composed string, not the value on its own: a model or a block's or tool's type that is not a string, written with fmt.Sprint; the anthropic-beta values joined with commas; and the path with its query. A rule anchored to a whole value still misses a secret written there. Plan 7 fixes them (below).
- **Minor, fixed: retries after the request ended (4be0bb5).** The wait before another attempt watched the round, the run and the call's timeout, but not the request. In the reviewer's probe the vendor answered 529, the client went during the wait, and the second attempt was made, answered, and recorded at 5,000 cost-weighted tokens, with the answer discarded. The wait now ends when the request does, and the call ends at its last failure, which cost nothing: Decision 8's reason not to send a request that ended holds for a retry. (`TestNoAttemptIsMadeOnceTheRequestEnded`)
- **Minor, fixed: admission after the wait for a slot (f5bed73).** A call was admitted before it waited for a slot at the vendor, and never again, so a comparison run's call admitted under 80% of the daily cap could start after the day had passed 80%, or 100%. It is now admitted again once it has its slot, which it frees if it is refused. (`TestACallIsAdmittedAgainOnceItHasItsSlot`)
- **Minor, fixed: an ambiguous record (8e9ff9d).** When the record of a forwarded call was committed though `Begin` failed, as when the database's answer is lost, the client was told that the call was not made, while its record counted its worst case for good. The gateway now tries once to record that the call was not made. Where no record was committed, the outcome's foreign key refuses it, which is harmless. It does not count against the session's round, and it is not tried again: a call never recorded would be tried for ten minutes. The memory store gained a hook for such a commit (`SetFailCommitted`). (`TestACallWhoseBeginFailedAfterItsCommitCountsNothing`)
- **Minor, fixed: a misleading reason (22f7db9).** A part whose escapes nest deeper than redaction decodes, which cannot be checked, was refused as one that "holds a match hidden by an escape". The refusal now says the part "holds escapes that may hide what a redaction rule matches". (`TestARefusalOfEscapesSaysTheyMayHideAMatch`)
- **From the triage, fixed:**
  - `isReplacement` must be an exact match: a mutant that took a value for a replacement when it began with one, ended with one or held one passed every test, and reopened 285 to 907 leaks per 40,000 fuzzed documents. A test now pins it, and fails under each of the three mutants. (d4d7ce1; `TestOnlyAValueThatIsExactlyAReplacementIsNotCheckedAsAMember`)
  - `redactContent` had no default case, so a block type added to the allowlist without a case there would have gone to the vendor unredacted. It now refuses a block of a kind it does not name, with thinking blocks named as deliberate exceptions, and a tool's result refuses a block other than text the same way. (bf46d4e; `TestRedactContentRefusesABlockOfAKindItDoesNotName`)
  - The vendor adapter printed an address that names no host in its error, as when a key is pasted where the base URL goes. It now prints nothing of such an address. (4452688; `TestTheVendorsAddressNamesAHostAndNothingElse`)
  - The documentation, in the commit that adds this section. ADR-007's Context defines the budget as spec Section 5.6 does, and its "Build, not extend" is marked decided, as is the spec's open question in Section 16. Spec Section 9.4 says that a model's own completion reaches the agent as the vendor sent it, and that a member whose value is already a rule's replacement is shown. The failure table's "hard token limit per run" now agrees with Section 5.6. Section 5.6 defines the reference token, and Section 15 gains a row for its price. Section 15's summary of retention names access and model-call records, and its row of thinking blocks, tool call ids and sessions kept gives each bound, 1,024, as fixed in code. Decision 11 says which 503s a client may repeat (below), and the wording of this section is corrected (above).

**Decisions that changed in the fix wave.** Besides those above:

- **Decision 11, which answers a client may repeat.** Decision 11 said that the 503s before anything was forwarded may be repeated. The 503 of a vendor that is failing carries `x-should-retry: false`, as the code has since Task 4: it counts against the session's round, as a refusal does. Decision 11 now says so: only the 503s of a store that could not be read or written, or of the gateway stopping, may be repeated.

**What remains of the JSON tool results.** The MCP gateway runs the patterns once more over the whole text it shows. Where that last pass breaks a JSON output's syntax, as the default rule's pattern for passwords can by taking a closing quote and a brace into its match, the shown text is no longer one JSON document: both gateways read it as text, and the LLM gateway can refuse it as before. Such text accounts for all 5 refusals of the reviewer's probe, and for all 21 in a wider draw of 199,870 outputs, of which 1,487 were left as text. An output of several text blocks, one of them JSON, is shown joined, and read as text too: 227 of 37,263 such outputs were refused. Each refusal ends the reader's turn and counts against its round; no secret reaches the vendor. Both are measured in Plan 7's smoke runs (below).

**Rulings of the fix wave.**

- **The gateway's encoding.** A JSON result is forwarded in the gateway's encoding of the redacted document, as the finding asked, not as the client sent it. Where the MCP gateway's last pass rewrote a key, the model reads the same document with its keys in order (16 of the 199,870), and where a replacement meets text that a pattern matches across both, it reads less than was shown (4), as Decision 5 allows.
- **No last pass over the whole encoding.** Running the patterns once more over the encoded result, as the MCP gateway's `Show` does, was tried and dropped: it matched across the JSON's punctuation, and changed 182 of 166,296 snapshots of accepted posts, leaving their JSON broken.
- **One document, or text.** A tool's result is read as JSON only when the whole text is one JSON object or array, as the MCP gateway reads output, and a text the MCP gateway's last pass left as text is read as text: the property tests leave such text out, and count it.

**What the final review set aside.** Each of these stands:

- The gateway's memory, 1,024 hashes and 1,024 ids for each of 1,024 sessions, is not freed when a run ends: within the plan's bounds.
- `Drain` lets a request in its hands start a vendor call: as "waits for those in its hands" says. Shutting down can take up to a call's timeout.
- The correction of Plan 6a's table of assumptions: question 13 is about message channels.

**Constraints the final review adds for later plans.** They add to those above.

- **Plan 7:**
  - Give the model each text block of an MCP gateway result as its own text block of the tool's result, the output apart from the gateway's note, and never join them: the LLM gateway reads a text that is one JSON document as JSON, and a joined text as text, where an escaped quote in a JSON output can be refused (c766c72).
  - Make the opening message and the system prompt that the coordinator composes pass the LLM gateway's checks, or every turn of the run is refused.
  - Measure, by reason, the refusals and the withheld answers of the smoke runs, among them the refusals of JSON outputs that the MCP gateway's last pass left as text, and of outputs of several blocks (above).
  - Check with a live call that the vendor accepts an echoed assistant text block that redaction changed, next to an unchanged thinking block.
  - Verify with a live call that the vendor accepts `inference_geo` before a deployment sets it.
- **Plan 8:** read back, in its tests, the columns of the model-call records it takes costs from: the Task 6 tests never read most of them back, so a swap of two token columns would pass.
- **Plan 10:**
  - Measure the sizes of model-call records, a whole conversation a call and completions up to 8 MiB, and the daily scan of `SpentSince`, in the load trial.
  - Close the redaction kernel's gaps of parity in the red team: it reads no member written as a quoted key followed by an unquoted value, so such a member reaches agents and the vendor unredacted; and `Value` applies member rules to a key and its value joined with an equals sign, while `Text` also reads members in the forms JSON, Python and Ruby write, so a member that a key and its value form only in one of those forms, such as a key `"password"` with the value `>"hunter2"`, is neither redacted nor flagged. Both were there before this plan.

**Residuals after the fix wave.** The scoped re-review of the fix wave found the rest addressed, and these left, each with the plan that takes it:

- **Plan 7:** quote a value that is not a string by its type only, and the path, the query and each anthropic-beta value on their own, so that every quoted client value is checked by the session's rules alone (the rest of the refusal-reason finding above).
- **Plan 7:** make `Request.Record` return nothing unless `Redact` passed on that request with the same redactor; today the gateway, its one caller, calls it only after, but it would record a JSON tool result unredacted if called before.
- **Plan 7:** a request can still be forwarded with an empty record, when the record's member check finds an escape that the part alone passed; record such a request, or refuse it, consistently.
- **Plan 10:** the admission made again once a call has its slot at the vendor reads the day's spend a second time, holding the slot; measure it with the daily scan in the load trial.
