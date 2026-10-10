// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react'
import { Button } from '@/components/ui'
import { t } from '@/i18n'

// The clipboard API needs a secure page. On plain http the old way still works.
export async function copyText(text: string): Promise<void> {
  if (navigator.clipboard) {
    await navigator.clipboard.writeText(text)
    return
  }
  const area = document.createElement('textarea')
  area.value = text
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.appendChild(area)
  area.select()
  document.execCommand('copy')
  area.remove()
}

export function CopyButton({ text, label }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <>
      <Button
        onClick={() => {
          copyText(text)
            .then(() => {
              setCopied(true)
              setTimeout(() => setCopied(false), 2000)
            })
            .catch(() => setCopied(false))
        }}
      >
        {label ?? t('common.copy')}
      </Button>
      <span role="status" className="sr-only">
        {copied ? t('common.copied') : ''}
      </span>
    </>
  )
}

// A block of text that scrolls sideways in its own box, with a copy button.
export function CodeBlock({ text, label }: { text: string; label: string }) {
  return (
    <div className="space-y-2">
      <pre
        tabIndex={0}
        aria-label={label}
        className="overflow-x-auto rounded-sm border border-border bg-surface p-3 font-mono text-sm"
      >
        {text}
      </pre>
      <CopyButton text={text} />
    </div>
  )
}
