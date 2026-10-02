// @vitest-environment happy-dom
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, cleanup, waitFor } from '@testing-library/react'
import { BrandingProvider, SiteLogo } from './Branding'

// The site's logo appears where a claim of ownership belongs, and nowhere else.
//
// # The property that matters
//
// SiteLogo must render nothing at all when no logo has been uploaded. Every placement is unconditional in the markup, so if this
// fell back to the built-in mark the way BrandMark does, an unbranded installation would grow a droplet at the top of the
// dashboard, the metrics view and the shadow report - decoration in three places, replacing headings that were fine.
//
// # Why both directions
//
// The first version of this file asserted only the absent case, twice, and passed. That proves nothing: a component that always
// returns null passes it, and so does one that is never rendered. The showing case is the control that gives the hiding case its
// meaning, so the two are written together and each would fail if the other were the only behaviour.

function stubBranding(logoVersion?: string) {
  globalThis.fetch = vi.fn().mockResolvedValue({
    ok: true,
    json: async () => ({
      productName: 'Mercy Health',
      tagline: 'Integration',
      logoVersion,
      customised: true,
    }),
  }) as never
}

describe('the site logo', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('shows the logo and the name once a logo has been uploaded', async () => {
    stubBranding('v7')

    const { container } = render(
      <BrandingProvider>
        <SiteLogo />
      </BrandingProvider>,
    )

    await waitFor(() => {
      expect(container.querySelector('img')).not.toBeNull()
    })

    // The version travels in the URL so a replaced logo appears immediately rather than being served from cache, which is the
    // difference between an upload that looks like it worked and one that looks like it silently failed.
    expect(container.querySelector('img')?.getAttribute('src')).toContain('v=v7')
    expect(container.textContent).toContain('Mercy Health')
  })

  it('renders nothing at all when no logo has been uploaded', async () => {
    stubBranding(undefined)

    const { container } = render(
      <BrandingProvider>
        <SiteLogo />
      </BrandingProvider>,
    )

    // Waits for the fetch to settle first, so this is not passing merely because branding had not arrived yet.
    await waitFor(() => {
      expect(vi.mocked(globalThis.fetch)).toHaveBeenCalled()
    })

    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('svg'), 'the built-in mark must not stand in for a missing logo').toBeNull()
    expect(container.textContent).toBe('')
  })
})
