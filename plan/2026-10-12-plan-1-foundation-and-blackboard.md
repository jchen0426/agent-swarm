# Plan 1: Foundation and the Blackboard — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Executed on 2026-10-12. See "Changes made during execution" at the end. |
| Date | 2026-10-12 |
| Roadmap | `2026-10-12-implementation-roadmap.md`, Plan 1 |

**Goal:** Stand up the Go repository with its build, lint, test and release toolchain, and build the core of the Work context: hypothesis keys, board entries with provenance, retraction and rulings, and the rules that turn a board into stances, convergence and a conclusion, persisted in Postgres and usable from the command line.

**Architecture:** The rules are pure domain code in `internal/work/key` and `internal/work/board`. The use cases in `internal/work/app` declare the ports they need: an event store, read-call lookup, run context and identifiers. Adapters implement those ports: `internal/work/postgres` for the append-only event store, and `internal/ids` for UUIDv7 identifiers. The `agentswarm` binary wires nothing to a server yet. It offers `version`, and `conclude`, which assesses a recorded board as of a moment.

**Tech Stack:** Go 1.26; testify v1.12.0; pgx v5.10.0; testcontainers-go v0.44.0 with Postgres 17; google/uuid v1.6.0; golangci-lint v2; GoReleaser v2; GNU Make 3.81 or later.

**Spec:** `plan/2026-10-11-agent-swarm-platform-spec.md`, especially Sections 2.2 (implementation constraints), 4.2 (blackboard entries), 4.3 (keys, convergence and the conclusion), 9.2 (deterministic rules) and 12.3 (replay). Also ADR-012 and ADR-015. Read the spec sections before the tasks that implement them.

**Verified:** Every code block in this plan was compiled, tested and linted, task by task, in a scratch copy before the plan was written: each task's tests fail before its implementation exists, and pass after it, together with `golangci-lint run ./...` at 0 issues, the integration tests in containers, the end-to-end tests and `goreleaser check`.

## Global Constraints

- **Module and Go version.** The module path is `github.com/jchen0426/agent-swarm`. go.mod says `go 1.26.0`, which matches `run.go: "1.26"` in `.golangci.yml`.
- **Vocabulary.** Names come from the spec: agent (never "employee"), run, board, entry, evidence, hypothesis, stance, contributor, source, convergence, conclusion, ruling, retraction.
- **Domain purity.** `internal/work/key` and `internal/work/board` import no database, network or framework package (spec Section 2.2).
- **Ports.** Each port is declared by its consumer (`internal/work/app`) and implemented by an adapter (`internal/work/postgres`, `internal/ids`). Wiring lives in `cmd/` (spec Section 2.2).
- **Append-only.** Board entries are append-only (spec Section 4.2), and the database enforces it.
- **Tenant.** Records carry a `tenant_id` (spec Section 1.4).
- **Convergence defaults.** [2] contributors, [2] sources, and a refutation margin of [2] (spec Section 15).
- **Time.** Commit times are database time, and never go backwards within a run (ADR-004).
- **Lint.** Every task ends with `golangci-lint run ./...` at `0 issues.`. The `.golangci.yml` of Task 1 is the contract.
- **Docker.** Integration tests start Postgres containers, so Docker must be running. Each test package removes its own container, so the plan does not depend on the Ryuk reaper.

## Decisions this plan makes where the spec is silent

Review these first. Each is pinned by the named test.

1. **A hypothesis's author supports it.** Posting a hypothesis records the author's `supports` stance. That stance is backed by the cited evidence that the author wrote. Evidence written by others only grounds the hypothesis. (`TestHypothesisAuthorSupportNeedsLiveEvidence`)
2. **Retraction falls back.** When a contributor's latest stance entry is retracted, their previous live stance counts again. (`TestRetractedEvidenceStopsCounting`)
3. **Sources.** Every live supporting entry of a current supporter counts toward distinct sources, not only their latest one. (`TestConvergenceNeedsTwoContributorsAndTwoSources`)
4. **A conclusion needs a supporter.** When nothing has converged, a hypothesis that nobody supports is not a conclusion. (`TestConclusionWithoutConvergence`)
5. **A dismissal binds to one entry.** It lifts one refuting entry. A later refutation by the same contributor stands again until it, too, is dismissed. A dismissed refuter still counts in the score. (`TestHumanRefutationStandsUntilDismissed`)
6. **A dismissal outlives its cause.** Human support that lifts an agent's refutation records the dismissal. The refutation stays dismissed even if the margin later shrinks. (`TestHumanSupportThatLiftsARefutationIsADismissal`)
7. **An empty key schema never merges.** Otherwise every hypothesis would merge into the first one. (`TestMergeable`)
8. **Size limits.** A claim may be at most 2,000 characters, a quote 2,000, a statement 1,000 and a reason's text 500. A hypothesis cites at most 20 entries. (`TestClaimLength`, `TestAgentEvidenceCitesItsOwnReadCall`)
9. **Who may post what.** Humans neither retract nor post hypotheses, as in the spec's entry table. Platform evidence carries no stance. (`TestRetraction`, `TestHypothesisRules`)
10. **Quote matching.** A quote matches if, after whitespace is normalized, it appears in the raw output. For an output that is one JSON document, it may instead appear in the decoded text, which lists keys and values with strings unescaped. (`TestQuoteAppears`)
11. **Rulings in this plan.** Reject, unreject and dismiss a refutation. Rulings that act on work items (close, reopen, extend, adoption, milestone, link) come with the coordinator plan.
12. **Deletion.** Updates are never allowed. Deletes are allowed only in a transaction that sets `agentswarm.retention = 'on'`, which the retention job will use. (`TestBoardEventsAreAppendOnly`)

## Review Focus

Inputs and failure modes the spec implies, most likely to bite first. Each has a test in the task that owns the code.

1. **A cutoff in a local time zone, or exactly at a commit time.** It means the same instant, and an entry committed at the cutoff counts. (`TestAsOfIncludesAnEntryCommittedAtTheCutoff` in Task 6; `TestConcludeAcceptsACutoffWithAnOffset` in Task 10)
2. **A rejected claim posted again under an equal key, or evidence that cites an alias.** The claim stays rejected, and the evidence counts for the merged hypothesis. (`TestANewAliasOfARejectedHypothesisStaysRejected` and `TestAliasesCountForTheMergedHypothesis` in Task 6)
3. **Non-ASCII text and Unicode white space in tool output.** Quotes still match. (`TestQuoteAppearsAcrossUnicodeSpaceAndText` in Task 4)
4. **Escaped JSON output** (`\u003c`, `\"`, `\n`). Quotes of the decoded text match. (`TestQuoteAppearsInEscapedJSON` in Task 4)
5. **Two writers appending to one run at once.** Each entry gets a distinct sequence number, commit times never go backwards, and nothing is lost. (`TestConcurrentAppendsGetDistinctSequenceNumbers` in Task 9)

## Not in this plan

- **Blackboard tools that depend on rounds:** entry limits, idempotent repeats, tasks and claims, `end_turn`, `read_board` and the wrap-up rules. They come in Plan 5, with the gateway that exposes them.
- **Proposals, platform evidence and item-level rulings:** Plans 5 and 7.
- **A server process and its wiring:** Plan 7.

## File Structure

| Path | Responsibility |
|---|---|
| `Makefile` | Build, test, lint and release entry points |
| `.golangci.yml` | The lint contract, including the black-box import guard for `test/e2e` |
| `.goreleaser.yaml` | Release archives for linux and darwin, amd64 and arm64 |
| `cmd/agentswarm/main.go`, `run.go` | The binary: command dispatch, exit codes and JSON output |
| `cmd/agentswarm/conclude.go` | `agentswarm conclude`: assess a recorded board as of a moment |
| `internal/versioninfo/` | Version, commit and date of the build |
| `internal/work/key/` | Hypothesis keys: closed value sets, `other`, equality and mergeability |
| `internal/work/board/board.go` | Entry types, authors, sources, inputs and settings |
| `internal/work/board/quote.go` | Quote matching against stored read-call output |
| `internal/work/board/state.go` | The folded board: replay and the rules every entry satisfies |
| `internal/work/board/post.go` | Checks for new posts: evidence, hypotheses, retractions and rulings |
| `internal/work/board/assess.go` | Stances, standing refutations, convergence, ranking and the conclusion |
| `internal/work/app/boards.go` | Board use cases and the ports they declare |
| `internal/ids/` | UUIDv7 identifiers |
| `internal/db/` | Connection, embedded migrations and the `board_events` table |
| `internal/testsupport/pgtest/` | One Postgres container per test package |
| `internal/work/postgres/` | The Postgres event store |
| `test/e2e/` | Black-box tests of the built binary |


## Tasks

| Task | Delivers |
|---|---|
| 1 | Repository toolchain and the version command |
| 2 | End-to-end test harness |
| 3 | Hypothesis keys |
| 4 | Board entries and quote matching |
| 5 | Board rules: replay, posting, retraction and rulings |
| 6 | Stances, convergence and the conclusion |
| 7 | Board use cases and the id generator |
| 8 | Database connection, migrations and test containers |
| 9 | Postgres event store |
| 10 | The conclude command |

---

### Task 1: Repository toolchain and the version command

**Files:**
- Create: `Makefile`
- Create: `.golangci.yml`
- Create: `.goreleaser.yaml`
- Create: `internal/versioninfo/versioninfo.go`
- Create: `cmd/agentswarm/main.go`
- Create: `cmd/agentswarm/run.go`
- Test: `internal/versioninfo/versioninfo_test.go`
- Test: `cmd/agentswarm/run_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `versioninfo.Get() versioninfo.Info`, where `Info` has `Version`, `Commit` and `Date` strings;
  - in `cmd/agentswarm`, `run(args []string, stdout, stderr io.Writer) int` with exit codes `exitOK` (0), `exitError` (1) and `exitUsage` (2), and `report(stdout, stderr io.Writer, v any, err error) int`;
  - Makefile targets: `build`, `test`, `test-integration`, `test-e2e`, `test-all`, `lint`, `fmt`, `tidy`, `release-check`, `snapshot` and `clean`.

- [ ] **Step 1: Initialize the module and add the test library**

```bash
go mod init github.com/jchen0426/agent-swarm
go mod edit -go=1.26.0
go get github.com/stretchr/testify/assert@v1.12.0 github.com/stretchr/testify/require@v1.12.0
```

- [ ] **Step 2: Add the toolchain configuration**

Create `Makefile`:

```makefile
# Build, test and lint entry points. Release archives come from GoReleaser.
# Written for GNU Make 3.81, the version macOS ships.

GO            ?= go
GOLANGCI_LINT ?= golangci-lint
GORELEASER    ?= goreleaser

MODULE  := github.com/jchen0426/agent-swarm
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X $(MODULE)/internal/versioninfo.version=$(VERSION) \
	-X $(MODULE)/internal/versioninfo.commit=$(COMMIT) \
	-X $(MODULE)/internal/versioninfo.date=$(DATE)

BIN := bin/agentswarm

.PHONY: all build test test-integration test-e2e test-all lint fmt tidy release-check snapshot clean

all: lint test build

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/agentswarm

# Unit tests: no network, no containers.
test:
	$(GO) test -race -count=1 ./...

# Unit and integration tests. Integration tests start containers, so Docker must run.
test-integration:
	$(GO) test -race -count=1 -tags integration ./...

# End-to-end tests drive the built binary from outside.
test-e2e: build
	AGENTSWARM_BIN=$(CURDIR)/$(BIN) $(GO) test -count=1 -tags e2e ./test/e2e/...

test-all: test-integration test-e2e

lint:
	$(GOLANGCI_LINT) run ./...

fmt:
	$(GOLANGCI_LINT) fmt ./...

tidy:
	$(GO) mod tidy

release-check:
	$(GORELEASER) check

snapshot:
	$(GORELEASER) release --snapshot --clean

clean:
	rm -rf bin dist
```

Create `.golangci.yml`:

```yaml
version: "2"

run:
  timeout: 10m
  modules-download-mode: readonly
  go: "1.26"
  # Integration and end-to-end tests sit behind build tags. Without the tags
  # the linters never parse them, and they rot silently.
  build-tags:
    - integration
    - e2e

linters:
  enable:
    - govet
    - errcheck
    - staticcheck
    - ineffassign
    - gocritic
    - gocyclo
    - dupl
    - misspell
    - unconvert
    - gosec
    - revive
    - unused
    - usestdlibvars
    - whitespace
    - testifylint
    - depguard
    - errorlint
    - predeclared
    - perfsprint
    - nilnesserr
    - godot
    - forbidigo
    - fatcontext
    - sloglint

  exclusions:
    paths:
      - ".*\\.pb\\.go$"
      - ".*_mock\\.go$"
    rules:
      # Test files are not checked for complexity, duplication or unchecked errors.
      - linters:
          - gocyclo
          - errcheck
          - dupl
          - gosec
          - perfsprint
        path: _test.go
      - linters:
          - forbidigo
        path: ^scripts/

  settings:
    sloglint:
      # When a context.Context is in scope, log calls must use the *Context
      # variants, so that request-scoped values reach the log handler.
      context: scope
    gocyclo:
      min-complexity: 15

    gocritic:
      enabled-tags:
        - diagnostic
        - experimental
        - opinionated
        - performance
        - style

    depguard:
      rules:
        main:
          deny:
            - pkg: "io/ioutil"
              desc: "Use 'os' or 'io' functions instead"
            - pkg: "github.com/pkg/errors"
              desc: "Use 'errors' or 'fmt' instead"
            - pkg: "github.com/golang/mock"
              desc: "Use 'go.uber.org/mock' instead (golang/mock is deprecated)"
            - pkg: "github.com/golang/mock/gomock"
              desc: "Use 'go.uber.org/mock/gomock' instead"
            - pkg: "github.com/golang/mock/mockgen"
              desc: "Use 'go.uber.org/mock/mockgen' instead"
        # End-to-end tests are black-box: they drive the built binary over its
        # public interfaces and never import platform internals. depguard
        # matches absolute file paths, so each glob starts with `**/`. Two
        # globs are needed because `**` matches one or more path segments, so
        # `**/test/e2e/**/*.go` alone misses the top-level files.
        e2e:
          files:
            - "**/test/e2e/*.go"
            - "**/test/e2e/**/*.go"
          allow:
            - $gostd
            - github.com/stretchr/testify
            - github.com/testcontainers/testcontainers-go
          deny:
            - pkg: "github.com/jchen0426/agent-swarm/internal"
              desc: "e2e is black-box; drive the binary, not internal types"
            - pkg: "github.com/jackc/pgx"
              desc: "no direct database access from e2e"
            - pkg: "database/sql"
              desc: "no direct database access from e2e"

    forbidigo:
      forbid:
        - pattern: ^fmt\.Print(f|ln)?$
          msg: "use log/slog structured logging instead of fmt.Print"

    errorlint:
      errorf: true
      asserts: true
      comparison: true

    revive:
      rules:
        - name: error-naming
        - name: error-strings
        - name: error-return
        - name: errorf
        - name: context-as-argument
        - name: var-naming
        - name: increment-decrement
        - name: var-declaration
        - name: indent-error-flow
        - name: superfluous-else

    gosec:
      excludes:
        - G118

    staticcheck:
      checks:
        - all

formatters:
  enable:
    - gci
  settings:
    gci:
      custom-order: true
      sections:
        - standard
        - blank
        - default
        - localmodule

issues:
  max-same-issues: 0
  max-issues-per-linter: 0

output:
  formats:
    text:
      print-linter-name: true
  sort-results: true
  sort-order:
    - linter
    - severity
```

Create `.goreleaser.yaml`:

```yaml
# GoReleaser builds the release archives of the agentswarm binary.
version: 2

project_name: agentswarm

before:
  hooks:
    - go mod download

builds:
  - id: agentswarm
    main: ./cmd/agentswarm
    binary: agentswarm
    env:
      - CGO_ENABLED=0
    goos:
      - linux
      - darwin
    goarch:
      - amd64
      - arm64
    flags:
      - -trimpath
    ldflags:
      - -s -w
      - -X github.com/jchen0426/agent-swarm/internal/versioninfo.version={{ .Version }}
      - -X github.com/jchen0426/agent-swarm/internal/versioninfo.commit={{ .FullCommit }}
      - -X github.com/jchen0426/agent-swarm/internal/versioninfo.date={{ .CommitDate }}
    mod_timestamp: "{{ .CommitTimestamp }}"

archives:
  - formats:
      - tar.gz
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"

checksum:
  name_template: checksums.txt

snapshot:
  version_template: "{{ incpatch .Version }}-snapshot"

changelog:
  disable: true
```

- [ ] **Step 3: Write the failing tests**

Create `internal/versioninfo/versioninfo_test.go`:

```go
package versioninfo_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/versioninfo"
)

func TestGetWithoutLinkerFlagsReportsDev(t *testing.T) {
	info := versioninfo.Get()

	assert.Equal(t, "dev", info.Version)
}
```

Create `cmd/agentswarm/run_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runCommand(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestVersion(t *testing.T) {
	out, _, code := runCommand("version")

	require.Equal(t, exitOK, code)
	var info map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &info))
	assert.Equal(t, "dev", info["version"])
}

func TestUsageErrors(t *testing.T) {
	_, errOut, code := runCommand()
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "Usage: agentswarm")

	_, errOut, code = runCommand("frobnicate")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, `unknown command "frobnicate"`)
}
```

- [ ] **Step 4: Run the tests and watch them fail**

Run: `go test ./...`

Expected: FAIL: `internal/versioninfo: no non-test Go files`, and in `cmd/agentswarm`, `undefined: run`.

- [ ] **Step 5: Write the implementation**

Create `internal/versioninfo/versioninfo.go`:

```go
// Package versioninfo reports the version, commit and build date of the running binary.
package versioninfo

import "runtime/debug"

// The linker sets these with -ldflags "-X" (see the Makefile and .goreleaser.yaml).
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// Info describes one build of the binary.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Get returns the build information. When the linker did not set the commit,
// it falls back to the version control data that the Go toolchain records.
func Get() Info {
	info := Info{Version: version, Commit: commit, Date: date}
	if info.Commit != "" {
		return info
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Commit = s.Value
		case "vcs.time":
			info.Date = s.Value
		}
	}
	return info
}
```

Create `cmd/agentswarm/main.go`:

```go
// Command agentswarm is the Agent Swarm platform binary.
package main

import "os"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
```

Create `cmd/agentswarm/run.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jchen0426/agent-swarm/internal/versioninfo"
)

const usage = `Usage: agentswarm <command> [flags]

Commands:
  version    Print the build version as JSON.
`

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// run executes one command and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "version":
		return report(stdout, stderr, versioninfo.Get(), nil)
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return exitOK
	default:
		//nolint:gosec // G705 targets HTML responses; this is a terminal, and %q escapes control characters.
		_, _ = fmt.Fprintf(stderr, "agentswarm: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

// report writes v as indented JSON, or err to stderr.
func report(stdout, stderr io.Writer, v any, err error) int {
	if err == nil {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(v)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agentswarm: %v\n", err)
		return exitError
	}
	return exitOK
}
```

- [ ] **Step 6: Run the tests and the linter**

Run: `go mod tidy`

Expected: go.mod lists testify as a direct requirement.

Run: `go test ./...`

Expected: PASS for `cmd/agentswarm` and `internal/versioninfo`.

Run: `golangci-lint run ./...`

Expected: `0 issues.`

Run: `make build && ./bin/agentswarm version`

Expected: JSON with `version`, `commit` and `date` taken from Git.

Run: `make release-check`

Expected: `1 configuration file(s) validated`.

- [ ] **Step 7: Commit**

```bash
git add .golangci.yml \
  .goreleaser.yaml \
  Makefile \
  cmd/agentswarm/main.go \
  cmd/agentswarm/run.go \
  cmd/agentswarm/run_test.go \
  go.mod \
  go.sum \
  internal/versioninfo/versioninfo.go \
  internal/versioninfo/versioninfo_test.go
git commit -m "build: add the Go toolchain and the version command"
```

---

### Task 2: End-to-end test harness

**Files:**
- Test: `test/e2e/main_test.go`
- Test: `test/e2e/cli_test.go`

**Interfaces:**
- Consumes: the binary from Task 1, through `make test-e2e`, which builds it and passes its path in `AGENTSWARM_BIN`. Without that variable, `TestMain` builds a fresh copy.
- Produces: `agentswarm(t *testing.T, args ...string) (stdout, stderr string, code int)` in package `e2e`.

- [ ] **Step 1: Write the harness and its tests**

Create `test/e2e/main_test.go`:

```go
//go:build e2e

// Package e2e drives the built agentswarm binary from outside, as a user does.
// It imports no platform package; a depguard rule enforces that.
package e2e

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// binary is the agentswarm binary under test: AGENTSWARM_BIN when set (the
// Makefile sets it to the artifact it built), otherwise a fresh build.
var binary string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	if b := os.Getenv("AGENTSWARM_BIN"); b != "" {
		binary = b
		return m.Run()
	}
	dir, err := os.MkdirTemp("", "agentswarm-e2e-")
	if err != nil {
		slog.Error("create a build directory", "error", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	binary = filepath.Join(dir, "agentswarm")
	build := exec.Command("go", "build", "-o", binary, "github.com/jchen0426/agent-swarm/cmd/agentswarm")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		slog.Error("build agentswarm", "error", err)
		return 1
	}
	return m.Run()
}

// agentswarm runs the binary and returns its output and exit code.
func agentswarm(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.String(), errOut.String(), exit.ExitCode()
	}
	require.NoError(t, err)
	return out.String(), errOut.String(), 0
}
```

Create `test/e2e/cli_test.go`:

```go
//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionPrintsBuildInfo(t *testing.T) {
	out, errOut, code := agentswarm(t, "version")

	require.Equal(t, 0, code, errOut)
	var info struct {
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &info))
	assert.NotEmpty(t, info.Version)
}

func TestUnknownCommandExitsWithUsage(t *testing.T) {
	_, errOut, code := agentswarm(t, "frobnicate")

	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, `unknown command "frobnicate"`)
}
```

- [ ] **Step 2: Run the end-to-end tests**

Run: `make test-e2e`

Expected: PASS. These tests pin behavior that Task 1 built, so they pass at once.

- [ ] **Step 3: Prove that the black-box import guard works**

Create a throwaway file `test/e2e/guard_probe_test.go`:

```go
//go:build e2e

package e2e

import (
	"testing"

	"github.com/jchen0426/agent-swarm/internal/versioninfo"
)

func TestGuardProbe(t *testing.T) { _ = versioninfo.Get() }
```

Run: `golangci-lint run ./test/e2e/...`

Expected: one depguard error, `import 'github.com/jchen0426/agent-swarm/internal/versioninfo' is not allowed from list 'e2e'`.

Then delete the probe: `rm test/e2e/guard_probe_test.go`. Run `golangci-lint run ./...`. Expected: `0 issues.`

- [ ] **Step 4: Commit**

```bash
git add test/e2e/cli_test.go \
  test/e2e/main_test.go
git commit -m "test: add the end-to-end harness for the binary"
```

---

### Task 3: Hypothesis keys

**Files:**
- Create: `internal/work/key/key.go`
- Test: `internal/work/key/key_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces, in package `key`:
  - `const Other = "other"`;
  - `ErrMissingField`, `ErrUnknownField` and `ErrValue`;
  - `type Field struct { Name string; Values []string }`, `type Schema struct { Fields []Field }` and `type Key map[string]string`;
  - `(Schema) Validate(Key) error`, `(Schema) Mergeable(Key) bool`, `(Key) Equal(Key) bool` and `(Key) String() string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/work/key/key_test.go`:

```go
package key_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/key"
)

func incidentSchema() key.Schema {
	return key.Schema{Fields: []key.Field{
		{Name: "service", Values: []string{"checkout", "payments"}},
		{Name: "failureMode", Values: []string{"bad-deploy", "config-change"}},
	}}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		k    key.Key
		want error
	}{
		{"valid", key.Key{"service": "checkout", "failureMode": "bad-deploy"}, nil},
		{"other is always allowed", key.Key{"service": key.Other, "failureMode": key.Other}, nil},
		{"missing field", key.Key{"service": "checkout"}, key.ErrMissingField},
		{"unknown field", key.Key{"service": "checkout", "failureMode": "bad-deploy", "region": "eu"}, key.ErrUnknownField},
		{"value outside the set", key.Key{"service": "search", "failureMode": "bad-deploy"}, key.ErrValue},
		{"values are case sensitive", key.Key{"service": "Checkout", "failureMode": "bad-deploy"}, key.ErrValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := incidentSchema().Validate(tt.k)
			if tt.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestEmptyClosedSetAcceptsOnlyOther(t *testing.T) {
	s := key.Schema{Fields: []key.Field{{Name: "failureMode"}}}

	require.NoError(t, s.Validate(key.Key{"failureMode": key.Other}))
	require.ErrorIs(t, s.Validate(key.Key{"failureMode": "bad-deploy"}), key.ErrValue)
}

func TestMergeable(t *testing.T) {
	s := incidentSchema()

	assert.True(t, s.Mergeable(key.Key{"service": "checkout", "failureMode": "bad-deploy"}))
	assert.False(t, s.Mergeable(key.Key{"service": "checkout", "failureMode": key.Other}))
	assert.False(t, key.Schema{}.Mergeable(key.Key{}), "a schema without fields never merges")
}

func TestEqualAndString(t *testing.T) {
	a := key.Key{"service": "checkout", "failureMode": "bad-deploy"}
	b := key.Key{"failureMode": "bad-deploy", "service": "checkout"}

	assert.True(t, a.Equal(b))
	assert.Equal(t, `failureMode="bad-deploy",service="checkout"`, a.String())
	assert.Equal(t, a.String(), b.String())
	assert.False(t, a.Equal(key.Key{"service": "checkout", "failureMode": "config-change"}))
	assert.NotEqual(t, key.Key{"service": "a,b"}.String(), key.Key{"service": "a", "b": ""}.String())
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/work/key/`

Expected: FAIL: `internal/work/key: no non-test Go files`.

- [ ] **Step 3: Write the implementation**

Create `internal/work/key/key.go`:

```go
// Package key models hypothesis keys: the fields that say what a hypothesis
// claims, so that equal claims merge into one hypothesis (spec Section 4.3).
package key

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Other is the value every field accepts besides its closed set. A key that
// contains it never merges with another key.
const Other = "other"

// Errors returned by Schema.Validate.
var (
	ErrMissingField = errors.New("key: missing field")
	ErrUnknownField = errors.New("key: unknown field")
	ErrValue        = errors.New("key: value outside the field's closed set")
)

// Field is one key field and its closed set of values.
type Field struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// Schema lists the key fields of a work item type. A run locks it when its
// item starts.
type Schema struct {
	Fields []Field `json:"fields"`
}

// Key holds one value per schema field.
type Key map[string]string

// Validate checks that k has exactly the schema's fields, each with a value
// from the field's closed set or Other.
func (s Schema) Validate(k Key) error {
	for _, f := range s.Fields {
		v, ok := k[f.Name]
		if !ok {
			return fmt.Errorf("%w %q", ErrMissingField, f.Name)
		}
		if v != Other && !slices.Contains(f.Values, v) {
			return fmt.Errorf("%w: %s=%q", ErrValue, f.Name, v)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(k)) {
		if !s.hasField(name) {
			return fmt.Errorf("%w %q", ErrUnknownField, name)
		}
	}
	return nil
}

// Mergeable reports whether a hypothesis with key k merges with others that
// have an equal key. Keys that contain Other never merge, and neither do keys
// of a schema without fields.
func (s Schema) Mergeable(k Key) bool {
	if len(s.Fields) == 0 {
		return false
	}
	for _, v := range k {
		if v == Other {
			return false
		}
	}
	return true
}

func (s Schema) hasField(name string) bool {
	return slices.ContainsFunc(s.Fields, func(f Field) bool { return f.Name == name })
}

// Equal reports whether two keys have the same fields with the same values.
func (k Key) Equal(other Key) bool {
	return maps.Equal(k, other)
}

// String renders the key canonically: fields sorted by name and values quoted,
// so that equal keys render identically and different keys differently.
func (k Key) String() string {
	var b strings.Builder
	for i, name := range slices.Sorted(maps.Keys(k)) {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(strconv.Quote(k[name]))
	}
	return b.String()
}
```

- [ ] **Step 4: Run the tests and the linter**

Run: `go test ./internal/work/key/`

Expected: PASS.

Run: `golangci-lint run ./...`

Expected: `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal/work/key/key.go \
  internal/work/key/key_test.go
git commit -m "feat(work): add hypothesis keys"
```

---

### Task 4: Board entries and quote matching

**Files:**
- Create: `internal/work/board/board.go`
- Create: `internal/work/board/quote.go`
- Test: `internal/work/board/quote_test.go`

**Interfaces:**
- Consumes: `key.Key` and `key.Schema` (Task 3).
- Produces, in package `board`:
  - errors `ErrInvalid`, `ErrNotFound`, `ErrNotAllowed`, `ErrProvenance` and `ErrReasonRequired`, and the size limits `MaxClaimRunes`, `MaxQuoteRunes`, `MaxStatementRunes`, `MaxReasonRunes` and `MaxCitations`;
  - the types `RunID`, `EntryID`, `Author` (with `IsContributor()`), `AuthorKind`, `Kind`, `Stance`, `Source`, `Provenance`, `Reason`, `Evidence`, `Hypothesis`, `Retraction`, `Ruling`, `RulingKind`, `Body` (with `Kind()`), `Event`, `Draft`, `Convergence`, `Settings`, `Context`, `ReadCall`, `EvidenceInput`, `HypothesisInput` and `RulingInput`;
  - `DefaultConvergence`, `HumanObservation` and `ToolSource(server, tool string) Source`;
  - `NormalizeSpace(string) string` and `QuoteAppears(output []byte, quote string) bool`.

- [ ] **Step 1: Write the failing tests**

Create `internal/work/board/quote_test.go`:

```go
package board_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

func TestNormalizeSpace(t *testing.T) {
	assert.Equal(t, "a b c", board.NormalizeSpace("  a\t b\n\nc  "))
	assert.Empty(t, board.NormalizeSpace(" \n\t "))
}

func TestQuoteAppears(t *testing.T) {
	text := []byte("10:03:01 ERROR  checkout\n  NullPointerException in CheckoutHandler")
	jsonOutput := []byte(`{"events":[{"msg":"pool \"orders\" exhausted","count":42,"ok":false}]}`)

	tests := []struct {
		name   string
		output []byte
		quote  string
		want   bool
	}{
		{"verbatim", text, "NullPointerException in CheckoutHandler", true},
		{"white space differs", text, "ERROR checkout NullPointerException", true},
		{"not in output", text, "OutOfMemoryError", false},
		{"empty quote", text, "", false},
		{"white space only", text, " \n ", false},
		{"raw JSON fragment", jsonOutput, `"count":42`, true},
		{"decoded JSON string", jsonOutput, `pool "orders" exhausted`, true},
		{"decoded JSON key and value", jsonOutput, "count: 42", true},
		{"decoded JSON boolean", jsonOutput, "ok: false", true},
		{"invented text", jsonOutput, "pool orders healthy", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, board.QuoteAppears(tt.output, tt.quote))
		})
	}
}

func TestQuoteAppearsIgnoresTrailingDataAfterJSON(t *testing.T) {
	// Two JSON documents are not one structured output, so only the raw text counts.
	output := []byte(`{"msg":"a \"b\""} {"msg":"c"}`)

	assert.False(t, board.QuoteAppears(output, `a "b"`))
	assert.True(t, board.QuoteAppears(output, `{"msg":"c"}`))
}

func TestQuoteAppearsAcrossUnicodeSpaceAndText(t *testing.T) {
	output := []byte("Zahlung\u00a0für Bestellung 42\tfehlgeschlagen")

	assert.True(t, board.QuoteAppears(output, "Zahlung für Bestellung 42 fehlgeschlagen"))
}

func TestQuoteAppearsInEscapedJSON(t *testing.T) {
	output := []byte(`{"msg":"\u003cnil\u003e pointer\nin handler"}`)

	assert.True(t, board.QuoteAppears(output, "<nil> pointer in handler"))
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/work/board/`

Expected: FAIL: `internal/work/board: no non-test Go files`.

- [ ] **Step 3: Write the implementation**

Create `internal/work/board/board.go`:

```go
// Package board implements a run's blackboard: typed, append-only entries with
// provenance, and the rules that turn them into stances, convergence and a
// conclusion (spec Sections 4.2 and 4.3).
package board

import (
	"errors"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// Errors returned by the board rules. Callers match them with errors.Is.
var (
	// ErrInvalid marks an entry that breaks a board rule.
	ErrInvalid = errors.New("board: invalid entry")
	// ErrNotFound marks a reference to an entry that is not on this board.
	ErrNotFound = errors.New("board: no such entry")
	// ErrNotAllowed marks an author who may not post this entry.
	ErrNotAllowed = errors.New("board: not allowed")
	// ErrProvenance marks evidence whose provenance does not check out.
	ErrProvenance = errors.New("board: provenance check failed")
	// ErrReasonRequired marks a human action that needs a preset reason.
	ErrReasonRequired = errors.New("board: a reason is required")
)

// Size limits that keep entries readable and bound flooding. The spec sets
// none; these are the implementation's choices.
const (
	MaxClaimRunes     = 2000
	MaxQuoteRunes     = 2000
	MaxStatementRunes = 1000
	MaxReasonRunes    = 500
	MaxCitations      = 20
)

// RunID identifies a run. Each run has its own board.
type RunID string

// EntryID identifies one board entry.
type EntryID string

// AuthorKind says who wrote an entry.
type AuthorKind string

// Author kinds.
const (
	AuthorAgent    AuthorKind = "agent"
	AuthorHuman    AuthorKind = "human"
	AuthorPlatform AuthorKind = "platform"
)

// Author is an entry's writer: an agent's name, a human's sign-in subject, or
// the platform.
type Author struct {
	Kind AuthorKind `json:"kind"`
	Name string     `json:"name"`
}

// IsContributor reports whether the author can hold a stance. Agents and
// humans can; the platform cannot.
func (a Author) IsContributor() bool {
	return a.Kind == AuthorAgent || a.Kind == AuthorHuman
}

// Kind is an entry's type.
type Kind string

// Entry kinds.
const (
	KindEvidence   Kind = "evidence"
	KindHypothesis Kind = "hypothesis"
	KindRetraction Kind = "retraction"
	KindRuling     Kind = "ruling"
)

// Stance is a contributor's position on a hypothesis.
type Stance string

// Stances.
const (
	Supports Stance = "supports"
	Refutes  Stance = "refutes"
)

// Source is where evidence comes from: a (server, tool) pair, human
// observation, or an inbound trigger source.
type Source string

// HumanObservation is the one source of all human evidence.
const HumanObservation Source = "human-observation"

// ToolSource names a (server, tool) pair as a source.
func ToolSource(server, tool string) Source {
	return Source(server + "/" + tool)
}

// ProvenanceKind says what backs an evidence entry.
type ProvenanceKind string

// Provenance kinds (spec Section 4.2).
const (
	FromReadCall ProvenanceKind = "read_call"
	FromHuman    ProvenanceKind = "human"
	FromPlatform ProvenanceKind = "platform"
)

// Provenance records what backs an evidence entry, and its source.
type Provenance struct {
	Kind   ProvenanceKind `json:"kind"`
	CallID string         `json:"callId,omitempty"`
	Quote  string         `json:"quote,omitempty"`
	// Ref names the trigger event or platform read behind a platform record.
	Ref    string `json:"ref,omitempty"`
	Source Source `json:"source"`
}

// Reason is a preset reason code with optional free text.
type Reason struct {
	Code string `json:"code"`
	Text string `json:"text,omitempty"`
}

// Evidence is a claim with provenance and, optionally, a stance on a hypothesis.
type Evidence struct {
	Claim      string     `json:"claim"`
	Provenance Provenance `json:"provenance"`
	Hypothesis EntryID    `json:"hypothesis,omitempty"`
	Stance     Stance     `json:"stance,omitempty"`
	// Dismisses lists the agent refutations that this human support lifted.
	// The support counts as their dismissal (spec Section 4.2).
	Dismisses []EntryID `json:"dismisses,omitempty"`
	Reason    *Reason   `json:"reason,omitempty"`
}

// Hypothesis is a keyed claim, grounded in evidence.
type Hypothesis struct {
	Key       key.Key   `json:"key"`
	Statement string    `json:"statement"`
	Evidence  []EntryID `json:"evidence"`
	// AliasOf names the earlier hypothesis with an equal key that this one
	// merged into. Empty for a hypothesis that merged into nothing.
	AliasOf EntryID `json:"aliasOf,omitempty"`
}

// Retraction withdraws the author's own earlier evidence.
type Retraction struct {
	Evidence EntryID `json:"evidence"`
}

// RulingKind is a human ruling that affects convergence.
type RulingKind string

// Ruling kinds. The other rulings of spec Section 4.2 act on work items and
// arrive with the coordinator.
const (
	RulingReject   RulingKind = "reject"
	RulingUnreject RulingKind = "unreject"
	RulingDismiss  RulingKind = "dismiss_refutation"
)

// Ruling is a human decision about a hypothesis or a refutation.
type Ruling struct {
	Kind       RulingKind `json:"kind"`
	Hypothesis EntryID    `json:"hypothesis,omitempty"`
	Refutation EntryID    `json:"refutation,omitempty"`
	Reason     *Reason    `json:"reason,omitempty"`
}

// Body holds an entry's content. Exactly one field is set.
type Body struct {
	Evidence   *Evidence   `json:"evidence,omitempty"`
	Hypothesis *Hypothesis `json:"hypothesis,omitempty"`
	Retraction *Retraction `json:"retraction,omitempty"`
	Ruling     *Ruling     `json:"ruling,omitempty"`
}

// Kind returns the kind of entry the body holds, or "" when it holds none or
// more than one.
func (b *Body) Kind() Kind {
	var kind Kind
	n := 0
	if b.Evidence != nil {
		kind, n = KindEvidence, n+1
	}
	if b.Hypothesis != nil {
		kind, n = KindHypothesis, n+1
	}
	if b.Retraction != nil {
		kind, n = KindRetraction, n+1
	}
	if b.Ruling != nil {
		kind, n = KindRuling, n+1
	}
	if n != 1 {
		return ""
	}
	return kind
}

// Event is one committed board entry. Seq orders a run's entries, and At is
// the database time of the commit; both only grow within a run.
type Event struct {
	Seq    int64     `json:"seq"`
	ID     EntryID   `json:"id"`
	Run    RunID     `json:"run"`
	Round  int       `json:"round"`
	Author Author    `json:"author"`
	At     time.Time `json:"at"`
	Body   Body      `json:"body"`
}

// Draft is an entry that passed the rules but is not committed yet. The store
// assigns its Seq and At.
type Draft struct {
	ID     EntryID
	Run    RunID
	Round  int
	Author Author
	Body   Body
}

// Convergence holds a work item type's convergence settings (spec Section 3.5).
type Convergence struct {
	MinContributors  int `json:"minContributors"`
	MinSources       int `json:"minSources"`
	RefutationMargin int `json:"refutationMargin"`
}

// DefaultConvergence is the spec's default: 2 contributors, 2 sources and a
// refutation margin of 2 (spec Section 15).
var DefaultConvergence = Convergence{MinContributors: 2, MinSources: 2, RefutationMargin: 2}

// Settings are a run's board settings, locked from its work item type.
type Settings struct {
	Schema      key.Schema  `json:"schema"`
	Convergence Convergence `json:"convergence"`
}

// Context says where and when an entry is posted.
type Context struct {
	Run   RunID
	Round int
	// HumanEntries is true only for the visible arm's primary run. Shadow-mode
	// runs receive no human entries (spec Section 7).
	HumanEntries bool
	Settings     Settings
}

// ReadCall is a gateway read call as the audit store keeps it.
type ReadCall struct {
	ID string
	// Run and Agent identify who made the call. Agent is empty for platform reads.
	Run    RunID
	Agent  string
	Server string
	Tool   string
	// Failed is true when the call ended in a read error, which cannot be cited.
	Failed bool
	// Output is the stored, redacted output.
	Output []byte
}

// EvidenceInput is what an author submits to post evidence.
type EvidenceInput struct {
	Claim string
	// CallID and Quote are an agent's provenance: one of its own read calls in
	// this run, and text that appears verbatim in that call's output.
	CallID string
	Quote  string
	// Hypothesis and Stance are optional, and go together.
	Hypothesis EntryID
	Stance     Stance
	// Reason is needed when a human's support would lift a standing refutation.
	Reason *Reason
}

// HypothesisInput is what an agent submits to post a hypothesis.
type HypothesisInput struct {
	Key       key.Key
	Statement string
	Evidence  []EntryID
}

// RulingInput is what a human submits to rule on a hypothesis or a refutation.
type RulingInput struct {
	Kind       RulingKind
	Hypothesis EntryID
	Refutation EntryID
	Reason     *Reason
}
```

Create `internal/work/board/quote.go`:

```go
package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// NormalizeSpace collapses every run of white space to one space and trims both ends.
func NormalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// QuoteAppears reports whether quote appears verbatim, after white-space
// normalization, in a read call's stored output. For JSON output it also
// matches the decoded text, so that escaping does not decide whether evidence
// counts (spec Section 4.2). An empty quote never appears.
func QuoteAppears(output []byte, quote string) bool {
	q := NormalizeSpace(quote)
	if q == "" {
		return false
	}
	if strings.Contains(NormalizeSpace(string(output)), q) {
		return true
	}
	text, ok := decodedJSONText(output)
	return ok && strings.Contains(NormalizeSpace(text), q)
}

// decodedJSONText renders one JSON document as plain text: every object key
// and scalar value, with strings unescaped, one value per line. It reports
// false when the output is not a single JSON document.
func decodedJSONText(output []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(output))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", false
	}
	var b strings.Builder
	writeJSONText(&b, v)
	return b.String(), true
}

func writeJSONText(b *strings.Builder, v any) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(t)) {
			b.WriteString(k)
			b.WriteString(": ")
			writeJSONText(b, t[k])
		}
	case []any:
		for _, e := range t {
			writeJSONText(b, e)
		}
	case string:
		b.WriteString(t)
		b.WriteByte('\n')
	case json.Number:
		b.WriteString(t.String())
		b.WriteByte('\n')
	case bool:
		b.WriteString(strconv.FormatBool(t))
		b.WriteByte('\n')
	default:
		b.WriteString("null\n")
	}
}
```

- [ ] **Step 4: Run the tests and the linter**

Run: `go test ./internal/work/board/`

Expected: PASS.

Run: `golangci-lint run ./...`

Expected: `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal/work/board/board.go \
  internal/work/board/quote.go \
  internal/work/board/quote_test.go
git commit -m "feat(work): add board entries and quote matching"
```

---

### Task 5: Board rules: replay, posting, retraction and rulings

**Files:**
- Create: `internal/work/board/state.go`
- Create: `internal/work/board/post.go`
- Test: `internal/work/board/fixture_test.go`
- Test: `internal/work/board/post_test.go`

**Interfaces:**
- Consumes: the types of Task 4.
- Produces, in package `board`:
  - `NewState(Settings) *State` and `Replay(Settings, []Event) (*State, error)`;
  - `(*State) Apply(*Event) error`, `(*State) Event(EntryID) (Event, bool)` and `(*State) RetractionOf(EntryID) (EntryID, bool)`;
  - `(*State) NewEvidence(*Context, EntryID, Author, *EvidenceInput, *ReadCall) (Draft, error)`;
  - `(*State) NewHypothesis(*Context, EntryID, Author, *HypothesisInput) (Draft, error)`;
  - `(*State) NewRetraction(*Context, EntryID, Author, EntryID) (Draft, error)`;
  - `(*State) NewRuling(*Context, EntryID, Author, *RulingInput) (Draft, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/work/board/fixture_test.go`:

```go
package board_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// Authors used across the board tests.
var (
	release = board.Author{Kind: board.AuthorAgent, Name: "release-investigator"}
	runtime = board.Author{Kind: board.AuthorAgent, Name: "runtime-investigator"}
	deps    = board.Author{Kind: board.AuthorAgent, Name: "dependency-investigator"}
	alice   = board.Author{Kind: board.AuthorHuman, Name: "alice@example.com"}
	bob     = board.Author{Kind: board.AuthorHuman, Name: "bob@example.com"}
)

func incidentSettings() board.Settings {
	return board.Settings{
		Schema: key.Schema{Fields: []key.Field{
			{Name: "service", Values: []string{"checkout", "payments"}},
			{Name: "failureMode", Values: []string{"bad-deploy", "config-change", "resource-exhaustion"}},
		}},
		Convergence: board.DefaultConvergence,
	}
}

func checkoutDeploy() key.Key {
	return key.Key{"service": "checkout", "failureMode": "bad-deploy"}
}

// fixture builds a board through the public rules, committing each draft the
// way the event store does: the next sequence number and a later time.
type fixture struct {
	t      *testing.T
	ctx    board.Context
	state  *board.State
	events []board.Event
	calls  map[string]*board.ReadCall
	now    time.Time
	n      int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := board.Context{Run: "run-1", Round: 1, HumanEntries: true, Settings: incidentSettings()}
	return &fixture{
		t:     t,
		ctx:   ctx,
		state: board.NewState(ctx.Settings),
		calls: make(map[string]*board.ReadCall),
		now:   time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC),
	}
}

func (f *fixture) nextID(prefix string) board.EntryID {
	f.n++
	return board.EntryID(fmt.Sprintf("%s-%d", prefix, f.n))
}

// call records a successful read call by an agent in the fixture's run.
func (f *fixture) call(agent board.Author, server, tool, output string) string {
	f.n++
	id := fmt.Sprintf("call-%d", f.n)
	f.calls[id] = &board.ReadCall{ID: id, Run: f.ctx.Run, Agent: agent.Name, Server: server, Tool: tool, Output: []byte(output)}
	return id
}

func (f *fixture) commit(d *board.Draft) board.Event {
	f.t.Helper()
	f.now = f.now.Add(time.Second)
	e := board.Event{Seq: int64(len(f.events) + 1), ID: d.ID, Run: d.Run, Round: d.Round, Author: d.Author, At: f.now, Body: d.Body}
	require.NoError(f.t, f.state.Apply(&e))
	f.events = append(f.events, e)
	return e
}

func (f *fixture) postEvidence(author board.Author, in *board.EvidenceInput) (board.Draft, error) {
	return f.state.NewEvidence(&f.ctx, f.nextID("ev"), author, in, f.calls[in.CallID])
}

// evidence posts an agent's evidence that quotes its own read call and
// returns the entry id. A non-empty hypothesis also sets a stance.
func (f *fixture) evidence(agent board.Author, server, tool, quote string, h board.EntryID, stance board.Stance) board.EntryID {
	f.t.Helper()
	callID := f.call(agent, server, tool, "output: "+quote)
	d, err := f.postEvidence(agent, &board.EvidenceInput{Claim: "saw " + quote, CallID: callID, Quote: quote, Hypothesis: h, Stance: stance})
	require.NoError(f.t, err)
	return f.commit(&d).ID
}

func (f *fixture) hypothesis(agent board.Author, k key.Key, evidence ...board.EntryID) board.EntryID {
	f.t.Helper()
	d, err := f.state.NewHypothesis(&f.ctx, f.nextID("hyp"), agent, &board.HypothesisInput{Key: k, Statement: "the cause is " + k.String(), Evidence: evidence})
	require.NoError(f.t, err)
	return f.commit(&d).ID
}

func (f *fixture) retract(agent board.Author, evidence board.EntryID) {
	f.t.Helper()
	d, err := f.state.NewRetraction(&f.ctx, f.nextID("ret"), agent, evidence)
	require.NoError(f.t, err)
	f.commit(&d)
}

func (f *fixture) rule(human board.Author, in *board.RulingInput) {
	f.t.Helper()
	d, err := f.state.NewRuling(&f.ctx, f.nextID("rul"), human, in)
	require.NoError(f.t, err)
	f.commit(&d)
}

var someReason = &board.Reason{Code: "wrong-cause"}
```

Create `internal/work/board/post_test.go`:

```go
package board_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

func TestAgentEvidenceCitesItsOwnReadCall(t *testing.T) {
	f := newFixture(t)
	own := f.call(release, "cd", "get_history", "checkout v42 deployed at 10:02")
	other := f.call(runtime, "logs", "search", "checkout v42 deployed at 10:02")
	failed := f.call(release, "cd", "get_history", "timeout")
	f.calls[failed].Failed = true
	foreign := f.call(release, "cd", "get_history", "checkout v42 deployed at 10:02")
	f.calls[foreign].Run = "run-2"

	tests := []struct {
		name   string
		callID string
		quote  string
		want   error
	}{
		{"unknown call", "call-404", "v42", board.ErrProvenance},
		{"another agent's call", other, "v42", board.ErrProvenance},
		{"a failed call", failed, "timeout", board.ErrProvenance},
		{"another run's call", foreign, "v42", board.ErrProvenance},
		{"quote not in the output", own, "v43", board.ErrProvenance},
		{"empty quote", own, "  ", board.ErrInvalid},
		{"too long a quote", own, strings.Repeat("v", board.MaxQuoteRunes+1), board.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.postEvidence(release, &board.EvidenceInput{Claim: "deployed", CallID: tt.callID, Quote: tt.quote})
			require.ErrorIs(t, err, tt.want)
		})
	}

	d, err := f.postEvidence(release, &board.EvidenceInput{Claim: " deployed ", CallID: own, Quote: "v42  deployed"})
	require.NoError(t, err)
	ev := d.Body.Evidence
	assert.Equal(t, "deployed", ev.Claim)
	assert.Equal(t, board.Provenance{Kind: board.FromReadCall, CallID: own, Quote: "v42 deployed", Source: "cd/get_history"}, ev.Provenance)
}

func TestEvidenceStanceGoesWithAHypothesis(t *testing.T) {
	f := newFixture(t)
	e := f.evidence(release, "cd", "get_history", "v42", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e)
	callID := f.call(runtime, "logs", "search", "NullPointerException")

	tests := []struct {
		name string
		in   board.EvidenceInput
		want error
	}{
		{"stance without hypothesis", board.EvidenceInput{Stance: board.Supports}, board.ErrInvalid},
		{"hypothesis without stance", board.EvidenceInput{Hypothesis: h}, board.ErrInvalid},
		{"unknown hypothesis", board.EvidenceInput{Hypothesis: "hyp-404", Stance: board.Supports}, board.ErrNotFound},
		{"evidence is not a hypothesis", board.EvidenceInput{Hypothesis: e, Stance: board.Supports}, board.ErrInvalid},
		{"unknown stance", board.EvidenceInput{Hypothesis: h, Stance: "maybe"}, board.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			in.Claim, in.CallID, in.Quote = "a claim", callID, "NullPointerException"
			_, err := f.postEvidence(runtime, &in)
			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestHumanEvidence(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))

	d, err := f.postEvidence(alice, &board.EvidenceInput{Claim: "I rolled it back and errors stopped", Hypothesis: h, Stance: board.Supports})
	require.NoError(t, err)
	assert.Equal(t, board.Provenance{Kind: board.FromHuman, Source: board.HumanObservation}, d.Body.Evidence.Provenance)

	_, err = f.postEvidence(alice, &board.EvidenceInput{Claim: "see this", CallID: "call-1", Quote: "v42"})
	require.ErrorIs(t, err, board.ErrInvalid, "humans cite no read call")

	f.ctx.HumanEntries = false
	_, err = f.postEvidence(alice, &board.EvidenceInput{Claim: "I checked"})
	require.ErrorIs(t, err, board.ErrNotAllowed, "shadow-mode runs take no human entries")
}

func TestHypothesisRules(t *testing.T) {
	f := newFixture(t)
	e := f.evidence(release, "cd", "get_history", "v42", "", "")
	gone := f.evidence(release, "cd", "get_history", "v41", "", "")
	f.retract(release, gone)

	tests := []struct {
		name   string
		author board.Author
		in     board.HypothesisInput
		want   error
	}{
		{"humans post no hypotheses", alice, board.HypothesisInput{Key: checkoutDeploy(), Statement: "s", Evidence: []board.EntryID{e}}, board.ErrNotAllowed},
		{"no evidence", release, board.HypothesisInput{Key: checkoutDeploy(), Statement: "s"}, board.ErrInvalid},
		{"unknown evidence", release, board.HypothesisInput{Key: checkoutDeploy(), Statement: "s", Evidence: []board.EntryID{"ev-404"}}, board.ErrNotFound},
		{"retracted evidence", release, board.HypothesisInput{Key: checkoutDeploy(), Statement: "s", Evidence: []board.EntryID{gone}}, board.ErrInvalid},
		{"empty statement", release, board.HypothesisInput{Key: checkoutDeploy(), Statement: " ", Evidence: []board.EntryID{e}}, board.ErrInvalid},
		{"key outside the schema", release, board.HypothesisInput{Key: key.Key{"service": "search", "failureMode": "bad-deploy"}, Statement: "s", Evidence: []board.EntryID{e}}, key.ErrValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.state.NewHypothesis(&f.ctx, "hyp-x", tt.author, &tt.in)
			require.ErrorIs(t, err, tt.want)
		})
	}

	d, err := f.state.NewHypothesis(&f.ctx, "hyp-x", release, &board.HypothesisInput{Key: checkoutDeploy(), Statement: "bad deploy", Evidence: []board.EntryID{e, e}})
	require.NoError(t, err)
	assert.Equal(t, []board.EntryID{e}, d.Body.Hypothesis.Evidence, "a repeated citation counts once")
}

func TestEqualKeysMerge(t *testing.T) {
	f := newFixture(t)
	first := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	second := f.hypothesis(runtime, checkoutDeploy(), f.evidence(runtime, "logs", "search", "NPE", "", ""))
	unsure := key.Key{"service": "checkout", "failureMode": key.Other}
	f.hypothesis(release, unsure, f.evidence(release, "cd", "get_history", "v40", "", ""))
	notMerged := f.hypothesis(runtime, unsure, f.evidence(runtime, "logs", "search", "warn", "", ""))

	e, ok := f.state.Event(second)
	require.True(t, ok)
	assert.Equal(t, first, e.Body.Hypothesis.AliasOf)
	e, ok = f.state.Event(notMerged)
	require.True(t, ok)
	assert.Empty(t, e.Body.Hypothesis.AliasOf, "keys that contain other never merge")
}

func TestRetraction(t *testing.T) {
	f := newFixture(t)
	e := f.evidence(release, "cd", "get_history", "v42", "", "")

	_, err := f.state.NewRetraction(&f.ctx, "ret-x", runtime, e)
	require.ErrorIs(t, err, board.ErrNotAllowed, "only the author retracts")
	_, err = f.state.NewRetraction(&f.ctx, "ret-x", alice, e)
	require.ErrorIs(t, err, board.ErrNotAllowed, "humans do not retract")

	f.retract(release, e)
	_, ok := f.state.RetractionOf(e)
	assert.True(t, ok)
	_, err = f.state.NewRetraction(&f.ctx, "ret-y", release, e)
	require.ErrorIs(t, err, board.ErrInvalid, "a second retraction is refused; callers treat a repeat as done")
}

func TestRulings(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	ref := f.evidence(runtime, "logs", "search", "no errors after 10:02", h, board.Refutes)
	sup := f.evidence(deps, "metrics", "query", "error rate 12%", h, board.Supports)

	tests := []struct {
		name   string
		author board.Author
		in     board.RulingInput
		want   error
	}{
		{"agents issue no rulings", release, board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason}, board.ErrNotAllowed},
		{"reject needs a reason", alice, board.RulingInput{Kind: board.RulingReject, Hypothesis: h}, board.ErrReasonRequired},
		{"unreject what is not rejected", alice, board.RulingInput{Kind: board.RulingUnreject, Hypothesis: h}, board.ErrInvalid},
		{"dismiss needs a reason", alice, board.RulingInput{Kind: board.RulingDismiss, Refutation: ref}, board.ErrReasonRequired},
		{"dismiss a support", alice, board.RulingInput{Kind: board.RulingDismiss, Refutation: sup, Reason: someReason}, board.ErrInvalid},
		{"unknown ruling", alice, board.RulingInput{Kind: "close", Hypothesis: h}, board.ErrInvalid},
		{"a reason's text needs a code", alice, board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: &board.Reason{Text: "no"}}, board.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.state.NewRuling(&f.ctx, "rul-x", tt.author, &tt.in)
			require.ErrorIs(t, err, tt.want)
		})
	}

	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})
	_, err := f.state.NewRuling(&f.ctx, "rul-y", bob, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})
	require.ErrorIs(t, err, board.ErrInvalid, "already rejected")

	f.ctx.HumanEntries = false
	_, err = f.state.NewRuling(&f.ctx, "rul-z", alice, &board.RulingInput{Kind: board.RulingUnreject, Hypothesis: h})
	require.ErrorIs(t, err, board.ErrNotAllowed, "shadow-mode runs take no rulings")
}

func TestReplayChecksConsistency(t *testing.T) {
	f := newFixture(t)
	e := f.evidence(release, "cd", "get_history", "v42", "", "")
	f.hypothesis(release, checkoutDeploy(), e)
	settings := incidentSettings()

	_, err := board.Replay(settings, f.events)
	require.NoError(t, err)

	swapped := []board.Event{f.events[1], f.events[0]}
	_, err = board.Replay(settings, swapped)
	require.ErrorIs(t, err, board.ErrNotFound, "a hypothesis cannot cite evidence committed after it")

	dup := []board.Event{f.events[0], f.events[0]}
	dup[1].Seq = 2
	_, err = board.Replay(settings, dup)
	require.ErrorIs(t, err, board.ErrInvalid, "duplicate ids")

	backwards := []board.Event{f.events[0], f.events[1]}
	backwards[1].Seq = 1
	_, err = board.Replay(settings, backwards)
	require.ErrorIs(t, err, board.ErrInvalid, "sequence numbers only grow")

	f.hypothesis(runtime, checkoutDeploy(), f.evidence(runtime, "logs", "search", "NPE", "", ""))
	unmerged := append([]board.Event(nil), f.events...)
	last := unmerged[len(unmerged)-1]
	h := *last.Body.Hypothesis
	h.AliasOf = ""
	last.Body.Hypothesis = &h
	unmerged[len(unmerged)-1] = last
	_, err = board.Replay(settings, unmerged)
	require.ErrorIs(t, err, board.ErrInvalid, "an equal mergeable key must merge")
}

func TestClaimLength(t *testing.T) {
	f := newFixture(t)
	callID := f.call(release, "cd", "get_history", "v42")

	_, err := f.postEvidence(release, &board.EvidenceInput{Claim: strings.Repeat("x", board.MaxClaimRunes+1), CallID: callID, Quote: "v42"})
	require.ErrorIs(t, err, board.ErrInvalid)
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/work/board/`

Expected: FAIL: `undefined: board.State`, `undefined: board.NewState` and `undefined: board.Replay`.

- [ ] **Step 3: Write the implementation**

Create `internal/work/board/state.go`:

```go
package board

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// State is a run's board, folded from its committed events in commit order.
type State struct {
	settings  Settings
	events    []Event
	index     map[EntryID]int     // entry id → position in events
	canonical map[EntryID]EntryID // hypothesis id → the hypothesis it merged into, or itself
	byKey     map[string]EntryID  // key string → canonical hypothesis, for mergeable keys
	retracted map[EntryID]EntryID // evidence id → the entry that retracted it
	rejected  map[EntryID]bool    // canonical hypothesis → rejected by a human
	dismissed map[EntryID]bool    // refuting evidence id → dismissed by a human
}

// NewState returns an empty board with the run's settings.
func NewState(settings Settings) *State {
	return &State{
		settings:  settings,
		index:     make(map[EntryID]int),
		canonical: make(map[EntryID]EntryID),
		byKey:     make(map[string]EntryID),
		retracted: make(map[EntryID]EntryID),
		rejected:  make(map[EntryID]bool),
		dismissed: make(map[EntryID]bool),
	}
}

// Replay folds committed events into a State. It checks that they are in
// commit order, uniquely identified and consistent with the board rules. It
// trusts each evidence entry's provenance, which was checked when it was posted.
func Replay(settings Settings, events []Event) (*State, error) {
	s := NewState(settings)
	for i := range events {
		if err := s.Apply(&events[i]); err != nil {
			return nil, fmt.Errorf("replay entry %d: %w", events[i].Seq, err)
		}
	}
	return s, nil
}

// Apply adds one committed event to the board, after checking it against the
// board rules and the commit order.
func (s *State) Apply(e *Event) error {
	if err := s.checkNewID(e.ID); err != nil {
		return err
	}
	if n := len(s.events); n > 0 {
		last := &s.events[n-1]
		if e.Seq <= last.Seq || e.At.Before(last.At) {
			return fmt.Errorf("%w: entry %s is out of commit order", ErrInvalid, e.ID)
		}
	}
	if err := s.check(e.Author, &e.Body); err != nil {
		return err
	}
	s.record(e)
	return nil
}

// Event returns the committed entry with the given id.
func (s *State) Event(id EntryID) (Event, bool) {
	e, ok := s.event(id)
	if !ok {
		return Event{}, false
	}
	return *e, true
}

// RetractionOf returns the entry that retracted the given evidence, if any.
func (s *State) RetractionOf(evidence EntryID) (EntryID, bool) {
	id, ok := s.retracted[evidence]
	return id, ok
}

func (s *State) checkNewID(id EntryID) error {
	if id == "" {
		return fmt.Errorf("%w: entry without an id", ErrInvalid)
	}
	if _, dup := s.index[id]; dup {
		return fmt.Errorf("%w: duplicate entry id %s", ErrInvalid, id)
	}
	return nil
}

// check applies the rules that every entry satisfies, both when it is posted
// and when it is replayed.
func (s *State) check(author Author, b *Body) error {
	if author.Name == "" {
		return fmt.Errorf("%w: entry without an author", ErrInvalid)
	}
	switch b.Kind() {
	case KindEvidence:
		return s.checkEvidence(author, b.Evidence)
	case KindHypothesis:
		return s.checkHypothesis(author, b.Hypothesis)
	case KindRetraction:
		return s.checkRetraction(author, b.Retraction)
	case KindRuling:
		return s.checkRuling(author, b.Ruling)
	default:
		return fmt.Errorf("%w: an entry holds exactly one body", ErrInvalid)
	}
}

func (s *State) checkEvidence(author Author, ev *Evidence) error {
	if err := checkText("claim", ev.Claim, MaxClaimRunes); err != nil {
		return err
	}
	if err := checkProvenance(author, &ev.Provenance); err != nil {
		return err
	}
	if (ev.Hypothesis == "") != (ev.Stance == "") {
		return fmt.Errorf("%w: a stance goes with a hypothesis, and a hypothesis with a stance", ErrInvalid)
	}
	if ev.Hypothesis != "" {
		if !author.IsContributor() {
			return fmt.Errorf("%w: the platform takes no stance", ErrNotAllowed)
		}
		if ev.Stance != Supports && ev.Stance != Refutes {
			return fmt.Errorf("%w: unknown stance %q", ErrInvalid, ev.Stance)
		}
		if _, err := s.canonicalOf(ev.Hypothesis); err != nil {
			return err
		}
	}
	if len(ev.Dismisses) > 0 {
		if author.Kind != AuthorHuman || ev.Stance != Supports {
			return fmt.Errorf("%w: only a human's support dismisses refutations", ErrInvalid)
		}
		for _, id := range ev.Dismisses {
			if _, err := s.refutation(id); err != nil {
				return err
			}
		}
		return checkReason(ev.Reason, true)
	}
	return checkReason(ev.Reason, false)
}

func checkProvenance(author Author, p *Provenance) error {
	switch author.Kind {
	case AuthorAgent:
		if p.Kind != FromReadCall || p.CallID == "" || p.Source == "" {
			return fmt.Errorf("%w: an agent's evidence cites one of its read calls", ErrProvenance)
		}
		return checkText("quote", p.Quote, MaxQuoteRunes)
	case AuthorHuman:
		if p.Kind != FromHuman || p.Source != HumanObservation {
			return fmt.Errorf("%w: a human's evidence is human observation", ErrProvenance)
		}
	case AuthorPlatform:
		if p.Kind != FromPlatform || p.Ref == "" || p.Source == "" {
			return fmt.Errorf("%w: the platform's evidence names its record", ErrProvenance)
		}
	default:
		return fmt.Errorf("%w: unknown author kind %q", ErrInvalid, author.Kind)
	}
	return nil
}

func (s *State) checkHypothesis(author Author, h *Hypothesis) error {
	if author.Kind != AuthorAgent {
		return fmt.Errorf("%w: only agents post hypotheses", ErrNotAllowed)
	}
	if err := checkText("statement", h.Statement, MaxStatementRunes); err != nil {
		return err
	}
	if err := s.settings.Schema.Validate(h.Key); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if len(h.Evidence) == 0 || len(h.Evidence) > MaxCitations {
		return fmt.Errorf("%w: a hypothesis cites 1 to %d evidence entries", ErrInvalid, MaxCitations)
	}
	seen := make(map[EntryID]bool, len(h.Evidence))
	for _, id := range h.Evidence {
		if seen[id] {
			return fmt.Errorf("%w: evidence %s is cited twice", ErrInvalid, id)
		}
		seen[id] = true
		if _, err := s.liveEvidence(id); err != nil {
			return err
		}
	}
	target, merges := s.mergeTarget(h.Key)
	switch {
	case h.AliasOf == target:
		return nil
	case merges:
		return fmt.Errorf("%w: hypothesis %s has key %s, so this one must merge into it", ErrInvalid, target, h.Key)
	default:
		return fmt.Errorf("%w: %s is not a hypothesis with an equal, mergeable key", ErrInvalid, h.AliasOf)
	}
}

func (s *State) checkRetraction(author Author, r *Retraction) error {
	if author.Kind != AuthorAgent {
		return fmt.Errorf("%w: only agents retract evidence", ErrNotAllowed)
	}
	e, err := s.evidence(r.Evidence)
	if err != nil {
		return err
	}
	if e.Author != author {
		return fmt.Errorf("%w: an agent retracts only its own evidence", ErrNotAllowed)
	}
	if _, done := s.retracted[r.Evidence]; done {
		return fmt.Errorf("%w: evidence %s is already retracted", ErrInvalid, r.Evidence)
	}
	return nil
}

func (s *State) checkRuling(author Author, r *Ruling) error {
	if author.Kind != AuthorHuman {
		return fmt.Errorf("%w: only humans issue rulings", ErrNotAllowed)
	}
	switch r.Kind {
	case RulingReject, RulingUnreject:
		if r.Refutation != "" {
			return fmt.Errorf("%w: a %s ruling names a hypothesis only", ErrInvalid, r.Kind)
		}
		h, err := s.canonicalOf(r.Hypothesis)
		if err != nil {
			return err
		}
		if r.Kind == RulingReject {
			if s.rejected[h] {
				return fmt.Errorf("%w: hypothesis %s is already rejected", ErrInvalid, h)
			}
			return checkReason(r.Reason, true)
		}
		if !s.rejected[h] {
			return fmt.Errorf("%w: hypothesis %s is not rejected", ErrInvalid, h)
		}
		return checkReason(r.Reason, false)
	case RulingDismiss:
		if r.Hypothesis != "" {
			return fmt.Errorf("%w: a dismissal names a refutation only", ErrInvalid)
		}
		if _, err := s.refutation(r.Refutation); err != nil {
			return err
		}
		if s.dismissed[r.Refutation] {
			return fmt.Errorf("%w: refutation %s is already dismissed", ErrInvalid, r.Refutation)
		}
		return checkReason(r.Reason, true)
	default:
		return fmt.Errorf("%w: unknown ruling %q", ErrInvalid, r.Kind)
	}
}

func checkText(name, value string, maxRunes int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is empty", ErrInvalid, name)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%w: %s is longer than %d characters", ErrInvalid, name, maxRunes)
	}
	return nil
}

func checkReason(r *Reason, required bool) error {
	if r == nil || r.Code == "" {
		if r != nil && r.Text != "" {
			return fmt.Errorf("%w: a reason's text needs a preset code", ErrInvalid)
		}
		if required {
			return ErrReasonRequired
		}
		return nil
	}
	if utf8.RuneCountInString(r.Text) > MaxReasonRunes {
		return fmt.Errorf("%w: a reason's text is longer than %d characters", ErrInvalid, MaxReasonRunes)
	}
	return nil
}

func (s *State) record(e *Event) {
	s.index[e.ID] = len(s.events)
	s.events = append(s.events, *e)
	b := &e.Body
	switch {
	case b.Evidence != nil:
		for _, id := range b.Evidence.Dismisses {
			s.dismissed[id] = true
		}
	case b.Hypothesis != nil:
		canon := e.ID
		if b.Hypothesis.AliasOf != "" {
			canon = b.Hypothesis.AliasOf
		}
		s.canonical[e.ID] = canon
		if canon == e.ID && s.settings.Schema.Mergeable(b.Hypothesis.Key) {
			s.byKey[b.Hypothesis.Key.String()] = e.ID
		}
	case b.Retraction != nil:
		s.retracted[b.Retraction.Evidence] = e.ID
	case b.Ruling != nil:
		switch b.Ruling.Kind {
		case RulingReject:
			s.rejected[s.canonical[b.Ruling.Hypothesis]] = true
		case RulingUnreject:
			delete(s.rejected, s.canonical[b.Ruling.Hypothesis])
		case RulingDismiss:
			s.dismissed[b.Ruling.Refutation] = true
		}
	}
}

func (s *State) event(id EntryID) (*Event, bool) {
	i, ok := s.index[id]
	if !ok {
		return nil, false
	}
	return &s.events[i], true
}

func (s *State) evidence(id EntryID) (*Event, error) {
	e, ok := s.event(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if e.Body.Evidence == nil {
		return nil, fmt.Errorf("%w: %s is not evidence", ErrInvalid, id)
	}
	return e, nil
}

// liveEvidence returns evidence that has not been retracted.
func (s *State) liveEvidence(id EntryID) (*Event, error) {
	e, err := s.evidence(id)
	if err != nil {
		return nil, err
	}
	if _, gone := s.retracted[id]; gone {
		return nil, fmt.Errorf("%w: evidence %s was retracted", ErrInvalid, id)
	}
	return e, nil
}

// refutation returns live evidence that refutes a hypothesis.
func (s *State) refutation(id EntryID) (*Event, error) {
	e, err := s.liveEvidence(id)
	if err != nil {
		return nil, err
	}
	if e.Body.Evidence.Stance != Refutes {
		return nil, fmt.Errorf("%w: %s is not a refutation", ErrInvalid, id)
	}
	return e, nil
}

// canonicalOf resolves a hypothesis id, which may be an alias, to the
// hypothesis it merged into.
func (s *State) canonicalOf(id EntryID) (EntryID, error) {
	if canon, ok := s.canonical[id]; ok {
		return canon, nil
	}
	if _, exists := s.index[id]; exists {
		return "", fmt.Errorf("%w: %s is not a hypothesis", ErrInvalid, id)
	}
	return "", fmt.Errorf("%w: %s", ErrNotFound, id)
}

// mergeTarget returns the hypothesis that a new hypothesis with key k merges
// into, if any.
func (s *State) mergeTarget(k key.Key) (EntryID, bool) {
	if !s.settings.Schema.Mergeable(k) {
		return "", false
	}
	id, ok := s.byKey[k.String()]
	return id, ok
}
```

Create `internal/work/board/post.go`:

```go
package board

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

var errNoHumanEntries = fmt.Errorf("%w: this run takes no human entries", ErrNotAllowed)

// NewEvidence checks an evidence post and returns the entry to commit.
//
// An agent's evidence cites one of its own read calls in this run, plus a
// quote that appears in the call's stored output; call is that read call as
// the audit store holds it, or nil when the store has no such call. A human's
// evidence is human observation, and only runs that take human entries accept
// it.
func (s *State) NewEvidence(c *Context, id EntryID, author Author, in *EvidenceInput, call *ReadCall) (Draft, error) {
	ev := &Evidence{
		Claim:      strings.TrimSpace(in.Claim),
		Hypothesis: in.Hypothesis,
		Stance:     in.Stance,
		Reason:     in.Reason,
	}
	switch author.Kind {
	case AuthorAgent:
		p, err := agentProvenance(c, author, in, call)
		if err != nil {
			return Draft{}, err
		}
		ev.Provenance = p
	case AuthorHuman:
		if !c.HumanEntries {
			return Draft{}, errNoHumanEntries
		}
		if in.CallID != "" || in.Quote != "" {
			return Draft{}, fmt.Errorf("%w: a human's evidence cites no read call", ErrInvalid)
		}
		ev.Provenance = Provenance{Kind: FromHuman, Source: HumanObservation}
	default:
		return Draft{}, fmt.Errorf("%w: %s authors do not post evidence this way", ErrNotAllowed, author.Kind)
	}
	return s.prepare(c, id, author, Body{Evidence: ev})
}

func agentProvenance(c *Context, author Author, in *EvidenceInput, call *ReadCall) (Provenance, error) {
	quote := NormalizeSpace(in.Quote)
	if err := checkText("quote", quote, MaxQuoteRunes); err != nil {
		return Provenance{}, err
	}
	switch {
	case call == nil || in.CallID == "" || call.ID != in.CallID:
		return Provenance{}, fmt.Errorf("%w: no read call %q", ErrProvenance, in.CallID)
	case call.Run != c.Run:
		return Provenance{}, fmt.Errorf("%w: read call %s belongs to another run", ErrProvenance, call.ID)
	case call.Agent != author.Name:
		return Provenance{}, fmt.Errorf("%w: read call %s was not made by %s", ErrProvenance, call.ID, author.Name)
	case call.Failed:
		return Provenance{}, fmt.Errorf("%w: read call %s ended in an error, which cannot be cited", ErrProvenance, call.ID)
	case !QuoteAppears(call.Output, quote):
		return Provenance{}, fmt.Errorf("%w: the quote does not appear in the output of read call %s", ErrProvenance, call.ID)
	}
	return Provenance{Kind: FromReadCall, CallID: call.ID, Quote: quote, Source: ToolSource(call.Server, call.Tool)}, nil
}

// NewHypothesis checks a hypothesis post and returns the entry to commit. A
// hypothesis whose key equals an earlier mergeable key becomes an alias of it,
// and its author's support counts for the earlier one (spec Section 4.3).
// Citing the same evidence twice counts once.
func (s *State) NewHypothesis(c *Context, id EntryID, author Author, in *HypothesisInput) (Draft, error) {
	h := &Hypothesis{
		Key:       maps.Clone(in.Key),
		Statement: strings.TrimSpace(in.Statement),
		Evidence:  uniqueIDs(in.Evidence),
	}
	h.AliasOf, _ = s.mergeTarget(h.Key)
	return s.prepare(c, id, author, Body{Hypothesis: h})
}

// NewRetraction checks that an agent retracts its own live evidence and
// returns the entry to commit.
func (s *State) NewRetraction(c *Context, id EntryID, author Author, evidence EntryID) (Draft, error) {
	return s.prepare(c, id, author, Body{Retraction: &Retraction{Evidence: evidence}})
}

// NewRuling checks a human's ruling and returns the entry to commit. Only runs
// that take human entries accept rulings.
func (s *State) NewRuling(c *Context, id EntryID, author Author, in *RulingInput) (Draft, error) {
	if author.Kind == AuthorHuman && !c.HumanEntries {
		return Draft{}, errNoHumanEntries
	}
	r := &Ruling{Kind: in.Kind, Hypothesis: in.Hypothesis, Refutation: in.Refutation, Reason: in.Reason}
	return s.prepare(c, id, author, Body{Ruling: r})
}

// prepare checks a new entry against the board and returns its draft.
func (s *State) prepare(c *Context, id EntryID, author Author, b Body) (Draft, error) {
	if c.Run == "" {
		return Draft{}, fmt.Errorf("%w: entry without a run", ErrInvalid)
	}
	if err := s.checkNewID(id); err != nil {
		return Draft{}, err
	}
	if err := s.check(author, &b); err != nil {
		return Draft{}, err
	}
	return Draft{ID: id, Run: c.Run, Round: c.Round, Author: author, Body: b}, nil
}

func uniqueIDs(ids []EntryID) []EntryID {
	out := make([]EntryID, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests and the linter**

Run: `go test ./internal/work/board/`

Expected: PASS.

Run: `golangci-lint run ./...`

Expected: `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal/work/board/fixture_test.go \
  internal/work/board/post.go \
  internal/work/board/post_test.go \
  internal/work/board/state.go
git commit -m "feat(work): add board rules for evidence, hypotheses, retractions and rulings"
```

---

### Task 6: Stances, convergence and the conclusion

**Files:**
- Create: `internal/work/board/assess.go`
- Modify (replace the whole file): `internal/work/board/post.go`
- Test: `internal/work/board/assess_test.go`

**Interfaces:**
- Consumes: `State` and the post checks of Task 5.
- Produces, in package `board`:
  - `AsOf([]Event, time.Time) []Event`;
  - `(*State) Assess() Assessment`, where `Assessment` has `Hypotheses []HypothesisView` and `Conclusion EntryID`;
  - `HypothesisView`, with `ID`, `Key`, `Statement`, `Author`, `Aliases`, `Supporters`, `Refuters`, `Sources`, `StandingRefutations`, `Rejected`, `Converged` and `Score`.

  `NewEvidence` now also records the refutations that a human's support lifts.

- [ ] **Step 1: Write the failing tests**

Create `internal/work/board/assess_test.go`:

```go
package board_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

func TestConvergenceNeedsTwoContributorsAndTwoSources(t *testing.T) {
	t.Run("one agent with two sources", func(t *testing.T) {
		f := newFixture(t)
		h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
		f.evidence(release, "metrics", "query", "error rate 12%", h, board.Supports)

		v := f.view(h)
		assert.Len(t, v.Sources, 2)
		assert.False(t, v.Converged)
	})
	t.Run("two agents with one source", func(t *testing.T) {
		f := newFixture(t)
		h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "logs", "search", "NPE", "", ""))
		f.evidence(runtime, "logs", "search", "NPE again", h, board.Supports)

		v := f.view(h)
		assert.Len(t, v.Supporters, 2)
		assert.False(t, v.Converged)
	})
	t.Run("two agents with two sources", func(t *testing.T) {
		f := newFixture(t)
		h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
		f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)

		a := f.state.Assess()
		assert.True(t, f.view(h).Converged)
		assert.Equal(t, h, a.Conclusion)
	})
}

func TestOnlyTheLatestStanceCounts(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	f.evidence(runtime, "logs", "search", "the NPE predates v42", h, board.Refutes)

	v := f.view(h)
	assert.Equal(t, []board.Author{release}, v.Supporters)
	assert.Equal(t, []board.Author{runtime}, v.Refuters)
	assert.Equal(t, 0, v.Score)
}

func TestRetractedEvidenceStopsCounting(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	wrong := f.evidence(runtime, "logs", "search", "no NPE after the rollback", h, board.Refutes)
	require.Equal(t, []board.Author{runtime}, f.view(h).Refuters)

	f.retract(runtime, wrong)

	v := f.view(h)
	assert.Equal(t, []board.Author{release, runtime}, v.Supporters, "the earlier support counts again")
	assert.True(t, v.Converged)
}

func TestHypothesisAuthorSupportNeedsLiveEvidence(t *testing.T) {
	f := newFixture(t)
	e := f.evidence(release, "cd", "get_history", "v42", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e)
	require.Equal(t, []board.Author{release}, f.view(h).Supporters)

	f.retract(release, e)

	assert.Empty(t, f.view(h).Supporters)
}

func TestAgentRefutationStandsUntilOutnumbered(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	ref := f.evidence(deps, "metrics", "query", "errors started before 10:02", h, board.Refutes)

	v := f.view(h)
	assert.Equal(t, []board.EntryID{ref}, v.StandingRefutations)
	assert.False(t, v.Converged)

	f.evidence(board.Author{Kind: board.AuthorAgent, Name: "network-investigator"}, "metrics", "query", "no packet loss", h, board.Supports)

	v = f.view(h)
	assert.Empty(t, v.StandingRefutations, "3 supporters against 1 refuter reaches the margin of 2")
	assert.True(t, v.Converged)
}

func TestHumanRefutationStandsUntilDismissed(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	f.evidence(deps, "metrics", "query", "error rate 12%", h, board.Supports)
	f.evidence(board.Author{Kind: board.AuthorAgent, Name: "network-investigator"}, "metrics", "query", "no packet loss", h, board.Supports)
	ref := f.humanEvidence(alice, h, board.Refutes, nil)

	assert.Equal(t, []board.EntryID{ref}, f.view(h).StandingRefutations, "no margin outweighs a human")

	f.rule(bob, &board.RulingInput{Kind: board.RulingDismiss, Refutation: ref, Reason: someReason})

	assert.True(t, f.view(h).Converged)
}

func TestHumanSupportThatLiftsARefutationIsADismissal(t *testing.T) {
	f := newFixture(t)
	first := f.evidence(release, "cd", "get_history", "v42", "", "")
	h := f.hypothesis(release, checkoutDeploy(), first)
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	ref := f.evidence(deps, "metrics", "query", "errors started before 10:02", h, board.Refutes)

	_, err := f.postEvidence(alice, &board.EvidenceInput{Claim: "rollback fixed it", Hypothesis: h, Stance: board.Supports})
	require.ErrorIs(t, err, board.ErrReasonRequired)

	support := f.humanEvidence(alice, h, board.Supports, someReason)
	e, _ := f.state.Event(support)
	assert.Equal(t, []board.EntryID{ref}, e.Body.Evidence.Dismisses)
	assert.True(t, f.view(h).Converged)

	f.retract(release, first)

	v := f.view(h)
	assert.Equal(t, []board.Author{runtime, alice}, v.Supporters, "agents sort before humans")
	assert.Empty(t, v.StandingRefutations, "the dismissal outlives the margin that caused it")
	assert.True(t, v.Converged)
}

func TestRejectedHypothesisCannotConverge(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	require.True(t, f.view(h).Converged)

	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})

	a := f.state.Assess()
	assert.False(t, f.view(h).Converged)
	assert.Empty(t, a.Conclusion, "a rejected hypothesis is no conclusion")

	f.rule(alice, &board.RulingInput{Kind: board.RulingUnreject, Hypothesis: h})

	assert.True(t, f.view(h).Converged)
}

func TestRejectingAnAliasRejectsTheMergedHypothesis(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	alias := f.hypothesis(runtime, checkoutDeploy(), f.evidence(runtime, "logs", "search", "NPE", "", ""))

	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: alias, Reason: someReason})

	assert.True(t, f.view(h).Rejected)
}

func TestConclusionWithoutConvergence(t *testing.T) {
	t.Run("the leading hypothesis", func(t *testing.T) {
		f := newFixture(t)
		lead := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
		f.evidence(runtime, "cd", "get_history", "v42 again", lead, board.Supports)
		f.hypothesis(deps, key.Key{"service": "payments", "failureMode": "resource-exhaustion"}, f.evidence(deps, "metrics", "query", "pool 98%", "", ""))

		a := f.state.Assess()
		assert.False(t, a.Hypotheses[0].Converged)
		assert.Equal(t, lead, a.Hypotheses[0].ID)
		assert.Equal(t, lead, a.Conclusion)
	})
	t.Run("a tie means no conclusion", func(t *testing.T) {
		f := newFixture(t)
		f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
		f.hypothesis(deps, key.Key{"service": "payments", "failureMode": "resource-exhaustion"}, f.evidence(deps, "metrics", "query", "pool 98%", "", ""))

		assert.Empty(t, f.state.Assess().Conclusion)
	})
	t.Run("no supporter means no conclusion", func(t *testing.T) {
		f := newFixture(t)
		e := f.evidence(release, "cd", "get_history", "v42", "", "")
		h := f.hypothesis(release, checkoutDeploy(), e)
		f.retract(release, e)
		f.evidence(runtime, "logs", "search", "no NPE", h, board.Refutes)

		a := f.state.Assess()
		assert.Equal(t, -1, a.Hypotheses[0].Score)
		assert.Empty(t, a.Conclusion)
	})
}

func TestAssessAsOfAMoment(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	cutoff := f.now
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)

	early, err := board.Replay(incidentSettings(), board.AsOf(f.events, cutoff))
	require.NoError(t, err)
	late, err := board.Replay(incidentSettings(), f.events)
	require.NoError(t, err)

	assert.False(t, early.Assess().Hypotheses[0].Converged)
	assert.Equal(t, h, early.Assess().Conclusion, "the leading hypothesis before convergence")
	assert.True(t, late.Assess().Hypotheses[0].Converged)
}

func (f *fixture) humanEvidence(human board.Author, h board.EntryID, stance board.Stance, reason *board.Reason) board.EntryID {
	f.t.Helper()
	d, err := f.postEvidence(human, &board.EvidenceInput{Claim: "I checked", Hypothesis: h, Stance: stance, Reason: reason})
	require.NoError(f.t, err)
	return f.commit(&d).ID
}

// view returns the assessment's view of hypothesis h.
func (f *fixture) view(h board.EntryID) board.HypothesisView {
	f.t.Helper()
	views := f.state.Assess().Hypotheses
	for i := range views {
		if views[i].ID == h {
			return views[i]
		}
	}
	f.t.Fatalf("hypothesis %s is not on the board", h)
	return board.HypothesisView{}
}

func TestAliasesCountForTheMergedHypothesis(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	alias := f.hypothesis(runtime, checkoutDeploy(), f.evidence(runtime, "logs", "search", "NPE", "", ""))
	f.evidence(deps, "metrics", "query", "error rate 12%", alias, board.Supports)

	v := f.view(h)
	assert.Equal(t, []board.EntryID{alias}, v.Aliases)
	assert.Equal(t, []board.Author{deps, release, runtime}, v.Supporters, "the alias's author and evidence citing the alias both count")
	require.Len(t, f.state.Assess().Hypotheses, 1, "an alias is not ranked on its own")
}

func TestANewAliasOfARejectedHypothesisStaysRejected(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})
	f.hypothesis(runtime, checkoutDeploy(), f.evidence(runtime, "logs", "search", "NPE", "", ""))

	v := f.view(h)
	assert.True(t, v.Rejected)
	assert.False(t, v.Converged, "posting a rejected claim again does not escape the rejection")
	assert.Empty(t, f.state.Assess().Conclusion)
}

func TestAsOfIncludesAnEntryCommittedAtTheCutoff(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	support := f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	e, ok := f.state.Event(support)
	require.True(t, ok)

	assert.Len(t, board.AsOf(f.events, e.At), len(f.events))
	assert.Len(t, board.AsOf(f.events, e.At.In(time.FixedZone("CEST", 2*60*60))), len(f.events), "a zone changes the notation, not the moment")
	assert.Len(t, board.AsOf(f.events, e.At.Add(-time.Nanosecond)), len(f.events)-1)
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/work/board/`

Expected: FAIL: `f.state.Assess undefined (type *board.State has no field or method Assess)`.

- [ ] **Step 3: Write the implementation**

Create `internal/work/board/assess.go`:

```go
package board

import (
	"cmp"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// HypothesisView is one hypothesis's standing on the board.
type HypothesisView struct {
	ID        EntryID   `json:"id"`
	Key       key.Key   `json:"key"`
	Statement string    `json:"statement"`
	Author    Author    `json:"author"`
	Aliases   []EntryID `json:"aliases"`
	// Supporters and Refuters are the contributors whose latest stance supports
	// or refutes the hypothesis.
	Supporters []Author `json:"supporters"`
	Refuters   []Author `json:"refuters"`
	// Sources are the distinct sources of the supporters' live supporting evidence.
	Sources []Source `json:"sources"`
	// StandingRefutations are the refuting entries that block convergence.
	StandingRefutations []EntryID `json:"standingRefutations"`
	Rejected            bool      `json:"rejected"`
	Converged           bool      `json:"converged"`
	// Score is the number of supporters minus the number of refuters.
	Score int `json:"score"`
}

// Assessment ranks a board's hypotheses and names the run's conclusion.
type Assessment struct {
	Hypotheses []HypothesisView `json:"hypotheses"`
	// Conclusion is empty when the run has no conclusion.
	Conclusion EntryID `json:"conclusion,omitempty"`
}

// AsOf returns the events committed at or before t, so that a board can be
// assessed as of a moment (spec Section 4.3). Events must be in commit order.
func AsOf(events []Event, t time.Time) []Event {
	n := sort.Search(len(events), func(i int) bool { return events[i].At.After(t) })
	return events[:n]
}

// Assess ranks the hypotheses by score, then by the number of distinct
// supporting sources, then by age, and picks the run's conclusion (spec
// Section 4.3).
func (s *State) Assess() Assessment {
	views := make([]HypothesisView, 0)
	for i := range s.events {
		e := &s.events[i]
		if e.Body.Hypothesis != nil && s.canonical[e.ID] == e.ID {
			views = append(views, s.view(e))
		}
	}
	sort.SliceStable(views, func(i, j int) bool { return outranks(&views[i], &views[j]) })
	return Assessment{Hypotheses: views, Conclusion: conclusion(views)}
}

// outranks orders hypotheses by score, then by distinct supporting sources.
func outranks(a, b *HypothesisView) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return len(a.Sources) > len(b.Sources)
}

// conclusion picks the conclusion from ranked hypotheses: the top converged
// one; if none converged, the top one that has a supporter and that no human
// rejected. A tie between the top two candidates means no conclusion.
func conclusion(ranked []HypothesisView) EntryID {
	candidates := filterViews(ranked, func(v *HypothesisView) bool { return v.Converged })
	if len(candidates) == 0 {
		candidates = filterViews(ranked, func(v *HypothesisView) bool {
			return !v.Rejected && len(v.Supporters) > 0
		})
	}
	switch {
	case len(candidates) == 0:
		return ""
	case len(candidates) > 1 && !outranks(candidates[0], candidates[1]):
		return ""
	default:
		return candidates[0].ID
	}
}

func filterViews(views []HypothesisView, keep func(*HypothesisView) bool) []*HypothesisView {
	var out []*HypothesisView
	for i := range views {
		if keep(&views[i]) {
			out = append(out, &views[i])
		}
	}
	return out
}

func (s *State) view(e *Event) HypothesisView {
	count := s.countStances(e.ID, nil)
	v := HypothesisView{
		ID:                  e.ID,
		Key:                 e.Body.Hypothesis.Key,
		Statement:           e.Body.Hypothesis.Statement,
		Author:              e.Author,
		Aliases:             s.aliases(e.ID),
		Supporters:          count.supporters,
		Refuters:            count.refuters,
		Sources:             count.sources,
		StandingRefutations: make([]EntryID, 0, len(count.standing)),
		Rejected:            count.rejected,
		Score:               len(count.supporters) - len(count.refuters),
	}
	for i := range count.standing {
		v.StandingRefutations = append(v.StandingRefutations, count.standing[i].entry)
	}
	conv := s.settings.Convergence
	v.Converged = len(v.Supporters) >= conv.MinContributors &&
		len(v.Sources) >= conv.MinSources &&
		len(v.StandingRefutations) == 0 &&
		!v.Rejected
	return v
}

func (s *State) aliases(h EntryID) []EntryID {
	out := make([]EntryID, 0)
	for i := range s.events {
		id := s.events[i].ID
		if s.events[i].Body.Hypothesis != nil && id != h && s.canonical[id] == h {
			out = append(out, id)
		}
	}
	return out
}

// stanceRecord is one contributor's stance on a hypothesis, as one entry states it.
type stanceRecord struct {
	contributor Author
	stance      Stance
	seq         int64
	entry       EntryID
	sources     []Source
}

// stanceCount is a hypothesis's stances, counted under the board rules.
type stanceCount struct {
	supporters []Author
	refuters   []Author
	sources    []Source
	standing   []stanceRecord
	rejected   bool
}

// countStances counts the stances on canonical hypothesis h. Only each
// contributor's latest live stance counts. extra, when set, is a stance not yet
// posted, used to see what posting it would change.
func (s *State) countStances(h EntryID, extra *stanceRecord) stanceCount {
	records := s.records(h)
	if extra != nil {
		records = append(records, *extra)
	}
	latest := make(map[Author]stanceRecord, len(records))
	for _, r := range records {
		latest[r.contributor] = r
	}
	sources := make(map[Source]bool)
	for _, r := range records {
		if r.stance == Supports && latest[r.contributor].stance == Supports {
			for _, src := range r.sources {
				sources[src] = true
			}
		}
	}
	count := stanceCount{supporters: make([]Author, 0), refuters: make([]Author, 0), rejected: s.rejected[h]}
	for c := range latest {
		if latest[c].stance == Supports {
			count.supporters = append(count.supporters, c)
		} else {
			count.refuters = append(count.refuters, c)
		}
	}
	slices.SortFunc(count.supporters, compareAuthors)
	slices.SortFunc(count.refuters, compareAuthors)
	count.sources = slices.Sorted(maps.Keys(sources))
	for _, c := range count.refuters {
		r := latest[c]
		if s.stands(&r, len(count.supporters), len(count.refuters)) {
			count.standing = append(count.standing, r)
		}
	}
	return count
}

// stands reports whether a refuting record blocks convergence. A dismissed
// refutation never does. Otherwise a human's refutation stands, and an agent's
// stands unless supporters outnumber refuters by the refutation margin.
func (s *State) stands(r *stanceRecord, supporters, refuters int) bool {
	if s.dismissed[r.entry] {
		return false
	}
	if r.contributor.Kind == AuthorHuman {
		return true
	}
	return supporters < refuters+s.settings.Convergence.RefutationMargin
}

// records returns the stance records on canonical hypothesis h in commit
// order. Evidence with a stance gives one record. A hypothesis gives its
// author a supporting record, backed by the cited evidence the author wrote.
// Retracted evidence backs nothing.
func (s *State) records(h EntryID) []stanceRecord {
	var out []stanceRecord
	for i := range s.events {
		e := &s.events[i]
		switch {
		case e.Body.Evidence != nil:
			if r, ok := s.evidenceRecord(e, h); ok {
				out = append(out, r)
			}
		case e.Body.Hypothesis != nil:
			if r, ok := s.authorRecord(e, h); ok {
				out = append(out, r)
			}
		}
	}
	return out
}

func (s *State) evidenceRecord(e *Event, h EntryID) (stanceRecord, bool) {
	ev := e.Body.Evidence
	if ev.Hypothesis == "" || s.canonical[ev.Hypothesis] != h {
		return stanceRecord{}, false
	}
	if _, gone := s.retracted[e.ID]; gone {
		return stanceRecord{}, false
	}
	return stanceRecord{
		contributor: e.Author,
		stance:      ev.Stance,
		seq:         e.Seq,
		entry:       e.ID,
		sources:     []Source{ev.Provenance.Source},
	}, true
}

func (s *State) authorRecord(e *Event, h EntryID) (stanceRecord, bool) {
	if s.canonical[e.ID] != h {
		return stanceRecord{}, false
	}
	var sources []Source
	for _, id := range e.Body.Hypothesis.Evidence {
		ev, ok := s.event(id)
		if !ok || ev.Author != e.Author {
			continue
		}
		if _, gone := s.retracted[id]; gone {
			continue
		}
		sources = append(sources, ev.Body.Evidence.Provenance.Source)
	}
	if len(sources) == 0 {
		return stanceRecord{}, false
	}
	return stanceRecord{contributor: e.Author, stance: Supports, seq: e.Seq, entry: e.ID, sources: sources}, true
}

func compareAuthors(a, b Author) int {
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
}
```

Replace `internal/work/board/post.go`:

```go
package board

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
)

var errNoHumanEntries = fmt.Errorf("%w: this run takes no human entries", ErrNotAllowed)

// NewEvidence checks an evidence post and returns the entry to commit.
//
// An agent's evidence cites one of its own read calls in this run, plus a
// quote that appears in the call's stored output; call is that read call as
// the audit store holds it, or nil when the store has no such call. A human's
// evidence is human observation, and only runs that take human entries accept
// it. A human's support that would lift an agent's standing refutation counts
// as its dismissal and needs a reason (spec Section 4.2).
func (s *State) NewEvidence(c *Context, id EntryID, author Author, in *EvidenceInput, call *ReadCall) (Draft, error) {
	ev := &Evidence{
		Claim:      strings.TrimSpace(in.Claim),
		Hypothesis: in.Hypothesis,
		Stance:     in.Stance,
		Reason:     in.Reason,
	}
	switch author.Kind {
	case AuthorAgent:
		p, err := agentProvenance(c, author, in, call)
		if err != nil {
			return Draft{}, err
		}
		ev.Provenance = p
	case AuthorHuman:
		if !c.HumanEntries {
			return Draft{}, errNoHumanEntries
		}
		if in.CallID != "" || in.Quote != "" {
			return Draft{}, fmt.Errorf("%w: a human's evidence cites no read call", ErrInvalid)
		}
		ev.Provenance = Provenance{Kind: FromHuman, Source: HumanObservation}
	default:
		return Draft{}, fmt.Errorf("%w: %s authors do not post evidence this way", ErrNotAllowed, author.Kind)
	}
	d, err := s.prepare(c, id, author, Body{Evidence: ev})
	if err != nil {
		return Draft{}, err
	}
	if author.Kind == AuthorHuman && ev.Stance == Supports {
		if err := s.liftRefutations(author, ev); err != nil {
			return Draft{}, err
		}
	}
	return d, nil
}

func agentProvenance(c *Context, author Author, in *EvidenceInput, call *ReadCall) (Provenance, error) {
	quote := NormalizeSpace(in.Quote)
	if err := checkText("quote", quote, MaxQuoteRunes); err != nil {
		return Provenance{}, err
	}
	switch {
	case call == nil || in.CallID == "" || call.ID != in.CallID:
		return Provenance{}, fmt.Errorf("%w: no read call %q", ErrProvenance, in.CallID)
	case call.Run != c.Run:
		return Provenance{}, fmt.Errorf("%w: read call %s belongs to another run", ErrProvenance, call.ID)
	case call.Agent != author.Name:
		return Provenance{}, fmt.Errorf("%w: read call %s was not made by %s", ErrProvenance, call.ID, author.Name)
	case call.Failed:
		return Provenance{}, fmt.Errorf("%w: read call %s ended in an error, which cannot be cited", ErrProvenance, call.ID)
	case !QuoteAppears(call.Output, quote):
		return Provenance{}, fmt.Errorf("%w: the quote does not appear in the output of read call %s", ErrProvenance, call.ID)
	}
	return Provenance{Kind: FromReadCall, CallID: call.ID, Quote: quote, Source: ToolSource(call.Server, call.Tool)}, nil
}

// liftRefutations records which agent refutations the human's support would
// lift, and requires a reason when there are any.
func (s *State) liftRefutations(author Author, ev *Evidence) error {
	h := s.canonical[ev.Hypothesis]
	support := stanceRecord{contributor: author, stance: Supports, seq: math.MaxInt64, sources: []Source{HumanObservation}}
	before := s.countStances(h, nil).standing
	after := s.countStances(h, &support).standing
	for i := range before {
		r := &before[i]
		stillStands := slices.ContainsFunc(after, func(a stanceRecord) bool { return a.entry == r.entry })
		if r.contributor != author && !stillStands {
			ev.Dismisses = append(ev.Dismisses, r.entry)
		}
	}
	if len(ev.Dismisses) > 0 {
		return checkReason(ev.Reason, true)
	}
	return nil
}

// NewHypothesis checks a hypothesis post and returns the entry to commit. A
// hypothesis whose key equals an earlier mergeable key becomes an alias of it,
// and its author's support counts for the earlier one (spec Section 4.3).
// Citing the same evidence twice counts once.
func (s *State) NewHypothesis(c *Context, id EntryID, author Author, in *HypothesisInput) (Draft, error) {
	h := &Hypothesis{
		Key:       maps.Clone(in.Key),
		Statement: strings.TrimSpace(in.Statement),
		Evidence:  uniqueIDs(in.Evidence),
	}
	h.AliasOf, _ = s.mergeTarget(h.Key)
	return s.prepare(c, id, author, Body{Hypothesis: h})
}

// NewRetraction checks that an agent retracts its own live evidence and
// returns the entry to commit.
func (s *State) NewRetraction(c *Context, id EntryID, author Author, evidence EntryID) (Draft, error) {
	return s.prepare(c, id, author, Body{Retraction: &Retraction{Evidence: evidence}})
}

// NewRuling checks a human's ruling and returns the entry to commit. Only runs
// that take human entries accept rulings.
func (s *State) NewRuling(c *Context, id EntryID, author Author, in *RulingInput) (Draft, error) {
	if author.Kind == AuthorHuman && !c.HumanEntries {
		return Draft{}, errNoHumanEntries
	}
	r := &Ruling{Kind: in.Kind, Hypothesis: in.Hypothesis, Refutation: in.Refutation, Reason: in.Reason}
	return s.prepare(c, id, author, Body{Ruling: r})
}

// prepare checks a new entry against the board and returns its draft.
func (s *State) prepare(c *Context, id EntryID, author Author, b Body) (Draft, error) {
	if c.Run == "" {
		return Draft{}, fmt.Errorf("%w: entry without a run", ErrInvalid)
	}
	if err := s.checkNewID(id); err != nil {
		return Draft{}, err
	}
	if err := s.check(author, &b); err != nil {
		return Draft{}, err
	}
	return Draft{ID: id, Run: c.Run, Round: c.Round, Author: author, Body: b}, nil
}

func uniqueIDs(ids []EntryID) []EntryID {
	out := make([]EntryID, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests and the linter**

Run: `go test -race ./internal/work/board/`

Expected: PASS.

Run: `golangci-lint run ./...`

Expected: `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal/work/board/assess.go \
  internal/work/board/assess_test.go \
  internal/work/board/post.go
git commit -m "feat(work): compute stances, convergence and the conclusion"
```

---

### Task 7: Board use cases and the id generator

**Files:**
- Create: `internal/work/app/boards.go`
- Create: `internal/ids/ids.go`
- Test: `internal/work/app/fakes_test.go`
- Test: `internal/work/app/boards_test.go`
- Test: `internal/ids/ids_test.go`

**Interfaces:**
- Consumes: the `board` package (Tasks 4–6).
- Produces:
  - in package `app`, the ports:
    - `EventStore` (`Load`; `Append` with a `decide` callback);
    - `ReadCalls` (`ReadCall`, which returns `ErrCallNotFound` for an unknown id);
    - `Runs` (`BoardContext`);
    - `IDs` (`NewID`);
  - `NewBoards(store EventStore, calls ReadCalls, runs Runs, ids IDs) *Boards`, with `PostEvidence`, `PostHypothesis`, `Retract`, `Rule` and `Assess(ctx, run, at time.Time)`;
  - in package `ids`, `UUIDv7{}`, whose `NewID() string` returns ordered version 7 UUIDs.

- [ ] **Step 1: Add the libraries this task needs**

```bash
go get github.com/google/uuid@v1.6.0
```

- [ ] **Step 2: Write the failing tests**

Create `internal/work/app/fakes_test.go`:

```go
package app_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// memStore is an in-memory EventStore. It serializes appends with one lock,
// as the Postgres store does per run.
type memStore struct {
	mu     sync.Mutex
	events map[board.RunID][]board.Event
	now    time.Time
}

func newMemStore() *memStore {
	return &memStore{events: make(map[board.RunID][]board.Event), now: time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)}
}

func (m *memStore) Load(_ context.Context, run board.RunID) ([]board.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]board.Event(nil), m.events[run]...), nil
}

func (m *memStore) Append(_ context.Context, run board.RunID, decide func([]board.Event) ([]board.Draft, error)) ([]board.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	history := m.events[run]
	drafts, err := decide(append([]board.Event(nil), history...))
	if err != nil {
		return nil, err
	}
	committed := make([]board.Event, 0, len(drafts))
	for i := range drafts {
		d := &drafts[i]
		m.now = m.now.Add(time.Second)
		committed = append(committed, board.Event{
			Seq: int64(len(history) + len(committed) + 1), ID: d.ID, Run: run, Round: d.Round,
			Author: d.Author, At: m.now, Body: d.Body,
		})
	}
	m.events[run] = append(history, committed...)
	return committed, nil
}

// memCalls is an in-memory ReadCalls.
type memCalls map[string]board.ReadCall

func (m memCalls) ReadCall(_ context.Context, id string) (board.ReadCall, error) {
	c, ok := m[id]
	if !ok {
		return board.ReadCall{}, app.ErrCallNotFound
	}
	return c, nil
}

// memRuns is an in-memory Runs.
type memRuns map[board.RunID]board.Context

func (m memRuns) BoardContext(_ context.Context, run board.RunID) (board.Context, error) {
	c, ok := m[run]
	if !ok {
		return board.Context{}, fmt.Errorf("no run %s", run)
	}
	return c, nil
}

// seqIDs issues readable sequential ids.
type seqIDs struct {
	mu sync.Mutex
	n  int
}

func (s *seqIDs) NewID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return fmt.Sprintf("e%d", s.n)
}
```

Create `internal/work/app/boards_test.go`:

```go
package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

var (
	release = board.Author{Kind: board.AuthorAgent, Name: "release-investigator"}
	runtime = board.Author{Kind: board.AuthorAgent, Name: "runtime-investigator"}
	alice   = board.Author{Kind: board.AuthorHuman, Name: "alice@example.com"}
)

func newBoards(t *testing.T) (*app.Boards, *memStore) {
	t.Helper()
	store := newMemStore()
	calls := memCalls{
		"c1": {ID: "c1", Run: "run-1", Agent: release.Name, Server: "cd", Tool: "get_history", Output: []byte("checkout v42 deployed at 10:02")},
		"c2": {ID: "c2", Run: "run-1", Agent: runtime.Name, Server: "logs", Tool: "search", Output: []byte(`{"msg":"NullPointerException in CheckoutHandler"}`)},
	}
	runs := memRuns{"run-1": {
		Run: "run-1", Round: 1, HumanEntries: true,
		Settings: board.Settings{
			Schema:      key.Schema{Fields: []key.Field{{Name: "service", Values: []string{"checkout"}}, {Name: "failureMode", Values: []string{"bad-deploy"}}}},
			Convergence: board.DefaultConvergence,
		},
	}}
	return app.NewBoards(store, calls, runs, &seqIDs{}), store
}

func TestBoardsReachAConclusion(t *testing.T) {
	ctx := context.Background()
	boards, _ := newBoards(t)

	e1, err := boards.PostEvidence(ctx, "run-1", release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42 deployed"})
	require.NoError(t, err)
	h, err := boards.PostHypothesis(ctx, "run-1", release, &board.HypothesisInput{
		Key: key.Key{"service": "checkout", "failureMode": "bad-deploy"}, Statement: "v42 broke checkout", Evidence: []board.EntryID{e1.ID},
	})
	require.NoError(t, err)
	_, err = boards.PostEvidence(ctx, "run-1", runtime, &board.EvidenceInput{
		Claim: "the handler throws", CallID: "c2", Quote: "NullPointerException in CheckoutHandler", Hypothesis: h.ID, Stance: board.Supports,
	})
	require.NoError(t, err)

	a, err := boards.Assess(ctx, "run-1", time.Time{})
	require.NoError(t, err)
	assert.Equal(t, h.ID, a.Conclusion)
	assert.True(t, a.Hypotheses[0].Converged)

	early, err := boards.Assess(ctx, "run-1", h.At)
	require.NoError(t, err)
	assert.False(t, early.Hypotheses[0].Converged, "as of the hypothesis, only its author supported it")
}

func TestPostEvidenceWithUnknownCallFailsProvenance(t *testing.T) {
	boards, store := newBoards(t)

	_, err := boards.PostEvidence(context.Background(), "run-1", release, &board.EvidenceInput{Claim: "x", CallID: "c404", Quote: "v42"})

	require.ErrorIs(t, err, board.ErrProvenance)
	events, _ := store.Load(context.Background(), "run-1")
	assert.Empty(t, events, "a refused post commits nothing")
}

func TestRetractTwiceReturnsTheFirstRetraction(t *testing.T) {
	ctx := context.Background()
	boards, store := newBoards(t)
	e1, err := boards.PostEvidence(ctx, "run-1", release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42"})
	require.NoError(t, err)

	first, err := boards.Retract(ctx, "run-1", release, e1.ID)
	require.NoError(t, err)
	again, err := boards.Retract(ctx, "run-1", release, e1.ID)
	require.NoError(t, err)

	assert.Equal(t, first, again)
	events, _ := store.Load(ctx, "run-1")
	assert.Len(t, events, 2, "the repeat committed nothing")

	_, err = boards.Retract(ctx, "run-1", runtime, e1.ID)
	require.ErrorIs(t, err, board.ErrNotAllowed, "a repeat by someone else is still refused")
}

func TestRuleRejectsAHypothesis(t *testing.T) {
	ctx := context.Background()
	boards, _ := newBoards(t)
	e1, err := boards.PostEvidence(ctx, "run-1", release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42"})
	require.NoError(t, err)
	h, err := boards.PostHypothesis(ctx, "run-1", release, &board.HypothesisInput{
		Key: key.Key{"service": "checkout", "failureMode": "bad-deploy"}, Statement: "v42 broke checkout", Evidence: []board.EntryID{e1.ID},
	})
	require.NoError(t, err)

	_, err = boards.Rule(ctx, "run-1", alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h.ID, Reason: &board.Reason{Code: "wrong-cause"}})
	require.NoError(t, err)

	a, err := boards.Assess(ctx, "run-1", time.Time{})
	require.NoError(t, err)
	assert.True(t, a.Hypotheses[0].Rejected)
	assert.Empty(t, a.Conclusion)
}

func TestUnknownRunIsAnError(t *testing.T) {
	boards, _ := newBoards(t)

	_, err := boards.Assess(context.Background(), "run-404", time.Time{})

	require.Error(t, err)
}
```

Create `internal/ids/ids_test.go`:

```go
package ids_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/ids"
)

func TestUUIDv7IDsAreVersion7AndOrdered(t *testing.T) {
	var gen ids.UUIDv7
	prev := gen.NewID()
	for range 1000 {
		next := gen.NewID()
		u, err := uuid.Parse(next)
		require.NoError(t, err)
		assert.Equal(t, uuid.Version(7), u.Version())
		assert.Greater(t, next, prev)
		prev = next
	}
}
```

- [ ] **Step 3: Run the tests and watch them fail**

Run: `go test ./internal/work/app/ ./internal/ids/`

Expected: FAIL: `internal/work/app: no non-test Go files` and `internal/ids: no non-test Go files`.

- [ ] **Step 4: Write the implementation**

Create `internal/work/app/boards.go`:

```go
// Package app holds the Work context's use cases and declares the ports they need.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// EventStore keeps each run's board events.
type EventStore interface {
	// Load returns a run's events in commit order.
	Load(ctx context.Context, run board.RunID) ([]board.Event, error)
	// Append serializes writers per run. decide sees every committed event of
	// the run and returns the entries to commit; an error from decide commits
	// nothing and is returned as is.
	Append(ctx context.Context, run board.RunID, decide func(history []board.Event) ([]board.Draft, error)) ([]board.Event, error)
}

// ReadCalls looks up gateway read calls in the audit store.
type ReadCalls interface {
	// ReadCall returns the call with the given id, or ErrCallNotFound.
	ReadCall(ctx context.Context, id string) (board.ReadCall, error)
}

// ErrCallNotFound is returned by ReadCalls when no read call has the id.
var ErrCallNotFound = errors.New("app: read call not found")

// Runs tells the board rules what they need to know about a run.
type Runs interface {
	BoardContext(ctx context.Context, run board.RunID) (board.Context, error)
}

// IDs issues new entry identifiers.
type IDs interface {
	NewID() string
}

// Boards posts entries to run boards and assesses them.
type Boards struct {
	store EventStore
	calls ReadCalls
	runs  Runs
	ids   IDs
}

// NewBoards returns the board use cases over the given ports.
func NewBoards(store EventStore, calls ReadCalls, runs Runs, ids IDs) *Boards {
	return &Boards{store: store, calls: calls, runs: runs, ids: ids}
}

// PostEvidence checks and commits an evidence entry.
func (b *Boards) PostEvidence(ctx context.Context, run board.RunID, author board.Author, in *board.EvidenceInput) (board.Event, error) {
	bc, err := b.runs.BoardContext(ctx, run)
	if err != nil {
		return board.Event{}, fmt.Errorf("load run %s: %w", run, err)
	}
	var call *board.ReadCall
	if author.Kind == board.AuthorAgent && in.CallID != "" {
		found, err := b.calls.ReadCall(ctx, in.CallID)
		switch {
		case errors.Is(err, ErrCallNotFound):
			// The board rules refuse evidence that cites no known call.
		case err != nil:
			return board.Event{}, fmt.Errorf("look up read call %s: %w", in.CallID, err)
		default:
			call = &found
		}
	}
	id := board.EntryID(b.ids.NewID())
	return b.appendOne(ctx, &bc, func(s *board.State) (board.Draft, error) {
		return s.NewEvidence(&bc, id, author, in, call)
	})
}

// PostHypothesis checks and commits a hypothesis, merging it into an earlier
// hypothesis with an equal key.
func (b *Boards) PostHypothesis(ctx context.Context, run board.RunID, author board.Author, in *board.HypothesisInput) (board.Event, error) {
	bc, err := b.runs.BoardContext(ctx, run)
	if err != nil {
		return board.Event{}, fmt.Errorf("load run %s: %w", run, err)
	}
	id := board.EntryID(b.ids.NewID())
	return b.appendOne(ctx, &bc, func(s *board.State) (board.Draft, error) {
		return s.NewHypothesis(&bc, id, author, in)
	})
}

// Retract retracts an agent's own evidence. Retracting evidence that is
// already retracted returns the earlier retraction and commits nothing, so a
// repeated call does no harm (spec Section 5.2).
func (b *Boards) Retract(ctx context.Context, run board.RunID, author board.Author, evidence board.EntryID) (board.Event, error) {
	bc, err := b.runs.BoardContext(ctx, run)
	if err != nil {
		return board.Event{}, fmt.Errorf("load run %s: %w", run, err)
	}
	id := board.EntryID(b.ids.NewID())
	var earlier board.Event
	committed, err := b.store.Append(ctx, run, func(history []board.Event) ([]board.Draft, error) {
		s, err := board.Replay(bc.Settings, history)
		if err != nil {
			return nil, err
		}
		if rid, done := s.RetractionOf(evidence); done {
			if e, _ := s.Event(rid); e.Author == author {
				earlier = e
				return nil, nil
			}
		}
		d, err := s.NewRetraction(&bc, id, author, evidence)
		if err != nil {
			return nil, err
		}
		return []board.Draft{d}, nil
	})
	if err != nil {
		return board.Event{}, err
	}
	if len(committed) == 0 {
		return earlier, nil
	}
	return committed[0], nil
}

// Rule checks and commits a human's ruling on a hypothesis or a refutation.
func (b *Boards) Rule(ctx context.Context, run board.RunID, author board.Author, in *board.RulingInput) (board.Event, error) {
	bc, err := b.runs.BoardContext(ctx, run)
	if err != nil {
		return board.Event{}, fmt.Errorf("load run %s: %w", run, err)
	}
	id := board.EntryID(b.ids.NewID())
	return b.appendOne(ctx, &bc, func(s *board.State) (board.Draft, error) {
		return s.NewRuling(&bc, id, author, in)
	})
}

// Assess returns a run's ranked hypotheses and conclusion as of at. A zero at
// assesses every committed entry.
func (b *Boards) Assess(ctx context.Context, run board.RunID, at time.Time) (board.Assessment, error) {
	bc, err := b.runs.BoardContext(ctx, run)
	if err != nil {
		return board.Assessment{}, fmt.Errorf("load run %s: %w", run, err)
	}
	events, err := b.store.Load(ctx, run)
	if err != nil {
		return board.Assessment{}, fmt.Errorf("load board of run %s: %w", run, err)
	}
	if !at.IsZero() {
		events = board.AsOf(events, at)
	}
	s, err := board.Replay(bc.Settings, events)
	if err != nil {
		return board.Assessment{}, err
	}
	return s.Assess(), nil
}

func (b *Boards) appendOne(ctx context.Context, bc *board.Context, decide func(*board.State) (board.Draft, error)) (board.Event, error) {
	committed, err := b.store.Append(ctx, bc.Run, func(history []board.Event) ([]board.Draft, error) {
		s, err := board.Replay(bc.Settings, history)
		if err != nil {
			return nil, err
		}
		d, err := decide(s)
		if err != nil {
			return nil, err
		}
		return []board.Draft{d}, nil
	})
	if err != nil {
		return board.Event{}, err
	}
	return committed[0], nil
}
```

Create `internal/ids/ids.go`:

```go
// Package ids issues identifiers for platform records.
package ids

import "github.com/google/uuid"

// UUIDv7 issues time-ordered UUIDs (RFC 9562, version 7). Within one process
// each new id sorts after the previous one.
type UUIDv7 struct{}

// NewID returns a new version 7 UUID in its canonical text form.
func (UUIDv7) NewID() string {
	return uuid.Must(uuid.NewV7()).String()
}
```

- [ ] **Step 5: Run the tests and the linter**

Run: `go mod tidy`

Expected: PASS.

Run: `go test -race ./internal/work/app/ ./internal/ids/`

Expected: PASS.

Run: `golangci-lint run ./...`

Expected: `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add go.mod \
  go.sum \
  internal/ids/ids.go \
  internal/ids/ids_test.go \
  internal/work/app/boards.go \
  internal/work/app/boards_test.go \
  internal/work/app/fakes_test.go
git commit -m "feat(work): add the board use cases and the UUIDv7 id generator"
```

---

### Task 8: Database connection, migrations and test containers

**Files:**
- Create: `internal/db/migrations/0001_board_events.sql`
- Create: `internal/db/db.go`
- Create: `internal/testsupport/pgtest/pgtest.go`
- Test: `internal/db/main_integration_test.go`
- Test: `internal/db/db_integration_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `db.Open(ctx, url string) (*pgxpool.Pool, error)` and `db.Migrate(ctx, *pgxpool.Pool) error`;
  - the `board_events` table, append-only;
  - `pgtest.Main(*testing.M) int`, `pgtest.Pool(*testing.T) *pgxpool.Pool` and `pgtest.Image`.

- [ ] **Step 1: Add the libraries this task needs**

```bash
go get github.com/jackc/pgx/v5@v5.10.0 github.com/jackc/pgx/v5/pgxpool@v5.10.0 github.com/testcontainers/testcontainers-go@v0.44.0 github.com/testcontainers/testcontainers-go/modules/postgres@v0.44.0
```

- [ ] **Step 2: Write the failing tests**

Create `internal/db/main_integration_test.go`:

```go
//go:build integration

package db_test

import (
	"os"
	"testing"

	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}
```

Create `internal/db/db_integration_test.go`:

```go
//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
)

func TestMigrateTwiceChangesNothing(t *testing.T) {
	pool := pgtest.Pool(t)

	require.NoError(t, db.Migrate(t.Context(), pool))

	var applied int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM schema_migrations").Scan(&applied))
	assert.Equal(t, 1, applied)
}

func TestBoardEventsAreAppendOnly(t *testing.T) {
	ctx := t.Context()
	pool := pgtest.Pool(t)
	_, err := pool.Exec(ctx, `INSERT INTO board_events
		(tenant_id, run_id, seq, id, round, kind, author_kind, author_name, committed_at, body)
		VALUES ('t', 'append-only', 1, 'append-only-1', 1, 'evidence', 'agent', 'a', now(), '{}')`)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, "UPDATE board_events SET author_name = 'b' WHERE id = 'append-only-1'")
	require.ErrorContains(t, err, "append-only")
	_, err = pool.Exec(ctx, "DELETE FROM board_events WHERE id = 'append-only-1'")
	require.ErrorContains(t, err, "append-only")
	_, err = pool.Exec(ctx, "TRUNCATE board_events")
	require.ErrorContains(t, err, "append-only")

	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL agentswarm.retention = 'on'"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "DELETE FROM board_events WHERE id = 'append-only-1'")
		return err
	})
	require.NoError(t, err, "the retention job may delete")
}

func TestOpenFailsOnABadURL(t *testing.T) {
	_, err := db.Open(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")

	require.Error(t, err)
}
```

- [ ] **Step 3: Run the tests and watch them fail**

Run: `go test -tags integration ./internal/db/`

Expected: FAIL: `no required module provides package github.com/jchen0426/agent-swarm/internal/testsupport/pgtest`, because the package does not exist yet.

- [ ] **Step 4: Write the implementation**

Create `internal/db/migrations/0001_board_events.sql`:

```sql
-- Board events: the append-only log of every run's blackboard (spec Section 4.2).
CREATE TABLE board_events (
    tenant_id    text        NOT NULL,
    run_id       text        NOT NULL,
    seq          bigint      NOT NULL CHECK (seq > 0),
    id           text        NOT NULL,
    round        integer     NOT NULL CHECK (round >= 0),
    kind         text        NOT NULL,
    author_kind  text        NOT NULL,
    author_name  text        NOT NULL,
    committed_at timestamptz NOT NULL,
    body         jsonb       NOT NULL,
    PRIMARY KEY (tenant_id, run_id, seq),
    UNIQUE (tenant_id, id)
);

-- Entries never change. Only the retention job deletes them, and only after it
-- sets agentswarm.retention to 'on' for its own transaction.
CREATE FUNCTION board_events_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('agentswarm.retention', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'board_events is append-only';
END;
$$;

CREATE TRIGGER board_events_append_only
    BEFORE UPDATE OR DELETE ON board_events
    FOR EACH ROW EXECUTE FUNCTION board_events_guard();

CREATE TRIGGER board_events_no_truncate
    BEFORE TRUNCATE ON board_events
    FOR EACH STATEMENT EXECUTE FUNCTION board_events_guard();
```

Create `internal/db/db.go`:

```go
// Package db opens the platform's Postgres database and applies its schema
// migrations.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationLock is the advisory lock that serializes concurrent migration runs.
const migrationLock = 7342601

// Open connects to Postgres and checks the connection.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// Migrate applies, in file-name order and in one transaction, every embedded
// migration that has not been applied yet. Running it again changes nothing.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (err error) {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	slices.Sort(names)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLock); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`
	if _, err = tx.Exec(ctx, createTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	for _, name := range names {
		if err := apply(ctx, tx, name); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

// apply runs one migration file unless it has run before.
func apply(ctx context.Context, tx pgx.Tx, name string) error {
	base := path.Base(name)
	var done bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)", base).Scan(&done); err != nil {
		return fmt.Errorf("check migration %s: %w", base, err)
	}
	if done {
		return nil
	}
	script, err := migrations.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", base, err)
	}
	// Without arguments, pgx sends the script over the simple protocol, which
	// allows several statements in one call.
	if _, err := tx.Exec(ctx, string(script)); err != nil {
		return fmt.Errorf("apply migration %s: %w", base, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (name) VALUES ($1)", base); err != nil {
		return fmt.Errorf("record migration %s: %w", base, err)
	}
	return nil
}
```

Create `internal/testsupport/pgtest/pgtest.go`:

```go
// Package pgtest runs a disposable Postgres container for integration tests.
//
// A test package calls Main from its TestMain, so that its tests share one
// container, and Pool from each test that needs a database. Tests share the
// database, so each test uses its own run ids.
package pgtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/jchen0426/agent-swarm/internal/db"
)

// Image is the Postgres image the integration tests run against.
const Image = "postgres:17-alpine"

var url string

// Main starts one Postgres container, runs the package's tests and removes
// the container. Use it as: func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }.
func Main(m *testing.M) int {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("agentswarm"),
		postgres.WithUsername("agentswarm"),
		postgres.WithPassword("agentswarm"),
		postgres.BasicWaitStrategies(),
	)
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			slog.ErrorContext(ctx, "remove postgres container", "error", err)
		}
	}()
	if err != nil {
		slog.ErrorContext(ctx, "start postgres container", "error", err)
		return 1
	}
	url, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		slog.ErrorContext(ctx, "read postgres connection string", "error", err)
		return 1
	}
	return m.Run()
}

// Pool returns a connection pool to the package's container, with every
// migration applied. The pool closes when the test ends.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	require.NotEmpty(t, url, "call pgtest.Main from TestMain first")
	pool, err := db.Open(t.Context(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, db.Migrate(t.Context(), pool))
	return pool
}
```

- [ ] **Step 5: Run the tests and the linter**

Run: `go mod tidy`

Expected: PASS.

Run: `go test -race -count=1 -tags integration ./internal/db/`

Expected: PASS.

Run: `golangci-lint run ./...`

Expected: `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add go.mod \
  go.sum \
  internal/db/db.go \
  internal/db/db_integration_test.go \
  internal/db/main_integration_test.go \
  internal/db/migrations/0001_board_events.sql \
  internal/testsupport/pgtest/pgtest.go
git commit -m "feat(db): add migrations, the board_events table and test containers"
```

---

### Task 9: Postgres event store

**Files:**
- Create: `internal/work/postgres/events.go`
- Test: `internal/work/postgres/main_integration_test.go`
- Test: `internal/work/postgres/events_integration_test.go`
- Test: `internal/work/postgres/boards_integration_test.go`

**Interfaces:**
- Consumes: `app.EventStore` (Task 7), the `board_events` table and `pgtest` (Task 8), and `ids.UUIDv7` (Task 7).
- Produces: `postgres.NewEventStore(pool *pgxpool.Pool, tenant string) *EventStore`, which implements `app.EventStore`.

- [ ] **Step 1: Write the failing tests**

Create `internal/work/postgres/main_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"os"
	"testing"

	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}
```

Create `internal/work/postgres/events_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var release = board.Author{Kind: board.AuthorAgent, Name: "release-investigator"}

func evidenceDraft(run board.RunID, id board.EntryID, quote string) board.Draft {
	return board.Draft{ID: id, Run: run, Round: 1, Author: release, Body: board.Body{Evidence: &board.Evidence{
		Claim:      "saw " + quote,
		Provenance: board.Provenance{Kind: board.FromReadCall, CallID: "c-" + string(id), Quote: quote, Source: "cd/get_history"},
	}}}
}

func TestAppendAndLoadRoundTrip(t *testing.T) {
	ctx := t.Context()
	store := postgres.NewEventStore(pgtest.Pool(t), "tenant-a")
	run := board.RunID("round-trip")

	committed, err := store.Append(ctx, run, func(history []board.Event) ([]board.Draft, error) {
		assert.Empty(t, history)
		return []board.Draft{evidenceDraft(run, "rt-1", "v42"), evidenceDraft(run, "rt-2", "v43")}, nil
	})
	require.NoError(t, err)
	require.Len(t, committed, 2)
	assert.Equal(t, []int64{1, 2}, []int64{committed[0].Seq, committed[1].Seq})
	assert.False(t, committed[1].At.Before(committed[0].At))

	loaded, err := store.Load(ctx, run)
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	assert.Equal(t, committed[0].ID, loaded[0].ID)
	assert.Equal(t, release, loaded[0].Author)
	assert.Equal(t, run, loaded[0].Run)
	assert.Equal(t, committed[0].Body, loaded[0].Body)
	assert.True(t, committed[0].At.Equal(loaded[0].At))
}

func TestAppendCommitsNothingWhenDecideFails(t *testing.T) {
	ctx := t.Context()
	store := postgres.NewEventStore(pgtest.Pool(t), "tenant-a")
	run := board.RunID("decide-fails")
	refused := errors.New("refused by the rules")

	_, err := store.Append(ctx, run, func([]board.Event) ([]board.Draft, error) { return nil, refused })

	require.ErrorIs(t, err, refused)
	loaded, err := store.Load(ctx, run)
	require.NoError(t, err)
	assert.Empty(t, loaded)
}

func TestConcurrentAppendsGetDistinctSequenceNumbers(t *testing.T) {
	ctx := t.Context()
	store := postgres.NewEventStore(pgtest.Pool(t), "tenant-a")
	run := board.RunID("concurrent")

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			_, err := store.Append(ctx, run, func(history []board.Event) ([]board.Draft, error) {
				id := board.EntryID("cc-" + string(rune('a'+i)))
				return []board.Draft{evidenceDraft(run, id, "v42")}, nil
			})
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	loaded, err := store.Load(ctx, run)
	require.NoError(t, err)
	require.Len(t, loaded, 20)
	for i := range loaded {
		assert.Equal(t, int64(i+1), loaded[i].Seq)
		if i > 0 {
			assert.False(t, loaded[i].At.Before(loaded[i-1].At), "commit times never go backwards")
		}
	}
}

func TestTenantsAndRunsAreSeparate(t *testing.T) {
	ctx := t.Context()
	pool := pgtest.Pool(t)
	a := postgres.NewEventStore(pool, "tenant-a")
	b := postgres.NewEventStore(pool, "tenant-b")

	_, err := a.Append(ctx, "shared-run", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{evidenceDraft("shared-run", "sep-1", "v42")}, nil
	})
	require.NoError(t, err)

	other, err := b.Load(ctx, "shared-run")
	require.NoError(t, err)
	assert.Empty(t, other)
	otherRun, err := a.Load(ctx, "another-run")
	require.NoError(t, err)
	assert.Empty(t, otherRun)
}

func TestAppendRefusesADraftForAnotherRun(t *testing.T) {
	store := postgres.NewEventStore(pgtest.Pool(t), "tenant-a")

	_, err := store.Append(t.Context(), "run-x", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{evidenceDraft("run-y", "wrong-run-1", "v42")}, nil
	})

	require.ErrorContains(t, err, "belongs to run run-y")
}
```

Create `internal/work/postgres/boards_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/ids"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

type staticCalls map[string]board.ReadCall

func (s staticCalls) ReadCall(_ context.Context, id string) (board.ReadCall, error) {
	c, ok := s[id]
	if !ok {
		return board.ReadCall{}, app.ErrCallNotFound
	}
	return c, nil
}

type staticRuns map[board.RunID]board.Context

func (s staticRuns) BoardContext(_ context.Context, run board.RunID) (board.Context, error) {
	return s[run], nil
}

// TestBoardsOverPostgres runs the board use cases against the real event
// store: two agents converge, and the conclusion holds after a reload.
func TestBoardsOverPostgres(t *testing.T) {
	ctx := t.Context()
	runtime := board.Author{Kind: board.AuthorAgent, Name: "runtime-investigator"}
	run := board.RunID("boards-over-postgres")
	calls := staticCalls{
		"c1": {ID: "c1", Run: run, Agent: release.Name, Server: "cd", Tool: "get_history", Output: []byte("checkout v42 deployed at 10:02")},
		"c2": {ID: "c2", Run: run, Agent: runtime.Name, Server: "logs", Tool: "search", Output: []byte("NullPointerException in CheckoutHandler")},
	}
	runs := staticRuns{run: {Run: run, Round: 1, Settings: board.Settings{
		Schema:      key.Schema{Fields: []key.Field{{Name: "service", Values: []string{"checkout"}}, {Name: "failureMode", Values: []string{"bad-deploy"}}}},
		Convergence: board.DefaultConvergence,
	}}}
	boards := app.NewBoards(postgres.NewEventStore(pgtest.Pool(t), "tenant-a"), calls, runs, ids.UUIDv7{})

	e1, err := boards.PostEvidence(ctx, run, release, &board.EvidenceInput{Claim: "v42 shipped", CallID: "c1", Quote: "v42 deployed"})
	require.NoError(t, err)
	h, err := boards.PostHypothesis(ctx, run, release, &board.HypothesisInput{
		Key: key.Key{"service": "checkout", "failureMode": "bad-deploy"}, Statement: "v42 broke checkout", Evidence: []board.EntryID{e1.ID},
	})
	require.NoError(t, err)
	_, err = boards.PostEvidence(ctx, run, runtime, &board.EvidenceInput{
		Claim: "the handler throws", CallID: "c2", Quote: "NullPointerException", Hypothesis: h.ID, Stance: board.Supports,
	})
	require.NoError(t, err)

	a, err := boards.Assess(ctx, run, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, h.ID, a.Conclusion)
	assert.True(t, a.Hypotheses[0].Converged)
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test -tags integration ./internal/work/postgres/`

Expected: FAIL: `internal/work/postgres: no non-test Go files`.

- [ ] **Step 3: Write the implementation**

Create `internal/work/postgres/events.go`:

```go
// Package postgres stores the Work context's data in Postgres. It implements
// the ports that the Work application declares.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// EventStore keeps board events in the board_events table. It implements
// app.EventStore.
type EventStore struct {
	pool   *pgxpool.Pool
	tenant string
}

// NewEventStore returns an event store for one tenant's rows. A deployment
// serves one organization, but its records still carry a tenant id (spec
// Section 1.4).
func NewEventStore(pool *pgxpool.Pool, tenant string) *EventStore {
	return &EventStore{pool: pool, tenant: tenant}
}

const selectEvents = `
SELECT seq, id, round, author_kind, author_name, committed_at, body
FROM board_events
WHERE tenant_id = $1 AND run_id = $2
ORDER BY seq`

const insertEvent = `
INSERT INTO board_events
    (tenant_id, run_id, seq, id, round, kind, author_kind, author_name, committed_at, body)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, greatest(clock_timestamp(), $9::timestamptz), $10)
RETURNING committed_at`

// Load returns a run's events in commit order.
func (s *EventStore) Load(ctx context.Context, run board.RunID) ([]board.Event, error) {
	return loadEvents(ctx, s.pool, s.tenant, run)
}

// Append commits the entries that decide returns, after the run's last event.
// A transaction-scoped advisory lock serializes appends per run, so decide
// always sees the latest history. Commit times come from the database clock
// and never go backwards within a run.
func (s *EventStore) Append(ctx context.Context, run board.RunID, decide func([]board.Event) ([]board.Draft, error)) (_ []board.Event, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin append: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", s.tenant+"/"+string(run)); err != nil {
		return nil, fmt.Errorf("lock run %s: %w", run, err)
	}
	history, err := loadEvents(ctx, tx, s.tenant, run)
	if err != nil {
		return nil, err
	}
	drafts, err := decide(history)
	if err != nil {
		return nil, err
	}
	var lastSeq int64
	var lastAt time.Time
	if n := len(history); n > 0 {
		lastSeq, lastAt = history[n-1].Seq, history[n-1].At
	}
	committed := make([]board.Event, 0, len(drafts))
	for i := range drafts {
		d := &drafts[i]
		if d.Run != run {
			return nil, fmt.Errorf("entry %s belongs to run %s, not %s", d.ID, d.Run, run)
		}
		e := board.Event{Seq: lastSeq + 1, ID: d.ID, Run: run, Round: d.Round, Author: d.Author, Body: d.Body}
		err = tx.QueryRow(ctx, insertEvent,
			s.tenant, string(run), e.Seq, string(e.ID), e.Round, string(e.Body.Kind()),
			string(e.Author.Kind), e.Author.Name, lastAt, e.Body,
		).Scan(&e.At)
		if err != nil {
			return nil, fmt.Errorf("insert entry %s: %w", d.ID, err)
		}
		lastSeq, lastAt = e.Seq, e.At
		committed = append(committed, e)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit append: %w", err)
	}
	return committed, nil
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func loadEvents(ctx context.Context, q querier, tenant string, run board.RunID) ([]board.Event, error) {
	rows, err := q.Query(ctx, selectEvents, tenant, string(run))
	if err != nil {
		return nil, fmt.Errorf("query board events of run %s: %w", run, err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (board.Event, error) {
		e := board.Event{Run: run}
		var id, authorKind string
		if err := row.Scan(&e.Seq, &id, &e.Round, &authorKind, &e.Author.Name, &e.At, &e.Body); err != nil {
			return board.Event{}, err
		}
		e.ID, e.Author.Kind = board.EntryID(id), board.AuthorKind(authorKind)
		return e, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read board events of run %s: %w", run, err)
	}
	return events, nil
}
```

- [ ] **Step 4: Run the tests and the linter**

Run: `go test -race -count=1 -tags integration ./internal/work/postgres/`

Expected: PASS.

Run: `golangci-lint run ./...`

Expected: `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal/work/postgres/boards_integration_test.go \
  internal/work/postgres/events.go \
  internal/work/postgres/events_integration_test.go \
  internal/work/postgres/main_integration_test.go
git commit -m "feat(work): store board events in Postgres"
```

---

### Task 10: The conclude command

**Files:**
- Create: `cmd/agentswarm/conclude.go`
- Modify (replace the whole file): `cmd/agentswarm/run.go`
- Test: `cmd/agentswarm/testdata/board.json`
- Test, modify (replace the whole file): `cmd/agentswarm/run_test.go`
- Test: `test/e2e/testdata/board.json`
- Test: `test/e2e/testdata/inconsistent.json`
- Test, modify (replace the whole file): `test/e2e/cli_test.go`

**Interfaces:**
- Consumes: `board.Replay`, `board.AsOf` and `(*State).Assess` (Tasks 5–6), and `report` and the exit codes (Task 1).
- Produces: `agentswarm conclude -board FILE [-at RFC3339]`. The board file is JSON: `{"settings": board.Settings, "events": [board.Event]}`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/agentswarm/testdata/board.json`:

```json
{
  "settings": {
    "schema": {
      "fields": [
        {"name": "service", "values": ["checkout", "payments"]},
        {"name": "failureMode", "values": ["bad-deploy", "resource-exhaustion"]}
      ]
    },
    "convergence": {"minContributors": 2, "minSources": 2, "refutationMargin": 2}
  },
  "events": [
    {
      "seq": 1, "id": "e1", "run": "run-1", "round": 1,
      "author": {"kind": "agent", "name": "release-investigator"},
      "at": "2026-10-12T10:05:00Z",
      "body": {"evidence": {
        "claim": "checkout v42 shipped two minutes before the alert",
        "provenance": {"kind": "read_call", "callId": "c1", "quote": "checkout v42 deployed at 10:02", "source": "cd/get_history"}
      }}
    },
    {
      "seq": 2, "id": "h1", "run": "run-1", "round": 1,
      "author": {"kind": "agent", "name": "release-investigator"},
      "at": "2026-10-12T10:05:10Z",
      "body": {"hypothesis": {
        "key": {"service": "checkout", "failureMode": "bad-deploy"},
        "statement": "v42 broke checkout",
        "evidence": ["e1"]
      }}
    },
    {
      "seq": 3, "id": "e2", "run": "run-1", "round": 2,
      "author": {"kind": "agent", "name": "runtime-investigator"},
      "at": "2026-10-12T10:07:00Z",
      "body": {"evidence": {
        "claim": "the checkout handler throws since 10:03",
        "provenance": {"kind": "read_call", "callId": "c2", "quote": "NullPointerException in CheckoutHandler", "source": "logs/search"},
        "hypothesis": "h1", "stance": "supports"
      }}
    },
    {
      "seq": 4, "id": "e3", "run": "run-1", "round": 2,
      "author": {"kind": "agent", "name": "dependency-investigator"},
      "at": "2026-10-12T10:07:30Z",
      "body": {"evidence": {
        "claim": "the payments database pool is nearly full",
        "provenance": {"kind": "read_call", "callId": "c3", "quote": "pool usage 98%", "source": "database/pool_stats"}
      }}
    },
    {
      "seq": 5, "id": "h2", "run": "run-1", "round": 2,
      "author": {"kind": "agent", "name": "dependency-investigator"},
      "at": "2026-10-12T10:07:40Z",
      "body": {"hypothesis": {
        "key": {"service": "payments", "failureMode": "resource-exhaustion"},
        "statement": "the payments pool is exhausted",
        "evidence": ["e3"]
      }}
    }
  ]
}
```

Replace `cmd/agentswarm/run_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

func runCommand(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestVersion(t *testing.T) {
	out, _, code := runCommand("version")

	require.Equal(t, exitOK, code)
	var info map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &info))
	assert.Equal(t, "dev", info["version"])
}

func TestUsageErrors(t *testing.T) {
	_, errOut, code := runCommand()
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "Usage: agentswarm")

	_, errOut, code = runCommand("frobnicate")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, `unknown command "frobnicate"`)

	_, errOut, code = runCommand("conclude")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "-board is required")
}

func TestConclude(t *testing.T) {
	out, errOut, code := runCommand("conclude", "-board", "testdata/board.json")

	require.Equal(t, exitOK, code, errOut)
	var a board.Assessment
	require.NoError(t, json.Unmarshal([]byte(out), &a))
	assert.Equal(t, board.EntryID("h1"), a.Conclusion)
	require.Len(t, a.Hypotheses, 2)
	assert.True(t, a.Hypotheses[0].Converged)
	assert.Equal(t, []board.Source{"cd/get_history", "logs/search"}, a.Hypotheses[0].Sources)
}

func TestConcludeAsOfAMoment(t *testing.T) {
	out, errOut, code := runCommand("conclude", "-board", "testdata/board.json", "-at", "2026-10-12T10:06:00Z")

	require.Equal(t, exitOK, code, errOut)
	var a board.Assessment
	require.NoError(t, json.Unmarshal([]byte(out), &a))
	require.Len(t, a.Hypotheses, 1, "h2 came later")
	assert.False(t, a.Hypotheses[0].Converged)
	assert.Equal(t, board.EntryID("h1"), a.Conclusion, "the leading hypothesis")
}

func TestConcludeAcceptsACutoffWithAnOffset(t *testing.T) {
	// 12:06+02:00 is 10:06 UTC, the cutoff of TestConcludeAsOfAMoment.
	out, errOut, code := runCommand("conclude", "-board", "testdata/board.json", "-at", "2026-10-12T12:06:00+02:00")

	require.Equal(t, exitOK, code, errOut)
	var a board.Assessment
	require.NoError(t, json.Unmarshal([]byte(out), &a))
	assert.Len(t, a.Hypotheses, 1)
}

func TestConcludeReportsBadInput(t *testing.T) {
	_, errOut, code := runCommand("conclude", "-board", "testdata/missing.json")
	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "read board file")

	_, errOut, code = runCommand("conclude", "-board", "testdata/board.json", "-at", "yesterday")
	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "parse -at")
}
```

Create `test/e2e/testdata/board.json`:

```json
{
  "settings": {
    "schema": {
      "fields": [
        {"name": "service", "values": ["checkout", "payments"]},
        {"name": "failureMode", "values": ["bad-deploy", "resource-exhaustion"]}
      ]
    },
    "convergence": {"minContributors": 2, "minSources": 2, "refutationMargin": 2}
  },
  "events": [
    {
      "seq": 1, "id": "e1", "run": "run-1", "round": 1,
      "author": {"kind": "agent", "name": "release-investigator"},
      "at": "2026-10-12T10:05:00Z",
      "body": {"evidence": {
        "claim": "checkout v42 shipped two minutes before the alert",
        "provenance": {"kind": "read_call", "callId": "c1", "quote": "checkout v42 deployed at 10:02", "source": "cd/get_history"}
      }}
    },
    {
      "seq": 2, "id": "h1", "run": "run-1", "round": 1,
      "author": {"kind": "agent", "name": "release-investigator"},
      "at": "2026-10-12T10:05:10Z",
      "body": {"hypothesis": {
        "key": {"service": "checkout", "failureMode": "bad-deploy"},
        "statement": "v42 broke checkout",
        "evidence": ["e1"]
      }}
    },
    {
      "seq": 3, "id": "e2", "run": "run-1", "round": 2,
      "author": {"kind": "agent", "name": "runtime-investigator"},
      "at": "2026-10-12T10:07:00Z",
      "body": {"evidence": {
        "claim": "the checkout handler throws since 10:03",
        "provenance": {"kind": "read_call", "callId": "c2", "quote": "NullPointerException in CheckoutHandler", "source": "logs/search"},
        "hypothesis": "h1", "stance": "supports"
      }}
    },
    {
      "seq": 4, "id": "e3", "run": "run-1", "round": 2,
      "author": {"kind": "agent", "name": "dependency-investigator"},
      "at": "2026-10-12T10:07:30Z",
      "body": {"evidence": {
        "claim": "the payments database pool is nearly full",
        "provenance": {"kind": "read_call", "callId": "c3", "quote": "pool usage 98%", "source": "database/pool_stats"}
      }}
    },
    {
      "seq": 5, "id": "h2", "run": "run-1", "round": 2,
      "author": {"kind": "agent", "name": "dependency-investigator"},
      "at": "2026-10-12T10:07:40Z",
      "body": {"hypothesis": {
        "key": {"service": "payments", "failureMode": "resource-exhaustion"},
        "statement": "the payments pool is exhausted",
        "evidence": ["e3"]
      }}
    }
  ]
}
```

Create `test/e2e/testdata/inconsistent.json`:

```json
{
  "settings": {
    "schema": {"fields": [{"name": "service", "values": ["checkout"]}]},
    "convergence": {"minContributors": 2, "minSources": 2, "refutationMargin": 2}
  },
  "events": [
    {
      "seq": 1, "id": "h1", "run": "run-1", "round": 1,
      "author": {"kind": "agent", "name": "release-investigator"},
      "at": "2026-10-12T10:05:10Z",
      "body": {"hypothesis": {"key": {"service": "checkout"}, "statement": "v42 broke checkout", "evidence": ["e-missing"]}}
    }
  ]
}
```

Replace `test/e2e/cli_test.go`:

```go
//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionPrintsBuildInfo(t *testing.T) {
	out, errOut, code := agentswarm(t, "version")

	require.Equal(t, 0, code, errOut)
	var info struct {
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &info))
	assert.NotEmpty(t, info.Version)
}

func TestUnknownCommandExitsWithUsage(t *testing.T) {
	_, errOut, code := agentswarm(t, "frobnicate")

	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, `unknown command "frobnicate"`)
}

// assessment is the part of the conclude output these tests read.
type assessment struct {
	Conclusion string `json:"conclusion"`
	Hypotheses []struct {
		ID        string   `json:"id"`
		Converged bool     `json:"converged"`
		Sources   []string `json:"sources"`
	} `json:"hypotheses"`
}

func TestConcludeAssessesARecordedBoard(t *testing.T) {
	out, errOut, code := agentswarm(t, "conclude", "-board", "testdata/board.json")

	require.Equal(t, 0, code, errOut)
	var a assessment
	require.NoError(t, json.Unmarshal([]byte(out), &a))
	assert.Equal(t, "h1", a.Conclusion)
	require.Len(t, a.Hypotheses, 2)
	assert.True(t, a.Hypotheses[0].Converged)
	assert.Equal(t, []string{"cd/get_history", "logs/search"}, a.Hypotheses[0].Sources)
}

func TestConcludeAsOfACutoff(t *testing.T) {
	out, errOut, code := agentswarm(t, "conclude", "-board", "testdata/board.json", "-at", "2026-10-12T10:06:00Z")

	require.Equal(t, 0, code, errOut)
	var a assessment
	require.NoError(t, json.Unmarshal([]byte(out), &a))
	require.Len(t, a.Hypotheses, 1)
	assert.False(t, a.Hypotheses[0].Converged)
	assert.Equal(t, "h1", a.Conclusion)
}

func TestConcludeRejectsAnInconsistentBoard(t *testing.T) {
	_, errOut, code := agentswarm(t, "conclude", "-board", "testdata/inconsistent.json")

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "no such entry")
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./cmd/agentswarm/`

Expected: FAIL: `TestUsageErrors`, `TestConclude` and the other conclude tests report `unknown command "conclude"`.

- [ ] **Step 3: Write the implementation**

Create `cmd/agentswarm/conclude.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// boardFile is the input of the conclude command: a run's board settings and
// its events in commit order. Provenance in it is trusted, as in a replay.
type boardFile struct {
	Settings board.Settings `json:"settings"`
	Events   []board.Event  `json:"events"`
}

func runConclude(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("conclude", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("board", "", "board file to assess (JSON)")
	at := flags.String("at", "", "assess as of this time, in RFC 3339 (default: every event)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if *path == "" {
		_, _ = fmt.Fprintln(stderr, "agentswarm conclude: -board is required")
		return exitUsage
	}
	assessment, err := conclude(*path, *at)
	return report(stdout, stderr, assessment, err)
}

// conclude replays the board in the file and assesses it, as of at when set.
func conclude(path, at string) (board.Assessment, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return board.Assessment{}, fmt.Errorf("read board file: %w", err)
	}
	var f boardFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return board.Assessment{}, fmt.Errorf("parse board file: %w", err)
	}
	events := f.Events
	if at != "" {
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return board.Assessment{}, fmt.Errorf("parse -at: %w", err)
		}
		events = board.AsOf(events, t)
	}
	s, err := board.Replay(f.Settings, events)
	if err != nil {
		return board.Assessment{}, err
	}
	return s.Assess(), nil
}
```

Replace `cmd/agentswarm/run.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jchen0426/agent-swarm/internal/versioninfo"
)

const usage = `Usage: agentswarm <command> [flags]

Commands:
  version    Print the build version as JSON.
  conclude   Assess a recorded board and print its hypotheses and conclusion as JSON.
`

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// run executes one command and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "version":
		return report(stdout, stderr, versioninfo.Get(), nil)
	case "conclude":
		return runConclude(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return exitOK
	default:
		//nolint:gosec // G705 targets HTML responses; this is a terminal, and %q escapes control characters.
		_, _ = fmt.Fprintf(stderr, "agentswarm: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

// report writes v as indented JSON, or err to stderr.
func report(stdout, stderr io.Writer, v any, err error) int {
	if err == nil {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(v)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agentswarm: %v\n", err)
		return exitError
	}
	return exitOK
}
```

- [ ] **Step 4: Run the tests and the linter**

Run: `go test ./cmd/agentswarm/`

Expected: PASS.

Run: `make test-e2e`

Expected: PASS.

Run: `make lint`

Expected: `0 issues.`

Run: `make test-all`

Expected: PASS for the unit, integration and end-to-end tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/agentswarm/conclude.go \
  cmd/agentswarm/run.go \
  cmd/agentswarm/run_test.go \
  cmd/agentswarm/testdata/board.json \
  test/e2e/cli_test.go \
  test/e2e/testdata/board.json \
  test/e2e/testdata/inconsistent.json
git commit -m "feat(cli): add the conclude command"
```


## After the last task

Run `make test-all` and `make snapshot` once more on a clean checkout. `make snapshot` writes release archives for four platforms to `dist/`, which Git ignores. Then push the branch and ask for the whole-branch review.

## Changes made during execution

The tasks ran as written. These changes came from a security review, from the user, and from the final whole-branch review. Each was test-driven: a failing test first, then the fix.

| Change | Why | Commits |
|---|---|---|
| Quotes match the raw output or one decoded JSON string, a key or a value, read token by token. There is no "key: value" text. This replaces Decision 10, and two `TestQuoteAppears` cases changed. | A background security review: joining decoded values let a quote stitch them into text the output never contained. | 50d21f7 |
| The command line runs on spf13/cobra. Flags are `--board` and `--at`. `report()` became `writeJSON` plus a `failure` error type, and the exit codes are unchanged. | The user asked for cobra during execution. | a5f57d8, 1ecbefb |
| `Replay` refuses convergence settings that need fewer than one contributor or source, or a negative margin. `NewState` is no longer exported. | Final review: missing settings decoded to 0/0/0, and a hypothesis with no supporter converged. | 76fba1f |
| `ReplayAsOf` checks the whole log before it cuts at a moment. `conclude` and the `Assess` use case use it, and `conclude` refuses data after the board. | Final review: a cut over an unchecked log could drop an out-of-order entry unseen. | 51ee84b |
| Without convergence, the conclusion is the highest-scoring hypothesis that no human rejected; if nobody supports it, there is no conclusion. This refines Decision 4. | Final review: Decision 4 could fall through to a lower-scoring, mostly refuted hypothesis, against the ordering of spec Section 4.3. | 0ab75c9 |
| `Append` runs at read committed explicitly. | Final review: under a repeatable-read default, concurrent appends failed with key violations. | ef19ba7 |

**Deferred review findings.** Minor, and left for later plans:

- `Replay` accepts events from other runs, and `Dismisses` lists it did not compute.
- A NUL character passes the rules but fails in jsonb.
- The concurrency tests do not exercise the commit-time guard, and the older integration tests cannot run twice in one container.
- `run(nil)` lets cobra read the process's own arguments.
- Unwrapped `RunE` errors count as usage errors.
- Quotes do not match across Unicode normalization forms.
- Degenerate quotes such as `{` satisfy provenance.
- `Sources` is `null` when empty.
- `stanceRecord.seq` is unused.
- Domain purity is enforced by review only, not by depguard.
- `Key.String` does not quote field names, and a few edge cases have no tests.

**Carried to later plans:**

- **Plan 5:**
  - define what counts as stored output;
  - decode nested JSON one string at a time, with a depth limit;
  - state the quoting rule in the `post_evidence` description;
  - decide a minimum quote content;
  - strip NUL characters in redaction.
- **Plan 9:** render quotes as inert text, with bidi controls neutralized.
- **Plan 10:** the retention job uses `SET LOCAL agentswarm.retention` and deletes whole runs only; separate the runtime role from the table owner.
