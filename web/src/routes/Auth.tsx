// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, Navigate, useNavigate, useSearch } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { fetchMe, useMe } from '@/auth'
import { CodeBlock } from '@/components/Copy'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { Banner, Button, Card, Skeleton, TextField } from '@/components/ui'
import { hasKey, t } from '@/i18n'
import type { MessageKey } from '@/i18n'
import { LanguageSelect } from '@/language'
import { queryClient } from '@/queryClient'

// The centered card of the login and setup pages (responsive, section 3).
function AuthCard({ title, children }: { title: string; children: ReactNode }) {
  return (
    <main id="main" tabIndex={-1} className="grid min-h-screen place-items-center p-4 outline-none">
      <div className="w-full max-w-[400px] space-y-4">
        <PageHeader title={title} />
        <Card className="space-y-4 border-primary">{children}</Card>
        <p className="text-center text-muted">
          <span aria-hidden="true">$ </span>polipan
        </p>
        <div className="flex justify-center">
          <LanguageSelect />
        </div>
      </div>
    </main>
  )
}

// After login or setup: read /me (it has the CSRF token), then open the app.
async function openSession(): Promise<void> {
  queryClient.setQueryData(['me'], await fetchMe())
}

function lockMessage(error: ApiError): string {
  if (error.retryAfter) {
    return t('login.locked_wait', { minutes: Math.max(1, Math.ceil(error.retryAfter / 60)) })
  }
  return t('login.locked')
}

type SsoLoginProvider = {
  id?: string
  name?: string
  kind?: string
  sso_required?: boolean
  break_glass?: boolean
}

// The buttons of the SSO providers. A full page load starts the login: the provider answers with a redirect.
function SsoButtons({ providers }: { providers: SsoLoginProvider[] }) {
  return (
    <div className="space-y-2">
      {providers.map((p) => (
        <a
          key={p.id}
          href={`/api/v1/auth/sso/${p.id}/start`}
          className="flex min-h-9 w-full items-center justify-center border border-border-strong px-3 py-1.5 font-medium hover:bg-raised"
        >
          {t('login.sso_with', { name: p.name ?? '' })}
        </a>
      ))}
    </div>
  )
}

export function LoginPage() {
  const search = useSearch({ from: '/login' })
  const me = useMe()
  const navigate = useNavigate()
  const providers = useQuery({
    queryKey: ['login-providers'],
    queryFn: () => unwrap(api.GET('/auth/sso/providers')) as Promise<SsoLoginProvider[]>,
  })
  const [recovery, setRecovery] = useState(false)
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [totp, setTotp] = useState('')
  // Stays on after the server asked for a code, so the field does not vanish after a wrong code.
  const [askCode, setAskCode] = useState(false)

  const login = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/auth/login', {
          body: { email, password, ...(totp.trim() ? { totp_code: totp.trim() } : {}) },
        }),
      ),
    onError: (e) => {
      if (e instanceof ApiError && e.status === 401 && e.details?.reason === 'totp_required') {
        setAskCode(true)
      }
    },
    onSuccess: async () => {
      await openSession()
      await navigate({ to: '/' })
    },
  })

  if (me.data) return <Navigate to="/" replace />

  const error = login.error
  const status = error instanceof ApiError ? error.status : 0
  // The account has 2FA: the server asks for a code (a 6-digit code or a recovery code).
  const needsCode =
    error instanceof ApiError && status === 401 && error.details?.reason === 'totp_required'
  function submit(event: FormEvent) {
    event.preventDefault()
    login.mutate()
  }

  const ssoList = providers.data ?? []
  const ssoRequired = ssoList.some((p) => p.sso_required)
  const breakGlass = ssoList.some((p) => p.break_glass)
  // An SSO-required org shows no password form. A link to the recovery login appears only when break-glass is on.
  const showPassword = !ssoRequired || recovery
  const ssoErrorKey = `login.sso_error.${search.sso_error ?? ''}`

  return (
    <AuthCard title={t('login.title')}>
      {search.expired && <Banner variant="warning">{t('login.expired')}</Banner>}
      {search.sso_error && (
        <Banner variant="danger" title={t('login.sso_error')}>
          {hasKey(ssoErrorKey) ? t(ssoErrorKey as MessageKey) : ''}
        </Banner>
      )}
      {ssoRequired && !recovery && <Banner variant="info">{t('login.sso_forced')}</Banner>}
      {ssoList.length > 0 && <SsoButtons providers={ssoList} />}
      {ssoList.length > 0 && showPassword && (
        <p className="text-center text-sm text-muted">{t('login.sso_or')}</p>
      )}
      {ssoRequired && !recovery && breakGlass && (
        <p className="text-sm text-muted">
          <button
            type="button"
            className="text-primary underline"
            onClick={() => setRecovery(true)}
          >
            {t('login.recovery')}
          </button>
        </p>
      )}
      {needsCode && <Banner variant="info">{t('login.totp_needed')}</Banner>}
      {error &&
        !needsCode &&
        (status === 401 ? (
          <Banner variant="danger">{t(askCode ? 'login.wrong_code' : 'login.wrong')}</Banner>
        ) : status === 429 && error instanceof ApiError ? (
          <Banner variant="danger" title={t('login.locked_title')}>
            {lockMessage(error)}
          </Banner>
        ) : (
          <ErrorBanner error={error} />
        ))}
      {showPassword && (
        <form onSubmit={submit} className="space-y-4">
          <TextField
            label={t('login.email')}
            type="email"
            autoComplete="username"
            inputMode="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
          <TextField
            label={t('login.password')}
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          {askCode && (
            <TextField
              label={t('login.totp_code')}
              hint={t('login.totp_hint')}
              autoComplete="one-time-code"
              autoCapitalize="none"
              spellCheck={false}
              required
              autoFocus
              value={totp}
              onChange={(e) => setTotp(e.target.value)}
            />
          )}
          <Button type="submit" variant="primary" loading={login.isPending} className="w-full">
            {t('login.submit')}
          </Button>
        </form>
      )}
      <p className="text-sm text-muted">
        <Link to="/forgot-password" className="text-primary underline">
          {t('login.forgot')}
        </Link>
      </p>
      <p className="text-sm text-muted">
        {t('login.first_time')}{' '}
        <Link to="/setup" className="text-primary underline">
          {t('login.go_setup')}
        </Link>
      </p>
    </AuthCard>
  )
}

const MIN_PASSWORD = 12

export function SetupPage() {
  // The token is read from the raw address: a token that looks like a number must stay text.
  const urlToken = new URLSearchParams(window.location.search).get('token') ?? ''
  const navigate = useNavigate()
  const token = urlToken
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [touched, setTouched] = useState(false)

  const setup = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/setup/first-user', {
          body: { setup_token: token, name, email, password },
        }),
      ),
    onSuccess: async () => {
      await openSession()
      await navigate({ to: '/' })
    },
  })

  const error = setup.error
  if (error instanceof ApiError && error.status === 404) {
    return (
      <AuthCard title={t('setup.used_title')}>
        <p>{t('setup.used_text')}</p>
        <Link to="/login" className="text-primary underline">
          {t('setup.go_login')}
        </Link>
      </AuthCard>
    )
  }

  const shortPassword = password.length < MIN_PASSWORD
  function submit(event: FormEvent) {
    event.preventDefault()
    setTouched(true)
    if (!shortPassword && token) setup.mutate()
  }

  return (
    <AuthCard title={t('setup.title')}>
      <p className="text-sm text-muted">{t('setup.intro')}</p>
      {!token && (
        <Banner variant="warning" title={t('setup.no_token_title')}>
          {t('setup.no_token')}
        </Banner>
      )}
      {error && <ErrorBanner error={error} title={t('setup.failed')} />}
      <form onSubmit={submit} className="space-y-4">
        <TextField
          label={t('setup.name')}
          autoComplete="name"
          required
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <TextField
          label={t('login.email')}
          type="email"
          autoComplete="username"
          inputMode="email"
          required
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <TextField
          label={t('login.password')}
          type="password"
          autoComplete="new-password"
          required
          hint={t('setup.password_rule', { count: password.length, min: MIN_PASSWORD })}
          error={touched && shortPassword ? t('setup.password_short', { min: MIN_PASSWORD }) : null}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <Button type="submit" variant="primary" loading={setup.isPending} className="w-full">
          {t('setup.submit')}
        </Button>
      </form>
    </AuthCard>
  )
}

// A user with an invitation link sets a name and a password. The server then signs the user in.
export function InvitePage() {
  const token = new URLSearchParams(window.location.search).get('token') ?? ''
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [touched, setTouched] = useState(false)
  const accept = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/auth/invitations/accept', { body: { token, name: name.trim(), password } }),
      ),
    onSuccess: async () => {
      await openSession()
      await navigate({ to: '/' })
    },
  })
  const short = password.length < MIN_PASSWORD
  function submit(event: FormEvent) {
    event.preventDefault()
    setTouched(true)
    if (!short && token) accept.mutate()
  }
  return (
    <AuthCard title={t('invite.title')}>
      <p className="text-sm text-muted">{t('invite.intro')}</p>
      {!token && (
        <Banner variant="warning" title={t('invite.no_token_title')}>
          {t('invite.no_token')}
        </Banner>
      )}
      {accept.error && <ErrorBanner error={accept.error} title={t('invite.failed')} />}
      <form onSubmit={submit} className="space-y-4">
        <TextField
          label={t('setup.name')}
          autoComplete="name"
          required
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <TextField
          label={t('login.password')}
          type="password"
          autoComplete="new-password"
          required
          hint={t('setup.password_rule', { count: password.length, min: MIN_PASSWORD })}
          error={touched && short ? t('setup.password_short', { min: MIN_PASSWORD }) : null}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <Button type="submit" variant="primary" loading={accept.isPending} className="w-full">
          {t('invite.submit')}
        </Button>
      </form>
    </AuthCard>
  )
}

// The page of the link "Forgot password?". There is no mail server: an admin resets it.
export function ForgotPasswordPage() {
  return (
    <AuthCard title={t('forgot.title')}>
      <p>{t('forgot.text')}</p>
      <CodeBlock text="polipan-server admin reset-password <email>" label={t('forgot.command')} />
      <p className="text-sm text-muted">{t('forgot.next')}</p>
      <Link to="/login" className="text-primary underline">
        {t('setup.go_login')}
      </Link>
    </AuthCard>
  )
}

// The admin gives the user a link with a token. The user sets a new password.
export function ResetPasswordPage() {
  const token = new URLSearchParams(window.location.search).get('token') ?? ''
  const navigate = useNavigate()
  const [password, setPassword] = useState('')
  const [touched, setTouched] = useState(false)
  const reset = useMutation({
    mutationFn: () =>
      unwrap(api.POST('/auth/password-reset/confirm', { body: { token, password } })),
    onSuccess: () => navigate({ to: '/login' }),
  })
  const short = password.length < MIN_PASSWORD
  function submit(event: FormEvent) {
    event.preventDefault()
    setTouched(true)
    if (!short && token) reset.mutate()
  }
  return (
    <AuthCard title={t('reset.title')}>
      {!token && (
        <Banner variant="warning" title={t('reset.no_token_title')}>
          {t('reset.no_token')}
        </Banner>
      )}
      {reset.error && <ErrorBanner error={reset.error} title={t('reset.failed')} />}
      <form onSubmit={submit} className="space-y-4">
        <TextField
          label={t('profile.new_password')}
          type="password"
          autoComplete="new-password"
          required
          hint={t('setup.password_rule', { count: password.length, min: MIN_PASSWORD })}
          error={touched && short ? t('setup.password_short', { min: MIN_PASSWORD }) : null}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <Button type="submit" variant="primary" loading={reset.isPending} className="w-full">
          {t('reset.submit')}
        </Button>
      </form>
    </AuthCard>
  )
}

// "/" opens the dashboard of the organization (the URL has its slug).
export function HomeRedirect() {
  const me = useMe()
  const org = useQuery({
    queryKey: ['org'],
    enabled: Boolean(me.data),
    queryFn: () => unwrap(api.GET('/org')),
  })
  if (me.isError) return <ErrorBanner error={me.error} onRetry={() => void me.refetch()} />
  if (me.data === null) return <Navigate to="/login" replace />
  if (org.data) return <Navigate to="/o/$org" params={{ org: org.data.slug ?? '' }} replace />
  if (org.isError) return <ErrorBanner error={org.error} onRetry={() => void org.refetch()} />
  return (
    <div className="p-6">
      <Skeleton />
    </div>
  )
}
