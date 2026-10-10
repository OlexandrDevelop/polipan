// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { ErrorBanner } from '@/components/ErrorBanner'
import { useUnsavedGuard } from '@/components/UnsavedGuard'
import { Banner, Button, SelectField, TextField } from '@/components/ui'
import { joinCommand, splitCommand } from '@/envText'
import { t } from '@/i18n'
import { accessFor } from '@/permissions'
import type { Service } from '@/scope'

const CPU_PATTERN = /^[0-9]+m?$|^[0-9]+\.[0-9]+$/
const MEMORY_PATTERN = /^[0-9]+(Ki|Mi|Gi|Ti)$/

interface Form {
  image: string
  repo: string
  branch: string
  builder: string
  dockerfile: string
  path: string
  port: string
  command: string
  replicas: string
  cpu: string
  memory: string
}

type Config = Record<string, unknown>

function str(v: unknown, fallback = ''): string {
  return typeof v === 'string' || typeof v === 'number' ? String(v) : fallback
}

function obj(v: unknown): Config {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Config) : {}
}

function readForm(config: Config): Form {
  const source = obj(config.source)
  const resources = obj(config.resources)
  return {
    image: str(config.image),
    repo: str(source.repo),
    branch: str(source.branch, 'main'),
    builder: str(source.builder, 'railpack'),
    dockerfile: str(source.dockerfile, 'Dockerfile'),
    path: str(source.path, '/'),
    port: str(config.port),
    command: joinCommand(config.command),
    replicas: str(config.replicas, '1'),
    cpu: str(resources.cpu),
    memory: str(resources.memory),
  }
}

function writeConfig(config: Config, f: Form, git: boolean): Config {
  const next: Config = { ...config }
  const set = (key: string, value: unknown) => {
    if (value === undefined || value === '') delete next[key]
    else next[key] = value
  }
  if (git) {
    next.source = {
      ...obj(config.source),
      type: 'git',
      repo: f.repo.trim(),
      branch: f.branch.trim(),
      builder: f.builder,
      path: f.path.trim() || '/',
      ...(f.builder === 'dockerfile' ? { dockerfile: f.dockerfile.trim() || 'Dockerfile' } : {}),
    }
  } else {
    set('image', f.image.trim())
  }
  set('port', f.port ? Number(f.port) : undefined)
  const command = splitCommand(f.command)
  set('command', command.length > 0 ? command : undefined)
  set('replicas', f.replicas ? Number(f.replicas) : undefined)
  const resources = { ...(f.cpu ? { cpu: f.cpu } : {}), ...(f.memory ? { memory: f.memory } : {}) }
  set('resources', Object.keys(resources).length > 0 ? resources : undefined)
  return next
}

function validate(f: Form, git: boolean) {
  const e: Partial<Record<keyof Form, string>> = {}
  if (git) {
    if (f.repo.trim() === '' || /\s/.test(f.repo.trim())) e.repo = t('wizard.repo_invalid')
    if (f.branch.trim() === '') e.branch = t('wizard.branch_invalid')
  } else if (f.image.trim() === '' || /\s/.test(f.image.trim())) {
    e.image = t('service.image_invalid')
  }
  if (f.port !== '') {
    const n = Number(f.port)
    if (!Number.isInteger(n) || n < 1 || n > 65535) e.port = t('service.port_invalid')
  }
  const r = Number(f.replicas)
  if (!Number.isInteger(r) || r < 0 || r > 100) e.replicas = t('settings.replicas_invalid')
  if (f.cpu !== '' && !CPU_PATTERN.test(f.cpu)) e.cpu = t('settings.cpu_invalid')
  if (f.memory !== '' && !MEMORY_PATTERN.test(f.memory)) e.memory = t('settings.memory_invalid')
  return e
}

// UI-06 settings (M2 part): source, build, port, command, replicas and resources.
// A change applies with the next deploy, except replicas (pending changes, REQ-ENV-003).
export function ServiceSettings({
  service,
  environmentProtected,
  onChanged,
}: {
  service: Service
  environmentProtected?: boolean
  onChanged: () => void
}) {
  const queryClient = useQueryClient()
  const me = useMe()
  const access = accessFor(me.data?.org_role, 'service:update', environmentProtected)
  const config = (service.config ?? {}) as Config
  const git = service.source_type === 'git'
  const initial = readForm(config)
  const [form, setForm] = useState<Form>(initial)
  const [submitted, setSubmitted] = useState(false)
  const [saved, setSaved] = useState(false)
  const dirty = JSON.stringify(form) !== JSON.stringify(initial)
  const guard = useUnsavedGuard(dirty && access === 'allow')
  const managed = new Set(service.git_managed_fields ?? [])
  const readOnly = access !== 'allow'
  const lock = (field: string) => readOnly || managed.has(field)
  const set = (patch: Partial<Form>) => {
    setSaved(false)
    setForm((f) => ({ ...f, ...patch }))
  }
  const errors = submitted ? validate(form, git) : {}

  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/services/{service_id}', {
          params: { path: { service_id: service.id ?? '' } },
          body: { config: writeConfig(config, form, git) },
        }),
      ),
    onSuccess: async (updated) => {
      setSaved(true)
      setSubmitted(false)
      setForm(readForm((updated.config ?? {}) as Config))
      onChanged()
      await queryClient.invalidateQueries({ queryKey: ['service', service.id] })
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (Object.keys(validate(form, git)).length === 0) save.mutate()
  }

  const managedHint = (field: string) => (managed.has(field) ? t('settings.managed') : undefined)
  const conflict = save.error instanceof ApiError && save.error.code === 'managed_by_git'

  return (
    <form onSubmit={submit} noValidate className="max-w-2xl space-y-6">
      {access === 'hidden' && <Banner>{t('settings.read_only')}</Banner>}
      {access === 'blocked' && <Banner variant="warning">{t('common.needs_admin')}</Banner>}
      {saved && <Banner>{t('settings.saved')}</Banner>}
      {save.error && (
        <ErrorBanner
          error={save.error}
          title={conflict ? t('variables.managed_by_git') : t('settings.save_failed')}
        />
      )}
      <fieldset className="space-y-4">
        <legend className="mb-2 text-lg font-semibold">{t('settings.source')}</legend>
        {git ? (
          <>
            <TextField
              label={t('wizard.repo_url')}
              readOnly={lock('source')}
              hint={managedHint('source')}
              autoComplete="off"
              spellCheck={false}
              error={errors.repo}
              value={form.repo}
              onChange={(e) => set({ repo: e.target.value })}
            />
            <TextField
              label={t('wizard.branch')}
              readOnly={lock('source')}
              autoComplete="off"
              spellCheck={false}
              error={errors.branch}
              value={form.branch}
              onChange={(e) => set({ branch: e.target.value })}
            />
          </>
        ) : (
          <TextField
            label={t('service.image')}
            hint={managedHint('image') ?? t('service.image_hint')}
            readOnly={lock('image')}
            autoComplete="off"
            spellCheck={false}
            error={errors.image}
            value={form.image}
            onChange={(e) => set({ image: e.target.value })}
          />
        )}
      </fieldset>
      {git && (
        <fieldset className="space-y-4">
          <legend className="mb-2 text-lg font-semibold">{t('settings.build')}</legend>
          <SelectField
            label={t('settings.builder')}
            hint={t('settings.builder_hint')}
            disabled={lock('source')}
            value={form.builder}
            onChange={(e) => set({ builder: e.target.value })}
          >
            <option value="railpack">{t('settings.builder.railpack')}</option>
            <option value="dockerfile">{t('settings.builder.dockerfile')}</option>
          </SelectField>
          {form.builder === 'dockerfile' && (
            <TextField
              label={t('settings.dockerfile')}
              readOnly={lock('source')}
              autoComplete="off"
              spellCheck={false}
              value={form.dockerfile}
              onChange={(e) => set({ dockerfile: e.target.value })}
            />
          )}
          <TextField
            label={t('settings.build_path')}
            hint={t('settings.build_path_hint')}
            readOnly={lock('source')}
            autoComplete="off"
            spellCheck={false}
            value={form.path}
            onChange={(e) => set({ path: e.target.value })}
          />
        </fieldset>
      )}
      <fieldset className="space-y-4">
        <legend className="mb-2 text-lg font-semibold">{t('settings.run')}</legend>
        <TextField
          label={t('service.port')}
          hint={managedHint('ports') ?? t('service.port_hint')}
          readOnly={lock('ports')}
          type="number"
          inputMode="numeric"
          error={errors.port}
          value={form.port}
          onChange={(e) => set({ port: e.target.value })}
        />
        <TextField
          label={t('settings.command')}
          hint={managedHint('command') ?? t('settings.command_hint')}
          readOnly={lock('command')}
          autoComplete="off"
          spellCheck={false}
          value={form.command}
          onChange={(e) => set({ command: e.target.value })}
        />
      </fieldset>
      <fieldset className="space-y-4">
        <legend className="mb-2 text-lg font-semibold">{t('settings.resources')}</legend>
        <TextField
          label={t('settings.replicas')}
          hint={managedHint('replicas') ?? t('settings.replicas_hint')}
          readOnly={lock('replicas')}
          type="number"
          inputMode="numeric"
          error={errors.replicas}
          value={form.replicas}
          onChange={(e) => set({ replicas: e.target.value })}
        />
        <TextField
          label={t('settings.cpu')}
          hint={managedHint('resources') ?? t('settings.cpu_hint')}
          readOnly={lock('resources')}
          autoComplete="off"
          error={errors.cpu}
          value={form.cpu}
          onChange={(e) => set({ cpu: e.target.value })}
        />
        <TextField
          label={t('settings.memory')}
          hint={managedHint('resources') ?? t('settings.memory_hint')}
          readOnly={lock('resources')}
          autoComplete="off"
          error={errors.memory}
          value={form.memory}
          onChange={(e) => set({ memory: e.target.value })}
        />
      </fieldset>
      {access === 'allow' && (
        <div className="flex flex-wrap items-center justify-end gap-3 border-t border-border bg-bg py-3 max-sm:sticky max-sm:bottom-[calc(4rem+env(safe-area-inset-bottom))]">
          <span className="text-sm text-muted">{t('settings.next_deploy')}</span>
          <Button
            onClick={() => setForm(initial)}
            reason={dirty ? undefined : t('settings.no_changes')}
          >
            {t('settings.reset')}
          </Button>
          <Button type="submit" variant="primary" loading={save.isPending}>
            {t('common.save')}
          </Button>
        </div>
      )}
      {guard.dialog}
    </form>
  )
}
