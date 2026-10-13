# Runtime spike (ADR-006)

Evidence for ADR-006, kept so that the criteria can be rerun when a runtime changes. It is not platform code: nothing in the main module imports it.

| Path | What it is |
|---|---|
| `llmgw/`, `mcpgw/` | Stand-ins for the LLM gateway and the MCP gateway |
| `thinloop/`, `cmd/thinloop/` | Candidate D, the chosen runtime |
| `agentsdk/` | Candidate B, the Claude Agent SDK |
| `sandbox/` | Images, canaries and the scripted scenario |
| `criteria/` | One test per criterion, the canary control and the live test |
| `results/RESULTS.md` | What the spike measured |

Run from this directory:

- unit tests: `go test ./...`
- the sandbox suite, with Docker running: `go test -tags spike -count=1 -v ./criteria`
- live runs, with the author's key: `ANTHROPIC_API_KEY=... go test -tags 'spike live' -count=1 -v -run TestLiveDiagnosis ./criteria`
- lint: `golangci-lint run --build-tags 'spike live' ./...`

The suite runs three candidates:
- `thinloop`, D;
- `agentsdk`, B with full hardening;
- `agentsdk-min`, an ablation of B with only `setting_sources=[]` and `strict_mcp_config`. The `--hardening` flag of `agentsdk/run_round.py` chooses the level.

It is expected to fail three subtests, which are spike results: `TestCriterion5NoHostContent/agentsdk-min`, and `TestCriterion7ToolListsChangeOnlyBetweenRounds` for `agentsdk` and `agentsdk-min`.

The sandbox network needs a Docker Engine that supports `com.docker.network.bridge.gateway_mode_ipv4=isolated`, so that the network has no gateway. `TestTheSandboxCannotReachTheHost` checks that the host cannot be reached.

Rerun the sandbox suite whenever a candidate's version changes (ADR-006, "Revisit when").
