// SPDX-License-Identifier: Apache-2.0

import { useQuery } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { CopyButton } from '@/components/Copy'
import { ErrorBanner } from '@/components/ErrorBanner'
import { LargerScreenNotice } from '@/components/LargerScreen'
import { PageHeader } from '@/components/PageHeader'
import { EmptyState, Skeleton } from '@/components/ui'
import { t } from '@/i18n'

function show(value: unknown): string {
  if (value === undefined || value === null) return '-'
  return typeof value === 'string' ? value : JSON.stringify(value)
}

// UI-35: the differences between two releases. Secrets are only version numbers, never values.
export function ReleaseDiffPage() {
  const params = useParams({
    from: '/o/$org/p/$project/e/$env/s/$service/releases/$release/diff/$other',
  })
  const left = useQuery({
    queryKey: ['release', params.release],
    queryFn: () =>
      unwrap(
        api.GET('/releases/{release_id}', { params: { path: { release_id: params.release } } }),
      ),
  })
  const right = useQuery({
    queryKey: ['release', params.other],
    queryFn: () =>
      unwrap(api.GET('/releases/{release_id}', { params: { path: { release_id: params.other } } })),
  })
  const diff = useQuery({
    queryKey: ['release-diff', params.release, params.other],
    queryFn: () =>
      unwrap(
        api.GET('/releases/{release_id}/diff/{other_release_id}', {
          params: { path: { release_id: params.release, other_release_id: params.other } },
        }),
      ),
  })

  const a = `v${left.data?.number ?? ''}`
  const b = `v${right.data?.number ?? ''}`
  const changes = diff.data?.changes ?? []
  const text = changes
    .map((c) => `${c.path}\n- ${a}: ${show(c.before)}\n+ ${b}: ${show(c.after)}`)
    .join('\n\n')

  const breadcrumb = (
    <Link
      to="/o/$org/p/$project/e/$env/s/$service/$tab"
      params={{ ...params, tab: 'releases' }}
      className="text-primary underline"
    >
      {params.service} › {t('service.tab.releases')}
    </Link>
  )
  const failed = [diff, left, right].find((q) => q.isError)

  return (
    <section>
      <PageHeader
        title={t('diff.title', { a, b })}
        context={[params.service]}
        breadcrumb={breadcrumb}
        actions={
          <>
            <Link
              to="/o/$org/p/$project/e/$env/s/$service/releases/$release/diff/$other"
              params={{ ...params, release: params.other, other: params.release }}
              className="inline-flex min-h-8 items-center rounded-sm border border-border-strong px-3 py-1 text-sm coarse:min-h-11"
            >
              {t('diff.swap')}
            </Link>
            {changes.length > 0 && <CopyButton text={text} label={t('diff.copy')} />}
          </>
        }
      />
      {failed && (
        <ErrorBanner
          error={failed.error}
          onRetry={() => {
            for (const q of [diff, left, right]) if (q.isError) void q.refetch()
          }}
        />
      )}
      {diff.isPending && <Skeleton />}
      {diff.data && changes.length === 0 && (
        <EmptyState title={t('diff.empty_title')} text={t('diff.empty_text')} />
      )}
      {changes.length > 0 && (
        <>
          <p className="mb-3 text-sm text-muted">{t('diff.secrets_note')}</p>
          <LargerScreenNotice className="mb-3 sm:hidden" />
          <ul className="space-y-1 sm:hidden">
            {changes.map((c, i) => (
              <li key={`${c.path}-${i}`} className="break-all font-mono text-sm">
                {c.path}
              </li>
            ))}
          </ul>
          <div className="hidden overflow-x-auto sm:block">
            <table className="w-full border-collapse text-left text-sm">
              <caption className="sr-only">{t('diff.caption')}</caption>
              <thead>
                <tr className="bg-surface">
                  <th scope="col" className="p-3 font-semibold text-muted">
                    {t('diff.field')}
                  </th>
                  <th scope="col" className="p-3 font-semibold text-muted">
                    {a}
                  </th>
                  <th scope="col" className="p-3 font-semibold text-muted">
                    {b}
                  </th>
                </tr>
              </thead>
              <tbody>
                {changes.map((c, i) => (
                  <tr key={`${c.path}-${i}`} className="border-b border-border align-top">
                    <th scope="row" className="break-all p-3 font-mono font-normal">
                      {c.path}
                    </th>
                    <td className="break-all p-3 font-mono">
                      <span aria-hidden="true">- </span>
                      {show(c.before)}
                    </td>
                    <td className="break-all p-3 font-mono">
                      <span aria-hidden="true">+ </span>
                      {show(c.after)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </section>
  )
}
