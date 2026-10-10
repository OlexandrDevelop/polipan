// SPDX-License-Identifier: Apache-2.0

import { useBlocker } from '@tanstack/react-router'
import { useRef } from 'react'
import { ConfirmDialog } from '@/components/Modal'
import { t } from '@/i18n'

// Asks before the user leaves a form with unsaved data (REQ-UI-031): the router blocker for
// links and the browser back button, beforeunload for reload and tab close.
// Call allowLeave() just before a navigation that saves the data.
export function useUnsavedGuard(dirty: boolean) {
  const allowed = useRef(false)
  const blocker = useBlocker({
    shouldBlockFn: ({ current, next }) =>
      dirty && !allowed.current && current.pathname !== next.pathname,
    enableBeforeUnload: () => dirty && !allowed.current,
    withResolver: true,
  })

  const dialog =
    blocker.status === 'blocked' ? (
      <ConfirmDialog
        title={t('unsaved.title')}
        text={t('unsaved.text')}
        confirmLabel={t('unsaved.leave')}
        onConfirm={blocker.proceed}
        onClose={blocker.reset}
      />
    ) : null

  return {
    dialog,
    allowLeave: () => {
      allowed.current = true
    },
  }
}
