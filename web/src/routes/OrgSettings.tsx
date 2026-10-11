// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import { Banner, Button, Card, Skeleton, TextField } from '@/components/ui'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'

const DEFAULT_METRICS_DAYS = 90
const DEFAULT_LOGS_DAYS = 30
const MAX_DAYS = 3650

function validDays(text: string): boolean {
  const n = Number(text)
  return Number.isInteger(n) && n >= 1 && n <= MAX_DAYS
}

type Settings = NonNullable<components['schemas']['Org']['settings']>

function RetentionForm({ settings }: { settings: Settings }) {
  const metrics = settings.metrics_retention_days ?? DEFAULT_METRICS_DAYS
  const logs = settings.logs_retention_days ?? DEFAULT_LOGS_DAYS
  const queryClient = useQueryClient()
  const allowed = useAccess('org:update') === 'allow'
  const [metricsText, setMetricsText] = useState(String(metrics))
  const [logsText, setLogsText] = useState(String(logs))
  const [asking, setAsking] = useState(false)
  const [saved, setSaved] = useState(false)

  const shorter =
    validDays(metricsText) &&
    validDays(logsText) &&
    (Number(metricsText) < metrics || Number(logsText) < logs)
  const invalid = !validDays(metricsText) || !validDays(logsText)
  const unchanged = Number(metricsText) === metrics && Number(logsText) === logs

  const save = useMutation({
    mutationFn: (confirm: boolean) =>
      unwrap(
        api.PATCH('/org', {
          body: {
            settings: {
              ...settings,
              metrics_retention_days: Number(metricsText),
              logs_retention_days: Number(logsText),
              // Only after the user said yes: older data is deleted (REQ-OBS retention).
              ...(confirm ? { confirm_retention_shrink: true } : {}),
            },
          },
        }),
      ),
    onSuccess: async () => {
      setAsking(false)
      setSaved(true)
      await queryClient.invalidateQueries({ queryKey: ['org'] })
    },
    onError: (error) => {
      // The server wants the confirmation (409). Ask the user, then send again.
      if (error instanceof ApiError && error.code === 'retention_shrink_needs_confirm') {
        setAsking(true)
      }
    },
  })
  const needsConfirm =
    save.error instanceof ApiError && save.error.code === 'retention_shrink_needs_confirm'

  function submit(event: FormEvent) {
    event.preventDefault()
    setSaved(false)
    if (invalid || unchanged) return
    if (shorter) setAsking(true)
    else save.mutate(false)
  }

  return (
    <Card>
      <form onSubmit={submit} className="space-y-4" noValidate>
        <h2 className="font-semibold">{t('org.retention')}</h2>
        <p className="text-sm text-muted">{t('org.retention_text')}</p>
        <div className="grid gap-3 sm:grid-cols-2">
          <TextField
            label={t('org.metrics_days')}
            type="number"
            min={1}
            max={MAX_DAYS}
            value={metricsText}
            disabled={!allowed}
            error={validDays(metricsText) ? null : t('org.days_invalid', { max: MAX_DAYS })}
            onChange={(e) => setMetricsText(e.target.value)}
          />
          <TextField
            label={t('org.logs_days')}
            type="number"
            min={1}
            max={MAX_DAYS}
            value={logsText}
            disabled={!allowed}
            error={validDays(logsText) ? null : t('org.days_invalid', { max: MAX_DAYS })}
            onChange={(e) => setLogsText(e.target.value)}
          />
        </div>
        {saved && <Banner variant="info">{t('org.saved')}</Banner>}
        {save.error && !needsConfirm && (
          <ErrorBanner error={save.error} title={t('org.save_failed')} />
        )}
        {allowed ? (
          <Button type="submit" variant="primary" loading={save.isPending && !asking}>
            {t('common.save')}
          </Button>
        ) : (
          <p className="text-sm text-muted">{t('org.read_only')}</p>
        )}
      </form>
      {asking && (
        <ConfirmDialog
          title={t('org.shrink_title')}
          text={t('org.shrink_text', {
            metrics: metricsText,
            logs: logsText,
          })}
          confirmLabel={t('org.shrink_confirm')}
          busy={save.isPending}
          error={save.error && !needsConfirm ? <ErrorBanner error={save.error} /> : undefined}
          onConfirm={() => save.mutate(true)}
          onClose={() => {
            save.reset()
            setAsking(false)
          }}
        />
      )}
    </Card>
  )
}

// The domain under which previews get their URL (F03).
function PreviewDomainForm({ settings }: { settings: Settings }) {
  const queryClient = useQueryClient()
  const allowed = useAccess('org:update') === 'allow'
  const current = settings.preview_domain ?? ''
  const [value, setValue] = useState(current)
  const [saved, setSaved] = useState(false)
  const valid =
    value.trim() === '' ||
    /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$/i.test(value.trim())
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/org', {
          body: { settings: { ...settings, preview_domain: value.trim() } },
        }),
      ),
    onSuccess: async () => {
      setSaved(true)
      await queryClient.invalidateQueries({ queryKey: ['org'] })
    },
  })
  return (
    <Card>
      <form
        className="space-y-4"
        noValidate
        onSubmit={(e) => {
          e.preventDefault()
          setSaved(false)
          if (valid && value.trim() !== current) save.mutate()
        }}
      >
        <h2 className="font-semibold">{t('org.preview_domain')}</h2>
        <TextField
          label={t('org.preview_domain_label')}
          hint={t('org.preview_domain_hint')}
          value={value}
          disabled={!allowed}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          error={valid ? null : t('domains.host_invalid')}
          onChange={(e) => setValue(e.target.value)}
        />
        {saved && <Banner variant="info">{t('org.saved')}</Banner>}
        {save.error && <ErrorBanner error={save.error} title={t('org.save_failed')} />}
        {allowed && (
          <Button type="submit" variant="primary" loading={save.isPending}>
            {t('common.save')}
          </Button>
        )}
      </form>
    </Card>
  )
}

// Organization settings: how long metrics and logs are kept (REQ-OBS-005, F09).
export function OrgSettingsPage() {
  const org = useQuery({ queryKey: ['org'], queryFn: () => unwrap(api.GET('/org')) })
  return (
    <section>
      <PageHeader title={t('org.title')} />
      <div className="space-y-4">
        {org.isPending && <Skeleton />}
        {org.isError && <ErrorBanner error={org.error} onRetry={() => void org.refetch()} />}
        {org.data && <RetentionForm settings={org.data.settings ?? ({} as Settings)} />}
        {org.data && <PreviewDomainForm settings={org.data.settings ?? ({} as Settings)} />}
      </div>
    </section>
  )
}
