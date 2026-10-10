// SPDX-License-Identifier: Apache-2.0

import { useQueryClient } from '@tanstack/react-query'
import type { QueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import type { Environment, Project, Service } from '@/scope'

// A stream is "lost" when it has been down for more than 5 s (REQ-UI-022).
const LOST_AFTER_MS = 5_000
const RETRY_MS = 3_000

interface StreamHandlers {
  onEvent: (message: MessageEvent<string>) => void
  // The stream is back after a loss. Read again what was missed.
  onReconnect: () => void
  onLost: (lost: boolean) => void
}

// An SSE stream that opens again by itself. The browser resumes with Last-Event-ID after a
// short break. After an HTTP error the browser gives up, so this opens a new stream.
export function openStream(url: string, names: string[], handlers: StreamHandlers): () => void {
  let source: EventSource | undefined
  let lost = false
  let lostTimer: ReturnType<typeof setTimeout> | undefined
  let retryTimer: ReturnType<typeof setTimeout> | undefined
  let stopped = false

  const connect = () => {
    const current = new EventSource(url)
    source = current
    current.onmessage = handlers.onEvent
    for (const name of names) current.addEventListener(name, handlers.onEvent as EventListener)
    current.onopen = () => {
      clearTimeout(lostTimer)
      lostTimer = undefined
      if (lost) {
        lost = false
        handlers.onLost(false)
        handlers.onReconnect()
      }
    }
    current.onerror = () => {
      lostTimer ??= setTimeout(() => {
        lost = true
        handlers.onLost(true)
      }, LOST_AFTER_MS)
      if (current.readyState === EventSource.CLOSED && !stopped) {
        retryTimer = setTimeout(connect, RETRY_MS)
      }
    }
  }
  connect()

  return () => {
    stopped = true
    clearTimeout(lostTimer)
    clearTimeout(retryTimer)
    source?.close()
  }
}

// What the bell shows (UI-46, deployments part): a deployment that failed or was rolled back.
export interface Notice {
  id: string
  deploymentId: string
  serviceId: string
  state: string
  reason?: string
  time: string
}

interface PlatformEvent {
  topic?: string
  object_id?: string
  payload?: { state?: string }
}

const DEPLOYMENT_KEYS = ['deployments', 'deployment', 'service', 'services', 'releases']
const BAD_ENDINGS = new Set(['failed', 'rolled_back'])

// One stream for the whole signed-in app (streamEvents). Each event refreshes the lists it
// changes, so the screens update without a reload. The polling of each screen stays as a backup.
export function useLiveEvents(enabled: boolean) {
  const queryClient = useQueryClient()
  const [disconnected, setDisconnected] = useState(false)
  const [notices, setNotices] = useState<Notice[]>([])

  useEffect(() => {
    if (!enabled) return
    const pending = new Set<string>()
    let flush: ReturnType<typeof setTimeout> | undefined
    const refresh = (keys: string[]) => {
      for (const key of keys) pending.add(key)
      flush ??= setTimeout(() => {
        flush = undefined
        for (const key of pending) void queryClient.invalidateQueries({ queryKey: [key] })
        pending.clear()
      }, 300)
    }

    const addNotice = async (id: string, state: string) => {
      try {
        const d = await queryClient.fetchQuery({
          queryKey: ['deployment', id],
          queryFn: () =>
            unwrap(
              api.GET('/deployments/{deployment_id}', { params: { path: { deployment_id: id } } }),
            ),
        })
        const notice: Notice = {
          id: `${id}:${state}`,
          deploymentId: id,
          serviceId: d.service_id ?? '',
          state,
          reason: d.failure_reason,
          time: d.finished_at ?? new Date().toISOString(),
        }
        setNotices((list) => [notice, ...list.filter((n) => n.id !== notice.id)].slice(0, 20))
      } catch {
        // The bell is a hint. A failed read is not shown here.
      }
    }

    const stop = openStream('/api/v1/events', [], {
      onEvent: (message) => {
        let event: PlatformEvent
        try {
          event = JSON.parse(message.data) as PlatformEvent
        } catch {
          return
        }
        const topic = event.topic ?? ''
        if (topic.startsWith('node')) refresh(['nodes'])
        if (topic.startsWith('deployment')) {
          refresh(DEPLOYMENT_KEYS)
          const state = event.payload?.state
          if (event.object_id && state && BAD_ENDINGS.has(state)) {
            void addNotice(event.object_id, state)
          }
        }
      },
      onReconnect: () => refresh([...DEPLOYMENT_KEYS, 'nodes']),
      onLost: setDisconnected,
    })
    return () => {
      stop()
      clearTimeout(flush)
    }
  }, [enabled, queryClient])

  return { disconnected, notices, clearNotices: () => setNotices([]) }
}

interface ServicePath {
  project: string
  env: string
  service: string
}

// Finds the URL names of a service by its id, from the lists the user already opened.
export function findServicePath(
  queryClient: QueryClient,
  serviceId: string,
): { path: ServicePath; name: string } | null {
  for (const [, services] of queryClient.getQueriesData<Service[]>({ queryKey: ['services'] })) {
    const service = services?.find((s) => s.id === serviceId)
    if (!service?.name) continue
    for (const [, envs] of queryClient.getQueriesData<Environment[]>({
      queryKey: ['environments'],
    })) {
      const env = envs?.find((e) => e.id === service.environment_id)
      if (!env?.slug) continue
      const projects = queryClient.getQueryData<Project[]>(['projects'])
      const project = projects?.find((p) => p.id === env.project_id)
      if (project?.slug) {
        return {
          path: { project: project.slug, env: env.slug, service: service.name },
          name: service.name,
        }
      }
    }
    return null
  }
  return null
}
