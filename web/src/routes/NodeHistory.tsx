// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import { Banner, EmptyState, Skeleton } from '@/components/ui'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'
import { useNodes } from '@/scope'

type AuditEvent = components['schemas']['AuditEvent']

interface Row {
  id: string
  time?: string
  service: string
  from: string
  to: string
  cause: string
  result: string
}

// The audit actions that tell what moved and why (UI-41).
const MOVES = new Set([
  'service.move',
  'service.failover',
  'service.failover_start',
  'service.failover_blocked',
])

function ids(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string') : []
}

// UI-41: what moved and why. It reads the audit log, so it needs audit:read (admin and owner).
export function NodeHistoryPage() {
  const { org } = useParams({ from: '/o/$org/nodes/history' })
  const allowed = useAccess('audit:read') === 'allow'
  const nodes = useNodes()
  const events = useQuery({
    queryKey: ['move-history'],
    enabled: allowed,
    queryFn: async () => {
      const page = await unwrap(
        api.GET('/audit-events', { params: { query: { limit: 100, action: 'service.*' } } }),
      )
      return (page.items ?? []).filter((e) => MOVES.has(e.action ?? ''))
    },
    refetchInterval: 10_000,
  })
  const name = (id: string) => nodes.data?.find((n) => n.id === id)?.name ?? id

  const rows: Row[] = (events.data ?? []).map((e: AuditEvent) => {
    const diff = (e.diff ?? {}) as Record<string, unknown>
    const from = ids(diff.from_nodes ?? (typeof diff.node_id === 'string' ? [diff.node_id] : []))
    const to = ids(diff.to_nodes)
    const cause = e.action === 'service.move' ? String(diff.cause ?? 'manual') : 'failover'
    let result = t('nodes.history_done')
    if (e.action === 'service.failover_start') result = t('nodes.history_started')
    if (e.action === 'service.failover_blocked')
      result = String(diff.reason ?? t('nodes.history_blocked'))
    return {
      id: e.id ?? String(e.seq),
      time: e.created_at,
      service: e.target_id ?? '-',
      from: from.map(name).join(', ') || '-',
      to: to.map(name).join(', ') || '-',
      cause: t(`nodes.cause.${cause}` as 'nodes.cause.manual'),
      result,
    }
  })

  return (
    <section>
      <PageHeader
        title={t('nodes.history_title')}
        breadcrumb={
          <Link to="/o/$org/nodes" params={{ org }} className="text-primary underline">
            {t('nodes.title')}
          </Link>
        }
      />
      {!allowed && <Banner>{t('nodes.history_needs_audit')}</Banner>}
      {allowed && events.isPending && <Skeleton />}
      {allowed && events.isError && (
        <ErrorBanner error={events.error} onRetry={() => void events.refetch()} />
      )}
      {allowed && events.data && rows.length === 0 && (
        <EmptyState title={t('nodes.history_empty_title')} text={t('nodes.history_empty_text')} />
      )}
      {allowed && rows.length > 0 && (
        <DataList
          caption={t('nodes.history_title')}
          rows={rows}
          rowKey={(r) => r.id}
          columns={[
            { header: t('audit.time'), primary: true, cell: (r) => <Time value={r.time} /> },
            {
              header: t('nodes.service'),
              cell: (r) => <span className="font-mono">{r.service}</span>,
            },
            { header: t('nodes.history_from'), cell: (r) => r.from },
            { header: t('nodes.history_to'), cell: (r) => r.to },
            { header: t('nodes.history_cause'), cell: (r) => r.cause },
            { header: t('nodes.history_result'), cell: (r) => r.result },
          ]}
        />
      )}
    </section>
  )
}
