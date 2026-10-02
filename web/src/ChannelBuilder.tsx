import { SyntaxBlock } from './SyntaxHighlight'
import { CodeArea } from './CodeArea'
import { useEffect, useRef, useState } from 'react'
import { FromSamplePanel } from './FromSamplePanel'
import { RawFilter } from './RawFilter'
import { wireToDraft } from './wireToDraft'
import { useDispatch, useSelector } from 'react-redux'
import { api } from './api'
import type { BuildResult, ChannelSummary, DictSegment } from './api'
import {
  draftToWire,
  emptyDraft,
  newStep,
  newDestination,
  type ChannelDraft,
  type Destination,
  destinationLabels,
  destinationHints,
  rulesToExpression,
} from './model'
import { RuleBuilder } from './RuleBuilder'
import { ReplayPanel } from './ReplayPanel'
import { Area, Check, Num } from './builderFields'
import { FormatSection, SourceSection } from './BuilderSourceSection'
import { DICOMTransformations } from './DICOMTransformations'
import { V3Transformations } from './V3Transformations'
import { StepCard } from './BuilderSteps'
import { BuilderScripts } from './BuilderScripts'
import { RECIPES } from './builderRecipes'
import { clearSaveError, saveChannel, type AppDispatch, type RootState, type UiError } from './store'
import { Confirm, ErrorBox, Field, Section, Spinner, Toggle } from './ui'

/**
 * The channel builder.
 *
 * Two things make this usable by someone who does not know HL7 or YAML. The form
 * is arranged as the message's journey — where it arrives, what to keep, where it
 * goes — and the generated file is shown alongside, live, so nothing about the
 * result is hidden. The YAML panel is not a separate editing mode; it is the
 * proof of what the form just built.
 */
export function ChannelBuilder({
  editing,
  onClose,
}: {
  editing: ChannelSummary | null
  onClose: () => void
}) {
  const dispatch = useDispatch<AppDispatch>()
  // Groups already in use, offered as suggestions. The commonest mistake is inventing "Lab" alongside
  // an existing "Lab interfaces" and ending up with two groups that mean the same thing.
  const existingGroups = useSelector((s: RootState) => {
    const seen = new Set<string>()
    for (const c of s.channels.items) {
      const g = (c.group ?? '').trim()
      if (g !== '') seen.add(g)
    }
    return [...seen].sort((a, b) => a.localeCompare(b))
  })

  const saving = useSelector((s: RootState) => s.channels.saving)
  const saveError = useSelector((s: RootState) => s.channels.saveError)

  const [draft, setDraft] = useState<ChannelDraft>(emptyDraft)
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState<UiError | null>(null)
  const [serverCheck, setServerCheck] = useState<{ ok: boolean; note: string } | null>(null)
  const [startError, setStartError] = useState<string | null>(null)

  // When rawYaml is set, the file is being edited as text. That used to be the only way to edit an
  // existing channel; now it is the fallback for files the form cannot represent, and the escape
  // hatch for anyone who would rather see the file.
  const [rawYaml, setRawYaml] = useState<string | null>(null)

  // The file exactly as it is on disk, kept so the text editor can be opened at any moment without
  // another fetch, and so switching to it shows the original rather than a regenerated version.
  const [fileOnDisk, setFileOnDisk] = useState<string | null>(null)

  // Why the form is not on offer, when it is not. Shown rather than hidden: a reader who is dropped
  // into a text editor with no explanation assumes the form is broken.
  const [formRefusal, setFormRefusal] = useState<string | null>(null)

  // Things that will change if the form saves this file, none of which affect behaviour.
  const [formWarnings, setFormWarnings] = useState<string[]>([])

  useEffect(() => {
    if (!editing) {
      setDraft(emptyDraft())
      setRawYaml(null)
      setFileOnDisk(null)
      setFormRefusal(null)
      setFormWarnings([])
      return
    }

    setLoading(true)
    setFormRefusal(null)
    setFormWarnings([])

    api
      .getChannel(editing.name)
      .then(async (res) => {
        setFileOnDisk(res.yaml)

        // Ask whether the form can represent this file before offering it. The server decides, by
        // decoding into the form model, writing it back and loading both through the real loader.
        try {
          const parsed = await api.parseChannel(res.yaml)
          if (parsed.editable && parsed.model) {
            setDraft(wireToDraft(parsed.model))
            setFormWarnings(parsed.warnings ?? [])
            setRawYaml(null)
            return
          }
          setFormRefusal(parsed.why ?? 'This channel cannot be shown in the form.')
        } catch {
          // If the question itself could not be asked, the text editor is still correct and still
          // works. Falling back silently is right here: the alternative is refusing to open a
          // channel at all because a convenience feature was unavailable.
          setFormRefusal(
            'The form could not check whether it can show this channel, so the file is open as text.',
          )
        }
        setRawYaml(res.yaml)
      })
      .catch((err) => setLoadError({ message: String(err), problems: [] }))
      .finally(() => setLoading(false))
  }, [editing])

  // The server generates the YAML and validates it in one call.
  //
  // The browser used to assemble the file itself, with its own quoting helper. That worked,
  // but it was a second implementation of a format whose rules live somewhere else: a value
  // that looks like a number, a path with a colon in it, a password containing a hash all
  // need different handling, and getting one wrong writes a file that loads as something
  // other than what the form displayed. Marshalling with the same library that reads the
  // file makes that whole class of mistake unavailable.
  // The HL7 dictionary, so a path field can say what PID-7 actually is while it is typed.
  const [dict, setDict] = useState<DictSegment[]>([])
  useEffect(() => {
    let live = true
    api
      .dictionary()
      .then((r) => {
        if (live) setDict(r.all ?? [])
      })
      .catch(() => {
        // Path descriptions are help, not a requirement. An error banner over a form that
        // still works only teaches people to ignore banners.
      })
    return () => {
      live = false
    }
  }, [])

  const [built, setBuilt] = useState<BuildResult | null>(null)
  // Why the file could not be built, when it could not.
  //
  // This used to be discarded. The preview then went blank with nothing to explain it: choosing the
  // DICOM C-FIND source, whose address is required, emptied the panel and left the Create channel button
  // sitting under it. The server says exactly what is missing, and saying nothing instead is the worst
  // of the options available.
  const [buildError, setBuildError] = useState<string | null>(null)
  const buildSeq = useRef(0)

  useEffect(() => {
    // Editing an existing channel shows its own text, untouched. Reversing a hand-written
    // file back into form state would risk silently changing what a live channel does.
    if (rawYaml !== null) return

    const mine = ++buildSeq.current
    const timer = setTimeout(() => {
      api
        .buildChannel(draftToWire(draft))
        .then((res) => {
          // A slow earlier response must not overwrite a later one, or the preview shows a
          // channel the form no longer describes.
          if (mine === buildSeq.current) {
            setBuilt(res)
            setBuildError(null)
          }
        })
        .catch((err: unknown) => {
          if (mine !== buildSeq.current) return
          setBuilt(null)
          setBuildError(err instanceof Error ? err.message : String(err))
        })
    }, 400)
    return () => clearTimeout(timer)
  }, [draft, rawYaml])

  const generated = rawYaml !== null ? rawYaml : (built?.yaml ?? '')

  // When editing raw text, validity still has to come from somewhere, so that path keeps
  // asking the validator directly.
  useEffect(() => {
    if (rawYaml === null) return
    if (!rawYaml.trim()) {
      setServerCheck(null)
      return
    }
    const timer = setTimeout(() => {
      api
        .validateChannel(rawYaml)
        .then((res) => setServerCheck(res.ok ? { ok: true, note: res.summary?.receives ?? '' } : null))
        .catch(() => setServerCheck(null))
    }, 400)
    return () => clearTimeout(timer)
  }, [rawYaml])

  function set<K extends keyof ChannelDraft>(key: K, value: ChannelDraft[K]) {
    setDraft((d) => ({ ...d, [key]: value }))
  }

  function updateDestination(id: string, patch: Partial<Destination>) {
    setDraft((d) => ({
      ...d,
      destinations: d.destinations.map((x) => (x.id === id ? { ...x, ...patch } : x)),
    }))
  }

  async function submit() {
    const result = await dispatch(saveChannel({ name: editing?.name, yaml: generated }))
    if (!saveChannel.fulfilled.match(result)) return

    // Start it, if it was built to run.
    //
    // Creating a channel used to leave it stopped, which is a surprising place to end up:
    // the form asked whether to start it with the server and was told yes, so somebody who
    // has just built a feed reasonably expects it to be listening. Finding out it is not
    // meant going back to the list and pressing another button, and the failure that
    // reveals it is a sender getting connection refused.
    //
    // Started explicitly here rather than inside the create endpoint, because binding a
    // port is a side effect and an API that does it as a consequence of writing a file is
    // harder to reason about. A failure to start is reported and does not undo the
    // creation: the file is valid and on disk, and an address already in use is worth
    // saying plainly rather than hiding by rolling back.
    if (!editing && draft.enabled) {
      const name = result.payload?.channel?.name
      if (name) {
        try {
          await api.startChannel(name)
        } catch (err) {
          setStartError(
            `${name} was created but did not start: ${
              err instanceof Error ? err.message : String(err)
            }`,
          )
          return
        }
      }
    }

    onClose()
  }

  if (loading) {
    return (
      <div className="p-8">
        <Spinner label="Loading the channel…" />
      </div>
    )
  }

  return (
    <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_28rem]">
      <div className="space-y-5">
        {loadError && <ErrorBox error={loadError} />}
        {saveError && <ErrorBox error={saveError} onDismiss={() => dispatch(clearSaveError())} />}
        {startError && (
          <ErrorBox
            error={{ message: startError, problems: [] }}
            onDismiss={() => setStartError(null)}
          />
        )}

        {/*
          When editing, say which of the two ways this channel is open and offer the other one. Before
          this, an existing channel always opened as text with no explanation and no alternative, and
          the form somebody had used to create it appeared to have vanished.
        */}
        {editing && (
          <div className="rounded-lg border border-slate-800 bg-slate-900/40 p-3">
            {rawYaml === null ? (
              <div className="flex flex-wrap items-center justify-between gap-3">
                <p className="text-xs leading-relaxed text-slate-400">
                  Editing <span className="text-slate-200">{editing.name}</span> in the form. Perfuse
                  checked that the form can hold everything in this file without changing what the
                  channel does.
                </p>
                <button
                  type="button"
                  className="btn-secondary shrink-0 text-xs"
                  onClick={() => setRawYaml(fileOnDisk ?? '')}
                  title="Shows the file exactly as it is on disk. Nothing you have typed into the form is saved by switching."
                >
                  Edit the file as text instead
                </button>
              </div>
            ) : (
              <div className="space-y-2">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <p className="text-xs leading-relaxed text-slate-400">
                    Editing <span className="text-slate-200">{editing.name}</span> as a file.
                  </p>
                  {/*
                    Offered only when the form was not refused. Offering a button that then explains
                    it cannot do the thing is worse than not offering it.
                  */}
                  {formRefusal === null && (
                    <button
                      type="button"
                      className="btn-secondary shrink-0 text-xs"
                      onClick={() => setRawYaml(null)}
                    >
                      Back to the form
                    </button>
                  )}
                </div>
                {formRefusal && (
                  <p className="rounded border border-amber-800/50 bg-amber-950/20 p-2 text-xs leading-relaxed text-amber-200/90">
                    {formRefusal}
                  </p>
                )}
              </div>
            )}

            {/*
              Warnings appear only in the form, since they describe what saving from the form would
              change. They never affect behaviour, which is why they are a note and not a refusal.
            */}
            {rawYaml === null &&
              formWarnings.map((warning) => (
                <p
                  key={warning}
                  className="mt-2 rounded border border-slate-700 bg-slate-950/40 p-2 text-xs leading-relaxed text-slate-400"
                >
                  {warning}
                </p>
              ))}
          </div>
        )}

        {rawYaml !== null ? (
          <Section
            title="Channel file"
            description="The file as it will be saved. Anything the form cannot express can be written here by hand."
          >
            <CodeArea
              language="yaml"
              className="min-h-[28rem]"
              // rawYaml being null is what says "not editing as text", and CodeArea wants a string. The branch this sits in only
              // renders when it is non-null, so the fallback is unreachable - written out rather than asserted because a
              // non-null assertion here would be a claim about a condition several hundred lines away.
              value={rawYaml ?? ''}
              onChange={setRawYaml}
            />

            <div className="mt-5">
              {editing && <ReplayPanel channel={editing.name} candidateYaml={rawYaml} />}
            </div>
          </Section>
        ) : (
          <>
            {/* Offered before the recipes, because somebody who has a sample has a better starting
                point than any template: it is their sender's actual message. */}
            {!editing && <FromSamplePanel onProposed={(yaml) => setRawYaml(yaml)} />}

            {!editing && (
              <Section
                title="Start from something that already works"
                description="Pick the closest one and change it. Each is a complete channel that only needs its addresses filled in — starting from a blank form is what makes this feel like a job for a specialist."
              >
                <div className="grid gap-2 sm:grid-cols-2">
                  {RECIPES.map((r) => (
                    <button
                      key={r.id}
                      type="button"
                      onClick={() => setDraft(r.build())}
                      className="rounded-lg border border-slate-700 p-3 text-left transition hover:border-sky-600 hover:bg-slate-900/60"
                    >
                      <span className="block text-sm font-medium text-slate-100">{r.label}</span>
                      <span className="mt-1 block text-xs leading-relaxed text-slate-400">
                        {r.blurb}
                      </span>
                    </button>
                  ))}
                </div>
              </Section>
            )}

            <Section title="Name it" description="What this channel is for, in your words.">
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="Channel name" hint="Used in logs, metrics and the filename.">
                  <input
                    className="input"
                    value={draft.name}
                    onChange={(e) => set('name', e.target.value)}
                    placeholder="adt-inbound"
                  />
                </Field>
                <Field label="Running">
                  <div className="pt-2">
                    <Toggle
                      checked={draft.enabled}
                      onChange={(v) => set('enabled', v)}
                      label={draft.enabled ? 'Start with the engine' : 'Saved but not started'}
                    />
                  </div>
                </Field>
              </div>
              <div className="mt-4">
                <Field label="Description" hint="What the next person needs to know.">
                  <input
                    className="input"
                    value={draft.description}
                    onChange={(e) => set('description', e.target.value)}
                    placeholder="ADT from the hospital, forwarded to the registry"
                  />
                </Field>
              </div>
              <div className="mt-4">
                <Field
                  label="Group"
                  htmlFor="channel-group"
                  hint="Optional. Channels with the same group are listed together — useful once there
                  are more than a screenful. A label only; it changes no behaviour."
                >
                  <input
                    id="channel-group"
                    className="input"
                    value={draft.group}
                    onChange={(e) => set('group', e.target.value)}
                    placeholder="Lab interfaces"
                    list="perfuse-existing-groups"
                  />
                  {/* Existing groups are offered as suggestions rather than a fixed list, because the
                      commonest mistake is inventing "Lab" alongside "Lab interfaces" and ending up with
                      two groups that mean the same thing. A datalist still allows a new one. */}
                  <datalist id="perfuse-existing-groups">
                    {existingGroups.map((g) => (
                      <option key={g} value={g} />
                    ))}
                  </datalist>
                </Field>
              </div>
              <div className="mt-4">
                <FormatSection draft={draft} set={set} />
              </div>
            </Section>

            <Section
              title="Where messages arrive"
              description="Every transport the engine supports, not just MLLP."
            >
              <SourceSection draft={draft} set={set} />
            </Section>

            <Section
              title="Large documents inside messages"
              description="Some feeds carry a PDF or a scanned image inside a field. Left alone, every copy is stored with every message and the database grows out of proportion to the traffic. Naming those fields here moves them out once and stores each distinct document a single time."
            >
              <Area
                label="Fields to move out"
                value={draft.attachmentsPaths}
                onChange={(v) => set('attachmentsPaths', v)}
                rows={3}
                placeholder={'OBX-5\nZPD-3'}
                hint="One HL7 path per line. A path in MSH is refused — the header is what makes a message
                identifiable, and a message whose header has been replaced by a token cannot be searched for."
              />

              <Num
                label="Only if larger than, in bytes"
                value={draft.attachmentsMinBytes}
                onChange={(v) => set('attachmentsMinBytes', v ?? 0)}
                placeholder="4096"
                hint="Leave empty for the default. Below 64 is refused: the token that replaces the value is
                itself about eighty characters, so moving anything smaller makes the message bigger."
              />

              <Check
                label="Put documents back when a message is read"
                value={draft.attachmentsReassemble}
                onChange={(v) => set('attachmentsReassemble', v)}
                hint="On unless you have a reason. Off, the message browser shows a token where the document was,
                which looks exactly like a feed that lost it — and somebody will spend an afternoon on that."
              />
            </Section>

            {/*
              A v3 channel gets its own transformation editor and neither of the two sections below.
              Both address HL7 v2 segments and fields, so showing them on a v3 channel would offer a
              filter that can never match and steps that would silently do nothing - and the server
              refuses both, so the form would be offering to build something that cannot load.
            */}
            {(draft.dataType === 'hl7v3' || draft.dataType === 'script') && (
              <V3Transformations draft={draft} set={set} />
            )}

            {/*
              An imaging channel gets named actions rather than the path-and-value editor. An object is
              binary with pixel data in it, so a general tag writer can produce an image that opens and is
              wrong - which whoever reads the study cannot detect.
            */}
            {draft.dataType === 'dicom' && <DICOMTransformations draft={draft} set={set} />}

            {draft.dataType !== 'hl7v3' && (
            <>
            <Section
              title="Which messages to keep"
              description="Leave this empty to forward everything. Messages that do not match are still acknowledged — the sender did nothing wrong, we are simply not interested."
            >
              {/*
                A filter the rule rows cannot represent is shown as the expression it is, not as an
                empty rule list. Showing empty rows would tell somebody this channel forwards
                everything while it is in fact filtering, and the next save would have made that true.
              */}
              {draft.rawFilter !== undefined ? (
                <RawFilter
                  value={draft.rawFilter}
                  onChange={(value) => set('rawFilter', value || undefined)}
                  onConvert={() => set('rawFilter', undefined)}
                />
              ) : (
                <>
                  <RuleBuilder
                    rules={draft.rules}
                    onChange={(rules) => set('rules', rules)}
                    emptyHint="No conditions, so every message is forwarded."
                  />

                  {/*
                    The other direction, which was missing. RawFilter could be left but never entered, so the only way
                    into it was to load YAML the rows could not represent.

                    That made a filter unreachable from the form on every format except HL7 v2, because the field list
                    the rows offer is v2 paths - MSH-9.1, PID-3.1 - and an X12 or NCPDP channel has none of them. The
                    server accepts a filter on those formats perfectly well, so the form was refusing to build
                    something the product supports.

                    Seeded with the current rows rather than blank, so switching does not discard work.
                  */}
                  <button
                    type="button"
                    className="btn-ghost mt-3 text-xs"
                    onClick={() => set('rawFilter', rulesToExpression(draft.rules))}
                  >
                    Write the filter as an expression
                  </button>
                </>
              )}
            </Section>

            <Section
              title="Should anything be changed?"
              description="Steps run in order, on messages that passed the conditions above, before any script."
              actions={
                <button
                  type="button"
                  className="btn-ghost"
                  onClick={() => set('steps', [...draft.steps, newStep()])}
                >
                  + Add a change
                </button>
              }
            >
              {draft.steps.length === 0 ? (
                <p className="text-sm text-slate-500">
                  Nothing is changed: messages are forwarded exactly as they arrive. That is a
                  perfectly good channel, and the safest one to start from.
                </p>
              ) : (
                <div className="space-y-4">
                  {draft.steps.map((step, i) => (
                    <StepCard
                      key={step.id}
                      step={step}
                      index={i}
                      total={draft.steps.length}
                      dict={dict}
                      onChange={(next) =>
                        set('steps', draft.steps.map((x) => (x.id === step.id ? next : x)))
                      }
                      onRemove={() => set('steps', draft.steps.filter((x) => x.id !== step.id))}
                      onMove={(to) => set('steps', moveItem(draft.steps, i, to))}
                    />
                  ))}
                </div>
              )}
            </Section>
            </>
            )}

            <Section
              title="Scripts"
              description="Code that runs at each stage. Optional, and most channels need none — but the preprocessor is the only place a message that does not parse can be repaired."
            >
              {/*
                Functional update, like every other setDraft in this file. This one was not, and it lost writes.
                Spreading the draft captured by this render is safe only while updates arrive inside React's event
                handling, where they are batched. The script editors call onChange from CodeMirror's update listener,
                which is outside it - so two edits in quick succession both read the same stale draft and the second
                patch discarded the first. Six boxes filled in order kept one of them, and every box still showed the
                text that had been thrown away.
              */}
              <BuilderScripts
                draft={draft}
                onChange={(patch) => setDraft((d) => ({ ...d, ...patch }))}
              />
            </Section>

            <Section
              title="Where messages go"
              description="Each destination receives a copy. A destination can have its own conditions on top of the channel's."
              actions={
                <button
                  type="button"
                  className="btn-ghost"
                  onClick={() => set('destinations', [...draft.destinations, newDestination()])}
                >
                  + Add destination
                </button>
              }
            >
              <div className="space-y-4">
                {draft.destinations.map((dest, i) => (
                  <DestinationCard
                    key={dest.id}
                    dest={dest}
                    index={i}
                    canRemove={draft.destinations.length > 1}
                    onChange={(patch) => updateDestination(dest.id, patch)}
                    onRemove={() =>
                      set(
                        'destinations',
                        draft.destinations.filter((d) => d.id !== dest.id),
                      )
                    }
                  />
                ))}
              </div>
            </Section>
          </>
        )}
      </div>

      <div className="space-y-4 lg:sticky lg:top-6 lg:self-start">
        <Section
          title="The file this creates"
          description="Exactly what gets written to disk. Advanced users edit this directly; both paths produce the same thing."
        >
          {generated ? (
            <SyntaxBlock code={generated} language="yaml" maxHeight="32rem" />
          ) : (
            <p className="rounded-lg border border-amber-800/60 bg-amber-950/20 p-3 text-xs leading-relaxed text-amber-200">
              {buildError
                ? `This cannot be written as a channel file yet: ${buildError}`
                : 'Nothing to show yet. Fill in the fields above.'}
            </p>
          )}

          {serverCheck?.ok && (
            <p className="mt-3 flex items-start gap-2 text-xs text-emerald-300">
              <span aria-hidden>✓</span>
              <span>Valid. {serverCheck.note}</span>
            </p>
          )}

          {/* Why this exists.
              
              The server checks every draft and returns what is wrong with it. Until now that answer was thrown away: a
              form that produced an unloadable channel showed the file, showed no tick, and said nothing. Found because
              three source types in the dropdown had no implementation behind them, so the builder happily wrote
              "type: dicom_move" and the panel gave no hint that the server would refuse it.
              
              Shown for the form as well as the text editor, because the form is where somebody has no other way to
              find out. */}
          {rawYaml === null && built !== null && !built.ok && built.problems.length > 0 && (
            <div role="alert" className="mt-3 rounded-lg border border-rose-800/60 bg-rose-950/25 p-3">
              <p className="text-xs font-medium text-rose-200">
                The server would not accept this channel:
              </p>
              <ul className="mt-1.5 space-y-1 text-xs text-rose-300/90">
                {built.problems.map((p, i) => (
                  <li key={i}>
                    {p.line ? <span className="font-mono text-rose-400">line {p.line}: </span> : null}
                    {p.message}
                  </li>
                ))}
              </ul>
            </div>
          )}

          {rawYaml === null && built?.ok && (
            <p className="mt-3 flex items-start gap-2 text-xs text-emerald-300">
              <span aria-hidden>✓</span>
              <span>Valid. {built.summary?.receives ?? 'The server accepts this channel.'}</span>
            </p>
          )}
        </Section>

        <div className="flex gap-2">
          <button className="btn-primary flex-1" onClick={submit} disabled={saving}>
            {saving ? 'Saving…' : editing ? 'Save changes' : 'Create channel'}
          </button>
          <button className="btn-ghost" onClick={onClose} disabled={saving}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  )
}

function DestinationCard({
  dest,
  index,
  canRemove,
  onChange,
  onRemove,
}: {
  dest: Destination
  index: number
  canRemove: boolean
  onChange: (patch: Partial<Destination>) => void
  onRemove: () => void
}) {
  const [confirming, setConfirming] = useState(false)

  return (
    <div className="rounded-lg border border-slate-800 bg-slate-950/40 p-4">
      <div className="mb-4 flex items-center justify-between gap-3">
        <span className="text-xs font-semibold tracking-wide text-slate-400 uppercase">
          Destination {index + 1}
        </span>
        <div className="flex items-center gap-3">
          <Toggle
            checked={dest.enabled}
            onChange={(v) => onChange({ enabled: v })}
            label={dest.enabled ? 'active' : 'paused'}
          />
          {canRemove && (
            <button
              type="button"
              onClick={() => setConfirming(true)}
              className="rounded-md px-2 py-1 text-slate-500 hover:bg-slate-800 hover:text-rose-300"
              aria-label="Remove this destination"
            >
              ✕
            </button>
          )}
        </div>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Name" hint="Appears in logs and delivery counts.">
          <input
            className="input"
            value={dest.name}
            onChange={(e) => onChange({ name: e.target.value })}
            placeholder="registry"
          />
        </Field>

        <Field label="Type" htmlFor={`dest-${dest.id}-type`}>
          <select
            id={`dest-${dest.id}-type`}
            className="select"
            value={dest.type}
            onChange={(e) => onChange({ type: e.target.value as Destination['type'] })}
          >
            {(Object.keys(destinationLabels) as Destination['type'][]).map((t) => (
              <option key={t} value={t}>
                {destinationLabels[t]}
              </option>
            ))}
          </select>
          <p className="mt-1 text-xs text-slate-400">{destinationHints[dest.type]}</p>
        </Field>

        {dest.type === 'tcp' && (
          <>
            <Field label="Send to" hint="Host and port of the receiving system.">
              <input
                className="input font-mono"
                value={dest.address}
                onChange={(e) => onChange({ address: e.target.value })}
                placeholder="instrument.lab:9100"
              />
            </Field>

            <Field
              label="How messages are framed"
              hint="No default, deliberately. Writing with the wrong framing does not fail — the far end reads messages split in the wrong places, or waits forever for a terminator that never comes."
            >
              <select
                id={`dest-${index}-tcp-framing`}
                className="select"
                value={dest.destTcpFraming}
                onChange={(e) => onChange({ destTcpFraming: e.target.value as Destination['destTcpFraming'] })}
              >
                <option value="">Choose…</option>
                <option value="mllp">MLLP (0x0b … 0x1c 0x0d)</option>
                <option value="delimited">A delimiter ends each message</option>
                <option value="fixed">Fixed-length records</option>
                <option value="length">A length prefix</option>
                <option value="whole">One message per connection</option>
              </select>
            </Field>

            {dest.destTcpFraming === 'delimited' && (
              <>
                <Field label="Delimiter" hint="Written with escapes, such as \r or \x03.">
                  <input
                    className="input font-mono"
                    value={dest.destTcpDelimiter}
                    onChange={(e) => onChange({ destTcpDelimiter: e.target.value })}
                    placeholder="\r"
                  />
                </Field>
                <Field label="Start block" hint="Written before each message, if the far end expects one.">
                  <input
                    className="input font-mono"
                    value={dest.destTcpStartBlock}
                    onChange={(e) => onChange({ destTcpStartBlock: e.target.value })}
                  />
                </Field>
              </>
            )}

            {dest.destTcpFraming === 'fixed' && (
              <Field label="Record length" hint="Every message is padded or truncated to this many bytes.">
                <input
                  className="input"
                  type="number"
                  value={dest.destTcpRecordLength || ''}
                  onChange={(e) => onChange({ destTcpRecordLength: Number(e.target.value) })}
                />
              </Field>
            )}

            {dest.destTcpFraming === 'length' && (
              <>
                <Field label="Length prefix size" hint="How many bytes carry the length. Two and four are both common.">
                  <input
                    className="input"
                    type="number"
                    value={dest.destTcpLengthBytes || ''}
                    onChange={(e) => onChange({ destTcpLengthBytes: Number(e.target.value) })}
                    placeholder="4"
                  />
                </Field>
                <Field
                  label="Byte order"
                  hint="Both conventions exist and the difference is silent: the far end reads a plausible but wrong length and waits."
                >
                  <label className="flex items-center gap-2 text-sm text-slate-300">
                    <input
                      type="checkbox"
                      className="checkbox"
                      checked={dest.destTcpBigEndian}
                      onChange={(e) => onChange({ destTcpBigEndian: e.target.checked })}
                    />
                    Big-endian (network order)
                  </label>
                </Field>
                <Field label="What the length counts" hint="Whether the prefix includes its own bytes. Both conventions exist.">
                  <label className="flex items-center gap-2 text-sm text-slate-300">
                    <input
                      type="checkbox"
                      className="checkbox"
                      checked={dest.destTcpLengthIncludesHeader}
                      onChange={(e) => onChange({ destTcpLengthIncludesHeader: e.target.checked })}
                    />
                    The length includes the prefix itself
                  </label>
                </Field>
              </>
            )}

            <Field
              label="Wait for a reply"
              hint="Changes what delivered means. Without it, success means the bytes reached the operating system's send buffer — which a peer that crashed a moment later never read."
            >
              <label className="flex items-center gap-2 text-sm text-slate-300">
                <input
                  type="checkbox"
                  className="checkbox"
                  checked={dest.tcpExpectReply}
                  onChange={(e) => onChange({ tcpExpectReply: e.target.checked })}
                />
                Treat a message as delivered only once the far end answers
              </label>
            </Field>

            <Field label="Reuse the connection" hint="Off by default. Some device endpoints refuse a second message on the same connection, which appears as every other message failing.">
              <label className="flex items-center gap-2 text-sm text-slate-300">
                <input
                  type="checkbox"
                  className="checkbox"
                  checked={dest.tcpKeepAlive}
                  onChange={(e) => onChange({ tcpKeepAlive: e.target.checked })}
                />
                Keep the connection open between messages
              </label>
            </Field>

            <Field label="Timeout" hint="How long to wait for the connection and for a reply. Blank uses the server default.">
              <input
                className="input"
                value={dest.destTcpTimeout}
                onChange={(e) => onChange({ destTcpTimeout: e.target.value })}
                placeholder="30s"
              />
            </Field>

            <Field label="Largest message" hint="Bounds one outbound message in bytes. Blank uses the transport default.">
              <input
                className="input"
                type="number"
                value={dest.destTcpMaxMessageSize || ''}
                onChange={(e) => onChange({ destTcpMaxMessageSize: Number(e.target.value) })}
              />
            </Field>
          </>
        )}

        {dest.type === 'mllp' && (
          <>
            <Field label="Send to" hint="Host and port of the receiving system.">
              <input
                className="input font-mono"
                value={dest.address}
                onChange={(e) => onChange({ address: e.target.value })}
                placeholder="registry.internal:6661"
              />
            </Field>

            <Field
              label="Wait for a reply"
              hint="Changes what delivered means. Without it, success means the bytes reached the operating system's send buffer — which a peer that crashed a moment later never read."
            >
              <label className="flex items-center gap-2 text-sm text-slate-300">
                <input
                  type="checkbox"
                  className="checkbox"
                  checked={dest.tcpExpectReply}
                  onChange={(e) => onChange({ tcpExpectReply: e.target.checked })}
                />
                Treat a message as delivered only once the far end answers
              </label>
            </Field>

            <Field
              label="Reuse the connection"
              hint="Off by default. A connection per message works everywhere, and some device endpoints refuse a second message on the same connection — which appears as every other message failing."
            >
              <label className="flex items-center gap-2 text-sm text-slate-300">
                <input
                  type="checkbox"
                  className="checkbox"
                  checked={dest.tcpKeepAlive}
                  onChange={(e) => onChange({ tcpKeepAlive: e.target.checked })}
                />
                Hold one connection open across messages
              </label>
            </Field>
          </>
        )}

        {dest.type === 'kafka' && (
          <>
            <Field
              label="Bootstrap servers"
              hint="Comma separated. Give more than one: a single bootstrap address is a single point of failure for starting up, and the cluster survives losing it when this destination would not."
            >
              <input
                className="input font-mono"
                value={dest.destKafkaBrokers}
                onChange={(e) => onChange({ destKafkaBrokers: e.target.value })}
                placeholder="kafka-1.hospital.local:9092, kafka-2.hospital.local:9092"
              />
            </Field>

            <Field label="Topic">
              <input
                className="input font-mono"
                value={dest.destKafkaTopic}
                onChange={(e) => onChange({ destKafkaTopic: e.target.value })}
                placeholder="adt.events"
              />
            </Field>

            <Field
              label="Partition key"
              hint="Set this. Kafka keeps records in order only within a partition, and records sharing a key always share a partition — so PID-3.1 keeps one patient's events in sequence while letting different patients go in parallel. Left empty there is no guarantee: records stay together for a while and then move, so a discharge can be read before its admission intermittently, under load."
            >
              <input
                className="input font-mono"
                value={dest.destKafkaKey}
                onChange={(e) => onChange({ destKafkaKey: e.target.value })}
                placeholder="PID-3.1"
              />
            </Field>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label="Acknowledgement"
                hint="All waits for every in-sync replica and is the only setting that survives a broker failing mid-write. None does not wait at all and will lose messages."
              >
                <select
                  className="input"
                  value={dest.destKafkaAcks}
                  onChange={(e) =>
                    onChange({ destKafkaAcks: e.target.value as 'all' | 'leader' | 'none' })
                  }
                >
                  <option value="all">All in-sync replicas</option>
                  <option value="leader">Leader only</option>
                  <option value="none">None — may lose messages</option>
                </select>
              </Field>
              <Field
                label="Compression"
                hint="HL7 is highly compressible text and the wire is usually the constraint. Snappy is the cheapest in CPU, which matters on the delivery path."
              >
                <select
                  className="input"
                  value={dest.destKafkaCompression}
                  onChange={(e) =>
                    onChange({
                      destKafkaCompression: e.target.value as
                        | 'none'
                        | 'gzip'
                        | 'snappy'
                        | 'lz4'
                        | 'zstd',
                    })
                  }
                >
                  <option value="snappy">Snappy</option>
                  <option value="lz4">LZ4</option>
                  <option value="zstd">Zstandard</option>
                  <option value="gzip">gzip</option>
                  <option value="none">None</option>
                </select>
              </Field>
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label="Authentication"
                hint="PLAIN sends the password readable on the wire, so pair it with TLS. SCRAM does not."
              >
                <select
                  className="input"
                  value={dest.destKafkaSaslMechanism}
                  onChange={(e) =>
                    onChange({
                      destKafkaSaslMechanism: e.target.value as
                        | ''
                        | 'plain'
                        | 'scram-sha-256'
                        | 'scram-sha-512',
                    })
                  }
                >
                  <option value="">None</option>
                  <option value="plain">PLAIN</option>
                  <option value="scram-sha-256">SCRAM-SHA-256</option>
                  <option value="scram-sha-512">SCRAM-SHA-512</option>
                </select>
              </Field>
              <Field label="Username">
                <input
                  className="input font-mono"
                  value={dest.destKafkaSaslUsername}
                  onChange={(e) => onChange({ destKafkaSaslUsername: e.target.value })}
                />
              </Field>
            </div>

            <Field label="Password">
              <input
                type={dest.destKafkaSaslPassword.startsWith('${') ? 'text' : 'password'}
                className="input font-mono"
                value={dest.destKafkaSaslPassword}
                onChange={(e) => onChange({ destKafkaSaslPassword: e.target.value })}
                placeholder="${KAFKA_PASSWORD}"
              />
            </Field>
          </>
        )}

        {dest.type === 'broker' && (
          <>
            <Field
              label="Broker address"
              hint="The STOMP port, 61613 by default — not the broker's native port. JMS is a Java API rather than a protocol, so STOMP is what crosses the wire."
            >
              <input
                className="input font-mono"
                value={dest.destBrokerAddr}
                onChange={(e) => onChange({ destBrokerAddr: e.target.value })}
                placeholder="broker.hospital.local:61613"
              />
            </Field>

            <Field
              label="Queue or topic"
              hint="Passed through exactly as typed. Brokers spell these differently — /queue/name on ActiveMQ, a bare name on RabbitMQ — so guessing would work against one and silently publish nowhere on another."
            >
              <input
                className="input font-mono"
                value={dest.destBrokerDestination}
                onChange={(e) => onChange({ destBrokerDestination: e.target.value })}
                placeholder="/queue/hl7.outbound"
              />
            </Field>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Login" hint="Leave both empty if the broker wants no credentials.">
                <input
                  className="input font-mono"
                  value={dest.destBrokerLogin}
                  onChange={(e) => onChange({ destBrokerLogin: e.target.value })}
                />
              </Field>
              <Field label="Passcode">
                <input
                  type={dest.destBrokerPasscode.startsWith('${') ? 'text' : 'password'}
                  className="input font-mono"
                  value={dest.destBrokerPasscode}
                  onChange={(e) => onChange({ destBrokerPasscode: e.target.value })}
                  placeholder="${BROKER_PASSCODE}"
                />
              </Field>
            </div>

            <Field
              label="Content type"
              hint="Leave empty for application/hl7-v2+er7. Some consumers route on this header rather than on the body."
            >
              <input
                className="input font-mono"
                value={dest.destBrokerContentType}
                onChange={(e) => onChange({ destBrokerContentType: e.target.value })}
                placeholder="application/hl7-v2+er7"
              />
            </Field>

            <Field
              label="Persistence"
              hint="On unless the consumer genuinely does not care. Off means the broker may drop the message when it restarts, and nothing reports that — the send succeeded."
            >
              <label className="flex items-center gap-2 text-sm text-slate-300">
                <input
                  type="checkbox"
                  className="checkbox"
                  checked={dest.destBrokerPersistent}
                  onChange={(e) => onChange({ destBrokerPersistent: e.target.checked })}
                />
                Ask the broker to keep the message across a restart
              </label>
            </Field>
          </>
        )}

        {dest.type === 'file' && (
          <>
            <Field
              label="Folder"
              hint="One file per day and message type. Files can be replayed later, byte for byte."
            >
              <input
                className="input font-mono"
                value={dest.dir}
                onChange={(e) => onChange({ dir: e.target.value })}
                placeholder="./archive"
              />
            </Field>

            <Field
              label="File name"
              hint="Leave empty for a timestamp and the message's control ID. Placeholders written as ${...} are filled in, so a name can carry the date or the message type — which is the difference between a folder somebody can find a message in and one they cannot."
            >
              <input
                className="input font-mono"
                value={dest.fileName}
                onChange={(e) => onChange({ fileName: e.target.value })}
                placeholder="${date}-${messageType}.hl7"
              />
            </Field>

            <Field
              label="Suffix while writing"
              hint="Defaults to .part, and removed by a rename once the file is complete. Whoever collects these files should not pick up a half-written one, and a rename within a folder is atomic."
            >
              <input
                className="input font-mono"
                value={dest.tempSuffix}
                onChange={(e) => onChange({ tempSuffix: e.target.value })}
                placeholder=".part"
              />
            </Field>

            <Field
              label="Delete after (hours)"
              hint="Empty or zero keeps files forever. An archive nobody prunes fills the disk, and a full disk stops the channel rather than the archiving."
            >
              <input
                className="input font-mono"
                type="number"
                min={0}
                value={dest.retainHours}
                onChange={(e) => onChange({ retainHours: e.target.value })}
                placeholder="0"
              />
            </Field>
          </>
        )}

        {dest.type === 'http' && (
          <>
            <Field
              label="Post to"
              hint="Use https for anything that is not on this machine; plain http to a remote host is refused."
            >
              <input
                className="input font-mono"
                value={dest.url}
                onChange={(e) => onChange({ url: e.target.value })}
                placeholder="https://api.internal/messages"
              />
            </Field>

            <Field label="Method" hint="POST unless the endpoint insists otherwise.">
              <select
                className="select"
                value={dest.httpMethod}
                onChange={(e) => onChange({ httpMethod: e.target.value })}
              >
                <option value="POST">POST</option>
                <option value="PUT">PUT</option>
                <option value="PATCH">PATCH</option>
              </select>
            </Field>

            <Field
              label="Content type"
              hint="Leave empty for application/hl7-v2+er7. Many endpoints want text/plain."
            >
              <input
                className="input font-mono"
                value={dest.httpContentType}
                onChange={(e) => onChange({ httpContentType: e.target.value })}
                placeholder="application/hl7-v2+er7"
              />
            </Field>

            <Field
              label="Treat as failed if the reply contains"
              hint="Some endpoints answer 200 with an error in the body. Without this, that message is gone and nobody knows."
            >
              <input
                className="input font-mono"
                value={dest.httpFailOnBody}
                onChange={(e) => onChange({ httpFailOnBody: e.target.value })}
                placeholder="ERROR"
              />
            </Field>

            <Field
              label="Extra headers"
              hint='One per line, written as "name: value". Some endpoints route on a header rather than on the URL, and a partner will usually send you the exact set to paste in.'
            >
              <textarea
                className="input font-mono"
                rows={3}
                value={dest.httpHeaders}
                onChange={(e) => onChange({ httpHeaders: e.target.value })}
                placeholder={'X-Facility: RGH\nX-Feed: adt'}
              />
            </Field>

            <Field
              label="Bearer token"
              hint="Sent as an Authorization header. Stored with the channel and never shown again, so leaving this empty when editing keeps the token already saved."
            >
              <input
                className="input font-mono"
                type="password"
                value={dest.httpBearerToken}
                onChange={(e) => onChange({ httpBearerToken: e.target.value })}
                placeholder="leave empty to keep the stored token"
              />
            </Field>

            <Field
              label="Status codes that mean delivered"
              hint="Comma separated. Empty means any 2xx. Some endpoints answer 202 for accepted-but-not-processed, which is worth deciding about rather than discovering."
            >
              <input
                className="input font-mono"
                value={dest.httpSuccessStatus}
                onChange={(e) => onChange({ httpSuccessStatus: e.target.value })}
                placeholder="200, 201, 202"
              />
            </Field>

            <Field
              label="Follow redirects"
              hint="Off by default. A redirect can point at a different host, which sends the message somewhere nobody configured and reports success."
            >
              <label className="flex items-center gap-2 text-sm text-slate-300">
                <input
                  type="checkbox"
                  className="checkbox"
                  checked={dest.httpFollowRedirects}
                  onChange={(e) => onChange({ httpFollowRedirects: e.target.checked })}
                />
                Follow a 3xx response to its new location
              </label>
            </Field>
          </>
        )}

        {dest.type === 'database' && (
          <>
            <Field label="Database">
              <select
                className="select"
                value={dest.dbDriver}
                onChange={(e) => onChange({ dbDriver: e.target.value })}
              >
                <option value="postgres">PostgreSQL</option>
                <option value="mysql">MySQL or MariaDB</option>
                <option value="sqlserver">Microsoft SQL Server</option>
                <option value="sqlite">SQLite</option>
              </select>
            </Field>

            <Field
              label="Connection string"
              hint="Write the password as ${DB_PASSWORD} and set it in the environment. Channels live in git, so a literal password here becomes a credential in version control."
            >
              <input
                className="input font-mono text-xs"
                value={dest.dbDSN}
                onChange={(e) => onChange({ dbDSN: e.target.value })}
                placeholder="postgres://interface:${DB_PASSWORD}@db.internal:5432/records"
              />
            </Field>

            <Field
              label="Statement"
              hint={
                dest.dbDriver === 'postgres'
                  ? 'Use $1, $2 for the values.'
                  : dest.dbDriver === 'sqlserver'
                    ? 'Use @p1, @p2 for the values.'
                    : 'Use ? for each value, in order.'
              }
            >
              <textarea
                className="input h-20 font-mono text-xs"
                value={dest.dbStatement}
                onChange={(e) => onChange({ dbStatement: e.target.value })}
                placeholder={
                  dest.dbDriver === 'postgres'
                    ? 'INSERT INTO admissions (mrn, surname) VALUES ($1, $2)'
                    : dest.dbDriver === 'sqlserver'
                      ? 'INSERT INTO admissions (mrn, surname) VALUES (@p1, @p2)'
                      : 'INSERT INTO admissions (mrn, surname) VALUES (?, ?)'
                }
              />
            </Field>

            <Field
              label="Values, in order"
              hint="One field path per line. These are bound as parameters, never built into the SQL — a name containing an apostrophe would break a concatenated statement, and O'Brien is a common name."
            >
              <textarea
                className="input h-20 font-mono text-xs"
                value={dest.dbParams.join('\n')}
                onChange={(e) =>
                  onChange({
                    dbParams: e.target.value
                      .split('\n')
                      .map((l) => l.trim())
                      .filter((l) => l !== ''),
                  })
                }
                placeholder={'PID-3.1\nPID-5.1'}
              />
            </Field>

            {dest.dbStatement !== '' && (
              <PlaceholderCheck
                statement={dest.dbStatement}
                driver={dest.dbDriver}
                count={dest.dbParams.length}
              />
            )}

            <Field
              label="Most connections to open"
              hint="Empty for the default. Worth setting where the database has a connection limit shared with other applications — exhausting it makes every one of them fail, and the cause looks like it is in whichever noticed first."
            >
              <input
                className="input"
                type="number"
                min={1}
                value={dest.dbMaxOpenConns}
                onChange={(e) => onChange({ dbMaxOpenConns: e.target.value })}
                placeholder="4"
              />
            </Field>
          </>
        )}

        {dest.type === 'dicom' && (
          <>
            <Field
              label="Archive"
              hint="Host and port of the PACS or workstation. 104 is the registered DICOM port and what most
              archives listen on, but 11112 is common for anything added later."
            >
              <input
                className="input font-mono text-xs"
                value={dest.dicomAddr}
                onChange={(e) => onChange({ dicomAddr: e.target.value })}
                placeholder="pacs.hospital.internal:104"
              />
            </Field>

            <Field
              label="Called AE title"
              hint="The name the archive answers to. Required in practice: most archives refuse a connection
              addressed to anything else, and that refusal looks exactly like the archive being down."
            >
              <input
                className="input font-mono text-xs"
                value={dest.dicomCalledAe}
                onChange={(e) => onChange({ dicomCalledAe: e.target.value })}
                placeholder="PACS_MAIN"
                maxLength={16}
              />
            </Field>

            <Field
              label="Calling AE title"
              hint="The name Perfuse presents. Frequently the only access control on an imaging endpoint, so
              the archive's administrator will want to know what to expect. Sixteen characters maximum — that
              is what the wire format carries."
            >
              <input
                className="input font-mono text-xs"
                value={dest.dicomCallingAe}
                onChange={(e) => onChange({ dicomCallingAe: e.target.value })}
                placeholder="PERFUSE"
                maxLength={16}
              />
            </Field>

            <Field
              label="Encryption"
              hint="DICOM metadata carries patient names, so an unencrypted link is a compliance problem rather
              than an inconvenience. Turn this on unless the archive cannot accept it. Mirth needs a paid
              extension for this and its community forks cannot offer it at all."
            >
              <Toggle
                checked={dest.dicomTls}
                onChange={(v) => onChange({ dicomTls: v })}
                label={dest.dicomTls ? 'encrypted' : 'not encrypted'}
              />
            </Field>
          </>
        )}

        {dest.type === 'javascript' && (
          <>
            <Field
              label="Script"
              hint="The message is in scope as msg, exactly as in a transformation, so msg['PID']['PID.3']['PID.3.1']
              reads the patient identifier. Do the work and fall off the end to report success; return a string to
              fail the delivery with that string as the reason."
            >
              <CodeArea
                language="js"
                
                rows={12}
                value={dest.jsScript}
                onChange={(next) => onChange({ jsScript: next })}
                placeholder={"var mrn = msg['PID']['PID.3']['PID.3.1'].toString();\nlogger.info('handled ' + mrn);"}
              />
            </Field>

            <Field
              label="Time limit"
              hint="Ten seconds if left empty. A script here runs on the delivery path, so one that hangs holds
              every message queued behind it — this is a stalled channel, not a slow one. Anything above five
              minutes is refused."
            >
              <input
                className="input"
                value={dest.jsTimeout}
                onChange={(e) => onChange({ jsTimeout: e.target.value })}
                placeholder="10s"
              />
            </Field>

            <Field
              label="Result"
              hint="Off by default, because scripts written for Mirth overwhelmingly do their work and return
              nothing — requiring a result would break every one of them on import. Turn it on if you would
              rather a script be explicit about whether it worked."
            >
              <Toggle
                checked={dest.jsRequireResult}
                onChange={(v) => onChange({ jsRequireResult: v })}
                label={dest.jsRequireResult ? 'must report a result' : 'returning nothing means success'}
              />
            </Field>
          </>
        )}

        {dest.type === 'soap' && (
          <>
            <Field label="Endpoint">
              <input
                className="input font-mono text-xs"
                value={dest.soapUrl}
                onChange={(e) => onChange({ soapUrl: e.target.value })}
                placeholder="https://registry.internal/PatientService"
              />
            </Field>

            <Field
              label="SOAP version"
              hint="1.1 unless the service says otherwise. The difference is not cosmetic — a 1.2 service
              rejects a 1.1 envelope, and the fault it returns is about the version rather than your message."
            >
              <select
                className="input"
                value={dest.soapVersion}
                onChange={(e) => onChange({ soapVersion: e.target.value as '1.1' | '1.2' })}
              >
                <option value="1.1">1.1</option>
                <option value="1.2">1.2</option>
              </select>
            </Field>

            <Field label="Action" hint="The SOAPAction. Most 1.1 services dispatch on it.">
              <input
                className="input font-mono text-xs"
                value={dest.soapAction}
                onChange={(e) => onChange({ soapAction: e.target.value })}
                placeholder="urn:SubmitPatient"
              />
            </Field>

            <Field
              label="Request body"
              hint="Only what goes inside the envelope — the Envelope and Body elements are added for you.
              Put a field in with ${PID-3.1}; values are XML-escaped automatically."
            >
              <CodeArea
                language="xml"
                
                rows={9}
                value={dest.soapBody}
                onChange={(next) => onChange({ soapBody: next })}
                placeholder={
                  '<SubmitPatient xmlns="urn:registry">\n' +
                  '  <Mrn>${PID-3.1}</Mrn>\n' +
                  '  <Family>${PID-5.1}</Family>\n' +
                  '  <Given>${PID-5.2}</Given>\n' +
                  '  <BirthDate>${PID-7}</BirthDate>\n' +
                  '</SubmitPatient>'
                }
              />
            </Field>

            {/* Caught at load anyway, but saying it here saves the round trip - and this is the mistake
                everybody makes, because a captured request or a vendor document always shows the envelope. */}
            {/^\s*<[a-zA-Z:]*[eE]nvelope/.test(dest.soapBody) && (
              <p className="rounded-lg border border-amber-900/60 bg-amber-950/30 p-3 text-sm text-amber-200">
                This looks like a whole envelope. Paste only what goes <em>inside</em> the Body element — the
                envelope is added for you, and sending two would produce a fault about the request structure
                that looks like a problem at their end.
              </p>
            )}

            <details className="rounded-lg border border-slate-800 p-3">
              <summary className="cursor-pointer text-sm text-slate-400 hover:text-slate-300">
                Authentication and fault handling
              </summary>
              <div className="mt-3 space-y-3">
              <Field
                label="Envelope header"
                hint="XML for the SOAP Header element. Left empty, no header is sent at all, which is what
                most services want."
              >
                <CodeArea
                language="xml"
                  
                  rows={4}
                  value={dest.soapHeader}
                  onChange={(next) => onChange({ soapHeader: next })}
                />
              </Field>

              <Field label="Username" hint="HTTP basic authentication, which many of these services expect.">
                <input
                  className="input font-mono"
                  value={dest.soapUsername}
                  onChange={(e) => onChange({ soapUsername: e.target.value })}
                />
              </Field>

              <Field label="Password">
                <input
                  type="password"
                  className="input font-mono"
                  value={dest.soapPassword}
                  onChange={(e) => onChange({ soapPassword: e.target.value })}
                />
              </Field>

              <Field
                label="Faults that mean success"
                hint="Comma-separated. A service that answers a resend with 'already submitted' is telling you
                the message arrived — without this the queue retries forever against a receiver that has it."
              >
                <input
                  className="input font-mono text-xs"
                  value={dest.soapFaultIsSuccess}
                  onChange={(e) => onChange({ soapFaultIsSuccess: e.target.value })}
                  placeholder="already submitted, DuplicateSubmission"
                />
              </Field>
              </div>
            </details>
          </>
        )}

        {dest.type === 'document' && (
          <>
            <Field label="Directory" hint="Where the documents are written. Usually a watched print share.">
              <input
                className="input font-mono"
                value={dest.docDir}
                onChange={(e) => onChange({ docDir: e.target.value })}
                placeholder="/mnt/reports"
              />
            </Field>

            <Field
              label="Format"
              hint="PDF prints the same everywhere, which is normally the point. Text is better if the next
              step is a script."
            >
              <select
                className="input"
                value={dest.docFormat}
                onChange={(e) => onChange({ docFormat: e.target.value as 'pdf' | 'text' })}
              >
                <option value="pdf">PDF</option>
                <option value="text">Plain text</option>
              </select>
            </Field>

            <Field
              label="The document"
              hint="Plain text, laid out how you want it printed. Put a field in with ${PID-5.1}, and use
              ${today} or ${now} for the date. This is not HTML — tags would be printed literally."
            >
              <textarea
                className="input font-mono text-xs"
                rows={10}
                spellCheck={false}
                value={dest.docTemplate}
                onChange={(e) => onChange({ docTemplate: e.target.value })}
                placeholder={
                  'LABORATORY RESULT\n' +
                  'Printed: ${today}\n\n' +
                  'Patient:  ${PID-5.1}, ${PID-5.2}\n' +
                  'MRN:      ${PID-3.1}\n\n' +
                  'Test:     ${OBX-3.2}\n' +
                  'Result:   ${OBX-5} ${OBX-6}\n' +
                  'Range:    ${OBX-7}\n'
                }
              />
            </Field>

            {/* Said next to the template, because this is the behaviour people are most likely to be surprised
                by, and it is a deliberate choice rather than a limitation. */}
            <p className="rounded-lg border border-slate-800 bg-slate-900/50 p-3 text-xs text-slate-400">
              If a message is missing one of these fields, the delivery fails instead of printing a page with a
              gap in it. A report with a blank where a name should be gets signed and filed as though it were
              complete.
            </p>

            <Field label="Title" hint="Appears in the document properties, not on the page.">
              <input
                className="input font-mono"
                value={dest.docTitle}
                onChange={(e) => onChange({ docTitle: e.target.value })}
                placeholder="Result for ${PID-5.1}"
              />
            </Field>

            {dest.docFormat === 'pdf' && (
              <>
                <Toggle
                  label="Landscape"
                  checked={dest.docLandscape}
                  onChange={(v) => onChange({ docLandscape: v })}
                />

                <Field
                  label="Font size (points)"
                  hint="Empty for the default. Worth raising for a document somebody reads on paper, and worth lowering only if a wide table is being cut off — a smaller font fits more and is the usual reason a printed result becomes unreadable."
                >
                  <input
                    className="input"
                    type="number"
                    min={4}
                    max={72}
                    step={0.5}
                    value={dest.docFontSize}
                    onChange={(e) => onChange({ docFontSize: e.target.value })}
                    placeholder="10"
                  />
                </Field>
              </>
            )}
          </>
        )}

        {dest.type === 'ftp' && (
          <>
            {/* Said once, at the top, where somebody choosing this can still change their mind. Not a
                warning banner they will learn to scroll past. */}
            <p className="rounded-lg border border-slate-800 bg-slate-900/50 p-3 text-xs text-slate-400">
              Prefer SFTP where the far end supports it — it is one connection, one port, and it encrypts
              everything. FTP is here because a lot of analysers and bureau services accept nothing else.
            </p>

            <Field label="Server" hint="Host, and a port if it is not 21 (or 990 for implicit TLS).">
              <input
                className="input font-mono"
                value={dest.ftpHost}
                onChange={(e) => onChange({ ftpHost: e.target.value })}
                placeholder="ftp.lab.internal"
              />
            </Field>

            <Field label="User" hint="Leave empty for anonymous.">
              <input
                className="input font-mono"
                value={dest.ftpUser}
                onChange={(e) => onChange({ ftpUser: e.target.value })}
                placeholder="interface"
              />
            </Field>

            <Field label="Password">
              <input
                type="password"
                className="input font-mono"
                value={dest.ftpPassword}
                onChange={(e) => onChange({ ftpPassword: e.target.value })}
              />
            </Field>

            <Field
              label="Encryption"
              hint="Explicit (AUTH TLS) is the usual answer. Implicit is TLS from the first byte, normally
              on port 990, and some older analysers only speak that."
            >
              <select
                className="input"
                value={dest.ftpSecurity}
                onChange={(e) =>
                  onChange({ ftpSecurity: e.target.value as 'explicit' | 'implicit' | 'none' })
                }
              >
                <option value="explicit">Explicit TLS (AUTH TLS)</option>
                <option value="implicit">Implicit TLS (port 990)</option>
                <option value="none">None — plain FTP</option>
              </select>
            </Field>

            {/* Shown only when the combination that matters has actually been chosen. This is the one real
                footgun here, and it is worth interrupting for - but only then. */}
            {dest.ftpSecurity === 'none' && dest.ftpPassword !== '' && (
              <div className="rounded-lg border border-amber-900/60 bg-amber-950/30 p-3">
                <p className="text-sm text-amber-200">
                  That password will cross the network in clear text on every delivery.
                </p>
                <p className="mt-1 text-xs text-amber-300/80">
                  Anyone able to watch the traffic can read it. If the server genuinely has no TLS, tick the
                  box below to say so deliberately — otherwise this channel will be refused when it loads.
                </p>
                <label className="mt-2 flex items-center gap-2 text-sm text-amber-100">
                  <input
                    type="checkbox"
                    checked={dest.ftpAllowClearPassword}
                    onChange={(e) => onChange({ ftpAllowClearPassword: e.target.checked })}
                  />
                  This server has no TLS and I accept sending the password in clear text
                </label>
              </div>
            )}

            <Field label="Directory">
              <input
                className="input font-mono"
                value={dest.ftpDir}
                onChange={(e) => onChange({ ftpDir: e.target.value })}
                placeholder="/incoming"
              />
            </Field>

            {dest.ftpSecurity !== 'none' && (
              <Toggle
                label="Accept any certificate"
                checked={dest.ftpInsecureSkipVerify}
                onChange={(v) => onChange({ ftpInsecureSkipVerify: v })}
              />
            )}
            {dest.ftpInsecureSkipVerify && (
              <p className="text-xs text-slate-500">
                Often unavoidable — these servers frequently have a self-signed certificate nobody can
                reissue. It does mean the connection is encrypted but not authenticated, so it protects
                against reading the traffic and not against a machine pretending to be the server.
              </p>
            )}
          </>
        )}

        {dest.type === 's3' && (
          <>
            <Field label="Bucket">
              <input
                className="input font-mono"
                value={dest.s3Bucket}
                onChange={(e) => onChange({ s3Bucket: e.target.value })}
                placeholder="hospital-hl7-archive"
              />
            </Field>

            <Field
              label="Region"
              hint="Part of the request signature, so a wrong one fails as if the credentials were wrong."
            >
              <input
                className="input font-mono"
                value={dest.s3Region}
                onChange={(e) => onChange({ s3Region: e.target.value })}
                placeholder="eu-west-2"
              />
            </Field>

            <Field
              label="Object key"
              hint="Leave empty for a date-partitioned default, which keeps the bucket listable and lets
              a lifecycle rule age messages out. Placeholders: ${date} ${timestamp} ${control_id}
              ${message_type} ${channel}"
            >
              <input
                className="input font-mono text-xs"
                value={dest.s3Key}
                onChange={(e) => onChange({ s3Key: e.target.value })}
                placeholder="${date}/${channel}/${control_id}.hl7"
              />
            </Field>

            <Field
              label="Access key ID"
              hint="Best practice is to reference an environment variable rather than write the value
              here, because this file goes into version control."
            >
              <input
                className="input font-mono text-xs"
                value={dest.s3AccessKeyId}
                onChange={(e) => onChange({ s3AccessKeyId: e.target.value })}
                placeholder="${AWS_ACCESS_KEY_ID}"
              />
            </Field>

            <Field label="Secret access key">
              <input
                type={dest.s3SecretAccessKey.startsWith('${') ? 'text' : 'password'}
                className="input font-mono text-xs"
                value={dest.s3SecretAccessKey}
                onChange={(e) => onChange({ s3SecretAccessKey: e.target.value })}
                placeholder="${AWS_SECRET_ACCESS_KEY}"
              />
            </Field>

            <Field
              label="Session token"
              hint="Only for temporary credentials — an assumed role or federated login. Those expire, so a channel configured with one stops delivering when they do, which is worth knowing before it happens rather than after."
            >
              <input
                type={dest.s3SessionToken.startsWith('${') ? 'text' : 'password'}
                className="input font-mono text-xs"
                value={dest.s3SessionToken}
                onChange={(e) => onChange({ s3SessionToken: e.target.value })}
                placeholder="${AWS_SESSION_TOKEN}"
              />
            </Field>

            {/* Shown only when a literal secret has been typed. A permanent warning would be ignored,
                and the advice is only actionable at the moment somebody is doing the thing. */}
            {dest.s3SecretAccessKey !== '' && !dest.s3SecretAccessKey.startsWith('${') && (
              <p className="text-xs text-amber-300/90">
                That secret will be written into the channel file. Consider{' '}
                <code className="font-mono">{'${AWS_SECRET_ACCESS_KEY}'}</code> instead — it is read
                from the environment at startup, so the file stays safe to commit.
              </p>
            )}

            <Field
              label="Endpoint"
              hint="Only for MinIO, Ceph, Wasabi and the like. Leave empty for AWS."
            >
              <input
                className="input font-mono text-xs"
                value={dest.s3Endpoint}
                onChange={(e) =>
                  onChange({
                    s3Endpoint: e.target.value,
                    // Turned on with the endpoint, because nearly every S3-compatible store requires it
                    // and forgetting produces a DNS error naming a hostname nobody typed. Still a
                    // checkbox, so it can be turned back off.
                    s3PathStyle: e.target.value !== '' ? true : dest.s3PathStyle,
                  })
                }
                placeholder="https://minio.hospital.local:9000"
              />
            </Field>

            <Toggle
              label="Path-style addressing"
              checked={dest.s3PathStyle}
              onChange={(v) => onChange({ s3PathStyle: v })}
            />

            <Field
              label="Server-side encryption"
              hint="Sets the encryption header. Often required by a bucket policy, which otherwise
              refuses uploads with a 403 that does not say why."
            >
              <input
                className="input font-mono"
                value={dest.s3Encryption}
                onChange={(e) => onChange({ s3Encryption: e.target.value })}
                placeholder="AES256"
              />
            </Field>
          </>
        )}

        {dest.type === 'sftp' && (
          <>
            <Field label="Server" hint="Host, and a port if it is not 22.">
              <input
                className="input font-mono"
                value={dest.sftpHost}
                onChange={(e) => onChange({ sftpHost: e.target.value })}
                placeholder="sftp.lab.internal"
              />
            </Field>

            <Field label="User">
              <input
                className="input font-mono"
                value={dest.sftpUser}
                onChange={(e) => onChange({ sftpUser: e.target.value })}
                placeholder="interface"
              />
            </Field>

            <Field
              label="Private key file"
              hint="The better choice, and what most hospital security teams ask for. Leave empty to use a password."
            >
              <input
                className="input font-mono text-xs"
                value={dest.sftpKeyFile}
                onChange={(e) => onChange({ sftpKeyFile: e.target.value })}
                placeholder="/etc/perfuse/id_ed25519"
              />
            </Field>

            {dest.sftpKeyFile === '' && (
              <Field
                label="Password"
                hint="Write it as ${SFTP_PASSWORD} and set it in the environment. Channels live in git."
              >
                <input
                  className="input font-mono text-xs"
                  value={dest.sftpPassword}
                  onChange={(e) => onChange({ sftpPassword: e.target.value })}
                  placeholder="${SFTP_PASSWORD}"
                />
              </Field>
            )}

            <Field
              label="Known hosts file"
              hint="Required. Without it there is nothing to tell the real server from anything answering on that address, and the credentials go over before anyone notices. Build it with: ssh-keyscan -H the-host >> known_hosts"
            >
              <input
                className="input font-mono text-xs"
                value={dest.sftpKnownHosts}
                onChange={(e) => onChange({ sftpKnownHosts: e.target.value })}
                placeholder="/etc/perfuse/known_hosts"
              />
            </Field>

            <Field label="Remote directory">
              <input
                className="input font-mono"
                value={dest.sftpDir}
                onChange={(e) => onChange({ sftpDir: e.target.value })}
                placeholder="/incoming"
              />
            </Field>

            {dest.sftpKnownHosts === '' && (
              <p className="rounded-md border border-rose-900/60 bg-rose-950/30 p-2 text-xs text-rose-200">
                Without a known hosts file the channel will not start. That is
                deliberate: an unverified SFTP connection is encrypted but not
                authenticated, which is a less obvious problem than no encryption at all.
              </p>
            )}
          </>
        )}

        {dest.type === 'channel' && (
          <>
            <Field
              label="Channel to hand it to"
              hint="The name of another channel in this server. That channel's own filter and changes will be applied to the message."
            >
              <input
                className="input font-mono text-xs"
                value={dest.routeTo}
                onChange={(e) => onChange({ routeTo: e.target.value })}
                placeholder="lab-results"
              />
            </Field>
            <p className="rounded-md border border-slate-700 bg-slate-950/40 p-2 text-xs leading-relaxed text-slate-400">
              The other channel does not need to be listening on a port — the message is handed
              straight to it. It does need to be running, and Perfuse will refuse to start if two
              channels route to each other in a circle.
            </p>
          </>
        )}

        {dest.type === 'smtp' && (
          <>
            <p className="rounded-lg border border-amber-800/50 bg-amber-950/20 p-3 text-xs leading-relaxed text-amber-300/90">
              Email is stored and forwarded by systems outside your control, and delivered to
              whatever address was typed. Use this to tell somebody a message arrived, not to
              move clinical data. Sending a whole message requires ticking the attachment box
              below — it is not something you can arrive at by leaving a field empty.
            </p>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Mail server" hint="Host, and a port if it is not 587.">
                <input
                  className="input font-mono"
                  value={dest.smtpHost}
                  onChange={(e) => onChange({ smtpHost: e.target.value })}
                  placeholder="mail.example.org"
                />
              </Field>
              <Field
                label="From"
                hint="Required. A message with no sender is discarded silently by a lot of mail infrastructure, which makes it the hardest kind of failure to find."
              >
                <input
                  className="input font-mono"
                  value={dest.smtpFrom}
                  onChange={(e) => onChange({ smtpFrom: e.target.value })}
                  placeholder="perfuse@example.org"
                />
              </Field>
            </div>

            <Field label="To" hint="Separate several with commas.">
              <input
                className="input font-mono"
                value={dest.smtpTo}
                onChange={(e) => onChange({ smtpTo: e.target.value })}
                placeholder="coordinator@example.org, oncall@example.org"
              />
            </Field>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Cc" hint="Visible to everybody who receives it.">
                <input
                  className="input font-mono"
                  value={dest.smtpCC}
                  onChange={(e) => onChange({ smtpCC: e.target.value })}
                />
              </Field>
              <Field
                label="Bcc"
                hint="Not written into any header. In this setting the recipient list is often who is being told about a patient, so it stays hidden."
              >
                <input
                  className="input font-mono"
                  value={dest.smtpBCC}
                  onChange={(e) => onChange({ smtpBCC: e.target.value })}
                />
              </Field>
            </div>

            <Field
              label="Subject"
              hint="Fields can be referenced in braces, so {MSH-9.1} becomes the message type and {MSH-10} its control ID."
            >
              <input
                className="input"
                value={dest.smtpSubject}
                onChange={(e) => onChange({ smtpSubject: e.target.value })}
                placeholder="{MSH-9.1} received, control {MSH-10}"
              />
            </Field>

            <Field
              label="Body"
              hint="A summary of what happened. Fields can be referenced in braces here too."
            >
              <textarea
                className="input"
                rows={3}
                value={dest.smtpBody}
                onChange={(e) => onChange({ smtpBody: e.target.value })}
                placeholder="A {MSH-9.1} message arrived for {PID-5.1}."
              />
            </Field>

            <Field
              label="Attachment"
              hint="Only attach the message if somebody genuinely needs to read it. It puts clinical content into email."
            >
              <Toggle
                label="Attach the whole message as a file"
                checked={dest.smtpAttach}
                onChange={(v) => onChange({ smtpAttach: v })}
              />
            </Field>

            {dest.smtpAttach && (
              <Field
                label="Attachment file name"
                hint="What the recipient sees in their mail client. Leave empty for a generated name — which is safe and tells them nothing, so a name saying what the file is makes it findable later."
              >
                <input
                  className="input font-mono"
                  value={dest.smtpAttachName}
                  onChange={(e) => onChange({ smtpAttachName: e.target.value })}
                  placeholder="message.hl7"
                />
              </Field>
            )}

            <Field
              label="Encryption"
              hint="On unless the server genuinely cannot do it. Turning it off sends clinical content and any password in clear text, and the loader refuses a password without it rather than allowing that combination."
            >
              <Toggle
                label="Encrypt the connection with STARTTLS"
                checked={dest.smtpStartTLS}
                onChange={(v) => onChange({ smtpStartTLS: v })}
              />
            </Field>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Username" hint="Leave both empty if the server wants no login.">
                <input
                  className="input font-mono"
                  value={dest.smtpUsername}
                  onChange={(e) => onChange({ smtpUsername: e.target.value })}
                />
              </Field>
              <Field
                label="Password"
                hint="The connection is encrypted before this is sent. A password with encryption turned off is refused rather than allowed."
              >
                <input
                  className="input font-mono"
                  type="password"
                  value={dest.smtpPassword}
                  onChange={(e) => onChange({ smtpPassword: e.target.value })}
                  autoComplete="new-password"
                />
              </Field>
            </div>
          </>
        )}


        {dest.type === 'fhir' && (
          <>
            <Field
              label="FHIR server"
              hint="The base URL. A transaction bundle is posted to it directly."
            >
              <input
                className="input font-mono"
                value={dest.url}
                onChange={(e) => onChange({ url: e.target.value })}
                placeholder="https://fhir.internal/fhir"
              />
            </Field>
            <Field
              label="Release"
              hint="R4 unless you know otherwise: it is what US Core, and therefore most EHRs and the CMS rules, are built on. R5 is newer and almost nothing accepts it yet."
            >
              <select
                className="select"
                value={dest.fhirVersion}
                onChange={(e) => onChange({ fhirVersion: e.target.value })}
              >
                <option value="R4">R4 (4.0.1)</option>

                <option value="R4B">R4B (4.3.0)</option>

                <option value="R5">R5 (5.0.0)</option>
              </select>
            </Field>

            <Field
              label="Identifier systems"
              hint='One per line, written as "assigning authority: system URI". Without a system a medical record number is ambiguous between facilities, so a bundle built without one asserts an identity it cannot support — two patients from different hospitals with the same number become one patient.'
            >
              <textarea
                className="input font-mono"
                rows={3}
                value={dest.fhirIdentifierSystems}
                onChange={(e) => onChange({ fhirIdentifierSystems: e.target.value })}
                placeholder={'RGH: http://rgh.example/mrn\nSTMARY: http://stmary.example/mrn'}
              />
            </Field>

            <Field
              label="Time zone of the sending system"
              hint="Used to resolve an HL7 timestamp that carries no offset — most of them. Without it the reader assumes its own zone, so a result taken at 23:30 can be recorded on the wrong day, which is the kind of error that survives every later check."
            >
              <input
                className="input font-mono"
                value={dest.fhirTimezone}
                onChange={(e) => onChange({ fhirTimezone: e.target.value })}
                placeholder="America/Chicago"
              />
            </Field>

            <Field
              label="Claim US Core conformance"
              hint="Adds the US Core profile to each resource. Only claim it if the resource really carries what the profile requires, because a receiver may validate against the claim."
            >
              <label className="flex items-center gap-2 text-sm text-slate-300">
                <input
                  type="checkbox"
                  className="checkbox"
                  checked={dest.fhirClaimUSCore}
                  onChange={(e) => onChange({ fhirClaimUSCore: e.target.checked })}
                />
                Mark resources as conforming to US Core
              </label>
            </Field>

            <Field
              label="Validate before sending"
              hint="Checks each resource here rather than finding out from the receiver. A rejected bundle at the far end tells you less, later, and often only in a log you cannot read."
            >
              <label className="flex items-center gap-2 text-sm text-slate-300">
                <input
                  type="checkbox"
                  className="checkbox"
                  checked={dest.fhirValidateBeforeSend}
                  onChange={(e) => onChange({ fhirValidateBeforeSend: e.target.checked })}
                />
                Validate each resource before it is sent
              </label>
            </Field>

            <Field
              label="Treat warnings as rejections"
              hint="Only has an effect when validation is on. Strict enough to stop a bundle that would be accepted, so it is for a feed being brought up rather than one in service."
            >
              <label className="flex items-center gap-2 text-sm text-slate-300">
                <input
                  type="checkbox"
                  className="checkbox"
                  checked={dest.fhirRejectOnWarning}
                  disabled={!dest.fhirValidateBeforeSend}
                  onChange={(e) => onChange({ fhirRejectOnWarning: e.target.checked })}
                />
                Refuse a resource that only produces warnings
              </label>
            </Field>
          </>
        )}

        {dest.type === 'cda' && (
          <>
            <Field
              label="Emit"
              hint="The original document is the legal record; the bundle is what a system imports."
            >
              <select
                className="select"
                value={dest.cdaWrite}
                onChange={(e) => onChange({ cdaWrite: e.target.value as Destination['cdaWrite'] })}
              >
                <option value="fhir">The converted FHIR bundle</option>
                <option value="document">The original document, unchanged</option>
                <option value="both">Both</option>
              </select>
            </Field>

            {dest.cdaWrite !== 'document' && (
              <Field
                label="FHIR server"
                hint="Leave empty to write the bundle to the folder below instead."
              >
                <input
                  className="input font-mono"
                  value={dest.url}
                  onChange={(e) => onChange({ url: e.target.value })}
                  placeholder="https://fhir.internal/fhir"
                />
              </Field>
            )}

            <Field
              label="Folder"
              hint="One file per document, named after the document's own identifier."
            >
              <input
                className="input font-mono"
                value={dest.dir}
                onChange={(e) => onChange({ dir: e.target.value })}
                placeholder="./documents"
              />
            </Field>

            <Field
              label="A message with no document"
              hint="A mixed feed should skip. A channel dedicated to documents should fail, because that is a fault at the sender."
            >
              <select
                className="select"
                value={dest.cdaOnNoDocument}
                onChange={(e) =>
                  onChange({ cdaOnNoDocument: e.target.value as Destination['cdaOnNoDocument'] })
                }
              >
                <option value="skip">Skip it</option>
                <option value="fail">Treat it as a failure</option>
              </select>
            </Field>

            <div className="sm:col-span-2">
              <Toggle
                checked={dest.cdaRequireAgreement}
                onChange={(v) => onChange({ cdaRequireAgreement: v })}
                label="Refuse a document whose narrative and coded entries contradict each other"
              />
              <p className="mt-1 text-xs text-slate-400">
                Off by default. The check is a judgement about a document somebody else authored,
                and dropping real clinical documents because a sender's generator is sloppy is worse
                than passing them through with a warning. Disagreements are logged either way.
              </p>
            </div>
          </>
        )}

        <Field label="Give up after" hint="Attempts, including the first.">
          <input
            className="input"
            type="number"
            min={1}
            value={dest.retryAttempts}
            onChange={(e) => onChange({ retryAttempts: Number(e.target.value) })}
          />
        </Field>
      </div>

      {/* The response transformer, which had no control anywhere.
          
          It exists because "did this arrive" is often not a question the transport can answer: an MLLP receiver returns an
          application acknowledgement whose meaning is in its text, and an HTTP receiver returns 200 with an error document. In both
          cases the transport succeeded and the message did not arrive.
          
          Refused at load on a destination that receives no reply, so it is offered only where it can run - a control that saves a
          script which is then refused is worse than no control. */}
      {(dest.type === 'mllp' || dest.type === 'http' || dest.type === 'soap' || dest.type === 'fhir') && (
        <div className="mt-4">
          <Field
            label="Inspect the reply"
            hint="A script given the receiver's response. Return false, or throw, to mark the delivery failed. Leave it empty unless the receiver answers in a way the transport cannot judge."
          >
            <textarea
              className="input font-mono text-xs"
              rows={3}
              value={dest.responseTransformer}
              onChange={(e) => onChange({ responseTransformer: e.target.value })}
              placeholder={"// response holds what came back\nreturn !response.includes('MSA|AE')"}
            />
          </Field>
        </div>
      )}

      {/* The queue, which the builder could not write at all.
          
          Separate from the retry settings above and easy to confuse with them. Retry governs immediate attempts inside one delivery,
          for a fault that lasted milliseconds. The queue governs what happens once those are exhausted: the message leaves the
          delivery path and is retried later on a longer schedule, which is what a destination switched off for maintenance needs. */}
      <div className="mt-4 rounded-lg border border-slate-800 p-3">
        <label className="flex items-center gap-2 text-sm text-slate-300">
          <input
            type="checkbox"
            className="checkbox"
            checked={dest.queueEnabled}
            onChange={(e) => onChange({ queueEnabled: e.target.checked })}
          />
          Queue messages this destination could not deliver
        </label>
        <p className="mt-1 text-xs text-slate-500">
          Without this, a message that exhausts its immediate attempts is a failure and stays one. With it, the message waits and is
          tried again later — which is what a destination taken down for maintenance needs.
        </p>

        {dest.queueEnabled && (
          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            <Field label="Attempts from the queue" hint="Empty for the default. Counted separately from the immediate attempts above.">
              <input
                className="input"
                type="number"
                min={1}
                value={dest.queueMaxAttempts}
                onChange={(e) => onChange({ queueMaxAttempts: e.target.value })}
                placeholder="10"
              />
            </Field>

            <Field
              label="Wait before the first retry"
              hint="A duration: 30s, 5m. The wait grows between attempts, because a destination that is down is usually down for minutes and retrying every second buries the log entry that matters."
            >
              <input
                className="input font-mono"
                value={dest.queueBackoff}
                onChange={(e) => onChange({ queueBackoff: e.target.value })}
                placeholder="30s"
              />
            </Field>

            <Field label="Longest wait" hint="The ceiling on that growing wait.">
              <input
                className="input font-mono"
                value={dest.queueMaxBackoff}
                onChange={(e) => onChange({ queueMaxBackoff: e.target.value })}
                placeholder="15m"
              />
            </Field>

            <Field
              label="Most messages to hold"
              hint="Empty means no limit, and a queue with no limit grows until the filesystem fills — which arrives as something unrelated breaking rather than as a queue problem."
            >
              <input
                className="input"
                type="number"
                min={1}
                value={dest.queueMaxDepth}
                onChange={(e) => onChange({ queueMaxDepth: e.target.value })}
                placeholder="10000"
              />
            </Field>

            <Field
              label="Keep delivered messages for (hours)"
              hint="Empty keeps them indefinitely. The same disk problem as above, arriving more slowly."
            >
              <input
                className="input"
                type="number"
                min={1}
                value={dest.queueRetainHours}
                onChange={(e) => onChange({ queueRetainHours: e.target.value })}
                placeholder="72"
              />
            </Field>
          </div>
        )}
      </div>

      <div className="mt-4">
        <p className="label">Only send messages where…</p>
        {/* Same reasoning as the channel filter: show the expression rather than an empty rule list. */}
        {dest.rawFilter !== undefined ? (
          <RawFilter
            value={dest.rawFilter}
            onChange={(value) => onChange({ rawFilter: value || undefined })}
            onConvert={() => onChange({ rawFilter: undefined })}
          />
        ) : (
          <>
            <RuleBuilder
              rules={dest.rules}
              onChange={(rules) => onChange({ rules })}
              emptyHint="No extra conditions, so this destination gets everything the channel keeps."
            />

            {/* Same gap as the channel filter, same fix. */}
            <button
              type="button"
              className="btn-ghost mt-3 text-xs"
              onClick={() => onChange({ rawFilter: rulesToExpression(dest.rules) })}
            >
              Write this condition as an expression
            </button>
          </>
        )}
      </div>

      <Confirm
        open={confirming}
        title="Remove this destination?"
        body="Messages will no longer be sent there once you save."
        confirmLabel="Remove"
        onConfirm={() => {
          setConfirming(false)
          onRemove()
        }}
        onCancel={() => setConfirming(false)}
      />
    </div>
  )
}


/**
 * PlaceholderCheck warns when the statement and the values disagree.
 *
 * Shown here rather than left to the loader because a mismatch binds values to the
 * wrong columns, and the result is data that looks entirely valid. Catching it while
 * somebody is looking at both halves is much cheaper than catching it in a table.
 */
function PlaceholderCheck({
  statement,
  driver,
  count,
}: {
  statement: string
  driver: string
  count: number
}) {
  const placeholders =
    driver === 'postgres'
      ? new Set(statement.match(/\$\d+/g) ?? []).size
      : driver === 'sqlserver'
        ? new Set(statement.match(/@p\d+/gi) ?? []).size
        : (statement.match(/\?/g) ?? []).length

  if (placeholders === count) {
    return null
  }

  return (
    <p className="rounded-md border border-amber-900/60 bg-amber-950/25 p-2 text-xs text-amber-200">
      The statement has {placeholders} placeholder{placeholders === 1 ? '' : 's'} and you
      have listed {count} value{count === 1 ? '' : 's'}. A mismatch binds values to the
      wrong columns, which writes data that looks correct.
    </p>
  )
}

/** moveItem returns the list with one entry moved, or unchanged if out of range. */
function moveItem<T>(list: T[], from: number, to: number): T[] {
  if (to < 0 || to >= list.length) return list
  const next = list.slice()
  const [item] = next.splice(from, 1)
  next.splice(to, 0, item!)
  return next
}
