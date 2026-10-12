// SPDX-License-Identifier: Apache-2.0

import { MutationCache, QueryClient } from '@tanstack/react-query'
import type { MessageKey } from '@/i18n'

// Do not retry answers of the server that are final (4xx). Retry once for network or 5xx errors.
function shouldRetry(count: number, error: unknown): boolean {
  const status = (error as { status?: number }).status
  if (typeof status === 'number' && status >= 400 && status < 500) return false
  return count < 1
}

interface MutationMeta {
  // The text of the message after a success. Default: "Done".
  success?: MessageKey
  // No message after a success (the page changes by itself, for example after a login).
  quiet?: boolean
}

declare module '@tanstack/react-query' {
  interface Register {
    mutationMeta: MutationMeta
  }
}

// Set once at the start of the app (src/mutationNotices.ts). This file has no text of its own.
export const mutationHooks: {
  success?: (meta: MutationMeta | undefined) => void
  error?: (error: unknown) => void
} = {}

// Every action (a mutation) says how it ended, in a message that is always in view.
const mutationCache = new MutationCache({
  onSuccess: (_data, _variables, _context, mutation) => mutationHooks.success?.(mutation.meta),
  onError: (error) => mutationHooks.error?.(error),
})

export const queryClient = new QueryClient({
  mutationCache,
  defaultOptions: { queries: { retry: shouldRetry, refetchOnWindowFocus: false } },
})
