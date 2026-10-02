import { IconMessages } from './Icons'
import { useCallback, useEffect, useState } from 'react'
import { api } from './api'
import type { QueueItem, QueueDepth, QueueSnapshot, MessageView } from './api'
import { Confirm, ErrorBox, Section, Spinner } from './ui'
import { ParsedMessage } from './Messages'

/**
 * The queue.
 *
 * Laid out around the one question somebody arrives with: is this draining, or is
 * it stuck. Depth alone cannot answer that — a queue of four hundred that is
 * moving is healthy and a queue of two that has not moved since Tuesday is not —
 * so the age of the oldest waiting message is given equal billing, and the row
 * says which of the two it is in words rather than leaving it to be inferred.
 *
 * Every action here is scoped to one message or one destination. The thing being
 * replaced is having to stop and restart a channel to shift a single stuck
 * message, which also interrupts everything that was working.
 */

type Role = 'viewer' | 'editor' | 'admin'

export function Queue({ role }: { role: Role }) {
  const [snapshot, setSnapshot] = useState<QueueSnapshot | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [filter, setFilter] = useState<{ destination?: string; state?: string }>({})
  const [selected, setSelected] = useState<QueueItem | null>(null)
  const [payload, setPayload] = useState<MessageView | null>(null)
  const [confirm, setConfirm] = useState<null | {
    title: string
    body: string
    action: () => Promise<void>
    danger?: boolean
  }>(null)

  const load = useCallback(async () => {
    try {
      const res = await api.queue({ ...filter, limit: 200 })
      setSnapshot(res)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not read the queue')
    }
  }, [filter])

  useEffect(() => {
    load()
    // Polled rather than pushed. Somebody watching a backlog drain wants to see
    // the number falling, and a stale page during an outage is the moment a
    // dashboard loses trust.
    const timer = setInterval(load, 3000)
    return () => clearInterval(timer)
  }, [load])

  async function run(fn: () => Promise<unknown>) {
    setBusy(true)
    try {
      await fn()
      await load()
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'that did not work')
    } finally {
      setBusy(false)
      setConfirm(null)
    }
  }

  async function inspect(item: QueueItem) {
    setSelected(item)
    setPayload(null)
    try {
      const res = await api.queueItem(item.id)
      setPayload(res.message)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not read that message')
    }
  }

  if (!snapshot) {
    return (
      <div className="space-y-5">
        <h1 className="text-lg font-semibold text-slate-100">Queue</h1>
        {error ? (
          <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />
        ) : (
          <div className="card p-6">
            <Spinner label="Reading the queue…" />
          </div>
        )}
      </div>
    )
  }

  const destinations = snapshot.destinations ?? []
  const totalPending = destinations.reduce((sum, d) => sum + d.pending, 0)
  const totalFailed = destinations.reduce((sum, d) => sum + d.failed, 0)
  const worst = destinations.reduce<QueueDepth | null>(
    (worst, d) => (d.pending > 0 && (!worst || d.oldestSeconds > worst.oldestSeconds) ? d : worst),
    null,
  )

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-lg font-semibold text-slate-100">Queue</h1>
          <p className="mt-1 max-w-2xl text-sm text-slate-500">
            Messages accepted from a sender but not yet delivered onward. Nothing here is lost:
            each one is on disk and keeps its place in order until its destination takes it.
          </p>
        </div>
        <button className="btn-ghost text-xs" onClick={load} disabled={busy}>
          Refresh now
        </button>
      </div>

      {error && (
        <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />
      )}

      {totalPending === 0 && totalFailed === 0 ? (
        <div className="card p-10 text-center">
          <p className="text-emerald-300">Nothing is waiting.</p>
          <p className="mx-auto mt-2 max-w-lg text-xs text-slate-400">
            Every destination is keeping up. A message only lands here when a destination refuses
            it and that destination has a queue configured.
          </p>
        </div>
      ) : (
        <div className="grid gap-3 sm:grid-cols-3">
          <Stat
            label="Waiting"
            value={totalPending.toLocaleString()}
            tone={totalPending > 0 ? 'amber' : 'plain'}
          />
          <Stat
            label="Oldest has waited"
            value={worst ? formatAge(worst.oldestSeconds) : '—'}
            hint={worst ? `${worst.channel} → ${worst.destination}` : undefined}
            // Age is the number that distinguishes draining from stuck, so it is
            // given the same weight as depth rather than buried in a table.
            tone={worst && worst.oldestSeconds > 900 ? 'rose' : 'amber'}
          />
          <Stat
            label="Given up on"
            value={totalFailed.toLocaleString()}
            tone={totalFailed > 0 ? 'rose' : 'plain'}
            hint={totalFailed > 0 ? 'these will not be retried without asking' : undefined}
          />
        </div>
      )}

      {destinations.length > 0 && (
        <Section
          title="Destinations"
          description="One row per destination with work outstanding. Each queue is independent — one receiver being down does not hold up another."
        >
          <div className="space-y-2">
            {destinations.map((d) => (
              <DestinationRow
                key={`${d.channel}/${d.destination}`}
                depth={d}
                role={role}
                busy={busy}
                onFilter={() => setFilter({ destination: d.destination })}
                onRetry={() =>
                  run(() => api.queueRetry({ channel: d.channel, destination: d.destination }))
                }
                onDrain={() =>
                  setConfirm({
                    title: `Abandon ${d.pending + d.failed} message${
                      d.pending + d.failed === 1 ? '' : 's'
                    }?`,
                    danger: true,
                    body:
                      `Every message waiting for ${d.destination} will be abandoned without being ` +
                      `delivered. Their senders were told we had accepted them, so this loses ` +
                      `clinical data that nothing else will replay. It is recorded against your ` +
                      `name.\n\nThis is the right call when a receiver has been down long enough ` +
                      `that replaying the backlog would do more harm than skipping it — a week of ` +
                      `stale admissions arriving at once, for instance. It is the wrong call if the ` +
                      `receiver is merely slow.`,
                    action: () =>
                      run(() =>
                        api.queueDrain({ channel: d.channel, destination: d.destination }),
                      ),
                  })
                }
              />
            ))}
          </div>
        </Section>
      )}

      <Section
        title="Messages"
        actions={
          <div className="flex flex-wrap gap-2">
            <select
              aria-label="Show only messages in this state"
              className="select py-1 text-xs"
              value={filter.state ?? ''}
              onChange={(e) => setFilter((f) => ({ ...f, state: e.target.value || undefined }))}
            >
              <option value="">Every state</option>
              <option value="pending">Waiting</option>
              <option value="failed">Given up on</option>
              <option value="skipped">Abandoned</option>
              <option value="delivered">Delivered late</option>
            </select>
            {(filter.destination || filter.state) && (
              <button className="btn-ghost py-1 text-xs" onClick={() => setFilter({})}>
                Clear filter
              </button>
            )}
          </div>
        }
      >
        {snapshot.items.length === 0 ? (
          <p className="text-sm text-slate-500">Nothing matches.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-slate-800 text-left text-xs tracking-wide text-slate-500 uppercase">
                  <th className="py-2 pr-3 font-medium">State</th>
                  <th className="py-2 pr-3 font-medium">Message</th>
                  <th className="py-2 pr-3 font-medium">Destination</th>
                  <th className="py-2 pr-3 font-medium">Waiting</th>
                  <th className="py-2 pr-3 font-medium">Tries</th>
                  <th className="py-2 pr-3 font-medium">Last error</th>
                  <th className="py-2 font-medium"></th>
                </tr>
              </thead>
              <tbody>
                {snapshot.items.map((item) => (
                  <tr
                    key={item.id}
                    className="border-b border-slate-800/60 align-top hover:bg-slate-900/40"
                  >
                    <td className="py-2 pr-3">
                      <StateBadge state={item.state} />
                    </td>
                    <td className="py-2 pr-3">
                      <button
                        className="font-mono text-xs text-sky-400 hover:text-sky-300"
                        onClick={() => inspect(item)}
                      >
                        {item.controlId || `#${item.id}`}
                      </button>
                      <div className="text-xs text-slate-400">{item.messageType}</div>
                    </td>
                    <td className="py-2 pr-3 text-slate-300">
                      {item.destination}
                      <div className="text-xs text-slate-400">{item.channel}</div>
                    </td>
                    <td className="py-2 pr-3 text-slate-400">
                      {formatAge(item.ageSeconds)}
                      {item.state === 'pending' && item.dueInSeconds > 1 && (
                        <div className="text-xs text-slate-400">
                          next try in {formatAge(item.dueInSeconds)}
                        </div>
                      )}
                    </td>
                    <td className="py-2 pr-3 text-slate-400">{item.attempts}</td>
                    <td className="max-w-xs py-2 pr-3">
                      <p className="truncate text-xs text-slate-500" title={item.lastError}>
                        {item.lastError || item.reason || '—'}
                      </p>
                    </td>
                    <td className="py-2">
                      <ItemActions
                        item={item}
                        role={role}
                        busy={busy}
                        onRetry={() => run(() => api.queueRetry({ id: item.id }))}
                        onSkip={() =>
                          setConfirm({
                            title: 'Abandon this message?',
                            danger: true,
                            body:
                              `${item.controlId || `#${item.id}`} will never be delivered to ` +
                              `${item.destination}. Its sender was told we had accepted it, so ` +
                              `nothing will replay it. Recorded against your name.`,
                            action: () => run(() => api.queueSkip({ id: item.id })),
                          })
                        }
                        onRemove={() => run(() => api.queueRemove({ id: item.id }))}
                      />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {snapshot.total > snapshot.items.length && (
              <p className="mt-3 text-xs text-slate-400">
                Showing {snapshot.items.length} of {snapshot.total.toLocaleString()}.
              </p>
            )}
          </div>
        )}
      </Section>

      {selected && (
        <Section
          icon={IconMessages}
          title={`Message ${selected.controlId || `#${selected.id}`}`}
          actions={
            <button
              className="btn-ghost py-1 text-xs"
              onClick={() => {
                setSelected(null)
                setPayload(null)
              }}
            >
              Close
            </button>
          }
        >
          {selected.lastError && (
            <div className="mb-4 rounded-lg border border-rose-900/60 bg-rose-950/30 p-3">
              <p className="label mb-1">Why it is here</p>
              <p className="font-mono text-xs break-words text-rose-200">
                {selected.reason && selected.reason !== selected.lastError && (
                  <>
                    <span className="text-rose-300/70">originally: </span>
                    {selected.reason}
                    <br />
                  </>
                )}
                {selected.lastError}
              </p>
            </div>
          )}
          {payload === null ? (
            <Spinner label="Loading the message…" />
          ) : (
            <ParsedMessage view={payload} />
          )}
        </Section>
      )}

      {confirm && (
        <Confirm
          open
          title={confirm.title}
          body={<span className="whitespace-pre-line">{confirm.body}</span>}
          confirmLabel={confirm.danger ? 'Abandon them' : 'Confirm'}
          onConfirm={confirm.action}
          onCancel={() => setConfirm(null)}
        />
      )}
    </div>
  )
}

function Stat({
  label,
  value,
  hint,
  tone,
}: {
  label: string
  value: string
  hint?: string
  tone: 'plain' | 'amber' | 'rose'
}) {
  const colour =
    tone === 'rose' ? 'text-rose-300' : tone === 'amber' ? 'text-amber-300' : 'text-slate-200'
  return (
    <div className="card p-4">
      <p className="label">{label}</p>
      <p className={`mt-1 text-2xl font-semibold ${colour}`}>{value}</p>
      {hint && <p className="mt-1 text-xs text-slate-400">{hint}</p>}
    </div>
  )
}

function StateBadge({ state }: { state: string }) {
  const style: Record<string, string> = {
    pending: 'border-amber-700 bg-amber-950/50 text-amber-300',
    delivered: 'border-emerald-700 bg-emerald-950/50 text-emerald-300',
    failed: 'border-rose-700 bg-rose-950/50 text-rose-300',
    skipped: 'border-slate-700 bg-slate-800/60 text-slate-400',
  }
  const label: Record<string, string> = {
    pending: 'waiting',
    delivered: 'delivered late',
    failed: 'gave up',
    skipped: 'abandoned',
  }
  return (
    <span className={`badge border ${style[state] ?? style.skipped}`}>
      {label[state] ?? state}
    </span>
  )
}

function DestinationRow({
  depth,
  role,
  busy,
  onFilter,
  onRetry,
  onDrain,
}: {
  depth: QueueDepth
  role: Role
  busy: boolean
  onFilter: () => void
  onRetry: () => void
  onDrain: () => void
}) {
  // Saying which it is, rather than showing two numbers and leaving the reader to
  // work it out. Retrying repeatedly without progress is the signature of a
  // receiver that is up but rejecting, which needs a different fix from one that
  // is simply down.
  const stuck = depth.pending > 0 && depth.maxAttempts >= 3
  const stale = depth.oldestSeconds > 900

  return (
    <div
      className={`rounded-lg border p-3 ${
        stuck || stale ? 'border-rose-900/60 bg-rose-950/20' : 'border-slate-800 bg-slate-900/40'
      }`}
    >
      <div className="flex flex-wrap items-center gap-3">
        <button
          className="font-medium text-slate-100 hover:text-sky-300"
          onClick={onFilter}
          title="Show only this destination"
        >
          {depth.destination}
        </button>
        <span className="text-xs text-slate-400">{depth.channel}</span>

        <div className="ml-auto flex flex-wrap items-center gap-4 text-sm">
          <span className="text-amber-300">
            {depth.pending.toLocaleString()} waiting
          </span>
          {depth.failed > 0 && (
            <span className="text-rose-300">{depth.failed.toLocaleString()} gave up</span>
          )}
          {depth.pending > 0 && (
            <span className={stale ? 'text-rose-300' : 'text-slate-400'}>
              oldest {formatAge(depth.oldestSeconds)}
            </span>
          )}

          {role !== 'viewer' && (
            <button className="btn-ghost py-1 text-xs" onClick={onRetry} disabled={busy}>
              Retry all now
            </button>
          )}
          {role === 'admin' && (
            <button
              className="rounded-lg border border-rose-800 px-2 py-1 text-xs text-rose-300 transition hover:bg-rose-950/40"
              onClick={onDrain}
              disabled={busy}
            >
              Abandon all
            </button>
          )}
        </div>
      </div>

      {stuck && (
        <p className="mt-2 text-xs text-rose-200/90">
          This queue has been retried {depth.maxAttempts} times without getting through. A
          destination that is reachable but refusing needs a different fix from one that is down —
          open a message below to see what it is answering.
        </p>
      )}
      {!stuck && stale && (
        <p className="mt-2 text-xs text-amber-200/90">
          Nothing has been delivered here for {formatAge(depth.oldestSeconds)}.
        </p>
      )}
    </div>
  )
}

function ItemActions({
  item,
  role,
  busy,
  onRetry,
  onSkip,
  onRemove,
}: {
  item: QueueItem
  role: Role
  busy: boolean
  onRetry: () => void
  onSkip: () => void
  onRemove: () => void
}) {
  if (role === 'viewer') return null

  const finished = item.state === 'delivered' || item.state === 'skipped'

  return (
    <div className="flex justify-end gap-1">
      {!finished && (
        <button
          className="rounded px-2 py-1 text-xs text-sky-400 hover:bg-sky-950/40"
          onClick={onRetry}
          disabled={busy}
        >
          Retry
        </button>
      )}
      {!finished && role === 'admin' && (
        <button
          className="rounded px-2 py-1 text-xs text-rose-400 hover:bg-rose-950/40"
          onClick={onSkip}
          disabled={busy}
        >
          Abandon
        </button>
      )}
      {finished && role === 'admin' && (
        <button
          className="rounded px-2 py-1 text-xs text-slate-500 hover:bg-slate-800"
          onClick={onRemove}
          disabled={busy}
        >
          Remove
        </button>
      )}
    </div>
  )
}

/** formatAge renders a duration the way somebody reads it aloud. */
function formatAge(seconds: number): string {
  const s = Math.max(0, Math.round(seconds))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  if (s < 86400) {
    const h = Math.floor(s / 3600)
    return `${h}h ${Math.floor((s % 3600) / 60)}m`
  }
  const d = Math.floor(s / 86400)
  return `${d}d ${Math.floor((s % 86400) / 3600)}h`
}
