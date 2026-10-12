// SPDX-License-Identifier: Apache-2.0

import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { CodeBlock } from '@/components/Copy'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Banner, Button, Panel } from '@/components/ui'
import { t } from '@/i18n'
import { accessFor } from '@/permissions'
import type { Service } from '@/scope'

// UI-27 (createDeployKey): an SSH deploy key and the webhook of a Git service. The public key,
// the webhook URL and the secret are shown once, then forgotten.
export function ServiceDeployKey({
  service,
  environmentProtected,
}: {
  service: Service
  environmentProtected?: boolean
}) {
  const me = useMe()
  const access = accessFor(me.data?.org_role, 'service:update', environmentProtected)
  const [result, setResult] = useState<{
    public_key?: string
    webhook_url?: string
    webhook_secret?: string
  } | null>(null)
  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/services/{service_id}/deploy-key', {
          params: { path: { service_id: service.id ?? '' } },
        }),
      ),
    onSuccess: setResult,
  })
  if (service.source_type !== 'git' || access === 'hidden') return null
  return (
    <Panel id="settings-deploy-key" title={t('deploykey.title')} className="space-y-4">
      <p className="text-muted">{t('deploykey.text')}</p>
      {create.error && <ErrorBanner error={create.error} title={t('deploykey.failed')} />}
      {result ? (
        <div className="space-y-4">
          <Banner variant="warning">{t('deploykey.once')}</Banner>
          {result.public_key && (
            <CodeBlock text={result.public_key} label={t('deploykey.public_key')} />
          )}
          {result.webhook_url && (
            <CodeBlock text={result.webhook_url} label={t('git.webhook_url')} />
          )}
          {result.webhook_secret && (
            <CodeBlock text={result.webhook_secret} label={t('git.webhook_secret')} />
          )}
          <Button onClick={() => setResult(null)}>{t('git.done')}</Button>
        </div>
      ) : (
        <Button
          loading={create.isPending}
          reason={access === 'blocked' ? t('common.needs_admin') : undefined}
          onClick={() => create.mutate()}
        >
          {t('deploykey.create')}
        </Button>
      )}
    </Panel>
  )
}
