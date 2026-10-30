# ADR-006 Agent runtime: a thin loop on the Messages API

| | |
|---|---|
| Status | Accepted. Spike C (roadmap Plan 2) decided it, and its live runs, on Claude Haiku 5.5, confirmed it. Plan 7d-1 built it as `agentswarm turn`, with a process runner for development and tests (As built (Plan 7d-1), below). Plan 7d-2 runs it in Docker sandboxes with no network, and tests criterion 4 there (As built (Plan 7d-2), below). |
| Date | 2026-10-10; revised 2026-10-12; decided and confirmed 2026-10-13 |
| Related | Spec Sections 2, 5.2, 5.6, 8.1, 8.2 and 9.3; roadmap Plans 2, 7d-1 and 7d-2; `spike/runtime/results/RESULTS.md`; ADR-001; ADR-007; ADR-013; ADR-015 |

## Context

- Third-party agent runtimes ship built-in tools for shell, files and the web. Used as they come, those tools would bypass the gateways, the budgets and the audit.
- The platform must control how context is built (redaction, data boundaries, round snapshots) and keep it bounded between rounds. In SDK-style runtimes the runtime manages much of this itself.
- Spike C tested the claims about SDK defaults in a sandbox. Canaries were planted in the working directory, the home directory, settings, auto memory and the environment.
- The platform is written in Go (ADR-015). The runtime inside the sandbox may use another language (spec Section 2.2).

## Options

- **A.** Build a full agent framework.
- **B.** The Claude Agent SDK, with the platform controlling the tool surface and egress.
- **C.** Run a coding-agent command-line tool directly.
- **D.** A thin loop on the model API: tool calls and structured output, with a step limit.
- **E.** An off-the-shelf diagnosis tool used as a runtime (ADR-001), where one can meet the criteria below.

## Decision

D: a thin loop on the Messages API, written in Go, one process per agent per round.

Spike C ran B and D in a sandbox whose only reachable endpoints were stand-ins for the two gateways. The sandbox was an internal Docker network with no gateway, checked against a listener on the host. The decision rule of roadmap Plan 2 chose D:
- D meets all seven criteria by construction, and every text in its requests comes from the platform.
- B keeps host content out of its prompts only through configuration, with at least four settings.
- Even hardened, B adds an environment block and a device id to its requests, and it refreshes its tools within a turn.

The criteria still bind any future runtime:

1. **Round-based turns.** A turn ends when the agent's `end_turn` call succeeds or the round time limit passes. Amended on 2026-10-21: the runtime calls `end_turn` when its model asks for it, and only then, since a quiet round rests on each agent's own turn end (spec Section 5.6).
2. **Gateway-only MCP.** The platform's MCP gateway is the only MCP endpoint.
3. **No reach outside.** Built-in tools that reach outside the sandbox are disabled.
4. **Egress denied by default.** Only the two gateways are reachable.
5. **No host content.** Host or workspace configuration never enters prompts.
6. **Bounded context.** The session context is kept bounded between rounds. Decided on 2026-10-19 (History): each turn starts from a fresh context, so none is kept between rounds.
7. **Tool lists change at round boundaries** only.

## Results

| Criterion | D: thin loop | B: Claude Agent SDK 0.2.165, hardened |
|---|---|---|
| 1. Round-based turns | Met. It ends at `end_turn` and at the round limit; no call starts after the limit | Met, with a `PostToolUse` hook on `end_turn` and a runner that cancels through anyio and `interrupt()` at the limit. Cancelled through asyncio instead, it kept calling for 3 s after the limit |
| 2. Gateway-only MCP | Met by construction | Met with `setting_sources=[]` and `strict_mcp_config`. Without them, the project's and the user's MCP servers and the settings hook started |
| 3. No reach outside | Met: the model sees only the gateway's tools | Met with `tools=[]` |
| 4. Egress denied | Met | Met |
| 5. No host content | Met: every text in its requests comes from the platform | Met for content with `setting_sources=[]`, `strict_mcp_config` and auto memory off; with only the first two, auto memory reached the model. Even fully hardened, it adds an identity line and an environment block, and sends a device id in its request metadata. The environment block gives the working directory, platform, shell, OS version with the host's kernel, and a git flag |
| 6. Bounded context | Met by construction: a new process each round | Met: no session persistence and a new configuration directory each run |
| 7. Tool lists change at round boundaries | Met: it lists tools once per turn | Not met: it refreshes on `tools/list_changed` within a turn |

For criterion 4, an internal Docker network alone let both candidates reach the host through the network's gateway. That was a defect of the first sandbox, not of either runtime. The sandbox now has no gateway.

Also measured, in `spike/runtime/results/RESULTS.md`:
- **Code for one diagnosis flow:** 211 lines of Go for D. B takes 111 lines of Python, including its hardening and its cancellation.
- **Request settings:** B defaults to `max_tokens` 128000 with adaptive thinking; D sends what the runner sets.
- **Release cadence over 90 days:** 46 releases of claude-agent-sdk, each bundling a new CLI; 27 of anthropic-sdk-go.
- **Live runs**, on Claude Haiku 5.5, the author's choice for cost: both candidates ended every turn with `end_turn`, 3 of 3 each, and reached the expected hypothesis in all six runs. Point 1 of the decision rule keeps both, so the decision stands. The runs were not repeated on Sonnet 5.5.
- **The desk check of E**, HolmesGPT 0.43.0: see the rejected options.

Whatever the runtime:

- the sandbox holds no model key; the LLM gateway adds it outside the sandbox (ADR-007);
- the sandbox's network has no gateway, because an internal network alone still reaches the host (spec Section 9.3);
- only tools registered with the platform are exposed;
- a lost session restarts from the current board snapshot, never from the runtime's own session store (spec Section 5.2);
- the runtime version is pinned in the runtime image, which every work item locks (spec Section 3);
- the MCP gateway holds each session's tool list until its round ends, and sends no `tools/list_changed` within a round (criterion 7, as a gateway rule).

## As built (Plan 7d-1)

Plan 7d-1 built runtime D as `agentswarm turn`, ran it as a host process, and ported the conformance suite.

**The loop** (`internal/runner`). The runtime reads its turn's spec, one JSON object, on standard input, and keeps reading it: when standard input ends, the turn ends at once. It lists its tools once per turn, gives the model each text block of a tool's result as its own, sends each of the agent's skills as a system block of its own, and holds its session tokens in memory, sending them only in request headers. Its client is built with `option.WithoutEnvironmentDefaults()` and `option.WithMaxRetries(0)`, uses no proxy and follows no redirect: the LLM gateway retries the vendor, the runtime retries no model call. It stops at 50 model calls, and asks each for at most the deployment's `runner.maxTokens` [4,096]; it keeps no token budget or prompt limit of its own, since the LLM gateway enforces the run's budget and the request's size. It ends a turn `ended` only when its `end_turn` call succeeds; an `end_turn` the gateway could not serve is tried once more, a pause of a second later, fixed in code, so that a failure of the platform's own is likelier over, unless the turn's deadline comes first, which ends the turn interrupted.

**The nudge.** When the model answers without calling a tool, the runtime sends it, once per turn, one fixed message of the platform's: "You answered without calling a tool. If you are done, call end_turn; otherwise, continue." A second answer without a tool ends the turn `finished`. The text is the platform's, so criterion 5 holds, and the LLM gateway's allowlist needs no change. A quiet round needs every agent's own turn end (spec Section 5.6), and Plan 3b's runs, without the nudge, ended 34 of 117 turns by `end_turn`. Measured live on 2026-10-21, on `claude-haiku-5-5` with Plan 3b's 13 cases and 3 trials, 117 of 117 turns ended by `end_turn`: 36 unprompted, and 81 after the nudge, 75 of them at once; none answered twice without a tool. Hits were 29 of 39, as in Plan 3b, at about the same cost. The mechanism check runs without the nudge unless given `--nudge`, so that its measurements stay comparable with Plan 3's.

**How a turn ends.** The runtime's exit code names how its turn ended, and the runner reports it as the turn's class. The LLM gateway marks every reply that is not a success with `X-Agentswarm-Outcome`, `refused`, `vendor`, `cut` or `unavailable`, and the MCP gateway answers a session gone with the JSON-RPC code -32001 and its own failures with -32002, so that the runtime tells them apart:

| Code | Class | When |
|---|---|---|
| 10 | ended | The `end_turn` call succeeded |
| 11 | finished | A second answer without a tool, after the nudge; the step limit; an answer cut at `max_tokens`, or at the model's context window (stop reason `model_context_window_exceeded`) |
| 12 | refused | A gateway refused the call; the model refused (`stop_reason: refusal`) |
| 13 | vendor | The model vendor failed, after the LLM gateway's retries |
| 14 | interrupted | The LLM gateway cut the call; the MCP session is gone; the runtime's own deadline, the end of its standard input, or SIGINT; an `end_turn` refused because the round ended |
| 15 | unavailable | A gateway could not serve the turn, or its reply had no mark; no HTTP answer at all; a second `end_turn` the gateway could not serve; SIGTERM, which only the engine or an operator sends, as at the engine's stop or restart |
| any other | crashed | A panic; an answer the runtime cannot read: malformed content, an unknown block type, an unknown stop reason, or `pause_turn`, since the runtime offers no server tools; any other error; an end by any other signal, such as the runtime's own SIGSEGV or SIGABRT; under the Docker runner, an end the engine's OOM killer made, whatever its code |
| a signal the runner did not send | unavailable | The runtime ended by SIGHUP, SIGINT, SIGKILL or SIGTERM, which its runner did not send, and not for the turn's memory: only the engine, an operator or the host sends these, as at the engine's stop or restart, an operator's `kill` or the host's shutdown. A container whose process ended so exits with 128 plus the signal's number. This runtime itself ends at SIGINT with 14 and at SIGTERM with 15; the row covers a runtime that dies by either. The process runner cannot tell the kernel's memory kill from another SIGKILL, so it reads a host process's memory kill as this row too |

Plan 7d-2 amended the table for both runners (History, 2026-10-28), so that a turn the engine, an operator or the host ended counts against no agent. The class advises only. Whether a round is quiet, and whether a wrap-up round concludes, is decided from the platform's own records: the agents' turn ends on the board, and the model calls and tool calls the gateways recorded (spec Section 5.6).

**The process runner, for development and tests only** (`internal/runner/process`). It runs each turn as a host process of `agentswarm turn`, and `serve` allows it only with `unsafeNoSandbox: true`, and only with the gateways' listeners on loopback, since they serve it plain HTTP, as Plan 7d-2 keeps them. Each turn runs in its own process group, with a minimal environment and a temporary home and working directory; its output goes to a file of its own, of at most [1 MiB]; and its standard input stays open for the turn's life, so the runtime exits when its runner does. At the deadline, and on a stop, the runner kills the process group; the runtime's own deadline, [2 s] later, is a backstop. A launch that fails is tried again, and its turn counts as unstarted; a spec the runner cannot deliver ends the turn `unavailable`, never `crashed`. It runs serve's own executable, or the deployment's `runner.binary`, as it finds that file at each turn's launch, and ignores the runtime image the item locked: an executable rebuilt in place while items run runs their later turns, and a build that reads a turn's spec otherwise crashes them. Plan 7d-2's runner runs the image the item locked. It does not meet criterion 4: a host process reaches what the host reaches, and a process that leaves its group escapes the kill. Plan 7d-2's runner implements the same port in a Docker sandbox.

**The conformance tests** (`test/e2e/conformance_test.go`). Criteria 1, 2, 3, 5, 6 and 7 are tests of the runtime run as a host process, against stand-ins for the two gateways, and one more checks that the runtime calls `end_turn` only when its model asks for it. Criterion 4, egress, needs the sandbox's network, and comes with Plan 7d-2.

## As built (Plan 7d-2)

Plan 7d-2 runs runtime D in Docker sandboxes, through a second runner of the same port, and tests criterion 4 in the sandbox on a live engine.

**The Docker runner** (`internal/runner/docker`). Serve runs it with `runner.kind: docker`, on Linux alone. Each turn is a container of the runtime image its item locked, by its id, never pulled; the image's entrypoint is the runtime, and the runner writes the turn's spec on the container's standard input, in the newest version that the image's label `io.agentswarm.turn-spec` lists and the runner writes. The coordinator hands the runner a turn under its lock, and the runner launches it afterwards, outside that lock: at most 8 launches at once, each within 10 s of getting its slot, both fixed in code, so that an engine that is slow or silent holds up no other step of the coordinator. A launch that fails is tried again while its round lasts, and its agent's board gets a `turn_unstarted` notice, once a round, that says why: `engine`, `image` or `launch` (spec Section 8.1); one the holding refused, or that a stop, a fence or a drain cancelled, posts none. The runner holds the container's standard input open for the turn's life: when serve's process ends, the engine closes it, and the runtime ends at once. At the deadline, and on a stop, the runner kills the container, and waits for nothing. How a turn ended is read as under the process runner, from the container's state: a turn whose own end the board holds is `ended`; one whose container finished at or after the turn's deadline, or by the runner's own kill, `interrupted`; one whose spec the runner could not deliver, `unavailable`; any other, as the table above says. On each of its exits, serve removes every container of its turns, waiting at most 1 s for the engine's answers, then closes the runner, which waits at most 1 s more for a minute's pass in flight. A sweep, at serve's start and every minute, removes the containers of turns the runner no longer has: at the start, every one of the instance's own, which its previous process left; later, its own that ended over a minute ago, that were made over 10 s ago and never started, or that still run a minute past their deadline and the runtime's slack; and another instance's once their round's deadline is over 5 minutes past.

**The sandbox.** Every setting is the runner's; an image's own defaults are refused, or overridden:
- no network: its only interface is `lo`, and its resolver is `127.0.0.1` alone, with no search domain; and IPC `none`, so there is no `/dev/shm`;
- the user `65532:65532`, with the sockets' group, `runner.docker.group`, as a supplementary group; a read-only root; every capability dropped, with `no-new-privileges`;
- the engine's init as PID 1, so that the container's end ends every process of the turn, a `setsid` one included;
- memory [256 MiB] with no swap, CPUs [1], processes [64] and open files [256], and an OOM score raised by 500, so that the host's OOM killer takes a sandbox before serve;
- `/tmp`, a tmpfs of [16 MiB] mounted `noexec,nosuid,nodev`, as its working directory and the only writable path besides what the kernel gives every container;
- one mount, serve's sockets directory, read-only at `/run/agentswarm`, which holds `mcp.sock` and `llm.sock`: the runtime dials them whatever the gateways' URLs say, and speaks TLS 1.3 and HTTP/1.1 there, trusting only the certificate in the turn's spec, which serve makes at its start, valid for ten years, its key in serve's memory alone;
- no environment of serve's, no health check and no logs in the engine; a stop sends SIGTERM, then SIGKILL 10 s later.

A turn can still read the kernel's version, the host paths of its mounts, and the `hosts` and `hostname` files the engine writes. None of it reaches a prompt (criterion 5), and gVisor would hide the first (Consequences, below).

**Images and their store.** `make image` builds a commit's runtime image from the commit alone: `FROM scratch`, the static `agentswarm` binary, `USER 65532:65532`, `ENTRYPOINT ["/agentswarm", "turn"]`, and labels for the spec versions the runtime reads and for its toolchain. One commit gives one image id on one engine. An image must be for Linux and the engine's architecture, list a spec version the runner writes, and hold no `Volumes`, health check or `OnBuild`, no environment but `PATH`, and no label that starts with `agentswarm.`. Serve checks that at its start for each image of `runner.images`, every minute, and at each launch, where an image that breaks it fails the launch with the reason `image`. No container holds a runtime image between rounds, so a prune of the host's images would delete them all: serve saves each image of `runner.images` to its store, `runner.docker.store`, at its start, and loads an image the engine lost back from there, with the same id, and tags it. Serve deletes nothing from the store, which a deployment backs up, since a rebuild may give another id. One build has another id on each of the engine's image stores, the classic one and containerd's, so changing an engine's image store invalidates every lock.

**Criterion 4, tested.** The Docker runner's integration tests run on a Linux engine, in `make test-docker` and `make test-linux`, with the runner's own sandbox settings and the default limits, and a probe image, curl with busybox, pinned by digest, in place of the runtime. From inside the sandbox they show that:
- its only interface is `lo`; no name resolves; and no TCP connection opens to a public address, to `169.254.169.254`, to the engine's bridge gateway, to `host.docker.internal`, or to a listener of the test's own, which a sandbox on the bridge network does reach;
- both sockets answer over TLS 1.3 and HTTP/1.1 with the spec's certificate, and a sandbox without the sockets' group cannot connect;
- only `/tmp` and `/dev/mqueue` can be written; the root and the mount are read-only; no host path is mounted but the sockets directory, so neither the engine's socket nor the secrets directory is there; and the resolver names `127.0.0.1` alone;
- the process runs as 65532, its effective, permitted, ambient and bounding capability sets all empty, with `no-new-privileges` and no `/dev/shm`; a fork past 64 processes fails; and an allocation past 256 MiB ends the container `OOMKilled`, a crash;
- an image with a `VOLUME`, a `HEALTHCHECK`, an environment beyond `PATH`, a label that starts with `agentswarm.`, or no `io.agentswarm.turn-spec` label is refused, and an image's own stop signal, user and working directory do not shape the sandbox;
- the runner's kill leaves no process of a probe that started `setsid` children; a sandbox ends when the runner's attachment ends, as at serve's death; and a container the engine stops, as `docker stop` does, or kills with a SIGKILL the runner did not send, reads `unavailable`.

Unit tests pin the CPU limit, the open-files limit and the size of `/tmp` in the container's configuration; no probe reads them from inside. These tests are the suite's host probe that the Consequences call for.

## Rejected options

- **A:** expensive, and not where the platform differs.
- **B:**
  - Keeping host content out takes at least four settings, and auto memory leaked with three. Its frequent releases can change what those settings mean without notice.
  - Even hardened, it sends an environment block and a device id.
  - It refreshes its tools within a turn.
  - It adds Python and a bundled CLI of about 240 MB to a Go platform.
- **C:** it loads project configuration automatically and updates itself, so it is the least controllable.
- **E** (HolmesGPT 0.43.0, desk-checked): an `end_turn` call from the gateway cannot stop an investigation, and it has no deadline, so it fails criterion 1 on its own. Its outward-reaching tools are switched off one by one, a deny-list that a later release can extend. It stays an external comparator (spec Section 13.3).

## Consequences

- **The platform owns the loop.** Plan 7's runner builds it test-driven, including what the SDK would have supplied: retries on model errors, prompt caching, and step and token budgets. Plan 7d-1 built it, with the LLM gateway retrying the vendor and enforcing the budgets, and the runtime keeping a step limit (As built (Plan 7d-1), above).
- **Language.** The runner is Go, like the platform (ADR-015), and the runtime image needs neither Python nor Node.
- **Context between rounds.** D starts every round from a fresh context: the full board snapshot plus the round's prompt. Spec Section 5.2 describes a session that persists across a run's rounds, with bounded context and per-round deltas. Plan 7 was to do one of two things, and Plan 7a did the first, a fresh context for each turn (History, 2026-10-19):
  - amends Section 5.2 to a fresh context per round;
  - or builds runner-managed context, keeping the agent's own exchanges within a bound, with its own test.

  B was not tested on sessions that persist across rounds.
- **The sandbox network.** An internal network whose bridge has no address on the host (`gateway_mode_ipv4=isolated`), or an equivalent default-deny, is the sandbox's egress mechanism. Plan 7d-2 chose the stricter equivalent: no network at all. A sandbox runs with `NetworkMode: none`, so its only interface is `lo`, and its only way out is serve's sockets directory, mounted read-only into it, with two Unix sockets that serve listens on: `mcp.sock` for the MCP gateway and `llm.sock` for the LLM gateway. Without a network there is no route to the host, its loopback, other sandboxes, the metadata endpoint, DNS or the internet, on any engine version and whatever the host's firewall. Over the sockets the runtime speaks TLS 1.3, so that the hop is encrypted and the gateways authenticated (spec Section 9.3), with a certificate serve makes at its start, which each turn's spec carries and the runtime trusts alone. An internal network was rejected, as the gateways would have to join it, by running serve in a container on it or through a relay container; so were host firewall rules, state outside Docker that serve would have to own. A Unix socket carries connections within one kernel alone, so the Docker runner needs serve on the containers' kernel, and serve refuses `runner.kind: docker` on any system but Linux. The Docker runner's probes are the suite's check (As built (Plan 7d-2), above).
- **The conformance test.** The spike's sandbox suite, `spike/runtime/criteria`, is the conformance test for the runner. Plan 7d-1 ported criteria 1, 2, 3, 5, 6 and 7, and Plan 7d-2 tests criterion 4 in the sandbox (As built (Plan 7d-1) and As built (Plan 7d-2), above).
- **Ending a round.** The runner kills the sandbox at the limit rather than waiting for the runtime to exit. With a call still in flight, both runtimes took about 5 s more to exit, a grace period in their client libraries. Plan 7d-1's process runner kills the turn's process group at the limit, and Plan 7d-2's Docker runner the turn's container.
- **Serve's privileges.** Under the Docker runner, serve's user is a member of the engine's `docker` group, so that it can start sandboxes. That is root on the host in effect: a compromised serve, or its account, compromises the host, the other instance and anything else that runs there. Serve already holds the vendor's key, the MCP servers' credentials and the database's URL, and it parses MCP outputs and model completions. So the platform gets a host, or a virtual machine, of its own (`examples/two-instances/`), and serve refuses a `runner.docker.group`, the sandboxes' group, of 0 or of the engine socket's group. Carried to before a team's measurement window (spec Section 14.3; roadmap Plan 10): a narrow launcher, a small process of its own that holds the engine's access and offers only "start, kill, list and remove a sandbox of the fixed profile", so that a compromised serve could neither widen a sandbox nor reach the host. Rootless Docker is the alternative to weigh then: its subgid mapping breaks the sockets' group as serve grants it today. gVisor (`runsc`) is weighed with it, and would also hide the host's kernel version from a sandbox.
- **The contract.** The platform's contract with any runtime stays "MCP tools plus the LLM gateway". The Go runner starts the runtime inside a sandbox (ADR-015).

## Reversibility

Medium. The runtime is swappable behind that contract, and the suite tells whether a candidate meets the criteria.

## Revisit when

- The loop needs a capability that would cost more to build than to adopt. Examples are managing context within a long round, and the persistent session of spec Section 5.2.
- An SDK offers a documented locked-down mode that passes the suite with its defaults.
- A second model vendor is needed (ADR-007).

## History

- **2026-10-10:** proposed: spike C to choose between the Claude Agent SDK and a thin loop.
- **2026-10-12:** rewritten in English. The seven criteria added, and off-the-shelf tools added as candidates (spec Section 16).
- **2026-10-13:** decided by spike C, provisionally: D. B refreshed its tools within a turn, and kept host content out only through configuration. The live runs were skipped; they are confirmed at the start of Plan 3. After the final review:
  - the sandbox gained an isolated gateway;
  - B's runner moved to anyio cancellation;
  - B's results were restated from measurements, including an ablation.
- **2026-10-13, later:** the live runs ran on Claude Haiku 5.5, chosen for cost. D and B each ended 3 of 3 turns with `end_turn`, so the decision is no longer provisional.
- **2026-10-19:** Plan 7a amends spec Section 5.2 to a fresh context per turn: a session's identity persists across rounds, its tokens are new each round, and the model's context does not persist. The runtime sends the tokens as headers only; no token is ever in the model's context.
- **2026-10-21:** Plan 7d-1 built the runtime as `agentswarm turn`, with the nudge, which a live probe measured at 117 of 117 turns ended by `end_turn`, and exit codes that name how a turn ended; a process runner runs it for development and tests. Criterion 1 now asks that a runtime call `end_turn` when its model does, and only then. Criteria 1, 2, 3, 5, 6 and 7 are tests of the runtime as a host process; criterion 4 moves to Plan 7d-2, with the sandbox.
- **2026-10-28:** Plan 7d-2 runs the runtime in Docker sandboxes with no network, which reach the gateways through two Unix sockets over TLS 1.3, the stricter equivalent of an internal network that the Consequences allow. The author accepted that serve's user is in the engine's group, root on the host in effect, on a host of the platform's own, and carried a narrow launcher to before a team's measurement window. The exit table reads an end by a signal the runner did not send as `unavailable`, for both runners, and the runtime ends at SIGTERM with 15. Built on 2026-10-29 and 2026-10-30, with criterion 4 tested in the sandbox (As built (Plan 7d-2)).
