// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@tanstack/react-query'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'

export type Project = components['schemas']['Project']
export type Environment = components['schemas']['Environment']
export type Service = components['schemas']['Service']
export type Node = components['schemas']['Node']

// URLs use slugs (navigation, section 2). The API uses ids, so these hooks find the object by slug.
// A project list has at most 200 entries in M1.
export function useProjects() {
  return useQuery({
    queryKey: ['projects'],
    queryFn: async () =>
      (await unwrap(api.GET('/projects', { params: { query: { limit: 200 } } }))).items ?? [],
  })
}

export function useEnvironments(projectId: string | undefined) {
  return useQuery({
    queryKey: ['environments', projectId],
    enabled: Boolean(projectId),
    queryFn: () =>
      unwrap(
        api.GET('/projects/{project_id}/environments', {
          params: { path: { project_id: projectId! } },
        }),
      ),
  })
}

export function useServices(environmentId: string | undefined) {
  return useQuery({
    queryKey: ['services', environmentId],
    enabled: Boolean(environmentId),
    queryFn: () =>
      unwrap(
        api.GET('/environments/{environment_id}/services', {
          params: { path: { environment_id: environmentId! } },
        }),
      ),
  })
}

export function useNodes(refetchInterval?: number) {
  return useQuery({
    queryKey: ['nodes'],
    queryFn: () => unwrap(api.GET('/nodes')),
    refetchInterval,
  })
}

interface Scope {
  state: 'pending' | 'error' | 'notfound' | 'ok'
  error: unknown
  retry: () => void
  project?: Project
  environment?: Environment
  environments: Environment[]
  service?: Service
}

// Finds the project, the environment and the service of a URL.
// The environment is optional. Without it the first environment is used.
export function useScope(projectSlug: string, envSlug?: string, serviceName?: string): Scope {
  const projects = useProjects()
  const project = projects.data?.find((p) => p.slug === projectSlug)
  const environments = useEnvironments(project?.id)
  const environment = envSlug
    ? environments.data?.find((e) => e.slug === envSlug)
    : environments.data?.[0]
  const services = useServices(serviceName ? environment?.id : undefined)
  const service = services.data?.find((s) => s.name === serviceName)

  const queries = [projects, environments, ...(serviceName ? [services] : [])]
  const failed = queries.find((q) => q.isError)
  const retry = () => {
    for (const q of queries) if (q.isError) void q.refetch()
  }
  const base = { environments: environments.data ?? [], retry }
  if (failed) return { ...base, state: 'error', error: failed.error }
  if (projects.isPending) return { ...base, state: 'pending', error: null }
  if (!project) return { ...base, state: 'notfound', error: null }
  if (environments.isPending) return { ...base, state: 'pending', error: null, project }
  if (!environment) return { ...base, state: 'notfound', error: null, project }
  if (serviceName && services.isPending) {
    return { ...base, state: 'pending', error: null, project, environment }
  }
  if (serviceName && !service)
    return { ...base, state: 'notfound', error: null, project, environment }
  return { ...base, state: 'ok', error: null, project, environment, service }
}
