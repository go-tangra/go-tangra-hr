<script setup lang="ts">
// HR statistics (absence types, open, approved and rejected requests, who is
// away today) and, for HR administrators, backup and restore.
import { onMounted, ref } from 'vue'
import { UiAlert, UiButton, UiCard, UiFilePicker, UiPage, UiSelect, UiStatGrid, UiStatTile, useConfirm, useToast, type SelectOption } from '@go-tangra/ui'
import { api, describe, describeRefusal, download, postBytes, saveBlob } from '@/api/client'
import type { Stats } from '@/api/types'
import { useOrg } from '@/stores/org'

const org = useOrg()
const toast = useToast()
const confirm = useConfirm()
const stats = ref<Stats | null>(null)
const error = ref('')
const file = ref<File | null>(null)
const mode = ref('skip')
const busy = ref('')
const modes: SelectOption[] = [{ title: 'Keep existing records', value: 'skip' }, { title: 'Overwrite existing records', value: 'overwrite' }]

onMounted(async () => {
  try {
    await org.load()
    stats.value = await api<Stats>('GET', 'stats')
  } catch (e) {
    error.value = describe(e)
  }
})

async function exportBackup(): Promise<void> {
  busy.value = 'export'
  try {
    const { blob, filename } = await download('POST', 'backup/export')
    saveBlob(blob, filename || 'hr-backup.json.gz')
  } catch (e) {
    toast.show({ kind: 'error', title: describeRefusal(e) })
  } finally {
    busy.value = ''
  }
}
async function importBackup(): Promise<void> {
  if (!file.value) return
  if (mode.value === 'overwrite' && !(await confirm.ask({ title: 'Overwrite existing records?', text: 'Absence types, pools, allowances, departments and holidays that exist here are replaced by the ones in the backup.', danger: true, confirmLabel: 'Import and overwrite' }))) return
  busy.value = 'import'
  try {
    await postBytes('backup/import', file.value, 'application/gzip', { mode: mode.value })
    toast.show({ kind: 'success', title: 'Backup imported' })
  } catch (e) {
    toast.show({ kind: 'error', title: describeRefusal(e) })
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <UiPage title="HR statistics" subtitle="Leave at a glance">
    <UiAlert v-if="error" kind="error">{{ error }}</UiAlert>
    <div v-if="stats" class="flex min-w-0 flex-col gap-3">
      <UiStatGrid :cols="4" data-test="stats">
        <UiStatTile title="Pending" :value="stats.pending" icon="mdi-clock-outline" color="warning" />
        <UiStatTile title="Awaiting signing" :value="stats.awaiting_signing" icon="mdi-file-document-edit-outline" color="info" />
        <UiStatTile title="Approved" :value="stats.approved" icon="mdi-check-circle-outline" color="success" />
        <UiStatTile title="Rejected" :value="stats.rejected" icon="mdi-cancel" color="error" />
      </UiStatGrid>
      <UiCard title="Away today" data-test="stats-absent">
        <ul class="flex flex-wrap gap-2">
          <li v-for="p in stats.absent_today" :key="p.user_id" class="rounded-box border border-base-300 px-2 py-1">{{ p.name || p.user_id }}</li>
          <li v-if="!stats.absent_today.length" class="text-sm text-base-content/70">Nobody is away today.</li>
        </ul>
      </UiCard>
    </div>
    <UiCard v-if="org.canManage" title="Backup" subtitle="All HR data of this tenant (signed documents stay in the signing module)" class="mt-3" data-test="backup">
      <div class="flex flex-col gap-3">
        <div><UiButton variant="outline" icon="mdi-download" :loading="busy === 'export'" data-test="backup-export" @click="exportBackup">Download backup</UiButton></div>
        <div class="grid grid-cols-1 gap-2 md:grid-cols-3 md:items-end">
          <UiFilePicker id="backup-file" v-model="file" label="Backup file" accept=".gz,application/gzip" data-test="backup-file" />
          <UiSelect id="backup-mode" v-model="mode" label="Existing records" :options="modes" />
          <UiButton :disabled="!file" :loading="busy === 'import'" data-test="backup-import" @click="importBackup">Import</UiButton>
        </div>
      </div>
    </UiCard>
  </UiPage>
</template>
