// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, Navigate, Outlet, useNavigate, useParams } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { api, setCsrfToken, TWO_FACTOR_KEY } from '@/api/client'
import { unwrap } from '@/api/errors'
import { sessionWasActive, useMe } from '@/auth'
import { CommandPalette } from '@/components/CommandPalette'
import { ContextSwitcher } from '@/components/ContextSwitcher'
import { Modal } from '@/components/Modal'
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

// A polite live region says "Loaded: <title>" when the page title changes (quality, section 1).
function RouteAnnouncer() {
  const [text, setText] = useState('')
  useEffect(() => {
    const title = document.querySelector('title')
    if (!title) return
    const watch = new MutationObserver(() => setText(t('nav.loaded', { title: document.title })))
    watch.observe(title, { childList: true, characterData: true, subtree: true })
    return () => watch.disconnect()
  }, [])
  return (
    <p role="status" aria-live="polite" className="sr-only">
      {text}
    </p>
  )
}

export function RootLayout() {
  return (
    <>
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:left-2 focus:top-2 focus:z-50 focus:rounded-sm focus:bg-bg focus:p-2"
      >
        {t('nav.skip')}
      </a>
      <RouteAnnouncer />
      <Outlet />
    </>
  )
}

type NavTo =
  | '/o/$org'
  | '/o/$org/projects'
  | '/o/$org/deployments'
  | '/o/$org/previews'
  | '/o/$org/nodes'
  | '/o/$org/catalog'
  | '/o/$org/alerts'
  | '/o/$org/logs'
  | '/o/$org/backups'
  | '/o/$org/settings/members'
  | '/o/$org/settings/teams'
  | '/o/$org/settings/organization'
  | '/o/$org/settings/git'
  | '/o/$org/settings/sso'
  | '/o/$org/settings/tokens'
  | '/o/$org/settings/audit'
  | '/o/$org/settings/profile'

const navLink =
  'flex min-h-11 min-w-11 flex-col items-center justify-center gap-0.5 rounded-sm px-1 py-1 text-xs sm:px-2 hover:bg-bg lg:flex-row lg:justify-start lg:gap-2 lg:px-3 lg:text-sm [&.active]:bg-primary [&.active]:text-on-primary'

// The frame of every signed-in page: navigation, top bar and the sign-in check.
export function AppShell() {
  const { org } = useParams({ from: '/o/$org' })
  const { project, env, deployment } = useParams({ strict: false })
  const me = useMe()
  const [palette, setPalette] = useState(false)
  const userId = me.data?.user?.id
  const live = useLiveEvents(Boolean(me.data))
  const [help, setHelp] = useState(false)
  const [more, setMore] = useState(false)
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
  // "bar" links stay in the phone bottom bar. The others are in the "More" sheet.
  const links: {
    to: NavTo
    label: string
    // The short word of the phone bottom bar (responsive, navigation).
    short?: string
    show: boolean
    bar?: boolean
    exact?: boolean
  }[] = [
    {
      to: '/o/$org',
      label: t('nav.dashboard'),
      short: t('nav.home'),
      show: true,
      bar: true,
      exact: true,
    },
    { to: '/o/$org/projects', label: t('nav.projects'), show: true, bar: true },
    {
      to: '/o/$org/deployments',
      label: t('nav.deployments'),
      short: t('nav.deploys'),
      show: true,
      bar: true,
    },
    { to: '/o/$org/previews', label: t('nav.previews'), show: true },
    { to: '/o/$org/nodes', label: t('nav.nodes'), show: true, bar: true },
    { to: '/o/$org/catalog', label: t('nav.catalog'), show: true },
    { to: '/o/$org/alerts', label: t('nav.alerts'), show: true },
    { to: '/o/$org/logs', label: t('nav.logs'), show: true },
    { to: '/o/$org/backups', label: t('nav.backups'), show: true },
    { to: '/o/$org/settings/members', label: t('nav.members'), show: canMembers === 'allow' },
    { to: '/o/$org/settings/teams', label: t('nav.teams'), show: canMembers === 'allow' },
    { to: '/o/$org/settings/organization', label: t('nav.organization'), show: canOrg === 'allow' },
    { to: '/o/$org/settings/git', label: t('nav.git'), show: canGit === 'allow' },
    { to: '/o/$org/settings/sso', label: t('nav.sso'), show: canOrg === 'allow' },
    { to: '/o/$org/settings/tokens', label: t('nav.tokens'), show: true },
    { to: '/o/$org/settings/audit', label: t('nav.audit'), show: canAudit === 'allow' },
    { to: '/o/$org/settings/profile', label: t('nav.profile'), show: true },
  ]
  const orgName = orgQuery.data?.name ?? 'Polipan'
  const wrongOrg = orgQuery.data?.slug !== undefined && orgQuery.data.slug !== org

  return (
    <div className="min-h-screen sm:flex">
      <nav
        aria-label={t('nav.main')}
        className="fixed inset-x-0 bottom-0 z-20 border-t border-border bg-surface pb-[env(safe-area-inset-bottom)] sm:sticky sm:top-0 sm:h-screen sm:overflow-y-auto sm:w-24 sm:shrink-0 sm:border-r sm:border-t-0 lg:w-56"
      >
        <div className="hidden p-4 text-base font-semibold sm:block lg:text-lg">Polipan</div>
        <ul className="flex justify-around gap-1 p-1 sm:flex-col sm:justify-start sm:p-2">
          {links
            .filter((l) => l.show)
            .map((l) => (
              <li key={l.to} className={`flex-1 sm:flex-none ${l.bar ? '' : 'max-sm:hidden'}`}>
                <Link
                  to={l.to}
                  params={{ org }}
                  activeOptions={l.exact ? { exact: true } : undefined}
                  className={navLink}
                >
                  {l.short ? (
                    <>
                      <span className="sm:hidden">{l.short}</span>
                      <span className="max-sm:hidden">{l.label}</span>
                    </>
                  ) : (
                    l.label
                  )}
                </Link>
              </li>
            ))}
          <li className="flex-1 sm:hidden">
            <button
              type="button"
              aria-haspopup="dialog"
              className={`${navLink} w-full`}
              onClick={() => setMore(true)}
            >
              {t('nav.more')}
            </button>
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
        {more && (
          <Modal title={t('nav.more')} onClose={() => setMore(false)}>
            <ul className="grid gap-1">
              {links
                .filter((l) => l.show && !l.bar)
                .map((l) => (
                  <li key={l.to}>
                    <Link
                      to={l.to}
                      params={{ org }}
                      className={`${navLink} !flex-row !justify-start !gap-2 !text-base`}
                      onClick={() => setMore(false)}
                    >
                      {l.label}
                    </Link>
                  </li>
                ))}
            </ul>
          </Modal>
        )}
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
