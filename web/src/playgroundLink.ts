// Sharing a playground session as a link.
//
// The playground runs the real engine compiled to WebAssembly, entirely in the browser. That makes it the natural way to ask somebody a
// question about a message - "why does this filter not match?" - and the natural way to answer one. A link that reproduces exactly what
// you are looking at turns a paragraph of description into one click.
//
// Everything about the encoding below is shaped by one fact: an HL7 message contains a real patient.

/** The state a link reproduces. */
export interface PlaygroundState {
  tab: string
  message: string
  script: string
  filter: string
  steps: string
}

/**
 * The largest link this will produce, in characters of encoded payload.
 *
 * Browsers themselves tolerate far more, but a link is only useful if it survives the journey - and it travels through ticket systems,
 * chat clients and mail gateways, several of which wrap or truncate at a few thousand characters. A truncated link does not fail
 * visibly: it decodes to nothing, or worse, to a shorter message that still parses.
 *
 * So the ceiling is deliberately conservative and encodeState refuses above it rather than producing something that may or may not
 * arrive intact.
 */
export const maxEncodedLength = 6000

/** Why a state could not be shared. */
export class TooLargeToShare extends Error {
  constructor(readonly length: number) {
    super(
      `this session encodes to ${length.toLocaleString()} characters, and the limit is ${maxEncodedLength.toLocaleString()}. ` +
        `Links this long are wrapped or cut short by ticket systems and mail gateways, and a cut link decodes to nothing ` +
        `or to a shorter message that still looks valid. Trim the message or the script and try again`,
    )
    this.name = 'TooLargeToShare'
  }
}

/**
 * Encodes a session as a URL fragment.
 *
 * A fragment, never a query string. The fragment is not sent to the server, so a message pasted into the playground does not reach the
 * access log, the proxy log, or any request trace on the way. With a query string it would reach all three, and a hospital's HTTP logs
 * are not where patient data should end up. This is the single most important line in the file.
 */
export function encodeState(state: PlaygroundState): string {
  const json = JSON.stringify(state)

  // UTF-8 first. btoa throws on anything above U+00FF, and a patient name with an accent in it is not an edge case in a hospital -
  // it is Tuesday.
  const bytes = new TextEncoder().encode(json)

  let binary = ''
  for (const b of bytes) binary += String.fromCharCode(b)

  // base64url, so the result survives being pasted into places that treat + and / as meaningful.
  const encoded = btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')

  if (encoded.length > maxEncodedLength) throw new TooLargeToShare(encoded.length)

  return encoded
}

/**
 * Decodes a fragment back into a session, or returns null.
 *
 * Null rather than throwing, and never a partial restore. A fragment can arrive damaged in a dozen ordinary ways - a line break
 * inserted by a mail client, a trailing bracket from markdown, a copy that missed the last character. Restoring the half that decoded
 * would show somebody a message that is not the one they were sent, with nothing to indicate it, and they would debug the wrong thing.
 */
export function decodeState(fragment: string): PlaygroundState | null {
  const trimmed = fragment.replace(/^#/, '').replace(/^playground=/, '').trim()
  if (!trimmed) return null

  try {
    const base64 = trimmed.replace(/-/g, '+').replace(/_/g, '/')
    const binary = atob(base64)

    const bytes = new Uint8Array(binary.length)
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)

    const parsed: unknown = JSON.parse(new TextDecoder().decode(bytes))
    if (typeof parsed !== 'object' || parsed === null) return null

    const record = parsed as Record<string, unknown>

    // Every field must be a string. A fragment is attacker-controlled input, and these values are put into editors and handed to the
    // engine. Checking the shape here means nothing downstream has to wonder.
    for (const key of ['tab', 'message', 'script', 'filter', 'steps']) {
      if (typeof record[key] !== 'string') return null
    }

    return {
      tab: record.tab as string,
      message: record.message as string,
      script: record.script as string,
      filter: record.filter as string,
      steps: record.steps as string,
    }
  } catch {
    return null
  }
}

/**
 * The fragment prefix that says which panel a link opens.
 *
 * Needed because the console selects its tab in state rather than from the URL, so a link with only a payload in it lands the recipient
 * on the dashboard - which defeats the entire purpose of sending it. Naming the panel in the fragment is the smallest thing that works
 * without changing how the rest of the console routes.
 *
 * Deep-linking every tab would be a better answer and is a separate change. This one does not pretend to be it.
 */
export const fragmentPrefix = 'playground='

/** Builds the full shareable URL for a session. */
export function shareLink(state: PlaygroundState, base: string): string {
  const url = base.split('#')[0]

  return `${url}#${fragmentPrefix}${encodeState(state)}`
}

/** Reports whether a fragment is a playground link, so the console knows which panel to open. */
export function isPlaygroundFragment(fragment: string): boolean {
  return fragment.replace(/^#/, '').startsWith(fragmentPrefix)
}
