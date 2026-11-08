> The scoped Opus re-review of Plan 9a-1's plan, version 2.

# Plan 9a-1, implementation plan v2: scoped re-review

Reviewer: Opus 5.5, 2026-11-08. Subject: `plan/2026-11-08-plan-9a1-sign-in-and-stop.md` (version 2, 571 lines), against `plan-review-v1.md` and the code at `a410bbb`, with Go 1.26.0's standard library checked locally. R-a to R-f are binding.

**Counts.**
- Important I1-I12: 8 Closed, 4 Partly, 0 Open.
- Minor m1-m25: 23 Closed, 1 Partly, 1 Open.
- New issues: 0 Critical, 1 Important, 12 Minor.

## 1. The v1 findings

| Finding | Verdict | What is missing |
|---|---|---|
| I1 | Closed | R76 makes every admin a responder (`Responder: responder \|\| admin`), with tests in Tasks 5 and 8. Item 3 of the fix, a docs line saying admins need not also be listed in `responders`, is not in Task 10 (n6) |
| I2 | **Partly** | `ItemID` and `OpeningDigest` move to `app` (Task 3), and Task 9 adds `TestADoubleSubmittedOpeningOpensOneItem`. Two gaps remain: (a) `web.Ports` (Task 7) has no `Tenant`, so `app.ItemID(tenant, k)` has no tenant to read in the web; (b) the retry case, `TestARetriedOpeningAfterUnavailableOpensOneItem`, is missing. Task 7's `TestARetryAfterUnavailableKeepsItsKey` covers acts only |
| I3 | **Partly** | R81 declares `Act(err error) *Refusal` and `Opening(err error) *Refusal` with no leader status. The not-held mapping runs through `standing(held, draining, err)` (`admin.go:434-442`), which needs `Leader.Status()` (`leader.go:188`). Without it, neither function can return `draining`, `taking_over` (with Retry-After) or `not_held`. The `item.ErrInvalid` and default branches also log (`admin.go:395-404`), and these functions have no logger. Fix: take the leader status and the logger as arguments (or make the functions methods on `Ports`); `TestSetActAndOpeningMapEveryRefusalCode` then pins the three not-held codes for `Act` and `Opening` |
| I4 | Closed | `internal/web/webtest` is importable: `SignedIn`, `BrowserOrigin` and `Browser` (Task 5), and `IdP` (Task 6). See n4 for `SignedIn`'s signature |
| I5 | Closed | R94, Task 4's field and test, Task 6's client, Task 9's harness |
| I6 | **Partly** | Task 7 owns the wiring and `web.Ports`, but `Ports{Coordinator, Views, Switching, LeaseHolder, Leader, Revisions, Log}` leaves out three things: (a) the secret store, which Task 5's keys and Task 6's client secret read "at use"; (b) `Tenant` (I2); (c) a clock. No task says who builds the `Authenticator` (OIDC settings, `caBundle`, secrets) in serve. `LeaseHolder`'s interface type is not written out |
| I7 | Closed | `board.StatusOf` table, `View.StopEntry *board.Stop` (the type exists, `board.go:243`), `ViewPorts.Held` |
| I8 | Closed as tests | `BrowserOrigin` in the client, the no-`no-referrer` test and the no-redirect test. But see **NI1**: the `CrossOriginProtection` layer that Task 5 adds changes what these tests exercise |
| I9 | Closed | R91 option (a); test renamed |
| I10 | Closed | R93's ledger, dollars, calibration and calendar rule. See n10 on what the completion notice reports |
| I11 | Closed | R95, Task 4's test, Task 10's docs line |
| I12 | **Partly** | Tasks 5 and 6 are split, and so is Task 7. Line 172 says every task is about 1.5-3 raw days, but the table has Task 3 at 3-3.5, Task 7 at 3-3.5 and Task 9 at 3.5-4. Task 9 again merges the pages (the old 6c) with the whole harness (the old Task 7). Fix: split Task 9 into the pages and the harness with its scenarios |
| m1-m6, m8-m13, m15-m25 | Closed | — |
| m7 | **Open** | The header (line 12) says 22.5-26.5 days, 4.5-5.3 person-weeks and 5.9-8 executed. The table sums to 22.5-27 days, 4.5-5.4 person-weeks and 5.85-8.1 executed |
| m14 | **Partly** | `crypto/hkdf` is right: it is in the standard library, with `Key`, `Extract` and `Expand`. `CrossOriginProtection` exists, but it does not fit as described (NI1) |

## 2. New Important

### NI1. `http.CrossOriginProtection` is not "the Origin half" of the design's CSRF check

- **Evidence: what Go 1.26 does** (`net/http/csrf.go:134-170`).
  - `Sec-Fetch-Site: same-origin` or `none` is accepted whatever `Origin` says, including `null`.
  - A request with neither `Sec-Fetch-Site` nor `Origin` is accepted ("assumed to be either same-origin or non-browser requests").
  - Otherwise it accepts when `url.Parse(origin).Host == req.Host`: an exact comparison with the request's Host, not normalized and not against `publicURL`.
- **Evidence: what the design and plan require.**
  - design-v2:119 requires `Origin` equal to `publicURL`'s origin, and refuses a POST with no `Origin`.
  - Task 5's `TestCSRFRefusesAMissingOrForeignOriginOrToken` asserts that a missing Origin is refused, with normalized origins (R95, m15).
- **Consequences.**
  - Used as Task 5 says ("`CrossOriginProtection` for the `Origin` and `Sec-Fetch-Site` half, then the HMAC form token"), a POST with no Origin and a valid token passes. Either the test fails, or the implementer adds an Origin check the plan never names.
  - R95's normalization is bypassed. A Host that the Host check accepts after normalization, such as one with a trailing dot or a different case, then fails `o.Host == req.Host`.
  - The N1 tests also stop matching real browsers. Chrome sends `Sec-Fetch-Site: same-origin` along with `Origin: null`, so the library accepts the post. `webtest.Browser` is not specified to send `Sec-Fetch-Site`, so the tests exercise only the fallback path.
- **Fix.**
  - State the CSRF gate as the design's own check: `Origin` present, and equal to `publicURL`'s origin after R95's normalization. `null` and a missing Origin are refused. The HMAC token follows.
  - Either drop `CrossOriginProtection`, or keep it only as an extra outer layer that refuses `cross-site` and `same-site`, and say that it decides nothing alone.
  - Have `webtest.Browser` send `Sec-Fetch-Site` as Chrome does.
  - Add a case to the CSRF test: `Sec-Fetch-Site: same-origin` with `Origin: null` is refused.

## 3. New Minor

- **n1.** `admin_test.go` calls `admin.ItemID` (`:422`, `:505`, `:544`, `:616`, `:678`). "Passes unchanged" (Task 3) holds only if `admin.ItemID` stays as a wrapper over `app.ItemID`. Say so.
- **n2.** The web's opening key is the raw `k`, in the same namespace as the admin API's `Idempotency-Key`. v1's review suggested `"web:"+k`. The risk is negligible, since `k` is random, but prefixing is free.
- **n3.** Task 1's `TestAnAdminIsAlwaysAResponder` only drives an actor with `Responder: true`, so it cannot fail on the I1 property. That property lives in Task 5's actor test. Rename the Task 1 test, or cite Task 5's test in Review Focus 4.
- **n4.** `webtest.SignedIn(t, roles, subject) *http.Cookie` takes no keys, so it cannot seal a cookie that the server under test opens. Pass the `Keys` (or the server). Put `auth`'s tests in `package auth_test`, so that `auth` importing `webtest` importing `auth` is not a cycle.
- **n5.** The answer to sign-out (POST, Task 6) is unspecified. A 303 to a page that redirects to `/auth/signin`, and then to the IdP, is the redirect chain that D3 forbids. Name `/auth/signout` in `TestAPOSTIsNeverAnsweredWithARedirectOffOrigin`.
- **n6.** Task 10's deployment docs omit "admins need not also be listed in `responders`" (I1, fix item 3).
- **n7.** `TestSafeReturnRefusesOpenRedirects` lacks `/auth/callback` and `/auth/signout` as refused targets (v1 review Section 5).
- **n8.** The Global Constraint says `internal/web` "imports `internal/work/app` and `internal/admin/switching`". It also needs `item`, `board`, `deploy` and go-oidc. Reword it as "never a store, never `internal/admin`".
- **n9.** Line 172, "each task is about 1.5-3 raw days", contradicts the size table (I12).
- **n10.** R93 assumes the completion notice splits input, output and cache-read tokens. If it gives only a total, say how that total is priced, for example at the input rate as an upper bound.
- **n11.** `TestADemotedAdminCannotTurnTheSwitchOff` sits in Task 6, before Task 8 builds the off. Assert it on the actor and freshness in Task 6, or move it to Task 8.
- **n12.** Task 3's table still lacks a viewer's on that arrives through `recordOn` (v1 review Section 5).

## 4. Claims about code, checked

| Claim | Verdict |
|---|---|
| `crypto/hkdf` in the standard library since Go 1.24 | True (`hkdf.Key/Extract/Expand`, generic over `hash.Hash`) |
| `net/http.CrossOriginProtection` since Go 1.25, "for the Origin half" | It exists; it does not fit as described (NI1) |
| `ItemID` and `digest` at `admin.go:282, 363, 685` | True. `digest` takes `*opening`, whose fields `OpenInput` has, so `OpeningDigest(*OpenInput)` can reproduce it |
| `board.Stop` and the `Assessment` fields that `StatusOf` reads | True (`board.go:243`; `assess.go:14-39`). Hypotheses are ranked, so "rank 0" is index 0 |
| `postgres.Lease` exists for `Holder` | True (`lease.go:30`) |
| `config.Unseen` | True (`names.go:71`) |
| `standing` needs the leader's status | True (`admin.go:415-442`). R81's signatures leave it out (I3) |
