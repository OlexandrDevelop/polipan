// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { CodeBlock } from '@/components/Copy'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog, Modal } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import { MembersTabs } from '@/components/SettingsTabs'
import { Time } from '@/components/Time'
import {
  Banner,
  Button,
  EmptyState,
  SelectField,
  Skeleton,
  StatusBadge,
  TextField,
} from '@/components/ui'
import { t, tState } from '@/i18n'
import { useAccess } from '@/permissions'

type Member = components['schemas']['Member']
type Role = components['schemas']['Role']

const ROLES: Role[] = ['owner', 'admin', 'developer', 'viewer']

export function useMembers() {
  return useQuery({
    queryKey: ['members'],
    queryFn: async () =>
      (await unwrap(api.GET('/members', { params: { query: { limit: 200 } } }))).items ?? [],
  })
}

// The user id of an open invitation starts with "inv_".
export function isInvitation(m: Member): boolean {
  return m.status === 'invited' || (m.user_id ?? '').startsWith('inv_')
}

// An error of a member change. The last owner rule has its own text.
function MemberError({ error, title }: { error: unknown; title: string }) {
  if (error instanceof ApiError && error.code === 'last_owner') {
    return (
      <Banner variant="danger" title={t('members.last_owner_title')}>
        {t('members.last_owner')}
      </Banner>
    )
  }
  return <ErrorBanner error={error} title={title} />
}

function InviteDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient()
  const me = useMe()
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<Role>('developer')
  const invite = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/members/invitations', { body: { email: email.trim(), role } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['members'] }),
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    invite.mutate()
  }
  const roles = me.data?.org_role === 'owner' ? ROLES : ROLES.filter((r) => r !== 'owner')
  return (
    <Modal title={t('members.invite_title')} onClose={onClose}>
      {invite.data ? (
        <div className="space-y-4">
          <p>{t('members.invite_done', { email: invite.data.email ?? '' })}</p>
          <CodeBlock text={invite.data.invite_url ?? ''} label={t('members.invite_url')} />
          <p className="text-sm text-muted">{t('members.invite_once')}</p>
          <div className="flex justify-end">
            <Button onClick={onClose}>{t('common.close')}</Button>
          </div>
        </div>
      ) : (
        <form onSubmit={submit} className="space-y-4">
          {invite.error && <ErrorBanner error={invite.error} title={t('members.invite_failed')} />}
          <TextField
            label={t('login.email')}
            type="email"
            required
            autoFocus
            autoComplete="off"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
          <SelectField
            label={t('members.role')}
            value={role}
            onChange={(e) => setRole(e.target.value as Role)}
          >
            {roles.map((r) => (
              <option key={r} value={r}>
                {tState('role', r)}
              </option>
            ))}
          </SelectField>
          <div className="flex flex-wrap justify-end gap-2">
            <Button onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" variant="primary" loading={invite.isPending}>
              {t('members.invite_send')}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  )
}

type Action = { kind: 'remove' | 'sessions'; member: Member }

function MembersTable({ members, canManage }: { members: Member[]; canManage: boolean }) {
  const queryClient = useQueryClient()
  const me = useMe()
  const [action, setAction] = useState<Action | null>(null)
  const refresh = () => queryClient.invalidateQueries({ queryKey: ['members'] })
  const change = useMutation({
    mutationFn: ({ id, body }: { id: string; body: { role?: Role; disabled?: boolean } }) =>
      unwrap(api.PATCH('/members/{user_id}', { params: { path: { user_id: id } }, body })),
    onSuccess: refresh,
  })
  const remove = useMutation({
    mutationFn: (id: string) =>
      unwrap(api.DELETE('/members/{user_id}', { params: { path: { user_id: id } } })),
    onSuccess: () => {
      setAction(null)
      return refresh()
    },
  })
  const sessions = useMutation({
    mutationFn: (id: string) =>
      unwrap(api.DELETE('/members/{user_id}/sessions', { params: { path: { user_id: id } } })),
    onSuccess: () => setAction(null),
  })
  const myId = me.data?.user?.id
  const isAdminOnly = me.data?.org_role === 'admin'

  return (
    <>
      {change.error && <MemberError error={change.error} title={t('members.change_failed')} />}
      <DataList
        caption={t('members.caption')}
        rows={members}
        rowKey={(m) => m.user_id ?? m.email ?? ''}
        columns={[
          {
            header: t('login.email'),
            primary: true,
            cell: (m) => (
              <span className="break-all">
                {m.email}
                {m.name ? <span className="block text-muted">{m.name}</span> : null}
              </span>
            ),
          },
          {
            header: t('members.role'),
            cell: (m) => {
              const locked = isAdminOnly && m.role === 'owner'
              if (!canManage || isInvitation(m)) return tState('role', m.role)
              return (
                <select
                  aria-label={t('members.role_of', { email: m.email ?? '' })}
                  title={locked ? t('members.admin_owner') : undefined}
                  disabled={locked}
                  value={m.role}
                  onChange={(e) =>
                    change.mutate({ id: m.user_id ?? '', body: { role: e.target.value as Role } })
                  }
                  className="min-h-8 rounded-sm border border-border-strong bg-bg px-2 text-sm coarse:min-h-11"
                >
                  {ROLES.filter((r) => r !== 'owner' || !isAdminOnly || m.role === 'owner').map(
                    (r) => (
                      <option key={r} value={r}>
                        {tState('role', r)}
                      </option>
                    ),
                  )}
                </select>
              )
            },
          },
          {
            header: t('members.status'),
            cell: (m) => (
              <StatusBadge
                tone={m.status === 'active' ? 'success' : m.status === 'invited' ? 'info' : 'muted'}
                label={tState('members.status', m.status)}
              />
            ),
          },
          {
            header: t('members.last_login'),
            cell: (m) => (m.last_login_at ? <Time value={m.last_login_at} /> : '-'),
            cardHidden: true,
          },
        ]}
        actionsHeader={canManage ? t('common.actions') : undefined}
        actions={
          canManage
            ? (m) => {
                const id = m.user_id ?? ''
                if (isInvitation(m)) {
                  return (
                    <Button
                      variant="destructive"
                      onClick={() => setAction({ kind: 'remove', member: m })}
                    >
                      {t('members.cancel_invite')}
                    </Button>
                  )
                }
                const locked = isAdminOnly && m.role === 'owner'
                const reason = locked ? t('members.admin_owner') : undefined
                return (
                  <>
                    <Button
                      reason={reason}
                      onClick={() =>
                        change.mutate({ id, body: { disabled: m.status !== 'disabled' } })
                      }
                    >
                      {m.status === 'disabled' ? t('members.enable') : t('members.disable')}
                    </Button>
                    <Button
                      reason={reason}
                      onClick={() => setAction({ kind: 'sessions', member: m })}
                    >
                      {t('members.revoke_sessions')}
                    </Button>
                    <Button
                      variant="destructive"
                      reason={reason ?? (id === myId ? t('members.remove_self') : undefined)}
                      onClick={() => setAction({ kind: 'remove', member: m })}
                    >
                      {t('common.remove')}
                    </Button>
                  </>
                )
              }
            : undefined
        }
      />
      {action?.kind === 'remove' && (
        <ConfirmDialog
          title={
            isInvitation(action.member) ? t('members.cancel_invite') : t('members.remove_title')
          }
          text={t(
            isInvitation(action.member) ? 'members.cancel_invite_text' : 'members.remove_text',
            {
              email: action.member.email ?? '',
            },
          )}
          confirmLabel={
            isInvitation(action.member) ? t('members.cancel_invite') : t('common.remove')
          }
          busy={remove.isPending}
          error={
            remove.error && <MemberError error={remove.error} title={t('members.remove_failed')} />
          }
          onConfirm={() => remove.mutate(action.member.user_id ?? '')}
          onClose={() => {
            remove.reset()
            setAction(null)
          }}
        />
      )}
      {action?.kind === 'sessions' && (
        <ConfirmDialog
          title={t('members.revoke_sessions')}
          text={t('members.revoke_sessions_text', { email: action.member.email ?? '' })}
          confirmLabel={t('members.revoke_sessions')}
          busy={sessions.isPending}
          error={sessions.error && <ErrorBanner error={sessions.error} />}
          onConfirm={() => sessions.mutate(action.member.user_id ?? '')}
          onClose={() => {
            sessions.reset()
            setAction(null)
          }}
        />
      )}
    </>
  )
}

// UI-23 and UI-45: members and open invitations.
export function MembersPage() {
  const members = useMembers()
  const manage = useAccess('member:manage')
  const [inviting, setInviting] = useState(false)
  const canManage = manage === 'allow'
  return (
    <section className="max-w-5xl">
      <PageHeader
        title={t('members.title')}
        actions={
          canManage && (
            <Button variant="primary" onClick={() => setInviting(true)}>
              {t('members.invite')}
            </Button>
          )
        }
      />
      <MembersTabs current="members" />
      {members.isPending && <Skeleton />}
      {members.isError && (
        <ErrorBanner error={members.error} onRetry={() => void members.refetch()} />
      )}
      {members.data &&
        (members.data.length === 0 ? (
          <EmptyState title={t('members.empty_title')} text={t('members.empty_text')} />
        ) : (
          <MembersTable members={members.data} canManage={canManage} />
        ))}
      {inviting && <InviteDialog onClose={() => setInviting(false)} />}
    </section>
  )
}
