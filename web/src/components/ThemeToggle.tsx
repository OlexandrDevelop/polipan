// SPDX-License-Identifier: Apache-2.0

import { Button } from '@/components/ui'
import { t } from '@/i18n'
import type { MessageKey } from '@/i18n'
import { useTheme } from '@/theme'
import type { ThemeChoice } from '@/theme'

export const themeChoices: { value: ThemeChoice; label: MessageKey }[] = [
  { value: 'system', label: 'theme.system' },
  { value: 'light', label: 'theme.light' },
  { value: 'dark', label: 'theme.dark' },
]

function labelOf(choice: ThemeChoice): string {
  return t(themeChoices.find((c) => c.value === choice)?.label ?? 'theme.system')
}

// A button that goes to the next mode: system, light, dark.
export function ThemeCycle() {
  const { choice, setChoice } = useTheme()
  const next = themeChoices[(themeChoices.findIndex((c) => c.value === choice) + 1) % 3].value
  return (
    <Button
      variant="ghost"
      aria-label={t('theme.cycle', { current: labelOf(choice), next: labelOf(next) })}
      onClick={() => setChoice(next)}
    >
      {t('theme.label')}: {labelOf(choice)}
    </Button>
  )
}

// The three modes as a radio group (profile page).
export function ThemeRadios() {
  const { choice, setChoice } = useTheme()
  return (
    <fieldset className="space-y-2">
      <legend className="text-sm font-medium">{t('theme.label')}</legend>
      {themeChoices.map((c) => (
        <label key={c.value} className="flex min-h-8 items-center gap-3 coarse:min-h-11">
          <input
            type="radio"
            name="theme"
            className="size-4 accent-primary coarse:size-6"
            checked={choice === c.value}
            onChange={() => setChoice(c.value)}
          />
          {t(c.label)}
        </label>
      ))}
    </fieldset>
  )
}
