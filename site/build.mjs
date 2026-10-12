// SPDX-License-Identifier: Apache-2.0
// Builds the static site into dist/. No packages needed (Node 20).
// Usage: node build.mjs
import { createHash } from 'node:crypto'
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(fileURLToPath(import.meta.url))
const out = join(root, 'dist')
const config = JSON.parse(readFileSync(join(root, 'site.config.json'), 'utf8'))
const base = `https://${config.domain}`
const repoUrl = `https://github.com/${config.repo}`

const esc = (s) =>
  String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c])

rmSync(out, { recursive: true, force: true })
mkdirSync(join(out, 'assets'), { recursive: true })
mkdirSync(join(out, '.well-known'), { recursive: true })

// CSS and JS get a hash in the name, so they can be cached for a long time.
function asset(name) {
  const data = readFileSync(join(root, 'assets', name))
  const hash = createHash('sha256').update(data).digest('hex').slice(0, 10)
  const dot = name.lastIndexOf('.')
  const hashed = `${name.slice(0, dot)}.${hash}${name.slice(dot)}`
  writeFileSync(join(out, 'assets', hashed), data)
  return `/assets/${hashed}`
}
for (const f of ['jetbrains-mono-latin-wght-normal.woff2', 'JetBrainsMono-OFL.txt', 'favicon.svg']) {
  cpSync(join(root, 'assets', f), join(out, 'assets', f))
}
// The font path in the CSS stays plain, so the CSS is copied with its hash only.
const files = {
  css: asset('site.css'),
  js: asset('site.js'),
  themeInit: asset('theme-init.js'),
}

const messages = Object.fromEntries(
  config.languages.map((l) => [l.code, JSON.parse(readFileSync(join(root, 'i18n', `${l.code}.json`), 'utf8'))]),
)
const en = messages.en
for (const [code, data] of Object.entries(messages)) {
  for (const key of Object.keys(en)) if (!(key in data)) throw new Error(`${code}: missing ${key}`)
  for (const key of Object.keys(data)) if (!(key in en)) throw new Error(`${code}: extra ${key}`)
}

const path = (code) => (code === 'en' ? '/' : `/${code}/`)

function head(t, code, title, description, canonical, withAlternates) {
  const alternates = withAlternates
    ? config.languages
        .map((l) => `<link rel="alternate" hreflang="${l.code}" href="${base}${path(l.code)}">`)
        .join('\n    ') + `\n    <link rel="alternate" hreflang="x-default" href="${base}/">`
    : ''
  return `<!doctype html>
<html lang="${code}">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>${esc(title)}</title>
    <meta name="description" content="${esc(description)}">
    <meta name="color-scheme" content="dark light">
    <meta property="og:type" content="website">
    <meta property="og:title" content="${esc(title)}">
    <meta property="og:description" content="${esc(description)}">
    <meta property="og:url" content="${base}${canonical}">
    <link rel="canonical" href="${base}${canonical}">
    ${alternates}
    <link rel="icon" href="/assets/favicon.svg" type="image/svg+xml">
    <link rel="preload" href="/assets/jetbrains-mono-latin-wght-normal.woff2" as="font" type="font/woff2" crossorigin>
    <link rel="stylesheet" href="${files.css}">
    <script src="${files.themeInit}"></script>
  </head>`
}

function topBar(t) {
  return `<header class="bar bar-top">
      <a class="brand" href="${path(t.code)}" aria-label="Polipan"><span aria-hidden="true">$ </span>polipan</a>
      <nav aria-label="${esc(t['nav.label'])}">
        <a href="${config.docsUrl}">${esc(t['nav.docs'])}</a>
        <a href="${config.communityUrl}">${esc(t['nav.community'])}</a>
        <a href="${repoUrl}">${esc(t['nav.github'])}</a>
      </nav>
    </header>`
}

function bottomBar(t, current) {
  const langs = config.languages
    .map(
      (l) =>
        `<a href="${path(l.code)}" lang="${l.code}" hreflang="${l.code}"${l.code === current ? ' aria-current="true"' : ''} title="${esc(l.name)}">[${l.code}]</a>`,
    )
    .join('')
  return `<footer class="bar bar-bottom">
      <div class="group">
        <a href="${repoUrl}/blob/main/LICENSE">${esc(t['footer.license'])}: Apache-2.0</a>
        <a href="${repoUrl}/security/policy">${esc(t['footer.security'])}</a>
      </div>
      <div class="group">
        <nav class="langs" aria-label="${esc(t['footer.lang'])}">${langs}</nav>
        <button type="button" data-theme-toggle data-word="${esc(t['footer.theme'])}" hidden></button>
      </div>
    </footer>`
}

function home(code) {
  const t = { ...messages[code], code }
  const features = ['deploy', 'safe', 'https', 'vars', 'watch', 'nodes', 'previews', 'team', 'lang']
    .map(
      (k) =>
        `<article class="panel"><h3 class="panel-title">${esc(t[`f.${k}.t`])}</h3><p>${esc(t[`f.${k}.d`])}</p></article>`,
    )
    .join('\n        ')
  const steps = [1, 2, 3]
    .map(
      (n) =>
        `<li class="panel"><h3 class="panel-title">${esc(t[`how.${n}.t`])}</h3><p>${esc(t[`how.${n}.d`])}</p></li>`,
    )
    .join('\n        ')
  const reqs = [1, 2, 3, 4].map((n) => `<li>${esc(t[`req.${n}`])}</li>`).join('\n          ')
  const sample = `<span class="ok">$</span> ${esc(config.installCommand)}
==> Checking the files of the release
==> Installing Docker
==> Starting the server, the registry and BuildKit
==> Polipan is running.
Open this one-time link and create the first user:
  https://panel.example.com/setup?token=…`
  return `${head(t, code, t['meta.title'], t['meta.description'], path(code), true)}
  <body>
    <a class="skip" href="#main">${esc(t.skip)}</a>
    ${topBar(t)}
    <main id="main" tabindex="-1">
      <section aria-labelledby="hero">
        <p class="kicker">${esc(t['hero.kicker'])}</p>
        <h1 id="hero">${esc(t['hero.title'])}<span class="cursor" aria-hidden="true"></span></h1>
        <p class="lead">${esc(t['hero.text'])}</p>
        <h2 class="section-title">${esc(t['hero.install'])}</h2>
        <div class="install">
          <pre tabindex="0"><code id="cmd"><span class="prompt">$ </span>${esc(config.installCommand)}</code></pre>
          <button type="button" class="btn" data-copy="#cmd" data-done="${esc(t.copied)}" aria-label="${esc(t['copy.label'])}" hidden>${esc(t.copy)}</button>
        </div>
        <p class="note">${esc(t['hero.note'])}</p>
      </section>
      <section aria-labelledby="features">
        <h2 class="section-title" id="features">${esc(t['features.title'])}</h2>
        <div class="grid">
        ${features}
        </div>
      </section>
      <section aria-labelledby="how">
        <h2 class="section-title" id="how">${esc(t['how.title'])}</h2>
        <ol class="grid steps">
        ${steps}
        </ol>
      </section>
      <section aria-labelledby="example">
        <h2 class="section-title" id="example">${esc(t['example.title'])}</h2>
        <pre class="term" tabindex="0"><code>${sample}</code></pre>
      </section>
      <section aria-labelledby="req">
        <div class="panel"><h2 class="panel-title" id="req">${esc(t['req.title'])}</h2>
          <ul class="reqs">
          ${reqs}
          </ul>
        </div>
      </section>
      <section aria-labelledby="open">
        <h2 class="section-title" id="open">${esc(t['open.title'])}</h2>
        <p class="lead">${esc(t['open.text'])}</p>
        <div class="links">
          <a href="${config.docsUrl}">${esc(t['nav.docs'])}</a>
          <a href="${config.communityUrl}">${esc(t['nav.community'])}</a>
          <a href="${repoUrl}">${esc(t['nav.github'])}</a>
        </div>
      </section>
    </main>
    ${bottomBar(t, code)}
    <script src="${files.js}" defer></script>
  </body>
</html>
`
}

for (const l of config.languages) {
  const dir = l.code === 'en' ? out : join(out, l.code)
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'index.html'), home(l.code))
}

const t404 = { ...en, code: 'en' }
writeFileSync(
  join(out, '404.html'),
  `${head(t404, 'en', `${en['404.title']} | Polipan`, en['404.text'], '/404.html', false)}
  <body>
    <a class="skip" href="#main">${esc(en.skip)}</a>
    ${topBar(t404)}
    <main id="main" tabindex="-1">
      <p class="kicker">404</p>
      <h1>${esc(en['404.title'])}</h1>
      <p class="lead">${esc(en['404.text'])}</p>
      <div class="links"><a href="/">${esc(en['404.home'])}</a></div>
    </main>
    ${bottomBar(t404, 'en')}
    <script src="${files.js}" defer></script>
  </body>
</html>
`,
)

writeFileSync(join(out, 'robots.txt'), `User-agent: *\nAllow: /\nSitemap: ${base}/sitemap.xml\n`)
writeFileSync(
  join(out, 'sitemap.xml'),
  `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${config.languages
    .map((l) => `  <url><loc>${base}${path(l.code)}</loc></url>`)
    .join('\n')}\n</urlset>\n`,
)
writeFileSync(
  join(out, '.well-known', 'security.txt'),
  `Contact: ${config.securityContact}\nPolicy: ${repoUrl}/security/policy\nPreferred-Languages: en, uk\nCanonical: ${base}/.well-known/security.txt\nExpires: 2027-10-12T00:00:00.000Z\n`,
)
console.log(`site ok: ${config.languages.length} languages in ${out}`)
