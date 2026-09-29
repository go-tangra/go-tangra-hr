# Contract: Mesh policies and stack wiring

Trust domain placeholder `example.org` (dev) / `infra.verax.net` (prod, prod-init).

## hr `deploy/policy.yaml` (callee rules)

```yaml
version: hr-dev-1
rules:
  - id: gateway-forwards
    from: ["spiffe://example.org/svc/gateway"]
    to: ["hr"]
    operations: ["*"]
    effect: allow
  - id: scheduler-execute
    from: ["spiffe://example.org/svc/scheduler"]
    to: ["hr"]
    operations: ["/scheduler.v1.TaskExecutor/ExecuteTask"]
    effect: allow
```

HR exposes no gRPC API to other modules.

## Rules added in other modules

| module | rule | from | operations |
|---|---|---|---|
| signing | hr-module-api | svc/hr | `/signing.v1.ModuleSubmissions/*` (six methods listed explicitly) |
| auth | hr-profiles (+ Check/registration rules) | svc/hr | Profiles/Lookup, ListMembers, Contacts |
| notification | modules-send | svc/hr | /notification.v1.Notifier/Send |
| scheduler | modules-register | svc/hr | RegisterTaskTypes, UnregisterTaskTypes |
| gateway | allow-list | `svc/hr=/api/hr;hr` (gateway-bootstrap `-allow`) | |

## Stack

- Ports: gRPC 9925, HTTP 9926, admin 127.0.0.1:9870; no host port.
- Database `hr`, role `hr_app` LOGIN NOBYPASSRLS, extensions `timescaledb`,
  `btree_gist`; migrations as postgres (`migrate_dsn`).
- Valkey ACL user `hr` (event stream reads, SSE hub) in the dev stack, docker example
  and production example.
- Enrolment token job `hr-token`, volume `hr-state`.
- Discovery (`configs/hr.yaml`): gateway, auth, notification, scheduler, signing.
  Scheduler `discovery.static.hr`.
- Signing's config gains nothing; its policy gains `hr-module-api`.
