// Controls for the settings area.
//
// Every control here is driven entirely by what the server said about a setting. Nothing in this file knows what retention is or
// what a passkey domain means - it knows how to draw a bounded number and a bare-string field. That is the whole point: adding a
// setting is one entry in the Go registry and no work here at all.

import { useEffect, useRef } from 'react'
import { EditorView, basicSetup } from 'codemirror'
import { EditorState } from '@codemirror/state'
import { javascript } from '@codemirror/lang-javascript'
import { oneDark } from '@codemirror/theme-one-dark'

/** One setting, exactly as the server describes it. */
export interface SettingDescriptor {
  key: string
  group: string
  subgroup: string
  label: string
  help: string
  kind: 'bool' | 'int' | 'string' | 'secret' | 'choice' | 'duration' | 'list' | 'code'
  widget: 'toggle' | 'slider' | 'number' | 'text' | 'secret' | 'radio' | 'select' | 'list' | 'code'
  effect: 'live' | 'restart' | 'reconnect'
  default: unknown
  min?: number
  max?: number
  unit?: string
  choices?: { value: string; label: string; help?: string }[]
  language?: string
  flag?: string
  advanced?: boolean
  sensitive?: boolean
}

export interface SettingsSubgroup {
  name: string
  settings: SettingDescriptor[]
}

export interface SettingsGroup {
  name: string
  subgroups: SettingsSubgroup[]
}

export interface SettingsSchema {
  groups: SettingsGroup[]
  values: Record<string, unknown>
  secrets: Record<string, boolean>
  explicit: string[]
  path: string
  writable: boolean
  restartRequired: string[]
}

// ---------------------------------------------------------------------------
// The decisions these controls make, as plain functions.
//
// Extracted so they can be tested without a DOM. This project has no component
// testing library and the properties worth pinning are all decisions rather than
// markup - what a badge says, whether a secret could ever render, when zero means
// something categorically different. Testing those directly is both cheaper and a
// better description of what must stay true.
// ---------------------------------------------------------------------------

/** What an effect badge says.
 *
 *  The wording matters more than it looks: this is the only thing between an operator and believing a
 *  change took effect when it did not. */
export function effectLabel(effect: SettingDescriptor['effect']): string {
  switch (effect) {
    case 'live':
      return 'Takes effect at once'
    case 'reconnect':
      return 'New connections only'
    default:
      // Anything unrecognised is treated as needing a restart, which is the safe direction to be
      // wrong in - it understates rather than overstates what has happened.
      return 'Needs a restart'
  }
}

/** The placeholder for a secret box.
 *
 *  Never the value. This is how somebody tells "already configured" from "blank" without being shown
 *  the credential. */
export function secretPlaceholder(isSet: boolean): string {
  return isSet ? 'A value is set — type to replace it' : 'Not set'
}

/** What a secret control puts in its input.
 *
 *  Always empty unless the operator is typing a replacement. A function so there is one answer rather
 *  than an expression repeated wherever a secret is drawn.
 *
 *  The server does not send secret values, so this is a second line rather than the first - but a
 *  control that would display one if handed one is a control waiting for a change elsewhere to become
 *  a disclosure. */
export function secretInputValue(pending: unknown): string {
  return typeof pending === 'string' ? pending : ''
}

/** Whether zero should be called out as meaning no limit.
 *
 *  Zero often means something categorically different - forever, or never - rather than one less than
 *  one. Left as a bare number at the end of a track, nobody reads it that way. */
export function showsNoLimitHint(value: number, min: number | undefined): boolean {
  return value === 0 && min === 0
}

/** Whether this interface understands a widget the server asked for.
 *
 *  A server newer than this interface should degrade to a usable text box rather than rendering
 *  nothing - a label with a gap under it gives no way to tell a bug from an empty value. */
export function isKnownWidget(widget: string): boolean {
  return ['toggle', 'slider', 'number', 'text', 'secret', 'radio', 'select', 'list', 'code'].includes(
    widget,
  )
}

/** EffectBadge says when a change will take hold.
 *
 *  Shown on the control rather than in a footnote. A settings page that silently needs a restart is
 *  how somebody changes retention, sees that it saved, and finds out three weeks later that nothing
 *  was ever deleted. */
export function EffectBadge({ effect }: { effect: SettingDescriptor['effect'] }) {
  const tone =
    effect === 'live'
      ? 'border border-emerald-800 bg-emerald-950/40 text-emerald-200'
      : effect === 'reconnect'
        ? 'bg-sky-100 text-sky-800'
        : 'bg-amber-100 text-amber-900'

  return (
    <span
      className={`rounded-full px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide ${tone}`}
    >
      {effectLabel(effect)}
    </span>
  )
}

/** Toggle is a switch, drawn rather than a checkbox.
 *
 *  Keyboard operable and labelled through aria-checked, because a div that looks like a switch and
 *  cannot be reached by tab is worse than the checkbox it replaced. */
function Toggle({
  value,
  onChange,
  describedBy,
}: {
  value: boolean
  onChange: (v: boolean) => void
  describedBy: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={value}
      aria-describedby={describedBy}
      onClick={() => onChange(!value)}
      className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 focus:outline-none focus:ring-2 focus:ring-sky-500 focus:ring-offset-2 ${
        value ? 'bg-sky-600' : 'bg-slate-300'
      }`}
    >
      <span
        className={`pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ${
          value ? 'translate-x-5' : 'translate-x-0'
        }`}
      />
    </button>
  )
}

/** Slider is a bounded number with its value shown.
 *
 *  The number is always visible next to the track, because a slider alone tells somebody roughly
 *  where they are and this is a field where 30 and 31 are different answers. */
function Slider({
  value,
  min,
  max,
  unit,
  onChange,
  describedBy,
}: {
  value: number
  min: number
  max: number
  unit?: string
  onChange: (v: number) => void
  describedBy: string
}) {
  return (
    <div className="flex items-center gap-3">
      <input
        type="range"
        min={min}
        max={max}
        value={value}
        aria-describedby={describedBy}
        onChange={(e) => onChange(Number(e.target.value))}
        className="h-2 w-48 cursor-pointer appearance-none rounded-full bg-slate-200 accent-sky-600"
      />
      {/* Editable as well as shown. A slider with a 0-3650 range cannot be driven to an exact
          number by dragging, and somebody who wants 90 days should be able to type 90. */}
      <input
        type="number"
        min={min}
        max={max}
        value={value}
        aria-label="exact value"
        onChange={(e) => onChange(Number(e.target.value))}
        className="w-20 rounded-lg border border-slate-300 px-2 py-1 text-sm tabular-nums"
      />
      {unit && <span className="text-xs text-slate-500">{unit}</span>}
      {/* Zero often means something categorically different - forever, or never - so it is called
          out rather than left as a number at one end of a track. */}
      {showsNoLimitHint(value, min) && (
        <span className="text-xs font-medium text-amber-700">(0 = no limit)</span>
      )}
    </div>
  )
}

/** Radio shows every option with its consequence.
 *
 *  Used where the options are worth reading rather than picking blind. A dropdown hides them until
 *  somebody opens it, which for a security setting is the wrong way round. */
function Radio({
  name,
  value,
  choices,
  onChange,
}: {
  name: string
  value: string
  choices: { value: string; label: string; help?: string }[]
  onChange: (v: string) => void
}) {
  return (
    <div className="space-y-2">
      {choices.map((c) => (
        <label
          key={c.value}
          className={`flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors ${
            value === c.value
              ? 'border-sky-500 bg-sky-950/40'
              : 'border-slate-800 bg-slate-900/60 hover:border-slate-700'
          }`}
        >
          <input
            type="radio"
            name={name}
            checked={value === c.value}
            onChange={() => onChange(c.value)}
            className="mt-0.5 accent-sky-600"
          />
          <span className="min-w-0">
            <span className="block text-sm font-medium text-slate-100">{c.label}</span>
            {c.help && (
              <span className="mt-0.5 block text-xs leading-relaxed text-slate-400">{c.help}</span>
            )}
          </span>
        </label>
      ))}
    </div>
  )
}

/** CodeBox is a CodeMirror 6 editor.
 *
 *  Built once and fed through a transaction, rather than recreated when the value changes - a
 *  recreated editor loses the cursor, which makes it unusable for anything longer than a line. */
function CodeBox({
  value,
  language,
  onChange,
}: {
  value: string
  language?: string
  onChange: (v: string) => void
}) {
  const host = useRef<HTMLDivElement | null>(null)
  const view = useRef<EditorView | null>(null)
  // Held in a ref so the update listener closes over a stable reference. Closing over the prop
  // would freeze the first onChange and silently drop every edit after a re-render.
  const emit = useRef(onChange)
  emit.current = onChange

  useEffect(() => {
    if (!host.current || view.current) return

    // basicSetup rather than a hand-picked list, because it is what the script editor uses - two
    // editors in one product differing over which shortcuts work is a small thing that reads as
    // carelessness.
    const extensions = [
      basicSetup,
      oneDark,
      EditorView.updateListener.of((update) => {
        if (update.docChanged) emit.current(update.state.doc.toString())
      }),
    ]
    if (language === 'javascript') extensions.push(javascript())

    view.current = new EditorView({
      state: EditorState.create({ doc: value, extensions }),
      parent: host.current,
    })

    return () => {
      view.current?.destroy()
      view.current = null
    }
    // Deliberately once. The value is pushed in through the effect below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [language])

  useEffect(() => {
    const v = view.current
    if (!v) return
    const current = v.state.doc.toString()
    // Only when it actually differs, or every keystroke would dispatch a transaction replacing the
    // document with what the user just typed and the cursor would jump to the end.
    if (current !== value) {
      v.dispatch({ changes: { from: 0, to: current.length, insert: value } })
    }
  }, [value])

  return <div ref={host} className="overflow-hidden rounded-lg border border-slate-300 text-sm" />
}

/** ListBox edits an ordered list of single lines. */
function ListBox({
  value,
  onChange,
}: {
  value: string[]
  onChange: (v: string[]) => void
}) {
  return (
    <div className="space-y-2">
      {value.map((entry, i) => (
        <div key={i} className="flex items-center gap-2">
          <input
            className="input flex-1"
            value={entry}
            onChange={(e) => {
              const next = [...value]
              next[i] = e.target.value
              onChange(next)
            }}
          />
          <button
            type="button"
            className="btn text-xs"
            onClick={() => onChange(value.filter((_, j) => j !== i))}
            aria-label={`remove entry ${i + 1}`}
          >
            Remove
          </button>
        </div>
      ))}
      <button type="button" className="btn text-xs" onClick={() => onChange([...value, ''])}>
        Add another
      </button>
    </div>
  )
}

/** SettingControl draws whichever control the server asked for.
 *
 *  The switch is exhaustive on widget rather than on kind, and the default case renders a plain text
 *  box rather than nothing - a server newer than this interface should degrade to something usable
 *  instead of showing a label with a gap under it. */
/** settingControlId is the id the primary control of a setting carries. */
export function settingControlId(key: string): string {
  return `${key}-control`
}

/** labelsItsControl reports whether this widget renders one element a label may reference.
 *
 * Deliberately a short list rather than an exclusion. A toggle is a button with role=switch and a slider
 * is a range paired with a number box; neither may be the target of a label's for attribute, and both
 * already carry their own accessible name. A radio group and a list render several controls, so naming
 * one of them would be a confidently wrong answer. */
export function labelsItsControl(widget: string): boolean {
  return widget === 'text' || widget === 'secret' || widget === 'select' || widget === 'number'
}

export function SettingControl({
  setting,
  value,
  secretIsSet,
  onChange,
}: {
  setting: SettingDescriptor
  value: unknown
  secretIsSet: boolean
  onChange: (v: unknown) => void
}) {
  const describedBy = `${setting.key}-help`
  // The label rendered beside this control points here. Derived from the key rather than generated, so
  // both sides can agree on it without threading a value through.
  const controlId = settingControlId(setting.key)

  switch (setting.widget) {
    case 'toggle':
      return (
        <Toggle
          value={Boolean(value)}
          onChange={onChange}
          describedBy={describedBy}
        />
      )

    case 'slider':
      return (
        <Slider
          value={Number(value ?? 0)}
          min={setting.min ?? 0}
          max={setting.max ?? 100}
          unit={setting.unit}
          onChange={onChange}
          describedBy={describedBy}
        />
      )

    case 'number':
      return (
        <div className="flex items-center gap-2">
          <input
            id={controlId}
            type="number"
            min={setting.min}
            max={setting.max}
            value={Number(value ?? 0)}
            aria-describedby={describedBy}
            onChange={(e) => onChange(Number(e.target.value))}
            className="w-28 rounded-lg border border-slate-300 px-2 py-1 text-sm tabular-nums"
          />
          {setting.unit && <span className="text-xs text-slate-500">{setting.unit}</span>}
        </div>
      )

    case 'radio':
      return (
        <Radio
          name={setting.key}
          value={String(value ?? '')}
          choices={setting.choices ?? []}
          onChange={onChange}
        />
      )

    case 'select':
      return (
        <select
          id={controlId}
          className="input max-w-xs"
          value={String(value ?? '')}
          aria-describedby={describedBy}
          onChange={(e) => onChange(e.target.value)}
        >
          {(setting.choices ?? []).map((c) => (
            <option key={c.value} value={c.value}>
              {c.label}
            </option>
          ))}
        </select>
      )

    case 'secret':
      return (
        <div className="space-y-1">
          <input
            id={controlId}
            type="password"
            className="input max-w-md"
            autoComplete="new-password"
            // Never the existing value, which the server does not send. The placeholder is how
            // somebody knows whether one is already set without being shown it.
            placeholder={secretPlaceholder(secretIsSet)}
            value={secretInputValue(value)}
            aria-describedby={describedBy}
            onChange={(e) => onChange(e.target.value)}
          />
          {secretIsSet && (
            <p className="text-xs text-slate-500">
              Leave this blank to keep the current value.
            </p>
          )}
        </div>
      )

    case 'list':
      return <ListBox value={Array.isArray(value) ? (value as string[]) : []} onChange={onChange} />

    case 'code':
      return (
        <CodeBox
          value={typeof value === 'string' ? value : ''}
          language={setting.language}
          onChange={onChange}
        />
      )

    default:
      return (
        <input
          id={controlId}
          className="input max-w-md"
          value={typeof value === 'string' ? value : String(value ?? '')}
          aria-describedby={describedBy}
          onChange={(e) => onChange(e.target.value)}
        />
      )
  }
}
