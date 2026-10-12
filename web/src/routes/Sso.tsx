// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { CodeBlock } from '@/components/Copy'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import {
  Banner,
  Button,
  Card,
  CheckboxField,
  Optional,
  EmptyState,
  SecretField,
  SelectField,
  Skeleton,
  TextField,
} from '@/components/ui'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'
import { useProjects } from '@/scope'

type Provider = components['schemas']['SsoProvider']
type Mapping = components['schemas']['GroupMapping']
type Role = components['schemas']['Role']
type Kind = 'oidc' | 'google' | 'github'

const KINDS: Kind[] = ['oidc', 'google', 'github']

// The search of the page: the result of "Test login" comes back here.
export function validateSsoSearch(search: Record<string, unknown>): {
  sso_test?: string
  reason?: string
} {
  return {
    ...(typeof search.sso_test === 'string' ? { sso_test: search.sso_test } : {}),
    ...(typeof search.reason === 'string' ? { reason: search.reason } : {}),
  }
}

function useProviders() {
  return useQuery({
    queryKey: ['sso-providers'],
    queryFn: () => unwrap(api.GET('/sso-providers')),
  })
}

function split(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

// The address the identity provider sends the browser back to. The admin copies it into the provider.
function callbackUrl(id: string): string {
  return `${window.location.origin}/api/v1/auth/sso/${id}/callback`
}

function RequiredSwitch({ providers }: { providers: Provider[] }) {
  const queryClient = useQueryClient()
  const org = useQuery({ queryKey: ['org'], queryFn: () => unwrap(api.GET('/org')) })
  const [asking, setAsking] = useState(false)
  const required = org.data?.sso_required === true
  const change = useMutation({
    mutationFn: (value: boolean) => unwrap(api.PATCH('/org', { body: { sso_required: value } })),
    onSuccess: async () => {
      setAsking(false)
      await queryClient.invalidateQueries({ queryKey: ['org'] })
    },
  })
  const noProvider = !providers.some((p) => p.enabled)
  return (
    <Card className="space-y-3">
      <CheckboxField
        label={t('sso.required')}
        hint={t('sso.required_hint')}
        checked={required}
        disabled={org.isPending || change.isPending || (!required && noProvider)}
        onChange={(e) => (e.target.checked ? setAsking(true) : change.mutate(false))}
      />
      {!required && noProvider && (
        <p className="text-sm text-muted">{t('sso.required_needs_provider')}</p>
      )}
      {change.error && !asking && (
        <ErrorBanner error={change.error} title={t('sso.required_failed')} />
      )}
      {asking && (
        <ConfirmDialog
          title={t('sso.required_confirm_title')}
          text={t('sso.required_confirm')}
          confirmLabel={t('sso.required_confirm_button')}
          busy={change.isPending}
          error={
            change.error && <ErrorBanner error={change.error} title={t('sso.required_failed')} />
          }
          onConfirm={() => change.mutate(true)}
          onClose={() => setAsking(false)}
        />
      )}
    </Card>
  )
}

function MappingsEditor({ provider, canOwner }: { provider: Provider; canOwner: boolean }) {
  const id = provider.id ?? ''
  const queryClient = useQueryClient()
  const projects = useProjects()
  const saved = useQuery({
    queryKey: ['sso-mappings', id],
    queryFn: () =>
      unwrap(
        api.GET('/sso-providers/{provider_id}/group-mappings', {
          params: { path: { provider_id: id } },
        }),
      ),
  })
  const [rows, setRows] = useState<Mapping[] | null>(null)
  const current = rows ?? saved.data ?? []
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT('/sso-providers/{provider_id}/group-mappings', {
          params: { path: { provider_id: id } },
          body: current.filter((m) => m.group_value.trim() !== ''),
        }),
      ),
    onSuccess: async () => {
      setRows(null)
      await queryClient.invalidateQueries({ queryKey: ['sso-mappings', id] })
    },
  })
  const update = (i: number, patch: Partial<Mapping>) =>
    setRows(current.map((m, j) => (i === j ? { ...m, ...patch } : m)))
  const roles: Role[] = canOwner
    ? ['owner', 'admin', 'developer', 'viewer']
    : ['admin', 'developer', 'viewer']
  const groupHint = provider.kind === 'github' ? t('sso.group_hint_github') : t('sso.group_hint')
  return (
    <section className="space-y-3" aria-labelledby={`map-${id}`}>
      <h3 id={`map-${id}`} className="text-base font-semibold">
        {t('sso.mappings')}
      </h3>
      <p className="text-sm text-muted">{t('sso.mappings_hint')}</p>
      {save.error && <ErrorBanner error={save.error} title={t('sso.mappings_failed')} />}
      {saved.isPending ? (
        <Skeleton lines={2} />
      ) : (
        <ul className="space-y-3">
          {current.map((m, i) => (
            <li key={i} className="grid gap-2 sm:grid-cols-[1fr_9rem_1fr_auto] sm:items-end">
              <TextField
                label={t('sso.group')}
                hint={i === 0 ? groupHint : undefined}
                value={m.group_value}
                onChange={(e) => update(i, { group_value: e.target.value })}
              />
              <SelectField
                label={t('sso.role')}
                value={m.role}
                onChange={(e) => update(i, { role: e.target.value as Role })}
              >
                {roles.map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </SelectField>
              <SelectField
                label={t('sso.scope')}
                value={m.project_id ?? ''}
                onChange={(e) =>
                  update(i, {
                    project_id: e.target.value || undefined,
                    ...(e.target.value && m.role === 'owner' ? { role: 'admin' as Role } : {}),
                  })
                }
              >
                <option value="">{t('sso.scope_org')}</option>
                {(projects.data ?? []).map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </SelectField>
              <Button
                onClick={() => setRows(current.filter((_, j) => j !== i))}
                aria-label={t('sso.mapping_remove', { group: m.group_value || String(i + 1) })}
              >
                {t('common.remove')}
              </Button>
            </li>
          ))}
        </ul>
      )}
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => setRows([...current, { group_value: '', role: 'viewer' }])}>
          {t('sso.mapping_add')}
        </Button>
        <Button
          variant="primary"
          loading={save.isPending}
          disabled={rows === null}
          onClick={() => save.mutate()}
        >
          {t('sso.mappings_save')}
        </Button>
      </div>
    </section>
  )
}

interface FormState {
  kind: Kind
  name: string
  issuer_url: string
  client_id: string
  client_secret: string
  scopes: string
  groups_claim: string
  allowed_domains: string
  default_role: string
  auto_create_users: boolean
  enabled: boolean
}

function toForm(p?: Provider): FormState {
  return {
    kind: (p?.kind as Kind) ?? 'oidc',
    name: p?.name ?? '',
    issuer_url: p?.issuer_url ?? '',
    client_id: p?.client_id ?? '',
    client_secret: '',
    scopes: (p?.scopes ?? []).join(' '),
    groups_claim: p?.groups_claim ?? '',
    allowed_domains: (p?.allowed_domains ?? []).join(', '),
    default_role: p?.default_role ?? '',
    auto_create_users: p?.auto_create_users ?? true,
    enabled: p?.enabled ?? true,
  }
}

function ProviderForm({
  provider,
  onSaved,
  onDeleted,
}: {
  provider?: Provider
  onSaved: (id: string) => void
  onDeleted: () => void
}) {
  const queryClient = useQueryClient()
  const me = useMe()
  const [form, setForm] = useState<FormState>(() => toForm(provider))
  const [deleting, setDeleting] = useState(false)
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) =>
    setForm({ ...form, [key]: value })
  const id = provider?.id
  const unsafe =
    form.kind !== 'oidc' &&
    form.auto_create_users &&
    form.default_role !== '' &&
    split(form.allowed_domains).length === 0

  const save = useMutation({
    mutationFn: () => {
      const body = {
        name: form.name.trim(),
        client_id: form.client_id.trim(),
        ...(form.client_secret ? { client_secret: form.client_secret } : {}),
        ...(form.kind === 'oidc'
          ? { issuer_url: form.issuer_url.trim(), groups_claim: form.groups_claim.trim() }
          : {}),
        ...(form.scopes.trim() ? { scopes: split(form.scopes) } : {}),
        allowed_domains: split(form.allowed_domains),
        ...(form.default_role ? { default_role: form.default_role as Role } : {}),
        auto_create_users: form.auto_create_users,
        enabled: form.enabled,
      }
      return id
        ? unwrap(
            api.PATCH('/sso-providers/{provider_id}', {
              params: { path: { provider_id: id } },
              body,
            }),
          )
        : unwrap(api.POST('/sso-providers', { body: { ...body, kind: form.kind } }))
    },
    onSuccess: async (saved) => {
      await queryClient.invalidateQueries({ queryKey: ['sso-providers'] })
      set('client_secret', '')
      onSaved(saved.id ?? '')
    },
  })
  const del = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE('/sso-providers/{provider_id}', { params: { path: { provider_id: id ?? '' } } }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['sso-providers'] })
      setDeleting(false)
      onDeleted()
    },
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    save.mutate()
  }
  return (
    <div className="space-y-6">
      <form onSubmit={submit} className="space-y-4">
        {save.error && <ErrorBanner error={save.error} title={t('sso.save_failed')} />}
        {unsafe && <Banner variant="warning">{t('sso.unsafe')}</Banner>}
        <SelectField
          label={t('sso.kind')}
          value={form.kind}
          disabled={id !== undefined}
          onChange={(e) => set('kind', e.target.value as Kind)}
        >
          {KINDS.map((k) => (
            <option key={k} value={k}>
              {t(`sso.kind.${k}`)}
            </option>
          ))}
        </SelectField>
        <TextField
          label={t('sso.name')}
          required
          value={form.name}
          onChange={(e) => set('name', e.target.value)}
        />
        {form.kind === 'oidc' && (
          <TextField
            label={t('sso.issuer')}
            hint={t('sso.issuer_hint')}
            type="url"
            required
            autoComplete="off"
            value={form.issuer_url}
            onChange={(e) => set('issuer_url', e.target.value)}
          />
        )}
        <TextField
          label={t('sso.client_id')}
          required
          autoComplete="off"
          value={form.client_id}
          onChange={(e) => set('client_id', e.target.value)}
        />
        <SecretField
          label={t('sso.client_secret')}
          required={id === undefined}
          value={form.client_secret}
          onValue={(v) => set('client_secret', v)}
        />
        <Optional
          filled={
            id !== undefined ||
            form.scopes !== '' ||
            form.groups_claim !== '' ||
            form.allowed_domains !== '' ||
            form.default_role !== ''
          }
        >
          <div className="space-y-4">
            <TextField
              label={t('sso.scopes')}
              hint={t('sso.scopes_hint')}
              value={form.scopes}
              onChange={(e) => set('scopes', e.target.value)}
            />
            {form.kind === 'oidc' && (
              <TextField
                label={t('sso.groups_claim')}
                hint={t('sso.groups_claim_hint')}
                value={form.groups_claim}
                onChange={(e) => set('groups_claim', e.target.value)}
              />
            )}
            <TextField
              label={t('sso.domains')}
              hint={form.kind === 'google' ? t('sso.domains_hint_google') : t('sso.domains_hint')}
              value={form.allowed_domains}
              onChange={(e) => set('allowed_domains', e.target.value)}
            />
            <SelectField
              label={t('sso.default_role')}
              hint={t('sso.default_role_hint')}
              value={form.default_role}
              onChange={(e) => set('default_role', e.target.value)}
            >
              <option value="">{t('sso.no_default_role')}</option>
              {['admin', 'developer', 'viewer'].map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </SelectField>
            <CheckboxField
              label={t('sso.auto_create')}
              hint={t('sso.auto_create_hint')}
              checked={form.auto_create_users}
              onChange={(e) => set('auto_create_users', e.target.checked)}
            />
            <CheckboxField
              label={t('sso.enabled')}
              checked={form.enabled}
              onChange={(e) => set('enabled', e.target.checked)}
            />
          </div>
        </Optional>
        <div className="flex flex-wrap justify-end gap-2">
          {id && (
            <Button variant="destructive" onClick={() => setDeleting(true)}>
              {t('common.delete')}
            </Button>
          )}
          <Button type="submit" variant="primary" loading={save.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
      {id && (
        <>
          <CodeBlock text={callbackUrl(id)} label={t('sso.callback_url')} />
          <div className="flex flex-wrap items-center gap-3">
            <a
              className="inline-flex min-h-9 items-center rounded-md border border-border px-3 py-1.5 text-sm font-medium hover:bg-surface-hover"
              href={`/api/v1/auth/sso/${id}/start?test=1`}
            >
              {t('sso.test_login')}
            </a>
            <span className="text-sm text-muted">{t('sso.test_login_hint')}</span>
          </div>
          <MappingsEditor provider={provider} canOwner={me.data?.org_role === 'owner'} />
        </>
      )}
      {deleting && (
        <ConfirmDialog
          title={t('sso.delete_title')}
          text={t('sso.delete_text', { name: provider?.name ?? '' })}
          confirmLabel={t('common.delete')}
          busy={del.isPending}
          error={del.error && <ErrorBanner error={del.error} title={t('sso.delete_failed')} />}
          onConfirm={() => del.mutate()}
          onClose={() => setDeleting(false)}
        />
      )}
    </div>
  )
}

// UI-24 Settings: SSO. List on the left, form on the right (not on a phone: read-only there).
export function SsoPage() {
  const access = useAccess('sso:manage')
  const providers = useProviders()
  const search = useSearch({ strict: false }) as { sso_test?: string; reason?: string }
  const navigate = useNavigate()
  const [selected, setSelected] = useState<string | 'new' | null>(null)
  const list = useMemo(() => providers.data ?? [], [providers.data])
  useEffect(() => {
    if (selected === null && list.length > 0) setSelected(list[0].id ?? null)
  }, [selected, list])

  if (access === 'hidden') return null
  const current = list.find((p) => p.id === selected)
  return (
    <div className="space-y-4 p-4 sm:p-6">
      <PageHeader title={t('sso.title')} />
      {search.sso_test === 'ok' && <Banner variant="info">{t('sso.test_ok')}</Banner>}
      {search.sso_test === 'error' && (
        <Banner variant="danger">
          {t('sso.test_error', { reason: search.reason ?? 'failed' })}
        </Banner>
      )}
      <p className="text-sm text-muted">{t('sso.intro')}</p>
      {providers.error && <ErrorBanner error={providers.error} />}
      {providers.isPending ? (
        <Skeleton lines={4} />
      ) : (
        <>
          <RequiredSwitch providers={list} />
          <Banner variant="info">
            <span className="sm:hidden">{t('sso.phone')}</span>
            <span className="max-sm:hidden">{t('sso.break_glass_note')}</span>
          </Banner>
          <div className="grid gap-6 lg:grid-cols-[18rem_1fr]">
            <nav aria-label={t('sso.list')} className="space-y-2">
              {list.length === 0 && (
                <EmptyState title={t('sso.empty_title')} text={t('sso.empty_text')} />
              )}
              <ul className="space-y-1">
                {list.map((p) => (
                  <li key={p.id}>
                    <button
                      type="button"
                      aria-current={selected === p.id ? 'true' : undefined}
                      onClick={() => setSelected(p.id ?? null)}
                      className="flex w-full items-center justify-between gap-2 rounded-md border border-border px-3 py-2 text-left hover:bg-surface-hover aria-[current=true]:border-primary"
                    >
                      <span className="break-words font-medium">{p.name}</span>
                      <span className="text-sm text-muted">
                        {t(`sso.kind.${p.kind ?? 'oidc'}`)}
                        {p.enabled === false ? ` · ${t('sso.disabled')}` : ''}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
              <Button className="max-sm:hidden" onClick={() => setSelected('new')}>
                {t('sso.add')}
              </Button>
            </nav>
            <Card className="max-sm:hidden">
              {selected === 'new' ? (
                <ProviderForm
                  key="new"
                  onSaved={(id) => setSelected(id)}
                  onDeleted={() => setSelected(null)}
                />
              ) : current ? (
                <ProviderForm
                  key={current.id}
                  provider={current}
                  onSaved={() => undefined}
                  onDeleted={() => {
                    setSelected(null)
                    void navigate({ to: '.', search: {} })
                  }}
                />
              ) : (
                <p className="text-muted">{t('sso.pick')}</p>
              )}
            </Card>
          </div>
        </>
      )}
    </div>
  )
}
