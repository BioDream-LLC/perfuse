import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from './api'
import type { PASCase, PASReview, PASUMStatus } from './api'
import { CodeArea } from './CodeArea'
import { IconPatient, IconQueue, IconX12 } from './Icons'
import { ErrorBox, Field, Section } from './ui'
import type { UiError } from './store'
import { uiLocale } from './i18n/locale'

/**
 * The reviewer queue for Da Vinci PAS.
 *
 * A prior authorization request no rule decides is pended for a person. This lists them soonest due first - an expedited
 * request has 72 hours under CMS-0057, a standard one 7 days - shows each one whole with the documents the provider has sent,
 * and records the reviewer's decision. The decision goes through the same rules as Claim/$decide, so the provider's PAS
 * subscription delivers it either way.
 */

const toError = (e: unknown): UiError => ({
  message: e instanceof Error ? e.message : String(e),
  problems: e instanceof ApiError ? e.problems : [],
})

const codeColour: Record<string, string> = {
  A1: 'bg-emerald-500/15 text-emerald-200',
  A2: 'bg-emerald-500/15 text-emerald-200',
  A6: 'bg-sky-500/15 text-sky-200',
  A3: 'bg-rose-500/15 text-rose-200',
  A4: 'bg-amber-500/15 text-amber-200',
  C: 'bg-slate-500/15 text-slate-300',
}

/** dueText says how long is left, or how long ago the deadline passed. */
export function dueText(due: string, now = Date.now()): { text: string; late: boolean } {
  const ms = new Date(due).getTime() - now
  const abs = Math.abs(ms)
  const hours = Math.round(abs / 3_600_000)
  const span = hours >= 48 ? `${Math.round(hours / 24)} days` : `${hours} hour${hours === 1 ? '' : 's'}`
  return ms >= 0 ? { text: `due in ${span}`, late: false } : { text: `overdue by ${span}`, late: true }
}

export function PASReviewQueue() {
  const [all, setAll] = useState(false)
  const [cases, setCases] = useState<PASCase[] | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [open, setOpen] = useState<string | null>(null)

  const load = useCallback(() => {
    setError(null)
    api.pasCases(all).then(
      (r) => setCases(r.cases),
      (e) => setError(toError(e)),
    )
  }, [all])
  useEffect(load, [load])

  if (error && !cases) return <ErrorBox error={error} />
  if (!cases) return <p className="text-sm text-slate-400">Reading the prior authorization requests…</p>
  return (
    <div className="grid gap-5 xl:grid-cols-[minmax(0,26rem)_1fr]">
      <Section
        title={all ? 'Every request' : 'Waiting for a reviewer'}
        icon={IconQueue}
        description="Pended requests, soonest due first: 72 hours for an expedited request, 7 days for a standard one; for a drug, 24 hours expedited and 72 standard (CMS-0062-P, proposed)."
      >
        <label className="mb-3 flex items-center gap-2 text-sm text-slate-300">
          <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} />
          Show decided requests too
        </label>
        {cases.length === 0 ? (
          <p className="text-sm text-slate-400" data-testid="pas-queue-empty">
            {all ? 'No prior authorization requests have been received.' : 'Nothing is waiting for a reviewer.'}
          </p>
        ) : (
          <ul className="space-y-2" data-testid="pas-queue">
            {cases.map((c) => {
              const due = dueText(c.due)
              return (
                <li key={c.id}>
                  <button
                    className={`w-full rounded border p-3 text-left text-sm ${open === c.id ? 'border-sky-500 bg-sky-500/10' : 'border-slate-800 hover:border-slate-600'}`}
                    onClick={() => setOpen(c.id)}
                    aria-pressed={open === c.id}
                  >
                    <span className="flex items-center justify-between gap-2">
                      <span className="font-medium text-slate-100">{c.member || 'Unnamed member'}</span>
                      {c.expedited && <span className="rounded bg-rose-500/15 px-2 py-0.5 text-xs text-rose-200">Expedited</span>}
                      {c.drug && <span className="rounded bg-violet-500/15 px-2 py-0.5 text-xs text-violet-200">Drug</span>}
                    </span>
                    <span className="mt-1 block text-xs text-slate-400">
                      {c.provider} · trace {c.trace || c.id.slice(0, 8)}
                    </span>
                    <span className="mt-1 flex flex-wrap gap-1">
                      {c.items.map((it) => (
                        <span key={it.sequence} className={`rounded px-1.5 py-0.5 text-xs ${codeColour[it.code] ?? 'bg-slate-700 text-slate-200'}`}>
                          {it.sequence}: {it.display || it.code}
                        </span>
                      ))}
                    </span>
                    {c.pended && (
                      <span className={`mt-1 block text-xs ${due.late ? 'text-rose-300' : 'text-slate-400'}`}>
                        {due.text}
                        {(c.attachments?.length ?? 0) > 0 && ` · ${c.attachments.length} document${c.attachments.length === 1 ? '' : 's'} received`}
                      </span>
                    )}
                  </button>
                </li>
              )
            })}
          </ul>
        )}
      </Section>
      <div className="space-y-5">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
        <UMLink />
        {open ? (
          <CaseView key={open} id={open} onDecided={load} />
        ) : (
          <p className="text-sm text-slate-400">Choose a request to read it and decide it.</p>
        )}
      </div>
    </div>
  )
}

const umColour: Record<string, string> = {
  answered: 'bg-emerald-500/15 text-emerald-200',
  acknowledged: 'bg-sky-500/15 text-sky-200',
  sent: 'bg-sky-500/15 text-sky-200',
  sending: 'bg-slate-500/15 text-slate-300',
  rejected: 'bg-rose-500/15 text-rose-200',
  unreachable: 'bg-amber-500/15 text-amber-200',
  unreadable: 'bg-amber-500/15 text-amber-200',
}

const umWords: Record<string, string> = {
  answered: 'answered',
  acknowledged: 'acknowledged, answer to come',
  sent: 'sent, answer to come',
  sending: 'being sent',
  rejected: 'rejected',
  unreachable: 'not reached',
  unreadable: 'answer unreadable',
}

/**
 * UMLink shows the payer's utilization management system as this server sees it: what was sent in the last seven days and what
 * became of it. Shown only when a UM link is configured; its settings are under Settings, Payers, Utilization management link.
 */
export function UMLink() {
  const [st, setSt] = useState<PASUMStatus | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const load = useCallback(() => {
    api.pasUM().then(setSt, (e) => setError(toError(e)))
  }, [])
  useEffect(load, [load])
  if (error) return <ErrorBox error={error} onDismiss={() => setError(null)} />
  if (!st?.enabled) return null
  const order = ['answered', 'acknowledged', 'sent', 'rejected', 'unreachable', 'unreadable', 'sending']
  const counts = order.filter((k) => st.counts[k])
  return (
    <Section
      title="Utilization management link"
      icon={IconX12}
      description="Every request is sent to the payer's UM system as an X12 278. The last seven days, and the latest exchanges with what became of each. Configured under Settings, Payers."
    >
      <div className="mb-3 flex flex-wrap gap-2" data-testid="um-counts">
        {counts.length === 0 ? (
          <span className="text-sm text-slate-400">Nothing sent in the last seven days.</span>
        ) : (
          counts.map((k) => (
            <span key={k} className={`rounded px-2 py-0.5 text-xs ${umColour[k] ?? 'bg-slate-700 text-slate-200'}`}>
              {st.counts[k]} {umWords[k] ?? k}
            </span>
          ))
        )}
        <button className="ml-auto text-xs text-sky-300 hover:underline" onClick={load}>
          Refresh
        </button>
      </div>
      {st.recent.length > 0 && (
        <ul className="space-y-1 text-xs" data-testid="um-recent">
          {st.recent.map((e) => (
            <li key={e.control} className="flex flex-wrap items-baseline gap-2 border-t border-slate-800 pt-1">
              <span className="font-mono text-slate-400">#{e.control}</span>
              <span className="text-slate-300">{e.kind}</span>
              <span className={`rounded px-1.5 py-0.5 ${umColour[e.status] ?? 'bg-slate-700 text-slate-200'}`}>{umWords[e.status] ?? e.status}</span>
              <span className="text-slate-500">{new Date(e.sent).toLocaleString(uiLocale())}</span>
              {e.detail && <span className="basis-full text-slate-400">{e.detail}</span>}
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}

// fewest is the most a modify may certify: one less than the smallest quantity asked for, or nothing when none was given.
function fewest(c: PASCase): number | undefined {
  const asked = c.items.map((i) => i.quantity ?? 0).filter((q) => q > 0)
  return asked.length ? Math.min(...asked) - 1 : undefined
}

function CaseView({ id, onDecided }: { id: string; onDecided: () => void }) {
  const [c, setC] = useState<PASCase | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [decision, setDecision] = useState<PASReview['decision']>('approve')
  const [reason, setReason] = useState('')
  const [npi, setNpi] = useState('')
  const [quantity, setQuantity] = useState('')
  const [altSystem, setAltSystem] = useState('')
  const [altCode, setAltCode] = useState('')
  const [chosen, setChosen] = useState<number[]>([])
  const [done, setDone] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [showBundles, setShowBundles] = useState(false)

  useEffect(() => {
    api.pasCase(id).then((got) => {
      setC(got)
      // A service approved instead is coded like the one asked for (HCPCS, CPT), so that is where the system starts.
      setAltSystem((cur) => cur || got.items.find((i) => i.system)?.system || '')
    }, (e) => setError(toError(e)))
  }, [id])

  async function decide() {
    setError(null)
    setDone(null)
    const review: PASReview = { decision, reason: reason || undefined, reviewerNpi: npi || undefined }
    if (chosen.length > 0) review.items = chosen
    if (decision === 'modify') {
      if (quantity) review.quantity = Number(quantity)
      if (altCode) review.alternative = { system: altSystem, code: altCode }
    }
    setBusy(true)
    try {
      await api.pasDecide(id, review)
      setDone('Decided. The provider is notified on the PAS subscription.')
      setC(await api.pasCase(id))
      onDecided()
    } catch (e) {
      setError(toError(e))
    } finally {
      setBusy(false)
    }
  }

  if (error && !c) return <ErrorBox error={error} />
  if (!c) return <p className="text-sm text-slate-400">Reading the request…</p>
  const pendedItems = c.items.filter((it) => it.code === 'A4')
  return (
    <div className="space-y-4" data-testid="pas-case">
      <Section title={c.member || 'Unnamed member'} icon={IconPatient} description={`Member ${c.memberId || 'unknown'} · ${c.provider}`}>
        <table className="w-full text-left text-sm">
          <thead className="text-xs text-slate-400">
            <tr>
              <th className="py-1 pr-2">Item</th>
              <th className="py-1 pr-2">Service</th>
              <th className="py-1 pr-2">Units</th>
              <th className="py-1">Where it stands</th>
            </tr>
          </thead>
          <tbody>
            {c.items.map((it) => (
              <tr key={it.sequence} className="border-t border-slate-800 align-top">
                <td className="py-1.5 pr-2 text-slate-300">{it.sequence}</td>
                <td className="py-1.5 pr-2 text-slate-200">{it.service || '-'}</td>
                <td className="py-1.5 pr-2 text-slate-300">{it.quantity ?? '-'}</td>
                <td className="py-1.5">
                  <span className={`rounded px-1.5 py-0.5 text-xs ${codeColour[it.code] ?? 'bg-slate-700 text-slate-200'}`}>
                    {it.code} {it.display}
                  </span>
                  {it.note && <p className="mt-1 text-xs text-slate-400">{it.note}</p>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
      <Section
        title="Documents"
        description={c.asked?.length ? `Asked for: ${c.asked.join(', ')}` : 'The response asked for no documents.'}
      >
        {!c.attachments?.length ? (
          <p className="text-sm text-slate-400">None received yet.</p>
        ) : (
          <ul className="space-y-2 text-sm" data-testid="pas-documents">
            {c.attachments.map((a) => (
              <li key={a.id} className="rounded border border-slate-800 p-2">
                <p className="text-slate-200">
                  {a.resourceType}
                  {a.code && ` · LOINC ${a.code}`}
                  {a.lineItems?.length ? ` · item ${a.lineItems.join(', ')}` : ''}
                  {a.final && <span className="ml-2 rounded bg-emerald-500/15 px-1.5 text-xs text-emerald-200">last submission</span>}
                </p>
                <p className="text-xs text-slate-500">received {new Date(a.received).toLocaleString(uiLocale())}</p>
                <DocumentText content={a.content} />
              </li>
            ))}
          </ul>
        )}
      </Section>
      {c.pended ? (
        <Section title="Decide" description="Recorded as a new version of the response, marked as reviewed by a person.">
          <div className="space-y-3">
            <div role="group" aria-label="Decision" className="flex flex-wrap gap-2">
              {(['approve', 'deny', 'modify'] as const).map((d) => (
                <button
                  key={d}
                  aria-pressed={decision === d}
                  className={decision === d ? 'btn-primary py-1 text-sm' : 'btn-ghost py-1 text-sm'}
                  onClick={() => setDecision(d)}
                >
                  {d === 'approve' ? 'Approve' : d === 'deny' ? 'Deny' : 'Modify'}
                </button>
              ))}
            </div>
            {pendedItems.length > 1 && (
              <fieldset className="text-sm text-slate-300">
                <legend className="mb-1 text-xs text-slate-400">Items (none chosen means every pended item)</legend>
                {pendedItems.map((it) => (
                  <label key={it.sequence} className="mr-3 inline-flex items-center gap-1">
                    <input
                      type="checkbox"
                      checked={chosen.includes(it.sequence)}
                      onChange={(e) =>
                        setChosen(e.target.checked ? [...chosen, it.sequence] : chosen.filter((n) => n !== it.sequence))
                      }
                    />
                    {it.sequence}
                  </label>
                ))}
              </fieldset>
            )}
            {decision === 'modify' && (
              <div className="grid gap-3 sm:grid-cols-3">
                {/* With one unit asked for there is no smaller number to certify: the input would have min 1 and
                    max 0 and accept nothing, so it says so instead and leaves the alternative code. */}
                <Field label="Units certified"
                  hint={(fewest(c) ?? 1) < 1 ? 'One unit was asked for, so fewer cannot be certified; approve a different code instead.' : 'Fewer than asked.'}>
                  <input className="input" type="number" min={1} disabled={(fewest(c) ?? 1) < 1}
                    max={fewest(c)} value={quantity} onChange={(e) => setQuantity(e.target.value)} />
                </Field>
                <Field label="Approved instead: code system">
                  <input className="input" value={altSystem} onChange={(e) => setAltSystem(e.target.value)} />
                </Field>
                <Field label="Approved instead: code">
                  <input className="input" value={altCode} onChange={(e) => setAltCode(e.target.value)} />
                </Field>
              </div>
            )}
            <div className="grid gap-3 sm:grid-cols-[1fr_12rem]">
              <Field label="Reason" hint="Sent to the provider in the response's note.">
                <input className="input" value={reason} onChange={(e) => setReason(e.target.value)} />
              </Field>
              <Field label="Reviewer NPI">
                <input className="input" value={npi} onChange={(e) => setNpi(e.target.value)} inputMode="numeric" />
              </Field>
            </div>
            <button className="btn-primary w-full" disabled={busy} onClick={() => void decide()}>
              Record the decision
            </button>
            {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
          </div>
        </Section>
      ) : (
        <p className="text-sm text-emerald-300">{done ?? 'Decided: nothing in this request is pended.'}</p>
      )}
      <button className="btn-ghost py-1 text-sm" aria-expanded={showBundles} onClick={() => setShowBundles(!showBundles)}>
        {showBundles ? 'Hide' : 'Show'} the PAS Bundles
      </button>
      {showBundles && (
        <div className="grid gap-3 lg:grid-cols-2">
          <Field label="Request">
            <CodeArea language="json" className="min-h-64" value={JSON.stringify(c.request, null, 2)} readOnly />
          </Field>
          <Field label="Current response">
            <CodeArea language="json" className="min-h-64" value={JSON.stringify(c.response, null, 2)} readOnly />
          </Field>
        </div>
      )}
    </div>
  )
}

/** DocumentText shows a plain-text attachment inline; anything else is named, not rendered. */
function DocumentText({ content }: { content?: Record<string, unknown> }) {
  const att = (content?.content as { attachment?: { contentType?: string; data?: string } }[] | undefined)?.[0]?.attachment
  if (!att?.data || !(att.contentType ?? '').startsWith('text/')) {
    return att?.contentType ? <p className="text-xs text-slate-500">{att.contentType}</p> : null
  }
  let text = ''
  try {
    text = new TextDecoder().decode(Uint8Array.from(atob(att.data), (ch) => ch.charCodeAt(0)))
  } catch {
    return <p className="text-xs text-amber-300">The document's data is not valid Base64.</p>
  }
  return <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap rounded bg-slate-950 p-2 text-xs text-slate-300">{text}</pre>
}
