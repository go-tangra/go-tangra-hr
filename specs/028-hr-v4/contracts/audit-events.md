# Contract: HR audit vocabulary

Closed vocabulary (`hr_audit_events`, platform audit schema; FR-059). Entries never
contain reasons, notes, review notes, names or e-mail addresses (SR-007); they carry
IDs, codes and counts only.

`outcome` ∈ {ok, denied, failed} with a reason code:

| action | object | detail (codes/counts only) | reason codes (failed/denied) |
|---|---|---|---|
| absence_type.create / .update / .delete | absence_type | requires_signing, deducts | in_use, duplicate, signing_template_invalid |
| pool.create / .update / .delete | pool | members | in_use, duplicate |
| allowance.create / .update / .delete | allowance | user_id, year | duplicate, in_use |
| request.create | request | user_id, type_id, days, status | overlap, insufficient_allowance, no_allowance, zero_days |
| request.update / .cancel / .delete | request | status_from, status_to | invalid_transition |
| request.approve / .reject / .revoke | request | status_from, status_to, charged_days | self_review, not_routed, insufficient_allowance, invalid_transition |
| request.signing_start | request | submission_id | signing_unavailable, signing_template_invalid, signer_inactive |
| request.signing_outcome | request | submission_id, outcome, source (event/reconcile), result | |
| request.overdraw | allowance | request_id, days | |
| department.create / .update / .move / .delete / .members | department | added, removed, manager_set | not_empty, cycle |
| holiday.create / .update / .delete / .import | holiday | lines, created | validation |
| carryover.preview / .run | tenant | source_year, created, updated | |
| member.deactivated | member | user_id, cancelled_requests | |
| mail.failed | request | template key, reason code, attempts | |
| backup.export / .import | tenant | records | backup_too_large, validation |
| task.carryover / task.reconcile | tenant / platform | checked, applied | |

Actor: user id, `system` (task types, event consumer) or the calling service's SPIFFE
id; tenant; correlation id; client IP for browser calls. Authorization refusals
answered with 404 are audited as `denied` / `not_found`.
