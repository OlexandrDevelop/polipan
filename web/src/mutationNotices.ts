// SPDX-License-Identifier: Apache-2.0

import { describeError } from '@/errorText'
import { t } from '@/i18n'
import { mutationHooks } from '@/queryClient'
import { notify } from '@/toast'

// A green message after a success, a red one with the reason after a failure.
export function installMutationNotices(): void {
  mutationHooks.success = (meta) => {
    if (meta?.quiet) return
    notify('success', t(meta?.success ?? 'toast.done'))
  }
  mutationHooks.error = (error) => {
    const text = describeError(error)
    notify('danger', text.heading, [text.detail, text.next].filter(Boolean).join(' '))
  }
}
