import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from './api'
import type { DICOMwebStatus, DicomJSON } from './api'
import { ErrorBox, Field, Section, Spinner } from './ui'
import type { UiError } from './store'

/**
 * Imaging: the archive this server keeps and serves over DICOMweb.
 *
 * Studies are searched with QIDO-RS, the same request a viewer makes, so what this page shows is what OHIF or any other
 * DICOMweb client will see. Objects arrive from a channel's DICOMweb destination, from STOW-RS, or from a viewer that
 * uploads. A single object downloads with WADO-URI; nothing is rendered here, because Perfuse moves images and a viewer
 * displays them.
 */

const first = (o: DicomJSON | undefined, tag: string): string => {
  const v = o?.[tag]?.Value?.[0]
  if (v === undefined || v === null) return ''
  if (typeof v === 'object' && 'Alphabetic' in (v as Record<string, unknown>)) {
    return String((v as Record<string, unknown>).Alphabetic ?? '').replace(/\^+/g, ' ').trim()
  }
  return String(v)
}
const all = (o: DicomJSON | undefined, tag: string): string => (o?.[tag]?.Value ?? []).map(String).join(', ')
const date = (d: string) => (d.length === 8 ? `${d.slice(0, 4)}-${d.slice(4, 6)}-${d.slice(6, 8)}` : d)

const toError = (e: unknown): UiError => ({
  message: e instanceof Error ? e.message : String(e),
  problems: e instanceof ApiError ? e.problems : [],
})

function size(bytes = 0): string {
  if (bytes > 1 << 30) return `${(bytes / (1 << 30)).toFixed(1)} GB`
  if (bytes > 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MB`
  return `${Math.round(bytes / 1024)} KB`
}

export function Imaging() {
  const [status, setStatus] = useState<DICOMwebStatus | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [name, setName] = useState('')
  const [patientId, setPatientId] = useState('')
  const [accession, setAccession] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [studies, setStudies] = useState<DicomJSON[] | null>(null)
  // searched says what the last search asked for, so the answer names its question: a search that changes nothing on the
  // screen reads as a button that does nothing.
  const [searched, setSearched] = useState('')
  const [open, setOpen] = useState<string | null>(null)
  const [series, setSeries] = useState<DicomJSON[]>([])
  const [instances, setInstances] = useState<Record<string, DicomJSON[]>>({})

  useEffect(() => {
    api.dicomwebStatus().then(setStatus).catch((e) => setError(toError(e)))
  }, [])

  const search = useCallback(async () => {
    setError(null)
    const q = new URLSearchParams()
    if (name.trim()) {
      q.set('PatientName', name.trim())
      q.set('fuzzymatching', 'true')
    }
    if (patientId.trim()) q.set('PatientID', patientId.trim())
    if (accession.trim()) q.set('AccessionNumber', accession.trim())
    if (from || to) q.set('StudyDate', `${from.replaceAll('-', '')}-${to.replaceAll('-', '')}`)
    const asked = [...q.entries()].filter(([k]) => k !== 'fuzzymatching').map(([k, v]) => `${k} ${v}`).join(', ')
    q.set('limit', '200')
    try {
      setStudies(await api.qido(`/studies?${q.toString()}`))
      setSearched(asked)
      setOpen(null)
    } catch (e) {
      setError(toError(e))
    }
  }, [name, patientId, accession, from, to])

  useEffect(() => {
    if (status?.enabled && status.readable) void search()
    // Searched once when the page opens; afterwards only when asked.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status])

  async function openStudy(uid: string) {
    if (open === uid) {
      setOpen(null)
      return
    }
    try {
      setSeries(await api.qido(`/studies/${uid}/series`))
      setInstances({})
      setOpen(uid)
    } catch (e) {
      setError(toError(e))
    }
  }

  async function openSeries(study: string, uid: string) {
    try {
      const list = await api.qido(`/studies/${study}/series/${uid}/instances`)
      setInstances((m) => ({ ...m, [uid]: list }))
    } catch (e) {
      setError(toError(e))
    }
  }

  if (status === null && error === null) return <Spinner label="Loading the imaging archive…" />

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-medium text-slate-100">Imaging archive</h2>
        <p className="mt-1 text-sm text-slate-400">
          Studies this server keeps, searched the way a viewer searches them (QIDO-RS). Channels fill it with a DICOMweb
          destination; viewers such as OHIF read it over DICOMweb.
        </p>
      </div>
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

      {status && !status.enabled && (
        <Section title="No archive on this server">
          <p className="text-sm text-slate-300">{status.hint}</p>
        </Section>
      )}

      {status?.enabled && (
        <>
          <div className="grid gap-4 sm:grid-cols-4" data-testid="imaging-counts">
            {(
              [
                ['Studies', status.studies],
                ['Series', status.series],
                ['Instances', status.instances],
                ['Stored', size(status.bytes)],
              ] as const
            ).map(([label, value]) => (
              <div key={label} className="rounded-xl border border-slate-800 bg-slate-900/60 p-4">
                <p className="text-xs uppercase tracking-wide text-slate-500">{label}</p>
                <p className="mt-1 text-2xl font-semibold text-cyan-300">{value}</p>
              </div>
            ))}
          </div>

          <Section title="For a viewer">
            <p className="text-sm text-slate-300">
              DICOMweb service root: <code className="font-mono text-cyan-300" data-testid="dicomweb-base">{status.base}</code>
            </p>
            <p className="mt-2 text-xs text-slate-400">{status.note}</p>
            <pre className="mt-3 overflow-auto rounded-lg bg-slate-950 p-3 font-mono text-xs text-slate-300">
              {JSON.stringify(
                {
                  qidoRoot: status.base,
                  wadoRoot: status.base,
                  wadoUriRoot: `${status.base}/wado`,
                  qidoSupportsIncludeField: false,
                  imageRendering: 'wadors',
                  thumbnailRendering: 'wadors',
                  supportsFuzzyMatching: true,
                },
                null,
                2,
              )}
            </pre>
            <p className="mt-2 text-xs text-slate-500">
              The data source settings for an OHIF viewer. Browser origins allowed to call it:{' '}
              {status.corsOrigins?.length ? status.corsOrigins.join(', ') : 'none (set them under Settings › Data › Imaging archive)'}.
            </p>
          </Section>

          {status.readable === false ? (
            <Section title="Studies">
              <p className="text-sm text-amber-300">
                This account is limited to a tenant or to channel groups, and the archive belongs to the whole server.
              </p>
            </Section>
          ) : (
            <Section title="Studies">
              <form
                className="grid gap-3 sm:grid-cols-6"
                onSubmit={(e) => {
                  e.preventDefault()
                  void search()
                }}
              >
                <Field label="Patient name">
                  <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
                </Field>
                <Field label="Patient ID">
                  <input className="input font-mono" value={patientId} onChange={(e) => setPatientId(e.target.value)} />
                </Field>
                <Field label="Accession">
                  <input className="input font-mono" value={accession} onChange={(e) => setAccession(e.target.value)} />
                </Field>
                <Field label="From">
                  <input type="date" className="input" value={from} onChange={(e) => setFrom(e.target.value)} />
                </Field>
                <Field label="To">
                  <input type="date" className="input" value={to} onChange={(e) => setTo(e.target.value)} />
                </Field>
                <div className="flex items-end">
                  <button className="btn-primary w-full" type="submit">
                    Search
                  </button>
                </div>
              </form>

              {studies !== null && (
                <p className="mt-4 text-sm text-slate-400" data-testid="imaging-result" aria-live="polite">
                  {studies.length === 0 ? 'No study matches' : `${studies.length} ${studies.length === 1 ? 'study matches' : 'studies match'}`}
                  {searched ? `: ${searched}.` : ' (every study).'}
                </p>
              )}
              {studies !== null && studies.length > 0 && (
                <table className="mt-4 w-full text-left text-sm" data-testid="imaging-studies">
                  <thead className="text-xs text-slate-500">
                    <tr>
                      <th className="py-1 pr-2">Date</th>
                      <th className="py-1 pr-2">Patient</th>
                      <th className="py-1 pr-2">ID</th>
                      <th className="py-1 pr-2">Accession</th>
                      <th className="py-1 pr-2">Description</th>
                      <th className="py-1 pr-2">Modalities</th>
                      <th className="py-1">Series / instances</th>
                    </tr>
                  </thead>
                  <tbody className="text-slate-300">
                    {studies.map((s) => {
                      const uid = first(s, '0020000D')
                      return [
                        <tr
                          key={uid}
                          className="cursor-pointer border-t border-slate-800 hover:bg-slate-800/40"
                          onClick={() => void openStudy(uid)}
                        >
                          <td className="py-1.5 pr-2 font-mono text-xs">{date(first(s, '00080020'))}</td>
                          <td className="py-1.5 pr-2 text-slate-100">{first(s, '00100010')}</td>
                          <td className="py-1.5 pr-2 font-mono text-xs">{first(s, '00100020')}</td>
                          <td className="py-1.5 pr-2 font-mono text-xs">{first(s, '00080050')}</td>
                          <td className="py-1.5 pr-2">{first(s, '00081030')}</td>
                          <td className="py-1.5 pr-2 text-cyan-300">{all(s, '00080061')}</td>
                          <td className="py-1.5 font-mono text-xs">
                            {first(s, '00201206')} / {first(s, '00201208')}
                          </td>
                        </tr>,
                        open === uid && (
                          <tr key={uid + '-series'} className="bg-slate-900/50">
                            <td colSpan={7} className="p-3">
                              <ul className="space-y-2" data-testid="imaging-series">
                                {series.map((r) => {
                                  const suid = first(r, '0020000E')
                                  return (
                                    <li key={suid} className="rounded-lg border border-slate-800 p-2">
                                      <div className="flex flex-wrap items-center gap-3 text-xs">
                                        <span className="font-semibold text-cyan-300">{first(r, '00080060')}</span>
                                        <span className="text-slate-200">
                                          #{first(r, '00200011')} {first(r, '0008103E')}
                                        </span>
                                        <span className="text-slate-500">{first(r, '00201209')} instances</span>
                                        <button
                                          className="btn-ghost py-0.5 text-xs"
                                          onClick={(e) => {
                                            e.stopPropagation()
                                            void openSeries(uid, suid)
                                          }}
                                        >
                                          Instances
                                        </button>
                                      </div>
                                      {instances[suid] && (
                                        <ul className="mt-2 space-y-1 font-mono text-xs text-slate-400">
                                          {instances[suid].map((i) => {
                                            const sop = first(i, '00080018')
                                            return (
                                              <li key={sop} className="flex flex-wrap gap-3">
                                                <span>#{first(i, '00200013')}</span>
                                                <span>
                                                  {first(i, '00280010')}×{first(i, '00280011')}
                                                  {first(i, '00280008') ? `, ${first(i, '00280008')} frames` : ''}
                                                </span>
                                                <a
                                                  className="text-cyan-300 hover:underline"
                                                  href={`/dicomweb/wado?requestType=WADO&studyUID=${uid}&seriesUID=${suid}&objectUID=${sop}&contentType=application/dicom`}
                                                  download={`${sop}.dcm`}
                                                >
                                                  Download
                                                </a>
                                              </li>
                                            )
                                          })}
                                        </ul>
                                      )}
                                    </li>
                                  )
                                })}
                              </ul>
                            </td>
                          </tr>
                        ),
                      ]
                    })}
                  </tbody>
                </table>
              )}
            </Section>
          )}
        </>
      )}
    </div>
  )
}
