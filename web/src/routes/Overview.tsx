// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@tanstack/react-query'
import { api } from '@/api/client'

// The first page: it shows that the web app reaches the server through the generated API client.
export function Overview() {
  const health = useQuery({
    queryKey: ['health'],
    queryFn: async () => {
      // /healthz answers 503 with a body when the database is down, so read the body for both.
      const { data, error } = await api.GET('/healthz')
      if (data) return data
      if (error) throw error
      throw new Error('no answer')
    },
    refetchInterval: 10_000,
  })
  const version = useQuery({
    queryKey: ['version'],
    queryFn: async () => {
      const { data, error } = await api.GET('/version')
      if (error) throw error
      return data
    },
  })

  return (
    <section className="space-y-4">
      <h1 className="text-xl font-semibold">Overview</h1>
      <dl className="grid max-w-md grid-cols-[auto_1fr] gap-x-6 gap-y-2 rounded-md border border-border bg-surface p-4">
        <dt className="text-muted">Server</dt>
        <dd>
          {health.isPending ? 'Checking...' : health.isError ? 'No answer' : health.data.status}
        </dd>
        <dt className="text-muted">Database</dt>
        <dd>{health.data?.database === undefined ? '-' : health.data.database ? 'up' : 'down'}</dd>
        <dt className="text-muted">Version</dt>
        <dd>{version.data?.version ?? '-'}</dd>
        <dt className="text-muted">API</dt>
        <dd>{version.data?.api ?? '-'}</dd>
      </dl>
    </section>
  )
}
