// Computed geometry (field boxes in percent of the page, a page's aspect ratio)
// is applied through the CSSOM: the edge CSP has no 'unsafe-inline' for
// style-src, which blocks style attributes in markup but not properties set
// from script (the kit positions its dropdown menus the same way).
import type { Directive } from 'vue'

type Props = Record<string, string>

function apply(el: HTMLElement, next: Props, prev?: Props): void {
  for (const k of Object.keys(prev ?? {})) if (!(k in next)) el.style.removeProperty(k)
  for (const [k, v] of Object.entries(next)) el.style.setProperty(k, v)
}

/** v-cssom="{ left: '10%', 'aspect-ratio': '595 / 842' }" */
export const vCssom: Directive<HTMLElement, Props> = {
  mounted: (el, b) => apply(el, b.value),
  updated: (el, b) => apply(el, b.value, b.oldValue ?? undefined),
}
