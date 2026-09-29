# Implementation Plan: HR Leave Management Module for v4

**Branch**: `028-hr-v4` (spec, v3 repo `hr-service`) → code on orphan branch `v4` of
hr-service (worktree `hr-service-v4`) | **Date**: 2026-09-29 |
**Spec**: [spec.md](./spec.md)

## Summary

A v4 hr module with v3 parity — absence types, allowance pools, yearly allowances,
leave requests with atomic charging, team calendar, balances, statistics and backup,
and the signing workflow (approval → sequential employee/approver submission →
automatic approval) — rebuilt on v4 foundations (SPIFFE mesh, RLS, enforced
permissions, module roles, audit) with the v3 defects fixed and the gaps closed:
departments with manager routing, public holidays, year-end carry-over, and e-mails
for every decision.

The signing link is a new mesh-only gRPC API in the signing module, admitted for
`svc/hr` only and scoped to submissions whose source is hr (research D1). Signing
outcomes arrive on the platform event bus and are applied from a persisted cursor,
idempotently, with a reconciliation task as a safety net (D2). Day counts, year
splits and value rendering are pure, fully tested packages (D6, D9).

## Technical Context

**Language/Version**: Go 1.26 (go 1.26.3, toolchain 1.26.8); UI TypeScript + Vue 3.

**Primary Dependencies**: go-tangra framework v4 (mesh, edge, policy, observe, stream
hub), auth SDK (verifier, permission checks, module roles, Profiles incl.
`Contacts`), portal SDK (gateway client), scheduler SDK (`taskexec`,
`schedulerclient`), notification SDK (`notifyclient.SendKey`), **new signing SDK**
(`signing.v1.ModuleSubmissions`, `signingclient`), `pgx/v5`, `goose/v3`,
`kin-openapi`, `valkey-go`. UI: `@go-tangra/ui` 4.2.3. No new third-party
dependencies.

**Storage**: TimescaleDB/PostgreSQL database `hr` (role `hr_app`, NOBYPASSRLS,
`btree_gist` for the overlap exclusion), tables per [data-model.md](./data-model.md);
Valkey (event stream read, SSE hub). No object store (signed PDFs stay in signing).

**Testing**: Go `testing`; `memstore` fake repository; fake signing module client,
fake auth profiles, fake notifier; bufconn tests for the task executor and for
signing's new gRPC service; fuzz: holiday import, backup restore, event decoding,
`leavedays`; contract test OpenAPI ↔ routes; integration suite (testcontainers
TimescaleDB + Valkey, `//go:build integration`): RLS isolation, concurrent approvals
(SC-005), overlap exclusion, event consumer restart/replay and reconciliation, carry-
over idempotency, backup round-trip; cross-module e2e in the dev stack (quickstart);
UI vitest (calendar layout, day preview) + lint; coverage ≥ 80 %, 100 % on
`internal/authz`, `internal/routing`, `internal/leavedays`, `internal/charges`,
`internal/signingmap`.

**Target Platform**: Linux container (alpine) in `go-tangra/deploy/stack` and
go-tangra-docker; behind the gateway; mesh identity by enrolment with lcm.

**Project Type**: Web service (Go, OpenAPI HTTP + gRPC task executor) +
Module-Federation UI; changes in signing (module API + SDK), auth (policy),
notification (templates + policy), scheduler (policy + discovery), stack repos.

**Performance Goals**: calendar month for 200 people / 1,000 absences < 2 s end to
end (SC-006; one query per range + names cached); carry-over 500 people < 30 s
(SC-007); approval incl. submission creation < 3 s; outcome applied < 1 min after the
event (SC-003).

**Constraints**: RLS on every row (workers use the system scope); no reasons, notes, names or
e-mail addresses in logs, events or audit (SR-007); days in tenths (no floats);
request span ≤ 366 days; calendar range ≤ 93 days; holiday import ≤ 64 KiB/500
lines; backup ≤ 256 MiB; ≤ 10 department levels.

**Scale/Scope**: eight user stories; ports gRPC 9925, HTTP 9926, admin 9870.

## Constitution Check

*GATE: passed before Phase 0 and re-checked after Phase 1 (one justified deviation,
see Complexity Tracking).*

- **I. Secure by Default** — refuses to start without db, valkey, gateway issuer,
  signing/auth/notification discovery; production refuses plaintext DB/Valkey and
  insecure enrolment; opt-outs are named config flags in `Warnings()`.
- **II. Zero Trust Service Communication** — SPIFFE mTLS with per-module policies:
  only the gateway relays the browser API, only the scheduler executes task types; the
  new signing API admits only `svc/hr` and only hr-sourced submissions
  (contracts/mesh-policies.md, signing-module-api.md).
- **III. Boundary Validation & Defense in Depth** — OpenAPI validation, permission +
  ownership + routing re-check in the module, RLS, overlap exclusion constraint,
  atomic charges with CHECKs, bounded event decoding, 404 masking, identity never from
  bodies (SR-002).
- **IV. Test-First** — tests precede implementation in every phase; negative tests
  for cross-tenant access, self-approval, unrouted approval, forged identity/days,
  replayed/foreign signing outcomes, other callers of the signing API; fuzz on every
  parser; 100 % on security/money-like packages.
- **V. Observability & Auditability** — closed audit vocabulary
  (contracts/audit-events.md); OTel metrics (requests by status, approvals, signing
  starts/outcomes by source, consumer lag, reconcile repairs, outbox backlog/failures);
  correlation ids across hr → signing.
- **VI. Supply Chain** — no new third-party dependency; new first-party signing SDK
  module, versioned and tagged like lcm's.
- **VII. Simplicity & Explicit Configuration** — one binary, one database; typed YAML
  with `KnownFields`; scheduled work in the scheduler; the only in-process loops are the
  event consumer and the mail outbox worker (both needed for latency; D2, D8).

## Project Structure

### Documentation (this feature)

```text
specs/028-hr-v4/
├── spec.md, checklists/requirements.md
├── plan.md, research.md, data-model.md, quickstart.md, tasks.md
└── contracts/{hr-api.md, signing-module-api.md, cross-module.md, audit-events.md, mesh-policies.md}
```

### Source Code

```text
hr-service (branch v4, worktree hr-service-v4) — module github.com/go-tangra/go-tangra-hr/v4
├── cmd/hrsvc/{main.go,version.go}              # + bootstrap (migrate) subcommand
├── api/openapi/{hr.yaml,embed.go}
├── internal/
│   ├── app/          # wiring, permissions/roles registration, gateway lease, workers, scheduler
│   ├── config/       # typed config (db, valkey, gateway, discovery, signing, notification, task_scheduler, links, limits, consumer, reconcile)
│   ├── authz/        # permissions, owner/manager/approver relations, 404 masking
│   ├── audit/        # closed vocabulary + writer
│   ├── leavedays/    # working days, holidays, half days, year split (pure)
│   ├── charges/      # charge/refund planning over allowances (pure core + repo tx)
│   ├── routing/      # approver computation over the department tree (pure)
│   ├── signingmap/   # absence type signing settings validation + value rendering (pure)
│   ├── people/       # auth Profiles client (ListMembers, Lookup, Contacts) + name cache + member sync
│   ├── catalog/      # absence types, pools, holidays (+ import)
│   ├── allowances/   # allowances, balances, carry-over (preview/run)
│   ├── departments/  # tree, members, routing recompute
│   ├── requests/     # create/preview/update/cancel/delete/approve/reject/revoke, lists, calendar
│   ├── signing/      # signing module client wrapper, start/cancel/delete/download, outcome apply
│   ├── consumer/     # platform stream consumer (persisted cursor, decode, apply)
│   ├── outbox/       # mail outbox worker (Contacts + SendKey)
│   ├── tasks/        # scheduler task types (carry-over, reconcile-signing, sync-members)
│   ├── events/       # hr.* publisher; stream/ (copied hub)
│   ├── backup/       # export/import
│   ├── metrics/
│   ├── store/        # pool, tenant scope, goose migrations, ids
│   ├── repo/         # contract; repodb/ (pgx), memstore/ (fake)
│   └── httpapi/      # OpenAPI-validated browser API, streamed PDF, SSE
├── pkg/hrmanifest/                             # gateway manifest, permissions, roles, grants, abilities, nav
├── ui/                                         # federated remote `hr`
│   └── src/{views/{calendar,requests,review,balance,absence-types,pools,allowances,departments,holidays,stats},
│            components/{Timeline,RequestForm,DayPreview,SigningSettings,DepartmentTree,PersonPicker},
│            api/, stores/, remote/}
├── deploy/{policy.yaml,container.yaml,README.md}
├── tests/{contract,integration}
├── Dockerfile, Makefile, scripts/{coverage-gate.sh,vulncheck.sh}, .github/workflows/ci.yaml
└── README.md, SECURITY.md

go-tangra-signing-v4 (branch 028-module-api)   sdk/ module (proto, gen, signingclient), grpc service,
                                              0004_source.sql, source scoping, policy hr-module-api, UI "Source: HR"
go-tangra-auth (branch 028-hr)                policy hr-profiles (+ Check/registration)
go-tangra-notification-v4 (branch 028-hr-mail) six hr.* system templates, policy modules-send
go-tangra-scheduler-v4 (branch 028-hr)         policy modules-register + discovery example
go-tangra/deploy/stack (branch 028-hr)         compose hr + token, configs/hr.yaml, init-db, valkey user, allow-list, consumer policies
go-tangra-docker (branch v4)                   compose example + production, configs/hr.yaml, policies, init-db, .env, prod-init
```

**Structure Decision**: mirror go-tangra-signing-v4 (app/httpapi/store/repo/audit/
manifest/tasks/UI, contacts client) and the lcm service-API pattern for the signing
change. Cross-repo SDK dependencies (signing SDK) use a TEMP local `replace` committed
last on the branch, dropped at release (as 026/027).

## Rollout

1. signing: SDK tag `sdk/v4.0.0`, module API + migration → signing minor (v4.1.0).
2. auth, notification (minor, templates), scheduler policy patches.
3. hr: `v3` branch keeps v3; `v4` → `main`; repository renamed `go-tangra-hr` (after
   the user confirms, research D11); release `v4.0.0`, image
   `ghcr.io/go-tangra/go-tangra-hr`.
4. Stack/prod: DB + role (+ btree_gist), Valkey user, gateway allow-list, consumer
   policies, deploy; platform administrator creates `hr:reconcile-signing` and
   `hr:sync-members`; each tenant's HR administrator creates `hr:carry-over`
   (quickstart).
5. v4 starts empty (no v3 data migration).

## Complexity Tracking

| Item | Why needed | Simpler alternative rejected because |
|------|------------|--------------------------------------|
| New signing SDK module + gRPC service | HR must create/cancel/delete submissions and fetch PDFs without a user token (reconciliation, deletion, member sync) | browser API via gateway needs a user token and signing permissions for approvers; see D1 |
| Persisted stream cursor + reconciliation task | an approval must never be lost (US3-3) | existing consumers drop events during downtime by design (F6) |
| System-scoped worker transactions | platform-scoped loops need the tenant list; runtime role is NOBYPASSRLS | a BYPASSRLS role widens the blast radius (D4) |
| Mail outbox + worker | e-mails must survive restarts and never block a request | goroutines (v3) lose mail; scheduler is not a queue (D8) |
| Charges table | two-year requests and exact refunds | a single allowance id (v3) cannot express split charges (D7) |
