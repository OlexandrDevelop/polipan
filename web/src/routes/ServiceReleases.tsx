// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { RollbackDialog } from '@/components/RollbackDialog'
import { Time } from '@/components/Time'
import {
  Button,
  CheckboxField,
  EmptyState,
  Skeleton,
  StatusBadge,
  TextAreaField,
} from '@/components/ui'
import { t, tState } from '@/i18n'
import { accessFor } from '@/permissions'
import type { Environment, Service } from '@/scope'
import type { Tone } from '@/status'
import type { components } from '@api/schema'

type Release = components['schemas']['Release']

const releaseTone: Record<string, Tone> = {
  live: 'success',
  previous: 'info',
  available: 'muted',
  pruned: 'warning',
}

export function useReleases(serviceId: string) {
  return useQuery({
    queryKey: ['releases', serviceId],
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/releases', {
          params: { path: { service_id: serviceId }, query: { limit: 50 } },
        }),
      ),
  })
}

function NoteDialog({ release, onClose }: { release: Release; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [note, setNote] = useState(release.note ?? '')
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/releases/{release_id}', {
          params: { path: { release_id: release.id ?? '' } },
          body: { note: note.trim() },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['releases'] })
      onClose()
    },
  })
  return (
    <Modal title={t('releases.note_title', { number: release.number ?? '' })} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate()
        }}
      >
        {save.error && <ErrorBanner error={save.error} title={t('releases.note_failed')} />}
        <TextAreaField
          label={t('releases.note')}
          hint={t('releases.note_hint')}
          autoFocus
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
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

// UI-09: the release history, the rollback and the note of each release.
export function ServiceReleases({
  service,
  environment,
  base,
  onDeploy,
}: {
  service: Service
  environment: Environment
  base: { org: string; project: string; env: string; service: string }
  onDeploy: () => void
}) {
  const me = useMe()
  const navigate = useNavigate()
  const releases = useReleases(service.id ?? '')
  const [rollbackId, setRollbackId] = useState<string | null>(null)
  const [noting, setNoting] = useState<Release | null>(null)
  const [picked, setPicked] = useState<string[]>([])
  const role = me.data?.org_role
  const rollbackAccess = accessFor(role, 'service:rollback', environment.protected)
  const noteAccess = accessFor(role, 'service:update', environment.protected)

  if (releases.isPending) return <Skeleton />
  if (releases.isError) {
    return <ErrorBanner error={releases.error} onRetry={() => void releases.refetch()} />
  }
  const rows = releases.data.items ?? []
  if (rows.length === 0) {
    return <EmptyState title={t('releases.empty_title')} text={t('releases.empty_text')} />
  }

  const toggle = (id: string, on: boolean) =>
    setPicked((list) => (on ? [...list, id].slice(-2) : list.filter((x) => x !== id)))
  const compare = () => {
    // The older release (lower number) is on the left.
    const [a, b] = picked
      .map((id) => rows.find((r) => r.id === id))
      .sort((x, y) => (x?.number ?? 0) - (y?.number ?? 0))
    if (a?.id && b?.id) {
      void navigate({
        to: '/o/$org/p/$project/e/$env/s/$service/releases/$release/diff/$other',
        params: { ...base, release: a.id, other: b.id },
      })
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <Button reason={picked.length === 2 ? undefined : t('releases.pick_two')} onClick={compare}>
          {t('releases.compare')}
        </Button>
      </div>
      <DataList
        caption={t('releases.caption')}
        rows={rows}
        rowKey={(r) => r.id ?? ''}
        columns={[
          {
            header: t('releases.number'),
            primary: true,
            cell: (r) => `v${r.number ?? ''}`,
          },
          {
            header: t('releases.commit'),
            cell: (r) =>
              r.source?.commit_sha ? (
                <span className="font-mono" title={r.source.commit_sha}>
                  {r.source.commit_sha.slice(0, 7)}
                </span>
              ) : (
                (r.image_ref ?? '-')
              ),
          },
          { header: t('releases.author'), cell: (r) => r.created_by ?? '-' },
          { header: t('releases.time'), cell: (r) => <Time value={r.created_at} /> },
          {
            header: t('releases.status'),
            cell: (r) => (
              <span className="flex flex-wrap gap-2">
                <StatusBadge
                  tone={releaseTone[r.status ?? ''] ?? 'muted'}
                  label={tState('status.release', r.status)}
                />
                {r.status !== 'live' && r.status !== 'pruned' && (
                  <StatusBadge
                    tone={r.fast_rollback_possible ? 'info' : 'muted'}
                    label={r.fast_rollback_possible ? t('releases.fast') : t('releases.rebuild')}
                  />
                )}
              </span>
            ),
          },
          { header: t('releases.note'), cell: (r) => r.note || '-', cardHidden: true },
        ]}
        actionsHeader={t('common.actions')}
        actions={(r) => (
          <>
            {rollbackAccess !== 'hidden' && r.status !== 'live' && (
              <Button
                reason={
                  rollbackAccess === 'blocked'
                    ? t('common.needs_admin')
                    : r.status === 'pruned'
                      ? t('rollback.pruned_reason')
                      : undefined
                }
                onClick={() => setRollbackId(r.id ?? '')}
              >
                {t('releases.rollback')}
              </Button>
            )}
            {r.status === 'pruned' && rollbackAccess !== 'hidden' && (
              <Button onClick={onDeploy}>{t('rollback.deploy_instead')}</Button>
            )}
            {noteAccess !== 'hidden' && (
              <Button
                reason={noteAccess === 'blocked' ? t('common.needs_admin') : undefined}
                onClick={() => setNoting(r)}
              >
                {t('releases.edit_note')}
              </Button>
            )}
            <CheckboxField
              label={t('releases.pick', { number: r.number ?? '' })}
              checked={picked.includes(r.id ?? '')}
              onChange={(e) => toggle(r.id ?? '', e.target.checked)}
            />
          </>
        )}
      />
      {rollbackId && (
        <RollbackDialog
          service={service}
          environment={environment}
          releaseId={rollbackId}
          onClose={() => setRollbackId(null)}
          onDeployInstead={() => {
            setRollbackId(null)
            onDeploy()
          }}
        />
      )}
      {noting && <NoteDialog release={noting} onClose={() => setNoting(null)} />}
    </div>
  )
}
