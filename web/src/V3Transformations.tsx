import {
  NULL_FLAVOURS,
  newV3Step,
  type ChannelDraft,
  type DraftV3Step,
  type V3StepKind,
} from './model'
import { Area, Choose, Pair, Text } from './builderFields'
import { Section } from './ui'

/** The v3 transformation editor.
 *
 * Its own component rather than a mode on the v2 step editor, because the vocabularies are not the
 * same and pretending otherwise would mean either hiding nullflavor — the one construct v2 cannot
 * express — or offering pad and date, which have no meaning in XML.
 */

/** V3_STEP_KINDS is what the form offers, in the order it offers them.
 *
 * Ordered by how often it is wanted rather than alphabetically, and the three removals are adjacent
 * on purpose so somebody choosing between them sees all three and their differences at once.
 */
const V3_STEP_KINDS: { value: V3StepKind; label: string; hint: string }[] = [
  {
    value: 'set',
    label: 'Set a value',
    hint: 'Writes a value, creating the element if the path is anchored from the document root.',
  },
  {
    value: 'copy',
    label: 'Copy a value',
    hint: 'Takes the value at one path and writes it to another. Refused if the source is absent, ' +
      'rather than overwriting the destination with nothing.',
  },
  {
    value: 'map',
    label: 'Translate through a table',
    hint: 'Looks the value up in a shared table. Decide below what happens when it is not there.',
  },
  {
    value: 'clear',
    label: 'Empty the value',
    hint: 'Leaves the element in place carrying nothing, and states no reason why.',
  },
  {
    value: 'nullflavor',
    label: 'Say why there is no value',
    hint: 'Removes the value and records a reason — masked, asked and unknown, not applicable. ' +
      'This is what v3 has and v2 does not.',
  },
  {
    value: 'remove',
    label: 'Remove the element',
    hint: 'Deletes it outright, meaning the question does not apply at all.',
  },
  {
    value: 'replace',
    label: 'Rewrite with a pattern',
    hint: 'A regular-expression substitution on the value.',
  },
  { value: 'trim', label: 'Trim whitespace', hint: 'Removes spaces from either end of the value.' },
  { value: 'case', label: 'Change case', hint: 'Converts the value to upper or lower case.' },
]

/** hintFor finds the explanation for a kind. */
export function hintFor(kind: V3StepKind): string {
  return V3_STEP_KINDS.find((k) => k.value === kind)?.hint ?? ''
}

/** pathHint is the advice shown under every path field.
 *
 * Extracted so it reads the same on all nine step kinds. Somebody who learns the rule once should not
 * have to rediscover it on the step where it was worded differently.
 */
export const pathHint =
  'v3 keeps most values in attributes, so a path usually ends in @value or @code. ' +
  'A path starting // finds the element wherever it sits, but can only change something that ' +
  'already exists — creating a value needs a path written from the document root.'

/** describeV3Step says what a step will do, in the same words the specification uses.
 *
 * Shown in the form because the reader of a channel is often not its author, and because these steps
 * change patient data. A pure function so it can be tested without rendering anything.
 */
export function describeV3Step(step: DraftV3Step): string {
  const flavour = NULL_FLAVOURS.find((f) => f.value === step.reason)?.label ?? step.reason

  let what: string
  switch (step.kind) {
    case 'set':
      what = `Sets ${step.path || '…'} to "${step.value}"`
      break
    case 'copy':
      what = `Copies ${step.from || '…'} to ${step.to || '…'}`
      break
    case 'clear':
      what = `Empties ${step.path || '…'}, without stating why`
      break
    case 'nullflavor':
      what = `Removes the value at ${step.path || '…'} and records the reason as ${flavour}`
      break
    case 'remove':
      what = `Removes ${step.path || '…'} entirely, meaning it does not apply`
      break
    case 'map': {
      const missing =
        step.onMissing === 'fail'
          ? 'refuses the message if a value is not in the table'
          : step.onMissing === 'clear'
            ? 'empties an untranslated value'
            : 'leaves an untranslated value unchanged'
      what = `Translates ${step.path || '…'} through the ${step.table || '…'} table, and ${missing}`
      break
    }
    case 'replace':
      what = `Rewrites ${step.path || '…'}, replacing ${step.from || '…'} with "${step.to}"`
      break
    case 'trim':
      what = `Removes surrounding whitespace from ${step.path || '…'}`
      break
    case 'case':
      what = `Converts ${step.path || '…'} to ${step.caseTo} case`
      break
  }

  if (step.when.trim()) what += `, but only when ${step.when.trim()}`

  return what
}

/** V3StepFields renders the inputs for whichever kind is selected. */
function V3StepFields({
  step,
  update,
}: {
  step: DraftV3Step
  update: (patch: Partial<DraftV3Step>) => void
}) {
  switch (step.kind) {
    case 'set':
      return (
        <Pair>
          <Text
            label="Path"
            value={step.path}
            onChange={(path) => update({ path })}
            placeholder="//birthTime@value"
            hint={pathHint}
          />
          <Text label="Value" value={step.value} onChange={(value) => update({ value })} />
        </Pair>
      )

    case 'copy':
      return (
        <Pair>
          <Text
            label="From"
            value={step.from}
            onChange={(from) => update({ from })}
            placeholder="//patient/id(1)@extension"
            hint={pathHint}
          />
          <Text
            label="To"
            value={step.to}
            onChange={(to) => update({ to })}
            placeholder="//patient/id(2)@extension"
            hint="If the source has no value the step fails rather than writing an empty one, which would overwrite a good value here with nothing."
          />
        </Pair>
      )

    case 'clear':
    case 'trim':
    case 'remove':
      return (
        <Text
          label="Path"
          value={step.path}
          onChange={(path) => update({ path })}
          placeholder="//birthTime@value"
          hint={pathHint}
        />
      )

    case 'nullflavor':
      return (
        <div className="space-y-3">
          <Text
            label="Path"
            value={step.path}
            onChange={(path) => update({ path })}
            placeholder="//birthTime"
            hint={pathHint}
          />
          <Choose
            label="Reason there is no value"
            value={step.reason}
            onChange={(reason) => update({ reason })}
            options={NULL_FLAVOURS}
            hint={
              'These are not interchangeable. A receiving system does different things with each: ' +
              'asked-and-unknown says the gap has been chased already, masked says somebody ' +
              'withheld it deliberately and filling it from another source undoes that decision.'
            }
          />
        </div>
      )

    case 'map':
      return (
        <div className="space-y-3">
          <Text
            label="Path"
            value={step.path}
            onChange={(path) => update({ path })}
            placeholder="//administrativeGenderCode@code"
            hint={pathHint}
          />
          <Pair>
            <Text
              label="Table name"
              value={step.table}
              onChange={(table) => update({ table })}
              placeholder="gender"
              hint="A shared table from the Tables tab. The channel refuses to start if no table of this name is loaded."
            />
            <Choose
              label="When a value is not in the table"
              value={step.onMissing}
              onChange={(onMissing) => update({ onMissing: onMissing as DraftV3Step['onMissing'] })}
              options={[
                { value: 'keep', label: 'Leave it unchanged' },
                { value: 'clear', label: 'Empty it' },
                { value: 'fail', label: 'Refuse the message' },
              ]}
              hint={
                'Worth choosing deliberately. A code that failed to translate and travelled on ' +
                'unchanged is the usual failure of a mapping table: the receiving system gets a ' +
                'code from the sender’s vocabulary and may recognise it as something else.'
              }
            />
          </Pair>
        </div>
      )

    case 'replace':
      return (
        <div className="space-y-3">
          <Text
            label="Path"
            value={step.path}
            onChange={(path) => update({ path })}
            placeholder="//patient/id(1)@extension"
            hint={pathHint}
          />
          <Pair>
            <Text
              label="Find (regular expression)"
              value={step.from}
              onChange={(from) => update({ from })}
              placeholder="^MRN"
              hint="Checked when the channel loads, so a pattern that does not compile stops it starting rather than failing on a message."
            />
            <Text label="Replace with" value={step.to} onChange={(to) => update({ to })} />
          </Pair>
        </div>
      )

    case 'case':
      return (
        <Pair>
          <Text
            label="Path"
            value={step.path}
            onChange={(path) => update({ path })}
            placeholder="//patientPerson/name/family"
            hint={pathHint}
          />
          <Choose
            label="Convert to"
            value={step.caseTo}
            onChange={(caseTo) => update({ caseTo: caseTo as DraftV3Step['caseTo'] })}
            options={[
              { value: 'upper', label: 'UPPER CASE' },
              { value: 'lower', label: 'lower case' },
            ]}
          />
        </Pair>
      )
  }
}

/** V3Transformations is the whole editor. */
export function V3Transformations({
  draft,
  set,
}: {
  draft: ChannelDraft
  set: <K extends keyof ChannelDraft>(key: K, value: ChannelDraft[K]) => void
}) {
  const steps = draft.v3Steps

  // A prescription is XML addressed by the same paths, so this editor serves SCRIPT channels too — the server
  // borrows the same path grammar for both. One action does not carry over: nullflavor states why a v3 value
  // is absent, and a prescription has no equivalent, so the server refuses it on a SCRIPT channel.
  //
  // Withheld from the list rather than offered and rejected. A control that produces a channel file which will
  // not load is worse than a control that is not there, because the person using it has no way to tell that
  // the form was wrong rather than their intent.
  const actions =
    draft.dataType === 'script'
      ? V3_STEP_KINDS.filter((k) => k.value !== 'nullflavor')
      : V3_STEP_KINDS

  const update = (id: string, patch: Partial<DraftV3Step>) =>
    set(
      'v3Steps',
      steps.map((s) => (s.id === id ? { ...s, ...patch } : s)),
    )

  const move = (index: number, by: number) => {
    const target = index + by
    if (target < 0 || target >= steps.length) return
    const next = [...steps]
    const a = next[index]
    const b = next[target]
    // Indexed assignment rather than a destructuring swap, because the compiler cannot know an index
    // inside a bounds check is populated and the swap form types both sides as possibly undefined.
    if (!a || !b) return
    next[index] = b
    next[target] = a
    set('v3Steps', next)
  }

  return (
    <Section
      title="Change the message"
      description="Steps run in order, top to bottom, after the filter and before anything is sent."
    >
      <div className="space-y-3">
        {steps.length === 0 && (
          <p className="rounded border border-slate-800 bg-slate-900/40 px-3 py-2 text-sm text-slate-400">
            No steps, so messages are forwarded exactly as they arrive.
          </p>
        )}

        {steps.map((step, i) => (
          <div key={step.id} className="rounded-lg border border-slate-800 bg-slate-900/30 p-3">
            <div className="mb-2 flex items-center justify-between gap-2">
              <span className="text-xs font-medium tracking-wide text-slate-400">
                Step {i + 1} of {steps.length}
              </span>
              <div className="flex items-center gap-1">
                <button
                  type="button"
                  onClick={() => move(i, -1)}
                  disabled={i === 0}
                  aria-label={`Move step ${i + 1} earlier`}
                  className="rounded px-2 py-1 text-xs text-slate-300 hover:bg-slate-800 disabled:opacity-30"
                >
                  ↑
                </button>
                <button
                  type="button"
                  onClick={() => move(i, 1)}
                  disabled={i === steps.length - 1}
                  aria-label={`Move step ${i + 1} later`}
                  className="rounded px-2 py-1 text-xs text-slate-300 hover:bg-slate-800 disabled:opacity-30"
                >
                  ↓
                </button>
                <button
                  type="button"
                  onClick={() =>
                    set(
                      'v3Steps',
                      steps.filter((s) => s.id !== step.id),
                    )
                  }
                  aria-label={`Remove step ${i + 1}`}
                  className="rounded px-2 py-1 text-xs text-rose-300 hover:bg-rose-950/50"
                >
                  Remove
                </button>
              </div>
            </div>

            <div className="space-y-3">
              <Choose
                label="What this step does"
                value={step.kind}
                onChange={(kind) => update(step.id, { kind: kind as V3StepKind })}
                options={actions}
                hint={hintFor(step.kind)}
              />

              <V3StepFields step={step} update={(patch) => update(step.id, patch)} />

              <Text
                label="Only when (optional)"
                value={step.when}
                onChange={(when) => update(step.id, { when })}
                placeholder={'//administrativeGenderCode@code == "F"'}
                hint="A v3 filter expression. Leave empty to run on every message."
              />

              <Area
                label="Why this step exists (optional)"
                value={step.description}
                onChange={(description) => update(step.id, { description })}
                rows={2}
                hint="Shown in the channel's specification. Worth writing: the next person to read this is deciding whether it is still needed."
              />

              {/* The plain-English summary, so somebody reviewing a channel does not have to
                  assemble the meaning from the fields above. */}
              <p className="rounded border border-slate-800 bg-slate-950/60 px-2 py-1.5 text-xs leading-relaxed text-slate-300">
                {describeV3Step(step)}
              </p>
            </div>
          </div>
        ))}

        <div className="flex flex-wrap gap-2">
          {actions.map((k) => (
            <button
              key={k.value}
              type="button"
              onClick={() => set('v3Steps', [...steps, newV3Step(k.value)])}
              className="rounded-lg border border-slate-700 bg-slate-900 px-2.5 py-1.5 text-xs text-slate-200 hover:border-sky-600 hover:bg-slate-800"
            >
              + {k.label}
            </button>
          ))}
        </div>

        {/* Said once, here, because it is the thing that most surprises somebody coming from v2. */}
        <p className="rounded border border-slate-800 bg-slate-900/40 px-3 py-2 text-xs leading-relaxed text-slate-400">
          Emptying a value, saying why it is missing, and removing the element are three different
          statements in v3, which is why they are three separate steps. A receiving system acts on the
          difference: a withheld address is a decision somebody made, and filling that gap from
          another source undoes it.
        </p>
      </div>
    </Section>
  )
}
