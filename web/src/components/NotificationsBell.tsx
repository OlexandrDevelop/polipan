// SPDX-License-Identifier: Apache-2.0

import { useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useAlertInstances } from '@/alerts'
import { Modal } from '@/components/Modal'
import { MuteDialog } from '@/components/MuteDialog'
import { Time } from '@/components/Time'
import { Button, StatusBadge } from '@/components/ui'
import { findServicePath } from '@/live'
import type { Notice } from '@/live'
import { t, tState } from '@/i18n'
import { useAccess } from '@/permissions'
import { severityTone } from '@/alerts'

// UI-46: firing alerts (listAlertInstances, refreshed by the alert events of the live stream)
// and deployments that failed or were rolled back. The deployment list is kept while the page
// stays open. The badge counts both.
export function NotificationsBell({
  org,
  notices,
  onClear,
}: {
  org: string
  notices: Notice[]
  onClear: () => void
}) {
  const [open, setOpen] = useState(false)
  const queryClient = useQueryClient()
  const alerts = useAlertInstances()
  const manage = useAccess('alert:manage') === 'allow'
  const [muting, setMuting] = useState<{ id: string; name: string } | null>(null)
  const firing = (alerts.data ?? []).filter((a) => a.state === 'firing')
  const count = firing.length + notices.length
  return (
    <>
      <Button variant="ghost" aria-label={t('bell.label', { count })} onClick={() => setOpen(true)}>
        {t('bell.title')}
        {count > 0 && (
          <span className="rounded-full border border-danger px-1.5 text-xs">{count}</span>
        )}
      </Button>
      {open && (
        <Modal title={t('bell.title')} onClose={() => setOpen(false)}>
          {count === 0 ? (
            <p className="text-muted">{t('bell.empty')}</p>
          ) : (
            <div className="space-y-4">
              {firing.length > 0 && (
                <section aria-label={t('bell.alerts')} className="space-y-2">
                  <h3 className="font-semibold">{t('bell.alerts')}</h3>
                  <ul className="space-y-3">
                    {firing.map((a) => (
                      <li key={a.id} className="space-y-1 text-sm">
                        <p className="flex flex-wrap items-center gap-2 font-semibold">
                          <StatusBadge
                            tone={severityTone(a.severity)}
                            label={tState('alerts.severity', a.severity)}
                          />
                          {a.rule_name}
                        </p>
                        <p className="text-muted">
                          {t('alerts.col_since')}: <Time value={a.first_fired_at} />
                        </p>
                        <p className="flex flex-wrap items-center gap-3">
                          <Link
                            to="/o/$org/alerts"
                            params={{ org }}
                            onClick={() => setOpen(false)}
                            className="text-primary underline"
                          >
                            {t('bell.open_alerts')}
                          </Link>
                          {manage && a.rule_id && !a.muted && (
                            <Button
                              onClick={() => {
                                setOpen(false)
                                setMuting({ id: a.rule_id ?? '', name: a.rule_name ?? '' })
                              }}
                            >
                              {t('alerts.mute')}
                            </Button>
                          )}
                        </p>
                      </li>
                    ))}
                  </ul>
                </section>
              )}
              {notices.length > 0 && <h3 className="font-semibold">{t('bell.deployments')}</h3>}
              <ul className="space-y-3">
                {notices.map((n) => {
                  const found = findServicePath(queryClient, n.serviceId)
                  return (
                    <li key={n.id} className="space-y-1 text-sm">
                      <p className="font-semibold">
                        {t('bell.item', {
                          service: found?.name ?? t('bell.a_service'),
                          state: tState('status.deployment', n.state).toLowerCase(),
                        })}
                      </p>
                      {n.reason && (
                        <p className="text-muted">{tState('deployment.reason', n.reason)}</p>
                      )}
                      <p className="text-muted">
                        <Time value={n.time} />
                      </p>
                      {found ? (
                        <Link
                          to="/o/$org/p/$project/e/$env/s/$service/deployments/$deployment"
                          params={{ org, ...found.path, deployment: n.deploymentId }}
                          onClick={() => setOpen(false)}
                          className="text-primary underline"
                        >
                          {t('bell.open')}
                        </Link>
                      ) : (
                        <Link
                          to="/o/$org/deployments"
                          params={{ org }}
                          onClick={() => setOpen(false)}
                          className="text-primary underline"
                        >
                          {t('bell.open_list')}
                        </Link>
                      )}
                    </li>
                  )
                })}
              </ul>
              {notices.length > 0 && <Button onClick={onClear}>{t('bell.clear')}</Button>}
            </div>
          )}
        </Modal>
      )}
      {muting && (
        <MuteDialog ruleId={muting.id} ruleName={muting.name} onClose={() => setMuting(null)} />
      )}
    </>
  )
}
