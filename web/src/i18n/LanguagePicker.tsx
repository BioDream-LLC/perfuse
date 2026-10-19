import { useState } from 'react'
import { LANGUAGES, languageOf, setLanguage } from './runtime'
import { setUiLocale, uiLocale } from './locale'

/** The locales offered: English in its two date orders, then each translated language with its usual region. */
const CHOICES: [string, string][] = [
  ['en-GB', 'English (UK)'],
  ['en-US', 'English (US)'],
  ['de-DE', LANGUAGES.de!],
  ['fr-FR', LANGUAGES.fr!],
  ['nl-NL', LANGUAGES.nl!],
  ['es-ES', LANGUAGES.es!],
  ['it-IT', LANGUAGES.it!],
  ['pt-PT', LANGUAGES.pt!],
  ['pl-PL', LANGUAGES.pl!],
  ['sv-SE', LANGUAGES.sv!],
  ['da-DK', LANGUAGES.da!],
  ['nb-NO', LANGUAGES.nb!],
  ['fi-FI', LANGUAGES.fi!],
]

/**
 * LanguagePicker sits in the header beside the theme: the language the console is written in, and the order dates and
 * numbers are written in. Kept in this browser for the person signed in. The translations were made by machine and have not
 * yet been reviewed by native speakers; English is the reference, and the picker says so.
 */
export function LanguagePicker() {
  const [locale, setLocale] = useState(() => {
    const l = uiLocale()
    return CHOICES.find(([c]) => c === l)?.[0] ?? CHOICES.find(([c]) => languageOf(c) === languageOf(l))?.[0] ?? 'en-GB'
  })
  return (
    <label className="hidden items-center gap-2 sm:inline-flex" title="Translated by machine and not yet reviewed by a native speaker. English is the reference.">
      <span className="sr-only">Language</span>
      <select
        aria-label="Language"
        translate="no"
        className="rounded-md border border-slate-700 bg-slate-900 px-2 py-1 text-xs text-slate-300"
        value={locale}
        onChange={(e) => {
          const next = e.target.value
          setLocale(next)
          setUiLocale(next)
          void setLanguage(languageOf(next)).then(() => window.dispatchEvent(new Event('perfuse-locale')))
        }}
      >
        {CHOICES.map(([c, label]) => (
          <option key={c} value={c}>
            {label}
          </option>
        ))}
      </select>
    </label>
  )
}
