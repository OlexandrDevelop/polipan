// SPDX-License-Identifier: Apache-2.0

import en from '@/locales/en/translation.json'

// Every visible string is a key of the English file. English is the only language in v1.
export type MessageKey = keyof typeof en

const messages: Record<string, string> = en

export function hasKey(key: string): key is MessageKey {
  return key in messages
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

const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })
const timeOnly = new Intl.DateTimeFormat(undefined, { timeStyle: 'medium' })
const relative = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })

export function formatDateTime(iso: string): string {
  return dateTime.format(new Date(iso))
}

export function formatTime(iso: string): string {
  return timeOnly.format(new Date(iso))
}

export function formatRelative(iso: string, now = Date.now()): string {
  const seconds = Math.round((new Date(iso).getTime() - now) / 1000)
  const abs = Math.abs(seconds)
  if (abs < 60) return relative.format(seconds, 'second')
  if (abs < 3600) return relative.format(Math.round(seconds / 60), 'minute')
  if (abs < 86400) return relative.format(Math.round(seconds / 3600), 'hour')
  return relative.format(Math.round(seconds / 86400), 'day')
}
