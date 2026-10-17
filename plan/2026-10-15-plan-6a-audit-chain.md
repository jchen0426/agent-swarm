# Plan 6a: The Audit Chain and Its Anchor — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Approved on 2026-10-17; executed inline on main |
| Date | 2026-10-15 |
| Roadmap | `2026-10-12-implementation-roadmap.md`, Plan 6, first half |
| Decided by the author | On 2026-10-14: Phase 0 is built before a team adopts the platform, on the assumptions ADR-016 records |

**Goal:** Make the audit records tamper-evident (spec Section 9.4): every audited row is hash-chained in the transaction that inserts it; the anchor store's owner writes the chain's head outside the database every hour; and a verify command shows a change, a deletion or a restore. Also close the gap Plan 5b's final review left open: an agent that cites its own words through a read that repeats a hidden argument.

**Architecture:** Plan 6 is split like Plan 5. This first half builds the audit chain; Plan 6b builds the LLM gateway, whose records join the chain.
- **The chain.** Migration `0005` adds `audit_chain`. A trigger on each of six audited tables appends one record for each inserted row, under a per-tenant lock, at read committed only. The six tables are the board's entries, tool calls, their outcomes, hidden tools, configuration revisions and their objects. A record names its row by table and key. It holds the digest of the row's columns, as the database renders them, and a hash that links it to the record before.
- **The domain.** The new Audit domain package `internal/audit/chain` holds the link hash, the one canonical form of an anchor, and the verification walk.
- **The use cases.** The new Audit application `internal/audit/app` anchors and verifies through two ports:
  - `Chain` reads one database snapshot. Its adapter is in `internal/audit/postgres`.
  - `Anchors` is the store outside the database. Its adapters are an S3 bucket with object lock, in `internal/audit/s3store`, and a directory for development, in `internal/audit/dirstore`.
- **The commands.** `agentswarm audit anchor` and `agentswarm audit verify` run them. The store's owner runs both, with its own credential and a database role that can only read.
- **The gateway.** It shows a hidden argument value that an agent's read repeats in its output as the stored arguments show it.

**Tech Stack:**
- Go 1.26
- pgx v5.10.0
- github.com/minio/minio-go/v7 v7.3.0, new; released 2026-08-15
- testify v1.12.1
- testcontainers-go v0.44.0, with Postgres 17 and MinIO `RELEASE.2024-12-18T13-15-44Z`, pinned by digest

**Spec:** `plan/2026-10-11-agent-swarm-platform-spec.md`. Read these before the tasks that implement them:
- Section 2, the audit and trajectory store;
- Section 3.3, the paragraph on echoes;
- Section 8.4, restore;
- Section 9.1, 9.3 and 9.4: threats, gateways and secrets, redaction and audit integrity;
- Section 15, the anchor interval of [1 h].

Also read ADR-016 (the assumption for profile question 14), and the end of Plan 5b (`2026-10-15-plan-5b-gateway-board-and-proposals.md`, "Deferred minor findings").

**Reviewed before it was written:** four fresh reviewers on Opus 5.5 read it before this plan was written, as Plans 4, 5a and 5b teach.
- **The first** read the design, the spec and the code. It tested its claims on Postgres 16, 17 and 18 and on the gateway's own redaction. It found two Critical issues, five Important and seven Minor. The worst three:
  - After a restore, verification would fail on every later run and stop naming what was lost.
  - The chain's start could be deleted without a trace.
  - The platform's own credential for the anchor store would let its operators write there, which spec Section 9.4 forbids.
- **The second** read the built code, against the spec and the first review. It found no Critical issue, four Important ones and nine Minor ones, and three tests that passed for the wrong reason. It backed each with a probe on Postgres 17 and MinIO. The four Important ones:
  - White space beyond ASCII between the words of a hidden value let a repeat slip past the gateway's rules.
  - A long hidden value cost time that grew with its square: one call could hold the gateway for minutes.
  - After a restore, the row counts of anchors the restore lost were compared with records written since, so verification reported rows written unchained that never were.
  - Tampering after a restore merged into the restore's own finding.
- **The third** read only the second review's fixes, looking for what they broke. It found three Important issues, two of them caused by the second review's fixes, and four Minor ones, each with a probe:
  - The gateway folded a long hidden value again for every string of an output, so one call over an output of numbers still cost seconds.
  - A base was the latest anchor the chain still held, so an hour after any rewrite, its finding read as a restore.
  - Another trigger or a rule could change or drop a row before it was chained, and verification did not look.
- **The fourth** read only the third review's fixes. It found three Important issues, each left by the third review's fixes, and four Minor ones, each with a probe:
  - After a restore far back, the anchor job read every later anchor inside its database snapshot with no statement between, so the database ended the idle transaction after a minute, and anchoring stopped for good.
  - One record of a source no audited table has, as an operator can append by hand, made every later anchor unreadable, so verification set them all aside.
  - A trigger that took the chain trigger's name passed both table checks, so one that rewrote each row before chaining it went unseen.

  Its findings were fixed and checked by test and by mutation; no fifth review read the fixes, so the final review after execution reads them first.

Every finding is answered below: by a decision with its test, by a ruling, or by a constraint on a later plan.

**Verified:** Every code block in this plan was compiled, tested and linted in a scratch copy. It was then replayed, task by task, in a fresh clone of 55527af:
- each task's tests fail before its implementation exists, and pass after it;
- `golangci-lint run ./...` stays at `0 issues.` after every task;
- the end state matches the scratch copy, file for file;
- `make lint test-all` passes on the end state.

Each rule a review asked for was also checked by mutation, 45 in all: with the rule removed from the code, the code still builds and the test that names it fails.

## Global Constraints

- **Module and Go version.** The module path is `github.com/jchen0426/agent-swarm`, with `go 1.26.0`.
- **Vocabulary.** Names come from the spec: audit chain, record, anchor, anchor store, verification, finding, run, board, entry. Never "employee".
- **Domain purity.** `internal/audit/chain` imports no database, network or framework package.
- **Ports.** `Chain`, `View` and `Anchors` are declared by their consumer, in `internal/audit/app`. Their adapters are `internal/audit/postgres`, `internal/audit/s3store` and `internal/audit/dirstore`. Wiring lives in `cmd/`.
- **Records.** `audit_chain` is append-only, and the retention job may not delete from it either. The audited tables keep their guards. Migration `0005` adds no column to any audited table.
- **Isolation and locks.**
  - Audited rows are inserted at read committed only; every connection `db.Open` makes defaults to it.
  - A transaction takes the chain lock after any other lock it holds, and an audited insert is the last thing it does before it commits.
- **Libraries.** One new module: `github.com/minio/minio-go/v7` v7.3.0. It raises `golang.org/x/net` to v0.58.0 and `golang.org/x/text` to v0.41.0; both were released in August 2026.
- **Lint.** Every task ends with `golangci-lint run ./...` at `0 issues.`.
- **Fuzzing.** Fuzz runs pass `-fuzzminimizetime 0` (Plan 5b, "Rulings").
- **Docker.** The integration tests of Tasks 4, 5 and 6 start containers (Postgres, MinIO), so Docker must run.
- **Language.** Code, comments and documents are in English. Test sources that need `\u` escapes are written with the escape, never the character itself.

## Decisions this plan makes where the spec is silent

Review these first. Each is pinned by the named tests. Decisions 5, 7, 8, 9 and 13 amend the spec, in Task 7; approving this plan approves those amendments.

1. **One chain per tenant, written by a trigger.** Each audited table has an `AFTER INSERT` trigger, `<table>_chain`. It appends one record to `audit_chain` in the inserting transaction:
   - it takes `pg_advisory_xact_lock` on the tenant's chain, held to commit;
   - it reads the tenant's last record;
   - it inserts seq + 1, with prev set to the last record's hash, or 32 zero bytes for seq 1.

   Records therefore commit in the order of their seq, and a rollback takes its records with it, so no gap is left. The chaining time is read once the lock is held, so it never goes back. No writer in Go changes, and no code path can insert an audited row without its record, short of turning triggers off, which verification finds (Decisions 9 and 10). The trigger fires after the foreign-key check, so a missing call fails as a foreign-key error before any record is written. (`TestEveryAuditedRowIsChained`, `TestRacingWritersLeaveOneContiguousChain`)
2. **A row's digest** is SHA-256 of the UTF-8 text of one JSON array of the row's columns, in a fixed order for each table and form. Times are integer microseconds since the epoch, so the session's time zone and date style never change the text.
   - The trigger renders the row from `NEW`.
   - Verification sends the same expressions itself, receives the rendering as UTF-8 bytes, and hashes it in Go. It never calls a function stored in the database it checks.

   (`TestTheDigestIgnoresSessionSettingsAndEncodings`)
3. **Forms.** A record holds its form. A later migration that adds a column to an audited table adds form 2 for that table, and verification knows both forms. Without forms, `ADD COLUMN ... DEFAULT` would change the rendering of every older row. Every table starts at form 1. A record of a form this build cannot render is a finding. (`TestVerificationReportsEveryKindOfFinding`)
4. **The link hash** is SHA-256 over fixed-width parts, so that no field can run into the next: the hash before, the seq, the hashes of the table, the key and the run, the form, the digest, and the chaining time in microseconds. The trigger computes it with `sha256`, `int8send` and `int2send`; verification recomputes it in Go. (`TestTheLinkHashCoversEveryField`, `TestFieldsCannotRunIntoEachOther`, `TestTheLinkHashIsTheDatabasesEncoding`)
5. **What is chained.** The six tables, each named by its primary key as a JSON array:

   | Table | Key | Run |
   |---|---|---|
   | `board_events` | `[run_id, seq]` | its run |
   | `tool_calls` | `[call_id]` | its run |
   | `tool_call_outcomes` | `[call_id]` | its call's run |
   | `tool_hides` | `[endpoint, tool, pinned_hash, registration]` | none |
   | `config_revisions` | `[commit_id]` | none |
   | `config_objects` | `[commit_id, kind, name]` | none |

   Configuration is chained because spec Section 9.4 keeps configuration records. Git keeps each commit, but not when the platform loaded it. Rows that exist when the migration runs are chained first, in the order they were written; the migration locks the six tables against writes until it commits, so that no row slips in between those rows and the triggers. (`TestEveryAuditedRowIsChained`, `TestChainingWhileWritersRunLeavesNoRowUnchained`)
6. **Read committed only.** The append raises unless its transaction is at read committed. At repeatable read or serializable, a transaction's snapshot predates its wait for the lock. It would read a stale last record and fail on a duplicate seq: the first review measured 723 failures in 800 inserts. Raising names the cause instead. `db.Open` sets every connection's default to read committed, and ends a transaction left idle for a minute, which would hold the lock. (`TestAuditedRowsAreWrittenOnlyAtReadCommitted`, `TestOpenReadsCommittedAndEndsIdleTransactions`)
7. **Records are never deleted.** `audit_chain` has its own guard, with no retention escape. Verification requires the chain's first record to be seq 1 with a zero prev; otherwise the records before it were deleted (`truncated`). Plan 10 adds pruning with chained prune records. (`TestTheChainsStartCannotBeDeleted`, `TestAChainThatDoesNotStartAtItsFirstRecordWasCut`)
8. **The anchor store's owner takes the anchors.** Spec Section 9.4 asks for a store "that the platform's operators cannot write". So the platform holds no credential for it, and no platform code writes an anchor.
   - The store's owner, outside the builders, runs `agentswarm audit anchor` every hour, with its own credential for the store and a database role that can only read.
   - The first review showed why the platform cannot write anchors itself. A credential that can create objects lets an operator:
     - date anchors as it likes;
     - add an anchor with an enormous seq, naming the runs whose incidents it wants excluded from measurement;
     - leave an object that never parses;
     - claim an anchor's name ahead of the job.

   Task 7 states this in spec Section 9.4 and in ADR-016's assumption for question 14.
9. **An anchor** holds:
   - the chain's head: seq, hash and chaining time;
   - its base: the latest anchor dated before it whose record the chain still holds at its seq, and that anchor's seq;
   - the runs of the records since the base;
   - how many rows each audited table holds, beside how many records the chain holds of it;
   - the audited tables whose state verification could not trust then (Decision 10's `table`), so that a trigger that comes and goes between two verifications still shows.

   All of it is read from one read-only snapshot. The job reads anchors newest first, one version of each name, and only until it finds the base, so a year of hourly anchors costs one read an hour. Once it has read the newest, it passes over, unread, every anchor whose name puts its seq past the chain's head, since the chain cannot hold it: after a restore far back, reading a year of them inside the snapshot, with no statement between, would let the database end the idle transaction, and anchoring would stop for good.
   - **The base.** Normally it is the anchor before. After a restore, the anchors between the base and the new anchor are exactly those whose records the restore lost. An anchor at a seq the chain has written again, with other records, is lost, not a base.
   - **The counts.** Rows beyond records show rows written with the trigger off, even once they are deleted, and whatever the chain holds later: both counts are the anchor's own (`unchained_rows`). Only records of the audited tables are counted, so that a record of any other source, as an operator can append by hand, never makes an anchor unreadable; verification reports that record (`unknown_form`).
   - **Written only if it reads back.** The job decodes each anchor before it writes it, and fails rather than write one that verification would set aside. An anchor too large to read back, over 4 MiB, fails the job in the same way, and verification then shows that anchoring stopped.
   - **Clocks and restores.** Anchors dated at or after the job's own time are passed over, so a base is always earlier than the anchor that names it. The job refuses to anchor an empty chain once records were anchored, as while a restore is under way: the first anchor after the restore must see its losses.
   - **Every hour.** An anchor is taken every hour, also when nothing happened, so that a quiet hour is told apart from anchoring that stopped.
   - **Its form.** An anchor has one canonical form, compact JSON, and its object's name is `<tenant>/<time to the second>-<seq>.json`.

   (`TestAnAnchorHoldsTheHeadAndTheRunsSinceTheAnchorBefore`, `TestAnAnchorAfterARestoreNamesTheLatestAnchorTheChainHolds`, `TestAnAnchorCountsTheRowsAndRecordsOfEachTable`, `TestAnAnchorReadsOnlyTheAnchorsItNeeds`, `TestAnAnchorPassesOverAnchorsPastTheHeadUnread`, `TestAnAnchorReadsOneVersionOfEachAnchor`, `TestAnAnchorPassesOverAnchorsDatedAfterNow`, `TestAnAnchorRefusesAnEmptyChainOnceRecordsWereAnchored`, `TestAnAnchorThatWouldNotReadBackIsNeverWritten`, `TestARogueRecordLeavesAnchorsReadable`, `TestAnAnchorRoundTrips`, `TestDecodeRefusesAnythingButAValidCanonicalAnchor`, `FuzzDecodeNeverFails`)
10. **Verification** lists every object and version in the store first. It then reads the chain and the tables in one read-only snapshot at repeatable read:
    - with the search path set to the system catalog only, every table named with its schema, and any table of an audited table's name in another schema reported, so that nothing stands in for the platform's tables;
    - with the connection's text in UTF-8, so that its encoding never changes what is hashed;
    - with joins that hash rather than loop, so that a whole chain is read in time linear in its size, also before its tables have statistics.

    Listing first means an anchor taken meanwhile never names a record the snapshot lacks. It reports these findings:
    - `broken_link`, `gap`, `truncated`: records that do not link, are missing, or were cut from the start;
    - `changed_row`, `missing_row`, `unchained_row`, `unknown_form`: rows that differ from their records, rows gone, rows no record chains, and records of a form this build cannot render;
    - `unchained_rows`: an anchor counted more rows of a table than the chain then held records of;
    - `lost`: anchors the chain no longer held when a later anchor was taken, between that anchor's base and it. Each such later anchor makes one finding, with the seq range and the runs whose records went, and the finding stays after new writes pass the lost anchors. A restore, a deletion and a rewrite look alike here; only the store's owner can tell which (Plan 10);
    - `diverged`: each other anchor the chain no longer holds, which no anchor taken since has seen. Tampering after a restore therefore shows apart from it: as `diverged` until the next anchor, then as a `lost` finding of its own;
    - `anchor_gap`, `anchor_stale`: anchors more than the interval plus 10 minutes apart, or none that recent;
    - `anchor_conflict`, `anchor_hidden`, `bad_anchor`: versions of one key that differ, delete markers, objects that are no anchor of the tenant under their own name, an anchor whose base is no earlier anchor in the store, and an anchor dated more than 10 minutes after now, as a host whose clock ran ahead writes it. Such an anchor's seq and hash still count, so the records it alone holds still show when they go; it just never counts as having seen other anchors lost, nor as the last time anchoring happened. Two anchors of one seq may hold two hashes, since a restored chain writes that seq again;
    - `store_unlocked`: a store, or an object in it, that is not kept write-once;
    - `table`: an audited table that is not a plain table, is filtered by row security, has a namesake in another schema, has another trigger or a rule acting on its inserts, which could change or drop a row before it is chained, or does not chain what is inserted into it; now, or when an anchor was taken. The chain trigger is known by its name, its kind, after each inserted row, and its function, so that a trigger that takes its name and rewrites each row before chaining it shows.

    (`TestARestoreShowsWhatWasLost`, `TestTamperingAfterARestoreShowsApartFromIt`, `TestARestoreFromABackupShowsWhatWasLost`, `TestAnAnchorWithoutItsBaseShows`, `TestRowsAnAnchorCountedButTheChainNeverHeldShow`, `TestVerificationFindsWhatWasDoneBehindThePlatformsBack`, `TestRowsWrittenUnchainedShowEvenOnceDeleted`, `TestTablesVerificationCannotTrustShow`, `TestATableOfTheSameNameInAnotherSchemaShows`, `TestATriggerOrARuleOnInsertsShows`, `TestAChainTriggerImpostorShows`, `TestTablesUnsafeWhenAnAnchorWasTakenStillShow`, `TestVerificationReportsAnAnchorDatedAfterNow`, `TestAnAnchorDatedAheadStillCountsAsEvidence`, `TestCheckAnchorsFindsWhatTheStoreHoldsWrong`, `TestStoppedAnchoringShows`)
11. **The anchor stores.**
    - **S3.** An S3 bucket with object lock is the store ADR-016 assumes. Its owner enables object lock with a default retention in compliance mode, longer than any record is kept. An anchor is put on condition that its name is free (`If-None-Match: *`), with its MD5, which a locked bucket requires. Verification lists every version and delete marker, since an unconditional put adds a version and a delete without a version adds a marker; a listing without versions shows neither. It lists keys and versions without their bodies, and reads a body or a retention only when asked. Verification checks the bucket's default retention and, in a locked bucket, each object's own; a bucket without object lock is listed all the same and reported. HTTPS is required, except to a loopback host. Its two secret references resolve at use (spec Section 9.3). The owner's job needs the rights to put objects, list versions, read them and their retention, and read the bucket's object lock.
    - **A directory.** It serves development and tests, and verification always reports it as `store_unlocked`.

    (`TestAnAnchorIsWrittenOnce`, `TestTheListingShowsSecondVersionsAndDeleteMarkers`, `TestOnlyComplianceRetentionIsWriteOnce`, `TestAVersionWithoutComplianceRetentionIsNotLocked`, `TestABucketWithoutObjectLockIsListed`, `TestPlainHTTPIsForALoopbackHostOnly`, `TestTheAnchorStoreIsNamedByAURL`)
12. **The commands.**
    - **The database.** The commands reach it with `--database-url`, or with the libpq environment variables when it is empty, so no password needs to be on a command line. `--schema` names the schema that holds the platform's tables.
    - **The store.** It is a URL: `s3://BUCKET/PREFIX?endpoint=HOST&key=REF&secret=REF` with `--secrets DIR`, or `file:///DIR`.
    - **The output.** Both commands print JSON. Findings exit with 3, an error with 1 and bad flags with 2, so that a verification that could not run never looks like one that ran. A tenant id that cannot name anchors is refused before any connection.
    - **The schedule.** A missed hour stays a finding, so the owner's scheduler runs `audit anchor` every hour and retries within ten minutes.

    (`TestAuditAnchorThenVerify`, `TestTheAuditCommandsNeedATenantAndAStore`)
13. **A hidden argument that an agent's read repeats** (Plan 5b's deferred minor M2). The board's echo check refuses a quote that lies within one of the call's stored argument values. Stored arguments are redacted, though. A value that a path rule replaced whole is invisible to the check, and to raters, who see a call's arguments beside its output. So a read tool that repeats such a value let an agent cite its own words.
    - **The rule.** In an agent's read, the gateway now shows each string, key and number of the arguments that redaction changed, wherever the output repeats it, as the stored arguments show it.
    - **How a value is found.** One literal rule per value runs before the tool's own rules, so the value is replaced whole before another rule can break it up. It matches in any case, with any run of white space where the value has one, white space beyond ASCII included, as quotes are compared. It also matches the value's spellings in JSON: escaped by default, with every character beyond ASCII escaped, and with slashes escaped too. So an honest repeat never turns the read into a read error.
    - **In linear time.** A literal is matched as text, not as a regular expression. Each value is folded once, when its rule is made: to upper case and then lower, so that the long s and the Kelvin sign meet their plain forms, with each run of white space as one space. Each text is folded once per rule and keeps where each of its bytes came from, and a text too short to hold a value is never folded. A rule's spellings are matched in one pass from the left, the longest where two start together; where one spelling's match overlaps another's occurrence, the search for the other goes on after the match, so that no occurrence is lost. A regular expression in any case costs time that grows with the square of a long value, and folding a value again for each string of an output cost seconds per call: either let one call hold the gateway for minutes.
    - **Measured folded.** A value that folds to fewer than 4 bytes is left alone, so that a value that only looks long, as "o" and the Kelvin sign fold to "ok", never replaces every "ok" in an output.
    - **Limits.**
      - A value that folds to fewer than 4 bytes is left alone: replacing it would mangle the output, and it cannot carry a claim.
      - More than 32 such values refuse the call, which bounds the redaction's work.
      - The platform's own reads are left alone: nobody can cite them, and a write call's candidate check compares what they hold.
    - **Effects.** The repeat is no longer in the stored output, so no quote can carry it as the agent wrote it, and the value no longer leaks through the output. The read stays citable for the rest of its answer.
    - **Why not refuse instead.** Refusing every quote from a read whose arguments redaction changed would have made a search whose query a rule hides useless as evidence, since search results contain the query.

    (`TestAnOutputThatRepeatsAHiddenArgumentShowsItAsStored`, `TestEvidenceCannotQuoteARepeatedHiddenArgument`, `TestARepeatIsFoundInAnyCaseAndSpelling`, `TestALongRepeatedHiddenArgumentCostsLinearTime`, `TestAShortHiddenArgumentIsLeftInTheOutput`, `TestShortValuesAreMeasuredInBytes`, `TestShortValuesAreMeasuredFolded`, `TestRepeatsAreBounded`, `TestAPlatformReadKeepsAHiddenArgumentItRepeats`, `TestChangesNamesEveryStringKeyAndNumberRedactionChanges`, `TestBeforePutsRulesFirst`, `TestALiteralIsFoundInAnyCaseAndSpacing`, `TestALiteralFoldsCaseBeyondASCII`, `TestALongLiteralIsMatchedInLinearTime`, `TestManyShortStringsWithALongLiteralCostLittle`, `TestOverlappingSpellingsAreAllReplaced`)

## Assumptions taken for the team (ADR-016)

| Onboarding question | What this plan assumes | What a different answer costs |
|---|---|---|
| 14: the store that anchors the audit chain | An S3 bucket with object lock in compliance mode. Its owner, outside the builders, runs `audit anchor` hourly and `audit verify`, with its own credential and a database role that can only read the platform's tables | An owner who cannot run a job would leave the platform to push anchors with a credential its operators hold, which spec Section 9.4 forbids; that needs an ADR and a weaker verification (Decision 8) |
| 13: the deployment | Postgres 17, where `sha256()` and `int8send()` exist; the role the platform runs as does not own its tables; the platform's database holds its tables in one schema, and nothing else of their names | A role that owns the tables can turn the chain trigger off, or add a trigger or a rule; verification shows the trigger and the rule, now or as an anchor recorded them, and the rows written meanwhile once an anchor counted them, but not a trigger, or rows, that come and go between two anchors |

## Review Focus

These are the inputs most likely to bite a person using this software. Each has a test in the task that owns the code.

1. **A restore or a failover that loses records.** Verification names what was lost and the runs that lost records, and keeps naming them after new writes pass the old anchors; tampering afterwards shows apart, and what the lost anchors counted is never compared with the new records. (`TestARestoreShowsWhatWasLost` and `TestTamperingAfterARestoreShowsApartFromIt` in Task 2; `TestAnAnchorAfterARestoreNamesTheLatestAnchorTheChainHolds` in Task 3; `TestARestoreFromABackupShowsWhatWasLost` in Task 5)
2. **Writers at once, under any default isolation.** The chain stays contiguous, or an insert fails and says why; it never forks. (`TestRacingWritersLeaveOneContiguousChain`, `TestAuditedRowsAreWrittenOnlyAtReadCommitted` and `TestOpenReadsCommittedAndEndsIdleTransactions` in Task 5)
3. **An operator who works behind the platform's back**: rows changed, deleted or inserted with the triggers off, the chain's start deleted, rows written unchained and then deleted, a table that row security filters, a trigger or a rule that changes or drops inserts, even one that takes the chain trigger's name, a record appended by hand. Each shows, and none stops anchoring. (`TestVerificationFindsWhatWasDoneBehindThePlatformsBack`, `TestTheChainsStartCannotBeDeleted`, `TestRowsWrittenUnchainedShowEvenOnceDeleted`, `TestTablesVerificationCannotTrustShow`, `TestATriggerOrARuleOnInsertsShows`, `TestAChainTriggerImpostorShows` and `TestARogueRecordLeavesAnchorsReadable` in Task 5)
4. **Text a session renders its own way**: time zones, date styles, a client encoding other than UTF-8, escapes, characters beyond ASCII, JSON inside strings. Digests never change with them. (`TestTheDigestIgnoresSessionSettingsAndEncodings` in Task 5)
5. **An agent that writes its claim into a hidden argument of a read that repeats it**, in any case, white space or spelling, however long. It cannot cite it, and cannot tie the gateway up with it. (`TestARepeatIsFoundInAnyCaseAndSpelling`, `TestEvidenceCannotQuoteARepeatedHiddenArgument` and `TestALongRepeatedHiddenArgumentCostsLinearTime` in Task 1)

## Not in this plan

- **Plan 6b:** the LLM gateway: its allowlist, budgets, cost attribution and prompt logging into the chained store.
- **Plan 7:** the runs, items and incidents, and which of their records are audited; `serve` and `migrate`.
- **Plan 10:**
  - pruning with chained prune records, at anchored boundaries;
  - letting the store's owner acknowledge a `lost` finding as a restore, so that verification turns clean again while still listing the losses;
  - the backup and restore drill;
  - the load trial of the chain lock.

## Constraints carried to later plans

- **Plan 6b:** chain the LLM gateway's tables from their first migration, each with a trigger `<table>_chain` and form 1, and add them to verification's sources and the anchor's counts.
- **Plan 7:**
  - Records of items and incidents, and the platform reads made when an item opens, carry their item and incident into the chain, and anchors name incidents as well as runs: a restore can lose a run's own record, and an item-open read has no run.
  - Run migrations as the tables' owner, and the platform as a role that does not own them, so that it cannot turn the chain trigger off.
  - Insert audited rows at read committed, and last in their transaction; a new column on an audited table comes with a new form.
- **Plan 8:** exclude the incidents of the runs that `lost` and `diverged` findings name, and the incidents that were active between the last anchor the chain holds and a restore.
- **Plan 10, the restore drill:** stop the anchor job while a restore runs, and start it once the platform writes again; its first anchor names the losses.
- **Plan 10:**
  - prune only with chained prune records, and let verification accept a pruned row or segment only under one;
  - let the owner acknowledge each `lost` finding as a restore, after checking it was one (an object only the owner can write), so that verification turns clean again while still listing the losses;
  - before a Postgres major upgrade, run the digest tests against the new major;
  - measure the chain lock in the load trial.

## File Structure

| File | Responsibility |
|---|---|
| `internal/actions/redact/redact.go` | Modified: `Changes`, what redacting a value changed in it; `Before`, rules that apply first |
| `internal/actions/app/call.go` | Modified: `repeats`, the rules that show a hidden argument an agent's read repeats as stored |
| `internal/audit/chain/chain.go` | New: `Sum`, `Link`, `LinkHash`, `Digest` |
| `internal/audit/chain/anchor.go` | New: `Anchor`, its key and canonical form; `Stored`, `CheckAnchors` |
| `internal/audit/chain/verify.go` | New: the kinds of finding, `Finding`, `Walk` |
| `internal/audit/app/auditor.go` | New: the ports `Chain`, `View`, `Anchors`; `Auditor.Anchor` and `Auditor.Verify` |
| `internal/audit/dirstore/dirstore.go` | New: anchors in a directory, for development and tests |
| `internal/audit/s3store/s3store.go` | New: anchors in an S3 bucket with object lock |
| `internal/db/migrations/0005_audit_chain.sql` | New: `audit_chain`, its guard, the append, the triggers, the existing rows chained |
| `internal/audit/postgres/chain.go` | New: `Chain`, the snapshot verification and anchoring read |
| `internal/db/db.go` | Modified: `Open` defaults every connection to read committed, and ends idle transactions |
| `internal/testsupport/pgtest/pgtest.go` | Modified: `URL`, for code that opens its own pool |
| `cmd/agentswarm/audit.go` | New: `agentswarm audit anchor` and `agentswarm audit verify` |
| `cmd/agentswarm/run.go` | Modified: the `audit` command |

## Tasks

The tasks run in order. Task 1 stands alone. Tasks 2 to 6 build the chain from its rules outwards. Task 7 documents both.

### Task 1: A hidden argument that an agent's read repeats

Decision 13. Plan 5b's final review deferred it (M2): the echo check reads a call's stored arguments, which are redacted, so an argument that a path rule replaced whole is invisible to it and to raters.

**Files:**
- Create: `internal/actions/app/repeats_internal_test.go`
- Modify: `internal/actions/redact/redact_test.go`, `internal/actions/redact/redact.go`, `internal/actions/app/gateway_test.go`, `internal/actions/app/board_test.go`, `internal/actions/app/call.go`

**Interfaces:**
- Consumes: `redact.Redactor` and `Redactor.Value` (Plans 5a and 5b); the read path's `read` in `internal/actions/app/call.go`.
- Produces:
  - `redact.Rule` gains `Literals []string`: text matched folded, in any case and with any run of white space, in linear time;
  - `func redact.Fold(s string) string`;
  - `type redact.Change struct { From, To string }`;
  - `func (r *Redactor) Changes(v any) []Change`;
  - `func (r *Redactor) Before(rules ...Rule) *Redactor`.

- [ ] **Step 1: Write the failing tests**

Replace `internal/actions/redact/redact_test.go` with this version, which adds `TestChangesNamesEveryStringKeyAndNumberRedactionChanges`, `TestBeforePutsRulesFirst`, `TestALiteralIsFoundInAnyCaseAndSpacing`, `TestALongLiteralIsMatchedInLinearTime`, `TestALiteralFoldsCaseBeyondASCII`, `TestManyShortStringsWithALongLiteralCostLittle` and `TestOverlappingSpellingsAreAllReplaced`:

```go
package redact_test

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/selector"
)

func rule(t *testing.T, replacement string, patterns []string, paths ...string) redact.Rule {
	t.Helper()
	r := redact.Rule{Replacement: replacement}
	for _, p := range patterns {
		r.Patterns = append(r.Patterns, regexp.MustCompile(p))
	}
	for _, p := range paths {
		s, err := selector.Parse(p)
		require.NoError(t, err)
		r.Paths = append(r.Paths, s)
	}
	return r
}

func decode(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var v any
	require.NoError(t, dec.Decode(&v))
	return v
}

func encode(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

func TestTextReplacesEveryMatchLiterally(t *testing.T) {
	r := redact.New(rule(t, "[key $1]", []string{`AKIA([0-9A-Z]{16})`}))

	got := r.Text("a AKIAABCDEFGHIJKLMNOP and AKIAQRSTUVWXYZ234567.")

	assert.Equal(t, "a [key $1] and [key $1].", got)
}

func TestValueRedactsKeysValuesAndNumbers(t *testing.T) {
	r := redact.New(rule(t, "[card]", []string{`\b4[0-9]{15}\b`}))
	doc := decode(t, `{"4111111111111111":"owner","card":4111111111111111,"note":["paid with 4111111111111111"],"amount":12.5}`)

	got := r.Value(doc)

	assert.JSONEq(t, `{"[card]":"owner","card":"[card]","note":["paid with [card]"],"amount":12.5}`, encode(t, got))
}

// Redacting a value never changes the value: the gateway stores arguments
// redacted, but checks and sends them as written.
func TestValueNeverChangesWhatItRedacts(t *testing.T) {
	r := redact.New(rule(t, "[secret]", []string{`hunter2`}, "$.window"))
	doc := decode(t, `{"window":"1h","note":["hunter2"],"nested":{"window":"2h","list":[{"pw":"hunter2"}]}}`)
	before := encode(t, doc)

	got := r.Value(doc)

	assert.JSONEq(t, `{"window":"[secret]","note":["[secret]"],"nested":{"window":"2h","list":[{"pw":"[secret]"}]}}`, encode(t, got))
	assert.JSONEq(t, before, encode(t, doc))
}

// A rule written for "password: ..." in plain text also finds the member
// {"password": "..."}, where key and value are separate strings.
func TestAPatternFindsAMemberWrittenAsKeyEqualsValue(t *testing.T) {
	r := redact.New(rule(t, "[secret]", []string{`(?i)\b(password|api[_-]?key)\s*[:=]\s*[^\s,;]{8,}`}))

	got := r.Value(decode(t, `{"password":"hunter2hunter2","user":"ana","nested":[{"api_key":"k3y-abcdefgh"}],"pin":12345678}`))

	assert.JSONEq(t, `{"password":"[secret]","user":"ana","nested":[{"api_key":"[secret]"}],"pin":12345678}`, encode(t, got))
}

func TestPathsReplaceWholeValuesBeforePatternsRun(t *testing.T) {
	r := redact.New(
		rule(t, "[env]", nil, "$..env[*].value"),
		rule(t, "[token]", []string{`tok_[a-z0-9]+`}),
	)
	doc := decode(t, `{"env":[{"name":"DB","value":{"password":"hunter2"}}],"log":"used tok_abc123"}`)

	got := r.Value(doc)

	assert.JSONEq(t, `{"env":[{"name":"DB","value":"[env]"}],"log":"used [token]"}`, encode(t, got))
	assert.True(t, r.HasPaths())
	assert.False(t, redact.New(rule(t, "x", []string{"y"})).HasPaths())
}

func TestKeysThatRedactionMakesEqualAreToldApart(t *testing.T) {
	r := redact.New(rule(t, "[email]", []string{`[a-z]+@example.com`}))
	for range 20 {
		got := r.Value(decode(t, `{"ana@example.com":1,"bo@example.com":2,"cy@example.com":3}`))

		assert.Equal(t, map[string]any{"[email]": json.Number("1"), "[email] (2)": json.Number("2"), "[email] (3)": json.Number("3")}, got)
	}
}

func TestAnEscapedMatchInTextIsFound(t *testing.T) {
	r := redact.New(rule(t, "[dsn]", []string{`[a-z]+://[^\s:/@]+:[^\s@/]+@`}))

	assert.True(t, r.Escaped(`payload={"dsn":"postgres:\/\/u:pw@db"}`))
	assert.False(t, r.Escaped(`key \u0041KIA`), "no rule matches the decoded text")
	assert.False(t, r.Escaped(`no escapes here`))
	assert.False(t, r.Escaped(`dsn postgres://[dsn]db`))
}

func TestNoRulesChangeNothing(t *testing.T) {
	r := redact.New()

	assert.Equal(t, "AKIAABCDEFGHIJKLMNOP", r.Text("AKIAABCDEFGHIJKLMNOP"))
	assert.JSONEq(t, `{"n":12345678901234567890}`, encode(t, r.Value(decode(t, `{"n":12345678901234567890}`))))
	assert.False(t, r.Escaped(`postgres:\/\/u:pw@h`))
}

// A pattern that matches the key alone still hides the value when its match
// in key=value runs on past the equals sign.
func TestAMatchAcrossTheEqualsSignReplacesTheValue(t *testing.T) {
	r := redact.New(rule(t, "[pw]", []string{`(?i)password\S*`}))

	got := r.Value(decode(t, `{"password":"hunter2hunter2","card":"4111"}`))

	assert.JSONEq(t, `{"[pw]":"[pw]","card":"4111"}`, encode(t, got))
}

var secrets = []string{
	`(?i)\b(password|passwd|secret|api[_-]?key)\s*[:=]\s*[^\s,;]{8,}`,
	`\bAKIA[0-9A-Z]{16}\b`,
	`(?i)\b[a-z]+://[^\s:/@]+:[^\s@/]+@`,
}

// JSON written inside text, such as a log line, a JSON line or JSON that was
// cut short, gets the member rule too.
func TestTextFindsMembersWrittenAsJSON(t *testing.T) {
	r := redact.New(rule(t, "[secret]", secrets))

	for in, want := range map[string]string{
		`Found: {"user":"ana","password":"hunter2hunter2"}`:    `Found: {"user":"ana","password":"[secret]"}`,
		"{\"a\":1,\"password\":\"hunter2hunter2\"}\n{\"b\":2}": "{\"a\":1,\"password\":\"[secret]\"}\n{\"b\":2}",
		`{"rows":[{"p\u0061ssword" : "hunter2hunter2"},{"x`:    `{"rows":[{"p\u0061ssword" : "[secret]"},{"x`,
		`{"pin":12345678,"ok":true}`:                           `{"pin":12345678,"ok":true}`,
	} {
		assert.Equal(t, want, r.Text(in), in)
	}
}

// A string value written in text is redacted as text of its own, decoded, so
// an escape inside it hides nothing.
func TestTextRedactsStringValuesWrittenInText(t *testing.T) {
	r := redact.New(rule(t, "[secret]", secrets))

	for in, want := range map[string]string{
		`log "msg":"loaded key\nAKIAABCDEFGHIJKLMNOP"`:              `log "msg":"loaded key\n[secret]"`,
		`{"msg":"{\"password\":\"hunter2hunter2\"}"}`:               `{"msg":"{\"password\":\"[secret]\"}"}`,
		`{"url":"https://h/p?user=x\u0026password=hunter2hunter2"}`: `{"url":"https://h/p?user=x&[secret]"}`,
	} {
		assert.Equal(t, want, r.Text(in), in)
	}
}

func TestEscapesAreDecodedLevelByLevel(t *testing.T) {
	r := redact.New(rule(t, "[dsn]", []string{`[a-z]+://[^\s:/@]+:[^\s@/]+@`}))

	assert.True(t, r.Escaped(`dsn postgres:\\/\\/admin:pw@db`), "two levels down")
	assert.True(t, r.Escaped(strings.Repeat(`\`, 32)+"n"), "deeper than the gateway decodes")
	assert.False(t, r.Escaped(`C:\new\table`))
	assert.False(t, r.Escaped(strings.Repeat(`\`, 16)+"x"))
}

func TestEscapedValueChecksEveryStringAndKey(t *testing.T) {
	r := redact.New(rule(t, "[dsn]", []string{`[a-z]+://[^\s:/@]+:[^\s@/]+@`}))

	assert.True(t, r.EscapedValue(r.Value(decode(t, `{"lines":["postgres:\\/\\/admin:pw@db"]}`))))
	assert.True(t, r.EscapedValue(r.Value(decode(t, `{"postgres:\\/\\/admin:pw@db":1}`))))
	assert.False(t, r.EscapedValue(r.Value(decode(t, `{"lines":["C:\\new"],"n":1}`))))
}

// Python and Ruby write members too, and a broken JSON string may hold a
// line break; text that is not JSON gets the member rule all the same.
func TestTextFindsMembersWrittenInOtherForms(t *testing.T) {
	r := redact.New(rule(t, "[secret]", secrets))

	for in, want := range map[string]string{
		`{'user': 'ana', 'password': 'hunter2hunter2'}`: `{'user': 'ana', 'password': "[secret]"}`,
		`{"password"=>"hunter2hunter2", "n"=>1}`:        `{"password"=>"[secret]", "n"=>1}`,
		"{\"password\":\"hunter2hunter2\nmore\"}":       `{"password":"[secret]"}`,
	} {
		assert.Equal(t, want, r.Text(in), in)
	}
}

// A match that needs both the key and the value hides the value, even when
// it does not run on into the value.
func TestAMatchThatNeedsTheKeyAndTheValueReplacesTheValue(t *testing.T) {
	r := redact.New(rule(t, "[pw]", []string{`(?i)^password=`}))

	assert.JSONEq(t, `{"password":"[pw]"}`, encode(t, r.Value(decode(t, `{"password":"hunter2"}`))))
	assert.JSONEq(t, `{"password":"[pw]"}`, r.Text(`{"password":"hunter2"}`))
}

// A value that runs on, as raw JSON written inside a string or a JSON line
// cut inside a string, never hides the member after it.
func TestAValueThatRunsOnNeverHidesTheNextMember(t *testing.T) {
	r := redact.New(rule(t, "[secret]", secrets))

	for in, want := range map[string]string{
		`{"msg": "request body: {"password":"hunter2hunter2"}"}`:                  `{"msg": "request body: {"password":"[secret]"}"}`,
		"{\"msg\":\"cut here\n{\"password\":\"hunter2hunter2\",\"user\":\"ana\"}": "{\"msg\":\"cut here\n{\"password\":\"[secret]\",\"user\":\"ana\"}",
		`2026-10-14 INFO {"msg": "body: {"api_key":"k3y-abcdefgh"}"}`:             `2026-10-14 INFO {"msg": "body: {"api_key":"[secret]"}"}`,
		`{"rows":[{"user":"ana","password":"hunter2hunt`:                          `{"rows":[{"user":"ana","password":"[secret]"`,
	} {
		assert.Equal(t, want, r.Text(in), in)
	}
}

// Text never fails on what members are written like, however they are cut.
func TestTextReadsAnyCutOfAMemberWithoutFailing(t *testing.T) {
	r := redact.New(rule(t, "[secret]", secrets))

	for _, in := range []string{`"a":"b":1`, `'a':'b':1`, `"a":"`, `"a":'`, `"a":`, `"a": "b"=>"c"`, `"a":"\`, `"a":"\"b":1`} {
		assert.NotPanics(t, func() { r.Text(in) }, in)
	}
}

func FuzzTextNeverFails(f *testing.F) {
	for _, s := range []string{`"a":"b":1`, `{"password":"hunter2hunter2"}`, `'a': 'b\'c', "d"=>"e"`, `"k":"v\nw","x":"\u00`, `\\\"`} {
		f.Add(s)
	}
	r := redact.New(redact.Rule{Replacement: "[secret]", Patterns: []*regexp.Regexp{regexp.MustCompile(secrets[0]), regexp.MustCompile(secrets[1])}})
	f.Fuzz(func(_ *testing.T, s string) {
		_ = r.Escaped(r.Text(s))
	})
}

// Changes names every string, key and number that redaction changes, with
// what the redacted value shows instead: the text redacted, or the
// replacement of a path that replaced the value around it. A string whose
// key alone changed is left out, as is what redaction leaves alone.
func TestChangesNamesEveryStringKeyAndNumberRedactionChanges(t *testing.T) {
	r := redact.New(
		rule(t, "[hidden]", nil, "$.query", "$.filter"),
		rule(t, "[token]", []string{`tok_[a-z0-9]+`}),
		rule(t, "[card]", []string{`\b4[0-9]{15}\b`}),
	)
	doc := decode(t, `{"query":"the cart service leaks memory","filter":{"who":"alice","ids":["x1"]},
		"note":"used tok_abc123 here","tok_key":"kept","card":4111111111111111,"plain":"as written"}`)

	got := r.Changes(doc)

	assert.ElementsMatch(t, []redact.Change{
		{From: "the cart service leaks memory", To: "[hidden]"},
		{From: "who", To: "[hidden]"},
		{From: "alice", To: "[hidden]"},
		{From: "ids", To: "[hidden]"},
		{From: "x1", To: "[hidden]"},
		{From: "used tok_abc123 here", To: "used [token] here"},
		{From: "tok_key", To: "[token]"},
		{From: "4111111111111111", To: "[card]"},
	}, got)
}

// Rules put before a redactor's own apply first.
func TestBeforePutsRulesFirst(t *testing.T) {
	r := redact.New(rule(t, "[word]", []string{`secret`}))

	both := r.Before(rule(t, "[phrase]", []string{`top secret plan`}))

	assert.Equal(t, "the [phrase], a [word]", both.Text("the top secret plan, a secret"))
	assert.Equal(t, "the top [word] plan, a [word]", r.Text("the top secret plan, a secret"), "the redactor itself is unchanged")
}

// A literal is found in any case and with any run of white space where it
// has one, also white space beyond ASCII, and the text around it stays as
// it was, invalid bytes included.
func TestALiteralIsFoundInAnyCaseAndSpacing(t *testing.T) {
	r := redact.New(redact.Rule{Literals: []string{" The Cart  leaks "}, Replacement: "[x]"})

	assert.Equal(t, "a [x] b [x].", r.Text("a THE\u00a0cart\n\t\u3000leaks b the cart leaks."))
	assert.Equal(t, "no match here", r.Text("no match here"))
	assert.Equal(t, "\xff [x] \u0130", r.Text("\xff the cart LEAKS \u0130"))
	assert.Equal(t, "x", redact.New(redact.Rule{Literals: []string{"  "}, Replacement: "[x]"}).Text("x"), "a literal of white space alone matches nothing")
}

// A long literal costs time linear in the text it is matched against.
func TestALongLiteralIsMatchedInLinearTime(t *testing.T) {
	value := strings.Repeat("a ", 30000) + "end"
	r := redact.New(redact.Rule{Literals: []string{value}, Replacement: "[x]"})
	text := strings.Repeat(value+" ", 4)

	start := time.Now()
	got := r.Text(text)

	assert.Equal(t, "[x] [x] [x] [x] ", got)
	assert.Less(t, time.Since(start), time.Second)
}

// Letters whose cases map unevenly meet their plain forms: the long s, the
// Kelvin sign, the final sigma.
func TestALiteralFoldsCaseBeyondASCII(t *testing.T) {
	r := redact.New(redact.Rule{Literals: []string{"the \u017fervice of \u212aelvin \u03bb\u03bf\u03b3\u03bf\u03c2"}, Replacement: "[x]"})

	assert.Equal(t, "a [x].", r.Text("a THE SERVICE OF kelvin \u039b\u039f\u0393\u039f\u03a3."))
	assert.Equal(t, "ok", redact.Fold("O\u212a"))
}

// A long literal costs little against an output of many short strings: it
// is folded once, and never against a string too short to hold it.
func TestManyShortStringsWithALongLiteralCostLittle(t *testing.T) {
	r := redact.New(redact.Rule{Literals: []string{strings.Repeat("a ", 15000) + "end"}, Replacement: "[x]"})
	numbers := make([]any, 60000)
	for i := range numbers {
		numbers[i] = json.Number(strconv.Itoa(i))
	}

	start := time.Now()
	got := r.Value(map[string]any{"numbers": numbers})

	assert.Len(t, got.(map[string]any)["numbers"], 60000)
	assert.Less(t, time.Since(start), time.Second)
}

// Spellings of one value that overlap are all replaced, leftmost first and
// longest where two start together, so that no repeat is left in between.
func TestOverlappingSpellingsAreAllReplaced(t *testing.T) {
	r := redact.New(redact.Rule{Literals: []string{"/tmp/tmp", `\/tmp\/tmp`}, Replacement: "[x]"})

	assert.Equal(t, "[x][x]", r.Text(`\/tmp\/tmp/tmp/tmp`))
	assert.Equal(t, "a [x] b [x]", r.Text("a /TMP/tmp b /tmp/tmp"))
}
```

Append to `internal/actions/app/gateway_test.go`:

```go
// hideFlag gives get_flag's rule a path that hides the flag argument whole.
func hideFlag(t *testing.T, e *env) {
	t.Helper()
	files := revisionFiles(t, defaultPins(), "reviewed")
	files["redaction-rules/default-secrets.yaml"] = []byte(`kind: RedactionRule
metadata: {name: default-secrets, owner: security@example.com}
spec:
  patterns: ['AKIA[0-9A-Z]{16}']
  paths: ['$..password', '$.flag']
`)
	e.use(t, files, "4ede4ede4ede4ede4ede4ede4ede4ede4ede4ede")
}

// Where a read's output repeats an argument value that the rules hid in the
// stored arguments, the output shows it as the stored arguments do, also
// with other spacing or written with an escape: nothing the agent wrote
// there can be quoted as the server's answer, and the value never leaks
// through the output.
func TestAnOutputThatRepeatsAHiddenArgumentShowsItAsStored(t *testing.T) {
	e := newEnv(t)
	hideFlag(t, e)
	e.up.set(func(u *upstream) {
		u.replies["get_flag"] = text(`{"flag":"x","on":false,"note":"no flag is named the  cart\nservice leaks memory; nor \u0074he cart service leaks memory","truncated":false}`)
	})

	r := e.call(t, "flags__get_flag", `{"flag":"the cart service leaks memory","window":"1h"}`)

	require.Equal(t, app.StatusOK, r.Status, r.Text)
	assert.NotContains(t, r.Text, "leaks memory")
	assert.Contains(t, r.Text, "no flag is named [redacted:default-secrets]; nor [redacted:default-secrets]")
	rec, out, _, _ := e.audit.Record(r.CallID)
	assert.NotContains(t, string(rec.Args), "leaks memory")
	assert.Equal(t, r.Text, out.Output, "the agent sees what is stored")
}

// A hidden argument value shorter than four characters is left in the
// output: replacing it would mangle the output, and it cannot carry a claim.
func TestAShortHiddenArgumentIsLeftInTheOutput(t *testing.T) {
	e := newEnv(t)
	hideFlag(t, e)
	e.up.set(func(u *upstream) {
		u.replies["get_flag"] = text(`{"flag":"x","on":true,"note":"cat x is on","truncated":false}`)
	})

	r := e.call(t, "flags__get_flag", `{"flag":"cat","window":"1h"}`)

	require.Equal(t, app.StatusOK, r.Status, r.Text)
	assert.Contains(t, r.Text, "cat x is on")
}

// A hidden argument value is found in the output however the server writes
// it: in another case, with other white space, escaped once more inside a
// string, as JSON writes it, with characters beyond ASCII or slashes
// escaped, or holding text another rule redacts. The output shows the
// stored value there, and an honest repeat never turns into a read error.
func TestARepeatIsFoundInAnyCaseAndSpelling(t *testing.T) {
	for flag, note := range map[string]string{
		"the cart service leaks memory":       "NO FLAG IS NAMED THE CART SERVICE LEAKS MEMORY",
		`level=error msg="timeout" in cart`:   `no results for level=error msg=\\\"timeout\\\" in cart`,
		"café leaks":                          `no flag is named caf\\u00e9 leaks`,
		"AKIAABCDEFGHIJKLMNOP the cart leaks": "no flag is named AKIAABCDEFGHIJKLMNOP the cart leaks",
		"the bus service leaks memory":        "no flag is named the\u00a0bus\u3000service\u2028leaks memory",
		"a/b/c leaks":                         `no flag is named a\\\/b\\\/c leaks`,
	} {
		e := newEnv(t)
		hideFlag(t, e)
		e.up.set(func(u *upstream) {
			u.replies["get_flag"] = text(`{"flag":"x","on":false,"note":"` + note + `","truncated":false}`)
		})
		args, err := json.Marshal(map[string]string{"flag": flag, "window": "1h"})
		require.NoError(t, err)

		r := e.call(t, "flags__get_flag", string(args))

		require.Equal(t, app.StatusOK, r.Status, "%s: %s", flag, r.Text)
		assert.Contains(t, strings.ToLower(r.Text), "[redacted:default-secrets]", flag)
		assert.NotContains(t, strings.ToLower(r.Text), "leaks", flag)
		assert.NotContains(t, r.Text, "timeout", flag)
	}
}

// The platform's own reads keep what the server answered: nobody can cite
// them, and a check of a write call's candidates compares what they hold.
func TestAPlatformReadKeepsAHiddenArgumentItRepeats(t *testing.T) {
	e := newEnv(t)
	files := revisionFiles(t, defaultPins(), "reviewed")
	files["redaction-rules/default-secrets.yaml"] = []byte("kind: RedactionRule\nmetadata: {name: default-secrets, owner: security@example.com}\nspec:\n  paths: ['$.flag']\n")
	server := string(files["servers/flags.yaml"])
	files["servers/flags.yaml"] = []byte(strings.Replace(server, "effect: read, audience: platform}", "effect: read, audience: platform, redact: [default-secrets]}", 1))
	e.use(t, files, "9a7f9a7f9a7f9a7f9a7f9a7f9a7f9a7f9a7f9a7f")
	e.up.set(func(u *upstream) {
		u.replies["owners"] = text(`{"owners":[{"user":"the cart service leaks memory"}]}`)
	})
	pr := &config.PlatformRead{Tool: "flags.owners", Args: map[string]string{"flag": "{labels.flag}"}, Field: "$.owners[*].user"}
	values, err := app.Trusted(map[string]string{"labels.flag": "the cart service leaks memory"})
	require.NoError(t, err)

	got, err := e.gw.PlatformRead(t.Context(), app.Origin{Run: "run-1", Commit: e.session.Commit}, "", pr, values)

	require.NoError(t, err)
	assert.Equal(t, []any{"the cart service leaks memory"}, got.Values)
	rec, _, _, _ := e.audit.Record(got.CallID)
	assert.NotContains(t, string(rec.Args), "leaks", "the stored arguments are redacted all the same")
}

// A long hidden argument that the read repeats, in an output of many
// strings, is found in time linear in the output: an agent cannot tie the
// gateway up with one.
func TestALongRepeatedHiddenArgumentCostsLinearTime(t *testing.T) {
	e := newEnv(t)
	hideFlag(t, e)
	flag := strings.Repeat("a ", 30000) + "leaks"
	numbers := strings.TrimSuffix(strings.Repeat("12345,", 3000), ",")
	e.up.set(func(u *upstream) {
		u.replies["get_flag"] = text(`{"flag":"x","on":false,"seen":[` + numbers + `],"note":"no flag is named ` + flag + `","truncated":false}`)
	})
	args, err := json.Marshal(map[string]string{"flag": flag, "window": "1h"})
	require.NoError(t, err)

	start := time.Now()
	r := e.call(t, "flags__get_flag", string(args))

	require.Equal(t, app.StatusOK, r.Status, r.Text)
	assert.Contains(t, r.Text, "no flag is named [redacted:default-secrets]")
	assert.Less(t, time.Since(start), 2*time.Second)
}
```

Append to `internal/actions/app/board_test.go`:

```go
// A quote of what the agent wrote in a hidden argument, repeated by the
// read, is refused: the stored output shows the stored value there. A quote
// of the answer around it, with the stored value, counts, since a rater sees
// the same value in the call's arguments.
func TestEvidenceCannotQuoteARepeatedHiddenArgument(t *testing.T) {
	e := newEnv(t)
	hideFlag(t, e)
	e.up.set(func(u *upstream) {
		u.replies["get_flag"] = text(`{"flag":"x","on":false,"note":"no flag is named the cart service leaks memory","truncated":false}`)
	})
	read := e.call(t, "flags__get_flag", `{"flag":"the cart service leaks memory","window":"1h"}`)
	require.Equal(t, app.StatusOK, read.Status, read.Text)

	own := e.call(t, "post_evidence", fmt.Sprintf(`{"claim":"the cart service leaks memory","call_id":%q,"quote":"the cart service leaks memory"}`, read.CallID))
	around := e.call(t, "post_evidence", fmt.Sprintf(`{"claim":"no such flag","call_id":%q,"quote":"no flag is named [redacted:default-secrets]"}`, read.CallID))

	assert.Equal(t, app.StatusRefused, own.Status)
	assert.Contains(t, own.Text, "does not appear")
	assert.Equal(t, app.StatusOK, around.Status, around.Text)
}
```

Create `internal/actions/app/repeats_internal_test.go`, which tests the bounds from inside the package:

```go
package app

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/selector"
)

// hideAll hides every string, key and number of the arguments.
var hideAll = redact.New(redact.Rule{Patterns: []*regexp.Regexp{regexp.MustCompile(`.+`)}, Replacement: "[hidden]"})

// At most 32 values an output is checked for; more refuse the call, which
// bounds the work redaction does on the output.
func TestRepeatsAreBounded(t *testing.T) {
	values := map[string]any{}
	for i := range maxRepeats {
		values[fmt.Sprintf("arg%d", i)] = "x"
	}
	_, err := repeats(redact.New(), values)
	require.NoError(t, err, "nothing changed, nothing to check")

	values = map[string]any{"terms": []any{}}
	for i := range maxRepeats / 2 {
		values["terms"] = append(values["terms"].([]any), fmt.Sprintf("term %d", i))
	}
	_, err = repeats(hideAll, values)
	require.NoError(t, err, "16 strings and their key")

	for i := range maxRepeats {
		values[fmt.Sprintf("more-%d", i)] = "a value"
	}
	_, err = repeats(hideAll, values)
	require.ErrorContains(t, err, "narrow the call")
}

// A value of fewer than four bytes is left alone; two characters beyond
// ASCII are enough to carry a word.
func TestShortValuesAreMeasuredInBytes(t *testing.T) {
	paths, err := selector.Parse("$.*")
	require.NoError(t, err)
	hidden := redact.New(redact.Rule{Paths: []selector.Selector{paths}, Replacement: "[hidden]"})

	red, err := repeats(hidden, map[string]any{"a": "abc", "b": "漢字"})

	require.NoError(t, err)
	assert.Equal(t, "abc [hidden]", red.Text("abc 漢字"))
}

// The length that counts is the folded one: a value that folds to fewer than
// four bytes, as "o" and the Kelvin sign fold to "ok", is left alone.
func TestShortValuesAreMeasuredFolded(t *testing.T) {
	paths, err := selector.Parse("$.*")
	require.NoError(t, err)
	hidden := redact.New(redact.Rule{Paths: []selector.Selector{paths}, Replacement: "[hidden]"})

	red, err := repeats(hidden, map[string]any{"a": "o\u212a"})

	require.NoError(t, err)
	assert.Equal(t, "ok, broken", red.Text("ok, broken"))
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/actions/...`
Expected: FAIL: both packages fail to build: `r.Changes undefined`, `undefined: redact.Change` and `r.Before undefined` in `redact_test.go`; `undefined: maxRepeats` and `undefined: repeats` in `repeats_internal_test.go`

- [ ] **Step 3: Name what redaction changed, and let rules apply first**

Replace `internal/actions/redact/redact.go` with this version, which adds `Rule.Literals`, the folded needles `New` keeps, `Before`, `Fold`, `replaceFolded` with its `matcher`, `fold`, `Change`, `Changes` and `changes`:

```go
// Package redact applies redaction rules (spec Section 9.4) to what the
// gateway stores and shows: outputs, errors and arguments. Rules apply to
// decoded text, so an escape cannot hide a match: patterns replace what they
// match in every string, key and number of a decoded JSON value, and in each
// member written as key=value, so that a rule for "password: ..." also finds
// {"password": "..."}; paths replace whole values. In text, each member
// written there as JSON, Python or Ruby write one, as in a log line or a JSON
// line, gets the same member rule, and each quoted value written there is
// redacted as text of its own, decoded.
package redact

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/selector"
)

// maxDepth is how far down redaction decodes: levels of JSON escapes, and
// string values written inside string values.
const maxDepth = 4

// writtenKey finds a key written in text the way JSON, Python and Ruby
// write one: a quoted key, then a colon or "=>". Group 1 or 2 is the key, in
// double or single quotes. A key does not span a line.
var writtenKey = regexp.MustCompile(`(?:"((?:[^"\\\n]|\\.)*)"|'((?:[^'\\\n]|\\.)*)')[ \t]*(?::|=>)[ \t]*`)

// writtenScalar finds a number or a boolean at the start of text.
var writtenScalar = regexp.MustCompile(`^(?:-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|True|False)\b`)

// Rule is one redaction rule. Literals are matched as text, folded as Fold
// folds it: in any case, and with any run of white space where a literal
// has one, in time linear in the text, however long the literal.
type Rule struct {
	Patterns    []*regexp.Regexp
	Paths       []selector.Selector
	Literals    []string
	Replacement string
}

// FromConfig returns the rule a configured redaction rule holds.
func FromConfig(r *config.RedactionRule) Rule {
	return Rule{Patterns: r.Patterns(), Paths: r.Paths(), Replacement: r.Replacement()}
}

// Redactor applies rules in order.
type Redactor struct {
	rules []Rule
	// needles holds each rule's literals, folded once, when the redactor
	// is made.
	needles [][]string
}

// New returns a redactor of rules, applied in the order given.
func New(rules ...Rule) *Redactor {
	r := &Redactor{rules: rules, needles: make([][]string, len(rules))}
	for i, rule := range rules {
		for _, lit := range rule.Literals {
			if n := Fold(lit); n != "" && !slices.Contains(r.needles[i], n) {
				r.needles[i] = append(r.needles[i], n)
			}
		}
	}
	return r
}

// Before returns a redactor that applies rules, then r's.
func (r *Redactor) Before(rules ...Rule) *Redactor {
	return New(append(slices.Clone(rules), r.rules...)...)
}

// HasPaths reports whether any rule has paths, which only a JSON value can
// answer.
func (r *Redactor) HasPaths() bool {
	return slices.ContainsFunc(r.rules, func(rule Rule) bool { return len(rule.Paths) > 0 })
}

// Replacements returns each rule's replacement text.
func (r *Redactor) Replacements() []string {
	out := make([]string, 0, len(r.rules))
	for _, rule := range r.rules {
		out = append(out, rule.Replacement)
	}
	return out
}

// Text replaces every match of every pattern in s. Before that, each member
// written in s gets the member rule, and each quoted value written there is
// redacted as text of its own, decoded, and written again as JSON.
func (r *Redactor) Text(s string) string { return r.text(s, 0) }

func (r *Redactor) text(s string, depth int) string {
	if len(r.rules) == 0 {
		return s
	}
	s = r.members(s, depth)
	for i, rule := range r.rules {
		if len(r.needles[i]) > 0 {
			s = replaceFolded(s, r.needles[i], rule.Replacement)
		}
		for _, re := range rule.Patterns {
			s = re.ReplaceAllLiteralString(s, rule.Replacement)
		}
	}
	return s
}

// Fold returns s as literals are compared: each letter in one case, lower
// case after upper, so that letters whose cases map unevenly, such as the
// long s and the Kelvin sign, meet their plain forms; each run of white
// space, as unicode.IsSpace finds it, as one space; and no white space at
// either end.
func Fold(s string) string {
	folded, _, _ := fold(s)
	return strings.TrimSpace(folded)
}

// replaceFolded replaces each occurrence in s of any of needles, folded
// literals, with with, in one pass from the left: at each point the
// occurrence that starts first, the longest where two start together, and
// never two that overlap. s is folded once, and each match is mapped back to
// the bytes of s it spans, so the rest of s stays as it was. A fold at most
// triples a text, so needles longer than that are skipped without folding
// it.
func replaceFolded(s string, needles []string, with string) string {
	shortest := len(needles[0])
	for _, n := range needles[1:] {
		shortest = min(shortest, len(n))
	}
	if 3*len(s) < shortest {
		return s
	}
	hay, starts, ends := fold(s)
	m := newMatcher(hay, needles)
	var b strings.Builder
	last, at := 0, 0
	for k := m.earliest(0); k >= 0; k = m.earliest(at) {
		from, to := m.next[k], m.next[k]+len(needles[k])
		b.WriteString(s[last:starts[from]])
		b.WriteString(with)
		last, at = ends[to-1], to
	}
	if at == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// matcher finds needles in a folded text, each from where the last search
// for it ended, so that no needle is searched for twice over one stretch.
type matcher struct {
	hay     string
	needles []string
	// next holds where each needle next occurs, or -1 when it does not.
	next []int
}

func newMatcher(hay string, needles []string) *matcher {
	m := &matcher{hay: hay, needles: needles, next: make([]int, len(needles))}
	for k := range needles {
		m.next[k] = m.find(k, 0)
	}
	return m
}

func (m *matcher) find(k, from int) int {
	if i := strings.Index(m.hay[from:], m.needles[k]); i >= 0 {
		return from + i
	}
	return -1
}

// earliest returns the needle whose next occurrence at or after at starts
// first, the longest where two start together, or -1 when none occurs.
func (m *matcher) earliest(at int) int {
	best := -1
	for k := range m.needles {
		if m.next[k] >= 0 && m.next[k] < at {
			m.next[k] = m.find(k, at)
		}
		if m.next[k] < 0 {
			continue
		}
		if best < 0 || m.next[k] < m.next[best] || m.next[k] == m.next[best] && len(m.needles[k]) > len(m.needles[best]) {
			best = k
		}
	}
	return best
}

// fold returns s with each letter in one case, lower after upper, and each
// run of white space as one space, and, for each byte of the result, where
// the rune it came from starts and ends in s.
func fold(s string) (folded string, starts, ends []int) {
	var b strings.Builder
	space := false
	for i, r := range s {
		end := i + utf8.RuneLen(r)
		if r == utf8.RuneError {
			_, end = utf8.DecodeRuneInString(s[i:])
			end += i
		}
		if unicode.IsSpace(r) {
			if space {
				ends[len(ends)-1] = end
				continue
			}
			space = true
			b.WriteByte(' ')
			starts, ends = append(starts, i), append(ends, end)
			continue
		}
		space = false
		n, _ := b.WriteRune(unicode.ToLower(unicode.ToUpper(r)))
		for range n {
			starts, ends = append(starts, i), append(ends, end)
		}
	}
	return b.String(), starts, ends
}

// members applies the member rule to each member written in s, and redacts
// each quoted value written there, down to maxDepth levels. Keys are found
// first, each on its own, so a value that runs on, as raw JSON inside a
// string or JSON cut inside a string, never hides the key after it: a value
// ends at the next key at the latest. A value redaction does not change
// keeps its spelling.
func (r *Redactor) members(s string, depth int) string {
	keys := writtenKey.FindAllStringSubmatchIndex(s, -1)
	if keys == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for i, m := range keys {
		limit := len(s)
		if i+1 < len(keys) {
			limit = keys[i+1][0] + 1
		}
		from, to, value, quoted := valueAt(s, m[1], limit)
		if to < 0 {
			continue
		}
		redacted, ok := r.member(unescape(group(s, m, 1)+group(s, m, 2)), value)
		if !ok {
			if !quoted || depth == maxDepth {
				continue
			}
			if redacted = r.text(value, depth+1); redacted == value {
				continue
			}
		}
		b.WriteString(s[last:from])
		b.WriteString(quote(redacted))
		last = to
	}
	b.WriteString(s[last:])
	return b.String()
}

// valueAt reads the value written at s[start:], before limit: a quoted
// string, decoded, or a number or a boolean. It returns where the value is
// written; to is -1 when there is none. A quoted value that does not close
// before limit was cut short, and runs to limit, short of the next key's
// opening quote.
func valueAt(s string, start, limit int) (from, to int, value string, quoted bool) {
	if start >= limit {
		return 0, -1, "", false
	}
	if q := s[start]; q == '"' || q == '\'' {
		for j := start + 1; j < limit; j++ {
			switch s[j] {
			case '\\':
				j++
			case q:
				return start, j + 1, unescape(s[start+1 : j]), true
			}
		}
		end := limit
		if limit < len(s) {
			end--
		}
		if end <= start {
			return 0, -1, "", false
		}
		return start, end, unescape(s[start+1 : end]), true
	}
	if v := writtenScalar.FindString(s[start:limit]); v != "" {
		return start, start + len(v), v, false
	}
	return 0, -1, "", false
}

// group returns the text of submatch g of m in s, or "" when it did not take
// part in the match.
func group(s string, m []int, g int) string {
	if m[2*g] < 0 {
		return ""
	}
	return s[m[2*g]:m[2*g+1]]
}

// Escaped reports whether text, with its JSON escapes decoded, holds what
// redaction would change: a match an escape hides from the text as written,
// as in "dsn":"postgres:\/\/u:pw@h". Such text cannot be redacted safely.
// Escapes are decoded level by level, down to maxDepth levels; text that
// still holds escapes below that cannot be checked, and is reported too.
func (r *Redactor) Escaped(text string) bool {
	if len(r.rules) == 0 {
		return false
	}
	view := text
	for range maxDepth {
		next := unescape(view)
		if next == view {
			return false
		}
		if r.Text(next) != next {
			return true
		}
		view = next
	}
	return unescape(view) != view
}

// EscapedValue reports whether any string or key of v, a value Value has
// redacted, is Escaped.
func (r *Redactor) EscapedValue(v any) bool {
	switch t := v.(type) {
	case string:
		return r.Escaped(t)
	case map[string]any:
		for k, e := range t {
			if r.Escaped(k) || r.EscapedValue(e) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(t, r.EscapedValue)
	}
	return false
}

// Value redacts v, a value decoded from JSON with numbers as json.Number, and
// returns the result. Paths first replace the values they select. Patterns
// then apply to every string, key and number, and to each member with a
// scalar value written as key=value: a match that runs from the key into the
// value replaces the value whole. A number a pattern changes becomes a
// string. Keys that redaction makes equal are told apart by a suffix, " (2)",
// " (3)", in sorted order. v itself never changes: the result is a copy.
func (r *Redactor) Value(v any) any {
	v = clone(v)
	for _, rule := range r.rules {
		for _, p := range rule.Paths {
			v = p.Replace(v, func(any) any { return rule.Replacement })
		}
	}
	return r.walk(v)
}

// Change is a string of a value that redaction changed: as written, and as
// the redacted value shows it.
type Change struct {
	From, To string
}

// Changes returns the strings, keys and numbers of v, a value decoded from
// JSON with numbers as json.Number, that Value changes, each with what the
// redacted value shows instead: the text as redacted, or the replacement a
// path put where the value around it was. A string whose key alone changed
// is left out; its key is not.
func (r *Redactor) Changes(v any) []Change {
	var out []Change
	changes(v, r.Value(v), r, &out)
	return out
}

// changes walks a value and its redacted copy together. Where a path
// replaced a value, the copy holds the replacement, which stands for every
// string, key and number below; where a key changed, the copy holds
// nothing, and a string shows as redacted text.
func changes(v, redacted any, r *Redactor, out *[]Change) {
	switch t := v.(type) {
	case string:
		to, ok := redacted.(string)
		if !ok {
			to = r.Text(t)
		}
		if to != t {
			*out = append(*out, Change{From: t, To: to})
		}
	case json.Number:
		if to, ok := redacted.(string); ok {
			*out = append(*out, Change{From: t.String(), To: to})
		}
	case map[string]any:
		m, isMap := redacted.(map[string]any)
		replaced, isReplaced := redacted.(string)
		for k, e := range t {
			switch to := r.Text(k); {
			case isReplaced:
				*out = append(*out, Change{From: k, To: replaced})
			case to != k:
				*out = append(*out, Change{From: k, To: to})
			}
			next := redacted
			if isMap {
				next = m[k]
			}
			changes(e, next, r, out)
		}
	case []any:
		a, isArray := redacted.([]any)
		for i, e := range t {
			next := redacted
			if isArray {
				next = nil
				if i < len(a) {
					next = a[i]
				}
			}
			changes(e, next, r, out)
		}
	}
}

// clone copies the objects and arrays of a decoded JSON value, so that
// redacting the copy leaves the value as it was.
func clone(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = clone(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = clone(e)
		}
		return out
	}
	return v
}

func (r *Redactor) walk(v any) any {
	switch t := v.(type) {
	case string:
		return r.Text(t)
	case json.Number:
		if s := r.Text(t.String()); s != t.String() {
			return s
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for _, k := range slices.Sorted(maps.Keys(t)) {
			key := r.Text(k)
			for n := 2; ; n++ {
				if _, taken := out[key]; !taken {
					break
				}
				key = fmt.Sprintf("%s (%d)", r.Text(k), n)
			}
			if text, ok := scalar(t[k]); ok {
				if replacement, ok := r.member(k, text); ok {
					out[key] = replacement
					continue
				}
			}
			out[key] = r.walk(t[k])
		}
		return out
	case []any:
		for i := range t {
			t[i] = r.walk(t[i])
		}
		return t
	default:
		return v
	}
}

// scalar returns a string, number or boolean as text.
func scalar(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
}

// member reports whether a pattern matches a member written as key=value
// in a way that reaches the value: its match needs both the key and the
// value, matching neither alone, or runs from the key past the equals sign
// into the value. It returns that rule's replacement, which replaces the
// value whole. A match within the key or within the value is the walk's to
// replace, in part.
func (r *Redactor) member(key, value string) (string, bool) {
	kv := key + "=" + value
	for _, rule := range r.rules {
		for _, re := range rule.Patterns {
			if !re.MatchString(kv) {
				continue
			}
			if !re.MatchString(key) && !re.MatchString(value) {
				return rule.Replacement, true
			}
			for _, m := range re.FindAllStringIndex(kv, -1) {
				if m[0] <= len(key) && m[1] > len(key)+1 {
					return rule.Replacement, true
				}
			}
		}
	}
	return "", false
}

// quote writes s as a JSON string, without escaping HTML characters.
func quote(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// unescape decodes JSON string escapes in s: \" \\ \/ \b \f \n \r \t and \uXXXX, with surrogate pairs,
// and \' as Python writes it. Other backslashes stay.
func unescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch c := s[i+1]; c {
		case '"', '\\', '/', '\'':
			b.WriteByte(c)
			i++
		case 'b', 'f', 'n', 'r', 't':
			b.WriteByte(map[byte]byte{'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}[c])
			i++
		case 'u':
			r, n := unicodeEscape(s[i:])
			if n == 0 {
				b.WriteByte(s[i])
				continue
			}
			b.WriteRune(r)
			i += n - 1
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// unicodeEscape decodes \uXXXX at the start of s, and a following low surrogate,
// returning the rune and the bytes used, or 0 bytes when there is none.
func unicodeEscape(s string) (r rune, n int) {
	hex := func(t string) (rune, bool) {
		if len(t) < 6 || t[0] != '\\' || t[1] != 'u' {
			return 0, false
		}
		v, err := strconv.ParseUint(t[2:6], 16, 16)
		return rune(v), err == nil
	}
	r1, ok := hex(s)
	if !ok {
		return 0, 0
	}
	if utf16.IsSurrogate(r1) {
		if r2, ok := hex(s[6:]); ok {
			if pair := utf16.DecodeRune(r1, r2); pair != 0xFFFD {
				return pair, 12
			}
		}
	}
	return r1, 6
}
```

- [ ] **Step 4: Show a repeated hidden argument as stored**

Replace `internal/actions/app/call.go` with this version. `read` puts the rules that `repeats` returns before the tool's own, once the arguments are read, for an agent's read only:

```go
package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Response is what a caller is told of one call.
type Response struct {
	// CallID names the call in the audit store; evidence cites it.
	CallID string
	// Source is the server and tool, as server/tool.
	Source string
	Status Status
	// Text is the canonical output when Status is ok, and otherwise what
	// went wrong, redacted.
	Text string
	// Notes say what was left out of the output.
	Notes []string
}

// errStopped ends the calls of a session whose run stopped.
var errStopped = errors.New("app: the run stopped")

// recordAttempts is how often the gateway tries to record a call's outcome.
const recordAttempts = 3

// Call makes one agent's read call (spec Section 6.2). A call the gateway
// refuses, or that ends in a read error, is still a Response; the error is
// for a session that does not exist, or a revision that cannot be read.
func (g *Gateway) Call(ctx context.Context, token, name string, args json.RawMessage) (*Response, error) {
	s, err := g.p.Sessions.Session(ctx, token)
	if err != nil {
		return nil, err
	}
	rev, err := g.p.Revisions.At(ctx, s.Commit)
	if err != nil {
		return nil, fmt.Errorf("app: revision %s: %w", s.Commit, err)
	}
	ctx, cancel := g.callContext(ctx, s.Deadline, s.Done)
	defer cancel()
	list, err := g.list(ctx, &s)
	if err != nil && ctx.Err() == nil {
		return nil, err
	}
	i := slices.IndexFunc(list, func(t Tool) bool { return t.Name == name })
	if i < 0 {
		// The name is the agent's own text, so it is stored redacted, like
		// any argument.
		rec := &CallRecord{ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Tool: cut(always(rev).Text(name), maxNameBytes), Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now()}
		if err != nil {
			return g.end(ctx, rec, StatusReadError, ended(ctx)), nil
		}
		release, busy := g.flights.take(ctx, token, unknownFlight, g.opts.SessionCalls, g.opts.SessionCalls)
		if busy != nil {
			return g.end(ctx, rec, StatusReadError, "your other calls did not finish before the round ended"), nil
		}
		defer release()
		return g.end(ctx, rec, StatusRefused, "there is no tool by that name in your list"), nil
	}
	switch list[i].kind {
	case boardTool:
		return g.boardCall(ctx, token, &s, rev, &list[i], args), nil
	case writeTool:
		return g.write(ctx, token, &s, rev, &list[i], args), nil
	}
	ref := list[i].ref
	release, err := g.flights.take(ctx, token, ref.Server, g.opts.SessionCalls, g.opts.ServerCalls)
	if err != nil {
		rec := &CallRecord{ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Server: ref.Server, Tool: ref.Tool, Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now()}
		return g.end(ctx, rec, StatusReadError, "your other calls did not finish before the round ended"), nil
	}
	defer release()
	return g.read(ctx, &read{rev: rev, ref: ref, class: s.Class, run: s.Run, agent: s.Agent, round: s.Round, args: args}), nil
}

// callContext ends at the earliest of the round's end, the call timeout and
// the session's end, which it gives as the cause.
func (g *Gateway) callContext(ctx context.Context, roundEnd time.Time, done <-chan struct{}) (context.Context, context.CancelFunc) {
	deadline := g.p.Now().Add(g.opts.CallTimeout)
	if !roundEnd.IsZero() && roundEnd.Before(deadline) {
		deadline = roundEnd
	}
	ctx, stop := context.WithCancelCause(ctx)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	if done != nil {
		go func() {
			select {
			case <-done:
				stop(errStopped)
			case <-ctx.Done():
			}
		}()
	}
	return ctx, func() {
		cancel()
		stop(nil)
	}
}

// ended says why a call's context ended before its tools were listed.
func ended(ctx context.Context) string {
	if errors.Is(context.Cause(ctx), errStopped) {
		return "the run stopped"
	}
	return "your tools could not be listed before the call's time ran out"
}

// read is one read call, by an agent or by the platform.
type read struct {
	rev   *config.Revision
	ref   config.ToolRef
	class limits.Class
	run   board.RunID
	agent string
	round int
	args  json.RawMessage
	cause string // the write call a platform read is made for
}

// read runs the read path in the order of spec Section 6.2: policy, server
// limits, the audit record, forwarding, partialWhen and redaction, the
// stored outcome, then the answer.
func (g *Gateway) read(ctx context.Context, r *read) *Response {
	srv := r.rev.Server(r.ref.Server)
	policy := srv.Spec.Tools[r.ref.Tool]
	red := redactor(r.rev, policy)
	rec := &CallRecord{
		ID: g.p.IDs.NewID(), Run: r.run, Agent: r.agent, Server: r.ref.Server, Tool: r.ref.Tool,
		SchemaHash: policy.SchemaHash, Args: json.RawMessage("null"), Round: r.round, At: g.p.Now(), Cause: r.cause,
	}
	if policy.Effect != config.EffectRead {
		// Only read tools are ever forwarded: in Phase 0 no write reaches a
		// server (spec Section 6.3), whatever list a caller holds.
		return g.end(ctx, rec, StatusRefused, "only read tools are forwarded")
	}
	lim := g.p.Limits.Server(r.ref.Server)
	if err := g.look(ctx, r.rev, r.ref.Server); err != nil {
		if errors.Is(err, ErrConnection) {
			lim.Failed()
		}
		return g.end(ctx, rec, StatusReadError, describe(r.ref.Server, cause(ctx, err), red))
	}
	v, ok := g.visible(ctx, r.rev, r.ref, true)
	if !ok {
		return g.end(ctx, rec, StatusRefused, "this tool is hidden: the server does not offer the definition that was reviewed")
	}
	args, err := v.schema.Read(r.args, g.opts.MaxArgsBytes)
	if err == nil {
		rec.Args = redactArgs(red, args.Values())
		err = v.schema.CheckBounds(args, policy.Bounds)
	}
	if err != nil {
		return g.end(ctx, rec, StatusRefused, red.Text(err.Error()))
	}
	if r.agent != "" {
		if red, err = repeats(red, args.Values()); err != nil {
			return g.end(ctx, rec, StatusRefused, err.Error())
		}
	}
	release, err := lim.Acquire(ctx, r.class)
	if err != nil {
		msg := r.ref.Server + " is degraded after repeated connection failures; try again later"
		if !errors.Is(err, limits.ErrDegraded) {
			msg = "no capacity on " + r.ref.Server + " before the round ended"
		}
		return g.end(ctx, rec, StatusReadError, msg)
	}
	defer release()
	if err := g.p.Audit.Begin(ctx, rec); err != nil {
		g.callNotice(ctx, rec, StatusRefused, "")
		return &Response{CallID: rec.ID, Source: source(r.ref), Status: StatusRefused, Text: "the call could not be recorded, so it was not made"}
	}
	conn := &Conn{Server: Server{Name: r.ref.Server, Endpoint: srv.Spec.Endpoint, CredentialRef: srv.Spec.CredentialRef}}
	if r.agent != "" {
		conn.Run, conn.Agent = r.run, r.agent
	}
	res, err := g.forward(ctx, conn, r.ref.Tool, args.Raw, lim)
	status, text, notes := g.outcome(ctx, r.ref, policy, &v, red, res, err)
	if !g.record(ctx, &Outcome{ID: rec.ID, Status: status, Output: text, At: g.p.Now()}) {
		g.callNotice(ctx, rec, StatusReadError, "")
		return &Response{CallID: rec.ID, Source: source(r.ref), Status: StatusReadError, Text: "the output could not be stored, so it cannot be used"}
	}
	g.callNotice(ctx, rec, status, "")
	return &Response{CallID: rec.ID, Source: source(r.ref), Status: status, Text: text, Notes: notes}
}

// forward sends the call. A connection failure is retried while the server
// is not degraded, and counts against the server once, after the retries
// (spec Section 6.2, steps 2 and 4).
func (g *Gateway) forward(ctx context.Context, conn *Conn, tool string, args json.RawMessage, lim *limits.Server) (*output.Result, error) {
	for attempt := 0; ; attempt++ {
		res, err := g.p.Upstream.Call(ctx, conn, tool, args)
		if !errors.Is(err, ErrConnection) {
			if err == nil {
				lim.Succeeded()
			}
			return res, err
		}
		if attempt == g.opts.Retries || lim.Degraded() || sleep(ctx, g.opts.Backoff<<attempt) != nil {
			lim.Failed()
			return nil, err
		}
	}
}

// outcome turns what the server answered into a status and the text the
// gateway stores and shows.
func (g *Gateway) outcome(ctx context.Context, ref config.ToolRef, policy *config.ToolPolicy, v *visibleTool, red *redact.Redactor, res *output.Result, err error) (status Status, text string, notes []string) {
	if err != nil {
		return StatusReadError, describe(ref.Server, cause(ctx, err), red), nil
	}
	tooLarge := fmt.Sprintf("%s answered with an error larger than %d bytes", ref.Server, g.opts.MaxOutputBytes)
	if res.IsError {
		c, err := output.Read(res, false)
		if err != nil {
			return StatusReadError, ref.Server + " answered with an error that could not be read", nil
		}
		if len(c.Text) > g.opts.MaxOutputBytes {
			return StatusReadError, tooLarge, nil
		}
		shown, err := c.Show(red)
		if err != nil {
			return StatusReadError, ref.Server + " answered with an error that could not be redacted", nil
		}
		if len(shown) > g.opts.MaxOutputBytes {
			return StatusReadError, tooLarge, nil
		}
		return StatusReadError, shown, c.Notes
	}
	c, err := output.Read(res, len(v.def.OutputSchema) > 0)
	if err != nil {
		return StatusReadError, red.Text(err.Error()), nil
	}
	// The cap applies to the canonical form, so an output too large to use
	// is refused before partialWhen and redaction run on it.
	if len(c.Text) > g.opts.MaxOutputBytes {
		return StatusReadError, g.outputTooLarge(), nil
	}
	if policy.PartialWhen != "" {
		plain, ok := c.Plain()
		if !ok {
			return StatusReadError, "the output is not JSON, so its completeness cannot be checked", nil
		}
		partial, err := g.p.Conditions.PartialWhen(ctx, policy.PartialWhen, plain)
		switch {
		case err != nil:
			return StatusReadError, "the output could not be checked for completeness, so it cannot be used", nil
		case partial:
			return StatusReadError, "the output is partial, so it cannot be used: narrow the query", nil
		}
	}
	shown, err := c.Show(red)
	if err != nil {
		return StatusReadError, red.Text(err.Error()), nil
	}
	if len(shown) > g.opts.MaxOutputBytes {
		return StatusReadError, g.outputTooLarge(), nil
	}
	return StatusOK, shown, c.Notes
}

func (g *Gateway) outputTooLarge() string {
	return fmt.Sprintf("the output is larger than %d bytes, so it cannot be used: narrow the query", g.opts.MaxOutputBytes)
}

// cause returns why ctx ended in place of a context error, so that a stopped
// run is told apart from a slow server.
func cause(ctx context.Context, err error) error {
	if c := context.Cause(ctx); errors.Is(c, errStopped) {
		return errStopped
	}
	return err
}

// describe says what went wrong, in the gateway's own words: never an error
// string of the client library, which may hold URLs or payloads.
func describe(server string, err error, red *redact.Redactor) string {
	var se *ServerError
	switch {
	case errors.As(err, &se):
		return serverError(server, se.Message, red)
	case errors.Is(err, errStopped):
		return "the run stopped"
	case errors.Is(err, ErrConnection):
		return server + " could not be reached"
	case errors.Is(err, ErrTooLarge):
		return server + "'s answer is larger than the gateway reads: narrow the query"
	case errors.Is(err, ErrNeedsInput):
		return server + " asked for more input, which the gateway does not give"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return server + " gave no answer in time"
	}
	return server + "'s answer could not be read"
}

// serverError tells a server's error message redacted as an output is, as
// one text block, or that it could not be redacted.
func serverError(server, message string, red *redact.Redactor) string {
	c, err := output.Read(&output.Result{Blocks: []output.Block{{Kind: output.Text, Text: message}}}, false)
	if err == nil {
		var shown string
		if shown, err = c.Show(red); err == nil {
			return server + " answered with an error: " + cut(shown, maxServerMessage)
		}
	}
	return server + " answered with an error that could not be redacted"
}

// maxServerMessage caps the part of a server's error message an agent is
// told, after redaction, so that a cut never leaves part of a secret.
const maxServerMessage = 4 << 10

// cut shortens s to at most n bytes, on a character boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// end records a call that ended before it was forwarded, and answers it. The
// call stands as answered even if it cannot be recorded.
func (g *Gateway) end(ctx context.Context, rec *CallRecord, status Status, why string) *Response {
	begin, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.opts.RecordTimeout)
	err := g.p.Audit.Begin(begin, rec)
	cancel()
	if err == nil {
		g.record(ctx, &Outcome{ID: rec.ID, Status: status, Output: why, At: g.p.Now()})
	}
	g.callNotice(ctx, rec, status, "")
	src := ""
	if rec.Server != "" {
		src = rec.Server + "/" + rec.Tool
	}
	return &Response{CallID: rec.ID, Source: src, Status: status, Text: why}
}

// record records an outcome, also after the call's context ended, trying a
// few times. It reports whether the outcome was recorded.
func (g *Gateway) record(ctx context.Context, o *Outcome) bool {
	ctx = context.WithoutCancel(ctx)
	for range recordAttempts {
		attempt, cancel := context.WithTimeout(ctx, g.opts.RecordTimeout)
		err := g.p.Audit.End(attempt, o)
		cancel()
		if err == nil {
			return true
		}
	}
	return false
}

// redactor returns the redaction rules for a tool: the ones its policy
// names, then the ones that apply always.
func redactor(rev *config.Revision, policy *config.ToolPolicy) *redact.Redactor {
	var rules []redact.Rule
	named := map[string]bool{}
	for _, name := range policy.Redact {
		if r := rev.RedactionRule(name); r != nil {
			rules = append(rules, redact.FromConfig(r))
			named[name] = true
		}
	}
	for _, r := range rev.AlwaysRedactionRules() {
		if !named[r.Metadata.Name] {
			rules = append(rules, redact.FromConfig(r))
		}
	}
	return redact.New(rules...)
}

// Limits of the values whose repeats an output shows as stored. A value
// that folds to fewer bytes is left alone: replacing it would mangle the
// output, and it cannot carry a claim. More values than maxRepeats refuse
// the call, which bounds the work redaction does on the output.
const (
	minRepeatBytes = 4
	maxRepeats     = 32
)

// repeats returns red with rules before its own for each string, key and
// number of an agent's read arguments that red changes in the stored
// arguments: where the output repeats the value as the agent wrote it, it
// shows it as the stored arguments do. Otherwise a read that echoes its
// arguments would let an agent cite its own words, which neither the echo
// check nor a rater, who sees the stored arguments, could tell from the
// server's answer (spec Section 3.3). A value is found as a literal, in any
// case and with any run of white space where it has one, as quotes are
// compared, and in the spellings JSON gives it; longer values come first,
// so that a value inside another never splits it.
func repeats(red *redact.Redactor, values map[string]any) (*redact.Redactor, error) {
	var changes []redact.Change
	seen := map[string]bool{}
	for _, c := range red.Changes(values) {
		if len(redact.Fold(c.From)) < minRepeatBytes || seen[c.From] {
			continue
		}
		seen[c.From] = true
		changes = append(changes, c)
	}
	if len(changes) > maxRepeats {
		return nil, fmt.Errorf("the redaction rules change %d of the arguments' values, more than the %d the output can be checked for; narrow the call", len(changes), maxRepeats)
	}
	slices.SortFunc(changes, func(a, b redact.Change) int { return cmp.Compare(len(b.From), len(a.From)) })
	rules := make([]redact.Rule, 0, len(changes))
	for _, c := range changes {
		rules = append(rules, redact.Rule{Literals: spellings(c.From), Replacement: c.To})
	}
	return red.Before(rules...), nil
}

// spellings returns how an output may write a value: as it is, and escaped
// as JSON writes it, by default, with every character beyond ASCII escaped,
// and with each slash escaped too, as some encoders write it.
func spellings(v string) []string {
	raw, _ := json.Marshal(v)
	escaped := string(raw[1 : len(raw)-1])
	var ascii strings.Builder
	for _, r := range escaped {
		switch {
		case r < utf8.RuneSelf:
			ascii.WriteRune(r)
		case r > 0xFFFF:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&ascii, "\\u%04x\\u%04x", hi, lo)
		default:
			fmt.Fprintf(&ascii, "\\u%04x", r)
		}
	}
	out := []string{v}
	for _, e := range []string{escaped, ascii.String()} {
		for _, s := range []string{e, strings.ReplaceAll(e, "/", `\/`)} {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// redactArgs returns the arguments as the audit store keeps them, redacted.
func redactArgs(red *redact.Redactor, values map[string]any) json.RawMessage {
	raw, err := json.Marshal(red.Value(values))
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}

func source(ref config.ToolRef) string { return ref.Server + "/" + ref.Tool }

// maxNameBytes caps the stored name of a tool an agent asked for but cannot
// see.
const maxNameBytes = 128

// always returns the redaction rules that apply always, for text that no
// tool's rules cover.
func always(rev *config.Revision) *redact.Redactor {
	var rules []redact.Rule
	for _, r := range rev.AlwaysRedactionRules() {
		rules = append(rules, redact.FromConfig(r))
	}
	return redact.New(rules...)
}

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test ./internal/actions/... && golangci-lint run ./...`
Expected: every package `ok`; `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add internal/actions/redact/redact.go internal/actions/redact/redact_test.go internal/actions/app/call.go internal/actions/app/gateway_test.go internal/actions/app/board_test.go internal/actions/app/repeats_internal_test.go
git commit -m "fix(actions): show a hidden argument that an agent's read repeats as the stored arguments show it"
```

### Task 2: The chain's rules

Decisions 4, 7, 9 and 10, in the domain: how a record links to the one before, the one form of an anchor, and what a walk over the records finds against the anchors. No database, no network.

**Files:**
- Create: `internal/audit/chain/chain.go`, `internal/audit/chain/anchor.go`, `internal/audit/chain/verify.go`, `internal/audit/chain/chain_test.go`, `internal/audit/chain/anchor_test.go`, `internal/audit/chain/verify_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Sum [32]byte`, with `String`, `MarshalText` and `UnmarshalText`;
  - `type Link struct { Seq int64; Source, Key, Run string; Form int16; Digest, Prev, Hash Sum; ChainedAt time.Time }`;
  - `func LinkHash(prev Sum, l *Link) Sum`; `func Digest(rendered string) Sum`;
  - `type Anchor struct { Tenant string; Seq int64; Hash Sum; ChainedAt, At time.Time; After int64; Base string; Runs []string; Counts, Records map[string]int64; Unsafe []string }`, with `Key() string` and `Encode() []byte`; `func KeySeq(key string) (int64, bool)`; `func Decode(b []byte) (Anchor, error)`; `func ValidTenant(tenant string) bool`; `const MaxAnchorBytes`;
  - `type Stored struct { Key, Version string; Deleted bool; Body []byte; LockedUntil time.Time }`;
  - `func CheckAnchors(tenant string, objects []Stored) ([]Anchor, []Finding)`;
  - `type Kind string` and its constants; `type Finding struct { Kind Kind; Seq, Through int64; Source, Key, Anchor string; Runs []string; Detail string }`; `const Grace`;
  - `func NewWalk(anchors []Anchor) *Walk`, `func (w *Walk) Next(l *Link)`, `func (w *Walk) End(now time.Time, every time.Duration) []Finding`.

- [ ] **Step 1: Write the failing tests**

Create `internal/audit/chain/chain_test.go`:

```go
package chain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

var t0 = time.Date(2026, 10, 15, 9, 0, 0, 123456000, time.UTC)

// link returns a valid record at seq after prev.
func link(seq int64, prev chain.Sum) chain.Link {
	l := chain.Link{
		Seq: seq, Source: "board_events", Key: `["run-1", ` + string(rune('0'+seq%10)) + `]`, Run: "run-1", Form: 1,
		Digest: chain.Digest("row"), Prev: prev, ChainedAt: t0.Add(time.Duration(seq) * time.Second),
	}
	l.Hash = chain.LinkHash(prev, &l)
	return l
}

// links returns a valid chain of n records.
func links(n int) []chain.Link {
	out := make([]chain.Link, 0, n)
	var prev chain.Sum
	for seq := int64(1); seq <= int64(n); seq++ {
		l := link(seq, prev)
		out = append(out, l)
		prev = l.Hash
	}
	return out
}

// The link hash covers every field of a record, so that changing any one
// changes the hash.
func TestTheLinkHashCoversEveryField(t *testing.T) {
	base := link(3, chain.Digest("before"))
	want := chain.LinkHash(base.Prev, &base)
	for name, change := range map[string]func(l *chain.Link){
		"seq":       func(l *chain.Link) { l.Seq++ },
		"source":    func(l *chain.Link) { l.Source = "tool_calls" },
		"key":       func(l *chain.Link) { l.Key += " " },
		"run":       func(l *chain.Link) { l.Run = "run-2" },
		"form":      func(l *chain.Link) { l.Form = 2 },
		"digest":    func(l *chain.Link) { l.Digest[0] ^= 1 },
		"chainedAt": func(l *chain.Link) { l.ChainedAt = l.ChainedAt.Add(time.Microsecond) },
	} {
		l := base
		change(&l)
		assert.NotEqual(t, want, chain.LinkHash(l.Prev, &l), name)
	}
	other := base.Prev
	other[31] ^= 1
	assert.NotEqual(t, want, chain.LinkHash(other, &base), "prev")
}

// Fields are hashed before they are joined, so moving text from one field
// to the next changes the hash.
func TestFieldsCannotRunIntoEachOther(t *testing.T) {
	a := chain.Link{Seq: 1, Source: "ab", Key: "c", ChainedAt: t0}
	b := chain.Link{Seq: 1, Source: "a", Key: "bc", ChainedAt: t0}
	assert.NotEqual(t, chain.LinkHash(chain.Sum{}, &a), chain.LinkHash(chain.Sum{}, &b))
}

// The link hash is SHA-256 over the parts in the order Postgres writes them:
// this vector is what the migration's function computes for the same record.
func TestTheLinkHashIsTheDatabasesEncoding(t *testing.T) {
	l := chain.Link{Seq: 1, Source: "s", Key: "k", Run: "r", Form: 1, Digest: chain.Digest("d"), ChainedAt: time.UnixMicro(1).UTC()}
	part := func(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
	var b []byte
	b = append(b, make([]byte, 32)...)
	b = append(b, 0, 0, 0, 0, 0, 0, 0, 1)
	b = append(b, part("s")...)
	b = append(b, part("k")...)
	b = append(b, part("r")...)
	b = append(b, 0, 1)
	b = append(b, part("d")...)
	b = append(b, 0, 0, 0, 0, 0, 0, 0, 1)
	assert.Equal(t, chain.Sum(sha256.Sum256(b)), chain.LinkHash(chain.Sum{}, &l))
}

// A hash is written as 64 lowercase hex digits, and read only in that form.
func TestAHashIsLowercaseHex(t *testing.T) {
	s := chain.Digest("x")
	text, err := s.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(s[:]), string(text))
	var back chain.Sum
	require.NoError(t, back.UnmarshalText(text))
	assert.Equal(t, s, back)
	for _, bad := range []string{"", "00", string(text[:63]) + "G", string(text[:63]) + "A", string(text) + "0"} {
		require.Error(t, back.UnmarshalText([]byte(bad)), bad)
	}
}
```

Create `internal/audit/chain/anchor_test.go`:

```go
package chain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// anchor returns a valid anchor of tenant-a's chain at l, taken at at,
// whose base, when after is not 0, is an anchor at seq after an hour before.
func anchor(l *chain.Link, at time.Time, after int64, runs ...string) chain.Anchor {
	a := chain.Anchor{Tenant: "tenant-a", Seq: l.Seq, Hash: l.Hash, ChainedAt: l.ChainedAt, At: at, After: after, Runs: runs}
	if after > 0 {
		base := chain.Anchor{Tenant: "tenant-a", Seq: after, At: at.Add(-time.Hour)}
		a.Base = base.Key()
	}
	return a
}

func stored(a *chain.Anchor) chain.Stored { return chain.Stored{Key: a.Key(), Body: a.Encode()} }

// An anchor is written in one canonical form, and read back as written.
func TestAnAnchorRoundTrips(t *testing.T) {
	l := links(3)[2]
	a := anchor(&l, t0.Add(time.Hour), 1, "run-1", "run-2")
	a.Counts = map[string]int64{"tool_calls": 2, "board_events": 1}
	a.Records = map[string]int64{"tool_calls": 2, "board_events": 1}

	got, err := chain.Decode(a.Encode())

	require.NoError(t, err)
	assert.Equal(t, a.Encode(), got.Encode())
	assert.Equal(t, a.Hash, got.Hash)
	assert.True(t, a.ChainedAt.Equal(got.ChainedAt))
	assert.Equal(t, "tenant-a/20261015T100000Z-00000000000000000003.json", got.Key())
	assert.Equal(t, a.Counts, got.Counts)
	assert.Equal(t, a.Records, got.Records)
	assert.Contains(t, string(a.Encode()), `"counts":{"board_events":1,"tool_calls":2}`, "tables are counted in sorted order")
}

// An empty chain is anchored at seq 0 with a zero hash and no head time.
func TestAnEmptyChainIsAnchoredAtZero(t *testing.T) {
	a := chain.Anchor{Tenant: "tenant-a", At: t0}

	got, err := chain.Decode(a.Encode())

	require.NoError(t, err)
	assert.Zero(t, got.Seq)
	assert.Equal(t, `{"tenant":"tenant-a","seq":0,"hash":"0000000000000000000000000000000000000000000000000000000000000000","chainedAt":"0001-01-01T00:00:00Z","at":"2026-10-15T09:00:00.123456Z","after":0,"base":"","runs":[],"counts":{},"records":{},"unsafe":[]}`, //nolint:testifylint // the canonical form is compared byte for byte
		string(a.Encode()))
}

// Only the canonical form of a valid anchor is read: anything else in the
// store is no anchor.
func TestDecodeRefusesAnythingButAValidCanonicalAnchor(t *testing.T) {
	l := links(3)[2]
	a := anchor(&l, t0.Add(time.Hour), 1, "run-1")
	good := string(a.Encode())
	for name, body := range map[string]string{
		"spaces":          strings.Replace(good, `"seq":3`, `"seq": 3`, 1),
		"unknown field":   strings.Replace(good, `{"tenant"`, `{"extra":1,"tenant"`, 1),
		"trailing data":   good + "{}",
		"field order":     strings.Replace(strings.Replace(good, `"tenant":"tenant-a",`, "", 1), `"runs"`, `"tenant":"tenant-a","runs"`, 1),
		"upper hex":       strings.Replace(good, `"hash":"`+l.Hash.String(), `"hash":"`+strings.ToUpper(l.Hash.String()), 1),
		"bad tenant":      strings.Replace(good, `"tenant-a"`, `"Tenant/A"`, 1),
		"after past seq":  strings.Replace(good, `"after":1`, `"after":4`, 1),
		"negative seq":    strings.Replace(good, `"seq":3`, `"seq":-3`, 1),
		"unsorted runs":   strings.Replace(good, `["run-1"]`, `["run-2","run-1"]`, 1),
		"repeated runs":   strings.Replace(good, `["run-1"]`, `["run-1","run-1"]`, 1),
		"empty run":       strings.Replace(good, `["run-1"]`, `[""]`, 1),
		"no time":         strings.Replace(good, `"at":"2026-10-15T10:00:00.123456Z"`, `"at":"0001-01-01T00:00:00Z"`, 1),
		"local time":      strings.Replace(good, `"at":"2026-10-15T10:00:00.123456Z"`, `"at":"2026-10-15T21:00:00.123456+11:00"`, 1),
		"head of empty":   `{"tenant":"tenant-a","seq":0,"hash":"` + l.Hash.String() + `","chainedAt":"0001-01-01T00:00:00Z","at":"2026-10-15T09:00:00Z","after":0,"runs":[]}`,
		"no head":         strings.Replace(good, `"chainedAt":"2026-10-15T09:00:03.123456Z"`, `"chainedAt":"0001-01-01T00:00:00Z"`, 1),
		"negative count":  strings.Replace(good, `"counts":{}`, `"counts":{"tool_calls":-1}`, 1),
		"bad record":      strings.Replace(good, `"records":{}`, `"records":{"Tool Calls":1}`, 1),
		"unsorted tables": strings.Replace(good, `"unsafe":[]`, `"unsafe":["tool_hides","board_events"]`, 1),
		"bad table":       strings.Replace(good, `"unsafe":[]`, `"unsafe":["Tool Hides"]`, 1),
		"no base":         strings.Replace(good, `"base":"tenant-a/20261015T090000Z-00000000000000000001.json"`, `"base":""`, 1),
		"base elsewhere":  strings.Replace(good, `"base":"tenant-a/`, `"base":"tenant-b/`, 1),
		"base at a seq":   strings.Replace(good, `-00000000000000000001.json"`, `-00000000000000000002.json"`, 1),
		"bad table name":  strings.Replace(good, `"counts":{}`, `"counts":{"Tool Calls":1}`, 1),
		"not json":        "anchor",
		"too large":       good + strings.Repeat(" ", chain.MaxAnchorBytes),
	} {
		_, err := chain.Decode([]byte(body))
		require.Error(t, err, name)
	}
	_, err := chain.Decode([]byte(good))
	require.NoError(t, err)
}

// A tenant id names anchors only in a form that is safe in an object's name.
func TestATenantIDIsSafeInAnObjectName(t *testing.T) {
	assert.True(t, chain.ValidTenant("tenant-a"))
	for _, bad := range []string{"", "Tenant", "a/b", "../a", "-a", strings.Repeat("a", 64), "a b"} {
		assert.False(t, chain.ValidTenant(bad), bad)
	}
}

// What a store holds is read as the tenant's anchors, once per key and in
// time order; what is no anchor of the tenant under its own name, a delete
// marker, and versions of one key that differ are found.
func TestCheckAnchorsFindsWhatTheStoreHoldsWrong(t *testing.T) {
	ls := links(3)
	a1 := anchor(&ls[0], t0.Add(time.Hour), 0, "run-1")
	a2 := anchor(&ls[2], t0.Add(2*time.Hour), 1, "run-1")
	forged := a2
	forged.Hash = chain.Digest("forged")
	forged.At = t0.Add(3 * time.Hour)
	other := a1
	other.Tenant = "tenant-b"
	renamed := stored(&a1)
	renamed.Key = "tenant-a/20990101T000000Z-00000000000000000001.json"
	rewritten := a2
	rewritten.Hash = chain.Digest("rewritten")
	second := stored(&rewritten)
	second.Version = "v2"

	anchors, findings := chain.CheckAnchors("tenant-a", []chain.Stored{
		stored(&a2), stored(&a1), stored(&a1), second, stored(&forged),
		{Key: a1.Key(), Version: "v3", Deleted: true}, stored(&other), renamed, {Key: "tenant-a/junk", Body: []byte("junk")},
	})

	require.Len(t, anchors, 3)
	assert.Equal(t, []int64{1, 3, 3}, []int64{anchors[0].Seq, anchors[1].Seq, anchors[2].Seq}, "after a restore, two anchors of one seq may hold two hashes")
	var kinds []chain.Kind
	for i := range findings {
		kinds = append(kinds, findings[i].Kind)
	}
	assert.ElementsMatch(t, []chain.Kind{chain.BadAnchor, chain.AnchorConflict, chain.AnchorHidden, chain.BadAnchor, chain.BadAnchor}, kinds)
}

// Reading an anchor never fails on any bytes, and what it accepts it writes
// back unchanged.
func FuzzDecodeNeverFails(f *testing.F) {
	l := links(2)[1]
	a := anchor(&l, t0, 1, "run-1")
	f.Add(a.Encode())
	f.Add([]byte(`{"tenant":"a","seq":0}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		got, err := chain.Decode(b)
		if err != nil {
			return
		}
		assert.Equal(t, string(b), string(got.Encode()))
	})
}
```

Create `internal/audit/chain/verify_test.go`:

```go
package chain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// take returns the anchor an hourly job takes at record l, at hour h after
// t0, naming base as the latest anchor the chain still held.
func take(l *chain.Link, h int, base *chain.Anchor, runs ...string) chain.Anchor {
	a := chain.Anchor{Tenant: "tenant-a", Seq: l.Seq, Hash: l.Hash, ChainedAt: l.ChainedAt, At: t0.Add(time.Duration(h) * time.Hour), Runs: runs}
	if base != nil {
		a.Base, a.After = base.Key(), base.Seq
	}
	return a
}

// walk runs a walk over records against anchors, at now.
func walk(records []chain.Link, anchors []chain.Anchor, now time.Time) []chain.Finding {
	w := chain.NewWalk(anchors)
	for i := range records {
		w.Next(&records[i])
	}
	return w.End(now, time.Hour)
}

func kinds(findings []chain.Finding) []chain.Kind {
	out := []chain.Kind{}
	for i := range findings {
		out = append(out, findings[i].Kind)
	}
	return out
}

// rewrite returns records with every one from index i on changed and linked
// again, consistently, as an operator who rewrites history would, or as a
// chain written again after a restore is.
func rewrite(records []chain.Link, i int, run string) []chain.Link {
	out := append([]chain.Link(nil), records[:i]...)
	var prev chain.Sum
	if i > 0 {
		prev = records[i-1].Hash
	}
	for j := i; j < len(records); j++ {
		l := records[j]
		l.Run, l.Digest, l.Prev = run, chain.Digest("rewritten "+run), prev
		l.Hash = chain.LinkHash(prev, &l)
		out = append(out, l)
		prev = l.Hash
	}
	return out
}

// A chain whose records follow one another from the first, and match every
// anchor, anchored within the hour, verifies clean.
func TestACleanChainVerifies(t *testing.T) {
	ls := links(5)
	a1 := take(&ls[1], 1, nil, "run-1")
	a2 := take(&ls[4], 2, &a1, "run-1")

	assert.Empty(t, walk(ls, []chain.Anchor{a1, a2}, t0.Add(150*time.Minute)))
}

// A record that was changed, re-linked or removed shows.
func TestARecordThatWasTamperedWithShows(t *testing.T) {
	ls := links(5)
	edited := append([]chain.Link(nil), ls...)
	edited[2].Digest = chain.Digest("other")
	relinked := append([]chain.Link(nil), ls...)
	relinked[2].Prev = chain.Digest("x")
	relinked[2].Hash = chain.LinkHash(relinked[2].Prev, &relinked[2])
	removed := append(append([]chain.Link(nil), ls[:2]...), ls[3:]...)
	a := take(&ls[4], 0, nil)
	now := t0.Add(time.Minute)

	assert.Equal(t, []chain.Kind{chain.BrokenLink}, kinds(walk(edited, []chain.Anchor{a}, now)), "edited")
	assert.Equal(t, []chain.Kind{chain.BrokenLink, chain.BrokenLink}, kinds(walk(relinked, []chain.Anchor{a}, now)), "relinked")
	assert.Equal(t, []chain.Kind{chain.Gap}, kinds(walk(removed, []chain.Anchor{a}, now)), "removed")
}

// A chain rewritten consistently from a record on shows at each anchor
// after it, each a finding of its own that names its runs.
func TestARewrittenChainShowsAtItsAnchors(t *testing.T) {
	ls := links(6)
	a1 := take(&ls[1], 0, nil, "run-1")
	a2 := take(&ls[3], 1, &a1, "run-2")
	a3 := take(&ls[5], 2, &a2, "run-3")

	findings := walk(rewrite(ls, 2, "run-x"), []chain.Anchor{a1, a2, a3}, t0.Add(2*time.Hour))

	require.Equal(t, []chain.Kind{chain.Diverged, chain.Diverged}, kinds(findings))
	assert.Equal(t, int64(4), findings[0].Seq)
	assert.Equal(t, []string{"run-2"}, findings[0].Runs)
	assert.Equal(t, []string{"run-3"}, findings[1].Runs)
}

// restored returns a chain of 12 records restored to its first 7, then
// written again to 14, and the anchors an hourly job took: four before the
// restore, at seq 3, 6, 9 and 12, and two after it, at seq 10 and 14. The
// first after the restore names the anchor at seq 6 as its base: the chain
// no longer held those at seq 9 and 12.
func restored() (records []chain.Link, anchors []chain.Anchor) {
	before := links(12)
	a1 := take(&before[2], 1, nil, "run-a")
	a2 := take(&before[5], 2, &a1, "run-b")
	a3 := take(&before[8], 3, &a2, "run-c")
	a3.Counts = map[string]int64{"board_events": 9, "tool_calls": 3}
	a3.Records = map[string]int64{"board_events": 9, "tool_calls": 3}
	a4 := take(&before[11], 4, &a3, "run-d")
	records = rewrite(append(append([]chain.Link(nil), before[:7]...), links(14)[7:]...), 7, "run-new")
	a5 := take(&records[9], 5, &a2, "run-new")
	a6 := take(&records[13], 6, &a5, "run-new")
	return records, []chain.Anchor{a1, a2, a3, a4, a5, a6}
}

// After a restore, the anchor taken next names the records the chain no
// longer held, and the runs whose records went, also once new writes pass
// the old anchors.
func TestARestoreShowsWhatWasLost(t *testing.T) {
	records, anchors := restored()

	findings := walk(records, anchors, t0.Add(6*time.Hour))

	require.Equal(t, []chain.Kind{chain.Lost}, kinds(findings))
	assert.Equal(t, []int64{9, 12}, []int64{findings[0].Seq, findings[0].Through})
	assert.Equal(t, []string{"run-c", "run-d"}, findings[0].Runs)
	assert.Equal(t, anchors[4].Key(), findings[0].Anchor)
}

// Tampering after a restore shows apart from it: a rewrite of the history
// before the restore, or of the first hour after it, makes a finding at each
// anchor it changed, and, once an anchor is taken after it, one finding of
// its own, seen by that anchor.
func TestTamperingAfterARestoreShowsApartFromIt(t *testing.T) {
	records, anchors := restored()
	for name, tc := range map[string]struct {
		from    int
		changed []chain.Anchor
		held    *chain.Anchor
	}{
		"before the restore": {4, []chain.Anchor{anchors[1], anchors[4], anchors[5]}, &anchors[0]},
		"after the restore":  {8, []chain.Anchor{anchors[4], anchors[5]}, &anchors[1]},
	} {
		tampered := rewrite(records, tc.from, "run-x")
		var diverged []string
		for _, a := range tc.changed {
			diverged = append(diverged, a.Key())
		}

		findings := walk(tampered, anchors, t0.Add(6*time.Hour))

		want := []chain.Kind{}
		for range tc.changed {
			want = append(want, chain.Diverged)
		}
		assert.Equal(t, append(want, chain.Lost), kinds(findings), name)
		var gotDiverged []string
		for i := range findings {
			if findings[i].Kind == chain.Diverged {
				gotDiverged = append(gotDiverged, findings[i].Anchor)
			}
		}
		assert.Equal(t, diverged, gotDiverged, name)

		after := take(&tampered[len(tampered)-1], 7, tc.held, "run-x")
		findings = walk(tampered, append(append([]chain.Anchor(nil), anchors...), after), t0.Add(7*time.Hour))

		var lostBy []string
		for i := range findings {
			require.Equal(t, chain.Lost, findings[i].Kind, "%s: %s", name, findings[i].String())
			lostBy = append(lostBy, findings[i].Anchor)
		}
		assert.Equal(t, []string{anchors[4].Key(), after.Key()}, lostBy, name)
	}
}

// An anchor whose base is no earlier anchor in the store sees nothing lost,
// and shows.
func TestAnAnchorWithoutItsBaseShows(t *testing.T) {
	ls := links(4)
	a1 := take(&ls[1], 0, nil)
	elsewhere := take(&ls[0], 1, nil)
	a2 := take(&ls[3], 1, &elsewhere)

	findings := walk(ls, []chain.Anchor{a1, a2}, t0.Add(time.Hour))

	assert.Equal(t, []chain.Kind{chain.BadAnchor}, kinds(findings))
}

// A chain whose first record is not the first ever written was cut: the
// records before it were deleted, and anchors of them show too.
func TestAChainThatDoesNotStartAtItsFirstRecordWasCut(t *testing.T) {
	ls := links(6)
	a1 := take(&ls[2], 0, nil, "run-1")
	a2 := take(&ls[5], 1, &a1, "run-2")

	findings := walk(ls[3:], []chain.Anchor{a1, a2}, t0.Add(time.Hour))

	assert.Equal(t, []chain.Kind{chain.Truncated, chain.Diverged}, kinds(findings))
	assert.Equal(t, []int64{1, 3}, []int64{findings[0].Seq, findings[0].Through})
}

// An anchor that counted more rows of a table than the chain held records
// of it shows rows that were written unchained, even once they are gone and
// the chain no longer holds the anchor: both counts are the anchor's own.
func TestRowsAnAnchorCountedButTheChainNeverHeldShow(t *testing.T) {
	ls := links(3)
	empty := chain.Anchor{Tenant: "tenant-a", At: t0.Add(-time.Hour), Counts: map[string]int64{"tool_hides": 1}}
	honest := take(&ls[1], 0, &empty, "run-1")
	honest.Counts = map[string]int64{"board_events": 2, "tool_calls": 0}
	honest.Records = map[string]int64{"board_events": 2}
	padded := take(&ls[2], 1, &honest, "run-1")
	padded.Counts = map[string]int64{"board_events": 3, "tool_calls": 4}
	padded.Records = map[string]int64{"board_events": 3}
	anchors := []chain.Anchor{empty, honest, padded}

	for name, records := range map[string][]chain.Link{"held": ls, "rewritten": rewrite(ls, 1, "run-x")} {
		var unchained []string
		for _, f := range walk(records, anchors, t0.Add(time.Hour)) {
			if f.Kind == chain.UnchainedRows {
				unchained = append(unchained, f.Source)
			}
		}
		assert.Equal(t, []string{"tool_hides", "tool_calls"}, unchained, name)
	}
}

// Anchoring that stopped, or never began, shows.
func TestStoppedAnchoringShows(t *testing.T) {
	ls := links(3)
	first := take(&ls[0], 0, nil)
	late := []chain.Anchor{first, take(&ls[2], 3, &first)}

	assert.Equal(t, []chain.Kind{chain.AnchorGap}, kinds(walk(ls, late, t0.Add(3*time.Hour))))
	assert.Equal(t, []chain.Kind{chain.AnchorStale}, kinds(walk(ls, late[:1], t0.Add(71*time.Minute))))
	assert.Empty(t, walk(ls, late[:1], t0.Add(69*time.Minute)))
	assert.Equal(t, []chain.Kind{chain.AnchorStale}, kinds(walk(ls, nil, t0)))
}

// An anchor dated after now is a finding, but its seq and hash still count:
// records it alone covers that the chain no longer holds still show.
func TestAnAnchorDatedAheadStillCountsAsEvidence(t *testing.T) {
	ls := links(4)
	a1 := take(&ls[1], 0, nil, "run-1")
	ahead := take(&ls[3], 24*365, &a1, "run-2")

	findings := walk(rewrite(ls, 2, "run-x"), []chain.Anchor{a1, ahead}, t0.Add(time.Hour))

	assert.ElementsMatch(t, []chain.Kind{chain.BadAnchor, chain.Diverged}, kinds(findings))
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/audit/chain/`
Expected: FAIL: `no non-test Go files in .../internal/audit/chain`

- [ ] **Step 3: Link records**

Create `internal/audit/chain/chain.go`:

```go
// Package chain holds the rules of the audit chain (spec Section 9.4): how
// each audited row links to the one before it, what an anchor outside the
// database holds, and what a verification finds when the two disagree.
package chain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Sum is a SHA-256 hash.
type Sum [sha256.Size]byte

// String returns the hash in lowercase hex.
func (s Sum) String() string { return hex.EncodeToString(s[:]) }

// MarshalText writes the hash in lowercase hex.
func (s Sum) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText reads a hash written in lowercase hex.
func (s *Sum) UnmarshalText(text []byte) error {
	if len(text) != 2*len(s) {
		return fmt.Errorf("chain: a hash is %d hex digits, not %d", 2*len(s), len(text))
	}
	for _, c := range text {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return errors.New("chain: a hash is written in lowercase hex")
		}
	}
	_, err := hex.Decode(s[:], text)
	return err
}

// Link is one record of the chain: an audited row, named by its table and
// its key, with the digest of the row as the database renders it.
type Link struct {
	Seq int64
	// Source is the audited table; Key is the row's primary key within the
	// tenant, as a JSON array; Run is the row's run, or empty.
	Source, Key, Run string
	// Form numbers the list of columns the digest covers, per source.
	Form   int16
	Digest Sum
	// Prev is the hash of the record before, zero for the first.
	Prev Sum
	Hash Sum
	// ChainedAt is when the record was chained, to the microsecond.
	ChainedAt time.Time
}

// LinkHash returns the hash that links l after the record whose hash is
// prev. It covers every field of l in fixed-width parts, so that no field
// can run into the next: prev, the sequence number, the hashes of the
// source, the key and the run, the form, the digest, and the chaining time
// in microseconds since the epoch. Integers are big-endian, as Postgres's
// int8send and int2send write them.
func LinkHash(prev Sum, l *Link) Sum {
	b := make([]byte, 0, 4*sha256.Size+sha256.Size+8+2+sha256.Size+8)
	b = append(b, prev[:]...)
	b, _ = binary.Append(b, binary.BigEndian, l.Seq)
	for _, s := range []string{l.Source, l.Key, l.Run} {
		h := sha256.Sum256([]byte(s))
		b = append(b, h[:]...)
	}
	b, _ = binary.Append(b, binary.BigEndian, l.Form)
	b = append(b, l.Digest[:]...)
	b, _ = binary.Append(b, binary.BigEndian, l.ChainedAt.UnixMicro())
	return sha256.Sum256(b)
}

// Digest returns the digest of a row as the database rendered it: its
// columns as one JSON array, in the text Postgres writes for it.
func Digest(rendered string) Sum { return sha256.Sum256([]byte(rendered)) }
```

- [ ] **Step 4: Write and read anchors**

Create `internal/audit/chain/anchor.go`:

```go
package chain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// MaxAnchorBytes caps an anchor object. An hour's anchor names the runs that
// added records in that hour, which stays far below it.
const MaxAnchorBytes = 4 << 20

// tenantName is what a tenant id in an anchor's key may look like.
var tenantName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// sourceName is what an audited table's name in an anchor's counts may look
// like.
var sourceName = regexp.MustCompile(`^[a-z][a-z_]{0,62}$`)

// ValidTenant reports whether a tenant id can name anchors.
func ValidTenant(tenant string) bool { return tenantName.MatchString(tenant) }

// Anchor is what the anchor store's owner writes outside the database every
// hour, all read from one snapshot: the head of a tenant's chain, the
// latest earlier anchor the chain still held, the runs whose records the
// chain gained since, how many rows each audited table held beside how many
// records the chain held of it, and the tables whose state verification
// could not trust then (spec Sections 8.4 and 9.4). An empty chain is
// anchored at Seq 0 with a zero Hash.
type Anchor struct {
	Tenant string `json:"tenant"`
	// Seq and Hash are the chain's last record when the anchor was taken,
	// and ChainedAt its chaining time, zero for an empty chain.
	Seq       int64     `json:"seq"`
	Hash      Sum       `json:"hash"`
	ChainedAt time.Time `json:"chainedAt"`
	// At is when the anchor was taken.
	At time.Time `json:"at"`
	// Base names the latest anchor the chain still held when this one was
	// taken, and After is its Seq; both are empty for the first anchor.
	// After a restore, the anchors between Base and this one are those whose
	// records the restore lost. Runs are the runs of the records after
	// After up to Seq, sorted.
	After int64    `json:"after"`
	Base  string   `json:"base"`
	Runs  []string `json:"runs"`
	// Counts are the rows each audited table held for the tenant, and
	// Records the records the chain held of each, so that rows no record
	// chained show later, even once they are gone.
	Counts  map[string]int64 `json:"counts"`
	Records map[string]int64 `json:"records"`
	// Unsafe names the tables, the chain's own among them, whose state
	// verification could not trust when the anchor was taken, as a trigger
	// that rewrites rows before they are chained; sorted. It keeps what a
	// verification run later might no longer see.
	Unsafe []string `json:"unsafe"`
}

// Key returns the anchor's object name: the tenant, the anchor's time to the
// second and its Seq, so that names sort by time.
func (a *Anchor) Key() string {
	return fmt.Sprintf("%s/%s-%020d.json", a.Tenant, a.At.UTC().Format("20060102T150405Z"), a.Seq)
}

// anchorKey is what an anchor's object name looks like; group 1 is its
// tenant and group 2 its Seq.
var anchorKey = regexp.MustCompile(`^([a-z0-9][a-z0-9-]{0,62})/[0-9]{8}T[0-9]{6}Z-([0-9]{20})\.json$`)

// KeySeq returns the Seq an anchor's object name gives, and false for a name
// that is no anchor's, so that a reader can pass over an anchor without
// reading it.
func KeySeq(key string) (int64, bool) {
	m := anchorKey.FindStringSubmatch(key)
	if len(m) != 3 {
		return 0, false
	}
	seq, err := strconv.ParseInt(m[2], 10, 64)
	return seq, err == nil
}

// Encode writes the anchor in its one canonical form: compact JSON, times in
// UTC, runs as an array.
func (a *Anchor) Encode() []byte {
	c := *a
	c.ChainedAt, c.At = c.ChainedAt.UTC(), c.At.UTC()
	if c.Runs == nil {
		c.Runs = []string{}
	}
	if c.Counts == nil {
		c.Counts = map[string]int64{}
	}
	if c.Records == nil {
		c.Records = map[string]int64{}
	}
	if c.Unsafe == nil {
		c.Unsafe = []string{}
	}
	b, _ := json.Marshal(&c)
	return b
}

// Decode reads an anchor. It refuses anything but the canonical form of a
// valid anchor, so that one anchor has one text.
func Decode(b []byte) (Anchor, error) {
	if len(b) > MaxAnchorBytes {
		return Anchor{}, fmt.Errorf("chain: an anchor is at most %d bytes", MaxAnchorBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var a Anchor
	if err := dec.Decode(&a); err != nil {
		return Anchor{}, fmt.Errorf("chain: anchor: %w", err)
	}
	if err := a.check(); err != nil {
		return Anchor{}, err
	}
	if !bytes.Equal(a.Encode(), b) {
		return Anchor{}, errors.New("chain: the anchor is not in its canonical form")
	}
	return a, nil
}

// check reports what makes an anchor invalid.
func (a *Anchor) check() error {
	switch {
	case !ValidTenant(a.Tenant):
		return fmt.Errorf("chain: %q is not a tenant id", a.Tenant)
	case a.Seq < 0 || a.After < 0 || a.After > a.Seq:
		return fmt.Errorf("chain: an anchor's seq %d must follow the seq %d of its base", a.Seq, a.After)
	case a.At.IsZero():
		return errors.New("chain: an anchor names when it was taken")
	case (a.Seq == 0) != a.ChainedAt.IsZero(), a.Seq == 0 && a.Hash != Sum{}:
		return errors.New("chain: only an anchor of an empty chain has no head")
	}
	if err := a.checkBase(); err != nil {
		return err
	}
	return a.checkLists()
}

// checkBase reports a base that is not an anchor of the tenant at After.
func (a *Anchor) checkBase() error {
	if a.Base == "" {
		if a.After != 0 {
			return errors.New("chain: an anchor that follows another names it as its base")
		}
		return nil
	}
	m := anchorKey.FindStringSubmatch(a.Base)
	if len(m) != 3 || m[1] != a.Tenant || m[2] != fmt.Sprintf("%020d", a.After) {
		return fmt.Errorf("chain: %q is no anchor of the tenant at seq %d", a.Base, a.After)
	}
	return nil
}

// checkLists reports runs that are not distinct, sorted and named, tables
// that are not, and counts that are not of audited tables.
func (a *Anchor) checkLists() error {
	for i, run := range a.Runs {
		if run == "" || i > 0 && a.Runs[i-1] >= run {
			return errors.New("chain: an anchor's runs are distinct, sorted and named")
		}
	}
	for i, table := range a.Unsafe {
		if !sourceName.MatchString(table) || i > 0 && a.Unsafe[i-1] >= table {
			return errors.New("chain: an anchor's unsafe tables are distinct, sorted and named")
		}
	}
	for _, counts := range []map[string]int64{a.Counts, a.Records} {
		for source, n := range counts {
			if !sourceName.MatchString(source) || n < 0 {
				return fmt.Errorf("chain: an anchor counts rows and records of audited tables, not %d of %q", n, source)
			}
		}
	}
	return nil
}

// Stored is one object, or one version of an object, in an anchor store.
type Stored struct {
	Key string
	// Version names the version, in a store that keeps versions.
	Version string
	// Deleted marks a delete marker: a version that hides the key from a
	// listing without versions.
	Deleted bool
	Body    []byte
	// LockedUntil is when the object's retention in compliance mode ends,
	// zero when it has none.
	LockedUntil time.Time
}

// CheckAnchors reads a tenant's anchors from every object and version a
// store holds. It returns the anchors, one per key, ordered by time and
// sequence, and what it found wrong: an object that is no valid anchor of
// the tenant under its own name, a key hidden by a delete marker, and
// versions of one key that differ. Two anchors of one Seq may hold two
// hashes: after a restore, the chain writes that Seq again.
func CheckAnchors(tenant string, objects []Stored) ([]Anchor, []Finding) {
	var findings []Finding
	byKey := map[string]Anchor{}
	for i := range objects {
		o := &objects[i]
		if o.Deleted {
			findings = append(findings, Finding{Kind: AnchorHidden, Anchor: o.Key, Detail: "a delete marker hides the anchor from a listing without versions"})
			continue
		}
		a, err := Decode(o.Body)
		switch {
		case err != nil:
			findings = append(findings, Finding{Kind: BadAnchor, Anchor: o.Key, Detail: err.Error()})
			continue
		case a.Tenant != tenant || a.Key() != o.Key:
			findings = append(findings, Finding{Kind: BadAnchor, Anchor: o.Key, Detail: "the object holds the anchor named " + a.Key()})
			continue
		}
		if seen, ok := byKey[o.Key]; ok {
			if !bytes.Equal(seen.Encode(), a.Encode()) {
				findings = append(findings, Finding{Kind: AnchorConflict, Seq: a.Seq, Anchor: o.Key, Detail: "two versions of the anchor differ"})
			}
			continue
		}
		byKey[o.Key] = a
	}
	anchors := make([]Anchor, 0, len(byKey))
	for k := range byKey {
		anchors = append(anchors, byKey[k])
	}
	slices.SortFunc(anchors, func(x, y Anchor) int {
		if c := x.At.Compare(y.At); c != 0 {
			return c
		}
		return int(min(max(x.Seq-y.Seq, -1), 1))
	})
	return anchors, findings
}
```

- [ ] **Step 5: Walk the chain against its anchors**

Create `internal/audit/chain/verify.go`:

```go
package chain

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Kind names what a verification found.
type Kind string

// What a verification can find.
const (
	// BrokenLink: a record whose prev is not the hash of the record before,
	// or whose hash does not recompute.
	BrokenLink Kind = "broken_link"
	// Gap: sequence numbers missing between records.
	Gap Kind = "gap"
	// Truncated: a chain whose first record is not the first ever written.
	// Records are never pruned in this phase, so the records before it were
	// deleted.
	Truncated Kind = "truncated"
	// ChangedRow: an audited row whose digest differs from its record's.
	ChangedRow Kind = "changed_row"
	// MissingRow: a record whose audited row is gone.
	MissingRow Kind = "missing_row"
	// UnchainedRow: an audited row that no record chains.
	UnchainedRow Kind = "unchained_row"
	// UnchainedRows: an anchor counted more rows of a table than the chain
	// held records of it, so rows were written that no record chained.
	UnchainedRows Kind = "unchained_rows"
	// UnknownForm: a record whose form this build cannot render.
	UnknownForm Kind = "unknown_form"
	// Lost: anchors whose records the chain no longer held when a later
	// anchor was taken, between that anchor's base and it: a restore, a
	// deletion or a rewrite. Only the store's owner can tell which.
	Lost Kind = "lost"
	// Diverged: an anchor whose records the chain no longer holds, which no
	// anchor taken since has seen: a restore, a deletion or a rewrite since.
	Diverged Kind = "diverged"
	// AnchorGap: consecutive anchors further apart than the interval allows.
	AnchorGap Kind = "anchor_gap"
	// AnchorStale: no anchor within the interval before the verification.
	AnchorStale Kind = "anchor_stale"
	// AnchorConflict: versions of one anchor, or anchors of one seq, that
	// disagree.
	AnchorConflict Kind = "anchor_conflict"
	// AnchorHidden: a delete marker that hides an anchor.
	AnchorHidden Kind = "anchor_hidden"
	// BadAnchor: an object in the store that is no valid anchor of the
	// tenant under its own name.
	BadAnchor Kind = "bad_anchor"
	// StoreUnlocked: an anchor store, or an object in it, that is not kept
	// write-once.
	StoreUnlocked Kind = "store_unlocked"
	// Table: an audited table that is not a plain table, filters rows by
	// row security, lets another trigger or a rule act on its inserts, or
	// does not chain what is inserted into it, now or when an anchor was
	// taken.
	Table Kind = "table"
)

// Grace is how late an anchor may be past its interval.
const Grace = 10 * time.Minute

// Finding is one thing a verification found.
type Finding struct {
	Kind Kind
	// Seq is the chain position the finding concerns, and Through the last
	// position of a range.
	Seq, Through int64
	// Source and Key name the audited row or table it concerns.
	Source, Key string
	// Anchor names the anchor object it concerns.
	Anchor string
	// Runs are the runs that may have lost records.
	Runs   []string
	Detail string
}

// String writes the finding on one line.
func (f *Finding) String() string {
	var b strings.Builder
	b.WriteString(string(f.Kind))
	switch {
	case f.Through > f.Seq:
		fmt.Fprintf(&b, " seq %d-%d", f.Seq, f.Through)
	case f.Seq > 0:
		fmt.Fprintf(&b, " seq %d", f.Seq)
	}
	if f.Source != "" {
		fmt.Fprintf(&b, " %s %s", f.Source, f.Key)
	}
	if f.Anchor != "" {
		fmt.Fprintf(&b, " anchor %s", f.Anchor)
	}
	if len(f.Runs) > 0 {
		fmt.Fprintf(&b, " runs %s", strings.Join(f.Runs, ","))
	}
	if f.Detail != "" {
		b.WriteString(": " + f.Detail)
	}
	return b.String()
}

// Walk checks a chain record by record, in sequence order, against its
// anchors.
type Walk struct {
	anchors []Anchor
	bySeq   map[int64][]int
	// held marks the anchors whose seq and hash a record matched.
	held     []bool
	first    *Link
	last     *Link
	findings []Finding
}

// NewWalk starts a walk against anchors, as CheckAnchors returns them, in
// time order.
func NewWalk(anchors []Anchor) *Walk {
	w := &Walk{anchors: anchors, bySeq: map[int64][]int{}, held: make([]bool, len(anchors))}
	for i := range anchors {
		w.bySeq[anchors[i].Seq] = append(w.bySeq[anchors[i].Seq], i)
		w.checkCounts(&anchors[i])
		for _, table := range anchors[i].Unsafe {
			w.add(&Finding{Kind: Table, Source: table, Anchor: anchors[i].Key(),
				Detail: "when the anchor was taken, verification could not trust the table's state"})
		}
	}
	for _, i := range w.bySeq[0] {
		w.held[i] = true
	}
	return w
}

// Next checks the next record: the first is the first ever written; each
// follows the one before without a gap, names its hash as prev, and hashes
// as it says. An anchor of its seq that holds its hash is held.
func (w *Walk) Next(l *Link) {
	switch {
	case w.last == nil:
		if l.Seq != 1 || l.Prev != (Sum{}) {
			w.add(&Finding{Kind: Truncated, Seq: 1, Through: l.Seq - 1, Detail: "the chain does not start at its first record: records before it were deleted"})
		}
	case l.Seq != w.last.Seq+1:
		w.add(&Finding{Kind: Gap, Seq: w.last.Seq + 1, Through: l.Seq - 1, Detail: "records are missing"})
	case l.Prev != w.last.Hash:
		w.add(&Finding{Kind: BrokenLink, Seq: l.Seq, Source: l.Source, Key: l.Key, Detail: "prev is not the hash of the record before"})
	}
	if LinkHash(l.Prev, l) != l.Hash {
		w.add(&Finding{Kind: BrokenLink, Seq: l.Seq, Source: l.Source, Key: l.Key, Detail: "the hash does not recompute"})
	}
	for _, i := range w.bySeq[l.Seq] {
		w.held[i] = w.anchors[i].Hash == l.Hash
	}
	c := *l
	if w.first == nil {
		w.first = &c
	}
	w.last = &c
}

// checkCounts reports each table of which an anchor counted more rows than
// the chain held records of it then. Both counts are the anchor's own, so
// the finding stands whatever the chain holds now.
func (w *Walk) checkCounts(a *Anchor) {
	for _, source := range slices.Sorted(maps.Keys(a.Counts)) {
		if n := a.Counts[source]; n > a.Records[source] {
			w.add(&Finding{Kind: UnchainedRows, Seq: a.Seq, Source: source, Anchor: a.Key(),
				Detail: fmt.Sprintf("the anchor counted %d rows, and the chain held %d records of them", n, a.Records[source])})
		}
	}
}

// End returns every finding: those of the records, then what only the
// whole chain shows. An anchor the chain does not hold was seen lost by a
// later anchor when it lies between that anchor and the base it named, the
// latest anchor the chain still held then. Each such later anchor makes
// one finding of what it saw lost, with the runs whose records went, and
// when they went: between its base and it. Every other anchor the chain
// does not hold makes a finding of its own. Whether records went to a
// restore, a deletion or a rewrite, only the store's owner can tell.
// Anchors further apart than every plus Grace, or none that recent, mean
// anchoring stopped. An anchor dated after now plus Grace, as a clock ahead
// writes it, is a finding; its seq and hash still count, but it neither
// sees others lost nor shows when anchoring last happened.
func (w *Walk) End(now time.Time, every time.Duration) []Finding {
	ahead := make([]bool, len(w.anchors))
	for i := range w.anchors {
		if ahead[i] = w.anchors[i].At.After(now.Add(Grace)); ahead[i] {
			w.add(&Finding{Kind: BadAnchor, Anchor: w.anchors[i].Key(),
				Detail: "the anchor is dated after now: the clock of the host that took it was ahead"})
		}
	}
	seenBy := w.seenLost(ahead)
	lost := map[int][]int{}
	for x := range w.anchors {
		switch {
		case w.held[x]:
		case seenBy[x] >= 0:
			lost[seenBy[x]] = append(lost[seenBy[x]], x)
		default:
			a := &w.anchors[x]
			w.add(&Finding{Kind: Diverged, Seq: a.Seq, Anchor: a.Key(), Runs: a.Runs,
				Detail: "the chain no longer holds the anchor's records, and no anchor taken since has seen it: a restore, a deletion or a rewrite since the anchor"})
		}
	}
	for _, by := range slices.Sorted(maps.Keys(lost)) {
		w.reportLost(by, lost[by])
	}
	w.reportStopped(now, every, ahead)
	return w.findings
}

// seenLost returns, for each anchor, the earliest later anchor that saw it
// lost, or -1. An anchor that names a base the store does not hold before
// it sees nothing, and is itself a finding; one dated ahead sees nothing.
func (w *Walk) seenLost(ahead []bool) []int {
	index := map[string]int{}
	for i := range w.anchors {
		index[w.anchors[i].Key()] = i
	}
	seenBy := make([]int, len(w.anchors))
	for i := range seenBy {
		seenBy[i] = -1
	}
	for i := range w.anchors {
		a := &w.anchors[i]
		if a.Base == "" || ahead[i] {
			continue
		}
		b, ok := index[a.Base]
		if !ok || b >= i {
			w.add(&Finding{Kind: BadAnchor, Anchor: a.Key(), Detail: "its base " + a.Base + " is no earlier anchor in the store"})
			continue
		}
		for x := b + 1; x < i; x++ {
			if !w.held[x] && seenBy[x] < 0 {
				seenBy[x] = i
			}
		}
	}
	return seenBy
}

// reportLost makes one finding of the anchors that anchor by saw lost.
func (w *Walk) reportLost(by int, anchors []int) {
	f := Finding{Kind: Lost, Anchor: w.anchors[by].Key(), Seq: w.anchors[anchors[0]].Seq,
		Detail: fmt.Sprintf("the chain no longer held the records of %d anchors when this anchor was taken: a restore, a deletion or a rewrite after %s, the latest anchor it still held", len(anchors), w.anchors[by].Base)}
	for _, x := range anchors {
		f.Seq, f.Through = min(f.Seq, w.anchors[x].Seq), max(f.Through, w.anchors[x].Seq)
		f.Runs = append(f.Runs, w.anchors[x].Runs...)
	}
	slices.Sort(f.Runs)
	f.Runs = slices.Compact(f.Runs)
	w.add(&f)
}

// reportStopped reports anchors further apart than every plus Grace, or
// none that recent, among those not dated ahead.
func (w *Walk) reportStopped(now time.Time, every time.Duration, ahead []bool) {
	var dated []*Anchor
	for i := range w.anchors {
		if !ahead[i] {
			dated = append(dated, &w.anchors[i])
		}
	}
	limit := every + Grace
	for i := 1; i < len(dated); i++ {
		if gap := dated[i].At.Sub(dated[i-1].At); gap > limit {
			w.add(&Finding{Kind: AnchorGap, Anchor: dated[i].Key(), Detail: fmt.Sprintf("%s after the anchor before", gap.Round(time.Second))})
		}
	}
	switch n := len(dated); {
	case n == 0:
		w.add(&Finding{Kind: AnchorStale, Detail: "no anchor was taken"})
	case now.Sub(dated[n-1].At) > limit:
		w.add(&Finding{Kind: AnchorStale, Anchor: dated[n-1].Key(), Detail: fmt.Sprintf("the latest anchor is %s old", now.Sub(dated[n-1].At).Round(time.Second))})
	}
}

func (w *Walk) add(f *Finding) { w.findings = append(w.findings, *f) }
```

- [ ] **Step 6: Run the tests to see them pass, and fuzz the decoder**

Run: `go test ./internal/audit/chain/ && go test ./internal/audit/chain/ -run XXX -fuzz FuzzDecodeNeverFails -fuzztime 20s -fuzzminimizetime 0 && golangci-lint run ./...`
Expected: `ok`, then `PASS` after millions of executions; `0 issues.`

- [ ] **Step 7: Commit**

```bash
git add internal/audit/chain
git commit -m "feat(audit): the audit chain's rules: links, anchors and what a walk over them finds"
```

### Task 3: Anchoring and verification

Decisions 8 to 10, as use cases over two ports: the chain in one database snapshot, and the store outside the database.

**Files:**
- Create: `internal/audit/app/auditor.go`, `internal/audit/app/auditor_test.go`

**Interfaces:**
- Consumes: Task 2's `chain` package.
- Produces:
  - `type Chain interface { Read(ctx context.Context, fn func(View) error) error }`;
  - `type View interface { Head; HashAt; Runs; Counts; Records; Links; Rows; Unchained; Tables }`, with the signatures in `auditor.go` below;
  - `type Row struct { Seq int64; Source, Key string; Form int16; Digest chain.Sum; Known bool; Rendered []byte }`;
  - `type Table struct { Name string; Plain, Filtered, Chains, Altered bool }`;
  - `type Anchors interface { Put(ctx, key string, body []byte) error; List(ctx, tenant string) ([]chain.Stored, error); Read(ctx, o *chain.Stored) error; Retention(ctx, o *chain.Stored) error; Locked(ctx) (bool, string, error) }`; `var ErrExists`;
  - `func New(c Chain, a Anchors, tenant string, now func() time.Time) (*Auditor, error)`;
  - `func (a *Auditor) Anchor(ctx) (chain.Anchor, error)` and `func (a *Auditor) Verify(ctx, every time.Duration) ([]chain.Finding, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/audit/app/auditor_test.go`:

```go
package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

var t0 = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

// fakeChain is a chain held in memory: its records, each with a run, the
// rows verification reads, and how its tables are held. It is its own view.
type fakeChain struct {
	links     []chain.Link
	rows      []app.Row
	unchained []app.Row
	counts    map[string]int64
	tables    []app.Table
}

func (c *fakeChain) Read(_ context.Context, fn func(app.View) error) error { return fn(c) }

func (c *fakeChain) Head(context.Context) (*chain.Link, error) {
	if len(c.links) == 0 {
		return nil, nil
	}
	l := c.links[len(c.links)-1]
	return &l, nil
}

func (c *fakeChain) HashAt(_ context.Context, seq int64) (chain.Sum, bool, error) {
	for i := range c.links {
		if c.links[i].Seq == seq {
			return c.links[i].Hash, true, nil
		}
	}
	return chain.Sum{}, false, nil
}

func (c *fakeChain) Runs(_ context.Context, after, upTo int64) ([]string, error) {
	var runs []string
	for i := range c.links {
		if l := &c.links[i]; l.Seq > after && l.Seq <= upTo && l.Run != "" && !slices.Contains(runs, l.Run) {
			runs = append(runs, l.Run)
		}
	}
	slices.Sort(runs)
	return runs, nil
}

func (c *fakeChain) Counts(context.Context) (map[string]int64, error) { return c.counts, nil }

func (c *fakeChain) Records(context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for i := range c.links {
		out[c.links[i].Source]++
	}
	return out, nil
}

func (c *fakeChain) Links(_ context.Context, fn func(*chain.Link) error) error {
	for i := range c.links {
		if err := fn(&c.links[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *fakeChain) Rows(_ context.Context, fn func(*app.Row) error) error {
	for i := range c.rows {
		if err := fn(&c.rows[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *fakeChain) Unchained(context.Context) ([]app.Row, error) { return c.unchained, nil }

func (c *fakeChain) Tables(context.Context) ([]app.Table, error) { return c.tables, nil }

// add appends a record of run to the chain, and its row.
func (c *fakeChain) add(run string) {
	var prev chain.Sum
	if n := len(c.links); n > 0 {
		prev = c.links[n-1].Hash
	}
	rendered := []byte(`["row ` + run + `"]`)
	l := chain.Link{Seq: int64(len(c.links) + 1), Source: "board_events", Key: `["k"]`, Run: run, Form: 1,
		Digest: chain.Digest(string(rendered)), Prev: prev, ChainedAt: t0.Add(time.Duration(len(c.links)) * time.Minute)}
	l.Hash = chain.LinkHash(prev, &l)
	c.links = append(c.links, l)
	c.rows = append(c.rows, app.Row{Seq: l.Seq, Source: l.Source, Key: l.Key, Form: 1, Digest: l.Digest, Known: true, Rendered: rendered})
}

// fakeAnchors is a store held in memory. A locked one keeps what it writes
// under retention for a year. It counts the bodies it reads.
type fakeAnchors struct {
	objects []chain.Stored
	locked  bool
	reads   int
}

func (s *fakeAnchors) Put(_ context.Context, key string, body []byte) error {
	if slices.ContainsFunc(s.objects, func(o chain.Stored) bool { return o.Key == key }) {
		return app.ErrExists
	}
	o := chain.Stored{Key: key, Body: body}
	if s.locked {
		o.LockedUntil = t0.AddDate(1, 0, 0)
	}
	s.objects = append(s.objects, o)
	return nil
}

func (s *fakeAnchors) List(context.Context, string) ([]chain.Stored, error) {
	out := make([]chain.Stored, 0, len(s.objects))
	for _, o := range s.objects {
		out = append(out, chain.Stored{Key: o.Key, Version: o.Version, Deleted: o.Deleted})
	}
	return out, nil
}

func (s *fakeAnchors) find(o *chain.Stored) *chain.Stored {
	for i := range s.objects {
		if s.objects[i].Key == o.Key && s.objects[i].Version == o.Version {
			return &s.objects[i]
		}
	}
	return nil
}

func (s *fakeAnchors) Read(_ context.Context, o *chain.Stored) error {
	s.reads++
	o.Body = s.find(o).Body
	return nil
}

// Retention fails in a store that keeps nothing write-once, as an S3 bucket
// without object lock does.
func (s *fakeAnchors) Retention(_ context.Context, o *chain.Stored) error {
	if !s.locked {
		return errors.New("the bucket is missing its object lock configuration")
	}
	o.LockedUntil = s.find(o).LockedUntil
	return nil
}

func (s *fakeAnchors) Locked(context.Context) (locked bool, why string, err error) {
	if s.locked {
		return true, "", nil
	}
	return false, "a fake", nil
}

func newAuditor(t *testing.T, c *fakeChain, s app.Anchors, now *time.Time) *app.Auditor {
	t.Helper()
	a, err := app.New(c, s, "tenant-a", func() time.Time { return *now })
	require.NoError(t, err)
	return a
}

func kinds(findings []chain.Finding) []chain.Kind {
	out := []chain.Kind{}
	for i := range findings {
		out = append(out, findings[i].Kind)
	}
	return out
}

// An anchor holds the chain's head, and the runs whose records the chain
// gained since the anchor before, its base.
func TestAnAnchorHoldsTheHeadAndTheRunsSinceTheAnchorBefore(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	c.add("run-2")
	first, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.add("run-2")
	c.add("run-3")
	now = now.Add(time.Hour)

	second, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []string{"run-1", "run-2"}, first.Runs)
	assert.Equal(t, int64(4), second.Seq)
	assert.Equal(t, c.links[3].Hash, second.Hash)
	assert.Equal(t, first.Key(), second.Base)
	assert.Equal(t, int64(2), second.After)
	assert.Equal(t, []string{"run-2", "run-3"}, second.Runs)
	assert.Len(t, s.objects, 2)
}

// An anchor counts the rows of each audited table, and the records the chain
// holds of it, from the same snapshot as the head.
func TestAnAnchorCountsTheRowsAndRecordsOfEachTable(t *testing.T) {
	c := &fakeChain{counts: map[string]int64{"board_events": 1, "tool_calls": 0}}
	c.add("run-1")
	s := &fakeAnchors{locked: true}
	now := t0

	an, err := newAuditor(t, c, s, &now).Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"board_events": 1, "tool_calls": 0}, an.Counts)
	assert.Equal(t, map[string]int64{"board_events": 1}, an.Records)
}

// An empty chain is anchored at seq 0, so that deleting everything shows.
func TestAnEmptyChainIsAnchored(t *testing.T) {
	s := &fakeAnchors{locked: true}
	now := t0

	an, err := newAuditor(t, &fakeChain{}, s, &now).Anchor(t.Context())

	require.NoError(t, err)
	assert.Zero(t, an.Seq)
	require.Len(t, s.objects, 1)
	_, err = chain.Decode(s.objects[0].Body)
	require.NoError(t, err)
}

// After a restore, the next anchor names as its base the latest anchor the
// chain still holds, and counts its runs from there: an anchor whose seq the
// chain wrote again, with other records, is lost, not a base. It is written
// all the same, since a missing anchor would hide the restore.
func TestAnAnchorAfterARestoreNamesTheLatestAnchorTheChainHolds(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	first, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.add("run-2")
	c.add("run-3")
	now = now.Add(time.Hour)
	lost, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.links, c.rows = c.links[:1], c.rows[:1]
	c.add("run-new")
	c.add("run-new")
	c.add("run-newer")
	now = now.Add(time.Hour)

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, first.Key(), lost.Base)
	assert.Equal(t, int64(4), an.Seq)
	assert.Equal(t, first.Key(), an.Base)
	assert.Equal(t, int64(1), an.After)
	assert.Equal(t, []string{"run-new", "run-newer"}, an.Runs)
}

// An anchor reads the anchors newest first, and only until it finds its
// base: a year of hourly anchors costs one read an hour.
func TestAnAnchorReadsOnlyTheAnchorsItNeeds(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	for range 30 {
		c.add("run-1")
		_, err := a.Anchor(t.Context())
		require.NoError(t, err)
		now = now.Add(time.Hour)
	}
	s.reads = 0

	_, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 1, s.reads)
}

// An anchor passes over anchors dated after now, as a host whose clock ran
// ahead writes them: its base is always an anchor taken before it.
func TestAnAnchorPassesOverAnchorsDatedAfterNow(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	first, err := a.Anchor(t.Context())
	require.NoError(t, err)
	now = t0.Add(48 * time.Hour)
	ahead, err := a.Anchor(t.Context())
	require.NoError(t, err)
	now = t0.Add(time.Hour)

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, first.Key(), an.Base)
	assert.NotEqual(t, ahead.Key(), an.Base)
}

// The chain is never anchored empty once records were anchored, as while a
// restore is under way: the first anchor after the restore must see its
// losses.
func TestAnAnchorRefusesAnEmptyChainOnceRecordsWereAnchored(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.links, c.rows = nil, nil
	now = now.Add(time.Hour)

	_, err = a.Anchor(t.Context())

	require.ErrorContains(t, err, "anchor once a restore is done")
	assert.Len(t, s.objects, 1)
}

// An anchor is written once: a second one for the same time and head fails.
func TestAnAnchorIsNeverWrittenTwice(t *testing.T) {
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, &fakeChain{}, s, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)

	_, err = a.Anchor(t.Context())

	require.ErrorIs(t, err, app.ErrExists)
}

// A clean chain, anchored within the hour in a write-once store, verifies
// with no finding.
func TestACleanChainVerifies(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	c.add("run-2")
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	now = now.Add(30 * time.Minute)

	got, err := a.Verify(t.Context(), time.Hour)

	require.NoError(t, err)
	assert.Empty(t, got)
}

// Verification reports what the rows, the chain and the store show: a row
// changed, a row gone, a record of a form this build cannot render, a row
// no record chains, and a store that is not write-once.
func TestVerificationReportsEveryKindOfFinding(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{}
	now := t0
	a := newAuditor(t, c, s, &now)
	for range 4 {
		c.add("run-1")
	}
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.rows[0].Rendered = []byte(`["changed"]`)
	c.rows[1].Rendered = nil
	c.rows[2].Known, c.rows[2].Rendered = false, nil
	c.unchained = []app.Row{{Source: "tool_hides", Key: `["x"]`}}

	got, err := a.Verify(t.Context(), time.Hour)

	require.NoError(t, err)
	assert.ElementsMatch(t, []chain.Kind{chain.StoreUnlocked, chain.ChangedRow, chain.MissingRow, chain.UnknownForm, chain.UnchainedRow}, kinds(got))
}

// An anchor dated after now is a finding, and takes no part in the walk:
// it neither serves as a base nor hides that anchoring stopped.
func TestVerificationReportsAnAnchorDatedAfterNow(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	now = t0.AddDate(1, 0, 0)
	_, err = a.Anchor(t.Context())
	require.NoError(t, err)
	now = t0.Add(5 * time.Hour)

	got, err := a.Verify(t.Context(), time.Hour)

	require.NoError(t, err)
	assert.ElementsMatch(t, []chain.Kind{chain.BadAnchor, chain.AnchorStale}, kinds(got))
}

// unsafeTables are tables verification cannot trust: one that is no plain
// table, one that row security filters, one whose inserts are not chained,
// one whose inserts another trigger or a rule acts on, one that is missing.
var unsafeTables = []app.Table{
	{Name: "audit_chain", Plain: true, Chains: true},
	{Name: "board_events", Plain: false, Chains: true},
	{Name: "tool_calls", Plain: true, Filtered: true, Chains: true},
	{Name: "tool_hides", Plain: true},
	{Name: "config_revisions", Plain: true, Chains: true, Altered: true},
	{Name: "config_objects"},
}

// tableFindings returns the tables that table findings name, and the
// anchors that saw them, apart.
func tableFindings(got []chain.Finding) (now, anchored []string) {
	for i := range got {
		switch {
		case got[i].Kind != chain.Table:
		case got[i].Anchor == "":
			now = append(now, got[i].Source)
		default:
			anchored = append(anchored, got[i].Source)
		}
	}
	return now, anchored
}

// Rows verification cannot trust show, table by table.
func TestVerificationReportsTablesItCannotTrust(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.tables = unsafeTables

	got, err := a.Verify(t.Context(), time.Hour)

	require.NoError(t, err)
	current, anchored := tableFindings(got)
	assert.Equal(t, []string{"board_events", "tool_calls", "tool_hides", "config_revisions", "config_objects"}, current)
	assert.Empty(t, anchored)
}

// An anchor keeps the tables verification could not trust when it was
// taken, so that a trigger dropped again before verification runs still
// shows.
func TestTablesUnsafeWhenAnAnchorWasTakenStillShow(t *testing.T) {
	c := &fakeChain{tables: unsafeTables}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	an, err := a.Anchor(t.Context())
	require.NoError(t, err)
	c.tables = nil

	got, err := a.Verify(t.Context(), time.Hour)

	require.NoError(t, err)
	assert.Equal(t, []string{"board_events", "config_objects", "config_revisions", "tool_calls", "tool_hides"}, an.Unsafe)
	current, anchored := tableFindings(got)
	assert.Empty(t, current)
	assert.Equal(t, an.Unsafe, anchored)
}

// In a locked store, each anchor object must itself be under retention in
// compliance mode: one whose retention ended can be deleted.
func TestEachAnchorMustBeUnderComplianceRetention(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	s.objects[0].LockedUntil = t0.Add(-time.Minute)

	got, err := a.Verify(t.Context(), time.Hour)

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, chain.StoreUnlocked, got[0].Kind)
	assert.Equal(t, s.objects[0].Key, got[0].Anchor)
}

// A tenant id that cannot name anchors is refused.
func TestATenantIDMustNameAnchors(t *testing.T) {
	_, err := app.New(&fakeChain{}, &fakeAnchors{}, "Tenant/A", time.Now)

	require.Error(t, err)
}

// A store that cannot be read fails verification; it never verifies clean.
func TestAStoreThatCannotBeReadFailsVerification(t *testing.T) {
	now := t0
	a := newAuditor(t, &fakeChain{}, &brokenAnchors{}, &now)

	_, err := a.Verify(t.Context(), time.Hour)

	require.Error(t, err)
}

type brokenAnchors struct{ fakeAnchors }

func (*brokenAnchors) List(context.Context, string) ([]chain.Stored, error) {
	return nil, errors.New("store down")
}

// After a restore far back, the anchors past the chain's head are passed
// over unread: the chain cannot hold them, and reading a year of them would
// hold the database snapshot open.
func TestAnAnchorPassesOverAnchorsPastTheHeadUnread(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	first, err := a.Anchor(t.Context())
	require.NoError(t, err)
	for range 20 {
		now = now.Add(time.Hour)
		c.add("run-2")
		_, err := a.Anchor(t.Context())
		require.NoError(t, err)
	}
	c.links, c.rows = c.links[:1], c.rows[:1]
	now = now.Add(time.Hour)
	s.reads = 0

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, first.Key(), an.Base)
	assert.Equal(t, 2, s.reads, "the newest anchor, then the base")
}

// An anchor reads one version of each anchor it reads: versions of one key
// are verification's to compare.
func TestAnAnchorReadsOneVersionOfEachAnchor(t *testing.T) {
	c := &fakeChain{}
	s := &fakeAnchors{locked: true}
	now := t0
	a := newAuditor(t, c, s, &now)
	c.add("run-1")
	c.add("run-1")
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	for _, v := range []string{"v2", "v3", "v4"} {
		o := s.objects[0]
		o.Version = v
		s.objects = append(s.objects, o)
	}
	c.links, c.rows = c.links[:1], c.rows[:1]
	c.add("run-x")
	now = now.Add(time.Hour)
	s.reads = 0

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Empty(t, an.Base, "the chain no longer holds the anchor")
	assert.Equal(t, 1, s.reads)
}

// An anchor that would not read back is never written: the job fails, so
// that the store never holds an anchor that verification would set aside.
func TestAnAnchorThatWouldNotReadBackIsNeverWritten(t *testing.T) {
	c := &fakeChain{}
	c.add("run-1")
	c.links[0].Source = "Not A Table"
	s := &fakeAnchors{locked: true}
	now := t0

	_, err := newAuditor(t, c, s, &now).Anchor(t.Context())

	require.ErrorContains(t, err, "would not read back")
	assert.Empty(t, s.objects)
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/audit/app/`
Expected: FAIL: `no non-test Go files in .../internal/audit/app`

- [ ] **Step 3: Anchor and verify**

Create `internal/audit/app/auditor.go`:

```go
// Package app holds the Audit context's use cases: anchoring the audit
// chain outside the database, and verifying the chain, its rows and its
// anchors (spec Sections 8.4 and 9.4). It declares the ports they need.
package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// Chain reads a tenant's audit chain, and the rows it covers, from the
// database.
type Chain interface {
	// Read calls fn with the chain and its rows as one snapshot of the
	// database shows them.
	Read(ctx context.Context, fn func(View) error) error
}

// View is a tenant's chain and the rows it covers, as of one moment.
type View interface {
	// Head returns the chain's last record, or nil when the chain is empty.
	Head(ctx context.Context) (*chain.Link, error)
	// HashAt returns the hash of the record at seq, and false when the
	// chain has no record there.
	HashAt(ctx context.Context, seq int64) (chain.Sum, bool, error)
	// Runs returns the distinct runs of the records after seq after, up to
	// and with seq upTo, sorted.
	Runs(ctx context.Context, after, upTo int64) ([]string, error)
	// Counts returns the rows each audited table holds.
	Counts(ctx context.Context) (map[string]int64, error)
	// Records returns the records the chain holds of each audited table.
	Records(ctx context.Context) (map[string]int64, error)
	// Links calls fn with every record, in sequence order.
	Links(ctx context.Context, fn func(*chain.Link) error) error
	// Rows calls fn with every record's row, as the database renders it.
	Rows(ctx context.Context, fn func(*Row) error) error
	// Unchained returns the audited rows that no record names.
	Unchained(ctx context.Context) ([]Row, error)
	// Tables returns how the database holds each audited table.
	Tables(ctx context.Context) ([]Table, error)
}

// Row is an audited row as verification reads it: the record that chains it
// and, when the row still exists and this build knows the record's form,
// the row's columns as the database renders them.
type Row struct {
	Seq         int64
	Source, Key string
	Form        int16
	Digest      chain.Sum
	// Known is false for a form this build cannot render.
	Known bool
	// Rendered is nil when the row is gone.
	Rendered []byte
}

// Table is how the database holds an audited table. Verification trusts
// what it reads only from a plain table that no row security filters, and
// whose inserts its chain trigger chains.
type Table struct {
	Name                    string
	Plain, Filtered, Chains bool
	// Altered is true when another trigger, or a rule, acts on the
	// table's inserts, which could change or drop a row before it is
	// chained.
	Altered bool
}

// Anchors is the store outside the database that keeps anchors write-once
// (spec Section 9.4). Its owner, outside the builders, takes the anchors;
// the platform holds no credential for it.
type Anchors interface {
	// Put writes body under key, once: it returns ErrExists when the key
	// holds an object already.
	Put(ctx context.Context, key string, body []byte) error
	// List returns every object under the tenant's name, every version of
	// each and delete markers included, without their bodies.
	List(ctx context.Context, tenant string) ([]chain.Stored, error)
	// Read fills in the body of an object List returned.
	Read(ctx context.Context, o *chain.Stored) error
	// Retention fills in when an object's retention in compliance mode
	// ends.
	Retention(ctx context.Context, o *chain.Stored) error
	// Locked reports whether the store keeps what it holds write-once, and
	// why not when it does not.
	Locked(ctx context.Context) (bool, string, error)
}

// ErrExists is returned by Anchors.Put for a key that holds an object.
var ErrExists = errors.New("app: the anchor exists")

// Auditor anchors and verifies one tenant's audit chain.
type Auditor struct {
	chain   Chain
	anchors Anchors
	tenant  string
	now     func() time.Time
}

// New returns the auditor of a tenant's chain. now is the clock.
func New(c Chain, a Anchors, tenant string, now func() time.Time) (*Auditor, error) {
	if !chain.ValidTenant(tenant) {
		return nil, fmt.Errorf("app: %q is not a tenant id that can name anchors", tenant)
	}
	return &Auditor{chain: c, anchors: a, tenant: tenant, now: now}, nil
}

// Anchor writes one anchor, from one snapshot: the chain's head; its base,
// the latest anchor, dated before now, whose record the chain still holds
// at its seq; the runs of the records since the base; how many rows each
// audited table holds beside how many records the chain holds of it; and
// the tables whose state verification could not trust now. It reads
// anchors newest first, passes over those whose name puts them past the
// head, and stops at the base. It anchors whatever the chain holds, also
// when earlier anchors disagree with it: verification reports the
// disagreement, and a missing anchor would hide it. It refuses to anchor an
// empty chain once records were anchored, as during a restore, whose losses
// the first anchor after it must see, and it never writes an anchor that
// would not read back.
func (a *Auditor) Anchor(ctx context.Context) (chain.Anchor, error) {
	objects, err := a.anchors.List(ctx, a.tenant)
	if err != nil {
		return chain.Anchor{}, fmt.Errorf("list anchors: %w", err)
	}
	an := chain.Anchor{Tenant: a.tenant, At: a.now().UTC()}
	err = a.chain.Read(ctx, func(v View) error {
		head, err := v.Head(ctx)
		if err != nil {
			return err
		}
		if head != nil {
			an.Seq, an.Hash, an.ChainedAt = head.Seq, head.Hash, head.ChainedAt.UTC()
		}
		base, latest, err := a.findBase(ctx, v, objects, &an)
		if err != nil {
			return err
		}
		if head == nil && latest != nil && latest.Seq > 0 {
			return fmt.Errorf("the chain is empty, but anchor %s holds %d records: anchor once a restore is done", latest.Key(), latest.Seq)
		}
		if base != nil {
			an.Base, an.After = base.Key(), base.Seq
		}
		return describe(ctx, v, &an)
	})
	if err != nil {
		return chain.Anchor{}, fmt.Errorf("read the chain: %w", err)
	}
	if _, err := chain.Decode(an.Encode()); err != nil {
		return chain.Anchor{}, fmt.Errorf("the anchor would not read back: %w", err)
	}
	if err := a.anchors.Put(ctx, an.Key(), an.Encode()); err != nil {
		return chain.Anchor{}, fmt.Errorf("write anchor %s: %w", an.Key(), err)
	}
	return an, nil
}

// describe reads into an what an anchor holds beside its head and base: the
// runs since the base, the rows and records of each table, and the tables
// verification could not trust now.
func describe(ctx context.Context, v View, an *chain.Anchor) (err error) {
	if an.Runs, err = v.Runs(ctx, an.After, an.Seq); err != nil {
		return err
	}
	if an.Counts, err = v.Counts(ctx); err != nil {
		return err
	}
	if an.Records, err = v.Records(ctx); err != nil {
		return err
	}
	tables, err := v.Tables(ctx)
	for _, f := range checkTables(tables) {
		an.Unsafe = append(an.Unsafe, f.Source)
	}
	slices.Sort(an.Unsafe)
	an.Unsafe = slices.Compact(an.Unsafe)
	return err
}

// findBase reads the anchors dated before an, newest first, and returns the
// first whose record the chain still holds at its seq, and the newest. Once
// it has the newest, it passes over, unread, every anchor whose name puts
// its seq past the head: the chain cannot hold it. An object that is no
// anchor of the tenant under its own name is passed over.
func (a *Auditor) findBase(ctx context.Context, v View, objects []chain.Stored, an *chain.Anchor) (base, latest *chain.Anchor, err error) {
	for _, o := range newestFirst(objects) {
		if seq, ok := chain.KeySeq(o.Key); !ok || latest != nil && seq > an.Seq {
			continue
		}
		if err := a.anchors.Read(ctx, &o); err != nil {
			return nil, nil, fmt.Errorf("read anchor %s: %w", o.Key, err)
		}
		got, err := chain.Decode(o.Body)
		if err != nil || got.Tenant != a.tenant || got.Key() != o.Key || !got.At.Before(an.At) {
			continue
		}
		if latest == nil {
			latest = &got
		}
		held, err := holds(ctx, v, &got, an.Seq)
		if err != nil {
			return nil, nil, err
		}
		if held {
			return &got, latest, nil
		}
	}
	return nil, latest, nil
}

// newestFirst returns one version of each key that is no delete marker,
// newest key first: an anchor's key begins with its time.
func newestFirst(objects []chain.Stored) []chain.Stored {
	seen := map[string]bool{}
	var keys []chain.Stored
	for _, o := range objects {
		if !o.Deleted && !seen[o.Key] {
			seen[o.Key] = true
			keys = append(keys, o)
		}
	}
	slices.SortFunc(keys, func(x, y chain.Stored) int { return strings.Compare(y.Key, x.Key) })
	return keys
}

// holds reports whether the chain, whose head is at seq head, still holds
// an anchor's record at its seq. An anchor of the empty chain is held by
// every chain.
func holds(ctx context.Context, v View, an *chain.Anchor, head int64) (bool, error) {
	switch {
	case an.Seq > head:
		return false, nil
	case an.Seq == 0:
		return true, nil
	}
	hash, ok, err := v.HashAt(ctx, an.Seq)
	return ok && hash == an.Hash, err
}

// Verify checks the chain, the rows it covers and its anchors, and returns
// what it found; every is the anchor interval. It lists and reads the
// anchors before it reads the database, so that an anchor taken meanwhile
// never names a record the snapshot lacks, and it reads the whole chain.
func (a *Auditor) Verify(ctx context.Context, every time.Duration) ([]chain.Finding, error) {
	now := a.now()
	objects, err := a.read(ctx)
	if err != nil {
		return nil, err
	}
	anchors, findings := chain.CheckAnchors(a.tenant, objects)
	locked, why, err := a.anchors.Locked(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the anchor store's lock: %w", err)
	}
	if locked {
		for i := range objects {
			if objects[i].Deleted {
				continue
			}
			if err := a.anchors.Retention(ctx, &objects[i]); err != nil {
				return nil, fmt.Errorf("read the retention of anchor %s: %w", objects[i].Key, err)
			}
		}
	}
	findings = append(findings, retention(objects, locked, why, now)...)
	w := chain.NewWalk(anchors)
	err = a.chain.Read(ctx, func(v View) error {
		tables, err := v.Tables(ctx)
		if err != nil {
			return err
		}
		findings = append(findings, checkTables(tables)...)
		if err := v.Links(ctx, func(l *chain.Link) error { w.Next(l); return nil }); err != nil {
			return err
		}
		err = v.Rows(ctx, func(r *Row) error {
			if f, ok := checkRow(r); ok {
				findings = append(findings, f)
			}
			return nil
		})
		if err != nil {
			return err
		}
		unchained, err := v.Unchained(ctx)
		for i := range unchained {
			findings = append(findings, chain.Finding{Kind: chain.UnchainedRow, Source: unchained[i].Source, Key: unchained[i].Key,
				Detail: "no record chains the row"})
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read the chain: %w", err)
	}
	return append(findings, w.End(now, every)...), nil
}

// read lists every object of the tenant and reads each that is no delete
// marker.
func (a *Auditor) read(ctx context.Context) ([]chain.Stored, error) {
	objects, err := a.anchors.List(ctx, a.tenant)
	if err != nil {
		return nil, fmt.Errorf("list anchors: %w", err)
	}
	for i := range objects {
		if objects[i].Deleted {
			continue
		}
		if err := a.anchors.Read(ctx, &objects[i]); err != nil {
			return nil, fmt.Errorf("read anchor %s: %w", objects[i].Key, err)
		}
	}
	return objects, nil
}

// retention reports a store that is not write-once, or, in one that is,
// each object whose retention in compliance mode has ended or never began.
func retention(objects []chain.Stored, locked bool, why string, now time.Time) []chain.Finding {
	if !locked {
		return []chain.Finding{{Kind: chain.StoreUnlocked, Detail: why}}
	}
	var out []chain.Finding
	for i := range objects {
		if o := &objects[i]; !o.Deleted && !o.LockedUntil.After(now) {
			out = append(out, chain.Finding{Kind: chain.StoreUnlocked, Anchor: o.Key, Detail: "the object is not under retention in compliance mode"})
		}
	}
	return out
}

// checkTables reports each audited table whose rows verification cannot
// trust, or whose inserts are not chained.
func checkTables(tables []Table) []chain.Finding {
	var out []chain.Finding
	for _, t := range tables {
		switch {
		case !t.Plain:
			out = append(out, chain.Finding{Kind: chain.Table, Source: t.Name, Detail: "not a plain table"})
		case t.Filtered:
			out = append(out, chain.Finding{Kind: chain.Table, Source: t.Name,
				Detail: "row security can hide rows from verification, or a table of the same name in another schema can take the platform's writes"})
		case !t.Chains:
			out = append(out, chain.Finding{Kind: chain.Table, Source: t.Name, Detail: "its chain trigger is missing or disabled"})
		case t.Altered:
			out = append(out, chain.Finding{Kind: chain.Table, Source: t.Name, Detail: "another trigger, or a rule, acts on its inserts before they are chained"})
		}
	}
	return out
}

// checkRow compares a row with its record.
func checkRow(r *Row) (chain.Finding, bool) {
	f := chain.Finding{Seq: r.Seq, Source: r.Source, Key: r.Key}
	switch {
	case !r.Known:
		f.Kind, f.Detail = chain.UnknownForm, fmt.Sprintf("this build cannot render form %d", r.Form)
	case r.Rendered == nil:
		f.Kind, f.Detail = chain.MissingRow, "the row is gone"
	case chain.Digest(string(r.Rendered)) != r.Digest:
		f.Kind, f.Detail = chain.ChangedRow, "the row differs from what was chained"
	default:
		return chain.Finding{}, false
	}
	return f, true
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./internal/audit/app/ && golangci-lint run ./...`
Expected: `ok`; `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal/audit/app
git commit -m "feat(audit): anchor the chain from one snapshot, and verify it against its anchors"
```

### Task 4: The anchor stores

Decision 11. Two adapters of the `Anchors` port: an S3 bucket with object lock, the store ADR-016 assumes, and a directory for development and tests.

**Files:**
- Create: `internal/audit/dirstore/dirstore.go`, `internal/audit/dirstore/dirstore_test.go`, `internal/audit/s3store/s3store.go`, `internal/audit/s3store/s3store_test.go`, `internal/audit/s3store/s3store_integration_test.go`
- Modify: `go.mod`, `go.sum` (with `go get`)

**Interfaces:**
- Consumes: Task 2's `chain.Stored`, `chain.ValidTenant` and `chain.MaxAnchorBytes`; Task 3's `app.ErrExists`.
- Produces:
  - `func dirstore.Open(dir string) (*dirstore.Store, error)`, with `Close`, `Put`, `List`, `Read`, `Retention` and `Locked`;
  - `type s3store.Config struct { Endpoint string; Insecure bool; Region, Bucket, Prefix string }` and `func s3store.New(cfg *Config, keyID, secretKey string) (*s3store.Store, error)`, with `Put`, `List`, `Read`, `Retention` and `Locked`.

- [ ] **Step 1: Write the failing tests**

Create `internal/audit/dirstore/dirstore_test.go`:

```go
package dirstore_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
	"github.com/jchen0426/agent-swarm/internal/audit/dirstore"
)

func open(t *testing.T) (s *dirstore.Store, dir string) {
	t.Helper()
	dir = t.TempDir()
	s, err := dirstore.Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

// An anchor is written once, as a file under its tenant's directory, and
// listed with every other file there, each read on its own.
func TestAnAnchorIsWrittenOnceAndListed(t *testing.T) {
	s, dir := open(t)
	ctx := t.Context()

	require.NoError(t, s.Put(ctx, "tenant-a/a.json", []byte("one")))
	require.ErrorIs(t, s.Put(ctx, "tenant-a/a.json", []byte("two")), app.ErrExists)
	require.NoError(t, s.Put(ctx, "tenant-b/b.json", []byte("other tenant")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tenant-a", "planted.json"), []byte("planted"), 0o600))

	got, err := s.List(ctx, "tenant-a")
	require.NoError(t, err)
	for i := range got {
		require.NoError(t, s.Read(ctx, &got[i]))
	}

	assert.ElementsMatch(t, []chain.Stored{{Key: "tenant-a/a.json", Body: []byte("one")}, {Key: "tenant-a/planted.json", Body: []byte("planted")}}, got)
}

// A tenant without anchors lists none.
func TestATenantWithoutAnchorsListsNone(t *testing.T) {
	s, _ := open(t)

	got, err := s.List(t.Context(), "tenant-a")

	require.NoError(t, err)
	assert.Empty(t, got)
}

// Names never leave the directory, and a file too large to be an anchor is
// listed cut, so that it never decodes.
func TestNamesStayInTheDirectory(t *testing.T) {
	s, dir := open(t)
	ctx := t.Context()
	for _, bad := range []string{"../a.json", "/a.json", "a.json", "tenant-a/x/a.json", "tenant-a/../../a.json"} {
		require.Error(t, s.Put(ctx, bad, []byte("x")), bad)
	}
	_, err := s.List(ctx, "../tenant-a")
	require.Error(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tenant-a"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tenant-a", "big.json"), make([]byte, chain.MaxAnchorBytes+10), 0o600))

	got, err := s.List(ctx, "tenant-a")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NoError(t, s.Read(ctx, &got[0]))

	assert.Len(t, got[0].Body, chain.MaxAnchorBytes+1)
}

// A directory is not write-once, and keeps no file under retention.
func TestADirectoryIsNotWriteOnce(t *testing.T) {
	s, _ := open(t)
	require.NoError(t, s.Put(t.Context(), "tenant-a/a.json", []byte("one")))
	o := chain.Stored{Key: "tenant-a/a.json"}

	locked, why, err := s.Locked(t.Context())

	require.NoError(t, err)
	assert.False(t, locked)
	assert.Contains(t, why, "not write-once")
	require.NoError(t, s.Retention(t.Context(), &o))
	assert.True(t, o.LockedUntil.IsZero())
}
```

Create `internal/audit/s3store/s3store_test.go`:

```go
package s3store_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/s3store"
)

// Plain HTTP reaches a loopback host only, where tests run their store; an
// anchor store anywhere else is reached over HTTPS.
func TestPlainHTTPIsForALoopbackHostOnly(t *testing.T) {
	for _, host := range []string{"127.0.0.1:9000", "localhost:9000", "[::1]:9000"} {
		_, err := s3store.New(&s3store.Config{Endpoint: host, Insecure: true, Bucket: "b"}, "id", "secret")
		require.NoError(t, err, host)
	}
	for _, host := range []string{"anchors.example:9000", "10.0.0.1:9000", "localhost.example:9000"} {
		_, err := s3store.New(&s3store.Config{Endpoint: host, Insecure: true, Bucket: "b"}, "id", "secret")
		require.ErrorContains(t, err, "loopback", host)
	}
	_, err := s3store.New(&s3store.Config{Endpoint: "anchors.example", Bucket: "b"}, "id", "secret")
	require.NoError(t, err)
	_, err = s3store.New(&s3store.Config{Endpoint: "anchors.example", Bucket: "b"}, "", "secret")
	require.Error(t, err)
}
```

Create `internal/audit/s3store/s3store_integration_test.go`. It runs an S3 store with object lock in a container; the bucket's owner makes each bucket, as a real owner would:

```go
//go:build integration

package s3store_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
	"github.com/jchen0426/agent-swarm/internal/audit/s3store"
)

// image is an S3 store with object lock, pinned by digest.
const image = "quay.io/minio/minio:RELEASE.2024-12-18T13-15-44Z@sha256:1dce27c494a16bae114774f1cec295493f3613142713130c2d22dd5696be6ad3"

const user, password = "anchor-owner", "anchor-owner-password"

var endpoint string

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, image,
		testcontainers.WithExposedPorts("9000/tcp"),
		testcontainers.WithEnv(map[string]string{"MINIO_ROOT_USER": user, "MINIO_ROOT_PASSWORD": password}),
		testcontainers.WithCmd("server", "/data"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/live").WithPort("9000")),
	)
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			slog.ErrorContext(ctx, "remove the S3 container", "error", err)
		}
	}()
	if err != nil {
		slog.ErrorContext(ctx, "start the S3 container", "error", err)
		return 1
	}
	if endpoint, err = ctr.PortEndpoint(ctx, "9000/tcp", ""); err != nil {
		slog.ErrorContext(ctx, "read the S3 endpoint", "error", err)
		return 1
	}
	return m.Run()
}

// list lists the tenant's objects, and reads each that is no delete marker,
// with its retention when retained is set.
func list(t *testing.T, s *s3store.Store, tenant string, retained bool) []chain.Stored {
	t.Helper()
	got, err := s.List(t.Context(), tenant)
	require.NoError(t, err)
	for i := range got {
		if got[i].Deleted {
			continue
		}
		require.NoError(t, s.Read(t.Context(), &got[i]))
		if retained {
			require.NoError(t, s.Retention(t.Context(), &got[i]))
		}
	}
	return got
}

// owner is the bucket owner's client.
func owner(t *testing.T) *minio.Client {
	t.Helper()
	c, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(user, password, "")})
	require.NoError(t, err)
	return c
}

// bucket makes a bucket as its owner would: with object lock, and a default
// retention of one day in mode, unless mode is empty.
func bucket(t *testing.T, name string, lock bool, mode minio.RetentionMode) *s3store.Store {
	t.Helper()
	c := owner(t)
	require.NoError(t, c.MakeBucket(t.Context(), name, minio.MakeBucketOptions{ObjectLocking: lock}))
	if mode != "" {
		days, unit := uint(1), minio.Days
		require.NoError(t, c.SetObjectLockConfig(t.Context(), name, &mode, &days, &unit))
	}
	s, err := s3store.New(&s3store.Config{Endpoint: endpoint, Insecure: true, Bucket: name, Prefix: "anchors/"}, user, password)
	require.NoError(t, err)
	return s
}

// An anchor is written once: a second write under its name is refused, so
// the platform never makes a second version of an anchor.
func TestAnAnchorIsWrittenOnce(t *testing.T) {
	s := bucket(t, "written-once", true, minio.Compliance)

	require.NoError(t, s.Put(t.Context(), "tenant-a/a.json", []byte("one")))
	err := s.Put(t.Context(), "tenant-a/a.json", []byte("two"))

	require.ErrorIs(t, err, app.ErrExists)
	got := list(t, s, "tenant-a", true)
	require.Len(t, got, 1)
	assert.Equal(t, "tenant-a/a.json", got[0].Key)
	assert.Equal(t, []byte("one"), got[0].Body)
	assert.WithinDuration(t, time.Now().AddDate(0, 0, 1), got[0].LockedUntil, time.Hour, "the bucket's default retention holds it a day")
}

// A version without retention in compliance mode, as in a bucket whose
// default retention is governance, is listed as not locked.
func TestAVersionWithoutComplianceRetentionIsNotLocked(t *testing.T) {
	s := bucket(t, "governance", true, minio.Governance)
	require.NoError(t, s.Put(t.Context(), "tenant-a/a.json", []byte("one")))

	got := list(t, s, "tenant-a", true)

	require.Len(t, got, 1)
	assert.True(t, got[0].LockedUntil.IsZero())
}

// A listing shows every version and every delete marker: what someone with
// the platform's right to put objects can add never hides an anchor.
func TestTheListingShowsSecondVersionsAndDeleteMarkers(t *testing.T) {
	s := bucket(t, "versions", true, minio.Compliance)
	ctx := t.Context()
	require.NoError(t, s.Put(ctx, "tenant-a/a.json", []byte("one")))
	c := owner(t)
	_, err := c.PutObject(ctx, "versions", "anchors/tenant-a/a.json", bytes.NewReader([]byte("two")), 3, minio.PutObjectOptions{})
	require.NoError(t, err)
	require.NoError(t, c.RemoveObject(ctx, "versions", "anchors/tenant-a/a.json", minio.RemoveObjectOptions{}))
	require.NoError(t, s.Put(ctx, "tenant-b/b.json", []byte("other tenant")))

	got := list(t, s, "tenant-a", true)

	var bodies []string
	var markers int
	for _, o := range got {
		assert.Equal(t, "tenant-a/a.json", o.Key)
		if o.Deleted {
			markers++
			continue
		}
		bodies = append(bodies, string(o.Body))
	}
	assert.ElementsMatch(t, []string{"one", "two"}, bodies)
	assert.Equal(t, 1, markers)
}

// Only a bucket with object lock and a default retention in compliance mode
// keeps anchors write-once.
func TestOnlyComplianceRetentionIsWriteOnce(t *testing.T) {
	for name, want := range map[string]struct {
		lock bool
		mode minio.RetentionMode
		ok   bool
	}{
		"compliance": {true, minio.Compliance, true},
		"governance": {true, minio.Governance, false},
		"no-default": {true, "", false},
		"no-lock":    {false, "", false},
	} {
		s := bucket(t, "lock-"+name, want.lock, want.mode)

		locked, why, err := s.Locked(t.Context())

		require.NoError(t, err, name)
		assert.Equal(t, want.ok, locked, name)
		if !want.ok {
			assert.NotEmpty(t, why, name)
		}
	}
}

// A bucket without object lock is listed and read all the same:
// verification reports the store unlocked, and anchoring goes on.
func TestABucketWithoutObjectLockIsListed(t *testing.T) {
	s := bucket(t, "unlocked", false, "")
	require.NoError(t, s.Put(t.Context(), "tenant-a/a.json", []byte("one")))

	got := list(t, s, "tenant-a", false)

	require.Len(t, got, 1)
	assert.Equal(t, []byte("one"), got[0].Body)
	assert.True(t, got[0].LockedUntil.IsZero())
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/audit/dirstore/ ./internal/audit/s3store/`
Expected: FAIL: both packages fail to build: `no non-test Go files in .../internal/audit/dirstore`, and `internal/audit/s3store` holds no package to build

- [ ] **Step 3: Keep anchors in a directory**

Create `internal/audit/dirstore/dirstore.go`:

```go
// Package dirstore keeps audit anchors as files in a directory, for
// development and tests. A directory is not write-once: whoever can write it
// can change an anchor, so verification reports the store as unlocked. It
// implements the Audit application's Anchors port.
package dirstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// Store keeps anchors under one directory. Names never resolve outside it.
type Store struct {
	root *os.Root
}

// Open returns the store of the anchors under dir, which must exist.
func Open(dir string) (*Store, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("dirstore: open %s: %w", dir, err)
	}
	return &Store{root: root}, nil
}

// Close releases the directory.
func (s *Store) Close() error { return s.root.Close() }

// Put writes body as the file key, which must not exist.
func (s *Store) Put(_ context.Context, key string, body []byte) error {
	if !fs.ValidPath(key) || strings.Count(key, "/") != 1 {
		return fmt.Errorf("dirstore: %q is not an anchor's name", key)
	}
	dir, _, _ := strings.Cut(key, "/")
	if err := s.root.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("dirstore: %w", err)
	}
	f, err := s.root.OpenFile(key, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o440)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: %s", app.ErrExists, key)
	}
	if err != nil {
		return fmt.Errorf("dirstore: %w", err)
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("dirstore: write %s: %w", key, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("dirstore: write %s: %w", key, err)
	}
	return nil
}

// List returns every file under the tenant's directory, without its body.
func (s *Store) List(_ context.Context, tenant string) ([]chain.Stored, error) {
	if !chain.ValidTenant(tenant) {
		return nil, fmt.Errorf("dirstore: %q is not a tenant id", tenant)
	}
	var out []chain.Stored
	err := fs.WalkDir(s.root.FS(), tenant, func(name string, d fs.DirEntry, err error) error {
		switch {
		case errors.Is(err, fs.ErrNotExist) && name == tenant:
			return fs.SkipAll
		case err != nil:
			return err
		case !d.IsDir():
			out = append(out, chain.Stored{Key: name})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dirstore: list %s: %w", tenant, err)
	}
	return out, nil
}

// Read reads a file, at most one byte past the largest anchor, so that a
// larger one never decodes.
func (s *Store) Read(_ context.Context, o *chain.Stored) error {
	f, err := s.root.Open(o.Key)
	if err != nil {
		return fmt.Errorf("dirstore: %w", err)
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, chain.MaxAnchorBytes+1))
	if err != nil {
		return fmt.Errorf("dirstore: read %s: %w", o.Key, err)
	}
	o.Body = body
	return nil
}

// Retention leaves a file without retention: a directory keeps none.
func (*Store) Retention(context.Context, *chain.Stored) error { return nil }

// Locked reports that a directory is not write-once.
func (*Store) Locked(context.Context) (locked bool, why string, err error) {
	return false, "a directory is not write-once: whoever can write it can change an anchor", nil
}
```

- [ ] **Step 4: Keep anchors in an S3 bucket with object lock**

Create `internal/audit/s3store/s3store.go`:

```go
// Package s3store keeps audit anchors in an S3 bucket with object lock, the
// write-once store that ADR-016 assumes for profile question 14. The bucket's
// owner, outside the builders, enables object lock with a default retention
// in compliance mode, longer than any record is kept, and takes the anchors
// itself: its job needs the rights to put objects, to list versions, to read
// them and their retention, and to read the bucket's object lock. The
// platform holds no credential for the bucket. It implements the Audit
// application's Anchors port.
package s3store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// Config names a bucket and the credentials to reach it.
type Config struct {
	// Endpoint is the store's host, with its port when it is not the
	// scheme's. Insecure allows plain HTTP, for a loopback host only.
	Endpoint string
	Insecure bool
	Region   string
	Bucket   string
	// Prefix is put before every anchor's name.
	Prefix string
}

// Store keeps anchors in one bucket, under one prefix.
type Store struct {
	client *minio.Client
	bucket string
	prefix string
}

// New returns the store cfg names, reached with the access key keyID and
// secretKey, which come from the secret store. It refuses plain HTTP to any
// host but a loopback one.
func New(cfg *Config, keyID, secretKey string) (*Store, error) {
	if cfg.Bucket == "" || cfg.Endpoint == "" || keyID == "" || secretKey == "" {
		return nil, errors.New("s3store: an endpoint, a bucket and an access key are required")
	}
	if cfg.Insecure && !loopback(cfg.Endpoint) {
		return nil, fmt.Errorf("s3store: %s is reached over HTTPS; plain HTTP is for a loopback host only", cfg.Endpoint)
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(keyID, secretKey, ""),
		Secure: !cfg.Insecure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3store: %w", err)
	}
	return &Store{client: client, bucket: cfg.Bucket, prefix: cfg.Prefix}, nil
}

// loopback reports whether endpoint names a loopback host.
func loopback(endpoint string) bool {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		host = endpoint
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Put writes body under key on condition that no object holds the key, so
// that the owner's job never makes a second version of an anchor. It sends
// the body's MD5, which a bucket with object lock requires.
func (s *Store) Put(ctx context.Context, key string, body []byte) error {
	opts := minio.PutObjectOptions{ContentType: "application/json", SendContentMd5: true}
	opts.SetMatchETagExcept("*")
	_, err := s.client.PutObject(ctx, s.bucket, s.prefix+key, bytes.NewReader(body), int64(len(body)), opts)
	if minio.ToErrorResponse(err).StatusCode == http.StatusPreconditionFailed {
		return fmt.Errorf("%w: %s", app.ErrExists, key)
	}
	if err != nil {
		return fmt.Errorf("s3store: put %s: %w", key, err)
	}
	return nil
}

// List returns every version of every object under the tenant's name,
// delete markers included, without their bodies: a listing without versions
// shows neither a second version of an anchor nor one a delete marker
// hides.
func (s *Store) List(ctx context.Context, tenant string) ([]chain.Stored, error) {
	if !chain.ValidTenant(tenant) {
		return nil, fmt.Errorf("s3store: %q is not a tenant id", tenant)
	}
	var out []chain.Stored
	for o := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.prefix + tenant + "/", Recursive: true, WithVersions: true}) {
		if o.Err != nil {
			return nil, fmt.Errorf("s3store: list %s: %w", tenant, o.Err)
		}
		out = append(out, chain.Stored{Key: strings.TrimPrefix(o.Key, s.prefix), Version: o.VersionID, Deleted: o.IsDeleteMarker})
	}
	return out, nil
}

// Read reads one version of an object, at most one byte past the largest
// anchor, so that a larger one never decodes.
func (s *Store) Read(ctx context.Context, o *chain.Stored) error {
	obj, err := s.client.GetObject(ctx, s.bucket, s.prefix+o.Key, minio.GetObjectOptions{VersionID: o.Version})
	if err != nil {
		return fmt.Errorf("s3store: read %s: %w", o.Key, err)
	}
	defer func() { _ = obj.Close() }()
	body, err := io.ReadAll(io.LimitReader(obj, chain.MaxAnchorBytes+1))
	if err != nil {
		return fmt.Errorf("s3store: read %s: %w", o.Key, err)
	}
	o.Body = body
	return nil
}

// Retention reads when a version's retention in compliance mode ends, zero
// when it has none.
func (s *Store) Retention(ctx context.Context, o *chain.Stored) error {
	until, err := s.retention(ctx, s.prefix+o.Key, o.Version)
	if err != nil {
		return err
	}
	o.LockedUntil = until
	return nil
}

// retention returns when a version's retention in compliance mode ends,
// zero when it has none.
func (s *Store) retention(ctx context.Context, key, version string) (time.Time, error) {
	mode, until, err := s.client.GetObjectRetention(ctx, s.bucket, key, version)
	if minio.ToErrorResponse(err).Code == "NoSuchObjectLockConfiguration" {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("s3store: read the retention of %s: %w", key, err)
	}
	if mode == nil || *mode != minio.Compliance || until == nil {
		return time.Time{}, nil
	}
	return *until, nil
}

// Locked reports whether the bucket keeps objects write-once: object lock is
// enabled, with a default retention in compliance mode, which nobody can
// shorten or lift.
func (s *Store) Locked(ctx context.Context) (locked bool, why string, err error) {
	enabled, mode, validity, _, err := s.client.GetObjectLockConfig(ctx, s.bucket)
	if minio.ToErrorResponse(err).Code == "ObjectLockConfigurationNotFoundError" {
		return false, "the bucket has no object lock", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("s3store: read the bucket's object lock: %w", err)
	}
	switch {
	case enabled != "Enabled":
		return false, "the bucket's object lock is not enabled", nil
	case mode == nil || *mode != minio.Compliance || validity == nil || *validity == 0:
		return false, "the bucket has no default retention in compliance mode", nil
	}
	return true, "", nil
}
```

Then add the client library, at the version this plan pins:

```bash
go get github.com/minio/minio-go/v7@v7.3.0
go mod tidy
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test ./internal/audit/dirstore/ ./internal/audit/s3store/ && go test -tags integration ./internal/audit/s3store/ && golangci-lint run ./...`
Expected: three `ok` lines; `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/audit/dirstore internal/audit/s3store
git commit -m "feat(audit): anchor stores: an S3 bucket with object lock, and a directory for development"
```

### Task 5: The chain in Postgres

Decisions 1 to 7, 9 and 10 in the database: the records, their guard, the append under the tenant's lock at read committed only, a trigger per audited table, and the snapshot that anchoring and verification read.

**Files:**
- Create: `internal/db/migrations/0005_audit_chain.sql`, `internal/audit/postgres/chain.go`, `internal/audit/postgres/chain_integration_test.go`
- Modify: `internal/db/db.go`, `internal/db/db_integration_test.go`, `internal/testsupport/pgtest/pgtest.go`

**Interfaces:**
- Consumes: Task 3's `app.View`, `app.Row` and `app.Table`; Task 4's `dirstore` in the tests; the audited tables of migrations `0001` to `0004`.
- Produces:
  - `func postgres.NewChain(pool *pgxpool.Pool, tenant, schema string) *postgres.Chain`, implementing `app.Chain`;
  - `func pgtest.URL(t *testing.T) string`;
  - `db.Open`, unchanged in signature, defaulting every connection to read committed.

- [ ] **Step 1: Write the failing tests**

Replace `internal/testsupport/pgtest/pgtest.go` with this version, which adds `URL`:

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
	return PoolWithParams(t, nil)
}

// URL returns the connection string of the package's container, with every
// migration applied, for code that opens its own pool.
func URL(t *testing.T) string {
	t.Helper()
	Pool(t)
	return url
}

// PoolWithParams is Pool with extra run-time parameters on every connection,
// such as a different default_transaction_isolation.
func PoolWithParams(t *testing.T, params map[string]string) *pgxpool.Pool {
	t.Helper()
	require.NotEmpty(t, url, "call pgtest.Main from TestMain first")
	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err)
	for name, value := range params {
		cfg.ConnConfig.RuntimeParams[name] = value
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, db.Migrate(t.Context(), pool))
	return pool
}
```

Replace `internal/db/db_integration_test.go` with this version, which adds `TestOpenReadsCommittedAndEndsIdleTransactions` and `TestChainingWhileWritersRunLeavesNoRowUnchained`. The databases its tests make are dropped when they end:

```go
//go:build integration

package db_test

import (
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
)

func TestMigrateTwiceChangesNothing(t *testing.T) {
	pool := pgtest.Pool(t)

	require.NoError(t, db.Migrate(t.Context(), pool))

	files, err := filepath.Glob("migrations/*.sql")
	require.NoError(t, err)
	var applied int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM schema_migrations").Scan(&applied))
	assert.Len(t, files, applied)
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

// Every connection Open makes runs at read committed whatever the database's
// default, since the audit chain is written only there, and ends a
// transaction left idle for a minute, which would hold the chain's lock.
func TestOpenReadsCommittedAndEndsIdleTransactions(t *testing.T) {
	url := pgtest.URL(t)
	admin := pgtest.Pool(t)
	_, err := admin.Exec(t.Context(), "CREATE DATABASE open_defaults")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS open_defaults WITH (FORCE)")
		assert.NoError(t, err)
	})
	_, err = admin.Exec(t.Context(), "ALTER DATABASE open_defaults SET default_transaction_isolation = 'repeatable read'")
	require.NoError(t, err)
	u, err := neturl.Parse(url)
	require.NoError(t, err)
	u.Path = "/open_defaults"

	pool, err := db.Open(t.Context(), u.String())
	require.NoError(t, err)
	defer pool.Close()

	var level, idle string
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT current_setting('transaction_isolation'), current_setting('idle_in_transaction_session_timeout')").Scan(&level, &idle))
	assert.Equal(t, "read committed", level)
	assert.Equal(t, "1min", idle)
}

// The migration that chains the audited tables, run while writers insert
// into them, leaves no row unchained: it locks them against writes from the
// rows it chains until its triggers are in place.
func TestChainingWhileWritersRunLeavesNoRowUnchained(t *testing.T) {
	admin := pgtest.Pool(t)
	_, err := admin.Exec(t.Context(), "CREATE DATABASE migrate_live")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS migrate_live WITH (FORCE)")
		assert.NoError(t, err)
	})
	u, err := neturl.Parse(pgtest.URL(t))
	require.NoError(t, err)
	u.Path = "/migrate_live"
	pool, err := db.Open(t.Context(), u.String())
	require.NoError(t, err)
	defer pool.Close()
	files, err := filepath.Glob("migrations/*.sql")
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), "CREATE TABLE schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())")
	require.NoError(t, err)
	for _, f := range files[:len(files)-1] {
		sql, err := os.ReadFile(f)
		require.NoError(t, err)
		_, err = pool.Exec(t.Context(), string(sql))
		require.NoError(t, err, f)
		_, err = pool.Exec(t.Context(), "INSERT INTO schema_migrations (name) VALUES ($1)", filepath.Base(f))
		require.NoError(t, err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO tool_calls (tenant_id, call_id, run_id, agent, server, tool, schema_hash, arguments, round, started_at)
SELECT 't', 'before-' || i, 'r', 'a', 's', 't', 'h', '{}', 1, now() FROM generate_series(1, 20000) i`)
	require.NoError(t, err)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_, err := pool.Exec(context.Background(), `INSERT INTO tool_calls (tenant_id, call_id, run_id, agent, server, tool, schema_hash, arguments, round, started_at)
VALUES ('t', $1, 'r', 'a', 's', 't', 'h', '{}', 1, now())`, fmt.Sprintf("during-%d-%d", w, i))
				assert.NoError(t, err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, db.Migrate(t.Context(), pool))
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	var rows, records int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM tool_calls").Scan(&rows))
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_chain WHERE source = 'tool_calls'").Scan(&records))
	assert.Greater(t, rows, 20000)
	assert.Equal(t, rows, records)
}
```

Create `internal/audit/postgres/chain_integration_test.go`. Its tests write through the platform's own stores, and tamper as an operator with the database's superuser would, with every trigger off:

```go
//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	neturl "net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
	"github.com/jchen0426/agent-swarm/internal/audit/dirstore"
	"github.com/jchen0426/agent-swarm/internal/audit/postgres"
	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	workpostgres "github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var investigator = board.Author{Kind: board.AuthorAgent, Name: "flag-investigator"}

// post appends one board entry to run, through the board's own store.
func post(t *testing.T, pool *pgxpool.Pool, tenant string, run board.RunID, id board.EntryID, body *board.Body) {
	t.Helper()
	_, err := workpostgres.NewEventStore(pool, tenant).Append(t.Context(), run, func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{{ID: id, Run: run, Round: 1, Author: investigator, Body: *body}}, nil
	})
	require.NoError(t, err)
}

func task(title string) *board.Body { return &board.Body{Task: &board.Task{Title: title}} }

// call records a call of run and its outcome, through the audit store.
func call(t *testing.T, pool *pgxpool.Pool, tenant, id string, run board.RunID, args, output string) {
	t.Helper()
	calls := postgres.NewCalls(pool, tenant)
	rec := record(id)
	rec.Run, rec.Args = run, json.RawMessage(args)
	require.NoError(t, calls.Begin(t.Context(), rec))
	require.NoError(t, calls.End(t.Context(), &actionsapp.Outcome{ID: id, Status: actionsapp.StatusOK, Output: output, At: at}))
}

// hide records a hidden tool.
func hide(t *testing.T, pool *pgxpool.Pool, tenant, tool string) {
	t.Helper()
	require.NoError(t, postgres.NewHides(pool, tenant).Hide(t.Context(), &actionsapp.Hide{
		Endpoint: "https://flags.internal/mcp", Tool: tool, PinnedHash: "sha256:pin", Registration: "sha256:reg",
		Server: "flags", Reason: actionsapp.ReasonChanged, Observed: "sha256:other", At: at,
	}))
}

// read calls fn with one snapshot of the tenant's chain.
func read(t *testing.T, c *postgres.Chain, fn func(app.View)) {
	t.Helper()
	require.NoError(t, c.Read(t.Context(), func(v app.View) error { fn(v); return nil }))
}

// readChain returns the tenant's records, and checks that each links to the
// one before as verification recomputes it.
func readChain(t *testing.T, c *postgres.Chain) []chain.Link {
	t.Helper()
	var links []chain.Link
	read(t, c, func(v app.View) {
		require.NoError(t, v.Links(t.Context(), func(l *chain.Link) error { links = append(links, *l); return nil }))
	})
	var prev chain.Sum
	for i := range links {
		l := &links[i]
		require.Equal(t, int64(i+1), l.Seq, "the chain has no gap")
		require.Equal(t, prev, l.Prev)
		require.Equal(t, chain.LinkHash(prev, l), l.Hash, "the database's hash is the one verification recomputes")
		prev = l.Hash
	}
	return links
}

// checkRows checks that every record's row renders to its digest, and that
// no row is unchained; it returns how many rows there are.
func checkRows(t *testing.T, c *postgres.Chain) int {
	t.Helper()
	n := 0
	read(t, c, func(v app.View) {
		require.NoError(t, v.Rows(t.Context(), func(r *app.Row) error {
			n++
			require.True(t, r.Known, r.Source)
			require.NotNil(t, r.Rendered, r.Source)
			assert.Equal(t, r.Digest, chain.Digest(string(r.Rendered)), "%s %s", r.Source, r.Key)
			return nil
		}))
		unchained, err := v.Unchained(t.Context())
		require.NoError(t, err)
		assert.Empty(t, unchained)
	})
	return n
}

func newChain(pool *pgxpool.Pool, tenant string) *postgres.Chain {
	return postgres.NewChain(pool, tenant, "public")
}

// Every row of every audited table is chained as it is inserted, in order,
// and verification renders each row to the digest its record holds.
func TestEveryAuditedRowIsChained(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-every"
	post(t, pool, tenant, "run-a", "every-1", task("check the pool"))
	post(t, pool, tenant, "run-a", "every-2", task("check the cache"))
	call(t, pool, tenant, "every-c1", "run-b", `{"flag":"x"}`, `{"on":true}`)
	hide(t, pool, tenant, "get_flag")
	c := newChain(pool, tenant)

	links := readChain(t, c)

	var got []string
	for i := range links {
		got = append(got, links[i].Source+" "+links[i].Run)
	}
	assert.Equal(t, []string{"board_events run-a", "board_events run-a", "tool_calls run-b", "tool_call_outcomes run-b", "tool_hides "}, got)
	assert.Equal(t, `["run-a", 2]`, links[1].Key)
	assert.Equal(t, 5, checkRows(t, c))
	read(t, c, func(v app.View) {
		head, err := v.Head(t.Context())
		require.NoError(t, err)
		assert.Equal(t, links[4].Hash, head.Hash)
		runs, err := v.Runs(t.Context(), 0, 5)
		require.NoError(t, err)
		assert.Equal(t, []string{"run-a", "run-b"}, runs)
		runs, err = v.Runs(t.Context(), 3, 5)
		require.NoError(t, err)
		assert.Equal(t, []string{"run-b"}, runs, "an outcome is its call's run")
		counts, err := v.Counts(t.Context())
		require.NoError(t, err)
		assert.Equal(t, map[string]int64{"board_events": 2, "tool_calls": 1, "tool_call_outcomes": 1, "tool_hides": 1,
			"config_revisions": 0, "config_objects": 0}, counts)
	})
}

// An empty chain has no head.
func TestAnEmptyChainHasNoHead(t *testing.T) {
	c := newChain(pgtest.Pool(t), "chain-empty")

	read(t, c, func(v app.View) {
		head, err := v.Head(t.Context())
		require.NoError(t, err)
		assert.Nil(t, head)
	})
	assert.Empty(t, readChain(t, c))
}

// Writers that race, some of them rolled back, leave one contiguous chain:
// records commit in the order of their seq, and a rollback leaves no gap.
func TestRacingWritersLeaveOneContiguousChain(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-race"
	var wg sync.WaitGroup
	for i := range 15 {
		wg.Go(func() { call(t, pool, tenant, fmt.Sprintf("race-c%d", i), "run-c", `{"flag":"x"}`, "ok") })
		wg.Go(func() {
			post(t, pool, tenant, board.RunID(fmt.Sprintf("run-%d", i%3)), board.EntryID(fmt.Sprintf("race-%d", i)), task("t"))
		})
		wg.Go(func() {
			err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(context.Background(), `INSERT INTO tool_calls (tenant_id, call_id, run_id, agent, server, tool, schema_hash, arguments, round, started_at)
VALUES ($1, $2, 'run-c', 'a', 's', 't', 'h', '{}', 1, now())`, tenant, fmt.Sprintf("race-rolled-back-%d", i))
				if err != nil {
					return err
				}
				return errors.New("roll back")
			})
			assert.EqualError(t, err, "roll back")
		})
	}
	wg.Wait()
	c := newChain(pool, tenant)

	links := readChain(t, c)
	assert.Len(t, links, 45)
	assert.Equal(t, 45, checkRows(t, c))
	for i := 1; i < len(links); i++ {
		assert.False(t, links[i].ChainedAt.Before(links[i-1].ChainedAt), "a record is chained no earlier than the one before")
	}
}

// The chain is written only at read committed: at any other level, a
// transaction's snapshot could predate the record it waited for, so the
// insert fails rather than forking the chain.
func TestAuditedRowsAreWrittenOnlyAtReadCommitted(t *testing.T) {
	const tenant = "chain-levels"
	for _, level := range []string{"repeatable read", "serializable"} {
		pool := pgtest.PoolWithParams(t, map[string]string{"default_transaction_isolation": level})

		err := postgres.NewCalls(pool, tenant).Begin(t.Context(), record("levels-"+level))

		require.ErrorContains(t, err, "read committed", level)
	}
}

// A row's digest is the same whatever the inserting session's settings and
// the reading connection's encoding, and whatever the text holds: quotes,
// backslashes, control characters, characters outside ASCII, and JSON
// written inside a string.
func TestTheDigestIgnoresSessionSettingsAndEncodings(t *testing.T) {
	odd := pgtest.PoolWithParams(t, map[string]string{"TimeZone": "Pacific/Chatham", "DateStyle": "SQL, DMY", "IntervalStyle": "sql_standard"})
	const tenant = "chain-odd"
	text := "q\"uote \\back\nline\ttab \x01ctl é ü {\"a\":[1,2.50]}"
	call(t, odd, tenant, "odd-c1", "run-odd", fmt.Sprintf(`{"text":%q}`, text), text+" 漢字")
	post(t, odd, tenant, "run-odd", "odd-1", &board.Body{Task: &board.Task{Title: text, Tags: []string{"a\"b", "漢"}}})

	for _, params := range []map[string]string{nil, {"client_encoding": "LATIN1"}} {
		c := newChain(pgtest.PoolWithParams(t, params), tenant)

		readChain(t, c)
		assert.Equal(t, 3, checkRows(t, c), params)
	}
}

// tamper runs statements with every trigger off, as an operator with the
// database's superuser may.
func tamper(t *testing.T, pool *pgxpool.Pool, statements ...string) {
	t.Helper()
	require.NoError(t, pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), "SET LOCAL session_replication_role = replica"); err != nil {
			return err
		}
		for _, s := range statements {
			if _, err := tx.Exec(t.Context(), s); err != nil {
				return err
			}
		}
		return nil
	}))
}

// auditor returns the auditor of tenant, with its anchors in a directory,
// and a clock tests move.
func auditor(t *testing.T, pool *pgxpool.Pool, tenant string, now *time.Time) *app.Auditor {
	t.Helper()
	store, err := dirstore.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := app.New(newChain(pool, tenant), store, tenant, func() time.Time { return *now })
	require.NoError(t, err)
	return a
}

// findings returns what a verification found, as kind and table, but for
// the directory store being unlocked.
func findings(t *testing.T, a *app.Auditor) []string {
	t.Helper()
	all, err := a.Verify(t.Context(), time.Hour)
	require.NoError(t, err)
	var out []string
	for i := range all {
		if all[i].Kind != chain.StoreUnlocked {
			out = append(out, string(all[i].Kind)+" "+all[i].Source)
		}
	}
	return out
}

// What an operator does to the database behind the platform's back shows:
// a row changed, a row deleted, a row inserted without its record.
func TestVerificationFindsWhatWasDoneBehindThePlatformsBack(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-tamper"
	post(t, pool, tenant, "run-t", "tamper-1", task("check the pool"))
	call(t, pool, tenant, "tamper-c1", "run-t", `{"flag":"x"}`, `{"on":true}`)
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	require.Empty(t, findings(t, a))

	tamper(t, pool,
		`UPDATE board_events SET body = '{"task":{"title":"check nothing"}}' WHERE tenant_id = 'chain-tamper' AND id = 'tamper-1'`,
		`DELETE FROM tool_call_outcomes WHERE tenant_id = 'chain-tamper' AND call_id = 'tamper-c1'`,
		`INSERT INTO tool_hides (tenant_id, endpoint, tool, pinned_hash, registration, server, reason, observed_hash, hidden_at)
VALUES ('chain-tamper', 'https://flags.internal/mcp', 'set_flag', 'p', 'r', 'flags', 'changed', 'o', now())`)

	assert.ElementsMatch(t, []string{"changed_row board_events", "missing_row tool_call_outcomes", "unchained_row tool_hides"}, findings(t, a))
}

// Records are never deleted, by the retention job either; a chain whose
// first records an operator deleted with the triggers off shows as cut.
func TestTheChainsStartCannotBeDeleted(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-prefix"
	post(t, pool, tenant, "run-p", "prefix-1", task("first"))
	post(t, pool, tenant, "run-p", "prefix-2", task("second"))
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)

	err = pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), "SET LOCAL agentswarm.retention = 'on'"); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), "DELETE FROM audit_chain WHERE tenant_id = 'chain-prefix' AND seq = 1")
		return err
	})
	require.ErrorContains(t, err, "append-only")
	tamper(t, pool,
		"DELETE FROM audit_chain WHERE tenant_id = 'chain-prefix' AND seq = 1",
		"DELETE FROM board_events WHERE tenant_id = 'chain-prefix' AND id = 'prefix-1'")

	assert.Equal(t, []string{"truncated "}, findings(t, a))
}

// Rows written while the chain trigger was off, and deleted since, still
// show: the anchor taken meanwhile counted them, and saw the trigger off.
func TestRowsWrittenUnchainedShowEvenOnceDeleted(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-unchained"
	hide(t, pool, tenant, "get_flag")
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := pool.Exec(t.Context(), "ALTER TABLE tool_hides DISABLE TRIGGER tool_hides_chain")
	require.NoError(t, err)
	enable := func() {
		_, err := pool.Exec(context.Background(), "ALTER TABLE tool_hides ENABLE TRIGGER tool_hides_chain")
		require.NoError(t, err)
	}
	t.Cleanup(enable)
	hide(t, pool, tenant, "set_flag")
	_, err = a.Anchor(t.Context())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"table tool_hides", "table tool_hides", "unchained_row tool_hides", "unchained_rows tool_hides"}, findings(t, a),
		"the table now, and as the anchor saw it")
	enable()

	tamper(t, pool, "DELETE FROM tool_hides WHERE tenant_id = 'chain-unchained' AND tool = 'set_flag'")

	assert.ElementsMatch(t, []string{"unchained_rows tool_hides", "table tool_hides"}, findings(t, a))
}

// A table that row security filters, or that is not a plain table, shows:
// verification could be shown other rows than raters see.
func TestTablesVerificationCannotTrustShow(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-tables"
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), "ALTER TABLE tool_hides ENABLE ROW LEVEL SECURITY")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "ALTER TABLE tool_hides DISABLE ROW LEVEL SECURITY")
		require.NoError(t, err)
	})

	assert.Equal(t, []string{"table tool_hides"}, findings(t, a))
}

// database opens a pool to a database of the package's container, by name.
func database(t *testing.T, name string) *pgxpool.Pool {
	t.Helper()
	u, err := neturl.Parse(pgtest.URL(t))
	require.NoError(t, err)
	u.Path = "/" + name
	pool, err := db.Open(t.Context(), u.String())
	require.NoError(t, err)
	return pool
}

// After a restore from a backup, the anchor taken next names what the chain
// no longer holds: its range, and the runs whose records went.
func TestARestoreFromABackupShowsWhatWasLost(t *testing.T) {
	admin := pgtest.Pool(t)
	ctx := t.Context()
	_, err := admin.Exec(ctx, "CREATE DATABASE restore_live")
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, name := range []string{"restore_live", "restore_backup"} {
			_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			assert.NoError(t, err)
		}
	})
	live := database(t, "restore_live")
	require.NoError(t, db.Migrate(ctx, live))
	store, err := dirstore.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	const tenant = "chain-restore"
	now := time.Now().Add(-3 * time.Hour)
	anchor := func(pool *pgxpool.Pool) {
		t.Helper()
		a, err := app.New(newChain(pool, tenant), store, tenant, func() time.Time { return now })
		require.NoError(t, err)
		_, err = a.Anchor(ctx)
		require.NoError(t, err)
		now = now.Add(time.Hour)
	}
	post(t, live, tenant, "run-early", "restore-1", task("before the backup"))
	anchor(live)
	live.Close()
	_, err = admin.Exec(ctx, "CREATE DATABASE restore_backup TEMPLATE restore_live")
	require.NoError(t, err)
	live = database(t, "restore_live")
	call(t, live, tenant, "restore-c1", "run-late", `{"flag":"x"}`, "ok")
	post(t, live, tenant, "run-later", "restore-2", task("after the backup"))
	anchor(live)
	live.Close()

	for _, sql := range []string{"DROP DATABASE restore_live", "CREATE DATABASE restore_live TEMPLATE restore_backup"} {
		_, err := admin.Exec(ctx, sql)
		require.NoError(t, err)
	}
	live = database(t, "restore_live")
	defer live.Close()
	for i := range 4 {
		post(t, live, tenant, "run-new", board.EntryID(fmt.Sprintf("restore-new-%d", i)), task("after the restore"))
	}
	anchor(live)
	a, err := app.New(newChain(live, tenant), store, tenant, func() time.Time { return now })
	require.NoError(t, err)

	got, err := a.Verify(ctx, time.Hour)

	require.NoError(t, err)
	var kinds []string
	for i := range got {
		if got[i].Kind == chain.StoreUnlocked {
			continue
		}
		kinds = append(kinds, string(got[i].Kind))
		if got[i].Kind == chain.Lost {
			assert.Equal(t, []int64{4, 4}, []int64{got[i].Seq, max(got[i].Through, got[i].Seq)})
			assert.Equal(t, []string{"run-late", "run-later"}, got[i].Runs)
		}
	}
	assert.Equal(t, []string{"lost"}, kinds)
}

// Another trigger or a rule that acts on an audited table's inserts, which
// could change or drop a row before it is chained, shows.
func TestATriggerOrARuleOnInsertsShows(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-altered"
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `
CREATE FUNCTION rewrite_body() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.body := '{}'; RETURN NEW; END; $$;
CREATE TRIGGER rewrite_body BEFORE INSERT ON board_events FOR EACH ROW EXECUTE FUNCTION rewrite_body();
CREATE RULE drop_hides AS ON INSERT TO tool_hides DO INSTEAD NOTHING;`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `
DROP RULE drop_hides ON tool_hides;
DROP TRIGGER rewrite_body ON board_events;
DROP FUNCTION rewrite_body();`)
		require.NoError(t, err)
	})

	assert.ElementsMatch(t, []string{"table board_events", "table tool_hides"}, findings(t, a))
}

// A table of an audited table's name in another schema, which the
// platform's search path could reach first and so take its writes
// unchained, shows.
func TestATableOfTheSameNameInAnotherSchemaShows(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-shadow"
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), "CREATE SCHEMA shadow; CREATE TABLE shadow.tool_hides (LIKE public.tool_hides)")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DROP SCHEMA shadow CASCADE")
		require.NoError(t, err)
	})

	assert.Equal(t, []string{"table tool_hides"}, findings(t, a))
}

// A record of a source no audited table has, as an operator can append by
// hand, shows as a record this build cannot render; it never makes an
// anchor unreadable, so later anchors still count.
func TestARogueRecordLeavesAnchorsReadable(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-rogue"
	post(t, pool, tenant, "run-r", "rogue-1", task("first"))
	_, err := pool.Exec(t.Context(), "SELECT audit_chain_append('chain-rogue', 'X', '[\"x\"]', '', 1::smallint, 'rogue')")
	require.NoError(t, err)
	now := time.Now()
	a := auditor(t, pool, tenant, &now)

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"board_events": 1, "tool_calls": 0, "tool_call_outcomes": 0, "tool_hides": 0, "config_revisions": 0, "config_objects": 0}, an.Records)
	assert.Equal(t, []string{"unknown_form X"}, findings(t, a))
}

// A trigger that takes the chain trigger's name, but fires before the row
// is inserted and rewrites it, shows: the chain trigger is known by its
// kind and its function, never by its name alone.
func TestAChainTriggerImpostorShows(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-impostor"
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := pool.Exec(t.Context(), `
DROP TRIGGER board_events_chain ON board_events;
CREATE FUNCTION impostor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.body := '{"task":{"title":"forged"}}';
    PERFORM audit_chain_append(NEW.tenant_id, 'board_events', audit_key(NEW), NEW.run_id, 1::smallint, audit_form1(NEW));
    RETURN NEW;
END;
$$;
CREATE TRIGGER board_events_chain BEFORE INSERT ON board_events FOR EACH ROW EXECUTE FUNCTION impostor();`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `
DROP TRIGGER board_events_chain ON board_events;
DROP FUNCTION impostor();
CREATE TRIGGER board_events_chain AFTER INSERT ON board_events FOR EACH ROW EXECUTE FUNCTION audit_chain_board_events();`)
		require.NoError(t, err)
	})
	post(t, pool, tenant, "run-i", "impostor-1", task("check the pool"))
	_, err = a.Anchor(t.Context())
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"table board_events", "table board_events"}, findings(t, a), "now, and as the anchor saw it")
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test -tags integration ./internal/audit/postgres/ ./internal/db/`
Expected: FAIL: `internal/audit/postgres` fails to build (`undefined: postgres.Chain`); in `internal/db`, `TestOpenReadsCommittedAndEndsIdleTransactions` fails (`expected: "read committed"`, `actual: "repeatable read"`; `expected: "1min"`, `actual: "0"`) and `TestChainingWhileWritersRunLeavesNoRowUnchained` fails with `relation "audit_chain" does not exist`

- [ ] **Step 3: Chain every audited row**

Create `internal/db/migrations/0005_audit_chain.sql`:

```sql
-- The audit chain (spec Section 9.4). Every row of the audited tables is
-- chained, in the transaction that inserts it, after the row chained before
-- it in its tenant. A record names the row by its source table and key,
-- holds its run, the form of its digest, the digest of the row's columns as
-- the database renders them, and the hash that links it to the record
-- before. Verification renders the rows again and recomputes the hashes
-- outside the database; anchors outside it hold the chain's head.
CREATE TABLE audit_chain (
    tenant_id  text        NOT NULL,
    seq        bigint      NOT NULL CHECK (seq > 0),
    source     text        NOT NULL,
    key        text        NOT NULL,
    run_id     text        NOT NULL,
    form       smallint    NOT NULL CHECK (form > 0),
    digest     bytea       NOT NULL CHECK (octet_length(digest) = 32),
    prev       bytea       NOT NULL CHECK (octet_length(prev) = 32),
    hash       bytea       NOT NULL CHECK (octet_length(hash) = 32),
    chained_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, seq),
    UNIQUE (tenant_id, source, key)
);

-- Records never change and are never deleted, by the retention job either:
-- pruning comes with chained prune records (Plan 10), and until then a chain
-- that does not start at its first record shows a deletion.
CREATE FUNCTION audit_chain_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_chain is append-only';
END;
$$;

CREATE TRIGGER audit_chain_append_only
    BEFORE UPDATE OR DELETE ON audit_chain
    FOR EACH ROW EXECUTE FUNCTION audit_chain_guard();

CREATE TRIGGER audit_chain_no_truncate
    BEFORE TRUNCATE ON audit_chain
    FOR EACH STATEMENT EXECUTE FUNCTION audit_chain_guard();

-- audit_chain_append chains one row. It takes the tenant's chain lock and
-- holds it until the transaction ends, so records commit in the order of
-- their seq, and a rollback takes its records with it and leaves no gap.
-- The chaining time is read once the lock is held, so it never goes back.
-- That holds only at read committed, where the statement after the lock
-- sees the record of the transaction it waited for; at any other level the
-- insert fails. A transaction takes the chain lock after any other lock it
-- holds, the run's lock of the board among them, so the two never deadlock.
-- The hash covers the hash before, the seq, the hashes of the source, the
-- key and the run, the form, the digest and the chaining time in
-- microseconds.
CREATE FUNCTION audit_chain_append(p_tenant text, p_source text, p_key text, p_run text, p_form smallint, p_rendered text)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    v_seq    bigint;
    v_prev   bytea;
    v_digest bytea := sha256(convert_to(p_rendered, 'UTF8'));
    v_at     timestamptz;
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION 'audited rows are inserted at read committed, not %', current_setting('transaction_isolation');
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('audit_chain/' || p_tenant, 0));
    v_at := clock_timestamp();
    SELECT c.seq, c.hash INTO v_seq, v_prev
    FROM audit_chain c WHERE c.tenant_id = p_tenant ORDER BY c.seq DESC LIMIT 1;
    IF NOT FOUND THEN
        v_seq := 0;
        v_prev := decode(repeat('00', 32), 'hex');
    END IF;
    v_seq := v_seq + 1;
    INSERT INTO audit_chain (tenant_id, seq, source, key, run_id, form, digest, prev, hash, chained_at)
    VALUES (p_tenant, v_seq, p_source, p_key, p_run, p_form, v_digest, v_prev,
            sha256(v_prev || int8send(v_seq)
                   || sha256(convert_to(p_source, 'UTF8')) || sha256(convert_to(p_key, 'UTF8'))
                   || sha256(convert_to(p_run, 'UTF8')) || int2send(p_form) || v_digest
                   || int8send((extract(epoch FROM v_at) * 1000000)::bigint)),
            v_at);
END;
$$;

-- Each audited table's key, and its columns in form 1: one JSON array, times
-- as microseconds since the epoch, so that the session's settings never
-- change the text. Verification sends the same expressions itself.
CREATE FUNCTION audit_key(r board_events) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.run_id, r.seq)::text $$;

CREATE FUNCTION audit_form1(r board_events) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.run_id, r.seq, r.id, r.round, r.kind, r.author_kind, r.author_name,
                               (extract(epoch FROM r.committed_at) * 1000000)::bigint, r.body)::text $$;

CREATE FUNCTION audit_key(r tool_calls) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.call_id)::text $$;

CREATE FUNCTION audit_form1(r tool_calls) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.call_id, r.run_id, r.agent, r.server, r.tool, r.schema_hash, r.arguments, r.round,
                               (extract(epoch FROM r.started_at) * 1000000)::bigint, r.cause)::text $$;

CREATE FUNCTION audit_key(r tool_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.call_id)::text $$;

CREATE FUNCTION audit_form1(r tool_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.call_id, r.status, r.output,
                               (extract(epoch FROM r.ended_at) * 1000000)::bigint)::text $$;

CREATE FUNCTION audit_key(r tool_hides) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.endpoint, r.tool, r.pinned_hash, r.registration)::text $$;

CREATE FUNCTION audit_form1(r tool_hides) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.endpoint, r.tool, r.pinned_hash, r.registration, r.server, r.reason,
                               r.observed_hash, (extract(epoch FROM r.hidden_at) * 1000000)::bigint)::text $$;

CREATE FUNCTION audit_key(r config_revisions) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.commit_id)::text $$;

CREATE FUNCTION audit_form1(r config_revisions) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.commit_id, r.digest, (extract(epoch FROM r.loaded_at) * 1000000)::bigint)::text $$;

CREATE FUNCTION audit_key(r config_objects) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.commit_id, r.kind, r.name)::text $$;

CREATE FUNCTION audit_form1(r config_objects) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.commit_id, r.kind, r.name, r.owner, r.file, r.hash, r.version)::text $$;

-- An outcome's run is its call's.
CREATE FUNCTION audit_run(r tool_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT c.run_id FROM tool_calls c WHERE c.tenant_id = r.tenant_id AND c.call_id = r.call_id $$;

CREATE FUNCTION audit_chain_board_events() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'board_events', audit_key(NEW), NEW.run_id, 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_tool_calls() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'tool_calls', audit_key(NEW), NEW.run_id, 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_tool_call_outcomes() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'tool_call_outcomes', audit_key(NEW), audit_run(NEW), 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_tool_hides() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'tool_hides', audit_key(NEW), '', 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_config_revisions() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'config_revisions', audit_key(NEW), '', 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_config_objects() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'config_objects', audit_key(NEW), '', 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

-- Every function resolves names in the system catalog first, then in the
-- schema that holds these tables, whatever the caller's search path.
DO $$
DECLARE
    f regprocedure;
BEGIN
    FOR f IN
        SELECT p.oid::regprocedure FROM pg_proc p
        WHERE p.pronamespace = (SELECT n.oid FROM pg_namespace n WHERE n.nspname = current_schema())
          AND (p.proname LIKE 'audit\_chain%' OR p.proname IN ('audit_key', 'audit_form1', 'audit_run'))
    LOOP
        EXECUTE format('ALTER FUNCTION %s SET search_path = pg_catalog, %I', f, current_schema());
    END LOOP;
END;
$$;

-- Rows that exist now are chained first, in the order they were written.
-- The audited tables are locked against writes until the migration commits,
-- so no row is written between the rows chained here and the triggers.
LOCK TABLE board_events, tool_calls, tool_call_outcomes, tool_hides, config_revisions, config_objects
    IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT * FROM (
            SELECT tenant_id, 'board_events' AS source, audit_key(b) AS key, run_id, audit_form1(b) AS rendered, committed_at AS at FROM board_events b
            UNION ALL
            SELECT tenant_id, 'tool_calls', audit_key(c), run_id, audit_form1(c), started_at FROM tool_calls c
            UNION ALL
            SELECT tenant_id, 'tool_call_outcomes', audit_key(o), audit_run(o), audit_form1(o), ended_at FROM tool_call_outcomes o
            UNION ALL
            SELECT tenant_id, 'tool_hides', audit_key(h), '', audit_form1(h), hidden_at FROM tool_hides h
            UNION ALL
            SELECT tenant_id, 'config_revisions', audit_key(v), '', audit_form1(v), loaded_at FROM config_revisions v
            UNION ALL
            SELECT o.tenant_id, 'config_objects', audit_key(o), '', audit_form1(o), v.loaded_at
            FROM config_objects o JOIN config_revisions v USING (tenant_id, commit_id)
        ) rows
        ORDER BY at, source, key
    LOOP
        PERFORM audit_chain_append(r.tenant_id, r.source, r.key, r.run_id, 1::smallint, r.rendered);
    END LOOP;
END;
$$;

CREATE TRIGGER board_events_chain AFTER INSERT ON board_events
    FOR EACH ROW EXECUTE FUNCTION audit_chain_board_events();

CREATE TRIGGER tool_calls_chain AFTER INSERT ON tool_calls
    FOR EACH ROW EXECUTE FUNCTION audit_chain_tool_calls();

CREATE TRIGGER tool_call_outcomes_chain AFTER INSERT ON tool_call_outcomes
    FOR EACH ROW EXECUTE FUNCTION audit_chain_tool_call_outcomes();

CREATE TRIGGER tool_hides_chain AFTER INSERT ON tool_hides
    FOR EACH ROW EXECUTE FUNCTION audit_chain_tool_hides();

CREATE TRIGGER config_revisions_chain AFTER INSERT ON config_revisions
    FOR EACH ROW EXECUTE FUNCTION audit_chain_config_revisions();

CREATE TRIGGER config_objects_chain AFTER INSERT ON config_objects
    FOR EACH ROW EXECUTE FUNCTION audit_chain_config_objects();

-- Verification joins the chain with every audited table; it needs the
-- chain's statistics from the start.
ANALYZE audit_chain;
```

- [ ] **Step 4: Read the chain from one snapshot**

Create `internal/audit/postgres/chain.go`. The renderings are the migration's, sent by verification itself:

```go
package postgres

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// form is how verification renders a row of one source in one form: its key
// and its columns, as migration 0005 writes them. Verification sends these
// expressions itself, so that it never relies on a function stored in the
// database it checks, and takes the text as UTF-8 bytes, so that the
// connection's encoding never changes what it hashes.
type form struct {
	key, row string
}

// source is an audited table with the forms this build renders.
type source struct {
	table string
	forms map[int16]form
}

// sources are the audited tables, in a fixed order.
var sources = []source{
	{"board_events", map[int16]form{1: {
		key: `jsonb_build_array(t.run_id, t.seq)::text`,
		row: `jsonb_build_array(t.tenant_id, t.run_id, t.seq, t.id, t.round, t.kind, t.author_kind, t.author_name,
			(extract(epoch FROM t.committed_at) * 1000000)::bigint, t.body)::text`,
	}}},
	{"tool_calls", map[int16]form{1: {
		key: `jsonb_build_array(t.call_id)::text`,
		row: `jsonb_build_array(t.tenant_id, t.call_id, t.run_id, t.agent, t.server, t.tool, t.schema_hash, t.arguments, t.round,
			(extract(epoch FROM t.started_at) * 1000000)::bigint, t.cause)::text`,
	}}},
	{"tool_call_outcomes", map[int16]form{1: {
		key: `jsonb_build_array(t.call_id)::text`,
		row: `jsonb_build_array(t.tenant_id, t.call_id, t.status, t.output, (extract(epoch FROM t.ended_at) * 1000000)::bigint)::text`,
	}}},
	{"tool_hides", map[int16]form{1: {
		key: `jsonb_build_array(t.endpoint, t.tool, t.pinned_hash, t.registration)::text`,
		row: `jsonb_build_array(t.tenant_id, t.endpoint, t.tool, t.pinned_hash, t.registration, t.server, t.reason, t.observed_hash,
			(extract(epoch FROM t.hidden_at) * 1000000)::bigint)::text`,
	}}},
	{"config_revisions", map[int16]form{1: {
		key: `jsonb_build_array(t.commit_id)::text`,
		row: `jsonb_build_array(t.tenant_id, t.commit_id, t.digest, (extract(epoch FROM t.loaded_at) * 1000000)::bigint)::text`,
	}}},
	{"config_objects", map[int16]form{1: {
		key: `jsonb_build_array(t.commit_id, t.kind, t.name)::text`,
		row: `jsonb_build_array(t.tenant_id, t.commit_id, t.kind, t.name, t.owner, t.file, t.hash, t.version)::text`,
	}}},
}

// Chain reads one tenant's audit chain and the rows it covers, in the
// schema that holds the platform's tables. It implements the Audit
// application's Chain port.
type Chain struct {
	pool   *pgxpool.Pool
	tenant string
	schema string
}

// NewChain returns the reader of one tenant's chain in schema.
func NewChain(pool *pgxpool.Pool, tenant, schema string) *Chain {
	return &Chain{pool: pool, tenant: tenant, schema: schema}
}

// Read calls fn with a view of one snapshot: a read-only transaction at
// repeatable read, whose search path holds only the system catalog, so
// that every name it reads is the one it means, and whose text arrives as
// UTF-8 whatever the connection asked for. Its joins hash rather than loop,
// so a whole chain is read in time linear in its size, also before the
// tables have statistics.
func (c *Chain) Read(ctx context.Context, fn func(app.View) error) error {
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin a snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, set := range []string{"SET LOCAL search_path = pg_catalog", "SET LOCAL client_encoding = 'UTF8'", "SET LOCAL enable_nestloop = off"} {
		if _, err := tx.Exec(ctx, set); err != nil {
			return fmt.Errorf("set up the snapshot: %w", err)
		}
	}
	return fn(&view{tx: tx, tenant: c.tenant, schema: c.schema})
}

type view struct {
	tx     pgx.Tx
	tenant string
	schema string
}

// table returns a table's name, qualified with the schema and quoted.
func (v *view) table(name string) string { return pgx.Identifier{v.schema, name}.Sanitize() }

func (v *view) selectLinks() string {
	return "SELECT seq, source, key, run_id, form, digest, prev, hash, chained_at FROM " + v.table("audit_chain") + " WHERE tenant_id = $1"
}

func (v *view) Head(ctx context.Context) (*chain.Link, error) {
	rows, err := v.tx.Query(ctx, v.selectLinks()+" ORDER BY seq DESC LIMIT 1", v.tenant)
	if err != nil {
		return nil, fmt.Errorf("read the chain's head: %w", err)
	}
	links, err := pgx.CollectRows(rows, scanLink)
	if err != nil {
		return nil, fmt.Errorf("read the chain's head: %w", err)
	}
	if len(links) == 0 {
		return nil, nil
	}
	return &links[0], nil
}

func (v *view) HashAt(ctx context.Context, seq int64) (chain.Sum, bool, error) {
	var hash []byte
	err := v.tx.QueryRow(ctx, "SELECT hash FROM "+v.table("audit_chain")+" WHERE tenant_id = $1 AND seq = $2", v.tenant, seq).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return chain.Sum{}, false, nil
	}
	if err != nil {
		return chain.Sum{}, false, fmt.Errorf("read the record at seq %d: %w", seq, err)
	}
	var s chain.Sum
	if len(hash) != len(s) {
		return chain.Sum{}, false, fmt.Errorf("the record at seq %d holds a hash of the wrong length", seq)
	}
	copy(s[:], hash)
	return s, true, nil
}

func (v *view) Runs(ctx context.Context, after, upTo int64) ([]string, error) {
	rows, err := v.tx.Query(ctx, "SELECT DISTINCT run_id FROM "+v.table("audit_chain")+
		" WHERE tenant_id = $1 AND seq > $2 AND seq <= $3 AND run_id <> ''", v.tenant, after, upTo)
	if err != nil {
		return nil, fmt.Errorf("read the chain's runs: %w", err)
	}
	runs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the chain's runs: %w", err)
	}
	slices.Sort(runs)
	return runs, nil
}

func (v *view) Counts(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, s := range sources {
		var n int64
		if err := v.tx.QueryRow(ctx, "SELECT count(*) FROM "+v.table(s.table)+" WHERE tenant_id = $1", v.tenant).Scan(&n); err != nil {
			return nil, fmt.Errorf("count the rows of %s: %w", s.table, err)
		}
		out[s.table] = n
	}
	return out, nil
}

// Records counts the chain's records of each audited table, and of no
// other source: a record of a source this build does not know is
// verification's to report, and must not make an anchor unreadable.
func (v *view) Records(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		out[s.table] = 0
		names = append(names, s.table)
	}
	rows, err := v.tx.Query(ctx, "SELECT source, count(*) FROM "+v.table("audit_chain")+
		" WHERE tenant_id = $1 AND source = ANY($2) GROUP BY source", v.tenant, names)
	if err != nil {
		return nil, fmt.Errorf("count the chain's records: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source string
		var n int64
		if err := rows.Scan(&source, &n); err != nil {
			return nil, fmt.Errorf("count the chain's records: %w", err)
		}
		out[source] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count the chain's records: %w", err)
	}
	return out, nil
}

func (v *view) Links(ctx context.Context, fn func(*chain.Link) error) error {
	rows, err := v.tx.Query(ctx, v.selectLinks()+" ORDER BY seq", v.tenant)
	if err != nil {
		return fmt.Errorf("read the chain: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return fmt.Errorf("read the chain: %w", err)
		}
		if err := fn(&l); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the chain: %w", err)
	}
	return nil
}

func scanLink(row pgx.CollectableRow) (chain.Link, error) {
	var l chain.Link
	var digest, prev, hash []byte
	var at time.Time
	if err := row.Scan(&l.Seq, &l.Source, &l.Key, &l.Run, &l.Form, &digest, &prev, &hash, &at); err != nil {
		return chain.Link{}, err
	}
	if len(digest) != len(l.Digest) || len(prev) != len(l.Prev) || len(hash) != len(l.Hash) {
		return chain.Link{}, fmt.Errorf("record %d holds a hash of the wrong length", l.Seq)
	}
	copy(l.Digest[:], digest)
	copy(l.Prev[:], prev)
	copy(l.Hash[:], hash)
	l.ChainedAt = at.UTC()
	return l, nil
}

// Rows renders each record's row as its form says, source by source; a
// record of a form or source this build does not know comes last.
func (v *view) Rows(ctx context.Context, fn func(*app.Row) error) error {
	var known strings.Builder
	for _, s := range sources {
		for _, n := range slices.Sorted(maps.Keys(s.forms)) {
			f := s.forms[n]
			fmt.Fprintf(&known, " AND NOT (c.source = '%s' AND c.form = %d)", s.table, n)
			query := fmt.Sprintf(`
SELECT c.seq, c.source, c.key, c.form, c.digest, t.rendered
FROM %s c
LEFT JOIN (SELECT %s AS key, convert_to(%s, 'UTF8') AS rendered FROM %s t WHERE t.tenant_id = $1) t ON t.key = c.key
WHERE c.tenant_id = $1 AND c.source = '%s' AND c.form = %d
ORDER BY c.seq`, v.table("audit_chain"), f.key, f.row, v.table(s.table), s.table, n)
			if err := v.rows(ctx, query, true, fn); err != nil {
				return err
			}
		}
	}
	return v.rows(ctx, "SELECT c.seq, c.source, c.key, c.form, c.digest, NULL::bytea FROM "+v.table("audit_chain")+
		" c WHERE c.tenant_id = $1"+known.String()+" ORDER BY c.seq", false, fn)
}

func (v *view) rows(ctx context.Context, query string, known bool, fn func(*app.Row) error) error {
	rows, err := v.tx.Query(ctx, query, v.tenant)
	if err != nil {
		return fmt.Errorf("read the chained rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r := app.Row{Known: known}
		var digest []byte
		if err := rows.Scan(&r.Seq, &r.Source, &r.Key, &r.Form, &digest, &r.Rendered); err != nil {
			return fmt.Errorf("read the chained rows: %w", err)
		}
		copy(r.Digest[:], digest)
		if err := fn(&r); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the chained rows: %w", err)
	}
	return nil
}

func (v *view) Unchained(ctx context.Context) ([]app.Row, error) {
	var out []app.Row
	for _, s := range sources {
		key := s.forms[1].key
		query := fmt.Sprintf(`
SELECT %s FROM %s t
WHERE t.tenant_id = $1 AND NOT EXISTS (
    SELECT 1 FROM %s c WHERE c.tenant_id = $1 AND c.source = '%s' AND c.key = %s)`, key, v.table(s.table), v.table("audit_chain"), s.table, key)
		rows, err := v.tx.Query(ctx, query, v.tenant)
		if err != nil {
			return nil, fmt.Errorf("read the unchained rows of %s: %w", s.table, err)
		}
		keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, fmt.Errorf("read the unchained rows of %s: %w", s.table, err)
		}
		for _, k := range keys {
			out = append(out, app.Row{Source: s.table, Key: k})
		}
	}
	return out, nil
}

// Tables reads, from the system catalog, how each audited table and the
// chain itself are held: a plain table, without row security, with no
// trigger but its chain trigger and no rule acting on its inserts, and, for
// an audited table, with its chain trigger enabled. The chain trigger is
// known by its name, its kind, after each inserted row, and its function,
// never by its name alone. A relation of the same name in another schema,
// which the platform's search path could reach before its own, counts
// against its table as a filter.
// chainTrigger is true of the trigger g that migration 0005 made for the
// table c: named <table>_chain, firing after each inserted row (tgtype 5:
// row and insert), and calling audit_chain_<table>() in the schema $1.
const chainTrigger = `g.tgname = c.relname || '_chain' AND g.tgtype = 5
             AND g.tgfoid = to_regprocedure(format('%I.%I()', $1::text, 'audit_chain_' || c.relname))`

func (v *view) Tables(ctx context.Context) ([]app.Table, error) {
	names := []string{"audit_chain"}
	for _, s := range sources {
		names = append(names, s.table)
	}
	rows, err := v.tx.Query(ctx, `
SELECT c.relname, c.relkind = 'r',
       c.relrowsecurity OR c.relforcerowsecurity OR EXISTS (
           SELECT 1 FROM pg_class o JOIN pg_namespace m ON m.oid = o.relnamespace
           WHERE o.relname = c.relname AND m.nspname <> $1 AND m.nspname NOT IN ('pg_catalog', 'information_schema')),
       c.relname = 'audit_chain' OR EXISTS (
           SELECT 1 FROM pg_trigger g
           WHERE g.tgrelid = c.oid AND g.tgenabled IN ('O', 'A') AND `+chainTrigger+`),
       EXISTS (
           SELECT 1 FROM pg_trigger g
           WHERE g.tgrelid = c.oid AND NOT g.tgisinternal AND g.tgenabled <> 'D'
             AND (g.tgtype::int & 4) <> 0 AND NOT (`+chainTrigger+`))
       OR EXISTS (SELECT 1 FROM pg_rewrite w WHERE w.ev_class = c.oid)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = ANY($2)`, v.schema, names)
	if err != nil {
		return nil, fmt.Errorf("read the audited tables: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (app.Table, error) {
		var t app.Table
		err := row.Scan(&t.Name, &t.Plain, &t.Filtered, &t.Chains, &t.Altered)
		return t, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the audited tables: %w", err)
	}
	out := make([]app.Table, 0, len(names))
	for _, name := range names {
		i := slices.IndexFunc(found, func(t app.Table) bool { return t.Name == name })
		if i < 0 {
			out = append(out, app.Table{Name: name})
			continue
		}
		out = append(out, found[i])
	}
	return out, nil
}
```

- [ ] **Step 5: Open every connection at read committed**

Replace `internal/db/db.go` with this version:

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

// Open connects to Postgres and checks the connection. An empty url takes
// the libpq environment variables, such as PGHOST and PGUSER. Every
// connection runs at read committed whatever the database's default, since
// the audit chain is written only there, and ends a transaction left idle
// for a minute, which would otherwise hold the chain's lock.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_isolation"] = "read committed"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
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

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test -tags integration ./internal/audit/postgres/ ./internal/db/ && golangci-lint run ./...`
Expected: both packages `ok`; `0 issues.`

- [ ] **Step 7: Run every integration test**

The trigger now fires for every audited insert, in every package.

Run: `make test-integration`
Expected: every package `ok`.

- [ ] **Step 8: Commit**

```bash
git add internal/db/migrations/0005_audit_chain.sql internal/db/db.go internal/db/db_integration_test.go internal/testsupport/pgtest/pgtest.go internal/audit/postgres/chain.go internal/audit/postgres/chain_integration_test.go
git commit -m "feat(audit): chain every audited row as it is inserted, and read the chain from one snapshot"
```

### Task 6: The audit commands

Decision 12. `agentswarm audit anchor` and `agentswarm audit verify`, which the anchor store's owner runs.

**Files:**
- Create: `cmd/agentswarm/audit.go`, `cmd/agentswarm/audit_test.go`, `cmd/agentswarm/audit_integration_test.go`
- Modify: `cmd/agentswarm/run.go`

**Interfaces:**
- Consumes: Task 3's `app.New`, `Auditor.Anchor` and `Auditor.Verify`; Task 4's `dirstore` and `s3store`; Task 5's `postgres.NewChain` and `db.Open`; `internal/secrets/files` (Plan 4).
- Produces: the commands, with the flags `--database-url`, `--schema`, `--tenant`, `--anchors`, `--secrets` and, for `verify`, `--every`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/agentswarm/audit_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The anchor store is named by a URL: s3:// with its endpoint and two secret
// references, or file:// with a directory. Anything else is refused before
// any connection is made.
func TestTheAnchorStoreIsNamedByAURL(t *testing.T) {
	secrets := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(secrets, "audit"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(secrets, "audit", "key"), []byte("id\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(secrets, "audit", "secret"), []byte("s3cret\n"), 0o600))
	ctx := t.Context()

	for _, good := range []string{
		"s3://anchors/agentswarm?endpoint=anchors.example&key=secret://audit/key&secret=secret://audit/secret",
		"s3://anchors?endpoint=127.0.0.1:9000&insecure=true&region=eu-1&key=secret://audit/key&secret=secret://audit/secret",
		"file://" + t.TempDir(),
	} {
		s, done, err := openAnchors(ctx, good, secrets)
		require.NoError(t, err, good)
		assert.NotNil(t, s)
		done()
	}
	for bad, why := range map[string]string{
		"s3://anchors?endpoint=anchors.example:9000&insecure=true&key=secret://audit/key&secret=secret://audit/secret": "loopback",
		"s3://anchors?endpoint=anchors.example&key=secret://audit/key&secret=secret://audit/missing":                   "no such secret",
		"s3://anchors?endpoint=anchors.example&key=audit/key&secret=secret://audit/secret":                             "not a secret reference",
		"s3://anchors?endpoint=anchors.example&key=secret://audit/key&secret=secret://audit/secret&acl=public":         `no "acl"`,
		"file://host/dir": "file:///DIR",
		"http://anchors":  "neither s3:// nor file://",
	} {
		_, _, err := openAnchors(ctx, bad, secrets)
		require.ErrorContains(t, err, why, bad)
	}
	_, _, err := openAnchors(ctx, "s3://anchors?endpoint=anchors.example&key=secret://audit/key&secret=secret://audit/secret", "")
	require.ErrorContains(t, err, "--secrets")
}

// Both audit commands need a tenant and an anchor store.
func TestTheAuditCommandsNeedATenantAndAStore(t *testing.T) {
	for _, cmd := range []string{"anchor", "verify"} {
		_, errOut, code := runCommand("audit", cmd, "--anchors", "file:///tmp")
		assert.Equal(t, exitUsage, code, cmd)
		assert.Contains(t, errOut, "tenant", cmd)
	}
	_, errOut, code := runCommand("audit", "anchor", "--tenant", "Bad/Tenant", "--anchors", "file://"+t.TempDir(), "--database-url", "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, `--tenant: "Bad/Tenant" is not a tenant id`, "refused before any connection")
}
```

Create `cmd/agentswarm/audit_integration_test.go`. It is the package's first integration test, so it starts the package's Postgres container:

```go
//go:build integration

package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

// verifyReport is what audit verify prints.
type verifyReport struct {
	Findings []struct {
		Kind   string `json:"kind"`
		Source string `json:"source"`
	} `json:"findings"`
}

func verifyKinds(t *testing.T, args ...string) []string {
	t.Helper()
	stdout, errOut, code := runCommand(append([]string{"audit", "verify"}, args...)...)
	require.Equal(t, exitFindings, code, "a directory store is never write-once: %s", errOut)
	var r verifyReport
	require.NoError(t, json.Unmarshal([]byte(stdout), &r), stdout)
	var kinds []string
	for _, f := range r.Findings {
		kinds = append(kinds, f.Kind+" "+f.Source)
	}
	return kinds
}

// The commands anchor the chain and verify it against the database and the
// store; a row changed behind the platform's back shows.
func TestAuditAnchorThenVerify(t *testing.T) {
	url := pgtest.URL(t)
	pool := pgtest.Pool(t)
	const tenant = "cli-audit"
	_, err := postgres.NewEventStore(pool, tenant).Append(t.Context(), "run-cli", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{{ID: "cli-1", Run: "run-cli", Round: 1, Author: board.Author{Kind: board.AuthorAgent, Name: "a"},
			Body: board.Body{Task: &board.Task{Title: "check the pool"}}}}, nil
	})
	require.NoError(t, err)
	args := []string{"--tenant", tenant, "--anchors", "file://" + t.TempDir(), "--database-url", url}

	stdout, errOut, code := runCommand(append([]string{"audit", "anchor"}, args...)...)

	require.Equal(t, exitOK, code, errOut)
	var anchored struct {
		Key  string `json:"key"`
		Seq  int64  `json:"seq"`
		Runs int    `json:"runs"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &anchored))
	assert.Equal(t, int64(1), anchored.Seq)
	assert.Equal(t, 1, anchored.Runs)
	assert.Equal(t, []string{"store_unlocked "}, verifyKinds(t, args...))

	_, err = pool.Exec(t.Context(), `
BEGIN;
SET LOCAL session_replication_role = replica;
UPDATE board_events SET author_name = 'someone else' WHERE tenant_id = 'cli-audit' AND id = 'cli-1';
COMMIT;`)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"store_unlocked ", "changed_row board_events"}, verifyKinds(t, args...))
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./cmd/agentswarm/`
Expected: FAIL: `undefined: openAnchors` in `audit_test.go`

- [ ] **Step 3: Add the commands**

Create `cmd/agentswarm/audit.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
	"github.com/jchen0426/agent-swarm/internal/audit/dirstore"
	"github.com/jchen0426/agent-swarm/internal/audit/postgres"
	"github.com/jchen0426/agent-swarm/internal/audit/s3store"
	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/secrets/files"
)

// auditFlags are the flags both audit commands take.
type auditFlags struct {
	database, schema, tenant, anchors, secrets string
	every                                      time.Duration
}

func (f *auditFlags) add(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.database, "database-url", "", "Postgres URL of a role that can only read; when empty, the libpq environment variables (PGHOST, PGUSER, ...)")
	fl.StringVar(&f.schema, "schema", "public", "the schema that holds the platform's tables")
	fl.StringVar(&f.tenant, "tenant", "", "the tenant whose chain to anchor or verify")
	fl.StringVar(&f.anchors, "anchors", "", "the anchor store: s3://BUCKET/PREFIX?endpoint=HOST&key=REF&secret=REF, or file:///DIR")
	fl.StringVar(&f.secrets, "secrets", "", "directory the secret references of an s3:// store resolve in")
	_ = cmd.MarkFlagRequired("tenant")
	_ = cmd.MarkFlagRequired("anchors")
}

func newAuditCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Anchor the audit chain outside the database, and verify it",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errNoCommand
		},
	}
	cmd.AddCommand(newAnchorCommand(), newVerifyCommand())
	return cmd
}

func newAnchorCommand() *cobra.Command {
	var f auditFlags
	cmd := &cobra.Command{
		Use:   "anchor --tenant ID --anchors URL",
		Short: "Write the audit chain's head to the anchor store",
		Long: `Write one anchor to the anchor store (spec Section 9.4): the head of the
tenant's audit chain, the runs whose records the chain gained since the anchor
before, and the rows each audited table holds, all read from one snapshot.

The store's owner runs it, outside the builders, every hour, also when nothing
happened, so that verification can tell a quiet hour from anchoring that
stopped. It needs the owner's credential for the store and a database role that
can only read; the platform holds neither.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var an chain.Anchor
			err := withAuditor(c.Context(), &f, func(a *app.Auditor) error {
				var err error
				an, err = a.Anchor(c.Context())
				return err
			})
			if err != nil {
				return failure{err}
			}
			return writeJSON(c.OutOrStdout(), struct {
				Key  string `json:"key"`
				Seq  int64  `json:"seq"`
				Runs int    `json:"runs"`
			}{an.Key(), an.Seq, len(an.Runs)})
		},
	}
	f.add(cmd)
	return cmd
}

func newVerifyCommand() *cobra.Command {
	var f auditFlags
	cmd := &cobra.Command{
		Use:   "verify --tenant ID --anchors URL",
		Short: "Check the audit chain, its rows and its anchors",
		Long: `Read the tenant's whole audit chain, every row it covers and every anchor,
every version of each, and report what disagrees (spec Sections 8.4 and 9.4):
records that do not link, rows changed, deleted or never chained, anchors the
chain no longer matches or has lost records past, as after a restore, anchors
that conflict or are hidden, anchoring that stopped, and a store that is not
write-once. The findings are printed as JSON. Any finding exits with 3, and a
verification that could not run with 1.

A record is anchored only once an anchor follows it, and a missed hour stays a
finding, so the owner's scheduler runs audit anchor every hour and retries
within ten minutes.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var findings []chain.Finding
			err := withAuditor(c.Context(), &f, func(a *app.Auditor) error {
				var err error
				findings, err = a.Verify(c.Context(), f.every)
				return err
			})
			if err != nil {
				return failure{err}
			}
			if err := writeJSON(c.OutOrStdout(), report(findings)); err != nil {
				return err
			}
			if len(findings) > 0 {
				return found{fmt.Errorf("%d findings", len(findings))}
			}
			return nil
		},
	}
	f.add(cmd)
	cmd.Flags().DurationVar(&f.every, "every", time.Hour, "the anchor interval")
	return cmd
}

// finding is a finding as verify prints it.
type finding struct {
	Kind    chain.Kind `json:"kind"`
	Seq     int64      `json:"seq,omitempty"`
	Through int64      `json:"through,omitempty"`
	Source  string     `json:"source,omitempty"`
	Key     string     `json:"key,omitempty"`
	Anchor  string     `json:"anchor,omitempty"`
	Runs    []string   `json:"runs,omitempty"`
	Detail  string     `json:"detail"`
}

func report(found []chain.Finding) any {
	out := make([]finding, 0, len(found))
	for i := range found {
		f := &found[i]
		out = append(out, finding{f.Kind, f.Seq, f.Through, f.Source, f.Key, f.Anchor, f.Runs, f.Detail})
	}
	return struct {
		Findings []finding `json:"findings"`
	}{out}
}

// withAuditor opens the anchor store and the database, and calls fn with
// the tenant's auditor. A tenant id that cannot name anchors is refused
// first.
func withAuditor(ctx context.Context, f *auditFlags, fn func(*app.Auditor) error) error {
	if !chain.ValidTenant(f.tenant) {
		return fmt.Errorf("--tenant: %q is not a tenant id: lowercase letters, digits and dashes", f.tenant)
	}
	store, closeStore, err := openAnchors(ctx, f.anchors, f.secrets)
	if err != nil {
		return err
	}
	defer closeStore()
	pool, err := db.Open(ctx, f.database)
	if err != nil {
		return err
	}
	defer pool.Close()
	a, err := app.New(postgres.NewChain(pool, f.tenant, f.schema), store, f.tenant, time.Now)
	if err != nil {
		return err
	}
	return fn(a)
}

// openAnchors opens the anchor store a URL names.
func openAnchors(ctx context.Context, raw, secretsDir string) (app.Anchors, func(), error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("--anchors: %w", err)
	}
	switch u.Scheme {
	case "file":
		if u.Host != "" || u.RawQuery != "" || u.Path == "" {
			return nil, nil, errors.New("--anchors: a file store is file:///DIR")
		}
		s, err := dirstore.Open(u.Path)
		if err != nil {
			return nil, nil, err
		}
		return s, func() { _ = s.Close() }, nil
	case "s3":
		s, err := openS3(ctx, u, secretsDir)
		if err != nil {
			return nil, nil, err
		}
		return s, func() {}, nil
	}
	return nil, nil, fmt.Errorf("--anchors: %q is neither s3:// nor file://", u.Scheme)
}

// openS3 opens an s3:// store: the bucket is the host, the prefix the path;
// endpoint, region and insecure are query parameters, and key and secret are
// secret references that resolve in secretsDir.
func openS3(ctx context.Context, u *url.URL, secretsDir string) (*s3store.Store, error) {
	q := u.Query()
	for name := range q {
		switch name {
		case "endpoint", "region", "insecure", "key", "secret":
		default:
			return nil, fmt.Errorf("--anchors: an s3:// store takes no %q", name)
		}
	}
	insecure := false
	if v := q.Get("insecure"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("--anchors: insecure: %w", err)
		}
		insecure = b
	}
	if secretsDir == "" {
		return nil, errors.New("--secrets: an s3:// store's key resolves in a secrets directory")
	}
	secrets, err := files.Open(secretsDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = secrets.Close() }()
	keyID, err := secrets.Secret(ctx, q.Get("key"))
	if err != nil {
		return nil, fmt.Errorf("--anchors: key: %w", err)
	}
	secretKey, err := secrets.Secret(ctx, q.Get("secret"))
	if err != nil {
		return nil, fmt.Errorf("--anchors: secret: %w", err)
	}
	prefix := strings.Trim(u.Path, "/")
	if prefix != "" {
		prefix += "/"
	}
	return s3store.New(&s3store.Config{Endpoint: q.Get("endpoint"), Insecure: insecure, Region: q.Get("region"), Bucket: u.Host, Prefix: prefix}, keyID, secretKey)
}
```

Replace `cmd/agentswarm/run.go` with this version, whose root command adds `newAuditCommand()`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/internal/versioninfo"
)

// Exit codes.
const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitFindings = 3
)

// errNoCommand reports that agentswarm ran without a command.
var errNoCommand = errors.New("a command is required")

// failure marks an error that happened while a command ran. Any other error
// comes from cobra before the command runs, such as an unknown command or a
// bad flag, and is a usage error.
type failure struct{ err error }

func (f failure) Error() string { return f.err.Error() }

func (f failure) Unwrap() error { return f.err }

// found marks a command that ran to its end and found what it checks for
// wrong, as a verification with findings. It exits apart from a failure, so
// that a check that could not run never looks like one that ran.
type found struct{ err error }

func (f found) Error() string { return f.err.Error() }

// run executes one command and returns the process exit code. An interrupt
// cancels the command's context.
func run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	root := newRootCommand()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	cmd, err := root.ExecuteContextC(ctx)
	var f failure
	var fd found
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &fd):
		_, _ = fmt.Fprintf(stderr, "agentswarm: %v\n", err)
		return exitFindings
	case errors.As(err, &f):
		_, _ = fmt.Fprintf(stderr, "agentswarm: %v\n", err)
		return exitError
	default:
		_, _ = fmt.Fprintf(stderr, "agentswarm: %v\n\n%s", err, cmd.UsageString())
		return exitUsage
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "agentswarm",
		Short:         "Agent Swarm, a governance platform for AI agents",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(*cobra.Command, []string) error {
			return errNoCommand
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(newVersionCommand(), newConcludeCommand(), newCheckCommand(), newRegistryCommand(), newAuditCommand())
	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the build version as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return writeJSON(cmd.OutOrStdout(), versioninfo.Get())
		},
	}
}

// writeJSON writes v as indented JSON. A failed write is a failure of the command.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return failure{fmt.Errorf("write output: %w", err)}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./cmd/agentswarm/ && go test -tags integration ./cmd/agentswarm/ && golangci-lint run ./...`
Expected: both `ok`; `0 issues.`

- [ ] **Step 5: Run everything**

Run: `make lint test-all`
Expected: `0 issues.`, every package `ok`, the e2e tests `ok`.

- [ ] **Step 6: Commit**

```bash
git add cmd/agentswarm/audit.go cmd/agentswarm/audit_test.go cmd/agentswarm/audit_integration_test.go cmd/agentswarm/run.go
git commit -m "feat(cli): agentswarm audit anchor and verify, which the anchor store's owner runs"
```

### Task 7: Documentation

Decisions 5, 7, 8, 9 and 13 amend the spec; the roadmap splits Plan 6; ADR-016's assumption for question 14 names who takes the anchors.

**Files:**
- Modify: `plan/2026-10-11-agent-swarm-platform-spec.md`, `plan/adr/ADR-016-delivery-build-before-team.md`, `plan/2026-10-12-implementation-roadmap.md`, `README.md`, and this plan

- [ ] **Step 1: Amend the spec**

In `plan/2026-10-11-agent-swarm-platform-spec.md`:

1. In the status row of the header table, after "the MCP gateway's read path (Sections 3.3, 6.1, 6.2, 9.4 and 15; Plan 5a).", add " Amended on 2026-10-15: the MCP gateway's board tools and record-only proposals (Sections 3.3, 4.2, 4.3, 4.4, 5.2, 5.3, 6.1, 8.1, 9.4 and 15; Plan 5b); the audit chain and its anchor (Sections 2, 3.3, 8.4 and 9.4; Plan 6a)." Plan 5b amended those sections but did not say so here.
2. In Section 2's table, replace the row of the audit and trajectory store with:

   ```markdown
   | Audit and trajectory store | An append-only, hash-chained record of tool calls with redacted outputs, board entries, rulings, configuration loads, model-call metadata, and redacted prompts and completions. Its chain head is anchored outside the database by the anchor store's owner (Section 9.4). |
   ```

3. In Section 3.3, at the end of the paragraph that begins "**Echoes.**", add: " Where an agent's read repeats an argument value that redaction changed, the gateway shows it in the output as the stored arguments do: raters see the same value in both, and the agent cannot quote it as it wrote it."
4. In Section 8.4, replace "the anchored audit chain (Section 9.4) shows what was lost." with "the anchored audit chain (Section 9.4) shows what was lost, and names the runs that lost records."
5. In Section 9.4, replace the bullet that begins "**Audit integrity.**" with:

   ```markdown
   - **Audit integrity.** Audit records are hash-chained: each row of an audited table is chained, in the transaction that inserts it, after the row chained before it, and in Phase 0 no record is ever deleted. Every [1 h] the chain head is anchored in a store outside the database that the platform's operators cannot write, owned by someone other than the builders. The owner takes the anchors itself, with its own credential for the store and a database role that can only read; the platform holds no credential for the store. An anchor holds the chain's head, the latest earlier anchor the chain still holds, the runs whose records the chain gained since then, the rows each audited table held, and the tables whose state could not be trusted then. The owner verifies the chain against the rows and the anchors, so a change, a deletion or a restore shows, and a restore names the runs that lost records. Phase 1 adds a full external audit sink (Phase 1 backlog).
   ```

- [ ] **Step 2: Name who takes the anchors in ADR-016**

In `plan/adr/ADR-016-delivery-build-before-team.md`, replace the row of question 14 with:

```markdown
| 14 | The store that anchors the audit chain | An object store with write-once retention, behind the audit anchor port. Its owner takes the anchors and verifies the chain, with its own credential and a database role that can only read (Plan 6a) | 6a |
```

At the end of its History, add: "- **2026-10-15:** the store's owner, not the platform, takes the anchors (question 14; Plan 6a)."

- [ ] **Step 3: Split Plan 6 in the roadmap**

In `plan/2026-10-12-implementation-roadmap.md`:

1. In the header table, after the row of Plan 5b, add:

   ```markdown
   | Plan 6a | `2026-10-15-plan-6a-audit-chain.md` (executed) |
   ```

2. Replace the row of Plan 6 with these two rows:

   ```markdown
   | 6a | Audit chain | The hash-chained audit store, its hourly anchor, which the anchor store's owner takes, and verification (spec Section 9.4) | Plans 4 and 5 | about 1 |
   | 6b | LLM gateway | The request allowlist, per-run budgets, cost attribution and prompt logging into the chained store (ADR-007) | Plan 6a | 1.5–2 |
   ```

3. In Section 3, replace "An owner, outside the builders, for the store that anchors the audit chain" with "An owner, outside the builders, for the store that anchors the audit chain, who also takes the hourly anchors and verifies the chain".
4. In Section 5, after the sentence on Plan 5b, add: "Plan 6a, on 2026-10-15, built the audit chain: every audited row is chained as it is inserted, the anchor store's owner takes the anchors, and verification shows a change, a deletion or a restore."

- [ ] **Step 4: Update the README**

In `README.md`, replace "and Plan 5b its board tools and record-only proposals; Plan 6, the LLM gateway and the audit chain, is next." with "Plan 5b its board tools and record-only proposals, and Plan 6a the audit chain; Plan 6b, the LLM gateway, is next.", and after the line for Plan 5b in the list of plans add `- [Plan 6a: the audit chain and its anchor](plan/2026-10-15-plan-6a-audit-chain.md) (executed)`.

- [ ] **Step 5: Mark this plan executed**

In this plan's header table, set the status to "Executed on DATE, inline on main. See "Changes made during execution" at the end.", with the date it ran, and add that section at the end: the rulings made during execution, and the final review's findings and what became of each.

- [ ] **Step 6: Commit**

```bash
git add plan/2026-10-11-agent-swarm-platform-spec.md plan/adr/ADR-016-delivery-build-before-team.md plan/2026-10-12-implementation-roadmap.md README.md plan/2026-10-15-plan-6a-audit-chain.md
git commit -m "docs: Plan 6a executed; spec amended for the audit chain and its anchor"
```
