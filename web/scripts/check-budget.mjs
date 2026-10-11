// SPDX-License-Identifier: Apache-2.0
// REQ-UI-029, REQ-UI-012, REQ-UI-015: the build gates of docs/spec/ui/quality.md section 3.
// Usage: node scripts/check-budget.mjs [dist folder]
// Fails when the build is over a budget, when index.html has an inline script, or when a built
// file names a third-party host.
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { gzipSync } from 'node:zlib'

const dist = resolve(process.argv[2] ?? 'dist-check')
const KB = 1024
const budget = { initialJs: 250, lazyJs: 150, css: 60, fonts: 120 }

const problems = []
const report = []
const html = readFileSync(join(dist, 'index.html'), 'utf8')
const assets = readdirSync(join(dist, 'assets'))
const gz = (file) => gzipSync(readFileSync(join(dist, 'assets', file))).length

// Initial JavaScript: the scripts and preloaded modules of index.html.
const initial = new Set(
  [...html.matchAll(/<(?:script|link)[^>]+(?:src|href)="\/assets\/([^"]+\.js)"/g)].map((m) => m[1]),
)
let initialBytes = 0
for (const file of initial) initialBytes += gz(file)
report.push(`initial js ${(initialBytes / KB).toFixed(1)} KB gzip (budget ${budget.initialJs})`)
if (initialBytes > budget.initialJs * KB) problems.push('initial JavaScript is over the budget')

// Lazy chunks: every other JavaScript file.
for (const file of assets.filter((f) => f.endsWith('.js') && !initial.has(f))) {
  const size = gz(file)
  report.push(`lazy ${file} ${(size / KB).toFixed(1)} KB gzip (budget ${budget.lazyJs})`)
  if (size > budget.lazyJs * KB) problems.push(`lazy chunk ${file} is over the budget`)
}

// CSS.
const css = assets.filter((f) => f.endsWith('.css'))
const cssBytes = css.reduce((sum, f) => sum + gz(f), 0)
report.push(`css ${(cssBytes / KB).toFixed(1)} KB gzip (budget ${budget.css})`)
if (cssBytes > budget.css * KB) problems.push('CSS is over the budget')

// Fonts: woff2 only, at most two families, total size, preloaded, font-display swap.
const fonts = assets.filter((f) => /\.(woff2?|ttf|otf)$/.test(f))
const fontBytes = fonts.reduce((sum, f) => sum + statSync(join(dist, 'assets', f)).size, 0)
const families = new Set(fonts.map((f) => f.split('-')[0]))
report.push(
  `fonts ${(fontBytes / KB).toFixed(1)} KB, ${families.size} families (budget ${budget.fonts})`,
)
if (fonts.some((f) => !f.endsWith('.woff2'))) problems.push('a font is not woff2')
if (families.size > 2) problems.push('more than two font families')
if (fontBytes > budget.fonts * KB) problems.push('fonts are over the budget')
const preloaded = [...html.matchAll(/rel="preload"[^>]+href="\/assets\/([^"]+\.woff2)"/g)].length
if (fonts.length > 0 && preloaded === 0) problems.push('no font is preloaded')
for (const f of css) {
  const text = readFileSync(join(dist, 'assets', f), 'utf8')
  const faces = text.match(/@font-face\{[^}]*\}/g) ?? []
  if (faces.some((face) => !face.includes('font-display:swap'))) {
    problems.push('a @font-face has no font-display: swap')
  }
}

// Hashed file names (immutable cache).
for (const f of assets) {
  if (!/-[A-Za-z0-9_-]{8}\.[a-z0-9]+$/.test(f)) problems.push(`asset without a hash: ${f}`)
}

// REQ-UI-015: no inline script or inline handler in index.html.
if (/<script(?![^>]*\bsrc=)[^>]*>/.test(html)) problems.push('index.html has an inline script')
if (/\son[a-z]+=/.test(html) || /\sstyle=/.test(html))
  problems.push('index.html has an inline handler or style')

// REQ-UI-012: no third-party host in any built file. XML namespaces are not requests.
// reactjs.org is in React's own error text and github.com is in a hint text. Neither is
// requested. The e2e test "no third-party host" watches the real requests.
const allowed = [
  'http://www.w3.org',
  'https://www.w3.org',
  'http://localhost',
  'https://reactjs.org',
  'https://github.com',
]
const isExample = (url) => /^https?:\/\/([a-z0-9-]+\.)*example\.(com|org)$/.test(url)
const files = [join(dist, 'index.html'), ...assets.map((f) => join(dist, 'assets', f))].filter(
  (f) => !/\.(woff2?|png|ico)$/.test(f),
)
for (const file of files) {
  const text = readFileSync(file, 'utf8')
  for (const m of text.matchAll(/https?:\/\/[A-Za-z0-9][A-Za-z0-9.-]*[A-Za-z0-9]/g)) {
    if (!allowed.includes(m[0]) && !isExample(m[0])) {
      problems.push(`${file.replace(dist, '')} names a host: ${m[0]}`)
    }
  }
}

console.log(report.join('\n'))
if (problems.length > 0) {
  console.error([...new Set(problems)].join('\n'))
  process.exit(1)
}
console.log('budget ok')
