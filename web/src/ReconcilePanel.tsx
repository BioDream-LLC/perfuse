import { CodeArea } from './CodeArea'
import { IconEdit, IconStart, IconStop, IconWarning } from './Icons'
import { useState } from 'react'
import { api, ApiError } from './api'
import type { MedChange, ReconcileResult } from './api'
import { ErrorBox, Field, Section } from './ui'
import type { UiError } from './store'

/**
 * Comparing two medication lists across a transition of care.
 *
 * A patient moves between facilities and each end holds a list. They disagree, because one was updated during the
 * admission and the other was not, or a drug was stopped and the stop never propagated. Whichever list the
 * receiving clinician happens to read decides what they believe the patient is taking.
 *
 * The case that hurts is not a drug present in one list and absent from the other, which is at least visible. It is
 * a drug that appears active in one and discontinued in the other: both documents conformant, both internally
 * consistent, contradicting each other about whether the patient is currently taking something. Nothing in either
 * file says so.
 */
export function ReconcilePanel({ document: after }: { document: string }) {
  const [before, setBefore] = useState('')
  const [result, setResult] = useState<ReconcileResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<UiError | null>(null)

  async function run() {
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      setResult(await api.reconcileDocuments({ before, after }))
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
          The document above is treated as the later list. Paste the earlier one to compare against — the summary
          from the sending facility, or the previous version of this document.
        </p>

        <div className="mt-3">
          <Field
            label="The earlier document"
            hint="Which is which matters: reversed, a drug that was started reads as one that was stopped."
          >
            <CodeArea
            language="xml"
              className="h-28 w-full"
              value={before}
              onChange={setBefore}
              placeholder="<ClinicalDocument …"
              spellCheck={false}
            />
          </Field>
        </div>

        <button
          className="btn-primary mt-3 py-1 text-xs"
          onClick={() => void run()}
          disabled={busy || before.trim() === ''}
        >
          {busy ? 'Comparing…' : 'Compare the lists'}
        </button>
      </div>

      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

      {result && (
        <div role="status" className="space-y-4">
          {/* Whether these are even the same patient, first and unmissably.
              
              Comparing two people's lists produces a report full of additions and removals that looks exactly like
              one patient whose therapy changed completely. It is an easy mistake to make with two files on a
              desktop, and everything below is worthless if this is wrong. */}
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
                ? 'These documents describe the same patient.'
                : 'These may not be the same patient.'}
            </p>
            <p
              className={`mt-1 text-xs ${
                result.patient.samePatient ? 'text-slate-500' : 'text-rose-300/80'
              }`}
            >
              {result.patient.explanation}
            </p>
          </div>

          {/* The conflicts, before anything else, because they are the only findings that are actively dangerous
              rather than merely different. */}
          {result.report.conflicts.length > 0 && (
            <Section icon={IconWarning} title={`${result.report.conflicts.length} contradiction${result.report.conflicts.length === 1 ? '' : 's'}`}>
              <p className="mb-3 text-xs text-slate-400">
                These drugs appear active in one document and stopped in the other. Both documents are conformant and
                internally consistent; they simply disagree, and nothing in either file says so.
              </p>
              <ChangeList changes={result.report.conflicts} tone="rose" />
            </Section>
          )}

          <div className="grid gap-4 lg:grid-cols-2">
            {result.report.added.length > 0 && (
              <Section icon={IconStart} title={`Started (${result.report.added.length})`}>
                <ChangeList changes={result.report.added} tone="emerald" />
              </Section>
            )}
            {result.report.removed.length > 0 && (
              <Section icon={IconStop} title={`No longer listed (${result.report.removed.length})`}>
                <p className="mb-3 text-xs text-slate-400">
                  Absent from the later document. That may mean stopped, or it may mean the later list was never
                  updated — the document does not distinguish the two.
                </p>
                <ChangeList changes={result.report.removed} tone="amber" />
              </Section>
            )}
          </div>

          {result.report.changed.length > 0 && (
            <Section icon={IconEdit} title={`Changed (${result.report.changed.length})`}>
              <ChangeList changes={result.report.changed} tone="sky" />
            </Section>
          )}

          {result.report.unchanged.length > 0 && (
            <details className="rounded-lg border border-slate-800 bg-slate-900/30 p-3">
              <summary className="cursor-pointer text-sm text-slate-400">
                {result.report.unchanged.length} unchanged
              </summary>
              <div className="mt-3">
                <ChangeList changes={result.report.unchanged} tone="slate" />
              </div>
            </details>
          )}

          {result.report.conflicts.length === 0 &&
            result.report.added.length === 0 &&
            result.report.removed.length === 0 &&
            result.report.changed.length === 0 && (
              <div className="rounded-lg border border-emerald-900/60 bg-emerald-950/30 p-4 text-sm text-emerald-200">
                The two lists agree throughout.
              </div>
            )}
        </div>
      )}
    </div>
  )
}

function ChangeList({ changes, tone }: { changes: MedChange[]; tone: string }) {
  const border =
    tone === 'rose'
      ? 'border-rose-900/60 bg-rose-950/20'
      : tone === 'emerald'
        ? 'border-emerald-900/60 bg-emerald-950/20'
        : tone === 'amber'
          ? 'border-amber-900/60 bg-amber-950/20'
          : tone === 'sky'
            ? 'border-sky-900/60 bg-sky-950/20'
            : 'border-slate-800 bg-slate-900/40'

  return (
    <ul className="space-y-2">
      {changes.map((c, i) => (
        <li key={i} className={`rounded-lg border p-3 ${border}`}>
          <div className="flex flex-wrap items-baseline gap-2">
            <span className="text-sm font-medium text-slate-200">{c.medication || 'an unnamed medication'}</span>
            {/* The code, because two drugs with similar names are told apart by it and not by the name. */}
            {c.code && <span className="font-mono text-[10px] text-slate-500">{c.code}</span>}
          </div>
          {c.detail && <p className="mt-1 text-xs text-slate-400">{c.detail}</p>}
        </li>
      ))}
    </ul>
  )
}
