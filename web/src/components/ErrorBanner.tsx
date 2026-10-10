// SPDX-License-Identifier: Apache-2.0

import { ApiError } from '@/api/errors'
import { Banner, Button } from '@/components/ui'
import { t } from '@/i18n'

interface ErrorBannerProps {
  error: unknown
  onRetry?: () => void
  // Text for a 401 or 422 style answer of a form, instead of the generic one.
  title?: string
}

// Says what failed, why, and the next step. The server message is only the detail (quality, 2).
export function ErrorBanner({ error, onRetry, title }: ErrorBannerProps) {
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
  }
  return (
    <Banner
      variant="danger"
      title={title ?? heading}
      action={onRetry && <Button onClick={onRetry}>{t('common.retry')}</Button>}
    >
      <p>{next}</p>
      {api && api.status !== 0 && (
        <p className="text-muted">
          {api.message}
          {api.requestId ? ` (${t('error.request_id', { id: api.requestId })})` : ''}
        </p>
      )}
    </Banner>
  )
}
