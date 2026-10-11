// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { Banner, Button, CheckboxField, SelectField, Skeleton, TextField } from '@/components/ui'
import { hasKey, t, tState } from '@/i18n'
import { useNodes } from '@/scope'
import type { Environment, Service } from '@/scope'
import { newKey } from '../idempotency'

type Plan = components['schemas']['DeployPlan']
type Warning = components['schemas']['Warning']
type Strategy = components['schemas']['Strategy']
type StrategyOptions = components['schemas']['StrategyOptions']

// The steps are percents of traffic, from small to large, for example "10, 25, 50, 100".
function parseSteps(text: string): number[] | null {
  const steps = text
    .split(/[,\s]+/)
    .filter(Boolean)
    .map(Number)
  if (steps.length === 0) return null
  const ok = steps.every(
    (n, i) => Number.isInteger(n) && n >= 1 && n <= 100 && (i === 0 || n > steps[i - 1]),
  )
  return ok ? steps : null
}

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
  // '' means the strategy the server chooses for this service.
  const [strategy, setStrategy] = useState<Strategy | ''>('')
  const [stepsText, setStepsText] = useState('10, 25, 50, 100')
  const [interval, setIntervalText] = useState('300')
  const [max5xx, setMax5xx] = useState('2')
  const [p95Ratio, setP95Ratio] = useState('1.5')
  // One key per opened dialog: a double click or a retry does not start two deployments.
  const [key] = useState(() => newKey())
  const git = service.source_type === 'git'
  const branch = ((service.config?.source ?? {}) as { branch?: string }).branch ?? 'main'
  const serviceId = service.id ?? ''

  const steps = parseSteps(stepsText)
  const canaryErrors = {
    steps: steps ? null : t('canary.err_steps'),
    interval:
      Number(interval) >= 1 && Number.isInteger(Number(interval)) ? null : t('canary.err_interval'),
    max5xx:
      Number(max5xx) >= 0 && max5xx !== '' && Number(max5xx) <= 100
        ? null
        : t('canary.err_percent'),
    ratio: Number(p95Ratio) >= 1 ? null : t('canary.err_ratio'),
  }
  const canaryInvalid = strategy === 'canary' && Object.values(canaryErrors).some(Boolean)
  const request = (): { strategy?: Strategy; strategy_options?: StrategyOptions } => {
    if (!strategy) return {}
    if (strategy !== 'canary' || !steps) return { strategy }
    // The generated type makes every option required because each has a default. Only the
    // canary options are sent, the server keeps its defaults for the rest.
    return {
      strategy,
      strategy_options: {
        canary_steps: steps,
        canary_step_interval_seconds: Number(interval),
        // The field is a ratio (0.02), the form asks for a percent.
        canary_max_5xx_rate: Number(max5xx) / 100,
        canary_max_p95_ratio: Number(p95Ratio),
      } as StrategyOptions,
    }
  }

  const plan = useQuery({
    queryKey: ['deploy-plan', serviceId, strategy],
    gcTime: 0,
    staleTime: 0,
    retry: false,
    queryFn: async () =>
      (await unwrap(
        api.POST('/services/{service_id}/deploy', {
          params: { path: { service_id: serviceId } },
          body: { dry_run: true, ...request() },
        }),
      )) as Plan,
  })

  const deploy = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/deploy', {
          params: { path: { service_id: serviceId }, header: { 'Idempotency-Key': key } },
          body: { dry_run: false, ...request(), ...(noCache ? { no_cache: true } : {}) },
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
        <SelectField
          label={t('deploy.strategy_choice')}
          value={strategy}
          onChange={(e) => setStrategy(e.target.value as Strategy | '')}
        >
          <option value="">{t('deploy.strategy_default')}</option>
          <option value="blue_green">{t('deploy.strategy_value.blue_green')}</option>
          <option value="recreate">{t('deploy.strategy_value.recreate')}</option>
          <option value="canary">{t('deploy.strategy_value.canary')}</option>
        </SelectField>
        {strategy === 'canary' && (
          <fieldset className="space-y-3">
            <legend className="text-sm font-medium">{t('canary.options')}</legend>
            <p className="text-sm text-muted">{t('canary.options_hint')}</p>
            <TextField
              label={t('canary.steps_label')}
              value={stepsText}
              error={canaryErrors.steps}
              onChange={(e) => setStepsText(e.target.value)}
            />
            <TextField
              label={t('canary.interval_label')}
              type="number"
              value={interval}
              error={canaryErrors.interval}
              onChange={(e) => setIntervalText(e.target.value)}
            />
            <TextField
              label={t('canary.max_5xx_label')}
              type="number"
              step="0.1"
              value={max5xx}
              error={canaryErrors.max5xx}
              onChange={(e) => setMax5xx(e.target.value)}
            />
            <TextField
              label={t('canary.p95_ratio_label')}
              type="number"
              step="0.1"
              value={p95Ratio}
              error={canaryErrors.ratio}
              onChange={(e) => setP95Ratio(e.target.value)}
            />
          </fieldset>
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
            reason={
              canaryInvalid ? t('canary.fix_options') : p ? undefined : t('deploy.plan_first')
            }
            onClick={() => deploy.mutate()}
          >
            {production ? t('service.deploy_production') : t('service.deploy')}
          </Button>
        </div>
      </div>
    </Modal>
  )
}
