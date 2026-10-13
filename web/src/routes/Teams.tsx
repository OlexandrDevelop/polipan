// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog, Modal } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import { MembersTabs } from '@/components/SettingsTabs'
import { Button, EmptyState, SelectField, Skeleton, TextField } from '@/components/ui'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'
import { hueOf, isInvitation, useMembers } from '@/routes/Members'

type Team = components['schemas']['Team']

export function useTeams() {
  return useQuery({ queryKey: ['teams'], queryFn: () => unwrap(api.GET('/teams')) })
}

function NameDialog({ team, onClose }: { team?: Team; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState(team?.name ?? '')
  const save = useMutation({
    mutationFn: () =>
      team
        ? unwrap(
            api.PATCH('/teams/{team_id}', {
              params: { path: { team_id: team.id ?? '' } },
              body: { name: name.trim() },
            }),
          )
        : unwrap(api.POST('/teams', { body: { name: name.trim() } })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['teams'] })
      onClose()
    },
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    if (name.trim() !== '') save.mutate()
  }
  return (
    <Modal title={team ? t('teams.rename_title') : t('teams.create_title')} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        {save.error && <ErrorBanner error={save.error} title={t('teams.save_failed')} />}
        <TextField
          label={t('teams.name')}
          required
          autoFocus
          autoComplete="off"
          value={name}
          onChange={(e) => setName(e.target.value)}
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

function TeamCard({ team, canManage }: { team: Team; canManage: boolean }) {
  const queryClient = useQueryClient()
  const members = useMembers()
  const [renaming, setRenaming] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [toAdd, setToAdd] = useState('')
  const teamId = team.id ?? ''
  const memberIds = team.member_ids ?? []
  const refresh = () => queryClient.invalidateQueries({ queryKey: ['teams'] })
  const add = useMutation({
    mutationFn: (userId: string) =>
      unwrap(
        api.PUT('/teams/{team_id}/members/{user_id}', {
          params: { path: { team_id: teamId, user_id: userId } },
        }),
      ),
    onSuccess: () => {
      setToAdd('')
      return refresh()
    },
  })
  const drop = useMutation({
    mutationFn: (userId: string) =>
      unwrap(
        api.DELETE('/teams/{team_id}/members/{user_id}', {
          params: { path: { team_id: teamId, user_id: userId } },
        }),
      ),
    onSuccess: refresh,
  })
  const del = useMutation({
    mutationFn: () =>
      unwrap(api.DELETE('/teams/{team_id}', { params: { path: { team_id: teamId } } })),
    onSuccess: refresh,
  })
  const all = (members.data ?? []).filter((m) => !isInvitation(m))
  const label = (id: string) => {
    const m = all.find((x) => x.user_id === id)
    return m ? `${m.name || m.email} (${m.email})` : id
  }
  const candidates = all.filter((m) => !memberIds.includes(m.user_id ?? ''))
  return (
    <div className="tile team">
      <div className="team-top">
        <h2 className="tile-name">{team.name}</h2>
        {canManage && (
          <details className="more more-inline">
            <summary className="more-toggle">{t('service.more')}</summary>
            <div className="more-menu">
              <Button variant="ghost" onClick={() => setRenaming(true)}>
                {t('teams.rename')}
              </Button>
              <Button variant="destructive" onClick={() => setDeleting(true)}>
                {t('common.delete')}
              </Button>
            </div>
          </details>
        )}
      </div>
      {(add.error || drop.error) && (
        <ErrorBanner error={add.error ?? drop.error} title={t('teams.change_failed')} />
      )}
      {memberIds.length === 0 ? (
        <p className="text-sm text-muted">{t('teams.no_members')}</p>
      ) : (
        <ul className="team-members">
          {memberIds.map((id) => (
            <li key={id} className="team-chip">
              <span aria-hidden="true" className="tile-letter avatar" data-hue={hueOf(label(id))}>
                {label(id).trim().charAt(0).toUpperCase()}
              </span>
              <span className="team-chip-name">{label(id)}</span>
              {canManage && (
                <button
                  type="button"
                  className="team-chip-x"
                  onClick={() => drop.mutate(id)}
                  aria-label={t('teams.remove_member', { name: label(id) })}
                >
                  ×
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {canManage && candidates.length > 0 && (
        <div className="flex flex-wrap items-end gap-2">
          <div className="min-w-48 flex-1">
            <SelectField
              label={t('teams.add_member')}
              value={toAdd}
              onChange={(e) => setToAdd(e.target.value)}
            >
              <option value="">{t('teams.choose_member')}</option>
              {candidates.map((m) => (
                <option key={m.user_id} value={m.user_id}>
                  {m.name || m.email} ({m.email})
                </option>
              ))}
            </SelectField>
          </div>
          <Button
            variant="primary"
            loading={add.isPending}
            reason={toAdd ? undefined : t('teams.choose_first')}
            onClick={() => add.mutate(toAdd)}
          >
            {t('teams.add')}
          </Button>
        </div>
      )}
      {renaming && <NameDialog team={team} onClose={() => setRenaming(false)} />}
      {deleting && (
        <ConfirmDialog
          title={t('teams.delete_title')}
          text={t('teams.delete_text', { name: team.name ?? '' })}
          confirmLabel={t('common.delete')}
          busy={del.isPending}
          error={del.error && <ErrorBanner error={del.error} />}
          onConfirm={() => del.mutate()}
          onClose={() => setDeleting(false)}
        />
      )}
    </div>
  )
}

// UI-44: teams and their members.
export function TeamsPage() {
  const teams = useTeams()
  const manage = useAccess('team:manage')
  const [creating, setCreating] = useState(false)
  const canManage = manage === 'allow'
  return (
    <section>
      <PageHeader
        title={t('teams.title')}
        actions={
          canManage && (
            <Button variant="primary" onClick={() => setCreating(true)}>
              {t('teams.create')}
            </Button>
          )
        }
      />
      <MembersTabs current="teams" />
      {teams.isPending && <Skeleton />}
      {teams.isError && <ErrorBanner error={teams.error} onRetry={() => void teams.refetch()} />}
      {teams.data &&
        (teams.data.length === 0 ? (
          <EmptyState
            title={t('teams.empty_title')}
            text={t('teams.empty_text')}
            action={
              canManage ? (
                <Button variant="primary" onClick={() => setCreating(true)}>
                  {t('teams.create')}
                </Button>
              ) : undefined
            }
          />
        ) : (
          <ul className="tiles tiles-wide">
            {teams.data.map((team) => (
              <li key={team.id}>
                <TeamCard team={team} canManage={canManage} />
              </li>
            ))}
          </ul>
        ))}
      {creating && <NameDialog onClose={() => setCreating(false)} />}
    </section>
  )
}
