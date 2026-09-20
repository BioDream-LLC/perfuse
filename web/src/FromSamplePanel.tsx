import { SyntaxBlock } from './SyntaxHighlight'
import { CodeArea } from './CodeArea'
import { useState } from 'react'
import { api, type SampleReading } from './api'
import { ErrorBox, Section } from './ui'

/** FromSamplePanel proposes a channel from the sample message a sender emailed you.
 *
 * Why this is the first thing offered when creating a channel: a single HL7 interface is commonly
 * costed in the tens of thousands, and one of the three things consistently named as sinking these
 * projects is that nobody in-house has done HL7 before. A small practice has neither the budget nor
 * the analyst. The sample message is the one artefact they reliably have.
 *
 * Why it shows its reasoning rather than just producing a file: one message tells you very little,
 * and the danger is that it looks like it tells you a lot. Every field present looks mandatory, every
 * code looks like the whole set, and nothing repeats. A confident-looking channel built from that
 * would be deployed, and the first message with two OBX segments would behave in a way nobody
 * predicted.
 */
export function FromSamplePanel({ onProposed }: { onProposed: (yaml: string) => void }) {
  const [sample, setSample] = useState('')
  const [name, setName] = useState('')
  const [reading, setReading] = useState<SampleReading | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function read() {
    setBusy(true)
    setError(null)
    setReading(null)
    try {
      setReading(await api.readSample(sample, name))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Section
      title="Start from a message somebody sent you"
      description="Paste the sample the lab, hospital or vendor gave you and this will propose a channel that handles it. Nothing needs to be running first. Line endings, MLLP framing and the covering note above the message are all handled."
    >
      <div className="space-y-3">
        <label className="block">
          <span className="text-xs font-medium text-slate-300">The sample</span>
          <CodeArea
            language="hl7"
            className="mt-1 min-h-[9rem]"
            value={sample}
            onChange={setSample}
            placeholder={'MSH|^~\\&|LABSYS|QUESTLAB|CLINICEHR|RIVERSIDE|20260828093000||ORU^R01|LAB00219|P|2.5'}
            spellCheck={false}
          />
        </label>

        <div className="flex flex-wrap items-end gap-3">
          <label className="block">
            <span className="text-xs font-medium text-slate-300">Call it</span>
            <input
              className="input mt-1"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="quest-labs"
            />
          </label>
          <button
            type="button"
            className="btn-primary"
            onClick={read}
            disabled={busy || sample.trim() === ''}
          >
            {busy ? 'reading…' : 'Read it and propose a channel'}
          </button>
        </div>

        {error && <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />}

        {reading && (
          <div className="space-y-3 rounded-xl border border-slate-800 bg-slate-950/60 p-4">
            <div className="flex flex-wrap items-center gap-3">
              <span className="text-sm text-slate-300">
                {reading.messages} message{reading.messages === 1 ? '' : 's'} read
                {reading.unreadable > 0 && (
                  <span className="text-amber-300"> · {reading.unreadable} could not be parsed</span>
                )}
              </span>
              <span className={`rounded-md px-2 py-0.5 text-xs font-medium ${confidenceChip(reading.confidence)}`}>
                {reading.confidence} confidence
              </span>
            </div>

            <div className="grid gap-3 sm:grid-cols-3">
              <ReasonList
                title="What the sample shows"
                items={reading.observed}
                className="border-emerald-900/60 bg-emerald-950/20 text-emerald-200"
              />
              <ReasonList
                title="What was guessed"
                items={reading.guessed}
                className="border-sky-900/60 bg-sky-950/20 text-sky-200"
              />
              {/* Given equal weight, not tucked away. This is the list a confident-looking generator
                  leaves out, and it is the one that decides whether the proposal can be trusted. */}
              <ReasonList
                title="What this cannot tell you"
                items={reading.unknowable}
                className="border-amber-900/60 bg-amber-950/20 text-amber-200"
              />
            </div>

            <details>
              <summary className="cursor-pointer text-xs text-slate-400 hover:text-slate-200">
                The proposed channel
              </summary>
              <SyntaxBlock code={reading.yaml} language="yaml" className="mt-2 max-h-80" />
            </details>

            <div className="flex flex-wrap items-center gap-3">
              <button type="button" className="btn-primary" onClick={() => onProposed(reading.yaml)}>
                Use this as a starting point
              </button>
              <span className="text-xs text-slate-500">
                Opens in the editor. Nothing is saved or started until you save it.
              </span>
            </div>
          </div>
        )}
      </div>
    </Section>
  )
}

function ReasonList({
  title,
  items,
  className,
}: {
  title: string
  items: string[]
  className: string
}) {
  return (
    <div className={`rounded-lg border p-3 ${className}`}>
      <h4 className="text-xs font-semibold">{title}</h4>
      {items.length === 0 ? (
        <p className="mt-1.5 text-xs opacity-70">nothing</p>
      ) : (
        <ul className="mt-1.5 space-y-1.5 text-xs leading-relaxed">
          {items.map((s, i) => (
            <li key={i}>{s}</li>
          ))}
        </ul>
      )}
    </div>
  )
}

/* Whole class names, never assembled — Tailwind scans for literals. */
function confidenceChip(confidence: string): string {
  switch (confidence) {
    case 'reasonable':
      return 'bg-emerald-950/60 text-emerald-300 border border-emerald-900/60'
    case 'moderate':
      return 'bg-sky-950/60 text-sky-300 border border-sky-900/60'
    default:
      return 'bg-amber-950/60 text-amber-300 border border-amber-900/60'
  }
}
