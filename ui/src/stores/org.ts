import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/api/client'
import { fetchAll } from '@/composables/useServerList'
import type { AbsenceType, Department, Me, Person } from '@/api/types'

// Who the caller is and the tenant's shared reference data: people (names),
// departments and absence types. Loaded once per view and refreshed on demand.
export const useOrg = defineStore('hr-org', () => {
  const me = ref<Me | null>(null)
  const people = ref<Person[]>([])
  const departments = ref<Department[]>([])
  const types = ref<AbsenceType[]>([])
  const loaded = ref(false)

  const can = (p: string) => computed(() => me.value?.permissions.includes(p) ?? false)
  const canManage = can('hr:manage')
  const canRead = can('hr:read')
  const canRequest = can('hr:request')
  const isManager = computed(() => (me.value?.manages.length ?? 0) > 0)
  const names = computed(() => new Map(people.value.map((p) => [p.user_id, p.name])))
  const typeById = computed(() => new Map(types.value.map((t) => [t.id, t])))
  const deptById = computed(() => new Map(departments.value.map((d) => [d.id, d])))

  async function load(force = false): Promise<void> {
    if (loaded.value && !force) return
    const [m, p, d, t] = await Promise.all([
      api<Me>('GET', 'me'),
      api<{ items: Person[] }>('GET', 'people'),
      api<{ items: Department[] }>('GET', 'departments'),
      // The list is paged (list contract); reference data wants all of it.
      fetchAll<AbsenceType>('absence-types', { all: true }).catch(() => fetchAll<AbsenceType>('absence-types')),
    ])
    me.value = m
    people.value = p.items ?? []
    departments.value = d.items ?? []
    types.value = t
    loaded.value = true
  }

  function name(id: string | undefined | null): string {
    if (!id) return ''
    return names.value.get(id) ?? id
  }

  return { me, people, departments, types, loaded, canManage, canRead, canRequest, isManager, typeById, deptById, load, name }
})
