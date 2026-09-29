# HR service — deployment notes

The **hr** module (feature 028, go-tangra-hr v4) manages absence types,
allowances, leave requests, departments, public holidays and the team
calendar, with signed leave forms through the signing module.

Module id `hr`; browser API under `/api/hr/v1` (gateway-proxied, platform
token); the only gRPC service it serves is `scheduler.v1.TaskExecutor` for the
scheduler.

## Server operations

| Item | Value |
|---|---|
| Binary | `hrsvc -config deploy/container.yaml` (applies migrations, then serves); `hrsvc bootstrap -config …` migrates and exits; `hrsvc version` |
| Listeners | gRPC `server.grpc_addr` (:9925), HTTP `server.http_addr` (:9926) — mesh, mTLS; admin `admin.addr` (127.0.0.1:9870, `/healthz`, `/readyz`, `/metrics`) |
| Store | TimescaleDB, database `hr`, app role `hr_app` (LOGIN, NOBYPASSRLS, created by init-db — migrations only grant to it); `db.migrate_dsn` for the migration role; extension `btree_gist` |
| Event bus | Valkey user `hr` (reads `platform:events:<tenant>` for signing outcomes, publishes `hr.*` events; rate-limit counters `hr:rl:*`) |
| Secrets | none (no KEK, no object store) |
| Mesh identity | enrols with lcm through the gateway edge (`mesh_enroll`), keeps its SVID in `/state` |
| Gateway | registers its manifest (routes, permissions, abilities, nav) on a lease; registers the module roles and built-in grants with auth |
| Image | `ghcr.io/go-tangra/go-tangra-hr:<semver>` (no `latest`) |

### Database

```sql
CREATE DATABASE hr;
CREATE ROLE hr_app LOGIN PASSWORD '…' NOBYPASSRLS;
-- in database hr, as the owner:
CREATE EXTENSION IF NOT EXISTS timescaledb;
CREATE EXTENSION IF NOT EXISTS btree_gist;
```

Every table carries `tenant_id` under FORCE row-level security;
`hr_audit_events` is a hypertable. `hr_requests_no_overlap` (btree_gist
exclusion) refuses overlapping open requests of a person.

### Background work

- **Signing outcomes**: an in-process consumer reads each known tenant's
  platform event stream from its persisted cursor (`consumer.*`) and applies
  `signing.submission.completed` / `cancelled` / `expired`.
- **E-mail outbox**: e-mails are queued in the same transaction as the change
  and sent by an in-process worker (`outbox.*`, retries with backoff).
- **Scheduler tasks**:
  - `hr:reconcile-signing` (**platform**, suggested `*/15 * * * *`) — asks the
    signing module for requests that waited longer than
    `reconcile.older_than_minutes`.
  - `hr:sync-members` (**platform**, suggested `0 * * * *`) — marks people
    who left the tenant inactive, cancels their open requests, refreshes
    names and re-routes pending approvals.
  - `hr:carry-over` (**tenant**, suggested `30 0 1 1 *`) — year-end
    carry-over, created by each tenant's HR administrator.

  A platform administrator creates the two platform tasks in the scheduler
  UI after the first deploy. With `task_scheduler.enabled=false` none of them
  run (the service warns at start); outcomes still arrive through the stream.

## Mesh policies

`deploy/policy.yaml` (this repository): the gateway forwards everything; only
`svc/scheduler` may call `/scheduler.v1.TaskExecutor/ExecuteTask`.

Rules the callees need for hr's outbound calls:

| Callee | Rule | Operations |
|---|---|---|
| auth | `hr-directory` from `svc/hr` | `/auth.v1.Profiles/Lookup`, `/auth.v1.Profiles/ListMembers`, `/auth.v1.Profiles/Contacts`; plus the usual `services-verify` / `services-register` |
| notification | `modules-send` gains `svc/hr` | `/notification.v1.Notifier/Send` (system templates `hr.*`) |
| signing | `hr-module-api` from `svc/hr` | `/signing.v1.ModuleSubmissions/*` (templates, create and send, state, cancel, delete, document) |
| scheduler | `modules-register` gains `svc/hr` | `RegisterTaskTypes`, `UnregisterTaskTypes`; scheduler `discovery.static.hr: ["hr:9925"]` |

## Gateway and portal

```
-allow spiffe://example.org/svc/hr=/api/hr;hr
```

The UI remote is served by the module at `/ui/` and relayed by the gateway
under `/m/hr/`.

## Notification templates

System templates (notification ≥ the release that adds them):
`hr.request_submitted`, `hr.request_approved`, `hr.request_rejected`,
`hr.request_revoked`, `hr.signing_failed`, `hr.allowance_overdrawn`. The
tenant needs an e-mail channel; links use `links.portal_base_url`.
