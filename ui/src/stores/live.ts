import { defineStore } from 'pinia'
import { ref } from 'vue'

// One shared EventSource relays the caller's hr events (GET /stream) through
// the gateway: request and review changes addressed to the caller and the
// tenant's calendar changes. Payloads carry ids and status only. It is
// reference-counted so several views share one connection; the module closes
// the stream at 290 s and EventSource reconnects on its own.
export const STREAM_URL = '/api/hr/v1/stream'
export const EVENTS = ['hr.request.changed', 'hr.review.changed', 'hr.calendar.changed'] as const
export type HrEventType = (typeof EVENTS)[number]
export interface HrEvent {
  type: HrEventType
  data: Record<string, unknown>
}
export type Listener = (e: HrEvent) => void

export const useLive = defineStore('hr-live', () => {
  const connected = ref(false)
  let source: EventSource | null = null
  let refs = 0
  const listeners = new Set<Listener>()

  function handle(type: HrEventType, raw: string): void {
    let data: unknown
    try {
      data = JSON.parse(raw)
    } catch {
      return
    }
    if (!data || typeof data !== 'object') return
    for (const l of listeners) l({ type, data: data as Record<string, unknown> })
  }

  function open(): void {
    if (source || typeof EventSource === 'undefined') return
    source = new EventSource(STREAM_URL, { withCredentials: true })
    source.onopen = () => (connected.value = true)
    source.onerror = () => (connected.value = false)
    for (const t of EVENTS) source.addEventListener(t, (e) => handle(t, (e as MessageEvent).data))
  }

  function close(): void {
    refs = 0
    source?.close()
    source = null
    connected.value = false
  }

  function connect(): () => void {
    refs += 1
    open()
    let released = false
    return () => {
      if (released) return
      released = true
      refs -= 1
      if (refs <= 0) close()
    }
  }

  function on(l: Listener): () => void {
    listeners.add(l)
    return () => listeners.delete(l)
  }

  return { connected, connect, close, on }
})

/** Calls fn at most once per `wait` ms for a burst of events. */
export function coalesce(fn: () => void, wait = 400): { trigger: () => void; cancel: () => void } {
  let timer: ReturnType<typeof setTimeout> | null = null
  return {
    trigger: () => {
      if (timer) return
      timer = setTimeout(() => {
        timer = null
        fn()
      }, wait)
    },
    cancel: () => {
      if (timer) clearTimeout(timer)
      timer = null
    },
  }
}
