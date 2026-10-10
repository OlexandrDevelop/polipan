// SPDX-License-Identifier: Apache-2.0

import { useQueries } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { filterItems, SearchDialog } from '@/components/SearchList'
import type { SearchItem } from '@/components/SearchList'
import { EnvironmentBadges } from '@/components/ui'
import { readContext, saveContext } from '@/context'
import { t } from '@/i18n'
import { accessFor } from '@/permissions'
import { useEnvironments, useProjects } from '@/scope'

// REQ-UI-020: picks the project and the environment. Searchable, keyboard only, kind badge in
// each row, and it remembers the last choice of the user on this device.
export function ContextSwitcher({
  org,
  projectSlug,
  envSlug,
}: {
  org: string
  projectSlug?: string
  envSlug?: string
}) {
  const [open, setOpen] = useState(false)
  const me = useMe()
  const userId = me.data?.user?.id
  const navigate = useNavigate()
  const projects = useProjects()
  const list = projects.data ?? []
  const environments = useQueries({
    queries: list.map((p) => ({
      queryKey: ['environments', p.id],
      enabled: open,
      queryFn: () =>
        unwrap(
          api.GET('/projects/{project_id}/environments', {
            params: { path: { project_id: p.id ?? '' } },
          }),
        ),
    })),
  })
  const last = readContext(userId)
  const currentProject = projectSlug ?? last?.project
  const currentEnv = projectSlug ? envSlug : last?.env
  const project = list.find((p) => p.slug === currentProject)
  const projectEnvs = useEnvironments(project?.id)
  const envName = projectEnvs.data?.find((e) => e.slug === currentEnv)?.name ?? currentEnv

  const label = project
    ? envName
      ? t('switcher.current', { project: project.name ?? '', env: envName })
      : (project.name ?? '')
    : t('switcher.choose')

  function items(query: string): SearchItem[] {
    const rows: SearchItem[] = []
    list.forEach((p, i) => {
      const envs = environments[i]?.data ?? []
      for (const e of envs) {
        rows.push({
          id: `${p.slug}/${e.slug}`,
          group: e.kind === 'preview' ? t('switcher.previews') : t('switcher.environments'),
          label: `${p.name} › ${e.name}`,
          badges: <EnvironmentBadges kind={e.kind} isProtected={e.protected} />,
          run: () => {
            saveContext(userId, { project: p.slug ?? '', env: e.slug ?? '' })
            void navigate({
              to: '/o/$org/p/$project/e/$env',
              params: { org, project: p.slug ?? '', env: e.slug ?? '' },
            })
          },
        })
      }
    })
    // The current choice first.
    const currentId = `${currentProject}/${currentEnv}`
    rows.sort((a, b) => Number(b.id === currentId) - Number(a.id === currentId))
    const result = filterItems(rows, query)
    if (accessFor(me.data?.org_role, 'project:create') !== 'hidden') {
      result.push({
        id: 'create-project',
        group: t('switcher.actions'),
        label: t('projects.create'),
        run: () => void navigate({ to: '/o/$org/projects', params: { org } }),
      })
    }
    return result
  }

  const loading = open && (projects.isPending || environments.some((q) => q.isPending))
  return (
    <>
      <button
        type="button"
        aria-haspopup="dialog"
        onClick={() => setOpen(true)}
        className="inline-flex min-h-8 max-w-full items-center gap-1 truncate rounded-sm border border-border-strong px-3 py-1 text-sm hover:bg-surface coarse:min-h-11"
      >
        <span className="sr-only">{t('switcher.label')}: </span>
        <span className="truncate">{label}</span>
        <span aria-hidden="true">▾</span>
      </button>
      {open && (
        <SearchDialog
          label={t('switcher.label')}
          placeholder={t('switcher.placeholder')}
          items={items}
          empty={(q) => (loading ? t('common.loading') : t('switcher.empty', { text: q }))}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  )
}
