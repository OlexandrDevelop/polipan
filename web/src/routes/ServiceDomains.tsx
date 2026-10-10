// SPDX-License-Identifier: Apache-2.0

import { isManaged } from '@/managed'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { CopyButton } from '@/components/Copy'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog, Modal } from '@/components/Modal'
import { Time } from '@/components/Time'
import {
  Banner,
  Button,
  CheckboxField,
  EmptyState,
  Skeleton,
  StatusBadge,
  TextField,
} from '@/components/ui'
import { t, tState } from '@/i18n'
import { useAccess } from '@/permissions'
import { useNodes } from '@/scope'
import type { Node, Service } from '@/scope'
import { tlsTone } from '@/status'

type Domain = components['schemas']['Domain']
interface DnsRecord {
  type: string
  name: string
  value: string
}

const HOST_PATTERN =
  /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]*[a-z0-9])?$/i

// The records the user must create. The API sends them when it knows them. If not, they come
// from the public IPs of the ingress nodes: A for IPv4, AAAA for IPv6.
function dnsRecords(domain: Domain, nodes: Node[]): DnsRecord[] {
  if (domain.dns_records && domain.dns_records.length > 0) {
    return domain.dns_records.map((r) => ({
      type: r.type ?? '',
      name: r.name ?? domain.host ?? '',
      value: r.value ?? '',
    }))
  }
  const ingress = domain.ingress_node_ids ?? []
  const wanted = nodes.filter(
    (n) => n.public_ip && (ingress.length === 0 || ingress.includes(n.id ?? '')),
  )
  return wanted.map((n) => ({
    type: n.public_ip?.includes(':') ? 'AAAA' : 'A',
    name: domain.host ?? '',
    value: n.public_ip ?? '',
  }))
}

function DnsHint({ domain, nodes }: { domain: Domain; nodes: Node[] }) {
  const records = dnsRecords(domain, nodes)
  if (records.length === 0) return <p className="text-sm text-muted">{t('domains.dns_no_ip')}</p>
  return (
    <div className="space-y-2">
      <p className="text-sm">{t('domains.dns_hint')}</p>
      <ul className="space-y-2">
        {records.map((r) => (
          <li key={`${r.type}-${r.value}`} className="flex flex-wrap items-center gap-2">
            <code className="break-all rounded-sm bg-surface px-2 py-1 font-mono text-sm">
              {r.type} {r.name} {r.value}
            </code>
            <CopyButton text={r.value} label={t('domains.copy_value')} />
          </li>
        ))}
      </ul>
    </div>
  )
}

function TlsState({ domain }: { domain: Domain }) {
  return (
    <div className="space-y-1">
      <StatusBadge
        tone={tlsTone(domain.tls_status)}
        label={tState('status.tls', domain.tls_status)}
      />
      {domain.tls_status === 'pending' && (
        <p className="text-sm text-muted">{t('domains.tls_pending')}</p>
      )}
      {domain.tls_status === 'failed' && (
        <p className="break-words text-sm text-danger">
          {t('domains.tls_failed')} {domain.tls_error}
        </p>
      )}
      {domain.tls_status === 'issued' && domain.tls_expires_at && (
        <p className="text-sm text-muted">
          {t('domains.tls_expires')} <Time value={domain.tls_expires_at} />
        </p>
      )}
    </div>
  )
}

function AddDomainDialog({ service, onClose }: { service: Service; onClose: () => void }) {
  const queryClient = useQueryClient()
  const defaultPort = typeof service.config?.port === 'number' ? String(service.config.port) : ''
  const [host, setHost] = useState('')
  const [port, setPort] = useState(defaultPort)
  const [https, setHttps] = useState(true)
  const [submitted, setSubmitted] = useState(false)

  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/domains', {
          params: { path: { service_id: service.id ?? '' } },
          body: {
            host: host.trim().toLowerCase(),
            https,
            path_prefix: '/',
            ...(port ? { container_port: Number(port) } : {}),
          },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['domains', service.id] })
      onClose()
    },
  })

  const hostError = HOST_PATTERN.test(host.trim()) ? null : t('domains.host_invalid')
  const portNumber = Number(port)
  const portError =
    port === '' || (Number.isInteger(portNumber) && portNumber >= 1 && portNumber <= 65535)
      ? null
      : t('service.port_invalid')

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (!hostError && !portError) create.mutate()
  }

  return (
    <Modal title={t('domains.add_title')} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        {create.error && <ErrorBanner error={create.error} title={t('domains.add_failed')} />}
        <TextField
          label={t('domains.host')}
          hint={t('domains.host_hint')}
          required
          autoFocus
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          error={submitted ? hostError : null}
          value={host}
          onChange={(e) => setHost(e.target.value)}
        />
        <TextField
          label={t('domains.port')}
          hint={t('domains.port_hint')}
          type="number"
          inputMode="numeric"
          error={submitted ? portError : null}
          value={port}
          onChange={(e) => setPort(e.target.value)}
        />
        <CheckboxField
          label={t('domains.https')}
          hint={t('domains.https_hint')}
          checked={https}
          onChange={(e) => setHttps(e.target.checked)}
        />
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type="submit" variant="primary" loading={create.isPending}>
            {t('domains.add')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

// UI-12 (basic): domains of a service, the TLS state and the DNS records to create.
export function ServiceDomains({
  service,
  environmentProtected,
}: {
  service: Service
  environmentProtected?: boolean
}) {
  const serviceId = service.id ?? ''
  const queryClient = useQueryClient()
  const baseAccess = useAccess('domain:manage', environmentProtected)
  const managed = isManaged(service.git_managed_fields, 'domains')
  const access = managed ? 'hidden' : baseAccess
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<Domain | null>(null)
  const nodes = useNodes()

  const domains = useQuery({
    queryKey: ['domains', serviceId],
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/domains', { params: { path: { service_id: serviceId } } }),
      ),
    // The certificate state changes without a user action: read again while one is pending.
    refetchInterval: (query) =>
      query.state.data?.some((d) => d.tls_status === 'pending') ? 5_000 : false,
  })

  const remove = useMutation({
    mutationFn: (domain: Domain) =>
      unwrap(
        api.DELETE('/domains/{domain_id}', { params: { path: { domain_id: domain.id ?? '' } } }),
      ),
    onSuccess: async () => {
      setRemoving(null)
      await queryClient.invalidateQueries({ queryKey: ['domains', serviceId] })
    },
  })

  const addButton =
    access === 'hidden' ? null : (
      <Button
        variant="primary"
        reason={access === 'blocked' ? t('common.needs_admin') : undefined}
        onClick={() => setAdding(true)}
      >
        {t('domains.add')}
      </Button>
    )

  return (
    <div className="space-y-4">
      {managed && <Banner>{t('settings.managed')}</Banner>}
      {nodes.isError && (
        <Banner variant="warning" title={t('domains.nodes_failed')}>
          {t('domains.nodes_failed_next')}
        </Banner>
      )}
      {domains.isPending && <Skeleton />}
      {domains.isError && (
        <ErrorBanner error={domains.error} onRetry={() => void domains.refetch()} />
      )}
      {domains.data && domains.data.length === 0 && (
        <EmptyState
          title={t('domains.empty_title')}
          text={t('domains.empty_text')}
          action={addButton}
        />
      )}
      {domains.data && domains.data.length > 0 && (
        <>
          {addButton && <div className="flex justify-end">{addButton}</div>}
          <DataList
            caption={t('domains.caption')}
            rows={domains.data}
            rowKey={(d) => d.id ?? ''}
            columns={[
              {
                header: t('domains.host'),
                primary: true,
                cell: (d) => (
                  <span className="break-all">
                    {d.host}
                    {d.path_prefix && d.path_prefix !== '/' ? d.path_prefix : ''}
                  </span>
                ),
              },
              { header: t('domains.tls'), cell: (d) => <TlsState domain={d} /> },
              { header: t('domains.port'), cell: (d) => d.container_port ?? '-', cardHidden: true },
              {
                header: t('domains.dns'),
                cell: (d) => <DnsHint domain={d} nodes={nodes.data ?? []} />,
              },
            ]}
            actionsHeader={t('common.actions')}
            actions={(d) =>
              access === 'hidden' ? null : (
                <Button
                  variant="destructive"
                  reason={access === 'blocked' ? t('common.needs_admin') : undefined}
                  onClick={() => setRemoving(d)}
                >
                  {t('domains.remove')}
                </Button>
              )
            }
          />
        </>
      )}
      {adding && <AddDomainDialog service={service} onClose={() => setAdding(false)} />}
      {removing && (
        <ConfirmDialog
          title={t('domains.remove_title', { host: removing.host ?? '' })}
          text={t('domains.remove_text', { host: removing.host ?? '' })}
          confirmLabel={t('domains.remove_confirm', { host: removing.host ?? '' })}
          busy={remove.isPending}
          error={
            remove.error && <ErrorBanner error={remove.error} title={t('domains.remove_failed')} />
          }
          onConfirm={() => remove.mutate(removing)}
          onClose={() => setRemoving(null)}
        />
      )}
    </div>
  )
}
