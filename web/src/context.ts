// SPDX-License-Identifier: Apache-2.0

// The last project and environment the user chose (REQ-UI-020). Kept on the device per user,
// because the API has no preference field (screens, known gaps).
export interface LastContext {
  project: string
  env: string
}

function key(userId: string | undefined): string {
  return `polipan.context.${userId ?? 'anon'}`
}

export function readContext(userId: string | undefined): LastContext | null {
  try {
    const raw = localStorage.getItem(key(userId))
    const v = raw ? (JSON.parse(raw) as Partial<LastContext>) : null
    return v && typeof v.project === 'string' && typeof v.env === 'string'
      ? { project: v.project, env: v.env }
      : null
  } catch {
    return null
  }
}

export function saveContext(userId: string | undefined, value: LastContext): void {
  try {
    localStorage.setItem(key(userId), JSON.stringify(value))
  } catch {
    // Storage may be off (private mode). The switcher still works without memory.
  }
}

const RECENT_KEY = 'polipan.palette.recent'

// Ids of the palette items used last, newest first.
export function readRecent(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(RECENT_KEY) ?? '[]') as unknown
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return []
  }
}

export function pushRecent(id: string): void {
  try {
    const next = [id, ...readRecent().filter((x) => x !== id)].slice(0, 5)
    localStorage.setItem(RECENT_KEY, JSON.stringify(next))
  } catch {
    // Storage may be off.
  }
}
