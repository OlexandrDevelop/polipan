// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { Banner, Button, SelectField, Skeleton } from '@/components/ui'
import { t } from '@/i18n'
import { useNodes } from '@/scope'
import type { Node, Service } from '@/scope'
import { newKey } from '../idempotency'

interface Placement {
  nodes?: string[]
  labels?: Record<string, string>
}

// Why a node cannot take the service, or undefined. The scheduler answers the same way on the server (09 section 7).
function whyNot(node: Node, service: Service, current: Set<string>): string | undefined {
  const placement = ((service.config ?? {}) as { placement?: Placement }).placement ?? {}
  if (node.state !== 'ready') return t('move.not_ready', { state: node.state ?? '' })
  if (current.has(node.id ?? '')) return t('move.already_there')
  if (placement.nodes && placement.nodes.length > 0 && !placement.nodes.includes(node.name ?? '')) {
    return t('move.not_in_names')
  }
  for (const [k, v] of Object.entries(placement.labels ?? {})) {
    if ((node.labels ?? {})[k] !== v) return t('move.missing_label', { label: `${k}=${v}` })
  }
  return undefined
}

// UI-20: move a service to another node. A service with a volume copies its data and is stopped while it does.
export function MoveDialog({ service, onClose }: { service: Service; onClose: () => void }) {
  const params = useParams({ strict: false })
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const nodes = useNodes()
  const serviceId = service.id ?? ''
  const volumes = useQuery({
    queryKey: ['volumes', serviceId],
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/volumes', { params: { path: { service_id: serviceId } } }),
      ),
  })
  const [target, setTarget] = useState('')
  const [key] = useState(() => newKey())
  const local = (volumes.data ?? []).filter((v) => v.kind !== 'tmpfs')
  const current = new Set(
    (service.placements ?? []).filter((p) => p.role !== 'stopped').map((p) => p.node_id ?? ''),
  )

  const move = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/move', {
          params: { path: { service_id: serviceId }, header: { 'Idempotency-Key': key } },
          body: {
            target_node_id: target,
            ...(local.length > 0 ? { volume_copy: 'backup' as const } : {}),
          },
        }),
      ),
    onSuccess: async (dep) => {
      await queryClient.invalidateQueries({ queryKey: ['service', serviceId] })
      if (dep.id && params.org && params.project && params.env && params.service) {
        await navigate({
          to: '/o/$org/p/$project/e/$env/s/$service/deployments/$deployment',
          params: {
            org: params.org,
            project: params.project,
            env: params.env,
            service: params.service,
            deployment: dep.id,
          },
        })
      }
      onClose()
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    if (target) move.mutate()
  }

  return (
    <Modal title={t('move.title', { name: service.name ?? '' })} onClose={onClose}>
      {(nodes.isPending || volumes.isPending) && <Skeleton lines={2} />}
      {nodes.isError && <ErrorBanner error={nodes.error} onRetry={() => void nodes.refetch()} />}
      {nodes.data && (
        <form onSubmit={submit} className="space-y-4">
          <SelectField
            label={t('move.target')}
            value={target}
            onChange={(e) => setTarget(e.target.value)}
          >
            <option value="">{t('move.choose')}</option>
            {nodes.data.map((n) => {
              const reason = whyNot(n, service, current)
              return (
                <option key={n.id} value={n.id} disabled={reason !== undefined}>
                  {n.name}
                  {reason ? ` (${reason})` : ''}
                </option>
              )
            })}
          </SelectField>
          {local.length > 0 ? (
            <Banner variant="warning" title={t('move.data_title')}>
              <p>{t('move.data_text')}</p>
              <ul className="mt-1 list-disc pl-5 text-sm">
                {local.map((v) => (
                  <li key={v.id}>
                    {v.name}
                    {v.usage_bytes
                      ? ` · ${Math.max(1, Math.round(v.usage_bytes / (1 << 20)))} MiB`
                      : ''}
                  </li>
                ))}
              </ul>
              <p className="mt-1 text-sm">{t('move.data_backup')}</p>
            </Banner>
          ) : (
            <Banner>{t('move.no_downtime')}</Banner>
          )}
          {move.error && <ErrorBanner error={move.error} title={t('move.failed')} />}
          <div className="flex flex-wrap justify-end gap-2">
            <Button onClick={onClose}>{t('common.cancel')}</Button>
            <Button
              type="submit"
              variant="primary"
              loading={move.isPending}
              reason={target ? undefined : t('move.choose')}
            >
              {t('move.submit')}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  )
}
