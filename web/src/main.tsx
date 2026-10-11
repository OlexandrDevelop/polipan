// SPDX-License-Identifier: Apache-2.0

import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { initLanguage } from '@/i18n'
import { LanguageProvider } from '@/language'
import { queryClient } from '@/queryClient'
import { router } from '@/router'
import { ThemeProvider } from '@/theme'
import './index.css'

const root = document.getElementById('root')
if (!root) throw new Error('root element not found')

const mount = root

// The language file loads first, so the first screen is already in the right language.
void initLanguage().then(() => {
  createRoot(mount).render(
    <StrictMode>
      <LanguageProvider>
        <ThemeProvider>
          <QueryClientProvider client={queryClient}>
            <RouterProvider router={router} />
          </QueryClientProvider>
        </ThemeProvider>
      </LanguageProvider>
    </StrictMode>,
  )
})
