/**
 * Copying to the clipboard, in the places this product actually runs.
 *
 * The obvious call is `navigator.clipboard.writeText`, and it is wrong on its own twice over. The API is
 * absent outside a secure context, and Perfuse is deliberately runnable over plain HTTP on a segregated
 * network - which is where most hospital interfaces live, and the deployment the server warns about rather
 * than refuses. And even where the API exists the promise can reject: permission denied, a document that is
 * not focused, a browser that requires a user gesture it did not see.
 *
 * What was there before was `void navigator.clipboard?.writeText(text)`. The `?.` handles the API being
 * missing and nothing handles the rejection, so a denied copy became an unhandled promise rejection - a
 * console error nobody sees and a button that quietly did nothing. The person then pastes whatever was in
 * the clipboard before, which for an API token or a message body is worse than a visible failure.
 *
 * So: try the modern API, fall back to a selection-based copy that works without a secure context, and
 * report which happened so the caller can say so. This never throws.
 */

/** CopyResult says what happened, because a copy button that cannot report failure will eventually lie. */
export type CopyResult = 'copied' | 'failed'

/**
 * copyText puts text on the clipboard and says whether it worked.
 *
 * Never rejects and never throws, so a caller cannot forget to handle it.
 */
export async function copyText(text: string): Promise<CopyResult> {
  // The modern path, where it exists and is permitted.
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return 'copied'
    } catch {
      // Fall through. A rejection here is normal rather than exceptional - an unfocused document is enough
      // to cause it - so it is not worth logging, only worth recovering from.
    }
  }

  // The fallback, which is what makes this work over plain HTTP.
  //
  // execCommand is deprecated and still the only thing available in a non-secure context. Kept narrow: a
  // detached textarea, selected, copied, removed, with the caret restored so the page does not jump.
  try {
    if (typeof document === 'undefined') return 'failed'

    const area = document.createElement('textarea')
    area.value = text
    // Off-screen rather than hidden: a display:none element cannot be selected.
    area.setAttribute('readonly', '')
    area.style.position = 'fixed'
    area.style.top = '-1000px'
    area.style.opacity = '0'
    document.body.appendChild(area)

    const previous = document.activeElement
    area.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(area)

    // Put focus back where the person left it, or the next keystroke goes nowhere.
    if (previous instanceof HTMLElement) previous.focus()

    return ok ? 'copied' : 'failed'
  } catch {
    return 'failed'
  }
}
