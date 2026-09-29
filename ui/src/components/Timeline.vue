<script setup lang="ts">
// The team timeline: one row per person grouped by department, one column per
// day, weekends and holidays shaded, absence bars colored by type (pending
// hatched, awaiting signing outlined). Dragging across days on an allowed row
// asks for a new request with those dates.
import { computed, ref } from 'vue'
import type { AbsenceType, Calendar, Department } from '@/api/types'
import { bar, cells, type Bar } from '@/utils/calendar'
import { STATUS_LABELS, days } from '@/utils/format'
import { vCssom } from '@/utils/cssom'

const props = defineProps<{
  data: Calendar
  from: string
  to: string
  today: string
  types: Map<string, AbsenceType>
  departments: Department[]
  canRequestFor: (userId: string) => boolean
}>()
const emit = defineEmits<{ request: [userId: string, start: string, end: string]; open: [requestId: string] }>()

const days0 = computed(() => cells(props.from, props.to, props.data.holidays, props.today))

interface Group {
  id: string
  name: string
  people: { user_id: string; name: string; bars: Bar[] }[]
}

const groups = computed<Group[]>(() => {
  const names = new Map(props.departments.map((d) => [d.id, d.name]))
  const byPerson = new Map<string, Bar[]>()
  for (const e of props.data.events) {
    const b = bar(e, props.from, props.to)
    if (b) byPerson.set(e.user_id, [...(byPerson.get(e.user_id) ?? []), b])
  }
  const out = new Map<string, Group>()
  for (const p of props.data.people) {
    const id = p.department_id ?? ''
    const g = out.get(id) ?? { id, name: id ? (names.get(id) ?? 'Department') : 'No department', people: [] }
    g.people.push({ user_id: p.user_id, name: p.name, bars: byPerson.get(p.user_id) ?? [] })
    out.set(id, g)
  }
  return [...out.values()].sort((a, b) => (a.id === '' ? 1 : b.id === '' ? -1 : a.name.localeCompare(b.name)))
})

function color(b: Bar): string {
  return props.types.get(b.event.absence_type_id)?.color || '#64748b'
}

function label(b: Bar): string {
  const t = props.types.get(b.event.absence_type_id)?.name ?? 'Absence'
  return `${t} · ${days(b.event.days)} · ${STATUS_LABELS[b.event.status as keyof typeof STATUS_LABELS] ?? b.event.status}`
}

// --- drag to request ---
const drag = ref<{ user: string; a: number; b: number } | null>(null)
function down(user: string, i: number): void {
  if (props.canRequestFor(user)) drag.value = { user, a: i, b: i }
}
function over(user: string, i: number): void {
  if (drag.value && drag.value.user === user) drag.value.b = i
}
function up(): void {
  const d = drag.value
  drag.value = null
  if (!d) return
  const [s, e] = d.a <= d.b ? [d.a, d.b] : [d.b, d.a]
  const first = days0.value[s]
  const last = days0.value[e]
  if (first && last) emit('request', d.user, first.date, last.date)
}
function selected(user: string, i: number): boolean {
  const d = drag.value
  if (!d || d.user !== user) return false
  return i >= Math.min(d.a, d.b) && i <= Math.max(d.a, d.b)
}
const colWidth = computed(() => (days0.value.length > 21 ? 'minmax(1.6rem, 1fr)' : 'minmax(2.6rem, 1fr)'))
</script>

<template>
  <div class="overflow-x-auto" data-test="timeline" @mouseup="up" @mouseleave="drag = null">
    <div v-cssom="{ 'grid-template-columns': `12rem repeat(${days0.length}, ${colWidth})` }" class="grid min-w-max text-xs" role="grid" aria-label="Team calendar">
      <div class="sticky left-0 z-10 bg-base-100 p-1 font-medium" role="columnheader">Person</div>
      <div
        v-for="c in days0"
        :key="c.date"
        role="columnheader"
        class="border-b border-base-300 p-1 text-center"
        :class="[c.weekend || c.holiday ? 'bg-base-200' : '', c.today ? 'font-bold text-primary' : '']"
        :title="c.holiday || undefined"
        :data-test="'timeline-day-' + c.date"
      >
        {{ Number(c.date.slice(8, 10)) }}
        <span v-if="c.holiday" class="block truncate text-[0.6rem]" :data-test="'timeline-holiday-' + c.date">{{ c.holiday }}</span>
      </div>

      <template v-for="g in groups" :key="g.id">
        <div class="sticky left-0 z-10 col-span-full bg-base-200/60 px-1 py-0.5 font-semibold" role="rowheader" :data-test="'timeline-group-' + (g.id || 'none')">{{ g.name }}</div>
        <template v-for="p in g.people" :key="p.user_id">
          <div class="sticky left-0 z-10 truncate border-b border-base-300 bg-base-100 p-1" role="rowheader" :data-test="'timeline-person-' + p.user_id">{{ p.name }}</div>
          <div v-cssom="{ 'grid-column': `span ${days0.length}` }" class="relative border-b border-base-300">
            <div v-cssom="{ 'grid-template-columns': `repeat(${days0.length}, 1fr)` }" class="grid h-8">
              <div
                v-for="(c, i) in days0"
                :key="c.date"
                class="h-full select-none border-l border-base-200"
                :class="[c.weekend || c.holiday ? 'bg-base-200' : '', selected(p.user_id, i) ? 'bg-primary/30' : '', canRequestFor(p.user_id) ? 'cursor-crosshair' : '']"
                @mousedown.prevent="down(p.user_id, i)"
                @mouseenter="over(p.user_id, i)"
              />
            </div>
            <button
              v-for="b in p.bars"
              :key="b.event.id"
              v-cssom="{ left: `calc(${(b.start / days0.length) * 100}% + 1px)`, width: `calc(${(b.length / days0.length) * 100}% - 2px)`, 'background-color': color(b) }"
              type="button"
              class="absolute top-1 h-6 truncate rounded px-1 text-left text-[0.65rem] text-white"
              :class="[b.event.status === 'pending' ? 'opacity-70 [background-image:repeating-linear-gradient(45deg,transparent,transparent_4px,rgba(255,255,255,.35)_4px,rgba(255,255,255,.35)_8px)]' : '', b.event.status === 'awaiting_signing' ? 'ring-2 ring-info' : '']"
              :title="label(b)"
              :aria-label="p.name + ': ' + label(b)"
              :data-test="'timeline-bar-' + b.event.id"
              @click="emit('open', b.event.id)"
            >
              {{ types.get(b.event.absence_type_id)?.name }}
            </button>
          </div>
        </template>
      </template>
    </div>
  </div>
</template>
