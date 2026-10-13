// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api, getText, postYaml } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { LargerScreenNotice } from '@/components/LargerScreen'
import { Modal } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import {
  Banner,
  Button,
  Card,
  CheckboxField,
  Hint,
  SectionTabs,
  SelectField,
  Skeleton,
  StatusBadge,
  TextAreaField,
  TextField,
} from '@/components/ui'
import { t, tState } from '@/i18n'
import { useAccess } from '@/permissions'
import { NotFoundPage } from '@/routes/ErrorPages'
import { useScope } from '@/scope'
import type { Tone } from '@/status'
import { newKey } from '../idempotency'

type ConfigPlan = components['schemas']['ConfigPlan']
type SyncState = NonNullable<components['schemas']['Project']['config_sync_state']>
type PlanError = NonNullable<ConfigPlan['errors']>[number]

const syncTone: Record<SyncState, Tone> = {
  in_sync: 'success',
  drifted: 'warning',
  plan_pending: 'info',
  apply_failed: 'danger',
}

const actionMark: Record<string, string> = { create: '+', update: '~', delete: '-', unmanaged: '?' }

function show(value: unknown): string {
  if (value === undefined || value === null) return '-'
  return typeof value === 'string' ? value : JSON.stringify(value)
}

// Errors of a bad file come in the plan answer, or as details of a 422.
function planErrors(error: unknown): PlanError[] {
  if (error instanceof ApiError && Array.isArray(error.details?.errors)) {
    return error.details.errors as PlanError[]
  }
  return []
}

function ErrorList({ errors }: { errors: PlanError[] }) {
  return (
    <Banner variant="danger" title={t('config.file_errors')}>
      <ul className="mt-1 space-y-1">
        {errors.map((e, i) => (
          <li key={i} className="break-words font-mono text-sm">
            {e.line ? `${t('config.line', { line: e.line })} ` : ''}
            {e.path ? `${e.path}: ` : ''}
            {e.message}
          </li>
        ))}
      </ul>
    </Banner>
  )
}

function SourceCard({
  projectId,
  source,
  canEdit,
}: {
  projectId: string
  source: NonNullable<components['schemas']['Project']['config_source']> | undefined
  canEdit: boolean
}) {
  const queryClient = useQueryClient()
  const connections = useQuery({
    queryKey: ['git-connections'],
    queryFn: () => unwrap(api.GET('/git-connections')),
  })
  const [repo, setRepo] = useState(source?.repo ?? '')
  const [branch, setBranch] = useState(source?.branch ?? '')
  const [path, setPath] = useState(source?.path ?? 'polipan.yaml')
  const [connectionId, setConnectionId] = useState(source?.connection_id ?? '')
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/projects/{project_id}', {
          params: { path: { project_id: projectId } },
          body: {
            config_source: {
              repo: repo.trim(),
              branch: branch.trim(),
              path: path.trim(),
              connection_id: connectionId,
            },
          },
        }),
      ),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['project', projectId] }),
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    save.mutate()
  }
  return (
    <Card>
      <form onSubmit={submit} className="space-y-4" aria-labelledby="config-source-title">
        <h2 id="config-source-title" className="text-lg font-semibold">
          {t('config.source_title')}
        </h2>
        {!canEdit && <p className="text-sm text-muted">{t('config.source_admin_only')}</p>}
        {save.error && (
          <ErrorBanner
            error={save.error}
            title={
              save.error instanceof ApiError && save.error.status === 403
                ? t('config.source_forbidden')
                : t('config.source_failed')
            }
          />
        )}
        {save.isSuccess && <Banner>{t('config.source_saved')}</Banner>}
        <SelectField
          label={t('config.connection')}
          value={connectionId}
          disabled={!canEdit}
          onChange={(e) => setConnectionId(e.target.value)}
        >
          <option value="">{t('config.connection_none')}</option>
          {(connections.data ?? []).map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </SelectField>
        <div className="grid gap-4 sm:grid-cols-3">
          <TextField
            label={t('config.repo')}
            value={repo}
            readOnly={!canEdit}
            autoComplete="off"
            onChange={(e) => setRepo(e.target.value)}
          />
          <TextField
            label={t('config.branch')}
            value={branch}
            readOnly={!canEdit}
            autoComplete="off"
            onChange={(e) => setBranch(e.target.value)}
          />
          <TextField
            label={t('config.path')}
            value={path}
            readOnly={!canEdit}
            autoComplete="off"
            onChange={(e) => setPath(e.target.value)}
          />
        </div>
        {canEdit && (
          <div className="flex justify-end">
            <Button type="submit" variant="primary" loading={save.isPending}>
              {t('common.save')}
            </Button>
          </div>
        )}
      </form>
    </Card>
  )
}

function PlanView({ plan }: { plan: ConfigPlan }) {
  const changes = plan.changes ?? []
  if (plan.valid === false) return <ErrorList errors={plan.errors ?? []} />
  if (changes.length === 0) return <Banner>{t('config.plan_empty')}</Banner>
  return (
    <div className="space-y-3">
      <p className="text-sm text-muted">{t('config.plan_count', { count: changes.length })}</p>
      <ul className="space-y-3">
        {changes.map((c, i) => (
          <li key={`${c.resource}-${c.name}-${i}`} className="rounded-sm border border-border p-3">
            <p className="break-all font-mono text-sm font-semibold">
              <span aria-hidden="true">{actionMark[c.action ?? ''] ?? ''} </span>
              {tState('config.action', c.action)}: {c.resource} {c.name}
            </p>
            {c.action === 'unmanaged' && (
              <p className="text-sm text-muted">{t('config.unmanaged_note')}</p>
            )}
            {(c.fields ?? []).length > 0 && (
              <div className="mt-2 overflow-x-auto">
                <table className="w-full border-collapse text-left text-sm">
                  <caption className="sr-only">{t('config.fields_caption')}</caption>
                  <thead>
                    <tr className="bg-surface">
                      <th scope="col" className="p-2 font-semibold text-muted">
                        {t('diff.field')}
                      </th>
                      <th scope="col" className="p-2 font-semibold text-muted">
                        {t('config.before')}
                      </th>
                      <th scope="col" className="p-2 font-semibold text-muted">
                        {t('config.after')}
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {(c.fields ?? []).map((f, j) => (
                      <tr key={`${f.path}-${j}`} className="border-b border-border align-top">
                        <th scope="row" className="break-all p-2 font-mono font-normal">
                          {f.path}
                        </th>
                        <td className="break-all p-2 font-mono">
                          <span aria-hidden="true">- </span>
                          {show(f.before)}
                        </td>
                        <td className="break-all p-2 font-mono">
                          <span aria-hidden="true">+ </span>
                          {show(f.after)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </li>
        ))}
      </ul>
    </div>
  )
}

function ApplyDialog({
  projectId,
  config,
  plan,
  canPrune,
  onClose,
  onApplied,
}: {
  projectId: string
  config: string
  plan: ConfigPlan
  canPrune: boolean
  onClose: () => void
  onApplied: (deploymentId: string | undefined) => void
}) {
  const queryClient = useQueryClient()
  const hasUnmanaged = (plan.changes ?? []).some((c) => c.action === 'unmanaged')
  const [prune, setPrune] = useState(false)
  const [pruneData, setPruneData] = useState(false)
  const apply = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/projects/{project_id}/config/apply', {
          params: {
            path: { project_id: projectId },
            header: { 'Idempotency-Key': newKey() },
          },
          body: {
            config,
            plan_hash: plan.plan_hash ?? '',
            ...(prune ? { prune: true, ...(pruneData ? { prune_data: true } : {}) } : {}),
          },
        }),
      ),
    onSuccess: async (result) => {
      await queryClient.invalidateQueries({ queryKey: ['project', projectId] })
      onApplied(result.deployments?.[0]?.id)
    },
  })
  const stale = apply.error instanceof ApiError && apply.error.code === 'plan_changed'
  return (
    <Modal title={t('config.apply_title')} onClose={onClose}>
      <div className="space-y-4">
        <p>{t('config.apply_text', { count: (plan.changes ?? []).length })}</p>
        <p className="text-sm">
          {t('config.plan_hash')} <code className="break-all font-mono">{plan.plan_hash}</code>
        </p>
        {hasUnmanaged && canPrune && (
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">{t('config.prune_title')}</legend>
            <CheckboxField
              label={t('config.prune')}
              hint={t('config.prune_hint')}
              checked={prune}
              onChange={(e) => {
                setPrune(e.target.checked)
                if (!e.target.checked) setPruneData(false)
              }}
            />
            {prune && (
              <CheckboxField
                label={t('config.prune_data')}
                hint={t('config.prune_data_hint')}
                checked={pruneData}
                onChange={(e) => setPruneData(e.target.checked)}
              />
            )}
          </fieldset>
        )}
        {apply.error && (
          <ErrorBanner
            error={apply.error}
            title={
              stale
                ? t('config.plan_changed')
                : apply.error instanceof ApiError && apply.error.code === 'managed_by_git'
                  ? t('variables.managed_by_git')
                  : t('config.apply_failed')
            }
          />
        )}
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button
            variant="primary"
            loading={apply.isPending}
            onClick={() => apply.mutate()}
            disabled={stale}
          >
            {t('config.apply')}
          </Button>
        </div>
      </div>
    </Modal>
  )
}

function download(text: string, name: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'application/yaml' }))
  const link = document.createElement('a')
  link.href = url
  link.download = name
  link.click()
  URL.revokeObjectURL(url)
}

// UI-29: link a repo file, see the plan and the drift. Phone is read-only (responsive, section 4).
export function ConfigAsCodePage() {
  const { org, project: projectSlug } = useParams({ from: '/o/$org/p/$project/config' })
  const navigate = useNavigate()
  const scope = useScope(projectSlug)
  const projectId = scope.project?.id ?? ''
  const canApply = useAccess('config:apply', false, projectId) === 'allow'
  const isAdmin = useAccess('config:source', false, projectId) === 'allow'
  const project = useQuery({
    queryKey: ['project', projectId],
    enabled: Boolean(projectId),
    queryFn: () =>
      unwrap(api.GET('/projects/{project_id}', { params: { path: { project_id: projectId } } })),
  })
  const [text, setText] = useState('')
  const [plan, setPlan] = useState<ConfigPlan | null>(null)
  const [applying, setApplying] = useState(false)
  const [applied, setApplied] = useState(false)

  const load = useMutation({
    mutationFn: () => getText(`/projects/${projectId}/config`),
    onSuccess: (yaml) => {
      setText(yaml)
      setPlan(null)
    },
  })
  const exportFile = useMutation({
    mutationFn: () => getText(`/projects/${projectId}/config`),
    onSuccess: (yaml) => download(yaml, 'polipan.yaml'),
  })
  const run = useMutation({
    mutationFn: () => postYaml<ConfigPlan>(`/projects/${projectId}/config/plan`, text),
    onSuccess: (result) => {
      setPlan(result)
      setApplied(false)
    },
    onError: () => setPlan(null),
  })

  if (scope.state === 'pending') return <Skeleton />
  if (scope.state === 'error') return <ErrorBanner error={scope.error} onRetry={scope.retry} />
  if (scope.state === 'notfound' || !scope.project) return <NotFoundPage />

  const state = project.data?.config_sync_state
  const readyToApply = canApply && plan?.valid !== false && Boolean(plan?.plan_hash) && !applied
  const errors = plan?.valid === false ? (plan.errors ?? []) : planErrors(run.error)

  return (
    <section className="space-y-4">
      <PageHeader
        title={t('config.title')}
        context={[scope.project.name ?? '']}
        badges={
          state && <StatusBadge tone={syncTone[state]} label={tState('config.state', state)} />
        }
        breadcrumb={
          <Link
            to="/o/$org/p/$project"
            params={{ org, project: projectSlug }}
            className="text-primary underline"
          >
            {scope.project.name}
          </Link>
        }
        actions={
          <Button onClick={() => exportFile.mutate()} loading={exportFile.isPending}>
            {t('config.export')}
          </Button>
        }
      />
      <Hint>{t('config.intro')}</Hint>
      <LargerScreenNotice />
      {project.isPending && <Skeleton />}
      {project.isError && (
        <ErrorBanner error={project.error} onRetry={() => void project.refetch()} />
      )}
      {exportFile.error && (
        <ErrorBanner error={exportFile.error} title={t('config.export_failed')} />
      )}
      {state === 'plan_pending' && (
        <Banner variant="warning" title={t('config.pending_title')}>
          {isAdmin ? t('config.pending_admin') : t('config.pending_other')}
        </Banner>
      )}
      {state === 'drifted' && (
        <Banner variant="warning" title={t('config.drifted_title')}>
          {t('config.drifted_text')}
        </Banner>
      )}
      {state === 'apply_failed' && (
        <Banner variant="danger" title={t('config.failed_title')}>
          {t('config.failed_text')}
        </Banner>
      )}
      <SectionTabs
        label={t('settings.sections')}
        sections={[
          {
            id: 'source',
            label: t('config.source_title'),
            content: (
              <>
                {project.data && (
                  <SourceCard
                    key={project.dataUpdatedAt}
                    projectId={projectId}
                    source={project.data.config_source}
                    canEdit={isAdmin}
                  />
                )}
              </>
            ),
          },
          {
            id: 'plan',
            label: t('config.plan_title'),
            content: (
              <div className="space-y-4">
                <Card className="space-y-4">
                  <h2 className="text-lg font-semibold">{t('config.plan_title')}</h2>
                  <TextAreaField
                    label={t('config.file')}
                    hint={t('config.file_hint')}
                    rows={14}
                    spellCheck={false}
                    value={text}
                    onChange={(e) => {
                      setText(e.target.value)
                      setPlan(null)
                    }}
                  />
                  {load.error && (
                    <ErrorBanner error={load.error} title={t('config.export_failed')} />
                  )}
                  <div className="flex flex-wrap gap-2">
                    <Button onClick={() => load.mutate()} loading={load.isPending}>
                      {t('config.load')}
                    </Button>
                    <Button
                      variant="primary"
                      loading={run.isPending}
                      disabled={text.trim() === ''}
                      onClick={() => run.mutate()}
                    >
                      {t('config.plan_now')}
                    </Button>
                    {readyToApply && (
                      <Button variant="primary" onClick={() => setApplying(true)}>
                        {t('config.apply')}
                      </Button>
                    )}
                  </div>
                  {!canApply && <p className="text-sm text-muted">{t('config.apply_denied')}</p>}
                </Card>
                {run.error && errors.length === 0 && (
                  <ErrorBanner error={run.error} title={t('config.plan_failed')} />
                )}
                {errors.length > 0 && <ErrorList errors={errors} />}
                {applied && <Banner>{t('config.applied')}</Banner>}
                {plan && plan.valid !== false && <PlanView plan={plan} />}
              </div>
            ),
          },
        ]}
      />
      {applying && plan && (
        <ApplyDialog
          projectId={projectId}
          config={text}
          plan={plan}
          canPrune={isAdmin}
          onClose={() => setApplying(false)}
          onApplied={(id) => {
            setApplying(false)
            setApplied(true)
            setPlan(null)
            if (!id) return
            void navigate({ to: '/o/$org/deployments', params: { org } })
          }}
        />
      )}
    </section>
  )
}
