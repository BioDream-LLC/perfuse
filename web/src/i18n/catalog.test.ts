import { describe, expect, it } from 'vitest'
// @ts-expect-error: a plain JavaScript build script, without types
import { check, LANGUAGES as scriptLanguages } from '../../scripts/i18n.mjs'
import { LANGUAGES } from './runtime'

describe('the message catalogue', () => {
  // Every piece of interface text has a translation in every language, keeping its {0} placeholders, and en.json is what
  // the source says today. A new screen fails here until scripts/i18n.mjs extract and translate have been run.
  it('is complete in every language', () => {
    expect(check()).toEqual([])
  }, 60000)

  it('names the same languages in the script and the console', () => {
    expect(scriptLanguages).toEqual(LANGUAGES)
  })
})
