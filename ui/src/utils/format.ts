import type { RequestStatus, ValueKey } from '@/api/types'

export const STATUS_LABELS: Record<RequestStatus, string> = {
  pending: 'Pending',
  awaiting_signing: 'Awaiting signing',
  approved: 'Approved',
  rejected: 'Rejected',
  cancelled: 'Cancelled',
  revoked: 'Revoked',
}

export const STATUS_COLORS = {
  pending: 'warning',
  awaiting_signing: 'info',
  approved: 'success',
  rejected: 'error',
  cancelled: 'neutral',
  revoked: 'neutral',
} as const

export const SIGNING_NOTES: Record<string, string> = {
  declined: 'The signing was declined; the request is back to pending.',
  expired: 'The signing expired; the request is back to pending.',
  cancelled: 'The signing was cancelled; the request is back to pending.',
}

export const VALUE_LABELS: Record<ValueKey, string> = {
  employee_name: 'Employee name',
  department: 'Department',
  absence_type: 'Absence type',
  start_date: 'Start date',
  end_date: 'End date',
  days: 'Number of days',
  reason: 'Reason',
  approver_name: 'Approver name',
  today: "Today's date",
}

/** "3" or "3.5" days, with the unit. */
export function days(n: number | undefined | null): string {
  const v = Number(n ?? 0)
  const s = Number.isInteger(v) ? String(v) : v.toFixed(1)
  return `${s} ${Math.abs(v) === 1 ? 'day' : 'days'}`
}

/** A calendar date (YYYY-MM-DD) as "3 Aug 2026". */
export function date(iso: string | undefined | null): string {
  if (!iso) return ''
  const [y, m, d] = iso.split('-').map(Number)
  if (!y || !m || !d) return iso
  return new Date(Date.UTC(y, m - 1, d)).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })
}

/** A date range, collapsed when it is one day. */
export function range(start: string, end: string): string {
  return start === end ? date(start) : `${date(start)} – ${date(end)}`
}

/** A timestamp as a local date and time. */
export function when(iso: string | null | undefined): string {
  if (!iso) return ''
  const t = new Date(iso)
  return Number.isNaN(t.getTime()) ? '' : t.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

/** Today as YYYY-MM-DD (local). */
export function today(now = new Date()): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())}`
}
