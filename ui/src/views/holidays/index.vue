<script setup lang="ts">
// Public holidays of the tenant: skipped when leave days are counted and
// shaded in the calendar. Recurring holidays repeat every year. A file of
// "YYYY-MM-DD,name[,yearly]" lines can be imported (checked first).
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { UiAlert, UiButton, UiCard, UiCheckbox, UiDataTable, UiDialog, UiDrawer, UiInput, UiPage, UiSelect, UiTextarea, useConfirm, useToast, type Column, type SelectOption } from '@go-tangra/ui'
import { api, describeRefusal, postBytes, ApiError } from '@/api/client'
import type { Holiday, ImportResult } from '@/api/types'
import { useServerList } from '@/composables/useServerList'
import { useOrg } from '@/stores/org'
import { date } from '@/utils/format'

const org = useOrg()
const toast = useToast()
const confirm = useConfirm()
const thisYear = new Date().getFullYear()
const year = ref(String(thisYear))
const years = computed<SelectOption[]>(() => [-1, 0, 1, 2].map((d) => ({ title: String(thisYear + d), value: String(thisYear + d) })))

// Server-paged (api/openapi/hr.yaml listHolidays): by date or name.
const list = useServerList<Holiday>('holidays', 'holidays', { sortable: ['date', 'name'], defaultSort: { key: 'date', dir: 'asc' } }, () => ({ year: Number(year.value) }))
const { lq, load } = list
watch(year, () => list.search())
onMounted(async () => {
  await org.load().catch(() => {})
  await load()
})

const d = reactive({ open: false, id: '', busy: false, error: '', form: { date: '', name: '', recurring: false } })
function open(h?: Holiday): void {
  d.id = h?.id ?? ''
  d.error = ''
  d.form = { date: h?.date ?? `${year.value}-01-01`, name: h?.name ?? '', recurring: h?.recurring ?? false }
  d.open = true
}
async function save(): Promise<void> {
  d.busy = true
  d.error = ''
  try {
    if (d.id) await api('PUT', `holidays/${d.id}` as 'holidays/{id}', d.form)
    else await api('POST', 'holidays', d.form)
    d.open = false
    await load()
  } catch (e) {
    d.error = describeRefusal(e)
  } finally {
    d.busy = false
  }
}
async function remove(h: Holiday): Promise<void> {
  if (!(await confirm.ask({ title: `Delete ${h.name}?`, danger: true, confirmLabel: 'Delete' }))) return
  try {
    await api('DELETE', `holidays/${h.id}` as 'holidays/{id}')
    await load()
  } catch (e) {
    toast.show({ kind: 'error', title: describeRefusal(e) })
  }
}

const imp = reactive({ open: false, text: '', result: null as ImportResult | null, busy: false, error: '' })
async function runImport(dry: boolean): Promise<void> {
  imp.busy = true
  imp.error = ''
  try {
    imp.result = await postBytes<ImportResult>('holidays/import', imp.text, 'text/plain', { dry_run: dry ? 'true' : undefined })
    if (!dry) {
      toast.show({ kind: 'success', title: `${imp.result.created} holidays imported` })
      imp.open = false
      await load()
    }
  } catch (e) {
    const r = e instanceof ApiError ? (e.detail?.result as ImportResult | undefined) : undefined
    if (r) imp.result = r
    imp.error = describeRefusal(e)
  } finally {
    imp.busy = false
  }
}

type Row = Holiday & Record<string, unknown>
const columns: Column<Row>[] = [
  { key: 'date', label: 'Date', format: (h) => date(h.date), sortable: true },
  { key: 'name', label: 'Name', sortable: true },
  { key: 'recurring', label: 'Every year', width: 'sm', format: (h) => (h.recurring ? 'Yes' : 'No') },
]
</script>

<template>
  <UiPage title="Holidays" subtitle="Public holidays are not counted as leave days">
    <template #actions>
      <UiButton v-if="org.canManage" variant="outline" icon="mdi-file-upload-outline" data-test="holiday-import" @click="imp.open = true; imp.result = null">Import</UiButton>
      <UiButton v-if="org.canManage" icon="mdi-plus" data-test="holiday-new" @click="open()">New holiday</UiButton>
    </template>
    <div class="flex min-w-0 flex-col gap-3">
      <UiCard>
        <UiSelect id="holiday-year" v-model="year" label="Year" :options="years" size="sm" data-test="holiday-year" />
      </UiCard>
      <UiAlert v-if="list.error.value" kind="error">{{ list.error.value }}</UiAlert>
      <UiCard :padded="false">
        <UiDataTable
          :items="(list.items.value as Row[])" :total="list.total.value" :page="lq.page.value" :page-size="lq.pageSize.value" :sort="lq.sort.value"
          :loading="list.loading.value" :columns="columns" caption="Holidays" empty-title="No holidays" :clickable="org.canManage"
          :row-attrs="(h) => ({ 'data-test': 'holiday-row-' + h.id })" data-test="holidays-table" @row-click="open($event)"
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

    <UiDrawer v-model="d.open" :title="d.id ? 'Edit holiday' : 'New holiday'">
      <form class="flex flex-col gap-3" data-test="holiday-form" @submit.prevent="save">
        <UiInput id="holiday-date" v-model="d.form.date" label="Date" type="date" required data-test="holiday-date" />
        <UiInput id="holiday-name" v-model="d.form.name" label="Name" required data-test="holiday-name" />
        <UiCheckbox id="holiday-recurring" v-model="d.form.recurring" label="Every year on this day" />
        <UiAlert v-if="d.error" kind="error">{{ d.error }}</UiAlert>
      </form>
      <template #actions>
        <UiButton variant="text" @click="d.open = false">Cancel</UiButton>
        <UiButton :loading="d.busy" data-test="holiday-save" @click="save">Save</UiButton>
      </template>
    </UiDrawer>

    <UiDialog v-model="imp.open" title="Import holidays" size="lg">
      <div class="flex flex-col gap-2">
        <UiTextarea id="holiday-import-text" v-model="imp.text" label="One holiday per line: YYYY-MM-DD,name[,yearly]" :rows="10" data-test="holiday-import-text" />
        <div v-if="imp.result" class="text-sm" data-test="holiday-import-result">
          <p>{{ imp.result.created }} to create · {{ imp.result.skipped }} already present</p>
          <p v-for="e in imp.result.errors" :key="e.line" class="text-error">Line {{ e.line }}: {{ e.reason }}</p>
        </div>
        <UiAlert v-if="imp.error" kind="error">{{ imp.error }}</UiAlert>
      </div>
      <template #actions>
        <UiButton variant="text" @click="imp.open = false">Close</UiButton>
        <UiButton variant="outline" :loading="imp.busy" data-test="holiday-import-check" @click="runImport(true)">Check</UiButton>
        <UiButton :loading="imp.busy" :disabled="!imp.result || (imp.result.errors?.length ?? 0) > 0" data-test="holiday-import-run" @click="runImport(false)">Import</UiButton>
      </template>
    </UiDialog>
  </UiPage>
</template>
