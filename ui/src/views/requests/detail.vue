<script setup lang="ts">
// One leave request: dates, days, status, reason and notes, approvers and the
// review. The owner can edit reason and notes while pending and cancel;
// routed approvers and HR administrators approve, reject or revoke; the
// signed leave document can be downloaded once signing completed.
import { computed, inject, onMounted, ref } from 'vue'
import { routeLocationKey, routerKey } from 'vue-router'
import { UiAlert, UiButton, UiCard, UiDialog, UiKeyValueTable, UiPage, UiSkeleton, UiStatusChip, UiTextarea, useToast } from '@go-tangra/ui'
import { api, describeRefusal, download, saveBlob } from '@/api/client'
import type { LeaveRequest } from '@/api/types'
import { useOrg } from '@/stores/org'
import { SIGNING_NOTES, STATUS_COLORS, STATUS_LABELS, days, range, when } from '@/utils/format'

const org = useOrg()
const toast = useToast()
const route = inject(routeLocationKey, null)
const router = inject(routerKey, null)
const id = computed(() => String(route?.params.id ?? ''))
const req = ref<LeaveRequest | null>(null)
const error = ref('')
const busy = ref('')

async function load(): Promise<void> {
  error.value = ''
  try {
    req.value = await api<LeaveRequest>('GET', `requests/${id.value}` as 'requests/{id}')
  } catch (e) {
    error.value = describeRefusal(e)
  }
}
onMounted(async () => {
  await org.load().catch(() => {})
  await load()
})

const typeName = computed(() => (req.value ? (org.typeById.get(req.value.absence_type_id)?.name ?? '') : ''))
const facts = computed(() => {
  const r = req.value
  if (!r) return []
  const rows: { label: string; value: string }[] = [
    { label: 'Person', value: r.user_name || org.name(r.user_id) },
    { label: 'Absence type', value: typeName.value },
    { label: 'Dates', value: range(r.start_date, r.end_date) + (r.half_start ? ' (half first day)' : '') + (r.half_end ? ' (half last day)' : '') },
    { label: 'Days', value: days(r.days) },
    { label: 'Approvers', value: (r.approver_names ?? []).filter(Boolean).join(', ') || '—' },
  ]
  if (r.reason) rows.push({ label: 'Reason', value: r.reason })
  if (r.notes) rows.push({ label: 'Notes', value: r.notes })
  if (r.reviewed_at) rows.push({ label: 'Decided', value: `${r.reviewer_name || org.name(r.reviewed_by)} · ${when(r.reviewed_at)}` })
  if (r.review_notes) rows.push({ label: 'Review notes', value: r.review_notes })
  return rows
})

async function act(kind: 'approve' | 'reject' | 'revoke' | 'cancel', notes = ''): Promise<void> {
  busy.value = kind
  try {
    req.value = await api<LeaveRequest>('POST', `requests/${id.value}/${kind}` as 'requests/{id}/approve', kind === 'cancel' ? undefined : { notes })
    const msg = { approve: req.value.status === 'awaiting_signing' ? 'Approved — the leave document was sent for signature' : 'Request approved',
      reject: 'Request rejected', revoke: 'Request revoked', cancel: 'Request cancelled' }[kind]
    toast.show({ kind: 'success', title: msg })
    dialog.value.open = false
  } catch (e) {
    toast.show({ kind: 'error', title: describeRefusal(e) })
  } finally {
    busy.value = ''
  }
}

const dialog = ref({ open: false, kind: 'reject' as 'approve' | 'reject' | 'revoke', notes: '' })
function ask(kind: 'approve' | 'reject' | 'revoke'): void {
  dialog.value = { open: true, kind, notes: '' }
}

const edit = ref({ open: false, reason: '', notes: '' })
async function saveEdit(): Promise<void> {
  try {
    req.value = await api<LeaveRequest>('PUT', `requests/${id.value}` as 'requests/{id}', { reason: edit.value.reason, notes: edit.value.notes })
    edit.value.open = false
  } catch (e) {
    toast.show({ kind: 'error', title: describeRefusal(e) })
  }
}

async function remove(): Promise<void> {
  try {
    await api('DELETE', `requests/${id.value}` as 'requests/{id}')
    toast.show({ kind: 'success', title: 'Request deleted' })
    void router?.push({ name: 'hr-requests' })
  } catch (e) {
    toast.show({ kind: 'error', title: describeRefusal(e) })
  }
}

async function downloadSigned(): Promise<void> {
  busy.value = 'download'
  try {
    const { blob, filename } = await download('GET', `requests/${id.value}/signed-document`)
    saveBlob(blob, filename || 'leave.pdf')
  } catch (e) {
    toast.show({ kind: 'error', title: describeRefusal(e) })
  } finally {
    busy.value = ''
  }
}
const dialogTitle = computed(() => ({ approve: 'Approve request', reject: 'Reject request', revoke: 'Revoke approved leave' })[dialog.value.kind])
</script>

<template>
  <UiPage :title="typeName || 'Leave request'" :subtitle="req ? range(req.start_date, req.end_date) : ''">
    <template #actions>
      <UiStatusChip v-if="req" :status="req.status" :label="STATUS_LABELS[req.status]" :colors="STATUS_COLORS" data-test="request-status" />
    </template>
    <UiAlert v-if="error" kind="error" data-test="request-error">{{ error }}</UiAlert>
    <UiSkeleton v-else-if="!req" :lines="5" />
    <div v-else class="flex min-w-0 flex-col gap-3">
      <UiAlert v-if="req.signing_note" kind="warning" data-test="request-signing-note">{{ SIGNING_NOTES[req.signing_note] }}</UiAlert>
      <UiAlert v-if="req.status === 'awaiting_signing'" kind="info" data-test="request-awaiting">
        The leave document is out for signature: first the employee, then the approver. The request is approved when both have signed.
      </UiAlert>
      <UiCard>
        <UiKeyValueTable :items="facts" data-test="request-facts" />
      </UiCard>
      <div class="flex flex-wrap gap-2" data-test="request-actions">
        <template v-if="req.can_review && (req.status === 'pending')">
          <UiButton icon="mdi-check" :loading="busy === 'approve'" data-test="request-approve" @click="ask('approve')">Approve</UiButton>
          <UiButton variant="soft" color="error" icon="mdi-close" data-test="request-reject" @click="ask('reject')">Reject</UiButton>
        </template>
        <UiButton v-if="req.can_review && req.status === 'awaiting_signing'" variant="soft" color="error" icon="mdi-close" data-test="request-reject" @click="ask('reject')">Reject</UiButton>
        <UiButton v-if="req.can_review && req.status === 'approved'" variant="soft" color="warning" icon="mdi-restart" data-test="request-revoke" @click="ask('revoke')">Revoke</UiButton>
        <UiButton v-if="req.can_edit" variant="outline" icon="mdi-pencil" data-test="request-edit" @click="edit = { open: true, reason: req.reason ?? '', notes: req.notes ?? '' }">Edit</UiButton>
        <UiButton v-if="req.can_cancel" variant="outline" icon="mdi-cancel" :loading="busy === 'cancel'" data-test="request-cancel" @click="act('cancel')">Cancel request</UiButton>
        <UiButton v-if="req.status === 'rejected' || req.status === 'cancelled'" variant="text" color="error" icon="mdi-delete" data-test="request-delete" @click="remove">Delete</UiButton>
        <UiButton v-if="req.signing && (req.status === 'approved' || req.status === 'revoked')" variant="outline" icon="mdi-download" :loading="busy === 'download'" data-test="request-download" @click="downloadSigned">Signed document</UiButton>
      </div>
    </div>
    <UiDialog v-model="dialog.open" :title="dialogTitle">
      <UiTextarea id="decision-notes" v-model="dialog.notes" :label="dialog.kind === 'revoke' ? 'Reason' : 'Notes for the employee'" :rows="3" data-test="decision-notes" />
      <template #actions>
        <UiButton variant="text" @click="dialog.open = false">Cancel</UiButton>
        <UiButton :color="dialog.kind === 'approve' ? 'primary' : 'error'" :loading="busy === dialog.kind" data-test="decision-confirm" @click="act(dialog.kind, dialog.notes)">
          {{ dialog.kind === 'approve' ? 'Approve' : dialog.kind === 'reject' ? 'Reject' : 'Revoke' }}
        </UiButton>
      </template>
    </UiDialog>
    <UiDialog v-model="edit.open" title="Edit request">
      <div class="flex flex-col gap-2">
        <UiTextarea id="edit-reason" v-model="edit.reason" label="Reason" :rows="2" />
        <UiTextarea id="edit-notes" v-model="edit.notes" label="Notes" :rows="2" />
      </div>
      <template #actions>
        <UiButton variant="text" @click="edit.open = false">Cancel</UiButton>
        <UiButton data-test="edit-save" @click="saveEdit">Save</UiButton>
      </template>
    </UiDialog>
  </UiPage>
</template>
