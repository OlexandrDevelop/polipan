// SPDX-License-Identifier: Apache-2.0

import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { initLanguage } from '@/i18n'
import { LanguageProvider } from '@/language'
import { installMutationNotices } from '@/mutationNotices'
import { queryClient } from '@/queryClient'
import { router } from '@/router'
import { ThemeProvider } from '@/theme'
import { ToastRegion } from '@/toast'
import './index.css'

const root = document.getElementById('root')
if (!root) throw new Error('root element not found')

const mount = root
installMutationNotices()

// The language file loads first, so the first screen is already in the right language.
void initLanguage().then(() => {
  createRoot(mount).render(
    <StrictMode>
      <LanguageProvider>
        <ThemeProvider>
          <QueryClientProvider client={queryClient}>
            <RouterProvider router={router} />
            <ToastRegion />
          </QueryClientProvider>
        </ThemeProvider>
      </LanguageProvider>
    </StrictMode>,
  )
})
