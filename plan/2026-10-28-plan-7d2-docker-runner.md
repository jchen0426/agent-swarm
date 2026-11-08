# Plan 7d-2: the runner in Docker sandboxes — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Approved on 2026-10-28 for subagent-driven execution on main. Executed from 2026-10-29 to 2026-11-01 (8da5e97..e419094, with this record): Tasks 1 to 16 with five side fixes, the live gate of Task 16's Part B with the author's approval, a whole-branch review and one fix wave, and the branch's end checks, the containerd image store's among them (see "Changes made during execution"). |
| Date | 2026-10-28 |
| Roadmap | `2026-10-12-implementation-roadmap.md`, Plan 7, its third part to run: 7a and 7d-1 (executed), 7d-2 (this plan), then 7b and 7c. The checkpoint after Plan 7d follows this plan (ADR-016). |
| How it was made | The design went through four versions, v1 to v4, kept in the author's backups. Three fresh reviewers on Opus 5.5 reviewed v1, v2 and v3, each probing Docker Engine 29.4.0 on this Mac: 0 Critical, 5 Important and 15 Minor findings; then 0, 2 and 12; then, in a review scoped to v3's changes, 0, 1 and 7. After review 2, the containerd image store, which a fresh Docker 29 install uses, was probed on Docker Engine 29.9.2 in a throwaway container, so that image identity, reproducibility and reload rest on both stores. After review 1, the author decided serve's privileges and that launches leave the coordinator's lock (Decisions 10 and 4). Then a plan review by a fresh reviewer on Opus 5.5 found this plan ready with fixes: 1 Critical, 2 Important and 16 Minor findings, each fixed in this text. The scoped re-review of the fixes found 0 Critical, 0 Important and 7 Minor, each fixed in the text. |
| Decided by the author | On 2026-10-20 and 2026-10-28, as "The author's decisions" lists. The design's six other calls are listed under "My calls, for the author to check", and approving this plan approves them. |

**Goal:** Run each agent's turn in a Docker sandbox on one Linux host, behind the coordinator's `Turns` port: a sandbox whose only way out is two Unix sockets to the gateways, over TLS 1.3, which holds no credential but its two session tokens and leaves nothing behind; launch turns outside the coordinator's lock; build the runtime image reproducibly from a commit and keep every image an item locked; and show it on Linux, through the harness's scenarios with sandboxed turns, probes of criterion 4, and a real model.

**Architecture:**
- **The sandbox** (Decisions 1 to 3). `internal/runner/docker` implements `app.Turns` on Docker Engine 29: one container per turn, with no network, a read-only root, no capability, limits, and the engine's init as PID 1. Its only way out is a read-only mount of serve's sockets directory, where serve serves the two gateways over TLS 1.3 and HTTP/1.1, with a certificate it makes at start.
- **Launches outside the lock** (Decision 4). `Turns.Start` registers a turn and returns it pending; the runner launches it afterwards and reports the launch through `Coordinator.Launched`. A launch that fails is tried again while its round lasts, and leaves a `turn_unstarted` notice on the board (Decision 7).
- **The runner** (Decision 5) reaches the engine through a port of its own, never holds a lock while the engine answers, kills every turn on each of serve's exits (`KillAll`), and sweeps what earlier processes left.
- **Images** (Decision 6). `make image` builds a commit's runtime image reproducibly. Serve checks every image against a contract, keeps a copy of each in a store, and reloads what the engine lost.
- **The runtime** (Decision 9) reads one shared spec type, version 2, dials the spec's sockets, trusts the spec's certificate alone, and ends `unavailable` at SIGTERM. Both runners read a signal they did not send as `unavailable`.
- **Serve** (Decisions 8, 13 and 14) refuses what it cannot run on, naming the key, logs its worst drain, and waits for its leader for the leader's busy bound. `examples/two-instances/` holds what a first deployment copies.
- **Tests on Linux** (Decisions 11 and 12). A Linux test image runs the suite under `--init` as the developer; the harness writes Docker deployments; probes show criterion 4; the controller's live gate runs a real model through sandboxes (Decision 15).
- **The order.** The Linux test image comes first, so that every later task can run on Linux. Then the runtime and its spec, the board's notice, the coordinator's launches, the image's build, the Docker runner in three tasks, the probes, the deployment file, serve, the harness, the drain, the examples, the documents and the live gate.

**Tech Stack:**
- Go 1.26 (`go 1.26.0`); the runtime image's toolchain is pinned to `go1.26.0`
- `github.com/moby/moby/client` v0.5.0, `github.com/moby/moby/api` v1.55.0 and `github.com/containerd/errdefs` v1.0.0, promoted from indirect to direct requirements; no module joins the graph
- pgx v5.10.0, testify v1.12.1, testcontainers-go v0.44.0, anthropic-sdk-go v1.80.0, modelcontextprotocol/go-sdk v1.8.0, go.yaml.in/yaml/v3 v3.0.5, as before
- Docker Engine 29 over its Unix socket, its API negotiated (1.40 and later; probed at 1.54 on OrbStack's 29.4.0 with the classic store, and at 1.56 on 29.9.2 with the containerd store); BuildKit through `docker buildx` (v0.33.0 here)
- Images, each pinned by digest by the task that first uses it: `golang:1.26` (Debian), with the Docker CLI's static release 29.4.0 pinned by its SHA-256 for `x86_64` and `aarch64`, for the Linux test image; `scratch` for the runtime image; `busybox` for the stubborn test image; `curlimages/curl` for the probes; `docker:29-dind` for the containerd-store check at the branch's end; `postgres:17-alpine` and `ghcr.io/shopify/toxiproxy:2.12.0`, as before

**Spec:** `plan/2026-10-11-agent-swarm-platform-spec.md`. Read these before the tasks that implement them:
- Sections 2 and 2.1: the agent runner, the session tokens, invariant 1;
- Section 8.1, and Section 8.2's rows of a sandbox that crashes, a turn that could not start, the lease, a stopped instance and a client that holds a connection open;
- Section 9.3: transport, secrets and sandboxes;
- Section 15, and Section 0's rule on bracketed values.

Also read ADR-006 (its seven criteria, "As built (Plan 7d-1)", and "The sandbox network" under Consequences, which this plan amends), ADR-016 (the checkpoint after Plan 7d), and Plan 7d-1 (`plan/2026-10-20-plan-7d1-serve-and-harness.md`): its Decisions 6, 12 and 13, its Contracts C1 to C9, which this plan amends, its constraints carried to Plan 7d-2, and its "Changes made during execution". The design this plan carries, and its three reviews, are in the author's backups: `plan/backups/plan7d2/design-v4.md` and `review-1.md` to `review-3.md` (git-ignored). This plan is self-contained: it carries every decision, and the design's reason wherever the reason keeps an implementer from a wrong simplification.

**How a task runs.** The plan is light: it gives each task its decisions, the interfaces it consumes and produces, and its tests by name, with what each asserts and why it fails before the implementation. It gives no implementation code; a code block appears only where an algorithm or an exact text is fixed. At execution each task's code is written test first: the implementer writes the listed tests, watches each fail for the reason given, then writes the code. The listed tests are the minimum; more may be added, and a test's name may change to match the code's vocabulary, with a ruling in the ledger. Each task's brief carries, verbatim, the Global Constraints, the Decisions and the Contracts it names. Each task is reviewed by a fresh reviewer on Opus 5.5, and the whole branch at its end.

## The author's decisions

- **2026-10-20:** Docker Engine on one host first, with the `Turns` port kept swappable and Kubernetes later if needed; a light plan, Opus reviews of the design, and subagent-driven execution with an Opus review of each task and of the whole branch.
- **2026-10-28:** the brainstorming questions skipped; then, after review 1:
  - **Serve's privileges** (Decision 10): serve's user joins the `docker` group in this plan, which makes it root on the host in effect. ADR-006 records the risk, and a narrow launcher comes before a team's measurement window.
  - **Launches outside the coordinator's lock** (Decision 4): `Start` registers a turn and returns; the runner launches it afterwards and reports the launch through a new callback; a launch that fails is tried again while its round lasts, as now.

## My calls, for the author to check

The design made six calls the author has not made. Each is marked "the planner's call, approved with this plan" in its decision:
1. **No network and two Unix sockets** (Decision 1). This amends ADR-006's mechanism, and ties the Docker runner to serve on Linux.
2. **TLS 1.3 over the sockets** (Decision 2), with a certificate serve makes at start, and HTTP/1.1 on both sides.
3. **Serve keeps a copy of each runtime image** (Decision 6): about 11 to 35 MB a release, which only an operator deletes.
4. **The gates** (Decision 12):
   - the full `make test-linux`, about 45 to 60 minutes, eight times: at Tasks 1, 2, 4, 5, 11 and 12, at the branch's end, and after the final review's fixes;
   - `make test-docker` in every other task from Task 6 on, which makes each such gate about 15 minutes longer than 7d-1's. A gate that runs the full `make test-linux` does not also run `make test-docker`, which the full run contains.
5. **One bare clone on one host, fetched fast-forward only; serve under systemd, a topology no test here runs, since every test runs serve in a container; the engine's live-restore off** (Decision 13).
6. **The live gate's spend**, about $0.10, with the vendor's key as a read-only file mount (Decision 15).

## Global Constraints

- **Module and Go version.** The module path is `github.com/jchen0426/agent-swarm`, with `go 1.26.0`.
- **Vocabulary.** Names come from the spec: work item, run, round, turn, session, board, entry, notice, stop reason, lease, holding, epoch, runner, sandbox. Agents are "AI agents" or "agents"; never "employee".
- **Domain purity.** `internal/work/item`, `internal/work/board`, `internal/work/tokens`, `internal/holding` and `internal/outcome` import no database, network or framework package.
- **Ports.** A port is declared by the package that uses it. Wiring lives in `cmd/agentswarm`. The Docker runner reaches the engine only through its own `Engine` port (Contract C14).
- **Decide, record, act.** Every decision the coordinator makes is a work record written before its effects, and every effect can be repeated (Plan 7a).
- **Audit records.** Append-only, chained, and written with the database's `clock_timestamp()`.
- **The holding.** Every guarded write runs under a context that carries the epoch of the holding it acts for. Every request to the model vendor and to an MCP server, every turn's start and every sandbox's start is preceded by `Holding.Valid` for that epoch.
- **Secrets.** A session token never appears in a record, a log, an error, a board entry, a turn's output file, a process's arguments or environment, a container's configuration (its command, environment or labels), the engine's logs, or anything the model vendor or an MCP server receives: it travels only in the spec on the turn's standard input, and the runtime sends it only in request headers. The vendor's key enters a test container only as a file mounted read-only, never as an environment variable or an argument, which the engine keeps in the container's configuration.
- **Migrations.** This plan adds none: the board's notices keep body version 1, and `reason` lives in the event's JSON body.
- **Libraries.** `github.com/moby/moby/client` v0.5.0, `github.com/moby/moby/api` v1.55.0 and `github.com/containerd/errdefs` v1.0.0 become direct requirements. All three are in the module graph already, as indirect requirements, so no module joins it. The runner tells a missing object with `errdefs.IsNotFound`: for a 404, moby client v0.5.0 returns an error with no `NotFound()` method, which only `errdefs` can tell (plan review 1, C1). The runner does not import `github.com/opencontainers/image-spec`. Code under `test/e2e` imports the standard library, testify and testcontainers-go only (the depguard rule `e2e`): the harness checks containers with the `docker` command line.
- **Values.** A value fixed in code is written without brackets; a bracketed value is a default a deployment may change (spec Section 0).
- **Test first.** Every test a task lists is written before its implementation and seen to fail for the reason the task gives, except a test the task marks as a guard: a guard keeps a behaviour of today's code that the task must not lose, passes at Step 1, and is not to be "fixed" into failing.
- **Language.** Code, comments and documents are in English.
- **Docker labels.** Every container a test or a Makefile target starts carries the label `agentswarm.project=agent-swarm`; sandboxes get it through `runner.docker.labels`.
- **The gates.** Every task ends green on the Mac: `golangci-lint run ./...` and `GOOS=linux golangci-lint run ./...`, each at `0 issues.` (`make lint` runs both from Task 1 on); `make test`; `make test-integration`; `make test-e2e`. The full `make test-linux` runs eight times:
  - at Task 1, which adds it;
  - at Tasks 2 and 4, which change the process runner and the port that both runners implement;
  - at Task 5, which changes the target itself (the worktree's runtime image and `AGENTSWARM_E2E_IMAGE`);
  - at Task 11, which wires serve;
  - at Task 12, which changes the harness;
  - at the branch's end, before the final review;
  - after the final review's fixes.

  Every other task from Task 6, which adds `internal/runner/docker`, on runs `make test-docker`: Tasks 6 to 10 and 13 to 16. It runs the Docker runner's integration tests, serve's from Task 11, and, from Task 12, the whole e2e suite under Docker, on Linux. A gate that runs the full `make test-linux` does not also run `make test-docker`, which the full run contains: its `linux` mode runs `make test-integration`, which holds the Docker runner's and serve's integration tests, and, from Task 12, the whole e2e suite under Docker. What the full run adds is the process runner's scenarios on Linux, and the tasks named above are the ones that touch those, or the target. Commit by path, never with `git add -A`, and with no `Co-Authored-By` line.

## Review Focus

The design says what the Docker runner must do, not every condition it will meet. These five are the conditions most likely to bite a person running it that no scenario reaches; each has its tests in the task that owns the code.

1. **The engine stops or restarts mid-round**, as an upgrade or `systemctl restart docker` does. A person expects the running turns to read `unavailable`, never `crashed`, so that no agent is removed for host maintenance. (`TestTheRuntimeEndsUnavailableAtSIGTERM`, Task 2; `TestASignalTheRunnerDidNotSendReadsUnavailable`, Task 7; `TestAContainerTheEngineStopsOrKillsReadsUnavailable`, Task 9)
2. **The engine hangs, or takes seconds a call.** A person expects the coordinator to go on, holding its lock for no launch, and every agent whose turn could not start to have a notice on its board saying why, once a round, a launch still queued at its deadline included, and a launch whose engine call hangs. (`TestAFailedLaunchPostsOneNoticePerAgentPerRound` and `TestALaunchThatFailsAfterItsRoundsDeadlineStillPostsItsNotice`, Task 4; `TestAnAttachTheEngineNeverAnswersEndsWithItsContext`, Task 6; `TestStartCallsNoEngine`, `TestALaunchThatOutlastsItsBoundOrMeetsNoEngineReadsEngine` and `TestALaunchQueuedPastItsDeadlineWhileTheEngineDoesNotAnswerReadsEngine`, Task 7)
3. **Someone prunes images on the host** (`docker image prune -a`, `docker system prune -a`, or a plain prune after a reload) while items run. A person expects the next launch to reload the locked image from the store, with the same id and tagged, rather than the item's turns failing until its budget stops it. (`TestAnImageTheEngineLostIsReloadedFromTheStoreAndTagged`, Task 6; `TestALaunchReloadsAnImageTheEngineLost`, Task 7)
4. **A deployment file copied for a second instance without changing its holder**, or a second serve of one deployment. A person expects the second to refuse to start, naming the holder, before its start sweep could kill the first one's turns. (`TestASecondLockOfOneTenantAndHolderIsRefused`, Task 6; `TestOpenRefusesAHolderAnotherServeHoldsBeforeItSweeps`, Task 8)
5. **A sockets directory that cannot serve**: missing, held by another serve, holding other files, too long a path for a socket, closed to the sandboxes' group, or inside the secrets directory. A person expects serve to refuse to start and name `runner.docker.sockets`. (`TestTheSocketsDirectoryMustStandApartAndFitASocketsPath`, Task 10; `TestASocketsDirectoryThatCannotServeIsRefusedNamingTheKey`, Task 11)

## Decisions

The design's decisions, as the three reviews and the author's answers left them. Their numbering is the design's, so that the reviews' references stay valid. Values in brackets are defaults, and every new one goes into spec Section 15 (Task 15).

### Decision 1. No network: the sandbox reaches the gateways through two Unix sockets (the planner's call, approved with this plan)

- **What.** A sandbox runs with `NetworkMode: none`, so its only interface is `lo`. Its only way out is serve's sockets directory, mounted read-only at `/run/agentswarm`. The directory holds two Unix sockets that serve listens on: `mcp.sock` for the MCP gateway, `llm.sock` for the LLM gateway.
- **Why.** Without a network there is no route to the host, its loopback, other sandboxes, the metadata endpoint, DNS or the internet, on any engine version and whatever the host's firewall. ADR-006 names "an internal network whose bridge has no address on the host, or an equivalent default-deny": no network is the stricter equivalent.
- **Rejected.**
  - An internal network with an isolated gateway: the gateways would have to join it, either by running serve in a container on it, which then needs the engine's socket and puts the loopback-only admin API out of the operators' reach, or through a relay container, a second component on every turn's path, which lets sandboxes on that network reach each other.
  - Host firewall rules: Linux-only state outside Docker, which serve would have to own.
- **Measured on 2026-10-28.** With serve's stand-in on the same Linux kernel, a sandbox reached it through a socket in a read-only bind mount; with the stand-in on macOS it did not, since OrbStack's file sharing shows a socket but carries no connection. The reviews confirmed that the only interface is `lo`; that DNS, `169.254.169.254` and `1.1.1.1` are unreachable and `host.docker.internal` does not resolve; that `rm`, `mv`, `ln -s` and `mkdir` in the mount are refused; that a 0660 socket refuses a sandbox without the sockets' group; and that the sockets work across containers through a directory at the same path.
- **Consequence.** The Docker runner needs serve on Linux, on the containers' kernel: on the host, or in a container on the host's network. Serve refuses `runner.kind: docker` on any other system. On a Mac the suite runs in a Linux container (Decision 12).

### Decision 2. TLS 1.3 over the sockets, HTTP/1.1, with a certificate serve makes at start (the planner's call, approved with this plan)

- **The certificate.** Under the Docker runner the agents' two listeners serve TLS 1.3 over the sockets, with one self-signed ECDSA P-256 certificate that serve makes at start, for `mcp.agentswarm.internal` and `llm.agentswarm.internal`. It is valid for ten years, fixed in code; its key lives only in serve's memory, and a restart makes a new one. Each turn's spec carries the certificate, and the runtime trusts it alone.
- **HTTP/1.1 on both sides**, through `http.Server.Protocols` and `http.Transport.Protocols`. Without it, a client cloned from `http.DefaultTransport` negotiates HTTP/2, and Go's HTTP/2 server has no per-request `ReadHeaderTimeout`, which spec Section 8.2's "a request whose header has not arrived within 10 s" needs. Review 2 probed that the setting on either side gives HTTP/1.1.
- **Why TLS over sockets no network carries.** Spec Section 9.3 requires the sandbox's hop to the gateways to be authenticated and encrypted. The isolation comes from Decision 1; TLS means that a later runner that reaches the gateways over a network finds the runtime speaking it already. It costs one certificate per process, and nothing for an operator to rotate.
- **The process runner** keeps plain HTTP on loopback (Plan 7d-1, Decision 1).

### Decision 3. The sandbox

Every setting is the runner's; the image's own defaults are refused (Decision 6) or overridden. Contract C13 gives each setting as the container's configuration. In short:
- **The image** is the turn's `Image`, the digest the item locked, never pulled; its entrypoint is the runtime, which reads its spec on standard input and exits with Contract C7's codes: the contract with any runtime image.
- **The user** is `65532:65532`, with `runner.docker.group` as a supplementary group, which lets it open the sockets.
- **The network** is `none`; DNS is `127.0.0.1` with search `.`, so that the host's resolver and search domains are not copied in. **IPC** is `none`, so there is no `/dev/shm`.
- **The root** is read-only; every capability is dropped, with `no-new-privileges`.
- **PID 1** is the engine's init (`Init: true`), which reaps and forwards signals. When the container ends, its PID namespace ends every process of the turn, a `setsid` one included.
- **Limits:** memory [256 MiB], with no swap; CPU [1]; processes [64]; open files [256]; the OOM score raised by 500, so that the host's OOM killer takes a sandbox before serve. Serve's unit gets the larger CPU weight (Decision 13).
- **Writable:** `/tmp`, a tmpfs of [16 MiB], `noexec,nosuid,nodev`, which is also the working directory; besides it, only what the kernel gives every container (`/dev/mqueue`, `/dev/pts`, and `/proc/self/oom_score_adj`, which can only be raised). `noexec` guards against accidents only, since a process can run code from `memfd_create`: the barrier is that the image has no shell and the runtime runs nothing.
- **The one mount** is the sockets directory, read-only, at `/run/agentswarm`, of type `bind`, which fails when the directory is missing rather than letting the engine create it as root.
- **No environment of serve's; no health check** (`NONE`, whatever the image says).
- **Standard input** is open and attached by the runner before the start; the engine closes it when the runner's attachment ends (`OpenStdin`, `StdinOnce`).
- **Stop** is `SIGTERM` with 10 s before the engine's SIGKILL, whatever the image says: an image's own stop signal, such as `SIGQUIT`, would make an engine's stop end a Go runtime with a code that reads as a crash.
- **No logs in the engine** (`LogConfig: none`): the attachment carries the output, which the runner writes to the turn's file.
- **Name and labels:** `agentswarm-turn-<turn id>`; the labels `agentswarm.tenant`, `.holder`, `.turn`, `.run`, `.agent`, `.round` and `.deadline`, and the deployment's `runner.docker.labels`.

A turn can still learn the kernel's version (`/proc/version`), the host paths of its mounts (`/proc/self/mountinfo`) and the engine's generated `hosts` and `hostname`. None of it reaches a prompt (criterion 5), and gVisor would hide the first (carried).

### Decision 4. Launches outside the coordinator's lock (the author's decision, 2026-10-28)

The coordinator calls `Turns.Start` under its lock, once per agent of a round and for every item at a takeover. Review 1 measured a launch through the moby client at 459 to 507 ms: a round of four held the lock 1.94 s, and a takeover of twelve turns 5.85 s; an engine at 4 to 5 s a launch would hold it 20 and 60 s while `Exited`, deadlines, settling and openings wait. So the launch leaves the lock. A turn not yet started already stays in `r.starting`, which `deadline` counts as unstarted and `whenAllEnded` waits for, so a launching turn needs only a mark and `Launched`.

- **The port** (Contract C10). `Start(ctx, t) (pending bool, err error)`:
  - `err != nil`: the launch failed now; as today, the coordinator tries the turn again after its retry wait, with the same id, while the round lasts, and the turn counts as unstarted;
  - `pending == false`: the turn runs (the process runner's answer);
  - `pending == true`: the runner launches the turn after `Start` returns, and reports the outcome once, through `Coordinator.Launched(ctx, Launch{Run, Agent, Round, Turn, Reason, Err})`: `Err == nil` means the turn runs, and its end comes through `Exited`, never before `Launched`; otherwise it is as if `Start` had failed, and `Reason` says why (Decision 7).
- **Locks.** The runner calls `Launched` and `Exited` while holding none of its own locks. The coordinator calls `Stop` under its lock, so the reverse order would deadlock it, and the leader with it, since the leader's `lose` fences under the leader's lock.
- **The coordinator.** A pending turn stays among the round's starting turns, marked as launching, with no retry armed. `Launched` without an error moves it to the running turns, and judges then whether it started too late (with less than a third of the round's span left). `Launched` with an error does what `begin` does for a failed `Start`: logs the step's failure, posts the notice (Decision 7), arms the retry. A `Launched` the coordinator no longer waits for (another turn's id, a round that ended, a run that stopped, a fence) changes nothing, with one exception below; a retake by the same process gives new ids. At the deadline a launching turn counts as unstarted, as a starting one does, and is stopped, and the coordinator keeps its id until `hold` turns to the run's next round: `hold` clears it before it posts that round's start (R5). The exception: a `Launched` with an error for that turn, while the run is still on that round in the same holding, posts the notice and arms no retry, since a launch queued while the engine hangs reports at its deadline, as the round ends (Decision 7; plan review 1, M1). `whenAllEnded` keeps waiting while one launches, and `startAgain` skips a launching turn. `Launched` takes the coordinator's lock, so it never comes before `Start` returns.
- **Forgetting.** A runner forgets a turn once it has reported its end. That is safe because the coordinator never starts a turn again once it learned of its end: `startAgain` needs a starting turn, which `Exited` removes. Both runners forget reported turns, so the process runner's `launched` set no longer grows for the life of the process. The `Turns` port's doc, and Plan 7d-1's Decision 12 text, say all this.

### Decision 5. The Docker runner (`internal/runner/docker`)

It implements `app.Turns` as Plan 7d-1's Decision 12 says, the holding check and `unavailable` for an undeliverable spec included. The differences:

- **`Start`** runs under the coordinator's lock and calls no engine. It checks the holding (a refusal wraps `app.ErrUnheld` and is logged `holding refused`, `request` `turn`); checks the turn's id, which names the output file and the container, and whether the turn is registered already (R25); creates the turn's output file, `<runner.output>/<turn id>.out`, mode 0600, returning its failure, so that an unwritable output directory fails the launch as it does under the process runner; registers the turn, arms its deadline's timer, queues its launch, and returns pending.
- **States.** A registered turn is **launching** (queued, or in steps 1 to 5 below), **started** (step 6, its container's start, is in flight), or **running** (from step 7 on, whether or not its `Launched` has been reported yet); a flag beside the state says whether its `Launched` was reported (R25). This corrects the design, whose running state began once `Launched` was reported: running from step 7 is what the design's own step 7 does, and the flag keeps the report apart. A container is **tracked** while its `agentswarm.turn` label names a registered turn; the sweep never touches a tracked container.
- **The launch** runs off the lock. At most 8 launches run at once, fixed in code (review 1 measured twelve at once at 2.57 s); a launch waits for a slot until its turn's deadline or a stop, and has 10 s, fixed in code, from when it gets one. Every engine call returns once its context ends, the attachment included, whose hijack in the moby client would otherwise ignore the context once it has dialed (plan review 1, I1).
  - **Steps 1 to 5** run on a context derived from `Start`'s, so that a fence or a drain, which cancel the holding's context, cancel them, and a stop cancels them too: (1) force-remove any container with the turn's name, left by an earlier attempt of the same turn; (2) check the image (Decision 6), reloading it from the store when the engine lost it, and choose the spec's version (Decision 9); (3) create the container; (4) attach to its standard input and output; (5) check the holding again.
  - **Steps 6 to 8** run on a context that a fence does not cancel, within what remains of the 10 s, since a cancelled request does not stop the engine and a start once sent must be seen through: (6) start the container; (7) under the runner's lock, check whether the turn was stopped meanwhile and, if not, move it to running, in one step, so that a stop after the check finds it running and kills it the usual way; (8) if it was stopped, kill the container, wait for its end, remove it, forget the turn, and report `Launched` with the reason `cancelled`, which posts no notice; otherwise start delivering the spec, copying the output and waiting for the container, and report `Launched`.
  - **The turn's goroutines** (the delivery, the copy, the wait) run on `context.WithoutCancel` of `Start`'s context, as the process runner's do, so that a fence or a drain does not cut them. Delivering the spec does not wait for `Launched`, which waits for the coordinator's lock; only the report of the turn's end waits for `Launched`.
  - **A launch that fails** removes what it made, by the container's id, on a fresh context of its own bounded by 10 s, so that a cancelled launch still cleans up; forgets the turn, then reports `Launched` with the error and its reason, so that the coordinator's retry finds the id free. The bound running out, or the engine not answering, reads `engine`; a stop, a fence or a drain reads `cancelled`, but a launch that a `Stop` ends at or after its turn's deadline ends as the deadline ends it: `engine` while the engine's last call found it unreachable, otherwise `cancelled` (plan review 2, F1); the holding's refusal reads `holding`; an image missing from the engine and the store, breaking the contract, or reading no spec version the runner writes, reads `image`; any other failure reads `launch`.
- **Stops.** At the deadline, on `Stop` and on `StopAll`, the runner cancels a launching turn's launch, marks a started turn stopped (step 7 then kills it), and sends SIGKILL to a running container from a goroutine. It never waits for the engine, and never holds a lock while the engine answers.
- **Serve's exits.** On every exit serve calls `KillAll(ctx)`: the exit at once, the end of a drain, a leader stopped over the schema, an error of `Lead`. `KillAll` cancels every launch, force-removes by name every registered turn's container in parallel (the engine orders a removal after a start in flight), and waits up to 1 s, fixed in code, for the engine's answers, so that the kills are sent before serve exits. Only then does serve close the runner: `Close` stops the minute's job, waits at most 1 s, fixed in code, for its pass in flight, releases the holder's lock and closes the engine's client. `Close` never comes before `KillAll`, whose removals need the client (plan review 1, M2).
- **Serve's death.** The attachment holds the turn's standard input open. When serve's process ends, the engine closes it, and the runtime ends at once (review 1: within 0.17 s). A runtime that ignores the end of its input runs until a sweep finds it.
- **How a turn ended** is advisory, as in Plan 7d-1, read from the container's state and judged by its `FinishedAt`, not by when the wait returned (Contract C7 gives the order): the board's turn end means `ended`; an end at or after the deadline, or by the runner's own kill, `interrupted`; a spec not delivered, `unavailable`; an end the engine's OOM killer made (`OOMKilled`), `crashed`; an exit code of C7's table, its class; an exit by SIGHUP, SIGINT, SIGKILL or SIGTERM (128 plus the signal), which only the engine or an operator sends (a `docker stop`, an engine's restart), `unavailable`, since spec Section 8.2 says a turn the platform could not serve does not count; anything else, a runtime's own SIGSEGV or SIGABRT included, `crashed`. Review 3 probed that the engine sets `OOMKilled` when the init's child is killed for its memory, so a memory kill is told from an outside SIGKILL.
- **A failed wait.** The runner inspects the container again. While it still runs, as under the engine's live-restore, the runner waits again up to the turn's deadline plus the runtime's slack, then kills it by name. While the engine does not answer, the runner retries the inspect with a backoff up to the same time, then reports the turn `unavailable` and leaves the container to the sweep. A container that is gone is `unavailable`.
- **Ending a turn.** Once its end is known the runner inspects the container, reports the end, forgets the turn, then removes the container with its volumes on a bounded context of its own; a removal that fails is left to the sweep.
- **The sweep** runs at start and every minute, fixed in code. It lists the containers labelled with its tenant, inspects each candidate, and acts on ids, never on names, since a retry reuses a turn's name. At start, after it takes its holder's lock (Decision 8) and checks the images (C14 gives `Open`'s order), and before any turn, it kills if they run, and removes, every container of its own holder: they are its previous process's, since it tracks nothing yet. Later it removes its own holder's untracked containers once due: stopped ones whose `FinishedAt` is over a minute past; never-started ones (a zero `FinishedAt`) created over 10 s ago; running ones whose `agentswarm.deadline` is past by the runtime's slack and a minute, which covers a turn stopped while its start was in flight. Another holder's containers fall due only once their `agentswarm.deadline` is over 5 minutes past, so that a live turn of another instance, or a clock step of a few minutes, is never touched; one whose deadline label is missing or unreadable falls due an hour after its creation. A due container is killed if it runs, then removed.
- **Logs** (Contract C3): `turn started` at step 7, with `turn`, `container` and `launch`; `turn exited` by both runners, with `turn` and `class`.

### Decision 6. Runtime images: built, checked and kept (the store is the planner's call, approved with this plan)

- **The build** (`make image`, Contract C12). `build/runtime/Dockerfile` is `FROM scratch` with the `agentswarm` binary at mode 0755 (`COPY --chmod=0755`, whatever the builder's umask: Debian's 002 and macOS's 022 would otherwise make two layers), `USER 65532:65532`, `ENTRYPOINT ["/agentswarm", "turn"]`, the OCI labels for version, revision and source (the version and the revision are the commit's id), `io.agentswarm.turn-spec` (the spec versions the runtime reads, comma-separated) and `io.agentswarm.go` (the toolchain). The binary is built from `git archive` of the commit, so that the image holds the commit's content and nothing of the worktree: static (`CGO_ENABLED=0`), `-trimpath`, `-buildvcs=false`, its date the commit's time and its version the commit's id (not `git describe --dirty`, so that a release tag added later changes nothing: a tag becomes another tag of the image), its toolchain pinned with `GOTOOLCHAIN`. The image is built without the cache, with `--provenance=false --sbom=false`, `SOURCE_DATE_EPOCH` the commit's time, the binary's mtime set to that time in UTC, and BuildKit's `rewrite-timestamp=true`. `make image` tags the image `agentswarm-runtime:<its id's 64 digits>` and prints the id.
  - **Why each step** (probed on 2026-10-28). The Makefile's `DATE`, Go's VCS stamping and an unpinned toolchain each made two builds of one commit differ. On the classic store, two builds gave one id once the mtime was set, and `rewrite-timestamp` did nothing. On the containerd store, two builds with attestations off and the mtime set gave one id, and so did two with `rewrite-timestamp`, even after a `touch`; two with the defaults gave two ids, since the provenance attestation carries the build's time.
  - **Store-specific ids.** One build has another id on each store: the config's digest on the classic store, the manifest's on the containerd store. Changing an engine's image store therefore invalidates every lock, and the docs say so. Two engines' BuildKits may compress layers differently, so the docs promise one id per engine, not across engines.
  - **The test** (`make test-image`) builds one commit twice and finds one binary and one id, on the engine at hand; at the branch's end the controller runs it against a containerd-store engine too.
- **The gates' image** (`make image-dev`). The gates run the worktree's code before its commit, so `make image`'s build of a commit would put the last commit's runtime in a gate's sandboxes while serve runs the task's (review 3, I1). `make image-dev` puts the same Dockerfile around the `bin/linux/agentswarm` the gate builds from the worktree, tags the result `agentswarm-runtime:dev-<the first 8 hex digits of the SHA-256 of the worktree's path>`, a tag of each worktree's own, and hands its id to the harness. It promises nothing about reproducibility: it builds with the cache, without `SOURCE_DATE_EPOCH` or `rewrite-timestamp`, and, as `make image` does, with `--provenance=false --sbom=false`; and it removes, after a backup of its `docker image inspect`, the dev image it replaces, its own worktree's alone, so that a gate in another worktree on the same engine keeps its image (C12; plan review 2, F4). `make image` stays the release build.
- **The contract an image must meet** (Contract C12): for Linux and the engine's architecture; `io.agentswarm.turn-spec` lists a version the runner writes; no `Volumes`, health check or `OnBuild`; no environment but `PATH`; no label that starts with `agentswarm.`, since an image's labels join the container's, and the sweep and `make test-clean` read those. Serve checks it at start for each image of `runner.images`, the runner again every minute, and at each launch for the image the item locked. A failure at start refuses the start; at a launch it fails the launch with the reason `image`.
- **Keeping them.** No container holds a runtime image between rounds, so a `docker image prune -a` or `docker system prune -a` would delete them all, and an item that locked a digest could never run again. So:
  - **The store.** `runner.docker.store` is a directory serve must be able to write, or it refuses to start. At start serve saves each image of `runner.images` there as `<64 hex digits>.tar`, unless it is there already; a save is written to a temporary file and renamed.
  - **Reloading.** When the engine lacks an image an item locked, the runner loads it from the store before the launch, or at its minute's check; checks that the loaded image has that id; and tags it as `make image` does, since a loaded image is untagged and a plain prune removes untagged images. Probed on both stores: a save by id, a removal and a load gave back the same id, in 0.16 s for 35 MB.
  - **Deleting.** Serve deletes nothing from the store. A copy can go once no active item locked its image, and the docs say how to find that out.
  - **Backing it up.** A locked image comes back from the store, not from a rebuild, which on another engine or by another builder may give another id; so the docs put the store among what a deployment backs up.
  - **Sharing.** Both instances on a host share the store.
- **Switching an active deployment from `process` to `docker`** fails the turns of its active items, whose locks name the process runner's placeholder digests, until they stop. The docs say to switch with no item active, and the notice (Decision 7) shows any item that was.

### Decision 7. Notices and status

- **A notice for a turn that could not start** (Contract C11). Spec Section 8.1 says nothing fails silently. The coordinator posts, at a failed launch, a notice of the new code `turn_unstarted`, which names the agent and a new field, `reason`: `engine` (the engine did not answer), `image` (the image is missing and not in the store, or breaks the contract) or `launch` (any other launch failure).
  - `reason` is not part of the board's key for repeated notices (round, agent, code, tool, rule), so the board's deduplication gives one notice per agent per round, across takeovers too.
  - `Launched` posts it under the coordinator's own context, which carries the holding's epoch, as guarded writes need. `begin` posts it too, with `launch`, when `Start` itself fails, as the process runner's can; a holding's refusal (`app.ErrUnheld`) posts nothing.
  - Nothing is posted for a launch the holding refused, or one a fence, a drain or a stop cancelled (`holding`, `cancelled`): a refusal means the instance is losing the lease, and its write would be refused. A launch still waiting for a slot at its turn's deadline ends `cancelled`, but while the engine does not answer it ends `engine`, so that a hung engine's queued launches still show. A launch that a `Stop` ends at or after its turn's deadline ends as the deadline ends it: `engine` while the engine's last call found it unreachable, otherwise `cancelled`, since the coordinator's deadline stops its launching turns at the instant the runner's own timer fires, and either may come first (plan review 2, F1). Its report comes as the round's deadline ends the round, so the coordinator posts the notice of a turn that was launching at the deadline while the run is still on that round in the same holding, and arms no retry (C10).
  - A notice that cannot be posted changes nothing else: the retry is armed all the same, as the MCP gateway's rule for its notices says. A post that finds the lease gone fences, as every write of the coordinator does. Since `begin` posts inside `hold`'s loop over the round's agents, `hold` checks `c.held` after each `begin` and stops once it is false, so that a fence there starts no other agent's turn (plan review 1, M12).
- **The board's version stays 1.** No release exists yet, and Plan 7d-1's R12 keeps bodies at version 1 until the first release. A version 2 would also break scenario 11(a), whose fixture stands for a later build's event as version 2.
- **Leniency on replay, within bounds.** So that a notice code added after the first release needs no new board version, replay accepts a notice whose code it does not know when the notice is well formed: the platform is its author; its code, and its `reason` when it has one, match the stop reason's format, `^[a-z][a-z_]{0,31}$`; its other fields are ignored; and it feeds no decision. Posting stays strict: this build posts only the codes and reasons it knows, through a check that replay does not share. A later code that names a call, or feeds a round's facts as model-call notices do, still takes a new board version, since an older build would count it as neither and could judge a round quiet that the later build would not. `agentswarm conclude` decodes board files strictly, so an older build's `conclude` refuses a board that holds `reason`: an offline command, and the docs say so.
- **`/v1/status`** (Contract C4) gains `runner`: `{"kind": "docker", "sandboxed": true, "engine": "ok", "missingImages": []}`, where `engine` is `ok` or `unreachable` as the last call to the engine found it (a status request calls no engine). The process runner reports `{"kind": "process", "sandboxed": false}`, and serve logs at start that its turns run unsandboxed.
- **`/readyz` is unchanged:** it says whether the admin API can serve, and a standby is ready.

### Decision 8. Serve with the Docker runner

- **The deployment file** (Contract C1) gains `runner.kind: docker` and a `runner.docker` block: `endpoint` (optional, `unix:///var/run/docker.sock`, `unix://` only), `sockets` (required: serve listens on `mcp.sock` and `llm.sock` there), `store` (required), `group` (optional: the gid the sockets and the sandboxes share; by default serve's own), `labels` (optional), and the limits `memory`, `cpus`, `pids`, `files` and `tmp`. With `kind: docker` these are refused: `listeners.mcp` and `listeners.llm`, which C1 now requires only with `kind: process`; `runner.binary` and `runner.unsafeNoSandbox`, the process runner's; and, in `runner.docker.labels`, the runner's own keys and `agentswarm.data`, which `make test-clean` reads (other keys, `agentswarm.project` among them, are allowed).
- **Serve refuses to start** with the Docker runner, naming the key, when:
  - it runs on a system other than Linux;
  - the endpoint is not `unix://`, or the engine does not answer;
  - an image of `runner.images` breaks Decision 6's contract, or is neither on the engine nor in the store;
  - the store is not a directory serve can write;
  - another serve holds this tenant's and holder's lock: a file serve holds with `flock` while it runs, `<store>/holders/<hex of the tenant>.<hex of the holder>.lock`. Encoding each name keeps a `/` from moving the path, and keeps `x-y` with `z` apart from `x` with `y-z`. Instances on one engine must share the store, which the docs say, so that a holder copied into a second deployment is refused rather than its start sweep killing the first one's turns;
  - `runner.docker.group` is 0, or the engine socket's group, or a group serve is not a member of;
  - `runner.docker.sockets` is not an absolute path to a directory that serve can write and the group can traverse; holds anything but `mcp.sock` and `llm.sock`, which serve replaces; is locked by another serve (serve holds an exclusive `flock` on it while it runs); gives a socket path longer than a Unix socket's 107 bytes; or lies inside, or holds, the secrets directory, the output directory, the store, the addresses file or the configuration repository.
- **The sockets** are made at start, mode 0660 and the group `runner.docker.group`. A runaway runtime's connections are bounded by its own open files [256] and the listener's header timeout. Serve's own file limit is the system's: Go raises its soft limit to the hard one at start.
- **Contracts that change:** C2 (`mcp` and `llm` hold `unix:<path>`), C3 (`serve started` gains `worstDrain`; `turn started` has `container` and `launch` under the Docker runner and `pid` under the process runner; `turn exited` joins the contract), C4 (`runner`), C6 (Decision 9) and C7 (Decision 9).

### Decision 9. The runtime, and the turn's spec across builds

- **C6 gains** `v`, the spec's version (1 for Plan 7d-1's form, 2 for this plan's); per gateway, `socket`, the Unix socket the runtime dials for every connection to that gateway whatever its URL's host; and once, `cert`, a PEM certificate the runtime trusts alone for the gateways' `https` URLs. Under the Docker runner the MCP gateway's entry is `{"url": "https://mcp.agentswarm.internal/", "token": "…", "socket": "/run/agentswarm/mcp.sock"}`, and the LLM gateway's has the same form.
- **The handshake.** A runner writes the spec for the image an item locked, which may be older or newer than serve. The image lists the versions it reads (`io.agentswarm.turn-spec`); the runner writes the newest version both know, and fails a launch with the reason `image` when there is none. Fields are added, never renamed or removed, and a runtime leaves a field it does not know alone, as it does today. One Go type, in one package, is what the runners write and the runtime reads.
- **The runtime's transport:** with `socket`, it dials the socket; with `cert`, it requires TLS 1.3, trusts that certificate alone, and checks the URL's host against it; it speaks HTTP/1.1; as today, it uses no proxy and follows no redirect.
- **SIGTERM.** The runtime ends at SIGTERM, at once, with code 15 (`unavailable`): only the engine or an operator sends it, as when the engine stops or restarts. Today it handles SIGINT only and dies by SIGTERM, which the engine's init reports as 143, which C7 read as a crash (review 2, I2, probed). The runner's rule for signals it did not send (Decision 5) covers runtimes that do not handle it.
- **C7 is amended for both runners.** A turn that ended by SIGTERM, SIGKILL, SIGINT or SIGHUP, which its runner did not send, and not for its memory, is `unavailable`; any other code outside the table is still `crashed`. The process runner reads a host process's signals the same way: an operator's `kill`, or a host's shutdown, is not the agent's. C7's comment in `internal/outcome`, ADR-006's table, the runtime's help (`agentswarm turn --help`) and the process runner's doc change with it.

### Decision 10. Serve's privileges (the author's decision, 2026-10-28)

- **The risk.** In this plan serve's user is a member of the `docker` group, so that it can start sandboxes. That is root on the host in effect: a compromised serve, or its account, compromises the host, the other instance and anything else that runs there. Serve already holds the vendor's key, the MCP servers' credentials and the database's URL, and it parses MCP outputs and model completions.
- **Where it is written.** ADR-006 records the risk, and the docs say to give the platform a host, or a virtual machine, of its own.
- **A guard.** Serve refuses a `runner.docker.group` of 0, or of the engine socket's group (Decision 8).
- **Carried, to before a team's measurement window** (spec Section 14.3; Plan 10): a narrow launcher, a small process of its own that holds the engine's access and offers only "start, kill, list and remove a sandbox of the fixed profile", so that a compromised serve could neither widen a sandbox nor reach the host. Rootless Docker is the alternative to weigh then.

### Decision 11. Criterion 4 and the sandbox's limits, tested

Integration tests of `internal/runner/docker` run on a Linux engine with the runner's own sandbox settings and a probe image, a pinned curl image with busybox, in place of the runtime. A seam the package keeps for its tests skips the image's contract and sets the probe's command; production code cannot reach it. They check:
- **network:** the only interface is `lo`; no name resolves; no TCP connection opens to a public address, to `169.254.169.254`, to the engine's bridge gateway, or to `host.docker.internal`;
- **gateways:** both sockets answer over TLS 1.3 and HTTP/1.1 with the spec's certificate; a sandbox without the sockets' group cannot connect;
- **files:** the writable paths are exactly Decision 3's; the mount and the root are read-only; there is no engine socket and no secrets directory; `resolv.conf` names `127.0.0.1` alone;
- **privileges and limits:** the process runs as 65532, with no capability (`CapEff` 0), `NoNewPrivs` 1 and no `/dev/shm`; a fork past [64] processes fails, and an allocation past [256 MiB] ends the container `OOMKilled`;
- **the image's contract:** an image with a `VOLUME`, a `HEALTHCHECK`, an environment beyond `PATH`, a label that starts with `agentswarm.`, or no `io.agentswarm.turn-spec`, is refused;
- **the `setsid` escape:** the runner kills a probe that starts `setsid` children, and afterwards no process and no container of it remains;
- **serve's death:** when the runner's attachment ends, the container ends;
- **the engine's signals:** a container stopped with `docker stop` reads `unavailable`, and so does one killed by a SIGKILL the runner did not send.

These tests are ADR-006's "the suite's host probe is its check", and they include the example deployment profile's sandbox probes.

### Decision 12. Tests on Linux, and the harness on Docker (the gates are the planner's call, approved with this plan)

- **The Linux test image** (`build/test/Dockerfile`) is based on `golang:1.26` (Debian) pinned by digest; adds the Docker CLI's static release, pinned by version and checksum, since the suite shells out to `docker`, and Debian's `jq`, which the live gate's steps use; and makes the module cache, the build cache and a home directory writable by any user, so that a named volume mounted there takes that mode.
- **Lint** runs on the Mac, twice: as before, and with `GOOS=linux`, so that the Linux-only files are linted too. The test image runs no linter.
- **`make test-linux`** (Contract C15): builds the test image, the worktree's runtime image (`make image-dev`, from Task 5) and the stubborn test image (from Task 12), and hands the two images' ids over as `AGENTSWARM_E2E_IMAGE` and `AGENTSWARM_E2E_STUBBORN`; makes `/srv/agentswarm-e2e` on the engine's host with a container that runs as root, and gives it to the developer (the engine would make a missing directory owned by root); reads the engine socket's group from inside a container (on OrbStack it is 0 inside, though the Mac's `stat` says 20); and runs the suite in the test image, as the developer's own uid and gid with that group added, with `--init`, the engine's socket, named volumes for the caches, the Linux binary at `bin/linux/agentswarm`, and `/srv/agentswarm-e2e` mounted at the same path inside and outside. In turn it runs `make test` and `make test-integration`; `make test-e2e` with `AGENTSWARM_E2E_RUNNER=docker`; and the process runner's scenarios that check a process is gone, by name, which ran on darwin only in Plan 7d-1. In-container testcontainers works on OrbStack, since published ports are reachable through the bridge gateway and `host.docker.internal` (review 1, probes G1 and G2).
- **`make test-docker`** is the Docker path alone: the same preparation, then, in the test image, the Docker runner's integration tests and the whole e2e suite under the Docker runner (a list of the scenarios that start turns would go stale; the suite's other tests are quick).
- **The engines, and `/srv`.** On a developer's Linux host, `make test-linux` makes `/srv/agentswarm-e2e` as root, and the targets need the buildx plugin, which Docker 29's builds use. On a Mac they need OrbStack, in whose engine's VM `/srv` resolves: serve's sockets and the sandboxes' mounts must share that path, and Docker Desktop does not share `/srv`. The docs say all this, and that `make test-clean` leaves `/srv/agentswarm-e2e` in place.
- **The gates** are the Global Constraints' (review 2 measured a Mac gate at about 20 to 25 minutes, and a full Linux gate at as much again). Later plans that touch serve or a runner keep `make test-docker` in their gates: there is no CI, so this is how the production runner stays tested.
- **The harness on Docker.**
  - **Choosing the runner.** The harness writes a Docker deployment when `AGENTSWARM_E2E_RUNNER=docker`, and the process runner's otherwise. Every scenario runs under both, except any that says why not; on a system other than Linux, a scenario that serves under Docker skips, saying "needs Linux: make test-linux".
  - **A tenant per invocation,** so that two gates on one engine never sweep each other's sandboxes. The harness reads its tenant from `AGENTSWARM_E2E_TENANT`, by default `harness`. `make test-docker` and `make test-linux` set a unique one for each `go test` invocation, whose tests share it, so that each start's sweep removes an earlier test's leftovers; at its end, `build/test/run.sh` removes every container of its invocations' tenants, running or not, after a backup (C15), since no later sweep lists those tenants again. `test/compat` leaves it unset, so that its check of the previous release's tenant keeps working after this plan is released. The four SQL statements that name the tenant `harness` use the harness's tenant: the lease's expiry, scenario 11(a)'s later record and later event, and the inspect-raw test's record. The sockets' directories go under `/srv/agentswarm-e2e/<tenant>/`.
  - **A stubborn test image,** `FROM busybox` pinned by digest, built from `test/e2e/testdata/stubborn/` and labelled with the spec's versions, which ignores the end of its input and SIGTERM and sleeps for at most 120 s, as Plan 7d-1's stubborn script does (R5), so that one left by a killed run does not live until `make test-clean`. It is built with `--provenance=false --sbom=false`, as the runtime image is, so that a containerd-store engine attaches no attestation to an image the store then keeps. The harness locks it, in place of `runner.binary`, for scenario 7 and scenario 16's stubborn case.
  - **Scenario assertions under Docker:** scenarios 8 and 16 say "no container of the run is running" within their bound (the next start's sweep removes the containers); `TestASecondSignalKillsEveryTurnAndExitsAtOnce` keeps its 2 s bound, since `KillAll` waits at most 1 s and the runner's `Close` then waits only for a minute's pass in flight, which takes milliseconds; each reader of `pid` reads `container`; the addresses' assertions follow the runner; scenario 7 measures a turn's life from its round's opening, not from `turn started`, which under Docker comes after the launch; scenario 14 also reads each turn container's `docker inspect` while the turn runs, since a container's configuration must hold no token.
  - **Checking containers.** The harness learns a turn's container from `turn started`, and checks it with the `docker` command line, as it already runs `psql`; the depguard rule `e2e` forbids the moby client.
  - **Labels.** Every container a test starts carries `agentswarm.project=agent-swarm`, through `runner.docker.labels` for sandboxes, so that `make test-clean` finds those still running an hour on. It lists running containers alone (`docker ps` without `-a`), so a stopped sandbox is removed by a later start's sweep within its invocation, or by `run.sh` at the invocation's end, not by `make test-clean` (plan review 1, M8). `run.sh` also removes the containers of the Docker runner's integration tests, by the label `agentswarm-it.run` that carries its invocation's id (C15; plan review 2, F5).

### Decision 13. Two instances on one host, and their repository (the planner's call, approved with this plan)

`examples/two-instances/` holds what a first deployment copies, and a test keeps its deployment files loadable.
- **Deployment files** `a.yaml` and `b.yaml`: holders `a` and `b`; each its own sockets directory, output directory, addresses file and admin port; shared database, secrets directory, image store, images and configuration repository.
- **One bare clone on one host**, `/var/lib/agentswarm/config.git`, read by both instances: one fetcher removes the lag two clones would have between the instances (on two hosts, each keeps its own). `agentswarm-config.service` and its timer run every minute `git --git-dir=/var/lib/agentswarm/config.git fetch --no-tags origin refs/heads/main:refs/heads/main`; the refspec has no `+`, so the fetch is fast-forward only, and a force-push stops it, visibly, rather than dropping commits that items locked. The clone sets `gc.auto=0` and `maintenance.auto=false`, so that no automatic repack removes a pack while go-git reads it, and a weekly timer runs `git gc`; `gitsource` opens the repository again for each read, so it sees new packs. `OnFailure=` names a unit, filled in by the deployment, that notifies someone.
- **`agentswarm@.service`**, one unit per instance: `User=agentswarm`, `Group=agentswarm`, `SupplementaryGroups=docker`; `After=` and `Wants=docker.service`, not `Requires=`, so that an engine restart does not stop both instances; `Restart=always`, `RestartSec=5` and `StartLimitIntervalSec=0`, since serve refuses to start while the engine is down; `TimeoutStopSec=120`, above the worst drain (Decision 14); `CPUWeight=1000`, so that serve outweighs each sandbox, whose cgroup has the default weight of 100 (review 2 measured that `--cpu-shares 512` gives a weight of 59, not half); no `LimitNOFILE`, since the system's hard limit is higher than one the unit would set; `RuntimeDirectory=agentswarm/%i` with mode 0750 for the sockets; no `PrivateTmp` or `BindPaths` for that path, which must be the same for serve and the engine.
- **The engine's live-restore stays off,** Docker's default: an engine stop or restart then stops each sandbox with SIGTERM, the runtime ends with 15, the runner reads `unavailable`, the round tries the agents again, and no agent is counted against. With live-restore on, turns would survive an upgrade but lose their output stream and the runner's re-wait would run; it is not tested, and the docs say so.
- **Supported hosts:** Debian 12 or Ubuntu 24.04, with Docker Engine 29, on either image store. SELinux-enforcing hosts are not tested: a container's domain may be refused the sockets.
- **Where serve runs in the tests.** Every Docker-runner test (Task 11's integration test, the e2e suite under Docker, and the live gate) runs serve inside the test container, on the engine's default bridge network, with the engine's socket mounted. The topology the examples recommend, serve on a Linux host under systemd, no test here runs. The code and the paths are the same, so the risk is small, and this plan accepts it: the first real deployment checks the examples' systemd topology on a Linux host (carried; plan review 1, M11). The docs also describe serve in a container for a deployment, with what it needs (the host's network, `--init`, a stop timeout above the worst drain, the engine's socket, the sockets' directory and the store at the same paths).

### Decision 14. The worst drain, and the drain's wait for the leader

- **The worst drain.** Serve computes it from its deployment, in one function, and logs it at start (`worstDrain`); the examples set the supervisor's stop timeout above it. Its terms, read from the drain's code (R22), are 82 s with the defaults; the function's test pins each term.
- **The wait for the leader.** The drainer waits for the leader's stop up to the leader's busy bound, `takeWait + 3 × renewEvery` [20 s], rather than the lease's time [15 s]. That bound covers a take's sample, the take, and a given-up take's release. A take that succeeded and then met the stop runs up to 25 s; the wait cuts it harmlessly, since the stop has released the lease already. The test pins which path the bound covers.
- **Launches** leave the coordinator's lock (Decision 4), so the fence waits for no launch, and the worst drain gains no term for them; at the exit, `KillAll` adds at most 1 s, and the runner's `Close` at most 1 s more.

### Decision 15. The live gate (the controller runs it; its spend is the planner's call, approved with this plan)

It runs on Linux: in the test image on this Mac, with serve and `itbench serve` in the same container (Task 16).
- **The vendor's key** is written from the keychain to a 0600 file, as on 2026-10-21; mounted read-only into the container at serve's secrets path, never passed as an environment variable or an argument; and the file is deleted after the run (the keychain keeps the key, so the file needs no backup).
- **A real model through sandboxes:** scenario S16 under serve with the Docker runner, as the 2026-10-21 smoke ran it on the process runner, about $0.02 on Claude Haiku 5.5.
- **The slowest model's output rate:** for each model the agents use, three calls, each with a prompt that elicits an answer of at least `runner.maxTokens`; each call's output tokens per second are recorded; it passes when the slowest writes `runner.maxTokens` within both the round's time limit and the call timeout (Plan 6b's constraint; Plan 7d-1, R14).
- **The two echoes Plan 7d-1 left unverified:** the gate takes a real answer that holds a thinking block, from a prompt that asks for reasoning first, as Plan 6b's live check does; sends back conversations that echo it twice, once with its text emptied and once with its text replaced by whitespace; and records whether the vendor accepts each. If the vendor refuses either, the runtime changes what it sends back, test first.
- **The cost** is about $0.10 in all.

### Resolutions this plan adds to the design

The design left these to the builder; each is a choice made here, and what it costs if it is wrong.

- **R1. The shared spec type** is `internal/runner/spec`. The runtime reads versions 1 and 2, so the runtime image's label is `1,2`, and a test keeps the Dockerfile's label equal to `spec.Reads`. The Docker runner writes version 2 only, since it needs `socket` and `cert`; the process runner writes version 2 without either, which its own executable reads. The runtime refuses a spec of a version it does not read, as it refuses any spec it cannot take (exit 1). Cost if wrong: a label and a list to change.
- **R2. One transport per gateway** in the runtime, since each gateway has its own socket: `TestTheRuntimesTransportIgnoresProxies` changes. Cost if wrong: none outside the runtime.
- **R3. SIGTERM** makes the turn command write one line on standard error and exit 15 from its signal's goroutine, at once, printing no result: the go-sdk may wait up to 5 s for its cancel notice during the toolbox's first connection (Plan 7d-1, Task 12). Cost if wrong: a turn's result lost at an engine's stop, which nothing reads.
- **R4. `app.ErrUnheld`** is the sentinel a runner's holding refusal wraps, declared by the port's consumer, so that `begin` posts no notice for it; the process runner's `errUnheld` wraps it. Cost if wrong: a sentinel to move.
- **R5. The coordinator marks a launching turn** in `running.launching`, which maps the agent to the turn's id, beside `starting`, which keeps the turn. `hold`'s reset of a run for its new round (`coordinator.go:975`) makes the map with the round's others, since `begin` writes to it and a nil map would panic. `deadline` clears `starting` but leaves `launching`, so that a failed `Launched` for a turn that was launching at the deadline is still known (Decision 4's exception). `hold` clears it before it posts the next round's start (`StartRound`), so that a late `Launched` for round N can never post into round N+1, not even when `hold` fails between that post and its reset, leaving the run on round N while the board is on N+1 (plan review 2, F6). Cost if wrong: none outside the coordinator.
- **R6. Launch reasons** are `app.LaunchReason` constants, and a notice's `reason` is the same word. Cost if wrong: a mapping to add.
- **R7. The image check every minute** runs in the runner's minute job, beside the sweep, rather than in the registry's refresh, where the design puts it: it keeps the registry's adapter free of the runner, at the same default period. `missingImages` lists the image ids, sorted. Cost if wrong: an image lost between refreshes is found a minute later, or at its launch.
- **R8. The store's file** is `<store>/<64 hex digits>.tar`, without `sha256:` in the name. Cost if wrong: a rename.
- **R9. `deploy.Runner.Docker` is a `*Docker`**, nil when the file has no `runner.docker` block; the decoder learns pointers to structs, filling a new block with its defaults before it decodes into it; a `runner.docker` block with `kind: process` is refused; `group` defaults to -1, read as serve's own group. Cost if wrong: a sentinel in the deployment's types.
- **R10. `cpus` is a whole number of CPUs** [1], since the deployment's decoder reads whole numbers only. Cost if wrong: a deployment that wants half a CPU waits for the decoder to read decimals.
- **R11. `make lint` runs both lints; `BIN ?=`.** The test container passes `BIN=bin/linux/agentswarm` on make's command line, which overrides even `BIN :=`, so that it never overwrites the Mac's `bin/agentswarm` in the shared checkout; `?=` adds only that the environment can set `BIN` too. The in-container steps live in `build/test/run.sh`. Cost if wrong: none outside the Makefile.
- **R12. The test container sets `TESTCONTAINERS_RYUK_DISABLED=true`**, as this Mac's testcontainers settings do, and `GOTOOLCHAIN=local`; the labelled containers' own cleanup and `make test-clean` remain. Its caches are the named volumes `agentswarm-test-gomod` and `agentswarm-test-gocache`, which `make test-clean` does not remove. Cost if wrong: an untested Ryuk path left for later.
- **R13. `make test-image`** holds the reproducibility test (`test/image`, tag `image`): it builds without the cache twice, so it runs at Task 5, at the branch's end on both stores, and when the build changes, not in every gate. The static Docker CLI in the test image has no buildx, so every image build runs on the host before the test container starts. Cost if wrong: a build regression found at the branch's end.
- **R14. The Docker runner's integration tests carry `//go:build integration && linux`**: the Mac's `make test-integration` skips them, and the `GOOS=linux` lint reads them. Their throwaway images are made with `ImageImport` and its `Changes`, so that no test needs BuildKit. Cost if wrong: none.
- **R15. The probe's seam** is an unexported field of `docker.Options`, `probe []string`, which only the package's own tests set: it skips the image's contract and replaces the image's entrypoint with the probe's command. No other package can set it, and the package's production code never does. The package's tests are internal (`package docker`), since Task 6's test its unexported parts. Cost if wrong: none.
- **R16. `make test-docker` exists from Task 6** with the Docker runner's integration tests; its e2e run joins at Task 12, when the harness reads `AGENTSWARM_E2E_RUNNER`. From Task 5, `make test-linux` builds the worktree's runtime image, which the harness uses from Task 12; Task 5's gate runs `make test-linux`, since Task 5 changes it. Cost if wrong: none.
- **R17. The harness abstracts the runner** through `Env.Runner`, `Env.Tenant`, `DeploymentSpec.Stubborn`, `Instance.TurnsStarted`, `Gone` and `ContainerConfig` (Contract C9). It makes each instance's sockets directory when `Serve` starts it, under `/srv/agentswarm-e2e/<tenant>/`, and removes it at the test's end, and keeps one store per `Env`, shared by its instances. Cost if wrong: a harness change.
- **R18. Scenario 7's upper bound** is the round's time limit plus 500 ms from the round's opening under the process runner, and plus 2 s under Docker, whose kill, wait and inspect take longer. The measure crosses two clocks: `openedAt` is the database's time, and `turn exited` serve's wall clock, which on the Mac under the process runner lie on either side of OrbStack's VM; they agree well within 500 ms. Cost if wrong: a bound to retune.
- **R19. Serve's exits.** `server.run` returns its exit code, and `serve` calls `server.exit`, which runs `KillAll`, then closes the Docker runner when there is one (`Close`, through the server's `closer`, nil under the process runner), then logs `serve stopped`, once: every exit kills by construction, and before the runner's client closes. `server.close`, which the drain ends with, leaves the runner to `exit`. The process runner's `KillAll` is its `StopAll`, whose kills are synchronous. Cost if wrong: none.
- **R20. Serve's start order under Docker:** the holding, then the Docker runner (the engine, the store, the holder's lock, the images, the start sweep), before any listener binds. The holding is therefore made before `listen`, moved out of `wire`, which makes it today (`serve.go:511`). The runner's `Skills`, `Ended`, `Exited` and `Launched` are bound to parts made later, as the process runner's `Exited` is today. Cost if wrong: a refusal logged after the listeners bound.
- **R21. `launch` in `turn started` and `worstDrain` in `serve started` are Go duration strings** (`487ms`, `1m22s`), as serve's other durations are. Cost if wrong: a log reader parses them.
- **R22. The worst drain** is the greater of the leader's path, 3 × `mcp.recordTimeout` + `drain` + `renewEvery` (the MCP gateway's drain, the LLM gateway's with the sessions' close beside it, the release), and the standby's, `takeWait + 3 × renewEvery`; plus the close's bounds, 6 × 5 s (three servers' shutdown, the registry's refresh, two pools); plus `KillAll`'s 1 s and the runner's `Close`'s 1 s: 50 + 30 + 1 + 1 = 82 s with the defaults. The function does not depend on the runner's kind, so it is an upper bound under the process runner too. Cost if wrong: a stop timeout to retune.
- **R23. `turn exited` under Docker** carries `container`, `exit` and `oom` besides `turn` and `class`. Cost if wrong: none.
- **R24. A health check of `NONE`** in an image breaks no contract, since the runner sets `NONE` anyway. Cost if wrong: an image refused that need not be.
- **R25. A `Start` whose id is registered** starts nothing, and answers as the first `Start` now stands: `(true, nil)` until its `Launched` was reported, `(false, nil)` after. A turn runs from step 7, before its report may have reached the coordinator, so the turn keeps a flag, `Launched` reported, beside its state (Decision 5). Cost if wrong: none, since the coordinator never repeats a `Start` of a registered turn.
- **R26. The process runner reads a SIGKILL it did not send as `unavailable`** even when the kernel's OOM killer sent it, since a host process's memory kill cannot be told apart; the Docker runner tells them apart by `OOMKilled`. Cost if wrong: a host process's memory crash not counted, under the development runner.
- **R27. The scenarios that check a process is gone,** run by name under the process runner in `make test-linux`, are scenarios 7, 8, 9 and 16, the second-signal test, and the harness's three tests of its parent (Contract C15). Cost if wrong: a name to add.
- **R28. `test/compat` strips `AGENTSWARM_E2E_TENANT`, `AGENTSWARM_E2E_RUNNER`, `AGENTSWARM_E2E_IMAGE` and `AGENTSWARM_E2E_STUBBORN`** from the old tree's environment: it passes its own environment on, so leaving them unset takes removing them. Cost if wrong: none.
- **R29. Scenario 14 under Docker** holds every model answer until the test has read `docker inspect` of each turn's container, then answers as the converging script does. The scripted vendor cannot tell which container a request comes from, each round starts new containers, and a `turn started` line may reach the log after its turn's first request. So a background reader reads `ContainerConfig` of each container a `turn started` line names, once, and marks it read; the vendor holds a request until the containers read equal the `turn started` lines and serve's log has not grown for 200 ms. Cost if wrong: none outside the scenario.

## Contracts between tasks

Tasks that never see each other's code meet here. A task that produces one of these follows it exactly; a task that consumes one relies on nothing else. Plan 7d-1's C5 (`agentswarm inspect`) and C8 (a migration's reader) are unchanged.

### C1 (amended). The deployment file's runner (Task 10 reads it; Tasks 11, 12 and 14 write it)

Plan 7d-1's C1 holds, with these changes. `runner.kind` is `process` or `docker`. With `kind: process` everything is as before, and a `runner.docker` block is refused. With `kind: docker`:

```yaml
listeners:
  admin: 127.0.0.1:7403             # required, loopback only
  addresses: /var/lib/agentswarm/a/addresses.json  # required (C2)
  # mcp and llm are refused: the sandboxes reach serve through runner.docker.sockets
runner:
  kind: docker
  output: /var/lib/agentswarm/a/turns   # as before
  outputCap: 1048576                    # as before [1 MiB]
  maxTokens: 4096                       # as before [4,096]
  images:
    thinloop: sha256:<64 hexadecimal digits>
  docker:
    endpoint: unix:///var/run/docker.sock  # optional, this default; unix:// only
    sockets: /run/agentswarm/a             # required: serve listens on mcp.sock and llm.sock here
    store: /var/lib/agentswarm/images      # required: serve keeps a copy of each runtime image here
    group: 990                             # optional: the gid the sockets and the sandboxes share; default serve's own
    labels: {}                             # optional: more labels on each sandbox
    memory: 268435456                      # optional [256 MiB]
    cpus: 1                                # optional [1], whole CPUs
    pids: 64                               # optional [64]
    files: 256                             # optional [256]
    tmp: 16777216                          # optional [16 MiB]
```

`deploy.Check` refuses, each naming its key: with `kind: docker`, `listeners.mcp`, `listeners.llm`, `runner.binary` (not empty) and `runner.unsafeNoSandbox: true`; a missing `runner.docker.sockets` or `runner.docker.store`; an endpoint other than `unix://` and an absolute path; a sockets path that is not absolute, whose `mcp.sock` would pass 107 bytes, or that lies inside or holds `secrets`, `runner.output`, `runner.docker.store`, `listeners.addresses` or `configuration.repository`, by their cleaned paths; a store path that is not absolute; a `group` of 0 or below -1; the label keys `agentswarm.tenant`, `agentswarm.holder`, `agentswarm.turn`, `agentswarm.run`, `agentswarm.agent`, `agentswarm.round`, `agentswarm.deadline` and `agentswarm.data`, or an empty key; and `memory`, `cpus`, `pids`, `files` or `tmp` not above zero. Serve's start refuses the rest of Decision 8's list, each naming its key (`runner.kind`, `runner.docker.endpoint`, `runner.images.<runtime>`, `runner.docker.store`, `holder`, `runner.docker.group`, `runner.docker.sockets`).

### C2 (amended). The addresses file (Task 11 writes it; the harness reads it)

Under the Docker runner, `mcp` and `llm` hold `unix:<path>`; `admin` stays `host:port`:

```json
{"pid": 4242, "mcp": "unix:/run/agentswarm/a/mcp.sock", "llm": "unix:/run/agentswarm/a/llm.sock", "admin": "127.0.0.1:53003"}
```

**Amended by Plan 9a-1, 2026-11-08:** the file gains `web`, the web listener's `host:port`, as in Plan 7d-1's Contract C2.

### C3 (amended). Serve's log (Tasks 7, 11 and 13 write it; the harness reads it)

Plan 7d-1's table holds, with these rows changed or added:

| `msg` | Attributes | Logged by |
|---|---|---|
| `serve started` | `holder`, `schemaLevel`, `worstDrain` (a Go duration, such as `1m22s`) | serve (Tasks 11 and 13) |
| `turn started` | `turn`; `pid` under the process runner; `container` (the container's id) and `launch` (a Go duration, from `Start` to the container's start) under the Docker runner | the process runner, as before; the Docker runner (Task 7) |
| `turn exited` | `turn`, `class`; the process runner adds `pid` and `status`, the Docker runner `container`, `exit` and `oom` | the process runner, as before; the Docker runner (Task 7) |
| `turns run unsandboxed` | `runner` (`process`), at Warn | serve at start under the process runner (Task 11) |

The Docker runner also logs lines the harness does not read: `a turn's launch failed` (`turn`, `reason`, `err`) at Warn; `an image was reloaded from the store` (`image`) at Info; `an image is missing` (`image`, `err`) at Error; `the sweep removed a container` (`container`, `holder`) at Info; and `a container could not be removed` (`container`, `err`) at Warn. No line carries a session token.

### C4 (amended). `/v1/status` (Task 11 serves it; the harness reads it)

`GET /v1/status` gains `runner`:

```json
{"leading": true, "epoch": 3, "schema": "compatible", "draining": false, "skipped": 0,
 "runner": {"kind": "docker", "sandboxed": true, "engine": "ok", "missingImages": []}}
```

`engine` is `ok` or `unreachable`; `missingImages` lists, sorted, the image ids of `runner.images` the runner's last check found neither on the engine nor in the store, or breaking the contract. Under the process runner, `runner` is `{"kind": "process", "sandboxed": false}`: no `engine`, no `missingImages`.

### C6 (version 2). A turn's spec (`internal/runner/spec`: Task 2 makes it; the runners write it; the runtime reads it)

One JSON object on the runtime's standard input, at most 1 MiB, followed by nothing until the runner closes the input. Under the Docker runner:

```json
{
  "v": 2,
  "turn": "0193...", "run": "0193...", "agent": "alpha", "round": 2,
  "mcp": {"url": "https://mcp.agentswarm.internal/", "token": "aswm_...", "socket": "/run/agentswarm/mcp.sock"},
  "llm": {"url": "https://llm.agentswarm.internal", "token": "aswl_...", "socket": "/run/agentswarm/llm.sock"},
  "cert": "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n",
  "model": "claude-sonnet-5-5",
  "system": ["<skill text>", "..."],
  "prompt": "<the round's prompt>",
  "deadline": "2026-10-28T10:00:02.5+11:00",
  "maxTokens": 4096
}
```

- `v` absent, or 0, reads as 1. The runtime reads versions 1 and 2, and refuses any other.
- Under the process runner: `"v": 2`, `http://` URLs of the loopback listeners, and no `socket` or `cert`.
- `socket`, when present, is an absolute path, dialed for every connection to that gateway whatever its URL's host. `cert`, when present, holds a PEM certificate, the only one the runtime trusts for an `https` URL, with TLS 1.3 at least.
- Every other field is Plan 7d-1's C6.
- **The handshake:** the runtime image's label `io.agentswarm.turn-spec` lists the versions it reads, comma-separated (`^[1-9][0-9]*(,[1-9][0-9]*)*$`; this build's: `1,2`). A runner writes the newest version both it and the image know; the Docker runner writes 2 only, and fails the launch with the reason `image` when the image does not list 2.

### C7 (amended). `internal/outcome` (Task 2 changes it)

The exit codes stay. Their comment, and a new list of the signals, read:

```go
// The exit codes of agentswarm turn. Any other code is a crash, except an
// end by one of OutsideSignals that the turn's runner did not send, and not
// for the turn's memory: only the engine, an operator or the host sends
// those, so such a turn is unavailable (Plan 7d-2, Decision 9). A container
// whose process ended by a signal exits with 128 and the signal's number.
const ( /* ExitEnded … ExitUnavailable, as before */ )

// OutsideSignals end a turn the runner did not end: SIGHUP, SIGINT, SIGKILL
// and SIGTERM.
var OutsideSignals = []syscall.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGKILL, syscall.SIGTERM}
```

**How a runner reads a turn's end,** first rule first:
- **The process runner:** the board holds the agent's turn end: `ended`; the process ended at or after its deadline, or by the runner's own SIGKILL: `interrupted`; its spec was not delivered: `unavailable`; its exit code is in the table: that class; it died by one of `OutsideSignals` the runner did not send: `unavailable` (R26); anything else: `crashed`.
- **The Docker runner:** the board's turn end: `ended`; `FinishedAt` at or after the deadline, or the runner's own kill: `interrupted`; the spec not delivered: `unavailable`; `OOMKilled`: `crashed`; an exit code in the table: that class; an exit code of 128 plus one of `OutsideSignals` (129, 130, 137, 143): `unavailable`; anything else: `crashed`. A failed wait whose container is gone, or whose engine does not answer past the deadline and the slack: `unavailable`.

### C9 (amended). The harness's API (Task 12 adds to it)

Plan 7d-1's C9 holds. Task 12 adds:

```go
// The environment the harness reads (Contract C15).
const (
	RunnerEnv   = "AGENTSWARM_E2E_RUNNER"   // "docker", or "process", the default
	ImageEnv    = "AGENTSWARM_E2E_IMAGE"    // the runtime image's id under Docker (make image-dev's)
	StubbornEnv = "AGENTSWARM_E2E_STUBBORN" // the stubborn test image's id
	TenantEnv   = "AGENTSWARM_E2E_TENANT"   // the tenant; "harness" by default
)

// Options gains Runner string: "process" or "docker"; "" means RunnerEnv's.
func (e *Env) Runner() string
// Tenant is the tenant of every deployment the harness writes, and of
// AuditVerify. A TenantEnv that is not ^[a-z0-9][a-z0-9-]{0,62}$ fails New.
func (e *Env) Tenant() string

// DeploymentSpec gains Stubborn bool: the turns ignore their input and
// SIGTERM, and sleep for 120 s at most (R5): the process runner runs a script
// of the harness's as runner.binary, the Docker runner the image StubbornEnv
// names. Binary stays the process runner's alone: a Docker deployment that
// sets it fails the test.

// TurnStarted is a turn serve logged as started (C3).
type TurnStarted struct {
	Turn      string
	PID       int    // the process runner's
	Container string // the Docker runner's
}

func (i *Instance) TurnsStarted() []TurnStarted
// SocketsDir is the instance's runner.docker.sockets under Docker; "" otherwise.
func (i *Instance) SocketsDir() string
// Gone reports whether a started turn is over: its process gone, or its
// container not running or removed (docker inspect).
func Gone(t *testing.T, s TurnStarted) bool
// ContainerConfig returns docker inspect's output of a container, or "" once
// it is removed.
func ContainerConfig(t *testing.T, id string) string

// InstanceStatus gains Runner RunnerStatus `json:"runner"` (C4).
type RunnerStatus struct {
	Kind          string   `json:"kind"`
	Sandboxed     bool     `json:"sandboxed"`
	Engine        string   `json:"engine"`
	MissingImages []string `json:"missingImages"`
}
```

Under `Runner() == "docker"`, `Deployment` writes C1's Docker form: `runner.docker.sockets` at `/srv/agentswarm-e2e/<tenant>/<instance>-<8 hex digits>` (which `Serve` makes, mode 0750, and the test's end removes), `runner.docker.store` at `<Env's directory>/images`, `runner.docker.labels` `{agentswarm.project: agent-swarm}`, `runner.images.thinloop` `ImageEnv`'s id (or `StubbornEnv`'s with `Stubborn`), and no `listeners.mcp`, `listeners.llm`, `runner.binary` or `runner.unsafeNoSandbox`. `Deployment` fails the test when the id it needs is unset. On a system other than Linux, `Serve` under Docker skips the test: "needs Linux: make test-linux".

### C10. The `Turns` port and `Coordinator.Launched` (Task 4 makes them; Tasks 7 and 11 use them)

```go
// package app (internal/work/app)

// Turns runs agents' turns: the runner, as host processes for development
// and tests (Plan 7d-1, Decision 12), or in Docker sandboxes (Plan 7d-2,
// Decision 5). The coordinator calls Turns under its lock. A runner reports a
// turn's end once, with its ID, through Coordinator.Exited, and how a launch
// it left pending ended once, through Coordinator.Launched. Both take the
// coordinator's lock, so a runner calls them holding none of its own locks:
// the coordinator calls Stop under its lock, and the reverse order would
// deadlock it, and the leader, which fences under its own. No method calls
// them before it returns, or waits for a turn to end, or for an engine. A
// runner forgets a turn once it has reported the turn's end: the coordinator
// never starts again a turn whose end it learned.
type Turns interface {
	// Start starts a turn, or registers it for a launch after Start returns.
	// It fails when it cannot: Holding.Valid refused the epoch ctx carries
	// (an error wrapping ErrUnheld), or the turn's output file, process or
	// sandbox could not be made. A turn whose Start failed is tried again
	// with the same ID while its round lasts, and counts as unstarted.
	// pending says the runner launches the turn and reports the launch
	// through Launched; a turn not pending runs. A Start repeated with the
	// ID of a turn the runner has starts nothing. A turn whose spec cannot
	// be delivered once it runs ends unavailable. Neither ends crashed.
	Start(ctx context.Context, t *Turn) (pending bool, err error)
	// Stop ends a turn at once: its launch is cancelled, its process or
	// container killed. It never waits.
	Stop(run board.RunID, agent string)
	// StopAll ends every turn the instance runs, as Stop does.
	StopAll()
}

// ErrUnheld marks a turn's start or launch that the holding refused: the
// holding it acts for is over. It is a launch that failed, posts no notice,
// and never fences.
var ErrUnheld = errors.New("app: the holding refused the turn's start")

// LaunchReason says why a launch failed (Decision 7).
type LaunchReason string

const (
	ReasonEngine    LaunchReason = "engine"    // the engine did not answer, or the launch outlasted its bound
	ReasonImage     LaunchReason = "image"     // the image is missing and not in the store, breaks the contract, or reads no spec the runner writes
	ReasonLaunch    LaunchReason = "launch"    // any other failure
	ReasonHolding   LaunchReason = "holding"   // the holding refused: no notice
	ReasonCancelled LaunchReason = "cancelled" // a stop, a fence or a drain: no notice
)

// Launch is how a launch that Start left pending ended. Err is nil when the
// turn runs: its end then comes through Exited, never before Launched.
// Otherwise the launch failed, as a failed Start does, for Reason.
type Launch struct {
	Run    board.RunID
	Agent  string
	Round  int
	Turn   string
	Reason LaunchReason
	Err    error
}

// Launched reports how a pending launch ended. It takes the coordinator's
// lock. One the coordinator no longer waits for (another turn's ID, a round
// that ended, a run that stopped, a fence) changes nothing, but for one case:
// a failed launch of a turn that was launching when its round's deadline
// ended the round, while the run is still on that round in the same holding,
// posts its notice and arms no retry.
func (c *Coordinator) Launched(ctx context.Context, l Launch)
```

### C11. The board's notice of a turn that could not start (Task 3 makes it; Task 4 posts it)

```go
// package board
// NoticeTurnUnstarted reports an agent's turn that could not start.
NoticeTurnUnstarted NoticeCode = "turn_unstarted"

// Notice gains:
// Reason says why a turn_unstarted notice's turn could not start: engine,
// image or launch. It is not part of a repeated notice's key.
Reason string `json:"reason,omitempty"`
```

- **Posting** (`State.Notify`, through `prepare`) is strict: `turn_unstarted` names its agent and a reason of `engine`, `image` or `launch`, and nothing else; no other code carries a reason; an unknown code is refused (`ErrInvalid`), as today.
- **Replay** (`State.Apply`, which `Replay` calls) accepts, besides what posting accepts: a notice of a code it does not know whose author is the platform and whose code, and reason if any, match `^[a-z][a-z_]{0,31}$`, its other fields ignored; and a known code whose reason this build does not know, if well formed. Such a notice feeds no decision: `Report` counts model-call notices alone, as today.
- A body of a later version still fails with `ErrVersion` before any of this (scenario 11(a)).

### C12. Runtime images and the store (Task 5 builds; Task 6 checks, saves and reloads)

`build/runtime/Dockerfile`, whole:

```dockerfile
FROM scratch
ARG VERSION
ARG GO
COPY --chmod=0755 agentswarm /agentswarm
USER 65532:65532
ENTRYPOINT ["/agentswarm", "turn"]
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VERSION}" \
      org.opencontainers.image.source="https://github.com/jchen0426/agent-swarm" \
      io.agentswarm.turn-spec="1,2" \
      io.agentswarm.go="${GO}"
```

`build/runtime/image.sh commit COMMIT` (`make image`, `IMAGE_COMMIT ?= HEAD`), in order:
1. `commit` is `git rev-parse --verify "$COMMIT^{commit}"`; `epoch` its `git log -1 --format=%ct`; `stamp` and `date` its committer time in UTC, `TZ=UTC git log -1 --date=format-local:%Y%m%d%H%M.%S --format=%cd` and `--date=format-local:%Y-%m-%dT%H:%M:%SZ`; `arch` is `$ARCH`, or `docker version --format '{{.Server.Arch}}'`.
2. `git archive "$commit" | tar -x -C "$work/src"`, `$work` a `mktemp -d` the script removes on exit.
3. In `$work/src`: `GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -buildvcs=false -ldflags "-s -w -X <module>/internal/versioninfo.version=$commit -X <module>/internal/versioninfo.commit=$commit -X <module>/internal/versioninfo.date=$date" -o "$work/context/agentswarm" ./cmd/agentswarm`.
4. `TZ=UTC touch -t "$stamp" "$work/context/agentswarm"`.
5. `SOURCE_DATE_EPOCH=$epoch docker buildx build --no-cache --provenance=false --sbom=false --build-arg VERSION=$commit --build-arg GO=go1.26.0 --output type=image,name=agentswarm-runtime:$commit,rewrite-timestamp=true -f build/runtime/Dockerfile "$work/context"`.
6. The id is the engine's own, `docker image inspect --format '{{.Id}}' agentswarm-runtime:$commit`, which `ImageInspect` and `ContainerCreate` use on either store; `docker tag <id> agentswarm-runtime:<id's 64 digits>`; then print `binary sha256:<the binary's SHA-256>` (`sha256sum`, or `shasum -a 256` where it is missing) and `image sha256:<64 digits>`.

`build/runtime/image.sh dev BINARY TAG` (`make image-dev`, after `make linux-binary`, with `TAG` the worktree's `$(DEV_IMAGE)`, C15) notes the id `TAG` names, if any; copies `BINARY` into the context; builds with `docker buildx build --provenance=false --sbom=false --build-arg VERSION=dev --build-arg GO=$(go env GOVERSION) --output type=image,name=$TAG -f build/runtime/Dockerfile "$work/context"`: the cache allowed, and neither `SOURCE_DATE_EPOCH` nor `rewrite-timestamp`, since a worktree's build promises no reproducibility; prints `image sha256:<64 digits>`, read as step 6 reads it; and, when the noted id differs, saves that image's `docker image inspect` under `.test-clean/` as `<its 64 hex digits>-<UTC time>.inspect.json`, no colon in the name and no file replaced, and removes it with `docker image rm` (without `--force`), so that each gate does not leave the last one's dangling. The tag is the worktree's own, so `image-dev` replaces only its own worktree's image, never one another worktree's gate runs on (plan review 2, F4). A removal the engine refuses, as when a container still uses the image, is printed and does not fail the target.

**The store:** `<store>/<64 hex digits>.tar` is `docker save` of the image by id, written to `<store>/.<64 hex digits>.tar.<random>` and renamed. **The holder's lock:** `<store>/holders/<hex of the tenant>.<hex of the holder>.lock`, the hex the lowercase hexadecimal of the name's bytes, held with `flock(LOCK_EX|LOCK_NB)` while serve runs. **Tags:** `agentswarm-runtime:<64 hex digits>` (`make image`, and every reload), `agentswarm-runtime:<commit id>` (the build's own name, which `make image` leaves), and `agentswarm-runtime:dev-<8 hex digits>` (`make image-dev`, one per worktree); a tag changes no id. No image the build makes carries a label that starts with `agentswarm.`.

**The contract,** checked at serve's start, every minute and at each launch:

| Field (`ImageInspect`) | Must be |
|---|---|
| `Os`, `Architecture` | `linux`, and the engine's (`ServerVersion`'s `Arch`) |
| label `io.agentswarm.turn-spec` | present, of the form above, listing a version the runner writes |
| `Config.Volumes`, `Config.OnBuild` | empty |
| `Config.Healthcheck` | absent, or `NONE` (R24) |
| `Config.Env` | `PATH=…` entries alone |
| `Config.Labels` | none that starts with `agentswarm.` |

### C13. The sandbox's settings (Task 6 builds them; Tasks 7 and 9 rely on them)

| Container configuration | Value |
|---|---|
| name | `agentswarm-turn-<turn id>` |
| `Config.Image` | the turn's `Image`, `sha256:<64 hex digits>`; never pulled |
| `Config.Entrypoint`, `Config.Cmd` | not set: the image's entrypoint runs |
| `Config.User` | `65532:65532` |
| `HostConfig.GroupAdd` | `[<runner.docker.group>]` |
| `Config.WorkingDir` | `/tmp` |
| `Config.Env` | not set |
| `Config.OpenStdin`, `StdinOnce`, `AttachStdin`, `AttachStdout`, `AttachStderr` | true; `Tty` false |
| `Config.Healthcheck` | `{Test: ["NONE"]}` |
| `Config.StopSignal`, `Config.StopTimeout` | `SIGTERM`, 10 |
| `Config.Labels` | `agentswarm.tenant`, `agentswarm.holder`, `agentswarm.turn`, `agentswarm.run`, `agentswarm.agent`, `agentswarm.round`, `agentswarm.deadline` (the turn's deadline, RFC 3339 with nanoseconds, UTC), and `runner.docker.labels` |
| `HostConfig.NetworkMode` | `none` |
| `HostConfig.DNS`, `HostConfig.DNSSearch` | `[127.0.0.1]`, `["."]` |
| `HostConfig.IpcMode` | `none` |
| `HostConfig.ReadonlyRootfs` | true |
| `HostConfig.CapDrop`, `HostConfig.SecurityOpt` | `["ALL"]`, `["no-new-privileges"]` |
| `HostConfig.Init` | true |
| `HostConfig.Resources` | `Memory` and `MemorySwap` = `memory`; `NanoCPUs` = `cpus` × 10⁹; `PidsLimit` = `pids`; `Ulimits` = `[{Name: "nofile", Soft: files, Hard: files}]` |
| `HostConfig.OomScoreAdj` | 500 |
| `HostConfig.Tmpfs` | `{"/tmp": "rw,noexec,nosuid,nodev,size=<tmp>"}` |
| `HostConfig.Mounts` | one: `{Type: bind, Source: <runner.docker.sockets>, Target: /run/agentswarm, ReadOnly: true}` |
| `HostConfig.LogConfig` | `{Type: "none"}` |
| anything else (`Privileged`, `Binds`, `PortBindings`, `Devices`, `VolumesFrom`, `AutoRemove`, `RestartPolicy`) | not set |

### C14. The Docker runner's API (Tasks 6, 7 and 8 make it; Tasks 9 and 11 use it)

```go
// package docker (internal/runner/docker)

// Where a sandbox reaches the gateways (Contract C6), fixed in code.
const (
	SocketsDir = "/run/agentswarm"
	MCPSocket  = "mcp.sock"
	LLMSocket  = "llm.sock"
	MCPURL     = "https://mcp.agentswarm.internal/"
	LLMURL     = "https://llm.agentswarm.internal"
)

// Engine is what the runner asks of the Docker Engine: a port of its own, so
// that its unit tests run against a fake. Every call returns once its ctx
// ends, whether or not the engine has answered, with an error wrapping both
// ErrUnreachable and ctx's error; the runner tells a stop's cancel from the
// engine's silence by why ctx ended (Decision 5), and a cancel leaves the
// engine's recorded state as it was (Status).
type Engine interface {
	Version(ctx context.Context) (Info, error)
	Image(ctx context.Context, ref string) (Image, error) // ErrNoImage when missing
	Save(ctx context.Context, id string, w io.Writer) error
	Load(ctx context.Context, r io.Reader) error
	Tag(ctx context.Context, id, ref string) error
	Create(ctx context.Context, name string, cfg *container.Config, host *container.HostConfig) (string, error)
	Attach(ctx context.Context, id string) (Attachment, error) // honours ctx, which the moby client's hijack does not (Task 6)
	Start(ctx context.Context, id string) error
	Wait(ctx context.Context, id string) error // returns once the container is not running
	Inspect(ctx context.Context, idOrName string) (Container, error) // ErrNoContainer when gone
	Kill(ctx context.Context, idOrName string) error   // ErrNoContainer when gone
	Remove(ctx context.Context, idOrName string) error // forced, with its volumes; ErrNoContainer when gone
	List(ctx context.Context, label string) ([]Listed, error) // every state, by label key=value
	Close() error
}

var (
	ErrNoImage     = errors.New("docker: no such image")
	ErrNoContainer = errors.New("docker: no such container")
	// ErrUnreachable wraps a call the engine did not answer: no connection,
	// or one that broke, or a context that ended first.
	ErrUnreachable = errors.New("docker: the engine does not answer")
)

type Info struct{ OS, Arch string }
type Image struct {
	ID                        string // sha256:<64 hex digits>
	OS, Arch                  string
	Labels                    map[string]string
	Env, Volumes, OnBuild     []string
	Healthcheck               []string // its Test; nil when the image has none
}
// Attachment is a container's standard input, open for the turn's life, and
// its standard output and error, demultiplexed into one stream (stdcopy).
type Attachment interface {
	io.WriteCloser // closing it ends the attachment, and so the container's input
	Output() io.Reader
}
type Container struct {
	ID, Name          string
	Labels            map[string]string
	Running           bool
	Created, Finished time.Time // Finished is zero for a container that never ran
	ExitCode          int
	OOMKilled         bool
}
type Listed struct {
	ID     string
	Labels map[string]string
}

// Dial returns the moby client's Engine at a unix:// endpoint, negotiating
// the API version.
func Dial(endpoint string) (Engine, error)

type Options struct {
	Tenant, Holder string
	Output         string        // runner.output
	OutputCap      int64         // [1 MiB]
	Slack          time.Duration // [2 s]
	MaxTokens      int64         // [4,096]
	Images         map[string]string // runner.images: checked at Open and every minute
	Sockets        string        // runner.docker.sockets, as the engine's host sees it
	Store          string        // runner.docker.store
	Group          int           // resolved: never -1
	Labels         map[string]string
	Memory, CPUs, Pids, Files, Tmp int64
	Cert           string        // the gateways' certificate, PEM, which every spec carries

	probe []string // the tests' seam (R15): skips the contract, replaces the entrypoint
}

// check refuses options the runner cannot run turns with: an empty tenant,
// holder, output, sockets or store; a cap, maxTokens or limit below 1; a
// negative slack; a group below 1; a cert that holds no PEM certificate.
func (o *Options) check() error

type Ports struct {
	Engine   Engine
	Holding  holding.Holding
	Skills   func(ctx context.Context, commit, agent string) ([]string, error)
	Ended    func(ctx context.Context, run board.RunID, round int, agent string) (bool, error)
	Exited   func(app.Exit)
	Launched func(app.Launch)
	Log      *slog.Logger
}

// openWait bounds each engine call Open makes: the ping, each image's
// inspect, save, load and tag, and the start sweep's list, inspects, kills
// and removals.
const openWait = 30 * time.Second

// Open checks what the runner runs on and returns it, in this order: the
// engine answers; the store; the holder's lock, which lives in the store;
// every image of Options.Images (on the engine or in the store, meeting the
// contract, saved to the store); then the start sweep and the minute's job
// (Task 8). Each refusal names its deployment key. An engine call that runs
// past openWait, or finds the engine unreachable, refuses the start naming
// runner.docker.endpoint.
func Open(ctx context.Context, p Ports, o Options) (*Runner, error)

func (r *Runner) Start(ctx context.Context, t *app.Turn) (bool, error)
func (r *Runner) Stop(run board.RunID, agent string)
func (r *Runner) StopAll()
// KillAll cancels every launch and force-removes every registered turn's
// container, by name, in parallel, waiting up to 1 s for the engine.
func (r *Runner) KillAll(ctx context.Context)
// Close stops the minute's job, waiting at most 1 s, fixed in code, for its
// pass in flight (Task 8); then releases the holder's lock and closes the
// engine's client (Task 7). It kills nothing: serve calls it after KillAll,
// never before, since KillAll's removals need the client.
func (r *Runner) Close() error
func (r *Runner) Status() Status

type Status struct {
	// Engine is "ok" or "unreachable", as the last engine call found it. A
	// call the engine answered marks it ok. Only these mark it unreachable:
	// no connection, a broken connection, or a call that ran out its own
	// bound (a launch's 10 s, openWait, KillAll's 1 s). A call ended by its
	// context's cancel (a stop, a fence, a drain) leaves it as it was (plan
	// review 2, F3).
	Engine        string
	MissingImages []string // sorted image ids
}

// Certificate is the gateways' certificate for the sandboxes (Decision 2).
type Certificate struct {
	TLS tls.Certificate
	PEM string
}

// NewCertificate makes a self-signed ECDSA P-256 certificate for
// mcp.agentswarm.internal and llm.agentswarm.internal, valid ten years from now.
func NewCertificate(now time.Time) (Certificate, error)
```

### C15. The Linux test environment (Task 1 makes it; Tasks 5, 6, 11 and 12 extend it)

**Makefile.** `BIN ?= bin/agentswarm`; `LINUX_BIN := bin/linux/agentswarm`; `TEST_IMAGE := agentswarm-test:dev`; `E2E_DIR := /srv/agentswarm-e2e`; from Task 5, `DEV_IMAGE := agentswarm-runtime:dev-<the first 8 hex digits of the SHA-256 of $(CURDIR)'s bytes>` (`printf %s "$(CURDIR)"`, through `sha256sum`, or `shasum -a 256` where it is missing). The commands below are the targets' shell, not their Make syntax.
- `make lint`: `golangci-lint run ./...`, then `GOOS=linux golangci-lint run ./...`.
- `make linux-binary`: `CGO_ENABLED=0 GOOS=linux GOARCH=<the engine's architecture, docker version --format '{{.Server.Arch}}'> go build -trimpath -ldflags '$(LDFLAGS)' -o $(LINUX_BIN) ./cmd/agentswarm`.
- `make linux-env`: `docker build -t $(TEST_IMAGE) build/test`. Images carry no `agentswarm.` label: the contract refuses one on a runtime image, and the project's label is the containers'.
- `make image`, `make image-dev` (Task 5): C12. `make test-image` (Task 5): `go test -count=1 -timeout 30m -tags image ./test/image/`.
- `make test-linux` and `make test-docker`, after `linux-env`, `linux-binary` and, from Task 5, `image-dev`, and, from Task 12, the stubborn image (`docker build --provenance=false --sbom=false -t agentswarm-stubborn:dev test/e2e/testdata/stubborn`, its id read with `docker image inspect --format '{{.Id}}'`):
  1. `docker run --rm --label agentswarm.project=agent-swarm --user 0:0 -v /srv:/srv $(TEST_IMAGE) sh -c 'mkdir -p /srv/agentswarm-e2e && chown <uid>:<gid> /srv/agentswarm-e2e'`, with the developer's `id -u` and `id -g`;
  2. the engine socket's group: `docker run --rm --label agentswarm.project=agent-swarm -v /var/run/docker.sock:/var/run/docker.sock $(TEST_IMAGE) stat -c %g /var/run/docker.sock`;
  3. `docker run --rm --init --label agentswarm.project=agent-swarm --user <uid>:<gid> --group-add <socket's group> -v /var/run/docker.sock:/var/run/docker.sock -v $(CURDIR):$(CURDIR) -w $(CURDIR) -v $(E2E_DIR):$(E2E_DIR) -v agentswarm-test-gomod:/go/pkg/mod -v agentswarm-test-gocache:/cache/go-build -e TESTCONTAINERS_RYUK_DISABLED=true -e AGENTSWARM_E2E_IMAGE=<id> -e AGENTSWARM_E2E_STUBBORN=<id> $(TEST_IMAGE) build/test/run.sh linux` (or `docker` for `make test-docker`); the image variables are passed once their tasks exist.

**`build/test/run.sh MODE`**, run in the container: it refuses to run as root ("build/test/run.sh: refuses to run as root, where the permission tests skip") and refuses a `/srv/agentswarm-e2e` it cannot write; it makes a new tenant for each `go test` invocation, `e2e-<UTC yyyymmddHHMMSS>-<8 hex digits from /dev/urandom>`; and it exports an invocation id of its own, `AGENTSWARM_IT_RUN`, `run-<UTC yyyymmddHHMMSS>-<8 hex digits from /dev/urandom>`, which the Docker runner's integration tests put on their containers as the label `agentswarm-it.run` (below). At its end, whether its steps passed or not, it lists every container of each tenant it made, and every container labelled `agentswarm-it.run=<its invocation id>`, running or not (`docker ps -a --filter label=agentswarm.tenant=<tenant>`, and `--filter label=agentswarm-it.run=<id>`), saves each one's `docker inspect` under `.test-clean/` as `make test-clean` does (`<id>-<UTC time>.inspect.json`, never replacing a file), and removes it with `docker rm -f -v`; one whose backup fails stays, and the script says so. Its exit code stays its steps'. `make test-clean` lists running containers alone, so without this a killed run's or a failed test's stopped sandboxes, and those of an invocation's last tests, would stay on the engine (plan review 1, M8; plan review 2, F5).
- `linux`: `make test test-integration`; then `AGENTSWARM_E2E_RUNNER=docker AGENTSWARM_E2E_TENANT=<new> make test-e2e BIN=bin/linux/agentswarm`; then, under the process runner, `AGENTSWARM_E2E_TENANT=<new> AGENTSWARM_BIN=$PWD/bin/linux/agentswarm go test -count=1 -timeout 60m -tags e2e -run '^(TestATurnThatNeverEndsIsKilledAtItsDeadline|TestAStandbyTakesOverFromAKilledLeader|TestAPartitionedLeaderSendsNothingAfterTheStandbysTake|TestKillingTheHarnessParentLeavesNothingRunning|TestASecondSignalKillsEveryTurnAndExitsAtOnce|TestKillingTheParentClosesItsChildsStandardInput|TestClosingTheParentsOwnInputEndsItAndItsChild|TestAParentKillsAChildThatDoesNotEndWhenItsInputCloses)$' ./test/e2e/` (R27). Until Task 12, the mode runs `make test test-integration` and `make test-e2e BIN=bin/linux/agentswarm`, the process runner's whole suite.
- `docker`: `go test -race -count=1 -tags integration ./internal/runner/docker/...`; from Task 11, `go test -race -count=1 -tags integration -run Docker ./cmd/agentswarm/`; from Task 12, then `AGENTSWARM_E2E_RUNNER=docker AGENTSWARM_E2E_TENANT=<new> make test-e2e BIN=bin/linux/agentswarm`.

Every integration test that runs a container of the sandbox's settings makes its sockets directory under `/srv/agentswarm-e2e/<a random name>`, which these targets prepare, mode 0750, and removes it when it ends; its containers carry `agentswarm.project=agent-swarm` through `Options.Labels`. Each `internal/runner/docker` integration test runs its runner under a random tenant of its own, `it-<8 hex digits>`, so that no two of them, nor two gates on one engine, sweep each other's containers, and removes its tenant's containers when it ends. When `AGENTSWARM_IT_RUN` is set, every container such a test makes also carries the label `agentswarm-it.run=<that id>`, through `Options.Labels` for the runner's: a key outside `agentswarm.`, which the image contract and the deployment's label rules leave alone. So `run.sh` removes what `t.Cleanup` never reached, after a killed run or a `go test` timeout (plan review 2, F5).

**`build/test/Dockerfile`:** `FROM golang:1.26@sha256:<digest>`; the Docker CLI from `docker-29.4.0.tgz` for `x86_64` or `aarch64` by `TARGETARCH`, its SHA-256 checked, `docker` alone installed in `/usr/local/bin`; `jq` from Debian's packages (`apt-get install --no-install-recommends jq`), which the live gate's steps need (Task 16); `mkdir -m 0777 /go/pkg/mod /cache/go-build /home/test`; `ENV GOCACHE=/cache/go-build HOME=/home/test GOTOOLCHAIN=local`.

## Plan 7d-1's tests this plan changes

Each is changed in the task named, and the change is in that task's report:
- **Task 2:** `TestTheRuntimesTransportIgnoresProxies` becomes `TestTheRuntimesTransportsIgnoreProxies` (one transport per gateway, R2); `TestASpecPrintsNoToken` and the helper `validSpec` take `spec.Spec`; `TestTheSpecReachesTheTurnOnItsStandardInput` expects `"v": 2`; `TestExitCodesGiveTheClassBeforeTheDeadline` reads a SIGKILL, SIGTERM, SIGINT or SIGHUP the runner did not send as `unavailable` and keeps SIGSEGV and SIGABRT `crashed`, its fake turn's `Signal` now naming the signal it ends by.
- **Task 4:** the 25 `Runner.Start` calls of `internal/runner/process/process_test.go` take two results (its 26th `.Start(` is an `exec.Cmd`'s); `TestARepeatedStartStartsNothing` drops its `Start` after the turn's end, since the runner forgets a reported turn; the coordinator's fakes `turns.Start` (`harness_test.go`) and `stalling.Start` (`leader_test.go`) take the new signature.
- **Task 10:** `TestAProcessRunnerNeedsUnsafeNoSandbox` and `TestTheAgentsListenersAreOnLoopbackWithTheProcessRunner` (`deploy_test.go:448-451`): each `kind: docker` case expected `runner.kind` alone, which Task 10 accepts. Their document is `minimal()`'s, which sets both listeners and `unsafeNoSandbox: true`, so under `kind: docker` it draws five refusals: those three keys, `runner.docker.sockets` and `runner.docker.store`. Each case now asserts with `Contains` that the key under test is named (`runner.unsafeNoSandbox`; `listeners.mcp` or `listeners.llm`), now as a key the Docker runner refuses, and that `runner.kind` is not, rather than the exact list.
- **Task 11:** `TestServesHTTPServersBoundTheirReads` also asserts that the agents' two servers allow HTTP/1 alone; the admin API's test fixture (`admin_test.go`) gives `admin.New` a runner port.
- **Task 12:** `TestTheHarnessDeploymentNamesEverySecretItUses`, `TestServeWritesItsAddressesAndBecomesReady`, `TestATurnThatNeverEndsIsKilledAtItsDeadline`, `TestAStandbyTakesOverFromAKilledLeader`, `TestAPartitionedLeaderSendsNothingAfterTheStandbysTake`, `TestNoSessionTokenLeavesThePlatform`, `TestKillingTheHarnessParentLeavesNothingRunning`, `TestASecondSignalKillsEveryTurnAndExitsAtOnce` and `TestInspectPrintsALaterVersionRaw`; the helpers `laterRecord` and `laterEvent` (scenario 11(a)) and `lastRenewal` (scenario 10), which name the tenant; and `turnPIDs`, `gone`, `waitForTurns`, `allGone` and `stubborn`, which give way to the harness's `TurnsStarted`, `Gone` and `DeploymentSpec.Stubborn`.
- **Task 13:** `TestTheUpstreamSessionsCloseBesideTheLLMGatewaysDrain`: the drainer's `ttl` becomes `busy`.

## Not in this plan

- Kubernetes, several hosts, and a sandbox runtime other than `runc`.
- Pulling images from a registry.
- Skills' scripts: the runtime has no tool that runs them.
- Serve on a Linux host under systemd, the examples' topology, as a tested one: every test here runs serve in the test container (Decision 13), and the first real deployment checks the examples (carried).
- The narrow launcher (Decision 10), gVisor, warm sandboxes: carried.
- **Plan 7b:** the queue, the kill switch, close, reopen, auto-close, budget extensions and links. **Plan 7c:** inbound events and trigger mappings. **Plan 8:** arms, the measurement window, finality and shadow runs. **Plan 9:** sign-in for the admin API, and the interface.

## Constraints carried to later plans

- **Before a team's measurement window** (spec Section 14.3; Plan 10): the narrow launcher, a small process that holds the engine's access and offers only "start, kill, list and remove a sandbox of the fixed profile"; or rootless Docker, weighed then (its subgid mapping breaks the sockets' group as granted today, review 1, I5); and gVisor (`runsc`) with it, which would also hide the host's kernel version from a sandbox.
- **If launches ever delay rounds:** warm sandboxes, a started container per image waiting on its standard input, since the runtime does nothing until its spec arrives.
- **The first real deployment** checks the examples' systemd topology on a Linux host: serve under `agentswarm@.service`, the sockets in its runtime directory, the shared clone's fetcher and its timers. Every test here runs serve in the test container, on the bridge network (plan review 1, M11). **SELinux-enforcing hosts** are untested (a container's domain may be refused the sockets); **the engine's live-restore** is untested (the runner's re-wait handles it, but the output stream is lost).
- **The containerd image store** is checked by hand at this branch's end; once CI exists, `make test-image` and `make test-docker` run there, on both stores.
- **Later plans that touch serve or a runner** keep `make test-docker` in their gates.
- **The first release:** an older build's `agentswarm conclude` refuses a board whose notice has `reason` (an offline command; documented); a later notice code that names a call, or feeds a round's facts, takes a new board version (Decision 7); `make test-compat` strips the harness's runner variables (R28).
- **Plan 7b:** the kill switch's stops go through `Turns.Stop` and `StopAll`, which never wait under Docker; a launch they cancel reports `cancelled` and posts no notice, so the kill switch's own notice is 7b's.
- **Plan 10 (the load trial):** 8 launches at once, the 10 s launch bound, `KillAll`'s 1 s, `Close`'s 1 s and `Open`'s 30 s a call, all fixed in code; the host's memory against [256 MiB] times the turns at once, which serve does not check at start (review 1, M7); the store's growth, which only an operator trims.
- **A deployment's operators:** changing an engine's image store invalidates every lock; switching a deployment from `process` to `docker` fails its active items' turns until they stop; the store is backed up, and a locked image comes back from it, never from a rebuild.

## File Structure

| File | Task | Responsibility |
|---|---|---|
| `build/test/Dockerfile`, `build/test/run.sh` | 1, 6, 12 | The Linux test image, and the steps it runs |
| `Makefile` | 1, 5, 6, 12 | `lint` twice; `BIN ?=`; `linux-binary`, `linux-env`, `test-linux`, `test-docker`; `image`, `image-dev`, `test-image` |
| `internal/runner/spec/spec.go` | 2 | Contract C6: the one spec type, its reading, its versions and the handshake |
| `cmd/agentswarm/turn.go` | 2 | The runtime: the shared spec, a transport per gateway, SIGTERM, its help |
| `internal/outcome/outcome.go` | 2 | Contract C7's comment and `OutsideSignals` |
| `internal/runner/process/process.go` | 2, 4, 11 | The shared spec; outside signals; `Start`'s two results; forgetting reported turns; `ErrUnheld`; `KillAll` |
| `test/e2e/conformance_test.go` | 2 | The runtime's SIGTERM |
| `internal/work/board/board.go`, `state.go`, `post.go` | 3 | `turn_unstarted`, `reason`, strict posting and lenient replay |
| `internal/work/app/ports.go`, `coordinator.go` | 4 | Contract C10; launching turns; `Launched`; the notice |
| `build/runtime/Dockerfile`, `build/runtime/image.sh`, `test/image/image_test.go` | 5 | The runtime image's build and its reproducibility test |
| `.golangci.yml` | 5 | The `image` build tag |
| `internal/runner/docker/engine.go`, `moby.go` | 6 | The engine's port and the moby client's adapter |
| `internal/runner/docker/options.go`, `images.go`, `store.go`, `sandbox.go`, `lock.go` | 6 | The runner's options and the probe's seam; the image contract; the store; the sandbox's settings (C13); the holder's lock |
| `internal/runner/docker/runner.go`, `launch.go`, `turn.go`, `tls.go` | 7 | `Open`, `Start`, stops, `KillAll`, `Close`'s release of the lock and the engine, the launch, a turn's life and end, the certificate |
| `internal/runner/docker/sweep.go`, `status.go` | 8 | The sweep, the minute's image check, `Status`, and `Close`'s stop of the minute's job |
| `internal/runner/docker/probe_integration_test.go` | 9 | Criterion 4 and the limits |
| `internal/deploy/deploy.go` | 10, 13 | Contract C1's Docker runner; the take's message |
| `cmd/agentswarm/serve.go`, `sockets.go`, `serve_docker_integration_test.go` | 11, 13 | Serve's start under Docker, the sockets and TLS, `KillAll` on every exit, status; the drain's wait and the worst drain |
| `internal/admin/admin.go` | 11 | `/v1/status`'s `runner` |
| `test/e2e/harness/*.go`, `test/e2e/testdata/stubborn/*` | 12 | The harness on Docker (Contract C9) and the stubborn image |
| `test/e2e/*_test.go` | 12 | The scenarios' Docker forms |
| `test/compat/main.go` | 12 | The harness's runner variables left out |
| `examples/two-instances/*`, `examples/deployment.yaml`, `cmd/agentswarm/examples_test.go` | 14 | Two instances on one host; the Docker variant; their tests |
| `plan/...`, `README.md` | 15 | The spec's amendments, ADR-006, the roadmap, Plan 7d-1's note, the README |
| `internal/llm/endpoint/live_test.go` | 16 | The live gate's echoes and output rate |

---

## Tasks

Each task lists its tests first. A test's line says what it asserts and why it fails before the implementation; "a build failure" is the expected first failure only where the test calls a name that does not exist yet. A test marked a guard passes at Step 1, and the line says why (Global Constraints, Test first). Commands run from the repository's root. Unit tests of Unix sockets make their directories under `/tmp`, since macOS caps a socket's path at 103 bytes.

### Task 1: The Linux test image, `make test-linux`, and lint for Linux

**Decisions:** 12 (the test image, lint, `make test-linux`), R11, R12. **Contracts:** C15.

**Files:**
- Create: `build/test/Dockerfile`, `build/test/run.sh` (executable; mode `linux`, Task 1's form)
- Modify: `Makefile` (`BIN ?=`, `LINUX_BIN`, `TEST_IMAGE`, `E2E_DIR`; `lint` twice; `linux-binary`, `linux-env`, `test-linux`; `.PHONY`)
- Modify, only as the notes' rule for a test that fails on Linux alone allows: the files such a failure shows wrong, and a test of each fault

**Interfaces:**
- Consumes: the existing `make test`, `make test-integration`, `make test-e2e` (whose `build` honours `BIN`).
- Produces: Contract C15's targets in Task 1's form; `bin/linux/agentswarm`; the image `agentswarm-test:dev`.

**Notes for the implementer:** pin `golang:1.26` by the digest `docker buildx imagetools inspect golang:1.26` gives, and the Docker CLI's two SHA-256 sums from `download.docker.com/linux/static/stable/{x86_64,aarch64}/docker-29.4.0.tgz`; the build fails on a mismatch. Do not add `-v` or a linter to the in-container steps.

No test of this repository has run on Linux before: under `-race`, as a uid with no passwd entry, with testcontainers inside a container. A test that passes on the Mac and fails only on Linux, in `make test-linux`, gets a reproduction first: a test in its own package that fails on Linux for the same reason. Then it gets a fix, in its own commit before Task 1's. One that cannot be fixed within the task is reported, with its reproduction, and the controller rules on it in the ledger before the task goes on (plan review 1, I2).

**Checks (write first):**
- `make test-linux` fails with "No rule to make target": no target yet.
- `docker run --rm --user 0:0 -v "$PWD:$PWD" -w "$PWD" agentswarm-test:dev build/test/run.sh linux` exits 1 with `build/test/run.sh: refuses to run as root, where the permission tests skip`: before this task there is neither the image nor the script.

- [ ] **Step 1: Run the checks above.** Expected: both fail, the first with make's "No rule to make target `test-linux'".
- [ ] **Step 2: Write** `build/test/Dockerfile`, `build/test/run.sh` and the Makefile's targets, as C15 gives them.
- [ ] **Step 3: Run the checks again.** Expected: `make test-linux` exits 0, its in-container run having passed `make test`, `make test-integration` and `make test-e2e BIN=bin/linux/agentswarm`; the root run exits 1 with the line above; `file bin/agentswarm` still names the Mac's executable, which the container left alone. A test that fails on Linux alone takes the notes' route: a reproduction, then a fix in its own commit, or a report to the controller.
- [ ] **Step 4: Gate.** `make lint` (`0 issues.` twice), `make test`, `make test-integration`, `make test-e2e`, `make test-linux`. Expected: every package `ok`.
- [ ] **Step 5: Commit:** `git add build/test/Dockerfile build/test/run.sh Makefile`; `git commit -m "build: a Linux test image, make test-linux, and lint for Linux"`, and each fix of a test that failed on Linux alone in its own commit before it.

### Task 2: The turn's spec in one package, the runtime's transport, and SIGTERM

**Decisions:** 2 (the runtime's side), 9, R1, R2, R3, R26. **Contracts:** C3 (`turn exited`), C6, C7.

**Files:**
- Create: `internal/runner/spec/spec.go`, `spec_test.go`
- Modify: `cmd/agentswarm/turn.go`, `turn_test.go` (the shared spec; `gatewayTransport`; `onTerminate`; the help)
- Modify: `internal/outcome/outcome.go` (C7's comment; `OutsideSignals`)
- Modify: `internal/runner/process/process.go`, `process_test.go` (the shared spec; outside signals; the doc)
- Modify: `test/e2e/conformance_test.go`

**Interfaces:**
- Consumes: Contract C7's exit codes; `runner.ExitCode`.
- Produces:

```go
// package spec (internal/runner/spec)
const (
	V1      = 1 // Plan 7d-1's form
	V2      = 2 // v, a socket per gateway, cert
	Label   = "io.agentswarm.turn-spec"
	MaxSize = 1 << 20
)

// Reads lists the versions this build's runtime reads; the runtime image's
// label lists them (build/runtime/Dockerfile).
var Reads = []int{V1, V2}

type Spec struct { /* Contract C6's fields, V with `json:"v,omitempty"` */ }
type Gateway struct {
	URL    string `json:"url"`
	Token  Token  `json:"token"`
	Socket string `json:"socket,omitempty"`
}

// Token prints as "[session token]" through String, GoString and LogValue.
type Token string

func Read(r io.Reader) (*Spec, error) // at most MaxSize bytes, reading no more of r; then Check
func (s *Spec) Check() error          // Contract C6's rules, Plan 7d-1's refusals included
func (s *Spec) Version() int          // 1 when V is 0
// LogValue is a spec as slog logs it: a group of its fields that holds
// neither token. slog's JSON handler would otherwise marshal the struct with
// encoding/json, which never asks Token.LogValue, and a Token.MarshalJSON
// that hid the token would hide it from the runtime too. Its receiver is a
// value, so that a Spec and a *Spec are both covered.
func (s Spec) LogValue() slog.Value
func ParseLabel(v string) ([]int, error)
func Newest(image, writes []int) (int, bool)

// package main (cmd/agentswarm)
// gatewayTransport is a gateway's transport: no proxy; HTTP/1.1 alone; its
// socket dialed for every connection when it has one; with roots, TLS 1.3
// at least and roots alone trusted.
func gatewayTransport(g spec.Gateway, roots *x509.CertPool) *http.Transport
// onTerminate writes "agentswarm: SIGTERM: the turn ends unavailable" on
// stderr and calls exit(outcome.ExitUnavailable) at the first SIGTERM on
// signals; it returns when stop closes.
func onTerminate(signals <-chan os.Signal, stop <-chan struct{}, stderr io.Writer, exit func(int))
```

**Notes for the implementer:** the turn command installs `signal.Notify(ch, syscall.SIGTERM)` for the turn's life alone and stops it on return; SIGINT keeps cancelling the turn as `run` does. The help's exit table gains, under 15, "or SIGTERM, which only the engine or an operator sends", and its last line reads "Any other code is a crash, but a runner reads an end by SIGHUP, SIGINT, SIGKILL or SIGTERM it did not send as unavailable (Contract C7)." The process runner's fake turn dies by its `Signal` by executing `/bin/sh -c 'kill -<SIGNAL> $$'`, so that Go's runtime cannot turn the signal into an exit code. `Spec.LogValue`'s value receiver draws gocritic's `hugeParam` (the `performance` tag `.golangci.yml` enables), so it carries `//nolint:gocritic // hugeParam: a value receiver, so that slog finds LogValue on a Spec as well as a *Spec`, as the harness's functions that take a spec by value do.

**Tests (write first):**
- `TestASpecWithoutAVersionIsVersion1` (`internal/runner/spec`, unit): Plan 7d-1's C6 example, without `v`, reads with `Version() == 1`. Fails first: no package.
- `TestTheRuntimeReadsVersions1And2Only` (unit): `"v": 2` reads; `"v": 3` is refused, naming 3.
- `TestASpecsSocketAndCertAreChecked` (unit, a table): a relative `socket` is refused; a `cert` that holds no PEM certificate is refused; neither, or an absolute socket and a real certificate, read.
- `TestASpecLargerThanMaxSizeIsRefused` (unit): 1 MiB + 1 byte is refused, and `Read` has read at most `MaxSize + 1` bytes of the reader.
- `TestATokenPrintsAsAPlaceholder` (unit): `%v`, `%+v`, `%#v` and `%s` of a `Spec`, and slog JSON lines that log a `Spec` and a `*Spec`, hold neither token.
- `TestTheImagesLabelParses` (unit, a table): `2` gives [2]; `1,2` gives [1 2]; the empty label, `1,,2`, `1, 2`, `0`, `a` and `1,2,` are refused.
- `TestNewestPicksTheNewestVersionBothKnow` (unit, a table): [1 2] and [2] give 2; [2 3] and [1 2] give 2; [1] and [2] give none; nothing and [2] give none.
- `TestTheRuntimeDialsAGatewaysSocketWhateverItsURLsHost` (`cmd/agentswarm`, unit): both stand-ins of `newGateways` serve on Unix sockets in a short temporary directory, plain HTTP, and the spec's URLs name `mcp.agentswarm.internal` and `llm.agentswarm.internal`, which do not resolve; the turn ends `ended` (10). Fails first: the runtime dials the URL's host.
- `TestTheRuntimeTrustsTheSpecsCertificateAlone` (unit): stand-ins serving TLS over the sockets with a certificate for those names; with that certificate as `cert` the turn ends 10; with another self-signed certificate for the same names, or the right one for another name, the turn ends 15 and the stand-ins' handlers saw no request.
- `TestTheRuntimeRequiresTLS13` (unit): a stand-in whose `MaxVersion` is TLS 1.2 is refused, 15, its handler unreached; the same stand-in at TLS 1.3 ends the turn 10. The second half is the positive control: without it the test passes today, when the runtime reaches no socket at all.
- `TestTheRuntimeSpeaksHTTP11ToAServerThatOffersH2` (unit): stand-ins that offer `h2` and `http/1.1` see at least one request, and every request with `ProtoMajor` 1. Without the first assertion the test passes today, when no request arrives.
- `TestTheRuntimesTransportsIgnoreProxies` (unit; replaces `TestTheRuntimesTransportIgnoresProxies`): with `HTTP_PROXY` and `HTTPS_PROXY` set, each gateway's transport has a nil `Proxy` and allows HTTP/1 alone, and each client uses its own gateway's transport.
- `TestOnTerminateExitsUnavailable` (unit): a SIGTERM sent on the channel makes `exit` see 15 and stderr the line above; a closed `stop` returns with no exit.
- `TestTheRuntimesHelpSaysHowSignalsRead` (unit): `agentswarm turn --help` holds both lines of the notes above.
- `TestTheRuntimeEndsUnavailableAtSIGTERM` (`test/e2e`, conformance; Review Focus 1): `agentswarm turn` as a host process, its scripted vendor hanging; after the vendor's first request, SIGTERM to the process ends it with 15 within 1 s. Fails first: it dies by SIGTERM.
- `TestExitCodesGiveTheClassBeforeTheDeadline` (`internal/runner/process`; changed): its rows "killed by a signal" become one row per signal: `KILL`, `TERM`, `INT` and `HUP` are `unavailable`; `SEGV` and `ABRT` are `crashed`. Fails first: the outside signals read `crashed`.
- `TestTheSpecReachesTheTurnOnItsStandardInput` (changed): the expected JSON gains `"v": 2` and nothing else.

- [ ] **Step 1: Write the tests above, and change the two process runner tests and `validSpec` as named.** Run `go test -race -count=1 ./internal/runner/... ./cmd/agentswarm/` and `make test-e2e`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the `spec` package; the runtime on it (`newTurnRuntime` builds a client per gateway on `gatewayTransport`, with the spec's `cert` as the roots); `onTerminate` in the turn command; the help; `outcome.OutsideSignals` and C7's comment; and the process runner on `spec.Spec` (writing `V: spec.V2`), reading outside signals as C7 says, its doc saying so.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-linux`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/runner/spec internal/outcome/outcome.go internal/runner/process cmd/agentswarm/turn.go cmd/agentswarm/turn_test.go test/e2e/conformance_test.go`; `git commit -m "feat(runner): one spec type, version 2; the runtime dials sockets over TLS 1.3, and ends unavailable at SIGTERM"`.

### Task 3: The board's notice of a turn that could not start

**Decisions:** 7 (the notice, the board's version, leniency on replay). **Contracts:** C11.

**Files:**
- Modify: `internal/work/board/board.go` (`NoticeTurnUnstarted`, `Notice.Reason`), `state.go` (`check`, `checkBody` and `checkNotice` gain a replay flag; `Apply` replays), `post.go` (`prepare` posts)
- Create: `internal/work/board/unstarted_test.go`
- Modify: `cmd/agentswarm/run_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: Contract C11. The unexported `(*State).check(author Author, b *Body, replay bool) error`: `Apply` passes true, `prepare` false.

**Tests (write first):** each in `internal/work/board`, unit, unless named.
- `TestATurnUnstartedNoticeNamesItsAgentAndAReason`: `Notify` takes `turn_unstarted` with agent `alpha` and each of `engine`, `image`, `launch`; it refuses one without an agent, one without a reason, and one of reason `quota`, each with `ErrInvalid`. Fails first: a build failure, no `NoticeTurnUnstarted`.
- `TestOnlyATurnUnstartedNoticeCarriesAReason`: a `refused_call` notice with a reason is refused.
- `TestPostingNeverTakesAnUnknownCode` (a guard, passing at Step 1): `Notify` with the code `disk_full` is refused, as today; it keeps posting strict once replay takes such a code.
- `TestReplayAcceptsAWellFormedNoticeOfAnUnknownCode`: an event of the platform's whose body `DecodeBody` reads from `{"notice": {"code": "disk_full", "reason": "quota", "limit": 3}}` replays; the board's `Report` is the one it gives without that event. Fails first: replay refuses the code.
- `TestReplayRefusesANoticeOfAnUnknownCodeThatIsNotWellFormed` (a table): an agent's notice; codes `Disk`, `disk-full`, `1disk` and 33 letters; a reason `Quota`: each fails the replay.
- `TestReplayAcceptsAKnownCodeWithAReasonThisBuildDoesNotKnow`: `turn_unstarted` with reason `quota` replays; `Notify` refuses it.
- `TestATurnUnstartedNoticeIsPostedOncePerAgentPerRoundWhateverItsReason`: `engine` then `image` for `alpha` in round 1 gives one entry and a repeat; round 2 gives a new one; `beta` gets its own.
- `TestALaterVersionsNoticeStillFailsWithErrVersion` (a guard, passing at Step 1): scenario 11(a)'s body, `{"v": 2, "notice": {"shape": "of a later build"}}`, replays as `ErrVersion`, as today; it keeps the version's check ahead of the leniency.
- `TestConcludeReadsATurnUnstartedNotice` (`cmd/agentswarm`, unit): `agentswarm conclude --board FILE` of a board holding a `turn_unstarted` notice with its reason prints its assessment.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/work/board/ ./cmd/agentswarm/`. Expected: FAIL, each test for the reason its line gives; the two guards pass.
- [ ] **Step 2: Implement** the code, the field, and the replay flag through `check` to `checkNotice`.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/board cmd/agentswarm/run_test.go`; `git commit -m "feat(board): a notice of a turn that could not start; replay takes a well-formed notice it does not know"`.

### Task 4: Launches outside the coordinator's lock

**Decisions:** 4, 7 (posting the notice), R4, R5, R6, R25. **Contracts:** C10, C11.

**Files:**
- Modify: `internal/work/app/ports.go` (C10), `coordinator.go` (`hold`, `begin`, `Launched`, `running.launching`, `startAgain`, `deadline`)
- Modify: `internal/work/app/harness_test.go` (`turns.Start`, its `pending` field, a helper that reports a launch), `leader_test.go` (`stalling.Start`), `coordinator_test.go`, `coordinator_internal_test.go`
- Modify: `internal/runner/process/process.go`, `process_test.go` (`Start`'s two results; forgetting; `errUnheld` wraps `app.ErrUnheld`)

**Interfaces:**
- Consumes: `board.NoticeTurnUnstarted`, `Notice.Reason` (Task 3).
- Produces: Contract C10; `process.(*Runner).Start(ctx context.Context, t *app.Turn) (bool, error)`.

**Notes for the implementer:**
- A `Start` that answers pending keeps the turn in `starting`, marks it in `launching`, and arms no retry. `begin` and `Launched` share one helper for a launch that failed: log the step's failure through `failed` (which fences on a lost lease), post `turn_unstarted` unless the error wraps `ErrUnheld` or the reason is `holding` or `cancelled`, then keep the turn in `starting` and arm the retry. `begin` posts under its own context; `Launched` under `c.ctx`. A failed post goes through `failed` too, and the retry is armed all the same, unless the post fenced the coordinator. A successful `Launched` does what `begin` does for a `Start` that did not fail, `startedLate` included.
- `hold` clears `launching` before it calls `StartRound` for a new round, and its reset makes the map again with the round's other maps (R5). `hold` checks `c.held` after each `begin`, and returns once it is false, starting no other turn and arming nothing: a notice's post that finds the lease gone fences the coordinator in the middle of the loop (Decision 7).
- `deadline` clears `starting` but leaves `launching`. A `Launched` with an error whose turn id `launching` still holds, for a round its deadline ended (`r.ended.by` is `EndedByDeadline`, the run still on `l.Round`, the coordinator held, the run in `c.runs`), logs the failure and posts the notice as the helper does, and arms no retry; any other `Launched` for a round that ended changes nothing (C10).

**Tests (write first):** each in `internal/work/app`, unit, unless named.
- `TestALaunchingTurnRunsOnceItsLaunchIsReported`: the fake's `Start` answers pending; until `Launched` the round does not end by its turns; after `Launched` with no error, `Exited` with `ended` for both agents ends the round by its turns. Fails first: a build failure, `Start` has one result.
- `TestALaunchingTurnAtTheDeadlineIsUnstartedAndStopped`: no `Launched` comes; at the deadline the turn counts `unstarted`, and the fake saw `Stop` for it.
- `TestARoundWaitsForItsLaunchingTurns`: one agent ended, the other launching: the round does not end until the other's turn ends.
- `TestAFailedLaunchIsTriedAgainWithTheSameID`: `Launched` with `ReasonEngine` arms a retry; after the retry wait, `Start` is called again with the same id.
- `TestStartAgainSkipsALaunchingTurn` (`coordinator_internal_test.go`, `package app`): a coordinator built by its fields, held, with a fake `Turns` and clock of the test's own and a run whose agent's turn launches, calls no `Start` when `startAgain` runs for it. No path reaches the guard today, since a launching turn has no retry armed; the test keeps it.
- `TestALaunchReportedWithLessThanAThirdOfTheRoundLeftCountsAsStartedLate`: `Launched` after two thirds of the span; the turn still running at the deadline counts `unstarted`.
- `TestAFailedLaunchPostsOneNoticePerAgentPerRound` (Review Focus 2): `engine`, then after its retry `image`, for `alpha` in round 1 leave one `turn_unstarted` notice naming `alpha`, reason `engine`; after a fence and a takeover, a failed launch in the same round adds none; round 2's failure adds one.
- `TestALaunchCancelledOrRefusedByTheHoldingPostsNothing`: `ReasonCancelled` and `ReasonHolding` arm the retry and post no notice.
- `TestAStartThatFailsPostsANoticeUnlessTheHoldingRefusedIt`: a `Start` failing with another error posts `turn_unstarted`, reason `launch`; one wrapping `ErrUnheld` posts nothing; both arm the retry.
- `TestALaunchedTheCoordinatorNoLongerWaitsForChangesNothing` (a table): another turn's id; a round its deadline ended, reported without an error; a round the run has left; a run that stopped; a fenced coordinator: no record, no notice, no retry.
- `TestALaunchThatFailsAfterItsRoundsDeadlineStillPostsItsNotice` (Review Focus 2): the fake's `Start` answers pending, and no `Launched` comes before the deadline, which ends the round and stops the turn; `Launched` with `ReasonEngine` for that turn then posts one `turn_unstarted` notice naming the agent, reason `engine`, and no `Start` follows. The same report once the run's next round opened, after a fence and a takeover, or with `ReasonCancelled`, posts nothing. It posts nothing, too, in the window where `hold` has posted round N+1's start and then failed before its reset (the fake board's `Report` fails), leaving the run on round N while the board is on N+1 (R5; plan review 2, F6). Fails first: the deadline forgets the launching turn, and the report is dropped; in the window's row, without the clear, the report passes every guard and posts into round N+1.
- `TestANoticeThatCannotBePostedStillArmsTheRetry` (a table): the board store fails the notice's append with an error of its own: the retry comes. It fails it with `holding.ErrLeaseLost`, in a round of two agents whose first agent's `Start` fails: the coordinator fences, and no `Start` is called for the second agent. Fails first, in that row: `hold` goes on to start the second agent's turn on a fenced coordinator.
- `TestLaunchedPostsUnderTheHoldingsEpoch`: the fake board store notes `EpochFrom(ctx)` of the notice's append: the term's epoch.
- `TestStartIsNeverPending` (`internal/runner/process`): `Start` answers `(false, nil)` for a turn it launched.
- `TestTheHoldingsRefusalIsErrUnheld` (`internal/runner/process`): with the holding invalid, `Start`'s error wraps `app.ErrUnheld` and not `holding.ErrLeaseLost`.
- `TestATurnIsForgottenOnceItsEndIsReported` (`internal/runner/process`): after its end is reported, a `Start` with the same id launches the turn again, and its end is reported again.

- [ ] **Step 1: Write the tests above, and change the fakes, the 25 `Runner.Start` calls and `TestARepeatedStartStartsNothing` as named.** Run `go test -race -count=1 ./internal/work/... ./internal/runner/process/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** C10 in `ports.go` and `coordinator.go`, and the process runner's `Start`, forgetting and `errUnheld`.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-linux`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/work/app internal/runner/process`; `git commit -m "feat(work): launches leave the coordinator's lock and report through Launched; a turn that could not start posts a notice"`.

### Task 5: The runtime image's build

**Decisions:** 6 (the build, the gates' image, the test), R1, R13. **Contracts:** C12, C15.

**Files:**
- Create: `build/runtime/Dockerfile`, `build/runtime/image.sh` (executable)
- Create: `test/image/image_test.go` (`//go:build image`)
- Modify: `Makefile` (`DEV_IMAGE`; `image`, `image-dev`, `test-image`; `test-linux` builds `image-dev` and passes `AGENTSWARM_E2E_IMAGE`)
- Modify: `.golangci.yml` (`image` among `run.build-tags`)
- Modify: `internal/runner/spec/spec_test.go`

**Interfaces:**
- Consumes: `spec.Reads`, `spec.Label` (Task 2); `make linux-binary` (Task 1).
- Produces: Contract C12's build; `make image`, `make image-dev`, `make test-image`.

**Tests (write first):**
- `TestTheRuntimeImagesLabelListsTheVersionsTheRuntimeReads` (`internal/runner/spec`, unit): `build/runtime/Dockerfile`'s `io.agentswarm.turn-spec` label, read from the file, parses to `spec.Reads`. Fails first: no Dockerfile.
- `TestOneCommitBuildsOneBinaryAndOneImage` (`test/image`, tag `image`): `make image IMAGE_COMMIT=HEAD` run twice, under umask 022 and then 002, prints one `binary` sum and one `image` id both times; the image is tagged `agentswarm-runtime:<its 64 digits>`; `docker image inspect` shows the user `65532:65532`, the entrypoint `["/agentswarm", "turn"]`, `io.agentswarm.turn-spec` `1,2`, `io.agentswarm.go` `go1.26.0`, and `org.opencontainers.image.version` the commit's full id; `docker run --rm --label agentswarm.project=agent-swarm --entrypoint /agentswarm <id> version` prints the commit's id as the version. Fails first: no target.
- `make image-dev` (a check): after `make linux-binary`, it prints `image sha256:<64 digits>`, and `docker image inspect` of the worktree's tag, `$(DEV_IMAGE)` (`agentswarm-runtime:dev-<8 hex digits>`), has that id. After another `make linux-binary`, whose date differs, a second `make image-dev` prints another id, and the first image is gone from the engine, its `docker image inspect` saved as `.test-clean/<its 64 hex digits>-<UTC time>.inspect.json`.

- [ ] **Step 1: Write the test and the check above.** Run `go test -count=1 ./internal/runner/spec/` and `make test-image`. Expected: FAIL, each for the reason its line gives.
- [ ] **Step 2: Write** the Dockerfile and `image.sh` as C12 gives them, and the targets.
- [ ] **Step 3: Run the tests and the check.** `go test -count=1 ./internal/runner/spec/`, `make test-image`, and the check's two rounds of `make linux-binary image-dev`. Expected: PASS; one sum and one id; the first dev image removed after its backup.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-linux`, which this task changes. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add build/runtime test/image Makefile .golangci.yml internal/runner/spec/spec_test.go`; `git commit -m "build: the runtime image of a commit, reproducibly, and the gates' image of the worktree"`.

### Task 6: The Docker runner's engine, images, store, sandbox and lock

**Decisions:** 3, 5 (the engine's port), 6 (the contract, the store, reloading), 8 (the holder's lock), R8, R14, R16, R24. **Contracts:** C12, C13, C14 (the engine's port, `Options`, `Dial`).

**Files:**
- Modify: `go.mod`, `go.sum` (`go mod tidy`: moby client and api, and `containerd/errdefs`, become direct)
- Create: `internal/runner/docker/engine.go` (the port, its types, its errors), `moby.go` (`Dial`)
- Create: `internal/runner/docker/images.go` (`checkImage`), `store.go` (`store`: `save`, `reload`, `has`), `sandbox.go` (`sandbox`), `lock.go` (`lockHolder`), and `options.go` (`Options`, its check)
- Create: `internal/runner/docker/options_test.go`, `images_test.go`, `store_test.go`, `sandbox_test.go`, `lock_test.go`, `moby_test.go` (unit, `package docker`) and `engine_integration_test.go` (`//go:build integration && linux`)
- Modify: `build/test/run.sh` (mode `docker`), `Makefile` (`test-docker`, C15)

**Interfaces:**
- Consumes: `spec.ParseLabel`, `spec.Newest`, `spec.V2` (Task 2); `app.Turn`.
- Produces: C14's `Engine`, `Info`, `Image`, `Attachment`, `Container`, `Listed`, `ErrNoImage`, `ErrNoContainer`, `ErrUnreachable`, `Dial`, `Options` and its `check`, and, unexported, for Tasks 7 and 8:

```go
// checkImage applies Contract C12's contract, and returns the spec's version
// to write: the newest the image and the runner (spec.V2 alone) both know.
func checkImage(img Image, arch string) (version int, err error)
// The store (Contract C12).
type store struct{ dir string }
func (s store) has(id string) bool
func (s store) save(ctx context.Context, e Engine, id string) error
// reload loads <hex>.tar, checks that the engine then holds id, and tags it
// agentswarm-runtime:<hex>.
func (s store) reload(ctx context.Context, e Engine, id string) error
// sandbox returns a turn's container configuration (Contract C13).
func (o *Options) sandbox(t *app.Turn) (name string, cfg *container.Config, host *container.HostConfig)
// lockHolder takes the holder's lock (Contract C12), or fails naming its path.
func lockHolder(storeDir, tenant, holder string) (unlock func(), err error)
```

**Notes for the implementer:**
- The adapter maps a connection failure (`client.IsErrConnectionFailed`), a broken connection and a context that ended to `ErrUnreachable`, and an error for which `errdefs.IsNotFound` holds to `ErrNoImage` or `ErrNoContainer`: moby client v0.5.0's error for a 404 is a `*client.httpError`, which has no `NotFound()` method and is told only through `errdefs` (plan review 1, C1). `Version` reads `ServerVersion`'s `Os` and `Arch`.
- `Attach` demultiplexes the stream with `github.com/moby/moby/api/pkg/stdcopy` into one reader, and honours its context, which the moby client's `ContainerAttach` does not once it has dialed: its hijack writes the request and reads the engine's answer on the bare connection, watching nothing (`hijack.go`). So the adapter runs `ContainerAttach` in a goroutine; when the context ends first, it returns an error wrapping `ErrUnreachable` and the context's error, and if the hijack completes afterwards, it closes that connection (plan review 1, I1). Every other call the runner makes is an ordinary request, which honours its context.
- The integration tests' runnable image is the probe image, `curlimages/curl` pinned by digest here, which carries busybox and which the tests pull themselves through the moby client (Tasks 7 to 9 use it too); their throwaway images are made with the client's `ImageImport` of a tar holding one random file, with `Changes` for the fields under test, and removed after (R14); their sockets directories, labels (the `agentswarm-it.run` label among them) and random tenants are C15's.

**Tests (write first):** each in `internal/runner/docker`, unit, unless named.
- `TestOptionsThatCannotRunTurnsAreRefused` (a table): each refusal of `check` (C14) names its field; options of every value right pass. Fails first: a build failure, no package.
- `TestAnImageMeetsTheContract` (a table): a good image gives version 2; `windows`, another architecture, no label, a label of `1` alone, a label `1,,2`, a volume, a health check `CMD true`, `ONBUILD`, `HTTPS_PROXY` in its environment, and the label `agentswarm.tenant` are each refused, saying which; a health check `NONE` passes. Fails first: a build failure, no `checkImage`.
- `TestTheSandboxIsTheRunnersWhateverTheImageSays`: for a turn and options of every limit, every field of Contract C13 holds its value, the deployment's labels are merged, and the deadline label is the turn's deadline in RFC 3339 with nanoseconds, UTC.
- `TestTheStoreWritesAnImageWholeOrNotAtAll`: a fake engine whose `Save` fails after writing half leaves no `<hex>.tar`; one that succeeds leaves it, and no temporary file.
- `TestAReloadMustGiveBackTheLockedID`: a fake whose `Image` answers another id after `Load` fails the reload and tags nothing.
- `TestTheHoldersLockIsNamedByItsTenantAndHolderInHexadecimal`: tenant `x-y` with holder `z`, and `x` with `y-z`, lock two files; a `/` in either name keeps the file in `holders/`.
- `TestASecondLockOfOneTenantAndHolderIsRefused` (Review Focus 4): a second `lockHolder` of the pair fails naming the lock's path while the first holds it, and succeeds once it unlocks.
- `TestAnAttachTheEngineNeverAnswersEndsWithItsContext` (unit, on any system; Review Focus 2): a stand-in engine on a Unix socket in `/tmp` answers the version negotiation's `/_ping` and accepts the attach request but never answers it; `Dial` of it, then `Attach` with a 100 ms context, returns an error wrapping `ErrUnreachable` within 1 s. The stand-in must answer the ping: one that answered nothing would end the call through the negotiation's own request, which honours the context, and the test would pass without the fix. Fails first: `Attach` blocks past the test's bound.
- `TestTheMobyEngineRunsAContainerOfTheSandboxsSettings` (integration, Linux): with the sandbox's settings around the probe image, its command `cat`, `Create`, `Attach`, `Start`, a write, then `Close` of the attachment end the container with exit 0; `Inspect` shows `Finished`; after `Remove`, `Inspect` gives `ErrNoContainer`.
- `TestTheMobyEngineReadsAnImagesContractFields` (integration, Linux): an imported image with `VOLUME /data`, `HEALTHCHECK CMD true`, `ENV A=b` and `LABEL agentswarm.x=y` gives those fields through `Image`.
- `TestAnImageTheEngineLostIsReloadedFromTheStoreAndTagged` (integration, Linux; Review Focus 3): an imported image is saved to a store, removed from the engine, and reloaded: its id is the same, and `agentswarm-runtime:<hex>` names it.
- `TestAnEngineThatIsNotThereIsUnreachable` (integration, Linux): `Dial("unix:///nonexistent.sock")` then `Version` fails wrapping `ErrUnreachable`; on a live engine, `Image` of an unknown id gives `ErrNoImage`, and `Inspect`, `Kill` and `Remove` of an unknown name each give `ErrNoContainer`, which a launch's step 1 meets for most turns.

- [ ] **Step 1: Write the tests above, and `make test-docker` with run.sh's mode `docker` (C15), so that the integration tests, which compile on Linux alone, run.** Run `go test -race -count=1 ./internal/runner/docker/` and `make test-docker`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the port, the adapter, the contract, the store, the settings and the lock; run `go mod tidy`.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS; `go.mod` lists the two moby modules and `github.com/containerd/errdefs` as direct, and no other module changed.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-docker`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add go.mod go.sum internal/runner/docker build/test/run.sh Makefile`; `git commit -m "feat(runner): the Docker runner's engine, image contract, store, sandbox settings and holder lock"`.

### Task 7: The Docker runner: launches, stops and ends

**Decisions:** 2 (the certificate), 5, 6 (checks at a launch and at start), 9 (the handshake, the spec), R15, R21, R23, R25. **Contracts:** C3, C6, C7, C10, C13, C14.

**Files:**
- Create: `internal/runner/docker/runner.go` (`Runner`, `Ports`, `Open`, `Start`, `Stop`, `StopAll`, `KillAll`, `Close`), `launch.go` (the slots and steps 1 to 8), `turn.go` (delivery, copy, wait, class, end), `tls.go` (`NewCertificate`)
- Create: `internal/runner/docker/fake_test.go` (a fake engine), `runner_test.go` (unit), `runner_integration_test.go` (`//go:build integration && linux`)

**Interfaces:**
- Consumes: Task 6's port, `Options` with its `probe` seam, `store`, `checkImage`, `sandbox`, `lockHolder`; `app.Launch`, `app.ErrUnheld`, the reasons (Task 4); `spec.Spec` (Task 2); `outcome.OutsideSignals`.
- Produces: C14's `Ports`; `Open` (its checks before the sweep, in C14's order, each engine call within `openWait`); `Runner`'s turn methods; `Close`'s release of the holder's lock and of the engine's client, which Task 7's own tests need to open a runner twice (plan review 1, M4); `Certificate`, `NewCertificate`.

**Notes for the implementer:** the turn's state, its timer, its launch's cancel and its attachment live in one struct under the runner's lock, and every engine call is made outside it. The fake engine records each call with its arguments, can block any call until released, returns a blocked call once its context ends, as C14's `Engine` says, and keeps containers' states; time-bound tests run in `testing/synctest` bubbles. Output is written as the process runner writes it: up to the cap, the rest dropped, never failing the turn. `launch` in `turn started` runs from `Start` to step 6's return.

**Tests (write first):** each in `internal/runner/docker`, unit with the fake engine, unless named.
- `TestStartCallsNoEngine` (Review Focus 2): with every engine call blocked, `Start` returns `(true, nil)` within 50 ms, the output file exists, and the fake recorded no call before `Start` returned. Fails first: a build failure, no `Runner`.
- `TestStartChecksTheHoldingFirst`: an invalid holding fails `Start` wrapping `app.ErrUnheld`, logs `holding refused` with `request` `turn`, makes no file and calls no engine; a context without an epoch fails with `holding.ErrNoEpoch`, logged `turn without an epoch`.
- `TestAnOutputFileThatCannotBeCreatedFailsTheStart`: a read-only output directory fails `Start`, and nothing is queued (skipped as root).
- `TestALaunchTakesItsStepsInOrderAndDeliversTheSpec`: the fake sees `Remove(name)`, `Image`, `Create`, `Attach`, `Start`; `Launched` comes once, with no error; the attachment receives Contract C6's JSON (version 2, the URLs, sockets and certificate, the deadline plus the slack, `maxTokens`); `turn started` is logged with `turn`, `container` and `launch`.
- `TestNoTokenAppearsInTheContainersConfiguration`: the `Create` call's configuration (entrypoint, command, environment, labels) holds neither token; only the attachment's bytes do.
- `TestALaunchChecksTheHoldingBeforeItStartsTheContainer`: the holding turns invalid after `Attach`; no `Start` call; the container is removed by id; `Launched` reports `ReasonHolding`; the turn is forgotten.
- `TestAFailedLaunchForgetsTheTurnBeforeItReports`: `Attach` fails; `Remove(id)` runs on a fresh context; `Launched` reports `ReasonLaunch`; inside that callback, a `Start` with the same id answers `(true, nil)`.
- `TestALaunchThatOutlastsItsBoundOrMeetsNoEngineReadsEngine` (a table; Review Focus 2): `Create` blocked past 10 s, `Attach` blocked past 10 s, and `Create` failing with `ErrUnreachable`, each report `ReasonEngine`, release the launch's slot, and remove the container, if made.
- `TestAnImageThatIsMissingOrBreaksTheContractReadsImage` (a table): no image and no store copy; a label of `1` alone; a volume: each reports `ReasonImage`, no `Create` call.
- `TestALaunchReloadsAnImageTheEngineLost` (Review Focus 3): `Image` answers `ErrNoImage`, the store holds the copy: `Load`, `Image` again, `Tag("agentswarm-runtime:<hex>")`, then `Create`; `an image was reloaded from the store` is logged.
- `TestAtMostEightLaunchesRunAtOnce`: twelve `Start`s with `Create` blocked: eight `Create` calls; once released, all twelve launch.
- `TestALaunchQueuedPastItsDeadlineWhileTheEngineDoesNotAnswerReadsEngine` (a table; Review Focus 2): with every slot held, a queued launch reaches its deadline: `ReasonCancelled` while the engine's last call answered, `ReasonEngine` when it found the engine unreachable. With every slot held and the engine unreachable, a `Stop` at the turn's deadline, before the runner's own timer fires, reports `ReasonEngine`, as the coordinator's deadline does when it comes first (plan review 2, F1).
- `TestAStopCancelsALaunchingTurn`: `Stop` while `Create` blocks: the launch's context ends, the container is removed by id on a fresh context, `Launched` reports `ReasonCancelled`.
- `TestAFenceCancelsTheLaunchsStepsButNotItsStart`: cancelling the context the runner's `Start` was given, while the engine's `Create` blocks, reports `ReasonCancelled`; cancelling it while the engine's `Start` blocks lets that start complete, and the turn runs.
- `TestAStartedTurnStoppedBeforeItRunsIsKilledRemovedAndReportedCancelled`: `Stop` while the container's `Start` blocks: once it returns, `Kill`, `Wait` and `Remove` follow, `Launched` reports `ReasonCancelled`, and no `Exited` comes.
- `TestStopAndStopAllNeverWait`: with `Kill` blocked, `Stop`, `StopAll` and the deadline's kill each return within 50 ms, and `Kill` is called for each running turn.
- `TestTheSpecIsDeliveredBeforeLaunchedReturns`: with the `Launched` callback blocked, the spec still reaches the attachment.
- `TestTheTurnsEndIsReportedOnceAfterLaunched`: the container ends while `Launched` blocks: `Exited` waits, then comes once.
- `TestASpecThatCannotBeDeliveredEndsTheTurnUnavailable`: `Skills` fails: the container is killed and the end reads `unavailable`.
- `TestHowATurnEndedIsReadFromItsContainer` (a table, by C7's order): the board's turn end, `ended`; `Finished` at the deadline, `interrupted`; the runner's own kill, `interrupted`; `OOMKilled` with 137, `crashed`; 10 to 15, their classes; 139 and 134, `crashed`; 1, `crashed`.
- `TestASignalTheRunnerDidNotSendReadsUnavailable` (Review Focus 1): 129, 130, 137 and 143, with `OOMKilled` false and no kill of the runner's, read `unavailable`.
- `TestAFailedWaitInspectsTheContainerAgain` (a table): `Wait` fails; `Inspect` running then exited reads the exit's class; gone reads `unavailable`; unreachable until the deadline plus the slack reads `unavailable` and removes nothing.
- `TestAContainerStillRunningPastItsDeadlineAfterAFailedWaitIsKilledByName`: after a failed `Wait`, the container runs past the deadline plus the slack: `Kill` by its name.
- `TestATurnsContainerIsRemovedAfterItsEndIsReported`: `Inspect`, then `Exited`, then `Remove(id)`; a `Remove` that fails is logged `a container could not be removed`, and nothing else changes.
- `TestKillAllRemovesEveryTurnsContainerAndWaitsAtMostASecond`: turns launching, started and running: every launch is cancelled, and `Remove` is called by name for each, in parallel; with the engine silent `KillAll` returns within 1 s, and with it answering as soon as all answered.
- `TestARepeatedStartStartsNothing`: `(true, nil)` while the turn launches and `(false, nil)` once its `Launched` was reported; one `Create`.
- `TestTheOutputIsCappedAndTheTurnNeverFailsForIt`: 3 MiB of output leaves a 1 MiB file, and the turn ends `ended`.
- `TestTurnExitedSaysTheContainerAndHowItEnded`: `turn exited` has `turn`, `container`, `class`, `exit` and `oom`.
- `TestOpenRefusesWhatTheRunnerCannotRunOn` (a table, in C14's order): the engine unreachable names `runner.docker.endpoint`; an unwritable store names `runner.docker.store`, not `holder`, since the store is checked before the lock in it; the holder's lock held names `holder`; an image neither on the engine nor in the store, and one breaking the contract, name `runner.images.thinloop`; in a bubble, a `Save` blocked past `openWait` names `runner.docker.endpoint`.
- `TestOpenSavesEachImageToTheStore`: an image on the engine and not in the store is saved once to `<hex>.tar`; a second `Open`, after `Close`, saves nothing.
- `TestCloseReleasesTheHoldersLockAndClosesTheEngine`: after `Close`, `lockHolder` of the pair succeeds and the fake engine's `Close` was called once; `Close` makes no other engine call and kills nothing.
- `TestNewCertificateIsAnECDSAP256LeafForBothGatewaysForTenYears`: P-256; DNS names exactly the two; `NotAfter` ten years after `now`; a TLS 1.3 handshake over a pipe with the certificate as the only root verifies for both names.
- `TestASandboxedTurnReadsItsSpecAndItsExitGivesItsClass` (integration, Linux): with the `probe` seam running `sh -c 'head -c 1 >/dev/null; exit 10'` in the probe image, a turn is launched, reported `Launched`, ends `ended`, and its container is gone afterwards.
- `TestOpenNeedsItsPortsAndOptions`: `Open` without the engine, the holding, `Skills`, `Ended`, `Exited` or `Launched`, or with options `check` refuses, fails, and calls no engine.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/runner/docker/` and `make test-docker`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the runner, the launch, a turn's life and end, `Open`'s checks, `Close`'s release of the lock and the engine, and the certificate.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-docker`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/runner/docker`; `git commit -m "feat(runner): the Docker runner launches turns off the coordinator's lock, stops them without waiting, and reads how they ended"`.

### Task 8: The sweep, the minute's image check, and the runner's status

**Decisions:** 5 (the sweep; serve's exits, for `Close`), 6 (the check every minute), 7 (`engine`, `missingImages`), R7. **Contracts:** C3, C14 (`Status`, `Close`).

**Files:**
- Create: `internal/runner/docker/sweep.go`, `status.go`, `sweep_test.go`, `status_test.go`, and `sweep_integration_test.go` (`//go:build integration && linux`)
- Modify: `internal/runner/docker/runner.go` (`Open` runs the start sweep after the images' checks, then starts the minute's job; `Close` first stops the job)

**Interfaces:**
- Consumes: Task 7's `Runner`, its registry of turns and its `Close`; Task 6's `store`.
- Produces: C14's `Status`; `Close`'s stop of the minute's job, waiting at most 1 s for its pass in flight, before Task 7's release of the lock and the engine; and `Open`'s start sweep.

**Tests (write first):** each in `internal/runner/docker`, unit with the fake engine, unless named.
- `TestOpenRefusesAHolderAnotherServeHoldsBeforeItSweeps` (Review Focus 4): with the holder's lock free, `Open` lists the tenant's containers; with it held by another, `Open` fails naming `holder`, and the fake saw no `List`, `Kill` or `Remove`. Fails first: `Open` does not sweep.
- `TestTheStartSweepRemovesThePreviousProcesssContainers`: of the holder's own containers, a running one is killed and then removed, a stopped and a never-started one removed, each by id; another holder's are left.
- `TestTheSweepNeverTouchesATrackedContainer`: a container whose `agentswarm.turn` names a registered turn stays, whatever its state and times.
- `TestTheSweepRemovesItsOwnUntrackedContainersWhenDue` (a table): stopped with `Finished` 61 s ago, removed, and 59 s ago, kept; never started and created 11 s ago, removed, and 9 s ago, kept; running past its deadline by the slack and 61 s, killed and removed, and by the slack and 59 s, kept.
- `TestTheSweepLeavesAnotherHoldersContainersUntilFiveMinutesPastTheirDeadline` (a table): 5 min 1 s past, removed; 4 min 59 s, kept; a missing or unreadable deadline label, removed an hour after creation and kept before.
- `TestTheSweepActsOnIDsNeverOnNames`: every `Kill` and `Remove` of the sweep names an id from its listing.
- `TestTheSweepRunsEveryMinute`: in a bubble, `List` is called at start and at 1 and 2 minutes.
- `TestTheMinuteCheckReloadsAnImageTheEngineLostAndNamesWhatIsMissing`: an image the engine lost is reloaded from the store; one neither on the engine nor in the store, or breaking the contract, is listed in `Status().MissingImages` and logged `an image is missing`; once back, it leaves the list.
- `TestStatusSaysWhetherTheEngineAnsweredItsLastCall` (a table): `ok`, then `unreachable` after a call that found it so, then `ok` after one that answered; a `Stop` that cancels a blocked `Create` leaves `ok` (C14's `Status`; plan review 2, F3); `Status` makes no engine call.
- `TestCloseStopsTheMinutesJobWaitingAtMostASecond` (a bubble): after `Close`, no `List` comes; with a minute's pass blocked in the engine, `Close` returns within 1 s, and the holder's lock is released all the same.
- `TestTheStartSweepRemovesAContainerOfThePreviousProcess` (integration, Linux): a stopped container labelled with the test's tenant and holder, and `agentswarm.project=agent-swarm`, is gone once `Open` returns.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/runner/docker/` and `make test-docker`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the sweep, the minute's job, `Status`, and `Close`'s stop of the job.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-docker`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/runner/docker`; `git commit -m "feat(runner): the Docker runner's sweep, its minute's image check, and its status"`.

### Task 9: Criterion 4 and the sandbox's limits, probed

**Decisions:** 3, 11, R15. **Contracts:** C13, C14 (the `probe` seam), C15.

**Files:**
- Create: `internal/runner/docker/probe_integration_test.go` (`//go:build integration && linux`)
- Modify: any file of `internal/runner/docker` a probe shows wrong, with a unit test that reproduces it first

**Interfaces:**
- Consumes: `Open`, `Start`, `Stop`, the `probe` seam, `NewCertificate` (Tasks 6 to 8).
- Produces: the probes.

**Notes for the implementer:** each probe runs the probe image (Task 6) under the runner's own settings, through the `probe` seam, in a sockets directory of C15's that the test serves both gateways' stand-ins on, over TLS with a certificate from `NewCertificate`. A probe's command gets the certificate's PEM as an argument, writes it to `/tmp`, and prints its findings on standard output, which the test reads from the turn's output file.

**Tests (write first):** each an integration test, on Linux.
- `TestTheSandboxHasNoInterfaceButLoopback`: `/sys/class/net` lists `lo` alone.
- `TestNoNameResolvesInTheSandbox`: `nslookup example.com` and `getent hosts host.docker.internal` fail.
- `TestNoTCPConnectionLeavesTheSandbox`: connections to `1.1.1.1:443`, `169.254.169.254:80`, the bridge network's gateway (read with `docker network inspect bridge` by the test) and `host.docker.internal:80` all fail.
- `TestBothSocketsAnswerOverTLS13AndHTTP11WithTheSpecsCertificate`: `curl --unix-socket /run/agentswarm/mcp.sock --cacert <pem> https://mcp.agentswarm.internal/` and the same for `llm` answer the stand-ins' text, over TLS 1.3 and HTTP/1.1.
- `TestASandboxWithoutTheSocketsGroupCannotConnect`: with `Group` set to another group, both connections are refused with a permission error.
- `TestTheSandboxWritesOnlyWhereItMay`: of `/`, `/etc`, `/run/agentswarm`, `/var`, `/home`, `/root`, `/dev`, `/tmp` and `/dev/mqueue`, a file can be made in `/tmp` and `/dev/mqueue` alone; a pty opens through `/dev/ptmx`; `/proc/self/oom_score_adj` can be raised and not lowered.
- `TestTheSandboxHoldsNoEngineSocketNorSecrets`: no `/var/run/docker.sock`, no path of the test's secrets directory, and `/proc/self/mountinfo` mounts nothing of the host but the sockets directory and the engine's own files.
- `TestTheSandboxsResolverNamesLoopbackAlone`: `/etc/resolv.conf` names `nameserver 127.0.0.1` and no search domain.
- `TestTheSandboxRunsUnprivileged`: uid and gid 65532; `CapEff` 0; `NoNewPrivs` 1; no `/dev/shm`.
- `TestTheSandboxsLimitsAreEnforced`: a fork past 64 processes fails; `awk 'BEGIN{s="x"; while(1) s=s s}'` past 256 MiB ends the container `OOMKilled`, and the turn reads `crashed`.
- `TestAnImageThatWouldShapeTheSandboxIsRefused` (a table, imported images): `VOLUME`, `HEALTHCHECK CMD true`, `ENV HTTPS_PROXY=…`, `LABEL agentswarm.data=/etc`, and no `io.agentswarm.turn-spec`: `Open` refuses each naming `runner.images.thinloop`, and a launch of each reports `ReasonImage`.
- `TestAnImagesOwnSettingsDoNotShapeTheSandbox`: an image imported from an export of the probe image's filesystem, with `STOPSIGNAL SIGQUIT`, `USER root` and `WORKDIR /etc`, its contract skipped by the seam, runs as 65532 in `/tmp`, and `docker inspect` shows `SIGTERM` and a timeout of 10.
- `TestAKillTakesTheSetsidChildrenWithIt`: a probe starts `setsid` children that ignore TERM and HUP, each requesting the MCP stand-in through its socket every 100 ms, with curl; the stand-in sees their requests; `Stop` ends the turn, afterwards no container of it remains, and no request reaches the stand-in for 2 s. The test observes through the stand-in, since it runs in the test container's PID namespace, and on a Mac the engine's host is OrbStack's VM: it cannot list the host's processes (plan review 1, M13).
- `TestASandboxEndsWhenItsAttachmentEnds`: a probe running `cat`: closing its attachment, as serve's death does, ends the container within 2 s.
- `TestAContainerTheEngineStopsOrKillsReadsUnavailable` (Review Focus 1): a probe running `sleep 600`: `docker stop` reads `unavailable` (143); `docker kill` (a SIGKILL the runner did not send) reads `unavailable` (137, not `OOMKilled`).

- [ ] **Step 1: Write the tests above.** Run `make test-docker`. Expected: each passes at once, which the report says, or fails on a fault of the runner, which then gets a unit test that fails first, and a fix.
- [ ] **Step 2: Fix** what the probes find, each fix test first.
- [ ] **Step 3: Run the probes.** `make test-docker`. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-docker`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/runner/docker`; `git commit -m "test(runner): criterion 4 and the sandbox's limits, probed on Linux"`, and each fix in its own commit before it.

### Task 10: The deployment file's Docker runner

**Decisions:** 8 (the deployment file and its refusals that need no system), R9, R10. **Contracts:** C1.

**Files:**
- Modify: `internal/deploy/deploy.go` (`KindProcess`, `KindDocker`; `Docker`; `Runner.Docker *Docker`; the decoder's pointers to structs; `Check`'s runner rules), `deploy_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:

```go
// package deploy
const (
	KindProcess = "process"
	KindDocker  = "docker"
)

// Docker is runner.docker (Contract C1), its defaults filled in.
type Docker struct {
	Endpoint string            `yaml:"endpoint"` // unix:///var/run/docker.sock
	Sockets  string            `yaml:"sockets"`
	Store    string            `yaml:"store"`
	Group    int               `yaml:"group"`    // -1, the default: serve's own group
	Labels   map[string]string `yaml:"labels"`
	Memory   int64             `yaml:"memory"`   // [268435456]
	CPUs     int64             `yaml:"cpus"`     // [1]
	Pids     int64             `yaml:"pids"`     // [64]
	Files    int64             `yaml:"files"`    // [256]
	Tmp      int64             `yaml:"tmp"`      // [16777216]
}
// Runner gains Docker *Docker `yaml:"docker"`: nil without a runner.docker block.
```

**Tests (write first):** each in `internal/deploy`, unit.
- `TestADockerRunnerLoadsWithItsDefaults`: `kind: docker` with `sockets` and `store` alone gives every default of C1, `Group` -1 and empty `Labels`. Fails first: `runner.docker` is not a key.
- `TestTheDockerVariantOfTheContractsExampleLoads`: C1's Docker example, its `<64 hexadecimal digits>` filled in, loads key by key.
- `TestKeysOfTheProcessRunnerAreRefusedWithTheDockerRunner` (a table): `listeners.mcp`, `listeners.llm`, `runner.binary` and `runner.unsafeNoSandbox: true` are refused, each naming its key.
- `TestADockerBlockIsRefusedWithTheProcessRunner`: `runner.docker` under `kind: process` is refused, naming `runner.docker`.
- `TestTheDockerRunnerNeedsItsSocketsAndStore`: without them, `runner.docker.sockets` and `runner.docker.store` are named.
- `TestAnEndpointThatIsNotAUnixSocketIsRefused`: `tcp://127.0.0.1:2375`, `ssh://host` and `unix://relative.sock` are refused.
- `TestTheRunnersOwnLabelsAreRefused` (a table): each of the seven keys and `agentswarm.data` is refused naming `runner.docker.labels.<key>`; `agentswarm.project` is not.
- `TestAGroupOf0IsRefused`: 0 and -2 are refused; 990 is not.
- `TestTheSocketsDirectoryMustStandApartAndFitASocketsPath` (Review Focus 5; a table): a relative path; one of 99 bytes (its `mcp.sock` at 108); one inside `secrets`; one holding `runner.output`; one equal to the store; one holding `listeners.addresses`; one inside `configuration.repository`: each names `runner.docker.sockets`; one of 98 bytes passes.
- `TestTheSandboxsLimitsMustBeAboveZero` (a table): `memory`, `cpus`, `pids`, `files` and `tmp` of 0 are each named; `cpus: 0.5` is refused as not a whole number.
- `TestAnUnknownRunnerKindIsRefused` (a guard, passing at Step 1): `kind: kubernetes` names `runner.kind`, as today, when `Check` refuses every kind but `process`; it keeps the refusal of the kinds `Check` does not know once it takes `docker`.
- `TestAProcessRunnerNeedsUnsafeNoSandbox` (changed): its `kind: docker` case, on `minimal()`'s document, asserts with `Contains` that `runner.unsafeNoSandbox` is named, now as a key the Docker runner refuses, and that `runner.kind` is not; not the exact list, since the document also draws `listeners.mcp`, `listeners.llm`, `runner.docker.sockets` and `runner.docker.store`.
- `TestTheAgentsListenersAreOnLoopbackWithTheProcessRunner` (changed): its `kind: docker` case asserts with `Contains` that the listener's key under test is named, now as a key the Docker runner refuses, and that `runner.kind` is not; not the exact list, for the same reason.

- [ ] **Step 1: Write the tests above, and change `TestAProcessRunnerNeedsUnsafeNoSandbox` and `TestTheAgentsListenersAreOnLoopbackWithTheProcessRunner`.** Run `go test -race -count=1 ./internal/deploy/`. Expected: FAIL, each test for the reason its line gives; the guard passes.
- [ ] **Step 2: Implement** the block, the decoder's pointers, and `Check`'s rules.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-docker`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add internal/deploy`; `git commit -m "feat(deploy): the Docker runner's block in the deployment file, and its refusals"`.

### Task 11: Serve with the Docker runner

**Decisions:** 1, 2 (the listeners), 5 (`KillAll` on every exit), 7 (status; the process runner's warning), 8 (the start's checks; the sockets), 10 (the group's guard), R19, R20. **Contracts:** C2, C3, C4, C10, C14.

**Files:**
- Create: `cmd/agentswarm/sockets.go` (`listenSockets`, `sandboxListener`), `sockets_test.go`, `serve_docker_integration_test.go` (`//go:build integration && linux`)
- Modify: `cmd/agentswarm/serve.go` (`checkSystem`; the start's order; the runner by kind; TLS on the agents' listeners; `server.run` returns its code and `serve` calls `server.exit`; the runner's status; `serve --help`), `serve_test.go`, `serve_integration_test.go`
- Modify: `build/test/run.sh` (mode `docker` runs `-run Docker ./cmd/agentswarm/`, C15)
- Modify: `internal/admin/admin.go`, `admin_test.go` (`RunnerStatus`, `Ports.Runner`)
- Modify: `internal/runner/process/process.go`, `process_test.go` (`KillAll`)

**Interfaces:**
- Consumes: `docker.Open`, `docker.Dial`, `docker.NewCertificate`, `(*docker.Runner).Status`, `Close` (Tasks 6 to 8); `deploy.Docker`, `KindDocker` (Task 10); `app.Launch` (Task 4).
- Produces:

```go
// package main (cmd/agentswarm)
// turnRunner is a runner as serve uses it.
type turnRunner interface {
	app.Turns
	KillAll(ctx context.Context)
}
// checkSystem refuses runner.kind docker on a system other than Linux.
func checkSystem(goos, kind string) error
// listenSockets checks runner.docker.sockets (Decision 8) on the system,
// takes its flock, replaces mcp.sock and llm.sock, and listens on both,
// mode 0660 and the group; engineSocket is the endpoint's path.
func listenSockets(d *deploy.Deployment, group int, engineSocket string) (mcp, llm net.Listener, unlock func(), err error)
// sandboxListener serves TLS on ln, 1.3 at least, with the gateways'
// certificate (Decision 2).
func sandboxListener(ln net.Listener, cert tls.Certificate) net.Listener
// server gains runner turnRunner, in place of *process.Runner, and closer
// io.Closer: the Docker runner, which exit closes; nil under the process
// runner, which has no Close.

// exit kills every turn (s.runner.KillAll), then closes the Docker runner
// when s.closer is not nil, then logs serve stopped with code: on every exit
// path, and never Close before KillAll.
func (s *server) exit(ctx context.Context, code int) error

// package admin
type RunnerStatus struct {
	Kind          string   `json:"kind"`
	Sandboxed     bool     `json:"sandboxed"`
	Engine        string   `json:"engine,omitzero"`
	MissingImages []string `json:"missingImages,omitzero"` // nil under process; never nil under docker
}
// Ports gains Runner interface{ Status() RunnerStatus }.

// package process
// KillAll is StopAll: the process runner's kills are synchronous.
func (r *Runner) KillAll(ctx context.Context)
```

**Notes for the implementer:**
- **The start's order under Docker** (R20): load the deployment (as before, and the store a writable directory); `checkSystem(runtime.GOOS, kind)`; the database and the schema; the holding, which moves out of `wire` (`serve.go:511` makes it today) to before `listen`; the certificate; `docker.Open`, its `Ports.Skills`, `Ended`, `Exited` and `Launched` bound to the registry and the coordinator made later; then the listeners (the admin's TCP; the sockets, with `group` resolved from -1 to `os.Getgid()`, each wrapped by `sandboxListener`); then the rest as before. `newServers` gives the agents' two servers `Protocols` that allow HTTP/1 alone, under either runner (the process runner's plain HTTP never negotiated HTTP/2). Under the process runner, nothing else changes but the warning `turns run unsandboxed`.
- `listenSockets` refuses, naming `runner.docker.group`, a group of the engine socket's (stat of `engineSocket`) or one not in `os.Getgroups()` nor `os.Getgid()`; naming `runner.docker.sockets`, a directory that is missing, not a directory, not writable by serve, not traversable by the group (group-owned by it with `g+x`, or `o+x`), holding anything but `mcp.sock` and `llm.sock`, flocked by another serve, or, by the resolved paths of those that exist, inside or holding the secrets, the output, the store, the addresses file or the repository. Its sockets are removed when their listeners close.
- `server.run` returns its exit code, and `serve` returns `r.s.exit(ctx, code)`, so every exit passes through `KillAll` and then the Docker runner's `Close`, which `exit` reaches through the server's `closer`, an `io.Closer` that is nil under the process runner (R19; plan review 2, F2). `server.close`, which the drain ends with, leaves the runner alone: closing it there would come before `KillAll`, whose removals need the engine's client (plan review 1, M2). A start that fails after `docker.Open` closes the runner it opened, as it closes what else it opened; that runner has no turn yet.
- The status adapter gives `{Kind: docker, Sandboxed: true, Engine, MissingImages}` from `Status()`, `MissingImages` never nil, and `{Kind: process}` for the process runner.

**Tests (write first):**
- `TestServeRefusesTheDockerRunnerOnASystemOtherThanLinux` (`cmd/agentswarm`, unit): `checkSystem("darwin", "docker")` names `runner.kind` and Linux; `("linux", "docker")` and `("darwin", "process")` pass. Fails first: a build failure.
- `TestASocketsDirectoryThatCannotServeIsRefusedNamingTheKey` (unit, a table; Review Focus 5): missing; a file; not writable (skipped as root); mode 0700 for the group; holding `notes.txt`; flocked by the test; a symbolic link into the secrets directory; a directory holding the output directory: each names `runner.docker.sockets`.
- `TestTheSocketsAreMadeForTheSandboxesGroup` (unit): a stale `mcp.sock` is replaced; both sockets are 0660 with the group; closing the listeners removes them, and the flock is released.
- `TestAGroupThatIsTheEnginesOrNotServesIsRefused` (unit): the engine socket's group, and a group serve is not in, each name `runner.docker.group`.
- `TestTheAgentsListenersServeTLS13AndHTTP11Alone` (unit): over a socket with the certificate, a client with `MaxVersion` TLS 1.2 fails its handshake; one offering `h2` and `http/1.1` is served HTTP/1.1; both servers keep `ReadHeaderTimeout` 10 s and `ReadTimeout` 30 s.
- `TestTheAddressesNameTheSocketsUnderTheDockerRunner` (unit): the addresses of unix listeners are `unix:<dir>/mcp.sock` and `unix:<dir>/llm.sock` (C2).
- `TestServeKillsEveryTurnBeforeItExits` (unit): `server.exit` with a fake runner, which is also the server's `closer`, calls `KillAll` once, then `Close` once, then logs `serve stopped` with the code; with a nil `closer`, as under the process runner, it calls `KillAll` and logs; `server.close`, which a drain ends with, calls neither.
- `TestStatusShowsTheRunner` (`internal/admin`, unit): the docker form's JSON is `{"kind":"docker","sandboxed":true,"engine":"ok","missingImages":[]}` exactly, and the process form's `{"kind":"process","sandboxed":false}`.
- `TestServesHTTPServersBoundTheirReads` (changed): the agents' two servers also allow HTTP/1 alone.
- The admin API's test fixture (changed): `admin.New` gets a runner port.
- `TestKillAllKillsEveryTurnBeforeItReturns` (`internal/runner/process`): after `KillAll`, no turn's process group is left.
- `TestServeWarnsThatTheProcessRunnersTurnsRunUnsandboxed` (`cmd/agentswarm`, integration): `start` on a process deployment logs `turns run unsandboxed` at Warn with `runner` `process`.
- `TestServeStartsWithTheDockerRunner` (`cmd/agentswarm`, integration, Linux): `start` on a Docker deployment (a fresh database, `AGENTSWARM_E2E_IMAGE`'s image, a sockets directory of C15's) writes addresses whose `mcp` and `llm` are `unix:` paths; a request over the MCP socket, TLS 1.3 with the certificate the handshake presents, is answered over HTTP/1.1; `/v1/status` shows the runner `{docker, true, ok, []}`. Then the test calls `s.close`, then `s.exit(ctx, 0)`: `close` removes the sockets, and `exit`, after it, releases the holder's lock. The holder's lock, taken as `lockHolder` takes it (C12's path, with `flock(LOCK_EX|LOCK_NB)`, since `lockHolder` is the docker package's own), fails between the two calls and succeeds after them (plan review 2, F2). It fails, saying why, when `AGENTSWARM_E2E_IMAGE` is unset.

- [ ] **Step 1: Write the tests above, and add run.sh's step that runs `-run Docker ./cmd/agentswarm/` under `make test-docker` (C15).** Run `go test -race -count=1 ./cmd/agentswarm/ ./internal/admin/ ./internal/runner/process/`, `go test -race -count=1 -tags integration -run TestServeWarns ./cmd/agentswarm/` and `make test-docker`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the notes above.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-linux`, which contains `make test-docker`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add cmd/agentswarm internal/admin internal/runner/process build/test/run.sh`; `git commit -m "feat(serve): serve runs the Docker runner on Linux, over TLS on two sockets, and kills every turn on every exit"`.

### Task 12: The harness on Docker

**Decisions:** 12 (the harness on Docker, the tenant, the stubborn image, the scenarios' forms), R17, R18, R27, R28, R29. **Contracts:** C1, C2, C3, C4, C9, C15.

**Files:**
- Modify: `test/e2e/harness/env.go` (`Runner`, `Tenant`; `tenantID` gives way), `deployment.go` (C1's Docker form; `Stubborn`), `serve.go` (the sockets directory; the skip; `TurnsStarted`; `SocketsDir`), `containers.go` (`Gone`, `ContainerConfig`, through the `docker` command line), `admin.go` (`RunnerStatus`)
- Create: `test/e2e/testdata/stubborn/Dockerfile` (`FROM busybox@sha256:<digest>`, the script copied with `--chmod=0755`, `USER 65532:65532`, `ENTRYPOINT ["/stubborn"]`, `LABEL io.agentswarm.turn-spec="1,2"`), `test/e2e/testdata/stubborn/stubborn` (`#!/bin/sh`, `trap '' TERM`, `exec sleep 120`)
- Modify: `test/e2e/serve_test.go`, `vendor_faults_test.go`, `holding_faults_test.go`, `helpers_test.go`, `harness_test.go`
- Modify: `test/compat/main.go`, `main_test.go`
- Modify: `build/test/run.sh`, `Makefile` (the stubborn image; `AGENTSWARM_E2E_STUBBORN`; C15's final modes)

**Interfaces:**
- Consumes: serve under both runners (Task 11); `make image-dev` (Task 5).
- Produces: Contract C9's part made by Task 12; C15's final form.

**Notes for the implementer:**
- The scenario helpers `turnPIDs`, `gone`, `waitForTurns` and `allGone` become `turnsStarted`, `harness.Gone`, `waitForTurns` over `TurnStarted` and `allGone` over `TurnStarted`; `stubborn` gives way to `DeploymentSpec.Stubborn`.
- Scenario 7 measures each turn's life from its round's `openedAt` in `inspect item`'s `round_opened` record (R18).
- Scenario 14 under Docker scripts the vendor to hold every answer until the test has read `ContainerConfig` of each container `turn started` named, then to answer as `investigating` does (R29). A background reader reads each such container's configuration once and marks it read; the vendor holds a request until the containers read equal the `turn started` lines in serve's log and the log has not grown for 200 ms, since the vendor cannot tell which container a request comes from, each round starts new containers, and a `turn started` line may reach the log after its turn's first request.
- `test/compat` removes the four harness variables of C9 from the old tree's environment (R28).

**Tests (write first):** each e2e, unless named; each scenario runs under both runners.
- `TestTheHarnessWritesADockerDeploymentWhenAsked` (on any system; `Options.Runner` `docker`, `ImageEnv` set to a digest): the deployment holds C1's Docker form as C9 says, with the sockets under `/srv/agentswarm-e2e/<tenant>/`, and none of the process runner's keys. Fails first: `Options` has no `Runner`.
- `TestTheHarnessTakesItsTenantFromTheEnvironment`: unset, the deployment's tenant is `harness`, a guard half that passes at Step 1, since the tenant is `harness` today; `AGENTSWARM_E2E_TENANT=e2e-x` makes it `e2e-x`, and `AuditVerify` verifies that tenant's chain, which fails first, since the harness reads no variable.
- `TestTheHarnessLocksTheStubbornImageUnderDocker` (on any system): with `Stubborn`, a Docker deployment's `runner.images.thinloop` is `StubbornEnv`'s id; a process deployment's `runner.binary` is the harness's script.
- `TestTheOldScenariosRunWithoutThisBuildsHarnessVariables` (`test/compat`, unit): with the four variables in the program's environment, the scenarios' command has none of them.
- `TestTheHarnessDeploymentNamesEverySecretItUses` (changed): its runner's keys follow `env.Runner()`.
- `TestServeWritesItsAddressesAndBecomesReady` (changed): under Docker `mcp` and `llm` are `unix:` paths in `SocketsDir()` and the status's runner is `{docker, true, ok, []}`; under the process runner they are on `127.0.0.1`, the runner `{process, false}`, and `turns run unsandboxed` is logged.
- `TestATurnThatNeverEndsIsKilledAtItsDeadline` (scenario 7; changed): with `Stubborn`, each turn is `interrupted`, its life from the round's opening above the limit less 1 s and at most the limit plus 500 ms (process) or 2 s (Docker), and every started turn `Gone`.
- `TestAStandbyTakesOverFromAKilledLeader` (scenario 8; changed): every turn a started is `Gone` within 10 s.
- `TestAPartitionedLeaderSendsNothingAfterTheStandbysTake` (scenario 9; changed): a's fence leaves every started turn `Gone` within 10 s.
- `TestNoSessionTokenLeavesThePlatform` (scenario 14; changed): under Docker, no session token, vendor key, server credential or admin token appears in any turn container's `ContainerConfig`, read while the turn ran.
- `TestKillingTheHarnessParentLeavesNothingRunning` (scenario 16; changed): serve and every started turn are `Gone` within 5 s, in both cases, the stubborn one through `Stubborn`.
- `TestASecondSignalKillsEveryTurnAndExitsAtOnce` (changed): serve exits 1 within 2 s, and every started turn is `Gone`.
- `TestInspectPrintsALaterVersionRaw`, scenario 10's `lastRenewal`, and scenario 11(a)'s `laterRecord` and `laterEvent` (changed): their SQL names `env.Tenant()`.

- [ ] **Step 1: Write the tests above, and change the scenarios and helpers as named.** Run `make test-e2e` and `go test -race -count=1 ./test/compat/`. Expected: FAIL, each test for the reason its line gives; the guard half passes.
- [ ] **Step 2: Implement** the harness's part of C9, the stubborn image, compat's change and C15's final modes. A scenario that fails on a component's fault gets a component test that reproduces it first, in the component's package, then the fix.
- [ ] **Step 3: Run the tests.** The commands of Step 1, then `make test-docker`, whose e2e run now serves every scenario under Docker. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-linux`, which contains `make test-docker`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add test/e2e test/compat build/test/run.sh Makefile`; `git commit -m "test(e2e): the harness's scenarios run with sandboxed turns, a tenant per invocation"`, and each fix in its own commit before it.

### Task 13: The drain's wait for the leader, and the worst drain

**Decisions:** 14, R21, R22. **Contracts:** C3 (`worstDrain`).

**Files:**
- Modify: `cmd/agentswarm/serve.go` (`drainer.busy`; `leaderBusy`; `worstDrain`; `serve started`), `serve_test.go`, `serve_integration_test.go`
- Modify: `internal/deploy/deploy.go` (the take's message no longer speaks of the drain's wait)

**Interfaces:**
- Consumes: the deployment's timings.
- Produces:

```go
// leaderBusy is the leader's busy bound: a take's sample, the take, and a
// given-up take's release, takeWait + 3 × renewEvery.
func leaderBusy(d *deploy.Deployment) time.Duration
// worstDrain is the longest a drain can take before serve exits (R22).
func worstDrain(d *deploy.Deployment) time.Duration
```

**Tests (write first):** each in `cmd/agentswarm`.
- `TestTheDrainWaitsForTheLeaderUpToItsBusyBound` (unit, a bubble): with `busy` 20 s, a leader whose `Done` closes 19 s after its `Stop` is waited for; one at 21 s is not, and `the leader did not stop within its busy bound` is logged. Fails first: the drainer has no `busy`.
- `TestTheLeadersBusyBoundCoversASampleATakeAndAGivenUpTakesRelease` (unit, a table): the defaults give 20 s; `takeWait` 2 s and `renewEvery` 1 s give 5 s; the 25 s path (a take that succeeded, then a sample and a schema check, then the stop) is above it.
- `TestTheWorstDrainCountsEachTerm` (unit, a table): the defaults give 1m22s; `mcp.recordTimeout` 6 s adds 3 s; `drain` 40 s adds 10 s; `renewEvery` 4 s takes 1 s away; a `takeWait` of 60 s, whose standby path then passes the leader's, gives 60 + 15 + 32 s (the close's 30 s, `KillAll`'s 1 s and the runner's `Close`'s 1 s).
- `TestServeStartedNamesTheWorstDrain` (integration): `serve started` has `worstDrain` `1m22s` for the default timings.
- `TestTheUpstreamSessionsCloseBesideTheLLMGatewaysDrain` (changed): `ttl` becomes `busy`, 20 s.

- [ ] **Step 1: Write the tests above, and change the existing one.** Run `go test -race -count=1 ./cmd/agentswarm/` and `go test -race -count=1 -tags integration -run TestServeStartedNamesTheWorstDrain ./cmd/agentswarm/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Implement** the bound, the function, the log's attribute and the message.
- [ ] **Step 3: Run the tests.** The commands of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-docker`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add cmd/agentswarm internal/deploy`; `git commit -m "feat(serve): the drain waits for the leader's busy bound, and serve logs its worst drain"`.

### Task 14: Two instances on one host

**Decisions:** 13, and Decision 6's docs on images. **Contracts:** C1.

**Files:**
- Create: `examples/two-instances/a.yaml`, `b.yaml`, `agentswarm@.service`, `agentswarm-config.service`, `agentswarm-config.timer`, `agentswarm-config-gc.service`, `agentswarm-config-gc.timer`, `README.md`
- Modify: `examples/deployment.yaml` (a commented Docker variant, between the lines `# >>> the Docker runner` and `# <<< the Docker runner`)
- Modify: `internal/deploy/deploy_test.go`; create `cmd/agentswarm/examples_test.go`

**Interfaces:**
- Consumes: `deploy.Load` (Task 10); `worstDrain` (Task 13).
- Produces: the examples.

**Notes for the implementer:** `README.md` says: how the bare clone is made (`git clone --bare`, then `git config gc.auto 0` and `git config maintenance.auto false`) and fetched; the units, and what `OnFailure=` names; that no test here runs serve under systemd, so the first deployment checks these units on its host; the image store, its backup, how to find the images no active item locks before trimming it, that plain prunes and `until=` filters take runtime images, and that changing an engine's image store invalidates every lock; building with `make image` on the engine that runs it; switching from `process` with no item active; live-restore off, and why; the supported hosts and SELinux; serve in a container and what it needs; and that serve's user in the `docker` group is root on the host in effect, so the platform gets a host or a virtual machine of its own.

**Tests (write first):**
- `TestTheTwoInstancesDeploymentsLoad` (`internal/deploy`, unit): `a.yaml` and `b.yaml` load with `kind: docker`; holders `a` and `b`; the same database, secrets, store, images and repository; different sockets, output, addresses and admin ports. Fails first: no files.
- `TestTheExampleDeploymentsDockerVariantLoads` (`internal/deploy`, unit): `examples/deployment.yaml` with its `runner:` block replaced by the variant's lines, uncommented, and its `listeners.mcp` and `listeners.llm` lines dropped, loads with `kind: docker`.
- `TestTheExampleUnitStopsAfterTheWorstDrain` (`cmd/agentswarm`, unit): `agentswarm@.service`'s `TimeoutStopSec` is above `worstDrain` of `a.yaml`.
- `TestTheExampleUnitsKeepTheirSettings` (`cmd/agentswarm`, unit): `agentswarm@.service` holds `SupplementaryGroups=docker`, `After=` and `Wants=docker.service`, no `Requires=` or `BindsTo=`, `Restart=always`, `RestartSec=5`, `StartLimitIntervalSec=0`, `CPUWeight=1000`, `RuntimeDirectory=agentswarm/%i` with `RuntimeDirectoryMode=0750`, and no `LimitNOFILE`, `PrivateTmp` or `BindPaths`; `agentswarm-config.service`'s `ExecStart` is Decision 13's fetch exactly, with `OnFailure=`; its timer fires every minute, and the gc timer weekly.

- [ ] **Step 1: Write the tests above.** Run `go test -race -count=1 ./internal/deploy/ ./cmd/agentswarm/`. Expected: FAIL, each test for the reason its line gives.
- [ ] **Step 2: Write** the examples and the README.
- [ ] **Step 3: Run the tests.** The command of Step 1. Expected: PASS.
- [ ] **Step 4: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-docker`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 5: Commit:** `git add examples internal/deploy/deploy_test.go cmd/agentswarm/examples_test.go`; `git commit -m "docs(examples): two instances on one host, their units, their shared clone and their image store"`.

### Task 15: The documents

**Decisions:** every decision, since this task writes their amendments: its brief carries the whole Decisions section.

**Files:**
- Modify: `plan/adr/ADR-006-agent-runtime-sdk-sandbox.md`: "The sandbox network" under Consequences (no network, two sockets, TLS 1.3); the exit-code table's last row (signals not the runner's); serve's privileges and the narrow launcher carried; "As built (Plan 7d-2)" (the Docker runner, the sandbox, images and their store, the probes of criterion 4); its status line; History, 2026-10-28.
- Modify: `plan/2026-10-11-agent-swarm-platform-spec.md`: Section 2's agent runner row; Section 8.1 (a turn that could not start is shown as a `turn_unstarted` notice with its reason); Section 8.2 (a sandbox's end when serve dies or is killed, and what a serve killed with -9 leaves until a sweep; the engine stopping or restarting, and live-restore; launches outside the coordinator's lock; the signals a runner did not send); Section 9.3 (the sandbox's hop is TLS 1.3 over a Unix socket); Section 15 (the sandbox's limits [256 MiB], [1], [64], [256], [16 MiB]; and, fixed in code, 8 launches at once, a launch's 10 s, `KillAll`'s 1 s, `Close`'s 1 s, `Open`'s 30 s for each engine call, the sweep's minute, a stop's 10 s, the certificate's ten years); its status line.
- Modify: `plan/2026-10-12-implementation-roadmap.md` (Plan 7d-2 executed; the checkpoint next), `plan/2026-10-20-plan-7d1-serve-and-harness.md` (a note under Decision 12 naming this plan's amendment of the `Turns` port: pending launches, `Launched`, runners forgetting reported turns, signals read as `unavailable`), `README.md` (the Docker runner; `make image`, `make image-dev`, `make test-image`, `make test-linux`, `make test-docker`; that on a Mac these targets need OrbStack, in whose engine's VM `/srv` resolves, since Docker Desktop does not share `/srv`, and on a Linux host the buildx plugin; that on a Linux host `make test-linux` makes `/srv/agentswarm-e2e` as root, which `make test-clean` leaves in place; that an older build's `agentswarm conclude` refuses a board holding a notice's `reason`; `examples/two-instances/`), and this plan's status line.

**Interfaces:**
- Consumes: Tasks 1 to 14.
- Produces: the documents.

**Checks:**
- Every bracketed value this plan adds appears in spec Section 15, and every value fixed in code is written without brackets.
- `grep -rniE "emplo[y]ee" README.md plan/*.md plan/adr/*.md examples` finds no new line but the vocabulary rule of this plan's Global Constraints.

- [ ] **Step 1: Write the documents.**
- [ ] **Step 2: Run the checks.** Expected: as stated.
- [ ] **Step 3: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-docker`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 4: Commit:** `git add plan/adr/ADR-006-agent-runtime-sdk-sandbox.md plan/2026-10-11-agent-swarm-platform-spec.md plan/2026-10-12-implementation-roadmap.md plan/2026-10-20-plan-7d1-serve-and-harness.md plan/2026-10-28-plan-7d2-docker-runner.md README.md`; `git commit -m "docs: Plan 7d-2's amendments to the spec, ADR-006, the roadmap and Plan 7d-1"`.

### Task 16: The live gate (Part B is the controller's)

**Decisions:** 15. **Contracts:** C1, C4.

**Part A, by an implementer, spending nothing.**

**Files:**
- Modify: `internal/llm/endpoint/live_test.go` (`//go:build live`)

**Interfaces:**
- Consumes: the live test's wiring, `e.connect` and `playLiveTurn`.
- Produces: `TestLiveEchoesOfAThinkingAnswerWithoutText` and `TestLiveOutputRateFitsARound`.

**Tests (write first):** each skips without `AGENTSWARM_LIVE_KEY`, as the live check does.
- `TestLiveEchoesOfAThinkingAnswerWithoutText`: through the gateway, a prompt that asks for reasoning first, and to answer in one sentence without a tool, gives an answer holding a thinking block and text; two follow-up requests send that answer back with the nudge after it, once with its text emptied (so the runtime sends the thinking block alone) and once with its text replaced by a space; the test logs, for each, whether the vendor answered 200, and fails when either was refused, naming which.
- `TestLiveOutputRateFitsARound`: for each model of `AGENTSWARM_LIVE_RATE_MODELS`, comma-separated (by default `claude-haiku-5-5`), a name kept apart from the live check's `AGENTSWARM_LIVE_MODEL` (`live_test.go:164`), which names one model for another test, three calls through the gateway with `max_tokens` 4,096 and a prompt that asks for the numbers from one to three thousand in words, one a line; it logs each call's output tokens per second, and fails unless the slowest writes 4,096 tokens within 150 s, the round's time limit, and within the call timeout, 10 minutes.

- [ ] **Step 1: Write the two tests.** Run `go vet -tags live ./internal/llm/endpoint/` and `go test -count=1 -tags live -run 'TestLiveEchoes|TestLiveOutputRate' ./internal/llm/endpoint/` without the key. Expected: vet clean; both skip.
- [ ] **Step 2: Gate.** `make lint`, `make test`, `make test-integration`, `make test-e2e`, `make test-docker`. Expected: every package `ok`; `0 issues.` twice.
- [ ] **Step 3: Commit:** `git add internal/llm/endpoint/live_test.go`; `git commit -m "test(llm): live checks of the echoes after a nudge and of the slowest model's output rate"`.

**Part B, by the controller, never an implementer: it spends money and uses the vendor's key.**

- [ ] **Step 4: Ask the author** to approve the spend, about $0.10, and the key's handling below. Without that approval, stop here, and the branch's end runs without it, saying so.
- [ ] **Step 5: Prepare,** on the Mac, from the branch's clean HEAD: `make linux-env linux-binary image`, the image's id noted; a labelled `postgres:17-alpine` container, which the test container reaches at `host.docker.internal` and its published port; and a secrets directory in a new temporary directory of mode 0700, holding `database/url`, `admin/token`, `mcp/itbench` and `vendor/key`, the key written from the keychain, mode 0600, never printed. In the test image (`docker run --init --label agentswarm.project=agent-swarm`, as `make test-linux` runs it, the checkout at its own path, with `~/itbench` mounted read-only at `/home/test/itbench`, where the README's `~/itbench` resolves under the image's `HOME`, and the secrets directory read-only at `/run/live-secrets`), build `itbench` into `bin/linux/itbench` (in `bench/itbench`, `go build -o ../../bin/linux/itbench ./cmd/itbench`), and follow `bench/itbench/README.md`'s "Under agentswarm serve", with `jq` from the test image, and with the repository root's `bin/linux` first on `PATH` by its absolute path, not `$PWD/bin/linux`, since the README's steps run from `bench/itbench` (plan review 2, F7). So `agentswarm` is the Linux binary `make linux-binary` built and `itbench` the one just built, run from `PATH` where the README says `./itbench`. Neither the README's `make build` nor its `go build -o itbench` is run in the container: they would overwrite the Mac's `bin/agentswarm` and `bench/itbench/itbench` in the shared checkout (R11). The deployment is of `kind: docker`: `secrets: /run/live-secrets`, its sockets under `/srv/agentswarm-e2e/live/a`, its store `/srv/agentswarm-e2e/live/store`, `runner.images.thinloop` the image's id, and `runner.docker.labels` `{agentswarm.project: agent-swarm}`. The key is never an environment variable or an argument of `docker run` or `docker exec`.
- [ ] **Step 6: Run S16** under serve with the Docker runner, and follow its item to its stop. **Pass:** every `turn started` names a container; the run stops by convergence or quiet; every model call is `ok`; the board holds no `turn_unstarted` notice; no failure on the platform's side; `/v1/status`'s runner is `{docker, true, ok, []}`; and no container of the run remains a minute after serve's drain. Record whether it hit S16's key, beside the 2026-10-21 run.
- [ ] **Step 7: Run** the live tests in the same container, the key read from the mounted file by the container's own shell, so that it is that one command's environment alone: `sh -c 'AGENTSWARM_LIVE_KEY="$(cat /run/live-secrets/vendor/key)" make test-live'`. **Pass:** the three live tests pass. If an echo is refused, the runtime's change of what it sends back is a task of its own, test first, before the final review.
- [ ] **Step 8: Clean up and record.** Stop serve with SIGTERM; delete the temporary secrets directory (the keychain keeps the key, and the other secrets are the run's own, so it needs no backup); remove the Postgres container after `make test-clean`'s kind of backup; record the costs, the output rates, the echoes' answers and S16's outcome in "Changes made during execution".

## The branch's end (the controller's)

- [ ] **The full `make test-linux`**, before the final review. Expected: every package `ok`.
- [ ] **The containerd store.** Start `docker:29-dind` (pinned by digest) with `--privileged`, the label `agentswarm.project=agent-swarm`, `DOCKER_TLS_CERTDIR=` empty and its port 2375 published on `127.0.0.1:23750`; check that `DOCKER_HOST=tcp://127.0.0.1:23750 docker info` shows `driver-type io.containerd.snapshotter.v1`; run `DOCKER_HOST=tcp://127.0.0.1:23750 make test-image`. Expected: PASS: one binary and one id on that store. Then remove the container.
- [ ] **The final review**, by a fresh reviewer on Opus 5.5, of the whole branch against this plan; then its fix wave, and the full `make test-linux` again.

## Changes made during execution

- **Task 1** (7ff2719, c56c430, 417556a; fix round c26fd80):
  - `build/test/run.sh` builds the binary under test with `CGO_ENABLED=0` in its e2e step, which C15's literal command does not set (c26fd80). `make build` sets no `CGO_ENABLED`, so the Linux gate would test a cgo build while the release is static (`.goreleaser.yaml`), and each run would overwrite `bin/linux/agentswarm`'s static build with it.
  - The test container takes the name of its worktree, `agentswarm-test-<the first 8 hex digits of the SHA-256 of the checkout's path>` (`$(TEST_CONTAINER)`), which C15's `docker run` does not give it. A second run in a worktree fails at once on the engine's name conflict, instead of running beside the first on the same `bin/linux` and `/srv/agentswarm-e2e`. `make test-docker` (Task 6) takes the same name, so the two never run at once in a worktree. A name left by a killed run is freed with `docker rm -f <name>`, which Task 5's refusal says, and the README names it too (6639736, in the final review's fix wave).
  - The container also gets `-e TINI_KILL_PROCESS_GROUP=1`: tini passes a signal to `run.sh`'s whole process group, so a Ctrl-C stops every step and `run.sh`'s cleanup runs. A docker CLI killed outright had left the container running unattached.
  - `run.sh` carries C15's per-invocation tenant, the `AGENTSWARM_IT_RUN` export and the end-of-run cleanup from this task, since C15 dates none of them to a later one; they do nothing until Tasks 6 and 12 give them containers. A recorded check shows that a run leaves no container with its labels.
  - `internal/audit/s3store`'s integration tests reach their store through a loopback listener that forwards to the published port (7ff2719). This failed on Linux alone: in the test container, testcontainers reaches a published port at the bridge's gateway, and the store takes plain HTTP for a loopback host only. A test pins that the store takes the tests' endpoint.
  - `TestManyShortStringsWithALongLiteralCostLittle`'s bound rises from 1 s to 10 s (c56c430). Under `-race` the walk runs about twice as slowly on Linux as on a Mac, and took up to 1.5 s there with every CPU busy. The test guards against folding the literal once per string, which costs 2m48s on a Mac and 5m16s on Linux, so the new bound is still far below what it guards against.
- **Task 2** (41f36cf; fix round c68c1ce, 6077ed8, 74ad00d, 3d7efa7):
  - A gateway's transport sets `Protocols` to HTTP/1.1 and also has a TLS configuration of its own that offers `http/1.1` alone over ALPN, with a `cert` in the spec or without (c68c1ce). `Protocols` alone leaves the ALPN offer of `h2` that the clone of `http.DefaultTransport` carries, so an https gateway with no `cert` (every version 1 spec) negotiated h2 with a server that offers it and failed with EOF: a regression from Plan 7d-1.
  - `spec.Token` prints as the placeholder for every verb (74ad00d): a `Format` method joins `String`, `GoString` and `LogValue`, since `fmt` asks those for some verbs only and printed the token itself for `%d`.
  - `spec.Gateway` gains a `LogValue` (a value receiver), which `Spec.LogValue` uses, since slog's JSON handler marshals a struct with `encoding/json`, which never asks `Token.LogValue`. A `Token` in an unexported field still prints raw, as `fmt` cannot call its methods: a `Token` is held only in `spec.Gateway` or in exported fields, and no struct that holds one is logged except through `LogValue`.
  - The runtime's own SIGINT ends a turn 14, `interrupted`, as it does for `run`; Decision 9's SIGINT among the signals read `unavailable` applies to a runtime that dies by it. The help's row 14 reads "or SIGINT" (6077ed8), and so does ADR-006's exit table (Task 15). The help also gained a sentence on the sockets and the certificate.
  - `TestExitCodesGiveTheClassBeforeTheDeadline`'s SIGSEGV and SIGABRT rows skip on macOS, where each death by one writes a crash report to `~/Library/Logs/DiagnosticReports`, and run in `make test-linux`; a SIGUSR1 row, which writes none and reads `crashed`, runs everywhere (3d7efa7).
- **Side fix 1** (8fad865; found by Task 2's gate, fixed after its re-review and before Task 3):
  - `forward` in `internal/llm/app/gateway.go` answers with the cut whenever the request cannot go on: its request has ended, its round has, or its run has stopped, whichever its `select` hears first. It had answered with whichever came first. The outcome is still recorded, the slot freed and no attempt made after the end; as for the end of the bounded context, none of the answer's thinking blocks or tool calls' ids is remembered.
  - `TestNoAttemptIsMadeOnceTheRequestEnded` failed now and then, answering 529 where 502 was expected; the flake predates this plan. A context's cancel closes its own `Done` before it cancels the contexts derived from it, so the attempts, which stop on the request's `Done`, could free the slot, record the outcome and return the vendor's 529 while the bounded context had not yet ended, and the `select` took it.
  - A run that stops during the wait raced the same way, and more often; `TestNoAttemptIsMadeOnceTheRunStops` covers it. Under load the request's test failed 6 of 10,800 runs and the run's 275 of 10,800; neither failed in 18,000 and 10,800 runs after the fix.
- **Task 3** (b957e20; fix round b1c8720):
  - `board.MaxNotices` (10) caps, and counts, only the notices an agent's calls leave, `refused_call` and `failed_read`, as its doc says; `turn_unstarted` is held to one per agent per round by its key. The change is in `internal/work/board/round.go` and in the doc of `internal/work/app/boards.go`'s `Notice`. `Notify`'s cap had counted every non-model notice and, once reached, answered a repeat of the agent's latest notice with no error, so a relaunch that failed after ten refused calls in a round left no `turn_unstarted` notice while the coordinator was told it had posted one.
  - `Notify` finds a repeat by its key before any rule runs, so a post whose key matches a later build's replayed notice comes back as that repeat.
  - Replay takes a well-formed `reason` on every known code, not only `turn_unstarted`: C11's "a known code whose reason this build does not know" is read literally and forward-compatibly, and replay checks a reason's format only. No reader reads `Reason`. A `refused_call` with a reason that this build would not post therefore replays; posting stays strict.
- **Task 4** (139d447; fix round d89f775, 6a69820, bb84025):
  - R5's clear of the launching marks runs in `missed` too, not only in `hold`, before `missed` posts the next round's start (d89f775). `missed` posts round N+1's start, and if a later step then fails (`InFlight`, `Report` or the round's end), the run keeps round N with its marks, and a late failed `Launched` would post round N's `turn_unstarted` notice into round N+1: the defect R5 exists to prevent. `running.launching`'s doc says a mark lasts until `Launched` reports a failure, or `hold` or `missed` clears it.
  - `TestARepeatedStartStartsNothing`, which the plan changes by dropping its `Start` after the turn's end, also holds the first turn's report in flight across the repeated `Start` (6a69820). With forgetting, a `Start` repeated after the report starts the turn again, so the test had depended on its 200 ms turn being unreported when the test reached the repeat.
- **Task 5** (7f75a62; fix round 8fa4d08):
  - `build/runtime/image.sh dev` takes four arguments, `dev BINARY TAG CONTAINER`, where C12 has `dev BINARY TAG` (8fa4d08). CONTAINER is the worktree's test container (`$(TEST_CONTAINER)`), and while a container of that name exists the script refuses and changes nothing, since a test run may be using the image TAG names.
  - `no-test-container`, an order-only prerequisite of `linux-env`, `linux-binary` and `image-dev` (and, from Task 12, `stubborn-image`), makes the same check first, even under `make -j`, so a second `make test-linux` or `make test-docker` in a worktree refuses before it changes anything. Before this, a second run rebuilt `bin/linux/agentswarm`, moved `$(DEV_IMAGE)` and backed up and deleted the live run's image, all before the container's name stopped it. A standalone `make linux-binary` or `make linux-env` refuses too while the container exists, a stale one included, and names the remedy.
  - `make image` builds with the Dockerfile of the commit's `git archive`, not the checkout's, and refuses a commit that has none, naming why: its runtime predates the image contract (8fa4d08). Decision 6 says the image holds "the commit's content and nothing of the worktree". With the checkout's Dockerfile, an image of an older commit would claim today's spec versions (one from before this plan would claim 2, which its runtime cannot read, and fail every launch where it should be refused at serve's start), and an uncommitted Dockerfile edit would change a release image.
  - The toolchain `make image` builds with is the one the commit's own `go.mod` names, its `toolchain` line or else its `go` line, where C12 fixes `go1.26.0`, and the `io.agentswarm.go` label names it, so that a later bump of the pin does not change an old commit's image. The checkout's script still supplies the module path in `-X`, `./cmd/agentswarm` and the context's file name.
  - The release build runs in a clean Go environment, which C12's step 3 does not name (8fa4d08): `GOENV=off`, `GOWORK=off`, empty `GOFLAGS` and `GOEXPERIMENT`, and the per-architecture variables (`GOAMD64`, `GOARM64` and the rest) and `GOFIPS140` unset. Otherwise two developers' builds of one commit differ silently, which defeats the reproducibility `make image` promises and its test cannot see; `GOFIPS140` changes the binary. `GOENV=off` also ignores module settings kept only in a `go env` file (`GOPROXY`, `GOPRIVATE`, `GOMODCACHE`, `GOPATH`): a visible failure or a cold module cache, never a different binary. `ARCH` keeps the name C12 gives it.
  - `make image`'s build passes `--platform linux/$arch`, which C12's step 5 does not (7f75a62). Without it, an `ARCH` other than the engine's gave a binary of one architecture inside an image whose config says another. On the classic store it leaves the id as it was; the branch's end checks the containerd store.
  - The dev image's predecessor is removed only when no tag names it (7f75a62). C12 trusts `docker image rm` to refuse, but by id it refuses only when two or more tags name an image; with exactly one other tag it untags that tag and deletes the image.
  - `make` passes its `GO` to the script, so that a dev image's `io.agentswarm.go` names the toolchain `linux-binary` built with (7f75a62).
- **Task 6** (3e249ad; fix round 2be44f0, 6c71cd7):
  - C14 is amended: `Kill` returns nil when the engine answers 409, that the container exists and does not run (2be44f0). This is for `Kill` alone; any other call's 409 stays an ordinary error of the port, and the port's doc reads "nil when it does not run; `ErrNoContainer` when gone". Every kill that races a container's end would otherwise get an error the port does not name, and step 8 would read a cancelled launch as `launch` and post a notice. The adapter, not the runner, owns the engine's error domain, and the turn's class is read from the container's state, so no caller needs to tell "killed" from "had already ended".
  - C15 is amended: `run.sh`'s end also removes every image labelled `agentswarm-it.run=<its invocation's id>`, after the containers (6c71cd7). Each is saved first with `docker image inspect` under `.test-clean/<its 64 hex digits>-<UTC time>.inspect.json`, as `image.sh` saves one, and never replacing a file; an image whose backup or removal fails stays, and the script says so. It untags an image's own tags one by one and passes no `--force`, so an image a container uses stays, and it lists with `docker image ls -a`, as this engine hides untagged images without it. A killed run's throwaway images would otherwise stay on the engine (one is tagged `agentswarm-runtime:<64 hex>`, like a release image), and Tasks 7 to 9 add more.
  - The probe seam is Task 6's, and so are the integration helpers that Tasks 7 to 9 share; the plan gives them no owner. `Options.probe` skips the image contract and replaces the entrypoint, in two halves: `Options.specVersion`, which `Open`, a launch and the minute's check call and never `checkImage`, or the contract half does nothing; and the entrypoint in `sandbox`. A unit test pins both.
  - `helpers_integration_test.go` holds the engine fixture, a random `it-` tenant, the `agentswarm-it.run` label and throwaway images made by `ImageImport`. Task 6's own tests make their PEM with `crypto/x509`, since `NewCertificate` comes in Task 7.
  - `Attach` goes one step past the plan's note: when its context ends first, it closes the hijack's connection at once, whether dialed already or later, through a dialer of its own that `Dial` gives the client, after settling the API version with one ping per client. The plan only has the connection closed if the hijack completes afterwards. An engine that never answers an attach would otherwise keep a goroutine and a socket per launch for as long as it hangs.
  - A contract signature keeps its by-value parameters with `//nolint:gocritic // hugeParam`, as `process.New` does: `Coordinator.Launched(ctx, Launch)`, `docker.Open(…, Options)` and `Spec.LogValue`. An unexported function takes a pointer instead: `checkImage` takes `*Image`, where C12 has `Image`, and Task 11's `sandboxListener` takes `*tls.Certificate`.
- **Task 7** (6d68f14; fix round 17f59a2):
  - The turn-id check, the capped output writer, the output file's creation and C7's exit-code table are one small package that both runners use, `internal/runner/output` (`CheckID`, `Create`, `File`, `Class`), in place of a copy in the Docker runner. A copied logic block drifts, and one fix to the package reaches both runners. `Create` and `Class` go beyond the two pieces first named, since C7's table is a contract that both runners must change together.
  - The process runner's `capped`, `classes`, id check and file creation are gone, a pure move, and the package has no test file of its own: the process runner's unchanged tests and the Docker runner's cover it. The holding check, the board's "ended" check and the spec's write stay in each runner, a few lines with logs of their own.
  - `openImage` is what the plan calls the runner's image check at `Open`: it finds the image of `runner.images.<name>` on the engine or reloads it from the store, checks it through `Options.specVersion`, and saves it to the store unless the store has it. It was `(*Runner).checkImage` until 17f59a2, renamed because two functions of one name invite a call of the package's `checkImage`, which the probe seam's contract half does not reach.
  - `openImage`'s engine calls run within `openWait` as two bounds, one for the image's find (the inspect and, when the engine lost the image, the reload's load, inspect and tag) and one for its save, where C14 bounds each call.
  - The engine-health record, `watched`, a wrapper round every engine call but `Close`, is made in Task 7, since its queued launches read it: a launch that a stop ends at its turn's deadline reads `engine` while the engine's last call found it unreachable. Task 8's `Status` reads it.
  - The `Wait` exception: a wait whose own deadline came first marks nothing, since the engine answers a wait only once the container ends, so a container that outlasts the wait's deadline says nothing of the engine. The exception also covers step 8's wait, bounded by the launch's 10 s, where C14's `Status` doc lists a launch's 10 s among the bounds that mark the engine unreachable. It has no effect today, as step 8's `Kill` before the wait and `failed`'s `Remove` after it note the engine. The built `Status` doc says "but a wait whose own deadline came first".
  - A turn's first wait is bounded at its deadline plus the runtime's slack, after which `waitEnd`'s failed-wait loop applies (17f59a2); Decision 5 bounds the waits after a failed one and not the first. It was the one path where no bound reaches a tracked container, which the sweep never touches: a lost deadline kill with a runtime past its own deadline, or an engine that holds the wait.
  - An engine that stops answering holds a turn past that limit by one inspect's bound, `endWait`, 10 s; or, when it shows the container running and then goes silent, by the kill's, the wait's and a last inspect's, about 30 s and 40 s at most. `waitEnd`'s doc says so, and two tests pin the kill by name at the limit (ac80d30, in Task 8's range).
  - A stopped turn whose container start then fails reports the stop's reason, `cancelled` (or `engine` at the deadline, by plan review 2's F1), not `launch` (17f59a2). `KillAll`'s removal can overtake a start in flight, which would otherwise post a false `turn_unstarted` notice for a turn that serve itself killed.
  - A `Create` that a stop cancels returns no id, so the launch has nothing to remove by id. The container the engine made all the same is a never-started one, which the sweep removes 10 s after its creation, and a retry's step 1 removes it by name. `TestAStopCancelsALaunchingTurn` has two rows for it: `Create` held (nothing removed by id) and `Attach` held (removed by id, on a context of its own).
  - The engine's 409 stays an ordinary error for `Remove` (a removal already in progress) and `Create` (a name in use). Step 1's removal by name that meets one fails the launch `launch`, and the coordinator's retry tries again once the removal has finished; a later removal logs `a container could not be removed` and leaves the container to the sweep; `KillAll` ignores it; and a `Create`'s 409 fails the launch `launch`.
  - `KillAll` ignores its context's cancellation: its removals run on `context.WithoutCancel(ctx)`, within 1 s, since serve calls it on exits where its own context may have ended already.
  - A failed launch also removes the turn's empty output file; `exit` in `turn exited` is -1 when the engine showed no end; and the runner pauses between waits that the engine refuses at once, 100 ms doubling to 2 s, rather than loop. A refused `Open` leaves the engine to its caller, who closes it: `Open`'s doc says so, and serve does (Task 11).
  - Beyond C3, the Docker runner logs, at Warn, `a turn's output could not be written`, `a turn's spec could not be delivered` (whatever the turn's class) and `the board could not tell whether a turn ended`.
- **Task 8** (ac80d30, ac82702, d9affa3; fix round 53c1805, ae2eb79):
  - C3 gains three lines that the Docker runner logs at Warn, which the harness does not read. `the sweep did not finish` (`err`) means a minute's sweep was cut short by a listing that failed or by the engine's silence; `the image check did not finish` (`err`), that the minute's image check was cut short by the engine's silence; `a container could not be inspected` (`container`, `err`), that the sweep's inspect of a container failed, and the container is left to the next sweep. Nothing may fail silently (spec Section 8.1). The harness reads serve's log by message only, so no new line can break it.
  - The failed inspect had first been logged as `a container could not be removed`, which misled an operator who filters by message (ae2eb79).
  - `an image is missing` (Error) is logged by each pass that finds the image missing, one line a minute for each image. The minute's check also lists an image as missing when the engine refuses its inspect with an answer other than "no such image", besides Decision 6's two cases: neither on the engine nor in the store, or breaking the contract.
  - An own-holder's running container with no readable `agentswarm.deadline` label is due an hour after its creation, as another holder's is. Decision 5 gives that rule for another holder's alone, and without one such a container would never be swept.
  - The start sweep refuses `Open`, naming `runner.docker.endpoint` and leaving the holder's lock free, on a listing that failed or on the engine's silence at any call. A failure on one container, such as a removal the engine refuses, is logged and left to the minute's sweep, so that one container never refuses a start, and the next container is removed in the same pass (53c1805).
  - ac80d30 carries two small fixes of Task 7's re-review (the two tests of the kill by name, and `waitEnd`'s doc), and ac82702 moves `watched` to `status.go`, unchanged but for its imports.
- **Task 9** (bc25e2f; fix round 4622d05): every probe passed at its first run, so no fault of the runner was found; six runs of `make test-docker` with one sandbox setting changed each failed their probe, for its own reason.
  - Decision 11's capabilities probe reads the permitted, bounding and ambient sets (`CapPrm`, `CapBnd`, `CapAmb`) besides `CapEff`, and expects all four to be zero (4622d05). For a user who is not root `CapEff` is 0 whatever `CapDrop` holds, so removing `CapDrop: ["ALL"]` left every probe green; with `CapDrop` alone removed the bounding set reads `00000000a80425fb`, the engine's default. The empty bounding set is the sandbox's own mark of `CapDrop: ALL`.
  - `TestTheSandboxHoldsNoEngineSocketNorSecrets` counts a mount as the engine's own only where the engine mounts one (`/proc`, `/sys` and `/dev` and below them, and `/tmp`), so that a whole tmpfs of the host's, bound anywhere else, is seen as the host's.
  - `TestNoTCPConnectionLeavesTheSandbox` has one more target, a listener of the test's own on the engine's default bridge, which a sandbox on the default bridge reaches, where `make test-docker` runs the test, and a sandbox under `none` cannot. Without it, the probe's sensitivity to a sandbox with a network rests on the host's internet.
  - No probe reads the sandbox's CPU, open-files or `/tmp` limits from inside; `sandbox_test.go` pins them in the container's configuration, and ADR-006 says so. The probes read the process and memory limits.
  - The TLS 1.3 and HTTP/1.1 halves of the socket probes are set by the stand-ins' own settings, so they probe the sandbox's side; the real gateways' listeners are tested over a socket by Task 11's `TestTheAgentsListenersServeTLS13AndHTTP11Alone`.
- **Side fix 2** (d20d396; found in Task 9, fixed in its fix round):
  - `TestAFailedWaitInspectsTheContainerAgain/gone` (Task 7) expects an `Inspect` after the failed `Wait` and before the turn's end is reported, no longer as the very next call. It had flaked 5 times in 300 under `-race` on a Mac, expecting `Inspect` and finding `Kill`.
  - The row's container is gone from the turn's first `Wait`, so the delivery of the spec fails and the runner kills the container, and that kill can come between the `Wait` and `waitEnd`'s `Inspect` (a stack capture showed `deliver`'s kill in all 14 failures of 600). The test over-specified the order of calls; the runner is not at fault. The assertion still fails under the no-inspect mutant, and `-race -count=500` is clean.
- **Task 10** (12c5197; fix rounds 4e1f0ab, f40e779, aef1395, 520d33a):
  - The sockets rule is one exported function and type of `internal/deploy`, `SocketsApart(sockets string, from *Paths) error` and `Paths` (secrets, output, store, addresses, repository and engine), written once. `Check` applies it to the file's cleaned paths, and serve's start (`listenSockets`, Task 11) to the resolved paths of those that exist; a five-way "inside or holds" rule written in two packages can drift.
  - `SocketsApart` compares absolute, clean paths by their elements, not by string prefix, and skips a path of `from` that is empty or relative; an empty or relative `sockets` meets nothing, as `runner.docker.sockets`'s own refusal names it. A caller with relative paths makes them absolute first. It returns an `error`, as a nil `Problems` held in one is not nil.
  - The rule also refuses a sockets directory that holds the engine's socket, `Paths.Engine`, the path of a `unix://` `runner.docker.endpoint`, naming `runner.docker.sockets` and, in its message, `runner.docker.endpoint` (f40e779). It does not refuse a sockets directory inside the socket's directory, since the examples put `/run/agentswarm/a` beside `/run/docker.sock`. A read-only bind does not stop `connect(2)` on a Unix socket, so the engine's socket in the sandboxes' mount would hand every sandbox the engine. Serve's start refuses such a directory by its contents too (Decision 8), and this is the second, static line for the one misconfiguration that gives the host away. A row pins the socket to the configured endpoint, `/run/user/1000` against `unix:///run/user/1000/docker.sock`, rootless Docker's (520d33a).
  - The Docker block lives in `internal/deploy/docker.go` and `docker_test.go`, a pure move (4e1f0ab); `deploy.go` had grown to 1,005 lines.
  - Serve refuses a runner kind it does not run, as a switch on the kind in `load` (aef1395): until Task 11, any but `process`, naming `runner.kind`, before serve opens anything or binds a listener. At 12c5197 `Check` accepted `kind: docker` and serve had no kind check, so a Docker deployment would have bound both agents' listeners on every interface and run turns unsandboxed. Task 11 added the Docker case.
  - An unknown `runner.kind` draws `runner.kind` alone: the agents' listeners are checked by the kind, so those of an unknown kind are no longer checked as `host:port`.
- **Side fix 3** (1c8c75a; found in Task 10, fixed in its fix round):
  - `TestKillAllRemovesEveryTurnsContainerAndWaitsAtMostASecond/the_engine_answering` (Task 7) reads the removals after `synctest.Wait()` and compares those made by name alone. It had flaked 2 in 300 under `-race`.
  - The running turn's end, which `KillAll`'s removal brings, removes its container by id (`turn.go`'s end path), an extra removal that the test read before `synctest.Wait()`; `KillAll` promises its removals by name, in no order with the end's. The runner is not at fault. The test still fails under a mutant, `-race -count=500` is clean, and a `-count=100` sweep of the package's unit tests under `-race` named no other flaky test; it takes about 615 s, so it needs `-timeout 30m`.
- **Task 11** (d90a28a; fix rounds b47a7cb, 2970a9c, f4e9aeb, 4212fcc, ec19950):
  - Decision 8 gains these refusals at start (2970a9c, f4e9aeb):
    - a sockets directory that serve's effective user does not own, or that its group or others can write, naming `runner.docker.sockets`;
    - one that is not a directory, a FIFO among them, naming `runner.docker.sockets`: the directory is opened with `O_DIRECTORY`, so a FIFO no longer holds the start waiting for a writer;
    - one whose resolved path gives `mcp.sock` more than 107 bytes, naming `runner.docker.sockets`: `Check` measures the path as configured, and this is the path as resolved, which serve binds, checked before the bind;
    - an unset `runner.docker.group` that resolves to serve's own gid 0, naming `runner.docker.group`, before any engine call; `docker.Open` had refused it as `Options.Group is 0, below 1`, without naming the key.
  - The owner and mode check reads the descriptor that serve has opened and holds the flock on (`fstat`), not the path. Serve gives each socket its mode and group by its path after the bind (`os.Chmod`, `os.Chown`), which follow a link swapped in between (CWE-59, CWE-367): a local user who can write in the directory could have serve make a file of its own, a secret among them, mode 0660 and the sandboxes' group, or leave a socket of its own in serve's way.
  - Directories of mode 0750 or 0755 pass, as do the harness's and systemd's `RuntimeDirectoryMode=0750`. A plain `mkdir` under umask 002, the default for Debian's and Ubuntu's private groups, makes 0775 and is refused, and the docs say to make the directory 0750. The path and every parent must be writable by root or serve's user alone: a parent swapped between the checks and the bind, or a link in `runner.docker.sockets` re-pointed after the start (the engine resolves it again at each sandbox's creation), escapes the start's checks, and the README says so.
  - The socket path's bound is `deploy.MaxSocketPath` (107), exported and used by `Check` and by serve's start in place of a copy in `sockets.go` (4212fcc): a security bound written twice can drift.
  - `serve.go` is split into `runner.go`, `listeners.go` and `sockets.go` (b47a7cb, a pure move). It had grown to 1,197 lines. `runner.go` holds `turnRunner`, `checkSystem`, the group's resolution, the Docker runner's ports and options and the status adapters; `listeners.go` holds `listen` and the addresses file; `sockets.go` is the plan's.
  - `listenSockets` passes the engine's socket to `SocketsApart` (`Paths.Engine`), each path made absolute before its links are resolved, and places the addresses file, which does not exist at a first start, by its resolved directory.
  - The addresses file gives the sockets directory as resolved, which is where the sockets are, so the harness compares its `unix:` paths after `filepath.EvalSymlinks(SocketsDir())`.
  - A start that fails after `docker.Open` closes the runner, and a refused `Open` leaves the engine to serve, which closes it. Beyond C3, serve logs `the Docker runner did not close` (Warn, `err`) when the runner's `Close` fails, at an exit or at a failed start; a holder's lock left held then shows.
  - `serve exits at once` is logged before the kill, which `exit` makes on every path; Plan 7d-1 logged it after `StopAll`. `serve stopped`, which the harness reads, still follows every kill.
- **Side fix 4** (7c10de8; found in Task 11, fixed in its fix round 2):
  - `internal/audit/s3store`'s integration tests (Plan 6a) wait for MinIO on `/minio/health/cluster`, where they waited on `/minio/health/live`. `TestAnAnchorIsWrittenOnce` had failed once, with MinIO answering "Server not initialized yet": `/live` answers before the object layer is up.
  - `/minio/health/ready` is not enough on the pinned release: it answers 200 together with `/live`, both while S3 calls still get 503 for about 25 to 45 ms, and `/cluster` stays 503 until S3 calls are served. The tests passed on 30 of 30 fresh MinIO starts. They still cannot run with `-count` above 1, as their bucket names are fixed in the one container that `TestMain` starts.
- **Task 12** (f07c308; fix rounds 349f6ad, e00a736, 939af02, 7cda18a, cd7a02c):
  - `run.sh`'s R27 step lists the tests its `-run` pattern selects with `go test -tags e2e -list`, and fails, naming the missing ones, unless all eight tests exist (scenarios 7, 8, 9 and 16, the second-signal test and the harness's three tests of its parent). `go test` passes a `-run` pattern whose names match no test, so a misspelt or renamed scenario would have left the Linux gate silently.
  - `run.sh` removes each of its tenants' directories under `/srv/agentswarm-e2e` at its end, with `rmdir` when empty, and names and leaves one that is not. R17 removes each instance's sockets directory at the test's end, but nothing removed the tenant's, so every gate left empty directories.
  - The stubborn image's recipe notes the id its tag names before it builds and, when the build gives another, removes the old image through `build/runtime/image.sh remove ID TAG`, a new mode (cd7a02c). It does what `dev` does for its predecessor: a backup first under `.test-clean/`, never replacing a file, and no `--force`, so an image that a tag still names or a container uses stays. A cached build keeps its id; one made after a BuildKit prune would otherwise leave the old image dangling. e00a736 first inlined a copy of the removal in the Makefile, and cd7a02c replaced the copy with the mode.
  - The stubborn image's tag is the worktree's own, `agentswarm-stubborn:dev-<the first 8 hex digits of the SHA-256 of the worktree's path>`, as `DEV_IMAGE`'s is, not C15's single `agentswarm-stubborn:dev` (ce1d30b, in the final review's fix wave). With one tag for every worktree, a rebuild in one worktree after a BuildKit prune would remove, after a backup, an image that another worktree's running gate had locked by id, and no store could reload it: the harness's store is new for each test.
  - The harness's tests, `test/e2e/harness_test.go`, which had reached 1,462 lines, are split into five files along their sections (349f6ad, a pure move): `harness_test.go` (containers, toxiproxy and the database), `harness_deployment_test.go`, `harness_mcp_test.go`, `harness_parent_test.go` and `harness_vendor_test.go`.
  - An `Options.Runner` or `AGENTSWARM_E2E_RUNNER` other than "", "process" or "docker" fails `New`, naming the option or the variable; C9 leaves it open, and a typo would run the process runner and report a Docker gate green.
  - C9's other failure paths are tested with a fake `testing.TB`: a `TenantEnv` off its pattern fails `New`; under Docker, `Deployment` fails when `ImageEnv` is unset, and with `Stubborn` when `StubbornEnv` is unset; and a Docker deployment that sets `Binary` fails.
  - Scenario 7 runs with a round time limit of 9 s under Docker, and 3 s under the process runner. A launch takes up to about 2.6 s after the round opens, and the late-start rule counts a turn started in the round's last third as unstarted, so 3 s would have made the scenario flake.
  - Scenario 14 under Docker reads each container's configuration on the test's goroutine, not in a background reader, since `ContainerConfig` calls `t.Fatalf`, which only the test's goroutine may; the vendor's hold is R29's. The hold ends with its test (`TestAHeldRequestIsLetGoWhenItsTestEnds`), so that a failed scenario's held answers do not poll serve's log into the tests after it.
  - The suite runs one test at a time and no test calls `t.Parallel`: the tests of an invocation share a tenant, and a serve's start sweep removes the tenant's and the holder's containers, running or not. `main_test.go` says so.
  - The task text's `turnsStarted` is C9's `Instance.TurnsStarted`.
- **Task 13** (75eabbe; fix round 6b0f55d):
  - R22 is amended: the leader's path is 3 × `mcp.recordTimeout` + `drain` + 2 × `renewEvery`, so the defaults give max(15 + 30 + 10, 20) + 30 + 1 + 1 = 87 s, `1m27s`, not 82 s. The leader's `Stop` releases the lease within `renewEvery`, but its `Done` closes only once nothing is in flight, and a renewal or a take that succeeded just before the stop still samples the database's clock and checks the schema, each within `renewEvery`, under serve's context, which the stop does not end. A throwaway test showed `Stop` return in 7 microseconds and `Done` close 1.002 s later at a `RenewEvery` of 500 ms.
  - Decision 14's 82 s, and R21's and C3's example `1m22s`, read 87 s and `1m27s`. `TestTheWorstDrainCountsEachTerm` has `renewEvery` 4 s take 2 s away, not 1 s, and `TestServeStartedNamesTheWorstDrain` expects `1m27s`; its other rows are as the plan has them.
  - `leaderBusy` is unchanged, `takeWait + 3 × renewEvery` [20 s], and so is the leader's code. The worst drain bounds what the drain's code waits for; Decision 14 already accepts that in-flight work runs on past the stop, which the busy bound covers. A smaller number would need a change to Plan 7a's leader. `TimeoutStopSec=120` stays above 87 s.
  - Decision 14's "the wait cuts it harmlessly" never applies in the code. A take that succeeded before the stop runs `takeWait + 4 × renewEvery` from its start, but at the stop only its sample and its check remain, 2 × `renewEvery`, within the busy bound; a renewal in flight at the stop finds `held` false when `Renew` returns and samples nothing; a take in flight runs beside the gateways' drains.
  - The drain's warning `the leader did not stop in the lease's time` (Plan 7d-1) reads `the leader did not stop within its busy bound`. The take's message in `internal/deploy/deploy.go`, and the comment in `deploy_test.go` that went with it, no longer speak of the drain's wait: the message ends "a take that outlasted the lease's time would delay a takeover past its bound".
  - Two tests beyond the plan's pin what a change could have left unseen (6b0f55d). `TestServesDrainerTakesTheLeadersBusyBound` pins serve's drainer to `leaderBusy`, through a `drainer()` method moved out of `drain`; a revert to the lease's time had passed every test. `TestAStopWaitsAtMostTwoRenewalPeriodsForARenewalInFlight`, in a `testing/synctest` bubble in `internal/work/app`, pins the leader's 2 × `renewEvery`.
  - One-line comments at the bounds that `worstDrain` copies say so: the MCP gateway's `drainRecords`, `killAllWait`, `closeWait` and the leader's multipliers (6b0f55d).
- **Task 14** (a853336; fix rounds b010e43, 9185160):
  - Decision 13 gains `Slice=system.slice` on `agentswarm@.service`. Systemd puts a template's instances in `system-agentswarm.slice`, of weight 100, and Docker's systemd cgroup driver puts each sandbox in `system.slice/docker-<id>.scope`; cgroup v2 weighs siblings alone, so without `Slice=` `CPUWeight=1000` weighs serve against the other instance only, and Decision 13's own reason, that serve outweighs each sandbox at 100, fails. No test can see it, as OrbStack uses the cgroupfs driver, so the first real deployment checks it. The settings test pins the line.
  - Decision 13 gains `TimeoutStartSec=` on the two oneshots: `5min` on `agentswarm-config.service` and `1h` on `agentswarm-config-gc.service` (b010e43). A oneshot has no start timeout by default, and the timer starts nothing while a fetch still runs, so a hung fetch would stop every later one without failing, and `OnFailure=` would never fire.
  - The settings test pins both oneshots as `Type=oneshot` with `OnFailure=` and `TimeoutStartSec=`, and bans `TimeoutSec=`, which would override `TimeoutStartSec=`, `PartOf=` and `Requisite=` beside `Requires=` and `BindsTo=` (b010e43, 9185160).
  - Decision 13's "the round tries the agents again" is wrong: the coordinator starts an `unavailable` turn's agent again in the next round (`Exited` drops the turn, and `startAgain` cannot restart a turn that has ended). The spec, ADR-006 and `examples/two-instances/README.md` follow the code (ea3ddb2, Task 15's fix round).
  - The README says that serve reads `database/url` once, at its start, and both its pools keep it, so rotating it restarts the instances (b010e43): write the new URL while the old credential still works (PostgreSQL, whose role holds one password, needs two roles with the same rights), restart the instances one at a time, each while the other leads, and only then revoke the old credential. The first README (a853336) said that a rotated secret needs no restart, which is false for this one: with the old credential revoked first, both instances lose their database while serve stays up as a standby that `Restart=always` never restarts.
  - The same promise in the package doc of `internal/secrets/files` is corrected (9185160). The vendor key and the admin token are read at each use; an MCP server's credential is kept for at most a minute and read again at once when the server refuses it.
  - The README also covers a `tmpfiles.d` line for a sockets directory made outside systemd (`/run` is a tmpfs), and the images that an instance not restarted since its file changed still locks, which the store's trimming must keep.
- **Side fix 5** (1d4d806, 56579b2; found in Task 14's gate):
  - Scenario 9 (`TestAPartitionedLeaderSendsNothingAfterTheStandbysTake`) flaked under Docker: `a`'s upstream MCP session close (`DELETE /mcp`) reached the MCP server 0.5 ms after the fence. The cause is not the Docker runner's asynchronous end of a turn, as first guessed: the same race fails the process runner once the stop is shortened. The scenario answers `a`'s read 500 as it sends SIGSTOP, and a SIGSTOP takes effect only as each thread returns to user space, 0.3 to 5.2 ms later, so in 26 of 65 runs `a` read the 500 first. The upstream pool then closes the session in the background, its `DELETE` checked against the holding once and sent on SIGCONT when the stop lands between the check and the send.
  - It is rare because the SDK's 5 s close timeout equals the scenario's 5 s stop: with a 4.5 s stop the scenario failed 8 of 30 runs under Docker and 3 of 15 under the process runner, and a component reproduction failed 70 of 70.
  - The fix is the scenario's, not the product's. The harness gains `WaitStopped(t, pid, bound)` (`test/e2e/harness/stopped.go`), which waits until every thread of the stopped serve is in state `T` (`/proc/<pid>/task/*/stat`, within 1 s; a fixed 100 ms where there is no `/proc`, as on macOS), and scenario 9 calls it before it answers the read. The strict assertion stays and is now deterministic: 30 of 30 passes with the 4.5 s stop under Docker.
  - Round 2 (56579b2) passes over a thread that ends while it is read (`ESRCH`, as a missing stat file), pins the 1 s bound and the threads `/proc` lists with tests, and has scenario 9 skip the wait when its `Kill` fails.
  - ADR-004 leaves a window between a check of the holding and its send, and the scenario's assertion that nothing of `a`'s reaches the vendor or the MCP servers after `b`'s take holds because the scenario chooses where the stop lands, not a stop at an arbitrary point. Plan 7d-1's record of its Task 17 carries this as an amendment.
  - The product keeps the window: the LLM gateway's attempts run under `context.WithoutCancel` after their holding check, so an attempt paused inside it is always sent after the resume, a model call paid for after the fence whose outcome no guarded write records (a guarded write is refused in its own transaction). ADR-004 states the window but not this certainty; fencing at the receiver, which it carries to Phase 1, removes the class.
- **Task 15** (11fe384; fix round ea3ddb2):
  - The documents are the plan's list: ADR-006 (the sandbox network, the exit-code table, "As built (Plan 7d-2)", its status and History), the spec's Sections 2, 8.1, 8.2, 9.3 and 15, the roadmap, a note under Decision 12 of Plan 7d-1 and an amendment in its record of Task 17, the README and this plan's status line.
  - Beyond the list: spec Section 15 gains the leader's busy bound [20 s], which the section's own check requires; the README says that each image build leaves a BuildKit cache record of about 35 MB, and how to prune; `plan/adr/README.md` marks ADR-006 Accepted; `test/e2e/conformance_test.go` points criterion 4 at the probes; the spec's Section 8.2 says that a crashed serve leaves its sandboxes to the sweep, whose 5 minutes are fixed in code.
  - ADR-006 names the sandbox's own host mounts and the ends that count as `unavailable`, and says that unit tests alone pin the CPU, open-files and `/tmp` limits.
- **Task 16** (Part A 9f5a97e; fix round 84e61c7; 446b333 in the final review's fix wave):
  - The echo test asks for its first answer up to three times, each on a new environment, as Plan 6b's live check does, and fails, naming every miss, only when no answer holds a thinking block and text, ends by `end_turn` and calls no tool. An answer with a tool call would be refused for its missing `tool_result`, which would read as a refused echo.
  - It sends three echoes, not two: a control with the answer's text as it came, then the text emptied, then the text replaced by a space. From the gateway's records, it checks that each reached the vendor in that shape. A refused control would be the ordinary echo every nudged turn sends today, not an edge case.
  - It tries a follow-up again, up to twice, on a 408, 429, 5xx or 529 before judging it; a 4xx refusal is the verdict. Each verdict line names the gateway's attempts.
  - The rate test judges every answered call at its own rate, 4,096 ÷ its output rate, a call that stopped short of 4,096 tokens included, since that estimate can only overstate the time. A call that got no answer and took past 150 s fails the test, so that a hang cannot pass.
  - The tests' client sends each request once (`WithMaxRetries(0)`), as the runtime's does. Each test logs its usage and spend, priced at Claude Haiku 5.5's card. Both tests end a minute before `go test`'s deadline, so `make test-live`'s 30 minutes hold.
  - Part B ran on 2026-10-30, with the author's approval, on dcc1b88's image (`sha256:c52437f5…`), in the test image on this Mac, through scripts the controller had dry-run first with a fake key (the vendor's 401 at every call, no spend). Every criterion of Steps 6 to 8 passed:
    - S16 under serve with the Docker runner converged in 2 rounds and 35 s, on `shipping`/`config-change`, a hit, as on 2026-10-21 under the process runner (2 rounds, 18 s, 29 calls, 169,150 cost-weighted tokens, about $0.017). The configuration repository was rebuilt from `bench/itbench/README.md`, since 2026-10-21's was gone, so the comparison is close, not exact.
    - Six turns started, each in a container; 24 model calls, all `ok`; no `turn_unstarted` notice (3 `refused_call` notices, the agents'); no tool call failed on the platform's side, and serve logged no ERROR line.
    - `/v1/status`'s runner was `{"kind":"docker","sandboxed":true,"engine":"ok","missingImages":[]}` at serve's start, while turns ran and after the stop. Serve drained on SIGTERM and exited 0 in 1 s; a minute later no container of the run's tenant remained.
    - S16 cost 157,479 cost-weighted tokens, $0.0157.
    - `make test-live` passed its three tests. The live check passed with SDK v1.80.0 on Claude Haiku 5.5, and its record moved to 2026-10-30 (b786117). The vendor answered 200 to all three echoes, the control, the text emptied and the text a lone space, each at its first attempt, so the runtime's echoes need no change. Claude Haiku 5.5 wrote 4,096 tokens at 315.1, 325.3 and 329.3 a second; the slowest call took 13.0 s, within the round's 2m30s and the call timeout's 10 minutes.
    - The tests cost $0.0009 (the echoes) and $0.0062 (the output rate), besides the live check's own few calls; the gate's whole spend was about $0.024.
    - The vendor's key, written from the keychain to a 0700 directory as a 0600 file and mounted read-only, was in none of the 132 files the run wrote. The directory was deleted after the run, its other files backed up, the key not copied.
    - The dry run showed one behaviour worth a later plan: the gateway reads a vendor's 401 as the vendor's failure, as for an outage, so a run with a refused key waits each round's full limit rather than failing fast.

- **The final review** (8da5e97..84e61c7, by a fresh reviewer on Opus 5.5: ready to merge with fixes, 0 Critical, 2 Important, 4 Minor; fix wave a8fcd61, da3a46b, 446b333, 6639736, ce1d30b, dcc1b88):
  - Serve refuses a `runner.docker.sockets` whose resolved path differs from the path as configured, naming the path to give (a8fcd61). What serve checks and locks is then what every sandbox mounts, which the engine resolves again at each creation, so a link re-pointed after the start can no longer send a later sandbox's mount to an unchecked directory. The comparison is exact: serve's start also refuses a trailing slash, `.`, `..` or `//`, which `deploy.Check`, cleaning the path, accepts.
  - The Docker runner's and serve's Docker integration tests skip unless `AGENTSWARM_IT_RUN` is set, which `build/test/run.sh` exports in both modes (da3a46b). A plain `make test-integration` on a Linux host had failed for want of `make test-docker`'s environment.
  - `make build` builds with `CGO_ENABLED=0`, as the release does (dcc1b88), so the Mac's e2e suite drives the build that ships; `run.sh`'s own override is gone.
  - The stubborn image's tag is per worktree (ce1d30b; see Task 12), and the README names `docker rm -f agentswarm-test-<id>` for a test container a killed run left (6639736; see Task 1).
  - Its two Important findings were verifications, not defects: the containerd image store's check and the live gate. The first ran at the branch's end, on a `docker:29-dind` container the author started (Docker 29.9.2, its containerd image store): `make test-image` found one binary and one id; a build of the same commit without `--platform` gave the same id, so `--platform` leaves it unchanged on that store too; and `make linux-binary image-dev` twice replaced the dev image. On that store the engine drops the old dev image as its tag moves, before `image.sh`'s removal inspects it, so the removal prints that it cannot inspect the image, and takes no backup of its inspect (deferred: back it up before the build). The second ran as Task 16's Part B, and every criterion passed.
  - At the branch's end, shellcheck 0.11.0, which the author installed, found two warnings in the scripts: SC1007 in `build/runtime/image.sh`, for `GOFLAGS=` and `GOEXPERIMENT=` followed by a space, and SC2086 in `build/test/run.sh`, for the unquoted image list it sorts. Both were fixed as it suggests, with the same behaviour (e419094), and it is clean.

  - Carried to the next plan that touches the coordinator: `coordinator.go`'s turn lifecycle, 1,483 lines, moved to a file of its own by a pure move.

