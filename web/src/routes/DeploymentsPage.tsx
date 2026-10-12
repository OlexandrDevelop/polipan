// SPDX-License-Identifier: Apache-2.0

import { useQueries } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { CancelDeployment } from '@/components/CancelDeployment'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import { EmptyState, Optional, SelectField, Skeleton, StatusBadge } from '@/components/ui'
import { t, tState } from '@/i18n'
import { useEnvironments, useProjects } from '@/scope'
import type { Environment, Service } from '@/scope'
import { deploymentTone, isDeploymentActive } from '@/status'
import type { components } from '@api/schema'

type Deployment = components['schemas']['Deployment']

interface Row {
  deployment: Deployment
  service: Service
  env: Environment
}

const STATES = [
  'queued',
  'building',
  'image_ready',
  'starting',
  'health_checking',
  'shifting',
  'switching',
  'draining',
  'live',
  'failed',
  'rolled_back',
  'cancelled',
  'superseded',
]

// UI-34: recent deployments of the services of one project. The API has no list for the whole
// organization (screens, known gaps), so this merges the lists of each service, newest first.
export function DeploymentsPage() {
  const { org } = useParams({ from: '/o/$org' })
  const search = useSearch({ from: '/o/$org/deployments' })
  const navigate = useNavigate({ from: '/o/$org/deployments' })
  const projects = useProjects()
  const project = projects.data?.find((p) => p.slug === search.project) ?? projects.data?.[0]
  const environments = useEnvironments(project?.id)
  const shownEnvs = (environments.data ?? []).filter((e) => !search.env || e.slug === search.env)

  const serviceQueries = useQueries({
    queries: shownEnvs.map((env) => ({
      queryKey: ['services', env.id],
      queryFn: () =>
        unwrap(
          api.GET('/environments/{environment_id}/services', {
            params: { path: { environment_id: env.id ?? '' } },
          }),
        ),
    })),
  })
  const services = serviceQueries.flatMap((q) => q.data ?? [])
  const deploymentQueries = useQueries({
    queries: services.map((service) => ({
      queryKey: ['deployments', service.id, 'recent'],
      queryFn: () =>
        unwrap(
          api.GET('/services/{service_id}/deployments', {
            params: { path: { service_id: service.id ?? '' }, query: { limit: 30 } },
          }),
        ),
      refetchInterval: 15_000,
    })),
  })

  const all = [projects, environments, ...serviceQueries, ...deploymentQueries]
  const failed = all.find((q) => q.isError)
  const retry = () => {
    for (const q of all) if (q.isError) void q.refetch()
  }
  const pending = all.some((q) => q.isPending && q.fetchStatus !== 'idle')

  const rows: Row[] = []
  deploymentQueries.forEach((q, i) => {
    const service = services[i]
    const env = shownEnvs.find((e) => e.id === service.environment_id)
    if (!env) return
    for (const deployment of q.data?.items ?? []) rows.push({ deployment, service, env })
  })
  rows.sort((a, b) => (b.deployment.started_at ?? '').localeCompare(a.deployment.started_at ?? ''))
  const visible = rows.filter(
    (r) =>
      (!search.state || r.deployment.state === search.state) &&
      (!search.service || r.service.name === search.service),
  )

  const setSearch = (patch: Record<string, string | undefined>) =>
    void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true })

  return (
    <section>
      <PageHeader title={t('deployments.title')} />
      {projects.data?.length === 0 && (
        <EmptyState title={t('projects.empty_title')} text={t('projects.empty_text')} />
      )}
      {project && (
        <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {(projects.data?.length ?? 0) > 1 && (
            <SelectField
              label={t('deployments.project')}
              value={project.slug}
              onChange={(e) =>
                setSearch({ project: e.target.value, env: undefined, service: undefined })
              }
            >
              {projects.data?.map((p) => (
                <option key={p.id} value={p.slug}>
                  {p.name}
                </option>
              ))}
            </SelectField>
          )}
          <Optional filled={Boolean(search.env || search.service || search.state)}>
            <div className="grid gap-3 sm:grid-cols-3">
              <SelectField
                label={t('deployments.environment')}
                value={search.env ?? ''}
                onChange={(e) =>
                  setSearch({ env: e.target.value || undefined, service: undefined })
                }
              >
                <option value="">{t('deployments.all')}</option>
                {environments.data?.map((e) => (
                  <option key={e.id} value={e.slug}>
                    {e.name}
                  </option>
                ))}
              </SelectField>
              <SelectField
                label={t('deployments.service')}
                value={search.service ?? ''}
                onChange={(e) => setSearch({ service: e.target.value || undefined })}
              >
                <option value="">{t('deployments.all')}</option>
                {services.map((s) => (
                  <option key={s.id} value={s.name}>
                    {s.name}
                  </option>
                ))}
              </SelectField>
              <SelectField
                label={t('deployment.state')}
                value={search.state ?? ''}
                onChange={(e) => setSearch({ state: e.target.value || undefined })}
              >
                <option value="">{t('deployments.all')}</option>
                {STATES.map((s) => (
                  <option key={s} value={s}>
                    {tState('status.deployment', s)}
                  </option>
                ))}
              </SelectField>
            </div>
          </Optional>
        </div>
      )}
      {failed && <ErrorBanner error={failed.error} onRetry={retry} />}
      {!failed && pending && <Skeleton />}
      {!failed && !pending && project && visible.length === 0 && (
        <EmptyState title={t('deployments.empty_title')} text={t('deployments.empty_text')} />
      )}
      {visible.length > 0 && (
        <DataList
          caption={t('deployment.caption')}
          rows={visible}
          rowKey={(r) => r.deployment.id ?? ''}
          columns={[
            {
              header: t('deployment.started'),
              primary: true,
              cell: (r) => (
                <Link
                  to="/o/$org/p/$project/e/$env/s/$service/deployments/$deployment"
                  params={{
                    org,
                    project: project?.slug ?? '',
                    env: r.env.slug ?? '',
                    service: r.service.name ?? '',
                    deployment: r.deployment.id ?? '',
                  }}
                  className="text-primary underline"
                >
                  <Time value={r.deployment.started_at} />
                </Link>
              ),
            },
            { header: t('deployments.service'), cell: (r) => r.service.name },
            { header: t('deployments.environment'), cell: (r) => r.env.name },
            {
              header: t('deployment.state'),
              cell: (r) => (
                <StatusBadge
                  tone={deploymentTone(r.deployment.state)}
                  label={tState('status.deployment', r.deployment.state)}
                />
              ),
            },
            {
              header: t('deployment.trigger'),
              cell: (r) => tState('deployment.trigger_value', r.deployment.trigger),
              cardHidden: true,
            },
          ]}
          actions={(r) =>
            isDeploymentActive(r.deployment.state) ? (
              <CancelDeployment deployment={r.deployment} environmentProtected={r.env.protected} />
            ) : null
          }
        />
      )}
    </section>
  )
}
