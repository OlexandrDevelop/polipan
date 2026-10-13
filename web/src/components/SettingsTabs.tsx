// SPDX-License-Identifier: Apache-2.0

import { Link, useParams } from '@tanstack/react-router'
import { t } from '@/i18n'

const tabClass = 'whitespace-nowrap coarse:min-h-11'

// The two tabs of UI-23: Members and Teams.
export function MembersTabs({ current }: { current: 'members' | 'teams' }) {
  const { org } = useParams({ from: '/o/$org' })
  return (
    <nav aria-label={t('members.tabs')} className="mb-4">
      <ul className="tabs">
        <li>
          <Link
            to="/o/$org/settings/members"
            params={{ org }}
            aria-current={current === 'members' ? 'page' : undefined}
            className={tabClass}
          >
            {t('nav.members')}
          </Link>
        </li>
        <li>
          <Link
            to="/o/$org/settings/teams"
            params={{ org }}
            aria-current={current === 'teams' ? 'page' : undefined}
            className={tabClass}
          >
            {t('nav.teams')}
          </Link>
        </li>
      </ul>
    </nav>
  )
}
