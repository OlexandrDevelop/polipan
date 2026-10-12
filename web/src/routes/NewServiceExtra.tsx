// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api, postYaml } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Stepper } from '@/components/Stepper'
import {
  Banner,
  Button,
  Card,
  CheckboxField,
  EmptyState,
  FieldGrid,
  Hint,
  Optional,
  SectionNav,
  SelectField,
  Skeleton,
  TextAreaField,
  TextField,
} from '@/components/ui'
import { composeProblems } from '@/composeProblems'
import { t } from '@/i18n'
import {
  initialValues,
  inputProblems,
  instantiateBody,
  TemplateInputs,
  TemplateServices,
  useTemplates,
} from '@/routes/Catalog'
import type { Template } from '@/routes/Catalog'
import type { Environment, Project } from '@/scope'
import { newKey } from '../idempotency'

type Service = components['schemas']['Service']
type Engine = NonNullable<components['schemas']['ServiceCreate']['db_engine']>

export type ExtraSource = 'template' | 'database' | 'compose'

interface Base {
  org: string
  project: string
  env: string
}

const NAME_PATTERN = /^[a-z0-9-]{1,40}$/
const MEMORY_PATTERN = /^[0-9]+(Ki|Mi|Gi|Ti)$/

// The engines of REQ-DB-001 and the versions the UI offers. The server checks the version again.
const ENGINES: { engine: Engine; versions: string[] }[] = [
  { engine: 'postgres', versions: ['16', '17', '15', '14'] },
  { engine: 'mysql', versions: ['8.4', '9.1', '8.0'] },
  { engine: 'mariadb', versions: ['11', '10.11'] },
  { engine: 'mongo', versions: ['7', '8', '6'] },
  { engine: 'redis', versions: ['7', '8', '6'] },
]

function DatabaseWizard({ base, environment }: { base: Base; environment: Environment }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [step, setStep] = useState(0)
  const [engine, setEngine] = useState<Engine>('postgres')
  const [version, setVersion] = useState('16')
  const [name, setName] = useState('')
  const [memory, setMemory] = useState('')
  const [deployNow, setDeployNow] = useState(true)
  const [tried, setTried] = useState(false)
  const [created, setCreated] = useState<Service | null>(null)
  const versions = ENGINES.find((e) => e.engine === engine)?.versions ?? []
  const errors = {
    name: NAME_PATTERN.test(name) ? null : t('service.name_invalid'),
    memory: memory === '' || MEMORY_PATTERN.test(memory) ? null : t('settings.memory_invalid'),
  }

  const create = useMutation({
    mutationFn: async () => {
      const service = await unwrap(
        api.POST('/environments/{environment_id}/services', {
          params: { path: { environment_id: environment.id ?? '' } },
          body: {
            name,
            type: 'database',
            db_engine: engine,
            db_version: version,
            ...(memory ? { config: { resources: { memory } } } : {}),
          },
        }),
      )
      if (deployNow) {
        await unwrap(
          api.POST('/services/{service_id}/deploy', {
            params: {
              path: { service_id: service.id ?? '' },
              header: { 'Idempotency-Key': newKey() },
            },
            body: { dry_run: false },
          }),
        )
      }
      return service
    },
    onSuccess: async (service) => {
      await queryClient.invalidateQueries({ queryKey: ['services', environment.id] })
      setCreated(service)
    },
  })

  function next(event: FormEvent) {
    event.preventDefault()
    if (step === 1 && (errors.name || errors.memory)) {
      setTried(true)
      return
    }
    if (step === 2) create.mutate()
    else setStep(step + 1)
  }

  if (created) {
    const svc = { ...base, service: created.name ?? name }
    return (
      <div className="max-w-2xl space-y-4">
        <Banner variant="info" title={t('database.created_title', { name: created.name ?? '' })}>
          {t('database.created_text')}
        </Banner>
        <Card className="space-y-2 text-sm">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
            <dt className="text-muted">{t('database.engine')}</dt>
            <dd>
              {created.db_engine} {created.db_version}
            </dd>
            <dt className="text-muted">{t('database.user')}</dt>
            <dd className="font-mono">DB_USER</dd>
            <dt className="text-muted">{t('database.password')}</dt>
            <dd className="font-mono">•••••••• ({t('database.password_hidden')})</dd>
            <dt className="text-muted">{t('database.reference')}</dt>
            <dd className="break-all font-mono">{`\${{db.${created.name}.URL}}`}</dd>
          </dl>
          <p className="text-muted">{t('database.reference_hint')}</p>
        </Card>
        {created.warnings?.map((w) => (
          <Banner key={w.code} variant="warning">
            {w.message}
          </Banner>
        ))}
        {created.backup?.status === 'none' && (
          <Banner variant="warning" title={t('backup.suggestion_title')}>
            {t('backup.suggestion_text')}
          </Banner>
        )}
        <div className="flex flex-wrap gap-2">
          <Link
            to="/o/$org/p/$project/e/$env/s/$service/$tab"
            params={{ ...svc, tab: 'backups' }}
            className="text-primary underline"
          >
            {t('database.set_backup')}
          </Link>
          <Button
            variant="primary"
            onClick={() =>
              void navigate({ to: '/o/$org/p/$project/e/$env/s/$service', params: svc })
            }
          >
            {t('database.open')}
          </Button>
        </div>
      </div>
    )
  }

  const steps = [t('database.step.engine'), t('database.step.options'), t('wizard.step.review')]
  return (
    <div className="gap-8 lg:grid lg:grid-cols-[200px_minmax(0,1fr)]">
      <div className="mb-4">
        <Stepper steps={steps} current={step} label={t('wizard.steps')} />
      </div>
      <form onSubmit={next} noValidate className="min-w-0 max-w-2xl space-y-6">
        <h2 className="text-lg font-semibold">{steps[step]}</h2>
        {step === 0 && (
          <>
            <SelectField
              label={t('database.engine')}
              value={engine}
              onChange={(e) => {
                const next = e.target.value as Engine
                setEngine(next)
                setVersion(ENGINES.find((x) => x.engine === next)?.versions[0] ?? '')
              }}
            >
              {ENGINES.map((e) => (
                <option key={e.engine} value={e.engine}>
                  {t(`database.engine.${e.engine}`)}
                </option>
              ))}
            </SelectField>
            <SelectField
              label={t('database.version')}
              hint={t('database.version_hint')}
              value={version}
              onChange={(e) => setVersion(e.target.value)}
            >
              {versions.map((v) => (
                <option key={v} value={v}>
                  {v}
                </option>
              ))}
            </SelectField>
          </>
        )}
        {step === 1 && (
          <>
            <TextField
              label={t('service.name')}
              hint={t('database.name_hint')}
              required
              autoComplete="off"
              autoCapitalize="none"
              error={tried ? errors.name : null}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            <Optional filled={memory !== '' || Boolean(tried && errors.memory)}>
              <TextField
                label={t('database.memory')}
                hint={t('database.memory_hint')}
                autoComplete="off"
                error={tried ? errors.memory : null}
                value={memory}
                onChange={(e) => setMemory(e.target.value)}
              />
            </Optional>
          </>
        )}
        {step === 2 && (
          <div className="space-y-4">
            <Card className="space-y-1 text-sm">
              <p>{t('database.review_engine', { engine, version })}</p>
              <p>{t('database.review_name', { name })}</p>
              <p>{t('database.review_volume')}</p>
              <p>{t('database.review_password')}</p>
              <p>{t('database.review_private')}</p>
              <p>{t('database.review_recreate')}</p>
            </Card>
            <label className="flex items-center gap-3 text-sm">
              <input
                type="checkbox"
                checked={deployNow}
                onChange={(e) => setDeployNow(e.target.checked)}
                className="size-4 accent-primary"
              />
              {t('wizard.deploy_now')}
            </label>
            {create.error && (
              <ErrorBanner error={create.error} title={t('service.create_failed')} />
            )}
          </div>
        )}
        <div className="flex flex-wrap justify-between gap-2 border-t border-border py-3">
          <Button
            onClick={() => setStep(Math.max(0, step - 1))}
            reason={step === 0 ? t('wizard.first_step') : undefined}
          >
            {t('wizard.back')}
          </Button>
          <Button type="submit" variant="primary" loading={create.isPending}>
            {step === 2 ? t('service.create') : t('wizard.next')}
          </Button>
        </div>
      </form>
    </div>
  )
}

function TemplateWizard({ base, environment }: { base: Base; environment: Environment }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const templates = useTemplates()
  const [step, setStep] = useState(0)
  const [slug, setSlug] = useState('')
  const [values, setValues] = useState<Record<string, string>>({})
  const [tried, setTried] = useState(false)
  const template = templates.data?.find((x) => x.slug === slug)
  const errors = template && tried ? inputProblems(template, values) : {}

  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/templates/{slug}/instantiate', {
          params: { path: { slug }, header: { 'Idempotency-Key': newKey() } },
          body: instantiateBody(template as Template, environment.id ?? '', values),
        }),
      ),
    onSuccess: async () => {
      setValues({}) // secrets are write-only
      await queryClient.invalidateQueries({ queryKey: ['services', environment.id] })
      await navigate({ to: '/o/$org/p/$project/e/$env', params: base })
    },
  })

  function pick(x: Template) {
    setSlug(x.slug ?? '')
    setValues(initialValues(x))
    setTried(false)
    setStep(1)
  }

  function next(event: FormEvent) {
    event.preventDefault()
    if (step === 1 && template) {
      setTried(true)
      if (Object.keys(inputProblems(template, values)).length > 0) return
      setStep(2)
    } else if (step === 2) create.mutate()
  }

  const steps = [t('template.step.template'), t('template.step.inputs'), t('wizard.step.review')]
  return (
    <div className="gap-8 lg:grid lg:grid-cols-[200px_minmax(0,1fr)]">
      <div className="mb-4">
        <Stepper steps={steps} current={step} label={t('wizard.steps')} />
      </div>
      <div className="min-w-0 max-w-3xl space-y-6">
        <h2 className="text-lg font-semibold">{steps[step]}</h2>
        {step === 0 && (
          <>
            {templates.isPending && <Skeleton />}
            {templates.isError && (
              <ErrorBanner error={templates.error} onRetry={() => void templates.refetch()} />
            )}
            {templates.data?.length === 0 && (
              <EmptyState title={t('template.empty_title')} text={t('template.empty_text')} />
            )}
            <ul className="grid gap-4 sm:grid-cols-2">
              {(templates.data ?? []).map((x) => (
                <li key={x.slug}>
                  <button
                    type="button"
                    onClick={() => pick(x)}
                    className="block h-full w-full rounded-md border border-border bg-surface p-4 text-left hover:border-primary"
                  >
                    <span className="block font-semibold text-primary underline">{x.name}</span>
                    <span className="mt-1 block text-sm text-muted">{x.description}</span>
                  </button>
                </li>
              ))}
            </ul>
          </>
        )}
        {step > 0 && template && (
          <form onSubmit={next} noValidate className="space-y-6">
            {step === 1 && (
              <TemplateInputs
                template={template}
                values={values}
                errors={errors}
                onChange={(name, v) => setValues((s) => ({ ...s, [name]: v }))}
              />
            )}
            {step === 2 && (
              <div className="space-y-4">
                <Card className="space-y-2">
                  <p className="font-semibold">{template.name}</p>
                  <TemplateServices template={template} />
                </Card>
                <Hint>{t('template.all_or_nothing')}</Hint>
                {create.error && (
                  <ErrorBanner error={create.error} title={t('template.create_failed')} />
                )}
              </div>
            )}
            <div className="flex flex-wrap justify-between gap-2 border-t border-border py-3">
              <Button onClick={() => setStep(step - 1)}>{t('wizard.back')}</Button>
              <Button type="submit" variant="primary" loading={create.isPending}>
                {step === 2 ? t('template.create') : t('wizard.next')}
              </Button>
            </div>
          </form>
        )}
      </div>
    </div>
  )
}

interface ComposeResult {
  services?: components['schemas']['ServiceCreate'][] | Service[]
  warnings?: string[]
}

// The variables of the file as NAME=value lines.
function parseVariables(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const trimmed = line.trim()
    if (!trimmed || trimmed.startsWith('#')) continue
    const at = trimmed.indexOf('=')
    if (at > 0) out[trimmed.slice(0, at).trim()] = trimmed.slice(at + 1).trim()
  }
  return out
}

// What the check or the import says: the services, the warnings, or the parts that cannot be imported.
function ComposeOutcome({ error, result }: { error: unknown; result: ComposeResult | null }) {
  const problems = composeProblems(error instanceof Error ? error.message : undefined)
  return (
    <>
      {Boolean(error) && !problems && <ErrorBanner error={error} title={t('compose.failed')} />}
      {problems && (
        <Banner variant="danger" title={t('compose.problems_title')}>
          <p className="text-muted">{t('compose.safe_tip')}</p>
          <ul className="mt-2 space-y-1">
            {problems.map((p) => (
              <li key={`${p.service}-${p.text}`}>
                {p.service && <strong className="font-term">{p.service}: </strong>}
                {p.text}
              </li>
            ))}
          </ul>
        </Banner>
      )}
      {result && (
        <div className="space-y-3">
          <Card className="space-y-2">
            <p className="font-semibold">
              {t('compose.result', { count: String(result.services?.length ?? 0) })}
            </p>
            <ul className="list-disc pl-5 text-sm">
              {(result.services ?? []).map((s) => (
                <li key={s.name}>{s.name}</li>
              ))}
            </ul>
          </Card>
          {(result.warnings ?? []).length > 0 && (
            <Banner variant="warning" title={t('compose.warnings')}>
              <ul className="list-disc pl-5">
                {(result.warnings ?? []).map((w) => (
                  <li key={w}>{w}</li>
                ))}
              </ul>
            </Banner>
          )}
        </div>
      )}
    </>
  )
}

// The Compose file of a Git repository: the check, the choices and the import. The repository, the branch and the
// path of the file come from the caller.
export function ComposeGitPanel({
  base,
  environment,
  repo,
  branch,
  file,
  connectionId,
}: {
  base: Base
  environment: Environment
  repo: string
  branch: string
  file: string
  connectionId?: string
}) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [variables, setVariables] = useState('')
  const [skipUnsafe, setSkipUnsafe] = useState(true)
  const [result, setResult] = useState<ComposeResult | null>(null)
  const send = (dry: boolean): Promise<ComposeResult> =>
    unwrap(
      api.POST('/environments/{environment_id}/import-compose-git', {
        params: {
          path: { environment_id: environment.id ?? '' },
          query: dry ? { dry_run: true } : {},
        },
        body: {
          repo: repo.trim(),
          branch: branch.trim() || 'main',
          path: file.trim() || 'docker-compose.yml',
          connection_id: connectionId || undefined,
          skip_unsafe: skipUnsafe,
          variables: parseVariables(variables),
        },
      }),
    ) as Promise<ComposeResult>
  const check = useMutation({ mutationFn: () => send(true), onSuccess: setResult })
  const run = useMutation({
    mutationFn: () => send(false),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['services', environment.id] })
      await navigate({ to: '/o/$org/p/$project/e/$env', params: base })
    },
  })
  const ready = repo.trim() !== ''
  return (
    <div className="space-y-4">
      <CheckboxField
        label={t('compose.skip_unsafe')}
        hint={t('compose.skip_unsafe_hint')}
        checked={skipUnsafe}
        onChange={(e) => {
          setSkipUnsafe(e.target.checked)
          setResult(null)
        }}
      />
      <Optional filled={variables !== ''}>
        <TextAreaField
          label={t('compose.variables')}
          hint={t('compose.variables_hint')}
          rows={4}
          spellCheck={false}
          autoCapitalize="none"
          value={variables}
          onChange={(e) => {
            setVariables(e.target.value)
            setResult(null)
          }}
        />
      </Optional>
      <ComposeOutcome error={check.error ?? run.error} result={result} />
      <div className="flex flex-wrap justify-end gap-2">
        <Button
          loading={check.isPending}
          reason={ready ? undefined : t('compose.fill_first')}
          onClick={() => check.mutate()}
        >
          {t('compose.check')}
        </Button>
        <Button
          variant="primary"
          loading={run.isPending}
          reason={result ? undefined : t('compose.check_first')}
          onClick={() => run.mutate()}
        >
          {t('compose.import')}
        </Button>
      </div>
    </div>
  )
}

// UI-05: Compose import. The file is pasted, or it is read from a Git repository. The check (dry_run)
// shows the services and the warnings first.
function ComposeWizard({ base, environment }: { base: Base; environment: Environment }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [mode, setMode] = useState<'git' | 'paste'>('git')
  const [text, setText] = useState('')
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [file, setFile] = useState('docker-compose.yml')
  const [variables, setVariables] = useState('')
  const [skipUnsafe, setSkipUnsafe] = useState(true)
  const [result, setResult] = useState<ComposeResult | null>(null)
  const path = `/environments/${environment.id}/import-compose`
  const ready = mode === 'paste' ? text.trim() !== '' : repo.trim() !== ''
  const send = (dry: boolean): Promise<ComposeResult> => {
    if (mode === 'paste') {
      const query = [dry && 'dry_run=true', skipUnsafe && 'skip_unsafe=true']
        .filter(Boolean)
        .join('&')
      return postYaml<ComposeResult>(`${path}${query ? `?${query}` : ''}`, text)
    }
    return unwrap(
      api.POST('/environments/{environment_id}/import-compose-git', {
        params: {
          path: { environment_id: environment.id ?? '' },
          query: dry ? { dry_run: true } : {},
        },
        body: {
          repo: repo.trim(),
          branch: branch.trim() || 'main',
          path: file.trim() || 'docker-compose.yml',
          skip_unsafe: skipUnsafe,
          variables: parseVariables(variables),
        },
      }),
    ) as Promise<ComposeResult>
  }
  const check = useMutation({
    mutationFn: () => send(true),
    onSuccess: setResult,
  })
  const run = useMutation({
    mutationFn: () => send(false),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['services', environment.id] })
      await navigate({ to: '/o/$org/p/$project/e/$env', params: base })
    },
  })
  const failure = check.error ?? run.error
  const changed = () => setResult(null)
  return (
    <form
      className="max-w-3xl space-y-4"
      onSubmit={(e) => {
        e.preventDefault()
        if (ready) check.mutate()
      }}
    >
      <SectionNav
        horizontal
        controls={false}
        label={t('compose.mode_label')}
        items={[
          { id: 'git', label: t('compose.mode.git') },
          { id: 'paste', label: t('compose.mode.paste') },
        ]}
        active={mode}
        onChange={(id) => {
          setMode(id as 'git' | 'paste')
          changed()
        }}
      />
      {mode === 'paste' ? (
        <TextAreaField
          label={t('compose.label')}
          hint={t('compose.hint')}
          rows={14}
          spellCheck={false}
          value={text}
          onChange={(e) => {
            setText(e.target.value)
            changed()
          }}
        />
      ) : (
        <div className="space-y-4">
          <TextField
            label={t('compose.repo')}
            hint={t('compose.repo_hint')}
            required
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            value={repo}
            onChange={(e) => {
              setRepo(e.target.value)
              changed()
            }}
          />
          <FieldGrid>
            <TextField
              label={t('wizard.branch')}
              autoComplete="off"
              spellCheck={false}
              value={branch}
              onChange={(e) => {
                setBranch(e.target.value)
                changed()
              }}
            />
            <TextField
              label={t('compose.file')}
              hint={t('compose.file_hint')}
              autoComplete="off"
              spellCheck={false}
              value={file}
              onChange={(e) => {
                setFile(e.target.value)
                changed()
              }}
            />
          </FieldGrid>
        </div>
      )}
      <CheckboxField
        label={t('compose.skip_unsafe')}
        hint={t('compose.skip_unsafe_hint')}
        checked={skipUnsafe}
        onChange={(e) => {
          setSkipUnsafe(e.target.checked)
          changed()
        }}
      />
      {mode === 'git' && (
        <Optional filled={variables !== ''}>
          <TextAreaField
            label={t('compose.variables')}
            hint={t('compose.variables_hint')}
            rows={4}
            spellCheck={false}
            autoCapitalize="none"
            value={variables}
            onChange={(e) => {
              setVariables(e.target.value)
              changed()
            }}
          />
        </Optional>
      )}
      <ComposeOutcome error={failure} result={result} />
      <div className="flex flex-wrap justify-end gap-2">
        <Button
          type="submit"
          loading={check.isPending}
          reason={ready ? undefined : t('compose.fill_first')}
        >
          {t('compose.check')}
        </Button>
        <Button
          variant="primary"
          loading={run.isPending}
          reason={result ? undefined : t('compose.check_first')}
          onClick={() => run.mutate()}
        >
          {t('compose.import')}
        </Button>
      </div>
    </form>
  )
}

// The start paths template, database and compose of the wizard (UI-32, UI-05).
export function ExtraWizard({
  source,
  base,
  project,
  environment,
}: {
  source: ExtraSource
  base: Base
  project: Project
  environment: Environment
}) {
  void project
  return (
    <div>
      <div className="mb-4 text-sm">
        <Link
          to="/o/$org/p/$project/e/$env/new"
          params={base}
          search={{}}
          className="text-primary underline"
        >
          {t('wizard.change_source')}
        </Link>
      </div>
      {source === 'database' && <DatabaseWizard base={base} environment={environment} />}
      {source === 'template' && <TemplateWizard base={base} environment={environment} />}
      {source === 'compose' && <ComposeWizard base={base} environment={environment} />}
    </div>
  )
}
