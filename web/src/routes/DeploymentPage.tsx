// SPDX-License-Identifier: Apache-2.0

import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { useCallback, useEffect, useRef, useState } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { CancelDeployment } from '@/components/CancelDeployment'
import { DeployDialog } from '@/components/DeployDialog'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { RollbackDialog } from '@/components/RollbackDialog'
import { Time } from '@/components/Time'
import { Banner, Button, CheckboxField, Skeleton, StatusBadge } from '@/components/ui'
import { formatTime, t, tState } from '@/i18n'
import { openStream } from '@/live'
import { useMe } from '@/auth'
import { accessFor } from '@/permissions'
import { NotFoundPage } from '@/routes/ErrorPages'
import { useScope } from '@/scope'
import { deploymentTone, isDeploymentActive } from '@/status'

type DeploymentEvent = components['schemas']['DeploymentEvent']

interface BuildLine {
  seq: number
  stream?: string
  line?: string
  time?: string
}

// A fresh live release shows the Roll back shortcut for 10 minutes (UI-08).
const SHORTCUT_MS = 10 * 60_000

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
    // The browser resumes with Last-Event-ID after a short break. After a longer one, read the
    // stored lines and the state again (REQ-API-103).
    return openStream(
      `/api/v1/deployments/${encodeURIComponent(deploymentId)}/events`,
      ['state', 'health', 'log', 'canary_gate'],
      {
        onEvent: handle,
        onReconnect: () => {
          void readStored()
          onState()
        },
        onLost: setDisconnected,
      },
    )
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

function weightOf(items: { weight?: number }[]): number {
  return items.reduce((sum, p) => sum + (p.weight ?? 0), 0)
}

// The share of traffic of the new and the old release. The weights come from the placements.
function TrafficBar({ label, percent }: { label: string; percent: number }) {
  const text = t('deployment.traffic_value', { label, percent })
  return (
    <div className="space-y-1">
      <p className="text-sm">{text}</p>
      <div
        role="progressbar"
        aria-label={text}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
        className="h-3 overflow-hidden rounded-sm border border-border bg-bg"
      >
        <div className="h-full bg-primary" style={{ width: `${percent}%` }} />
      </div>
    </div>
  )
}

// UI-08 (without the canary bar): one deployment with its steps, replicas, traffic and the
// live build log. Cancel and the Roll back shortcut are here.
export function DeploymentPage() {
  const params = useParams({ from: '/o/$org/p/$project/e/$env/s/$service/deployments/$deployment' })
  const queryClient = useQueryClient()
  const me = useMe()
  const scope = useScope(params.project, params.env, params.service)
  const serviceId = scope.service?.id ?? ''
  const [dialog, setDialog] = useState<'rollback' | 'deploy' | null>(null)
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
  const service = useQuery({
    queryKey: ['service', serviceId],
    enabled: Boolean(serviceId),
    queryFn: () =>
      unwrap(api.GET('/services/{service_id}', { params: { path: { service_id: serviceId } } })),
    refetchInterval: live ? 3_000 : false,
  })
  const refresh = useCallback(() => {
    for (const key of ['deployment', 'service', 'deployments', 'releases']) {
      void queryClient.invalidateQueries({ queryKey: [key] })
    }
  }, [queryClient])
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
  if (scope.state === 'notfound') return <NotFoundPage />
  const d = deployment.data
  const steps = d.steps ?? []
  const environment = scope.environment
  const role = me.data?.org_role
  const deployAccess = accessFor(role, 'service:deploy', environment?.protected)
  const rollbackAccess = accessFor(role, 'service:rollback', environment?.protected)
  const finishedAgo = d.finished_at ? Date.now() - new Date(d.finished_at).getTime() : Infinity
  const shortcut =
    d.state === 'live' &&
    Boolean(d.previous_release_id) &&
    finishedAgo < SHORTCUT_MS &&
    rollbackAccess !== 'hidden'
  const placements = service.data?.placements ?? []
  const newWeight = weightOf(placements.filter((p) => p.release_id === d.release_id))
  const oldWeight = weightOf(placements.filter((p) => p.release_id !== d.release_id))
  const totalWeight = newWeight + oldWeight
  const percent = (w: number) => (totalWeight > 0 ? Math.round((w / totalWeight) * 100) : 0)

  // After a lost stream the screen may be old. Ask the server first, then open the dialog.
  const openRollback = async () => {
    await queryClient.refetchQueries({ queryKey: ['deployment', params.deployment] })
    setDialog('rollback')
  }
  const redeploy = deployAccess === 'allow' && scope.state === 'ok' && (
    <Button onClick={() => setDialog('deploy')}>{t('deployment.deploy_again')}</Button>
  )

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
        actions={
          live && <CancelDeployment deployment={d} environmentProtected={environment?.protected} />
        }
      />
      <div className="mb-4 space-y-3">
        {log.disconnected && (
          <Banner variant="warning" title={t('live.paused')}>
            {t('live.paused_text')}
          </Banner>
        )}
        {scope.state === 'error' && <ErrorBanner error={scope.error} onRetry={scope.retry} />}
        {service.isError && (
          <ErrorBanner error={service.error} onRetry={() => void service.refetch()} />
        )}
        {d.state === 'failed' && (
          <Banner
            variant="danger"
            title={t('deployment.failed_title')}
            action={redeploy || undefined}
          >
            <p>{tState('deployment.reason', d.failure_reason)}</p>
            {d.failure_detail && <p className="text-muted">{d.failure_detail}</p>}
            <p>{t('deployment.failed_next')}</p>
          </Banner>
        )}
        {d.state === 'rolled_back' && (
          <Banner
            variant="warning"
            title={t('deployment.rolled_back_title')}
            action={redeploy || undefined}
          >
            {d.failure_reason && <p>{tState('deployment.reason', d.failure_reason)}</p>}
            {d.failure_detail && <p className="text-muted">{d.failure_detail}</p>}
            <p>{t('deployment.rolled_back_next')}</p>
          </Banner>
        )}
        {d.state === 'cancelled' && (
          <Banner title={t('deployment.cancelled_title')} action={redeploy || undefined}>
            {t('deployment.cancelled_next')}
          </Banner>
        )}
        {d.state === 'superseded' && (
          <Banner title={t('deployment.superseded_title')}>
            {t('deployment.superseded_next')}
          </Banner>
        )}
        {shortcut && (
          <Banner
            title={t('deployment.live_title')}
            action={
              <Button
                reason={rollbackAccess === 'blocked' ? t('common.needs_admin') : undefined}
                onClick={() => void openRollback()}
              >
                {t('service.rollback')}
              </Button>
            }
          >
            {t('deployment.live_next')}
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
        <div className="min-w-0 space-y-6">
          <section aria-labelledby="dep-replicas" className="space-y-2">
            <h2 id="dep-replicas" className="font-semibold">
              {t('deployment.replicas')}
            </h2>
            {service.isPending && Boolean(serviceId) && <Skeleton lines={2} />}
            {service.data && placements.length === 0 && (
              <p className="text-muted">{t('service.no_placements')}</p>
            )}
            <ul className="space-y-1 text-sm">
              {placements.map((p) => (
                <li key={p.id}>
                  {p.release_id === d.release_id
                    ? t('deployment.new_release')
                    : t('deployment.old_release')}{' '}
                  #{(p.replica_index ?? 0) + 1} · {tState('status.role', p.role)} ·{' '}
                  {tState('status.health', p.health)}
                </li>
              ))}
            </ul>
            {totalWeight > 0 && (
              <div className="space-y-2 pt-2">
                <h3 className="text-sm font-semibold">{t('deployment.traffic')}</h3>
                <TrafficBar label={t('deployment.old_release')} percent={percent(oldWeight)} />
                <TrafficBar label={t('deployment.new_release')} percent={percent(newWeight)} />
              </div>
            )}
          </section>
          <details open className="space-y-2">
            <summary className="min-h-11 cursor-pointer font-semibold">{t('build.log')}</summary>
            {log.stored.isError && (
              <ErrorBanner error={log.stored.error} onRetry={() => void log.stored.refetch()} />
            )}
            {log.stored.isPending ? <Skeleton /> : <BuildLog lines={log.lines} />}
          </details>
        </div>
      </div>
      {dialog === 'rollback' && d.previous_release_id && scope.service && environment && (
        <RollbackDialog
          service={service.data ?? scope.service}
          environment={environment}
          releaseId={d.previous_release_id}
          onClose={() => setDialog(null)}
          onDeployInstead={() => setDialog('deploy')}
        />
      )}
      {dialog === 'deploy' && scope.service && environment && (
        <DeployDialog
          service={service.data ?? scope.service}
          environment={environment}
          onClose={() => setDialog(null)}
        />
      )}
    </section>
  )
}
