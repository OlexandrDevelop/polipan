// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef } from 'react'
import type { ReactNode } from 'react'

interface PageHeaderProps {
  title: string
  // The parts of the document title that come after the page title.
  context?: string[]
  badges?: ReactNode
  breadcrumb?: ReactNode
  actions?: ReactNode
}

// The one h1 of a page. It sets the document title and takes the focus when the page opens,
// so a screen reader announces the new page.
export function PageHeader({ title, context = [], badges, breadcrumb, actions }: PageHeaderProps) {
  const heading = useRef<HTMLHeadingElement>(null)
  const documentTitle = [title, ...context, 'Polipan'].join(' · ')

  useEffect(() => {
    document.title = documentTitle
  }, [documentTitle])

  useEffect(() => {
    heading.current?.focus({ preventScroll: true })
  }, [])

  return (
    <header className="mb-6 space-y-2">
      {breadcrumb && <div className="text-muted">{breadcrumb}</div>}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-3">
          <h1 ref={heading} tabIndex={-1} className="break-words text-xl font-bold outline-none">
            <span aria-hidden="true" className="text-primary">
              ${' '}
            </span>
            {title}
          </h1>
          {badges}
        </div>
        {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
      </div>
    </header>
  )
}
