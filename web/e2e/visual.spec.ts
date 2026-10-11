// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState } from './mockApi'
import { openScreen, SCREENS, setUp, THEMES, WIDTHS } from './quality'

// REQ-UI-030: visual regression of the main screens, in both themes, at four widths. The set is
// small on purpose (the screens marked "visual" in quality.ts and the login page). A difference
// of more than 0.1% of the pixels fails. A person approves a new image with
// "npx playwright test visual.spec.ts --update-snapshots" and checks it in.
const HEIGHT = 640

async function ready(page: import('@playwright/test').Page) {
  await page.evaluate(() => document.fonts.ready)
  // The pointer is parked, so no hover style is in the image.
  await page.mouse.move(0, 0)
}

for (const screen of SCREENS.filter((s) => s.visual)) {
  for (const theme of THEMES) {
    test(`REQ-UI-030 visual ${screen.name} in ${theme}`, async ({ page }) => {
      test.setTimeout(90_000)
      await page.clock.setFixedTime(new Date('2026-10-11T10:00:00Z'))
      const state = defaultState()
      await setUp(page, state)
      // The live log lines of the logs screen are fixed too.
      for (const width of WIDTHS) {
        await openScreen(page, screen, { width, theme })
        await page.setViewportSize({ width, height: HEIGHT })
        await ready(page)
        await expect(page).toHaveScreenshot(`${screen.name}-${theme}-${width}.png`)
      }
    })
  }
}

for (const theme of THEMES) {
  test(`REQ-UI-030 visual login in ${theme}`, async ({ page }) => {
    test.setTimeout(90_000)
    await page.clock.setFixedTime(new Date('2026-10-11T10:00:00Z'))
    await setUp(page, defaultState())
    await page.route('**/api/v1/me', (route) =>
      route.fulfill({
        status: 401,
        contentType: 'application/json',
        body: JSON.stringify({ code: 'unauthorized', message: 'no session', request_id: 'mock' }),
      }),
    )
    for (const width of WIDTHS) {
      await openScreen(page, { name: 'login', path: '/login' }, { width, theme })
      await page.setViewportSize({ width, height: HEIGHT })
      await ready(page)
      await expect(page).toHaveScreenshot(`login-${theme}-${width}.png`)
    }
  })
}
