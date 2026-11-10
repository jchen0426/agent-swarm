# Plan 9a-2 design re-review v2 (scoped)

| | |
|---|---|
| Under review | `plan/2026-11-10-plan-9a2-live-board-design.md`, Draft v2, against review v1 (`plan/2026-11-10-plan-9a2-design-review-v1.md`) and the code at HEAD 09f0adb |
| Method | Read-only. Every claim was checked against the code, the Fetch and HTML standards, or MDN's Trusted Types reference. |
| Verdict | **Of v1's 20 findings, 15 are addressed and 5 partly** (I3, I4, I5, M8, M9). **v2 introduces 2 new Important and 5 new Minor findings.** The Important residual of I4 (the semaphore) counts as new too. The polling, the inert XHR document and the forward formula are sound. Fix N1, N2 and the I4 residual before the author's approval. |

## 1. v1's findings

| # | Status | Where in v2 | Note |
|---|---|---|---|
| I1 | Addressed | D1 | Polling replaces SSE. `/board` answers 401 with no redirect, and the script backs off and resumes on visibility. |
| I2 | Addressed | D1 | Polling holds no connection open. |
| I3 | **Partly** | D1, D2 | The fixed kill-switch header is now a region, and the kill switch and lead state are in the version. But the facts "computed at each render" are never rendered once the poll answers 304 (N1). |
| I4 | **Partly** | D2 | The 1 s shared probe, the strip from records, the singleflight and Debug logging are all in. But the semaphore of [4] equals pgx's smallest default pool, `max(4, NumCPU)`, so "viewers can never take the coordinator's connections" is false on a host with 4 CPUs or fewer. The semaphore also bounds projections only, not page loads, sibling reads or raw-output reads. **Fix:** at most half the pool, or 1–2, covering every web read; or give the web a pool of its own. Still Important. |
| I5 | **Partly** | D1 | The focus rule protects keyboard users. A screen reader in browse mode does not move `activeElement`, so a region can still be replaced under it, despite the anti-goal. v1's keeping of each `<details>` element's open state and the scroll position across a replacement was dropped (N6). "Each carries the version it was rendered at" cannot also mean "replaced only when its own version changed": each region needs its own content hash. |
| I6 | Addressed | D5 | Forward formula with a real-rounds test. Two precision points are in §3. |
| I7 | Addressed | D4 | |
| I8 | Addressed | D8 | People see typed records; the agents' side is carried on. Match a trigger record on (source, event id), since an event id is unique per source only (`Inbound.Seen(source, eventID)`, `ingest.go:107`). |
| I9 | Addressed | D11 | |
| I10 | Addressed | D2, D13 | Latent until 8a. Two small leaks remain (N5). |
| M1 | Addressed | D1 | Checked in §3. |
| M2 | Addressed | D1, D12 | See N6. |
| M3 | Addressed | D6 | |
| M4 | Addressed | D5 | `RoundEnded.Converged` is recorded (`item.go:426`). |
| M5 | Addressed | D3, D7 | |
| M6 | Addressed | D8, D9 | |
| M7 | Addressed | D10 | Conforms to spec 10.1. Gate on `Grounds == Member`, not on `May`'s error being nil, which also lets in a fresh admin through `AdminOverride` (`policy.go:94`). D15's admin case will catch it. |
| M8 | Partly (moot) | D1, D2 | SSE is gone, so most of it is moot. The new 401 path can reload-loop during a key rotation when reads alternate between instances that cache different keys for up to 1 min (N4). |
| M9 | **Partly** | D1 | One fetch in flight, the query carried and a first poll answering 304 are all in. "Drop a response older than the one applied" cannot be built on an opaque hash (N3). |
| M10 | Addressed | D15 | |

## 2. New findings

**N1 (Important; D1, D2): a 304 answer freezes everything outside the version.**
- **What escapes it.** The version hashes the latest record, the latest board entry, the kill switch and the lead state. D2 says the queue position, the strip and the viewer's parts are computed "at each render", but an unchanged version answers 304, and on a 304 nothing renders. On an idle item:
  - the strip never updates, so ruling 4's "30 s late" becomes "until this item's own records change";
  - row 1's queue position stays stale (spec 10.2);
  - every relative time freezes: "N min ago" and "closes … (in N min)" (`layout.go:81-93`), and "may be reopened until T" outlives T (`internal/web/pages/board.go:266-268`).
- **Fix.** Make the ETag the version plus the incident's strip-cache generation, the queue position held in memory, and the current minute; or render only absolute times. The projection cache can stay keyed by the version alone.

**N2 (Important; D14): the form-key change does nothing.**
- **The MAC already survives a sign-in.** `FormMAC` binds only the subject, the act, the item and the key (`internal/web/auth/keys.go:138-144`). It is already "bound to the subject and a form nonce, not to freshness".
- **What actually breaks the retry is the CSRF token.** It binds the session's `IssuedAt` (`keys.go:135`), and every sign-in, a fresh one included, mints a new `IssuedAt` (`flow.go:337`). The old form is then refused with "This form is out of date … Reload the page" (`csrf.go:41-46`). The reload brings a new key, and a new key opens a second item.
- **Fix.** On that refusal, or on the 401 path, re-render the same form with the same key and MAC, its values kept, under the new session's token. The MAC proves the key was issued to this subject, so resending it is the same act. Alternatively, show the pending act: the key gives the item id.

**N3 (Minor; D1, D2): an opaque version cannot be ordered.** The page is rendered from fresh reads, while polls read the latest tick, which can be up to 1 s old. Two instances also tick apart. A poll can therefore answer an older board, and an entry vanishes for one poll. **Fix:** stamp every page and every response with the tick's version, never a fresher one. Or encode the seqs so the server can decode them, and never answer a version older than the one the client sent.

**N4 (Minor; D2, profile): the lead state flaps when reads alternate between instances.** Spec and profile let `/readyz` serve reads on either instance. Then consecutive polls carry different versions. Every poll answers 200, and the standby notice and the act buttons flip every 5 s. **Fix:** the profile routes the web to the leader (`/leaderz`), falling over to the standby only when no leader answers. This also ends M8's reload loop.

**N5 (Minor; D13): small leaks and gaps on a hidden item.**
- The hidden version, the state word alone, never refreshes the fixed kill-switch header. Add the switch's seq and the lead state; neither leaks anything of the run.
- The indicator's "last updated at T" is "the latest record … time". Item records include `round_opened` and `round_ended`, so on a hidden item T must be the time the state word last changed.

**N6 (Minor; D1): `<details>` state and scroll are no longer kept.** v2 dropped v1's rule. A phone's feed arrives `open` and is re-opened by every replacement unless the script copies the old `open` state. Replacing a region above the viewport also shifts the view. **Fix:** restore v1's rule.

**N7 (Minor; D2, D8): two small gaps.**
- The first poll for an item the last tick did not cover (just opened, or idle for more than 10 s) has no result to read. Probe it alone, or wait for the next tick.
- `Triggered.Title` copies source text into the chained work records. Item titles already live there, so it is not a new kind of data, but name its retention class. The text also already exists in the inbound store (`InboundEvent.Title`, `ingest.go:504`), an alternative that needs no record change.

## 3. The checks asked for

- **The inert XHR document, `adoptNode` and `trusted-types 'none'`: sound.**
  - **No sink.** `XMLHttpRequest` with `responseType = "document"` parses network bytes, not a string. It is not among Trusted Types' sinks in MDN's list (`innerHTML`, `outerHTML`, `insertAdjacentHTML`, `DOMParser.parseFromString`, `Range.createContextualFragment`, `document.write`, `srcdoc`, the `setHTMLUnsafe`/`parseHTMLUnsafe` family). Neither `adoptNode` nor `replaceWith` is a sink.
  - **No script risk.** Script elements parsed into a scripting-disabled document never run when moved. Inline handlers, `javascript:` URLs and inline scripts are blocked anyway by `script-src 'self'` without `'unsafe-inline'`, and `base-uri`, `form-action` and `default-src 'none'` cover the rest.
  - **Two caveats.** The region content's safety still rests on html/template. Per-engine verification is left to D15's browser gate.
- **304s from XHR: sound.** An author-set `If-None-Match` puts the fetch in no-store mode, so the script sees the 304 itself (Fetch standard). `no-store` on the response does not interfere.
- **The 401 without a redirect: sound.** The one caveat is N4's alternating instances.
- **D5's formula: sound** against `decide.go` and `rounds.go`.
  - **What it covers.** Grace and SettleWait run after `EndedAt`, which equals the deadline (`turns.go:238`, `rounds.go:291-295`, `320`), and W covers them. Resumes start a normal round (R67), and n counts it. `wrapUps` counts across the whole run, a resume included (`state.go:678-690`), so k carries over correctly. Outages and takeovers are named as moving T.
  - **Precision 1.** Add the open round to U only when it is a normal round. A wrap-up counts no wall clock (`decide.go:119-122`), and adding one understates T by up to L when convergence is then revoked.
  - **Precision 2.** k excludes the open wrap-up only if that round counts.
- **D8's `Title`: no migration needed.** `item.Body` decodes with plain `json.Unmarshal`, so older builds ignore an unknown field (`item.go:116-131`). R12 keeps bodies at version 1 until the first release. 7c-1b's "migration at its own level" rule covers new kinds, codes and causes, not optional fields. The audit chain hashes whatever body is stored.
- **D10: conforms to spec 10.1.** The one caveat is M7's `Grounds == Member` point.
- **D14: no effect** (N2).
