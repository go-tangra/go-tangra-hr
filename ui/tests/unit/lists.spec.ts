// Server-side tables (go-tangra specs/032): the hr lists send page,
// page_size, sort and order, sort the whole list from the headers (spec
// fields only), keep page/size/sort in the URL, adopt the server's clamped
// page and go back to page 1 when filters change.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, reactive } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import RequestList from '@/views/requests/list.vue'
import Holidays from '@/views/holidays/index.vue'
import { fetchAll, useServerList } from '@/composables/useServerList'

type Call = { url: string; init: RequestInit }
function fetchMock(handler: (url: string, q: URLSearchParams) => unknown): Call[] {
  const calls: Call[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    calls.push({ url, init })
    return new Response(JSON.stringify(handler(url, new URL(url, 'https://x').searchParams)), { status: 200 })
  }))
  return calls
}

const params = (c: Call | undefined) => new URL(c!.url, 'https://x').searchParams
const pageOf = (q: URLSearchParams, total: number, items: unknown[] = []) =>
  ({ items, total, page: Number(q.get('page') ?? 1), page_size: Number(q.get('page_size') ?? 25), sort: q.get('sort'), order: q.get('order') })
const referenceData = (url: string, q: URLSearchParams): unknown => {
  if (url.includes('/me')) return { user_id: 'hana', permissions: ['hr:read', 'hr:manage'], manages: [] }
  if (url.includes('/absence-types')) return pageOf(q, 0)
  return { items: [] }
}

describe('server-paged hr tables', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('pages and sorts requests over the whole list', async () => {
    const calls = fetchMock((url, q) => (url.includes('/requests')
      ? pageOf(q, 120, [{ id: 'r' + q.get('page'), user_id: 'maria', user_name: 'Maria', absence_type_id: 't', start_date: '2026-07-06', end_date: '2026-07-06', days: 1, status: 'pending', created_at: '2026-07-01T09:00:00Z' }])
      : referenceData(url, q)))
    const w = mount(RequestList, { props: { view: 'all', showPerson: true } })
    await flushPromises()
    const lists = () => calls.filter((c) => c.url.includes('/requests'))
    const last = () => params(lists().at(-1))
    expect([last().get('view'), last().get('page'), last().get('page_size'), last().get('sort'), last().get('order')]).toEqual(['all', '1', '25', 'start_date', 'desc'])
    expect(w.text()).toContain('of 120')
    await w.find('[aria-label="Page 3"]').trigger('click')
    await flushPromises()
    expect(last().get('page')).toBe('3')
    // The person column sorts by name on the server; first click ascending, then descending, from page 1.
    const header = (label: string) => w.findAll('th button').find((b) => b.text().startsWith(label))
    await header('Person')!.trigger('click')
    await flushPromises()
    expect([last().get('sort'), last().get('order'), last().get('page')]).toEqual(['user', 'asc', '1'])
    await header('Person')!.trigger('click')
    await flushPromises()
    expect(last().get('order')).toBe('desc')
    await header('Days')!.trigger('click')
    await flushPromises()
    expect([last().get('sort'), last().get('order')]).toEqual(['days', 'desc'])
    // Only the server's sort fields have a control.
    expect(header('Type')).toBeUndefined()
    // A reload (live update) keeps page and sort.
    await w.find('[aria-label="Page 2"]').trigger('click')
    await flushPromises()
    await w.setProps({ reload: 1 })
    await flushPromises()
    expect([last().get('page'), last().get('sort'), last().get('order')]).toEqual(['2', 'days', 'desc'])
    w.unmount()
  })

  it('keeps holiday page, size and sort in the URL and adopts the clamped page', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/hr/holidays', component: Holidays }] })
    await router.push('/hr/holidays?holidays.page=9&holidays.size=10&holidays.sort=name&holidays.order=desc')
    await router.isReady()
    const calls = fetchMock((url, q) => (url.includes('/holidays') ? { ...pageOf(q, 31), page: Math.min(Number(q.get('page')), 4) } : referenceData(url, q)))
    mount(Holidays, { global: { plugins: [router] } })
    await flushPromises()
    const first = params(calls.find((c) => c.url.includes('/holidays')))
    expect([first.get('page'), first.get('page_size'), first.get('sort'), first.get('order'), first.get('year')]).toEqual(['9', '10', 'name', 'desc', String(new Date().getFullYear())])
    expect(router.currentRoute.value.query['holidays.page']).toBe('4')
    // Unknown sort fields fall back to the default silently.
    await router.push('/hr/holidays?holidays.sort=recurring')
    await flushPromises()
    const fb = params(calls.filter((c) => c.url.includes('/holidays')).at(-1))
    expect([fb.get('sort'), fb.get('order')]).toEqual(['date', 'asc'])
  })

  it('returns to page 1 when filters change', async () => {
    const calls = fetchMock((_, q) => pageOf(q, 100))
    const f = reactive({ year: 2026 })
    let list!: ReturnType<typeof useServerList>
    mount(defineComponent({
      setup() {
        list = useServerList('t', 'allowances', { sortable: ['year', 'user'], defaultSort: { key: 'year', dir: 'desc' }, defaultSize: 50 }, () => ({ year: f.year }))
        void list.load()
        return () => h('div')
      },
    }))
    await flushPromises()
    list.lq.setPage(2)
    await flushPromises()
    expect(params(calls.at(-1)).get('page')).toBe('2')
    f.year = 2025
    list.search()
    await flushPromises()
    expect([params(calls.at(-1)).get('page'), params(calls.at(-1)).get('year'), params(calls.at(-1)).get('page_size')]).toEqual(['1', '2025', '50'])
  })

  it('reads every page of reference data at the largest size', async () => {
    const calls = fetchMock((_, q) => pageOf(q, 250, Array.from({ length: q.get('page') === '1' ? 200 : 50 }, (_, i) => ({ id: q.get('page') + '-' + i }))))
    const all = await fetchAll<{ id: string }>('absence-types', { all: true })
    expect(all).toHaveLength(250)
    expect(calls.map((c) => [params(c).get('page'), params(c).get('page_size'), params(c).get('all')])).toEqual([['1', '200', 'true'], ['2', '200', 'true']])
  })
})
