// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { Suspense, useEffect, useState } from 'react'
import type { ComponentProps, ReactNode } from 'react'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { DataList } from '@/components/DataList'
import { DeployDialog } from '@/components/DeployDialog'
import { MoveDialog } from '@/components/MoveDialog'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { RollbackDialog } from '@/components/RollbackDialog'
import { Time } from '@/components/Time'
import {
  Banner,
  Button,
  Panel,
  EmptyState,
  DeployProgress,
  EnvironmentBadges,
  KeyHint,
  Pane,
  SectionNav,
  Skeleton,
  StatusBadge,
} from '@/components/ui'
import type { SectionItem } from '@/components/ui'
import { t, tState } from '@/i18n'
import { accessFor, effectiveRole } from '@/permissions'
import { lazyNamed } from '@/lazy'
import { ServiceDeployKey } from '@/routes/ServiceDeployKey'
import { useSingleKey } from '@/shortcuts'
import { useMe } from '@/auth'
import { NotFoundPage } from '@/routes/ErrorPages'
import { ServiceDomains } from '@/routes/ServiceDomains'
import { ServiceHealth } from '@/routes/ServiceHealth'
import { ServiceReleases, useReleases } from '@/routes/ServiceReleases'
import { ServicePlacement, FailoverBanner } from '@/routes/ServicePlacement'
import { ServiceSettings } from '@/routes/ServiceSettings'
import { BackupBadge, ServiceBackups } from '@/routes/ServiceBackups'
import { ServiceDelete } from '@/routes/ServiceDelete'
import { ServiceStorage } from '@/routes/ServiceStorage'
import { ServiceVariables } from '@/routes/ServiceVariables'
import { useNodes, useScope } from '@/scope'
import type { Service } from '@/scope'
import { failureDetail } from '@/deploymentText'
import { deploymentTone, isDeploymentActive, serviceTone } from '@/status'
import type { Tone } from '@/status'

const ServiceLogs = lazyNamed(() => import('@/routes/ServiceLogs'), 'ServiceLogs')
const MetricsPanel = lazyNamed(() => import('@/components/MetricsPanel'), 'MetricsPanel')
const ServiceTerminal = lazyNamed(() => import('@/routes/ServiceTerminal'), 'ServiceTerminal')

const tabs = [
  'overview',
  'deployments',
  'releases',
  'domains',
  'env',
  'volumes',
  'health',
  'backups',
  'logs',
  'metrics',
  'terminal',
  'settings',
] as const
type Tab = (typeof tabs)[number]

function useDeployments(serviceId: string) {
  return useQuery({
    queryKey: ['deployments', serviceId],
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/deployments', {
          params: { path: { service_id: serviceId }, query: { limit: 20 } },
        }),
      ),
    // A running deployment changes often. Live events (SSE) come later.
    refetchInterval: (query) =>
      query.state.data?.items?.some((d) => isDeploymentActive(d.state)) ? 3_000 : 15_000,
  })
}

// The colour of the health of one container.
function healthTone(health: string | undefined): Tone {
  switch (health) {
    case 'healthy':
      return 'success'
    case 'unhealthy':
      return 'danger'
    case 'starting':
      return 'warning'
    default:
      return 'muted'
  }
}

function sourceText(service: Service): string {
  const config = (service.config ?? {}) as Record<string, unknown>
  // The server keeps the Git source in config.git.
  const source = (config.git ?? config.source ?? {}) as Record<string, unknown>
  if (service.source_type === 'git' && typeof source.repo === 'string') {
    return `${source.repo} @ ${typeof source.branch === 'string' ? source.branch : 'main'}`
  }
  return typeof config.image === 'string' ? config.image : '-'
}

// The last deployment failed. One short line with the reason, and a button for the fix. The long text of the
// build is on the page of the deployment.
function FailedBanner({
  latest,
  base,
}: {
  latest: { id?: string; failure_reason?: string; failure_detail?: string }
  base: { org: string; project: string; env: string; service: string }
}) {
  const detail = failureDetail(latest.failure_detail)
  const link =
    'inline-flex min-h-8 items-center border px-3 py-1 hover:bg-raised coarse:min-h-11 border-border-strong'
  const fix = detail?.needsDeployKey
    ? 'deploy-key'
    : detail?.needsBuildSettings
      ? 'source'
      : undefined
  return (
    <Banner
      variant="danger"
      title={t('deployment.failed_title')}
      action={
        <span className="flex flex-wrap gap-2">
          {fix && (
            <Link
              to="/o/$org/p/$project/e/$env/s/$service/$tab"
              params={{ ...base, tab: 'settings' }}
              hash={fix}
              className={`${link} border-primary bg-primary font-bold text-on-primary`}
            >
              {fix === 'source'
                ? t('deployment.open_build_settings')
                : t('deployment.open_deploy_key')}
            </Link>
          )}
          <Link
            to="/o/$org/p/$project/e/$env/s/$service/deployments/$deployment"
            params={{ ...base, deployment: latest.id ?? '' }}
            className={link}
          >
            {t('service.open_deployment')}
          </Link>
        </span>
      }
    >
      <p>
        {detail && !detail.raw ? detail.text : tState('deployment.reason', latest.failure_reason)}
      </p>
      {detail?.raw && <p className="text-muted">{detail.needsBuildSettings ? detail.text : ''}</p>}
    </Banner>
  )
}

function Overview({
  service,
  serviceId,
  base,
}: {
  service: Service
  serviceId: string
  base: { org: string; project: string; env: string; service: string }
}) {
  const deployments = useDeployments(serviceId)
  const releases = useReleases(serviceId)
  const nodes = useNodes()
  const domains = useQuery({
    queryKey: ['domains', serviceId],
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/domains', { params: { path: { service_id: serviceId } } }),
      ),
  })
  const latest = deployments.data?.items?.[0]
  const liveRelease = releases.data?.items?.find((r) => r.id === service.live_release_id)
  const nodeName = (id?: string) => nodes.data?.find((n) => n.id === id)?.name ?? id ?? '-'
  const config = service.config ?? {}

  return (
    <div className="space-y-4">
      {deployments.isError && (
        <ErrorBanner error={deployments.error} onRetry={() => void deployments.refetch()} />
      )}
      {latest && isDeploymentActive(latest.state) && (
        <Banner
          title={t('service.deploying_title')}
          action={
            <Link
              to="/o/$org/p/$project/e/$env/s/$service/deployments/$deployment"
              params={{ ...base, deployment: latest.id ?? '' }}
              className="text-primary underline"
            >
              {t('service.open_deployment')}
            </Link>
          }
        >
          <div className="w-full min-w-[16rem] max-w-md pt-1">
            <DeployProgress state={latest.state} compact />
          </div>
        </Banner>
      )}
      {latest?.state === 'failed' && <FailedBanner latest={latest} base={base} />}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Panel
          title={service.source_type === 'git' ? t('service.source_git') : t('service.source')}
          className="space-y-1"
        >
          <p className="break-all font-mono text-sm">{sourceText(service)}</p>
          <p className="text-sm text-muted">
            {t('service.port')}: {config.port ? String(config.port) : '-'}
          </p>
        </Panel>
        <Panel title={t('service.release')} className="space-y-1">
          {releases.isPending && Boolean(service.live_release_id) ? (
            <Skeleton lines={1} />
          ) : (
            <p className="break-all text-sm">
              {liveRelease
                ? `v${liveRelease.number ?? ''}${liveRelease.source?.commit_sha ? ` · ${liveRelease.source.commit_sha.slice(0, 7)}` : ''}`
                : (service.live_release_id ?? t('service.no_release'))}
            </p>
          )}
          {liveRelease?.created_at && (
            <p className="text-sm text-muted">
              <Time value={liveRelease.created_at} />
            </p>
          )}
          {releases.isError && (
            <ErrorBanner error={releases.error} onRetry={() => void releases.refetch()} />
          )}
          <p className="text-sm text-muted">
            {t('service.desired')}: {tState('status.desired', service.desired_state)}
          </p>
        </Panel>
        <Panel
          title={t('containers.title')}
          className="sm:col-span-2 lg:col-span-3"
          hint={String((service.placements ?? []).length)}
        >
          {(service.placements ?? []).length === 0 ? (
            <p className="text-muted">{t('service.no_placements')}</p>
          ) : (
            <DataList
              caption={t('containers.title')}
              rows={service.placements ?? []}
              rowKey={(p) => p.id ?? ''}
              columns={[
                {
                  header: t('containers.replica'),
                  primary: true,
                  cell: (p) => `#${(p.replica_index ?? 0) + 1}`,
                },
                {
                  header: t('containers.id'),
                  cell: (p) =>
                    p.container_id ? (
                      <code title={p.container_id}>{p.container_id.slice(0, 12)}</code>
                    ) : (
                      <span className="text-muted">{t('containers.waiting')}</span>
                    ),
                },
                { header: t('containers.node'), cell: (p) => nodeName(p.node_id) },
                {
                  header: t('containers.release'),
                  cell: (p) => {
                    const r = releases.data?.items?.find((x) => x.id === p.release_id)
                    return r?.number ? `v${r.number}` : '-'
                  },
                },
                { header: t('containers.role'), cell: (p) => tState('status.role', p.role) },
                {
                  header: t('containers.health'),
                  cell: (p) => (
                    <StatusBadge
                      tone={healthTone(p.health)}
                      label={tState('status.health', p.health)}
                    />
                  ),
                },
                { header: t('containers.address'), cell: (p) => p.ip || '-' },
                {
                  header: t('containers.created'),
                  cell: (p) => (p.created_at ? <Time value={p.created_at} /> : '-'),
                },
              ]}
            />
          )}
        </Panel>
        <Panel title={t('service.urls')} className="space-y-1 sm:col-span-2 lg:col-span-3">
          {domains.isPending && <Skeleton lines={1} />}
          {domains.isError && (
            <ErrorBanner error={domains.error} onRetry={() => void domains.refetch()} />
          )}
          {domains.data?.length === 0 && <p className="text-muted">{t('service.no_urls')}</p>}
          <ul className="space-y-1">
            {(domains.data ?? []).map((d) => (
              <li key={d.id} className="flex flex-wrap items-center gap-2">
                <a
                  href={`${d.https === false ? 'http' : 'https'}://${d.host}${d.path_prefix ?? ''}`}
                  target="_blank"
                  rel="noreferrer"
                  className="break-all text-primary underline"
                >
                  {d.host}
                  {d.path_prefix && d.path_prefix !== '/' ? d.path_prefix : ''}
                </a>
              </li>
            ))}
          </ul>
        </Panel>
      </div>
    </div>
  )
}

function Deployments({
  serviceId,
  base,
}: {
  serviceId: string
  base: { org: string; project: string; env: string; service: string }
}) {
  const deployments = useDeployments(serviceId)
  if (deployments.isPending) return <Skeleton />
  if (deployments.isError) {
    return <ErrorBanner error={deployments.error} onRetry={() => void deployments.refetch()} />
  }
  const rows = deployments.data.items ?? []
  if (rows.length === 0) {
    return <EmptyState title={t('deployment.empty_title')} text={t('deployment.empty_text')} />
  }
  return (
    <DataList
      caption={t('deployment.caption')}
      rows={rows}
      rowKey={(d) => d.id ?? ''}
      columns={[
        {
          header: t('deployment.started'),
          cell: (d) => (
            <Link
              to="/o/$org/p/$project/e/$env/s/$service/deployments/$deployment"
              params={{ ...base, deployment: d.id ?? '' }}
              className="text-primary underline"
            >
              <Time value={d.started_at} />
            </Link>
          ),
          primary: true,
        },
        {
          header: t('deployment.state'),
          cell: (d) => (
            <StatusBadge
              tone={deploymentTone(d.state)}
              label={tState('status.deployment', d.state)}
            />
          ),
        },
        {
          header: t('deployment.trigger'),
          cell: (d) => tState('deployment.trigger_value', d.trigger),
        },
        {
          header: t('deployment.result'),
          cell: (d) =>
            d.state === 'failed' ? (
              <span>
                {tState('deployment.reason', d.failure_reason)}
                {d.failure_detail && <span className="block text-muted">{d.failure_detail}</span>}
              </span>
            ) : (
              '-'
            ),
        },
        {
          header: t('deployment.finished'),
          cell: (d) => <Time value={d.finished_at} />,
          cardHidden: true,
        },
      ]}
    />
  )
}

// The palette opens a dialog with "?dialog=deploy" or "?dialog=rollback". The page removes it.
export function validateServiceSearch(search: Record<string, unknown>): {
  dialog?: 'deploy' | 'rollback'
} {
  return search.dialog === 'deploy' || search.dialog === 'rollback' ? { dialog: search.dialog } : {}
}

// UI-06 (basic): state and control of one service, with the tabs of M1.
// The settings tab. A menu of sections, and only the open section is shown.
function SettingsTab({
  service,
  environmentProtected,
  base,
  onChanged,
}: {
  service: Service
  environmentProtected?: boolean
  base: ComponentProps<typeof ServiceDelete>['base']
  onChanged: () => void
}) {
  const git = service.source_type === 'git'
  const items: SectionItem[] = [
    { id: 'source', label: t('settings.source') },
    { id: 'run', label: t('settings.run') },
    { id: 'resources', label: t('settings.resources') },
    { id: 'deploy', label: t('settings.deploy') },
    { id: 'hooks', label: t('settings.hooks') },
    ...(git ? [{ id: 'deploy-key', label: t('deploykey.title') }] : []),
    { id: 'placement', label: t('placement.title') },
    { id: 'danger', label: t('delete.title'), danger: true },
  ]
  // A link can open a section: the address ends with #deploy-key.
  const [section, setSection] = useState(() => {
    const wanted = window.location.hash.replace('#', '')
    return items.some((i) => i.id === wanted) ? wanted : 'source'
  })
  return (
    <div className="grid items-start gap-6 lg:grid-cols-[13rem_minmax(0,1fr)] lg:gap-8">
      <SectionNav
        items={items}
        active={section}
        onChange={setSection}
        label={t('settings.sections')}
      />
      <div className="min-w-0 max-w-3xl">
        <ServiceSettings
          key={service.id}
          service={service}
          environmentProtected={environmentProtected}
          onChanged={onChanged}
          section={section}
          onSection={setSection}
        />
        {git && (
          <Pane id="deploy-key" active={section === 'deploy-key'}>
            <ServiceDeployKey service={service} environmentProtected={environmentProtected} />
          </Pane>
        )}
        <Pane id="placement" active={section === 'placement'}>
          <ServicePlacement
            key={`${service.id}-${service.failover}-${service.allow_data_loss_failover}`}
            service={service}
            environmentProtected={environmentProtected}
            onChanged={onChanged}
          />
        </Pane>
        <Pane id="danger" active={section === 'danger'}>
          <ServiceDelete
            service={service}
            environmentProtected={environmentProtected}
            base={base}
          />
        </Pane>
      </div>
    </div>
  )
}

export function ServicePage() {
  const params = useParams({ strict: false })
  const org = params.org ?? ''
  const projectSlug = params.project ?? ''
  const envSlug = params.env ?? ''
  const name = params.service ?? ''
  const tab: Tab = (tabs as readonly string[]).includes(params.tab ?? '')
    ? (params.tab as Tab)
    : 'overview'

  const scope = useScope(projectSlug, envSlug, name)
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const me = useMe()
  const serviceId = scope.service?.id ?? ''
  const [, setNotice] = useState<ReactNode>(null)
  // A change made on this page shows the banner at once, before the server reports it.
  const [localPending, setLocalPending] = useState(false)
  const [dialog, setDialog] = useState<'deploy' | 'rollback' | 'move' | null>(null)
  const releases = useReleases(serviceId)
  const wanted = (useSearch({ strict: false }) as { dialog?: 'deploy' | 'rollback' }).dialog

  const live = useQuery({
    queryKey: ['service', serviceId],
    enabled: Boolean(serviceId),
    queryFn: () =>
      unwrap(api.GET('/services/{service_id}', { params: { path: { service_id: serviceId } } })),
    refetchInterval: (query) => (query.state.data?.status === 'deploying' ? 3_000 : 10_000),
  })

  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['service', serviceId] }),
      queryClient.invalidateQueries({ queryKey: ['deployments', serviceId] }),
    ])
  }
  const restart = useMutation({
    meta: { success: 'service.restart_started' },
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/restart', { params: { path: { service_id: serviceId } } }),
      ),
    onSuccess: async () => {
      setNotice(t('service.restart_started'))
      await refresh()
    },
  })
  const stop = useMutation({
    meta: { success: 'service.stop_started' },
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/stop', { params: { path: { service_id: serviceId } } }),
      ),
    onSuccess: async () => {
      setNotice(t('service.stop_started'))
      await refresh()
    },
  })
  const start = useMutation({
    meta: { success: 'service.start_started' },
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/start', { params: { path: { service_id: serviceId } } }),
      ),
    onSuccess: async () => {
      setNotice(t('service.start_started'))
      await refresh()
    },
  })

  // The key "d" opens the deploy dialog. It never starts a deploy (navigation, section 5).
  // "r" and "l" open Releases and Logs, "[" and "]" go to the previous and next tab.
  const environmentForKey = scope.environment
  const keyAllowed =
    scope.state === 'ok' &&
    accessFor(me.data?.org_role, 'service:deploy', environmentForKey?.protected) === 'allow'
  const execKeyAccess = accessFor(me.data?.org_role, 'service:exec', environmentForKey?.protected)
  const rollbackKeyAccess = accessFor(
    me.data?.org_role,
    'service:rollback',
    environmentForKey?.protected,
  )
  useEffect(() => {
    if (!wanted || scope.state !== 'ok') return
    if (wanted === 'deploy' && keyAllowed) setDialog('deploy')
    if (wanted === 'rollback' && rollbackKeyAccess === 'allow') setDialog('rollback')
    void navigate({ to: '.', search: {}, replace: true })
  }, [wanted, scope.state, keyAllowed, rollbackKeyAccess, navigate])
  const goTab = (id: Tab) =>
    void navigate({
      to: '/o/$org/p/$project/e/$env/s/$service/$tab',
      params: { org, project: projectSlug, env: envSlug, service: name, tab: id },
    })
  useSingleKey((event) => {
    if (scope.state !== 'ok') return false
    if (event.key === 'd' && keyAllowed) {
      setDialog('deploy')
      return true
    }
    if (event.key === 'r') {
      goTab('releases')
      return true
    }
    if (event.key === 'l') {
      goTab('logs')
      return true
    }
    if (event.key === '[' || event.key === ']') {
      const shown = tabs.filter((id) => id !== 'terminal' || execKeyAccess !== 'hidden')
      const at = shown.indexOf(tab)
      const next = shown[(at + (event.key === ']' ? 1 : -1) + shown.length) % shown.length]
      goTab(next)
      return true
    }
    return false
  })

  if (scope.state === 'pending') return <Skeleton />
  if (scope.state === 'error') return <ErrorBanner error={scope.error} onRetry={scope.retry} />
  if (scope.state === 'notfound' || !scope.service || !scope.environment) return <NotFoundPage />

  const service = live.data ?? scope.service
  const environment = scope.environment
  const role = effectiveRole(me.data, environment.project_id)
  const deployAccess = accessFor(role, 'service:deploy', environment.protected)
  const updateAccess = accessFor(role, 'service:update', environment.protected)
  const busy = service.status === 'deploying'
  const protectedReason = t('common.needs_admin')
  const busyReason = t('service.busy')

  const rollbackAccess = accessFor(role, 'service:rollback', environment.protected)
  const moveAccess = accessFor(role, 'service:move', environment.protected)
  const restartAccess = accessFor(role, 'service:restart')
  const previous = releases.data?.items?.find((r) => r.status === 'previous')
  const rollbackReason =
    rollbackAccess === 'blocked'
      ? protectedReason
      : busy
        ? busyReason
        : previous
          ? undefined
          : t('service.no_previous')

  const mutationError = [restart, stop, start].find((m) => m.error)?.error
  const conflict = mutationError instanceof ApiError && mutationError.status === 409

  const actions = (
    <>
      {deployAccess !== 'hidden' && (
        <Button
          variant="primary"
          aria-keyshortcuts="d"
          reason={deployAccess === 'blocked' ? protectedReason : busy ? busyReason : undefined}
          onClick={() => {
            setNotice(null)
            setDialog('deploy')
          }}
        >
          {environment.kind === 'production' ? t('service.deploy_production') : t('service.deploy')}
          <KeyHint k="d" />
        </Button>
      )}
      {rollbackAccess !== 'hidden' && (
        <Button
          reason={rollbackReason}
          onClick={() => {
            setNotice(null)
            setDialog('rollback')
          }}
        >
          {t('service.rollback')}
        </Button>
      )}
      {moveAccess !== 'hidden' && (service.placements ?? []).length > 0 && (
        <Button
          reason={moveAccess === 'blocked' ? protectedReason : busy ? busyReason : undefined}
          onClick={() => {
            setNotice(null)
            setDialog('move')
          }}
        >
          {t('service.move')}
        </Button>
      )}
      {restartAccess !== 'hidden' && (
        <Button
          loading={restart.isPending}
          reason={busy ? busyReason : undefined}
          onClick={() => {
            setNotice(null)
            restart.mutate()
          }}
        >
          {t('service.restart')}
        </Button>
      )}
      {updateAccess !== 'hidden' &&
        (service.desired_state === 'stopped' ? (
          <Button
            loading={start.isPending}
            reason={updateAccess === 'blocked' ? protectedReason : busy ? busyReason : undefined}
            onClick={() => {
              setNotice(null)
              start.mutate()
            }}
          >
            {t('service.start')}
          </Button>
        ) : (
          <Button
            loading={stop.isPending}
            reason={updateAccess === 'blocked' ? protectedReason : busy ? busyReason : undefined}
            onClick={() => {
              setNotice(null)
              stop.mutate()
            }}
          >
            {t('service.stop')}
          </Button>
        ))}
    </>
  )

  const base = { org, project: projectSlug, env: envSlug, service: name }
  const execAccess = accessFor(role, 'service:exec', environment.protected)
  const visibleTabs = tabs.filter((id) => id !== 'terminal' || execAccess !== 'hidden')
  const changed = () => setLocalPending(true)
  return (
    <section
      className={
        environment.kind === 'production' || environment.protected
          ? 'border-t-4 border-danger pt-4'
          : ''
      }
    >
      <PageHeader
        title={name}
        context={[environment.name ?? '', scope.project?.name ?? '']}
        breadcrumb={
          <Link
            to="/o/$org/p/$project/e/$env"
            params={{ org, project: projectSlug, env: envSlug }}
            className="text-primary underline"
          >
            {scope.project?.name} › {environment.name}
          </Link>
        }
        badges={
          <>
            <StatusBadge
              tone={serviceTone(service.status)}
              label={tState('status.service', service.status)}
            />
            <EnvironmentBadges kind={environment.kind} isProtected={environment.protected} />
            <BackupBadge service={service} />
          </>
        }
        actions={actions}
      />
      <div className="mb-4 space-y-3">
        {(service.pending_changes || localPending) && (
          <Banner
            variant="warning"
            title={t('service.pending_title')}
            action={
              deployAccess === 'allow' ? (
                <Button
                  reason={busy ? busyReason : undefined}
                  onClick={() => {
                    setNotice(null)
                    setDialog('deploy')
                  }}
                >
                  {t('service.redeploy')}
                </Button>
              ) : undefined
            }
          >
            {t('service.pending_text')}
          </Banner>
        )}
        {mutationError && (
          <ErrorBanner
            error={mutationError}
            title={conflict ? t('service.conflict') : t('service.action_failed')}
          />
        )}
        {live.isError && <ErrorBanner error={live.error} onRetry={() => void live.refetch()} />}
      </div>
      <nav aria-label={t('service.tabs')} className="mb-4">
        <ul className="flex gap-1 overflow-x-auto border-b border-border-strong">
          {visibleTabs.map((id) => (
            <li key={id}>
              <Link
                to="/o/$org/p/$project/e/$env/s/$service/$tab"
                params={{ ...base, tab: id }}
                aria-current={id === tab ? 'page' : undefined}
                className="block min-h-11 whitespace-nowrap border-b-4 border-transparent px-4 py-2 text-muted transition-colors duration-[200ms] hover:text-text aria-[current=page]:border-primary aria-[current=page]:font-semibold aria-[current=page]:text-text"
              >
                {t(`service.tab.${id}`)}
              </Link>
            </li>
          ))}
        </ul>
      </nav>
      <div key={tab} className="enter">
        {tab === 'overview' && (
          <>
            <div className="mb-4">
              <FailoverBanner service={service} />
            </div>
            <Overview service={service} serviceId={serviceId} base={base} />
          </>
        )}
        {tab === 'deployments' && <Deployments serviceId={serviceId} base={base} />}
        {tab === 'releases' && (
          <ServiceReleases
            service={service}
            environment={environment}
            base={base}
            onDeploy={() => setDialog('deploy')}
          />
        )}
        {tab === 'domains' && (
          <ServiceDomains service={service} environmentProtected={environment.protected} />
        )}
        {tab === 'env' && scope.project && (
          <ServiceVariables
            project={scope.project}
            environment={environment}
            service={service}
            onChanged={changed}
          />
        )}
        {tab === 'volumes' && (
          <ServiceStorage service={service} environmentProtected={environment.protected} />
        )}
        {tab === 'health' && (
          <ServiceHealth
            key={service.id}
            service={service}
            environmentProtected={environment.protected}
            onChanged={changed}
          />
        )}
        {tab === 'backups' && (
          <ServiceBackups service={service} environmentProtected={environment.protected} />
        )}
        {tab === 'logs' && (
          <Suspense fallback={<Skeleton />}>
            <ServiceLogs serviceId={serviceId} />
          </Suspense>
        )}
        {tab === 'metrics' && (
          <Suspense fallback={<Skeleton />}>
            <MetricsPanel
              kind="service"
              id={serviceId}
              markers={(releases.data?.items ?? [])
                .filter((r) => r.created_at)
                .map((r) => ({
                  time: Math.floor(new Date(r.created_at ?? '').getTime() / 1000),
                  label: `v${r.number}`,
                  onOpen: () =>
                    void navigate({
                      to: '/o/$org/p/$project/e/$env/s/$service/$tab',
                      params: { ...base, tab: 'releases' },
                    }),
                }))}
            />
          </Suspense>
        )}
        {tab === 'terminal' && (
          <Suspense fallback={<Skeleton />}>
            <ServiceTerminal service={service} environmentProtected={environment.protected} />
          </Suspense>
        )}
        {tab === 'settings' && (
          <SettingsTab
            service={service}
            environmentProtected={environment.protected}
            base={base}
            onChanged={changed}
          />
        )}
      </div>
      {dialog === 'deploy' && (
        <DeployDialog
          service={service}
          environment={environment}
          onClose={() => setDialog(null)}
          onDeployed={() => setLocalPending(false)}
        />
      )}
      {dialog === 'move' && <MoveDialog service={service} onClose={() => setDialog(null)} />}
      {dialog === 'rollback' && previous?.id && (
        <RollbackDialog
          service={service}
          environment={environment}
          releaseId={previous.id}
          onClose={() => setDialog(null)}
          onDeployInstead={() => setDialog('deploy')}
        />
      )}
    </section>
  )
}
