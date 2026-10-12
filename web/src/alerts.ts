// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@tanstack/react-query'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import type { Tone } from '@/status'

export type AlertInstance = components['schemas']['AlertInstance']
export type Incident = components['schemas']['Incident']
export type AlertRule = components['schemas']['AlertRule']
export type AlertChannel = components['schemas']['AlertChannel']

// The rule types of REQ-ALERT-002.
export const RULE_TYPES = [
  'node_suspect',
  'node_down',
  'service_unhealthy',
  'deployment_failed',
  'deployment_rolled_back',
  'deployment_live',
  'cpu_high',
  'memory_high',
  'disk_high',
  'cert_expiring',
  'cert_failed',
  'backup_failed',
  'restore_check_failed',
  'restore_check_overdue',
  'backup_missing',
  'audit_chain_broken',
  'failover_started',
  'container_restart_loop',
] as const

// Rules with a threshold and a time (REQ-ALERT-003).
export const THRESHOLD_TYPES = new Set(['cpu_high', 'memory_high', 'disk_high'])

export const SCOPES = ['org', 'project', 'environment', 'service', 'node'] as const
export type ScopeKind = (typeof SCOPES)[number]

export function severityTone(severity: string | undefined): Tone {
  if (severity === 'critical') return 'danger'
  if (severity === 'warning') return 'warning'
  return 'info'
}

export function scopeOf(rule: AlertRule): { kind: ScopeKind; id: string } {
  const s = rule.scope ?? {}
  if (s.service_id) return { kind: 'service', id: s.service_id }
  if (s.node_id) return { kind: 'node', id: s.node_id }
  if (s.environment_id) return { kind: 'environment', id: s.environment_id }
  if (s.project_id) return { kind: 'project', id: s.project_id }
  return { kind: 'org', id: '' }
}

// Alerts are refreshed by the event stream (live.ts) and by this interval as a backup.
export function useAlertInstances() {
  return useQuery({
    queryKey: ['alert-instances'],
    queryFn: async () =>
      (await unwrap(api.GET('/alert-instances', { params: { query: { limit: 100 } } }))).items ??
      [],
    refetchInterval: 15_000,
  })
}

// One incident of a node with its alerts, the probable cause first.
export function useIncident(id: string) {
  return useQuery({
    queryKey: ['incident', id],
    queryFn: () =>
      unwrap(api.GET('/incidents/{incident_id}', { params: { path: { incident_id: id } } })),
    refetchInterval: 15_000,
  })
}

// The firing alerts in groups: the alerts of one incident together, the others alone, in the order of
// the list.
export function groupByIncident(list: AlertInstance[]): AlertInstance[][] {
  const groups: AlertInstance[][] = []
  const byIncident = new Map<string, AlertInstance[]>()
  for (const a of list) {
    const id = a.related_count ? a.incident_id : undefined
    const group = id ? byIncident.get(id) : undefined
    if (group) {
      group.push(a)
      continue
    }
    const fresh = [a]
    groups.push(fresh)
    if (id) byIncident.set(id, fresh)
  }
  return groups
}

export function useAlertRules() {
  return useQuery({
    queryKey: ['alert-rules'],
    queryFn: () => unwrap(api.GET('/alert-rules')),
  })
}

export function useAlertChannels() {
  return useQuery({
    queryKey: ['alert-channels'],
    queryFn: () => unwrap(api.GET('/alert-channels')),
  })
}

// Enabled rules that would send to nobody, and channels that fail (REQ-ALERT-005, REQ-ALERT-008).
export function rulesWithoutChannel(rules: AlertRule[] | undefined): AlertRule[] {
  return (rules ?? []).filter((r) => r.enabled !== false && (r.channel_ids ?? []).length === 0)
}

export function failingChannels(channels: AlertChannel[] | undefined): AlertChannel[] {
  return (channels ?? []).filter((c) => c.state === 'failing')
}
