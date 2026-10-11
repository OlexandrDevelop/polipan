// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState } from './mockApi'
import { setUp } from './quality'

// REQ-UI-029: a table with 1 000 rows draws its first rows within 200 ms, and the rest on request.
test('REQ-UI-029 the first paint of a table with 1 000 rows takes at most 200 ms', async ({
  page,
}) => {
  await setUp(page, defaultState())
  const tokens = Array.from({ length: 1000 }, (_, i) => ({
    id: `t${i}`,
    name: `token ${i}`,
    token_prefix: `plp_${i}`,
    scopes: ['deploy'],
  }))
  await page.route('**/api/v1/tokens*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(tokens) }),
  )
  await page.goto('/o/acme/projects')
  await page.locator('h1').first().waitFor()
  // The time from the click to the moment the rows are in the page.
  const took = await page.evaluate(
    () =>
      new Promise<number>((resolve) => {
        const link = [...document.querySelectorAll('nav a')].find((a) =>
          /tokens/i.test(a.getAttribute('href') ?? ''),
        ) as HTMLElement | undefined
        const open = link ?? document.createElement('a')
        const start = performance.now()
        new MutationObserver((_, observer) => {
          if (document.querySelectorAll('main tbody tr').length >= 200) {
            observer.disconnect()
            // After the next frame: the rows are painted.
            requestAnimationFrame(() => resolve(performance.now() - start))
          }
        }).observe(document.body, { childList: true, subtree: true })
        open.click()
      }),
  )
  console.log(`1 000 rows: first paint after ${took.toFixed(0)} ms`)
  expect(took).toBeLessThanOrEqual(200)
  await expect(page.locator('main tbody tr')).toHaveCount(200)
  await page.getByRole('button', { name: 'Show 200 more' }).click()
  await expect(page.locator('main tbody tr')).toHaveCount(400)
})
