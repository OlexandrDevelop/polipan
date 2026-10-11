// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState } from './mockApi'
import { openScreen, SCREENS, setUp } from './quality'

// REQ-UI-016: no horizontal page scroll at 360 px on every screen.
for (const screen of SCREENS) {
  test(`REQ-UI-016 no horizontal page scroll at 360 px on ${screen.name}`, async ({ page }) => {
    const state = defaultState()
    state.deployments.d1 = {
      id: 'd1',
      service_id: 's1',
      state: 'live',
      trigger: 'manual',
      started_at: '2026-10-10T10:00:00Z',
    }
    await setUp(page, state)
    await openScreen(page, screen, { width: 360, theme: 'light' })
    const wide = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    )
    expect(wide).toBeLessThanOrEqual(0)
  })
}

// REQ-UI-018: a data table becomes a list of cards on a phone.
for (const name of ['tokens', 'members', 'nodes', 'audit', 'git', 'backup-targets']) {
  test(`REQ-UI-018 the table of ${name} becomes cards at 360 px`, async ({ page }) => {
    await setUp(page, defaultState())
    const screen = SCREENS.find((s) => s.name === name)!
    await openScreen(page, screen, { width: 360, theme: 'light' })
    await expect(page.locator('table:visible')).toHaveCount(0)
    await expect(page.locator('main article').first()).toBeVisible()
    await page.setViewportSize({ width: 1280, height: 900 })
    await expect(page.locator('main table:visible').first()).toBeVisible()
  })
}

// REQ-UI-017: 44 px targets on touch input.
test.describe('touch profile', () => {
  test.use({ hasTouch: true, isMobile: true, viewport: { width: 360, height: 740 } })
  for (const name of ['dashboard', 'projects', 'service', 'service-releases', 'nodes', 'tokens']) {
    test(`REQ-UI-017 touch targets are at least 44 px on ${name}`, async ({ page }) => {
      await setUp(page, defaultState())
      const screen = SCREENS.find((s) => s.name === name)!
      await page.goto(screen.path)
      await page.locator('main h1').first().waitFor()
      await page.waitForLoadState('networkidle')
      const small = await page.evaluate(() => {
        const found: string[] = []
        const nodes = document.querySelectorAll<HTMLElement>(
          'button, a[href], input:not([type=hidden]), select, textarea, [role=tab]',
        )
        for (const el of nodes) {
          // A checkbox or radio button is clicked through its label: the label is the target.
          const target = el.matches('input[type=checkbox], input[type=radio]')
            ? (el.closest('label') ?? el)
            : el
          const box = target.getBoundingClientRect()
          if (box.width === 0 || box.height === 0 || el.matches('.sr-only')) continue
          // A link inside a sentence is exempt (WCAG 2.2 target size, inline).
          if (el.tagName === 'A' && el.closest('p, dd, li > span')) continue
          if (box.height < 43.5 || box.width < 43.5) {
            found.push(
              `${el.tagName} "${(el.textContent ?? '').trim().slice(0, 30)}" ${Math.round(box.width)}x${Math.round(box.height)}`,
            )
          }
        }
        return found
      })
      expect(small).toEqual([])
    })
  }
})

// REQ-UI-027: reduced motion switches every animation and transition off.
test.describe('reduced motion', () => {
  test.use({ reducedMotion: 'reduce' })
  for (const name of ['dashboard', 'service', 'nodes', 'service-metrics']) {
    test(`REQ-UI-027 no animation runs on ${name} with reduced motion`, async ({ page }) => {
      await setUp(page, defaultState())
      const screen = SCREENS.find((s) => s.name === name)!
      await page.goto(screen.path)
      await page.locator('main h1').first().waitFor()
      const running = await page.evaluate(() =>
        document
          .getAnimations()
          .filter(
            (a) =>
              a.playState === 'running' && a.effect?.getComputedTiming().iterations === Infinity,
          )
          .map(
            (a) =>
              ((a.effect as KeyframeEffect | null)?.target as HTMLElement | null)?.className ?? '',
          ),
      )
      expect(running).toEqual([])
      const transition = await page.evaluate(
        () => getComputedStyle(document.querySelector('button')!).transitionDuration,
      )
      expect(transition).toBe('0s')
      const scroll = await page.evaluate(
        () => getComputedStyle(document.documentElement).scrollBehavior,
      )
      expect(scroll).toBe('auto')
    })
  }

  test('REQ-UI-027 a loading skeleton does not pulse with reduced motion', async ({ page }) => {
    await setUp(page, defaultState())
    // The answer comes late, so the skeleton stays on screen.
    await page.route('**/api/v1/nodes', async (route) => {
      await new Promise((r) => setTimeout(r, 1500))
      await route.fallback()
    })
    await page.goto('/o/acme/nodes')
    const skeleton = page.locator('main .animate-pulse').first()
    await expect(skeleton).toBeVisible()
    const name = await skeleton.evaluate((el) => getComputedStyle(el).animationName)
    expect(name).toBe('none')
  })
})

// REQ-UI-015: the theme.
test('REQ-UI-015 the stored theme is set before the app starts (no flash)', async ({ page }) => {
  await setUp(page, defaultState())
  // The app script never loads, so only /theme-init.js can have set the theme.
  await page.route('**/src/main.tsx*', (route) => route.abort())
  await page.emulateMedia({ colorScheme: 'light' })
  await page.addInitScript(() => localStorage.setItem('polipan.theme', 'dark'))
  await page.goto('/login')
  expect(await page.evaluate(() => document.documentElement.dataset.theme)).toBe('dark')
})

test('REQ-UI-015 a stored value that is not system, light or dark is ignored', async ({ page }) => {
  await setUp(page, defaultState())
  await page.route('**/src/main.tsx*', (route) => route.abort())
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.addInitScript(() =>
    localStorage.setItem('polipan.theme', '"><img src=x onerror=alert(1)>'),
  )
  await page.goto('/login')
  // "system" with a dark scheme.
  expect(await page.evaluate(() => document.documentElement.dataset.theme)).toBe('dark')
  await page.emulateMedia({ colorScheme: 'light' })
  await page.goto('/login')
  expect(await page.evaluate(() => document.documentElement.dataset.theme)).toBe('light')
})

test('REQ-UI-015 system theme follows the system live and the choice persists', async ({
  page,
}) => {
  await setUp(page, defaultState())
  await page.emulateMedia({ colorScheme: 'light' })
  await page.goto('/o/acme/settings/profile')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  const theme = () => page.evaluate(() => document.documentElement.dataset.theme)
  expect(await theme()).toBe('light')
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect.poll(theme).toBe('dark')

  await page.getByRole('radio', { name: 'Light' }).check()
  await expect.poll(theme).toBe('light')
  // An explicit choice does not follow the system.
  await page.emulateMedia({ colorScheme: 'dark' })
  expect(await theme()).toBe('light')
  await page.reload()
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  expect(await theme()).toBe('light')
  expect(await page.evaluate(() => localStorage.getItem('polipan.theme'))).toBe('light')
})

// REQ-UI-008: route change moves the focus to the heading and sets the title.
test('REQ-UI-008 a route change moves the focus to the h1 and changes the title', async ({
  page,
}) => {
  await setUp(page, defaultState())
  await page.goto('/o/acme')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await page.getByRole('link', { name: 'Nodes', exact: true }).first().click()
  await expect(page.getByRole('heading', { level: 1, name: 'Nodes' })).toBeFocused()
  await expect(page).toHaveTitle(/Nodes/)
})

test('REQ-UI-008 the skip link is the first focusable element', async ({ page }) => {
  await setUp(page, defaultState())
  await page.goto('/o/acme/nodes')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  // The first tabbable element in the page is the skip link, also when the h1 has the focus.
  const first = await page.evaluate(
    () =>
      document.querySelector<HTMLElement>(
        'a[href], button, input, select, textarea, [tabindex="0"]',
      )?.textContent,
  )
  expect(first).toBe('Skip to content')
  await page.getByRole('link', { name: 'Skip to content' }).focus()
  await page.keyboard.press('Enter')
  await expect(page.locator('#main')).toBeFocused()
})
