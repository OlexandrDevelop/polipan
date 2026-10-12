// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, Navigate, Outlet, useNavigate, useParams } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { api, setCsrfToken, TWO_FACTOR_KEY } from '@/api/client'
import { unwrap } from '@/api/errors'
import { sessionWasActive, useMe } from '@/auth'
import { Boot } from '@/components/Boot'
import { CommandPalette } from '@/components/CommandPalette'
import { ContextSwitcher } from '@/components/ContextSwitcher'
import { Modal } from '@/components/Modal'
import { StatusBar } from '@/components/StatusBar'
import { Banner, KeyHint, Skeleton } from '@/components/ui'
import { readContext, saveContext } from '@/context'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'
import { useLiveEvents } from '@/live'
import { NotFoundPage } from '@/routes/ErrorPages'
import { queryClient } from '@/queryClient'
import { useCalm, useFx } from '@/display'
import {
  focusSearch,
  moveCursor,
  openCursorRow,
  ShortcutHelp,
  useGoKeys,
  useHelpKey,
  useSingleKey,
} from '@/shortcuts'
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
  'flex min-h-8 items-center justify-center gap-0.5 whitespace-nowrap px-2 py-0.5 lowercase hover:bg-raised max-sm:min-h-12 max-sm:flex-col max-sm:px-1 max-sm:text-xs coarse:min-h-11 [&.active]:bg-selection [&.active]:font-bold [&.active]:text-primary [&.active]:underline [&.active]:underline-offset-4'

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
  const calm = useCalm()
  const fx = useFx()
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
    // REQ-UI-036: vim keys and the extra switches. None of them runs a destructive action.
    switch (event.key) {
      case ':':
        setPalette(true)
        return true
      case 'c':
        calm.toggle()
        return true
      case 'x':
        if (!calm.calm) fx.cycle()
        return true
      case 'j':
        return moveCursor('next')
      case 'k':
        return moveCursor('prev')
      case 'G':
        return moveCursor('last')
      case 'Enter':
        // Enter on a focused link or button keeps its own meaning.
        return event.target === document.body && openCursorRow()
    }
    return false
  })
  useGoKeys((letter) => {
    if (letter === 'g') return moveCursor('first')
    if (letter === 's' || letter === 'e') {
      // The service list of the last environment, or the deployments of the organization.
      const last = readContext(userId)
      const to = letter === 'e' || !last ? '/o/$org/deployments' : '/o/$org/p/$project/e/$env'
      void navigate({ to, params: { org, project: last?.project, env: last?.env } } as never)
      return true
    }
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
    meta: { quiet: true },
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

  const bar = links.filter((l) => l.show)
  return (
    <div className="flex min-h-screen flex-col">
      <Boot />
      <div className="fx-scanlines" aria-hidden="true" />
      <header className="sticky top-0 z-20 flex min-h-9 items-center gap-2 border-b border-border-strong bg-surface px-3 max-sm:fixed max-sm:inset-x-0 max-sm:bottom-0 max-sm:top-auto max-sm:order-4 max-sm:min-h-14 max-sm:border-b-0 max-sm:border-t max-sm:pb-[env(safe-area-inset-bottom)] sm:order-1">
        <span aria-hidden="true" className="font-bold text-primary">
          $
        </span>
        <span className="hidden min-w-0 items-center gap-1 text-muted lg:flex">
          <span>polipan</span>
          <span aria-hidden="true">/</span>
          <span className="truncate">{org}</span>
          <span aria-hidden="true">/</span>
        </span>
        <div className="flex min-w-0 items-center">
          <ContextSwitcher org={org} projectSlug={project} envSlug={env} />
        </div>
        <button
          type="button"
          aria-keyshortcuts="Control+K Meta+K :"
          aria-label={t('palette.label')}
          onClick={() => setPalette(true)}
          className="prompt-line group flex min-h-8 min-w-0 flex-1 items-center gap-2 border border-border-strong bg-bg px-2 text-left text-muted hover:bg-raised max-sm:min-h-11"
        >
          <span aria-hidden="true">:</span>
          <span aria-hidden="true" className="truncate">
            {t('promptline.placeholder')}
          </span>
          <span aria-hidden="true" className="cursor-block" />
          <span className="ml-auto hidden sm:inline">
            <KeyHint k={t('palette.shortcut')} />
          </span>
        </button>
      </header>
      <nav
        aria-label={t('nav.main')}
        className="sticky top-9 z-10 border-b border-border bg-bg pb-[env(safe-area-inset-bottom)] max-sm:fixed max-sm:inset-x-0 max-sm:bottom-14 max-sm:top-auto max-sm:z-20 max-sm:order-3 max-sm:border-b-0 max-sm:border-t max-sm:bg-surface max-sm:pb-0 sm:order-2"
      >
        <ul className="flex gap-0 overflow-x-auto sm:px-2 max-sm:justify-around">
          {bar.map((l, i) => (
            <li key={l.to} className={`shrink-0 max-sm:flex-1 ${l.bar ? '' : 'max-sm:hidden'}`}>
              <Link
                to={l.to}
                params={{ org }}
                activeOptions={l.exact ? { exact: true } : undefined}
                className={navLink}
              >
                <span
                  aria-hidden="true"
                  className="text-muted max-sm:hidden [.active>&]:text-primary"
                >
                  {i + 1}:
                </span>
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
      <main
        id="main"
        tabIndex={-1}
        className="mx-auto w-full max-w-[1600px] flex-1 p-4 outline-none max-sm:order-2 max-sm:pb-32 sm:order-3 sm:p-6 lg:p-8"
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
      <StatusBar
        org={org}
        orgName={orgName}
        projectSlug={project}
        envSlug={env}
        disconnected={live.disconnected}
        notices={live.notices}
        onClear={live.clearNotices}
        onHelp={openHelp}
        onSignOut={() => logout.mutate()}
        signingOut={logout.isPending}
      />
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
                    className={`${navLink} !justify-start !text-base`}
                    onClick={() => setMore(false)}
                  >
                    {l.label}
                  </Link>
                </li>
              ))}
            <li className="sm:hidden">
              <button
                type="button"
                className={`${navLink} w-full !justify-start !text-base`}
                onClick={() => logout.mutate()}
              >
                {t('nav.sign_out')}
              </button>
            </li>
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
  )
}
