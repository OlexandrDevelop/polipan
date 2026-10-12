// SPDX-License-Identifier: Apache-2.0

import { isManaged } from '@/managed'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Time } from '@/components/Time'
import { useUnsavedGuard } from '@/components/UnsavedGuard'
import {
  Banner,
  Button,
  CheckboxField,
  FieldGrid,
  Optional,
  Pane,
  Panel,
  SectionNav,
  SelectField,
  StatusBadge,
  TextField,
} from '@/components/ui'
import { obj, str, wholeNumber } from '@/configForm'
import type { Config } from '@/configForm'
import { joinCommand, splitCommand } from '@/envText'
import { t } from '@/i18n'
import { accessFor } from '@/permissions'
import type { Service } from '@/scope'

// Defaults of the glossary. They show as placeholder text and as a "default" hint.
const DEFAULTS = {
  interval: '5',
  timeout: '3',
  healthy_threshold: '3',
  unhealthy_threshold: '3',
  start_period: '30',
  deploy_timeout: '300',
} as const

type NumberField = keyof typeof DEFAULTS

interface Form {
  type: string
  path: string
  port: string
  command: string
  restart: boolean
  interval: string
  timeout: string
  healthy_threshold: string
  unhealthy_threshold: string
  start_period: string
  deploy_timeout: string
}

const NUMBER_FIELDS = Object.keys(DEFAULTS) as NumberField[]

function readForm(config: Config): Form {
  const h = obj(config.health)
  return {
    type: str(h.type),
    path: str(h.path),
    port: str(h.port),
    command: joinCommand(h.command),
    restart: h.restart_on_unhealthy === true,
    interval: str(h.interval),
    timeout: str(h.timeout),
    healthy_threshold: str(h.healthy_threshold),
    unhealthy_threshold: str(h.unhealthy_threshold),
    start_period: str(h.start_period),
    deploy_timeout: str(h.deploy_timeout),
  }
}

function writeConfig(config: Config, f: Form): Config {
  const health: Config = {}
  if (f.type) health.type = f.type
  if (f.type === 'http' && f.path.trim()) health.path = f.path.trim()
  if (f.type !== 'command' && f.port) health.port = Number(f.port)
  if (f.type === 'command') health.command = splitCommand(f.command)
  for (const key of NUMBER_FIELDS) if (f[key]) health[key] = Number(f[key])
  if (f.restart) health.restart_on_unhealthy = true
  const next: Config = { ...config }
  if (Object.keys(health).length > 0) next.health = health
  else delete next.health
  return next
}

function validate(f: Form) {
  const e: Partial<Record<keyof Form, string>> = {}
  if (f.type === 'http' && f.path.trim() !== '' && !f.path.trim().startsWith('/')) {
    e.path = t('health.path_invalid')
  }
  if (f.port !== '' && (!wholeNumber(f.port) || Number(f.port) > 65535)) {
    e.port = t('service.port_invalid')
  }
  if (f.type === 'command' && splitCommand(f.command).length === 0) {
    e.command = t('health.command_required')
  }
  for (const key of NUMBER_FIELDS) {
    if (f[key] !== '' && !wholeNumber(f[key])) e[key] = t('health.number_invalid')
  }
  return e
}

interface TestResult {
  healthy?: boolean
  status?: number
  body_excerpt?: string
  duration_ms?: number
}

// UI-14: configure the health check and test it against a live container (REQ-HC-011).
export function ServiceHealth({
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
  const initial = readForm(config)
  const [section, setSection] = useState('check')
  const [form, setForm] = useState<Form>(initial)
  const [submitted, setSubmitted] = useState(false)
  const [saved, setSaved] = useState(false)
  const [testedAt, setTestedAt] = useState<string>()
  const dirty = JSON.stringify(form) !== JSON.stringify(initial)
  const guard = useUnsavedGuard(dirty && access === 'allow')
  const readOnly = access !== 'allow'
  const set = (patch: Partial<Form>) => {
    setSaved(false)
    setForm((f) => ({ ...f, ...patch }))
  }
  const errors = submitted ? validate(form) : {}
  const managed = isManaged(service.git_managed_fields, 'health')

  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/services/{service_id}', {
          params: { path: { service_id: service.id ?? '' } },
          body: { config: writeConfig(config, form) },
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

  const test = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/health-check:test', {
          params: { path: { service_id: service.id ?? '' } },
        }),
      ) as Promise<TestResult>,
    onSuccess: () => setTestedAt(new Date().toISOString()),
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    const found = Object.keys(validate(form))
    if (found.length === 0) save.mutate()
    // Open the section that has the wrong value.
    else
      setSection(
        found.some((k) => ['path', 'command', 'port', 'type'].includes(k)) ? 'check' : 'timing',
      )
  }

  const fieldHint = (key: NumberField) => t('health.default_value', { value: DEFAULTS[key] })
  const numberField = (key: NumberField, label: string) => (
    <TextField
      label={label}
      hint={fieldHint(key)}
      placeholder={DEFAULTS[key]}
      readOnly={readOnly || managed}
      type="number"
      inputMode="numeric"
      error={errors[key]}
      value={form[key]}
      onChange={(e) => set({ [key]: e.target.value })}
    />
  )
  const conflict = save.error instanceof ApiError && save.error.code === 'managed_by_git'
  const result = test.data

  return (
    <div className="grid items-start gap-6 lg:grid-cols-[13rem_minmax(0,1fr)] lg:gap-8">
      <SectionNav
        items={[
          { id: 'check', label: t('health.type') },
          { id: 'timing', label: t('health.timing') },
          { id: 'test', label: t('health.test_title') },
        ]}
        active={section}
        onChange={setSection}
        label={t('settings.sections')}
      />
      <div className="min-w-0 max-w-3xl space-y-6">
        <form onSubmit={submit} noValidate className="space-y-6">
          {access === 'hidden' && <Banner>{t('settings.read_only')}</Banner>}
          {access === 'blocked' && <Banner variant="warning">{t('common.needs_admin')}</Banner>}
          {managed && <Banner>{t('settings.managed')}</Banner>}
          {saved && <Banner>{t('settings.saved')}</Banner>}
          {save.error && (
            <ErrorBanner
              error={save.error}
              title={conflict ? t('variables.managed_by_git') : t('settings.save_failed')}
            />
          )}
          <Pane id="check" active={section === 'check'}>
            <Panel title={t('health.type')}>
              <FieldGrid>
                <div className="sm:col-span-2">
                  <SelectField
                    label={t('health.type')}
                    hint={t('health.type_hint')}
                    disabled={readOnly || managed}
                    value={form.type}
                    onChange={(e) => set({ type: e.target.value })}
                  >
                    <option value="">{t('health.type_default')}</option>
                    <option value="http">{t('health.type.http')}</option>
                    <option value="tcp">{t('health.type.tcp')}</option>
                    <option value="command">{t('health.type.command')}</option>
                  </SelectField>
                </div>
                {form.type === 'http' && (
                  <div className="sm:col-span-2">
                    <TextField
                      label={t('health.path')}
                      hint={t('health.path_hint')}
                      placeholder="/healthz"
                      readOnly={readOnly || managed}
                      autoComplete="off"
                      spellCheck={false}
                      error={errors.path}
                      value={form.path}
                      onChange={(e) => set({ path: e.target.value })}
                    />
                  </div>
                )}
                {form.type === 'command' ? (
                  <div className="sm:col-span-2">
                    <TextField
                      label={t('health.command')}
                      hint={t('health.command_hint')}
                      readOnly={readOnly || managed}
                      autoComplete="off"
                      spellCheck={false}
                      error={errors.command}
                      value={form.command}
                      onChange={(e) => set({ command: e.target.value })}
                    />
                  </div>
                ) : (
                  <Optional filled={form.port !== '' || form.restart || Boolean(errors.port)}>
                    <FieldGrid>
                      <TextField
                        label={t('service.port')}
                        hint={t('health.port_hint')}
                        readOnly={readOnly || managed}
                        type="number"
                        inputMode="numeric"
                        error={errors.port}
                        value={form.port}
                        onChange={(e) => set({ port: e.target.value })}
                      />
                      <div className="sm:col-span-2">
                        <CheckboxField
                          label={t('health.restart')}
                          hint={t('health.restart_hint')}
                          disabled={readOnly || managed}
                          checked={form.restart}
                          onChange={(e) => set({ restart: e.target.checked })}
                        />
                      </div>
                    </FieldGrid>
                  </Optional>
                )}
              </FieldGrid>
            </Panel>
          </Pane>
          <Pane id="timing" active={section === 'timing'}>
            <Panel title={t('health.timing')}>
              <FieldGrid>
                <Optional
                  label={t('settings.limits')}
                  filled={
                    NUMBER_FIELDS.some((k) => form[k] !== '') ||
                    NUMBER_FIELDS.some((k) => Boolean(errors[k]))
                  }
                >
                  <FieldGrid>
                    {numberField('interval', t('health.interval'))}
                    {numberField('timeout', t('health.timeout'))}
                    {numberField('healthy_threshold', t('health.healthy_threshold'))}
                    {numberField('unhealthy_threshold', t('health.unhealthy_threshold'))}
                    {numberField('start_period', t('health.start_period'))}
                    {numberField('deploy_timeout', t('health.deploy_timeout'))}
                  </FieldGrid>
                </Optional>
              </FieldGrid>
            </Panel>
          </Pane>
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
        <Pane id="test" active={section === 'test'}>
          <Panel title={t('health.test_title')} className="space-y-3">
            <p className="text-sm text-muted">{t('health.test_text')}</p>
            <Button loading={test.isPending} onClick={() => test.mutate()}>
              {t('health.test')}
            </Button>
            {test.error && <ErrorBanner error={test.error} title={t('health.test_failed')} />}
            {result && (
              <div
                className="space-y-2 rounded-md border border-border bg-surface p-4"
                role="status"
              >
                <StatusBadge
                  tone={result.healthy ? 'success' : 'danger'}
                  label={result.healthy ? t('health.result_healthy') : t('health.result_unhealthy')}
                />
                <p className="text-sm">
                  {result.status ? t('health.result_status', { status: result.status }) : ''}{' '}
                  {t('health.result_time', { ms: result.duration_ms ?? 0 })}
                </p>
                <p className="text-sm text-muted">
                  {t('health.tested_at')}: <Time value={testedAt} />
                </p>
                {result.body_excerpt && (
                  <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-sm border border-border bg-bg p-2 font-mono text-xs">
                    {result.body_excerpt}
                  </pre>
                )}
              </div>
            )}
          </Panel>
        </Pane>
      </div>
    </div>
  )
}
