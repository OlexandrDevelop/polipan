// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { Banner, Button, Card, SelectField, Skeleton } from '@/components/ui'
import { t, tState } from '@/i18n'
import { useAccess } from '@/permissions'
import { NotFoundPage } from '@/routes/ErrorPages'
import { isInvitation, useMembers } from '@/routes/Members'
import { useTeams } from '@/routes/Teams'
import { useProjects } from '@/scope'

type ProjectRole = components['schemas']['ProjectRole']
type OverrideRole = ProjectRole['role']

const OVERRIDE_ROLES: OverrideRole[] = ['admin', 'developer', 'viewer']

function RolesEditor({ projectId, initial }: { projectId: string; initial: ProjectRole[] }) {
  const queryClient = useQueryClient()
  const members = useMembers()
  const teams = useTeams()
  const canManage = useAccess('member:manage') === 'allow'
  // Rows are kept without project_id: the path says the project.
  const [rows, setRows] = useState<ProjectRole[]>(
    initial.map((r) => ({ user_id: r.user_id, team_id: r.team_id, role: r.role })),
  )
  const [target, setTarget] = useState('')
  const [role, setRole] = useState<OverrideRole>('developer')
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/projects/{project_id}/roles', {
          params: { path: { project_id: projectId } },
          body: rows,
        }),
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['project-roles', projectId] })
      void queryClient.invalidateQueries({ queryKey: ['me'] })
    },
  })

  const users = (members.data ?? []).filter((m) => !isInvitation(m))
  const name = (r: ProjectRole) => {
    if (r.team_id) {
      return t('roles.team_name', {
        name: teams.data?.find((x) => x.id === r.team_id)?.name ?? r.team_id,
      })
    }
    const m = users.find((x) => x.user_id === r.user_id)
    return m?.email ?? r.user_id ?? ''
  }
  const key = (r: ProjectRole) => (r.team_id ? `team:${r.team_id}` : `user:${r.user_id}`)
  const taken = new Set(rows.map(key))
  const options = [
    ...users.map((m) => ({ value: `user:${m.user_id}`, label: m.email ?? '' })),
    ...(teams.data ?? []).map((x) => ({
      value: `team:${x.id}`,
      label: t('roles.team_name', { name: x.name ?? '' }),
    })),
  ].filter((o) => !taken.has(o.value))

  function add() {
    const [kind, id] = target.split(':')
    setRows([...rows, kind === 'team' ? { team_id: id, role } : { user_id: id, role }])
    setTarget('')
  }

  return (
    <div className="space-y-4">
      {save.error && <ErrorBanner error={save.error} title={t('roles.save_failed')} />}
      {save.isSuccess && <Banner>{t('profile.saved')}</Banner>}
      {rows.length === 0 ? (
        <p className="text-muted">{t('roles.empty')}</p>
      ) : (
        <ul className="space-y-2">
          {rows.map((r, i) => (
            <li key={key(r)}>
              <Card className="flex flex-wrap items-center justify-between gap-2">
                <span className="break-all">{name(r)}</span>
                {canManage ? (
                  <div className="flex items-center gap-2">
                    <select
                      aria-label={t('roles.role_of', { name: name(r) })}
                      value={r.role}
                      onChange={(e) =>
                        setRows(
                          rows.map((x, j) =>
                            j === i ? { ...x, role: e.target.value as OverrideRole } : x,
                          ),
                        )
                      }
                      className="min-h-8 rounded-sm border border-border-strong bg-bg px-2 text-sm coarse:min-h-11"
                    >
                      {OVERRIDE_ROLES.map((o) => (
                        <option key={o} value={o}>
                          {tState('role', o)}
                        </option>
                      ))}
                    </select>
                    <Button onClick={() => setRows(rows.filter((_, j) => j !== i))}>
                      {t('common.remove')}
                    </Button>
                  </div>
                ) : (
                  <span>{tState('role', r.role)}</span>
                )}
              </Card>
            </li>
          ))}
        </ul>
      )}
      {canManage && (
        <>
          <div className="flex flex-wrap items-end gap-2">
            <div className="min-w-48 flex-1">
              <SelectField
                label={t('roles.who')}
                value={target}
                onChange={(e) => setTarget(e.target.value)}
              >
                <option value="">{t('roles.choose')}</option>
                {options.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </SelectField>
            </div>
            <SelectField
              label={t('members.role')}
              value={role}
              onChange={(e) => setRole(e.target.value as OverrideRole)}
            >
              {OVERRIDE_ROLES.map((o) => (
                <option key={o} value={o}>
                  {tState('role', o)}
                </option>
              ))}
            </SelectField>
            <Button reason={target ? undefined : t('roles.choose_first')} onClick={add}>
              {t('roles.add')}
            </Button>
          </div>
          <Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>
            {t('common.save')}
          </Button>
        </>
      )}
    </div>
  )
}

// UI-44 (part): the role overrides of one project.
export function ProjectRolesPage() {
  const { org, project: slug } = useParams({ from: '/o/$org/p/$project/roles' })
  const projects = useProjects()
  const project = projects.data?.find((p) => p.slug === slug)
  const roles = useQuery({
    queryKey: ['project-roles', project?.id],
    enabled: Boolean(project?.id),
    queryFn: () =>
      unwrap(
        api.GET('/projects/{project_id}/roles', {
          params: { path: { project_id: project?.id ?? '' } },
        }),
      ),
  })
  if (projects.isPending) return <Skeleton />
  if (projects.isError)
    return <ErrorBanner error={projects.error} onRetry={() => void projects.refetch()} />
  if (!project) return <NotFoundPage />
  return (
    <section className="max-w-3xl space-y-4">
      <PageHeader
        title={t('roles.title')}
        context={[project.name ?? '']}
        breadcrumb={
          <Link
            to="/o/$org/p/$project"
            params={{ org, project: slug }}
            className="text-primary underline"
          >
            {project.name}
          </Link>
        }
      />
      <p className="text-sm text-muted">{t('roles.intro')}</p>
      {roles.isPending && <Skeleton />}
      {roles.isError && <ErrorBanner error={roles.error} onRetry={() => void roles.refetch()} />}
      {roles.data && <RolesEditor projectId={project.id ?? ''} initial={roles.data} />}
    </section>
  )
}
