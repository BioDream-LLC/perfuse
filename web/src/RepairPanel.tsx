import { SyntaxBlock } from './SyntaxHighlight'
import { IconSuccess } from './Icons'
import { useState } from 'react'
import { api, ApiError } from './api'
import type { RepairReport } from './api'
import { ErrorBox, Field, Section } from './ui'
import { useCopy } from './useCopy'
import type { UiError } from './store'

/**
 * The document that is valid, transferred successfully, and blank on screen.
 *
 * C-CDA carries every clinical fact twice: coded entries a machine imports, and narrative a clinician reads. They
 * are not alternatives, and almost every viewer in the field renders the narrative and ignores the entries.
 *
 * So a section with entries and an empty narrative passes the schema, passes the receiver's import, is counted as
 * a successful exchange by both ends, and displays as nothing. The patient's medication list is in the file and
 * invisible, and no error is raised anywhere because nothing is technically wrong.
 *
 * The harm is that "no medications" and "we failed to render the medications" look identical on screen. One of
 * them gets somebody prescribed a drug that interacts with what they are already taking.
 */
export function RepairPanel({ document: xml }: { document: string }) {
  const [custodian, setCustodian] = useState('')
  const [report, setReport] = useState<RepairReport | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<UiError | null>(null)
  const { label: copyLabel, copy } = useCopy()

  async function run() {
    setBusy(true)
    setError(null)
    setReport(null)
    try {
      setReport(await api.repairDocument({ document: xml, custodianName: custodian || undefined }))
    } catch (e) {
      setError({
        message: e instanceof Error ? e.message : String(e),
        problems: e instanceof ApiError ? e.problems : [],
      })
    } finally {
      setBusy(false)
    }
  }

  function download() {
    if (!report?.document) return
    const blob = new Blob([report.document], { type: 'application/xml' })
    const url = URL.createObjectURL(blob)
    const a = window.document.createElement('a')
    a.href = url
    a.download = 'repaired.cda.xml'
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="space-y-4">
      <div className="card p-4">
        <p className="text-sm text-slate-300">
          A section holding coded entries with no narrative passes every check and displays as empty. Most viewers
          render the narrative and ignore the entries, so the content is in the file and nobody sees it — and no
          error is raised anywhere, because nothing is technically wrong.
        </p>
        <p className="mt-2 text-sm text-slate-400">
          That matters because an empty Medications heading and a failure to render the medications look identical.
          A clinician reading one concludes the patient takes nothing.
        </p>

        <div className="mt-3 grid gap-3 border-t border-slate-800 pt-3 sm:grid-cols-2">
          <Field
            label="Custodian organisation"
            hint="Needed only if the document names none. Who is answerable for the content."
          >
            <input
              className="input w-full py-1 text-xs"
              value={custodian}
              onChange={(e) => setCustodian(e.target.value)}
              placeholder="Example Hospital"
            />
          </Field>
        </div>

        <button className="btn-primary mt-3 py-1 text-xs" onClick={() => void run()} disabled={busy}>
          {busy ? 'Checking…' : 'Check what a reader would see'}
        </button>
      </div>

      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

      {report && (
        <div role="status" className="space-y-4">
          {/* The count first, because it is the finding. Everything else explains it. */}
          {report.invisibleEntries > 0 ? (
            <div className="rounded-lg border border-rose-900/60 bg-rose-950/30 p-4">
              <p className="text-sm font-medium text-rose-200">
                {report.invisibleEntries} coded{' '}
                {report.invisibleEntries === 1 ? 'fact' : 'facts'} no reader would have seen.
              </p>
              <p className="mt-1 text-xs text-rose-300/80">
                Present in the file, absent from the screen. The exchange would have been recorded as successful at
                both ends.
              </p>
            </div>
          ) : (
            <div className="rounded-lg border border-emerald-900/60 bg-emerald-950/30 p-4 text-sm text-emerald-200">
              Everything coded in this document is also in the narrative, so a reader sees all of it.
            </div>
          )}

          {report.repairs.length > 0 && (
            <Section icon={IconSuccess} title={`${report.fixed} repaired, ${report.reported} reported`}>
              <ul className="space-y-3">
                {report.repairs.map((rep, i) => (
                  <li
                    key={i}
                    className={`rounded-lg border p-3 ${
                      rep.fixed
                        ? 'border-emerald-900/60 bg-emerald-950/20'
                        : 'border-amber-900/60 bg-amber-950/20'
                    }`}
                  >
                    <div className="flex flex-wrap items-baseline gap-2">
                      {rep.section && <span className="text-sm font-medium text-slate-200">{rep.section}</span>}
                      <span
                        className={`badge border text-[10px] ${
                          rep.fixed
                            ? 'border-emerald-800/50 bg-emerald-950/50 text-emerald-300'
                            : 'border-amber-800/50 bg-amber-950/50 text-amber-300'
                        }`}
                      >
                        {rep.fixed ? 'repaired' : 'reported only'}
                      </span>
                      <span className="font-mono text-[10px] text-slate-500">{rep.kind}</span>
                    </div>

                    <p className="mt-1.5 text-sm text-slate-300">{rep.problem}</p>

                    {/* The consequence, given its own emphasis. The problem is technical and the consequence is
                        clinical, and only one of them makes anybody act today. */}
                    <p className="mt-2 border-l-2 border-slate-700 pl-3 text-xs text-slate-400">
                      {rep.consequence}
                    </p>

                    <p className="mt-2 text-xs text-slate-500">{rep.action}</p>

                    {rep.recovered && (
                      <p className="mt-2 rounded-md border border-slate-800 bg-slate-950/60 p-2 font-mono text-[11px] text-slate-300">
                        {rep.recovered}
                      </p>
                    )}
                  </li>
                ))}
              </ul>
            </Section>
          )}

          {report.document && (
            <Section title="The repaired document">
              <p className="mb-2 text-xs text-slate-500">
                Check it before you trust it. Every change above was derived from data already in the document — no
                clinical content was invented — but a repair nobody reads is a repair nobody should accept.
              </p>
              <div className="mb-2 flex gap-2">
                <button className="btn-ghost py-1 text-xs" onClick={download}>
                  Download it
                </button>
                <button className="btn-ghost py-1 text-xs" onClick={() => void copy(report.document ?? '')}>
                  {copyLabel ?? 'Copy it'}
                </button>
              </div>
              <SyntaxBlock code={report.document} language="xml" className="max-h-96" />
            </Section>
          )}
        </div>
      )}
    </div>
  )
}
