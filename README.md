# go-tangra-hr (v4)

Leave management module of the go-tangra v4 platform (feature 028), the v4
successor of the v3 hr-service: absence types and pools, yearly allowances
with year-end carry-over, leave requests approved by the requester's manager,
departments, public holidays and a team calendar. Absence types can require a
signed leave form: approval then starts a submission in the signing module
and the request is approved only when everyone has signed.

- **Absence types and pools** — colour, icon, whether days are deducted and
  whether approval is needed, a shared pool of days, a carry-over cap, and for
  signing-required types a signing template with party and field mappings
  (validated against the template in the signing module).
- **Allowances** — days per person, year and type or pool; the balance shows
  total, carried over, used, pending and remaining. Carry-over moves unused
  days (up to the cap) into the next year; it can be previewed, is idempotent
  and also runs as the tenant task `hr:carry-over`.
- **Requests** — whole and half days, weekends and holidays not counted,
  overlaps refused, days charged per calendar year (a request across New
  Year uses both years' allowances). Requests go to the manager of the
  requester's department (the parent department's manager for managers, HR
  administrators when there is none); nobody reviews their own request.
  Approve, reject and revoke (days refunded) with notes and e-mails.
- **Signing** — for signing-required types, approval starts a submission
  through the signing module API (idempotent, source `hr`) and the request
  waits in `awaiting_signing`. Completion approves and charges the days;
  declined, cancelled or expired submissions put the request back to pending
  with the outcome shown. Outcomes arrive on the platform event stream (cursor
  persisted per tenant) and a platform task reconciles anything missed.
- **Calendar, statistics, backup** — week/two-week/month team calendar with
  holidays, live updates over SSE, statistics, tenant export and import.

## Layout

```
cmd/hrsvc                 binary (serve | bootstrap | version)
api/openapi               browser API contract (OpenAPI 3.1)
internal/                 config, authz, audit, apperr, leavedays, routing, charges,
                          store, repo (+repodb, memstore, repotest), catalog, people,
                          departments, allowances, requests, signingmap, signing,
                          events, consumer, outbox, tasks, backup, stream, httpapi, app
pkg/hrmanifest            gateway manifest, permissions, module roles, abilities, nav
ui/                       federated UI remote (Vue 3, @go-tangra/ui)
deploy/                   policy.yaml, container.yaml (stack-shaped example), README.md
specs/028-hr-v4           specification, plan, research, data model, contracts, tasks
```

## Development

```bash
unset GOROOT; export GOWORK=off
make test                 # unit + contract tests (race)
make cover                # ≥ 80 % overall; 100 % authz, routing, leavedays, charges, signingmap
make lint vuln            # vet, staticcheck, gosec; govulncheck
make fuzz                 # every fuzz target for FUZZTIME
sg docker -c 'make test-integration'   # TimescaleDB via testcontainers
(cd ui && npm ci && npm run lint && npx vitest run)
make build                # bin/hrsvc (no UI); make build-ui embeds the remote
```

Permissions: `hr:calendar` (see the team calendar), `hr:request` (own
requests; managers review their departments' requests — relationship checks
in code), `hr:read` (all HR data of the tenant), `hr:manage` (configure types,
allowances, departments, holidays; review any request; backup). Module roles
HR administrator / viewer / employee / calendar viewer; tenant owners and
admins get the administrator role, operators and members the employee role,
auditors the viewer role. Deployment: see [deploy/README.md](deploy/README.md).
Security: see [SECURITY.md](SECURITY.md).
