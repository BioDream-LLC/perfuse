import { useCallback, useEffect, useState } from 'react'
import { useSelector } from 'react-redux'
import { api, ApiError, atLeast } from './api'
import type { MPIComparison, MPIMatch, MPIPerson, MPIPersonView, MPIReview, MPISettings, MPISummary } from './api'
import type { RootState, UiError } from './store'
import { ErrorBox, Field, Section, Spinner } from './ui'
import { IconCheck, IconPatientIndex, IconSearch, IconSettings, IconWarning } from './Icons'

/**
 * The patient index: one enterprise identifier (EUID) per person, the records each source system holds for them, the
 * possible matches waiting for a person to decide, links and merges by hand, and the matching settings.
 *
 * Every number here is a patient's, so every read is audited on the server, and the screen shows only what it was asked for.
 */

function toError(e: unknown): UiError {
  return { message: e instanceof Error ? e.message : String(e), problems: e instanceof ApiError ? e.problems : [] }
}

function nameOf(p: MPIPerson): string {
  const r = p.records[0]
  if (!r) return p.euid
  const n = [r.family, r.given].filter(Boolean).join(', ')
  return n || p.euid
}

const resultColour: Record<string, string> = {
  agree: 'border-emerald-800 bg-emerald-950/40 text-emerald-300',
  partial: 'border-amber-800 bg-amber-950/40 text-amber-300',
  disagree: 'border-rose-800 bg-rose-950/40 text-rose-300',
  missing: 'border-slate-800 bg-slate-900 text-slate-500',
}

const fieldLabel: Record<string, string> = {
  family: 'family name',
  given: 'given name',
  birth_date: 'birth date',
  sex: 'sex',
  postal: 'postal code',
  phone: 'phone',
  line: 'address',
  identifier: 'identifier',
}

/** Why shows how each field compared, coloured by what it means. */
function Why({ why }: { why: MPIComparison }) {
  return (
    <div className="flex flex-wrap gap-1.5" aria-label="How the fields compared">
      {why.fields.map((f) => (
        <span key={f.field} className={`rounded border px-1.5 py-0.5 text-[11px] ${resultColour[f.result] ?? ''}`} title={`weight ${f.weight}`}>
          {fieldLabel[f.field] ?? f.field}: {f.result}
        </span>
      ))}
    </div>
  )
}

function Score({ score, grade }: { score: number; grade?: string }) {
  const colour = score >= 0.9 ? 'text-emerald-300' : score >= 0.6 ? 'text-amber-300' : 'text-slate-400'
  return (
    <span className={`font-mono text-sm ${colour}`}>
      {(score * 100).toFixed(0)}%{grade && <span className="ml-1 text-[11px] uppercase tracking-wide">{grade}</span>}
    </span>
  )
}

function RecordRow({ r, actions }: { r: MPIPerson['records'][number]; actions?: React.ReactNode }) {
  return (
    <tr className="align-top">
      <td className="py-1.5 pr-3 font-mono text-xs text-sky-300">
        {r.system}
        <br />
        <span className="text-slate-200">{r.value}</span>
      </td>
      <td className="py-1.5 pr-3 text-slate-200">{[r.family, r.given].filter(Boolean).join(', ') || '—'}</td>
      <td className="py-1.5 pr-3 font-mono text-xs text-slate-300">{r.birthDate || '—'}</td>
      <td className="py-1.5 pr-3 text-xs text-slate-300">{r.sex || '—'}</td>
      <td className="py-1.5 pr-3 text-xs text-slate-400">{[r.line, r.city, r.postal].filter(Boolean).join(', ') || '—'}</td>
      <td className="py-1.5 pr-3 text-xs text-slate-500">{r.source || ''}</td>
      <td className="py-1.5 text-right">{actions}</td>
    </tr>
  )
}

function RecordsTable({ person, children }: { person: MPIPerson; children?: (r: MPIPerson['records'][number]) => React.ReactNode }) {
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left text-[11px] uppercase tracking-wide text-slate-500">
          <th className="py-1 pr-3">System and number</th>
          <th className="py-1 pr-3">Name</th>
          <th className="py-1 pr-3">Born</th>
          <th className="py-1 pr-3">Sex</th>
          <th className="py-1 pr-3">Address</th>
          <th className="py-1 pr-3">From</th>
          <th />
        </tr>
      </thead>
      <tbody className="divide-y divide-slate-800">
        {person.records.map((r) => (
          <RecordRow key={r.id} r={r} actions={children?.(r)} />
        ))}
      </tbody>
    </table>
  )
}

function PersonDetail({ euid, canEdit, onChanged }: { euid: string; canEdit: boolean; onChanged: () => void }) {
  const [view, setView] = useState<MPIPersonView | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [mergeInto, setMergeInto] = useState('')
  const [linkTo, setLinkTo] = useState<Record<number, string>>({})
  const [note, setNote] = useState('')

  const load = useCallback(() => {
    api.mpiPerson(euid).then(setView, (e) => setError(toError(e)))
  }, [euid])
  useEffect(load, [load])

  const act = (p: Promise<unknown>, done: string) =>
    p.then(
      () => {
        setNote(done)
        setError(null)
        load()
        onChanged()
      },
      (e) => setError(toError(e)),
    )

  if (!view) return error ? <ErrorBox error={error} /> : <Spinner />
  return (
    <div className="space-y-3 rounded-xl border border-slate-800 p-3" data-testid="mpi-person">
      <div className="flex flex-wrap items-baseline gap-3">
        <span className="font-mono text-sm text-emerald-300">{view.person.euid}</span>
        <span className="text-slate-200">{[view.golden.family, view.golden.given].filter(Boolean).join(', ')}</span>
        <span className="font-mono text-xs text-slate-400">{view.golden.birthDate}</span>
        <span className="text-xs text-slate-500">
          {view.person.records.length} record{view.person.records.length === 1 ? '' : 's'}
        </span>
      </div>
      {note && (
        <p role="status" className="text-xs text-emerald-300">
          {note}
        </p>
      )}
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      <RecordsTable person={view.person}>
        {(r) =>
          canEdit && (
            <div className="flex flex-col items-end gap-1">
              {view.person.records.length > 1 && (
                <button
                  type="button"
                  className="btn-ghost py-0.5 text-xs text-rose-300"
                  onClick={() => {
                    const why = window.prompt('Why is this record not the same person?') ?? ''
                    if (why !== null) void act(api.mpiUnlink(r.id, why), `${r.value} is now a person of its own.`)
                  }}
                >
                  Not this person
                </button>
              )}
              <span className="flex gap-1">
                <input
                  aria-label={`Move ${r.value} to the person with EUID`}
                  className="input w-36 py-0.5 font-mono text-[11px]"
                  placeholder="move to EUID"
                  value={linkTo[r.id] ?? ''}
                  onChange={(e) => setLinkTo((m) => ({ ...m, [r.id]: e.target.value }))}
                />
                <button
                  type="button"
                  className="btn-ghost py-0.5 text-xs"
                  disabled={!linkTo[r.id]?.trim()}
                  onClick={() => void act(api.mpiLink(r.id, linkTo[r.id]!.trim(), 'moved by hand'), `${r.value} moved.`)}
                >
                  Move
                </button>
              </span>
            </div>
          )
        }
      </RecordsTable>
      <div className="text-xs text-slate-400">
        <span className="text-slate-500">Identifiers:</span>{' '}
        {view.identifiers.map((i) => (
          <code key={i} className="mr-2 font-mono text-sky-300">
            {i}
          </code>
        ))}
      </div>
      {canEdit && (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span className="text-slate-400">Merge this person into</span>
          <input
            aria-label="Merge into the person with EUID"
            className="input w-44 py-0.5 font-mono text-xs"
            value={mergeInto}
            onChange={(e) => setMergeInto(e.target.value)}
            placeholder="EUID"
          />
          <button
            type="button"
            className="btn-ghost py-0.5 text-xs"
            disabled={!mergeInto.trim()}
            onClick={() => {
              if (window.confirm(`Merge every record of ${euid} into ${mergeInto.trim()}?`))
                void api.mpiMerge(euid, mergeInto.trim(), 'merged by hand').then(onChanged, (e) => setError(toError(e)))
            }}
          >
            Merge
          </button>
        </div>
      )}
      <details>
        <summary className="cursor-pointer text-xs text-slate-400">History ({view.history.length})</summary>
        <ul className="mt-2 space-y-1 text-xs">
          {view.history.map((h, i) => (
            <li key={i} className="flex gap-2">
              <span className="w-40 shrink-0 text-slate-500">{new Date(h.at).toLocaleString()}</span>
              <span className="w-20 shrink-0 text-violet-300">{h.action}</span>
              <span className="text-slate-300">
                {h.detail} <span className="text-slate-500">by {h.actor}</span>
              </span>
            </li>
          ))}
        </ul>
      </details>
    </div>
  )
}

function Reviews({ onChanged }: { onChanged: () => void }) {
  const [list, setList] = useState<MPIReview[] | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const load = useCallback(() => {
    api.mpiReviews().then((r) => setList(r.reviews), (e) => setError(toError(e)))
  }, [])
  useEffect(load, [load])
  const decide = (id: number, same: boolean) =>
    api.mpiDecide(id, same).then(
      () => {
        load()
        onChanged()
      },
      (e) => setError(toError(e)),
    )
  if (!list) return error ? <ErrorBox error={error} /> : <Spinner />
  return (
    <Section
      title="Possible matches to review"
      icon={IconWarning}
      description="Records that may be a person the index already has, not certain enough to link on their own. Decide each: the same person links them; different people keeps them apart and stops the pair being offered again."
    >
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      {list.length === 0 ? (
        <p className="text-sm text-emerald-400">Nothing to review.</p>
      ) : (
        <ul className="space-y-4" data-testid="mpi-reviews">
          {list.map((rv) => (
            <li key={rv.id} className="space-y-2 rounded-xl border border-slate-800 p-3">
              <div className="flex flex-wrap items-center gap-3">
                <Score score={rv.score} />
                <Why why={rv.why} />
              </div>
              <div className="grid gap-3 lg:grid-cols-2">
                <div>
                  <p className="mb-1 text-[11px] uppercase tracking-wide text-slate-500">This record arrived</p>
                  <RecordsTable person={{ euid: rv.record.euid ?? '', records: [rv.record], created: '' }} />
                </div>
                <div>
                  <p className="mb-1 text-[11px] uppercase tracking-wide text-slate-500">
                    It may be <span className="font-mono text-emerald-300 normal-case">{rv.candidate.euid}</span>
                  </p>
                  <RecordsTable person={rv.candidate} />
                </div>
              </div>
              <div className="flex gap-2">
                <button type="button" className="btn-primary py-1 text-xs" onClick={() => void decide(rv.id, true)}>
                  <IconCheck className="h-3.5 w-3.5" aria-hidden="true" /> The same person
                </button>
                <button type="button" className="btn-ghost py-1 text-xs" onClick={() => void decide(rv.id, false)}>
                  Different people
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}

function Find() {
  const [q, setQ] = useState({ family: '', given: '', birthDate: '', sex: '', postal: '', identifier: '' })
  const [found, setFound] = useState<MPIMatch[] | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const run = () =>
    api
      .mpiFind({
        family: q.family || undefined,
        given: q.given || undefined,
        birthDate: q.birthDate || undefined,
        sex: q.sex || undefined,
        postal: q.postal || undefined,
        identifiers: q.identifier.includes('|') ? [q.identifier.trim()] : undefined,
      })
      .then((r) => setFound(r.matches), (e) => setError(toError(e)))
  return (
    <Section
      title="Find a person"
      icon={IconSearch}
      description="By what you know: a name, a birth date, an identifier as system|value. Scored the way the index matches, the same as Patient/$match answers."
    >
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label="Family name">
          <input className="input" value={q.family} onChange={(e) => setQ({ ...q, family: e.target.value })} />
        </Field>
        <Field label="Given name">
          <input className="input" value={q.given} onChange={(e) => setQ({ ...q, given: e.target.value })} />
        </Field>
        <Field label="Birth date">
          <input type="date" className="input" value={q.birthDate} onChange={(e) => setQ({ ...q, birthDate: e.target.value })} />
        </Field>
        <Field label="Sex">
          <select className="input" value={q.sex} onChange={(e) => setQ({ ...q, sex: e.target.value })}>
            <option value="">any</option>
            <option value="F">female</option>
            <option value="M">male</option>
            <option value="O">other</option>
          </select>
        </Field>
        <Field label="Postal code">
          <input className="input" value={q.postal} onChange={(e) => setQ({ ...q, postal: e.target.value })} />
        </Field>
        <Field label="Identifier (system|value)">
          <input className="input font-mono text-xs" value={q.identifier} onChange={(e) => setQ({ ...q, identifier: e.target.value })} />
        </Field>
      </div>
      <button type="button" className="btn-primary mt-3 py-1 text-xs" onClick={() => void run()}>
        <IconSearch className="h-3.5 w-3.5" aria-hidden="true" /> Find
      </button>
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      {found && (
        <ul className="mt-3 space-y-2" data-testid="mpi-find-results">
          {found.length === 0 && <li className="text-sm text-slate-500">No one like that.</li>}
          {found.map((m) => (
            <li key={m.person.euid} className="flex flex-wrap items-center gap-3 rounded-lg border border-slate-800 px-3 py-2">
              <Score score={m.score} grade={m.grade} />
              <span className="font-mono text-xs text-emerald-300">{m.person.euid}</span>
              <span className="text-sm text-slate-200">{nameOf(m.person)}</span>
              <Why why={m.why} />
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}

function SettingsPanel({ settings, onSaved }: { settings: MPISettings; onSaved: () => void }) {
  const [s, setS] = useState(settings)
  const [trusted, setTrusted] = useState(settings.trusted.join('\n'))
  const [error, setError] = useState<UiError | null>(null)
  const [saved, setSaved] = useState(false)
  const save = () =>
    api
      .mpiSaveSettings({
        ...s,
        trusted: trusted
          .split('\n')
          .map((x) => x.trim())
          .filter(Boolean),
      })
      .then(
        () => {
          setSaved(true)
          setError(null)
          onSaved()
        },
        (e) => setError(toError(e)),
      )
  return (
    <Section title="How the index matches" icon={IconSettings} description="For administrators. A change applies to records registered from now on.">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          label={`Link without review at ${(s.autoLink * 100).toFixed(0)}%`}
          hint="And only when family name and birth date both agree, and no second person scores as high. Higher links fewer on their own."
        >
          <input
            type="range"
            min={0.7}
            max={1}
            step={0.01}
            className="w-full"
            value={s.autoLink}
            onChange={(e) => setS({ ...s, autoLink: Number(e.target.value) })}
          />
        </Field>
        <Field label={`Queue for review from ${(s.review * 100).toFixed(0)}%`} hint="Below this, a record becomes a new person without asking.">
          <input
            type="range"
            min={0.3}
            max={s.autoLink}
            step={0.01}
            className="w-full"
            value={s.review}
            onChange={(e) => setS({ ...s, review: Number(e.target.value) })}
          />
        </Field>
        <Field
          label="Identifier systems trusted to be one per person"
          hint="Records sharing one of these link at once, with no scoring: a national identifier, an enterprise number. Never a payer's subscriber number, which a family shares."
        >
          <textarea className="input font-mono text-xs" rows={3} value={trusted} onChange={(e) => setTrusted(e.target.value)} placeholder="http://hl7.org/fhir/sid/us-ssn" />
        </Field>
        <Field label="System for the enterprise identifier" hint="How the EUID is named in FHIR and PIX answers.">
          <input className="input font-mono text-xs" value={s.euidSystem} onChange={(e) => setS({ ...s, euidSystem: e.target.value })} />
        </Field>
      </div>
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      <div className="mt-3 flex items-center gap-3">
        <button type="button" className="btn-primary py-1 text-xs" onClick={() => void save()}>
          Save
        </button>
        {saved && (
          <span role="status" className="text-xs text-emerald-300">
            Saved.
          </span>
        )}
      </div>
    </Section>
  )
}

export function PatientIndex() {
  const me = useSelector((s: RootState) => s.session.me)
  const canEdit = atLeast(me?.role, 'editor')
  const isAdmin = atLeast(me?.role, 'admin')
  const [data, setData] = useState<MPISummary | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [q, setQ] = useState('')
  const [open, setOpen] = useState<string | null>(null)
  const [reload, setReload] = useState(0)

  useEffect(() => {
    api.mpiSummary(q).then(setData, (e) => setError(toError(e)))
  }, [q, reload])
  const changed = () => setReload((n) => n + 1)

  if (!data) return error ? <ErrorBox error={error} /> : <Spinner />
  if (!data.enabled) {
    return (
      <Section title="Patient index" icon={IconPatientIndex}>
        <p className="text-sm text-slate-300">This server keeps no patient index.</p>
        <p className="mt-2 text-sm text-slate-400">
          A patient index links the records every system keeps for a person into one enterprise identifier, queues the matches it is
          not sure of for somebody to decide, and answers Patient/$match and PIXm. It holds patients' names, birth dates and
          identifiers, so it is off until somebody decides to keep one: turn on Settings → Data → Patient index (or start the server
          with -mpi), restart, and add a patient index destination to the channels that carry registrations.
        </p>
      </Section>
    )
  }

  const c = data.counts
  return (
    <div className="space-y-8">
      <Section
        title="Patient index"
        icon={IconPatientIndex}
        description="One enterprise identifier per person, across every system that registers patients here. Every search and every record read is audited."
      >
        <div className="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-4">
          {[
            ['people', c.persons, 'text-emerald-300'],
            ['records', c.records, 'text-sky-300'],
            ['people with several records', c.linked, 'text-violet-300'],
            ['to review', c.reviews, c.reviews > 0 ? 'text-amber-300' : 'text-slate-400'],
          ].map(([label, n, colour]) => (
            <div key={label as string} className="rounded-xl border border-slate-800 p-3">
              <p className={`text-2xl font-semibold ${colour}`}>{(n as number).toLocaleString()}</p>
              <p className="text-xs text-slate-500">{label}</p>
            </div>
          ))}
        </div>
        <input
          aria-label="Search the patient index"
          className="input mb-3 w-full"
          placeholder="name, number, EUID or birth date (YYYY-MM-DD)"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <ul className="divide-y divide-slate-800" data-testid="mpi-persons">
          {data.persons.length === 0 && <li className="py-2 text-sm text-slate-500">No one{q ? ' matches' : ' registered yet'}.</li>}
          {data.persons.map((p) => (
            <li key={p.euid} className="py-2">
              <button type="button" className="flex w-full flex-wrap items-baseline gap-3 text-left" onClick={() => setOpen(open === p.euid ? null : p.euid)}>
                <span className="font-mono text-xs text-emerald-300">{p.euid}</span>
                <span className="text-sm text-slate-200">{nameOf(p)}</span>
                <span className="font-mono text-xs text-slate-400">{p.records[0]?.birthDate}</span>
                <span className="text-xs text-slate-500">
                  {p.records.length} record{p.records.length === 1 ? '' : 's'} · {[...new Set(p.records.map((r) => r.source || r.system))].join(', ')}
                </span>
              </button>
              {open === p.euid && (
                <div className="mt-2">
                  <PersonDetail euid={p.euid} canEdit={canEdit} onChanged={changed} />
                </div>
              )}
            </li>
          ))}
        </ul>
      </Section>
      {canEdit && <Reviews key={reload} onChanged={changed} />}
      <Find />
      {isAdmin && <SettingsPanel settings={data.settings} onSaved={changed} />}
    </div>
  )
}
