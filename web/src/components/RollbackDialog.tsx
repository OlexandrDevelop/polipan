// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { Banner, Button, EnvironmentBadges, Skeleton, TextField } from '@/components/ui'
import { hasKey, t } from '@/i18n'
import type { Environment, Service } from '@/scope'

const MAX_CHANGES = 12

function show(value: unknown): string {
  if (value === undefined || value === null) return '-'
  return typeof value === 'string' ? value : JSON.stringify(value)
}

// UI-09: what a rollback restores and what it does not, then the confirm. The server has no
// dry run for rollbackService, so the list comes from getRelease and diffReleases.
export function RollbackDialog({
  service,
  environment,
  releaseId,
  onClose,
  onDeployInstead,
}: {
  service: Service
  environment: Environment
  releaseId: string
  onClose: () => void
  // Offered when the image of the release is gone: deploy from its commit instead.
  onDeployInstead?: () => void
}) {
  const params = useParams({ strict: false })
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [key] = useState(() => crypto.randomUUID())
  const [typed, setTyped] = useState('')
  const liveId = service.live_release_id

  const release = useQuery({
    queryKey: ['release', releaseId],
    queryFn: () =>
      unwrap(api.GET('/releases/{release_id}', { params: { path: { release_id: releaseId } } })),
  })
  const diff = useQuery({
    queryKey: ['release-diff', liveId, releaseId],
    enabled: Boolean(liveId) && liveId !== releaseId,
    queryFn: () =>
      unwrap(
        api.GET('/releases/{release_id}/diff/{other_release_id}', {
          params: { path: { release_id: liveId ?? '', other_release_id: releaseId } },
        }),
      ),
  })

  const rollback = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/rollback', {
          params: { path: { service_id: service.id ?? '' }, header: { 'Idempotency-Key': key } },
          body: { release_id: releaseId },
        }),
      ),
    onSuccess: async (deployment) => {
      await Promise.all(
        ['service', 'deployments', 'releases'].map((k) =>
          queryClient.invalidateQueries({ queryKey: [k] }),
        ),
      )
      if (deployment.id && params.org && params.project && params.env && params.service) {
        await navigate({
          to: '/o/$org/p/$project/e/$env/s/$service/deployments/$deployment',
          params: {
            org: params.org,
            project: params.project,
            env: params.env,
            service: params.service,
            deployment: deployment.id,
          },
        })
      }
      onClose()
    },
  })

  const r = release.data
  const pruned = r?.status === 'pruned'
  const needsTyped = Boolean(environment.protected)
  const typedOk = !needsTyped || typed === environment.name
  const code = rollback.error instanceof ApiError ? rollback.error.code : ''
  const errorKey = `rollback.error.${code}`
  const changes = diff.data?.changes ?? []

  let reason: string | undefined
  if (!r) reason = t('rollback.loading')
  else if (pruned) reason = t('rollback.pruned_reason')
  else if (r.status === 'live') reason = t('rollback.already_live')
  else if (!typedOk) reason = t('common.type_first')

  return (
    <Modal
      title={t('rollback.title', { service: service.name ?? '', number: r?.number ?? '' })}
      onClose={onClose}
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (!reason) rollback.mutate()
        }}
      >
        <div className="flex flex-wrap gap-2">
          <EnvironmentBadges kind={environment.kind} isProtected={environment.protected} />
        </div>
        {release.isPending && <Skeleton />}
        {release.isError && (
          <ErrorBanner error={release.error} onRetry={() => void release.refetch()} />
        )}
        {r && (
          <>
            <p>{t('rollback.text', { number: r.number ?? '' })}</p>
            {pruned ? (
              <Banner
                variant="warning"
                title={t('rollback.pruned_title')}
                action={
                  onDeployInstead && (
                    <Button onClick={onDeployInstead}>{t('rollback.deploy_instead')}</Button>
                  )
                }
              >
                {t('rollback.pruned_text')}
              </Banner>
            ) : (
              <Banner>
                {r.fast_rollback_possible ? t('rollback.fast') : t('rollback.rebuild')}
              </Banner>
            )}
            <dl className="space-y-2 text-sm">
              <div>
                <dt className="font-semibold">{t('rollback.restored')}</dt>
                <dd className="text-muted">{t('rollback.restored_list')}</dd>
              </div>
              <div>
                <dt className="font-semibold">{t('rollback.not_restored')}</dt>
                <dd className="text-muted">{t('rollback.not_restored_list')}</dd>
              </div>
            </dl>
            {diff.isPending && diff.fetchStatus !== 'idle' && <Skeleton lines={2} />}
            {diff.isError && <ErrorBanner error={diff.error} onRetry={() => void diff.refetch()} />}
            {changes.length > 0 && (
              <div className="space-y-1">
                <h3 className="text-sm font-semibold">{t('rollback.changes')}</h3>
                <ul className="space-y-1 break-words font-mono text-xs">
                  {changes.slice(0, MAX_CHANGES).map((c, i) => (
                    <li key={`${c.path}-${i}`}>
                      {c.path}: {show(c.before)} → {show(c.after)}
                    </li>
                  ))}
                </ul>
                {changes.length > MAX_CHANGES && (
                  <p className="text-sm text-muted">
                    {t('rollback.more', { count: changes.length - MAX_CHANGES })}
                  </p>
                )}
              </div>
            )}
          </>
        )}
        {needsTyped && (
          <TextField
            label={t('common.type_to_confirm', { name: environment.name ?? '' })}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
          />
        )}
        {rollback.error && (
          <ErrorBanner
            error={rollback.error}
            title={hasKey(errorKey) ? t(errorKey) : t('rollback.failed')}
          />
        )}
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type="submit" variant="destructive" loading={rollback.isPending} reason={reason}>
            {t('rollback.confirm', { number: r?.number ?? '' })}
          </Button>
        </div>
      </form>
    </Modal>
  )
}
