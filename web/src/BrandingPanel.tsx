import { useRef, useState } from 'react'
import { useBranding, logoURL, BrandMark } from './Branding'
import { IconUpload, IconTrash, IconCheck, IconWarning } from './Icons'

/**
 * BrandingPanel is where a customer makes this their product.
 *
 * Deliberately one screen with a live preview rather than a set of fields under a Save button. The
 * whole reason somebody opens this is to try a colour and look at it, so the accent applies as it is
 * typed and the logo the moment it uploads. Nothing here needs a restart.
 */

const MAX_BYTES = 1024 * 1024

export function BrandingPanel() {
  const { branding, reload } = useBranding()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const fileRef = useRef<HTMLInputElement>(null)

  async function upload(file: File) {
    setError('')
    setNote('')

    // Checked here as well as on the server, because a person who picked a twelve megabyte photograph
    // should be told immediately rather than after uploading it. The server is what refuses; this is
    // only there to save the wait.
    if (file.size > MAX_BYTES) {
      setError(
        `That file is ${(file.size / 1024 / 1024).toFixed(1)} MB. Keep it under 1 MB — it is sent to every browser on every page load.`,
      )
      return
    }

    setBusy(true)
    try {
      const res = await fetch('/api/branding/logo', {
        method: 'PUT',
        headers: { 'X-Perfuse-Request': '1', 'Content-Type': file.type || 'application/octet-stream' },
        body: file,
        credentials: 'same-origin',
      })
      if (!res.ok) {
        const body = (await res.json().catch(() => ({}))) as { error?: string }
        // The server's message names what was wrong with the image, which is the actionable part.
        setError(body.error ?? 'That image could not be used.')
        return
      }
      await reload()
      setNote('Logo updated. It is live everywhere, including the sign-in page.')
    } catch {
      setError('The upload did not reach the server.')
    } finally {
      setBusy(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }

  async function removeLogo() {
    setError('')
    setNote('')
    setBusy(true)
    try {
      const res = await fetch('/api/branding/logo', {
        method: 'DELETE',
        headers: { 'X-Perfuse-Request': '1' },
        credentials: 'same-origin',
      })
      if (!res.ok) {
        setError('The logo could not be removed.')
        return
      }
      await reload()
      setNote('Back to the built-in mark.')
    } finally {
      setBusy(false)
    }
  }

  const url = logoURL(branding)

  return (
    <section className="card p-5">
      <div className="mb-4 flex items-start justify-between gap-4">
        <div>
          <h2 className="text-sm font-semibold tracking-wide text-slate-200 uppercase">Make it yours</h2>
          <p className="mt-1 max-w-2xl text-xs leading-relaxed text-slate-500">
            Upload a logo and set the name, and this becomes your product everywhere it appears —
            the sign-in page, the header, the browser tab. The name and colour are set under Branding
            in the settings below; the logo is here because it is a file rather than a value.
          </p>
        </div>
      </div>

      {/* Preview first, because the point of this screen is seeing the result. */}
      <div className="mb-5 overflow-hidden rounded-xl border border-slate-800 bg-slate-950/60">
        <div className="border-b border-slate-800 px-4 py-2 text-[11px] tracking-wide text-slate-500 uppercase">
          How it looks
        </div>
        <div className="flex flex-wrap items-center gap-6 p-5">
          <div className="inline-flex items-center gap-2.5">
            <BrandMark size={32} />
            <span className="text-xl font-semibold tracking-tight text-slate-100">
              {branding.productName}
            </span>
          </div>
          {branding.tagline && (
            <span className="text-sm text-slate-400">{branding.tagline}</span>
          )}
          {/*
            The colour is shown as a swatch, and the label is left readable.
            This used to paint the label in the accent itself over an 18% wash of it. That works on a near-black page, where the
            wash is dark and the accent is the bright thing on it - and it fails on white, where both are pale: 1.89:1. Which is
            an awkward failure for a control whose whole job is to show a colour clearly.
            A solid square demonstrates the colour better than tinted text does, and it cannot become unreadable, because there
            is nothing to read in it.
          */}
          <span className="inline-flex items-center gap-2 rounded-lg border border-slate-700 px-3 py-1.5 text-sm font-medium text-slate-300">
            <span
              aria-hidden="true"
              className="size-3.5 shrink-0 rounded-full border border-slate-600"
              style={{ background: 'var(--brand-accent, #0ea5e9)' }}
            />
            Accent colour
          </span>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <label className="btn-ghost inline-flex cursor-pointer items-center gap-2">
          <IconUpload size={16} />
          {url ? 'Replace logo' : 'Upload a logo'}
          <input
            ref={fileRef}
            type="file"
            className="sr-only"
            accept="image/png,image/jpeg,image/gif,image/webp,image/svg+xml"
            disabled={busy}
            onChange={(e) => {
              const file = e.target.files?.[0]
              if (file) void upload(file)
            }}
          />
        </label>

        {url && (
          <button type="button" className="btn-ghost inline-flex items-center gap-2" disabled={busy} onClick={() => void removeLogo()}>
            <IconTrash size={16} />
            Remove
          </button>
        )}

        <span className="text-xs text-slate-500">
          PNG, JPEG, GIF, WebP or SVG, up to 1 MB. Square works best.
        </span>
      </div>

      {/*
        Said plainly rather than buried, because an administrator uploading an SVG from a design agency
        deserves to know it is rewritten. A file that comes back looking different is otherwise a
        mystery, and the honest explanation is short.
      */}
      <p className="mt-3 max-w-2xl text-xs leading-relaxed text-slate-500">
        An uploaded SVG is rewritten to remove anything that is not drawing — scripts, event handlers
        and references to other sites. That is because the logo is shown to everyone who reaches the
        sign-in page, before they have signed in. Gradients, paths and text are kept.
      </p>

      {error && (
        <p className="mt-3 flex items-start gap-2 text-xs text-rose-300" role="alert">
          <IconWarning size={14} />
          {error}
        </p>
      )}
      {note && !error && (
        <p className="mt-3 flex items-start gap-2 text-xs text-emerald-300" role="status">
          <IconCheck size={14} />
          {note}
        </p>
      )}
    </section>
  )
}
