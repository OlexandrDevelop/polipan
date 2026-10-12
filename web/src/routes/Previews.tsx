// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal, TypedConfirmDialog } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import {
  Banner,
  Button,
  Card,
  CheckboxField,
  EmptyState,
  SelectField,
  Skeleton,
  StatusBadge,
  TextAreaField,
  TextField,
  SectionTabs,
} from '@/components/ui'
import { t, tState } from '@/i18n'
import { useAccess } from '@/permissions'
import { useEnvironments, useProjects } from '@/scope'
import type { Environment, Project } from '@/scope'
import type { Tone } from '@/status'

type PreviewSettings = components['schemas']['PreviewSettings']

const stateTone: Record<string, Tone> = {
  creating: 'info',
  active: 'success',
  failed: 'danger',
  closing: 'warning',
  deleted: 'muted',
}

export function usePreviews(projectId: string | undefined) {
  return useQuery({
    queryKey: ['previews', projectId],
    enabled: Boolean(projectId),
    // A preview that is being created or closed changes by itself.
    refetchInterval: 15000,
    queryFn: () =>
      unwrap(
        api.GET('/projects/{project_id}/previews', {
          params: { path: { project_id: projectId! } },
        }),
      ),
  })
}

export function isExpired(env: Environment, now = Date.now()): boolean {
  const at = env.preview?.expires_at
  return Boolean(at) && new Date(at!).getTime() < now
}

// The state of a preview in words, and an extra badge when the time is over.
export function PreviewState({ env }: { env: Environment }) {
  const state = env.preview?.state
  return (
    <span className="flex flex-wrap gap-2">
      <StatusBadge
        tone={stateTone[state ?? ''] ?? 'muted'}
        label={tState('status.preview', state)}
      />
      {isExpired(env) && state !== 'deleted' && (
        <StatusBadge tone="warning" label={t('previews.expired')} />
      )}
    </span>
  )
}

export function PreviewTitle({ env }: { env: Environment }) {
  return (
    <span className="break-all">
      {env.preview?.repo ?? env.name}
      {env.preview?.pr_number ? ` #${env.preview.pr_number}` : ''}
    </span>
  )
}

export function useDeletePreview(projectId: string | undefined, onDone?: () => void) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (environmentId: string) =>
      unwrap(
        api.DELETE('/environments/{environment_id}', {
          params: { path: { environment_id: environmentId } },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['previews', projectId] })
      await queryClient.invalidateQueries({ queryKey: ['environments', projectId] })
      onDone?.()
    },
  })
}

function CreatePreviewDialog({
  project,
  environments,
  onClose,
}: {
  project: Project
  environments: Environment[]
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const bases = environments.filter((e) => e.kind !== 'preview')
  const [repo, setRepo] = useState('')
  const [pr, setPr] = useState('')
  const [base, setBase] = useState(project.settings?.previews?.base_environment_id ?? '')
  const prNumber = Number(pr)
  const prValid = Number.isInteger(prNumber) && prNumber > 0
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/projects/{project_id}/previews', {
          params: { path: { project_id: project.id ?? '' } },
          body: {
            repo: repo.trim(),
            pr_number: prNumber,
            ...(base ? { base_environment_id: base } : {}),
          },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['previews', project.id] })
      onClose()
    },
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    if (repo.trim() && prValid) create.mutate()
  }
  return (
    <Modal title={t('previews.create_title')} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        {create.error && <ErrorBanner error={create.error} title={t('previews.create_failed')} />}
        <TextField
          label={t('previews.repo')}
          hint={t('previews.repo_hint')}
          required
          autoFocus
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          value={repo}
          onChange={(e) => setRepo(e.target.value)}
        />
        <TextField
          label={t('previews.pr_number')}
          type="number"
          min={1}
          required
          value={pr}
          error={pr && !prValid ? t('previews.pr_invalid') : null}
          onChange={(e) => setPr(e.target.value)}
        />
        <SelectField
          label={t('previews.base')}
          value={base}
          onChange={(e) => setBase(e.target.value)}
        >
          <option value="">{t('previews.base_default')}</option>
          {bases.map((e) => (
            <option key={e.id} value={e.id}>
              {e.name}
            </option>
          ))}
        </SelectField>
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button
            type="submit"
            variant="primary"
            loading={create.isPending}
            reason={repo.trim() && prValid ? undefined : t('previews.fill_first')}
          >
            {t('previews.create')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

function selectorText(selector: PreviewSettings['node_selector']): string {
  return Object.entries(selector ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')
}

function parseSelector(text: string): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const at = line.indexOf('=')
    if (at > 0) out[line.slice(0, at).trim()] = line.slice(at + 1).trim()
  }
  return Object.keys(out).length > 0 ? out : undefined
}

function selectorValid(text: string): boolean {
  return text
    .split('\n')
    .filter((l) => l.trim())
    .every((l) => l.indexOf('=') > 0)
}

// The settings of the previews of one project (UI-18).
function PreviewSettingsForm({
  project,
  environments,
}: {
  project: Project
  environments: Environment[]
}) {
  const queryClient = useQueryClient()
  const allowed = useAccess('preview:manage', false, project.id) === 'allow'
  const isAdmin = useAccess('org:update', false, project.id) === 'allow'
  const current = project.settings?.previews
  const [enabled, setEnabled] = useState(current?.enabled ?? false)
  const [base, setBase] = useState(current?.base_environment_id ?? '')
  const [maxActive, setMaxActive] = useState(String(current?.max_active ?? 10))
  const [ttl, setTtl] = useState(String(current?.ttl_days ?? 7))
  const [grace, setGrace] = useState(String(current?.cleanup_grace_minutes ?? 60))
  const [forks, setForks] = useState(current?.from_forks ?? false)
  const [selector, setSelector] = useState(selectorText(current?.node_selector))
  const [seed, setSeed] = useState((current?.seed_command ?? []).join(' '))
  const [strategy, setStrategy] = useState<string>(current?.strategy ?? '')
  const [saved, setSaved] = useState(false)

  const maxOk = Number.isInteger(Number(maxActive)) && Number(maxActive) >= 1
  const ttlOk = Number.isInteger(Number(ttl)) && Number(ttl) >= 1
  const graceOk = Number.isInteger(Number(grace)) && Number(grace) >= 0
  const selectorOk = selectorValid(selector)
  const bases = environments.filter((e) => e.kind !== 'preview')

  const save = useMutation({
    mutationFn: () => {
      const previews: PreviewSettings = {
        enabled,
        max_active: Number(maxActive),
        ttl_days: Number(ttl),
        cleanup_grace_minutes: Number(grace),
        from_forks: forks,
        ...(base ? { base_environment_id: base } : {}),
        ...(parseSelector(selector) ? { node_selector: parseSelector(selector) } : {}),
        ...(seed.trim() ? { seed_command: seed.trim().split(/\s+/) } : {}),
        ...(strategy ? { strategy: strategy as PreviewSettings['strategy'] } : {}),
      }
      return unwrap(
        api.PATCH('/projects/{project_id}', {
          params: { path: { project_id: project.id ?? '' } },
          body: {
            settings: {
              retain_releases: project.settings?.retain_releases ?? 10,
              build_timeout_minutes: project.settings?.build_timeout_minutes ?? 30,
              previews,
            },
          },
        }),
      )
    },
    onSuccess: async () => {
      setSaved(true)
      await queryClient.invalidateQueries({ queryKey: ['projects'] })
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    setSaved(false)
    if (maxOk && ttlOk && graceOk && selectorOk) save.mutate()
  }

  return (
    <Card>
      <form onSubmit={submit} className="space-y-4" noValidate>
        <h2 className="font-semibold">{t('previews.settings')}</h2>
        <CheckboxField
          label={t('previews.enabled')}
          hint={t('previews.enabled_hint')}
          checked={enabled}
          disabled={!allowed}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        <div className="grid gap-3 sm:grid-cols-2">
          <SelectField
            label={t('previews.base')}
            hint={t('previews.base_hint')}
            value={base}
            disabled={!allowed}
            onChange={(e) => setBase(e.target.value)}
          >
            <option value="">{t('previews.base_none')}</option>
            {bases.map((e) => (
              <option key={e.id} value={e.id}>
                {e.name}
              </option>
            ))}
          </SelectField>
          <SelectField
            label={t('promote.strategy')}
            value={strategy}
            disabled={!allowed}
            onChange={(e) => setStrategy(e.target.value)}
          >
            <option value="">{t('promote.strategy_default')}</option>
            <option value="recreate">{tState('deploy.strategy_value', 'recreate')}</option>
            <option value="blue_green">{tState('deploy.strategy_value', 'blue_green')}</option>
          </SelectField>
          <TextField
            label={t('previews.max_active')}
            type="number"
            min={1}
            value={maxActive}
            disabled={!allowed}
            error={maxOk ? null : t('previews.number_invalid')}
            onChange={(e) => setMaxActive(e.target.value)}
          />
          <TextField
            label={t('previews.ttl_days')}
            hint={t('previews.ttl_hint')}
            type="number"
            min={1}
            value={ttl}
            disabled={!allowed}
            error={ttlOk ? null : t('previews.number_invalid')}
            onChange={(e) => setTtl(e.target.value)}
          />
          <TextField
            label={t('previews.grace_minutes')}
            hint={t('previews.grace_hint')}
            type="number"
            min={0}
            value={grace}
            disabled={!allowed}
            error={graceOk ? null : t('previews.number_invalid')}
            onChange={(e) => setGrace(e.target.value)}
          />
        </div>
        <CheckboxField
          label={t('previews.from_forks')}
          hint={isAdmin ? t('previews.from_forks_hint') : t('previews.from_forks_admin')}
          checked={forks}
          disabled={!allowed || !isAdmin}
          onChange={(e) => setForks(e.target.checked)}
        />
        <TextAreaField
          label={t('previews.node_selector')}
          hint={t('previews.node_selector_hint')}
          value={selector}
          disabled={!allowed}
          error={selectorOk ? null : t('previews.selector_invalid')}
          autoCapitalize="none"
          spellCheck={false}
          onChange={(e) => setSelector(e.target.value)}
        />
        <TextField
          label={t('previews.seed_command')}
          hint={t('previews.seed_hint')}
          value={seed}
          disabled={!allowed}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          onChange={(e) => setSeed(e.target.value)}
        />
        {saved && <Banner>{t('previews.saved')}</Banner>}
        {save.error && <ErrorBanner error={save.error} title={t('previews.save_failed')} />}
        {allowed ? (
          <Button type="submit" variant="primary" loading={save.isPending}>
            {t('common.save')}
          </Button>
        ) : (
          <p className="text-sm text-muted">{t('previews.read_only')}</p>
        )}
      </form>
    </Card>
  )
}

function PreviewList({ project, environments }: { project: Project; environments: Environment[] }) {
  const { org } = useParams({ from: '/o/$org/previews' })
  const previews = usePreviews(project.id)
  const allowed = useAccess('preview:manage', false, project.id) === 'allow'
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Environment | null>(null)
  const remove = useDeletePreview(project.id, () => setDeleting(null))

  const createButton = allowed ? (
    <Button variant="primary" onClick={() => setCreating(true)}>
      {t('previews.create')}
    </Button>
  ) : undefined

  let body
  if (previews.isPending) body = <Skeleton />
  else if (previews.isError)
    body = <ErrorBanner error={previews.error} onRetry={() => void previews.refetch()} />
  else if (previews.data.length === 0)
    body = (
      <EmptyState
        title={t('previews.empty_title')}
        text={t('previews.empty_text')}
        action={createButton}
      />
    )
  else
    body = (
      <DataList
        caption={t('previews.caption')}
        rows={previews.data}
        rowKey={(e) => e.id ?? ''}
        columns={[
          {
            header: t('previews.col_pr'),
            primary: true,
            cell: (e) => (
              <Link
                to="/o/$org/p/$project/previews/$env"
                params={{ org, project: project.slug ?? '', env: e.slug ?? '' }}
                className="text-primary underline"
              >
                <PreviewTitle env={e} />
              </Link>
            ),
          },
          { header: t('previews.col_state'), cell: (e) => <PreviewState env={e} /> },
          {
            header: t('previews.col_url'),
            cell: (e) =>
              e.preview?.url ? (
                <a
                  href={e.preview.url}
                  target="_blank"
                  rel="noreferrer noopener"
                  className="break-all text-primary underline"
                >
                  {e.preview.url}
                </a>
              ) : (
                '-'
              ),
          },
          {
            header: t('previews.col_expires'),
            cell: (e) => <Time value={e.preview?.expires_at} />,
          },
        ]}
        actionsHeader={t('common.actions')}
        actions={(e) =>
          allowed && (
            <Button variant="destructive" onClick={() => setDeleting(e)}>
              {t('common.delete')}
              <span className="sr-only"> {e.slug}</span>
            </Button>
          )
        }
      />
    )

  return (
    <div className="space-y-4">
      {createButton && previews.data && previews.data.length > 0 && (
        <div className="flex justify-end">{createButton}</div>
      )}
      {body}
      {creating && (
        <CreatePreviewDialog
          project={project}
          environments={environments}
          onClose={() => setCreating(false)}
        />
      )}
      {deleting && (
        <TypedConfirmDialog
          title={t('previews.delete_title', { name: deleting.name ?? '' })}
          text={t('previews.delete_text')}
          confirmLabel={t('previews.delete_confirm')}
          typed={deleting.name ?? ''}
          busy={remove.isPending}
          error={remove.error ? <ErrorBanner error={remove.error} /> : undefined}
          onConfirm={() => remove.mutate(deleting.id ?? '')}
          onClose={() => {
            remove.reset()
            setDeleting(null)
          }}
        />
      )}
    </div>
  )
}

// UI-18: the previews of a project and their settings. The project is a filter in the URL.
export function PreviewsPage() {
  const search = useSearch({ from: '/o/$org/previews' })
  const { org } = useParams({ from: '/o/$org/previews' })
  const navigate = useNavigate()
  const projects = useProjects()
  const list = projects.data ?? []
  const project = list.find((p) => p.slug === search.project) ?? list[0]
  const environments = useEnvironments(project?.id)

  return (
    <section>
      <PageHeader title={t('previews.title')} />
      {projects.isPending && <Skeleton />}
      {projects.isError && (
        <ErrorBanner error={projects.error} onRetry={() => void projects.refetch()} />
      )}
      {projects.data && list.length === 0 && (
        <EmptyState title={t('previews.no_project_title')} text={t('previews.no_project_text')} />
      )}
      {project && (
        <div className="space-y-6">
          <div className="max-w-sm">
            <SelectField
              label={t('previews.project')}
              value={project.slug ?? ''}
              onChange={(e) =>
                void navigate({
                  to: '/o/$org/previews',
                  params: { org },
                  search: { project: e.target.value },
                })
              }
            >
              {list.map((p) => (
                <option key={p.id} value={p.slug}>
                  {p.name}
                </option>
              ))}
            </SelectField>
          </div>
          <SectionTabs
            label={t('settings.sections')}
            sections={[
              {
                id: 'list',
                label: t('previews.title'),
                content: <PreviewList project={project} environments={environments.data ?? []} />,
              },
              {
                id: 'settings',
                label: t('previews.settings'),
                content: environments.data ? (
                  <PreviewSettingsForm
                    key={project.id}
                    project={project}
                    environments={environments.data}
                  />
                ) : (
                  <Skeleton />
                ),
              },
            ]}
          />
        </div>
      )}
    </section>
  )
}
