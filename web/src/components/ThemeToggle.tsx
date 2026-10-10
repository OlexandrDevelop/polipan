// SPDX-License-Identifier: Apache-2.0

import { useTheme } from '@/theme'
import type { ThemeChoice } from '@/theme'

const choices: { value: ThemeChoice; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]

export function ThemeToggle() {
  const { choice, setChoice } = useTheme()
  return (
    <label className="flex items-center gap-2 text-sm text-muted">
      Theme
      <select
        className="min-h-8 rounded-sm border border-border-strong bg-bg px-2 text-text"
        value={choice}
        onChange={(e) => setChoice(e.target.value as ThemeChoice)}
      >
        {choices.map((c) => (
          <option key={c.value} value={c.value}>
            {c.label}
          </option>
        ))}
      </select>
    </label>
  )
}
