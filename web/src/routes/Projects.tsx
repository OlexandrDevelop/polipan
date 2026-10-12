// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { Onboarding } from '@/components/Onboarding'
import { useAlertInstances } from '@/alerts'
import { PageHeader } from '@/components/PageHeader'
import { Counter, Sparkline, useHistory } from '@/components/Live'
import {
  Button,
  Card,
  EmptyState,
  Meter,
  Panel,
  StatCard,
  Skeleton,
  StatusBadge,
  TextField,
} from '@/components/ui'
import { DataList } from '@/components/DataList'
import { t, tState } from '@/i18n'
import { useAccess } from '@/permissions'
import { useNodes, useProjects } from '@/scope'
import type { Node } from '@/scope'
import { nodeTone } from '@/status'
import { VerifiedBadge, worstPolicy } from '@/routes/ServiceBackups'

function CreateProjectDialog({ onClose }: { onClose: () => void }) {
  const { org } = useParams({ from: '/o/$org' })
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [name, setName] = useState('')

  const create = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/projects', { body: { name: name.trim(), default_environments: true } })),
    onSuccess: async (project) => {
      await queryClient.invalidateQueries({ queryKey: ['projects'] })
      await navigate({ to: '/o/$org/p/$project', params: { org, project: project.slug ?? '' } })
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    create.mutate()
  }

  return (
    <Modal title={t('projects.create_title')} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        {create.error && <ErrorBanner error={create.error} title={t('projects.create_failed')} />}
        <TextField
          label={t('projects.name')}
          hint={t('projects.name_hint')}
          required
          autoFocus
          autoComplete="off"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type="submit" variant="primary" loading={create.isPending}>
            {t('projects.create')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

// The projects as cards, or the empty state with the "create project" action.
function ProjectCards({ onCreate, narrow }: { onCreate: () => void; narrow?: boolean }) {
  const { org } = useParams({ from: '/o/$org' })
  const projects = useProjects()
  const canCreate = useAccess('project:create')

  if (projects.isPending) return <Skeleton />
  if (projects.isError) {
    return <ErrorBanner error={projects.error} onRetry={() => void projects.refetch()} />
  }
  if (projects.data.length === 0) {
    return (
      <EmptyState
        title={t('projects.empty_title')}
        text={t('projects.empty_text')}
        action={
          canCreate === 'allow' ? (
            <Button variant="primary" onClick={onCreate}>
              {t('projects.create')}
            </Button>
          ) : undefined
        }
      />
    )
  }
  return (
    <ul className={`grid gap-4 ${narrow ? '' : 'sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4'}`}>
      {projects.data.map((project) => (
        <li key={project.id}>
          <Card>
            <h3 className="break-words text-lg font-bold">
              <Link
                to="/o/$org/p/$project"
                params={{ org, project: project.slug ?? '' }}
                className="text-primary underline"
              >
                {project.name}
              </Link>
            </h3>
            <p className="text-muted">{project.slug}</p>
          </Card>
        </li>
      ))}
    </ul>
  )
}

export function ProjectsPage() {
  const [creating, setCreating] = useState(false)
  const canCreate = useAccess('project:create')
  return (
    <section>
      <PageHeader
        title={t('projects.title')}
        actions={
          canCreate === 'allow' && (
            <Button variant="primary" onClick={() => setCreating(true)}>
              {t('projects.create')}
            </Button>
          )
        }
      />
      <ProjectCards onCreate={() => setCreating(true)} />
      {creating && <CreateProjectDialog onClose={() => setCreating(false)} />}
    </section>
  )
}

// The firing alerts card of the dashboard (UI-03).
function AlertsCard({ org }: { org: string }) {
  const alerts = useAlertInstances()
  const firing = (alerts.data ?? []).filter((a) => a.state === 'firing')
  return (
    <StatCard
      label={t('dashboard.alerts')}
      tone={firing.length > 0 ? 'danger' : undefined}
      note={
        <div className="space-y-1">
          {alerts.isError && (
            <ErrorBanner error={alerts.error} onRetry={() => void alerts.refetch()} />
          )}
          {alerts.data && firing.length === 0 && <p>{t('dashboard.all_quiet')}</p>}
          {firing.length > 0 && (
            <StatusBadge tone="danger" label={t('dashboard.firing', { count: firing.length })} />
          )}
          <Link to="/o/$org/alerts" params={{ org }} className="block text-primary underline">
            {t('dashboard.alerts')} →
          </Link>
        </div>
      }
    >
      {alerts.isPending ? '-' : <Counter value={firing.length} />}
    </StatCard>
  )
}

// The backup card of the dashboard: the restore-verified badge of the policy that needs the most
// attention, and the warnings "no backup" and "restore not verified" (UI-03, REQ-UI-033).
function BackupsCard({ org }: { org: string }) {
  const policies = useQuery({
    queryKey: ['backup-policies', 'all'],
    queryFn: () => unwrap(api.GET('/backup-policies')),
    refetchInterval: 15_000,
  })
  const worst = worstPolicy(policies.data ?? [])
  return (
    <StatCard
      label={t('dashboard.backups')}
      tone={policies.data && !worst ? 'warning' : undefined}
      note={
        <div className="space-y-1">
          {policies.isError && (
            <ErrorBanner error={policies.error} onRetry={() => void policies.refetch()} />
          )}
          <Link to="/o/$org/backups" params={{ org }} className="block text-primary underline">
            {t('dashboard.backups')} →
          </Link>
        </div>
      }
    >
      <span className="text-base">
        {policies.isPending && '-'}
        {policies.data && !worst && <StatusBadge tone="warning" label={t('backup.badge.none')} />}
        {worst && <VerifiedBadge policy={worst} />}
      </span>
    </StatCard>
  )
}

// UI-03 (basic): the nodes by state, alerts, backups and the projects. Each part shows its own error.
// One node of the dashboard "top" view: state, CPU, memory and disk as text meters, and a
// sparkline of the CPU values seen while the page is open.
function NodeCpu({ node }: { node: Node }) {
  const cpu = node.capacity?.cpu_millis
    ? Math.round(((node.usage?.cpu_millis ?? 0) / node.capacity.cpu_millis) * 100)
    : undefined
  const history = useHistory(cpu)
  if (cpu === undefined) return <span>-</span>
  return (
    <span className="inline-flex items-center gap-2 whitespace-nowrap">
      <Meter value={cpu} label={t('nodes.cpu')} />
      <Sparkline values={history} className="hidden text-primary xl:block" />
    </span>
  )
}

function percentOf(used?: number, total?: number): number | undefined {
  return used !== undefined && total ? Math.round((used / total) * 100) : undefined
}

function NodesTable({ org, nodes }: { org: string; nodes: Node[] }) {
  return (
    <DataList
      caption={t('dashboard.nodes')}
      rows={nodes}
      rowKey={(n) => n.id ?? ''}
      columns={[
        {
          header: t('nodes.name'),
          primary: true,
          cell: (n) => (
            <Link
              to="/o/$org/nodes/$node"
              params={{ org, node: n.id ?? '' }}
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
        { header: t('nodes.cpu'), cell: (n) => <NodeCpu node={n} /> },
        {
          header: t('nodes.memory'),
          cardHidden: true,
          cell: (n) => {
            const v = percentOf(n.usage?.memory_bytes, n.capacity?.memory_bytes)
            return v === undefined ? '-' : <Meter value={v} label={t('nodes.memory')} />
          },
        },
        {
          header: t('nodes.disk'),
          cardHidden: true,
          cell: (n) => {
            const v = percentOf(n.usage?.disk_bytes, n.capacity?.disk_bytes)
            return v === undefined ? '-' : <Meter value={v} label={t('nodes.disk')} />
          },
        },
      ]}
    />
  )
}

// UI-03, the "top" view: overview tiles with big numbers, a nodes panel with meters, and the
// projects.
export function DashboardPage() {
  const { org } = useParams({ from: '/o/$org' })
  const [creating, setCreating] = useState(false)
  const nodes = useNodes(10_000)
  const projects = useProjects()
  const canCreate = useAccess('project:create')

  const byState = new Map<string, number>()
  for (const node of nodes.data ?? []) {
    const state = node.state ?? 'pending'
    byState.set(state, (byState.get(state) ?? 0) + 1)
  }

  return (
    <section className="space-y-8">
      <PageHeader title={t('dashboard.title')} />
      <Onboarding />
      <div className="grid gap-5 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          label={t('dashboard.projects')}
          note={
            <Link to="/o/$org/projects" params={{ org }} className="text-primary underline">
              {t('dashboard.projects')} →
            </Link>
          }
        >
          {projects.isError ? (
            <ErrorBanner error={projects.error} onRetry={() => void projects.refetch()} />
          ) : (
            <Counter value={projects.data?.length ?? '-'} />
          )}
        </StatCard>
        <StatCard
          label={t('dashboard.nodes')}
          tone={
            nodes.data && byState.size > 0 && (byState.get('ready') ?? 0) < nodes.data.length
              ? 'warning'
              : undefined
          }
          note={
            <div className="space-y-1">
              {nodes.isError && (
                <ErrorBanner error={nodes.error} onRetry={() => void nodes.refetch()} />
              )}
              {nodes.data && byState.size === 0 && <p>{t('dashboard.no_nodes')}</p>}
              <ul className="flex flex-wrap gap-x-3 gap-y-1">
                {[...byState.entries()].map(([state, count]) => (
                  <li key={state} className="flex items-center gap-1">
                    <StatusBadge tone={nodeTone(state)} label={tState('status.node', state)} />
                    <span className="font-term font-bold">{count}</span>
                  </li>
                ))}
              </ul>
              <Link to="/o/$org/nodes" params={{ org }} className="text-primary underline">
                {t('dashboard.nodes')} →
              </Link>
            </div>
          }
        >
          {nodes.isPending ? (
            '-'
          ) : (
            <>
              <Counter value={byState.get('ready') ?? 0} />
              <span className="text-base text-muted">/{nodes.data?.length ?? 0}</span>
            </>
          )}
        </StatCard>
        <AlertsCard org={org} />
        <BackupsCard org={org} />
      </div>
      <div className="grid items-start gap-8 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        {/* The panel is there while the nodes load, so the page below does not jump. */}
        {(nodes.isPending || (nodes.data?.length ?? 0) > 0) && (
          <Panel title={t('dashboard.nodes')} hint={t('dashboard.refresh_hint')}>
            {nodes.isPending ? (
              <Skeleton lines={3} />
            ) : (
              <NodesTable org={org} nodes={nodes.data ?? []} />
            )}
          </Panel>
        )}
        <Panel title={t('dashboard.projects')} className="space-y-4">
          {canCreate === 'allow' && (projects.data?.length ?? 0) > 0 && (
            <div className="flex justify-end">
              <Button variant="primary" onClick={() => setCreating(true)}>
                {t('projects.create')}
              </Button>
            </div>
          )}
          <ProjectCards narrow onCreate={() => setCreating(true)} />
        </Panel>
      </div>
      {creating && <CreateProjectDialog onClose={() => setCreating(false)} />}
    </section>
  )
}
