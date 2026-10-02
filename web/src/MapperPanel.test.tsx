/**
 * Tests for MapperPanel.tsx.
 *
 * Verifies:
 * - Abstention is visually distinct from a low-confidence suggestion
 * - Confidence of 0 renders as "0" (not blank due to falsy-zero bug)
 * - Confidence ring SVG math is valid for 0 and 100
 *
 * @vitest-environment happy-dom
 */
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { MapperPanel } from './MapperPanel'

/** mockFetchWith stands in for the network, faithfully enough for the shared api client.
 *
 * It returns the body as text as well as JSON, because a real Response does and the client reads text so
 * it can attach the server's own message to a failure. The earlier version returned an empty string
 * there, which was adequate only for a component calling fetch directly - and calling fetch directly was
 * the defect: it sent no X-Perfuse-Request header, so the server refused every request and the panel had
 * never worked. */
function mockFetchWith(suggestions: unknown[]) {
  const body = JSON.stringify({ suggestions })
  return vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({ suggestions }),
    text: async () => body,
  })
}

describe('MapperPanel', () => {
  let originalFetch: typeof globalThis.fetch

  beforeEach(() => {
    originalFetch = globalThis.fetch
  })

  afterEach(() => {
    cleanup()
    globalThis.fetch = originalFetch
  })

  it('renders abstention badge when abstained is true', async () => {
    globalThis.fetch = mockFetchWith([
      {
        sourceField: 'ZPI-1',
        suggestions: [
          { target: 'PID-3.1', confidence: 30, reasoning: 'Low confidence match', abstained: true },
        ],
      },
    ]) as unknown as typeof fetch

    const { container } = render(<MapperPanel />)

    // Trigger the suggestion
    const button = container.querySelector('.btn-primary') as HTMLButtonElement
    fireEvent.click(button)

    await waitFor(() => {
      expect(container.textContent).toContain('abstained')
    })

    // The badge element should exist
    const badge = container.querySelector('.badge')
    expect(badge).not.toBeNull()
    expect(badge!.textContent).toBe('abstained')
  })

  it('visually distinguishes abstention from a low-confidence suggestion', async () => {
    globalThis.fetch = mockFetchWith([
      {
        sourceField: 'TestField',
        suggestions: [
          { target: 'PID-3.1', confidence: 30, reasoning: 'Low but not abstained', abstained: false },
          { target: 'PID-7', confidence: 30, reasoning: 'Abstained entirely', abstained: true },
        ],
      },
    ]) as unknown as typeof fetch

    const { container } = render(<MapperPanel />)
    const button = container.querySelector('.btn-primary') as HTMLButtonElement
    fireEvent.click(button)

    await waitFor(() => {
      expect(container.textContent).toContain('PID-3.1')
    })

    // Find the suggestion rows by their border+p-3 styling
    const rows = container.querySelectorAll('.mb-2.flex.items-center.gap-3.rounded-lg.border.p-3')
    expect(rows.length).toBe(2)

    // The first row (PID-3.1, not abstained) should NOT have amber
    const firstRow = rows[0]!
    expect(firstRow.textContent).toContain('PID-3.1')
    expect(firstRow.className).not.toContain('amber')

    // The second row (PID-7, abstained) SHOULD have amber
    const secondRow = rows[1]!
    expect(secondRow.textContent).toContain('PID-7')
    expect(secondRow.className).toContain('amber')

    // The abstained row should also have the badge
    const badge = secondRow.querySelector('.badge')
    expect(badge).not.toBeNull()
    expect(badge!.textContent).toBe('abstained')
  })

  it('renders confidence of 0 as "0", not blank', async () => {
    globalThis.fetch = mockFetchWith([
      {
        sourceField: 'Unknown',
        suggestions: [
          { target: 'PID-99', confidence: 0, reasoning: 'No match found', abstained: true },
        ],
      },
    ]) as unknown as typeof fetch

    const { container } = render(<MapperPanel />)
    const button = container.querySelector('.btn-primary') as HTMLButtonElement
    fireEvent.click(button)

    await waitFor(() => {
      expect(container.textContent).toContain('PID-99')
    })

    // Find the confidence display - should show "0", not be blank
    // The ConfidenceRing renders {confidence} inside a span
    const spans = container.querySelectorAll('span')
    const zeroSpan = Array.from(spans).find(
      (s) => s.textContent === '0' && s.className.includes('absolute'),
    )
    expect(zeroSpan).not.toBeNull()
  })

  it('confidence ring SVG is valid for 0 (empty ring)', async () => {
    globalThis.fetch = mockFetchWith([
      {
        sourceField: 'X',
        suggestions: [
          { target: 'Y', confidence: 0, reasoning: 'None', abstained: true },
        ],
      },
    ]) as unknown as typeof fetch

    const { container } = render(<MapperPanel />)
    const button = container.querySelector('.btn-primary') as HTMLButtonElement
    fireEvent.click(button)

    await waitFor(() => {
      expect(container.textContent).toContain('Y')
    })

    // Scoped to the ring's own svg rather than counted across the whole panel.
    //
    // This took every circle in the container and assumed index 1 was the progress arc. That held until the section heading gained an
    // icon, which contributed a circle of its own and shifted the numbering - so the test broke while the ring was entirely correct.
    // Indexing a global query by position is a claim about everything else on the page, which is not what this is testing.
    const ring = container.querySelector('svg.-rotate-90')
    expect(ring, 'the confidence ring svg was not found').not.toBeNull()

    const circles = ring!.querySelectorAll('circle')
    // Background arc plus progress arc.
    expect(circles.length).toBeGreaterThanOrEqual(2)

    // The progress circle should have valid stroke-dashoffset
    const progressCircle = circles[1]!
    const offset = progressCircle.getAttribute('stroke-dashoffset')
    expect(offset).not.toBeNull()
    expect(Number(offset)).not.toBeNaN()
    // For confidence=0, offset should equal the circumference (full offset = empty ring)
    const circumference = 2 * Math.PI * 18
    expect(Number(offset)).toBeCloseTo(circumference, 1)
  })

  it('confidence ring SVG is valid for 100 (full ring)', async () => {
    globalThis.fetch = mockFetchWith([
      {
        sourceField: 'X',
        suggestions: [
          { target: 'Y', confidence: 100, reasoning: 'Perfect', abstained: false },
        ],
      },
    ]) as unknown as typeof fetch

    const { container } = render(<MapperPanel />)
    const button = container.querySelector('.btn-primary') as HTMLButtonElement
    fireEvent.click(button)

    await waitFor(() => {
      expect(container.textContent).toContain('Perfect')
    })

    // Scoped to the ring's own svg rather than counted across the whole panel.
    //
    // This took every circle in the container and assumed index 1 was the progress arc. That held until the section heading gained an
    // icon, which contributed a circle of its own and shifted the numbering - so the test broke while the ring was entirely correct.
    // Indexing a global query by position is a claim about everything else on the page, which is not what this is testing.
    const ring = container.querySelector('svg.-rotate-90')
    expect(ring, 'the confidence ring svg was not found').not.toBeNull()

    const circles = ring!.querySelectorAll('circle')
    expect(circles.length).toBeGreaterThanOrEqual(2)

    // For confidence=100, offset should be 0 (full ring drawn)
    const progressCircle = circles[1]!
    const offset = progressCircle.getAttribute('stroke-dashoffset')
    expect(offset).not.toBeNull()
    expect(Number(offset)).toBeCloseTo(0, 1)
  })
})
