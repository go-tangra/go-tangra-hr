# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub's
**"Report a vulnerability"** (Security → Advisories) on
`github.com/go-tangra/go-tangra-hr`. Do not open a public issue. Include the
affected version or commit, the impact and a reproduction. You will get an
acknowledgement within five working days.

## Threat model (summary)

Trust boundaries: browser → gateway → hr (platform token), hr →
auth/notification/signing/scheduler (mesh, SPIFFE mTLS), scheduler → hr (task
execution), signing → hr (platform event stream in Valkey), and hr →
database / Valkey.

| Threat | Mitigation |
|---|---|
| Reviewing one's own or someone else's request | Every review re-checks, inside the request's transaction, that the reviewer is a routed approver (department manager up the tree, or an HR administrator) and not the requester; others get 404 |
| Reading other people's leave | Requests and balances are visible to the owner, their approvers and HR readers only; the calendar shows names, dates and type (no reasons or notes) to holders of `hr:calendar` |
| Cross-tenant reads or references | FORCE row-level security on every table, tenant-qualified composite foreign keys (FK checks ignore RLS), tenant from the platform token; isolation tested on the parameterised routes |
| Overdrawing allowances, double-booking | Days are charged per year inside the approval transaction with row locks; overlapping requests are refused by an exclusion constraint; concurrent approvals are tested against TimescaleDB |
| Forged signing outcomes | Outcomes are read only from the tenant's platform event stream (written by mesh-authenticated modules), decoded with size and shape bounds, matched to `(tenant, submission_id)`, and applied only to requests awaiting signing; reconciliation asks the signing module directly over mTLS |
| Lost signing outcomes | The stream cursor is persisted per tenant; `hr:reconcile-signing` queries the state of every request that has waited longer than `reconcile.older_than_minutes` |
| Abuse of the signing module API | Only `svc/hr` may call `signing.v1.ModuleSubmissions` (callee policy); every call names the tenant, source `hr` and an idempotency key; the signing module re-validates the template and the parties |
| Malicious imports | Holiday imports and backup archives are size- and line-bounded, parsed strictly (fuzzed) and validated before anything is written |
| Leaking reasons or personal data | E-mails carry names, dates and links (no reasons in subjects); events carry ids and states only; the audit vocabulary refuses reason/note/name/e-mail/secret detail keys; e-mail addresses are resolved at send time and never stored |

## Supported versions

Only the latest `v4.x` release receives security fixes.
