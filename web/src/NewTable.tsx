import { useState } from 'react'
import { api, ApiError } from './api'
import { ErrorBox, Field } from './ui'
import type { UiError } from './store'

/**
 * Creating a shared mapping table without touching the server.
 *
 * These were readable and not writable: the section reported which channels each table affected and said "no
 * shared tables yet" on a fresh installation, with nothing to press. Making one meant writing YAML on the
 * machine by hand, which is the thing this product exists to stop people doing.
 *
 * The description and the reason per row are asked for rather than tucked away, because this is the one place
 * where provenance is the whole point. The section's own text says why: most of what makes an interface hard to
 * maintain is that nobody knows why a mapping is the way it is, and the person who knew has left. A mapping with
 * a reason attached can be argued with; one without becomes something nobody dares change and nobody dares
 * delete.
 */

type Row = { from: string; to: string; why: string }

const emptyRow: Row = { from: '', to: '', why: '' }

export function NewTable({ onSaved }: { onSaved: (notes: string[]) => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [describes, setDescribes] = useState('')
  const [source, setSource] = useState('')
  const [rows, setRows] = useState<Row[]>([{ ...emptyRow }, { ...emptyRow }])
  const [fallback, setFallback] = useState('')
  const [strict, setStrict] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<UiError | null>(null)

  function setRow(i: number, patch: Partial<Row>) {
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  }

  function reset() {
    setName('')
    setDescribes('')
    setSource('')
    setRows([{ ...emptyRow }, { ...emptyRow }])
    setFallback('')
    setStrict(false)
    setError(null)
  }

  async function save() {
    setBusy(true)
    setError(null)
    try {
      const res = await api.writeTable({
        name,
        describes,
        source,
        default: fallback,
        strict,
        // Blank rows are dropped rather than refused. Two are offered to start with and somebody who needs one
        // should not have to think about the other.
        entries: rows
          .filter((r) => r.from.trim() !== '' || r.to.trim() !== '')
          .map((r) => ({ from: r.from, to: r.to, why: r.why || undefined })),
      })
      reset()
      setOpen(false)
      // Handed upwards rather than shown here.
      //
      // Saving the first table changes the section from its empty state to its populated one, which unmounts
      // this component and mounts a fresh one - taking any success message with it. The save worked and the
      // interface said nothing, which is indistinguishable from the save having failed silently. The notice
      // belongs to whoever survives the change.
      onSaved(res.notes)
    } catch (e) {
      setError({
        message: e instanceof Error ? e.message : String(e),
        problems: e instanceof ApiError ? e.problems : [],
      })
    } finally {
      setBusy(false)
    }
  }

  if (!open) {
    return (
      <button className="btn-primary py-1 text-xs" onClick={() => setOpen(true)}>
        New shared table
      </button>
    )
  }

  return (
    <div className="rounded-xl border border-slate-800 bg-slate-900/40 p-4">
      <h3 className="text-sm font-medium text-slate-200">A new shared table</h3>

      <div className="mt-3 grid gap-3 sm:grid-cols-2">
        {/* Field rather than a hand-rolled label.
            
            A hint placed inside a label becomes part of the control's accessible name, so this read as "Name How
            channels will refer to it, in a map step" - which is unusable to a screen reader and to anything
            addressing the field by name. Field puts the label text in the label and the hint in
            aria-describedby, which is the distinction that matters. */}
        <Field label="Name" hint="How channels will refer to it, in a map step.">
          <input
            className="input w-full py-1 text-xs"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="sex-codes"
          />
        </Field>

        <Field label="Where these came from" hint="Optional, and worth filling in.">
          <input
            className="input w-full py-1 text-xs"
            value={source}
            onChange={(e) => setSource(e.target.value)}
            placeholder="the lab's interface specification, v4"
          />
        </Field>
      </div>

      <div className="mt-3">
        <Field
          label="What this table is for"
          hint="One sentence. Required, because a mapping nobody can explain becomes one nobody dares change."
        >
          <input
            className="input w-full py-1 text-xs"
            value={describes}
            onChange={(e) => setDescribes(e.target.value)}
            placeholder="How this hospital's sex codes map to what the receiver expects"
          />
        </Field>
      </div>

      <div className="mt-4">
        <div className="grid grid-cols-[1fr_1fr_2fr_auto] gap-2 pb-1 text-[11px] uppercase tracking-wide text-slate-500">
          <span>From</span>
          <span>To</span>
          <span>Why</span>
          <span />
        </div>
        <div className="space-y-2">
          {rows.map((r, i) => (
            <div key={i} className="grid grid-cols-[1fr_1fr_2fr_auto] gap-2">
              <input
                aria-label={`Value ${i + 1} to map from`}
                className="input py-1 text-xs"
                value={r.from}
                onChange={(e) => setRow(i, { from: e.target.value })}
              />
              <input
                aria-label={`Value ${i + 1} to map to`}
                className="input py-1 text-xs"
                value={r.to}
                onChange={(e) => setRow(i, { to: e.target.value })}
              />
              <input
                aria-label={`Why value ${i + 1} maps that way`}
                className="input py-1 text-xs"
                value={r.why}
                onChange={(e) => setRow(i, { why: e.target.value })}
                placeholder="agreed with the lab, 2019"
              />
              <button
                className="btn-ghost px-2 py-1 text-xs"
                aria-label={`Remove row ${i + 1}`}
                onClick={() => setRows((rs) => (rs.length > 1 ? rs.filter((_, j) => j !== i) : rs))}
                disabled={rows.length <= 1}
              >
                ✕
              </button>
            </div>
          ))}
        </div>
        <button
          className="btn-ghost mt-2 py-1 text-xs"
          onClick={() => setRows((rs) => [...rs, { ...emptyRow }])}
        >
          Another row
        </button>
      </div>

      <div className="mt-4 grid gap-3 sm:grid-cols-2">
        <Field
          label="If a value is not in the table"
          hint="Empty means pass the original value through unchanged."
        >
          <input
            className="input w-full py-1 text-xs"
            value={fallback}
            onChange={(e) => setFallback(e.target.value)}
            placeholder="leave it alone"
          />
        </Field>

        <label className="flex items-start gap-2 text-xs text-slate-400">
          <input
            type="checkbox"
            className="mt-0.5"
            checked={strict}
            onChange={(e) => setStrict(e.target.checked)}
          />
          <span>
            <span className="block text-slate-300">Refuse values this table does not know</span>
            <span className="mt-0.5 block text-[11px] text-slate-500">
              Turns an unmapped value into a failed message rather than letting it through. Right when the
              receiver would reject it anyway, wrong when most values are meant to pass unchanged.
            </span>
          </span>
        </label>
      </div>

      {error && (
        <div className="mt-3">
          <ErrorBox error={error} onDismiss={() => setError(null)} />
        </div>
      )}

      <div className="mt-4 flex gap-2">
        <button className="btn-primary py-1 text-xs" onClick={() => void save()} disabled={busy}>
          {busy ? 'Saving…' : 'Save table'}
        </button>
        <button
          className="btn-ghost py-1 text-xs"
          onClick={() => {
            reset()
            setOpen(false)
          }}
          disabled={busy}
        >
          Cancel
        </button>
      </div>
    </div>
  )
}
