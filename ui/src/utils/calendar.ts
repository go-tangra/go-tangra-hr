// Timeline math for the team calendar (pure, unit tested): the visible days
// of a week / two-week / month view, and where an absence bar sits on them.
import type { CalendarEvent } from '@/api/types'

export type View = 'week' | '2weeks' | 'month'

const DAY = 86_400_000

/** Parses YYYY-MM-DD as a UTC date. */
export function parse(iso: string): Date {
  const [y, m, d] = iso.split('-').map(Number)
  return new Date(Date.UTC(y ?? 1970, (m ?? 1) - 1, d ?? 1))
}

/** Formats a UTC date as YYYY-MM-DD. */
export function iso(d: Date): string {
  return d.toISOString().slice(0, 10)
}

export function addDays(iso0: string, n: number): string {
  return iso(new Date(parse(iso0).getTime() + n * DAY))
}

/** Monday of the week containing day. */
export function monday(day: string): string {
  const d = parse(day)
  const wd = (d.getUTCDay() + 6) % 7
  return addDays(day, -wd)
}

/** The first and last visible day of a view anchored at day. */
export function span(view: View, day: string): { from: string; to: string } {
  if (view === 'month') {
    const d = parse(day)
    const first = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1))
    const last = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 0))
    return { from: iso(first), to: iso(last) }
  }
  const from = monday(day)
  return { from, to: addDays(from, view === 'week' ? 6 : 13) }
}

/** The anchor of the previous or next period. */
export function shift(view: View, day: string, dir: -1 | 1): string {
  if (view === 'month') {
    const d = parse(day)
    return iso(new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + dir, 1)))
  }
  return addDays(monday(day), dir * (view === 'week' ? 7 : 14))
}

export interface DayCell {
  date: string
  weekend: boolean
  holiday: string // holiday name, "" when none
  today: boolean
}

/** The visible days with weekend, holiday and today marks. */
export function cells(from: string, to: string, holidays: { date: string; name: string }[], todayIso: string): DayCell[] {
  const names = new Map(holidays.map((h) => [h.date, h.name]))
  const out: DayCell[] = []
  for (let d = from; d <= to; d = addDays(d, 1)) {
    const wd = parse(d).getUTCDay()
    out.push({ date: d, weekend: wd === 0 || wd === 6, holiday: names.get(d) ?? '', today: d === todayIso })
    if (out.length > 400) break
  }
  return out
}

export interface Bar {
  event: CalendarEvent
  start: number // first visible column (0-based)
  length: number // visible columns
  clippedStart: boolean
  clippedEnd: boolean
}

/** Places an event on the visible days (null when outside). */
export function bar(e: CalendarEvent, from: string, to: string): Bar | null {
  if (e.end_date < from || e.start_date > to) return null
  const s = e.start_date < from ? from : e.start_date
  const t = e.end_date > to ? to : e.end_date
  const start = Math.round((parse(s).getTime() - parse(from).getTime()) / DAY)
  const length = Math.round((parse(t).getTime() - parse(s).getTime()) / DAY) + 1
  return { event: e, start, length, clippedStart: e.start_date < from, clippedEnd: e.end_date > to }
}

/** A title for the visible period ("August 2026", "3 – 16 Aug 2026"). */
export function title(view: View, from: string, to: string): string {
  const f = parse(from)
  const t = parse(to)
  if (view === 'month') return f.toLocaleDateString(undefined, { month: 'long', year: 'numeric', timeZone: 'UTC' })
  const opt: Intl.DateTimeFormatOptions = { day: 'numeric', month: 'short', timeZone: 'UTC' }
  return `${f.toLocaleDateString(undefined, opt)} – ${t.toLocaleDateString(undefined, { ...opt, year: 'numeric' })}`
}
