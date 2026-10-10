// SPDX-License-Identifier: Apache-2.0

import createClient from 'openapi-fetch'
import type { Middleware } from 'openapi-fetch'
import type { paths } from '@api/schema'
import { queryClient } from '@/queryClient'

// The only way the web app talks to the server: the public API (REQ-CORE-009).
// Types are generated from api/openapi.yaml into api/gen/schema.d.ts.
export const api = createClient<paths>({ baseUrl: '/api/v1' })

// The CSRF token comes from GET /me. Every state-changing request sends it back.
let csrfToken = ''

export function setCsrfToken(token: string): void {
  csrfToken = token
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
    return response
  },
}

api.use(session)
