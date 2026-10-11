// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState, useSyncExternalStore } from 'react'
import { Modal } from '@/components/Modal'
import { t } from '@/i18n'

const STORAGE_KEY = 'polipan.shortcuts'
const CHANGE = 'polipan-shortcuts'

// Single-key shortcuts can be turned off in the profile (REQ-UI-021, UI-43). The choice stays
// on the device. Ctrl K and "?" always work.
export function singleKeysEnabled(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) !== 'off'
  } catch {
    return true
  }
}

export function setSingleKeys(on: boolean) {
  localStorage.setItem(STORAGE_KEY, on ? 'on' : 'off')
  window.dispatchEvent(new Event(CHANGE))
}

export function useSingleKeys(): boolean {
  return useSyncExternalStore(
    (notify) => {
      window.addEventListener(CHANGE, notify)
      return () => window.removeEventListener(CHANGE, notify)
    },
    singleKeysEnabled,
    () => true,
  )
}

// True when the key press belongs to a field or an open dialog, not to the page.
export function isTyping(event: KeyboardEvent): boolean {
  const target = event.target as HTMLElement | null
  return Boolean(
    target?.closest('input, textarea, select, [contenteditable], dialog') ||
    document.querySelector('dialog[open]'),
  )
}

export function isPlainKey(event: KeyboardEvent): boolean {
  return !event.ctrlKey && !event.metaKey && !event.altKey
}

// Runs the handler for a plain key when no field has the focus. Returns true when it used the key.
export function useSingleKey(handler: (event: KeyboardEvent) => boolean, enabled = true) {
  const on = useSingleKeys()
  useEffect(() => {
    if (!enabled || !on) return
    const onKey = (event: KeyboardEvent) => {
      if (!isPlainKey(event) || isTyping(event) || event.defaultPrevented) return
      if (handler(event)) event.preventDefault()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })
}

// Opens the help on "?" (also when single keys are off). Not while a field has the focus.
export function useHelpKey(open: () => void) {
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== '?' || event.ctrlKey || event.metaKey || event.altKey) return
      if (isTyping(event)) return
      event.preventDefault()
      open()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])
}

// "g" then a letter goes to a page. The second key must come within 900 ms.
export function useGoKeys(go: (letter: string) => boolean) {
  const [armed, setArmed] = useState(false)
  useEffect(() => {
    if (!armed) return
    const timer = window.setTimeout(() => setArmed(false), 900)
    return () => window.clearTimeout(timer)
  }, [armed])
  useSingleKey((event) => {
    if (armed) {
      setArmed(false)
      return go(event.key)
    }
    if (event.key === 'g') {
      setArmed(true)
      return true
    }
    return false
  })
}

const CURSOR_ROWS = 'main tbody tr, main [data-cursor-row]'

function cursorRows(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>(CURSOR_ROWS)].filter(
    (row) => row.getClientRects().length > 0 && !row.closest('[hidden]'),
  )
}

// REQ-UI-036: the cursor row. It is a visual mark on top of real links, so Tab, the mouse and
// screen readers work as in a normal table. It never runs an action by itself: Enter follows
// the first link of the row, the same as a click would.
export function moveCursor(to: 'next' | 'prev' | 'first' | 'last'): boolean {
  const rows = cursorRows()
  if (rows.length === 0) return false
  const current = rows.findIndex((row) => row.hasAttribute('data-cursor'))
  const index =
    to === 'first'
      ? 0
      : to === 'last'
        ? rows.length - 1
        : to === 'next'
          ? Math.min(current + 1, rows.length - 1)
          : Math.max(current === -1 ? 0 : current - 1, 0)
  document.querySelectorAll('[data-cursor]').forEach((row) => row.removeAttribute('data-cursor'))
  rows[index].setAttribute('data-cursor', '')
  rows[index].scrollIntoView({ block: 'nearest' })
  return true
}

export function openCursorRow(): boolean {
  const row = document.querySelector<HTMLElement>('main [data-cursor]')
  const target = row?.querySelector<HTMLElement>('a[href]')
  if (!target) return false
  target.click()
  return true
}

// Moves the focus to the search or filter box of the page.
export function focusSearch(): boolean {
  const box = document.querySelector<HTMLElement>(
    'main input[type="search"], main [data-search], main input[role="searchbox"]',
  )
  if (!box) return false
  box.focus()
  return true
}

const ROWS: { group: 'global' | 'service'; keys: string; action: string }[] = [
  { group: 'global', keys: 'Ctrl K', action: 'shortcuts.palette' },
  { group: 'global', keys: '?', action: 'shortcuts.help' },
  { group: 'global', keys: '/', action: 'shortcuts.focus_search' },
  { group: 'global', keys: 'g d, g p, g n, g a, g l, g s, g e', action: 'shortcuts.go' },
  { group: 'global', keys: 't', action: 'shortcuts.theme' },
  { group: 'global', keys: ':', action: 'shortcuts.colon_action' },
  { group: 'global', keys: 'c', action: 'shortcuts.calm_action' },
  { group: 'global', keys: 'x', action: 'shortcuts.fx_action' },
  { group: 'global', keys: 'j, k, Enter, g g, G', action: 'shortcuts.vim_action' },
  { group: 'global', keys: 'Esc', action: 'shortcuts.close' },
  { group: 'service', keys: 'd', action: 'shortcuts.deploy' },
  { group: 'service', keys: 'r', action: 'shortcuts.releases' },
  { group: 'service', keys: 'l', action: 'shortcuts.logs' },
  { group: 'service', keys: '[ ]', action: 'shortcuts.tabs' },
]

// UI-42: the help overlay that lists every shortcut.
export function ShortcutHelp({ onClose }: { onClose: () => void }) {
  return (
    <Modal title={t('shortcuts.title')} onClose={onClose}>
      <p className="mb-3 text-sm text-muted">{t('shortcuts.intro')}</p>
      {(['global', 'service'] as const).map((group) => (
        <table key={group} className="mb-4 w-full border-collapse text-left text-sm">
          <caption className="mb-1 text-left font-semibold">
            {t(`shortcuts.group.${group}`)}
          </caption>
          <thead className="sr-only">
            <tr>
              <th scope="col">{t('shortcuts.keys')}</th>
              <th scope="col">{t('shortcuts.action')}</th>
            </tr>
          </thead>
          <tbody>
            {ROWS.filter((r) => r.group === group).map((r) => (
              <tr key={r.keys} className="border-b border-border">
                <td className="py-2 pr-3 align-top">
                  <kbd className="key">{r.keys}</kbd>
                </td>
                <td className="py-2">{t(r.action as 'shortcuts.palette')}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ))}
    </Modal>
  )
}
