// SPDX-License-Identifier: Apache-2.0

import { Banner, Button } from '@/components/ui'
import { describeError } from '@/errorText'
import { t } from '@/i18n'

interface ErrorBannerProps {
  error: unknown
  onRetry?: () => void
  // Text for a 401 or 422 style answer of a form, instead of the generic one.
  title?: string
}

// Says what failed, why, and the next step. The server message is only the detail (quality, 2).
export function ErrorBanner({ error, onRetry, title }: ErrorBannerProps) {
  const text = describeError(error)
  return (
    <Banner
      variant="danger"
      title={title ?? text.heading}
      action={onRetry && <Button onClick={onRetry}>{t('common.retry')}</Button>}
    >
      <p>{text.next}</p>
      {text.detail && <p className="text-muted">{text.detail}</p>}
    </Banner>
  )
}
