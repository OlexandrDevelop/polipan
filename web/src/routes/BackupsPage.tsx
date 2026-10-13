// SPDX-License-Identifier: Apache-2.0

import { useQueries, useQuery } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import { EmptyState, Skeleton } from '@/components/ui'
import { t, tState } from '@/i18n'
import { useProjects } from '@/scope'
import { VerifiedBadge } from '@/routes/ServiceBackups'

// The backups of the whole org (navigation: Backups): every policy with its state, the control-plane database first.
export function BackupsPage() {
  const { org } = useParams({ from: '/o/$org/backups' })
  const policies = useQuery({
    queryKey: ['backup-policies', 'all'],
    queryFn: () => unwrap(api.GET('/backup-policies')),
    refetchInterval: 15000,
  })
  const serviceIds = [
    ...new Set((policies.data ?? []).map((p) => p.service_id).filter(Boolean)),
  ] as string[]
  const services = useQueries({
    queries: serviceIds.map((id) => ({
      queryKey: ['service', id],
      queryFn: () =>
        unwrap(api.GET('/services/{service_id}', { params: { path: { service_id: id } } })),
    })),
  })
  const serviceName = (id?: string) =>
    services.find((s) => s.data?.id === id)?.data?.name ?? id ?? ''

  // The page of a service is found by slugs, so every project's environments are read once.
  const projects = useProjects()
  const envLists = useQueries({
    queries: (projects.data ?? []).map((p) => ({
      queryKey: ['environments', p.id],
      queryFn: () =>
        unwrap(
          api.GET('/projects/{project_id}/environments', {
            params: { path: { project_id: p.id ?? '' } },
          }),
        ),
    })),
  })
  const where = (serviceId?: string) => {
    const service = services.find((q) => q.data?.id === serviceId)?.data
    for (const [i, list] of envLists.entries()) {
      const env = list.data?.find((e) => e.id === service?.environment_id)
      const project = projects.data?.[i]
      if (env && project && service?.name) {
        return { project: project.slug ?? '', env: env.slug ?? '', service: service.name }
      }
    }
    return undefined
  }

  let body
  if (policies.isPending) body = <Skeleton />
  else if (policies.isError)
    body = <ErrorBanner error={policies.error} onRetry={() => void policies.refetch()} />
  else if (policies.data.length === 0)
    body = <EmptyState title={t('backup.empty_title')} text={t('backup.empty_text')} />
  else
    body = (
      <ul className="tiles" aria-label={t('backup.all_caption')}>
        {policies.data.map((p) => {
          const title = p.control_plane ? t('backup.control_plane') : serviceName(p.service_id)
          return (
            <li key={p.id}>
              <div className="tile tile-template">
                <span aria-hidden="true" className="tile-letter" data-hue={p.control_plane ? 1 : 0}>
                  {(title || '?').trim().charAt(0).toUpperCase()}
                </span>
                <h2 className="tile-name">{title}</h2>
                <p className="tile-slug">{p.control_plane ? 'polipan' : p.engine}</p>
                <p className="backup-schedule">
                  <span className="font-mono">{p.schedule}</span>
                </p>
                {p.local_only && <p className="backup-warn">{t('backup.local_warning')}</p>}
                <div className="tile-foot">
                  <VerifiedBadge policy={p} />
                  <p className="backup-last">
                    {p.last_run ? (
                      <>
                        {tState('backup.run', p.last_run.state)} ·{' '}
                        <Time
                          value={p.last_run.finished_at ?? p.last_run.started_at ?? undefined}
                        />
                      </>
                    ) : (
                      t('backup.no_runs')
                    )}
                  </p>
                  {p.control_plane ? (
                    <Link
                      to="/o/$org/settings/backup-targets"
                      params={{ org }}
                      className="tile-action"
                    >
                      {t('backup.open_targets')}
                    </Link>
                  ) : (
                    (() => {
                      const at = where(p.service_id)
                      return at ? (
                        <Link
                          to="/o/$org/p/$project/e/$env/s/$service/$tab"
                          params={{ org, ...at, tab: 'backups' }}
                          className="tile-action"
                        >
                          {t('common.edit')}
                        </Link>
                      ) : null
                    })()
                  )}
                </div>
              </div>
            </li>
          )
        })}
      </ul>
    )

  return (
    <section>
      <PageHeader
        title={t('backup.page_title')}
        actions={
          <Link to="/o/$org/settings/backup-targets" params={{ org }} className="btn-link">
            {t('backup.open_targets')}
          </Link>
        }
      />
      <div className="space-y-4">
        <p className="page-sub">{t('backup.page_intro')}</p>
        {body}
      </div>
    </section>
  )
}
