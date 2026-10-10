// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { ConfirmDialog } from '@/components/Modal'
import { Button } from '@/components/ui'
import { t } from '@/i18n'
import { accessFor } from '@/permissions'
import { useMe } from '@/auth'

type Deployment = components['schemas']['Deployment']

// Cancel is refused by the server from "switching" on (deployment engine, section 10).
const NOT_CANCELLABLE = new Set(['switching', 'draining'])

// The "Cancel deployment" button with its confirm dialog (UI-08, UI-34).
export function CancelDeployment({
  deployment,
  environmentProtected,
  onDone,
}: {
  deployment: Deployment
  environmentProtected?: boolean
  onDone?: () => void
}) {
  const queryClient = useQueryClient()
  const me = useMe()
  const [open, setOpen] = useState(false)
  const access = accessFor(me.data?.org_role, 'deployment:cancel', environmentProtected)

  const cancel = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/deployments/{deployment_id}/cancel', {
          params: { path: { deployment_id: deployment.id ?? '' } },
        }),
      ),
    onSuccess: async () => {
      setOpen(false)
      await Promise.all(
        ['deployment', 'deployments', 'service'].map((key) =>
          queryClient.invalidateQueries({ queryKey: [key] }),
        ),
      )
      onDone?.()
    },
  })

  if (access === 'hidden') return null
  const reason =
    access === 'blocked'
      ? t('common.needs_admin')
      : NOT_CANCELLABLE.has(deployment.state ?? '')
        ? t('deployment.cancel_not_now')
        : undefined
  const notCancellable =
    cancel.error instanceof ApiError && cancel.error.code === 'deployment_not_cancellable'

  return (
    <>
      <Button
        variant="destructive"
        reason={reason}
        onClick={() => {
          // The screen may be old after a lost stream. Read the state first, then ask.
          void queryClient
            .refetchQueries({ queryKey: ['deployment', deployment.id] })
            .finally(() => setOpen(true))
        }}
      >
        {t('deployment.cancel')}
      </Button>
      {open && (
        <ConfirmDialog
          title={t('deployment.cancel_title')}
          text={t('deployment.cancel_text')}
          confirmLabel={t('deployment.cancel')}
          busy={cancel.isPending}
          error={
            cancel.error && (
              <ErrorBanner
                error={cancel.error}
                title={
                  notCancellable ? t('deployment.cancel_not_now') : t('deployment.cancel_failed')
                }
              />
            )
          }
          onConfirm={() => cancel.mutate()}
          onClose={() => {
            cancel.reset()
            setOpen(false)
          }}
        />
      )}
    </>
  )
}
