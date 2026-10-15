---
title: HR leave
summary: Request leave, have it approved by your manager, track yearly allowances and see who is away on the team calendar.
kind: guide
order: 0
tags: [hr, leave, overview]
---
The HR module manages leave: people request days off, their manager
approves, the days come off a yearly allowance, and everyone sees who is away
on the team calendar. Absence types can also require a signed leave form,
signed through the signing module.

## Concepts

- **Absence type** — a kind of absence, such as vacation or sick leave. A type
  says whether it **deducts** days from an allowance, whether it **needs
  approval**, and whether it needs a **signed leave document**.
- **Allowance pool** — several absence types sharing one allowance, for
  example paid leave and a company day off drawing from the same days.
- **Allowance** — the days a person has for one year, for one absence type or
  pool. The balance shows total, carried over, used, pending and remaining.
- **Request** — a leave request: absence type, first and last day, optional
  half first or last day. Weekends and public holidays are not counted.
- **Department** — departments form a tree. The manager of a person's
  department approves their requests; see
  [who approves](guides/review.md#who-approves).

## Pages in the portal

| Page | Shows | Needs |
|---|---|---|
| Leave calendar | who is away and when, public holidays | `hr:calendar` |
| My leave | your balance and your requests | `hr:request` |
| To review | requests waiting for your decision | `hr:request` |
| Allowances | yearly allowances of everyone; carry-over | `hr:read` |
| Absence types | absence types and allowance pools | `hr:read` |
| Departments | the department tree, managers and members | `hr:read` |
| Holidays | public holidays | `hr:read` |
| HR statistics | requests by status, who is away today; backup | `hr:read` |

Pages update live. Changing settings needs `hr:manage`; see
[permissions and roles](reference.md#permissions-and-roles).

## Where to go next

- [Get started](getting-started.md): request your first leave.
- [Review leave requests](guides/review.md)
- [Set up leave](guides/setup.md): types, pools, allowances, departments,
  holidays and the year-end carry-over.
- [Signed leave forms](guides/signed-forms.md)
- [Example: annual leave for a small company](examples/annual-leave.md)
- [Reference](reference.md): fields, statuses, permissions, API and limits.
