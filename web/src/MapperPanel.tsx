import { useEffect, useState } from 'react'
import { Section, Spinner, ErrorBox } from './ui'
import type { UiError } from './store'
import { toUiError } from './store'
import { api } from './api'

/**
 * AI-assisted mapping panel.
 *
 * The user enters source fields (from a feed they are integrating) and target fields
 * (what their system expects). The engine suggests mappings with confidence scores.
 * Below the threshold, it abstains rather than guessing wrong.
 *
 * No network calls to any AI service — this runs entirely on the server, using fuzzy
 * matching and pattern recognition. The "AI" is the confidence scoring and abstention.
 */

interface Suggestion {
  target: string
  confidence: number
  reasoning: string
  abstained: boolean
}

interface SuggestionGroup {
  sourceField: string
  suggestions: Suggestion[]
}

export function MapperPanel() {
  // The default now carries example values, because without them the engine abstains on everything and the panel's first impression
  // was that it never suggests anything.
  const [sourceFields, setSourceFields] = useState(
    'PatientMRN = MRN00412, MRN00998\nDateOfBirth = 19800101, 19750612\nPhoneNumber = 5551234567\nZPI-1',
  )
  const [targetFields, setTargetFields] = useState('PID-3.1 = mrn\nPID-7 = date\nPID-13 = phone\nPID-14')
  const [threshold, setThreshold] = useState(70)
  const [results, setResults] = useState<SuggestionGroup[] | null>(null)

  // Approval state, keyed by source and target together, because one source field can be offered several targets and approving one
  // must not mark the others.
  const [approved, setApproved] = useState<Record<string, boolean>>({})
  const [channel, setChannel] = useState('')
  const [channels, setChannels] = useState<string[]>([])
  const [applying, setApplying] = useState(false)
  const [applied, setApplied] = useState<string | null>(null)

  // Derived from the checkboxes rather than kept alongside them, so the two cannot disagree. An abstention is excluded here as well
  // as being unofferable in the interface, because a list that could contain one would eventually contain one.
  const approvedMappings = (results ?? []).flatMap((group) =>
    group.suggestions
      .filter((s) => isApprovable(s, threshold) && approved[`${group.sourceField}|${s.target}`] === true)
      .map((s) => ({
        source: group.sourceField,
        target: s.target,
        confidence: s.confidence,
        reasoning: s.reasoning,
        abstained: false,
      })),
  )

  useEffect(() => {
    let live = true
    api
      .listChannels()
      .then((res) => live && setChannels(res.channels.map((c) => c.name)))
      .catch(() => undefined)

    return () => {
      live = false
    }
  }, [])

  async function downloadRecipe() {
    setApplying(true)
    setApplied(null)
    setError(null)

    try {
      const downloaded = await api.recipeFromMappings({
        name: 'mapping-recipe',
        sourceSystem: '',
        targetSystem: '',
        mappings: approvedMappings,
      })

      const url = URL.createObjectURL(downloaded.blob)
      const a = document.createElement('a')
      a.href = url
      a.download = downloaded.filename
      a.click()
      URL.revokeObjectURL(url)

      setApplied(`${approvedMappings.length} mapping(s) written to a recipe file. Nothing on this server changed.`)
    } catch (e) {
      setError(toUiError(e))
    } finally {
      setApplying(false)
    }
  }

  async function apply() {
    setApplying(true)
    setApplied(null)
    setError(null)

    try {
      const res = await api.approveMappings({ channel, mappings: approvedMappings })
      setApplied(`${res.added.length} step(s) added to ${res.channel}. ${res.note}`)

      // Cleared, because leaving them ticked invites a second application of the same mappings - which would append duplicate steps
      // rather than fail, since a channel may legitimately copy one field twice.
      setApproved({})
    } catch (e) {
      setError(toUiError(e))
    } finally {
      setApplying(false)
    }
  }
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<UiError | null>(null)

  const suggest = async () => {
    setLoading(true)
    setError(null)

    // A source field may carry example values, written after an equals sign: "PatientMRN = MRN00412, MRN00998".
    //
    // The examples are what let the engine be confident about anything. Without them it scores on the name alone, and a name on its
    // own is weak evidence even when it matches exactly - PatientMRN against PatientMRN reaches only sixty-eight, which abstains at
    // the default threshold. So the panel used to abstain on almost everything, and the engine's confident path was unreachable from
    // the interface.
    const fields = sourceFields
      .split('\n')
      .map((s) => s.trim())
      .filter(Boolean)
      .map((line) => {
        const at = line.indexOf('=')
        if (at < 0) return { name: line, examples: [] as string[], codeSystem: '' }

        return {
          name: line.slice(0, at).trim(),
          examples: line
            .slice(at + 1)
            .split(',')
            .map((v) => v.trim())
            .filter(Boolean),
          codeSystem: '',
        }
      })
      .filter((f) => f.name !== '')

    // A target may name the kind of value it holds, after an equals sign: "PID-3.1 = mrn".
    //
    // A kind rather than a regular expression, which is what the engine's field means and what I got wrong first: passing a regex
    // made it an unrecognised kind, silently scoring nothing. The recognised kinds are mrn, npi, ssn, phone, date and email.
    //
    // Needed for the same reason the source examples are. Confidence is name similarity plus evidence that the values belong
    // together, and with no kind on the target there is nothing for the examples to agree with - so even PID-3.1 against PID-3.1
    // scores fifty and abstains. The panel sent neither, which is why it abstained on nearly everything.
    const targets = targetFields
      .split('\n')
      .map((s) => s.trim())
      .filter(Boolean)
      .map((line) => {
        const at = line.indexOf('=')
        if (at < 0) return { name: line, codeSystem: '', pattern: '' }

        return { name: line.slice(0, at).trim(), codeSystem: '', pattern: line.slice(at + 1).trim() }
      })
      .filter((f) => f.name !== '')

    try {
      const data = await api.suggestMappings({ fields, targets, threshold })
      setResults((data.suggestions ?? []) as SuggestionGroup[])
    } catch (e) {
      setError(toUiError(e))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Section
      title="AI mapping assistant"
      description="Enter source and target fields. The engine suggests mappings with confidence scores and abstains when it is not sure enough."
    >
      <div className="grid gap-4 md:grid-cols-2">
        <div>
          <label className="label" htmlFor="mapper-source-fields">
            Source fields (one per line)
          </label>
          <textarea
            id="mapper-source-fields"
            className="input font-mono text-xs"
            rows={6}
            value={sourceFields}
            onChange={(e) => setSourceFields(e.target.value)}
            placeholder="PatientMRN = MRN00412, MRN00998&#10;DateOfBirth = 19800101&#10;PhoneNumber"
          />
          <p className="mt-1 text-xs text-slate-500">
            Add example values after an equals sign, separated by commas. The engine checks them against the kind of value the
            target holds, and that check is usually what carries a suggestion over the threshold — a name alone rarely does, even
            when it matches exactly.
          </p>
        </div>
        <div>
          <label className="label" htmlFor="mapper-target-fields">
            Target fields (one per line)
          </label>
          <textarea
            id="mapper-target-fields"
            className="input font-mono text-xs"
            rows={6}
            value={targetFields}
            onChange={(e) => setTargetFields(e.target.value)}
            placeholder="PID-3.1 = mrn&#10;PID-7 = date&#10;PID-13 = phone"
          />
          <p className="mt-1 text-xs text-slate-500">
            Name the kind of value a target holds after an equals sign: <code className="font-mono">mrn</code>,{' '}
            <code className="font-mono">npi</code>, <code className="font-mono">ssn</code>,{' '}
            <code className="font-mono">phone</code>, <code className="font-mono">date</code> or{' '}
            <code className="font-mono">email</code>. Without one there is nothing for the source's examples to agree with, so a
            suggestion rests on the name alone — which is rarely enough to be sure of.
          </p>
        </div>
      </div>

      <div className="mt-4 flex items-center gap-4">
        <label className="text-sm text-slate-300">
          Confidence threshold:
          <input
            type="range"
            min={10}
            max={95}
            value={threshold}
            onChange={(e) => setThreshold(Number(e.target.value))}
            className="ml-2 align-middle accent-sky-500"
          />
          <span className="ml-2 font-mono text-sky-300">{threshold}%</span>
        </label>

        <button className="btn-primary" onClick={suggest} disabled={loading}>
          {loading ? <Spinner /> : 'Suggest mappings'}
        </button>
      </div>

      {error && <ErrorBox error={error} />}

      {results && results.length > 0 && (
        <div className="mt-6 space-y-3">
          {results.map((group) => (
            <div
              key={group.sourceField}
              className="rounded-lg border border-slate-800 bg-slate-900/60 p-4"
            >
              <h4 className="mb-2 font-mono text-sm font-medium text-slate-200">
                {group.sourceField}
              </h4>
              {group.suggestions.length === 0 && (
                <p className="text-xs text-slate-500 italic">No suggestions — the engine abstained entirely.</p>
              )}
              {group.suggestions.map((s, i) => (
                <div
                  key={i}
                  className={`mb-2 flex items-center gap-3 rounded-lg border p-3 ${
                    s.abstained
                      ? 'border-amber-800/50 bg-amber-950/20'
                      : s.confidence >= 85
                        ? 'border-emerald-800/50 bg-emerald-950/20'
                        : 'border-slate-700 bg-slate-950/40'
                  }`}
                >
                  <div className="flex-1">
                    <div className="flex items-center gap-2">
                      <span className="font-mono text-sm text-slate-100">{s.target}</span>
                      {s.abstained && (
                        <span className="badge bg-amber-900/60 text-amber-300">abstained</span>
                      )}
                    </div>
                    <p className="mt-0.5 text-xs text-slate-400">{s.reasoning}</p>

                    {/* Approval is per mapping and there is deliberately no way to accept several at once.
                        
                        A control that accepted everything above a threshold would undo the abstention design, because the number is
                        not the decision - the engine has already used it to decide whether to offer the mapping at all, and what
                        remains is a judgement about this field in this feed. */}
                    {s.abstained ? (
                      <p className="mt-1 text-xs text-amber-300/80">
                        The engine does not know, so this cannot be approved. Map it by hand if you know the answer.
                      </p>
                    ) : s.confidence < threshold ? (
                      <p className="mt-1 text-xs text-slate-500">
                        Below your threshold of {threshold}%, so it is shown for context but cannot be approved.
                      </p>
                    ) : (
                      <label className="mt-1 flex items-center gap-2 text-xs text-slate-300">
                        <input
                          type="checkbox"
                          className="checkbox"
                          checked={approved[`${group.sourceField}|${s.target}`] === true}
                          onChange={(e) =>
                            setApproved((prev) => ({
                              ...prev,
                              [`${group.sourceField}|${s.target}`]: e.target.checked,
                            }))
                          }
                        />
                        Approve this mapping
                      </label>
                    )}
                  </div>
                  <ConfidenceRing confidence={s.confidence} abstained={s.abstained} />
                </div>
              ))}
            </div>
          ))}

          {/* What to do with what was approved.
              
              Below the suggestions rather than above, because the decision follows the reading. The count is stated rather than
              implied: somebody who ticked three boxes and sees "apply 2" has found a bug in this screen, and somebody who sees
              "apply 3" knows what is about to happen. */}
          <div className="rounded-lg border border-slate-800 bg-slate-950/40 p-3">
            <p className="text-sm text-slate-300">
              {approvedMappings.length === 0
                ? 'Nothing approved yet.'
                : `${approvedMappings.length} mapping${approvedMappings.length === 1 ? '' : 's'} approved.`}
            </p>
            <p className="mt-1 text-xs text-slate-500">
              Approved mappings become copy steps on a channel. They are written to the channel file and take effect when it next
              loads — nothing changes in the traffic until then.
            </p>

            {applied !== null && (
              <p role="status" className="mt-2 rounded-md border border-emerald-500/40 bg-emerald-500/10 p-2 text-xs text-emerald-200">
                {applied}
              </p>
            )}

            <div className="mt-3 flex flex-wrap items-end gap-2">
              <label className="block text-xs">
                <span className="mb-1 block text-slate-400">Add them to</span>
                <select className="select" value={channel} onChange={(e) => setChannel(e.target.value)}>
                  <option value="">choose a channel…</option>
                  {channels.map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </select>
              </label>

              <button
                className="btn-primary"
                disabled={applying || channel === '' || approvedMappings.length === 0}
                onClick={() => void apply()}
              >
                Add these steps
              </button>

              {channel === '' && approvedMappings.length > 0 && (
                <span className="text-xs text-slate-600">Choose a channel first.</span>
              )}

              {/* The recipe, which is the right artefact for the commonest case.
                  
                  A step copies between two paths inside one message, so it can only express a mapping whose source is itself a path.
                  Most mappings here are not: they come from a column name or a vendor's own field name, which a channel cannot read
                  directly. Those are real mappings and a recipe is where they belong, so the option sits beside the other one rather
                  than appearing only after a refusal. */}
              <button
                className="btn-secondary"
                disabled={applying || approvedMappings.length === 0}
                onClick={() => void downloadRecipe()}
              >
                Download as a recipe
              </button>
            </div>

            <p className="mt-2 text-xs text-slate-500">
              A step can only be added when the source is a path in the message, such as <code className="font-mono">ZPI-1</code>.
              Field names from another system — a column, a JSON key — cannot be read by a channel directly, so those go into a
              recipe.
            </p>

          </div>
        </div>
      )}
    </Section>
  )
}

/** isApprovable reports whether a suggestion may be approved.
 *
 * Two conditions, and the second is easy to miss. Abstained is a property of the source field rather than of one suggestion: it is set
 * on all of them only when the best is below the threshold, so once the best clears it every also-ran carries abstained false. A
 * suggestion scoring 46 then arrives looking exactly as endorsed as one scoring 77.
 *
 * That was invisible while the panel abstained on everything, which it did until the engine was taught what an HL7 path means. Fixing
 * one thing is what made the other reachable.
 */
function isApprovable(s: Suggestion, threshold: number): boolean {
  return !s.abstained && s.confidence >= threshold
}

/** A small circular confidence indicator. */
function ConfidenceRing({ confidence, abstained }: { confidence: number; abstained: boolean }) {
  const radius = 18
  const circumference = 2 * Math.PI * radius
  const offset = circumference - (confidence / 100) * circumference

  const color = abstained
    ? '#f59e0b'
    : confidence >= 85
      ? '#10b981'
      : confidence >= 70
        ? '#38bdf8'
        : '#64748b'

  return (
    <div className="relative flex size-12 shrink-0 items-center justify-center">
      <svg className="size-12 -rotate-90" viewBox="0 0 44 44">
        <circle cx="22" cy="22" r={radius} fill="none" stroke="#1e293b" strokeWidth="3" />
        <circle
          cx="22"
          cy="22"
          r={radius}
          fill="none"
          stroke={color}
          strokeWidth="3"
          strokeDasharray={circumference}
          strokeDashoffset={offset}
          strokeLinecap="round"
          className="transition-all duration-500"
        />
      </svg>
      <span className="absolute text-[10px] font-bold" style={{ color }}>
        {confidence}
      </span>
    </div>
  )
}
