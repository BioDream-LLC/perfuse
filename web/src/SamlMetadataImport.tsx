import { useState } from 'react'

import { api, type SAMLConfig } from './api'
import { CodeArea } from './CodeArea'

/**
 * SamlMetadataImport fills the SAML settings in from a provider's own published document.
 *
 * Why it exists. Every SAML identity provider publishes a metadata document describing itself, and
 * without this, configuring one meant opening that document by hand, finding the signing certificate,
 * stripping the line breaks out of the base64, wrapping it in PEM headers and pasting the sign-on URL
 * separately. A stray space in the result fails with a signature error that says nothing about
 * formatting, which is a bad afternoon.
 *
 * It also decides which providers this product appears to support. A panel that accepts a certificate
 * and a URL works for whichever vendor's documentation you happen to be following. A panel that reads
 * metadata works for all of them, including the ones nobody has tested here.
 *
 * Nothing is saved by reading. The document is parsed, what it contains is shown, and applying it to
 * the form is a separate click — because a document somebody pasted is not yet a decision to trust a
 * certificate.
 */
export function SamlMetadataImport({
  config,
  onApply,
}: {
  config: SAMLConfig | null
  onApply: (next: Partial<SAMLConfig>) => void
}) {
  const [open, setOpen] = useState(false)
  const [url, setUrl] = useState('')
  const [document, setDocument] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [read, setRead] = useState<Awaited<ReturnType<typeof api.readSAMLMetadata>> | null>(null)

  async function readIt() {
    setBusy(true)
    setError('')
    setRead(null)
    try {
      setRead(await api.readSAMLMetadata({ url: url.trim(), document: document.trim() }))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  function apply() {
    if (!read) return
    onApply({
      idpSSOURL: read.ssoUrl,
      idpCertPEM: read.certPem,
      // The certificate file is cleared, or a stale path would keep overriding the certificate that was
      // just read and the form would show one thing while the server used another.
      idpCertFile: '',
    })
    setRead(null)
    setOpen(false)
  }

  if (!open) {
    return (
      <div className="rounded-lg border border-slate-700 bg-slate-900/40 p-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <p className="text-sm font-medium text-slate-200">Read the settings from your provider</p>
            <p className="mt-1 text-xs leading-relaxed text-slate-400">
              Okta, Entra, AWS IAM Identity Center, Keycloak, ADFS and anything else that publishes SAML
              metadata. Paste the document or give its URL and the sign-on address and certificate are
              filled in for you.
            </p>
          </div>
          <button className="btn-secondary shrink-0" onClick={() => setOpen(true)}>
            Read metadata
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-4 rounded-lg border border-slate-700 bg-slate-900/40 p-4">
      <p className="text-sm font-medium text-slate-200">Read the settings from your provider</p>

      <label className="block">
        <span className="text-xs font-medium text-slate-300">Metadata URL</span>
        <input
          className="input mt-1 w-full"
          type="url"
          placeholder="https://login.microsoftonline.com/…/federationmetadata.xml"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
        />
      </label>

      <label className="block">
        <span className="text-xs font-medium text-slate-300">
          Or paste the document, if this server cannot reach your provider
        </span>
        {/* CodeArea rather than a plain textarea, which a test in this repository enforces: a box holding XML gets
            highlighting and the right editing behaviour, and a metadata document is long enough that reading it
            unhighlighted is unpleasant. */}
        <CodeArea
          value={document}
          onChange={setDocument}
          language="xml"
          rows={6}
          placeholder="<EntityDescriptor …>"
          className="mt-1"
        />
      </label>

      <div className="flex items-center gap-3">
        <button
          className="btn-secondary"
          onClick={readIt}
          disabled={busy || (!url.trim() && !document.trim())}
        >
          {busy ? 'Reading…' : 'Read it'}
        </button>
        <button className="btn-ghost" onClick={() => setOpen(false)}>
          Cancel
        </button>
      </div>

      {error && (
        <p className="rounded-lg border border-amber-800/60 bg-amber-950/20 p-3 text-xs leading-relaxed text-amber-200">
          {error}
        </p>
      )}

      {read && (
        <div className="space-y-3 rounded-lg border border-slate-700 bg-slate-950/40 p-3">
          <dl className="space-y-1 text-xs">
            <div className="flex gap-2">
              <dt className="w-28 shrink-0 text-slate-400">Provider</dt>
              <dd className="break-all text-slate-200">{read.entityId}</dd>
            </div>
            <div className="flex gap-2">
              <dt className="w-28 shrink-0 text-slate-400">Sign-on URL</dt>
              <dd className="break-all text-slate-200">{read.ssoUrl || '—'}</dd>
            </div>
          </dl>

          {/* What you are about to trust, described rather than shown as base64.
              
              The fingerprint is here so it can be compared against what the provider's own console
              says. It is the only way to notice a document altered between the provider and here. */}
          <div className="space-y-2">
            {read.certificates.map((cert) => (
              <div
                key={cert.fingerprint}
                className={`rounded border p-2 text-xs leading-relaxed ${
                  cert.expired
                    ? 'border-red-800/60 bg-red-950/30 text-red-200'
                    : 'border-slate-700 bg-slate-900/60 text-slate-300'
                }`}
              >
                <p className="font-medium">{cert.subject}</p>
                <p className="mt-1">
                  {cert.expired ? 'Expired ' : 'Expires '}
                  {cert.notAfter}
                  {cert.expired && ' — assertions signed with this will not verify'}
                </p>
                <p className="mt-1 break-all font-mono text-[0.65rem] text-slate-400">
                  {cert.algorithm} · {cert.fingerprint}
                </p>
              </div>
            ))}
          </div>

          {config?.idpCertPEM && config.idpCertPEM !== read.certPem && (
            <p className="rounded border border-amber-800/60 bg-amber-950/20 p-2 text-xs leading-relaxed text-amber-200">
              This replaces the certificate currently configured. Sign-ins will be checked against the
              new one as soon as you save.
            </p>
          )}

          <button className="btn-primary" onClick={apply}>
            Use these settings
          </button>
        </div>
      )}
    </div>
  )
}
