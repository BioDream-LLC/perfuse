import { describe, expect, it } from 'vitest'
import { buildCompletions, completeHL7, toDiagnostics } from './ScriptEditor'
import type { DictSegment, ScriptCheck } from './api'

const dict: DictSegment[] = [
  {
    segment: 'PID',
    description: 'Patient identification',
    fields: [
      { number: 3, name: 'Patient Identifier List', repeats: true },
      { number: 5, name: 'Patient Name', components: ['Family', 'Given'] },
      { number: 7, name: 'Date/Time of Birth' },
      { number: 8, name: 'Administrative Sex', table: '0001' },
    ],
  },
  {
    segment: 'OBX',
    description: 'Observation result',
    fields: [{ number: 5, name: 'Observation Value' }],
  },
]

function ctxFor(text: string) {
  // A minimal stand-in for CodeMirror's CompletionContext. Only matchBefore and pos are
  // used, so faking those is honest rather than a shortcut - the alternative is mounting
  // an editor to test a pure function.
  return {
    pos: text.length,
    matchBefore(re: RegExp) {
      const m = text.match(new RegExp(re.source + '$'))
      if (!m) return null
      return { from: text.length - m[0].length, to: text.length, text: m[0] }
    },
  } as never
}

describe('toDiagnostics', () => {
  it('places the marker on the reported line', () => {
    const source = 'var a = 1;\nvar b = ;\nvar c = 3;'
    const check: ScriptCheck = {
      ok: false,
      kind: 'transformer',
      language: 'javascript',
      rewritten: false,
      notes: [],
      error: { line: 2, column: 9, message: 'Unexpected token' },
    }

    const [d] = toDiagnostics(check, source)
    expect(d).toBeDefined()
    // Line 2 starts at offset 11; column 9 is offset 19.
    expect(d!.from).toBe(19)
    expect(d!.to).toBeGreaterThan(d!.from)
    expect(d!.message).toBe('Unexpected token')
  })

  it('returns nothing when the script is clean', () => {
    const check: ScriptCheck = { ok: true, language: 'javascript', kind: 'transformer', rewritten: false, notes: [] }
    expect(toDiagnostics(check, 'var a = 1;')).toEqual([])
  })

  it('still marks something when there is no position', () => {
    // A marker placed past the end of the document silently disappears, which looks
    // exactly like the error having gone away.
    const check: ScriptCheck = {
      ok: false,
      kind: 'transformer',
      language: 'javascript',
      rewritten: false,
      notes: [],
      error: { line: 0, message: 'something went wrong' },
    }
    const out = toDiagnostics(check, 'var a = 1;')
    expect(out).toHaveLength(1)
    expect(out[0]!.from).toBe(0)
  })

  it('does not run past the end of the document', () => {
    // The server reports a line from the translated source, which can be longer than what
    // the author is looking at.
    const check: ScriptCheck = {
      ok: false,
      kind: 'transformer',
      language: 'javascript',
      rewritten: false,
      notes: [],
      error: { line: 99, column: 4, message: 'far away' },
    }
    const source = 'var a = 1;'
    const out = toDiagnostics(check, source)
    expect(out).toHaveLength(1)
    expect(out[0]!.to).toBeLessThanOrEqual(source.length)
  })

  it('clamps a column past the end of its line', () => {
    const source = 'ab\ncd'
    const check: ScriptCheck = {
      ok: false,
      kind: 'transformer',
      language: 'javascript',
      rewritten: false,
      notes: [],
      error: { line: 2, column: 50, message: 'past the end' },
    }
    const out = toDiagnostics(check, source)
    expect(out[0]!.from).toBeLessThanOrEqual(source.length)
    expect(out[0]!.to).toBeLessThanOrEqual(source.length)
  })
})

describe('buildCompletions', () => {
  it('offers every segment with its description', () => {
    const sets = buildCompletions(dict)
    expect(sets.segments.map((s) => s.label)).toEqual(['PID', 'OBX'])
    expect(sets.segments[0]!.detail).toBe('Patient identification')
  })

  it('labels fields the way a script writes them', () => {
    // PID.5, not PID-5. Completing into the wrong notation would be worse than not
    // completing at all.
    const sets = buildCompletions(dict)
    const fields = sets.fieldsBySegment.get('PID')!
    expect(fields.map((f) => f.label)).toEqual(['PID.3', 'PID.5', 'PID.7', 'PID.8'])
  })

  it('carries the detail that stops the wrong field being used', () => {
    const sets = buildCompletions(dict)
    const fields = sets.fieldsBySegment.get('PID')!

    const dob = fields.find((f) => f.label === 'PID.7')!
    expect(dob.detail).toContain('Birth')

    const sex = fields.find((f) => f.label === 'PID.8')!
    expect(sex.detail).toContain('Administrative Sex')
    expect(sex.detail).toContain('0001')

    const ids = fields.find((f) => f.label === 'PID.3')!
    expect(ids.detail).toContain('repeats')
  })

  it('exposes components as info', () => {
    const sets = buildCompletions(dict)
    const name = sets.fieldsBySegment.get('PID')!.find((f) => f.label === 'PID.5')!
    expect(name.info).toContain('Family')
  })
})

describe('completeHL7', () => {
  const sets = buildCompletions(dict)

  it('offers segments inside a bracket subscript', () => {
    const res = completeHL7(ctxFor("msg['"), sets)
    expect(res).not.toBeNull()
    expect(res!.options.map((o) => o.label)).toContain('PID')
  })

  it('narrows to one segment once a dot is typed', () => {
    const res = completeHL7(ctxFor("msg['PID."), sets)
    expect(res).not.toBeNull()
    const labels = res!.options.map((o) => o.label)
    expect(labels).toContain('PID.7')
    expect(labels).not.toContain('OBX.5')
  })

  it('is case insensitive on the segment name', () => {
    const res = completeHL7(ctxFor("msg['pid."), sets)
    expect(res).not.toBeNull()
    expect(res!.options.map((o) => o.label)).toContain('PID.7')
  })

  it('replaces from the start of the typed text', () => {
    // Getting this wrong appends rather than replaces, producing msg['PIDPID.7'].
    const text = "msg['PID."
    const res = completeHL7(ctxFor(text), sets)
    expect(res!.from).toBe(text.length - 'PID.'.length)
  })

  it('offers nothing outside a subscript', () => {
    // Fifty HL7 segment names while somebody types a variable name would make the editor
    // worse than one with no completion at all.
    for (const text of ['var x = ', 'msg.', "logger.info('", 'PID', '']) {
      expect(completeHL7(ctxFor(text), sets)).toBeNull()
    }
  })

  it('offers nothing for an unknown segment', () => {
    expect(completeHL7(ctxFor("msg['ZZZ."), sets)).toBeNull()
  })

  it('offers nothing when the dictionary failed to load', () => {
    // Completion degrades rather than throwing, because the editor is still useful
    // without it.
    const empty = buildCompletions([])
    expect(completeHL7(ctxFor("msg['"), empty)).toBeNull()
  })

  it('works with a double-quoted subscript', () => {
    const res = completeHL7(ctxFor('msg["PID.'), sets)
    expect(res).not.toBeNull()
    expect(res!.options.map((o) => o.label)).toContain('PID.7')
  })
})
