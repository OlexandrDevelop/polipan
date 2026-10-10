// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, Navigate, useNavigate, useSearch } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { fetchMe, useMe } from '@/auth'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { Banner, Button, Card, Skeleton, TextField } from '@/components/ui'
import { t } from '@/i18n'
import { queryClient } from '@/queryClient'

// The centered card of the login and setup pages (responsive, section 3).
function AuthCard({ title, children }: { title: string; children: ReactNode }) {
  return (
    <main id="main" tabIndex={-1} className="grid min-h-screen place-items-center p-4 outline-none">
      <div className="w-full max-w-[400px] space-y-4">
        <PageHeader title={title} />
        <Card className="space-y-4">{children}</Card>
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

export function LoginPage() {
  const search = useSearch({ from: '/login' })
  const me = useMe()
  const navigate = useNavigate()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')

  const login = useMutation({
    mutationFn: () => unwrap(api.POST('/auth/login', { body: { email, password } })),
    onSuccess: async () => {
      await openSession()
      await navigate({ to: '/' })
    },
  })

  if (me.data) return <Navigate to="/" replace />

  const error = login.error
  const status = error instanceof ApiError ? error.status : 0
  function submit(event: FormEvent) {
    event.preventDefault()
    login.mutate()
  }

  return (
    <AuthCard title={t('login.title')}>
      {search.expired && <Banner variant="warning">{t('login.expired')}</Banner>}
      {error &&
        (status === 401 ? (
          <Banner variant="danger">{t('login.wrong')}</Banner>
        ) : status === 429 && error instanceof ApiError ? (
          <Banner variant="danger" title={t('login.locked_title')}>
            {lockMessage(error)}
          </Banner>
        ) : (
          <ErrorBanner error={error} />
        ))}
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
        <Button type="submit" variant="primary" loading={login.isPending} className="w-full">
          {t('login.submit')}
        </Button>
      </form>
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
