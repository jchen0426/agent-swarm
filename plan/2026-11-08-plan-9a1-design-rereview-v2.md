> The Opus re-review of Plan 9a-1's design, version 2.

# Plan 9a-1 design v2: scoped re-review

Reviewer: Opus, 2026-11-08. Subject: `plan/backups/plan9a/design-v2.md`, against `design-review-v1.md` (2C/15I/16M).
Taken as binding: rulings R-a to R-f in v2's header. Not relitigated.

**Counts.** First-review findings: C1, C2 closed; 11 of 15 Important closed, 4 partly closed (I3, I6, I9, I13); Minors addressed, with M4 and M15 partly. New in v2: **0 Critical, 6 Important, 15 Minor.**

**Verdict.** v2 answers the first review well. The kill switch's control now follows the code (C1), the headers are in (C2), and fresh authentication is pinned down (I8). Three things need fixing before planning:
- **The CSRF check refuses every form.** v2's own `Referrer-Policy: no-referrer` makes browsers send `Origin: null` on every POST (N1).
- **R-e is not finished.** The opening has no actor in the coordinator. The on's role check has no stated place, and only one place covers the drain route. The fresh-authentication rule is a second copy of the policy (N2–N4).
- **R-f's trigger is unarmed.** v2 gives no calendar guess and no cost guess to measure against (N6).

---

## 1. Q1: is each first-review finding closed?

| Item | Verdict | Notes |
|---|---|---|
| **C1** drain on, preview | **Closed** | (a) D7 shares the routing: `setKillSwitch` → `switchNotHeld` → `recordOn`, plus `refusal()`. (b) The on is offered when the preview answers `draining`, `taking_over` or `unavailable`, with `After` read from the table. (c) Proof 8 covers it. I checked against `lifecycle.go:205-283`: a preview during a drain answers 409 `draining` (`:246-253`), and the web maps that to the labeled on. Q2 below covers the role check on this route. |
| **C2** CSP, framing | **Closed**, but the fix introduces N1 | D3 has the headers, `nosniff`, `no-store`, HSTS left to the proxy, GET-only hops to the IdP, and proof 4. |
| **I1** loopback off | **Closed** by R-d and the amendment | Section 3 amends 5.1 and 8.3. Its 9.1 amendment does not mention the break-glass exception (M-d). |
| **I2** principal/responders | **Closed** | D6 has every rule, applies to the admin path, and proof 7 covers it. See M-e for the redaction-rule side effect. |
| **I3** one policy | **Partly closed** | The item acts and the switch go through `app.May` under the lock. Missing: (1) the opening's role check cannot be in the coordinator: `OpenInput` has no actor (`coordinator.go:225-232`, `:276`). See N2. (2) The on's check has no stated place, and only `checkSwitch` covers the drain route (N4). (3) The fresh-authentication predicate duplicates the policy in the web layer (N3). |
| **I4** admin actions fresh | **Closed** | v2 takes option (a) and states which acts count. The duplication is tracked under N3. |
| **I5** `/leaderz` | **Closed** | 200 while the lease is held, the drain included. It is served on the web listener, and the docs give the load balancer's recipe. Naming the leader needs a read that does not exist yet (M-g). The Host check breaks the probes (N5). |
| **I6** answer fields | **Partly closed** | D7 lists every field. Proof 8 does not cover `StayedUnstaffed` or `StayedUnstaffedUnread` (M-h). |
| **I7** keys, refusal codes | **Closed** | Per-act keys; the key is kept after `unavailable` or `taking_over`; the table is re-read after a 503; every code is mapped through the shared function. Small gaps: M-a, M-c, M-o. |
| **I8** fresh authentication | **Closed** | Points (a)–(g) are all in D4, with proofs for (a), (b) and (d). |
| **I9** subjects | **Partly closed** | `sso:` prefix, `admin` reserved, `subjectClaim`. v1's fix also said principals and responders are matched in the same prefixed form. v2 dropped that sentence. Admin-API openings and future inbound sources can still record a bare `bob`, which no web subject ever matches (M-f). |
| **I10** board page | **Closed** | D9 is cut to what `Assessment` holds (`assess.go:14-38` has `Converged`, `Score`, supporters/refuters and `Conclusion`). `board.Status` drops "confirming". |
| **I11** HTTP/1 | **Closed** | `SetHTTP1`, `MaxHeaderBytes`, `IdleTimeout`, the body cap, and the proof. |
| **I12** origins, redirects | **Closed** | `publicURL` is the single source, no forwarded header is read, the return-path rules and per-flow cookies are in, sign-out is a POST, and proofs are listed. The Host check needs a carve-out (N5). |
| **I13** carries | **Partly closed** | Section 1 moves most carries by name. Still missing: the release rule on model-call records (6b:233; say no 9a-1 surface shows them), approvers for the off going to Phase 1, and the sentence that notices are visible only in logs until 9a-2 (M-n). |
| **I14** size | **Closed**, re-tabled | Q3 checks the table line by line. |
| **I15** missing delay | **Closed** | `ClosesAtUnread`, one log line per item, and no kill-switch read per row. |
| **M1–M3, M5–M14, M16** | **Closed** | |
| **M4** IdP down | **Partly closed** | Lazy discovery is in. The runbook line is missing: "during an IdP outage, the only stop is the loopback token on the host". |
| **M15** proofs | **Partly closed** | Missing proofs: StayedUnstaffed and its unread flag, a viewer's on during a drain, and probes with a foreign Host (M-h). |

## 2. Q2: does the coordinator's policy agree with the fresh-authentication rule above C16?

I traced every act through both front ends against the code.

**Agrees:**
- **Loopback admin.** `adminActor` is `{admin, Admin:true}` (`lifecycle.go:36`). `Admin` passes `May` for every item act and passes the on and off checks. The loopback's openings carry no actor today, so nothing changes. The admin path loses nothing, as proof 6 says, apart from D6's new `Open` checks, which is intended.
- **Off.** The web layer checks freshness before it calls. `checkSwitch` refuses a non-admin's off, and its preview, with `admin_only` (`killswitch.go:436`).
- **Off by a non-admin during a drain.** `SetKillSwitch` answers `ErrNotHeld` at `:156`, before `checkSwitch`. `switchNotHeld` then answers `draining` (`lifecycle.go:258-266`), not `admin_only`. The person is told the wrong reason, but nothing is let through. Accept it, or have the web layer show admin-only first.

**Gaps:**
- **(a) The drain's `RecordKillSwitchOn`.** It gets the role check only if the check sits in `checkSwitch`. `RecordKillSwitchOn` calls `checkSwitch(true, a)` first (`killswitch.go:281`). `SetKillSwitch` calls it only after `holds()`. During a drain, every on reaches `recordOn` without `SetKillSwitch` checking anything. D5 says only "On: `Responder || Admin`", with no place given. If the check is written in `SetKillSwitch`'s body, or in `May` called from there, a viewer's on is recorded during every deploy. See N4.
- **(b) The opening.** D5 lists "An opening: `Responder || Admin`" under the coordinator's policy. `Open` takes no actor. `OpenInput` holds ID, type, title, source, routing, digest, principal and responders (`coordinator.go:225-232`), and the admin route sets `Source: person` (`admin.go:284`). So the opening's check is web-only, against R-e. Separately, D8's form lets the principal be someone else and has no responders field. A responder who opens for someone else therefore cannot promote or close the item they just opened: the coordinator answers `not_item_responder`. Nothing records who opened it. See N2.
- **(c) Fresh authentication as a second copy.** The web layer decides "not the item's principal or responder" from a view read outside the lock. Read literally, D5 sends a non-admin who is not on the item to re-authenticate, and the coordinator then refuses them with `not_item_responder`: the web allowed the step, the coordinator refused. If the view cannot be read, the rule is undefined. See N3.
- **(d) Previews.** D5 does not say whether `May` covers previews. A viewer's on preview reaches `wouldKill`, which takes the coordinator's lock and may read boards (`killswitch.go:487-512`), from any signed-in viewer. Refuse both item-act previews and the on's preview for actors without the role.
- **(e) Texts.** `refusedTexts` (`lifecycle.go:60-67`) has no entries for `not_item_responder` or `responder_only`. The shared mapping falls back to "… is not allowed now", which is the opaque answer Q2 asks about. Add both texts to the shared map.

## 3. Q3: the size table, line by line

| Line | v2 | My view | Why |
|---|---|---|---|
| Listener, `publicURL`, headers, HTTP/1, Host check, logs | 1.5–2 | **Fair** | Includes the fourth listener in `listen()` (`listeners.go:31-62`, both runner branches), the `addresses` file (Contract C2), and keeping the web server serving through the drain, as the admin server does. |
| OIDC, flow cookies, session, CSRF, return paths, fresh authentication | 4–4.5 | **Fair** | |
| Deployment fields | 1–1.5 | **Low: 1.5–2** | There are about 15 keys, counting `listeners.web`, `publicURL`, `services` and `timeZone`, which D4's table omits. The first review said +0.5–1, and v2 did not take it. |
| Policy and actor roles | 1 | **Fair** | Existing tests use admin actors. Only `killswitch_test.go:1002`, a non-admin `alice`, changes. Add 0.25 if N2's `OpenInput.Actor` and `Opened.By` are taken: a record field with a version. |
| `Open`'s checks | 0.5 | Fair | |
| Shared routing, mapping, control pages | 2–2.5 | **Low: 2.5–3** | The routing writes HTTP responses today (`refuseCode`, `answer`). Turning it into a returned value, while `lifecycle_test.go`'s 749 lines keep passing, is real work. The lease-holder read for the standby page (M-g) is not counted anywhere. |
| `Views.List`, `ClosedSince`, rows, forms, opening | 2.5–3 | **Fair, upper end** | `ClosedSince` needs both a Postgres store and a memory store, plus a new light fold. MACing the keys adds a little (M-c). |
| Board page and `board.Status` | 1–1.5 | Fair | |
| Templates, stylesheet, accessibility | 1 | **Low: 1.5–2** | About 9 pages: no role, items, board, switch, off, preview/commit pairs, answers, standby, errors. Plus a dark theme and 200% text. |
| Fake IdP in the harness, and the proofs | 2.5–3 | **Low: 3–3.5** | `__Host-`/`Secure` cookies and the Host check need https at `publicURL`. Go's `cookiejar` does not send Secure cookies over http, so the harness needs a TLS stub proxy in front of serve (M-k). |
| Amendments | 0.5 | Fair | |
| **Total** | **18–21** | **19.5–23.5 raw, about 3.9–4.7 pw; 5.1–7.0 executed at 1.3–1.5** | |

**Verdict.** v2's figure is honest in kind, and about 1.5–2.5 days low in four lines. v2 is right that the first review's 12.5–15 for the cut scope left out the Important items that review added back: the reviewer's sum was wrong. Under R-f, person-weeks no longer stop the plan, so the figure is for the record. What R-f does need is missing (N6).

## 4. New issues

### Important

**N1. `Referrer-Policy: no-referrer` makes every POST carry `Origin: null`, and D4 refuses that.**
- **Evidence.** D3 (v2:68) sets `no-referrer` on every response. D4 (v2:119) refuses a POST whose `Origin` is not `publicURL`'s origin. The Fetch standard's "append a request Origin header" step serializes the origin as `null` for any non-GET/HEAD, non-CORS request whose referrer policy is `no-referrer`. Chrome and Firefox both do this, and a form's request takes its document's policy. So every act, every on, every off and sign-out are refused. The first harness run will show it.
- **Fix.** Use `Referrer-Policy: same-origin` on every page; it sends `Origin` on same-origin POSTs. Keep `no-referrer` only on `/auth/callback`'s response, which should be a bare 303 to the return path, so that `code` and `state` leave the address bar at once. Add a proof that a form POST from a served page carries a matching Origin and is accepted.

**N2. The opening is outside the coordinator's policy, the opener can be locked out, and nobody is recorded as the opener.**
- **Evidence.** D5 (v2:147) and D8 (v2:225); `coordinator.go:225-232`, `:276`, `:315-321`. `item.Opened` has no field naming who opened. The admin route records `Source: person` (`admin.go:284`).
- **Fix.** Pick one:
  - **(a)** Add `OpenInput.Actor`. `Open` calls `May(actor, open, nil)` and records `Opened.By`, a body-version change. The admin route passes `adminActor`. Inbound sources pass a source actor.
  - **(b)** State that the opening's role check is web-only, and why.

  Either way:
  - A non-admin's principal is fixed to themselves. Only an admin may name another person, and that already needs fresh authentication (D5).
  - Or the opener is added to the responders.

  Spec 9.4's "who did what" favours (a).

**N3. The fresh-authentication rule is a second copy of the policy.**
- **Evidence.** D5 (v2:149-152). The web layer judges "not principal or responder" from a view read outside the lock. A non-admin who is not on the item is sent to re-authenticate and is then refused. A view that cannot be read leaves the rule undefined.
- **Fix, simplest.** The web layer sets `Actor.Admin = session.admin && fresh(auth_time)`. The coordinator then enforces spec 9.1 under its lock, with no second copy. For an admin session, the web maps `admin_only` and `not_item_responder` to a "re-authenticate" link.
- **Fix, otherwise.** `May` returns its grounds (member, admin override or refused). The web layer calls the same function on the view, to decide both the buttons it shows and whether to ask for freshness, and requires freshness when the view cannot be read.

**N4. The on's role check must sit in `checkSwitch`, or the drain route skips it.**
- **Evidence.** `killswitch.go:156` (`ErrNotHeld` before any check), `:161`, `:281`, `:427-438`. During a drain, every on goes `switchNotHeld` → `recordOn` → `RecordKillSwitchOn`. Only `checkSwitch` runs on that path.
- **Fix.** Add a case to `checkSwitch`, before the reason check: `on && !a.Actor.Responder && !a.Actor.Admin` → `responder_only`. That covers the preview too (Q2(d)). Add a proof: a viewer's on during a drain is refused with `responder_only` and writes no row.

**N5. The Host check refuses the load balancer's health probes.**
- **Evidence.** D2: a foreign `Host` gets 400 (v2:54), and the health routes are on the same listener (v2:56-60). Common load balancers, AWS ALB among them, probe with `Host` set to the target's IP, and that cannot be changed. Every target would be marked unhealthy, and the web would be unreachable.
- **Fix.** Exempt `GET /healthz`, `/readyz` and `/leaderz` from the Host check. They read no cookie and set none. Add a proof for each.

**N6. R-f's trigger has nothing to measure against.**
- **Evidence.** Section 8 (v2:330-331) stops a plan at twice "its plan's calendar guess" and twice "its plan's stated guess" of cost. Section 6 gives neither, only days.
- **Fix.** Add to Section 6 a calendar guess (days from first task to branch end, subagent-driven) and a model-spend guess (live runs plus subagents), each with its basis, for example 7b-1 and 7b-2 as executed. This applies R-f; it does not reopen it.

### Minor

- **M-a.** D8 (v2:224) commits item acts with "the preview's `After`". `After` belongs to the kill switch alone (`acts.go:38-40`). Drop it for item acts.
- **M-b.** A close whose preview answers `unavailable` cannot be committed, which is C1's shape for "stop an item". Offer the close labeled "consequences cannot be counted right now", as D7 does for the on.
- **M-c.** The minted key comes back through a hidden field, so the server must accept a key from the client to keep it across a retry. That contradicts "never accepted" (v2:222). Fix: MAC the key, bound to the subject, the act and the item (the opening for openings), and refuse a key without a valid MAC. Opening ids are `ItemID(tenant, key)` (`admin.go:282`).
- **M-d.** Section 3's 9.1 amendment says every admin action needs fresh authentication, but R-d's break-glass token acts with none. Add "except through the loopback token (Sections 5.1 and 8.3)".
- **M-e.** Checking subjects with the always-applying redaction rules (v2:100, 163) refuses every email subject in a deployment whose always rule redacts emails. With no current revision, it cannot run at all. For subjects at sign-in, check length and `config.Unseen` only, or state both outcomes.
- **M-f.** (I9) Say that principals and responders are recorded in the subject form, `sso:<value>`, from every source. Say that the admin API documents this, and that D6 refuses bare values that collide with the reserved `admin`.
- **M-g.** Naming the leader needs a new read of `leases.holder` (`0008_leases.sql:16`; `postgres/lease.go` has no such read), and a size. The holder is `d.Holder`, a name, not a URL. Say so on the page.
- **M-h.** Proofs to add: `StayedUnstaffed` with states, and `StayedUnstaffedUnread`; a viewer's on during a drain (N4); probes with a foreign Host (N5); a served form's Origin (N1).
- **M-i.** Groups: Entra emits object ids, and drops the claim on overage (`_claim_names`). Google emits no groups claim. Say that group values are matched as given (ids allowed), and that a missing or overage claim lands on "no role" with words naming the cause.
- **M-j.** One key both seals the session (AES-GCM) and signs the CSRF token (HMAC). Derive subkeys with HKDF. Also state that a failed key read fails closed and is logged.
- **M-k.** The harness needs https in front of serve, for `__Host-` cookies and the Host check. Plan a stub TLS proxy, and size it (Q3).
- **M-l.** Gather every new field into D4's table (`listeners.web`, `web.publicURL`, `web.services`, `web.timeZone`). `web.services` can drift from the registry's routing. Check it against the current revision when the form renders, or derive it from the registry.
- **M-m.** `ClosedSince`, "items whose latest record is a close" (v2:209): other kinds of record can follow a close. Use "an `item_closed` with no later `item_reopened`", as `Active` does (`postgres/records.go:63-69`), and take that close's `recorded_at`.
- **M-n.** (I13, M4) Add four lines: the 6b:233 release rule (no 9a-1 surface shows model-call records); approvers for the off go to Phase 1; until 9a-2, notices are visible only in logs; and M4's runbook line.
- **M-o.** D7 re-reads the table after a 503 or a stale answer. Do the same after the switch's 409 `not_held` or `draining`, as when the drain's on loses the lease (`lifecycle.go:276-280`), and after `taking_over`.
