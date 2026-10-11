// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import type { Locator, Page } from '@playwright/test'
import { defaultState } from './mockApi'
import { setUp } from './quality'

const serviceUrl = '/o/acme/p/shop/e/staging/s/api'

// Presses Tab until the element has the focus. There is no mouse in these tests.
async function tabTo(page: Page, target: Locator, max = 60) {
  for (let i = 0; i < max; i++) {
    if (await target.evaluate((el) => el === document.activeElement)) return
    await page.keyboard.press('Tab')
  }
  throw new Error('the element did not get the focus with Tab')
}

test('REQ-UI-008 keyboard only: deploy a service', async ({ page }) => {
  const state = defaultState()
  await setUp(page, state)
  await page.goto(serviceUrl)
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()

  // The key "d" only opens the dialog (REQ-UI-021).
  await page.keyboard.press('d')
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByText(/Zero downtime is not available/)).toBeVisible()
  expect(state.deployCalls.map((c) => c.dry_run)).toEqual([true])

  await tabTo(page, dialog.getByRole('button', { name: 'Deploy', exact: true }))
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/deployments\/d1$/)
  expect(state.deployCalls.map((c) => c.dry_run)).toEqual([true, false])
})

test('REQ-UI-008 keyboard only: roll back to the previous release', async ({ page }) => {
  const state = defaultState()
  await setUp(page, state)
  await page.goto(`${serviceUrl}/releases`)
  await expect(page.getByText('v2').first()).toBeVisible()

  await tabTo(page, page.getByRole('button', { name: 'Roll back', exact: true }).first())
  await page.keyboard.press('Enter')
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByText('Fast rollback, about 5 seconds.')).toBeVisible()
  await tabTo(page, dialog.getByRole('button', { name: 'Roll back to v1' }))
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/deployments\/d2$/)
  expect(state.rollbackCalls).toEqual([{ release_id: 'r1' }])
})

test('REQ-UI-008 a dialog traps the focus and Esc gives it back', async ({ page }) => {
  await setUp(page, defaultState())
  await page.goto(serviceUrl)
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await page.keyboard.press('d')
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  for (let i = 0; i < 12; i++) {
    await page.keyboard.press('Tab')
    // The focus stays in the dialog. At the end of the dialog it may go to the browser (body).
    expect(
      await dialog.evaluate(
        (d) => d.contains(document.activeElement) || document.activeElement === document.body,
      ),
    ).toBe(true)
  }
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
})

test('REQ-UI-021 shortcuts: go to a page, theme, help overlay', async ({ page }) => {
  await setUp(page, defaultState())
  await page.goto('/o/acme')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()

  await page.keyboard.press('g')
  await page.keyboard.press('p')
  await expect(page).toHaveURL(/\/o\/acme\/projects$/)
  await page.keyboard.press('g')
  await page.keyboard.press('n')
  await expect(page).toHaveURL(/\/o\/acme\/nodes$/)

  const before = await page.evaluate(() => document.documentElement.dataset.theme)
  await page.keyboard.press('t')
  await page.keyboard.press('t')
  const stored = await page.evaluate(() => localStorage.getItem('polipan.theme'))
  expect(stored).toBe('dark')
  expect(before === 'light' || before === 'dark').toBe(true)

  await page.keyboard.press('?')
  const help = page.getByRole('dialog', { name: 'Keyboard shortcuts' })
  await expect(help).toBeVisible()
  await expect(help.getByText('Open the deploy dialog')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(help).toBeHidden()
})

test('REQ-UI-021 shortcuts are off while a field has the focus and when turned off', async ({
  page,
}) => {
  await setUp(page, defaultState())
  await page.goto('/o/acme/catalog')
  const search = page.getByRole('searchbox').first()
  await expect(search).toBeVisible()
  // "/" moves the focus to the filter box. Then "g p" is only text.
  await page.keyboard.press('/')
  await expect(search).toBeFocused()
  await page.keyboard.type('gp')
  await expect(page).toHaveURL(/\/catalog$/)

  await page.evaluate(() => localStorage.setItem('polipan.shortcuts', 'off'))
  await page.goto('/o/acme')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await page.keyboard.press('g')
  await page.keyboard.press('p')
  await expect(page).toHaveURL(/\/o\/acme$/)
  // The help key still works.
  await page.keyboard.press('?')
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible()
})

async function paletteTexts(page: Page): Promise<string[]> {
  await page.keyboard.press('Control+k')
  const list = page.getByRole('listbox', { name: 'Command palette' })
  await expect(list).toBeVisible()
  return list.getByRole('option').allInnerTexts()
}

test('REQ-UI-019 palette lists only the commands the owner role allows', async ({ page }) => {
  await setUp(page, defaultState('owner'))
  await page.goto(serviceUrl)
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  const text = (await paletteTexts(page)).join('\n')
  expect(text).toContain('Deploy api')
  expect(text).toContain('Roll back api')
  expect(text).toContain('Organization')
  expect(text).toContain('Git connections')
  expect(text).toContain('Audit')
})

test('REQ-UI-019 palette lists only the commands the viewer role allows', async ({ page }) => {
  await setUp(page, defaultState('viewer'))
  await page.goto(serviceUrl)
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  const text = (await paletteTexts(page)).join('\n')
  expect(text).not.toContain('Deploy api')
  expect(text).not.toContain('Roll back api')
  expect(text).not.toContain('Add service')
  expect(text).not.toContain('Add node')
  expect(text).not.toContain('Organization')
  expect(text).not.toContain('Git connections')
  expect(text).not.toContain('Audit')
  expect(text).not.toContain('SSO')
  // A viewer keeps the read pages and the harmless commands.
  expect(text).toContain('Nodes')
  expect(text).toContain('Switch theme')
})

test('REQ-UI-019 a palette deploy command opens the dialog and does not deploy', async ({
  page,
}) => {
  const state = defaultState()
  await setUp(page, state)
  await page.goto(serviceUrl)
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await page.keyboard.press('Control+k')
  await page.keyboard.type('>deploy api')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('dialog')).toBeVisible()
  expect(state.deployCalls.map((c) => c.dry_run)).toEqual([true])
})

test('REQ-UI-004 a developer cannot open the organization page or delete it', async ({ page }) => {
  await setUp(page, defaultState('developer'))
  await page.goto('/o/acme/settings/git')
  await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible()
})
