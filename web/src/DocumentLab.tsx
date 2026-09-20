import { CodeArea } from './CodeArea'
import { useEffect, useState } from 'react'
import { api } from './api'
import type { DocumentInspection, CdaSection, CdaEntry, CdaFinding } from './api'
import { ErrorBox, Field, Section, Spinner, Toggle } from './ui'
import { SyntaxBlock } from './SyntaxHighlight'
import { useCopy } from './useCopy'
import { RepairPanel } from './RepairPanel'
import { ReconcilePanel } from './ReconcilePanel'
import { PrintPanel } from './PrintPanel'
import { SignaturePanel } from './SignaturePanel'
import { MergePanel } from './MergePanel'

/**
 * The document lab.
 *
 * Paste a clinical document, or the HL7 message carrying one, and see what it
 * contains: sections named in English rather than by template OID, the narrative
 * beside the coded entries, and whether the two agree.
 *
 * The agreement panel is the reason this screen exists. Everything else here is a
 * nicer view of what other tools show; that check is something nothing else does.
 */

const sampleMDM = [
  'MSH|^~\\&|EHR|SITEA|ARCHIVE|RFAC|20260818130000-0500||MDM^T02^MDM_T02|MD1|P|2.5.1',
  'EVN|T02|20260818130000-0500',
  'PID|1||MRN900^^^SITEA^MR||Frost^Ivy^L||19910228|F',
  'TXA|1|DS|AP^application^HL7|20260818125500-0500||||||1234^Shaw^Sam||DOC-1^SITEA|PARENT-DOC-0^SITEA||||AU||AV',
  'OBX|1|ED|34133-9^Summary^LN||^application/hl7-cda+xml^^Base64^PASTE_BASE64_HERE||||||F',
].join('\n')

const sampleCDA = `<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <templateId root="2.16.840.1.113883.10.20.22.1.1"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <id root="2.16.840.1.113883.19.5.99999.1" extension="DOC-1"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1" displayName="Summarization of Episode Note"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="20260818120000-0500"/>
  <confidentialityCode code="N" codeSystem="2.16.840.1.113883.5.25"/>
  <recordTarget><patientRole>
    <id root="2.16.840.1.113883.19.5.99999.2" extension="MRN900"/>
    <addr use="HP"><streetAddressLine>4 Elm Rd</streetAddressLine>
      <city>Vestavia</city><state>AL</state><postalCode>35216</postalCode></addr>
    <patient>
      <name><given>Ivy</given><family>Frost</family></name>
      <administrativeGenderCode code="F" codeSystem="2.16.840.1.113883.5.1"/>
      <birthTime value="19910228"/>
    </patient>
  </patientRole></recordTarget>
  <component><structuredBody>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.6.1"/>
      <code code="48765-2" codeSystem="2.16.840.1.113883.6.1" displayName="Allergies"/>
      <title>Allergies and Adverse Reactions</title>
      <text><table><thead><tr><th>Substance</th><th>Reaction</th></tr></thead><tbody>
        <tr><td ID="a1">Penicillin</td><td>Hives</td></tr>
        <tr><td ID="a2">Sulfa</td><td>Rash</td></tr>
      </tbody></table></text>
      <entry><act classCode="ACT" moodCode="EVN">
        <statusCode code="active"/>
        <entryRelationship typeCode="SUBJ"><observation classCode="OBS" moodCode="EVN">
          <templateId root="2.16.840.1.113883.10.20.22.4.7"/>
          <code code="ASSERTION" codeSystem="2.16.840.1.113883.5.4"/>
          <statusCode code="completed"/>
          <value code="7980" codeSystem="2.16.840.1.113883.6.88" displayName="Penicillin"/>
          <text><reference value="#a1"/></text>
        </observation></entryRelationship>
      </act></entry>
    </section></component>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.3.1"/>
      <code code="30954-2" codeSystem="2.16.840.1.113883.6.1" displayName="Results"/>
      <title>Results</title>
      <text><table><tbody>
        <tr><td>Hemoglobin</td><td>13.5 g/dL</td></tr>
      </tbody></table></text>
      <entry><observation classCode="OBS" moodCode="EVN">
        <code code="718-7" codeSystem="2.16.840.1.113883.6.1" displayName="Hemoglobin"/>
        <statusCode code="completed"/>
        <effectiveTime value="20260818090000-0500"/>
        <value value="13.5" unit="g/dL"/>
      </observation></entry>
    </section></component>
  </structuredBody></component>
</ClinicalDocument>`

export function DocumentLab() {
  // Reports whether the copy actually happened; the API is absent over plain HTTP.
  const { copy: copyBundle, label: copyLabel } = useCopy()

  const [mode, setMode] = useState<'document' | 'message'>('document')
  const [text, setText] = useState(sampleCDA)
  // Seeded from the server, like the FHIR lab, rather than hardcoded here.
  //
  // This panel held its own copy of both the default and the option list, so it offered R5 first and preselected it while the
  // server's default was R4. Three copies of the same list is how they disagree.
  const [version, setVersion] = useState('')
  const [versions, setVersions] = useState<{ name: string; number: string }[]>([])

  useEffect(() => {
    api
      .fhirVersions()
      .then((res) => {
        setVersions(res.versions)
        setVersion((current) => current || res.default)
      })
      .catch(() => {})
  }, [])
  const [convert, setConvert] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<DocumentInspection | null>(null)
  const [view, setView] = useState<
    | 'sections'
    | 'agreement'
    | 'conformance'
    | 'repair'
    | 'reconcile'
    | 'merge'
    | 'print'
    | 'signature'
    | 'fhir'
    | 'notes'
  >('sections')

  async function inspect() {
    setBusy(true)
    setError(null)
    try {
      const res = await api.inspectDocument({
        document: mode === 'document' ? text : '',
        message: mode === 'message' ? text : '',
        version,
        convert,
      })
      setResult(res)
      if (res.found && (res.agreement?.errors ?? 0) > 0) {
        // Open on the problem. Somebody who pasted a document with a
        // contradiction in it should not have to go looking for it.
        setView('agreement')
      } else {
        setView('sections')
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not read that document')
      setResult(null)
    } finally {
      setBusy(false)
    }
  }

  const doc = result?.document
  const agreement = result?.agreement
  const validation = result?.validation

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-lg font-semibold text-slate-100">Documents</h1>
        <p className="mt-1 max-w-3xl text-sm text-slate-500">
          Read a clinical document, check that its narrative and its coded entries agree, and convert
          it to FHIR. Paste the document, or the HL7 message carrying it — which is how they
          normally arrive. Nothing here is stored.
        </p>
      </div>

      {error && (
        <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />
      )}

      <div className="grid gap-5 xl:grid-cols-2">
        <div className="space-y-4">
          <Section
            title="Input"
            actions={
              <div className="flex gap-2">
                <button
                  className="btn-ghost py-1 text-xs"
                  onClick={() => {
                    setMode('document')
                    setText(sampleCDA)
                  }}
                >
                  Sample document
                </button>
                <button
                  className="btn-ghost py-1 text-xs"
                  onClick={() => {
                    setMode('message')
                    setText(sampleMDM)
                  }}
                >
                  Sample MDM
                </button>
              </div>
            }
          >
            <div className="mb-3 flex rounded-lg border border-slate-800 p-0.5">
              {(
                [
                  ['document', 'Clinical document (XML)'],
                  ['message', 'HL7 message carrying one'],
                ] as const
              ).map(([id, label]) => (
                <button
                  key={id}
                  onClick={() => setMode(id)}
                  className={`flex-1 rounded-md px-3 py-1.5 text-xs font-medium transition ${
                    mode === id ? 'bg-slate-800 text-slate-100' : 'text-slate-500 hover:text-slate-300'
                  }`}
                >
                  {label}
                </button>
              ))}
            </div>

            <CodeArea
              aria-label="Clinical document or HL7 message to read"
              className="min-h-72"
              value={text}
              onChange={setText}
              spellCheck={false}
            />
            <p className="mt-2 text-xs text-slate-400">
              Use synthetic data. This is a browser form, not a place for real patient information.
            </p>
          </Section>

          <Section title="Options">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label="FHIR release"
                hint="R4 unless you know otherwise: it is what US Core, and therefore most EHRs, are built on."
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
              <div className="flex items-end">
                <Toggle checked={convert} onChange={setConvert} label="Also convert to FHIR" />
              </div>
            </div>

            <button className="btn-primary mt-5 w-full" onClick={inspect} disabled={busy}>
              {busy ? 'Reading…' : 'Read the document'}
            </button>
          </Section>
        </div>

        <div className="space-y-4">
          {busy && (
            <div className="card p-6">
              <Spinner label="Reading…" />
            </div>
          )}

          {result && !result.found && (
            <div className="card p-5">
              <p className="font-medium text-amber-300">No clinical document found</p>
              <p className="mt-1 text-sm text-slate-400">{result.message}</p>
              {result.attachments && result.attachments.length > 0 && (
                <div className="mt-4">
                  <p className="label mb-2">What the message did carry</p>
                  <ul className="space-y-2">
                    {result.attachments.map((a) => (
                      <li key={a.segment} className="rounded-md border border-slate-800 p-3 text-sm">
                        <p className="text-slate-300">
                          OBX {a.segment} — {a.mimeType || 'no declared type'} ·{' '}
                          {a.bytes.toLocaleString()} bytes
                          {a.encoding && ` · ${a.encoding}`}
                        </p>
                        {a.notes?.map((n, i) => (
                          <p key={i} className="mt-1 text-xs text-amber-300/80">
                            {n}
                          </p>
                        ))}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </div>
          )}

          {result?.found && doc && (
            <>
              <div className="card p-4">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0">
                    <p className="label mb-1">{doc.documentType || 'Clinical document'}</p>
                    <h2 className="truncate text-base font-semibold text-slate-100">
                      {doc.title || 'Untitled'}
                    </h2>
                    <p className="mt-1 text-sm text-slate-400">
                      {doc.patient.family || doc.patient.given
                        ? `${(doc.patient.given ?? []).join(' ')} ${doc.patient.family ?? ''}`.trim()
                        : 'no patient named'}
                      {doc.patient.birthTime && ` · born ${formatCdaDate(doc.patient.birthTime)}`}
                      {doc.patient.genderName && ` · ${doc.patient.genderName}`}
                    </p>
                  </div>

                  <div className="flex flex-col items-end gap-1.5">
                    <AgreementBadge agreement={agreement} />
                    {doc.version && (
                      <span className="text-xs text-slate-400">version {doc.version}</span>
                    )}
                  </div>
                </div>

                <dl className="mt-4 grid grid-cols-2 gap-3 border-t border-slate-800 pt-3 text-xs sm:grid-cols-4">
                  <Fact label="Effective" value={formatCdaDate(doc.effectiveTime)} />
                  <Fact label="Sections" value={String(doc.sections?.length ?? 0)} />
                  <Fact label="Confidentiality" value={doc.confidentiality} />
                  <Fact label="Custodian" value={doc.custodian} />
                </dl>

                {doc.patient.identifiers && doc.patient.identifiers.length > 0 && (
                  <div className="mt-3 border-t border-slate-800 pt-3">
                    <p className="label mb-1.5">Identifiers</p>
                    <ul className="space-y-1 text-xs">
                      {doc.patient.identifiers.map((id, i) => (
                        <li key={i} className="flex flex-wrap items-baseline gap-2">
                          <span className="font-mono text-slate-200">{id.extension}</span>
                          <span className="text-slate-400">
                            {id.assigner || id.root || 'no assigning authority'}
                          </span>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}

                {result.transport && (
                  <div className="mt-3 border-t border-slate-800 pt-3 text-xs">
                    <p className="label mb-1.5">From the HL7 message</p>
                    <p className="text-slate-400">
                      {result.transport.info.uniqueID && (
                        <>
                          document <span className="font-mono">{result.transport.info.uniqueID}</span>
                        </>
                      )}
                      {result.transport.statusMeaning && ` · ${result.transport.statusMeaning}`}
                    </p>
                    {result.transport.replaces && (
                      <p className="mt-1.5 rounded-md border border-amber-900/60 bg-amber-950/30 p-2 text-amber-200">
                        This document replaces{' '}
                        <span className="font-mono">{result.transport.info.parentID}</span>. A
                        receiving system that files both would hold two contradictory versions with
                        nothing to say which is current.
                      </p>
                    )}
                  </div>
                )}
              </div>

              <div className="flex flex-wrap gap-1">
                {(
                  [
                    ['sections', `Sections (${doc.sections?.length ?? 0})`],
                    [
                      'agreement',
                      `Agreement${
                        agreement && agreement.findings.length > 0
                          ? ` (${agreement.findings.length})`
                          : ''
                      }`,
                    ],
                    [
                      'conformance',
                      `Conformance${
                        validation && validation.findings.length > 0
                          ? ` (${validation.findings.length})`
                          : ''
                      }`,
                    ],
                    ['repair', 'What a reader sees'],
                    ['reconcile', 'Compare medications'],
                    ['merge', 'Combine sources'],
                    ['print', 'Print'],
                    ['signature', 'Signature'],
                    ['fhir', 'FHIR'],
                    ['notes', `Notes${result.notes?.length ? ` (${result.notes.length})` : ''}`],
                  ] as const
                ).map(([id, label]) => (
                  <button
                    key={id}
                    onClick={() => setView(id)}
                    className={`rounded-lg px-3 py-1.5 text-sm font-medium transition ${
                      view === id
                        ? 'bg-slate-800 text-slate-100'
                        : 'text-slate-500 hover:text-slate-300'
                    }`}
                  >
                    {label}
                  </button>
                ))}
              </div>

              {view === 'sections' && (
                <div className="space-y-3">
                  {(doc.sections ?? []).map((section, i) => (
                    <SectionBlock key={i} section={section} />
                  ))}
                </div>
              )}

              {view === 'agreement' && <AgreementPanel agreement={agreement} />}

              {view === 'conformance' && <ConformancePanel validation={validation} />}

              {/* Passed the raw text rather than the parsed document, because the repair is regenerated
                  server-side from the original bytes. Sending back a parsed and re-serialised document would mean
                  repairing something the person never supplied. */}
              {view === 'repair' && <RepairPanel document={text} />}

              {/* The document on screen is the later list; the panel asks for the earlier one. */}
              {view === 'reconcile' && <ReconcilePanel document={text} />}

              {view === 'merge' && <MergePanel document={text} />}

              {view === 'print' && <PrintPanel document={text} />}

              {view === 'signature' && <SignaturePanel document={text} />}

              {view === 'fhir' && (
                <div className="space-y-4">
                  {result.conversionError && (
                    <div className="rounded-lg border border-rose-900/60 bg-rose-950/30 p-3 text-sm text-rose-200">
                      {result.conversionError}
                    </div>
                  )}
                  {result.fhir ? (
                    <>
                      <div className="card p-4">
                        <p className="label mb-3">Resources produced</p>
                        <div className="flex flex-wrap gap-2">
                          {Object.entries(result.fhir.counts).map(([type, count]) => (
                            <span
                              key={type}
                              className="badge border border-slate-700 bg-slate-800/60 text-slate-300"
                            >
                              {type}
                              <span className="font-mono text-slate-500">{count}</span>
                            </span>
                          ))}
                        </div>
                      </div>

                      {result.fhir.notes?.length > 0 && (
                        <Section
                          title="Conversion decisions"
                          description="Every judgement the conversion made, so the ones that matter can be checked."
                        >
                          <NoteList notes={result.fhir.notes} />
                        </Section>
                      )}

                      <div className="card overflow-hidden">
                        <div className="flex items-center justify-between border-b border-slate-800 px-4 py-2">
                          <span className="text-xs tracking-wide text-slate-500 uppercase">
                            Transaction bundle
                          </span>
                          <button
                            className="text-xs text-slate-500 hover:text-slate-300"
                            onClick={() =>
                              void copyBundle(JSON.stringify(result.fhir!.bundle, null, 2))
                            }
                          >
                            {copyLabel ?? 'Copy'}
                          </button>
                        </div>
                        <SyntaxBlock code={JSON.stringify(result.fhir.bundle, null, 2)} language="json" />
                      </div>
                    </>
                  ) : (
                    <p className="text-sm text-slate-500">
                      Turn on “Also convert to FHIR” and read the document again.
                    </p>
                  )}
                </div>
              )}

              {view === 'notes' && (
                <Section
                  title="What the reader noticed"
                  description="Problems with the document itself, as distinct from disagreements between its two halves."
                >
                  {result.notes?.length ? (
                    <NoteList notes={result.notes} />
                  ) : (
                    <p className="text-sm text-slate-500">Nothing to report.</p>
                  )}
                </Section>
              )}
            </>
          )}

          {!result && !busy && (
            <div className="card p-10 text-center">
              <p className="text-slate-400">Paste a document and read it.</p>
              <p className="mx-auto mt-2 max-w-md text-xs text-slate-400">
                The sample has a deliberate defect: the narrative lists two allergies and the coded
                entries carry one. That is the failure the agreement check exists to find, and
                nothing else looks for it.
              </p>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

function Fact({ label, value }: { label: string; value?: string }) {
  return (
    <div>
      <dt className="text-[10px] tracking-wide text-slate-400 uppercase">{label}</dt>
      <dd className="mt-0.5 truncate text-slate-300" title={value}>
        {value || '—'}
      </dd>
    </div>
  )
}

function AgreementBadge({ agreement }: { agreement?: DocumentInspection['agreement'] }) {
  if (!agreement) return null

  if (agreement.errors > 0) {
    return (
      <span className="badge border border-rose-700 bg-rose-950/50 text-rose-300">
        {agreement.errors} contradiction{agreement.errors === 1 ? '' : 's'}
      </span>
    )
  }
  if (agreement.warnings > 0) {
    return (
      <span className="badge border border-amber-700 bg-amber-950/50 text-amber-300">
        {agreement.warnings} to check
      </span>
    )
  }
  return (
    <span className="badge border border-emerald-700 bg-emerald-950/50 text-emerald-300">
      Narrative and codes agree
    </span>
  )
}

function AgreementPanel({ agreement }: { agreement?: DocumentInspection['agreement'] }) {
  if (!agreement) {
    return <p className="text-sm text-slate-500">The document was not checked.</p>
  }

  const errors = agreement.findings.filter((f) => f.severity === 'error')
  const warnings = agreement.findings.filter((f) => f.severity === 'warning')
  const info = agreement.findings.filter((f) => f.severity === 'info')

  return (
    <div className="space-y-4">
      <div className="card p-4">
        <p className="text-sm text-slate-300">
          Every section of a clinical document carries its content twice: a narrative a clinician
          reads, and coded entries a receiving system imports. They are supposed to say the same
          thing. Nothing in the usual toolchain checks that they do.
        </p>
        <div className="mt-3 flex flex-wrap gap-4 border-t border-slate-800 pt-3 text-xs">
          <span className="text-slate-500">{agreement.checked} sections compared</span>
          {agreement.skipped > 0 && (
            <span className="text-slate-400">{agreement.skipped} skipped</span>
          )}
          <span className={errors.length > 0 ? 'text-rose-300' : 'text-slate-400'}>
            {errors.length} contradictions
          </span>
          <span className={warnings.length > 0 ? 'text-amber-300' : 'text-slate-400'}>
            {warnings.length} worth checking
          </span>
        </div>
      </div>

      {agreement.findings.length === 0 && (
        <div className="rounded-lg border border-emerald-900/60 bg-emerald-950/30 p-4 text-sm text-emerald-200">
          The narrative and the coded entries agree throughout.
        </div>
      )}

      {[
        ['Contradictions', errors, 'rose'],
        ['Worth checking', warnings, 'amber'],
        ['Notes', info, 'slate'],
      ].map(([title, list, tone]) => {
        const findings = list as CdaFinding[]
        if (findings.length === 0) return null
        return (
          <Section key={title as string} title={title as string}>
            <ul className="space-y-3">
              {findings.map((f, i) => (
                <li
                  key={i}
                  className={`rounded-lg border p-3 ${
                    tone === 'rose'
                      ? 'border-rose-900/60 bg-rose-950/20'
                      : tone === 'amber'
                        ? 'border-amber-900/60 bg-amber-950/20'
                        : 'border-slate-800 bg-slate-900/40'
                  }`}
                >
                  <div className="flex items-baseline gap-2">
                    <span className="text-sm font-medium text-slate-200">{f.section}</span>
                    <span className="font-mono text-[10px] text-slate-400">{f.kind}</span>
                  </div>
                  <p className="mt-1 text-sm text-slate-300">{f.message}</p>

                  {(f.narrative || f.coded) && (
                    <div className="mt-2.5 grid gap-2 text-xs sm:grid-cols-2">
                      {f.narrative && (
                        <div className="rounded-md border border-slate-800 bg-slate-950/60 p-2">
                          <p className="mb-1 text-[10px] tracking-wide text-slate-400 uppercase">
                            What a clinician reads
                          </p>
                          <p className="whitespace-pre-wrap text-slate-400">{f.narrative}</p>
                        </div>
                      )}
                      {f.coded && (
                        <div className="rounded-md border border-slate-800 bg-slate-950/60 p-2">
                          <p className="mb-1 text-[10px] tracking-wide text-slate-400 uppercase">
                            What a system imports
                          </p>
                          <p className="text-slate-400">{f.coded}</p>
                        </div>
                      )}
                    </div>
                  )}
                </li>
              ))}
            </ul>
          </Section>
        )
      })}

      {agreement.reasons && agreement.reasons.length > 0 && (
        <details className="card p-4 text-xs">
          <summary className="cursor-pointer text-slate-400">
            Why {agreement.skipped} section{agreement.skipped === 1 ? ' was' : 's were'} not compared
          </summary>
          <ul className="mt-2 space-y-1 text-slate-400">
            {agreement.reasons.map((r, i) => (
              <li key={i}>{r}</li>
            ))}
          </ul>
        </details>
      )}
    </div>
  )
}

const sectionTone: Record<string, string> = {
  Allergies: 'border-rose-800 bg-rose-950/20',
  Medications: 'border-violet-800 bg-violet-950/20',
  'Discharge medications': 'border-violet-800 bg-violet-950/20',
  Problems: 'border-amber-800 bg-amber-950/20',
  Results: 'border-cyan-800 bg-cyan-950/20',
  'Vital signs': 'border-teal-800 bg-teal-950/20',
  Procedures: 'border-indigo-800 bg-indigo-950/20',
  Immunisations: 'border-emerald-800 bg-emerald-950/20',
}

function SectionBlock({ section }: { section: CdaSection }) {
  const [open, setOpen] = useState(true)
  const [showNarrative, setShowNarrative] = useState(true)

  const tone = sectionTone[section.kind ?? ''] ?? 'border-slate-700 bg-slate-900/40'

  return (
    <div className={`overflow-hidden rounded-lg border ${tone}`}>
      <button
        className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition hover:bg-white/5"
        onClick={() => setOpen((v) => !v)}
      >
        <span className="font-semibold text-slate-100">{section.title || section.kind || 'Section'}</span>
        {section.kind && section.kind !== section.title && (
          <span className="badge border border-slate-700 bg-slate-800/60 text-slate-400">
            {section.kind}
          </span>
        )}
        {!section.kind && (
          <span className="badge border border-amber-800 bg-amber-950/40 text-amber-300">
            not recognised
          </span>
        )}
        {section.nilFlavor && (
          <span className="badge border border-slate-700 bg-slate-800/60 text-slate-400">
            explicitly absent
          </span>
        )}
        <span className="ml-auto shrink-0 text-xs text-slate-400">
          {section.entries?.length ?? 0} coded entr{(section.entries?.length ?? 0) === 1 ? 'y' : 'ies'}
        </span>
        <span className="text-slate-400">{open ? '−' : '+'}</span>
      </button>

      {open && (
        <div className="border-t border-slate-800/80 bg-slate-950/50 p-4">
          {section.empty && !section.nilFlavor && (
            <p className="mb-3 rounded-md border border-amber-900/60 bg-amber-950/20 p-2 text-xs text-amber-200">
              This section is empty and does not say why. An explicit nullFlavor would distinguish
              “nothing to report” from “not assessed”.
            </p>
          )}

          <div className="mb-3 flex gap-1 text-xs">
            <button
              onClick={() => setShowNarrative(true)}
              className={`rounded px-2 py-1 ${
                showNarrative ? 'bg-slate-800 text-slate-200' : 'text-slate-500'
              }`}
            >
              What a clinician reads
            </button>
            <button
              onClick={() => setShowNarrative(false)}
              className={`rounded px-2 py-1 ${
                !showNarrative ? 'bg-slate-800 text-slate-200' : 'text-slate-500'
              }`}
            >
              What a system imports
            </button>
          </div>

          {showNarrative ? (
            section.narrativeHtml ? (
              <div
                className="cda-narrative text-sm text-slate-300"
                // The server strips every element and attribute except a fixed
                // safe set, because a clinical document arrives from outside the
                // organisation and its markup cannot be trusted.
                dangerouslySetInnerHTML={{ __html: section.narrativeHtml }}
              />
            ) : (
              <p className="text-sm text-slate-400">No narrative.</p>
            )
          ) : section.entries?.length ? (
            <ul className="space-y-2">
              {section.entries.map((entry, i) => (
                <EntryRow key={i} entry={entry} depth={0} />
              ))}
            </ul>
          ) : (
            <p className="text-sm text-slate-400">No coded entries.</p>
          )}

          {section.code && (
            <p className="mt-3 border-t border-slate-800 pt-2 font-mono text-[10px] text-slate-400">
              {section.code} · {section.codeSystem}
            </p>
          )}
        </div>
      )}
    </div>
  )
}

function EntryRow({ entry, depth }: { entry: CdaEntry; depth: number }) {
  return (
    <li style={{ marginLeft: depth * 14 }}>
      <div className="flex flex-wrap items-baseline gap-2 text-sm">
        {entry.negationInd && (
          <span className="badge border border-rose-700 bg-rose-950/50 text-rose-300">NOT</span>
        )}
        <span className="text-slate-200">{describeEntry(entry)}</span>
        {entry.code && (
          <span className="font-mono text-[10px] text-slate-400">
            {entry.code}
            {entry.codeSystem && ` · ${entry.codeSystem}`}
          </span>
        )}
        {entry.statusCode && (
          <span className="badge border border-slate-700 bg-slate-800/60 text-slate-500">
            {entry.statusCode}
          </span>
        )}
        {entry.effectiveTime && (
          <span className="ml-auto text-xs text-slate-400">
            {formatCdaDate(entry.effectiveTime)}
          </span>
        )}
      </div>

      {entry.text && (
        <p className="mt-0.5 text-xs text-slate-500">links to narrative: “{entry.text}”</p>
      )}

      {entry.children && entry.children.length > 0 && (
        <ul className="mt-1.5 space-y-1.5 border-l border-slate-800 pl-3">
          {entry.children.map((child, i) => (
            <EntryRow key={i} entry={child} depth={0} />
          ))}
        </ul>
      )}
    </li>
  )
}

function describeEntry(entry: CdaEntry): string {
  const name = entry.codeName || entry.code || entry.kind || 'entry'
  if (entry.valueName) return `${name}: ${entry.valueName}`
  if (entry.value) return `${name}: ${entry.value}${entry.unit ? ` ${entry.unit}` : ''}`
  if (entry.nilFlavor) return `${name} (absent)`
  return name
}

function NoteList({
  notes,
}: {
  notes: { severity: string; path?: string; message: string; rule?: string }[]
}) {
  return (
    <ul className="space-y-2 text-sm">
      {notes.map((n, i) => (
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
              {n.path && <code className="font-mono">{n.path}</code>}
              {n.path && n.rule && ' · '}
              {n.rule}
            </p>
          </div>
        </li>
      ))}
    </ul>
  )
}

/** formatCdaDate renders an HL7 timestamp readably, and says when it cannot rather
 *  than showing a mangled date. */
function formatCdaDate(value?: string): string {
  if (!value) return ''
  const digits = value.replace(/[^0-9]/g, '')
  if (digits.length < 8) {
    if (digits.length === 6) return `${digits.slice(0, 4)}-${digits.slice(4, 6)}`
    if (digits.length === 4) return digits
    return value
  }
  const date = `${digits.slice(0, 4)}-${digits.slice(4, 6)}-${digits.slice(6, 8)}`
  if (digits.length < 12) return date
  return `${date} ${digits.slice(8, 10)}:${digits.slice(10, 12)}`
}

/**
 * Conformance against the implementation guide.
 *
 * A different question from agreement, and worth its own panel for that reason. Agreement asks whether the prose
 * and the coded entries say the same thing. This asks whether a receiver would accept the document at all.
 *
 * The checker deliberately does not reproduce the full Schematron. A report with two hundred findings, most of
 * them about elements nobody reads, teaches nobody anything and trains people to ignore the report. These are the
 * statements that cause actual rejection or actual misreading.
 */
function ConformancePanel({ validation }: { validation?: DocumentInspection['validation'] }) {
  if (!validation) {
    return <p className="text-sm text-slate-500">The document was not checked against a guide.</p>
  }

  const errors = validation.findings.filter((f) => f.severity === 'error')
  const warnings = validation.findings.filter((f) => f.severity === 'warning')
  const info = validation.findings.filter((f) => f.severity === 'info')

  return (
    <div className="space-y-4">
      <div className="card p-4">
        <div className="flex flex-wrap items-baseline gap-2">
          <span className="text-sm text-slate-300">Checked against</span>
          <span className="badge border border-sky-800/60 bg-sky-950/40 text-sky-300">{validation.profile}</span>
        </div>
        <p className="mt-2 text-sm text-slate-400">
          An exchange partner that refuses a document usually says only that it was non-conformant. These are the
          conformance statements that cause a rejection or a misreading, rather than the several hundred that
          nobody acts on.
        </p>

        <div className="mt-3 flex flex-wrap gap-4 border-t border-slate-800 pt-3 text-xs">
          <span className={errors.length > 0 ? 'text-rose-300' : 'text-emerald-300'}>
            {errors.length} would cause rejection
          </span>
          <span className={warnings.length > 0 ? 'text-amber-300' : 'text-slate-400'}>
            {warnings.length} would lose information
          </span>
          <span className="text-slate-400">{info.length} worth knowing</span>
        </div>
      </div>

      {validation.errors === 0 && (
        <div className="rounded-lg border border-emerald-900/60 bg-emerald-950/30 p-4 text-sm text-emerald-200">
          Nothing here would cause a receiver to reject the document.
          {validation.warnings > 0 && ' Some information may not survive the trip — see below.'}
        </div>
      )}

      {[
        ['Would be rejected', errors, 'rose'],
        ['Would lose information', warnings, 'amber'],
        ['Worth knowing', info, 'slate'],
      ].map(([title, list, tone]) => {
        const findings = list as CdaFinding[]
        if (findings.length === 0) return null
        return (
          <Section key={title as string} title={title as string}>
            <ul className="space-y-3">
              {findings.map((f, i) => (
                <li
                  key={i}
                  className={`rounded-lg border p-3 ${
                    tone === 'rose'
                      ? 'border-rose-900/60 bg-rose-950/20'
                      : tone === 'amber'
                        ? 'border-amber-900/60 bg-amber-950/20'
                        : 'border-slate-800 bg-slate-900/40'
                  }`}
                >
                  <div className="flex flex-wrap items-baseline gap-2">
                    {f.section && <span className="text-sm font-medium text-slate-200">{f.section}</span>}
                    {/* The rule name, so a finding can be looked up in the guide or suppressed by name.
                        A finding you cannot cite is a finding you cannot argue with a supplier about. */}
                    {f.kind && <span className="font-mono text-[10px] text-slate-400">{f.kind}</span>}
                  </div>
                  <p className="mt-1 text-sm text-slate-300">{f.message}</p>
                </li>
              ))}
            </ul>
          </Section>
        )
      })}
    </div>
  )
}
