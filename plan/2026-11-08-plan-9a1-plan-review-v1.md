> The Opus review of Plan 9a-1's plan, version 1.

# Plan 9a-1, implementation plan v1: review

Reviewer: Opus 5.5, 2026-11-08. Subject: `plan/2026-11-08-plan-9a1-sign-in-and-stop.md` (448 lines), against `design-v2.md`, `design-review-v1.md`, `design-rereview-v2.md`, the spec and the code at `a410bbb`.
Binding and not relitigated: R-a to R-f.

**Counts: 0 Critical, 12 Important, 25 Minor.**

**Verdict.** The plan does what it claims for most of the re-review: N2, N3, N4 and N5 are closed by named decisions and named tests that would fail first, and the I13 and M4 lines are all in "Not in this plan" and Task 8. The problems are in the seams between tasks, not in the decisions:
- Five things Task 6 or Task 7 consume have no producer: the web opening's id and digest, the item acts' refusal mapping, an importable fake IdP and session helper, serve's wiring of the web's ports, and the board page's data.
- One decision rests on config that does not exist (R91).
- One combination of R76 and R79 takes the on away from an admin who is not fresh, against spec 10.1.
- The test for the N1 class can pass while the bug is present.
- R-f's spend number cannot be measured as written.

None of these needs a new ruling from the author. Each has a fix of a sentence to a quarter of a day. With the fixes, the honest size rises by about 1.75 to 2.25 raw days (Section 6).

---

## 1. Is each re-review finding closed?

| Finding | Decision | Test | Verdict |
|---|---|---|---|
| I3 (opening, the on, freshness as a copy) | R76, R77, R78, R79 | `TestMayDecidesEveryActForEveryRole`, `TestAResponderOpensOnlyAsTheirOwnPrincipal`, `TestAViewersOnDuringADrainIsRefusedAndWritesNoRow`, `TestTheActorIsAdminOnlyInsideFreshness` | **Closed**, but see I1 below: R76 together with R79 refuses the on to an admin who is not fresh |
| I6 (StayedUnstaffed, unread) | Task 6's `/kill-switch` | `TestTheOffsAnswerListsStayedUnstaffedWithStatesAndTheUnreadFlag` | Closed |
| I9 (subjects, principals) | R80 | `TestSubjectsAreRecordedAsSSOAndCheckedForUnseenCharacters`, `...BareAdmin` | Closed. M-e's positive case is untested (m20) |
| I13 (carries) | "Not in this plan", lines 426-440 | n/a | Closed. Line 439 says "approvers turning the switch on". That is right, and the re-review's "approvers for the off" was wrong: spec line 690 says "Responders and approvers may turn it on" |
| N1 (`Origin: null`) | R87 | `TestAServedFormsPostCarriesItsOriginAndIsAccepted` | **Decision closed; test weak** (I8) |
| N2 (opening's actor) | R78 | `TestOpenRecordsWhoOpened`, `TestAResponderOpensOnlyAsTheirOwnPrincipal` | Closed in the coordinator. The web opening's id and digest have no producer (I2) |
| N3 (second policy copy) | R76, R77 | `TestAnAdminsItemActNeedsAdmin` (Task 1), `TestAnAdminsItemActNeedsFreshness` (Task 6) | Closed |
| N4 (on check in `checkSwitch`) | R79 | `TestAViewersOnIsRefusedResponderOnly`, `TestAViewersOnDuringADrainIsRefusedAndWritesNoRow` | Closed. `RecordKillSwitchOn` calls `checkSwitch` first (`killswitch.go:281-282`), so R79's placement covers the drain |
| N5 (Host check and probes) | R88 | `TestHealthRoutesSkipTheHostCheck` | Closed. They should also skip R86's 503 (m16) |
| N6 (R-f has nothing to measure) | Size, lines 415-417 | n/a | Numbers given; spend not measurable as written (I10) |
| M-a | R83 | none needed | Closed |
| M-b | R84 | `TestACloseWhosePreviewCannotAnswerIsStillOffered` | Closed |
| M-c | R85 | `TestAKeyWithABadMACIsRefused`, `TestARetryAfterUnavailableKeepsItsKey` | Closed for acts. For openings, see I2 |
| M-d | Task 8 (9.1 amendment) | doc | Closed |
| M-e | R80 | none positive | **Partly**: nothing asserts that an email subject signs in under an always rule that redacts emails (m20) |
| M-f | R80 (docs), BareAdmin | `...BareAdmin` | Closed |
| M-g | R90 | `TestLeaseHolderNamesTheHolderAndItsExpiry`, `TestAStandbyDisablesTheControlAndNamesTheLeader` | Closed. The web has no port to reach it (I6) |
| M-h | Task 1, 4, 5, 6 tests | listed | Closed |
| M-i | R89 | `TestNoGroupsOrAnOverageLandsOnNoRole` | Closed |
| M-j | R86 | `TestAKeyThatCannotBeReadFailsClosed` | Closed. The once-a-minute log is untested (m20) |
| M-k | Task 7 `tlsproxy.go` | scenarios 26-28 | **Partly**: serve cannot trust the fake IdP's TLS (I5) |
| M-l | R91 | `TestTheOpeningFormOffersTheRevisionsServices...` | **Not closed**: the config R91 reads does not exist (I9) |
| M-m | R92 | `TestClosedSinceUsesTheDatabasesTime` | Closed |
| M-n | "Not in this plan", Task 8 | doc | Closed |
| M-o | R82 | `TestAStaleOrUnavailableSwitchAnswerRereadsTheTable` | **Partly**: the test's name covers stale and unavailable, but not `not_held`, `draining` or `taking_over` (m12) |

---

## 2. Important

### I1. An admin who is not fresh, and not in the responders group, cannot turn the kill switch on or open an item

- **Evidence.**
  - R76 (plan:74) builds `Actor{Responder: r, Admin: admin && fresh(auth_time)}`, and nothing defines `r`. Task 4's table (plan:230) requires the three groups to be "pairwise distinct".
  - R79 (plan:82) refuses `on && !Responder && !Admin`. R78 (plan:76) has `Open` call `May(actor, ActOpen, nil)`, which by design D5 needs `Responder || Admin`.
  - So an admin in `admins` alone, outside the window, gets `responder_only` on the on, and is refused an opening.
  - Spec 10.1 (spec:773 ff.) gives the admin "Use the kill switch, including turning it off". Spec 8.3 (spec:690) says the on "needs no fresh authentication, so stopping stays fast". `TestTheOnOffersPresetReasonsAndNeedsNoFreshness` (plan:333) would pass for a responder and never try an admin.
- **Fix.**
  - R76: define the roles as nested, `Responder = inGroup(responders) || inGroup(admins)` and viewer below both, so that an admin is always a responder and `Admin` alone carries freshness.
  - Add `TestAnAdminOutsideFreshnessTurnsTheSwitchOnAndOpensForThemselves` to Task 5 (the actor) and to Task 6 (the page offers it).
  - State in Task 8's deployment docs that admins need not also be listed in `responders`.

### I2. The web opening's id and digest have no producer, so a double-submitted opening form can open two items

- **Evidence.**
  - An opening is idempotent only through `OpenInput.ID` and `Digest`: `Open` calls `c.repeated` when `in.ID != ""` (`coordinator.go:279-287`).
  - The admin route derives both: `ItemID(a.o.Tenant, key)` (`admin.go:282`, defined `admin.go:685-692`) and `digest(in)` (`admin.go:363-375`), which is spec 5.1's canonical digest (spec:493).
  - Global Constraints (plan:48) let `internal/web` import only `internal/work/app` and `internal/admin/switching`, not `internal/admin`.
  - R85 (plan:100) mints `k` for openings, but no task turns `k` into an id or a digest, or brings the tenant to the web. With `ID` empty, `Open` mints a fresh id (`coordinator.go:304-306`), so a double submit or an R85 retry after `unavailable` opens two items.
  - `TestADoubleSubmitChangesNothing` (plan:327) covers acts only.
- **Fix.**
  - Task 3 moves `ItemID` and the digest into `app`, as `app.OpeningID(tenant, key)` and `app.OpeningDigest(type, principal, responders, routing)`; the admin route calls them, and `admin_test.go:544` is the guard.
  - Task 6 passes the tenant to the web, and its opening uses `OpeningID(tenant, "web:"+k)`. Add `TestADoubleSubmittedOpeningOpensOneItem` and `TestARetriedOpeningAfterUnavailableOpensOneItem`.

### I3. The item acts' refusal mapping is not exported, so Task 6 has nothing to call for promote, close, reopen and opening refusals

- **Evidence.**
  - Task 3 produces only `Set`, `Result`, `Refusal`, `Ports` and `Words` (plan:200-204), and `Set` is the switch's alone (R81, plan:92).
  - An item act's error becomes a status and code through `lifecycleRefused`, then `standing` and `unheld` for not-held errors, then `refusal()` (`lifecycle.go:300-358`; `standing` at `admin.go:434-442`).
  - The opening's not-held answer is `notHeld` (`admin.go:415-421`), and its other refusals are `refused` (`admin.go:381-405`).
  - Task 6 needs `draining`, `taking_over` (Retry-After), `not_held`, `key_reused`, `held_by_later_build`, `no_such_item`, `not_item_responder` and so on, for item acts and openings. `TestSetMapsEveryRefusalCode` lists `key_reused` (plan:209), a code `Set` can never produce.
- **Fix.** Task 3 also produces `func Act(err error, leader Leader) Refusal` and `func Opening(err error, leader Leader) Refusal`, and the admin's `act` and `openItem` call them. Rename the test to `TestEveryLifecycleErrorMapsToAStatusCodeAndWords`, run as a table over `Set`, `Act` and `Opening`. `lifecycle_test.go` and `admin_test.go` stay the guards.

### I4. The fake IdP and the signed-in-session helper live where Task 6 and Task 7 cannot import them

- **Evidence.**
  - Task 5 (plan:272) builds the fake provider inside its tests, "that Task 7 reuses". Task 6 (plan:323) uses "a signed-in session from Task 5's helpers".
  - `_test.go` code in `internal/web/auth` cannot be imported by `internal/web/pages` or by `test/e2e/harness`.
  - Task 7 (plan:354) says `fakeidp.go` is "Task 5's provider", then (plan:365) says "build the fakes". It is unclear which task writes it.
- **Fix.**
  - Task 5 produces an importable, non-test package, `internal/web/webtest`, holding `NewIdP(t) *IdP` (httptest TLS, an RSA JWKS, settable claims, `auth_time`, a `prompt=login` switch) and `SignedIn(t, srv, Session) *http.Client`.
  - Task 7's `fakeidp.go` serves that IdP on a port. Change Task 7's step to "wire the IdP; build the TLS stub".
  - Name the package in File Structure.

### I5. Serve cannot trust the fake IdP's certificate, nor a corporate IdP behind a private CA

- **Evidence.**
  - Task 4 requires `web.oidc.issuer` to be https (plan:225) and has no CA field.
  - Task 7 serves the IdP "with TLS from the harness's test CA" (plan:354). Serve's discovery, JWKS and token calls would refuse it.
  - The harness already solves this for MCP servers through `mcp.caBundle` (`deploy.go:190-195`; `harness/deployment.go:453`; `harness/ca.go:23-26`), and serve loads such a bundle at `serve.go:616`.
- **Fix.** Add `web.oidc.caBundle` to Task 4's table, as a PEM path trusted besides the system roots and checked as `mcp.caBundle` is. Task 5's OIDC client uses it through `oidc.ClientContext`, and Task 7's deployment writer sets it. Test: `TestDeployRefusesEachBadWebField` gains an unreadable bundle.

### I6. No task wires the web's ports into serve, and the lease holder has no port

- **Evidence.**
  - Task 4 adds the listener and the health routes (plan:244). Task 5 and Task 6 add handlers. File Structure (plan:128) lists `serve.go` under Task 4 only.
  - Nobody builds `web.New(Ports{Coordinator, Views, Switching, Leader, Holder, Revisions, Secrets, Schema, Tenant, Clock})` in `server.wire` (`serve.go:477-560`, where `admin.New` is built at `:548`). Scenarios 26-28 cannot run without it.
  - R90's `postgres.Lease.Holder` (plan:105) cannot be called from `internal/web`, which may not import a store (plan:48).
- **Fix.** Task 6 owns the wiring. Add `cmd/agentswarm/serve.go` to its files, define `web.Ports` with a `Holder interface{ Holder(ctx) (string, time.Time, error) }`, and add `TestServeWiresTheWebWithTheLeaseHolderAndTheViews` (cmd) to Task 6. About 0.5 day.

### I7. The board page consumes data that no task produces

- **Evidence.**
  - Task 2 produces `board.Status(s *State, h EntryID)` (plan:183). The web never holds a `*board.State`: `Views.Item` replays the board inside `app` and gives only `RoundView.Report` (`views.go:148-196`), whose `Report.Assessment` holds `HypothesisView`s (`report.go:24-29`; `assess.go:13-38`).
  - The stop entry (plan:306) is `State.Stopped()` (`report.go:60-66`). `View` carries only the stop reason (`views.go:24-27`) and `Report.Stopped` as a bool.
  - The status words are not defined against the fields that exist (`Rejected`, `Converged`, `Score`, `StandingRefutations`). Nothing says what makes a hypothesis "refuted" rather than "trailing".
- **Fix.**
  - Task 2 produces `func board.StatusOf(a *Assessment, i int) StatusWord`, defined in a table: converged means `Converged`; rejected means `Rejected`; refuted means `len(StandingRefutations) > 0 && !Rejected`; leading means rank 0 and not converged; trailing is the rest.
  - Task 2 also produces `View.StopEntry *board.Stop` (or `Views.Board(ctx, id)`), filled from the same reading of the board.
  - `TestStatusWordsForEachHypothesisState` becomes that table.

### I8. The N1 test can pass while N1 is present, and nothing stops a POST from being answered with a redirect that leads to the IdP

- **Evidence.**
  - `TestAServedFormsPostCarriesItsOriginAndIsAccepted` (plan:292) and Task 7's client, "sending `Origin` as a browser does" (plan:356), both have the test choose the `Origin` it sends. A regression that puts `no-referrer` back on pages, through the header, a `<meta name=referrer>` or a form's `referrerpolicy`, would still pass. That is exactly the class N1 found.
  - D3 (design-v2:73) forbids a redirect to the IdP after a POST, because Chrome applies `form-action` to the redirects that follow a form POST. No test enforces it. The likeliest case is a POST with an expired session: if the server answers 303 to `/auth/signin`, which answers 303 to the IdP, Chrome blocks the navigation under CSP.
  - A responder who leaves `/kill-switch` open past the session's [8 h] and taps "on" in an incident gets a blocked page. `TestAnExpiredSessionSendsToSignIn` (plan:276) does not say which method it uses.
- **Fix.**
  - Put the Fetch rule in the test client, as `webtest.BrowserOrigin(resp *http.Response, page []byte, target *url.URL) string`. It sends `null` when the effective policy of the page that served the form is `no-referrer`, reading the header, then `<meta name=referrer>`, then the form's `referrerpolicy`. Both the Task 5 test and the Task 7 client use it.
  - Add `TestNoServedPageSetsANoReferrerPolicyBesidesTheCallback`.
  - Add `TestAPOSTIsNeverAnsweredWithARedirectOffOrigin`: an expired session, a missing session or a lost freshness on every POST route answers 200 or 4xx with a sign-in link, never a 3xx to `/auth/*` or to the IdP.

### I9. R91 reads a service list that the configuration does not have

- **Evidence.**
  - R91 (plan:106) says the opening form's services are "the set of `service` routing values that the current revision's work-item types accept".
  - `config.WorkItemTypeSpec` (`internal/registry/config/kinds.go:193-204`) holds the hypothesis key, budget, round, team, limits, convergence, milestones, auto-close, measurement and shadow runs. No type enumerates routing values, and routing is checked only for form and redaction (`coordinator.go:1321-1342`).
  - So the list is always empty, the form always shows the fallback text field, and `TestTheOpeningFormOffersTheRevisionsServices...` (plan:341) cannot be written as named.
- **Fix.** Either:
  - **(a)** State R91 as "a text field checked as routing (`checkRouting`'s rules), with the values of the items open now offered as suggestions", and rename the test; or
  - **(b)** name a concrete source, for example the inline `List` of a hypothesis key field named `service` (`kinds.go:206-215`), with the fallback.

  (a) costs nothing. (b) ties the form to a naming convention that the spec does not define.

### I10. R-f's spend trigger cannot be measured as written, and its guess has no basis

- **Evidence.**
  - The proposed trigger (plan:422) adds "live runs plus subagent tokens" and compares the sum with a guess in tokens (plan:416). Live runs are priced in dollars, and tokens are not dollars.
  - "As the harness reports them" names no harness. The e2e harness reports no tokens, and nothing records each subagent's usage into a ledger.
  - Whether cache reads count is unstated. In a long subagent session they are most of the tokens, a ratio of 5 to 20.
  - The basis line (plan:415) gives 7b-1 and 7b-2 for the calendar only. The roadmap and ADR-016 record no token figure for any plan, so 10-15M has no basis.
- **Fix.**
  - Define spend in dollars: subagent usage priced at the model's rates, cache reads at their own rate, plus live runs.
  - Name a ledger file under `plan/backups/plan9a/` that the controller appends to after every subagent: the task, the role, the input, output and cache-read tokens, and the dollars.
  - Add a calibration point. After Task 1 and its review, re-base the guess from that task's measured cost times the size ratio, and report it to the author.
  - For the calendar, say whether time spent waiting for the author counts: suggest that it does not, from the first task's start to the push.

### I11. The deployment docs omit the one proxy setting that breaks every request

- **Evidence.**
  - R88 and Task 4's Host check (plan:242, 251) refuse a `Host` that is not `publicURL`'s host.
  - Common proxies rewrite `Host` to the upstream by default; nginx's `proxy_pass` sends `$proxy_host`. Every request would arrive as `127.0.0.1:<port>` and get 400.
  - Task 8's docs (plan:382-388) list TLS, HSTS, the load balancer's recipe, the key and the IdP outage, but not this. The harness's own stub will be written to preserve `Host`, so no test can catch it.
- **Fix.**
  - Task 8 adds "the proxy must forward the client's `Host` unchanged (nginx: `proxy_set_header Host $host`)".
  - Make the 400's body say "Host does not match web.publicURL", and have serve log the first such refusal at Warn, with the received host, which is not a secret.
  - Add `TestAForeignHostsRefusalNamesTheSettingAndIsLoggedOnce` to Task 4.

### I12. Task 5 and Task 6 are each too large for one implementer and one review

- **Evidence.**
  - Task 5 (plan:259-296) is 4-4.5 raw days with 19 tests. It spans key derivation, sealing, two cookie kinds, CSRF, return paths, OIDC discovery and token checks, freshness, and a reusable fake IdP (I4).
  - Task 6 (plan:298-347) is 4.5-5 days with 22 tests, about 9 pages, the stylesheet, and now the serve wiring (I6).
  - 7b-1's record (plan:415) is tasks of about 0.5-1.5 days. Its overrun was in the two largest tasks (ADR-016, 2026-11-03).
- **Fix.** Split into six tasks of about 1.5-2 days each:
  - **5a:** HKDF keys, the sealed session and its cookie, CSRF, `SafeReturn`, `Actor()`, and the `webtest` session helper.
  - **5b:** OIDC: discovery, flow cookies, the callback, the refused-token table, freshness, and the `webtest` IdP.
  - **6a:** the layout, header and stylesheet, `/items`, the item acts with MAC'd keys, the refusal pages, the standby, and the serve wiring.
  - **6b:** `/kill-switch`: on, off, the answers, R82, and the drain.
  - **6c:** the opening form (with I2's id), the board page, and the `visible` seam.

  Each gets its own review. Calendar is unchanged, since the work is sequential anyway.

---

## 3. Minor

- **m1. "R12's rule" points at the wrong R12** (plan:24, 77, 164). The body-version rule is Plan 7d-1's R12 (7d-1 plan:530; `item.go:166-172`). 7b-1's R12 is about reopens (7b-1 plan:464). The substance holds: `Body.UnmarshalJSON` decodes non-strictly (`item.go:99-119`), so older builds drop `by`. Cite "Plan 7d-1's R12", and say that the 7b-1 carry about new record *kinds* (7b-1 plan:1094) does not apply to an optional field.
- **m2. R81 misplaces what moves** (plan:96).
  - `standing` is in `admin.go:434`, not `lifecycle.go`, and the opening's `notHeld` shares it (`admin.go:417`).
  - `refusal()` needs `invalid()` (`admin.go:447`), `heldByLaterBuild`, `refusedTexts` and the codes.
  - `unheld` and `lifecycleRefused` also log. `Set`'s signature has no logger, so the logger goes in `Ports` or as an argument.
- **m3. `switching.Ports`** (plan:202) lists `Items`, which `Set` never reads. Name the coordinator's subset (`SetKillSwitch`, `RecordKillSwitchOn`) and `Leader.Status`.
- **m4. `Words(code)` can only return the admin's texts.** Those carry API instructions, "Idempotency-Key" and "the seq read as after" (`lifecycle.go:44-55`, `:349-358`), and depend on `q.what`. Give the web its own text table keyed by code, and test that every code has a web text.
- **m5. `Row.QueuePosition = -1` needs a held signal** (plan:181, 190). `Views` has none: `Lifecycle` has no held flag (`queue.go:17-34`). A standby and a leader's promoted-but-unstarted item both give 0 (`views.go:201`). Add `ViewPorts.Held func() bool`.
- **m6. `ClosedSince` on `app.Records`** breaks `recordsPort` (`interleave_world_test.go:1149`) and `failing` (`harness_test.go:675`). Either list them in Task 2, or put `ClosedSince` on a narrow interface that only `Views` takes. Run the interleaving default in Task 2, which touches `app`, rather than in Task 3, which does not (plan:55, 213).
- **m7. The header's size disagrees with the table.** Line 12 gives 20-23.5 raw days, 4-4.7 person-weeks and 5.2-7 executed. The table (plan:413) sums to 20.5-24, 4.1-4.8 and 5.3-7.2.
- **m8. A test name differs.** Review Focus 4 (plan:68) names `TestAnAdminsItemActNeedsFreshness` for Task 1, but Task 1's test is `TestAnAdminsItemActNeedsAdmin` (plan:162).
- **m9. R80's wording is wrong at opening time.** It says "the revision the item locks" (plan:88), but an opening locks none. Use the current revision, as `checkRouting(rev, ...)` does (`coordinator.go:294-300`).
- **m10. Where `May` sits in `Open`.** Put it before `c.repeated` (`coordinator.go:279-287`), so that an actor with no role cannot learn whether a key opened an item. Carry to 7c-1 that R78's "principal equals subject" rule would refuse a source actor.
- **m11. Item-act previews.** Add the preview case to `TestAStrangerResponderCannotPromoteCloseOrReopen` (re-review Q2(d)). The plan's placement after `target()` covers it (`close.go:72`, before the `a.Preview` branch at `:91`), but no test pins it.
- **m12. R82's test** should cover all five codes as a table: `stale`, `unavailable`, `taking_over`, `not_held` and `draining`.
- **m13. Two Task 4 tests.** "`no-store` on authenticated pages" (plan:248) has no authenticated page until Task 6: use a stub handler marked authenticated, or move the assertion. `TestASlowHeaderIsCutAtTenSeconds` should take the timeout as an option, not sleep for 10 s.
- **m14. Use the standard library.**
  - `crypto/hkdf` has been in the standard library since Go 1.24, so `x/crypto/hkdf` is not needed (plan:24).
  - Consider `net/http.CrossOriginProtection` (Go 1.25+) for the Origin and `Sec-Fetch-Site` half of CSRF, keeping the HMAC token.
  - If `webtest` signs tokens with go-jose, it becomes a direct requirement.
- **m15. Compare origins and hosts normalized.** Strip a default port, compare case-insensitively, and drop a trailing dot. Add those cases and an IPv6 host to `TestAForeignHostIsRefused` and `TestCSRFRefuses...`.
- **m16. Health routes and the key.** R86's "every page answers 503" must not cover `/healthz`, `/readyz` or `/leaderz` (R88). Say so and test it.
- **m17. Status words.** See I7 for their definitions. Without them, `TestStatusWordsForEachHypothesisState` is the implementer's opinion.
- **m18. Task 7's file list is short.**
  - It also changes `harness/deployment.go` (the `web` block, the client-secret and session-key files under `secrets/`, `web.oidc.caBundle`), `harness/serve.go` (`Addresses.Web`, `serve.go:28-33`) and `env.go` (the IdP's lifetime).
  - Web scenarios skip when `env.Runner() == "docker"` (`env.go:197`).
  - The stub must be bound before serve starts, since `publicURL` sits in the deployment file and serve's port is known only from the addresses file. It must also preserve `Host`.
  - Decide whether one stub switches upstream, as a load balancer does (more faithful for scenario 27), or each instance gets its own.
- **m19. Contract amendments.** Task 8 also amends Contract C2 (the addresses file gains `web`) and C4 (the opening route gains codes, R15; 7b-1 plan:467).
- **m20. Untested promises.** M-e: an email-like subject signs in under an always rule that redacts emails. M-j: a key that cannot be read is logged at most once a minute.
- **m21. Flow cookies.** Name each flow cookie by a hash of `state`, delete it at the callback, and cap the number of live flow cookies (for example 5), so that many tabs cannot push the `Cookie` header toward `MaxHeaderBytes`.
- **m22. The session key's encoding.** Say what the secret at `web.sessionKeyRef` holds, raw bytes or base64, and whether a trailing newline is trimmed. The harness writes `value+"\n"` (`harness/deployment.go:296`), so ">= 32 bytes" is ambiguous.
- **m23. The calendar guess is optimistic.** At 7b-1's rate (about 3.5-4 person-weeks executed in 2 days), 5.3-7.2 executed weeks is about 2.7-4 days, before two `test-linux`, two `test-docker` and two 40-minute soaks. A guess of 3-4 days, with the trigger at 8, is more honest than 2-3 with the trigger at 6. If 2-3 is kept, say it is the optimistic end.
- **m24. "Under the lock" is loose for the drain.** `RecordKillSwitchOn` runs `checkSwitch` without `c.mu` (`killswitch.go:281-283`). That is harmless, since the check is pure, but R-e's "under its lock" should read "in the coordinator" for this route.
- **m25. Items with no principal.** Every item opened through the admin API today has no principal. On the web only a fresh admin can act on such an item. Say so in the docs and in `/items` ("no principal: an admin acts").

---

## 4. Statements about existing code, checked

| Statement | Verdict |
|---|---|
| `killswitch_test.go:1002`, a non-admin `alice`, is the only non-admin actor | **True.** It is the only `Actor{...}` without `Admin: true` in the tree |
| `acts.go:89-97` holds the `Refused` codes | True |
| `checkSwitch` covers `SetKillSwitch`, its preview and `RecordKillSwitchOn` | True (`killswitch.go:162`, `:282`, `:428-449`) |
| `leases.holder` exists to read | True (`0008_leases.sql:16`) |
| `Views.KillSwitch` reads the table | True (`views.go:138-140`) |
| `golang.org/x/oauth2` is indirect, and `x/crypto` is indirect | True (go.mod: `v0.35.0`, `v0.55.0`). `crypto/hkdf` is in the standard library anyway (m14) |
| `standing` and `refusal()` live in `lifecycle.go` | **Partly false** (m2) |
| Work-item types enumerate service routing values | **False** (I9) |
| Scenarios 26-28 | Consistent. The repository has 1-23, and 24-25 are reserved for 7c-1 (7b-2 plan:31, 247). Say so in Task 7 |
| `lifecycle_test.go` passes unchanged after the move | Plausible, if the HTTP writers stay in `admin` and only the decision moves. `admin_test.go` reads `body["error"]` only (`admin_test.go:358`), so adding `code` to the opening's answers (R15) does not break it |
| "R12's rule" | Wrong R12 cited (m1) |

---

## 5. The riskiest behaviours: are the right tests there?

- **The on during a drain.** Right: the coordinator (Task 1), `Set` (Task 3), the web (Task 6) and scenario 27. Add the viewer's on that arrives through `recordOn` to Task 3's table.
- **Freshness.** Right on the callback side: the missing time, the old time, another subject, and roles re-read. Missing: the admin who is not fresh keeps the on (I1).
- **Policy under the lock.** Right: `May`'s table, and the coordinator refusing with no record. Add previews (m11).
- **Open redirects.** Good list. Add `/auth/callback` and `/auth/signout` as return targets that are refused, since "GET page only" (design-v2:116) has no route allowlist in `SafeReturn(path) (string, bool)`, and a tab character.
- **CSRF with real browser Origin semantics.** Weak (I8).
- **The Host check.** Present, but the rule's operational failure is untested and undocumented (I11, m15).

---

## 6. Size, calendar and spend

| Change from this review | Raw days |
|---|---|
| I2: opening id and digest moved, two tests | 0.25 |
| I3: `Act` and `Opening` mappings exported | 0.25 |
| I4: `webtest` as a package | 0.25 |
| I5: `web.oidc.caBundle` | 0.25 |
| I6: serve wiring and the holder port | 0.5 |
| I7: `StatusOf` and `StopEntry` in the view | 0.25-0.5 |
| I8: Fetch-rule client and the no-redirect test | 0.25 |
| **Added** | **about 1.75-2.25** |

- **Size.** About 22.25-26.25 raw days, about 4.5-5.25 person-weeks raw, and about 5.8-7.9 executed at 1.3-1.5.
- **Calendar.** About 3-4 days (m23).
- **Spend.** Re-base after Task 1 in dollars (I10). Until then, the plan should say "no basis on record" next to 10-15M.

Under R-f these move the stop line, not the scope. The author should see the new figures with the plan's approval.
