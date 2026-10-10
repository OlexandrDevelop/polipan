// SPDX-License-Identifier: Apache-2.0

import type { components } from '@api/schema'
import { useMe } from '@/auth'

export type Role = components['schemas']['Role']

// The permissions of the M1 to M3 screens. Rules follow the matrix in docs/spec/10-security.md.
//   all:   every role
//   dev:   owner, admin, developer
//   admin: owner, admin
//   p:     owner, admin; developer only on a non-protected environment
export type Permission =
  | 'project:create'
  | 'env:create'
  | 'service:create'
  | 'service:update'
  | 'service:delete'
  | 'service:deploy'
  | 'service:rollback'
  | 'deployment:read'
  | 'deployment:cancel'
  | 'service:restart'
  | 'service:exec'
  | 'domain:manage'
  | 'node:manage'
  | 'variable:write'
  | 'secret:list'
  | 'secret:write'
  | 'secret:destroy'
  | 'volume:manage'
  | 'git:manage'
  | 'backup:manage'
  | 'backup:restore'
  | 'template:manage'
  | 'org:update'
  | 'member:read'
  | 'member:manage'
  | 'team:manage'
  | 'token:own'
  | 'token:manage'
  | 'audit:read'
  | 'audit:export'

const rules: Record<Permission, 'all' | 'dev' | 'admin' | 'p'> = {
  'project:create': 'dev',
  'env:create': 'dev',
  'service:create': 'p',
  'service:update': 'p',
  'service:delete': 'p',
  'service:deploy': 'p',
  'service:rollback': 'p',
  'deployment:read': 'all',
  'deployment:cancel': 'p',
  'service:restart': 'dev',
  'service:exec': 'p',
  'domain:manage': 'p',
  'node:manage': 'admin',
  'variable:write': 'p',
  'secret:list': 'dev',
  'secret:write': 'p',
  'secret:destroy': 'admin',
  'volume:manage': 'p',
  'git:manage': 'admin',
  'backup:manage': 'p',
  'backup:restore': 'admin',
  'template:manage': 'dev',
  'org:update': 'admin',
  'member:read': 'all',
  'member:manage': 'admin',
  'team:manage': 'admin',
  'token:own': 'all',
  'token:manage': 'admin',
  'audit:read': 'admin',
  'audit:export': 'admin',
}

// hidden: the role can never do it. blocked: shown but disabled. allow: enabled.
export type Access = 'allow' | 'hidden' | 'blocked'

export function accessFor(
  role: Role | undefined,
  permission: Permission,
  isProtected = false,
): Access {
  if (role === 'owner' || role === 'admin') return 'allow'
  const rule = rules[permission]
  if (rule === 'all') return role ? 'allow' : 'hidden'
  if (role === 'developer' && rule !== 'admin') {
    return rule === 'p' && isProtected ? 'blocked' : 'allow'
  }
  return 'hidden'
}

const roleRank: Record<Role, number> = { viewer: 0, developer: 1, admin: 2, owner: 3 }

interface RoleSource {
  org_role?: Role
  project_roles?: { project_id?: string; role: 'admin' | 'developer' | 'viewer' }[]
}

// The role of the user in one project (security, section 3). Owner and admin keep their role.
// For a developer or a viewer, the override of the project (user or team) replaces the org role.
// If there are many overrides, the highest one wins.
export function effectiveRole(
  me: RoleSource | null | undefined,
  projectId?: string,
): Role | undefined {
  const orgRole = me?.org_role
  if (!orgRole || orgRole === 'owner' || orgRole === 'admin' || !projectId) return orgRole
  const found = (me?.project_roles ?? []).filter((r) => r.project_id === projectId)
  if (found.length === 0) return orgRole
  return found.reduce<Role>(
    (best, r) => (roleRank[r.role] > roleRank[best] ? r.role : best),
    'viewer',
  )
}

// Pass the project id on a project screen, to use the role of that project.
export function useAccess(permission: Permission, isProtected = false, projectId?: string): Access {
  const me = useMe()
  return accessFor(effectiveRole(me.data, projectId), permission, isProtected)
}

// The effective role of the signed-in user in a project (the org role without a project id).
export function useRole(projectId?: string): Role | undefined {
  const me = useMe()
  return effectiveRole(me.data, projectId)
}
