import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { sectionIcons } from './Icons'

// Every section heading has an icon, and keeps having one.
//
// # What this is for
//
// Seventy-nine icons existed and four files used them, all navigation. Every section heading in the application was plain text, so
// the icons a person saw while choosing where to go vanished the moment they arrived.
//
// Resolving the icon from the heading text covers all of them without editing thirty-three files, but a lookup table has an obvious
// failure mode: a heading added later simply misses, silently, and ends up as the one plain heading in the app. Nobody would notice
// for months, and when they did it would look like a design choice.
//
// So this reads the headings out of the source. It is the same guard shape used for the builder's language menu and the workbench's
// slot list, and it exists for the same reason: two declarations that have to agree, with nothing but a test to make them.
//
// # What it cannot see
//
// Headings whose title is computed rather than written. Those need an explicit icon prop, and no static check can tell whether one
// was passed. Counting them is the compromise: if the number of computed titles grows, that is worth a look, and the count being
// asserted means somebody has to think about it rather than drift past it.

function sourceFiles(): string[] {
  const dir = join(__dirname)

  return readdirSync(dir)
    .filter((f) => f.endsWith('.tsx') && !f.endsWith('.test.tsx'))
    .map((f) => join(dir, f))
}

/** Every <Section> in the source, with its literal title where it has one. */
function sectionTitles(): { literal: string[]; computed: number } {
  const literal: string[] = []
  let computed = 0

  for (const path of sourceFiles()) {
    const body = readFileSync(path, 'utf8')

    for (const m of body.matchAll(/<Section\b([\s\S]{0,300}?)>/g)) {
      const props = m[1] ?? ''

      // An explicit icon prop answers for itself, computed title or not.
      if (/\bicon=/.test(props)) continue

      // A forwarded title - title={title} - is not a gap. The lookup happens on the real string at run time, so the icon
      // resolves exactly as it would for a literal; the string simply lives at the caller, where this test already checks it.
      // A template literal is different and does count, because a string built at run time can never match a registry key,
      // so those sites need an explicit icon.
      if (/title=\{\s*title(\s+as\s+string)?\s*\}/.test(props)) continue

      const title = /title="([^"]*)"/.exec(props)
      if (title) {
        literal.push(title[1] ?? '')
      } else {
        computed += 1
      }
    }
  }

  return { literal, computed }
}

describe('section icons', () => {
  it('finds sections to check, so a passing result means something', () => {
    const { literal } = sectionTitles()

    // Without this the whole file passes if the regex stops matching, which is the failure mode of every source-reading test.
    expect(literal.length).toBeGreaterThan(20)
  })

  it('gives every written heading an icon', () => {
    const { literal } = sectionTitles()

    const missing = [...new Set(literal)].filter((t) => !sectionIcons[t]).sort()

    expect(
      missing,
      `these section headings have no icon. Add them to sectionIcons in Icons.tsx, or pass an explicit icon prop:\n` +
        missing.map((m) => `  ${JSON.stringify(m)}`).join('\n'),
    ).toEqual([])
  })

  it('has no entries for headings that no longer exist', () => {
    // Any quoted occurrence counts, not only a literal Section title. Some headings are built from a table of tuples, where the
    // string is a data value and the lookup still happens on the real text at run time. Requiring them to appear as a Section
    // title would have forced either a worse data structure or a weaker test, and both would be the tail wagging the dog.
    const all = sourceFiles()
      .map((f) => readFileSync(f, 'utf8'))
      .join('\n')

    // A stale entry is harmless on screen and misleading to read: it suggests a section exists that does not, and the next person
    // renaming a heading cannot tell which entries are load-bearing.
    const stale = Object.keys(sectionIcons)
      .filter((t) => !all.includes(`'${t}'`) && !all.includes(`"${t}"`))
      .sort()

    expect(stale, `these sectionIcons entries match no heading in the source: ${stale.join(', ')}`).toEqual([])
  })

  it('counts the headings whose title is computed', () => {
    const { computed } = sectionTitles()

    // These cannot be checked statically and need an explicit icon prop. The number is asserted so that adding one is a decision
    // rather than an accident. Raise it deliberately, having passed an icon.
    expect(computed).toBe(0)
  })
})
