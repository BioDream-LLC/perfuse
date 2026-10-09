import { describe, expect, it } from 'vitest'
import { TAB_IDS, viewFromHash } from './tabHash'

describe('viewFromHash', () => {
  // #/cms0057 was never read back because the pattern allowed letters only, so a link, a reload or Back landed on the dashboard.
  it('reads every view back out of its own address', () => {
    for (const id of TAB_IDS) expect(viewFromHash(`#/${id}`)).toBe(id)
  })
  it('names no view for anything else', () => {
    for (const h of ['', '#/', '#/nope', '#/users/1', '#users', '#/USERS']) expect(viewFromHash(h)).toBeNull()
  })
})
