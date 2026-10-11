// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, Navigate, Outlet, useNavigate, useParams } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { api, setCsrfToken, TWO_FACTOR_KEY } from '@/api/client'
import { unwrap } from '@/api/errors'
import { sessionWasActive, useMe } from '@/auth'
import { CommandPalette } from '@/components/CommandPalette'
import { ContextSwitcher } from '@/components/ContextSwitcher'
import { NotificationsBell } from '@/components/NotificationsBell'
import { ThemeCycle } from '@/components/ThemeToggle'
import { Banner, Button, Skeleton } from '@/components/ui'
import { saveContext } from '@/context'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'
import { useLiveEvents } from '@/live'
import { NotFoundPage } from '@/routes/ErrorPages'
import { queryClient } from '@/queryClient'
import { focusSearch, ShortcutHelp, useGoKeys, useHelpKey, useSingleKey } from '@/shortcuts'
import { useTheme } from '@/theme'

export function RootLayout() {
  return (
    <>
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:left-2 focus:top-2 focus:z-50 focus:rounded-sm focus:bg-bg focus:p-2"
      >
        {t('nav.skip')}
      </a>
      <Outlet />
    </>
  )
}

const navLink =
  'flex min-h-11 flex-col items-center justify-center gap-0.5 rounded-sm px-2 py-1 text-xs hover:bg-bg lg:flex-row lg:justify-start lg:gap-2 lg:px-3 lg:text-sm [&.active]:bg-primary [&.active]:text-on-primary'

// The frame of every signed-in page: navigation, top bar and the sign-in check.
export function AppShell() {
  const { org } = useParams({ from: '/o/$org' })
  const { project, env, deployment } = useParams({ strict: false })
  const me = useMe()
  const [palette, setPalette] = useState(false)
  const userId = me.data?.user?.id
  const live = useLiveEvents(Boolean(me.data))
  const [help, setHelp] = useState(false)
  const openHelp = useCallback(() => setHelp(true), [])
  const theme = useTheme()
  const navigate = useNavigate()

  // Single-key shortcuts (REQ-UI-021). They are off while a field has the focus.
  useHelpKey(openHelp)
  useSingleKey((event) => {
    if (event.key === '/') return focusSearch()
    if (event.key === 't') {
      const order = ['system', 'light', 'dark'] as const
      theme.setChoice(order[(order.indexOf(theme.choice) + 1) % order.length])
      return true
    }
    return false
  })
  useGoKeys((letter) => {
    const pages: Record<string, string> = {
      d: '/o/$org',
      p: '/o/$org/projects',
      n: '/o/$org/nodes',
      a: '/o/$org/alerts',
      l: '/o/$org/logs',
    }
    const to = pages[letter]
    if (!to) return false
    void navigate({ to, params: { org } } as never)
    return true
  })

  // Ctrl+K or Cmd+K opens the command palette on every signed-in page (REQ-UI-019).
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPalette(true)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // The switcher remembers the last project and environment the user opened.
  useEffect(() => {
    if (project && env) saveContext(userId, { project, env })
  }, [userId, project, env])

  const orgQuery = useQuery({
    queryKey: ['org'],
    enabled: Boolean(me.data),
    queryFn: () => unwrap(api.GET('/org')),
  })
  const twoFactor = useQuery({
    queryKey: TWO_FACTOR_KEY,
    enabled: false,
    initialData: false,
    queryFn: () => false,
  })
  const canMembers = useAccess('member:read')
  const canAudit = useAccess('audit:read')
  const canOrg = useAccess('org:update')
  const canGit = useAccess('git:manage')
  const logout = useMutation({
    mutationFn: () => unwrap(api.POST('/auth/logout')),
    onSettled: () => {
      setCsrfToken('')
      queryClient.clear()
      queryClient.setQueryData(['me'], null)
      void navigate({ to: '/login' })
    },
  })

  if (me.isPending) {
    return (
      <div className="p-6">
        <Skeleton />
      </div>
    )
  }
  if (me.data === null) {
    return <Navigate to="/login" search={{ expired: sessionWasActive() || undefined }} replace />
  }
  if (twoFactor.data && !window.location.pathname.endsWith('/settings/profile')) {
    return <Navigate to="/o/$org/settings/profile" params={{ org }} replace />
  }
  const orgName = orgQuery.data?.name ?? 'Polipan'
  const wrongOrg = orgQuery.data?.slug !== undefined && orgQuery.data.slug !== org

  return (
    <div className="min-h-screen sm:flex">
      <nav
        aria-label={t('nav.main')}
        className="fixed inset-x-0 bottom-0 z-20 border-t border-border bg-surface pb-[env(safe-area-inset-bottom)] sm:sticky sm:top-0 sm:h-screen sm:w-20 sm:shrink-0 sm:border-r sm:border-t-0 lg:w-56"
      >
        <div className="hidden p-4 text-lg font-semibold sm:block">Polipan</div>
        <ul className="flex justify-around gap-1 p-1 sm:flex-col sm:justify-start sm:p-2">
          <li className="flex-1 sm:flex-none">
            <Link to="/o/$org" params={{ org }} activeOptions={{ exact: true }} className={navLink}>
              {t('nav.dashboard')}
            </Link>
          </li>
          <li className="flex-1 sm:flex-none">
            <Link to="/o/$org/projects" params={{ org }} className={navLink}>
              {t('nav.projects')}
            </Link>
          </li>
          <li className="flex-1 sm:flex-none">
            <Link to="/o/$org/deployments" params={{ org }} className={navLink}>
              {t('nav.deployments')}
            </Link>
          </li>
          <li className="flex-1 max-sm:hidden sm:flex-none">
            <Link to="/o/$org/previews" params={{ org }} className={navLink}>
              {t('nav.previews')}
            </Link>
          </li>
          <li className="flex-1 sm:flex-none">
            <Link to="/o/$org/nodes" params={{ org }} className={navLink}>
              {t('nav.nodes')}
            </Link>
          </li>
          <li className="flex-1 max-sm:hidden sm:flex-none">
            <Link to="/o/$org/catalog" params={{ org }} className={navLink}>
              {t('nav.catalog')}
            </Link>
          </li>
          <li className="flex-1 max-sm:hidden sm:flex-none">
            <Link to="/o/$org/alerts" params={{ org }} className={navLink}>
              {t('nav.alerts')}
            </Link>
          </li>
          <li className="flex-1 max-sm:hidden sm:flex-none">
            <Link to="/o/$org/logs" params={{ org }} className={navLink}>
              {t('nav.logs')}
            </Link>
          </li>
          <li className="flex-1 max-sm:hidden sm:flex-none">
            <Link to="/o/$org/backups" params={{ org }} className={navLink}>
              {t('nav.backups')}
            </Link>
          </li>
          {canMembers === 'allow' && (
            <li className="flex-1 max-sm:hidden sm:flex-none">
              <Link to="/o/$org/settings/members" params={{ org }} className={navLink}>
                {t('nav.members')}
              </Link>
            </li>
          )}
          {canMembers === 'allow' && (
            <li className="flex-1 max-sm:hidden sm:flex-none">
              <Link to="/o/$org/settings/teams" params={{ org }} className={navLink}>
                {t('nav.teams')}
              </Link>
            </li>
          )}
          {canOrg === 'allow' && (
            <li className="flex-1 max-sm:hidden sm:flex-none">
              <Link to="/o/$org/settings/organization" params={{ org }} className={navLink}>
                {t('nav.organization')}
              </Link>
            </li>
          )}
          {canGit === 'allow' && (
            <li className="flex-1 max-sm:hidden sm:flex-none">
              <Link to="/o/$org/settings/git" params={{ org }} className={navLink}>
                {t('nav.git')}
              </Link>
            </li>
          )}
          {canOrg === 'allow' && (
            <li className="flex-1 max-sm:hidden sm:flex-none">
              <Link to="/o/$org/settings/sso" params={{ org }} className={navLink}>
                {t('nav.sso')}
              </Link>
            </li>
          )}
          <li className="flex-1 max-sm:hidden sm:flex-none">
            <Link to="/o/$org/settings/tokens" params={{ org }} className={navLink}>
              {t('nav.tokens')}
            </Link>
          </li>
          {canAudit === 'allow' && (
            <li className="flex-1 max-sm:hidden sm:flex-none">
              <Link to="/o/$org/settings/audit" params={{ org }} className={navLink}>
                {t('nav.audit')}
              </Link>
            </li>
          )}
          <li className="flex-1 sm:flex-none">
            <Link to="/o/$org/settings/profile" params={{ org }} className={navLink}>
              {t('nav.profile')}
            </Link>
          </li>
        </ul>
      </nav>
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between gap-2 border-b border-border px-4 py-2 sm:px-6">
          <div className="flex min-w-0 items-center gap-2">
            <span className="hidden truncate font-medium sm:inline">{orgName}</span>
            <ContextSwitcher org={org} projectSlug={project} envSlug={env} />
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <Button
              variant="ghost"
              aria-keyshortcuts="Control+K Meta+K"
              onClick={() => setPalette(true)}
            >
              {t('palette.open')}
              <kbd className="hidden rounded-sm border border-border px-1 text-xs text-muted lg:inline">
                {t('palette.shortcut')}
              </kbd>
            </Button>
            <Button
              variant="ghost"
              aria-label={t('shortcuts.open')}
              aria-keyshortcuts="?"
              className="max-sm:hidden"
              onClick={openHelp}
            >
              ?
            </Button>
            <NotificationsBell org={org} notices={live.notices} onClear={live.clearNotices} />
            <span className="hidden sm:inline-flex">
              <ThemeCycle />
            </span>
            <Button variant="ghost" loading={logout.isPending} onClick={() => logout.mutate()}>
              {t('nav.sign_out')}
            </Button>
          </div>
        </div>
        <main
          id="main"
          tabIndex={-1}
          className="mx-auto w-full max-w-[1600px] p-4 pb-24 outline-none sm:p-6 sm:pb-6 lg:p-8"
        >
          {/* The deployment page shows its own banner for its own stream. */}
          {live.disconnected && !deployment && (
            <div className="mb-4">
              <Banner variant="warning" title={t('live.paused')}>
                {t('live.paused_text')}
              </Banner>
            </div>
          )}
          <div data-stale={live.disconnected || undefined}>
            {wrongOrg ? <NotFoundPage /> : <Outlet />}
          </div>
        </main>
        {help && <ShortcutHelp onClose={() => setHelp(false)} />}
        {palette && (
          <CommandPalette
            org={org}
            projectSlug={project}
            envSlug={env}
            onClose={() => setPalette(false)}
            onSignOut={() => logout.mutate()}
          />
        )}
      </div>
    </div>
  )
}
