// SPDX-License-Identifier: Apache-2.0

export interface LogsSearch {
  q?: string
  project?: string
  env?: string
  service?: string
  release?: string
  node?: string
  level?: string
  stream?: string
  range?: string
  from?: string
  to?: string
  context?: string
}

const RANGES = ['15m', '1h', '6h', '24h', '7d', '30d', 'custom']

// The filters stay in the URL, so a link restores the search (UI-37, save the link).
export function validateLogsSearch(search: Record<string, unknown>): LogsSearch {
  const out: LogsSearch = {}
  for (const key of [
    'q',
    'project',
    'env',
    'service',
    'release',
    'node',
    'level',
    'stream',
    'from',
    'to',
    'context',
  ] as const) {
    const value = search[key]
    if (typeof value === 'string' && value) out[key] = value
    else if (typeof value === 'number') out[key] = String(value)
  }
  if (typeof search.range === 'string' && RANGES.includes(search.range)) out.range = search.range
  return out
}
