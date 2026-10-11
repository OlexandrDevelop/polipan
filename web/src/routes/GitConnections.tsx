// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { DataList } from '@/components/DataList'
import { ErrorBanner } from '@/components/ErrorBanner'
import { AddGitConnectionDialog } from '@/components/GitConnectionDialog'
import { ConfirmDialog } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import { Banner, Button, EmptyState, Skeleton } from '@/components/ui'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'
import { NotFoundPage } from '@/routes/ErrorPages'

type Connection = components['schemas']['GitConnection']

// UI-27: Git connections of the organization. Tokens and keys are write-only.
export function GitConnectionsPage() {
  const queryClient = useQueryClient()
  const access = useAccess('git:manage')
  const [adding, setAdding] = useState(false)
  const [deleting, setDeleting] = useState<Connection | null>(null)
  const list = useQuery({
    queryKey: ['git-connections'],
    enabled: access === 'allow',
    queryFn: () => unwrap(api.GET('/git-connections')),
  })
  const remove = useMutation({
    mutationFn: (c: Connection) =>
      unwrap(
        api.DELETE('/git-connections/{connection_id}', {
          params: { path: { connection_id: c.id ?? '' } },
        }),
      ),
    onSuccess: async () => {
      setDeleting(null)
      await queryClient.invalidateQueries({ queryKey: ['git-connections'] })
    },
  })
  // The page is for admins and owners. Others get the "not found" page.
  if (access !== 'allow') return <NotFoundPage />

  const add = (
    <Button variant="primary" onClick={() => setAdding(true)}>
      {t('git.add')}
    </Button>
  )
  let body
  if (list.isPending) body = <Skeleton />
  else if (list.isError)
    body = <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
  else if (list.data.length === 0)
    body = <EmptyState title={t('git.empty_title')} text={t('git.empty_text')} action={add} />
  else
    body = (
      <DataList
        caption={t('git.caption')}
        rows={list.data}
        rowKey={(x) => x.id ?? ''}
        columns={[
          {
            header: t('git.name'),
            primary: true,
            cell: (x) => <span className="break-all">{x.name}</span>,
          },
          { header: t('git.provider'), cell: (x) => t(`git.provider.${x.provider ?? 'generic'}`) },
          {
            header: t('git.base_url'),
            cell: (x) => <span className="break-all font-mono text-xs">{x.base_url || '-'}</span>,
          },
          {
            header: t('git.webhook_url'),
            cell: (x) => (
              <span className="break-all font-mono text-xs">{x.webhook_url || '-'}</span>
            ),
          },
        ]}
        actions={(x) => (
          <Button variant="destructive" onClick={() => setDeleting(x)}>
            {t('git.delete')}
          </Button>
        )}
      />
    )

  return (
    <section>
      <PageHeader
        title={t('git.title')}
        actions={list.data && list.data.length > 0 ? add : undefined}
      />
      <div className="space-y-4">
        <Banner>{t('git.intro')}</Banner>
        {body}
      </div>
      {adding && (
        <AddGitConnectionDialog
          onClose={() => setAdding(false)}
          onCreated={() => setAdding(false)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={t('git.delete_title', { name: deleting.name ?? '' })}
          text={t('git.delete_text')}
          confirmLabel={t('git.delete')}
          busy={remove.isPending}
          error={remove.error ? <ErrorBanner error={remove.error} /> : undefined}
          onConfirm={() => remove.mutate(deleting)}
          onClose={() => {
            remove.reset()
            setDeleting(null)
          }}
        />
      )}
    </section>
  )
}
