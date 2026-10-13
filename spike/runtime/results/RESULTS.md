# Runtime Spike Results

| | |
|---|---|
| Date | 2026-10-13 |
| Versions | claude-agent-sdk 0.2.165 (Claude Code CLI 2.1.294); anthropic-sdk-go v1.80.0; MCP Go SDK v1.8.0 |
| Engine | OrbStack, Docker Engine 29.4.0 (linux/arm64) |
| Plan | `plan/2026-10-13-plan-2-runtime-spike.md`, with the changes recorded at its end |

## Criteria (scripted model)

From `go test -tags spike -count=1 -v ./criteria` after the final review's fixes. Candidates:
- `thinloop`: D.
- `agentsdk`: B with full hardening.
- `agentsdk-min`: an ablation of B with only `setting_sources=[]` and `strict_mcp_config` beyond the options every mode keeps.

| Test | thinloop | agentsdk | agentsdk-min |
|---|---|---|---|
| `TestOnlyTheGatewaysAreReachable` (criterion 4) | PASS | — | — |
| `TestTheSandboxCannotReachTheHost` (criterion 4) | PASS | — | — |
| `TestCriterion1EndTurnEndsTheTurn` | PASS | PASS | PASS |
| `TestCriterion1TheTimeLimitEndsTheTurn` | PASS | PASS | PASS |
| `TestCriteria2And3OnlyGatewayTools` | PASS | PASS | PASS |
| `TestCriterion5NoHostContent` | PASS (exact) | PASS, with findings | **FAIL** |
| `TestCriterion6NothingCarriesOverBetweenRounds` | PASS | PASS | PASS |
| `TestCriterion7ToolListsChangeOnlyBetweenRounds` | PASS | **FAIL** | **FAIL** |
| `TestTheSandboxHoldsNoModelKey` | PASS | PASS | PASS |
| `TestCanariesCatchTheUnhardenedSDK` (control) | — | PASS | — |
| `TestAFailedStartLeavesNothingBehind` (harness) | PASS | — | — |

A dash marks a test that does not run per candidate.

## Findings

**The sandbox.** An internal Docker network alone did not isolate the sandbox: containers reached the host through the network's gateway address. The first runs of this spike had that defect, for both candidates. The suite now creates sandbox networks with `com.docker.network.bridge.gateway_mode_ipv4=isolated`, which gives the network no gateway. `TestTheSandboxCannotReachTheHost` probes several targets: the network's and the default bridge's gateways, `host.docker.internal`, `gateway.docker.internal`, and Docker's host-gateway mapping. Each probe tries a listener in the test process.

**Criterion 7, B.** After a tool was added in the middle of round 1, B's next two requests in that round offered it (`mcp__gw__probe_extra`). B refreshes its tools on `tools/list_changed` within a turn.

**Criterion 5, B with full hardening.** No content canary reached the model. Its requests nevertheless carry content the platform did not provide:
- an identity line in `system`: "You are a Claude agent, built on Anthropic's Claude Agent SDK.";
- an environment block, which tells the model:
  - the working directory;
  - whether it is a git repository;
  - the platform and the shell;
  - the OS version, which names the host's kernel (`Linux 7.0.14-orbstack-…`);
- request settings the runner did not choose: `max_tokens` 128000, adaptive thinking, and `metadata.user_id` holding a device id and a session id.

D's requests hold only the skill, the prompt and the turn; the exact check passes.

**The ablation, agentsdk-min.** With only `setting_sources=[]` and `strict_mcp_config`, B sent its auto memory to the model: the `CANARY-AUTO-MEMORY` file, read from `~/.claude/projects/<project>/memory/`. It also added an attribution header (`x-anthropic-billing-header: …`) and a token-budget reminder. Keeping host content out therefore takes at least `tools=[]`, `setting_sources=[]`, `strict_mcp_config` and auto memory switched off. Full hardening switches auto memory off, together with the other switches.

**The control: B without hardening.** It keeps `tools=[]`, the gateway as the only MCP server, `dontAsk`, the `end_turn` hook and the agent's own system prompt.
- It leaked `CANARY-PROJECT-MEMORY`, `CANARY-USER-MEMORY` and `CANARY-AUTO-MEMORY`.
- It started the project's and the user's MCP servers, and ran the settings hook (`project-mcp-started`, `user-mcp-started`, `settings-hook-ran`).
- The settings value, environment value, file name and file content canaries have no known path into a prompt in this configuration. They are checked only negatively.

**The time limit.** The setup gave each read 2 s and a script that never runs out.
- **Measured:** both candidates made 3 model requests and 3 tool calls. The last started 4.0–4.1 s after the turn's first request, and no call started after the limit. Their processes exited 6.0–6.4 s after the container started.
- **B's first runner:** it cancelled through `asyncio.wait_for`, and kept calling 6.06 s and 8.07 s after the first request. The SDK documents that an asyncio cancellation skips its shielded shutdown. Cancelling through anyio cancel scopes plus `interrupt()` fixed it.
- **With a five-minute read still in flight:** both processes took about 5 s longer to exit. MCP Go SDK v1.8.0 bounds the `DELETE` sent by `ClientSession.Close` at 5 s (`closeDeleteTimeout`). claude-agent-sdk's `close()` waits 5 s before it sends SIGTERM.

**Verification note.** The plan's scratch copy had silently resolved the MCP Go SDK to v1.3.1; on it, D exited 0.4 s after the limit. All figures here come from the pinned v1.8.0.

## Live runs

Not run. The author chose to skip the runs against the real model on 2026-10-13. The decision in ADR-006 is therefore provisional (decision rule, point 4), and these runs are confirmed at the start of Plan 3:

```bash
ANTHROPIC_API_KEY=... go test -tags 'spike live' -count=1 -v -run TestLiveDiagnosis ./criteria
```

## Code for one diagnosis flow

From `wc -l`:
- D: `thinloop/thinloop.go` 142 lines and `cmd/thinloop/main.go` 69 lines, 211 lines of Go in all.
- B: `agentsdk/run_round.py` 111 lines of Python, including its hardening and its cancellation.

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

As a runtime it is out: it has no turn contract, `end_turn` cannot stop it, and its safety rests on a deny-list. As an external comparator (spec Section 13.3) it needs care:
- It serializes every call to one MCP server and runs MCP `initialize` again for each call, so its latency is not comparable.
- At startup it calls an identity tool, such as `whoami`, if the gateway exposes one.
- Its documentation and code disagree in places.
- It releases every one to three weeks, so pin a version.
