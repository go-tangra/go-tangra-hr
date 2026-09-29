import { describe, expect, it } from 'vitest'
import { ICONS } from '@go-tangra/ui'
// Read as text: the manifest is Go, outside the app program.
import manifest from '../../../pkg/hrmanifest/manifest.go?raw'

// The shell only has CSS for the icons in the kit's safelist: a name used here
// but missing there renders as an empty box (menu entries included).
const views = import.meta.glob('/src/**/*.{vue,ts}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

describe('icons', () => {
  it('every icon the manifest nav and the views name is in the kit safelist', () => {
    const used = new Set([manifest, ...Object.values(views)].flatMap((s) => s.match(/mdi-[a-z0-9-]+/g) ?? []))
    expect(used.size).toBeGreaterThan(0)
    const known = new Set<string>(ICONS)
    expect([...used].filter((n) => !known.has(n)).sort()).toEqual([])
  })
})
