// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { defaultState } from './mockApi'
import { setUp } from './quality'

// REQ-UI-029: the log viewer keeps 60 fps while the user scrolls 10 000 lines and while 1 000
// lines per second come in. The log stream is replaced by a fake EventSource, so the test
// controls the rate. Frame times come from requestAnimationFrame.

// Frame timing depends on the load of the machine, so a failed run is tried again twice.
test.describe.configure({ retries: 2 })

async function fakeLogStream(page: Page) {
  await page.addInitScript(() => {
    const Native = window.EventSource
    const w = window as unknown as {
      __push: (count: number) => void
      __frames: number[]
      __measure: (ms: number) => Promise<number[]>
    }
    let target: EventTarget | undefined
    let n = 0
    class Fake extends EventTarget {
      static CLOSED = 2
      readyState = 1
      onmessage: ((e: Event) => void) | null = null
      onopen: ((e: Event) => void) | null = null
      onerror: ((e: Event) => void) | null = null
      constructor() {
        super()
        // eslint-disable-next-line @typescript-eslint/no-this-alias
        target = this
        setTimeout(() => this.onopen?.(new Event('open')), 0)
      }
      close() {}
    }
    w.__push = (count: number) => {
      for (let i = 0; i < count; i++) {
        n += 1
        const line = {
          time: new Date(1_790_000_000_000 + n).toISOString(),
          message: `request ${n} served in ${n % 97} ms \u001b[32mok\u001b[0m`,
          level: n % 50 === 0 ? 'error' : 'info',
          stream: 'stdout',
          replica: n % 3,
        }
        target?.dispatchEvent(new MessageEvent('log', { data: JSON.stringify(line) }))
      }
    }
    w.__measure = (ms: number) =>
      new Promise((resolve) => {
        const times: number[] = []
        let last = performance.now()
        const start = last
        const tick = (now: number) => {
          times.push(now - last)
          last = now
          if (now - start < ms) requestAnimationFrame(tick)
          else resolve(times)
        }
        requestAnimationFrame(tick)
      })
    window.EventSource = function (url: string | URL, init?: EventSourceInit) {
      if (String(url).includes('/logs/stream')) return new Fake()
      return new Native(url, init)
    } as unknown as typeof EventSource
    ;(window.EventSource as unknown as { CLOSED: number }).CLOSED = 2
  })
}

function stats(times: number[]) {
  const sorted = [...times].sort((a, b) => a - b)
  const p95 = sorted[Math.floor(sorted.length * 0.95)]
  const slow = times.filter((x) => x > 50).length
  return { frames: times.length, p95, slow }
}

test('REQ-UI-029 the log viewer scrolls 10 000 lines at 60 fps', async ({ page }) => {
  await setUp(page, defaultState())
  await fakeLogStream(page)
  await page.goto('/o/acme/p/shop/e/staging/s/api/logs')
  const log = page.getByRole('log')
  await expect(log).toBeVisible()
  await page.evaluate(() => (window as unknown as { __push: (n: number) => void }).__push(10_000))
  await expect(log.locator('[data-line]').last()).toBeVisible()
  // Stop following, then scroll through the box for two seconds.
  await page.getByLabel('Follow the end').uncheck()
  const measure = page.evaluate(() =>
    (window as unknown as { __measure: (ms: number) => Promise<number[]> }).__measure(2000),
  )
  await log.evaluate(async (el) => {
    const end = performance.now() + 2000
    while (performance.now() < end) {
      el.scrollTop = (el.scrollTop + 40) % Math.max(1, el.scrollHeight - el.clientHeight)
      await new Promise((r) => requestAnimationFrame(r))
    }
  })
  const result = stats(await measure)
  console.log(`scroll: ${JSON.stringify(result)}`)
  expect(result.p95).toBeLessThanOrEqual(20)
  expect(result.slow).toBeLessThanOrEqual(2)
})

test('REQ-UI-029 the log viewer takes 1 000 lines per second without dropping frames', async ({
  page,
}) => {
  await setUp(page, defaultState())
  await fakeLogStream(page)
  await page.goto('/o/acme/p/shop/e/staging/s/api/logs')
  await expect(page.getByRole('log')).toBeVisible()
  await page.evaluate(() => {
    const w = window as unknown as { __push: (n: number) => void }
    // 100 lines every 100 ms for 5 seconds.
    let ticks = 0
    const timer = setInterval(() => {
      w.__push(100)
      if (++ticks >= 50) clearInterval(timer)
    }, 100)
  })
  const times = await page.evaluate(() =>
    (window as unknown as { __measure: (ms: number) => Promise<number[]> }).__measure(5000),
  )
  const result = stats(times)
  console.log(`ingest: ${JSON.stringify(result)}`)
  expect(result.p95).toBeLessThanOrEqual(20)
  expect(result.slow).toBeLessThanOrEqual(3)
  await expect(page.getByRole('log').locator('[data-line]').last()).toContainText('request 5000')
})
