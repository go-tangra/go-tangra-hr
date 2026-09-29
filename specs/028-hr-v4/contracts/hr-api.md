# Contract: HR browser API (through the gateway)

OpenAPI 3.1 document `api/openapi/hr.yaml` (source of the gateway manifest via
`x-freya-*` extensions, as every v4 module). Base path `/api/hr/v1` (paths below are relative to it). JSON, errors in the
platform envelope `{error: {code, message, field?}}`. Every route declares its
permission (`x-freya-permission`) or `member`; the module re-checks permissions,
ownership and manager relations (SR-003, SR-004). Objects of another tenant, or ones
the caller may not see, answer `404 not_found`.

## Permissions

| permission | meaning |
|---|---|
| `hr:calendar` | calendar (person, type, dates, days, status of everyone) |
| `hr:request` | request leave for oneself; read/edit/cancel own requests; own balance |
| `hr:read` | read all requests, balances, allowances, types, pools, departments, holidays, statistics |
| `hr:manage` | manage types, pools, allowances, departments, holidays; request for anyone; approve/reject/revoke any other person's request; carry-over; backup |

Manager rights (approve routed requests, read their departments' requests and
balances) come from `hr_departments.manager_id`, not from a permission.

## Routes

| method | path | permission | notes |
|---|---|---|---|
| GET | /me | member | `{user_id, permissions, manages:[department ids], department}` for the UI |
| GET | /people | hr:calendar | active members `{user_id, name, department_id}`; `?department=&q=` |
| GET | /calendar | hr:calendar | `?from=&to=` (≤ 93 days) `&department=&user=` → `{people, events:[{id,user_id,type_id,start,end,half_start,half_end,days,status}], holidays}` |
| GET | /absence-types | hr:calendar | active only unless `hr:read` (`?all=true`) |
| POST/PUT/DELETE | /absence-types[/{id}] | hr:manage | 409 `in_use` on delete (FR-004) |
| GET | /absence-types/{id}/signing-check | hr:manage | re-validates template, parties, mapped fields against signing |
| GET | /signing/templates | hr:manage | proxied from signing module API: `{id,name,parties:[{id,name}],fields:[{id,name,type}]}` |
| GET/POST/PUT/DELETE | /pools[/{id}] | read: hr:read; write: hr:manage | |
| GET | /allowances | hr:read (or manager for own departments) | `?user=&year=&type=&pool=&page=` |
| POST/PUT/DELETE | /allowances[/{id}] | hr:manage | 409 `duplicate`, `in_use` |
| GET | /balance/{user_id} | own, manager, hr:read | `?year=` → lines `{type_or_pool, name, color, total, carried, used, pending, remaining, member_type_ids}` |
| GET | /requests | hr:request | `?view=mine|review|all&user=&department=&type=&status=&from=&to=&page=`; `all` needs hr:read (managers: their departments) |
| POST | /requests | hr:request | `{absence_type_id, start_date, end_date, half_start, half_end, reason, notes, user_id?}` (`user_id` ≠ caller needs hr:manage) → 201 request with computed days, approvers |
| POST | /requests/preview | hr:request | same body → `{days, balance_after, approvers:[names], overlaps:bool}` (no write) |
| GET | /requests/{id} | owner, approver, manager, hr:read | details incl. reason/notes |
| PUT | /requests/{id} | owner, status pending | `{reason, notes}` |
| POST | /requests/{id}/approve | routed approver or hr:manage, not owner | `{notes?}`; signing types → `awaiting_signing` |
| POST | /requests/{id}/reject | routed approver or hr:manage, not owner | `{notes}` |
| POST | /requests/{id}/revoke | routed approver or hr:manage, not owner | `{reason}` |
| POST | /requests/{id}/cancel | owner (or hr:manage) | |
| DELETE | /requests/{id} | owner or hr:manage; rejected/cancelled | deletes the submission in signing |
| GET | /requests/{id}/signed-document | owner, reviewer, manager, hr:manage | streams `application/pdf` (`Content-Disposition: attachment`), 409 `not_signed` |
| GET/POST/PUT/DELETE | /departments[/{id}] | read: hr:calendar; write: hr:manage | tree; 409 `not_empty`, `cycle` |
| PUT | /departments/{id}/members | hr:manage | `{add:[user ids], remove:[user ids]}` |
| GET/POST/PUT/DELETE | /holidays[/{id}] | read: hr:calendar; write: hr:manage | `?year=` |
| POST | /holidays/import | hr:manage | `text/csv` or text, `YYYY-MM-DD,name[,yearly]`, ≤ 64 KiB, ≤ 500 lines; `?dry_run=true` |
| POST | /carry-over/preview | hr:manage | `{source_year}` → changes per person |
| POST | /carry-over | hr:manage | `{source_year}` → run summary |
| GET | /stats | hr:read | counts + today's absentees |
| POST | /backup/export | hr:manage | streamed archive (`x-freya-timeout` 300) |
| POST | /backup/import | hr:manage | archive (`x-freya-max-body-bytes` 256 MiB), `?mode=skip|overwrite` |
| GET | /stream | member | SSE of the tenant's platform events for this user (live refresh, as signing `/v1/stream`) |
| GET | /health | public | liveness for the gateway |

## Error codes

`validation` (field), `not_found`, `forbidden`, `overlap`, `insufficient_allowance`,
`no_allowance`, `zero_days`, `invalid_transition`, `self_review`, `not_routed`,
`in_use`, `duplicate`, `not_empty`, `cycle`, `signing_unavailable`,
`signing_template_invalid`, `signer_inactive`, `not_signed`, `conflict` (stale
version).

## Events published (`platform:events:<tenant>`)

| type | payload | target |
|---|---|---|
| `hr.request.changed` | `{request_id, status}` | requester + approvers (users) |
| `hr.review.changed` | `{count}` | an approver (their "To review" badge) |
| `hr.calendar.changed` | `{from, to}` | broadcast |

IDs and status codes only (SR-007).
