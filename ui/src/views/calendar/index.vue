<script setup lang="ts">
// Leave calendar: week, two-week or month timeline of the tenant's active
// people grouped by department, with filters by department and person.
// Pending, awaiting-signing and approved absences are shown (no reasons);
// holidays are shaded. Drag across days on your own row (any row for HR
// administrators) to request leave.
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { routerKey } from 'vue-router'
import { UiAlert, UiButton, UiCard, UiPage, UiSelect, UiSkeleton, UiTabs, useToast, type SelectOption } from '@go-tangra/ui'
import { api, describe } from '@/api/client'
import type { Calendar } from '@/api/types'
import RequestForm from '@/components/RequestForm.vue'
import Timeline from '@/components/Timeline.vue'
import { coalesce, useLive } from '@/stores/live'
import { useOrg } from '@/stores/org'
import { shift, span, title, type View } from '@/utils/calendar'
import { today as todayIso } from '@/utils/format'

const org = useOrg()
const live = useLive()
const toast = useToast()
const router = inject(routerKey, null)

const view = ref<View>('2weeks')
const anchor = ref(todayIso())
const department = ref('')
const person = ref('')
const data = ref<Calendar | null>(null)
const loading = ref(false)
const error = ref('')
const range = computed(() => span(view.value, anchor.value))
const heading = computed(() => title(view.value, range.value.from, range.value.to))
const tabs = [
  { key: 'week', label: 'Week' },
  { key: '2weeks', label: '2 weeks' },
  { key: 'month', label: 'Month' },
]
const deptOptions = computed<SelectOption[]>(() => org.departments.map((d) => ({ title: d.name, value: d.id })))
const personOptions = computed<SelectOption[]>(() => org.people.map((p) => ({ title: p.name, value: p.user_id })))

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    data.value = await api<Calendar>('GET', 'calendar', undefined, { query: { from: range.value.from, to: range.value.to,
      department: department.value || undefined, user: person.value || undefined } })
  } catch (e) {
    error.value = describe(e)
  } finally {
    loading.value = false
  }
}
watch([range, department, person], () => void load())

const refresh = coalesce(() => void load(), 500)
let release: (() => void) | null = null
let unsubscribe: (() => void) | null = null
onMounted(async () => {
  try {
    await org.load()
  } catch (e) {
    error.value = describe(e)
  }
  void load()
  release = live.connect()
  unsubscribe = live.on(() => refresh.trigger())
})
onBeforeUnmount(() => {
  refresh.cancel()
  unsubscribe?.()
  release?.()
})

function canRequestFor(userId: string): boolean {
  return org.canManage || (org.canRequest && userId === org.me?.user_id)
}

const form = ref({ open: false, start: '', end: '', user: '' })
function request(userId: string, start: string, end: string): void {
  form.value = { open: true, start, end, user: userId === org.me?.user_id ? '' : userId }
}
function created(): void {
  toast.show({ kind: 'success', title: 'Request submitted' })
  void load()
}
function open(id: string): void {
  void router?.push({ name: 'hr-request', params: { id } })
}
</script>

<template>
  <UiPage title="Leave calendar" subtitle="Who is away, and when">
    <template #actions>
      <UiButton v-if="org.canRequest || org.canManage" icon="mdi-plus" data-test="calendar-new" @click="request(org.me?.user_id ?? '', todayIso(), todayIso())">New request</UiButton>
    </template>
    <div class="flex min-w-0 flex-col gap-3">
      <UiCard>
        <div class="flex flex-wrap items-end gap-2" data-test="calendar-controls">
          <UiButton variant="outline" size="sm" icon="mdi-chevron-left" icon-only label="Previous" data-test="calendar-prev" @click="anchor = shift(view, anchor, -1)" />
          <UiButton variant="outline" size="sm" data-test="calendar-today" @click="anchor = todayIso()">Today</UiButton>
          <UiButton variant="outline" size="sm" icon="mdi-chevron-right" icon-only label="Next" data-test="calendar-next" @click="anchor = shift(view, anchor, 1)" />
          <h2 class="mx-2 text-lg font-semibold" data-test="calendar-title">{{ heading }}</h2>
          <UiTabs v-model="view" :tabs="tabs" data-test="calendar-view" />
          <div class="ms-auto grid grid-cols-2 gap-2">
            <UiSelect id="calendar-department" v-model="department" label="Department" :options="deptOptions" placeholder="All" clearable size="sm" data-test="calendar-department" />
            <UiSelect id="calendar-person" v-model="person" label="Person" :options="personOptions" placeholder="Everyone" clearable size="sm" data-test="calendar-person" />
          </div>
        </div>
      </UiCard>
      <UiAlert v-if="error" kind="error" data-test="calendar-error">{{ error }}</UiAlert>
      <UiCard :padded="false">
        <UiSkeleton v-if="loading && !data" :lines="6" />
        <Timeline
          v-else-if="data"
          :data="data"
          :from="range.from"
          :to="range.to"
          :today="todayIso()"
          :types="org.typeById"
          :departments="org.departments"
          :can-request-for="canRequestFor"
          @request="request"
          @open="open"
        />
        <p class="p-3 text-xs text-base-content/70">Drag across days on your row to request leave. Hatched: pending · outlined: awaiting signing.</p>
      </UiCard>
    </div>
    <RequestForm v-model="form.open" :start="form.start" :end="form.end" :user-id="form.user" @created="created" />
  </UiPage>
</template>
