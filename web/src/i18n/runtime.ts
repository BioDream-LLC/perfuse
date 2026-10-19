/**
 * The console in another language.
 *
 * The components are written in English, and stay so. When a language is chosen, its catalogue (src/i18n/catalog/<lang>.json,
 * English text to translation, made by scripts/i18n.mjs) is loaded and every piece of text the console draws is swapped for
 * its translation as it appears: text nodes, and the attributes people read (title, placeholder, aria-label, alt). A sentence
 * built from parts (`${n} messages waiting`) is matched by its pattern ({0} messages waiting) and translated with its parts in
 * the place the language puts them.
 *
 * What is never translated: code, messages, payloads and anything a person typed. Text inside <code>, <pre>, <kbd>, <samp>,
 * <textarea>, inputs' values, code editors, and any element marked translate="no" or data-no-translate stays as it is, so an
 * HL7 segment or a channel name is never "translated" because it happens to be an English word.
 */
import { uiLocale } from './locale'

type Catalogue = Record<string, string>

interface Pattern {
  re: RegExp
  to: string
  /** needle is the pattern's longest literal part: a text that does not contain it cannot match, which is checked first. */
  needle: string
}

const SKIP = 'code, pre, kbd, samp, textarea, script, style, .cm-editor, [translate="no"], [data-no-translate]'
const ATTRS = ['title', 'placeholder', 'aria-label', 'alt'] as const

let exact: Map<string, string> | null = null
let patterns: Pattern[] = []
let observer: MutationObserver | null = null
// What each text node and attribute said in English, and what it was changed to: React writes English again when it
// redraws, and the next translation must start from that, not from the translation.
const textState = new WeakMap<Text, { en: string; shown: string }>()
const attrState = new WeakMap<Element, Map<string, { en: string; shown: string }>>()

const loaders = import.meta.glob<{ default: Catalogue }>(['./catalog/*.json', '!./catalog/en.json'])

/** The languages the console speaks, besides English, with their own names (kept in step with scripts/i18n.mjs). */
export const LANGUAGES: Record<string, string> = {
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

/** languageOf is the console language for a locale: de-AT is German, nb-NO and no are Norwegian, anything else English. */
export function languageOf(locale: string): string {
  const l = locale.toLowerCase()
  if (l === 'no' || l.startsWith('no-') || l.startsWith('nn')) return 'nb'
  const base = l.split('-')[0] ?? ''
  return LANGUAGES[base] ? base : 'en'
}

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/** compile turns the catalogue into an exact table and the patterns (keys with {0}, {1} ...). */
export function compile(cat: Catalogue): { exact: Map<string, string>; patterns: Pattern[] } {
  const ex = new Map<string, string>()
  const pats: Pattern[] = []
  for (const [en, to] of Object.entries(cat)) {
    if (/\{\d+\}/.test(en)) {
      const parts = en.split(/(\{\d+\})/)
      let re = '^'
      let needle = ''
      const order: number[] = []
      for (const p of parts) {
        const m = /^\{(\d+)\}$/.exec(p)
        if (m) {
          re += '(.+?)'
          order.push(Number(m[1]))
        } else {
          const lit = p.replace(/\{\{/g, '{').replace(/\}\}/g, '}')
          re += escapeRe(lit)
          if (lit.trim().length > needle.length) needle = lit.trim()
        }
      }
      // The groups are numbered in the order they appear; the translation names them by the English placeholder's number.
      const remap = order
      pats.push({
        re: new RegExp(re + '$', 's'),
        to: to.replace(/\{(\d+)\}/g, (_, n) => `\u0000${remap.indexOf(Number(n)) + 1}\u0000`),
        needle,
      })
    } else {
      ex.set(en, to)
    }
  }
  // The most specific patterns first: a longer literal part is less likely to match by accident.
  pats.sort((a, b) => b.needle.length - a.needle.length)
  return { exact: ex, patterns: pats }
}

/** translate gives a text in the chosen language, or null when the catalogue does not know it. Spacing is kept. */
export function translateText(text: string, ex: Map<string, string> | null = exact, pats: Pattern[] = patterns): string | null {
  if (!ex) return null
  const core = text.replace(/\s+/g, ' ').trim()
  if (!core || !/[A-Za-z]/.test(core)) return null
  const lead = /^\s*/.exec(text)?.[0] ?? ''
  const trail = /\s*$/.exec(text)?.[0] ?? ''
  const hit = ex.get(core)
  if (hit !== undefined) return lead + hit.trim() + trail
  if (core.length > 600) return null
  for (const p of pats) {
    if (p.needle && !core.includes(p.needle)) continue
    const m = p.re.exec(core)
    if (!m) continue
    // A part is itself translated when the catalogue knows it (a status word inside a sentence).
    const out = p.to.replace(/\u0000(\d+)\u0000/g, (_, i) => {
      const part = m[Number(i)] ?? ''
      return ex.get(part.trim()) ?? part
    })
    return lead + out.trim() + trail
  }
  return null
}

function skipped(el: Element | null): boolean {
  return !!el && !!el.closest(SKIP)
}

function doText(node: Text) {
  if (skipped(node.parentElement)) return
  const st = textState.get(node)
  const now = node.data
  if (st && now === st.shown) return
  const en = now
  const tr = translateText(en)
  if (tr === null || tr === en) {
    if (st) textState.delete(node)
    return
  }
  textState.set(node, { en, shown: tr })
  node.data = tr
}

function doAttrs(el: Element) {
  if (skipped(el)) return
  for (const a of ATTRS) {
    const v = el.getAttribute(a)
    if (v === null) continue
    let m = attrState.get(el)
    const st = m?.get(a)
    if (st && v === st.shown) continue
    const tr = translateText(v)
    if (tr === null || tr === v) {
      m?.delete(a)
      continue
    }
    if (!m) {
      m = new Map()
      attrState.set(el, m)
    }
    m.set(a, { en: v, shown: tr })
    el.setAttribute(a, tr)
  }
}

function walk(root: Node) {
  if (root.nodeType === Node.TEXT_NODE) {
    doText(root as Text)
    return
  }
  if (root.nodeType !== Node.ELEMENT_NODE && root.nodeType !== Node.DOCUMENT_FRAGMENT_NODE) return
  if (root.nodeType === Node.ELEMENT_NODE) {
    const el = root as Element
    if (skipped(el)) return
    doAttrs(el)
  }
  const tw = document.createTreeWalker(root, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT, {
    acceptNode: (n) =>
      n.nodeType === Node.ELEMENT_NODE && (n as Element).matches(SKIP) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT,
  })
  for (let n = tw.nextNode(); n; n = tw.nextNode()) {
    if (n.nodeType === Node.TEXT_NODE) doText(n as Text)
    else doAttrs(n as Element)
  }
}

/** restore puts English back everywhere this changed it. */
function restore(root: Node) {
  const tw = document.createTreeWalker(root, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT)
  for (let n = tw.nextNode(); n; n = tw.nextNode()) {
    if (n.nodeType === Node.TEXT_NODE) {
      const st = textState.get(n as Text)
      if (st && (n as Text).data === st.shown) (n as Text).data = st.en
      textState.delete(n as Text)
    } else {
      const m = attrState.get(n as Element)
      if (m) {
        for (const [a, st] of m) if ((n as Element).getAttribute(a) === st.shown) (n as Element).setAttribute(a, st.en)
        attrState.delete(n as Element)
      }
    }
  }
}

let current = 'en'

/** currentLanguage is the language the console is shown in now. */
export function currentLanguage(): string {
  return current
}

/** setLanguage shows the console in a language ('en' for English), loading its catalogue the first time. */
export async function setLanguage(lang: string): Promise<void> {
  const want = LANGUAGES[lang] ? lang : 'en'
  observer?.disconnect()
  observer = null
  if (current !== 'en') restore(document.body)
  current = want
  document.documentElement.lang = want === 'en' ? 'en' : want
  if (want === 'en') {
    exact = null
    patterns = []
    return
  }
  const load = loaders[`./catalog/${want}.json`]
  if (!load) return
  const cat = (await load()).default
  if (current !== want) return // changed again while loading
  ;({ exact, patterns } = compile(cat))
  walk(document.body)
  observer = new MutationObserver((records) => {
    for (const r of records) {
      if (r.type === 'characterData') doText(r.target as Text)
      else if (r.type === 'attributes') doAttrs(r.target as Element)
      else r.addedNodes.forEach((n) => walk(n))
    }
  })
  observer.observe(document.body, { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: [...ATTRS] })
}

/** startLanguage shows the console in the language of the locale chosen (or the browser's), at start. */
export function startLanguage(): Promise<void> {
  return setLanguage(languageOf(uiLocale()))
}
