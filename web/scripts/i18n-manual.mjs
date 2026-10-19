// Translates the reference manual, unit by unit (a title, a paragraph, a list or a table), into the console's languages.
//
//   go run ./cmd/perfusedoc -units docs/manual/i18n/units.json
//   OPENROUTER_API_KEY=... node web/scripts/i18n-manual.mjs de fr ...
//
// Each language's translations are kept in docs/manual/i18n/<lang>.json, English unit to translation, so a rerun only
// translates what is new or changed. A translation is kept only when it keeps the unit's shape: the same code spans in
// backquotes, the same link targets, the same number of lines in a list or table and the same columns in a table. Anything
// else stays English in the translated manual, which says it is a machine translation and points at the original.
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { LANGUAGES } from './i18n.mjs'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const dir = path.join(root, 'docs', 'manual', 'i18n')

const codeSpans = (s) => (s.match(/`[^`]+`/g) ?? []).sort().join('\u0000')
const links = (s) => (s.match(/\]\([^)]+\)/g) ?? []).sort().join('\u0000')
// shape is a unit's structure: its list items and table rows (with their columns), in order. Lines that only continue a
// paragraph or an item are not counted, since a translation wraps differently.
const shape = (s) =>
  s
    .split('\n')
    .map((l) => (l.trim().startsWith('|') ? `|${(l.match(/\|/g) ?? []).length}` : /^\s*([-*]|\d+\.)\s/.test(l) ? 'li' : ''))
    .filter(Boolean)
    .join(',')
export function keepsShape(en, t) {
  return codeSpans(en) === codeSpans(t) && links(en) === links(t) && shape(en) === shape(t)
}

const rules = `You translate a technical reference manual for Perfuse, a healthcare integration engine, from English into LANG.
The text is Markdown. Keep exactly as they are: everything inside backquotes, link targets in (parentheses), **bold** and
*italic* markers, table pipes and the number of cells in each table row, the number of lines and list items, and these names:
Perfuse, HL7, FHIR, MLLP, DICOM, X12, EDIFACT, AS2, OpenPGP, CDA, SMART, OAuth, SAML, SCIM, LDAP, TLS, SFTP, S3, Kafka, SNMP,
PostgreSQL, SQLite, Mirth, LOINC, SNOMED CT, ICD-10, ICD-11, GDPR, NHS, AWS, Azure, YAML, JSON and other product, protocol,
file and command names. Translate the words around them, in the register of professional technical documentation, using the
formal form of address where the language has one, and the same term for the same thing throughout (channel, source,
destination, queue, tenant, message store). Answer with a JSON object only: each key is the number given, each value the
translation of that unit.`

async function batch(lang, units, key) {
  const body = {
    model: process.env.I18N_MODEL || 'meta-llama/llama-3.3-70b-instruct',
    temperature: 0,
    // The fastest of the providers serving the model: the work is many independent requests, so throughput is what matters.
    provider: { sort: 'throughput' },
    response_format: { type: 'json_object' },
    messages: [
      { role: 'system', content: rules.replace('LANG', `${LANGUAGES[lang]} (${lang})`) },
      { role: 'user', content: JSON.stringify(Object.fromEntries(units.map((u, i) => [String(i), u]))) },
    ],
  }
  for (let attempt = 0; attempt < 4; attempt++) {
    try {
      const res = await fetch('https://openrouter.ai/api/v1/chat/completions', {
        method: 'POST',
        headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json', 'X-Title': 'Perfuse manual translation' },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(300000),
      })
      if (!res.ok) throw new Error(`OpenRouter ${res.status}: ${(await res.text()).slice(0, 160)}`)
      const j = await res.json()
      let txt = (j.choices?.[0]?.message?.content ?? '').replace(/^```(?:json)?\s*|\s*```$/g, '')
      const obj = JSON.parse(txt.slice(txt.indexOf('{'), txt.lastIndexOf('}') + 1))
      const out = {}
      units.forEach((u, i) => {
        const t = obj[String(i)]
        if (typeof t === 'string' && t.trim() && keepsShape(u, t)) out[u] = t.trim()
      })
      return out
    } catch (e) {
      if (attempt === 3) {
        console.error(`  ${lang}: a batch failed: ${e.message}`)
        return {}
      }
      await new Promise((r) => setTimeout(r, 3000 * (attempt + 1)))
    }
  }
  return {}
}

async function translate(lang, key) {
  const units = JSON.parse(fs.readFileSync(path.join(dir, 'units.json'), 'utf8'))
  const file = path.join(dir, `${lang}.json`)
  let have = {}
  try {
    have = JSON.parse(fs.readFileSync(file, 'utf8'))
  } catch {
    // none yet
  }
  const save = () => {
    const keep = Object.fromEntries(units.filter((u) => have[u]).map((u) => [u, have[u]]))
    fs.writeFileSync(file, JSON.stringify(keep, null, 1) + '\n')
  }
  const parallel = Number(process.env.I18N_PARALLEL || 4)
  for (let round = 0; round < 3; round++) {
    const missing = units.filter((u) => !have[u])
    if (!missing.length) break
    // Batches of about 900 words, or one long unit on its own; the third round tries each unit alone.
    const batches = []
    let cur = [], words = 0
    for (const u of missing) {
      const w = u.split(/\s+/).length
      if (cur.length && (words + w > (round === 2 ? 0 : 900) || cur.length >= 25)) {
        batches.push(cur)
        cur = []
        words = 0
      }
      cur.push(u)
      words += w
    }
    if (cur.length) batches.push(cur)
    console.log(`${lang}: ${missing.length} units in ${batches.length} batches (round ${round + 1})`)
    let next = 0, done = 0
    const worker = async () => {
      while (next < batches.length) {
        const b = batches[next++]
        Object.assign(have, await batch(lang, b, key))
        if (++done % 10 === 0) {
          save()
          console.log(`  ${lang}: ${done}/${batches.length}`)
        }
      }
    }
    await Promise.all(Array.from({ length: parallel }, worker))
    save()
  }
  console.log(`${lang}: done, ${units.filter((u) => !have[u]).length} of ${units.length} left in English`)
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  const key = process.env.OPENROUTER_API_KEY
  if (!key) {
    console.error('OPENROUTER_API_KEY is not set')
    process.exit(2)
  }
  const langs = process.argv.slice(2).length ? process.argv.slice(2) : Object.keys(LANGUAGES)
  for (const l of langs) await translate(l, key)
}
