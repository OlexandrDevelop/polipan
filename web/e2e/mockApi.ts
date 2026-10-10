// SPDX-License-Identifier: Apache-2.0

import type { Page, Route } from '@playwright/test'
import type { components } from '../../api/gen/schema'

type Schemas = components['schemas']

// A small in-browser mock of the API, typed by the schema generated from openapi.yaml.
// Each test gets its own copy of the data.
export interface MockState {
  role: Schemas['Role']
  services: Schemas['Service'][]
  variables: Schemas['Variable'][]
  // The secret has a "value" field on purpose: a broken server could send one, and the UI must
  // still never show it (AC-ENV-02).
  secrets: (Schemas['Secret'] & { value?: string })[]
  releases: Schemas['Release'][]
  deployments: Record<string, Schemas['Deployment']>
  // The body of each POST /deploy and POST /rollback, to check what the UI sent.
  deployCalls: Schemas['DeployRequest'][]
  rollbackCalls: { release_id?: string }[]
  // True: the event stream answers 503 (the server is paused).
  eventsDown: boolean
  // UI-29: the sync state, the answer of the plan call and the bodies of the apply calls.
  configState: NonNullable<Schemas['Project']['config_sync_state']>
  configPlan: Schemas['ConfigPlan']
  // A 422 answer of the plan call (a file with errors). It wins over configPlan.
  configPlanError?: { code: string; message: string; details: Record<string, unknown> }
  planBodies: string[]
  applyCalls: { config?: string; plan_hash?: string; prune?: boolean; prune_data?: boolean }[]
  // The code of a 409 answer of the apply call.
  applyConflict?: string
  // M8: alerts, live log lines, canary controls and backup policies.
  alertInstances: Schemas['AlertInstance'][]
  alertRules: Schemas['AlertRule'][]
  alertChannels: Schemas['AlertChannel'][]
  muteCalls: { rule_id: string; until?: string; reason?: string }[]
  logLines: Schemas['LogLine'][]
  canaryCalls: string[]
  backupPolicies: Schemas['BackupPolicy'][]
  // true: the metrics store is down (503 monitoring_unavailable).
  metricsDown: boolean
}

export const LEAKED_SECRET = 'sk_live_must_never_show'

export function defaultState(role: Schemas['Role'] = 'owner'): MockState {
  return {
    role,
    services: [
      {
        id: 's1',
        environment_id: 'e1',
        name: 'api',
        type: 'app',
        source_type: 'image',
        status: 'running',
        desired_state: 'running',
        config: { image: 'nginx:1.27', port: 80 },
        live_release_id: 'r2',
        placements: [],
        pending_changes: false,
      },
    ],
    releases: [
      {
        id: 'r2',
        service_id: 's1',
        number: 2,
        status: 'live',
        image_ref: 'nginx:1.27',
        created_at: '2026-10-02T10:00:00Z',
      },
      {
        id: 'r1',
        service_id: 's1',
        number: 1,
        status: 'previous',
        fast_rollback_possible: true,
        image_ref: 'nginx:1.26',
        created_at: '2026-10-01T10:00:00Z',
      },
    ],
    deployments: {},
    alertInstances: [],
    alertRules: [],
    alertChannels: [],
    muteCalls: [],
    logLines: [],
    canaryCalls: [],
    backupPolicies: [],
    metricsDown: false,
    deployCalls: [],
    rollbackCalls: [],
    eventsDown: false,
    configState: 'in_sync',
    configPlan: { plan_hash: 'h1', valid: true, changes: [] },
    planBodies: [],
    applyCalls: [],
    variables: [
      { id: 'v1', name: 'LOG_LEVEL', value: 'info', environment_id: 'e1', service_id: 's1' },
    ],
    secrets: [
      {
        id: 'sec1',
        name: 'STRIPE_KEY',
        environment_id: 'e1',
        service_id: 's1',
        current_version: 3,
        updated_at: '2026-10-01T10:00:00Z',
        updated_by: 'owner@example.com',
        value: LEAKED_SECRET,
      },
    ],
  }
}

const project: Schemas['Project'] = { id: 'p1', name: 'Shop', slug: 'shop' }
const environment: Schemas['Environment'] = {
  id: 'e1',
  project_id: 'p1',
  name: 'Staging',
  slug: 'staging',
  kind: 'staging',
  protected: false,
}

function json(route: Route, body: unknown, status = 200) {
  return route.fulfill({
    status,
    contentType: 'application/json',
    headers: { 'X-CSRF-Token': 'test-csrf' },
    body: JSON.stringify(body),
  })
}

// A finished SSE answer. The browser opens the stream again after 2 s.
function stream(route: Route, events: string) {
  return route.fulfill({
    status: 200,
    contentType: 'text/event-stream',
    headers: { 'Cache-Control': 'no-cache' },
    body: `retry: 2000\n\n${events}`,
  })
}

function newDeployment(
  id: string,
  releaseId: string,
  trigger: Schemas['Deployment']['trigger'],
): Schemas['Deployment'] {
  const time = new Date().toISOString()
  return {
    id,
    service_id: 's1',
    release_id: releaseId,
    previous_release_id: 'r2',
    trigger,
    strategy: 'recreate',
    state: 'building',
    steps: [
      { state: 'queued', time },
      { state: 'building', time },
    ],
    started_at: time,
  }
}

export async function mockApi(page: Page, state: MockState) {
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.replace(/^\/api\/v1/, '')
    const method = request.method()
    const service = state.services[0]

    if (method === 'GET' && path === '/me') {
      return json(route, {
        user: { id: 'u1', email: 'owner@example.com', name: 'Owner' },
        org_role: state.role,
      })
    }
    if (method === 'GET' && path === '/org')
      return json(route, { id: 'o1', name: 'Acme', slug: 'acme' })
    if (method === 'GET' && path === '/projects') return json(route, { items: [project] })
    if (method === 'GET' && path === '/projects/p1') {
      return json(route, {
        ...project,
        config_source: { repo: 'acme/shop', branch: 'main', path: 'polipan.yaml' },
        config_sync_state: state.configState,
      })
    }
    if (method === 'GET' && path === '/projects/p1/config') {
      return route.fulfill({
        status: 200,
        contentType: 'application/yaml',
        body: 'version: 1\nservices: {}\n',
      })
    }
    if (method === 'POST' && path === '/projects/p1/config/plan') {
      state.planBodies.push(request.postData() ?? '')
      if (state.configPlanError) return json(route, state.configPlanError, 422)
      return json(route, state.configPlan)
    }
    if (method === 'POST' && path === '/projects/p1/config/apply') {
      state.applyCalls.push(request.postDataJSON() as MockState['applyCalls'][number])
      if (state.applyConflict) {
        return json(
          route,
          { code: state.applyConflict, message: 'conflict', request_id: 'mock' },
          409,
        )
      }
      return json(route, { deployments: [] }, 202)
    }
    if (method === 'GET' && path === '/projects/p1/environments') return json(route, [environment])
    if (method === 'GET' && path === '/environments/e1/services') return json(route, state.services)
    if (method === 'GET' && path === '/services/s1') return json(route, service)
    if (method === 'GET' && path === '/events') {
      if (state.eventsDown) return json(route, { code: 'unavailable', message: 'paused' }, 503)
      return stream(route, '')
    }
    if (method === 'GET' && path === '/services/s1/deployments') {
      return json(route, { items: Object.values(state.deployments) })
    }
    if (method === 'GET' && path === '/services/s1/releases') {
      return json(route, { items: state.releases })
    }
    if (method === 'GET' && path.startsWith('/releases/') && !path.includes('/diff/')) {
      return json(
        route,
        state.releases.find((r) => r.id === path.split('/')[2]),
      )
    }
    if (method === 'GET' && path === '/releases/r2/diff/r1') {
      return json(route, {
        changes: [
          { path: 'image_ref', before: 'nginx:1.27', after: 'nginx:1.26' },
          { path: 'secret_versions.STRIPE_KEY', before: 3, after: 2 },
        ],
      })
    }
    if (method === 'POST' && path === '/services/s1/deploy') {
      const body = request.postDataJSON() as Schemas['DeployRequest']
      state.deployCalls.push(body)
      if (body.dry_run) {
        return json(route, {
          strategy: 'recreate',
          forced: true,
          expected_downtime: 'short',
          nodes: [],
          warnings: [
            { code: 'zero_downtime_not_available', message: 'forced' },
            { code: 'migration_compat', message: 'migration' },
          ],
        })
      }
      state.deployments.d1 = newDeployment('d1', 'r3', 'manual')
      return json(route, state.deployments.d1, 202)
    }
    if (method === 'POST' && path === '/services/s1/rollback') {
      const body = request.postDataJSON() as { release_id?: string }
      state.rollbackCalls.push(body)
      state.deployments.d2 = newDeployment('d2', body.release_id ?? '', 'rollback')
      return json(route, state.deployments.d2, 202)
    }
    if (method === 'GET' && path === '/services/s1/logs/stream') {
      const events = state.logLines
        .map((l) => `event: log\ndata: ${JSON.stringify(l)}\n\n`)
        .join('')
      return stream(route, events)
    }
    if (method === 'GET' && path === '/services/s1/metrics') {
      if (state.metricsDown) {
        return json(
          route,
          { code: 'monitoring_unavailable', message: 'down', request_id: 'mock' },
          503,
        )
      }
      const to = Math.floor(Date.now() / 1000)
      const points = [0, 1, 2, 3].map((i) => [to - 900 + i * 300, 10 + i * 20])
      return json(route, {
        step_seconds: 300,
        series: url.searchParams
          .getAll('metrics')
          .map((metric) => ({ metric, labels: {}, points })),
      })
    }
    if (method === 'GET' && path === '/alert-instances') {
      return json(route, { items: state.alertInstances, next_cursor: null })
    }
    if (method === 'GET' && path === '/alert-rules') return json(route, state.alertRules)
    if (method === 'GET' && path === '/alert-channels') return json(route, state.alertChannels)
    const mute = path.match(/^\/alert-rules\/(\w+)\/mute$/)
    if (method === 'POST' && mute) {
      state.muteCalls.push({ rule_id: mute[1], ...(request.postDataJSON() as object) })
      return json(route, state.alertRules.find((r) => r.id === mute[1]) ?? {})
    }
    if (method === 'GET' && path === '/backup-policies') return json(route, state.backupPolicies)
    const canary = path.match(/^\/deployments\/(\w+)\/(promote|pause|resume|abort)$/)
    if (method === 'POST' && canary) {
      state.canaryCalls.push(canary[2])
      const deployment = state.deployments[canary[1]]
      if (canary[2] === 'pause') deployment.canary_paused = true
      if (canary[2] === 'resume') deployment.canary_paused = false
      return json(route, deployment)
    }
    const deploymentPath = path.match(/^\/deployments\/(\w+)(\/logs|\/events)?$/)
    if (method === 'GET' && deploymentPath) {
      const deployment = state.deployments[deploymentPath[1]]
      if (deploymentPath[2] === '/logs') return json(route, [])
      if (deploymentPath[2] === '/events' && deployment.strategy === 'canary') {
        // A canary stays in shifting. The stream brings one gate result.
        const gate = {
          step: 10,
          passed: true,
          health_ok: true,
          green_requests: 120,
          green_5xx_rate: 0.004,
          green_p95_seconds: 0.21,
          blue_p95_seconds: 0.18,
        }
        return stream(
          route,
          `event: canary_gate\ndata: ${JSON.stringify({ type: 'canary_gate', data: gate, time: new Date().toISOString() })}\n\n`,
        )
      }
      if (deploymentPath[2] === '/events') {
        // The first event brings the deployment to live, like the engine would.
        const time = new Date().toISOString()
        deployment.state = 'live'
        deployment.finished_at = time
        deployment.steps = [
          ...(deployment.steps ?? []).filter((x) => x.state !== 'live'),
          { state: 'live', time },
        ]
        service.live_release_id = deployment.release_id
        return stream(
          route,
          `event: state\ndata: ${JSON.stringify({ type: 'state', state: 'live', time })}\n\n`,
        )
      }
      return json(route, deployment)
    }
    if (method === 'GET' && path === '/services/s1/domains') return json(route, [])
    if (method === 'GET' && path === '/nodes') return json(route, [])
    if (method === 'GET' && path === '/git-connections') return json(route, [])
    if (method === 'GET' && path === '/projects/p1/variables') return json(route, state.variables)
    if (method === 'GET' && path === '/projects/p1/secrets') return json(route, state.secrets)
    if (method === 'PATCH' && path.startsWith('/variables/')) {
      const id = path.split('/')[2]
      const body = request.postDataJSON() as Schemas['VariableWrite']
      state.variables = state.variables.map((v) => (v.id === id ? { ...v, ...body } : v))
      // Like the server: a changed variable marks the service, nothing restarts (REQ-ENV-003).
      service.pending_changes = true
      return json(
        route,
        state.variables.find((v) => v.id === id),
      )
    }
    if (method === 'PUT' && path.startsWith('/secrets/')) {
      const secret = state.secrets[0]
      secret.current_version = (secret.current_version ?? 0) + 1
      service.pending_changes = true
      return json(route, secret)
    }
    return json(route, { code: 'not_found', message: 'not in the mock', request_id: 'mock' }, 404)
  })
}
