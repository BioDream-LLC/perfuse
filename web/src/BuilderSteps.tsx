import { useMemo } from 'react'
import { Choose, Num, Pair, Text } from './builderFields'
import type { DraftStep, StepKind } from './model'
import type { DictSegment } from './api'

// Building the changes a channel makes to a message.
//
// This did not exist before: the form could filter messages but not alter them, so any channel
// needing a field set, a code translated or a date reformatted had to be written by hand. That
// is most real channels.
//
// The path field resolves against the HL7 dictionary as it is typed. PID-8 means nothing on
// its own, and somebody intending to set the patient's sex who has typed PID-7 sees
// "Date/Time of Birth" appear underneath. That is the difference between a mistake caught in a
// form and a mistake found in production three weeks later by a pharmacist.

const STEP_OPTIONS: { value: StepKind; label: string }[] = [
  { value: 'set', label: 'Set a field to a value' },
  { value: 'copy', label: 'Copy one field into another' },
  { value: 'map', label: 'Translate a code using a table' },
  { value: 'replace', label: 'Find and replace text' },
  { value: 'clear', label: 'Empty a field' },
  { value: 'remove', label: 'Remove a field completely' },
  { value: 'trim', label: 'Trim surrounding spaces' },
  { value: 'case', label: 'Change to upper or lower case' },
  { value: 'pad', label: 'Pad to a fixed width' },
  { value: 'date', label: 'Reformat a date' },
]

/** describePath resolves an HL7 path against the dictionary. */
//
// Exported for testing. The parsing is deliberately forgiving: people write PID-5, PID.5 and
// PID5 interchangeably, and rejecting two of the three would be pedantry in a field meant to
// be filled in quickly.
export function describePath(path: string, dict: DictSegment[]): string | null {
  const trimmed = path.trim().toUpperCase()
  if (!trimmed) return null

  const m = /^([A-Z][A-Z0-9]{1,2})(?:[-.]?(\d+))?(?:\.(\d+))?/.exec(trimmed)
  if (!m || !m[1]) return null

  const seg = dict.find((s) => s.segment === m[1])
  if (!seg) return null
  if (!m[2]) return seg.description

  const field = seg.fields.find((f) => f.number === Number(m[2]))
  if (!field) return `${seg.segment} has no field ${m[2]} in the dictionary`

  // Resolving the component matters because PID-5.1 is the family name and PID-5.2 is the
  // given name, which is a distinction people get wrong constantly.
  if (m[3] && field.components?.length) {
    const comp = field.components[Number(m[3]) - 1]
    if (comp) return `${field.name} — ${comp}`
  }

  const extra: string[] = []
  if (field.repeats) extra.push('may repeat')
  if (field.table) extra.push(`table ${field.table}`)
  return field.name + (extra.length ? ` (${extra.join(', ')})` : '')
}

function PathField({
  label,
  value,
  onChange,
  dict,
  hint,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  dict: DictSegment[]
  hint?: string
}) {
  const resolved = useMemo(() => describePath(value, dict), [value, dict])
  return (
    <div>
      <Text label={label} required value={value} onChange={onChange} placeholder="PID-5.1" mono />
      {resolved ? (
        <p className="mt-1 text-xs text-sky-400">{resolved}</p>
      ) : (
        hint && <p className="mt-1 text-xs text-slate-500">{hint}</p>
      )}
    </div>
  )
}

function TableEditor({
  rows,
  onChange,
}: {
  rows: [string, string][]
  onChange: (next: [string, string][]) => void
}) {
  return (
    <div className="space-y-2">
      <span className="text-xs font-medium text-slate-300">Translations</span>

      {rows.length === 0 && (
        <p className="text-xs text-slate-500">
          Nothing yet. Add each code the sender uses and what it should become.
        </p>
      )}

      {rows.map(([from, to], i) => (
        <div className="flex items-center gap-2" key={i}>
          <input
            className="w-24 rounded-md border border-slate-700 bg-slate-900 px-2 py-1 font-mono text-xs text-slate-100"
            value={from}
            placeholder="1"
            aria-label={`code ${i + 1}`}
            onChange={(e) => {
              // Held as an ordered list rather than an object so that editing a code does
              // not move its row while somebody is typing in it, which is what happens when
              // a key is deleted and re-added.
              const next = rows.slice() as [string, string][]
              next[i] = [e.target.value, to]
              onChange(next)
            }}
          />
          <span className="text-slate-500">→</span>
          <input
            className="w-24 rounded-md border border-slate-700 bg-slate-900 px-2 py-1 font-mono text-xs text-slate-100"
            value={to}
            placeholder="M"
            aria-label={`becomes ${i + 1}`}
            onChange={(e) => {
              const next = rows.slice() as [string, string][]
              next[i] = [from, e.target.value]
              onChange(next)
            }}
          />
          <button
            type="button"
            className="text-xs text-slate-500 hover:text-rose-400"
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
          >
            remove
          </button>
        </div>
      ))}

      <button
        type="button"
        className="rounded-md border border-slate-700 px-2 py-1 text-xs text-slate-300 hover:border-sky-600"
        onClick={() => onChange([...rows, ['', '']])}
      >
        add a translation
      </button>
    </div>
  )
}

export function StepCard({
  step,
  index,
  total,
  dict,
  onChange,
  onRemove,
  onMove,
}: {
  step: DraftStep
  index: number
  total: number
  dict: DictSegment[]
  onChange: (next: DraftStep) => void
  onRemove: () => void
  onMove: (to: number) => void
}) {
  const patch = (p: Partial<DraftStep>) => onChange({ ...step, ...p })

  return (
    <div className="rounded-lg border border-slate-700 bg-slate-900/40 p-3">
      <div className="mb-3 flex items-center justify-between gap-2">
        <span className="text-xs text-slate-500">
          step {index + 1} of {total}
        </span>
        <div className="flex gap-1">
          <button
            type="button"
            onClick={() => onMove(index - 1)}
            disabled={index === 0}
            title="Run earlier"
            className="rounded border border-slate-700 px-2 py-1 text-xs text-slate-400 disabled:opacity-30 enabled:hover:text-slate-100"
          >
            ↑
          </button>
          <button
            type="button"
            onClick={() => onMove(index + 1)}
            disabled={index === total - 1}
            title="Run later"
            className="rounded border border-slate-700 px-2 py-1 text-xs text-slate-400 disabled:opacity-30 enabled:hover:text-slate-100"
          >
            ↓
          </button>
          <button
            type="button"
            onClick={onRemove}
            className="rounded border border-slate-700 px-2 py-1 text-xs text-rose-400 hover:border-rose-700"
          >
            remove
          </button>
        </div>
      </div>

      <div className="space-y-3">
        <Choose
          label="What should this do?"
          value={step.kind}
          onChange={(kind) => patch({ kind })}
          options={STEP_OPTIONS}
        />

        {step.kind === 'copy' ? (
          <Pair>
            <PathField
              label="Copy from"
              value={step.from}
              onChange={(from) => patch({ from })}
              dict={dict}
            />
            <PathField
              label="Into"
              value={step.to}
              onChange={(to) => patch({ to })}
              dict={dict}
            />
          </Pair>
        ) : (
          <PathField
            label="Field"
            value={step.path}
            onChange={(path) => patch({ path })}
            dict={dict}
            hint={
              step.kind === 'clear'
                ? 'Empties the value but leaves the field present, which is not the same as removing it.'
                : undefined
            }
          />
        )}

        {step.kind === 'set' && (
          <Text
            label="Set it to"
            value={step.value}
            onChange={(value) => patch({ value })}
            hint="Written literally. Quoting is handled for you, so a value with a colon in it or a leading zero is safe."
          />
        )}

        {step.kind === 'copy' && (
          <Text
            label="If the source is empty, use"
            value={step.fallback}
            onChange={(fallback) => patch({ fallback })}
            hint="Without this, copying an empty field clears the target. That is occasionally wanted and usually not."
          />
        )}

        {step.kind === 'replace' && (
          <>
            <Pair>
              <Text
                label="Find"
                value={step.pattern}
                onChange={(pattern) => patch({ pattern })}
                placeholder="^0+"
                mono
                hint="A regular expression."
              />
              <Text
                label="Replace with"
                value={step.replacement}
                onChange={(replacement) => patch({ replacement })}
                mono
                hint="Leave empty to delete what was matched."
              />
            </Pair>
            <Choose
              label="How many matches"
              value={step.all ? 'all' : 'first'}
              onChange={(v) => patch({ all: v === 'all' })}
              options={[
                { value: 'first', label: 'just the first' },
                { value: 'all', label: 'every match' },
              ]}
            />
          </>
        )}

        {step.kind === 'map' && (
          <>
            <TableEditor rows={step.table} onChange={(table) => patch({ table })} />
            <Choose
              label="A value that is not in the table"
              value={step.unmatched}
              onChange={(unmatched) => patch({ unmatched })}
              options={[
                { value: 'keep', label: 'is left exactly as it is' },
                { value: 'default', label: 'becomes a fallback value' },
                { value: 'strict', label: 'is an error, and the message is not delivered' },
              ]}
              hint="Leaving it alone passes an untranslated code downstream, where something else has to cope with it. Erroring stops the message. Neither is always right, which is why you are asked."
            />
            {step.unmatched === 'default' && (
              <Text
                label="Fallback value"
                value={step.fallback}
                onChange={(fallback) => patch({ fallback })}
                placeholder="U"
              />
            )}
          </>
        )}

        {step.kind === 'pad' && (
          <>
            <Pair>
              <Num
                label="Width"
                value={step.width}
                onChange={(width) => patch({ width: width ?? 0 })}
              />
              <Text
                label="Pad with"
                value={step.padWith}
                onChange={(padWith) => patch({ padWith })}
                placeholder="0"
                mono
              />
            </Pair>
            <Choose
              label="Which side"
              value={step.padRight ? 'right' : 'left'}
              onChange={(v) => patch({ padRight: v === 'right' })}
              options={[
                { value: 'left', label: 'on the left, as for a number' },
                { value: 'right', label: 'on the right, as for a name' },
              ]}
            />
          </>
        )}

        {step.kind === 'date' && (
          <Pair>
            <Text
              label="Currently formatted as"
              value={step.from}
              onChange={(from) => patch({ from })}
              placeholder="yyyyMMdd"
              mono
            />
            <Text
              label="Change it to"
              value={step.to}
              onChange={(to) => patch({ to })}
              placeholder="yyyy-MM-dd"
              mono
            />
          </Pair>
        )}

        {step.kind === 'date' && (
          <Choose
            label="If the value does not match that format"
            value={step.dateOnError}
            onChange={(dateOnError) => patch({ dateOnError })}
            options={[
              { value: 'fail', label: 'stop the message (default)' },
              { value: 'keep', label: 'leave the value as it is' },
              { value: 'clear', label: 'empty the field' },
            ]}
            hint={
              step.dateOnError === 'keep'
                ? 'The value passes through in its original format. Downstream then sees two formats in one field, which is the failure this step existed to prevent — but it is the right choice where the field is optional and a partner sends it inconsistently.'
                : step.dateOnError === 'clear'
                  ? 'The field is emptied. Honest, and better than a wrong format, but a receiver that requires the field then rejects the message instead.'
                  : 'Stopping is the default and usually right: a timestamp that silently did not convert is accepted downstream and then misread, which is worse than a message that failed loudly.'
            }
          />
        )}

        {step.kind === 'case' && (
          <Choose
            label="Change to"
            value={step.caseTo}
            onChange={(caseTo) => patch({ caseTo })}
            options={[
              { value: 'upper', label: 'UPPER CASE' },
              { value: 'lower', label: 'lower case' },
            ]}
          />
        )}

        <Text
          label="Why does this step exist?"
          value={step.description}
          onChange={(description) => patch({ description })}
          placeholder="strip the leading zeroes Epic adds"
          hint="Optional, and the most useful thing on this card six months from now. It is shown everywhere the step appears."
        />

        <Text
          label="Only run it when"
          value={step.when}
          onChange={(when) => patch({ when })}
          placeholder="leave empty to run for every message"
          mono
        />
      </div>
    </div>
  )
}

export { STEP_OPTIONS }
