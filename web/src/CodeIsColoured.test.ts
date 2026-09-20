import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

// Code is shown coloured, and stays that way.
//
// # What this guards
//
// Every box that holds HL7, FHIR, XML, X12, YAML or a script goes through CodeArea when it is editable and SyntaxBlock when it is not.
// Both draw the same tokenizer. A plain textarea or a bare pre holding code is the defect: it was the state of the Playground, where
// the two things that tab exists to demonstrate - a message and a Mirth script - were the least readable text on the page.
//
// # Why a source-reading test rather than a rendering one
//
// Because the failure is a new box, not a broken one. Somebody adding a panel reaches for a textarea, it works, it looks fine to them,
// and nothing complains. A rendering test only covers the boxes somebody thought to write a test for, which is exactly the set that is
// already correct.
//
// # What is deliberately left plain
//
// Not everything monospaced is code, and colouring things that are not is worse than leaving them grey, because it implies a structure
// that is not there. The allowed list below is the whole set, each with a reason. A PEM block is base64. A column of field names is a
// list. The printed-document template says in its own hint that it is not markup and that tags would be printed literally, so
// colouring it as XML would state the opposite of what the field says.

/** Boxes that may hold plain text, with the reason each one is not code. */
const allowedPlain: Record<string, string> = {
  'InspectCertificate.tsx': 'a PEM certificate is base64, and there is no structure to colour',
  'SignaturePanel.tsx': 'PEM again',
  'MapperPanel.tsx': 'two columns of field names, one per line - a list, not a document',
  'ChannelBuilder.tsx':
    'SQL, a list of query parameters, an email body with placeholders, and the printed-document template ' +
    'whose own hint says it is not markup. The code-bearing boxes in this file use CodeArea',
  'builderFields.tsx': 'the generic Area, used for prose fields. ScriptArea beside it carries the script slots',
  'Playground.tsx': 'the shared field here uses CodeArea; this entry covers nothing else in the file',
  'CodeArea.tsx': 'the implementation. The textarea underneath the coloured layer is the whole mechanism',
}

function sourceFiles(): string[] {
  const dir = join(__dirname)

  return readdirSync(dir)
    .filter((f) => f.endsWith('.tsx') && !f.endsWith('.test.tsx'))
    .map((f) => join(dir, f))
}

describe('code is shown coloured', () => {
  it('has boxes to check, so a pass means something', () => {
    const total = sourceFiles()
      .map((f) => readFileSync(f, 'utf8'))
      .join('\n')
      .match(/<CodeArea\b/g)

    expect(total?.length ?? 0).toBeGreaterThan(10)
  })

  it('routes every code-bearing box through the highlighter', () => {
    const offenders: string[] = []

    for (const path of sourceFiles()) {
      const name = path.split('/').pop() ?? ''
      const body = readFileSync(path, 'utf8')

      if (!body.includes('<textarea')) continue
      if (allowedPlain[name]) continue

      offenders.push(name)
    }

    expect(
      offenders,
      'these files use a plain textarea. If it holds HL7, FHIR, XML, X12, YAML or a script, use CodeArea with the right\n' +
        'language. If it genuinely holds prose or a list, add it to allowedPlain with the reason:\n' +
        offenders.map((o) => `  ${o}`).join('\n'),
    ).toEqual([])
  })

  it('keeps a reason for every plain box, and no stale reasons', () => {
    // An allowance with no textarea left in the file is a leftover, and the next person cannot tell which entries still matter.
    const stale = Object.keys(allowedPlain).filter((name) => {
      const path = sourceFiles().find((p) => p.endsWith(name))
      if (!path) return true

      return !readFileSync(path, 'utf8').includes('<textarea')
    })

    expect(stale, `these allowedPlain entries no longer have a plain textarea: ${stale.join(', ')}`).toEqual([])
  })

  it('names a language wherever one is known', () => {
    // A CodeArea with no language falls back to sniffing the content, which is right for a box that accepts either a CDA document
    // or an HL7 message and wrong as a habit - a message that has not been typed yet sniffs as nothing.
    const sniffing: string[] = []

    for (const path of sourceFiles()) {
      const body = readFileSync(path, 'utf8')

      for (const m of body.matchAll(/<CodeArea\b([\s\S]{0,400}?)\/>/g)) {
        if (!/\blanguage=/.test(m[1] ?? '')) {
          sniffing.push(path.split('/').pop() ?? '')
        }
      }
    }

    // DocumentLab's box takes a clinical document or an HL7 message and cannot know which until something is pasted.
    expect(sniffing.sort()).toEqual(['DocumentLab.tsx'])
  })
})
