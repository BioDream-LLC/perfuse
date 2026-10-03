import { useState } from 'react'
import { api, ApiError } from './api'
import type {
  AttachmentBuildResult,
  AttachmentView,
  BuiltX12,
  CRDResponse,
  EligibilityRead,
  Enrollment,
  PriorAuthResult,
} from './api'
import { sample271, sample834 } from './api'
import { CodeArea } from './CodeArea'
import { IconX12 } from './Icons'
import { ErrorBox, Field, Section } from './ui'
import type { UiError } from './store'

/**
 * Claims attachments and prior authorisation: the two payer transactions with federal dates attached.
 *
 * CMS-0053-F makes the 006020 X12 275 the HIPAA standard for claims attachments from 26 May 2028. CMS-0057-F requires payers to
 * run a FHIR prior authorisation API from 1 January 2027, which in practice means translating Da Vinci PAS to and from the X12 278
 * their utilisation management systems still speak.
 *
 * Everything here transforms what is pasted and sends nothing. A 275 built on this page is handed back to be delivered through a
 * channel, which is where the trading partner, the credentials and the audit trail belong.
 */
export function PayerLab() {
  const [tab, setTab] = useState<'build' | 'read' | 'auth' | 'elig' | 'status' | 'enrol' | 'crd'>('build')
  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-medium text-slate-100">Eligibility, claim status, attachments and prior authorisation</h2>
        <p className="mt-1 text-sm text-slate-400">
          Build 270 eligibility inquiries and read the 271 answer - checked against the CAQH CORE data content rule - build 276
          claim status requests, read 834 enrolment files, build and read X12 275 claims attachments (006020X314, the CMS-0053-F
          standard), and turn X12 278 decisions into Da Vinci PAS ClaimResponses. Nothing here is stored or sent.
        </p>
      </div>
      {/* Toggle buttons rather than tabs: the view itself is already the selected tab in the navigation, and a second
          selected tab on the same page leaves assistive technology - and anything else asking "which tab is selected" -
          with two answers. */}
      <div role="group" aria-label="Payer tools" className="flex flex-wrap gap-2">
        {(
          [
            ['elig', 'Eligibility (270/271)'],
            ['status', 'Claim status (276)'],
            ['enrol', 'Enrolment (834)'],
            ['crd', 'Coverage requirements (CRD)'],
            ['build', 'Build a 275'],
            ['read', 'Read a 275'],
            ['auth', '278 to PAS'],
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
      {tab === 'build' && <BuildAttachment />}
      {tab === 'read' && <ReadAttachment />}
      {tab === 'auth' && <PriorAuth />}
      {tab === 'elig' && <Eligibility />}
      {tab === 'status' && <ClaimStatus />}
      {tab === 'enrol' && <EnrollmentReader />}
      {tab === 'crd' && <CoverageRequirements />}
    </div>
  )
}

const toError = (e: unknown): UiError => ({
  message: e instanceof Error ? e.message : String(e),
  problems: e instanceof ApiError ? e.problems : [],
})

const sampleCCDA = `<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <templateId root="2.16.840.1.113883.10.20.22.1.8" extension="2015-08-01"/>
  <code code="11504-8" codeSystem="2.16.840.1.113883.6.1" displayName="Surgical operation note"/>
  <title>Operative note (synthetic)</title>
</ClinicalDocument>`

function BuildAttachment() {
  const [f, setF] = useState({
    senderId: 'CLINIC01',
    receiverId: 'PAYER01',
    reference: 'ATT-0001',
    traceNumber: 'ACN-778899',
    solicited: false,
    payerName: 'EXAMPLE HEALTH PLAN',
    payerId: '12345',
    providerName: 'EXAMPLE CLINIC',
    providerNpi: '1234567893',
    patientLast: 'DOE',
    patientFirst: 'JANE',
    patientId: 'MEMBER001',
    claimId: 'CLAIM-42',
    serviceDate: '20260901',
    contentType: 'text/xml',
    filename: 'operative-note.xml',
  })
  const [doc, setDoc] = useState(sampleCCDA)
  const [fileB64, setFileB64] = useState<string | null>(null)
  const [result, setResult] = useState<AttachmentBuildResult | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [busy, setBusy] = useState(false)

  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setF({ ...f, [k]: e.target.type === 'checkbox' ? e.target.checked : e.target.value })

  async function onFile(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    if (!file) return
    const bytes = new Uint8Array(await file.arrayBuffer())
    let bin = ''
    for (const b of bytes) bin += String.fromCharCode(b)
    setFileB64(btoa(bin))
    setF({ ...f, contentType: file.type || 'application/octet-stream', filename: file.name })
  }

  async function build() {
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      setResult(
        await api.buildAttachment({
          senderId: f.senderId,
          receiverId: f.receiverId,
          reference: f.reference,
          traceNumber: f.traceNumber,
          solicited: f.solicited,
          payer: { name: f.payerName, id: f.payerId },
          provider: { name: f.providerName, id: f.providerNpi },
          patient: { name: f.patientLast, firstName: f.patientFirst, id: f.patientId },
          providerClaimId: f.claimId,
          serviceDate: f.serviceDate,
          contentType: f.contentType,
          filename: f.filename,
          ...(fileB64 ? { documentBase64: fileB64 } : { documentText: doc }),
        }),
      )
    } catch (e) {
      setError(toError(e))
    } finally {
      setBusy(false)
    }
  }

  const input = (label: string, k: keyof typeof f, hint?: string) => (
    <Field label={label} hint={hint}>
      <input className="input font-mono" aria-label={label} value={String(f[k])} onChange={set(k)} />
    </Field>
  )

  return (
    <div className="grid gap-5 xl:grid-cols-2">
      <div className="space-y-4">
        <Section title="Who and which claim">
          <div className="grid gap-3 sm:grid-cols-2">
            {input('Payer name', 'payerName')}
            {input('Payer ID', 'payerId')}
            {input('Provider name', 'providerName')}
            {input('Provider NPI', 'providerNpi', 'Checked against the NPI check digit.')}
            {input('Patient last name', 'patientLast')}
            {input('Patient first name', 'patientFirst')}
            {input('Member ID', 'patientId')}
            {input('Provider claim ID', 'claimId')}
            {input('Service date', 'serviceDate', 'CCYYMMDD, or a range CCYYMMDD-CCYYMMDD')}
            {input(
              'Trace number',
              'traceNumber',
              f.solicited ? "The 277 request's 2200D TRN02." : "The claim's PWK06 attachment control number.",
            )}
          </div>
          <label className="mt-3 flex items-center gap-2 text-sm text-slate-300">
            <input type="checkbox" checked={f.solicited} onChange={set('solicited')} />
            This answers a payer's 277 request (BGN01 11) rather than accompanying a claim (02)
          </label>
        </Section>
        <Section title="Envelope">
          <div className="grid gap-3 sm:grid-cols-3">
            {input('Sender ID', 'senderId')}
            {input('Receiver ID', 'receiverId')}
            {input('Reference', 'reference')}
          </div>
        </Section>
        <Section title="The document">
          <div className="grid gap-3 sm:grid-cols-2">
            {input('Content type', 'contentType')}
            {input('File name', 'filename')}
          </div>
          <div className="mt-3">
            <label className="text-sm text-slate-300">
              Upload a file (PDF, TIFF, JPEG) or paste a C-CDA below
              <input type="file" className="mt-1 block text-xs text-slate-400" onChange={(e) => void onFile(e)} />
            </label>
          </div>
          {!fileB64 && (
            <CodeArea
              language="xml"
              aria-label="Document to attach"
              className="mt-3 min-h-40"
              value={doc}
              onChange={setDoc}
              spellCheck={false}
            />
          )}
          <p className="mt-2 text-xs text-slate-400">Use synthetic data. This is a browser form, not a place for real patient information.</p>
          <button className="btn-primary mt-4 w-full" onClick={() => void build()} disabled={busy}>
            {busy ? 'Building…' : 'Build 275'}
          </button>
        </Section>
      </div>
      <div className="space-y-4">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
        {result && (
          <>
            <div className="card p-4" data-testid="attachment-result">
              <p className={result.roundTrip ? 'text-sm text-emerald-300' : 'text-sm text-rose-300'}>
                {result.roundTrip
                  ? `Read back: the document inside is byte-for-byte the one that went in (${result.readBack.documents[0]?.size} bytes).`
                  : 'Read back did not return the same document. Do not send this.'}
              </p>
              <p className="mt-2 text-xs text-amber-300">{result.basis}</p>
            </div>
            <Section title={`X12 275 (${result.bytes} bytes)`} icon={IconX12}>
              <pre className="max-h-96 overflow-auto font-mono text-xs break-all whitespace-pre-wrap text-slate-300" data-testid="x12-output">
                {result.x12.replace(/~/g, '~\n')}
              </pre>
            </Section>
          </>
        )}
      </div>
    </div>
  )
}

function ReadAttachment() {
  const [x12, setX12] = useState('')
  const [view, setView] = useState<AttachmentView | null>(null)
  const [error, setError] = useState<UiError | null>(null)

  async function read() {
    setError(null)
    setView(null)
    try {
      setView(await api.readAttachment(x12))
    } catch (e) {
      setError(toError(e))
    }
  }

  function download(b64: string, name: string, type: string) {
    const bin = atob(b64)
    const bytes = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
    const url = URL.createObjectURL(new Blob([bytes], { type: type || 'application/octet-stream' }))
    const a = document.createElement('a')
    a.href = url
    a.download = name || 'attachment'
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="grid gap-5 xl:grid-cols-2">
      <Section title="X12 275">
        <CodeArea language="x12" aria-label="X12 275 to read" className="min-h-64" value={x12} onChange={setX12} spellCheck={false} />
        <button className="btn-primary mt-4 w-full" onClick={() => void read()}>
          Read 275
        </button>
      </Section>
      <div className="space-y-4">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
        {view && (
          <div className="card p-4 text-sm" data-testid="attachment-view">
            <p className="text-slate-200">
              {view.version} · BGN01 {view.purpose} {view.purpose === '11' ? '(answers a 277 request)' : view.purpose === '02' ? '(unsolicited)' : ''}
            </p>
            <p className="mt-1 text-xs text-slate-400">
              Payer {view.payer.Name} ({view.payer.IDCode}) · Provider {view.provider.Name} ({view.provider.IDCode}) · Patient{' '}
              {view.patient.FirstName} {view.patient.Name} ({view.patient.IDCode})
              {view.providerClaimId && <> · Claim {view.providerClaimId}</>}
            </p>
            {view.documents.map((d, i) => (
              <div key={i} className="mt-3 rounded border border-slate-700 p-3">
                <p className="text-slate-200">
                  {d.filename || `Document ${i + 1}`} · {d.contentType || 'no content type'} · {d.size} bytes
                </p>
                <p className="text-xs text-slate-400">
                  Trace {d.traceType === '2' ? '(277 TRN02)' : '(claim PWK06)'} {d.traceNumber} · CAT {d.category}/{d.transmission}
                </p>
                {d.notes.map((n) => (
                  <p key={n} className="mt-1 text-xs text-amber-300">
                    {n}
                  </p>
                ))}
                <button className="btn-ghost mt-2 py-1 text-xs" onClick={() => download(d.documentBase64, d.filename, d.contentType)}>
                  Download the document
                </button>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

const sample278 = [
  'ISA*00*          *00*          *ZZ*PAYER          *ZZ*PROVIDER       *260916*1500*^*00501*000000404*0*T*:~',
  'GS*HI*PAYER*PROVIDER*20260916*1500*404*X*005010X217~',
  'ST*278*0001*005010X217~',
  'BHT*0007*11*REQ-9*20260916*1500*11~',
  'HL*1**20*1~',
  'NM1*X3*2*ACME HEALTH PLAN*****PI*ACME01~',
  'HL*2*1*21*1~',
  'NM1*1P*2*RIVERSIDE ORTHOPAEDICS*****XX*1234567893~',
  'HL*3*2*22*1~',
  'NM1*IL*1*TURNER*ROSALIND****MI*MEM88771~',
  'HL*4*3*EV*1~',
  'TRN*2*AUTHREQ-4471~',
  'UM*HS*I*4~',
  'DTP*472*D8*20261001~',
  'HI*BK:M1711~',
  'HCR*A1*AUTH-99120~',
  'HL*5*4*SS*0~',
  'SV1*HC:29881*450.00*UN*1~',
  'SE*15*0001~',
  'GE*1*404~',
  'IEA*1*000000404~',
].join('\n')

function PriorAuth() {
  const [x12, setX12] = useState(sample278)
  const [result, setResult] = useState<PriorAuthResult | null>(null)
  const [error, setError] = useState<UiError | null>(null)

  async function convert() {
    setError(null)
    setResult(null)
    try {
      setResult(await api.priorAuthClaimResponse(x12))
    } catch (e) {
      setError(toError(e))
    }
  }

  return (
    <div className="grid gap-5 xl:grid-cols-2">
      <Section title="X12 278 response">
        <CodeArea language="x12" aria-label="X12 278 response" className="min-h-64" value={x12} onChange={setX12} spellCheck={false} />
        <button className="btn-primary mt-4 w-full" onClick={() => void convert()}>
          Convert to PAS ClaimResponse
        </button>
      </Section>
      <div className="space-y-4">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
        {result && (
          <>
            <div className="card p-4 text-sm" data-testid="priorauth-summary">
              <p className="text-slate-200">{result.summary}</p>
              {result.decisions.map((d, i) => (
                <p key={i} className="mt-1 text-xs text-slate-400">
                  Item {i + 1}: {d.decision}
                  {d.number && <> · authorisation {d.number}</>}
                  {d.reasonCode && <> · reason {d.reasonCode}</>}
                  {d.lostInNarrowing && <> · {d.lostInNarrowing}</>}
                </p>
              ))}
              {result.notes.map((n) => (
                <p key={n} className="mt-1 text-xs text-amber-300">
                  {n}
                </p>
              ))}
              <p className="mt-2 text-xs text-slate-500">Shaped to {result.profile}.</p>
            </div>
            <Section title="ClaimResponse">
              <pre className="max-h-[32rem] overflow-auto font-mono text-xs text-slate-300" data-testid="claimresponse">
                {JSON.stringify(result.claimResponse, null, 2)}
              </pre>
            </Section>
          </>
        )}
      </div>
    </div>
  )
}

/** A small labelled input, so the forms below stay readable. */
function In({ label, value, onChange, placeholder }: { label: string; value: string; onChange: (v: string) => void; placeholder?: string }) {
  return (
    <Field label={label}>
      <input className="input font-mono text-xs" value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
    </Field>
  )
}

function BuiltResult({ result, title }: { result: BuiltX12; title: string }) {
  return (
    <Section title={title} icon={IconX12}>
      {result.envelopeProblems.length === 0 ? (
        <p className="mb-2 text-xs text-emerald-300">Read back: {result.segments} segments, envelope counts and control numbers agree.</p>
      ) : (
        result.envelopeProblems.map((p) => (
          <p key={p.Message} className="mb-1 text-xs text-rose-300">
            {p.Segment}: {p.Message}
          </p>
        ))
      )}
      <pre data-testid="built-x12" className="max-h-96 overflow-auto whitespace-pre-wrap break-all font-mono text-xs text-slate-300">
        {result.x12.replaceAll('~', '~\n')}
      </pre>
      <p className="mt-2 text-xs text-slate-500">{result.basis}</p>
    </Section>
  )
}

/** Eligibility: build a 270, and read the 271 that comes back into the answer a front desk needs. */
function Eligibility() {
  const [f, setF] = useState({
    senderId: 'CLINIC01', receiverId: 'PAYER01', payerName: 'Springfield Health Plan', payerId: 'SHP01',
    providerName: 'Riverside Clinic', npi: '1234567893', last: 'DOE', first: 'JANE', member: 'MBR123456', dob: '19800101',
    types: '30',
  })
  const set = (k: keyof typeof f) => (v: string) => setF((o) => ({ ...o, [k]: v }))
  const [built, setBuilt] = useState<BuiltX12 | null>(null)
  const [x271, setX271] = useState(sample271)
  const [read, setRead] = useState<EligibilityRead | null>(null)
  const [error, setError] = useState<UiError | null>(null)

  async function build() {
    setError(null)
    try {
      setBuilt(
        await api.buildEligibility({
          senderId: f.senderId, receiverId: f.receiverId,
          payer: { lastName: f.payerName, id: f.payerId },
          provider: { lastName: f.providerName, id: f.npi },
          subscriber: { lastName: f.last, firstName: f.first, id: f.member, dob: f.dob },
          serviceTypes: f.types.split(/[ ,]+/).filter(Boolean),
        }),
      )
    } catch (e) {
      setError(toError(e))
    }
  }
  async function readIt() {
    setError(null)
    try {
      setRead(await api.readEligibility(x271))
    } catch (e) {
      setError(toError(e))
    }
  }

  return (
    <div className="space-y-5">
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      <div className="grid gap-5 xl:grid-cols-2">
        <Section title="Ask: a 270 inquiry">
          <div className="grid gap-3 sm:grid-cols-2">
            <In label="Payer name" value={f.payerName} onChange={set('payerName')} />
            <In label="Payer ID" value={f.payerId} onChange={set('payerId')} />
            <In label="Provider name" value={f.providerName} onChange={set('providerName')} />
            <In label="Provider NPI" value={f.npi} onChange={set('npi')} />
            <In label="Subscriber last name" value={f.last} onChange={set('last')} />
            <In label="Subscriber first name" value={f.first} onChange={set('first')} />
            <In label="Member ID" value={f.member} onChange={set('member')} />
            <In label="Date of birth (CCYYMMDD)" value={f.dob} onChange={set('dob')} />
            <In label="Service types" value={f.types} onChange={set('types')} placeholder="30" />
            <In label="Sender ID (ISA06)" value={f.senderId} onChange={set('senderId')} />
            <In label="Receiver ID (ISA08)" value={f.receiverId} onChange={set('receiverId')} />
          </div>
          <p className="mt-2 text-xs text-slate-500">
            Service type 30 is health benefit plan coverage, the one a CORE-certified payer must answer in full.
          </p>
          <button className="btn-primary mt-4 w-full" onClick={() => void build()}>
            Build the 270
          </button>
        </Section>
        {built && <BuiltResult result={built} title="X12 270" />}
      </div>

      <div className="grid gap-5 xl:grid-cols-2">
        <Section title="Answer: read a 271">
          <CodeArea language="x12" aria-label="X12 271 response" className="min-h-48" value={x271} onChange={setX271} spellCheck={false} />
          <button className="btn-primary mt-4 w-full" onClick={() => void readIt()}>
            Read the 271
          </button>
        </Section>
        {read && (
          <div className="space-y-4">
            <div className="card p-4 text-sm" data-testid="eligibility-summary">
              <p className={read.eligibility.status === 'active' ? 'font-medium text-emerald-300' : 'font-medium text-amber-300'}>
                Coverage {read.eligibility.status}
              </p>
              {read.eligibility.summary.map((l) => (
                <p key={l} className="mt-1 text-slate-200">
                  {l}
                </p>
              ))}
            </div>
            <Section title="CAQH CORE data content">
              <ul className="space-y-1 text-xs" data-testid="eligibility-core">
                {read.core.map((c) => (
                  <li key={c.requirement} className={c.met ? 'text-emerald-300' : 'text-amber-300'}>
                    {c.met ? 'Met' : 'Missing'}: {c.requirement}
                    {c.detail && !c.met ? ` (${c.detail})` : ''}
                  </li>
                ))}
              </ul>
              <p className="mt-2 text-xs text-slate-500">{read.coreBasis}</p>
            </Section>
            <Section title={`Benefits (${read.eligibility.benefits.length})`} icon={IconX12}>
              <table className="w-full text-left text-xs">
                <thead className="text-slate-500">
                  <tr>
                    <th className="py-1 pr-2">Benefit</th>
                    <th className="py-1 pr-2">Service types</th>
                    <th className="py-1 pr-2">Network</th>
                    <th className="py-1">Amount</th>
                  </tr>
                </thead>
                <tbody className="text-slate-300">
                  {read.eligibility.benefits.map((b, i) => (
                    <tr key={i} className="border-t border-slate-800">
                      <td className="py-1 pr-2">
                        {b.meaning || b.code}
                        {b.level ? ` (${b.level})` : ''}
                        {b.period === '29' ? ', remaining' : ''}
                      </td>
                      <td className="py-1 pr-2 font-mono">{(b.serviceTypes ?? []).join(' ')}</td>
                      <td className="py-1 pr-2">{{ Y: 'in', N: 'out', W: 'both' }[b.inNetwork ?? ''] ?? ''}</td>
                      <td className="py-1 font-mono">
                        {b.amount ? `$${b.amount}` : b.percent ? `${Math.round(Number(b.percent) * 100)}%` : b.plan ?? ''}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Section>
          </div>
        )}
      </div>
    </div>
  )
}

/** Claim status: build a 276. The 277 that answers it is read on the attachments tab, which already reads 277s. */
function ClaimStatus() {
  const [f, setF] = useState({
    senderId: 'CLINIC01', receiverId: 'PAYER01', payerName: 'Springfield Health Plan', payerId: 'SHP01',
    providerName: 'Riverside Clinic', npi: '1234567893', last: 'DOE', first: 'JANE', member: 'MBR123456',
    account: 'PCN0042', payerClaim: '', charge: '250.00', from: '20260915', to: '',
  })
  const set = (k: keyof typeof f) => (v: string) => setF((o) => ({ ...o, [k]: v }))
  const [built, setBuilt] = useState<BuiltX12 | null>(null)
  const [error, setError] = useState<UiError | null>(null)

  async function build() {
    setError(null)
    try {
      setBuilt(
        await api.buildClaimStatus({
          senderId: f.senderId, receiverId: f.receiverId,
          payer: { lastName: f.payerName, id: f.payerId },
          provider: { lastName: f.providerName, id: f.npi },
          subscriber: { lastName: f.last, firstName: f.first, id: f.member },
          patientAccount: f.account, payerClaimNumber: f.payerClaim || undefined, chargeAmount: f.charge || undefined,
          serviceFrom: f.from, serviceTo: f.to || undefined,
        }),
      )
    } catch (e) {
      setError(toError(e))
    }
  }

  return (
    <div className="grid gap-5 xl:grid-cols-2">
      <Section title="Ask: a 276 claim status request">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
        <div className="grid gap-3 sm:grid-cols-2">
          <In label="Payer name" value={f.payerName} onChange={set('payerName')} />
          <In label="Payer ID" value={f.payerId} onChange={set('payerId')} />
          <In label="Provider name" value={f.providerName} onChange={set('providerName')} />
          <In label="Provider NPI" value={f.npi} onChange={set('npi')} />
          <In label="Subscriber last name" value={f.last} onChange={set('last')} />
          <In label="Member ID" value={f.member} onChange={set('member')} />
          <In label="Patient account (CLM01)" value={f.account} onChange={set('account')} />
          <In label="Payer claim number" value={f.payerClaim} onChange={set('payerClaim')} placeholder="if known" />
          <In label="Total charge" value={f.charge} onChange={set('charge')} />
          <In label="Service from (CCYYMMDD)" value={f.from} onChange={set('from')} />
          <In label="Service to" value={f.to} onChange={set('to')} placeholder="same day" />
        </div>
        <button className="btn-primary mt-4 w-full" onClick={() => void build()}>
          Build the 276
        </button>
      </Section>
      {built && <BuiltResult result={built} title="X12 276" />}
    </div>
  )
}

/** Enrolment: read an 834 into who was added, changed or terminated, and on which coverage. */
function EnrollmentReader() {
  const [x, setX] = useState(sample834)
  const [e, setE] = useState<Enrollment | null>(null)
  const [error, setError] = useState<UiError | null>(null)

  async function readIt() {
    setError(null)
    try {
      setE(await api.readEnrollment(x))
    } catch (err) {
      setError(toError(err))
    }
  }

  return (
    <div className="grid gap-5 xl:grid-cols-2">
      <Section title="An 834 enrolment file">
        <CodeArea language="x12" aria-label="X12 834" className="min-h-48" value={x} onChange={setX} spellCheck={false} />
        <button className="btn-primary mt-4 w-full" onClick={() => void readIt()}>
          Read the 834
        </button>
      </Section>
      <div className="space-y-4">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
        {e && (
          <Section
            title={`${e.members.length} member${e.members.length === 1 ? '' : 's'} from ${e.sponsor.lastName || 'the sponsor'}`}
            icon={IconX12}
          >
            <table className="w-full text-left text-xs" data-testid="enrollment-members">
              <thead className="text-slate-500">
                <tr>
                  <th className="py-1 pr-2">Member</th>
                  <th className="py-1 pr-2">Change</th>
                  <th className="py-1">Coverage</th>
                </tr>
              </thead>
              <tbody className="text-slate-300">
                {e.members.map((m, i) => (
                  <tr key={i} className="border-t border-slate-800 align-top">
                    <td className="py-1 pr-2">
                      {m.person.firstName} {m.person.lastName}
                      <span className="block text-slate-500">
                        {m.subscriber ? 'subscriber' : m.relationship} · {m.memberId || m.subscriberId}
                      </span>
                    </td>
                    <td className="py-1 pr-2">{m.action}</td>
                    <td className="py-1">
                      {m.coverages.map((c, j) => (
                        <span key={j} className="block">
                          {c.line} {c.plan} {c.level}
                          {c.begin ? ` from ${c.begin}` : ''}
                          {c.end ? ` until ${c.end}` : ''}
                        </span>
                      ))}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Section>
        )}
      </div>
    </div>
  )
}

/**
 * Coverage requirements: an order sent to a Da Vinci CRD service, the way an EHR does at order-sign, to see whether it is covered,
 * needs prior authorisation, and which questionnaire gathers the documentation. Empty URL asks this server's own rules.
 */
function CoverageRequirements() {
  const [f, setF] = useState({ url: '', token: '', system: 'https://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets', code: 'E0424',
    kind: 'DeviceRequest', coverage: 'cov-1', patient: 'p-1' })
  const set = (k: keyof typeof f) => (v: string) => setF((o) => ({ ...o, [k]: v }))
  const [resp, setResp] = useState<CRDResponse | null>(null)
  const [error, setError] = useState<UiError | null>(null)

  async function ask() {
    setError(null)
    setResp(null)
    const coding = { coding: [{ system: f.system || undefined, code: f.code }] }
    const order: Record<string, unknown> = { resourceType: f.kind, id: 'draft-1', status: 'draft', subject: { reference: `Patient/${f.patient}` } }
    if (f.kind === 'DeviceRequest') Object.assign(order, { intent: 'original-order', codeCodeableConcept: coding })
    else Object.assign(order, { intent: 'order', code: coding })
    try {
      setResp(
        await api.askCRD({
          url: f.url || undefined,
          token: f.token || undefined,
          order,
          coverage: { resourceType: 'Coverage', id: f.coverage, status: 'active' },
          patientId: f.patient,
        }),
      )
    } catch (e) {
      setError(toError(e))
    }
  }

  const info = (resp?.systemActions ?? []).flatMap((a) =>
    ((a.resource.extension as { url: string; extension?: { url: string; valueCode?: string; valueCanonical?: string }[] }[]) ?? [])
      .filter((e) => e.url.endsWith('ext-coverage-information'))
      .map((e) => e.extension ?? []),
  )

  return (
    <div className="grid gap-5 xl:grid-cols-2">
      <Section title="Ask: an order at order-sign">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Order type">
            <select className="input" value={f.kind} onChange={(e) => set('kind')(e.target.value)}>
              <option value="DeviceRequest">DeviceRequest (equipment)</option>
              <option value="ServiceRequest">ServiceRequest (procedure, imaging)</option>
            </select>
          </Field>
          <In label="Code" value={f.code} onChange={set('code')} placeholder="E0424" />
          <In label="Code system" value={f.system} onChange={set('system')} />
          <In label="Coverage id" value={f.coverage} onChange={set('coverage')} />
          <In label="CRD service URL" value={f.url} onChange={set('url')} placeholder="this server's rules" />
          <In label="Bearer token" value={f.token} onChange={set('token')} placeholder="for a payer's service" />
        </div>
        <button className="btn-primary mt-4 w-full" onClick={() => void ask()}>
          Ask for coverage requirements
        </button>
      </Section>
      {resp && (
        <div className="space-y-3" data-testid="crd-cards">
          {resp.cards.map((c) => (
            <div key={c.uuid} className="card p-4 text-sm">
              <p className={c.indicator === 'info' ? 'font-medium text-slate-100' : 'font-medium text-amber-300'}>{c.summary}</p>
              {c.detail && <p className="mt-1 text-slate-300">{c.detail}</p>}
              <p className="mt-1 text-xs text-slate-500">From {c.source.label}</p>
              {(c.links ?? []).map((l) => (
                <a key={l.url} className="mt-1 block text-xs text-sky-300 underline" href={l.url} target="_blank" rel="noreferrer">
                  {l.label}
                </a>
              ))}
            </div>
          ))}
          {info.map((ext, i) => (
            <Section key={i} title="Coverage information on the order">
              <ul className="space-y-0.5 font-mono text-xs text-slate-300" data-testid="crd-coverage-info">
                {ext
                  .filter((x) => x.valueCode || x.valueCanonical)
                  .map((x) => (
                    <li key={x.url + (x.valueCode ?? x.valueCanonical)}>
                      {x.url}: {x.valueCode ?? x.valueCanonical}
                    </li>
                  ))}
              </ul>
            </Section>
          ))}
        </div>
      )}
    </div>
  )
}
