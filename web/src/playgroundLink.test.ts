import { describe, expect, it } from 'vitest'
import { decodeState, encodeState, maxEncodedLength, shareLink, TooLargeToShare } from './playgroundLink'

const session = {
  tab: 'filter',
  message: 'MSH|^~\\&|SENDING|SITEA|PERFUSE|SITEB|20260823090000||ADT^A01|MSG1|P|2.5.1\rPID|1||MRN1^^^SITEA^MR||Evrard^Camille',
  script: 'return msg.get("PID-5.1")',
  filter: "MSH-9.2 == 'A01'",
  steps: '- set:\n    path: PID-8\n    value: U',
}

describe('sharing a playground session', () => {
  it('round trips', () => {
    expect(decodeState(encodeState(session))).toEqual(session)
  })

  it('survives characters outside latin-1', () => {
    // btoa throws above U+00FF. An accented patient name is not an edge case in a hospital, and neither is a Japanese one - a
    // encoder that only handled ASCII would fail on ordinary data and look like a bug in the message.
    const accented = { ...session, message: 'PID|1||M1||Évrard^Camille~中村^優希' }

    expect(decodeState(encodeState(accented))).toEqual(accented)
  })

  it('produces a fragment, never a query string', () => {
    // The single most important property here. A fragment is not sent to the server, so a message pasted into the playground does
    // not reach the access log, the proxy log or a request trace. A query string reaches all three, and a hospital's HTTP logs are
    // not where patient data belongs.
    const link = shareLink(session, 'https://perfuse.example.org/playground')

    expect(link).toContain('#')
    expect(link.split('#')[0]).not.toContain('?')
    expect(link.indexOf('#')).toBeLessThan(link.indexOf(encodeState(session)))

    // And the fragment names the panel, or the recipient lands on the dashboard with the session unused - the one thing the
    // link exists to prevent.
    expect(link).toContain('#playground=')
  })

  it('replaces an existing fragment rather than appending to it', () => {
    const link = shareLink(session, 'https://perfuse.example.org/playground#somethingelse')

    expect(link.match(/#/g)).toHaveLength(1)
    expect(decodeState(link.split('#')[1]!)).toEqual(session)
  })

  it('refuses to build a link too long to survive being sent', () => {
    const huge = { ...session, message: 'A'.repeat(maxEncodedLength * 2) }

    expect(() => encodeState(huge)).toThrow(TooLargeToShare)

    // The refusal has to say what to do. A cut link does not fail visibly - it decodes to nothing, or to a shorter message that
    // still parses - so producing one and hoping is the worse option.
    try {
      encodeState(huge)
    } catch (e) {
      expect(String(e)).toContain('Trim the message')
    }
  })

  it('returns null for a fragment that did not arrive intact', () => {
    // Mail clients insert line breaks, markdown adds a trailing bracket, a copy misses the last character. Restoring the half that
    // decoded would show somebody a message that is not the one they were sent, with nothing to say so.
    for (const bad of ['', '#', 'not base64 at all!!', encodeState(session).slice(0, 12), '####']) {
      expect(decodeState(bad), `${bad} should not decode`).toBeNull()
    }
  })

  it('rejects a fragment whose fields are not strings', () => {
    // A fragment is attacker-controlled input and these values go into editors and into the engine. Anything not of the declared
    // shape is refused whole rather than partly restored.
    const encode = (o: unknown) => {
      const bytes = new TextEncoder().encode(JSON.stringify(o))
      let binary = ''
      for (const b of bytes) binary += String.fromCharCode(b)

      return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
    }

    expect(decodeState(encode({ ...session, message: 42 }))).toBeNull()
    expect(decodeState(encode({ ...session, script: null }))).toBeNull()
    expect(decodeState(encode({ tab: 'filter' }))).toBeNull()
    expect(decodeState(encode(['not', 'an', 'object']))).toBeNull()
    expect(decodeState(encode('a string'))).toBeNull()
  })

  it('tolerates a leading hash', () => {
    expect(decodeState('#' + encodeState(session))).toEqual(session)
  })
})
