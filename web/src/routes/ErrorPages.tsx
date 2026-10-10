// SPDX-License-Identifier: Apache-2.0

import { Link } from '@tanstack/react-router'
import { PageHeader } from '@/components/PageHeader'
import { t } from '@/i18n'

// UI-47: what happened, why, and one next step.
export function NotFoundPage() {
  return (
    <div className="mx-auto max-w-prose space-y-4 p-6 text-center">
      <PageHeader title={t('error.page_not_found')} />
      <p>{t('error.page_not_found_text')}</p>
      <Link
        to="/"
        className="inline-flex min-h-8 items-center rounded-sm border border-primary bg-primary px-3 py-1 text-sm font-semibold text-on-primary coarse:min-h-11"
      >
        {t('error.go_dashboard')}
      </Link>
    </div>
  )
}
