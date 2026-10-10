// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog, Modal } from '@/components/Modal'
import { Time } from '@/components/Time'
import {
  Banner,
  Button,
  Card,
  CheckboxField,
  EmptyState,
  SelectField,
  Skeleton,
  StatusBadge,
  TextAreaField,
  TextField,
} from '@/components/ui'
import { formatBytes } from '@/envText'
import { formatRelative, t, tState } from '@/i18n'
import { accessFor } from '@/permissions'
import type { Service } from '@/scope'
import type { Tone } from '@/status'

type Policy = components['schemas']['BackupPolicy']
type Run = components['schemas']['BackupRun']
type Check = components['schemas']['RestoreCheck']
type Target = components['schemas']['BackupTarget']

const CRON_FIELDS = 5

export function healthTone(health: string | undefined, overdue: boolean | undefined): Tone {
  if (health === 'failing') return 'danger'
  if (overdue || health === 'unverified') return 'warning'
  if (health === 'verified') return 'success'
  return 'muted'
}

// The badge of REQ-BKP-105: "Restore verified N days ago", or a warning.
export function VerifiedBadge({ policy }: { policy: Policy }) {
  const check = policy.last_restore_check
  if (policy.health === 'failing') {
    return <StatusBadge tone="danger" label={t('backup.badge.failing')} />
  }
  if (check?.state === 'passed' && check.finished_at && !policy.check_overdue) {
    return (
      <StatusBadge
        tone="success"
        label={t('backup.badge.verified', { when: formatRelative(check.finished_at) })}
      />
    )
  }
  if (policy.check_overdue) return <StatusBadge tone="warning" label={t('backup.badge.overdue')} />
  return <StatusBadge tone="warning" label={t('backup.badge.unverified')} />
}

function useTargets() {
  return useQuery({
    queryKey: ['backup-targets'],
    queryFn: () => unwrap(api.GET('/backup-targets')),
  })
}

function validCron(text: string): boolean {
  const parts = text.trim().split(/\s+/)
  return parts.length === CRON_FIELDS && parts.every((p) => /^[\w*/,-]+$/.test(p))
}

const SUGGESTION = {
  schedule: '0 2 * * *',
  daily: '7',
  weekly: '4',
  monthly: '6',
  check: '0 3 * * 0',
}

function PolicyDialog({
  service,
  policy,
  onClose,
}: {
  service: Service
  policy?: Policy
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const targets = useTargets()
  const isDb = service.type === 'database'
  const volumes = useQuery({
    queryKey: ['volumes', service.id],
    enabled: !isDb,
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/volumes', {
          params: { path: { service_id: service.id ?? '' } },
        }),
      ),
  })
  const [schedule, setSchedule] = useState(policy?.schedule ?? SUGGESTION.schedule)
  const [picked, setPicked] = useState<string[]>(policy?.target_ids ?? [])
  const [daily, setDaily] = useState(String(policy?.retention?.keep_daily ?? SUGGESTION.daily))
  const [weekly, setWeekly] = useState(String(policy?.retention?.keep_weekly ?? SUGGESTION.weekly))
  const [monthly, setMonthly] = useState(
    String(policy?.retention?.keep_monthly ?? SUGGESTION.monthly),
  )
  const [check, setCheck] = useState(policy?.restore_check_enabled ?? true)
  const [checkSchedule, setCheckSchedule] = useState(
    policy?.restore_check_schedule ?? SUGGESTION.check,
  )
  const [mode, setMode] = useState(policy?.restore_check_mode ?? 'full')
  const [volumeId, setVolumeId] = useState(policy?.volume_id ?? '')
  const [pre, setPre] = useState((policy?.hooks?.pre ?? []).join('\n'))
  const [post, setPost] = useState((policy?.hooks?.post ?? []).join('\n'))
  const [submitted, setSubmitted] = useState(false)
  const num = (v: string) => (v.trim() === '' ? 0 : Number(v))
  const errors = {
    schedule: validCron(schedule) ? null : t('backup.cron_invalid'),
    targets: picked.length > 0 ? null : t('backup.targets_required'),
    retention:
      [daily, weekly, monthly].every((v) => Number.isInteger(num(v)) && num(v) >= 0) &&
      num(daily) + num(weekly) + num(monthly) > 0
        ? null
        : t('backup.retention_invalid'),
    checkSchedule: !check || validCron(checkSchedule) ? null : t('backup.cron_invalid'),
    volume: isDb || volumeId || policy ? null : t('backup.volume_required'),
  }
  const valid = Object.values(errors).every((e) => !e)

  const save = useMutation({
    mutationFn: () => {
      const lines = (text: string) =>
        text
          .split('\n')
          .map((l) => l.trim())
          .filter(Boolean)
      const body = {
        schedule: schedule.trim(),
        target_ids: picked,
        retention: { keep_daily: num(daily), keep_weekly: num(weekly), keep_monthly: num(monthly) },
        restore_check_enabled: check,
        restore_check_schedule: checkSchedule.trim(),
        restore_check_mode: mode as 'full' | 'sample',
        pre_hook: lines(pre),
        post_hook: lines(post),
        ...(!policy && volumeId ? { volume_id: volumeId } : {}),
      }
      return policy?.id
        ? unwrap(
            api.PATCH('/backup-policies/{policy_id}', {
              params: { path: { policy_id: policy.id } },
              body,
            }),
          )
        : unwrap(
            api.POST('/services/{service_id}/backup-policies', {
              params: { path: { service_id: service.id ?? '' } },
              body,
            }),
          )
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['backup-policies'] })
      await queryClient.invalidateQueries({ queryKey: ['service'] })
      await queryClient.invalidateQueries({ queryKey: ['services'] })
      onClose()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (valid) save.mutate()
  }

  return (
    <Modal title={policy ? t('backup.edit_policy') : t('backup.add_policy')} onClose={onClose}>
      <form onSubmit={submit} noValidate className="space-y-4">
        {save.error && <ErrorBanner error={save.error} title={t('backup.save_failed')} />}
        {!isDb && !policy && (
          <SelectField
            label={t('backup.volume')}
            required
            value={volumeId}
            error={submitted ? errors.volume : null}
            onChange={(e) => setVolumeId(e.target.value)}
          >
            <option value="">{t('backup.volume_pick')}</option>
            {(volumes.data ?? [])
              .filter((v) => v.kind === 'volume')
              .map((v) => (
                <option key={v.id} value={v.id}>
                  {v.name} ({v.mount_path})
                </option>
              ))}
          </SelectField>
        )}
        <TextField
          label={t('backup.schedule')}
          hint={t('backup.schedule_hint')}
          required
          autoComplete="off"
          spellCheck={false}
          error={submitted ? errors.schedule : null}
          value={schedule}
          onChange={(e) => setSchedule(e.target.value)}
        />
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium">{t('backup.targets')}</legend>
          {targets.isPending && <Skeleton lines={1} />}
          {targets.data?.length === 0 && (
            <Banner variant="warning">
              {t('backup.no_targets')} <TargetsLink />
            </Banner>
          )}
          {(targets.data ?? []).map((tg) => (
            <CheckboxField
              key={tg.id}
              label={`${tg.name} (${tg.kind})`}
              checked={picked.includes(tg.id ?? '')}
              onChange={(e) =>
                setPicked((list) =>
                  e.target.checked ? [...list, tg.id ?? ''] : list.filter((x) => x !== tg.id),
                )
              }
            />
          ))}
          {submitted && errors.targets && <p className="text-sm text-danger">{errors.targets}</p>}
        </fieldset>
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium">{t('backup.retention')}</legend>
          <div className="grid grid-cols-3 gap-3">
            <TextField
              label={t('backup.keep_daily')}
              type="number"
              inputMode="numeric"
              min={0}
              value={daily}
              onChange={(e) => setDaily(e.target.value)}
            />
            <TextField
              label={t('backup.keep_weekly')}
              type="number"
              inputMode="numeric"
              min={0}
              value={weekly}
              onChange={(e) => setWeekly(e.target.value)}
            />
            <TextField
              label={t('backup.keep_monthly')}
              type="number"
              inputMode="numeric"
              min={0}
              value={monthly}
              onChange={(e) => setMonthly(e.target.value)}
            />
          </div>
          <p className="text-sm text-muted">{t('backup.retention_hint')}</p>
          {submitted && errors.retention && (
            <p className="text-sm text-danger">{errors.retention}</p>
          )}
        </fieldset>
        <CheckboxField
          label={t('backup.check_enabled')}
          hint={t('backup.check_enabled_hint')}
          checked={check}
          onChange={(e) => setCheck(e.target.checked)}
        />
        {check && (
          <>
            <TextField
              label={t('backup.check_schedule')}
              hint={t('backup.schedule_hint')}
              autoComplete="off"
              spellCheck={false}
              error={submitted ? errors.checkSchedule : null}
              value={checkSchedule}
              onChange={(e) => setCheckSchedule(e.target.value)}
            />
            <SelectField
              label={t('backup.check_mode')}
              hint={t('backup.check_mode_hint')}
              value={mode}
              onChange={(e) => setMode(e.target.value as 'full' | 'sample')}
            >
              <option value="full">{t('backup.check_mode.full')}</option>
              <option value="sample">{t('backup.check_mode.sample')}</option>
            </SelectField>
          </>
        )}
        <details>
          <summary className="cursor-pointer text-sm font-medium">{t('backup.hooks')}</summary>
          <div className="mt-3 space-y-3">
            <TextAreaField
              label={t('backup.pre_hook')}
              hint={t('backup.hook_hint')}
              value={pre}
              onChange={(e) => setPre(e.target.value)}
            />
            <TextAreaField
              label={t('backup.post_hook')}
              value={post}
              onChange={(e) => setPost(e.target.value)}
            />
          </div>
        </details>
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type="submit" variant="primary" loading={save.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

function TargetsLink() {
  const { org } = useParams({ strict: false }) as { org?: string }
  if (!org) return null
  return (
    <Link to="/o/$org/settings/backup-targets" params={{ org }} className="text-primary underline">
      {t('backup.open_targets')}
    </Link>
  )
}

// UI-39: pick the target and the mode, read what is replaced, confirm, follow the progress, see the result.
export function RestoreWizard({
  service,
  run,
  targets,
  onClose,
}: {
  service: Service
  run: Run
  targets: Target[]
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const good = (run.snapshots ?? []).filter((s) => s.ok)
  const isDb = service.type === 'database'
  const [targetId, setTargetId] = useState(good[0]?.target_id ?? '')
  const [mode, setMode] = useState<'in_place' | 'new_service'>(isDb ? 'new_service' : 'in_place')
  const [newName, setNewName] = useState(`${service.name ?? 'db'}-restore`)
  const [confirm, setConfirm] = useState('')
  const [step, setStep] = useState<'choose' | 'summary' | 'progress'>('choose')
  const [jobId, setJobId] = useState('')
  const snapshot = good.find((s) => s.target_id === targetId)
  const nameOk = /^[a-z0-9-]{1,40}$/.test(newName)
  const confirmed = mode === 'new_service' || confirm === service.name

  const start = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/backup-runs/{run_id}/restore', {
          params: {
            path: { run_id: run.id ?? '' },
            header: { 'Idempotency-Key': crypto.randomUUID() },
          },
          body: {
            target_id: targetId,
            mode,
            ...(mode === 'new_service' ? { new_service_name: newName } : { confirm }),
          },
        }),
      ),
    onSuccess: (out) => {
      setJobId(out.job_id ?? '')
      setStep('progress')
    },
  })
  const job = useQuery({
    queryKey: ['backup-restore', jobId],
    enabled: Boolean(jobId),
    queryFn: () =>
      unwrap(api.GET('/backup-restores/{restore_id}', { params: { path: { restore_id: jobId } } })),
    refetchInterval: (q) =>
      q.state.data?.state === 'succeeded' || q.state.data?.state === 'failed' ? false : 2000,
  })
  const finished = job.data?.state === 'succeeded' || job.data?.state === 'failed'

  async function close() {
    await queryClient.invalidateQueries({ queryKey: ['backup-policies'] })
    await queryClient.invalidateQueries({ queryKey: ['backup-runs'] })
    await queryClient.invalidateQueries({ queryKey: ['services'] })
    onClose()
  }

  return (
    <Modal title={t('restore.title', { name: service.name ?? '' })} onClose={() => void close()}>
      {step === 'choose' && (
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (snapshot && (mode === 'in_place' || nameOk)) setStep('summary')
          }}
        >
          <p className="text-sm text-muted">
            {t('restore.from_run')} <Time value={run.started_at} />
          </p>
          <SelectField
            label={t('restore.target')}
            value={targetId}
            onChange={(e) => setTargetId(e.target.value)}
          >
            {good.map((s) => (
              <option key={s.target_id} value={s.target_id}>
                {targets.find((x) => x.id === s.target_id)?.name ?? s.target_id} ·{' '}
                {s.snapshot_id?.slice(0, 8)} · {formatBytes(s.size_bytes)}
              </option>
            ))}
          </SelectField>
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">{t('restore.mode')}</legend>
            {isDb && (
              <label className="flex items-start gap-3">
                <input
                  type="radio"
                  name="mode"
                  checked={mode === 'new_service'}
                  onChange={() => setMode('new_service')}
                  className="mt-1"
                />
                <span>
                  <span className="font-medium">{t('restore.mode.new_service')}</span>
                  <span className="block text-sm text-muted">
                    {t('restore.mode.new_service_text')}
                  </span>
                </span>
              </label>
            )}
            <label className="flex items-start gap-3">
              <input
                type="radio"
                name="mode"
                checked={mode === 'in_place'}
                onChange={() => setMode('in_place')}
                className="mt-1"
              />
              <span>
                <span className="font-medium">{t('restore.mode.in_place')}</span>
                <span className="block text-sm text-muted">{t('restore.mode.in_place_text')}</span>
              </span>
            </label>
          </fieldset>
          {mode === 'new_service' && (
            <TextField
              label={t('restore.new_name')}
              autoComplete="off"
              autoCapitalize="none"
              error={nameOk ? null : t('service.name_invalid')}
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
            />
          )}
          <div className="flex flex-wrap justify-end gap-2">
            <Button onClick={() => void close()}>{t('common.cancel')}</Button>
            <Button
              type="submit"
              variant="primary"
              reason={snapshot ? undefined : t('restore.no_snapshot')}
            >
              {t('wizard.next')}
            </Button>
          </div>
        </form>
      )}
      {step === 'summary' && (
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (confirmed) start.mutate()
          }}
        >
          <Card className="space-y-1 text-sm">
            <p>
              {t('restore.summary_snapshot', {
                id: snapshot?.snapshot_id?.slice(0, 12) ?? '',
                size: formatBytes(snapshot?.size_bytes),
              })}
            </p>
            <p>
              {mode === 'in_place'
                ? t('restore.summary_in_place', { name: service.name ?? '' })
                : t('restore.summary_new', { name: newName })}
            </p>
          </Card>
          {mode === 'in_place' && (
            <>
              <Banner variant="warning" title={t('restore.downtime_title')}>
                {isDb && service.db_engine !== 'redis'
                  ? t('restore.downtime_db')
                  : t('restore.downtime_files')}
              </Banner>
              <p className="text-sm">{t('restore.safety')}</p>
              <TextField
                label={t('common.type_to_confirm', { name: service.name ?? '' })}
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
              />
            </>
          )}
          {start.error && <ErrorBanner error={start.error} title={t('restore.start_failed')} />}
          <div className="flex flex-wrap justify-end gap-2">
            <Button onClick={() => setStep('choose')}>{t('wizard.back')}</Button>
            <Button
              type="submit"
              variant={mode === 'in_place' ? 'destructive' : 'primary'}
              loading={start.isPending}
              reason={confirmed ? undefined : t('common.type_first')}
            >
              {mode === 'in_place' ? t('restore.start_in_place') : t('restore.start_new')}
            </Button>
          </div>
        </form>
      )}
      {step === 'progress' && (
        <div className="space-y-4">
          {job.isError && <ErrorBanner error={job.error} />}
          {!finished && (
            <Banner>
              <span aria-live="polite">{t('restore.running')}</span>
            </Banner>
          )}
          {job.data?.state === 'succeeded' && (
            <Banner variant="info" title={t('restore.done')}>
              {mode === 'new_service'
                ? t('restore.done_new', { name: newName })
                : t('restore.done_in_place')}
            </Banner>
          )}
          {job.data?.state === 'failed' && (
            <Banner variant="danger" title={t('restore.failed')}>
              {job.data.error}
            </Banner>
          )}
          <div className="flex justify-end">
            <Button variant="primary" onClick={() => void close()}>
              {t('common.close')}
            </Button>
          </div>
        </div>
      )}
    </Modal>
  )
}

function runTone(state: string | undefined): Tone {
  switch (state) {
    case 'succeeded':
      return 'success'
    case 'failed':
      return 'danger'
    case 'running':
      return 'info'
    default:
      return 'muted'
  }
}

function CheckList({ check }: { check: Check }) {
  return (
    <ul className="space-y-1 text-sm">
      {(check.checks ?? []).map((c) => (
        <li key={c.name} className="flex flex-wrap gap-2">
          <StatusBadge
            tone={c.ok ? 'success' : 'danger'}
            label={c.ok ? t('backup.ok') : t('backup.not_ok')}
          />
          <span className="font-medium">{c.name}</span>
          {c.detail && <span className="break-words text-muted">{c.detail}</span>}
        </li>
      ))}
    </ul>
  )
}

function PolicyCard({
  service,
  policy,
  targets,
  manage,
  restoreAllowed,
}: {
  service: Service
  policy: Policy
  targets: Target[]
  manage: 'allow' | 'blocked' | 'hidden'
  restoreAllowed: boolean
}) {
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [restoring, setRestoring] = useState<Run | null>(null)
  const policyId = policy.id ?? ''
  const runs = useQuery({
    queryKey: ['backup-runs', policyId],
    queryFn: () =>
      unwrap(
        api.GET('/backup-policies/{policy_id}/runs', {
          params: { path: { policy_id: policyId }, query: { limit: 10 } },
        }),
      ),
    refetchInterval: (q) =>
      q.state.data?.items?.some((r) => r.state === 'queued' || r.state === 'running')
        ? 3000
        : 15000,
  })
  const checks = useQuery({
    queryKey: ['restore-checks', policyId],
    queryFn: () =>
      unwrap(api.GET('/restore-checks', { params: { query: { policy_id: policyId, limit: 5 } } })),
    refetchInterval: (q) =>
      q.state.data?.items?.some((c) => c.state === 'queued' || c.state === 'running')
        ? 3000
        : 15000,
  })
  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: ['backup-runs', policyId] })
    await queryClient.invalidateQueries({ queryKey: ['restore-checks', policyId] })
    await queryClient.invalidateQueries({ queryKey: ['backup-policies'] })
  }
  const runNow = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/backup-policies/{policy_id}/run', {
          params: {
            path: { policy_id: policyId },
            header: { 'Idempotency-Key': crypto.randomUUID() },
          },
        }),
      ),
    onSuccess: refresh,
  })
  const runCheck = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/backup-policies/{policy_id}/restore-checks', {
          params: {
            path: { policy_id: policyId },
            header: { 'Idempotency-Key': crypto.randomUUID() },
          },
        }),
      ),
    onSuccess: refresh,
  })
  const remove = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE('/backup-policies/{policy_id}', { params: { path: { policy_id: policyId } } }),
      ),
    onSuccess: async () => {
      setDeleting(false)
      await refresh()
      await queryClient.invalidateQueries({ queryKey: ['services'] })
    },
  })
  const reason = manage === 'blocked' ? t('common.needs_admin') : undefined
  const names = (policy.target_ids ?? []).map((id) => targets.find((x) => x.id === id)?.name ?? id)
  const lastCheck = checks.data?.items?.[0]

  return (
    <Card className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <h3 className="font-semibold">
            {policy.volume_id
              ? t('backup.kind_volume')
              : t('backup.kind_db', { engine: policy.engine ?? '' })}
          </h3>
          <p className="text-sm text-muted">
            {t('backup.summary', {
              schedule: policy.schedule ?? '',
              targets: names.join(', '),
              daily: String(policy.retention?.keep_daily ?? 0),
            })}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <VerifiedBadge policy={policy} />
          {!policy.enabled && <StatusBadge tone="muted" label={t('backup.disabled')} />}
        </div>
      </div>
      {policy.check_overdue && <Banner variant="warning">{t('backup.overdue_text')}</Banner>}
      {(runNow.error || runCheck.error || remove.error) && (
        <ErrorBanner error={runNow.error ?? runCheck.error ?? remove.error} />
      )}
      {manage !== 'hidden' && (
        <div className="flex flex-wrap gap-2">
          <Button
            variant="primary"
            reason={reason}
            loading={runNow.isPending}
            onClick={() => runNow.mutate()}
          >
            {t('backup.run_now')}
          </Button>
          <Button reason={reason} loading={runCheck.isPending} onClick={() => runCheck.mutate()}>
            {t('backup.run_check')}
          </Button>
          <Button reason={reason} onClick={() => setEditing(true)}>
            {t('backup.edit')}
          </Button>
          <Button variant="destructive" reason={reason} onClick={() => setDeleting(true)}>
            {t('backup.delete')}
          </Button>
        </div>
      )}
      <section aria-label={t('backup.runs')} className="space-y-2">
        <h4 className="font-medium">{t('backup.runs')}</h4>
        {runs.isPending && <Skeleton lines={2} />}
        {runs.isError && <ErrorBanner error={runs.error} onRetry={() => void runs.refetch()} />}
        {runs.data && runs.data.items?.length === 0 && (
          <p className="text-sm text-muted">{t('backup.no_runs')}</p>
        )}
        {runs.data && (runs.data.items?.length ?? 0) > 0 && (
          <DataList
            caption={t('backup.runs')}
            rows={runs.data.items ?? []}
            rowKey={(r) => r.id ?? ''}
            columns={[
              {
                header: t('backup.col_time'),
                primary: true,
                cell: (r) => <Time value={r.started_at ?? undefined} />,
              },
              {
                header: t('backup.col_state'),
                cell: (r) => (
                  <span className="flex flex-col gap-1">
                    <StatusBadge tone={runTone(r.state)} label={tState('backup.run', r.state)} />
                    {r.error && <span className="break-words text-xs text-danger">{r.error}</span>}
                  </span>
                ),
              },
              {
                header: t('backup.col_targets'),
                cell: (r) => (
                  <ul className="space-y-1 text-sm">
                    {(r.snapshots ?? []).map((s) => (
                      <li key={s.target_id} className="break-all">
                        {s.ok ? '✓' : '✗'}{' '}
                        {targets.find((x) => x.id === s.target_id)?.name ?? s.target_id}
                        {s.ok
                          ? ` · ${s.snapshot_id?.slice(0, 8)} · ${formatBytes(s.size_bytes)}`
                          : ''}
                      </li>
                    ))}
                  </ul>
                ),
              },
              {
                header: t('backup.col_duration'),
                cell: (r) =>
                  r.duration_ms !== undefined ? `${(r.duration_ms / 1000).toFixed(1)} s` : '-',
                cardHidden: true,
              },
              {
                header: t('backup.col_trigger'),
                cell: (r) => tState('backup.trigger', r.trigger),
                cardHidden: true,
              },
            ]}
            actions={
              restoreAllowed
                ? (r) =>
                    (r.snapshots ?? []).some((s) => s.ok) ? (
                      <Button onClick={() => setRestoring(r)}>{t('backup.restore')}</Button>
                    ) : null
                : undefined
            }
          />
        )}
      </section>
      <section aria-label={t('backup.checks')} className="space-y-2">
        <h4 className="font-medium">{t('backup.checks')}</h4>
        {checks.data && (checks.data.items?.length ?? 0) === 0 && (
          <p className="text-sm text-muted">{t('backup.no_checks')}</p>
        )}
        {lastCheck && (
          <div className="space-y-2">
            <p className="flex flex-wrap items-center gap-2 text-sm">
              <StatusBadge
                tone={
                  lastCheck.state === 'passed'
                    ? 'success'
                    : lastCheck.state === 'failed'
                      ? 'danger'
                      : 'info'
                }
                label={tState('backup.check', lastCheck.state)}
              />
              <Time value={lastCheck.finished_at ?? lastCheck.started_at ?? undefined} />
              {lastCheck.duration_ms !== undefined && (
                <span>{(lastCheck.duration_ms / 1000).toFixed(1)} s</span>
              )}
            </p>
            <CheckList check={lastCheck} />
          </div>
        )}
      </section>
      {editing && (
        <PolicyDialog service={service} policy={policy} onClose={() => setEditing(false)} />
      )}
      {restoring && (
        <RestoreWizard
          service={service}
          run={restoring}
          targets={targets}
          onClose={() => setRestoring(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={t('backup.delete_title')}
          text={t('backup.delete_text')}
          confirmLabel={t('backup.delete')}
          busy={remove.isPending}
          onConfirm={() => remove.mutate()}
          onClose={() => setDeleting(false)}
        />
      )}
    </Card>
  )
}

// UI-17: policies, runs, restore checks and restore of a service.
export function ServiceBackups({
  service,
  environmentProtected,
}: {
  service: Service
  environmentProtected?: boolean
}) {
  const queryClient = useQueryClient()
  const me = useMe()
  const role = me.data?.org_role
  const manage = accessFor(role, 'backup:manage', environmentProtected)
  const restoreAllowed = accessFor(role, 'backup:restore') === 'allow'
  const [adding, setAdding] = useState(false)
  const serviceId = service.id ?? ''
  const policies = useQuery({
    queryKey: ['backup-policies', serviceId],
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/backup-policies', {
          params: { path: { service_id: serviceId } },
        }),
      ),
  })
  const targets = useTargets()
  const skip = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH('/services/{service_id}', {
          params: { path: { service_id: serviceId } },
          body: { backup_skipped: true },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['services'] })
      await queryClient.invalidateQueries({ queryKey: ['service'] })
    },
  })
  const reason = manage === 'blocked' ? t('common.needs_admin') : undefined
  const add =
    manage !== 'hidden' ? (
      <Button variant="primary" reason={reason} onClick={() => setAdding(true)}>
        {t('backup.add_policy')}
      </Button>
    ) : undefined

  if (policies.isPending) return <Skeleton />
  if (policies.isError)
    return <ErrorBanner error={policies.error} onRetry={() => void policies.refetch()} />
  const list = policies.data
  const noTargets = targets.data?.length === 0
  return (
    <div className="space-y-4">
      {service.type === 'database' && list.length === 0 && service.backup?.status === 'none' && (
        <Banner
          variant="warning"
          title={t('backup.suggestion_title')}
          action={
            manage !== 'hidden' ? (
              <Button reason={reason} loading={skip.isPending} onClick={() => skip.mutate()}>
                {t('backup.skip')}
              </Button>
            ) : undefined
          }
        >
          {t('backup.suggestion_text')}
        </Banner>
      )}
      {service.type === 'database' && list.length === 0 && service.backup?.status === 'skipped' && (
        <Banner variant="warning">{t('backup.skipped_text')}</Banner>
      )}
      {noTargets && (
        <Banner variant="info">
          {t('backup.no_targets')} <TargetsLink />
        </Banner>
      )}
      {skip.error && <ErrorBanner error={skip.error} />}
      {list.length === 0 ? (
        <EmptyState title={t('backup.empty_title')} text={t('backup.empty_text')} action={add} />
      ) : (
        <>
          {add && <div className="flex justify-end">{add}</div>}
          {list.map((p) => (
            <PolicyCard
              key={p.id}
              service={service}
              policy={p}
              targets={targets.data ?? []}
              manage={manage}
              restoreAllowed={restoreAllowed}
            />
          ))}
        </>
      )}
      {adding && <PolicyDialog service={service} onClose={() => setAdding(false)} />}
    </div>
  )
}

// The badge of a service in its header (REQ-BKP-105, REQ-UI-033): verified, not verified, or no backup.
export function BackupBadge({ service }: { service: Service }) {
  const policies = useQuery({
    queryKey: ['backup-policies', service.id],
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/backup-policies', {
          params: { path: { service_id: service.id ?? '' } },
        }),
      ),
  })
  if (!policies.data) return null
  if (policies.data.length === 0) {
    return service.type === 'database' ? (
      <StatusBadge tone="warning" label={t('backup.badge.none')} />
    ) : null
  }
  // The worst policy decides.
  const rank = (p: Policy) =>
    p.health === 'failing' ? 0 : p.check_overdue || p.health !== 'verified' ? 1 : 2
  const worst = [...policies.data].sort((a, b) => rank(a) - rank(b))[0]
  return <VerifiedBadge policy={worst} />
}
