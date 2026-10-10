// SPDX-License-Identifier: Apache-2.0

import { useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { Modal } from '@/components/Modal'
import { Time } from '@/components/Time'
import { Button } from '@/components/ui'
import { findServicePath } from '@/live'
import type { Notice } from '@/live'
import { t, tState } from '@/i18n'

// UI-46 (deployments part): deployments that failed or were rolled back, from the live stream.
// Alerts come with the alerts milestone. The list is kept while the page stays open.
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
  return (
    <>
      <Button
        variant="ghost"
        aria-label={t('bell.label', { count: notices.length })}
        onClick={() => setOpen(true)}
      >
        {t('bell.title')}
        {notices.length > 0 && (
          <span className="rounded-full border border-danger px-1.5 text-xs">{notices.length}</span>
        )}
      </Button>
      {open && (
        <Modal title={t('bell.title')} onClose={() => setOpen(false)}>
          {notices.length === 0 ? (
            <p className="text-muted">{t('bell.empty')}</p>
          ) : (
            <div className="space-y-4">
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
              <Button onClick={onClear}>{t('bell.clear')}</Button>
            </div>
          )}
        </Modal>
      )}
    </>
  )
}
