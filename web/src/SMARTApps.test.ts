import { describe, expect, it } from 'vitest'
import { splitList } from './SMARTApps'

describe('splitList', () => {
  it('reads scopes and URIs separated by spaces, commas or new lines', () => {
    expect(splitList(' openid  fhirUser,patient/*.rs\nlaunch ')).toEqual(['openid', 'fhirUser', 'patient/*.rs', 'launch'])
    expect(splitList('')).toEqual([])
  })
})
