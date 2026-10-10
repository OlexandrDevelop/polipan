// SPDX-License-Identifier: Apache-2.0

export type Tone = 'success' | 'warning' | 'danger' | 'muted' | 'info'

export function serviceTone(status: string | undefined): Tone {
  switch (status) {
    case 'running':
      return 'success'
    case 'deploying':
    case 'degraded':
      return 'warning'
    case 'failed':
      return 'danger'
    default:
      return 'muted'
  }
}

export function deploymentTone(state: string | undefined): Tone {
  switch (state) {
    case 'live':
      return 'success'
    case 'failed':
      return 'danger'
    case 'rolled_back':
    case 'cancelled':
    case 'superseded':
      return 'muted'
    default:
      return 'warning'
  }
}

export function isDeploymentActive(state: string | undefined): boolean {
  return deploymentTone(state) === 'warning'
}

export function nodeTone(state: string | undefined): Tone {
  switch (state) {
    case 'ready':
      return 'success'
    case 'down':
      return 'danger'
    case 'cordoned':
    case 'maintenance':
      return 'muted'
    default:
      return 'warning'
  }
}

export function tlsTone(status: string | undefined): Tone {
  if (status === 'issued') return 'success'
  if (status === 'failed') return 'danger'
  return 'warning'
}
