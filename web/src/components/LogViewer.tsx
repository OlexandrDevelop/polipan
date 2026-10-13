// SPDX-License-Identifier: Apache-2.0

import { memo, useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent, ReactNode } from 'react'
import { copyText } from '@/components/Copy'
import { useCalm } from '@/display'
import { Button, CheckboxField } from '@/components/ui'
import { formatTime, formatDateTime, t } from '@/i18n'
import { downloadText, hasMatch, highlight, lineText, parseAnsi, plainText } from '@/logText'
import type { AnsiTone, LogEntry } from '@/logText'

// Only this many lines are drawn at once. The viewer keeps more in memory (MAX_LINES) and
// the user can draw more with a button. This keeps the page fast with 10 000 lines.
const PAGE = 500

const toneClass: Record<AnsiTone, string> = {
  danger: 'text-danger',
  success: 'text-success',
  warning: 'text-warning',
  info: 'text-info',
  primary: 'text-primary',
  muted: 'text-muted',
}

// A level has an icon and a word, so it is clear without color.
function Level({ level }: { level?: string }) {
  const name = level || 'unknown'
  const shape =
    name === 'error'
      ? '✕'
      : name === 'warn'
        ? '▲'
        : name === 'info'
          ? '●'
          : name === 'debug'
            ? '◇'
            : '□'
  const color = name === 'error' ? 'text-danger' : name === 'warn' ? 'text-warning' : 'text-muted'
  return (
    <span className={`inline-flex w-16 shrink-0 items-center gap-1 text-xs ${color}`}>
      <span aria-hidden="true">{shape}</span>
      {t(`logs.level.${name as 'error'}`)}
    </span>
  )
}

function Message({ entry, query }: { entry: LogEntry; query: string }) {
  // Every part is drawn as text by React, so HTML in a log line stays text.
  const parts = useMemo(() => highlight(parseAnsi(entry.message), query), [entry.message, query])
  return (
    <>
      {parts.map((p, i) => (
        <span
          key={i}
          className={`${p.tone ? toneClass[p.tone] : ''} ${p.bold ? 'font-bold' : ''} ${
            p.mark ? 'bg-warning/30 underline decoration-2 underline-offset-2' : ''
          }`}
        >
          {p.text}
        </span>
      ))}
    </>
  )
}

interface LogRowProps {
  l: LogEntry
  query: string
  wrap: boolean
  showService?: boolean
  showReplica?: boolean
  // The newest live line is revealed by a cover that slides away (transform only).
  fresh?: boolean
  onContext?: (entry: LogEntry) => void
  onCopy: (entry: LogEntry) => void
}

// One log line. It is memoized, so a new line draws one row, not the whole list.
const LogRow = memo(function LogRow({
  l,
  query,
  wrap,
  showService,
  showReplica,
  fresh,
  onContext,
  onCopy,
}: LogRowProps) {
  return (
    <div
      data-line=""
      data-id={l.id}
      tabIndex={-1}
      className={`group flex gap-2 px-2 py-0.5 focus:bg-bg focus:outline focus:outline-2 focus:-outline-offset-2 focus:outline-primary ${
        l.match === false ? 'text-muted' : ''
      }`}
    >
      {l.time && (
        <time
          dateTime={l.time}
          title={new Date(l.time).toISOString()}
          className="shrink-0 text-log-time max-sm:hidden"
        >
          {formatTime(l.time)}
        </time>
      )}
      {showService && l.service && <span className="shrink-0">{l.service}</span>}
      {showReplica && l.replica !== undefined && (
        <span className="shrink-0 text-muted">
          {l.node ? `${l.node}/` : ''}#{l.replica}
        </span>
      )}
      <Level level={l.level} />
      {l.stream === 'stderr' && <span className="shrink-0 text-danger">{t('logs.stderr')}</span>}
      {l.match === false && <span className="sr-only">{t('logs.context_line')}</span>}
      <span
        className={`min-w-0 flex-1 ${wrap ? 'whitespace-pre-wrap break-words' : ''} ${fresh ? 'typed' : ''}`}
        // Up to 4 characters per frame (16 ms).
        style={
          fresh
            ? {
                animationDuration: `${Math.min(400, Math.max(100, (l.message.length / 4) * 16))}ms`,
              }
            : undefined
        }
      >
        <Message entry={l} query={query} />
      </span>
      <span className="flex shrink-0 gap-1 opacity-0 focus-within:opacity-100 group-focus:opacity-100 group-hover:opacity-100 coarse:opacity-100">
        {onContext && l.match !== false && (
          <button type="button" tabIndex={-1} onClick={() => onContext(l)} className="log-btn">
            {t('logs.context')}
          </button>
        )}
        <button
          type="button"
          tabIndex={-1}
          onClick={() => onCopy(l)}
          aria-label={t('logs.copy_line')}
          className="log-btn"
        >
          {t('common.copy')}
        </button>
      </span>
    </div>
  )
})

interface LogViewerProps {
  lines: LogEntry[]
  label: string
  // live: oldest first, follows the end. search: newest first, with older results below.
  mode: 'live' | 'search'
  follow: boolean
  onFollowChange: (follow: boolean) => void
  wrap: boolean
  onWrapChange: (wrap: boolean) => void
  // The text to highlight.
  query: string
  showReplica?: boolean
  showService?: boolean
  // New lines that wait while the viewer is paused.
  pending?: number
  fileName: string
  empty: ReactNode
  onLoadOlder?: () => void
  loadingOlder?: boolean
  onContext?: (entry: LogEntry) => void
  // The "/" key moves the focus to the input with this id.
  searchId?: string
}

// The viewer of UI-15 and UI-37 (components, LogViewer). Log text is data: it is drawn as text,
// never as HTML (REQ-OBS-101). The table view is the alternative for a screen reader (REQ-UI-026).
export function LogViewer({
  lines,
  label,
  mode,
  follow,
  onFollowChange,
  wrap,
  onWrapChange,
  query,
  showReplica,
  showService,
  pending = 0,
  fileName,
  empty,
  onLoadOlder,
  loadingOlder,
  onContext,
  searchId,
}: LogViewerProps) {
  const id = useId()
  const box = useRef<HTMLDivElement>(null)
  const [limit, setLimit] = useState(PAGE)
  const [table, setTable] = useState(false)
  const [announce, setAnnounce] = useState(false)
  const [announcement, setAnnouncement] = useState('')
  const [copied, setCopied] = useState('')
  const lastCount = useRef(lines.length)
  const { calm } = useCalm()
  const arrivals = useRef<number[]>([])
  const seenCount = useRef(lines.length)
  const freshId = useRef('')

  const live = mode === 'live'
  const shown = live ? lines.slice(-limit) : lines.slice(0, limit)
  const hidden = lines.length - shown.length
  // Without a search text there is nothing to match, so a busy stream does no work here.
  const matches = useMemo(
    () =>
      query.trim() === ''
        ? []
        : shown.filter((l) => l.gap === undefined && hasMatch(l, query)).map((l) => l.id),
    [shown, query],
  )
  const [matchAt, setMatchAt] = useState(-1)

  // Typewriter (section 5.1): only the newest line, only while following, and never when more
  // than 20 lines came in the last second. The text is in the DOM at once. This is worked out
  // while drawing, so a busy stream does not cost one more render per batch.
  const added = lines.length - seenCount.current
  if (added > 0) {
    seenCount.current = lines.length
    const now = performance.now()
    arrivals.current = arrivals.current.filter((at) => now - at < 1000)
    for (let i = 0; i < Math.min(added, 21); i++) arrivals.current.push(now)
    const last = lines[lines.length - 1]
    freshId.current =
      live && follow && !calm && !table && arrivals.current.length <= 20 ? (last?.id ?? '') : ''
  } else if (added < 0) {
    seenCount.current = lines.length
    freshId.current = ''
  }

  // Keep the end in view while following.
  useEffect(() => {
    const el = box.current
    if (live && follow && !table && el) el.scrollTop = el.scrollHeight
  }, [lines.length, follow, live, table])

  // A live region that does not spam: one short sentence every 5 s, only when the user asks
  // for it, and never while paused.
  useEffect(() => {
    if (!announce || !follow || !live) return
    const timer = setInterval(() => {
      const added = lines.length - lastCount.current
      lastCount.current = lines.length
      if (added > 0) setAnnouncement(t('logs.announce', { count: added }))
    }, 5_000)
    return () => clearInterval(timer)
  }, [announce, follow, live, lines.length])

  function rows(): HTMLElement[] {
    return [...(box.current?.querySelectorAll<HTMLElement>('[data-line]') ?? [])]
  }
  function focusRow(index: number) {
    const all = rows()
    all[Math.min(all.length - 1, Math.max(0, index))]?.focus()
  }
  function gotoMatch(step: number) {
    if (matches.length === 0) return
    const next = (matchAt + step + matches.length) % matches.length
    setMatchAt(next)
    if (live) onFollowChange(false)
    const row = box.current?.querySelector<HTMLElement>(`[data-id="${CSS.escape(matches[next])}"]`)
    row?.focus()
  }
  const copy = useCallback((entry: LogEntry) => {
    void copyText(lineText(entry)).then(() => setCopied(t('common.copied')))
  }, [])

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const target = event.target as HTMLElement
    if (target.closest('button, input, select, textarea, a')) return
    if (event.ctrlKey || event.metaKey || event.altKey) return
    const current = rows().indexOf(target.closest<HTMLElement>('[data-line]') as HTMLElement)
    if (event.key === 'j') focusRow(current + 1)
    else if (event.key === 'k') focusRow(current < 0 ? rows().length - 1 : current - 1)
    else if (event.key === ' ' && live) onFollowChange(!follow)
    else if (event.key === '/' && searchId && document.getElementById(searchId))
      document.getElementById(searchId)?.focus()
    else if (event.key === 'c' && current >= 0) {
      const entry = shown.find((l) => l.id === rows()[current].dataset.id)
      if (entry) copy(entry)
    } else if (event.key === 'Enter' && query.trim() !== '') gotoMatch(event.shiftKey ? -1 : 1)
    else return
    event.preventDefault()
  }

  function onScroll() {
    const el = box.current
    if (live && follow && el && el.scrollHeight - el.scrollTop - el.clientHeight > 48) {
      onFollowChange(false)
    }
  }

  function jumpToLatest() {
    onFollowChange(true)
    setLimit(PAGE)
  }

  return (
    <div className="space-y-2">
      <div className="log-toolbar flex flex-wrap items-center gap-x-4 gap-y-2">
        {live && (
          <CheckboxField
            label={t('logs.follow')}
            checked={follow && !table}
            onChange={(e) => onFollowChange(e.target.checked)}
          />
        )}
        <CheckboxField
          label={t('logs.wrap')}
          checked={wrap}
          onChange={(e) => onWrapChange(e.target.checked)}
        />
        {live && (
          <CheckboxField
            label={t('logs.announce_toggle')}
            checked={announce}
            onChange={(e) => setAnnounce(e.target.checked)}
          />
        )}
        <Button
          aria-pressed={table}
          onClick={() => {
            setTable(!table)
            if (!table && live) onFollowChange(false)
          }}
        >
          {table ? t('logs.show_list') : t('logs.show_table')}
        </Button>
        <Button
          reason={shown.length === 0 ? t('logs.nothing_to_download') : undefined}
          onClick={() => downloadText(fileName, shown.map(lineText).join('\n') + '\n')}
        >
          {t('logs.download')}
        </Button>
        {query.trim() !== '' && (
          <span className="flex items-center gap-2">
            <Button
              onClick={() => gotoMatch(-1)}
              reason={matches.length ? undefined : t('logs.no_match')}
            >
              {t('logs.prev_match')}
            </Button>
            <Button
              onClick={() => gotoMatch(1)}
              reason={matches.length ? undefined : t('logs.no_match')}
            >
              {t('logs.next_match')}
            </Button>
            <span className="text-muted">{t('logs.match_count', { count: matches.length })}</span>
          </span>
        )}
      </div>
      <p id={`${id}-keys`} className="log-keys text-muted max-sm:hidden">
        {t('logs.keys')}
      </p>
      <span role="status" className="sr-only">
        {copied}
      </span>
      <span role="status" aria-live="polite" className="sr-only">
        {announcement}
      </span>
      {table ? (
        <div
          tabIndex={0}
          role="region"
          aria-label={t('logs.table_label', { label })}
          className="console max-h-[70vh] overflow-auto"
        >
          <table className="w-full border-collapse text-left text-sm">
            <caption className="sr-only">{t('logs.table_label', { label })}</caption>
            <thead className="sticky top-0 bg-surface">
              <tr>
                <th scope="col" className="p-2 font-semibold text-muted">
                  {t('logs.col_time')}
                </th>
                {showService && (
                  <th scope="col" className="p-2 font-semibold text-muted">
                    {t('logs.col_service')}
                  </th>
                )}
                {showReplica && (
                  <th scope="col" className="p-2 font-semibold text-muted">
                    {t('logs.col_replica')}
                  </th>
                )}
                <th scope="col" className="p-2 font-semibold text-muted">
                  {t('logs.col_level')}
                </th>
                <th scope="col" className="p-2 font-semibold text-muted">
                  {t('logs.col_stream')}
                </th>
                <th scope="col" className="p-2 font-semibold text-muted">
                  {t('logs.col_message')}
                </th>
              </tr>
            </thead>
            <tbody>
              {shown.map((l) => (
                <tr key={l.id} className="border-t border-border align-top">
                  <td className="whitespace-nowrap p-2">
                    {l.time ? <time dateTime={l.time}>{formatDateTime(l.time)}</time> : '-'}
                  </td>
                  {showService && <td className="p-2">{l.service ?? '-'}</td>}
                  {showReplica && <td className="p-2">{l.replica ?? '-'}</td>}
                  <td className="p-2">
                    {l.gap !== undefined
                      ? '-'
                      : t(`logs.level.${(l.level || 'unknown') as 'error'}`)}
                  </td>
                  <td className="p-2">{l.stream ?? '-'}</td>
                  <td className="break-words p-2 font-mono">
                    {l.gap !== undefined ? t('logs.gap', { seconds: l.gap }) : plainText(l.message)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="relative">
          <div
            ref={box}
            role="log"
            aria-live="off"
            aria-label={label}
            aria-describedby={`${id}-keys`}
            tabIndex={0}
            onKeyDown={onKeyDown}
            onScroll={onScroll}
            className={`console font-term max-h-[70vh] min-h-24 overflow-auto py-2 ${
              wrap ? '' : 'whitespace-pre'
            }`}
          >
            {live && (hidden > 0 || onLoadOlder) && (
              <div className="p-2">
                <Button
                  loading={loadingOlder}
                  onClick={() => {
                    if (hidden > 0) setLimit(limit + PAGE)
                    else onLoadOlder?.()
                  }}
                >
                  {hidden > 0
                    ? t('logs.show_more', { count: Math.min(hidden, PAGE) })
                    : t('logs.load_older')}
                </Button>
              </div>
            )}
            {shown.length === 0 && <p className="p-3 text-muted">{empty}</p>}
            {shown.map((l) =>
              l.gap !== undefined ? (
                <div
                  key={l.id}
                  data-line=""
                  data-id={l.id}
                  tabIndex={-1}
                  className="px-2 py-1 text-warning"
                >
                  <span aria-hidden="true">── </span>
                  {t('logs.gap', { seconds: l.gap })}
                  <span aria-hidden="true"> ──</span>
                </div>
              ) : (
                <LogRow
                  key={l.id}
                  l={l}
                  query={query}
                  wrap={wrap}
                  showService={showService}
                  showReplica={showReplica}
                  fresh={l.id === freshId.current}
                  onContext={onContext}
                  onCopy={copy}
                />
              ),
            )}
            {!live && (hidden > 0 || onLoadOlder) && (
              <div className="p-2">
                <Button
                  loading={loadingOlder}
                  onClick={() => {
                    if (hidden > 0) setLimit(limit + PAGE)
                    else onLoadOlder?.()
                  }}
                >
                  {hidden > 0
                    ? t('logs.show_more', { count: Math.min(hidden, PAGE) })
                    : t('logs.load_more')}
                </Button>
              </div>
            )}
          </div>
          {live && !follow && (
            <div className="absolute bottom-3 right-5">
              <Button variant="primary" onClick={jumpToLatest}>
                {pending > 0 ? t('logs.jump_pending', { count: pending }) : t('logs.jump')}
              </Button>
            </div>
          )}
        </div>
      )}
      {table && live && pending > 0 && (
        <p className="text-sm text-muted">{t('logs.pending_table', { count: pending })}</p>
      )}
    </div>
  )
}
