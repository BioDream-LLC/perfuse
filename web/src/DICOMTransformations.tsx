import { Area, Check, Choose, Text } from './builderFields'
import { Section } from './ui'
import {
  type ChannelDraft,
  type DICOMStepKind,
  type DraftDICOMStep,
  newDICOMStep,
} from './model'

/**
 * The imaging transformation editor.
 *
 * # Why these are named actions and not a path writer
 *
 * Every other format gets a path and a value. DICOM deliberately does not, and the reason is the pixel
 * data: an object is binary, a general tag writer can set any tag to any bytes, and a mistake does not
 * produce a rejected message — it produces an image that opens and is wrong. A radiologist reading that
 * study has no way to tell.
 *
 * So the four things sites actually need are offered as named actions with bounded effects. Each knows
 * which tags it touches, and none can reach the pixels. That is a smaller feature than the other formats
 * have, on purpose, and the hint text says so rather than leaving somebody hunting for the path field.
 */

const DICOM_STEP_KINDS: { value: DICOMStepKind; label: string; hint: string }[] = [
  {
    value: 'deidentify',
    label: 'Remove the patient',
    hint:
      'Replaces the identifying tags — name, identifier, address, referring physician — and leaves the ' +
      'imaging intact. This is the one to reach for when sending studies outside the hospital.',
  },
  {
    value: 'stripPrivate',
    label: 'Strip private tags',
    hint:
      'Removes vendor-private tags, which often carry identifiers nobody documented. Name any that the ' +
      'receiving system needs, and they are left alone.',
  },
  {
    value: 'setAeTitle',
    label: 'Rewrite the AE titles',
    hint:
      'Changes the calling and called application entity titles. Needed when a receiver accepts only ' +
      'titles it was configured with, which most archives do.',
  },
  {
    value: 'setInstitution',
    label: 'Set the institution',
    hint:
      'Rewrites where the study says it was performed. Usually paired with removing the patient, since ' +
      'the institution is identifying on its own for a small site.',
  },
]

function hintFor(kind: DICOMStepKind): string {
  return DICOM_STEP_KINDS.find((k) => k.value === kind)?.hint ?? ''
}

/** DICOMTransformations is the whole editor. */
export function DICOMTransformations({
  draft,
  set,
}: {
  draft: ChannelDraft
  set: <K extends keyof ChannelDraft>(key: K, value: ChannelDraft[K]) => void
}) {
  const steps = draft.dicomSteps

  const update = (id: string, patch: Partial<DraftDICOMStep>) =>
    set(
      'dicomSteps',
      steps.map((s) => (s.id === id ? { ...s, ...patch } : s)),
    )

  const move = (index: number, by: number) => {
    const target = index + by
    if (target < 0 || target >= steps.length) return
    const next = [...steps]
    const a = next[index]
    const b = next[target]
    if (!a || !b) return
    next[index] = b
    next[target] = a
    set('dicomSteps', next)
  }

  return (
    <Section
      title="Change the object"
      description="Steps run in order, top to bottom, before anything is sent. Only these four actions are offered: a general tag writer could corrupt the image itself."
    >
      <div className="space-y-3">
        {steps.length === 0 && (
          <p className="rounded border border-slate-800 bg-slate-900/40 px-3 py-2 text-sm text-slate-400">
            No steps, so objects are forwarded exactly as they arrive.
          </p>
        )}

        {steps.map((step, i) => (
          <div
            key={step.id}
            className="rounded-lg border border-slate-800 bg-slate-900/40 p-3 space-y-3"
          >
            <div className="flex items-center justify-between gap-2">
              <span className="text-xs font-medium uppercase tracking-wide text-slate-400">
                Step {i + 1}
              </span>
              <div className="flex gap-1">
                <button
                  type="button"
                  aria-label={`Move step ${i + 1} up`}
                  onClick={() => move(i, -1)}
                  className="rounded border border-slate-700 px-2 py-1 text-xs text-slate-300 hover:border-sky-600"
                >
                  ↑
                </button>
                <button
                  type="button"
                  aria-label={`Move step ${i + 1} down`}
                  onClick={() => move(i, 1)}
                  className="rounded border border-slate-700 px-2 py-1 text-xs text-slate-300 hover:border-sky-600"
                >
                  ↓
                </button>
                <button
                  type="button"
                  aria-label={`Remove step ${i + 1}`}
                  onClick={() =>
                    set(
                      'dicomSteps',
                      steps.filter((s) => s.id !== step.id),
                    )
                  }
                  className="rounded border border-slate-700 px-2 py-1 text-xs text-rose-300 hover:border-rose-600"
                >
                  Remove
                </button>
              </div>
            </div>

            <Choose
              label="What this step does"
              value={step.kind}
              onChange={(kind) => update(step.id, { kind: kind as DICOMStepKind })}
              options={DICOM_STEP_KINDS}
              hint={hintFor(step.kind)}
            />

            {step.kind === 'deidentify' && (
              <>
                <Text
                  label="Replacement patient identifier"
                  value={step.patientId}
                  onChange={(v) => update(step.id, { patientId: v })}
                  placeholder="Leave empty for a fixed anonymous value"
                  hint="Set this when the receiving system needs a stable key to group a patient's studies."
                />
                <Text
                  label="Replacement patient name"
                  value={step.patientName}
                  onChange={(v) => update(step.id, { patientName: v })}
                  placeholder="Leave empty for a fixed anonymous value"
                />
                <Check
                  label="Keep study and birth dates"
                  value={step.keepDates}
                  onChange={(v) => update(step.id, { keepDates: v })}
                  hint="Off by default. On, ages and intervals stay comparable, which research often needs — and which is a disclosure decision rather than a technical one."
                />
              </>
            )}

            {step.kind === 'stripPrivate' && (
              <Area
                label="Private tags to keep"
                value={step.keep}
                onChange={(v) => update(step.id, { keep: v })}
                placeholder={'0009,0010\n0029,1010'}
                rows={3}
                hint="One tag per line, written group,element — the comma belongs inside a tag, so it cannot separate them. Leave empty to remove all of them. Name any the receiving system reads, or it will lose information it was relying on."
              />
            )}

            {step.kind === 'setAeTitle' && (
              <>
                <Text
                  label="Calling AE title"
                  value={step.calling}
                  onChange={(v) => update(step.id, { calling: v })}
                  hint="What this channel presents itself as."
                />
                <Text
                  label="Called AE title"
                  value={step.called}
                  onChange={(v) => update(step.id, { called: v })}
                  hint="What the object says it was sent to."
                />
              </>
            )}

            {step.kind === 'setInstitution' && (
              <>
                <Text
                  label="Institution name"
                  value={step.institutionName}
                  onChange={(v) => update(step.id, { institutionName: v })}
                />
                <Text
                  label="Institution address"
                  value={step.address}
                  onChange={(v) => update(step.id, { address: v })}
                />
                <Text
                  label="Department"
                  value={step.department}
                  onChange={(v) => update(step.id, { department: v })}
                />
              </>
            )}

            <Text
              label="Why this step exists"
              value={step.description}
              onChange={(v) => update(step.id, { description: v })}
              placeholder="Optional"
              hint="Worth writing. The next person reading this channel is trying to work out whether the step is still needed."
            />
          </div>
        ))}

        <div className="flex flex-wrap gap-2">
          {DICOM_STEP_KINDS.map((k) => (
            <button
              key={k.value}
              type="button"
              onClick={() => set('dicomSteps', [...steps, newDICOMStep(k.value)])}
              className="rounded-lg border border-slate-700 bg-slate-900 px-2.5 py-1.5 text-xs text-slate-200 hover:border-sky-600 hover:bg-slate-800"
            >
              + {k.label}
            </button>
          ))}
        </div>
      </div>
    </Section>
  )
}
