import { useEffect, useState } from 'react'
import { api, type FrictionReport } from './api'

/**
 * What this installation has refused to do.
 *
 * This screen exists in place of a queue item that asked for one real operator to be handed the binary and
 * watched building a channel. That item sat at the top of the queue for three days and was never going to
 * happen: the only parties who have ever run this software are the person who commissioned it and the
 * program that wrote it.
 *
 * Its purpose survives. Every claim about whether Perfuse is easy to use rests on the judgement of whoever
 * wrote it, which is the same circularity that let a thousand self-agreeing SAML tests pass while no real
 * identity provider could sign anybody in. A refusal is evidence from outside that loop - somebody wanted
 * something, the server said no, and neither party was guessing.
 *
 * So this leads with refusals rather than achievements. A panel showing how quickly a channel was built
 * would be the software marking its own homework; a list of the sentences that stopped somebody is the
 * opposite of that, and it is the only thing here that can contradict its author.
 */
export function FrictionPanel() {
  const [report, setReport] = useState<FrictionReport | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const load = () => {
    setLoading(true)
    setError(null)
    api
      .friction()
      .then(setReport)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false))
  }

  useEffect(load, [])

  return (
    <section className="space-y-4" aria-labelledby="friction-heading">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h2 id="friction-heading" className="text-base font-semibold text-slate-100">
            Where this installation got stuck
          </h2>
          <p className="mt-1 text-sm text-slate-500">
            What Perfuse refused to do, counted. Nothing anybody typed is recorded — only the route, the
            status and this server&apos;s own words, because the value that broke a validation rule is
            often patient data.
          </p>
        </div>
        {/*
          Named for what it reloads, not just "Refresh". Activity already has a Refresh button for the audit
          trail, and two controls with the same accessible name on one screen is a screen reader announcing
          "Refresh, button" twice with nothing to choose between them. It also broke an existing test, which
          is how it came to light.
        */}
        <button className="btn-ghost" onClick={load} disabled={loading}>
          {loading ? 'Reading…' : 'Reload report'}
        </button>
      </div>

      {error && (
        <p className="text-sm text-rose-300" role="alert">
          {error}
        </p>
      )}

      {report && (
        <>
          {/*
            The funnel first, because a single sentence saying where somebody stopped is worth more than
            four timestamps a reader has to compare. "A channel exists and no message has ever arrived" is
            the state this is for, and it is invisible from every other screen.
          */}
          <div className="card p-4">
            <p className="text-sm text-slate-400">
              Stuck at:{' '}
              <span
                className={
                  report.funnel.stuckAt.startsWith('nothing')
                    ? 'font-medium text-emerald-300'
                    : 'font-medium text-amber-300'
                }
              >
                {report.funnel.stuckAt}
              </span>
            </p>

            <dl className="mt-3 grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-4">
              <div>
                <dt className="text-xs tracking-wide text-slate-500 uppercase">Channels</dt>
                <dd className="text-slate-200">{report.funnel.channelCount}</dd>
              </div>
              <div>
                <dt className="text-xs tracking-wide text-slate-500 uppercase">Messages in</dt>
                <dd className="text-slate-200">{report.funnel.messageCount}</dd>
              </div>
              <div>
                <dt className="text-xs tracking-wide text-slate-500 uppercase">Delivered</dt>
                {/*
                  Delivered is called out separately from received on purpose. A channel can take traffic
                  for weeks and deliver none of it, and from every other screen that looks like it is
                  working.
                */}
                <dd
                  className={
                    report.funnel.messageCount > 0 && report.funnel.delivered === 0
                      ? 'font-medium text-amber-300'
                      : 'text-slate-200'
                  }
                >
                  {report.funnel.delivered}
                </dd>
              </div>
              <div>
                <dt className="text-xs tracking-wide text-slate-500 uppercase">
                  Sign-in to first message
                </dt>
                <dd className="text-slate-200">
                  {report.funnel.minutesToFirstMessage === undefined
                    ? '—'
                    : formatMinutes(report.funnel.minutesToFirstMessage)}
                </dd>
              </div>
            </dl>
          </div>

          {report.refusals.length === 0 ? (
            <p className="text-sm text-slate-500">
              Nothing has been refused yet. That is either a good sign or a new installation.
            </p>
          ) : (
            <div className="card overflow-hidden">
              <table className="w-full text-sm">
                <caption className="sr-only">
                  Refusals grouped by message, most frequent first
                </caption>
                <thead className="border-b border-slate-800 bg-slate-900/60 text-left">
                  <tr className="text-xs tracking-wide text-slate-500 uppercase">
                    <th className="px-4 py-3">Times</th>
                    <th className="px-4 py-3">What it said</th>
                    <th className="px-4 py-3">Where</th>
                    <th className="px-4 py-3">Last</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-800">
                  {report.refusals.map((r) => (
                    <tr key={`${r.route}|${r.status}|${r.message}`}>
                      <td className="px-4 py-2.5 font-mono text-slate-300">{r.count}</td>
                      <td className="px-4 py-2.5 text-slate-300">
                        {r.message}
                        {r.problems > 0 && (
                          /*
                            A refusal naming eleven problems at once is a different experience from one
                            naming a single typo, and only the count distinguishes them.
                          */
                          <span className="ml-2 text-xs text-slate-500">
                            {r.problems} field {r.problems === 1 ? 'problem' : 'problems'}
                          </span>
                        )}
                      </td>
                      <td className="px-4 py-2.5">
                        {/*
                          Both of these are slate-500. slate-600 was the first choice for the status and failed the contrast sweep at
                          2.48:1 against white and 2.66:1 against the midnight background - the themes remap the slate scale, so a
                          higher number is not reliably darker.
                        */}
                        <code className="font-mono text-xs text-slate-500">{r.route}</code>
                        <span className="ml-2 text-xs text-slate-500">{r.status}</span>
                      </td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-slate-500">
                        {new Date(r.last).toLocaleString()}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {report.total > report.refusals.length && (
            <p className="text-sm text-slate-500">
              {report.total} refusals recorded in total; the {report.refusals.length} most frequent are
              shown.
            </p>
          )}
        </>
      )}
    </section>
  )
}

/** formatMinutes reads a duration the way somebody would say it. */
function formatMinutes(minutes: number): string {
  if (minutes < 1) return 'under a minute'
  if (minutes < 90) return `${Math.round(minutes)} minutes`

  const hours = minutes / 60
  if (hours < 48) return `${hours.toFixed(1)} hours`

  return `${Math.round(hours / 24)} days`
}
