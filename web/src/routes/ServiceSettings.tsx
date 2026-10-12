// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { ErrorBanner } from '@/components/ErrorBanner'
import { useUnsavedGuard } from '@/components/UnsavedGuard'
import {
  Banner,
  Button,
  CheckboxField,
  FieldGrid,
  Optional,
  Pane,
  Panel,
  SelectField,
  TextField,
} from '@/components/ui'
import { obj, str, wholeNumber } from '@/configForm'
import type { Config } from '@/configForm'
import { joinCommand, splitCommand } from '@/envText'
import { t } from '@/i18n'
import { isManaged } from '@/managed'
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
  strategy: string
  drain: string
  allowDowntime: boolean
  allowOverlap: boolean
  preDeploy: string
  postDeploy: string
}

function readForm(config: Config): Form {
  // The server keeps the Git source in config.git.
  const source = obj(config.git ?? config.source)
  const resources = obj(config.resources)
  const options = obj(config.strategy_options)
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
    strategy: str(config.strategy),
    drain: str(options.drain_seconds),
    allowDowntime: options.allow_downtime_on_low_capacity === true,
    allowOverlap: options.allow_overlap !== false,
    preDeploy: joinCommand(config.pre_deploy),
    postDeploy: joinCommand(config.post_deploy),
  }
}

function writeConfig(config: Config, f: Form, git: boolean): Config {
  const next: Config = { ...config }
  const set = (key: string, value: unknown) => {
    if (value === undefined || value === '') delete next[key]
    else next[key] = value
  }
  if (git) {
    delete next.source
    next.git = {
      ...obj(config.git ?? config.source),
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
  set('strategy', f.strategy || undefined)
  const options = {
    ...(f.drain ? { drain_seconds: Number(f.drain) } : {}),
    ...(f.allowDowntime ? { allow_downtime_on_low_capacity: true } : {}),
    ...(f.allowOverlap ? {} : { allow_overlap: false }),
  }
  set('strategy_options', Object.keys(options).length > 0 ? options : undefined)
  for (const [key, text] of [
    ['pre_deploy', f.preDeploy],
    ['post_deploy', f.postDeploy],
  ] as const) {
    const command = splitCommand(text)
    set(key, command.length > 0 ? command : undefined)
  }
  return next
}

// The section of each field that can have a wrong value.
const SECTION_OF_FIELD: Partial<Record<keyof Form, string>> = {
  repo: 'source',
  branch: 'source',
  image: 'source',
  port: 'run',
  replicas: 'run',
  cpu: 'resources',
  memory: 'resources',
  drain: 'deploy',
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
  if (f.drain !== '' && !wholeNumber(f.drain, 0)) e.drain = t('settings.drain_invalid')
  return e
}

// UI-06 settings (M2 part): source, build, port, command, replicas and resources.
// A change applies with the next deploy, except replicas (pending changes, REQ-ENV-003).
export function ServiceSettings({
  service,
  environmentProtected,
  onChanged,
  section,
  onSection,
}: {
  service: Service
  environmentProtected?: boolean
  onChanged: () => void
  // The open section of the settings page. A section with a wrong value opens by itself.
  section: string
  onSection: (id: string) => void
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
  const isManagedField = (field: string) => isManaged(service.git_managed_fields, field)
  const readOnly = access !== 'allow'
  const lock = (field: string) => readOnly || isManagedField(field)
  const set = (patch: Partial<Form>) => {
    setSaved(false)
    setForm((f) => ({ ...f, ...patch }))
  }
  const errors = submitted ? validate(form, git) : {}

  const save = useMutation({
    meta: { success: 'settings.saved' },
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
    const found = validate(form, git)
    const first = Object.keys(found)[0] as keyof Form | undefined
    if (!first) save.mutate()
    else onSection(SECTION_OF_FIELD[first] ?? 'source')
  }

  const managedHint = (field: string) => (isManagedField(field) ? t('settings.managed') : undefined)
  const conflict = save.error instanceof ApiError && save.error.code === 'managed_by_git'

  return (
    <form onSubmit={submit} noValidate className="space-y-6">
      {access === 'hidden' && <Banner>{t('settings.read_only')}</Banner>}
      {access === 'blocked' && <Banner variant="warning">{t('common.needs_admin')}</Banner>}
      {saved && <Banner>{t('settings.saved')}</Banner>}
      {save.error && (
        <ErrorBanner
          error={save.error}
          title={conflict ? t('variables.managed_by_git') : t('settings.save_failed')}
        />
      )}
      <div className="max-w-3xl">
        <Pane id="source" active={section === 'source'}>
          <Panel id="settings-source" title={t('settings.source')}>
            <FieldGrid>
              {git ? (
                <>
                  <div className="sm:col-span-2">
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
                  </div>
                  <TextField
                    label={t('wizard.branch')}
                    readOnly={lock('source')}
                    autoComplete="off"
                    spellCheck={false}
                    error={errors.branch}
                    value={form.branch}
                    onChange={(e) => set({ branch: e.target.value })}
                  />
                  <Optional
                    filled={form.builder !== 'railpack' || (form.path !== '/' && form.path !== '')}
                  >
                    <FieldGrid>
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
                    </FieldGrid>
                  </Optional>
                </>
              ) : (
                <div className="sm:col-span-2">
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
                </div>
              )}
            </FieldGrid>
          </Panel>
        </Pane>
        <Pane id="run" active={section === 'run'}>
          <Panel id="settings-run" title={t('settings.run')}>
            <FieldGrid>
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
              <Optional filled={form.port !== '' || form.command !== '' || Boolean(errors.port)}>
                <FieldGrid>
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
                </FieldGrid>
              </Optional>
            </FieldGrid>
          </Panel>
        </Pane>
        <Pane id="resources" active={section === 'resources'}>
          <Panel id="settings-resources" title={t('settings.resources')}>
            <FieldGrid>
              <Optional
                label={t('settings.limits')}
                filled={
                  form.cpu !== '' || form.memory !== '' || Boolean(errors.cpu || errors.memory)
                }
              >
                <FieldGrid>
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
                </FieldGrid>
              </Optional>
            </FieldGrid>
          </Panel>
        </Pane>
        <Pane id="deploy" active={section === 'deploy'}>
          <Panel id="settings-deploy" title={t('settings.deploy')}>
            <FieldGrid>
              <SelectField
                label={t('settings.strategy')}
                hint={managedHint('strategy') ?? t('settings.strategy_hint')}
                disabled={lock('strategy')}
                value={form.strategy}
                onChange={(e) => set({ strategy: e.target.value })}
              >
                <option value="">{t('settings.strategy_default')}</option>
                <option value="blue_green">{t('deploy.strategy_value.blue_green')}</option>
                <option value="recreate">{t('deploy.strategy_value.recreate')}</option>
              </SelectField>
              <Optional
                filled={
                  form.drain !== '' ||
                  form.allowDowntime ||
                  !form.allowOverlap ||
                  Boolean(errors.drain)
                }
              >
                <FieldGrid>
                  <TextField
                    label={t('settings.drain')}
                    hint={t('settings.drain_hint')}
                    placeholder="30"
                    readOnly={lock('strategy')}
                    type="number"
                    inputMode="numeric"
                    error={errors.drain}
                    value={form.drain}
                    onChange={(e) => set({ drain: e.target.value })}
                  />
                  <div className="space-y-2 sm:col-span-2">
                    <CheckboxField
                      label={t('settings.allow_downtime')}
                      hint={t('settings.allow_downtime_hint')}
                      disabled={lock('strategy')}
                      checked={form.allowDowntime}
                      onChange={(e) => set({ allowDowntime: e.target.checked })}
                    />
                    <CheckboxField
                      label={t('settings.allow_overlap')}
                      hint={t('settings.allow_overlap_hint')}
                      disabled={lock('strategy')}
                      checked={form.allowOverlap}
                      onChange={(e) => set({ allowOverlap: e.target.checked })}
                    />
                  </div>
                </FieldGrid>
              </Optional>
            </FieldGrid>
          </Panel>
        </Pane>
        <Pane id="hooks" active={section === 'hooks'}>
          <Panel id="settings-hooks" title={t('settings.hooks')}>
            <FieldGrid>
              <Optional filled={form.preDeploy.trim() !== '' || form.postDeploy.trim() !== ''}>
                <FieldGrid>
                  <div className="sm:col-span-2">
                    <TextField
                      label={t('settings.pre_deploy')}
                      hint={managedHint('pre_deploy') ?? t('settings.pre_deploy_hint')}
                      readOnly={lock('pre_deploy')}
                      autoComplete="off"
                      spellCheck={false}
                      value={form.preDeploy}
                      onChange={(e) => set({ preDeploy: e.target.value })}
                    />
                  </div>
                  {form.preDeploy.trim() !== '' && form.strategy !== 'recreate' && (
                    <div className="sm:col-span-2">
                      <Banner variant="warning">{t('settings.migration_warning')}</Banner>
                    </div>
                  )}
                  <div className="sm:col-span-2">
                    <TextField
                      label={t('settings.post_deploy')}
                      hint={managedHint('post_deploy') ?? t('settings.post_deploy_hint')}
                      readOnly={lock('post_deploy')}
                      autoComplete="off"
                      spellCheck={false}
                      value={form.postDeploy}
                      onChange={(e) => set({ postDeploy: e.target.value })}
                    />
                  </div>
                </FieldGrid>
              </Optional>
            </FieldGrid>
          </Panel>
        </Pane>
      </div>
      {access === 'allow' && (
        <div
          data-dirty={dirty || undefined}
          className="font-term sticky z-10 flex flex-wrap items-center justify-between gap-3 border border-border-strong bg-surface px-4 py-3 data-[dirty]:border-amber max-sm:bottom-[calc(4rem+env(safe-area-inset-bottom))] sm:bottom-9"
        >
          <span className={dirty ? 'text-amber' : 'text-muted'}>
            {dirty ? `▲ ${t('settings.unsaved')}` : t('settings.next_deploy')}
          </span>
          <span className="flex gap-3">
            <Button
              onClick={() => setForm(initial)}
              reason={dirty ? undefined : t('settings.no_changes')}
            >
              {t('settings.reset')}
            </Button>
            <Button type="submit" variant="primary" loading={save.isPending}>
              {t('common.save')}
            </Button>
          </span>
        </div>
      )}
      {guard.dialog}
    </form>
  )
}
