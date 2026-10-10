// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api } from '@/api/client'
import { ApiError, unwrap } from '@/api/errors'
import { useMe } from '@/auth'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { Banner, Button, CheckboxField, TextField } from '@/components/ui'
import { t } from '@/i18n'
import { accessFor } from '@/permissions'
import { useServices } from '@/scope'
import type { Service } from '@/scope'

interface Base {
  org: string
  project: string
  env: string
}

// Waits until a manual backup run of the policy has ended. A failed run stops the delete.
async function finalBackup(policyId: string): Promise<void> {
  await unwrap(
    api.POST('/backup-policies/{policy_id}/run', {
      params: { path: { policy_id: policyId }, header: { 'Idempotency-Key': crypto.randomUUID() } },
    }),
  )
  const deadline = Date.now() + 30 * 60 * 1000
  while (Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 3000))
    const runs = await unwrap(
      api.GET('/backup-policies/{policy_id}/runs', {
        params: { path: { policy_id: policyId }, query: { limit: 1 } },
      }),
    )
    const last = runs.items?.[0]
    if (last?.state === 'succeeded') return
    if (last?.state === 'failed')
      throw new ApiError(0, 'backup_failed', last.error ?? t('delete.backup_failed'))
  }
  throw new ApiError(0, 'backup_timeout', t('delete.backup_timeout'))
}

// REQ-DB-009 and REQ-TPL-008: delete asks for the name, offers a final backup of a database, and deletes volumes only on request.
export function ServiceDelete({
  service,
  environmentProtected,
  base,
}: {
  service: Service
  environmentProtected?: boolean
  base: Base
}) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const me = useMe()
  const access = accessFor(me.data?.org_role, 'service:delete', environmentProtected)
  const [open, setOpen] = useState(false)
  const [typed, setTyped] = useState('')
  const [volumes, setVolumes] = useState(false)
  const [backupFirst, setBackupFirst] = useState(true)
  const [group, setGroup] = useState(false)
  const services = useServices(service.environment_id)
  const policies = useQuery({
    queryKey: ['backup-policies', service.id],
    enabled: open && service.type === 'database',
    queryFn: () =>
      unwrap(
        api.GET('/services/{service_id}/backup-policies', {
          params: { path: { service_id: service.id ?? '' } },
        }),
      ),
  })
  const instanceId = service.template_instance_id
  const members = (services.data ?? []).filter(
    (s) => instanceId && s.template_instance_id === instanceId,
  )
  const policy = policies.data?.[0]
  const confirmText = group ? (service.template_slug ?? '') : (service.name ?? '')

  const remove = useMutation({
    mutationFn: async () => {
      if (backupFirst && policy?.id && !group) await finalBackup(policy.id)
      if (group && instanceId) {
        await unwrap(
          api.DELETE('/template-instances/{instance_id}', {
            params: {
              path: { instance_id: instanceId },
              query: { confirm: typed, delete_volumes: volumes },
            },
          }),
        )
      } else {
        await unwrap(
          api.DELETE('/services/{service_id}', {
            params: {
              path: { service_id: service.id ?? '' },
              query: { confirm: typed, delete_volumes: volumes },
            },
          }),
        )
      }
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['services'] })
      await navigate({ to: '/o/$org/p/$project/e/$env', params: base })
    },
  })

  if (access === 'hidden') return null
  return (
    <section className="mt-10 max-w-2xl space-y-3 border-t border-border pt-6">
      <h2 className="text-lg font-semibold text-danger">{t('delete.title')}</h2>
      <p className="text-sm text-muted">{t('delete.text')}</p>
      <Button
        variant="destructive"
        reason={access === 'blocked' ? t('common.needs_admin') : undefined}
        onClick={() => setOpen(true)}
      >
        {t('delete.button')}
      </Button>
      {open && (
        <Modal
          title={t('delete.dialog_title', { name: service.name ?? '' })}
          onClose={() => setOpen(false)}
        >
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault()
              if (typed === (group ? confirmText : service.name)) remove.mutate()
            }}
          >
            {members.length > 1 && (
              <CheckboxField
                label={t('delete.group', { count: String(members.length) })}
                hint={members.map((m) => m.name).join(', ')}
                checked={group}
                onChange={(e) => {
                  setGroup(e.target.checked)
                  setTyped('')
                }}
              />
            )}
            {service.type === 'database' && !group && (
              <>
                {policy ? (
                  <CheckboxField
                    label={t('delete.final_backup')}
                    hint={t('delete.final_backup_hint')}
                    checked={backupFirst}
                    onChange={(e) => setBackupFirst(e.target.checked)}
                  />
                ) : (
                  <Banner variant="warning">{t('delete.no_backup')}</Banner>
                )}
              </>
            )}
            <CheckboxField
              label={t('delete.volumes')}
              hint={t('delete.volumes_hint')}
              checked={volumes}
              onChange={(e) => setVolumes(e.target.checked)}
            />
            <TextField
              label={t('common.type_to_confirm', {
                name: group ? confirmText : (service.name ?? ''),
              })}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
            />
            {remove.error && <ErrorBanner error={remove.error} title={t('delete.failed')} />}
            <div className="flex flex-wrap justify-end gap-2">
              <Button onClick={() => setOpen(false)}>{t('common.cancel')}</Button>
              <Button
                type="submit"
                variant="destructive"
                loading={remove.isPending}
                reason={
                  typed === (group ? confirmText : service.name)
                    ? undefined
                    : t('common.type_first')
                }
              >
                {t('delete.confirm')}
              </Button>
            </div>
          </form>
        </Modal>
      )}
    </section>
  )
}
