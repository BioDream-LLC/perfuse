// @vitest-environment happy-dom
import { describe, expect, it } from 'vitest'
import { compile, languageOf, setLanguage, translateText } from './runtime'

describe('the console in another language', () => {
  const { exact, patterns } = compile({
    'Save rules': 'Regeln speichern',
    'Messages failing': 'Fehlerhafte Nachrichten',
    '{0} messages waiting on {1}': 'Auf {1} warten {0} Nachrichten',
    'Running': 'Läuft',
    'Status: {0}': 'Status: {0}',
  })

  it('translates whole texts and keeps their spacing', () => {
    expect(translateText('Save rules', exact, patterns)).toBe('Regeln speichern')
    expect(translateText('  Save   rules ', exact, patterns)).toBe('  Regeln speichern ')
    expect(translateText('Not in the catalogue', exact, patterns)).toBeNull()
    expect(translateText('2026-10-18 14:00', exact, patterns)).toBeNull()
  })

  it('puts a pattern’s parts where the language wants them, translating a part it knows', () => {
    expect(translateText('12 messages waiting on adt-in', exact, patterns)).toBe('Auf adt-in warten 12 Nachrichten')
    expect(translateText('Status: Running', exact, patterns)).toBe('Status: Läuft')
  })

  it('reads a locale as a language', () => {
    expect(languageOf('de-AT')).toBe('de')
    expect(languageOf('nb-NO')).toBe('nb')
    expect(languageOf('no')).toBe('nb')
    expect(languageOf('en-GB')).toBe('en')
    expect(languageOf('ja-JP')).toBe('en')
  })

  it('swaps text as it is drawn, leaves code alone, and puts English back', async () => {
    document.body.innerHTML = '<div><button title="Save rules">Save rules</button><pre>Save rules</pre><span translate="no">Save rules</span></div>'
    await setLanguage('de')
    // The German catalogue is the real one: whatever it says for "Save rules", it is not English any more.
    const button = document.querySelector('button')!
    const translated = button.textContent
    expect(document.querySelector('pre')!.textContent).toBe('Save rules')
    expect(document.querySelector('span')!.textContent).toBe('Save rules')
    // A screen drawn later is translated too.
    const later = document.createElement('p')
    later.textContent = 'Save rules'
    document.body.appendChild(later)
    await new Promise((r) => setTimeout(r, 0))
    expect(later.textContent).toBe(translated)
    await setLanguage('en')
    expect(button.textContent).toBe('Save rules')
    expect(later.textContent).toBe('Save rules')
    expect(document.documentElement.lang).toBe('en')
  })
})
