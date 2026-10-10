// SPDX-License-Identifier: Apache-2.0

import { Link, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import { MuteDialog } from '@/components/MuteDialog'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import { Banner, Button, EmptyState, Skeleton, StatusBadge } from '@/components/ui'
import {
  failingChannels,
  rulesWithoutChannel,
  scopeOf,
  severityTone,
  useAlertChannels,
  useAlertInstances,
  useAlertRules,
} from '@/alerts'
import type { AlertInstance, AlertRule } from '@/alerts'
import { t, tState } from '@/i18n'
import { useAccess } from '@/permissions'

// A short text of the details of an alert. The values are data and are drawn as text.
function detailsText(details: AlertInstance['details']): string {
  return Object.entries(details ?? {})
    .filter(([, v]) => ['string', 'number', 'boolean'].includes(typeof v))
    .map(([k, v]) => `${k}: ${String(v)}`)
    .join(', ')
}

export function AlertBadges({ alert }: { alert: AlertInstance }) {
  return (
    <span className="flex flex-wrap gap-1">
      <StatusBadge
        tone={severityTone(alert.severity)}
        label={tState('alerts.severity', alert.severity)}
      />
      {alert.muted && <StatusBadge tone="muted" label={t('alerts.muted')} />}
      {alert.suppressed && <StatusBadge tone="muted" label={t('alerts.suppressed')} />}
    </span>
  )
}

// UI-22: firing and resolved alerts, the rules and mute.
export function AlertsPage() {
  const { org } = useParams({ from: '/o/$org/alerts' })
  const manage = useAccess('alert:manage') === 'allow'
  const instances = useAlertInstances()
  const rules = useAlertRules()
  const channels = useAlertChannels()
  const [muting, setMuting] = useState<{ id: string; name: string } | null>(null)

  const firing = (instances.data ?? []).filter((a) => a.state === 'firing')
  const resolved = (instances.data ?? []).filter((a) => a.state === 'resolved')
  const noChannel = rulesWithoutChannel(rules.data)
  const failing = failingChannels(channels.data)
  const channelName = (id: string) => channels.data?.find((c) => c.id === id)?.name ?? id

  const muteButton = (ruleId: string | undefined, name: string | undefined) =>
    manage && ruleId ? (
      <Button onClick={() => setMuting({ id: ruleId, name: name ?? '' })}>
        {t('alerts.mute')}
      </Button>
    ) : null

  const instanceColumns = [
    {
      header: t('alerts.col_alert'),
      primary: true,
      cell: (a: AlertInstance) => (
        <span>
          {a.rule_name}
          <span className="block text-sm font-normal text-muted">
            {tState('alerts.type', a.rule_type)}
          </span>
        </span>
      ),
    },
    { header: t('alerts.col_severity'), cell: (a: AlertInstance) => <AlertBadges alert={a} /> },
    {
      header: t('alerts.col_since'),
      cell: (a: AlertInstance) => <Time value={a.first_fired_at} />,
    },
    {
      header: t('alerts.col_details'),
      cell: (a: AlertInstance) => <span className="break-words">{detailsText(a.details)}</span>,
    },
  ]

  return (
    <section>
      <PageHeader
        title={t('alerts.title')}
        actions={
          <Link to="/o/$org/settings/alerts" params={{ org }} className="text-primary underline">
            {manage ? t('alerts.open_editor') : t('alerts.open_editor_read')}
          </Link>
        }
      />
      <div className="space-y-4">
        {noChannel.length > 0 && (
          <Banner
            variant="warning"
            title={t('alerts.no_channel_title')}
            action={
              <Link
                to="/o/$org/settings/alerts"
                params={{ org }}
                className="text-primary underline"
              >
                {t('alerts.add_channel')}
              </Link>
            }
          >
            {t('alerts.no_channel_text', { names: noChannel.map((r) => r.name).join(', ') })}
          </Banner>
        )}
        {failing.map((c) => (
          <Banner
            key={c.id}
            variant="danger"
            title={t('alerts.channel_failing', { name: c.name ?? '' })}
            action={
              <Link
                to="/o/$org/settings/alerts"
                params={{ org }}
                className="text-primary underline"
              >
                {t('alerts.fix_channel')}
              </Link>
            }
          >
            <p>{c.last_error || t('alerts.channel_failing_text')}</p>
          </Banner>
        ))}
        {channels.isError && (
          <ErrorBanner error={channels.error} onRetry={() => void channels.refetch()} />
        )}

        <h2 className="text-lg font-semibold">{t('alerts.firing')}</h2>
        {instances.isPending && <Skeleton />}
        {instances.isError && (
          <ErrorBanner error={instances.error} onRetry={() => void instances.refetch()} />
        )}
        {instances.data && firing.length === 0 && (
          <EmptyState title={t('alerts.quiet_title')} text={t('alerts.quiet_text')} />
        )}
        {firing.length > 0 && (
          <DataList
            caption={t('alerts.firing')}
            rows={firing}
            rowKey={(a) => a.id ?? ''}
            columns={instanceColumns}
            actionsHeader={t('common.actions')}
            actions={(a) => muteButton(a.rule_id, a.rule_name)}
          />
        )}

        {resolved.length > 0 && (
          <>
            <h2 className="text-lg font-semibold">{t('alerts.resolved')}</h2>
            <DataList
              caption={t('alerts.resolved')}
              rows={resolved}
              rowKey={(a) => a.id ?? ''}
              columns={[
                ...instanceColumns.slice(0, 3),
                {
                  header: t('alerts.col_resolved'),
                  cell: (a: AlertInstance) => <Time value={a.resolved_at} />,
                },
              ]}
            />
          </>
        )}

        <h2 className="text-lg font-semibold">{t('alerts.rules')}</h2>
        {rules.isPending && <Skeleton />}
        {rules.isError && <ErrorBanner error={rules.error} onRetry={() => void rules.refetch()} />}
        {rules.data && rules.data.length === 0 && (
          <p className="text-muted">{t('alerts.no_rules')}</p>
        )}
        {rules.data && rules.data.length > 0 && (
          <DataList
            caption={t('alerts.rules')}
            rows={rules.data}
            rowKey={(r) => r.id ?? ''}
            columns={[
              { header: t('alerts.col_rule'), primary: true, cell: (r: AlertRule) => r.name },
              {
                header: t('alerts.col_type'),
                cell: (r: AlertRule) => tState('alerts.type', r.type),
              },
              {
                header: t('alerts.col_scope'),
                cell: (r: AlertRule) => {
                  const s = scopeOf(r)
                  return s.id ? `${t(`alerts.scope.${s.kind}`)}: ${s.id}` : t('alerts.scope.org')
                },
              },
              {
                header: t('alerts.col_severity'),
                cell: (r: AlertRule) => (
                  <StatusBadge
                    tone={severityTone(r.severity)}
                    label={tState('alerts.severity', r.severity)}
                  />
                ),
              },
              {
                header: t('alerts.col_channels'),
                cell: (r: AlertRule) =>
                  (r.channel_ids ?? []).length === 0 ? (
                    <span className="text-warning">{t('alerts.no_channel_short')}</span>
                  ) : (
                    (r.channel_ids ?? []).map(channelName).join(', ')
                  ),
              },
              {
                header: t('alerts.col_state'),
                cell: (r: AlertRule) =>
                  r.muted_until && new Date(r.muted_until).getTime() > Date.now() ? (
                    <span>
                      {t('alerts.muted_until')} <Time value={r.muted_until} />
                      {r.mute_reason && (
                        <span className="block text-sm text-muted">{r.mute_reason}</span>
                      )}
                    </span>
                  ) : r.enabled === false ? (
                    t('alerts.disabled')
                  ) : (
                    t('alerts.enabled')
                  ),
              },
            ]}
            actionsHeader={t('common.actions')}
            actions={(r) => muteButton(r.id, r.name)}
          />
        )}
      </div>
      {muting && (
        <MuteDialog ruleId={muting.id} ruleName={muting.name} onClose={() => setMuting(null)} />
      )}
    </section>
  )
}
