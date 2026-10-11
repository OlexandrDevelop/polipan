// SPDX-License-Identifier: Apache-2.0

import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
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
