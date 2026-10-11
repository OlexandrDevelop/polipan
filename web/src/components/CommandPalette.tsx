// SPDX-License-Identifier: Apache-2.0

import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useMe } from '@/auth'
import { filterItems, SearchDialog } from '@/components/SearchList'
import type { SearchItem } from '@/components/SearchList'
import { StatusBadge } from '@/components/ui'
import { pushRecent, readContext, readRecent } from '@/context'
import { t, tState } from '@/i18n'
import { accessFor } from '@/permissions'
import type { Permission } from '@/permissions'
import { useEnvironments, useProjects, useServices } from '@/scope'
import type { Environment, Service } from '@/scope'
import { serviceTone } from '@/status'
import { useTheme } from '@/theme'

interface PaletteProps {
  org: string
  projectSlug?: string
  envSlug?: string
  onClose: () => void
  onSignOut: () => void
}

// UI-42 and REQ-UI-019: the command palette (Ctrl+K or Cmd+K). It searches pages, projects,
// environments and services from the cached lists and runs commands the role allows.
// "> text" shows commands only, "/ text" shows pages only.
export function CommandPalette({ org, projectSlug, envSlug, onClose, onSignOut }: PaletteProps) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const me = useMe()
  const role = me.data?.org_role
  const theme = useTheme()
  const projects = useProjects()
  const last = readContext(me.data?.user?.id)
  const contextProject = projectSlug ?? last?.project
  const contextEnv = projectSlug ? envSlug : last?.env
  const project = projects.data?.find((p) => p.slug === contextProject)
  const envs = useEnvironments(project?.id)
  const env =
    envs.data?.find((e) => e.slug === contextEnv) ?? (contextEnv ? undefined : envs.data?.[0])
  // The services of the current environment are read on demand; others come from the cache.
  const services = useServices(env?.id)

  function all(): SearchItem[] {
    // Each page lists the permission it needs. A page the role cannot open is not shown.
    const pageDefs: {
      id: string
      label: string
      to: string
      need?: Permission
      search?: boolean
    }[] = [
      { id: 'dashboard', label: t('nav.dashboard'), to: '/o/$org' },
      { id: 'projects', label: t('nav.projects'), to: '/o/$org/projects' },
      { id: 'deployments', label: t('nav.deployments'), to: '/o/$org/deployments' },
      { id: 'previews', label: t('nav.previews'), to: '/o/$org/previews', search: true },
      { id: 'nodes', label: t('nav.nodes'), to: '/o/$org/nodes' },
      { id: 'catalog', label: t('nav.catalog'), to: '/o/$org/catalog' },
      { id: 'backups', label: t('nav.backups'), to: '/o/$org/backups' },
      { id: 'alerts', label: t('nav.alerts'), to: '/o/$org/alerts' },
      { id: 'logs', label: t('nav.logs'), to: '/o/$org/logs', search: true },
      { id: 'profile', label: t('nav.profile'), to: '/o/$org/settings/profile' },
      { id: 'tokens', label: t('nav.tokens'), to: '/o/$org/settings/tokens', need: 'token:own' },
      {
        id: 'members',
        label: t('nav.members'),
        to: '/o/$org/settings/members',
        need: 'member:read',
      },
      { id: 'teams', label: t('nav.teams'), to: '/o/$org/settings/teams', need: 'team:manage' },
      { id: 'audit', label: t('nav.audit'), to: '/o/$org/settings/audit', need: 'audit:read' },
      {
        id: 'alert-settings',
        label: t('alerts.editor_title'),
        to: '/o/$org/settings/alerts',
        need: 'alert:manage',
      },
      {
        id: 'backup-targets',
        label: t('target.title'),
        to: '/o/$org/settings/backup-targets',
        need: 'backup:manage',
      },
      { id: 'git', label: t('git.title'), to: '/o/$org/settings/git', need: 'git:manage' },
      {
        id: 'organization',
        label: t('nav.organization'),
        to: '/o/$org/settings/organization',
        need: 'org:update',
      },
      { id: 'sso', label: t('nav.sso'), to: '/o/$org/settings/sso', need: 'sso:manage' },
    ]
    const pages: SearchItem[] = pageDefs
      .filter((p) => !p.need || accessFor(role, p.need) === 'allow')
      .map((p) => ({
        id: `page:${p.id}`,
        group: t('palette.pages'),
        label: p.label,
        run: () =>
          void navigate({
            to: p.to,
            params: { org },
            ...(p.search ? { search: {} } : {}),
          } as never),
      }))

    const commands: SearchItem[] = []
    const createAccess = accessFor(role, 'service:create', env?.protected)
    if (createAccess !== 'hidden' && project && env) {
      commands.push({
        id: 'cmd:add-service',
        group: t('palette.commands'),
        label: t('service.add'),
        detail: `${project.name} › ${env.name}`,
        reason: createAccess === 'blocked' ? t('common.needs_admin') : undefined,
        run: () =>
          void navigate({
            to: '/o/$org/p/$project/e/$env/new',
            params: { org, project: project.slug ?? '', env: env.slug ?? '' },
            search: {},
          }),
      })
    }
    if (accessFor(role, 'node:manage') !== 'hidden') {
      commands.push({
        id: 'cmd:add-node',
        group: t('palette.commands'),
        label: t('nodes.add'),
        run: () => void navigate({ to: '/o/$org/nodes', params: { org } }),
      })
    }
    // Deploy and roll back ask for the service in the same step: one command per service.
    for (const svc of services.data ?? []) {
      if (!project || !env || !svc.name) continue
      const base = { org, project: project.slug ?? '', env: env.slug ?? '', service: svc.name }
      const deployAccess = accessFor(role, 'service:deploy', env.protected)
      if (deployAccess !== 'hidden') {
        commands.push({
          id: `cmd:deploy:${svc.id}`,
          group: t('palette.commands'),
          label: t('palette.deploy', { name: svc.name }),
          detail: `${project.name} › ${env.name}`,
          reason: deployAccess === 'blocked' ? t('common.needs_admin') : undefined,
          run: () =>
            void navigate({
              to: '/o/$org/p/$project/e/$env/s/$service',
              params: base,
              search: { dialog: 'deploy' },
            }),
        })
      }
      const rollbackAccess = accessFor(role, 'service:rollback', env.protected)
      if (rollbackAccess !== 'hidden') {
        commands.push({
          id: `cmd:rollback:${svc.id}`,
          group: t('palette.commands'),
          label: t('palette.rollback', { name: svc.name }),
          detail: `${project.name} › ${env.name}`,
          reason: rollbackAccess === 'blocked' ? t('common.needs_admin') : undefined,
          run: () =>
            void navigate({
              to: '/o/$org/p/$project/e/$env/s/$service',
              params: base,
              search: { dialog: 'rollback' },
            }),
        })
      }
      commands.push({
        id: `cmd:metrics:${svc.id}`,
        group: t('palette.commands'),
        label: t('palette.metrics', { name: svc.name }),
        detail: `${project.name} › ${env.name}`,
        run: () =>
          void navigate({
            to: '/o/$org/p/$project/e/$env/s/$service/$tab',
            params: { ...base, tab: 'metrics' },
          }),
      })
    }
    commands.push(
      {
        id: 'cmd:theme',
        group: t('palette.commands'),
        label: t('palette.switch_theme'),
        run: () => {
          const order = ['system', 'light', 'dark'] as const
          theme.setChoice(order[(order.indexOf(theme.choice) + 1) % order.length])
        },
      },
      {
        id: 'cmd:sign-out',
        group: t('palette.commands'),
        label: t('nav.sign_out'),
        run: onSignOut,
      },
    )

    const entities: SearchItem[] = []
    const projectById = new Map((projects.data ?? []).map((p) => [p.id, p]))
    for (const p of projects.data ?? []) {
      entities.push({
        id: `project:${p.id}`,
        group: t('palette.projects'),
        label: p.name ?? '',
        run: () =>
          void navigate({ to: '/o/$org/p/$project', params: { org, project: p.slug ?? '' } }),
      })
    }
    const envById = new Map<string, Environment>()
    for (const [key, data] of queryClient.getQueriesData<Environment[]>({
      queryKey: ['environments'],
    })) {
      const p = projectById.get(key[1] as string)
      if (!p || !Array.isArray(data)) continue
      for (const e of data) {
        if (e.id) envById.set(e.id, e)
        entities.push({
          id: `env:${e.id}`,
          group: t('palette.environments'),
          label: `${p.name} › ${e.name}`,
          run: () =>
            void navigate({
              to: '/o/$org/p/$project/e/$env',
              params: { org, project: p.slug ?? '', env: e.slug ?? '' },
            }),
        })
      }
    }
    for (const [, data] of queryClient.getQueriesData<Service[]>({ queryKey: ['services'] })) {
      if (!Array.isArray(data)) continue
      for (const s of data) {
        const e = envById.get(s.environment_id ?? '')
        const p = e ? projectById.get(e.project_id) : undefined
        if (!e || !p) continue
        entities.push({
          id: `service:${s.id}`,
          group: t('palette.services'),
          label: s.name ?? '',
          detail: `${p.name} › ${e.name}`,
          badges: (
            <StatusBadge tone={serviceTone(s.status)} label={tState('status.service', s.status)} />
          ),
          run: () =>
            void navigate({
              to: '/o/$org/p/$project/e/$env/s/$service',
              params: { org, project: p.slug ?? '', env: e.slug ?? '', service: s.name ?? '' },
            }),
        })
      }
    }
    return [...commands, ...entities, ...pages]
  }

  function items(raw: string): SearchItem[] {
    let list = all()
    let query = raw
    if (raw.startsWith('>')) {
      list = list.filter((i) => i.id.startsWith('cmd:'))
      query = raw.slice(1)
    } else if (raw.startsWith('/')) {
      list = list.filter((i) => i.id.startsWith('page:'))
      query = raw.slice(1)
    }
    const remember = (item: SearchItem): SearchItem => ({
      ...item,
      run: () => {
        pushRecent(item.id.replace(/^recent:/, ''))
        item.run()
      },
    })
    if (query.trim() === '') {
      const recent = readRecent()
      const recentItems = recent
        .map((id) => list.find((i) => i.id === id))
        .filter((i): i is SearchItem => Boolean(i))
        .map((i) => ({ ...i, id: `recent:${i.id}`, group: t('palette.recent') }))
      return [...recentItems, ...list].map(remember)
    }
    // The last row always searches the logs for the text (navigation, section 4).
    const searchLogs: SearchItem = {
      id: 'page:logs-search',
      group: t('palette.pages'),
      label: t('palette.search_logs', { text: query.trim() }),
      run: () =>
        void navigate({ to: '/o/$org/logs', params: { org }, search: { q: query.trim() } }),
    }
    return [...filterItems(list, query), searchLogs].map(remember)
  }

  return (
    <SearchDialog
      label={t('palette.label')}
      placeholder={t('palette.placeholder')}
      items={items}
      empty={(q) => t('palette.empty', { text: q })}
      onClose={onClose}
    />
  )
}
