import { useEffect, useMemo, useState } from 'react'
import { ContentSearch } from './ContentSearch'
import { TracePanel } from './TracePanel'
import { useSelector } from 'react-redux'
import { api } from './api'
import { SyntaxBlock } from './SyntaxHighlight'
import type { FieldView, MessageView, SegmentView, StoredMessage } from './api'
import { outcomeColours } from './charts'
import type { RootState } from './store'
import { Confirm, ErrorBox, Field, Section, Spinner } from './ui'

/**
 * The message browser.
 *
 * The screen somebody opens when a clinician says a result never arrived. It has
 * to answer three things quickly: did it get here, what happened to it, and what
 * exactly did it say.
 */
export function Messages() {
  const me = useSelector((s: RootState) => s.session.me)!
  const canReprocess = me.role === 'editor' || me.role === 'admin'

  const [messages, setMessages] = useState<StoredMessage[]>([])
  const [channels, setChannels] = useState<string[]>([])
  const [total, setTotal] = useState(0)
  const [offset, setOffset] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<number | null>(null)

  const [filters, setFilters] = useState({
    channel: '',
    outcome: '',
    type: '',
    controlId: '',
    search: '',
  })

  async function load(nextOffset = offset) {
    setLoading(true)
    try {
      const res = await api.listMessages({ ...filters, limit: 50, offset: nextOffset })
      setMessages(res.messages ?? [])
      setTotal(res.total)
      setChannels(res.channels ?? [])
      setOffset(nextOffset)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not load messages')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load(0)
    // Filters are applied on submit rather than on every keystroke, because a
    // payload search is a table scan and firing one per character is unkind.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div className="space-y-5">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-lg font-semibold text-slate-100">Messages</h1>
          <p className="mt-1 text-sm text-slate-500">
            Everything that arrived, what happened to it, and the bytes as they were sent.
          </p>
        </div>
        <button className="btn-ghost" onClick={() => load(offset)}>
          Refresh
        </button>
      </div>

      {error && <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />}

      <Section title="Find a message">
        <form
          className="grid gap-4 lg:grid-cols-5"
          onSubmit={(e) => {
            e.preventDefault()
            load(0)
          }}
        >
          <Field label="Channel">
            <select
              className="select"
              value={filters.channel}
              onChange={(e) => setFilters({ ...filters, channel: e.target.value })}
            >
              <option value="">any</option>
              {channels.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
          </Field>

          {/*
            Spelled out rather than named.

            "filtered", "partial" and "unparseable" are engine vocabulary, and the one that
            matters most is the least obvious: a filtered message was acknowledged as fine and
            deliberately not forwarded, which is exactly what somebody hunting a missing message
            needs to understand. Naming the outcome tells them nothing; describing it answers
            their question.

            "pending" was missing entirely, so there was no way to ask what is still in flight -
            which is the first question during an incident.
          */}
          <Field label="What happened to it">
            <select
              className="select"
              value={filters.outcome}
              onChange={(e) => setFilters({ ...filters, outcome: e.target.value })}
            >
              <option value="">anything</option>
              <option value="delivered">delivered everywhere</option>
              <option value="partial">delivered to some destinations, not all</option>
              <option value="filtered">accepted but deliberately not forwarded</option>
              <option value="pending">still being processed</option>
              <option value="failed">could not be delivered</option>
              <option value="unparseable">could not be read as HL7</option>
            </select>
          </Field>

          <Field label="Type" hint="ADT, ORU, ORM">
            <input
              className="input"
              value={filters.type}
              onChange={(e) => setFilters({ ...filters, type: e.target.value.toUpperCase() })}
              placeholder="ADT"
            />
          </Field>

          <Field label="Control ID" hint="MSH-10, exact">
            <input
              className="input font-mono"
              value={filters.controlId}
              onChange={(e) => setFilters({ ...filters, controlId: e.target.value })}
            />
          </Field>

          {/* An explicit id, because this field holds an input and a button, so Field cannot work out
              which one the label names. Without it the label named nothing. */}
          <Field label="In the message" hint="Searches the stored payload" htmlFor="messages-content-search">
            <div className="flex gap-2">
              <input
                id="messages-content-search"
                className="input font-mono"
                value={filters.search}
                onChange={(e) => setFilters({ ...filters, search: e.target.value })}
                placeholder="MRN or accession"
              />
              <button className="btn-primary shrink-0">Search</button>
            </div>
          </Field>
        </form>
      </Section>

      {/*
        Content search is a separate panel, not another filter field.

        The filters above are indexed lookups; this parses messages. Putting them together would hide
        that one of them can take a moment and can only reach part of the history, and somebody would
        read a partial answer as a complete one.
      */}
      <Section
        title="Search inside the messages"
        description="Ask a question about the content rather than the metadata, in the same language channel filters use."
      >
        <ContentSearch onOpen={(id) => setSelected(id)} />
      </Section>

      {loading && <Spinner label="Loading messages…" />}

      <div className="card overflow-hidden">
        <table className="w-full text-sm">
          <thead className="border-b border-slate-800 bg-slate-900/60 text-left">
            <tr className="text-xs tracking-wide text-slate-500 uppercase">
              <th className="px-4 py-3">Received</th>
              <th className="px-4 py-3">Channel</th>
              <th className="px-4 py-3">Message</th>
              <th className="px-4 py-3">Control ID</th>
              <th className="px-4 py-3">Outcome</th>
              <th className="px-4 py-3 text-right">Took</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-800">
            {messages.map((m) => (
              <tr
                key={m.id}
                onClick={() => setSelected(m.id)}
                className="cursor-pointer transition hover:bg-slate-900/60"
              >
                <td className="px-4 py-2.5 whitespace-nowrap text-slate-500">
                  {new Date(m.receivedAt).toLocaleString()}
                </td>
                <td className="px-4 py-2.5 text-slate-300">{m.channel}</td>
                <td className="px-4 py-2.5">
                  <span className="font-medium text-slate-200">
                    {m.messageType || '?'}
                    {m.triggerEvent && <span className="text-slate-500">^{m.triggerEvent}</span>}
                  </span>
                  {m.sender && <span className="ml-2 text-xs text-slate-400">from {m.sender}</span>}
                </td>
                <td className="px-4 py-2.5 font-mono text-xs text-slate-500">{m.controlId}</td>
                <td className="px-4 py-2.5">
                  <OutcomeBadge outcome={m.outcome} ack={m.ackCode} />
                </td>
                <td className="px-4 py-2.5 text-right font-mono text-xs text-slate-500">
                  {m.durationMs}ms
                </td>
              </tr>
            ))}
            {!loading && messages.length === 0 && (
              <tr>
                <td colSpan={6} className="px-4 py-12 text-center text-slate-500">
                  Nothing matches. Messages appear here once a channel handles one.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {total > 50 && (
        <div className="flex items-center justify-between text-sm text-slate-500">
          <span>
            {offset + 1}–{Math.min(offset + 50, total)} of {total.toLocaleString()}
          </span>
          <div className="flex gap-2">
            <button
              className="btn-ghost py-1 text-xs"
              disabled={offset === 0}
              onClick={() => load(Math.max(0, offset - 50))}
            >
              Newer
            </button>
            <button
              className="btn-ghost py-1 text-xs"
              disabled={offset + 50 >= total}
              onClick={() => load(offset + 50)}
            >
              Older
            </button>
          </div>
        </div>
      )}

      {selected !== null && (
        <MessageDetail
          id={selected}
          canReprocess={canReprocess}
          onClose={() => setSelected(null)}
          onReprocessed={() => load(offset)}
        />
      )}
    </div>
  )
}

// What each outcome means, for the tooltip.
//
// The one worth reading twice is filtered: the sender was told the message was accepted, and it
// was deliberately not forwarded. Somebody hunting a message that never arrived downstream will
// find it here marked green, and needs to understand that this is correct rather than a fault.
const outcomeMeanings: Record<string, string> = {
  delivered: 'Written to every destination.',
  partial:
    'Some destinations received it and others did not. The sender was told this went wrong, so it may be resent.',
  filtered:
    'Accepted and deliberately not forwarded, because the channel filter excluded it. The sender was told the message was fine, because it was — we simply were not interested in it.',
  pending: 'Still being processed. An acknowledge-on-receipt channel leaves this state briefly.',
  failed: 'Could not be delivered. The sender was told this went wrong.',
  unparseable: 'The bytes could not be read as an HL7 message, so it was rejected.',
  queued: 'Held on disk and being retried until a receiver accepts it.',
}

// The acknowledgement codes, which are HL7 vocabulary that even experienced people muddle.
const ackMeanings: Record<string, string> = {
  AA: 'AA — accepted. The sender considers this message done.',
  AE: 'AE — application error. Something went wrong here, and a well-behaved sender will resend.',
  AR: 'AR — rejected. We could not process it at all, and resending the same bytes will not help.',
  CA: 'CA — commit accept. Receipt is confirmed; processing may not have finished.',
  CE: 'CE — commit error.',
  CR: 'CR — commit reject.',
}

export function OutcomeBadge({ outcome, ack }: { outcome: string; ack?: string }) {
  const colour = outcomeColours[outcome] ?? '#64748b'
  return (
    <span className="inline-flex items-center gap-2">
      <span className="size-2 rounded-full" style={{ background: colour }} />
      <span style={{ color: colour }} title={outcomeMeanings[outcome] ?? outcome}>
        {outcome}
      </span>
      {ack && (
        <span
          className="cursor-help font-mono text-xs text-slate-400"
          title={ackMeanings[ack] ?? `${ack} — acknowledgement code returned to the sender.`}
        >
          {ack}
        </span>
      )}
    </span>
  )
}

function MessageDetail({
  id,
  canReprocess,
  onClose,
  onReprocessed,
}: {
  id: number
  canReprocess: boolean
  onClose: () => void
  onReprocessed: () => void
}) {
  const [message, setMessage] = useState<StoredMessage | null>(null)
  const [parsed, setParsed] = useState<MessageView | null>(null)
  const [parseError, setParseError] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<'parsed' | 'raw' | 'trace'>('parsed')
  const [confirming, setConfirming] = useState(false)
  const [reprocessing, setReprocessing] = useState(false)
  const [result, setResult] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    api
      .getMessage(id)
      .then((res) => {
        if (cancelled) return
        setMessage(res.message)
        setParsed(res.parsed ?? null)
        setParseError(res.parseError ?? null)
        if (!res.parsed) setTab('raw')
      })
      .catch((err) => !cancelled && setError(String(err)))
    return () => {
      cancelled = true
    }
  }, [id])

  async function reprocess() {
    setReprocessing(true)
    try {
      const res = await api.reprocessMessage(id)
      setResult(
        res.ackCode
          ? `Sent again. The channel answered ${res.ackCode}${res.ackText ? `: ${res.ackText}` : ''}.`
          : 'Sent again.',
      )
      onReprocessed()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'reprocessing failed')
    } finally {
      setReprocessing(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex justify-end bg-black/60" onClick={onClose}>
      <div
        className="h-full w-full max-w-4xl overflow-y-auto border-l border-slate-800 bg-slate-950 p-6"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="mb-5 flex items-start justify-between gap-4">
          <div>
            <h2 className="text-lg font-semibold text-slate-100">
              {parsed ? (
                <>
                  {parsed.messageType}^{parsed.triggerEvent}
                  {parsed.eventMeaning && (
                    <span className="ml-3 text-sm font-normal text-slate-500">
                      {parsed.eventMeaning}
                    </span>
                  )}
                </>
              ) : (
                `Message #${id}`
              )}
            </h2>
            {message && (
              <p className="mt-1 text-sm text-slate-500">
                {message.channel} · {new Date(message.receivedAt).toLocaleString()} ·{' '}
                {message.size} bytes
                {parsed?.version && ` · HL7 ${parsed.version}`}
              </p>
            )}
          </div>
          <div className="flex gap-2">
            {canReprocess && message && (
              <button
                className="btn-ghost"
                onClick={() => setConfirming(true)}
                disabled={reprocessing || !message.raw}
                title={message.raw ? 'Send this message through the channel again' : 'The payload was pruned'}
              >
                {reprocessing ? 'Sending…' : 'Reprocess'}
              </button>
            )}
            <button className="btn-ghost" onClick={onClose}>
              Close
            </button>
          </div>
        </div>

        {error && <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />}
        {result && (
          <div className="mb-4 rounded-lg border border-emerald-900/60 bg-emerald-950/30 p-3 text-sm text-emerald-200">
            {result}
          </div>
        )}
        {parseError && (
          <div className="mb-4 rounded-lg border border-amber-900/60 bg-amber-950/30 p-3 text-sm text-amber-200">
            This message could not be parsed: {parseError}. The raw bytes are still below, which is
            usually what you need in this situation.
          </div>
        )}

        {!message && <Spinner label="Loading…" />}

        {message && (
          <div className="space-y-5">
            <div className="card p-4">
              <p className="label mb-3">What happened</p>
              <div className="flex flex-wrap items-center gap-6 text-sm">
                <OutcomeBadge outcome={message.outcome} ack={message.ackCode} />
                <span className="text-slate-500">{message.durationMs}ms</span>
                {message.controlId && (
                  <span className="font-mono text-xs text-slate-500">{message.controlId}</span>
                )}
              </div>

              {message.error && (
                <pre className="mt-3 overflow-x-auto rounded-md border border-rose-900/60 bg-rose-950/30 p-2.5 font-mono text-xs whitespace-pre-wrap text-rose-200">
                  {message.error}
                </pre>
              )}

              {message.attachmentError && (
                <p className="mt-4 rounded-lg border border-rose-900/60 bg-rose-950/25 p-3 text-sm text-rose-200">
                  {message.attachmentError}
                </p>
              )}

              {message.attachments && message.attachments.length > 0 && (
                <div className="mt-4 border-t border-slate-800 pt-3">
                  <h4 className="text-xs uppercase tracking-wide text-slate-500">
                    Stored outside the message
                  </h4>
                  <ul className="mt-2 space-y-1.5">
                    {message.attachments.map((a) => (
                      <li key={a.digest} className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
                        <span className="font-mono text-xs text-slate-500">{a.digest.slice(0, 12)}</span>
                        <span className="text-slate-200">{formatBytes(a.size)}</span>
                        {/* The number that makes deduplication visible: a document referenced forty times was
                            stored once, and that is invisible without saying so. */}
                        {a.messages > 1 && (
                          <span className="text-emerald-300">
                            shared by {a.messages} messages — stored once
                          </span>
                        )}
                      </li>
                    ))}
                  </ul>
                  <p className="mt-2 text-xs text-slate-500">
                    The payload above was moved out of the stored message and put back to show it here. Destinations
                    always receive the whole message.
                  </p>
                </div>
              )}

              {message.deliveries && message.deliveries.length > 0 && (
                <ul className="mt-4 space-y-2 border-t border-slate-800 pt-3">
                  {message.deliveries.map((d) => (
                    <li key={d.destination} className="text-sm">
                      <div className="flex items-center gap-3">
                        <span className="text-slate-300">{d.destination}</span>
                        <span
                          className={
                            d.status === 'delivered'
                              ? 'text-emerald-300'
                              : d.status === 'failed'
                                ? 'text-rose-300'
                                : 'text-slate-500'
                          }
                        >
                          {d.status}
                        </span>
                        {d.attempts > 1 && (
                          <span className="text-xs text-amber-300">
                            after {d.attempts} attempts
                          </span>
                        )}
                        <span className="ml-auto font-mono text-xs text-slate-400">
                          {d.durationMs}ms
                        </span>
                      </div>
                      {d.error && (
                        <p className="mt-1 font-mono text-xs text-rose-300/80">{d.error}</p>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </div>

            <div className="flex gap-1">
              {(['parsed', 'raw', 'trace'] as const).map((t) => (
                <button
                  key={t}
                  onClick={() => setTab(t)}
                  disabled={(t === 'parsed' && !parsed) || (t === 'trace' && !canReprocess)}
                  title={
                    t === 'trace' && !canReprocess
                      ? 'Following a message through a channel needs edit permission, because it compiles and runs the channel configuration'
                      : undefined
                  }
                  className={`rounded-lg px-3 py-1.5 text-sm font-medium transition disabled:opacity-40 ${
                    tab === t ? 'bg-slate-800 text-slate-100' : 'text-slate-500 hover:text-slate-300'
                  }`}
                >
                  {t === 'parsed' ? 'Explained' : t === 'raw' ? 'Raw' : 'Why this happened'}
                </button>
              ))}
            </div>

            {tab === 'parsed' && parsed && <ParsedMessage view={parsed} />}

            {/*
              The trace lives here because this is where the question gets asked. Somebody looking
              at a message in the list is looking at it because something about it was surprising,
              and "why this happened" is the next thing they want.
            */}
            {tab === 'trace' && (
              <div>
                <p className="mb-3 text-xs leading-relaxed text-slate-400">
                  Runs this message through {message.channel} again and shows every stage: what it
                  was read as, what the filter decided and why, what each step changed, and which
                  destinations would take it. Nothing is delivered.
                </p>
                <TracePanel channel={message.channel} messageId={message.id} />
              </div>
            )}

            {tab === 'raw' && (
              message.raw
                ? <SyntaxBlock code={atob(message.raw).replace(/\r/g, '\n')} />
                : <pre className="overflow-x-auto rounded-lg border border-slate-800 bg-slate-950 p-4 font-mono text-xs leading-relaxed whitespace-pre-wrap text-slate-300">
                    The payload was pruned, so only the record of this message remains.
                  </pre>
            )}
          </div>
        )}

        <Confirm
          open={confirming}
          title="Send this message again?"
          body="It goes to the channel's own listener, so it takes exactly the same path as a message from the sending system, including every destination. Anything already delivered will be delivered again."
          confirmLabel="Reprocess"
          onConfirm={() => {
            setConfirming(false)
            reprocess()
          }}
          onCancel={() => setConfirming(false)}
        />
      </div>
    </div>
  )
}

/**
 * ParsedMessage is the annotated view.
 *
 * The point of this screen: PID-5.1 reads as "Patient Name - Family" instead of
 * leaving somebody to count carets. Counting separators by hand is where
 * off-by-one mapping mistakes come from.
 */
export function ParsedMessage({ view }: { view: MessageView }) {
  const [open, setOpen] = useState<Set<number>>(() => new Set(view.segments.map((s) => s.index)))
  const [hideEmpty, setHideEmpty] = useState(true)

  function toggle(index: number) {
    setOpen((previous) => {
      const next = new Set(previous)
      if (next.has(index)) next.delete(index)
      else next.add(index)
      return next
    })
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between text-xs text-slate-500">
        <span>
          {view.segments.length} segments · separators{' '}
          <code className="font-mono text-slate-400">{view.separators}</code>
        </span>
        <div className="flex gap-3">
          <button className="hover:text-slate-300" onClick={() => setHideEmpty((v) => !v)}>
            {hideEmpty ? 'Show empty fields' : 'Hide empty fields'}
          </button>
          <button
            className="hover:text-slate-300"
            onClick={() =>
              setOpen((p) => (p.size === 0 ? new Set(view.segments.map((s) => s.index)) : new Set()))
            }
          >
            {open.size === 0 ? 'Expand all' : 'Collapse all'}
          </button>
        </div>
      </div>

      {view.segments.map((segment) => (
        <SegmentBlock
          key={segment.index}
          segment={segment}
          open={open.has(segment.index)}
          hideEmpty={hideEmpty}
          onToggle={() => toggle(segment.index)}
        />
      ))}
    </div>
  )
}

const segmentColours: Record<string, string> = {
  MSH: 'border-sky-800 bg-sky-950/30',
  MSA: 'border-sky-800 bg-sky-950/30',
  EVN: 'border-violet-800 bg-violet-950/30',
  PID: 'border-emerald-800 bg-emerald-950/30',
  PD1: 'border-emerald-800 bg-emerald-950/30',
  NK1: 'border-teal-800 bg-teal-950/30',
  PV1: 'border-amber-800 bg-amber-950/30',
  PV2: 'border-amber-800 bg-amber-950/30',
  OBR: 'border-indigo-800 bg-indigo-950/30',
  OBX: 'border-cyan-800 bg-cyan-950/30',
  ORC: 'border-indigo-800 bg-indigo-950/30',
  DG1: 'border-rose-800 bg-rose-950/30',
  AL1: 'border-rose-800 bg-rose-950/30',
  IN1: 'border-slate-700 bg-slate-900/40',
  GT1: 'border-slate-700 bg-slate-900/40',
  NTE: 'border-slate-700 bg-slate-900/40',
  ERR: 'border-rose-800 bg-rose-950/30',
}

function SegmentBlock({
  segment,
  open,
  hideEmpty,
  onToggle,
}: {
  segment: SegmentView
  open: boolean
  hideEmpty: boolean
  onToggle: () => void
}) {
  const fields = useMemo(
    () => (hideEmpty ? segment.fields.filter((f) => !f.empty) : segment.fields),
    [segment.fields, hideEmpty],
  )

  const tone = segmentColours[segment.name] ?? 'border-slate-700 bg-slate-900/40'

  return (
    <div className={`overflow-hidden rounded-lg border ${tone}`}>
      <button
        className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition hover:bg-white/5"
        onClick={onToggle}
      >
        <span className="font-mono text-sm font-semibold text-slate-100">{segment.name}</span>
        <span className="truncate text-xs text-slate-400">{segment.description}</span>
        {!segment.known && (
          <span className="badge border border-amber-700 bg-amber-950/50 text-amber-300">
            not in the dictionary
          </span>
        )}
        <span className="ml-auto shrink-0 text-xs text-slate-400">
          {fields.length} field{fields.length === 1 ? '' : 's'}
        </span>
        <span className="text-slate-400">{open ? '−' : '+'}</span>
      </button>

      {open && (
        <div className="border-t border-slate-800/80 bg-slate-950/50">
          {fields.length === 0 ? (
            <p className="px-4 py-3 text-xs text-slate-400">No populated fields.</p>
          ) : (
            <ul className="divide-y divide-slate-800/60">
              {fields.map((field) => (
                <FieldRow key={field.number} field={field} />
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}

function FieldRow({ field }: { field: FieldView }) {
  const [expanded, setExpanded] = useState(false)
  const hasComponents = (field.components?.length ?? 0) > 1

  return (
    <li className="px-4 py-2">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <code className="w-20 shrink-0 font-mono text-xs text-sky-300">{field.path}</code>
        <span className="w-52 shrink-0 truncate text-xs text-slate-400" title={field.name}>
          {field.name}
        </span>

        <span className="min-w-0 flex-1 font-mono text-sm break-all text-slate-100">
          {field.empty ? <span className="text-slate-500">(empty)</span> : field.value}
        </span>

        {field.meaning && (
          <span className="badge shrink-0 border border-emerald-800 bg-emerald-950/40 text-emerald-300">
            {field.meaning}
          </span>
        )}
        {field.repeats && field.repeats > 1 && (
          <span className="badge shrink-0 border border-violet-800 bg-violet-950/40 text-violet-300">
            {field.repeats} repetitions
          </span>
        )}
        {hasComponents && (
          <button
            className="shrink-0 text-xs text-slate-500 hover:text-slate-300"
            onClick={() => setExpanded((v) => !v)}
          >
            {expanded ? 'hide parts' : `${field.components!.length} parts`}
          </button>
        )}
      </div>

      {field.description && (
        <p className="mt-1 ml-[6.3rem] text-xs text-slate-400">{field.description}</p>
      )}

      {expanded && field.components && (
        <ul className="mt-2 ml-[6.3rem] space-y-1 border-l border-slate-800 pl-3">
          {field.components.map((c) => (
            <li key={c.number} className="flex flex-wrap items-baseline gap-x-3 text-xs">
              <code className="w-24 shrink-0 font-mono text-slate-500">{c.path}</code>
              <span className="w-44 shrink-0 truncate text-slate-500">{c.name}</span>
              <span className="font-mono break-all text-slate-300">
                {c.value || <span className="text-slate-500">(empty)</span>}
              </span>
            </li>
          ))}
        </ul>
      )}
    </li>
  )
}

/** formatBytes renders a payload size the way somebody reads it, not the way it is stored. */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}
