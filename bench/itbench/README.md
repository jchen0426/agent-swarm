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
- `serve --tls-ca FILE` serves HTTPS, with a certificate that a new CA signs for the host it listens on, and writes that CA's certificate to `FILE` for clients to trust.

## Under `agentswarm serve`

`agentswarm check` runs the agents in its own process. To run them on the platform, through `agentswarm serve`, its gateways and its runner, open a scenario as a work item. On 2026-10-21 a run of S16 this way converged on the scenario's key in two rounds and 18 seconds, for 169,150 cost-weighted tokens: about $0.02 on Claude Haiku 5.5.

The steps run from this directory, with `agentswarm` built at the repository's root (`make build`) and on the `PATH`, `jq` installed, and the run's files under `/tmp/smoke`.

1. Serve the datasets over HTTPS, since a server registration takes an https endpoint only:

   ```bash
   mkdir -p /tmp/smoke
   ./itbench serve --datasets ~/itbench/datasets --addr 127.0.0.1:18443 --tls-ca /tmp/smoke/ca.pem
   ```

2. Pin the scenario's tools, as a reviewer would after reading their definitions:

   ```bash
   agentswarm registry hash-tools --endpoint https://127.0.0.1:18443/S16/mcp --ca /tmp/smoke/ca.pem > /tmp/smoke/hashes.json
   ```

3. Write the configuration repository, a Git repository at `/tmp/smoke/config` with the branch `main`. `examples/config/`, at the repository's root, shows each kind of file. For each investigator of `agents/agents.json`:
   - `agents/<name>.yaml`:

     ```yaml
     kind: Agent
     metadata: {name: changes, owner: you@example.com}
     spec:
       runtime: thinloop
       model: claude-haiku-5-5
       skills: [changes-skill@1.0.0, itbench-team@1.0.0]
       tools: ["itbench.*"]
       subscribe: {workItemTypes: [incident]}
     ```

   - `skills/<name>-skill/skill.yaml`, and its skill, `agents/<name>.md`, as `skills/<name>-skill/1.0.0/SKILL.md`.

   And once:
   - `skills/itbench-team/`, a skill every investigator has: the failure modes of `agents/failure-modes.yaml` with their meanings, which `check` gives in its prompt. Under serve the alert reaches the agents as the board's first evidence.
   - `servers/itbench.yaml`, with each tool of `hashes.json` and its hash:

     ```yaml
     kind: McpServer
     metadata: {name: itbench, owner: your-team}
     spec:
       endpoint: https://127.0.0.1:18443/S16/mcp
       image: "sha256:<64 hexadecimal digits>"
       credentialRef: secret://mcp/itbench
       tools:
         "get_changes": {schemaHash: "sha256:<from hashes.json>", effect: read}
     ```

   - `work-item-types/incident.yaml`, whose key takes the services that the set `checkset` wrote lists for the case (`jq -r '.cases[] | select(.id == "S16") | .task.services | join(", ")' set.json`) and the failure modes:

     ```yaml
     kind: WorkItemType
     metadata: {name: incident, owner: your-team}
     spec:
       hypothesisKey:
         - {name: service, values: [accounting, ad, cart, checkout]}
         - {name: failureMode, values: [bad-release, flag-change, config-change]}
       budget: {wallClock: 20m, tokens: 1000000, rounds: 6}
       round: {timeLimit: 3m}
       measurement: {invisibleShare: 0}
     ```

   - `redaction-rules/default-secrets.yaml`, a rule with `always: true` and a pattern, as in `examples/config/`: the skills' scan needs one.

   Commit the files, then check them: `agentswarm registry validate --repo /tmp/smoke/config`.

4. Start Postgres 17 or later. Write the secrets directory, `/tmp/smoke/secrets`, each secret a file of mode 0600: `database/url`, `admin/token`, `vendor/key` (the model vendor's key) and `mcp/itbench` (any value: `itbench serve` asks for no credential). Then migrate:

   ```bash
   agentswarm migrate --secrets /tmp/smoke/secrets --database secret://database/url
   ```

5. Copy `examples/deployment.yaml`, at the repository's root, to `/tmp/smoke/deployment.yaml`. Point its paths at `/tmp/smoke`: the secrets, the configuration repository, the addresses file, the turns' output directory, and `mcp.caBundle: /tmp/smoke/ca.pem`. Its card prices Claude Haiku 5.5.

6. Run serve, and open S16 by the admin API, with the scenario's alert as the item's title:

   ```bash
   agentswarm serve --deployment /tmp/smoke/deployment.yaml 2> /tmp/smoke/serve.log &
   admin=$(jq -r .admin /tmp/smoke/addresses.json)
   token=$(cat /tmp/smoke/secrets/admin/token)
   alert=$(jq -r '.cases[] | select(.id == "S16") | .task.alert' set.json)
   curl -sS -X POST "http://$admin/v1/items" -H "Authorization: Bearer $token" -H "Idempotency-Key: S16-1" \
     --data "$(jq -n --arg title "$alert" '{type: "incident", title: $title}')"
   ```

7. Follow the item with `curl -sS -H "Authorization: Bearer $token" "http://$admin/v1/items/<id>"` until its `stop` is set: each round shows its evidence and the board's report. `agentswarm inspect --deployment /tmp/smoke/deployment.yaml calls <run>` lists the run's model and tool calls.

8. Stop serve with SIGTERM: it drains under the lease, releases it and exits 0. Then delete `/tmp/smoke/secrets/vendor/key`.

## Tests

- Unit tests: `go test ./...`.
- On the real data, about a minute: `ITBENCH_RAW=~/itbench/raw go test -count=1 ./checks`.
- Lint: `golangci-lint run ./...` with the repository's `.golangci.yml`, two directories up.

## Data

ITBench-Lite, IBM Research, Apache-2.0: <https://huggingface.co/datasets/ibm-research/ITBench-Lite>. The prepared datasets are derived from it. `prepare` renames the benchmark's own image, node and command names (`prepare/sanitize.go`) and removes its objects and writers' names.
