import { describe, expect, it } from 'vitest'
import { dueText } from './PASReviewQueue'

describe('dueText', () => {
  const now = Date.parse('2026-10-08T12:00:00Z')
  it('says how long is left, in hours under two days', () => {
    expect(dueText('2026-10-09T12:00:00Z', now)).toEqual({ text: 'due in 24 hours', late: false })
    expect(dueText('2026-10-08T13:00:00Z', now)).toEqual({ text: 'due in 1 hour', late: false })
  })
  it('says days beyond two days, and when the deadline has passed', () => {
    expect(dueText('2026-10-15T12:00:00Z', now)).toEqual({ text: 'due in 7 days', late: false })
    expect(dueText('2026-10-05T12:00:00Z', now)).toEqual({ text: 'overdue by 3 days', late: true })
  })
})
