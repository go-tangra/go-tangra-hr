<script setup lang="ts">
// My leave: my balance for the year and my requests; HR readers and managers
// also get the "All requests" tab (everyone / their departments).
import { computed, onMounted, ref, watch } from 'vue'
import { UiAlert, UiButton, UiCard, UiPage, UiSelect, UiTabs, useToast, type SelectOption } from '@go-tangra/ui'
import { api, describe } from '@/api/client'
import type { Balance } from '@/api/types'
import RequestForm from '@/components/RequestForm.vue'
import { useOrg } from '@/stores/org'
import { days } from '@/utils/format'
import RequestList from './list.vue'

const org = useOrg()
const toast = useToast()
const tab = ref('mine')
const year = ref(String(new Date().getFullYear()))
const balance = ref<Balance | null>(null)
const error = ref('')
const formOpen = ref(false)
const reload = ref(0)
const tabs = computed(() => [{ key: 'mine', label: 'My requests' }, ...(org.canRead || org.isManager ? [{ key: 'all', label: 'All requests' }] : [])])
const years = computed<SelectOption[]>(() => [-1, 0, 1].map((d) => ({ title: String(new Date().getFullYear() + d), value: String(new Date().getFullYear() + d) })))

async function loadBalance(): Promise<void> {
  if (!org.me) return
  try {
    balance.value = await api<Balance>('GET', `balance/${encodeURIComponent(org.me.user_id)}` as 'balance/{user_id}', undefined, { query: { year: Number(year.value) } })
  } catch (e) {
    error.value = describe(e)
  }
}
watch(year, () => void loadBalance())
onMounted(async () => {
  try {
    await org.load()
    await loadBalance()
  } catch (e) {
    error.value = describe(e)
  }
})
function created(): void {
  toast.show({ kind: 'success', title: 'Request submitted' })
  reload.value++
  void loadBalance()
}
</script>

<template>
  <UiPage title="My leave" subtitle="Your balance and your leave requests">
    <template #actions>
      <UiButton v-if="org.canRequest" icon="mdi-plus" data-test="requests-new" @click="formOpen = true">New request</UiButton>
    </template>
    <div class="flex min-w-0 flex-col gap-3">
      <UiAlert v-if="error" kind="error">{{ error }}</UiAlert>
      <UiCard v-if="balance" title="Balance" data-test="balance">
        <template #header>
          <UiSelect id="balance-year" v-model="year" label="Year" :options="years" size="sm" sr-only-label />
        </template>
        <p v-if="!balance.lines.length" class="text-sm text-base-content/70">No allowance for {{ balance.year }}.</p>
        <div class="grid grid-cols-1 gap-2 md:grid-cols-3">
          <div v-for="l in balance.lines" :key="l.id" class="rounded-box border border-base-300 p-3" :data-test="'balance-' + l.id">
            <p class="font-medium">{{ l.name }}</p>
            <p class="text-2xl font-semibold" :class="l.remaining < 0 ? 'text-error' : ''">{{ days(l.remaining) }}</p>
            <p class="text-xs text-base-content/70">
              {{ days(l.total) }} + {{ days(l.carried) }} carried · {{ days(l.used) }} used<span v-if="l.pending"> · {{ days(l.pending) }} pending</span>
            </p>
          </div>
        </div>
      </UiCard>
      <UiTabs v-if="tabs.length > 1" v-model="tab" :tabs="tabs" />
      <RequestList v-if="tab === 'mine'" view="mine" :reload="reload" />
      <RequestList v-else view="all" show-person :reload="reload" />
    </div>
    <RequestForm v-model="formOpen" @created="created" />
  </UiPage>
</template>
