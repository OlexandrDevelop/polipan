// SPDX-License-Identifier: Apache-2.0

import { QueryClient } from '@tanstack/react-query'

// Do not retry answers of the server that are final (4xx). Retry once for network or 5xx errors.
function shouldRetry(count: number, error: unknown): boolean {
  const status = (error as { status?: number }).status
  if (typeof status === 'number' && status >= 400 && status < 500) return false
  return count < 1
}

export const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: shouldRetry, refetchOnWindowFocus: false } },
})
