// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react'
import { useMe } from '@/auth'
import { NotificationsBell } from '@/components/NotificationsBell'
import { EnvironmentBadges } from '@/components/ui'
import { readContext } from '@/context'
import { useCalm, useFx } from '@/display'
import { t } from '@/i18n'
import { LanguageSelect } from '@/language'
import type { MessageKey } from '@/i18n'
import type { Notice } from '@/live'
import { useEnvironments, useNodes, useProjects } from '@/scope'
import { useTheme } from '@/theme'

// The clock is plain text in a time element. It is not a live region, so it is never announced.
function Clock() {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const id = window.setInterval(() => setNow(new Date()), 1000)
    return () => window.clearInterval(id)
  }, [])
  const text = now.toISOString().slice(11, 19)
  return <time dateTime={now.toISOString()}>{text}</time>
}

const bracket =
  'inline-flex min-h-6 items-center justify-center whitespace-nowrap px-1 text-muted hover:bg-selection hover:text-text max-sm:min-h-11 max-sm:min-w-11 coarse:min-h-11 coarse:px-2'

interface StatusBarProps {
  org: string
  orgName: string
  projectSlug?: string
  envSlug?: string
  disconnected: boolean
  notices: Notice[]
  onClear: () => void
  onHelp: () => void
  onSignOut: () => void
  signingOut: boolean
}

// REQ-UI-035: org, environment, live state, node health, user, clock, theme, Calm, effects and
// help. It is the footer of the page (the header on a phone).
export function StatusBar({
  org,
  orgName,
  projectSlug,
  envSlug,
  disconnected,
  notices,
  onClear,
  onHelp,
  onSignOut,
  signingOut,
}: StatusBarProps) {
  const me = useMe()
  const theme = useTheme()
  const { calm, byOs, toggle: toggleCalm } = useCalm()
  const { fx, cycle: cycleFx } = useFx()
  const nodes = useNodes()
  const projects = useProjects()
  const last = readContext(me.data?.user?.id)
  const project = projects.data?.find((p) => p.slug === (projectSlug ?? last?.project))
  const envs = useEnvironments(project?.id)
  const env = envs.data?.find((e) => e.slug === (projectSlug ? envSlug : last?.env))
  const total = nodes.data?.length ?? 0
  const ready = nodes.data?.filter((n) => n.state === 'ready').length ?? 0
  const order = ['system', 'light', 'dark'] as const
  const nextTheme = order[(order.indexOf(theme.choice) + 1) % order.length]
  const nextFx =
    fx === 'off' ? 'scanlines' : fx === 'scanlines' ? 'glow' : fx === 'glow' ? 'curve' : 'off'
  const themeWord = t(`theme.${theme.choice}` as MessageKey)
  const name = me.data?.user?.name ?? me.data?.user?.email ?? ''
  return (
    <footer
      aria-label={t('statusbar.label')}
      data-production={env?.kind === 'production' || undefined}
      className="sticky z-20 flex flex-wrap items-center justify-between gap-x-3 gap-y-0 border-t border-border-strong bg-raised px-3 py-0.5 max-sm:top-0 max-sm:order-1 max-sm:border-b max-sm:border-t-0 sm:bottom-0 sm:order-4 sm:min-h-7"
    >
      <div className="flex min-w-0 flex-wrap items-center gap-x-3">
        <span className="border border-border-strong px-1" title={t('statusbar.org')}>
          {orgName}
        </span>
        {env && (
          <span className="inline-flex items-center gap-1">
            <EnvironmentBadges kind={env.kind} isProtected={env.protected} />
          </span>
        )}
        <span className={disconnected ? 'text-warning' : 'text-success'} data-testid="live-state">
          <span
            aria-hidden="true"
            className="live-dot mr-1"
            data-state={disconnected ? 'reconnecting' : 'live'}
          />
          {disconnected ? `▲ ${t('statusbar.reconnecting')}` : t('statusbar.live')}
        </span>
        <span
          className="max-sm:hidden"
          role="img"
          aria-label={t('statusbar.nodes_label', { ready, total })}
        >
          <span aria-hidden="true" className={ready < total ? 'text-warning' : 'text-success'}>
            ●
          </span>{' '}
          <span aria-hidden="true">{t('statusbar.nodes', { ready, total })}</span>
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-x-1">
        <span
          className="hidden truncate text-muted sm:inline"
          title={t('statusbar.user', { name })}
        >
          {name}
        </span>
        <span className="hidden text-muted sm:inline">
          <Clock />
        </span>
        <NotificationsBell org={org} notices={notices} onClear={onClear} />
        <button
          type="button"
          className={bracket}
          aria-label={t('theme.cycle', {
            current: themeWord,
            next: t(`theme.${nextTheme}` as MessageKey),
          })}
          onClick={() => theme.setChoice(nextTheme)}
        >
          [{t('statusbar.theme', { value: theme.choice })}]
        </button>
        <LanguageSelect className="max-sm:hidden" />
        <button
          type="button"
          className={bracket}
          aria-pressed={calm}
          aria-label={t('statusbar.calm_label')}
          title={byOs ? t('statusbar.calm_os') : undefined}
          onClick={toggleCalm}
        >
          [{t('statusbar.calm', { value: calm ? 'on' : 'off' })}]
        </button>
        <button
          type="button"
          className={bracket}
          aria-label={t('statusbar.fx_label', {
            value: t(`fx.${fx}` as MessageKey),
            next: t(`fx.${nextFx}` as MessageKey),
          })}
          disabled={calm}
          onClick={cycleFx}
        >
          [{t('statusbar.fx', { value: fx })}]
        </button>
        <button
          type="button"
          className={`${bracket} max-sm:hidden`}
          aria-label={t('shortcuts.open')}
          aria-keyshortcuts="?"
          onClick={onHelp}
        >
          [?]
        </button>
        <button
          type="button"
          className={`${bracket} max-sm:hidden`}
          aria-busy={signingOut || undefined}
          onClick={onSignOut}
        >
          {t('nav.sign_out')}
        </button>
      </div>
    </footer>
  )
}
