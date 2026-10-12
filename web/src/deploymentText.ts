// SPDX-License-Identifier: Apache-2.0

import { hasKey, t } from '@/i18n'

// The server writes some texts of a deployment in English. These are the ones we know, in the language
// of the user. Another text stays as the server wrote it.
export interface FailureDetail {
  text: string
  // The fix is a deploy key, so the page can link to the place where it is made.
  needsDeployKey: boolean
  // The fix is in the build settings (the builder or the folder of the app).
  needsBuildSettings: boolean
  // The text of the server when it is long. The page shows it behind a button.
  raw?: string
}

export function failureDetail(detail: string | undefined): FailureDetail | undefined {
  if (!detail) return undefined
  if (detail.includes('an ssh URL needs a deploy key')) {
    return { text: t('deployment.detail.ssh_key'), needsDeployKey: true, needsBuildSettings: false }
  }
  if (detail.includes('could not determine how to build the app')) {
    return {
      text: t('deployment.detail.no_app'),
      needsDeployKey: false,
      needsBuildSettings: true,
      raw: detail,
    }
  }
  if (detail.length > 240) {
    return {
      text: `${detail.slice(0, 200).trimEnd()}…`,
      needsDeployKey: false,
      needsBuildSettings: false,
      raw: detail,
    }
  }
  return { text: detail, needsDeployKey: false, needsBuildSettings: false }
}

// The message of a step of a deployment.
export function stepMessage(message: string | undefined): string | undefined {
  if (!message) return undefined
  if (message === 'deployment created') return t('deployment.msg.created')
  if (message === 'building the image') return t('deployment.msg.building')
  const prefix = 'build_failed: '
  if (message.startsWith(prefix)) return failureDetail(message.slice(prefix.length))?.text
  return message
}

// A warning of a deployment: our text for its code, else the server text.
export function warningMessage(w: { code?: string; message?: string }): string {
  const key = `plan.warning.${w.code ?? ''}`
  return hasKey(key) ? t(key) : (w.message ?? w.code ?? '')
}
