// SPDX-License-Identifier: Apache-2.0

import { ApiError } from '@/api/errors'
import { t } from '@/i18n'

export interface ErrorText {
  heading: string
  next: string
  // What the server said, with the request id. Empty when there is nothing more to say.
  detail: string
}

// Says what failed and what to do next. The server message is only the detail (quality, 2).
export function describeError(error: unknown): ErrorText {
  const api = error instanceof ApiError ? error : null
  let heading = t('error.generic')
  let next = t('error.next_retry')
  if (api?.status === 0) {
    heading = t('error.unreachable')
    next = t('error.unreachable_next')
  } else if (api?.status === 403) {
    heading = t('error.forbidden')
    next = t('error.forbidden_next')
  } else if (api?.status === 404) {
    heading = t('error.not_found')
    next = t('error.not_found_next')
  } else if (api?.status === 409) {
    heading = t('error.conflict')
    next = t('error.conflict_next')
  } else if (api?.status === 400 || api?.status === 422) {
    heading = t('error.invalid')
    next = t('error.invalid_next')
  }
  const detail =
    api && api.status !== 0
      ? `${api.message}${api.requestId ? ` (${t('error.request_id', { id: api.requestId })})` : ''}`
      : ''
  return { heading, next, detail }
}
