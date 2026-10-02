import { useState } from 'react'

import { api } from './api'

/**
 * MirthExportButton offers a channel as Mirth XML, after saying what will not survive.
 *
 * Why it asks first. Mirth's format has nowhere to put a Perfuse filter, a transformation chain, a
 * contract or a shadow comparison, so an exported channel does less than the one it came from. The
 * description in the file says so, and a description is the easiest thing in the world to scroll past
 * — so the losses are listed here, before the file exists, where somebody about to carry it to
 * another system has to read them.
 *
 * Why a refusal is possible. A channel with an S3 destination has no Mirth equivalent, and the
 * conversion declines rather than substituting something. A channel that imported cleanly and
 * delivered to the wrong place would be worse than no export at all.
 */
export function MirthExportButton({ channel }: { channel: string }) {
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [notes, setNotes] = useState<string[]>([])
  const [refusal, setRefusal] = useState('')
  const [error, setError] = useState('')

  async function ask() {
    setLoading(true)
    setError('')
    setRefusal('')
    setNotes([])
    try {
      const res = await api.mirthExportPreview(channel)
      setNotes(res.notes ?? [])
      setRefusal(res.convertible ? '' : res.refusal)
      setOpen(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setOpen(true)
    } finally {
      setLoading(false)
    }
  }

  return (
    <>
      <button
        className="btn-ghost"
        onClick={ask}
        disabled={loading}
        title="Download this channel as a Mirth Connect channel file, so a migration can be reversed"
      >
        {loading ? 'Checking…' : 'To Mirth'}
      </button>

      {open && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
          role="dialog"
          aria-modal="true"
          aria-labelledby="mirth-export-title"
        >
          <div className="max-h-[80vh] w-full max-w-2xl overflow-auto rounded-xl border border-slate-700 bg-slate-900 p-6">
            <h2 id="mirth-export-title" className="text-lg font-semibold text-slate-100">
              Export {channel} to Mirth
            </h2>

            {error && (
              <p className="mt-4 rounded-lg border border-red-800/60 bg-red-950/30 p-3 text-sm text-red-200">
                {error}
              </p>
            )}

            {refusal && (
              <div className="mt-4 rounded-lg border border-amber-800/60 bg-amber-950/20 p-3 text-sm leading-relaxed text-amber-200">
                <p className="font-medium">This channel cannot be exported to Mirth.</p>
                <p className="mt-2">{refusal}</p>
                <p className="mt-2 text-amber-300/80">
                  Nothing is offered rather than an approximation: a channel that imported cleanly and
                  delivered somewhere else would be worse than no file.
                </p>
              </div>
            )}

            {!refusal && !error && (
              <>
                <p className="mt-4 text-sm leading-relaxed text-slate-300">
                  The transports and their addresses convert. Everything below does not, and the
                  channel will do less in Mirth than it does here. The file says so in its
                  description as well, so whoever imports it sees it too.
                </p>

                {notes.length === 0 ? (
                  <p className="mt-4 rounded-lg border border-emerald-800/60 bg-emerald-950/20 p-3 text-sm text-emerald-200">
                    Nothing is lost. This channel uses only what Mirth can express.
                  </p>
                ) : (
                  <ul className="mt-4 space-y-2">
                    {notes.map((note) => (
                      <li
                        key={note}
                        className="rounded-lg border border-amber-800/60 bg-amber-950/20 p-3 text-sm leading-relaxed text-amber-200"
                      >
                        {note}
                      </li>
                    ))}
                  </ul>
                )}
              </>
            )}

            <div className="mt-6 flex items-center justify-end gap-3">
              <button className="btn-ghost" onClick={() => setOpen(false)}>
                Close
              </button>
              {/* The download link deliberately has no onClick that closes this dialogue.
                  
                  Unmounting the anchor as part of its own click cancels the download in Chromium, which showed up as a test waiting
                  ninety seconds for a download event that never arrived. The dialogue stays until it is dismissed, and the browser's own
                  download indicator is the confirmation. */}
              {!refusal && !error && (
                <a className="btn-primary" href={api.mirthExportUrl(channel)} download>
                  Download the Mirth file
                </a>
              )}
            </div>
          </div>
        </div>
      )}
    </>
  )
}
