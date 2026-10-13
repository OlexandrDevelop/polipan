// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api, setCsrfToken } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog, TypedConfirmDialog } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import {
  Banner,
  Button,
  Card,
  CheckboxField,
  Hint,
  Optional,
  SectionTabs,
  Skeleton,
  TextField,
} from '@/components/ui'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'
import { queryClient } from '@/queryClient'

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
        <Hint>{t('org.retention_text')}</Hint>
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

type Org = components['schemas']['Org']

const HOST_PATTERN = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$/i

// Name, default domain suffix, certificate email and the 2FA policy (UI-28).
function GeneralForm({ org }: { org: Org }) {
  const queryClient = useQueryClient()
  const allowed = useAccess('org:update') === 'allow'
  const settings = org.settings ?? ({} as Settings)
  const [name, setName] = useState(org.name ?? '')
  const [suffix, setSuffix] = useState(settings.default_domain_suffix ?? '')
  const [email, setEmail] = useState(settings.acme_email ?? '')
  const [require2fa, setRequire2fa] = useState(Boolean(settings.require_2fa))
  const [saved, setSaved] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  const nameError = name.trim() === '' ? t('org.name_required') : null
  const suffixError =
    suffix.trim() === '' || HOST_PATTERN.test(suffix.trim()) ? null : t('domains.host_invalid')
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/org', {
          body: {
            name: name.trim(),
            settings: {
              ...settings,
              default_domain_suffix: suffix.trim(),
              acme_email: email.trim(),
              require_2fa: require2fa,
            },
          },
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
          setSubmitted(true)
          if (!nameError && !suffixError) save.mutate()
        }}
      >
        <h2 className="font-semibold">{t('org.general')}</h2>
        <Hint>{t('org.general_text')}</Hint>
        <TextField
          label={t('org.name_label')}
          required
          autoComplete="off"
          value={name}
          disabled={!allowed}
          error={submitted ? nameError : null}
          onChange={(e) => setName(e.target.value)}
        />
        <Optional
          filled={suffix !== '' || email !== '' || require2fa || Boolean(submitted && suffixError)}
        >
          <div className="space-y-4">
            <TextField
              label={t('org.domain_suffix')}
              hint={t('org.domain_suffix_hint')}
              value={suffix}
              disabled={!allowed}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              error={submitted ? suffixError : null}
              onChange={(e) => setSuffix(e.target.value)}
            />
            <TextField
              label={t('org.acme_email')}
              hint={t('org.acme_email_hint')}
              type="email"
              value={email}
              disabled={!allowed}
              autoComplete="off"
              onChange={(e) => setEmail(e.target.value)}
            />
            <CheckboxField
              label={t('org.require_2fa')}
              hint={t('org.require_2fa_hint')}
              checked={require2fa}
              disabled={!allowed}
              onChange={(e) => setRequire2fa(e.target.checked)}
            />
          </div>
        </Optional>
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

// The version of the panel. There is no update check in the API yet, so there is no notice.
function VersionCard() {
  const version = useQuery({ queryKey: ['version'], queryFn: () => unwrap(api.GET('/version')) })
  return (
    <Card>
      <h2 className="font-semibold">{t('org.version_title')}</h2>
      {version.isPending && <Skeleton lines={1} />}
      {version.isError && (
        <ErrorBanner error={version.error} onRetry={() => void version.refetch()} />
      )}
      {version.data && (
        <p className="mt-2 font-mono text-sm">
          {t('org.version_value', {
            version: version.data.version ?? '-',
            api: version.data.api ?? '-',
          })}
        </p>
      )}
    </Card>
  )
}

// Delete the organization: the owner types its slug first (REQ-UI-004).
function DangerZone({ org }: { org: Org }) {
  const allowed = useAccess('org:delete') === 'allow'
  const navigate = useNavigate()
  const [asking, setAsking] = useState(false)
  const remove = useMutation({
    mutationFn: () =>
      unwrap(api.DELETE('/org', { params: { query: { confirm: org.slug ?? '' } } })),
    onSuccess: () => {
      setCsrfToken('')
      queryClient.clear()
      queryClient.setQueryData(['me'], null)
      void navigate({ to: '/login' })
    },
  })
  if (!allowed) return null
  return (
    <Card>
      <h2 className="font-semibold text-danger">{t('org.danger_title')}</h2>
      <p className="my-2 text-sm text-muted">{t('org.danger_text')}</p>
      <Button variant="destructive" onClick={() => setAsking(true)}>
        {t('org.delete')}
      </Button>
      {asking && (
        <TypedConfirmDialog
          title={t('org.delete_title', { name: org.name ?? '' })}
          text={t('org.delete_text')}
          confirmLabel={t('org.delete')}
          typed={org.slug ?? ''}
          busy={remove.isPending}
          error={
            remove.error ? (
              <ErrorBanner error={remove.error} title={t('org.delete_failed')} />
            ) : undefined
          }
          onConfirm={() => remove.mutate()}
          onClose={() => {
            remove.reset()
            setAsking(false)
          }}
        />
      )}
    </Card>
  )
}

// Organization settings: how long metrics and logs are kept (REQ-OBS-005, F09).
export function OrgSettingsPage() {
  const org = useQuery({ queryKey: ['org'], queryFn: () => unwrap(api.GET('/org')) })
  return (
    <section>
      <PageHeader title={t('org.title')} />
      {org.isPending && <Skeleton />}
      {org.isError && <ErrorBanner error={org.error} onRetry={() => void org.refetch()} />}
      {org.data && (
        <SectionTabs
          label={t('settings.sections')}
          sections={[
            {
              id: 'general',
              label: t('org.general'),
              content: (
                <>
                  {org.data && <GeneralForm org={org.data} />}
                  <VersionCard />
                </>
              ),
            },
            {
              id: 'retention',
              label: t('org.retention'),
              content: <RetentionForm settings={org.data.settings ?? ({} as Settings)} />,
            },
            {
              id: 'previews',
              label: t('org.preview_domain'),
              content: <PreviewDomainForm settings={org.data.settings ?? ({} as Settings)} />,
            },
            {
              id: 'danger',
              label: t('org.danger_title'),
              danger: true,
              content: <DangerZone org={org.data} />,
            },
          ]}
        />
      )}
    </section>
  )
}
