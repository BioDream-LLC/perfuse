import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from './api'
import type { XDSDoc, XDSStatus } from './api'
import { CodeArea } from './CodeArea'
import { ErrorBox, Field, Section, Spinner } from './ui'
import type { UiError } from './store'

/**
 * Document sharing: this server's IHE XDS.b registry and repository, and the XCA gateway over it.
 *
 * What it holds, where other systems reach it (SOAP XDS.b and XCA, and MHD on FHIR), a FindDocuments query of this
 * registry or another community's, and a way to publish a CDA document by hand with the metadata a channel's xds
 * destination would give it.
 */

const toError = (e: unknown): UiError => ({
  message: e instanceof Error ? e.message : String(e),
  problems: e instanceof ApiError ? e.problems : [],
})

const when = (dtm: string) =>
  dtm.length >= 8 ? `${dtm.slice(0, 4)}-${dtm.slice(4, 6)}-${dtm.slice(6, 8)}${dtm.length >= 12 ? ` ${dtm.slice(8, 10)}:${dtm.slice(10, 12)}` : ''}` : dtm

function Documents({ docs, local, testid }: { docs: XDSDoc[]; local: boolean; testid: string }) {
  if (docs.length === 0) return <p className="mt-3 text-sm text-slate-400">No document.</p>
  return (
    <table className="mt-3 w-full text-left text-sm" data-testid={testid}>
      <thead className="text-xs text-slate-500">
        <tr>
          <th className="py-1 pr-2">Created</th>
          <th className="py-1 pr-2">Title</th>
          <th className="py-1 pr-2">Patient</th>
          <th className="py-1 pr-2">Type</th>
          <th className="py-1 pr-2">Status</th>
          <th className="py-1">Document</th>
        </tr>
      </thead>
      <tbody className="text-slate-300">
        {docs.map((d) => (
          <tr key={d.entryUUID} className="border-t border-slate-800">
            <td className="py-1.5 pr-2 font-mono text-xs">{when(d.creationTime)}</td>
            <td className="py-1.5 pr-2 text-slate-100">
              {d.title || '—'}
              {d.author && <span className="block text-xs text-slate-500">{d.author}</span>}
            </td>
            <td className="py-1.5 pr-2 font-mono text-xs">{d.patientId}</td>
            <td className="py-1.5 pr-2 text-xs">{d.type.display || d.type.code}</td>
            <td className={`py-1.5 pr-2 text-xs ${d.status === 'Approved' ? 'text-emerald-300' : 'text-slate-500'}`}>
              {d.status === 'Deprecated' ? 'Replaced' : d.status}
            </td>
            <td className="py-1.5 text-xs">
              {local ? (
                <a className="text-cyan-300 hover:underline" href={`/api/xds/documents/${encodeURIComponent(d.uniqueId)}`}>
                  Download ({d.size} bytes)
                </a>
              ) : (
                <span className="font-mono text-slate-500">{d.repositoryUniqueId}</span>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

const sampleCDA = `<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <templateId root="2.16.840.1.113883.10.20.22.1.1" extension="2015-08-01"/>
  <id root="2.25.${Math.floor(Math.random() * 1e15)}"/>
  <code code="11488-4" codeSystem="2.16.840.1.113883.6.1" displayName="Consult note"/>
  <title>Consult note</title>
  <effectiveTime value="20261015093000-0500"/>
  <confidentialityCode code="N" codeSystem="2.16.840.1.113883.5.25"/>
  <languageCode code="en-US"/>
  <recordTarget><patientRole>
    <id root="1.2.826.0.1.3680043.10.1337.99" extension="DWMRN1"/>
    <patient><name><given>Jane</given><family>Doe</family></name>
      <administrativeGenderCode code="F" codeSystem="2.16.840.1.113883.5.1"/><birthTime value="19700101"/></patient>
  </patientRole></recordTarget>
  <author><time value="20261015093000-0500"/><assignedAuthor><id root="2.16.840.1.113883.4.6" extension="1234567893"/>
    <assignedPerson><name><given>Alex</given><family>Smith</family></name></assignedPerson></assignedAuthor></author>
  <custodian><assignedCustodian><representedCustodianOrganization><name>Example Hospital</name></representedCustodianOrganization></assignedCustodian></custodian>
  <component><structuredBody><component><section><title>Assessment</title><text>Stable.</text></section></component></structuredBody></component>
</ClinicalDocument>`

export function DocumentSharing() {
  const [status, setStatus] = useState<XDSStatus | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [patient, setPatient] = useState('')
  const [remote, setRemote] = useState('')
  const [gateway, setGateway] = useState(false)
  const [token, setToken] = useState('')
  const [found, setFound] = useState<{ status: string; errors: string[]; documents: XDSDoc[]; where: string } | null>(null)
  const [doc, setDoc] = useState(sampleCDA)
  const [domain, setDomain] = useState('1.2.826.0.1.3680043.10.1337.99')
  const [facility, setFacility] = useState('225732001')
  const [practice, setPractice] = useState('394802001')
  const [published, setPublished] = useState<XDSDoc | null>(null)

  const load = useCallback(() => {
    api.xdsStatus().then(setStatus).catch((e) => setError(toError(e)))
  }, [])
  useEffect(load, [load])

  async function find() {
    setError(null)
    try {
      const r = await api.xdsQuery({ patient: patient.trim(), url: remote.trim() || undefined, gateway, token: token || undefined })
      setFound({ ...r, where: remote.trim() || 'this server' })
    } catch (e) {
      setError(toError(e))
    }
  }

  async function publish() {
    setError(null)
    setPublished(null)
    try {
      const r = await api.xdsPublish({
        document: doc,
        patientDomain: domain.trim() || undefined,
        facilityType: { code: facility.trim(), scheme: '2.16.840.1.113883.6.96' },
        practiceSetting: { code: practice.trim(), scheme: '2.16.840.1.113883.6.96' },
      })
      setPublished(r.document)
      load()
    } catch (e) {
      setError(toError(e))
    }
  }

  if (!status && !error) return <Spinner label="Loading the document registry…" />

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-medium text-slate-100">Document sharing</h2>
        <p className="mt-1 text-sm text-slate-400">
          This server&apos;s IHE XDS.b registry and repository, and the XCA gateway other communities query it through. Channels publish to
          it, or to another organisation&apos;s repository, with an xds destination.
        </p>
      </div>
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      {status && !status.enabled && (
        <Section title="No document registry on this server">
          <p className="text-sm text-slate-300">{status.hint}</p>
        </Section>
      )}
      {status?.enabled && status.counts && (
        <>
          <div className="grid gap-4 sm:grid-cols-4" data-testid="xds-counts">
            {(
              [
                ['Documents', status.counts.documents],
                ['Patients', status.counts.patients],
                ['Submission sets', status.counts.submissionSets],
                ['Replaced', status.counts.deprecated],
              ] as const
            ).map(([label, value]) => (
              <div key={label} className="rounded-xl border border-slate-800 bg-slate-900/60 p-4">
                <p className="text-xs uppercase tracking-wide text-slate-500">{label}</p>
                <p className="mt-1 text-2xl font-semibold text-cyan-300">{value}</p>
              </div>
            ))}
          </div>

          <Section title="Where other systems reach it">
            <dl className="grid gap-2 text-sm sm:grid-cols-[16rem_1fr]">
              <dt className="text-slate-400">Repository (ITI-41, ITI-43)</dt>
              <dd className="font-mono text-xs text-cyan-300" data-testid="xds-repository">
                {status.endpoints?.repository}
              </dd>
              <dt className="text-slate-400">Registry (ITI-18, ITI-42)</dt>
              <dd className="font-mono text-xs text-cyan-300">{status.endpoints?.registry}</dd>
              <dt className="text-slate-400">Responding gateway (XCA ITI-38, ITI-39)</dt>
              <dd className="font-mono text-xs text-cyan-300">
                {status.homeCommunityId ? status.endpoints?.gateway : <span className="text-slate-400">off: no home community id</span>}
              </dd>
              <dt className="text-slate-400">MHD, PIXm and PDQm (FHIR)</dt>
              <dd className="font-mono text-xs text-cyan-300">{status.endpoints?.repository.replace('/xds/repository', '/mhd')}</dd>
              <dt className="text-slate-400">PIX manager, HL7 v3 (ITI-44, ITI-45)</dt>
              <dd className="font-mono text-xs text-cyan-300">
                {status.endpoints?.pixv3 ?? <span className="text-slate-400">off: no patient index (-mpi)</span>}
              </dd>
              <dt className="text-slate-400">PDQ supplier, HL7 v3 (ITI-47)</dt>
              <dd className="font-mono text-xs text-cyan-300">
                {status.endpoints?.pdqv3 ?? <span className="text-slate-400">off: no patient index (-mpi)</span>}
              </dd>
              <dt className="text-slate-400">Repository uniqueId</dt>
              <dd className="font-mono text-xs text-slate-200">{status.repositoryUniqueId}</dd>
              <dt className="text-slate-400">Home community id</dt>
              <dd className="font-mono text-xs text-slate-200">{status.homeCommunityId || '—'}</dd>
            </dl>
            <p className="mt-3 text-xs text-slate-500">
              Each takes an API token as a bearer token: the viewer role to query and retrieve, the operator role to submit. Most affinity
              domains put mutual TLS in front as well.
            </p>
          </Section>

          <Section title="Find a patient's documents">
            <form
              className="grid gap-3 sm:grid-cols-4"
              onSubmit={(e) => {
                e.preventDefault()
                void find()
              }}
            >
              <div className="sm:col-span-2">
                <Field label="Patient ID" hint="As the affinity domain registers it: id^^^&OID&ISO.">
                  <input
                    className="input font-mono"
                    value={patient}
                    onChange={(e) => setPatient(e.target.value)}
                    placeholder="DWMRN1^^^&1.2.826.0.1.3680043.10.1337.99&ISO"
                  />
                </Field>
              </div>
              <div className="sm:col-span-2">
                <Field label="Registry or gateway (optional)" hint="Empty asks this server. Otherwise another registry's ITI-18 or a gateway's ITI-38 address.">
                  <input className="input font-mono text-xs" value={remote} onChange={(e) => setRemote(e.target.value)} placeholder="https://hie.example/xds/registry" />
                </Field>
              </div>
              <label className="flex items-center gap-2 text-sm text-slate-300">
                <input type="checkbox" checked={gateway} onChange={(e) => setGateway(e.target.checked)} disabled={!remote} /> It is an XCA gateway
              </label>
              <Field label="Bearer token for it (optional)">
                <input type="password" className="input font-mono" value={token} onChange={(e) => setToken(e.target.value)} disabled={!remote} />
              </Field>
              <div className="flex items-end">
                <button className="btn-primary" type="submit" disabled={!patient.trim()}>
                  Find documents
                </button>
              </div>
            </form>
            {found && (
              <div className="mt-3" data-testid="xds-found">
                <p className="text-xs text-slate-500">
                  {found.where}: {found.status}
                  {found.errors.length > 0 && <span className="text-amber-300"> · {found.errors.join('; ')}</span>}
                </p>
                <Documents docs={found.documents} local={!remote.trim()} testid="xds-found-docs" />
              </div>
            )}
          </Section>

          <Section title="Publish a CDA document here">
            <p className="text-sm text-slate-400">
              The metadata is read from the document as a channel&apos;s xds destination reads it; what CDA does not say, you give here.
            </p>
            <div className="mt-3 grid gap-3 sm:grid-cols-3">
              <Field label="Affinity domain (patient ID OID)">
                <input className="input font-mono" value={domain} onChange={(e) => setDomain(e.target.value)} />
              </Field>
              <Field label="Facility type (SNOMED CT)">
                <input className="input font-mono" value={facility} onChange={(e) => setFacility(e.target.value)} />
              </Field>
              <Field label="Practice setting (SNOMED CT)">
                <input className="input font-mono" value={practice} onChange={(e) => setPractice(e.target.value)} />
              </Field>
            </div>
            <div className="mt-3">
              <CodeArea language="xml" aria-label="The CDA document" value={doc} onChange={setDoc} className="min-h-64" />
            </div>
            <button className="btn-primary mt-3" type="button" onClick={() => void publish()}>
              Publish
            </button>
            {published && (
              <p className="mt-3 rounded-lg border border-emerald-900/60 bg-emerald-950/30 p-3 text-sm text-emerald-200" data-testid="xds-published">
                Registered {published.title || 'the document'} for {published.patientId}: uniqueId {published.uniqueId}, {published.format.display || published.format.code}.
              </p>
            )}
          </Section>

          <Section title="Latest documents">
            <Documents docs={status.documents ?? []} local testid="xds-documents" />
          </Section>
        </>
      )}
    </div>
  )
}
