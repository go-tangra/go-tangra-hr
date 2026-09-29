<script setup lang="ts">
// A server-paged table of leave requests for one view (mine, review, all).
import { computed, inject, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { routerKey } from 'vue-router'
import { UiAlert, UiCard, UiDataTable, UiPagination, UiSelect, UiStatusChip, type Column, type SelectOption } from '@go-tangra/ui'
import { api, describe } from '@/api/client'
import type { LeaveRequest, RequestPage, RequestStatus } from '@/api/types'
import { REQUEST_STATUSES } from '@/api/types'
import { coalesce, useLive } from '@/stores/live'
import { useOrg } from '@/stores/org'
import { STATUS_COLORS, STATUS_LABELS, days, range } from '@/utils/format'

const props = defineProps<{ view: 'mine' | 'review' | 'all'; showPerson?: boolean; reload?: number }>()
const org = useOrg()
const live = useLive()
const router = inject(routerKey, null)

const items = ref<LeaveRequest[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 25
const loading = ref(false)
const error = ref('')
const f = reactive({ status: '' as RequestStatus | '', type: '', department: '' })
const statusOptions: SelectOption[] = REQUEST_STATUSES.map((s) => ({ title: STATUS_LABELS[s], value: s }))
const typeOptions = computed<SelectOption[]>(() => org.types.map((t) => ({ title: t.name, value: t.id })))
const deptOptions = computed<SelectOption[]>(() => org.departments.map((d) => ({ title: d.name, value: d.id })))

async function load(p = page.value): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const res = await api<RequestPage>('GET', 'requests', undefined, { query: { view: props.view, page: p, page_size: pageSize,
      status: f.status || undefined, type: f.type || undefined, department: f.department || undefined } })
    items.value = res.items ?? []
    total.value = res.total ?? 0
    page.value = p
  } catch (e) {
    error.value = describe(e)
  } finally {
    loading.value = false
  }
}
watch(f, () => void load(1))
watch(() => props.reload, () => void load())
const refresh = coalesce(() => void load(), 500)
let release: (() => void) | null = null
let unsubscribe: (() => void) | null = null
onMounted(() => {
  void load(1)
  release = live.connect()
  unsubscribe = live.on((e) => { if (e.type !== 'hr.calendar.changed') refresh.trigger() })
})
onBeforeUnmount(() => {
  refresh.cancel()
  unsubscribe?.()
  release?.()
})

type Row = LeaveRequest & Record<string, unknown>
const columns = computed<Column<Row>[]>(() => [
  ...(props.showPerson ? [{ key: 'user_name', label: 'Person' } as Column<Row>] : []),
  { key: 'absence_type_id', label: 'Type', format: (r) => org.typeById.get(r.absence_type_id)?.name ?? '' },
  { key: 'start_date', label: 'Dates', format: (r) => range(r.start_date, r.end_date) },
  { key: 'days', label: 'Days', width: 'sm', format: (r) => days(r.days) },
  { key: 'status', label: 'Status', width: 'sm' },
])
const pages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)))
</script>

<template>
  <div class="flex min-w-0 flex-col gap-3">
    <UiCard>
      <div class="grid grid-cols-1 gap-2 md:grid-cols-3" :data-test="'requests-filters-' + view">
        <UiSelect :id="'requests-status-' + view" v-model="f.status" label="Status" :options="statusOptions" placeholder="Any" clearable size="sm" />
        <UiSelect :id="'requests-type-' + view" v-model="f.type" label="Absence type" :options="typeOptions" placeholder="Any" clearable size="sm" />
        <UiSelect v-if="view !== 'mine'" :id="'requests-department-' + view" v-model="f.department" label="Department" :options="deptOptions" placeholder="Any" clearable size="sm" />
      </div>
    </UiCard>
    <UiAlert v-if="error" kind="error">{{ error }}</UiAlert>
    <UiCard :padded="false">
      <UiDataTable
        :items="(items as Row[])"
        :columns="columns"
        :loading="loading"
        :caption="view === 'review' ? 'Requests waiting for your decision' : 'Leave requests'"
        :empty-title="view === 'review' ? 'Nothing to review' : 'No requests'"
        clickable
        :row-attrs="(r) => ({ 'data-test': 'request-row-' + r.id })"
        :data-test="'requests-table-' + view"
        @row-click="router?.push({ name: 'hr-request', params: { id: $event.id } })"
      >
        <template #cell-status="{ row }">
          <UiStatusChip :status="row.status" :label="STATUS_LABELS[row.status]" :colors="STATUS_COLORS" :data-test="'request-status-' + row.id" />
        </template>
      </UiDataTable>
    </UiCard>
    <UiPagination v-if="pages > 1" :has-prev="page > 1" :has-next="page < pages" :label="`Page ${page} of ${pages} · ${total} requests`" @prev="load(page - 1)" @next="load(page + 1)" />
  </div>
</template>
