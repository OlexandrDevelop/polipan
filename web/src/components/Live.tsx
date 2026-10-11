// SPDX-License-Identifier: Apache-2.0

import { useRef, useState } from 'react'

// A big number. When the value changes it rolls in (transform and opacity, 200 ms). The final
// value is in the DOM at once, so a screen reader reads the final value only.
export function Counter({ value }: { value: number | string }) {
  const first = useRef(true)
  const roll = !first.current
  first.current = false
  return (
    <span key={String(value)} className={roll ? 'counter-roll' : undefined}>
      {value}
    </span>
  )
}

// A small trend line (20 px high, 1.5 px). It is decoration: the number next to it is the text.
export function Sparkline({ values, className = '' }: { values: number[]; className?: string }) {
  const w = 100
  const h = 20
  const max = Math.max(...values, 1)
  const min = Math.min(...values, 0)
  const span = max - min || 1
  const step = values.length > 1 ? w / (values.length - 1) : w
  const points = values
    .map((v, i) => `${(i * step).toFixed(1)},${(h - 2 - ((v - min) / span) * (h - 4)).toFixed(1)}`)
    .join(' ')
  return (
    <svg
      aria-hidden="true"
      viewBox={`0 0 ${w} ${h}`}
      preserveAspectRatio="none"
      className={`h-5 w-20 ${className}`}
    >
      {values.length > 1 ? (
        <polyline
          points={points}
          fill="none"
          stroke="currentColor"
          strokeWidth="1.5"
          vectorEffect="non-scaling-stroke"
        />
      ) : (
        <line
          x1="0"
          y1="10"
          x2={w}
          y2="10"
          stroke="currentColor"
          strokeWidth="1"
          strokeDasharray="2 3"
        />
      )}
    </svg>
  )
}

// Keeps the last values of a number, for a sparkline of live data (no extra API call).
export function useHistory(value: number | undefined, size = 30): number[] {
  const [history, setHistory] = useState<number[]>([])
  const last = useRef<number | undefined>(undefined)
  if (value !== undefined && value !== last.current) {
    last.current = value
    setHistory((h) => [...h, value].slice(-size))
  }
  return history
}
