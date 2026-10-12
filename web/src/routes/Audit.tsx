// SPDX-License-Identifier: Apache-2.0

import { useInfiniteQuery, useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import {
  Banner,
  Button,
  EmptyState,
  Optional,
  SelectField,
  Skeleton,
  TextField,
} from '@/components/ui'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'
import { useProjects } from '@/scope'

type AuditEvent = components['schemas']['AuditEvent']

interface Filters {
  actor_id: string
  action: string
  target_type: string
  target_id: string
  project_id: string
  from: string
  to: string
}

const EMPTY: Filters = {
  actor_id: '',
  action: '',
  target_type: '',
  target_id: '',
  project_id: '',
  from: '',
  to: '',
}

// A datetime-local value has no zone. The API wants an ISO time, so the local time is converted.
function isoOrUndefined(local: string): string | undefined {
  return local ? new Date(local).toISOString() : undefined
}

function queryOf(filters: Filters) {
  return {
    actor_id: filters.actor_id.trim() || undefined,
    action: filters.action.trim() || undefined,
    target_type: filters.target_type.trim() || undefined,
    target_id: filters.target_id.trim() || undefined,
    project_id: filters.project_id || undefined,
    from: isoOrUndefined(filters.from),
    to: isoOrUndefined(filters.to),
  }
}

function FilterForm({ initial, onApply }: { initial: Filters; onApply: (f: Filters) => void }) {
  const projects = useProjects()
  const [draft, setDraft] = useState(initial)
  const set = (key: keyof Filters) => (e: { target: { value: string } }) =>
    setDraft({ ...draft, [key]: e.target.value })
  function submit(event: FormEvent) {
    event.preventDefault()
    onApply(draft)
  }
  return (
    <form
      onSubmit={submit}
      aria-label={t('audit.filters')}
      className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-4"
    >
      <TextField
        label={t('audit.action')}
        autoComplete="off"
        hint={t('audit.action_hint')}
        value={draft.action}
        onChange={set('action')}
      />
      <TextField
        label={t('audit.from')}
        type="datetime-local"
        value={draft.from}
        onChange={set('from')}
      />
      <TextField
        label={t('audit.to')}
        type="datetime-local"
        value={draft.to}
        onChange={set('to')}
      />
      <Optional
        filled={
          draft.actor_id !== '' ||
          draft.target_type !== '' ||
          draft.target_id !== '' ||
          draft.project_id !== ''
        }
      >
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <TextField
            label={t('audit.actor')}
            autoComplete="off"
            value={draft.actor_id}
            onChange={set('actor_id')}
          />
          <TextField
            label={t('audit.target_type')}
            autoComplete="off"
            value={draft.target_type}
            onChange={set('target_type')}
          />
          <TextField
            label={t('audit.target_id')}
            autoComplete="off"
            value={draft.target_id}
            onChange={set('target_id')}
          />
          <SelectField
            label={t('audit.project')}
            value={draft.project_id}
            onChange={set('project_id')}
          >
            <option value="">{t('audit.any_project')}</option>
            {(projects.data ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </SelectField>
        </div>
      </Optional>
      <div className="flex items-end gap-2">
        <Button type="submit" variant="primary">
          {t('audit.search')}
        </Button>
        <Button
          onClick={() => {
            setDraft(EMPTY)
            onApply(EMPTY)
          }}
        >
          {t('audit.clear')}
        </Button>
      </div>
    </form>
  )
}

function Detail({ event, onClose }: { event: AuditEvent; onClose: () => void }) {
  const rows: [string, string | number | undefined][] = [
    [t('audit.seq'), event.seq],
    [t('audit.actor'), `${event.actor_type ?? ''} ${event.actor_id ?? ''}`.trim()],
    [t('audit.action'), event.action],
    [t('audit.object'), `${event.target_type ?? ''} ${event.target_id ?? ''}`.trim()],
    [t('audit.project'), event.project_id],
    [t('audit.ip'), event.ip],
    [t('audit.request_id'), event.request_id],
    [t('audit.hash'), event.hash],
  ]
  return (
    <Modal title={t('audit.detail_title')} onClose={onClose}>
      <dl className="mb-4 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
        {rows.map(([name, value]) => (
          <div key={name} className="contents">
            <dt className="text-muted">{name}</dt>
            <dd className="break-all">{value ?? '-'}</dd>
          </div>
        ))}
      </dl>
      <h3 className="mb-1 font-semibold">{t('audit.diff')}</h3>
      <pre
        tabIndex={0}
        aria-label={t('audit.diff')}
        className="max-h-96 overflow-auto rounded-sm border border-border bg-surface p-3 font-mono text-sm"
      >
        {JSON.stringify(event.diff ?? {}, null, 2)}
      </pre>
      <p className="mt-2 text-sm text-muted">{t('audit.diff_hint')}</p>
    </Modal>
  )
}

// UI-26: who did what. The log can be searched, exported and checked.
export function AuditPage() {
  const canExport = useAccess('audit:export') === 'allow'
  const [filters, setFilters] = useState<Filters>(EMPTY)
  const [open, setOpen] = useState<AuditEvent | null>(null)

  const events = useInfiniteQuery({
    queryKey: ['audit', filters],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/audit-events', {
          params: { query: { limit: 50, cursor: pageParam, ...queryOf(filters) } },
        }),
      ),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  })
  const verify = useMutation({ mutationFn: () => unwrap(api.GET('/audit/verify')) })
  const exportLog = useMutation({
    mutationFn: async () => {
      const q = queryOf(filters)
      const params = new URLSearchParams()
      if (q.from) params.set('from', q.from)
      if (q.to) params.set('to', q.to)
      const res = await fetch(`/api/v1/audit-events/export?${params.toString()}`, {
        credentials: 'same-origin',
      })
      if (!res.ok) throw new ApiError(res.status, 'export_failed', res.statusText)
      const url = URL.createObjectURL(await res.blob())
      const link = document.createElement('a')
      link.href = url
      link.download = 'audit-events.jsonl'
      link.click()
      URL.revokeObjectURL(url)
    },
  })

  const rows = events.data?.pages.flatMap((p) => p.items ?? []) ?? []
  return (
    <section className="max-w-6xl">
      <PageHeader
        title={t('audit.title')}
        actions={
          <>
            <Button loading={verify.isPending} onClick={() => verify.mutate()}>
              {t('audit.verify')}
            </Button>
            {canExport && (
              <Button loading={exportLog.isPending} onClick={() => exportLog.mutate()}>
                {t('audit.export')}
              </Button>
            )}
          </>
        }
      />
      {verify.data &&
        (verify.data.ok ? (
          <div className="mb-4">
            <Banner variant="info" title={t('audit.verify_ok_title')}>
              {t('audit.verify_ok', { count: verify.data.checked_rows ?? 0 })}
            </Banner>
          </div>
        ) : (
          <div className="mb-4">
            <Banner variant="danger" title={t('audit.verify_bad_title')}>
              {t('audit.verify_bad', { seq: verify.data.first_broken_seq ?? 0 })}
            </Banner>
          </div>
        ))}
      {verify.error && (
        <div className="mb-4">
          <ErrorBanner error={verify.error} title={t('audit.verify_failed')} />
        </div>
      )}
      {exportLog.error && (
        <div className="mb-4">
          <ErrorBanner error={exportLog.error} title={t('audit.export_failed')} />
        </div>
      )}
      <FilterForm initial={filters} onApply={setFilters} />
      {events.isPending && <Skeleton />}
      {events.isError && <ErrorBanner error={events.error} onRetry={() => void events.refetch()} />}
      {events.data &&
        (rows.length === 0 ? (
          <EmptyState title={t('audit.empty_title')} text={t('audit.empty_text')} />
        ) : (
          <div className="space-y-4">
            <DataList
              caption={t('audit.caption')}
              rows={rows}
              rowKey={(e) => String(e.seq ?? e.id)}
              columns={[
                {
                  header: t('audit.time'),
                  primary: true,
                  cell: (e) => <Time value={e.created_at} />,
                },
                {
                  header: t('audit.actor'),
                  cell: (e) => (
                    <span className="break-all">
                      {e.actor_type} {e.actor_id}
                    </span>
                  ),
                },
                {
                  header: t('audit.action'),
                  cell: (e) => <span className="break-all">{e.action}</span>,
                },
                {
                  header: t('audit.object'),
                  cell: (e) => (
                    <span className="break-all">
                      {e.target_type} {e.target_id}
                    </span>
                  ),
                  cardHidden: true,
                },
              ]}
              actionsHeader={t('common.actions')}
              actions={(e) => <Button onClick={() => setOpen(e)}>{t('audit.details')}</Button>}
            />
            {events.hasNextPage && (
              <Button
                loading={events.isFetchingNextPage}
                onClick={() => void events.fetchNextPage()}
              >
                {t('audit.more')}
              </Button>
            )}
          </div>
        ))}
      {open && <Detail event={open} onClose={() => setOpen(null)} />}
    </section>
  )
}
