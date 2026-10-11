// SPDX-License-Identifier: Apache-2.0
// REQ-UI-024 and REQ-UI-001: the parity matrix check.
//  1. Every row of the two tables in docs/spec/ui/parity.md has screen ids, and each id exists in
//     the index of docs/spec/ui/screens.md.
//  2. Every screen UI-01..UI-49 is named in parity.md, navigation.md or the supporting line.
//  3. Every screen has a stable URL: its route (or the route it opens on) is in src/router.tsx.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const read = (path) => readFileSync(fileURLToPath(new URL(path, import.meta.url)), 'utf8')
const screens = read('../../docs/spec/ui/screens.md')
const parity = read('../../docs/spec/ui/parity.md')
const navigation = read('../../docs/spec/ui/navigation.md')
const router = read('../src/router.tsx')
const servicePage = read('../src/routes/ServicePage.tsx')

const problems = []
const idsOf = (text) => [...text.matchAll(/UI-\d{2}/g)].map((m) => m[0])

// The index of screens.md is the table between "## 2. Index" and "## 3.".
const index = screens.slice(screens.indexOf('## 2. Index'), screens.indexOf('## 3.'))
const known = new Set(idsOf(index))
for (let n = 1; n <= 49; n++) {
  const id = `UI-${String(n).padStart(2, '0')}`
  if (!known.has(id)) problems.push(`${id} is not in the index of screens.md`)
}

// Rows of the first two tables of parity.md.
const tables = parity.slice(parity.indexOf('## 1.'), parity.indexOf('## 3.'))
let rows = 0
for (const line of tables.split('\n')) {
  if (!line.startsWith('|') || /^\|[-| ]+\|$/.test(line)) continue
  const cells = line.split('|').map((c) => c.trim())
  if (cells[1] === 'Easypanel feature' || cells[1] === 'Extra feature') continue
  rows++
  const ids = idsOf(cells[2] ?? '')
  if (ids.length === 0) problems.push(`parity row without a screen id: ${cells[1]}`)
  for (const id of ids)
    if (!known.has(id)) problems.push(`parity row "${cells[1]}": ${id} is unknown`)
}
if (rows < 30) problems.push(`only ${rows} parity rows were read`)

const mentioned = new Set([...idsOf(parity), ...idsOf(navigation)])
for (const id of known) {
  if (!mentioned.has(id)) problems.push(`${id} is in no row of parity.md and not in navigation.md`)
}

// One entry per screen: a fragment that must be in router.tsx or in the tab list of ServicePage.
// "overlay" screens open on top of a page, so they use the route of that page.
const routes = {
  'UI-01': "path: '/login'",
  'UI-02': "path: '/setup'",
  'UI-03': 'dashboardRoute',
  'UI-04': "path: 'p/$project'",
  'UI-05': "path: 'p/$project/e/$env/new'",
  'UI-06': "path: 'p/$project/e/$env/s/$service'",
  'UI-07': "path: 'p/$project/e/$env/s/$service'",
  'UI-08': 'deployments/$deployment',
  'UI-09': 'tab:releases',
  'UI-10': "path: 'p/$project/e/$env/s/$service'",
  'UI-11': 'tab:env',
  'UI-12': 'tab:domains',
  'UI-13': 'tab:volumes',
  'UI-14': 'tab:health',
  'UI-15': 'tab:logs',
  'UI-16': 'tab:metrics',
  'UI-17': "path: 'backups'",
  'UI-18': "path: 'previews'",
  'UI-19': "path: 'nodes'",
  'UI-20': "path: 'p/$project/e/$env/s/$service'",
  'UI-21': "path: 'catalog'",
  'UI-22': "path: 'alerts'",
  'UI-23': "path: 'settings/members'",
  'UI-24': "path: 'settings/sso'",
  'UI-25': "path: 'settings/tokens'",
  'UI-26': "path: 'settings/audit'",
  'UI-27': "path: 'settings/git'",
  'UI-28': "path: 'settings/organization'",
  'UI-29': "path: 'p/$project/config'",
  'UI-30': 'tab:terminal',
  'UI-31': 'dashboardRoute',
  'UI-32': "path: 'p/$project/e/$env/new'",
  'UI-33': "path: 'p/$project/e/$env'",
  'UI-34': "path: 'deployments'",
  'UI-35': 'releases/$release/diff/$other',
  'UI-36': "path: 'p/$project/previews/$env'",
  'UI-37': "path: 'logs'",
  'UI-38': "path: 'settings/backup-targets'",
  'UI-39': "path: 'backups'",
  'UI-40': "path: 'nodes/$node'",
  'UI-41': "path: 'nodes/history'",
  'UI-42': "path: '/o/$org'",
  'UI-43': "path: 'settings/profile'",
  'UI-44': "path: 'settings/teams'",
  'UI-45': "path: 'settings/members'",
  'UI-46': "path: '/o/$org'",
  'UI-47': 'notFoundComponent',
  'UI-48': "path: 'settings/alerts'",
  'UI-49': 'tab:env',
}
for (const id of known) {
  const fragment = routes[id]
  if (!fragment) {
    problems.push(`${id} has no route entry in scripts/check-parity.mjs`)
  } else if (fragment.startsWith('tab:')) {
    if (!servicePage.includes(`'${fragment.slice(4)}',`))
      problems.push(`${id}: no service tab ${fragment}`)
  } else if (!router.includes(fragment)) {
    problems.push(`${id}: no route with ${fragment} in router.tsx`)
  }
}

if (problems.length > 0) {
  console.error(problems.join('\n'))
  process.exit(1)
}
console.log(`parity ok: ${rows} rows, ${known.size} screens`)
