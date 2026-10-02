import { useEffect, useState } from 'react'
import { api, ApiError } from './api'
import type { CARINConversion, CMS0057Status, PAMetricsResult, PDexPriorAuthResult } from './api'
import { CodeArea } from './CodeArea'
import { ErrorBox, Field, Section } from './ui'
import type { UiError } from './store'

/**
 * CMS-0057 for payers.
 *
 * The CMS Interoperability and Prior Authorization final rule requires impacted payers to run four FHIR APIs - Patient Access,
 * Provider Access, Payer-to-Payer and Prior Authorization - from 1 January 2027, and to post prior authorization metrics every
 * year from 2026. This page says which of the four this instance can answer and what each still needs, and does the three
 * conversions the data behind them depends on. The conversions store nothing and send nothing.
 */
export function CMS0057() {
  const [tab, setTab] = useState<'apis' | 'carin' | 'pdex' | 'metrics'>('apis')
  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-medium text-slate-100">CMS-0057 payer APIs</h2>
        <p className="mt-1 text-sm text-slate-400">
          The four FHIR APIs the CMS Interoperability and Prior Authorization rule requires of payers, and the data behind them:
          claims as CARIN Blue Button, prior authorizations as Da Vinci PDex, and the metrics posted every year. Drugs are out of
          scope, as they are in the rule.
        </p>
      </div>
      <div role="group" aria-label="CMS-0057 tools" className="flex flex-wrap gap-2">
        {(
          [
            ['apis', 'The four APIs'],
            ['carin', 'Claims to CARIN BB'],
            ['pdex', 'Prior auth to PDex'],
            ['metrics', 'Prior auth metrics'],
          ] as const
        ).map(([id, label]) => (
          <button
            key={id}
            aria-pressed={tab === id}
            className={tab === id ? 'btn-primary py-1 text-sm' : 'btn-ghost py-1 text-sm'}
            onClick={() => setTab(id)}
          >
            {label}
          </button>
        ))}
      </div>
      {tab === 'apis' && <Readiness />}
      {tab === 'carin' && <CARIN />}
      {tab === 'pdex' && <PDex />}
      {tab === 'metrics' && <Metrics />}
    </div>
  )
}

const toError = (e: unknown): UiError => ({
  message: e instanceof Error ? e.message : String(e),
  problems: e instanceof ApiError ? e.problems : [],
})

function Readiness() {
  const [status, setStatus] = useState<CMS0057Status | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  useEffect(() => {
    api.cms0057Status().then(setStatus, (e) => setError(toError(e)))
  }, [])
  if (error) return <ErrorBox error={error} />
  if (!status) return <p className="text-sm text-slate-400">Reading this instance's FHIR configuration…</p>
  return (
    <div className="grid gap-4 lg:grid-cols-2" data-testid="cms0057-apis">
      {status.apis.map((a) => (
        <section key={a.name} className="card p-5" aria-labelledby={`api-${a.name}`}>
          <div className="flex items-start justify-between gap-3">
            <h3 id={`api-${a.name}`} className="text-sm font-medium text-slate-100">
              {a.name}
            </h3>
            <span
              className={
                a.ready
                  ? 'rounded bg-emerald-500/15 px-2 py-0.5 text-xs text-emerald-200'
                  : 'rounded bg-amber-500/15 px-2 py-0.5 text-xs text-amber-200'
              }
            >
              {a.ready ? 'Ready' : 'Not ready'}
            </span>
          </div>
          <p className="mt-1 text-xs text-slate-500">
            {a.rule} · {a.deadline}
          </p>
          <p className="mt-3 text-xs text-slate-400">{a.guides.join(' · ')}</p>
          <ul className="mt-2 space-y-0.5 font-mono text-xs text-slate-300">
            {a.endpoints.map((e) => (
              <li key={e} className="break-all">
                {e}
              </li>
            ))}
          </ul>
          {a.missing.length > 0 && (
            <div className="mt-3">
              <p className="text-xs text-amber-300">Still needs:</p>
              <ul className="mt-1 list-disc pl-5 text-xs text-amber-200">
                {a.missing.map((m) => (
                  <li key={m}>{m}</li>
                ))}
              </ul>
            </div>
          )}
        </section>
      ))}
    </div>
  )
}

const isa = 'ISA*00*          *00*          *ZZ*SUBMITTER      *ZZ*RECEIVER       *250304*1200*^*00501*000000101*0*T*:~'

const sample837 = [
  isa,
  'GS*HC*SUBMITTER*RECEIVER*20250304*1200*1*X*005010X222A1~',
  'ST*837*0001*005010X222A1~',
  'BHT*0019*00*BATCH0001*20250304*1200*CH~',
  'HL*1**20*1~',
  'PRV*BI*PXC*207Q00000X~',
  'NM1*85*2*RIVERSIDE FAMILY PRACTICE*****XX*1234567893~',
  'N3*100 MAIN STREET~',
  'N4*SPRINGFIELD*IL*627010000~',
  'REF*EI*123456789~',
  'HL*2*1*22*0~',
  'SBR*P*18*GRP1001******CI~',
  'NM1*IL*1*SAMPLESON*BRAVO****MI*MBR123456~',
  'DMG*D8*19800215*F~',
  'NM1*PR*2*EXAMPLE HEALTH PLAN*****PI*EHP01~',
  'CLM*PATACCT001*250***11:B:1*Y*A*Y*Y~',
  'HI*ABK:J029*ABF:R509~',
  'NM1*82*1*CARTER*ALEX****XX*1497758544~',
  'LX*1~',
  'SV1*HC:99213:25*150*UN*1***1:2~',
  'DTP*472*D8*20250221~',
  'REF*6R*LINE001~',
  'LX*2~',
  'SV1*HC:87880*100*UN*1***1~',
  'DTP*472*D8*20250221~',
  'REF*6R*LINE002~',
  'SE*25*0001~',
  'GE*1*1~',
  'IEA*1*000000101~',
].join('\n')

const sample835 = [
  isa,
  'GS*HP*RECEIVER*SUBMITTER*20250315*0900*2*X*005010X221A1~',
  'ST*835*0001*005010X221A1~',
  'BPR*I*155*C*ACH*CCP*01*999999999*DA*123456*1512345678**01*888888888*DA*654321*20250315~',
  'TRN*1*EFT0001*1512345678~',
  'DTM*405*20250314~',
  'N1*PR*EXAMPLE HEALTH PLAN~',
  'N1*PE*RIVERSIDE FAMILY PRACTICE*XX*1234567893~',
  'LX*1~',
  'CLP*PATACCT001*1*250*155*45*12*EHPCLAIM20250001*11*1~',
  'NM1*QC*1*SAMPLESON*BRAVO****MI*MBR123456~',
  'DTM*050*20250305~',
  'SVC*HC:99213:25*150*95**1~',
  'CAS*CO*45*30~',
  'CAS*PR*2*25~',
  'AMT*B6*120~',
  'REF*6R*LINE001~',
  'SVC*HC:87880*100*60**1~',
  'CAS*CO*45*20~',
  'CAS*PR*3*20~',
  'AMT*B6*80~',
  'REF*6R*LINE002~',
  'SE*21*0001~',
  'GE*1*2~',
  'IEA*1*000000101~',
].join('\n')

function CARIN() {
  const [claims, setClaims] = useState(sample837)
  const [remit, setRemit] = useState(sample835)
  const [system, setSystem] = useState('')
  const [network, setNetwork] = useState('innetwork')
  const [result, setResult] = useState<CARINConversion | null>(null)
  const [error, setError] = useState<UiError | null>(null)

  async function convert() {
    setError(null)
    setResult(null)
    try {
      setResult(await api.carinBB({ claims, remittance: remit, identifierSystem: system, networkStatus: network }))
    } catch (e) {
      setError(toError(e))
    }
  }

  return (
    <div className="grid gap-5 xl:grid-cols-2">
      <Section
        title="Claims and remittance"
        description="An 837 (professional or institutional) and the 835 that paid it. Each claim is paired with its payment by patient account number."
      >
        <div className="space-y-4">
          <Field label="837 claims">
            <CodeArea language="x12" className="min-h-48" value={claims} onChange={setClaims} spellCheck={false} />
          </Field>
          <Field label="835 remittance">
            <CodeArea language="x12" className="min-h-48" value={remit} onChange={setRemit} spellCheck={false} />
          </Field>
          <Field
            label="Identifier system"
            hint="The payer's namespace for member ids and claim numbers: a URI the payer owns. CARIN requires one, and none is invented."
          >
            <input
              className="input"
              placeholder="https://fhir.your-plan.org/identifier"
              value={system}
              onChange={(e) => setSystem(e.target.value)}
            />
          </Field>
          <Field label="Billing provider's network status" hint="Neither X12 transaction says, and it decides what the member pays.">
            <select className="input" value={network} onChange={(e) => setNetwork(e.target.value)}>
              <option value="innetwork">In network</option>
              <option value="outofnetwork">Out of network</option>
              <option value="">Not known</option>
            </select>
          </Field>
          <button className="btn-primary w-full" onClick={() => void convert()}>
            Convert to CARIN Blue Button
          </button>
        </div>
      </Section>
      <div className="space-y-4">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
        {result?.skipped.map((s) => (
          <p key={s} className="text-xs text-amber-300">
            {s}
          </p>
        ))}
        {result?.results.map((r) => (
          <Section key={r.claimNumber} title="CARIN Blue Button bundle">
            <div data-testid="carin-result" className="space-y-2 text-sm">
              <p className="text-slate-200">
                Claim {r.claimNumber} · {r.profile.split('/').pop()}
              </p>
              {r.notes.map((n) => (
                <p key={n} className="text-xs text-amber-300">
                  {n}
                </p>
              ))}
              <pre className="max-h-[32rem] overflow-auto font-mono text-xs text-slate-300" data-testid="carin-bundle">
                {JSON.stringify(r.bundle, null, 2)}
              </pre>
            </div>
          </Section>
        ))}
      </div>
    </div>
  )
}

const sampleClaimResponse = JSON.stringify(
  {
    resourceType: 'ClaimResponse',
    id: 'auth-1001',
    status: 'active',
    type: { coding: [{ system: 'http://terminology.hl7.org/CodeSystem/claim-type', code: 'professional' }] },
    use: 'preauthorization',
    patient: { reference: 'Patient/pt-MBR123456' },
    created: '2026-02-10T14:30:00Z',
    insurer: { reference: 'Organization/payer-EHP01' },
    requestor: { reference: 'Organization/org-1234567893' },
    outcome: 'complete',
    preAuthRef: 'AUTH-99120',
    item: [
      {
        itemSequence: 1,
        adjudication: [
          {
            extension: [
              {
                url: 'http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction',
                extension: [
                  { url: 'number', valueString: 'AUTH-99120' },
                  {
                    url: 'http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode',
                    valueCodeableConcept: { coding: [{ system: 'https://codesystem.x12.org/005010/306', code: 'A1' }] },
                  },
                ],
              },
            ],
            category: { coding: [{ system: 'http://terminology.hl7.org/CodeSystem/adjudication', code: 'submitted' }] },
          },
        ],
      },
    ],
  },
  null,
  2,
)

const sampleClaim = JSON.stringify(
  {
    resourceType: 'Claim',
    id: 'req-1001',
    status: 'active',
    use: 'preauthorization',
    priority: { coding: [{ system: 'http://terminology.hl7.org/CodeSystem/processpriority', code: 'normal' }] },
    insurance: [{ sequence: 1, focal: true, coverage: { reference: 'Coverage/cov-MBR123456' } }],
    item: [
      {
        sequence: 1,
        category: { coding: [{ system: 'https://codesystem.x12.org/005010/1365', code: '73' }] },
        productOrService: { coding: [{ system: 'http://www.ama-assn.org/go/cpt', code: '70553' }] },
        servicedDate: '2026-02-20',
        quantity: { value: 1 },
        net: { value: 1200, currency: 'USD' },
      },
    ],
  },
  null,
  2,
)

function PDex() {
  const [response, setResponse] = useState(sampleClaimResponse)
  const [claim, setClaim] = useState(sampleClaim)
  const [result, setResult] = useState<PDexPriorAuthResult | null>(null)
  const [error, setError] = useState<UiError | null>(null)

  async function convert() {
    setError(null)
    setResult(null)
    try {
      setResult(await api.pdexPriorAuth({ response, claim }))
    } catch (e) {
      setError(toError(e))
    }
  }

  return (
    <div className="grid gap-5 xl:grid-cols-2">
      <Section
        title="PAS response and request"
        description="The Da Vinci PAS ClaimResponse (or the response Bundle), and the Claim it answers, which says what coverage the decision was made under."
      >
        <div className="space-y-4">
          <Field label="PAS ClaimResponse">
            <CodeArea language="json" className="min-h-48" value={response} onChange={setResponse} spellCheck={false} />
          </Field>
          <Field label="PAS Claim">
            <CodeArea language="json" className="min-h-48" value={claim} onChange={setClaim} spellCheck={false} />
          </Field>
          <button className="btn-primary w-full" onClick={() => void convert()}>
            Convert to PDex prior authorization
          </button>
        </div>
      </Section>
      <div className="space-y-4">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
        {result && (
          <Section title="PDex prior authorization">
            <div className="space-y-2 text-sm" data-testid="pdex-result">
              <p className="text-slate-200">Decision: {result.decision}</p>
              {result.notes.map((n) => (
                <p key={n} className="text-xs text-amber-300">
                  {n}
                </p>
              ))}
              <pre className="max-h-[32rem] overflow-auto font-mono text-xs text-slate-300">
                {JSON.stringify(result.explanationOfBenefit, null, 2)}
              </pre>
            </div>
          </Section>
        )}
      </div>
    </div>
  )
}

const sampleDecisions = `request_id,line_of_business,priority,received,decided,decision,extended,appealed,appeal_decision,service_code,service_description
PA1,Medicare Advantage H1234,standard,2025-01-06 09:00,2025-01-08 15:00,approved,no,no,,70553,MRI brain with and without contrast
PA2,Medicare Advantage H1234,standard,2025-02-01 10:00,2025-02-12 10:00,denied,yes,yes,approved,27447,Total knee replacement
PA3,Medicare Advantage H1234,standard,2025-03-03 08:00,2025-03-05 08:00,denied,no,yes,denied,E0601,CPAP device
PA4,Medicare Advantage H1234,expedited,2025-05-01 08:00,2025-05-01 14:00,approved,no,no,,70553,MRI brain with and without contrast
PA5,Medicare Advantage H1234,expedited,2025-05-02 08:00,2025-05-05 20:00,denied,no,no,,43775,Laparoscopic sleeve gastrectomy`

const sampleServices = `category,code,description
Imaging,70553,MRI of the brain with and without contrast
Orthopedic surgery,27447,Total knee replacement
Durable medical equipment,E0601,CPAP device for sleep apnea`

function Metrics() {
  const [decisions, setDecisions] = useState(sampleDecisions)
  const [services, setServices] = useState(sampleServices)
  const [year, setYear] = useState(2025)
  const [org, setOrg] = useState('')
  const [contact, setContact] = useState('')
  const [standardDays, setStandardDays] = useState(7)
  const [result, setResult] = useState<PAMetricsResult | null>(null)
  const [error, setError] = useState<UiError | null>(null)

  async function compute() {
    setError(null)
    setResult(null)
    try {
      setResult(
        await api.paMetrics({ decisions, services, year, organization: org, contact, standardDays, expeditedHours: 72 }),
      )
    } catch (e) {
      setError(toError(e))
    }
  }

  function download(name: string, body: string, type: string) {
    const url = URL.createObjectURL(new Blob([body], { type }))
    const a = document.createElement('a')
    a.href = url
    a.download = name
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="grid gap-5 xl:grid-cols-2">
      <Section
        title="Prior authorization decisions"
        description="One row per request, exported from the utilization management system. Columns are found by name: priority, received, decided and decision are required."
      >
        <div className="space-y-4">
          <Field label="Decisions (CSV)">
            <CodeArea language="text" className="min-h-48" value={decisions} onChange={setDecisions} spellCheck={false} />
          </Field>
          <Field
            label="Items and services requiring prior authorization (CSV)"
            hint="CMS requires this list on the same page, with plain-language descriptions rather than codes alone."
          >
            <CodeArea language="text" className="min-h-24" value={services} onChange={setServices} spellCheck={false} />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Reporting year">
              <input className="input" type="number" value={year} onChange={(e) => setYear(Number(e.target.value))} />
            </Field>
            <Field label="Standard decision timeframe (days)" hint="7 under CMS-0057; QHP issuers on the federal exchanges use 15.">
              <input
                className="input"
                type="number"
                min={1}
                value={standardDays}
                onChange={(e) => setStandardDays(Number(e.target.value))}
              />
            </Field>
            <Field label="Organization name">
              <input className="input" value={org} onChange={(e) => setOrg(e.target.value)} />
            </Field>
            <Field label="Contact for questions">
              <input className="input" value={contact} onChange={(e) => setContact(e.target.value)} />
            </Field>
          </div>
          <button className="btn-primary w-full" onClick={() => void compute()}>
            Compute the metrics
          </button>
        </div>
      </Section>
      <div className="space-y-4">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
        {result && (
          <Section title="Public metrics page">
            <div className="space-y-3 text-sm" data-testid="metrics-result">
              {result.report.linesOfBusiness.map((l) => (
                <div key={l.name} className="rounded border border-slate-800 p-3">
                  <p className="font-medium text-slate-200">{l.name}</p>
                  <p className="mt-1 text-xs text-slate-400">
                    Standard: {l.standard.approved.percent}% approved of {l.standard.requests}, median{' '}
                    {l.standard.turnaround.median || 'n/a'} · Expedited: {l.expedited.approved.percent}% approved of{' '}
                    {l.expedited.requests}, median {l.expedited.turnaround.median || 'n/a'}
                  </p>
                </div>
              ))}
              {Object.entries(result.report.excluded).map(([why, n]) => (
                <p key={why} className="text-xs text-slate-500">
                  Excluded {n}: {why}
                </p>
              ))}
              {result.report.dataQuality.map((q) => (
                <p key={q} className="text-xs text-amber-300">
                  {q}
                </p>
              ))}
              <div className="flex flex-wrap gap-2">
                <button
                  className="btn-ghost py-1 text-sm"
                  onClick={() => download(`prior-authorization-metrics-${year}.html`, result.html, 'text/html')}
                >
                  Download the page
                </button>
                <button
                  className="btn-ghost py-1 text-sm"
                  onClick={() => download(`prior-authorization-metrics-${year}.csv`, result.csv, 'text/csv')}
                >
                  Download CSV
                </button>
              </div>
              {/* Sandboxed with no permissions: the page is the payer's public HTML, previewed, not run. */}
              <iframe
                title="Preview of the public prior authorization metrics page"
                sandbox=""
                srcDoc={result.html}
                className="h-[32rem] w-full rounded border border-slate-800 bg-white"
              />
            </div>
          </Section>
        )}
      </div>
    </div>
  )
}
