---
title: Operate HR
summary: Configuration keys and defaults, the scheduler tasks to create, the service policies and e-mail templates HR needs, and fixes for common problems.
kind: reference
order: 90
tags: [hr, operations, configuration, policy, troubleshooting]
audience: operator
---
HR runs as `hrsvc` (image `ghcr.io/go-tangra/go-tangra-hr`).

```sh
hrsvc -config /app/deploy/container.yaml   # serve; applies migrations
hrsvc bootstrap -config <file>             # apply migrations and exit
hrsvc version
```

At start it enrolls for its SVID `spiffe://<trust domain>/svc/hr`, migrates
its TimescaleDB schema (database `hr`, extensions `timescaledb` and
`btree_gist`), serves the module API and `scheduler.v1.TaskExecutor`,
registers with the gateway, registers its permissions and roles with auth,
and, with `task_scheduler.enabled`, registers its task types with the
scheduler. It stores no secrets.

## Configuration

The file is YAML. Unknown keys are refused, so a typo stops the service at
start with `config: <file>: ...`. Besides the framework keys (`service_name`,
`trust_domain`, `env`, `identity`, `authz`, `server`, `admin`, `discovery`,
`limits`) HR reads:

| Key | Default | Notes |
|---|---|---|
| `db.dsn` | — | required; in production `sslmode=verify-full` (or `verify-ca`) |
| `db.migrate_dsn`, `db.max_conns` | `db.dsn`, 16 | owner connection for migrations; pool size 0–256 |
| `valkey.addresses` | — | required; the event bus (signing outcomes in, `hr.*` events out) |
| `valkey.username`, `valkey.password`, `valkey.ca_file` | — | Valkey user `hr` over TLS |
| `valkey.allow_plaintext` | false | development only; refused in production |
| `events.enabled` | true | live updates of the HR pages |
| `platform_tenant_id` | `00000000-0000-0000-0000-000000000001` | its admins and owners act as platform administrators |
| `gateway.service` | `gateway` | mesh name of the gateway; the only peer allowed to fetch the help pages |
| `gateway.issuer` | — | required, an `https://` origin: the platform token issuer |
| `mesh_enroll.*` | — | `enabled`, `enroll_url`, `lcm_grpc`, `tenant_id`, `token_file`, `state_file`, `insecure` (refused in production) |
| `auth.service`, `notification.service`, `signing.service` | `auth`, `notification`, `signing` | discovery names of those modules |
| `task_scheduler.enabled`, `task_scheduler.service` | false, `scheduler` | register the task types with the scheduler |
| `links.portal_base_url` | — | required, an `https://` origin: links in e-mails |
| `limits_hr.max_page_size` | 100 | 1–500 |
| `limits_hr.max_calendar_days` | 93 | 7–186, the longest calendar range |
| `limits_hr.max_request_days` | 366 | 1–731 calendar days of one request |
| `limits_hr.max_import_bytes`, `limits_hr.max_import_lines` | 64 KiB, 500 | holiday import |
| `limits_hr.max_backup_bytes` | 256 MiB | 1 MiB–1 GiB |
| `limits_hr.max_department_depth` | 10 | 1–50 |
| `consumer.enabled` | true | apply signing outcomes from the event stream; off: only the reconcile task applies them |
| `consumer.block_ms`, `consumer.batch`, `consumer.tenants_refresh_seconds` | 2000, 100, 30 | stream reading |
| `reconcile.older_than_minutes` | 10 | 1–1440; requests awaiting signing longer than this are checked |
| `outbox.interval_ms`, `outbox.batch`, `outbox.max_attempts` | 2000, 50, 5 | e-mail sending and retries |

The log lists accepted opt-outs at start, for example
`task_scheduler.enabled=false: no carry-over, signing reconciliation or
member sync tasks`.

## Scheduler tasks

With `task_scheduler.enabled`, HR registers three task types. A platform
administrator creates the two platform tasks in the scheduler after the
first deployment; each tenant's HR administrator creates its own carry-over.

| Type | Scope | Suggested schedule | Does |
|---|---|---|---|
| `hr:reconcile-signing` (Reconcile leave signing) | platform | `*/15 * * * *` | applies signing outcomes that the stream consumer missed |
| `hr:sync-members` (Sync HR members) | platform | `0 * * * *` | marks people who left a tenant inactive, cancels their open requests, refreshes names, re-routes approvals |
| `hr:carry-over` (Carry over leave allowances) | tenant | `30 0 1 1 *` | carries unused days of `source_year` (default: last year) into the next year |

## Service policies

HR's own policy:

```yaml
rules:
  - id: gateway-forwards
    from: ["spiffe://<trust domain>/svc/gateway"]
    to: ["hr"]
    operations: ["*"]
    effect: allow
  - id: scheduler-execute
    from: ["spiffe://<trust domain>/svc/scheduler"]
    to: ["hr"]
    operations: ["/scheduler.v1.TaskExecutor/ExecuteTask"]
    effect: allow
```

`gateway-forwards` also covers the help pages (`GET /docs/v1/bundle`).

Other modules must admit `spiffe://<trust domain>/svc/hr`:

| Module | Operations | Why |
|---|---|---|
| auth | `/auth.v1.Profiles/Lookup`, `/auth.v1.Profiles/ListMembers`, `/auth.v1.Profiles/Contacts` (rule `hr-directory`) | members, names and e-mail addresses |
| notification | `/notification.v1.Notifier/Send` (`modules-send`) | e-mails |
| signing | `/signing.v1.ModuleSubmissions/*` (`hr-module-api`) | templates, leave forms, signed documents |
| scheduler | `RegisterTaskTypes`, `UnregisterTaskTypes` (`modules-register`) | task types |

HR lists `auth`, `notification`, `signing`, `scheduler` and `gateway` in
`discovery.static`; the scheduler lists `hr`.

## E-mail

E-mails use the notification module's system templates
`hr.request_submitted`, `hr.request_approved`, `hr.request_rejected`,
`hr.request_revoked`, `hr.signing_failed` and `hr.allowance_overdrawn`, sent
through the tenant's e-mail channel. They are queued with each change and
retried up to `outbox.max_attempts` times.

## Troubleshooting

**The service refuses to start with `config: ...`.** The message names the
key. Common ones: `links.portal_base_url must be an https origin`,
`gateway.issuer must be an https origin`, `db.dsn must use
sslmode=verify-full` in production.

**The service refuses to start with `help docs: ...`.** The help pages built
into the binary are invalid. This is caught by `tangra-docs lint` in CI; a
build that skips CI must be rebuilt.

**Approvals answer `temporarily_unavailable`.** HR cannot reach auth to look
up members: check the `hr-directory` rule and `discovery.static.auth`.

**Requests stay in Awaiting signing after everyone signed.** The outcome did
not arrive on the event stream: check `consumer.enabled` and Valkey, and
that the `hr:reconcile-signing` task exists and runs.

**Approval of a signed-form type answers `signing_unavailable`.** Check the
signing module, its `hr-module-api` rule and `discovery.static.signing`.

**No e-mails arrive.** Check the notification module's `modules-send` rule,
the tenant's e-mail channel and `links.portal_base_url`.

**People who left still appear, or approvals go to a departed manager.** The
`hr:sync-members` task is missing or failing.
