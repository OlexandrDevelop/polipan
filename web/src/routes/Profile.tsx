// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { ThemeRadios } from '@/components/ThemeToggle'
import { Time } from '@/components/Time'
import { Banner, Button, Card, Skeleton, StatusBadge, TextField } from '@/components/ui'
import { t } from '@/i18n'

const MIN_PASSWORD = 12

function NameForm({ current }: { current: string }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState(current)
  const save = useMutation({
    mutationFn: () => unwrap(api.PATCH('/me', { body: { name: name.trim() } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['me'] }),
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    save.mutate()
  }
  return (
    <form onSubmit={submit} className="space-y-4">
      <h2 className="text-lg font-semibold">{t('profile.name_title')}</h2>
      {save.error && <ErrorBanner error={save.error} title={t('profile.save_failed')} />}
      {save.isSuccess && <Banner>{t('profile.saved')}</Banner>}
      <TextField
        label={t('profile.name')}
        required
        autoComplete="name"
        value={name}
        onChange={(e) => setName(e.target.value)}
      />
      <Button type="submit" variant="primary" loading={save.isPending}>
        {t('common.save')}
      </Button>
    </form>
  )
}

function PasswordForm() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [submitted, setSubmitted] = useState(false)
  const save = useMutation({
    mutationFn: () =>
      unwrap(api.PATCH('/me', { body: { current_password: current, new_password: next } })),
    onSuccess: () => {
      setCurrent('')
      setNext('')
      setSubmitted(false)
    },
  })
  const short = next.length < MIN_PASSWORD
  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (!short) save.mutate()
  }
  return (
    <form onSubmit={submit} className="space-y-4">
      <h2 className="text-lg font-semibold">{t('profile.password_title')}</h2>
      {save.error && <ErrorBanner error={save.error} title={t('profile.password_failed')} />}
      {save.isSuccess && <Banner>{t('profile.password_saved')}</Banner>}
      <TextField
        label={t('profile.current_password')}
        type="password"
        required
        autoComplete="current-password"
        value={current}
        onChange={(e) => setCurrent(e.target.value)}
      />
      <TextField
        label={t('profile.new_password')}
        type="password"
        required
        autoComplete="new-password"
        hint={t('setup.password_rule', { count: next.length, min: MIN_PASSWORD })}
        error={submitted && short ? t('setup.password_short', { min: MIN_PASSWORD }) : null}
        value={next}
        onChange={(e) => setNext(e.target.value)}
      />
      <Button type="submit" variant="primary" loading={save.isPending}>
        {t('profile.password_save')}
      </Button>
    </form>
  )
}

function Sessions() {
  const queryClient = useQueryClient()
  const sessions = useQuery({
    queryKey: ['sessions'],
    queryFn: () => unwrap(api.GET('/me/sessions')),
  })
  const revoke = useMutation({
    mutationFn: (id: string) =>
      unwrap(api.DELETE('/me/sessions/{session_id}', { params: { path: { session_id: id } } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['sessions'] }),
  })
  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold">{t('profile.sessions_title')}</h2>
      {sessions.isPending && <Skeleton />}
      {sessions.isError && (
        <ErrorBanner error={sessions.error} onRetry={() => void sessions.refetch()} />
      )}
      {revoke.error && <ErrorBanner error={revoke.error} title={t('profile.revoke_failed')} />}
      {sessions.data && (
        <DataList
          caption={t('profile.sessions_caption')}
          rows={sessions.data}
          rowKey={(s) => s.id ?? ''}
          columns={[
            {
              header: t('profile.session_device'),
              primary: true,
              cell: (s) => (
                <span className="break-words">
                  {s.user_agent || '-'}{' '}
                  {s.current && <StatusBadge tone="info" label={t('profile.session_current')} />}
                </span>
              ),
            },
            { header: t('profile.session_ip'), cell: (s) => s.ip ?? '-' },
            { header: t('profile.session_started'), cell: (s) => <Time value={s.created_at} /> },
            {
              header: t('profile.session_expires'),
              cell: (s) => <Time value={s.expires_at} />,
              cardHidden: true,
            },
          ]}
          actionsHeader={t('common.actions')}
          actions={(s) =>
            s.current ? null : (
              <Button variant="destructive" onClick={() => revoke.mutate(s.id ?? '')}>
                {t('profile.revoke')}
              </Button>
            )
          }
        />
      )}
    </div>
  )
}

// UI-43 (M1): profile, password, sessions and theme. Two-factor sign-in comes in M5.
export function ProfilePage() {
  const me = useMe()
  return (
    <section className="max-w-3xl space-y-8">
      <PageHeader title={t('profile.title')} />
      {me.isPending && <Skeleton />}
      {me.data && (
        <>
          <Card className="space-y-1">
            <p>{me.data.user?.email}</p>
            <p className="text-sm text-muted">
              {t('profile.role', { role: me.data.org_role ?? '' })}
            </p>
          </Card>
          <NameForm key={me.data.user?.name} current={me.data.user?.name ?? ''} />
          <PasswordForm />
          <Sessions />
          <div className="space-y-2">
            <h2 className="text-lg font-semibold">{t('profile.appearance')}</h2>
            <ThemeRadios />
            <p className="text-sm text-muted">{t('profile.language')}</p>
          </div>
        </>
      )}
    </section>
  )
}
