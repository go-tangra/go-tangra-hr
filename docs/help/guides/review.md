---
title: Review leave requests
summary: Who approves which request, and how to approve, reject or revoke leave.
kind: guide
order: 20
tags: [hr, review, approval, managers]
requires: hr:request
---
Requests are reviewed by the requester's manager, or by HR. Open **To
review** to see the requests waiting for your decision, then open one.

## Who approves

The approver is found in the department tree, again for every decision, so
changes to departments apply to open requests too:

1. the manager of the requester's department;
2. if that department has no manager, the manager is the requester, or the
   manager has left the tenant: the manager of the nearest parent department
   that has one;
3. if there is none: the HR administrators (everyone holding `hr:manage`).

Nobody reviews their own request. A manager's own request therefore goes to
the manager above them. Someone with `hr:manage` can decide any request
except their own.

A person who is in no department is approved by the HR administrators.

## Decide

On the request page you see the person, absence type, dates, days, approvers,
reason and notes.

| Action | When | Effect |
|---|---|---|
| **Approve** | pending | the days are taken from the allowance and the request is **Approved**; for a type with a signed form, signing starts instead (see [signed leave forms](signed-forms.md)) |
| **Reject** | pending or awaiting signing | **Rejected** with your notes; a running signing is cancelled |
| **Revoke** | approved | **Revoked** with a reason; exactly the charged days go back to the allowance |

The employee gets an email for each decision.

Approval checks the balance again: if the person no longer has enough days
for that year, approval is refused with `insufficient_allowance`. HR can
raise the allowance first.

## What managers can see

A manager sees the balances and requests of the people in the departments
they manage, including sub-departments (**My leave → All requests**). The
team calendar shows absences without reasons or notes.

Related: [get started](../getting-started.md), [reference](../reference.md#request-statuses).
