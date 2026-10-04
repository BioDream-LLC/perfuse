import { useState } from 'react'
import { api, type ECRFacility, type EICRResult } from './api'
import { ErrorBox, Field, Section } from './ui'
import { SyntaxBlock } from './SyntaxHighlight'

// A reportable visit, for trying the case report: a COVID-19 diagnosis at an emergency visit. Synthetic.
export const sampleReportable = [
  'MSH|^~\\&|EPIC|SPRINGFIELD^2.16.840.1.113883.19.5^ISO|ECR|PH|20261004093000-0500||ADT^A08^ADT_A01|MSG0001|P|2.5.1',
  'EVN|A08|20261004093000-0500',
  'PID|1||884422^^^SPRINGFIELD&2.16.840.1.113883.19.5&ISO^MR||Testpatient^Avery^Q||19800214|F||2106-3^White^CDCREC|12 Elm St^^Springfield^IL^62701^USA^H||^PRN^PH^^1^217^5550142|||||||||2186-5^Not Hispanic or Latino^CDCREC',
  'PV1|1|E|ED^^^SPRINGFIELD||||1234567893^Clinician^Morgan^^^^MD^^NPI&2.16.840.1.113883.4.6&ISO||||||||||||V8812^^^SPRINGFIELD&2.16.840.1.113883.19.5&ISO^VN|||||||||||||||||||||||||20261004080000-0500',
  'PV2|||^Fever and cough',
  'DG1|1||840539006^COVID-19^SCT||20261004|F',
  'DG1|2||386661006^Fever^SCT||20261004|W',
].join('\n')

const blankFacility: ECRFacility = { name: '', npi: '', phone: '', line: '', city: '', state: '', postalCode: '' }

/**
 * CaseReport builds the eCR eICR that a case reporting destination would send for the message in the FHIR lab, and shows
 * which code triggered it and what the report had to mark as missing.
 */
export function CaseReport({ message, system }: { message: string; system: string }) {
  const [facility, setFacility] = useState<ECRFacility>(blankFacility)
  const [result, setResult] = useState<EICRResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function build() {
    setBusy(true)
    setError(null)
    try {
      setResult(await api.buildEICR({ message, system, facility }))
    } catch (e) {
      setResult(null)
      setError(e instanceof Error ? e.message : 'the case report could not be built')
    } finally {
      setBusy(false)
    }
  }

  const set = (k: keyof ECRFacility) => (e: React.ChangeEvent<HTMLInputElement>) => setFacility({ ...facility, [k]: e.target.value })
  const fields: [keyof ECRFacility, string][] = [
    ['name', 'Facility name'],
    ['npi', 'NPI'],
    ['phone', 'Phone'],
    ['line', 'Street'],
    ['city', 'City'],
    ['state', 'State'],
    ['postalCode', 'ZIP'],
  ]

  return (
    <Section
      title="Public health case report (eICR)"
      description="What a case reporting destination would send to public health for this message: an HL7 eCR 2.1 eICR, built only when a code in it is a reportable-condition trigger. Nothing is sent from here."
    >
      {error && <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />}
      <p className="mb-3 text-xs text-slate-400">
        The reporting facility: eCR requires its phone and address, which a v2 message does not carry.
      </p>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {fields.map(([k, label]) => (
          <Field key={k} label={label}>
            <input className="input" aria-label={`Reporting facility ${label.toLowerCase()}`} value={facility[k]} onChange={set(k)} />
          </Field>
        ))}
      </div>
      <button className="btn-primary mt-4 w-full" onClick={() => void build()} disabled={busy || !message.trim()}>
        {busy ? 'Building…' : 'Build the case report'}
      </button>

      {result && (
        <div className="mt-4 space-y-3 text-sm" data-testid="eicr-result">
          {!result.reportable ? (
            <p className="text-slate-300">Not reportable: {result.reason}</p>
          ) : (
            <>
              <p className="text-slate-300">
                Reportable. {result.bundle ? 'Triggered by:' : `Triggered, but no report could be built: ${result.reason}`}
              </p>
              <ul className="list-inside list-disc text-slate-300">
                {result.triggers.map((t, i) => (
                  <li key={i}>
                    {t.condition ?? t.display}: <code className="font-mono text-xs">{t.code}</code> ({t.system.replace('http://', '')}) on{' '}
                    {t.resource.split('/')[0]}
                  </li>
                ))}
              </ul>
            </>
          )}
          <p className="text-xs text-slate-500">
            Trigger codes: {result.triggerSource} ({result.triggerCodes} codes).
          </p>
          {result.notes.length > 0 && (
            <div>
              <p className="label mb-1">What the report could not take from the message</p>
              <ul className="space-y-1 text-xs text-amber-200">
                {result.notes.map((n, i) => (
                  <li key={i}>{n}</li>
                ))}
              </ul>
            </div>
          )}
          {result.bundle && (
            <div className="card overflow-hidden">
              <div className="border-b border-slate-800 px-4 py-2 text-xs tracking-wide text-slate-500 uppercase">eICR document bundle</div>
              <SyntaxBlock code={JSON.stringify(result.bundle, null, 2)} language="json" maxHeight="28rem" />
            </div>
          )}
        </div>
      )}
    </Section>
  )
}
