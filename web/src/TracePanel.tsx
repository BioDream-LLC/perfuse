import { CodeArea } from './CodeArea'
import { useState } from 'react'
import { StepThrough } from './StepThrough'
import { api, type MessageTrace, type TraceStage, type TraceValue } from './api'

// Watching one message go through a channel.
//
// Laid out as a vertical sequence rather than a table, because the message goes through the stages
// in order and the reader is following it. Each stage shows what it looked at and what it did, and
// a stage that stopped the message is the last one shown - which is itself the answer.
//
// The values a filter read are the important part of the display. "The filter excluded this
// message" is not an answer; "the filter read MSH-9.1 and found ORU" is, and it needs no knowledge
// of the expression language to act on.

export function TracePanel({
  channel,
  messageId,
  initialMessage,
}: {
  channel: string
  messageId?: number
  initialMessage?: string
}) {
  const [trace, setTrace] = useState<MessageTrace | null>(null)
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pasted, setPasted] = useState(initialMessage ?? '')
  const [showBoth, setShowBoth] = useState(false)

  async function run() {
    setRunning(true)
    setError(null)
    setTrace(null)
    try {
      const subject = messageId ? { id: messageId } : { message: pasted }
      setTrace(await api.traceMessage(channel, subject))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="space-y-3">
      {!messageId && (
        <label className="block">
          <span className="text-xs font-medium text-slate-300">Message to follow</span>
          <CodeArea
            language="hl7"
            className="mt-1"
            rows={5}
            value={pasted}
            spellCheck={false}
            placeholder="Paste a message. Line feeds are fine — anything copied out of a log or an email arrives that way."
            onChange={setPasted}
          />
        </label>
      )}

      <button
        type="button"
        className="btn-primary"
        disabled={running || (!messageId && !pasted.trim())}
        onClick={run}
      >
        {running ? 'following…' : 'Follow it through the channel'}
      </button>

      {error && <p className="text-xs text-rose-400">{error}</p>}

      {trace && (
        <div className="space-y-3">
          <ol className="space-y-2">
            {trace.stages.map((s, i) => (
              <StageRow key={i} stage={s} last={i === trace.stages.length - 1} />
            ))}
          </ol>

          {trace.destinations && trace.destinations.length > 0 && (
            <div className="rounded-lg border border-slate-800 p-3">
              <h4 className="text-xs font-medium text-slate-300">Where it would go</h4>
              <ul className="mt-2 space-y-1.5">
                {trace.destinations.map((d) => (
                  <li key={d.name} className="text-xs">
                    <span
                      className={
                        'mr-2 font-medium ' + (d.would ? 'text-emerald-400' : 'text-slate-500')
                      }
                    >
                      {d.would ? '→' : '✕'} {d.name}
                    </span>
                    <span className="text-slate-400">{d.why}</span>
                    {d.reads && d.reads.length > 0 && <Values values={d.reads} />}
                  </li>
                ))}
              </ul>
            </div>
          )}

          {/* A stale trace gets a heading as well as the paragraph.
              A caveat in prose is a caveat somebody skims, and this one changes what the whole panel
              means: it is no longer an explanation of what happened, only of what would happen now. */}
          {trace.caveat && (
            <div className="rounded-lg border border-amber-800/50 bg-amber-950/20 p-3">
              {trace.stale && (
                <p className="mb-1 text-xs font-semibold text-amber-200">
                  This is not a record of what happened
                </p>
              )}
              <p className="text-xs leading-relaxed text-amber-200/90">{trace.caveat}</p>
            </div>
          )}

          {trace.output && (
            <div>
              <button
                type="button"
                className="text-xs text-slate-400 underline hover:text-slate-200"
                onClick={() => setShowBoth((v) => !v)}
              >
                {showBoth ? 'hide' : 'compare'} the message before and after
              </button>
              {showBoth && (
                <div className="mt-2 grid gap-3 lg:grid-cols-2">
                  <Pane title="As it arrived" body={trace.input} />
                  <Pane title="As it would leave" body={trace.output} />
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function StageRow({ stage, last }: { stage: TraceStage; last: boolean }) {
  // A stage that stopped the message is emphasised, because it is the answer. Everything before it
  // succeeded and is context.
  const stopped = !stage.ok
  return (
    <li
      className={
        'rounded-lg border p-3 ' +
        (stopped ? 'border-amber-800/60 bg-amber-950/20' : 'border-slate-800')
      }
    >
      <div className="flex items-baseline gap-2">
        <span
          className={
            'text-xs font-medium ' + (stopped ? 'text-amber-300' : 'text-emerald-400')
          }
        >
          {stopped ? '✕' : '✓'}
        </span>
        <span className="text-xs font-medium capitalize text-slate-200">{stage.stage}</span>
        {stopped && last && (
          <span className="text-[10px] uppercase tracking-wide text-amber-400">
            stopped here
          </span>
        )}
      </div>

      <p className="mt-1 text-xs leading-relaxed text-slate-400">{stage.detail}</p>

      {stage.reads && stage.reads.length > 0 && <Values values={stage.reads} />}

      {stage.changes && stage.changes.length > 0 && (
        <table className="mt-2 w-full text-xs">
          <thead>
            <tr className="text-slate-500">
              <th className="text-left font-normal">field</th>
              <th className="text-left font-normal">was</th>
              <th className="text-left font-normal">became</th>
            </tr>
          </thead>
          <tbody>
            {stage.changes.map((c, i) => (
              <tr key={i}>
                <td className="pr-2 font-mono text-slate-400">{c.path}</td>
                <td className="pr-2 font-mono text-slate-300">{c.from || '(empty)'}</td>
                <td className="font-mono text-sky-300">
                  {c.to || '(empty)'}
                  {c.note && <span className="ml-2 text-amber-400/80">{c.note}</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {/* Steps that ran and changed nothing.
          Shown as prominently as the changes rather than tucked away, because this is usually the
          answer. A step addressing a field the sender does not populate produces no error and no log
          line, so before this it was invisible - the change count was simply lower than expected, and
          noticing required an expectation precise enough to be surprised by. */}
      {/* Per-step detail, before the stage-level lists. Somebody who has opened this because a field
          is wrong wants to find the step, not read two summaries first. */}
      {stage.steps && stage.steps.length > 0 && (
        <div className="mt-3">
          <StepThrough steps={stage.steps} />
        </div>
      )}

      {stage.skipped && stage.skipped.length > 0 && (
        <div className="mt-2 rounded border border-slate-800 bg-slate-950/50 p-2">
          <p className="mb-1 text-[11px] font-medium text-slate-400">
            {stage.skipped.length === 1
              ? '1 step changed nothing'
              : `${stage.skipped.length} steps changed nothing`}
          </p>
          <ul className="space-y-1">
            {stage.skipped.map((sk, i) => (
              <li key={i} className="text-[11px] leading-relaxed text-slate-400">
                <span className="text-slate-300">{sk.step}</span>
                {sk.path && <span className="ml-1.5 font-mono text-amber-400/80">{sk.path}</span>}
                <span className="ml-1.5 text-slate-500">— {sk.why}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </li>
  )
}

function Values({ values }: { values: TraceValue[] }) {
  return (
    <div className="mt-2 flex flex-wrap gap-2">
      {values.map((v) => (
        <span key={v.path} className="rounded bg-slate-900 px-1.5 py-0.5 font-mono text-[11px]">
          <span className="text-slate-500">{v.path}</span>
          <span className="mx-1 text-slate-400">=</span>
          {v.present ? (
            <span className="text-slate-200">{v.value === '' ? '(empty)' : v.value}</span>
          ) : (
            // Absent and empty are different things, and filters turn on the difference. A
            // display that showed both as blank would reproduce the confusion this exists to
            // remove.
            <span className="text-amber-400">not present</span>
          )}
        </span>
      ))}
    </div>
  )
}

function Pane({ title, body }: { title: string; body: string }) {
  return (
    <div>
      <p className="mb-1 text-xs text-slate-500">{title}</p>
      <pre className="max-h-64 overflow-auto rounded-lg bg-slate-950/80 p-2 text-[11px] leading-relaxed text-slate-300">
        {body.replace(/\r/g, '\n')}
      </pre>
    </div>
  )
}
