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

# The binary build writes. The environment can set it, and make's command line overrides it:
# the Linux test container builds at LINUX_BIN, so that it never overwrites this host's
# binary in the checkout they share (R11).
BIN ?= bin/agentswarm

# The Linux test environment (Plan 7d-2, Contract C15): the binary for Linux, the test
# image, and the directory mounted at the same path inside and outside the test container.
LINUX_BIN  := bin/linux/agentswarm
TEST_IMAGE := agentswarm-test:dev
E2E_DIR    := /srv/agentswarm-e2e

.PHONY: all build test test-integration test-e2e test-all test-live test-clean test-compat linux-binary linux-env test-linux lint fmt tidy release-check snapshot clean

all: lint test build

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/agentswarm

# Unit tests: no network, no containers.
test:
	$(GO) test -race -count=1 ./...

# Unit and integration tests. Integration tests start containers, so Docker must run.
test-integration:
	$(GO) test -race -count=1 -tags integration ./...

# End-to-end tests drive the built binary from outside. The timeout is explicit (R20): a test
# binary that go test's timeout kills takes the harness's parents, their serves and their
# turns with it.
test-e2e: build
	AGENTSWARM_BIN=$(CURDIR)/$(BIN) $(GO) test -count=1 -timeout 60m -tags e2e ./test/e2e/...

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

# The N-1 test (Plan 7d-1, Decision 18): the previous release's harness scenarios 1, 2 and 8,
# run from a worktree at its tag, with the binary built there, against a schema this build
# migrated; then this build verifies the audit chain they left and inspects each item they
# left. The previous release is the latest release tag in HEAD's history, such as v0.1.0,
# HEAD's own and pre-releases such as v0.2.0-rc.1 left out. The scenarios share a Postgres in a
# labelled container, so Docker must run; once done, the container is removed after a backup
# under .test-clean/, as make test-clean makes one. Until a release is tagged, it says so and
# passes. It checks a promise, that the previous release can read the schema this build leaves:
# when the schema's min_reader is above that release's level, its last migration's number, it
# says so, with Decision 18's deploy order, and passes without the scenarios.
test-compat: build
	$(GO) run ./test/compat -bin $(CURDIR)/$(BIN)

# The binary for Linux, on the Docker engine's architecture.
linux-binary:
	arch=$$(docker version --format '{{.Server.Arch}}') && \
	CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(LINUX_BIN) ./cmd/agentswarm

# The Linux test image (build/test/Dockerfile): Go, the Docker CLI and jq. It carries no
# agentswarm. label, as the project's label is the containers'.
linux-env:
	docker build -t $(TEST_IMAGE) build/test

# The suite on Linux, in the test image, on this Docker engine (Plan 7d-2, Decision 12):
# build/test/run.sh linux runs make test and make test-integration, then make test-e2e with
# the Linux binary. First a container that runs as root makes E2E_DIR on the engine's host
# and gives it to the developer, since the engine would make a missing directory owned by
# root. Another reads the engine socket's group from inside a container, where it can differ
# from the host's (on OrbStack it is 0 inside, though the Mac's stat says 20). The suite then
# runs as the developer's uid and gid, so that the permission tests run, with that group
# added, with the socket, and with the checkout and E2E_DIR at the same paths inside and
# outside. Its caches are the named volumes agentswarm-test-gomod and agentswarm-test-gocache.
# make test-clean leaves the volumes and E2E_DIR. Ryuk is off (R12); the labelled
# containers' own cleanup and make test-clean remain. On a Linux host the targets need the
# buildx plugin. On a Mac they need OrbStack, in whose VM /srv resolves; Docker Desktop does
# not share /srv.
test-linux: linux-env linux-binary
	docker run --rm --label agentswarm.project=agent-swarm --user 0:0 -v /srv:/srv $(TEST_IMAGE) \
		sh -c "mkdir -p $(E2E_DIR) && chown $$(id -u):$$(id -g) $(E2E_DIR)"
	group=$$(docker run --rm --label agentswarm.project=agent-swarm -v /var/run/docker.sock:/var/run/docker.sock \
		$(TEST_IMAGE) stat -c %g /var/run/docker.sock) && \
	docker run --rm --init --label agentswarm.project=agent-swarm --user $$(id -u):$$(id -g) --group-add $$group \
		-v /var/run/docker.sock:/var/run/docker.sock -v $(CURDIR):$(CURDIR) -w $(CURDIR) -v $(E2E_DIR):$(E2E_DIR) \
		-v agentswarm-test-gomod:/go/pkg/mod -v agentswarm-test-gocache:/cache/go-build \
		-e TESTCONTAINERS_RYUK_DISABLED=true $(TEST_IMAGE) build/test/run.sh linux

# Lint runs twice: for this host, and with GOOS=linux, so that the files built on Linux alone
# are linted too. The Linux test image runs no linter.
lint:
	$(GOLANGCI_LINT) run ./...
	GOOS=linux $(GOLANGCI_LINT) run ./...

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
