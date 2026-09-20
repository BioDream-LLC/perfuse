import { useState } from 'react'
import { api, ApiError } from './api'
import { ErrorBox } from './ui'
import type { UiError } from './store'

/**
 * Printing a clinical document.
 *
 * The narrative is what gets rendered, not the coded entries. That is the whole design decision: the narrative is
 * the attested content, the part a human being wrote or approved and the part the document's legal weight attaches
 * to. Rendering the entries would put content on a page that looks authoritative and that nobody signed off, and
 * somebody holding the printout in three years would have no way to tell which parts a program invented.
 *
 * So a section with entries and no narrative prints as visibly unattested, with a count of what is behind it, and
 * the repair function is the honest place to rebuild it — where the result can be inspected before it is accepted.
 */
export function PrintPanel({ document: xml }: { document: string }) {
  const [includeCodes, setIncludeCodes] = useState(false)
  const [footer, setFooter] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<UiError | null>(null)
  const [done, setDone] = useState<string | null>(null)

  async function render() {
    setBusy(true)
    setError(null)
    setDone(null)
    try {
      const { blob, filename } = await api.documentPDF({
        document: xml,
        includeCodes,
        footer: footer || undefined,
      })

      const url = URL.createObjectURL(blob)
      const a = window.document.createElement('a')
      a.href = url
      a.download = filename
      a.click()
      URL.revokeObjectURL(url)

      // Said explicitly, because a download that the browser handles silently leaves somebody wondering whether
      // the button did anything at all.
      setDone(`Saved ${filename}.`)
    } catch (e) {
      setError({
        message: e instanceof Error ? e.message : String(e),
        problems: e instanceof ApiError ? e.problems : [],
      })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-4">
      <div className="card p-4">
        <p className="text-sm text-slate-300">
          Renders the narrative — the part a clinician wrote or approved. The coded entries are not turned into prose,
          because a printout that looks authoritative and was never attested by anybody is worse than one that admits
          what is missing.
        </p>
        <p className="mt-2 text-sm text-slate-400">
          A section with coded entries and no narrative prints as unattested, saying how many facts are behind it. To
          rebuild it, use <span className="text-slate-300">What a reader sees</span> and check the result first.
        </p>

        <label className="mt-4 flex items-start gap-2 text-xs text-slate-400">
          <input
            type="checkbox"
            className="mt-0.5"
            checked={includeCodes}
            onChange={(e) => setIncludeCodes(e.target.checked)}
          />
          <span>
            <span className="block text-slate-300">Also print the coded entries as a table</span>
            <span className="mt-0.5 block text-[11px] text-slate-500">
              Labelled as a restatement, not as narrative. Useful when checking an exchange; a page of codes in front
              of a clinician who does not need them makes the medications harder to read.
            </span>
          </span>
        </label>

        <div className="mt-3">
          <label className="mb-1 block text-xs font-medium text-slate-300" htmlFor="pdf-footer">
            Provenance note
          </label>
          <input
            id="pdf-footer"
            className="input w-full py-1 text-xs"
            value={footer}
            onChange={(e) => setFooter(e.target.value)}
            placeholder="Printed at Example Hospital, Health Records"
          />
          <p className="mt-1 text-[11px] text-slate-500">
            Appended to the footer. A printed clinical document with no statement of where it came from cannot be
            audited.
          </p>
        </div>

        <button className="btn-primary mt-4 py-1 text-xs" onClick={() => void render()} disabled={busy}>
          {busy ? 'Rendering…' : 'Render as PDF'}
        </button>
      </div>

      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

      {done && (
        <p role="status" className="text-xs text-emerald-300">
          {done}
        </p>
      )}
    </div>
  )
}
