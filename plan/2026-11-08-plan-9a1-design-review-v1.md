> The Opus review of Plan 9a-1's design, version 1.

# Plan 9a-1 design v1: review

Reviewer: Opus, 2026-11-08. Subject: `plan/backups/plan9a/design-v1.md`.
Authority: the spec (Sections 4.1, 4.2, 5.1, 7, 8.2, 8.3, 9.1, 9.3, 9.4, 10, 13.1, 15), roadmap row 9a, ADR-016 (questions 10, 12, 13), Plan 7b-1's carries to 9a, and the Plan 1, 5b, 6b and 7a carries to Plan 9.
Taken as given, as the author ruled: the 9a-1/9a-2 split, and a standby that refuses both on and off.

**Counts:** 2 Critical, 15 Important, 16 Minor.

**Verdict.** The split and the overall shape (stdlib server, `html/template`, server-rendered forms, sealed cookie, go-oidc) are sound. The design is not ready to plan yet, for three reasons:
- **The kill switch's control does not follow the code it sits on.** During a drain the on goes through a route the draft never mentions, and the preview the draft makes mandatory is refused there.
- **Several controls the spec requires are missing:** CSP and frame protection, fresh authentication for admin actions, and HTTP/1-only for the header timeout.
- **The size is under-counted by about a quarter.** The pre-split row already crosses ADR-016's trigger, and the author has to hear that now.

---

## 1. Answers to the draft's Section 7 questions

### Q1. A sealed cookie with no server-side store, given the kill switch?

**Yes, for Phase 0, under four conditions.**

What a stolen cookie can and cannot do:
- An on is the safe direction.
- An off cannot be done with a stolen cookie alone. It needs a fresh IdP login, provided condition (b) below holds.
- The real residual is a stolen responder's cookie closing items for up to the session's length.

The conditions:
- **(a) A revocation lever.** Read `web.sessionKeyRef` at use, as the admin token is read at each request (`internal/admin/admin.go:203`), with a cache of at most one minute. Rotating the secret then signs everyone out within a minute. That is the emergency revoke, and it costs nothing to build.
- **(b) Fresh authentication re-reads roles.** It replaces the session's roles with the fresh ID token's groups, not only `auth_time`. Then "removed from the admins group" takes effect for the most dangerous act at once, not 12 h later. As written, D3 updates only `auth_time` ("carries the fresh auth_time through the session cookie"), so a demoted admin keeps the off for 12 h.
- **(c) A shorter default** (8 h, about a shift) and a `__Host-` cookie name (see M3).
- **(d) The plan states the residual**, as the draft already offers to.

### Q2. D5: the admin API on the web listener with ID tokens?

**Not sound as written. Cut it from 9a-1** (the draft's own candidate cut).
- **ID tokens are not API credentials.** An ID token's audience is the web client. Accepting it as a bearer token turns every place that sees an ID token (browser history, proxy logs, an IdP debugger) into an API credential holder for its lifetime, with no scopes.
- **A script has no way to get one.** The web client is confidential, with `clientSecretRef`. A CLI would need a public client and the device-authorization grant, which the draft does not design.
- **Access tokens fit better, but are not cheaper.** Their format is IdP-specific: opaque at some IdPs, JWT with a vendor-specific `aud` at others. They need either per-IdP validation (RFC 9068 where offered) or introspection. That is a design of its own.
- **It widens a leak surface.** `GET /v1/items/{id}` returns every round's `board.Report` (`internal/admin/admin.go:497-504`, `574`). Once Plan 8a adds the invisible arm, serving that route to every ID-token holder exposes hidden-run output, which spec Section 7 forbids ("no view shows it"). The loopback token is at least host-bounded.
- **The refactor is under-sized.** `admin.New` hard-wires `loopback()` and `authorized()` (`admin.go:165-166`), and every act runs as `adminActor` (`lifecycle.go:36`, `151`, `219`). Making the actor pluggable touches about 1,200 lines and about 1,800 lines of tests. That is more than 1.5 days.

**Fix:** drop D5. Keep the loopback token. Move the carried item "sign-in for the admin API" (7b-1 plan:1071) explicitly to a named later plan (9b, or 10a if scripts need it before a team). When it is built, use access tokens with a configured audience, and never accept cookies on `/api/` or bearer tokens on pages.

### Q3. Is the role mapping faithful to spec Section 10.1?

**Mostly. Five deviations need a ruling or a fix.**
1. **The "Responder" row is item-scoped in the spec** ("the item's principal and responders"). The draft adds a deployment-level `responders` group for opening and for the kill switch's on. That is a reasonable stand-in for "open items for their services" while no service ownership exists. State it as an ADR-016 question 12 assumption, with what Plan 7c-2 changes, rather than as the spec's meaning.
2. **"For their services" is not enforceable before 7c-2.** It is acceptable only because of the Phase 0 data rule (everyone entitled to everything). Say so, and say that 7c-2 owns it.
3. **The admin's item acts conflict with spec Section 9.1.** That section says "every admin action require[s] fresh authentication". D4 gives admins every responder act on every item, with no fresh authentication. See I4.
4. **`sub` is not usable as a typed name.** Pairwise or opaque subs (Entra, Okta `00u…`) cannot be typed into an opening form. See I9.
5. **The rater role buys nothing in 9a-1.** Drop it until Plan 8b (M1).

### Q4. Are `Views.List` and `ClosedSince` free of the stale-account class?

**Yes in the narrow sense.** They read records, not the coordinator's account. That account is where the class lives, as closed by Plan 7b-2.

Caveats the plan should state:
- **Queue positions come from the coordinator's published snapshot**, not from records (`views.go:201`). On a standby, or during a takeover or drain, the snapshot is empty, and every queued row shows no position. Say "position not known on this instance", not nothing.
- **`Active` plus N `Load`s is not a snapshot.** A row may fold to a later state than the list query saw. That is harmless if the row shows the folded state.
- **`ClosedSince` needs three rules.** It must use the database's time (`now() - window` in SQL), not the web host's clock: the spec measures the reopen window by the database's time (Section 4.1). It must use the item's latest close, and exclude items reopened since, which `Active` already returns. It must use `d.Lifecycle.ReopenWindow`, not a literal 24 h.
- **The real future risk is the web layer's decisions made from a view.** These are the buttons offered and the item-role check. They are safe today only because the coordinator re-checks state under its lock and principal/responders are immutable. Once 7c-2 resolves responders through identity sources (spec Sections 5.1 step 3 and 10.3, "resolved again at sending time"), a check against the view's frozen list diverges. See I3.
- **The 7b-1 residual "a view's failed read of the auto-close delay is logged nowhere on a standby"** (7b-1 plan:1100) sits in exactly the code `Views.List` reuses (`views.go:247`). 9a-1 should take it. See I15.

### Q5. Does D7's standby and drain handling agree with spec Section 8.3 and 7b-1's code?

**No.** See C1 (the drain's on and the preview) and I5 (`/leaderz`), plus I6 (the answers' required contents) and I7 (unknown outcomes). The standby half is right: the control is disabled, and there is no write without the lease.

### Q6. Is the size honest?

**No.** See I14. As written it is about 22–26 raw days, about 4.4–5.2 person-weeks. Applying the record's ratio of 1.3–1.5 gives about 5.7–7.8 as executed, for 9a-1 alone. With 9a-2 at about 2.5–3.5 raw, the row is about 7–8.7 raw against its 4–4.5. ADR-016's trigger, 1.5 times the row or 6–6.75, is crossed on raw figures, before any ratio. The cuts in Section 4 bring 9a-1 to about 12.5–15 raw days (2.5–3.0 pw).

---

## 2. Critical

### C1. The kill switch's on fails during a drain, and the mandatory preview cannot run there

**Claim.** D7 says "Draining. The off is refused with words. The on still goes through." The draft does not say how. In code, the on during a drain does **not** go through `Coordinator.SetKillSwitch`:
- During the drain, `SetKillSwitch` answers `ErrNotHeld` (`killswitch.go:156-160`, via `holds()`, `acts.go:114-116`).
- The admin handler routes that refusal to `RecordKillSwitchOn` under the draining holding's epoch. It decides this from the leader's status (`internal/admin/lifecycle.go:243-283`, `switchNotHeld`/`recordOn`), with R24's retry rule.

A web handler that calls `SetKillSwitch` directly, as D7 implies, refuses the on during every deploy. That is the moment spec Section 8.3 ("During a drain. The draining leader still takes the on") was written for.

A second problem: D7's on flow is preview-then-commit, with `After` taken from the preview. But a preview is refused during a drain:
- `RecordKillSwitchOn` answers `ErrNotHeld` for `a.Preview` (`killswitch.go:286`).
- The admin API answers a preview as `draining` (`lifecycle.go:246-253`).

So even with the routing fixed, the person cannot reach the commit, because the preview step fails first.

**Evidence.** Spec Section 8.3 "During a drain"; spec Section 8.2, the stop row ("The kill switch's on alone is still taken"); 7b-1 plan:375 (Decision 5/13: the on recorded under the draining holding).

**Fix.**
- **(a) Share the routing.** Extract the admin API's switch routing (`setKillSwitch` → `switchNotHeld` → `recordOn`, plus `standing`) into one function both front ends call. For example, `admin.SetSwitch(ctx, ports, actor, on, after, reason, preview) (Answer, Refusal)` in a small shared package, so the web layer cannot diverge from C4's tested behavior.
- **(b) Don't require a preview for the on.** Read `After` from `Views.KillSwitch` (the table, which any instance can read; `admin.go:624`, `lifecycle.go:191`). Show the preview's "stops N runs on M items" when it is available. When the preview answers draining, taking over or unavailable, still offer the on, labeled: "Consequences cannot be counted right now; the on is recorded and the instance that leads next stops every run." The spec requires showing consequences, but it never makes the stop wait for them.
- **(c) Add a proof:** scenario "on via the web during a drain is recorded deferred, and the next leader halts the runs".

### C2. No content security policy and no frame protection, on a page whose one-step control is the kill switch

**Claim.** Spec Section 9.1 requires "inert text with contextual encoding **under a strict content security policy**". The draft mentions neither CSP nor `frame-ancestors`/`X-Frame-Options`. The kill switch's on needs no fresh authentication by design (Section 8.3), and item close stops runs. Both can then be clickjacked from any site that frames the page. SameSite and Origin checks do not stop clickjacking, because the click is the user's own, in a framed same-origin page.

**Evidence.** Spec Section 9.1, the row "Content that attacks the human interface"; draft D1/D3/D9 (no headers listed).

**Fix.** Every response carries:
- `Content-Security-Policy: default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`. That is trivial, since 9a-1 has no JavaScript.
- `X-Frame-Options: DENY`.
- `Referrer-Policy: no-referrer`. The OIDC callback URL carries `code` and `state`.
- `Cache-Control: no-store` on authenticated pages.
- `Strict-Transport-Security` when serve terminates TLS.

One trap: Chrome enforces `form-action` on the redirects that follow a form POST. A POST that redirects to the IdP for fresh authentication would be blocked. Make fresh authentication a GET navigation (a link or button to `/auth/fresh?return=…`), or add the issuer's origin to `form-action`.

Add a proof that every route sets these headers, and one that a framed page is refused.

---

## 3. Important

### I1. The loopback token's off without fresh authentication conflicts with spec Section 8.3 once sign-in exists

**Claim.** Spec Section 8.3 lets the admin API's token turn the switch off only "**Until** the human interface and its sign-in exist". Section 5.1 says the same of openings by key. D2/D5 keep the token as a break-glass admin that turns the switch off with no fresh authentication, indefinitely.

**Fix.** The design is defensible: loopback access means host access, so a host operator could edit the table anyway. But it needs an explicit spec amendment, with the author's ruling, to Sections 8.3 and 5.1: "after sign-in, the loopback token stays as break-glass; its acts are recorded as subject `admin`". Alternatively, restrict the token to the on after 9a-1. Record the ruling in the plan. Do not leave the conflict silent.

### I2. Principal and responders are stored unchecked, rendered to people, and used as authorization data

**Claim.** `Coordinator.Open` records `Principal` and `Responders` exactly as given (`internal/work/app/coordinator.go:319-320`). It applies no length or count bound, no `config.Unseen` check and no redaction. Only routing (`checkRouting`, `:1318`) and the title (`title`, `:1294`) are checked.

D6 renders both fields in every row. D4 matches them, by string equality, to decide who may promote, close and reopen. Three consequences:
- **Spoofing.** A bidi or zero-width principal ("bob" followed by U+200B, or "admin" built from homoglyphs) misleads readers about who owns an item. That is the attack spec Section 9.4 forbids for agent text.
- **Unbounded size.** A 64 KiB body of responders is accepted.
- **No redaction.** Spec Section 9.4 requires everything that reaches a store to pass redaction. Principal and responders reach the store unredacted.

**Fix.** In `Open`, refuse a principal or responder that:
- is empty when present;
- is over about 256 runes;
- holds `config.Unseen` characters;
- would be changed by the always-applying rules;
- or comes with more than about 16 responders.

Use `ErrInvalid` with a reason that never quotes the value. Normalize by trimming. This is a coordinator change, so it covers the admin path too, and costs about 0.5 day.

### I3. Role checks in two front ends will drift. Put one policy where the state is read under the lock

**Claim.** D4: "Item roles are checked in the web layer, before the act, against the view's principal and responders. The coordinator's own checks stay as they are." D5 then repeats "the same item-role checks" in a second handler. That gives three entry points (pages, `/api/v1`, loopback) and two copies of the policy.

The coordinator's only role check is `!on && !a.Actor.Admin` (`killswitch.go:436`). A viewer's actor that reaches `SetKillSwitch` turns the switch on, because the coordinator takes "anyone who may act" (`killswitch.go:126-130`). The check also reads a view taken outside the coordinator's lock. That is safe only while principal and responders are immutable, which stops being true at 7c-2 (Q4).

7b-1's carry places "roles per item, and fresh authentication, above C16" (7b-1 plan:467, 1071). This is the designer's call recorded there, not a spec rule.

**Fix.** At minimum, one pure policy function `authz.May(actor Roles, act Kind, opened *item.Opened) error`, used by every front end, with a table test. Better, and still small (about 0.5 day):
- Extend `app.Actor` with the deployment roles it was signed in with: `Responder bool` beside `Admin`.
- Have `Promote`, `Close` and `Reopen` call the same function on `s.Opened` after `target()`, under the lock, refusing with `*Refused{Code: "not_item_responder"}`.
- Have `SetKillSwitch`'s on require `Responder || Admin`.

The admin token's actor stays `Admin: true`. This changes 7b-1's recorded division of labour, so it needs the author's nod. Fresh authentication stays above C16, since the coordinator cannot see it.

### I4. Admin actions need fresh authentication (spec Section 9.1)

**Claim.** Spec Section 9.1: "Turning the kill switch off **and every admin action** require fresh authentication." D4 gives admins every responder act on every item, and D3 requires fresh authentication only for the off.

**Fix.** Either:
- **(a)** an admin's act on an item where they are not principal or responder is an admin action, and needs `auth_time` within the window; or
- **(b)** admins get no item acts in 9a-1 beyond the switch, since the loopback token remains for break-glass.

(a) composes well. The off's answer lists the items left unstaffed with promote links (D7), and the admin has just re-authenticated, so those links work inside the window. Recommend (a), and state which acts count as admin actions.

### I5. `/leaderz` semantics are undefined for a drain, and as a load balancer's health check they can block the on

**Claim.** D7 adds "`GET /leaderz` answers 200 only on the leader, for a load balancer". A draining leader still holds the lease, and it is the only instance that takes the on (spec Section 8.3). If `/leaderz` answers like `/readyz`, which answers 503 `draining` (`admin.go:236-238`), the load balancer moves traffic to the standby. The standby refuses both, so for the whole drain nobody can turn the switch on.

Also, with `/leaderz` as the only routing signal, every page, reads included, is unreachable during a takeover. The standby's read pages are never reached through the load balancer, and the standby message ("Use <web.publicURL>") loops for a user who arrived through `publicURL`.

**Fix.**
- Define `/leaderz` as 200 while the lease is held, the drain included, and 503 otherwise.
- State which listener serves `/healthz`, `/readyz` and `/leaderz`.
- Document the load balancer's recipe.
- On a standby page, name the leader's instance, from the lease row's holder, rather than only `publicURL`.

### I6. The on and off answers omit what spec Section 8.3 requires them to show

**Claim.** D7 shows the on's preview and, for the off, "the items left not staffed". The spec and the code require more:
- **Every answer but a drain's on lists what is still owed.** `KillSwitchOutcome.Owed` (`killswitch.go:39`; `lifecycle.go:471`, `502-504`).
- **The on's answer lists the runs it stopped and the items it left unstaffed.** `Stopped` and `Unstaffed`.
- **A drain's on says it was deferred.** `Deferred`: "recorded; the instance that leads next stops every run".
- **The off says when its list could not be read in full.** `StayedUnstaffedUnread`, "a list that could not be read in full says so" (spec Section 8.3).
- **The answer, not the preview, is authoritative.** 7b-2 left a residual: a run with no agent to invite can be named by the preview and left out by the answer (roadmap, 7b-2 residual).

**Fix.** List these fields in D7's answer pages, and add each to proof 4/6.

### I7. Unknown outcomes and the per-rendering key: a retry must reuse the key, and the switch must be re-read

**Claim.** D6: "Each act carries an `Idempotency-Key` that the form makes once per rendering." The coordinator answers an unknown outcome with "send it again with the same key" (`lifecycle.go:50`, `341`; carryOn, `acts.go:288-306`). If the error page re-renders the form with a new key, the user's retry is a new act.

Most item acts are idempotent by state, but not all. Reopen of an item that a concurrent auto-close re-closed, for example, is a second, real reopen. And the audit trail then shows two acts where the person meant one.

For the switch, the 503 text says to read the switch and send again with the seq read (`lifecycle.go:51`). D7 handles only stale.

The refusal list in D6 also omits several cases:
- `taking_over`: 503 with Retry-After (`lifecycle.go:354-357`);
- `key_reused`, `held_by_later_build` and `no_such_item`;
- the `Refused` codes `closed`, `reopen_window_passed`, `limit_reached`, `kill_switch_on`, `daily_cap_reached`, `admin_only` and `reason_required` (`acts.go:89-97`);
- `unavailable`.

**Fix.** Rules:
- A form re-rendered after `unavailable` or `taking_over` keeps its key.
- A new key is minted only after a definitive answer.
- Each act form on a page has its own key (R42: a key names one act of its item).
- After a switch 503, the page re-reads the table and shows the state now, with a fresh form whose `After` is that row.

Map every code above to words. Reuse the admin API's `refusal()` mapping (`lifecycle.go:318-342`) through the shared function of C1, so the codes and their meanings cannot fork.

### I8. Fresh authentication is under-specified

**Claim.** D3 gives one line. These need to be pinned down:
- **(a) `auth_time` is required.** It is required when `max_age` is sent (OIDC Core 3.1.2.1). Refuse a token without it, and refuse one whose `auth_time` is older than the window, so that an IdP that ignores `prompt=login` fails closed. Google, for one, ignores `prompt=login`.
- **(b) The fresh token's `sub` and `iss` must equal the session's.** Otherwise, decide explicitly that fresh authentication replaces the session wholesale.
- **(c) Roles are re-read from the fresh token** (Q1(b)).
- **(d) The POST is lost.** The act's POST does not survive the round trip to the IdP. The flow must return to a GET of the act's form, re-previewed with a current `After`. It must never replay the POST.
- **(e) Clock skew.** The IdP's and the instance's clocks differ. Allow a small leeway (about 60 s), stated.
- **(f) CSP `form-action`** (C2).
- **(g) Spec Section 15 parameters.** The window [5 min] and session length [12 h] are new parameters. Add them to spec Section 15.

**Fix.** Write these into D3, with a proof for each of (a), (b) and (d).

### I9. Subject identity: collision with `admin`, issuer scoping, and usability

**Claim.** The loopback token records the subject `admin` (`lifecycle.go:36`). An IdP user whose `sub` (or chosen claim) is `admin` becomes indistinguishable in every record: `Closed.By`, `Promoted.By`, the kill switch's `by`. That defeats 9a-1's purpose, "a real identity".

`sub` is unique only per issuer. And openings "name people by `sub`" (D4), which people cannot type (Q3).

**Fix.**
- Record web subjects as `sso:<value>`, or with an issuer-scoped prefix. Reserve the unprefixed `admin` for the token.
- Make the identifying claim configurable (`web.oidc.subjectClaim`, default `sub`; `email` or `preferred_username` when the deployment vouches that it is immutable), and state the reassignment risk.
- Item principals and responders are matched in the same prefixed form.

### I10. The board page's quotes are not in `Report`

**Claim.** D8: "the ranked hypotheses from `Report`, each with its key, its status word and its evidence quotes". `board.Report` holds `Assessment`, `Facts` and `Stopped` (`internal/work/board/report.go:24-29`). `HypothesisView` holds supporters, refuters, sources and standing refutations, with **no evidence entries or quotes** (`assess.go:14-31`). `board.State` exports no per-hypothesis evidence list (`board/state.go`; only `Event(id)`).

The "status word" set (converged, confirming, leading, trailing, refuted, rejected; spec Section 10.2) is not computed anywhere either. "Confirming" needs the wrap-up round's state.

So D8 needs a new board read, which is essentially 9a-2's quote rule without its selection, plus a status-word function. 1.5–2 days does not cover that with its tests.

**Fix.** Cut D8 to a page without quotes:
- state, stop reason and banners;
- the conclusion's key and its model-written statement;
- hypotheses with key, score, status word, and supporter and refuter counts;
- the run's stop entry, which Plan 7a's carry asks the interface to show (7a plan:223).

Move quotes, and with them the Plan 1 bidi carry (plan-1:4964), to 9a-2, where the quote rule picks them anyway. Statements and claims are agent text the board already refuses with unseen characters (spec Section 9.4), so bidi neutralization is not needed in 9a-1. Escaping by `html/template` still is.

### I11. The web listener must be HTTP/1-only, or it loses the spec's header timeout

**Claim.** Spec Section 8.2: "Each listener ends a request whose header has not arrived within 10 s". Serve's own comment says Go's HTTP/2 server has no per-request header timeout, and pins the agents' listeners to HTTP/1 for that reason (`cmd/agentswarm/serve.go:687-701`). A TLS-terminating `http.Server` negotiates h2 by default. The draft is silent on this, and this is the one listener exposed off loopback.

**Fix.** Use `srv.Protocols.SetHTTP1(true)` on the web server, as `agents()` does. Also set `MaxHeaderBytes` and an `IdleTimeout`. Add a proof: a slow header is cut at 10 s over TLS.

### I12. Origins, redirects and proxies: configure them, never derive them from the request

**Claim.** D3 checks that "`Origin` is the listener's own origin", and returns deep links to "same-origin relative paths". D2 trusts `X-Forwarded-Proto` from a loopback peer. `web.publicURL` appears only in D7's standby text, and not in D3's fields. If the origin, the OIDC `redirect_uri` or redirects are derived from `Host` or forwarded headers, Host-header injection and open redirects follow.

**Fix.**
- **Make `web.publicURL` required** and the single source of the origin, the redirect URL and absolute links. Refuse requests whose `Host` differs; this is cheap protection against DNS rebinding too.
- **Trust no forwarded header at all.** With `publicURL` configured, nothing needs `X-Forwarded-Proto`, so drop it.
- **Return paths:**
  - accept only a value that starts with a single `/`, is not `//` or `/\`, holds no backslash or control character, and parses with no scheme or host;
  - keep it inside the sealed state cookie, never in the callback's query;
  - return only to GET pages.
- **State cookies:** one per flow, with a random name suffix, so that two tabs signing in do not clobber each other. Use `SameSite=Lax` with `response_mode=query`, since a `form_post` response would drop Lax cookies.
- **Sign-out is a POST** under the same CSRF check.

Add proofs for `//evil`, `/\evil`, percent-encoded variants and a foreign `Host`.

### I13. Carried constraints: what 9a-1 honors and where the rest goes must be explicit

**Claim.** The draft does not account for the carries. My reading follows. 9a-1 must state each line, or move it by name.

| Carry | Source | 9a-1 must | May leave to |
|---|---|---|---|
| Items list | 7b-1:1071 | Honor (D6) | — |
| Roles per item, fresh authentication | 7b-1:467, 1071 | Honor (D3/D4; I3, I4, I8) | — |
| Kill switch control, presets, responders on, admins off with fresh authentication | 7b-1:1071 | Honor (D7; C1, I6) | approvers to Phase 1 |
| Standby on | 7b-1:1071 | Honored by the author's ruling (refuse) | — |
| Opening and promoting from the items view | 7b-1:1071; spec 10.2 | Promote: honor. Opening: honor, or move to 9a-2 by name (cut S5) | 9a-2 |
| Sign-in for the admin API | 7b-1:1071; roadmap row 9a | If D5 is cut, move by name and record the row's change | 9b or 10a |
| Opening route's codes (R15) | 7b-1:467 | Cheap (about 0.25 day); do it, since the shared refusal mapping of I7 touches it | — |
| Live board merges item records into the feed | 7b-1:1071 | — | 9a-2 |
| A view's failed auto-close delay read, silent on a standby | 7b-1:1100 | Honor (I15) | — |
| Close and reopen in the interface "through C16" | 7b-1:1073 (assigned to **9b**) | The draft takes them into 9a-1. That is defensible ("stop" an item), but record the move from 9b | — |
| Quotes inert, bidi neutralized | Plan 1:4964 | Honor if quotes ship; otherwise move with them (I10) | 9a-2 |
| Suggestions, their summary parts as data, "outside service" mark, proposer | 5b:186-188, 203-207 | — | 9a-2 (say so) |
| Showing notices to people | 5b:189; spec 8.1 | — | 9a-2 (say so; until then a failure is visible only in logs, which spec 8.1 says must not fail silently) |
| Release rule on model-call records | 6b:233 | No 9a-1 surface shows them; state it | the plan that shows them |
| Stop entry shown; rulings after it | 7a:223 | Stop entry: honor on the board page. Rulings: 9b | 9b |
| Message to the responders' channel at start; kill switch messages | 7a:223; 7b-1:1076 | — | 9b (say so) |
| 8a hides invisible-arm boards in 9a's views | 7b-1:1080 | Leave a single seam (a `Visible(view)` predicate every board route calls); 8a fills it | 8a |

### I14. The size is under-counted

**Claim.** Section 6 sums to 17.5–21 days. These items are missing or too small:

| Item | Extra days |
|---|---|
| Shared kill-switch routing and refusal mapping (C1, I7) | +1 |
| Security headers, HTTP/1, Host check, `publicURL` (C2, I11, I12) | +0.5–1 |
| Validation of principal and responders (I2) | +0.5 |
| Policy function and actor roles (I3) | +0.5 |
| Subject namespacing (I9) | +0.25 |
| Board evidence read and status words, if D8 stays (I10) | +1–1.5 |
| Deployment fields: about 15 new keys, each with `deploy.go`'s checker rules, refusals that never repeat secrets, and tests (`internal/deploy/deploy.go:547-690`) | +0.5–1 more than the 1–1.5 given |
| A fake IdP inside the black-box harness, reachable by a separately started serve, with TLS or an allowed http issuer in tests | +1 |
| D5's refactor, if it stays (Q2) | +0.5–1 more than the 1.5 given |

**As written:** about 22–26 raw days, about 4.4–5.2 pw. At the record's 1.3–1.5 that is about 5.7–7.8 as executed, against a whole-row estimate of 4–4.5.

**With the cuts of Section 4:** about 12.5–15 raw days (2.5–3.0 pw), about 3.3–4.5 executed.

The pre-split row as designed (9a-1 as written plus 9a-2 at about 2.5–3.5) comes to about 7–8.7 raw. That crosses ADR-016's trigger, 1.5 times the row, or 6–6.75, on raw figures alone. The draft's sentence "The author must hear this with the plan" should say exactly that: **the trigger fires at design time.**

**Fix.** Re-table Section 6 with these lines, state the trigger in those words, and offer the simplified scope as the default.

### I15. The missing-delay residual and the standby's empty snapshot surface in the new list

**Claim.** `Views.autoClose` drops `ClosesAt` silently when `delayOf` fails (`views.go:247-249`). 7b-1 names Plan 9a as this residual's owner (7b-1 plan:1100). `Views.List` folds every item through this code. A row that shows no auto-close time without saying why is the silent failure spec Section 8.1 forbids.

**Fix.**
- Give `View` a `ClosesAtUnread` flag beside `AutoCloseUnread`.
- Render "auto-close time not known".
- Log once per item per process.
- Read the kill switch once per list, not once per awaiting row (`views.go:238`; see M6).

---

## 4. Simplifications that keep 9a-1's purpose

The purpose: a person can see the platform and safely stop it, with a real identity. Cuts in order of value:

| # | Cut | Saves | Cost or carry |
|---|---|---|---|
| S1 | Drop D5 (Q2) | 1.5–2.5 d | Move the carry by name; the roadmap row changes |
| S2 | Proxy-only TLS: drop termination in serve, require `web.proxied` behind a TLS proxy. Keep HTTP/1, timeouts and the Host check | 0.5–1 d | Spec 9.3 rotation without downtime would otherwise need a certificate reloader (`GetCertificate` with re-read). The plaintext loopback hop matches the admin API's |
| S3 | D8 without quotes (I10) | 1–1.5 d | Quotes and the bidi carry go to 9a-2 |
| S4 | Leave the invisible-arm row to Plan 8a. Every item is visible until then (`item.go:146-150`). Leave the predicate seam of I13 | 0.5 d | 8a's carry already covers the hiding |
| S5 | The opening form to 9a-2, or a minimal one: type select, title, `service` from a configured list, principal defaulting to self, no free-form routing editor | 0.5–1 d | Openings stay on the admin API and inbound events (7c-1) |
| S6 | Drop "the incident's other items" from `View`. There is no index on `incident_id` (`0007_work_records.sql`), every item is its own incident until 7c-1's links, and the list groups in memory | 0.25 d | — |
| S7 | A light fold for rows, records only: no board replay and no per-row kill switch read (`views.go:178`, `238`). Keep `Views.Item` for the board page | Not a saving; prevents N board replays per page load | — |
| S8 | Harness web scenarios under the process runner only. The kill switch's effect on Docker turns is already covered by scenarios 19–23 through the admin API. `make test-docker` stays in the gate | 0.5 d | — |
| S9 | Drop the rater group (M1) | Small | — |

---

## 5. Minor

- **M1. Drop the `raters` group from 9a-1.** It grants nothing until Plan 8b, which adds it along with "a rater MUST NOT be a platform builder" (spec 10.1).
- **M2. go-oidc adds two dependencies.** It pulls `github.com/go-jose/go-jose/v4` as well, so D1's "one new dependency" is two. Fine; say so. Also check `azp` when `aud` has several values, since go-oidc does not.
- **M3. Cookie name and scope.** Name the cookie `__Host-session`, with `Path=/` and no `Domain`, rather than path-scoped. Inside the sealed payload, include the issuer and client ID and an issued-at, so that a cookie from another deployment sharing a key is refused.
- **M4. Serve must start and lead while the IdP is down.** Discovery should be lazy and retried. Document that during an IdP outage nobody can sign in, so the only stop is the loopback token on the host. Put that in the runbook.
- **M5. Two instances need the same keys.** Both must share `sessionKeyRef`, and the state-cookie key, because a callback may land on the other instance after a failover. Put it in the deployment docs.
- **M6. Never compute a preview in the header.** The fixed header reads the table only (`Views.KillSwitch`). `wouldKill` takes the coordinator's lock and may read boards (`killswitch.go:487-512`). Calling it on every page load would contend with the coordinator, which Plan 10b is to measure.
- **M7. The off's preview shows nothing in this build** (`killswitch.go:454`). Spec 4.2's "consequences first" is met only weakly. Consider having the off's preview list the items that will stay unstaffed, which `UnstaffedBy` already reads.
- **M8. Offer one-tap preset reasons on the on.** For example, `runaway_cost`, `bad_output` and `security`, each a submit button, plus `other` with text. That keeps "stopping stays fast" (spec 8.3) on a phone.
- **M9. Times without JavaScript.** Times render on the server, so they are in UTC or the deployment's zone. Add a `web.timeZone` setting, or render relative times ("4 min ago") alongside UTC. Spec 10.4's zone-by-name rule is for time entry, which is 9b's, but a responder at night will misread UTC.
- **M10. Logs.** Never log the callback's query (`code`, `state`), ID tokens, cookies or form bodies (spec 9.3: secrets never in logs). Request logging should record the path only.
- **M11. Show "held by a later build"** on a row whose view says so (`views.go:159`). That is a fixed text, not an error.
- **M12. Each instance has its own reopen window.** `ReopenableUntil` comes from each instance's deployment file (`views.go:208`; 7b-1's 8b carry). The page should show the window the coordinator will enforce, which is the leader's. Note that it can differ during a rolling change.
- **M13. Opening keys share one namespace across people** (`admin.go:282`, `ItemID(tenant, key)`). Server-minted random keys make collisions moot. Never accept a client-supplied key on the web path.
- **M14. Spec 5.1 step 4** ("an in-scope invisible-arm item open for the same service, no new item opens") is Plan 8's. Note it under out of scope, so that 9a-1's opening form is not later assumed to implement it.
- **M15. Proofs to add:**
  - headers and framing (C2);
  - the drain's on via the web (C1);
  - a retry after `unavailable` keeps its key (I7);
  - `taking_over` maps to "try again" (I7);
  - fresh authentication with a different `sub`, a missing `auth_time`, and an IdP that ignored `max_age` (I8);
  - `alg:none` and HS256-with-public-key tokens refused, as a regression, even though go-oidc handles them;
  - open-redirect variants and a foreign `Host` (I12);
  - a slow header over TLS (I11);
  - a principal with a zero-width character refused (I2);
  - rotating the session key signs everyone out (Q1(a)).
- **M16. Clarify D9's 44 px.** Spec 10.4 says 44 points. On the web, CSS px are the intended unit; say so.

---

## 6. What the draft gets right

Worth keeping as is:
- the stdlib server with `html/template` and no JavaScript;
- go-oidc rather than hand-written JWT;
- PKCE with state and nonce;
- double-submit CSRF plus an `Origin` check;
- fresh authentication only on the off, keeping the on fast;
- the standby's refusal with no write outside the lease;
- reading the view from records rather than the coordinator's account;
- "refusals in words, never a raw error";
- a plain board page instead of a premature live one;
- naming the cuts the author can take.
