// SPDX-License-Identifier: Apache-2.0

import type { components } from '@api/schema'
import { t } from '@/i18n'

export interface ComposeProblem {
  service: string
  text: string
}

const PREFIX = 'template is not safe: '

// The server says "template is not safe: a; b; c". Each part names a service and a volume. This turns the
// parts into short lines that say what to do, grouped by service. Returns undefined for any other error.
export function composeProblems(message: string | undefined): ComposeProblem[] | undefined {
  if (!message || !message.includes(PREFIX)) return undefined
  const parts = message
    .slice(message.indexOf(PREFIX) + PREFIX.length)
    .split('; ')
    .map((p) => p.trim())
    .filter(Boolean)
  const found = new Map<string, ComposeProblem>()
  for (const part of parts) {
    const m = /^services\.([^.]+)\.volumes\[(\d+)\]: (.*)$/.exec(part)
    if (!m) {
      found.set(part, { service: '', text: part })
      continue
    }
    const [, service, index, what] = m
    const key = `${service}#${index}`
    const bind = /^bind mount (\S+) is not allowed/.exec(what)
    if (what.includes('Docker socket')) {
      // The socket is the stronger reason, so it replaces the "bind mount" line of the same volume.
      found.set(key, { service, text: t('compose.problem.socket') })
    } else if (bind && !found.has(key)) {
      found.set(key, { service, text: t('compose.problem.bind', { path: bind[1] }) })
    } else if (!bind) {
      found.set(key, { service, text: what })
    }
  }
  return [...found.values()]
}

export type ComposeLoss = components['schemas']['ComposeLoss']

export interface ComposeLossGroup {
  service: string
  losses: ComposeLoss[]
}

// The losses of an import grouped by service, in the order of the answer. A loss of the whole file has the
// service ''.
export function groupLosses(losses: ComposeLoss[] | undefined): ComposeLossGroup[] {
  const groups = new Map<string, ComposeLoss[]>()
  for (const loss of losses ?? []) {
    const service = /^services\.([^.[]+)/.exec(loss.path)?.[1] ?? ''
    groups.set(service, [...(groups.get(service) ?? []), loss])
  }
  return [...groups].map(([service, list]) => ({ service, losses: list }))
}

// The label of a disposition. An unknown one is shown as it is.
export function lossLabel(disposition: string): string {
  switch (disposition) {
    case 'imported':
      return t('compose.loss.imported')
    case 'replaced':
      return t('compose.loss.replaced')
    case 'ignored':
      return t('compose.loss.ignored')
    case 'dropped':
      return t('compose.loss.dropped')
    case 'manual':
      return t('compose.loss.manual')
    default:
      return disposition
  }
}
