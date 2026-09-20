import { CodeArea } from './CodeArea'
import { IconDocuments, IconInfo } from './Icons'
import { useState } from 'react'
import { api, ApiError } from './api'
import type { MergeResult } from './api'
import { ErrorBox, Field, Section } from './ui'
import type { UiError } from './store'

/**
 * Combining several documents into one view.
 *
 * A patient has been seen at three facilities. Each sends a summary, and a clinician now holds three documents each
 * claiming to describe the same person's medications, problems and allergies. Reading all three and working out the
 * union by hand, under time pressure, is exactly the task that goes wrong.
 *
 * What this produces is a reading aid, not a record. Disagreements are surfaced rather than resolved, because resolving
 * a disagreement about whether a patient takes a drug is a clinical judgement — and a program making it silently would
 * produce something that looks like a medical record and was authored by nobody. That is why the result is shown as a
 * comparison and is deliberately not offered as a document to save or send.
 */
export function MergePanel({ document: first }: { document: string }) {
  const [extra, setExtra] = useState<string[]>([''])
  const [result, setResult] = useState<MergeResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<UiError | null>(null)

  const filled = extra.filter((d) => d.trim() !== '')

  async function run() {
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      setResult(await api.mergeDocuments({ documents: [first, ...filled] }))
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
          The document above is the first source. Add the others — summaries from the other facilities that have seen
          this patient — and this shows what all of them say together.
        </p>
        <p className="mt-2 text-sm text-slate-400">
          Disagreements are surfaced, not resolved. Deciding whether a patient is taking a drug when two sources
          disagree is a clinical judgement, so nothing here decides it.
        </p>

        <div className="mt-3 space-y-3">
          {extra.map((doc, i) => (
            <Field
              key={i}
              label={`Source ${i + 2}`}
              hint={i === 0 ? 'Another summary for the same patient.' : undefined}
            >
              <CodeArea
                language="xml"
                className="h-20 w-full"
                value={doc}
                onChange={(next0) => {
                  const next = [...extra]
                  next[i] = next0
                  setExtra(next)
                }}
                placeholder="<ClinicalDocument …"
              />
            </Field>
          ))}
        </div>

        <div className="mt-3 flex gap-2">
          <button
            className="btn-primary py-1 text-xs"
            onClick={() => void run()}
            disabled={busy || filled.length === 0}
          >
            {busy ? 'Combining…' : 'Show what they all say'}
          </button>
          <button
            className="btn-ghost py-1 text-xs"
            onClick={() => setExtra([...extra, ''])}
            disabled={extra.length >= 19}
          >
            Add another source
          </button>
        </div>
      </div>

      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

      {result && (
        <div role="status" className="space-y-4">
          {/* Whether these are the same patient, first and unmissably. Merging two people's lists produces one that
              belongs to neither, and everything below is worthless if this is wrong. */}
          <div
            className={`rounded-lg border p-4 ${
              result.patient.samePatient
                ? 'border-slate-800 bg-slate-900/40'
                : 'border-rose-900/60 bg-rose-950/30'
            }`}
          >
            <p
              className={`text-sm font-medium ${
                result.patient.samePatient ? 'text-slate-300' : 'text-rose-200'
              }`}
            >
              {result.patient.samePatient
                ? 'Every source describes the same patient.'
                : 'At least one source may describe a different person.'}
            </p>
            <p
              className={`mt-1 text-xs ${
                result.patient.samePatient ? 'text-slate-500' : 'text-rose-300/80'
              }`}
            >
              {result.patient.explanation}
            </p>
            {result.patient.disagreements.map((d, i) => (
              <p key={i} className="mt-1.5 font-mono text-[11px] text-rose-300">
                {d}
              </p>
            ))}
          </div>

          {/* The sources, named, so anything surprising below can be traced back. */}
          <Section icon={IconDocuments} title={`${result.sources.length} sources`}>
            <ul className="grid gap-2 sm:grid-cols-2">
              {result.sources.map((s) => (
                <li key={s.position} className="rounded-lg border border-slate-800 bg-slate-900/40 p-3 text-xs">
                  <div className="flex items-baseline gap-2">
                    <span className="badge border border-slate-700 bg-slate-800/60 text-slate-400">
                      {s.position}
                    </span>
                    <span className="font-medium text-slate-200">{s.title}</span>
                  </div>
                  {s.custodian && <p className="mt-1 text-slate-500">{s.custodian}</p>}
                </li>
              ))}
            </ul>
          </Section>

          {result.sections.map((sec) => (
            <Section key={sec.kind} icon={IconInfo} title={sec.section.title || sec.kind}>
              {sec.notes.length > 0 && (
                <ul className="mb-3 space-y-1.5">
                  {sec.notes.map((n, i) => (
                    <li
                      key={i}
                      className="rounded-md border border-amber-900/50 bg-amber-950/20 p-2 text-xs text-amber-200"
                    >
                      {n.message}
                    </li>
                  ))}
                </ul>
              )}

              {sec.section.entries && sec.section.entries.length > 0 ? (
                <ul className="space-y-2">
                  {sec.section.entries.map((e, i) => (
                    <li
                      key={i}
                      className="flex flex-wrap items-baseline gap-2 rounded-lg border border-slate-800 bg-slate-900/40 p-2.5 text-xs"
                    >
                      <span className="text-slate-200">{e.codeName || e.value || 'an unnamed entry'}</span>
                      {/* The code, because similar drug names are told apart by it and not by the name. */}
                      {e.code && <span className="font-mono text-[10px] text-slate-500">{e.code}</span>}
                      {e.statusCode && (
                        <span className="badge border border-slate-700 bg-slate-800/60 text-slate-400">
                          {e.statusCode}
                        </span>
                      )}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="whitespace-pre-wrap text-xs text-slate-400">{sec.section.narrativeText}</p>
              )}
            </Section>
          ))}

          {/* Last, and unmissable. A merged view that looks like a clinical record and was authored by nobody is the
              worst possible artefact, so it says what it is not. */}
          <div className="rounded-lg border border-slate-700 bg-slate-900/60 p-4 text-xs text-slate-400">
            {result.caveat}
          </div>
        </div>
      )}
    </div>
  )
}
