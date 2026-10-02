import { useState } from 'react'
import { GenerateTestMessages } from './GenerateTestMessages'
import { api, type FeedProfile, type ProfileField, type ProfileSegment } from './api'
import { Section } from './ui'
import { useCopy } from './useCopy'

// What is actually in a feed.
//
// The notes come first, deliberately. The tables underneath are complete and easy to skim past;
// the notes are the three or four facts that will change what somebody builds, and putting them
// second would mean nobody read them.
//
// No message content appears here, because none is sent. Counts, rates and shapes, plus the codes
// a sender really uses - which are not identifying, and are the most useful single thing on the
// screen.

const SAMPLES = [100, 1000, 5000, 10000]

export function FeedProfilePanel({ channel }: { channel: string }) {
  const { copy, label } = useCopy()

  const [report, setReport] = useState<FeedProfile | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [limit, setLimit] = useState(1000)
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const [synthesising, setSynthesising] = useState(false)
  const [synthesis, setSynthesis] = useState<string | null>(null)
  const [shareName, setShareName] = useState('')
  const [shareSource, setShareSource] = useState('')
  const [sharing, setSharing] = useState(false)
  const [shareError, setShareError] = useState<string | null>(null)

  /** exportProfile downloads the profile as a file that can be sent to somebody else.
   *
   * A download rather than a copyable blob, because the file is the artefact - it gets attached to a ticket or committed beside a
   * channel, and a profile pasted into a chat window is one nobody can diff later. */
  async function exportProfile() {
    if (report === null) return

    setSharing(true)
    setShareError(null)

    try {
      // The channel is sent, not the profile on screen.
      //
      // The shorter path would be to post the report already rendered here, and it would put the assembly of the one artefact that
      // must not carry patient data in the browser's hands. The server rebuilds it from the stored messages, so the guarantee rests
      // on the profiler - which has a guard proving it - rather than on a request body.
      const downloaded = await api.exportProfile({
        channel,
        limit,
        name: shareName.trim(),
        description: '',
        source: shareSource.trim(),
        tags: [],
      })

      // The server names the file, because it is the side that knows the format version and the naming rules. Recomputing the
      // name here would drift from it the first time either changed.
      const url = URL.createObjectURL(downloaded.blob)
      const a = document.createElement('a')
      a.href = url
      a.download = downloaded.filename
      a.click()
      URL.revokeObjectURL(url)
    } catch (e) {
      // Shown rather than swallowed, because the interesting failure here is the server refusing to export a profile that would
      // carry patient data, and that refusal is the whole reason the check exists.
      setShareError(e instanceof Error ? e.message : String(e))
    } finally {
      setSharing(false)
    }
  }

  async function run() {
    setLoading(true)
    setError(null)
    try {
      setReport(await api.profileChannel(channel, limit))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setReport(null)
    } finally {
      setLoading(false)
    }
  }

  return (
    <Section
      title="What is actually in this feed?"
      description="Reads messages this channel has already handled and reports its real shape — which message types, which segments, how often each field is populated, and which codes the sender actually sends. No message content is read or shown."
    >
      <div className="space-y-4">
        <div className="flex flex-wrap items-end gap-3">
          <label className="block">
            <span className="text-xs font-medium text-slate-300">Sample size</span>
            <select
              className="select mt-1"
              value={limit}
              onChange={(e) => setLimit(Number(e.target.value))}
            >
              {SAMPLES.map((n) => (
                <option key={n} value={n}>
                  the last {n.toLocaleString()} messages
                </option>
              ))}
            </select>
          </label>
          <button type="button" className="btn-primary" disabled={loading} onClick={run}>
            {loading ? 'reading…' : 'Profile this feed'}
          </button>
        </div>

        {error && <p className="text-xs text-rose-400">{error}</p>}

        {/* Offered beside the profile rather than in a section of its own, because it answers the
            question the profile provokes: now that I know what this feed looks like, give me some. */}
        <GenerateTestMessages channel={channel} />

        {report && !synthesis && (
          <div className="flex items-center gap-3">
            <button
              type="button"
              className="btn-primary"
              disabled={synthesising}
              onClick={async () => {
                setSynthesising(true)
                try {
                  const res = await api.synthesiseChannel(channel, limit)
                  setSynthesis(res.yaml)
                } catch (e) {
                  setError(e instanceof Error ? e.message : String(e))
                } finally {
                  setSynthesising(false)
                }
              }}
            >
              {synthesising ? 'synthesising…' : 'Synthesise a channel from this'}
            </button>
            <span className="text-xs text-slate-400">
              Generates a channel YAML from what this feed actually sends. A hypothesis, not a decision.
            </span>
          </div>
        )}

        {synthesis && (
          <div className="space-y-3">
            <div className="flex items-center gap-3">
              <h4 className="text-sm font-medium text-slate-200">Generated channel</h4>
              <button
                type="button"
                className="text-xs text-sky-400 hover:text-sky-300"
                onClick={() => { void copy(synthesis) }}
              >
                {label ?? 'Copy to clipboard'}
              </button>
              <button
                type="button"
                className="text-xs text-slate-400 hover:text-slate-300"
                onClick={() => setSynthesis(null)}
              >
                dismiss
              </button>
            </div>
            <p className="text-xs text-amber-300">
              Review every line. The mapping entries have blank targets because only a person knows what each code should become.
              A confident wrong mapping in clinical data is worse than no mapping.
            </p>
            <pre className="max-h-[500px] overflow-auto rounded border border-slate-700 bg-slate-950 p-4 text-xs text-slate-300">
              {synthesis}
            </pre>
          </div>
        )}

        {report && (
          <div className="space-y-5">
            {/* Sharing, which the machinery has supported for a while with nothing to reach it.
                
                Placed with the report rather than on its own screen, because the moment somebody wants to share a profile is the
                moment they are looking at one and recognise their own feed in somebody else's problem. */}
            <div className="rounded-lg border border-slate-800 bg-slate-950/40 p-3">
              <h4 className="text-xs font-medium text-slate-300">Share this profile</h4>
              <p className="mt-1 text-xs text-slate-500">
                A profile is statistical: fill rates, lengths, shapes, and the codes actually used. Values are kept only for fields
                the standard defines as code tables, so it carries no patient data — and the export refuses if that is ever not
                true rather than trusting it.
              </p>

              <div className="mt-2 grid gap-2 sm:grid-cols-2">
                <label className="block text-xs">
                  <span className="mb-1 block text-slate-400">Name</span>
                  <input
                    className="input w-full"
                    value={shareName}
                    onChange={(e) => setShareName(e.target.value)}
                    placeholder="Epic ADT feed"
                  />
                </label>
                <label className="block text-xs">
                  <span className="mb-1 block text-slate-400">Which system produced it</span>
                  <input
                    className="input w-full"
                    value={shareSource}
                    onChange={(e) => setShareSource(e.target.value)}
                    placeholder="Epic 2023"
                  />
                  <span className="mt-1 block text-slate-600">
                    Required. The question somebody else asks of a shared profile is whether it describes their system.
                  </span>
                </label>
              </div>

              <div className="mt-2 flex items-center gap-2">
                <button
                  className="btn-secondary text-xs"
                  disabled={shareName.trim() === '' || shareSource.trim() === '' || sharing}
                  onClick={() => void exportProfile()}
                >
                  Download profile
                </button>
                {(shareName.trim() === '' || shareSource.trim() === '') && (
                  <span className="text-xs text-slate-600">Give it a name and say which system produced it.</span>
                )}
              </div>

              {shareError !== null && (
                <p role="status" className="mt-2 text-xs text-rose-300">
                  {shareError}
                </p>
              )}
            </div>

            <p className="text-xs text-slate-400">
              {report.messages.toLocaleString()} messages read of{' '}
              {report.available.toLocaleString()} recorded.
              {report.unreadable > 0 &&
                ` ${report.unreadable.toLocaleString()} could not be parsed and are excluded from every figure below.`}
            </p>

            {report.notes && report.notes.length > 0 && (
              <div>
                <h4 className="text-xs font-medium text-slate-300">
                  What a specification would not have told you
                </h4>
                <ul className="mt-2 space-y-1.5">
                  {report.notes.map((n, i) => (
                    <li key={i} className="text-xs leading-relaxed text-amber-200/90">
                      {n}
                    </li>
                  ))}
                </ul>
              </div>
            )}

            <div>
              <h4 className="text-xs font-medium text-slate-300">Message types</h4>
              <table className="mt-2 w-full text-xs">
                <tbody>
                  {report.types.map((t) => (
                    <tr key={t.type} className="border-t border-slate-800">
                      <td className="py-1 font-mono text-slate-300">{t.type}</td>
                      <td className="py-1 text-right text-slate-400">
                        {t.count.toLocaleString()}
                      </td>
                      <td className="w-24 py-1 text-right text-slate-500">
                        {(t.rate * 100).toFixed(1)}%
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            <div>
              <h4 className="text-xs font-medium text-slate-300">Segments</h4>
              <div className="mt-2 space-y-1">
                {report.segments.map((s) => (
                  <SegmentRow
                    key={s.id}
                    segment={s}
                    open={!!open[s.id]}
                    onToggle={() => setOpen((o) => ({ ...o, [s.id]: !o[s.id] }))}
                  />
                ))}
              </div>
            </div>
          </div>
        )}
      </div>
    </Section>
  )
}

function SegmentRow({
  segment,
  open,
  onToggle,
}: {
  segment: ProfileSegment
  open: boolean
  onToggle: () => void
}) {
  return (
    <div className="rounded-lg border border-slate-800">
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center justify-between gap-3 px-3 py-2 text-left"
      >
        <span className="flex items-baseline gap-2">
          <span className="font-mono text-xs text-slate-200">{segment.id}</span>
          {!segment.standard && (
            // Called out rather than left to be noticed. These are the segments no vendor
            // specification mentions and every integration has to handle.
            <span className="rounded bg-amber-900/40 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-amber-300">
              local
            </span>
          )}
          <span className="text-xs text-slate-500">{segment.name}</span>
        </span>
        <span className="shrink-0 text-xs text-slate-400">
          {(segment.rate * 100).toFixed(1)}% of messages
          {segment.maxPerMessage > 1 && ` · up to ${segment.maxPerMessage} each`}
          <span className="ml-2 text-slate-400">{open ? '−' : '+'}</span>
        </span>
      </button>

      {open && (
        <div className="border-t border-slate-800 px-3 py-2">
          <table className="w-full text-xs">
            <thead>
              <tr className="text-slate-500">
                <th className="text-left font-normal">field</th>
                <th className="text-left font-normal">populated</th>
                <th className="text-left font-normal">distinct</th>
                <th className="text-left font-normal">length</th>
                <th className="text-left font-normal">looks like</th>
              </tr>
            </thead>
            <tbody>
              {segment.fields.map((f) => (
                <FieldRow key={f.path} field={f} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function FieldRow({ field }: { field: ProfileField }) {
  const partial = field.fillRate < 0.999

  return (
    <>
      <tr className="border-t border-slate-800/60 align-top">
        <td className="py-1 pr-2">
          <span className="font-mono text-slate-300">{field.path}</span>
          {field.name && <span className="ml-2 text-slate-500">{field.name}</span>}
          {field.maxRepeats > 1 && (
            <span className="ml-2 text-sky-400">repeats up to {field.maxRepeats}</span>
          )}
        </td>
        <td className={'py-1 pr-2 ' + (partial ? 'text-amber-300' : 'text-slate-400')}>
          {(field.fillRate * 100).toFixed(1)}%
        </td>
        <td className="py-1 pr-2 text-slate-400">
          {field.distinct.toLocaleString()}
          {field.distinctCapped && '+'}
        </td>
        <td className="py-1 pr-2 text-slate-500">
          {field.minLength === field.maxLength
            ? field.maxLength
            : `${field.minLength}–${field.maxLength}`}
        </td>
        <td className="py-1 text-slate-500">{field.shape}</td>
      </tr>

      {field.codes && field.codes.length > 0 && (
        <tr>
          <td colSpan={5} className="pb-2 pl-4">
            <div className="flex flex-wrap gap-1">
              {field.codes.map((c) => (
                <span
                  key={c.code}
                  title={
                    c.known
                      ? c.meaning
                      : `Not defined by HL7 table ${field.table}. A strict mapping downstream will reject it.`
                  }
                  className={
                    'rounded px-1.5 py-0.5 font-mono text-[11px] ' +
                    (c.known
                      ? 'bg-slate-800 text-slate-300'
                      : 'bg-amber-900/40 text-amber-200')
                  }
                >
                  {c.code}
                  <span className="ml-1 text-slate-500">{c.count.toLocaleString()}</span>
                </span>
              ))}
            </div>
          </td>
        </tr>
      )}
    </>
  )
}
