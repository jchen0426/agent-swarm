> Plan 9a-1's design, version 2, as corrected by its re-review and the plan review; copied here from `plan/backups/plan9a/design-v2.md`.

# Plan 9a-1 design, v2: sign-in, roles, the items view and the kill switch's control

Status: v2 after the Opus review of v1 (`design-review-v1.md`: 2 Critical, 15 Important, 16 Minor), 2026-11-08. Not approved.

**The author's rulings of 2026-11-08:**
- **R-a.** 9a is split. 9a-1 is this design; 9a-2 is the live board.
- **R-b.** A standby refuses both on and off (spec Section 8.3 stands). Its pages say so and name the leader.
- **R-c.** The review's cuts are taken: about 2.5–3.0 raw person-weeks.
- **R-d.** After sign-in, the loopback admin token stays as the break-glass path. It turns the switch on and off and acts as subject `admin`, and spec Sections 5.1 and 8.3 are amended to say so (I1).
- **R-e.** Role checks move into the coordinator, under its lock, through one policy function. This changes Plan 7b-1's recorded division of labour, which put them above C16 (I3).
- **R-f.** ADR-016's revisit trigger becomes calendar time and cost. Person-weeks are still recorded, but no longer stop a plan. The numbers are proposed in Section 8 and confirmed with this plan.

## 1. Purpose and scope

A person can see the platform and safely stop it, with a real identity.

**9a-1 builds:**
- sign-in through OIDC;
- roles, checked in the coordinator;
- fresh authentication;
- the items view, with promote, close, reopen and a minimal opening form;
- the kill switch's control, routed as the admin API routes it, the drain included;
- a plain board page without quotes;
- the security headers and the listener's hardening.

**Moved out by name (I13):**
- **The admin API's sign-in** (7b-1 plan:1071; roadmap row 9a) moves to **Plan 10a**. It is to be built with access tokens of a configured audience, never ID tokens, and never cookies on `/api/` (review Q2). The loopback token stays.
- **To 9a-2:**
  - quotes, and with them the bidi carry (Plan 1:4964);
  - the feed merged with the item's records (7b-1:1071);
  - suggestions, their summary parts as data, the "outside this item's service" mark and the proposer (5b:186-188, 203-207);
  - notices shown to people (5b:189);
  - the live refresh;
  - the rules header, the incident strip, the full board-state table, and the wide layout.
- **To 9b:** rulings after the stop entry (7a:223); messages, those of the kill switch included (7a:223; 7b-1:1076); milestones and time entry.
- **To 8a:**
  - the invisible-arm row and the hiding of invisible-arm boards (7b-1:1080). 9a-1 leaves one seam, `web.visible(view) bool`, which every board route and every row's link calls. Until Plan 8a it returns true, since every item is visible (`item.go:146-150`).
  - spec 5.1 step 4 (M14).
- **To Plan 8b:** the rater role (M1).

**Taken into 9a-1 from 9b:** close and reopen in the interface (7b-1:1073 assigned them to 9b). To stop an item is part of "stop".

## 2. Decisions

### D1. Stack

- **Rendering.** The standard library's HTTP server (roadmap line 97) and `html/template`, rendered on the server. Forms post and redirect. There is no JavaScript in 9a-1.
- **New dependencies:** `github.com/coreos/go-oidc/v3`, which brings `github.com/go-jose/go-jose/v4`, and `golang.org/x/oauth2`, which is promoted from indirect.
- **Audience.** Verification checks `azp` when `aud` has several values, since go-oidc does not (M2).

### D2. The web listener (I11, I12, S2)

- **Proxy only.** `listeners.web` listens on loopback, plain HTTP, behind a TLS-terminating proxy that the deployment runs. Serve does not terminate TLS in 9a-1.
- **`web.publicURL`** (https, required) is the single source of the origin, the OIDC redirect URL and every absolute link. A request whose `Host` is not `publicURL`'s host is refused (400). No forwarded header is trusted or read.
- **The server.** HTTP/1 only (`Protocols.SetHTTP1(true)`, as `agents()` in `cmd/agentswarm/serve.go:687-701`). It has spec 8.2's 10 s header timeout, `MaxHeaderBytes` of 64 KiB, an `IdleTimeout` of 120 s, and the body cap of the admin API (64 KiB).
- **Health checks** are served on the web listener:
  - `/healthz` and `/readyz`, as on the admin listener;
  - `/leaderz`, which answers 200 while this instance holds the lease, **the drain included**, and 503 otherwise (I5).
  
  The deployment docs give the load balancer's recipe: route to the instance whose `/leaderz` is 200, and fall back to any `/readyz` 200 for reads.
- **Logs.** Request logs record the method, the path without its query, and the status. They never record the callback's query, tokens, cookies or form bodies (M10).

### D3. Security headers (C2)

Every response carries:
- `Content-Security-Policy: default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`
- `X-Frame-Options: DENY`
- `Referrer-Policy: no-referrer`
- `X-Content-Type-Options: nosniff`

Authenticated pages also carry `Cache-Control: no-store`. HSTS is the proxy's job, and the docs say so.

Every hop to the IdP is a GET navigation, a link or a 303 after a GET, never a redirect that follows a POST. This is because Chrome applies `form-action` to redirects after a form POST.

### D4. Sign-in (I8, I9, I12, Q1)

**Deployment fields:**

| Field | Notes |
|---|---|
| `web.oidc.issuer` | |
| `web.oidc.clientID` | |
| `web.oidc.clientSecretRef` | a `secret://` ref |
| `web.oidc.subjectClaim` | default `sub`. The deployment may name `email` or `preferred_username` only if it vouches that the value is never reassigned; the docs state the risk. |
| `web.oidc.groupsClaim` | default `groups` |
| `web.groups.viewers`, `web.groups.responders`, `web.groups.admins` | one group name each |
| `web.sessionKeyRef` | a `secret://` ref |
| `web.sessionLength` | [8 h], new in spec Section 15 |
| `web.freshWindow` | [5 min], new in spec Section 15 |

Their checks follow `deploy.go`'s checker. A refusal never repeats a secret's value.

**The flow:**
- Authorization code with PKCE, `state`, `nonce` and `response_mode=query`.
- Each flow has its own sealed state cookie, `__Host-flow-<random>`, which carries the PKCE verifier, the nonce, the return path, whether the flow is fresh, and an expiry of 10 min. Two tabs therefore never clobber each other.
- Discovery is lazy and retried, so serve starts and leads while the IdP is down (M4).

**Subjects.**
- A person is recorded as `sso:<claim value>`. The unprefixed `admin` stays reserved for the loopback token.
- The value must pass the same checks as a principal (D6). A token whose value fails them is refused at sign-in, with words.

**Roles.**
- The groups claim maps to the deployment roles `viewer`, `responder` and `admin`.
- A token with no groups claim, or none of the three groups, signs in to a page that says "no role", and gets no data.

**The session.**
- A cookie, `__Host-session` (`Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/`, no `Domain`), sealed with AES-GCM under the key at `web.sessionKeyRef`.
- **The cookie's life (amended by Plan 9a-2, 2026-11-10).** The cookie outlives its session by one day: its `Expires` and `Max-Age` run one day past the sealed expiry, where the cookie once lived exactly as long as its session. A browser then still sends a session that has expired, so that the board's poll can tell an expired session, which reloads the page once into sign-in, from a sign-out, which deletes the cookie. The sealed expiry still refuses the session at its own time: the longer life of the cookie lets a request do nothing that an expired session may not.
- **Contents:** the issuer, the client ID, the subject, the roles, `auth_time`, the issue time and the expiry.
- **The key is read at use,** cached at most 1 min. So rotating the secret signs everyone out within a minute: the emergency revoke (Q1a).
- **Both instances share the key** (M5).
- A cookie whose issuer or client ID differs from the deployment's is refused.

**Return paths.**
- One is accepted only if it starts with a single `/`, is not `//` or `/\`, holds no backslash, control character or percent-encoded `/` or `\`, and parses with no scheme or host.
- It is kept in the flow cookie, never in a query.
- It returns to a GET page only.

**CSRF.** Every POST checks:
- that `Origin` equals `publicURL`'s origin, refusing a POST that has no `Origin`;
- a form token that is the session's HMAC under the session key.

Sign-out is a POST under the same checks.

**Fresh authentication.**
- It is a GET to `/auth/fresh?return=<path>`, which starts a flow with `max_age=0` and `prompt=login`.
- The callback requires `auth_time`, and refuses a token whose `auth_time` is older than `web.freshWindow` plus a fixed leeway of 60 s. An IdP that ignores `prompt=login` (Google does) therefore fails closed.
- The token's issuer and subject must equal the session's. Otherwise the callback is refused and the session is kept.
- **Roles are re-read** from the fresh token and replace the session's (Q1b). So a person removed from `admins` loses the off at once.
- **The POST is never replayed.** The flow returns to the act's form as a GET, re-previewed with a current `After`.

**The residual stated:** a stolen session cookie can act as its responder until it expires, at most [8 h], or until the key is rotated. An off needs a fresh IdP login.

### D5. Roles, in the coordinator (R-e, I3, I4)

**The actor.** `app.Actor` gains `Responder bool` beside `Admin`. The web layer builds the actor from the session. The loopback token's actor stays `{Subject: "admin", Admin: true}`.

**The policy.** One pure function, `app.May(a Actor, act ActKind, o *item.Opened) error`, table-tested. `Promote`, `Close` and `Reopen` call it on `s.Opened` after `target()`, under the coordinator's lock. A refusal is `*Refused{Code: "not_item_responder"}`.

**What each role may do:**
- **The kill switch:**
  - On: `Responder || Admin`. A viewer's on is refused with `*Refused{Code: "responder_only"}`.
  - Off: `Admin` (as today, `killswitch.go:436`).
- **An item act** (promote, close, reopen):
  - allowed if `a.Subject` is the item's principal or one of its responders;
  - or if `a.Admin` holds, which makes it an admin action;
  - the loopback token is always an admin.
- **An opening:** `Responder || Admin`.

**Fresh authentication (spec Section 9.1, I4)** stays above C16, since the coordinator cannot see it. The web layer requires `auth_time` within the window for:
- the off;
- any act where the person is not the item's principal or responder, which is an admin action;
- an admin's opening for someone else.

The answer to the off lists the items left not staffed, with promote links. These work inside the window the admin just opened.

**ADR-016, question 12 assumption, stated.** A deployment `responders` group stands in for spec 10.1's "open items for their services". There is no service ownership until Plan 7c-2, which maps people to services through identity sources. Until then the Phase 0 data rule makes it safe, since everyone who can view is entitled to everything.

### D6. Principal and responders checked at open (I2)

`Coordinator.Open` refuses with `ErrInvalid`, with a reason that never quotes the value, when any of the following holds:
- a principal or responder is empty when present;
- one is longer than 256 runes, or holds `config.Unseen` characters;
- one would be changed by the always-applying redaction rules;
- there are more than 16 responders.

Values are trimmed. This covers the admin path too.

### D7. The kill switch's control (C1, I6, I7, M6, M7, M8)

**One routing.** The admin API's switch routing moves into `internal/admin/killswitch` (name to settle), a function both front ends call:
- `setKillSwitch`, then `switchNotHeld`, then `recordOn`, with R24's retry, `standing`, and the `refusal()` mapping (`lifecycle.go:243-283`, `318-342`).

The web layer cannot then diverge from C4's tested behavior. The opening route's codes (R15, 7b-1:467) are done with it.

**The fixed header** reads `Views.KillSwitch` only, which is the table. It shows the state in words, `on since T by X` or `off`, and the row it read. It never computes a preview (M6). It is on every page and links to `/kill-switch`.

**On:**
- The page reads `After` from the table.
- It shows the preview's "stops N runs on M items" when the preview answers.
- When the preview answers `draining`, `taking_over` or `unavailable`, the on is still offered, labeled: "Consequences cannot be counted right now. The on is recorded, and the instance that leads next stops every run."
- **Reasons** are one-tap submit buttons for preset codes (`runaway_cost`, `bad_output`, `security`), plus `other`, with text checked by `app.PersonText`.
- There is no fresh authentication.

**Off:**
- An admin only, inside the fresh window.
- Its page shows the items the latest on left not staffed, from `UnstaffedBy`, as its preview (M7).

**Answers show** what spec Section 8.3 lists (I6):
- `Stopped`, `Unstaffed` and `Owed`;
- `Deferred` for a drain's on, with the words above;
- `StayedUnstaffed`, each item with its state now and a promote link;
- "the list could not be read in full" when `StayedUnstaffedUnread` is set.

The answer is authoritative. The preview's count can differ (7b-2's residual), and the page says "the answer below is what happened".

**Unknown outcomes (I7):**
- A switch answer of 503 makes the page read the table again and show the state now, with a fresh form whose `After` is that row.
- A stale refusal shows the same.

**Standby (R-b):**
- The control and every act are disabled.
- The page names the leader's instance from the lease row's holder, and says "this instance does not lead; the switch is set on the leader".
- A standby's queue positions show as "not known on this instance".

### D8. The items view (S4, S5, S6, S7, I15, M11, M12, M13)

**The list.** `Views.List(ctx)` returns:
- the active items (`Records.Active`);
- the items whose latest record is a close inside the reopen window. `Records.ClosedSince` selects them in SQL, by the database's time (`recorded_at > now() - window`), with the window from `Lifecycle.ReopenWindow`. Items reopened since are excluded, since `Active` returns them.

**Rows** come from a light, records-only fold: no board replay and no read of the kill switch per row. The fields are:
- the id, title, service (the routing value `service`, if any), principal and state as a text label;
- the queue position from the snapshot, or "not known on this instance";
- the close time;
- `ReopenableUntil`, showing the window this instance's deployment sets (M12);
- `HeldByLaterBuild`, shown as fixed text (M11);
- `ClosesAt`, or "auto-close time not known" when the delay could not be read. `View` gains `ClosesAtUnread`, and the failure is logged once per item per process (I15).

Rows are grouped by incident in memory. Each item is its own incident until Plan 7c-1's links, and there is no "other items" field (S6).

**Acts:**
- **Each act form has its own key,** minted by the server once per rendering (R42; M13). A client-supplied key is never accepted on the web path.
- **Re-renders after `unavailable` or `taking_over` keep the key.** A new key is minted only after a definitive answer (I7).
- **Promote, close and reopen** each show their preview's consequences first, then commit with the same key and the preview's `After`. Close and reopen take a reason: a preset code, or `other` with text.
- **The minimal opening form** has a type from the revision's work-item types, a title, a `service` from the deployment's configured list (`web.services`), and the principal, defaulting to the person. There are no responders, no free-form routing and no key field.

**Refusals in words, through the shared mapping:**
- `stale`, `invalid`, `unavailable`, `taking_over` (try again shortly), `draining`, `not_held`, `key_reused`, `held_by_later_build` and `no_such_item`;
- the `Refused` codes `closed`, `reopen_window_passed`, `limit_reached`, `kill_switch_on`, `daily_cap_reached`, `admin_only`, `reason_required`, `not_item_responder` and `responder_only`.

### D9. The board page (S3, I10)

The page is `GET /items/{id}`, behind `web.visible`. It holds:
- the state and the stop reason, with spec 10.2's fixed texts for the known states (queued with its position; investigating; not staffed; awaiting close; closed);
- banners for the kill switch ("Stopped by the kill switch") and for not staffed;
- the run's stop entry (7a:223);
- the conclusion's key, and its statement marked "model-written";
- the hypotheses ranked by score, each with its key, score, supporter and refuter counts, and status word.

**Status words.** A new function `board.Status` yields converged, leading, trailing, refuted or rejected. "Confirming" waits for 9a-2, which reads the wrap-up round.

**What it leaves out.** No quotes, no raw outputs, no feed and no live refresh. Statements are escaped by `html/template`. Bidi neutralization goes with quotes to 9a-2: agent text with unseen characters is already refused on the board (spec 9.4). The page shows "last updated at T" in the deployment's time zone (`web.timeZone`, default UTC) and in relative words (M9).

### D10. Accessibility

- Semantic HTML and native forms, so every action works from the keyboard.
- Every status has a text label.
- Touch targets are at least 44 CSS px, which stands for spec 10.4's 44 points (M16).
- The layout holds at 200% text, and there is a dark theme through `prefers-color-scheme`. One stylesheet, served by serve.

## 3. Spec and doc amendments, made with the plan

- **Section 5.1 and Section 8.3, "Until sign-in" (R-d):** "After sign-in exists, the admin API's token stays as the break-glass path on the host: it turns the switch on and off and opens items, recorded as subject `admin`."
- **Section 9.1:** fresh authentication is required for the off and for every admin action, which is an act on an item where the person is not its principal or responder.
- **Section 15:** session length [8 h] and fresh-authentication window [5 min].
- **Roadmap row 9a, split into 9a-1 and 9a-2.** The admin API's sign-in moves to row 10a, and close and reopen in the interface move from 9b to 9a-1.
- **ADR-016:** a History entry for R-c, R-e and R-f, and the question 12 assumption as stated in D5.

## 4. Proofs

1. **Sign-in with a fake OIDC provider** (`httptest`, a real JWKS), in unit tests and in the black-box harness:
   - the session, the deep-link return, sign-out (POST) and expiry;
   - rotating the session key signs everyone out.
2. **Refused tokens:** a wrong issuer, a wrong audience, a bad nonce, an expired token, `alg: none`, HS256 signed with the public key, and `aud` with several values but a wrong `azp`. A token with no groups gets no data.
3. **Fresh authentication:**
   - a missing `auth_time`;
   - an old `auth_time`, as from an IdP that ignored `max_age`;
   - a different `sub`;
   - roles re-read: a demoted admin cannot turn the switch off;
   - the act's form is re-shown as a GET, with no POST replayed.
4. **Headers and framing:** every route sets D3's headers. HTTP/1 only. A slow header is cut at 10 s. A foreign `Host` is refused.
5. **Open redirects:** `//evil`, `/\evil` and percent-encoded variants are refused. CSRF: a missing or foreign `Origin` and a missing token are refused.
6. **Policy:**
   - `app.May`'s table;
   - in the coordinator: a viewer's on is refused, a non-item responder's promote is refused, and the loopback admin's acts are unchanged;
   - an admin's item act outside the window is refused by the web layer.
7. **`Open`'s new checks:** a principal with a zero-width character, an over-long responder, and 17 responders.
8. **The kill switch:**
   - the on's answer lists `Stopped`, `Unstaffed` and `Owed`;
   - an on via the web during a drain is recorded `Deferred`, and the next leader halts the runs (harness scenario);
   - a stale `After` and a 503 re-read the table;
   - the off's page lists the unstaffed items;
   - the standby disables the control, names the leader, and `/leaderz` answers 503 there and 200 on a draining leader.
9. **The items view:**
   - active items, plus items closed inside the window by the database's time;
   - an item reopened since is listed once;
   - "auto-close time not known" with its one log line.
10. **Acts:**
    - preview, then commit;
    - a double submit changes nothing;
    - a retry after `unavailable` keeps its key;
    - `taking_over` maps to words.
11. **The board page:** a seeded board renders states, banners, the stop entry, status words and "model-written". The seam `web.visible` is called on every board route.
12. **Harness scenarios under the process runner:** a person signs in, opens an item, turns the switch on, and an admin re-authenticates and turns it off. `make test-docker` stays in the gate (Plan 7d-2's rule).

## 5. Out of scope

- The admin API's sign-in, which goes to 10a.
- TLS termination in serve.
- Everything moved in Section 1.

## 6. Size

| Part | Days |
|---|---|
| The listener, `publicURL`, headers, HTTP/1, the Host check and logs (D2, D3) | 1.5–2 |
| OIDC sign-in, flow cookies, the session, CSRF, return paths, fresh authentication (D4) | 4–4.5 |
| Deployment fields and their checks (D2, D4) | 1–1.5 |
| The policy in the coordinator and the actor roles (D5) | 1 |
| `Open`'s checks (D6) | 0.5 |
| Shared kill-switch routing and refusal mapping, and the control's pages (D7) | 2–2.5 |
| `Views.List`, `ClosedSince`, rows, the acts' forms and the minimal opening (D8) | 2.5–3 |
| The board page and `board.Status` (D9) | 1–1.5 |
| Templates, the stylesheet and accessibility basics (D10) | 1 |
| The fake IdP in the harness, and the proofs | 2.5–3 |
| Spec, roadmap and ADR amendments | 0.5 |
| **Total** | **18–21 days, about 3.6–4.2 person-weeks raw** |

**This is above the review's 2.5–3.0 figure for the cut scope.** The review's sum left out the items it added back as Important: the policy in the coordinator (R-e), `Open`'s checks, the shared routing, the full refusal mapping, the fake IdP in the harness, and the amendments. Taken one line at a time, I cannot make the honest figure smaller than about 3.6 raw. At the record's ratio of 1.3–1.5, that is about 4.7–6.3 executed. The author is told this with the plan. Under R-f, the stop is calendar and cost, and Section 8 gives the proposed numbers.

## 7. Questions for the re-review

1. Does v2 close C1, C2 and I1–I15? Name any that are only partly closed.
2. Are D5's policy in the coordinator and the fresh-authentication rule above C16 consistent, with no act that one front end allows and the coordinator refuses for a reason the person cannot see?
3. Is the size table honest, line by line?

## 8. The proposed revisit trigger (R-f)

These go to ADR-016 with the plan's approval, and the numbers are the author's to confirm.
- **Calendar time.** A plan whose execution runs past **5 calendar days** from its first task, or past **twice** its plan's calendar guess, whichever is later, stops for the author.
- **Cost.** A plan whose model spend, meaning live runs plus subagents where it can be measured, passes **twice** its plan's stated guess stops for the author.
- **Person-weeks** are still estimated and recorded on the roadmap, and reported at each branch's end. They no longer stop a plan.
- **The exit date** of 2027-04-30 is unchanged.
