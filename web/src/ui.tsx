import { CodeArea } from './CodeArea'
import { sectionIcons, type IconProps } from './Icons'
import { cloneElement, isValidElement, useEffect, useId, useRef } from 'react'
import type { ReactElement, ReactNode } from 'react'
import type { UiError } from './store'

/** Small shared pieces. Kept in one file because each is a handful of lines and
 *  splitting them across twenty files makes the code harder to read, not easier. */

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="flex items-center gap-3 text-sm text-slate-400">
      <span className="size-4 animate-spin rounded-full border-2 border-slate-700 border-t-sky-500" />
      {label}
    </div>
  )
}

/**
 * ErrorBox shows the server's message and, when it is a validation failure, every
 * individual problem. Showing all of them means one pass to fix a channel rather
 * than one error per attempt.
 */
export function ErrorBox({ error, onDismiss }: { error: UiError; onDismiss?: () => void }) {
  return (
    // role=alert so the message is announced, not only coloured.
    //
    // These boxes appear after an action - a save that was refused, a message that would not parse - which
    // is precisely when a screen reader user has no reason to go looking. Without a live region the rose
    // border is the entire notification, and it says nothing to anybody not looking at it. Assertive rather
    // than polite because the person has just asked for something and it did not happen.
    <div role="alert" className="rounded-lg border border-rose-900/60 bg-rose-950/40 p-3 text-sm">
      <div className="flex items-start justify-between gap-3">
        <p className="font-medium text-rose-200">{error.message}</p>
        {onDismiss && (
          <button
            onClick={onDismiss}
            className="text-rose-400 hover:text-rose-200"
            aria-label="Dismiss"
          >
            ✕
          </button>
        )}
      </div>
      {error.problems.length > 0 && (
        <ul className="mt-2 space-y-1 text-rose-300/90">
          {error.problems.map((p, i) => (
            <li key={i} className="flex gap-2">
              <span aria-hidden className="text-rose-500">
                •
              </span>
              <span>{p}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/**
 * Field labels a form control.
 *
 * The label is associated with its control automatically. It used to be associated only when a caller
 * passed htmlFor, and almost nobody did - so most labels in the product named nothing. A screen reader
 * then announces an unlabelled input, clicking the text does not focus the field, and the whole channel
 * builder is a column of anonymous boxes to anyone not using a mouse and eyes.
 *
 * Done by cloning a single child to give it the generated id, rather than by wrapping the control in the
 * label. Wrapping would associate implicitly with no cloning at all, but a label may only contain one
 * control, and several fields here hold a slider paired with a number box. Cloning one child and leaving
 * anything more complicated alone is correct in both cases: the common field gets its association, and a
 * compound field is no worse off than before.
 */
/** labelable are the elements a label's `for` may legally reference.
 *
 * Checked, because pointing `for` at a div is worse than pointing it nowhere: it looks associated to
 * anyone reading the markup and resolves to something a screen reader will not treat as a control. A
 * Field wrapping a composite gets no association and is honest about it. */
const labelable = new Set(['input', 'select', 'textarea'])

export function Field({
  label,
  hint,
  children,
  htmlFor,
}: {
  label: string
  hint?: ReactNode
  children: ReactNode
  htmlFor?: string
}) {
  const generated = useId()
  const element = isValidElement(children) ? (children as ReactElement<{ id?: string }>) : undefined
  const canAssociate = (typeof element?.type === 'string' && labelable.has(element.type)) || element?.type === CodeArea

  // CodeArea counts as labelable. It is a component rather than a tag, so the string test above rejected it - and because a label
  // pointing at nothing looks associated and is not, converting the code boxes to CodeArea quietly unlabelled every one of them.
  // Two browser tests found it by name and stopped finding it. What makes this safe rather than a fudge is that CodeArea renders a
  // real textarea and forwards the id to it, so the association is genuine.
  //
  // A caller's own id wins, then an explicit htmlFor, then the generated one.
  const existing = element?.props.id
  const id = existing ?? htmlFor ?? (canAssociate ? generated : undefined)

  const control =
    canAssociate && !existing && !htmlFor
      ? cloneElement(element as ReactElement<{ id?: string }>, { id })
      : children

  return (
    <div>
      <label className="label" htmlFor={id}>
        {label}
      </label>
      {control}
      {hint && <p className="mt-1.5 text-xs leading-relaxed text-slate-500">{hint}</p>}
    </div>
  )
}

/**
 * Section is a titled card, and now a titled card with an icon.
 *
 * Seventy-nine icons existed and four files used them, all of them navigation. Every section heading in the application was plain
 * text, so the icons a person saw while choosing where to go disappeared the moment they arrived.
 *
 * The icon is resolved from the title rather than passed at each of the fifty-three call sites, for two reasons. It gives every
 * existing section one without touching thirty-three files, and it makes the mapping a single list that a test can check - a heading
 * added later with no icon fails that test instead of quietly being the one plain heading in the app. An explicit icon prop wins
 * where the title is computed and there is nothing to look up.
 */
export function Section({
  title,
  description,
  children,
  actions,
  icon,
}: {
  title: string
  description?: ReactNode
  children: ReactNode
  actions?: ReactNode
  /** Overrides the icon looked up from the title. Needed where the title is built at run time. */
  icon?: (props: IconProps) => ReactNode
}) {
  const Icon = icon ?? sectionIcons[title]

  return (
    <section className="card p-5">
      <div className="mb-4 flex items-start justify-between gap-4">
        <div className="flex items-start gap-2.5">
          {/*
            Decorative: the heading beside it already says what this is, so announcing the icon as well would make a screen
            reader read every section twice. aria-hidden rather than a label is the correct choice for an icon that duplicates
            adjacent text.
          */}
          {Icon && (
            <span className="mt-0.5 shrink-0 text-slate-500" aria-hidden="true">
              <Icon size={15} />
            </span>
          )}
          <div>
          <h2 className="text-sm font-semibold tracking-wide text-slate-200 uppercase">{title}</h2>
          {description && (
            <p className="mt-1 max-w-2xl text-xs leading-relaxed text-slate-500">{description}</p>
          )}
          </div>
        </div>
        {actions}
      </div>
      {children}
    </section>
  )
}

export function Toggle({
  checked,
  onChange,
  label,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  label: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      onClick={() => onChange(!checked)}
      className="inline-flex items-center gap-3 text-sm text-slate-300"
    >
      <span
        className={`relative h-5 w-9 rounded-full transition ${
          checked ? 'bg-sky-600' : 'bg-slate-700'
        }`}
      >
        <span
          className={`absolute top-0.5 left-0.5 size-4 rounded-full bg-white transition-transform ${
            checked ? 'translate-x-4' : 'translate-x-0'
          }`}
        />
      </span>
      {label}
    </button>
  )
}

/** StatusDot is the at-a-glance enabled indicator used in the channel list. */
export function StatusDot({ on }: { on: boolean }) {
  return (
    <span className="inline-flex items-center gap-2">
      <span className={`size-2 rounded-full ${on ? 'bg-emerald-400' : 'bg-slate-600'}`} />
      <span className={on ? 'text-emerald-300' : 'text-slate-500'}>{on ? 'enabled' : 'disabled'}</span>
    </span>
  )
}

export function RoleBadge({ role }: { role: string }) {
  const colours: Record<string, string> = {
    admin: 'bg-amber-500/15 text-amber-300 border border-amber-500/30',
    editor: 'bg-sky-500/15 text-sky-300 border border-sky-500/30',
    viewer: 'bg-slate-500/15 text-slate-300 border border-slate-500/30',
  }
  return <span className={`badge ${colours[role] ?? colours.viewer}`}>{role}</span>
}

/** Confirm is a deliberate speed bump in front of anything destructive. */
export function Confirm({
  open,
  title,
  body,
  confirmLabel,
  onConfirm,
  onCancel,
}: {
  open: boolean
  title: string
  body: ReactNode
  confirmLabel: string
  onConfirm: () => void
  onCancel: () => void
}) {
  const titleId = useId()
  const bodyId = useId()
  const panel = useRef<HTMLDivElement | null>(null)
  const cancelRef = useRef<HTMLButtonElement | null>(null)

  // Escape cancels, and focus starts on Cancel rather than on the destructive button.
  //
  // This is the last thing between somebody and stopping a live interface or deleting an account, and it was
  // a plain div: no dialog role, so a screen reader was told nothing had happened; no focus move, so the next
  // Tab went to whatever was behind it; and no Escape, so the only way out was finding Cancel by sight.
  //
  // Focus goes to Cancel deliberately. A confirmation whose dangerous button is focused turns a stray Return
  // keypress into the action it was meant to guard against.
  useEffect(() => {
    if (!open) return

    cancelRef.current?.focus()

    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onCancel()
        return
      }

      // Keep Tab inside the dialog. Without this, tabbing leaves the modal and lands on the page underneath,
      // which is still there and still looks operable.
      if (e.key !== 'Tab') return
      const focusable = panel.current?.querySelectorAll<HTMLElement>(
        'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
      )
      if (!focusable || focusable.length === 0) return
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (first === undefined || last === undefined) return

      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
    }

    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [open, onCancel])

  if (!open) return null
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
      // Clicking away cancels, which is what the backdrop looks like it should do.
      onClick={(e) => {
        if (e.target === e.currentTarget) onCancel()
      }}
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={bodyId}
        className="card w-full max-w-md p-5"
      >
        <h3 id={titleId} className="text-base font-semibold text-slate-100">
          {title}
        </h3>
        <div id={bodyId} className="mt-2 text-sm leading-relaxed text-slate-400">
          {body}
        </div>
        <div className="mt-5 flex justify-end gap-2">
          <button ref={cancelRef} className="btn-ghost" onClick={onCancel}>
            Cancel
          </button>
          <button className="btn-danger" onClick={onConfirm}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
