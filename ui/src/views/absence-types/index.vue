<script setup lang="ts">
// Absence types and allowance pools. A type deducts from an allowance (its
// own, or its pool's), may need approval and a signed leave document; a pool
// shares one balance between several types. Only HR administrators change
// them; types in use can be deactivated but not deleted.
import { computed, onMounted, reactive, ref } from 'vue'
import { UiAlert, UiButton, UiCard, UiCheckbox, UiDataTable, UiDrawer, UiInput, UiNumberInput, UiPage, UiSelect, UiTabs, UiTextarea, useConfirm, useToast, type Column, type SelectOption } from '@go-tangra/ui'
import { vCssom } from '@/utils/cssom'
import { api, describe, describeRefusal } from '@/api/client'
import type { AbsenceType, AbsenceTypeWrite, Pool, PoolWrite, SigningSettings } from '@/api/types'
import { useServerList } from '@/composables/useServerList'
import SigningSettingsEditor from '@/components/SigningSettings.vue'
import { useOrg } from '@/stores/org'

const org = useOrg()
const toast = useToast()
const tab = ref('types')
const pools = ref<Pool[]>([])
const error = ref('')
// The types table is server-paged (api/openapi/hr.yaml listAbsenceTypes);
// pool membership reads the whole type list of the org store.
const list = useServerList<AbsenceType>('types', 'absence-types', { sortable: ['sort_order', 'name'], defaultSort: { key: 'sort_order', dir: 'asc' } }, () => ({ all: true }))
const { lq } = list

async function load(): Promise<void> {
  error.value = ''
  try {
    await org.load()
    await list.load()
    pools.value = (await api<{ items: Pool[] }>('GET', 'pools')).items ?? []
  } catch (e) {
    error.value = describe(e)
  }
}
onMounted(load)
const poolOptions = computed<SelectOption[]>(() => pools.value.map((p) => ({ title: p.name, value: p.id })))
const poolName = (id?: string) => pools.value.find((p) => p.id === id)?.name ?? ''

// --- type drawer ---
const emptySigning = (): SigningSettings => ({ template_id: '', employee_party: '', approver_party: '', fields: {} })
const t = reactive({ open: false, id: '', busy: false, error: '', form: {} as Required<Omit<AbsenceTypeWrite, 'signing' | 'carry_over_cap' | 'metadata'>> & { carry_over_cap: number | null; signing: SigningSettings } })
function openType(x?: AbsenceType): void {
  t.id = x?.id ?? ''
  t.error = ''
  t.form = { name: x?.name ?? '', description: x?.description ?? '', color: x?.color || '#3b82f6', icon: x?.icon ?? '', sort_order: x?.sort_order ?? 0,
    active: x?.active ?? true, deducts: x?.deducts ?? true, requires_approval: x?.requires_approval ?? true, pool_id: x?.pool_id ?? '',
    carry_over_cap: x?.carry_over_cap ?? null, requires_signing: x?.requires_signing ?? false, signing: x?.signing ?? emptySigning() }
  t.open = true
}
async function saveType(): Promise<void> {
  t.busy = true
  t.error = ''
  const f = t.form
  const body: AbsenceTypeWrite = { name: f.name, description: f.description, color: f.color, icon: f.icon, sort_order: Number(f.sort_order) || 0,
    active: f.active, deducts: f.deducts || f.pool_id !== '', requires_approval: f.requires_approval || f.requires_signing, pool_id: f.pool_id,
    carry_over_cap: f.carry_over_cap === null || f.carry_over_cap === undefined || String(f.carry_over_cap) === '' ? null : Number(f.carry_over_cap),
    requires_signing: f.requires_signing, signing: f.requires_signing ? f.signing : null }
  try {
    if (t.id) await api('PUT', `absence-types/${t.id}` as 'absence-types/{id}', body)
    else await api('POST', 'absence-types', body)
    t.open = false
    toast.show({ kind: 'success', title: 'Absence type saved' })
    await load()
    await org.load(true)
  } catch (e) {
    t.error = describeRefusal(e)
  } finally {
    t.busy = false
  }
}

// --- pool drawer ---
const p = reactive({ open: false, id: '', busy: false, error: '', form: { name: '', description: '', color: '#10b981', carry_over_cap: null as number | null } })
function openPool(x?: Pool): void {
  p.id = x?.id ?? ''
  p.error = ''
  p.form = { name: x?.name ?? '', description: x?.description ?? '', color: x?.color || '#10b981', carry_over_cap: x?.carry_over_cap ?? null }
  p.open = true
}
async function savePool(): Promise<void> {
  p.busy = true
  p.error = ''
  const body: PoolWrite = { name: p.form.name, description: p.form.description, color: p.form.color,
    carry_over_cap: p.form.carry_over_cap === null || String(p.form.carry_over_cap) === '' ? null : Number(p.form.carry_over_cap) }
  try {
    if (p.id) await api('PUT', `pools/${p.id}` as 'pools/{id}', body)
    else await api('POST', 'pools', body)
    p.open = false
    await load()
  } catch (e) {
    p.error = describeRefusal(e)
  } finally {
    p.busy = false
  }
}

const confirm = useConfirm()
async function remove(kind: 'type' | 'pool', id: string, name: string): Promise<void> {
  if (!(await confirm.ask({ title: `Delete ${name}?`, text: 'This cannot be undone.', danger: true, confirmLabel: 'Delete' }))) return
  try {
    await api('DELETE', `${kind === 'type' ? 'absence-types' : 'pools'}/${id}` as 'pools/{id}')
    toast.show({ kind: 'success', title: 'Deleted' })
    if (kind === 'type') await org.load(true)
    await load()
  } catch (e) {
    toast.show({ kind: 'error', title: describeRefusal(e) })
  }
}

type TRow = AbsenceType & Record<string, unknown>
const typeColumns: Column<TRow>[] = [
  { key: 'name', label: 'Name', sortable: true },
  { key: 'sort_order', label: 'Order', width: 'sm', format: (x) => String(x.sort_order ?? 0), sortable: true },
  { key: 'rules', label: 'Rules', format: (x) => [x.deducts ? `Deducts${x.pool_id ? ' (' + poolName(x.pool_id) + ')' : ''}` : 'No deduction',
    x.requires_approval ? 'Approval' : 'No approval', x.requires_signing ? 'Signed document' : ''].filter(Boolean).join(' · ') },
  { key: 'active', label: 'Active', width: 'sm', format: (x) => (x.active ? 'Yes' : 'No') },
]
type PRow = Pool & Record<string, unknown>
const poolColumns: Column<PRow>[] = [
  { key: 'name', label: 'Name' },
  { key: 'members', label: 'Absence types', format: (x) => org.types.filter((y) => y.pool_id === x.id).map((y) => y.name).join(', ') },
  { key: 'carry_over_cap', label: 'Carry-over cap', format: (x) => (x.carry_over_cap === null || x.carry_over_cap === undefined ? 'No cap' : String(x.carry_over_cap)) },
]
const tabs = [{ key: 'types', label: 'Absence types' }, { key: 'pools', label: 'Allowance pools' }]
</script>

<template>
  <UiPage title="Absence types" subtitle="Kinds of absence, their rules and shared allowance pools">
    <template #actions>
      <UiButton v-if="org.canManage && tab === 'types'" icon="mdi-plus" data-test="type-new" @click="openType()">New absence type</UiButton>
      <UiButton v-if="org.canManage && tab === 'pools'" icon="mdi-plus" data-test="pool-new" @click="openPool()">New pool</UiButton>
    </template>
    <div class="flex min-w-0 flex-col gap-3">
      <UiAlert v-if="error || list.error.value" kind="error">{{ error || list.error.value }}</UiAlert>
      <UiTabs v-model="tab" :tabs="tabs" />
      <UiCard v-if="tab === 'types'" :padded="false">
        <UiDataTable
          :items="(list.items.value as TRow[])" :total="list.total.value" :page="lq.page.value" :page-size="lq.pageSize.value" :sort="lq.sort.value"
          :loading="list.loading.value" :columns="typeColumns" caption="Absence types" empty-title="No absence types" :clickable="org.canManage"
          :row-attrs="(x) => ({ 'data-test': 'type-row-' + x.id })" data-test="types-table" @row-click="openType($event)"
          @update:page="lq.setPage" @update:page-size="lq.setPageSize" @update:sort="lq.setSort"
        >
          <template #cell-name="{ row }">
            <span class="inline-flex items-center gap-2"><span v-cssom="{ 'background-color': row.color || '#64748b' }" class="inline-block size-3 rounded-full" />{{ row.name }}</span>
          </template>
          <template v-if="org.canManage" #actions="{ row }">
            <div class="flex justify-end" @click.stop>
              <UiButton size="xs" variant="text" color="error" icon="mdi-delete" icon-only label="Delete" @click="remove('type', row.id, row.name)" />
            </div>
          </template>
        </UiDataTable>
      </UiCard>
      <UiCard v-else :padded="false">
        <UiDataTable
          :items="(pools as PRow[])" :columns="poolColumns" caption="Allowance pools" empty-title="No pools" :clickable="org.canManage"
          :row-attrs="(x) => ({ 'data-test': 'pool-row-' + x.id })" data-test="pools-table" @row-click="openPool($event)"
        >
          <template v-if="org.canManage" #actions="{ row }">
            <div class="flex justify-end" @click.stop>
              <UiButton size="xs" variant="text" color="error" icon="mdi-delete" icon-only label="Delete" @click="remove('pool', row.id, row.name)" />
            </div>
          </template>
        </UiDataTable>
      </UiCard>
    </div>

    <UiDrawer v-model="t.open" :title="t.id ? 'Edit absence type' : 'New absence type'" size="lg">
      <form class="flex flex-col gap-3" data-test="type-form" @submit.prevent="saveType">
        <UiInput id="type-name" v-model="t.form.name" label="Name" required data-test="type-name" />
        <UiTextarea id="type-description" v-model="t.form.description" label="Description" :rows="2" />
        <div class="grid grid-cols-2 gap-2">
          <UiInput id="type-color" v-model="t.form.color" label="Color (#rrggbb)" data-test="type-color" />
          <UiNumberInput id="type-sort" v-model="t.form.sort_order" label="Sort order" :min="0" :max="10000" />
        </div>
        <UiCheckbox id="type-active" v-model="t.form.active" label="Active (can be requested)" />
        <UiCheckbox id="type-deducts" v-model="t.form.deducts" label="Deducts from an allowance" data-test="type-deducts" />
        <UiSelect v-if="t.form.deducts" id="type-pool" v-model="t.form.pool_id" label="Allowance pool" :options="poolOptions" placeholder="Its own allowance" clearable data-test="type-pool" />
        <UiNumberInput v-if="t.form.deducts && !t.form.pool_id" id="type-cap" v-model="t.form.carry_over_cap" label="Carry-over cap (days, empty = no cap)" :min="0" :max="365" :step="0.5" />
        <UiCheckbox id="type-approval" v-model="t.form.requires_approval" label="Requires approval" data-test="type-approval" />
        <UiCheckbox id="type-signing" v-model="t.form.requires_signing" label="Requires a signed leave document" data-test="type-signing" />
        <SigningSettingsEditor v-if="t.form.requires_signing" v-model="t.form.signing" />
        <UiAlert v-if="t.error" kind="error" data-test="type-error">{{ t.error }}</UiAlert>
      </form>
      <template #actions>
        <UiButton variant="text" @click="t.open = false">Cancel</UiButton>
        <UiButton :loading="t.busy" data-test="type-save" @click="saveType">Save</UiButton>
      </template>
    </UiDrawer>

    <UiDrawer v-model="p.open" :title="p.id ? 'Edit pool' : 'New pool'">
      <form class="flex flex-col gap-3" data-test="pool-form" @submit.prevent="savePool">
        <UiInput id="pool-name" v-model="p.form.name" label="Name" required data-test="pool-name" />
        <UiTextarea id="pool-description" v-model="p.form.description" label="Description" :rows="2" />
        <UiInput id="pool-color" v-model="p.form.color" label="Color (#rrggbb)" />
        <UiNumberInput id="pool-cap" v-model="p.form.carry_over_cap" label="Carry-over cap (days, empty = no cap)" :min="0" :max="365" :step="0.5" />
        <UiAlert v-if="p.error" kind="error">{{ p.error }}</UiAlert>
      </form>
      <template #actions>
        <UiButton variant="text" @click="p.open = false">Cancel</UiButton>
        <UiButton :loading="p.busy" data-test="pool-save" @click="savePool">Save</UiButton>
      </template>
    </UiDrawer>
  </UiPage>
</template>
