// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog, Modal } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import { Time } from '@/components/Time'
import {
  Banner,
  Button,
  EmptyState,
  Optional,
  SecretField,
  SelectField,
  Skeleton,
  StatusBadge,
  TextAreaField,
  TextField,
} from '@/components/ui'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'

type Target = components['schemas']['BackupTarget']
type Kind = NonNullable<Target['kind']>

const KINDS: Kind[] = ['s3', 'b2', 'sftp', 'local', 'azure', 'gcs']

interface FieldSpec {
  key: string
  label: string
  secret?: boolean
  multiline?: boolean
  required?: boolean
  hint?: string
  config?: boolean
}

// The fields of each kind: config values are shown again, credentials are write-only (REQ-BKP-004).
function fieldsOf(kind: Kind): FieldSpec[] {
  switch (kind) {
    case 's3':
      return [
        {
          key: 'endpoint',
          label: t('target.endpoint'),
          hint: t('target.endpoint_hint'),
          config: true,
        },
        { key: 'bucket', label: t('target.bucket'), required: true, config: true },
        { key: 'path', label: t('target.path'), hint: t('target.path_hint'), config: true },
        { key: 'region', label: t('target.region'), config: true },
        { key: 'access_key_id', label: t('target.access_key_id'), secret: true, required: true },
        {
          key: 'secret_access_key',
          label: t('target.secret_access_key'),
          secret: true,
          required: true,
        },
      ]
    case 'b2':
      return [
        { key: 'bucket', label: t('target.bucket'), required: true, config: true },
        { key: 'path', label: t('target.path'), hint: t('target.path_hint'), config: true },
        { key: 'account_id', label: t('target.account_id'), secret: true, required: true },
        { key: 'account_key', label: t('target.account_key'), secret: true, required: true },
      ]
    case 'sftp':
      return [
        { key: 'host', label: t('target.host'), required: true, config: true },
        { key: 'port', label: t('target.port'), config: true },
        { key: 'user', label: t('target.user'), required: true, config: true },
        { key: 'path', label: t('target.sftp_path'), required: true, config: true },
        {
          key: 'known_host',
          label: t('target.known_host'),
          hint: t('target.known_host_hint'),
          config: true,
        },
        {
          key: 'private_key',
          label: t('target.private_key'),
          secret: true,
          multiline: true,
          required: true,
        },
      ]
    case 'local':
      return [
        {
          key: 'path',
          label: t('target.local_path'),
          hint: t('target.local_hint'),
          required: true,
          config: true,
        },
      ]
    case 'azure':
      return [
        { key: 'container', label: t('target.container'), required: true, config: true },
        { key: 'path', label: t('target.path'), config: true },
        { key: 'account_name', label: t('target.account_name'), secret: true, required: true },
        { key: 'account_key', label: t('target.account_key'), secret: true, required: true },
      ]
    default:
      return [
        { key: 'bucket', label: t('target.bucket'), required: true, config: true },
        { key: 'path', label: t('target.path'), config: true },
        { key: 'project_id', label: t('target.project_id'), config: true },
        {
          key: 'credentials_json',
          label: t('target.credentials_json'),
          secret: true,
          multiline: true,
          required: true,
        },
      ]
  }
}

function TargetDialog({ target, onClose }: { target?: Target; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState(target?.name ?? '')
  const [kind, setKind] = useState<Kind>(target?.kind ?? 's3')
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(Object.entries(target?.config ?? {}).map(([k, v]) => [k, String(v)])),
  )
  const [submitted, setSubmitted] = useState(false)
  const fields = fieldsOf(kind)
  const editing = Boolean(target)
  const problem = (f: FieldSpec): string | null => {
    const v = (values[f.key] ?? '').trim()
    // On an edit the credentials stay when left empty. A new target needs them all.
    if (f.required && v === '' && !(editing && f.secret)) return t('target.required')
    return null
  }
  const nameError = /^[A-Za-z0-9][A-Za-z0-9 ._-]{0,62}$/.test(name)
    ? null
    : t('target.name_invalid')
  const valid = !nameError && fields.every((f) => !problem(f))

  const save = useMutation({
    mutationFn: () => {
      const config: Record<string, unknown> = {}
      const credentials: Record<string, string> = {}
      for (const f of fields) {
        const v = (values[f.key] ?? '').trim()
        if (f.secret) {
          if (v !== '') credentials[f.key] = f.multiline ? (values[f.key] ?? '') : v
        } else if (v !== '') {
          config[f.key] = f.key === 'port' ? Number(v) : v
        }
      }
      const body = {
        name: name.trim(),
        kind,
        config,
        ...(Object.keys(credentials).length > 0 || !editing ? { credentials } : {}),
      }
      return target?.id
        ? unwrap(
            api.PATCH('/backup-targets/{target_id}', {
              params: { path: { target_id: target.id } },
              body,
            }),
          )
        : unwrap(api.POST('/backup-targets', { body }))
    },
    onSuccess: async () => {
      // The credentials are write-only: forget them at once.
      setValues({})
      await queryClient.invalidateQueries({ queryKey: ['backup-targets'] })
      onClose()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (valid) save.mutate()
  }

  const renderField = (f: (typeof fields)[number]) => {
    const common = {
      label: f.label,
      hint: f.hint,
      required: f.required && !(editing && f.secret),
      error: submitted ? problem(f) : null,
    }
    if (f.secret && f.multiline) {
      return (
        <TextAreaField
          key={`${kind}-${f.key}`}
          {...common}
          hint={editing ? t('target.secret_keep') : t('secret.write_only')}
          autoComplete="off"
          spellCheck={false}
          data-secret-input=""
          value={values[f.key] ?? ''}
          onChange={(e) => setValues((v) => ({ ...v, [f.key]: e.target.value }))}
        />
      )
    }
    if (f.secret) {
      return (
        <SecretField
          key={`${kind}-${f.key}`}
          {...common}
          value={values[f.key] ?? ''}
          onValue={(v) => setValues((s) => ({ ...s, [f.key]: v }))}
        />
      )
    }
    return (
      <TextField
        key={`${kind}-${f.key}`}
        {...common}
        autoComplete="off"
        autoCapitalize="none"
        spellCheck={false}
        value={values[f.key] ?? ''}
        onChange={(e) => setValues((s) => ({ ...s, [f.key]: e.target.value }))}
      />
    )
  }
  const optionalFields = fields.filter((f) => !f.required)

  return (
    <Modal title={editing ? t('target.edit_title') : t('target.add_title')} onClose={onClose}>
      <form onSubmit={submit} noValidate className="space-y-4">
        {save.error && <ErrorBanner error={save.error} title={t('target.save_failed')} />}
        <TextField
          label={t('target.name')}
          required
          autoComplete="off"
          error={submitted ? nameError : null}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <SelectField
          label={t('target.kind')}
          value={kind}
          disabled={editing}
          onChange={(e) => {
            setKind(e.target.value as Kind)
            setValues({})
          }}
        >
          {KINDS.map((k) => (
            <option key={k} value={k}>
              {t(`target.kind.${k}`)}
            </option>
          ))}
        </SelectField>
        {fields.filter((f) => f.required).map(renderField)}
        {optionalFields.length > 0 && (
          <Optional filled={editing || optionalFields.some((f) => (values[f.key] ?? '') !== '')}>
            <div className="space-y-4">{optionalFields.map(renderField)}</div>
          </Optional>
        )}
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

// UI-38: where backups go. Keys are write-only; a failed test shows the reason.
export function BackupTargetsPage() {
  useParams({ from: '/o/$org/settings/backup-targets' })
  const queryClient = useQueryClient()
  const access = useAccess('backup:manage')
  const [editing, setEditing] = useState<Target | 'new' | null>(null)
  const [deleting, setDeleting] = useState<Target | null>(null)
  const [results, setResults] = useState<Record<string, { ok: boolean; error?: string }>>({})
  const targets = useQuery({
    queryKey: ['backup-targets'],
    queryFn: () => unwrap(api.GET('/backup-targets')),
  })
  const test = useMutation({
    mutationFn: async (target: Target) => {
      const out = await unwrap(
        api.POST('/backup-targets/{target_id}/test', {
          params: { path: { target_id: target.id ?? '' } },
        }),
      )
      setResults((r) => ({ ...r, [target.id ?? '']: { ok: Boolean(out.ok), error: out.error } }))
      return out
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['backup-targets'] }),
  })
  const remove = useMutation({
    mutationFn: (target: Target) =>
      unwrap(
        api.DELETE('/backup-targets/{target_id}', {
          params: { path: { target_id: target.id ?? '' } },
        }),
      ),
    onSuccess: async () => {
      setDeleting(null)
      await queryClient.invalidateQueries({ queryKey: ['backup-targets'] })
    },
  })
  const manage = access === 'allow'
  const add = manage ? (
    <Button variant="primary" onClick={() => setEditing('new')}>
      {t('target.add')}
    </Button>
  ) : undefined

  let body
  if (targets.isPending) body = <Skeleton />
  else if (targets.isError)
    body = <ErrorBanner error={targets.error} onRetry={() => void targets.refetch()} />
  else if (targets.data.length === 0)
    body = <EmptyState title={t('target.empty_title')} text={t('target.empty_text')} action={add} />
  else
    body = (
      <DataList
        caption={t('target.caption')}
        rows={targets.data}
        rowKey={(x) => x.id ?? ''}
        columns={[
          {
            header: t('target.name'),
            primary: true,
            cell: (x) => <span className="break-all">{x.name}</span>,
          },
          { header: t('target.kind'), cell: (x) => t(`target.kind.${x.kind ?? 's3'}`) },
          {
            header: t('target.where'),
            cell: (x) => (
              <span className="break-all font-mono text-xs">
                {[
                  x.config?.endpoint,
                  x.config?.host,
                  x.config?.bucket ?? x.config?.container,
                  x.config?.path,
                ]
                  .filter(Boolean)
                  .map(String)
                  .join(' / ')}
              </span>
            ),
          },
          {
            header: t('target.last_test'),
            cell: (x) => {
              const r = results[x.id ?? '']
              if (r) {
                return r.ok ? (
                  <StatusBadge tone="success" label={t('target.test_ok')} />
                ) : (
                  <span className="space-y-1">
                    <StatusBadge tone="danger" label={t('target.test_failed')} />
                    {r.error && (
                      <span className="block break-words text-xs text-danger">{r.error}</span>
                    )}
                  </span>
                )
              }
              if (x.last_test_at) {
                return (
                  <span className="flex flex-wrap items-center gap-2">
                    <StatusBadge
                      tone={x.last_test_ok ? 'success' : 'danger'}
                      label={x.last_test_ok ? t('target.test_ok') : t('target.test_failed')}
                    />
                    <Time value={x.last_test_at} />
                  </span>
                )
              }
              return <span className="text-muted">{t('target.never_tested')}</span>
            },
          },
        ]}
        actions={
          manage
            ? (x) => (
                <>
                  <Button
                    loading={test.isPending && test.variables?.id === x.id}
                    onClick={() => test.mutate(x)}
                  >
                    {t('target.test')}
                  </Button>
                  <Button onClick={() => setEditing(x)}>{t('backup.edit')}</Button>
                  <Button variant="destructive" onClick={() => setDeleting(x)}>
                    {t('backup.delete')}
                  </Button>
                </>
              )
            : undefined
        }
      />
    )

  return (
    <section>
      <PageHeader
        title={t('target.title')}
        actions={targets.data && targets.data.length > 0 ? add : undefined}
      />
      <div className="space-y-4">
        <Banner>{t('target.intro')}</Banner>
        {test.error && <ErrorBanner error={test.error} title={t('target.test_failed')} />}
        {body}
      </div>
      {editing && (
        <TargetDialog
          target={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={t('target.delete_title', { name: deleting.name ?? '' })}
          text={t('target.delete_text')}
          confirmLabel={t('backup.delete')}
          busy={remove.isPending}
          error={remove.error ? <ErrorBanner error={remove.error} /> : undefined}
          onConfirm={() => remove.mutate(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </section>
  )
}
