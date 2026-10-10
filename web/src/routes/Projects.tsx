// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import { Button, Card, EmptyState, Skeleton, StatusBadge, TextField } from '@/components/ui'
import { t, tState } from '@/i18n'
import { useAccess } from '@/permissions'
import { useNodes, useProjects } from '@/scope'
import { nodeTone } from '@/status'

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
    <ul className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
      {projects.data.map((project) => (
        <li key={project.id}>
          <Card>
            <h2 className="break-words text-lg font-semibold">
              <Link
                to="/o/$org/p/$project"
                params={{ org, project: project.slug ?? '' }}
                className="text-primary underline"
              >
                {project.name}
              </Link>
            </h2>
            <p className="text-sm text-muted">{project.slug}</p>
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

// UI-03 (basic): the nodes by state and the projects. Each part shows its own error.
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
    <section className="space-y-6">
      <PageHeader title={t('dashboard.title')} />
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Card>
          <h2 className="font-semibold">{t('dashboard.projects')}</h2>
          <p className="text-2xl">{projects.data?.length ?? '-'}</p>
        </Card>
        <Card className="space-y-2">
          <h2 className="font-semibold">
            <Link to="/o/$org/nodes" params={{ org }} className="text-primary underline">
              {t('dashboard.nodes')}
            </Link>
          </h2>
          {nodes.isPending && <Skeleton lines={1} />}
          {nodes.isError && (
            <ErrorBanner error={nodes.error} onRetry={() => void nodes.refetch()} />
          )}
          {nodes.data && byState.size === 0 && (
            <p className="text-muted">{t('dashboard.no_nodes')}</p>
          )}
          <ul className="space-y-1">
            {[...byState.entries()].map(([state, count]) => (
              <li key={state} className="flex items-center gap-2">
                <StatusBadge tone={nodeTone(state)} label={tState('status.node', state)} />
                <span>{count}</span>
              </li>
            ))}
          </ul>
        </Card>
      </div>
      <div className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-lg font-semibold">{t('dashboard.projects')}</h2>
          {canCreate === 'allow' && (projects.data?.length ?? 0) > 0 && (
            <Button variant="primary" onClick={() => setCreating(true)}>
              {t('projects.create')}
            </Button>
          )}
        </div>
        <ProjectCards onCreate={() => setCreating(true)} />
      </div>
      {creating && <CreateProjectDialog onClose={() => setCreating(false)} />}
    </section>
  )
}
