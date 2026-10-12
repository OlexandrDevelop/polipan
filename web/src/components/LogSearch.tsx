// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react'
import { ErrorBanner } from '@/components/ErrorBanner'
import { LogViewer } from '@/components/LogViewer'
import { Banner, Optional, SelectField, Skeleton, TextField } from '@/components/ui'
import { t } from '@/i18n'
import { LEVELS, LOG_RANGES, useLogSearch, toEntry } from '@/logsData'
import type { LogRange, SearchParams } from '@/logsData'
import type { LogEntry } from '@/logText'

// The form fields every log search has: text, level, stream and the time range.
export interface CommonFields {
  q: string
  level: string
  stream: string
  range: LogRange
  from: string
  to: string
  context: string
}

export function CommonFieldsForm({
  value,
  onChange,
  retentionDays,
  searchId,
}: {
  value: CommonFields
  onChange: (value: CommonFields) => void
  retentionDays: number
  searchId?: string
}) {
  const set = (patch: Partial<CommonFields>) => onChange({ ...value, ...patch })
  const beyond =
    value.range === 'custom' &&
    value.from !== '' &&
    new Date(value.from).getTime() < Date.now() - retentionDays * 86400_000
  return (
    <>
      <TextField
        id={searchId}
        label={t('logsearch.text')}
        hint={t('logsearch.text_hint')}
        value={value.q}
        onChange={(e) => set({ q: e.target.value })}
        autoComplete="off"
        spellCheck={false}
      />
      <SelectField
        label={t('logsearch.range')}
        value={value.range}
        onChange={(e) => set({ range: e.target.value as LogRange })}
        hint={t('logsearch.retention', { days: retentionDays })}
      >
        {LOG_RANGES.filter((r) => r.seconds <= retentionDays * 86400).map((r) => (
          <option key={r.id} value={r.id}>
            {t(`logsearch.range.${r.id}`)}
          </option>
        ))}
        <option value="custom">{t('logsearch.range.custom')}</option>
      </SelectField>
      {value.range === 'custom' && (
        <>
          <TextField
            type="datetime-local"
            label={t('logsearch.from')}
            value={value.from}
            onChange={(e) => set({ from: e.target.value })}
            error={beyond ? t('logsearch.beyond', { days: retentionDays }) : null}
          />
          <TextField
            type="datetime-local"
            label={t('logsearch.to')}
            value={value.to}
            onChange={(e) => set({ to: e.target.value })}
          />
        </>
      )}
      <Optional filled={value.level !== '' || value.stream !== '' || value.context !== '0'}>
        <div className="grid gap-3 sm:grid-cols-3">
          <SelectField
            label={t('logsearch.level')}
            value={value.level}
            onChange={(e) => set({ level: e.target.value })}
          >
            <option value="">{t('logsearch.any')}</option>
            {LEVELS.map((l) => (
              <option key={l} value={l}>
                {t(`logs.level.${l}`)}
              </option>
            ))}
          </SelectField>
          <SelectField
            label={t('logsearch.stream')}
            value={value.stream}
            onChange={(e) => set({ stream: e.target.value })}
          >
            <option value="">{t('logsearch.any')}</option>
            <option value="stdout">stdout</option>
            <option value="stderr">stderr</option>
          </SelectField>
          <TextField
            type="number"
            min={0}
            max={20}
            label={t('logsearch.context')}
            hint={t('logsearch.context_hint')}
            value={value.context}
            onChange={(e) => set({ context: e.target.value })}
          />
        </div>
      </Optional>
    </>
  )
}

// The results of a search in the LogViewer (searchLogs, newest first, with the cursor).
export function LogSearchResults({
  params,
  query,
  showService,
  fileName,
  onContext,
  serviceNames,
}: {
  params: SearchParams | null
  query: string
  showService?: boolean
  fileName: string
  onContext?: (entry: LogEntry) => void
  serviceNames?: Map<string, string>
}) {
  const result = useLogSearch(params)
  const [wrap, setWrap] = useState(true)
  if (params === null) return null
  if (result.isPending) return <Skeleton />
  if (result.isError) {
    return <ErrorBanner error={result.error} onRetry={() => void result.refetch()} />
  }
  const lines = result.data.pages.flatMap((p) =>
    (p.items ?? []).map((l) => toEntry(l, serviceNames?.get(l.service_id ?? ''))),
  )
  return (
    <div className="space-y-3">
      {lines.length === 0 ? (
        <Banner title={t('logsearch.none_title')}>{t('logsearch.none')}</Banner>
      ) : (
        <p className="text-sm text-muted">{t('logsearch.count', { count: lines.length })}</p>
      )}
      {lines.length > 0 && (
        <LogViewer
          mode="search"
          lines={lines}
          label={t('logsearch.results')}
          follow={false}
          onFollowChange={() => undefined}
          wrap={wrap}
          onWrapChange={setWrap}
          query={query}
          showReplica
          showService={showService}
          fileName={fileName}
          empty={t('logsearch.none')}
          onContext={onContext}
          onLoadOlder={result.hasNextPage ? () => void result.fetchNextPage() : undefined}
          loadingOlder={result.isFetchingNextPage}
        />
      )}
    </div>
  )
}
