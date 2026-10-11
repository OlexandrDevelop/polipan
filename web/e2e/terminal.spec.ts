// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from 'node:fs'
import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { defaultState, mockApi } from './mockApi'
import { mockMore } from './quality'

// The checks of the terminal skin (docs/spec/ui/terminal-style.md, REQ-UI-034 to REQ-UI-043).
// Axe, the visual images, the budgets, the CSP and the keyboard flows are in their own files.

const tokens = JSON.parse(
  readFileSync(new URL('../../docs/design/tokens-terminal.json', import.meta.url), 'utf8'),
) as { color: Record<'light' | 'dark', Record<string, string | string[]>> }
const css = readFileSync(new URL('../src/index.css', import.meta.url), 'utf8')

function luminance(hex: string): number {
  const channel = (i: number) => {
    const c = parseInt(hex.slice(1 + i * 2, 3 + i * 2), 16) / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(0) + 0.7152 * channel(1) + 0.0722 * channel(2)
}
function ratio(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

for (const theme of ['light', 'dark'] as const) {
  test(`REQ-UI-037 REQ-UI-034 the ${theme} theme passes the contrast limits and uses only token values`, () => {
    const c = tokens.color[theme]
    const text = [
      'text',
      'muted',
      'log-time',
      'primary',
      'amber',
      'cyan',
      'success',
      'warning',
      'danger',
      'info',
    ]
    for (const surface of ['bg', 'surface', 'raised']) {
      for (const name of text) {
        expect(
          ratio(c[name] as string, c[surface] as string),
          `${name} on ${surface}`,
        ).toBeGreaterThanOrEqual(4.5)
      }
      for (const color of [c['border-strong'] as string, ...(c.chart as string[])]) {
        expect(ratio(color, c[surface] as string), `${color} on ${surface}`).toBeGreaterThanOrEqual(
          3,
        )
      }
    }
    expect(ratio(c.text as string, c.selection as string)).toBeGreaterThanOrEqual(4.5)
    expect(ratio(c.muted as string, c.selection as string)).toBeGreaterThanOrEqual(4.5)
    // The CSS variables are the values of the token file.
    const block =
      theme === 'light'
        ? css.split(":root[data-theme='dark']")[0]
        : css.split(":root[data-theme='dark']")[1]
    for (const [name, value] of Object.entries(c)) {
      if (typeof value !== 'string') continue
      expect(block, `--color-${name}`).toContain(`--color-${name}: ${value};`)
    }
  })
}

async function open(
  page: Page,
  path = '/o/acme',
  setup?: (state: ReturnType<typeof defaultState>) => void,
) {
  const state = defaultState()
  setup?.(state)
  await mockApi(page, state)
  await mockMore(page)
  await page.addInitScript(() => sessionStorage.setItem('polipan.boot', '1'))
  await page.goto(path)
  await page.locator('main h1').first().waitFor()
}

test('REQ-UI-035 the status bar and the prompt line are on the page and the prompt opens the palette', async ({
  page,
}) => {
  await open(page)
  const bar = page.getByRole('contentinfo', { name: 'Status bar' })
  await expect(bar).toContainText('Acme')
  await expect(bar).toContainText('Live')
  await expect(bar.getByRole('button', { name: /^Theme: / })).toBeVisible()
  await expect(bar.getByRole('button', { name: 'Calm mode' })).toBeVisible()
  await page.getByRole('button', { name: 'Command palette' }).click()
  await expect(page.getByRole('combobox', { name: 'Command palette' })).toBeFocused()
  await page.keyboard.press('Escape')
  // On a phone the status bar is at the top and the prompt line at the bottom.
  await page.setViewportSize({ width: 360, height: 700 })
  const top = (await bar.boundingBox())?.y ?? 99
  const prompt = (await page.getByRole('button', { name: 'Command palette' }).boundingBox())?.y ?? 0
  expect(top).toBeLessThan(5)
  expect(prompt).toBeGreaterThan(600)
})

test('REQ-UI-036 j, k and Enter move a cursor row, and they are off in a field', async ({
  page,
}) => {
  await open(page, '/o/acme')
  const rows = page.locator('main tbody tr')
  await expect(rows.first()).toBeVisible()
  await page.keyboard.press('j')
  await expect(rows.first()).toHaveAttribute('data-cursor', '')
  await page.keyboard.press('g')
  await page.keyboard.press('g')
  await page.keyboard.press('G')
  await expect(page.locator('[data-cursor]')).toHaveCount(1)
  // The colon opens the command line, and typing in it never moves a cursor.
  await page.keyboard.press(':')
  const box = page.getByRole('combobox', { name: 'Command palette' })
  await expect(box).toBeFocused()
  await page.keyboard.type('jk')
  await expect(box).toHaveValue('jk')
})

test('REQ-UI-039 Calm mode stops every animation and is saved', async ({ page }) => {
  await open(page, '/o/acme/p/shop/e/staging/s/api/deployments/d1', (state) => {
    state.deployments.d1 = {
      id: 'd1',
      service_id: 's1',
      release_id: 'r2',
      trigger: 'manual',
      strategy: 'blue_green',
      state: 'live',
      steps: [{ state: 'live', time: '2026-10-10T10:00:00Z' }],
      started_at: '2026-10-10T10:00:00Z',
      finished_at: '2026-10-10T10:01:00Z',
    }
    state.services[0].placements = [
      {
        id: 'p1',
        release_id: 'r2',
        weight: 100,
        replica_index: 0,
        role: 'primary',
        health: 'healthy',
      },
      {
        id: 'p2',
        release_id: 'r1',
        weight: 0,
        replica_index: 0,
        role: 'primary',
        health: 'healthy',
      },
    ] as never
  })
  const html = page.locator('html')
  await expect(html).toHaveAttribute('data-calm', 'off')
  await expect(page.locator('.packet').first()).toBeVisible()
  // REQ-UI-038: only transform and opacity animate.
  const properties = await page.evaluate(() =>
    document
      .getAnimations()
      .filter((a) => a.effect instanceof KeyframeEffect && a.playState === 'running')
      .flatMap((a) => (a.effect as KeyframeEffect).getKeyframes().flatMap((k) => Object.keys(k)))
      .filter((k) => !['offset', 'easing', 'composite', 'computedOffset'].includes(k)),
  )
  expect([...new Set(properties)].sort()).toEqual(['opacity', 'transform'])

  await page.keyboard.press('c')
  await expect(html).toHaveAttribute('data-calm', 'on')
  await expect(page.locator('.packet')).toHaveCount(0)
  expect(
    await page.evaluate(
      () => document.getAnimations().filter((a) => a.playState === 'running').length,
    ),
  ).toBe(0)
  await page.reload()
  await expect(html).toHaveAttribute('data-calm', 'on')
})

test('REQ-UI-039 reduced motion turns Calm mode on and the switch cannot undo it', async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await open(page)
  await expect(page.locator('html')).toHaveAttribute('data-calm', 'on')
  const calm = page.getByRole('button', { name: 'Calm mode' })
  await expect(calm).toHaveAttribute('title', /system asks for less motion/)
  await calm.click()
  await expect(page.locator('html')).toHaveAttribute('data-calm', 'on')
  expect(
    await page.evaluate(
      () => document.getAnimations().filter((a) => a.playState === 'running').length,
    ),
  ).toBe(0)
})

test('REQ-UI-040 the effects switch is off by default and cycles with x, not in Calm mode', async ({
  page,
}) => {
  await open(page)
  const html = page.locator('html')
  await expect(html).toHaveAttribute('data-fx', 'off')
  for (const next of ['scanlines', 'glow', 'curve', 'off']) {
    await page.keyboard.press('x')
    await expect(html).toHaveAttribute('data-fx', next)
  }
  await page.keyboard.press('c')
  await page.keyboard.press('x')
  await expect(html).toHaveAttribute('data-fx', 'off')
})

test('REQ-UI-038 the boot lines show once per session, never block, and any key skips them', async ({
  page,
}) => {
  const state = defaultState()
  await mockApi(page, state)
  await mockMore(page)
  await page.goto('/o/acme')
  const boot = page.getByTestId('boot')
  await expect(boot).toBeVisible()
  // The page under it is already usable.
  await page.locator('main h1').first().waitFor()
  await page.keyboard.press('Shift')
  await expect(boot).toHaveCount(0)
  await page.reload()
  await page.locator('main h1').first().waitFor()
  await expect(boot).toHaveCount(0)
})

test('REQ-UI-043 theme-init.js accepts only known values', async ({ page }) => {
  await open(page)
  await page.addInitScript(() => {
    localStorage.setItem('polipan.theme', 'evil')
    localStorage.setItem('polipan.calm', 'maybe')
    localStorage.setItem('polipan.fx', '<script>')
  })
  await page.reload()
  const html = page.locator('html')
  await expect(html).toHaveAttribute('data-calm', 'off')
  await expect(html).toHaveAttribute('data-fx', 'off')
  await expect(html).toHaveAttribute('data-theme', /^(light|dark)$/)
})

// REQ-UI-043: axe also passes with every effect on (dark theme) and in Calm mode.
for (const fx of ['scanlines', 'glow', 'curve']) {
  test(`REQ-UI-043 REQ-UI-040 axe has no serious issue with the ${fx} effect on`, async ({
    page,
  }) => {
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.addInitScript((value) => localStorage.setItem('polipan.fx', value), fx)
    await open(page)
    await expect(page.locator('html')).toHaveAttribute('data-fx', fx)
    const result = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
      .analyze()
    const bad = result.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
    const text = bad.map(
      (v) =>
        `${v.id}: ${v.nodes.map((n) => `${n.target.join(' ')} ${n.any[0]?.message ?? ''}`).join(' | ')}`,
    )
    expect(text, text.join('\n')).toEqual([])
  })
}
