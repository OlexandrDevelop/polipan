// SPDX-License-Identifier: Apache-2.0

import { createContext, useCallback, useContext, useMemo, useState } from 'react'
import type { ChangeEvent, ReactNode } from 'react'
import { LANGUAGES, currentLanguage, setLanguage as applyLanguage, t } from '@/i18n'
import type { LanguageCode } from '@/i18n'

interface LanguageValue {
  language: LanguageCode
  setLanguage: (code: LanguageCode) => Promise<void>
}

const LanguageContext = createContext<LanguageValue | null>(null)

// The text comes from plain functions, so a new language needs a fresh tree: the key remounts it.
export function LanguageProvider({ children }: { children: ReactNode }) {
  const [language, setState] = useState<LanguageCode>(currentLanguage)

  const setLanguage = useCallback(async (code: LanguageCode) => {
    await applyLanguage(code)
    setState(code)
  }, [])

  const value = useMemo(() => ({ language, setLanguage }), [language, setLanguage])
  return (
    <LanguageContext.Provider value={value}>
      <div key={language} className="contents">
        {children}
      </div>
    </LanguageContext.Provider>
  )
}

export function useLanguage(): LanguageValue {
  const ctx = useContext(LanguageContext)
  if (!ctx) throw new Error('useLanguage must be used inside LanguageProvider')
  return ctx
}

// A list of the languages. Each name is in its own language, so a lost user can find theirs.
export function LanguageSelect({ className = '' }: { className?: string }) {
  const { language, setLanguage } = useLanguage()
  return (
    <select
      aria-label={t('language.label')}
      className={`min-h-6 border border-border-strong bg-raised px-1 coarse:min-h-11 ${className}`}
      value={language}
      onChange={(e: ChangeEvent<HTMLSelectElement>) =>
        void setLanguage(e.target.value as LanguageCode)
      }
    >
      {LANGUAGES.map((l) => (
        <option key={l.code} value={l.code} lang={l.code}>
          {l.name}
        </option>
      ))}
    </select>
  )
}
