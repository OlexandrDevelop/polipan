// SPDX-License-Identifier: Apache-2.0

// Small helpers to read and write the free-form service config in forms.
export type Config = Record<string, unknown>

export function str(v: unknown, fallback = ''): string {
  return typeof v === 'string' || typeof v === 'number' ? String(v) : fallback
}

export function obj(v: unknown): Config {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Config) : {}
}

// A whole number of at least `min`, or the text is wrong.
export function wholeNumber(text: string, min = 1): boolean {
  const n = Number(text)
  return Number.isInteger(n) && n >= min
}
