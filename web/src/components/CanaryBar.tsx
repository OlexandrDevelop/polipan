// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog } from '@/components/Modal'
import { Button, Card, StatusBadge } from '@/components/ui'
import { t, tState } from '@/i18n'
import { formatValue } from '@/metrics'
import type { Access } from '@/permissions'
import { isDeploymentActive } from '@/status'

type Deployment = components['schemas']['Deployment']

// The data of a canary_gate event (deployment events stream, or Deployment.canary_gate).
export interface CanaryGate {
  step?: number
  passed?: boolean
  reason?: string
  health_ok?: boolean
  green_requests?: number
  green_5xx_rate?: number
  green_p95_seconds?: number
  blue_p95_seconds?: number
}

function yesNo(value: boolean | undefined): string {
  if (value === undefined) return '-'
  return value ? t('common.yes') : t('common.no')
}

// UI-08, canary bar: the steps, the live gate values and the controls Promote, Pause or Resume,
// and Abort (REQ-DEPLOY-006). The controls need service:deploy.
export function CanaryBar({
  deployment,
  gate,
  access,
}: {
  deployment: Deployment
  gate?: CanaryGate
  access: Access
}) {
  const queryClient = useQueryClient()
  const [confirmAbort, setConfirmAbort] = useState(false)
  const id = deployment.id ?? ''
  const steps = deployment.canary_steps ?? deployment.strategy_options?.canary_steps ?? []
  const current = deployment.canary_step
  const paused = deployment.canary_paused === true
  const active = isDeploymentActive(deployment.state)
  const shown = gate ?? (deployment.canary_gate as CanaryGate | undefined)

  const refresh = () =>
    Promise.all(
      ['deployment', 'deployments', 'service'].map((key) =>
        queryClient.invalidateQueries({ queryKey: [key] }),
      ),
    )
  const call = useMutation({
    mutationFn: async (action: 'promote' | 'pause' | 'resume' | 'abort') => {
      // The screen may be old after a lost stream. Ask the server for the state first.
      await queryClient.refetchQueries({ queryKey: ['deployment', id] })
      const fresh = queryClient.getQueryData<Deployment>(['deployment', id])
      if (fresh && !isDeploymentActive(fresh.state)) return
      const path = { params: { path: { deployment_id: id } } }
      if (action === 'promote') await unwrap(api.POST('/deployments/{deployment_id}/promote', path))
      else if (action === 'pause')
        await unwrap(api.POST('/deployments/{deployment_id}/pause', path))
      else if (action === 'resume')
        await unwrap(api.POST('/deployments/{deployment_id}/resume', path))
      else await unwrap(api.POST('/deployments/{deployment_id}/abort', path))
    },
    onSuccess: async () => {
      setConfirmAbort(false)
      await refresh()
    },
  })

  const reason = access === 'blocked' ? t('common.needs_admin') : undefined
  const pending = (action: string) => call.isPending && call.variables === action

  return (
    <Card className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="font-semibold">{t('canary.title')}</h2>
        {paused && <StatusBadge tone="warning" label={t('canary.paused')} />}
      </div>
      {steps.length > 0 && (
        <ol aria-label={t('canary.steps')} className="flex flex-wrap gap-2">
          {steps.map((p) => {
            const done = current !== undefined && p < current
            const now = current === p
            return (
              <li
                key={p}
                aria-current={now ? 'step' : undefined}
                className={`inline-flex items-center gap-1.5 border px-3 py-1 text-sm ${
                  now ? 'border-primary font-semibold' : 'border-border-strong'
                }`}
              >
                <span aria-hidden="true">{done ? '✓' : now ? '▶' : '○'}</span>
                {p} %
                <span className="sr-only">
                  {done
                    ? t('canary.step_done')
                    : now
                      ? t('canary.step_current')
                      : t('canary.step_next')}
                </span>
              </li>
            )
          })}
        </ol>
      )}
      <section aria-label={t('canary.gate')} className="space-y-2">
        <h3 className="text-sm font-semibold">{t('canary.gate')}</h3>
        {!shown ? (
          <p className="text-sm text-muted">{t('canary.no_gate')}</p>
        ) : (
          <>
            <p className="flex flex-wrap items-center gap-2 text-sm">
              <StatusBadge
                tone={shown.passed ? 'success' : 'danger'}
                label={shown.passed ? t('canary.gate_passed') : t('canary.gate_failed')}
              />
              {shown.step !== undefined && (
                <span>{t('canary.gate_step', { step: shown.step })}</span>
              )}
              {shown.reason && (
                <span className="text-muted">{tState('canary.reason', shown.reason)}</span>
              )}
            </p>
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm sm:grid-cols-[auto_1fr_auto_1fr]">
              <dt className="text-muted">{t('canary.health_ok')}</dt>
              <dd>{yesNo(shown.health_ok)}</dd>
              <dt className="text-muted">{t('canary.green_requests')}</dt>
              <dd>{shown.green_requests ?? '-'}</dd>
              <dt className="text-muted">{t('canary.green_5xx')}</dt>
              <dd>
                {shown.green_5xx_rate === undefined
                  ? '-'
                  : formatValue(shown.green_5xx_rate, 'ratio')}
                {deployment.strategy_options?.canary_max_5xx_rate !== undefined &&
                  ` (${t('canary.max')} ${formatValue(deployment.strategy_options.canary_max_5xx_rate, 'ratio')})`}
              </dd>
              <dt className="text-muted">{t('canary.green_p95')}</dt>
              <dd>
                {shown.green_p95_seconds === undefined
                  ? '-'
                  : formatValue(shown.green_p95_seconds, 'seconds')}
              </dd>
              <dt className="text-muted">{t('canary.blue_p95')}</dt>
              <dd>
                {shown.blue_p95_seconds === undefined
                  ? '-'
                  : formatValue(shown.blue_p95_seconds, 'seconds')}
              </dd>
            </dl>
          </>
        )}
      </section>
      {access !== 'hidden' && active && (
        <div className="flex flex-wrap gap-2 max-sm:sticky max-sm:bottom-16 max-sm:z-10 max-sm:bg-surface max-sm:py-2">
          <Button
            variant="primary"
            reason={reason}
            loading={pending('promote')}
            onClick={() => call.mutate('promote')}
          >
            {t('canary.promote')}
          </Button>
          {paused ? (
            <Button
              reason={reason}
              loading={pending('resume')}
              onClick={() => call.mutate('resume')}
            >
              {t('canary.resume')}
            </Button>
          ) : (
            <Button reason={reason} loading={pending('pause')} onClick={() => call.mutate('pause')}>
              {t('canary.pause')}
            </Button>
          )}
          <Button variant="destructive" reason={reason} onClick={() => setConfirmAbort(true)}>
            {t('canary.abort')}
          </Button>
        </div>
      )}
      {call.error && !confirmAbort && (
        <ErrorBanner error={call.error} title={t('canary.action_failed')} />
      )}
      {confirmAbort && (
        <ConfirmDialog
          title={t('canary.abort_title')}
          text={t('canary.abort_text')}
          confirmLabel={t('canary.abort')}
          busy={pending('abort')}
          error={
            call.error ? (
              <ErrorBanner error={call.error} title={t('canary.action_failed')} />
            ) : undefined
          }
          onConfirm={() => call.mutate('abort')}
          onClose={() => {
            call.reset()
            setConfirmAbort(false)
          }}
        />
      )}
    </Card>
  )
}
