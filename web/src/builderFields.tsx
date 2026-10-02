import { CodeArea } from './CodeArea'
import { ScriptEditor, type ScriptKind } from './ScriptEditor'
import { cloneElement, isValidElement, useId } from 'react'
import type { ReactElement, ReactNode } from 'react'

// Form controls for the channel builder.
//
// Every control takes a hint, and most of them are used with one. That is the whole
// argument of this screen: the reason somebody hires help to configure an integration engine
// is not that the fields are hard to type, it is that nothing on screen says what happens if
// you get one wrong. A label that reads "Acknowledge when" tells you nothing. A hint saying
// an acknowledged message can still be lost if the process restarts tells you what you are
// choosing between.

interface BaseProps {
  label: string
  /** What this setting does and what goes wrong if it is set badly. */
  hint?: ReactNode
  /** Shown when the field is required and empty, or otherwise wrong. */
  problem?: string
  required?: boolean
}

/**
 * Wrapper labels one builder control.
 *
 * The label element holds only the label text, and the control is joined to it by id. It used to wrap
 * the control, the "required" marker and the hint all inside the label, which associated them - but it
 * also made every one of those words part of the field's accessible NAME. A screen reader announced the
 * field called "Listen on required A port on its own accepts connections on every interface...", which
 * is a paragraph where a name belongs.
 *
 * The hint is attached with aria-describedby instead. That is the distinction the two attributes exist
 * for: a name identifies the field, a description explains it, and a reader announces the name first and
 * the description after. Nothing changes visually.
 */
function Wrapper({
  label,
  hint,
  problem,
  required,
  children,
}: BaseProps & { children: ReactNode }) {
  const generated = useId()
  const element = isValidElement(children) ? (children as ReactElement) : undefined
  // Only a real form control may be referenced by a label's for attribute. Pointing it at a div looks
  // associated and is not, which is worse than leaving it off.
  const canAssociate =
    (typeof element?.type === 'string' && ['input', 'select', 'textarea'].includes(element.type)) ||
    element?.type === CodeArea
  const id = canAssociate ? generated : undefined
  const describedBy = problem ? `${generated}-problem` : hint ? `${generated}-hint` : undefined

  const control =
    canAssociate && element
      ? cloneElement(element as ReactElement<{ id?: string; 'aria-describedby'?: string }>, {
          id,
          'aria-describedby': describedBy,
        })
      : children

  return (
    <div className="block">
      <span className="flex items-baseline gap-2">
        <label className="text-xs font-medium text-slate-300" htmlFor={id}>
          {label}
        </label>
        {/* Marked with aria-hidden because required is already carried by the control itself, and
            hearing the word twice is worse than hearing it once. */}
        {required && (
          <span aria-hidden="true" className="text-[10px] uppercase tracking-wide text-slate-500">
            required
          </span>
        )}
      </span>
      <div className="mt-1">{control}</div>
      {problem ? (
        <span id={`${generated}-problem`} className="mt-1 block text-xs text-rose-400">
          {problem}
        </span>
      ) : (
        hint && (
          <span id={`${generated}-hint`} className="mt-1 block text-xs leading-relaxed text-slate-500">
            {hint}
          </span>
        )
      )}
    </div>
  )
}

const inputClass =
  'w-full rounded-md border border-slate-700 bg-slate-900 px-2 py-1.5 text-sm text-slate-100 ' +
  'placeholder:text-slate-400 focus:border-sky-600 focus:outline-none'

export function Text({
  value,
  onChange,
  placeholder,
  mono,
  ...base
}: BaseProps & {
  value: string | undefined
  onChange: (v: string) => void
  placeholder?: string
  mono?: boolean
}) {
  return (
    <Wrapper {...base}>
      <input
        type="text"
        className={inputClass + (mono ? ' font-mono text-xs' : '')}
        value={value ?? ''}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
      />
    </Wrapper>
  )
}

export function Secret({
  value,
  onChange,
  ...base
}: BaseProps & { value: string | undefined; onChange: (v: string) => void }) {
  return (
    <Wrapper {...base}>
      <input
        type="password"
        className={inputClass}
        value={value ?? ''}
        autoComplete="new-password"
        onChange={(e) => onChange(e.target.value)}
      />
    </Wrapper>
  )
}

export function Area({
  value,
  onChange,
  rows = 3,
  placeholder,
  ...base
}: BaseProps & {
  value: string | undefined
  onChange: (v: string) => void
  rows?: number
  placeholder?: string
}) {
  return (
    <Wrapper {...base}>
      <textarea
        className={inputClass + ' font-mono text-xs'}
        rows={rows}
        value={value ?? ''}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
      />
    </Wrapper>
  )
}

/**
 * ScriptArea is Area for anything that holds source code.
 *
 * The builder's six script slots were plain textareas. The syntax-highlighted editor existed the whole time and was used in exactly
 * one place - the workbench - which had it backwards: the workbench is where somebody pastes a script to find out whether it
 * survives, and the builder is where they write the one that will actually run. The good editor was in the tourist spot and the bare
 * textarea was in the workshop.
 *
 * It is also the only comparison with Mirth's desktop client that Perfuse was losing. That editor highlights JavaScript, so a person
 * migrating met a plainer tool than the one they left.
 *
 * Using the real editor brings three things a textarea cannot: highlighting that follows the chosen language, a marker on the line an
 * error is actually on, and compilation as you type rather than at deploy - which is the promise the whole product makes.
 */
export function ScriptArea({
  value,
  onChange,
  kind,
  language,
  minHeight = '9rem',
  ...base
}: BaseProps & {
  value: string | undefined
  onChange: (v: string) => void
  kind: ScriptKind
  language?: 'javascript' | 'lua'
  minHeight?: string
}) {
  return (
    <Wrapper {...base}>
      {/*
        The label cannot be associated with this the way it is with a textarea, because CodeMirror renders a div and a label may
        only point at a real control. So the editor is named directly instead, from the same string - six editors on this page
        would otherwise announce themselves as six anonymous edit boxes.
      */}
      <ScriptEditor
        value={value ?? ''}
        onChange={onChange}
        kind={kind}
        language={language}
        minHeight={minHeight}
        ariaLabel={base.label}
      />
    </Wrapper>
  )
}

export function Num({
  value,
  onChange,
  placeholder,
  ...base
}: BaseProps & {
  value: number | undefined
  onChange: (v: number | undefined) => void
  placeholder?: string
}) {
  return (
    <Wrapper {...base}>
      <input
        type="number"
        className={inputClass}
        value={value ?? ''}
        placeholder={placeholder}
        onChange={(e) => {
          const raw = e.target.value
          // Cleared means "not set", which is different from zero. Zero is a real value
          // for several of these settings and usually means "no limit".
          onChange(raw === '' ? undefined : Number(raw))
        }}
      />
    </Wrapper>
  )
}

export function Choose<T extends string>({
  value,
  onChange,
  options,
  ...base
}: BaseProps & {
  value: T | undefined
  onChange: (v: T) => void
  options: { value: T; label: string }[]
}) {
  return (
    <Wrapper {...base}>
      <select
        className={inputClass}
        value={value ?? ''}
        onChange={(e) => onChange(e.target.value as T)}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </Wrapper>
  )
}

/**
 * Check is a checkbox with a label beside it.
 *
 * The hint sits outside the label and is attached with aria-describedby. It used to be inside, which
 * made the whole explanation part of the checkbox's accessible name - so the control was announced as
 * "Put documents back when a message is read On unless you have a reason. Off, the message arrives with
 * a reference where the document was, and whatever reads it has to fetch each one", where a name belongs.
 *
 * The label still wraps the input, so the whole label text remains a click target. That is worth keeping:
 * a checkbox is small and clicking its words is how most people hit it.
 */
export function Check({
  value,
  onChange,
  label,
  hint,
}: {
  value: boolean | undefined
  onChange: (v: boolean) => void
  label: string
  hint?: ReactNode
}) {
  const id = useId()
  return (
    <div>
      <label className="flex cursor-pointer items-start gap-2">
        <input
          type="checkbox"
          className="mt-0.5 h-4 w-4 rounded border-slate-600 bg-slate-900 accent-sky-600"
          checked={!!value}
          aria-describedby={hint ? `${id}-hint` : undefined}
          onChange={(e) => onChange(e.target.checked)}
        />
        <span className="block text-xs font-medium text-slate-300">{label}</span>
      </label>
      {/* Still visible, and indented to line up under the label text. The hints are the argument of this
          whole screen, so moving one out of the label must not hide it. */}
      {hint && (
        <span id={`${id}-hint`} className="mt-0.5 block pl-6 text-xs leading-relaxed text-slate-500">
          {hint}
        </span>
      )}
    </div>
  )
}

/** A group of related fields with a heading, collapsed until wanted. */
//
// Collapsed by default because the number of settings on a channel is genuinely large, and a
// screen showing all of them at once is the reason the existing tools feel like they need an
// expert. The common path is the fields outside these groups; everything in one is something
// most channels never set.
export function Advanced({
  title,
  hint,
  open,
  onToggle,
  children,
}: {
  title: string
  hint?: string
  open: boolean
  onToggle: () => void
  children: ReactNode
}) {
  return (
    <div className="rounded-lg border border-slate-800">
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center justify-between px-3 py-2 text-left"
      >
        <span>
          <span className="text-xs font-medium text-slate-300">{title}</span>
          {hint && <span className="ml-2 text-xs text-slate-500">{hint}</span>}
        </span>
        <span className="text-slate-500">{open ? '−' : '+'}</span>
      </button>
      {open && <div className="space-y-3 border-t border-slate-800 px-3 py-3">{children}</div>}
    </div>
  )
}

/** Two fields side by side on a wide screen, stacked on a narrow one. */
export function Pair({ children }: { children: ReactNode }) {
  return <div className="grid gap-3 sm:grid-cols-2">{children}</div>
}
