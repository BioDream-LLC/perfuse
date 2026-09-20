/**
 * @vitest-environment happy-dom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { DICOMTransformations } from './DICOMTransformations'
import { draftToWire, emptyDraft, newDICOMStep } from './model'

// Named imaging steps, from the form to the wire.
//
// internal/dicom carried these four actions with no caller at all for a while - the steps, dicom.Apply and
// tests, five hundred lines nothing referenced. So the assertions here are about reachability rather than
// about what each action does, which the Go tests already cover.

afterEach(cleanup)

describe('the imaging step editor', () => {
  const imaging = () => ({ ...emptyDraft(), dataType: 'dicom' as const })

  it('offers the four named actions and no path field', () => {
    render(<DICOMTransformations draft={imaging()} set={() => {}} />)

    expect(screen.getByRole('button', { name: '+ Remove the patient' })).toBeTruthy()
    expect(screen.getByRole('button', { name: '+ Strip private tags' })).toBeTruthy()
    expect(screen.getByRole('button', { name: '+ Rewrite the AE titles' })).toBeTruthy()
    expect(screen.getByRole('button', { name: '+ Set the institution' })).toBeTruthy()

    // The absence is the design. A path writer over a binary object can corrupt the image, so there is
    // deliberately no way to address an arbitrary tag - and a test asserting the four buttons exist would
    // still pass if one had been added.
    expect(screen.queryByLabelText(/^path$/i)).toBeNull()
  })

  it('shows only the fields the chosen action uses', () => {
    const draft = {
      ...imaging(),
      dicomSteps: [{ ...newDICOMStep('stripPrivate') }],
    }

    render(<DICOMTransformations draft={draft} set={() => {}} />)

    expect(screen.getByLabelText(/private tags to keep/i)).toBeTruthy()

    // A de-identify field on a strip-private step would put a key in the channel file that the action
    // ignores, and whoever read it later would reasonably think it did something.
    expect(screen.queryByLabelText(/replacement patient name/i)).toBeNull()
  })
})

describe('imaging steps on the wire', () => {
  const withStep = (step: ReturnType<typeof newDICOMStep>) => ({
    ...emptyDraft(),
    name: 'imaging',
    dataType: 'dicom' as const,
    dicomSteps: [step],
  })

  it('emits a de-identify step under dicom.transformations', () => {
    const wire = draftToWire(withStep(newDICOMStep('deidentify'))) as Record<string, any>

    expect(wire.dicom?.transformations).toHaveLength(1)
    expect(wire.dicom.transformations[0].deidentify).toBeDefined()
  })

  it('takes one tag per line, because a tag contains a comma', () => {
    // This test used to assert the opposite, and asserting it is what made the bug survive review: the form
    // split on commas, so a person typing the two tags 0009,0010 and 0029,1010 produced four entries, none of
    // which is a tag, and the server refused the file with an error naming a value they never typed.
    const draft = {
      ...emptyDraft(),
      dataType: 'dicom' as const,
      dicomSteps: [{ ...newDICOMStep('stripPrivate'), keep: '0009,0010\n0029,1010' }],
    }

    const wire = draftToWire(draft) as Record<string, any>
    const keep = wire.dicom.transformations[0].stripPrivate.keep

    expect(keep).toEqual(['0009,0010', '0029,1010'])
  })

  it('sends only the chosen action', () => {
    const step = { ...newDICOMStep('setAeTitle'), calling: 'PERFUSE', patientName: 'IGNORED' }
    const wire = draftToWire(withStep(step)) as Record<string, any>

    const emitted = wire.dicom.transformations[0]
    expect(emitted.setAeTitle.calling).toBe('PERFUSE')
    expect(emitted.deidentify).toBeUndefined()
  })

  it('emits no dicom block on another format', () => {
    const draft = { ...withStep(newDICOMStep('deidentify')), dataType: 'hl7' as const }

    // The server refuses a dicom block on an HL7 channel, so the form must not write one.
    expect((draftToWire(draft) as Record<string, any>).dicom).toBeUndefined()
  })
})

describe('imaging steps survive a round trip', () => {
  // Opening an existing channel in the form and saving it must not lose its steps.
  //
  // This was the gap when the block was first added: draftToWire emitted the steps and wireToDraft ignored
  // them, so a channel edited through the form came back without its transformations. For a de-identify step
  // that means the next object leaves carrying the patient, and nothing reports that a step was dropped -
  // the form saved successfully, the file is valid, and the protection is gone.
  it('reads back what it wrote', async () => {
    const { wireToDraft } = await import('./wireToDraft')

    const original = {
      ...emptyDraft(),
      name: 'imaging',
      dataType: 'dicom' as const,
      dicomSteps: [
        { ...newDICOMStep('deidentify'), patientId: 'ANON-1', keepDates: true },
        { ...newDICOMStep('stripPrivate'), keep: '0009,0010\n0029,1010' },
      ],
    }

    const wire = draftToWire(original) as Record<string, any>
    const back = wireToDraft(wire)

    expect(back.dicomSteps).toHaveLength(2)

    expect(back.dicomSteps[0]?.kind).toBe('deidentify')
    expect(back.dicomSteps[0]?.patientId).toBe('ANON-1')
    expect(back.dicomSteps[0]?.keepDates).toBe(true)

    expect(back.dicomSteps[1]?.kind).toBe('stripPrivate')
    // One tag per line, unchanged by the trip. The earlier version of this assertion expected
    // '0009, 0010, 0029, 1010' - four entries from two tags - which is how the round trip test managed to
    // pass while the form produced a file the server refuses.
    expect(back.dicomSteps[1]?.keep).toBe('0009,0010\n0029,1010')
  })
})
