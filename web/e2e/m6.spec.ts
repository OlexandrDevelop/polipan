// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState, mockApi } from './mockApi'

const configUrl = '/o/acme/p/shop/config'

const changePlan = {
  plan_hash: 'abc123',
  valid: true,
  changes: [
    {
      action: 'update' as const,
      resource: 'service',
      name: 'api',
      fields: [{ path: 'replicas', before: 1, after: 2 }],
    },
    { action: 'unmanaged' as const, resource: 'service', name: 'old' },
  ],
}

test('REQ-UI-006 REQ-CAC-004 the plan shows the diff and apply sends the plan hash', async ({
  page,
}) => {
  const state = defaultState()
  state.configPlan = changePlan
  await mockApi(page, state)
  await page.goto(configUrl)

  await expect(page.getByRole('heading', { level: 1, name: 'Config as code' })).toBeVisible()
  await expect(page.getByText('In sync')).toBeVisible()
  await page.getByRole('button', { name: 'Load current state' }).click()
  await expect(page.getByLabel('polipan.yaml', { exact: true })).toHaveValue(/version: 1/)

  await page.getByRole('button', { name: 'Plan now' }).click()
  await expect(page.getByText('Update: service api')).toBeVisible()
  await expect(page.getByRole('cell', { name: '1', exact: true })).toBeVisible()
  await expect(page.getByRole('cell', { name: '2', exact: true })).toBeVisible()
  expect(state.applyCalls).toEqual([])

  await page.getByRole('button', { name: 'Apply', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByText('abc123')).toBeVisible()
  await dialog.getByRole('button', { name: 'Apply', exact: true }).click()
  await expect(dialog).toBeHidden()
  expect(state.applyCalls).toHaveLength(1)
  expect(state.applyCalls[0].plan_hash).toBe('abc123')
  expect(state.applyCalls[0].prune).toBeUndefined()
})

test('REQ-CAC-007 a file with errors shows the line and the path', async ({ page }) => {
  const state = defaultState()
  state.configPlanError = {
    code: 'validation_failed',
    message: 'The file is not valid.',
    details: { errors: [{ line: 7, path: 'services.api.replicas', message: 'must be a number' }] },
  }
  await mockApi(page, state)
  await page.goto(configUrl)

  await page.getByLabel('polipan.yaml', { exact: true }).fill('version: 1')
  await page.getByRole('button', { name: 'Plan now' }).click()
  await expect(page.getByText('Line 7 services.api.replicas: must be a number')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Apply', exact: true })).toHaveCount(0)
})

test('REQ-CAC-013 a pending plan shows a banner, and apply is refused after a change', async ({
  page,
}) => {
  const state = defaultState()
  state.configState = 'plan_pending'
  state.configPlan = changePlan
  state.applyConflict = 'plan_changed'
  await mockApi(page, state)
  await page.goto(configUrl)

  await expect(page.getByText('A change from Git waits for an admin.')).toBeVisible()
  await page.getByLabel('polipan.yaml', { exact: true }).fill('version: 1')
  await page.getByRole('button', { name: 'Plan now' }).click()
  await page.getByRole('button', { name: 'Apply', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Delete resources that are not in the file').check()
  await dialog.getByRole('button', { name: 'Apply', exact: true }).click()
  await expect(dialog.getByText('The project changed after the plan. Plan again.')).toBeVisible()
  expect(state.applyCalls[0].prune).toBe(true)
})

test('REQ-UI-006 a viewer sees the state but cannot apply or change the source', async ({
  page,
}) => {
  const state = defaultState('viewer')
  state.configPlan = changePlan
  await mockApi(page, state)
  await page.goto(configUrl)

  await expect(page.getByText('Only an admin can change the config source.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Save' })).toHaveCount(0)
  await page.getByLabel('polipan.yaml', { exact: true }).fill('version: 1')
  await page.getByRole('button', { name: 'Plan now' }).click()
  await expect(page.getByText('Update: service api')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Apply', exact: true })).toHaveCount(0)
})
