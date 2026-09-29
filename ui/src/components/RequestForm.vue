<script setup lang="ts">
// New leave request: absence type, dates (half first/last day), reason and
// notes. The server counts the working days (weekends and the tenant's
// holidays skipped) and shows the balance after, the approvers and overlaps
// as the form changes. HR administrators may file for anyone.
import { computed, reactive, ref, watch } from 'vue'
import { UiAlert, UiButton, UiCheckbox, UiDrawer, UiInput, UiSelect, UiTextarea, type SelectOption } from '@go-tangra/ui'
import { api, describeRefusal } from '@/api/client'
import type { LeaveRequest, Preview, RequestWrite } from '@/api/types'
import { useOrg } from '@/stores/org'
import { days } from '@/utils/format'

const props = defineProps<{ modelValue: boolean; start?: string; end?: string; userId?: string }>()
const emit = defineEmits<{ 'update:modelValue': [boolean]; created: [LeaveRequest] }>()

const org = useOrg()
const f = reactive({ user_id: '', absence_type_id: '', start_date: '', end_date: '', half_start: false, half_end: false, reason: '', notes: '' })
const preview = ref<Preview | null>(null)
const previewError = ref('')
const error = ref('')
const busy = ref(false)

const typeOptions = computed<SelectOption[]>(() => org.types.filter((t) => t.active).map((t) => ({ title: t.name, value: t.id })))
const peopleOptions = computed<SelectOption[]>(() => org.people.map((p) => ({ title: p.name, value: p.user_id })))
const oneDay = computed(() => f.start_date !== '' && f.start_date === f.end_date)

watch(
  () => props.modelValue,
  (open) => {
    if (!open) return
    Object.assign(f, { user_id: props.userId ?? '', absence_type_id: f.absence_type_id || (typeOptions.value[0]?.value as string) || '',
      start_date: props.start ?? '', end_date: props.end ?? props.start ?? '', half_start: false, half_end: false, reason: '', notes: '' })
    error.value = ''
    preview.value = null
  },
  { immediate: true },
)

function body(): RequestWrite {
  const b: RequestWrite = { absence_type_id: f.absence_type_id, start_date: f.start_date, end_date: f.end_date,
    half_start: f.half_start, half_end: oneDay.value ? false : f.half_end, reason: f.reason.trim(), notes: f.notes }
  if (f.user_id && f.user_id !== org.me?.user_id) b.user_id = f.user_id
  return b
}

let seq = 0
watch(
  () => [f.user_id, f.absence_type_id, f.start_date, f.end_date, f.half_start, f.half_end],
  async () => {
    previewError.value = ''
    if (!f.absence_type_id || !f.start_date || !f.end_date) {
      preview.value = null
      return
    }
    const n = ++seq
    try {
      const p = await api<Preview>('POST', 'requests/preview', body())
      if (n === seq) preview.value = p
    } catch (e) {
      if (n === seq) {
        preview.value = null
        previewError.value = describeRefusal(e)
      }
    }
  },
)

async function submit(): Promise<void> {
  busy.value = true
  error.value = ''
  try {
    const r = await api<LeaveRequest>('POST', 'requests', body())
    emit('created', r)
    emit('update:modelValue', false)
  } catch (e) {
    error.value = describeRefusal(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UiDrawer :model-value="modelValue" title="New leave request" size="md" @close="emit('update:modelValue', false)">
    <form class="flex flex-col gap-3" data-test="request-form" @submit.prevent="submit">
      <UiSelect v-if="org.canManage" id="request-user" v-model="f.user_id" label="For" :options="peopleOptions" placeholder="Myself" clearable data-test="request-user" />
      <UiSelect id="request-type" v-model="f.absence_type_id" label="Absence type" :options="typeOptions" required data-test="request-type" />
      <div class="grid grid-cols-2 gap-2">
        <UiInput id="request-start" v-model="f.start_date" label="First day" type="date" required data-test="request-start" />
        <UiInput id="request-end" v-model="f.end_date" label="Last day" type="date" required data-test="request-end" />
      </div>
      <div class="flex flex-wrap gap-4">
        <UiCheckbox id="request-half-start" v-model="f.half_start" :label="oneDay ? 'Half day' : 'Half first day'" data-test="request-half-start" />
        <UiCheckbox v-if="!oneDay" id="request-half-end" v-model="f.half_end" label="Half last day" data-test="request-half-end" />
      </div>
      <UiTextarea id="request-reason" v-model="f.reason" label="Reason" :rows="2" data-test="request-reason" />
      <UiTextarea id="request-notes" v-model="f.notes" label="Notes" :rows="2" hint="Visible to your approvers and HR" data-test="request-notes" />

      <div v-if="preview" class="rounded-box bg-base-200 p-3 text-sm" data-test="request-preview">
        <p class="font-medium" data-test="request-preview-days">{{ days(preview.days) }}</p>
        <p v-for="(left, year) in preview.remaining" :key="year" :class="left < 0 ? 'text-error' : ''">
          {{ year }}: {{ days(left) }} left after this request
        </p>
        <p v-if="preview.status === 'approved'">No approval needed: approved when you submit.</p>
        <p v-else-if="preview.approver_names?.length">Approver: {{ preview.approver_names.join(', ') }}</p>
        <p v-if="preview.overlaps" class="text-error" data-test="request-preview-overlap">This overlaps another request.</p>
      </div>
      <UiAlert v-if="previewError" kind="warning" data-test="request-preview-error">{{ previewError }}</UiAlert>
      <UiAlert v-if="error" kind="error" data-test="request-error">{{ error }}</UiAlert>
    </form>
    <template #actions>
      <UiButton variant="text" @click="emit('update:modelValue', false)">Cancel</UiButton>
      <UiButton :loading="busy" :disabled="!f.absence_type_id || !f.start_date || !f.end_date" data-test="request-submit" @click="submit">Submit request</UiButton>
    </template>
  </UiDrawer>
</template>
