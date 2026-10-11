// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api, TWO_FACTOR_KEY } from '@/api/client'
import { unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { CodeBlock } from '@/components/Copy'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { ThemeRadios } from '@/components/ThemeToggle'
import { Time } from '@/components/Time'
import {
  Banner,
  Button,
  Card,
  CheckboxField,
  Skeleton,
  StatusBadge,
  TextField,
} from '@/components/ui'
import { FX_STEPS, useCalm, useFx } from '@/display'
import { t } from '@/i18n'
import type { MessageKey } from '@/i18n'
import { setSingleKeys, useSingleKeys } from '@/shortcuts'

const MIN_PASSWORD = 12

// Single-key shortcuts on or off. The choice stays on this device (UI-43).
function KeysSetting() {
  const on = useSingleKeys()
  return (
    <CheckboxField
      label={t('shortcuts.enabled')}
      hint={t('shortcuts.setting_text')}
      checked={on}
      onChange={(e) => setSingleKeys(e.target.checked)}
    />
  )
}

// UI-43, Appearance: Calm mode (REQ-UI-039) and the effects switch (REQ-UI-040).
function DisplaySettings() {
  const { calm, byOs, toggle } = useCalm()
  const { fx, cycle } = useFx()
  return (
    <div className="space-y-2">
      <CheckboxField
        label={t('statusbar.calm_label')}
        hint={byOs ? t('statusbar.calm_os') : t('profile.calm_text')}
        checked={calm}
        disabled={byOs}
        onChange={toggle}
      />
      <Button
        aria-label={t('statusbar.fx_label', {
          value: t(`fx.${fx}` as MessageKey),
          next: t(`fx.${FX_STEPS[(FX_STEPS.indexOf(fx) + 1) % FX_STEPS.length]}` as MessageKey),
        })}
        reason={calm ? t('profile.fx_calm') : undefined}
        onClick={cycle}
      >
        {t('profile.fx')}: {t(`fx.${fx}` as MessageKey)}
      </Button>
    </div>
  )
}

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

// The secret key inside an otpauth:// link, for a user who cannot scan a code.
function secretOf(uri: string): string {
  try {
    return new URL(uri).searchParams.get('secret') ?? ''
  } catch {
    return ''
  }
}

function downloadText(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
  const link = document.createElement('a')
  link.href = url
  link.download = name
  link.click()
  URL.revokeObjectURL(url)
}

// Two-factor sign-in. There is no QR library in the app, so the key is shown as text.
function TwoFactor({ enabled }: { enabled: boolean }) {
  const queryClient = useQueryClient()
  const required = useQuery({
    queryKey: TWO_FACTOR_KEY,
    enabled: false,
    initialData: false,
    queryFn: () => false,
  })
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [recovery, setRecovery] = useState<string[]>([])
  const refreshMe = () => {
    queryClient.setQueryData(TWO_FACTOR_KEY, false)
    return queryClient.invalidateQueries({ queryKey: ['me'] })
  }
  const enroll = useMutation({ mutationFn: () => unwrap(api.POST('/auth/totp/enroll')) })
  const verify = useMutation({
    mutationFn: () => unwrap(api.POST('/auth/totp/verify', { body: { totp_code: code.trim() } })),
    onSuccess: async (data) => {
      setRecovery(data.recovery_codes ?? [])
      setCode('')
      enroll.reset()
      await refreshMe()
    },
  })
  const disable = useMutation({
    mutationFn: () => unwrap(api.POST('/auth/totp/disable', { body: { password } })),
    onSuccess: () => {
      setPassword('')
      return refreshMe()
    },
  })
  const uri = enroll.data?.otpauth_uri ?? ''
  const codes = recovery.join('\n')

  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold">{t('twofa.title')}</h2>
      {required.data && !enabled && (
        <Banner variant="warning" title={t('twofa.required_title')}>
          {t('twofa.required')}
        </Banner>
      )}
      {recovery.length > 0 && (
        <div className="space-y-2">
          <Banner variant="warning" title={t('twofa.recovery_title')}>
            {t('twofa.recovery_text')}
          </Banner>
          <CodeBlock text={codes} label={t('twofa.recovery_title')} />
          <Button onClick={() => downloadText('polipan-recovery-codes.txt', codes + '\n')}>
            {t('twofa.download')}
          </Button>
          <div>
            <Button onClick={() => setRecovery([])}>{t('twofa.saved')}</Button>
          </div>
        </div>
      )}
      {enabled ? (
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault()
            disable.mutate()
          }}
        >
          <StatusBadge tone="success" label={t('twofa.on')} />
          {disable.error && <ErrorBanner error={disable.error} title={t('twofa.disable_failed')} />}
          <TextField
            label={t('twofa.password')}
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          <Button type="submit" variant="destructive" loading={disable.isPending}>
            {t('twofa.disable')}
          </Button>
        </form>
      ) : !uri ? (
        <div className="space-y-2">
          <p className="text-sm text-muted">{t('twofa.off_text')}</p>
          {enroll.error && <ErrorBanner error={enroll.error} title={t('twofa.enroll_failed')} />}
          <Button variant="primary" loading={enroll.isPending} onClick={() => enroll.mutate()}>
            {t('twofa.enroll')}
          </Button>
        </div>
      ) : (
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault()
            verify.mutate()
          }}
        >
          <p>{t('twofa.scan_text')}</p>
          {secretOf(uri) && <CodeBlock text={secretOf(uri)} label={t('twofa.key')} />}
          <p className="text-sm text-muted">{t('twofa.uri')}</p>
          <CodeBlock text={uri} label={t('twofa.uri')} />
          {verify.error && <ErrorBanner error={verify.error} title={t('twofa.verify_failed')} />}
          <TextField
            label={t('twofa.code')}
            inputMode="numeric"
            autoComplete="one-time-code"
            required
            value={code}
            onChange={(e) => setCode(e.target.value)}
          />
          <Button type="submit" variant="primary" loading={verify.isPending}>
            {t('twofa.verify')}
          </Button>
        </form>
      )}
    </div>
  )
}

// UI-43: profile, password, sessions, two-factor and theme.
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
          <TwoFactor enabled={Boolean(me.data.user?.totp_enabled)} />
          <Sessions />
          <div className="space-y-2">
            <h2 className="text-lg font-bold">{t('profile.appearance')}</h2>
            <ThemeRadios />
            <DisplaySettings />
            <KeysSetting />
            <p className="text-sm text-muted">{t('profile.language')}</p>
          </div>
        </>
      )}
    </section>
  )
}
