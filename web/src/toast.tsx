// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useState } from 'react'
import { t } from '@/i18n'

export type ToastKind = 'success' | 'danger' | 'info'

interface ToastItem {
  id: number
  kind: ToastKind
  title: string
  detail?: string
}

type Listener = (toast: Omit<ToastItem, 'id'>) => void

let listener: Listener | null = null

// Shows a message in the corner of the screen. It works from any place, also outside React.
export function notify(kind: ToastKind, title: string, detail?: string): void {
  listener?.({ kind, title, detail })
}

const SHOW_MS: Record<ToastKind, number> = { success: 5000, info: 5000, danger: 15000 }
const MAX_TOASTS = 4

const style: Record<ToastKind, string> = {
  success: 'border-success',
  info: 'border-info',
  danger: 'border-danger',
}
const glyph: Record<ToastKind, string> = { success: '●', info: '⚡', danger: '✕' }
const glyphColor: Record<ToastKind, string> = {
  success: 'text-success',
  info: 'text-info',
  danger: 'text-danger',
}

let nextId = 1

// The place for the messages of actions. It stays in view when the page is scrolled.
export function ToastRegion() {
  const [items, setItems] = useState<ToastItem[]>([])
  const remove = useCallback((id: number) => setItems((all) => all.filter((x) => x.id !== id)), [])

  useEffect(() => {
    listener = (toast) => {
      const id = nextId++
      setItems((all) =>
        [
          ...all.filter((x) => !(x.title === toast.title && x.detail === toast.detail)),
          { ...toast, id },
        ].slice(-MAX_TOASTS),
      )
      window.setTimeout(() => remove(id), SHOW_MS[toast.kind])
    }
    return () => {
      listener = null
    }
  }, [remove])

  return (
    <div
      role="region"
      aria-label={t('toast.region')}
      className="pointer-events-none fixed right-3 top-14 z-50 flex w-[min(26rem,calc(100vw-1.5rem))] flex-col gap-2 max-sm:top-16"
    >
      {items.map((item) => (
        <div
          key={item.id}
          role={item.kind === 'danger' ? 'alert' : 'status'}
          data-testid="toast"
          data-kind={item.kind}
          className={`pointer-events-auto flex items-start gap-3 border border-l-[6px] bg-surface p-3 ${style[item.kind]}`}
        >
          <span aria-hidden="true" className={glyphColor[item.kind]}>
            {glyph[item.kind]}
          </span>
          <div className="min-w-0 flex-1 space-y-1">
            <p className="break-words font-bold">{item.title}</p>
            {item.detail && <p className="break-words text-muted">{item.detail}</p>}
          </div>
          <button
            type="button"
            aria-label={t('common.close')}
            className="inline-flex min-h-6 min-w-6 items-center justify-center text-muted hover:bg-raised hover:text-text coarse:min-h-11 coarse:min-w-11"
            onClick={() => remove(item.id)}
          >
            [x]
          </button>
        </div>
      ))}
    </div>
  )
}
