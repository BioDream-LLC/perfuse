import { CodeArea } from './CodeArea'
import { useState } from 'react'
import { api, type ParityReport } from './api'
import { ErrorBox, Section } from './ui'

/** ParityPanel compares this channel's output against the engine being replaced.
 *
 * Why this matters more than any feature: Perfuse can import a Mirth channel and can trace what it
 * would do with a message. Neither answers the question somebody actually has before moving a live
 * clinical feed, which is whether this produces the same output as the thing that has run for six
 * years. No amount of design quality answers that. Only evidence does, and the evidence has to come
 * from the old engine's own output on the site's own traffic.
 *
 * Why it asks for a pasted export rather than connecting to anything: this cannot reach into Mirth,
 * and pretending otherwise would be the worst kind of feature. What it needs is in Mirth's own
 * message browser - the message as it arrived and the message it produced - which an administrator
 * already knows how to export.
 */
export function ParityPanel({ channel }: { channel: string }) {
  const [text, setText] = useState('')
  const [report, setReport] = useState<ParityReport | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function run() {
    setBusy(true)
    setError(null)
    setReport(null)
    try {
      const pairs = parsePairs(text)
      if (pairs.length === 0) {
        setError(
          'No pairs were found. Each pair is an arriving message and the message the old engine produced from it, separated by a line containing only ---, with pairs separated by a line containing only ===.',
        )
        return
      }
      setReport(await api.checkParity(channel, pairs))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Section
      title="Prove it against the old engine"
      description="Paste pairs of messages exported from the engine you are replacing — what arrived, and what it produced — and this reports where Perfuse would differ. Nothing is sent anywhere. This is the evidence for the decision to move a live feed, and it has to come from the old engine's own output on your own traffic."
    >
      <div className="space-y-3">
        <p className="rounded-lg border border-slate-800 bg-slate-900/60 p-3 text-xs leading-relaxed text-slate-400">
          Format: the arriving message, a line containing only <code className="text-slate-300">---</code>, the message
          the old engine produced, then a line containing only <code className="text-slate-300">===</code> before the
          next pair. Both are in Mirth&apos;s message browser as the raw and encoded or sent content.
        </p>

        <CodeArea
          language="hl7"
          aria-label="Message pairs from the old engine"
          className="min-h-[11rem]"
          value={text}
          onChange={setText}
          placeholder={'MSH|^~\\&|OLD|...\n---\nMSH|^~\\&|NEW|...\n===\nMSH|^~\\&|OLD|...\n---\nMSH|^~\\&|NEW|...'}
          spellCheck={false}
        />

        <button type="button" className="btn-primary" onClick={run} disabled={busy || text.trim() === ''}>
          {busy ? 'comparing…' : 'Compare'}
        </button>

        {error && <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />}

        {report && (
          <div className="space-y-4">
            {/* The verdict first and largest, because it is the sentence that ends up in a change
                request. It gives exact counts rather than a percentage: 49,993 of 50,000 is a
                different statement from "over 99%", and the seven are the whole point. */}
            <p className="rounded-xl border border-slate-700 bg-slate-900 p-4 text-sm leading-relaxed text-slate-100">
              {report.verdict}
            </p>

            <div className="grid gap-2 sm:grid-cols-4">
              <Tally label="Identical" value={report.identical} className="border-emerald-900/60 bg-emerald-950/25 text-emerald-200" />
              <Tally label="Cosmetic only" value={report.equivalent} className="border-sky-900/60 bg-sky-950/25 text-sky-200" />
              <Tally label="Differ" value={report.differing} className="border-amber-900/60 bg-amber-950/25 text-amber-200" />
              <Tally label="Would not run" value={report.failed} className="border-rose-900/60 bg-rose-950/25 text-rose-200" />
            </div>

            {report.failureReasons.length > 0 && (
              <div className="rounded-xl border border-rose-900/60 bg-rose-950/20 p-4">
                <h4 className="text-sm font-semibold text-rose-100">Would not run</h4>
                <p className="mt-1 text-xs text-rose-300/80">
                  A different problem from producing the wrong output — usually a setting the importer could not fill in.
                </p>
                <ul className="mt-2 space-y-1.5">
                  {report.failureReasons.map((f, i) => (
                    <li key={i} className="text-xs text-rose-200">
                      <span className="font-medium">{f.messages.toLocaleString()}×</span> {f.error}
                      {f.reference && <span className="ml-2 text-rose-400/70">e.g. {f.reference}</span>}
                    </li>
                  ))}
                </ul>
              </div>
            )}

            {report.findings.length > 0 && (
              <div className="space-y-2">
                <h4 className="text-sm font-semibold text-slate-200">
                  Where the output differs, by field
                </h4>
                {report.findings.map((f) => (
                  <div key={f.path} className="rounded-lg border border-slate-800 bg-slate-950/60 p-3">
                    <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                      <span className="font-mono text-sm text-slate-200">{f.path}</span>
                      <span className="text-xs text-slate-400">
                        {f.messages.toLocaleString()} message{f.messages === 1 ? '' : 's'}
                      </span>
                      {/* The most useful thing on screen. One systematic difference across four
                          thousand messages is one decision, not four thousand problems. */}
                      {f.systematic ? (
                        <span className="rounded bg-sky-950/60 px-1.5 py-0.5 text-xs font-medium text-sky-300">
                          same every time — one decision
                        </span>
                      ) : (
                        <span className="rounded bg-amber-950/60 px-1.5 py-0.5 text-xs font-medium text-amber-300">
                          {f.distinctPairs.toLocaleString()} different ways — the content differs
                        </span>
                      )}
                    </div>

                    <ul className="mt-2 space-y-1">
                      {f.examples.map((e, i) => (
                        <li key={i} className="flex flex-wrap items-baseline gap-2 text-xs">
                          <span className="text-slate-500">old engine</span>
                          <span className="font-mono text-rose-300">{e.expected || '(empty)'}</span>
                          <span className="text-slate-600">→</span>
                          <span className="text-slate-500">Perfuse</span>
                          <span className="font-mono text-emerald-300">{e.got || '(empty)'}</span>
                          <span className="text-slate-600">×{e.count.toLocaleString()}</span>
                          {e.reference && <span className="text-slate-500">{e.reference}</span>}
                        </li>
                      ))}
                    </ul>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </Section>
  )
}

function Tally({ label, value, className }: { label: string; value: number; className: string }) {
  return (
    <div className={`rounded-lg border p-3 ${className}`}>
      <span className="block text-xl font-semibold">{value.toLocaleString()}</span>
      <span className="block text-xs opacity-80">{label}</span>
    </div>
  )
}

/** parsePairs splits the pasted text into input and expected pairs.
 *
 * Done in the browser so a malformed paste is reported before a request is made, and so the format is
 * visible in one place next to the instructions describing it.
 */
function parsePairs(text: string): { input: string; expected: string; reference: string }[] {
  const out: { input: string; expected: string; reference: string }[] = []

  text.split(/^===\s*$/m).forEach((block, i) => {
    const halves = block.split(/^---\s*$/m)
    if (halves.length !== 2) return
    const input = (halves[0] ?? '').trim()
    const expected = (halves[1] ?? '').trim()
    if (input === '' || expected === '') return
    // A positional reference when the export carries no identifier, so a finding still points at
    // something the reader can find in what they pasted.
    out.push({ input, expected, reference: `pair ${i + 1}` })
  })

  return out
}
