import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, ApiError } from './api'
import type { MeasuresList, MeasureEvalResult } from './api'
import { CodeArea } from './CodeArea'
import { ErrorBox, Field, Section, Spinner } from './ui'
import type { UiError } from './store'

/**
 * Digital quality measures: FHIR Measures whose population criteria are FHIRPath, evaluated per patient over this server's
 * FHIR store (or over pasted resources) for a measurement period, giving a DEQM-shaped MeasureReport with the population
 * counts, the score and the strata. Saved Measures are also evaluated by the FHIR endpoint's Measure/$evaluate-measure.
 * Measures written in CQL are refused with the reason: Perfuse has no CQL engine.
 */

const toError = (e: unknown): UiError => ({
  message: e instanceof Error ? e.message : String(e),
  problems: e instanceof ApiError ? e.problems : [],
})

type Json = Record<string, unknown>

const popNames: Record<string, string> = {
  'initial-population': 'Initial population',
  denominator: 'Denominator',
  'denominator-exclusion': 'Denominator exclusions',
  'denominator-exception': 'Denominator exceptions',
  numerator: 'Numerator',
  'numerator-exclusion': 'Numerator exclusions',
}

const popColour: Record<string, string> = {
  'initial-population': 'text-cyan-300',
  denominator: 'text-violet-300',
  'denominator-exclusion': 'text-amber-300',
  'denominator-exception': 'text-amber-300',
  numerator: 'text-emerald-300',
  'numerator-exclusion': 'text-amber-300',
}

const codeOf = (cc: unknown): string => {
  const c = (cc as Json | undefined)?.coding as Json[] | undefined
  return (c?.[0]?.code as string) ?? ''
}

const scoreOf = (g: Json): number | null => {
  const s = (g.measureScore ?? g.measureScoreQuantity) as Json | undefined
  return typeof s?.value === 'number' ? s.value : null
}

const percent = (v: number | null) => (v === null ? '\u2014' : `${(v * 100).toFixed(1)}%`)

const lastYear = (): [string, string] => {
  const y = new Date().getFullYear() - 1
  return [`${y}-01-01`, `${y}-12-31`]
}

function Populations({ pops, contained }: { pops: Json[]; contained: Json[] }) {
  const members = (p: Json): string[] => {
    const ref = ((p.subjectResults as Json | undefined)?.reference as string | undefined)?.replace(/^#/, '')
    const list = contained.find((c) => c.id === ref)
    return ((list?.entry as Json[] | undefined) ?? []).map((e) => ((e.item as Json).reference as string) ?? '')
  }
  return (
    <table className="w-full text-left text-sm" data-testid="measure-populations">
      <tbody className="text-slate-300">
        {pops.map((p) => {
          const code = codeOf(p.code)
          const who = members(p)
          return (
            <tr key={code} className="border-t border-slate-800 align-top">
              <td className={`py-1.5 pr-3 ${popColour[code] ?? ''}`}>{popNames[code] ?? code}</td>
              <td className="py-1.5 pr-3 font-mono text-slate-100">{String(p.count ?? 0)}</td>
              <td className="py-1.5 font-mono text-xs text-slate-400">{who.join(', ')}</td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}

export function Measures() {
  const [list, setList] = useState<MeasuresList | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [text, setText] = useState('')
  const [[start, end], setPeriod] = useState<[string, string]>(lastYear)
  const [reportType, setReportType] = useState('summary')
  const [subject, setSubject] = useState('')
  const [resources, setResources] = useState('')
  const [result, setResult] = useState<MeasureEvalResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState('')

  const load = useCallback(() => {
    api.measures().then((l) => {
      setList(l)
      setText((t) => t || (l.examples[0] ? JSON.stringify(JSON.parse(l.examples[0].measure), null, 2) : ''))
    }).catch((e) => setError(toError(e)))
  }, [])
  useEffect(load, [load])

  const parsed = (): Json | null => {
    try {
      return JSON.parse(text) as Json
    } catch (e) {
      setError({ message: 'The measure is not JSON: ' + (e instanceof Error ? e.message : String(e)), problems: [] })
      return null
    }
  }

  const open = (raw: string) => {
    try {
      setText(JSON.stringify(JSON.parse(raw), null, 2))
    } catch {
      setText(raw)
    }
    setResult(null)
    setSaved('')
  }

  const evaluate = () => {
    const measure = parsed()
    if (!measure) return
    setBusy(true)
    setError(null)
    api
      .measureEvaluate({ measure, periodStart: start, periodEnd: end, reportType, subject: subject.trim() ? `Patient/${subject.trim().replace(/^Patient\//, '')}` : '', resources })
      .then(setResult)
      .catch((e) => {
        setResult(null)
        setError(toError(e))
      })
      .finally(() => setBusy(false))
  }

  const save = () => {
    const measure = parsed()
    if (!measure) return
    setError(null)
    api
      .measureSave(measure)
      .then((r) => {
        setSaved(`Saved as Measure/${r.saved}. The FHIR endpoint evaluates it as Measure/${r.saved}/$evaluate-measure.`)
        load()
      })
      .catch((e) => setError(toError(e)))
  }

  const remove = (id: string) => {
    if (!window.confirm(`Delete Measure/${id} from the FHIR store?`)) return
    api.measureDelete(id).then(load).catch((e) => setError(toError(e)))
  }

  const download = () => {
    if (!result) return
    const blob = new Blob([JSON.stringify(result.report, null, 2)], { type: 'application/fhir+json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'MeasureReport.json'
    a.click()
    URL.revokeObjectURL(url)
  }

  const report = result?.report
  const groups = useMemo(() => ((report?.group as Json[] | undefined) ?? []), [report])
  const contained = useMemo(() => ((report?.contained as Json[] | undefined) ?? []), [report])
  const improvement = codeOf(report?.improvementNotation)

  if (!list && !error) return <Spinner />

  return (
    <div className="space-y-6" data-testid="measures">
      {error && <ErrorBox error={error} />}
      {list && (
        <>
          <Section
            title="Quality measures"
            description={
              <>
                A digital quality measure counts the patients in each population (initial population, denominator, exclusions,
                numerator) over a measurement period and scores the result, as a FHIR MeasureReport (Da Vinci DEQM). Here the
                criteria are FHIRPath; Measures written in CQL are refused, because Perfuse has no CQL engine. The examples are
                illustrative and are not CMS eCQMs or HEDIS measures.{' '}
                {list.fhir
                  ? 'They are evaluated over this server\u2019s FHIR store, or over resources you paste.'
                  : 'This server has no FHIR store (-fhir), so paste the resources to evaluate.'}
              </>
            }
          >
            <div className="flex flex-wrap gap-2" data-testid="measure-picks">
              {list.examples.map((e) => (
                <button key={e.id} type="button" className="rounded-full border border-slate-700 px-3 py-1 text-xs text-slate-300 hover:border-emerald-600"
                  onClick={() => open(e.measure)} title="An example">
                  {e.title || e.id}
                </button>
              ))}
            </div>
            {list.measures.length > 0 && (
              <table className="mt-4 w-full text-left text-sm" data-testid="measures-saved">
                <thead className="text-xs text-slate-500">
                  <tr>
                    <th className="py-1 pr-2">Measure in the FHIR store</th>
                    <th className="py-1 pr-2">Scoring</th>
                    <th className="py-1" />
                  </tr>
                </thead>
                <tbody className="text-slate-300">
                  {list.measures.map((m) => (
                    <tr key={m.id} className="border-t border-slate-800 align-top">
                      <td className="py-1.5 pr-2">
                        <span className="text-slate-100">{m.title || m.id}</span>
                        <span className="block font-mono text-xs text-slate-500">Measure/{m.id}{m.version ? ` \u00b7 ${m.version}` : ''}</span>
                      </td>
                      <td className="py-1.5 pr-2 text-xs">
                        {m.problem ? <span className="text-red-300">{m.problem}</span> : <span className="text-violet-300">{m.scoring}</span>}
                      </td>
                      <td className="py-1.5 text-right text-xs whitespace-nowrap">
                        <button type="button" className="text-cyan-300 hover:underline"
                          onClick={() => api.measureGet(m.id).then(open).catch((e) => setError(toError(e)))}>
                          Open
                        </button>
                        <button type="button" className="ml-3 text-red-300 hover:underline" onClick={() => remove(m.id)}>
                          Delete
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Section>

          <Section title="Evaluate">
            <Field label="Measure (JSON)" hint="group.population criteria as text/fhirpath. Variables: %subject, %periodStart, %periodEnd. The focus is a Bundle of the patient's record.">
              <CodeArea value={text} onChange={setText} language="json" rows={16} />
            </Field>
            <div className="mt-3 grid gap-3 sm:grid-cols-4">
              <Field label="Period start">
                <input className="input" type="date" value={start} onChange={(e) => setPeriod([e.target.value, end])} />
              </Field>
              <Field label="Period end">
                <input className="input" type="date" value={end} onChange={(e) => setPeriod([start, e.target.value])} />
              </Field>
              <Field label="Report">
                <select className="input" value={reportType} onChange={(e) => setReportType(e.target.value)}>
                  <option value="summary">Summary</option>
                  <option value="subject-list">Summary with patient lists</option>
                  <option value="subject">One patient</option>
                </select>
              </Field>
              <Field label="Patient" hint={reportType === 'subject' ? 'Required for one patient.' : 'Empty: every patient.'}>
                <input className="input font-mono" value={subject} onChange={(e) => setSubject(e.target.value)} placeholder="pt1" />
              </Field>
            </div>
            <Field label="Resources to evaluate over (optional)" hint="NDJSON from a bulk export, a Bundle or a JSON array, Patients included. Empty: this server's FHIR store.">
              <CodeArea value={resources} onChange={setResources} language="json" rows={5} placeholder='{"resourceType":"Patient","id":"pt1","birthDate":"1960-01-01"}' />
            </Field>
            <div className="mt-3 flex flex-wrap items-center gap-3">
              <button type="button" className="btn-primary" disabled={busy || !text.trim()} onClick={evaluate}>
                Evaluate
              </button>
              {list.fhir && (
                <button type="button" className="btn" disabled={!text.trim()} onClick={save}>
                  Save to the FHIR store
                </button>
              )}
              {saved && <span className="text-sm text-emerald-300" data-testid="measure-saved">{saved}</span>}
            </div>
          </Section>

          {report && (
            <>
              <Section
                title="Populations"
                description={`${String(report.type)} report for ${start} to ${end}, from ${result?.source === 'pasted' ? 'the resources pasted above' : 'this server\u2019s FHIR store'}.`}
              >
                {groups.map((g, i) => {
                  const score = scoreOf(g)
                  return (
                    <div key={i} className="mb-4" data-testid="measure-group">
                      {groups.length > 1 && <h4 className="mb-1 text-xs text-slate-500">Group {String(g.id ?? i + 1)}</h4>}
                      <Populations pops={(g.population as Json[]) ?? []} contained={contained} />
                      {(g.population as Json[] | undefined)?.some((p) => codeOf(p.code) === 'numerator') && (
                        <p className="mt-2 text-sm" data-testid="measure-score">
                          Score <span className="font-mono text-lg text-emerald-300">{percent(score)}</span>
                          {improvement && <span className="ml-2 text-xs text-slate-500">({improvement === 'increase' ? 'higher is better' : 'lower is better'})</span>}
                          {score === null && <span className="ml-2 text-xs text-amber-300">no one is left in the denominator</span>}
                        </p>
                      )}
                    </div>
                  )
                })}
              </Section>

              {groups.some((g) => (g.stratifier as Json[] | undefined)?.length) && (
                <Section title="Stratified by">
                  {groups.flatMap((g, gi) =>
                    ((g.stratifier as Json[] | undefined) ?? []).map((st, si) => (
                      <div key={`${gi}-${si}`} className="mb-4">
                        <h4 className="mb-1 text-xs text-slate-400">{String(((st.code as Json[] | undefined)?.[0] as Json | undefined)?.text ?? `Stratifier ${si + 1}`)}</h4>
                        <table className="w-full text-left text-sm" data-testid="measure-strata">
                          <thead className="text-xs text-slate-500">
                            <tr>
                              <th className="py-1 pr-3">Stratum</th>
                              {((((st.stratum as Json[] | undefined)?.[0]?.population as Json[]) ?? [])).map((p) => (
                                <th key={codeOf(p.code)} className="py-1 pr-3">{popNames[codeOf(p.code)] ?? codeOf(p.code)}</th>
                              ))}
                              <th className="py-1">Score</th>
                            </tr>
                          </thead>
                          <tbody className="text-slate-300">
                            {((st.stratum as Json[] | undefined) ?? []).map((s, k) => (
                              <tr key={k} className="border-t border-slate-800">
                                <td className="py-1 pr-3 text-cyan-300">{String((s.value as Json | undefined)?.text ?? '')}</td>
                                {((s.population as Json[]) ?? []).map((p) => (
                                  <td key={codeOf(p.code)} className="py-1 pr-3 font-mono">{String(p.count ?? 0)}</td>
                                ))}
                                <td className="py-1 font-mono text-emerald-300">{percent(scoreOf(s))}</td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    )),
                  )}
                </Section>
              )}

              <Section title="The MeasureReport">
                <div className="mb-2">
                  <button type="button" className="btn" onClick={download}>Download MeasureReport</button>
                </div>
                <CodeArea value={JSON.stringify(report, null, 2)} onChange={() => {}} language="json" rows={14} readOnly />
              </Section>
            </>
          )}
        </>
      )}
    </div>
  )
}
