# Research: HR Leave Management Module for v4

**Feature**: 028-hr-v4 | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

References are `file:line` in the repositories under `/home/jadmin/projects/go-tangra/`.
"v3" is `hr-service` (branch `main`).

## Findings (v3 and v4 as they are)

| # | Finding | Evidence |
|---|---------|----------|
| F1 | v3 signing flow: approve → `awaiting_signing`, `CreateSubmission`+`SendSubmission` (sequential, Employee then Approver, prefill `Name2`/`TotalDays`/`StartDate`/`EndDate`/`Today`), rollback to pending on failure; completion via Redis pub/sub `signing.submission.completed` → approve + deduct; revoke → `CancelSubmission` in a goroutine; delete → `DeleteSubmission`; PDF via plain HTTP `GET /api/v1/signing/templates/pdf?key=` returned as base64 data URL. | hr-service/internal/service/leave_service.go (approveWithSigning, RevokeLeaveRequest, GetSignedDocumentUrl); internal/client/signing_client.go; internal/event/{subscriber,handler}.go |
| F2 | v3 balance = total + carried − used; atomic `SELECT … FOR UPDATE` deduct/refund; refund floored at 0; `deducted_allowance_id` recorded; pool allowances keyed by pool; whole leave charged to the start year. | hr-service/internal/data/leave_allowance_repo.go:192-386; internal/service/leave_helpers.go |
| F3 | v3 trusts `user_name`, `user_email`, `org_unit_name`, `days` from the request body; no self-approval check; `ListLeaveRequests` open to every `hr.request.view` holder; cancel has no status check. | hr-service/protos/hr/service/v1/leave.proto (CreateLeaveRequestRequest); leave_service.go |
| F4 | v4 signing has no module API: only gateway and scheduler may call it; submissions carry no source/reference; `Create` requires the user permission `submissions:create`; prefill is keyed by template field id and validated. | go-tangra-signing-v4/deploy/policy.yaml; internal/submissions/submissions.go:135-238; internal/store/migrations/0001_schema.sql:50-76 |
| F5 | Module-to-module precedent: lcm `Certificates/Download` for deployer — proto in `go-tangra-lcm/sdk/v4`, tenant in the request body, caller = mTLS peer SPIFFE id (`authn.FromContext`), elevated service subject recorded for audit, one-caller policy rule; client `Freya.Client(ctx, "lcm")`. | go-tangra-lcm-v4/sdk/api/proto/lcm/v1/lcm.proto:51,83; internal/grpcapi/server.go:33-55; internal/issue/certificates.go:323; deploy/policy.yaml:27-31; go-tangra-deployer-v4/internal/app/app.go:207-214 |
| F6 | Event consumers (dns, deployer) XREAD `platform:events:<tenant>` from the tail with an in-memory cursor; tenants from config; events during downtime are lost by design. Entries `{to, type, data, at}`, `MaxLen` 10000; decoders cap `data` at 4 KiB. | go-tangra-dns-v4/internal/ipamsync/consumer.go:20-26,81-115; decode.go:56-80; go-tangra-signing-v4/internal/stream/hub.go:19,206 |
| F7 | Signing publishes `signing.submission.completed|cancelled(reason_code cancelled/declined)|expired` with ids only. | go-tangra-signing-v4/internal/events/events.go:18-58 |
| F8 | Valkey users are `~* &* +@all` per module; a new `hr` user is added in three compose files. | go-tangra-docker/docker-compose.yaml.example:25; docker-compose.production.yaml.example:47-60; go-tangra/deploy/stack/compose.yaml:23 |
| F9 | Scheduler task types: `taskexec.NewServer` + `schedulerclient.Registrar`; descriptor `{Type "<svc>:<action>", PayloadSchema, DefaultCron, Platform}`; the type prefix must equal the caller's service; scheduler `discovery.static.<svc>` and policy `modules-register`. | go-tangra-signing-v4/internal/app/scheduler.go:20-53; internal/tasks/tasks.go:54-73; go-tangra-scheduler-v4/internal/registry/registry.go:61; go-tangra-docker/configs/scheduler.yaml:19-29 |
| F10 | Notification system templates are Go values in the notification module (English + inline Bulgarian), seeded without overwriting edits; key namespace must equal the caller's service; mesh rule `modules-send`. | go-tangra-notification-v4/internal/notify/systemtemplates.go:18-250; internal/notify/send.go:141-148; deploy/policy.yaml:13-16 |
| F11 | auth `Profiles.ListMembers` (active member ids), `Lookup` (names), `Contacts` (names + e-mail, signing only); no member-removal event on the bus. | go-tangra-auth/sdk/api/proto/auth/v1/auth.proto:217-255; go-tangra-docker/policies/auth.yaml:20-40 |
| F12 | Ports in use up to gRPC/HTTP 9915/9916 (signing) and admin 9860; free: 9925/9926, admin 9870. DB convention `<module>` + `<module>_app` NOBYPASSRLS. | go-tangra-docker/configs/*.yaml; go-tangra/deploy/stack/init-db.sql:53-58 |
| F13 | Module roles (019): manifest `Permissions`, `Roles`, `Grants` (owner/admin → full set), `Registration()` sent via `RegisterPermissions` in a retry loop. | go-tangra-signing-v4/pkg/signingmanifest/manifest.go:41-215; internal/app/permissions.go:44-60 |

## D1. Signing module API: a mesh-only gRPC service in a new signing SDK module

**Decision**: signing gains `github.com/go-tangra/go-tangra-signing/sdk/v4` with proto
`signing.v1.ModuleSubmissions` (contracts/signing-module-api.md): `ListTemplates`,
`CreateAndSend`, `GetSubmission`, `Cancel`, `Delete`, `FinalDocument` (server
streaming). Following lcm (F5): the tenant comes in the request, the caller is the
mTLS peer, the handler builds a *service subject* (`authz.Service(spiffe)` + tenant)
and signing's policy admits only `svc/hr` (SR-006). Submissions get `source` and
`source_ref` columns (migration `0004_source.sql`); service calls may only read or
change submissions whose `source` equals the caller's service name. `CreateAndSend`
records `created_by` = the approving user (given in the request, checked to be an
active tenant member through `Contacts`), so the approver also finds the submission
in signing's own UI and receives the completion mail as sender.

**Rationale**: the proven lcm pattern; no user token is needed, so the same API
serves the interactive approval, reconciliation and deletion; `source` scoping keeps a
compromised hr from touching users' own submissions.

**Alternatives**: on-behalf user token like warden (F5) — would need the approver to
hold `submissions:create` in signing and fails for reconciliation/deletion without a
user; calling signing's browser API through the gateway with the user's token — same
problem and puts HR on the public path; a generic "any module" API — widens the
attack surface for no current consumer.

## D2. Durable signing outcomes: persisted cursor + idempotent apply + reconciliation

**Decision**: HR consumes `platform:events:<tenant>` for every tenant in `hr_tenants`
with XREAD from the **persisted** cursor (`hr_tenants.stream_cursor`, '' = tail on
first start), handling only `signing.submission.*`. Each outcome is recorded in
`hr_signing_outcomes` (PK tenant+submission) and applied to the matching request in
the same transaction that advances the cursor. If the cursor was trimmed away
(`MaxLen` 10000, F6) or HR was down long, the platform task `hr:reconcile-signing`
(every 15 min) asks signing `GetSubmission` for every request awaiting signing longer
than `reconcile_after` (default 10 min) and applies the terminal state the same way
(FR-033, FR-034, SR-005).

**Rationale**: the existing consumers accept loss (F6); HR must not (US3 scenario 3).
Persisting the cursor removes loss in normal restarts; reconciliation covers trimming
and outages; the outcome table makes both paths idempotent.

**Alternatives**: consumer groups (XREADGROUP) — needs group management per tenant key
and still needs reconciliation for trimmed streams; signing calling HR back — couples
signing to HR and needs retries on the signing side; reconciliation only — minutes of
delay on every approval.

## D3. Approval routing computed at write time, re-checked at decision time

**Decision**: `approver_ids` is computed when a request is created and recomputed for
a person's pending requests when their department, a department's manager or parent
changes, or a member is deactivated (FR-020). The approve/reject/revoke handlers
re-derive the routing from the current tree and refuse if the caller is not in it
(unless `hr:manage` and not the owner) (SR-003). The stored list drives "To review",
notifications and the GIN-indexed query.

**Rationale**: fast "To review" and one e-mail target list, with the authoritative
check on every decision.

**Alternatives**: compute on read only — expensive list queries over the tree; stored
only — stale after reorganisations.

## D4. Tenant registry read under the system scope

**Decision**: `hr_tenants` (ids, stream cursor, sync time) lists the tenants using HR.
Like every hr table it is under RLS; the consumer, reconciliation and member sync read
it under the system scope (`app.system = 'on'`, the pattern signing's workers use,
go-tangra-signing-v4/internal/store/store.go `Scope.System`) and then open
tenant-scoped transactions per tenant. A row is inserted (`ON CONFLICT DO NOTHING`)
by the first write in a tenant.

**Rationale**: the runtime role is NOBYPASSRLS (F12); config-listed tenants (as dns,
F6) do not scale to every tenant using HR; the system scope is only set by trusted
worker code paths.

**Alternatives**: a table without RLS — an exception to the platform rule; a
BYPASSRLS maintenance role — larger blast radius; tenants from auth — HR only cares
about tenants that use it.

## D5. Members: auth for identity, HR for departments, periodic sync for leavers

**Decision**: the people list is `ListMembers` + `Lookup` (names) for the tenant;
e-mail addresses come from `Profiles.Contacts` at send time (auth policy gains
`svc/hr` for Lookup, ListMembers, Contacts). `hr_members` caches names and holds the
department. Because auth publishes no removal event (F11), the platform task
`hr:sync-members` (hourly) marks leavers inactive, cancels their pending and
awaiting-signing requests (cancelling submissions) and recomputes routing where they
were managers (FR-044).

**Alternatives**: an auth removal event — a new auth feature for one consumer (can
replace the sync later); on-demand checks only — leavers stay in calendars and
routings.

## D6. Day counting and year split in a pure package

**Decision**: `internal/leavedays` (pure, 100 % covered, fuzzed): working days Mon–Fri
minus holidays (single and yearly recurring) minus half days; split by calendar year
for charging (FR-011, FR-015). Values are `numeric(6,1)` in the DB and fixed-point
tenths (`int`) in Go — no floats (SC-005).

## D7. Charges table instead of `deducted_allowance_id`

**Decision**: `hr_request_charges(request, allowance, year, days)` written in the
charging transaction; refund returns exactly those rows (locks the allowances in id
order to avoid deadlocks). Pool vs type is resolved at charge time from the type's
pool (F2), per year.

**Rationale**: a request can be charged to two years' allowances (FR-015); v3's
single id could not express that.

## D8. E-mails through an outbox

**Decision**: e-mails are written to `hr_mail_outbox` in the same transaction as the
state change and drained by one in-process worker (batch, backoff, ≤ 5 attempts),
which resolves recipients through `Contacts` and calls `notifyclient.SendKey` with
`hr.*` keys (contracts/cross-module.md). Six system templates are added to the
notification module (F10), English with the Bulgarian line.

**Rationale**: e-mails must never block or fail a request and must survive restarts
(FR-054); v3 lost them in goroutines.

**Alternatives**: fire-and-forget goroutine (v3) — lost on restart; a scheduler task
per mail — scheduler is for scheduled work, not queues.

## D9. Signing settings stored on the absence type, validated against signing

**Decision**: `hr_absence_types.signing` = `{template_id, template_name,
employee_party, approver_party, fields: {field_id: value_key}}` with value keys from a
closed set. Saving validates against `ListTemplates` (template active, two distinct
parties, field ids exist and are text-valued); approval re-validates and refuses
with `signing_template_invalid` (edge cases). Rendering of values (dates `dd.mm.yyyy`,
days) is in `internal/signingmap` (pure).

**Rationale**: replaces v3's hard-coded field names (gap list).

## D10. Carry-over as a tenant task with a preview

**Decision**: `hr:carry-over` (tenant-scoped, payload `{source_year?}`, default
previous year; suggested cron `30 0 1 1 *`). One transaction per tenant; for each
person × type/pool with a source-year allowance: target = min(max(unused,0), cap);
create the next-year allowance (total copied) or set its `carried_over`; record
`carried_from_run`. Re-running replaces the carried value with the same result
(idempotent). The preview endpoint runs the same computation without writing.

## D11. Repository and module path

**Decision**: code on an orphan branch `v4` of `go-tangra/hr-service` (worktree
`hr-service-v4`), module `github.com/go-tangra/go-tangra-hr/v4` (the v3 module path's
name). At release the GitHub repository is renamed `go-tangra-hr` (GitHub redirects
the old URL), v3 is kept on branch `v3`, and images are
`ghcr.io/go-tangra/go-tangra-hr` — matching every other v4 module. The rename is a
release step that needs the user's confirmation.

**Alternatives**: keep `hr-service` — the only v4 repo outside the `go-tangra-*`
naming, and the module path would not resolve.

## D12. Ports and stack

gRPC 9925, HTTP 9926, admin 127.0.0.1:9870; database `hr`, role `hr_app`; Valkey user
`hr`; no object store (signed PDFs stay in signing). Discovery: signing, auth,
notification, scheduler, gateway.

## D13. Calendar privacy

The calendar endpoint returns person, type, dates, half days, days and status only;
details require the request read rules (SR-004). Other people's sick leave is shown
with its type (as v3). A per-type "private" flag (shown as "Absent") is out of scope
and can be added later.
