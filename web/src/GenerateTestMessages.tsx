import { useState } from 'react'
import { api, type GeneratedCorpus } from './api'
import { ErrorBox, Spinner } from './ui'

/** GenerateTestMessages produces a test corpus shaped like this channel's real traffic.
 *
 * Why this is worth a button: every guide to interface testing says you need samples that reflect
 * your own environment - your trigger events, your code values, your case mix - and the standard
 * advice is to pull messages out of production and strip the identifiers by hand. That is slow, and
 * it fails quietly the one time somebody misses a field.
 *
 * A clinic has it worse. There is no test feed, no second copy of the sending system, and nobody
 * whose job is to build a corpus.
 *
 * The safety argument is stated on screen rather than assumed, because somebody is about to email
 * this to a vendor and needs to know what is in it.
 */
export function GenerateTestMessages({ channel }: { channel: string }) {
  const [count, setCount] = useState(25)
  const [seed, setSeed] = useState(1)
  const [result, setResult] = useState<GeneratedCorpus | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function generate() {
    setBusy(true)
    setError(null)
    try {
      setResult(await api.generateTestMessages(channel, count, seed))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  function download() {
    if (!result) return
    // A blob rather than a data URL: a corpus of five thousand messages exceeds what a URL can hold,
    // and the failure is a silently truncated file.
    const blob = new Blob([result.corpus], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${channel}-test-messages-seed${result.seed}.hl7`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="rounded-xl border border-teal-900/60 bg-teal-950/20 p-4">
      <h4 className="text-sm font-semibold text-teal-100">Generate test messages</h4>
      <p className="mt-1 text-xs leading-relaxed text-teal-300/85">
        Messages shaped like this feed - the same trigger events in the same proportions, the same
        fields populated as often as they really are, the same code values - and containing none of
        it. Nothing real can come through: this is built from the profile, and a profile holds fill
        rates and value shapes rather than the messages themselves. Code values do carry over,
        because which codes a sender uses is the whole point and a code identifies nobody.
      </p>

      <div className="mt-3 flex flex-wrap items-end gap-3">
        <label className="text-xs text-slate-400">
          How many
          <input
            type="number"
            min={1}
            max={5000}
            value={count}
            onChange={(e) => setCount(Number(e.target.value))}
            className="mt-1 block w-24 rounded-md border border-slate-700 bg-slate-900 px-2 py-1 text-sm text-slate-200"
          />
        </label>
        <label className="text-xs text-slate-400">
          Seed
          <input
            type="number"
            value={seed}
            onChange={(e) => setSeed(Number(e.target.value))}
            className="mt-1 block w-24 rounded-md border border-slate-700 bg-slate-900 px-2 py-1 text-sm text-slate-200"
          />
          <span className="mt-1 block text-slate-600">same seed, same corpus</span>
        </label>
        <button className="btn-primary" onClick={generate} disabled={busy}>
          {busy ? 'Generating…' : 'Generate'}
        </button>
        {result && (
          <button className="btn-ghost" onClick={download}>
            Download
          </button>
        )}
      </div>

      {busy && <Spinner label="Learning the shape of this feed…" />}
      {error && <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />}

      {result && (
        <div className="mt-4 space-y-3">
          <p className="text-xs text-slate-400">
            {result.messages} messages, learned from {result.learnedFrom.messages} recorded ones
            across {result.learnedFrom.segments} segment types. Every one is marked as a test message
            in MSH-11, so a receiver that checks can refuse it.
          </p>

          {result.learnedFrom.types.length > 0 && (
            <ul className="flex flex-wrap gap-2">
              {result.learnedFrom.types.map((t) => (
                <li
                  key={t.type}
                  className="rounded-md border border-slate-700 bg-slate-900 px-2 py-1 font-mono text-xs text-slate-300"
                >
                  {t.type}
                  <span className="ml-1.5 text-slate-500">{(t.rate * 100).toFixed(0)}%</span>
                </li>
              ))}
            </ul>
          )}

          <pre className="max-h-80 overflow-auto rounded-lg border border-slate-800 bg-black/40 p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap text-slate-300">
            {result.corpus.replace(/\r/g, '\n')}
          </pre>
        </div>
      )}
    </div>
  )
}
