import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from './api'
import type { DirectCert, DirectContact, DirectView } from './api'
import { CodeArea } from './CodeArea'
import { ErrorBox, Field, Section, Spinner } from './ui'
import type { UiError } from './store'

/**
 * Direct secure messaging: the Direct addresses this server's channels send and receive as, and the address book of
 * correspondents with the certificate each one's mail is encrypted to. The book is the directory a direct destination reads
 * recipient certificates from; a certificate can be pasted (from the correspondent's HISP) or found in DNS, where Direct
 * publishes them as CERT records.
 */

const toError = (e: unknown): UiError => ({
  message: e instanceof Error ? e.message : String(e),
  problems: e instanceof ApiError ? e.problems : [],
})

const day = (iso: string) => (iso ? new Date(iso).toISOString().slice(0, 10) : '')

function CertLine({ c }: { c: DirectCert }) {
  const soon = !c.expired && new Date(c.notAfter).getTime() - Date.now() < 30 * 86400_000
  return (
    <span className="block text-xs">
      <span className="font-mono text-slate-200">{c.bound || c.subject}</span>
      <span className="text-slate-500"> · issued by </span>
      <span className="text-violet-300">{c.issuer}</span>
      <span className="text-slate-500"> · until </span>
      <span className={c.expired ? 'text-red-300' : soon ? 'text-amber-300' : 'text-emerald-300'}>
        {day(c.notAfter)}
        {c.expired ? ' (expired)' : soon ? ' (soon)' : ''}
      </span>
    </span>
  )
}

const noCert = { text: 'No certificate', cls: 'text-amber-300' }
const sourceLabel: Record<string, { text: string; cls: string }> = {
  dns: { text: 'DNS', cls: 'text-cyan-300' },
  upload: { text: 'Pasted', cls: 'text-violet-300' },
  file: { text: 'File', cls: 'text-slate-300' },
  none: { text: 'No certificate', cls: 'text-amber-300' },
}

export function Direct() {
  const [view, setView] = useState<DirectView | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [busy, setBusy] = useState(false)
  const [address, setAddress] = useState('')
  const [name, setName] = useState('')
  const [organisation, setOrganisation] = useState('')
  const [notes, setNotes] = useState('')
  const [pem, setPem] = useState('')
  const [saved, setSaved] = useState('')
  const [lookAddr, setLookAddr] = useState('')
  const [lookDNS, setLookDNS] = useState('')
  const [found, setFound] = useState<DirectCert[] | null>(null)

  const load = useCallback(() => {
    api.direct().then(setView).catch((e) => setError(toError(e)))
  }, [])
  useEffect(load, [load])

  const edit = (c: DirectContact) => {
    setAddress(c.address)
    setName(c.name ?? '')
    setOrganisation(c.organisation ?? '')
    setNotes(c.notes ?? '')
    setPem('')
    setSaved('')
  }

  const save = (lookUp: boolean) => {
    setBusy(true)
    setError(null)
    setSaved('')
    api
      .directSave(address.trim(), { name, organisation, notes, certificate: lookUp ? undefined : pem || undefined, lookUp })
      .then((c) => {
        setSaved(`${c.address} saved${c.certificates.length ? ` with ${c.certificates.length} certificate${c.certificates.length > 1 ? 's' : ''}` : ''}.`)
        setPem('')
        load()
      })
      .catch((e) => setError(toError(e)))
      .finally(() => setBusy(false))
  }

  const remove = (a: string) => {
    if (!window.confirm(`Remove ${a} and its certificate from the address book? Messages to it will then need a DNS lookup.`)) return
    api
      .directDelete(a)
      .then(load)
      .catch((e) => setError(toError(e)))
  }

  const lookup = () => {
    setBusy(true)
    setError(null)
    setFound(null)
    api
      .directLookup(lookAddr.trim(), lookDNS.trim())
      .then((r) => setFound(r.certificates))
      .catch((e) => setError(toError(e)))
      .finally(() => setBusy(false))
  }

  if (!view && !error) return <Spinner />

  return (
    <div className="space-y-6" data-testid="direct">
      {error && <ErrorBox error={error} />}
      {view && (
        <>
          <Section
            title="This server's Direct addresses"
            description={
              <>
                Addresses come from channels: a <span className="font-mono text-cyan-300">direct</span> source receives an address&apos;s
                mail from its HISP, a <span className="font-mono text-cyan-300">direct</span> destination sends as one. Perfuse is the
                edge system, not a HISP: the HISP issues the address and its certificate.
              </>
            }
          >
            {view.addresses.length === 0 ? (
              <p className="text-sm text-slate-400" data-testid="direct-no-addresses">
                No channel sends or receives Direct yet. In the channel builder, choose the source{' '}
                <span className="text-slate-200">Our HISP delivers Direct secure messages to us</span> or the destination{' '}
                <span className="text-slate-200">A Direct address, through a HISP</span>.
              </p>
            ) : (
              <table className="w-full text-left text-sm" data-testid="direct-addresses">
                <thead className="text-xs text-slate-500">
                  <tr>
                    <th className="py-1 pr-2">Address</th>
                    <th className="py-1 pr-2">Channel</th>
                    <th className="py-1 pr-2">Role</th>
                    <th className="py-1 pr-2">HISP</th>
                    <th className="py-1">Certificate</th>
                  </tr>
                </thead>
                <tbody className="text-slate-300">
                  {view.addresses.map((a) => (
                    <tr key={a.channel + a.role + (a.destination ?? '')} className="border-t border-slate-800 align-top">
                      <td className="py-1.5 pr-2 font-mono text-xs text-slate-100">{a.address}</td>
                      <td className="py-1.5 pr-2 text-xs">
                        {a.channel}
                        {a.destination && <span className="text-slate-500"> › {a.destination}</span>}
                      </td>
                      <td className={`py-1.5 pr-2 text-xs ${a.role === 'send' ? 'text-cyan-300' : 'text-violet-300'}`}>
                        {a.role === 'send' ? `Sends to ${(a.to ?? []).join(', ')}` : `Receives on ${a.listen}`}
                      </td>
                      <td className="py-1.5 pr-2 font-mono text-xs">{a.hisp || '—'}</td>
                      <td className="py-1.5 text-xs">
                        {a.certificate && <CertLine c={a.certificate} />}
                        {a.anchors && a.anchors.length > 0 && (
                          <span className="block text-slate-500">
                            Trusts {a.anchors.length} anchor{a.anchors.length > 1 ? 's' : ''}: {a.anchors.map((x) => x.subject).join(', ')}
                          </span>
                        )}
                        {a.problem && <span className="block text-red-300">{a.problem}</span>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Section>

          <Section
            title="Address book"
            description={
              <>
                Correspondents and the certificates their mail is encrypted to. A direct destination reads these (from{' '}
                <span className="font-mono text-xs text-slate-300">{view.certsDir || 'the server directory'}</span>) before it asks
                DNS. A certificate must be for the address, or for its domain; a certificate for anyone else is refused, because the
                recipient could not read mail encrypted to it.
              </>
            }
          >
            {view.contacts.length === 0 ? (
              <p className="text-sm text-slate-400" data-testid="direct-no-contacts">
                Nobody yet. Add a correspondent below.
              </p>
            ) : (
              <table className="w-full text-left text-sm" data-testid="direct-contacts">
                <thead className="text-xs text-slate-500">
                  <tr>
                    <th className="py-1 pr-2">Address</th>
                    <th className="py-1 pr-2">Who</th>
                    <th className="py-1 pr-2">Certificate</th>
                    <th className="py-1 pr-2">From</th>
                    <th className="py-1" />
                  </tr>
                </thead>
                <tbody className="text-slate-300">
                  {view.contacts.map((c) => {
                    const src = sourceLabel[c.source] ?? noCert
                    return (
                      <tr key={c.address} className="border-t border-slate-800 align-top">
                        <td className="py-1.5 pr-2 font-mono text-xs text-slate-100">{c.address}</td>
                        <td className="py-1.5 pr-2 text-xs">
                          {c.name || '—'}
                          {c.organisation && <span className="block text-slate-500">{c.organisation}</span>}
                        </td>
                        <td className="py-1.5 pr-2">
                          {c.certificates.length === 0 ? (
                            <span className="text-xs text-amber-300">None: messages to it need DNS</span>
                          ) : (
                            c.certificates.map((x) => <CertLine key={x.sha256} c={x} />)
                          )}
                        </td>
                        <td className={`py-1.5 pr-2 text-xs ${src.cls}`}>{src.text}</td>
                        <td className="py-1.5 text-right text-xs whitespace-nowrap">
                          <button type="button" className="text-cyan-300 hover:underline" onClick={() => edit(c)}>
                            Edit
                          </button>
                          {c.certificates.length > 0 && (
                            <a className="ml-3 text-cyan-300 hover:underline" href={`/api/direct/contacts/${encodeURIComponent(c.address)}/certificate`}>
                              PEM
                            </a>
                          )}
                          <button type="button" className="ml-3 text-red-300 hover:underline" onClick={() => remove(c.address)}>
                            Remove
                          </button>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            )}
            <div className="mt-4 grid gap-3 sm:grid-cols-2" data-testid="direct-contact-form">
              <Field label="Direct address or domain" hint="name@direct.example.org, or direct.example.org for a domain certificate.">
                <input className="input font-mono" value={address} onChange={(e) => setAddress(e.target.value)} placeholder="intake@direct.clinic.example" />
              </Field>
              <Field label="Name">
                <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Dr Jane Smith" />
              </Field>
              <Field label="Organisation">
                <input className="input" value={organisation} onChange={(e) => setOrganisation(e.target.value)} placeholder="Riverside Clinic" />
              </Field>
              <Field label="Notes">
                <input className="input" value={notes} onChange={(e) => setNotes(e.target.value)} />
              </Field>
            </div>
            <Field label="Certificate (PEM)" hint="Paste the certificate the correspondent's HISP gave you, or leave this empty and find it in DNS.">
              <CodeArea value={pem} onChange={setPem} language="text" rows={5} placeholder="-----BEGIN CERTIFICATE-----" />
            </Field>
            <div className="mt-3 flex flex-wrap items-center gap-3">
              <button type="button" className="btn-primary" disabled={busy || !address.trim()} onClick={() => save(false)}>
                Save
              </button>
              <button type="button" className="btn" disabled={busy || !address.trim()} onClick={() => save(true)}>
                Save with the certificate from DNS
              </button>
              {saved && (
                <span className="text-sm text-emerald-300" data-testid="direct-saved">
                  {saved}
                </span>
              )}
            </div>
          </Section>

          <Section
            title="Find a certificate in DNS"
            description="Direct publishes certificates as DNS CERT records (RFC 4398): under the address with its @ made a dot, else under the domain. This asks without saving anything."
          >
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="Direct address">
                <input className="input font-mono" value={lookAddr} onChange={(e) => setLookAddr(e.target.value)} placeholder="intake@direct.clinic.example" />
              </Field>
              <Field label="DNS server" hint="host:port. Empty: this server's resolver.">
                <input className="input font-mono" value={lookDNS} onChange={(e) => setLookDNS(e.target.value)} placeholder="1.1.1.1:53" />
              </Field>
              <div className="flex items-end">
                <button type="button" className="btn" disabled={busy || !lookAddr.trim()} onClick={lookup}>
                  Look up
                </button>
              </div>
            </div>
            {found && (
              <div className="mt-3" data-testid="direct-found">
                {found.length === 0 ? (
                  <p className="text-sm text-amber-300">DNS publishes no certificate for that address or its domain.</p>
                ) : (
                  found.map((c) => (
                    <div key={c.sha256} className="mt-2 rounded-lg border border-slate-800 p-3">
                      <CertLine c={c} />
                      <span className="block font-mono text-xs text-slate-500">SHA-256 {c.sha256}</span>
                    </div>
                  ))
                )}
              </div>
            )}
          </Section>
        </>
      )}
    </div>
  )
}
