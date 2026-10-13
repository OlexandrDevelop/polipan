// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { CodeBlock } from '@/components/Copy'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import {
  Banner,
  Button,
  EmptyState,
  Optional,
  Skeleton,
  StatusBadge,
  TextField,
} from '@/components/ui'
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
          <Optional filled={labelText !== '' || Boolean(submitted && !labels)}>
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
          </Optional>
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
// One thin bar of a node card: the label, the percent and the fill. The tone follows the load.
function Bar({ label, used, total }: { label: string; used?: number; total?: number }) {
  const value = used !== undefined && total ? Math.round((used / total) * 100) : undefined
  const pct = Math.min(100, Math.max(0, value ?? 0))
  const tone = pct >= 90 ? 'bad' : pct >= 70 ? 'warn' : 'ok'
  return (
    <div className="bar" data-tone={value === undefined ? 'none' : tone}>
      <div className="bar-top">
        <span>{label}</span>
        <b>{value === undefined ? '-' : `${value}%`}</b>
      </div>
      <div
        className="bar-track"
        role="progressbar"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={value}
      >
        <span className="bar-fill" style={{ transform: `scaleX(${pct / 100})` }} />
      </div>
    </div>
  )
}

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
        <ul className="node-cards" aria-label={t('nodes.caption')}>
          {nodes.data.map((n) => {
            const labels = Object.entries(n.labels ?? {})
            return (
              <li key={n.id} className="node-card">
                <div className="node-card-top">
                  <h3>
                    <Link
                      to="/o/$org/nodes/$node"
                      params={{ org: org ?? '', node: n.id ?? '' }}
                      className="node-card-link"
                    >
                      {n.name}
                    </Link>
                  </h3>
                  <StatusBadge tone={nodeTone(n.state)} label={tState('status.node', n.state)} />
                </div>
                <p className="node-card-sub">
                  {n.public_ip ?? '-'}
                  {' · '}
                  {t('nodes.workloads')}: {n.workload_count ?? 0}
                  {n.last_heartbeat_at && (
                    <>
                      {' · '}
                      <span title={n.last_heartbeat_at}>{formatRelative(n.last_heartbeat_at)}</span>
                    </>
                  )}
                </p>
                <Bar
                  label={t('nodes.cpu')}
                  used={n.usage?.cpu_millis}
                  total={n.capacity?.cpu_millis}
                />
                <Bar
                  label={t('nodes.memory')}
                  used={n.usage?.memory_bytes}
                  total={n.capacity?.memory_bytes}
                />
                <Bar
                  label={t('nodes.disk')}
                  used={n.usage?.disk_bytes}
                  total={n.capacity?.disk_bytes}
                />
                {labels.length > 0 && (
                  <ul className="tile-envs">
                    {labels.map(([k, v]) => (
                      <li key={k}>
                        {k}={v}
                      </li>
                    ))}
                  </ul>
                )}
              </li>
            )
          })}
        </ul>
      )}
      {adding && <AddNodeDialog onClose={() => setAdding(false)} />}
    </section>
  )
}
