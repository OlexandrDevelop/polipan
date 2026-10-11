// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react'
import type { ReactNode } from 'react'
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
  const primary = columns.find((c) => c.primary) ?? columns[0]
  const details = columns.filter((c) => c !== primary && !c.cardHidden)
  return (
    <>
      <div className="hidden overflow-x-auto sm:block">
        <table className="w-full border-collapse text-left text-sm">
          <caption className="sr-only">{caption}</caption>
          <thead>
            <tr className="bg-surface">
              {columns.map((c) => (
                <th key={c.header} scope="col" className="p-3 font-semibold text-muted">
                  {c.header}
                </th>
              ))}
              {actions && (
                <th scope="col" className="p-3 font-semibold text-muted">
                  <span className={actionsHeader ? '' : 'sr-only'}>{actionsHeader}</span>
                </th>
              )}
            </tr>
          </thead>
          <tbody>
            {visible.map((row) => (
              <tr key={rowKey(row)} className="border-b border-border align-top">
                {columns.map((c) => (
                  <td key={c.header} className="p-3">
                    {c.cell(row)}
                  </td>
                ))}
                {actions && <td className="p-3">{actions(row)}</td>}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <ul aria-label={caption} className="space-y-3 sm:hidden">
        {visible.map((row) => (
          <li key={rowKey(row)}>
            <article className="space-y-2 rounded-md border border-border bg-surface p-4">
              <div className="break-words font-semibold">{primary.cell(row)}</div>
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
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
          <span className="text-sm text-muted">
            {t('list.shown', { shown: visible.length, total: rows.length })}
          </span>
        </div>
      )}
    </>
  )
}
