// SPDX-License-Identifier: Apache-2.0

import createClient from 'openapi-fetch'
import type { Middleware } from 'openapi-fetch'
import type { paths } from '@api/schema'
import { ApiError } from '@/api/errors'
import { queryClient } from '@/queryClient'

// The only way the web app talks to the server: the public API (REQ-CORE-009).
// Types are generated from api/openapi.yaml into api/gen/schema.d.ts.
export const api = createClient<paths>({ baseUrl: '/api/v1' })

// The query key of the flag "the org needs 2FA from this user".
export const TWO_FACTOR_KEY = ['two-factor-required']

// The CSRF token comes from GET /me. Every state-changing request sends it back.
let csrfToken = ''

export function setCsrfToken(token: string): void {
  csrfToken = token
}

// POST with a YAML body (custom templates, Compose import). openapi-fetch sends JSON only.
export async function postYaml<T>(path: string, text: string): Promise<T> {
  let res: Response
  try {
    res = await fetch('/api/v1' + path, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/yaml', 'X-CSRF-Token': csrfToken },
      body: text,
    })
  } catch {
    throw new ApiError(0, 'network', 'The server did not answer.')
  }
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as {
      code?: string
      message?: string
      request_id?: string
      details?: Record<string, unknown>
    }
    throw new ApiError(
      res.status,
      body.code ?? 'unknown',
      body.message ?? res.statusText,
      undefined,
      body.request_id,
      body.details,
    )
  }
  return (await res.json()) as T
}

// GET that answers text (the YAML export).
export async function getText(path: string): Promise<string> {
  let res: Response
  try {
    res = await fetch('/api/v1' + path, { credentials: 'same-origin' })
  } catch {
    throw new ApiError(0, 'network', 'The server did not answer.')
  }
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as {
      code?: string
      message?: string
      request_id?: string
    }
    throw new ApiError(
      res.status,
      body.code ?? 'unknown',
      body.message ?? res.statusText,
      undefined,
      body.request_id,
    )
  }
  return res.text()
}

const changesState = new Set(['POST', 'PATCH', 'PUT', 'DELETE'])

// A 401 on these paths is a normal answer (wrong password, setup closed), not an ended session.
function isAnonymousCall(url: string): boolean {
  const path = new URL(url).pathname
  return path.endsWith('/auth/login') || path.endsWith('/setup/first-user')
}

const session: Middleware = {
  onRequest({ request }) {
    if (csrfToken && changesState.has(request.method)) {
      request.headers.set('X-CSRF-Token', csrfToken)
    }
    return request
  },
  onResponse({ request, response }) {
    // The session ended: the shell sees "no user" and goes to the login page.
    if (response.status === 401 && !isAnonymousCall(request.url)) {
      queryClient.setQueryData(['me'], null)
    }
    // The org needs 2FA and the user has none: the shell sends the user to the 2FA setup.
    if (response.status === 403) {
      void response
        .clone()
        .json()
        .then((body: { details?: { reason?: string } }) => {
          if (body.details?.reason === 'two_factor_required') {
            queryClient.setQueryData(TWO_FACTOR_KEY, true)
          }
        })
        .catch(() => undefined)
    }
    return response
  },
}

api.use(session)
