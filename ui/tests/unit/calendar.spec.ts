import { describe, expect, it } from 'vitest'
import type { CalendarEvent } from '@/api/types'
import { addDays, bar, cells, monday, shift, span, title } from '@/utils/calendar'

const ev = (start_date: string, end_date: string) => ({ start_date, end_date }) as CalendarEvent

describe('calendar', () => {
  it('finds the Monday of a week, also across a year boundary', () => {
    expect(monday('2026-07-01')).toBe('2026-06-29')
    expect(monday('2026-06-29')).toBe('2026-06-29')
    expect(monday('2027-01-03')).toBe('2026-12-28')
    expect(addDays('2026-12-31', 1)).toBe('2027-01-01')
  })

  it('spans week, two weeks and month views', () => {
    expect(span('week', '2026-07-01')).toEqual({ from: '2026-06-29', to: '2026-07-05' })
    expect(span('2weeks', '2026-07-01')).toEqual({ from: '2026-06-29', to: '2026-07-12' })
    expect(span('month', '2026-02-14')).toEqual({ from: '2026-02-01', to: '2026-02-28' })
    expect(span('month', '2028-02-14').to).toBe('2028-02-29')
  })

  it('shifts to the previous and next period', () => {
    expect(shift('week', '2026-07-01', 1)).toBe('2026-07-06')
    expect(shift('2weeks', '2026-07-01', -1)).toBe('2026-06-15')
    expect(shift('month', '2026-12-15', 1)).toBe('2027-01-01')
    expect(shift('month', '2026-01-15', -1)).toBe('2025-12-01')
  })

  it('marks weekends, holidays and today', () => {
    const c = cells('2026-12-24', '2026-12-27', [{ date: '2026-12-25', name: 'Christmas' }], '2026-12-24')
    expect(c.map((d) => d.date)).toEqual(['2026-12-24', '2026-12-25', '2026-12-26', '2026-12-27'])
    expect(c.map((d) => d.weekend)).toEqual([false, false, true, true])
    expect(c[1]?.holiday).toBe('Christmas')
    expect(c[0]?.today).toBe(true)
    expect(cells('2026-01-01', '2028-01-01', [], '').length).toBeLessThanOrEqual(401)
  })

  it('places and clips absence bars', () => {
    expect(bar(ev('2026-06-01', '2026-06-05'), '2026-06-29', '2026-07-05')).toBeNull()
    expect(bar(ev('2026-07-02', '2026-07-03'), '2026-06-29', '2026-07-05')).toMatchObject({ start: 3, length: 2, clippedStart: false, clippedEnd: false })
    expect(bar(ev('2026-06-25', '2026-07-10'), '2026-06-29', '2026-07-05')).toMatchObject({ start: 0, length: 7, clippedStart: true, clippedEnd: true })
  })

  it('titles the visible period', () => {
    expect(title('month', '2026-08-01', '2026-08-31')).toMatch(/2026/)
    expect(title('week', '2026-08-03', '2026-08-09')).toContain('–')
  })
})
