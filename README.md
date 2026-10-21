# agent-swarm

Status: the platform specification was approved on 2026-10-12. Plan 1 (the toolchain and the core of the blackboard) is implemented. Plan 2 (the runtime spike) chose the agent runtime (ADR-006), and live runs confirmed it. Plan 3a built the mechanism check's data and scorer; Plan 3b built the retrospective check command, and the mechanism check passed on 2026-10-14. On 2026-10-14 the author decided to build the rest of Phase 0 before a team adopts the platform (ADR-016); Plan 4 built the registry, Plan 5a the MCP gateway's read path, Plan 5b its board tools and record-only proposals, Plan 6a the audit chain, Plan 6b the LLM gateway, Plan 7a the coordinator's core, and Plan 7d-1 `agentswarm serve`, the agent runtime and a fault-injection harness; Plan 7d-2, the runner in sandboxes, is next.

## Documents

- [Platform specification](plan/2026-10-11-agent-swarm-platform-spec.md) (approved)
- [Example deployment profile](plan/2026-10-13-example-deployment-profile.md) (fictional; each team keeps its real profile outside this repository)
- [Phase 1 write path: design backlog](plan/2026-10-11-phase-1-write-path-backlog.md) (not normative; designed after the Phase 0 gate)
- [Architecture decision records](plan/adr/README.md)
- [Implementation roadmap](plan/2026-10-12-implementation-roadmap.md) (approved)
- [Plan 1: foundation and the blackboard](plan/2026-10-12-plan-1-foundation-and-blackboard.md) (executed)
- [Plan 2: runtime spike](plan/2026-10-13-plan-2-runtime-spike.md) (executed)
- [Plan 3a: mechanism check data and scoring](plan/2026-10-13-plan-3a-mechanism-check-data.md) (executed)
- [Plan 3b: the mechanism check's agent runs](plan/2026-10-14-plan-3b-mechanism-check-runs.md) (executed; the mechanism check passed)
- [Plan 4: the registry](plan/2026-10-14-plan-4-registry.md) (executed)
- [Plan 5a: the MCP gateway's read path](plan/2026-10-14-plan-5a-gateway-read-path.md) (executed)
- [Plan 5b: the MCP gateway's board tools and proposals](plan/2026-10-15-plan-5b-gateway-board-and-proposals.md) (executed)
- [Plan 6a: the audit chain and its anchor](plan/2026-10-15-plan-6a-audit-chain.md) (executed)
- [Plan 6b: the LLM gateway](plan/2026-10-17-plan-6b-llm-gateway.md) (executed)
- [Plan 7a: the coordinator's core](plan/2026-10-18-plan-7a-coordinator-core.md) (executed)
- [Plan 7d-1: `serve`, its clocks and timers, and a fault-injection harness](plan/2026-10-20-plan-7d1-serve-and-harness.md) (executed)

## Implementation

Go, with domain-driven design and interface-driven ports and adapters. See Section 2.2 of the platform specification.

Build and test with `make build`, `make test`, `make test-integration` (needs Docker), `make test-e2e` and `make test-live`. `make test-live` plays one turn of the agent runtime through the LLM gateway against the model vendor: it needs `AGENTSWARM_LIVE_KEY`, the vendor's key, and costs a few cents. Run it, and commit `internal/llm/endpoint/testdata/live-check.json`, whenever the anthropic-sdk-go version changes (`make test` fails until then), and before agents switch model (set `AGENTSWARM_LIVE_MODEL` to the new model).

`agentswarm registry validate --dir DIR` checks a configuration repository against every loader rule; `examples/config/` is a fictional team's repository. `agentswarm registry hash-tools --endpoint URL` prints each of a server's tools with the whole definition that a server registration pins, and its schema hash; `--ca FILE` trusts a CA bundle besides the system roots.

`agentswarm migrate` applies the database's migrations to the database the libpq environment variables name (`PGHOST` and the rest), or to the one `--secrets DIR --database REF` names. Migrations are expand-only: each records the oldest build that can read the schema it leaves, and one that older builds cannot read is refused while an instance holds the lease, unless `--force` is given. `agentswarm serve --deployment FILE` runs one instance of the platform: the coordinator, the leader, which holds the platform's lease, both gateways, and the admin API on loopback, with the deployment file of Plan 7d-1's Contract C1. Of several instances, one leads at a time, and the others stand by. SIGINT or SIGTERM drains it under the lease it holds; a second signal stops every turn and exits at once. Its runner runs each turn as a host process of `agentswarm turn`, the agent runtime, which reads its turn's spec on standard input; it runs turns unsandboxed, so serve allows it only with `runner.unsafeNoSandbox: true`, for development and tests. `agentswarm inspect --deployment FILE item ID`, `board RUN` or `calls RUN` prints, as JSON lines, what the database holds of an item, a run's board or a run's calls.

Every container a test starts carries the label `agentswarm.project=agent-swarm`. A test run that is killed leaves its containers behind: `make test-clean` removes this project's containers that have run for over an hour, after it saves each one's `docker inspect` output and its labelled data under `.test-clean/`, as `<id>-<UTC start>.inspect.json` and `<id>-<UTC start>-data.tar`, which no later run replaces. Run it between test runs, as it cannot tell a run still going from one that was killed; `go run ./test/testclean -dry-run` shows what would go. `make test-compat` is the test against the previous release: it runs that release's harness scenarios 1, 2 and 8, from a worktree at its tag, against a schema this build migrated, then has this build verify the audit chain and inspect each item they left. The previous release is the latest release tag in HEAD's history, such as `v0.1.0`; a pre-release such as `v0.2.0-rc.1` does not count. It needs Docker, and backs up its Postgres container under `.test-clean/` before it removes it. Until a release is tagged, it prints `no release tagged; nothing to check` and passes, and at the first release's own commit it prints `no release before <tag>; nothing to check`. It checks a promise, that the previous release can read the schema this build leaves, which only migrations an older build can ignore may make: when the schema's `min_reader` is above the previous release's level, its last migration's number, it prints `<tag> cannot read the schema this build migrated (its level <level> is below the schema's min_reader <min_reader>); deploy in Decision 18's order: stop the older instances, migrate, then start the new build; nothing to check` and passes without running the scenarios.
