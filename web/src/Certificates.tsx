import { useEffect, useState } from 'react'
import { api } from './api'
import type { CertificateSnapshot, CertificateInfo } from './api'
import { ErrorBox, Section, Spinner } from './ui'
import { InspectCertificate } from './InspectCertificate'

/**
 * Certificates.
 *
 * The reason this page exists is expiry. An expired certificate on an interface
 * feed is a real outage, and it happens because nobody was told: the renewal was
 * somebody's job three months ago, that person has moved on, and the first sign of
 * trouble is a sender reporting that messages stopped.
 *
 * So days remaining is the headline, not the expiry date. "Expires 4 November"
 * requires arithmetic; "expires in 9 days" does not.
 */
export function Certificates() {
  const [snapshot, setSnapshot] = useState<CertificateSnapshot | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let live = true
    api
      .certificates()
      .then((s) => live && setSnapshot(s))
      .catch((err) => live && setError(err instanceof Error ? err.message : 'could not read'))
    return () => {
      live = false
    }
  }, [])

  if (error) {
    return <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />
  }
  if (!snapshot) {
    return (
      <div className="card p-6">
        <Spinner label="Reading certificates…" />
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-lg font-semibold text-slate-100">Certificates</h1>
        <p className="mt-1 max-w-3xl text-sm text-slate-500">
          Every encrypted endpoint, and how long its certificate has left. This page exists
          because a certificate expiring is the commonest way an interface stops, and it is
          always something that could have been seen coming.
        </p>
      </div>

      {(snapshot.expired > 0 || snapshot.expiring > 0) && (
        <div
          className={`rounded-lg border p-4 ${
            snapshot.expired > 0
              ? 'border-rose-800 bg-rose-950/30'
              : 'border-amber-800 bg-amber-950/30'
          }`}
        >
          {snapshot.expired > 0 ? (
            <p className="font-medium text-rose-200">
              {snapshot.expired} certificate{snapshot.expired === 1 ? ' has' : 's have'} expired.
              Connections using {snapshot.expired === 1 ? 'it' : 'them'} are failing now.
            </p>
          ) : (
            <p className="font-medium text-amber-200">
              {snapshot.expiring} certificate{snapshot.expiring === 1 ? '' : 's'} expire within a
              month.
            </p>
          )}
        </div>
      )}

      {/* Somewhere to check one before it is anywhere near live traffic.
          
          Placed in both states, because the question "is this the right certificate and how long does it last"
          arrives when a supplier sends one - which has nothing to do with how many endpoints are already
          configured. On a server with no TLS at all this is the only thing on the page that can be done. */}
      <InspectCertificate />

      {snapshot.endpoints.length === 0 ? (
        <div className="card p-8 text-center">
          <p className="text-slate-400">No endpoint is using TLS.</p>
          <p className="mx-auto mt-2 max-w-lg text-xs text-slate-400">
            That is a reasonable choice on a segregated network, which is where most hospital
            interfaces live. Add a tls block to a channel's source or destination to encrypt one.
          </p>
        </div>
      ) : (
        <div className="space-y-3">
          {snapshot.endpoints.map((e, i) => (
            <div key={i} className="card p-4">
              <div className="flex flex-wrap items-baseline gap-2">
                <span className="font-medium text-slate-100">{e.channel}</span>
                <span className="badge border border-slate-700 bg-slate-800/60 text-slate-400">
                  {e.where === 'listener' ? 'listens' : `sends to ${e.destination}`}
                </span>
                <span className="font-mono text-xs text-slate-500">{e.address}</span>

                <div className="ml-auto flex gap-2">
                  <span className="badge border border-slate-700 bg-slate-800/60 text-slate-400">
                    TLS {e.tls.minVersion}+
                  </span>
                  {e.tls.mutualTls && (
                    // Worth its own badge: it is the difference between protecting the
                    // traffic and controlling who can send.
                    <span className="badge border border-emerald-800 bg-emerald-950/40 text-emerald-300">
                      client certificates required
                    </span>
                  )}
                </div>
              </div>

              {e.tls.problems?.map((p, j) => (
                <p
                  key={j}
                  className="mt-2 rounded-md border border-rose-900/60 bg-rose-950/30 p-2 text-xs text-rose-200"
                >
                  {p}
                </p>
              ))}
              {e.tls.warnings?.map((wn, j) => (
                <p
                  key={j}
                  className="mt-2 rounded-md border border-amber-900/60 bg-amber-950/25 p-2 text-xs text-amber-200"
                >
                  {wn}
                </p>
              ))}

              {(e.tls.certificates ?? []).map((c, j) => (
                <CertificateRow key={`c${j}`} cert={c} label="presents" />
              ))}
              {(e.tls.authorities ?? []).map((c, j) => (
                <CertificateRow key={`a${j}`} cert={c} label="trusts" />
              ))}
            </div>
          ))}
        </div>
      )}

      {snapshot.plaintext && snapshot.plaintext.length > 0 && (
        <Section
          title="Not encrypted"
          description="Listed because a page showing only the encrypted endpoints would let somebody conclude everything was encrypted. On a segregated network this is often the right choice — it is just worth knowing which is which."
        >
          <ul className="space-y-1 text-sm text-slate-400">
            {snapshot.plaintext.map((p, i) => (
              <li key={i} className="font-mono text-xs">
                {p}
              </li>
            ))}
          </ul>
        </Section>
      )}
    </div>
  )
}

function CertificateRow({ cert, label }: { cert: CertificateInfo; label: string }) {
  const tone =
    cert.status === 'expired'
      ? 'text-rose-300'
      : cert.status === 'expiring'
        ? 'text-amber-300'
        : 'text-slate-400'

  return (
    <div className="mt-3 border-t border-slate-800 pt-3">
      <div className="flex flex-wrap items-baseline gap-2 text-sm">
        <span className="text-xs text-slate-400">{label}</span>
        <span className="text-slate-200">{cert.subject}</span>
        {cert.isCa && (
          <span className="badge border border-slate-700 bg-slate-800/60 text-slate-500">
            authority
          </span>
        )}
        {cert.selfSigned && (
          <span className="badge border border-slate-700 bg-slate-800/60 text-slate-500">
            self-signed
          </span>
        )}

        {/* Days, not a date. This is the number somebody acts on. */}
        <span className={`ml-auto font-medium ${tone}`}>
          {cert.daysRemaining < 0
            ? `expired ${Math.abs(cert.daysRemaining)} days ago`
            : `${cert.daysRemaining} days left`}
        </span>
      </div>

      <div className="mt-1 flex flex-wrap gap-3 text-xs text-slate-400">
        <span>issued by {cert.issuer}</span>
        <span>
          {cert.keyType}
          {cert.keyBits ? ` ${cert.keyBits}` : ''}
        </span>
        {cert.hosts && cert.hosts.length > 0 && (
          <span className="font-mono">{cert.hosts.join(', ')}</span>
        )}
      </div>

      {cert.notes?.map((n, i) => (
        <p key={i} className="mt-1.5 text-xs text-amber-200/80">
          {n}
        </p>
      ))}
    </div>
  )
}
