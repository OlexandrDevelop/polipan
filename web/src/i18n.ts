// SPDX-License-Identifier: Apache-2.0

import en from '@/locales/en/translation.json'

// Every visible string is a key of the English file. English is always loaded. The other
// languages are separate files that load on demand, and a missing key falls back to English.
export type MessageKey = keyof typeof en

export const LANGUAGES = [
  { code: 'en', name: 'English' },
  { code: 'uk', name: 'Українська' },
  { code: 'it', name: 'Italiano' },
  { code: 'pl', name: 'Polski' },
  { code: 'de', name: 'Deutsch' },
  { code: 'fr', name: 'Français' },
  { code: 'es', name: 'Español' },
  { code: 'nl', name: 'Nederlands' },
  { code: 'nb', name: 'Norsk' },
  { code: 'cs', name: 'Čeština' },
  { code: 'el', name: 'Ελληνικά' },
  { code: 'tr', name: 'Türkçe' },
] as const

export type LanguageCode = (typeof LANGUAGES)[number]['code']

const STORAGE_KEY = 'polipan.language'
const loaders = import.meta.glob<{ default: Record<string, string> }>(
  './locales/*/translation.json',
)

let messages: Record<string, string> = en
let language: LanguageCode = 'en'

function isLanguage(code: string | null | undefined): code is LanguageCode {
  return LANGUAGES.some((l) => l.code === code)
}

export function currentLanguage(): LanguageCode {
  return language
}

// The language for Intl formatting. Norwegian Bokmål is "nb".
export function currentLocale(): string {
  return language
}

// The saved choice, else the first browser language we have, else English.
function pickLanguage(): LanguageCode {
  const saved = localStorage.getItem(STORAGE_KEY)
  if (isLanguage(saved)) return saved
  for (const tag of navigator.languages ?? [navigator.language]) {
    const base = tag.toLowerCase().split('-')[0]
    const code = base === 'no' || base === 'nn' ? 'nb' : base
    if (isLanguage(code)) return code
  }
  return 'en'
}

async function load(code: LanguageCode): Promise<void> {
  if (code === 'en') {
    messages = en
  } else {
    const loader = loaders[`./locales/${code}/translation.json`]
    messages = loader ? { ...en, ...(await loader()).default } : en
  }
  language = code
  document.documentElement.lang = code
}

// Call once before the first render.
export async function initLanguage(): Promise<void> {
  await load(pickLanguage())
}

// Switches the language and remembers it. The caller re-renders the tree.
export async function setLanguage(code: LanguageCode): Promise<void> {
  localStorage.setItem(STORAGE_KEY, code)
  await load(code)
}

export function hasKey(key: string): key is MessageKey {
  return key in en
}

// Replaces {name} in the text with the value of params.name.
export function t(key: MessageKey, params?: Record<string, string | number>): string {
  const text = messages[key]
  if (!params) return text
  return text.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? ''))
}

// Looks up "prefix.value". Falls back to the raw value for states the UI does not know yet.
export function tState(prefix: string, value: string | undefined): string {
  if (!value) return '-'
  const key = `${prefix}.${value}`
  return hasKey(key) ? t(key) : value
}

const dateTime = () =>
  new Intl.DateTimeFormat(currentLocale(), { dateStyle: 'medium', timeStyle: 'short' })
const timeOnly = () => new Intl.DateTimeFormat(currentLocale(), { timeStyle: 'medium' })
const relative = () => new Intl.RelativeTimeFormat(currentLocale(), { numeric: 'auto' })

export function formatDateTime(iso: string): string {
  return dateTime().format(new Date(iso))
}

export function formatTime(iso: string): string {
  return timeOnly().format(new Date(iso))
}

export function formatRelative(iso: string, now = Date.now()): string {
  const seconds = Math.round((new Date(iso).getTime() - now) / 1000)
  const abs = Math.abs(seconds)
  if (abs < 60) return relative().format(seconds, 'second')
  if (abs < 3600) return relative().format(Math.round(seconds / 60), 'minute')
  if (abs < 86400) return relative().format(Math.round(seconds / 3600), 'hour')
  return relative().format(Math.round(seconds / 86400), 'day')
}
