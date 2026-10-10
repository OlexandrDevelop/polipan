// SPDX-License-Identifier: Apache-2.0

import type { ClipboardEvent } from 'react'
import { Button } from '@/components/ui'
import { looksSecret, nameProblem, parseEnvText } from '@/envText'
import { t } from '@/i18n'

export interface KeyValueRow {
  name: string
  value: string
  secret: boolean
}

// The error text of each row: bad name, reserved name or a name used twice.
export function rowErrors(rows: KeyValueRow[]): (string | null)[] {
  return rows.map((row, i) => {
    if (row.name === '' && row.value === '') return null
    const problem = nameProblem(row.name)
    if (problem === 'invalid') return t('variables.name_invalid')
    if (problem === 'reserved') return t('variables.name_reserved')
    if (rows.findIndex((r) => r.name === row.name) !== i) return t('variables.name_duplicate')
    return null
  })
}

// Rows with a name, ready to save.
export function filledRows(rows: KeyValueRow[]): KeyValueRow[] {
  return rows.filter((r) => r.name !== '' || r.value !== '')
}

const input =
  'block min-h-10 w-full rounded-sm border border-border-strong bg-bg px-3 py-1 text-base text-text coarse:min-h-11 aria-[invalid=true]:border-danger'

interface KeyValueEditorProps {
  rows: KeyValueRow[]
  onChange: (rows: KeyValueRow[]) => void
  showErrors: boolean
}

// Name and value rows with add and remove buttons. Pasting .env text into a name field adds
// one row per line (components: KeyValueEditor). A secret value is a write-only field.
export function KeyValueEditor({ rows, onChange, showErrors }: KeyValueEditorProps) {
  const errors = rowErrors(rows)
  const update = (i: number, patch: Partial<KeyValueRow>) =>
    onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)))

  function paste(i: number, event: ClipboardEvent<HTMLInputElement>) {
    const text = event.clipboardData.getData('text')
    if (!text.includes('\n') && !text.includes('=')) return
    const items = parseEnvText(text)
    if (!items || items.length === 0) return
    event.preventDefault()
    const added = items.map((it) => ({ ...it, secret: looksSecret(it.name) }))
    onChange([...rows.slice(0, i), ...added, ...rows.slice(i + 1)])
  }

  return (
    <div className="space-y-3">
      <ul className="space-y-3">
        {rows.map((row, i) => {
          const n = i + 1
          const error = showErrors ? errors[i] : null
          return (
            <li
              key={i}
              className="grid gap-2 rounded-sm border border-border p-3 sm:grid-cols-[1fr_1fr_auto_auto] sm:items-center sm:border-0 sm:p-0"
            >
              <input
                aria-label={t('variables.row_name', { n })}
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? `kv-error-${i}` : undefined}
                className={`${input} font-mono`}
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                value={row.name}
                onPaste={(e) => paste(i, e)}
                onChange={(e) => update(i, { name: e.target.value })}
              />
              <input
                aria-label={t('variables.row_value', { n })}
                type={row.secret ? 'password' : 'text'}
                autoComplete={row.secret ? 'new-password' : 'off'}
                spellCheck={false}
                className={`${input} font-mono`}
                value={row.value}
                onChange={(e) => update(i, { value: e.target.value })}
              />
              <label className="flex min-h-10 items-center gap-2 text-sm coarse:min-h-11">
                <input
                  type="checkbox"
                  className="size-4 accent-primary coarse:size-6"
                  checked={row.secret}
                  onChange={(e) => update(i, { secret: e.target.checked })}
                />
                {t('variables.secret')}
              </label>
              <Button
                variant="ghost"
                aria-label={t('variables.row_remove', { n })}
                onClick={() => onChange(rows.filter((_, j) => j !== i))}
              >
                {t('common.remove')}
              </Button>
              {error && (
                <p id={`kv-error-${i}`} className="text-sm text-danger sm:col-span-4">
                  {error}
                </p>
              )}
            </li>
          )
        })}
      </ul>
      <Button onClick={() => onChange([...rows, { name: '', value: '', secret: false }])}>
        {t('variables.add_row')}
      </Button>
    </div>
  )
}
