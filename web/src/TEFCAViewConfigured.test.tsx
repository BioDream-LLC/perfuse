/**
 * Tests for TEFCAView.tsx, centred on the configured branch.
 *
 * Why a component test rather than an end-to-end one. TEFCA participation is built from settings when the server starts, so
 * s.TEFCA is fixed for the lifetime of the process. The end-to-end harness shares one server across the whole suite and cannot
 * restart it, which leaves the configured branch of this screen unreachable there - and that is the branch that carries the
 * notice saying exchange is not implemented. The specs in e2e/use-tefca.spec.ts cover the unconfigured branch through the real
 * server; this file covers the other one.
 *
 * The pairing matters. internal/api/tefca_test.go asserts that a configured participant reports exchangeImplemented false with
 * an explanation naming the gap; this asserts the screen turns that into something an operator reads. Either half alone leaves
 * the honesty true but invisible.
 *
 * @vitest-environment happy-dom
 */
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, waitFor, cleanup, within } from '@testing-library/react'
import { TEFCAView } from './TEFCAView'

/** A configured participant, as the server describes one. */
function configuredStatus(overrides: Record<string, unknown> = {}) {
  return {
    configured: true,
    organisation: 'Example Hospital',
    oid: '2.16.840.1.113883.19.5',
    qhinEndpoint: 'https://qhin.example/fhir',
    participantType: 'provider',
    purposes: ['treatment'],
    allPurposes: ['treatment', 'payment', 'operations', 'public-health', 'individual-access'],
    trail: { path: '/tmp/audit.jsonl', writable: true },
    exchangeImplemented: false,
    exchangeExplanation:
      'tefca: this build has no QHIN transport, so nothing was exchanged. Purpose-of-use checking, configuration ' +
      'validation and the audit trail are real and work; the network call is not implemented.',
    ...overrides,
  }
}

/** An audit page with nothing in it, in the shape the view reads.
 *
 * Written out in full rather than approximated. The totals live under summary, and a page missing it threw inside the trail
 * component, which unmounted the whole tree - so the test reported "the notice is not on screen" when nothing at all was on
 * screen. An incomplete stub fails as a missing feature. */
function emptyAuditPage(configured: boolean) {
  return {
    configured,
    entries: [],
    total: 0,
    truncated: false,
    from: new Date(0).toISOString(),
    to: new Date().toISOString(),
    summary: { total: 0, failed: 0, byType: {}, byPurpose: {}, worstOrg: '', worstCount: 0, withoutPurpose: 0 },
  }
}

/** mockFetch answers the endpoints this view reads, and fails loudly on anything else. */
function mockFetch(status: Record<string, unknown>) {
  return vi.fn().mockImplementation(async (url: string) => {
    // The audit page has to be answered with the shape the view reads, not an approximation. An empty object renders and then
    // throws on the first summary number, which reports as a mysterious render failure rather than as an incomplete stub.
    const body = url.includes('/api/tefca/status') ? status : emptyAuditPage(status.configured === true)
    const text = JSON.stringify(body)

    return { ok: true, status: 200, json: async () => body, text: async () => text }
  })
}

describe('TEFCAView, configured', () => {
  let originalFetch: typeof globalThis.fetch

  beforeEach(() => {
    originalFetch = globalThis.fetch
  })

  afterEach(() => {
    globalThis.fetch = originalFetch
    cleanup()
    vi.restoreAllMocks()
  })

  it('says which transport is missing, and does not claim the other one is proven', async () => {
    // The claim an operator would otherwise make wrongly, in both directions.
    //
    // This screen used to carry one notice saying no exchange was possible at all. That became wrong when Facilitated FHIR was built,
    // and the replacement has to be careful in the other direction too: "implemented" would be read as "working with our partners",
    // which it is not - the security layer is verified against a reference server and has never spoken to a real QHIN.
    globalThis.fetch = mockFetch(configuredStatus())

    const { container } = render(<TEFCAView />)

    await waitFor(() => {
      expect(within(container).getByText(/IHE profiles is not implemented/i)).toBeTruthy()
    })

    const text = container.textContent ?? ''

    expect(text, 'the notice does not say what still works, so the only reasonable action looks like switching it off').toMatch(
      /audit trail/i,
    )
    expect(text, 'the notice does not say an attempt is refused rather than reported as done').toMatch(/refused/i)
  })

  it('says Facilitated FHIR exists and has never reached a real QHIN', async () => {
    // Implemented is not the same as proven, and this screen is where somebody decides whether to rely on it. A notice saying only
    // that it is implemented would be read as a working exchange.
    globalThis.fetch = mockFetch(configuredStatus({ facilitatedFHIRImplemented: true }))

    const { container } = render(<TEFCAView />)

    await waitFor(() => {
      expect(within(container).getByText(/Facilitated FHIR is implemented/i)).toBeTruthy()
    })

    const text = container.textContent ?? ''

    expect(text, 'the screen does not say it has never spoken to a real QHIN').toMatch(/never spoken to a real QHIN/i)
    expect(text, 'the screen does not say what the verification actually was').toMatch(/reference server/i)
    expect(text, 'the screen does not say what is still needed').toMatch(/onboarding/i)
  })

  it('stops saying either thing once the server stops reporting it', async () => {
    // The other direction, so this pair cannot outlive the gap it describes. When a transport arrives the server stops sending false,
    // and a notice left hard-coded into the screen would then be a lie that is harder to notice - because nobody investigates a
    // warning claiming a working feature does not work.
    globalThis.fetch = mockFetch(
      configuredStatus({
        exchangeImplemented: true,
        exchangeExplanation: '',
        facilitatedFHIRImplemented: false,
      }),
    )

    const { container } = render(<TEFCAView />)

    await waitFor(() => {
      expect(within(container).getByText(/Example Hospital/)).toBeTruthy()
    })

    const text = container.textContent ?? ''

    expect(text).not.toMatch(/IHE profiles is not implemented/i)
    expect(text).not.toMatch(/Facilitated FHIR is implemented/i)
  })
})
