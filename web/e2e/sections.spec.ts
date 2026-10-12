// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState, mockApi } from './mockApi'

const serviceUrl = '/o/acme/p/shop/e/staging/s/api'

// A page with many settings shows one section at a time. The menu changes the section: it does not
// scroll the page, and a section fades in.
test('the menu of the settings opens one section, does not scroll, and animates', async ({
  page,
}) => {
  await mockApi(page, defaultState())
  await page.setViewportSize({ width: 1280, height: 700 })
  await page.goto(`${serviceUrl}/settings`)

  const source = page.getByRole('tabpanel', { name: 'Source' })
  await expect(source).toBeVisible()
  // Only the open section is on the screen.
  await expect(page.getByLabel('Port')).toBeHidden()
  await expect(page.getByLabel('CPU limit (optional)')).toBeHidden()

  // The image is changed in the first section.
  const image = page.getByLabel('Image')
  await image.fill('nginx:1.28')

  const scrollBefore = await page.evaluate(() => window.scrollY)
  await page.getByRole('tab', { name: 'Run', exact: true }).click()
  const run = page.getByRole('tabpanel', { name: 'Run' })
  await expect(run).toBeVisible()
  await expect(source).toBeHidden()
  await expect(page.getByLabel('Port')).toBeVisible()
  expect(await page.evaluate(() => window.scrollY)).toBe(scrollBefore)
  // The section appears with an animation (a fade), not at once.
  expect(await run.evaluate((el) => getComputedStyle(el).animationName)).toBe('enter-up')

  await page.getByRole('tab', { name: 'Replicas and resources' }).click()
  await expect(page.getByLabel('CPU limit (optional)')).toBeVisible()
  await expect(page.getByLabel('Port')).toBeHidden()

  // What was typed in another section is kept.
  await page.getByRole('tab', { name: 'Source', exact: true }).click()
  await expect(image).toHaveValue('nginx:1.28')
  await expect(page.getByText('Unsaved changes')).toBeVisible()

  // The arrow keys move between the sections.
  await page.getByRole('tab', { name: 'Source', exact: true }).focus()
  await page.keyboard.press('ArrowDown')
  await expect(page.getByRole('tab', { name: 'Run', exact: true })).toHaveAttribute(
    'aria-selected',
    'true',
  )
})

test('a wrong value in a hidden section opens that section when you save', async ({ page }) => {
  await mockApi(page, defaultState())
  await page.goto(`${serviceUrl}/settings`)
  await page.getByRole('tab', { name: 'Run', exact: true }).click()
  await page.getByLabel('Port').fill('99999')
  await page.getByRole('tab', { name: 'Source', exact: true }).click()
  await page.getByRole('button', { name: 'Save', exact: true }).first().click()
  await expect(page.getByRole('tabpanel', { name: 'Run' })).toBeVisible()
  await expect(page.getByText('Use a whole number from 1 to 65535.')).toBeVisible()
})

test('a new page and a new tab fade in', async ({ page }) => {
  await mockApi(page, defaultState())
  await page.goto('/o/acme')
  const wrapper = page.locator('main > div > .enter').first()
  await expect(wrapper).toBeVisible()
  expect(await wrapper.evaluate((el) => getComputedStyle(el).animationName)).toBe('enter-up')
  // A click in the menu opens a page that fades in again.
  await page.getByRole('link', { name: 'Projects', exact: true }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Projects' })).toBeVisible()
  expect(
    await page
      .locator('main > div > .enter')
      .first()
      .evaluate((el) => getComputedStyle(el).animationName),
  ).toBe('enter-up')
  // The tabs of a service.
  await page.goto(serviceUrl)
  await page.getByRole('link', { name: 'Domains' }).click()
  await expect(page.getByRole('link', { name: 'Domains' })).toHaveAttribute('aria-current', 'page')
  const tabContent = page.locator('main .enter').last()
  expect(await tabContent.evaluate((el) => getComputedStyle(el).animationName)).toBe('enter-up')
})

test('Calm mode turns the section animation off', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('polipan.calm', 'on'))
  await mockApi(page, defaultState())
  await page.goto(`${serviceUrl}/settings`)
  await page.getByRole('tab', { name: 'Run', exact: true }).click()
  const run = page.getByRole('tabpanel', { name: 'Run' })
  await expect(run).toBeVisible()
  expect(await run.evaluate((el) => getComputedStyle(el).animationName)).toBe('none')
})

test('the profile and the organization have sections', async ({ page }) => {
  await mockApi(page, defaultState())
  await page.goto('/o/acme/settings/profile')
  await expect(page.getByRole('tab', { name: 'Account' })).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByLabel('Current password')).toBeHidden()
  await page.getByRole('tab', { name: 'Security' }).click()
  await expect(page.getByLabel('Current password')).toBeVisible()
  await page.goto('/o/acme/settings/organization')
  await expect(page.getByLabel('Metrics (days)')).toBeHidden()
  await page.getByRole('tab', { name: 'Data retention' }).click()
  await expect(page.getByLabel('Metrics (days)')).toBeVisible()
})
