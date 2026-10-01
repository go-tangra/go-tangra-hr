// One server-paged table (go-tangra specs/032-server-side-tables): page, size
// and sort live in the URL (kit useListQuery), the current page of rows and
// its total are loaded whenever they change. Filters are read through
// `filters()`; `search()` applies changed filters from page 1, `load()`
// reloads the current page in place (live updates keep page and sort).
import { ref, watch, type Ref } from 'vue'
import { useListQuery, type ListQueryOptions } from '@go-tangra/ui'
import { api, describe } from '@/api/client'

/** The list contract response. */
export interface Page<T> {
  items: T[]
  total: number
  page: number
  page_size: number
  sort: string
  order: 'asc' | 'desc'
}

type Filters = Record<string, string | number | boolean | undefined>

export function useServerList<T>(key: string, path: string, opts: ListQueryOptions, filters: () => Filters = () => ({})) {
  const lq = useListQuery(key, opts)
  const items = ref([]) as Ref<T[]>
  const total = ref(0)
  const loading = ref(false)
  const error = ref('')

  async function load(): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      const res = await lq.track(api<Page<T>>('GET', path, undefined, { query: { ...filters(), ...lq.query.value } }))
      if (!res) return // superseded by a newer request
      items.value = res.items ?? []
      total.value = res.total ?? 0
      lq.clampTo(res.page)
    } catch (e) {
      error.value = describe(e)
    } finally {
      loading.value = false
    }
  }

  /** Apply changed filters: back to page 1 (which reloads), or reload in place. */
  function search(): void {
    if (lq.page.value !== 1) lq.resetPage()
    else void load()
  }

  watch(lq.query, () => void load())
  return { lq, items, total, loading, error, load, search }
}

/** Every record of a small list endpoint (reference data such as absence types), page by page at the largest size. */
export async function fetchAll<T>(path: string, query: Filters = {}): Promise<T[]> {
  const out: T[] = []
  for (let page = 1; page <= 50; page++) {
    const res = await api<Page<T>>('GET', path, undefined, { query: { ...query, page, page_size: 200 } })
    out.push(...(res.items ?? []))
    if (out.length >= (res.total ?? 0) || !res.items?.length) break
  }
  return out
}
