// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog, Modal, TypedConfirmDialog } from '@/components/Modal'
import { Time } from '@/components/Time'
import {
  Banner,
  Button,
  CheckboxField,
  Optional,
  EmptyState,
  SecretField,
  SelectField,
  LockIcon,
  Skeleton,
  TextAreaField,
  TextField,
} from '@/components/ui'
import { looksSecret, nameProblem, parseEnvText } from '@/envText'
import type { EnvItem } from '@/envText'
import { t } from '@/i18n'
import type { MessageKey } from '@/i18n'
import { accessFor } from '@/permissions'
import type { Access } from '@/permissions'
import { useServices } from '@/scope'
import type { Environment, Project, Service } from '@/scope'

type Variable = components['schemas']['Variable']
type Secret = components['schemas']['Secret']
type ScopeName = 'project' | 'environment' | 'service'

interface Ctx {
  projectId: string
  environmentId: string
  serviceId: string
}

type Row =
  | { kind: 'variable'; id: string; name: string; scope: ScopeName; item: Variable }
  | { kind: 'secret'; id: string; name: string; scope: ScopeName; item: Secret }

function scopeOf(row: { environment_id?: string; service_id?: string }): ScopeName {
  if (row.service_id) return 'service'
  if (row.environment_id) return 'environment'
  return 'project'
}

const rank: Record<ScopeName, number> = { project: 1, environment: 2, service: 3 }

// Keeps the rows that apply to this service: project, this environment, this service.
function applies(ctx: Ctx, row: { environment_id?: string; service_id?: string }): boolean {
  if (row.service_id) return row.service_id === ctx.serviceId
  if (row.environment_id) return row.environment_id === ctx.environmentId
  return true
}

function scopeBody(ctx: Ctx, scope: ScopeName) {
  if (scope === 'service') return { environment_id: ctx.environmentId, service_id: ctx.serviceId }
  if (scope === 'environment') return { environment_id: ctx.environmentId }
  return {}
}

// The list call has scope filters. It reads the three scopes and merges them, so it works
// whether a filter means "only this scope" or "this scope and above".
function scopeQueries(ctx: Ctx) {
  return [
    { key: 'p', query: {} },
    { key: 'e', query: { environment_id: ctx.environmentId } },
    { key: 's', query: { service_id: ctx.serviceId } },
  ]
}

function merge<T extends { id?: string; environment_id?: string; service_id?: string }>(
  ctx: Ctx,
  lists: (T[] | undefined)[],
): T[] {
  const byId = new Map<string, T>()
  for (const list of lists) for (const row of list ?? []) if (row.id) byId.set(row.id, row)
  return [...byId.values()].filter((row) => applies(ctx, row))
}

function useVariables(ctx: Ctx) {
  const results = useQueries({
    queries: scopeQueries(ctx).map((s) => ({
      queryKey: ['variables', ctx.projectId, s.key, s.query],
      queryFn: () =>
        unwrap(
          api.GET('/projects/{project_id}/variables', {
            params: { path: { project_id: ctx.projectId }, query: s.query },
          }),
        ),
    })),
  })
  return {
    data: merge(
      ctx,
      results.map((r) => r.data),
    ),
    pending: results.some((r) => r.isPending),
    error: results.find((r) => r.isError)?.error,
    retry: () => results.forEach((r) => void r.refetch()),
  }
}

function useSecrets(ctx: Ctx, enabled: boolean) {
  const results = useQueries({
    queries: scopeQueries(ctx).map((s) => ({
      queryKey: ['secrets', ctx.projectId, s.key, s.query],
      enabled,
      queryFn: () =>
        unwrap(
          api.GET('/projects/{project_id}/secrets', {
            params: { path: { project_id: ctx.projectId }, query: s.query },
          }),
        ),
    })),
  })
  return {
    data: merge(
      ctx,
      results.map((r) => r.data),
    ),
    pending: enabled && results.some((r) => r.isPending),
    error: results.find((r) => r.isError)?.error,
    retry: () => results.forEach((r) => void r.refetch()),
  }
}

// The text of a known error code; the server message stays as the detail.
function errorTitle(error: unknown, fallback: MessageKey): string {
  if (error instanceof ApiError) {
    if (error.code === 'name_conflict') return t('variables.name_conflict')
    if (error.code === 'reference_cycle') return t('variables.reference_cycle')
    if (error.code === 'unresolved_reference') return t('variables.unresolved_reference')
    if (error.code === 'managed_by_git') return t('variables.managed_by_git')
  }
  return t(fallback)
}

function useInvalidate(ctx: Ctx, onChanged: () => void) {
  const queryClient = useQueryClient()
  return async () => {
    onChanged()
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['variables', ctx.projectId] }),
      queryClient.invalidateQueries({ queryKey: ['secrets', ctx.projectId] }),
      queryClient.invalidateQueries({ queryKey: ['service', ctx.serviceId] }),
    ])
  }
}

interface EditTarget {
  kind: 'variable' | 'secret'
  row?: Row
}

// Create or change one variable or secret. A secret value is write-only (REQ-UI-005).
function ItemDialog({
  ctx,
  target,
  onClose,
  onChanged,
}: {
  ctx: Ctx
  target: EditTarget
  onClose: () => void
  onChanged: () => void
}) {
  const existing = target.row
  const isSecret = target.kind === 'secret'
  const [name, setName] = useState(existing?.name ?? '')
  const [value, setValue] = useState(
    existing?.kind === 'variable' ? (existing.item.value ?? '') : '',
  )
  const [scope, setScope] = useState<ScopeName>(existing?.scope ?? 'service')
  const [buildTime, setBuildTime] = useState(Boolean(existing?.item.build_time))
  const [preview, setPreview] = useState(Boolean(existing?.item.preview))
  const [submitted, setSubmitted] = useState(false)
  const invalidate = useInvalidate(ctx, onChanged)

  const problem = existing ? null : nameProblem(name)
  const nameError =
    problem === 'invalid'
      ? t('variables.name_invalid')
      : problem === 'reserved'
        ? t('variables.name_reserved')
        : null
  const valueError = isSecret && value === '' ? t('variables.value_required') : null

  const save = useMutation({
    mutationFn: async () => {
      const flags = { build_time: buildTime, preview }
      if (!isSecret) {
        const body = { name, value, ...scopeBody(ctx, scope), ...flags }
        if (existing) {
          return unwrap(
            api.PATCH('/variables/{variable_id}', {
              params: { path: { variable_id: existing.id } },
              body,
            }),
          )
        }
        return unwrap(
          api.POST('/projects/{project_id}/variables', {
            params: { path: { project_id: ctx.projectId } },
            body,
          }),
        )
      }
      if (existing) {
        return unwrap(
          api.PUT('/secrets/{secret_id}', {
            params: { path: { secret_id: existing.id } },
            body: { value, ...flags },
          }),
        )
      }
      return unwrap(
        api.POST('/projects/{project_id}/secrets', {
          params: { path: { project_id: ctx.projectId } },
          body: { name, value, ...scopeBody(ctx, scope), ...flags },
        }),
      )
    },
    onSuccess: async () => {
      // The secret value leaves the page at once.
      setValue('')
      await invalidate()
      onClose()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (!nameError && !valueError) save.mutate()
  }

  const title = existing
    ? isSecret
      ? t('secret.set_title', { name })
      : t('variables.edit_title', { name })
    : isSecret
      ? t('secret.add_title')
      : t('variables.add_title')

  return (
    <Modal title={title} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4" noValidate>
        {save.error && (
          <ErrorBanner error={save.error} title={errorTitle(save.error, 'variables.save_failed')} />
        )}
        <TextField
          label={t('variables.name')}
          hint={existing ? undefined : t('variables.name_hint')}
          required
          readOnly={Boolean(existing)}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          className="font-mono"
          error={submitted ? nameError : null}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        {isSecret ? (
          <SecretField
            label={existing ? t('secret.new_value') : t('variables.value')}
            required
            version={existing?.kind === 'secret' ? existing.item.current_version : undefined}
            error={submitted ? valueError : null}
            value={value}
            onValue={setValue}
          />
        ) : (
          <TextAreaField
            label={t('variables.value')}
            hint={t('variables.value_hint')}
            spellCheck={false}
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
        )}
        <Optional filled={Boolean(existing) || buildTime || preview || scope !== 'service'}>
          <div className="space-y-4">
            {!existing && (
              <SelectField
                label={t('variables.scope')}
                hint={t('variables.scope_hint')}
                value={scope}
                onChange={(e) => setScope(e.target.value as ScopeName)}
              >
                <option value="service">{t('variables.scope.service')}</option>
                <option value="environment">{t('variables.scope.environment')}</option>
                <option value="project">{t('variables.scope.project')}</option>
              </SelectField>
            )}
            <CheckboxField
              label={t('variables.build_time')}
              hint={t('variables.build_time_hint')}
              checked={buildTime}
              onChange={(e) => setBuildTime(e.target.checked)}
            />
            <CheckboxField
              label={t('variables.preview')}
              hint={t('variables.preview_hint')}
              checked={preview}
              onChange={(e) => setPreview(e.target.checked)}
            />
          </div>
        </Optional>
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type="submit" variant="primary" loading={save.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

interface ImportItem extends EnvItem {
  secret: boolean
}

// Bulk import of .env text or a JSON object (REQ-ENV-006). Not offered on a phone.
function ImportDialog({
  ctx,
  onClose,
  onChanged,
}: {
  ctx: Ctx
  onClose: () => void
  onChanged: () => void
}) {
  const [text, setText] = useState('')
  const [items, setItems] = useState<ImportItem[] | null>(null)
  const [scope, setScope] = useState<ScopeName>('service')
  const [parseError, setParseError] = useState<string | null>(null)
  const invalidate = useInvalidate(ctx, onChanged)
  const bad = (items ?? []).filter((i) => nameProblem(i.name) !== null)

  const run = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/projects/{project_id}/variables:bulk', {
          params: { path: { project_id: ctx.projectId } },
          body: {
            ...scopeBody(ctx, scope),
            items: (items ?? []).map((i) => ({ name: i.name, value: i.value, secret: i.secret })),
          },
        }),
      ),
    onSuccess: async () => {
      setText('')
      setItems(null)
      await invalidate()
      onClose()
    },
  })

  function read() {
    const parsed = parseEnvText(text)
    if (!parsed || parsed.length === 0) {
      setParseError(t('import.parse_failed'))
      setItems(null)
      return
    }
    setParseError(null)
    // The text holds values that may be secret: it is not kept after reading.
    setText('')
    setItems(parsed.map((p) => ({ ...p, secret: looksSecret(p.name) })))
  }

  return (
    <Modal title={t('import.title')} onClose={onClose}>
      <div className="space-y-4">
        {items === null ? (
          <>
            <TextAreaField
              label={t('import.text')}
              hint={t('import.text_hint')}
              spellCheck={false}
              autoComplete="off"
              rows={8}
              error={parseError}
              value={text}
              onChange={(e) => setText(e.target.value)}
            />
            <div className="flex flex-wrap justify-end gap-2">
              <Button onClick={onClose}>{t('common.cancel')}</Button>
              <Button variant="primary" onClick={read}>
                {t('import.read')}
              </Button>
            </div>
          </>
        ) : (
          <>
            {run.error && (
              <ErrorBanner error={run.error} title={errorTitle(run.error, 'import.failed')} />
            )}
            {bad.length > 0 && (
              <Banner variant="danger" title={t('import.bad_names')}>
                {bad.map((b) => b.name).join(', ')}
              </Banner>
            )}
            <fieldset className="space-y-2">
              <legend className="text-sm font-medium">
                {t('import.found', { count: items.length })}
              </legend>
              <ul className="max-h-72 space-y-1 overflow-auto">
                {items.map((item, i) => (
                  <li key={`${item.name}-${i}`}>
                    <CheckboxField
                      label={t('import.mark_secret', { name: item.name })}
                      checked={item.secret}
                      onChange={(e) =>
                        setItems(
                          items.map((it, j) =>
                            j === i ? { ...it, secret: e.target.checked } : it,
                          ),
                        )
                      }
                    />
                  </li>
                ))}
              </ul>
            </fieldset>
            <SelectField
              label={t('variables.scope')}
              value={scope}
              onChange={(e) => setScope(e.target.value as ScopeName)}
            >
              <option value="service">{t('variables.scope.service')}</option>
              <option value="environment">{t('variables.scope.environment')}</option>
              <option value="project">{t('variables.scope.project')}</option>
            </SelectField>
            <div className="flex flex-wrap justify-end gap-2">
              <Button onClick={() => setItems(null)}>{t('wizard.back')}</Button>
              <Button
                variant="primary"
                loading={run.isPending}
                reason={bad.length > 0 ? t('import.bad_names') : undefined}
                onClick={() => run.mutate()}
              >
                {t('import.run', { count: items.length })}
              </Button>
            </div>
          </>
        )}
      </div>
    </Modal>
  )
}

// Where a name was used: the last releases of the services in this environment (REQ-ENV-008).
// Releases keep plain values and secret version numbers only, never secret values.
function WhereUsedDialog({
  row,
  environmentId,
  onClose,
}: {
  row: Row
  environmentId: string
  onClose: () => void
}) {
  const services = useServices(environmentId)
  const list = services.data ?? []
  const releases = useQueries({
    queries: list.map((s) => ({
      queryKey: ['releases', s.id, 10],
      queryFn: () =>
        unwrap(
          api.GET('/services/{service_id}/releases', {
            params: { path: { service_id: s.id ?? '' }, query: { limit: 10 } },
          }),
        ),
    })),
  })
  const uses = list.flatMap((s, i) =>
    (releases[i]?.data?.items ?? [])
      .filter((r) =>
        row.kind === 'secret'
          ? r.secret_versions?.[row.name] !== undefined
          : r.variables?.[row.name] !== undefined,
      )
      .map((r) => ({
        key: `${s.id}-${r.id}`,
        service: s.name ?? '',
        release: r.number,
        value:
          row.kind === 'secret'
            ? t('secret.version', { version: r.secret_versions?.[row.name] ?? 0 })
            : (r.variables?.[row.name] ?? ''),
      })),
  )
  const pending = services.isPending || releases.some((r) => r.isPending)
  const failed = services.error ?? releases.find((r) => r.isError)?.error
  return (
    <Modal title={t('where_used.title', { name: row.name })} onClose={onClose}>
      <div className="space-y-3">
        <p className="text-sm text-muted">{t('where_used.hint')}</p>
        {failed && <ErrorBanner error={failed} />}
        {pending ? (
          <Skeleton />
        ) : uses.length === 0 ? (
          <p>{t('where_used.none')}</p>
        ) : (
          <DataList
            caption={t('where_used.title', { name: row.name })}
            rows={uses}
            rowKey={(u) => u.key}
            columns={[
              { header: t('where_used.service'), cell: (u) => u.service, primary: true },
              { header: t('where_used.release'), cell: (u) => `#${u.release ?? '-'}` },
              {
                header: t('variables.value'),
                cell: (u) => <span className="break-all font-mono">{u.value}</span>,
              },
            ]}
          />
        )}
      </div>
    </Modal>
  )
}

// UI-49: the versions of one secret. Never a value.
function VersionsPanel({
  row,
  ctx,
  writeAccess,
  destroyAccess,
  onClose,
  onChanged,
}: {
  row: Extract<Row, { kind: 'secret' }>
  ctx: Ctx
  writeAccess: Access
  destroyAccess: Access
  onClose: () => void
  onChanged: () => void
}) {
  const queryClient = useQueryClient()
  const invalidate = useInvalidate(ctx, onChanged)
  const [value, setValue] = useState('')
  const [destroying, setDestroying] = useState<number | null>(null)
  const versions = useQuery({
    queryKey: ['secret-versions', row.id],
    queryFn: () =>
      unwrap(api.GET('/secrets/{secret_id}/versions', { params: { path: { secret_id: row.id } } })),
  })
  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: ['secret-versions', row.id] })
    await invalidate()
  }
  const setNew = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/secrets/{secret_id}', {
          params: { path: { secret_id: row.id } },
          body: { value },
        }),
      ),
    onSuccess: async () => {
      setValue('')
      await refresh()
    },
  })
  const restore = useMutation({
    mutationFn: (version: number) =>
      unwrap(
        api.POST('/secrets/{secret_id}/versions/{version}/restore', {
          params: { path: { secret_id: row.id, version } },
        }),
      ),
    onSuccess: refresh,
  })
  const destroy = useMutation({
    mutationFn: (version: number) =>
      unwrap(
        api.DELETE('/secrets/{secret_id}/versions/{version}', {
          params: { path: { secret_id: row.id, version } },
        }),
      ),
    onSuccess: async () => {
      setDestroying(null)
      await refresh()
    },
  })
  const current = row.item.current_version
  const blockedReason = writeAccess === 'blocked' ? t('common.needs_admin') : undefined

  return (
    <Modal title={t('versions.title', { name: row.name })} onClose={onClose}>
      <div className="space-y-4">
        <p className="text-sm text-muted">{t('versions.hint')}</p>
        {writeAccess !== 'hidden' && (
          <form
            className="hidden space-y-2 sm:block"
            onSubmit={(e) => {
              e.preventDefault()
              if (value !== '') setNew.mutate()
            }}
          >
            <SecretField
              label={t('secret.new_value')}
              version={current}
              value={value}
              onValue={setValue}
            />
            {setNew.error && (
              <ErrorBanner error={setNew.error} title={t('variables.save_failed')} />
            )}
            <Button
              type="submit"
              loading={setNew.isPending}
              reason={blockedReason ?? (value === '' ? t('secret.enter_value') : undefined)}
            >
              {t('secret.save_version')}
            </Button>
          </form>
        )}
        {(restore.error || destroy.error) && (
          <ErrorBanner error={restore.error ?? destroy.error} title={t('versions.action_failed')} />
        )}
        {versions.isPending && <Skeleton />}
        {versions.isError && (
          <ErrorBanner error={versions.error} onRetry={() => void versions.refetch()} />
        )}
        {versions.data && (
          <DataList
            caption={t('versions.title', { name: row.name })}
            rows={[...versions.data].sort((a, b) => (b.version ?? 0) - (a.version ?? 0))}
            rowKey={(v) => String(v.version)}
            columns={[
              {
                header: t('versions.version'),
                primary: true,
                cell: (v) => (
                  <span>
                    {t('secret.version', { version: v.version ?? 0 })}
                    {v.version === current && (
                      <span className="text-muted"> · {t('versions.current')}</span>
                    )}
                  </span>
                ),
              },
              { header: t('versions.created'), cell: (v) => <Time value={v.created_at} /> },
              { header: t('versions.by'), cell: (v) => v.created_by ?? '-' },
              {
                header: t('versions.used_by'),
                cell: (v) =>
                  v.destroyed
                    ? t('versions.destroyed')
                    : t('versions.used_count', { count: v.used_by_releases ?? 0 }),
              },
            ]}
            actions={(v) => {
              const n = v.version ?? 0
              if (v.destroyed || n === current) return null
              const used = (v.used_by_releases ?? 0) > 0
              return (
                <div className="flex flex-wrap gap-2">
                  {writeAccess !== 'hidden' && (
                    <Button
                      loading={restore.isPending && restore.variables === n}
                      reason={blockedReason}
                      onClick={() => restore.mutate(n)}
                    >
                      {t('versions.restore')}
                    </Button>
                  )}
                  {destroyAccess !== 'hidden' && (
                    <Button
                      variant="destructive"
                      className="max-sm:hidden"
                      reason={
                        used
                          ? t('versions.used_reason', { count: v.used_by_releases ?? 0 })
                          : undefined
                      }
                      onClick={() => setDestroying(n)}
                    >
                      {t('versions.destroy')}
                    </Button>
                  )}
                </div>
              )
            }}
          />
        )}
      </div>
      {destroying !== null && (
        <TypedConfirmDialog
          title={t('versions.destroy_title', { name: row.name, version: destroying })}
          text={t('versions.destroy_text')}
          confirmLabel={t('versions.destroy')}
          typed={row.name}
          busy={destroy.isPending}
          onConfirm={() => destroy.mutate(destroying)}
          onClose={() => setDestroying(null)}
        />
      )}
    </Modal>
  )
}

function Chip({ children }: { children: ReactNode }) {
  return (
    <span className="inline-flex items-center rounded-full border border-border-strong px-2 py-0.5 text-xs">
      {children}
    </span>
  )
}

// UI-11: variables and secrets of one service, with the inherited values of the project and
// the environment. Secret values are never shown (REQ-UI-005, AC-ENV-02).
export function ServiceVariables({
  project,
  environment,
  service,
  onChanged,
}: {
  project: Project
  environment: Environment
  service: Service
  onChanged: () => void
}) {
  const ctx: Ctx = {
    projectId: project.id ?? '',
    environmentId: environment.id ?? '',
    serviceId: service.id ?? '',
  }
  const me = useMe()
  const role = me.data?.org_role
  const varWrite = accessFor(role, 'variable:write', environment.protected)
  const secretList = accessFor(role, 'secret:list', environment.protected)
  const secretWrite = accessFor(role, 'secret:write', environment.protected)
  const secretDestroy = accessFor(role, 'secret:destroy', environment.protected)
  const variables = useVariables(ctx)
  const secrets = useSecrets(ctx, secretList !== 'hidden')
  const invalidate = useInvalidate(ctx, onChanged)

  const [editing, setEditing] = useState<EditTarget | null>(null)
  const [importing, setImporting] = useState(false)
  const [deleting, setDeleting] = useState<Row | null>(null)
  const [whereUsed, setWhereUsed] = useState<Row | null>(null)
  const [versionsOf, setVersionsOf] = useState<Extract<Row, { kind: 'secret' }> | null>(null)

  const remove = useMutation({
    mutationFn: (row: Row) =>
      row.kind === 'variable'
        ? unwrap(
            api.DELETE('/variables/{variable_id}', { params: { path: { variable_id: row.id } } }),
          )
        : unwrap(api.DELETE('/secrets/{secret_id}', { params: { path: { secret_id: row.id } } })),
    onSuccess: async () => {
      setDeleting(null)
      await invalidate()
    },
  })

  const rows: Row[] = [
    ...variables.data.map((v): Row => ({
      kind: 'variable',
      id: v.id ?? '',
      name: v.name ?? '',
      scope: scopeOf(v),
      item: v,
    })),
    ...secrets.data.map((s): Row => ({
      kind: 'secret',
      id: s.id ?? '',
      name: s.name ?? '',
      scope: scopeOf(s),
      item: s,
    })),
  ].sort((a, b) => a.name.localeCompare(b.name) || rank[b.scope] - rank[a.scope])
  // The most specific scope wins (REQ-ENV-001).
  const winner = new Map<string, Row>()
  for (const row of rows) {
    const best = winner.get(row.name)
    if (!best || rank[row.scope] > rank[best.scope]) winner.set(row.name, row)
  }

  const blocked = (a: Access) => (a === 'blocked' ? t('common.needs_admin') : undefined)
  const toolbar = (
    <div className="flex flex-wrap gap-2">
      {varWrite !== 'hidden' && (
        <Button
          variant="primary"
          reason={blocked(varWrite)}
          onClick={() => setEditing({ kind: 'variable' })}
        >
          {t('variables.add')}
        </Button>
      )}
      {secretWrite !== 'hidden' && (
        <Button reason={blocked(secretWrite)} onClick={() => setEditing({ kind: 'secret' })}>
          {t('secret.add')}
        </Button>
      )}
      {varWrite !== 'hidden' && (
        <Button
          className="max-sm:hidden"
          reason={blocked(varWrite)}
          onClick={() => setImporting(true)}
        >
          {t('import.open')}
        </Button>
      )}
    </div>
  )

  const loadError = variables.error ?? secrets.error
  let body
  if (variables.pending || secrets.pending) body = <Skeleton />
  else if (rows.length === 0)
    body = (
      <EmptyState
        title={t('variables.empty_title')}
        text={t('variables.empty_text')}
        action={toolbar}
      />
    )
  else
    body = (
      <DataList
        caption={t('variables.caption')}
        rows={rows}
        rowKey={(r) => `${r.kind}-${r.id}`}
        columns={[
          {
            header: t('variables.name'),
            primary: true,
            cell: (r) => (
              <span className="flex flex-wrap items-center gap-1">
                <span className="break-all font-mono">{r.name}</span>
                {r.kind === 'secret' && <Chip>{t('variables.secret')}</Chip>}
                {r.item.build_time && <Chip>{t('variables.build_time_short')}</Chip>}
                {r.item.preview && <Chip>{t('variables.preview_short')}</Chip>}
                {r.item.git_managed && (
                  <Chip>
                    <LockIcon /> {t('variables.git_managed')}
                  </Chip>
                )}
              </span>
            ),
          },
          {
            header: t('variables.value'),
            cell: (r) =>
              r.kind === 'secret' ? (
                <span className="text-muted" data-secret-row="">
                  {t('secret.set_version', { version: r.item.current_version ?? 0 })}
                  {r.item.updated_at && (
                    <>
                      {' · '}
                      {t('secret.updated')} <Time value={r.item.updated_at} />
                    </>
                  )}
                  {r.item.updated_by && <> · {t('secret.by', { who: r.item.updated_by })}</>}
                </span>
              ) : (
                <span className="break-all font-mono">{r.item.value}</span>
              ),
          },
          {
            header: t('variables.scope'),
            cell: (r) => (
              <span>
                {t(`variables.scope.${r.scope}`)}
                {winner.get(r.name) !== r && (
                  <span className="block text-muted">
                    {t('variables.overridden', {
                      scope: t(`variables.scope.${winner.get(r.name)?.scope ?? 'service'}`),
                    })}
                  </span>
                )}
              </span>
            ),
          },
        ]}
        actions={(r) => {
          const access = r.kind === 'secret' ? secretWrite : varWrite
          const locked = r.item.git_managed
          return (
            <div className="flex flex-wrap gap-2">
              {access !== 'hidden' && !locked && (
                <Button
                  reason={blocked(access)}
                  onClick={() => setEditing({ kind: r.kind, row: r })}
                >
                  {r.kind === 'secret' ? t('secret.set_new') : t('common.edit')}
                </Button>
              )}
              {r.kind === 'secret' && (
                <Button onClick={() => setVersionsOf(r)}>{t('versions.open')}</Button>
              )}
              <Button variant="ghost" onClick={() => setWhereUsed(r)}>
                {t('where_used.open')}
              </Button>
              {access !== 'hidden' && !locked && (
                <Button
                  variant="destructive"
                  reason={blocked(access)}
                  onClick={() => setDeleting(r)}
                >
                  {t('common.delete')}
                </Button>
              )}
            </div>
          )
        }}
      />
    )

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-muted">{t('variables.intro')}</p>
        {rows.length > 0 && toolbar}
      </div>
      {loadError !== undefined && loadError !== null && (
        <ErrorBanner
          error={loadError}
          onRetry={() => {
            variables.retry()
            secrets.retry()
          }}
        />
      )}
      {body}
      <p className="text-sm text-muted">{t('secret.never_shown')}</p>
      {editing && (
        <ItemDialog
          ctx={ctx}
          target={editing}
          onClose={() => setEditing(null)}
          onChanged={onChanged}
        />
      )}
      {importing && (
        <ImportDialog ctx={ctx} onClose={() => setImporting(false)} onChanged={onChanged} />
      )}
      {whereUsed && (
        <WhereUsedDialog
          row={whereUsed}
          environmentId={ctx.environmentId}
          onClose={() => setWhereUsed(null)}
        />
      )}
      {versionsOf && (
        <VersionsPanel
          row={versionsOf}
          ctx={ctx}
          writeAccess={secretWrite}
          destroyAccess={secretDestroy}
          onClose={() => setVersionsOf(null)}
          onChanged={onChanged}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={t('variables.delete_title', { name: deleting.name })}
          text={deleting.kind === 'secret' ? t('secret.delete_text') : t('variables.delete_text')}
          confirmLabel={t('variables.delete_confirm', { name: deleting.name })}
          busy={remove.isPending}
          error={
            remove.error ? (
              <ErrorBanner error={remove.error} title={t('variables.delete_failed')} />
            ) : null
          }
          onConfirm={() => remove.mutate(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </div>
  )
}
