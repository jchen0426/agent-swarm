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

# The test container is named for this worktree, by the first 8 hex digits of the SHA-256
# of the checkout's path, so that a second make test-linux in it fails at once.
WORKTREE_ID    := $(shell printf %s "$(CURDIR)" | if command -v sha256sum >/dev/null; then sha256sum; else shasum -a 256; fi | cut -c1-8)
TEST_CONTAINER := agentswarm-test-$(WORKTREE_ID)

# The runtime image (Plan 7d-2, Contract C12): make image builds IMAGE_COMMIT's, and make
# image-dev this worktree's, named DEV_IMAGE, a tag of the worktree's own.
IMAGE_COMMIT ?= HEAD
DEV_IMAGE    := agentswarm-runtime:dev-$(WORKTREE_ID)

# The stubborn test image (Plan 7d-2, Decision 12), which the e2e harness runs under the
# Docker runner for the turns that ignore their input and SIGTERM, named STUBBORN_IMAGE, a
# tag of the worktree's own, as DEV_IMAGE is: so a rebuild in one worktree never removes an
# image that another worktree's gate locked, which that worktree's tag still names.
STUBBORN_IMAGE := agentswarm-stubborn:dev-$(WORKTREE_ID)

.PHONY: all build test test-integration test-e2e test-all test-live test-clean test-compat no-test-container linux-binary linux-env image image-dev stubborn-image test-image test-linux test-docker lint fmt tidy release-check snapshot clean

all: lint test build

# The binary, static, as the release builds it (.goreleaser.yaml), so that the e2e suite
# drives the build that ships.
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/agentswarm

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

# Refuses at once while a container named TEST_CONTAINER exists, running or not: a test run
# of this worktree is going on, or a killed one left it. The steps that change what such a
# run uses, LINUX_BIN, TEST_IMAGE, DEV_IMAGE and STUBBORN_IMAGE, take this check as an
# order-only prerequisite, so that it runs before them even under make -j, and a second run
# refuses before it changes anything; the name itself stops a run only once these steps are
# done.
no-test-container:
	@if docker container inspect $(TEST_CONTAINER) >/dev/null 2>&1; then \
		echo "make: a container named $(TEST_CONTAINER) exists, so a test run of this worktree is going on, or a killed one left it; once no run is live, remove it with: docker rm -f $(TEST_CONTAINER)" >&2; \
		exit 1; \
	fi

linux-env linux-binary image-dev stubborn-image: | no-test-container

# The binary for Linux, on the Docker engine's architecture.
linux-binary:
	arch=$$(docker version --format '{{.Server.Arch}}') && \
	CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(LINUX_BIN) ./cmd/agentswarm

# The Linux test image (build/test/Dockerfile): Go, the Docker CLI and jq. It carries no
# agentswarm. label, as the project's label is the containers'.
linux-env:
	docker build -t $(TEST_IMAGE) build/test

# The runtime image of a commit, IMAGE_COMMIT, HEAD by default (Plan 7d-2, Decision 6): the
# release build. build/runtime/image.sh builds it from the commit alone: the binary from
# git archive of the commit, by the toolchain its go.mod names, in a clean Go environment,
# and the image from the commit's own Dockerfile, without the cache, so that one commit
# gives one image id on one engine. ARCH sets the architecture, by default the engine's. It
# tags the image agentswarm-runtime:<its id's 64 hex digits>, and prints the binary's
# SHA-256 and the image's id. It refuses a commit without build/runtime/Dockerfile, whose
# runtime predates the image contract. It needs the buildx plugin.
image:
	GO='$(GO)' build/runtime/image.sh commit $(IMAGE_COMMIT)

# The worktree's runtime image, which the gates' sandboxes run, as the gates run the
# worktree's code before its commit: the same Dockerfile around a fresh make linux-binary,
# never what LINUX_BIN held, which a gate's e2e step rebuilds. It is named DEV_IMAGE, so
# that it replaces only this worktree's image, and promises no reproducibility. It prints
# the image's id, and removes the image DEV_IMAGE named before, once its docker image
# inspect is saved under .test-clean/, so that each gate does not leave the last one's
# dangling. While the test container exists, a run may be using that image, so it refuses,
# as does image.sh itself, which is given the container's name.
image-dev: linux-binary
	GO='$(GO)' build/runtime/image.sh dev $(LINUX_BIN) $(DEV_IMAGE) $(TEST_CONTAINER)

# The stubborn test image (Plan 7d-2, Decision 12), from test/e2e/testdata/stubborn: busybox,
# pinned by digest, around a turn that reads nothing, ignores SIGTERM and sleeps for 120 s at
# most, which the harness locks in place of the runtime image for scenario 7 and scenario 16's
# stubborn case. It is built as the runtime image is, without provenance or an SBOM, so that
# an engine with the containerd store attaches no attestation to an image the runner's store
# then keeps. It carries no agentswarm. label, which the image contract refuses. A build keeps
# its id while BuildKit's cache holds it; once the cache is pruned, a build gets another id,
# and build/runtime/image.sh remove then removes the image STUBBORN_IMAGE, the worktree's own
# tag, named before, as it removes image-dev's, so that each such build does not leave the
# last one's dangling: once its docker image inspect is saved under .test-clean/, in a file
# never replaced, and never while a tag still names it, another worktree's among them, or
# while a container uses it, which the engine refuses.
stubborn-image:
	@old=$$(docker image inspect --format '{{.Id}}' $(STUBBORN_IMAGE) 2>/dev/null) || old=; \
	echo "docker build --provenance=false --sbom=false -t $(STUBBORN_IMAGE) test/e2e/testdata/stubborn"; \
	docker build --provenance=false --sbom=false -t $(STUBBORN_IMAGE) test/e2e/testdata/stubborn || exit 1; \
	new=$$(docker image inspect --format '{{.Id}}' $(STUBBORN_IMAGE)) || exit 1; \
	if [ -n "$$old" ] && [ "$$old" != "$$new" ]; then \
		echo "build/runtime/image.sh remove $$old $(STUBBORN_IMAGE)"; \
		build/runtime/image.sh remove "$$old" $(STUBBORN_IMAGE); \
	fi

# The reproducibility test of make image (R13): one commit, built twice without the cache,
# gives one binary and one image on this engine. It builds on the host, as the test image's
# Docker CLI has no buildx, so it runs at the branch's end and when the build changes, not
# in every gate.
test-image:
	$(GO) test -count=1 -timeout 30m -tags image ./test/image/

# The suite on Linux, in the test image, on this Docker engine (Plan 7d-2, Decision 12):
# build/test/run.sh linux runs make test and make test-integration; then make test-e2e with
# the Linux binary, under the Docker runner; then, under the process runner, the scenarios
# that check a process is gone (R27). Before it, make image-dev builds the worktree's runtime
# image, and make stubborn-image the stubborn test image, whose ids the suite gets as
# AGENTSWARM_E2E_IMAGE and AGENTSWARM_E2E_STUBBORN, for the sandboxes the harness runs under
# the Docker runner. First a container that runs as root makes E2E_DIR on the engine's host
# and gives it to the developer, since the engine would make a missing directory owned by
# root. Another reads the engine socket's group from inside a container, where it can differ
# from the host's (on OrbStack it is 0 inside, though the Mac's stat says 20). The suite then
# runs as the developer's uid and gid, so that the permission tests run, with that group
# added, with the socket, and with the checkout and E2E_DIR at the same paths inside and
# outside. Its caches are the named volumes agentswarm-test-gomod and agentswarm-test-gocache.
# make test-clean leaves the volumes and E2E_DIR. Ryuk is off (R12); the labelled
# containers' own cleanup and make test-clean remain. The container takes this worktree's
# name, TEST_CONTAINER, and a second run in the worktree refuses at once, before it changes
# anything the first one uses (no-test-container), rather than run beside the first on the
# same files. Its init, tini, passes a signal to run.sh's whole process group
# (TINI_KILL_PROCESS_GROUP), so a Ctrl-C stops every step, and run.sh's cleanup runs. On a
# Linux host the targets need the buildx plugin. On a Mac they need OrbStack, in whose VM
# /srv resolves; Docker Desktop does not share /srv.
#
# make test-docker runs the Docker path alone, in the same way and under the same name, so
# that it and make test-linux never run at once in one worktree: build/test/run.sh docker
# runs the Docker runner's integration tests, which build on Linux alone (R14), serve's tests
# of the Docker runner, and the whole e2e suite under the Docker runner. Each target passes
# its own name's mode to run.sh, linux or docker.
test-linux test-docker: linux-env linux-binary image-dev stubborn-image
	docker run --rm --label agentswarm.project=agent-swarm --user 0:0 -v /srv:/srv $(TEST_IMAGE) \
		sh -c "mkdir -p $(E2E_DIR) && chown $$(id -u):$$(id -g) $(E2E_DIR)"
	group=$$(docker run --rm --label agentswarm.project=agent-swarm -v /var/run/docker.sock:/var/run/docker.sock \
		$(TEST_IMAGE) stat -c %g /var/run/docker.sock) && \
	image=$$(docker image inspect --format '{{.Id}}' $(DEV_IMAGE)) && \
	stubborn=$$(docker image inspect --format '{{.Id}}' $(STUBBORN_IMAGE)) && \
	docker run --rm --init --name $(TEST_CONTAINER) --label agentswarm.project=agent-swarm \
		--user $$(id -u):$$(id -g) --group-add $$group \
		-v /var/run/docker.sock:/var/run/docker.sock -v $(CURDIR):$(CURDIR) -w $(CURDIR) -v $(E2E_DIR):$(E2E_DIR) \
		-v agentswarm-test-gomod:/go/pkg/mod -v agentswarm-test-gocache:/cache/go-build \
		-e TINI_KILL_PROCESS_GROUP=1 -e TESTCONTAINERS_RYUK_DISABLED=true \
		-e AGENTSWARM_E2E_IMAGE=$$image -e AGENTSWARM_E2E_STUBBORN=$$stubborn \
		$(TEST_IMAGE) build/test/run.sh $(patsubst test-%,%,$@)

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
