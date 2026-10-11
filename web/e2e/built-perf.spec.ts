// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { defaultState } from './mockApi'
import { setUp } from './quality'

// REQ-UI-029: the Web Vitals budgets, measured on the built app. The API is mocked in the
// browser, so this measures the app shell and the first render (not the network of the server).
// The numbers are printed, so a person can see how far each is from its budget.

interface Vitals {
  lcp: number
  cls: number
  inp: number
  moved: string[]
}

async function observe(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as {
      __vitals: { lcp: number; cls: number; inp: number; moved: string[] }
    }
    w.__vitals = { lcp: 0, cls: 0, inp: 0, moved: [] }
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) w.__vitals.lcp = entry.startTime
    }).observe({ type: 'largest-contentful-paint', buffered: true })
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries() as unknown as {
        value: number
        hadRecentInput: boolean
        sources?: { node?: Element }[]
      }[]) {
        if (entry.hadRecentInput) continue
        w.__vitals.cls += entry.value
        // Which elements moved: printed when a test fails, so the cause is easy to find.
        for (const source of entry.sources ?? []) {
          const el = source.node as HTMLElement | undefined
          w.__vitals.moved.push(
            `${entry.value.toFixed(3)} ${el?.tagName}.${String(el?.className).slice(0, 60)}`,
          )
        }
      }
    }).observe({ type: 'layout-shift', buffered: true })
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries())
        w.__vitals.inp = Math.max(w.__vitals.inp, entry.duration)
    }).observe({ type: 'event', durationThreshold: 16, buffered: true } as PerformanceObserverInit)
  })
}

// A set-up install: the checklist of UI-31 is done, so the dashboard is the normal one.
async function establishedInstall(page: Page) {
  await page.route('**/api/v1/services/s1/domains', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([{ id: 'dm1', host: 'shop.example.com' }]),
    }),
  )
}

async function vitals(page: Page): Promise<Vitals> {
  return page.evaluate(() => (window as unknown as { __vitals: Vitals }).__vitals)
}

async function interact(page: Page) {
  // A few real interactions for INP: open and close the palette, go to another page.
  await page.keyboard.press('Control+k')
  await page.getByRole('combobox', { name: 'Command palette' }).waitFor()
  await page.keyboard.type('nodes')
  await page.keyboard.press('Escape')
  await page.getByRole('link', { name: 'Projects', exact: true }).first().click()
  await page.getByRole('heading', { level: 1, name: 'Projects' }).waitFor()
  await page.waitForTimeout(300)
}

for (const path of ['/o/acme', '/o/acme/p/shop/e/staging/s/api']) {
  test(`REQ-UI-029 laptop: LCP, CLS and INP of ${path}`, async ({ page }) => {
    await setUp(page, defaultState())
    await establishedInstall(page)
    await observe(page)
    await page.goto(path)
    await page.locator('h1').first().waitFor()
    await page.waitForLoadState('networkidle')
    await page.waitForTimeout(500)
    await interact(page)
    const v = await vitals(page)
    console.log(
      `laptop ${path} LCP ${v.lcp.toFixed(0)} ms, CLS ${v.cls.toFixed(3)}, INP ${v.inp.toFixed(0)} ms`,
    )
    expect(v.lcp).toBeGreaterThan(0)
    expect(v.lcp).toBeLessThanOrEqual(2000)
    expect(v.cls, v.moved.join('\n')).toBeLessThanOrEqual(0.1)
    expect(v.inp).toBeLessThanOrEqual(200)
  })

  test(`REQ-UI-029 phone on 4G: LCP and CLS of ${path}`, async ({ browser }) => {
    const context = await browser.newContext({
      viewport: { width: 360, height: 740 },
      hasTouch: true,
      isMobile: true,
      deviceScaleFactor: 2,
      baseURL: `http://localhost:5180`,
    })
    const page = await context.newPage()
    await setUp(page, defaultState())
    await establishedInstall(page)
    await observe(page)
    // A mid phone: 4x slower CPU and 4G (1.6 Mbit/s down, 150 ms round trip).
    const cdp = await context.newCDPSession(page)
    await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 })
    await cdp.send('Network.emulateNetworkConditions', {
      offline: false,
      latency: 150,
      downloadThroughput: (1.6 * 1024 * 1024) / 8,
      uploadThroughput: (750 * 1024) / 8,
    })
    await page.goto(path)
    await page.locator('h1').first().waitFor()
    await page.waitForLoadState('networkidle')
    await page.waitForTimeout(500)
    const v = await vitals(page)
    console.log(`phone ${path} LCP ${v.lcp.toFixed(0)} ms, CLS ${v.cls.toFixed(3)}`)
    expect(v.lcp).toBeGreaterThan(0)
    expect(v.lcp).toBeLessThanOrEqual(3500)
    expect(v.cls, v.moved.join('\n')).toBeLessThanOrEqual(0.1)
    await context.close()
  })
}

test('REQ-UI-029 one SSE connection per page', async ({ page }) => {
  await setUp(page, defaultState())
  await page.addInitScript(() => {
    const Native = window.EventSource
    const w = window as unknown as { __streams: string[] }
    w.__streams = []
    window.EventSource = class extends Native {
      constructor(url: string | URL, init?: EventSourceInit) {
        super(url, init)
        w.__streams.push(String(url))
      }
    }
  })
  for (const path of [
    '/o/acme',
    '/o/acme/projects',
    '/o/acme/p/shop/e/staging/s/api',
    '/o/acme/nodes',
  ]) {
    await page.goto(path)
    await page.locator('h1').first().waitFor()
    await page.waitForTimeout(500)
    const streams = await page.evaluate(
      () => (window as unknown as { __streams: string[] }).__streams,
    )
    expect(streams, path).toHaveLength(1)
  }
})
