import { CodeArea } from './CodeArea'
import { useEffect, useState } from 'react'
import { api } from './api'
import type { ConversionResult, MessageView } from './api'
import { SyntaxBlock } from './SyntaxHighlight'
import { ParsedMessage } from './Messages'
import { ErrorBox, Field, Section, Spinner, Toggle } from './ui'
import { useCopy } from './useCopy'

/**
 * The FHIR lab.
 *
 * Paste an HL7 v2 message, see the FHIR it becomes, the decisions the mapper had
 * to make, and whether the result validates. That is the question every
 * interoperability project spends weeks answering with a spreadsheet, and nothing
 * here is stored.
 */

const sampleADT = [
  'MSH|^~\\&|SENDAPP|SITEA|RECVAPP|RECVFAC|20260818120000-0500||ADT^A01^ADT_A01|CTRL1|P|2.5.1',
  'EVN|A01|20260818115900-0500',
  'PID|1||MRN123456^^^SITEA^MR~999887777^^^SSA^SS||Doe^Jane^Q^^Ms.^^L||19800101|F|||123 Main St^Apt 4^Birmingham^AL^35205^USA^H||2055551234^^PH',
  'PV1|1|I|ICU^0201^01^SITEA||||1234^Attending^Adam^^^Dr.|||MED|||||||||V0012345^^^SITEA^VN|||||||||||||||||||||20260818120000-0500',
].join('\n')

const sampleORU = [
  'MSH|^~\\&|LAB|SITEA|EHR|RECVFAC|20260818130000-0500||ORU^R01^ORU_R01|CTRL2|P|2.5.1',
  'PID|1||MRN123456^^^SITEA^MR||Doe^Jane^Q||19800101|F',
  'OBR|1|ORD987|FILL654|CBC^Complete Blood Count^LN|||20260818113000-0500||||||||||||||||20260818125900-0500|||F',
  'OBX|1|NM|718-7^Hemoglobin^LN||13.5|g/dL|12.0-16.0|N|||F|||20260818120000-0500',
  'OBX|2|NM|6690-2^Leukocytes^LN||14.2|10*3/uL|4.0-11.0|H|||F|||20260818120000-0500',
  'OBX|3|ST|11156-7^Comment^LN||Specimen slightly hemolysed||||||F',
].join('\n')

export function FhirLab() {
  const { copy: copyBundle, label: copyLabel } = useCopy()

  const [message, setMessage] = useState(sampleADT)
  // Seeded from the server rather than hardcoded.
  //
  // This was 'R5', so the lab converted to R5 while the server's own stated default was R4 and its structs carried R4 field
  // spellings. The endpoint below reports the default; ignoring it and writing a release name here is how the two drifted.
  const [version, setVersion] = useState('')
  const [system, setSystem] = useState('http://example.org/mrn')
  const [usCore, setUsCore] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [result, setResult] = useState<ConversionResult | null>(null)
  const [parsed, setParsed] = useState<MessageView | null>(null)
  const [view, setView] = useState<'bundle' | 'notes' | 'v2'>('bundle')

  const [versions, setVersions] = useState<{ name: string; number: string; note: string }[]>([])
  const [versionNote, setVersionNote] = useState('')

  useEffect(() => {
    api
      .fhirVersions()
      .then((res) => {
        setVersions(res.versions)
        setVersionNote(res.note)
        // Only if the user has not already chosen, so a slow response cannot overwrite a deliberate choice.
        setVersion((current) => current || res.default)
      })
      .catch(() => {})
  }, [])

  async function convert() {
    setBusy(true)
    setError(null)
    try {
      const [conversion, inspection] = await Promise.all([
        api.convertToFHIR({ message, version, system, usCore }),
        api.inspectHL7(message).catch(() => null),
      ])
      setResult(conversion)
      setParsed(inspection?.parsed ?? null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'conversion failed')
      setResult(null)
    } finally {
      setBusy(false)
    }
  }

  const warnings = result?.notes.filter((n) => n.severity !== 'info') ?? []
  const infos = result?.notes.filter((n) => n.severity === 'info') ?? []

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-lg font-semibold text-slate-100">FHIR lab</h1>
        <p className="mt-1 max-w-3xl text-sm text-slate-500">
          Paste an HL7 v2 message and see exactly what it becomes, every decision the mapper made,
          and whether the result validates. Nothing here is stored.
        </p>
      </div>

      {error && <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />}

      <div className="grid gap-5 xl:grid-cols-2">
        <div className="space-y-4">
          <Section
            title="HL7 v2 message"
            actions={
              <div className="flex gap-2">
                <button className="btn-ghost py-1 text-xs" onClick={() => setMessage(sampleADT)}>
                  Sample ADT
                </button>
                <button className="btn-ghost py-1 text-xs" onClick={() => setMessage(sampleORU)}>
                  Sample lab result
                </button>
              </div>
            }
          >
            <CodeArea
            language="hl7"
              aria-label="HL7 v2 message to convert"
              className="min-h-64"
              value={message}
              onChange={setMessage}
              spellCheck={false}
              placeholder="Paste a message. Line feeds are fine; they are normalised to carriage returns."
            />
            <p className="mt-2 text-xs text-slate-400">
              Use synthetic data. This is a browser form, not a place for real patient information.
            </p>
          </Section>

          <Section title="How to convert it">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label="FHIR release"
                hint={
                  versions.find((v) => v.name === version)?.note ??
                  'R4 unless you know otherwise: it is what US Core, and therefore most EHRs, are built on.'
                }
              >
                <select
                  className="select"
                  value={version}
                  onChange={(e) => setVersion(e.target.value)}
                >
                  {versions.map((v) => (
                    <option key={v.name} value={v.name}>
                      {v.name} ({v.number})
                    </option>
                  ))}
                </select>
              </Field>

              <Field
                label="Identifier system"
                hint="Namespaces an MRN. Without one it is ambiguous between facilities."
              >
                <input
                  className="input font-mono"
                  value={system}
                  onChange={(e) => setSystem(e.target.value)}
                  placeholder="http://example.org/mrn"
                />
              </Field>
            </div>

            <div className="mt-4">
              <Toggle
                checked={usCore}
                onChange={setUsCore}
                label="Claim US Core profiles on the output"
              />
              <p className="mt-1.5 text-xs text-slate-400">
                Only worth doing once you have checked the output. Asserting a profile that does not
                hold is worse than asserting none.
              </p>
            </div>

            <button className="btn-primary mt-5 w-full" onClick={convert} disabled={busy}>
              {busy ? 'Converting…' : 'Convert to FHIR'}
            </button>

            {versionNote && <p className="mt-3 text-xs text-slate-400">{versionNote}</p>}
          </Section>
        </div>

        <div className="space-y-4">
          {busy && (
            <div className="card p-6">
              <Spinner label="Converting…" />
            </div>
          )}

          {result && (
            <>
              <div className="card p-4">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div>
                    <p className="label mb-1">Result</p>
                    <p className="text-sm text-slate-300">
                      {result.messageType}
                      {result.triggerEvent && `^${result.triggerEvent}`} → FHIR {result.version}
                    </p>
                  </div>
                  <div
                    className={`badge ${
                      result.validation.valid
                        ? 'border border-emerald-700 bg-emerald-950/40 text-emerald-300'
                        : 'border border-rose-700 bg-rose-950/40 text-rose-300'
                    }`}
                  >
                    {result.validation.valid ? 'Valid' : `${result.validation.errors} error(s)`}
                  </div>
                </div>

                <div className="mt-4 flex flex-wrap gap-2">
                  {Object.entries(result.counts).map(([type, count]) => (
                    <span
                      key={type}
                      className="badge border border-slate-700 bg-slate-800/60 text-slate-300"
                    >
                      {type}
                      <span className="font-mono text-slate-500">{count}</span>
                    </span>
                  ))}
                </div>

                <div className="mt-4 flex gap-4 border-t border-slate-800 pt-3 text-xs">
                  <span className={result.validation.errors > 0 ? 'text-rose-300' : 'text-slate-500'}>
                    {result.validation.errors} errors
                  </span>
                  <span className={warnings.length > 0 ? 'text-amber-300' : 'text-slate-500'}>
                    {warnings.length} mapping warnings
                  </span>
                  <span className="text-slate-500">{infos.length} notes</span>
                </div>
              </div>

              <div className="flex gap-1">
                {(
                  [
                    ['bundle', 'FHIR bundle'],
                    ['notes', `Decisions${warnings.length ? ` (${warnings.length})` : ''}`],
                    ['v2', 'v2 explained'],
                  ] as const
                ).map(([id, label]) => (
                  <button
                    key={id}
                    onClick={() => setView(id)}
                    className={`rounded-lg px-3 py-1.5 text-sm font-medium transition ${
                      view === id ? 'bg-slate-800 text-slate-100' : 'text-slate-500 hover:text-slate-300'
                    }`}
                  >
                    {label}
                  </button>
                ))}
              </div>

              {view === 'bundle' && (
                <div className="card overflow-hidden">
                  <div className="flex items-center justify-between border-b border-slate-800 px-4 py-2">
                    <span className="text-xs tracking-wide text-slate-500 uppercase">
                      Transaction bundle
                    </span>
                    <button
                      className="text-xs text-slate-500 hover:text-slate-300"
                      onClick={() =>
                        void copyBundle(JSON.stringify(result.bundle, null, 2))
                      }
                    >
                      {copyLabel ?? 'Copy'}
                    </button>
                  </div>
                  <SyntaxBlock code={JSON.stringify(result.bundle, null, 2)} language="json" maxHeight="36rem" />
                </div>
              )}

              {view === 'notes' && (
                <div className="space-y-4">
                  {result.validation.findings.length > 0 && (
                    <Section
                      title="Validation"
                      description="Errors mean a server would reject it. Warnings are profile judgements."
                    >
                      <ul className="space-y-2 text-sm">
                        {result.validation.findings.map((f, i) => (
                          <li key={i} className="flex gap-3">
                            <span
                              className={`badge shrink-0 ${
                                f.severity === 'error'
                                  ? 'border border-rose-800 bg-rose-950/40 text-rose-300'
                                  : f.severity === 'warning'
                                    ? 'border border-amber-800 bg-amber-950/40 text-amber-300'
                                    : 'border border-slate-700 bg-slate-800/60 text-slate-400'
                              }`}
                            >
                              {f.severity}
                            </span>
                            <div className="min-w-0">
                              <code className="font-mono text-xs text-sky-300">{f.path}</code>
                              <p className="text-slate-400">{f.message}</p>
                              <p className="text-xs text-slate-400">rule: {f.rule}</p>
                            </div>
                          </li>
                        ))}
                      </ul>
                    </Section>
                  )}

                  <Section
                    title="Mapping decisions"
                    description="Every judgement the mapper made, so the ones that matter can be checked instead of reading all the output."
                  >
                    {result.notes.length === 0 ? (
                      <p className="text-sm text-slate-500">
                        Nothing needed a judgement call: every value mapped directly.
                      </p>
                    ) : (
                      <ul className="space-y-2.5 text-sm">
                        {[...warnings, ...infos].map((n, i) => (
                          <li key={i} className="flex gap-3">
                            <span
                              className={`badge shrink-0 ${
                                n.severity === 'error'
                                  ? 'border border-rose-800 bg-rose-950/40 text-rose-300'
                                  : n.severity === 'warning'
                                    ? 'border border-amber-800 bg-amber-950/40 text-amber-300'
                                    : 'border border-slate-700 bg-slate-800/60 text-slate-400'
                              }`}
                            >
                              {n.severity}
                            </span>
                            <div className="min-w-0">
                              <p className="text-slate-300">{n.message}</p>
                              <p className="mt-0.5 text-xs text-slate-400">
                                {n.source && <code className="font-mono">{n.source}</code>}
                                {n.source && n.target && ' → '}
                                {n.target && <code className="font-mono">{n.target}</code>}
                              </p>
                            </div>
                          </li>
                        ))}
                      </ul>
                    )}
                  </Section>
                </div>
              )}

              {view === 'v2' && parsed && (
                <div className="card p-4">
                  <ParsedMessage view={parsed} />
                </div>
              )}
              {view === 'v2' && !parsed && (
                <p className="text-sm text-slate-500">The message could not be parsed.</p>
              )}
            </>
          )}

          {!result && !busy && (
            <div className="card p-10 text-center">
              <p className="text-slate-400">Paste a message and convert it.</p>
              <p className="mx-auto mt-2 max-w-md text-xs text-slate-400">
                You will get the FHIR bundle, every mapping decision that involved judgement, and
                the validation result for the release you picked.
              </p>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
