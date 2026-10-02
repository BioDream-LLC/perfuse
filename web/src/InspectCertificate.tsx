import { useState } from 'react'
import { api, ApiError } from './api'
import type { InspectedCertificate } from './api'
import { ErrorBox, Field } from './ui'
import type { UiError } from './store'

/**
 * Checking a certificate before it goes anywhere near a channel.
 *
 * The server has been able to do this for a while and nothing in the interface asked it to. The endpoint existed,
 * was protected, was tested, and had no caller - the same shape as the AI Mapper, which rendered perfectly and had
 * never once worked because nothing had ever pressed it.
 *
 * The workflow it serves is the one that prevents the outage this page is about. A supplier sends a certificate,
 * it sits on somebody's clipboard, and the question is whether it is the right one and how long it lasts. Without
 * this the only way to find out was to install it and watch, which is a poor moment to discover that the
 * alternative names do not include the hostname the other end will use.
 */
export function InspectCertificate() {
  const [pem, setPem] = useState('')
  const [certs, setCerts] = useState<InspectedCertificate[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<UiError | null>(null)

  async function inspect() {
    setBusy(true)
    setError(null)
    setCerts(null)
    try {
      const res = await api.inspectCertificate({ pem })
      setCerts(res.certificates)
    } catch (e) {
      setError({
        message: e instanceof Error ? e.message : String(e),
        problems: e instanceof ApiError ? e.problems : [],
      })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="rounded-xl border border-slate-800 bg-slate-900/40 p-4">
      <h3 className="text-sm font-medium text-slate-200">Check a certificate</h3>
      <p className="mt-1 text-xs text-slate-500">
        Paste what a supplier sent you. Nothing is installed and no channel is changed — this only reads it and
        says what it is, which is worth knowing before it is in the path of live traffic.
      </p>

      <div className="mt-3">
        <Field
          label="Certificate in PEM form"
          hint="Beginning with BEGIN CERTIFICATE. A chain of several is fine; each is reported separately."
        >
          <textarea
            className="input h-32 w-full font-mono text-xs"
            value={pem}
            onChange={(e) => setPem(e.target.value)}
            placeholder="-----BEGIN CERTIFICATE-----"
            spellCheck={false}
          />
        </Field>
      </div>

      <button
        className="btn-primary mt-3 py-1 text-xs"
        onClick={() => void inspect()}
        disabled={busy || pem.trim() === ''}
      >
        {busy ? 'Reading…' : 'Read it'}
      </button>

      {error && (
        <div className="mt-3">
          <ErrorBox error={error} onDismiss={() => setError(null)} />
        </div>
      )}

      {certs !== null && certs.length === 0 && (
        <p role="status" className="mt-3 text-xs text-amber-300">
          No certificates were found in that. A private key or a certificate request would look like this and is
          not a certificate.
        </p>
      )}

      {certs !== null && certs.length > 0 && (
        <div role="status" className="mt-3 space-y-3">
          {certs.map((c, i) => (
            <div key={`${c.serialNumber}-${i}`} className="rounded-lg border border-slate-800 p-3 text-xs">
              <div className="flex flex-wrap items-baseline gap-2">
                <span className="font-medium text-slate-100">{c.subject}</span>
                <span
                  className={`badge border ${
                    c.status === 'ok'
                      ? 'border-emerald-800/50 bg-emerald-950/50 text-emerald-300'
                      : c.status === 'expiring'
                        ? 'border-amber-800/50 bg-amber-950/50 text-amber-300'
                        : 'border-rose-800/50 bg-rose-950/50 text-rose-300'
                  }`}
                >
                  {c.status === 'ok'
                    ? `${c.daysRemaining} days left`
                    : c.status === 'expiring'
                      ? `expires in ${c.daysRemaining} days`
                      : c.status === 'expired'
                        ? `expired ${Math.abs(c.daysRemaining)} days ago`
                        : 'not valid yet'}
                </span>
              </div>

              <dl className="mt-2 grid gap-x-4 gap-y-1 sm:grid-cols-2">
                <div>
                  <dt className="inline text-slate-500">Issued by </dt>
                  <dd className="inline text-slate-300">{c.issuer}</dd>
                </div>
                <div>
                  <dt className="inline text-slate-500">Serial </dt>
                  <dd className="inline font-mono text-slate-400">{c.serialNumber}</dd>
                </div>
                <div>
                  <dt className="inline text-slate-500">Valid from </dt>
                  <dd className="inline text-slate-300">{new Date(c.notBefore).toLocaleDateString()}</dd>
                </div>
                <div>
                  <dt className="inline text-slate-500">Until </dt>
                  <dd className="inline text-slate-300">{new Date(c.notAfter).toLocaleDateString()}</dd>
                </div>
              </dl>

              {/* The alternative names, prominently, because these are what verification actually uses.
                  
                  The common name has not counted for years, and reporting only that is how somebody concludes a
                  certificate should work when it cannot - then spends an afternoon on a handshake failure that
                  was decided the moment the certificate was issued. */}
              <div className="mt-2">
                <span className="text-slate-500">Valid for </span>
                {c.hosts && c.hosts.length > 0 ? (
                  <span className="font-mono text-slate-300">{c.hosts.join(', ')}</span>
                ) : (
                  <span className="text-amber-300">
                    nothing — it has no subject alternative names, so verification will fail against any hostname
                  </span>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
