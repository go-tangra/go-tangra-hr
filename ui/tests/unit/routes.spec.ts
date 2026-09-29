import { describe, expect, it } from 'vitest'
import manifest from '../../../pkg/hrmanifest/manifest.go?raw'
import { routes } from '@/remote/routes'
import { nav } from '@/remote/nav'

describe('remote', () => {
  it('mounts every route under /hr with the module meta', () => {
    expect(routes.length).toBe(9)
    for (const r of routes) {
      expect(r.path === '/hr' || r.path.startsWith('/hr/')).toBe(true)
      expect(r.meta?.module).toBe('hr')
    }
    expect(new Set(routes.map((r) => r.name)).size).toBe(routes.length)
  })

  it('every manifest nav path has a route', () => {
    const paths = [...manifest.matchAll(/Path:\s*"(\/hr[^"]*)"/g)].map((m) => m[1])
    expect(paths.length).toBeGreaterThan(0)
    const known = new Set(routes.map((r) => r.path))
    expect(paths.filter((p) => !known.has(p ?? ''))).toEqual([])
  })

  it('has no dynamic nav entries', () => {
    expect(nav()).toEqual([])
  })
})
