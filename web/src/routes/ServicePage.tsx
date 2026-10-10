// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { ReactNode } from 'react'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import {
  Banner,
  Button,
  Card,
  EmptyState,
  EnvironmentBadges,
  Skeleton,
  StatusBadge,
} from '@/components/ui'
import { t, tState } from '@/i18n'
import { accessFor } from '@/permissions'
import { useMe } from '@/auth'
import { NotFoundPage } from '@/routes/ErrorPages'
import { ServiceDomains } from '@/routes/ServiceDomains'
import { ServiceLogs } from '@/routes/ServiceLogs'
import { useNodes, useScope } from '@/scope'
import type { Service } from '@/scope'
import { deploymentTone, isDeploymentActive, serviceTone } from '@/status'

const tabs = ['overview', 'deployments', 'domains', 'logs'] as const
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

function Overview({ service, serviceId }: { service: Service; serviceId: string }) {
  const deployments = useDeployments(serviceId)
  const nodes = useNodes()
  const domains = useQuery({
    queryKey: ['domains', serviceId],
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/domains', { params: { path: { service_id: serviceId } } }),
      ),
  })
  const latest = deployments.data?.items?.[0]
  const nodeName = (id?: string) => nodes.data?.find((n) => n.id === id)?.name ?? id ?? '-'
  const config = service.config ?? {}

  return (
    <div className="space-y-4">
      {latest?.state === 'failed' && (
        <Banner variant="danger" title={t('deployment.failed_title')}>
          <p>{tState('deployment.reason', latest.failure_reason)}</p>
          {latest.failure_detail && <p className="text-muted">{latest.failure_detail}</p>}
          <p>{t('deployment.failed_next')}</p>
        </Banner>
      )}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Card className="space-y-1">
          <h2 className="font-semibold">{t('service.source')}</h2>
          <p className="break-all font-mono text-sm">{String(config.image ?? '-')}</p>
          <p className="text-sm text-muted">
            {t('service.port')}: {config.port ? String(config.port) : '-'}
          </p>
        </Card>
        <Card className="space-y-1">
          <h2 className="font-semibold">{t('service.release')}</h2>
          <p className="break-all text-sm">{service.live_release_id ?? t('service.no_release')}</p>
          <p className="text-sm text-muted">
            {t('service.desired')}: {tState('status.desired', service.desired_state)}
          </p>
        </Card>
        <Card className="space-y-1">
          <h2 className="font-semibold">{t('service.replicas')}</h2>
          {(service.placements ?? []).length === 0 && (
            <p className="text-muted">{t('service.no_placements')}</p>
          )}
          <ul className="space-y-1 text-sm">
            {(service.placements ?? []).map((p) => (
              <li key={p.id}>
                {nodeName(p.node_id)} · {tState('status.role', p.role)} ·{' '}
                {tState('status.health', p.health)}
              </li>
            ))}
          </ul>
        </Card>
        <Card className="space-y-1 sm:col-span-2 lg:col-span-3">
          <h2 className="font-semibold">{t('service.urls')}</h2>
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
        </Card>
      </div>
    </div>
  )
}

function Deployments({ serviceId }: { serviceId: string }) {
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
          cell: (d) => <Time value={d.started_at} />,
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

// UI-06 (basic): state and control of one service, with the tabs of M1.
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
  const me = useMe()
  const serviceId = scope.service?.id ?? ''
  const [notice, setNotice] = useState<ReactNode>(null)

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
  const deploy = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/deploy', {
          params: {
            path: { service_id: serviceId },
            header: { 'Idempotency-Key': crypto.randomUUID() },
          },
          body: { dry_run: false },
        }),
      ),
    onSuccess: async () => {
      setNotice(t('service.deploy_started'))
      await refresh()
    },
  })
  const stop = useMutation({
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
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/start', { params: { path: { service_id: serviceId } } }),
      ),
    onSuccess: async () => {
      setNotice(t('service.start_started'))
      await refresh()
    },
  })

  if (scope.state === 'pending') return <Skeleton />
  if (scope.state === 'error') return <ErrorBanner error={scope.error} onRetry={scope.retry} />
  if (scope.state === 'notfound' || !scope.service || !scope.environment) return <NotFoundPage />

  const service = live.data ?? scope.service
  const environment = scope.environment
  const role = me.data?.org_role
  const deployAccess = accessFor(role, 'service:deploy', environment.protected)
  const updateAccess = accessFor(role, 'service:update', environment.protected)
  const busy = service.status === 'deploying'
  const protectedReason = t('common.needs_admin')
  const busyReason = t('service.busy')

  const mutationError = [deploy, stop, start].find((m) => m.error)?.error
  const conflict = mutationError instanceof ApiError && mutationError.status === 409

  const actions = (
    <>
      {deployAccess !== 'hidden' && (
        <Button
          variant="primary"
          loading={deploy.isPending}
          reason={deployAccess === 'blocked' ? protectedReason : busy ? busyReason : undefined}
          onClick={() => {
            setNotice(null)
            deploy.mutate()
          }}
        >
          {environment.kind === 'production' ? t('service.deploy_production') : t('service.deploy')}
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
          </>
        }
        actions={actions}
      />
      <div className="mb-4 space-y-3">
        {environment.protected && (
          <Banner variant="warning">{t('service.protected_banner')}</Banner>
        )}
        {notice && <Banner>{notice}</Banner>}
        {mutationError && (
          <ErrorBanner
            error={mutationError}
            title={conflict ? t('service.conflict') : t('service.action_failed')}
          />
        )}
        {live.isError && <ErrorBanner error={live.error} onRetry={() => void live.refetch()} />}
      </div>
      <nav aria-label={t('service.tabs')} className="mb-4">
        <ul className="flex gap-1 overflow-x-auto border-b border-border">
          {tabs.map((id) => (
            <li key={id}>
              <Link
                to="/o/$org/p/$project/e/$env/s/$service/$tab"
                params={{ ...base, tab: id }}
                aria-current={id === tab ? 'page' : undefined}
                className="block min-h-11 whitespace-nowrap border-b-4 border-transparent px-4 py-2 text-muted hover:text-text aria-[current=page]:border-primary aria-[current=page]:font-semibold aria-[current=page]:text-text"
              >
                {t(`service.tab.${id}`)}
              </Link>
            </li>
          ))}
        </ul>
      </nav>
      {tab === 'overview' && <Overview service={service} serviceId={serviceId} />}
      {tab === 'deployments' && <Deployments serviceId={serviceId} />}
      {tab === 'domains' && (
        <ServiceDomains service={service} environmentProtected={environment.protected} />
      )}
      {tab === 'logs' && <ServiceLogs serviceId={serviceId} />}
    </section>
  )
}
