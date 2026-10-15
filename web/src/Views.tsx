import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from './api'
import type { ViewsList, ViewRunResult } from './api'
import { CodeArea } from './CodeArea'
import { ErrorBox, Field, Section, Spinner } from './ui'
import type { UiError } from './store'

/**
 * SQL on FHIR views: ViewDefinitions that flatten FHIR resources into tables. Write or pick one, run it over this server's
 * FHIR store or over resources pasted in (NDJSON from a bulk export, a Bundle), see the rows, and download them as CSV,
 * NDJSON or Parquet for a spreadsheet, DuckDB, Athena or Spark. Saved views are also run by the FHIR endpoint's $sql-run.
 */

const toError = (e: unknown): UiError => ({
  message: e instanceof Error ? e.message : String(e),
  problems: e instanceof ApiError ? e.problems : [],
})

const showCell = (v: unknown) => {
  if (v === null || v === undefined) return <span className="text-slate-600">null</span>
  if (typeof v === 'boolean') return <span className={v ? 'text-emerald-300' : 'text-amber-300'}>{String(v)}</span>
  if (typeof v === 'number') return <span className="text-violet-300">{v}</span>
  if (typeof v === 'object') return <span className="text-cyan-300">{JSON.stringify(v)}</span>
  return String(v)
}

export function Views() {
  const [list, setList] = useState<ViewsList | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [text, setText] = useState('')
  const [name, setName] = useState('')
  const [resources, setResources] = useState('')
  const [patient, setPatient] = useState('')
  const [limit, setLimit] = useState(200)
  const [result, setResult] = useState<ViewRunResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState('')

  const load = useCallback(() => {
    api.views().then((l) => {
      setList(l)
      setText((t) => t || (l.examples[0] ? JSON.stringify(JSON.parse(l.examples[0].view), null, 2) : ''))
    }).catch((e) => setError(toError(e)))
  }, [])
  useEffect(load, [load])

  const parsed = (): unknown | null => {
    try {
      return JSON.parse(text)
    } catch (e) {
      setError({ message: 'The view is not JSON: ' + (e instanceof Error ? e.message : String(e)), problems: [] })
      return null
    }
  }

  const run = () => {
    const view = parsed()
    if (!view) return
    setBusy(true)
    setError(null)
    api
      .viewRun({ view, resources, patient, limit })
      .then(setResult)
      .catch((e) => {
        setResult(null)
        setError(toError(e))
      })
      .finally(() => setBusy(false))
  }

  const download = (format: string) => {
    const view = parsed()
    if (!view) return
    setError(null)
    api.viewDownload({ view, resources, patient, limit: 100000, format }).catch((e) => setError(toError(e)))
  }

  const open = (raw: string) => {
    try {
      const v = JSON.parse(raw)
      setText(JSON.stringify(v, null, 2))
      setName(typeof v.name === 'string' ? v.name : '')
      setResult(null)
      setSaved('')
    } catch {
      setText(raw)
    }
  }

  const save = () => {
    const view = parsed() as Record<string, unknown> | null
    if (!view) return
    const n = (name || (typeof view.name === 'string' ? view.name : '')).trim()
    if (!n) {
      setError({ message: 'Give the view a name to save it under.', problems: [] })
      return
    }
    view.name = n
    api
      .viewSave(n, view)
      .then(() => {
        setSaved(`Saved as ${n}. The FHIR endpoint runs it as $sql-run?subjectReference=ViewDefinition/${n}.`)
        setText(JSON.stringify(view, null, 2))
        load()
      })
      .catch((e) => setError(toError(e)))
  }

  const remove = (n: string) => {
    if (!window.confirm(`Delete the view ${n}?`)) return
    api.viewDelete(n).then(load).catch((e) => setError(toError(e)))
  }

  if (!list && !error) return <Spinner />

  return (
    <div className="space-y-6" data-testid="views">
      {error && <ErrorBox error={error} />}
      {list && (
        <>
          <Section
            title="SQL on FHIR views"
            description={
              <>
                A ViewDefinition (SQL on FHIR v2) turns each FHIR resource of one type into rows of named columns, by FHIRPath, so the
                data can be read by any SQL engine or spreadsheet. Start from an example or a saved view, run it, and download the
                table.{' '}
                {list.fhir
                  ? 'It runs over this server\u2019s FHIR store, or over resources you paste.'
                  : 'This server has no FHIR store (-fhir), so paste the resources to run it over.'}
              </>
            }
          >
            <div className="flex flex-wrap gap-2" data-testid="view-picks">
              {list.examples.map((e) => (
                <button key={e.name} type="button" className="rounded-full border border-slate-700 px-3 py-1 text-xs text-slate-300 hover:border-cyan-600"
                  onClick={() => open(e.view)} title="An example">
                  {e.title || e.name}
                </button>
              ))}
            </div>
            {list.views.length > 0 && (
              <table className="mt-4 w-full text-left text-sm" data-testid="views-saved">
                <thead className="text-xs text-slate-500">
                  <tr>
                    <th className="py-1 pr-2">Saved view</th>
                    <th className="py-1 pr-2">Reads</th>
                    <th className="py-1 pr-2">Columns</th>
                    <th className="py-1" />
                  </tr>
                </thead>
                <tbody className="text-slate-300">
                  {list.views.map((v) => (
                    <tr key={v.name} className="border-t border-slate-800 align-top">
                      <td className="py-1.5 pr-2">
                        <span className="font-mono text-xs text-slate-100">{v.name}</span>
                        {v.title && <span className="block text-xs text-slate-500">{v.title}</span>}
                      </td>
                      <td className="py-1.5 pr-2 text-xs text-violet-300">{v.resource}</td>
                      <td className="py-1.5 pr-2 font-mono text-xs">
                        {v.problem ? <span className="text-red-300">{v.problem}</span> : v.columns.join(', ')}
                      </td>
                      <td className="py-1.5 text-right text-xs whitespace-nowrap">
                        <button type="button" className="text-cyan-300 hover:underline"
                          onClick={() => api.viewGet(v.name).then((raw) => open(raw)).catch((e) => setError(toError(e)))}>
                          Open
                        </button>
                        <button type="button" className="ml-3 text-red-300 hover:underline" onClick={() => remove(v.name)}>
                          Delete
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Section>

          <Section title="The view">
            <Field label="ViewDefinition (JSON)" hint="resource, select with column, forEach, forEachOrNull, repeat and unionAll, where, constant.">
              <CodeArea value={text} onChange={setText} language="json" rows={16} />
            </Field>
            <div className="mt-3 grid gap-3 sm:grid-cols-3">
              <Field label="Run for one patient" hint="Patient id. Empty: every patient.">
                <input className="input font-mono" value={patient} onChange={(e) => setPatient(e.target.value)} placeholder="pt1" />
              </Field>
              <Field label="Rows to show">
                <input className="input" type="number" min={1} max={100000} value={limit} onChange={(e) => setLimit(Number(e.target.value) || 200)} />
              </Field>
              <Field label="Save as" hint="A name: letters, digits and _.">
                <input className="input font-mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="patient_demographics" />
              </Field>
            </div>
            <Field label="Resources to run over (optional)" hint="NDJSON from a bulk export, a Bundle or a JSON array. Empty: this server's FHIR store.">
              <CodeArea value={resources} onChange={setResources} language="json" rows={5} placeholder='{"resourceType":"Patient","id":"pt1"}' />
            </Field>
            <div className="mt-3 flex flex-wrap items-center gap-3">
              <button type="button" className="btn-primary" disabled={busy || !text.trim()} onClick={run}>
                Run
              </button>
              {(['csv', 'ndjson', 'parquet'] as const).map((f) => (
                <button key={f} type="button" className="btn" disabled={busy || !text.trim()} onClick={() => download(f)}>
                  Download {f === 'ndjson' ? 'NDJSON' : f.toUpperCase()}
                </button>
              ))}
              <button type="button" className="btn" disabled={!text.trim()} onClick={save}>
                Save
              </button>
              {saved && <span className="text-sm text-emerald-300" data-testid="view-saved">{saved}</span>}
            </div>
          </Section>

          {result && (
            <Section
              title="Rows"
              description={`${result.rows.length}${result.truncated ? '+' : ''} row${result.rows.length === 1 ? '' : 's'} from ${result.resource} resources ${result.source === 'pasted' ? 'pasted above' : 'in this server\u2019s FHIR store'}.`}
            >
              {result.rows.length === 0 ? (
                <p className="text-sm text-slate-400" data-testid="view-empty">No resource gave a row.</p>
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-left text-sm" data-testid="view-rows">
                    <thead className="text-xs text-slate-500">
                      <tr>
                        {result.columns.map((c) => (
                          <th key={c.name} className="py-1 pr-3 font-mono">
                            {c.name}
                            {c.type && <span className="ml-1 text-slate-600">{c.type}</span>}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody className="font-mono text-xs text-slate-300">
                      {result.rows.map((r, i) => (
                        <tr key={i} className="border-t border-slate-800">
                          {r.map((v, j) => (
                            <td key={j} className="py-1 pr-3">{showCell(v)}</td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              {result.truncated && <p className="mt-2 text-xs text-amber-300">There are more rows; download the table for all of them.</p>}
            </Section>
          )}
        </>
      )}
    </div>
  )
}
