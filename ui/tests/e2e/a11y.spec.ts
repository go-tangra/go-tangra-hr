import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { base, signIn } from './helpers'

// Every HR view inside the shell, both themes, zero serious or critical
// axe findings. (Positions set through the CSSOM show as style attributes in
// the DOM; the edge CSP, not this test, forbids style attributes in markup.)
// Needs a full platform with the HR module; skips without operator
// credentials.
const password = process.env.E2E_OPERATOR_PASSWORD ?? ''
const email = process.env.E2E_OPERATOR_EMAIL ?? 'ops@example.org'
const routes = ['/hr', '/hr/requests', '/hr/review', '/hr/allowances', '/hr/absence-types', '/hr/departments', '/hr/holidays', '/hr/stats']
const tags = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']

test.describe('hr accessibility', () => {
  test.skip(!password, 'E2E_OPERATOR_PASSWORD not set')

  for (const theme of ['freya-light', 'freya-dark']) {
    test(`views are axe clean in ${theme}`, async ({ page }) => {
      await page.addInitScript((t) => localStorage.setItem('freya.theme', t), theme)
      await page.goto(base + '/')
      await signIn(page, email, password)
      for (const route of routes) {
        await page.goto(base + route)
        await expect(page.locator('main h1, main h2').first()).toBeVisible({ timeout: 15_000 })
        const results = await new AxeBuilder({ page }).withTags(tags).analyze()
        const blocking = results.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
        expect(blocking, route + ': ' + JSON.stringify(blocking.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) })))).toEqual([])
      }
    })
  }
})
