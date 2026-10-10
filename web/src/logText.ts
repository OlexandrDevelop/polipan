// SPDX-License-Identifier: Apache-2.0

// Helpers for log lines. A log line is DATA. These helpers only cut text into parts. The parts
// are drawn as React text, never as HTML (REQ-OBS-101).

export interface LogEntry {
  id: string
  time?: string
  message: string
  level?: string
  stream?: string
  replica?: number
  node?: string
  service?: string
  serviceId?: string
  // false for a context line around a match (search mode).
  match?: boolean
  // A marker line: the stream skipped this many seconds.
  gap?: number
}

// The most lines the viewer keeps in memory (components, LogViewer).
export const MAX_LINES = 10_000

export type AnsiTone = 'danger' | 'success' | 'warning' | 'info' | 'primary' | 'muted'

export interface Part {
  text: string
  tone?: AnsiTone
  bold?: boolean
  // The part is a match of the highlight text.
  mark?: boolean
}

const ESC = String.fromCharCode(27)
const ANSI = new RegExp(`${ESC}\\[([0-9;]*)m`, 'g')
// Any other escape sequence is dropped.
const OTHER_ESC = new RegExp(`${ESC}\\[[0-9;?]*[A-Za-ln-z]`, 'g')

const TONES: Record<number, AnsiTone> = {
  31: 'danger',
  32: 'success',
  33: 'warning',
  34: 'info',
  35: 'primary',
  36: 'info',
  90: 'muted',
  91: 'danger',
  92: 'success',
  93: 'warning',
  94: 'info',
  95: 'primary',
  96: 'info',
}

// Splits text with ANSI colors into parts. Colors are mapped to theme tones.
export function parseAnsi(raw: string): Part[] {
  const text = raw.replace(OTHER_ESC, '')
  const parts: Part[] = []
  let tone: AnsiTone | undefined
  let bold = false
  let last = 0
  for (const m of text.matchAll(ANSI)) {
    if (m.index > last) parts.push({ text: text.slice(last, m.index), tone, bold })
    last = m.index + m[0].length
    for (const code of m[1] === '' ? [0] : m[1].split(';').map(Number)) {
      if (code === 0) {
        tone = undefined
        bold = false
      } else if (code === 1) bold = true
      else if (code === 22) bold = false
      else if (code === 39) tone = undefined
      else if (TONES[code]) tone = TONES[code]
    }
  }
  if (last < text.length) parts.push({ text: text.slice(last), tone, bold })
  return parts
}

// The text without ANSI codes. Used for search, copy and download.
export function plainText(raw: string): string {
  return raw.replace(ANSI, '').replace(OTHER_ESC, '')
}

// Marks the places where the query text is found (not case sensitive).
export function highlight(parts: Part[], query: string): Part[] {
  const needle = query.trim().toLowerCase()
  if (!needle) return parts
  const out: Part[] = []
  for (const part of parts) {
    const lower = part.text.toLowerCase()
    let from = 0
    for (;;) {
      const at = lower.indexOf(needle, from)
      if (at < 0) break
      if (at > from) out.push({ ...part, text: part.text.slice(from, at) })
      out.push({ ...part, text: part.text.slice(at, at + needle.length), mark: true })
      from = at + needle.length
    }
    if (from < part.text.length) out.push({ ...part, text: part.text.slice(from) })
  }
  return out
}

export function hasMatch(entry: LogEntry, query: string): boolean {
  const needle = query.trim().toLowerCase()
  return needle !== '' && plainText(entry.message).toLowerCase().includes(needle)
}

// One text line for copy and download.
export function lineText(entry: LogEntry): string {
  if (entry.gap !== undefined) return `-- gap of ${entry.gap} s --`
  const head = [entry.time, entry.replica !== undefined ? `#${entry.replica}` : '', entry.level]
    .filter(Boolean)
    .join(' ')
  return `${head} ${plainText(entry.message)}`.trim()
}

// Saves text as a file in the browser. Nothing goes to the server.
export function downloadText(name: string, text: string): void {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
  const link = document.createElement('a')
  link.href = url
  link.download = name
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}

// A key to find the same line again (the live stream sends the last 200 lines after a reconnect).
export function lineKey(l: {
  time?: string
  replica?: number
  node_id?: string
  message?: string
}): string {
  return `${l.time ?? ''}|${l.node_id ?? ''}|${l.replica ?? ''}|${l.message ?? ''}`
}
