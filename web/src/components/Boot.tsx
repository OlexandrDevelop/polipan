// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react'
import { calmNow } from '@/display'
import { t } from '@/i18n'

const KEY = 'polipan.boot'
const LINES = ['boot.api', 'boot.session', 'boot.stream'] as const
const TOTAL_MS = 600

function shouldBoot(): boolean {
  try {
    if (calmNow() || sessionStorage.getItem(KEY)) return false
    sessionStorage.setItem(KEY, '1')
    return true
  } catch {
    return false
  }
}

// The boot lines (terminal-style, section 5). Once per browser session, at most 600 ms, any key or
// click skips it, never in Calm mode. It only paints over the page: the app loads under it, and it
// does not take clicks or the focus. Only opacity animates.
export function Boot() {
  const [show, setShow] = useState(shouldBoot)
  useEffect(() => {
    if (!show) return
    const done = () => setShow(false)
    const timer = window.setTimeout(done, TOTAL_MS)
    window.addEventListener('keydown', done, { once: true })
    window.addEventListener('pointerdown', done, { once: true })
    return () => {
      window.clearTimeout(timer)
      window.removeEventListener('keydown', done)
      window.removeEventListener('pointerdown', done)
    }
  }, [show])
  if (!show) return null
  return (
    <div aria-hidden="true" className="boot pointer-events-none" data-testid="boot">
      {LINES.map((key, i) => (
        <p key={key} className="boot-line" style={{ ['--i' as string]: i }}>
          [ <span className="text-success">ok</span> ] {t(key)}
        </p>
      ))}
      <p className="boot-line mt-2 text-xs" style={{ ['--i' as string]: LINES.length }}>
        {t('boot.skip')}
      </p>
    </div>
  )
}
