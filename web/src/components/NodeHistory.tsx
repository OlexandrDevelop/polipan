// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState } from 'react'
import type { PointerEvent } from 'react'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { currentLocale, t } from '@/i18n'
import { autoStep } from '@/metrics'
import type { Node } from '@/scope'

const DAY = 24 * 3600
const H = 150
const PAD = { left: 42, right: 6, top: 8, bottom: 20 }

type Point = [number, number]
interface Line {
  id: 'cpu' | 'memory' | 'disk'
  label: string
  color: string
  points: Point[]
}

function percentOf(used?: number, total?: number): number | undefined {
  return used !== undefined && total ? Math.round((used / total) * 100) : undefined
}

// The load of one node in the last 24 hours: CPU, memory and disk as a percent of the node. The
// values are scaled by the capacity of the node, so the three lines share one axis.
export function NodeHistory({ node }: { node: Node }) {
  const plot = useRef<HTMLDivElement>(null)
  const [W, setW] = useState(720)
  const [hidden, setHidden] = useState<Set<string>>(new Set())
  const [cursor, setCursor] = useState<number | null>(null)
  useEffect(() => {
    const el = plot.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const watch = new ResizeObserver(([entry]) =>
      setW(Math.max(280, Math.round(entry.contentRect.width))),
    )
    watch.observe(el)
    return () => watch.disconnect()
  })
  const id = node.id ?? ''
  const step = autoStep(DAY)

  const data = useQuery({
    queryKey: ['node-history', id],
    enabled: Boolean(id),
    retry: false,
    refetchInterval: 60_000,
    queryFn: async () => {
      const to = Math.floor(Date.now() / 1000)
      const from = to - DAY
      const result = await unwrap(
        api.GET('/nodes/{node_id}/metrics', {
          params: {
            path: { node_id: id },
            query: {
              metrics: ['cpu', 'memory', 'disk'],
              from: new Date(from * 1000).toISOString(),
              to: new Date(to * 1000).toISOString(),
              step: `${step}s`,
            },
          },
        }),
      )
      return { result, from, to }
    },
  })

  const lines = useMemo<Line[]>(() => {
    const series = data.data?.result.series ?? []
    const pick = (metric: string) =>
      (series.find((s) => s.metric === metric)?.points ?? []).map((p) => [p[0], p[1]] as Point)
    const scale = (points: Point[], total?: number): Point[] =>
      total ? points.map(([time, value]) => [time, (value / total) * 100] as Point) : []
    return [
      {
        id: 'cpu',
        label: t('nodes.cpu'),
        color: 'var(--hist-cpu)',
        points: scale(pick('cpu'), node.capacity?.cpu_millis),
      },
      {
        id: 'memory',
        label: t('nodes.memory'),
        color: 'var(--hist-memory)',
        points: scale(pick('memory'), node.capacity?.memory_bytes),
      },
      {
        id: 'disk',
        label: t('nodes.disk'),
        color: 'var(--hist-disk)',
        points: scale(pick('disk'), node.capacity?.disk_bytes),
      },
    ].filter((l) => l.points.length > 0) as Line[]
  }, [data.data, node.capacity?.cpu_millis, node.capacity?.memory_bytes, node.capacity?.disk_bytes])

  const unavailable =
    data.error instanceof ApiError &&
    (data.error.code === 'monitoring_unavailable' || data.error.status === 503)

  if (data.isPending) return <div className="history history-wait" aria-busy="true" />
  if (data.isError || lines.length === 0) {
    return (
      <p className="history history-empty">
        {unavailable ? t('metrics.unavailable') : t('metrics.no_data')}
      </p>
    )
  }

  const from = data.data.from
  const to = data.data.to
  const span = Math.max(1, to - from)
  const x = (time: number) => PAD.left + ((time - from) / span) * (W - PAD.left - PAD.right)
  const y = (value: number) =>
    PAD.top + (1 - Math.min(100, Math.max(0, value)) / 100) * (H - PAD.top - PAD.bottom)
  const now: Record<string, number | undefined> = {
    cpu: percentOf(node.usage?.cpu_millis, node.capacity?.cpu_millis),
    memory: percentOf(node.usage?.memory_bytes, node.capacity?.memory_bytes),
    disk: percentOf(node.usage?.disk_bytes, node.capacity?.disk_bytes),
  }
  const visible = lines.filter((l) => !hidden.has(l.id))
  const path = (points: Point[]) =>
    points
      .map((p, i) => `${i === 0 ? 'M' : 'L'}${x(p[0]).toFixed(1)} ${y(p[1]).toFixed(1)}`)
      .join('')

  const hours = new Intl.DateTimeFormat(currentLocale(), { hour: '2-digit', minute: '2-digit' })
  const full = new Intl.DateTimeFormat(currentLocale(), {
    day: 'numeric',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
  })
  const xTicks = [0, 1, 2, 3, 4].map((i) => from + (span * i) / 4)
  const times = lines[0].points.map((p) => p[0])
  const at = cursor === null ? undefined : times[cursor]
  const valueAt = (l: Line, time: number) => {
    const p = l.points.find((q) => q[0] === time)
    return p ? p[1] : undefined
  }

  function onMove(event: PointerEvent<SVGSVGElement>) {
    const box = event.currentTarget.getBoundingClientRect()
    const time =
      from +
      ((((event.clientX - box.left) / box.width) * W - PAD.left) / (W - PAD.left - PAD.right)) *
        span
    let best = 0
    for (let i = 1; i < times.length; i++) {
      if (Math.abs(times[i] - time) < Math.abs(times[best] - time)) best = i
    }
    setCursor(best)
  }

  const summary = lines
    .map((l) => `${l.label} ${Math.round(l.points[l.points.length - 1][1])}%`)
    .join(', ')

  return (
    <div className="history">
      <div className="history-top">
        <span className="history-title">{t('metrics.range.24h')}</span>
        <ul className="history-legend">
          {lines.map((l) => (
            <li key={l.id}>
              <button
                type="button"
                aria-pressed={!hidden.has(l.id)}
                onClick={() =>
                  setHidden((prev) => {
                    const next = new Set(prev)
                    if (!next.delete(l.id)) next.add(l.id)
                    return next
                  })
                }
              >
                <span aria-hidden="true" className="history-dot" style={{ background: l.color }} />
                {l.label}
                {now[l.id] !== undefined && <b>{now[l.id]}%</b>}
              </button>
            </li>
          ))}
        </ul>
      </div>
      <div className="history-plot" ref={plot}>
        <svg
          viewBox={`0 0 ${W} ${H}`}
          role="img"
          aria-label={`${t('metrics.range.24h')}: ${summary}`}
          onPointerMove={onMove}
          onPointerLeave={() => setCursor(null)}
        >
          {[0, 25, 50, 75, 100].map((v) => (
            <g key={v}>
              <line className="history-grid" x1={PAD.left} x2={W - PAD.right} y1={y(v)} y2={y(v)} />
              <text className="history-axis" x={PAD.left - 8} y={y(v) + 4} textAnchor="end">
                {v}%
              </text>
            </g>
          ))}
          {xTicks.map((time, i) => (
            <text
              key={time}
              className="history-axis"
              x={x(time)}
              y={H - 6}
              textAnchor={i === 0 ? 'start' : i === 4 ? 'end' : 'middle'}
            >
              {hours.format(new Date(time * 1000))}
            </text>
          ))}
          {visible.map((l) => (
            <g key={l.id}>
              <path className="history-line" d={path(l.points)} stroke={l.color} pathLength={1} />
            </g>
          ))}
          {at !== undefined && (
            <g>
              <line
                className="history-cursor"
                x1={x(at)}
                x2={x(at)}
                y1={PAD.top}
                y2={H - PAD.bottom}
              />
              {visible.map((l) => {
                const v = valueAt(l, at)
                return v === undefined ? null : (
                  <circle
                    key={l.id}
                    cx={x(at)}
                    cy={y(v)}
                    r="4.5"
                    fill={l.color}
                    className="history-point"
                  />
                )
              })}
            </g>
          )}
        </svg>
        {at !== undefined && (
          <div
            className="history-tip"
            style={{ left: `${(x(at) / W) * 100}%` }}
            data-side={x(at) > W * 0.6 ? 'left' : 'right'}
          >
            <b>{full.format(new Date(at * 1000))}</b>
            {visible.map((l) => {
              const v = valueAt(l, at)
              return v === undefined ? null : (
                <span key={l.id}>
                  <i style={{ background: l.color }} />
                  {l.label} {Math.round(v)}%
                </span>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}
