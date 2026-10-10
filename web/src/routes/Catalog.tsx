// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useDeferredValue, useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api, postYaml } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import {
  Banner,
  Button,
  Card,
  CheckboxField,
  EmptyState,
  SecretField,
  SelectField,
  Skeleton,
  TextAreaField,
  TextField,
} from '@/components/ui'
import { t } from '@/i18n'
import { accessFor } from '@/permissions'
import { useMe } from '@/auth'
import { useEnvironments, useProjects } from '@/scope'

export type Template = components['schemas']['Template']

interface InputSpec {
  name: string
  label?: string
  type: string
  default?: unknown
  required?: boolean
  generate?: boolean
}

export function useTemplates() {
  return useQuery({
    queryKey: ['templates'],
    queryFn: () => unwrap(api.GET('/templates')),
  })
}

export function inputsOf(template: Template): InputSpec[] {
  return (template.inputs ?? []) as unknown as InputSpec[]
}

// The values of the inputs of a template. A secret that Polipan generates is not asked for.
export function initialValues(template: Template): Record<string, string> {
  const values: Record<string, string> = {}
  for (const i of inputsOf(template)) {
    values[i.name] = i.default !== undefined && i.default !== null ? String(i.default) : ''
  }
  return values
}

export function inputProblems(template: Template, values: Record<string, string>) {
  const out: Record<string, string> = {}
  for (const i of inputsOf(template)) {
    const v = (values[i.name] ?? '').trim()
    const optional = i.type === 'secret' && i.generate
    if (i.required && v === '' && !optional) out[i.name] = t('template.input_required')
    else if (v !== '' && i.type === 'number' && Number.isNaN(Number(v)))
      out[i.name] = t('template.input_number')
    else if (
      v !== '' &&
      i.type === 'domain' &&
      !/^(?=.{1,253}$)([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$/i.test(v)
    )
      out[i.name] = t('domains.host_invalid')
  }
  return out
}

// The form of the inputs of a template (REQ-TPL-001). Secret inputs are write-only.
export function TemplateInputs({
  template,
  values,
  onChange,
  errors,
}: {
  template: Template
  values: Record<string, string>
  onChange: (name: string, value: string) => void
  errors: Record<string, string>
}) {
  const inputs = inputsOf(template)
  if (inputs.length === 0) return <p className="text-muted">{t('template.no_inputs')}</p>
  return (
    <div className="space-y-4">
      {inputs.map((i) => {
        const label = i.label || i.name
        const common = { label, required: i.required, error: errors[i.name] ?? null }
        if (i.type === 'secret') {
          return (
            <div key={i.name} className="space-y-1">
              <SecretField
                {...common}
                value={values[i.name] ?? ''}
                onValue={(v) => onChange(i.name, v)}
              />
              {i.generate && <p className="text-sm text-muted">{t('template.secret_generated')}</p>}
            </div>
          )
        }
        if (i.type === 'bool') {
          return (
            <CheckboxField
              key={i.name}
              label={label}
              checked={values[i.name] === 'true'}
              onChange={(e) => onChange(i.name, e.target.checked ? 'true' : 'false')}
            />
          )
        }
        return (
          <TextField
            key={i.name}
            {...common}
            type={i.type === 'number' ? 'number' : 'text'}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            hint={i.type === 'domain' ? t('template.domain_hint') : undefined}
            value={values[i.name] ?? ''}
            onChange={(e) => onChange(i.name, e.target.value)}
          />
        )
      })}
    </div>
  )
}

export function instantiateBody(
  template: Template,
  environmentId: string,
  values: Record<string, string>,
) {
  const inputs: Record<string, unknown> = {}
  for (const i of inputsOf(template)) {
    const v = values[i.name] ?? ''
    if (v.trim() === '') continue
    inputs[i.name] =
      i.type === 'number'
        ? Number(v)
        : i.type === 'bool'
          ? v === 'true'
          : i.type === 'secret'
            ? v
            : v.trim()
  }
  return { environment_id: environmentId, inputs }
}

// The services that a template makes, as a short list for the review.
export function TemplateServices({ template }: { template: Template }) {
  return (
    <ul className="list-disc space-y-1 pl-5 text-sm">
      {(template.services ?? []).map((s) => {
        const svc = s as {
          name?: string
          type?: string
          db?: { engine?: string }
          source?: { image?: { name?: string } }
        }
        return (
          <li key={svc.name}>
            <span className="font-medium">{svc.name}</span>{' '}
            <span className="text-muted">
              {svc.type === 'database'
                ? t('template.database', { engine: svc.db?.engine ?? '' })
                : svc.source?.image?.name}
            </span>
          </li>
        )
      })}
    </ul>
  )
}

function useInstantiate(onDone: (services: components['schemas']['Service'][]) => void) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ slug, body }: { slug: string; body: ReturnType<typeof instantiateBody> }) =>
      unwrap(
        api.POST('/templates/{slug}/instantiate', {
          params: { path: { slug }, header: { 'Idempotency-Key': crypto.randomUUID() } },
          body,
        }),
      ),
    onSuccess: async (services) => {
      await queryClient.invalidateQueries({ queryKey: ['services'] })
      onDone(services)
    },
  })
}

function InstantiateDialog({ template, onClose }: { template: Template; onClose: () => void }) {
  const navigate = useNavigate()
  const { org } = useParams({ from: '/o/$org/catalog' })
  const projects = useProjects()
  const [projectId, setProjectId] = useState('')
  const environments = useEnvironments(projectId || undefined)
  const [envId, setEnvId] = useState('')
  const [values, setValues] = useState(() => initialValues(template))
  const [submitted, setSubmitted] = useState(false)
  const errors = submitted ? inputProblems(template, values) : {}
  const project = projects.data?.find((p) => p.id === projectId)
  const environment = environments.data?.find((e) => e.id === envId)
  const mutation = useInstantiate((services) => {
    setValues({}) // secrets are write-only: forget them
    const first = services[0]
    if (project?.slug && environment?.slug) {
      void navigate({
        to: '/o/$org/p/$project/e/$env',
        params: { org, project: project.slug, env: environment.slug },
      })
    } else if (first) onClose()
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (!envId || Object.keys(inputProblems(template, values)).length > 0) return
    mutation.mutate({ slug: template.slug ?? '', body: instantiateBody(template, envId, values) })
  }
  const conflict = mutation.error instanceof ApiError && mutation.error.status === 409

  return (
    <Modal title={t('template.use_title', { name: template.name ?? '' })} onClose={onClose}>
      <form onSubmit={submit} noValidate className="space-y-4">
        <p className="text-sm text-muted">{template.description}</p>
        <TemplateServices template={template} />
        <SelectField
          label={t('template.project')}
          required
          value={projectId}
          onChange={(e) => {
            setProjectId(e.target.value)
            setEnvId('')
          }}
        >
          <option value="">{t('template.pick_project')}</option>
          {(projects.data ?? []).map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </SelectField>
        <SelectField
          label={t('template.environment')}
          required
          value={envId}
          error={submitted && !envId ? t('template.environment_required') : null}
          onChange={(e) => setEnvId(e.target.value)}
        >
          <option value="">{t('template.pick_environment')}</option>
          {(environments.data ?? []).map((e) => (
            <option key={e.id} value={e.id}>
              {e.name}
              {e.protected ? ` (${t('env.protected')})` : ''}
            </option>
          ))}
        </SelectField>
        <TemplateInputs
          template={template}
          values={values}
          errors={errors}
          onChange={(name, v) => setValues((s) => ({ ...s, [name]: v }))}
        />
        {mutation.error && (
          <ErrorBanner
            error={mutation.error}
            title={conflict ? t('template.conflict') : t('template.create_failed')}
          />
        )}
        <p className="text-sm text-muted">{t('template.all_or_nothing')}</p>
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type="submit" variant="primary" loading={mutation.isPending}>
            {t('template.create')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

function CustomTemplateDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient()
  const [text, setText] = useState('')
  const create = useMutation({
    mutationFn: () => postYaml<Template>('/templates', text),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['templates'] })
      onClose()
    },
  })
  return (
    <Modal title={t('template.custom_title')} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (text.trim()) create.mutate()
        }}
      >
        <TextAreaField
          label={t('template.custom_manifest')}
          hint={t('template.custom_hint')}
          rows={14}
          spellCheck={false}
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
        {create.error && <ErrorBanner error={create.error} title={t('template.custom_failed')} />}
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type="submit" variant="primary" loading={create.isPending}>
            {t('template.custom_add')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

// UI-21: the catalog of templates.
export function CatalogPage() {
  const { org } = useParams({ from: '/o/$org/catalog' })
  const me = useMe()
  const templates = useTemplates()
  const [query, setQuery] = useState('')
  const deferred = useDeferredValue(query).trim().toLowerCase()
  const [using, setUsing] = useState<Template | null>(null)
  const [adding, setAdding] = useState(false)
  const canCreate = accessFor(me.data?.org_role, 'service:create') !== 'hidden'
  const canManage = accessFor(me.data?.org_role, 'template:manage') === 'allow'

  const list = (templates.data ?? []).filter(
    (x) =>
      deferred === '' || `${x.name} ${x.slug} ${x.description}`.toLowerCase().includes(deferred),
  )
  let body
  if (templates.isPending) body = <Skeleton />
  else if (templates.isError)
    body = <ErrorBanner error={templates.error} onRetry={() => void templates.refetch()} />
  else if (list.length === 0)
    body = (
      <EmptyState
        title={t('template.empty_title')}
        text={t('template.empty_text')}
        action={
          <Link to="/o/$org/projects" params={{ org }} className="text-primary underline">
            {t('template.import_compose')}
          </Link>
        }
      />
    )
  else
    body = (
      <ul className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-5">
        {list.map((x) => (
          <li key={x.slug}>
            <Card className="flex h-full flex-col gap-3">
              <div className="flex items-start justify-between gap-2">
                <h2 className="break-words text-lg font-semibold">{x.name}</h2>
                <span className="shrink-0 text-xs text-muted">
                  {x.builtin ? t('template.builtin') : t('template.custom')} · v{x.version}
                </span>
              </div>
              <p className="flex-1 text-sm text-muted">{x.description}</p>
              <TemplateServices template={x} />
              {canCreate && (
                <div>
                  <Button variant="primary" onClick={() => setUsing(x)}>
                    {t('template.use')}
                  </Button>
                </div>
              )}
            </Card>
          </li>
        ))}
      </ul>
    )

  return (
    <section>
      <PageHeader
        title={t('template.title')}
        actions={
          canManage ? (
            <Button onClick={() => setAdding(true)}>{t('template.add_custom')}</Button>
          ) : undefined
        }
      />
      <div className="space-y-4">
        <Banner>{t('template.intro')}</Banner>
        <TextField
          label={t('template.search')}
          type="search"
          autoComplete="off"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        {body}
      </div>
      {using && <InstantiateDialog template={using} onClose={() => setUsing(null)} />}
      {adding && <CustomTemplateDialog onClose={() => setAdding(false)} />}
    </section>
  )
}
