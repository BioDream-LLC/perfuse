import { describe, expect, it } from 'vitest'
import { draftToWire, emptyDraft, newDestination, newStep, type ChannelDraft } from './model'
import { RECIPES } from './builderRecipes'
import { describePath } from './BuilderSteps'
import type { DictSegment } from './api'

// The browser no longer writes YAML, so what is worth testing here is the mapping from form
// state onto the server's model. A field dropped in this translation is invisible: the
// generated file simply does not carry the setting, and the form still looks right.

function wire(draft: ChannelDraft): Record<string, any> {
  return draftToWire(draft) as Record<string, any>
}

describe('draftToWire', () => {
  it('sends only the source block for the chosen transport', () => {
    // Carrying the others would configure transports the channel does not use, and the
    // loader refuses unknown combinations rather than ignoring them.
    const mllp = wire({ ...emptyDraft(), sourceKind: 'mllp', listen: ':6661' })
    expect(mllp.source.type).toBe('mllp')
    expect(mllp.source.listen).toBe(':6661')
    expect(mllp.source.http).toBeUndefined()
    expect(mllp.source.database).toBeUndefined()
    expect(mllp.source.sftp).toBeUndefined()

    const http = wire({ ...emptyDraft(), sourceKind: 'http' })
    expect(http.source.type).toBe('http')
    expect(http.source.http.listen).toBeTruthy()
    expect(http.source.listen).toBeUndefined()
  })

  it('carries every source transport', () => {
    // The gap this work closed: the form could previously only describe MLLP, so anything
    // else had to be written by hand.
    for (const kind of ['mllp', 'http', 'database', 'sftp'] as const) {
      const out = wire({ ...emptyDraft(), sourceKind: kind })
      expect(out.source.type).toBe(kind)
    }
  })

  it('omits an acknowledgement block on an X12 channel', () => {
    // X12 has no synchronous acknowledgement at all, so sending ack settings would
    // configure something that cannot happen. The loader refuses it rather than ignoring
    // it, which means this has to be right.
    const out = wire({ ...emptyDraft(), dataType: 'x12', sourceKind: 'http' })
    expect(out.source.ack).toBeUndefined()
    expect(out.dataType).toBe('x12')
    expect(out.x12).toEqual({ envelope: 'require', split: true })
  })

  it('omits the x12 block entirely on an HL7 channel', () => {
    const out = wire(emptyDraft())
    expect(out.dataType).toBeUndefined()
    expect(out.x12).toBeUndefined()
  })

  it('sends only the action matching each step kind', () => {
    // A step carrying two actions has no defined meaning and the loader refuses it.
    const kinds = [
      'set',
      'copy',
      'map',
      'replace',
      'clear',
      'remove',
      'trim',
      'case',
      'pad',
      'date',
    ] as const

    for (const kind of kinds) {
      const out = wire({ ...emptyDraft(), steps: [{ ...newStep(kind), path: 'PID-8' }] })
      const step = out.transformations[0]

      const actions = kinds.filter((k) => step[k] !== undefined)
      expect(actions).toEqual([kind])
    }
  })

  it('drops a half-typed translation row', () => {
    // An empty code is somebody mid-keystroke, not a translation of the empty string.
    // Sending it would make the table claim something nobody meant.
    const step = { ...newStep('map'), path: 'PID-8' }
    step.table = [
      ['1', 'M'],
      ['', ''],
      ['2', 'F'],
    ]
    const out = wire({ ...emptyDraft(), steps: [step] })
    expect(out.transformations[0].map.table).toEqual({ '1': 'M', '2': 'F' })
  })

  it('translates the three unmatched-value choices distinctly', () => {
    const base = { ...newStep('map'), path: 'PID-8' }
    base.table = [['1', 'M']]

    const keep = wire({ ...emptyDraft(), steps: [{ ...base, unmatched: 'keep' }] })
    expect(keep.transformations[0].map.strict).toBeUndefined()
    expect(keep.transformations[0].map.default).toBeUndefined()

    const fallback = wire({
      ...emptyDraft(),
      steps: [{ ...base, unmatched: 'default', fallback: 'U' }],
    })
    expect(fallback.transformations[0].map.default).toBe('U')
    expect(fallback.transformations[0].map.strict).toBeUndefined()

    const strict = wire({ ...emptyDraft(), steps: [{ ...base, unmatched: 'strict' }] })
    expect(strict.transformations[0].map.strict).toBe(true)
    expect(strict.transformations[0].map.default).toBeUndefined()
  })

  it('sends the FHIR identifier system', () => {
    // This had no field at all until now, which meant a FHIR destination built from the
    // form could never validate: the loader requires it, because an MRN on its own is
    // ambiguous between facilities.
    const dest = {
      ...newDestination(),
      name: 'fhir',
      type: 'fhir' as const,
      url: 'https://fhir.example.org/fhir',
      fhirIdentifierSystem: 'urn:oid:1.2.3',
    }
    const out = wire({ ...emptyDraft(), destinations: [dest] })
    expect(out.destinations[0].fhir.defaultIdentifierSystem).toBe('urn:oid:1.2.3')
  })

  it('omits an empty description rather than sending a blank one', () => {
    const out = wire({ ...emptyDraft(), description: '   ' })
    expect(out.description).toBeUndefined()
  })

  it('states enabled only when it is false', () => {
    // The channel default is true, so an absent key should mean the default. Writing
    // "enabled: true" into every generated file is noise in a diff.
    expect(wire({ ...emptyDraft(), enabled: true }).enabled).toBeUndefined()
    expect(wire({ ...emptyDraft(), enabled: false }).enabled).toBe(false)
  })

  it('renders durations as strings, not numbers', () => {
    // Emitted as nanoseconds they are valid and unreadable.
    const out = wire({ ...emptyDraft(), sourceKind: 'sftp', sftpPollSeconds: 30 })
    expect(out.source.sftp.pollInterval).toBe('30s')
  })

  it('omits a zero duration entirely', () => {
    const out = wire({ ...emptyDraft(), idleTimeoutSeconds: 0 })
    expect(out.source.idleTimeout).toBeUndefined()
  })

  it('never sends the client-side step id', () => {
    // The server refuses unknown fields, correctly: an unknown key in a channel file is
    // how a setting silently does nothing.
    const out = wire({ ...emptyDraft(), steps: [{ ...newStep('trim'), path: 'PID-5' }] })
    expect(out.transformations[0].id).toBeUndefined()
  })

  it('never sends the client-side destination id', () => {
    const out = wire(emptyDraft())
    expect(out.destinations[0].id).toBeUndefined()
  })
})

describe('recipes', () => {
  it('every recipe has a name and at least one destination', () => {
    // A starting point that does not validate teaches nothing, because the first thing it
    // does is show an error. The blank recipe is the deliberate exception.
    for (const r of RECIPES) {
      const draft = r.build()
      if (r.id === 'blank') continue

      expect(draft.name, `${r.id} has no name`).toBeTruthy()
      expect(draft.destinations.length, `${r.id} has no destination`).toBeGreaterThan(0)
      for (const d of draft.destinations) {
        expect(d.name, `${r.id} has an unnamed destination`).toBeTruthy()
      }
    }
  })

  it('the X12 recipe does not use MLLP', () => {
    // MLLP is an HL7 transport and the loader refuses it on an X12 channel, so a recipe
    // pairing the two would open showing an error.
    const x12 = RECIPES.find((r) => r.id === 'x12')!.build()
    expect(x12.dataType).toBe('x12')
    expect(x12.sourceKind).not.toBe('mllp')
  })

  it('the FHIR recipe sets an identifier system', () => {
    const fhir = RECIPES.find((r) => r.id === 'fhir')!.build()
    expect(fhir.destinations[0]!.fhirIdentifierSystem).toBeTruthy()
  })

  it('every recipe explains itself in a sentence', () => {
    for (const r of RECIPES) {
      expect(r.blurb.length, `${r.id} barely explains itself`).toBeGreaterThan(40)
      expect(r.label.length).toBeGreaterThan(3)
    }
  })

  it('recipe ids are unique', () => {
    const ids = RECIPES.map((r) => r.id)
    expect(new Set(ids).size).toBe(ids.length)
  })
})

const dict: DictSegment[] = [
  {
    segment: 'PID',
    description: 'Patient identification',
    fields: [
      { number: 5, name: 'Patient Name', components: ['Family Name', 'Given Name'] },
      { number: 7, name: 'Date/Time of Birth' },
      { number: 8, name: 'Administrative Sex', table: '0001' },
      { number: 3, name: 'Patient Identifier List', repeats: true },
    ],
  },
]

describe('describePath', () => {
  it('names the field so a wrong number is obvious', () => {
    // The reason this exists: PID-7 and PID-8 are one keystroke apart and mean entirely
    // different things.
    expect(describePath('PID-7', dict)).toContain('Birth')
    expect(describePath('PID-8', dict)).toContain('Administrative Sex')
  })

  it('accepts the three ways people write a path', () => {
    for (const written of ['PID-7', 'PID.7', 'PID7']) {
      expect(describePath(written, dict), written).toContain('Birth')
    }
  })

  it('resolves a component', () => {
    // PID-5.1 is the family name and PID-5.2 the given name, which is a distinction people
    // get wrong constantly.
    expect(describePath('PID-5.1', dict)).toContain('Family Name')
    expect(describePath('PID-5.2', dict)).toContain('Given Name')
  })

  it('mentions repetition and the code table', () => {
    expect(describePath('PID-3', dict)).toContain('may repeat')
    expect(describePath('PID-8', dict)).toContain('0001')
  })

  it('describes the segment when no field is given', () => {
    expect(describePath('PID', dict)).toBe('Patient identification')
  })

  it('says so when a field is not in the dictionary', () => {
    expect(describePath('PID-99', dict)).toContain('no field 99')
  })

  it('is quiet about anything it does not know', () => {
    // Silence rather than a guess. A confident wrong description is worse than none.
    expect(describePath('', dict)).toBeNull()
    expect(describePath('ZZZ-1', dict)).toBeNull()
    expect(describePath('!!', dict)).toBeNull()
  })

  it('is case insensitive', () => {
    expect(describePath('pid-7', dict)).toContain('Birth')
  })

  it('does not fall over with an empty dictionary', () => {
    expect(describePath('PID-7', [])).toBeNull()
  })
})
