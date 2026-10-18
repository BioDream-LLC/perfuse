import { useCallback, useEffect, useRef, useState } from 'react'
import { useSelector } from 'react-redux'
import { api, ApiError, atLeast } from './api'
import type { CodeSystemInfo, Concept, TableDiff, TableEdition, TableEditions, UnmappedValue } from './api'
import { ErrorBox, Section, Spinner } from './ui'
import type { RootState, UiError } from './store'
import { IconCheck, IconDelete, IconEdit, IconSearch, IconUpload, IconWarning } from './Icons'

function toError(e: unknown): UiError {
  return { message: e instanceof Error ? e.message : String(e), problems: e instanceof ApiError ? e.problems : [] }
}

function when(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

/** StatusChip colours an edition by what it means for a message today. */
function StatusChip({ e }: { e: TableEdition }) {
  if (e.inForce)
    return (
      <span className="rounded bg-emerald-950/60 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-emerald-300">
        in force
      </span>
    )
  if (e.status === 'draft')
    return (
      <span className="rounded bg-amber-950/60 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-amber-300">
        draft, waiting for approval
      </span>
    )
  if (e.status === 'retired')
    return <span className="rounded bg-slate-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-slate-400">retired</span>
  const today = new Date().toISOString().slice(0, 10)
  if (e.effectiveFrom && e.effectiveFrom > today)
    return (
      <span className="rounded bg-sky-950/60 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-sky-300">
        approved, from {e.effectiveFrom}
      </span>
    )
  return <span className="rounded bg-slate-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-slate-400">superseded</span>
}

/**
 * TableVersions lists a table's editions with what can be done to each: approve a draft (from a date), retire an edition, delete a
 * draft, compare one with the edition in force, or start a new version from it.
 */
export function TableVersions({
  file,
  name,
  onEdit,
  onChanged,
}: {
  file: string
  name: string
  onEdit: (edition: TableEdition) => void
  onChanged: () => void
}) {
  const [data, setData] = useState<TableEditions | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [from, setFrom] = useState<Record<string, string>>({})
  const [diff, setDiff] = useState<{ version: string; diff: TableDiff } | null>(null)

  const load = useCallback(async () => {
    try {
      setData(await api.tableEditions(file, name))
      setError(null)
    } catch (e) {
      setError(toError(e))
    }
  }, [file, name])

  useEffect(() => {
    void load()
  }, [load])

  async function act(run: () => Promise<{ notes: string[] }>) {
    setBusy(true)
    setError(null)
    setNote('')
    try {
      const res = await run()
      setNote(res.notes.join(' '))
      await load()
      onChanged()
    } catch (e) {
      setError(toError(e))
    } finally {
      setBusy(false)
    }
  }

  async function compare(version: string) {
    if (!data) return
    try {
      setDiff({ version, diff: await api.tableDiff(file, name, data.inForce, version) })
    } catch (e) {
      setError(toError(e))
    }
  }

  if (!data) return error ? <ErrorBox error={error} /> : <Spinner />

  return (
    <div className="mt-3 space-y-2" data-testid={`table-versions-${name}`}>
      {note && (
        <p role="status" className="rounded-lg border border-emerald-900/60 bg-emerald-950/30 p-2 text-xs text-emerald-300">
          {note}
        </p>
      )}
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      <ul className="divide-y divide-slate-800 rounded-lg border border-slate-800">
        {data.editions.map((e) => {
          const v = e.version ?? ''
          return (
            <li key={v || '(none)'} className="space-y-2 px-3 py-2">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-sm text-slate-200">version {v || '(none)'}</span>
                <StatusChip e={e} />
                <span className="text-xs text-slate-500">
                  {e.entries.length} entr{e.entries.length === 1 ? 'y' : 'ies'}
                  {e.effectiveFrom && <> · from {e.effectiveFrom}</>}
                  {e.effectiveUntil && <> · until {e.effectiveUntil}</>}
                  {e.author && <> · saved by {e.author}</>}
                  {e.approvedBy && (
                    <>
                      {' '}
                      · approved by <span className="text-emerald-400">{e.approvedBy}</span> on {e.approvedOn}
                    </>
                  )}
                </span>
              </div>
              {((e.unknownFrom?.length ?? 0) > 0 || (e.unknownTo?.length ?? 0) > 0) && (
                <p className="flex items-start gap-1.5 text-xs text-amber-300">
                  <IconWarning className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                  <span>
                    {(e.unknownFrom?.length ?? 0) > 0 && (
                      <>
                        Not codes in {e.sourceSystem}: <span className="font-mono">{e.unknownFrom!.join(', ')}</span>.{' '}
                      </>
                    )}
                    {(e.unknownTo?.length ?? 0) > 0 && (
                      <>
                        Not codes in {e.targetSystem}: <span className="font-mono">{e.unknownTo!.join(', ')}</span>.
                      </>
                    )}
                  </span>
                </p>
              )}
              <div className="flex flex-wrap items-center gap-2">
                <button type="button" className="btn-ghost py-1 text-xs" onClick={() => onEdit(e)} disabled={busy}>
                  <IconEdit className="h-3.5 w-3.5" aria-hidden="true" /> {e.status === 'draft' ? 'Edit draft' : 'New version from this'}
                </button>
                {e.status === 'draft' && (
                  <>
                    <label className="flex items-center gap-1 text-xs text-slate-400">
                      in force from
                      <input
                        type="date"
                        aria-label={`Version ${v} in force from`}
                        className="input py-0.5 text-xs"
                        value={from[v] ?? e.effectiveFrom ?? ''}
                        onChange={(ev) => setFrom((f) => ({ ...f, [v]: ev.target.value }))}
                      />
                    </label>
                    <button
                      type="button"
                      className="btn-primary py-1 text-xs"
                      disabled={busy}
                      onClick={() => void act(() => api.approveEdition(file, name, v, from[v] || undefined))}
                    >
                      <IconCheck className="h-3.5 w-3.5" aria-hidden="true" /> Approve
                    </button>
                    <button
                      type="button"
                      className="btn-ghost py-1 text-xs text-rose-300"
                      disabled={busy}
                      onClick={() => void act(() => api.deleteDraftEdition(file, name, v))}
                    >
                      <IconDelete className="h-3.5 w-3.5" aria-hidden="true" /> Delete draft
                    </button>
                  </>
                )}
                {e.status !== 'draft' && e.status !== 'retired' && (
                  <button
                    type="button"
                    className="btn-ghost py-1 text-xs"
                    disabled={busy}
                    onClick={() => {
                      if (window.confirm(`Retire version ${v || '(none)'} of ${name}? A channel mapping through it stops loading unless another version is in force.`))
                        void act(() => api.retireEdition(file, name, v))
                    }}
                  >
                    Retire
                  </button>
                )}
                {!e.inForce && data.inForce !== undefined && data.editions.some((x) => x.inForce) && (
                  <button type="button" className="btn-ghost py-1 text-xs" onClick={() => void compare(v)}>
                    Compare with the version in force
                  </button>
                )}
              </div>
              {diff?.version === v && (
                <div className="rounded-lg bg-slate-950/60 p-2 font-mono text-xs" data-testid="table-diff">
                  {diff.diff.added.length + diff.diff.removed.length + diff.diff.changed.length === 0 && (
                    <p className="text-slate-500">The same entries as the version in force.</p>
                  )}
                  {diff.diff.added.map((a) => (
                    <p key={`a${a.from}`} className="text-emerald-300">
                      + {a.from} → {a.to}
                    </p>
                  ))}
                  {diff.diff.removed.map((a) => (
                    <p key={`r${a.from}`} className="text-rose-300">
                      − {a.from} → {a.to}
                    </p>
                  ))}
                  {diff.diff.changed.map((c) => (
                    <p key={`c${c.from}`} className="text-amber-300">
                      ~ {c.from}: {c.before} → {c.after}
                    </p>
                  ))}
                </div>
              )}
            </li>
          )
        })}
      </ul>
    </div>
  )
}

/** UnmappedValues lists what arrived that a table did not cover, most frequent first, with a way to map each. */
export function UnmappedValues({ onMap, reloadKey }: { onMap: (table: string, value: string) => void; reloadKey: number }) {
  const [data, setData] = useState<{ values: UnmappedValue[]; recording: boolean } | null>(null)
  const [error, setError] = useState<UiError | null>(null)

  // Editors and above only: the values are what arrived in messages, and can be patient data. A viewer is not asked to fetch
  // a list it may not read (and refused), the section is simply left out.
  const me = useSelector((s: RootState) => s.session.me)
  const mayRead = atLeast(me?.role, 'editor')

  const load = useCallback(async () => {
    if (!mayRead) {
      setData({ values: [], recording: false })
      return
    }
    try {
      setData(await api.unmapped())
      setError(null)
    } catch (e) {
      // A viewer may not read this list; the section is then left out rather than shown as broken.
      if (e instanceof ApiError && e.status === 403) setData({ values: [], recording: false })
      else setError(toError(e))
    }
  }, [mayRead])

  useEffect(() => {
    void load()
  }, [load, reloadKey])

  if (error) return <ErrorBox error={error} />
  if (!data || !data.recording) return null

  return (
    <Section
      title="Values no table maps"
      icon={IconWarning}
      description="What arrived through a map step that its table does not cover, most frequent first. Map each one, or dismiss it until it is seen again."
    >
      {data.values.length === 0 ? (
        <p className="text-sm text-emerald-400">Every value that arrived was in its table.</p>
      ) : (
        <table className="w-full text-sm" data-testid="unmapped-values">
          <thead>
            <tr className="text-left text-[11px] uppercase tracking-wide text-slate-500">
              <th className="py-1 pr-3">Table</th>
              <th className="py-1 pr-3">Value</th>
              <th className="py-1 pr-3">Seen</th>
              <th className="py-1 pr-3">Channels</th>
              <th className="py-1 pr-3">Last seen</th>
              <th />
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-800">
            {data.values.map((u) => (
              <tr key={`${u.table}|${u.value}`}>
                <td className="py-1.5 pr-3 text-slate-300">{u.table}</td>
                <td className="py-1.5 pr-3 font-mono text-amber-300">{u.value}</td>
                <td className="py-1.5 pr-3 text-slate-300">{u.count.toLocaleString()}</td>
                <td className="py-1.5 pr-3 text-xs text-slate-400">{u.channels.join(', ')}</td>
                <td className="py-1.5 pr-3 text-xs text-slate-500">{when(u.lastSeen)}</td>
                <td className="py-1.5 text-right">
                  <button type="button" className="btn-primary py-0.5 text-xs" onClick={() => onMap(u.table, u.value)}>
                    Map it
                  </button>{' '}
                  <button
                    type="button"
                    className="btn-ghost py-0.5 text-xs"
                    onClick={() => void api.dismissUnmapped(u.table, u.value).then(load, (e) => setError(toError(e)))}
                  >
                    Dismiss
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Section>
  )
}

const formats = [
  { value: 'loinc', label: 'LOINC (Loinc.csv)', licence: 'LOINC is free with a loinc.org account under its licence.' },
  { value: 'snomed', label: 'SNOMED CT (RF2 description snapshot)', licence: 'SNOMED CT needs an affiliate licence (free in member countries through the national release centre; in the US through UMLS).' },
  { value: 'rxnorm', label: 'RxNorm (RXNCONSO.RRF)', licence: 'The RxNorm full release needs a UMLS licence (free from the National Library of Medicine).' },
  { value: 'icd10cm', label: 'ICD-10-CM (CMS order or codes file)', licence: '' },
  { value: 'csv', label: 'CSV: CPT, HCPCS or local codes', licence: 'CPT needs a licence from the AMA.' },
]

/** CodeSystems lists imported code systems, imports a publisher's file, and searches codes. */
export function CodeSystems() {
  const [systems, setSystems] = useState<CodeSystemInfo[] | null>(null)
  const [available, setAvailable] = useState(true)
  const [error, setError] = useState<UiError | null>(null)
  const [note, setNote] = useState('')
  const [format, setFormat] = useState('loinc')
  const [version, setVersion] = useState('')
  const [system, setSystem] = useState('')
  const [label, setLabel] = useState('')
  const [codeColumn, setCodeColumn] = useState('code')
  const [displayColumn, setDisplayColumn] = useState('display')
  const [licensed, setLicensed] = useState(false)
  const [busy, setBusy] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  const [searchSystem, setSearchSystem] = useState('')
  const [q, setQ] = useState('')
  const [found, setFound] = useState<Concept[] | null>(null)

  const load = useCallback(async () => {
    try {
      const res = await api.termSystems()
      setSystems(res.systems)
      setAvailable(res.available)
      const first = res.systems[0]
      if (first) setSearchSystem((prev) => prev || first.system)
    } catch (e) {
      setError(toError(e))
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const fmt = formats.find((f) => f.value === format) ?? formats[0]!

  async function upload() {
    const file = fileRef.current?.files?.[0]
    if (!file) {
      setError({ message: 'Choose the file to import first.', problems: [] })
      return
    }
    setBusy(true)
    setError(null)
    setNote('')
    const params = new URLSearchParams({ format })
    if (version) params.set('version', version)
    if (format === 'csv') {
      params.set('system', system)
      params.set('codeColumn', codeColumn)
      params.set('displayColumn', displayColumn)
    }
    if (label) params.set('name', label)
    if (fmt.licence) params.set('licence', 'confirmed by the importer: ' + fmt.licence)
    try {
      const res = await fetch(`/api/terminology/import?${params}`, {
        method: 'POST',
        headers: { 'X-Perfuse-Request': '1', 'Content-Type': 'application/octet-stream' },
        body: file,
        credentials: 'same-origin',
      })
      const body = (await res.json().catch(() => ({}))) as { error?: string; concepts?: number; system?: string; version?: string }
      if (!res.ok) throw new Error(body.error ?? `HTTP ${res.status}`)
      setNote(`Imported ${body.concepts?.toLocaleString()} codes into ${body.system}, version ${body.version}.`)
      if (fileRef.current) fileRef.current.value = ''
      await load()
    } catch (e) {
      setError(toError(e))
    } finally {
      setBusy(false)
    }
  }

  async function search() {
    try {
      setFound((await api.termSearch(searchSystem, q)).concepts)
    } catch (e) {
      setError(toError(e))
    }
  }

  if (!available) return null
  if (!systems) return error ? <ErrorBox error={error} /> : <Spinner />

  return (
    <Section
      title="Code systems"
      icon={IconSearch}
      description="Published code systems imported from their publishers' files, for checking mapping tables, finding codes, and answering FHIR CodeSystem $lookup and $validate-code. Several are licensed: none ships with Perfuse."
    >
      <div className="space-y-4">
        {note && (
          <p role="status" className="rounded-lg border border-emerald-900/60 bg-emerald-950/30 p-2 text-xs text-emerald-300">
            {note}
          </p>
        )}
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

        {systems.length === 0 ? (
          <p className="text-sm text-slate-400">None imported yet.</p>
        ) : (
          <table className="w-full text-sm" data-testid="code-systems">
            <thead>
              <tr className="text-left text-[11px] uppercase tracking-wide text-slate-500">
                <th className="py-1 pr-3">Name</th>
                <th className="py-1 pr-3">System</th>
                <th className="py-1 pr-3">Version</th>
                <th className="py-1 pr-3">Codes</th>
                <th className="py-1 pr-3">Imported</th>
                <th />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-800">
              {systems.map((c) => (
                <tr key={`${c.system}|${c.version}`}>
                  <td className="py-1.5 pr-3 text-slate-200">{c.name}</td>
                  <td className="py-1.5 pr-3 font-mono text-xs text-sky-300">{c.system}</td>
                  <td className="py-1.5 pr-3 font-mono text-xs text-slate-300">{c.version}</td>
                  <td className="py-1.5 pr-3 text-emerald-300">{c.concepts.toLocaleString()}</td>
                  <td className="py-1.5 pr-3 text-xs text-slate-500">
                    {when(c.importedAt)} by {c.importedBy}
                  </td>
                  <td className="py-1.5 text-right">
                    <button
                      type="button"
                      className="btn-ghost py-0.5 text-xs text-rose-300"
                      aria-label={`Remove ${c.name} ${c.version}`}
                      onClick={() => {
                        if (window.confirm(`Remove ${c.name} version ${c.version}? Its ${c.concepts} codes are deleted.`))
                          void api.termDelete(c.system, c.version).then(load, (e) => setError(toError(e)))
                      }}
                    >
                      <IconDelete className="h-3.5 w-3.5" aria-hidden="true" />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        {/* Only once there is something to search: an empty list of code systems and a search that can never run is a
            control that does nothing (and a select with no options, which a keyboard user lands on and cannot leave). */}
        {systems.length > 0 && (
          <div className="rounded-xl border border-slate-800 p-3">
            <h3 className="mb-2 text-sm font-medium text-slate-300">Find a code</h3>
            <div className="flex flex-wrap gap-2">
              <select
                aria-label="Code system to search"
                className="input py-1 text-xs"
                value={searchSystem}
                onChange={(e) => setSearchSystem(e.target.value)}
              >
                {[...new Map(systems.map((c) => [c.system, c])).values()].map((c) => (
                  <option key={c.system} value={c.system}>
                    {c.name}
                  </option>
                ))}
              </select>
              <input
                aria-label="Code or words"
                className="input flex-1 py-1 text-xs"
                placeholder="a code, or words in its name"
                value={q}
                onChange={(e) => setQ(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && void search()}
              />
              <button type="button" className="btn-ghost py-1 text-xs" onClick={() => void search()} disabled={!searchSystem || !q}>
                <IconSearch className="h-3.5 w-3.5" aria-hidden="true" /> Search
              </button>
            </div>
            {found && (
              <ul className="mt-2 max-h-72 divide-y divide-slate-800 overflow-y-auto text-sm" data-testid="code-search-results">
                {found.length === 0 && <li className="py-1 text-slate-500">No codes match.</li>}
                {found.map((c) => (
                  <li key={c.code} className="flex gap-3 py-1">
                    <span className="w-28 shrink-0 font-mono text-sky-300">{c.code}</span>
                    <span className={c.active ? 'text-slate-200' : 'text-slate-500 line-through'}>{c.display}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}

        <details className="rounded-xl border border-slate-800 p-3">
          <summary className="cursor-pointer text-sm font-medium text-slate-300">Import a code system</summary>
          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            <label className="text-xs text-slate-400">
              Format
              <select className="input mt-1 w-full py-1 text-xs" value={format} onChange={(e) => setFormat(e.target.value)}>
                {formats.map((f) => (
                  <option key={f.value} value={f.value}>
                    {f.label}
                  </option>
                ))}
              </select>
            </label>
            <label className="text-xs text-slate-400">
              Version
              <input className="input mt-1 w-full py-1 text-xs" value={version} onChange={(e) => setVersion(e.target.value)} placeholder="2.80, 2026, 20250901" />
            </label>
            {format === 'csv' && (
              <>
                <label className="text-xs text-slate-400">
                  System URL
                  <input className="input mt-1 w-full py-1 font-mono text-xs" value={system} onChange={(e) => setSystem(e.target.value)} placeholder="http://www.ama-assn.org/go/cpt" />
                </label>
                <label className="text-xs text-slate-400">
                  Name
                  <input className="input mt-1 w-full py-1 text-xs" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="CPT" />
                </label>
                <label className="text-xs text-slate-400">
                  Code column
                  <input className="input mt-1 w-full py-1 text-xs" value={codeColumn} onChange={(e) => setCodeColumn(e.target.value)} />
                </label>
                <label className="text-xs text-slate-400">
                  Display column
                  <input className="input mt-1 w-full py-1 text-xs" value={displayColumn} onChange={(e) => setDisplayColumn(e.target.value)} />
                </label>
              </>
            )}
            <label className="text-xs text-slate-400 sm:col-span-2">
              The file
              <input ref={fileRef} type="file" className="mt-1 block w-full text-xs" aria-label="Code system file" />
            </label>
          </div>
          {fmt.licence && (
            <label className="mt-3 flex items-start gap-2 text-xs text-slate-400">
              <input type="checkbox" className="mt-0.5" checked={licensed} onChange={(e) => setLicensed(e.target.checked)} />
              <span>
                <span className="block text-slate-300">This organisation holds the licence for this file</span>
                <span className="mt-0.5 block text-[11px] text-slate-500">{fmt.licence} Recorded with the import.</span>
              </span>
            </label>
          )}
          <button
            type="button"
            className="btn-primary mt-3 py-1 text-xs"
            disabled={busy || (!!fmt.licence && !licensed) || (format === 'csv' && !system)}
            onClick={() => void upload()}
          >
            <IconUpload className="h-3.5 w-3.5" aria-hidden="true" /> {busy ? 'Importing… a large file takes a minute' : 'Import'}
          </button>
        </details>
      </div>
    </Section>
  )
}
