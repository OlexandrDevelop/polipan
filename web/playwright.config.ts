// SPDX-License-Identifier: Apache-2.0

import { defineConfig, devices } from '@playwright/test'

// UI tests run against the Vite dev server. The API is mocked in the browser (e2e/mockApi.ts),
// so no Polipan server is needed. PW_CHANNEL=chrome uses an installed Google Chrome instead of
// the Playwright browser download.
const port = 5179

export default defineConfig({
  testDir: 'e2e',
  fullyParallel: true,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? 'line' : 'list',
  use: {
    baseURL: `http://localhost:${port}`,
    channel: process.env.PW_CHANNEL || undefined,
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'laptop',
      use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } },
    },
  ],
  webServer: {
    command: `npx vite --port ${port} --strictPort`,
    url: `http://localhost:${port}`,
    reuseExistingServer: !process.env.CI,
  },
})
