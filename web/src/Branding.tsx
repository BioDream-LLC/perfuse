import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'

/**
 * White-labelling.
 *
 * A site running this for their own customers needs it to look like their product. The name, an accent
 * colour and a logo all come from the server, and all three are needed before anyone signs in - the
 * sign-in page is the first thing a customer sees, so branding it is most of the point.
 *
 * The accent reaches the stylesheet through a CSS custom property rather than through inline styles on
 * every element. One assignment on the root then applies everywhere the stylesheet already refers to
 * --brand-accent, so adding a branded surface later needs no change here.
 */

export interface Branding {
  productName: string
  tagline?: string
  accentColour?: string
  /** logoVersion changes with the image, and is absent when none was uploaded. */
  logoVersion?: string
  customised: boolean
}

/** defaultBranding is what an unbranded installation looks like. */
export const defaultBranding: Branding = { productName: 'Perfuse', customised: false }

/**
 * logoURL is where to fetch the current logo.
 *
 * The version is in the query string so a replaced logo appears at once. Without it the browser serves
 * the previous one from cache and the customer concludes the upload silently failed.
 */
export function logoURL(b: Branding): string | undefined {
  if (!b.logoVersion) return undefined
  return `/api/branding/logo?v=${encodeURIComponent(b.logoVersion)}`
}

const BrandingContext = createContext<{
  branding: Branding
  /** reload re-reads branding, so a change on the settings screen shows up without a refresh. */
  reload: () => Promise<void>
}>({ branding: defaultBranding, reload: async () => {} })

export function useBranding() {
  return useContext(BrandingContext)
}

export function BrandingProvider({ children }: { children: React.ReactNode }) {
  const [branding, setBranding] = useState<Branding>(defaultBranding)

  const reload = useCallback(async () => {
    try {
      const res = await fetch('/api/branding', { credentials: 'same-origin' })
      if (!res.ok) return
      const data = (await res.json()) as Partial<Branding>
      setBranding({
        productName: data.productName?.trim() || 'Perfuse',
        tagline: data.tagline?.trim() || undefined,
        accentColour: data.accentColour?.trim() || undefined,
        logoVersion: data.logoVersion || undefined,
        customised: Boolean(data.customised),
      })
    } catch {
      // An installation that cannot answer this is still usable, so the default name stands rather
      // than the interface refusing to render. Branding is presentation; failing closed here would
      // turn a cosmetic problem into an outage.
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  // The accent is applied to the document root, not to a wrapper, so it also reaches anything
  // rendered in a portal - a dialog or a dropdown outside the React tree still picks it up.
  useEffect(() => {
    const root = document.documentElement
    if (branding.accentColour) {
      root.style.setProperty('--brand-accent', branding.accentColour)
    } else {
      root.style.removeProperty('--brand-accent')
    }
  }, [branding.accentColour])

  // The tab title follows the product name, because a browser with twelve tabs open is where somebody
  // actually looks for which product this is.
  useEffect(() => {
    document.title = branding.productName
  }, [branding.productName])

  const value = useMemo(() => ({ branding, reload }), [branding, reload])
  return <BrandingContext.Provider value={value}>{children}</BrandingContext.Provider>
}

/**
 * BrandMark renders the logo, or the built-in mark when none was uploaded.
 *
 * The uploaded image goes through an img element rather than being inlined. An SVG inlined into the
 * document would run any script that survived sanitising; the same SVG in an img cannot, and the
 * server also serves it under a policy that permits nothing. Three layers, and this is one of them -
 * so this must stay an img even though inlining would allow the logo to inherit the accent colour.
 */
/**
 * SiteLogo shows the operator's own logo, and nothing at all when they have not uploaded one.
 *
 * # Why it renders nothing rather than the built-in mark
 *
 * This is for the places where a logo is a claim of ownership rather than a piece of navigation: the top of the dashboard, the
 * head of a printed report. A site that has uploaded their logo wants those to look like their product. A site that has not
 * should see nothing there, because the built-in droplet repeated across the dashboard would be decoration, and the header
 * already says what this software is.
 *
 * That is the difference from BrandMark, which always draws something because a header needs a mark.
 *
 * # Why the name is beside it
 *
 * A logo alone is often a symbol nobody outside the organisation recognises, and on a printout it has to be readable as
 * attribution months later. The name comes from the same branding, so the two cannot disagree.
 */
export function SiteLogo({
  size = 40,
  showName = true,
  className = '',
}: {
  size?: number
  /** Off where the surrounding text already names the site. */
  showName?: boolean
  className?: string
}) {
  const { branding } = useBranding()
  const url = logoURL(branding)

  if (!url) return null

  return (
    <div className={`inline-flex items-center gap-3 ${className}`}>
      <img
        src={url}
        alt={showName ? '' : branding.productName}
        width={size}
        height={size}
        className="shrink-0 rounded-lg object-contain"
        style={{ width: size, height: size }}
      />
      {showName && (
        <div className="min-w-0">
          <div className="truncate text-sm font-semibold text-slate-200">{branding.productName}</div>
          {branding.tagline && <div className="truncate text-xs text-slate-500">{branding.tagline}</div>}
        </div>
      )}
    </div>
  )
}

export function BrandMark({ size = 32, className = '' }: { size?: number; className?: string }) {
  const { branding } = useBranding()
  const url = logoURL(branding)

  if (url) {
    return (
      <img
        src={url}
        alt={branding.productName}
        width={size}
        height={size}
        className={`rounded-lg object-contain ${className}`}
        style={{ width: size, height: size }}
      />
    )
  }

  // The built-in mark: a droplet over a pulse line, in the accent colour so an installation that set
  // only a colour still looks like theirs.
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      className={className}
      role="img"
      aria-label={branding.productName}
    >
      <defs>
        <linearGradient id="brandmark-fill" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="var(--brand-accent, #0ea5e9)" />
          <stop offset="1" stopColor="var(--brand-accent-deep, #6366f1)" />
        </linearGradient>
      </defs>
      <rect width="32" height="32" rx="9" fill="url(#brandmark-fill)" opacity="0.18" />
      <rect x="0.75" y="0.75" width="30.5" height="30.5" rx="8.5" fill="none" stroke="url(#brandmark-fill)" strokeOpacity="0.55" />
      <path
        d="M8 17.5h4.2l2-4.6 2.6 9 2.3-6.1 1.6 3.3H24"
        fill="none"
        stroke="url(#brandmark-fill)"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}
