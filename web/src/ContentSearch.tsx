import { useState } from 'react'
import { api, type ContentSearchResult, type StoredMessage } from './api'
import { OutcomeBadge } from './Messages'

// Searching by what is inside the messages.
//
// Separate from the ordinary filters, because it is a different kind of question and has a different
// cost. The filters above are indexed lookups; this parses messages. Mixing them into one panel
// would hide that a search can take a moment and can look at only part of the history.
//
// The expression language is the one channel filters use, which is the point. Somebody who works out
// here that a mapping is missing a code can paste the same expression into the channel and be sure
// it means the same thing.

const EXAMPLES = [
  { label: 'a specific patient', where: "PID-3 == '1234567'" },
  { label: 'sex code not mapped', where: "PID-8 in ['1', '2']" },
  { label: 'no date of birth', where: 'PID-7 is empty' },
  { label: 'admissions only', where: "MSH-9.2 == 'A01'" },
  { label: 'a field that should never be sent', where: 'PID-19 exists' },
]

export function ContentSearch({
  onOpen,
}: {
  /** Called with a message id when a result is clicked, so the detail view can open it. */
  onOpen?: (id: number) => void
}) {
  const [where, setWhere] = useState('')
  const [result, setResult] = useState<ContentSearchResult | null>(null)
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [examine, setExamine] = useState(2000)

  async function run() {
    if (!where.trim()) return
    setRunning(true)
    setError(null)
    setResult(null)
    try {
      setResult(await api.searchMessages({ where, examine }))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-end gap-2">
        <label className="min-w-[18rem] flex-1">
          <span className="text-xs font-medium text-slate-300">
            Find messages where
          </span>
          <input
            className="input mt-1 font-mono text-xs"
            value={where}
            spellCheck={false}
            placeholder="PID-8 in ['1', '2'] and MSH-9.2 == 'A01'"
            onChange={(e) => setWhere(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void run()
            }}
          />
        </label>

        <label>
          <span className="text-xs font-medium text-slate-300">Look at</span>
          <select
            className="select mt-1"
            value={examine}
            onChange={(e) => setExamine(Number(e.target.value))}
          >
            <option value={500}>the last 500</option>
            <option value={2000}>the last 2,000</option>
            <option value={10000}>the last 10,000</option>
            <option value={50000}>the last 50,000</option>
          </select>
        </label>

        <button type="button" className="btn-primary" disabled={running || !where.trim()} onClick={run}>
          {running ? 'searching…' : 'Search'}
        </button>
      </div>

      <p className="text-xs leading-relaxed text-slate-500">
        This is the same language channel filters use, so anything that works here can be pasted
        straight into a channel. It reads and parses messages rather than using an index, so it looks
        at recent traffic first and tells you how far back it got.
      </p>

      <div className="flex flex-wrap gap-2">
        {EXAMPLES.map((e) => (
          <button
            key={e.label}
            type="button"
            className="rounded-md border border-slate-700 px-2 py-1 text-xs text-slate-300 hover:border-sky-600 hover:text-sky-300"
            onClick={() => setWhere(e.where)}
            title={e.where}
          >
            {e.label}
          </button>
        ))}
      </div>

      {error && <p className="text-xs text-rose-400">{error}</p>}

      {result && <Results result={result} onOpen={onOpen} />}
    </div>
  )
}

function Results({
  result,
  onOpen,
}: {
  result: ContentSearchResult
  onOpen?: (id: number) => void
}) {
  // Defended rather than trusted. The server now always sends an array, but a browser talking to an
  // older build would get null here, and the failure would be a blank screen on the search that
  // found nothing - the one most likely to be run.
  const matches = result.matches ?? []

  return (
    <div className="space-y-2">
      <p className="text-xs text-slate-400">
        {matches.length === 0
          ? 'Nothing matched.'
          : `${matches.length} message${matches.length === 1 ? '' : 's'} matched.`}{' '}
        <span className="text-slate-500">
          Looked at {result.examined.toLocaleString()} of {result.available.toLocaleString()}{' '}
          recorded.
          {result.unreadable > 0 &&
            ` ${result.unreadable.toLocaleString()} could not be parsed and were not tested.`}
        </span>
      </p>

      {result.truncated && (
        // Said plainly. Without it, "nothing matched" over a partial scan reads as "this does not
        // exist", and somebody stops looking.
        <p className="rounded border border-amber-800/50 bg-amber-950/20 p-2 text-xs text-amber-200/90">
          The scan stopped at its limit, so there may be matches further back that were not looked
          at. Raise the limit or narrow by channel or time to reach them.
        </p>
      )}

      {result.paths && result.paths.length > 0 && (
        <p className="text-xs text-slate-500">
          Fields read: {result.paths.map((p) => <code key={p} className="mr-2">{p}</code>)}
        </p>
      )}

      {matches.length > 0 && (
        <table className="w-full text-xs">
          <thead>
            <tr className="text-slate-500">
              <th className="text-left font-normal">when</th>
              <th className="text-left font-normal">channel</th>
              <th className="text-left font-normal">type</th>
              <th className="text-left font-normal">control id</th>
              <th className="text-left font-normal">outcome</th>
            </tr>
          </thead>
          <tbody>
            {matches.map((m) => (
              <Row key={m.id} message={m} onOpen={onOpen} />
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

function Row({
  message,
  onOpen,
}: {
  message: StoredMessage
  onOpen?: (id: number) => void
}) {
  return (
    <tr
      className={'border-t border-slate-800 ' + (onOpen ? 'cursor-pointer hover:bg-slate-900' : '')}
      onClick={() => onOpen?.(message.id)}
    >
      <td className="py-1 text-slate-400">{new Date(message.receivedAt).toLocaleString()}</td>
      <td className="py-1 text-slate-300">{message.channel}</td>
      <td className="py-1 text-slate-400">
        {message.messageType}
        {message.triggerEvent ? `^${message.triggerEvent}` : ''}
      </td>
      <td className="py-1 font-mono text-slate-400">{message.controlId}</td>
      <td className="py-1">
        <OutcomeBadge outcome={message.outcome} ack={message.ackCode} />
      </td>
    </tr>
  )
}
