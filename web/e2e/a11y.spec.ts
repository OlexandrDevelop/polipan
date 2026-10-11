// SPDX-License-Identifier: Apache-2.0

import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { defaultState } from './mockApi'
import { openScreen, SCREENS, setUp, THEMES, WIDTHS } from './quality'

// REQ-UI-030: axe on every main screen, in both themes, at four widths. Zero serious or critical.
for (const screen of SCREENS) {
  for (const theme of THEMES) {
    test(`REQ-UI-030 REQ-UI-016 axe has no serious issue on ${screen.name} in ${theme}`, async ({
      page,
    }) => {
      const state = defaultState()
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
      test.setTimeout(120_000)
      await setUp(page, state)
      for (const width of WIDTHS) {
        await openScreen(page, screen, { width, theme })
        const result = await new AxeBuilder({ page })
          .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
          .analyze()
        const bad = result.violations
          .filter((v) => v.impact === 'serious' || v.impact === 'critical')
          .map((v) => `${width}px ${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(' | ')}`)
        expect(bad, bad.join('\n')).toEqual([])
      }
    })
  }
}

// Dialogs and overlays: opened with the keyboard, checked with axe.
const OVERLAYS: { name: string; path: string; open: (page: Page) => Promise<void> }[] = [
  {
    name: 'command palette',
    path: '/o/acme/p/shop/e/staging/s/api',
    open: async (page) => {
      await page.keyboard.press('Control+k')
      await page.getByRole('combobox', { name: 'Command palette' }).waitFor()
    },
  },
  {
    name: 'shortcut help',
    path: '/o/acme/nodes',
    open: async (page) => {
      await page.keyboard.press('?')
      await page.getByRole('dialog', { name: 'Keyboard shortcuts' }).waitFor()
    },
  },
  {
    name: 'deploy dialog',
    path: '/o/acme/p/shop/e/staging/s/api',
    open: async (page) => {
      await page.keyboard.press('d')
      await page
        .getByRole('dialog')
        .getByText(/Zero downtime is not available/)
        .waitFor()
    },
  },
  {
    name: 'rollback dialog',
    path: '/o/acme/p/shop/e/staging/s/api/releases',
    open: async (page) => {
      await page.getByRole('button', { name: 'Roll back', exact: true }).first().click()
      await page.getByRole('dialog').getByText('Fast rollback, about 5 seconds.').waitFor()
    },
  },
  {
    name: 'delete organization dialog',
    path: '/o/acme/settings/organization',
    open: async (page) => {
      await page.getByRole('button', { name: 'Delete organization' }).click()
      await page.getByRole('dialog').waitFor()
    },
  },
]

for (const overlay of OVERLAYS) {
  for (const theme of THEMES) {
    test(`REQ-UI-030 axe has no serious issue in the ${overlay.name} in ${theme}`, async ({
      page,
    }) => {
      test.setTimeout(120_000)
      await setUp(page, defaultState())
      for (const width of WIDTHS) {
        await openScreen(page, { name: overlay.name, path: overlay.path }, { width, theme })
        await overlay.open(page)
        const result = await new AxeBuilder({ page })
          .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
          .analyze()
        const bad = result.violations
          .filter((v) => v.impact === 'serious' || v.impact === 'critical')
          .map((v) => `${width}px ${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(' | ')}`)
        expect(bad, bad.join('\n')).toEqual([])
      }
    })
  }
}
