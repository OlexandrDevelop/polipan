// SPDX-License-Identifier: Apache-2.0

import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The build goes into the Go package that embeds it in polipan-server.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
      '@api': fileURLToPath(new URL('../api/gen', import.meta.url)),
    },
  },
  build: {
    outDir: '../server/internal/web/dist',
    emptyOutDir: false,
    sourcemap: false,
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/healthz': 'http://localhost:8080',
    },
  },
})
