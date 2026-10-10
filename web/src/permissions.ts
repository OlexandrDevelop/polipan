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

export function useAccess(permission: Permission, isProtected = false): Access {
  const me = useMe()
  return accessFor(me.data?.org_role, permission, isProtected)
}
