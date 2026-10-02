import { IconShield } from './Icons'
import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from './api'
import type { SigningIdentity, VerifyResult } from './api'
import { ErrorBox, Field, Section } from './ui'
import type { UiError } from './store'

/**
 * Checking, and making, a signature on a clinical document.
 *
 * A signature asserts that a named party, at a named time, took responsibility for a specific set of bytes. All three
 * parts matter and each fails differently, so the report keeps them apart rather than reducing everything to one
 * badge. The bytes are intact, or not. The signature came from the key in that certificate, or not. That certificate
 * is trusted, unknown, or was never checked. And the certificate was valid when it was used, or not.
 *
 * The last distinction is the one a single badge destroys: a document whose certificate has since expired is not a
 * forgery, and reporting it identically to an altered document sends somebody looking in the wrong place.
 */
export function SignaturePanel({ document: xml }: { document: string }) {
  const [result, setResult] = useState<VerifyResult | null>(null)
  const [trustPem, setTrustPem] = useState('')
  const [identity, setIdentity] = useState<SigningIdentity | null>(null)
  const [role, setRole] = useState('')
  const [signed, setSigned] = useState<{ document: string; caveat: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<UiError | null>(null)

  const toError = (e: unknown): UiError => ({
    message: e instanceof Error ? e.message : String(e),
    problems: e instanceof ApiError ? e.problems : [],
  })

  const loadIdentity = useCallback(async () => {
    try {
      setIdentity(await api.signingIdentity())
    } catch {
      // Left absent rather than surfaced. Not being able to sign is not an error in a panel whose main purpose is
      // checking, and an error box here would suggest the verification below is unreliable.
    }
  }, [])

  useEffect(() => {
    void loadIdentity()
  }, [loadIdentity])

  async function verify() {
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      setResult(await api.verifyDocument({ document: xml, trustPem: trustPem || undefined }))
    } catch (e) {
      setError(toError(e))
    } finally {
      setBusy(false)
    }
  }

  async function sign() {
    setBusy(true)
    setError(null)
    setSigned(null)
    try {
      const res = await api.signDocument({ document: xml, referenceId: '', role: role || undefined })
      setSigned({ document: res.document, caveat: res.caveat })
    } catch (e) {
      setError(toError(e))
    } finally {
      setBusy(false)
    }
  }

  function download(text: string) {
    const blob = new Blob([text], { type: 'application/xml' })
    const url = URL.createObjectURL(blob)
    const a = window.document.createElement('a')
    a.href = url
    a.download = 'signed.cda.xml'
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="space-y-4">
      <div className="card p-4">
        <h4 className="text-sm font-medium text-slate-200">Check the signature</h4>
        <p className="mt-1 text-sm text-slate-400">
          Whether the content has changed since it was signed, who signed it, when, and in what capacity. Most
          documents are unsigned — that is not a fault.
        </p>

        <details className="mt-3 rounded-lg border border-slate-800 bg-slate-950/40 p-3">
          <summary className="cursor-pointer text-xs text-slate-400">
            Certificates to trust (optional)
          </summary>
          <div className="mt-3">
            <Field
              label="Trusted certificates in PEM form"
              hint="Without these, the check proves only that the signature is internally consistent — a self-signed certificate gives the same result."
            >
              <textarea
                className="input h-24 w-full font-mono text-[11px]"
                value={trustPem}
                onChange={(e) => setTrustPem(e.target.value)}
                placeholder="-----BEGIN CERTIFICATE-----"
                spellCheck={false}
              />
            </Field>
          </div>
        </details>

        <button className="btn-primary mt-3 py-1 text-xs" onClick={() => void verify()} disabled={busy}>
          {busy ? 'Checking…' : 'Check it'}
        </button>
      </div>

      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

      {result && <VerificationReport result={result} />}

      {/* Signing, kept below and behind a disclosure, because checking is the common action and signing is rare
          and consequential. */}
      <details className="rounded-xl border border-slate-800 bg-slate-900/30 p-4">
        <summary className="cursor-pointer text-sm font-medium text-slate-200">Sign this document</summary>

        <div className="mt-3 space-y-3">
          {identity === null ? (
            <p className="text-xs text-slate-500">Checking what this server could sign as…</p>
          ) : !identity.available ? (
            <p className="text-xs text-amber-300">{identity.reason}</p>
          ) : (
            <>
              {/* Who the signature will name, before the button is pressed. Finding out afterwards that a clinical
                  document was signed by a hostname is a poor way to learn it. */}
              <div className="rounded-lg border border-slate-800 bg-slate-950/40 p-3 text-xs">
                <p className="text-slate-300">
                  This would be signed as{' '}
                  <span className="font-mono text-slate-100">{identity.commonName}</span>
                </p>
                <p className="mt-1.5 text-slate-500">{identity.explanation}</p>
              </div>

              <Field
                label="Capacity"
                hint="Recorded inside the signature and covered by it. Naming the capacity is what tells a reader why this signature is the relevant one."
              >
                <input
                  className="input w-full py-1 text-xs"
                  value={role}
                  onChange={(e) => setRole(e.target.value)}
                  placeholder="Attending physician responsible for this discharge"
                />
              </Field>

              <button className="btn-primary py-1 text-xs" onClick={() => void sign()} disabled={busy}>
                {busy ? 'Signing…' : 'Sign it'}
              </button>
            </>
          )}

          {signed && (
            <div role="status" className="space-y-2">
              {/* The caveat comes back with the signature and is shown with it, so nobody uses this without
                  meeting it. A signature nobody questions carries more weight than no signature. */}
              <div className="rounded-lg border border-amber-900/60 bg-amber-950/25 p-3 text-xs text-amber-200">
                {signed.caveat}
              </div>
              <button className="btn-ghost py-1 text-xs" onClick={() => download(signed.document)}>
                Download the signed document
              </button>
            </div>
          )}
        </div>
      </details>
    </div>
  )
}

function VerificationReport({ result }: { result: VerifyResult }) {
  const r = result.report

  if (!r.signed) {
    return (
      <div role="status" className="rounded-lg border border-slate-800 bg-slate-900/40 p-4">
        <p className="text-sm text-slate-300">This document carries no signature.</p>
        {r.notes.map((n, i) => (
          <p key={i} className="mt-1 text-xs text-slate-500">
            {n}
          </p>
        ))}
      </div>
    )
  }

  return (
    <div role="status" className="space-y-3">
      <div
        className={`rounded-lg border p-4 ${
          result.sound ? 'border-emerald-900/60 bg-emerald-950/30' : 'border-rose-900/60 bg-rose-950/30'
        }`}
      >
        <p className={`text-sm font-medium ${result.sound ? 'text-emerald-200' : 'text-rose-200'}`}>
          {result.sound
            ? 'Everything that could be checked passed.'
            : 'This signature does not stand up. See below.'}
        </p>
        {r.signer && <p className="mt-1 font-mono text-xs text-slate-400">{r.signer}</p>}
      </div>

      {/* The four questions separately. Reducing them to one badge is how a verifier misleads people: an expired
          certificate and an altered document are completely different situations. */}
      <div className="grid gap-2 sm:grid-cols-2">
        <Check ok={r.digestValid} label="The content has not changed" />
        <Check ok={r.signatureValid} label="Signed by the key in that certificate" />
        <Check ok={r.propertiesValid} label="The signing time and capacity are covered" />
        <Check ok={r.validAtSigningTime} label="The certificate was valid when used" />
        <Check
          ok={r.trusted}
          unchecked={!r.trustChecked}
          label={r.trustChecked ? 'The certificate is trusted here' : 'Trust was not checked'}
        />
      </div>

      <dl className="grid gap-x-4 gap-y-1 rounded-lg border border-slate-800 bg-slate-900/40 p-3 text-xs sm:grid-cols-2">
        {r.signingTime && (
          <div>
            <dt className="inline text-slate-500">Signed </dt>
            <dd className="inline text-slate-300">{new Date(r.signingTime).toLocaleString()}</dd>
          </div>
        )}
        {r.claimedRole && (
          <div>
            <dt className="inline text-slate-500">Capacity </dt>
            <dd className="inline text-slate-300">{r.claimedRole}</dd>
          </div>
        )}
        {r.issuer && (
          <div className="sm:col-span-2">
            <dt className="inline text-slate-500">Issued by </dt>
            <dd className="inline text-slate-400">{r.issuer}</dd>
          </div>
        )}
        {r.level && (
          <div className="sm:col-span-2">
            <dt className="inline text-slate-500">Level </dt>
            <dd className="inline text-slate-400">{r.level}</dd>
          </div>
        )}
      </dl>

      {r.problems.length > 0 && (
        <Section icon={IconShield} title={`${r.problems.length} problem${r.problems.length === 1 ? '' : 's'}`}>
          <ul className="space-y-2">
            {r.problems.map((p, i) => (
              <li key={i} className="rounded-lg border border-rose-900/60 bg-rose-950/20 p-3 text-xs text-rose-200">
                {p}
              </li>
            ))}
          </ul>
        </Section>
      )}

      {r.notes.length > 0 && (
        <details className="rounded-lg border border-slate-800 bg-slate-900/30 p-3">
          <summary className="cursor-pointer text-xs text-slate-400">
            {r.notes.length} thing{r.notes.length === 1 ? '' : 's'} this check does not establish
          </summary>
          <ul className="mt-2 space-y-1.5">
            {r.notes.map((n, i) => (
              <li key={i} className="text-xs text-slate-500">
                {n}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  )
}

/** Check renders one of the questions, distinguishing "no" from "not asked". */
function Check({ ok, label, unchecked }: { ok: boolean; label: string; unchecked?: boolean }) {
  const tone = unchecked ? 'text-slate-500' : ok ? 'text-emerald-300' : 'text-rose-300'
  const mark = unchecked ? '–' : ok ? '✓' : '✗'

  return (
    <div className="flex items-baseline gap-2 rounded-lg border border-slate-800 bg-slate-900/40 p-2.5 text-xs">
      {/* aria-hidden on the glyph and the state in the text, because a screen reader announcing a tick mark
          conveys nothing. */}
      <span className={`${tone} font-bold`} aria-hidden="true">
        {mark}
      </span>
      <span className="text-slate-300">
        {label}
        <span className="sr-only">{unchecked ? ': not checked' : ok ? ': yes' : ': no'}</span>
      </span>
    </div>
  )
}
