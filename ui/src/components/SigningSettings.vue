<script setup lang="ts">
// Signing settings of an absence type: the signing template, which of its
// parties is the employee and which the approver, and which template fields
// receive which leave value (prefilled when the document is sent).
import { computed, onMounted, ref } from 'vue'
import { UiAlert, UiSelect, type SelectOption } from '@go-tangra/ui'
import { api, describe } from '@/api/client'
import type { SigningSettings, SigningTemplate } from '@/api/types'
import { VALUE_KEYS } from '@/api/types'
import { VALUE_LABELS } from '@/utils/format'

const model = defineModel<SigningSettings>({ required: true })
const templates = ref<SigningTemplate[]>([])
const error = ref('')

onMounted(async () => {
  try {
    templates.value = (await api<{ items: SigningTemplate[] }>('GET', 'signing/templates')).items ?? []
  } catch (e) {
    error.value = describe(e)
  }
})

const template = computed(() => templates.value.find((t) => t.id === model.value.template_id))
const templateOptions = computed<SelectOption[]>(() => templates.value.map((t) => ({ title: t.name, value: t.id })))
const partyOptions = computed<SelectOption[]>(() => (template.value?.parties ?? []).map((p) => ({ title: p.name, value: p.id })))
const valueOptions: SelectOption[] = VALUE_KEYS.map((k) => ({ title: VALUE_LABELS[k], value: k }))
const textFields = computed(() => (template.value?.fields ?? []).filter((f) => f.text_valued))

function setTemplate(v: unknown): void {
  const t = templates.value.find((x) => x.id === v)
  model.value = { template_id: String(v ?? ''), template_name: t?.name ?? '', employee_party: t?.parties[0]?.id ?? '',
    approver_party: t?.parties[1]?.id ?? '', fields: {} }
}
function setField(id: string, v: unknown): void {
  const fields = { ...model.value.fields }
  if (v) fields[id] = v as (typeof VALUE_KEYS)[number]
  else delete fields[id]
  model.value = { ...model.value, fields }
}
</script>

<template>
  <div class="flex flex-col gap-2 rounded-box border border-base-300 p-3" data-test="signing-settings">
    <UiAlert v-if="error" kind="warning">{{ error }}</UiAlert>
    <UiSelect id="signing-template" :model-value="model.template_id" label="Signing template" :options="templateOptions" required data-test="signing-template" @update:model-value="setTemplate" />
    <div v-if="template" class="grid grid-cols-2 gap-2">
      <UiSelect
        id="signing-employee" :model-value="model.employee_party" label="Employee signs as" :options="partyOptions" data-test="signing-employee"
        @update:model-value="model = { ...model, employee_party: String($event ?? '') }"
      />
      <UiSelect
        id="signing-approver" :model-value="model.approver_party" label="Approver signs as" :options="partyOptions" data-test="signing-approver"
        @update:model-value="model = { ...model, approver_party: String($event ?? '') }"
      />
    </div>
    <div v-if="textFields.length" class="flex flex-col gap-1">
      <p class="text-sm font-medium">Prefill template fields</p>
      <UiSelect
        v-for="f in textFields" :id="'signing-field-' + f.id" :key="f.id" :model-value="model.fields[f.id] ?? ''" :label="f.name"
        :options="valueOptions" placeholder="Leave empty" clearable size="sm" :data-test="'signing-field-' + f.id" @update:model-value="setField(f.id, $event)"
      />
    </div>
  </div>
</template>
