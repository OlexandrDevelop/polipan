// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState, mockApi } from './mockApi'

const serviceUrl = '/o/acme/p/shop/e/staging/s/api'

test('REQ-UI-007 AC-DEPLOY-04 the deploy dialog shows the plan before anything starts', async ({
  page,
}) => {
  const state = defaultState()
  await mockApi(page, state)
  await page.goto(serviceUrl)

  await page.getByRole('button', { name: 'Deploy', exact: true }).click()
  const dialog = page.getByRole('dialog')
  // The plan: the forced strategy with its reason, the downtime and the warning.
  await expect(
    dialog.locator('dd').filter({ hasText: 'Recreate (stop the old, then start the new)' }),
  ).toBeVisible()
  await expect(dialog.getByText(/Zero downtime is not available/)).toBeVisible()
  await expect(dialog.getByText('Short downtime expected')).toBeVisible()
  await expect(dialog.getByText(/pre-deploy command runs while the old release/)).toBeVisible()
  // Only the dry run was sent so far.
  expect(state.deployCalls.map((c) => c.dry_run)).toEqual([true])

  await dialog.getByRole('button', { name: 'Deploy', exact: true }).click()
  await expect(page).toHaveURL(/\/deployments\/d1$/)
  expect(state.deployCalls.map((c) => c.dry_run)).toEqual([true, false])
})

test('the deploy dialog works on a page without a secure context (plain HTTP)', async ({
  page,
}) => {
  // A panel opened on http://<server address> has no crypto.randomUUID. The dialog must still start the deployment.
  await page.addInitScript(() => {
    Object.defineProperty(crypto, 'randomUUID', { value: undefined })
  })
  const state = defaultState()
  await mockApi(page, state)
  const keys: string[] = []
  page.on('request', (r) => {
    const key = r.headers()['idempotency-key']
    if (r.method() === 'POST' && r.url().endsWith('/services/s1/deploy') && key) keys.push(key)
  })
  await page.goto(serviceUrl)
  await page.getByRole('button', { name: 'Deploy', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Deploy', exact: true }).click()
  await expect(page).toHaveURL(/\/deployments\/d1$/)
  expect(keys.length).toBeGreaterThan(0)
  for (const key of keys) {
    expect(key).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
  }
})

test('REQ-UI-002 the deployment page moves to live steps without a reload', async ({ page }) => {
  const state = defaultState()
  state.deployments.d1 = {
    id: 'd1',
    service_id: 's1',
    release_id: 'r3',
    previous_release_id: 'r2',
    trigger: 'manual',
    strategy: 'recreate',
    state: 'building',
    steps: [{ state: 'building', time: '2026-10-10T10:00:00Z' }],
    started_at: '2026-10-10T10:00:00Z',
  }
  await mockApi(page, state)
  await page.goto(`${serviceUrl}/deployments/d1`)

  // The stream sends "live". The page reads the new state and shows the step.
  const steps = page.getByRole('list').filter({ hasText: 'Live' }).first()
  await expect(steps).toContainText('Building')
  await expect(page.getByText('The deployment is live.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Roll back' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Cancel deployment' })).toHaveCount(0)
})

test('AC-REL-01 the rollback button flow starts a rollback of the chosen release', async ({
  page,
}) => {
  const state = defaultState()
  await mockApi(page, state)
  await page.goto(`${serviceUrl}/releases`)

  await expect(page.getByText('v2').first()).toBeVisible()
  await page.getByRole('button', { name: 'Roll back', exact: true }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByText('Fast rollback, about 5 seconds.')).toBeVisible()
  await expect(dialog.getByText('Data in volumes and database content.')).toBeVisible()
  await expect(dialog.getByText(/secret_versions.STRIPE_KEY: 3 → 2/)).toBeVisible()
  expect(state.rollbackCalls).toEqual([])

  await dialog.getByRole('button', { name: 'Roll back to v1' }).click()
  await expect(page).toHaveURL(/\/deployments\/d2$/)
  expect(state.rollbackCalls).toEqual([{ release_id: 'r1' }])
})

test('the rollback dialog works on a page without a secure context (plain HTTP)', async ({
  page,
}) => {
  await page.addInitScript(() => {
    Object.defineProperty(crypto, 'randomUUID', { value: undefined })
  })
  const state = defaultState()
  await mockApi(page, state)
  await page.goto(`${serviceUrl}/releases`)
  await page.getByRole('button', { name: 'Roll back', exact: true }).first().click()
  await page.getByRole('dialog').getByRole('button', { name: 'Roll back to v1' }).click()
  await expect(page).toHaveURL(/\/deployments\/d2$/)
  expect(state.rollbackCalls).toEqual([{ release_id: 'r1' }])
})

test('REQ-UI-022 REQ-UI-002 a paused event stream shows the banner and clears it', async ({
  page,
}) => {
  const state = defaultState()
  state.eventsDown = true
  await mockApi(page, state)
  await page.goto('/o/acme/projects')

  const banner = page.getByText('Live updates paused. Reconnecting…')
  await expect(banner).toBeVisible({ timeout: 10_000 })
  // The page data stays visible while the banner shows.
  await expect(page.getByRole('link', { name: 'Shop' })).toBeVisible()
  state.eventsDown = false
  await expect(banner).toBeHidden({ timeout: 10_000 })
})

test('REQ-UI-016 the new M3 screens have no horizontal scroll at 360 px', async ({ page }) => {
  const state = defaultState()
  state.deployments.d1 = {
    id: 'd1',
    service_id: 's1',
    state: 'live',
    trigger: 'manual',
    started_at: '2026-10-10T10:00:00Z',
  }
  await mockApi(page, state)
  await page.setViewportSize({ width: 360, height: 740 })
  for (const path of [
    `${serviceUrl}/releases`,
    `${serviceUrl}/health`,
    `${serviceUrl}/settings`,
    `${serviceUrl}/releases/r2/diff/r1`,
    `${serviceUrl}/deployments/d1`,
    '/o/acme/deployments',
    '/o/acme',
  ]) {
    await page.goto(path)
    await expect(page.locator('h1')).toBeVisible()
    const wide = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
    )
    expect(wide, path).toBe(false)
  }
})
