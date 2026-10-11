// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Button, Card, StatusBadge, TextField } from '@/components/ui'
import { t } from '@/i18n'
import { AddNodeDialog } from '@/routes/Nodes'
import { useNodes, useProjects } from '@/scope'

// The name the setup gives to a new organization. A different name means step 1 is done.
const DEFAULT_ORG_NAME = 'Polipan'
const MAX_PROJECTS = 5
const MAX_SERVICES = 10

type StepId = 'org' | 'server' | 'domain' | 'service'

function readSkipped(userId: string): StepId[] {
  try {
    const value = JSON.parse(
      localStorage.getItem(`polipan.onboarding.${userId}`) ?? '[]',
    ) as unknown
    return Array.isArray(value) ? (value as StepId[]) : []
  } catch {
    return []
  }
}

function writeSkipped(userId: string, ids: StepId[]) {
  try {
    localStorage.setItem(`polipan.onboarding.${userId}`, JSON.stringify(ids))
  } catch {
    // Storage may be off. The marks are then lost on reload, and nothing else breaks.
  }
}

// UI-31 (local steps): the first-run checklist. Progress is computed from the data, so it is
// never stale. Only "skipped" is kept on the device (screens, known gaps). The live "waiting
// for the server" row of step 2 comes with multi-server support.
export function Onboarding() {
  const { org } = useParams({ from: '/o/$org' })
  const me = useMe()
  const queryClient = useQueryClient()
  const role = me.data?.org_role
  const userId = me.data?.user?.id ?? 'anon'
  const [skipped, setSkipped] = useState<StepId[]>(() => readSkipped(userId))
  const [showAll, setShowAll] = useState(false)
  const [name, setName] = useState('')
  const [addingServer, setAddingServer] = useState(false)

  const orgQuery = useQuery({ queryKey: ['org'], queryFn: () => unwrap(api.GET('/org')) })
  const nodes = useNodes()
  const projects = useProjects()
  const shownProjects = (projects.data ?? []).slice(0, MAX_PROJECTS)
  const envQueries = useQueries({
    queries: shownProjects.map((p) => ({
      queryKey: ['environments', p.id],
      queryFn: () =>
        unwrap(
          api.GET('/projects/{project_id}/environments', {
            params: { path: { project_id: p.id ?? '' } },
          }),
        ),
    })),
  })
  const environments = envQueries.flatMap((q) => q.data ?? [])
  const serviceQueries = useQueries({
    queries: environments.map((e) => ({
      queryKey: ['services', e.id],
      queryFn: () =>
        unwrap(
          api.GET('/environments/{environment_id}/services', {
            params: { path: { environment_id: e.id ?? '' } },
          }),
        ),
    })),
  })
  const services = serviceQueries.flatMap((q) => q.data ?? [])
  const domainQueries = useQueries({
    queries: services.slice(0, MAX_SERVICES).map((s) => ({
      queryKey: ['domains', s.id],
      queryFn: () =>
        unwrap(
          api.GET('/services/{service_id}/domains', {
            params: { path: { service_id: s.id ?? '' } },
          }),
        ),
    })),
  })

  const rename = useMutation({
    mutationFn: () => unwrap(api.PATCH('/org', { body: { name: name.trim() } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['org'] }),
  })

  if (role !== 'owner' && role !== 'admin' && role !== 'developer') return null

  const all = [orgQuery, nodes, projects, ...envQueries, ...serviceQueries, ...domainQueries]
  const failed = all.find((q) => q.isError)
  if (failed) {
    return (
      <ErrorBanner
        error={failed.error}
        title={t('onboarding.load_failed')}
        onRetry={() => {
          for (const q of all) if (q.isError) void q.refetch()
        }}
      />
    )
  }
  // Nothing is shown while the checklist loads. A skeleton would move the page when a finished
  // setup removes the checklist again (CLS, quality section 3).
  if (all.some((q) => q.isPending && q.fetchStatus !== 'idle')) return null

  // The first service decides where the links of step 3 and 4 go.
  const firstEnv = environments[0]
  const firstProject = shownProjects.find((p) => p.id === firstEnv?.project_id) ?? shownProjects[0]
  const firstService = services[0]
  const serviceEnv = environments.find((e) => e.id === firstService?.environment_id)
  const serviceProject = shownProjects.find((p) => p.id === serviceEnv?.project_id)

  const isAdmin = role === 'owner' || role === 'admin'
  const done: Record<StepId, boolean> = {
    org: Boolean(orgQuery.data?.name) && orgQuery.data?.name !== DEFAULT_ORG_NAME,
    server: (nodes.data?.length ?? 0) > 1,
    domain: domainQueries.some((q) => (q.data?.length ?? 0) > 0),
    service: services.length > 0,
  }
  const ids: StepId[] = isAdmin ? ['org', 'server', 'domain', 'service'] : ['domain', 'service']
  const open = ids.filter((id) => !done[id] && !skipped.includes(id))
  const skippedNow = ids.filter((id) => !done[id] && skipped.includes(id))

  const mark = (id: StepId, on: boolean) => {
    const next = on ? [...skipped, id] : skipped.filter((x) => x !== id)
    setSkipped(next)
    writeSkipped(userId, next)
  }

  if (open.length === 0 && !showAll) {
    return skippedNow.length > 0 ? (
      <div>
        <Button onClick={() => setShowAll(true)}>{t('onboarding.resume')}</Button>
      </div>
    ) : null
  }

  function submitName(event: FormEvent) {
    event.preventDefault()
    if (name.trim() !== '') rename.mutate()
  }

  const action = (id: StepId) => {
    switch (id) {
      case 'org':
        return (
          <form onSubmit={submitName} className="flex flex-wrap items-end gap-2">
            <TextField
              label={t('onboarding.org_name')}
              autoComplete="off"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            <Button type="submit" variant="primary" loading={rename.isPending}>
              {t('common.save')}
            </Button>
          </form>
        )
      case 'server':
        // The dialog shows the install command, the time left and the live row pending, then ready (UI-31).
        return (
          <div className="flex flex-wrap items-center gap-3">
            <Button onClick={() => setAddingServer(true)}>{t('onboarding.add_server')}</Button>
            <Link to="/o/$org/nodes" params={{ org }} className="text-primary underline">
              {t('onboarding.all_servers')}
            </Link>
          </div>
        )
      case 'domain':
        return firstService && serviceEnv && serviceProject ? (
          <Link
            to="/o/$org/p/$project/e/$env/s/$service/$tab"
            params={{
              org,
              project: serviceProject.slug ?? '',
              env: serviceEnv.slug ?? '',
              service: firstService.name ?? '',
              tab: 'domains',
            }}
            className="text-primary underline"
          >
            {t('onboarding.add_domain')}
          </Link>
        ) : (
          <p className="text-sm text-muted">{t('onboarding.domain_needs_service')}</p>
        )
      case 'service':
        return firstProject && firstEnv ? (
          <Link
            to="/o/$org/p/$project/e/$env/new"
            params={{ org, project: firstProject.slug ?? '', env: firstEnv.slug ?? '' }}
            className="text-primary underline"
          >
            {t('onboarding.add_service')}
          </Link>
        ) : (
          <Link to="/o/$org/projects" params={{ org }} className="text-primary underline">
            {t('onboarding.create_project')}
          </Link>
        )
    }
  }

  return (
    <Card className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-lg font-semibold">{t('onboarding.title')}</h2>
        {showAll && <Button onClick={() => setShowAll(false)}>{t('common.close')}</Button>}
      </div>
      <p className="text-sm text-muted">{t('onboarding.text')}</p>
      {rename.error && <ErrorBanner error={rename.error} title={t('onboarding.rename_failed')} />}
      <ol className="space-y-4">
        {ids.map((id) => {
          const isDone = done[id]
          const isSkipped = !isDone && skipped.includes(id)
          return (
            <li key={id} className="space-y-2 border-t border-border pt-3">
              <div className="flex flex-wrap items-center gap-2">
                <h3 className="font-semibold">{t(`onboarding.step.${id}`)}</h3>
                <StatusBadge
                  tone={isDone ? 'success' : isSkipped ? 'muted' : 'warning'}
                  label={
                    isDone
                      ? t('onboarding.done')
                      : isSkipped
                        ? t('onboarding.skipped')
                        : t('onboarding.todo')
                  }
                />
              </div>
              <p className="text-sm text-muted">{t(`onboarding.step.${id}_text`)}</p>
              {!isDone && (
                <div className="flex flex-wrap items-end gap-3">
                  {action(id)}
                  {isSkipped ? (
                    <Button onClick={() => mark(id, false)}>{t('onboarding.resume_step')}</Button>
                  ) : (
                    <Button onClick={() => mark(id, true)}>{t('onboarding.skip')}</Button>
                  )}
                </div>
              )}
            </li>
          )
        })}
      </ol>
      {addingServer && <AddNodeDialog onClose={() => setAddingServer(false)} />}
    </Card>
  )
}
