# Data Model: HR Leave Management Module for v4

**Feature**: 028-hr-v4 | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

Database `hr` (TimescaleDB/PostgreSQL), role `hr_app` LOGIN NOBYPASSRLS. Every table
has `tenant_id uuid NOT NULL` and a row-level security policy
`tenant_id = current_setting('app.tenant_id')::uuid` (as signing, `0003_rls.sql`).
IDs are UUIDv7 (`store.NewID`). User IDs are auth user IDs (`text`). Days are
`numeric(6,1)` (half days); money-like float arithmetic is never used for balances.
Timestamps `timestamptz`; leave dates are calendar `date`s (no time zone).

## hr_absence_types

| column | type | rules |
|---|---|---|
| id | uuid PK | |
| tenant_id | uuid | RLS |
| name | text | 1–100 chars; unique per tenant (`lower(name)`) |
| description | text | ≤ 2000 |
| color | text | `#rrggbb` |
| icon | text | ≤ 64, kit icon name |
| sort_order | int | 0–10000 |
| active | bool | default true |
| metadata | jsonb | object, ≤ 8 KiB |
| deducts | bool | deducts from allowance |
| requires_approval | bool | |
| pool_id | uuid NULL → hr_pools | ON DELETE RESTRICT; a type in a pool must deduct |
| carry_over_cap | numeric(6,1) NULL | NULL = no cap, 0 = nothing carried; ignored when in a pool (pool cap applies) |
| requires_signing | bool | |
| signing | jsonb NULL | required when requires_signing: `{template_id, template_name, employee_party, approver_party, fields: {<template field id>: <leave value key>}}` |
| created_at/by, updated_at/by | | |

Leave value keys (closed set, FR-030): `employee_name`, `department`, `absence_type`,
`start_date`, `end_date`, `days`, `reason`, `approver_name`, `today`. Dates are
rendered `dd.mm.yyyy` (as v3's "Today"), days with one decimal only when a half day.

## hr_pools

| column | type | rules |
|---|---|---|
| id | uuid PK | |
| tenant_id | uuid | |
| name | text | 1–100, unique per tenant |
| description, color, icon | | as types |
| carry_over_cap | numeric(6,1) NULL | |
| created_at/by, updated_at/by | | |

Member types are `hr_absence_types.pool_id` (a type is in at most one pool — FR-002).

## hr_allowances

| column | type | rules |
|---|---|---|
| id | uuid PK | |
| tenant_id | uuid | |
| user_id | text | auth user |
| year | int | 2000–2099 |
| absence_type_id | uuid NULL → hr_absence_types | exactly one of type/pool (CHECK) |
| pool_id | uuid NULL → hr_pools | |
| total_days | numeric(6,1) | 0–365 |
| carried_over | numeric(6,1) | 0–365 |
| used_days | numeric(6,1) | ≥ 0 (CHECK) — changed only by charges/refunds |
| carried_from_run | uuid NULL | last carry-over run that set carried_over (idempotency, FR-048) |
| notes | text | ≤ 2000 |
| created_at/by, updated_at/by | | |

Unique `(tenant_id, user_id, year, absence_type_id)` and `(tenant_id, user_id, year,
pool_id)` (NULLS DISTINCT with the CHECK). Remaining = total + carried − used (may be
negative after an overdraw, FR-035).

## hr_requests

| column | type | rules |
|---|---|---|
| id | uuid PK | |
| tenant_id | uuid | |
| user_id | text | requester (from the verified caller or chosen by an HR admin) |
| absence_type_id | uuid → hr_absence_types | RESTRICT |
| start_date, end_date | date | end ≥ start; span ≤ 366 days |
| half_start, half_end | bool | half_end only when end > start or with half_start false |
| days | numeric(6,1) | computed (FR-011), > 0 |
| status | text | `pending`, `awaiting_signing`, `approved`, `rejected`, `cancelled`, `revoked` |
| reason | text | ≤ 1000 |
| notes | text | ≤ 2000 |
| approver_ids | text[] | computed at creation and on department changes (FR-020) |
| reviewed_by | text | |
| reviewed_at | timestamptz NULL | |
| review_notes | text | ≤ 1000 (rejection notes / revoke reason) |
| submission_id | uuid NULL | signing submission of the current attempt |
| signing_note | text | last signing outcome note (`declined`, `expired`, `cancelled`) |
| signing_started_at | timestamptz NULL | for reconciliation (FR-034) |
| version | int | optimistic lock for status changes |
| created_at/by, updated_at | | |

Indexes: `(tenant_id, user_id, start_date)`, `(tenant_id, status, start_date)`,
`(tenant_id, start_date, end_date) WHERE status IN ('pending','awaiting_signing','approved')`
(calendar + overlap), GIN on `approver_ids`, unique `(submission_id)` where not null.
Overlap is enforced in the create transaction with `SELECT … FOR UPDATE` on the
person's active requests plus an exclusion constraint
`EXCLUDE USING gist (tenant_id WITH =, user_id WITH =, daterange(start_date, end_date, '[]') WITH &&) WHERE (status IN ('pending','awaiting_signing','approved'))`
(btree_gist).

### Status machine (FR-016)

```text
pending ──approve (no signing)──▶ approved ──revoke──▶ revoked
   │   ──approve (signing)──▶ awaiting_signing ──completed──▶ approved
   │                              │ ──declined/expired/cancelled──▶ pending
   │                              │ ──reject──▶ rejected   ──cancel──▶ cancelled
   ├──reject──▶ rejected
   └──cancel──▶ cancelled        approved ──cancel (not started)──▶ cancelled
delete: rejected | cancelled only
```

## hr_request_charges

| column | type | rules |
|---|---|---|
| request_id | uuid → hr_requests ON DELETE CASCADE | |
| tenant_id | uuid | |
| allowance_id | uuid → hr_allowances ON DELETE RESTRICT | |
| year | int | |
| days | numeric(6,1) | > 0 |

PK `(request_id, allowance_id)`. Written in the same transaction as the charge;
refund returns exactly these rows and deletes them (FR-014, FR-015). An allowance with
charges cannot be deleted (FR-004).

## hr_departments

| column | type | rules |
|---|---|---|
| id | uuid PK | |
| tenant_id | uuid | |
| parent_id | uuid NULL → hr_departments | RESTRICT; no cycles (checked in the service under a tenant lock) ; depth ≤ 10 |
| name | text | 1–100, unique among siblings |
| manager_id | text NULL | auth user |
| created_at/by, updated_at/by | | |

## hr_members

| column | type | rules |
|---|---|---|
| tenant_id | uuid | |
| user_id | text | PK with tenant |
| department_id | uuid NULL → hr_departments | RESTRICT |
| display_name | text | cache of auth profile (refreshed), for lists and e-mails |
| active | bool | false once the user left the tenant (FR-044) |
| synced_at | timestamptz | |

Rows are created on first use (assignment, allowance, request) and by the member sync.

## hr_holidays

| column | type | rules |
|---|---|---|
| id | uuid PK | |
| tenant_id | uuid | |
| date | date | for recurring: the month/day is used every year from `date`'s year on |
| name | text | 1–100 |
| recurring | bool | |
| created_at/by | | |

Unique `(tenant_id, date)`.

## hr_signing_outcomes

| column | type | rules |
|---|---|---|
| tenant_id | uuid | |
| submission_id | uuid | PK with tenant |
| outcome | text | `completed`, `declined`, `cancelled`, `expired` |
| source | text | `event` or `reconcile` |
| event_id | text | stream entry id (event) |
| request_id | uuid NULL | request it was applied to (NULL = ignored) |
| result | text | `applied`, `ignored_status`, `ignored_unknown` |
| received_at | timestamptz | |

The primary key makes every outcome apply at most once (SR-005).

## hr_tenants (no RLS — ids only)

| column | type | rules |
|---|---|---|
| tenant_id | uuid PK | tenant with HR data (inserted on the first write of a tenant) |
| stream_cursor | text | last processed `platform:events:<tenant>` entry id ('' = start from the tail) |
| cursor_at | timestamptz | |
| members_synced_at | timestamptz NULL | last member sync (FR-044) |

The only table without row-level security: platform-scoped work (event consumer,
signing reconciliation, member sync) needs the list of tenants, and the runtime role
cannot read across tenants of RLS tables (research D4). It holds IDs and cursors only.
The cursor is advanced in the same transaction as the outcome it records
(at-least-once delivery + idempotent apply = exactly-once effect).

## hr_carryover_runs

| column | type | rules |
|---|---|---|
| id | uuid PK | |
| tenant_id | uuid | |
| source_year | int | |
| created_at/by | | actor or `system` (task) |
| created | int | allowances created |
| updated | int | allowances updated |

## hr_mail_outbox

| column | type | rules |
|---|---|---|
| id | uuid PK | |
| tenant_id | uuid | |
| key | text | `hr.*` template key (contracts/cross-module.md) |
| user_id | text | recipient (e-mail resolved at send time via auth Contacts) |
| vars | jsonb | template variables (personal data, no secrets; RLS-protected, deleted after send) |
| attempts | int | ≤ 5 |
| next_at | timestamptz | backoff |
| last_error | text | code only |

Written in the request's transaction, drained by the in-process mail worker (FR-054).
Rows are deleted when sent or after the last attempt (the failure is audited).

## hr_audit_events

Same shape as signing `0002_audit.sql` (closed vocabulary,
contracts/audit-events.md), no personal free text.
