import { useCallback, useEffect, useState } from 'react'
import { useSelector } from 'react-redux'
import { api, ApiError } from './api'
import type { PrivacyFinding, PrivacyRequest, PrivacySummary } from './api'
import type { RootState, UiError } from './store'
import { ErrorBox, Field, Section, Spinner } from './ui'
import { IconDelete, IconDownload, IconLock, IconSearch, IconShield, IconWarning } from './Icons'
import { uiLocale } from './i18n/locale'

/**
 * Data protection: a person's requests under the GDPR and the UK GDPR. Find what this server holds about them, give them a
 * copy, erase it with a second administrator's approval, or keep it under a legal hold; and the record of processing for
 * the register. Administrators only, and every step is in the audit log.
 *
 * Nothing here decides whether a request must be granted: that, and any exemption, is the controller's to decide.
 */

function toError(e: unknown): UiError {
  return { message: e instanceof Error ? e.message : String(e), problems: e instanceof ApiError ? e.problems : [] }
}

const kindLabel: Record<string, string> = {
  access: 'Access (Article 15)',
  portability: 'Portability (Article 20)',
  erasure: 'Erasure (Article 17)',
  restriction: 'Restriction (Article 18)',
}

const statusClass: Record<string, string> = {
  open: 'text-sky-300',
  approved: 'text-amber-300',
  done: 'text-emerald-300',
  refused: 'text-slate-400',
}

function splitIdents(s: string): string[] {
  return s
    .split(/\n|,/)
    .map((x) => x.trim())
    .filter(Boolean)
}

function date(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso.length === 10 ? iso + 'T00:00:00Z' : iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString(uiLocale(), { dateStyle: 'medium' })
}

function overdue(r: PrivacyRequest): boolean {
  return (r.status === 'open' || r.status === 'approved') && r.due < new Date().toISOString().slice(0, 10)
}

function FindingView({ f }: { f: PrivacyFinding }) {
  const rows: [string, number, string][] = [
    ['Messages', f.counts.messages ?? 0, 'as received, with their records'],
    ['Waiting in the delivery queue', f.counts.queue ?? 0, 'not yet delivered'],
    ['FHIR resources', f.counts.fhir ?? 0, 'with every stored version'],
    ['Prior authorization requests', f.counts.pas ?? 0, 'with the answers given'],
    ['People in the patient index', f.counts.persons ?? 0, 'and their linked records'],
  ]
  return (
    <div className="mt-3 space-y-3" data-testid="privacy-finding">
      <table className="w-full text-sm">
        <tbody className="divide-y divide-slate-800">
          {rows.map(([label, n, note]) => (
            <tr key={label}>
              <td className="py-1.5 pr-3 text-slate-300">{label}</td>
              <td className={`py-1.5 pr-3 font-mono ${n > 0 ? 'text-emerald-300' : 'text-slate-500'}`}>{n.toLocaleString(uiLocale())}</td>
              <td className="py-1.5 text-xs text-slate-500">{note}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="text-xs text-slate-400">
        Searched for {f.identifiers.length} identifier{f.identifiers.length === 1 ? '' : 's'}
        {f.persons.length > 0 ? ', widened by the patient index to every number of the person’s linked records' : ''}:{' '}
        <span className="font-mono text-[11px] text-slate-300">{f.identifiers.join(', ')}</span>
      </p>
      {f.holds.length > 0 && (
        <p className="flex items-start gap-2 rounded-lg border border-amber-900/60 bg-amber-950/30 p-2 text-xs text-amber-300">
          <IconLock className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          Under a legal hold ({f.holds.map((h) => h.reason).join('; ')}): it cannot be erased until the hold is lifted.
        </p>
      )}
      {!f.indexed && (
        <p className="flex items-start gap-2 rounded-lg border border-amber-900/60 bg-amber-950/30 p-2 text-xs text-amber-300">
          <IconWarning className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          The identity index is off, so messages cannot be found by identifier.
        </p>
      )}
      <ul className="list-disc space-y-1 pl-5 text-xs text-slate-500">
        {f.notes.map((n) => (
          <li key={n}>{n}</li>
        ))}
      </ul>
    </div>
  )
}

function NewRequest({ onCreated }: { onCreated: () => void }) {
  const [kind, setKind] = useState('access')
  const [subject, setSubject] = useState('')
  const [idents, setIdents] = useState('')
  const [note, setNote] = useState('')
  const [error, setError] = useState<UiError | null>(null)
  const [preview, setPreview] = useState<PrivacyFinding | null>(null)
  const [busy, setBusy] = useState(false)
  const find = () => {
    setBusy(true)
    api.privacyFind(splitIdents(idents)).then(
      (f) => {
        setPreview(f)
        setError(null)
        setBusy(false)
      },
      (e) => {
        setError(toError(e))
        setBusy(false)
      },
    )
  }
  const create = () =>
    api.privacyCreate({ kind, subject, identifiers: splitIdents(idents), note }).then(
      () => {
        setSubject('')
        setIdents('')
        setNote('')
        setPreview(null)
        setError(null)
        onCreated()
      },
      (e) => setError(toError(e)),
    )
  return (
    <Section title="Record a request" icon={IconShield} description="A request is due one month after it is received (Article 12(3)).">
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Kind">
          <select className="input" value={kind} onChange={(e) => setKind(e.target.value)}>
            {Object.entries(kindLabel).map(([v, l]) => (
              <option key={v} value={v}>
                {l}
              </option>
            ))}
          </select>
        </Field>
        <Field label="From" hint="How your records refer to the person and the request: a name and the date of the letter. Never searched.">
          <input className="input" value={subject} onChange={(e) => setSubject(e.target.value)} />
        </Field>
        <Field
          label="The person’s identifiers"
          hint="One per line, as system|value (https://fhir.nhs.uk/Id/nhs-number|943 476 5919) or a number on its own. The patient index adds the person’s other numbers."
        >
          <textarea className="input font-mono text-xs" rows={3} value={idents} onChange={(e) => setIdents(e.target.value)} />
        </Field>
        <Field label="Note" hint="How the identity was confirmed, an extension and its reason.">
          <textarea className="input text-xs" rows={3} value={note} onChange={(e) => setNote(e.target.value)} />
        </Field>
      </div>
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      <div className="mt-3 flex flex-wrap gap-2">
        <button type="button" className="btn-ghost py-1 text-xs" disabled={busy || !idents.trim()} onClick={find}>
          <IconSearch className="h-3.5 w-3.5" aria-hidden="true" /> {busy ? 'Searching…' : 'See what is held'}
        </button>
        <button type="button" className="btn-primary py-1 text-xs" disabled={!idents.trim() || !subject.trim()} onClick={() => void create()}>
          Record the request
        </button>
      </div>
      {preview && <FindingView f={preview} />}
    </Section>
  )
}

function RequestRow({ r, me, onChanged }: { r: PrivacyRequest; me: string; onChanged: () => void }) {
  const [error, setError] = useState<UiError | null>(null)
  const [finding, setFinding] = useState<PrivacyFinding | null>(null)
  const [reason, setReason] = useState('')
  const [said, setSaid] = useState('')
  const open = r.status === 'open' || r.status === 'approved'
  const act = (p: Promise<unknown>, done: string) =>
    p.then(
      () => {
        setError(null)
        setSaid(done)
        onChanged()
      },
      (e) => setError(toError(e)),
    )
  return (
    <li className="rounded-lg border border-slate-800 p-3" data-request={r.id}>
      <div className="flex flex-wrap items-center gap-3 text-sm">
        <span className="font-mono text-xs text-slate-500">#{r.id}</span>
        <span className="text-slate-200">{kindLabel[r.kind] ?? r.kind}</span>
        <span className="text-slate-400">{r.subject}</span>
        <span className={`text-xs ${statusClass[r.status] ?? ''}`}>{r.status}</span>
        <span className={`text-xs ${overdue(r) ? 'text-rose-300' : 'text-slate-500'}`}>
          {open ? `due ${date(r.due)}${overdue(r) ? ', overdue' : ''}` : `${r.status} ${date(r.closedAt ?? '')}${r.outcome ? `: ${r.outcome}` : ''}`}
        </span>
      </div>
      <p className="mt-1 font-mono text-[11px] text-slate-500">{r.identifiers.join(', ')}</p>
      <p className="mt-1 text-xs text-slate-500">
        Recorded by {r.createdBy} on {date(r.createdAt)}
        {r.approvedBy ? `, approved by ${r.approvedBy}` : ''}
        {r.note ? `. ${r.note}` : ''}
      </p>
      {open && (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <button
            type="button"
            className="btn-ghost py-0.5 text-xs"
            onClick={() => api.privacyFind(r.identifiers).then(setFinding, (e) => setError(toError(e)))}
          >
            <IconSearch className="h-3.5 w-3.5" aria-hidden="true" /> What is held
          </button>
          {(r.kind === 'access' || r.kind === 'portability') && (
            <a className="btn-ghost py-0.5 text-xs" href={`/api/privacy/requests/${r.id}/export`} download>
              <IconDownload className="h-3.5 w-3.5" aria-hidden="true" /> Download the copy (zip)
            </a>
          )}
          {r.kind === 'erasure' && r.status === 'open' && (
            <button
              type="button"
              className="btn-ghost py-0.5 text-xs"
              disabled={r.createdBy === me}
              title={r.createdBy === me ? 'Another administrator approves an erasure you recorded' : undefined}
              onClick={() => void act(api.privacyApprove(r.id), 'Approved.')}
            >
              Approve the erasure
            </button>
          )}
          {r.kind === 'erasure' && r.status === 'approved' && (
            <button
              type="button"
              className="btn-ghost py-0.5 text-xs text-rose-300"
              onClick={() => {
                if (window.confirm('Erase everything this server holds about the person? It cannot be undone; backups keep it until they expire.'))
                  void act(api.privacyErase(r.id), 'Erased.')
              }}
            >
              <IconDelete className="h-3.5 w-3.5" aria-hidden="true" /> Erase now
            </button>
          )}
          {r.kind !== 'erasure' && (
            <button type="button" className="btn-ghost py-0.5 text-xs" onClick={() => void act(api.privacyClose(r.id, 'done', reason), 'Closed as done.')}>
              Mark answered
            </button>
          )}
          <input
            className="input w-64 py-0.5 text-xs"
            placeholder="reason, for a refusal or a note"
            aria-label={`Reason or note for request ${r.id}`}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
          <button type="button" className="btn-ghost py-0.5 text-xs" onClick={() => void act(api.privacyClose(r.id, 'refused', reason), 'Refused.')}>
            Refuse
          </button>
          <span role="status" className="text-xs text-emerald-300">
            {said}
          </span>
        </div>
      )}
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      {finding && <FindingView f={finding} />}
    </li>
  )
}

function Holds({ summary, onChanged }: { summary: PrivacySummary; onChanged: () => void }) {
  const [idents, setIdents] = useState('')
  const [reason, setReason] = useState('')
  const [until, setUntil] = useState('')
  const [error, setError] = useState<UiError | null>(null)
  const place = () =>
    api.privacyHold(splitIdents(idents), reason, until).then(
      () => {
        setIdents('')
        setReason('')
        setUntil('')
        setError(null)
        onChanged()
      },
      (e) => setError(toError(e)),
    )
  return (
    <Section
      title="Legal holds"
      icon={IconLock}
      description="A hold keeps a person's data from erasure and from the retention window, for a claim, an investigation or a statutory duty."
    >
      <ul className="space-y-2" data-testid="privacy-holds">
        {summary.holds.length === 0 && <li className="text-sm text-slate-500">None.</li>}
        {summary.holds.map((h) => (
          <li key={h.id} className="flex flex-wrap items-center gap-3 rounded-lg border border-slate-800 px-3 py-2 text-sm">
            <span className={h.liftedAt ? 'text-slate-500' : 'text-amber-300'}>{h.reason}</span>
            <span className="font-mono text-[11px] text-slate-400">{h.identifiers.join(', ')}</span>
            <span className="text-xs text-slate-500">
              by {h.createdBy} {date(h.createdAt)}
              {h.until ? `, until ${date(h.until)}` : ''}
              {h.liftedAt ? `; lifted by ${h.liftedBy} ${date(h.liftedAt)}` : ''}
            </span>
            {!h.liftedAt && (
              <button type="button" className="btn-ghost py-0.5 text-xs" onClick={() => api.privacyLift(h.id).then(onChanged, (e) => setError(toError(e)))}>
                Lift
              </button>
            )}
          </li>
        ))}
      </ul>
      <div className="mt-3 grid gap-3 sm:grid-cols-3">
        <Field label="Identifiers" hint="One per line, as for a request.">
          <textarea className="input font-mono text-xs" rows={2} value={idents} onChange={(e) => setIdents(e.target.value)} />
        </Field>
        <Field label="Reason">
          <input className="input" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="claim 2026/17" />
        </Field>
        <Field label="Until" hint="Empty: until lifted.">
          <input type="date" className="input" value={until} onChange={(e) => setUntil(e.target.value)} />
        </Field>
      </div>
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      <button type="button" className="btn-primary mt-3 py-1 text-xs" disabled={!idents.trim() || !reason.trim()} onClick={() => void place()}>
        Place the hold
      </button>
    </Section>
  )
}

export function DataProtection() {
  const me = useSelector((s: RootState) => s.session.me)
  const [summary, setSummary] = useState<PrivacySummary | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [reapplied, setReapplied] = useState('')
  const load = useCallback(() => {
    api.privacySummary().then(
      (s) => {
        setSummary(s)
        setError(null)
      },
      (e) => setError(toError(e)),
    )
  }, [])
  useEffect(() => load(), [load])
  if (!summary) return error ? <ErrorBox error={error} /> : <Spinner />
  const open = summary.requests.filter((r) => r.status === 'open' || r.status === 'approved')
  const closed = summary.requests.filter((r) => !(r.status === 'open' || r.status === 'approved'))
  return (
    <div className="space-y-6">
      <Section
        title="Data protection"
        icon={IconShield}
        description="Requests under the GDPR and the UK GDPR: what is held about a person, a copy for them, erasure, restriction. Administrators only; every step is audited with the person's numbers masked."
      >
        <div className="grid gap-3 text-sm sm:grid-cols-3">
          <div>
            <p className="text-xs uppercase tracking-wide text-slate-500">Where the data is kept</p>
            <p className={summary.location ? 'text-slate-200' : 'text-amber-300'}>
              {summary.location || 'Not stated: Settings → Data → Data protection'}
            </p>
          </div>
          <div>
            <p className="text-xs uppercase tracking-wide text-slate-500">Searched</p>
            <p className="text-slate-200">{summary.stores.join(', ') || 'nothing: no stores'}</p>
          </div>
          <div>
            <p className="text-xs uppercase tracking-wide text-slate-500">Open requests</p>
            <p className={open.some(overdue) ? 'text-rose-300' : 'text-slate-200'}>
              {open.length}
              {open.some(overdue) ? `, ${open.filter(overdue).length} overdue` : ''}
            </p>
          </div>
        </div>
        <div className="mt-3 flex flex-wrap gap-2">
          <a className="btn-ghost py-1 text-xs" href="/api/privacy/register" download>
            <IconDownload className="h-3.5 w-3.5" aria-hidden="true" /> Record of processing (Markdown)
          </a>
          <a className="btn-ghost py-1 text-xs" href="/api/privacy/register?format=csv" download>
            <IconDownload className="h-3.5 w-3.5" aria-hidden="true" /> Record of processing (CSV)
          </a>
          <button
            type="button"
            className="btn-ghost py-1 text-xs"
            onClick={() => {
              if (window.confirm('Apply every erasure in the ledger again? Do this after restoring a backup made before them.'))
                api.privacyReapply().then(
                  (r) => {
                    const n = Object.values(r.counts).reduce((a, b) => a + b, 0)
                    setReapplied(n === 0 ? 'Nothing erased before was found.' : `Erased again: ${Object.entries(r.counts).map(([k, v]) => `${k} ${v}`).join(', ')}.`)
                    load()
                  },
                  (e) => setError(toError(e)),
                )
            }}
          >
            Apply erasures again (after a restore)
          </button>
          <span role="status" className="self-center text-xs text-emerald-300">
            {reapplied}
          </span>
        </div>
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      </Section>

      <NewRequest onCreated={load} />

      <Section title="Requests" icon={IconShield} description="Open ones first, soonest due first.">
        <ul className="space-y-2" data-testid="privacy-requests">
          {open.length === 0 && <li className="text-sm text-slate-500">No open requests.</li>}
          {open.map((r) => (
            <RequestRow key={r.id} r={r} me={me?.username ?? ''} onChanged={load} />
          ))}
        </ul>
        {closed.length > 0 && (
          <details className="mt-3">
            <summary className="cursor-pointer text-xs text-slate-400">Answered ({closed.length})</summary>
            <ul className="mt-2 space-y-2">
              {closed.map((r) => (
                <RequestRow key={r.id} r={r} me={me?.username ?? ''} onChanged={load} />
              ))}
            </ul>
          </details>
        )}
      </Section>

      <Holds summary={summary} onChanged={load} />

      <Section title="Retention" icon={IconShield} description="How long each kind of record is kept on this server.">
        <table className="w-full text-sm" data-testid="privacy-retention">
          <tbody className="divide-y divide-slate-800">
            {summary.retention.map((r) => (
              <tr key={r.category}>
                <td className="py-1.5 pr-3 text-slate-300">{r.category}</td>
                <td className="py-1.5 pr-3 text-slate-200">{r.kept}</td>
                <td className="py-1.5 text-xs text-slate-500">{r.setting ? `Settings: ${r.setting}` : 'not deleted on a schedule'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>

      <Section title="Erasure ledger" icon={IconShield} description="Each erasure, without the person's numbers: salted hashes let it be applied again to a restored copy.">
        <ul className="space-y-1 text-sm" data-testid="privacy-ledger">
          {summary.erasures.length === 0 && <li className="text-slate-500">No erasures yet.</li>}
          {summary.erasures.map((e) => (
            <li key={e.id} className="text-slate-300">
              {date(e.at)}: request #{e.requestId}, by {e.actor}, approved by {e.approver}:{' '}
              <span className="text-slate-400">
                {Object.entries(e.counts)
                  .map(([k, v]) => `${k} ${v}`)
                  .join(', ') || 'nothing was held'}
              </span>
            </li>
          ))}
        </ul>
      </Section>
    </div>
  )
}
