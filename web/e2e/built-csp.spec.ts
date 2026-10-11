// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState } from './mockApi'
import { openScreen, SCREENS, setUp, THEMES } from './quality'

// These tests run against the built app with the CSP header of the Go server (playwright.config.ts).

test('REQ-UI-015 the built index.html has no inline script and the CSP has no unsafe-inline', async ({
  request,
  baseURL,
}) => {
  const response = await request.get(`${baseURL}/`)
  const csp = response.headers()['content-security-policy'] ?? ''
  expect(csp).toContain("script-src 'self'")
  expect(csp).not.toContain('unsafe-inline')
  expect(csp).not.toContain('unsafe-eval')
  const html = await response.text()
  expect(html).not.toMatch(/<script(?![^>]*\bsrc=)[^>]*>/)
  expect(html).not.toMatch(/\son[a-z]+=/)
})

for (const theme of THEMES) {
  test(`REQ-UI-015 REQ-UI-012 no CSP violation and no third-party host on any screen in ${theme}`, async ({
    page,
  }) => {
    test.setTimeout(240_000)
    const state = defaultState()
    state.deployments.d1 = {
      id: 'd1',
      service_id: 's1',
      release_id: 'r2',
      trigger: 'manual',
      strategy: 'blue_green',
      state: 'live',
      steps: [{ state: 'live', time: '2026-10-10T10:00:00Z' }],
      started_at: '2026-10-10T10:00:00Z',
    }
    await setUp(page, state)
    const hosts = new Set<string>()
    const problems: string[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (!['data:', 'blob:'].includes(url.protocol)) hosts.add(url.host)
    })
    page.on('console', (message) => {
      if (/Content Security Policy|Refused to/.test(message.text())) problems.push(message.text())
    })
    page.on('pageerror', (error) => problems.push(error.message))
    await page.addInitScript(() => {
      document.addEventListener('securitypolicyviolation', (event) => {
        console.error(`Content Security Policy: ${event.violatedDirective} ${event.blockedURI}`)
      })
    })
    for (const screen of SCREENS) {
      await openScreen(page, screen, { width: 1280, theme })
    }
    expect(problems).toEqual([])
    expect([...hosts]).toEqual([new URL(page.url()).host])
  })
}

test('REQ-UI-012 the page has no link, image or form that points to another host', async ({
  page,
}) => {
  await setUp(page, defaultState())
  for (const path of ['/o/acme', '/o/acme/settings/git', '/o/acme/catalog']) {
    await page.goto(path)
    await page.locator('h1').first().waitFor()
    const foreign = await page.evaluate(() =>
      [...document.querySelectorAll<HTMLElement>('[src], [href], [action]')]
        .map(
          (el) =>
            el.getAttribute('src') ?? el.getAttribute('href') ?? el.getAttribute('action') ?? '',
        )
        .filter(
          (u) => /^(https?:)?\/\//.test(u) && new URL(u, location.href).host !== location.host,
        ),
    )
    expect(foreign).toEqual([])
  }
})

test('REQ-UI-015 the CSP check itself sees an inline script', async ({ page }) => {
  await setUp(page, defaultState())
  const seen: string[] = []
  page.on('console', (message) => {
    if (/Content Security Policy|Refused to/.test(message.text())) seen.push(message.text())
  })
  await page.goto('/login')
  await page.locator('h1').first().waitFor()
  await page.evaluate(() => {
    const script = document.createElement('script')
    script.textContent = 'window.__bad = 1'
    document.head.append(script)
  })
  await expect.poll(() => seen.length).toBeGreaterThan(0)
  expect(await page.evaluate(() => (window as unknown as { __bad?: number }).__bad)).toBeUndefined()
})
