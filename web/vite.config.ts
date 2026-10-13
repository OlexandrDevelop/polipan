// SPDX-License-Identifier: Apache-2.0

import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'
import type { Plugin } from 'vite'

// Preloads the Latin font files, so the text does not wait for them (quality, section 3).
function preloadFonts(): Plugin {
  return {
    name: 'polipan-preload-fonts',
    transformIndexHtml: {
      order: 'post',
      handler(_html, ctx) {
        return Object.keys(ctx.bundle ?? {})
          .filter((file) => /-latin-wght-normal-.*\.woff2$/.test(file))
          .map((file) => ({
            tag: 'link',
            attrs: {
              rel: 'preload',
              as: 'font',
              type: 'font/woff2',
              crossorigin: '',
              href: `/${file}`,
            },
            injectTo: 'head' as const,
          }))
      },
    },
  }
}

// The build goes into the Go package that embeds it in polipan-server.
export default defineConfig({
  plugins: [react(), preloadFonts()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
      '@api': fileURLToPath(new URL('../api/gen', import.meta.url)),
    },
  },
  build: {
    // POLIPAN_WEB_OUT lets the quality checks build into a scratch folder.
    outDir: process.env.POLIPAN_WEB_OUT ?? '../server/internal/web/dist',
    emptyOutDir: false,
    sourcemap: false,
  },
  // The preview server sends the same policy as the Go server (security.go), so the tests see
  // every violation.
  preview: {
    headers: {
      'Content-Security-Policy':
        "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
    },
  },
  server: {
    proxy: {
      '/api': process.env.POLIPAN_API ?? 'http://localhost:8080',
      '/healthz': process.env.POLIPAN_API ?? 'http://localhost:8080',
    },
  },
})
