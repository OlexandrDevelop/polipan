// SPDX-License-Identifier: Apache-2.0

import { useInfiniteQuery } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import type { components, paths } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { openStream } from '@/live'
import { lineKey, MAX_LINES } from '@/logText'
import type { LogEntry } from '@/logText'

export type LogLine = components['schemas']['LogLine']
export type SearchParams = NonNullable<paths['/logs/search']['get']['parameters']['query']>

let counter = 0

export function toEntry(l: LogLine, service?: string): LogEntry {
  counter += 1
  return {
    id: `l${counter}`,
    time: l.time,
    message: l.message ?? '',
    level: l.level,
    stream: l.stream,
    replica: l.replica,
    node: l.node_id,
    service,
    serviceId: l.service_id,
    match: l.match,
  }
}

function cap(lines: LogEntry[]): LogEntry[] {
  return lines.length > MAX_LINES ? lines.slice(lines.length - MAX_LINES) : lines
}

// The live tail (streamServiceLogs): the last 200 lines, then new ones. The browser opens the
// stream again after a break and the server sends the last 200 lines again, so lines that were
// already seen are skipped. A break shows a gap marker. While "frozen" the new lines wait,
// so a screen reader can read a stable list.
export function useLogStream(serviceId: string, frozen: boolean) {
  const [lines, setLines] = useState<LogEntry[]>([])
  const [pending, setPending] = useState(0)
  const [lost, setLost] = useState(false)
  const held = useRef<LogEntry[]>([])
  const frozenRef = useRef(frozen)
  const seen = useRef(new Set<string>())

  useEffect(() => {
    frozenRef.current = frozen
    if (!frozen && held.current.length > 0) {
      const flush = held.current
      held.current = []
      setPending(0)
      setLines((prev) => cap([...prev, ...flush]))
    }
  }, [frozen])

  useEffect(() => {
    setLines([])
    setPending(0)
    held.current = []
    seen.current = new Set()
    let lostAt = 0
    // Lines are collected and drawn once per frame. 1 000 lines per second are about 16 lines
    // per frame, so the list changes a little each frame and never in one big step (quality,
    // section 3).
    let incoming: LogEntry[] = []
    let shownPending = 0
    let frame = 0
    const flush = () => {
      frame = 0
      if (incoming.length > 0) {
        const batch = incoming
        incoming = []
        setLines((prev) => cap([...prev, ...batch]))
      }
      if (held.current.length !== shownPending) {
        shownPending = held.current.length
        setPending(shownPending)
      }
    }
    const schedule = () => {
      if (frame === 0) frame = requestAnimationFrame(flush)
    }
    const push = (entry: LogEntry) => {
      if (frozenRef.current) held.current.push(entry)
      else incoming.push(entry)
      schedule()
    }
    const closeStream = openStream(
      `/api/v1/services/${encodeURIComponent(serviceId)}/logs/stream`,
      ['log', 'gap'],
      {
        onEvent: (message) => {
          let data: LogLine & { seconds?: number }
          try {
            data = JSON.parse(message.data) as LogLine & { seconds?: number }
          } catch {
            return
          }
          if (message.type === 'gap') {
            counter += 1
            push({ id: `g${counter}`, message: '', gap: Math.round(data.seconds ?? 0) })
            return
          }
          const key = lineKey(data)
          if (seen.current.has(key)) return
          if (seen.current.size > 30_000) seen.current.clear()
          seen.current.add(key)
          push(toEntry(data))
        },
        onReconnect: () => {
          counter += 1
          const seconds = Math.max(1, Math.round((Date.now() - lostAt) / 1000))
          push({ id: `g${counter}`, message: '', gap: seconds })
        },
        onLost: (isLost) => {
          if (isLost) lostAt = Date.now() - 5_000
          setLost(isLost)
        },
      },
    )
    return () => {
      cancelAnimationFrame(frame)
      closeStream()
    }
  }, [serviceId])

  return { lines, setLines, pending, lost }
}

// The search (searchLogs): newest first, more results with the cursor.
export function useLogSearch(params: SearchParams | null) {
  return useInfiniteQuery({
    queryKey: ['log-search', params],
    enabled: params !== null,
    retry: false,
    initialPageParam: '',
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/logs/search', {
          params: { query: { ...params, limit: 100, ...(pageParam ? { cursor: pageParam } : {}) } },
        }),
      ),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  })
}

export const LEVELS = ['debug', 'info', 'warn', 'error', 'unknown'] as const

const PRESETS = [
  { id: '15m', seconds: 900 },
  { id: '1h', seconds: 3600 },
  { id: '6h', seconds: 6 * 3600 },
  { id: '24h', seconds: 86400 },
  { id: '7d', seconds: 7 * 86400 },
  { id: '30d', seconds: 30 * 86400 },
] as const
export type LogRange = (typeof PRESETS)[number]['id'] | 'custom'
export const LOG_RANGES = PRESETS

export function rangeWindow(
  range: LogRange,
  customFrom: string,
  customTo: string,
): { from: string; to: string } {
  if (range === 'custom') {
    return {
      from: customFrom ? new Date(customFrom).toISOString() : '',
      to: customTo ? new Date(customTo).toISOString() : '',
    }
  }
  const seconds = PRESETS.find((p) => p.id === range)?.seconds ?? 3600
  const now = Date.now()
  return { from: new Date(now - seconds * 1000).toISOString(), to: new Date(now).toISOString() }
}
