// SPDX-License-Identifier: Apache-2.0

import { defineConfig, devices } from '@playwright/test'

// UI tests run against the Vite dev server. The API is mocked in the browser (e2e/mockApi.ts),
// so no Polipan server is needed. PW_CHANNEL=chrome uses an installed Google Chrome instead of
// the Playwright browser download.
const port = 5179
// The built app (npm run build:check) is served by "vite preview" with the CSP header of the
// Go server. Only the e2e/built-*.spec.ts files run against it.
const builtPort = 5180

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
      testIgnore: /built-.*\.spec\.ts/,
      use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } },
    },
    {
      name: 'built',
      testMatch: /built-.*\.spec\.ts/,
      fullyParallel: false,
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 1280, height: 800 },
        baseURL: `http://localhost:${builtPort}`,
      },
    },
  ],
  webServer: [
    {
      command: `npx vite --port ${port} --strictPort`,
      url: `http://localhost:${port}`,
      reuseExistingServer: !process.env.CI,
    },
    {
      command: `npx vite preview --outDir dist-check --port ${builtPort} --strictPort`,
      url: `http://localhost:${builtPort}`,
      reuseExistingServer: !process.env.CI,
    },
  ],
})
