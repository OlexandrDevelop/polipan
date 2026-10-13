// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { Onboarding } from '@/components/Onboarding'
import { useAlertInstances } from '@/alerts'
import { NodeHistory } from '@/components/NodeHistory'
import { PageHeader } from '@/components/PageHeader'
import { Button, EmptyState, Skeleton, StatusBadge, TextField } from '@/components/ui'
import { t, tState } from '@/i18n'
import { useAccess } from '@/permissions'
import { useEnvironments, useNodes, useProjects } from '@/scope'
import type { Node, Project } from '@/scope'
import { nodeTone } from '@/status'
import { VerifiedBadge, worstPolicy } from '@/routes/ServiceBackups'

export function CreateProjectDialog({ onClose }: { onClose: () => void }) {
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
function ProjectCards({ onCreate }: { onCreate: () => void }) {
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
    <ul className="tiles">
      {projects.data.map((project) => (
        <li key={project.id}>
          <ProjectTile org={org} project={project} />
        </li>
      ))}
      {canCreate === 'allow' && (
        <li>
          <button type="button" className="tile tile-new" onClick={onCreate}>
            <span aria-hidden="true" className="tile-plus">
              +
            </span>
            <span>{t('projects.create')}</span>
          </button>
        </li>
      )}
    </ul>
  )
}

// One project: a letter, the name and its environments. The whole tile is the link.
function ProjectTile({ org, project }: { org: string; project: Project }) {
  const envs = useEnvironments(project.id)
  const shown = (envs.data ?? []).filter((e) => e.kind !== 'preview')
  const letter = (project.name ?? '?').trim().charAt(0).toUpperCase()
  return (
    <div className="tile">
      <span aria-hidden="true" className="tile-letter">
        {letter}
      </span>
      <h3 className="tile-name">
        <Link to="/o/$org/p/$project" params={{ org, project: project.slug ?? '' }}>
          {project.name}
        </Link>
      </h3>
      <p className="tile-slug">{project.slug}</p>
      {shown.length > 0 && (
        <ul className="tile-envs">
          {shown.map((e) => (
            <li key={e.id} data-kind={e.kind}>
              {e.name}
            </li>
          ))}
        </ul>
      )}
      <span aria-hidden="true" className="tile-go">
        →
      </span>
    </div>
  )
}

export function ProjectsPage() {
  const [creating, setCreating] = useState(false)
  return (
    <section>
      <PageHeader title={t('projects.title')} />
      <ProjectCards onCreate={() => setCreating(true)} />
      {creating && <CreateProjectDialog onClose={() => setCreating(false)} />}
    </section>
  )
}

// One fact of the quiet line under the projects: a link with a word and a value.
function Fact({
  to,
  org,
  label,
  children,
}: {
  to: '/o/$org/alerts' | '/o/$org/backups' | '/o/$org/nodes'
  org: string
  label: string
  children: ReactNode
}) {
  return (
    <Link to={to} params={{ org }} className="fact">
      <span className="fact-label">{label}</span>
      <span className="fact-value">{children}</span>
    </Link>
  )
}

function AlertsFact({ org }: { org: string }) {
  const alerts = useAlertInstances()
  const firing = (alerts.data ?? []).filter((a) => a.state === 'firing')
  return (
    <Fact to="/o/$org/alerts" org={org} label={t('dashboard.alerts')}>
      {alerts.isPending && '-'}
      {alerts.isError && '!'}
      {alerts.data && firing.length === 0 && t('dashboard.all_quiet')}
      {firing.length > 0 && (
        <StatusBadge tone="danger" label={t('dashboard.firing', { count: firing.length })} />
      )}
    </Fact>
  )
}

// The backup fact: the restore-verified badge of the policy that needs the most attention, and
// the warnings "no backup" and "restore not verified" (UI-03, REQ-UI-033).
function BackupsFact({ org }: { org: string }) {
  const policies = useQuery({
    queryKey: ['backup-policies', 'all'],
    queryFn: () => unwrap(api.GET('/backup-policies')),
    refetchInterval: 15_000,
  })
  const worst = worstPolicy(policies.data ?? [])
  return (
    <Fact to="/o/$org/backups" org={org} label={t('dashboard.backups')}>
      {policies.isPending && '-'}
      {policies.isError && '!'}
      {policies.data && !worst && <StatusBadge tone="warning" label={t('backup.badge.none')} />}
      {worst && <VerifiedBadge policy={worst} />}
    </Fact>
  )
}

// The nodes are the main thing of the dashboard: the load of the last 24 hours for each.
function NodeGauges({ org, nodes }: { org: string; nodes: Node[] }) {
  return (
    <div className="nodes">
      {nodes.map((n) => (
        <section key={n.id} className="node" aria-label={n.name}>
          <header className="node-head">
            <Link to="/o/$org/nodes/$node" params={{ org, node: n.id ?? '' }}>
              {n.name}
            </Link>
            <StatusBadge tone={nodeTone(n.state)} label={tState('status.node', n.state)} />
          </header>
          <NodeHistory node={n} />
        </section>
      ))}
    </div>
  )
}

// UI-03: the history of the nodes comes first, then one quiet line with the state of the nodes,
// alerts and backups, then the projects.
export function DashboardPage() {
  const { org } = useParams({ from: '/o/$org' })
  const [creating, setCreating] = useState(false)
  const nodes = useNodes(10_000)
  const canCreate = useAccess('project:create')

  const total = nodes.data?.length ?? 0
  const ready = (nodes.data ?? []).filter((n) => n.state === 'ready').length

  return (
    <section className="home">
      <PageHeader title={t('dashboard.title')} />
      <Onboarding />
      {nodes.isError && <ErrorBanner error={nodes.error} onRetry={() => void nodes.refetch()} />}
      {nodes.isPending && <Skeleton lines={4} />}
      {total > 0 && <NodeGauges org={org} nodes={nodes.data ?? []} />}
      <div className="facts">
        <Fact to="/o/$org/nodes" org={org} label={t('dashboard.nodes')}>
          {nodes.isPending && '-'}
          {nodes.isError && '!'}
          {nodes.data &&
            (total === 0 ? (
              t('dashboard.no_nodes')
            ) : (
              <StatusBadge
                tone={ready === total ? 'success' : 'warning'}
                label={`${ready}/${total}`}
              />
            ))}
        </Fact>
        <AlertsFact org={org} />
        <BackupsFact org={org} />
      </div>
      <ProjectList onCreate={() => setCreating(true)} canCreate={canCreate === 'allow'} />
      {creating && <CreateProjectDialog onClose={() => setCreating(false)} />}
    </section>
  )
}

// The projects as large quiet rows. A row is one link.
function ProjectList({ onCreate, canCreate }: { onCreate: () => void; canCreate: boolean }) {
  const { org } = useParams({ from: '/o/$org' })
  const projects = useProjects()
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
          canCreate ? (
            <Button variant="primary" onClick={onCreate}>
              {t('projects.create')}
            </Button>
          ) : undefined
        }
      />
    )
  }
  return (
    <div>
      <ul className="home-projects">
        {projects.data.map((project) => (
          <li key={project.id}>
            <Link to="/o/$org/p/$project" params={{ org, project: project.slug ?? '' }}>
              <span className="home-name">{project.name}</span>
              <span className="home-slug">{project.slug}</span>
              <span aria-hidden="true" className="home-go">
                →
              </span>
            </Link>
          </li>
        ))}
      </ul>
      {canCreate && (
        <button type="button" className="home-add" onClick={onCreate}>
          + {t('projects.create')}
        </button>
      )}
    </div>
  )
}
