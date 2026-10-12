// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useDeferredValue, useEffect, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { AddGitConnectionDialog } from '@/components/GitConnectionDialog'
import { filledRows, KeyValueEditor, rowErrors } from '@/components/KeyValueEditor'
import type { KeyValueRow } from '@/components/KeyValueEditor'
import { PageHeader } from '@/components/PageHeader'
import { Stepper } from '@/components/Stepper'
import { useUnsavedGuard } from '@/components/UnsavedGuard'
import {
  Banner,
  Button,
  Card,
  CheckboxField,
  Optional,
  SecretField,
  SelectField,
  Skeleton,
  TextField,
} from '@/components/ui'
import { splitCommand } from '@/envText'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'
import { NotFoundPage } from '@/routes/ErrorPages'
import { ComposeGitPanel, ExtraWizard } from '@/routes/NewServiceExtra'
import type { ExtraSource } from '@/routes/NewServiceExtra'
import { useScope } from '@/scope'
import type { Environment, Project, Service } from '@/scope'
import { newKey } from '../idempotency'

type Deployment = components['schemas']['Deployment']
export type SourceKind = 'git' | 'image'
export type StartPath = SourceKind | ExtraSource

const NAME_PATTERN = /^[a-z0-9-]{1,40}$/
const CPU_PATTERN = /^[0-9]+m?$|^[0-9]+\.[0-9]+$/
const MEMORY_PATTERN = /^[0-9]+(Ki|Mi|Gi|Ti)$/

interface Draft {
  connectionId: string
  repo: string
  branch: string
  builder: 'railpack' | 'dockerfile'
  dockerfile: string
  path: string
  image: string
  registryLogin: boolean
  registryHost: string
  registryUser: string
  registryPassword: string
  name: string
  port: string
  command: string
  replicas: string
  cpu: string
  memory: string
  domain: string
  https: boolean
  vars: KeyValueRow[]
  deployNow: boolean
  // Read a Docker Compose file of the repository and make its services, instead of one service.
  useCompose: boolean
  composeFile: string
}

const emptyDraft: Draft = {
  connectionId: '',
  repo: '',
  branch: 'main',
  builder: 'railpack',
  dockerfile: 'Dockerfile',
  path: '/',
  image: '',
  registryLogin: false,
  registryHost: '',
  registryUser: '',
  registryPassword: '',
  name: '',
  port: '',
  command: '',
  replicas: '1',
  cpu: '',
  memory: '',
  domain: '',
  https: true,
  vars: [],
  deployNow: true,
  useCompose: false,
  composeFile: 'docker-compose.yml',
}

type Errors = Partial<Record<keyof Draft, string>>

function validateStep(step: number, source: SourceKind, d: Draft): Errors {
  const e: Errors = {}
  if (step === 0) {
    if (source === 'git') {
      if (d.repo.trim() === '' || /\s/.test(d.repo.trim())) e.repo = t('wizard.repo_invalid')
      if (d.branch.trim() === '' || /\s/.test(d.branch.trim()))
        e.branch = t('wizard.branch_invalid')
    } else {
      if (d.image.trim() === '' || /\s/.test(d.image.trim())) e.image = t('service.image_invalid')
      if (d.registryLogin) {
        if (d.registryHost.trim() === '') e.registryHost = t('wizard.registry_host_invalid')
        if (d.registryUser.trim() === '') e.registryUser = t('wizard.registry_user_invalid')
        if (d.registryPassword === '') e.registryPassword = t('wizard.registry_password_invalid')
      }
    }
  }
  if (step === 1) {
    if (!NAME_PATTERN.test(d.name)) e.name = t('service.name_invalid')
    if (d.port !== '') {
      const n = Number(d.port)
      if (!Number.isInteger(n) || n < 1 || n > 65535) e.port = t('service.port_invalid')
    }
    const r = Number(d.replicas)
    if (!Number.isInteger(r) || r < 0 || r > 100) e.replicas = t('settings.replicas_invalid')
    if (d.cpu !== '' && !CPU_PATTERN.test(d.cpu)) e.cpu = t('settings.cpu_invalid')
    if (d.memory !== '' && !MEMORY_PATTERN.test(d.memory)) e.memory = t('settings.memory_invalid')
  }
  if (step === 2) {
    const host = d.domain.trim().toLowerCase()
    if (host !== '' && !/^(?=.{1,253}$)([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$/.test(host)) {
      e.domain = t('domains.host_invalid')
    }
    if (rowErrors(d.vars).some(Boolean)) e.vars = t('wizard.vars_invalid')
  }
  return e
}

// The secret that holds the registry login of an image service.
function registrySecretName(service: string): string {
  return `REGISTRY_${service.toUpperCase().replace(/-/g, '_')}`
}

function sourceBody(source: SourceKind, d: Draft) {
  if (source === 'git') {
    return {
      type: 'git',
      ...(d.connectionId ? { connection_id: d.connectionId } : {}),
      repo: d.repo.trim(),
      branch: d.branch.trim(),
      path: d.path.trim() || '/',
      builder: d.builder,
      ...(d.builder === 'dockerfile' ? { dockerfile: d.dockerfile.trim() || 'Dockerfile' } : {}),
    }
  }
  return {
    type: 'image',
    image: d.image.trim(),
    ...(d.registryLogin ? { registry_secret: registrySecretName(d.name) } : {}),
  }
}

function configBody(source: SourceKind, d: Draft) {
  const command = splitCommand(d.command)
  const resources = {
    ...(d.cpu ? { cpu: d.cpu } : {}),
    ...(d.memory ? { memory: d.memory } : {}),
  }
  return {
    ...(source === 'image' ? { image: d.image.trim() } : {}),
    ...(d.port ? { port: Number(d.port) } : {}),
    ...(command.length > 0 ? { command } : {}),
    ...(d.replicas !== '1' ? { replicas: Number(d.replicas) } : {}),
    ...(Object.keys(resources).length > 0 ? { resources } : {}),
  }
}

// Writes the registry login as a secret of the environment. A retry after a failed create
// finds the secret from the first try and writes a new version.
async function saveRegistrySecret(project: Project, environment: Environment, d: Draft) {
  const name = registrySecretName(d.name)
  const value = JSON.stringify({
    registry: d.registryHost.trim(),
    username: d.registryUser.trim(),
    password: d.registryPassword,
  })
  const projectId = project.id ?? ''
  try {
    await unwrap(
      api.POST('/projects/{project_id}/secrets', {
        params: { path: { project_id: projectId } },
        body: { name, value, environment_id: environment.id, preview: false, build_time: false },
      }),
    )
  } catch (error) {
    if (!(error instanceof ApiError) || error.status !== 409) throw error
    const list = await unwrap(
      api.GET('/projects/{project_id}/secrets', {
        params: { path: { project_id: projectId }, query: { environment_id: environment.id } },
      }),
    )
    const found = list.find(
      (s) => s.name === name && s.environment_id === environment.id && !s.service_id,
    )
    if (!found?.id) throw error
    await unwrap(
      api.PUT('/secrets/{secret_id}', {
        params: { path: { secret_id: found.id } },
        body: { value },
      }),
    )
  }
}

function useGitConnections(enabled: boolean) {
  return useQuery({
    queryKey: ['git-connections'],
    enabled,
    queryFn: () => unwrap(api.GET('/git-connections')),
  })
}

// Picks a repository of a connection with a search (listGitRepos).
function RepoPicker({
  connectionId,
  value,
  onPick,
}: {
  connectionId: string
  value: string
  onPick: (repo: string, branch?: string) => void
}) {
  const [q, setQ] = useState('')
  const query = useDeferredValue(q.trim())
  const repos = useQuery({
    queryKey: ['git-repos', connectionId, query],
    queryFn: () =>
      unwrap(
        api.GET('/git-connections/{connection_id}/repos', {
          params: { path: { connection_id: connectionId }, query: query ? { q: query } : {} },
        }),
      ),
  })
  const list = (repos.data ?? []).slice(0, 20)
  return (
    <fieldset className="space-y-2">
      <legend className="text-sm font-medium">{t('wizard.repo')}</legend>
      <TextField
        label={t('wizard.repo_search')}
        type="search"
        autoComplete="off"
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      {repos.isPending && <Skeleton lines={2} />}
      {repos.isError && <ErrorBanner error={repos.error} onRetry={() => void repos.refetch()} />}
      {repos.data && list.length === 0 && <p className="text-muted">{t('wizard.repo_none')}</p>}
      <ul className="max-h-72 space-y-1 overflow-auto">
        {list.map((r) => (
          <li key={r.full_name}>
            <button
              type="button"
              aria-pressed={value === r.full_name}
              onClick={() => onPick(r.full_name ?? '', r.default_branch)}
              className="flex min-h-10 w-full items-center justify-between gap-2 rounded-sm border border-border px-3 py-1 text-left text-sm hover:bg-surface aria-[pressed=true]:border-primary aria-[pressed=true]:font-semibold coarse:min-h-11"
            >
              <span className="break-all">{r.full_name}</span>
              {r.private && <span className="text-muted">{t('wizard.repo_private')}</span>}
            </button>
          </li>
        ))}
      </ul>
    </fieldset>
  )
}

function GitSourceStep({
  draft,
  set,
  errors,
}: {
  draft: Draft
  set: (patch: Partial<Draft>) => void
  errors: Errors
}) {
  const canManage = useAccess('git:manage') === 'allow'
  const connections = useGitConnections(canManage)
  const [adding, setAdding] = useState(false)
  return (
    <div className="space-y-4">
      {canManage ? (
        <div className="space-y-2">
          {connections.isError && (
            <ErrorBanner error={connections.error} onRetry={() => void connections.refetch()} />
          )}
          <SelectField
            label={t('wizard.connection')}
            hint={t('wizard.connection_hint')}
            value={draft.connectionId}
            onChange={(e) => set({ connectionId: e.target.value, repo: '' })}
          >
            <option value="">{t('wizard.connection_public')}</option>
            {(connections.data ?? []).map((c) => (
              <option key={c.id} value={c.id}>
                {c.name} ({t(`git.provider.${c.provider ?? 'generic'}`)})
              </option>
            ))}
          </SelectField>
          <Button onClick={() => setAdding(true)}>{t('git.add')}</Button>
        </div>
      ) : (
        <Banner>{t('wizard.connection_admin_only')}</Banner>
      )}
      {draft.connectionId ? (
        <>
          <RepoPicker
            connectionId={draft.connectionId}
            value={draft.repo}
            onPick={(repo, branch) => set({ repo, ...(branch ? { branch } : {}) })}
          />
          {errors.repo && <p className="text-sm text-danger">{errors.repo}</p>}
        </>
      ) : (
        <TextField
          label={t('wizard.repo_url')}
          hint={t('wizard.repo_url_hint')}
          required
          type="url"
          inputMode="url"
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          error={errors.repo}
          value={draft.repo}
          onChange={(e) => set({ repo: e.target.value })}
        />
      )}
      <TextField
        label={t('wizard.branch')}
        required
        autoComplete="off"
        autoCapitalize="none"
        spellCheck={false}
        error={errors.branch}
        value={draft.branch}
        onChange={(e) => set({ branch: e.target.value })}
      />
      <CheckboxField
        label={t('wizard.use_compose')}
        hint={t('wizard.use_compose_hint')}
        checked={draft.useCompose}
        onChange={(e) => set({ useCompose: e.target.checked })}
      />
      {draft.useCompose && (
        <TextField
          label={t('compose.file')}
          hint={t('compose.file_hint')}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          value={draft.composeFile}
          onChange={(e) => set({ composeFile: e.target.value })}
        />
      )}
      {adding && (
        <AddGitConnectionDialog
          onClose={() => setAdding(false)}
          onCreated={(c) => {
            setAdding(false)
            set({ connectionId: c.id ?? '', repo: '' })
          }}
        />
      )}
    </div>
  )
}

function ImageSourceStep({
  draft,
  set,
  errors,
}: {
  draft: Draft
  set: (patch: Partial<Draft>) => void
  errors: Errors
}) {
  return (
    <div className="space-y-4">
      <TextField
        label={t('service.image')}
        hint={t('service.image_hint')}
        required
        autoComplete="off"
        autoCapitalize="none"
        spellCheck={false}
        error={errors.image}
        value={draft.image}
        onChange={(e) => set({ image: e.target.value })}
      />
      <Optional filled={draft.registryLogin}>
        <CheckboxField
          label={t('wizard.registry_login')}
          hint={t('wizard.registry_login_hint')}
          checked={draft.registryLogin}
          onChange={(e) => set({ registryLogin: e.target.checked })}
        />
        {draft.registryLogin && (
          <div className="space-y-4 border-l-4 border-border pl-4">
            <TextField
              label={t('wizard.registry_host')}
              hint={t('wizard.registry_host_hint')}
              required
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              error={errors.registryHost}
              value={draft.registryHost}
              onChange={(e) => set({ registryHost: e.target.value })}
            />
            <TextField
              label={t('wizard.registry_user')}
              required
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              error={errors.registryUser}
              value={draft.registryUser}
              onChange={(e) => set({ registryUser: e.target.value })}
            />
            <SecretField
              label={t('wizard.registry_password')}
              required
              error={errors.registryPassword}
              value={draft.registryPassword}
              onValue={(registryPassword) => set({ registryPassword })}
            />
          </div>
        )}
      </Optional>
    </div>
  )
}

function BuildRunStep({
  source,
  draft,
  set,
  errors,
}: {
  source: SourceKind
  draft: Draft
  set: (patch: Partial<Draft>) => void
  errors: Errors
}) {
  return (
    <div className="space-y-4">
      <TextField
        label={t('service.name')}
        hint={t('service.name_hint')}
        required
        autoComplete="off"
        autoCapitalize="none"
        error={errors.name}
        value={draft.name}
        onChange={(e) => set({ name: e.target.value })}
      />
      <Optional
        filled={
          (source === 'git' &&
            (draft.builder !== 'railpack' || (draft.path !== '/' && draft.path !== ''))) ||
          draft.port !== '' ||
          draft.command !== '' ||
          draft.replicas !== '1' ||
          draft.cpu !== '' ||
          draft.memory !== '' ||
          Boolean(errors.port || errors.replicas || errors.cpu || errors.memory)
        }
      >
        <div className="space-y-4">
          {source === 'git' && (
            <>
              <SelectField
                label={t('settings.builder')}
                hint={t('settings.builder_hint')}
                value={draft.builder}
                onChange={(e) => set({ builder: e.target.value as Draft['builder'] })}
              >
                <option value="railpack">{t('settings.builder.railpack')}</option>
                <option value="dockerfile">{t('settings.builder.dockerfile')}</option>
              </SelectField>
              {draft.builder === 'dockerfile' && (
                <TextField
                  label={t('settings.dockerfile')}
                  autoComplete="off"
                  spellCheck={false}
                  value={draft.dockerfile}
                  onChange={(e) => set({ dockerfile: e.target.value })}
                />
              )}
              <TextField
                label={t('settings.build_path')}
                hint={t('settings.build_path_hint')}
                autoComplete="off"
                spellCheck={false}
                value={draft.path}
                onChange={(e) => set({ path: e.target.value })}
              />
            </>
          )}
          <TextField
            label={t('service.port')}
            hint={t('service.port_hint')}
            type="number"
            inputMode="numeric"
            error={errors.port}
            value={draft.port}
            onChange={(e) => set({ port: e.target.value })}
          />
          <TextField
            label={t('settings.command')}
            hint={t('settings.command_hint')}
            autoComplete="off"
            spellCheck={false}
            value={draft.command}
            onChange={(e) => set({ command: e.target.value })}
          />
          <TextField
            label={t('settings.replicas')}
            type="number"
            inputMode="numeric"
            error={errors.replicas}
            value={draft.replicas}
            onChange={(e) => set({ replicas: e.target.value })}
          />
          <TextField
            label={t('settings.cpu')}
            hint={t('settings.cpu_hint')}
            autoComplete="off"
            error={errors.cpu}
            value={draft.cpu}
            onChange={(e) => set({ cpu: e.target.value })}
          />
          <TextField
            label={t('settings.memory')}
            hint={t('settings.memory_hint')}
            autoComplete="off"
            error={errors.memory}
            value={draft.memory}
            onChange={(e) => set({ memory: e.target.value })}
          />
        </div>
      </Optional>
    </div>
  )
}

function DomainEnvStep({
  draft,
  set,
  errors,
  showErrors,
}: {
  draft: Draft
  set: (patch: Partial<Draft>) => void
  errors: Errors
  showErrors: boolean
}) {
  return (
    <div className="space-y-4">
      <Optional label={t('wizard.domain')} filled={draft.domain !== '' || Boolean(errors.domain)}>
        <div className="space-y-4">
          <TextField
            label={t('wizard.domain')}
            hint={t('wizard.domain_hint')}
            type="text"
            inputMode="url"
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            error={errors.domain}
            value={draft.domain}
            onChange={(e) => set({ domain: e.target.value })}
          />
          {draft.domain.trim() !== '' && (
            <CheckboxField
              label={t('domains.https')}
              hint={t('domains.https_hint')}
              checked={draft.https}
              onChange={(e) => set({ https: e.target.checked })}
            />
          )}
        </div>
      </Optional>
      <Optional label={t('wizard.variables')} filled={filledRows(draft.vars).length > 0}>
        <fieldset className="space-y-2">
          <legend className="sr-only">{t('wizard.variables')}</legend>
          <p className="text-muted">{t('wizard.variables_hint')}</p>
          <KeyValueEditor
            rows={draft.vars}
            onChange={(vars) => set({ vars })}
            showErrors={showErrors}
          />
        </fieldset>
      </Optional>
    </div>
  )
}

function Summary({ source, draft }: { source: SourceKind; draft: Draft }) {
  const vars = filledRows(draft.vars)
  const rows: [string, ReactNode][] = [
    [t('wizard.source'), t(`wizard.source.${source}`)],
    source === 'git'
      ? [t('wizard.repo'), draft.repo ? `${draft.repo} @ ${draft.branch}` : '-']
      : [t('service.image'), draft.image || '-'],
    [t('service.name'), draft.name || '-'],
    [t('service.port'), draft.port || '-'],
    [t('wizard.domain'), draft.domain || '-'],
    [t('wizard.variables'), t('wizard.variable_count', { count: vars.length })],
  ]
  if (source === 'git')
    rows.splice(2, 0, [t('settings.builder'), t(`settings.builder.${draft.builder}`)])
  if (source === 'image' && draft.registryLogin) {
    rows.splice(2, 0, [t('wizard.registry_login'), draft.registryHost])
  }
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted">{k}</dt>
          <dd className="min-w-0 break-words">{v}</dd>
        </div>
      ))}
    </dl>
  )
}

function SourceChooser({ base }: { base: { org: string; project: string; env: string } }) {
  const card =
    'block h-full rounded-md border border-border bg-surface p-5 text-left hover:border-primary'
  return (
    <ul className="grid gap-4 sm:grid-cols-2">
      {(['git', 'image'] as const).map((source) => (
        <li key={source}>
          <Link
            to="/o/$org/p/$project/e/$env/new"
            params={base}
            search={{ source }}
            className={card}
          >
            <span className="block text-lg font-semibold text-primary underline">
              {t(`wizard.source.${source}`)}
            </span>
            <span className="mt-1 block text-muted">{t(`wizard.source.${source}_text`)}</span>
          </Link>
        </li>
      ))}
      {(['template', 'database', 'compose'] as const).map((source) => (
        <li key={source}>
          <Link
            to="/o/$org/p/$project/e/$env/new"
            params={base}
            search={{ source }}
            className={card}
          >
            <span className="block text-lg font-semibold text-primary underline">
              {t(`wizard.source.${source}`)}
            </span>
            <span className="mt-1 block text-muted">{t(`wizard.source.${source}_text`)}</span>
          </Link>
        </li>
      ))}
    </ul>
  )
}

// UI-05 (chooser) and UI-32 (wizard, git and image paths). Stepper: Source → Build and run →
// Domain and env → Review. Data stays when going back; leaving with data asks first (REQ-UI-031).
export function NewServicePage() {
  const {
    org,
    project: projectSlug,
    env: envSlug,
  } = useParams({ from: '/o/$org/p/$project/e/$env/new' })
  const {
    source,
    repo: searchRepo,
    branch: searchBranch,
    connection: searchConnection,
  } = useSearch({
    from: '/o/$org/p/$project/e/$env/new',
  })
  const scope = useScope(projectSlug, envSlug)
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const access = useAccess('service:create', scope.environment?.protected)
  const [draft, setDraft] = useState<Draft>(emptyDraft)
  const [step, setStep] = useState(0)
  const [tried, setTried] = useState<boolean[]>([false, false, false, false])
  const created = useRef<{ service?: Service; domain?: boolean; vars?: boolean }>({})
  const heading = useRef<HTMLHeadingElement>(null)
  const firstRender = useRef(true)

  const dirty = JSON.stringify(draft) !== JSON.stringify(emptyDraft)
  const guard = useUnsavedGuard(dirty)
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))
  const base = { org, project: projectSlug, env: envSlug }

  useEffect(() => {
    if (firstRender.current) {
      firstRender.current = false
      return
    }
    heading.current?.focus()
  }, [step])

  const create = useMutation({
    mutationFn: async () => {
      const project = scope.project as Project
      const environment = scope.environment as Environment
      const kind = source as SourceKind
      let service = created.current.service
      if (!service) {
        if (kind === 'image' && draft.registryLogin) {
          await saveRegistrySecret(project, environment, draft)
        }
        service = await unwrap(
          api.POST('/environments/{environment_id}/services', {
            params: { path: { environment_id: environment.id ?? '' } },
            body: {
              name: draft.name,
              type: 'app',
              source: sourceBody(kind, draft),
              config: configBody(kind, draft),
            },
          }),
        )
        created.current.service = service
      }
      const serviceId = service.id ?? ''
      if (draft.domain.trim() && !created.current.domain) {
        await unwrap(
          api.POST('/services/{service_id}/domains', {
            params: { path: { service_id: serviceId } },
            body: { host: draft.domain.trim().toLowerCase(), path_prefix: '/', https: draft.https },
          }),
        )
        created.current.domain = true
      }
      const vars = filledRows(draft.vars)
      if (vars.length > 0 && !created.current.vars) {
        await unwrap(
          api.POST('/projects/{project_id}/variables:bulk', {
            params: { path: { project_id: project.id ?? '' } },
            body: {
              environment_id: environment.id,
              service_id: serviceId,
              items: vars.map((v) => ({ name: v.name, value: v.value, secret: v.secret })),
            },
          }),
        )
        created.current.vars = true
      }
      let deployment: Deployment | undefined
      if (draft.deployNow && access === 'allow') {
        deployment = (await unwrap(
          api.POST('/services/{service_id}/deploy', {
            params: {
              path: { service_id: serviceId },
              header: { 'Idempotency-Key': newKey() },
            },
            body: { dry_run: false },
          }),
        )) as Deployment
      }
      return { service, deployment }
    },
    onSuccess: async ({ service, deployment }) => {
      // The data is saved, so leaving is fine now.
      setDraft((d) => ({ ...d, registryPassword: '' }))
      guard.allowLeave()
      await queryClient.invalidateQueries({ queryKey: ['services', scope.environment?.id] })
      const params = { ...base, service: service.name ?? draft.name }
      if (deployment?.id) {
        await navigate({
          to: '/o/$org/p/$project/e/$env/s/$service/deployments/$deployment',
          params: { ...params, deployment: deployment.id },
        })
      } else {
        await navigate({ to: '/o/$org/p/$project/e/$env/s/$service', params })
      }
    },
  })

  if (scope.state === 'pending') return <Skeleton />
  if (scope.state === 'error') return <ErrorBanner error={scope.error} onRetry={scope.retry} />
  if (scope.state === 'notfound') return <NotFoundPage />

  const back = (
    <Link to="/o/$org/p/$project/e/$env" params={base} className="text-primary underline">
      {scope.project?.name} › {scope.environment?.name}
    </Link>
  )
  const header = (
    <PageHeader
      title={t('service.new_title')}
      context={[scope.environment?.name ?? '', scope.project?.name ?? '']}
      breadcrumb={back}
    />
  )

  if (access !== 'allow') {
    return (
      <section className="max-w-xl">
        {header}
        <Banner variant="warning">
          {access === 'blocked' ? t('common.needs_admin') : t('common.no_permission')}
        </Banner>
      </section>
    )
  }

  if (!source) {
    return (
      <section className="max-w-3xl">
        {header}
        <h2 className="mb-4 text-lg font-semibold">{t('wizard.choose')}</h2>
        <SourceChooser base={base} />
      </section>
    )
  }

  if (source === 'template' || source === 'database' || source === 'compose') {
    return (
      <section className="max-w-5xl">
        {header}
        {scope.project && scope.environment && (
          <ExtraWizard
            source={source}
            base={base}
            project={scope.project}
            environment={scope.environment}
            initial={{ repo: searchRepo, branch: searchBranch, connection: searchConnection }}
          />
        )}
      </section>
    )
  }

  const steps = [
    t('wizard.step.source'),
    t('wizard.step.build'),
    t('wizard.step.domain'),
    t('wizard.step.review'),
  ]
  const errors = tried[step] ? validateStep(step, source, draft) : {}
  const last = step === steps.length - 1

  const composeMode = source === 'git' && draft.useCompose

  function next(event: FormEvent) {
    event.preventDefault()
    if (composeMode) return
    if (last) {
      create.mutate()
      return
    }
    const stepErrors = validateStep(step, source as SourceKind, draft)
    if (Object.keys(stepErrors).length > 0) {
      setTried((list) => list.map((v, i) => (i === step ? true : v)))
      return
    }
    setStep(step + 1)
  }

  return (
    <section>
      {header}
      <div className="gap-8 lg:grid lg:grid-cols-[200px_minmax(0,1fr)_260px] 2xl:grid-cols-[220px_minmax(0,1fr)_340px]">
        <div className="mb-4">
          <Stepper steps={steps} current={step} label={t('wizard.steps')} />
        </div>
        <form onSubmit={next} noValidate className="min-w-0 max-w-2xl space-y-6">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <h2 ref={heading} tabIndex={-1} className="text-lg font-semibold outline-none">
              {steps[step]}
            </h2>
            {step === 0 && (
              <Link
                to="/o/$org/p/$project/e/$env/new"
                params={base}
                search={{}}
                className="text-sm text-primary underline"
              >
                {t('wizard.change_source')}
              </Link>
            )}
          </div>
          <div key={step} className="enter space-y-6">
            {step === 0 && source === 'git' && (
              <GitSourceStep draft={draft} set={set} errors={errors} />
            )}
            {step === 0 && source === 'image' && (
              <ImageSourceStep draft={draft} set={set} errors={errors} />
            )}
            {step === 1 && <BuildRunStep source={source} draft={draft} set={set} errors={errors} />}
            {step === 2 && (
              <DomainEnvStep draft={draft} set={set} errors={errors} showErrors={tried[2]} />
            )}
            {step === 3 && (
              <div className="space-y-4">
                <Card>
                  <Summary source={source} draft={draft} />
                </Card>
                <CheckboxField
                  label={t('wizard.deploy_now')}
                  hint={t('wizard.deploy_now_hint')}
                  checked={draft.deployNow}
                  onChange={(e) => set({ deployNow: e.target.checked })}
                />
                {create.error && (
                  <ErrorBanner error={create.error} title={t('service.create_failed')} />
                )}
                {created.current.service && create.error && (
                  <Banner>{t('wizard.partly_created')}</Banner>
                )}
              </div>
            )}
          </div>
          {composeMode && scope.project && scope.environment ? (
            <ComposeGitPanel
              base={base}
              environment={scope.environment}
              repo={draft.repo}
              branch={draft.branch}
              file={draft.composeFile}
              connectionId={draft.connectionId}
            />
          ) : (
            <div className="flex flex-wrap justify-between gap-2 border-t border-border bg-bg py-3 max-sm:sticky max-sm:bottom-[calc(4rem+env(safe-area-inset-bottom))]">
              <Button
                onClick={() => setStep(Math.max(0, step - 1))}
                reason={step === 0 ? t('wizard.first_step') : undefined}
              >
                {t('wizard.back')}
              </Button>
              <Button type="submit" variant="primary" loading={create.isPending}>
                {last ? t('service.create') : t('wizard.next')}
              </Button>
            </div>
          )}
        </form>
        <aside aria-label={t('wizard.summary')} className="hidden lg:block">
          <Card className="space-y-3">
            <h2 className="font-semibold">{t('wizard.summary')}</h2>
            <Summary source={source} draft={draft} />
          </Card>
        </aside>
      </div>
      {guard.dialog}
    </section>
  )
}
