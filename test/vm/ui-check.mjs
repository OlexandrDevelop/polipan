// SPDX-License-Identifier: Apache-2.0
// Checks the terminal-style web app of the panel in a headless browser: login, dashboard, deploy view, both themes,
// phone width. It needs a panel with a user and a service that was deployed once (verify.py makes them).
//
//   node test/vm/ui-check.mjs            (use test/vm/vm.sh verify-ui, it sets the variables)
//
// Variables: UI_URL (http://127.0.0.1:8088), UI_EMAIL, UI_PASSWORD, UI_OUT (folder for the screenshots).
import { createRequire } from 'node:module'
import { mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
// Playwright is a dev dependency of the web app.
const require = createRequire(resolve(here, '../../web/package.json'))
const { chromium } = require('@playwright/test')

const base = process.env.UI_URL || 'http://127.0.0.1:8088'
const email = process.env.UI_EMAIL || 'owner@example.com'
const password = process.env.UI_PASSWORD || 'correct horse battery'
const out = process.env.UI_OUT || '/tmp/polipan-ui'
mkdirSync(out, { recursive: true })

let failed = 0
function check(name, ok, detail = '') {
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ': ' + detail : ''}`)
  if (!ok) failed++
}

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || undefined })
const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, locale: 'en-US', timezoneId: 'UTC' })
const page = await context.newPage()
const problems = []
page.on('pageerror', (e) => problems.push('page error: ' + e.message))
// The login page asks /me before the user signs in: its 401 is normal.
page.on('console', (m) => {
  if (m.type() === 'error' && !/status of 401/.test(m.text())) problems.push('console: ' + m.text())
})
page.on('response', (r) => {
  const bad = r.status() >= 500 || (r.status() === 401 && !/\/api\/v1\/(me|auth\/)/.test(r.url()))
  if (r.status() >= 400) console.log('  (info) HTTP', r.status(), r.request().method(), r.url())
  if (bad) problems.push(`HTTP ${r.status()} ${r.url()}`)
})

// ---- login ----
await page.goto(base + '/login')
await page.getByLabel('Email').fill(email)
await page.getByLabel('Password').fill(password)
await page.screenshot({ path: `${out}/01-login.png` })
await page.getByRole('button', { name: 'Sign in' }).click()
await page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 20000 })
check('login: the user signs in and leaves the login page', true, new URL(page.url()).pathname)

// ---- dashboard ----
await page.waitForLoadState('networkidle')
await page.screenshot({ path: `${out}/02-dashboard.png` })
const body = await page.locator('body').innerText()
check('dashboard: the page shows content and the project', body.includes('VM check'), body.slice(0, 80).replace(/\s+/g, ' '))
const font = await page.evaluate(() => getComputedStyle(document.body).fontFamily)
check('terminal style: the font is monospace', /mono|courier|consol/i.test(font), font)

// ---- deploy view: the service page and the page of a deployment ----
const api = async (path) => (await page.request.get(base + '/api/v1' + path)).json()
const projects = await api('/projects')
const project = projects.items.find((p) => p.name === 'VM check') || projects.items[0]
const envs = await api(`/projects/${project.id}/environments`)
const env = envs.find((e) => e.kind === 'production')
const services = await api(`/environments/${env.id}/services`)
const svc = services.find((s) => s.name === 'zd') || services[0]
const deps = await api(`/services/${svc.id}/deployments`)
const dep = deps.items[0]
const slug = (o) => o.slug || o.name || o.id
const org = new URL(page.url()).pathname.match(/^\/o\/([^/]+)/)?.[1] || 'default'
const servicePath = `/o/${org}/p/${slug(project)}/e/${slug(env)}/s/${slug(svc)}`
await page.goto(base + servicePath)
await page.waitForLoadState('networkidle')
const svcText = await page.locator('body').innerText()
check('service page: shows the service name and its state', svcText.includes(svc.name) && /live|running/i.test(svcText), svcText.slice(0, 100).replace(/\s+/g, ' '))
await page.screenshot({ path: `${out}/03-service.png` })
await page.goto(`${base}${servicePath}/deployments/${dep.id}`)
await page.waitForLoadState('networkidle')
const depText = await page.locator('body').innerText()
check('deploy view: the page of a deployment shows its steps', /live/i.test(depText) && /(start|switch|health)/i.test(depText), depText.slice(0, 120).replace(/\s+/g, ' '))
await page.screenshot({ path: `${out}/04-deploy.png` })

// ---- both themes ----
const colors = {}
for (const theme of ['light', 'dark']) {
  await page.evaluate((t) => localStorage.setItem('polipan.theme', t), theme)
  await page.reload()
  await page.waitForLoadState('networkidle')
  const attr = await page.evaluate(() => document.documentElement.getAttribute('data-theme'))
  colors[theme] = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  await page.screenshot({ path: `${out}/05-theme-${theme}.png` })
  check(`theme ${theme}: data-theme is ${theme}`, attr === theme, `background ${colors[theme]}`)
}
check('themes: light and dark look different', colors.light !== colors.dark)

// ---- phone width ----
await page.setViewportSize({ width: 390, height: 844 })
for (const [name, path] of [['dashboard', `/o/${org}`], ['deploy', `${servicePath}/deployments/${dep.id}`]]) {
  await page.goto(base + path)
  await page.waitForLoadState('networkidle')
  const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  await page.screenshot({ path: `${out}/06-phone-${name}.png` })
  check(`phone width 390 px: ${name} has no sideways scroll`, wide <= 1, `overflow ${wide} px`)
}

check('browser: no page error, no console error, no HTTP 5xx', problems.length === 0, problems.slice(0, 3).join(' | '))
await browser.close()
console.log(`screenshots in ${out}`)
process.exit(failed ? 1 : 0)
