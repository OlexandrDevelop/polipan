// SPDX-License-Identifier: Apache-2.0

import { formatDateTime } from '@/i18n'

// Times come from the API in UTC. They show in the time zone of the user, with UTC in the tooltip.
export function Time({ value }: { value?: string }) {
  if (!value) return <span>-</span>
  return (
    <time dateTime={value} title={new Date(value).toISOString()}>
      {formatDateTime(value)}
    </time>
  )
}
