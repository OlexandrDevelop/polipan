// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { accessFor, effectiveRole } from '../src/permissions'

// Pure logic, no browser.
test('REQ-RBAC the project override replaces the org role of a developer or a viewer', () => {
  const me = {
    org_role: 'developer' as const,
    project_roles: [
      { project_id: 'p1', role: 'viewer' as const },
      { project_id: 'p2', role: 'admin' as const },
      { project_id: 'p2', role: 'viewer' as const },
    ],
  }
  expect(effectiveRole(me, 'p1')).toBe('viewer')
  expect(effectiveRole(me, 'p2')).toBe('admin')
  expect(effectiveRole(me, 'p3')).toBe('developer')
  expect(effectiveRole(me)).toBe('developer')
})

test('REQ-RBAC an override never lowers an owner or an admin', () => {
  const roles = [{ project_id: 'p1', role: 'viewer' as const }]
  expect(effectiveRole({ org_role: 'owner', project_roles: roles }, 'p1')).toBe('owner')
  expect(effectiveRole({ org_role: 'admin', project_roles: roles }, 'p1')).toBe('admin')
})

test('REQ-RBAC the M5 permissions follow the matrix', () => {
  expect(accessFor('viewer', 'member:read')).toBe('allow')
  expect(accessFor('viewer', 'member:manage')).toBe('hidden')
  expect(accessFor('developer', 'audit:read')).toBe('hidden')
  expect(accessFor('admin', 'audit:export')).toBe('allow')
  expect(accessFor('developer', 'token:own')).toBe('allow')
  expect(accessFor('developer', 'token:manage')).toBe('hidden')
})
