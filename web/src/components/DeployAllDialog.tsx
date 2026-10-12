// SPDX-License-Identifier: Apache-2.0

import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { Modal } from '@/components/Modal'
import { Button, CheckboxField, StatusBadge } from '@/components/ui'
import { t } from '@/i18n'
import { newKey } from '@/idempotency'
import type { Service } from '@/scope'

type Row = 'idle' | 'running' | 'started' | 'failed'

// Databases and images start first, the services that are built from Git last.
function rank(service: Service): number {
  if (service.type === 'database') return 0
  return service.source_type === 'git' ? 2 : 1
}

// A service with an entrypoint and no port is most likely a command that runs once (composer, artisan). It is not
// selected, but the user can select it.
function looksOneOff(service: Service): boolean {
  const config = (service.config ?? {}) as { entrypoint?: unknown[]; port?: number }
  return Array.isArray(config.entrypoint) && config.entrypoint.length > 0 && !config.port
}

// Starts the deployment of many services, one after the other.
export function DeployAllDialog({
  services,
  onClose,
}: {
  services: Service[]
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const list = [...services].sort(
    (a, b) => rank(a) - rank(b) || (a.name ?? '').localeCompare(b.name ?? ''),
  )
  const [picked, setPicked] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(list.map((s) => [s.id ?? '', !looksOneOff(s)])),
  )
  const [state, setState] = useState<Record<string, Row>>({})
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const chosen = list.filter((s) => picked[s.id ?? ''])

  async function start() {
    setBusy(true)
    for (const service of chosen) {
      const id = service.id ?? ''
      setState((s) => ({ ...s, [id]: 'running' }))
      try {
        await unwrap(
          api.POST('/services/{service_id}/deploy', {
            params: { path: { service_id: id }, header: { 'Idempotency-Key': newKey() } },
            body: { dry_run: false },
          }),
        )
        setState((s) => ({ ...s, [id]: 'started' }))
      } catch (error) {
        setState((s) => ({ ...s, [id]: 'failed' }))
        setErrors((e) => ({
          ...e,
          [id]: error instanceof ApiError ? error.message : t('error.generic'),
        }))
      }
    }
    setBusy(false)
    await queryClient.invalidateQueries({ queryKey: ['services'] })
  }

  const started = Object.keys(state).length > 0
  return (
    <Modal title={t('deployall.title')} onClose={onClose}>
      <div className="space-y-4">
        <p className="text-muted">{t('deployall.text')}</p>
        <ul className="space-y-1">
          {list.map((service) => {
            const id = service.id ?? ''
            const row = state[id] ?? 'idle'
            return (
              <li key={id} className="flex flex-wrap items-center justify-between gap-2">
                <CheckboxField
                  label={service.name ?? id}
                  checked={Boolean(picked[id])}
                  disabled={busy || started}
                  onChange={(e) => setPicked((p) => ({ ...p, [id]: e.target.checked }))}
                />
                {row !== 'idle' && (
                  <StatusBadge
                    tone={row === 'failed' ? 'danger' : row === 'started' ? 'success' : 'warning'}
                    label={t(`deployall.${row}` as 'deployall.started')}
                  />
                )}
                {errors[id] && <span className="w-full text-sm text-danger">{errors[id]}</span>}
              </li>
            )
          })}
        </ul>
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.close')}</Button>
          {!started && (
            <Button
              variant="primary"
              loading={busy}
              reason={chosen.length === 0 ? t('deployall.pick') : undefined}
              onClick={() => void start()}
            >
              {t('deployall.start', { count: chosen.length })}
            </Button>
          )}
        </div>
      </div>
    </Modal>
  )
}
