// SPDX-License-Identifier: Apache-2.0

// Helpers for variables (REQ-ENV-002, REQ-ENV-006, REQ-ENV-010) and commands.

export interface EnvItem {
  name: string
  value: string
}

const NAME = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/

// The error key of a variable name, or null when the name is good.
export function nameProblem(name: string): 'invalid' | 'reserved' | null {
  if (!NAME.test(name)) return 'invalid'
  if (name.toUpperCase().startsWith('POLIPAN_')) return 'reserved'
  return null
}

// Names that often hold a secret. The import marks them as secret first; the user can change it.
export function looksSecret(name: string): boolean {
  return /SECRET|TOKEN|PASSWORD|PASSWD|PRIVATE|API_KEY|_KEY$|DSN|CREDENTIAL/i.test(name)
}

function unquote(raw: string): string {
  const v = raw.trim()
  if (v.length >= 2 && v.startsWith('"') && v.endsWith('"')) {
    return v.slice(1, -1).replace(/\\n/g, '\n').replace(/\\"/g, '"')
  }
  if (v.length >= 2 && v.startsWith("'") && v.endsWith("'")) return v.slice(1, -1)
  // An unquoted value ends at " #" (a comment).
  const hash = v.search(/\s#/)
  return hash >= 0 ? v.slice(0, hash).trimEnd() : v
}

// Reads .env text (KEY=value lines, "export" prefix, quotes, # comments) or a JSON object.
// Returns null when the text is neither.
export function parseEnvText(text: string): EnvItem[] | null {
  const trimmed = text.trim()
  if (trimmed === '') return []
  if (trimmed.startsWith('{')) {
    try {
      const data = JSON.parse(trimmed) as unknown
      if (!data || typeof data !== 'object' || Array.isArray(data)) return null
      return Object.entries(data as Record<string, unknown>).map(([name, value]) => ({
        name,
        value: typeof value === 'string' ? value : JSON.stringify(value),
      }))
    } catch {
      return null
    }
  }
  const items: EnvItem[] = []
  for (const line of trimmed.split(/\r?\n/)) {
    const clean = line.trim()
    if (clean === '' || clean.startsWith('#')) continue
    const body = clean.replace(/^export\s+/, '')
    const eq = body.indexOf('=')
    if (eq <= 0) return null
    items.push({ name: body.slice(0, eq).trim(), value: unquote(body.slice(eq + 1)) })
  }
  return items
}

// Splits a command line into words. Single and double quotes keep spaces.
export function splitCommand(text: string): string[] {
  const words: string[] = []
  let word = ''
  let quote: string | null = null
  let started = false
  for (const ch of text) {
    if (quote) {
      if (ch === quote) quote = null
      else word += ch
    } else if (ch === '"' || ch === "'") {
      quote = ch
      started = true
    } else if (/\s/.test(ch)) {
      if (started) words.push(word)
      word = ''
      started = false
    } else {
      word += ch
      started = true
    }
  }
  if (started) words.push(word)
  return words
}

// The reverse of splitCommand, for showing a stored command in a text field.
export function joinCommand(words: unknown): string {
  if (!Array.isArray(words)) return ''
  return words
    .map((w) => String(w))
    .map((w) => (w === '' || /[\s'"]/.test(w) ? `"${w.replace(/"/g, '')}"` : w))
    .join(' ')
}

const byteUnits = ['byte', 'kilobyte', 'megabyte', 'gigabyte', 'terabyte']

// Bytes as a short text with the unit, for example "1.5 GB" (Intl, so a language can change it).
export function formatBytes(bytes: number | undefined): string {
  if (bytes === undefined || bytes === null || Number.isNaN(bytes)) return '-'
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < byteUnits.length - 1) {
    value /= 1024
    unit++
  }
  return new Intl.NumberFormat(undefined, {
    style: 'unit',
    unit: byteUnits[unit],
    unitDisplay: 'short',
    maximumFractionDigits: 1,
  }).format(value)
}
