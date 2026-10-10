// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Time } from '@/components/Time'
import { Banner, Button, CheckboxField, SelectField, TextField } from '@/components/ui'
import { obj } from '@/configForm'
import type { Config } from '@/configForm'
import { t } from '@/i18n'
import { accessFor } from '@/permissions'
import type { Service } from '@/scope'

function labelsText(labels: unknown): string {
  return Object.entries(obj(labels))
    .map(([k, v]) => `${k}=${String(v)}`)
    .join('\n')
}

function parseLabels(text: string): Record<string, string> | null {
  const out: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (line === '') continue
    const at = line.indexOf('=')
    if (at < 1) return null
    out[line.slice(0, at).trim()] = line.slice(at + 1).trim()
  }
  return out
}

// The failover state of a service (09 section 9) in words.
export function FailoverBanner({ service }: { service: Service }) {
  const fs = service.failover_status
  if (!fs || fs.state === 'healthy' || !fs.state) return null
  if (fs.state === 'blocked') {
    return (
      <Banner variant="danger" title={t('failover.blocked_title')}>
        {fs.reason}
      </Banner>
    )
  }
  if (fs.state === 'moving')
    return <Banner title={t('failover.moving_title')}>{t('failover.moving_text')}</Banner>
  return (
    <Banner title={t('failover.moved_title')}>
      {fs.data_lost_since ? (
        <span>
          {t('failover.data_lost')} <Time value={fs.data_lost_since} />
        </span>
      ) : (
        t('failover.moved_text')
      )}
    </Banner>
  )
}

// REQ-NODE-004 and REQ-NODE-007: where the service may run, and what happens when its node fails.
export function ServicePlacement({
  service,
  environmentProtected,
  onChanged,
}: {
  service: Service
  environmentProtected?: boolean
  onChanged: () => void
}) {
  const queryClient = useQueryClient()
  const me = useMe()
  const access = accessFor(me.data?.org_role, 'service:update', environmentProtected)
  const config = (service.config ?? {}) as Config
  const placement = obj(config.placement)
  const [names, setNames] = useState(((placement.nodes as string[] | undefined) ?? []).join(', '))
  const [labels, setLabels] = useState(labelsText(placement.labels))
  const [spread, setSpread] = useState(placement.spread !== false)
  const [failover, setFailover] = useState<'none' | 'auto'>(service.failover ?? 'none')
  const [lossOk, setLossOk] = useState(service.allow_data_loss_failover === true)
  const [saved, setSaved] = useState(false)
  const parsed = parseLabels(labels)
  const readOnly = access !== 'allow'

  const save = useMutation({
    mutationFn: () => {
      const next: Config = { ...config }
      const list = names
        .split(',')
        .map((n) => n.trim())
        .filter(Boolean)
      const p: Record<string, unknown> = {}
      if (list.length > 0) p.nodes = list
      if (parsed && Object.keys(parsed).length > 0) p.labels = parsed
      if (!spread) p.spread = false
      if (Object.keys(p).length > 0) next.placement = p
      else delete next.placement
      return unwrap(
        api.PATCH('/services/{service_id}', {
          params: { path: { service_id: service.id ?? '' } },
          body: { config: next, failover, allow_data_loss_failover: lossOk },
        }),
      )
    },
    onSuccess: async () => {
      setSaved(true)
      onChanged()
      await queryClient.invalidateQueries({ queryKey: ['service', service.id] })
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    setSaved(false)
    if (parsed) save.mutate()
  }
  const showBackup = service.last_backup_at !== undefined || lossOk

  return (
    <form onSubmit={submit} noValidate className="max-w-2xl space-y-4 pb-6">
      <h2 className="text-lg font-semibold">{t('placement.title')}</h2>
      {saved && <Banner>{t('settings.saved')}</Banner>}
      {save.error && <ErrorBanner error={save.error} title={t('settings.save_failed')} />}
      <FailoverBanner service={service} />
      <TextField
        label={t('placement.nodes')}
        hint={t('placement.nodes_hint')}
        readOnly={readOnly}
        autoComplete="off"
        value={names}
        onChange={(e) => setNames(e.target.value)}
      />
      <div className="space-y-1">
        <label htmlFor="placement-labels" className="block text-sm font-medium">
          {t('placement.labels')}
        </label>
        <textarea
          id="placement-labels"
          rows={2}
          spellCheck={false}
          readOnly={readOnly}
          aria-invalid={parsed ? undefined : true}
          className="block w-full rounded-sm border border-border-strong bg-bg px-3 py-2 font-mono text-base text-text aria-[invalid=true]:border-danger"
          value={labels}
          onChange={(e) => setLabels(e.target.value)}
        />
        <p className="text-sm text-muted">
          {parsed ? t('placement.labels_hint') : t('nodes.labels_invalid')}
        </p>
      </div>
      <CheckboxField
        label={t('placement.spread')}
        hint={t('placement.spread_hint')}
        disabled={readOnly}
        checked={spread}
        onChange={(e) => setSpread(e.target.checked)}
      />
      <SelectField
        label={t('failover.policy')}
        hint={t('failover.policy_hint')}
        disabled={readOnly}
        value={failover}
        onChange={(e) => setFailover(e.target.value as 'none' | 'auto')}
      >
        <option value="none">{t('failover.none')}</option>
        <option value="auto">{t('failover.auto')}</option>
      </SelectField>
      <CheckboxField
        label={t('failover.allow_loss')}
        hint={t('failover.allow_loss_hint')}
        disabled={readOnly || failover !== 'auto'}
        checked={lossOk}
        onChange={(e) => setLossOk(e.target.checked)}
      />
      {showBackup && (
        <p className="text-sm text-muted">
          {t('failover.last_backup')}:{' '}
          {service.last_backup_at ? (
            <Time value={service.last_backup_at} />
          ) : (
            t('failover.no_backup')
          )}
        </p>
      )}
      {!readOnly && (
        <Button
          type="submit"
          variant="primary"
          loading={save.isPending}
          reason={parsed ? undefined : t('nodes.labels_invalid')}
        >
          {t('common.save')}
        </Button>
      )}
    </form>
  )
}
