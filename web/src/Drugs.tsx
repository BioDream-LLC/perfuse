import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, ApiError } from './api'
import type { EPACase, EPAMetrics, FormularyDrug, FormularyView, RTPBAnswer, RTPBPriced } from './api'
import { ErrorBox, Field, Section, Spinner } from './ui'
import { CodeArea } from './CodeArea'
import type { UiError } from './store'

/**
 * Drugs: the payer's formulary and the two NCPDP standards answered from it.
 *
 * One file is the source for all three, so they cannot disagree: the Da Vinci US Drug Formulary API publishes it, Real-Time
 * Prescription Benefit prices a drug from it, and NCPDP SCRIPT electronic prior authorization asks its questions and decides
 * through the same rules, decision service and review queue as every other prior authorization here. CMS-0062-P proposes
 * all three for impacted payers from October 2027.
 */

type Tab = 'formulary' | 'price' | 'epa'

const toError = (e: unknown): UiError => ({
  message: e instanceof Error ? e.message : String(e),
  problems: e instanceof ApiError ? e.problems : [],
})

const restrictionWords: Record<string, string> = {
  '75': 'Prior authorization required',
  '76': 'Plan limits exceeded',
  '70': 'Not covered (plan exclusion)',
  '608': 'Step therapy required',
  '7X': 'Days supply over the plan limit',
  AB8: 'Prior authorization on file',
}

const statusWords: Record<string, { text: string; cls: string }> = {
  CC: { text: 'Covered', cls: 'text-emerald-300' },
  CR: { text: 'Covered with restrictions', cls: 'text-amber-300' },
  NC: { text: 'Not covered', cls: 'text-rose-300' },
}

const money = (v?: number) => (v === undefined ? '—' : `$${v.toFixed(2)}`)

const esc = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')

export function Drugs() {
  const [view, setView] = useState<FormularyView | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [tab, setTab] = useState<Tab>('formulary')

  useEffect(() => {
    api.formulary().then(setView).catch((e) => setError(toError(e)))
  }, [])

  if (!view && !error) return <Spinner label="Loading the formulary…" />

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-medium text-slate-100">Drugs</h2>
        <p className="mt-1 text-sm text-slate-400">
          The drug formulary, published over the Da Vinci US Drug Formulary API, and the NCPDP Real-Time Prescription Benefit
          and SCRIPT electronic prior authorization answered from it.
        </p>
      </div>
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      {view && !view.enabled && (
        <Section title="No formulary on this server">
          <p className="text-sm text-slate-300">{view.hint}</p>
        </Section>
      )}
      {view?.enabled && (
        <>
          <div className="flex flex-wrap gap-2" role="tablist" aria-label="Drug tools">
            {(['formulary', 'price', 'epa'] as Tab[]).map((t) => (
              <button
                key={t}
                type="button"
                role="tab"
                aria-selected={tab === t}
                className={tab === t ? 'btn-primary py-1 text-xs' : 'btn-ghost py-1 text-xs'}
                onClick={() => setTab(t)}
              >
                {{ formulary: 'Formulary', price: 'Price check (RTPB)', epa: 'Prior authorization (ePA)' }[t]}
              </button>
            ))}
          </div>
          {tab === 'formulary' && <FormularyTab view={view} />}
          {tab === 'price' && <PriceTab view={view} onError={setError} />}
          {tab === 'epa' && <EPATab view={view} onError={setError} />}
        </>
      )}
    </div>
  )
}

function FormularyTab({ view }: { view: FormularyView }) {
  const [q, setQ] = useState('')
  const drugs = (view.drugs ?? []).filter((d) =>
    `${d.display} ${d.rxnorm} ${d.ndc.join(' ')} ${d.tier}`.toLowerCase().includes(q.trim().toLowerCase()),
  )
  return (
    <>
      <div className="grid gap-4 sm:grid-cols-5" data-testid="formulary-counts">
        {(view.summary?.formularies ?? []).flatMap((f) => [
          ['Drugs', f.drugs, 'text-cyan-300'],
          ['Prior authorization', f.priorAuthorization, 'text-amber-300'],
          ['Step therapy', f.stepTherapy, 'text-amber-300'],
          ['Quantity limits', f.quantityLimit, 'text-sky-300'],
          ['Not covered', f.notCovered, 'text-slate-300'],
        ]).map(([label, value, cls], i) => (
          <div key={`${label}-${i}`} className="rounded-xl border border-slate-800 bg-slate-900/60 p-4">
            <p className="text-xs uppercase tracking-wide text-slate-500">{label}</p>
            <p className={`mt-1 text-2xl font-semibold ${cls}`}>{value}</p>
          </div>
        ))}
      </div>

      <Section title="Where it is published">
        <dl className="grid gap-2 text-sm sm:grid-cols-[14rem_1fr]">
          <dt className="text-slate-400">Da Vinci US Drug Formulary API</dt>
          <dd className="font-mono text-xs text-cyan-300" data-testid="formulary-base">
            {view.formularyBase}
          </dd>
          <dt className="text-slate-400">Real-Time Prescription Benefit</dt>
          <dd className="font-mono text-xs text-cyan-300">POST {view.rtpb}</dd>
          <dt className="text-slate-400">SCRIPT prior authorization</dt>
          <dd className="font-mono text-xs text-cyan-300">
            {view.epa ? `POST ${view.script}` : <span className="text-amber-300">off: needs the FHIR endpoint (-fhir)</span>}
          </dd>
          <dt className="text-slate-400">Payer ID</dt>
          <dd className="font-mono text-xs text-slate-200">{view.payerId || '—'}</dd>
        </dl>
        <p className="mt-3 text-xs text-slate-500">
          The Drug Formulary API is public, as CMS requires: it holds the formulary and nothing else. The NCPDP endpoints take an
          API token of the operator role.
        </p>
      </Section>

      <Section title="Plans">
        <ul className="space-y-1 text-sm">
          {(view.summary?.plans ?? []).map((p) => (
            <li key={p.id}>
              <span className="text-slate-100">{p.name}</span> <span className="font-mono text-xs text-slate-500">{p.id}</span>{' '}
              <span className="text-slate-400">uses {p.formulary}, {p.costs} cost shares</span>
            </li>
          ))}
        </ul>
      </Section>

      <Section title="Drugs on the formulary">
        <Field label="Find a drug">
          <input className="input" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Name, RxNorm, NDC or tier" />
        </Field>
        <table className="mt-4 w-full text-left text-sm" data-testid="formulary-drugs">
          <thead className="text-xs text-slate-500">
            <tr>
              <th className="py-1 pr-2">Drug</th>
              <th className="py-1 pr-2">RxNorm</th>
              <th className="py-1 pr-2">Tier</th>
              <th className="py-1">Requirements</th>
            </tr>
          </thead>
          <tbody className="text-slate-300">
            {drugs.map((d) => (
              <tr key={d.formulary + d.rxnorm} className="border-t border-slate-800">
                <td className="py-1.5 pr-2 text-slate-100">
                  {d.display}
                  {d.notes && <p className="text-xs text-slate-500">{d.notes}</p>}
                </td>
                <td className="py-1.5 pr-2 font-mono text-xs">{d.rxnorm}</td>
                <td className="py-1.5 pr-2 text-xs">{d.tier}</td>
                <td className={`py-1.5 text-xs ${d.notCovered ? 'text-slate-400' : d.requirements.length ? 'text-amber-300' : 'text-emerald-300'}`}>
                  {d.requirements.length ? d.requirements.join('; ') : 'none'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
    </>
  )
}

function Priced({ p, label }: { p: RTPBPriced; label: string }) {
  const st = statusWords[p.coverageStatus] ?? { text: p.coverageStatus, cls: 'text-slate-300' }
  return (
    <div className="rounded-lg border border-slate-800 p-3">
      <p className="text-xs uppercase tracking-wide text-slate-500">{label}</p>
      <p className="mt-1 text-slate-100">{p.description}</p>
      <p className={`mt-1 text-sm font-medium ${st.cls}`}>{st.text}</p>
      <dl className="mt-2 grid grid-cols-[10rem_1fr] gap-1 text-xs">
        <dt className="text-slate-500">Member pays</dt>
        <dd className="text-slate-100">{money(p.patientPays)}</dd>
        <dt className="text-slate-500">Plan pays</dt>
        <dd className="text-slate-300">{money(p.planCost)}</dd>
        <dt className="text-slate-500">Fill</dt>
        <dd className="text-slate-300">
          {p.quantity} for {p.days} days, {p.benefit.replaceAll('-', ' ')}
        </dd>
      </dl>
      {(p.restrictions ?? []).length > 0 && (
        <ul className="mt-2 flex flex-wrap gap-1.5">
          {p.restrictions!.map((r) => (
            <li key={r} className={`rounded px-2 py-0.5 text-xs ${r === 'AB8' ? 'bg-emerald-500/15 text-emerald-200' : 'bg-amber-500/15 text-amber-200'}`}>
              {restrictionWords[r] ?? r} <span className="font-mono opacity-70">{r}</span>
            </li>
          ))}
        </ul>
      )}
      <p className="mt-2 text-xs text-slate-400">{p.message}</p>
    </div>
  )
}

function PriceTab({ view, onError }: { view: FormularyView; onError: (e: UiError) => void }) {
  const drugs = (view.drugs ?? []).filter((d) => d.ndc.length > 0)
  const [member, setMember] = useState('')
  const [ndc, setNdc] = useState(drugs[0]?.ndc[0] ?? '')
  const [quantity, setQuantity] = useState('30')
  const [days, setDays] = useState('30')
  const [mail, setMail] = useState(false)
  const [result, setResult] = useState<{ answer: RTPBAnswer; xml: string } | null>(null)
  const [showXML, setShowXML] = useState(false)

  async function check() {
    try {
      setResult(await api.rtpbTry({ member, ndc, quantity: Number(quantity), days: Number(days), mail }))
    } catch (e) {
      onError(toError(e))
    }
  }

  return (
    <Section title="Price a drug for a member">
      <form
        className="grid gap-3 sm:grid-cols-6"
        onSubmit={(e) => {
          e.preventDefault()
          void check()
        }}
      >
        <Field label="Member ID">
          <input className="input font-mono" value={member} onChange={(e) => setMember(e.target.value)} />
        </Field>
        <div className="sm:col-span-2">
          <Field label="Drug">
            <select className="input" value={ndc} onChange={(e) => setNdc(e.target.value)}>
              {drugs.map((d) => (
                <option key={d.ndc[0]} value={d.ndc[0]}>
                  {d.display}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <Field label="Quantity">
          <input className="input" inputMode="decimal" value={quantity} onChange={(e) => setQuantity(e.target.value)} />
        </Field>
        <Field label="Days supply">
          <input className="input" inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value)} />
        </Field>
        <div className="flex items-end gap-3">
          <label className="flex items-center gap-2 text-sm text-slate-300">
            <input type="checkbox" checked={mail} onChange={(e) => setMail(e.target.checked)} /> Mail order
          </label>
          <button className="btn-primary" type="submit">
            Check
          </button>
        </div>
      </form>
      {result && (
        <div className="mt-4 space-y-3" data-testid="rtpb-result">
          {!result.answer.processed ? (
            <p className="rounded-lg border border-rose-900/60 bg-rose-950/30 p-3 text-sm text-rose-200">
              Not priced (reject {result.answer.reject}): {result.answer.note}
            </p>
          ) : (
            <>
              <p className="text-xs text-slate-500">Plan {result.answer.plan}</p>
              {result.answer.requested && <Priced p={result.answer.requested} label="Requested" />}
              {(result.answer.alternatives ?? []).map((a) => (
                <Priced key={a.ndc} p={a} label="Alternative" />
              ))}
            </>
          )}
          <button className="btn-ghost text-xs" type="button" onClick={() => setShowXML((v) => !v)}>
            {showXML ? 'Hide' : 'Show'} the RTPBResponse
          </button>
          {showXML && <CodeArea language="xml" aria-label="The RTPBResponse" readOnly value={result.xml} className="max-h-96 min-h-48" />}
        </div>
      )}
    </Section>
  )
}

// ── Electronic prior authorization ──────────────────────────────────────────────────────────────────────────────────────

interface ParsedQuestion {
  id: string
  text: string
  kind: 'select' | 'text' | 'number' | 'date'
  choices: { id: string; text: string }[]
  multiple: boolean
}

interface Parsed {
  kind: string
  status: 'Open' | 'Approved' | 'Denied' | 'Closed' | ''
  caseId: string
  note: string
  reasonCode: string
  authNumber: string
  expires: string
  questionSet: { id: string; title: string; description: string } | null
  questions: ParsedQuestion[]
}

const childText = (el: Element | null | undefined, name: string) =>
  el ? Array.from(el.children).find((c) => c.localName === name)?.textContent?.trim() ?? '' : ''
const child = (el: Element | null | undefined, name: string) =>
  el ? (Array.from(el.children).find((c) => c.localName === name) ?? null) : null
const children = (el: Element | null | undefined, name: string) => (el ? Array.from(el.children).filter((c) => c.localName === name) : [])

function parseReply(xml: string): Parsed {
  const doc = new DOMParser().parseFromString(xml, 'application/xml')
  const body = child(doc.documentElement, 'Body')
  const tx = body?.firstElementChild ?? null
  const status = child(child(child(tx, 'Response'), 'ResponseStatus'), 'Open') ??
    child(child(child(tx, 'Response'), 'ResponseStatus'), 'Approved') ??
    child(child(child(tx, 'Response'), 'ResponseStatus'), 'Denied') ??
    child(child(child(tx, 'Response'), 'ResponseStatus'), 'Closed')
  const qs = child(status, 'QuestionSet') ?? child(status, 'MoreInformationRequired')
  const header = child(qs, 'Header')
  return {
    kind: tx?.localName ?? '',
    status: (status?.localName ?? '') as Parsed['status'],
    caseId: childText(status, 'PACaseID'),
    note: childText(status, 'PANote') || childText(status, 'DenialReason'),
    reasonCode: childText(status, 'ReasonCode'),
    authNumber: childText(status, 'AuthorizationNumber'),
    expires: childText(child(child(status, 'AuthorizationPeriod'), 'ExpirationDate'), 'DateTime'),
    questionSet: header
      ? { id: childText(header, 'QuestionSetID'), title: childText(header, 'QuestionSetTitle'), description: childText(header, 'QuestionSetDescription') }
      : null,
    questions: children(qs, 'Question').map((q) => {
      const type = child(q, 'QuestionType')
      const sel = child(type, 'Select')
      return {
        id: childText(q, 'QuestionID'),
        text: childText(q, 'QuestionText'),
        kind: sel ? 'select' : child(type, 'Numeric') ? 'number' : child(type, 'Date') ? 'date' : 'text',
        multiple: ['true', 'y'].includes(childText(sel, 'SelectMultiple').toLowerCase()),
        choices: children(sel, 'Choice').map((c) => ({ id: childText(c, 'ChoiceID'), text: childText(c, 'ChoiceText') })),
      }
    }),
  }
}

interface Who {
  member: string
  last: string
  first: string
  dob: string
  npi: string
  quantity: string
  days: string
  diagnosis: string
}

function scriptMessage(kind: string, d: FormularyDrug, w: Who, payer: string, inner: string) {
  const id = `CONSOLE-${Date.now().toString(36)}`
  return `<?xml version="1.0" encoding="utf-8"?>
<Message DatatypesVersion="20230115" TransportVersion="20230115" TransactionDomain="SCRIPT" TransactionVersion="20230115" StructuresVersion="20230115" ECLVersion="20230115">
  <Header>
    <To Qualifier="ZZZ">${esc(payer)}</To>
    <From Qualifier="D">PERFUSE-CONSOLE</From>
    <MessageID>${id}</MessageID>
    <SentTime>${new Date().toISOString()}</SentTime>
  </Header>
  <Body>
    <${kind}>
      <PAReferenceID>${id}</PAReferenceID>
      <BenefitsCoordination><PBMMemberID>${esc(w.member)}</PBMMemberID></BenefitsCoordination>
      <Patient><HumanPatient><Names><Name><LastName>${esc(w.last)}</LastName><FirstName>${esc(w.first)}</FirstName></Name></Names><GenderAndSex><AdministrativeGender>U</AdministrativeGender></GenderAndSex><DateOfBirth><Date>${esc(w.dob)}</Date></DateOfBirth></HumanPatient></Patient>
      <Prescriber><NonVeterinarian><Identification><NPI>${esc(w.npi)}</NPI></Identification></NonVeterinarian></Prescriber>
      <MedicationPrescribed>
        <DrugDescription>${esc(d.display)}</DrugDescription>
        <Product><DrugCoded><NDC>${esc(d.ndc[0] ?? '')}</NDC></DrugCoded></Product>
        <Quantity><Value>${esc(w.quantity)}</Value><CodeListQualifier>38</CodeListQualifier></Quantity>
        <DaysSupply>${esc(w.days)}</DaysSupply>${w.diagnosis ? `
        <Diagnosis><ClinicalInformationQualifier>1</ClinicalInformationQualifier><Qualifier>ABF</Qualifier><Value>${esc(w.diagnosis.replace('.', ''))}</Value></Diagnosis>` : ''}
      </MedicationPrescribed>${inner}
    </${kind}>
  </Body>
</Message>`
}

function answersXML(p: Parsed, answers: Record<string, string | string[]>) {
  const qs = p.questionSet!
  const qx = p.questions
    .map((q, i) => {
      const a = answers[q.id]
      let type = ''
      if (q.kind === 'select') {
        const ids = (Array.isArray(a) ? a : a ? [a] : []).map((x) => `<ChoiceID>${esc(x)}</ChoiceID>`).join('')
        const choices = q.choices
          .map((c, n) => `<Choice><ChoiceID>${esc(c.id)}</ChoiceID><SequenceNumber>${n + 1}</SequenceNumber><ChoiceText>${esc(c.text)}</ChoiceText><AdditionalFreeTextIndicator>NA</AdditionalFreeTextIndicator></Choice>`)
          .join('')
        type = `<Select><SelectMultiple>${q.multiple}</SelectMultiple>${choices}${ids ? `<Answer><SubmitterProvidedAnswer>${ids}</SubmitterProvidedAnswer></Answer>` : ''}</Select>`
      } else if (q.kind === 'number') {
        type = `<Numeric><IsNumeric>true</IsNumeric>${a ? `<Answer><SubmitterProvidedNumericAnswer>${esc(String(a))}</SubmitterProvidedNumericAnswer></Answer>` : ''}</Numeric>`
      } else if (q.kind === 'date') {
        type = `<Date><IsDateTimeRequired>false</IsDateTimeRequired>${a ? `<Answer><SubmitterProvidedAnswer><Date>${esc(String(a))}</Date></SubmitterProvidedAnswer></Answer>` : ''}</Date>`
      } else {
        type = `<FreeText><IsFreeText>true</IsFreeText>${a ? `<Answer><SubmitterProvidedAnswer>${esc(String(a))}</SubmitterProvidedAnswer></Answer>` : ''}</FreeText>`
      }
      return `<Question><QuestionID>${esc(q.id)}</QuestionID><SequenceNumber>${i + 1}</SequenceNumber><QuestionText>${esc(q.text)}</QuestionText><QuestionType>${type}</QuestionType></Question>`
    })
    .join('')
  return `
      <Request><SolicitedModel><PACaseID>${esc(p.caseId)}</PACaseID><QuestionSet><Header><QuestionSetID>${esc(qs.id)}</QuestionSetID><QuestionSetTitle>${esc(qs.title)}</QuestionSetTitle></Header>${qx}</QuestionSet></SolicitedModel></Request>`
}

function Outcome({ p }: { p: Parsed }) {
  const tone =
    p.status === 'Approved'
      ? 'border-emerald-900/60 bg-emerald-950/30 text-emerald-200'
      : p.status === 'Denied'
        ? 'border-rose-900/60 bg-rose-950/30 text-rose-200'
        : 'border-sky-900/60 bg-sky-950/30 text-sky-200'
  return (
    <div className={`rounded-lg border p-3 text-sm ${tone}`} data-testid="epa-outcome">
      <p className="font-medium">
        {p.kind}: {p.status === 'Closed' ? `closed (${p.reasonCode})` : p.status.toLowerCase()}
        {p.caseId && <span className="ml-2 font-mono text-xs opacity-80">case {p.caseId}</span>}
      </p>
      {p.authNumber && (
        <p className="mt-1 text-xs">
          Authorization {p.authNumber}
          {p.expires && `, until ${p.expires.slice(0, 10)}`}
        </p>
      )}
      {p.note && <p className="mt-1 text-xs opacity-90">{p.note}</p>}
    </div>
  )
}

function EPATab({ view, onError }: { view: FormularyView; onError: (e: UiError) => void }) {
  const drugs = useMemo(() => (view.drugs ?? []).filter((d) => d.ndc.length > 0), [view.drugs])
  const [rx, setRx] = useState(drugs.find((d) => d.priorAuthorization)?.rxnorm ?? drugs[0]?.rxnorm ?? '')
  const [who, setWho] = useState<Who>({ member: '', last: 'DOE', first: 'JANE', dob: '1970-01-01', npi: '1234567893', quantity: '30', days: '30', diagnosis: '' })
  const [reply, setReply] = useState<Parsed | null>(null)
  const [answers, setAnswers] = useState<Record<string, string | string[]>>({})
  const [raw, setRaw] = useState('')
  const [cases, setCases] = useState<EPACase[]>([])
  const [metrics, setMetrics] = useState<EPAMetrics | null>(null)
  const [open, setOpen] = useState<EPACase | null>(null)
  const drug = drugs.find((d) => d.rxnorm === rx)

  const load = useCallback(async () => {
    try {
      setCases((await api.epaCases()).cases)
      const m = await api.epaMetrics()
      setMetrics(m.metrics ?? null)
    } catch (e) {
      onError(toError(e))
    }
  }, [onError])

  useEffect(() => {
    void load()
  }, [load])

  async function send(kind: string, inner = '') {
    if (!drug) return
    try {
      const out = await api.epaTry(scriptMessage(kind, drug, who, view.payerId ?? '', inner))
      setRaw(out.xml)
      const p = parseReply(out.xml)
      setReply(p)
      setAnswers({})
      void load()
    } catch (e) {
      onError(toError(e))
    }
  }

  const set = (k: keyof Who) => (e: React.ChangeEvent<HTMLInputElement>) => setWho((w) => ({ ...w, [k]: e.target.value }))

  if (!view.epa) {
    return (
      <Section title="Electronic prior authorization is off">
        <p className="text-sm text-slate-300">SCRIPT prior authorization keeps its cases in the FHIR store: start the server with -fhir, and -pas to decide them.</p>
      </Section>
    )
  }

  return (
    <>
      {!view.epaDecides && (
        <p className="rounded-lg border border-amber-900/60 bg-amber-950/30 p-3 text-sm text-amber-200">
          Initiations are answered, but requests cannot be decided without Da Vinci PAS: start the server with -pas.
        </p>
      )}
      <Section
        title="Try a prior authorization"
        description="Sends the SCRIPT messages a prescriber's system would, to this server's own endpoint: an initiation first, then the answered questions."
      >
        <div className="grid gap-3 sm:grid-cols-4">
          <div className="sm:col-span-2">
            <Field label="Drug">
              <select className="input" value={rx} onChange={(e) => setRx(e.target.value)}>
                {drugs.map((d) => (
                  <option key={d.rxnorm} value={d.rxnorm}>
                    {d.display} {d.requirements.length ? `(${d.requirements.join(', ')})` : ''}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          <Field label="Member ID">
            <input className="input font-mono" value={who.member} onChange={set('member')} />
          </Field>
          <Field label="Prescriber NPI">
            <input className="input font-mono" value={who.npi} onChange={set('npi')} />
          </Field>
          <Field label="Patient last name">
            <input className="input" value={who.last} onChange={set('last')} />
          </Field>
          <Field label="First name">
            <input className="input" value={who.first} onChange={set('first')} />
          </Field>
          <Field label="Birth date">
            <input type="date" className="input" value={who.dob} onChange={set('dob')} />
          </Field>
          <Field label="Diagnosis (ICD-10)">
            <input className="input font-mono" value={who.diagnosis} onChange={set('diagnosis')} placeholder="I82.409" />
          </Field>
          <Field label="Quantity">
            <input className="input" value={who.quantity} onChange={set('quantity')} />
          </Field>
          <Field label="Days supply">
            <input className="input" value={who.days} onChange={set('days')} />
          </Field>
          <div className="flex items-end sm:col-span-2">
            <button className="btn-primary" type="button" onClick={() => void send('PAInitiationRequest')}>
              Start a prior authorization
            </button>
          </div>
        </div>

        {reply && (
          <div className="mt-4 space-y-3">
            <Outcome p={reply} />
            {reply.status === 'Open' && reply.questionSet && reply.questions.length > 0 && (
              <form
                className="space-y-3 rounded-lg border border-slate-800 p-3"
                data-testid="epa-questions"
                onSubmit={(e) => {
                  e.preventDefault()
                  void send('PARequest', answersXML(reply, answers))
                }}
              >
                <p className="text-sm font-medium text-slate-100">{reply.questionSet.title}</p>
                {reply.questionSet.description && <p className="text-xs text-slate-400">{reply.questionSet.description}</p>}
                {reply.questions.map((q) => (
                  <div key={q.id}>
                    {q.kind === 'select' ? (
                      <fieldset>
                        <legend className="label">{q.text}</legend>
                        <div className="flex flex-wrap gap-3">
                          {q.choices.map((c) => (
                            <label key={c.id} className="flex items-center gap-1.5 text-sm text-slate-300">
                              <input
                                type={q.multiple ? 'checkbox' : 'radio'}
                                name={q.id}
                                checked={Array.isArray(answers[q.id]) ? (answers[q.id] as string[]).includes(c.id) : answers[q.id] === c.id}
                                onChange={(e) =>
                                  setAnswers((a) => {
                                    if (!q.multiple) return { ...a, [q.id]: c.id }
                                    const now = new Set((a[q.id] as string[]) ?? [])
                                    if (e.target.checked) now.add(c.id)
                                    else now.delete(c.id)
                                    return { ...a, [q.id]: [...now] }
                                  })
                                }
                              />
                              {c.text}
                            </label>
                          ))}
                        </div>
                      </fieldset>
                    ) : (
                      <Field label={q.text}>
                        {q.kind === 'text' ? (
                          <textarea
                            className="input min-h-16"
                            value={(answers[q.id] as string) ?? ''}
                            onChange={(e) => setAnswers((a) => ({ ...a, [q.id]: e.target.value }))}
                          />
                        ) : (
                          <input
                            className="input"
                            type={q.kind === 'date' ? 'date' : 'text'}
                            inputMode={q.kind === 'number' ? 'decimal' : undefined}
                            value={(answers[q.id] as string) ?? ''}
                            onChange={(e) => setAnswers((a) => ({ ...a, [q.id]: e.target.value }))}
                          />
                        )}
                      </Field>
                    )}
                  </div>
                ))}
                <button className="btn-primary" type="submit">
                  Send the answers
                </button>
              </form>
            )}
            <details>
              <summary className="cursor-pointer text-xs text-slate-400">The response as sent</summary>
              <CodeArea language="xml" aria-label="The SCRIPT response" readOnly value={raw} className="mt-2 max-h-96 min-h-48" />
            </details>
          </div>
        )}
      </Section>

      {metrics && (
        <Section title="Drug prior authorization figures" description="The past year, as CMS-0062-P proposes payers report them for drugs.">
          <dl className="grid gap-3 text-sm sm:grid-cols-4" data-testid="epa-metrics">
            {(
              [
                ['Requests', metrics.requests, 'text-cyan-300'],
                ['Approved', metrics.approved, 'text-emerald-300'],
                ['Denied', metrics.denied, 'text-slate-100'],
                ['Pending', metrics.pending, 'text-amber-300'],
                ['Appealed', metrics.appealed, 'text-sky-300'],
                ['Approved on appeal', metrics.approvedOnAppeal, 'text-emerald-300'],
                ['Median hours to decide', metrics.medianHours.toFixed(1), 'text-slate-100'],
                ['Decided late', metrics.late, metrics.late ? 'text-rose-300' : 'text-slate-100'],
              ] as const
            ).map(([label, value, cls]) => (
              <div key={label}>
                <dt className="text-xs text-slate-500">{label}</dt>
                <dd className={`text-lg font-semibold ${cls}`}>{value}</dd>
              </div>
            ))}
          </dl>
        </Section>
      )}

      <Section title="Prior authorization cases" description="Every SCRIPT case. A pended one is also in Claims & auth › Review queue, marked Drug.">
        {cases.length === 0 ? (
          <p className="text-sm text-slate-400">No case yet.</p>
        ) : (
          <table className="w-full text-left text-sm" data-testid="epa-cases">
            <thead className="text-xs text-slate-500">
              <tr>
                <th className="py-1 pr-2">Case</th>
                <th className="py-1 pr-2">Drug</th>
                <th className="py-1 pr-2">Member</th>
                <th className="py-1 pr-2">Stands</th>
                <th className="py-1">Prescriber told</th>
              </tr>
            </thead>
            <tbody className="text-slate-300">
              {cases.map((c) => (
                <tr
                  key={c.caseId}
                  className="cursor-pointer border-t border-slate-800 hover:bg-slate-800/40"
                  onClick={() => api.epaCase(c.caseId).then(setOpen).catch((e) => onError(toError(e)))}
                >
                  <td className="py-1.5 pr-2 font-mono text-xs">{c.caseId}</td>
                  <td className="py-1.5 pr-2 text-slate-100">{c.drug}</td>
                  <td className="py-1.5 pr-2 font-mono text-xs">{c.member}</td>
                  <td
                    className={`py-1.5 pr-2 text-xs ${
                      { approved: 'text-emerald-300', denied: 'text-rose-300', cancelled: 'text-slate-500' }[c.state] ?? 'text-amber-300'
                    }`}
                  >
                    {c.state}
                    {c.appealed && ` · appeal ${c.appealOutcome || 'pending'}`}
                  </td>
                  <td className={`py-1.5 text-xs ${c.replySent ? 'text-emerald-300' : 'text-rose-300'}`}>{c.replySent ? 'yes' : 'no'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {open && (
          <div className="mt-4 space-y-2 rounded-lg border border-slate-800 p-3 text-sm" data-testid="epa-case">
            <p className="text-slate-100">
              Case <span className="font-mono">{open.caseId}</span>: {open.drug}
            </p>
            {open.reason && <p className="text-xs text-slate-400">{open.reason}</p>}
            {open.authNumber && (
              <p className="text-xs text-emerald-300">
                Authorization {open.authNumber}, until {open.authEnd}
              </p>
            )}
            {(open.answers ?? []).length > 0 && (
              <dl className="grid gap-1 text-xs sm:grid-cols-[1fr_1fr]">
                {open.answers!.map((a) => (
                  <div key={a.id} className="contents">
                    <dt className="text-slate-500">{a.question}</dt>
                    <dd className="text-slate-200">{a.text}</dd>
                  </div>
                ))}
              </dl>
            )}
            {!open.replySent && open.reply && (
              <div className="flex items-center gap-3">
                <p className="text-xs text-rose-300">The prescriber has not been told: {open.replyError}</p>
                <button
                  className="btn-ghost text-xs"
                  type="button"
                  onClick={() =>
                    api
                      .epaResend(open.caseId)
                      .then(() => load())
                      .catch((e) => onError(toError(e)))
                  }
                >
                  Send again
                </button>
              </div>
            )}
          </div>
        )}
      </Section>
    </>
  )
}
