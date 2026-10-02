import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { AlertRulesEditor } from './AlertRulesEditor'
import { ApiError, api } from './api'
import type { Alert, AlertSnapshot } from './api'

/**
 * Alerts.
 *
 * Two pieces. A banner that follows you between tabs, because an alert you only
 * see by visiting the right page is not an alert. And a panel with the detail,
 * including what recently recovered — "why did the overnight batch look odd" is
 * usually answered by something that fired at three and cleared at half past, and
 * an interface showing only current state cannot answer it at all.
 */

/**
 * AlertsState keeps the three cases apart, because collapsing them loses work.
 *
 * The server already distinguishes them: it answers 501 when alerting is switched
 * off, and some other status when a request merely failed. Treating both as "no
 * data" means one unlucky poll replaces a working page with "alerting is not
 * enabled", and takes any half-finished rule edit down with it.
 */
export type AlertsState = {
  /** The last snapshot that loaded. Kept across a failed poll on purpose. */
  snapshot: AlertSnapshot | null
  /** Set only when the server says alerting is switched off. */
  disabled: boolean
  /** True until the first attempt finishes, so "nothing yet" is not "nothing wrong". */
  loading: boolean
}

/** The state before anything has been asked of the server. */
export const initialAlertsState: AlertsState = {
  snapshot: null,
  disabled: false,
  loading: true,
}

/**
 * nextAlertsState decides what one poll result means.
 *
 * Pulled out of the hook so it can be tested without a fake clock. The rule it
 * encodes is the whole point: 501 is the server stating that alerting is not
 * configured, which is durable and worth showing. Anything else — a dropped
 * connection, a restart, an expired session — is transient, and throwing away the
 * last good snapshot for one of those is what replaced a working page with
 * "not enabled" and took any half-finished rule edit with it.
 */
export function nextAlertsState(
  prev: AlertsState,
  outcome: { snapshot: AlertSnapshot } | { error: unknown },
): AlertsState {
  if ('snapshot' in outcome) {
    return { snapshot: outcome.snapshot, disabled: false, loading: false }
  }
  if (outcome.error instanceof ApiError && outcome.error.status === 501) {
    return { snapshot: null, disabled: true, loading: false }
  }
  return { ...prev, loading: false }
}

/** useAlerts polls, and is shared by the banner and the panel. */
export function useAlerts(): AlertsState {
  const [state, setState] = useState<AlertsState>(initialAlertsState)

  useEffect(() => {
    let live = true
    async function load() {
      try {
        const res = await api.alerts()
        if (live) setState((prev) => nextAlertsState(prev, { snapshot: res }))
      } catch (err) {
        if (live) setState((prev) => nextAlertsState(prev, { error: err }))
      }
    }
    load()
    const timer = setInterval(load, 15000)
    return () => {
      live = false
      clearInterval(timer)
    }
  }, [])

  return state
}

export function AlertBanner({
  snapshot,
  onOpen,
}: {
  snapshot: AlertSnapshot | null
  onOpen: () => void
}) {
  const [dismissed, setDismissed] = useState<string | null>(null)

  if (!snapshot || snapshot.firing.length === 0) return null

  const critical = snapshot.firing.filter((a) => a.severity === 'critical')
  const worst = critical[0] ?? snapshot.firing[0]
  if (!worst) return null

  // Dismissal is keyed on what is wrong, so clearing the banner for one problem
  // does not hide the next one. A banner that stays hidden after the situation
  // changes is worse than one that keeps reappearing.
  const key = snapshot.firing.map((a) => a.key).join(',')
  if (dismissed === key) return null

  const tone = critical.length
    ? 'border-rose-700 bg-rose-950/70'
    : 'border-amber-700 bg-amber-950/70'

  return (
    <div className={`border-b ${tone}`}>
      <div className="mx-auto flex max-w-7xl flex-wrap items-center gap-3 px-4 py-2.5 text-sm">
        <span
          className={`badge border ${
            critical.length
              ? 'border-rose-600 bg-rose-900/60 text-rose-200'
              : 'border-amber-600 bg-amber-900/60 text-amber-200'
          }`}
        >
          {critical.length > 0 ? 'Critical' : 'Warning'}
        </span>

        <span className={critical.length ? 'text-rose-100' : 'text-amber-100'}>
          {worst.summary}
        </span>

        {snapshot.firing.length > 1 && (
          <span className="text-xs text-slate-400">
            and {snapshot.firing.length - 1} more
          </span>
        )}

        <div className="ml-auto flex gap-2">
          <button
            className="rounded px-2 py-1 text-xs text-slate-200 underline hover:text-white"
            onClick={onOpen}
          >
            What to do
          </button>
          <button
            className="rounded px-2 py-1 text-xs text-slate-400 hover:text-slate-200"
            onClick={() => setDismissed(key)}
            title="Hides this until something changes"
          >
            Dismiss
          </button>
        </div>
      </div>
    </div>
  )
}

export function Alerts({
  role,
  alerts,
}: {
  role: 'viewer' | 'editor' | 'admin'
  alerts: AlertsState
}) {
  // Taken as a prop rather than polled again. App already polls for the banner, and
  // a second timer on the same endpoint doubled the requests and gave the two views
  // different ideas of the current state for up to fifteen seconds.
  const { snapshot, disabled, loading } = alerts
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState<string | null>(null)

  async function acknowledge(alert: Alert) {
    setBusy(true)
    try {
      await api.acknowledgeAlert(alert.key)
      setNote(`Acknowledged. It stays listed, but nothing further will be sent about it.`)
    } catch (err) {
      setNote(err instanceof Error ? err.message : 'could not acknowledge that')
    } finally {
      setBusy(false)
    }
  }

  // The rules editor edits configuration and does not depend on what is currently
  // firing, so it is mounted independently of the snapshot. Keeping it inside the
  // snapshot branch meant a single failed poll unmounted it and discarded whatever
  // was half-typed.
  const rulesEditor = role === 'admin' ? <AlertRulesEditor /> : null

  // One return, and the editor always sits in the same place in the tree.
  // Returning a different shape per state remounts everything below the point the
  // shapes diverge, which is what threw away half-finished edits when a poll failed.
  let status: ReactNode = null
  if (loading) {
    status = <div className="card p-6 text-sm text-slate-500">Loading.</div>
  } else if (disabled) {
    status = (
      <div className="card p-6 text-sm text-slate-500">
        Alerting is not enabled on this server.
      </div>
    )
  } else if (!snapshot) {
    status = (
      <div className="card p-6 text-sm text-slate-500">
        Could not reach the server for the current alerts. Retrying every fifteen seconds.
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-lg font-semibold text-slate-100">Alerts</h1>
        <p className="mt-1 max-w-3xl text-sm text-slate-500">
          Conditions are checked every thirty seconds and must hold for a while before anything
          fires, so a single unlucky message during a quiet hour does not wake anybody. Every
          alert clears itself when the condition stops.
        </p>
      </div>

      {note && (
        <div className="rounded-lg border border-slate-700 bg-slate-900/60 p-3 text-sm text-slate-300">
          {note}
        </div>
      )}

      {status}

      {snapshot && (
        <>
      {snapshot.firing.length === 0 ? (
        <div className="card p-10 text-center">
          <p className="text-emerald-300">Nothing is wrong.</p>
          <p className="mx-auto mt-2 max-w-lg text-xs text-slate-400">
            {snapshot.rules.length} rule{snapshot.rules.length === 1 ? '' : 's'} are being checked.
          </p>
        </div>
      ) : (
        <div className="space-y-3">
          {snapshot.firing.map((alert) => (
            <AlertCard
              key={alert.key}
              alert={alert}
              canAcknowledge={role !== 'viewer'}
              busy={busy}
              onAcknowledge={() => acknowledge(alert)}
            />
          ))}
        </div>
      )}

      {snapshot.resolved.length > 0 && (
        <details className="card p-4">
          <summary className="cursor-pointer text-sm text-slate-400">
            Recently recovered ({snapshot.resolved.length})
          </summary>
          <p className="mt-2 mb-3 text-xs text-slate-400">
            Kept because a problem that fixed itself is often the answer to why an earlier batch
            looked wrong.
          </p>
          <ul className="space-y-2">
            {snapshot.resolved.map((alert, i) => (
              <li
                key={`${alert.key}-${i}`}
                className="flex flex-wrap items-baseline gap-2 border-t border-slate-800 pt-2 text-xs"
              >
                <span className="badge border border-slate-700 bg-slate-800/60 text-slate-400">
                  recovered
                </span>
                <span className="text-slate-400">{alert.summary}</span>
                <span className="ml-auto text-slate-400">
                  lasted {formatSpan(alert.firingSince, alert.resolvedAt)}
                </span>
              </li>
            ))}
          </ul>
        </details>
      )}

      <details className="card p-4">
        <summary className="cursor-pointer text-sm text-slate-400">
          What is being watched ({snapshot.rules.length})
        </summary>
        <table className="mt-3 w-full text-xs">
          <thead>
            <tr className="border-b border-slate-800 text-left text-slate-500">
              <th className="py-1.5 pr-3 font-medium">Condition</th>
              <th className="py-1.5 pr-3 font-medium">Threshold</th>
              <th className="py-1.5 pr-3 font-medium">Must hold for</th>
              <th className="py-1.5 font-medium">Severity</th>
            </tr>
          </thead>
          <tbody>
            {snapshot.rules.map((rule, i) => (
              <tr key={i} className="border-b border-slate-800/60">
                <td className="py-1.5 pr-3 text-slate-300">
                  {ruleLabels[rule.kind] ?? rule.kind}
                  {rule.channel && <span className="text-slate-400"> · {rule.channel}</span>}
                </td>
                <td className="py-1.5 pr-3 text-slate-400">
                  {formatThreshold(rule.kind, rule.threshold)}
                </td>
                <td className="py-1.5 pr-3 text-slate-400">
                  {rule.for ? formatNanos(rule.for) : '2m'}
                </td>
                <td className="py-1.5">
                  <span
                    className={`badge border ${
                      rule.severity === 'critical'
                        ? 'border-rose-800 bg-rose-950/40 text-rose-300'
                        : 'border-amber-800 bg-amber-950/40 text-amber-300'
                    }`}
                  >
                    {rule.severity ?? 'warning'}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
        </>
      )}

      {/* Editing, for an administrator.
          
          The table above says what is being watched and could not change it, so an operator who found a threshold wrong had to
          leave, open the settings screen, and edit YAML - knowing already that the kind is spelled error-rate and that threshold
          means a share for some kinds and a count for others. The rules are now editable where they are read. */}
      {rulesEditor}
    </div>
  )
}

function AlertCard({
  alert,
  canAcknowledge,
  busy,
  onAcknowledge,
}: {
  alert: Alert
  canAcknowledge: boolean
  busy: boolean
  onAcknowledge: () => void
}) {
  const critical = alert.severity === 'critical'

  return (
    <div
      className={`rounded-lg border p-4 ${
        critical ? 'border-rose-800 bg-rose-950/25' : 'border-amber-800 bg-amber-950/20'
      }`}
    >
      <div className="flex flex-wrap items-start gap-3">
        <span
          className={`badge border ${
            critical
              ? 'border-rose-700 bg-rose-950/60 text-rose-300'
              : 'border-amber-700 bg-amber-950/60 text-amber-300'
          }`}
        >
          {alert.severity}
        </span>

        <div className="min-w-0 flex-1">
          <p className="font-medium text-slate-100">{alert.summary}</p>
          {/* The detail says what it means or what to do. An alert that states only
              a fact leaves the reader to work out whether it matters, at the worst
              possible moment for that. */}
          {alert.detail && <p className="mt-1 text-sm text-slate-400">{alert.detail}</p>}

          <div className="mt-2 flex flex-wrap gap-4 text-xs text-slate-400">
            <span>since {formatWhen(alert.firingSince)}</span>
            {alert.channel && <span>channel {alert.channel}</span>}
            {alert.destination && <span>destination {alert.destination}</span>}
            <span className="font-mono">
              {formatThreshold(alert.kind, alert.value)} vs{' '}
              {formatThreshold(alert.kind, alert.threshold)}
            </span>
          </div>
        </div>

        {alert.acknowledged ? (
          <span className="badge border border-slate-700 bg-slate-800/60 text-slate-400">
            acknowledged by {alert.acknowledgedBy}
          </span>
        ) : (
          canAcknowledge && (
            <button
              className="btn-ghost py-1 text-xs"
              onClick={onAcknowledge}
              disabled={busy}
              title="Stops notifications without hiding the alert"
            >
              Acknowledge
            </button>
          )
        )}
      </div>
    </div>
  )
}

const ruleLabels: Record<string, string> = {
  'error-rate': 'Messages failing',
  'queue-depth': 'Queue too deep',
  'queue-age': 'Queue not draining',
  'queue-stuck': 'Same message refused repeatedly',
  'no-traffic': 'Channel gone silent',
  'below-rhythm': 'Quieter than this feed normally is',
  'channel-down': 'Channel enabled but not listening',
  'script-errors': 'Transformer errors',
  'slow-delivery': 'Slow at the tail',
}

function formatThreshold(kind: string, value: number): string {
  if (kind === 'error-rate') return `${(value * 100).toFixed(1)}%`
  // A fraction of normal traffic lost, so a bare number would read as a message count and be off by
  // two orders of magnitude.
  if (kind === 'below-rhythm') return `${(value * 100).toFixed(0)}% below normal`
  if (kind === 'queue-age' || kind === 'slow-delivery') return formatSeconds(value)
  return value.toLocaleString()
}

function formatSeconds(seconds: number): string {
  if (seconds < 1) return `${Math.round(seconds * 1000)}ms`
  if (seconds < 60) return `${seconds.toFixed(seconds < 10 ? 1 : 0)}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
}

function formatNanos(nanos: number): string {
  return formatSeconds(nanos / 1e9)
}

function formatWhen(iso: string): string {
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return 'unknown'
  return formatSeconds((Date.now() - then) / 1000) + ' ago'
}

function formatSpan(from: string, to?: string): string {
  const start = new Date(from).getTime()
  const end = to ? new Date(to).getTime() : Date.now()
  if (Number.isNaN(start) || Number.isNaN(end)) return 'unknown'
  return formatSeconds((end - start) / 1000)
}
