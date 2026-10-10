// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { CodeBlock } from '@/components/Copy'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog, Modal } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import {
  Banner,
  Button,
  CheckboxField,
  EmptyState,
  SelectField,
  Skeleton,
  StatusBadge,
  TextField,
} from '@/components/ui'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'
import { useProjects } from '@/scope'

type ApiToken = components['schemas']['ApiToken']
type Scope = 'read' | 'deploy' | 'write' | 'admin'

const SCOPES: Scope[] = ['read', 'deploy', 'write', 'admin']

function CreateDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient()
  const projects = useProjects()
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<Scope[]>(['read'])
  const [projectId, setProjectId] = useState('')
  const [expires, setExpires] = useState('')
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/tokens', {
          body: {
            name: name.trim(),
            scopes,
            ...(projectId ? { project_id: projectId } : {}),
            // The date input has a day. The token ends at the end of that day (UTC).
            ...(expires ? { expires_at: `${expires}T23:59:59Z` } : {}),
          },
        }),
      ),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['tokens'] }),
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    if (scopes.length > 0) create.mutate()
  }
  function toggle(scope: Scope, on: boolean) {
    setScopes(on ? [...scopes, scope] : scopes.filter((s) => s !== scope))
  }
  return (
    <Modal title={t('tokens.create_title')} onClose={onClose}>
      {create.data ? (
        <div className="space-y-4">
          <Banner variant="warning" title={t('tokens.shown_once_title')}>
            {t('tokens.shown_once')}
          </Banner>
          <CodeBlock text={create.data.token ?? ''} label={t('tokens.value')} />
          <div className="flex justify-end">
            <Button variant="primary" onClick={onClose}>
              {t('tokens.done')}
            </Button>
          </div>
        </div>
      ) : (
        <form onSubmit={submit} className="space-y-4">
          {create.error && <ErrorBanner error={create.error} title={t('tokens.create_failed')} />}
          <TextField
            label={t('tokens.name')}
            required
            autoFocus
            autoComplete="off"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">{t('tokens.scopes')}</legend>
            {SCOPES.map((s) => (
              <CheckboxField
                key={s}
                label={t(`tokens.scope.${s}`)}
                hint={t(`tokens.scope_hint.${s}`)}
                checked={scopes.includes(s)}
                onChange={(e) => toggle(s, e.target.checked)}
              />
            ))}
            {scopes.length === 0 && <p className="text-sm text-danger">{t('tokens.scope_none')}</p>}
          </fieldset>
          <SelectField
            label={t('tokens.project')}
            hint={t('tokens.project_hint')}
            value={projectId}
            onChange={(e) => setProjectId(e.target.value)}
          >
            <option value="">{t('tokens.all_projects')}</option>
            {(projects.data ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </SelectField>
          <TextField
            label={t('tokens.expires')}
            hint={t('tokens.expires_hint')}
            type="date"
            value={expires}
            onChange={(e) => setExpires(e.target.value)}
          />
          <div className="flex flex-wrap justify-end gap-2">
            <Button onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" variant="primary" loading={create.isPending}>
              {t('tokens.create')}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  )
}

// UI-25: API tokens. An admin can list and revoke the tokens of everyone.
export function TokensPage() {
  const queryClient = useQueryClient()
  const projects = useProjects()
  const isAdmin = useAccess('token:manage') === 'allow'
  const [showAll, setShowAll] = useState(false)
  const [creating, setCreating] = useState(false)
  const [revoking, setRevoking] = useState<ApiToken | null>(null)
  const all = isAdmin && showAll
  const tokens = useQuery({
    queryKey: ['tokens', all],
    queryFn: () => unwrap(api.GET('/tokens', { params: { query: { all } } })),
  })
  // Own tokens use DELETE /tokens. The tokens of others need DELETE /org/tokens (token:manage).
  const revoke = useMutation({
    mutationFn: (token: ApiToken) =>
      all
        ? unwrap(
            api.DELETE('/org/tokens/{token_id}', {
              params: { path: { token_id: token.id ?? '' } },
            }),
          )
        : unwrap(
            api.DELETE('/tokens/{token_id}', { params: { path: { token_id: token.id ?? '' } } }),
          ),
    onSuccess: () => {
      setRevoking(null)
      return queryClient.invalidateQueries({ queryKey: ['tokens'] })
    },
  })
  const projectName = (id?: string) =>
    id ? (projects.data?.find((p) => p.id === id)?.name ?? id) : t('tokens.all_projects')
  return (
    <section className="max-w-5xl space-y-4">
      <PageHeader
        title={t('tokens.title')}
        actions={
          <Button variant="primary" onClick={() => setCreating(true)}>
            {t('tokens.create')}
          </Button>
        }
      />
      <p className="text-sm text-muted">{t('tokens.intro')}</p>
      {isAdmin && (
        <CheckboxField
          label={t('tokens.show_all')}
          checked={showAll}
          onChange={(e) => setShowAll(e.target.checked)}
        />
      )}
      {tokens.isPending && <Skeleton />}
      {tokens.isError && <ErrorBanner error={tokens.error} onRetry={() => void tokens.refetch()} />}
      {tokens.data &&
        (tokens.data.length === 0 ? (
          <EmptyState title={t('tokens.empty_title')} text={t('tokens.empty_text')} />
        ) : (
          <DataList
            caption={t('tokens.caption')}
            rows={tokens.data}
            rowKey={(k) => k.id ?? ''}
            columns={[
              {
                header: t('tokens.name'),
                primary: true,
                cell: (k) => (
                  <span className="break-words">
                    {k.name}
                    <span className="block font-mono text-xs text-muted">{k.token_prefix}</span>
                  </span>
                ),
              },
              { header: t('tokens.scopes'), cell: (k) => (k.scopes ?? []).join(', ') },
              { header: t('tokens.project'), cell: (k) => projectName(k.project_id) },
              {
                header: t('tokens.expires'),
                cell: (k) => (k.expires_at ? <Time value={k.expires_at} /> : t('tokens.never')),
              },
              {
                header: t('tokens.last_used'),
                cell: (k) =>
                  k.last_used_at ? <Time value={k.last_used_at} /> : t('tokens.never_used'),
                cardHidden: true,
              },
              {
                header: t('tokens.status'),
                cell: (k) =>
                  k.revoked_at ? (
                    <StatusBadge tone="muted" label={t('tokens.revoked')} />
                  ) : (
                    <StatusBadge tone="success" label={t('tokens.active')} />
                  ),
              },
            ]}
            actionsHeader={t('common.actions')}
            actions={(k) =>
              k.revoked_at ? null : (
                <Button variant="destructive" onClick={() => setRevoking(k)}>
                  {t('tokens.revoke')}
                </Button>
              )
            }
          />
        ))}
      {creating && <CreateDialog onClose={() => setCreating(false)} />}
      {revoking && (
        <ConfirmDialog
          title={t('tokens.revoke')}
          text={t('tokens.revoke_text', { name: revoking.name ?? '' })}
          confirmLabel={t('tokens.revoke')}
          busy={revoke.isPending}
          error={
            revoke.error && <ErrorBanner error={revoke.error} title={t('tokens.revoke_failed')} />
          }
          onConfirm={() => revoke.mutate(revoking)}
          onClose={() => {
            revoke.reset()
            setRevoking(null)
          }}
        />
      )}
    </section>
  )
}
