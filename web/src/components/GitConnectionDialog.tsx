// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { components } from '@api/schema'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { CodeBlock } from '@/components/Copy'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { Banner, Button, Optional, SecretField, SelectField, TextField } from '@/components/ui'
import { t } from '@/i18n'

type GitConnection = components['schemas']['GitConnection']

// Adds a Git connection. The webhook secret is shown once after it was created (UI-27, UI-32).
export function AddGitConnectionDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: (c: GitConnection) => void
}) {
  const queryClient = useQueryClient()
  const [provider, setProvider] = useState<'github' | 'gitlab' | 'gitea' | 'generic'>('github')
  const [name, setName] = useState('')
  const [baseUrl, setBaseUrl] = useState('')
  const [token, setToken] = useState('')
  const [appId, setAppId] = useState('')
  const [installationId, setInstallationId] = useState('')
  const [privateKey, setPrivateKey] = useState('')
  const [submitted, setSubmitted] = useState(false)
  const nameError = name.trim() === '' ? t('git.name_required') : null

  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/git-connections', {
          body: {
            provider,
            name: name.trim(),
            ...(baseUrl.trim() ? { base_url: baseUrl.trim() } : {}),
            ...(provider === 'github'
              ? { app_id: appId.trim(), installation_id: installationId.trim() }
              : {}),
            ...(provider === 'github' && privateKey ? { private_key: privateKey } : {}),
            ...(provider !== 'github' && token ? { token } : {}),
          },
        }),
      ),
    onSuccess: async (connection) => {
      // The token and key are write-only: clear them at once.
      setToken('')
      setPrivateKey('')
      await queryClient.invalidateQueries({ queryKey: ['git-connections'] })
      setDone(connection)
    },
  })
  // The webhook secret comes only in the create answer: show it once, then forget it.
  const [done, setDone] = useState<GitConnection | null>(null)

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (!nameError) create.mutate()
  }

  if (done) {
    return (
      <Modal title={t('git.added_title')} onClose={() => onCreated(done)}>
        <div className="space-y-4">
          <p>{t('git.webhook_text')}</p>
          {done.webhook_url && <CodeBlock text={done.webhook_url} label={t('git.webhook_url')} />}
          {done.webhook_secret && (
            <>
              <Banner variant="warning">{t('git.webhook_secret_once')}</Banner>
              <CodeBlock text={done.webhook_secret} label={t('git.webhook_secret')} />
            </>
          )}
          <div className="flex justify-end">
            <Button variant="primary" onClick={() => onCreated(done)}>
              {t('git.done')}
            </Button>
          </div>
        </div>
      </Modal>
    )
  }

  return (
    <Modal title={t('git.add_title')} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        {create.error && <ErrorBanner error={create.error} title={t('git.add_failed')} />}
        <SelectField
          label={t('git.provider')}
          value={provider}
          onChange={(e) => setProvider(e.target.value as typeof provider)}
        >
          <option value="github">{t('git.provider.github')}</option>
          <option value="gitlab">{t('git.provider.gitlab')}</option>
          <option value="gitea">{t('git.provider.gitea')}</option>
          <option value="generic">{t('git.provider.generic')}</option>
        </SelectField>
        <TextField
          label={t('git.name')}
          required
          autoComplete="off"
          error={submitted ? nameError : null}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <Optional
          filled={
            baseUrl !== '' ||
            appId !== '' ||
            installationId !== '' ||
            privateKey !== '' ||
            token !== ''
          }
        >
          <div className="space-y-4">
            {provider !== 'github' && (
              <TextField
                label={t('git.base_url')}
                hint={t('git.base_url_hint')}
                type="url"
                inputMode="url"
                autoComplete="off"
                value={baseUrl}
                onChange={(e) => setBaseUrl(e.target.value)}
              />
            )}
            {provider === 'github' ? (
              <>
                <TextField
                  label={t('git.app_id')}
                  autoComplete="off"
                  value={appId}
                  onChange={(e) => setAppId(e.target.value)}
                />
                <TextField
                  label={t('git.installation_id')}
                  autoComplete="off"
                  value={installationId}
                  onChange={(e) => setInstallationId(e.target.value)}
                />
                <SecretField
                  label={t('git.private_key')}
                  value={privateKey}
                  onValue={setPrivateKey}
                />
              </>
            ) : (
              <SecretField label={t('git.token')} value={token} onValue={setToken} />
            )}
          </div>
        </Optional>
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type="submit" variant="primary" loading={create.isPending}>
            {t('git.add')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}
