// SPDX-License-Identifier: Apache-2.0

import { isManaged } from '@/managed'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog, Modal, TypedConfirmDialog } from '@/components/Modal'
import {
  Banner,
  Button,
  CheckboxField,
  Optional,
  EmptyState,
  SelectField,
  Skeleton,
  TextAreaField,
  TextField,
} from '@/components/ui'
import { formatBytes } from '@/envText'
import { t } from '@/i18n'
import { accessFor } from '@/permissions'
import type { Access } from '@/permissions'
import { useNodes } from '@/scope'
import type { Service } from '@/scope'

type Volume = components['schemas']['Volume']
type VolumeWrite = components['schemas']['VolumeWrite']
interface FileMount {
  path: string
  content: string
}

const GIB = 1024 ** 3
const MAX_FILE = 1024 * 1024

function UsageBar({ used, limit }: { used?: number; limit?: number }) {
  if (used === undefined) return <span>-</span>
  const text = limit
    ? t('storage.usage_of', { used: formatBytes(used), limit: formatBytes(limit) })
    : formatBytes(used)
  return (
    <span className="flex flex-wrap items-center gap-2">
      {limit ? (
        <meter
          min={0}
          max={limit}
          value={used}
          high={limit * 0.85}
          optimum={0}
          aria-label={text}
          className="h-3 w-24"
        />
      ) : null}
      <span>{text}</span>
    </span>
  )
}

function VolumeDialog({
  serviceId,
  volume,
  onClose,
}: {
  serviceId: string
  volume?: Volume
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const [name, setName] = useState(volume?.name ?? '')
  const [kind, setKind] = useState<NonNullable<VolumeWrite['kind']>>(volume?.kind ?? 'volume')
  const [path, setPath] = useState(volume?.mount_path ?? '')
  const [mode, setMode] = useState<NonNullable<VolumeWrite['access_mode']>>(
    volume?.access_mode ?? 'single_writer',
  )
  const [size, setSize] = useState(
    volume?.size_limit_bytes ? String(Math.round((volume.size_limit_bytes / GIB) * 10) / 10) : '',
  )
  const [submitted, setSubmitted] = useState(false)
  const errors = {
    name: /^[a-z0-9][a-z0-9_.-]{0,62}$/.test(name) ? null : t('storage.name_invalid'),
    path: path.startsWith('/') && !/\s/.test(path) ? null : t('storage.path_invalid'),
    size: size === '' || Number(size) > 0 ? null : t('storage.size_invalid'),
  }

  const save = useMutation({
    mutationFn: () => {
      const body: VolumeWrite = {
        name,
        kind,
        mount_path: path,
        access_mode: mode,
        ...(size ? { size_limit_bytes: Math.round(Number(size) * GIB) } : {}),
      }
      return volume?.id
        ? unwrap(
            api.PATCH('/volumes/{volume_id}', { params: { path: { volume_id: volume.id } }, body }),
          )
        : unwrap(
            api.POST('/services/{service_id}/volumes', {
              params: { path: { service_id: serviceId } },
              body,
            }),
          )
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['volumes', serviceId] })
      onClose()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (!errors.name && !errors.path && !errors.size) save.mutate()
  }

  return (
    <Modal
      title={volume ? t('storage.edit_title', { name: volume.name ?? '' }) : t('storage.add_title')}
      onClose={onClose}
    >
      <form onSubmit={submit} noValidate className="space-y-4">
        {save.error && <ErrorBanner error={save.error} title={t('storage.save_failed')} />}
        <TextField
          label={t('storage.name')}
          required
          readOnly={Boolean(volume)}
          autoComplete="off"
          autoCapitalize="none"
          error={submitted ? errors.name : null}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <TextField
          label={t('storage.path')}
          hint={t('storage.path_hint')}
          required
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          error={submitted ? errors.path : null}
          value={path}
          onChange={(e) => setPath(e.target.value)}
        />
        <Optional
          filled={
            Boolean(volume) ||
            size !== '' ||
            kind !== 'volume' ||
            mode !== 'single_writer' ||
            Boolean(submitted && errors.size)
          }
        >
          <div className="space-y-4">
            {!volume && (
              <SelectField
                label={t('storage.kind')}
                hint={t(`storage.kind.${kind}_hint`)}
                value={kind}
                onChange={(e) => setKind(e.target.value as typeof kind)}
              >
                <option value="volume">{t('storage.kind.volume')}</option>
                <option value="bind">{t('storage.kind.bind')}</option>
                <option value="tmpfs">{t('storage.kind.tmpfs')}</option>
              </SelectField>
            )}
            <SelectField
              label={t('storage.access_mode')}
              hint={t('storage.access_mode_hint')}
              value={mode}
              onChange={(e) => setMode(e.target.value as typeof mode)}
            >
              <option value="single_writer">{t('storage.mode.single_writer')}</option>
              <option value="shared">{t('storage.mode.shared')}</option>
            </SelectField>
            <TextField
              label={t('storage.size')}
              hint={t('storage.size_hint')}
              type="number"
              inputMode="decimal"
              error={submitted ? errors.size : null}
              value={size}
              onChange={(e) => setSize(e.target.value)}
            />
          </div>
        </Optional>
        <p className="text-sm text-muted">{t('storage.applies_next_deploy')}</p>
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

function Volumes({ serviceId, access }: { serviceId: string; access: Access }) {
  const queryClient = useQueryClient()
  const nodes = useNodes()
  const [editing, setEditing] = useState<Volume | 'new' | null>(null)
  const [deleting, setDeleting] = useState<Volume | null>(null)
  const [deleteData, setDeleteData] = useState(false)
  const volumes = useQuery({
    queryKey: ['volumes', serviceId],
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/volumes', { params: { path: { service_id: serviceId } } }),
      ),
  })
  const remove = useMutation({
    mutationFn: (v: Volume) =>
      unwrap(
        api.DELETE('/volumes/{volume_id}', {
          params: {
            path: { volume_id: v.id ?? '' },
            query: deleteData ? { delete_data: true } : {},
          },
        }),
      ),
    onSuccess: async () => {
      setDeleting(null)
      setDeleteData(false)
      await queryClient.invalidateQueries({ queryKey: ['volumes', serviceId] })
    },
  })
  const nodeName = (id?: string) => nodes.data?.find((n) => n.id === id)?.name ?? id ?? '-'
  const reason = access === 'blocked' ? t('common.needs_admin') : undefined
  const add =
    access !== 'hidden' ? (
      <Button variant="primary" reason={reason} onClick={() => setEditing('new')}>
        {t('storage.add')}
      </Button>
    ) : undefined

  let body
  if (volumes.isPending) body = <Skeleton />
  else if (volumes.isError)
    body = <ErrorBanner error={volumes.error} onRetry={() => void volumes.refetch()} />
  else if (volumes.data.length === 0)
    body = (
      <EmptyState title={t('storage.empty_title')} text={t('storage.empty_text')} action={add} />
    )
  else
    body = (
      <DataList
        caption={t('storage.caption')}
        rows={volumes.data}
        rowKey={(v) => v.id ?? v.name ?? ''}
        columns={[
          {
            header: t('storage.name'),
            primary: true,
            cell: (v) => <span className="break-all">{v.name}</span>,
          },
          {
            header: t('storage.path'),
            cell: (v) => <span className="break-all font-mono">{v.mount_path}</span>,
          },
          {
            header: t('storage.usage'),
            cell: (v) => <UsageBar used={v.usage_bytes} limit={v.size_limit_bytes} />,
          },
          {
            header: t('storage.kind'),
            cell: (v) => t(`storage.kind.${v.kind ?? 'volume'}`),
            cardHidden: true,
          },
          {
            header: t('storage.access_mode'),
            cell: (v) => t(`storage.mode.${v.access_mode ?? 'single_writer'}`),
            cardHidden: true,
          },
          { header: t('storage.node'), cell: (v) => nodeName(v.node_id), cardHidden: true },
        ]}
        actions={
          access === 'hidden'
            ? undefined
            : (v) => (
                <div className="flex flex-wrap gap-2">
                  <Button reason={reason} onClick={() => setEditing(v)}>
                    {t('common.edit')}
                  </Button>
                  <Button variant="destructive" reason={reason} onClick={() => setDeleting(v)}>
                    {t('common.delete')}
                  </Button>
                </div>
              )
        }
      />
    )

  return (
    <section className="space-y-3" aria-labelledby="volumes-h">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="volumes-h" className="text-lg font-semibold">
          {t('storage.volumes')}
        </h2>
        {volumes.data && volumes.data.length > 0 && add}
      </div>
      {body}
      {editing && (
        <VolumeDialog
          serviceId={serviceId}
          volume={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && (
        <TypedConfirmDialog
          title={t('storage.delete_title', { name: deleting.name ?? '' })}
          text={t('storage.delete_text')}
          confirmLabel={t('storage.delete_confirm', { name: deleting.name ?? '' })}
          typed={deleting.name ?? ''}
          busy={remove.isPending}
          error={
            <>
              <CheckboxField
                label={t('storage.delete_data')}
                hint={t('storage.delete_data_hint')}
                checked={deleteData}
                onChange={(e) => setDeleteData(e.target.checked)}
              />
              {remove.error && (
                <ErrorBanner error={remove.error} title={t('storage.delete_failed')} />
              )}
            </>
          }
          onConfirm={() => remove.mutate(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </section>
  )
}

// File mounts are part of the service config (REQ-VOL-006). Not for secrets.
function FileMounts({ service, access }: { service: Service; access: Access }) {
  const queryClient = useQueryClient()
  const files = (Array.isArray(service.config?.files) ? service.config.files : []) as FileMount[]
  const [editing, setEditing] = useState<{ index: number; file: FileMount } | null>(null)
  const [removing, setRemoving] = useState<number | null>(null)
  const [submitted, setSubmitted] = useState(false)
  const reason = access === 'blocked' ? t('common.needs_admin') : undefined

  const save = useMutation({
    mutationFn: (next: FileMount[]) =>
      unwrap(
        api.PATCH('/services/{service_id}', {
          params: { path: { service_id: service.id ?? '' } },
          body: { config: { ...(service.config ?? {}), files: next } },
        }),
      ),
    onSuccess: async () => {
      setEditing(null)
      setRemoving(null)
      setSubmitted(false)
      await queryClient.invalidateQueries({ queryKey: ['service', service.id] })
    },
  })

  const file = editing?.file
  const pathError =
    file && (!file.path.startsWith('/') || /\s/.test(file.path)) ? t('storage.path_invalid') : null
  const sizeError =
    file && new Blob([file.content]).size > MAX_FILE ? t('storage.file_too_big') : null

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (!editing || pathError || sizeError) return
    const next = [...files]
    if (editing.index < 0) next.push(editing.file)
    else next[editing.index] = editing.file
    save.mutate(next)
  }

  return (
    <section className="space-y-3" aria-labelledby="files-h">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="files-h" className="text-lg font-semibold">
          {t('storage.files')}
        </h2>
        {access !== 'hidden' && (
          <Button
            reason={reason}
            onClick={() => setEditing({ index: -1, file: { path: '', content: '' } })}
          >
            {t('storage.add_file')}
          </Button>
        )}
      </div>
      <p className="text-sm text-muted">{t('storage.files_hint')}</p>
      {files.length === 0 ? (
        <p className="text-muted">{t('storage.files_empty')}</p>
      ) : (
        <ul className="space-y-2">
          {files.map((f, i) => (
            <li
              key={f.path}
              className="flex flex-wrap items-center justify-between gap-2 rounded-sm border border-border p-3"
            >
              <span className="min-w-0 break-all font-mono text-sm">
                {f.path}{' '}
                <span className="text-muted">({formatBytes(new Blob([f.content]).size)})</span>
              </span>
              {access !== 'hidden' && (
                <span className="flex flex-wrap gap-2">
                  <Button reason={reason} onClick={() => setEditing({ index: i, file: f })}>
                    {t('common.edit')}
                  </Button>
                  <Button variant="destructive" reason={reason} onClick={() => setRemoving(i)}>
                    {t('common.remove')}
                  </Button>
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
      {editing && (
        <Modal
          title={editing.index < 0 ? t('storage.add_file') : t('storage.edit_file')}
          onClose={() => setEditing(null)}
        >
          <form onSubmit={submit} noValidate className="space-y-4">
            {save.error && <ErrorBanner error={save.error} title={t('storage.save_failed')} />}
            <TextField
              label={t('storage.file_path')}
              hint={t('storage.file_path_hint')}
              required
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              error={submitted ? pathError : null}
              value={editing.file.path}
              onChange={(e) =>
                setEditing({ ...editing, file: { ...editing.file, path: e.target.value } })
              }
            />
            <TextAreaField
              label={t('storage.file_content')}
              hint={t('storage.file_content_hint')}
              rows={10}
              spellCheck={false}
              error={submitted ? sizeError : null}
              value={editing.file.content}
              onChange={(e) =>
                setEditing({ ...editing, file: { ...editing.file, content: e.target.value } })
              }
            />
            <div className="flex flex-wrap justify-end gap-2">
              <Button onClick={() => setEditing(null)}>{t('common.cancel')}</Button>
              <Button type="submit" variant="primary" loading={save.isPending}>
                {t('common.save')}
              </Button>
            </div>
          </form>
        </Modal>
      )}
      {removing !== null && (
        <ConfirmDialog
          title={t('storage.remove_file_title', { path: files[removing]?.path ?? '' })}
          text={t('storage.remove_file_text')}
          confirmLabel={t('common.remove')}
          busy={save.isPending}
          error={
            save.error ? <ErrorBanner error={save.error} title={t('storage.save_failed')} /> : null
          }
          onConfirm={() => save.mutate(files.filter((_, i) => i !== removing))}
          onClose={() => setRemoving(null)}
        />
      )}
    </section>
  )
}

// Volumes left by deleted services, on every node (REQ-VOL-004). Delete needs the typed name.
function OrphanVolumes({ access }: { access: Access }) {
  const queryClient = useQueryClient()
  const nodes = useNodes()
  const [deleting, setDeleting] = useState<{ nodeId: string; name: string } | null>(null)
  const list = nodes.data ?? []
  const orphans = useQueries({
    queries: list.map((n) => ({
      queryKey: ['orphan-volumes', n.id],
      queryFn: () =>
        unwrap(
          api.GET('/nodes/{node_id}/orphan-volumes', { params: { path: { node_id: n.id ?? '' } } }),
        ),
    })),
  })
  const rows = list.flatMap((n, i) =>
    (orphans[i]?.data ?? []).map((o) => ({
      key: `${n.id}/${o.name}`,
      nodeId: n.id ?? '',
      node: n.name ?? '',
      name: o.name ?? '',
      size: o.size_bytes,
      last: o.last_service,
    })),
  )
  const remove = useMutation({
    mutationFn: (o: { nodeId: string; name: string }) =>
      unwrap(
        api.DELETE('/orphan-volumes/{node_id}/{name}', {
          params: { path: { node_id: o.nodeId, name: o.name } },
        }),
      ),
    onSuccess: async (_, o) => {
      setDeleting(null)
      await queryClient.invalidateQueries({ queryKey: ['orphan-volumes', o.nodeId] })
    },
  })
  const failed = nodes.error ?? orphans.find((q) => q.isError)?.error
  const pending = nodes.isPending || orphans.some((q) => q.isPending)

  return (
    <section className="space-y-3" aria-labelledby="orphans-h">
      <h2 id="orphans-h" className="text-lg font-semibold">
        {t('storage.orphans')}
      </h2>
      <p className="text-sm text-muted">{t('storage.orphans_hint')}</p>
      {failed && <ErrorBanner error={failed} onRetry={() => void nodes.refetch()} />}
      {pending ? (
        <Skeleton lines={2} />
      ) : rows.length === 0 ? (
        <p className="text-muted">{t('storage.orphans_empty')}</p>
      ) : (
        <DataList
          caption={t('storage.orphans')}
          rows={rows}
          rowKey={(r) => r.key}
          columns={[
            {
              header: t('storage.name'),
              primary: true,
              cell: (r) => <span className="break-all">{r.name}</span>,
            },
            { header: t('storage.node'), cell: (r) => r.node },
            { header: t('storage.usage'), cell: (r) => formatBytes(r.size) },
            { header: t('storage.last_service'), cell: (r) => r.last ?? '-' },
          ]}
          actions={
            access === 'hidden'
              ? undefined
              : (r) => (
                  <Button variant="destructive" onClick={() => setDeleting(r)}>
                    {t('common.delete')}
                  </Button>
                )
          }
        />
      )}
      {deleting && (
        <TypedConfirmDialog
          title={t('storage.orphan_delete_title', { name: deleting.name })}
          text={t('storage.orphan_delete_text')}
          confirmLabel={t('storage.delete_confirm', { name: deleting.name })}
          typed={deleting.name}
          busy={remove.isPending}
          error={
            remove.error ? (
              <ErrorBanner error={remove.error} title={t('storage.delete_failed')} />
            ) : null
          }
          onConfirm={() => remove.mutate(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </section>
  )
}

// UI-13: volumes, file mounts and orphan volumes.
export function ServiceStorage({
  service,
  environmentProtected,
}: {
  service: Service
  environmentProtected?: boolean
}) {
  const me = useMe()
  const access = accessFor(me.data?.org_role, 'volume:manage', environmentProtected)
  const orphanAccess = accessFor(me.data?.org_role, 'volume:manage', false)
  const managed = isManaged(service.git_managed_fields, 'volumes')
  return (
    <div className="space-y-8">
      {managed && <Banner>{t('settings.managed')}</Banner>}
      <Volumes serviceId={service.id ?? ''} access={managed ? 'hidden' : access} />
      <FileMounts
        service={service}
        access={accessFor(me.data?.org_role, 'service:update', environmentProtected)}
      />
      <OrphanVolumes access={orphanAccess} />
    </div>
  )
}
