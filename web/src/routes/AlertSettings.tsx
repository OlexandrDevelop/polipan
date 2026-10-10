// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog, Modal } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import {
  Banner,
  Button,
  CheckboxField,
  SecretField,
  SelectField,
  Skeleton,
  StatusBadge,
  TextField,
} from '@/components/ui'
import {
  RULE_TYPES,
  SCOPES,
  scopeOf,
  severityTone,
  THRESHOLD_TYPES,
  useAlertChannels,
  useAlertRules,
} from '@/alerts'
import type { AlertChannel, AlertRule } from '@/alerts'
import type { components } from '@api/schema'
import { t, tState } from '@/i18n'
import { useAccess } from '@/permissions'
import { useEnvironments, useNodes, useProjects, useServices } from '@/scope'

type ChannelWrite = components['schemas']['AlertChannelWrite']
type RuleWrite = components['schemas']['AlertRuleWrite']
type Kind = NonNullable<ChannelWrite['kind']>

const KINDS: Kind[] = ['telegram', 'slack', 'email', 'webhook']

function text(config: Record<string, unknown> | undefined, key: string): string {
  const value = config?.[key]
  if (Array.isArray(value)) return value.join(', ')
  return value === undefined || value === null ? '' : String(value)
}

function ChannelDialog({ channel, onClose }: { channel?: AlertChannel; onClose: () => void }) {
  const queryClient = useQueryClient()
  const editing = Boolean(channel)
  const [kind, setKind] = useState<Kind>(channel?.kind ?? 'telegram')
  const [name, setName] = useState(channel?.name ?? '')
  const [enabled, setEnabled] = useState(channel?.enabled ?? true)
  const [secret, setSecret] = useState('')
  const c = channel?.config
  const [chatId, setChatId] = useState(text(c, 'chat_id'))
  const [host, setHost] = useState(text(c, 'smtp_host'))
  const [port, setPort] = useState(text(c, 'smtp_port') || '587')
  const [user, setUser] = useState(text(c, 'smtp_user'))
  const [from, setFrom] = useState(text(c, 'from'))
  const [recipients, setRecipients] = useState(text(c, 'recipients'))
  const [url, setUrl] = useState(text(c, 'url'))
  const [submitted, setSubmitted] = useState(false)

  // The secret is write-only. An empty value on edit keeps the stored one.
  const secretNeeded = !editing && kind !== 'webhook'
  const errors = {
    name: name.trim() ? null : t('alerts.err_required'),
    secret: secretNeeded && !secret ? t('alerts.err_required') : null,
    chatId: kind === 'telegram' && !chatId.trim() ? t('alerts.err_required') : null,
    host: kind === 'email' && !host.trim() ? t('alerts.err_required') : null,
    port:
      kind === 'email' && !(Number(port) >= 1 && Number(port) <= 65535)
        ? t('alerts.err_port')
        : null,
    from: kind === 'email' && !from.trim() ? t('alerts.err_required') : null,
    recipients:
      kind === 'email' && !recipients.split(',').some((r) => r.includes('@'))
        ? t('alerts.err_recipients')
        : null,
    url: kind === 'webhook' && !/^https?:\/\//.test(url.trim()) ? t('alerts.err_url') : null,
  }
  const invalid = Object.values(errors).some(Boolean)

  function config(): Record<string, unknown> {
    if (kind === 'telegram') return { chat_id: chatId.trim() }
    if (kind === 'email') {
      return {
        smtp_host: host.trim(),
        smtp_port: Number(port),
        smtp_user: user.trim(),
        from: from.trim(),
        recipients: recipients
          .split(',')
          .map((r) => r.trim())
          .filter(Boolean),
      }
    }
    if (kind === 'webhook') return { url: url.trim() }
    return {}
  }

  const save = useMutation({
    mutationFn: () => {
      const body: ChannelWrite = {
        kind,
        name: name.trim(),
        enabled,
        config: config(),
        ...(secret ? { secret } : {}),
      }
      return editing
        ? unwrap(
            api.PATCH('/alert-channels/{channel_id}', {
              params: { path: { channel_id: channel?.id ?? '' } },
              body,
            }),
          )
        : unwrap(api.POST('/alert-channels', { body }))
    },
    onSuccess: async () => {
      setSecret('')
      await queryClient.invalidateQueries({ queryKey: ['alert-channels'] })
      onClose()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (!invalid) save.mutate()
  }
  const show = (error: string | null) => (submitted ? error : null)

  return (
    <Modal title={editing ? t('alerts.channel_edit') : t('alerts.channel_new')} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4" noValidate>
        <SelectField
          label={t('alerts.kind')}
          value={kind}
          disabled={editing}
          onChange={(e) => setKind(e.target.value as Kind)}
        >
          {KINDS.map((k) => (
            <option key={k} value={k}>
              {t(`alerts.kind.${k}`)}
            </option>
          ))}
        </SelectField>
        <TextField
          label={t('alerts.name')}
          required
          value={name}
          error={show(errors.name)}
          onChange={(e) => setName(e.target.value)}
        />
        {kind === 'telegram' && (
          <>
            <TextField
              label={t('alerts.chat_id')}
              required
              value={chatId}
              error={show(errors.chatId)}
              onChange={(e) => setChatId(e.target.value)}
            />
            <SecretField
              label={t('alerts.bot_token')}
              required={!editing}
              value={secret}
              onValue={setSecret}
              error={show(errors.secret)}
            />
          </>
        )}
        {kind === 'slack' && (
          <SecretField
            label={t('alerts.slack_url')}
            required={!editing}
            value={secret}
            onValue={setSecret}
            error={show(errors.secret)}
          />
        )}
        {kind === 'email' && (
          <>
            <TextField
              label={t('alerts.smtp_host')}
              required
              value={host}
              error={show(errors.host)}
              onChange={(e) => setHost(e.target.value)}
            />
            <TextField
              label={t('alerts.smtp_port')}
              required
              type="number"
              value={port}
              error={show(errors.port)}
              onChange={(e) => setPort(e.target.value)}
            />
            <TextField
              label={t('alerts.smtp_user')}
              value={user}
              autoComplete="off"
              onChange={(e) => setUser(e.target.value)}
            />
            <TextField
              label={t('alerts.from')}
              required
              type="email"
              value={from}
              error={show(errors.from)}
              onChange={(e) => setFrom(e.target.value)}
            />
            <TextField
              label={t('alerts.recipients')}
              required
              hint={t('alerts.recipients_hint')}
              value={recipients}
              error={show(errors.recipients)}
              onChange={(e) => setRecipients(e.target.value)}
            />
            <SecretField
              label={t('alerts.smtp_password')}
              value={secret}
              onValue={setSecret}
              error={show(errors.secret)}
            />
          </>
        )}
        {kind === 'webhook' && (
          <>
            <TextField
              label={t('alerts.webhook_url')}
              required
              type="url"
              value={url}
              error={show(errors.url)}
              onChange={(e) => setUrl(e.target.value)}
            />
            <SecretField label={t('alerts.hmac')} value={secret} onValue={setSecret} />
          </>
        )}
        <CheckboxField
          label={t('alerts.enabled_label')}
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        {save.error && <ErrorBanner error={save.error} title={t('alerts.save_failed')} />}
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

function RuleDialog({
  rule,
  channels,
  onClose,
}: {
  rule?: AlertRule
  channels: AlertChannel[]
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const editing = Boolean(rule)
  const start = rule ? scopeOf(rule) : { kind: 'org' as const, id: '' }
  const [name, setName] = useState(rule?.name ?? '')
  const [type, setType] = useState(rule?.type ?? 'service_unhealthy')
  const [kind, setKind] = useState<(typeof SCOPES)[number]>(start.kind)
  const [project, setProject] = useState(rule?.scope?.project_id ?? '')
  const [env, setEnv] = useState(rule?.scope?.environment_id ?? '')
  const [service, setService] = useState(rule?.scope?.service_id ?? '')
  const [node, setNode] = useState(rule?.scope?.node_id ?? '')
  const [severity, setSeverity] = useState<NonNullable<RuleWrite['severity']>>(
    rule?.severity ?? 'warning',
  )
  const [channelIds, setChannelIds] = useState<string[]>(rule?.channel_ids ?? [])
  const [threshold, setThreshold] = useState(String(rule?.params?.threshold ?? 90))
  const [forSeconds, setForSeconds] = useState(String(rule?.params?.for_seconds ?? 300))
  const [repeat, setRepeat] = useState(
    String(Math.round((rule?.repeat_interval_seconds ?? 14400) / 60)),
  )
  const [enabled, setEnabled] = useState(rule?.enabled ?? true)
  const [submitted, setSubmitted] = useState(false)

  const projects = useProjects()
  const environments = useEnvironments(project || undefined)
  const services = useServices(env || undefined)
  const nodes = useNodes()
  const threshold_rule = THRESHOLD_TYPES.has(type)

  const targetId =
    kind === 'project'
      ? project
      : kind === 'environment'
        ? env
        : kind === 'service'
          ? service
          : kind === 'node'
            ? node
            : 'org'
  const errors = {
    name: name.trim() ? null : t('alerts.err_required'),
    scope: targetId ? null : t('alerts.err_scope'),
    threshold:
      !threshold_rule || (Number(threshold) > 0 && Number(threshold) <= 100)
        ? null
        : t('alerts.err_threshold'),
    forSeconds:
      !threshold_rule || (Number(forSeconds) >= 0 && forSeconds !== '')
        ? null
        : t('alerts.err_number'),
    repeat: Number(repeat) >= 1 ? null : t('alerts.err_number'),
  }
  const invalid = Object.values(errors).some(Boolean)
  const show = (error: string | null) => (submitted ? error : null)

  const save = useMutation({
    mutationFn: () => {
      const body: RuleWrite = {
        name: name.trim(),
        type,
        severity,
        channel_ids: channelIds,
        enabled,
        repeat_interval_seconds: Number(repeat) * 60,
        scope:
          kind === 'org'
            ? {}
            : kind === 'project'
              ? { project_id: project }
              : kind === 'environment'
                ? { environment_id: env }
                : kind === 'service'
                  ? { service_id: service }
                  : { node_id: node },
        params: threshold_rule
          ? { threshold: Number(threshold), for_seconds: Number(forSeconds) }
          : {},
      }
      return editing
        ? unwrap(
            api.PATCH('/alert-rules/{rule_id}', {
              params: { path: { rule_id: rule?.id ?? '' } },
              body,
            }),
          )
        : unwrap(api.POST('/alert-rules', { body }))
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['alert-rules'] })
      onClose()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (!invalid) save.mutate()
  }

  return (
    <Modal title={editing ? t('alerts.rule_edit') : t('alerts.rule_new')} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4" noValidate>
        <TextField
          label={t('alerts.name')}
          required
          value={name}
          error={show(errors.name)}
          onChange={(e) => setName(e.target.value)}
        />
        <SelectField
          label={t('alerts.rule_type')}
          value={type}
          onChange={(e) => setType(e.target.value)}
        >
          {RULE_TYPES.map((r) => (
            <option key={r} value={r}>
              {tState('alerts.type', r)}
            </option>
          ))}
        </SelectField>
        <SelectField
          label={t('alerts.scope')}
          value={kind}
          onChange={(e) => setKind(e.target.value as (typeof SCOPES)[number])}
        >
          {SCOPES.map((s) => (
            <option key={s} value={s}>
              {t(`alerts.scope.${s}`)}
            </option>
          ))}
        </SelectField>
        {(kind === 'project' || kind === 'environment' || kind === 'service') && (
          <SelectField
            label={t('logsearch.project')}
            value={project}
            error={kind === 'project' ? show(errors.scope) : null}
            onChange={(e) => {
              setProject(e.target.value)
              setEnv('')
              setService('')
            }}
          >
            <option value="">{t('alerts.pick')}</option>
            {(projects.data ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </SelectField>
        )}
        {(kind === 'environment' || kind === 'service') && (
          <SelectField
            label={t('logsearch.environment')}
            value={env}
            error={kind === 'environment' ? show(errors.scope) : null}
            disabled={!project}
            onChange={(e) => {
              setEnv(e.target.value)
              setService('')
            }}
          >
            <option value="">{t('alerts.pick')}</option>
            {(environments.data ?? []).map((e) => (
              <option key={e.id} value={e.id}>
                {e.name}
              </option>
            ))}
          </SelectField>
        )}
        {kind === 'service' && (
          <SelectField
            label={t('logsearch.service')}
            value={service}
            error={show(errors.scope)}
            disabled={!env}
            onChange={(e) => setService(e.target.value)}
          >
            <option value="">{t('alerts.pick')}</option>
            {(services.data ?? []).map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </SelectField>
        )}
        {kind === 'node' && (
          <SelectField
            label={t('logsearch.node')}
            value={node}
            error={show(errors.scope)}
            onChange={(e) => setNode(e.target.value)}
          >
            <option value="">{t('alerts.pick')}</option>
            {(nodes.data ?? []).map((n) => (
              <option key={n.id} value={n.id}>
                {n.name}
              </option>
            ))}
          </SelectField>
        )}
        <SelectField
          label={t('alerts.col_severity')}
          value={severity}
          onChange={(e) => setSeverity(e.target.value as typeof severity)}
        >
          {(['info', 'warning', 'critical'] as const).map((s) => (
            <option key={s} value={s}>
              {tState('alerts.severity', s)}
            </option>
          ))}
        </SelectField>
        {threshold_rule && (
          <div className="grid gap-3 sm:grid-cols-2">
            <TextField
              label={t('alerts.threshold')}
              type="number"
              value={threshold}
              error={show(errors.threshold)}
              onChange={(e) => setThreshold(e.target.value)}
            />
            <TextField
              label={t('alerts.for_seconds')}
              type="number"
              value={forSeconds}
              error={show(errors.forSeconds)}
              onChange={(e) => setForSeconds(e.target.value)}
            />
          </div>
        )}
        <TextField
          label={t('alerts.repeat')}
          type="number"
          hint={t('alerts.repeat_hint')}
          value={repeat}
          error={show(errors.repeat)}
          onChange={(e) => setRepeat(e.target.value)}
        />
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium">{t('alerts.col_channels')}</legend>
          {channels.length === 0 && <p className="text-sm text-muted">{t('alerts.no_channels')}</p>}
          {channels.map((c) => (
            <CheckboxField
              key={c.id}
              label={`${c.name} (${t(`alerts.kind.${c.kind ?? 'webhook'}`)})`}
              checked={channelIds.includes(c.id ?? '')}
              onChange={(e) =>
                setChannelIds(
                  e.target.checked
                    ? [...channelIds, c.id ?? '']
                    : channelIds.filter((id) => id !== c.id),
                )
              }
            />
          ))}
          {channelIds.length === 0 && (
            <p className="text-sm text-warning">{t('alerts.no_channel_warning')}</p>
          )}
        </fieldset>
        <CheckboxField
          label={t('alerts.enabled_label')}
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        {save.error && <ErrorBanner error={save.error} title={t('alerts.save_failed')} />}
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

type Dialog =
  | { kind: 'channel'; channel?: AlertChannel }
  | { kind: 'rule'; rule?: AlertRule }
  | { kind: 'delete-channel'; channel: AlertChannel }
  | { kind: 'delete-rule'; rule: AlertRule }
  | null

// UI-48: where alerts go (channels) and when they fire (rules). Only admin and owner change them.
// On a phone the editor is read-only.
export function AlertSettingsPage() {
  const { org } = useParams({ from: '/o/$org/settings/alerts' })
  const queryClient = useQueryClient()
  const manage = useAccess('alert:manage') === 'allow'
  const channels = useAlertChannels()
  const rules = useAlertRules()
  const [dialog, setDialog] = useState<Dialog>(null)
  const [tests, setTests] = useState<Record<string, { ok: boolean; error?: string }>>({})

  const test = useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.POST('/alert-channels/{channel_id}/test', { params: { path: { channel_id: id } } }),
      ),
    onSuccess: (result, id) => {
      setTests((prev) => ({ ...prev, [id]: { ok: Boolean(result.ok), error: result.error } }))
      void queryClient.invalidateQueries({ queryKey: ['alert-channels'] })
    },
  })
  const remove = useMutation({
    mutationFn: async (d: NonNullable<Dialog>) => {
      if (d.kind === 'delete-channel') {
        await unwrap(
          api.DELETE('/alert-channels/{channel_id}', {
            params: { path: { channel_id: d.channel.id ?? '' } },
          }),
        )
      } else if (d.kind === 'delete-rule') {
        await unwrap(
          api.DELETE('/alert-rules/{rule_id}', { params: { path: { rule_id: d.rule.id ?? '' } } }),
        )
      }
    },
    onSuccess: async () => {
      setDialog(null)
      await Promise.all(
        ['alert-channels', 'alert-rules'].map((k) =>
          queryClient.invalidateQueries({ queryKey: [k] }),
        ),
      )
    },
  })
  const channelName = (id: string) => channels.data?.find((c) => c.id === id)?.name ?? id
  const wide = 'hidden gap-2 sm:flex'

  return (
    <section>
      <PageHeader
        title={t('alerts.editor_title')}
        breadcrumb={
          <Link to="/o/$org/alerts" params={{ org }} className="text-primary underline">
            {t('alerts.title')}
          </Link>
        }
      />
      <div className="space-y-6">
        {!manage && <Banner>{t('alerts.read_only')}</Banner>}
        {manage && <p className="text-sm text-muted sm:hidden">{t('alerts.phone_read_only')}</p>}

        <section aria-labelledby="alert-channels" className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h2 id="alert-channels" className="text-lg font-semibold">
              {t('alerts.channels')}
            </h2>
            {manage && (
              <span className={wide}>
                <Button variant="primary" onClick={() => setDialog({ kind: 'channel' })}>
                  {t('alerts.channel_new')}
                </Button>
              </span>
            )}
          </div>
          {channels.isPending && <Skeleton />}
          {channels.isError && (
            <ErrorBanner error={channels.error} onRetry={() => void channels.refetch()} />
          )}
          {test.error && <ErrorBanner error={test.error} title={t('alerts.test_failed')} />}
          {channels.data && channels.data.length === 0 && (
            <p className="text-muted">{t('alerts.no_channels')}</p>
          )}
          {channels.data && channels.data.length > 0 && (
            <DataList
              caption={t('alerts.channels')}
              rows={channels.data}
              rowKey={(c) => c.id ?? ''}
              columns={[
                { header: t('alerts.name'), primary: true, cell: (c: AlertChannel) => c.name },
                {
                  header: t('alerts.kind'),
                  cell: (c: AlertChannel) => t(`alerts.kind.${c.kind ?? 'webhook'}`),
                },
                {
                  header: t('alerts.col_state'),
                  cell: (c: AlertChannel) => {
                    const result = tests[c.id ?? '']
                    return (
                      <span className="space-y-1">
                        <StatusBadge
                          tone={
                            c.state === 'failing'
                              ? 'danger'
                              : c.enabled === false
                                ? 'muted'
                                : 'success'
                          }
                          label={
                            c.state === 'failing'
                              ? t('alerts.channel_state.failing')
                              : c.enabled === false
                                ? t('alerts.disabled')
                                : t('alerts.channel_state.ok')
                          }
                        />
                        {c.state === 'failing' && c.last_error && (
                          <span className="block break-words text-sm text-danger">
                            {c.last_error}
                          </span>
                        )}
                        {result && (
                          <span role="status" className="block text-sm">
                            {result.ok
                              ? t('alerts.test_ok')
                              : t('alerts.test_error', { error: result.error ?? '' })}
                          </span>
                        )}
                      </span>
                    )
                  },
                },
              ]}
              actionsHeader={t('common.actions')}
              actions={
                manage
                  ? (c) => (
                      <span className={wide}>
                        <Button
                          loading={test.isPending && test.variables === c.id}
                          onClick={() => test.mutate(c.id ?? '')}
                        >
                          {t('alerts.send_test')}
                        </Button>
                        <Button onClick={() => setDialog({ kind: 'channel', channel: c })}>
                          {t('common.edit')}
                        </Button>
                        <Button
                          variant="destructive"
                          onClick={() => setDialog({ kind: 'delete-channel', channel: c })}
                        >
                          {t('common.delete')}
                        </Button>
                      </span>
                    )
                  : undefined
              }
            />
          )}
        </section>

        <section aria-labelledby="alert-rules" className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h2 id="alert-rules" className="text-lg font-semibold">
              {t('alerts.rules')}
            </h2>
            {manage && (
              <span className={wide}>
                <Button variant="primary" onClick={() => setDialog({ kind: 'rule' })}>
                  {t('alerts.rule_new')}
                </Button>
              </span>
            )}
          </div>
          {rules.isPending && <Skeleton />}
          {rules.isError && (
            <ErrorBanner error={rules.error} onRetry={() => void rules.refetch()} />
          )}
          {rules.data && rules.data.length === 0 && (
            <p className="text-muted">{t('alerts.no_rules')}</p>
          )}
          {rules.data && rules.data.length > 0 && (
            <DataList
              caption={t('alerts.rules')}
              rows={rules.data}
              rowKey={(r) => r.id ?? ''}
              columns={[
                { header: t('alerts.name'), primary: true, cell: (r: AlertRule) => r.name },
                {
                  header: t('alerts.col_type'),
                  cell: (r: AlertRule) => tState('alerts.type', r.type),
                },
                {
                  header: t('alerts.col_severity'),
                  cell: (r: AlertRule) => (
                    <StatusBadge
                      tone={severityTone(r.severity)}
                      label={tState('alerts.severity', r.severity)}
                    />
                  ),
                },
                {
                  header: t('alerts.col_channels'),
                  cell: (r: AlertRule) =>
                    (r.channel_ids ?? []).length === 0 ? (
                      <span className="text-warning">{t('alerts.no_channel_short')}</span>
                    ) : (
                      (r.channel_ids ?? []).map(channelName).join(', ')
                    ),
                },
                {
                  header: t('alerts.col_state'),
                  cell: (r: AlertRule) =>
                    r.enabled === false ? t('alerts.disabled') : t('alerts.enabled'),
                },
              ]}
              actionsHeader={t('common.actions')}
              actions={
                manage
                  ? (r) => (
                      <span className={wide}>
                        <Button onClick={() => setDialog({ kind: 'rule', rule: r })}>
                          {t('common.edit')}
                        </Button>
                        <Button
                          variant="destructive"
                          onClick={() => setDialog({ kind: 'delete-rule', rule: r })}
                        >
                          {t('common.delete')}
                        </Button>
                      </span>
                    )
                  : undefined
              }
            />
          )}
        </section>
      </div>
      {dialog?.kind === 'channel' && (
        <ChannelDialog channel={dialog.channel} onClose={() => setDialog(null)} />
      )}
      {dialog?.kind === 'rule' && (
        <RuleDialog
          rule={dialog.rule}
          channels={channels.data ?? []}
          onClose={() => setDialog(null)}
        />
      )}
      {(dialog?.kind === 'delete-channel' || dialog?.kind === 'delete-rule') && (
        <ConfirmDialog
          title={t('alerts.delete_title')}
          text={t('alerts.delete_text', {
            name:
              dialog.kind === 'delete-channel'
                ? (dialog.channel.name ?? '')
                : (dialog.rule.name ?? ''),
          })}
          confirmLabel={t('common.delete')}
          busy={remove.isPending}
          error={remove.error ? <ErrorBanner error={remove.error} /> : undefined}
          onConfirm={() => remove.mutate(dialog)}
          onClose={() => {
            remove.reset()
            setDialog(null)
          }}
        />
      )}
    </section>
  )
}
