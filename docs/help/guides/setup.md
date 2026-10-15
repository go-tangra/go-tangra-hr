---
title: Set up leave
summary: Create absence types and allowance pools, give people allowances, build the department tree, enter public holidays and carry unused days into the next year.
kind: guide
order: 30
tags: [hr, setup, absence-types, allowances, departments, holidays, carry-over]
requires: hr:manage
---
Everything on this page needs `hr:manage` (the *HR administrator* role, and
the tenant's owners and admins). People with `hr:read` see the same pages
without changing anything.

## 1. Absence types

Open **Absence types** and choose **New absence type**.

| Field | Meaning |
|---|---|
| Name, Description, Color, Sort order | how the type shows in forms and on the calendar |
| Active (can be requested) | inactive types cannot be chosen for new requests |
| Deducts from an allowance | approved days come off the person's allowance |
| Allowance pool | empty: the type has its own allowance; otherwise the pool's allowance is used |
| Carry-over cap | for a type with its own allowance: at most this many unused days move to next year; empty means no cap |
| Requires approval | off: requests are approved when submitted |
| Requires a signed leave document | see [signed leave forms](signed-forms.md); needs **Requires approval** |

A type that does not deduct (for example sick leave) needs no allowance. A
type can be deleted only while no request or allowance uses it; otherwise
make it inactive.

## 2. Allowance pools

On the **Allowance pools** tab, **New pool** creates a pool with a name and
an optional **Carry-over cap**. Then choose the pool in each absence type that
should share it. Allowances are then given for the pool, not the types.

## 3. Allowances

Open **Allowances** and choose **New allowance**: the **Person**, the
**Year**, the **Absence type or pool**, the **Days** (0–365, half days
allowed) and optionally days **Carried over** and notes. A person has at most
one allowance per year and type or pool.

**Used** days change only through requests. Lowering an allowance below what
was already used is allowed and shows as a negative remaining balance. An
allowance that requests were charged to cannot be deleted.

## 4. Departments

Open **Departments**. **New department** takes a name, an optional parent
(**Part of**) and an optional **Manager**. Select a department to add and
remove members. A person belongs to one department.

The tree decides who approves; see
[who approves](review.md#who-approves). Changes apply to open requests
straight away. Only empty departments can be deleted.

## 5. Public holidays

Open **Holidays** and choose **New holiday** for a date and name; tick
**Every year on this day** for fixed-date holidays. Holidays are not counted
as leave days. Changing holidays later does not change requests already
filed.

To enter many at once, choose **Import** and paste one holiday per line:

```text
# date,name[,yearly]
2027-01-01,New Year's Day,yearly
2027-03-03,Liberation Day,yearly
2027-12-31,Company day off
```

**Check** reports refused lines without saving; **Import** saves only when
no line is refused. Dates that already exist are skipped. At most 500 lines.

## 6. Year-end carry-over

In **Allowances**, choose **Carry over**, pick the year to carry **from** and
choose **Preview**. For every allowance of that year it shows the unused
days, what moves to the next year and the change:

- unused days = total + carried over − used (never below zero);
- carried days = the unused days, at most the cap of the type or pool;
- **New allowance**: the person has no allowance for the next year yet; one
  is created with the same total and the carried days;
- **Update**: next year's allowance gets the new carried-over value;
- **Already carried**: nothing to change.

Choose **Carry over** to apply it. Running it again gives the same result,
so it is safe to repeat after late corrections.

The scheduler can do this every year: create a task of type **Carry over
leave allowances** (`hr:carry-over`, suggested `30 0 1 1 *`, 00:30 on
1 January). Without a `source_year` in its payload it carries the previous
year.

Related: [example: annual leave](../examples/annual-leave.md),
[reference](../reference.md).
