import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'

/**
 * Red means something is wrong right now, and nothing else.
 *
 * The question that produced this file, asked after clicking through the interface: several screens
 * showed red text the moment they opened. Should that be informational colouring instead, turning red
 * only when something the user typed goes wrong?
 *
 * Yes. Red is the only colour in an interface that carries an instruction - stop, look at this, and it
 * may be something you did. A screen that opens red has spent that meaning before the user has acted,
 * so when their input really is rejected the message has to compete with decoration that was already
 * there. One screen was worse than decorative: the users screen opened with "the server sent a response
 * that could not be read" on every installation without passkeys configured, which was a real fault
 * being reported in the right colour for entirely the wrong reason.
 *
 * Four registers, and each says something different:
 *
 *   slate    a fact about how the product works. Never coloured. Most text.
 *   emerald  something succeeded, just now, because of what you did.
 *   amber    a caution. Worth knowing before you act; not a fault. Not configured, audited,
 *            approximated, unsupported-but-handled.
 *   red      something is wrong at this moment. A rejected value, a failed request, an expired
 *            certificate, a firing critical alert.
 *
 * What this test does not police. Syntax highlighting colours HL7 segment names and XML tag names red,
 * and that is a different vocabulary - inside a code box, red is a token type rather than a judgement.
 * Severity words like "critical" are also left alone, since there the red is the meaning.
 */

const src = 'src'

/** componentFiles is every component in the tree. */
function componentFiles(): string[] {
  return readdirSync(src)
    .filter((f) => f.endsWith('.tsx'))
    .map((f) => `${src}/${f}`)
}

describe('red is reserved for something being wrong now', () => {
  it('does not use red for a state that is merely not configured', () => {
    // These words describe an absence or an approximation. An absence is a caution at most: the operator
    // may want to change it, but nothing is broken and nothing they did caused it.
    const cautionWords = [
      'not configured',
      'not switched on',
      'not set up',
      'not enabled',
      'nothing to show yet',
      'no channels yet',
      'does not convert',
      'cannot be converted',
      'approximated',
    ]

    const offenders: string[] = []

    for (const file of componentFiles()) {
      const text = readFileSync(file, 'utf8')

      // Line-based, because the styling and the words have to be in the same element to mislead anybody.
      for (const line of text.split('\n')) {
        const lower = line.toLowerCase()
        if (!/text-red-|text-rose-|border-red-|border-rose-|bg-red-|bg-rose-/.test(line)) continue
        if (cautionWords.some((w) => lower.includes(w))) {
          offenders.push(`${file}: ${line.trim().slice(0, 120)}`)
        }
      }
    }

    expect(
      offenders,
      `red is for something being wrong now. These describe a state that is absent or approximate, which is amber:\n\n${offenders.join('\n')}`,
    ).toEqual([])
  })

  it('keeps a red style available for real failures', () => {
    // The other half. A convention enforced only by removing red would end with no way to say a value was
    // rejected, which is worse than the inconsistency it tidied.
    const all = componentFiles()
      .map((f) => readFileSync(f, 'utf8'))
      .join('\n')

    expect(/text-red-|text-rose-|border-red-|bg-red-/.test(all)).toBe(true)
  })
})
