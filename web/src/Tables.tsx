import { useEffect, useState } from 'react'
import { HBar } from './charts'
import { ErrorBox, Section, Spinner } from './ui'
import type { UiError } from './store'
import { NewTable } from './NewTable'

type TableUse = {
  channel: string
  step: string
  path: string
  running: boolean
}

type TableImpact = {
  name: string
  file: string
  describes?: string
  decidedBy?: string
  decidedOn?: string
  source?: string
  entries: number
  usedBy: TableUse[]
  channels: number
  running: number
  paths: string[]
}

type CodesetsResponse = {
  tables: TableImpact[]
  unreferenced: number
}

/**
 * Tables shows shared mapping tables and what each one affects.
 *
 * The reason this page exists is the blast radius. A shared table is shared precisely so one edit reaches every
 * channel that uses it — that is the feature, and it is also the hazard, because the consequences of an edit are
 * otherwise invisible until messages start arriving translated differently.
 */
export function Tables() {
  const [data, setData] = useState<CodesetsResponse | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [loading, setLoading] = useState(true)
  const [expanded, setExpanded] = useState<string | null>(null)

  // Bumped after a save so the listing picks up the new table without a page reload.
  const [reloadCount, setReloadCount] = useState(0)
  const reload = () => setReloadCount((n) => n + 1)

  // Held here rather than in the form, which is unmounted when the first table turns this section from its empty
  // state into its populated one.
  const [savedNotes, setSavedNotes] = useState<string[] | null>(null)

  const afterSave = (notes: string[]) => {
    setSavedNotes(notes)
    reload()
  }

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const res = await fetch('/api/codesets')
        if (!res.ok) {
          const body = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
          throw new Error(body.error ?? `HTTP ${res.status}`)
        }
        const body = (await res.json()) as CodesetsResponse
        if (!cancelled) {
          setData(body)
          setError(null)
        }
      } catch (e) {
        if (!cancelled) setError({ message: e instanceof Error ? e.message : String(e), problems: [] })
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [reloadCount])

  if (loading && !data) return <Spinner />
  if (error && !data) return <ErrorBox error={error} />
  if (!data) return null

  if (data.tables.length === 0) {
    return (
      <Section
        title="Shared mapping tables"
        description="One table, used by every channel that needs it, with a record of who decided each mapping and why."
      >
        {/* The form first, the YAML second.
            
            This panel used to be an explanation followed by a file to write by hand on the server. The
            explanation is worth keeping - it is the reason the feature exists - but a section whose only content
            is instructions for work you must do elsewhere is not a section. The example is still here, behind a
            disclosure, because somebody managing tables in version control wants the format. */}
        {savedNotes !== null && (
          <div role="status" className="mb-4 rounded-lg border border-emerald-900/60 bg-emerald-950/30 p-3 text-xs">
            <p className="font-medium text-emerald-200">Table saved.</p>
            <ul className="mt-1 space-y-1 text-emerald-300/80">
              {savedNotes.map((n) => (
                <li key={n}>{n}</li>
              ))}
            </ul>
          </div>
        )}
        <div className="mb-4">
          <NewTable onSaved={afterSave} />
        </div>

        <div className="rounded-xl border border-dashed border-slate-800 p-6">
          <p className="text-sm text-slate-400">
            No shared tables yet. Most of what makes an interface hard to maintain is that nobody knows{' '}
            <em>why</em> a mapping is the way it is, and the person who knew has left. A table with a reason attached
            can be argued with; one without becomes something nobody dares change and nobody dares delete.
          </p>
          <details className="mt-4">
            <summary className="cursor-pointer text-xs text-slate-400 hover:text-slate-300">
              or write the file yourself
            </summary>
          <pre className="mt-3 overflow-x-auto rounded-lg bg-slate-950 p-3 font-mono text-xs text-slate-300">
            {`# codes.codeset.yaml
tables:
  - name: sex-to-lab
    describes: hospital sex codes into the lab's numeric codes
    decided_by: Dave, during the 2019 migration
    decided_on: 2019-04-11
    source: the lab's interface specification, version 3
    entries:
      - from: U
        to: "9"
        why: the lab has no 'unknown', and 9 is its 'not stated'

# then in the channel
tables:
  - codes.codeset.yaml
transformations:
  - map:
      path: PID-8
      use: sex-to-lab`}
          </pre>
          </details>
        </div>
      </Section>
    )
  }

  const widest = data.tables.filter((t) => t.channels > 0)

  return (
    <Section
      title="Shared mapping tables"
      description="What each table affects, so an edit is a decision rather than a surprise. Widest reach first."
    >
      <div className="space-y-4">
        {/* Somewhere to make one, not only somewhere to read about them.
            
            First, because on a fresh installation everything below this says there is nothing yet - and a
            section whose entire content is an explanation of what you cannot do here is not a section. */}
        {savedNotes !== null && (
          <div role="status" className="mb-4 rounded-lg border border-emerald-900/60 bg-emerald-950/30 p-3 text-xs">
            <p className="font-medium text-emerald-200">Table saved.</p>
            <ul className="mt-1 space-y-1 text-emerald-300/80">
              {savedNotes.map((n) => (
                <li key={n}>{n}</li>
              ))}
            </ul>
          </div>
        )}
        <NewTable onSaved={afterSave} />

        {widest.length > 1 && (
          <div className="rounded-xl border border-slate-800 p-4">
            <h3 className="mb-3 text-sm font-medium text-slate-300">Channels affected by each table</h3>
            <HBar
              rows={widest.map((t) => ({
                name: t.name,
                value: t.channels,
                // Amber once a table reaches several running channels: not an error, but the point at which an
                // edit stops being a local change.
                colour: t.running >= 3 ? '#fbbf24' : '#38bdf8',
                note: t.running > 0 ? `${t.running} running now` : 'none running',
              }))}
            />
          </div>
        )}

        {data.tables.map((t) => {
          const open = expanded === `${t.file}:${t.name}`
          const unused = t.channels === 0

          return (
            <div
              key={`${t.file}:${t.name}`}
              className={`rounded-xl border p-4 ${
                unused ? 'border-slate-800 bg-slate-900/30' : 'border-slate-800'
              }`}
            >
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <h3 className="font-medium text-slate-100">{t.name}</h3>
                    <span className="font-mono text-xs text-slate-500">{t.file}</span>
                    {unused && (
                      <span className="rounded bg-slate-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-slate-400">
                        used by nothing
                      </span>
                    )}
                  </div>
                  {t.describes && <p className="mt-1 text-sm text-slate-400">{t.describes}</p>}
                </div>

                <div className="shrink-0 text-right">
                  {/* The headline is a sentence rather than a number, because "3" beside a table name does not
                      say three of what, and the answer is the whole point of the page. */}
                  <div className="text-sm font-medium text-slate-200">
                    {unused ? (
                      'no channel uses this'
                    ) : (
                      <>
                        editing this changes{' '}
                        <span className={t.running >= 3 ? 'text-amber-300' : 'text-sky-300'}>
                          {t.channels} channel{t.channels === 1 ? '' : 's'}
                        </span>
                      </>
                    )}
                  </div>
                  <div className="mt-0.5 text-xs text-slate-500">
                    {t.entries} entr{t.entries === 1 ? 'y' : 'ies'}
                    {t.paths.length > 0 && <> · writes {t.paths.join(', ')}</>}
                  </div>
                </div>
              </div>

              {/* Provenance, which is the half nobody else builds. A mapping with a reason can be argued with. */}
              {(t.decidedBy || t.decidedOn || t.source) && (
                <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 rounded-lg bg-slate-950/50 px-3 py-2 text-xs text-slate-400">
                  {t.decidedBy && (
                    <span>
                      decided by <span className="text-slate-300">{t.decidedBy}</span>
                    </span>
                  )}
                  {t.decidedOn && <span>on {t.decidedOn}</span>}
                  {t.source && <span>source: {t.source}</span>}
                </div>
              )}

              {t.running > 0 && (
                <p className="mt-3 rounded-lg border border-amber-900/60 bg-amber-950/25 p-3 text-sm text-amber-200">
                  {t.running} of these channel{t.running === 1 ? ' is' : 's are'} running now, so a change takes
                  effect on the next message through {t.running === 1 ? 'it' : 'them'}.
                </p>
              )}

              {!unused && (
                <>
                  <button
                    type="button"
                    onClick={() => setExpanded(open ? null : `${t.file}:${t.name}`)}
                    className="mt-3 text-xs text-sky-400 hover:text-sky-300"
                  >
                    {open ? 'Hide' : 'Show'} the {t.usedBy.length} place
                    {t.usedBy.length === 1 ? '' : 's'} it is used
                  </button>

                  {open && (
                    <ul className="mt-2 divide-y divide-slate-800 rounded-lg border border-slate-800">
                      {t.usedBy.map((use, i) => (
                        <li key={`${use.channel}-${use.path}-${i}`} className="flex items-center gap-3 px-3 py-2">
                          <span
                            className={`h-2 w-2 shrink-0 rounded-full ${
                              use.running ? 'bg-emerald-400' : 'bg-slate-600'
                            }`}
                            title={use.running ? 'running' : 'not running'}
                          />
                          <span className="min-w-0 flex-1 truncate text-sm text-slate-200">{use.channel}</span>
                          <span className="truncate text-xs text-slate-500">{use.step}</span>
                          <span className="shrink-0 font-mono text-xs text-slate-400">{use.path}</span>
                        </li>
                      ))}
                    </ul>
                  )}
                </>
              )}
            </div>
          )
        })}

        {data.unreferenced > 0 && (
          <p className="text-xs text-slate-500">
            {data.unreferenced} table{data.unreferenced === 1 ? ' is' : 's are'} loaded but referenced by no
            channel. Either something is misspelled, or {data.unreferenced === 1 ? 'it' : 'they'} can be removed.
          </p>
        )}
      </div>
    </Section>
  )
}
