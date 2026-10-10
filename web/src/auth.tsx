// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@tanstack/react-query'
import type { components } from '@api/schema'
import { api, setCsrfToken } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'

// GET /me also returns the CSRF token of the session.
export type Me = components['schemas']['Me'] & { csrf_token?: string }

let hadSession = false

// True when a user was signed in before. The login page then says that the session ended.
export function sessionWasActive(): boolean {
  return hadSession
}

// Returns null when nobody is signed in.
export async function fetchMe(): Promise<Me | null> {
  try {
    const result = await api.GET('/me')
    const me = (await unwrap(Promise.resolve(result))) as Me
    // The server sends the token in the header X-CSRF-Token (a field csrf_token is also accepted).
    setCsrfToken(result.response.headers.get('X-CSRF-Token') ?? me.csrf_token ?? '')
    hadSession = true
    return me
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) return null
    throw error
  }
}

export function useMe() {
  return useQuery({ queryKey: ['me'], queryFn: fetchMe, staleTime: 5 * 60_000 })
}
