---
title: Reference
summary: Request, allowance, absence type and department fields, request statuses, permissions, roles, API routes, errors and limits.
kind: reference
order: 60
tags: [hr, reference, requests, permissions, api, limits]
---
Days are numbers in half-day steps (`3`, `2.5`). Dates are calendar dates
(`2027-01-08`).

## Request fields

| Field | Meaning |
|---|---|
| `id` | request ID |
| `user_id`, `user_name` | the person taking leave |
| `absence_type_id` | the absence type |
| `start_date`, `end_date` | first and last day |
| `half_start`, `half_end` | half first day, half last day |
| `days` | working days counted: Monday to Friday, without public holidays, minus half days |
| `status` | see [request statuses](#request-statuses) |
| `reason` (up to 1000 characters), `notes` (up to 2000) | from the requester |
| `approver_ids`, `approver_names` | who may decide it now |
| `reviewed_by`, `reviewer_name`, `reviewed_at`, `review_notes` | the decision |
| `signing`, `signing_note` | a signed document exists or is being signed; `declined`, `expired` or `cancelled` after a failed signing |
| `can_review`, `can_edit`, `can_cancel` | what the caller may do |

A request may span at most 366 calendar days. Days are charged per calendar
year: each year needs an allowance.

## Request statuses

| Status | Meaning | Days charged |
|---|---|:-:|
| `pending` | waiting for the approver | |
| `awaiting_signing` | approved by the approver, leave form being signed | |
| `approved` | approved (or needed no approval) | yes |
| `rejected` | rejected by the approver | |
| `cancelled` | withdrawn by the requester or HR, or the person left the tenant | |
| `revoked` | approved, then revoked; the days were refunded | |

Pending, awaiting-signing and approved requests of a person may not
overlap.

## Allowance and balance fields

An allowance has `user_id`, `year` (2000–2099), either `absence_type_id` or
`pool_id`, `total_days` (0–365), `carried_over` (0–365), `used_days`,
`remaining` (total + carried − used; negative after an overdraw) and
`notes`.

A balance line (**My leave → Balance**) adds `pending`: the days of open
requests in that year.

## Absence type and pool fields

| Field | Meaning |
|---|---|
| `name` | 1–100 characters, unique in the tenant |
| `description`, `color` (`#rrggbb`), `icon`, `sort_order` (0–10000) | display |
| `active` | can be requested |
| `deducts` | approved days are charged to an allowance |
| `pool_id` | the pool whose allowance is charged (deducting types only) |
| `carry_over_cap` | 0–365 days, empty for no cap (types with their own allowance, and pools) |
| `requires_approval` | false: approved on submission |
| `requires_signing`, `signing` | signed leave document; see [signed leave forms](guides/signed-forms.md) |

## Department fields

A department has `name` (1–100 characters), `parent_id`, `manager_id` and
`member_ids`. A person is a member of at most one department. The tree is at
most 10 levels deep (`limits_hr.max_department_depth`) and may not contain a
cycle.

## Permissions and roles

| Permission | Allows |
|---|---|
| `hr:calendar` | see the team calendar (who is away, with the absence type) |
| `hr:request` | request leave for yourself, see your requests and balance; managers also review for their departments |
| `hr:read` | read every request, balance, allowance, absence type, department and holiday |
| `hr:manage` | manage absence types, pools, allowances, departments and holidays; request for and review anyone; carry-over; backups |

| Module role | Permissions |
|---|---|
| HR administrator | all four |
| HR viewer | `hr:calendar`, `hr:read` |
| HR employee | `hr:calendar`, `hr:request` |
| HR calendar viewer | `hr:calendar` |

The platform's built-in roles also receive HR permissions: `owner` and
`admin` those of HR administrator; `operator` and `member` those of HR
employee; `auditor` those of HR viewer.

Manager rights come from the department tree, not from a permission: the
manager of a department and the managers above it see its members' requests
and balances and review their requests.

## API

The browser API is under `/api/hr/v1`, through the gateway. Changing requests
need the `X-CSRF-Token` header. The module checks ownership and manager
relations on top of the route permission; IDs you may not see answer `404`.

| Route | Permission |
|---|---|
| `GET /me`, `GET /people`, `GET /calendar` | `hr:calendar` |
| `GET /absence-types`, `GET /absence-types/{id}` | `hr:calendar` |
| `POST /absence-types`, `PUT`, `DELETE /absence-types/{id}`, `GET /absence-types/{id}/signing-check`, `GET /signing/templates` | `hr:manage` |
| `GET /pools`, `GET /pools/{id}` | `hr:read` |
| `POST /pools`, `PUT`, `DELETE /pools/{id}` | `hr:manage` |
| `GET /allowances`, `GET /allowances/{id}`, `GET /balance/{user_id}` | `hr:calendar` (visibility checked) |
| `POST /allowances`, `PUT`, `DELETE /allowances/{id}`, `POST /carry-over/preview`, `POST /carry-over` | `hr:manage` |
| `GET /requests` (`view=mine\|review\|all`), `GET /requests/{id}` | `hr:calendar` (visibility checked) |
| `POST /requests`, `POST /requests/preview`, `PUT`, `DELETE /requests/{id}`, `POST /requests/{id}/cancel` | `hr:request` |
| `POST /requests/{id}/approve`, `/reject`, `/revoke` | `hr:calendar` (routing checked) |
| `GET /requests/{id}/signed-document` | `hr:calendar` (relation checked) |
| `GET /departments` | `hr:calendar` |
| `POST /departments`, `PUT`, `DELETE /departments/{id}`, `PUT /departments/{id}/members` | `hr:manage` |
| `GET /holidays` | `hr:calendar` |
| `POST /holidays`, `PUT`, `DELETE /holidays/{id}`, `POST /holidays/import` | `hr:manage` |
| `GET /stats` | `hr:read` |
| `GET /stream` (server-sent events) | `hr:calendar` |
| `POST /backup/export`, `POST /backup/import` (`mode=skip\|overwrite`) | `hr:manage` |

`POST /holidays/import` takes `text/plain` lines `YYYY-MM-DD,name[,yearly]`
and `?dry_run=true` to check only. The backup export is a gzip archive of all
HR data of the tenant; signed documents stay in the signing module.

### Errors

Refusals answer `{"reason": "...", "field": "...", "detail": {...}}`.

| Status | `reason` | Typical cause |
|---|---|---|
| 403 | `forbidden`, `self_review`, `not_routed` | missing permission; deciding your own request; not an approver |
| 404 | `not_found` | no such item, or not visible to you |
| 409 | `overlap` | another pending or approved request on these days |
| 409 | `insufficient_allowance`, `no_allowance` | not enough days, or no allowance for a year |
| 409 | `invalid_transition` | the action does not fit the request's status |
| 409 | `in_use`, `not_empty`, `duplicate`, `cycle` | deleting something in use; a non-empty department; a duplicate name or allowance; a department loop |
| 422 | `validation`, `zero_days` | a field is invalid (`field` names it); the dates contain no working day |
| 422 | `signing_template_invalid`, `signer_inactive` | signing settings no longer match the template; a signer is not accepted |
| 503 | `signing_unavailable`, `temporarily_unavailable` | the signing module or the member directory cannot be reached |

## Limits

| Limit | Default |
|---|---|
| calendar days of one request | 366 (`limits_hr.max_request_days`) |
| calendar range shown at once | 93 days (`limits_hr.max_calendar_days`) |
| list page size | 100 (`limits_hr.max_page_size`) |
| holiday import | 64 KiB, 500 lines |
| backup | 256 MiB |
| department depth | 10 |
