// SPDX-License-Identifier: Apache-2.0

import type { Page } from '@playwright/test'
import { mockApi } from './mockApi'
import type { MockState } from './mockApi'

// The four widths of the quality plan (ui/quality.md, section 4) and the two themes.
export const WIDTHS = [360, 768, 1280, 1920] as const
export const THEMES = ['light', 'dark'] as const
export type Theme = (typeof THEMES)[number]

const SERVICE = '/o/acme/p/shop/e/staging/s/api'

// Every main screen. "visual" marks the small set that gets a screenshot.
export interface Screen {
  name: string
  path: string
  // The heading the page must show before it is checked.
  heading?: RegExp
  visual?: boolean
}

export const SCREENS: Screen[] = [
  { name: 'dashboard', path: '/o/acme', visual: true },
  { name: 'projects', path: '/o/acme/projects' },
  { name: 'project', path: '/o/acme/p/shop/e/staging', visual: true },
  { name: 'wizard', path: '/o/acme/p/shop/e/staging/new?source=image' },
  { name: 'wizard-chooser', path: '/o/acme/p/shop/e/staging/new' },
  { name: 'service', path: SERVICE, visual: true },
  { name: 'service-deployments', path: `${SERVICE}/deployments` },
  { name: 'service-releases', path: `${SERVICE}/releases`, visual: true },
  { name: 'service-env', path: `${SERVICE}/env` },
  { name: 'service-domains', path: `${SERVICE}/domains` },
  { name: 'service-logs', path: `${SERVICE}/logs` },
  { name: 'service-metrics', path: `${SERVICE}/metrics` },
  { name: 'service-settings', path: `${SERVICE}/settings` },
  { name: 'deployment', path: `${SERVICE}/deployments/d1` },
  { name: 'deployments', path: '/o/acme/deployments' },
  { name: 'nodes', path: '/o/acme/nodes', visual: true },
  { name: 'node', path: '/o/acme/nodes/n1' },
  { name: 'catalog', path: '/o/acme/catalog' },
  { name: 'backups', path: '/o/acme/backups' },
  { name: 'backup-targets', path: '/o/acme/settings/backup-targets' },
  { name: 'alerts', path: '/o/acme/alerts' },
  { name: 'alert-settings', path: '/o/acme/settings/alerts' },
  { name: 'logs', path: '/o/acme/logs?q=error', visual: true },
  { name: 'previews', path: '/o/acme/previews' },
  { name: 'members', path: '/o/acme/settings/members' },
  { name: 'teams', path: '/o/acme/settings/teams' },
  { name: 'tokens', path: '/o/acme/settings/tokens' },
  { name: 'audit', path: '/o/acme/settings/audit' },
  { name: 'sso', path: '/o/acme/settings/sso' },
  { name: 'git', path: '/o/acme/settings/git' },
  { name: 'organization', path: '/o/acme/settings/organization' },
  { name: 'profile', path: '/o/acme/settings/profile' },
  { name: 'config', path: '/o/acme/p/shop/config' },
  { name: 'roles', path: '/o/acme/p/shop/roles' },
  { name: 'not-found', path: '/o/acme/nothing-here' },
]

const NODES = [
  {
    id: 'n1',
    name: 'node-1',
    state: 'ready',
    public_ip: '203.0.113.10',
    mesh_ip: '10.88.0.1',
    os: 'linux',
    arch: 'amd64',
    docker_version: '27.3.1',
    agent_version: '1.0.0',
    capacity: { cpu_millis: 4000, memory_bytes: 8_000_000_000, disk_bytes: 100_000_000_000 },
    usage: { cpu_millis: 800, memory_bytes: 2_000_000_000, disk_bytes: 30_000_000_000 },
    last_heartbeat_at: '2026-10-10T10:00:00Z',
    workload_count: 1,
    workloads: [],
    labels: {},
  },
  {
    id: 'n2',
    name: 'node-2',
    state: 'draining',
    public_ip: '203.0.113.11',
    mesh_ip: '10.88.0.2',
    os: 'linux',
    arch: 'arm64',
    docker_version: '27.3.1',
    agent_version: '1.0.0',
    capacity: { cpu_millis: 2000, memory_bytes: 4_000_000_000, disk_bytes: 50_000_000_000 },
    usage: { cpu_millis: 100, memory_bytes: 500_000_000, disk_bytes: 5_000_000_000 },
    last_heartbeat_at: '2026-10-10T10:00:00Z',
    workload_count: 0,
    workloads: [],
    labels: {},
  },
]

// Extra answers for the screens that M2 to M9 tests did not need. They are registered after
// mockApi, so they win. Everything else falls back to mockApi.
export async function mockMore(page: Page) {
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace(/^\/api\/v1/, '')
    const method = request.method()
    const json = (body: unknown, status = 200) =>
      route.fulfill({
        status,
        contentType: 'application/json',
        headers: { 'X-CSRF-Token': 'test-csrf' },
        body: JSON.stringify(body),
      })
    if (method !== 'GET') return route.fallback()
    if (path === '/org') {
      return json({
        id: 'o1',
        name: 'Acme',
        slug: 'acme',
        settings: { metrics_retention_days: 90, logs_retention_days: 30 },
      })
    }
    if (path === '/version') return json({ version: '1.0.0', commit: 'abc1234', api: 'v1' })
    if (path === '/nodes') return json(NODES)
    if (path === '/nodes/n1') return json(NODES[0])
    if (path === '/nodes/n1/orphan-volumes') return json([])
    if (path === '/nodes/n1/metrics') return json({ step_seconds: 300, series: [] })
    if (path === '/git-connections') {
      return json([
        {
          id: 'g1',
          provider: 'github',
          name: 'acme-org',
          webhook_url: 'https://panel.example.com/api/v1/webhooks/git/w1',
        },
      ])
    }
    if (path === '/tokens') {
      return json([
        {
          id: 't1',
          name: 'ci',
          token_prefix: 'plp_ab12',
          scopes: ['deploy'],
          last_used_at: '2026-10-09T10:00:00Z',
        },
      ])
    }
    if (path === '/members') {
      return json({
        next_cursor: null,
        items: [
          {
            user_id: 'u1',
            email: 'owner@example.com',
            name: 'Owner',
            role: 'owner',
            source: 'manual',
            status: 'active',
            last_login_at: '2026-10-10T09:00:00Z',
          },
          {
            user_id: 'u2',
            email: 'dev@example.com',
            name: 'Dev',
            role: 'developer',
            source: 'sso',
            status: 'active',
          },
        ],
      })
    }
    if (path === '/teams') return json([{ id: 'tm1', name: 'Platform', member_ids: ['u1', 'u2'] }])
    if (path === '/projects/p1/roles') return json([])
    if (path === '/audit-events') {
      return json({
        items: [
          {
            seq: 2,
            id: 'a2',
            actor_type: 'user',
            actor_id: 'owner@example.com',
            action: 'service.deploy',
            target_type: 'service',
            target_id: 's1',
            created_at: '2026-10-10T09:30:00Z',
            diff: { image: { before: 'nginx:1.26', after: 'nginx:1.27' } },
          },
          {
            seq: 1,
            id: 'a1',
            actor_type: 'user',
            actor_id: 'owner@example.com',
            action: 'org.update',
            target_type: 'org',
            target_id: 'o1',
            created_at: '2026-10-10T09:00:00Z',
          },
        ],
        next_cursor: null,
      })
    }
    if (path === '/audit/verify') return json({ ok: true, checked_rows: 2 })
    if (path === '/sso-providers') return json([])
    if (path === '/auth/sso/providers') return json([])
    if (path === '/me/sessions') {
      return json([
        {
          id: 'ss1',
          ip: '203.0.113.5',
          user_agent: 'Firefox',
          created_at: '2026-10-10T08:00:00Z',
          current: true,
        },
      ])
    }
    if (path === '/templates') {
      return json([
        {
          slug: 'postgres',
          version: '16',
          name: 'PostgreSQL',
          description: 'A relational database.',
          builtin: true,
          inputs: [],
          services: [],
        },
        {
          slug: 'redis',
          version: '7',
          name: 'Redis',
          description: 'An in-memory store.',
          builtin: true,
          inputs: [],
          services: [],
        },
      ])
    }
    if (path === '/backup-targets') {
      return json([{ id: 'bt1', name: 'offsite', kind: 's3', config: { bucket: 'backups' } }])
    }
    if (path === '/restore-checks') return json({ items: [], next_cursor: null })
    if (path === '/services/s1/volumes') return json([])
    if (path === '/services/s1/backup-policies') return json([])
    if (path === '/logs/search') {
      return json({
        items: [
          {
            time: '2026-10-10T09:30:00Z',
            message: 'error: connection refused',
            level: 'error',
            stream: 'stderr',
            service_id: 's1',
          },
        ],
        next_cursor: null,
      })
    }
    return route.fallback()
  })
}

// Signs the mock user in and sets the theme the way the real page reads it.
export async function openScreen(
  page: Page,
  screen: Screen,
  options: { width: number; theme: Theme },
) {
  await page.setViewportSize({ width: options.width, height: 900 })
  await page.emulateMedia({ colorScheme: options.theme })
  await page.addInitScript(() => {
    try {
      localStorage.setItem('polipan.theme', 'system')
    } catch {
      // Storage may be blocked.
    }
  })
  await page.goto(screen.path)
  await page.locator('h1').first().waitFor()
  await page.waitForLoadState('networkidle')
}

export async function setUp(page: Page, state: MockState) {
  await mockApi(page, state)
  await mockMore(page)
}
