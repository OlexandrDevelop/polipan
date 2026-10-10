// SPDX-License-Identifier: Apache-2.0

import { useEffect, useId, useRef, useState } from 'react'
import type { KeyboardEvent, ReactNode } from 'react'
import { t } from '@/i18n'

export interface SearchItem {
  id: string
  group: string
  label: string
  detail?: string
  badges?: ReactNode
  // Shown but not runnable, with the reason (REQ-UI-004).
  reason?: string
  run: () => void
}

interface SearchDialogProps {
  label: string
  placeholder: string
  items: (query: string) => SearchItem[]
  empty: (query: string) => string
  onClose: () => void
}

// A dialog with a search box and a result list: the ARIA combobox pattern with a listbox.
// Focus stays in the input; arrows move, Enter runs, Esc closes and the focus goes back.
// Full screen on a phone. Used by the command palette and the context switcher.
export function SearchDialog({ label, placeholder, items, empty, onClose }: SearchDialogProps) {
  const ref = useRef<HTMLDialogElement>(null)
  const listId = useId()
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const results = items(query)
  const current = results[Math.min(active, results.length - 1)]

  useEffect(() => {
    const dialog = ref.current
    if (dialog && !dialog.open) dialog.showModal()
  }, [])

  useEffect(() => {
    document.getElementById(`${listId}-${active}`)?.scrollIntoView({ block: 'nearest' })
  }, [active, listId])

  function run(item: SearchItem | undefined) {
    if (!item || item.reason) return
    onClose()
    item.run()
  }

  function key(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setActive((a) => Math.min(a + 1, results.length - 1))
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setActive((a) => Math.max(a - 1, 0))
    } else if (event.key === 'Enter') {
      event.preventDefault()
      run(current)
    }
  }

  let lastGroup = ''
  return (
    <dialog
      ref={ref}
      aria-label={label}
      onClose={onClose}
      className="m-0 h-full max-h-none w-full max-w-none bg-bg p-0 text-text backdrop:bg-black/50 sm:mx-auto sm:mt-[10vh] sm:h-auto sm:max-h-[75vh] sm:max-w-[640px] sm:rounded-lg sm:border sm:border-border-strong"
    >
      <div className="flex items-center gap-2 border-b border-border p-2">
        <input
          role="combobox"
          aria-label={label}
          aria-expanded="true"
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={current ? `${listId}-${results.indexOf(current)}` : undefined}
          autoFocus
          autoComplete="off"
          spellCheck={false}
          placeholder={placeholder}
          className="min-h-11 w-full min-w-0 flex-1 bg-transparent px-2 text-base outline-none"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value)
            setActive(0)
          }}
          onKeyDown={key}
        />
        <button
          type="button"
          onClick={onClose}
          className="min-h-11 rounded-sm px-3 text-sm text-muted hover:bg-surface"
        >
          {t('common.close')}
        </button>
      </div>
      <p role="status" className="sr-only">
        {t('search.count', { count: results.length })}
      </p>
      <ul
        id={listId}
        role="listbox"
        aria-label={label}
        className="max-h-[calc(100%-4rem)] overflow-auto p-2 sm:max-h-[60vh]"
      >
        {results.length === 0 && (
          <li role="presentation" className="p-3 text-muted">
            {empty(query)}
          </li>
        )}
        {results.map((item, i) => {
          const heading = item.group !== lastGroup ? item.group : null
          lastGroup = item.group
          return (
            <li key={item.id} role="presentation">
              {heading && (
                <div
                  role="presentation"
                  className="px-2 pb-1 pt-3 text-xs font-semibold uppercase text-muted"
                >
                  {heading}
                </div>
              )}
              <div
                id={`${listId}-${i}`}
                role="option"
                aria-selected={item === current}
                aria-disabled={item.reason ? true : undefined}
                title={item.reason}
                onMouseMove={() => setActive(i)}
                onClick={() => run(item)}
                className="flex min-h-11 cursor-pointer flex-wrap items-center justify-between gap-2 rounded-sm px-3 py-2 aria-disabled:cursor-not-allowed aria-disabled:opacity-60 aria-selected:bg-surface aria-selected:outline aria-selected:outline-2 aria-selected:outline-primary"
              >
                <span className="flex min-w-0 flex-wrap items-center gap-2">
                  <span className="break-all">{item.label}</span>
                  {item.badges}
                </span>
                {(item.detail || item.reason) && (
                  <span className="text-sm text-muted">{item.reason ?? item.detail}</span>
                )}
              </div>
            </li>
          )
        })}
      </ul>
    </dialog>
  )
}

// Best match first: name starts with the text, then contains it.
export function filterItems(items: SearchItem[], query: string): SearchItem[] {
  const q = query.trim().toLowerCase()
  if (q === '') return items
  const scored: { item: SearchItem; score: number; i: number }[] = []
  items.forEach((item, i) => {
    const label = item.label.toLowerCase()
    const text = `${label} ${(item.detail ?? '').toLowerCase()}`
    const score = label.startsWith(q) ? 0 : label.includes(q) ? 1 : text.includes(q) ? 2 : -1
    if (score >= 0) scored.push({ item, score, i })
  })
  // Keep the groups together, best match first inside each group.
  const groups = [...new Set(items.map((i) => i.group))]
  return scored
    .sort(
      (a, b) =>
        groups.indexOf(a.item.group) - groups.indexOf(b.item.group) ||
        a.score - b.score ||
        a.i - b.i,
    )
    .map((s) => s.item)
}
