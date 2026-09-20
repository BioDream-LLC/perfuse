import { describe, expect, it } from 'vitest'

import { draftToWire, emptyDraft } from './model'

// Declarative steps on a SCRIPT channel go in the script block, not the hl7v3 one.
//
// The same v3Steps array feeds both, because a prescription and a v3 document are both XML addressed by the
// same path grammar. What must not happen is a SCRIPT channel emitting an hl7v3 block: the server refuses
// that, so the form would produce a file that will not load.
describe('script declarative steps', () => {
  const stepped = () => ({
    ...emptyDraft(),
    name: 'rx',
    dataType: 'script' as const,
    v3Steps: [
      {
        id: 's1',
        kind: 'set' as const,
        path: '//DrugDescription',
        value: 'REDACTED',
        from: '',
        to: '',
        table: '',
        onMissing: 'keep' as const,
        reason: '',
        pattern: '',
        replacement: '',
        caseTo: 'upper' as const,
        when: '',
        description: '',
      },
    ],
  })

  it('emits the steps under script.transformations', () => {
    const wire = draftToWire(stepped()) as Record<string, any>

    expect(wire.script?.transformations).toHaveLength(1)
    expect(wire.script.transformations[0].set.path).toBe('//DrugDescription')
  })

  it('does not emit an hl7v3 block for a SCRIPT channel', () => {
    const wire = draftToWire(stepped()) as Record<string, any>

    // The server refuses an hl7v3 block on a SCRIPT channel, so emitting one would make the form
    // produce a channel file that cannot load - which is worse than not offering the steps at all.
    expect(wire.hl7v3).toBeUndefined()
  })

  it('keeps emitting v3 steps under hl7v3 for a v3 channel', () => {
    const wire = draftToWire({ ...stepped(), dataType: 'hl7v3' as const }) as Record<string, any>

    expect(wire.hl7v3?.transformations).toHaveLength(1)
    expect(wire.script).toBeUndefined()
  })

  it('emits no script block when there are no steps', () => {
    const wire = draftToWire({ ...stepped(), v3Steps: [] }) as Record<string, any>

    expect(wire.script).toBeUndefined()
  })
})
