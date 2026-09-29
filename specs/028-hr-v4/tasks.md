# Tasks: HR Leave Management Module for v4

**Feature**: 028-hr-v4 | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

Tests are MANDATORY and precede implementation in every phase (Constitution IV).
`[P]` = parallelizable (different files, no dependency on an unfinished task);
`[US#]` = user story. Paths are relative to `hr-service-v4/` unless a repository is
named. Module `github.com/go-tangra/go-tangra-hr/v4`. Mirror go-tangra-signing-v4
(app, httpapi, store, repo, audit, manifest, tasks, contacts client, UI). v3 sources
are in `../hr-service` (branch `main`). Release tasks (push, tags, images, rename,
deploy) are marked **(release)** and are not done in this change.

Story order: US1 → US6 → US5 → US2 (MVP) → US4 → US3 → US7 → US8. Cross-module work
(Phase 3) runs in parallel with Phases 4–7 and must finish before US3.


> **Implementation note (2026-09-29):** several planned test files were
> consolidated — HTTP contract/route/isolation checks live in
> `internal/httpapi/api_test.go` (every declared OpenAPI route has a handler,
> no undeclared route) and `pkg/hrmanifest/manifest_test.go` (gateway limits);
> request create/review/list/calendar tests in `internal/requests/requests_test.go`;
> signing start/outcome tests in `internal/requests` + `internal/signing`;
> restore and decode fuzz targets in `internal/backup/backup_test.go` and
> `internal/consumer/decode_test.go`; member sync in `internal/tasks/tasks_test.go`.
> Cross-module branches: signing `028-module-api`, auth `028-hr`, notification
> `028-hr-mail`, scheduler `028-hr`, go-tangra `028-hr` (stack), go-tangra-docker
> `028-hr` (from `v4`).
> T042 uses 261 one-day requests × 4 concurrent approvals (1,044) against one
> 100-day allowance (one person can hold at most 261 weekday requests a year).
> T053: an empty cursor starts at the tail; a cursor whose entry was trimmed
> resumes at the oldest remaining entry (nothing re-applied — outcomes are
> idempotent per submission); missed outcomes are repaired by reconcile.

## Phase 1: Setup

- [x] T001 Orphan branch `v4` of hr-service as worktree `hr-service-v4`; copy `specs/028-hr-v4/`; `go.mod` (module …/go-tangra-hr/v4, go 1.26.3, toolchain 1.26.8), `.gitignore`, `.dockerignore`.
- [x] T002 [P] `Makefile` (lint, vuln, test, test-integration, cover, fuzz, ui-build, build, build-ui, image), `scripts/coverage-gate.sh` (≥ 80 %, 100 % authz/routing/leavedays/charges/signingmap), `scripts/vulncheck.sh`, `Dockerfile` (ui → build -tags ui → alpine, `hrsvc`), `.github/workflows/ci.yaml` (go vet/test, UI lint/unit, image `ghcr.io/go-tangra/go-tangra-hr`, semver tags, no latest; triggers on the default branch).
- [x] T003 [P] UI scaffold `ui/` from go-tangra-signing-v4/ui (package `go-tangra-hr-ui`, `@go-tangra/ui` ^4.2.3, vite base `/m/hr/`, remote `hr`, `layer(utilities)`, icon safelist test), `ui/embed.go`, `ui/embed_stub.go`.

## Phase 2: Foundational

### Tests (write first, must fail)
- [x] T004 [P] `internal/config/config_test.go` — defaults, unknown keys refused, required db/valkey/gateway/discovery (signing, auth, notification), production guards, limits bounds, `links.portal_base_url`, consumer (block, batch) and reconcile (`older_than`) bounds, warnings.
- [x] T005 [P] `internal/authz/authz_test.go` — permissions `hr:calendar`, `hr:request`, `hr:read`, `hr:manage`; Require for user/system/service actors; owner, approver, manager relations; not-found masking. 100 %.
- [x] T006 [P] `internal/audit/audit_test.go` — vocabulary (contracts/audit-events.md), unknown actions refused, redaction of reason/notes/name/email keys, writer flush/close/drop.
- [x] T007 [P] `internal/leavedays/leavedays_test.go` + `leavedays_fuzz_test.go` — Mon–Fri count, single and recurring holidays (incl. 29 Feb), half start/end rules, zero-day ranges, span limit, split by year, tenths arithmetic. 100 %.
- [x] T008 [P] `internal/routing/routing_test.go` — approvers for member/manager/top manager/no manager/inactive manager/unassigned; self excluded; HR-admin fallback; depth and cycle guard. 100 %.
- [x] T009 [P] `internal/charges/charges_test.go` — plan charges per year for type and pool allowances, insufficient/no allowance, refund exactly the charged rows, never below zero, lock ordering. 100 % (pure core).
- [x] T010 [P] `pkg/hrmanifest/manifest_test.go` — routes from OpenAPI (every route has a permission or `member`), permissions, roles (HR administrator/HR viewer/Employee/Calendar viewer), grants (owner/admin → administrator, auditor → viewer, member → employee), abilities, nav.
- [x] T011 [P] `tests/contract/openapi_test.go` — document valid; every declared route mounted; no undeclared route; streamed routes carry `x-freya-timeout`, import carries `x-freya-max-body-bytes` (≤ 1 GiB, timeout ≤ 300); error envelope.

### Implementation
- [x] T012 `internal/config/config.go` — framework config inline + db (dsn, migrate_dsn), valkey, gateway, mesh_enroll, discovery, task_scheduler, notification, signing (service), links, limits, consumer, reconcile, outbox.
- [x] T013 [P] `internal/authz/authz.go` — permissions, Checker (auth `Authorization/Check`/BatchCheck ≤ 100), Require, relation helpers, platform admin rule as signing (platform tenant admin/owner).
- [x] T014 [P] `internal/audit/audit.go` — closed vocabulary, redaction, async writer.
- [x] T015 [P] `internal/leavedays/leavedays.go`, `internal/routing/routing.go`, `internal/charges/plan.go` (pure).
- [x] T016 `internal/store/` — pool, tenant scope (`SET app.tenant_id`), ids; migrations `0001_schema.sql` (all tables of data-model.md, btree_gist exclusion), `0002_audit.sql`, `0003_rls.sql` (every table except `hr_tenants`); `cmd/hrsvc bootstrap` runs goose as postgres.
- [x] T017 `internal/repo/` contract + `repodb/` (pgx) + `memstore/` (fake with the same semantics incl. overlap and charge CHECKs).
- [x] T018 [P] `internal/people/` — auth Profiles client (ListMembers paging, Lookup ≤ 100, Contacts), name cache with TTL, fake.
- [x] T019 `api/openapi/hr.yaml` (contracts/hr-api.md) + `pkg/hrmanifest/` (manifest, permissions, roles, grants, abilities, nav) + `internal/httpapi/` skeleton (validation, errors, auth context, `/health`, `/me`, `/people`, SSE `/stream` with copied `internal/stream` hub).
- [x] T020 `internal/app/` — wiring, permission/role registration loop, gateway lease, metrics, graceful shutdown; `cmd/hrsvc/main.go`; `deploy/policy.yaml`, `deploy/container.yaml`.

## Phase 3: Cross-module prerequisites (parallel with Phases 4–7)

- [x] T021 [P] go-tangra-signing-v4 (branch `028-module-api`): `sdk/` module — `sdk/api/proto/signing/v1/module.proto` (contracts/signing-module-api.md), buf config, generated code, `sdk/pkg/signingclient` (+ fake); tests first.
- [x] T022 go-tangra-signing-v4: tests first `internal/grpcapi/module_test.go` (bufconn) — caller must be svc/hr, other services PermissionDenied; tenant UUID validated; ListTemplates active only, no PDF/values; CreateAndSend validates template/parties/members/prefill, stores source/source_ref/created_by, idempotency replay returns the same id, send failure deletes the draft; Get/Cancel/Delete/FinalDocument only for source=hr (user submissions NotFound); FinalDocument only completed, streamed in chunks; audit entries.
- [x] T023 go-tangra-signing-v4: `0004_source.sql`, store/repo fields, `submissions.CreateForService`/service subject paths, `internal/grpcapi/module.go`, registration in `internal/app`, `deploy/policy.yaml` rule `hr-module-api`, UI badge "Source: HR" on submissions; memstore parity; CHANGELOG/README.
- [x] T024 [P] go-tangra-auth (branch `028-hr`): `deploy/policy.yaml` rule `hr-profiles` (Lookup, ListMembers, Contacts) + svc/hr in Authorization/Check and registration rules; policy test if present.
- [x] T025 [P] go-tangra-notification-v4 (branch `028-hr-mail`): tests first (templates render with required variables, missing variable refused, `hr.` key prefix accepted only for svc/hr); six `hr.*` system templates (contracts/cross-module.md, English + Bulgarian line); policy `modules-send` gains svc/hr.
- [x] T026 [P] go-tangra-scheduler-v4 (branch `028-hr`): policy `modules-register` gains svc/hr; example `discovery.static.hr`.
- [x] T027 [P] go-tangra/deploy/stack (branch `028-hr`): compose `hr-token`, `hr` (depends lcm, gateway, timescaledb, valkey, signing), `configs/hr.yaml`, init-db (database `hr`, role `hr_app`, timescaledb + btree_gist), Valkey ACL user `hr`, gateway `-allow svc/hr=/api/hr;hr`, scheduler discovery, consumer policies (signing, auth, notification, scheduler).

## Phase 4: User Story 1 — Absence types, pools, allowances (P1)

**Goal**: administrators configure types, pools and allowances; balances are correct.
**Independent test**: US1 acceptance scenarios; quickstart Scenario 1.

### Tests (write first, must fail)
- [x] T028 [P] [US1] `internal/catalog/types_test.go` — CRUD, unique name, color/icon/metadata validation, pool membership (one pool, pool ⇒ deducts), deactivate, delete refused in use; audit.
- [x] T029 [P] [US1] `internal/catalog/pools_test.go` — CRUD, delete refused with members.
- [x] T030 [P] [US1] `internal/allowances/allowances_test.go` — CRUD, one per person/year/type-or-pool, type XOR pool, delete refused with charges, balance lines (total, carried, used, pending, remaining; pool member ids), visibility (own, manager, hr:read).
- [x] T031 [P] [US1] `internal/httpapi/catalog_test.go` + `allowances_test.go` — routes, permissions, 404 masking, error codes.

### Implementation
- [x] T032 [US1] `internal/catalog/{types,pools}.go`, `internal/allowances/{allowances,balance}.go`, handlers.
- [x] T033 [P] [US1] UI views `absence-types` (list + drawer with behaviours; signing section placeholder), `pools`, `allowances` (filters, person picker, type/pool switch), `balance` component.

## Phase 5: User Story 6 — Public holidays (P2, needed by the day count)

- [x] T034 [P] [US6] `internal/catalog/holidays_test.go` + `import_fuzz_test.go` — CRUD, unique date, recurring expansion per year, import parsing (≤ 64 KiB, ≤ 500 lines, `YYYY-MM-DD,name[,yearly]`, dry run, line errors), audit.
- [x] T035 [US6] `internal/catalog/holidays.go` + import + handlers; UI view `holidays` (year filter, import dialog with dry-run result).

## Phase 6: User Story 5 — Departments and routing (P2, needed by approvals)

- [x] T036 [P] [US5] `internal/departments/departments_test.go` — CRUD, nesting, move with cycle/depth guard, delete refused (members/children), manager must be an active member, member assignment (one department), routing recompute of pending requests on every change; audit.
- [x] T037 [US5] `internal/departments/departments.go` + handlers; `hr_members` upkeep; UI view `departments` (tree, manager picker, member assignment).

## Phase 7: User Story 2 — Requests and approval (P1) 🎯 MVP

**Goal**: request → routed approval → atomic charge; e-mails.
**Independent test**: US2 acceptance scenarios; quickstart Scenarios 3–4.

### Tests (write first, must fail)
- [x] T038 [P] [US2] `internal/requests/create_test.go` — identity from caller (body user only with hr:manage), server day count (holidays, half days), zero days, overlap (incl. exclusion constraint race), no/insufficient allowance, auto-approve with charge, approvers stored, preview without write.
- [x] T039 [P] [US2] `internal/requests/review_test.go` — approve/reject/revoke/cancel/update/delete transitions (FR-016, FR-017), self_review, not_routed, hr:manage override, optimistic version, charges and exact refunds, two-year split.
- [x] T040 [P] [US2] `internal/requests/list_test.go` — views mine/review/all, filters, paging, detail visibility (SR-004), managers over sub-departments.
- [x] T041 [P] [US2] `internal/outbox/outbox_test.go` — enqueue in tx, worker batch/backoff/≤ 5 attempts, recipients via Contacts, missing e-mail skipped + audited, never blocks requests; templates keys/vars match contracts.
- [x] T042 [P] [US2] `tests/integration/concurrency_test.go` (`//go:build integration`) — 1,000 concurrent approvals against one allowance never overdraw (SC-005); overlap exclusion under concurrency.

### Implementation
- [x] T043 [US2] `internal/charges/tx.go` (lock allowances by id, write charges), `internal/requests/{create,review,list}.go`, handlers.
- [x] T044 [US2] `internal/outbox/` worker + `internal/app` wiring; `internal/events` publisher (`hr.request.changed`, `hr.review.changed`, `hr.calendar.changed`).
- [x] T045 [P] [US2] UI views `requests` (mine/all), `review` (To review with badge), `RequestForm` with live `DayPreview` (days, balance after, approvers), request detail with actions.

## Phase 8: User Story 4 — Team calendar (P1)

- [x] T046 [P] [US4] `internal/requests/calendar_test.go` — range ≤ 93 days, department/person filters, statuses shown, payload has no reason/notes, holidays included, inactive members hidden.
- [x] T047 [US4] Calendar query + handler (single range query, names from cache).
- [x] T048 [P] [US4] UI `calendar` view + `Timeline` component (week/2 weeks/month, today, navigation, grouped by department, bars by type/status, hatched pending, awaiting-signing mark, shaded named holidays, hover card, drag-to-request on allowed rows); vitest for layout math; perf check 200 people/1,000 bars (SC-006).

## Phase 9: User Story 3 — Signing workflow (P1)

**Goal**: approval through a signed document, durable outcomes.
**Independent test**: US3 acceptance scenarios; quickstart Scenarios 5–7, 11.
**Needs**: T021–T023.

### Tests (write first, must fail)
- [x] T049 [P] [US3] `internal/signingmap/signingmap_test.go` — settings validation against a template (active, two distinct parties, field ids exist and text-valued, closed value keys), rendering (dates `dd.mm.yyyy`, days with halves, names, department, today). 100 %.
- [x] T050 [P] [US3] `internal/signing/start_test.go` (fake signingclient) — approve → awaiting_signing + CreateAndSend (employee then approver, prefill, source_ref, idempotency key per attempt), failure keeps pending and reports reason, template invalid/signer inactive refused; cancel/reject → Cancel; delete → Delete; revoke keeps the document; download streams only for allowed viewers.
- [x] T051 [P] [US3] `internal/consumer/decode_test.go` + `decode_fuzz_test.go` — only `signing.submission.*`, data ≤ 4 KiB, single object, bounded ids, unknown types skipped.
- [x] T052 [P] [US3] `internal/signing/outcome_test.go` — completed → approved + charge once (overdraw allowed + `hr.allowance_overdrawn`), declined/cancelled/expired → pending with note + `hr.signing_failed`, request not awaiting / unknown / other tenant ignored and recorded, duplicate outcome no-op, cursor advanced in the same tx.
- [x] T053 [P] [US3] `tests/integration/consumer_test.go` — restart replay from persisted cursor; trimmed cursor falls back to tail; reconcile task repairs a missed completion and a missed decline; exactly one charge overall (SC-002, SC-003).

### Implementation
- [x] T054 [US3] `internal/signingmap/`, `internal/signing/{client,start,outcome,download}.go`; absence type signing settings save/validate; `GET /signing/templates`, `/absence-types/{id}/signing-check`, `/requests/{id}/signed-document` (streamed).
- [x] T055 [US3] `internal/consumer/` (per-tenant XREAD from `hr_tenants.stream_cursor`, block/batch from config, backoff, metrics lag) + `internal/tasks/reconcile.go` (`hr:reconcile-signing`, platform) + executor/registrar wiring (`internal/app/scheduler.go`).
- [x] T056 [P] [US3] UI `SigningSettings` in the absence type drawer (template picker, party mapping, field mapping table, check result), request detail signing state, signing note, download button.

## Phase 10: User Story 7 — Carry-over (P2)

- [x] T057 [P] [US7] `internal/allowances/carryover_test.go` — min(unused, cap) per type/pool, no cap/zero cap, create vs update next year, idempotent re-run, preview equals run, 500 people < 30 s (SC-007), audit.
- [x] T058 [US7] `internal/allowances/carryover.go`, `/carry-over/preview` + `/carry-over`, `internal/tasks/carryover.go` (`hr:carry-over`, tenant); UI carry-over dialog on the allowances page (preview table, apply).

## Phase 11: User Story 8 — Roles, statistics, backup, leavers (P3)

- [x] T059 [P] [US8] `internal/httpapi/roles_test.go` — role × route matrix (manifest grants), tenant isolation per route.
- [x] T060 [P] [US8] `internal/backup/backup_test.go` + `restore_fuzz_test.go` — export streams all HR tables of the tenant within `max_backup_bytes`; import skip/overwrite into the caller's tenant only; submission references kept; malformed archives refused; audit.
- [x] T061 [P] [US8] `internal/people/sync_test.go` — `hr:sync-members`: leavers inactive, pending/awaiting requests cancelled (+ submissions), managers' routings recomputed, names refreshed.
- [x] T062 [US8] Statistics endpoint + UI `stats`; `internal/backup/` + handlers + UI backup panel; `internal/tasks/sync.go` (`hr:sync-members`, platform).

## Phase 12: Polish & cross-cutting

- [x] T063 [P] Leak test `tests/integration/leak_test.go` — full flow with known reasons/notes/names; assert none in logs, audit, events (stream) or backups metadata (SC-008).
- [x] T064 [P] Isolation suite `tests/integration/isolation_test.go` — tenant B against every tenant-A route → 404 (SC-004); employee against colleagues' details → 404.
- [x] T065 [P] UI polish — icons in the kit safelist (unit test), dark theme check, a11y e2e (`ui/tests/e2e/a11y.spec.ts`) for all routes, empty/error states.
- [x] T066 [P] `README.md`, `SECURITY.md` (threat model of spec SR, signing API trust, durability design), `deploy/README.md`.
- [x] T067 `make lint vuln cover` green (≥ 80 %, 100 % security packages); `govulncheck` clean; signing repo green too; quickstart automated section passes.
- [x] T068 go-tangra-docker (branch `v4`): `docker-compose.yaml.example` (hr, hr-token, volume, Valkey user, gateway allow), production overlay, `configs/hr.yaml`, `policies/hr.yaml` + consumer policy updates (signing, auth, notification, scheduler), `init-db.sql`, `.env.example` (`HR_IMAGE`, `HR_DB_PASSWORD`), `scripts/prod-init.sh` (SERVICES), scheduler discovery, PRODUCTION/README sections.

## Phase 13: Release **(release)**

- [ ] T069 **(release)** signing: tag `sdk/v4.0.0`, drop TEMP replace, release v4.1.0 (module API + migration).
- [ ] T070 **(release)** auth, notification (minor: hr templates), scheduler patch.
- [ ] T071 **(release)** hr: push old `main` as `v3`, merge `v4` into `main` (`-s ours` + PR); rename the GitHub repository to `go-tangra-hr` **after user confirmation** (research D11); drop TEMP replaces; tag `v4.0.0`; image.
- [ ] T072 **(release)** Stack/prod: DB + role + btree_gist, Valkey user, allow-list, policies, pins; deploy signing then hr; platform tasks `hr:reconcile-signing`, `hr:sync-members`; tenant task `hr:carry-over`; UI smoke of quickstart Scenarios 1–12 (operator sign-in).

## Dependencies & sequencing

Setup → Foundational → US1 → US6 → US5 → US2 (MVP) → US4 → US3 → US7 → US8 → Polish.
Phase 3 starts after Phase 1; T021–T023 (signing API) must finish before US3, T024
(auth policy) before T018 is exercised against the stack, T025 (templates) before the
outbox is exercised end to end, T026 before task registration in the stack.
Within a phase, tests precede implementation.

## Parallel execution examples

- T004–T011 in parallel (independent test files).
- T021/T024/T025/T026/T027 in parallel (five repositories); T022→T023 sequential.
- US6 (T034/T035) and US5 (T036/T037) in parallel after US1.
- UI tasks (T033, T045, T048, T056) alongside backend handlers once the OpenAPI (T019) is fixed.

## Implementation strategy

MVP = Phases 1, 2, 4–7 (+ T024/T025): types, pools, allowances, holidays, departments,
requests with routed approval, charges and e-mails. Then the calendar (US4), the
signing workflow with the signing module API (US3), carry-over (US7), roles/backup/
leavers (US8), polish, release.
