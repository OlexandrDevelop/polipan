// SPDX-License-Identifier: Apache-2.0

import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { useCallback, useEffect, useRef, useState } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import { Banner, CheckboxField, Skeleton, StatusBadge } from '@/components/ui'
import { formatTime, t, tState } from '@/i18n'
import { deploymentTone, isDeploymentActive } from '@/status'

type DeploymentEvent = components['schemas']['DeploymentEvent']

interface BuildLine {
  seq: number
  stream?: string
  line?: string
  time?: string
}

const DISCONNECT_MS = 5_000

// Build and pre_deploy log lines: first the stored lines (getDeploymentLogs), then live lines
// from the deployment stream (streamDeploymentEvents). Secret values come masked as ***.
function useBuildLog(deploymentId: string, live: boolean, onState: () => void) {
  const [lines, setLines] = useState<BuildLine[]>([])
  const [disconnected, setDisconnected] = useState(false)
  const lastSeq = useRef(0)

  const append = useCallback((items: BuildLine[]) => {
    const fresh = items.filter((l) => l.seq > lastSeq.current).sort((a, b) => a.seq - b.seq)
    if (fresh.length === 0) return
    lastSeq.current = fresh[fresh.length - 1].seq
    setLines((prev) => [...prev, ...fresh])
  }, [])

  const readStored = useCallback(async () => {
    const stored = await unwrap(
      api.GET('/deployments/{deployment_id}/logs', {
        params: {
          path: { deployment_id: deploymentId },
          query: lastSeq.current > 0 ? { after_seq: lastSeq.current } : {},
        },
      }),
    )
    append(stored.map((l) => ({ ...l, seq: l.seq ?? 0 })))
  }, [deploymentId, append])

  const stored = useQuery({
    queryKey: ['deployment-logs', deploymentId],
    queryFn: async () => {
      await readStored()
      return true
    },
    // A stopped build does not change. While it runs, the stream brings the new lines.
    staleTime: Infinity,
  })

  useEffect(() => {
    if (!live) return
    const source = new EventSource(`/api/v1/deployments/${encodeURIComponent(deploymentId)}/events`)
    let timer: ReturnType<typeof setTimeout> | undefined
    let lost = false
    const handle = (message: MessageEvent<string>) => {
      let event: DeploymentEvent
      try {
        event = JSON.parse(message.data) as DeploymentEvent
      } catch {
        return
      }
      if (event.type === 'log') {
        const data = (event.data ?? {}) as Partial<BuildLine>
        append([
          { seq: Number(data.seq ?? 0), stream: data.stream, line: data.line, time: event.time },
        ])
      } else {
        onState()
      }
    }
    source.onmessage = handle
    for (const name of ['state', 'health', 'log', 'canary_gate']) {
      source.addEventListener(name, handle as EventListener)
    }
    source.onopen = () => {
      clearTimeout(timer)
      if (lost) {
        // Back online: read what was missed, then hide the banner.
        lost = false
        setDisconnected(false)
        void readStored()
        onState()
      }
    }
    source.onerror = () => {
      clearTimeout(timer)
      timer = setTimeout(() => {
        lost = true
        setDisconnected(true)
      }, DISCONNECT_MS)
    }
    return () => {
      clearTimeout(timer)
      source.close()
    }
  }, [deploymentId, live, append, readStored, onState])

  return { lines, stored, disconnected }
}

function BuildLog({ lines }: { lines: BuildLine[] }) {
  const [follow, setFollow] = useState(true)
  const box = useRef<HTMLPreElement>(null)
  useEffect(() => {
    const el = box.current
    if (follow && el) el.scrollTop = el.scrollHeight
  }, [lines, follow])
  return (
    <div className="space-y-2">
      <CheckboxField
        label={t('build.follow')}
        checked={follow}
        onChange={(e) => setFollow(e.target.checked)}
      />
      <pre
        ref={box}
        role="log"
        aria-live="off"
        aria-label={t('build.log')}
        tabIndex={0}
        className="max-h-[60vh] overflow-auto whitespace-pre-wrap break-words rounded-sm border border-border bg-surface p-3 font-mono text-sm"
      >
        {lines.length === 0
          ? t('build.empty')
          : lines.map((l) => (
              <span key={l.seq} className="block">
                {l.time && (
                  <span className="text-muted" title={l.time}>
                    {formatTime(l.time)}{' '}
                  </span>
                )}
                {l.stream === 'stderr' && <span className="text-danger">{t('logs.stderr')} </span>}
                {l.line}
              </span>
            ))}
      </pre>
    </div>
  )
}

// UI-08 (M2 part): one deployment with its steps and the live build log.
export function DeploymentPage() {
  const params = useParams({ from: '/o/$org/p/$project/e/$env/s/$service/deployments/$deployment' })
  const queryClient = useQueryClient()
  const deployment = useQuery({
    queryKey: ['deployment', params.deployment],
    queryFn: () =>
      unwrap(
        api.GET('/deployments/{deployment_id}', {
          params: { path: { deployment_id: params.deployment } },
        }),
      ),
    refetchInterval: (query) => (isDeploymentActive(query.state.data?.state) ? 5_000 : false),
  })
  const live = deployment.data ? isDeploymentActive(deployment.data.state) : false
  const refresh = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: ['deployment', params.deployment] })
  }, [queryClient, params.deployment])
  const log = useBuildLog(params.deployment, live, refresh)

  const breadcrumb = (
    <Link
      to="/o/$org/p/$project/e/$env/s/$service/$tab"
      params={{ ...params, tab: 'deployments' }}
      className="text-primary underline"
    >
      {params.service} › {t('service.tab.deployments')}
    </Link>
  )

  if (deployment.isPending) return <Skeleton />
  if (deployment.isError) {
    return <ErrorBanner error={deployment.error} onRetry={() => void deployment.refetch()} />
  }
  const d = deployment.data
  const steps = d.steps ?? []

  return (
    <section>
      <PageHeader
        title={t('deployment.title', { service: params.service })}
        context={[params.env, params.project]}
        breadcrumb={breadcrumb}
        badges={
          <StatusBadge
            tone={deploymentTone(d.state)}
            label={tState('status.deployment', d.state)}
          />
        }
      />
      <div className="mb-4 space-y-3">
        {log.disconnected && (
          <Banner variant="warning" title={t('live.paused')}>
            {t('live.paused_text')}
          </Banner>
        )}
        {d.state === 'failed' && (
          <Banner variant="danger" title={t('deployment.failed_title')}>
            <p>{tState('deployment.reason', d.failure_reason)}</p>
            {d.failure_detail && <p className="text-muted">{d.failure_detail}</p>}
            <p>{t('deployment.failed_next')}</p>
          </Banner>
        )}
        {(d.warnings ?? []).map((w) => (
          <Banner key={w.code} variant="warning">
            {w.message ?? w.code}
          </Banner>
        ))}
      </div>
      <div className="grid gap-6 sm:grid-cols-[minmax(0,240px)_minmax(0,1fr)]">
        <div className="space-y-2">
          <h2 className="font-semibold">{t('deployment.steps')}</h2>
          {steps.length === 0 ? (
            <p className="text-muted">{tState('status.deployment', d.state)}</p>
          ) : (
            <ol className="space-y-2">
              {steps.map((s, i) => {
                const current = i === steps.length - 1 && live
                return (
                  <li
                    key={`${s.state}-${i}`}
                    aria-current={current ? 'step' : undefined}
                    className="flex flex-wrap items-center gap-2 text-sm"
                  >
                    <StatusBadge
                      tone={
                        current
                          ? 'warning'
                          : deploymentTone(s.state === 'failed' ? 'failed' : 'live')
                      }
                      label={tState('status.deployment', s.state)}
                    />
                    <Time value={s.time} />
                    {s.message && <span className="w-full text-muted">{s.message}</span>}
                  </li>
                )
              })}
            </ol>
          )}
          <p className="text-sm text-muted">
            {t('deployment.started')}: <Time value={d.started_at} />
          </p>
        </div>
        <details open className="min-w-0 space-y-2">
          <summary className="min-h-11 cursor-pointer font-semibold">{t('build.log')}</summary>
          {log.stored.isError && (
            <ErrorBanner error={log.stored.error} onRetry={() => void log.stored.refetch()} />
          )}
          {log.stored.isPending ? <Skeleton /> : <BuildLog lines={log.lines} />}
        </details>
      </div>
    </section>
  )
}
