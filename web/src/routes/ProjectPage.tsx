// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { PageHeader } from '@/components/PageHeader'
import {
  Button,
  Card,
  CheckboxField,
  EmptyState,
  EnvironmentBadges,
  SelectField,
  Skeleton,
  StatusBadge,
  TextField,
} from '@/components/ui'
import { t, tState } from '@/i18n'
import { useAccess, accessFor, useRole } from '@/permissions'
import { useMe } from '@/auth'
import { NotFoundPage } from '@/routes/ErrorPages'
import { useScope, useServices } from '@/scope'
import type { Environment } from '@/scope'
import { serviceTone } from '@/status'

function CreateEnvironmentDialog({
  projectId,
  projectSlug,
  environments,
  onClose,
}: {
  projectId: string
  projectSlug: string
  environments: Environment[]
  onClose: () => void
}) {
  const { org } = useParams({ from: '/o/$org' })
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const me = useMe()
  const isAdmin = me.data?.org_role === 'owner' || me.data?.org_role === 'admin'
  const [name, setName] = useState('')
  const [kind, setKind] = useState<'staging' | 'production' | 'custom'>('staging')
  const [copyFrom, setCopyFrom] = useState('')
  const [isProtected, setProtected] = useState(false)

  const create = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/projects/{project_id}/environments', {
          params: { path: { project_id: projectId } },
          body: {
            name: name.trim(),
            kind,
            protected: isProtected,
            ...(copyFrom ? { clone_from_environment_id: copyFrom } : {}),
          },
        }),
      ),
    onSuccess: async (env) => {
      await queryClient.invalidateQueries({ queryKey: ['environments', projectId] })
      await navigate({
        to: '/o/$org/p/$project/e/$env',
        params: { org, project: projectSlug, env: env.slug ?? '' },
      })
    },
  })

  function submit(event: FormEvent) {
    event.preventDefault()
    create.mutate()
  }

  return (
    <Modal title={t('env.create_title')} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        {create.error && <ErrorBanner error={create.error} title={t('env.create_failed')} />}
        <TextField
          label={t('env.name')}
          required
          autoFocus
          autoComplete="off"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <SelectField
          label={t('env.kind')}
          value={kind}
          onChange={(e) => setKind(e.target.value as typeof kind)}
        >
          <option value="staging">{t('env.kind.staging')}</option>
          <option value="production">{t('env.kind.production')}</option>
          <option value="custom">{t('env.kind.custom')}</option>
        </SelectField>
        {environments.length > 0 && (
          <SelectField
            label={t('env.copy_from')}
            value={copyFrom}
            onChange={(e) => setCopyFrom(e.target.value)}
          >
            <option value="">{t('env.copy_none')}</option>
            {environments.map((e) => (
              <option key={e.id} value={e.id}>
                {e.name}
              </option>
            ))}
          </SelectField>
        )}
        {isAdmin && (
          <CheckboxField
            label={t('env.protected_label')}
            hint={t('env.protected_hint')}
            checked={isProtected}
            onChange={(e) => setProtected(e.target.checked)}
          />
        )}
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type="submit" variant="primary" loading={create.isPending}>
            {t('env.create')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

function ServiceCards({ environment, project }: { environment: Environment; project: string }) {
  const role = useRole(environment.project_id)
  const { org } = useParams({ from: '/o/$org' })
  const services = useServices(environment.id)
  const access = accessFor(role, 'service:create', environment.protected)

  const addButton =
    access === 'hidden' ? undefined : access === 'blocked' ? (
      <Button variant="primary" reason={t('common.needs_admin')}>
        {t('service.add')}
      </Button>
    ) : (
      <Link
        to="/o/$org/p/$project/e/$env/new"
        params={{ org, project, env: environment.slug ?? '' }}
        className="inline-flex min-h-8 items-center rounded-sm border border-primary bg-primary px-3 py-1 text-sm font-semibold text-on-primary coarse:min-h-11"
      >
        {t('service.add')}
      </Link>
    )

  if (services.isPending) return <Skeleton />
  if (services.isError) {
    return <ErrorBanner error={services.error} onRetry={() => void services.refetch()} />
  }
  if (services.data.length === 0) {
    return (
      <EmptyState
        title={t('service.empty_title')}
        text={t('service.empty_text')}
        action={addButton}
      />
    )
  }
  return (
    <div className="space-y-4">
      {addButton && <div className="flex justify-end">{addButton}</div>}
      <ul className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
        {services.data.map((service) => (
          <li key={service.id}>
            <Card className="space-y-2">
              <h3 className="break-words text-lg font-semibold">
                <Link
                  to="/o/$org/p/$project/e/$env/s/$service"
                  params={{
                    org,
                    project,
                    env: environment.slug ?? '',
                    service: service.name ?? '',
                  }}
                  className="text-primary underline"
                >
                  {service.name}
                </Link>
              </h3>
              <StatusBadge
                tone={serviceTone(service.status)}
                label={tState('status.service', service.status)}
              />
              {typeof service.config?.image === 'string' && (
                <p className="break-all font-mono text-sm text-muted">{service.config.image}</p>
              )}
            </Card>
          </li>
        ))}
      </ul>
    </div>
  )
}

// UI-04 project page and UI-33 environments: environment tabs and the services of one environment.
export function ProjectPage() {
  const { org, project: projectSlug, env: envSlug } = useParams({ strict: false })
  const navigate = useNavigate()
  const scope = useScope(projectSlug ?? '', envSlug)
  const [creating, setCreating] = useState(false)
  const canCreateEnv = useAccess('env:create', false, scope.project?.id)
  const canManageRoles = useAccess('member:manage')

  if (scope.state === 'pending') return <Skeleton />
  if (scope.state === 'error') return <ErrorBanner error={scope.error} onRetry={scope.retry} />
  if (scope.state === 'notfound' || !scope.project) return <NotFoundPage />
  const { project, environment, environments } = scope
  const orgSlug = org ?? ''

  return (
    <section>
      <PageHeader
        title={project.name ?? ''}
        actions={
          <>
            {canManageRoles === 'allow' && (
              <Link
                to="/o/$org/p/$project/roles"
                params={{ org: orgSlug, project: project.slug ?? '' }}
                className="inline-flex min-h-8 items-center rounded-sm border border-border-strong px-3 py-1 text-sm coarse:min-h-11"
              >
                {t('roles.link')}
              </Link>
            )}
            {canCreateEnv === 'allow' && (
              <Button onClick={() => setCreating(true)}>{t('env.create')}</Button>
            )}
          </>
        }
      />
      {environments.length === 0 || !environment ? (
        <EmptyState
          title={t('env.empty_title')}
          text={t('env.empty_text')}
          action={
            canCreateEnv === 'allow' ? (
              <Button variant="primary" onClick={() => setCreating(true)}>
                {t('env.create')}
              </Button>
            ) : undefined
          }
        />
      ) : (
        <>
          <nav aria-label={t('env.nav')} className="mb-4">
            <div className="sm:hidden">
              <SelectField
                label={t('env.select')}
                value={environment.slug ?? ''}
                onChange={(e) =>
                  void navigate({
                    to: '/o/$org/p/$project/e/$env',
                    params: { org: orgSlug, project: project.slug ?? '', env: e.target.value },
                  })
                }
              >
                {environments.map((e) => (
                  <option key={e.id} value={e.slug}>
                    {e.name}
                  </option>
                ))}
              </SelectField>
            </div>
            <ul className="hidden gap-1 overflow-x-auto border-b border-border sm:flex">
              {environments.map((e) => (
                <li key={e.id}>
                  <Link
                    to="/o/$org/p/$project/e/$env"
                    params={{ org: orgSlug, project: project.slug ?? '', env: e.slug ?? '' }}
                    aria-current={e.id === environment.id ? 'page' : undefined}
                    className="block whitespace-nowrap border-b-4 border-transparent px-4 py-2 text-muted hover:text-text aria-[current=page]:border-primary aria-[current=page]:font-semibold aria-[current=page]:text-text"
                  >
                    {e.name}
                  </Link>
                </li>
              ))}
            </ul>
          </nav>
          <div
            className={`space-y-4 ${
              environment.kind === 'production' || environment.protected
                ? 'border-t-4 border-danger pt-4'
                : ''
            }`}
          >
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-lg font-semibold">{environment.name}</h2>
              <EnvironmentBadges kind={environment.kind} isProtected={environment.protected} />
            </div>
            <ServiceCards environment={environment} project={project.slug ?? ''} />
          </div>
        </>
      )}
      {creating && (
        <CreateEnvironmentDialog
          projectId={project.id ?? ''}
          projectSlug={project.slug ?? ''}
          environments={environments}
          onClose={() => setCreating(false)}
        />
      )}
    </section>
  )
}
