import { useEffect, useState } from 'react'

import { api, type AccessWatch } from './api'
import { uiLocale } from './i18n/locale'

/**
 * Watching access to patient data, for an administrator.
 *
 * What fires is on the Alerts screen like any other alert, through the four patient-data rules. This panel answers the two
 * questions an alert does not: what has the monitor learned about each channel (a channel it has not learned yet is not
 * judged at all, and saying so prevents trusting a silence), and what is it holding right now, including what is under a
 * rule's threshold.
 */
const kindWords: Record<AccessWatch['signals'][number]['kind'], string> = {
  volume: 'Burst',
  'after-hours': 'At night',
  'new-destination': 'New destination',
  'bulk-reads': 'Many records read',
}

export function AccessWatchPanel() {
  const [watch, setWatch] = useState<AccessWatch | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let live = true
    const load = () =>
      api.accessWatch().then(
        (w) => live && (setWatch(w), setError(null)),
        (e) => live && setError(e instanceof Error ? e.message : String(e)),
      )
    void load()
    const timer = setInterval(load, 30_000)
    return () => {
      live = false
      clearInterval(timer)
    }
  }, [])

  return (
    <section className="card p-4" aria-labelledby="access-watch-heading" data-testid="access-watch">
      <h2 id="access-watch-heading" className="text-sm font-semibold text-slate-200">
        Watching access to patient data
      </h2>
      {error && <p className="mt-2 text-xs text-rose-300">{error}</p>}
      {watch && !watch.on && (
        <p className="mt-2 text-xs text-slate-400">
          Off on this server. It is started with <code>perfuse serve -watch-access</code>, which is the default.
        </p>
      )}
      {watch?.on && (
        <>
          <p className="mt-1 max-w-3xl text-xs leading-relaxed text-slate-400">
            Each channel's traffic is compared with its own history, and the audit trail is read for one person opening many
            patients' records. Daytime is {String(watch.dayStart).padStart(2, '0')}:00 to{' '}
            {String(watch.dayEnd).padStart(2, '0')}:00 ({watch.zone}). A channel is judged once it has carried{' '}
            {watch.minHistory.toLocaleString(uiLocale())} messages. Anything seen is held for {watch.holdMinutes} minutes, and
            the four patient-data rules below decide what becomes an alert.
          </p>

          <h3 className="mt-4 text-xs font-medium text-slate-300">Seen in the last {watch.holdMinutes} minutes</h3>
          {watch.signals.length === 0 ? (
            <p className="mt-1 text-xs text-emerald-300">Nothing unusual.</p>
          ) : (
            <ul className="mt-2 space-y-2">
              {watch.signals.map((s) => (
                <li key={`${s.kind}|${s.channel ?? ''}|${s.subject ?? ''}`} className="border-t border-slate-800 pt-2 text-xs">
                  <span className="badge mr-2 border border-amber-800 bg-amber-950/40 text-amber-300">{kindWords[s.kind]}</span>
                  <span className="text-slate-200">{s.summary}</span>
                  <p className="mt-1 text-slate-400">{s.detail}</p>
                </li>
              ))}
            </ul>
          )}

          <h3 className="mt-4 text-xs font-medium text-slate-300">What each channel normally does</h3>
          {watch.channels.length === 0 ? (
            <p className="mt-1 text-xs text-slate-400">No channel has carried anything yet.</p>
          ) : (
            <table className="mt-2 w-full text-xs">
              <thead>
                <tr className="border-b border-slate-800 text-left text-slate-500">
                  <th className="py-1.5 pr-3 font-medium">Channel</th>
                  <th className="py-1.5 pr-3 font-medium">Messages learned from</th>
                  <th className="py-1.5 pr-3 font-medium">Pattern</th>
                  <th className="py-1.5 font-medium">Destinations known</th>
                </tr>
              </thead>
              <tbody>
                {watch.channels.map((c) => (
                  <tr key={c.channel} className="border-b border-slate-800/60">
                    <td className="py-1.5 pr-3 text-slate-300">{c.channel}</td>
                    <td className="py-1.5 pr-3 text-slate-400">{c.messages.toLocaleString(uiLocale())}</td>
                    <td className="py-1.5 pr-3">
                      {!c.learned ? (
                        <span className="text-slate-500">still learning; not judged yet</span>
                      ) : c.daytimeOnly ? (
                        <span className="text-sky-300">daytime only</span>
                      ) : (
                        <span className="text-slate-300">day and night</span>
                      )}
                    </td>
                    <td className="py-1.5 text-slate-400">{c.destinations}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}
    </section>
  )
}
