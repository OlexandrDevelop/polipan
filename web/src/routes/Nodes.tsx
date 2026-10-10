// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { CodeBlock } from '@/components/Copy'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { NodeBars } from '@/components/NodeBars'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import { Banner, Button, EmptyState, Skeleton, StatusBadge, TextField } from '@/components/ui'
import { formatRelative, t, tState } from '@/i18n'
import { useAccess } from '@/permissions'
import { useNodes } from '@/scope'
import { nodeTone } from '@/status'

const PENDING_HELP_AFTER_MS = 5 * 60_000

// One label per line: key=value. Returns null when a line is wrong.
function parseLabels(text: string): Record<string, string> | null {
  const labels: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (line === '') continue
    const at = line.indexOf('=')
    if (at < 1) return null
    labels[line.slice(0, at).trim()] = line.slice(at + 1).trim()
  }
  return labels
}

function useNow(everyMs: number): number {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), everyMs)
    return () => clearInterval(id)
  }, [everyMs])
  return now
}

interface JoinToken {
  install_command?: string
  expires_at?: string
}

// The row of the new node: it shows up as pending when the agent joins and turns ready after its first hello
// (UI-31 step 2, flows section 4). The list reads every 3 s.
function LiveJoin({ name }: { name: string }) {
  const nodes = useNodes(3_000)
  const node = nodes.data?.find((n) => n.name === name)
  return (
    <div
      className="flex flex-wrap items-center gap-2 rounded-sm border border-border p-3"
      aria-live="polite"
    >
      <span className="font-medium">{name}</span>
      {node ? (
        <StatusBadge tone={nodeTone(node.state)} label={tState('status.node', node.state)} />
      ) : (
        <span className="text-sm text-muted">{t('nodes.waiting')}</span>
      )}
      {node?.state === 'ready' && (
        <span className="text-sm">{t('nodes.joined', { ip: node.mesh_ip ?? '-' })}</span>
      )}
    </div>
  )
}

// The install command is shown once, with the time left (flows, section 4).
function JoinCommand({
  token,
  name,
  onNew,
}: {
  token: JoinToken
  name: string
  onNew: () => void
}) {
  const now = useNow(15_000)
  const [created] = useState(Date.now())
  const left = token.expires_at ? new Date(token.expires_at).getTime() - now : 0
  const expired = left <= 0
  const minutes = Math.max(1, Math.ceil(left / 60_000))

  if (expired) {
    return (
      <div className="space-y-4">
        <Banner variant="warning" title={t('nodes.expired_title')}>
          {t('nodes.expired_text')}
        </Banner>
        <Button variant="primary" onClick={onNew}>
          {t('nodes.new_command')}
        </Button>
      </div>
    )
  }
  return (
    <div className="space-y-4">
      <p>{t('nodes.run_command')}</p>
      <CodeBlock text={token.install_command ?? ''} label={t('nodes.command_label')} />
      <p className="text-sm text-muted">{t('nodes.shown_once')}</p>
      <p className="text-sm">{t('nodes.expires_in', { minutes })}</p>
      <LiveJoin name={name} />
      {now - created > PENDING_HELP_AFTER_MS && (
        <Banner variant="warning" title={t('nodes.help_title')}>
          <ul className="list-disc pl-5">
            <li>{t('nodes.help_port')}</li>
            <li>{t('nodes.help_firewall')}</li>
            <li>{t('nodes.help_clock')}</li>
          </ul>
        </Banner>
      )}
    </div>
  )
}

export function AddNodeDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [labelText, setLabelText] = useState('')
  const [submitted, setSubmitted] = useState(false)
  const labels = parseLabels(labelText)

  const create = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/nodes', { body: { name: name.trim(), labels: labels ?? {} } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['nodes'] }),
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (labels && name.trim() !== '') create.mutate()
  }

  return (
    <Modal title={t('nodes.add_title')} onClose={onClose}>
      {create.data ? (
        <JoinCommand token={create.data} name={name.trim()} onNew={() => create.reset()} />
      ) : (
        <form onSubmit={submit} className="space-y-4">
          {create.error && <ErrorBanner error={create.error} title={t('nodes.add_failed')} />}
          <TextField
            label={t('nodes.name')}
            required
            autoFocus
            autoComplete="off"
            error={submitted && name.trim() === '' ? t('nodes.name_required') : null}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <div className="space-y-1">
            <label htmlFor="node-labels" className="block text-sm font-medium">
              {t('nodes.labels')}
            </label>
            <textarea
              id="node-labels"
              rows={3}
              spellCheck={false}
              aria-describedby="node-labels-hint"
              aria-invalid={submitted && !labels ? true : undefined}
              className="block w-full rounded-sm border border-border-strong bg-bg px-3 py-2 font-mono text-base text-text aria-[invalid=true]:border-danger"
              value={labelText}
              onChange={(e) => setLabelText(e.target.value)}
            />
            <p id="node-labels-hint" className="text-sm text-muted">
              {submitted && !labels ? t('nodes.labels_invalid') : t('nodes.labels_hint')}
            </p>
          </div>
          <div className="flex flex-wrap justify-end gap-2">
            <Button onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" variant="primary" loading={create.isPending}>
              {t('nodes.create_command')}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  )
}

// UI-19 (basic): the servers and the "Add node" command. The list reads every 5 s,
// so a new node turns from pending to ready without a reload.
export function NodesPage() {
  const { org } = useParams({ strict: false })
  const nodes = useNodes(5_000)
  const access = useAccess('node:manage')
  const [adding, setAdding] = useState(false)

  const history = (
    <Link to="/o/$org/nodes/history" params={{ org: org ?? '' }} className="text-primary underline">
      {t('nodes.history_link')}
    </Link>
  )
  const addButton =
    access === 'allow' ? (
      <Button variant="primary" onClick={() => setAdding(true)}>
        {t('nodes.add')}
      </Button>
    ) : undefined

  return (
    <section>
      <PageHeader
        title={t('nodes.title')}
        actions={
          <>
            {history}
            {addButton}
          </>
        }
      />
      {nodes.isPending && <Skeleton />}
      {nodes.isError && <ErrorBanner error={nodes.error} onRetry={() => void nodes.refetch()} />}
      {nodes.data && nodes.data.length === 0 && (
        <EmptyState
          title={t('nodes.empty_title')}
          text={t('nodes.empty_text')}
          action={addButton}
        />
      )}
      {nodes.data && nodes.data.length > 0 && (
        <DataList
          caption={t('nodes.caption')}
          rows={nodes.data}
          rowKey={(n) => n.id ?? ''}
          columns={[
            {
              header: t('nodes.name'),
              primary: true,
              cell: (n) => (
                <Link
                  to="/o/$org/nodes/$node"
                  params={{ org: org ?? '', node: n.id ?? '' }}
                  className="text-primary underline"
                >
                  {n.name}
                </Link>
              ),
            },
            {
              header: t('nodes.state'),
              cell: (n) => (
                <StatusBadge tone={nodeTone(n.state)} label={tState('status.node', n.state)} />
              ),
            },
            { header: t('nodes.public_ip'), cell: (n) => n.public_ip ?? '-' },
            { header: t('nodes.load'), cell: (n) => <NodeBars node={n} /> },
            { header: t('nodes.workloads'), cell: (n) => n.workload_count ?? 0 },
            {
              header: t('nodes.labels_column'),
              cardHidden: true,
              cell: (n) =>
                Object.entries(n.labels ?? {})
                  .map(([k, v]) => `${k}=${v}`)
                  .join(', ') || '-',
            },
            {
              header: t('nodes.heartbeat'),
              cardHidden: true,
              cell: (n) =>
                n.last_heartbeat_at ? (
                  <span title={n.last_heartbeat_at}>{formatRelative(n.last_heartbeat_at)}</span>
                ) : (
                  <Time />
                ),
            },
          ]}
        />
      )}
      {adding && <AddNodeDialog onClose={() => setAdding(false)} />}
    </section>
  )
}
