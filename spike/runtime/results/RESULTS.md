# Runtime Spike Results

| | |
|---|---|
| Date | 2026-10-13 |
| Versions | claude-agent-sdk 0.2.165 (Claude Code CLI 2.1.294); anthropic-sdk-go v1.80.0; MCP Go SDK v1.8.0 |
| Docker | 29.4.0 |
| Plan | `plan/2026-10-13-plan-2-runtime-spike.md` |

## Criteria (scripted model)

From Task 5's run of `go test -tags spike -count=1 -v ./criteria`.

| Test | thinloop (D) | agentsdk (B, hardened) |
|---|---|---|
| `TestOnlyTheGatewaysAreReachable` (criterion 4, sandbox) | PASS | PASS |
| `TestCriterion1EndTurnEndsTheTurn` | PASS | PASS |
| `TestCriterion1TheTimeLimitEndsTheTurn` | PASS | PASS |
| `TestCriteria2And3OnlyGatewayTools` | PASS | PASS |
| `TestCriterion5NoHostContent` | PASS | PASS, with a finding |
| `TestCriterion6NothingCarriesOverBetweenRounds` | PASS | PASS |
| `TestCriterion7ToolListsChangeOnlyBetweenRounds` | PASS | **FAIL** |
| `TestTheSandboxHoldsNoModelKey` | PASS | PASS |
| `TestCanariesCatchTheSDKDefaults` (control) | — | PASS |

## Findings

- **Criterion 7, B:** after a tool was added in the middle of round 1, B's round-1 requests 2 and 3 offered `mcp__gw__probe_extra`. B refreshes its tools on `tools/list_changed` within a turn.
- **Criterion 5, B:** hardened, B sent no content canary, but its requests 1 and 2 named the working directory, `/canary-workdir`. D sent neither.
- **The control, B with the SDK's defaults:**
  - leaked `CANARY-PROJECT-MEMORY` and `CANARY-USER-MEMORY` (both CLAUDE.md files) to the model;
  - started `project-mcp-started`, `settings-hook-ran` and `user-mcp-started`: the project's and the user's MCP servers, and the settings hook.
- **Stopping at a 5 s round limit:**
  - **D:** D's turn ended at the limit; a timestamped diagnostic build showed `Run` returning at 5.003 s. Its container exited 10.46 s after start, because MCP Go SDK v1.8.0's `ClientSession.Close` waits on the session teardown over the Docker network; outside Docker, the same binary exited at 5.49 s.
  - **B:** B's container exited 11.25 s after start, about 6 s after the limit. Its turn's end was not measured separately.
  - In both cases the runner should kill the sandbox at the limit rather than wait for the process to exit.
- **Verification note:** the plan's scratch copy had silently resolved the MCP Go SDK to v1.3.1, on which D exited 0.4 s after the limit. The figures above are from the pinned v1.8.0.

## Live runs

Not run. The author chose to skip the runs against the real model on 2026-10-13. The decision in ADR-006 is therefore provisional (decision rule, point 4), and these runs are confirmed at the start of Plan 3:

```bash
ANTHROPIC_API_KEY=... go test -tags 'spike live' -count=1 -v -run TestLiveDiagnosis ./criteria
```

## Code for one diagnosis flow

From `wc -l`:
- D: `thinloop/thinloop.go` 142 lines and `cmd/thinloop/main.go` 69 lines, 211 lines of Go in all.
- B: `agentsdk/run_round.py` 92 lines of Python, including its hardening.

## Release cadence

Releases in the 90 days to 2026-10-13, from PyPI and the Go module proxy (`tools/cadence.py`):

| Library | Releases |
|---|---|
| claude-agent-sdk (each bundles a new Claude Code CLI) | 46 |
| anthropic-sdk-go | 27 |

## Candidate E: HolmesGPT

HolmesGPT 0.43.0 (2026-10-07), checked against master at 83ad1ab (2026-10-12). Python, Apache-2.0. It runs as a CLI, as a Python library, or as an HTTP server.

| # | Criterion | Result | Evidence |
|---|---|---|---|
| 1 | Round-based turns | Not met on its own | An investigation ends when the model stops calling tools, at `max_steps`, or when the model calls a "pause" tool that an HTTP client declared. An `end_turn` tool from the gateway is executed and the loop goes on. It has no wall-clock deadline, only per-call timeouts. [tool_calling_llm.py](https://github.com/HolmesGPT/holmesgpt/blob/master/holmes/core/tool_calling_llm.py); [frontend tools](https://holmesgpt.dev/latest/reference/http-api/#frontend-tools) |
| 2 | Gateway-only MCP | Met through configuration | Remote servers go under `mcp_servers` with `mode: streamable-http`. Every built-in toolset must be disabled one by one; server mode enables six by default. [remote MCP servers](https://holmesgpt.dev/latest/data-sources/remote-mcp-servers/); [toolset_manager.py](https://github.com/HolmesGPT/holmesgpt/blob/master/holmes/core/toolset_manager.py) |
| 3 | No reach outside | Met through a deny-list | bash, internet, kubernetes, prometheus and grafana are each a toolset disabled by name. A built-in added in a later release is on by default in CLI mode. [toolsets](https://github.com/HolmesGPT/holmesgpt/blob/master/holmes/plugins/toolsets/__init__.py) |
| 4 | Egress denied | Met through configuration; one unknown | A custom model base URL works (`OPENAI_API_BASE`). LiteLLM fetches its cost map unless `LITELLM_LOCAL_MODEL_COST_MAP=true`. The interactive CLI checks for updates at api.robusta.dev. Unknown: whether token counting downloads tokenizer files. [environment variables](https://holmesgpt.dev/latest/reference/environment-variables/) |
| 5 | No host content | Met | It reads only its configured file and skills. Every prompt section can be switched off. [prompt.py](https://github.com/HolmesGPT/holmesgpt/blob/master/holmes/core/prompt.py) |
| 6 | Bounded context | Met | Each call starts from the messages passed in. [context management](https://holmesgpt.dev/latest/reference/context-management/) |
| 7 | Tool lists | Met: fixed per run | It lists tools once when the toolset loads and ignores `tools/list_changed`. [toolset_mcp.py](https://github.com/HolmesGPT/holmesgpt/blob/master/holmes/plugins/toolsets/mcp/toolset_mcp.py) |

As a runtime it is out: it has no turn contract and cannot be stopped by `end_turn`, and its safety rests on a deny-list. As an external comparator (spec Section 13.3) it needs care:
- It serializes every call to one MCP server and runs MCP `initialize` again for each call, so its latency is not comparable.
- At startup it calls an identity tool, such as `whoami`, if the gateway exposes one.
- Its documentation and code disagree in places.
- It releases every one to three weeks, so pin a version.
