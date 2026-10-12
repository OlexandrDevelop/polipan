// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState, mockApi } from './mockApi'

const serviceUrl = '/o/acme/p/shop/e/staging/s/api'

// An action says how it ended in a message that is in view, also when the page is scrolled.
test('a failed save shows a red message with the reason, a good save shows a green one', async ({
  page,
}) => {
  const state = defaultState()
  await mockApi(page, state)
  let fail = true
  await page.route('**/api/v1/services/s1', async (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback()
    if (fail) {
      return route.fulfill({
        status: 422,
        contentType: 'application/json',
        body: JSON.stringify({
          code: 'invalid_argument',
          message: 'config: the port is already used',
          request_id: 'req_abc',
        }),
      })
    }
    const service = state.services.find((s) => s.id === 's1')
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(service),
    })
  })
  await page.setViewportSize({ width: 1280, height: 500 })
  await page.goto(`${serviceUrl}/settings`)
  const save = page.getByRole('button', { name: 'Save', exact: true }).first()
  await save.scrollIntoViewIfNeeded()
  await save.click()

  const bad = page.getByTestId('toast').filter({ hasText: 'config: the port is already used' })
  await expect(bad).toBeVisible()
  await expect(bad).toBeInViewport()
  await expect(bad).toHaveAttribute('data-kind', 'danger')
  await expect(bad).toHaveAttribute('role', 'alert')
  await bad.getByRole('button', { name: 'Close' }).click()
  await expect(bad).toBeHidden()

  fail = false
  await save.click()
  const good = page.getByTestId('toast').filter({ hasText: 'Saved. Deploy to apply the changes.' })
  await expect(good).toBeVisible()
  await expect(good).toHaveAttribute('data-kind', 'success')
  await expect(good).toBeInViewport()
})

test('a request that cannot reach the server shows a red message', async ({ page }) => {
  const state = defaultState()
  await mockApi(page, state)
  await page.route('**/api/v1/services/s1', (route) =>
    route.request().method() === 'PATCH' ? route.abort() : route.fallback(),
  )
  await page.goto(`${serviceUrl}/settings`)
  await page.getByRole('button', { name: 'Save', exact: true }).first().click()
  await expect(
    page.getByTestId('toast').filter({ hasText: 'The server does not answer.' }),
  ).toBeVisible()
})
