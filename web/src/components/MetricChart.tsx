// SPDX-License-Identifier: Apache-2.0

import { useId, useMemo, useState } from 'react'
import type { KeyboardEvent, PointerEvent } from 'react'
import { Button } from '@/components/ui'
import { formatValue } from '@/metrics'
import type { ChartSeries, Unit } from '@/metrics'
import { currentLocale, t } from '@/i18n'

const W = 640
const H = 220
const PAD = { left: 56, right: 12, top: 12, bottom: 28 }
const MAX_SERIES = 6

// Colors come from the theme tokens. Each series also has its own line style and marker shape,
// so color is never the only signal (REQ-UI-013).
const COLORS = [
  'var(--color-primary)',
  'var(--color-info)',
  'var(--color-success)',
  'var(--color-warning)',
  'var(--color-danger)',
  'var(--color-text)',
]
const DASHES = ['', '8 4', '2 4', '10 3 2 3', '1 5', '14 4']
type Shape = 'circle' | 'square' | 'triangle' | 'diamond' | 'cross' | 'plus'
const SHAPES: Shape[] = ['circle', 'square', 'triangle', 'diamond', 'cross', 'plus']

export interface ChartMarker {
  // Unix seconds.
  time: number
  label: string
  onOpen?: () => void
}

function Marker({ shape, x, y, color }: { shape: Shape; x: number; y: number; color: string }) {
  const s = 4
  switch (shape) {
    case 'circle':
      return <circle cx={x} cy={y} r={s} fill={color} />
    case 'square':
      return <rect x={x - s} y={y - s} width={2 * s} height={2 * s} fill={color} />
    case 'triangle':
      return (
        <path d={`M${x} ${y - s - 1}L${x + s + 1} ${y + s}L${x - s - 1} ${y + s}Z`} fill={color} />
      )
    case 'diamond':
      return (
        <path
          d={`M${x} ${y - s - 1}L${x + s + 1} ${y}L${x} ${y + s + 1}L${x - s - 1} ${y}Z`}
          fill={color}
        />
      )
    case 'cross':
      return (
        <path
          d={`M${x - s} ${y - s}L${x + s} ${y + s}M${x + s} ${y - s}L${x - s} ${y + s}`}
          stroke={color}
          strokeWidth="2"
        />
      )
    default:
      return (
        <path d={`M${x - s} ${y}H${x + s}M${x} ${y - s}V${y + s}`} stroke={color} strokeWidth="2" />
      )
  }
}

const dayFormat = () =>
  new Intl.DateTimeFormat(currentLocale(), { dateStyle: 'medium', timeStyle: 'short' })
const hourFormat = () =>
  new Intl.DateTimeFormat(currentLocale(), { hour: '2-digit', minute: '2-digit' })
const dateHourFormat = () =>
  new Intl.DateTimeFormat(currentLocale(), { month: 'short', day: 'numeric', hour: '2-digit' })

function fullTime(seconds: number): string {
  return dayFormat().format(new Date(seconds * 1000))
}

// A chart as a small SVG, without a library. It has a legend with toggle buttons, a shared
// crosshair (pointer, touch and arrow keys), release markers and a table view (REQ-UI-026).
export function MetricChart({
  title,
  unit,
  series,
  from,
  to,
  markers = [],
}: {
  title: string
  unit: Unit
  series: ChartSeries[]
  from: number
  to: number
  markers?: ChartMarker[]
}) {
  const id = useId()
  const [hidden, setHidden] = useState<Set<string>>(new Set())
  const [table, setTable] = useState(false)
  const [cursor, setCursor] = useState<number | null>(null)

  const shown = useMemo(() => series.slice(0, MAX_SERIES), [series])
  const visible = shown.filter((s) => !hidden.has(s.id))
  const times = useMemo(() => {
    const all = new Set<number>()
    for (const s of shown) for (const p of s.points) all.add(p[0])
    return [...all].sort((a, b) => a - b)
  }, [shown])
  const hasData = times.length > 0

  const max = Math.max(0, ...visible.flatMap((s) => s.points.map((p) => p[1])))
  const top = max === 0 ? 1 : max * 1.1
  const span = Math.max(1, to - from)
  const x = (time: number) => PAD.left + ((time - from) / span) * (W - PAD.left - PAD.right)
  const y = (value: number) => PAD.top + (1 - value / top) * (H - PAD.top - PAD.bottom)
  const xTicks = [0, 1, 2, 3, 4].map((i) => from + (span * i) / 4)
  const yTicks = [0, 1, 2, 3].map((i) => (top * i) / 3)
  const tickFormat = span > 2 * 86400 ? dateHourFormat : hourFormat

  const valueAt = (s: ChartSeries, time: number) => s.points.find((p) => p[0] === time)?.[1]
  const activeTime = cursor !== null ? times[cursor] : undefined

  function move(index: number) {
    setCursor(Math.min(times.length - 1, Math.max(0, index)))
  }
  function onKey(event: KeyboardEvent) {
    if (!hasData) return
    const at = cursor ?? times.length - 1
    if (event.key === 'ArrowLeft') move(at - 1)
    else if (event.key === 'ArrowRight') move(at + 1)
    else if (event.key === 'Home') move(0)
    else if (event.key === 'End') move(times.length - 1)
    else if (event.key === 'Escape') setCursor(null)
    else return
    event.preventDefault()
  }
  function onPointer(event: PointerEvent<SVGSVGElement>) {
    if (!hasData) return
    const box = event.currentTarget.getBoundingClientRect()
    const px = ((event.clientX - box.left) / box.width) * W
    const time = from + ((px - PAD.left) / (W - PAD.left - PAD.right)) * span
    let best = 0
    for (let i = 1; i < times.length; i++) {
      if (Math.abs(times[i] - time) < Math.abs(times[best] - time)) best = i
    }
    setCursor(best)
  }

  const summary = visible
    .map((s) => {
      const values = s.points.map((p) => p[1])
      if (values.length === 0) return null
      return {
        name: s.name,
        last: formatValue(values[values.length - 1], unit),
        max: formatValue(Math.max(...values), unit),
      }
    })
    .filter((s) => s !== null)

  return (
    <figure className="space-y-2 rounded-md border border-border bg-surface p-3">
      <figcaption className="flex flex-wrap items-center justify-between gap-2">
        <span className="font-semibold">{title}</span>
        {hasData && (
          <Button aria-pressed={table} onClick={() => setTable(!table)}>
            {table ? t('chart.show_chart') : t('chart.show_table')}
          </Button>
        )}
      </figcaption>
      {!hasData ? (
        <p className="py-6 text-center text-muted">{t('chart.empty')}</p>
      ) : (
        <>
          <ul className="space-y-0.5 text-sm text-muted">
            {summary.map((s) => (
              <li key={s.name}>{t('chart.summary', { name: s.name, last: s.last, max: s.max })}</li>
            ))}
          </ul>
          {table ? (
            <div
              tabIndex={0}
              role="region"
              aria-label={t('chart.table_label', { title })}
              className="max-h-72 overflow-auto"
            >
              <table className="w-full border-collapse text-left text-sm">
                <caption className="sr-only">{t('chart.table_label', { title })}</caption>
                <thead className="sticky top-0 bg-surface">
                  <tr>
                    <th scope="col" className="p-2 font-semibold text-muted">
                      {t('chart.time')}
                    </th>
                    {shown.map((s) => (
                      <th key={s.id} scope="col" className="p-2 font-semibold text-muted">
                        {s.name}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {times.map((time) => (
                    <tr key={time} className="border-t border-border">
                      <th scope="row" className="whitespace-nowrap p-2 font-normal">
                        {fullTime(time)}
                      </th>
                      {shown.map((s) => {
                        const v = valueAt(s, time)
                        return (
                          <td key={s.id} className="p-2">
                            {v === undefined ? '-' : formatValue(v, unit)}
                          </td>
                        )
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div
              tabIndex={0}
              role="group"
              aria-label={t('chart.keys', { title })}
              aria-describedby={`${id}-read`}
              onKeyDown={onKey}
              className="rounded-sm"
            >
              <svg
                viewBox={`0 0 ${W} ${H}`}
                className="h-auto w-full touch-pan-y"
                role="presentation"
                onPointerMove={onPointer}
                onPointerDown={onPointer}
                onPointerLeave={(e) => {
                  if (e.pointerType === 'mouse') setCursor(null)
                }}
              >
                {yTicks.map((v) => (
                  <g key={v}>
                    <line
                      x1={PAD.left}
                      x2={W - PAD.right}
                      y1={y(v)}
                      y2={y(v)}
                      stroke="var(--color-border)"
                    />
                    <text
                      x={PAD.left - 6}
                      y={y(v) + 4}
                      textAnchor="end"
                      fontSize="11"
                      fill="var(--color-muted)"
                    >
                      {formatValue(v, unit)}
                    </text>
                  </g>
                ))}
                {xTicks.map((v, i) => (
                  <text
                    key={v}
                    x={x(v)}
                    y={H - 8}
                    textAnchor={i === 0 ? 'start' : i === xTicks.length - 1 ? 'end' : 'middle'}
                    fontSize="11"
                    fill="var(--color-muted)"
                  >
                    {tickFormat().format(new Date(v * 1000))}
                  </text>
                ))}
                {markers
                  .filter((m) => m.time >= from && m.time <= to)
                  .map((m) => (
                    <g
                      key={`${m.label}-${m.time}`}
                      role={m.onOpen ? 'link' : 'img'}
                      tabIndex={0}
                      aria-label={t('chart.marker', { label: m.label, time: fullTime(m.time) })}
                      onClick={m.onOpen}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') m.onOpen?.()
                        e.stopPropagation()
                      }}
                      className="cursor-pointer"
                    >
                      <title>{`${m.label} - ${fullTime(m.time)}`}</title>
                      <line
                        x1={x(m.time)}
                        x2={x(m.time)}
                        y1={PAD.top}
                        y2={H - PAD.bottom}
                        stroke="var(--color-text)"
                        strokeDasharray="3 3"
                        strokeWidth="2"
                      />
                      <text
                        x={x(m.time) + 3}
                        y={PAD.top + 10}
                        fontSize="11"
                        fill="var(--color-text)"
                      >
                        {m.label}
                      </text>
                    </g>
                  ))}
                {shown.map((s, i) =>
                  hidden.has(s.id) || s.points.length === 0 ? null : (
                    <g key={s.id}>
                      <path
                        d={s.points
                          .map(
                            (p, n) =>
                              `${n === 0 ? 'M' : 'L'}${x(p[0]).toFixed(1)} ${y(p[1]).toFixed(1)}`,
                          )
                          .join('')}
                        fill="none"
                        stroke={COLORS[i]}
                        strokeWidth="2"
                        strokeDasharray={DASHES[i] || undefined}
                      />
                      {s.points
                        .filter((_, n) => n % Math.max(1, Math.ceil(s.points.length / 6)) === 0)
                        .map((p) => (
                          <Marker
                            key={p[0]}
                            shape={SHAPES[i]}
                            x={x(p[0])}
                            y={y(p[1])}
                            color={COLORS[i]}
                          />
                        ))}
                    </g>
                  ),
                )}
                {activeTime !== undefined && (
                  <line
                    x1={x(activeTime)}
                    x2={x(activeTime)}
                    y1={PAD.top}
                    y2={H - PAD.bottom}
                    stroke="var(--color-text)"
                    strokeWidth="1"
                  />
                )}
              </svg>
            </div>
          )}
          <p id={`${id}-read`} role="status" className="min-h-5 text-sm">
            {!table && activeTime !== undefined
              ? `${fullTime(activeTime)}: ` +
                visible
                  .map((s) => {
                    const v = valueAt(s, activeTime)
                    return `${s.name} ${v === undefined ? '-' : formatValue(v, unit)}`
                  })
                  .join('; ')
              : ''}
          </p>
          <ul aria-label={t('chart.legend')} className="flex flex-wrap gap-2">
            {shown.map((s, i) => (
              <li key={s.id}>
                <button
                  type="button"
                  aria-pressed={!hidden.has(s.id)}
                  onClick={() => {
                    const next = new Set(hidden)
                    if (next.has(s.id)) next.delete(s.id)
                    else next.add(s.id)
                    setHidden(next)
                  }}
                  className="inline-flex min-h-8 items-center gap-2 rounded-sm border border-border-strong px-2 py-1 text-sm coarse:min-h-11 aria-[pressed=false]:opacity-60 aria-[pressed=false]:line-through"
                >
                  <svg width="28" height="12" aria-hidden="true">
                    <line
                      x1="0"
                      x2="28"
                      y1="6"
                      y2="6"
                      stroke={COLORS[i]}
                      strokeWidth="2"
                      strokeDasharray={DASHES[i] || undefined}
                    />
                    <Marker shape={SHAPES[i]} x={14} y={6} color={COLORS[i]} />
                  </svg>
                  {s.name}
                </button>
              </li>
            ))}
          </ul>
          {series.length > MAX_SERIES && (
            <p className="text-sm text-muted">{t('chart.too_many', { count: MAX_SERIES })}</p>
          )}
        </>
      )}
    </figure>
  )
}
