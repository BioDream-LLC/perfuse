import { useState } from 'react'
import { api, type ReplayReport } from './api'
import { Section } from './ui'

// Asking what a change would have done, before making it.
//
// The design decision worth stating: this shows a count and examples, and never a verdict.
// "This changes 12 of your last 5,000 messages, here they are" is something a person can check
// against their intention. "Safe to deploy" is a claim the tool cannot support, because whether
// a difference is wanted is precisely the judgement that cannot be automated. A tool that said
// "safe" would be believed, and would eventually be wrong.
//
// The default ignore list matters more than it looks. Without it every single message differs on
// its timestamp and control ID, the report is a hundred per cent changed, and the feature is
// useless on first use - which is when somebody decides whether to trust it.

const DEFAULT_IGNORE = ['MSH-7', 'MSH-10']

const LIMITS = [100, 1000, 5000, 10000]

export function ReplayPanel({ channel, candidateYaml }: { channel: string; candidateYaml: string }) {
  const [report, setReport] = useState<ReplayReport | null>(null)
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [limit, setLimit] = useState(1000)
  const [ignore, setIgnore] = useState(DEFAULT_IGNORE.join(', '))

  async function run() {
    setRunning(true)
    setError(null)
    setReport(null)
    try {
      const res = await api.replayChannel(
        channel,
        candidateYaml,
        limit,
        ignore
          .split(',')
          .map((p) => p.trim())
          .filter(Boolean),
      )
      setReport(res)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setRunning(false)
    }
  }

  return (
    <Section
      title="What would this have done?"
      description="Runs your edits against messages this channel has already handled, alongside the version running now, and shows every message the two disagree about."
    >
      <div className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="block">
            <span className="text-xs font-medium text-slate-300">How many recent messages</span>
            <select
              className="select mt-1"
              value={limit}
              onChange={(e) => setLimit(Number(e.target.value))}
            >
              {LIMITS.map((n) => (
                <option key={n} value={n}>
                  the last {n.toLocaleString()}
                </option>
              ))}
            </select>
          </label>

          <label className="block">
            <span className="text-xs font-medium text-slate-300">Ignore these fields</span>
            <input
              className="input mt-1 font-mono text-xs"
              value={ignore}
              onChange={(e) => setIgnore(e.target.value)}
            />
            <span className="mt-1 block text-xs text-slate-500">
              Timestamps and control IDs differ on every message and say nothing about whether a
              change is safe.
            </span>
          </label>
        </div>

        <button
          type="button"
          className="btn-primary"
          disabled={running || !candidateYaml.trim()}
          onClick={run}
        >
          {running ? 'replaying…' : 'Replay against recorded messages'}
        </button>

        {error && <p className="text-xs text-rose-400">{error}</p>}

        {report && <Report report={report} />}
      </div>
    </Section>
  )
}

function Report({ report }: { report: ReplayReport }) {
  if (report.problems && report.problems.length > 0) {
    return (
      <div className="rounded-lg border border-rose-800/60 bg-rose-950/20 p-3">
        <p className="text-xs font-medium text-rose-200">
          The candidate does not load yet, so there is nothing to compare.
        </p>
        <ul className="mt-2 space-y-1">
          {report.problems.map((p, i) => (
            <li key={i} className="text-xs text-rose-300">
              {p.message.replace(/^-\s*/, '')}
            </li>
          ))}
        </ul>
      </div>
    )
  }

  const clean = report.changed === 0
  const pct = report.examined > 0 ? Math.round((report.changed / report.examined) * 100) : 0

  return (
    <div className="space-y-4">
      <div
        className={
          'rounded-lg border p-3 ' +
          (clean ? 'border-emerald-800/60 bg-emerald-950/20' : 'border-amber-800/60 bg-amber-950/20')
        }
      >
        <p className={'text-sm font-medium ' + (clean ? 'text-emerald-200' : 'text-amber-200')}>
          {clean
            ? `No difference across ${report.examined.toLocaleString()} messages.`
            : `${report.changed.toLocaleString()} of ${report.examined.toLocaleString()} messages come out differently (${pct}%).`}
        </p>
        <p className="mt-1 text-xs text-slate-400">
          {report.examined.toLocaleString()} examined of {report.available.toLocaleString()}{' '}
          recorded for this channel.
          {report.unparseable > 0 &&
            ` ${report.unparseable.toLocaleString()} stored message(s) could not be read by either version and were not counted.`}
        </p>
        {clean && (
          // Said explicitly, because the temptation to read "no differences" as "safe" is
          // strong and the two are not the same claim.
          <p className="mt-2 text-xs text-slate-400">
            That means this change would not have altered any of these messages. It does not
            mean it is safe for messages you have not seen yet.
          </p>
        )}
      </div>

      {report.fieldCounts && Object.keys(report.fieldCounts).length > 0 && (
        <div>
          <h4 className="text-xs font-medium text-slate-300">Which fields change, and how often</h4>
          <p className="mt-1 text-xs text-slate-500">
            One field against every message is a deliberate change. One field against three
            messages out of thousands is the edge case worth looking at.
          </p>
          <table className="mt-2 w-full text-xs">
            <tbody>
              {Object.entries(report.fieldCounts)
                .sort((a, b) => b[1] - a[1])
                .map(([path, count]) => (
                  <tr key={path} className="border-t border-slate-800">
                    <td className="py-1 font-mono text-slate-300">{path}</td>
                    <td className="py-1 text-right text-slate-400">
                      {count.toLocaleString()} message{count === 1 ? '' : 's'}
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
      )}

      {report.differences.length > 0 && (
        <div>
          <h4 className="text-xs font-medium text-slate-300">
            Examples
            {report.truncated && (
              <span className="ml-2 font-normal text-slate-500">
                (first {report.differences.length}; the counts above are complete)
              </span>
            )}
          </h4>

          <div className="mt-2 space-y-2">
            {report.differences.map((d, i) => (
              <div key={i} className="rounded-lg border border-slate-800 p-2">
                <p className="font-mono text-xs text-slate-400">
                  {d.controlId || '(no control id)'} · {d.messageType || 'unknown type'}
                </p>

                {d.verdict && <p className="mt-1 text-xs text-amber-300">{d.verdict}</p>}

                {d.fields && d.fields.length > 0 && (
                  <table className="mt-1 w-full text-xs">
                    <thead>
                      <tr className="text-slate-500">
                        <th className="text-left font-normal">field</th>
                        <th className="text-left font-normal">now</th>
                        <th className="text-left font-normal">would become</th>
                      </tr>
                    </thead>
                    <tbody>
                      {d.fields.map((f, j) => (
                        <tr key={j}>
                          <td className="pr-2 font-mono text-slate-400">{f.path}</td>
                          <td className="pr-2 font-mono text-slate-300">{f.live || '(empty)'}</td>
                          <td className="font-mono text-sky-300">{f.candidate || '(empty)'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
