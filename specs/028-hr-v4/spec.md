# Feature Specification: HR Leave Management Module for v4

**Feature Branch**: `028-hr-v4`

**Created**: 2026-09-29

**Status**: Planned (plan + 72 tasks, 2026-09-29)

**Spans**: new v4 hr module (API + UI remote), go-tangra-signing-v4 (mesh API for other modules, HR as a submission source), go-tangra-notification-v4 (hr e-mails), go-tangra-scheduler-v4 (hr task types), go-tangra-auth (hr permissions and module roles), go-tangra-portal-v4 (UI remote, gateway route), go-tangra-docker (stack, database, mesh policies)

**Input**: User description: "V3 have following module /home/jadmin/projects/go-tangra/hr-service i like the same with same functionality but for V4. The v3 module have a strong integration to the signing module"

**Decisions taken with the user (2026-09-29)**:

1. **HR drives signing through a signing module API**: the signing module
   gets a mesh-only API that only the hr module may call. Through it HR lists
   the tenant's signing templates, creates and sends a submission from a
   template, cancels or deletes it, and fetches the signed PDF. HR follows the
   outcome through signing's published events (completed, cancelled,
   expired). The flow stays automatic, as in v3 (FR-030–FR-039).
2. **HR keeps its own departments**: v4 auth has no org units. HR keeps
   departments (name, optional parent, optional manager) and assigns
   employees to them. The calendar groups and filters by department
   (FR-040–FR-044).
3. **A failed signing returns the request to pending**: when the signing
   submission is declined, expires or is cancelled, the approval is undone.
   The request goes back to pending with a note, and nothing is deducted. The
   approver can approve again (a new submission) or reject (FR-036).
4. **The v3 gaps are closed**:
   - more e-mails: approvers learn of new requests; employees learn of
     approval, rejection, revocation and failed signing (FR-050–FR-054);
   - per-tenant public holidays, excluded from the day count and shown in
     the calendar (FR-045–FR-047);
   - a year-end carry-over task in the scheduler (FR-048, FR-049);
   - manager approval: requests go to the employee's department manager, and
     HR administrators can still approve any request (FR-020–FR-023).

## Context

### What v3 provides

The v3 hr-service manages employee leave (absences) for a tenant:

- **Absence types** (for example "Annual leave", "Sick leave", "Home
  office"). Each has a name, description, color, icon, sort order, active
  flag and free-form metadata, plus three behaviours:
  - *deducts from allowance*: approved days are taken from the employee's
    yearly allowance;
  - *requires approval*: requests wait for a reviewer; otherwise they are
    approved on creation;
  - *requires signing*: approval starts a signing workflow with a chosen
    signing template.
- **Allowance pools**: several absence types can share one balance (for
  example "Annual leave" and "Unpaid half-day" drawing from the same pool).
- **Allowances**: per employee, per year, for one absence type or one pool:
  total days, carried-over days, used days and notes. The remaining balance
  is total + carried over − used.
- **Leave requests**: an employee requests an absence type for a date range
  with a reason and notes.
  - The day count is the number of working days (Monday–Friday) in the
    range.
  - A request is refused if it overlaps another pending, approved or
    awaiting-signing request of the same person, or if the remaining balance
    is too low.
  - Statuses: pending → approved / rejected / cancelled; approved → revoked
    or cancelled; pending → awaiting signing → approved.
  - Approving deducts the days atomically (a concurrent approval cannot
    overdraw the balance). Cancelling or revoking an approved request refunds
    them.
  - Only rejected requests can be deleted.
  - A rejection e-mail is sent to the employee.
- **Signing integration**: when the absence type requires signing, approving
  sets the request to *awaiting signing* and creates a sequential submission
  from the type's template: the employee signs first, then the approver.
  - The template is prefilled with the employee's name, the day count, the
    start and end dates and today's date.
  - When signing publishes "submission completed", the request becomes
    approved and the days are deducted.
  - Revoking cancels the submission; deleting the request deletes it.
  - Participants and reviewers can download the signed PDF from the request.
  - Administrators pick the template from the tenant's signing templates.
- **Calendar**: a team timeline (week, two weeks, month) with one row per
  person, grouped and filterable by org unit and person. Bars are colored by
  absence type and marked pending, awaiting signing or approved. Dragging
  across days starts a new request.
- **Balance**: per person and year, one line per absence type or pool with
  total, carried over, used and remaining days.
- **Roles**: HR administrator (everything), employee (own requests, calendar,
  balances), viewer (read-only) and client (calendar and requests only).
- **Other**: statistics (types, pending, approved, rejected requests), an
  audit log, and backup and restore of the module's data.

### The v4 gap

v4 has no HR module, and v4 signing has no API that another module can call.
Porting v3 is not a copy, because v3 has functional and security defects that
v4 must not inherit:

- The requester's name, e-mail and org unit are taken from the request body
  and stored as given, so they can be forged. The day count can also be
  supplied by the client (any value up to 365, whatever the date range).
- An approver can approve their own request. Anyone with the approve
  permission can approve anyone's request; there is no manager routing.
- Every employee can list every colleague's requests, including reasons,
  notes and review notes.
- Cancelling has no status check (a rejected or revoked request can be
  "cancelled"), and cancelling a request awaiting signing leaves its
  submission running.
- Absence types and pools can be deleted while allowances and requests use
  them.
- Signing completion arrives over a fire-and-forget channel: if HR is down
  when a submission completes, the request stays awaiting signing forever.
  The handler finds the request by submission ID only, without a tenant
  check. A declined or expired submission is never handled.
- The signed PDF is fetched over plain, unauthenticated HTTP from signing's
  PDF proxy by storage key, and returned to the browser as a base64 data URL
  inside a JSON response.
- The signing template's field names are hard-coded in HR ("Name2",
  "TotalDays", …). The day count is prefilled rounded down to a whole number.
- A leave that spans the new year is deducted entirely from the start year's
  allowance.
- The rejection e-mail is sent as a platform-level message and silently fails
  if a channel named "Default SMTP" is missing. No other event is e-mailed.
- Declared but missing: public holidays (holidays count as working days),
  year-end carry-over (carried-over days are typed by hand) and manager
  approval.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Set up absence types, pools and allowances (Priority: P1)

An HR administrator creates the absence types "Annual leave" (deducts from
allowance, requires approval), "Sick leave" (no deduction, no approval) and
"Home office" (no deduction, requires approval). They create the pool
"Vacation" and put "Annual leave" and "Unpaid half-day" in it. They then give
every employee a 2026 allowance of 20 days in the "Vacation" pool, with the
days carried over from 2025.

**Why this priority**: No leave can be requested or balanced without types
and allowances.

**Independent Test**: Create types, a pool and allowances through the UI and
read them back. A person's balance shows one line per pool or standalone type
with total, carried over, used, pending and remaining days.

**Acceptance Scenarios**:

1. **Given** an HR administrator, **When** they create an absence type with a
   name (unique in the tenant), description, color, icon, sort order, active
   flag and the three behaviours, **Then** it is listed in sort order and
   available for requests while active.
2. **Given** absence types, **When** the administrator creates a pool and
   selects member types, **Then** those types draw from the pool's balance.
   A type belongs to at most one pool.
3. **Given** a tenant member, **When** the administrator creates an allowance
   for a year and either one type or one pool, with total days, carried-over
   days and notes, **Then** it appears in the allowance list and in the
   person's balance. A second allowance for the same person, year and
   type/pool is refused.
4. **Given** a type, pool or allowance in use by requests (or a pool with
   member types), **When** the administrator deletes it, **Then** deletion is
   refused with the reason. Types can be deactivated instead.
5. **Given** an employee, **When** they open their balance, **Then** they see
   their own lines for the chosen year. They cannot see other people's
   balances unless they are HR administrators, HR viewers or the person's
   manager.

---

### User Story 2 - Request leave and have it approved by the manager (Priority: P1)

Maria, in the "Engineering" department managed by Ivan, drags across 3–7
August in the calendar and requests "Annual leave". Two of those days fall on
the weekend, so the request counts 3 working days. Ivan receives an e-mail,
opens "To review" and approves. Maria receives an "approved" e-mail, and her
remaining vacation drops by 3 days.

**Why this priority**: Requesting and approving leave is the core purpose of
the module.

**Independent Test**: In the dev stack, an employee creates a request, the
department manager approves it, and the balance changes by exactly the
counted days. Both e-mails arrive in the test mailbox.

**Acceptance Scenarios**:

1. **Given** an employee with an allowance, **When** they request an active
   absence type for a date range with a reason and notes (optionally a half
   day on the first and/or last day), **Then** the server counts the working
   days in the range, skipping weekends and the tenant's public holidays, and
   creates the request. The requester's name and department come from the
   platform and HR records, never from the request.
2. **Given** a request that would overlap another pending, awaiting-signing
   or approved request of the same person, or whose days exceed the remaining
   balance, **Then** it is refused with the reason.
3. **Given** an absence type that does not require approval, **Then** the
   request is approved at once and the days are deducted.
4. **Given** a type that requires approval, **Then** the request is pending
   and its approvers are notified by e-mail (FR-021, FR-050).
5. **Given** a pending request, **When** an allowed approver approves it,
   **Then** the days are deducted atomically. If the balance no longer
   suffices (another request was approved meanwhile), approval is refused.
   The employee is notified.
6. **Given** a pending request, **When** an allowed approver rejects it with
   review notes, **Then** it is rejected and the employee is notified with
   the notes.
7. **Given** a request of the approver themselves, **Then** they cannot
   approve or reject it; it is routed on (FR-022).
8. **Given** a pending request, **Then** its owner can edit the reason and
   notes, or cancel it. After review, only cancelling (approved: refund) is
   possible.

---

### User Story 3 - Approval with a signed leave document (Priority: P1)

"Annual leave" requires signing with the signing template "Leave request
form". When Ivan approves Maria's request, it becomes "awaiting signing".
Maria is invited by the signing module to sign the prefilled form in the
portal, then Ivan. When Ivan has signed, the request becomes approved, the
days are deducted, and both can download the signed form from the request.

**Why this priority**: The user named the signing integration as the core of
the v3 module.

**Independent Test**: In the dev stack, approving a request of a
signing-required type creates one sequential submission (employee, then
approver) with the mapped fields prefilled. Both sign; within a minute the
request is approved, the balance is reduced once, and the signed PDF
downloads from the request. Stopping HR while the last signature happens and
starting it again still approves the request.

**Acceptance Scenarios**:

1. **Given** an HR administrator editing an absence type, **When** they turn
   on "requires signing", **Then** they pick an active template from the
   tenant's signing templates, assign its two parties to "employee" and
   "approver", and map template fields to leave values (employee name,
   department, absence type, start date, end date, day count, reason,
   approver name, today). A mapped field that no longer exists on the
   template is reported.
2. **Given** a pending request of a signing-required type, **When** it is
   approved, **Then** the request becomes awaiting signing (nothing is
   deducted yet), and a sequential submission is created and sent: employee
   first, then the approving user, with the mapped values prefilled. If the
   submission cannot be created, the request stays pending and the approver
   sees the reason.
3. **Given** the submission completes, **Then** the request becomes approved
   and the days are deducted exactly once, even if the completion is received
   more than once or after an HR restart.
4. **Given** the submission is declined by a signer, expires or is cancelled
   in the signing module, **Then** the request returns to pending with a note
   saying why, nothing is deducted, and the employee and approver are
   notified. The approver can approve again (a new submission) or reject.
5. **Given** a request awaiting signing, **When** its owner cancels it or an
   approver rejects it, **Then** the submission is cancelled in the signing
   module.
6. **Given** an approved request with a signed document, **When** it is
   revoked, **Then** the days are refunded; the signed document is kept as a
   record. When a rejected request is deleted, its submission and documents
   are deleted in the signing module.
7. **Given** a completed signing, **When** the employee, the approver, their
   manager or an HR administrator downloads the signed document from the
   request, **Then** the PDF is streamed through the HR module. Anyone else
   is refused.

---

### User Story 4 - Team calendar (Priority: P1)

Ivan opens the calendar in "2 weeks" view, filtered to his department. He
sees one row per team member, colored bars for their absences (pending
hatched, awaiting signing marked, approved solid), and the public holiday on
Friday shaded. Hovering a bar shows the type, dates and days. He drags across
next Monday–Tuesday on his own row to request leave.

**Why this priority**: The calendar is v3's landing page and the main way
teams plan absences.

**Independent Test**: With 3 departments and 30 people, the calendar shows
the right rows per department filter, bars for pending, awaiting-signing and
approved requests (not rejected, cancelled or revoked), and shaded holidays,
in week, 2-week and month views.

**Acceptance Scenarios**:

1. **Given** the calendar, **Then** it shows week, 2-week and month views
   with "today" and previous/next navigation, one row per active tenant
   member, grouped by department.
2. **Given** a department or person filter, **Then** only matching rows are
   shown.
3. **Given** absences in the range, **Then** pending, awaiting-signing and
   approved requests are drawn in their type's color and marked by status.
   Other people's reasons, notes and review notes are not shown.
4. **Given** public holidays in the range, **Then** those days are shaded
   and named.
5. **Given** a user allowed to request leave, **When** they drag across days
   on their own row (or, for HR administrators, anyone's row), **Then** the
   new request form opens with those dates.

---

### User Story 5 - Departments and manager routing (Priority: P2)

The HR administrator creates the departments "Engineering" (manager Ivan) and
"Engineering / Platform" (manager Petar), and assigns every member. Requests
of Platform members go to Petar. Petar's own requests go to Ivan. Ivan's own
requests go to HR administrators, because Engineering has no parent.

**Why this priority**: Manager routing replaces v3's "any approver approves
anything", and the calendar groups by department.

**Independent Test**: Build the departments above and check, for one
employee of each level, who sees the request in "To review", who receives the
e-mail, and that nobody else (except HR administrators) can approve it.

**Acceptance Scenarios**:

1. **Given** an HR administrator, **When** they create, rename, nest, move
   and delete departments (a department with members or sub-departments
   cannot be deleted), **Then** the tree is updated.
2. **Given** a department, **When** a manager is set (any tenant member),
   **Then** that person approves requests of the department's members.
3. **Given** a member assigned to a department, **Then** a person belongs to
   at most one department. Unassigned members appear under "No department".
4. **Given** a request, **Then** its approvers are: the manager of the
   requester's department, unless the requester is that manager, in which
   case the manager of the nearest ancestor department with a different
   manager; with no such manager, HR administrators. HR administrators can
   always approve, except their own requests.
5. **Given** a manager, **Then** they can see their department members'
   requests (and those of sub-departments) and balances, in addition to their
   own.

---

### User Story 6 - Public holidays (Priority: P2)

The HR administrator adds the 2026 Bulgarian public holidays, marking
recurring ones (1 January, 3 March, 24 May, …) as yearly. Requests spanning a
holiday no longer count it, and the calendar shades it.

**Why this priority**: Without holidays, v3 overcharged allowances on every
leave spanning a holiday.

**Independent Test**: Add a holiday on a Wednesday; a Monday–Friday request
counts 4 days; the calendar shades Wednesday.

**Acceptance Scenarios**:

1. **Given** an HR administrator, **When** they add a holiday with a date, a
   name and "every year", **Then** it applies to that date (and the same day
   every year when recurring).
2. **Given** holidays, **Then** new requests do not count them as working
   days. Existing requests keep their counted days.
3. **Given** the holiday list, **Then** it can be filtered by year, edited
   and deleted, and imported from a file of date/name lines.

---

### User Story 7 - Year-end carry-over (Priority: P2)

On 1 January the scheduler runs "HR: carry over allowances" for the tenant.
For every 2026 allowance in the "Vacation" pool (carry-over cap 5 days), it
creates or updates the 2027 allowance: carried over = min(unused, 5); total
days = the 2026 total unless the administrator has already set 2027.

**Why this priority**: v3 administrators typed carried-over days by hand for
every employee.

**Independent Test**: With three employees with 2, 5 and 9 unused days and a
cap of 5, one run creates 2027 allowances carrying 2, 5 and 5 days. Running it
again changes nothing.

**Acceptance Scenarios**:

1. **Given** an absence type or pool, **When** the administrator sets a
   carry-over cap (0 = none carried, empty = no cap), **Then** the task uses
   it.
2. **Given** the task runs for year Y, **Then** for each person with a Y
   allowance it creates the Y+1 allowance (total copied from Y) or updates
   the carried-over days of an existing one. Running it again for the same
   year does not add days twice.
3. **Given** a leave spanning two years, **Then** its days are deducted from
   each year's allowance according to the days in each year (FR-015).
4. **Given** an HR administrator, **Then** they can also run carry-over
   manually for a chosen year from the allowances page and see what it will
   change before applying.

---

### User Story 8 - Roles, statistics, backup (Priority: P3)

An HR viewer from payroll sees every request and balance but changes
nothing. A "calendar viewer" sees only the calendar. The administrator
backs up the HR data before an upgrade and restores it into a test tenant.

**Why this priority**: Parity with v3's roles, statistics and backup.

**Independent Test**: Every HR operation is checked against each role in the
authorization test suite. A backup restored into an empty tenant reproduces
types, pools, allowances, requests, departments and holidays.

**Acceptance Scenarios**:

1. **Given** the module roles (FR-060), **Then** every operation is allowed
   or refused as the role table says, in the caller's tenant only.
2. **Given** an HR administrator or viewer, **Then** the statistics show the
   counts of absence types and of pending, awaiting-signing, approved and
   rejected requests, and who is absent today.
3. **Given** an HR administrator, **When** they back up the module, **Then**
   they get one archive of the tenant's HR data; restoring it into a tenant
   recreates the data. Signing documents stay in the signing module; restored
   requests keep their submission references.

---

### Edge Cases

- A request whose range contains only weekends and holidays counts 0 days
  and is refused.
- A person without an allowance for a deducting type (or its pool) cannot
  request it; the refusal says no allowance is configured for that year.
- Two approvers approve the last remaining days of two requests at the same
  time: exactly one succeeds.
- A signing completion arrives for a request that was cancelled meanwhile:
  it is ignored and recorded; nothing is deducted.
- A completion for another tenant's submission ID, or for an unknown one, is
  ignored.
- The signing module is unreachable at approval: the request stays pending,
  and the approver can retry.
- The signing template was archived or deleted after being chosen: approval
  of a signing-required type is refused with the reason, and the
  administrator is told in the absence type view.
- The employee or approver is no longer an active tenant member when signing
  starts: approval is refused with the reason.
- A department manager leaves the tenant: their department's requests route
  as if it had no manager.
- A member is moved to another department while requests are pending: the
  approvers are recomputed from the current department.
- An allowance is lowered below what is already used: allowed, and the
  balance shows the overdraw.
- A revoked request's refund can never make used days negative.
- Carry-over for a year with no allowances does nothing.

## Requirements *(mandatory)*

### Functional Requirements

**Absence types, pools and allowances**

- **FR-001**: HR administrators MUST be able to create, edit, deactivate and
  delete absence types: name (unique per tenant), description, color, icon,
  sort order, active flag, metadata, deducts-from-allowance,
  requires-approval, requires-signing (with its signing settings, FR-030),
  pool membership and carry-over cap.
- **FR-002**: HR administrators MUST be able to create, edit and delete
  allowance pools (name, description, color, icon, member types). A type is
  in at most one pool.
- **FR-003**: HR administrators MUST be able to create, edit and delete
  allowances: person, year (2000–2099), one type or one pool, total days
  (0–365), carried-over days, notes. One allowance per person, year and
  type/pool.
- **FR-004**: Deleting a type, pool or allowance MUST be refused while
  requests or allowances reference it (a pool: while it has member types).
- **FR-005**: The balance of a person for a year MUST list one line per pool
  and per deducting type outside a pool: total, carried over, used, pending
  (days in pending and awaiting-signing requests) and remaining (total +
  carried over − used).

**Leave requests**

- **FR-010**: A tenant member with the request permission MUST be able to
  request leave for themselves; HR administrators also for any member. A
  request has an active absence type, a start and end date, optional
  half-day on the first and last day, a reason and notes.
- **FR-011**: The day count MUST be computed by the server: working days
  (Monday–Friday) in the range, minus the tenant's public holidays, minus
  0.5 per half day. The client cannot set it. Requests of 0 days are refused.
- **FR-012**: A request MUST be refused when it overlaps a pending,
  awaiting-signing or approved request of the same person, or, for a
  deducting type, when no allowance exists or its days exceed the remaining
  balance.
- **FR-013**: A type without approval MUST be approved on creation, with the
  days deducted atomically; otherwise the request is pending.
- **FR-014**: Deductions and refunds MUST be atomic per allowance: concurrent
  approvals can never make the remaining balance negative, and a refund can
  never make used days negative. The request records which allowances were
  charged and how many days each, so refunds return exactly those.
- **FR-015**: A request spanning two years MUST be charged to each year's
  allowance by the days falling in that year; both must suffice.
- **FR-016**: Status changes MUST follow: pending → approved, awaiting
  signing, rejected or cancelled; awaiting signing → approved, pending
  (signing failed), rejected or cancelled; approved → revoked or cancelled.
  Any other transition is refused. Only rejected and cancelled requests can
  be deleted.
- **FR-017**: The owner MAY edit reason and notes while pending, and cancel
  while pending, awaiting signing or approved (approved and not yet started:
  refund; approved and started: refused, it must be revoked by an approver).
- **FR-018**: Approvers MUST be able to approve and reject (with review
  notes) pending requests, and revoke approved ones (with a reason, refund).
  The reviewer and review time are recorded.
- **FR-019**: Request lists MUST be filterable by person, department, type,
  status and date range, with paging. "My requests" and "To review" views
  MUST exist.

**Approval routing**

- **FR-020**: Every request of a type requiring approval MUST have computed
  approvers (US5 scenario 4): the requester's department manager; when the
  requester is that manager, the nearest ancestor department's manager who
  is not the requester; with none, the HR administrators.
- **FR-021**: The approvers MUST see the request in "To review" and receive
  the new-request e-mail.
- **FR-022**: Nobody can approve, reject or revoke their own request, HR
  administrators included.
- **FR-023**: HR administrators MAY approve, reject and revoke any other
  person's request. Managers act on the requests routed to them only.

**Signing integration** (decision 1)

- **FR-030**: An absence type requiring signing MUST have: an active signing
  template of the tenant, the template party for the employee and the party
  for the approver, and a mapping from template fields to leave values
  (employee name, department, absence type, start date, end date, day count
  with halves, reason, approver name, today's date). The template list and
  its parties and fields come from the signing module.
- **FR-031**: Approving a request of such a type MUST set it to awaiting
  signing, record the reviewer, and create and send one sequential
  submission through the signing module: employee first, then the approving
  user, both as tenant users, with the mapped values prefilled and HR as the
  submission's source, referencing the request.
- **FR-032**: If the submission cannot be created or sent, the request MUST
  stay pending (nothing recorded as approved), and the approver gets the
  reason. A half-created submission is cancelled.
- **FR-033**: HR MUST receive the signing module's submission outcomes
  (completed, cancelled with reason, expired) reliably: none lost while HR is
  down or restarting, each handled at most once, only for submissions HR
  created in that tenant.
- **FR-034**: HR MUST also reconcile periodically: every request awaiting
  signing longer than a configured time is checked against the submission's
  state in the signing module, and the outcome applied.
- **FR-035**: On completion, the request MUST become approved and the days
  deducted exactly once. If the balance no longer suffices, the request is
  still approved (the document is signed), the allowance shows the overdraw,
  and HR administrators are notified.
- **FR-036**: On decline, expiry or cancellation from the signing module,
  the request MUST return to pending with a note naming the outcome, without
  deduction, and the employee and approver are notified (decision 3).
- **FR-037**: Cancelling or rejecting a request awaiting signing MUST cancel
  its submission. Deleting a request MUST delete its submission and
  documents in the signing module. Revoking keeps the signed document.
- **FR-038**: The signed PDF MUST be downloadable from the request by the
  employee, the approver, the employee's managers and HR administrators,
  streamed through the HR module from the signing module over the mesh.
- **FR-039**: The signing module MUST offer an API reachable only over the
  mesh and only by the hr module, acting for a tenant:
  - list active templates with their parties and field names;
  - create and send a submission from a template (signers are active tenant
    users, the sender is the approving user, source and external reference
    recorded, prefill values validated against the template's fields);
  - get a submission's state;
  - cancel and delete a submission created by HR;
  - fetch the final signed PDF of a completed submission created by HR.
  HR cannot read or change submissions it did not create.

**Departments and people**

- **FR-040**: HR administrators MUST be able to create, rename, nest, move
  and delete departments (delete: no members, no sub-departments), and set
  a manager (an active tenant member) per department.
- **FR-041**: HR administrators MUST be able to assign each tenant member to
  at most one department.
- **FR-042**: The people list (for the calendar, allowances and requests)
  MUST come from the tenant's active members in auth, with names from auth
  profiles and departments from HR.
- **FR-043**: Managers MUST see the requests and balances of their
  department's members and its sub-departments.
- **FR-044**: When a member leaves the tenant, their HR data MUST stay
  (history), they MUST disappear from the calendar and people pickers, and
  their pending requests MUST be cancelled.

**Holidays and carry-over**

- **FR-045**: HR administrators MUST be able to add, edit and delete public
  holidays (date, name, recurring yearly) and import them from a file of
  date and name lines.
- **FR-046**: Holidays MUST be excluded from new requests' day counts and
  shaded and named in the calendar.
- **FR-047**: Changing holidays MUST NOT change the day counts of existing
  requests.
- **FR-048**: The module MUST register a scheduler task type "carry over
  allowances" (tenant-scoped, payload: source year, default the previous
  year) that applies FR-049 for the tenant. It is idempotent.
- **FR-049**: Carry-over for year Y MUST, per person and type/pool with a Y
  allowance, set the Y+1 allowance's carried-over days to min(unused, cap),
  creating the Y+1 allowance with Y's total when missing. A preview shows the
  changes before a manual run applies them.

**E-mails** (decision 4)

- **FR-050**: New pending request → its approvers.
- **FR-051**: Approved, rejected (with notes) and revoked (with reason) →
  the employee.
- **FR-052**: Signing failed (declined, expired, cancelled) → the employee
  and the approver.
- **FR-053**: Completion with overdraw → HR administrators.
- **FR-054**: E-mails MUST use tenant templates of the notification module
  over the tenant's e-mail channel (English and Bulgarian), sent
  asynchronously; a failure is logged and retried, never blocks or fails the
  request. The signing invitations themselves are sent by the signing
  module.

**Calendar, statistics, backup, audit**

- **FR-055**: The calendar MUST offer week, 2-week and month timelines of
  active members grouped by department, filters by department and person,
  bars for pending, awaiting-signing and approved requests colored by type,
  shaded holidays, hover details, and drag-to-request on rows the user may
  request for.
- **FR-056**: The calendar MUST show colleagues' absences as person, type,
  dates, days and status only.
- **FR-057**: Statistics MUST show counts of absence types and of pending,
  awaiting-signing, approved and rejected requests, and today's absentees.
- **FR-058**: HR administrators MUST be able to back up and restore the
  tenant's HR data (types, pools, allowances, requests, departments,
  holidays, signing settings).
- **FR-059**: Every change (types, pools, allowances, departments, holidays,
  status changes, carry-over runs, restores) MUST be written to the platform
  audit log with actor, tenant, object and outcome.

**Roles and permissions**

- **FR-060**: The module MUST register its permissions and these module
  roles with auth (feature 019):

  | Role | Can |
  |------|-----|
  | HR administrator | everything, all people of the tenant |
  | HR viewer | read everything (requests, balances, types, pools, allowances, departments, holidays, statistics), change nothing |
  | Employee | calendar; own requests and balance; request, edit, cancel own; download own signed documents |
  | Calendar viewer | calendar only |

  Managers get approval and read rights for their departments from the
  department tree (FR-023, FR-043), not from a role. Tenant owners and
  administrators hold the HR administrator permissions, as in other modules.

### Security Requirements *(mandatory — Constitution: Development Workflow)*

- **Trust boundaries crossed**:
  - browser → gateway → hr (user API, signed document download)
  - hr → signing (mesh API: templates, submissions, signed PDF)
  - signing → event bus → hr (submission outcomes)
  - hr → auth (members, profiles, contacts, permissions)
  - hr → notification (e-mails over the mesh)
  - scheduler → hr (carry-over and reconciliation tasks)
- **Data classification**:
  - **Confidential (personal)**: absence reasons, notes, review notes, sick
    leave, balances, departments, signed leave documents.
  - **Internal**: absence types, pools, holidays.
- **Authentication/Authorization**: every user call is authenticated at the
  gateway and authorised by the module's permissions in the caller's tenant,
  plus ownership and manager checks. Module-to-module calls are
  authenticated by mesh identity and allowed by policy.
- **Threat scenarios**:
  - reading colleagues' reasons, sick leave or balances
  - approving one's own request, or a request not routed to the approver
  - forging the requester's identity or the day count
  - overdrawing a balance through concurrent approvals
  - acting on another tenant's data by ID
  - a forged or replayed signing outcome approving a request
  - another module (or a compromised one) using signing's module API
  - HR reaching submissions it did not create
  - leaking personal data through logs, events, e-mails or backups
- **SR-001**: Every operation MUST be limited to the caller's tenant; every
  object read by ID is checked against the tenant.
- **SR-002**: Requester identity, names, e-mail addresses and departments
  MUST come from auth and HR records, never from request bodies.
- **SR-003**: Approval rights MUST be computed on the server for every
  decision (FR-020–FR-023); self-approval is refused.
- **SR-004**: Details of a request (reason, notes, review notes, signed
  document) MUST only be readable by its owner, its approvers, the owner's
  managers, HR administrators and HR viewers.
- **SR-005**: Signing outcomes MUST only be accepted from the signing
  module's authenticated event stream (or its API during reconciliation),
  matched by tenant and submission, and applied once.
- **SR-006**: The signing module API MUST be allowed only for the hr module's
  mesh identity, act within the tenant given, and only on submissions whose
  source is hr (except listing templates).
- **SR-007**: Events published by HR MUST carry IDs and status codes only —
  no reasons, notes, names or e-mail addresses. Logs MUST NOT contain them
  either.
- **SR-008**: Backups MUST be readable only by HR administrators of the
  tenant, and restore MUST only write into the caller's tenant.
- **SR-009**: Inputs MUST be validated with limits (text lengths, dates,
  days, import file size and line count, page sizes) and fuzz-tested where
  parsed (holiday import, backup restore).

### Key Entities

- **Absence type**: name, description, color, icon, sort order, active,
  metadata, deducts-from-allowance, requires-approval, requires-signing,
  signing settings (template, employee party, approver party, field
  mapping), pool, carry-over cap.
- **Allowance pool**: name, description, color, icon, member types,
  carry-over cap.
- **Allowance**: person, year, type or pool, total days, carried-over days,
  used days, notes.
- **Leave request**: person, absence type, start and end date, half days,
  counted days, status, reason, notes, reviewer, review notes and time,
  signing submission reference, signing note, charged allowances (allowance,
  days).
- **Department**: name, parent, manager, members.
- **Public holiday**: date, name, recurring yearly.
- **Signing outcome record**: submission, outcome, when received, applied to
  which request (for at-most-once handling and audit).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An employee requests leave from the calendar in under 1
  minute, and an approver approves it from the e-mail link in under 30
  seconds.
- **SC-002**: A signing-required request moves from approval through two
  signatures to approved in the dev stack, with the balance reduced exactly
  once and the signed PDF downloadable, including when HR is restarted
  during signing.
- **SC-003**: A declined or expired submission returns its request to
  pending within 1 minute of the outcome (or within one reconciliation
  period if HR was down), in 100% of tests.
- **SC-004**: 0 cross-tenant reads or changes, 0 self-approvals, 0 approvals
  outside the routing, and 0 readings of colleagues' request details by
  employees succeed in the authorization test suite.
- **SC-005**: 1,000 concurrent approvals against one allowance never make
  its remaining balance negative, and every refund restores exactly what was
  charged.
- **SC-006**: The calendar month view for 200 people and 1,000 absences
  renders in under 2 seconds.
- **SC-007**: A carry-over run for 500 people completes in under 30 seconds
  and a second run changes nothing.
- **SC-008**: 0 reasons, notes, names or e-mail addresses are found in HR's
  events or logs in an end-to-end leak test.
- **SC-009**: Only the hr module's mesh identity can call signing's module
  API; every other identity is refused in the policy tests.

## Assumptions

- **People**: HR people are the tenant's members in auth. HR stores user IDs
  and resolves names from auth profiles and e-mail addresses through auth's
  policy-restricted contacts lookup (added in feature 027).
- **Departments replace org units**: HR departments are HR's own data
  (decision 2). Other modules cannot see them.
- **Working week**: Monday–Friday for every tenant. Configurable working
  weeks are out of scope.
- **Half days**: only the first and last day of a request can be half days.
  Hourly leave is out of scope.
- **Signing documents stay in signing**: HR stores the submission reference,
  not the PDF. Signing's retention applies.
- **E-mails**: tenant-level system templates in the notification module, as
  the signing module does (feature 027), English and Bulgarian.
- **Scheduler**: carry-over is a tenant-scoped task type created by the
  tenant's HR administrator (suggested: yearly on 1 January). Signing
  reconciliation is a platform-scoped task type, every 15 minutes.
- **Data migration**: migrating v3 HR data is not required; v4 starts empty.
  Allowances are entered by administrators (or restored from a v4 backup).
- **Localization**: the UI is in English, like the other v4 modules.

## Dependencies

- v4 framework: mesh identity, per-module policies, audit, event bus, and
  permission registration with module roles (feature 019).
- Signing v4 (feature 027): the new module API (FR-039), HR as a submission
  source, and its published submission events.
- Auth: tenant members, profiles, contacts lookup, permissions.
- Notification v4: tenant e-mail channel and system templates for hr.
- Scheduler v4 (feature 026): carry-over and reconciliation task types.
- Portal v4: gateway route and federated UI remote.
- go-tangra-docker: stack service, database and role, mesh policies
  (hr → signing, hr → notification, scheduler → hr), event bus access.

## Out of Scope

- Payroll, time tracking, hourly leave and overtime.
- Configurable working weeks and regional holiday calendars shipped with the
  module (holidays are entered or imported per tenant).
- Multi-level approval chains beyond one approver per request.
- Employee records beyond leave (contracts, salaries, documents), other than
  the signed leave documents kept by signing.
- Migrating v3 HR data.
