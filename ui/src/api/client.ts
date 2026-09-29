// The hr API through the gateway: the kit client bound to this module's base.
// Mutating calls carry the platform CSRF header (X-CSRF-Token, double-submit
// cookie) — the kit client adds it to every non-GET request.
import { createApi, ApiError, csrfToken, describe, type Method } from '@go-tangra/ui/api'
import { registerReasons } from '@go-tangra/ui/forms'
import type { paths } from './schema.d'

export { ApiError, describe }

// Path names are checked against the OpenAPI contract at compile time.
export type ApiPath = keyof paths
export const BASE = '/api/hr/v1'

// hr-specific refusal reasons (closed vocabulary, api/openapi/hr.yaml).
registerReasons({
  overlap: 'This overlaps another leave request of the same person.',
  insufficient_allowance: 'Not enough days left in the allowance.',
  no_allowance: 'No allowance is set up for this absence type and year.',
  zero_days: 'The dates contain no working days.',
  invalid_transition: 'The request cannot be changed in its current state.',
  self_review: 'You cannot decide your own request.',
  not_routed: 'This request is not waiting for your decision.',
  in_use: 'It is still in use.',
  duplicate: 'That already exists.',
  not_empty: 'The department still has members or sub-departments.',
  cycle: 'A department cannot be moved under itself.',
  signing_unavailable: 'The signing module is not reachable. Try again later.',
  signing_template_invalid: 'The signing template or its field mapping is no longer valid.',
  signer_inactive: 'The employee or approver is no longer an active member.',
  not_signed: 'There is no signed document for this request yet.',
  invalid_backup: 'The backup file is not valid.',
  payload_too_large: 'The file is too large.',
})

export const api = createApi({ base: BASE })

/** The field a refusal names, if any. */
export function refusalField(err: unknown): string | undefined {
  if (!(err instanceof ApiError)) return undefined
  const f = err.detail?.field
  return typeof f === 'string' && f ? f : undefined
}

/** describe() plus the field the server named. */
export function describeRefusal(err: unknown): string {
  const f = refusalField(err)
  return f ? `${describe(err)} (${f})` : describe(err)
}

function url(path: string, query?: Record<string, string | undefined>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(query ?? {})) if (v !== undefined && v !== '') q.set(k, v)
  const qs = q.toString()
  return `${BASE}/${path}${qs ? '?' + qs : ''}`
}

async function send(method: Method, target: string, headers: Record<string, string>, body?: BodyInit): Promise<Response> {
  try {
    return await fetch(target, {
      method,
      headers: { ...(method !== 'GET' ? { 'X-CSRF-Token': csrfToken() } : {}), ...headers },
      credentials: 'same-origin',
      ...(body !== undefined ? { body } : {}),
    })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    throw new ApiError(0, 'network')
  }
}

async function refusal(res: Response): Promise<ApiError> {
  const data = (await res.json().catch(() => ({}))) as Record<string, unknown>
  const reason = typeof data.reason === 'string' ? data.reason : 'error'
  const rest = Object.fromEntries(Object.entries(data).filter(([k]) => k !== 'reason' && k !== 'detail'))
  const detail = typeof data.detail === 'object' && data.detail !== null ? { ...rest, ...(data.detail as Record<string, unknown>) } : rest
  return new ApiError(res.status, reason, Object.keys(detail).length ? detail : undefined)
}

async function json<T>(res: Response): Promise<T> {
  if (!res.ok) throw await refusal(res)
  if (res.status === 204) return undefined as T
  return (await res.json().catch(() => ({}))) as T
}

/** POSTs raw bytes (a backup archive, a holiday file) with their content type; the answer is JSON. */
export async function postBytes<T = unknown>(path: string, body: Blob | string, contentType: string, query?: Record<string, string | undefined>): Promise<T> {
  return json<T>(await send('POST', url(path, query), { Accept: 'application/json', 'Content-Type': contentType }, body))
}

function filename(res: Response): string {
  const cd = res.headers.get('Content-Disposition') ?? ''
  const star = /filename\*=UTF-8''([^;]+)/i.exec(cd)?.[1]?.trim()
  let name = /filename="?([^";]+)"?/i.exec(cd)?.[1]?.trim() ?? ''
  if (star) {
    try {
      name = decodeURIComponent(star)
    } catch {
      // Keep the plain name.
    }
  }
  return name
}

/** Takes a file back (POST backup export, GET signed document). */
export async function download(method: Method, path: string): Promise<{ blob: Blob; filename: string }> {
  const res = await send(method, url(path), { Accept: 'application/pdf, application/gzip, application/json' })
  if (!res.ok) throw await refusal(res)
  return { blob: await res.blob(), filename: filename(res) }
}

/** Saves a blob through a temporary link. */
export function saveBlob(blob: Blob, name: string): void {
  const href = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = href
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(href), 1000)
}
