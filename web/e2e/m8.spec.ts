// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState, mockApi } from './mockApi'

const logsUrl = '/o/acme/p/shop/e/staging/s/api/logs'
const HTML_LINE = '<img src=x onerror="window.__pwned=1"> hello'

test('REQ-OBS-101 REQ-UI-026 the log viewer shows HTML as text and has a table view', async ({
  page,
}) => {
  const state = defaultState()
  state.logLines = [
    {
      time: '2026-10-10T10:00:00Z',
      message: HTML_LINE,
      level: 'error',
      stream: 'stdout',
      replica: 0,
      match: true,
    },
    { time: '2026-10-10T10:00:01Z', message: 'token=***', level: 'info', replica: 0, match: true },
  ]
  await mockApi(page, state)
  await page.goto(logsUrl)

  const log = page.getByRole('log', { name: 'Service log' })
  await expect(log).toContainText(HTML_LINE)
  await expect(log).toContainText('token=***')
  await expect(log.locator('img')).toHaveCount(0)
  expect(await page.evaluate(() => (window as unknown as { __pwned?: number }).__pwned)).toBe(
    undefined,
  )

  await page.getByRole('button', { name: 'Show as table' }).click()
  const table = page.getByRole('table', { name: 'Service log as a table' })
  await expect(table.getByRole('cell', { name: HTML_LINE })).toBeVisible()
  await expect(table.getByRole('columnheader', { name: 'Message' })).toBeVisible()
})

test('UI-22 an admin mutes a rule with a reason, a viewer cannot', async ({ page }) => {
  const state = defaultState()
  state.alertRules = [
    {
      id: 'r1',
      name: 'CPU high',
      type: 'cpu_high',
      severity: 'warning',
      channel_ids: [],
      repeat_interval_seconds: 14400,
      enabled: true,
    },
  ]
  state.alertInstances = [
    {
      id: 'a1',
      rule_id: 'r1',
      rule_name: 'CPU high',
      rule_type: 'cpu_high',
      severity: 'warning',
      state: 'firing',
      first_fired_at: '2026-10-10T09:00:00Z',
    },
  ]
  state.alertChannels = [
    {
      id: 'c1',
      kind: 'telegram',
      name: 'Ops chat',
      state: 'failing',
      last_error: 'chat not found',
    },
  ]
  await mockApi(page, state)
  await page.goto('/o/acme/alerts')

  await expect(page.getByRole('heading', { level: 1, name: 'Alerts' })).toBeVisible()
  await expect(page.getByText('Some alert rules have no channel')).toBeVisible()
  await expect(page.getByText('chat not found')).toBeVisible()
  await page.getByRole('button', { name: 'Mute' }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('button', { name: 'Mute rule' })).toHaveAttribute(
    'aria-disabled',
    'true',
  )
  await dialog.getByLabel('Reason').fill('planned work')
  await dialog.getByRole('button', { name: 'Mute rule' }).click()
  await expect(dialog).toBeHidden()
  expect(state.muteCalls).toHaveLength(1)
  expect(state.muteCalls[0].rule_id).toBe('r1')
  expect(state.muteCalls[0].reason).toBe('planned work')
})

test('UI-22 a viewer sees the alerts without a Mute button', async ({ page }) => {
  const state = defaultState('viewer')
  state.alertInstances = []
  state.alertRules = [
    {
      id: 'r1',
      name: 'CPU high',
      type: 'cpu_high',
      severity: 'warning',
      channel_ids: ['c1'],
      repeat_interval_seconds: 14400,
    },
  ]
  await mockApi(page, state)
  await page.goto('/o/acme/alerts')
  await expect(page.getByText('All quiet')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Mute' })).toHaveCount(0)
})

test('REQ-DEPLOY-006 the canary bar shows the gate and its buttons call the API', async ({
  page,
}) => {
  const state = defaultState()
  state.deployments.dc = {
    id: 'dc',
    service_id: 's1',
    release_id: 'r3',
    previous_release_id: 'r2',
    trigger: 'manual',
    strategy: 'canary',
    state: 'shifting',
    canary_steps: [10, 25, 50, 100],
    canary_step: 10,
    canary_paused: false,
    steps: [{ state: 'shifting', time: new Date().toISOString() }],
    started_at: new Date().toISOString(),
  }
  await mockApi(page, state)
  await page.goto('/o/acme/p/shop/e/staging/s/api/deployments/dc')

  const bar = page.getByRole('heading', { name: 'Canary' })
  await expect(bar).toBeVisible()
  await expect(page.getByRole('list', { name: 'Canary steps' }).getByRole('listitem')).toHaveCount(
    4,
  )
  await expect(page.getByText('Gate passed')).toBeVisible()
  await expect(page.getByText('120')).toBeVisible()

  await page.getByRole('button', { name: 'Promote' }).click()
  await expect.poll(() => state.canaryCalls).toEqual(['promote'])
  await page.getByRole('button', { name: 'Pause' }).click()
  await expect(page.getByRole('button', { name: 'Resume' })).toBeVisible()
  expect(state.canaryCalls).toEqual(['promote', 'pause'])

  await page.getByRole('button', { name: 'Abort' }).click()
  const dialog = page.getByRole('dialog')
  expect(state.canaryCalls).toHaveLength(2)
  await dialog.getByRole('button', { name: 'Abort' }).click()
  await expect.poll(() => state.canaryCalls).toEqual(['promote', 'pause', 'abort'])
})

test('REQ-UI-033 the restore-verified badge shows on the dashboard and on backups', async ({
  page,
}) => {
  const state = defaultState()
  const finished = new Date(Date.now() - 3 * 86_400_000).toISOString()
  state.backupPolicies = [
    {
      id: 'bp1',
      service_id: 's1',
      schedule: '0 2 * * *',
      engine: 'postgres',
      enabled: true,
      restore_check_schedule: '0 3 * * 0',
      restore_check_enabled: true,
      health: 'verified',
      last_restore_check: { id: 'rc1', state: 'passed', finished_at: finished },
    },
  ]
  await mockApi(page, state)
  await page.goto('/o/acme')
  await expect(page.getByText('Restore verified 3 days ago')).toBeVisible()

  state.backupPolicies[0].last_restore_check = {
    id: 'rc2',
    state: 'passed',
    finished_at: new Date(Date.now() - 40 * 86_400_000).toISOString(),
  }
  await page.goto('/o/acme/backups')
  await expect(page.getByText('Restore verified 40 days ago').first()).toBeVisible()
})

test('REQ-UI-026 a chart has a table view, and a monitoring outage shows a banner', async ({
  page,
}) => {
  const state = defaultState()
  await mockApi(page, state)
  await page.goto('/o/acme/p/shop/e/staging/s/api/metrics')

  await expect(page.getByText('CPU: last 70 %, max 70 %').first()).toBeVisible()
  await page.getByRole('button', { name: 'Show as table' }).first().click()
  await expect(page.getByRole('table', { name: 'CPU as a table' })).toBeVisible()

  state.metricsDown = true
  await page.getByLabel('Time range').selectOption('6h')
  await expect(page.getByText('Monitoring is not available')).toBeVisible()
})
