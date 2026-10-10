// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Button, CheckboxField, Skeleton } from '@/components/ui'
import { formatTime, t } from '@/i18n'

const REFRESH_MS = 5_000

// The last lines of the log of a service (Docker logs tail in M1). It reads again every 5 s
// while "Follow" is on. Turn it off to pause, so a screen reader can read the lines.
export function ServiceLogs({ serviceId }: { serviceId: string }) {
  const [follow, setFollow] = useState(true)
  const [wrap, setWrap] = useState(true)
  const box = useRef<HTMLPreElement>(null)

  const logs = useQuery({
    queryKey: ['logs', serviceId],
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/logs', {
          params: { path: { service_id: serviceId }, query: { limit: 200 } },
        }),
      ),
    refetchInterval: follow ? REFRESH_MS : false,
  })

  useEffect(() => {
    const el = box.current
    if (follow && el) el.scrollTop = el.scrollHeight
  }, [logs.data, follow])

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-4">
        <Button onClick={() => void logs.refetch()} loading={logs.isFetching}>
          {t('logs.refresh')}
        </Button>
        <CheckboxField
          label={t('logs.follow')}
          checked={follow}
          onChange={(e) => setFollow(e.target.checked)}
        />
        <CheckboxField
          label={t('logs.wrap')}
          checked={wrap}
          onChange={(e) => setWrap(e.target.checked)}
        />
      </div>
      {logs.isPending && <Skeleton />}
      {logs.isError && <ErrorBanner error={logs.error} onRetry={() => void logs.refetch()} />}
      {logs.data && (
        <pre
          ref={box}
          role="log"
          aria-live="off"
          aria-label={t('logs.label')}
          tabIndex={0}
          className={`max-h-[60vh] overflow-auto rounded-sm border border-border bg-surface p-3 font-mono text-sm ${
            wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre'
          }`}
        >
          {logs.data.length === 0
            ? t('logs.empty')
            : logs.data.map((line, i) => (
                <span key={`${line.time}-${i}`} className="block">
                  {line.time && (
                    <span className="text-muted" title={line.time}>
                      {formatTime(line.time)}{' '}
                    </span>
                  )}
                  {line.stream === 'stderr' && (
                    <span className="text-danger">{t('logs.stderr')} </span>
                  )}
                  {line.message}
                </span>
              ))}
        </pre>
      )}
    </div>
  )
}
