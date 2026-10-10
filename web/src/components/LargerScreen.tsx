// SPDX-License-Identifier: Apache-2.0

import { CopyButton } from '@/components/Copy'
import { Banner } from '@/components/ui'
import { t } from '@/i18n'

// Shown on a phone instead of a desktop-only function (responsive, section 5).
export function LargerScreenNotice({ className = 'sm:hidden' }: { className?: string }) {
  return (
    <div className={className}>
      <Banner
        title={t('common.larger_screen')}
        action={<CopyButton text={window.location.href} label={t('common.copy_link')} />}
      >
        {t('common.larger_screen_text')}
      </Banner>
    </div>
  )
}
