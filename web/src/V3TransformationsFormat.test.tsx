/**
 * @vitest-environment happy-dom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { V3Transformations } from './V3Transformations'
import { emptyDraft } from './model'

// The step editor serves both v3 and SCRIPT, and withholds one action from SCRIPT.
//
// A component test rather than an e2e one, because the e2e suite runs against an externally served build of
// the assets and cannot see a source change until that is rebuilt and restarted. This runs from source, so it
// is the check that actually holds when the code changes.
//
// nullflavor states why a v3 value is absent. A prescription has no equivalent and the server refuses the
// step, so the form must not offer it - a control that produces a channel file which will not load is worse
// than a control that is not there, because the person using it cannot tell the form was wrong.

afterEach(cleanup)

describe('the step editor by message format', () => {
  const draftFor = (dataType: 'hl7v3' | 'script') => ({ ...emptyDraft(), dataType })

  it('offers nullflavor on a v3 channel', () => {
    render(<V3Transformations draft={draftFor('hl7v3')} set={() => {}} />)

    expect(screen.getByRole('button', { name: '+ Say why there is no value' })).toBeTruthy()
  })

  it('withholds nullflavor on a prescription channel', () => {
    render(<V3Transformations draft={draftFor('script')} set={() => {}} />)

    expect(screen.queryByRole('button', { name: '+ Say why there is no value' })).toBeNull()
  })

  it('still offers the actions that carry over', () => {
    render(<V3Transformations draft={draftFor('script')} set={() => {}} />)

    // Without these the test above would pass against an editor that rendered nothing at all.
    expect(screen.getByRole('button', { name: '+ Set a value' })).toBeTruthy()
    expect(screen.getByRole('button', { name: '+ Remove the element' })).toBeTruthy()
    expect(screen.getByRole('button', { name: '+ Empty the value' })).toBeTruthy()
  })
})
