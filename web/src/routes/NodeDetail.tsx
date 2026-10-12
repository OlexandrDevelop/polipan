// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { Suspense, useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { lazyNamed } from '@/lazy'
import { NodeBars } from '@/components/NodeBars'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog, TypedConfirmDialog } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import {
  Banner,
  Button,
  Card,
  EmptyState,
  Hint,
  SectionTabs,
  Skeleton,
  StatusBadge,
} from '@/components/ui'
import { formatRelative, t, tState } from '@/i18n'
import { useAccess } from '@/permissions'
import { NotFoundPage } from '@/routes/ErrorPages'
import { ApiError } from '@/api/errors'
import { nodeTone } from '@/status'

type Dialog = 'drain' | 'delete' | { orphan: string } | null

// One label per line: key=value.
const MetricsPanel = lazyNamed(() => import('@/components/MetricsPanel'), 'MetricsPanel')

function labelsText(labels: Record<string, string> | undefined): string {
  return Object.entries(labels ?? {})
    .map(([k, v]) => `${k}=${v}`)
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

function LabelsForm({
  id,
  labels,
  disabled,
}: {
  id: string
  labels?: Record<string, string>
  disabled: boolean
}) {
  const queryClient = useQueryClient()
  const initial = labelsText(labels)
  const [text, setText] = useState(initial)
  const parsed = parseLabels(text)
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/nodes/{node_id}', {
          params: { path: { node_id: id } },
          body: { labels: parsed ?? {} },
        }),
      ),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['node', id] }),
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    if (parsed) save.mutate()
  }
  return (
    <form onSubmit={submit} className="space-y-2">
      <label htmlFor="node-labels" className="block text-sm font-medium">
        {t('nodes.labels_column')}
      </label>
      <textarea
        id="node-labels"
        rows={3}
        spellCheck={false}
        disabled={disabled}
        aria-invalid={parsed ? undefined : true}
        className="block w-full rounded-sm border border-border-strong bg-bg px-3 py-2 font-mono text-base text-text aria-[invalid=true]:border-danger"
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
      <p className="text-sm text-muted">
        {parsed ? t('nodes.labels_hint') : t('nodes.labels_invalid')}
      </p>
      {save.error && <ErrorBanner error={save.error} />}
      {!disabled && (
        <Button
          type="submit"
          loading={save.isPending}
          reason={
            parsed
              ? text === initial
                ? t('settings.no_changes')
                : undefined
              : t('nodes.labels_invalid')
          }
        >
          {t('nodes.save_labels')}
        </Button>
      )}
    </form>
  )
}

function OrphanVolumes({ id, canDelete }: { id: string; canDelete: boolean }) {
  const queryClient = useQueryClient()
  const [asking, setAsking] = useState<string | null>(null)
  const list = useQuery({
    queryKey: ['orphan-volumes', id],
    queryFn: () =>
      unwrap(api.GET('/nodes/{node_id}/orphan-volumes', { params: { path: { node_id: id } } })),
  })
  const remove = useMutation({
    mutationFn: (name: string) =>
      unwrap(
        api.DELETE('/orphan-volumes/{node_id}/{name}', { params: { path: { node_id: id, name } } }),
      ),
    onSuccess: async () => {
      setAsking(null)
      await queryClient.invalidateQueries({ queryKey: ['orphan-volumes', id] })
    },
  })
  return (
    <Card className="space-y-2">
      <h2 className="font-semibold">{t('nodes.orphans')}</h2>
      <Hint>{t('nodes.orphans_hint')}</Hint>
      {list.isPending && <Skeleton lines={1} />}
      {list.isError && <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />}
      {list.data && list.data.length === 0 && <p className="text-muted">{t('nodes.no_orphans')}</p>}
      {list.data && list.data.length > 0 && (
        <ul className="space-y-1 text-sm">
          {list.data.map((v) => (
            <li key={v.name} className="flex flex-wrap items-center justify-between gap-2">
              <span className="font-mono">{v.name}</span>
              {canDelete && (
                <Button variant="destructive" onClick={() => setAsking(v.name ?? '')}>
                  {t('common.delete')}
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {asking && (
        <TypedConfirmDialog
          title={t('nodes.orphan_delete_title')}
          text={t('nodes.orphan_delete_text', { name: asking })}
          confirmLabel={t('common.delete')}
          typed={asking}
          busy={remove.isPending}
          error={remove.error ? <ErrorBanner error={remove.error} /> : undefined}
          onConfirm={() => remove.mutate(asking)}
          onClose={() => {
            setAsking(null)
            remove.reset()
          }}
        />
      )}
    </Card>
  )
}

// UI-40: one node. State, load, workloads, the actions (cordon, drain, maintenance, delete) and the self-check.
export function NodeDetailPage() {
  const { org, node: id } = useParams({ from: '/o/$org/nodes/$node' })
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const manage = useAccess('node:manage') === 'allow'
  const [dialog, setDialog] = useState<Dialog>(null)

  const nodeQuery = useQuery({
    queryKey: ['node', id],
    queryFn: () => unwrap(api.GET('/nodes/{node_id}', { params: { path: { node_id: id } } })),
    refetchInterval: 4_000,
  })
  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['node', id] }),
      queryClient.invalidateQueries({ queryKey: ['nodes'] }),
    ])
  }
  const path = { params: { path: { node_id: id } } }
  const cordon = useMutation({
    mutationFn: () => unwrap(api.POST('/nodes/{node_id}/cordon', path)),
    onSuccess: refresh,
  })
  const uncordon = useMutation({
    mutationFn: () => unwrap(api.POST('/nodes/{node_id}/uncordon', path)),
    onSuccess: refresh,
  })
  const drain = useMutation({
    mutationFn: () => unwrap(api.POST('/nodes/{node_id}/drain', path)),
    onSuccess: async () => {
      setDialog(null)
      await refresh()
    },
  })
  const maintenance = useMutation({
    mutationFn: (enabled: boolean) =>
      unwrap(api.POST('/nodes/{node_id}/maintenance', { ...path, body: { enabled } })),
    onSuccess: refresh,
  })
  const selfCheck = useMutation({
    mutationFn: () => unwrap(api.POST('/nodes/{node_id}/self-check', path)),
  })
  const remove = useMutation({
    mutationFn: (force: boolean) =>
      unwrap(
        api.DELETE('/nodes/{node_id}', { params: { path: { node_id: id }, query: { force } } }),
      ),
    onSuccess: async () => {
      setDialog(null)
      await queryClient.invalidateQueries({ queryKey: ['nodes'] })
      await navigate({ to: '/o/$org/nodes', params: { org } })
    },
  })

  if (nodeQuery.isPending) return <Skeleton />
  if (nodeQuery.error instanceof ApiError && nodeQuery.error.status === 404) return <NotFoundPage />
  if (nodeQuery.isError)
    return <ErrorBanner error={nodeQuery.error} onRetry={() => void nodeQuery.refetch()} />
  const node = nodeQuery.data
  const state = node.state ?? ''
  const workloads = (node.workloads ?? []).filter((w) => w.role !== 'stopped')
  const notEmpty = workloads.length > 0
  const actionError = [cordon, uncordon, maintenance, selfCheck].find((m) => m.error)?.error
  const sinceBeat = node.last_heartbeat_at ? formatRelative(node.last_heartbeat_at) : '-'

  const reasonFor = (needs: string[], text: string) => (needs.includes(state) ? undefined : text)
  const actions = manage ? (
    <>
      <Button
        loading={cordon.isPending}
        reason={reasonFor(['ready'], t('nodes.reason_cordon'))}
        onClick={() => cordon.mutate()}
      >
        {t('nodes.cordon')}
      </Button>
      <Button
        loading={uncordon.isPending}
        reason={reasonFor(['cordoned'], t('nodes.reason_uncordon'))}
        onClick={() => uncordon.mutate()}
      >
        {t('nodes.uncordon')}
      </Button>
      <Button
        reason={reasonFor(['ready', 'cordoned'], t('nodes.reason_drain'))}
        onClick={() => setDialog('drain')}
      >
        {t('nodes.drain')}
      </Button>
      {state === 'maintenance' ? (
        <Button loading={maintenance.isPending} onClick={() => maintenance.mutate(false)}>
          {t('nodes.maintenance_off')}
        </Button>
      ) : (
        <Button
          loading={maintenance.isPending}
          reason={reasonFor(['cordoned', 'draining'], t('nodes.reason_maintenance'))}
          onClick={() => maintenance.mutate(true)}
        >
          {t('nodes.maintenance_on')}
        </Button>
      )}
      <Button variant="destructive" onClick={() => setDialog('delete')}>
        {t('common.delete')}
      </Button>
    </>
  ) : undefined

  return (
    <section>
      <PageHeader
        title={node.name ?? id}
        breadcrumb={
          <Link to="/o/$org/nodes" params={{ org }} className="text-primary underline">
            {t('nodes.title')}
          </Link>
        }
        badges={
          <StatusBadge tone={nodeTone(node.state)} label={tState('status.node', node.state)} />
        }
        actions={actions}
      />
      <div className="mb-4 space-y-3">
        {(state === 'down' || state === 'suspect') && (
          <Banner
            variant={state === 'down' ? 'danger' : 'warning'}
            title={tState('status.node', state)}
          >
            {t('nodes.no_heartbeat', { since: sinceBeat })}
          </Banner>
        )}
        {state === 'draining' && (
          <Banner title={t('nodes.draining_title')}>
            {node.drain_note
              ? t('nodes.drain_left', { left: node.drain_note })
              : t('nodes.draining_text')}
          </Banner>
        )}
        {actionError && <ErrorBanner error={actionError} title={t('nodes.action_failed')} />}
      </div>
      <SectionTabs
        label={t('settings.sections')}
        sections={[
          {
            id: 'overview',
            label: t('nodes.details'),
            content: (
              <div className="grid gap-4 lg:grid-cols-2">
                <Card className="space-y-3 lg:col-span-2">
                  <h2 className="font-semibold">{t('nodes.load')}</h2>
                  <NodeBars node={node} />
                </Card>
                <Card className="space-y-1 text-sm">
                  <h2 className="font-semibold">{t('nodes.details')}</h2>
                  <p>
                    {t('nodes.heartbeat')}:{' '}
                    {node.last_heartbeat_at ? <Time value={node.last_heartbeat_at} /> : '-'}
                  </p>
                  <p>
                    {t('nodes.public_ip')}: {node.public_ip ?? '-'}
                  </p>
                  <p>
                    {t('nodes.mesh_ip')}: {node.mesh_ip ?? '-'} ({node.mesh_subnet ?? '-'})
                  </p>
                  <p>
                    {t('nodes.os')}: {node.os ?? '-'} {node.arch ?? ''}
                  </p>
                  <p>
                    {t('nodes.agent')}: {node.agent_version ?? '-'}
                  </p>
                  <p>
                    {t('nodes.docker')}: {node.docker_version ?? '-'}
                  </p>
                </Card>
                <Card className="space-y-2">
                  <h2 className="font-semibold">{t('nodes.self_check')}</h2>
                  <Hint>{t('nodes.self_check_hint')}</Hint>
                  {manage && (
                    <Button loading={selfCheck.isPending} onClick={() => selfCheck.mutate()}>
                      {t('nodes.run_self_check')}
                    </Button>
                  )}
                  {selfCheck.data && (
                    <ul className="space-y-1 text-sm" aria-live="polite">
                      <li>Docker: {selfCheck.data.docker ? t('nodes.ok') : t('nodes.failed')}</li>
                      <li>Caddy: {selfCheck.data.caddy ? t('nodes.ok') : t('nodes.failed')}</li>
                      <li>
                        WireGuard: {selfCheck.data.wireguard ? t('nodes.ok') : t('nodes.failed')}
                      </li>
                      {(selfCheck.data as { errors?: string[] }).errors?.map((e) => (
                        <li key={e} className="text-danger">
                          {e}
                        </li>
                      ))}
                    </ul>
                  )}
                </Card>
              </div>
            ),
          },
          {
            id: 'metrics',
            label: t('nodes.metrics'),
            content: (
              <Card className="space-y-3">
                <h2 className="font-semibold">{t('nodes.metrics')}</h2>
                <Suspense fallback={<Skeleton />}>
                  <MetricsPanel kind="node" id={id} />
                </Suspense>
              </Card>
            ),
          },
          {
            id: 'labels',
            label: t('nodes.labels_column'),
            content: (
              <Card className="space-y-2">
                <LabelsForm
                  key={labelsText(node.labels)}
                  id={id}
                  labels={node.labels}
                  disabled={!manage}
                />
              </Card>
            ),
          },
          {
            id: 'workloads',
            label: t('nodes.workloads'),
            content: (
              <>
                <div className="space-y-2">
                  <h2 className="font-semibold">{t('nodes.workloads')}</h2>
                  {(node.workloads ?? []).length === 0 ? (
                    <EmptyState
                      title={t('nodes.no_workloads')}
                      text={t('nodes.no_workloads_text')}
                    />
                  ) : (
                    <DataList
                      caption={t('nodes.workloads')}
                      rows={node.workloads ?? []}
                      rowKey={(w) => w.placement_id ?? ''}
                      columns={[
                        { header: t('nodes.service'), primary: true, cell: (w) => w.service_name },
                        { header: t('nodes.role'), cell: (w) => tState('status.role', w.role) },
                        {
                          header: t('nodes.health'),
                          cell: (w) => tState('status.health', w.health),
                        },
                      ]}
                    />
                  )}
                </div>
                <div className="lg:col-span-2">
                  <OrphanVolumes id={id} canDelete={manage} />
                </div>
              </>
            ),
          },
        ]}
      />
      {dialog === 'drain' && (
        <ConfirmDialog
          title={t('nodes.drain_title')}
          text={t('nodes.drain_text', { name: node.name ?? id })}
          confirmLabel={t('nodes.drain')}
          busy={drain.isPending}
          error={drain.error ? <ErrorBanner error={drain.error} /> : undefined}
          onConfirm={() => drain.mutate()}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog === 'delete' && (
        <TypedConfirmDialog
          title={t('nodes.delete_title')}
          text={
            notEmpty
              ? t('nodes.delete_text_force', { count: workloads.length })
              : t('nodes.delete_text')
          }
          confirmLabel={notEmpty ? t('nodes.delete_force') : t('common.delete')}
          typed={node.name ?? id}
          busy={remove.isPending}
          error={remove.error ? <ErrorBanner error={remove.error} /> : undefined}
          onConfirm={() => remove.mutate(notEmpty)}
          onClose={() => {
            setDialog(null)
            remove.reset()
          }}
        />
      )}
    </section>
  )
}
