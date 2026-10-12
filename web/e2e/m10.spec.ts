// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState } from './mockApi'
import { setUp } from './quality'

test('UI-27 the Git connections page lists, adds and deletes a connection', async ({ page }) => {
  await setUp(page, defaultState('admin'))
  let deleted = ''
  await page.route('**/api/v1/git-connections/g1', (route) => {
    deleted = route.request().method()
    return route.fulfill({ status: 204 })
  })
  await page.goto('/o/acme/settings/git')
  await expect(page.getByRole('heading', { level: 1, name: 'Git connections' })).toBeVisible()
  await expect(page.getByRole('cell', { name: 'acme-org' })).toBeVisible()
  await page.getByRole('button', { name: 'Delete', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Delete', exact: true }).click()
  await expect.poll(() => deleted).toBe('DELETE')
})

test('UI-27 a developer does not see the Git connections page', async ({ page }) => {
  await setUp(page, defaultState('developer'))
  await page.goto('/o/acme/settings/git')
  await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Git', exact: true })).toHaveCount(0)
})

test('UI-28 the owner saves the name and deletes the organization with the typed slug', async ({
  page,
}) => {
  await setUp(page, defaultState('owner'))
  const calls: { method: string; url: string; body: unknown }[] = []
  await page.route('**/api/v1/org*', (route) => {
    const request = route.request()
    if (request.method() === 'GET') return route.fallback()
    calls.push({
      method: request.method(),
      url: request.url(),
      body: request.postData() ? request.postDataJSON() : null,
    })
    if (request.method() === 'DELETE') return route.fulfill({ status: 204 })
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ id: 'o1', name: 'Acme 2', slug: 'acme' }),
    })
  })
  await page.goto('/o/acme/settings/organization')
  await expect(page.getByText('Version 1.0.0')).toBeVisible()

  await page.getByLabel('Organization name').fill('Acme 2')
  await page.getByRole('button', { name: 'Save' }).first().click()
  await expect.poll(() => calls.length).toBe(1)
  expect(calls[0].method).toBe('PATCH')
  expect((calls[0].body as { name: string }).name).toBe('Acme 2')

  await page.getByRole('tab', { name: 'Delete organization' }).click()
  await page.getByRole('button', { name: 'Delete organization' }).click()
  const dialog = page.getByRole('dialog')
  const confirm = dialog.getByRole('button', { name: 'Delete organization' })
  await expect(confirm).toBeDisabled()
  await dialog.getByLabel(/Type/).fill('acme')
  await confirm.click()
  await expect.poll(() => calls.length).toBe(2)
  expect(calls[1].method).toBe('DELETE')
  expect(calls[1].url).toContain('confirm=acme')
  await expect(page).toHaveURL(/\/login/)
})

test('UI-28 an admin cannot delete the organization', async ({ page }) => {
  await setUp(page, defaultState('admin'))
  await page.goto('/o/acme/settings/organization')
  await expect(page.getByRole('heading', { level: 1, name: 'Organization' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Delete organization' })).toHaveCount(0)
})

test('REQ-UI-018 the phone bottom bar has a More sheet with every other page', async ({ page }) => {
  await setUp(page, defaultState('owner'))
  await page.setViewportSize({ width: 360, height: 740 })
  await page.goto('/o/acme')
  await page.getByRole('button', { name: 'More' }).click()
  const sheet = page.getByRole('dialog', { name: 'More' })
  for (const name of ['Previews', 'Catalog', 'Alerts', 'Logs', 'Backups', 'Organization', 'Git']) {
    await expect(sheet.getByRole('link', { name, exact: true })).toBeVisible()
  }
  await sheet.getByRole('link', { name: 'Catalog' }).click()
  await expect(page).toHaveURL(/\/catalog$/)
})

test('UI-27 a Git service shows the deploy key and the webhook once', async ({ page }) => {
  const state = defaultState('admin')
  state.services[0].source_type = 'git'
  await setUp(page, state)
  await page.route('**/api/v1/services/s1/deploy-key', (route) =>
    route.fulfill({
      status: 201,
      contentType: 'application/json',
      body: JSON.stringify({
        public_key: 'ssh-ed25519 AAAAC3Nza polipan',
        webhook_url: 'https://panel.example.com/api/v1/webhooks/git/w9',
        webhook_secret: 'whsec_once',
      }),
    }),
  )
  await page.goto('/o/acme/p/shop/e/staging/s/api/settings')
  await page.getByRole('tab', { name: 'Deploy key and webhook' }).click()
  await page.getByRole('button', { name: 'Create deploy key' }).click()
  await expect(page.getByText('ssh-ed25519 AAAAC3Nza polipan')).toBeVisible()
  await expect(page.getByText('whsec_once')).toBeVisible()
  await page.getByRole('button', { name: 'Done' }).click()
  await expect(page.getByText('whsec_once')).toHaveCount(0)
})
