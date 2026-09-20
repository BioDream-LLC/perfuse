import { SiteLogo } from './Branding'
import { IconShadow } from './Icons'
import { useEffect, useState } from 'react'
import { api } from './api'
import type { ChannelSummary, ShadowReport, ShadowSummary } from './api'
import { ShadowEditor } from './ShadowEditor'
import { ErrorBox, Section, Spinner } from './ui'

/**
 * Shadow.
 *
 * The page answers the question that makes interface work frightening: how do you
 * know a change is safe? A channel test proves the cases somebody thought of. This
 * proves the cases that actually arrive.
 *
 * It deliberately never says "safe to promote". Nothing here can tell an intended
 * change from a mistake — only that there is a difference, or that there is not.
 */
export function Shadow() {
  const [summaries, setSummaries] = useState<ShadowSummary[] | null>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const [report, setReport] = useState<ShadowReport | null>(null)
  const [reportError, setReportError] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [channels, setChannels] = useState<ChannelSummary[]>([])
  const [reload, setReload] = useState(0)

  // The channel list is loaded for the editor, and reloaded when a comparison starts or stops so the buttons match reality.
  useEffect(() => {
    let live = true
    api
      .listChannels()
      .then((res) => live && setChannels(res.channels))
      .catch(() => undefined)

    return () => {
      live = false
    }
  }, [reload])

  useEffect(() => {
    let live = true
    const load = () => {
      api
        .shadows()
        .then((s) => {
          if (!live) return
          setSummaries(s)
          const first = s[0]
          if (selected === null && first !== undefined) {
            setSelected(first.channel)
          }
        })
        .catch((e) => live && setError(e instanceof Error ? e.message : 'could not read'))
    }
    load()
    const t = setInterval(load, 5000)
    return () => {
      live = false
      clearInterval(t)
    }
  }, [selected, reload])

  useEffect(() => {
    if (selected === null) return

    // Drop the previous channel's report before fetching this one.
    //
    // Without this the old comparison stays on screen while the new one loads, and stays for good if the
    // load fails. This view exists to answer whether a configuration change is safe, so a confident answer
    // about the wrong channel is the worst thing it can do: somebody promotes a candidate believing it was
    // compared, and what they read was another channel's result. Showing nothing is honest; showing the
    // wrong thing is not.
    setReport(null)
    setReportError(null)

    let live = true
    const load = () => {
      api
        .shadow(selected)
        .then((r) => live && setReport(r))
        .catch((e) => {
          // Said out loud rather than swallowed. A silent failure here is indistinguishable from a channel
          // that genuinely has nothing to report, and those two need different responses from a reader.
          if (live) {
            setReportError(e instanceof Error ? e.message : 'could not read this comparison')
          }
        })
    }
    load()
    const t = setInterval(load, 5000)
    return () => {
      live = false
      clearInterval(t)
    }
  }, [selected])

  if (error) {
    return <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />
  }
  if (summaries === null) {
    return (
      <div className="card p-6">
        <Spinner label="Reading comparisons…" />
      </div>
    )
  }

  return (
    <div className="space-y-5">
      {/* A shadow run is the evidence somebody takes to a decision about replacing an engine, so it is worth attributing. */}
      <SiteLogo size={40} />

      <div>
        <h1 className="text-lg font-semibold text-slate-100">Shadow</h1>
        <p className="mt-1 max-w-3xl text-sm text-slate-500">
          Runs a candidate version of a channel beside the live one on real traffic and
          reports where they differ. The candidate has no senders, so it cannot deliver
          anything, and it runs after the live message has been acknowledged.
        </p>
      </div>

      {/* The editor is shown whether or not anything is being compared.
          
          Above the report rather than below it, and present in the empty state, because "nothing is being shadowed" used to be a
          dead end: the screen said no comparison was running and offered no way to start one. */}
      <ShadowEditor
        channels={channels}
        comparing={new Set(summaries.map((s) => s.channel))}
        onChanged={() => setReload((n) => n + 1)}
      />

      {summaries.length === 0 ? (
        <div className="card p-8 text-center">
          <p className="text-slate-400">Nothing is being shadowed.</p>
          <p className="mx-auto mt-3 max-w-xl text-left text-xs text-slate-400">
            Add a shadow block to a channel naming the candidate file:
          </p>
          <pre className="mx-auto mt-2 max-w-xl overflow-x-auto rounded-md border border-slate-800 bg-slate-950 p-3 text-left font-mono text-xs text-slate-400">
            {`shadow:
  channel: ./adt-candidate.yaml
  ignore: [MSH-7, MSH-10]   # things that differ every message`}
          </pre>
        </div>
      ) : (
        <>
          {summaries.length > 1 && (
            <div className="flex flex-wrap gap-2">
              {summaries.map((s) => (
                <button
                  key={s.channel}
                  onClick={() => setSelected(s.channel)}
                  aria-pressed={selected === s.channel}
                  // Without this the count runs straight into the name and the button reads as
                  // "shadowed1", which sounds like a channel called shadowed1 rather than a channel with
                  // one difference. The visible text stays compact; the spoken one says what it means.
                  aria-label={
                    s.differed > 0
                      ? `${s.channel}, ${s.differed} ${s.differed === 1 ? 'message' : 'messages'} differed`
                      : s.channel
                  }
                  className={`badge border ${
                    selected === s.channel
                      ? 'border-sky-700 bg-sky-950/50 text-sky-200'
                      : 'border-slate-700 bg-slate-800/60 text-slate-400 hover:text-slate-200'
                  }`}
                >
                  {s.channel}
                  {s.differed > 0 && (
                    <span className="ml-1.5 text-amber-300">{s.differed}</span>
                  )}
                </button>
              ))}
            </div>
          )}

          {reportError !== null ? (
            <div className="card border-amber-800/60 bg-amber-950/20 p-4 text-sm text-amber-200">
              <p className="font-medium">This comparison could not be read.</p>
              <p className="mt-1 text-amber-300/80">{reportError}</p>
              <p className="mt-2 text-xs text-amber-300/60">
                Nothing is shown rather than the previous channel&rsquo;s result, which would look like an
                answer about this one.
              </p>
            </div>
          ) : (
            report && <ShadowDetail report={report} />
          )}
        </>
      )}
    </div>
  )
}

function ShadowDetail({ report }: { report: ShadowReport }) {
  const s = report.stats
  const rate = s.compared > 0 ? (s.differed / s.compared) * 100 : 0

  return (
    <div className="space-y-4">
      <div className="card p-4">
        <div className="flex flex-wrap items-baseline gap-2">
          <span className="font-medium text-slate-100">{report.channel}</span>
          <span className="text-xs text-slate-400">compared against</span>
          <span className="font-mono text-xs text-slate-400">{report.candidate}</span>
        </div>

        {/* The verdict is the headline, phrased as an observation rather than an
            approval. Nothing here can know whether a difference is intended. */}
        <p
          className={`mt-3 rounded-md border p-3 text-sm ${
            s.candidateFailed > 0
              ? 'border-rose-800 bg-rose-950/30 text-rose-200'
              : s.filterDisagreed > 0
                ? 'border-amber-800 bg-amber-950/30 text-amber-200'
                : s.differed > 0
                  ? 'border-slate-700 bg-slate-800/40 text-slate-300'
                  : 'border-emerald-900 bg-emerald-950/25 text-emerald-200'
          }`}
        >
          {report.verdict}
        </p>

        <div className="mt-4 grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
          <Stat label="Compared" value={s.compared} />
          <Stat label="Identical" value={s.same} tone="good" />
          <Stat label="Differed" value={s.differed} tone={s.differed > 0 ? 'warn' : undefined} />
          <Stat
            label="Filter disagreed"
            value={s.filterDisagreed}
            tone={s.filterDisagreed > 0 ? 'bad' : undefined}
            hint="One version keeps a message the other drops"
          />
          <Stat
            label="Candidate failed"
            value={s.candidateFailed}
            tone={s.candidateFailed > 0 ? 'bad' : undefined}
          />
          <Stat label="Not sampled" value={s.skipped} />
        </div>

        {s.compared > 0 && (
          <div className="mt-4">
            <div className="flex justify-between text-xs text-slate-500">
              <span>{rate.toFixed(1)}% of messages came out differently</span>
              <span>{s.compared.toLocaleString()} compared</span>
            </div>
            <div className="mt-1.5 h-2 overflow-hidden rounded-full bg-slate-800">
              <div
                className={rate > 0 ? 'h-full bg-amber-500' : 'h-full bg-emerald-600'}
                style={{ width: `${Math.max(rate, rate > 0 ? 2 : 100)}%` }}
              />
            </div>
          </div>
        )}
      </div>

      {report.differences.length === 0 ? (
        <div className="card p-6 text-center text-sm text-slate-500">
          No differences recorded.
        </div>
      ) : (
        <Section
          icon={IconShadow}
          title={`Differences (${report.differences.length} kept)`}
          description="Compared field by field rather than byte by byte, so a change to one component reads as one line instead of a diff of a pipe-delimited string."
        >
          <div className="space-y-3">
            {report.differences
              .slice()
              .reverse()
              .map((d, i) => (
                <div key={i} className="rounded-md border border-slate-800 bg-slate-900/40 p-3">
                  <div className="flex flex-wrap items-baseline gap-2 text-xs">
                    <span
                      className={`badge border ${
                        d.kind === 'filter'
                          ? 'border-amber-800 bg-amber-950/40 text-amber-300'
                          : d.kind === 'candidate-error'
                            ? 'border-rose-800 bg-rose-950/40 text-rose-300'
                            : d.kind === 'live-error'
                              ? 'border-sky-800 bg-sky-950/40 text-sky-300'
                              : 'border-slate-700 bg-slate-800/60 text-slate-400'
                      }`}
                    >
                      {d.kind}
                    </span>
                    {d.messageType && (
                      <span className="font-mono text-slate-400">{d.messageType}</span>
                    )}
                    {d.controlId && (
                      <span className="font-mono text-slate-400">{d.controlId}</span>
                    )}
                    <span className="ml-auto text-slate-400">
                      {new Date(d.at).toLocaleTimeString()}
                    </span>
                  </div>

                  {d.note && <p className="mt-2 text-xs text-slate-400">{d.note}</p>}

                  {d.fields && d.fields.length > 0 && (
                    <table className="mt-2 w-full text-xs">
                      <thead>
                        <tr className="text-left text-slate-400">
                          <th className="pb-1 pr-3 font-medium">Field</th>
                          <th className="pb-1 pr-3 font-medium">Live</th>
                          <th className="pb-1 font-medium">Candidate</th>
                        </tr>
                      </thead>
                      <tbody className="font-mono">
                        {d.fields.map((f, j) => (
                          <tr key={j} className="border-t border-slate-800/60">
                            <td className="py-1 pr-3 text-slate-400">{f.path}</td>
                            <td className="py-1 pr-3 text-rose-300/90">
                              {f.live === '' ? <span className="text-slate-500">empty</span> : f.live}
                            </td>
                            <td className="py-1 text-emerald-300/90">
                              {f.candidate === '' ? (
                                <span className="text-slate-500">empty</span>
                              ) : (
                                f.candidate
                              )}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  )}
                </div>
              ))}
          </div>
        </Section>
      )}
    </div>
  )
}

function Stat({
  label,
  value,
  tone,
  hint,
}: {
  label: string
  value: number
  tone?: 'good' | 'warn' | 'bad'
  hint?: string
}) {
  const colour =
    tone === 'bad'
      ? 'text-rose-300'
      : tone === 'warn'
        ? 'text-amber-300'
        : tone === 'good'
          ? 'text-emerald-300'
          : 'text-slate-200'

  return (
    <div title={hint}>
      <div className={`text-xl font-semibold ${colour}`}>{value.toLocaleString()}</div>
      <div className="text-xs text-slate-400">{label}</div>
    </div>
  )
}
