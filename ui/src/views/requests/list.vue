<script setup lang="ts">
// A server-paged table of leave requests for one view (mine, review, all):
// numbered pages with the total, whole-list sorting, page/size/sort in the
// URL per view; filters go back to page 1, live updates reload the page.
import { computed, inject, onBeforeUnmount, onMounted, reactive, watch } from 'vue'
import { routerKey } from 'vue-router'
import { UiAlert, UiCard, UiDataTable, UiSelect, UiStatusChip, type Column, type SelectOption } from '@go-tangra/ui'
import type { LeaveRequest, RequestStatus } from '@/api/types'
import { REQUEST_STATUSES } from '@/api/types'
import { useServerList } from '@/composables/useServerList'
import { coalesce, useLive } from '@/stores/live'
import { useOrg } from '@/stores/org'
import { STATUS_COLORS, STATUS_LABELS, date, days, range } from '@/utils/format'

const props = defineProps<{ view: 'mine' | 'review' | 'all'; showPerson?: boolean; reload?: number }>()
const org = useOrg()
const live = useLive()
const router = inject(routerKey, null)

const f = reactive({ status: '' as RequestStatus | '', type: '', department: '' })
const statusOptions: SelectOption[] = REQUEST_STATUSES.map((s) => ({ title: STATUS_LABELS[s], value: s }))
const typeOptions = computed<SelectOption[]>(() => org.types.map((t) => ({ title: t.name, value: t.id })))
const deptOptions = computed<SelectOption[]>(() => org.departments.map((d) => ({ title: d.name, value: d.id })))

// Sort fields the server allows (api/openapi/hr.yaml listRequests).
const list = useServerList<LeaveRequest>(
  'requests-' + props.view,
  'requests',
  { sortable: ['start_date', 'end_date', 'status', 'days', 'created_at', 'user'], defaultSort: { key: 'start_date', dir: 'desc' } },
  () => ({ view: props.view, status: f.status || undefined, type: f.type || undefined, department: f.department || undefined }),
)
const { lq, load } = list
watch(f, () => list.search())
watch(() => props.reload, () => void load())
const refresh = coalesce(() => void load(), 300)
let release: (() => void) | null = null
let unsubscribe: (() => void) | null = null
onMounted(() => {
  void load()
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
  ...(props.showPerson ? [{ key: 'user', label: 'Person', format: (r) => r.user_name ?? '', sortable: true } as Column<Row>] : []),
  { key: 'absence_type_id', label: 'Type', format: (r) => org.typeById.get(r.absence_type_id)?.name ?? '' },
  { key: 'start_date', label: 'Dates', format: (r) => range(r.start_date, r.end_date), sortable: true, defaultDir: 'desc' },
  { key: 'days', label: 'Days', width: 'sm', format: (r) => days(r.days), sortable: true, defaultDir: 'desc' },
  { key: 'status', label: 'Status', width: 'sm', sortable: true },
  { key: 'created_at', label: 'Requested', width: 'sm', format: (r) => (r.created_at ? date(r.created_at.slice(0, 10)) : ''), sortable: true, defaultDir: 'desc', hideOnStack: true },
])
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
    <UiAlert v-if="list.error.value" kind="error">{{ list.error.value }}</UiAlert>
    <UiCard :padded="false">
      <UiDataTable
        :items="(list.items.value as Row[])"
        :total="list.total.value"
        :page="lq.page.value"
        :page-size="lq.pageSize.value"
        :sort="lq.sort.value"
        :columns="columns"
        :loading="list.loading.value"
        :caption="view === 'review' ? 'Requests waiting for your decision' : 'Leave requests'"
        :empty-title="view === 'review' ? 'Nothing to review' : 'No requests'"
        clickable
        :row-attrs="(r) => ({ 'data-test': 'request-row-' + r.id })"
        :data-test="'requests-table-' + view"
        @row-click="router?.push({ name: 'hr-request', params: { id: $event.id } })"
        @update:page="lq.setPage"
        @update:page-size="lq.setPageSize"
        @update:sort="lq.setSort"
      >
        <template #cell-status="{ row }">
          <UiStatusChip :status="row.status" :label="STATUS_LABELS[row.status]" :colors="STATUS_COLORS" :data-test="'request-status-' + row.id" />
        </template>
      </UiDataTable>
    </UiCard>
  </div>
</template>
