// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { calmNow } from '@/display'
import { Button } from '@/components/ui'
import { t } from '@/i18n'

export interface Column<T> {
  header: string
  cell: (row: T) => ReactNode
  // The title of the card on a phone. Exactly one column should have it.
  primary?: boolean
  // Not shown on the phone card (low priority).
  cardHidden?: boolean
}

interface DataListProps<T> {
  caption: string
  rows: T[]
  columns: Column<T>[]
  rowKey: (row: T) => string
  actions?: (row: T) => ReactNode
  actionsHeader?: string
}

// A long list draws this many rows at first and more on request. The page stays fast with
// 1 000 rows and never depends on a scroll position (quality, section 3; responsive, paging).
export const FIRST_ROWS = 200

// Row flash (terminal-style, section 5): a row whose data changed is marked for a short time.
// The data is in the DOM at once. The mark is only paint (an overlay that fades out; in Calm mode
// a static dashed outline for 2 seconds).
function useFlash<T>(rows: T[], rowKey: (row: T) => string): Set<string> {
  const seen = useRef<Map<string, string> | null>(null)
  const [flashed, setFlashed] = useState<Set<string>>(new Set())
  const timer = useRef<number>()
  useEffect(() => () => window.clearTimeout(timer.current), [])
  useEffect(() => {
    const next = new Map(rows.map((row) => [rowKey(row), JSON.stringify(row)]))
    const before = seen.current
    seen.current = next
    if (!before) return
    const changed = [...next].filter(([key, value]) => before.has(key) && before.get(key) !== value)
    if (changed.length === 0) return
    setFlashed((old) => new Set([...old, ...changed.map(([key]) => key)]))
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setFlashed(new Set()), calmNow() ? 2000 : 320)
  }, [rows, rowKey])
  return flashed
}

// A table from tablet width, a list of cards on a phone (REQ-UI-018).
export function DataList<T>({
  caption,
  rows,
  columns,
  rowKey,
  actions,
  actionsHeader = '',
}: DataListProps<T>) {
  const [limit, setLimit] = useState(FIRST_ROWS)
  const visible = rows.slice(0, limit)
  const flashed = useFlash(visible, rowKey)
  const primary = columns.find((c) => c.primary) ?? columns[0]
  const details = columns.filter((c) => c !== primary && !c.cardHidden)
  return (
    <>
      <div className="hidden overflow-x-auto sm:block">
        <table className="dense w-full border-collapse text-left">
          <caption className="sr-only">{caption}</caption>
          <thead>
            <tr className="bg-raised">
              {columns.map((c) => (
                <th key={c.header} scope="col" className="">
                  {c.header}
                </th>
              ))}
              {actions && (
                <th scope="col" className="">
                  <span className={actionsHeader ? '' : 'sr-only'}>{actionsHeader}</span>
                </th>
              )}
            </tr>
          </thead>
          <tbody>
            {visible.map((row) => (
              <tr
                key={rowKey(row)}
                data-flash={flashed.has(rowKey(row)) || undefined}
                className="border-b border-border align-top hover:bg-raised"
              >
                {columns.map((c) => (
                  <td key={c.header}>{c.cell(row)}</td>
                ))}
                {actions && <td>{actions(row)}</td>}
              </tr>
            ))}
          </tbody>
        </table>
        <p aria-hidden="true" className="row-hint mt-1 hidden text-muted lg:block">
          <kbd className="key">j</kbd> <kbd className="key">k</kbd> <kbd className="key">Enter</kbd>{' '}
          {t('list.keys_hint')}
        </p>
      </div>
      <ul aria-label={caption} className="space-y-3 sm:hidden">
        {visible.map((row) => (
          <li key={rowKey(row)}>
            <article
              data-cursor-row=""
              data-flash={flashed.has(rowKey(row)) || undefined}
              className="space-y-2 border border-border-strong bg-surface p-3"
            >
              <div className="break-words font-bold">{primary.cell(row)}</div>
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
                {details.map((c) => (
                  <div key={c.header} className="contents">
                    <dt className="text-muted">{c.header}</dt>
                    <dd className="min-w-0 break-words">{c.cell(row)}</dd>
                  </div>
                ))}
              </dl>
              {actions && <div className="flex flex-wrap gap-2">{actions(row)}</div>}
            </article>
          </li>
        ))}
      </ul>
      {rows.length > visible.length && (
        <div className="mt-3 flex items-center gap-3">
          <Button onClick={() => setLimit(limit + FIRST_ROWS)}>
            {t('list.show_more', { count: Math.min(FIRST_ROWS, rows.length - visible.length) })}
          </Button>
          <span className="text-muted">
            {t('list.shown', { shown: visible.length, total: rows.length })}
          </span>
        </div>
      )}
    </>
  )
}
