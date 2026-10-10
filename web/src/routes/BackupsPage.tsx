// SPDX-License-Identifier: Apache-2.0

import { useQueries, useQuery } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import { Banner, EmptyState, Skeleton } from '@/components/ui'
import { t, tState } from '@/i18n'
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

  let body
  if (policies.isPending) body = <Skeleton />
  else if (policies.isError)
    body = <ErrorBanner error={policies.error} onRetry={() => void policies.refetch()} />
  else if (policies.data.length === 0)
    body = <EmptyState title={t('backup.empty_title')} text={t('backup.empty_text')} />
  else
    body = (
      <DataList
        caption={t('backup.all_caption')}
        rows={policies.data}
        rowKey={(p) => p.id ?? ''}
        columns={[
          {
            header: t('backup.col_what'),
            primary: true,
            cell: (p) =>
              p.control_plane ? (
                <span>
                  {t('backup.control_plane')}
                  {p.local_only && (
                    <span className="mt-1 block text-sm text-warning">
                      {t('backup.local_warning')}
                    </span>
                  )}
                </span>
              ) : (
                <span className="break-all">
                  {serviceName(p.service_id)} · {p.engine}
                </span>
              ),
          },
          {
            header: t('backup.schedule'),
            cell: (p) => <span className="font-mono">{p.schedule}</span>,
          },
          { header: t('backup.col_state'), cell: (p) => <VerifiedBadge policy={p} /> },
          {
            header: t('backup.col_last_run'),
            cell: (p) =>
              p.last_run ? (
                <span>
                  {tState('backup.run', p.last_run.state)} ·{' '}
                  <Time value={p.last_run.finished_at ?? p.last_run.started_at ?? undefined} />
                </span>
              ) : (
                <span className="text-muted">{t('backup.no_runs')}</span>
              ),
          },
        ]}
      />
    )

  return (
    <section>
      <PageHeader title={t('backup.page_title')} />
      <div className="space-y-4">
        <Banner>
          {t('backup.page_intro')}{' '}
          <Link
            to="/o/$org/settings/backup-targets"
            params={{ org }}
            className="text-primary underline"
          >
            {t('backup.open_targets')}
          </Link>
        </Banner>
        {body}
      </div>
    </section>
  )
}
