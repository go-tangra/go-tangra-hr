<script setup lang="ts">
// Allowances: yearly days per person for an absence type or a pool (total,
// carried over, used, remaining). HR readers see everyone, managers their
// departments, others their own. HR administrators add and edit allowances
// and carry unused days into the next year (preview first).
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { UiAlert, UiButton, UiCard, UiDataTable, UiDialog, UiDrawer, UiNumberInput, UiPage, UiSelect, UiTextarea, useConfirm, useToast, type Column, type SelectOption } from '@go-tangra/ui'
import { api, describe, describeRefusal } from '@/api/client'
import type { Allowance, AllowanceWrite, CarryOverPlan, Pool } from '@/api/types'
import { useServerList } from '@/composables/useServerList'
import { useOrg } from '@/stores/org'
import { days } from '@/utils/format'

const org = useOrg()
const toast = useToast()
const confirm = useConfirm()
const thisYear = new Date().getFullYear()
const f = reactive({ year: String(thisYear), user: '' })
const pools = ref<Pool[]>([])
const error = ref('')

const years = computed<SelectOption[]>(() => [-2, -1, 0, 1].map((d) => ({ title: String(thisYear + d), value: String(thisYear + d) })))
const peopleOptions = computed<SelectOption[]>(() => org.people.map((p) => ({ title: p.name, value: p.user_id })))
const targetOptions = computed<SelectOption[]>(() => [
  ...pools.value.map((p) => ({ title: `Pool: ${p.name}`, value: 'pool:' + p.id })),
  ...org.types.filter((t) => t.deducts && !t.pool_id).map((t) => ({ title: t.name, value: 'type:' + t.id })),
])
const target = (a: Allowance) => (a.pool_id ? pools.value.find((p) => p.id === a.pool_id)?.name : org.typeById.get(a.absence_type_id ?? '')?.name) ?? ''

// Server-paged (api/openapi/hr.yaml listAllowances): "user" sorts by name,
// "type" by the absence type or pool name. The year filter always narrows to
// one year, so the table starts sorted by person rather than by year.
const list = useServerList<Allowance>(
  'allowances',
  'allowances',
  { sortable: ['year', 'user', 'type', 'total', 'remaining'], defaultSort: { key: 'user', dir: 'asc' }, defaultSize: 50 },
  () => ({ year: Number(f.year), user: f.user || undefined }),
)
const { lq, load } = list
watch(f, () => list.search())
onMounted(async () => {
  try {
    await org.load()
    if (org.canRead) pools.value = (await api<{ items: Pool[] }>('GET', 'pools')).items ?? []
  } catch (e) {
    error.value = describe(e)
  }
  void load()
})

// --- drawer ---
const d = reactive({ open: false, id: '', busy: false, error: '', form: { user_id: '', year: String(thisYear), target: '', total_days: 20, carried_over: 0, notes: '' } })
function open(a?: Allowance): void {
  d.id = a?.id ?? ''
  d.error = ''
  d.form = { user_id: a?.user_id ?? '', year: a ? String(a.year) : f.year, target: a ? (a.pool_id ? 'pool:' + a.pool_id : 'type:' + a.absence_type_id) : '',
    total_days: a?.total_days ?? 20, carried_over: a?.carried_over ?? 0, notes: a?.notes ?? '' }
  d.open = true
}
async function save(): Promise<void> {
  d.busy = true
  d.error = ''
  const [kind, id] = d.form.target.split(':')
  const body: AllowanceWrite = { user_id: d.form.user_id, year: Number(d.form.year), total_days: Number(d.form.total_days),
    carried_over: Number(d.form.carried_over) || 0, notes: d.form.notes, ...(kind === 'pool' ? { pool_id: id ?? '' } : { absence_type_id: id ?? '' }) }
  try {
    if (d.id) await api('PUT', `allowances/${d.id}` as 'allowances/{id}', body)
    else await api('POST', 'allowances', body)
    d.open = false
    toast.show({ kind: 'success', title: 'Allowance saved' })
    await load()
  } catch (e) {
    d.error = describeRefusal(e)
  } finally {
    d.busy = false
  }
}
async function remove(a: Allowance): Promise<void> {
  if (!(await confirm.ask({ title: 'Delete this allowance?', text: `${org.name(a.user_id)} · ${a.year} · ${target(a)}`, danger: true, confirmLabel: 'Delete' }))) return
  try {
    await api('DELETE', `allowances/${a.id}` as 'allowances/{id}')
    await load()
  } catch (e) {
    toast.show({ kind: 'error', title: describeRefusal(e) })
  }
}

// --- carry-over ---
const co = reactive({ open: false, year: String(thisYear - 1), plan: null as CarryOverPlan | null, busy: false, error: '' })
async function preview(): Promise<void> {
  co.busy = true
  co.error = ''
  try {
    co.plan = await api<CarryOverPlan>('POST', 'carry-over/preview', { source_year: Number(co.year) })
  } catch (e) {
    co.error = describeRefusal(e)
  } finally {
    co.busy = false
  }
}
async function run(): Promise<void> {
  co.busy = true
  try {
    const plan = await api<CarryOverPlan>('POST', 'carry-over', { source_year: Number(co.year) })
    toast.show({ kind: 'success', title: `Carried ${plan.source_year} into ${plan.source_year + 1}`, text: `${plan.created} created, ${plan.updated} updated` })
    co.open = false
    f.year = String(plan.source_year + 1)
  } catch (e) {
    co.error = describeRefusal(e)
  } finally {
    co.busy = false
  }
}

type Row = Allowance & Record<string, unknown>
const columns: Column<Row>[] = [
  { key: 'user', label: 'Person', format: (a) => org.name(a.user_id), sortable: true },
  { key: 'type', label: 'For', format: (a) => target(a), sortable: true },
  { key: 'total', label: 'Total', width: 'sm', format: (a) => days(a.total_days), sortable: true, defaultDir: 'desc' },
  { key: 'carried_over', label: 'Carried', width: 'sm', format: (a) => days(a.carried_over) },
  { key: 'used_days', label: 'Used', width: 'sm', format: (a) => days(a.used_days) },
  { key: 'remaining', label: 'Remaining', width: 'sm', format: (a) => days(a.remaining), sortable: true, defaultDir: 'desc' },
]
type CRow = CarryOverPlan['items'][number] & Record<string, unknown>
const carryColumns: Column<CRow>[] = [
  { key: 'user_id', label: 'Person', format: (i) => org.name(i.user_id ?? '') },
  { key: 'unused', label: 'Unused', format: (i) => days(i.unused) },
  { key: 'carried', label: 'Carried over', format: (i) => days(i.carried) },
  { key: 'action', label: 'Change', format: (i) => ({ create: 'New allowance', update: 'Update', none: 'Already carried' })[i.action ?? 'none'] ?? '' },
]
</script>

<template>
  <UiPage title="Allowances" subtitle="Yearly leave days per person">
    <template #actions>
      <UiButton v-if="org.canManage" variant="outline" icon="mdi-arrow-right" data-test="carry-open" @click="co.open = true; co.plan = null">Carry over</UiButton>
      <UiButton v-if="org.canManage" icon="mdi-plus" data-test="allowance-new" @click="open()">New allowance</UiButton>
    </template>
    <div class="flex min-w-0 flex-col gap-3">
      <UiCard>
        <div class="grid grid-cols-1 gap-2 md:grid-cols-3">
          <UiSelect id="allowance-year" v-model="f.year" label="Year" :options="years" size="sm" data-test="allowance-year" />
          <UiSelect id="allowance-user" v-model="f.user" label="Person" :options="peopleOptions" placeholder="Everyone" clearable size="sm" />
        </div>
      </UiCard>
      <UiAlert v-if="error || list.error.value" kind="error">{{ error || list.error.value }}</UiAlert>
      <UiCard :padded="false">
        <UiDataTable
          :items="(list.items.value as Row[])" :total="list.total.value" :page="lq.page.value" :page-size="lq.pageSize.value" :sort="lq.sort.value"
          :columns="columns" :loading="list.loading.value" caption="Allowances" empty-title="No allowances"
          :clickable="org.canManage" :row-attrs="(a) => ({ 'data-test': 'allowance-row-' + a.id })" data-test="allowances-table" @row-click="open($event)"
          @update:page="lq.setPage" @update:page-size="lq.setPageSize" @update:sort="lq.setSort"
        >
          <template v-if="org.canManage" #actions="{ row }">
            <div class="flex justify-end" @click.stop>
              <UiButton size="xs" variant="text" color="error" icon="mdi-delete" icon-only label="Delete" @click="remove(row)" />
            </div>
          </template>
        </UiDataTable>
      </UiCard>
    </div>

    <UiDrawer v-model="d.open" :title="d.id ? 'Edit allowance' : 'New allowance'">
      <form class="flex flex-col gap-3" data-test="allowance-form" @submit.prevent="save">
        <UiSelect id="allowance-form-user" v-model="d.form.user_id" label="Person" :options="peopleOptions" required data-test="allowance-form-user" />
        <UiSelect id="allowance-form-year" v-model="d.form.year" label="Year" :options="years" required />
        <UiSelect id="allowance-form-target" v-model="d.form.target" label="Absence type or pool" :options="targetOptions" required data-test="allowance-form-target" />
        <div class="grid grid-cols-2 gap-2">
          <UiNumberInput id="allowance-form-total" v-model="d.form.total_days" label="Days" :min="0" :max="365" :step="0.5" data-test="allowance-form-total" />
          <UiNumberInput id="allowance-form-carried" v-model="d.form.carried_over" label="Carried over" :min="0" :max="365" :step="0.5" />
        </div>
        <UiTextarea id="allowance-form-notes" v-model="d.form.notes" label="Notes" :rows="2" />
        <UiAlert v-if="d.error" kind="error" data-test="allowance-error">{{ d.error }}</UiAlert>
      </form>
      <template #actions>
        <UiButton variant="text" @click="d.open = false">Cancel</UiButton>
        <UiButton :loading="d.busy" data-test="allowance-save" @click="save">Save</UiButton>
      </template>
    </UiDrawer>

    <UiDialog v-model="co.open" title="Carry over unused days" size="lg">
      <div class="flex flex-col gap-3">
        <div class="flex items-end gap-2">
          <UiSelect id="carry-year" v-model="co.year" label="From year" :options="years" size="sm" />
          <UiButton variant="outline" :loading="co.busy" data-test="carry-preview" @click="preview">Preview</UiButton>
        </div>
        <p class="text-sm text-base-content/70">Unused days (up to the carry-over cap of the absence type or pool) go into the next year's allowance. Running it again changes nothing.</p>
        <UiDataTable v-if="co.plan" :items="(co.plan.items as CRow[])" :columns="carryColumns" caption="Carry-over preview" empty-title="No allowances in that year" data-test="carry-table" />
        <UiAlert v-if="co.error" kind="error">{{ co.error }}</UiAlert>
      </div>
      <template #actions>
        <UiButton variant="text" @click="co.open = false">Close</UiButton>
        <UiButton :disabled="!co.plan" :loading="co.busy" data-test="carry-run" @click="run">Carry over</UiButton>
      </template>
    </UiDialog>
  </UiPage>
</template>
