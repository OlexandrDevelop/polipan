// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import {
  Banner,
  Button,
  CheckboxField,
  EnvironmentBadges,
  SelectField,
  Skeleton,
  StatusBadge,
  TextField,
} from '@/components/ui'
import { hasKey, t, tState } from '@/i18n'
import { accessFor, useRole } from '@/permissions'
import type { Environment, Project } from '@/scope'
import { newKey } from '../idempotency'

type Plan = components['schemas']['PromotionPlan']
type Deployment = components['schemas']['Deployment']
type Strategy = components['schemas']['Strategy']
type PlanItem = NonNullable<Plan['items']>[number]

export type PromoteSource =
  // One release of a service (UI-09 releases tab).
  | { kind: 'release'; releaseId: string; serviceName: string; releaseNumber?: number }
  // All services of the source environment.
  | { kind: 'environment' }

function show(value: unknown): string {
  if (value === undefined || value === null) return '-'
  return typeof value === 'string' ? value : JSON.stringify(value)
}

// The names of the missing variables, from a plan or from a 422 answer.
export function missingNames(plan: Plan | undefined): string[] {
  return [...new Set((plan?.items ?? []).flatMap((i) => i.missing_variables ?? []))]
}

type Step = 'plan' | 'result'

// The result of one service of a started promotion (flows, section 3).
export type ServiceResult = 'done' | 'failed' | 'not_started' | 'running'

export function resultOf(deployment: Deployment | undefined): ServiceResult {
  if (!deployment) return 'not_started'
  if (deployment.state === 'live') return 'done'
  if (['failed', 'rolled_back', 'cancelled', 'superseded'].includes(deployment.state ?? '')) {
    return 'failed'
  }
  return 'running'
}

const resultTone = {
  done: 'success',
  failed: 'danger',
  not_started: 'muted',
  running: 'info',
} as const

// UI-10: promote a release or a whole environment. The dry run comes first and shows the diff,
// the missing variables and the strategy. Confirm sends the hash of the plan that the user saw.
export function PromoteDialog({
  org,
  project,
  source,
  sourceEnvironment,
  environments,
  onClose,
}: {
  org: string
  project: Project
  source: PromoteSource
  sourceEnvironment: Environment
  environments: Environment[]
  onClose: () => void
}) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const role = useRole(project.id)
  // A preview is never a target. The source itself is no target either.
  const targets = environments.filter((e) => e.id !== sourceEnvironment.id && e.kind !== 'preview')
  const [targetId, setTargetId] = useState(targets[0]?.id ?? '')
  const [strategy, setStrategy] = useState<'' | Strategy>('')
  const [includeDatabases, setIncludeDatabases] = useState(false)
  const [typed, setTyped] = useState('')
  const [started, setStarted] = useState<Deployment[] | null>(null)
  const [step, setStep] = useState<Step>('plan')
  const target = targets.find((e) => e.id === targetId)

  const body = (dry: boolean, planHash?: string): components['schemas']['PromoteRequest'] => ({
    target_environment_id: targetId,
    dry_run: dry,
    create_missing: false,
    include_databases: source.kind === 'environment' && includeDatabases,
    ...(strategy ? { strategy } : {}),
    ...(planHash ? { plan_hash: planHash } : {}),
  })

  // One call for both kinds of source. The answer is a plan (dry run) or the started deployments.
  const send = async (request: components['schemas']['PromoteRequest'], key?: string) => {
    const header = key ? { 'Idempotency-Key': key } : {}
    if (source.kind === 'release') {
      return (await unwrap(
        api.POST('/releases/{release_id}/promote', {
          params: { path: { release_id: source.releaseId }, header },
          body: request,
        }),
      )) as unknown
    }
    return (await unwrap(
      api.POST('/environments/{environment_id}/promote', {
        params: { path: { environment_id: sourceEnvironment.id ?? '' }, header },
        body: request,
      }),
    )) as unknown
  }

  const plan = useQuery({
    queryKey: [
      'promote-plan',
      source.kind === 'release' ? source.releaseId : sourceEnvironment.id,
      targetId,
      strategy,
      includeDatabases,
    ],
    enabled: Boolean(targetId) && step === 'plan',
    retry: false,
    gcTime: 0,
    staleTime: 0,
    queryFn: async () => (await send(body(true))) as Plan,
  })
  const planData = plan.data
  const hash = planData?.plan_hash

  const confirm = useMutation({
    mutationFn: async () => (await send(body(false, hash), newKey())) as Deployment | Deployment[],
    onSuccess: async (answer) => {
      const list = Array.isArray(answer) ? answer : [answer]
      await Promise.all(
        ['services', 'service', 'deployments', 'releases'].map((k) =>
          queryClient.invalidateQueries({ queryKey: [k] }),
        ),
      )
      const items = (planData?.items ?? []).filter((i) => !i.skipped_reason)
      // One service started and fine: go to its deployment page in the target (flows, section 3).
      if (source.kind === 'release' && list[0]?.id && target?.slug && project.slug) {
        await navigate({
          to: '/o/$org/p/$project/e/$env/s/$service/deployments/$deployment',
          params: {
            org,
            project: project.slug,
            env: target.slug,
            service: items[0]?.service ?? source.serviceName,
            deployment: list[0].id,
          },
        })
        onClose()
        return
      }
      setStarted(list)
      setStep('result')
    },
    onError: (error) => {
      // The plan is out of date: read it again, so the user sees what changed.
      if (error instanceof ApiError && error.code === 'plan_changed') void plan.refetch()
      if (error instanceof ApiError && error.code === 'missing_variables') void plan.refetch()
    },
  })

  const missing = missingNames(planData)
  const items = planData?.items ?? []
  const runnable = items.filter((i) => !i.skipped_reason)
  const access = accessFor(role, 'service:promote', target?.protected)
  const needsTyped = Boolean(target?.protected)
  const typedOk = !needsTyped || typed === target?.name
  const error = confirm.error instanceof ApiError ? confirm.error : undefined
  const code = error?.code ?? ''
  const errorKey = `promote.error.${code}`
  const errorNames = Array.isArray(error?.details?.names) ? (error.details.names as string[]) : []

  let reason: string | undefined
  if (access === 'blocked') reason = t('common.needs_admin')
  else if (!target) reason = t('promote.pick_target')
  else if (!planData) reason = t('promote.loading')
  else if (runnable.length === 0) reason = t('promote.nothing')
  else if (missing.length > 0) reason = t('promote.blocked_missing')
  else if (!typedOk) reason = t('common.type_first')

  const title =
    source.kind === 'release'
      ? t('promote.title_release', {
          service: source.serviceName,
          number: source.releaseNumber ?? '',
        })
      : t('promote.title_environment', { name: sourceEnvironment.name ?? '' })

  if (step === 'result') {
    const done = started ?? []
    return (
      <Modal title={t('promote.result_title')} onClose={onClose}>
        <div className="space-y-4">
          <p>{t('promote.result_text', { name: target?.name ?? '' })}</p>
          <ul className="space-y-2" aria-label={t('promote.result_list')}>
            {runnable.map((item, i) => {
              const deployment = done[i]
              const result = resultOf(deployment)
              return (
                <li key={item.service} className="flex flex-wrap items-center gap-2">
                  <span className="font-semibold">{item.service}</span>
                  <StatusBadge
                    tone={resultTone[result]}
                    label={t(`promote.result.${result}` as const)}
                  />
                  {deployment?.id && target?.slug && project.slug && (
                    <Link
                      to="/o/$org/p/$project/e/$env/s/$service/deployments/$deployment"
                      params={{
                        org,
                        project: project.slug,
                        env: target.slug,
                        service: item.service ?? '',
                        deployment: deployment.id,
                      }}
                      className="text-primary underline"
                    >
                      {t('promote.open_deployment')}
                    </Link>
                  )}
                </li>
              )
            })}
          </ul>
          {done.length < runnable.length && (
            <Banner variant="warning">{t('promote.partial')}</Banner>
          )}
          <div className="flex justify-end">
            <Button variant="primary" onClick={onClose}>
              {t('common.close')}
            </Button>
          </div>
        </div>
      </Modal>
    )
  }

  return (
    <Modal title={title} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (!reason) confirm.mutate()
        }}
      >
        <div className="flex flex-wrap items-center gap-2">
          <span>{sourceEnvironment.name}</span>
          <span aria-hidden="true">→</span>
          <span className="sr-only">{t('promote.to')}</span>
          {target && <span className="font-semibold">{target.name}</span>}
          {target && <EnvironmentBadges kind={target.kind} isProtected={target.protected} />}
        </div>
        {targets.length === 0 ? (
          <Banner>{t('promote.no_targets')}</Banner>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2">
            <SelectField
              label={t('promote.target')}
              value={targetId}
              onChange={(e) => {
                setTargetId(e.target.value)
                setTyped('')
                confirm.reset()
              }}
            >
              {targets.map((e) => (
                <option key={e.id} value={e.id}>
                  {e.name}
                </option>
              ))}
            </SelectField>
            <SelectField
              label={t('promote.strategy')}
              value={strategy}
              onChange={(e) => setStrategy(e.target.value as '' | Strategy)}
            >
              <option value="">{t('promote.strategy_default')}</option>
              <option value="recreate">{tState('deploy.strategy_value', 'recreate')}</option>
              <option value="blue_green">{tState('deploy.strategy_value', 'blue_green')}</option>
              <option value="canary">{tState('deploy.strategy_value', 'canary')}</option>
            </SelectField>
          </div>
        )}
        {source.kind === 'environment' && (
          <CheckboxField
            label={t('promote.include_databases')}
            hint={t('promote.include_databases_hint')}
            checked={includeDatabases}
            onChange={(e) => setIncludeDatabases(e.target.checked)}
          />
        )}
        {plan.isFetching && !planData && <Skeleton />}
        {plan.isError && <ErrorBanner error={plan.error} onRetry={() => void plan.refetch()} />}
        {planData && (
          <PlanView plan={planData} missing={missing} onCheckAgain={() => void plan.refetch()} />
        )}
        {code === 'plan_changed' && (
          <Banner variant="warning" title={t('promote.error.plan_changed')}>
            {t('promote.plan_changed_text')}
          </Banner>
        )}
        {code === 'missing_variables' && (
          <Banner variant="danger" title={t('promote.error.missing_variables')}>
            {errorNames.join(', ')}
          </Banner>
        )}
        {error && code !== 'plan_changed' && code !== 'missing_variables' && (
          <ErrorBanner error={error} title={hasKey(errorKey) ? t(errorKey) : t('promote.failed')} />
        )}
        {needsTyped && access !== 'blocked' && (
          <TextField
            label={t('common.type_to_confirm', { name: target?.name ?? '' })}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
          />
        )}
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type="submit" variant="primary" loading={confirm.isPending} reason={reason}>
            {t('promote.confirm', { name: target?.name ?? '' })}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

function PlanView({
  plan,
  missing,
  onCheckAgain,
}: {
  plan: Plan
  missing: string[]
  onCheckAgain: () => void
}) {
  const items = plan.items ?? []
  return (
    <div className="space-y-4">
      {(plan.warnings ?? []).map((w, i) => (
        <Banner key={`${w.code}-${i}`} variant="warning">
          {w.message}
        </Banner>
      ))}
      {missing.length > 0 && (
        <Banner
          variant="danger"
          title={t('promote.missing_title')}
          action={<Button onClick={onCheckAgain}>{t('promote.check_again')}</Button>}
        >
          <p>{t('promote.missing_text')}</p>
          <ul className="mt-1 list-disc pl-5 font-mono text-sm">
            {missing.map((name) => (
              <li key={name}>
                <span aria-hidden="true">✕ </span>
                {name}
              </li>
            ))}
          </ul>
        </Banner>
      )}
      {items.length === 0 && <p className="text-muted">{t('promote.nothing')}</p>}
      {items.map((item) => (
        <PlanItemView key={item.service} item={item} />
      ))}
    </div>
  )
}

function PlanItemView({ item }: { item: PlanItem }) {
  const changes = item.changes ?? []
  return (
    <section className="space-y-2" aria-label={item.service}>
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="font-semibold">{item.service}</h3>
        {item.strategy && (
          <StatusBadge tone="info" label={tState('deploy.strategy_value', item.strategy)} />
        )}
        {item.image_digest && (
          <span className="break-all font-mono text-xs text-muted" title={item.image_digest}>
            {item.image_digest.slice(0, 19)}
          </span>
        )}
      </div>
      {item.skipped_reason ? (
        <p className="text-sm text-muted">
          {t('promote.skipped', { reason: item.skipped_reason })}
        </p>
      ) : changes.length === 0 ? (
        <p className="text-sm text-muted">{t('promote.no_changes')}</p>
      ) : (
        <DataList
          caption={t('promote.changes_caption', { service: item.service ?? '' })}
          rows={changes}
          rowKey={(c) => c.path ?? ''}
          columns={[
            {
              header: t('promote.col_field'),
              primary: true,
              cell: (c) => <span className="break-all font-mono text-xs">{c.path}</span>,
            },
            {
              header: t('promote.col_before'),
              cell: (c) => <span className="break-all font-mono text-xs">{show(c.before)}</span>,
            },
            {
              header: t('promote.col_after'),
              cell: (c) => <span className="break-all font-mono text-xs">{show(c.after)}</span>,
            },
          ]}
        />
      )}
    </section>
  )
}
