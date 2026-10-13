import { describe, expect, it } from 'vitest'
import { formatGroups, parseGroups } from './groups'

describe('channel groups with a role each', () => {
  it('reads groups and the roles given to some of them', () => {
    expect(parseGroups('lab=editor, adt ,, lab')).toEqual({ groups: ['lab', 'adt'], roles: { lab: 'editor' } })
  })
  it('refuses a role that does not exist, and platform', () => {
    expect(parseGroups('lab=owner').bad).toMatch(/not a role/)
    expect(parseGroups('lab=platform').bad).toMatch(/not a role/)
  })
  it('writes back what it reads', () => {
    expect(formatGroups(['lab', 'adt'], { lab: 'editor' })).toBe('lab=editor, adt')
  })
})
