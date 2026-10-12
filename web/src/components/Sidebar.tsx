// SPDX-License-Identifier: Apache-2.0

import { Link, useRouterState } from '@tanstack/react-router'
import { useState } from 'react'
import { t } from '@/i18n'

export interface SidebarLink {
  to: string
  label: string
  exact?: boolean
  // A page of the settings group.
  settings?: boolean
}

// The menu on the left of a wide screen: the pages of the workspace with a number, the settings
// pages in a group that opens, and the user at the bottom.
export function Sidebar({
  org,
  links,
  name,
  role,
  onSignOut,
  signingOut,
}: {
  org: string
  links: SidebarLink[]
  name: string
  role: string
  onSignOut: () => void
  signingOut: boolean
}) {
  const path = useRouterState({ select: (s) => s.location.pathname })
  const work = links.filter((l) => !l.settings)
  const settings = links.filter((l) => l.settings)
  const inSettings = path.includes('/settings/')
  const [open, setOpen] = useState(inSettings)
  const expanded = open || inSettings
  const initials =
    name
      .split(/[\s@._-]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((w) => w[0]?.toUpperCase())
      .join('') || '?'
  const item =
    'group relative flex min-h-10 items-center gap-3 px-5 py-1.5 text-text transition-colors duration-[200ms] hover:bg-selection/60 [&.active]:bg-selection [&.active]:font-bold [&.active]:text-primary [&.active]:before:absolute [&.active]:before:inset-y-0 [&.active]:before:left-0 [&.active]:before:w-[3px] [&.active]:before:bg-primary'
  return (
    <aside
      aria-label={t('nav.main')}
      className="font-term sticky top-14 hidden h-[calc(100vh-3.5rem)] flex-col overflow-y-auto border-r border-[var(--card-line)] bg-[var(--card-bg)] lg:col-start-1 lg:row-start-2 lg:flex"
    >
      <nav className="flex-1 py-5">
        <p className="px-5 pb-3 text-xs uppercase tracking-[0.16em] text-muted">
          {t('nav.workspace')}
        </p>
        <ul>
          {work.map((l, i) => (
            <li key={l.to}>
              <Link
                to={l.to}
                params={{ org }}
                activeOptions={l.exact ? { exact: true } : undefined}
                className={item}
              >
                <span aria-hidden="true" className="w-5 text-xs text-muted">
                  {String(i + 1).padStart(2, '0')}
                </span>
                {l.label}
              </Link>
            </li>
          ))}
        </ul>
        {settings.length > 0 && (
          <div className="mt-5 border-t border-[var(--card-line)] pt-3">
            <button
              type="button"
              aria-expanded={expanded}
              onClick={() => setOpen(!open)}
              className={`${item} w-full text-left`}
            >
              <span aria-hidden="true" className="w-5 text-xs text-muted">
                {String(work.length + 1).padStart(2, '0')}
              </span>
              {t('nav.settings')}
              <span aria-hidden="true" className="ml-auto text-muted">
                {expanded ? '▾' : '▸'}
              </span>
            </button>
            {expanded && (
              <ul className="enter">
                {settings.map((l) => (
                  <li key={l.to}>
                    <Link to={l.to} params={{ org }} className={`${item} pl-[3.25rem]`}>
                      {l.label}
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </nav>
      <div className="flex items-center gap-3 border-t border-[var(--card-line)] px-5 py-4">
        <span
          aria-hidden="true"
          className="grid size-9 shrink-0 place-items-center border border-primary text-xs font-bold text-primary"
        >
          {initials}
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm font-semibold">{name}</span>
          <span className="block truncate text-xs text-muted">{role}</span>
        </span>
        <button
          type="button"
          aria-busy={signingOut || undefined}
          onClick={onSignOut}
          className="text-xs text-muted transition-colors hover:text-text"
        >
          {t('nav.sign_out')}
        </button>
      </div>
    </aside>
  )
}
