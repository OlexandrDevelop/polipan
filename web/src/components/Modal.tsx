// SPDX-License-Identifier: Apache-2.0

import { useEffect, useId, useRef } from 'react'
import type { ReactNode } from 'react'
import { Button } from '@/components/ui'
import { t } from '@/i18n'

interface ModalProps {
  title: string
  onClose: () => void
  children: ReactNode
}

// A dialog on the native <dialog> element: it traps focus, Esc closes it and the focus
// goes back to the opener. On a phone it is a bottom sheet.
export function Modal({ title, onClose, children }: ModalProps) {
  const ref = useRef<HTMLDialogElement>(null)
  const titleId = useId()

  useEffect(() => {
    const dialog = ref.current
    if (dialog && !dialog.open) dialog.showModal()
  }, [])

  return (
    <dialog
      ref={ref}
      aria-labelledby={titleId}
      onClose={onClose}
      className="m-auto max-h-[90vh] w-full max-w-[560px] overflow-auto rounded-lg border border-border-strong bg-bg p-5 text-text backdrop:bg-black/50 max-sm:mb-0 max-sm:max-w-none max-sm:rounded-b-none lg:max-w-[640px]"
    >
      <div className="mb-4 flex items-start justify-between gap-4">
        <h2 id={titleId} className="text-lg font-semibold">
          {title}
        </h2>
        <Button variant="ghost" onClick={onClose}>
          {t('common.close')}
        </Button>
      </div>
      {children}
    </dialog>
  )
}

interface ConfirmDialogProps {
  title: string
  text: string
  confirmLabel: string
  busy?: boolean
  error?: ReactNode
  onConfirm: () => void
  onClose: () => void
}

// A destructive action names the object and the effect. Cancel comes first, so it has the focus.
export function ConfirmDialog({
  title,
  text,
  confirmLabel,
  busy,
  error,
  onConfirm,
  onClose,
}: ConfirmDialogProps) {
  return (
    <Modal title={title} onClose={onClose}>
      <div className="space-y-4">
        <p>{text}</p>
        {error}
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button variant="destructive" loading={busy} onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </div>
      </div>
    </Modal>
  )
}
