// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { MetricChart } from '@/components/MetricChart'
import type { ChartMarker } from '@/components/MetricChart'
import { Banner, SectionNav, Skeleton, TextField, SelectField } from '@/components/ui'
import { hasKey, t } from '@/i18n'
import type { MessageKey } from '@/i18n'
import { autoStep, metricUnit, MAX_POINTS, RANGES, seriesName, stepAllowed, STEPS } from '@/metrics'
import type { ChartSeries, MetricName, RangeId } from '@/metrics'

interface Group {
  id:
    'cpu' | 'memory' | 'network' | 'requests' | 'errors' | 'latency' | 'restarts' | 'disk' | 'load'
  metrics: MetricName[]
}

// The charts of a service and of a node (UI-16). Each group is one chart.
const SERVICE_GROUPS: Group[] = [
  { id: 'cpu', metrics: ['cpu'] },
  { id: 'memory', metrics: ['memory'] },
  { id: 'network', metrics: ['network_in', 'network_out'] },
  { id: 'requests', metrics: ['requests'] },
  { id: 'errors', metrics: ['status_5xx_rate'] },
  { id: 'latency', metrics: ['latency_p50', 'latency_p95', 'latency_p99'] },
  { id: 'restarts', metrics: ['restarts'] },
]
const NODE_GROUPS: Group[] = [
  { id: 'cpu', metrics: ['cpu'] },
  { id: 'memory', metrics: ['memory'] },
  { id: 'disk', metrics: ['disk'] },
  { id: 'load', metrics: ['load'] },
  { id: 'network', metrics: ['network_in', 'network_out'] },
]

const SERVICE_PAGES = [
  { id: 'resources', charts: ['cpu', 'memory'] },
  { id: 'traffic', charts: ['network', 'requests', 'errors', 'latency'] },
  { id: 'stability', charts: ['restarts'] },
]
const NODE_PAGES = [
  { id: 'resources', charts: ['cpu', 'memory', 'disk', 'load'] },
  { id: 'traffic', charts: ['network'] },
]

// <input type="datetime-local"> wants local time without a zone.
function localInput(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function metricLabel(metric: string): string {
  const key = `metric.${metric}`
  return hasKey(key) ? t(key) : metric
}

export function MetricsPanel({
  kind,
  id,
  markers = [],
}: {
  kind: 'service' | 'node'
  id: string
  // Release markers (service only).
  markers?: ChartMarker[]
}) {
  const [range, setRange] = useState<RangeId>('1h')
  const [step, setStep] = useState<'auto' | number>('auto')
  const [customFrom, setCustomFrom] = useState(() => localInput(new Date(Date.now() - 3600_000)))
  const [customTo, setCustomTo] = useState(() => localInput(new Date()))

  const org = useQuery({ queryKey: ['org'], queryFn: () => unwrap(api.GET('/org')) })
  const retentionDays = org.data?.settings?.metrics_retention_days ?? 90
  const groups = kind === 'service' ? SERVICE_GROUPS : NODE_GROUPS
  // The charts are in pages, so the tab does not open with every chart at once.
  const pages = kind === 'service' ? SERVICE_PAGES : NODE_PAGES
  const [page, setPage] = useState('resources')
  const shown = (pages.find((p) => p.id === page) ?? pages[0]).charts
  const names = [...new Set(groups.flatMap((g) => g.metrics))]

  const custom = range === 'custom'
  const customStart = new Date(customFrom).getTime()
  const customEnd = new Date(customTo).getTime()
  const oldest = Date.now() - retentionDays * 86400_000
  const customError =
    !custom || (customStart < customEnd && customStart >= oldest)
      ? null
      : customStart >= customEnd
        ? t('metrics.range_order')
        : t('metrics.beyond_retention', { days: retentionDays })
  const presetSeconds = RANGES.find((r) => r.id === range)?.seconds
  const spanSeconds = custom
    ? Math.max(1, Math.round((customEnd - customStart) / 1000))
    : (presetSeconds ?? 3600)
  const useStep = step !== 'auto' && stepAllowed(spanSeconds, step) ? step : autoStep(spanSeconds)

  const data = useQuery({
    queryKey: ['metrics', kind, id, range, useStep, custom ? customFrom + customTo : ''],
    enabled: customError === null && Boolean(id),
    retry: false,
    // A preset range ends now, so it moves with time.
    refetchInterval: custom ? false : 30_000,
    queryFn: async () => {
      const to = custom ? Math.floor(customEnd / 1000) : Math.floor(Date.now() / 1000)
      const from = custom ? Math.floor(customStart / 1000) : to - spanSeconds
      const query = {
        metrics: names,
        from: new Date(from * 1000).toISOString(),
        to: new Date(to * 1000).toISOString(),
        step: `${useStep}s`,
      }
      const result =
        kind === 'service'
          ? await unwrap(
              api.GET('/services/{service_id}/metrics', {
                params: { path: { service_id: id }, query },
              }),
            )
          : await unwrap(
              api.GET('/nodes/{node_id}/metrics', { params: { path: { node_id: id }, query } }),
            )
      return { result, from, to }
    },
  })

  const unavailable =
    data.error instanceof ApiError &&
    (data.error.code === 'monitoring_unavailable' || data.error.status === 503)
  const allEmpty =
    data.data !== undefined && (data.data.result.series ?? []).every((s) => !s.points?.length)

  function seriesOf(group: Group): ChartSeries[] {
    const out: ChartSeries[] = []
    for (const s of data.data?.result.series ?? []) {
      if (!group.metrics.includes(s.metric as MetricName)) continue
      const label = s.metric ?? ''
      out.push({
        id: `${label}-${JSON.stringify(s.labels ?? {})}`,
        name: seriesName(metricLabel(label), s.labels),
        points: (s.points ?? []).map((p) => [p[0], p[1]] as [number, number]),
      })
    }
    return out
  }

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <SelectField
          label={t('metrics.range')}
          value={range}
          onChange={(e) => setRange(e.target.value as RangeId)}
        >
          {RANGES.filter((r) => r.seconds <= retentionDays * 86400).map((r) => (
            <option key={r.id} value={r.id}>
              {t(`metrics.range.${r.id}`)}
            </option>
          ))}
          <option value="custom">{t('metrics.range.custom')}</option>
        </SelectField>
        <SelectField
          label={t('metrics.step')}
          value={String(step)}
          onChange={(e) => setStep(e.target.value === 'auto' ? 'auto' : Number(e.target.value))}
          hint={t('metrics.step_hint', { count: MAX_POINTS })}
        >
          <option value="auto">{t('metrics.step_auto', { seconds: autoStep(spanSeconds) })}</option>
          {STEPS.map((s) => (
            <option key={s} value={s} disabled={!stepAllowed(spanSeconds, s)}>
              {t('metrics.step_value', { seconds: s })}
            </option>
          ))}
        </SelectField>
        {custom && (
          <>
            <TextField
              type="datetime-local"
              label={t('metrics.from')}
              value={customFrom}
              onChange={(e) => setCustomFrom(e.target.value)}
            />
            <TextField
              type="datetime-local"
              label={t('metrics.to')}
              value={customTo}
              onChange={(e) => setCustomTo(e.target.value)}
            />
          </>
        )}
      </div>
      <p className="text-sm text-muted">{t('metrics.retention', { days: retentionDays })}</p>
      {customError && <Banner variant="warning">{customError}</Banner>}
      {data.isPending && customError === null && <Skeleton />}
      {data.isError && unavailable && (
        <Banner variant="warning" title={t('metrics.unavailable')}>
          {t('metrics.unavailable_next')}
        </Banner>
      )}
      {data.isError && !unavailable && (
        <ErrorBanner error={data.error} onRetry={() => void data.refetch()} />
      )}
      {data.data && allEmpty && <p className="text-muted">{t('metrics.no_data')}</p>}
      {data.data && (
        <SectionNav
          horizontal
          controls={false}
          label={t('metrics.pages')}
          items={pages.map((p) => ({ id: p.id, label: t(`metrics.page.${p.id}` as MessageKey) }))}
          active={page}
          onChange={setPage}
        />
      )}
      {data.data && (
        <div key={page} className="enter grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
          {groups
            .filter((g) => shown.includes(g.id))
            .map((g) => (
              <MetricChart
                key={g.id}
                title={t(`metrics.chart.${g.id}`)}
                unit={metricUnit[g.metrics[0]]}
                series={seriesOf(g)}
                from={data.data.from}
                to={data.data.to}
                markers={g.id === 'cpu' || g.id === 'errors' || g.id === 'latency' ? markers : []}
              />
            ))}
        </div>
      )}
    </div>
  )
}
