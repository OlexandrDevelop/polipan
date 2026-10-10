// SPDX-License-Identifier: Apache-2.0

import createClient from 'openapi-fetch'
import type { paths } from '@api/schema'

// The only way the web app talks to the server: the public API (REQ-CORE-009).
// Types are generated from api/openapi.yaml into api/gen/schema.d.ts.
export const api = createClient<paths>({ baseUrl: '/api/v1' })
