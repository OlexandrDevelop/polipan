// SPDX-License-Identifier: Apache-2.0
// Every language file has exactly the keys of the English file and the same {placeholders}.
// Usage: node scripts/check-locales.mjs
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const dir = fileURLToPath(new URL('../src/locales/', import.meta.url))
const load = (code) => JSON.parse(readFileSync(`${dir}${code}/translation.json`, 'utf8'))
const en = load('en')
const holes = (text) => [...String(text).matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(',')

const problems = []
for (const code of readdirSync(dir).filter((c) => c !== 'en')) {
  const data = load(code)
  for (const key of Object.keys(en)) {
    if (!(key in data)) problems.push(`${code}: missing ${key}`)
    else if (typeof data[key] !== 'string' || data[key].trim() === '')
      problems.push(`${code}: empty ${key}`)
    else if (holes(data[key]) !== holes(en[key]))
      problems.push(`${code}: placeholders differ in ${key}`)
  }
  for (const key of Object.keys(data)) if (!(key in en)) problems.push(`${code}: extra ${key}`)
}
if (problems.length > 0) {
  console.error(problems.slice(0, 200).join('\n'))
  console.error(`${problems.length} problems`)
  process.exit(1)
}
console.log(`locales ok: ${readdirSync(dir).length} languages, ${Object.keys(en).length} keys`)
