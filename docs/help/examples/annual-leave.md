---
title: "Example: annual leave for a small company"
summary: Set up an annual leave pool with a carry-over cap, sick leave without approval, two departments and holidays, then follow a request that spans New Year.
kind: example
order: 50
tags: [hr, example, setup, allowances, carry-over]
requires: hr:manage
---
**Goal.** A company of a few people gives everyone 20 days of annual leave a
year, of which at most 5 unused days move to the next year. Sick leave is
recorded but needs no approval and takes no days. Engineers are approved by
the head of engineering, she by the managing director.

## 1. Pool and types

In **Absence types → Allowance pools**, create the pool:

| Name | Carry-over cap |
|---|---|
| Annual leave | 5 |

Then two absence types:

| Name | Deducts | Allowance pool | Requires approval |
|---|:-:|---|:-:|
| Vacation | yes | Annual leave | yes |
| Sick leave | no | — | no |

Later, more types can join the pool (for example a company day off), and
they all draw from the same 20 days.

## 2. Departments

| Department | Part of | Manager | Members |
|---|---|---|---|
| Company | — | Maria (managing director) | Maria |
| Engineering | Company | Ivan | Ivan, Elena |

Elena's requests go to Ivan. Ivan cannot approve his own, so they go up the
tree to Maria.
Maria manages the top department herself, so her requests go to the HR
administrators.

## 3. Holidays

In **Holidays → Import**:

```text
2026-12-24,Christmas Eve,yearly
2026-12-25,Christmas Day,yearly
2026-12-26,Christmas Day 2,yearly
2027-01-01,New Year's Day,yearly
```

## 4. Allowances

In **Allowances**, give Elena 20 days of **Annual leave** for 2026 and,
already in December, 20 days for 2027. By December she has used 12 days of
2026.

## 5. A request across New Year

Elena requests **Vacation** from Monday 28 December 2026 to Friday
8 January 2027. The form counts:

| Year | Working days | Why |
|---|---|---|
| 2026 | 4 | 28–31 December, Monday to Thursday |
| 2027 | 5 | 4–8 January; 1 January is a holiday, 2–3 January a weekend |

That is 9 days. The preview shows 4 days left in 2026 (20 − 12 − 4) and 15
in 2027 (20 − 5), and Ivan as approver. Without the 2027 allowance the
request would be refused with `no_allowance`.

Ivan approves. The 4 days are charged to 2026 and the 5 days to 2027.

## 6. Carry-over

On 1 January, HR opens **Allowances → Carry over** from 2026 and chooses
**Preview**. Elena has 4 unused days, under the cap of 5, so 4 days move:
the change is **Update**, because her 2027 allowance already exists. After
**Carry over**, her 2027 balance is 20 total + 4 carried − 5 used = 19 days.

Had she 8 unused days, only 5 would move. A scheduler task of type **Carry
over leave allowances** can do this every year.

## 7. Sick leave

When Elena reports sick for a day, she files a **Sick leave** request. It is
approved when submitted, shows on the calendar, and leaves her balance
unchanged.

Related: [set up leave](../guides/setup.md), [review requests](../guides/review.md).
