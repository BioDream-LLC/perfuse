/**
 * The locale the console formats dates, times and numbers in, and (once a language is chosen) writes its words in. It is the
 * signed-in person's choice when they made one, kept in this browser, and the browser's own language otherwise.
 */
const KEY = 'perfuse.locale'

export function uiLocale(): string {
  try {
    const chosen = localStorage.getItem(KEY)
    if (chosen) return chosen
  } catch {
    // storage refused (private mode): the browser's language
  }
  return typeof navigator !== 'undefined' && navigator.language ? navigator.language : 'en-GB'
}

export function setUiLocale(locale: string | null): void {
  try {
    if (locale) localStorage.setItem(KEY, locale)
    else localStorage.removeItem(KEY)
  } catch {
    // nothing to keep it in; the choice lasts until the page reloads
  }
}
