// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState } from './mockApi'
import { setUp } from './quality'

// REQ-UI-038 and REQ-UI-043: the traffic bar packets, the live dot and the braille spinner run at
// 60 fps on the built app, with at most 12 animations at once. Frame times come from
// requestAnimationFrame. Frame timing depends on the load of the machine, so a failed run is
// tried again twice.
test.describe.configure({ retries: 2 })

test('REQ-UI-038 the traffic packets and the live dot keep 60 fps with at most 12 animations', async ({
  page,
}) => {
  const state = defaultState()
  state.deployments.d1 = {
    id: 'd1',
    service_id: 's1',
    release_id: 'r2',
    trigger: 'manual',
    strategy: 'canary',
    state: 'live',
    steps: [{ state: 'live', time: '2026-10-10T10:00:00Z' }],
    started_at: '2026-10-10T10:00:00Z',
    finished_at: '2026-10-10T10:01:00Z',
  }
  state.services[0].placements = [
    {
      id: 'p1',
      release_id: 'r2',
      weight: 25,
      replica_index: 0,
      role: 'primary',
      health: 'healthy',
    },
    {
      id: 'p2',
      release_id: 'r1',
      weight: 75,
      replica_index: 0,
      role: 'primary',
      health: 'healthy',
    },
  ] as never
  await setUp(page, state)
  await page.addInitScript(() => sessionStorage.setItem('polipan.boot', '1'))
  await page.goto('/o/acme/p/shop/e/staging/s/api/deployments/d1')
  // 25 % new: 2 packets in the new lane and 4 in the old lane.
  await expect(page.locator('.packet')).toHaveCount(6)
  expect(await page.evaluate(() => document.getAnimations().length)).toBeLessThanOrEqual(12)
  const times = await page.evaluate(
    () =>
      new Promise<number[]>((resolve) => {
        const times: number[] = []
        let last = performance.now()
        const start = last
        const tick = (now: number) => {
          times.push(now - last)
          last = now
          if (now - start < 2000) requestAnimationFrame(tick)
          else resolve(times)
        }
        requestAnimationFrame(tick)
      }),
  )
  const sorted = [...times].sort((a, b) => a - b)
  const p95 = sorted[Math.floor(sorted.length * 0.95)]
  const slow = times.filter((x) => x > 50).length
  console.log(`motion: ${JSON.stringify({ frames: times.length, p95, slow })}`)
  expect(p95).toBeLessThanOrEqual(20)
  expect(slow).toBeLessThanOrEqual(2)
})
