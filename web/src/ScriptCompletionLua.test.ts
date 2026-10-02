import { describe, it, expect } from 'vitest'
import { completeHL7 } from './ScriptEditor'
import type { CompletionContext } from '@codemirror/autocomplete'

// Completion had to reach the Lua form of a segment reference.
//
// The workbench compiles Lua now, but the completion trigger matched a bracket subscript only - msg['PID' - which is JavaScript's
// shape. In Lua the same reference is a call: msg.child("PID"). So a Lua author got no field names at all, on the panel whose
// stated purpose is that somebody who does not know HL7 can still find the field they want.
//
// The scoping matters as much as the trigger. Offering fifty segment names inside every string literal would be worse than
// offering none, so this is limited to the named tree accessors, and get and set are excluded on purpose: those belong to the
// path-addressed formats, where a path is a loop name or a column heading and an HL7 segment would be confidently wrong.

function contextAt(text: string): CompletionContext {
  return {
    pos: text.length,
    matchBefore(re: RegExp) {
      const m = text.match(new RegExp(re.source.replace(/\$$/, '') + '$'))
      if (!m) return null
      return { from: text.length - m[0].length, to: text.length, text: m[0] }
    },
  } as unknown as CompletionContext
}

const sets = {
  segments: [
    { label: 'PID', detail: 'Patient identification' },
    { label: 'MSH', detail: 'Message header' },
  ],
  fieldsBySegment: new Map([
    [
      'PID',
      [
        { label: 'PID.3', detail: 'Patient identifier list' },
        { label: 'PID.5', detail: 'Patient name' },
      ],
    ],
  ]),
}

describe('script completion', () => {
  it('offers segments inside a JavaScript subscript', () => {
    const out = completeHL7(contextAt("msg['P"), sets as never)
    expect(out).not.toBeNull()
    expect(out?.options.map((o) => o.label)).toContain('PID')
  })

  it('offers segments inside a Lua accessor call', () => {
    // The gap. Before this, a Lua author typing the correct thing got nothing.
    const out = completeHL7(contextAt('msg.child("P'), sets as never)
    expect(out).not.toBeNull()
    expect(out?.options.map((o) => o.label)).toContain('PID')
  })

  it('offers a segment fields inside ensure, which is the write counterpart', () => {
    const out = completeHL7(contextAt('msg.child("PID").ensure("PID.'), sets as never)
    expect(out).not.toBeNull()
    expect(out?.options.map((o) => o.label)).toContain('PID.5')
  })

  it('offers nothing inside an unrelated call', () => {
    // The scoping half. Without this the trigger would fire in every string literal in the file.
    expect(completeHL7(contextAt('logger.info("P'), sets as never)).toBeNull()
    expect(completeHL7(contextAt('string.find(m, "P'), sets as never)).toBeNull()
  })

  it('offers nothing inside get or set, which address the non-HL7 formats', () => {
    // A delimited path is a column heading and an X12 path is an element position. Offering PID there would be a
    // confident wrong answer, which is worse than silence.
    expect(completeHL7(contextAt('msg.get("W'), sets as never)).toBeNull()
    expect(completeHL7(contextAt('msg.set("S'), sets as never)).toBeNull()
  })
})
