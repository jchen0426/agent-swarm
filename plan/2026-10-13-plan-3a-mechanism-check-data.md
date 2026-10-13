# Plan 3a: Mechanism Check Data and Scoring — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Executed on 2026-10-13, with the changes recorded at the end |
| Date | 2026-10-13 |
| Roadmap | `2026-10-12-implementation-roadmap.md`, Plan 3, the part that needs no model |
| Decided by the author | On 2026-10-13: build only what needs no model first; agent runs and Gate A wait for model access |

**Goal:** Build everything the mechanism check needs before any agent runs: the 13 ITBench-Lite scenarios downloaded at a pinned commit and checked; datasets that end at the alert and hide how the faults were injected; read-only MCP tools over them; the frozen answers; the "most recent change" baseline; a scorer. Fix the pass criteria before any run.

**Architecture:** A separate Go module in `bench/itbench/`, like `spike/runtime/`: the main module never imports it. `raw` reads the benchmark's tables. `fetch` downloads and checks them. `prepare` turns one scenario into a `dataset`: alerts, changes, objects, events, logs, error spans and metrics, cut at the alert. `server` serves a dataset through ten read-only MCP tools. `score` holds the frozen scenario set and scores answers by key, and `baseline` computes the most recent change. The `itbench` command ties them together, and `checks` runs everything on the real data.

**Tech Stack:** Go 1.26; the MCP Go SDK v1.8.0; go.yaml.in/yaml/v3 v3.0.5; cobra v1.10.2; testify v1.12.0. Data: ITBench-Lite SRE v0.2 (IBM Research, Apache-2.0) at commit `d0916b08ba421ce5e672e9ad68aa947d938dfef0`.

**Spec:** `plan/2026-10-11-agent-swarm-platform-spec.md`, Sections 1.4 (a public benchmark only for the mechanism check), 4.3 (keys), 13.2–13.4 (the hit, the baseline, matching), 14.1 (the mechanism check) and 14.4 (the retrospective check command). The example deployment profile, Section 4.3 (the key) and the comparators (the baseline).

**Verified:** Every code block was compiled, tested and linted in a scratch copy, then replayed task by task in an empty tree: each red step fails for the stated reason, each green step passes, and `golangci-lint run ./...` reports 0 issues after every task. Task 13 ran on the real data: the download check, the real-data tests and the commands' output below are from that replay.

## What verification found in the data

The full list is in Task 13's `RESULTS.md`. What shaped this plan:

- **The trigger.** Each scenario holds exactly one `alerts/alerts_in_alerting_state_*.json`, the alerts in alerting state when ITBench started its own agent. This plan uses it as the alert, so the alert time is the benchmark's, not ours.
- **Noisy alerts.** `RequestErrorRate` fires above an error rate of 0, so it also fires before the faults. Standing alerts fire throughout.
- **Every fault that is a change is visible before the alert.** Each has an object snapshot after it and at or before the alert, S33 by less than a second. So every stream is cut at the alert, with no look-ahead.
- **The changes in the window are exactly the injections.** S33's injection changed two Deployments, ad and cart, 8 s apart, though its ground truth names only ad.
- **Traces end before the fault in 9 of the 13 scenarios.** The benchmark's trace tables stop early, so each dataset records where its traces end.
- **S16's evidence is in INFO logs and the proxy's access logs.** It has no WARN or ERROR record, so logs keep every severity.
- **The injections named themselves:** `hello-bench-invalid`, `invalid-node`, `invalid-command`. The dataset renames them to plausible names that fail the same way.
- **The baseline.** The latest change in the namespace hits 8 of 13 scenarios, 8 of the 9 changes. Limited to the services that the trigger names, as the example profile defines it, it hits 1.
- **One bug only the real data showed.** An MCP tool whose output held raw JSON bytes failed every call: the SDK derived an array schema from the type. `TestEveryToolAnswersOverMCP` now calls every tool over MCP.

## Pass criteria of the mechanism check

Fixed here, before any run (spec Section 14.1). Plan 3b applies them; changing them after a run voids the check. **The author reviews them with this plan.**

- **Trials.** The planned agents run on each of the 13 scenarios three times. A trial gives each scenario one run per agent, without the coordinator. Its conclusion is the agents' hypotheses aggregated by key, as in the independent run (spec Section 13.3; Plan 1's assessment).
- **C1, mechanics, in every run.** The run ends with a conclusion or with none, within its budget. Every evidence quote matches the output of the read it cites (spec Section 4.2). Every call goes to the read-only tools. One violation fails the check.
- **C2, ground truth and the baseline.** In at least two of the three trials, the conclusions hit at least as many of the 9 change scenarios as the namespace-wide baseline does: 8.
- **Reported, not gated:** hits on all 13 scenarios and on the 4 without a change; the service-scoped baseline; false convergence; tokens per run, which set the first token budget default.

A hit is the conclusion's key equal to one of the scenario's keys in `scenarios.yaml`. A key containing `other` is never a hit, because the spec leaves such keys to raters (Section 13.4).

**Clarified on 2026-10-13, after the final review and before any run.** The check has no raters. Spec Section 13.4 also leaves to raters a miss that involves `dependency-failure` or another service. Here such a conclusion counts as a miss, as before. `itbench score` marks it, so the author can see how close a run came.

## Decisions this plan makes where the spec is silent

Review these first. Each is pinned by the named test.

| Decision | Choice | Why | Pinned by |
|---|---|---|---|
| Alert time | The trigger file's timestamp, or the latest `activeAt` among its alerts if later | The benchmark's own start; nothing in the trigger postdates it (S4's alert does) | `TestTheTriggerTimeIsTheLaterOfTheFileTimeAndItsAlerts` |
| Window | The 30 minutes before the alert, every stream cut at the alert | Leftovers of earlier runs fall outside; the faults are visible inside | `TestBuildPreparesEveryStream`; `checks` |
| What agents read | Only the `otel-demo` namespace, plus alerts with no namespace | The other namespaces are the benchmark's harness and the cluster's own | `TestReadSnapshotsKeepsTheNamespaceUpToTheCut`, `TestAlertPollsKeepTheWindowAndDropOtherNamespaces` |
| What is removed | Objects labeled `it-bench` and chaos resources; every object's writers (`managedFields`); `it-bench` labels; four renames | They tell how a fault was injected, not what the system did | `TestObjectsAreTheLastSnapshotWithoutWritersOrHarnessLabels`; `Leaks` |
| What is kept | Flag names and descriptions; the restart that follows each flag change; the pod deletion in S102 | They are what the system did, and any operator would see them | `RESULTS.md`; reported per scenario |
| Changes | Differences in desired state between snapshots, timed by the latest writer other than a controller; creations at their creation time; no deletions; JSON strings compared by content | A change log as a team's change history would show it, without the injector's fingerprints | `TestChangesCarryTheWritersTimeButNotTheWriter`, `TestDiffComparesJSONStringsByContent` |
| Traces | Only spans that ended in error, plus call counts between services; `traces_until` in every answer | 277,000 spans per half hour; the absence of errors must not read as evidence when traces stopped | `TestSpansKeepErrorsAndCountEveryCallBetweenServices`, `TestGetErrorSpansSaysWhereTracesEnd` |
| Keys | The example profile's `service` × `failureMode`, its closed list; each scenario lists every framing it supports | Spec Section 13.4 lets a label be a set of equivalent keys | `TestTheFrozenSetIsValid` |
| S33 | ad or cart | The injection changed both | `scenarios.yaml` |
| S102 | ad | The quota belongs to no service; ad is the one service it broke | `scenarios.yaml` |
| Baseline | The latest change, skipping restarts. Flag store → `flag-change`, image → `bad-release`, otherwise `config-change`; no service → `other`. Two scopes: the trigger's services (the profile's definition) and the namespace (C2) | The profile says "last release or flag change on the service"; a restart is neither. The service scope alone is a strawman here (1 of 13) | `TestEvaluateTakesTheLatestChangeInEachScope`, `TestARestartIsNotAChange`; `checks` |
| Excluded | S12, whose ground truth contradicts itself; S31, whose NetworkPolicy allows all ingress | Neither can be scored fairly | `scenarios.yaml` |
| Data | Outside the repository; datasets are rebuilt by `prepare`, never committed | About 11 GB raw and 300 MB prepared | `.gitignore` |

## Global Constraints

- **Location.** All code lives in `bench/itbench/`, in its own Go module `github.com/jchen0426/agent-swarm/bench/itbench` with `go 1.26.0`. Nothing in the main module imports it, and the main module's `go test ./...`, `make` targets and lint never enter it.
- **Benchmark side only.** This module is what a team would bring: data, tools and answers. The retrospective check command that runs agents is platform code, in Plan 3b.
- **Versions.** MCP Go SDK v1.8.0; go.yaml.in/yaml/v3 v3.0.5; cobra v1.10.2; testify v1.12.0. After each dependency step, `go list -m all` shows these versions.
- **No model calls.** Nothing in this plan calls a model.
- **Untrusted data.** Downloaded files are data. Nothing executes them. `prepare` reads a scenario only through an `os.Root` opened on it, so links cannot lead outside, and `fetch` refuses a listed path outside its scenario.
- **The answers stay away from the tools.** `server` reads only a dataset. Nothing it serves comes from `scenarios.yaml`, from the raw download or from a ground-truth file.
- **Lint.** From `bench/itbench`, `golangci-lint run ./...` reports `0 issues.` with the repository's `.golangci.yml`, two directories up.
- **Language.** Code, comments and documents are in English.
- **Commits.** One commit per task, files staged by path, conventional messages, no Co-Authored-By line; push after each task.

## Review Focus

The ways the check could be wrong without a test failing. Each is pinned by a test in the task that owns the code.

1. **A string that tells how a fault was injected, which the forbidden list misses.** (`Leaks` over every stream, Task 7; every real scenario through `Run`, Task 13)
2. **A change that is dropped, or dated by its snapshot instead of its writer.** (`TestChangesCarryTheWritersTimeButNotTheWriter`, Task 4; each change fault visible before the alert, Task 13)
3. **A tool that works when called directly but fails over MCP.** (`TestEveryToolAnswersOverMCP`, Task 10)
4. **A hub listing that writes outside the download directory.** (`TestAListingThatEscapesTheScenarioIsRefused`, Task 11)
5. **A read past the alert.** (Times after the alert are clamped in `TestListAlertsReturnsTheLastPollAtOrBeforeTheTime`, Task 10; rows after the cut are dropped in `TestReadSnapshotsKeepsTheNamespaceUpToTheCut` and `TestLogsKeepTheNamespaceInTheWindow`, Tasks 4 and 5)

## Not in this plan

Plan 3b, once model access is settled:
- **Plan 2's live runs**, which ADR-006 left provisional.
- **The planned agents and their skills** for this benchmark.
- **The retrospective check command**, in the platform: runs the agents without the coordinator, aggregates their hypotheses by key with Plan 1's assessment, and scores them with this plan's set.
- **The runs, the criteria's verdict and the first token budget default.**

## File Structure

| Path | Responsibility |
|---|---|
| `bench/itbench/go.mod` | The module; the main module never imports it |
| `bench/itbench/raw/` | Readers for the benchmark's tables and Python literals |
| `bench/itbench/dataset/` | The prepared form of a scenario, one JSON Lines file per stream |
| `bench/itbench/prepare/` | One scenario into a dataset: the trigger, alerts, changes and objects, events, logs, spans, metrics, renames and the leak check |
| `bench/itbench/scenarios.yaml` | The frozen set: the commit, the closed list, each scenario's keys and why |
| `bench/itbench/score/` | Loads the set, scores answers by key next to the baseline |
| `bench/itbench/baseline/` | The most recent change, in two scopes |
| `bench/itbench/server/` | The ten read-only MCP tools |
| `bench/itbench/fetch/` | Downloads at the pinned commit, with hash checks |
| `bench/itbench/cmd/itbench/` | The `itbench` command: `fetch`, `prepare`, `serve`, `baseline`, `score` |
| `bench/itbench/checks/` | Tests on the real downloaded data |
| `bench/itbench/README.md`, `RESULTS.md` | How to use it; what it found in the data |

## Tasks

| Task | Delivers |
|---|---|
| 1 | The module and the raw readers |
| 2 | The dataset |
| 3 | The trigger and the alert polls |
| 4 | Changes and objects |
| 5 | Events, logs and spans |
| 6 | Metrics |
| 7 | The pipeline and the leak check |
| 8 | The scenario set and scoring |
| 9 | The baseline |
| 10 | The read-only MCP tools |
| 11 | Downloads |
| 12 | The `itbench` command |
| 13 | The real data, the results and the roadmap |

---

### Task 1: The module and the raw readers

**Files:**
- Create: `bench/itbench/go.mod`, `bench/itbench/go.sum`, `bench/itbench/.gitignore`
- Create: `bench/itbench/raw/pyrepr.go`, `bench/itbench/raw/tsv.go`
- Test: `bench/itbench/raw/pyrepr_test.go`, `bench/itbench/raw/tsv_test.go`, `bench/itbench/raw/realdata_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces, in package `raw`:
  - `func ParsePython(s string) (any, error)`: dicts as `map[string]any`, lists and tuples as `[]any`, `int64`, `float64`, `bool`, `nil`, and a call such as `np.datetime64('…')` as its argument;
  - `type Row`, with `(Row).Get(name string) string`;
  - `func ReadTSV(r io.Reader, fn func(Row) error) error`;
  - `func ParseTime(s string) (time.Time, error)`.

- [ ] **Step 1: Create the module**

From the repository root:

```bash
mkdir -p bench/itbench && cd bench/itbench
go mod init github.com/jchen0426/agent-swarm/bench/itbench
go mod edit -go=1.26.0
go get github.com/stretchr/testify/assert@v1.12.0 github.com/stretchr/testify/require@v1.12.0
```

Expected: `go.mod` requires testify v1.12.0. Getting the `assert` and `require` packages, not just the module, records the go.sum entries of their dependencies, so that the red step fails on the missing code, not on go.sum. Run every later command in `bench/itbench` unless a step says otherwise.

Create `.gitignore`, so that the built command and data kept here by mistake never reach a commit. It must not name `raw/`, which is a package:

```text
# The built command, and data kept here by mistake: keep downloads outside the repository.
/itbench
/data/
```

- [ ] **Step 2: Write the failing tests**

The tables are ClickHouse tables that pandas exported: tab separators, CSV quoting with embedded newlines, and map columns as Python dict literals, some holding `np.datetime64(...)` calls. Create `raw/pyrepr_test.go`:

```go
package raw_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

func TestParsePythonDict(t *testing.T) {
	got, err := raw.ParsePython(`{'k8s.pod.name': 'cart-1', 'count': 3, 'ratio': 0.5, 'ok': True, 'none': None, ` +
		`'list': [1, 'two'], 'nested': {'a': "b"}, 'neg': -2, 'exp': 1.5e+00}`)

	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"k8s.pod.name": "cart-1", "count": int64(3), "ratio": 0.5, "ok": true, "none": nil,
		"list": []any{int64(1), "two"}, "nested": map[string]any{"a": "b"}, "neg": int64(-2), "exp": 1.5,
	}, got)
}

func TestParsePythonStringEscapes(t *testing.T) {
	got, err := raw.ParsePython(`{'q': 'it\'s', 'dq': "say \"hi\"", 'nl': 'a\nb', 'tab': 'a\tb', 'bs': 'a\\b', 'hex': '\x41', 'uni': 'é'}`)

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"q": "it's", "dq": `say "hi"`, "nl": "a\nb", "tab": "a\tb", "bs": `a\b`, "hex": "A", "uni": "é"}, got)
}

func TestParsePythonEmptyAndTuple(t *testing.T) {
	got, err := raw.ParsePython(`{'empty': {}, 'list': [], 'tuple': (1, 2), 'one': (3,)}`)

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"empty": map[string]any{}, "list": []any{}, "tuple": []any{int64(1), int64(2)}, "one": []any{int64(3)}}, got)
}

func TestParsePythonCallsBecomeTheirArgumentText(t *testing.T) {
	got, err := raw.ParsePython(`[np.datetime64('2025-12-15T17:18:15.800000000'), 1]`)

	require.NoError(t, err)
	assert.Equal(t, []any{"2025-12-15T17:18:15.800000000", int64(1)}, got)
}

func TestParsePythonRejectsGarbage(t *testing.T) {
	for _, in := range []string{`{'a': }`, `{'a' 1}`, `'unterminated`, `{'a': 1} trailing`, ``} {
		_, err := raw.ParsePython(in)
		assert.Error(t, err, in)
	}
}
```

Create `raw/tsv_test.go`:

```go
package raw_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

func TestReadTSVHandlesQuotingAndEmbeddedNewlines(t *testing.T) {
	in := "Timestamp\tBody\tServiceName\n" +
		"2025-12-15 17:24:14.846190080\t\"{\"\"a\"\": 1}\"\tcart\n" +
		"2025-12-15 17:24:15\t\"line one\n\tline two\"\tad\n"

	var got [][]string
	err := raw.ReadTSV(strings.NewReader(in), func(r raw.Row) error {
		got = append(got, []string{r.Get("Timestamp"), r.Get("Body"), r.Get("ServiceName"), r.Get("NoSuchColumn")})
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, [][]string{
		{"2025-12-15 17:24:14.846190080", `{"a": 1}`, "cart", ""},
		{"2025-12-15 17:24:15", "line one\n\tline two", "ad", ""},
	}, got)
}

func TestReadTSVReportsAShortRow(t *testing.T) {
	err := raw.ReadTSV(strings.NewReader("a\tb\n1\t2\n3\n"), func(raw.Row) error { return nil })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 3")
}

func TestReadTSVStopsOnTheCallbacksError(t *testing.T) {
	stop := errors.New("stop")
	rows := 0

	err := raw.ReadTSV(strings.NewReader("a\n1\n2\n"), func(raw.Row) error {
		rows++
		return stop
	})

	require.ErrorIs(t, err, stop)
	assert.Equal(t, 1, rows)
}

func TestParseTimeReadsTheExportFormats(t *testing.T) {
	want := time.Date(2025, 12, 15, 17, 24, 14, 846190080, time.UTC)
	for _, s := range []string{"2025-12-15 17:24:14.846190080", "2025-12-15T17:24:14.846190080Z"} {
		got, err := raw.ParseTime(s)
		require.NoError(t, err, s)
		assert.True(t, want.Equal(got), "%s: got %v", s, got)
	}
	_, err := raw.ParseTime("yesterday")
	assert.Error(t, err)
}
```

`raw/realdata_test.go` parses every table of one downloaded scenario, and skips without one:

```go
package raw_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

// pythonColumns are the columns that hold Python literals in the raw tables.
var pythonColumns = []string{
	"ResourceAttributes", "ScopeAttributes", "LogAttributes", "SpanAttributes",
	"Events.Timestamp", "Events.Name", "Events.Attributes",
	"Links.TraceId", "Links.SpanId", "Links.TraceState", "Links.Attributes", "tags",
}

// TestEveryRawTableParses reads every table of one downloaded scenario, set by
// ITBENCH_RAW_SCENARIO, and parses every Python-literal column.
func TestEveryRawTableParses(t *testing.T) {
	dir := os.Getenv("ITBENCH_RAW_SCENARIO")
	if dir == "" {
		t.Skip("ITBENCH_RAW_SCENARIO is not set")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.tsv"))
	require.NoError(t, err)
	more, err := filepath.Glob(filepath.Join(dir, "metrics", "*.tsv"))
	require.NoError(t, err)
	files = append(files, more...)
	require.NotEmpty(t, files)

	for _, path := range files {
		f, err := os.Open(path)
		require.NoError(t, err)
		rows := 0
		err = raw.ReadTSV(f, func(r raw.Row) error {
			rows++
			for _, col := range pythonColumns {
				if v := r.Get(col); v != "" {
					if _, err := raw.ParsePython(v); err != nil {
						t.Errorf("%s row %d column %s: %v: %.120s", filepath.Base(path), rows, col, err, v)
					}
				}
			}
			for _, col := range []string{"Timestamp", "timestamp"} {
				if v := r.Get(col); v != "" {
					if _, err := raw.ParseTime(v); err != nil {
						t.Errorf("%s row %d: %v", filepath.Base(path), rows, err)
					}
				}
			}
			return nil
		})
		assert.NoError(t, f.Close())
		require.NoError(t, err, path)
		assert.Positive(t, rows, path)
		t.Logf("%s: %d rows", strings.TrimPrefix(path, dir), rows)
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./raw`
Expected: FAIL with `no non-test Go files in .../bench/itbench/raw`.

- [ ] **Step 4: Write the readers**

Create `raw/pyrepr.go`:

```go
// Package raw reads ITBench-Lite's raw snapshot files: ClickHouse tables that
// pandas exported as tab-separated values, whose map columns hold Python
// literals.
package raw

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ParsePython parses one Python literal as pandas writes it: dicts, lists,
// tuples, strings, numbers, True, False and None. A call such as
// np.datetime64('…') becomes its single argument.
func ParsePython(s string) (any, error) {
	p := &pyParser{s: s}
	p.space()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.i != len(p.s) {
		return nil, p.errorf("unexpected %q", p.s[p.i:min(p.i+20, len(p.s))])
	}
	return v, nil
}

type pyParser struct {
	s string
	i int
}

func (p *pyParser) errorf(format string, args ...any) error {
	return fmt.Errorf("python literal at %d: %s", p.i, fmt.Sprintf(format, args...))
}

func (p *pyParser) space() {
	for p.i < len(p.s) && strings.ContainsRune(" \t\r\n", rune(p.s[p.i])) {
		p.i++
	}
}

func (p *pyParser) value() (any, error) {
	if p.i >= len(p.s) {
		return nil, p.errorf("unexpected end")
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.dict()
	case c == '[':
		return p.seq(']')
	case c == '(':
		return p.seq(')')
	case c == '\'' || c == '"':
		return p.str()
	case c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9'):
		return p.number()
	case c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z'):
		return p.ident()
	default:
		return nil, p.errorf("unexpected %q", string(c))
	}
}

// expect consumes c after optional space.
func (p *pyParser) expect(c byte) error {
	p.space()
	if p.i >= len(p.s) || p.s[p.i] != c {
		return p.errorf("want %q", string(c))
	}
	p.i++
	return nil
}

// closes reports, after optional space, whether the next byte is end, and
// consumes it if so.
func (p *pyParser) closes(end byte) bool {
	p.space()
	if p.i < len(p.s) && p.s[p.i] == end {
		p.i++
		return true
	}
	return false
}

func (p *pyParser) dict() (any, error) {
	p.i++ // {
	out := map[string]any{}
	for !p.closes('}') {
		p.space()
		k, err := p.value()
		if err != nil {
			return nil, err
		}
		if err := p.expect(':'); err != nil {
			return nil, err
		}
		p.space()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		key, ok := k.(string)
		if !ok {
			key = fmt.Sprint(k)
		}
		out[key] = v
		if p.closes('}') {
			break
		}
		if err := p.expect(','); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (p *pyParser) seq(end byte) (any, error) {
	p.i++ // [ or (
	out := []any{}
	for !p.closes(end) {
		p.space()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if p.closes(end) {
			break
		}
		if err := p.expect(','); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (p *pyParser) str() (any, error) {
	quote := p.s[p.i]
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == quote:
			p.i++
			return b.String(), nil
		case c == '\\' && p.i+1 < len(p.s):
			if err := p.escape(&b); err != nil {
				return nil, err
			}
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	return nil, p.errorf("unterminated string")
}

// escape decodes one backslash escape. Unknown escapes keep the backslash,
// as Python does.
func (p *pyParser) escape(b *strings.Builder) error {
	c := p.s[p.i+1]
	p.i += 2
	switch c {
	case 'n':
		b.WriteByte('\n')
	case 't':
		b.WriteByte('\t')
	case 'r':
		b.WriteByte('\r')
	case '0':
		b.WriteByte(0)
	case '\\', '\'', '"':
		b.WriteByte(c)
	case 'x', 'u', 'U':
		width := map[byte]int{'x': 2, 'u': 4, 'U': 8}[c]
		if p.i+width > len(p.s) {
			return p.errorf("short \\%c escape", c)
		}
		n, err := strconv.ParseInt(p.s[p.i:p.i+width], 16, 32)
		if err != nil || !utf8.ValidRune(rune(n)) {
			return p.errorf("bad \\%c escape", c)
		}
		b.WriteRune(rune(n))
		p.i += width
	default:
		b.WriteByte('\\')
		b.WriteByte(c)
	}
	return nil
}

func (p *pyParser) number() (any, error) {
	start := p.i
	for p.i < len(p.s) && strings.ContainsRune("0123456789+-.eE", rune(p.s[p.i])) {
		p.i++
	}
	text := p.s[start:p.i]
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, p.errorf("bad number %q", text)
	}
	return f, nil
}

// keywords are the bare names a literal can hold.
var keywords = map[string]any{"True": true, "False": false, "None": nil, "nan": math.NaN(), "inf": math.Inf(1)}

func isNameByte(c byte) bool {
	return c == '_' || c == '.' || (c >= '0' && c <= '9') || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func (p *pyParser) ident() (any, error) {
	start := p.i
	for p.i < len(p.s) && isNameByte(p.s[p.i]) {
		p.i++
	}
	name := p.s[start:p.i]
	if p.i < len(p.s) && p.s[p.i] == '(' {
		args, err := p.seq(')')
		if err != nil {
			return nil, err
		}
		if list, _ := args.([]any); len(list) == 1 {
			return list[0], nil
		}
		return args, nil
	}
	if v, ok := keywords[name]; ok {
		return v, nil
	}
	return nil, p.errorf("unknown name %q", name)
}
```

Create `raw/tsv.go`:

```go
package raw

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"time"
)

// Row is one record of a table, read by column name.
type Row struct {
	cols   map[string]int
	fields []string
}

// Get returns the named column, or "" when the table has no such column.
func (r Row) Get(name string) string {
	i, ok := r.cols[name]
	if !ok {
		return ""
	}
	return r.fields[i]
}

// ReadTSV calls fn for each record of a pandas-exported table: tab
// separators, a header row, and CSV quoting that allows embedded newlines.
// It stops at the first error, including one returned by fn.
func ReadTSV(r io.Reader, fn func(Row) error) error {
	cr := csv.NewReader(r)
	cr.Comma = '\t'
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	cols := make(map[string]int, len(header))
	for i, name := range header {
		cols[name] = i
	}
	for {
		fields, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(Row{cols: cols, fields: fields}); err != nil {
			return err
		}
	}
}

// ParseTime reads a timestamp as the export writes it, in UTC.
func ParseTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", time.RFC3339Nano} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q", s)
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./raw`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/raw`. `TestEveryRawTableParses` skips until Task 13 downloads the data.

- [ ] **Step 6: Lint**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 7: Commit**

From the repository root:

```bash
git add bench/itbench/go.mod bench/itbench/go.sum bench/itbench/.gitignore bench/itbench/raw/pyrepr.go bench/itbench/raw/tsv.go \
  bench/itbench/raw/pyrepr_test.go bench/itbench/raw/tsv_test.go bench/itbench/raw/realdata_test.go
git commit -m "feat(bench): read ITBench-Lite's raw tables"
git push
```

---

### Task 2: The dataset

**Files:**
- Create: `bench/itbench/dataset/dataset.go`
- Test: `bench/itbench/dataset/dataset_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces, in package `dataset`:
  - `type Meta struct { Namespace string; AlertTime, WindowStart, TracesUntil time.Time; Trigger []Alert }`;
  - the records `Alert`, `Change` with `Field { Path string; From, To *string }`, `Object` (with `Body json.RawMessage`), `Event`, `Log`, `Span`, `Edge`, `ServicePoint` and `PodPoint`;
  - `type Dataset struct { Meta Meta; Alerts []Alert; Changes []Change; Objects []Object; Events []Event; Logs []Log; Spans []Span; Edges []Edge; Services []ServicePoint; Pods []PodPoint }`;
  - `func (d *Dataset) Save(dir string) error` and `func Load(dir string) (*Dataset, error)`, both through an `os.Root` on `dir`.

- [ ] **Step 1: Write the failing tests**

Create `dataset/dataset_test.go`:

```go
package dataset_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

func TestSaveThenLoadReturnsTheSameDataset(t *testing.T) {
	at := time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC)
	to := "quote:0000"
	in := &dataset.Dataset{
		Meta: dataset.Meta{Namespace: "otel-demo", AlertTime: at, WindowStart: at.Add(-30 * time.Minute),
			Trigger: []dataset.Alert{{Name: "RequestErrorRate", Service: "shipping", State: "firing", ActiveAt: at}}},
		Alerts:   []dataset.Alert{{Polled: at, Name: "RequestErrorRate", Service: "shipping", State: "firing", ActiveAt: at}},
		Changes:  []dataset.Change{{Time: at, Kind: "Deployment", Name: "shipping", Service: "shipping", Action: "updated", Fields: []dataset.Field{{Path: "env", To: &to}}}},
		Objects:  []dataset.Object{{Kind: "Deployment", Name: "shipping", Observed: at, Body: json.RawMessage(`{"kind":"Deployment"}`)}},
		Events:   []dataset.Event{{Time: at, Type: "Warning", Reason: "BackOff", ObjectKind: "Pod", ObjectName: "shipping-1", Message: "back-off"}},
		Logs:     []dataset.Log{{Time: at, Service: "shipping", Body: "failed"}},
		Spans:    []dataset.Span{{Time: at, TraceID: "t", SpanID: "s", Service: "shipping", Name: "/get-quote", Kind: "Server", Status: "Error"}},
		Edges:    []dataset.Edge{{From: "checkout", To: "shipping", Calls: 10, Errors: 2}},
		Services: []dataset.ServicePoint{{Time: at, Service: "shipping", Operation: "/get-quote", Kind: "server", Requests: 10, Errors: 2, P95MS: 12.5}},
		Pods:     []dataset.PodPoint{{Time: at, Pod: "shipping-1", Service: "shipping", CPUCores: 0.1, MemoryBytes: 1e6}},
	}
	dir := t.TempDir()

	require.NoError(t, in.Save(dir))
	out, err := dataset.Load(dir)

	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestLoadFailsWithoutMeta(t *testing.T) {
	_, err := dataset.Load(t.TempDir())

	assert.Error(t, err)
}

func TestSaveWritesOneFilePerStream(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, (&dataset.Dataset{}).Save(dir))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"meta.json", "alerts.jsonl", "changes.jsonl", "objects.jsonl", "events.jsonl",
		"logs.jsonl", "spans.jsonl", "edges.jsonl", "service_metrics.jsonl", "pod_metrics.jsonl"}, names)
	meta, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	require.NoError(t, err)
	assert.Contains(t, string(meta), `"alert_time"`)
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./dataset`
Expected: FAIL with `no non-test Go files in .../bench/itbench/dataset`.

- [ ] **Step 3: Write the dataset**

The JSON names are what agents read, so they are plain words in snake case. Create `dataset/dataset.go`:

```go
// Package dataset is the prepared form of one benchmark scenario: what the
// read-only tools serve, cut at the alert time and stripped of the harness.
package dataset

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// Meta describes the scenario as the responders would see it. TracesUntil
// is the last span in the window: in several scenarios the benchmark's
// traces end before the fault.
type Meta struct {
	Namespace   string    `json:"namespace"`
	AlertTime   time.Time `json:"alert_time"`
	WindowStart time.Time `json:"window_start"`
	TracesUntil time.Time `json:"traces_until,omitzero"`
	Trigger     []Alert   `json:"trigger"`
}

// Alert is one alert in one poll of the alerting system.
type Alert struct {
	Polled      time.Time `json:"polled,omitzero"`
	Name        string    `json:"name"`
	Service     string    `json:"service,omitempty"`
	Pod         string    `json:"pod,omitempty"`
	Namespace   string    `json:"namespace,omitempty"`
	Severity    string    `json:"severity,omitempty"`
	State       string    `json:"state"`
	ActiveAt    time.Time `json:"active_at"`
	Description string    `json:"description,omitempty"`
}

// Change is one change to an object's desired state.
type Change struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	Service string    `json:"service,omitempty"`
	Action  string    `json:"action"`
	Fields  []Field   `json:"fields,omitempty"`
}

// Field is one changed value. A nil From means added, a nil To removed.
type Field struct {
	Path string  `json:"path"`
	From *string `json:"from,omitempty"`
	To   *string `json:"to,omitempty"`
}

// Object is the last snapshot of an object at or before the alert.
type Object struct {
	Kind     string          `json:"kind"`
	Name     string          `json:"name"`
	Service  string          `json:"service,omitempty"`
	Observed time.Time       `json:"observed"`
	Body     json.RawMessage `json:"body"`
}

// Event is one Kubernetes event: its last occurrence, and its first.
type Event struct {
	Time       time.Time `json:"time"`
	First      time.Time `json:"first,omitzero"`
	Type       string    `json:"type"`
	Reason     string    `json:"reason"`
	ObjectKind string    `json:"object_kind"`
	ObjectName string    `json:"object_name"`
	Message    string    `json:"message"`
	Count      int       `json:"count,omitempty"`
	Source     string    `json:"source,omitempty"`
}

// Log is one log record.
type Log struct {
	Time     time.Time `json:"time"`
	Service  string    `json:"service"`
	Pod      string    `json:"pod,omitempty"`
	Severity string    `json:"severity,omitempty"`
	Body     string    `json:"body"`
	TraceID  string    `json:"trace_id,omitempty"`
}

// Span is one span that ended in error.
type Span struct {
	Time       time.Time         `json:"time"`
	TraceID    string            `json:"trace_id"`
	SpanID     string            `json:"span_id"`
	ParentID   string            `json:"parent_id,omitempty"`
	Service    string            `json:"service"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	DurationMS float64           `json:"duration_ms"`
	Status     string            `json:"status"`
	Message    string            `json:"message,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Edge counts the calls from one service to another, from all spans.
type Edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Calls  int    `json:"calls"`
	Errors int    `json:"errors"`
}

// ServicePoint is one minute of one operation's request metrics.
type ServicePoint struct {
	Time      time.Time `json:"time"`
	Service   string    `json:"service"`
	Operation string    `json:"operation"`
	Kind      string    `json:"kind"`
	Requests  float64   `json:"requests"`
	Errors    float64   `json:"errors"`
	P95MS     float64   `json:"p95_ms,omitempty"`
}

// PodPoint is one sample of one pod's resource use.
type PodPoint struct {
	Time        time.Time `json:"time"`
	Pod         string    `json:"pod"`
	Service     string    `json:"service,omitempty"`
	CPUCores    float64   `json:"cpu_cores"`
	MemoryBytes float64   `json:"memory_bytes"`
	CPULimit    float64   `json:"cpu_limit_cores,omitempty"`
	MemoryLimit float64   `json:"memory_limit_bytes,omitempty"`
}

// Dataset is one prepared scenario.
type Dataset struct {
	Meta     Meta
	Alerts   []Alert
	Changes  []Change
	Objects  []Object
	Events   []Event
	Logs     []Log
	Spans    []Span
	Edges    []Edge
	Services []ServicePoint
	Pods     []PodPoint
}

// Save writes the dataset into dir, one file per stream.
func (d *Dataset) Save(dir string) (err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	meta, err := json.MarshalIndent(d.Meta, "", "  ")
	if err != nil {
		return err
	}
	return errors.Join(
		root.WriteFile("meta.json", meta, 0o600),
		writeLines(root, "alerts.jsonl", d.Alerts),
		writeLines(root, "changes.jsonl", d.Changes),
		writeLines(root, "objects.jsonl", d.Objects),
		writeLines(root, "events.jsonl", d.Events),
		writeLines(root, "logs.jsonl", d.Logs),
		writeLines(root, "spans.jsonl", d.Spans),
		writeLines(root, "edges.jsonl", d.Edges),
		writeLines(root, "service_metrics.jsonl", d.Services),
		writeLines(root, "pod_metrics.jsonl", d.Pods),
	)
}

// Load reads a dataset that Save wrote.
func Load(dir string) (_ *Dataset, err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	meta, err := root.ReadFile("meta.json")
	if err != nil {
		return nil, err
	}
	d := &Dataset{}
	if err := json.Unmarshal(meta, &d.Meta); err != nil {
		return nil, fmt.Errorf("meta.json: %w", err)
	}
	return d, errors.Join(
		readLines(root, "alerts.jsonl", &d.Alerts),
		readLines(root, "changes.jsonl", &d.Changes),
		readLines(root, "objects.jsonl", &d.Objects),
		readLines(root, "events.jsonl", &d.Events),
		readLines(root, "logs.jsonl", &d.Logs),
		readLines(root, "spans.jsonl", &d.Spans),
		readLines(root, "edges.jsonl", &d.Edges),
		readLines(root, "service_metrics.jsonl", &d.Services),
		readLines(root, "pod_metrics.jsonl", &d.Pods),
	)
}

func writeLines[T any](root *os.Root, name string, items []T) (err error) {
	f, err := root.Create(name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for i := range items {
		if err := enc.Encode(&items[i]); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return w.Flush()
}

func readLines[T any](root *os.Root, name string, out *[]T) (err error) {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	dec := json.NewDecoder(bufio.NewReader(f))
	for dec.More() {
		var item T
		if err := dec.Decode(&item); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*out = append(*out, item)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./dataset`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/dataset`.

- [ ] **Step 5: Lint**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add bench/itbench/dataset/dataset.go bench/itbench/dataset/dataset_test.go
git commit -m "feat(bench): add the prepared dataset of a scenario"
git push
```

---

### Task 3: The trigger and the alert polls

**Files:**
- Create: `bench/itbench/prepare/doc.go`, `bench/itbench/prepare/alerts.go`
- Test: `bench/itbench/prepare/alerts_test.go`

**Interfaces:**
- Consumes: `dataset.Alert` (Task 2).
- Produces, in package `prepare` (unexported, used by Task 7):
  - `readTrigger(fsys fs.FS) (time.Time, []dataset.Alert, error)`: the alert time and the trigger's alerts;
  - `readAlertPolls(fsys fs.FS, namespace string, from, to time.Time) ([]dataset.Alert, error)`;
  - the test helper `files(nameContent ...string) fstest.MapFS`, which later tasks' tests use.

- [ ] **Step 1: Write the failing tests**

Create `prepare/alerts_test.go`:

```go
package prepare

import (
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// files builds a file system from name and content pairs.
func files(nameContent ...string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for i := 0; i+1 < len(nameContent); i += 2 {
		fsys[nameContent[i]] = &fstest.MapFile{Data: []byte(nameContent[i+1])}
	}
	return fsys
}

const errorRateAlert = `{"labels": {"alertname": "RequestErrorRate", "namespace": "otel-demo", "service_name": "%s", "severity": "warning"},
 "annotations": {"description": "Request error rate in service %s is above 0"}, "state": "firing", "activeAt": "%s", "value": "1e-02"}`

func TestTheTriggerTimeIsTheLaterOfTheFileTimeAndItsAlerts(t *testing.T) {
	fsys := files("alerts/alerts_in_alerting_state_2025-12-15T175422.255557Z.json",
		`{"data": {"alerts": [`+fmt.Sprintf(errorRateAlert, "frontend-proxy", "frontend-proxy", "2025-12-15T17:55:47.934820964Z")+`,`+
			fmt.Sprintf(errorRateAlert, "frontend", "frontend", "2025-12-15T17:50:00Z")+
			`]}, "status": "success", "timestamp": "2025-12-15T17:54:22Z"}`)

	at, trigger, err := readTrigger(fsys)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 55, 47, 934820964, time.UTC), at)
	require.Len(t, trigger, 2)
	assert.Equal(t, "frontend-proxy", trigger[0].Service)
	assert.Equal(t, "Request error rate in service frontend-proxy is above 0", trigger[0].Description)
}

func TestReadTriggerNeedsExactlyOneTriggerFile(t *testing.T) {
	_, _, err := readTrigger(files())

	assert.ErrorContains(t, err, "want one trigger file")
}

func TestAlertPollsKeepTheWindowAndDropOtherNamespaces(t *testing.T) {
	poll := func(service, activeAt string) string {
		return `[` + fmt.Sprintf(errorRateAlert, service, service, activeAt) + `,
 {"labels": {"alertname": "KubePodNotReady", "namespace": "data-recorders", "pod": "recorder-0"}, "annotations": {}, "state": "firing", "activeAt": "2025-12-15T17:20:00Z"},
 {"labels": {"alertname": "KubeSchedulerDown", "severity": "critical"}, "annotations": {}, "state": "firing", "activeAt": "2025-12-15T17:20:00Z"}]`
	}
	fsys := files(
		"alerts/alerts_at_2025-12-15T15-21-58.000000.json", poll("cart", "2025-12-15T15:20:00Z"),
		"alerts/alerts_at_2025-12-15T17-50-35.385790.json", poll("frontend", "2025-12-15T17:49:47Z"),
		"alerts/alerts_at_2025-12-15T18-00-35.000000.json", poll("checkout", "2025-12-15T17:59:00Z"),
	)
	from := time.Date(2025, 12, 15, 17, 25, 0, 0, time.UTC)
	to := time.Date(2025, 12, 15, 17, 55, 0, 0, time.UTC)

	alerts, err := readAlertPolls(fsys, "otel-demo", from, to)

	require.NoError(t, err)
	require.Len(t, alerts, 2)
	polled := time.Date(2025, 12, 15, 17, 50, 35, 385790000, time.UTC)
	assert.Equal(t, polled, alerts[0].Polled)
	assert.Equal(t, "frontend", alerts[0].Service)
	assert.Equal(t, "KubeSchedulerDown", alerts[1].Name)
	assert.Equal(t, polled, alerts[1].Polled)
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./prepare`
Expected: FAIL with `undefined: readTrigger`. The tests are in package `prepare`, so the build fails on the missing functions.

- [ ] **Step 3: Write the readers**

Create `prepare/doc.go`:

```go
// Package prepare turns one downloaded ITBench-Lite scenario into a dataset:
// the namespace's data from the half hour before the alert, without the
// benchmark's own objects, its writers' names or its fault names.
package prepare
```

Create `prepare/alerts.go`:

```go
package prepare

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// promAlert is one alert as the Prometheus API returns it.
type promAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	State       string            `json:"state"`
	ActiveAt    time.Time         `json:"activeAt"`
}

func (a *promAlert) convert(polled time.Time) dataset.Alert {
	return dataset.Alert{
		Polled:      polled,
		Name:        a.Labels["alertname"],
		Service:     a.Labels["service_name"],
		Pod:         a.Labels["pod"],
		Namespace:   a.Labels["namespace"],
		Severity:    a.Labels["severity"],
		State:       a.State,
		ActiveAt:    a.ActiveAt,
		Description: a.Annotations["description"],
	}
}

// readTrigger reads the benchmark's trigger: the alerts in alerting state when
// it started its agent. The alert time is the later of the file's timestamp
// and the latest activeAt, so that nothing in the trigger postdates it.
func readTrigger(fsys fs.FS) (time.Time, []dataset.Alert, error) {
	files, err := fs.Glob(fsys, "alerts/alerts_in_alerting_state_*.json")
	if err != nil {
		return time.Time{}, nil, err
	}
	if len(files) != 1 {
		return time.Time{}, nil, fmt.Errorf("want one trigger file, found %d", len(files))
	}
	data, err := fs.ReadFile(fsys, files[0])
	if err != nil {
		return time.Time{}, nil, err
	}
	var file struct {
		Data struct {
			Alerts []promAlert `json:"alerts"`
		} `json:"data"`
		Timestamp time.Time `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return time.Time{}, nil, fmt.Errorf("%s: %w", files[0], err)
	}
	at := file.Timestamp.UTC()
	trigger := make([]dataset.Alert, 0, len(file.Data.Alerts))
	for i := range file.Data.Alerts {
		a := file.Data.Alerts[i].convert(time.Time{})
		if a.ActiveAt.After(at) {
			at = a.ActiveAt.UTC()
		}
		trigger = append(trigger, a)
	}
	return at, trigger, nil
}

// readAlertPolls reads the per-minute alert polls taken in [from, to], keeping
// alerts of the namespace and alerts with no namespace.
func readAlertPolls(fsys fs.FS, namespace string, from, to time.Time) ([]dataset.Alert, error) {
	files, err := fs.Glob(fsys, "alerts/alerts_at_*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []dataset.Alert
	for _, file := range files {
		stamp := strings.TrimSuffix(strings.TrimPrefix(path.Base(file), "alerts_at_"), ".json")
		polled, err := time.ParseInLocation("2006-01-02T15-04-05.999999", stamp, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if polled.Before(from) || polled.After(to) {
			continue
		}
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		var alerts []promAlert
		if err := json.Unmarshal(data, &alerts); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		for i := range alerts {
			if ns := alerts[i].Labels["namespace"]; ns == "" || ns == namespace {
				out = append(out, alerts[i].convert(polled))
			}
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./prepare`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/prepare`.

- [ ] **Step 5: Lint**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add bench/itbench/prepare/doc.go bench/itbench/prepare/alerts.go bench/itbench/prepare/alerts_test.go
git commit -m "feat(bench): read the benchmark's trigger and alert polls"
git push
```

---

### Task 4: Changes and objects

**Files:**
- Create: `bench/itbench/prepare/diff.go`, `bench/itbench/prepare/sanitize.go`, `bench/itbench/prepare/tables.go`, `bench/itbench/prepare/objects.go`
- Test: `bench/itbench/prepare/diff_test.go`, `bench/itbench/prepare/objects_test.go`

**Interfaces:**
- Consumes: `raw.ReadTSV`, `raw.ParseTime` (Task 1); `dataset.Change`, `dataset.Field`, `dataset.Object` (Task 2); the test helper `files` (Task 3).
- Produces, in package `prepare`:
  - `diffTrees(path string, a, b any) []dataset.Field` and `render(v any) *string`;
  - `scrub(s string) string`, `scrubTree(v any) any`, `isHarness`, `stripObject`, `metadata`, `stringMap`, and `const harnessMark = "it-bench"`;
  - `readRows(fsys fs.FS, name string, fn func(raw.Row) error) error`;
  - `readSnapshots(fsys fs.FS, name, namespace string, to time.Time) (map[objKey][]snapshot, error)`, `buildObjects(...) []dataset.Object`, `buildChanges(snaps, from, to) []dataset.Change`;
  - the helpers `str0`, `scrubPtr` and `marshal`; the test helpers `str`, `tsvRow`, `table`, `deployment` and `helmWrite`.

- [ ] **Step 1: Write the failing tests**

Create `prepare/diff_test.go`:

```go
package prepare

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal([]byte(s), &v))
	return v
}

func str(s string) *string { return &s }

func TestDiffMatchesNamedListItemsByName(t *testing.T) {
	a := decode(t, `{"spec":{"containers":[{"name":"shipping","env":[{"name":"A","value":"1"},{"name":"QUOTE_ADDR","value":"quote:8080"}]}]}}`)
	b := decode(t, `{"spec":{"containers":[{"name":"shipping","env":[{"name":"QUOTE_ADDR","value":"quote:0000"},{"name":"A","value":"1"}]}]}}`)

	got := diffTrees("", a, b)

	assert.Equal(t, []dataset.Field{{Path: "spec.containers[shipping].env[QUOTE_ADDR].value", From: str("quote:8080"), To: str("quote:0000")}}, got)
}

func TestDiffComparesJSONStringsByContent(t *testing.T) {
	a := decode(t, `{"data":{"demo.flagd.json":"{\n  \"flags\": {\n    \"x\": {\"defaultVariant\": \"off\", \"state\": \"ENABLED\"}\n  }\n}"}}`)
	same := decode(t, `{"data":{"demo.flagd.json":"{\"flags\":{\"x\":{\"state\":\"ENABLED\",\"defaultVariant\":\"off\"}}}"}}`)
	b := decode(t, `{"data":{"demo.flagd.json":"{\"flags\":{\"x\":{\"state\":\"ENABLED\",\"defaultVariant\":\"on\"}}}"}}`)

	assert.Empty(t, diffTrees("", a, same))
	assert.Equal(t, []dataset.Field{{Path: `data["demo.flagd.json"].flags.x.defaultVariant`, From: str("off"), To: str("on")}}, diffTrees("", a, b))
}

func TestDiffReportsAddedAndRemovedValues(t *testing.T) {
	a := decode(t, `{"spec":{"replicas":1,"args":["a","b"]}}`)
	b := decode(t, `{"spec":{"nodeSelector":{"disk":"ssd"},"args":["a"]}}`)

	got := diffTrees("", a, b)

	assert.Equal(t, []dataset.Field{
		{Path: "spec.args[1]", From: str("b")},
		{Path: "spec.nodeSelector", To: str(`{"disk":"ssd"}`)},
		{Path: "spec.replicas", From: str("1")},
	}, got)
}

func TestDiffTruncatesLongValues(t *testing.T) {
	long := make([]byte, 600)
	for i := range long {
		long[i] = 'x'
	}
	got := diffTrees("", decode(t, `{"a":"`+string(long)+`"}`), decode(t, `{"a":"y"}`))

	require.Len(t, got, 1)
	assert.Len(t, []rune(*got[0].From), maxValue+1)
	assert.Equal(t, "y", *got[0].To)
}
```

The writers in these tests are the ones the data holds: `helm` installed the demo, `OpenAPI-Generator` is the benchmark's injector, and `kube-controller-manager` is a controller. Create `prepare/objects_test.go`:

```go
package prepare

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// tsvRow is one raw table row: a timestamp and a JSON body.
type tsvRow struct{ at, body string }

// table renders rows the way pandas exports them: quoted JSON with doubled quotes.
func table(rows ...tsvRow) string {
	var b strings.Builder
	b.WriteString("Timestamp\tTimestampTime\tBody\n")
	for _, r := range rows {
		b.WriteString(r.at + "\t" + r.at[:19] + "\t\"" + strings.ReplaceAll(r.body, `"`, `""`) + "\"\n")
	}
	return b.String()
}

func deployment(env, managers string) string {
	return `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"shipping","namespace":"otel-demo",
 "labels":{"opentelemetry.io/name":"shipping","it-bench/monitoring":"true"},
 "annotations":{"deployment.kubernetes.io/revision":"2"},"resourceVersion":"9","managedFields":[` + managers + `]},
 "spec":{"template":{"spec":{"containers":[{"name":"shipping","image":"ghcr.io/open-telemetry/demo:2.1.3-shipping",
 "env":[{"name":"QUOTE_ADDR","value":"` + env + `"}]}]}}},"status":{"replicas":1}}`
}

const helmWrite = `{"manager":"helm","operation":"Update","time":"2025-12-15T17:15:20Z"}`

func TestReadSnapshotsKeepsTheNamespaceUpToTheCut(t *testing.T) {
	fsys := files("k8s_objects_raw.tsv", table(
		tsvRow{"2025-12-15 17:30:41.1", deployment("quote:8080", helmWrite)},
		tsvRow{"2025-12-15 17:30:41.2", `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"x","namespace":"data-recorders"}}`},
		tsvRow{"2025-12-15 17:30:41.3", `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"Role","metadata":{"name":"agent-access","namespace":"otel-demo","labels":{"app.kubernetes.io/part-of":"it-bench"}}}`},
		tsvRow{"2025-12-15 17:30:41.4", `{"apiVersion":"chaos-mesh.org/v1alpha1","kind":"NetworkChaos","metadata":{"name":"p","namespace":"otel-demo"}}`},
		tsvRow{"2025-12-15 18:00:41.1", deployment("quote:0000", helmWrite)},
	))

	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC))

	require.NoError(t, err)
	require.Len(t, snaps, 1)
	assert.Len(t, snaps[objKey{"Deployment", "shipping"}], 1)
}

func TestObjectsAreTheLastSnapshotWithoutWritersOrHarnessLabels(t *testing.T) {
	fsys := files("k8s_objects_raw.tsv", table(
		tsvRow{"2025-12-15 17:50:41.1", deployment("quote:8080", helmWrite)},
		tsvRow{"2025-12-15 17:55:41.1", deployment("quote:0000", helmWrite)},
	))
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC))
	require.NoError(t, err)

	objects := buildObjects(snaps)

	require.Len(t, objects, 1)
	o := objects[0]
	assert.Equal(t, "shipping", o.Service)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 55, 41, 100000000, time.UTC), o.Observed)
	body := string(o.Body)
	assert.Contains(t, body, "quote:0000")
	assert.NotContains(t, body, "managedFields")
	assert.NotContains(t, body, "helm\"")
	assert.NotContains(t, body, "it-bench")
}

func TestChangesCarryTheWritersTimeButNotTheWriter(t *testing.T) {
	injected := helmWrite + `,{"manager":"OpenAPI-Generator","operation":"Update","time":"2025-12-15T17:52:47Z"},
 {"manager":"kube-controller-manager","operation":"Update","time":"2025-12-15T17:52:50Z"}`
	fsys := files("k8s_objects_raw.tsv", table(
		tsvRow{"2025-12-15 17:50:41.1", deployment("quote:8080", helmWrite)},
		tsvRow{"2025-12-15 17:55:41.1", deployment("quote:0000", injected)},
	))
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC))
	require.NoError(t, err)

	changes := buildChanges(snaps, time.Date(2025, 12, 15, 17, 27, 59, 0, time.UTC), time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC))

	require.Len(t, changes, 1)
	assert.Equal(t, dataset.Change{
		Time: time.Date(2025, 12, 15, 17, 52, 47, 0, time.UTC), Kind: "Deployment", Name: "shipping", Service: "shipping", Action: "updated",
		Fields: []dataset.Field{{Path: "spec.template.spec.containers[shipping].env[QUOTE_ADDR].value", From: str("quote:8080"), To: str("quote:0000")}},
	}, changes[0])
	text, err := json.Marshal(changes)
	require.NoError(t, err)
	assert.NotContains(t, string(text), "OpenAPI-Generator")
}

func TestChangesIgnoreStatusRevisionsAndRuntimeKinds(t *testing.T) {
	pod := func(phase string) string {
		return `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"shipping-1","namespace":"otel-demo"},"spec":{"nodeName":"` + phase + `"}}`
	}
	fsys := files("k8s_objects_raw.tsv", table(
		tsvRow{"2025-12-15 17:50:41.1", deployment("quote:8080", helmWrite)},
		tsvRow{"2025-12-15 17:50:41.2", pod("a")},
		tsvRow{"2025-12-15 17:55:41.1", strings.Replace(strings.Replace(deployment("quote:8080", helmWrite), `"revision":"2"`, `"revision":"3"`, 1), `"replicas":1`, `"replicas":2`, 1)},
		tsvRow{"2025-12-15 17:55:41.2", pod("b")},
	))
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC))
	require.NoError(t, err)

	assert.Empty(t, buildChanges(snaps, time.Date(2025, 12, 15, 17, 27, 59, 0, time.UTC), time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC)))
}

func TestACreationInTheWindowIsAChange(t *testing.T) {
	quota := `{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"otel-demo-memory","namespace":"otel-demo","creationTimestamp":"2025-12-15T17:54:21Z"},"spec":{"hard":{"memory":"1Gi"}}}`
	old := `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"old","namespace":"otel-demo","creationTimestamp":"2025-12-15T17:15:21Z"}}`
	fsys := files("k8s_objects_raw.tsv", table(tsvRow{"2025-12-15 17:59:03.1", quota}, tsvRow{"2025-12-15 17:59:03.2", old}))
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 59, 18, 0, time.UTC))
	require.NoError(t, err)

	changes := buildChanges(snaps, time.Date(2025, 12, 15, 17, 29, 18, 0, time.UTC), time.Date(2025, 12, 15, 17, 59, 18, 0, time.UTC))

	assert.Equal(t, []dataset.Change{{Time: time.Date(2025, 12, 15, 17, 54, 21, 0, time.UTC), Kind: "ResourceQuota", Name: "otel-demo-memory", Action: "created"}}, changes)
}

func TestAConfigMapBelongsToTheOneServiceThatMountsIt(t *testing.T) {
	flagd := `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"flagd","namespace":"otel-demo","labels":{"opentelemetry.io/name":"flagd"}},
 "spec":{"template":{"spec":{"volumes":[{"name":"config","configMap":{"name":"flagd-config"}}]}}}}`
	config := func(variant, managers string) string {
		return `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"flagd-config","namespace":"otel-demo","managedFields":[` + managers + `]},
 "data":{"demo.flagd.json":"{\"flags\":{\"productCatalogFailure\":{\"defaultVariant\":\"` + variant + `\"}}}"}}`
	}
	fsys := files("k8s_objects_raw.tsv", table(
		tsvRow{"2025-12-15 17:43:28.1", flagd},
		tsvRow{"2025-12-15 17:43:28.2", config("off", helmWrite)},
		tsvRow{"2025-12-15 17:48:28.2", config("on", helmWrite+`,{"manager":"OpenAPI-Generator","operation":"Update","time":"2025-12-15T17:48:20Z"}`)},
	))
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 55, 47, 0, time.UTC))
	require.NoError(t, err)

	changes := buildChanges(snaps, time.Date(2025, 12, 15, 17, 25, 47, 0, time.UTC), time.Date(2025, 12, 15, 17, 55, 47, 0, time.UTC))

	require.Len(t, changes, 1)
	assert.Equal(t, "flagd", changes[0].Service)
	assert.Equal(t, `data["demo.flagd.json"].flags.productCatalogFailure.defaultVariant`, changes[0].Fields[0].Path)
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./prepare`
Expected: FAIL with `undefined: diffTrees`.

- [ ] **Step 3: Write the diff**

Create `prepare/diff.go`:

```go
package prepare

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// maxValue caps the runes of a value in a change, so a changed blob stays readable.
const maxValue = 300

// missing marks a key or list item that one side does not have.
type missing struct{}

var plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// diffTrees lists the differences between two decoded JSON values, in path
// order. Lists whose items all have a unique name are matched by name, and
// strings that hold JSON objects or arrays are compared by content, so
// reordering or reformatting is not a change.
func diffTrees(path string, a, b any) []dataset.Field {
	a, b = parseJSONString(a), parseJSONString(b)
	if am, ok := a.(map[string]any); ok {
		if bm, ok := b.(map[string]any); ok {
			return diffMaps(path, am, bm, join)
		}
	}
	if al, ok := a.([]any); ok {
		if bl, ok := b.([]any); ok {
			if an, bn := byName(al), byName(bl); an != nil && bn != nil {
				return diffMaps(path, an, bn, item)
			}
			return diffLists(path, al, bl)
		}
	}
	if reflect.DeepEqual(a, b) {
		return nil
	}
	return []dataset.Field{{Path: path, From: render(a), To: render(b)}}
}

func diffMaps(path string, a, b map[string]any, key func(path, k string) string) []dataset.Field {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []dataset.Field
	for _, k := range keys {
		out = append(out, diffTrees(key(path, k), get(a, k), get(b, k))...)
	}
	return out
}

func diffLists(path string, a, b []any) []dataset.Field {
	var out []dataset.Field
	for i := range max(len(a), len(b)) {
		var av, bv any = missing{}, missing{}
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		out = append(out, diffTrees(path+"["+strconv.Itoa(i)+"]", av, bv)...)
	}
	return out
}

// byName indexes a list by its items' names, or returns nil if any item has
// no name or a name repeats.
func byName(list []any) map[string]any {
	out := make(map[string]any, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil
		}
		name, ok := m["name"].(string)
		if !ok {
			return nil
		}
		if _, dup := out[name]; dup {
			return nil
		}
		out[name] = item
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func get(m map[string]any, k string) any {
	if v, ok := m[k]; ok {
		return v
	}
	return missing{}
}

// item appends a named list item to a path.
func item(path, name string) string { return path + "[" + name + "]" }

// join appends a key to a path, in brackets unless it is a plain identifier.
func join(path, key string) string {
	if plainKey.MatchString(key) {
		if path == "" {
			return key
		}
		return path + "." + key
	}
	return path + "[" + strconv.Quote(key) + "]"
}

func parseJSONString(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") && !strings.HasPrefix(t, "[") {
		return v
	}
	var parsed any
	if json.Unmarshal([]byte(t), &parsed) != nil {
		return v
	}
	return parsed
}

func render(v any) *string {
	if _, ok := v.(missing); ok {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil {
			b = []byte("?")
		}
		s = string(b)
	}
	if r := []rune(s); len(r) > maxValue {
		s = string(r[:maxValue]) + "…"
	}
	return &s
}
```

- [ ] **Step 4: Write the renames and the harness rules**

Each rename keeps the failure the same. A quay.io repository that does not exist still fails the pull; an image name with a newline still cannot be parsed; a node selector still matches no node; a command still is not in the image. Create `prepare/sanitize.go`:

```go
package prepare

import (
	"strings"
)

// harnessMark is in every label the benchmark puts on its own objects.
const harnessMark = "it-bench"

// renames replace the names the benchmark gave its faulty values with
// plausible ones. Each keeps the failure the same: an image that cannot be
// pulled, an image name that cannot be parsed, a node that does not exist and
// a command that is not in the image.
var renames = strings.NewReplacer(
	"it-bench/hello-bench-invalid", "open-telemetry/demo-product-catalog",
	"it-bench/hello-bench-arm64", "open-telemetry/demo-checkout",
	"invalid-node", "i-0e4b1c7d9a2f36b58",
	"invalid-command", "product-catalog-server",
)

// scrub applies the renames to one string.
func scrub(s string) string { return renames.Replace(s) }

// scrubTree applies the renames to every string in a decoded JSON value.
func scrubTree(v any) any {
	switch t := v.(type) {
	case string:
		return scrub(t)
	case map[string]any:
		for k, x := range t {
			t[k] = scrubTree(x)
		}
	case []any:
		for i, x := range t {
			t[i] = scrubTree(x)
		}
	}
	return v
}

// isHarness reports whether an object belongs to the benchmark rather than the
// system under test: chaos resources, and objects labeled as the benchmark's.
func isHarness(body map[string]any) bool {
	if api, _ := body["apiVersion"].(string); strings.Contains(api, "chaos-mesh.org") {
		return true
	}
	for _, v := range stringMap(metadata(body)["labels"]) {
		if strings.Contains(v, harnessMark) {
			return true
		}
	}
	return false
}

// stripObject removes the writers' identities and the benchmark's labels, and
// applies the renames.
func stripObject(body map[string]any) {
	md := metadata(body)
	delete(md, "managedFields")
	for _, field := range []string{"labels", "annotations"} {
		m, ok := md[field].(map[string]any)
		if !ok {
			continue
		}
		for k, v := range m {
			if s, _ := v.(string); strings.Contains(k, harnessMark) || strings.Contains(s, harnessMark) {
				delete(m, k)
			}
		}
	}
	scrubTree(body)
}

func metadata(body map[string]any) map[string]any {
	md, _ := body["metadata"].(map[string]any)
	if md == nil {
		return map[string]any{}
	}
	return md
}

func stringMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	out := make(map[string]string, len(m))
	for k, x := range m {
		if s, ok := x.(string); ok {
			out[k] = s
		}
	}
	return out
}
```

- [ ] **Step 5: Write the snapshots, objects and changes**

Create `prepare/tables.go`:

```go
package prepare

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

// readRows calls fn for each row of a raw table.
func readRows(fsys fs.FS, name string, fn func(raw.Row) error) (err error) {
	f, err := fsys.Open(name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if err := raw.ReadTSV(f, fn); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
```

Create `prepare/objects.go`:

```go
package prepare

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

// controllers are writers whose updates are the system reacting, not a change.
var controllers = map[string]bool{"kube-controller-manager": true, "kubelet": true, "kube-scheduler": true}

// runtimeKinds are objects the system creates and updates on its own.
var runtimeKinds = map[string]bool{
	"Pod": true, "ReplicaSet": true, "Endpoints": true, "EndpointSlice": true,
	"Event": true, "Lease": true, "ControllerRevision": true,
}

// generated are metadata fields and annotations the API server or a
// controller sets, which are never part of a change.
var (
	generatedMeta        = []string{"managedFields", "resourceVersion", "generation", "uid", "creationTimestamp", "selfLink"}
	generatedAnnotations = []string{"deployment.kubernetes.io/revision", "kubectl.kubernetes.io/last-applied-configuration"}
)

type objKey struct{ kind, name string }

// snapshot is one object as one pull saw it.
type snapshot struct {
	at   time.Time
	body map[string]any
}

// readSnapshots reads the snapshots of the namespace's objects taken at or
// before to, oldest first, without the benchmark's own objects.
func readSnapshots(fsys fs.FS, name, namespace string, to time.Time) (map[objKey][]snapshot, error) {
	out := map[objKey][]snapshot{}
	err := readRows(fsys, name, func(r raw.Row) error {
		at, err := raw.ParseTime(r.Get("Timestamp"))
		if err != nil {
			return err
		}
		if at.After(to) {
			return nil
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(r.Get("Body")), &body); err != nil {
			return err
		}
		md := metadata(body)
		if md["namespace"] != namespace || isHarness(body) {
			return nil
		}
		kind, _ := body["kind"].(string)
		objName, _ := md["name"].(string)
		k := objKey{kind, objName}
		out[k] = append(out[k], snapshot{at: at, body: body})
		return nil
	})
	for _, list := range out {
		sort.SliceStable(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
	}
	return out, err
}

// buildObjects returns each object's last snapshot, stripped.
func buildObjects(snaps map[objKey][]snapshot) []dataset.Object {
	mounts := mountedBy(snaps)
	out := make([]dataset.Object, 0, len(snaps))
	for k, list := range snaps {
		last := list[len(list)-1]
		var body map[string]any
		_ = json.Unmarshal(marshal(last.body), &body)
		stripObject(body)
		out = append(out, dataset.Object{Kind: k.kind, Name: k.name, Service: serviceOf(k, last.body, mounts), Observed: last.at, Body: marshal(body)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// buildChanges derives the changes to desired state made in (from, to]: a
// creation at its creation time, and an update between two snapshots at the
// latest time a writer other than a controller wrote it. Writers stay
// anonymous. Deletions are not derived.
func buildChanges(snaps map[objKey][]snapshot, from, to time.Time) []dataset.Change {
	mounts := mountedBy(snaps)
	var out []dataset.Change
	for k, list := range snaps {
		if runtimeKinds[k.kind] || metadata(list[len(list)-1].body)["ownerReferences"] != nil {
			continue
		}
		service := serviceOf(k, list[len(list)-1].body, mounts)
		created, err := time.Parse(time.RFC3339, str0(metadata(list[0].body)["creationTimestamp"]))
		if err == nil && created.After(from) && !created.After(to) {
			out = append(out, dataset.Change{Time: created, Kind: k.kind, Name: k.name, Service: service, Action: "created"})
		}
		for i := 1; i < len(list); i++ {
			prev, next := list[i-1], list[i]
			fields := diffTrees("", desired(prev.body), desired(next.body))
			if len(fields) == 0 {
				continue
			}
			at, ok := lastWrite(next.body, prev.at)
			if !ok || at.Before(from) || at.After(to) {
				continue
			}
			for j := range fields {
				fields[j].From, fields[j].To = scrubPtr(fields[j].From), scrubPtr(fields[j].To)
			}
			out = append(out, dataset.Change{Time: at, Kind: k.kind, Name: k.name, Service: service, Action: "updated", Fields: fields})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.Before(out[j].Time)
		}
		return out[i].Kind+"/"+out[i].Name < out[j].Kind+"/"+out[j].Name
	})
	return out
}

// desired is the part of an object that a change sets: no status, no
// generated metadata, and none of the benchmark's labels.
func desired(body map[string]any) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		if k != "status" {
			out[k] = v
		}
	}
	md := make(map[string]any)
	for k, v := range metadata(body) {
		md[k] = v
	}
	for _, k := range generatedMeta {
		delete(md, k)
	}
	for _, field := range []string{"labels", "annotations"} {
		kept := make(map[string]any)
		for k, v := range stringMap(md[field]) {
			if !strings.Contains(k, harnessMark) && !strings.Contains(v, harnessMark) {
				kept[k] = v
			}
		}
		for _, k := range generatedAnnotations {
			delete(kept, k)
		}
		md[field] = kept
	}
	out["metadata"] = md
	return out
}

// lastWrite is the latest time after since that a writer other than a
// controller wrote the object.
func lastWrite(body map[string]any, since time.Time) (time.Time, bool) {
	var latest time.Time
	list, _ := metadata(body)["managedFields"].([]any)
	for _, item := range list {
		m, _ := item.(map[string]any)
		if controllers[str0(m["manager"])] {
			continue
		}
		at, err := time.Parse(time.RFC3339, str0(m["time"]))
		if err == nil && at.After(since) && at.After(latest) {
			latest = at
		}
	}
	return latest, !latest.IsZero()
}

// serviceOf names the service an object belongs to: its name label, or for a
// ConfigMap the one service whose workload mounts it.
func serviceOf(k objKey, body map[string]any, mounts map[string]map[string]bool) string {
	labels := stringMap(metadata(body)["labels"])
	for _, l := range []string{"opentelemetry.io/name", "app.kubernetes.io/name"} {
		if s := labels[l]; s != "" {
			return s
		}
	}
	if k.kind == "ConfigMap" && len(mounts[k.name]) == 1 {
		for s := range mounts[k.name] {
			return s
		}
	}
	return ""
}

// mountedBy maps each ConfigMap to the services whose workloads mount it.
func mountedBy(snaps map[objKey][]snapshot) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for k, list := range snaps {
		if k.kind != "Deployment" && k.kind != "StatefulSet" && k.kind != "DaemonSet" {
			continue
		}
		body := list[len(list)-1].body
		service := serviceOf(k, body, nil)
		spec, _ := body["spec"].(map[string]any)
		template, _ := spec["template"].(map[string]any)
		podSpec, _ := template["spec"].(map[string]any)
		volumes, _ := podSpec["volumes"].([]any)
		for _, v := range volumes {
			vm, _ := v.(map[string]any)
			cm, _ := vm["configMap"].(map[string]any)
			if name := str0(cm["name"]); name != "" && service != "" {
				if out[name] == nil {
					out[name] = map[string]bool{}
				}
				out[name][service] = true
			}
		}
	}
	return out
}

func str0(v any) string {
	s, _ := v.(string)
	return s
}

func scrubPtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := scrub(*s)
	return &v
}

// marshal encodes JSON without escaping HTML characters.
func marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}
```

- [ ] **Step 6: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./prepare`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/prepare`.

- [ ] **Step 7: Lint**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 8: Commit**

```bash
git add bench/itbench/prepare/diff.go bench/itbench/prepare/sanitize.go bench/itbench/prepare/tables.go bench/itbench/prepare/objects.go \
  bench/itbench/prepare/diff_test.go bench/itbench/prepare/objects_test.go
git commit -m "feat(bench): derive anonymous changes and stripped objects from snapshots"
git push
```

---

### Task 5: Events, logs and spans

**Files:**
- Create: `bench/itbench/prepare/events.go`, `bench/itbench/prepare/telemetry.go`
- Test: `bench/itbench/prepare/events_test.go`, `bench/itbench/prepare/telemetry_test.go`

**Interfaces:**
- Consumes: `readRows`, `scrub`, `scrubTree`, `render`, `str0` (Task 4); `raw.ParsePython` (Task 1).
- Produces, in package `prepare`:
  - `readEvents(fsys fs.FS, name, namespace string, from, to time.Time) ([]dataset.Event, error)`;
  - `readLogs(fsys fs.FS, name, namespace string, from, to time.Time) ([]dataset.Log, error)`;
  - `readSpans(fsys fs.FS, name, namespace string, from, to time.Time) ([]dataset.Span, []dataset.Edge, time.Time, error)`, whose time is the last span's;
  - `pyMap(s string) (map[string]any, error)`; the test helpers `columns`, `resource`, `spanRow`, `eventRow`, `windowFrom` and `windowTo`.

- [ ] **Step 1: Write the failing tests**

The event stream records each event twice, as `v1` and as `events.k8s.io/v1`, and again on every update. Create `prepare/events_test.go`:

```go
package prepare

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

func eventRow(uid, ns, kind, name, reason, message, first, last string, count int) string {
	return `{"object":{"apiVersion":"v1","kind":"Event","metadata":{"uid":"` + uid + `","namespace":"` + ns + `"},
 "involvedObject":{"kind":"` + kind + `","name":"` + name + `","namespace":"` + ns + `"},"reason":"` + reason + `","message":"` + message + `",
 "type":"Warning","count":` + strconv.Itoa(count) + `,"firstTimestamp":"` + first + `","lastTimestamp":"` + last + `","source":{"component":"kubelet"}},"type":"ADDED"}`
}

func TestEventsKeepTheLatestStateOfEachCoreEventInTheWindow(t *testing.T) {
	fsys := files("k8s_events_raw.tsv", table(
		tsvRow{"2025-12-15 17:50:09.1", eventRow("u1", "otel-demo", "Pod", "pc-1", "Failed", `Failed to pull image \"quay.io/it-bench/hello-bench-invalid:1.0.0\"`, "2025-12-15T17:50:09Z", "2025-12-15T17:50:09Z", 1)},
		tsvRow{"2025-12-15 17:52:09.1", eventRow("u1", "otel-demo", "Pod", "pc-1", "Failed", `Failed to pull image \"quay.io/it-bench/hello-bench-invalid:1.0.0\"`, "2025-12-15T17:50:09Z", "2025-12-15T17:52:09Z", 3)},
		tsvRow{"2025-12-15 17:56:09.1", eventRow("u1", "otel-demo", "Pod", "pc-1", "Failed", `Failed to pull image`, "2025-12-15T17:50:09Z", "2025-12-15T17:56:09Z", 5)},
		tsvRow{"2025-12-15 17:50:10.1", `{"object":{"apiVersion":"events.k8s.io/v1","kind":"Event","metadata":{"uid":"u2","namespace":"otel-demo"},"regarding":{"kind":"Pod","name":"pc-1","namespace":"otel-demo"},"reason":"Failed","note":"dup"},"type":"ADDED"}`},
		tsvRow{"2025-12-15 17:50:11.1", eventRow("u3", "chaos-mesh", "StressChaos", "s", "Applied", "x", "2025-12-15T17:50:11Z", "2025-12-15T17:50:11Z", 1)},
		tsvRow{"2025-12-15 17:10:11.1", eventRow("u4", "otel-demo", "Pod", "old-1", "BackOff", "old run", "2025-12-15T15:10:11Z", "2025-12-15T17:10:11Z", 1)},
	))

	events, err := readEvents(fsys, "k8s_events_raw.tsv", "otel-demo", time.Date(2025, 12, 15, 17, 25, 50, 0, time.UTC), time.Date(2025, 12, 15, 17, 55, 50, 0, time.UTC))

	require.NoError(t, err)
	assert.Equal(t, []dataset.Event{{
		Time: time.Date(2025, 12, 15, 17, 52, 9, 0, time.UTC), First: time.Date(2025, 12, 15, 17, 50, 9, 0, time.UTC),
		Type: "Warning", Reason: "Failed", ObjectKind: "Pod", ObjectName: "pc-1", Count: 3, Source: "kubelet",
		Message: `Failed to pull image "quay.io/open-telemetry/demo-product-catalog:1.0.0"`,
	}}, events)
}
```

Create `prepare/telemetry_test.go`:

```go
package prepare

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// columns renders a table with the given header and rows, quoting fields as pandas does.
func columns(header []string, rows ...[]string) string {
	lines := []string{strings.Join(header, "\t")}
	for _, r := range rows {
		quoted := make([]string, len(r))
		for i, f := range r {
			if strings.ContainsAny(f, "\"\t\n") {
				f = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
			}
			quoted[i] = f
		}
		lines = append(lines, strings.Join(quoted, "\t"))
	}
	return strings.Join(lines, "\n") + "\n"
}

func resource(ns, pod string) string {
	return `{'k8s.namespace.name': '` + ns + `', 'k8s.pod.name': '` + pod + `', 'service.name': 'x'}`
}

var (
	windowFrom = time.Date(2025, 12, 15, 17, 27, 59, 0, time.UTC)
	windowTo   = time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC)
)

func TestLogsKeepTheNamespaceInTheWindow(t *testing.T) {
	header := []string{"Timestamp", "TraceId", "SeverityText", "SeverityNumber", "ServiceName", "Body", "ResourceAttributes"}
	fsys := files("otel_logs_raw.tsv", columns(header,
		[]string{"2025-12-15 17:50:00.5", "abc", "INFO", "9", "shipping", "failed to get quote:\n invalid uri", resource("otel-demo", "shipping-1")},
		[]string{"2025-12-15 17:50:01.0", "", "", "0", "otel-collector", "noise", resource("opentelemetry-collectors", "c-1")},
		[]string{"2025-12-15 17:58:00.0", "", "INFO", "9", "shipping", "after the alert", resource("otel-demo", "shipping-1")},
		[]string{"2025-12-15 17:20:00.0", "", "INFO", "9", "shipping", "before the window", resource("otel-demo", "shipping-1")},
	))

	logs, err := readLogs(fsys, "otel_logs_raw.tsv", "otel-demo", windowFrom, windowTo)

	require.NoError(t, err)
	assert.Equal(t, []dataset.Log{{
		Time: time.Date(2025, 12, 15, 17, 50, 0, 500000000, time.UTC), Service: "shipping", Pod: "shipping-1",
		Severity: "INFO", Body: "failed to get quote:\n invalid uri", TraceID: "abc",
	}}, logs)
}

func spanRow(at, trace, span, parent, service, name, kind, status, message, durationNS string) []string {
	return []string{at, trace, span, parent, name, kind, service, resource("otel-demo", service+"-1"),
		`{'http.response.status_code': '500', 'server.address': 'shipping'}`, durationNS, status, message,
		`['exception']`, `[{'exception.message': 'boom', 'exception.stacktrace': 'long'}]`}
}

func TestSpansKeepErrorsAndCountEveryCallBetweenServices(t *testing.T) {
	header := []string{"Timestamp", "TraceId", "SpanId", "ParentSpanId", "SpanName", "SpanKind", "ServiceName", "ResourceAttributes",
		"SpanAttributes", "Duration", "StatusCode", "StatusMessage", "Events.Name", "Events.Attributes"}
	fsys := files("otel_traces_raw.tsv", columns(header,
		spanRow("2025-12-15 17:50:00.0", "t1", "c1", "", "checkout", "POST shipping", "Client", "Unset", "", "2000000"),
		spanRow("2025-12-15 17:50:00.1", "t1", "s1", "c1", "shipping", "/get-quote", "Server", "Error", "Internal Server Error", "1500000"),
		spanRow("2025-12-15 17:50:01.0", "t2", "c2", "", "checkout", "POST shipping", "Client", "Unset", "", "2000000"),
		spanRow("2025-12-15 17:50:01.1", "t2", "s2", "c2", "shipping", "/get-quote", "Server", "Unset", "", "1000000"),
		spanRow("2025-12-15 17:59:00.0", "t3", "s3", "", "shipping", "/get-quote", "Server", "Error", "late", "1000000"),
	))

	spans, edges, until, err := readSpans(fsys, "otel_traces_raw.tsv", "otel-demo", windowFrom, windowTo)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 50, 1, 100000000, time.UTC), until)
	require.Len(t, spans, 1)
	assert.Equal(t, dataset.Span{
		Time: time.Date(2025, 12, 15, 17, 50, 0, 100000000, time.UTC), TraceID: "t1", SpanID: "s1", ParentID: "c1",
		Service: "shipping", Name: "/get-quote", Kind: "Server", DurationMS: 1.5, Status: "Error", Message: "Internal Server Error",
		Attributes: map[string]string{"http.response.status_code": "500", "server.address": "shipping", "exception.message": "boom"},
	}, spans[0])
	assert.Equal(t, []dataset.Edge{{From: "checkout", To: "shipping", Calls: 2, Errors: 1}}, edges)
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./prepare`
Expected: FAIL with `undefined: readEvents`.

- [ ] **Step 3: Write the readers**

Create `prepare/events.go`:

```go
package prepare

import (
	"encoding/json"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

// coreEvent is a core/v1 Event as the watch stream records it.
type coreEvent struct {
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		UID string `json:"uid"`
	} `json:"metadata"`
	InvolvedObject struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		Namespace  string `json:"namespace"`
	} `json:"involvedObject"`
	Reason         string     `json:"reason"`
	Message        string     `json:"message"`
	Type           string     `json:"type"`
	Count          int        `json:"count"`
	FirstTimestamp *time.Time `json:"firstTimestamp"`
	LastTimestamp  *time.Time `json:"lastTimestamp"`
	EventTime      *time.Time `json:"eventTime"`
	Source         struct {
		Component string `json:"component"`
	} `json:"source"`
}

func (e *coreEvent) last() time.Time {
	for _, t := range []*time.Time{e.LastTimestamp, e.EventTime, e.FirstTimestamp} {
		if t != nil && !t.IsZero() {
			return t.UTC()
		}
	}
	return time.Time{}
}

// fromCluster reports whether the event is about the namespace's own objects:
// a core/v1 event, not the stream's events.k8s.io copy, and not about chaos
// resources.
func (e *coreEvent) fromCluster(namespace string) bool {
	return e.APIVersion == "v1" && e.InvolvedObject.Namespace == namespace &&
		!strings.Contains(e.InvolvedObject.APIVersion, "chaos-mesh.org") && !strings.Contains(e.Source.Component, "chaos")
}

func (e *coreEvent) convert() dataset.Event {
	var first time.Time
	if e.FirstTimestamp != nil {
		first = e.FirstTimestamp.UTC()
	}
	return dataset.Event{
		Time: e.last(), First: first, Type: e.Type, Reason: e.Reason,
		ObjectKind: e.InvolvedObject.Kind, ObjectName: e.InvolvedObject.Name,
		Message: scrub(e.Message), Count: e.Count, Source: e.Source.Component,
	}
}

// seenEvent is an event's state as the stream recorded it at a time.
type seenEvent struct {
	at    time.Time
	event coreEvent
}

// readEvents reads the namespace's events, each in its last state recorded
// at or before to, keeping those last seen in [from, to].
func readEvents(fsys fs.FS, name, namespace string, from, to time.Time) ([]dataset.Event, error) {
	latest := map[string]*seenEvent{}
	err := readRows(fsys, name, func(r raw.Row) error {
		at, err := raw.ParseTime(r.Get("Timestamp"))
		if err != nil || at.After(to) {
			return err
		}
		var row struct {
			Object coreEvent `json:"object"`
		}
		if err := json.Unmarshal([]byte(r.Get("Body")), &row); err != nil {
			return err
		}
		if !row.Object.fromCluster(namespace) {
			return nil
		}
		if prev := latest[row.Object.Metadata.UID]; prev == nil || at.After(prev.at) {
			latest[row.Object.Metadata.UID] = &seenEvent{at, row.Object}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []dataset.Event
	for _, s := range latest {
		if last := s.event.last(); !last.Before(from) && !last.After(to) {
			out = append(out, s.event.convert())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.Before(out[j].Time)
		}
		return out[i].ObjectName+out[i].Reason < out[j].ObjectName+out[j].Reason
	})
	return out, nil
}
```

Create `prepare/telemetry.go`:

```go
package prepare

import (
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

// readTable calls fn for each row of a raw table whose timestamp is in
// [from, to] and whose resource is in the namespace, with that resource.
func readTable(fsys fs.FS, name, namespace string, from, to time.Time, fn func(raw.Row, time.Time, map[string]any) error) error {
	return readRows(fsys, name, func(r raw.Row) error {
		at, err := raw.ParseTime(r.Get("Timestamp"))
		if err != nil {
			return err
		}
		if at.Before(from) || at.After(to) {
			return nil
		}
		resource, err := pyMap(r.Get("ResourceAttributes"))
		if err != nil {
			return fmt.Errorf("column ResourceAttributes: %w", err)
		}
		if resource["k8s.namespace.name"] != namespace {
			return nil
		}
		return fn(r, at, resource)
	})
}

// readLogs reads the namespace's log records in [from, to].
func readLogs(fsys fs.FS, name, namespace string, from, to time.Time) ([]dataset.Log, error) {
	var out []dataset.Log
	err := readTable(fsys, name, namespace, from, to, func(r raw.Row, at time.Time, resource map[string]any) error {
		out = append(out, dataset.Log{
			Time: at, Service: r.Get("ServiceName"), Pod: str0(resource["k8s.pod.name"]),
			Severity: r.Get("SeverityText"), Body: scrub(r.Get("Body")), TraceID: r.Get("TraceId"),
		})
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, err
}

// readSpans reads the namespace's spans in [from, to]. It keeps the spans
// that ended in error, counts the calls between services from all spans (a
// span whose parent is in another service is a call from that service), and
// returns the time of the last span.
func readSpans(fsys fs.FS, name, namespace string, from, to time.Time) ([]dataset.Span, []dataset.Edge, time.Time, error) {
	type link struct {
		parent, service string
		failed          bool
	}
	serviceOfSpan := map[string]string{}
	var links []link
	var out []dataset.Span
	var last time.Time
	err := readTable(fsys, name, namespace, from, to, func(r raw.Row, at time.Time, _ map[string]any) error {
		if at.After(last) {
			last = at
		}
		service, status := r.Get("ServiceName"), r.Get("StatusCode")
		serviceOfSpan[r.Get("SpanId")] = service
		links = append(links, link{r.Get("ParentSpanId"), service, status == "Error"})
		if status != "Error" {
			return nil
		}
		span, err := errorSpan(r, at)
		if err != nil {
			return err
		}
		out = append(out, span)
		return nil
	})
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	type pair struct{ from, to string }
	counts := map[pair]*dataset.Edge{}
	for i := range links {
		l := &links[i]
		caller, ok := serviceOfSpan[l.parent]
		if !ok || caller == l.service {
			continue
		}
		p := pair{caller, l.service}
		if counts[p] == nil {
			counts[p] = &dataset.Edge{From: caller, To: l.service}
		}
		counts[p].Calls++
		if l.failed {
			counts[p].Errors++
		}
	}
	edges := make([]dataset.Edge, 0, len(counts))
	for _, e := range counts {
		edges = append(edges, *e)
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].From+"\x00"+edges[i].To < edges[j].From+"\x00"+edges[j].To })
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, edges, last, nil
}

func errorSpan(r raw.Row, at time.Time) (dataset.Span, error) {
	attrs, err := pyMap(r.Get("SpanAttributes"))
	if err != nil {
		return dataset.Span{}, fmt.Errorf("column SpanAttributes: %w", err)
	}
	attributes := make(map[string]string, len(attrs)+1)
	for k, v := range attrs {
		attributes[k] = *render(scrubTree(v))
	}
	if events, err := raw.ParsePython(r.Get("Events.Attributes")); err == nil {
		list, _ := events.([]any)
		for _, e := range list {
			m, _ := e.(map[string]any)
			if msg, ok := m["exception.message"].(string); ok {
				attributes["exception.message"] = *render(scrub(msg))
				break
			}
		}
	}
	ns, err := strconv.ParseFloat(r.Get("Duration"), 64)
	if err != nil {
		return dataset.Span{}, fmt.Errorf("column Duration: %w", err)
	}
	return dataset.Span{
		Time: at, TraceID: r.Get("TraceId"), SpanID: r.Get("SpanId"), ParentID: r.Get("ParentSpanId"),
		Service: r.Get("ServiceName"), Name: r.Get("SpanName"), Kind: r.Get("SpanKind"), DurationMS: ns / 1e6,
		Status: r.Get("StatusCode"), Message: *render(scrub(r.Get("StatusMessage"))), Attributes: attributes,
	}, nil
}

// pyMap parses a Python dict literal; an empty string is an empty dict.
func pyMap(s string) (map[string]any, error) {
	if s == "" {
		return map[string]any{}, nil
	}
	v, err := raw.ParsePython(s)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("not a dict: %.40q", s)
	}
	return m, nil
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./prepare`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/prepare`.

- [ ] **Step 5: Lint**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add bench/itbench/prepare/events.go bench/itbench/prepare/telemetry.go bench/itbench/prepare/events_test.go bench/itbench/prepare/telemetry_test.go
git commit -m "feat(bench): read events, logs, error spans and calls between services"
git push
```

---

### Task 6: Metrics

**Files:**
- Create: `bench/itbench/prepare/metrics.go`
- Test: `bench/itbench/prepare/metrics_test.go`

**Interfaces:**
- Consumes: `readRows`, `pyMap`, `str0` (Tasks 4 and 5).
- Produces, in package `prepare`:
  - `readServiceMetrics(fsys fs.FS, name string, from, to time.Time) ([]dataset.ServicePoint, error)`: requests, errors and p95 per operation and minute, from the span metrics' histogram buckets;
  - `readPodMetrics(fsys fs.FS, name string, from, to time.Time) ([]dataset.PodPoint, error)`: CPU and memory use and limits per minute;
  - `increase(before, after float64) float64` and `quantile(q float64, buckets map[float64]float64) float64`.

- [ ] **Step 1: Write the failing tests**

The service tables hold cumulative histogram buckets scraped every 30 s, one series per operation, kind, status and bound. Create `prepare/metrics_test.go`:

```go
package prepare

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

func bucketRow(at, le, status, value string) []string {
	tags := `{'exported_instance': 'i-1', 'le': '` + le + `', 'service_name': 'shipping', 'span_kind': 'SPAN_KIND_SERVER', 'span_name': '/get-quote', 'status_code': '` + status + `'}`
	return []string{"traces_span_metrics_duration_milliseconds_bucket", at, value, "shipping", "otel-demo", le, tags, "duration_p95", ""}
}

func TestServiceMetricsAreIncreasesPerMinuteWithAP95(t *testing.T) {
	header := []string{"metric_name", "timestamp", "value", "service_name", "namespace", "bucket_le", "tags", "metric_type", "status_code"}
	var rows [][]string
	for _, b := range []struct{ le, status, before, after string }{
		{"10.0", "STATUS_CODE_UNSET", "100", "110"}, {"100.0", "STATUS_CODE_UNSET", "100", "130"},
		{"1000.0", "STATUS_CODE_UNSET", "100", "140"}, {"+Inf", "STATUS_CODE_UNSET", "100", "140"},
		{"10.0", "STATUS_CODE_ERROR", "0", "0"}, {"100.0", "STATUS_CODE_ERROR", "0", "4"},
		{"1000.0", "STATUS_CODE_ERROR", "0", "4"}, {"+Inf", "STATUS_CODE_ERROR", "0", "4"},
	} {
		rows = append(rows, bucketRow("2025-12-15 17:50:00.0", b.le, b.status, b.before), bucketRow("2025-12-15 17:51:00.0", b.le, b.status, b.after))
	}
	rows = append(rows, bucketRow("2025-12-15 17:58:30.0", "+Inf", "STATUS_CODE_UNSET", "999"))
	fsys := files("service.tsv", columns(header, rows...))

	points, err := readServiceMetrics(fsys, "service.tsv", windowFrom, windowTo)

	require.NoError(t, err)
	assert.Equal(t, []dataset.ServicePoint{{
		Time: time.Date(2025, 12, 15, 17, 51, 0, 0, time.UTC), Service: "shipping", Operation: "/get-quote", Kind: "server",
		Requests: 44, Errors: 4, P95MS: 802,
	}}, points)
}

func TestACounterResetCountsFromZero(t *testing.T) {
	assert.InDelta(t, 3.0, increase(10, 3), 1e-9)
	assert.InDelta(t, 5.0, increase(10, 15), 1e-9)
}

func podRow(metric, at, container, value string) []string {
	tags := `{'container': '` + container + `', 'namespace': 'otel-demo', 'pod': 'valkey-cart-58df56c79c-4cvft'}`
	if container == "" {
		tags = `{'namespace': 'otel-demo', 'pod': 'valkey-cart-58df56c79c-4cvft'}`
	}
	return []string{metric, at, value, "valkey-cart-58df56c79c-4cvft", "otel-demo", tags}
}

func TestPodMetricsSumContainersAndKeepTheMinutesLastSample(t *testing.T) {
	header := []string{"metric_name", "timestamp", "value", "pod_name", "namespace", "tags"}
	const (
		cpu = "node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate"
		mem = "node_namespace_pod_container:container_memory_working_set_bytes"
		lim = "cluster:namespace:pod_memory:active:kube_pod_container_resource_limits"
	)
	fsys := files("pod.tsv", columns(header,
		podRow(cpu, "2025-12-15 17:50:00.6", "valkey-cart", "0.002"),
		podRow(cpu, "2025-12-15 17:50:30.6", "valkey-cart", "0.004"),
		podRow(cpu, "2025-12-15 17:50:30.6", "", "0.5"),
		podRow(mem, "2025-12-15 17:50:16.2", "valkey-cart", "3000000"),
		podRow(mem, "2025-12-15 17:50:16.2", "POD", "225280"),
		podRow(lim, "2025-12-15 17:50:12.3", "valkey-cart", "10485760"),
		podRow(lim, "2025-12-15 17:50:12.3", "valkey-cart", "10485760"),
	))

	points, err := readPodMetrics(fsys, "pod.tsv", windowFrom, windowTo)

	require.NoError(t, err)
	assert.Equal(t, []dataset.PodPoint{{
		Time: time.Date(2025, 12, 15, 17, 50, 0, 0, time.UTC), Pod: "valkey-cart-58df56c79c-4cvft", Service: "valkey-cart",
		CPUCores: 0.004, MemoryBytes: 3000000, MemoryLimit: 10485760,
	}}, points)
}

func TestMetricsSkipEmptyValues(t *testing.T) {
	fsys := files(
		"pod.tsv", columns([]string{"metric_name", "timestamp", "value", "pod_name", "namespace", "tags"},
			podRow("node_namespace_pod_container:container_memory_working_set_bytes", "2025-12-15 17:50:16.2", "valkey-cart", "")),
		"service.tsv", columns([]string{"metric_name", "timestamp", "value", "service_name", "namespace", "bucket_le", "tags", "metric_type", "status_code"},
			bucketRow("2025-12-15 17:50:00.0", "+Inf", "STATUS_CODE_UNSET", "")),
	)

	pods, err := readPodMetrics(fsys, "pod.tsv", windowFrom, windowTo)
	require.NoError(t, err)
	assert.Empty(t, pods)
	services, err := readServiceMetrics(fsys, "service.tsv", windowFrom, windowTo)
	require.NoError(t, err)
	assert.Empty(t, services)
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./prepare`
Expected: FAIL with `undefined: readServiceMetrics`.

- [ ] **Step 3: Write the readers**

Create `prepare/metrics.go`:

```go
package prepare

import (
	"io/fs"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

const spanBuckets = "traces_span_metrics_duration_milliseconds_bucket"

// Pod resource metrics: recording rules per container.
const (
	podCPU         = "node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate"
	podMemory      = "node_namespace_pod_container:container_memory_working_set_bytes"
	podCPULimit    = "cluster:namespace:pod_cpu:active:kube_pod_container_resource_limits"
	podMemoryLimit = "cluster:namespace:pod_memory:active:kube_pod_container_resource_limits"
)

// podSuffix is the ReplicaSet hash and pod suffix of a Deployment's pod name.
var podSuffix = regexp.MustCompile(`-[a-z0-9]{6,10}-[a-z0-9]{5}$`)

type sample struct {
	at    time.Time
	value float64
}

// eachRow calls fn for each row of a metrics table with its time and tags.
func eachRow(fsys fs.FS, name string, fn func(r raw.Row, at time.Time, tags map[string]any) error) error {
	return readRows(fsys, name, func(r raw.Row) error {
		at, err := raw.ParseTime(r.Get("timestamp"))
		if err != nil {
			return err
		}
		tags, err := pyMap(r.Get("tags"))
		if err != nil {
			return err
		}
		return fn(r, at, tags)
	})
}

// readServiceMetrics turns one service's span metrics into request counts,
// error counts and p95 latency per operation and minute. An increase between
// two scrapes counts in the minute of the later one, if that is in (from, to].
// The p95 is rounded to 0.1 ms.
func readServiceMetrics(fsys fs.FS, name string, from, to time.Time) ([]dataset.ServicePoint, error) {
	type seriesKey struct{ instance, kind, name, status, le string }
	series := map[seriesKey][]sample{}
	service := ""
	err := eachRow(fsys, name, func(r raw.Row, at time.Time, tags map[string]any) error {
		if r.Get("metric_name") != spanBuckets || r.Get("value") == "" {
			return nil
		}
		v, err := strconv.ParseFloat(r.Get("value"), 64)
		if err != nil {
			return err
		}
		service = r.Get("service_name")
		k := seriesKey{str0(tags["exported_instance"]), str0(tags["span_kind"]), str0(tags["span_name"]), str0(tags["status_code"]), r.Get("bucket_le")}
		series[k] = append(series[k], sample{at, v})
		return nil
	})
	if err != nil {
		return nil, err
	}
	type pointKey struct {
		minute     time.Time
		kind, name string
	}
	type counts struct {
		buckets map[float64]float64
		errors  float64
	}
	agg := map[pointKey]*counts{}
	for k, samples := range series {
		le, err := strconv.ParseFloat(k.le, 64)
		if err != nil {
			return nil, err
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i].at.Before(samples[j].at) })
		for i := 1; i < len(samples); i++ {
			at := samples[i].at
			if !at.After(from) || at.After(to) {
				continue
			}
			pk := pointKey{at.Truncate(time.Minute), kindName(k.kind), k.name}
			if agg[pk] == nil {
				agg[pk] = &counts{buckets: map[float64]float64{}}
			}
			inc := increase(samples[i-1].value, samples[i].value)
			agg[pk].buckets[le] += inc
			if math.IsInf(le, 1) && k.status == "STATUS_CODE_ERROR" {
				agg[pk].errors += inc
			}
		}
	}
	out := make([]dataset.ServicePoint, 0, len(agg))
	for k, c := range agg {
		out = append(out, dataset.ServicePoint{
			Time: k.minute, Service: service, Operation: k.name, Kind: k.kind,
			Requests: c.buckets[math.Inf(1)], Errors: c.errors, P95MS: math.Round(quantile(0.95, c.buckets)*10) / 10,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.Before(out[j].Time)
		}
		return out[i].Kind+out[i].Operation < out[j].Kind+out[j].Operation
	})
	return out, nil
}

// increase is a counter's growth between two scrapes; a drop is a reset to zero.
func increase(before, after float64) float64 {
	if after < before {
		return after
	}
	return after - before
}

// quantile estimates a quantile from cumulative histogram buckets, as
// Prometheus' histogram_quantile does.
func quantile(q float64, buckets map[float64]float64) float64 {
	total := buckets[math.Inf(1)]
	if total == 0 {
		return 0
	}
	les := make([]float64, 0, len(buckets))
	for le := range buckets {
		les = append(les, le)
	}
	sort.Float64s(les)
	rank := q * total
	var prevLE, prevCount float64
	for _, le := range les {
		c := buckets[le]
		if c >= rank {
			if math.IsInf(le, 1) {
				return prevLE
			}
			if c == prevCount {
				return le
			}
			return prevLE + (le-prevLE)*(rank-prevCount)/(c-prevCount)
		}
		prevLE, prevCount = le, c
	}
	return prevLE
}

func kindName(k string) string { return strings.ToLower(strings.TrimPrefix(k, "SPAN_KIND_")) }

// podMetrics are the pod tables' metrics that the dataset keeps.
var podMetrics = map[string]bool{podCPU: true, podMemory: true, podCPULimit: true, podMemoryLimit: true}

type podKey struct {
	metric string
	at     time.Time
}

// readPodMetrics turns one pod's table into CPU and memory use and limits per
// minute, summed over its containers, from each minute's last scrape.
func readPodMetrics(fsys fs.FS, name string, from, to time.Time) ([]dataset.PodPoint, error) {
	values := map[podKey]map[string]float64{}
	pod := ""
	err := readRows(fsys, name, func(r raw.Row) error {
		metric, value := r.Get("metric_name"), r.Get("value")
		if !podMetrics[metric] || value == "" {
			return nil
		}
		at, err := raw.ParseTime(r.Get("timestamp"))
		if err != nil || at.Before(from) || at.After(to) {
			return err
		}
		tags, err := pyMap(r.Get("tags"))
		if err != nil {
			return err
		}
		container := str0(tags["container"])
		if container == "" || container == "POD" {
			return nil
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return err
		}
		pod = r.Get("pod_name")
		k := podKey{metric, at}
		if values[k] == nil {
			values[k] = map[string]float64{}
		}
		values[k][container] = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return podPoints(pod, values), nil
}

// podPoints sums each scrape over the containers, and keeps each metric's
// last scrape in each minute.
func podPoints(pod string, values map[podKey]map[string]float64) []dataset.PodPoint {
	last := map[time.Time]map[string]sample{}
	for k, byContainer := range values {
		minute := k.at.Truncate(time.Minute)
		if last[minute] == nil {
			last[minute] = map[string]sample{}
		}
		if prev, ok := last[minute][k.metric]; ok && prev.at.After(k.at) {
			continue
		}
		sum := 0.0
		for _, v := range byContainer {
			sum += v
		}
		last[minute][k.metric] = sample{k.at, sum}
	}
	out := make([]dataset.PodPoint, 0, len(last))
	for minute, m := range last {
		out = append(out, dataset.PodPoint{
			Time: minute, Pod: pod, Service: podSuffix.ReplaceAllString(pod, ""),
			CPUCores: m[podCPU].value, MemoryBytes: m[podMemory].value,
			CPULimit: m[podCPULimit].value, MemoryLimit: m[podMemoryLimit].value,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./prepare`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/prepare`.

- [ ] **Step 5: Lint**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add bench/itbench/prepare/metrics.go bench/itbench/prepare/metrics_test.go
git commit -m "feat(bench): turn span and pod metrics into per-minute points"
git push
```

---

### Task 7: The pipeline and the leak check

**Files:**
- Create: `bench/itbench/prepare/prepare.go`
- Test: `bench/itbench/prepare/prepare_test.go`

**Interfaces:**
- Consumes: every reader of Tasks 3–6.
- Produces, in package `prepare`:
  - `const Window = 30 * time.Minute`;
  - `func Run(rawDir, out, namespace string) (*dataset.Dataset, error)`: opens `rawDir` as an `os.Root`, builds the dataset, refuses one that leaks, and saves it into `out`;
  - `func Leaks(d *dataset.Dataset) []string`;
  - `build(fsys fs.FS, namespace string) (*dataset.Dataset, error)`.

- [ ] **Step 1: Write the failing tests**

`scenario` is a small download with every table, so the pipeline's wiring is tested without the real data. Create `prepare/prepare_test.go`:

```go
package prepare

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// scenario is a small downloaded scenario with every table, whose log body
// is logBody.
func scenario(logBody string) fstest.MapFS {
	injected := helmWrite + `,{"manager":"OpenAPI-Generator","operation":"Update","time":"2025-12-15T17:52:47Z"}`
	return files(
		"alerts/alerts_in_alerting_state_2025-12-15T175759.000000Z.json",
		`{"data": {"alerts": [`+sprintfAlert("shipping", "2025-12-15T17:55:00Z")+`]}, "timestamp": "2025-12-15T17:57:59Z"}`,
		"alerts/alerts_at_2025-12-15T17-56-35.000000.json", `[`+sprintfAlert("shipping", "2025-12-15T17:55:00Z")+`]`,
		"k8s_objects_raw.tsv", table(
			tsvRow{"2025-12-15 17:50:41.1", deployment("quote:8080", helmWrite)},
			tsvRow{"2025-12-15 17:55:41.1", deployment("quote:0000", injected)},
		),
		"k8s_events_raw.tsv", table(
			tsvRow{"2025-12-15 17:53:00.1", eventRow("u1", "otel-demo", "Pod", "shipping-1", "BackOff", "back-off", "2025-12-15T17:53:00Z", "2025-12-15T17:53:00Z", 1)},
		),
		"otel_logs_raw.tsv", columns([]string{"Timestamp", "TraceId", "SeverityText", "SeverityNumber", "ServiceName", "Body", "ResourceAttributes"},
			[]string{"2025-12-15 17:50:00.5", "", "INFO", "9", "shipping", logBody, resource("otel-demo", "shipping-1")},
		),
		"otel_traces_raw.tsv", columns([]string{"Timestamp", "TraceId", "SpanId", "ParentSpanId", "SpanName", "SpanKind", "ServiceName",
			"ResourceAttributes", "SpanAttributes", "Duration", "StatusCode", "StatusMessage", "Events.Name", "Events.Attributes"},
			spanRow("2025-12-15 17:50:00.0", "t1", "c1", "", "checkout", "POST shipping", "Client", "Unset", "", "2000000"),
			spanRow("2025-12-15 17:50:00.1", "t1", "s1", "c1", "shipping", "/get-quote", "Server", "Error", "Internal Server Error", "1500000"),
		),
		"metrics/service_shipping_raw.tsv", columns([]string{"metric_name", "timestamp", "value", "service_name", "namespace", "bucket_le", "tags", "metric_type", "status_code"},
			bucketRow("2025-12-15 17:50:00.0", "+Inf", "STATUS_CODE_UNSET", "10"),
			bucketRow("2025-12-15 17:51:00.0", "+Inf", "STATUS_CODE_UNSET", "20"),
		),
		"metrics/pod_shipping-75d58f5d84-gw77q_raw.tsv", columns([]string{"metric_name", "timestamp", "value", "pod_name", "namespace", "tags"},
			podRow("node_namespace_pod_container:container_memory_working_set_bytes", "2025-12-15 17:50:16.2", "valkey-cart", "3000000"),
		),
	)
}

func sprintfAlert(service, activeAt string) string {
	return `{"labels": {"alertname": "RequestErrorRate", "namespace": "otel-demo", "service_name": "` + service + `"},
 "annotations": {}, "state": "firing", "activeAt": "` + activeAt + `"}`
}

func TestBuildPreparesEveryStream(t *testing.T) {
	d, err := build(scenario("failed to get quote"), "otel-demo")

	require.NoError(t, err)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC), d.Meta.AlertTime)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 27, 59, 0, time.UTC), d.Meta.WindowStart)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 50, 0, 100000000, time.UTC), d.Meta.TracesUntil)
	assert.Len(t, d.Meta.Trigger, 1)
	assert.Len(t, d.Alerts, 1)
	require.Len(t, d.Changes, 1)
	assert.Equal(t, time.Date(2025, 12, 15, 17, 52, 47, 0, time.UTC), d.Changes[0].Time)
	assert.Len(t, d.Objects, 1)
	assert.Len(t, d.Events, 1)
	assert.Len(t, d.Logs, 1)
	assert.Len(t, d.Spans, 1)
	assert.Equal(t, []dataset.Edge{{From: "checkout", To: "shipping", Calls: 1, Errors: 1}}, d.Edges)
	assert.Len(t, d.Services, 1)
	assert.Len(t, d.Pods, 1)
}

func TestRunWritesTheDataset(t *testing.T) {
	raw, out := t.TempDir(), filepath.Join(t.TempDir(), "S16")
	require.NoError(t, os.CopyFS(raw, scenario("failed to get quote")))

	d, err := Run(raw, out, "otel-demo")

	require.NoError(t, err)
	loaded, err := dataset.Load(out)
	require.NoError(t, err)
	assert.Equal(t, d.Changes, loaded.Changes)
}

func TestRunWritesNothingWhenTheDatasetWouldLeak(t *testing.T) {
	raw, out := t.TempDir(), filepath.Join(t.TempDir(), "S16")
	require.NoError(t, os.CopyFS(raw, scenario("StressChaos applied to valkey-cart")))

	_, err := Run(raw, out, "otel-demo")

	require.ErrorContains(t, err, `logs: "chaos"`)
	assert.NoDirExists(t, out)
}

func TestLeaksFindsForbiddenStringsInAnyStream(t *testing.T) {
	d := &dataset.Dataset{
		Logs:   []dataset.Log{{Time: time.Unix(0, 0), Service: "cart", Body: "pulled quay.io/IT-Bench/collector"}},
		Events: []dataset.Event{{Reason: "Applied", Message: "StressChaos applied"}},
		Spans:  []dataset.Span{{Attributes: map[string]string{"k": "fine"}}},
	}

	leaks := Leaks(d)

	assert.Len(t, leaks, 2)
	assert.Contains(t, leaks[0], `events: "chaos"`)
	assert.Contains(t, leaks[1], `logs: "it-bench"`)
	assert.Empty(t, Leaks(&dataset.Dataset{Logs: []dataset.Log{{Body: "InvalidImageName"}}}))
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./prepare`
Expected: FAIL with `undefined: build`.

- [ ] **Step 3: Write the pipeline**

The forbidden list names the benchmark, its injector, its namespaces and its fault names. `Leaks` searches every stream, case-insensitively. Create `prepare/prepare.go`:

```go
package prepare

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// Window is how far before the alert the dataset reaches.
const Window = 30 * time.Minute

// forbidden are strings that would tell an agent the data comes from a
// benchmark, or how its fault was injected. A dataset holding one is not
// written.
var forbidden = []string{
	"it-bench", "itbench", "hello-bench", "chaos", "openapi-generator",
	"agent-access", "data-recorders", "invalid-node", "invalid-command",
}

// Run prepares the scenario downloaded in rawDir into out. It reads only
// inside rawDir, and writes nothing if the dataset would leak.
func Run(rawDir, out, namespace string) (_ *dataset.Dataset, err error) {
	root, err := os.OpenRoot(rawDir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	d, err := build(root.FS(), namespace)
	if err != nil {
		return nil, err
	}
	if leaks := Leaks(d); len(leaks) > 0 {
		return nil, fmt.Errorf("the dataset would leak the benchmark: %s", strings.Join(leaks, "; "))
	}
	if err := os.MkdirAll(out, 0o750); err != nil { //nolint:gosec // G703: the operator names the output directory.
		return nil, err
	}
	if err := d.Save(out); err != nil {
		return nil, err
	}
	return d, nil
}

// build reads one downloaded scenario into a dataset.
func build(fsys fs.FS, namespace string) (*dataset.Dataset, error) {
	at, trigger, err := readTrigger(fsys)
	if err != nil {
		return nil, err
	}
	from := at.Add(-Window)
	d := &dataset.Dataset{Meta: dataset.Meta{Namespace: namespace, AlertTime: at, WindowStart: from, Trigger: trigger}}
	if d.Alerts, err = readAlertPolls(fsys, namespace, from, at); err != nil {
		return nil, err
	}
	snaps, err := readSnapshots(fsys, "k8s_objects_raw.tsv", namespace, at)
	if err != nil {
		return nil, err
	}
	d.Objects, d.Changes = buildObjects(snaps), buildChanges(snaps, from, at)
	if d.Events, err = readEvents(fsys, "k8s_events_raw.tsv", namespace, from, at); err != nil {
		return nil, err
	}
	if d.Logs, err = readLogs(fsys, "otel_logs_raw.tsv", namespace, from, at); err != nil {
		return nil, err
	}
	if d.Spans, d.Edges, d.Meta.TracesUntil, err = readSpans(fsys, "otel_traces_raw.tsv", namespace, from, at); err != nil {
		return nil, err
	}
	if err := readMetrics(fsys, d, from, at); err != nil {
		return nil, err
	}
	return d, nil
}

func readMetrics(fsys fs.FS, d *dataset.Dataset, from, to time.Time) error {
	services, err := fs.Glob(fsys, "metrics/service_*_raw.tsv")
	if err != nil {
		return err
	}
	for _, name := range services {
		points, err := readServiceMetrics(fsys, name, from, to)
		if err != nil {
			return err
		}
		d.Services = append(d.Services, points...)
	}
	pods, err := fs.Glob(fsys, "metrics/pod_*_raw.tsv")
	if err != nil {
		return err
	}
	for _, name := range pods {
		points, err := readPodMetrics(fsys, name, from, to)
		if err != nil {
			return err
		}
		d.Pods = append(d.Pods, points...)
	}
	sort.SliceStable(d.Services, func(i, j int) bool { return d.Services[i].Time.Before(d.Services[j].Time) })
	sort.SliceStable(d.Pods, func(i, j int) bool { return d.Pods[i].Time.Before(d.Pods[j].Time) })
	return nil
}

// Leaks lists each forbidden string found in the dataset, with the stream
// that holds it and some context, case-insensitively.
func Leaks(d *dataset.Dataset) []string {
	streams := []struct {
		name string
		v    any
	}{
		{"meta", d.Meta}, {"alerts", d.Alerts}, {"changes", d.Changes}, {"objects", d.Objects}, {"events", d.Events},
		{"logs", d.Logs}, {"spans", d.Spans}, {"edges", d.Edges}, {"service_metrics", d.Services}, {"pod_metrics", d.Pods},
	}
	var out []string
	for _, s := range streams {
		text := strings.ToLower(string(marshal(s.v)))
		for _, word := range forbidden {
			if i := strings.Index(text, word); i >= 0 {
				out = append(out, fmt.Sprintf("%s: %q in %q", s.name, word, text[max(0, i-60):min(len(text), i+len(word)+60)]))
			}
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./prepare`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/prepare`.

- [ ] **Step 5: Lint**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add bench/itbench/prepare/prepare.go bench/itbench/prepare/prepare_test.go
git commit -m "feat(bench): prepare a scenario and refuse a dataset that leaks"
git push
```

---

### Task 8: The scenario set and scoring

**Files:**
- Create: `bench/itbench/scenarios.yaml`, `bench/itbench/score/score.go`
- Test: `bench/itbench/score/score_test.go`
- Modify: `bench/itbench/go.mod`, `bench/itbench/go.sum`

**Interfaces:**
- Consumes: nothing.
- Produces, in package `score`:
  - `const Other = "other"`; `type Key struct { Service, FailureMode string }` with `(*Key).String()`, which is `none` for nil;
  - `type Scenario struct { ID, Dir string; Change bool; Keys []Key; Why string }` with `(*Scenario).Hit(k *Key) bool`;
  - `type Set struct { Benchmark, Source, Revision, Prefix, Namespace, FlagStore string; FailureModes []string; Scenarios []Scenario; Excluded []Excluded }` and `func Load(path string) (*Set, error)`;
  - `type Answers map[string]Key`; `type Row`; `type Report`; `func Compare(set *Set, answers, baseline Answers) Report`.

- [ ] **Step 1: Add the YAML library**

Run: `go get go.yaml.in/yaml/v3@v3.0.5`
Expected: `go.mod` requires `go.yaml.in/yaml/v3 v3.0.5`, the YAML organization's maintained module.

- [ ] **Step 2: Write the scenario set and the failing tests**

This file is the check's answer key. Each scenario's keys come from its ground truth and from what Task 13's data showed, as the decisions table says. Create `scenarios.yaml`:

```yaml
# The mechanism check's scenarios and their answers (spec Section 14.1).
#
# Fixed on 2026-10-13, before any agent run. A conclusion is a hit when it
# equals one of a scenario's keys. Keys follow the example deployment profile:
# the service where the cause lies, and a failure mode from its closed list.
# A scenario lists several keys where the fault supports several framings
# (spec Section 13.4). The read-only tools never read this file.
#
# change: true marks a fault that is a visible change, where the "most recent
# change" baseline applies.
benchmark: ITBench-Lite SRE v0.2
source: https://huggingface.co/datasets/ibm-research/ITBench-Lite
revision: d0916b08ba421ce5e672e9ad68aa947d938dfef0
prefix: snapshots/sre/v0.2-B96DF826-4BB2-4B62-97AB-6D84254C53D7
namespace: otel-demo
# The baseline counts a change to this ConfigMap as a flag change.
flagStore: flagd-config
failureModes:
  - bad-release
  - flag-change
  - config-change
  - dependency-failure
  - resource-exhaustion
  - traffic-surge
  - infrastructure-failure
  - external-provider
  - other
scenarios:
  - id: S4
    dir: Scenario-4
    change: true
    keys:
      - {service: product-catalog, failureMode: flag-change}
      - {service: flagd, failureMode: flag-change}
    why: Flag productCatalogFailure turned on in flagd-config; product-catalog was restarted 2 s later.
  - id: S5
    dir: Scenario-5
    change: true
    keys:
      - {service: ad, failureMode: flag-change}
      - {service: flagd, failureMode: flag-change}
    why: Flag adFailure turned on in flagd-config; ad was restarted 1 s later.
  - id: S16
    dir: Scenario-16
    change: true
    keys:
      - {service: shipping, failureMode: config-change}
      - {service: shipping, failureMode: bad-release}
    why: The shipping Deployment's QUOTE_ADDR was changed to a bad port.
  - id: S20
    dir: Scenario-20
    change: true
    keys:
      - {service: product-catalog, failureMode: bad-release}
    why: The product-catalog Deployment's image was changed to one that cannot be pulled.
  - id: S21
    dir: Scenario-21
    change: false
    keys:
      - {service: valkey-cart, failureMode: resource-exhaustion}
    why: Memory stress on the valkey-cart pod (a chaos experiment, not a change).
  - id: S23
    dir: Scenario-23
    change: true
    keys:
      - {service: checkout, failureMode: bad-release}
    why: The checkout Deployment's image was changed to a name that cannot be parsed.
  - id: S24
    dir: Scenario-24
    change: true
    keys:
      - {service: checkout, failureMode: config-change}
      - {service: checkout, failureMode: bad-release}
    why: The checkout Deployment's KAFKA_ADDR was changed to a bad port.
  - id: S33
    dir: Scenario-33
    change: true
    keys:
      - {service: ad, failureMode: config-change}
      - {service: ad, failureMode: bad-release}
      - {service: cart, failureMode: config-change}
      - {service: cart, failureMode: bad-release}
    why: >-
      The ad and cart Deployments got a node selector that matches no node, 8 s apart.
      The ground truth names only ad; the data shows both, so either counts.
  - id: S34
    dir: Scenario-34
    change: false
    keys:
      - {service: valkey-cart, failureMode: config-change}
      - {service: valkey-cart, failureMode: dependency-failure}
    why: >-
      valkey-cart started requiring a password that cart does not send. It was set
      inside the running server, so no object changed.
  - id: S80
    dir: Scenario-80
    change: false
    keys:
      - {service: checkout, failureMode: infrastructure-failure}
      - {service: kafka, failureMode: infrastructure-failure}
    why: A network partition between checkout and kafka (a chaos experiment).
  - id: S81
    dir: Scenario-81
    change: false
    keys:
      - {service: shipping, failureMode: infrastructure-failure}
      - {service: quote, failureMode: infrastructure-failure}
    why: A network partition between shipping and quote (a chaos experiment).
  - id: S102
    dir: Scenario-102
    change: true
    keys:
      - {service: ad, failureMode: config-change}
      - {service: ad, failureMode: resource-exhaustion}
    why: >-
      A namespace memory quota was created, and the ad pod could not be recreated under it.
      The quota belongs to no service, so the key names the one service it broke.
  - id: S105
    dir: Scenario-105
    change: true
    keys:
      - {service: product-catalog, failureMode: bad-release}
      - {service: product-catalog, failureMode: config-change}
    why: The product-catalog Deployment's command was changed to one that is not in the image.
excluded:
  - {id: S12, why: Its ground truth contradicts itself.}
  - {id: S31, why: "Its NetworkPolicy allows all ingress, so the fault may have no effect."}
```

Create `score/score_test.go`:

```go
package score_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/score"
)

const setYAML = `benchmark: ITBench-Lite SRE v0.2
source: https://huggingface.co/datasets/ibm-research/ITBench-Lite
revision: d0916b08ba421ce5e672e9ad68aa947d938dfef0
prefix: snapshots/sre/v0.2-B96DF826-4BB2-4B62-97AB-6D84254C53D7
namespace: otel-demo
flagStore: flagd-config
failureModes: [bad-release, flag-change, config-change, other]
scenarios:
  - id: S16
    dir: Scenario-16
    change: true
    keys:
      - {service: shipping, failureMode: config-change}
      - {service: shipping, failureMode: bad-release}
    why: QUOTE_ADDR set to a bad port.
  - id: S21
    dir: Scenario-21
    change: false
    keys:
      - {service: valkey-cart, failureMode: bad-release}
    why: test.
excluded:
  - {id: S12, why: its ground truth contradicts itself}
`

func writeSet(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenarios.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadReadsTheSet(t *testing.T) {
	set, err := score.Load(writeSet(t, setYAML))

	require.NoError(t, err)
	assert.Equal(t, "otel-demo", set.Namespace)
	assert.Equal(t, "flagd-config", set.FlagStore)
	require.Len(t, set.Scenarios, 2)
	assert.Equal(t, "Scenario-16", set.Scenarios[0].Dir)
	assert.True(t, set.Scenarios[0].Change)
	assert.Equal(t, score.Key{Service: "shipping", FailureMode: "bad-release"}, set.Scenarios[0].Keys[1])
	assert.Equal(t, "S12", set.Excluded[0].ID)
}

func TestLoadRejectsBadSets(t *testing.T) {
	for name, bad := range map[string]string{
		"unknown field":        setYAML + "extra: 1\n",
		"mode outside the set": strings.Replace(setYAML, "failureMode: bad-release}\n    why: test.", "failureMode: traffic-surge}\n    why: test.", 1),
		"other as an answer":   strings.Replace(setYAML, "failureMode: bad-release}\n    why: test.", "failureMode: other}\n    why: test.", 1),
		"no keys":              strings.Replace(setYAML, "    keys:\n      - {service: valkey-cart, failureMode: bad-release}\n", "    keys: []\n", 1),
		"repeated id":          strings.Replace(setYAML, "id: S21", "id: S16", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := score.Load(writeSet(t, bad))
			assert.Error(t, err)
		})
	}
}

func TestHitNeedsAnAcceptedKeyAndNeverOther(t *testing.T) {
	set, err := score.Load(writeSet(t, setYAML))
	require.NoError(t, err)
	s16 := set.Scenarios[0]

	assert.True(t, s16.Hit(&score.Key{Service: "shipping", FailureMode: "bad-release"}))
	assert.False(t, s16.Hit(&score.Key{Service: "quote", FailureMode: "config-change"}))
	assert.False(t, s16.Hit(nil), "no conclusion is not a hit")
}

func TestCompareCountsHitsAndDiscordance(t *testing.T) {
	set, err := score.Load(writeSet(t, setYAML))
	require.NoError(t, err)
	agents := score.Answers{"S16": {Service: "quote", FailureMode: "config-change"}, "S21": {Service: "valkey-cart", FailureMode: "bad-release"}}
	baseline := score.Answers{"S16": {Service: "shipping", FailureMode: "config-change"}}

	r := score.Compare(set, agents, baseline)

	assert.Equal(t, 1, r.Hits)
	assert.Equal(t, 1, r.BaselineHits)
	assert.Equal(t, 1, r.OnlyAnswers)
	assert.Equal(t, 1, r.OnlyBaseline)
	assert.Equal(t, 0, r.ChangeHits)
	assert.Equal(t, 1, r.ChangeBaselineHits)
	require.Len(t, r.Rows, 2)
	assert.Equal(t, score.Row{ID: "S16", Change: true, Answer: "quote/config-change", Hit: false, Baseline: "shipping/config-change", BaselineHit: true}, r.Rows[0])
	assert.Equal(t, score.Row{ID: "S21", Answer: "valkey-cart/bad-release", Hit: true, Baseline: "none"}, r.Rows[1])
}

func TestTheFrozenSetIsValid(t *testing.T) {
	set, err := score.Load("../scenarios.yaml")

	require.NoError(t, err)
	assert.Equal(t, "d0916b08ba421ce5e672e9ad68aa947d938dfef0", set.Revision)
	assert.Equal(t, "flagd-config", set.FlagStore)
	var ids, changes []string
	for _, sc := range set.Scenarios {
		ids = append(ids, sc.ID)
		if sc.Change {
			changes = append(changes, sc.ID)
		}
	}
	assert.Equal(t, []string{"S4", "S5", "S16", "S20", "S21", "S23", "S24", "S33", "S34", "S80", "S81", "S102", "S105"}, ids)
	assert.Equal(t, []string{"S4", "S5", "S16", "S20", "S23", "S24", "S33", "S102", "S105"}, changes)
	assert.Len(t, set.Excluded, 2)
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./score`
Expected: FAIL with `no non-test Go files in .../bench/itbench/score`.

- [ ] **Step 4: Write the scorer**

Create `score/score.go`:

```go
// Package score holds the mechanism check's frozen answers and scores
// conclusions against them by key.
package score

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"go.yaml.in/yaml/v3"
)

// Other is the value every key field accepts. It is never a hit: the spec
// leaves keys containing it to raters (Section 13.4).
const Other = "other"

// Key is a hypothesis key of the example profile: the service where the
// cause lies, and the kind of failure.
type Key struct {
	Service     string `yaml:"service" json:"service"`
	FailureMode string `yaml:"failureMode" json:"failureMode"`
}

// String renders a key, or "none" for no conclusion.
func (k *Key) String() string {
	if k == nil {
		return "none"
	}
	return k.Service + "/" + k.FailureMode
}

// Scenario is one benchmark scenario and the keys that count as a hit.
type Scenario struct {
	ID     string `yaml:"id"`
	Dir    string `yaml:"dir"`
	Change bool   `yaml:"change"`
	Keys   []Key  `yaml:"keys"`
	Why    string `yaml:"why"`
}

// Excluded is a scenario left out, with the reason.
type Excluded struct {
	ID  string `yaml:"id"`
	Why string `yaml:"why"`
}

// Set is the frozen scenario set: where the data comes from, the closed list
// of failure modes, the flag store the baseline recognizes, and each
// scenario's answer.
type Set struct {
	Benchmark    string     `yaml:"benchmark"`
	Source       string     `yaml:"source"`
	Revision     string     `yaml:"revision"`
	Prefix       string     `yaml:"prefix"`
	Namespace    string     `yaml:"namespace"`
	FlagStore    string     `yaml:"flagStore"`
	FailureModes []string   `yaml:"failureModes"`
	Scenarios    []Scenario `yaml:"scenarios"`
	Excluded     []Excluded `yaml:"excluded"`
}

// Load reads and checks a scenario set.
func Load(path string) (_ *Set, err error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var set Set
	if err := dec.Decode(&set); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &set, set.check()
}

func (s *Set) check() error {
	seen := map[string]bool{}
	for _, sc := range s.Scenarios {
		if seen[sc.ID] {
			return fmt.Errorf("scenario %s appears twice", sc.ID)
		}
		seen[sc.ID] = true
		if len(sc.Keys) == 0 {
			return fmt.Errorf("scenario %s has no keys", sc.ID)
		}
		for _, k := range sc.Keys {
			if k.Service == "" || k.Service == Other || k.FailureMode == Other || !slices.Contains(s.FailureModes, k.FailureMode) {
				return fmt.Errorf("scenario %s: key %s is not a closed-list key", sc.ID, k.String())
			}
		}
	}
	return nil
}

// Hit reports whether a conclusion equals one of the scenario's keys. No
// conclusion is never a hit.
func (s *Scenario) Hit(k *Key) bool {
	return k != nil && slices.Contains(s.Keys, *k)
}

// Answers maps a scenario id to one method's conclusion. A scenario that is
// not in the map has no conclusion.
type Answers map[string]Key

func (a Answers) get(id string) *Key {
	if k, ok := a[id]; ok {
		return &k
	}
	return nil
}

// Row is one scenario's result.
type Row struct {
	ID          string
	Change      bool
	Answer      string
	Hit         bool
	Baseline    string
	BaselineHit bool
}

// Report compares a method's answers with the baseline's, overall and on the
// scenarios whose fault is a change.
type Report struct {
	Rows                           []Row
	Scenarios, ChangeScenarios     int
	Hits, BaselineHits             int
	ChangeHits, ChangeBaselineHits int
	OnlyAnswers, OnlyBaseline      int
}

// Compare scores answers and the baseline on every scenario of the set.
func Compare(set *Set, answers, baseline Answers) Report {
	var r Report
	for i := range set.Scenarios {
		sc := &set.Scenarios[i]
		a, b := answers.get(sc.ID), baseline.get(sc.ID)
		row := Row{ID: sc.ID, Change: sc.Change, Answer: a.String(), Hit: sc.Hit(a), Baseline: b.String(), BaselineHit: sc.Hit(b)}
		r.Rows = append(r.Rows, row)
		r.Scenarios++
		r.Hits += btoi(row.Hit)
		r.BaselineHits += btoi(row.BaselineHit)
		r.OnlyAnswers += btoi(row.Hit && !row.BaselineHit)
		r.OnlyBaseline += btoi(row.BaselineHit && !row.Hit)
		if sc.Change {
			r.ChangeScenarios++
			r.ChangeHits += btoi(row.Hit)
			r.ChangeBaselineHits += btoi(row.BaselineHit)
		}
	}
	return r
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./score`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/score`.

- [ ] **Step 6: Lint**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 7: Commit**

```bash
git add bench/itbench/go.mod bench/itbench/go.sum bench/itbench/scenarios.yaml bench/itbench/score/score.go bench/itbench/score/score_test.go
git commit -m "feat(bench): freeze the scenario set and score answers by key"
git push
```

---

### Task 9: The baseline

**Files:**
- Create: `bench/itbench/baseline/baseline.go`
- Test: `bench/itbench/baseline/baseline_test.go`

**Interfaces:**
- Consumes: `dataset.Dataset`, `dataset.Change`, `dataset.Meta` (Task 2); `score.Key`, `score.Other` (Task 8).
- Produces, in package `baseline`:
  - `func Evaluate(d *dataset.Dataset, flagStore string) (service, namespace *score.Key)`;
  - `func KeyOf(c *dataset.Change, flagStore string) score.Key`;
  - `func AlertingServices(m *dataset.Meta) []string`.

- [ ] **Step 1: Write the failing tests**

Create `baseline/baseline_test.go`:

```go
package baseline_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/bench/itbench/baseline"
	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/score"
)

func str(s string) *string { return &s }

func at(minute int) time.Time { return time.Date(2025, 12, 15, 17, minute, 0, 0, time.UTC) }

var (
	flagChange = dataset.Change{Time: at(48), Kind: "ConfigMap", Name: "flagd-config", Service: "flagd", Action: "updated",
		Fields: []dataset.Field{{Path: `data["demo.flagd.json"].flags.adFailure.defaultVariant`, From: str("off"), To: str("on")}}}
	restart = dataset.Change{Time: at(49), Kind: "Deployment", Name: "ad", Service: "ad", Action: "updated",
		Fields: []dataset.Field{{Path: `spec.template.metadata.annotations["kubectl.kubernetes.io/restartedAt"]`, To: str("2025-12-15T17:49:00Z")}}}
	envChange = dataset.Change{Time: at(50), Kind: "Deployment", Name: "shipping", Service: "shipping", Action: "updated",
		Fields: []dataset.Field{{Path: "spec.template.spec.containers[shipping].env[QUOTE_ADDR].value", From: str("quote:8080"), To: str("quote:0000")}}}
	imageChange = dataset.Change{Time: at(51), Kind: "Deployment", Name: "checkout", Service: "checkout", Action: "updated",
		Fields: []dataset.Field{{Path: "spec.template.spec.containers[checkout].image", From: str("a:1"), To: str("a:2")}}}
	quota = dataset.Change{Time: at(52), Kind: "ResourceQuota", Name: "otel-demo-memory", Action: "created"}
)

func TestEvaluateTakesTheLatestChangeInEachScope(t *testing.T) {
	d := &dataset.Dataset{
		Meta:    dataset.Meta{Trigger: []dataset.Alert{{Service: "shipping"}, {Service: "frontend"}}},
		Changes: []dataset.Change{flagChange, restart, envChange, imageChange},
	}

	service, namespace := baseline.Evaluate(d, "flagd-config")

	assert.Equal(t, &score.Key{Service: "shipping", FailureMode: "config-change"}, service)
	assert.Equal(t, &score.Key{Service: "checkout", FailureMode: "bad-release"}, namespace)
}

func TestARestartIsNotAChange(t *testing.T) {
	d := &dataset.Dataset{Meta: dataset.Meta{Trigger: []dataset.Alert{{Service: "ad"}}}, Changes: []dataset.Change{flagChange, restart}}

	service, namespace := baseline.Evaluate(d, "flagd-config")

	assert.Nil(t, service)
	assert.Equal(t, &score.Key{Service: "flagd", FailureMode: "flag-change"}, namespace)
}

func TestNoAlertingServiceMeansNoServiceAnswer(t *testing.T) {
	d := &dataset.Dataset{Meta: dataset.Meta{Trigger: []dataset.Alert{{Name: "PendingPodsDetected"}}}, Changes: []dataset.Change{envChange}}

	service, namespace := baseline.Evaluate(d, "flagd-config")

	assert.Nil(t, service)
	assert.NotNil(t, namespace)
}

func TestKeyOfMapsTheKindOfChange(t *testing.T) {
	assert.Equal(t, score.Key{Service: "flagd", FailureMode: "flag-change"}, baseline.KeyOf(&flagChange, "flagd-config"))
	assert.Equal(t, score.Key{Service: "shipping", FailureMode: "config-change"}, baseline.KeyOf(&envChange, "flagd-config"))
	assert.Equal(t, score.Key{Service: "checkout", FailureMode: "bad-release"}, baseline.KeyOf(&imageChange, "flagd-config"))
	assert.Equal(t, score.Key{Service: "other", FailureMode: "config-change"}, baseline.KeyOf(&quota, "flagd-config"))
}

func TestAlertingServicesAreTheTriggersServices(t *testing.T) {
	meta := dataset.Meta{Trigger: []dataset.Alert{{Service: "frontend"}, {Service: ""}, {Service: "checkout"}, {Service: "frontend"}}}

	assert.Equal(t, []string{"checkout", "frontend"}, baseline.AlertingServices(&meta))
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./baseline`
Expected: FAIL with `no non-test Go files in .../bench/itbench/baseline`.

- [ ] **Step 3: Write the baseline**

Create `baseline/baseline.go`:

```go
// Package baseline is the mechanism check's deterministic baseline: the most
// recent change before the alert, mapped to a key. It is the benchmark's
// version of the example profile's "last release or flag change on the
// service".
package baseline

import (
	"slices"
	"sort"
	"strings"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/score"
)

const restartedAt = `annotations["kubectl.kubernetes.io/restartedAt"]`

// Evaluate returns the baseline's answer in two scopes: the latest change to
// a service the trigger's alerts name, and the latest change in the
// namespace. Either is nil when no change is in its scope.
func Evaluate(d *dataset.Dataset, flagStore string) (service, namespace *score.Key) {
	alerting := AlertingServices(&d.Meta)
	if c := latest(d.Changes, func(c *dataset.Change) bool { return slices.Contains(alerting, c.Service) }); c != nil {
		k := KeyOf(c, flagStore)
		service = &k
	}
	if c := latest(d.Changes, func(*dataset.Change) bool { return true }); c != nil {
		k := KeyOf(c, flagStore)
		namespace = &k
	}
	return service, namespace
}

// latest returns the latest change that keep accepts, or nil. A restart is
// not a change: it sets nothing but the restart annotation.
func latest(changes []dataset.Change, keep func(*dataset.Change) bool) *dataset.Change {
	var best *dataset.Change
	for i := range changes {
		c := &changes[i]
		if isRestart(c) || !keep(c) {
			continue
		}
		if best == nil || !c.Time.Before(best.Time) {
			best = c
		}
	}
	return best
}

func isRestart(c *dataset.Change) bool {
	if len(c.Fields) == 0 {
		return false
	}
	for _, f := range c.Fields {
		if !strings.HasSuffix(f.Path, restartedAt) {
			return false
		}
	}
	return true
}

// KeyOf maps a change to a key. A change to the flag store is a flag change, a
// change to a container image is a release, and any other change is a
// configuration change. A change with no service names the service "other".
func KeyOf(c *dataset.Change, flagStore string) score.Key {
	service := c.Service
	if service == "" {
		service = score.Other
	}
	mode := "config-change"
	switch {
	case c.Kind == "ConfigMap" && c.Name == flagStore:
		mode = "flag-change"
	case slices.ContainsFunc(c.Fields, func(f dataset.Field) bool { return strings.HasSuffix(f.Path, ".image") }):
		mode = "bad-release"
	}
	return score.Key{Service: service, FailureMode: mode}
}

// AlertingServices are the services the trigger's alerts name, sorted.
func AlertingServices(m *dataset.Meta) []string {
	var out []string
	for i := range m.Trigger {
		if s := m.Trigger[i].Service; s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./baseline`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/baseline`.

- [ ] **Step 5: Lint**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add bench/itbench/baseline/baseline.go bench/itbench/baseline/baseline_test.go
git commit -m "feat(bench): add the most-recent-change baseline in two scopes"
git push
```

---

### Task 10: The read-only MCP tools

**Files:**
- Create: `bench/itbench/server/tools.go`, `bench/itbench/server/mcp.go`
- Test: `bench/itbench/server/tools_test.go`, `bench/itbench/server/mcp_test.go`
- Modify: `bench/itbench/go.mod`, `bench/itbench/go.sum`

**Interfaces:**
- Consumes: `dataset.Dataset` and its records (Task 2).
- Produces, in package `server`:
  - `func NewTools(d *dataset.Dataset) *Tools`, with one method per tool: `ListAlerts`, `ListServices`, `GetChanges`, `GetEvents`, `QueryLogs`, `GetErrorSpans`, `GetServiceMetrics`, `GetPodMetrics`, `ListObjects` and `GetObject`, each taking a pointer to its input type;
  - `type Window struct { Since, Until string }`, embedded in the inputs that take a time range;
  - `func New(d *dataset.Dataset) *mcp.Server` and `func Handler(d *dataset.Dataset) http.Handler`, serving MCP at `/mcp` and a health check at `/healthz`.

- [ ] **Step 1: Add the MCP SDK**

Run: `go get github.com/modelcontextprotocol/go-sdk/mcp@v1.8.0`
Expected: `go.mod` requires `github.com/modelcontextprotocol/go-sdk v1.8.0`. Getting the package, not the module, records the go.sum entries of its dependencies.

- [ ] **Step 2: Write the failing tests**

Create `server/tools_test.go`:

```go
package server_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/server"
)

func at(minute, second int) time.Time {
	return time.Date(2025, 12, 15, 17, minute, second, 0, time.UTC)
}

func str(s string) *string { return &s }

func fixture() *dataset.Dataset {
	shippingAlert := dataset.Alert{Name: "RequestErrorRate", Service: "shipping", Namespace: "otel-demo", State: "firing", ActiveAt: at(49, 47)}
	frontendAlert := dataset.Alert{Name: "RequestErrorRate", Service: "frontend", Namespace: "otel-demo", State: "firing", ActiveAt: at(55, 0)}
	first, second := shippingAlert, frontendAlert
	first.Polled, second.Polled = at(50, 35), at(56, 35)
	shippingAgain := shippingAlert
	shippingAgain.Polled = at(56, 35)
	return &dataset.Dataset{
		Meta: dataset.Meta{Namespace: "otel-demo", AlertTime: at(57, 59), WindowStart: at(27, 59), TracesUntil: at(44, 0),
			Trigger: []dataset.Alert{shippingAlert}},
		Alerts: []dataset.Alert{first, shippingAgain, second},
		Changes: []dataset.Change{
			{Time: at(40, 0), Kind: "ConfigMap", Name: "flagd-config", Service: "flagd", Action: "updated"},
			{Time: at(52, 47), Kind: "Deployment", Name: "shipping", Service: "shipping", Action: "updated",
				Fields: []dataset.Field{{Path: "spec.template.spec.containers[shipping].env[QUOTE_ADDR].value", From: str("quote:8080"), To: str("quote:0000")}}},
		},
		Objects: []dataset.Object{
			{Kind: "Deployment", Name: "shipping", Service: "shipping", Observed: at(55, 41), Body: json.RawMessage(`{"kind":"Deployment","metadata":{"name":"shipping"}}`)},
			{Kind: "Pod", Name: "shipping-1", Service: "shipping", Observed: at(55, 41), Body: json.RawMessage(
				`{"kind":"Pod","status":{"phase":"Running","containerStatuses":[{"name":"shipping","ready":false,"restartCount":2,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}`)},
			{Kind: "Pod", Name: "ad-1", Service: "ad", Observed: at(55, 41), Body: json.RawMessage(
				`{"kind":"Pod","status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable"}]}}`)},
		},
		Events: []dataset.Event{
			{Time: at(53, 0), Type: "Warning", Reason: "BackOff", ObjectKind: "Pod", ObjectName: "shipping-1", Message: "Back-off restarting", Count: 3},
			{Time: at(54, 0), Type: "Normal", Reason: "Pulled", ObjectKind: "Pod", ObjectName: "cart-1", Message: "pulled"},
		},
		Logs: []dataset.Log{
			{Time: at(50, 0), Service: "shipping", Body: "Failed to get quote: InvalidUri"},
			{Time: at(51, 0), Service: "shipping", Body: "order shipped"},
			{Time: at(52, 0), Service: "shipping", Body: "FAILED to get quote again"},
			{Time: at(53, 0), Service: "cart", Body: "failed to get cart"},
		},
		Spans: []dataset.Span{
			{Time: at(43, 0), TraceID: "t1", SpanID: "s1", Service: "shipping", Name: "/get-quote", Kind: "Server", Status: "Error", Message: "Internal Server Error"},
			{Time: at(43, 30), TraceID: "t2", SpanID: "s2", Service: "checkout", Name: "PlaceOrder", Kind: "Server", Status: "Error"},
		},
		Edges: []dataset.Edge{{From: "checkout", To: "shipping", Calls: 10, Errors: 2}, {From: "frontend", To: "checkout", Calls: 20}},
		Services: []dataset.ServicePoint{
			{Time: at(55, 0), Service: "shipping", Operation: "/get-quote", Kind: "server", Requests: 10, Errors: 4, P95MS: 12},
			{Time: at(55, 0), Service: "shipping", Operation: "/ship-order", Kind: "server", Requests: 5, P95MS: 30},
			{Time: at(55, 0), Service: "shipping", Operation: "POST quote", Kind: "client", Requests: 10, Errors: 4, P95MS: 2},
			{Time: at(56, 0), Service: "shipping", Operation: "/get-quote", Kind: "server", Requests: 8, Errors: 8, P95MS: 9},
		},
		Pods: []dataset.PodPoint{
			{Time: at(55, 0), Pod: "shipping-1", Service: "shipping", CPUCores: 0.01, MemoryBytes: 2e7},
			{Time: at(55, 0), Pod: "cart-1", Service: "cart", CPUCores: 0.02, MemoryBytes: 3e7},
		},
	}
}

func TestListAlertsReturnsTheLastPollAtOrBeforeTheTime(t *testing.T) {
	tools := server.NewTools(fixture())

	latest, err := tools.ListAlerts(&server.AlertsInput{})
	require.NoError(t, err)
	assert.Equal(t, at(56, 35), latest.Polled)
	assert.Len(t, latest.Alerts, 2)

	earlier, err := tools.ListAlerts(&server.AlertsInput{At: "2025-12-15T17:51:00Z"})
	require.NoError(t, err)
	assert.Equal(t, at(50, 35), earlier.Polled)
	assert.Equal(t, "shipping", earlier.Alerts[0].Service)

	future, err := tools.ListAlerts(&server.AlertsInput{At: "2025-12-15T19:00:00Z"})
	require.NoError(t, err)
	assert.Equal(t, at(56, 35), future.Polled, "times after the alert are clamped to it")

	_, err = tools.ListAlerts(&server.AlertsInput{At: "yesterday"})
	assert.ErrorContains(t, err, "RFC 3339")
}

func TestGetChangesFiltersByServiceNewestFirst(t *testing.T) {
	tools := server.NewTools(fixture())

	all, err := tools.GetChanges(&server.ChangesInput{})
	require.NoError(t, err)
	require.Len(t, all.Changes, 2)
	assert.Equal(t, "shipping", all.Changes[0].Name)

	flagd, err := tools.GetChanges(&server.ChangesInput{Service: "flagd"})
	require.NoError(t, err)
	require.Len(t, flagd.Changes, 1)
	assert.Equal(t, "flagd-config", flagd.Changes[0].Name)

	none, err := tools.GetChanges(&server.ChangesInput{Window: server.Window{Since: "2025-12-15T17:53:00Z"}})
	require.NoError(t, err)
	assert.Empty(t, none.Changes)
}

func TestGetEventsFilters(t *testing.T) {
	tools := server.NewTools(fixture())

	warnings, err := tools.GetEvents(&server.EventsInput{Type: "Warning"})
	require.NoError(t, err)
	require.Len(t, warnings.Events, 1)
	assert.Equal(t, "BackOff", warnings.Events[0].Reason)

	cart, err := tools.GetEvents(&server.EventsInput{Object: "cart-1"})
	require.NoError(t, err)
	require.Len(t, cart.Events, 1)
	assert.Equal(t, "Pulled", cart.Events[0].Reason)
}

func TestQueryLogsMatchesTextAndKeepsTheMostRecent(t *testing.T) {
	tools := server.NewTools(fixture())

	out, err := tools.QueryLogs(&server.LogsInput{Service: "shipping", Contains: "failed", Limit: 1})

	require.NoError(t, err)
	assert.Equal(t, 2, out.Total)
	assert.True(t, out.Truncated)
	require.Len(t, out.Logs, 1)
	assert.Equal(t, "FAILED to get quote again", out.Logs[0].Body)

	_, err = tools.QueryLogs(&server.LogsInput{})
	assert.ErrorContains(t, err, "service")
}

func TestGetErrorSpansSaysWhereTracesEnd(t *testing.T) {
	tools := server.NewTools(fixture())

	out, err := tools.GetErrorSpans(&server.SpansInput{Service: "shipping"})

	require.NoError(t, err)
	assert.Equal(t, at(44, 0), out.TracesUntil)
	require.Len(t, out.Spans, 1)
	assert.Equal(t, "/get-quote", out.Spans[0].Name)
}

func TestServiceMetricsAggregateOperationsPerMinute(t *testing.T) {
	tools := server.NewTools(fixture())

	total, err := tools.GetServiceMetrics(&server.ServiceMetricsInput{Service: "shipping"})
	require.NoError(t, err)
	assert.Equal(t, []server.MetricPoint{
		{Time: at(55, 0), Requests: 15, Errors: 4, MaxP95MS: 30},
		{Time: at(56, 0), Requests: 8, Errors: 8, MaxP95MS: 9},
	}, total.Points)

	client, err := tools.GetServiceMetrics(&server.ServiceMetricsInput{Service: "shipping", Kind: "client", Operation: "POST quote"})
	require.NoError(t, err)
	assert.Equal(t, []server.MetricPoint{{Time: at(55, 0), Requests: 10, Errors: 4, MaxP95MS: 2}}, client.Points)
}

func TestGetPodMetricsSelectsTheServicesPods(t *testing.T) {
	tools := server.NewTools(fixture())

	out, err := tools.GetPodMetrics(&server.PodMetricsInput{Service: "cart"})

	require.NoError(t, err)
	require.Len(t, out.Points, 1)
	assert.Equal(t, "cart-1", out.Points[0].Pod)
}

func TestListObjectsSummarizesPods(t *testing.T) {
	tools := server.NewTools(fixture())

	out, err := tools.ListObjects(&server.ListObjectsInput{Kind: "Pod"})

	require.NoError(t, err)
	require.Len(t, out.Objects, 2)
	assert.Equal(t, "ad-1", out.Objects[0].Name)
	assert.Equal(t, "Pending; Unschedulable", out.Objects[0].Status)
	assert.Equal(t, "Running; ready 0/1; restarts 2; CrashLoopBackOff", out.Objects[1].Status)
}

func TestGetObjectReturnsTheSnapshot(t *testing.T) {
	tools := server.NewTools(fixture())

	out, err := tools.GetObject(&server.GetObjectInput{Kind: "Deployment", Name: "shipping"})
	require.NoError(t, err)
	assert.Equal(t, at(55, 41), out.Observed)
	assert.Equal(t, map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": "shipping"}}, out.Object)

	_, err = tools.GetObject(&server.GetObjectInput{Kind: "Deployment", Name: "nope"})
	assert.ErrorContains(t, err, "no Deployment named nope")
}

func TestListServicesJoinsObjectsAndCalls(t *testing.T) {
	tools := server.NewTools(fixture())

	out, err := tools.ListServices(&server.ListServicesInput{})

	require.NoError(t, err)
	names := make([]string, 0, len(out.Services))
	for _, s := range out.Services {
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"ad", "cart", "checkout", "flagd", "frontend", "shipping"}, names)
	for _, s := range out.Services {
		if s.Name == "checkout" {
			assert.Equal(t, []server.Call{{Service: "shipping", Calls: 10, Errors: 2}}, s.Calls)
			assert.Equal(t, []server.Call{{Service: "frontend", Calls: 20}}, s.CalledBy)
		}
	}
}
```

The SDK derives each tool's input and output schema from its types, and checks every call and answer against them. So `TestEveryToolAnswersOverMCP` calls every tool over MCP: an output type the SDK maps to the wrong schema fails only there. Create `server/mcp_test.go`:

```go
package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/server"
)

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientSide, serverSide := mcp.NewInMemoryTransports()
	_, err := server.New(fixture()).Connect(ctx, serverSide, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientSide, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestTheServerOffersOnlyReadOnlyTools(t *testing.T) {
	cs := connect(t)

	res, err := cs.ListTools(context.Background(), nil)

	require.NoError(t, err)
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		assert.True(t, tool.Annotations.ReadOnlyHint, tool.Name)
	}
	slices.Sort(names)
	assert.Equal(t, []string{"get_changes", "get_error_spans", "get_events", "get_object", "get_pod_metrics",
		"get_service_metrics", "list_alerts", "list_objects", "list_services", "query_logs"}, names)
	assert.Contains(t, cs.InitializeResult().Instructions, "2025-12-15T17:57:59Z")
}

func TestAToolCallReturnsJSONText(t *testing.T) {
	cs := connect(t)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_changes", Arguments: map[string]any{"service": "shipping"}})

	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, `"to":"quote:0000"`)
}

func TestABadCallIsAToolError(t *testing.T) {
	cs := connect(t)

	missing, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "query_logs", Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.True(t, missing.IsError)

	unknown, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_changes", Arguments: map[string]any{"servise": "shipping"}})
	require.NoError(t, err)
	assert.True(t, unknown.IsError, "an unknown argument is rejected, not ignored")
}

func TestTheHandlerServesMCPAndHealth(t *testing.T) {
	srv := httptest.NewServer(server.Handler(fixture()))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	resp, err = http.Get(srv.URL + "/other")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_alerts"})
	require.NoError(t, err)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "RequestErrorRate")
}

// Every tool must answer over MCP, not only when called directly: the SDK
// checks each answer against the schema it derives from the output type.
func TestEveryToolAnswersOverMCP(t *testing.T) {
	cs := connect(t)
	for tool, args := range map[string]map[string]any{
		"list_alerts":         nil,
		"list_services":       nil,
		"get_changes":         nil,
		"get_events":          nil,
		"query_logs":          {"service": "shipping"},
		"get_error_spans":     nil,
		"get_service_metrics": {"service": "shipping"},
		"get_pod_metrics":     {"service": "shipping"},
		"list_objects":        {"kind": "Pod"},
		"get_object":          {"kind": "Deployment", "name": "shipping"},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		require.NoError(t, err, tool)
		assert.False(t, res.IsError, tool)
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./server`
Expected: FAIL with `no non-test Go files in .../bench/itbench/server`.

- [ ] **Step 4: Write the tools**

Every answer is bounded: at most 200 items, and log bodies at 1,000 characters. Times are clamped to the window, so a time after the alert reads as the alert. Create `server/tools.go`:

```go
// Package server serves one prepared scenario through read-only MCP tools.
// Every read ends at the alert time, because the dataset does.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

const (
	defaultLimit = 50
	maxLimit     = 200
	maxBody      = 1000
)

// Tools answers the read tools' queries over one dataset.
type Tools struct{ d *dataset.Dataset }

// NewTools returns the tools over a dataset.
func NewTools(d *dataset.Dataset) *Tools { return &Tools{d: d} }

// Window is a time range within the dataset's half hour before the alert.
type Window struct {
	Since string `json:"since,omitempty" jsonschema:"start of the range, RFC 3339; default and earliest: 30 minutes before the alert"`
	Until string `json:"until,omitempty" jsonschema:"end of the range, RFC 3339; default and latest: the alert time"`
}

func (t *Tools) window(w Window) (since, until time.Time, err error) {
	if since, err = t.clamp(w.Since, t.d.Meta.WindowStart); err != nil {
		return since, until, err
	}
	until, err = t.clamp(w.Until, t.d.Meta.AlertTime)
	return since, until, err
}

// clamp parses a time, defaulting to def, and keeps it within the window.
func (t *Tools) clamp(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q is not RFC 3339, for example 2025-12-15T17:50:00Z", s)
	}
	if v.Before(t.d.Meta.WindowStart) {
		return t.d.Meta.WindowStart, nil
	}
	if v.After(t.d.Meta.AlertTime) {
		return t.d.Meta.AlertTime, nil
	}
	return v.UTC(), nil
}

func within(at, since, until time.Time) bool { return !at.Before(since) && !at.After(until) }

func limitOf(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	return min(n, maxLimit)
}

// AlertsInput selects a poll of the alerting system.
type AlertsInput struct {
	At string `json:"at,omitempty" jsonschema:"RFC 3339 time; the last poll at or before it is returned; default and latest: the alert time"`
}

// AlertsOutput is one poll of the alerting system.
type AlertsOutput struct {
	Polled time.Time       `json:"polled"`
	Alerts []dataset.Alert `json:"alerts"`
}

// ListAlerts returns the alerts firing at the last poll at or before a time.
func (t *Tools) ListAlerts(in *AlertsInput) (AlertsOutput, error) {
	at, err := t.clamp(in.At, t.d.Meta.AlertTime)
	if err != nil {
		return AlertsOutput{}, err
	}
	out := AlertsOutput{Alerts: []dataset.Alert{}}
	for i := range t.d.Alerts {
		if p := t.d.Alerts[i].Polled; !p.After(at) && p.After(out.Polled) {
			out.Polled = p
		}
	}
	for i := range t.d.Alerts {
		if a := t.d.Alerts[i]; a.Polled.Equal(out.Polled) {
			a.Polled = time.Time{}
			out.Alerts = append(out.Alerts, a)
		}
	}
	return out, nil
}

// ListServicesInput takes no arguments.
type ListServicesInput struct{}

// Call counts the calls between this service and another, from traces.
type Call struct {
	Service string `json:"service"`
	Calls   int    `json:"calls"`
	Errors  int    `json:"errors,omitempty"`
}

// Service is one service: whom it calls and who calls it.
type Service struct {
	Name     string `json:"name"`
	Calls    []Call `json:"calls,omitempty"`
	CalledBy []Call `json:"called_by,omitempty"`
}

// ListServicesOutput lists the namespace's services.
type ListServicesOutput struct {
	Services []Service `json:"services"`
}

// ListServices lists every service the data names, with the calls between
// them seen in traces.
func (t *Tools) ListServices(*ListServicesInput) (ListServicesOutput, error) {
	byName := map[string]*Service{}
	add := func(name string) *Service {
		if byName[name] == nil {
			byName[name] = &Service{Name: name}
		}
		return byName[name]
	}
	for _, o := range t.d.Objects {
		if o.Service != "" {
			add(o.Service)
		}
	}
	for _, c := range t.d.Changes {
		if c.Service != "" {
			add(c.Service)
		}
	}
	for _, l := range t.d.Logs {
		add(l.Service)
	}
	for _, p := range t.d.Pods {
		add(p.Service)
	}
	for _, e := range t.d.Edges {
		add(e.From).Calls = append(add(e.From).Calls, Call{Service: e.To, Calls: e.Calls, Errors: e.Errors})
		add(e.To).CalledBy = append(add(e.To).CalledBy, Call{Service: e.From, Calls: e.Calls, Errors: e.Errors})
	}
	out := ListServicesOutput{Services: []Service{}}
	for _, s := range byName {
		out.Services = append(out.Services, *s)
	}
	sort.Slice(out.Services, func(i, j int) bool { return out.Services[i].Name < out.Services[j].Name })
	return out, nil
}

// ChangesInput selects changes.
type ChangesInput struct {
	Service string `json:"service,omitempty" jsonschema:"only changes to this service's objects"`
	Window
}

// ChangesOutput lists changes, newest first.
type ChangesOutput struct {
	Changes []dataset.Change `json:"changes"`
}

// GetChanges lists changes to desired state, newest first.
func (t *Tools) GetChanges(in *ChangesInput) (ChangesOutput, error) {
	since, until, err := t.window(in.Window)
	if err != nil {
		return ChangesOutput{}, err
	}
	out := ChangesOutput{Changes: []dataset.Change{}}
	for _, c := range slices.Backward(t.d.Changes) {
		if within(c.Time, since, until) && (in.Service == "" || c.Service == in.Service) {
			out.Changes = append(out.Changes, c)
		}
	}
	return out, nil
}

// EventsInput selects Kubernetes events.
type EventsInput struct {
	Object string `json:"object,omitempty" jsonschema:"only events about the object with this name"`
	Type   string `json:"type,omitempty" jsonschema:"Warning or Normal"`
	Limit  int    `json:"limit,omitempty" jsonschema:"at most this many of the most recent events; default 50, at most 200"`
	Window
}

// EventsOutput lists events, newest first.
type EventsOutput struct {
	Events []dataset.Event `json:"events"`
	Total  int             `json:"total"`
}

// GetEvents lists Kubernetes events by their last occurrence, newest first.
func (t *Tools) GetEvents(in *EventsInput) (EventsOutput, error) {
	since, until, err := t.window(in.Window)
	if err != nil {
		return EventsOutput{}, err
	}
	out := EventsOutput{Events: []dataset.Event{}}
	for i := len(t.d.Events) - 1; i >= 0; i-- {
		e := &t.d.Events[i]
		if !within(e.Time, since, until) || (in.Object != "" && e.ObjectName != in.Object) || (in.Type != "" && !strings.EqualFold(e.Type, in.Type)) {
			continue
		}
		out.Total++
		if len(out.Events) < limitOf(in.Limit) {
			out.Events = append(out.Events, *e)
		}
	}
	return out, nil
}

// LogsInput selects a service's log records.
type LogsInput struct {
	Service  string `json:"service" jsonschema:"the service whose logs to search"`
	Contains string `json:"contains,omitempty" jsonschema:"only records containing this text, ignoring case"`
	Limit    int    `json:"limit,omitempty" jsonschema:"at most this many of the most recent matches; default 50, at most 200"`
	Window
}

// LogsOutput holds the most recent matching records, oldest first.
type LogsOutput struct {
	Logs      []dataset.Log `json:"logs"`
	Total     int           `json:"total"`
	Truncated bool          `json:"truncated"`
}

// QueryLogs searches a service's logs and returns the most recent matches.
func (t *Tools) QueryLogs(in *LogsInput) (LogsOutput, error) {
	if in.Service == "" {
		return LogsOutput{}, errors.New("name a service")
	}
	since, until, err := t.window(in.Window)
	if err != nil {
		return LogsOutput{}, err
	}
	needle := strings.ToLower(in.Contains)
	var matches []dataset.Log
	for _, l := range t.d.Logs {
		if l.Service == in.Service && within(l.Time, since, until) && strings.Contains(strings.ToLower(l.Body), needle) {
			matches = append(matches, l)
		}
	}
	keep := limitOf(in.Limit)
	out := LogsOutput{Logs: []dataset.Log{}, Total: len(matches), Truncated: len(matches) > keep}
	for _, l := range matches[max(0, len(matches)-keep):] {
		if r := []rune(l.Body); len(r) > maxBody {
			l.Body = string(r[:maxBody]) + "…"
		}
		out.Logs = append(out.Logs, l)
	}
	return out, nil
}

// SpansInput selects spans that ended in error.
type SpansInput struct {
	Service string `json:"service,omitempty" jsonschema:"only spans of this service"`
	Limit   int    `json:"limit,omitempty" jsonschema:"at most this many of the most recent spans; default 50, at most 200"`
	Window
}

// SpansOutput lists error spans, newest first, and where the traces end.
type SpansOutput struct {
	Spans       []dataset.Span `json:"spans"`
	Total       int            `json:"total"`
	TracesUntil time.Time      `json:"traces_until"`
}

// GetErrorSpans lists the spans that ended in error, newest first. Traces
// may end before the alert; TracesUntil says where.
func (t *Tools) GetErrorSpans(in *SpansInput) (SpansOutput, error) {
	since, until, err := t.window(in.Window)
	if err != nil {
		return SpansOutput{}, err
	}
	out := SpansOutput{Spans: []dataset.Span{}, TracesUntil: t.d.Meta.TracesUntil}
	for i := len(t.d.Spans) - 1; i >= 0; i-- {
		s := &t.d.Spans[i]
		if !within(s.Time, since, until) || (in.Service != "" && s.Service != in.Service) {
			continue
		}
		out.Total++
		if len(out.Spans) < limitOf(in.Limit) {
			out.Spans = append(out.Spans, *s)
		}
	}
	return out, nil
}

// ServiceMetricsInput selects a service's request metrics.
type ServiceMetricsInput struct {
	Service   string `json:"service" jsonschema:"the service"`
	Kind      string `json:"kind,omitempty" jsonschema:"server (requests it serves, the default) or client (calls it makes)"`
	Operation string `json:"operation,omitempty" jsonschema:"one operation (span name); default: all operations together"`
	Window
}

// MetricPoint is one minute of requests.
type MetricPoint struct {
	Time     time.Time `json:"time"`
	Requests float64   `json:"requests"`
	Errors   float64   `json:"errors"`
	MaxP95MS float64   `json:"max_p95_ms"`
}

// ServiceMetricsOutput is a service's requests per minute.
type ServiceMetricsOutput struct {
	Points []MetricPoint `json:"points"`
}

// GetServiceMetrics returns a service's requests, errors and p95 latency per
// minute. Over several operations it sums requests and errors and gives the
// highest operation's p95.
func (t *Tools) GetServiceMetrics(in *ServiceMetricsInput) (ServiceMetricsOutput, error) {
	if in.Service == "" {
		return ServiceMetricsOutput{}, errors.New("name a service")
	}
	since, until, err := t.window(in.Window)
	if err != nil {
		return ServiceMetricsOutput{}, err
	}
	kind := in.Kind
	if kind == "" {
		kind = "server"
	}
	byMinute := map[time.Time]*MetricPoint{}
	for _, p := range t.d.Services {
		if p.Service != in.Service || p.Kind != kind || (in.Operation != "" && p.Operation != in.Operation) || !within(p.Time, since, until) {
			continue
		}
		m := byMinute[p.Time]
		if m == nil {
			m = &MetricPoint{Time: p.Time}
			byMinute[p.Time] = m
		}
		m.Requests += p.Requests
		m.Errors += p.Errors
		m.MaxP95MS = max(m.MaxP95MS, p.P95MS)
	}
	out := ServiceMetricsOutput{Points: []MetricPoint{}}
	for _, m := range byMinute {
		out.Points = append(out.Points, *m)
	}
	sort.Slice(out.Points, func(i, j int) bool { return out.Points[i].Time.Before(out.Points[j].Time) })
	return out, nil
}

// PodMetricsInput selects pods' resource use.
type PodMetricsInput struct {
	Service string `json:"service,omitempty" jsonschema:"the service whose pods to show"`
	Pod     string `json:"pod,omitempty" jsonschema:"one pod"`
	Window
}

// PodMetricsOutput is pods' resource use per minute.
type PodMetricsOutput struct {
	Points []dataset.PodPoint `json:"points"`
}

// GetPodMetrics returns CPU and memory use and limits per pod and minute.
func (t *Tools) GetPodMetrics(in *PodMetricsInput) (PodMetricsOutput, error) {
	if in.Service == "" && in.Pod == "" {
		return PodMetricsOutput{}, errors.New("name a service or a pod")
	}
	since, until, err := t.window(in.Window)
	if err != nil {
		return PodMetricsOutput{}, err
	}
	out := PodMetricsOutput{Points: []dataset.PodPoint{}}
	for _, p := range t.d.Pods {
		if (in.Service == "" || p.Service == in.Service) && (in.Pod == "" || p.Pod == in.Pod) && within(p.Time, since, until) {
			out.Points = append(out.Points, p)
		}
	}
	return out, nil
}

// ListObjectsInput selects a kind of object.
type ListObjectsInput struct {
	Kind string `json:"kind" jsonschema:"for example Pod, Deployment, Service, ConfigMap, ResourceQuota"`
}

// ObjectSummary is one object in a list.
type ObjectSummary struct {
	Name     string    `json:"name"`
	Service  string    `json:"service,omitempty"`
	Observed time.Time `json:"observed"`
	Status   string    `json:"status,omitempty"`
}

// ListObjectsOutput lists objects by name.
type ListObjectsOutput struct {
	Objects []ObjectSummary `json:"objects"`
}

// ListObjects lists the objects of one kind as last observed, with a status
// line for pods.
func (t *Tools) ListObjects(in *ListObjectsInput) (ListObjectsOutput, error) {
	out := ListObjectsOutput{Objects: []ObjectSummary{}}
	for _, o := range t.d.Objects {
		if o.Kind != in.Kind {
			continue
		}
		s := ObjectSummary{Name: o.Name, Service: o.Service, Observed: o.Observed}
		if o.Kind == "Pod" {
			s.Status = podStatus(o.Body)
		}
		out.Objects = append(out.Objects, s)
	}
	sort.Slice(out.Objects, func(i, j int) bool { return out.Objects[i].Name < out.Objects[j].Name })
	return out, nil
}

// podStatus summarizes a pod: its phase, readiness, restarts and the reasons
// its containers wait or it is not scheduled.
func podStatus(body json.RawMessage) string {
	var pod struct {
		Status struct {
			Phase      string `json:"phase"`
			Conditions []struct {
				Type, Status, Reason string
			} `json:"conditions"`
			ContainerStatuses []struct {
				Ready        bool `json:"ready"`
				RestartCount int  `json:"restartCount"`
				State        struct {
					Waiting *struct {
						Reason string `json:"reason"`
					} `json:"waiting"`
				} `json:"state"`
			} `json:"containerStatuses"`
		} `json:"status"`
	}
	if json.Unmarshal(body, &pod) != nil {
		return ""
	}
	parts := []string{pod.Status.Phase}
	for _, c := range pod.Status.Conditions {
		if c.Type == "PodScheduled" && c.Status == "False" && c.Reason != "" {
			parts = append(parts, c.Reason)
		}
	}
	if n := len(pod.Status.ContainerStatuses); n > 0 {
		ready, restarts := 0, 0
		var waiting []string
		for _, c := range pod.Status.ContainerStatuses {
			if c.Ready {
				ready++
			}
			restarts += c.RestartCount
			if c.State.Waiting != nil && c.State.Waiting.Reason != "" {
				waiting = append(waiting, c.State.Waiting.Reason)
			}
		}
		parts = append(parts, fmt.Sprintf("ready %d/%d", ready, n), fmt.Sprintf("restarts %d", restarts))
		parts = append(parts, waiting...)
	}
	return strings.Join(parts, "; ")
}

// GetObjectInput names one object.
type GetObjectInput struct {
	Kind string `json:"kind" jsonschema:"the object's kind, for example Deployment"`
	Name string `json:"name" jsonschema:"the object's name"`
}

// GetObjectOutput is an object as last observed at or before the alert. The
// object is decoded: as raw bytes, its schema would be an array.
type GetObjectOutput struct {
	Observed time.Time      `json:"observed"`
	Object   map[string]any `json:"object"`
}

// GetObject returns one object as the last snapshot at or before the alert
// saw it. Snapshots are about five minutes apart.
func (t *Tools) GetObject(in *GetObjectInput) (GetObjectOutput, error) {
	for i := range t.d.Objects {
		if o := &t.d.Objects[i]; o.Kind == in.Kind && o.Name == in.Name {
			out := GetObjectOutput{Observed: o.Observed}
			return out, json.Unmarshal(o.Body, &out.Object)
		}
	}
	return GetObjectOutput{}, fmt.Errorf("no %s named %s", in.Kind, in.Name)
}
```

Create `server/mcp.go`:

```go
package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// New returns an MCP server whose tools read the dataset and nothing else.
func New(d *dataset.Dataset) *mcp.Server {
	t := NewTools(d)
	s := mcp.NewServer(&mcp.Implementation{Name: "itbench-read", Version: "1"}, &mcp.ServerOptions{
		Instructions: fmt.Sprintf("Read-only views of namespace %s. Data starts at %s and ends at the alert time, %s. Times are RFC 3339 in UTC.",
			d.Meta.Namespace, d.Meta.WindowStart.Format(time.RFC3339), d.Meta.AlertTime.Format(time.RFC3339)),
	})
	add(s, "list_alerts", "List the alerts firing at the last poll at or before a time; polls are a minute apart.", t.ListAlerts)
	add(s, "list_services", "List the services, with the calls between them seen in traces.", t.ListServices)
	add(s, "get_changes", "List changes to Kubernetes objects' desired state, newest first, with the fields each changed.", t.GetChanges)
	add(s, "get_events", "List Kubernetes events by their last occurrence, newest first.", t.GetEvents)
	add(s, "query_logs", "Search one service's logs; returns the most recent matches, oldest first.", t.QueryLogs)
	add(s, "get_error_spans", "List trace spans that ended in error, newest first. traces_until says where the traces end.", t.GetErrorSpans)
	add(s, "get_service_metrics", "Return a service's requests, errors and p95 latency per minute.", t.GetServiceMetrics)
	add(s, "get_pod_metrics", "Return pods' CPU and memory use and limits per minute.", t.GetPodMetrics)
	add(s, "list_objects", "List the Kubernetes objects of one kind as last observed, with a status line for pods.", t.ListObjects)
	add(s, "get_object", "Return one Kubernetes object as last observed; snapshots are about five minutes apart.", t.GetObject)
	return s
}

func add[In, Out any](s *mcp.Server, name, description string, fn func(*In) (Out, error)) {
	tool := &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}}
	mcp.AddTool(s, tool, func(_ context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := fn(&in)
		return nil, out, err
	})
}

// Handler serves the tools over streamable HTTP at /mcp, and a health check
// at /healthz.
func Handler(d *dataset.Dataset) http.Handler {
	s := New(d)
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = io.WriteString(w, "ok")
		case "/mcp":
			streamable.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./server`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/server`.

- [ ] **Step 6: Lint**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 7: Commit**

```bash
git add bench/itbench/go.mod bench/itbench/go.sum bench/itbench/server/tools.go bench/itbench/server/mcp.go \
  bench/itbench/server/tools_test.go bench/itbench/server/mcp_test.go
git commit -m "feat(bench): serve a dataset through read-only MCP tools"
git push
```

---

### Task 11: Downloads

**Files:**
- Create: `bench/itbench/fetch/fetch.go`
- Test: `bench/itbench/fetch/fetch_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces, in package `fetch`:
  - `func ParseSource(dataset, revision string) (*Source, error)`, which accepts only a commit id;
  - `func (s *Source) Scenario(ctx context.Context, prefix, dir, destRoot string) (int, error)`.

- [ ] **Step 1: Write the failing tests**

The tests run against a stand-in for the hub: a listing in two pages and the files' contents. Create `fetch/fetch_test.go`:

```go
package fetch_test

import (
	"context"
	"crypto/sha1" //nolint:gosec // git blob ids are SHA-1; the test computes one.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/fetch"
)

const (
	revision = "d0916b08ba421ce5e672e9ad68aa947d938dfef0"
	prefix   = "snapshots/sre/v0.2"
)

func blobID(content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content))) //nolint:gosec // see the import.
	return hex.EncodeToString(sum[:])
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// hub stands in for the Hugging Face hub: a tree listing in two pages, and
// file contents.
type hub struct {
	listing  []map[string]any
	contents map[string]string
	gets     atomic.Int32
}

func (h *hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	treePrefix := "/api/datasets/org/bench/tree/" + revision + "/"
	resolvePrefix := "/datasets/org/bench/resolve/" + revision + "/"
	switch {
	case strings.HasPrefix(r.URL.Path, treePrefix):
		page := h.listing[:1]
		if r.URL.Query().Get("cursor") == "2" {
			page = h.listing[1:]
		} else {
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?recursive=true&cursor=2>; rel="next"`, r.Host, r.URL.Path))
		}
		_ = json.NewEncoder(w).Encode(page)
	case strings.HasPrefix(r.URL.Path, resolvePrefix):
		h.gets.Add(1)
		content, ok := h.contents[strings.TrimPrefix(r.URL.Path, resolvePrefix)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(content))
	default:
		http.NotFound(w, r)
	}
}

func newHub(t *testing.T, small, large, largeID string) (*hub, *fetch.Source) {
	t.Helper()
	h := &hub{
		listing: []map[string]any{
			{"type": "file", "path": prefix + "/Scenario-1/ground_truth.yaml", "size": len(small), "oid": blobID(small)},
			{"type": "file", "path": prefix + "/Scenario-1/metrics/pod_x_raw.tsv", "size": len(large), "oid": "x",
				"lfs": map[string]any{"oid": largeID, "size": len(large)}},
		},
		contents: map[string]string{prefix + "/Scenario-1/ground_truth.yaml": small, prefix + "/Scenario-1/metrics/pod_x_raw.tsv": large},
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	src, err := fetch.ParseSource(srv.URL+"/datasets/org/bench", revision)
	require.NoError(t, err)
	return h, src
}

func TestScenarioDownloadsAndChecksEveryFile(t *testing.T) {
	h, src := newHub(t, "fault: x\n", "a\tb\n1\t2\n", sha256Hex("a\tb\n1\t2\n"))
	dest := t.TempDir()

	n, err := src.Scenario(context.Background(), prefix, "Scenario-1", dest)

	require.NoError(t, err)
	assert.Equal(t, 2, n)
	got, err := os.ReadFile(filepath.Join(dest, "Scenario-1", "metrics", "pod_x_raw.tsv"))
	require.NoError(t, err)
	assert.Equal(t, "a\tb\n1\t2\n", string(got))
	assert.Equal(t, int32(2), h.gets.Load())

	_, err = src.Scenario(context.Background(), prefix, "Scenario-1", dest)
	require.NoError(t, err)
	assert.Equal(t, int32(2), h.gets.Load(), "files already present with the right hash are not fetched again")
}

func TestAHashMismatchFailsAndLeavesNoFile(t *testing.T) {
	_, src := newHub(t, "fault: x\n", "a\tb\n", sha256Hex("something else"))
	dest := t.TempDir()

	_, err := src.Scenario(context.Background(), prefix, "Scenario-1", dest)

	require.ErrorContains(t, err, "sha256")
	_, statErr := os.Stat(filepath.Join(dest, "Scenario-1", "metrics", "pod_x_raw.tsv"))
	assert.True(t, os.IsNotExist(statErr))
	leftovers, _ := filepath.Glob(filepath.Join(dest, "Scenario-1", "metrics", "*"))
	assert.Empty(t, leftovers)
}

func TestAListingThatEscapesTheScenarioIsRefused(t *testing.T) {
	h, src := newHub(t, "fault: x\n", "a\n", sha256Hex("a\n"))
	h.listing[1]["path"] = prefix + "/Scenario-1/../../escape.tsv"

	_, err := src.Scenario(context.Background(), prefix, "Scenario-1", t.TempDir())

	require.ErrorContains(t, err, "outside")
}

func TestParseSourceNeedsADatasetURL(t *testing.T) {
	_, err := fetch.ParseSource("https://huggingface.co/models/org/x", revision)
	require.Error(t, err)
	_, err = fetch.ParseSource("https://huggingface.co/datasets/org/bench", "main")
	require.ErrorContains(t, err, "commit")
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./fetch`
Expected: FAIL with `no non-test Go files in .../bench/itbench/fetch`.

- [ ] **Step 3: Write the downloader**

The hub lists a file in LFS with its SHA-256 and any other file with its git blob id, a SHA-1. A file is written under a temporary name and renamed only once its size and hash match. Create `fetch/fetch.go`:

```go
// Package fetch downloads scenario directories from a Hugging Face dataset at
// a pinned commit, and checks every file against the hub's hash.
package fetch

import (
	"context"
	"crypto/sha1" //nolint:gosec // the hub identifies small files by their git blob id, which is SHA-1.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	commit   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)
)

// maxPages bounds a listing, so that a hub that keeps linking cannot loop.
const maxPages = 100

// Source is a dataset on the hub at one commit.
type Source struct {
	base, repo, revision string
	client               *http.Client
}

// ParseSource reads a dataset URL such as
// https://huggingface.co/datasets/org/name. The revision must be a commit, so
// that every run reads the same bytes.
func ParseSource(dataset, revision string) (*Source, error) {
	u, err := url.Parse(dataset)
	if err != nil {
		return nil, err
	}
	repo, ok := strings.CutPrefix(u.Path, "/datasets/")
	if !ok || strings.Count(strings.Trim(repo, "/"), "/") != 1 {
		return nil, fmt.Errorf("%s is not a dataset URL like https://huggingface.co/datasets/org/name", dataset)
	}
	if !commit.MatchString(revision) {
		return nil, fmt.Errorf("revision %q is not a commit id", revision)
	}
	return &Source{base: u.Scheme + "://" + u.Host, repo: strings.Trim(repo, "/"), revision: revision, client: &http.Client{}}, nil
}

// File is one file as the hub lists it. LFS is set for files stored in LFS,
// whose id is their SHA-256; other files' id is their git blob id.
type File struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	OID  string `json:"oid"`
	LFS  *struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"lfs"`
}

// list returns every file under dir.
func (s *Source) list(ctx context.Context, dir string) ([]File, error) {
	next := fmt.Sprintf("%s/api/datasets/%s/tree/%s/%s?recursive=true", s.base, s.repo, s.revision, dir)
	var out []File
	for page := 0; next != ""; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("listing %s: more than %d pages", dir, maxPages)
		}
		var files []File
		link, err := s.getJSON(ctx, next, &files)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", dir, err)
		}
		out = append(out, files...)
		next = ""
		if m := nextLink.FindStringSubmatch(link); m != nil {
			next = m[1]
		}
	}
	return out, nil
}

func (s *Source) getJSON(ctx context.Context, target string, v any) (link string, err error) {
	resp, err := s.get(ctx, target)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	return resp.Header.Get("Link"), json.NewDecoder(resp.Body).Decode(v)
}

func (s *Source) get(ctx context.Context, target string) (*http.Response, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	if u.Scheme+"://"+u.Host != s.base {
		return nil, fmt.Errorf("%s is not on %s", target, s.base)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req) //nolint:gosec // G704: the host was checked against the set's source above.
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", target, resp.Status)
	}
	return resp, nil
}

// Scenario downloads the files under prefix/dir into destRoot/dir, skipping
// files already there with the right hash, and returns how many files the
// directory holds.
func (s *Source) Scenario(ctx context.Context, prefix, dir, destRoot string) (int, error) {
	files, err := s.list(ctx, prefix+"/"+dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range files {
		f := &files[i]
		if f.Type != "file" {
			continue
		}
		rel := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(f.Path, prefix+"/")))
		if !strings.HasPrefix(f.Path, prefix+"/") || !filepath.IsLocal(rel) || !strings.HasPrefix(rel, dir+string(filepath.Separator)) {
			return n, fmt.Errorf("%s is outside %s", f.Path, dir)
		}
		if err := s.download(ctx, f, filepath.Join(destRoot, rel)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// download writes the file to dest, unless dest already holds it.
func (s *Source) download(ctx context.Context, f *File, dest string) (err error) {
	if ok, err := matches(f, dest); err != nil || ok {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	resp, err := s.get(ctx, fmt.Sprintf("%s/datasets/%s/resolve/%s/%s", s.base, s.repo, s.revision, f.Path))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	part := dest + ".part"
	out, err := os.Create(filepath.Clean(part))
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(part)
		}
	}()
	_, copyErr := io.Copy(out, resp.Body)
	if err := errors.Join(copyErr, out.Close()); err != nil {
		return err
	}
	if ok, err := matches(f, part); err != nil || !ok {
		return errors.Join(err, fmt.Errorf("%s: size or %s does not match the hub", f.Path, hashName(f)))
	}
	return os.Rename(part, dest)
}

// matches reports whether path holds the file the hub lists.
func matches(f *File, path string) (_ bool, err error) {
	in, err := os.Open(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	h, want := sha256.New(), ""
	if f.LFS != nil {
		want = f.LFS.OID
	} else {
		h, want = sha1.New(), f.OID //nolint:gosec // see the import.
		_, _ = fmt.Fprintf(h, "blob %d\x00", f.Size)
	}
	n, err := io.Copy(h, in)
	if err != nil {
		return false, err
	}
	return n == f.Size && sumOf(h) == want, nil
}

func sumOf(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }

func hashName(f *File) string {
	if f.LFS != nil {
		return "sha256"
	}
	return "git blob id"
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./fetch`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/fetch`.

- [ ] **Step 5: Lint**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add bench/itbench/fetch/fetch.go bench/itbench/fetch/fetch_test.go
git commit -m "feat(bench): download scenarios at a pinned commit and check their hashes"
git push
```

---

### Task 12: The `itbench` command

**Files:**
- Create: `bench/itbench/cmd/itbench/main.go`, `bench/itbench/cmd/itbench/run.go`, `bench/itbench/cmd/itbench/commands.go`, `bench/itbench/README.md`
- Test: `bench/itbench/cmd/itbench/run_test.go`
- Modify: `bench/itbench/go.mod`, `bench/itbench/go.sum`

**Interfaces:**
- Consumes: `fetch` (Task 11), `prepare.Run` (Task 7), `dataset.Load` (Task 2), `server.Handler` (Task 10), `baseline.Evaluate` (Task 9), `score` (Task 8).
- Produces: the command `itbench` with `fetch`, `prepare`, `serve`, `baseline` and `score`; exit code 0 on success, 1 when a command fails, 2 on a usage error, like `agentswarm`.

- [ ] **Step 1: Add cobra**

Run: `go get github.com/spf13/cobra@v1.10.2`
Expected: `go.mod` requires `github.com/spf13/cobra v1.10.2`, the version the main module uses.

- [ ] **Step 2: Write the failing tests**

Create `cmd/itbench/run_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

func runCommand(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

const testSet = `benchmark: test
source: https://huggingface.co/datasets/org/bench
revision: d0916b08ba421ce5e672e9ad68aa947d938dfef0
prefix: snapshots
namespace: otel-demo
flagStore: flagd-config
failureModes: [bad-release, flag-change, config-change, other]
scenarios:
  - {id: S1, dir: Scenario-1, change: true, keys: [{service: shipping, failureMode: config-change}], why: test}
  - {id: S2, dir: Scenario-2, change: false, keys: [{service: cart, failureMode: bad-release}], why: test}
`

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestUsageErrors(t *testing.T) {
	_, errOut, code := runCommand()
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "a command is required")

	_, errOut, code = runCommand("score")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, `required flag(s) "answers" not set`)
}

func TestScorePrintsEachScenarioAndTheTotals(t *testing.T) {
	dir := t.TempDir()
	set := writeTestFile(t, dir, "set.yaml", testSet)
	answers := writeTestFile(t, dir, "answers.json", `{"S1": {"service": "shipping", "failureMode": "config-change"}, "S2": {"service": "cart", "failureMode": "config-change"}}`)
	baseline := writeTestFile(t, dir, "baseline.json", `{"S1": {"service": "shipping", "failureMode": "config-change"}}`)

	out, errOut, code := runCommand("score", "--set", set, "--answers", answers, "--baseline", baseline)

	require.Equal(t, exitOK, code, errOut)
	assert.Contains(t, out, "S1")
	assert.Contains(t, out, "shipping/config-change")
	assert.Contains(t, out, "hits 1/2 (change scenarios 1/1); baseline 1/2 (change scenarios 1/1); only the answers right: 0; only the baseline right: 0")
}

func TestScoreRejectsAnAnswerOutsideTheSet(t *testing.T) {
	dir := t.TempDir()
	set := writeTestFile(t, dir, "set.yaml", testSet)
	answers := writeTestFile(t, dir, "answers.json", `{"S9": {"service": "x", "failureMode": "config-change"}}`)

	_, errOut, code := runCommand("score", "--set", set, "--answers", answers)

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "S9")
}

func TestBaselineWritesAnswersForEveryScenarioWithAChange(t *testing.T) {
	dir := t.TempDir()
	set := writeTestFile(t, dir, "set.yaml", testSet)
	datasets := filepath.Join(dir, "datasets")
	alert := time.Date(2025, 12, 15, 17, 57, 59, 0, time.UTC)
	to := "quote:0000"
	for id, changes := range map[string][]dataset.Change{
		"S1": {{Time: alert.Add(-5 * time.Minute), Kind: "Deployment", Name: "shipping", Service: "shipping", Action: "updated",
			Fields: []dataset.Field{{Path: "spec.template.spec.containers[shipping].env[QUOTE_ADDR].value", To: &to}}}},
		"S2": nil,
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(datasets, id), 0o750))
		d := &dataset.Dataset{Meta: dataset.Meta{AlertTime: alert, Trigger: []dataset.Alert{{Service: "frontend"}}}, Changes: changes}
		require.NoError(t, d.Save(filepath.Join(datasets, id)))
	}

	out, errOut, code := runCommand("baseline", "--set", set, "--datasets", datasets)
	require.Equal(t, exitOK, code, errOut)
	var answers map[string]map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &answers))
	assert.Equal(t, map[string]map[string]string{"S1": {"service": "shipping", "failureMode": "config-change"}}, answers)

	out, errOut, code = runCommand("baseline", "--set", set, "--datasets", datasets, "--scope", "service")
	require.Equal(t, exitOK, code, errOut)
	assert.JSONEq(t, `{}`, out)
}

func TestServeNeedsADataset(t *testing.T) {
	_, errOut, code := runCommand("serve", "--dataset", filepath.Join(t.TempDir(), "missing"))

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "missing: no such file or directory")
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./cmd/itbench`
Expected: FAIL with `undefined: run`.

- [ ] **Step 4: Write the command**

Create `cmd/itbench/main.go`:

```go
// Command itbench prepares ITBench-Lite scenarios for the mechanism check,
// serves them through read-only MCP tools, and scores answers by key.
package main

import "os"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
```

Create `cmd/itbench/run.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/bench/itbench/score"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

var errNoCommand = errors.New("a command is required")

// failure marks an error that happened while a command ran; any other error
// is a usage error from cobra.
type failure struct{ err error }

func (f failure) Error() string { return f.err.Error() }

func (f failure) Unwrap() error { return f.err }

// run executes one command and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	root := &cobra.Command{
		Use:           "itbench",
		Short:         "Prepare, serve and score ITBench-Lite scenarios for the mechanism check",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE:          func(*cobra.Command, []string) error { return errNoCommand },
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(newFetchCommand(), newPrepareCommand(), newServeCommand(), newBaselineCommand(), newScoreCommand())
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	cmd, err := root.ExecuteContextC(context.Background())
	var f failure
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &f):
		_, _ = fmt.Fprintf(stderr, "itbench: %v\n", err)
		return exitError
	default:
		_, _ = fmt.Fprintf(stderr, "itbench: %v\n\n%s", err, cmd.UsageString())
		return exitUsage
	}
}

// selected returns the set's scenarios, or only those named.
func selected(set *score.Set, only []string) ([]score.Scenario, error) {
	if len(only) == 0 {
		return set.Scenarios, nil
	}
	var out []score.Scenario
	for _, id := range only {
		i := slices.IndexFunc(set.Scenarios, func(s score.Scenario) bool { return s.ID == id })
		if i < 0 {
			return nil, fmt.Errorf("scenario %s is not in the set", id)
		}
		out = append(out, set.Scenarios[i])
	}
	return out, nil
}
```

Create `cmd/itbench/commands.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/bench/itbench/baseline"
	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/fetch"
	"github.com/jchen0426/agent-swarm/bench/itbench/prepare"
	"github.com/jchen0426/agent-swarm/bench/itbench/score"
	"github.com/jchen0426/agent-swarm/bench/itbench/server"
)

func newFetchCommand() *cobra.Command {
	var setPath, dest string
	var only []string
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Download the set's scenarios at the pinned commit, checking every file's hash",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			set, scenarios, err := loadSelected(setPath, only)
			if err != nil {
				return failure{err}
			}
			src, err := fetch.ParseSource(set.Source, set.Revision)
			if err != nil {
				return failure{err}
			}
			for _, sc := range scenarios {
				n, err := src.Scenario(cmd.Context(), set.Prefix, sc.Dir, dest)
				if err != nil {
					return failure{fmt.Errorf("%s: %w", sc.ID, err)}
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %d files\n", sc.ID, n)
			}
			return nil
		},
	}
	setFlag(cmd, &setPath, &only)
	cmd.Flags().StringVar(&dest, "dest", "", "directory to download into")
	_ = cmd.MarkFlagRequired("dest")
	return cmd
}

func newPrepareCommand() *cobra.Command {
	var setPath, rawDir, out string
	var only []string
	cmd := &cobra.Command{
		Use:   "prepare",
		Short: "Turn downloaded scenarios into datasets cut at the alert, without the benchmark's traces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			set, scenarios, err := loadSelected(setPath, only)
			if err != nil {
				return failure{err}
			}
			for _, sc := range scenarios {
				d, err := prepare.Run(filepath.Join(rawDir, sc.Dir), filepath.Join(out, sc.ID), set.Namespace)
				if err != nil {
					return failure{fmt.Errorf("%s: %w", sc.ID, err)}
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: alert %s; %d changes, %d objects, %d events, %d logs, %d error spans (traces end %s), %d service and %d pod metric points\n",
					sc.ID, d.Meta.AlertTime.Format(time.RFC3339), len(d.Changes), len(d.Objects), len(d.Events), len(d.Logs), len(d.Spans),
					d.Meta.TracesUntil.Format(time.RFC3339), len(d.Services), len(d.Pods))
			}
			return nil
		},
	}
	setFlag(cmd, &setPath, &only)
	cmd.Flags().StringVar(&rawDir, "raw", "", "directory that fetch downloaded into")
	cmd.Flags().StringVar(&out, "out", "", "directory to write the datasets into, one per scenario id")
	_ = cmd.MarkFlagRequired("raw")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func newServeCommand() *cobra.Command {
	var dir, addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve one dataset through read-only MCP tools at /mcp",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := dataset.Load(dir)
			if err != nil {
				return failure{err}
			}
			srv := &http.Server{Addr: addr, Handler: server.Handler(d), ReadHeaderTimeout: 10 * time.Second}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			errc := make(chan error, 1)
			go func() { errc <- srv.ListenAndServe() }()
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "serving %s at http://%s/mcp\n", dir, addr)
			select {
			case err := <-errc:
				return failure{err}
			case <-ctx.Done():
				shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return srv.Shutdown(shutdown) //nolint:contextcheck // the command's context is already done.
			}
		},
	}
	cmd.Flags().StringVar(&dir, "dataset", "", "a dataset that prepare wrote")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "address to listen on")
	_ = cmd.MarkFlagRequired("dataset")
	return cmd
}

func newBaselineCommand() *cobra.Command {
	var setPath, datasets, scope string
	var only []string
	cmd := &cobra.Command{
		Use:   "baseline",
		Short: "Write the most-recent-change baseline's answers as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if scope != "namespace" && scope != "service" {
				return fmt.Errorf("--scope must be namespace or service, not %q", scope)
			}
			set, scenarios, err := loadSelected(setPath, only)
			if err != nil {
				return failure{err}
			}
			answers := score.Answers{}
			for _, sc := range scenarios {
				d, err := dataset.Load(filepath.Join(datasets, sc.ID))
				if err != nil {
					return failure{fmt.Errorf("%s: %w", sc.ID, err)}
				}
				service, namespace := baseline.Evaluate(d, set.FlagStore)
				if k := map[string]*score.Key{"service": service, "namespace": namespace}[scope]; k != nil {
					answers[sc.ID] = *k
				}
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(answers); err != nil {
				return failure{err}
			}
			return nil
		},
	}
	setFlag(cmd, &setPath, &only)
	cmd.Flags().StringVar(&datasets, "datasets", "", "directory that prepare wrote into")
	cmd.Flags().StringVar(&scope, "scope", "namespace", "namespace: the latest change anywhere; service: the latest change to an alerting service")
	_ = cmd.MarkFlagRequired("datasets")
	return cmd
}

func newScoreCommand() *cobra.Command {
	var setPath, answersPath, baselinePath string
	cmd := &cobra.Command{
		Use:   "score",
		Short: "Score answers against the set's keys, next to the baseline's",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			set, err := score.Load(setPath)
			if err != nil {
				return failure{err}
			}
			answers, err := readAnswers(set, answersPath)
			if err != nil {
				return failure{err}
			}
			base := score.Answers{}
			if baselinePath != "" {
				if base, err = readAnswers(set, baselinePath); err != nil {
					return failure{err}
				}
			}
			r := score.Compare(set, answers, base)
			return printReport(cmd, &r)
		},
	}
	cmd.Flags().StringVar(&setPath, "set", "scenarios.yaml", "the scenario set")
	cmd.Flags().StringVar(&answersPath, "answers", "", "JSON object mapping scenario ids to keys")
	cmd.Flags().StringVar(&baselinePath, "baseline", "", "the baseline's answers, in the same form")
	_ = cmd.MarkFlagRequired("answers")
	return cmd
}

func setFlag(cmd *cobra.Command, setPath *string, only *[]string) {
	cmd.Flags().StringVar(setPath, "set", "scenarios.yaml", "the scenario set")
	cmd.Flags().StringSliceVar(only, "only", nil, "only these scenario ids")
}

func loadSelected(path string, only []string) (*score.Set, []score.Scenario, error) {
	set, err := score.Load(path)
	if err != nil {
		return nil, nil, err
	}
	scenarios, err := selected(set, only)
	return set, scenarios, err
}

// readAnswers reads a JSON object of scenario ids to keys, and refuses ids
// that are not in the set.
func readAnswers(set *score.Set, path string) (score.Answers, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var answers score.Answers
	if err := json.Unmarshal(data, &answers); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for id := range answers {
		if _, err := selected(set, []string{id}); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return answers, nil
}

func printReport(cmd *cobra.Command, r *score.Report) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
	yes := map[bool]string{true: "yes", false: "no"}
	_, _ = fmt.Fprintln(w, "scenario\tchange\tanswer\thit\tbaseline\thit")
	for _, row := range r.Rows {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", row.ID, yes[row.Change], row.Answer, yes[row.Hit], row.Baseline, yes[row.BaselineHit])
	}
	_, _ = fmt.Fprintf(w, "\nhits %d/%d (change scenarios %d/%d); baseline %d/%d (change scenarios %d/%d); only the answers right: %d; only the baseline right: %d\n",
		r.Hits, r.Scenarios, r.ChangeHits, r.ChangeScenarios, r.BaselineHits, r.Scenarios, r.ChangeBaselineHits, r.ChangeScenarios, r.OnlyAnswers, r.OnlyBaseline)
	if err := w.Flush(); err != nil {
		return failure{err}
	}
	return nil
}
```

Create `README.md`:

````markdown
# ITBench-Lite for the mechanism check

The benchmark side of the mechanism check (spec Section 14.1): public fault scenarios, prepared so that agents can read them through read-only MCP tools that cannot see how the faults were injected, with frozen answers, a deterministic baseline and a scorer. It is not platform code: nothing in the main module imports it.

| Path | What it is |
|---|---|
| `scenarios.yaml` | The 13 scenarios, the pinned commit, and each scenario's accepted keys, fixed before any run |
| `raw/` | Readers for the benchmark's tables: tab-separated values with CSV quoting, and Python literals |
| `fetch/` | Downloads the scenarios at the pinned commit, and checks every file's hash |
| `prepare/` | Turns one scenario into a dataset: the half hour before the alert, without the benchmark's traces |
| `dataset/` | The prepared form, one JSON Lines file per stream |
| `server/` | The read-only MCP tools over one dataset |
| `score/`, `baseline/` | The scenario set, scoring by key, and the "most recent change" baseline |
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

- `fetch` lists each scenario at the commit in `scenarios.yaml` and skips files already present with the right hash, so rerunning it verifies a download.
- `prepare` refuses to write a dataset that holds a forbidden string, such as the benchmark's name or a chaos resource.
- `serve` reads only the dataset it is given. It never reads `scenarios.yaml` or the raw download.
- `baseline --scope service` limits the baseline to the services that the trigger's alerts name.
- Answers are a JSON object of scenario ids to keys, such as `{"S16": {"service": "shipping", "failureMode": "config-change"}}`.

## Tests

- Unit tests: `go test ./...`.
- On the real data, about a minute: `ITBENCH_RAW=~/itbench/raw go test -count=1 ./checks`.
- Lint: `golangci-lint run ./...` with the repository's `.golangci.yml`, two directories up.

## Data

ITBench-Lite, IBM Research, Apache-2.0: <https://huggingface.co/datasets/ibm-research/ITBench-Lite>. The prepared datasets are derived from it. `prepare` renames the benchmark's own image, node and command names (`prepare/sanitize.go`) and removes its objects and writers' names.
````

- [ ] **Step 5: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./cmd/itbench`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/cmd/itbench`.

- [ ] **Step 6: Lint**

Run: `golangci-lint run ./...`
Expected: `0 issues.`

- [ ] **Step 7: Commit**

```bash
git add bench/itbench/go.mod bench/itbench/go.sum bench/itbench/cmd/itbench/main.go bench/itbench/cmd/itbench/run.go \
  bench/itbench/cmd/itbench/commands.go bench/itbench/cmd/itbench/run_test.go bench/itbench/README.md
git commit -m "feat(bench): add the itbench command"
git push
```

---

### Task 13: The real data, the results and the roadmap

**Files:**
- Create: `bench/itbench/checks/realdata_test.go`, `bench/itbench/RESULTS.md`
- Modify: `plan/2026-10-12-implementation-roadmap.md`

**Interfaces:**
- Consumes: everything above.
- Produces: the measured facts that this plan and Plan 3b cite.

This task downloads about 11 GB. Choose a directory outside the repository; the steps use `~/itbench`.

- [ ] **Step 1: Write the real-data test**

It prepares all 13 scenarios, then checks that each change fault is visible before the alert, the baseline's scores, and that every tool answers from the real data. Create `checks/realdata_test.go`:

```go
// Package checks_test runs the pipeline on the real downloaded scenarios and
// pins what the mechanism check's plan reports about them.
package checks_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/baseline"
	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/prepare"
	"github.com/jchen0426/agent-swarm/bench/itbench/score"
	"github.com/jchen0426/agent-swarm/bench/itbench/server"
)

// TestTheRealScenarios needs ITBENCH_RAW, the directory that itbench fetch
// downloaded into. Preparing all thirteen takes about a minute.
func TestTheRealScenarios(t *testing.T) {
	rawDir := os.Getenv("ITBENCH_RAW")
	if rawDir == "" {
		t.Skip("ITBENCH_RAW is not set")
	}
	set, err := score.Load("../scenarios.yaml")
	require.NoError(t, err)
	out := t.TempDir()
	datasets := map[string]*dataset.Dataset{}
	for _, sc := range set.Scenarios {
		d, err := prepare.Run(filepath.Join(rawDir, sc.Dir), filepath.Join(out, sc.ID), set.Namespace)
		require.NoError(t, err, sc.ID)
		datasets[sc.ID] = d
	}

	t.Run("each fault that is a change is visible before the alert", func(t *testing.T) {
		for _, sc := range set.Scenarios {
			var services []string
			for _, c := range datasets[sc.ID].Changes {
				services = append(services, c.Service)
			}
			if !sc.Change {
				assert.Empty(t, services, sc.ID)
				continue
			}
			found := slices.ContainsFunc(sc.Keys, func(k score.Key) bool { return slices.Contains(services, k.Service) })
			assert.True(t, found || sc.ID == "S102", "%s: changes to %v", sc.ID, services)
		}
		quota := datasets["S102"].Changes
		require.Len(t, quota, 1)
		assert.Equal(t, "ResourceQuota", quota[0].Kind, "S102's change belongs to no service")
	})

	t.Run("the baseline's scores", func(t *testing.T) {
		service, namespace := score.Answers{}, score.Answers{}
		for _, sc := range set.Scenarios {
			s, n := baseline.Evaluate(datasets[sc.ID], set.FlagStore)
			if s != nil {
				service[sc.ID] = *s
			}
			if n != nil {
				namespace[sc.ID] = *n
			}
		}
		r := score.Compare(set, namespace, service)
		assert.Equal(t, 8, r.Hits, "namespace scope")
		assert.Equal(t, 8, r.ChangeHits, "namespace scope, change scenarios")
		assert.Equal(t, 9, r.ChangeScenarios)
		assert.Equal(t, 1, r.BaselineHits, "service scope")
	})

	t.Run("the tools answer from the real data", func(t *testing.T) {
		ctx := context.Background()
		clientSide, serverSide := mcp.NewInMemoryTransports()
		_, err := server.New(datasets["S16"]).Connect(ctx, serverSide, nil)
		require.NoError(t, err)
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "check", Version: "0"}, nil).Connect(ctx, clientSide, nil)
		require.NoError(t, err)
		defer func() { _ = cs.Close() }()
		for _, call := range []struct {
			tool string
			args map[string]any
			want string
		}{
			{"list_alerts", nil, "RequestErrorRate"},
			{"list_services", nil, "shipping"},
			{"get_changes", nil, "QUOTE_ADDR"},
			{"get_events", map[string]any{"type": "Normal"}, "shipping"},
			{"query_logs", map[string]any{"service": "frontend-proxy", "contains": "/api/checkout", "limit": 5}, "POST"},
			{"get_error_spans", map[string]any{"service": "shipping", "limit": 5}, "InvalidUri"},
			{"get_service_metrics", map[string]any{"service": "shipping"}, "max_p95_ms"},
			{"get_pod_metrics", map[string]any{"service": "shipping"}, "memory_bytes"},
			{"list_objects", map[string]any{"kind": "Pod"}, "shipping"},
			{"get_object", map[string]any{"kind": "Deployment", "name": "shipping"}, "quote:0000"},
		} {
			res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: call.tool, Arguments: call.args})
			require.NoError(t, err, call.tool)
			require.False(t, res.IsError, call.tool)
			text := res.Content[0].(*mcp.TextContent).Text
			assert.Contains(t, text, call.want, call.tool)
			t.Logf("%s: %d bytes", call.tool, len(text))
		}
	})
}
```

Run: `go test -count=1 ./checks`
Expected: `ok`, because the test skips without `ITBENCH_RAW`.

- [ ] **Step 2: Download and check the scenarios**

Run: `go build -o itbench ./cmd/itbench && ./itbench fetch --dest ~/itbench/raw`
Expected, after the download:

```text
S4: 91 files
S5: 83 files
S16: 87 files
S20: 215 files
S21: 80 files
S23: 80 files
S24: 207 files
S33: 82 files
S34: 77 files
S80: 81 files
S81: 78 files
S102: 204 files
S105: 207 files
```

All 1,572 files matched the hub's hashes. Run the command again: it downloads nothing and takes seconds, because it only checks the hashes.

- [ ] **Step 3: Run the real-data tests**

Run: `ITBENCH_RAW=~/itbench/raw go test -count=1 -timeout 20m ./checks`
Expected: `ok  	github.com/jchen0426/agent-swarm/bench/itbench/checks`, in about 40 s. Also run Task 1's table test on the smallest scenario: `ITBENCH_RAW_SCENARIO=~/itbench/raw/Scenario-81 go test -count=1 -run TestEveryRawTableParses ./raw`. Expected: `ok`.

- [ ] **Step 4: Prepare the datasets and score the baseline**

Run:

```bash
./itbench prepare --raw ~/itbench/raw --out ~/itbench/datasets
./itbench baseline --datasets ~/itbench/datasets > ~/itbench/baseline-namespace.json
./itbench baseline --datasets ~/itbench/datasets --scope service > ~/itbench/baseline-service.json
./itbench score --answers ~/itbench/baseline-namespace.json --baseline ~/itbench/baseline-service.json
```

Expected: `prepare` prints one line per scenario, such as `S16: alert 2025-12-15T17:57:59Z; 1 changes, 111 objects, 9 events, 88804 logs, 2111 error spans (traces end 2025-12-15T17:57:58Z), 2447 service and 631 pod metric points`. `score`, with the namespace scope as the answers and the service scope as the baseline, ends with:

```text
hits 8/13 (change scenarios 8/9); baseline 1/13 (change scenarios 1/9); only the answers right: 7; only the baseline right: 0
```

If any number differs from `RESULTS.md` below, stop: the data or the code changed, and the plan's claims must be checked before going on.

- [ ] **Step 5: Record the results**

Create `RESULTS.md`:

```markdown
# What the pipeline found in the data

| | |
|---|---|
| Date | 2026-10-13 |
| Data | ITBench-Lite SRE v0.2 at commit `d0916b08ba421ce5e672e9ad68aa947d938dfef0` |
| Plan | `plan/2026-10-13-plan-3a-mechanism-check-data.md` |

## Download

`itbench fetch` checked all 1,572 files of the 13 scenarios, about 11 GB, against the hub's hashes at the commit: SHA-256 for files in LFS, the git blob id for the others. All matched.

## Scenarios

From `itbench prepare` and `itbench baseline`. Times are UTC on 2025-12-15. "Fault" is when the fault began: for a change, its writer's timestamp; for a chaos experiment, its first event in the `chaos-mesh` namespace, which the datasets drop. S34's fault was set inside the running server and left no record.

| Id | Change | Fault | Alert | Changes | Events | Logs | Error spans | Traces end | Baseline, namespace | Baseline, service |
|---|---|---|---|---|---|---|---|---|---|---|
| S4 | yes | 17:48:20 | 17:55:47 | 2 | 9 | 91,164 | 29 | 17:44:17, before the fault | flagd/flag-change, hit | none |
| S5 | yes | 17:47:32 | 17:54:27 | 2 | 12 | 89,104 | 2,667 | 17:54:26 | flagd/flag-change, hit | none |
| S16 | yes | 17:52:47 | 17:57:59 | 1 | 9 | 88,804 | 2,111 | 17:57:58 | shipping/config-change, hit | shipping/config-change, hit |
| S20 | yes | 17:50:05 | 17:55:50 | 1 | 20 | 79,446 | 16,484 | 17:55:49 | product-catalog/bad-release, hit | none |
| S21 | no | 17:53:00 | 17:58:52 | 0 | 6 | 86,997 | 3 | 17:33:07, before the fault | none | none |
| S23 | yes | 17:54:47 | 18:00:13 | 1 | 25 | 76,144 | 14 | 17:42:43, before the fault | checkout/bad-release, hit | none |
| S24 | yes | 17:49:57 | 17:55:17 | 1 | 13 | 80,231 | 19 | 17:38:46, before the fault | checkout/config-change, hit | none |
| S33 | yes | 17:54:51 | 17:59:41 | 2 | 6 | 88,791 | 7 | 17:36:13, before the fault | cart/config-change, hit | none |
| S34 | no | unknown | 17:53:39 | 0 | 2 | 67,172 | 1,335 | 17:53:38 | none | none |
| S80 | no | 17:54:00 | 17:59:26 | 0 | 3 | 99,495 | 1,607 | 17:50:40, before the fault | none | none |
| S81 | no | 17:58:00 | 18:02:48 | 0 | 0 | 76,732 | 10 | 17:44:07, before the fault | none | none |
| S102 | yes | 17:54:21 | 17:59:18 | 1 | 14 | 74,781 | 15 | 17:53:49, before the fault | other/config-change, miss | none |
| S105 | yes | 17:54:13 | 17:59:15 | 1 | 21 | 67,418 | 12 | 17:39:31, before the fault | product-catalog/config-change, hit | none |

The baseline scores 8 of 13 in the namespace scope, 8 of the 9 change scenarios, and 1 of 13 in the service scope.

## Findings

- **The trigger.** Each scenario holds exactly one `alerts_in_alerting_state_*.json`: the alerts in alerting state when the benchmark started its agent. Its one to six alerts name up to four services. In S33 the only alert is `PendingPodsDetected`, which names no service, so the service-scoped baseline has nothing to search.
- **Noisy alerts.** `RequestErrorRate` fires above an error rate of 0, so it also fires before the faults, on background errors. Standing alerts such as `KubeClientCertificateExpiration` fire throughout.
- **Every fault that is a change is visible before the alert.** Each has a snapshot after it and at or before the alert, with S33 the closest: snapshot at 17:59:37, alert at 17:59:41. So every stream is cut at the alert, with no look-ahead.
- **The changes in the window are exactly the injections.** The benchmark's own setup writes, such as resetting the flags, fall before the window. A flag fault is the flag change plus a restart of the flag's service a second or two later. The baseline skips restarts, so it names the flag change.
- **S33 changed two Deployments.** ad and cart each got a node selector that matches no node, 8 s apart. The ground truth names only ad. Either counts as a hit, and the baseline names cart, the later one.
- **S102's change belongs to no service.** It created a namespace memory quota, and the ad pod could not be recreated under it. The baseline maps it to `other`, which is never a hit.
- **Traces end before the fault in 9 scenarios.** The benchmark's trace tables stop early. Each dataset records where its traces end, and `get_error_spans` returns it with every answer, so that an absence of errors is not read as evidence.
- **Logs.** All severities are kept. S16's evidence is in INFO logs and in the proxy's access logs, which carry no severity; it has no WARN or ERROR record at all.
- **Values that named the fault.** The injections used names such as `hello-bench-invalid`, `invalid-node` and `invalid-command`. `prepare` renames them to plausible names that fail the same way (`prepare/sanitize.go`).

## Tool answers

On S16, from `checks/realdata_test.go`, each tool's answer for a typical call is 0.3–6 KB of JSON: `get_changes` 252 bytes, `get_pod_metrics` 6,138 bytes, the others about 2–3 KB.
```

- [ ] **Step 6: Update the roadmap**

In `plan/2026-10-12-implementation-roadmap.md`:

- In the header table, after the `Plan 2` row, add a row `Plan 3a` whose value is the file name `2026-10-13-plan-3a-mechanism-check-data.md` followed by `(executed)`.
- In the Plan 3 row's "Delivers" cell, add at the start: `In two parts. 3a, which needs no model: the benchmark's scenarios, prepared for read-only MCP tools that cannot see the injections; the frozen keys, the baseline and the scorer; the pass criteria. 3b: the rest.`
- In Section 5, replace `Plan 3 is written next.` with `Plan 3a was executed on <the date>: the mechanism check's data, tools, answers, baseline and scorer, with its pass criteria fixed. Plan 3b, the agent runs, waits for model access.`

- [ ] **Step 7: Run everything**

Run: `rm itbench && go test -count=1 ./... && golangci-lint run ./...`
Expected: every package `ok`, `checks` skipping without `ITBENCH_RAW`, and `0 issues.`

- [ ] **Step 8: Commit**

From the repository root:

```bash
git add bench/itbench/checks/realdata_test.go bench/itbench/RESULTS.md plan/2026-10-12-implementation-roadmap.md
git commit -m "docs(bench): record what the mechanism check's data holds"
git push
```

---

## Changes made during execution

The code blocks above are the code before the final review. The repository holds the final code: commit `dc6c6ca` and the documents after it.

**Task 13.** The data went into `~/itbench/raw` as an APFS clone of the copy that verification had downloaded and checked, instead of a second 11 GB download. `itbench fetch` then checked all 1,572 files against the commit and downloaded nothing, which is the step's own "run it again" check.

**The final review** (a fresh reviewer, on 01efc2c..6f370b0) found no Critical issue and five Important ones. Four Minor ones were re-graded to Important by their effect. Each fix began with a test that failed:

| Finding | Fix | Test |
|---|---|---|
| Deleted objects were served as they last looked: S102's ad pod showed "Running; ready 1/1" | An object the last pull did not list is left out. 8 pods, each with a `SuccessfulDelete` event | `TestAnObjectMissingFromTheLastPullIsGone`; the real-data check |
| The minute of the alert read zero traffic in S4, S20 and S34, and the first minute in four scenarios | Only whole minutes are kept, after the table's first scrape; repeated rows are skipped | `TestServiceMetricsKeepOnlyCompleteMinutes`, `TestDuplicateSamplesAddNothing`, `TestTheTablesFirstMinuteIsLeftOut` |
| A series' first value was dropped, so a fault's first errors were lost (S4's frontend: 9 counted of 25) | A series that appears after the table's first scrape counts its first value | `TestANewSeriesCountsItsFirstSample` |
| `list_services` gave call counts from traces without saying where the traces end | It returns `traces_until` | `TestListServicesSaysWhereTracesEnd` |
| An unknown service, kind, pod or object returned an empty answer, which reads as "no traffic" | It is a tool error that lists the valid names; kinds match in any case | `TestUnknownNamesAreToolErrors`, `TestKindsMatchWhateverTheirCase` |
| The MCP server was named `itbench-read`, which agents see | It is named `observability` | `TestTheServerDoesNotNameTheBenchmark` |
| Events with the same time came out in map order, so two preparations differed | Events are ordered by every field; two preparations now give the same 130 files | `TestEventsAtTheSameTimeHaveAFixedOrder` |
| Section 13.4's rater cases were not stated | The clarification under the pass criteria; `score` marks them | `TestRatersWouldJudgeWhatSection134LeavesToThem` |
| The real-data check passed as long as some change touched a key's service | It pins the exact changes, the deleted pods and whole minutes, and calls every tool on all 13 datasets. Dropping S33's cart change made it fail | `TestTheRealScenarios` |

Corrections:
- S33's change is visible by less than a second, not 4 s: its Deployments were snapshotted at 17:59:40.13.
- Task 13's expected `prepare` line for S16 now reads `110 objects` and `2362 service and 631 pod metric points`. The `score` summary ends with `answers that raters would judge, counted as misses: 1`: the baseline's S102 answer, `other/config-change`.

Left as the data has it: inside the window, a minute can read zero and the next one double, when a counter's once-a-minute flush falls just past a minute's last scrape. This happens 3 times in S20, once in S33 and twice in S80. Smoothing would invent data, so `get_service_metrics`' description tells agents instead.

Deferred minor findings:
- A write in the same second just after a snapshot would be dropped (`lastWrite`), because writers' times have whole seconds. No case in the pinned data.
- The trigger's alerts and the metric tables are not filtered by namespace. All the pinned data is `otel-demo`.
- `list_alerts` returns the earlier poll when a poll kept no alert, and a zero time before the first poll. Every poll holds standing alerts that have no namespace.
- `fetch` has no HTTP timeouts and no limit on a body's size, and the literal parser has no depth limit. These only allow denial of service, and only on data that is pinned and hash-checked.
