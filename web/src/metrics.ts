// SPDX-License-Identifier: Apache-2.0

import type { components } from '@api/schema'
import { currentLocale } from '@/i18n'

export type MetricResult = components['schemas']['MetricResult']
export type MetricName =
  | 'cpu'
  | 'memory'
  | 'network_in'
  | 'network_out'
  | 'disk'
  | 'requests'
  | 'status_5xx_rate'
  | 'latency_p50'
  | 'latency_p95'
  | 'latency_p99'
  | 'restarts'
  | 'load'

export type Unit = 'percent' | 'bytes' | 'bytes_per_s' | 'per_s' | 'ratio' | 'seconds' | 'count'

// The unit of each metric. The OpenAPI file does not say it, so this is the agreed reading:
// cpu in percent, memory and disk in bytes, rates per second, 5xx as a ratio, latency in seconds.
export const metricUnit: Record<MetricName, Unit> = {
  cpu: 'percent',
  memory: 'bytes',
  network_in: 'bytes_per_s',
  network_out: 'bytes_per_s',
  disk: 'bytes',
  requests: 'per_s',
  status_5xx_rate: 'ratio',
  latency_p50: 'seconds',
  latency_p95: 'seconds',
  latency_p99: 'seconds',
  restarts: 'count',
  load: 'count',
}

const SIZES = ['B', 'KiB', 'MiB', 'GiB', 'TiB']

function number(value: number, digits = 1): string {
  return new Intl.NumberFormat(currentLocale(), { maximumFractionDigits: digits }).format(value)
}

function size(value: number): string {
  let n = value
  let i = 0
  while (Math.abs(n) >= 1024 && i < SIZES.length - 1) {
    n /= 1024
    i++
  }
  return `${number(n)} ${SIZES[i]}`
}

export function formatValue(value: number, unit: Unit): string {
  switch (unit) {
    case 'percent':
      return `${number(value)} %`
    case 'ratio':
      return `${number(value * 100, 2)} %`
    case 'bytes':
      return size(value)
    case 'bytes_per_s':
      return `${size(value)}/s`
    case 'per_s':
      return `${number(value, 2)}/s`
    case 'seconds':
      return value < 1 ? `${number(value * 1000, 0)} ms` : `${number(value, 2)} s`
    default:
      return number(value, 2)
  }
}

export const RANGES = [
  { id: '1h', seconds: 3600 },
  { id: '6h', seconds: 6 * 3600 },
  { id: '24h', seconds: 24 * 3600 },
  { id: '7d', seconds: 7 * 86400 },
  { id: '30d', seconds: 30 * 86400 },
  { id: '90d', seconds: 90 * 86400 },
] as const
export type RangeId = (typeof RANGES)[number]['id'] | 'custom'

// The scrape interval is 15 s, so a smaller step has no use.
export const STEPS = [15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 14400, 21600, 43200, 86400]
// AC-OBS-05: fewer than 1500 points in a chart.
export const MAX_POINTS = 1500

export function pointsFor(rangeSeconds: number, step: number): number {
  return Math.floor(rangeSeconds / step) + 1
}

// The smallest step that gives fewer than 1000 points, so there is room for growth.
export function autoStep(rangeSeconds: number): number {
  return STEPS.find((s) => pointsFor(rangeSeconds, s) <= 1000) ?? Math.ceil(rangeSeconds / 1000)
}

// A step is allowed when it keeps the chart under 1500 points.
export function stepAllowed(rangeSeconds: number, step: number): boolean {
  return pointsFor(rangeSeconds, step) < MAX_POINTS
}

export interface ChartSeries {
  id: string
  name: string
  points: [number, number][]
}

// The label of a series: the metric name and the labels the server added (replica, node).
export function seriesName(
  metricLabel: string,
  labels: Record<string, string> | undefined,
): string {
  const extra = Object.entries(labels ?? {})
    .map(([k, v]) => `${k} ${v}`)
    .join(', ')
  return extra ? `${metricLabel} (${extra})` : metricLabel
}
