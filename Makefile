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

.PHONY: all build test test-integration test-e2e test-all test-live test-clean lint fmt tidy release-check snapshot clean

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

# Live check: plays a turn of the runtime through the LLM gateway against the model vendor's
# real API, and records the SDK version it passed with (testdata/live-check.json). It needs
# AGENTSWARM_LIVE_KEY and the network, and costs a few cents a run, so no other target runs it.
# Run it when the runtime's SDK or the agents' model changes, and commit the record. Without
# the key the test would skip and the target succeed, having checked nothing, so the target
# stops first (the shell reads the key, which is never printed). The timeout is for three
# turns, which can outlast go test's default of ten minutes.
test-live:
	@test -n "$$AGENTSWARM_LIVE_KEY" || { echo "make test-live needs AGENTSWARM_LIVE_KEY set to the model vendor's key" >&2; exit 1; }
	$(GO) test -count=1 -timeout 30m -tags live -run TestLive -v ./internal/llm/endpoint/

# A test run that is killed leaves its containers behind. Every container a test starts
# carries the label agentswarm.project=agent-swarm, and this removes the ones that have run
# for over an hour. Before it removes one, it saves the container's docker inspect output,
# and the files at the path of its agentswarm.data label, under .test-clean/, in files named
# for the run that no later run replaces. Run it between test runs, as it cannot tell a run
# still going from one that was killed. To see what would go, run:
# go run ./test/testclean -dry-run
test-clean:
	$(GO) run ./test/testclean

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
