// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { TypedConfirmDialog } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import { Banner, Button, Card, EnvironmentBadges, Skeleton, StatusBadge } from '@/components/ui'
import { t, tState } from '@/i18n'
import { useAccess } from '@/permissions'
import { NotFoundPage } from '@/routes/ErrorPages'
import { PreviewState, PreviewTitle, useDeletePreview, usePreviews } from '@/routes/Previews'
import { useProjects, useServices } from '@/scope'
import type { Service } from '@/scope'
import { serviceTone } from '@/status'

function Redeploy({ service }: { service: Service }) {
  const queryClient = useQueryClient()
  const [key] = useState(() => crypto.randomUUID())
  const [started, setStarted] = useState(false)
  const deploy = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/deploy', {
          params: { path: { service_id: service.id ?? '' }, header: { 'Idempotency-Key': key } },
          body: { dry_run: false },
        }),
      ),
    onSuccess: async () => {
      setStarted(true)
      await queryClient.invalidateQueries({ queryKey: ['services'] })
    },
  })
  return (
    <span className="flex flex-col gap-1">
      <Button loading={deploy.isPending} onClick={() => deploy.mutate()}>
        {t('previews.redeploy')}
        <span className="sr-only"> {service.name}</span>
      </Button>
      {started && <span className="text-sm text-muted">{t('previews.redeploy_started')}</span>}
      {deploy.error && <ErrorBanner error={deploy.error} title={t('previews.redeploy_failed')} />}
    </span>
  )
}

const EXTEND_STEPS = [1, 7]

// Moves the expiry of a preview by some days (updateEnvironment, settings.extend_days).
function ExtendExpiry({ environmentId, projectId }: { environmentId: string; projectId?: string }) {
  const queryClient = useQueryClient()
  const extend = useMutation({
    mutationFn: (days: number) =>
      unwrap(
        api.PATCH('/environments/{environment_id}', {
          params: { path: { environment_id: environmentId } },
          // The generated type of settings is empty, the server accepts extend_days (1 to 365).
          body: { settings: { extend_days: days } as unknown as Record<string, never> },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['environment', environmentId] })
      await queryClient.invalidateQueries({ queryKey: ['previews', projectId] })
    },
  })
  return (
    <div className="mt-2 space-y-2">
      <div role="group" aria-label={t('previews.extend')} className="flex flex-wrap gap-2">
        {EXTEND_STEPS.map((days) => (
          <Button key={days} loading={extend.isPending} onClick={() => extend.mutate(days)}>
            {days === 1 ? t('previews.extend_day') : t('previews.extend_days', { days })}
          </Button>
        ))}
      </div>
      {extend.error && <ErrorBanner error={extend.error} title={t('previews.extend_failed')} />}
    </div>
  )
}

// UI-36: one preview environment.
export function PreviewDetailPage() {
  const {
    org,
    project: projectSlug,
    env: envSlug,
  } = useParams({ from: '/o/$org/p/$project/previews/$env' })
  const navigate = useNavigate()
  const projects = useProjects()
  const project = projects.data?.find((p) => p.slug === projectSlug)
  const previews = usePreviews(project?.id)
  const listed = previews.data?.find((e) => e.slug === envSlug)
  // The environment itself has the newest state.
  const environment = useQuery({
    queryKey: ['environment', listed?.id],
    enabled: Boolean(listed?.id),
    refetchInterval: 10000,
    queryFn: () =>
      unwrap(
        api.GET('/environments/{environment_id}', {
          params: { path: { environment_id: listed?.id ?? '' } },
        }),
      ),
  })
  const env = environment.data ?? listed
  const services = useServices(env?.id)
  const allowed = useAccess('preview:manage', false, project?.id) === 'allow'
  const [deleting, setDeleting] = useState(false)
  const remove = useDeletePreview(project?.id, () => {
    void navigate({ to: '/o/$org/previews', params: { org }, search: { project: projectSlug } })
  })

  if (projects.isPending || (project && previews.isPending)) return <Skeleton />
  if (projects.isError) {
    return <ErrorBanner error={projects.error} onRetry={() => void projects.refetch()} />
  }
  if (previews.isError) {
    return <ErrorBanner error={previews.error} onRetry={() => void previews.refetch()} />
  }
  if (!project || !env) return <NotFoundPage />
  const url = env.preview?.url

  return (
    <section className="space-y-6">
      <PageHeader
        title={t('previews.detail_title', { name: env.name ?? '' })}
        breadcrumb={
          <Link
            to="/o/$org/previews"
            params={{ org }}
            search={{ project: projectSlug }}
            className="text-primary underline"
          >
            {t('previews.title')}
          </Link>
        }
        badges={<EnvironmentBadges kind={env.kind} />}
        actions={
          <>
            {url && (
              <a
                href={url}
                target="_blank"
                rel="noreferrer noopener"
                className="inline-flex min-h-8 items-center rounded-sm border border-border-strong px-3 py-1 text-sm coarse:min-h-11"
              >
                {t('previews.open_url')}
              </a>
            )}
            {allowed && (
              <Button variant="destructive" onClick={() => setDeleting(true)}>
                {t('common.delete')}
              </Button>
            )}
          </>
        }
      />
      {env.preview?.state === 'failed' && (
        <Banner variant="danger" title={t('previews.failed_title')}>
          {env.preview.failure_reason ? (
            <p className="break-words">{env.preview.failure_reason}</p>
          ) : (
            t('previews.failed_text')
          )}
        </Banner>
      )}
      {env.preview?.waiting_for_label && (
        <Banner variant="warning">{t('previews.waiting_label')}</Banner>
      )}
      <Card>
        <dl className="grid gap-3 text-sm sm:grid-cols-2">
          <div>
            <dt className="font-semibold">{t('previews.col_pr')}</dt>
            <dd>
              <PreviewTitle env={env} />
            </dd>
          </div>
          <div>
            <dt className="font-semibold">{t('previews.col_state')}</dt>
            <dd>
              <PreviewState env={env} />
            </dd>
          </div>
          <div>
            <dt className="font-semibold">{t('previews.col_url')}</dt>
            <dd className="break-all">{url ?? '-'}</dd>
          </div>
          <div>
            <dt className="font-semibold">{t('previews.col_expires')}</dt>
            <dd>
              <Time value={env.preview?.expires_at} />
              {allowed && env.id && <ExtendExpiry environmentId={env.id} projectId={project.id} />}
            </dd>
          </div>
        </dl>
      </Card>
      <div className="space-y-2">
        <h2 className="text-lg font-semibold">{t('previews.services')}</h2>
        {services.isPending && <Skeleton />}
        {services.isError && (
          <ErrorBanner error={services.error} onRetry={() => void services.refetch()} />
        )}
        {services.data &&
          (services.data.length === 0 ? (
            <p className="text-muted">{t('previews.no_services')}</p>
          ) : (
            <DataList
              caption={t('previews.services_caption')}
              rows={services.data}
              rowKey={(s) => s.id ?? ''}
              columns={[
                { header: t('previews.col_service'), primary: true, cell: (s) => s.name },
                {
                  header: t('previews.col_state'),
                  cell: (s) => (
                    <StatusBadge
                      tone={serviceTone(s.status)}
                      label={tState('status.service', s.status)}
                    />
                  ),
                },
              ]}
              actionsHeader={t('common.actions')}
              actions={(s) => allowed && <Redeploy service={s} />}
            />
          ))}
      </div>
      {deleting && (
        <TypedConfirmDialog
          title={t('previews.delete_title', { name: env.name ?? '' })}
          text={t('previews.delete_text')}
          confirmLabel={t('previews.delete_confirm')}
          typed={env.name ?? ''}
          busy={remove.isPending}
          error={remove.error ? <ErrorBanner error={remove.error} /> : undefined}
          onConfirm={() => remove.mutate(env.id ?? '')}
          onClose={() => {
            remove.reset()
            setDeleting(false)
          }}
        />
      )}
    </section>
  )
}
