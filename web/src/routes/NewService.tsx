// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { PageHeader } from '@/components/PageHeader'
import { Banner, Button, Skeleton, TextField } from '@/components/ui'
import { t } from '@/i18n'
import { useAccess } from '@/permissions'
import { NotFoundPage } from '@/routes/ErrorPages'
import { useScope } from '@/scope'

const NAME_PATTERN = /^[a-z0-9-]{1,40}$/

function validate(name: string, image: string, port: string) {
  const errors: { name?: string; image?: string; port?: string } = {}
  if (!NAME_PATTERN.test(name)) errors.name = t('service.name_invalid')
  if (image.trim() === '' || /\s/.test(image.trim())) errors.image = t('service.image_invalid')
  if (port !== '') {
    const n = Number(port)
    if (!Number.isInteger(n) || n < 1 || n > 65535) errors.port = t('service.port_invalid')
  }
  return errors
}

// The service form for an image source (M1). Git, templates and databases come in M2.
export function NewServicePage() {
  const {
    org,
    project: projectSlug,
    env: envSlug,
  } = useParams({ from: '/o/$org/p/$project/e/$env/new' })
  const scope = useScope(projectSlug, envSlug)
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [image, setImage] = useState('')
  const [port, setPort] = useState('')
  const [submitted, setSubmitted] = useState(false)
  const access = useAccess('service:create', scope.environment?.protected)

  const create = useMutation({
    mutationFn: () => {
      const portNumber = port === '' ? undefined : Number(port)
      return unwrap(
        api.POST('/environments/{environment_id}/services', {
          params: { path: { environment_id: scope.environment?.id ?? '' } },
          body: {
            name,
            type: 'app',
            source: { image: image.trim() },
            config: { image: image.trim(), ...(portNumber ? { port: portNumber } : {}) },
          },
        }),
      )
    },
    onSuccess: async (service) => {
      await queryClient.invalidateQueries({ queryKey: ['services', scope.environment?.id] })
      await navigate({
        to: '/o/$org/p/$project/e/$env/s/$service',
        params: { org, project: projectSlug, env: envSlug, service: service.name ?? name },
      })
    },
  })

  if (scope.state === 'pending') return <Skeleton />
  if (scope.state === 'error') return <ErrorBanner error={scope.error} onRetry={scope.retry} />
  if (scope.state === 'notfound') return <NotFoundPage />

  const errors = validate(name, image, port)
  const valid = Object.keys(errors).length === 0

  function submit(event: FormEvent) {
    event.preventDefault()
    setSubmitted(true)
    if (valid) create.mutate()
  }

  const back = (
    <Link
      to="/o/$org/p/$project/e/$env"
      params={{ org, project: projectSlug, env: envSlug }}
      className="text-primary underline"
    >
      {scope.project?.name} › {scope.environment?.name}
    </Link>
  )

  return (
    <section className="max-w-xl">
      <PageHeader
        title={t('service.new_title')}
        context={[scope.environment?.name ?? '', scope.project?.name ?? '']}
        breadcrumb={back}
      />
      {access !== 'allow' ? (
        <Banner variant="warning">
          {access === 'blocked' ? t('common.needs_admin') : t('common.no_permission')}
        </Banner>
      ) : (
        <form onSubmit={submit} className="space-y-4">
          {create.error && <ErrorBanner error={create.error} title={t('service.create_failed')} />}
          <TextField
            label={t('service.name')}
            hint={t('service.name_hint')}
            required
            autoComplete="off"
            error={submitted ? errors.name : null}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <TextField
            label={t('service.image')}
            hint={t('service.image_hint')}
            required
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            error={submitted ? errors.image : null}
            value={image}
            onChange={(e) => setImage(e.target.value)}
          />
          <TextField
            label={t('service.port')}
            hint={t('service.port_hint')}
            type="number"
            inputMode="numeric"
            error={submitted ? errors.port : null}
            value={port}
            onChange={(e) => setPort(e.target.value)}
          />
          <div className="flex flex-wrap justify-end gap-2">
            <Button type="submit" variant="primary" loading={create.isPending}>
              {t('service.create')}
            </Button>
          </div>
        </form>
      )}
    </section>
  )
}
