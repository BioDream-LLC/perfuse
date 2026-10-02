import { useState } from 'react'
import { api, type IdentitySearchResult } from './api'
import { OutcomeBadge } from './Messages'

// Finding a message by what somebody knows about it.
//
// One box. An MRN, a patient's name, a date of birth, an accession number, a claim number - typed
// however the person has it written down - and every message about it comes back, whatever format
// it arrived in.
//
// This is deliberately not the content search next to it. That one is powerful and asks the person
// to know that identity lives at PID-3 in an HL7 v2 admission and somewhere else entirely in a FHIR
// resource, a DICOM instance or an 837 claim. The question being asked here is not "which field"
// but "where is this patient", and the answer should not require knowing four vocabularies.
//
// It is also not a scan. The identifiers were pulled out when each message was recorded, so this is
// an index lookup and the result is the whole history rather than the recent part of it - which is
// why there is no "examined" count here and there is one on the panel beside it.

/** What the kinds are called in the interface, rather than what the database calls them. */
const KIND_LABELS: Record<string, string> = {
  patient_id: 'Patient ID or MRN',
  patient_name: 'Patient name',
  birth_date: 'Date of birth',
  account: 'Account or visit',
  accession: 'Accession or order',
  claim: 'Claim',
  study_uid: 'Study UID',
}

function kindLabel(kind: string): string {
  return KIND_LABELS[kind] ?? kind
}

const EXAMPLES = [
  'MRN0012345',
  'Sampleson, Bravo',
  '1970-01-01',
  'ACC77281',
]

export function FindPatient({ onOpen }: { onOpen?: (id: number) => void }) {
  const [term, setTerm] = useState('')
  const [kind, setKind] = useState('')
  const [result, setResult] = useState<IdentitySearchResult | null>(null)
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // The term the result belongs to, so the message below a stale result cannot describe a
  // search the person has already retyped.
  const [searched, setSearched] = useState('')

  async function run() {
    if (!term.trim()) return
    setRunning(true)
    setError(null)
    setResult(null)
    try {
      const res = await api.findMessages({ term, kind: kind || undefined })
      setResult(res)
      setSearched(term)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-end gap-2">
        <label className="min-w-[16rem] flex-1">
          <span className="text-xs font-medium text-slate-300">
            Find messages about
          </span>
          <input
            className="input mt-1"
            value={term}
            spellCheck={false}
            placeholder="an MRN, a name, a date of birth, an accession number…"
            onChange={(e) => setTerm(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void run()
            }}
          />
        </label>

        <label>
          <span className="text-xs font-medium text-slate-300">Looking in</span>
          <select
            className="input mt-1"
            value={kind}
            onChange={(e) => setKind(e.target.value)}
          >
            {/* Anything, first and default, because the person often does not know which
                sort of number they are holding - which is the whole reason for this panel. */}
            <option value="">anything</option>
            {(result?.kinds ?? Object.keys(KIND_LABELS)).map((k) => (
              <option key={k} value={k}>
                {kindLabel(k)}
              </option>
            ))}
          </select>
        </label>

        <button
          className="btn-primary"
          disabled={running || !term.trim()}
          onClick={() => void run()}
        >
          {running ? 'Searching…' : 'Find'}
        </button>
      </div>

      <div className="flex flex-wrap items-center gap-2 text-xs text-slate-500">
        <span>For example</span>
        {EXAMPLES.map((ex) => (
          <button
            key={ex}
            className="rounded border border-slate-700 px-2 py-0.5 font-mono text-slate-300 hover:border-slate-500"
            onClick={() => setTerm(ex)}
          >
            {ex}
          </button>
        ))}
      </div>

      <p className="text-xs text-slate-500">
        Matched on the value however it is written: a message holding{' '}
        <code className="font-mono">SAMPLESON^BRAVO</code> is found by typing{' '}
        <code className="font-mono">Sampleson, Bravo</code>. Works across HL7 v2, FHIR, DICOM
        and X12 without naming a field in any of them.
      </p>

      {error && <p className="text-sm text-rose-300">{error}</p>}

      {result && <Results result={result} term={searched} onOpen={onOpen} />}
    </div>
  )
}

function Results({
  result,
  term,
  onOpen,
}: {
  result: IdentitySearchResult
  term: string
  onOpen?: (id: number) => void
}) {
  // Nothing found, and which kind of nothing it is.
  //
  // These two read identically to somebody looking at an empty table and mean opposite things.
  // Saying "no messages mention this patient" when the index was never built is telling somebody
  // a patient was never seen here, which is a clinical conclusion drawn from a settings toggle.
  if (result.total === 0) {
    if (!result.indexed) {
      return (
        <p className="text-sm text-amber-300">
          Patient identifiers are not being indexed, so this search has nothing to look in.
          This result does not mean no message mentions <b>{term}</b>. Turn on{' '}
          <b>Index patient identifiers</b> in Settings under Data; messages recorded from then
          on can be found this way.
        </p>
      )
    }
    return (
      <p className="text-sm text-slate-400">
        No message mentions <b>{term}</b>.
      </p>
    )
  }

  return (
    <div className="space-y-2">
      <p className="text-xs text-slate-400">
        {result.total} {result.total === 1 ? 'message' : 'messages'} mention{' '}
        <b>{term}</b>
        {result.matches.length < result.total &&
          `, showing the most recent ${result.matches.length}`}
        .
      </p>

      <div className="card overflow-hidden">
        <table className="w-full text-sm">
          <thead className="border-b border-slate-800 bg-slate-900/60 text-left">
            <tr className="text-xs tracking-wide text-slate-500 uppercase">
              <th className="px-3 py-2">Received</th>
              <th className="px-3 py-2">Channel</th>
              <th className="px-3 py-2">Type</th>
              <th className="px-3 py-2">Matched</th>
              <th className="px-3 py-2">Outcome</th>
            </tr>
          </thead>
          <tbody>
            {result.matches.map((m) => (
              <tr
                key={m.message.id}
                className="cursor-pointer border-b border-slate-800/60 hover:bg-slate-900/40"
                onClick={() => onOpen?.(m.message.id)}
              >
                <td className="px-3 py-2 font-mono text-xs whitespace-nowrap">
                  {new Date(m.message.receivedAt).toLocaleString()}
                </td>
                <td className="px-3 py-2">{m.message.channel}</td>
                <td className="px-3 py-2 font-mono text-xs">
                  {m.message.messageType}
                  {m.message.triggerEvent ? `^${m.message.triggerEvent}` : ''}
                </td>
                {/* Why this row is here. A page of otherwise identical ADT messages gives no
                    clue otherwise, and "it matched the account number, not the MRN" changes
                    what somebody does next. */}
                <td className="px-3 py-2 text-xs">
                  <span className="text-slate-400">{kindLabel(m.matchedKind)}</span>{' '}
                  <span className="font-mono text-slate-300">{m.matchedValue}</span>
                </td>
                <td className="px-3 py-2">
                  <OutcomeBadge outcome={m.message.outcome} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
