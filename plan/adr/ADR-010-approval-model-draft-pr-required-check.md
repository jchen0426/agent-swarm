# ADR-010 Approval model: draft pull request plus a required check

| | |
|---|---|
| Status | Moved out of the platform layer |
| Date | 2026-10-10; moved 2026-10-12; revised 2026-10-13 |
| Related | Spec Section 4.4; Phase 1 backlog issues 6–10 |

## The original decision

For mitigations made through pull requests on the deployment repository:

- a bot opened each pull request as a draft;
- the on-call engineer submitted it for approval;
- a required check, published by the platform, passed only if a code owner other than the requester approved the exact generated commit, the target had not changed since, and the operation was still pending;
- repository rules narrowed what the bot could do.

## Why it moved

It is a deployment's choice of how two-person control works on one code host, not a platform decision. The platform keeps only the Phase 1 principles of spec Section 4.4:

- an approval covers one exact call;
- the gateway persists its intent before a write;
- an abandoned operation must be unable to take effect;
- one target has at most one unfinished write.

A deployment that mitigates through pull requests keeps its own draft of this model in its deployment profile. The later draft drops the draft step, because the bot opens a pull request only after the platform approval.

## Open

Whether a required check becomes required hardening for Phase 1 is decided with the Phase 1 design. Backlog issues 6–10 cover who publishes the check, how it learns operation state, how human pull requests pass, and how it is fenced.

## History

- **2026-10-10:** accepted.
- **2026-10-12:** moved out of the platform layer (spec Section 16).
- **2026-10-13:** the reference to the first deployment's draft replaced, as deployment profiles left this repository.
