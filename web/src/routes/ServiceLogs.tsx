// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { CommonFieldsForm, LogSearchResults } from '@/components/LogSearch'
import type { CommonFields } from '@/components/LogSearch'
import { LogViewer } from '@/components/LogViewer'
import { Banner, Button, SelectField, TextField } from '@/components/ui'
import { t } from '@/i18n'
import { LEVELS, rangeWindow, toEntry, useLogStream } from '@/logsData'
import type { SearchParams } from '@/logsData'
import { lineKey, plainText } from '@/logText'
import type { LogEntry } from '@/logText'

const SEARCH_ID = 'log-filter-text'

// A phone wraps lines by default (components, LogViewer).
function wrapDefault(): boolean {
  return !window.matchMedia('(min-width: 640px)').matches
}

function entryKey(l: LogEntry): string {
  return lineKey({ time: l.time, replica: l.replica, node_id: l.node, message: l.message })
}

function LiveTail({ serviceId }: { serviceId: string }) {
  const [follow, setFollow] = useState(true)
  const [wrap, setWrap] = useState(wrapDefault)
  const [level, setLevel] = useState('')
  const [replica, setReplica] = useState('')
  const [text, setText] = useState('')
  const [olderError, setOlderError] = useState<unknown>(null)
  const [loadingOlder, setLoadingOlder] = useState(false)
  const stream = useLogStream(serviceId, !follow)

  const replicas = useMemo(
    () =>
      [
        ...new Set(stream.lines.map((l) => l.replica).filter((r): r is number => r !== undefined)),
      ].sort((a, b) => a - b),
    [stream.lines],
  )
  const needle = text.trim().toLowerCase()
  const shown = useMemo(
    () =>
      stream.lines.filter(
        (l) =>
          l.gap !== undefined ||
          ((level === '' || (l.level || 'unknown') === level) &&
            (replica === '' || String(l.replica) === replica) &&
            (needle === '' || plainText(l.message).toLowerCase().includes(needle))),
      ),
    [stream.lines, level, replica, needle],
  )

  // Older lines come from getServiceLogs, before the oldest line of the viewer.
  async function loadOlder() {
    const first = stream.lines.find((l) => l.time)
    setLoadingOlder(true)
    setOlderError(null)
    try {
      const older = await unwrap(
        api.GET('/services/{service_id}/logs', {
          params: {
            path: { service_id: serviceId },
            query: { limit: 200, ...(first?.time ? { to: first.time } : {}) },
          },
        }),
      )
      const have = new Set(stream.lines.map(entryKey))
      const fresh = older.map((l) => toEntry(l)).filter((l) => !have.has(entryKey(l)))
      stream.setLines((prev) => [...fresh, ...prev])
    } catch (error) {
      setOlderError(error)
    } finally {
      setLoadingOlder(false)
    }
  }

  return (
    <div className="space-y-3">
      <div className="grid gap-3 sm:grid-cols-3">
        <TextField
          id={SEARCH_ID}
          label={t('logs.filter_text')}
          value={text}
          onChange={(e) => setText(e.target.value)}
          autoComplete="off"
          spellCheck={false}
        />
        <SelectField
          label={t('logs.filter_level')}
          value={level}
          onChange={(e) => setLevel(e.target.value)}
        >
          <option value="">{t('logsearch.any')}</option>
          {LEVELS.map((l) => (
            <option key={l} value={l}>
              {t(`logs.level.${l}`)}
            </option>
          ))}
        </SelectField>
        <SelectField
          label={t('logs.filter_replica')}
          value={replica}
          onChange={(e) => setReplica(e.target.value)}
        >
          <option value="">{t('logsearch.any')}</option>
          {replicas.map((r) => (
            <option key={r} value={String(r)}>
              {t('logs.replica', { number: r })}
            </option>
          ))}
        </SelectField>
      </div>
      {stream.lost && (
        <Banner variant="warning" title={t('logs.reconnecting')}>
          {t('logs.reconnecting_text')}
        </Banner>
      )}
      {olderError !== null && <ErrorBanner error={olderError} />}
      <LogViewer
        mode="live"
        lines={shown}
        label={t('logs.label')}
        follow={follow}
        onFollowChange={setFollow}
        wrap={wrap}
        onWrapChange={setWrap}
        query={text}
        showReplica={replicas.length > 1}
        pending={stream.pending}
        fileName="service-logs.txt"
        empty={stream.lines.length === 0 ? t('logs.empty') : t('logs.empty_filtered')}
        onLoadOlder={() => void loadOlder()}
        loadingOlder={loadingOlder}
        searchId={SEARCH_ID}
      />
    </div>
  )
}

function SearchMode({ serviceId }: { serviceId: string }) {
  const org = useQuery({ queryKey: ['org'], queryFn: () => unwrap(api.GET('/org')) })
  const retentionDays = org.data?.settings?.logs_retention_days ?? 30
  const [form, setForm] = useState<CommonFields>({
    q: '',
    level: '',
    stream: '',
    range: '1h',
    from: '',
    to: '',
    context: '0',
  })
  const [active, setActive] = useState<SearchParams | null>(null)
  const [activeQuery, setActiveQuery] = useState('')

  function submit(event: FormEvent) {
    event.preventDefault()
    const { from, to } = rangeWindow(form.range, form.from, form.to)
    setActive({
      service_id: serviceId,
      ...(form.q.trim() ? { q: form.q.trim() } : {}),
      ...(form.level ? { level: form.level as SearchParams['level'] } : {}),
      ...(form.stream ? { stream: form.stream as SearchParams['stream'] } : {}),
      ...(from ? { from } : {}),
      ...(to ? { to } : {}),
      ...(Number(form.context) > 0 ? { context: Math.min(20, Number(form.context)) } : {}),
    })
    setActiveQuery(form.q)
  }

  return (
    <div className="space-y-3">
      <form onSubmit={submit} className="space-y-3">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <CommonFieldsForm
            value={form}
            onChange={setForm}
            retentionDays={retentionDays}
            searchId={SEARCH_ID}
          />
        </div>
        <Button type="submit" variant="primary">
          {t('logsearch.submit')}
        </Button>
      </form>
      <LogSearchResults params={active} query={activeQuery} fileName="service-logs-search.txt" />
    </div>
  )
}

// UI-15: the live tail of a service (SSE) and the search mode (UI-37 for one service).
export function ServiceLogs({ serviceId }: { serviceId: string }) {
  const [mode, setMode] = useState<'live' | 'search'>('live')
  return (
    <div className="space-y-4">
      <div role="group" aria-label={t('logs.mode')} className="flex gap-2">
        <Button aria-pressed={mode === 'live'} onClick={() => setMode('live')}>
          {t('logs.mode_live')}
        </Button>
        <Button aria-pressed={mode === 'search'} onClick={() => setMode('search')}>
          {t('logs.mode_search')}
        </Button>
      </div>
      {mode === 'live' ? <LiveTail serviceId={serviceId} /> : <SearchMode serviceId={serviceId} />}
    </div>
  )
}
