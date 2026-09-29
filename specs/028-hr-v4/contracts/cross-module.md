# Contract: Cross-module integration

## Signing events consumed (research D2)

Stream `platform:events:<tenant>` for each tenant in `hr_tenants`; entries
`{to, type, data, at}` (go-tangra-signing-v4/internal/stream/hub.go:206). HR decodes
only these types (data ≤ 4 KiB, one JSON object, bounded ids — as dns `decode.go`):

| type | data | HR action |
|---|---|---|
| `signing.submission.completed` | `{submission_id, template_id, final_version, audit_trail}` | request awaiting signing → approved, charge days (FR-035) |
| `signing.submission.cancelled` | `{submission_id, template_id, reason_code}` (`cancelled`/`declined`) | → pending, `signing_note` = reason_code (FR-036) |
| `signing.submission.expired` | `{submission_id, template_id}` | → pending, `signing_note = expired` |

Matching: `(tenant, submission_id)` → `hr_requests.submission_id`; unknown ids and
requests not awaiting signing are recorded as ignored. A submission cancelled by HR
itself (reason codes `hr_*`) is recorded as ignored because the request already left
`awaiting_signing`.

## Events published by HR

See contracts/hr-api.md (`hr.request.changed`, `hr.review.changed`,
`hr.calendar.changed`; ids and status only).

## Auth (`Profiles`, `Authorization`)

| call | use |
|---|---|
| `Authorization/Check`, `BatchCheck` | permissions of the caller in the tenant |
| `Authorization/RegisterPermissions` | manifest registration (module roles) |
| `Profiles/ListMembers` | active members of the tenant (people list, member sync) |
| `Profiles/Lookup` | display names (≤ 100 per call) |
| `Profiles/Contacts` | e-mail at send time (outbox worker) |

auth `deploy/policy.yaml` (and go-tangra-docker `policies/auth.yaml`): rule
`hr-profiles` for `svc/hr` on Lookup, ListMembers, Contacts; `svc/hr` added to the
Authorization/Check and registration rules.

## Notification (`Notifier/Send` via `notifyclient.SendKey`)

System templates in go-tangra-notification-v4 `internal/notify/systemtemplates.go`
(English body + inline Bulgarian line, as `signing.*`). Variables are HTML-escaped by
the template engine; no reasons in subjects.

| key | to | variables |
|---|---|---|
| `hr.request_submitted` | each approver | ApproverName, EmployeeName, AbsenceType, StartDate, EndDate, Days, Reason, ReviewURL |
| `hr.request_approved` | employee | EmployeeName, AbsenceType, StartDate, EndDate, Days, ReviewerName, RequestURL |
| `hr.request_rejected` | employee | EmployeeName, AbsenceType, StartDate, EndDate, Days, ReviewerName, ReviewNotes, RequestURL |
| `hr.request_revoked` | employee | EmployeeName, AbsenceType, StartDate, EndDate, Days, ReviewerName, Reason, RequestURL |
| `hr.signing_failed` | employee, approver | RecipientName, EmployeeName, AbsenceType, StartDate, EndDate, Outcome, RequestURL |
| `hr.allowance_overdrawn` | HR administrators (tenant owners/admins with hr:manage) | EmployeeName, AbsenceType, Year, Remaining, RequestURL |

Links are built from `links.portal_base_url` (`/hr/requests/<id>`). The signing
invitations for signing-required types are sent by the signing module. Notification policy `modules-send` gains `svc/hr`. HR
administrators for `hr.allowance_overdrawn` are resolved by listing members and
batch-checking `hr:manage` (≤ 100 per BatchCheck).

## Scheduler

| type | scope | payload schema | default cron |
|---|---|---|---|
| `hr:carry-over` | tenant | `{"source_year": int 2000–2098 (optional)}` | `30 0 1 1 *` |
| `hr:reconcile-signing` | platform | `{"older_than_minutes": int 1–1440 (optional, default 10)}` | `*/15 * * * *` |
| `hr:sync-members` | platform | `{}` | `0 * * * *` |

Registered with `schedulerclient.Registrar`, executed by `taskexec.NewServer`
(callee rule `scheduler-execute`); scheduler `discovery.static.hr: ["hr:9925"]` and
`modules-register` gains `svc/hr`. The two platform tasks are created by a platform
administrator after deploy (quickstart); carry-over by each tenant's HR administrator.
