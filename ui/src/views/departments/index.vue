<script setup lang="ts">
// Departments: a tree with a manager per department and its members. A
// request goes to the manager of the requester's department (the parent
// department's manager for managers themselves); without one, to HR
// administrators.
import { computed, onMounted, reactive, ref } from 'vue'
import { UiAlert, UiButton, UiCard, UiCombobox, UiDrawer, UiInput, UiPage, UiSelect, UiTree, useConfirm, useToast, type SelectOption } from '@go-tangra/ui'
import { api, describe, describeRefusal } from '@/api/client'
import type { Department, DepartmentWrite } from '@/api/types'
import { useOrg } from '@/stores/org'

const org = useOrg()
const toast = useToast()
const confirm = useConfirm()
const error = ref('')
const selected = ref('')

async function load(): Promise<void> {
  error.value = ''
  try {
    await org.load(true)
  } catch (e) {
    error.value = describe(e)
  }
}
onMounted(load)

interface Node {
  id: string
  label: string
  children?: Node[]
}
const tree = computed<Node[]>(() => {
  const kids = new Map<string, Department[]>()
  for (const d of org.departments) kids.set(d.parent_id ?? '', [...(kids.get(d.parent_id ?? '') ?? []), d])
  const build = (parent: string, depth: number): Node[] =>
    depth > 20 ? [] : (kids.get(parent) ?? []).sort((a, b) => a.name.localeCompare(b.name)).map((d) => ({ id: d.id, label: d.name, children: build(d.id, depth + 1) }))
  return build('', 0)
})
const current = computed(() => org.deptById.get(selected.value))
const members = computed(() => (current.value?.member_ids ?? []).map((id) => ({ id, name: org.name(id) })))
const peopleOptions = computed<SelectOption[]>(() => org.people.map((p) => ({ title: p.name, value: p.user_id })))
const parentOptions = computed<SelectOption[]>(() => org.departments.filter((d) => d.id !== d0.id).map((d) => ({ title: d.name, value: d.id })))

const d0 = reactive({ open: false, id: '', busy: false, error: '', form: { name: '', parent_id: '', manager_id: '' } as Required<DepartmentWrite> })
function open(d?: Department, parent = ''): void {
  d0.id = d?.id ?? ''
  d0.error = ''
  d0.form = { name: d?.name ?? '', parent_id: d?.parent_id ?? parent, manager_id: d?.manager_id ?? '' }
  d0.open = true
}
async function save(): Promise<void> {
  d0.busy = true
  d0.error = ''
  try {
    if (d0.id) await api('PUT', `departments/${d0.id}` as 'departments/{id}', d0.form)
    else await api('POST', 'departments', d0.form)
    d0.open = false
    await load()
  } catch (e) {
    d0.error = describeRefusal(e)
  } finally {
    d0.busy = false
  }
}
async function remove(d: Department): Promise<void> {
  if (!(await confirm.ask({ title: `Delete ${d.name}?`, text: 'Only empty departments can be deleted.', danger: true, confirmLabel: 'Delete' }))) return
  try {
    await api('DELETE', `departments/${d.id}` as 'departments/{id}')
    selected.value = ''
    await load()
  } catch (e) {
    toast.show({ kind: 'error', title: describeRefusal(e) })
  }
}
const adding = ref('')
async function setMembers(add: string[], remove: string[]): Promise<void> {
  if (!current.value) return
  try {
    await api('PUT', `departments/${current.value.id}/members` as 'departments/{id}/members', { add, remove })
    adding.value = ''
    await load()
  } catch (e) {
    toast.show({ kind: 'error', title: describeRefusal(e) })
  }
}
</script>

<template>
  <UiPage title="Departments" subtitle="Who approves whose leave">
    <template #actions>
      <UiButton v-if="org.canManage" icon="mdi-plus" data-test="department-new" @click="open()">New department</UiButton>
    </template>
    <UiAlert v-if="error" kind="error">{{ error }}</UiAlert>
    <div class="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-[18rem_minmax(0,1fr)]">
      <UiCard title="Tree" data-test="department-tree-card">
        <UiTree :items="tree" :selected="selected" data-test="department-tree" @select="selected = String($event)" />
        <p v-if="!tree.length" class="text-sm text-base-content/70">No departments yet.</p>
      </UiCard>
      <UiCard v-if="current" :title="current.name" :subtitle="current.manager_id ? 'Manager: ' + org.name(current.manager_id) : 'No manager: requests go up the tree or to HR'" data-test="department-detail">
        <template v-if="org.canManage" #header>
          <span class="flex gap-1">
            <UiButton size="xs" variant="text" icon="mdi-plus" icon-only label="New sub-department" data-test="department-add-child" @click="open(undefined, current.id)" />
            <UiButton size="xs" variant="text" icon="mdi-pencil-outline" icon-only label="Edit" data-test="department-edit" @click="open(current)" />
            <UiButton size="xs" variant="text" color="error" icon="mdi-delete" icon-only label="Delete" data-test="department-delete" @click="remove(current)" />
          </span>
        </template>
        <ul class="flex flex-col gap-1" data-test="department-members">
          <li v-for="m in members" :key="m.id" class="flex items-center justify-between rounded-box border border-base-300 px-2 py-1">
            <span>{{ m.name }}</span>
            <UiButton v-if="org.canManage" size="xs" variant="text" icon="mdi-close" icon-only :label="'Remove ' + m.name" @click="setMembers([], [m.id])" />
          </li>
          <li v-if="!members.length" class="text-sm text-base-content/70">No members.</li>
        </ul>
        <div v-if="org.canManage" class="mt-3 flex items-end gap-2">
          <UiSelect id="department-add-member" v-model="adding" label="Add member" :options="peopleOptions" size="sm" data-test="department-add-member" />
          <UiButton size="sm" :disabled="!adding" data-test="department-add-member-save" @click="setMembers([adding], [])">Add</UiButton>
        </div>
      </UiCard>
    </div>

    <UiDrawer v-model="d0.open" :title="d0.id ? 'Edit department' : 'New department'">
      <form class="flex flex-col gap-3" data-test="department-form" @submit.prevent="save">
        <UiInput id="department-name" v-model="d0.form.name" label="Name" required data-test="department-name" />
        <UiSelect id="department-parent" v-model="d0.form.parent_id" label="Part of" :options="parentOptions" placeholder="Top level" clearable />
        <UiCombobox id="department-manager" v-model="d0.form.manager_id" label="Manager" :options="peopleOptions" placeholder="No manager" data-test="department-manager" />
        <UiAlert v-if="d0.error" kind="error" data-test="department-error">{{ d0.error }}</UiAlert>
      </form>
      <template #actions>
        <UiButton variant="text" @click="d0.open = false">Cancel</UiButton>
        <UiButton :loading="d0.busy" data-test="department-save" @click="save">Save</UiButton>
      </template>
    </UiDrawer>
  </UiPage>
</template>
