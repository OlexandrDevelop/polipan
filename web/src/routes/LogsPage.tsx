// SPDX-License-Identifier: Apache-2.0

import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { CommonFieldsForm, LogSearchResults } from '@/components/LogSearch'
import type { CommonFields } from '@/components/LogSearch'
import { PageHeader } from '@/components/PageHeader'
import { Banner, Button, Optional, SelectField } from '@/components/ui'
import { t } from '@/i18n'
import { rangeWindow } from '@/logsData'
import type { LogRange, SearchParams } from '@/logsData'
import type { LogEntry } from '@/logText'
import type { LogsSearch } from '@/logsSearch'
import { useEnvironments, useNodes, useProjects, useServices } from '@/scope'
import type { Service } from '@/scope'

function buildParams(s: LogsSearch): SearchParams {
  const range = (s.range ?? '1h') as LogRange
  const { from, to } = rangeWindow(range, s.from ?? '', s.to ?? '')
  const context = Number(s.context ?? '0')
  return {
    ...(s.q?.trim() ? { q: s.q.trim() } : {}),
    ...(s.project ? { project_id: s.project } : {}),
    ...(s.env ? { environment_id: s.env } : {}),
    ...(s.service ? { service_id: s.service } : {}),
    ...(s.release ? { release_id: s.release } : {}),
    ...(s.node ? { node_id: s.node } : {}),
    ...(s.level ? { level: s.level as SearchParams['level'] } : {}),
    ...(s.stream ? { stream: s.stream as SearchParams['stream'] } : {}),
    ...(from ? { from } : {}),
    ...(to ? { to } : {}),
    ...(context > 0 ? { context: Math.min(20, context) } : {}),
  }
}

// UI-37: search the logs of the whole org. The filters narrow it to one place.
export function LogsPage() {
  const { org } = useParams({ from: '/o/$org/logs' })
  const search = useSearch({ from: '/o/$org/logs' })
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const orgQuery = useQuery({ queryKey: ['org'], queryFn: () => unwrap(api.GET('/org')) })
  const retentionDays = orgQuery.data?.settings?.logs_retention_days ?? 30

  const [common, setCommon] = useState<CommonFields>({
    q: search.q ?? '',
    level: search.level ?? '',
    stream: search.stream ?? '',
    range: (search.range ?? '1h') as LogRange,
    from: search.from ?? '',
    to: search.to ?? '',
    context: search.context ?? '0',
  })
  const [project, setProject] = useState(search.project ?? '')
  const [env, setEnv] = useState(search.env ?? '')
  const [service, setService] = useState(search.service ?? '')
  const [release, setRelease] = useState(search.release ?? '')
  const [node, setNode] = useState(search.node ?? '')
  const [active, setActive] = useState<SearchParams>(() => buildParams(search))
  const [activeQuery, setActiveQuery] = useState(search.q ?? '')
  // A search that was opened from a line, with the way back.
  const [back, setBack] = useState<{ params: SearchParams; query: string } | null>(null)

  const projects = useProjects()
  const environments = useEnvironments(project || undefined)
  const services = useServices(env || undefined)
  const nodes = useNodes()
  const releases = useQuery({
    queryKey: ['releases', service],
    enabled: Boolean(service),
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/releases', {
          params: { path: { service_id: service }, query: { limit: 50 } },
        }),
      ),
  })

  // Service names for the results, from the lists the user already opened.
  const serviceNames = useMemo(() => {
    const map = new Map<string, string>()
    for (const [, list] of queryClient.getQueriesData<Service[]>({ queryKey: ['services'] })) {
      for (const s of Array.isArray(list) ? list : []) if (s.id && s.name) map.set(s.id, s.name)
    }
    return map
    // The lists change when a filter loads a new one.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [queryClient, services.data])

  function submit(event: FormEvent) {
    event.preventDefault()
    const next: LogsSearch = {
      ...(common.q.trim() ? { q: common.q.trim() } : {}),
      ...(project ? { project } : {}),
      ...(env ? { env } : {}),
      ...(service ? { service } : {}),
      ...(release ? { release } : {}),
      ...(node ? { node } : {}),
      ...(common.level ? { level: common.level } : {}),
      ...(common.stream ? { stream: common.stream } : {}),
      range: common.range,
      ...(common.range === 'custom' ? { from: common.from, to: common.to } : {}),
      ...(Number(common.context) > 0 ? { context: common.context } : {}),
    }
    setBack(null)
    setActive(buildParams(next))
    setActiveQuery(common.q)
    void navigate({ to: '/o/$org/logs', params: { org }, search: next })
  }

  // Shows the lines around one line: the same service, one minute before and after.
  function openContext(entry: LogEntry) {
    if (!entry.time || !entry.serviceId) return
    const at = new Date(entry.time).getTime()
    setBack({ params: active, query: activeQuery })
    setActive({
      service_id: entry.serviceId,
      from: new Date(at - 60_000).toISOString(),
      to: new Date(at + 60_000).toISOString(),
    })
    setActiveQuery('')
  }

  return (
    <section>
      <PageHeader title={t('logsearch.title')} />
      <form onSubmit={submit} className="mb-4 space-y-3">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <CommonFieldsForm
            value={common}
            onChange={setCommon}
            retentionDays={retentionDays}
            searchId="logs-search-text"
          />
          <Optional filled={project !== '' || node !== ''}>
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              <SelectField
                label={t('logsearch.project')}
                value={project}
                onChange={(e) => {
                  setProject(e.target.value)
                  setEnv('')
                  setService('')
                  setRelease('')
                }}
              >
                <option value="">{t('logsearch.any')}</option>
                {(projects.data ?? []).map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </SelectField>
              <SelectField
                label={t('logsearch.environment')}
                value={env}
                disabled={!project}
                onChange={(e) => {
                  setEnv(e.target.value)
                  setService('')
                  setRelease('')
                }}
              >
                <option value="">{t('logsearch.any')}</option>
                {(environments.data ?? []).map((e) => (
                  <option key={e.id} value={e.id}>
                    {e.name}
                  </option>
                ))}
              </SelectField>
              <SelectField
                label={t('logsearch.service')}
                value={service}
                disabled={!env}
                onChange={(e) => {
                  setService(e.target.value)
                  setRelease('')
                }}
              >
                <option value="">{t('logsearch.any')}</option>
                {(services.data ?? []).map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </SelectField>
              <SelectField
                label={t('logsearch.release')}
                value={release}
                disabled={!service}
                onChange={(e) => setRelease(e.target.value)}
              >
                <option value="">{t('logsearch.any')}</option>
                {(releases.data?.items ?? []).map((r) => (
                  <option key={r.id} value={r.id}>
                    v{r.number}
                  </option>
                ))}
              </SelectField>
              <SelectField
                label={t('logsearch.node')}
                value={node}
                onChange={(e) => setNode(e.target.value)}
              >
                <option value="">{t('logsearch.any')}</option>
                {(nodes.data ?? []).map((n) => (
                  <option key={n.id} value={n.id}>
                    {n.name}
                  </option>
                ))}
              </SelectField>
            </div>
          </Optional>
        </div>
        <Button type="submit" variant="primary">
          {t('logsearch.submit')}
        </Button>
      </form>
      {back && (
        <div className="mb-3">
          <Banner
            title={t('logsearch.context_title')}
            action={
              <Button
                onClick={() => {
                  setActive(back.params)
                  setActiveQuery(back.query)
                  setBack(null)
                }}
              >
                {t('logsearch.back')}
              </Button>
            }
          >
            {t('logsearch.context_text')}
          </Banner>
        </div>
      )}
      <LogSearchResults
        params={active}
        query={activeQuery}
        showService
        fileName="logs-search.txt"
        onContext={openContext}
        serviceNames={serviceNames}
      />
    </section>
  )
}
