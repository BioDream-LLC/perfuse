// The console's message catalogue: every piece of interface text, read out of the source with the TypeScript compiler.
//
//   node scripts/i18n.mjs extract           writes src/i18n/catalog/en.json (the English source of every translation)
//   node scripts/i18n.mjs translate de fr   fills the missing entries of src/i18n/catalog/<lang>.json (needs OPENROUTER_API_KEY)
//   node scripts/i18n.mjs check             fails when en.json is out of date or a language lacks an entry
//
// English stays in the components: the catalogue is keyed by the English text itself, and the console swaps text for its
// translation as it is drawn (src/i18n/runtime.ts). So a new screen needs nothing but a rerun of extract and translate, and
// the check (also a unit test) fails until it has had them.
//
// What counts as interface text: JSX text; string and template literals in attributes people read (title, placeholder,
// aria-label, label, hint, description, about ...); and literals that read as prose anywhere else (a status message, an
// option's label in a table of options). What does not: class names, ids, paths and URLs, values compared against, imports,
// object keys. A template literal becomes a pattern whose ${...} parts are {0}, {1} ...
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

const here = path.dirname(fileURLToPath(import.meta.url))
const root = path.resolve(here, '..')
const srcDir = path.join(root, 'src')
const catDir = path.join(srcDir, 'i18n', 'catalog')

/** The languages the console is translated into, with their names in their own language. English is the source. */
export const LANGUAGES = {
  de: 'Deutsch',
  fr: 'Français',
  nl: 'Nederlands',
  es: 'Español',
  it: 'Italiano',
  pt: 'Português (Portugal)',
  pl: 'Polski',
  sv: 'Svenska',
  da: 'Dansk',
  nb: 'Norsk bokmål',
  fi: 'Suomi',
}

const readAttrs = new Set(['title', 'placeholder', 'aria-label', 'alt', 'label', 'hint', 'description', 'about', 'help', 'summary',
  'message', 'emptyText', 'confirmLabel', 'cancelLabel', 'heading', 'subtitle', 'caption', 'tooltip', 'text', 'detail', 'note'])
const skipAttrs = new Set(['className', 'key', 'id', 'type', 'href', 'src', 'data-testid', 'role', 'name', 'value', 'aria-hidden',
  'target', 'rel', 'autoComplete', 'inputMode', 'method', 'htmlFor', 'accept', 'pattern', 'lang', 'dir', 'download', 'form',
  'aria-describedby', 'aria-controls', 'aria-labelledby', 'aria-live', 'aria-current', 'aria-expanded', 'aria-selected',
  'language', 'mode', 'size', 'variant', 'tone', 'icon', 'kind', 'path', 'viewBox', 'd', 'fill', 'stroke', 'strokeWidth',
  'strokeLinecap', 'strokeLinejoin', 'transform', 'xmlns', 'gradientUnits', 'stopColor', 'offset', 'x1', 'x2', 'y1', 'y2', 'cx', 'cy', 'r'])
const skipCalls = new Set(['request', 'fetch', 'require', 'import', 'querySelector', 'querySelectorAll', 'getElementById', 'getItem',
  'setItem', 'removeItem', 'getByTestId', 'addEventListener', 'removeEventListener', 'createElement', 'setAttribute', 'getAttribute',
  'matchMedia', 'split', 'join', 'replace', 'replaceAll', 'startsWith', 'endsWith', 'includes', 'indexOf', 'test', 'match', 'RegExp',
  'URLSearchParams', 'set', 'get', 'has', 'delete', 'append', 'toLocaleDateString', 'toLocaleTimeString', 'toLocaleString',
  'DisplayNames', 'NumberFormat', 'DateTimeFormat', 'encodeURIComponent', 'atLeast', 'localeCompare', 'padStart', 'padEnd',
  'parse', 'stringify', 'log', 'warn', 'error', 'debug', 'info', 'assert', 'dispatchEvent', 'Event', 'CustomEvent', 'Blob', 'File'])

/** classLike is a string of Tailwind classes or other machine words: lower case, joined by dashes, colons and slashes. */
function classLike(s) {
  const toks = s.trim().split(/\s+/)
  return toks.every((t) => /^[a-z0-9:/[\]().!%#_&>+*=@,-]+$/.test(t)) && toks.some((t) => /[-:/[\]]/.test(t))
}

/** prose reads as words a person reads, not as an identifier, a path or a format string. */
function prose(s) {
  const t = s.trim()
  if (t.length < 2 || !/[A-Za-z]{2}/.test(t)) return false
  if (classLike(t)) return false
  if (/^(https?:|mailto:|urn:|\/|#\/|\.\/|\.\.\/|data:|blob:)/.test(t)) return false
  if (/^[\w.$-]+$/.test(t) && !/^[A-Z][a-z]+(?:[A-Z][a-z]+)*$/.test(t) && !/^[A-Z][a-z]*$/.test(t)) return false // ids, keys, camelCase
  if (/^[A-Z0-9_]+$/.test(t)) return false // CONSTANTS, codes
  if (/^[a-z]+(\.[a-z]+)+$/.test(t)) return false // dotted keys
  if (/^\{.*\}$|^\[.*\]$/.test(t)) return false // JSON
  if (/^[^\s]*[=<>{}|\\]{2,}/.test(t)) return false
  if (/^[a-z]/.test(t) && !/\s/.test(t)) return false // a single lower-case word: a value, not a label
  return true
}

/** codeLike is a sample of code, a message or a name, which is the same in every language. */
function codeLike(s) {
  const t = s.trim()
  if (/\$\{|\|\^~|^[A-Z]{2,3}[|*]|[*~]\d|^\.[a-z0-9]+$|^--?[a-z]/.test(t)) return true // ${VAR}, HL7, X12, .ext, -flag
  if (!/\s/.test(t) && /[.$*~|^\\{}<>=/_@:]/.test(t) && !/^[A-Z][a-z]+[.:]?$/.test(t)) return true // one token with symbols
  if (/^[\d\s.,:%+\-–—/()]+[a-zA-Z]{0,3}$/.test(t)) return true // 60s, 5m, 10 %, 2025-01
  return false
}

function collapse(s) {
  return s.replace(/\s+/g, ' ').trim()
}

function attrName(node) {
  return node.name.getText()
}

function propertyName(node) {
  return node.name && (ts.isIdentifier(node.name) || ts.isStringLiteral(node.name)) ? node.name.text : ''
}

/** callee is the name a call is made through: request, api.foo, setError. */
function callee(call) {
  const e = call.expression
  if (ts.isIdentifier(e)) return e.text
  if (ts.isPropertyAccessExpression(e)) return e.name.text
  if (ts.isNewExpression?.(call)) return ''
  return ''
}

export function extract() {
  const files = fs
    .readdirSync(srcDir, { recursive: true })
    .filter((f) => /\.tsx?$/.test(f) && !/\.test\.|\.d\.ts$|^i18n[/\\]/.test(f))
    .sort()
  const strings = new Map() // text -> first place seen
  const patterns = new Map()
  const add = (map, text, where) => {
    if (!map.has(text)) map.set(text, where)
  }
  for (const rel of files) {
    const file = path.join(srcDir, rel)
    const src = ts.createSourceFile(file, fs.readFileSync(file, 'utf8'), ts.ScriptTarget.Latest, true,
      file.endsWith('x') ? ts.ScriptKind.TSX : ts.ScriptKind.TS)
    const where = (n) => `${rel}:${src.getLineAndCharacterOfPosition(n.getStart()).line + 1}`
    // reason says whether a literal at this position is interface text: 'yes', 'no', or 'prose' (if it reads as prose).
    const reason = (n) => {
      let p = n.parent
      while (p && (ts.isParenthesizedExpression(p) || ts.isConditionalExpression(p) || ts.isBinaryExpression(p) && [ts.SyntaxKind.BarBarToken, ts.SyntaxKind.QuestionQuestionToken, ts.SyntaxKind.AmpersandAmpersandToken, ts.SyntaxKind.PlusToken].includes(p.operatorToken.kind) || ts.isJsxExpression(p) || ts.isAsExpression(p) || ts.isArrayLiteralExpression(p) || ts.isSatisfiesExpression?.(p))) {
        if (ts.isConditionalExpression(p) && p.condition === n) return 'no'
        n = p
        p = p.parent
      }
      if (!p) return 'prose'
      if (ts.isImportDeclaration(p) || ts.isExportDeclaration(p) || ts.isImportTypeNode?.(p) || ts.isExternalModuleReference(p)) return 'no'
      if (ts.isLiteralTypeNode(p)) return 'no'
      if (ts.isElementAccessExpression(p) && p.argumentExpression === n) return 'no'
      if (ts.isCaseClause(p)) return 'no'
      if (ts.isBinaryExpression(p) && [ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken, ts.SyntaxKind.EqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsToken, ts.SyntaxKind.InKeyword].includes(p.operatorToken.kind)) return 'no'
      if (ts.isPropertyAssignment(p) && p.name === n) return 'no'
      if (ts.isJsxAttribute(p)) {
        const a = attrName(p)
        if (readAttrs.has(a)) return 'yes'
        if (skipAttrs.has(a) || a.startsWith('data-') || a.startsWith('on')) return 'no'
        return 'prose'
      }
      if (ts.isPropertyAssignment(p)) {
        const k = propertyName(p)
        if (readAttrs.has(k)) return 'yes'
        if (['className', 'id', 'key', 'path', 'url', 'href', 'type', 'kind', 'value', 'format', 'system', 'code', 'mode', 'method', 'tab', 'icon', 'tone', 'accent', 'text-color', 'colour', 'color', 'bg', 'ring', 'dot', 'activeBg', 'border', 'chip', 'badge', 'cls', 'class'].includes(k)) return 'no'
        return 'prose'
      }
      if (ts.isCallExpression(p) || ts.isNewExpression(p)) {
        const c = ts.isCallExpression(p) ? callee(p) : (ts.isIdentifier(p.expression) ? p.expression.text : '')
        if (skipCalls.has(c)) return 'no'
        if (['setError', 'setNote', 'setSaid', 'setMessage', 'setStatus', 'confirm', 'alert', 'prompt', 'Error', 'toast', 'notify', 'setInfo', 'setWarning', 'setDone', 'setResult', 'setHint'].includes(c)) return 'yes'
        return 'prose'
      }
      return 'prose'
    }
    const visit = (n) => {
      if (ts.isJsxText(n)) {
        const t = collapse(n.text)
        if (t && /[A-Za-z]{2}/.test(t) && !codeLike(t)) add(strings, t, where(n))
      } else if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n)) {
        const t = collapse(n.text)
        const r = reason(n)
        if (!codeLike(t) && (r === 'yes' ? t && /[A-Za-z]/.test(t) && !classLike(t) : r === 'prose' && prose(t))) add(strings, t, where(n))
      } else if (ts.isTemplateExpression(n)) {
        const parts = [n.head.text, ...n.templateSpans.map((s) => s.literal.text)]
        const words = parts.join(' ')
        const r = reason(n)
        if ((r === 'yes' || (r === 'prose' && prose(words))) && /[A-Za-z]{2}/.test(words) && !classLike(words)) {
          let pat = ''
          parts.forEach((p, i) => {
            pat += p.replace(/\{/g, '{{').replace(/\}/g, '}}')
            if (i < parts.length - 1) pat += `{${i}}`
          })
          pat = collapse(pat)
          // A template that is only placeholders and punctuation has nothing to translate.
          if (/[A-Za-z]{2}/.test(pat.replace(/\{\d+\}/g, ''))) add(patterns, pat, where(n))
        }
      }
      ts.forEachChild(n, visit)
    }
    visit(src)
  }
  const sorted = (m) => [...m.keys()].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0))
  return { strings: sorted(strings), patterns: sorted(patterns) }
}

function readJSON(p, fallback) {
  try {
    return JSON.parse(fs.readFileSync(p, 'utf8'))
  } catch {
    return fallback
  }
}

function writeJSON(p, v) {
  fs.mkdirSync(path.dirname(p), { recursive: true })
  fs.writeFileSync(p, JSON.stringify(v, null, 1) + '\n')
}

/** placeholders lists a text's {0} {1} ..., sorted, so a translation can be checked to keep them all. */
export function placeholders(s) {
  return (s.match(/\{\d+\}/g) ?? []).sort().join(',')
}

const glossary = `Keep these exactly as they are: Perfuse, HL7, FHIR, MLLP, DICOM, DICOMweb, X12, EDIFACT, AS2, OpenPGP, NCPDP, CDA,
SMART, OAuth, OpenID Connect, SAML, SCIM, LDAP, TLS, SFTP, S3, SQS, SNS, Kafka, IMAP, POP3, SNMP, MCP, JSON, YAML, XML, CSV,
SQL, PostgreSQL, SQLite, API, URL, URI, OID, EUID, MRN, PID, MSH, ACK, PAS, CRD, DTR, CMS, PDex, IPS, XDS, XCA, MHD, PIXm,
PDQm, TEFCA, Mirth, Inferno, LOINC, SNOMED CT, ICD-10, ICD-11, RxNorm, CPT, GDPR, UK GDPR, NHS, AWS, Azure, Lua, JavaScript,
WebAssembly, Git, product and file names, code, paths, and anything in backquotes. Keep {0}, {1} ... placeholders exactly, in the
position the sentence needs. Keep the sentence case of the English (a capital only where the English has one, apart from the
language's own rules, such as German nouns). Keep the same punctuation at the start and end, including a leading or trailing
space if there is one, and any trailing colon or ellipsis. Use the formal form of address where a language has one (Sie, vous,
u), the register of a professional healthcare IT product, and the same word for the same term every time: a "channel" is the
integration engine's channel, a "destination" where a channel sends, a "source" where it receives, the "queue" the delivery
queue, a "tenant" a separate organisation on one server, a "claim" a healthcare insurance claim (billing), "prior authorization" the
payer's approval before care. A text that is code, a command-line flag, a file name or extension, a sample message or an
identifier is returned unchanged.`

async function translateBatch(lang, items, key) {
  const body = {
    model: process.env.I18N_MODEL || 'meta-llama/llama-3.3-70b-instruct',
    temperature: 0,
    // The fastest of the providers serving the model: the work is many independent requests, so throughput is what matters.
    provider: { sort: 'throughput' },
    messages: [
      { role: 'system', content: `You translate the user interface of a healthcare integration engine from English into ${LANGUAGES[lang]} (${lang}). ${glossary}\nAnswer with a JSON object only: each key is the number given, each value the translation.` },
      { role: 'user', content: JSON.stringify(Object.fromEntries(items.map((s, i) => [String(i), s]))) },
    ],
    response_format: { type: 'json_object' },
  }
  for (let attempt = 0; attempt < 4; attempt++) {
    try {
      const res = await fetch('https://openrouter.ai/api/v1/chat/completions', {
        method: 'POST',
        headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json', 'X-Title': 'Perfuse console translation' },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(180000),
      })
      if (!res.ok) throw new Error(`OpenRouter ${res.status}: ${(await res.text()).slice(0, 200)}`)
      const j = await res.json()
      let txt = j.choices?.[0]?.message?.content ?? ''
      txt = txt.replace(/^```(?:json)?\s*|\s*```$/g, '')
      const obj = JSON.parse(txt.slice(txt.indexOf('{'), txt.lastIndexOf('}') + 1))
      const out = {}
      items.forEach((s, i) => {
        const t = obj[String(i)]
        if (typeof t === 'string' && t.trim() && placeholders(t) === placeholders(s)) {
          // The English's leading and trailing spaces, which the model tends to drop.
          const lead = s.match(/^\s*/)[0], trail = s.match(/\s*$/)[0]
          out[s] = lead + t.trim() + trail
        }
      })
      return out
    } catch (e) {
      if (attempt === 3) {
        console.error(`  ${lang}: a batch failed: ${e.message}`)
        return {}
      }
      await new Promise((r) => setTimeout(r, 2000 * (attempt + 1)))
    }
  }
  return {}
}

async function translate(langs) {
  const key = process.env.OPENROUTER_API_KEY
  if (!key) throw new Error('OPENROUTER_API_KEY is not set')
  const en = readJSON(path.join(catDir, 'en.json'), null)
  if (!en) throw new Error('run extract first')
  const source = [...en.strings, ...en.patterns]
  const parallel = Number(process.env.I18N_PARALLEL || 6)
  for (const lang of langs) {
    if (!LANGUAGES[lang]) throw new Error(`${lang} is not one of ${Object.keys(LANGUAGES).join(', ')}`)
    const file = path.join(catDir, `${lang}.json`)
    const have = readJSON(file, {})
    for (let round = 0; round < 3; round++) {
      const missing = source.filter((s) => !have[s])
      if (missing.length === 0) break
      console.log(`${lang}: ${missing.length} to translate (round ${round + 1})`)
      const batches = []
      for (let i = 0; i < missing.length; i += 40) batches.push(missing.slice(i, i + 40))
      let next = 0, done = 0
      const worker = async () => {
        while (next < batches.length) {
          const b = batches[next++]
          Object.assign(have, await translateBatch(lang, b, key))
          if (++done % 10 === 0) {
            writeJSON(file, Object.fromEntries(Object.entries(have).sort()))
            console.log(`  ${lang}: ${done}/${batches.length} batches`)
          }
        }
      }
      await Promise.all(Array.from({ length: parallel }, worker))
      writeJSON(file, Object.fromEntries(Object.entries(have).filter(([k]) => source.includes(k)).sort()))
    }
    // Entries for text no screen shows any more go, so a catalogue holds only what is in use.
    writeJSON(file, Object.fromEntries(Object.entries(have).filter(([k]) => source.includes(k)).sort()))
    const left = source.filter((s) => !have[s]).length
    console.log(`${lang}: done, ${left} left untranslated`)
  }
}

export function check() {
  const problems = []
  const want = extract()
  const en = readJSON(path.join(catDir, 'en.json'), null)
  if (!en || JSON.stringify(en) !== JSON.stringify(want)) problems.push('src/i18n/catalog/en.json is out of date: run node scripts/i18n.mjs extract')
  const source = [...want.strings, ...want.patterns]
  for (const lang of Object.keys(LANGUAGES)) {
    const t = readJSON(path.join(catDir, `${lang}.json`), {})
    const missing = source.filter((s) => typeof t[s] !== 'string' || !t[s].trim())
    const broken = source.filter((s) => typeof t[s] === 'string' && placeholders(t[s]) !== placeholders(s))
    if (missing.length) problems.push(`${lang}: ${missing.length} untranslated, such as ${JSON.stringify(missing.slice(0, 3))}: run node scripts/i18n.mjs translate ${lang}`)
    if (broken.length) problems.push(`${lang}: ${broken.length} translations lose a {n} placeholder, such as ${JSON.stringify(broken.slice(0, 2))}`)
  }
  return problems
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  const [cmd, ...args] = process.argv.slice(2)
  if (cmd === 'extract') {
    const out = extract()
    writeJSON(path.join(catDir, 'en.json'), out)
    console.log(`${out.strings.length} strings and ${out.patterns.length} patterns`)
  } else if (cmd === 'translate') {
    await translate(args.length ? args : Object.keys(LANGUAGES))
  } else if (cmd === 'check') {
    const p = check()
    for (const x of p) console.error(x)
    process.exit(p.length ? 1 : 0)
  } else {
    console.error('usage: node scripts/i18n.mjs extract | translate [lang ...] | check')
    process.exit(2)
  }
}
