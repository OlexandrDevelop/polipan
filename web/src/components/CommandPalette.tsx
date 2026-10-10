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
  useServices(env?.id)

  function all(): SearchItem[] {
    const pages: SearchItem[] = [
      {
        id: 'page:dashboard',
        group: t('palette.pages'),
        label: t('nav.dashboard'),
        run: () => void navigate({ to: '/o/$org', params: { org } }),
      },
      {
        id: 'page:projects',
        group: t('palette.pages'),
        label: t('nav.projects'),
        run: () => void navigate({ to: '/o/$org/projects', params: { org } }),
      },
      {
        id: 'page:nodes',
        group: t('palette.pages'),
        label: t('nav.nodes'),
        run: () => void navigate({ to: '/o/$org/nodes', params: { org } }),
      },
      {
        id: 'page:profile',
        group: t('palette.pages'),
        label: t('nav.profile'),
        run: () => void navigate({ to: '/o/$org/settings/profile', params: { org } }),
      },
    ]

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
    return filterItems(list, query).map(remember)
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
