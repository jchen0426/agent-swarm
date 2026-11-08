# Plan 9a-1: sign-in, roles, the items view and the kill switch's control — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Approved by the author on 2026-11-08, version 3, with R-f's trigger numbers as proposed (8 calendar days for this plan; spend calibrated after Task 1). Executed on 2026-11-08: Tasks 1–11, then the branch's end. Before the final review (d3cebda) and after its fix wave (bfea716), `make test-linux` (123 packages ok), `make test-docker` and the 40-minute soak passed (39,459 and 37,930 schedules before; 38,558 and 38,355 after). See "Changes made during execution". |
| Date | 2026-11-08 |
| Spec | `plan/2026-10-11-agent-swarm-platform-spec.md`: Sections 4.1, 5.1, 8.2, 8.3, 9.1, 9.3, 9.4, 10.1, 10.2, 10.4 and 15. Task 11 amends Sections 5.1, 8.3, 9.1 and 15 as the author ruled (below). |
| Design | `plan/backups/plan9a/design-v2.md`, as corrected by its re-review (`design-rereview-v2.md`) and by this plan's review (`plan-review-v1.md`). The first design review is `design-review-v1.md`. Task 11 copies them, with the plan reviews, into `plan/` as tracked files. |
| Roadmap | `2026-10-12-implementation-roadmap.md`, row 9a. Task 11 splits it into 9a-1 (this plan) and 9a-2 (the live board). |
| Size | **Raw, about 22.5–27.5 days, about 4.5–5.5 person-weeks; executed, at the record's 1.3–1.5, about 5.9–8.3. Calendar guess, about 3–4 days. Spend: no basis on record; set after Task 1 (R93).** Under R-f, calendar time and spend stop a plan; person-weeks are recorded. See "Size". |
| How it was made | A fact sweep, then design v1 and its Opus review (2 Critical, 15 Important, 16 Minor), then the author's rulings R-a to R-f, then design v2 and its Opus re-review (0 new Critical, 6 new Important, 15 new Minor). Version 1 of this plan followed, and its Opus plan review (0 Critical, 12 Important, 25 Minor; `plan-review-v1.md`). Version 2 answered every finding: Important I1 to I12 and Minor m1 to m25 are each cited where they are taken. A scoped re-review on Opus 5.5 (`plan-rereview-v2.md`) found 8 of the 12 Important closed and 4 partly closed, and one new Important, NI1 (`http.CrossOriginProtection` lets a POST without `Origin` through), with Minors n1 to n12. This version, 3, answers those. Versions 1 and 2 are kept at `plan/backups/plan9a/plan-9a1.v1.md` and `plan-9a1.v2.md`. |
| Decided by the author | R-a to R-f, below. The planner's calls are R76 to R97, each marked "(the planner's call; cost if wrong: ...)". Approving this plan approves them. |

**Goal:** A person can see the platform and safely stop it, with a real identity. They sign in through the organization's OIDC provider. Their roles are checked in the coordinator. They see the items, promote, close, reopen and open an item, and turn the kill switch on, or off as an admin after a fresh authentication. All of this works the same way during a drain as the admin API does, and a standby refuses the switch and names the leader.

**Architecture:**
- **In the coordinator,** `app.Actor` gains `Responder`, and one policy function, `app.May`, decides who may act on what. `Open` gains an actor, records who opened, and checks principals and responders. `Views` gains a list, a light fold, the stop entry and the status words. The opening's id and digest move from `internal/admin` to `app`.
- **The admin API's routing** of the kill switch and of the item acts' refusals becomes the package `internal/admin/switching`, which returns values that both front ends use.
- **A new web listener** on loopback sits behind the deployment's TLS proxy. It serves `internal/web`: OIDC sign-in with sealed cookies, CSRF checks, security headers, and pages rendered by `html/template`, with no JavaScript.
- **Test support.** `internal/web/webtest` is an importable fake IdP, a session helper and a browser-like client that applies the Fetch rule for `Origin`. The black-box harness uses it behind a TLS stub, in scenarios under the process runner.

**Tech Stack:** Go 1.26 (`go 1.26.0`).
- New modules: `github.com/coreos/go-oidc/v3` and `github.com/go-jose/go-jose/v4`. The latter becomes a direct requirement, since `webtest` signs tokens with it.
- `golang.org/x/oauth2` is promoted from indirect.
- The standard library supplies `crypto/hkdf` (since Go 1.24) (m14). `net/http.CrossOriginProtection` is not used: it accepts `Sec-Fetch-Site: same-origin` whatever `Origin` says, and a request with neither header (NI1).
- No migration. One optional record body field, `Opened.By`.

**How a task runs.** As in Plan 7b-1: light plan, test first. Each task gives its decisions, the interfaces it consumes and produces, and its tests by name, each with what it asserts. Each task is reviewed by a fresh reviewer on Opus 5.5, and the whole branch at its end. The controller keeps the spend ledger (R93).

## The author's decisions

- **Earlier decisions:** those of 2026-10-14, 2026-11-01 and 2026-11-02, as Plan 7b-1 lists them; the cut of Plan 7b-2 on 2026-11-07; Plan 3c moved to the onboarding build on 2026-11-08.
- **R-a (2026-11-08).** Row 9a is split. 9a-1 is this plan. 9a-2, the live board, follows it.
- **R-b.** A standby refuses both on and off; spec Section 8.3 stands. Its pages say so and name the leader.
- **R-c.** The reviews' cuts are taken:
  - no admin API sign-in, which moves to Plan 10a;
  - TLS through the deployment's proxy only;
  - a board page without quotes;
  - the invisible-arm row left to Plan 8a, behind one seam;
  - a minimal opening form;
  - web scenarios under the process runner only.
- **R-d.** After sign-in, the loopback admin token stays as the break-glass path on the host. It turns the switch on and off, opens items, and is recorded as subject `admin`. Spec Sections 5.1, 8.3 and 9.1 are amended to say so.
- **R-e.** Role checks move into the coordinator, through one policy function. This changes Plan 7b-1's recorded division of labour, which put them above C16.
  - Item acts and the opening are decided under the coordinator's lock.
  - The kill switch is decided in `checkSwitch`, which the drain's route runs without the lock. That is harmless, since the check is pure (m24).
- **R-f.** ADR-016's revisit trigger becomes calendar time and cost, as proposed in "The revisit trigger" and confirmed with this plan.

## Global Constraints

- **Module and Go version.** `github.com/jchen0426/agent-swarm`, `go 1.26.0`.
- **Vocabulary.** The spec's words. Agents are "AI agents" or "agents", never "employee".
- **Domain purity.** `internal/work/item`, `internal/work/board`, `internal/work/control`, `internal/work/tokens`, `internal/holding` and `internal/outcome` import no database, network or framework package. `internal/web` imports never a store and never `internal/admin` itself (it does import `internal/admin/switching`, the domain packages, `internal/deploy` and go-oidc) (n8). Every store and secret it needs reaches it as a port that serve wires.
- **Decide, record, act.** Unchanged. This plan adds no decision of the coordinator's beyond refusals, and one optional field to the opening's record.
- **The holding.** Every guarded write runs under a context that carries its holding's epoch. The web layer writes only through the coordinator's use cases and `switching`.
- **Secrets.** These are read through the secret store at use, and never logged, rendered or repeated in a refusal: the session key, the OIDC client secret, ID tokens, codes, `state` and cookies.
- **No JavaScript** in 9a-1. Every page works without it.
- **Test first.** Every listed test is written before its implementation and seen to fail for the reason its line gives, unless it is marked a guard.
- **Language.** Code, comments and documents in English.
- **The gates.** Every task ends green on the Mac with `make lint` at `0 issues.` and its own packages' tests with `-race`. Tasks 1, 2 and 3, which touch `internal/work/app` or the admin routing, also run the interleaving test's default run within its budget (m6). At the branch's end, `make test-linux`, `make test-docker` and the 40-minute soak run, before the final review and after its fixes. Commit by path, never `git add -A`, with no `Co-Authored-By` line.

## Review Focus

1. **A drain.**
   - The on through the web must be recorded deferred, through `RecordKillSwitchOn`.
   - A viewer's on must be refused there too, writing no row.
   - `/leaderz` must answer 200 on the draining leader.
   
   (`TestAViewersOnDuringADrainIsRefusedAndWritesNoRow`, Task 1; `TestTheWebsOnDuringADrainIsRecordedDeferred`, Task 8; scenario 27, Task 10)
2. **A form POST from a served page,** under the referrer policy the page itself sets, must carry a matching `Origin`. No POST may be answered with a redirect off the origin (N1; I8). (`TestAServedFormsPostCarriesItsOriginAndIsAccepted`, `TestCSRFRefusesAMissingNullOrForeignOriginWhateverSecFetchSiteSays`, `TestNoServedPageSetsANoReferrerPolicyBesidesTheCallback`, `TestAPOSTIsNeverAnsweredWithARedirectOffOrigin`, Tasks 5 and 7)
3. **Fresh authentication with an IdP that ignores `prompt=login`, or a different person.** Each must fail closed, and roles must be re-read. (`TestFreshAuthenticationRefusesAnOldAuthTime`, `TestFreshAuthenticationRefusesAnotherSubject`, `TestFreshAuthenticationReplacesTheSessionsRoles`, Task 6; `TestADemotedAdminCannotTurnTheSwitchOff`, Task 8)
4. **An admin who is not fresh.** They may still turn the switch on and open for themselves (I1). On an item they are not on, they must re-authenticate, and the coordinator enforces it (N3). (`TestTheActorIsAdminOnlyInsideFreshnessAndEveryAdminIsAResponder`, Task 5; `TestAnAdminsItemActNeedsAdmin`, Task 1; `TestAnAdminsItemActNeedsFreshness`, Task 7; `TestAnAdminWhoIsNotFreshTurnsTheSwitchOn`, Task 8) (n3)
5. **A load balancer's probe with the target's IP as `Host`** must reach `/leaderz`, and a proxy that rewrites `Host` must be named in the refusal (N5; I11). (`TestHealthRoutesSkipTheHostCheck`, `TestAForeignHostsRefusalNamesTheSettingAndIsLoggedOnce`, Task 4)

## Decisions

The design's decisions D1 to D10 (`design-v2.md`) hold as amended below. The planner's calls:

- **R76. Admin rights only inside freshness (N3).** The web layer builds `Actor{Subject: "sso:" + v, Responder: responder || admin, Admin: admin && fresh(auth_time)}`.
  - Every admin is a responder (I1). So an admin who is not fresh still turns the switch on and opens items for themselves, with no fresh authentication (spec 8.3 and 10.1).
  - The coordinator enforces spec 9.1 for item acts: an admin's act on an item where they are neither principal nor responder needs `Admin`, which exists only while fresh.
  - For a session holding the admin role, a refusal `admin_only` or `not_item_responder` renders as "Re-authenticate to act as an admin", with the fresh link.
  - The buttons a page shows come from `app.May` called on the row (R77). There is no copy of the policy in the web layer.
  
  (Cost if wrong: an admin re-authenticates more often than strictly needed.)
- **R77. `app.May` returns its grounds.** `func May(a Actor, k ActKind, o *item.Opened) (Grounds, error)`, where `Grounds` is `Member`, `AdminOverride` or `Unrestricted`. The coordinator uses the error; the web uses the grounds. (Cost if wrong: one exported type more.)
- **R78. The opening's actor (N2(a)).**
  - `OpenInput.Actor` is new. `Open` calls `May(actor, ActOpen, nil)` first, before `c.repeated` (m10), so that an actor with no role cannot learn whether a key opened an item.
  - It records `Opened.By`, the opener's subject, as an optional field. Under Plan 7d-1's R12, a body decodes non-strictly (`item.go:99-119`): an older build drops `by`, and a body without it folds as before. The 7b-1 carry about new record kinds does not apply to an optional field (m1).
  - A non-admin's principal must equal their subject: "invalid: a responder opens items as their own principal". Only an admin names another principal; in the web that admin is fresh, by R76.
  - The admin route passes `adminActor`. Inbound sources (Plan 7c-1) will pass a source actor, which this rule would refuse; that is carried to 7c-1 (m10).
  
  (Cost if wrong: a record field to carry forever.)
- **R79. The on's role check in `checkSwitch` (N4).** `checkSwitch` gains, before the reason check: `on && !a.Actor.Responder && !a.Actor.Admin`, which gives `*Refused{Code: "responder_only"}`. It covers `SetKillSwitch`, its preview, and `RecordKillSwitchOn`. (Cost if wrong: none found.)
- **R80. Principals and responders (I2, I9, M-e, M-f, m9).** `Open` checks them as follows:
  - Trim them.
  - Refuse a value that is empty when present, over 256 runes, or holding `config.Unseen` characters.
  - Refuse more than 16 responders.
  - Refuse the bare `admin`.
  - Refuse a value that the always-applying rules of the **current** revision would change, as `checkRouting(rev, ...)` does (`coordinator.go:294-300`).
  
  The admin API's documentation says people are named as `sso:<value>`. At sign-in, a subject is checked for length and `config.Unseen` only, never against redaction rules. So a deployment whose always rule redacts emails can still sign people in by email (proved in Task 6, m20). Such people cannot be named as principals, and the deployment docs say so. (Cost if wrong: an opening refused that a team needs, which is a rule change.)
- **R81. The shared routing (I3, m2, m3, m4).** The package `internal/admin/switching` returns values:
  - `func Set(ctx context.Context, p Ports, on bool, act *app.Act) Result`, the kill switch;
  - `func Act(p Ports, err error) *Refusal`, the item acts' refusals, which read `Leader.Status` for `draining`, `taking_over` and `not_held`, as `standing` does (`admin.go:434`), and log through `p.Log` (I3);
  - `func Opening(p Ports, err error) *Refusal`, the opening's refusals with R15's codes, likewise;
  - `Result{Switched *app.KillSwitchOutcome; Deferred bool; Refusal *Refusal}`;
  - `Refusal{Status int; Code string; RetryAfter time.Duration}`.
  
  `Ports` holds `Coordinator`, with `SetKillSwitch` and `RecordKillSwitchOn`; `Leader`, with `Status`; and `Log *slog.Logger`, since `unheld` and `lifecycleRefused` log.
  
  These move out of `internal/admin`:
  - from `lifecycle.go`: `setKillSwitch`, `switchNotHeld`, `recordOn` with R24's retry, `lifecycleRefused`, `unheld` and `refusal()`;
  - from `admin.go`: `standing` (`admin.go:434`, which the opening's `notHeld` shares), `invalid()`, `heldByLaterBuild`, `refusedTexts` and the codes.
  
  `admin.ItemID` stays as a wrapper over `app.ItemID`, since `admin_test.go` calls it in five places (n1). The admin API keeps its own texts, which carry API instructions such as `Idempotency-Key` and "the seq read as after". The web has its own table of words keyed by code (Task 7), and a test that every code has a web text. `lifecycle_test.go` passes unchanged as the guard. (Cost if wrong: a moved function; the admin's tests catch drift.)
- **R82. Re-read the table after any non-final switch answer (I7, M-o).** After `stale`, `unavailable`, `taking_over`, `not_held` or `draining`, the page reads `Views.KillSwitch` and shows the state now, with a fresh form whose `After` is that row.
- **R83. Item acts have no `After` (M-a).** `After` is the kill switch's alone. An item act previews and commits with the same key.
- **R84. A close whose preview cannot answer (M-b)** is offered, labeled "Consequences cannot be counted right now", as the on is.
- **R85. Form keys are minted and MAC'd (M-c).** A form's key is `k`, 16 random bytes in base64url, carried with `mac = HMAC(subkey_forms, subject|act|item|k)`. A posted key whose MAC fails is refused, `invalid`. For openings the item field is `open`. A retry after `unavailable` or `taking_over` re-renders with the same key and MAC (I7). The opening's item id comes from `app.ItemID(tenant, "web:"+k)`, the prefix keeping the web's keys apart from the admin API's (n2), and its digest from `app.OpeningDigest`, both moved from `internal/admin` in Task 3 (I2). The tenant reaches the web through `web.Ports.Tenant`.
- **R86. Keys by HKDF (M-j, m16, m22).**
  - The secret at `web.sessionKeyRef` holds a base64 (std) encoding of at least 32 random bytes, with surrounding whitespace trimmed. The harness writes `value+"\n"`.
  - `crypto/hkdf` with SHA-256 derives three subkeys: `session` (AES-256-GCM), `csrf` (HMAC) and `forms` (HMAC).
  - A key that cannot be read fails closed. Every page but `/healthz`, `/readyz` and `/leaderz` then answers 503 "sign-in is unavailable", and the failure is logged at most once a minute.
  - The key is read at use, cached at most 1 min.
- **R87. Referrer policy (N1).** Every page carries `Referrer-Policy: same-origin`. `/auth/callback` answers a bare 303 to the return path, with `Referrer-Policy: no-referrer`.
- **R88. Health routes skip the Host check (N5).** `GET /healthz`, `/readyz` and `/leaderz` read no cookie, set none, are exempt from the Host check, and never answer the key's 503.
- **R89. Groups as given (M-i).** Group values are matched as strings, as the IdP emits them; object ids are allowed. A missing groups claim, or an overage marker (`_claim_names` naming the claim), lands on "no role", with words naming the cause.
- **R90. The leader's name (M-g, I6).** `postgres.Lease` gains `Holder(ctx) (name string, expires time.Time, err error)`, which reads `leases.holder`. It reaches the web as the port `web.Ports.LeaseHolder`, which serve wires. The standby page shows the holder's name, which is `d.Holder`, a name and not a URL, and says so.
- **R91. The opening form's service (I9).** A text field, checked by `checkRouting`'s rules, with the `service` values of the items open now offered as suggestions in a `<datalist>`. No configuration lists services. (Cost if wrong: a typo opens an item for a service nobody watches, which close fixes.)
- **R92. `ClosedSince` (M-m, m6).**
  - It returns the items with an `item_closed` and no later `item_reopened`, whose latest `item_closed` committed after `now() - window` by the database's time.
  - It sits on a narrow interface, `app.ClosedLister`, which only `Views` takes, so that `recordsPort` (`interleave_world_test.go:1149`) and `failing` (`harness_test.go:675`) do not change.
  - Postgres runs it in SQL; the memory store runs it from the records' times.
- **R93. Spend, measured (I10).**
  - **The ledger.** The controller keeps `plan/backups/plan9a/spend.md`. After every subagent it appends one line: the task, the role, the model, the input, output and cache-read tokens the harness's completion notice reports, and the dollars at the vendor's published rates for that model, with cache reads at their own rate. Where a notice gives only a total, the total is priced at the input rate, an upper bound, and the line says so (n10).
  - **Spend** is the ledger's dollars plus any live run's dollars; this plan has none.
  - **The calibration.** After Task 1 and its review, the controller sets the plan's spend guess to Task 1's dollars times the ratio of the plan's raw days to Task 1's, and reports it to the author.
  - **The calendar** counts from the first task's start to the push, less time spent waiting for the author.
- **R94. `web.oidc.caBundle` (I5).** It is a PEM path trusted beside the system roots, checked as `mcp.caBundle` is (`deploy.go:190-195`). The OIDC client uses it through `oidc.ClientContext`. The harness writes it for its test CA.
- **R96. The CSRF gate (NI1).** Every POST must carry an `Origin` header equal, after R95's normalization, to `publicURL`'s origin. A missing `Origin` or `Origin: null` is refused, whatever `Sec-Fetch-Site` says, and so is any other origin. Then the HMAC form token is checked. `http.CrossOriginProtection` is not used. `webtest.Browser` sends `Origin` by `BrowserOrigin` and `Sec-Fetch-Site` as Chrome does.
- **R97. Sign-out answers a page (n5).** `POST /auth/signout` clears the cookie and answers 200 with a "signed out" page and a sign-in link. It never redirects.
- **R95. The Host refusal names its cause (I11, m15).** The Host and Origin checks compare normalized: a default port stripped, case folded, a trailing dot dropped, and IPv6 in brackets. A refused `Host` answers 400 "Host does not match web.publicURL", and serve logs such a refusal at Warn, at most once a minute, with the host it received, which is not a secret (amended in execution: first written as the first refusal only). The docs say the proxy must forward the client's `Host` unchanged (nginx: `proxy_set_header Host $host`).

## Earlier tests this plan changes

- **Task 1:**
  - `killswitch_test.go:1002`, a non-admin `alice`, gains `Responder: true` where it turns the switch on.
  - Tests that build an `OpenInput` gain `Actor`.
  - Tests that act on items as a non-admin gain the subject as principal.
- **Task 3:** `internal/admin/lifecycle_test.go` and `admin_test.go` are the guards and do not change.

## File Structure

| File | Task | Responsibility |
|---|---|---|
| `internal/work/app/acts.go`, `policy.go` (new), `killswitch.go`, `promote.go`, `close.go`, `reopen.go`, `coordinator.go`; `internal/work/item/item.go` | 1 | `Actor.Responder`; `May`; the checks in `checkSwitch` and the acts; `OpenInput.Actor`; `Open`'s checks; `Opened.By` |
| `internal/work/app/views.go`, `ports.go`; `internal/work/postgres/records.go`; `internal/work/memory/records.go`; `internal/work/board/status.go` (new) | 2 | `Views.List`, the light fold, `ClosedLister`, `View.StopEntry`, `ClosesAtUnread`, `ViewPorts.Held`, `board.StatusOf` |
| `internal/admin/switching/` (new), `internal/admin/lifecycle.go`, `admin.go`; `internal/work/app/opening.go` (new); `internal/work/postgres/lease.go` | 3 | The shared routing and refusals; `ItemID` and `OpeningDigest` moved to `app`; `Lease.Holder` |
| `internal/deploy/deploy.go`; `cmd/agentswarm/listeners.go`, `serve.go`; `internal/web/server.go`, `headers.go`, `health.go` | 4 | `listeners.web` and `web.*`; the fourth listener; the server, headers, Host check, health routes and logs |
| `internal/web/auth/keys.go`, `session.go`, `csrf.go`, `return.go`, `actor.go`; `internal/web/webtest/session.go`, `browser.go` | 5 | Keys, the sealed session, CSRF, return paths, the actor; the session helper and the browser-like client |
| `internal/web/auth/oidc.go`, `flow.go`, `fresh.go`; `internal/web/webtest/idp.go` | 6 | OIDC, flow cookies, the callback, freshness; the fake IdP |
| `internal/web/pages/layout.go`, `items.go`, `acts.go`, `words.go`, `standby.go`; `templates/`; `static/app.css`; `cmd/agentswarm/serve.go` (the wiring) | 7 | The layout, header and stylesheet, `/items`, the item acts, the refusal words, the standby, and the serve wiring of `web.Ports` |
| `internal/web/pages/killswitch.go`; templates | 8 | `/kill-switch`: the on, the off, the answers, R82, the drain |
| `internal/web/pages/open.go`, `board.go`, `visible.go`; templates | 9 | The opening form, the board page, the `visible` seam |
| `test/e2e/harness/tlsproxy.go` (new), `deployment.go`, `serve.go`, `env.go`; `test/e2e/web_test.go` (new) | 10 | The TLS stub, the `web` block and secrets, `Addresses.Web`, the IdP's lifetime; scenarios 26 to 28 |
| `plan/...`, `README.md` | 11 | Amendments and documents |

---

## Tasks

Commands run from the repository's root. Unit tests use the memory stores and the harness clock, as in Plan 7b-1. Each task is about 1.5–3.5 raw days (I12; n9).

### Task 1: Roles in the coordinator, and the opening's actor and checks

**Decisions:** R76 (its coordinator half), R77, R78, R79, R80; design D5 and D6.

**Produces:**
- `app.Actor{Subject string; Responder, Admin bool}`;
- `type ActKind string` with `ActOpen`, `ActPromote`, `ActClose`, `ActReopen`, `ActSwitchOn` and `ActSwitchOff`;
- `type Grounds int` with `Member`, `AdminOverride` and `Unrestricted`;
- `func May(a Actor, k ActKind, o *item.Opened) (Grounds, error)`;
- `CodeNotItemResponder = "not_item_responder"` and `CodeResponderOnly = "responder_only"`;
- `OpenInput.Actor`;
- `item.Opened.By`.

**Tests (write first):**
- `TestMayDecidesEveryActForEveryRole`, a table covering:
  - viewer, responder and admin, fresh and not;
  - a member, as principal or as responder, and a stranger;
  - every `ActKind`.
- `TestAResponderActorOpensForThemselvesAndTurnsTheSwitchOn`: the actor the web builds for an admin who is not fresh (`Responder: true, Admin: false`; Task 5 proves the web builds it so, n3) turns the switch on and opens for themselves.
- `TestAViewersOnIsRefusedResponderOnly`: preview included.
- `TestAViewersOnDuringADrainIsRefusedAndWritesNoRow`: through `RecordKillSwitchOn` (N4).
- `TestAStrangerResponderCannotPromoteCloseOrReopen`: `not_item_responder`, with no record, previews included (m11).
- `TestAnAdminsItemActNeedsAdmin`: a stranger with `Admin` acts, with `Grounds` `AdminOverride`; without it, the act is refused.
- `TestTheLoopbackAdminActsAsBefore`: a guard.
- `TestOpenRecordsWhoOpened`: `Opened.By`; a body without `by` folds.
- `TestAResponderOpensOnlyAsTheirOwnPrincipal`.
- `TestAnActorWithNoRoleLearnsNothingFromARepeatedKey` (m10).
- `TestOpenRefusesAPrincipalOrResponderWithUnseenCharacters`, `...OverLong`, `...SeventeenResponders`, `...BareAdmin` and `...ThatTheCurrentRevisionsRulesWouldChange`. None of these refusals quotes the value.

- [ ] Write the tests; see each fail for its reason.
- [ ] Implement `policy.go`; the checks in `checkSwitch`, `Promote`, `Close` and `Reopen` after `target()`; and `Open`'s actor and checks.
- [ ] Update the earlier tests.
- [ ] Gates: `make lint`; `go test -race ./internal/work/...`; the interleaving default run.
- [ ] Commit: `feat(work): roles decided in the coordinator, and the opening's actor and checks`.
- [ ] Controller: the spend calibration (R93), reported to the author with the task's completion.

### Task 2: The items list, the light fold, the stop entry and the status words

**Decisions:** R92, I15, M11, M12; I7, m5, m6, m17.

**Produces:**
- `app.ClosedLister{ClosedSince(ctx, window time.Duration) ([]item.ID, error)}`, implemented by both stores, and taken by `NewViews` through `ViewPorts`;
- `ViewPorts.Held func() bool` (m5);
- `func (v *Views) List(ctx context.Context) ([]Row, error)`;
- `Row{ID, Title, Service, Principal, Responders, State, QueuePosition int, PositionKnown bool, ClosedAt, ReopenableUntil, ClosesAt, ClosesAtUnread, HeldByLaterBuild, Incident}`;
- `View.ClosesAtUnread`;
- `View.StopEntry *board.Stop`, from the same replay `Views.Item` makes;
- `func board.StatusOf(a *board.Assessment, i int) StatusWord`, defined by this table:

| Word | When |
|---|---|
| converged | `Converged` |
| rejected | `Rejected` |
| refuted | `len(StandingRefutations) > 0 && !Rejected` |
| leading | rank 0 and none of the above |
| trailing | the rest |

**Tests:**
- `TestClosedSinceUsesTheDatabasesTime` (Postgres, testcontainers): an item closed just inside and just outside the window. The latest close counts, and an item reopened since is excluded.
- `TestTheMemoryStoresClosedSinceMatchesPostgres`.
- `TestListShowsActiveAndRecentlyClosedItemsOnce`.
- `TestListsLightFoldReadsNoBoardAndNoSwitchPerRow`: counting fakes.
- `TestAStandbysRowsShowPositionNotKnown`, and a leader's promoted-but-unstarted item, whose position is known.
- `TestAnUnreadAutoCloseDelayIsFlaggedAndLoggedOncePerItem` (I15; 7b-1's residual).
- `TestTheItemViewCarriesTheStopEntry`.
- `TestStatusWordsForEachHypothesisState`: the table above, on seeded assessments.

- [ ] Tests first; implement; gates, the interleaving default run included; commit `feat(work): the items list, its light fold, the stop entry and the status words`.

### Task 3: The shared routing, the opening's id moved, and the lease holder

**Decisions:** R81, R82 (its value half), R85 (`ItemID` and `OpeningDigest`), R90; I2, I3, I6.

**Produces:**
- `switching.Set`, `switching.Act`, `switching.Opening`, `Result`, `Refusal` and `Ports`, as in R81;
- `app.ItemID(tenant, key string) item.ID` and `app.OpeningDigest(in *OpenInput) string`, moved from `internal/admin` (`admin.go:282, 363, 685`), which now calls them;
- `postgres.Lease.Holder`.

**Tests:**
- `internal/admin/lifecycle_test.go` and `admin_test.go` pass unchanged: guards.
- `TestSetDuringADrainRecordsTheOnDeferred`.
- `TestAViewersOnThroughRecordOnIsRefused` (n12).
- `TestActAndOpeningReadTheLeadersStatus`: `draining`, `taking_over` and `not_held` from `Leader.Status` (I3).
- `TestSetActAndOpeningMapEveryRefusalCode`: every code of `acts.go:89-97`, plus `not_item_responder`, `responder_only`, `stale`, `unavailable`, `taking_over`, `draining`, `not_held`, `key_reused`, `held_by_later_build` and `no_such_item`, each to a status.
- `TestTheOpeningRouteAnswersItsCodes` (R15).
- `TestItemIDAndOpeningDigestAreStableAcrossTheMove`: the ids of a fixed table of keys are unchanged.
- `TestLeaseHolderNamesTheHolderAndItsExpiry` (Postgres).

- [ ] Tests first; move and refactor; gates, the interleaving default run included; commit `refactor(admin): the kill switch's routing and the acts' refusals as values both front ends use`.

### Task 4: The web listener, its server, headers and health routes

**Decisions:** R86 (the fields), R87, R88, R94, R95; design D2 and D3; I11, I12, M10, m13, m15, m16.

**Deployment fields:**

| Field | Rules |
|---|---|
| `listeners.web` | loopback, required when `web` is set |
| `web.publicURL` | https, path `/` or empty, no query |
| `web.oidc.issuer` | https |
| `web.oidc.clientID` | |
| `web.oidc.clientSecretRef` | `secret://` |
| `web.oidc.caBundle` | optional, a readable PEM, as `mcp.caBundle` |
| `web.oidc.subjectClaim` | default `sub` |
| `web.oidc.groupsClaim` | default `groups` |
| `web.groups.viewers`, `.responders`, `.admins` | each non-empty, pairwise distinct |
| `web.sessionKeyRef` | `secret://` |
| `web.sessionLength` | 1 h to 24 h, default [8 h] |
| `web.freshWindow` | 1 min to 15 min, default [5 min] |
| `web.timeZone` | IANA, default UTC |

A refusal never repeats a secret. With no `web` block there is no web listener, so today's deployments are unchanged.

**Produces:**
- `deploy.Web`;
- `web.NewServer(opts) *http.Server`: HTTP/1 only, a header timeout of 10 s that tests can set, `MaxHeaderBytes` of 64 KiB, `IdleTimeout` of 120 s and a 64 KiB body cap;
- the header middleware, with an `authenticated` marker for `no-store`;
- the normalized Host check;
- `/healthz`, `/readyz` and `/leaderz`, the last answering 200 while the lease is held, the drain included;
- in serve, the fourth listener, `Addresses.Web` in the addresses file (Contract C2), and serving through a drain, as the admin listener does.

**Tests:**
- `TestDeployRefusesEachBadWebField`: a table, an unreadable `caBundle` included, with no secret in any message.
- `TestEveryRouteSetsTheSecurityHeaders`: CSP, `X-Frame-Options`, `Referrer-Policy: same-origin`, `nosniff`, and `no-store` on a stub handler marked authenticated (m13).
- `TestTheWebServerSpeaksHTTP1Only`.
- `TestASlowHeaderIsCut`: the timeout set short, with no 10 s sleep.
- `TestAForeignHostIsRefused`: case, a trailing dot, a default port and IPv6 (m15).
- `TestAForeignHostsRefusalNamesTheSettingAndIsLoggedOnce` (I11).
- `TestHealthRoutesSkipTheHostCheck` (N5).
- `TestHealthRoutesNeverAnswerTheKeys503` (m16).
- `TestLeaderzAnswers200OnADrainingLeaderAnd503OnAStandby`.
- `TestRequestLogsHoldNoQueryCookieOrBody`.
- `TestServeWritesTheWebAddressAndServesThroughADrain` (cmd).

- [ ] Tests first; implement; gates; commit `feat(web): the web listener, its hardened server, headers and health routes`.

### Task 5: Keys, the session, CSRF and return paths

**Decisions:** R76 (its web half), R86, R87; design D4's session half; M3, M5, m14, m15, m22.

**Produces:**
- in `internal/web/auth`:
  - `Keys`, derived by HKDF and read at use;
  - `Session{Issuer, ClientID, Subject, Roles, AuthTime, IssuedAt, Expires}`, with `Seal` and `Open`;
  - the `__Host-session` cookie;
  - `CSRF(next)`, per R96;
  - `SafeReturn(path string) (string, bool)`;
  - `func (a *Authenticator) Actor(s Session, now time.Time) app.Actor`, per R76.
- in `internal/web/webtest` (importable, I4):
  - `SignedIn(t, keys auth.Keys, roles, subject) *http.Cookie`; `auth`'s own tests sit in `package auth_test`, so that `webtest` importing `auth` makes no cycle (n4);
  - `BrowserOrigin(resp *http.Response, page []byte, target *url.URL) string`. It applies the Fetch rule: `null` when the effective referrer policy of the page that served the form is `no-referrer`, from the header, then `<meta name=referrer>`, then the form's `referrerpolicy` (I8).
  - `Browser`: a cookie jar, redirects, `BrowserOrigin` and `Sec-Fetch-Site` on every form post (R96).

**Tests:**
- `TestSealAndOpenRoundTripAndRefuseAnotherDeploymentsCookie`.
- `TestRotatingTheSessionKeySignsEveryoneOutWithinAMinute`.
- `TestAKeyThatCannotBeReadFailsClosedAndIsLoggedAtMostOnceAMinute` (m20).
- `TestTheSessionKeyIsBase64WithWhitespaceTrimmed` (m22).
- `TestAnExpiredSessionOnAGetSendsToSignIn`.
- `TestCSRFRefusesAMissingNullOrForeignOriginWhateverSecFetchSiteSays` (NI1): missing, `null` with `Sec-Fetch-Site: same-origin`, foreign, and a missing token; normalized origins accepted (m15).
- `TestAServedFormsPostCarriesItsOriginAndIsAccepted` (N1): a page served by a stub handler through the real middleware, its form posted by `webtest.Browser`.
- `TestBrowserOriginSendsNullUnderNoReferrer`: `webtest`'s own test, over the header, the meta tag and the form attribute.
- `TestSafeReturnRefusesOpenRedirects`: `//evil`, `/\evil`, `%2F%2Fevil`, `/%5Cevil`, `https://evil`, control characters, and `/auth/*` targets (n7).
- `TestTheActorIsAdminOnlyInsideFreshnessAndEveryAdminIsAResponder` (R76; I1).
- `TestSignOutIsAPostUnderCSRFAndAnswersAPage` (R97).

- [ ] Tests first; implement; gates; commit `feat(web): sealed sessions, keys, CSRF and safe return paths`.

### Task 6: OIDC sign-in, flow cookies and fresh authentication

**Decisions:** R86, R87, R89, R94; design D4's flow half; I8, I9, M2, M4, m20, m21.

**Produces:**
- in `internal/web/auth`: `func NewAuthenticator(w deploy.Web, secrets Secrets, clock func() time.Time, log *slog.Logger) (*Authenticator, error)`, which Task 7 wires (I6); `Authenticator` with `SignIn` (GET `/auth/signin?return=`), `Fresh` (GET `/auth/fresh?return=`), `Callback` (GET `/auth/callback`) and `SignOut` (POST `/auth/signout`).
  - Discovery is lazy and retried.
  - The OIDC client trusts `caBundle`.
  - **Flow cookies** are named by a hash of `state`, valid 10 min, deleted at the callback, and capped at 5 live, the oldest dropped (m21).
  - The callback answers a bare 303 with `Referrer-Policy: no-referrer`.
- in `internal/web/webtest`: `IdP`, a fake OIDC provider (`httptest`, TLS from a given CA, a real RSA JWKS, settable claims, and switches to ignore `prompt=login` and to omit `auth_time`), importable by Tasks 7 to 9 (I4).

**Tests:**
- `TestSignInSetsASessionAndReturnsToTheDeepLink`.
- `TestTwoTabsSigningInDoNotClobberEachOther`.
- `TestFlowCookiesAreCappedAndDeletedAtTheCallback` (m21).
- `TestServeStartsWhileTheIdPIsDown` (M4).
- `TestRefusedTokens`, a table:
  - a wrong issuer, a wrong audience, a bad nonce, an expired token;
  - `alg: none`, HS256 signed with the public key;
  - several `aud` values with a wrong `azp`.
- `TestNoGroupsOrAnOverageLandsOnNoRole` (R89).
- `TestSubjectsAreRecordedAsSSOAndCheckedForUnseenCharacters` (I9).
- `TestAnEmailSubjectSignsInUnderAnAlwaysRuleThatRedactsEmails` (M-e; m20).
- `TestFreshAuthenticationRefusesAMissingAuthTime`.
- `TestFreshAuthenticationRefusesAnOldAuthTime`: the window plus 60 s, with the IdP ignoring `prompt=login`.
- `TestFreshAuthenticationRefusesAnotherSubject`.
- `TestFreshAuthenticationReplacesTheSessionsRoles`: a demoted admin's fresh session holds no admin role, asserted on the session and the actor (n11).
- `TestFreshAuthenticationReturnsToTheFormAsAGetAndNeverReplaysThePost`.
- `TestTheCallbackAnswersABare303WithNoReferrer`.

- [ ] Tests first; implement; gates; commit `feat(web): OIDC sign-in, flow cookies and fresh authentication`.

### Task 7: The layout, the items view, the item acts, the standby and the wiring

**Decisions:** R76 to R78 (buttons from `May`; the fresh link), R82, R83, R84, R85, R90; design D8 and D10; M6, M9, M11, M16, m4, m25; I6, I8.

**Produces:**
- `web.Ports{Coordinator, Views, Switching switching.Ports, LeaseHolder interface{ Holder(context.Context) (string, time.Time, error) }, Leader, Revisions, Tenant string, Clock func() time.Time, Log *slog.Logger}` and the `Authenticator`, built from `deploy.Web` and the secret store and wired in `serve.wire` (I6);
- **the layout:** the fixed header with the switch's state from `Views.KillSwitch` only (M6), linking to `/kill-switch`; the stylesheet, with 44 CSS px touch targets, holding at 200% text, with a dark theme;
- **`/items`:**
  - rows grouped by incident;
  - buttons from `May`;
  - "no principal: an admin acts" on items opened by the admin API without one (m25);
  - "held by a later build" as fixed text;
  - "not known on this instance" for positions on a standby;
- **the act pages** for promote, close and reopen: a preview, then a commit with the same MAC'd key. Close and reopen take a preset reason, or `other` with text;
- **the refusal words:** the web's own table, keyed by code (m4);
- **the standby:** acts disabled, and the holder's name.

**Tests:**
- `TestTheHeaderShowsTheSwitchFromTheTableAndComputesNoPreview`.
- `TestItemsPageGroupsByIncidentAndShowsOnlyTheButtonsMayAllows`.
- `TestAnItemWithNoPrincipalSaysAnAdminActs` (m25).
- `TestEachActPreviewsThenCommitsWithTheSameKey`.
- `TestADoubleSubmitChangesNothing`.
- `TestARetryAfterUnavailableKeepsItsKey`.
- `TestAKeyWithABadMACIsRefused`.
- `TestTakingOverMapsToTryAgainShortly`.
- `TestACloseWhosePreviewCannotAnswerIsStillOffered` (M-b).
- `TestAnAdminsItemActNeedsFreshness`: the refusal renders the fresh link.
- `TestEveryRefusalCodeHasWebWords` (m4).
- `TestAStandbyDisablesTheActsAndNamesTheLeader`.
- `TestAPOSTIsNeverAnsweredWithARedirectOffOrigin` (I8): for every POST route, `/auth/signout` included (n5), an expired session, a missing session and lost freshness each answer 200 or 4xx with a sign-in or fresh link, never a 3xx to `/auth/*` or to the IdP.
- `TestNoServedPageSetsANoReferrerPolicyBesidesTheCallback` (I8).
- `TestEveryPageWorksWithoutJavaScriptAndEveryStatusHasText`.
- `TestServeWiresTheWebPorts` (cmd).

- [ ] Tests first; implement; gates; commit `feat(web): the items view, the item acts, the standby, and serve's wiring of the web`.

### Task 8: The kill switch's control

**Decisions:** R79, R81, R82; design D7; I6 (the answers), M7, M8.

**Produces:**
- **`/kill-switch`, the on:**
  - one-tap preset reasons (`runaway_cost`, `bad_output`, `security`), and `other` with text;
  - `After` from the table;
  - the preview's "stops N runs on M items" when it answers, else the on still offered with the words of D7;
  - no freshness needed.
- **The off:** for a fresh admin, else the fresh link. Its preview lists the unstaffed items from `UnstaffedBy`.
- **The answers:**
  - `Stopped`, `Unstaffed` and `Owed`;
  - `Deferred`, with its words;
  - `StayedUnstaffed`, each with its state now and a promote link;
  - the unread flag;
  - "the answer below is what happened".
- **R82's re-read** after `stale`, `unavailable`, `taking_over`, `not_held` and `draining`.

**Tests:**
- `TestTheOnOffersPresetReasonsAndNeedsNoFreshness`.
- `TestAnAdminWhoIsNotFreshTurnsTheSwitchOn` (I1).
- `TestTheOnIsOfferedWhenItsPreviewCannotCount`.
- `TestTheWebsOnDuringADrainIsRecordedDeferred`.
- `TestTheOffNeedsAFreshAdminAndListsTheUnstaffedItems`.
- `TestADemotedAdminCannotTurnTheSwitchOff`: through Task 6's fresh flow and this page (n11).
- `TestTheOnsAnswerListsStoppedUnstaffedAndOwed`.
- `TestTheOffsAnswerListsStayedUnstaffedWithStatesAndTheUnreadFlag` (I6, M-h).
- `TestANonFinalSwitchAnswerRereadsTheTable`: a table of the five codes (m12).
- `TestAStandbyDisablesTheControl`.

- [ ] Tests first; implement; gates; commit `feat(web): the kill switch's control`.

### Task 9: The opening form and the board page

**Decisions:** R78, R80, R85, R91; design D9; I2.

**Produces:**
- **`/items/new`:**
  - a type from the revision's work-item types;
  - a title;
  - a service field with suggestions (R91);
  - the principal, which is the person themselves unless the session is a fresh admin;
  - the id by `app.ItemID(tenant, "web:"+k)` from the MAC'd key.
- **`/items/{id}`,** behind `web.visible(view)`, which returns true until Plan 8a:
  - the state with spec 10.2's fixed texts;
  - banners;
  - the stop entry from `View.StopEntry`;
  - the conclusion's key and its statement marked "model-written";
  - the hypotheses with key, score, counts and `StatusOf`;
  - "last updated at T", in `web.timeZone` and in relative words.

**Tests:**
- `TestTheOpeningFormFixesAResponderAsPrincipalAndOffersOpenServices`.
- `TestADoubleSubmittedOpeningOpensOneItem` (I2).
- `TestARetriedOpeningAfterUnavailableKeepsItsKeyAndOpensOneItem` (I2).
- `TestTheBoardPageRendersStatesBannersStopEntryAndStatusWords`.
- `TestModelTextIsMarkedModelWrittenAndEscaped`.
- `TestEveryBoardRouteCallsTheVisibleSeam`.

- [ ] Tests first; implement; gates; commit `feat(web): the opening form and the board page`.

### Task 10: The harness: the TLS stub, the web's deployment, and scenarios 26 to 28

**Decisions:** R-c (the process runner only); R94, R96; M-k, m18.

**Produces** in the harness (m18):
- `tlsproxy.go`: a TLS stub bound before serve starts, at `publicURL`'s port. It preserves `Host`, and switches its upstream between instances as a load balancer would, by their `/leaderz`.
- `deployment.go` gains the `web` block, the client-secret and session-key files under `secrets/` (the key in base64), and `web.oidc.caBundle` for the harness's CA.
- `serve.go` gains `Addresses.Web`, and `env.go` the lifetime of `webtest.IdP`, served with TLS from `ca.go`.
- The web scenarios skip when `env.Runner() == "docker"`.

**Scenarios, under the process runner,** through `webtest.Browser`:
- **26:** a responder signs in, opens an item, sees it, and turns the switch on with a preset reason. Turns stop, and the board shows "Stopped by the kill switch".
- **27:** during a drain, a responder's on through the web is recorded deferred, and the next leader halts the runs. `/leaderz` answers 200 on the draining leader and 503 on the standby, and the stub follows the lease.
- **28:** an admin re-authenticates and turns the switch off, and a viewer's on is refused.

`make test-docker` runs the existing scenarios unchanged.

- [ ] Scenarios first; see them fail; build the stub and the deployment; gates (`make test-linux`); commit `test(harness): the TLS stub, the web's deployment, and scenarios 26 to 28`.

### Task 11: The documents

- **Spec amendments:**
  - Sections 5.1 and 8.3, "Until sign-in" (R-d): "After sign-in exists, the admin API's token stays as the break-glass path on the host: it turns the switch on and off and opens items, recorded as subject `admin`."
  - Section 9.1: fresh authentication is required for the off and for every admin action, which is an act on an item where the person is neither its principal nor a responder, or an opening for another person. The exception is the loopback token (M-d).
  - Section 15: session length [8 h], fresh window [5 min], and the fixed 60 s leeway.
- **Contracts** (m19): C2's addresses file gains `web`; C4's opening route gains its codes (R15).
- **Roadmap:**
  - row 9a split into 9a-1 and 9a-2, with 9a-2's scope as under "Not in this plan";
  - the admin API's sign-in added to row 10a;
  - close and reopen in the interface noted as moved from 9b;
  - the Status line amended.
- **ADR-016:**
  - History entries for R-c, R-e and R-f;
  - the revisit trigger rewritten as below;
  - the question 12 assumption: a deployment `responders` group stands in for "open items for their services" until Plan 7c-2, safe under the Phase 0 data rule.
- **Deployment docs** (the example profile's sign-in section):
  - the proxy terminates TLS, sets HSTS, and **forwards the client's `Host` unchanged** (I11);
  - the load balancer's recipe (`/leaderz`, and `/readyz` for reads);
  - both instances share `web.sessionKeyRef`, which holds base64;
  - rotating it is the emergency sign-out;
  - while the IdP is down, nobody can sign in, and the only stop is the loopback token on the host (M4);
  - subjects take the form `sso:`, with the reassignment risk of an email claim;
  - items with no principal are acted on by an admin (m25);
  - admins need not also be listed in `web.groups.responders`: every admin is a responder (I1; n6).
- **The design, its reviews and the plan review** are copied into `plan/` as tracked files.
- **README** status.

- [ ] Write; commit `docs: Plan 9a-1's amendments, the roadmap's split, and ADR-016's new trigger`.

## The branch's end (the controller's)

- `make test-linux`, `make test-docker` and the 40-minute soak, before the final review on Opus 5.5 and again after its one fix wave.
- Push to origin main.
- Back up and delete the workspace.
- The final message to the author lists every ruling with its cost if wrong, every deferred minor, the calendar days used, and the spend from the ledger.

## Size

| Part | Days |
|---|---|
| Task 1: roles, the opening's actor and checks | 1.5–2 |
| Task 2: the list, `ClosedSince` in both stores, the light fold, the stop entry, the status words | 2.25–2.75 |
| Task 3: the shared routing and refusals, `ItemID` moved, the lease holder | 3–3.5 |
| Task 4: deployment fields, listener, server, headers, health | 2.75–3.25 |
| Task 5: keys, session, CSRF, return paths, `webtest`'s session and browser | 2–2.5 |
| Task 6: OIDC, flows, freshness, `webtest`'s IdP | 2.5–3 |
| Task 7: layout, items, acts, standby, wiring | 3–3.5 |
| Task 8: the kill switch's control | 1.5–2 |
| Task 9: the opening form and the board page | 1.5–2 |
| Task 10: the harness's stub, deployment and scenarios | 2–2.5 |
| Task 11: documents | 0.5 |
| **Total** | **22.5–27.5 days, about 4.5–5.5 person-weeks raw; about 5.9–8.3 executed at 1.3–1.5** |

- **Calendar guess:** about 3–4 days, from the first task to the push, less waiting for the author (m23). The basis is 7b-1, about 3.5–4 person-weeks executed in 2 days, plus the branch's end's two Linux runs, two Docker runs and two soaks.
- **Spend guess:** no basis on record. It is set after Task 1 by R93's calibration and reported to the author.
- **Under R-f,** the plan stops for the author past 8 calendar days, or past twice the calibrated spend.

## The revisit trigger (R-f), proposed for ADR-016

- **Calendar time.** A plan stops for the author when its execution runs past 5 calendar days from its first task, or past twice its plan's calendar guess, whichever is later. Time spent waiting for the author does not count.
- **Cost.** A plan stops for the author when its spend passes twice its guess.
  - Spend is in dollars: subagent tokens from the controller's ledger, priced at the vendor's rates with cache reads at their own rate, plus live runs.
  - A plan with no basis sets its guess after its first task, by that task's dollars times the ratio of raw days.
- **Person-weeks** are still estimated, recorded on the roadmap and reported at each branch's end. They stop no plan.
- **The exit date** of 2027-04-30 is unchanged.

## Not in this plan

- **9a-2, the live board:**
  - the rules header, the incident strip, and the quotes with the quote rule and the bidi carry (Plan 1:4964);
  - suggestions, their summary parts as data, the "outside this item's service" mark and the proposer (5b:186-188, 203-207);
  - the feed merged with the item's records (7b-1:1071);
  - notices shown to people (5b:189). Until 9a-2, a notice is visible only in logs;
  - "confirming", the full board-state table, SSE and the connection indicator;
  - the wide layout and raw outputs.
- **Plan 10a:** the admin API's sign-in, with access tokens of a configured audience; never ID tokens, and never cookies on `/api/`.
- **9b:** rulings after the stop entry (7a:223); messages, the kill switch's included; milestones and time entry; accessibility tested in a browser.
- **8a:** the invisible-arm row, the hiding behind `web.visible`, and spec 5.1 step 4.
- **8b:** the rater role.
- **Phase 1:** approvers turning the switch on.
- **The release rule on model-call records (6b:233):** no 9a-1 surface shows model-call records.
- **Plan 7c-1:** a source actor for inbound openings, which R78's principal rule would refuse as written (m10).

## Constraints carried to later plans

- **Plan 9a-2 (the live board):**
  - everything under "Not in this plan", 9a-2;
  - the act forms' previews have no time bound and no single flight, as the kill switch's preview has (the final review's m2);
  - a retry after a lost answer, sent once the session or its freshness changed, carries a key whose MAC no longer verifies, so a new key can open a second item (m3): bind the form key to something that survives a fresh sign-in, or show the pending act;
  - a subject with surrounding spaces signs in, but `Open` trims the principal, so that person is refused "opens as their own principal": refuse such a subject at sign-in;
  - the standby page compares the lease's expiry, a database time, with the host's clock (m9);
  - the "confirming until T" word, the investigating state's deadline, and "last updated" as the board's latest event, none of which `View` carries yet.
- **Plan 10a:** the admin API's sign-in, with access tokens of a configured audience.
- **Plan 7c-1:** inbound openings pass a source actor, which R78's principal rule refuses as written; it must exempt sources.
- **Plan 7c-2:** service ownership replaces the deployment `responders` group's stand-in (ADR-016, question 12).
- **Plan 8a:** the invisible-arm row and the board hiding behind `pages.visible`.
- **Plan 8b:** the rater role.
- **Any plan that lists every turn the web takes:** the web's request logs carry subjects, which may be emails.
- **Deferred minors** (none affects correctness; the per-task reviews in `plan/backups/plan9a/` and the workspace backup list them in full): test-only tightening (computed expectations in Task 1's table test, an untested fold-corrupt branch, guard-only tests, a 1 s test delay), duplicated `Retry-After` code, `Result.Deferred`'s doc, a loose lease test assertion, the `/kill-switch` uncounted words' wording, `discover()` logging with the first caller's context, and a refused invalid opening losing typed values when no revision is current.

## Changes made during execution

Executed on 2026-11-08, subagent-driven on main, b6b7f36..HEAD: each task by an implementer, a task review on Opus 5.5 (Sonnet for the harness), scoped re-reviews of each fix round, then the whole-branch review on Opus 5.5 (0 Critical, 1 Important, 12 Minor), one fix wave and its scoped re-review. Every controller ruling is in the workspace ledger, backed up to `plan/backups/`; the ones that changed this plan's text:

- **R81 amended:** `switching.Act` and `switching.Opening` take the request's context first; `Refusal` gains `Err`, the cause the admin API needs to keep its texts. The admin opening route answers `code` beside `error` (7b-1's R15; Contract C4 amended).
- **R76 refined:** `Authenticator.Acting(r)` builds the actor with the authenticator's clock.
- **R80 amended:** the subject cap is 252 runes at sign-in, so that with `sso:` it fits `Open`'s 256 (`app.MaxNamedRunes`, exported); duplicate responders are refused, through the admin API too; a principal refused because the always rules would change the actor's own subject gets its own reason (`ErrOwnSubjectRedacted`, under `ErrInvalid`), and the web says the deployment's subject claim is redacted.
- **R86 amended:** the key is read outside the keyring's lock, one read at a time on a detached 5 s context; a panicking store fails closed and its value is never logged.
- **R95 amended:** the Host refusal is logged at most once a minute.
- **R93 amended:** the harness reports only a subagent's total tokens, so the ledger prices each total at the input rate, an estimate rather than a bound.
- **Added:** `web.oidc.extraScopes` (IdPs that send groups only on request); a fourth HKDF subkey for flow cookies; client_secret_basic set explicitly; ASCII-only host and origin normalization in `internal/web/origin` (the Kelvin sign U+212A folded to `k`); `Row.Unreadable`, `Row.AutoClosePaused` and `Row.AutoCloseUnread`; at most one kill switch preview in flight, bounded at 1.5 s; the on offered at a preview's row when the table cannot be read; separate words for a standby's refusal and for a `not_held` answer after a send; `/leaderz` holder reads on the main pool with a 1 s bound; items rows link to their boards; `build/test/run.sh` runs the web scenarios under the process runner; the e2e depguard rule allows `internal/web/webtest` alone, in strict list mode.
- **Spend (R-f):** about $20 at the input rate for every subagent token, against the calibrated $13–16 and the stop line of about $30. The overrun is in fix rounds: most tasks took one, Tasks 5 and 8 two counting the controller's follow-ups.
- **Calendar:** one day, 2026-11-08, against a guess of 3–4.
