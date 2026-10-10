// SPDX-License-Identifier: Apache-2.0

import { t } from '@/i18n'

interface StepperProps {
  steps: string[]
  current: number
  label: string
}

// The steps of a wizard as an ordered list. The current one has aria-current="step".
// On a phone only "Step 2 of 4" is shown; from tablet the whole list.
export function Stepper({ steps, current, label }: StepperProps) {
  return (
    <nav aria-label={label}>
      <p className="text-sm text-muted sm:hidden">
        {t('wizard.step_of', { n: current + 1, total: steps.length })}
      </p>
      <ol className="hidden gap-2 sm:flex lg:flex-col">
        {steps.map((step, i) => {
          const state = i < current ? 'done' : i === current ? 'current' : 'todo'
          return (
            <li
              key={step}
              aria-current={i === current ? 'step' : undefined}
              className={`flex items-center gap-2 rounded-sm px-2 py-1 text-sm ${
                state === 'current' ? 'bg-surface font-semibold' : 'text-muted'
              }`}
            >
              <span
                aria-hidden="true"
                className={`inline-flex size-6 shrink-0 items-center justify-center rounded-full border text-xs ${
                  state === 'todo'
                    ? 'border-border-strong'
                    : 'border-primary bg-primary text-on-primary'
                }`}
              >
                {i + 1}
              </span>
              <span>
                {step}
                {state === 'done' && <span className="sr-only"> ({t('wizard.done')})</span>}
              </span>
            </li>
          )
        })}
      </ol>
    </nav>
  )
}
