// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState, LEAKED_SECRET, mockApi } from './mockApi'

const serviceUrl = '/o/acme/p/shop/e/staging/s/api'

test('AC-ENV-02 REQ-UI-005 the variables screen never shows a secret value', async ({ page }) => {
  await mockApi(page, defaultState())
  await page.goto(`${serviceUrl}/env`)

  await expect(page.getByText('STRIPE_KEY').first()).toBeVisible()
  await expect(page.getByText('Set, v3').first()).toBeVisible()
  await expect(page.locator('body')).not.toContainText(LEAKED_SECRET)

  // The value field is write-only: empty, masked, no reveal button.
  await page.getByRole('button', { name: 'Set new value' }).first().click()
  const dialog = page.getByRole('dialog')
  const input = dialog.getByLabel('New value')
  await expect(input).toHaveAttribute('type', 'password')
  await expect(input).toHaveValue('')
  await expect(dialog.getByRole('button', { name: /show|reveal/i })).toHaveCount(0)

  await input.fill('sk_live_new_value')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toBeHidden()
  await expect(page.getByText('Set, v4').first()).toBeVisible()
  await expect(page.locator('body')).not.toContainText('sk_live_new_value')
  await expect(page.locator('input[type="password"]')).toHaveCount(0)
})

test('AC-ENV-03 REQ-ENV-003 a changed variable shows pending changes until a deploy', async ({
  page,
}) => {
  await mockApi(page, defaultState())
  await page.goto(`${serviceUrl}/env`)

  await expect(page.getByText('LOG_LEVEL').first()).toBeVisible()
  await expect(page.getByText('Pending changes: redeploy to apply.')).toHaveCount(0)

  await page.getByRole('button', { name: 'Edit' }).first().click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Value').fill('debug')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toBeHidden()

  await expect(page.getByText('Pending changes: redeploy to apply.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Redeploy' })).toBeVisible()
})

test('REQ-UI-031 the wizard keeps data and asks before leaving with unsaved data', async ({
  page,
}) => {
  await mockApi(page, defaultState())
  await page.goto('/o/acme/p/shop/e/staging/new')
  await page.getByRole('link', { name: /Docker image/ }).click()
  await expect(page).toHaveURL(/source=image/)

  await page.getByLabel('Image').fill('ghcr.io/acme/web:1.0')
  await page.getByRole('button', { name: 'Next' }).click()
  await expect(page.getByRole('heading', { name: 'Build and run' })).toBeVisible()
  await page.getByRole('button', { name: 'Back' }).click()
  await expect(page.getByLabel('Image')).toHaveValue('ghcr.io/acme/web:1.0')

  // Leaving with data asks first. Cancel keeps the user and the data on the page.
  await page.getByRole('link', { name: 'Projects' }).click()
  const confirm = page.getByRole('dialog', { name: 'Leave this page?' })
  await expect(confirm).toBeVisible()
  await confirm.getByRole('button', { name: 'Cancel' }).click()
  await expect(page).toHaveURL(/\/new/)
  await expect(page.getByLabel('Image')).toHaveValue('ghcr.io/acme/web:1.0')

  // Confirming leaves.
  await page.getByRole('link', { name: 'Projects' }).click()
  await page.getByRole('button', { name: 'Leave and lose data' }).click()
  await expect(page).toHaveURL(/\/o\/acme\/projects$/)
})

test('REQ-UI-019 Ctrl+K opens the command palette and it works with the keyboard', async ({
  page,
}) => {
  await mockApi(page, defaultState())
  await page.goto('/o/acme/p/shop/e/staging')
  await expect(page.getByRole('link', { name: 'api' })).toBeVisible()

  await page.keyboard.press('Control+k')
  const input = page.getByRole('combobox', { name: 'Command palette' })
  await expect(input).toBeFocused()
  await expect(page.getByRole('option', { name: /Add service/ })).toBeVisible()

  await input.fill('api')
  await expect(page.getByRole('option', { name: /^api/ })).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/s\/api$/)
})

test('REQ-UI-019 the palette lists only the commands the role allows', async ({ page }) => {
  await mockApi(page, defaultState('viewer'))
  await page.goto('/o/acme/p/shop/e/staging')
  await expect(page.getByRole('link', { name: 'api' })).toBeVisible()
  await page.keyboard.press('Control+k')
  await page.getByRole('combobox', { name: 'Command palette' }).fill('>')
  await expect(page.getByRole('option', { name: /Switch theme/ })).toBeVisible()
  await expect(page.getByRole('option', { name: /Add service/ })).toHaveCount(0)
  await expect(page.getByRole('option', { name: /Add node/ })).toHaveCount(0)
})

test('REQ-UI-016 the new M2 screens have no horizontal scroll at 360 px', async ({ page }) => {
  await mockApi(page, defaultState())
  await page.setViewportSize({ width: 360, height: 740 })
  for (const path of [
    `${serviceUrl}/env`,
    `${serviceUrl}/volumes`,
    `${serviceUrl}/settings`,
    `${serviceUrl}/terminal`,
    '/o/acme/p/shop/e/staging/new?source=git',
  ]) {
    await page.goto(path)
    await expect(page.locator('h1')).toBeVisible()
    const wide = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
    )
    expect(wide, path).toBe(false)
  }
})
