// SPDX-License-Identifier: Apache-2.0

import { expect, test } from '@playwright/test'
import { defaultState, mockApi } from './mockApi'
import type { MockState } from './mockApi'

const releasesUrl = '/o/acme/p/shop/e/staging/s/api/releases'

function withPreviews(): MockState {
  const state = defaultState()
  state.previews = [
    {
      id: 'e-pr12',
      project_id: 'p1',
      name: 'pr-12',
      slug: 'pr-12',
      kind: 'preview',
      preview: {
        repo: 'acme/shop',
        pr_number: 12,
        state: 'active',
        expires_at: '2026-10-20T10:00:00Z',
        url: 'https://pr-12.preview.example.com',
      },
    },
    {
      id: 'e-pr7',
      project_id: 'p1',
      name: 'pr-7',
      slug: 'pr-7',
      kind: 'preview',
      preview: {
        repo: 'acme/shop',
        pr_number: 7,
        state: 'failed',
        expires_at: '2026-09-01T10:00:00Z',
        failure_reason: 'api: secret STRIPE_KEY is not marked for previews',
        waiting_for_label: true,
      },
    },
  ]
  return state
}

test('UI-18 UI-36 the previews list marks states, deletes with the typed name and opens a detail', async ({
  page,
}) => {
  const state = withPreviews()
  await mockApi(page, state)
  await page.goto('/o/acme/previews')

  await expect(page.getByRole('heading', { level: 1, name: 'Previews' })).toBeVisible()
  const table = page.getByRole('table', { name: 'Previews of the project' })
  const row12 = table.getByRole('row', { name: /acme\/shop #12/ })
  await expect(row12.getByText('Active')).toBeVisible()
  await expect(row12.getByRole('link', { name: 'https://pr-12.preview.example.com' })).toBeVisible()
  const row7 = table.getByRole('row', { name: /acme\/shop #7/ })
  await expect(row7.getByText('Failed')).toBeVisible()
  await expect(row7.getByText('Expired')).toBeVisible()

  // The preview environments are not tabs of the project page.
  await page.goto('/o/acme/p/shop/e/staging')
  await expect(page.getByRole('link', { name: 'pr-12' })).toHaveCount(0)

  await page.goto('/o/acme/previews')
  await table
    .getByRole('row', { name: /acme\/shop #12/ })
    .getByRole('link')
    .first()
    .click()
  await expect(page.getByRole('heading', { level: 1, name: 'Preview pr-12' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Open preview' })).toBeVisible()

  // Extend the expiry by 7 days: the new date shows.
  const expires = page.locator('dd', { has: page.locator('time') })
  const before = await expires.locator('time').first().getAttribute('datetime')
  await page
    .getByRole('group', { name: 'Extend expiry' })
    .getByRole('button', { name: '+7 days' })
    .click()
  await expect(expires.locator('time').first()).not.toHaveAttribute('datetime', before ?? '')
  expect(state.extendCalls).toEqual([7])

  await page.getByRole('link', { name: 'Previews', exact: true }).first().click()
  await table
    .getByRole('row', { name: /acme\/shop #12/ })
    .getByRole('button', { name: /Delete/ })
    .click()
  const dialog = page.getByRole('dialog', { name: 'Delete pr-12' })
  const confirm = dialog.getByRole('button', { name: 'Delete preview' })
  await expect(confirm).toHaveAttribute('aria-disabled', 'true')
  await dialog.getByLabel('Type pr-12 to confirm').fill('pr-12')
  await confirm.click()
  await expect(table.getByRole('row', { name: /acme\/shop #12/ })).toHaveCount(0)
  expect(state.deletedEnvironments).toEqual(['e-pr12'])
})

test('UI-36 a failed preview shows the reason and the label note', async ({ page }) => {
  await mockApi(page, withPreviews())
  await page.goto('/o/acme/p/shop/previews/pr-7')
  await expect(page.getByText('api: secret STRIPE_KEY is not marked for previews')).toBeVisible()
  await expect(page.getByText('A maintainer must add the label polipan-preview')).toBeVisible()
})

test('UI-18 an empty list explains the next step and a preview can be created by hand', async ({
  page,
}) => {
  const state = defaultState()
  await mockApi(page, state)
  await page.goto('/o/acme/previews')
  await expect(
    page.getByText('No previews. Open a pull request or create one by hand.'),
  ).toBeVisible()
  await page.getByRole('button', { name: 'Create preview' }).click()
  const dialog = page.getByRole('dialog', { name: 'Create a preview' })
  await dialog.getByLabel('Repository').fill('acme/shop')
  await dialog.getByLabel('Pull request number').fill('5')
  await dialog.getByRole('button', { name: 'Create preview' }).click()
  await expect(page.getByRole('table', { name: 'Previews of the project' })).toContainText(
    'acme/shop #5',
  )
  expect(state.createPreviewCalls).toEqual([{ repo: 'acme/shop', pr_number: 5 }])
})

test('UI-10 the promote dialog shows the diff, blocks on missing variables and handles plan_changed', async ({
  page,
}) => {
  const state = defaultState()
  state.promoteMissing = ['DATABASE_URL']
  await mockApi(page, state)
  await page.goto(releasesUrl)

  await page.getByRole('row', { name: /v2/ }).getByRole('button', { name: 'Promote' }).click()
  const dialog = page.getByRole('dialog', { name: 'Promote api v2' })
  await expect(dialog.getByLabel('Target environment')).toHaveValue('e2')

  // The diff table and the red list with the word "Missing", not only a color.
  const diff = dialog.getByRole('table', { name: 'Changes for api' })
  await expect(diff.getByRole('row', { name: /image_ref nginx:1.26 nginx:1.27/ })).toBeVisible()
  await expect(dialog.getByText('Missing variables', { exact: true })).toBeVisible()
  await expect(dialog.getByRole('listitem').filter({ hasText: 'DATABASE_URL' })).toBeVisible()

  const confirm = dialog.getByRole('button', { name: 'Promote to Production' })
  await expect(confirm).toHaveAttribute('aria-disabled', 'true')
  expect(state.promoteCalls).toHaveLength(0)

  // The user sets the variable in the target, then checks again.
  state.promoteMissing = []
  await dialog.getByRole('button', { name: 'Check again' }).click()
  await expect(dialog.getByText('Missing variables', { exact: true })).toHaveCount(0)

  // The target is protected: the typed name is needed.
  await expect(confirm).toHaveAttribute('aria-disabled', 'true')
  await dialog.getByLabel('Type Production to confirm').fill('Production')
  await expect(confirm).not.toHaveAttribute('aria-disabled', 'true')

  // The plan changes before the confirm: the server answers 409 and the UI reads the plan again.
  state.planChangesOnce = true
  await confirm.click()
  await expect(dialog.getByText('The plan changed.')).toBeVisible()
  expect(state.promoteCalls.map((c) => c.plan_hash)).toEqual(['ph1'])

  // The second confirm sends the hash of the new plan.
  await confirm.click()
  await expect(page).toHaveURL(/\/p\/shop\/e\/production\/s\/api\/deployments\/dp1$/)
  expect(state.promoteCalls.map((c) => c.plan_hash)).toEqual(['ph1', 'ph2'])
  expect(state.promoteCalls[1]).toMatchObject({ target_environment_id: 'e2', dry_run: false })
})

test('UI-10 a promote of a whole environment shows the result of each service', async ({
  page,
}) => {
  const state = defaultState('admin')
  await mockApi(page, state)
  await page.goto('/o/acme/p/shop/e/staging')
  await page.getByRole('button', { name: 'Promote all' }).click()
  const dialog = page.getByRole('dialog', { name: 'Promote Staging' })
  await dialog.getByLabel('Type Production to confirm').fill('Production')
  await dialog.getByRole('button', { name: 'Promote to Production' }).click()
  const result = page.getByRole('list', { name: 'Result of each service' })
  await expect(result).toContainText('api')
  await expect(result).toContainText('Started')
})
