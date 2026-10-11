// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { defaultState } from './mockApi'
import { setUp } from './quality'

const CODES = ['uk', 'it', 'pl', 'de', 'fr', 'es', 'nl', 'nb', 'cs', 'el', 'tr']
const text = (code: string, key: string) =>
  JSON.parse(readFileSync(`src/locales/${code}/translation.json`, 'utf8'))[key] as string

test('the language list switches the whole screen and is remembered', async ({ page }) => {
  await setUp(page, defaultState())
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.addInitScript(() => sessionStorage.setItem('polipan.boot', '1'))
  await page.goto('/')
  const footer = page.locator('footer')
  await expect(footer).toBeVisible()
  await expect(page.locator('html')).toHaveAttribute('lang', 'en')
  for (const code of CODES) {
    await footer.locator('select').selectOption(code)
    await expect(page.locator('html')).toHaveAttribute('lang', code)
    await expect(page.locator('footer')).toHaveAttribute(
      'aria-label',
      text(code, 'statusbar.label'),
    )
  }
  // The last choice stays after a reload.
  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('lang', 'tr')
  await expect(page.locator('footer')).toHaveAttribute('aria-label', text('tr', 'statusbar.label'))
})

test('the browser language picks the first screen language', async ({ browser }) => {
  const context = await browser.newContext({ locale: 'nb-NO' })
  const page = await context.newPage()
  await setUp(page, defaultState())
  await page.goto('/')
  await expect(page.locator('html')).toHaveAttribute('lang', 'nb')
  await context.close()
})
