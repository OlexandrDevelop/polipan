// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { Banner, Button, CheckboxField, Skeleton } from '@/components/ui'
import { hasKey, t, tState } from '@/i18n'
import { useNodes } from '@/scope'
import type { Environment, Service } from '@/scope'

type Plan = components['schemas']['DeployPlan']
type Warning = components['schemas']['Warning']

function warningText(w: Warning): string {
  const key = `plan.warning.${w.code ?? ''}`
  return hasKey(key) ? t(key) : (w.message ?? w.code ?? '')
}

// UI-07: the plan first (dry run), then the real deploy after the user confirms (REQ-UI-007).
export function DeployDialog({
  service,
  environment,
  onClose,
  onDeployed,
}: {
  service: Service
  environment: Environment
  onClose: () => void
  onDeployed?: () => void
}) {
  const params = useParams({ strict: false })
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const nodes = useNodes()
  const [noCache, setNoCache] = useState(false)
  // One key per opened dialog: a double click or a retry does not start two deployments.
  const [key] = useState(() => crypto.randomUUID())
  const git = service.source_type === 'git'
  const branch = ((service.config?.source ?? {}) as { branch?: string }).branch ?? 'main'
  const serviceId = service.id ?? ''

  const plan = useQuery({
    queryKey: ['deploy-plan', serviceId],
    gcTime: 0,
    staleTime: 0,
    retry: false,
    queryFn: async () =>
      (await unwrap(
        api.POST('/services/{service_id}/deploy', {
          params: { path: { service_id: serviceId } },
          body: { dry_run: true },
        }),
      )) as Plan,
  })

  const deploy = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/deploy', {
          params: { path: { service_id: serviceId }, header: { 'Idempotency-Key': key } },
          body: { dry_run: false, ...(noCache ? { no_cache: true } : {}) },
        }),
      ),
    onSuccess: async (result) => {
      const id = (result as { id?: string }).id
      onDeployed?.()
      await Promise.all(
        ['service', 'deployments'].map((k) => queryClient.invalidateQueries({ queryKey: [k] })),
      )
      if (id && params.org && params.project && params.env && params.service) {
        await navigate({
          to: '/o/$org/p/$project/e/$env/s/$service/deployments/$deployment',
          params: {
            org: params.org,
            project: params.project,
            env: params.env,
            service: params.service,
            deployment: id,
          },
        })
      }
      onClose()
    },
  })

  const nodeName = (id: string) => nodes.data?.find((n) => n.id === id)?.name ?? id
  const production = environment.kind === 'production'
  const p = plan.data
  const forcedWarning = p?.warnings?.find((w) => w.code === 'zero_downtime_not_available')

  return (
    <Modal title={t('deploy.title', { service: service.name ?? '' })} onClose={onClose}>
      <div className="space-y-4">
        {production && (
          <Banner variant="warning">
            {environment.protected ? t('deploy.protected') : t('deploy.production')}
          </Banner>
        )}
        {plan.isPending && <Skeleton />}
        {plan.isError && (
          <ErrorBanner
            error={plan.error}
            title={t('deploy.plan_failed')}
            onRetry={() => void plan.refetch()}
          />
        )}
        {p && (
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            <dt className="text-muted">{t('deploy.source')}</dt>
            <dd>
              {git ? t('deploy.commit_head', { branch }) : (service.config?.image as string) || '-'}
            </dd>
            <dt className="text-muted">{t('deploy.strategy')}</dt>
            <dd>
              {tState('deploy.strategy_value', p.strategy)}
              {p.forced && (
                <span className="block text-muted">
                  {forcedWarning ? warningText(forcedWarning) : t('deploy.forced')}
                </span>
              )}
            </dd>
            <dt className="text-muted">{t('deploy.downtime')}</dt>
            <dd>{tState('deploy.downtime_value', p.expected_downtime)}</dd>
            {(p.nodes ?? []).length > 0 && (
              <>
                <dt className="text-muted">{t('deploy.nodes')}</dt>
                <dd>{(p.nodes ?? []).map(nodeName).join(', ')}</dd>
              </>
            )}
          </dl>
        )}
        {(p?.warnings ?? [])
          .filter((w) => w !== forcedWarning)
          .map((w, i) => (
            <Banner key={`${w.code}-${i}`} variant="warning">
              {warningText(w)}
            </Banner>
          ))}
        {p && git && (
          <CheckboxField
            label={t('deploy.no_cache')}
            hint={t('deploy.no_cache_hint')}
            checked={noCache}
            onChange={(e) => setNoCache(e.target.checked)}
          />
        )}
        {deploy.error && <ErrorBanner error={deploy.error} title={t('deploy.failed')} />}
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button
            variant="primary"
            loading={deploy.isPending}
            reason={p ? undefined : t('deploy.plan_first')}
            onClick={() => deploy.mutate()}
          >
            {production ? t('service.deploy_production') : t('service.deploy')}
          </Button>
        </div>
      </div>
    </Modal>
  )
}
