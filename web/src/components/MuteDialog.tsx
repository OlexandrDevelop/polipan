// SPDX-License-Identifier: Apache-2.0

import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { api } from '@/api/client'
import { unwrap } from '@/api/errors'
import { ErrorBanner } from '@/components/ErrorBanner'
import { Modal } from '@/components/Modal'
import { Button, SelectField, TextField } from '@/components/ui'
import { t } from '@/i18n'

const HOURS = [1, 4, 24, 72, 168]

// Mutes one rule for a time, with a reason (REQ-ALERT-006, audited). There is no acknowledge in
// v1, so Mute is the only way to quiet an alert (UI-22).
export function MuteDialog({
  ruleId,
  ruleName,
  onClose,
}: {
  ruleId: string
  ruleName: string
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const [hours, setHours] = useState('4')
  const [reason, setReason] = useState('')
  const mute = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST('/alert-rules/{rule_id}/mute', {
          params: { path: { rule_id: ruleId } },
          body: {
            until: new Date(Date.now() + Number(hours) * 3600_000).toISOString(),
            reason: reason.trim(),
          },
        }),
      ),
    onSuccess: async () => {
      await Promise.all(
        ['alert-rules', 'alert-instances'].map((key) =>
          queryClient.invalidateQueries({ queryKey: [key] }),
        ),
      )
      onClose()
    },
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    if (reason.trim()) mute.mutate()
  }
  return (
    <Modal title={t('mute.title', { name: ruleName })} onClose={onClose}>
      <form onSubmit={submit} className="space-y-4">
        <p className="text-sm text-muted">{t('mute.text')}</p>
        <SelectField label={t('mute.for')} value={hours} onChange={(e) => setHours(e.target.value)}>
          {HOURS.map((h) => (
            <option key={h} value={h}>
              {t(`mute.hours.${h as 1}`)}
            </option>
          ))}
        </SelectField>
        <TextField
          label={t('mute.reason')}
          required
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          autoComplete="off"
        />
        {mute.error && <ErrorBanner error={mute.error} title={t('mute.failed')} />}
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button
            type="submit"
            variant="primary"
            loading={mute.isPending}
            reason={reason.trim() ? undefined : t('mute.reason_first')}
          >
            {t('mute.submit')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}
