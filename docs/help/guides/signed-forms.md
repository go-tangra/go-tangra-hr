---
title: Signed leave forms
summary: Make an absence type require a leave form signed by the employee and the approver through the signing module, and follow requests while they are being signed.
kind: guide
order: 40
tags: [hr, signing, leave-form, approval]
requires: hr:manage
---
An absence type can require a signed leave document. Approving such a request
does not approve it yet: HR starts a submission in the **signing** module,
prefilled with the leave details, and the request is approved only when
everyone has signed.

## Prepare the template

In the signing module, create a template for the leave form with two
parties, one for the employee and one for the approver, and the text fields
you want prefilled. The HR module lists the tenant's templates from the
signing module.

## Set up the absence type

Open **Absence types**, edit the type and tick **Requires approval** and
**Requires a signed leave document**. Then:

| Field | Meaning |
|---|---|
| Signing template | the leave form template |
| Employee signs as | the template party of the person taking leave |
| Approver signs as | the template party of the approver; it must differ from the employee's |
| one field per text field of the template | the leave value written into that field, or nothing |

Leave values you can map:

| Value | Filled with |
|---|---|
| `employee_name` | the person's name |
| `department` | their department |
| `absence_type` | the absence type |
| `start_date`, `end_date` | first and last day |
| `days` | the working days counted |
| `reason` | the request's reason |
| `approver_name` | the approver's name |
| `today` | the date of the approval |

Saving checks the settings against the template in the signing module: both
parties and every mapped field must exist (`signing_template_invalid`
otherwise). If the template changes later and approvals are refused with
that reason, open the type, adjust the mapping and save.

## What happens to a request

1. The employee requests leave as usual; the request is **Pending**.
2. The approver chooses **Approve**. The signing module sends the form to the
   employee and the approver, and the request is **Awaiting signing**.
   Nothing is charged yet.
3. When everyone has signed, the request becomes **Approved** and the days
   are charged. The charge is made even if it overdraws the allowance, since
   the form is already signed; HR administrators then get an email.
4. If a signer declines, or the submission is cancelled or expires, the
   request goes back to **Pending**, shows *declined*, *cancelled* or
   *expired*, and the employee and approver are told. The approver can
   approve again, which sends a new form, or reject.

While a request awaits signing, the employee can cancel it and the approver
can reject it; both cancel the running submission.

On an approved or revoked request, **Signed document** downloads the signed
document. The employee, the approver, the reviewer, their managers and HR
administrators can download it. Revoking keeps the document as a record.

## When the signing module is not available

Approval is refused with `signing_unavailable` when the signing module cannot
be reached, and with `signer_inactive` when the employee has left the tenant
or the signing module does not accept a signer. Outcomes that were missed while the
HR module was down are picked up by the **Reconcile leave signing** task;
see [operations](../operations.md#scheduler-tasks).
