/**
 * @vitest-environment happy-dom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { FindPatient } from './FindPatient'
import { api, type IdentitySearchResult } from './api'

// An empty patient search has two meanings and they are opposite.
//
// "No message mentions this patient" is a statement about the traffic. "Nothing has been indexed"
// is a statement about a settings toggle. Showing the first when the second is true tells somebody
// a patient was never seen here, which is a clinical conclusion drawn from configuration - and it
// is the failure that will actually happen, because indexing only covers messages recorded after it
// was switched on.

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

function result(over: Partial<IdentitySearchResult>): IdentitySearchResult {
  return { matches: [], total: 0, indexed: true, kinds: [], ...over }
}

async function searchFor(term: string, res: IdentitySearchResult) {
  vi.spyOn(api, 'findMessages').mockResolvedValue(res)

  render(<FindPatient />)

  const box = screen.getByPlaceholderText(/an MRN, a name/i) as HTMLInputElement
  const { fireEvent } = await import('@testing-library/react')
  fireEvent.change(box, { target: { value: term } })
  fireEvent.click(screen.getByRole('button', { name: /find/i }))

  // The panel renders from state set in a promise, so wait for it rather than asserting
  // immediately - an assertion that runs first passes against an empty panel whatever the
  // component would eventually say.
  const { waitFor } = await import('@testing-library/react')
  await waitFor(() => {
    expect(document.body.textContent).toMatch(/not being indexed|mention/i)
  })
}

describe('an empty patient search', () => {
  it('says the index is off rather than that the patient was never seen', async () => {
    await searchFor('MRN0012345', result({ indexed: false }))

    // The distinction, stated. Without this somebody reads "no messages" as an answer about
    // the patient.
    expect(document.body.textContent).toMatch(/not being indexed/i)
    expect(document.body.textContent).toMatch(/does not mean/i)

    // And it must not make the claim it cannot support.
    expect(document.body.textContent).not.toMatch(/^No message mentions/im)
  })

  it('says no message mentions the patient when the index is on', async () => {
    await searchFor('MRN0012345', result({ indexed: true }))

    expect(document.body.textContent).toMatch(/No message mentions/i)
    expect(document.body.textContent).not.toMatch(/not being indexed/i)
  })
})

describe('a patient search that found something', () => {
  it('shows which identifier matched, not only that one did', async () => {
    await searchFor(
      'MRN0012345',
      result({
        total: 1,
        indexed: true,
        matches: [
          {
            message: {
              id: 7,
              channel: 'adt',
              receivedAt: '2026-09-21T12:00:00Z',
              controlID: 'FIND001',
              messageType: 'ADT',
              triggerEvent: 'A01',
              sender: 'EPIC',
              remote: '',
              outcome: 'Delivered',
              ackCode: 'AA',
              size: 120,
              segments: 2,
              durationMs: 3,
              error: '',
            } as never,
            matchedKind: 'account',
            matchedValue: 'VISIT778899',
          },
        ],
      }),
    )

    // The reason the row is in the list. A page of otherwise identical ADT messages gives no clue
    // otherwise, and "it matched the account number, not the MRN" changes what somebody does next.
    expect(document.body.textContent).toMatch(/Account or visit/i)
    expect(document.body.textContent).toMatch(/VISIT778899/)

    // Named in the interface's words rather than the database's.
    expect(document.body.textContent).not.toMatch(/patient_id|birth_date|study_uid/)
  })
})
