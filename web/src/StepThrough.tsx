import { useState } from 'react'
import type { TraceStep } from './api'

/** StepThrough walks a message through a channel's transformation steps, one at a time.
 *
 * Why this is worth its own component rather than a longer list inside the trace panel: the panel
 * answers "what happened", which is a thing to read. This answers "which step did that", which is a
 * thing to operate. They want different controls.
 *
 * The design point is that the message is shown at each step, not just the changes. A list of deltas
 * cannot show the message as it stood before the step that broke it, and that is what somebody is
 * looking for.
 */
export function StepThrough({ steps }: { steps: TraceStep[] }) {
  const [at, setAt] = useState(0)
  const [showAll, setShowAll] = useState(false)

  if (steps.length === 0) return null

  const current = steps[at]
  if (!current) return null

  // Counted rather than filtered, because a step that did nothing still has to keep its position -
  // its number is how somebody finds it in the channel's configuration.
  const noEffect = steps.filter((s) => s.outcome === 'no-effect').length

  return (
    <div className="rounded-xl border border-indigo-900/60 bg-indigo-950/20 p-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h4 className="text-sm font-semibold text-indigo-100">Step through</h4>
          <p className="mt-0.5 text-xs text-indigo-300/80">
            {steps.length} step{steps.length === 1 ? '' : 's'}, each showing the message as it stood
            afterwards.
            {noEffect > 0 && (
              <>
                {' '}
                <span className="text-amber-300">
                  {noEffect} found nothing to change
                </span>
                , which is usually a field the sender does not populate.
              </>
            )}
          </p>
        </div>
        <button
          className="btn-ghost text-xs"
          onClick={() => setShowAll((v) => !v)}
          aria-pressed={showAll}
        >
          {showAll ? 'One at a time' : 'Show every step'}
        </button>
      </div>

      {/* The rail. Every step is a control, so a fourteen-step pipeline can be jumped around rather
          than clicked through, and the colour says what happened without opening it. */}
      <ol className="mb-4 flex flex-wrap gap-1.5" aria-label="Transformation steps">
        {steps.map((s, i) => (
          <li key={s.number}>
            <button
              onClick={() => {
                setAt(i)
                setShowAll(false)
              }}
              aria-current={!showAll && i === at ? 'step' : undefined}
              title={`${s.label} — ${s.detail}`}
              className={`rounded-md px-2 py-1 font-mono text-xs transition ${outcomeChip(s.outcome)} ${
                !showAll && i === at ? 'ring-2 ring-indigo-400' : ''
              }`}
            >
              {s.number}
            </button>
          </li>
        ))}
      </ol>

      {showAll ? (
        <div className="space-y-3">
          {steps.map((s) => (
            <StepDetail key={s.number} step={s} />
          ))}
        </div>
      ) : (
        <>
          <StepDetail step={current} />
          <div className="mt-3 flex items-center justify-between gap-3">
            <button
              className="btn-ghost text-xs"
              onClick={() => setAt((v) => Math.max(0, v - 1))}
              disabled={at === 0}
            >
              ← Previous
            </button>
            <span className="text-xs text-slate-500">
              Step {current.number} of {steps.length}
            </span>
            <button
              className="btn-ghost text-xs"
              onClick={() => setAt((v) => Math.min(steps.length - 1, v + 1))}
              disabled={at === steps.length - 1}
            >
              Next →
            </button>
          </div>
        </>
      )}
    </div>
  )
}

function StepDetail({ step }: { step: TraceStep }) {
  return (
    <div className="rounded-lg border border-slate-800 bg-slate-950/60 p-3">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="font-mono text-xs text-slate-500">{step.number}</span>
        <span className="text-sm font-medium text-slate-200">{step.label}</span>
        <span className={`rounded px-1.5 py-0.5 text-xs font-medium ${outcomeChip(step.outcome)}`}>
          {outcomeWord(step.outcome)}
        </span>
        {step.path && <span className="font-mono text-xs text-slate-500">{step.path}</span>}
      </div>

      <p className="mt-1.5 text-xs leading-relaxed text-slate-400">{step.detail}</p>

      {step.changes && step.changes.length > 0 && (
        <ul className="mt-2 space-y-1">
          {step.changes.map((c, i) => (
            <li key={i} className="flex flex-wrap items-baseline gap-2 text-xs">
              <span className="font-mono text-slate-500">{c.path}</span>
              <span className="font-mono text-rose-300/80 line-through">{c.from || '(empty)'}</span>
              <span className="text-slate-600">→</span>
              <span className="font-mono text-emerald-300">{c.to || '(empty)'}</span>
              {c.note && <span className="text-slate-500">{c.note}</span>}
            </li>
          ))}
        </ul>
      )}

      <details className="mt-2.5">
        <summary className="cursor-pointer text-xs text-slate-500 hover:text-slate-300">
          The message after this step
        </summary>
        <pre className="mt-2 max-h-64 overflow-auto rounded-md border border-slate-800 bg-black/40 p-2.5 font-mono text-xs leading-relaxed whitespace-pre-wrap text-slate-300">
          {step.message.replace(/\r/g, '\n')}
        </pre>
      </details>
    </div>
  )
}

/* Whole class names, never assembled. Tailwind scans for literals, so a constructed class is absent
   from the stylesheet and fails as an unstyled chip rather than a build error. */
function outcomeChip(outcome: TraceStep['outcome']): string {
  switch (outcome) {
    case 'changed':
      return 'bg-emerald-950/60 text-emerald-300 border border-emerald-900/60'
    case 'no-effect':
      return 'bg-amber-950/60 text-amber-300 border border-amber-900/60'
    case 'skipped':
      return 'bg-slate-800 text-slate-400 border border-slate-700'
    case 'failed':
      return 'bg-rose-950/60 text-rose-300 border border-rose-900/60'
  }
}

/** The words matter more than usual here.
 *
 * "Skipped" and "found nothing" are the two outcomes that look identical in the message and mean
 * opposite things: one is the step working as written, the other is almost always the bug. Naming
 * them differently is the whole point of the view.
 */
function outcomeWord(outcome: TraceStep['outcome']): string {
  switch (outcome) {
    case 'changed':
      return 'changed'
    case 'no-effect':
      return 'found nothing'
    case 'skipped':
      return 'condition false'
    case 'failed':
      return 'failed'
  }
}
