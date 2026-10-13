# ITBench-Lite for the mechanism check

The benchmark side of the mechanism check (spec Section 14.1): public fault scenarios, prepared so that agents can read them through read-only MCP tools that cannot see how the faults were injected, with frozen answers, a deterministic baseline and a scorer. It is not platform code: nothing in the main module imports it.

| Path | What it is |
|---|---|
| `scenarios.yaml` | The 13 test scenarios, the pinned commit, and each scenario's accepted keys, fixed before any run |
| `dev.yaml` | The 20 development scenarios, where the agents are piloted and their skills revised, in the same form |
| `raw/` | Readers for the benchmark's tables: tab-separated values with CSV quoting, and Python literals |
| `fetch/` | Downloads the scenarios at the pinned commit, and checks every file's hash |
| `prepare/` | Turns one scenario into a dataset: the half hour before the alert, without the benchmark's traces |
| `dataset/` | The prepared form, one JSON Lines file per stream |
| `server/` | The read-only MCP tools over one dataset |
| `score/`, `baseline/` | The scenario set, scoring by key, and the "most recent change" baseline |
| `checkset/` | Writes a set as the input of the platform's `agentswarm check` command |
| `agents/` | The stand-in team's content: three investigators' skills and the failure modes' meanings |
| `cmd/itbench/` | The `itbench` command |
| `checks/` | Tests on the real downloaded data |
| `RESULTS.md` | What the pipeline found in the data |

## Use

Keep the data outside the repository; it is about 11 GB downloaded and 300 MB prepared. If it must live here, put it in `data/`, which git ignores. From this directory:

```bash
go build -o itbench ./cmd/itbench
./itbench fetch --dest ~/itbench/raw
./itbench prepare --raw ~/itbench/raw --out ~/itbench/datasets
./itbench serve --dataset ~/itbench/datasets/S16 --addr 127.0.0.1:8080
./itbench baseline --datasets ~/itbench/datasets > baseline.json
./itbench score --answers answers.json --baseline baseline.json
```

To run the agents with the platform's check command, serve every dataset at once and write the set it reads. Each scenario is served at `/<id>/mcp`:

```bash
./itbench serve --datasets ~/itbench/datasets --addr 127.0.0.1:8080
./itbench checkset --datasets ~/itbench/datasets > set.json
agentswarm check --set set.json --agents agents/agents.json --out ~/itbench/check
./itbench score --answers ~/itbench/check/answers-t1.json --baseline baseline.json
```

The development set works the same way with `--set dev.yaml` and its own datasets directory.

- `fetch` lists each scenario at the commit in `scenarios.yaml` and skips files already present with the right hash, so rerunning it verifies a download.
- `prepare` refuses to write a dataset that holds a forbidden string, such as the benchmark's name or a chaos resource.
- `serve` reads only the dataset it is given. It never reads `scenarios.yaml` or the raw download.
- `baseline --scope service` limits the baseline to the services that the trigger's alerts name.
- `checkset` gives each case the trigger's alerts as text, the services the data names, the scenario's keys as labels, and the namespace-wide baseline's answer. It refuses a key whose service the data does not name.
- Answers are a JSON object of scenario ids to keys, such as `{"S16": {"service": "shipping", "failureMode": "config-change"}}`.
- `score` marks the answers that spec Section 13.4 would leave to raters. The check has no raters, so they count as misses.

## Tests

- Unit tests: `go test ./...`.
- On the real data, about a minute: `ITBENCH_RAW=~/itbench/raw go test -count=1 ./checks`.
- Lint: `golangci-lint run ./...` with the repository's `.golangci.yml`, two directories up.

## Data

ITBench-Lite, IBM Research, Apache-2.0: <https://huggingface.co/datasets/ibm-research/ITBench-Lite>. The prepared datasets are derived from it. `prepare` renames the benchmark's own image, node and command names (`prepare/sanitize.go`) and removes its objects and writers' names.
