import { useEffect, useRef, useState } from 'react'
import { api, ApiError } from './api'
import type { HealthCard, SHLCreated, SHLHosted, SHLResolved } from './api'
import { CodeArea } from './CodeArea'
import { ErrorBox, Field, Section } from './ui'
import type { UiError } from './store'

/**
 * SMART Health Links and Cards: CMS's "Kill the Clipboard".
 *
 * A patient arrives with a QR code instead of a clipboard. Reading it fetches the records it points at, decrypted with the key the
 * link carries, and verifies any SMART Health Cards inside against their issuers. Accepting them hands the FHIR content to a channel,
 * which is the route into the EHR. The visit record goes back as a link this server hosts - encrypted, with a key the server does not
 * keep.
 */
export function HealthLinks() {
  const [tab, setTab] = useState<'receive' | 'share' | 'verify'>('receive')
  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-medium text-slate-100">SMART Health Links and Cards</h2>
        <p className="mt-1 text-sm text-slate-400">
          Read the QR code a patient shows at check-in, verify the health cards inside it, and send the records on to a channel. Share
          the visit record back the same way. CMS's Kill the Clipboard pledge asks for both.
        </p>
      </div>
      <div role="group" aria-label="Health link tools" className="flex flex-wrap gap-2">
        {(
          [
            ['receive', 'Receive'],
            ['share', 'Share'],
            ['verify', 'Verify a card'],
          ] as const
        ).map(([id, label]) => (
          <button
            key={id}
            aria-pressed={tab === id}
            className={tab === id ? 'btn-primary py-1 text-sm' : 'btn-ghost py-1 text-sm'}
            onClick={() => setTab(id)}
          >
            {label}
          </button>
        ))}
      </div>
      {tab === 'receive' && <Receive />}
      {tab === 'share' && <Share />}
      {tab === 'verify' && <Verify />}
    </div>
  )
}

const toError = (e: unknown): UiError => ({
  message: e instanceof Error ? e.message : String(e),
  problems: e instanceof ApiError ? e.problems : [],
})

/** A camera scanner, where the browser has one built in. Offered only when it exists: a button that does nothing is worse than none. */
function useScanner(onText: (t: string) => void) {
  const video = useRef<HTMLVideoElement | null>(null)
  const [scanning, setScanning] = useState(false)
  // BarcodeDetector is in Chromium-based browsers; elsewhere the link is pasted, which every scanner app can do.
  const supported = typeof window !== 'undefined' && 'BarcodeDetector' in window
  const stream = useRef<MediaStream | null>(null)

  useEffect(() => () => stream.current?.getTracks().forEach((t) => t.stop()), [])

  async function start() {
    if (!supported || !video.current) return
    setScanning(true)
    try {
      stream.current = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } })
      video.current.srcObject = stream.current
      await video.current.play()
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const detector = new (window as any).BarcodeDetector({ formats: ['qr_code'] })
      const tick = async () => {
        if (!stream.current || !video.current) return
        const codes = await detector.detect(video.current).catch(() => [])
        if (codes.length > 0) {
          onText(codes[0].rawValue as string)
          stop()
          return
        }
        requestAnimationFrame(() => void tick())
      }
      void tick()
    } catch {
      stop()
    }
  }
  function stop() {
    stream.current?.getTracks().forEach((t) => t.stop())
    stream.current = null
    setScanning(false)
  }
  return { video, supported, scanning, start, stop }
}

function Receive() {
  const [link, setLink] = useState('')
  const [passcode, setPasscode] = useState('')
  const [channel, setChannel] = useState('')
  const [result, setResult] = useState<SHLResolved | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [busy, setBusy] = useState(false)
  const scanner = useScanner((t) => setLink(t))
  const needsPasscode = (() => {
    try {
      const i = link.indexOf('shlink:/')
      if (i < 0) return false
      const json = JSON.parse(atob(link.slice(i + 8).replace(/-/g, '+').replace(/_/g, '/')))
      return typeof json.flag === 'string' && json.flag.includes('P')
    } catch {
      return false
    }
  })()

  async function receive(deliver: boolean) {
    setBusy(true)
    setError(null)
    try {
      setResult(await api.resolveSHL({ link, passcode: passcode || undefined, channel: deliver ? channel : undefined }))
    } catch (e) {
      setError(toError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="grid gap-5 xl:grid-cols-2">
      <Section title="Receive a SMART Health Link">
        <div className="space-y-3">
          <Field label="The link" hint="What the patient's QR code holds: shlink:/... or a viewer URL ending #shlink:/...">
            <textarea
              aria-label="SMART Health Link"
              className="input min-h-24 font-mono text-xs"
              value={link}
              onChange={(e) => setLink(e.target.value)}
              placeholder="shlink:/eyJ1cmwiOi..."
            />
          </Field>
          {scanner.supported && (
            <div>
              <video ref={scanner.video} className={scanner.scanning ? 'w-full rounded-lg' : 'hidden'} muted playsInline />
              <button className="btn py-1 text-sm" onClick={() => (scanner.scanning ? scanner.stop() : void scanner.start())}>
                {scanner.scanning ? 'Stop the camera' : 'Scan with the camera'}
              </button>
            </div>
          )}
          {needsPasscode && (
            <Field label="Passcode" hint="This link is protected. The patient has the passcode; ten wrong tries disable the link.">
              <input className="input" value={passcode} onChange={(e) => setPasscode(e.target.value)} autoComplete="off" />
            </Field>
          )}
          <button className="btn-primary w-full" disabled={busy || link.trim() === ''} onClick={() => void receive(false)}>
            Read the records
          </button>
        </div>
      </Section>

      <div className="space-y-4">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
        {result && (
          <>
            <div className="card p-4 text-sm" data-testid="shl-received">
              <p className="font-medium text-slate-100">
                {result.label || 'Shared records'}: {result.files.length} file{result.files.length === 1 ? '' : 's'}
              </p>
              {result.files.map((f, i) => (
                <div key={i} className="mt-3 border-t border-slate-800 pt-3">
                  <p className="text-xs text-slate-400">{f.contentType}</p>
                  {f.summary?.patient && (
                    <p className="mt-1 text-slate-200">
                      {f.summary.patient}
                      {f.summary.birthDate ? `, born ${f.summary.birthDate}` : ''}
                    </p>
                  )}
                  {f.summary && Object.keys(f.summary.counts).length > 0 && (
                    <p className="mt-1 text-xs text-slate-400">
                      {Object.entries(f.summary.counts)
                        .map(([k, v]) => `${v} ${k}`)
                        .join(' · ')}
                    </p>
                  )}
                  {(f.cards ?? []).map((c, j) => (
                    <CardLine key={j} card={c} />
                  ))}
                  {f.routed && <p className="mt-1 text-xs text-emerald-300">{f.routed}</p>}
                </div>
              ))}
            </div>
            <Section title="Send it on">
              <Field
                label="Channel"
                hint="The FHIR content goes to this channel as a message, which is how it reaches the EHR. A channel with a FHIR destination files it."
              >
                <input className="input font-mono" value={channel} onChange={(e) => setChannel(e.target.value)} placeholder="ehr-intake" />
              </Field>
              <button
                className="btn-primary mt-3 w-full"
                disabled={busy || channel.trim() === ''}
                onClick={() => void receive(true)}
              >
                Deliver to the channel
              </button>
            </Section>
          </>
        )}
      </div>
    </div>
  )
}

function CardLine({ card }: { card: HealthCard }) {
  return (
    <p className={card.verified ? 'mt-1 text-xs text-emerald-300' : 'mt-1 text-xs text-amber-300'}>
      SMART Health Card from {card.issuer}: {card.verified ? 'signature verified' : `not verified - ${card.problem}`}
    </p>
  )
}

const sampleVisit = JSON.stringify(
  {
    resourceType: 'Bundle',
    type: 'collection',
    entry: [
      { resource: { resourceType: 'Patient', name: [{ family: 'Doe', given: ['Jane'] }], birthDate: '1980-01-01' } },
      { resource: { resourceType: 'Encounter', status: 'finished', class: { code: 'AMB' } } },
    ],
  },
  null,
  2,
)

function Share() {
  const [content, setContent] = useState(sampleVisit)
  const [label, setLabel] = useState('Visit summary')
  const [passcode, setPasscode] = useState('')
  const [days, setDays] = useState('30')
  const [created, setCreated] = useState<SHLCreated | null>(null)
  const [links, setLinks] = useState<SHLHosted[]>([])
  const [error, setError] = useState<UiError | null>(null)

  async function load() {
    try {
      setLinks((await api.listSHL()).links)
    } catch (e) {
      setError(toError(e))
    }
  }
  useEffect(() => {
    void load()
  }, [])

  async function share() {
    setError(null)
    try {
      setCreated(
        await api.createSHL({
          label,
          content: JSON.parse(content),
          passcode: passcode || undefined,
          expiresInDays: Number(days) || 30,
        }),
      )
      await load()
    } catch (e) {
      setError(toError(e))
    }
  }

  return (
    <div className="space-y-5">
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      <div className="grid gap-5 xl:grid-cols-2">
        <Section title="Share a visit record">
          <div className="space-y-3">
            <Field label="FHIR content" hint="A Bundle, usually: the visit's encounter, results and medications.">
              <CodeArea language="json" aria-label="FHIR content to share" className="min-h-48" value={content} onChange={setContent} />
            </Field>
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="Label">
                <input className="input" value={label} maxLength={80} onChange={(e) => setLabel(e.target.value)} />
              </Field>
              <Field label="Passcode" hint="Optional. Tell the patient separately.">
                <input className="input" value={passcode} onChange={(e) => setPasscode(e.target.value)} autoComplete="off" />
              </Field>
              <Field label="Expires after, days">
                <input className="input" value={days} onChange={(e) => setDays(e.target.value)} />
              </Field>
            </div>
            <button className="btn-primary w-full" onClick={() => void share()}>
              Create the link
            </button>
          </div>
        </Section>
        {created && (
          <Section title="For the patient">
            <div
              data-testid="shl-qr"
              className="mx-auto w-64 rounded-lg bg-white p-2"
              role="img"
              aria-label="QR code of the SMART Health Link"
              // The SVG is generated by this server from the link, with no text from the request in it but the link itself.
              dangerouslySetInnerHTML={{ __html: created.qrSvg }}
            />
            <p className="mt-3 break-all font-mono text-xs text-slate-300" data-testid="shl-link">
              {created.link}
            </p>
            <p className="mt-2 text-xs text-slate-400">{created.note}</p>
            {created.warning && <p className="mt-2 text-xs text-amber-300">{created.warning}</p>}
          </Section>
        )}
      </div>
      <Section title="Links this server hosts">
        {links.length === 0 ? (
          <p className="text-sm text-slate-400">None yet.</p>
        ) : (
          <table className="w-full text-left text-xs" data-testid="shl-hosted">
            <thead className="text-slate-500">
              <tr>
                <th className="py-1 pr-2">Label</th>
                <th className="py-1 pr-2">Fetched</th>
                <th className="py-1 pr-2">Expires</th>
                <th className="py-1" />
              </tr>
            </thead>
            <tbody className="text-slate-300">
              {links.map((l) => (
                <tr key={l.id} className="border-t border-slate-800">
                  <td className="py-1 pr-2">
                    {l.label || '(no label)'}
                    {l.hasPasscode ? ` · passcode, ${l.attemptsLeft} tries left` : ''}
                  </td>
                  <td className="py-1 pr-2">
                    {l.accesses} time{l.accesses === 1 ? '' : 's'}
                    {l.lastRecipient ? `, last by ${l.lastRecipient}` : ''}
                  </td>
                  <td className="py-1 pr-2">{l.revokedAt ? 'revoked' : l.expiresAt ? new Date(l.expiresAt).toLocaleDateString() : ''}</td>
                  <td className="py-1 text-right">
                    {!l.revokedAt && (
                      <button
                        className="btn py-0.5 text-xs"
                        onClick={async () => {
                          await api.revokeSHL(l.id)
                          await load()
                        }}
                      >
                        Revoke
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
    </div>
  )
}

function Verify() {
  const [text, setText] = useState('')
  const [cards, setCards] = useState<HealthCard[] | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  return (
    <div className="grid gap-5 xl:grid-cols-2">
      <Section title="Verify a SMART Health Card">
        <CodeArea
          language="text"
          aria-label="SMART Health Card"
          className="min-h-48"
          value={text}
          onChange={setText}
          placeholder="shc:/56762909524320603460292437404460... or a .smart-health-card file"
        />
        <button
          className="btn-primary mt-4 w-full"
          onClick={async () => {
            setError(null)
            try {
              setCards((await api.verifySHC(text)).cards)
            } catch (e) {
              setError(toError(e))
            }
          }}
        >
          Verify
        </button>
      </Section>
      <div className="space-y-3">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
        {cards?.map((c, i) => (
          <div key={i} className="card p-4 text-sm" data-testid="shc-result">
            <CardLine card={c} />
            <p className="mt-1 text-xs text-slate-400">
              Issued {new Date(c.issuedAt).toLocaleDateString()} · {c.types.join(', ')}
            </p>
            <pre className="mt-2 max-h-72 overflow-auto font-mono text-xs text-slate-300">{JSON.stringify(c.bundle, null, 2)}</pre>
          </div>
        ))}
      </div>
    </div>
  )
}
