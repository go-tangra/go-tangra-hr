# Quickstart: HR Leave Management Module for v4

Validation guide for the dev stack (`go-tangra/deploy/stack`) and production. Details
of routes, payloads and events are in [contracts/](./contracts/); tables in
[data-model.md](./data-model.md).

## Automated

```bash
cd hr-service-v4
make lint vuln test cover            # ≥ 80 %, 100 % authz/routing/leavedays/charges/signingmap
sg docker -c 'make test-integration'  # RLS, concurrency, consumer restart, reconcile, carry-over, backup
cd ../go-tangra-signing-v4 && make test && sg docker -c 'make test-integration'  # module API + source scoping
```

## Stack prerequisites

1. signing with the module API, auth/notification/scheduler policies, hr service
   running (`hr` registered with the gateway: portal "Gateway operations" shows `hr`).
2. Platform administrator creates scheduler tasks `hr:reconcile-signing`
   (`*/15 * * * *`) and `hr:sync-members` (`0 * * * *`), UTC.
3. Test tenant with users: Maria (employee), Ivan (manager Engineering), Petar
   (manager Engineering / Platform), Hana (HR administrator). Mailpit for e-mails.

## Scenarios

1. **Setup (US1)** — Hana creates "Annual leave" (deducts, approval), "Sick leave",
   pool "Vacation" (cap 5) with "Annual leave", allowances 2026: 20 days for Maria.
   Deleting "Vacation" is refused (`in_use`). Maria's balance: 20 total, 0 used.
2. **Holidays (US6)** — Hana imports `2026-08-05,Test holiday` (a Wednesday). The
   preview of a request Mon 3 – Fri 7 Aug 2026 shows 4 days (5 weekdays minus the
   holiday); with a half last day, 3.5 days. The calendar shades 5 Aug.
3. **Departments (US5)** — Engineering (Ivan) › Platform (Petar); Maria in Platform.
   Maria's request routes to Petar; Petar's to Ivan; Ivan's to Hana. Ivan cannot
   approve Maria's request (`not_routed`); Maria cannot approve her own
   (`self_review`).
4. **Approve without signing (US2)** — Maria requests; Petar receives
   `hr.request_submitted`, approves; Maria receives `hr.request_approved`; balance
   used = counted days. Concurrent second approval exceeding the balance is refused.
5. **Signing (US3)** — Hana sets "Annual leave" to require signing with the "Leave
   request form" template (employee/approver parties, fields mapped). Maria requests,
   Petar approves → `awaiting_signing`; signing invites Maria; she signs, then Petar.
   Within 1 minute the request is approved, balance charged once, signed PDF
   downloads from the request (Maria, Petar, Ivan, Hana — not another employee).
6. **Durability (US3)** — repeat 5 but stop `hr` before Petar signs; start it after →
   request approved (cursor replay). Repeat with the stream cursor removed from
   `hr_tenants` → approved at the next `hr:reconcile-signing` run.
7. **Signing failure (US3)** — Maria declines in signing → request back to pending
   with note "declined", no charge, both get `hr.signing_failed`; Petar approves again
   → a new submission.
8. **Calendar (US4)** — 2-week view filtered to Engineering shows Maria's bars,
   shaded holiday, no reasons in the payload (`GET /api/hr/v1/calendar`).
9. **Carry-over (US7)** — preview for 2026 shows Maria carrying min(unused, 5); run it
   twice → one 2027 allowance, same carried value.
10. **Roles, isolation, backup (US8)** — HR viewer reads, cannot change; calendar
    viewer sees only the calendar; tenant B gets 404 for tenant A ids; export and
    import into an empty tenant reproduces data.
11. **Signing API guard** — from a non-hr service identity, `ModuleSubmissions/*` is
    denied by policy; from hr, a user-created submission id answers NotFound.
12. **Leaver** — deactivate Maria in auth; after `hr:sync-members` she disappears from
    the calendar, her pending requests are cancelled (and their submissions).
