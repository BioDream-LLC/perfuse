import { SyntaxBlock } from './SyntaxHighlight'
import { CodeArea } from './CodeArea'
import { useState } from 'react'
import { ParityPanel } from './ParityPanel'
import { api, ApiError } from './api'
import { Section, ErrorBox, Spinner } from './ui'
import type { UiError } from './store'
import { Donut, HBar } from './charts'

/**
 * Migrating from Mirth, as a picture.
 *
 * The decision to leave Mirth is not made by reading a feature comparison. It is made by looking at a
 * list of your own channels and seeing how many come across clean. So this asks for one thing - the
 * export you already have - and answers one question, big enough to read from across a desk.
 *
 * Everything else here follows from that. The headline is a fraction, not a table. The channels needing
 * attention are open by default and the clean ones are collapsed, because the clean ones need no reading
 * at all. And nothing is imported until somebody presses a button per channel: an "import all" that
 * silently created forty channels would be faster and much worse, since the first thing anybody wants to
 * know is whether they trust the first one.
 */

type Note = {
  severity: string
  /** Optional on the wire: a note about the channel as a whole has no location. */
  where?: string
  message: string
  action?: string
}

type Counts = {
  declarative: number
  scripted: number
  filterRules: number
  blockers: number
  warnings: number
}

type Imported = {
  name: string
  sourceName: string
  yaml: string
  confidence: string
  notes: Note[]
  counts: Counts
  blocked: boolean

  /** How much of this channel's logic runs only on one vendor's software. */
  portability: {
    total: number
    portable: number
    vendorOnly: number
    needsAnotherRoute: number
    unknown: number
    vendorReferences: string[]
  }
  portabilityVerdict: string
}

type Summary = {
  total: number
  clean: number
  warnings: number
  blocked: number
  declarativeSteps: number
  scriptedSteps: number
}

type ImportResult = {
  channels: Imported[]
  summary: Summary
  failed: string[]
}

/** Colours are shared with the rest of the UI so green means the same thing everywhere. */
const statusColour = {
  clean: '#34d399',
  warnings: '#fbbf24',
  blocked: '#f87171',
} as const

function statusOf(c: Imported): keyof typeof statusColour {
  if (c.blocked) return 'blocked'
  if (c.counts.warnings > 0) return 'warnings'
  return 'clean'
}

const statusLabel: Record<keyof typeof statusColour, string> = {
  clean: 'Ready to import',
  warnings: 'Imports, but read the notes',
  blocked: 'Needs work first',
}

export function MirthMigration() {
  const [xml, setXml] = useState('')
  const [result, setResult] = useState<ImportResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<UiError | null>(null)

  // Which channels are expanded. Seeded from the result so the ones needing attention open themselves:
  // a page that opens fully collapsed makes somebody click forty times to find the three that matter.
  const [open, setOpen] = useState<Record<string, boolean>>({})

  const [saved, setSaved] = useState<Record<string, string>>({})

  async function analyse(text: string) {
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      const res = (await api.importMirth(text)) as ImportResult
      setResult(res)
      const expand: Record<string, boolean> = {}
      for (const c of res.channels) {
        if (statusOf(c) !== 'clean') expand[c.name] = true
      }
      setOpen(expand)
    } catch (e) {
      // The endpoint's problems carry the useful half - "a channel export starts with a <channel>
      // element" - so dropping them would leave somebody with a refusal and no idea what to do.
      setError({
        message: e instanceof Error ? e.message : String(e),
        problems: e instanceof ApiError ? e.problems : [],
      })
    } finally {
      setBusy(false)
    }
  }

  /** Reading a dropped file rather than making somebody open it and copy the contents. */
  async function onDrop(ev: React.DragEvent) {
    ev.preventDefault()
    const files = Array.from(ev.dataTransfer.files)
    if (files.length === 0) return

    const texts = await Promise.all(files.map((f) => f.text()))
    const joined = texts.join('\n')
    setXml(joined)
    void analyse(joined)
  }

  async function importOne(c: Imported) {
    try {
      await api.createChannel(c.yaml)
      setSaved((s) => ({ ...s, [c.name]: 'imported' }))
    } catch (e) {
      setSaved((s) => ({
        ...s,
        [c.name]: e instanceof Error ? e.message : String(e),
      }))
    }
  }

  return (
    <div className="space-y-6">
      <Section
        title="Come across from Mirth"
        description="Drop a Mirth or OIE channel export here — one channel or a whole server. Nothing is
        changed until you import a channel yourself, so this is safe to run on anything."
      >
        <div
          onDragOver={(e) => e.preventDefault()}
          onDrop={onDrop}
          className="rounded-xl border-2 border-dashed border-slate-700 bg-slate-900/40 p-8 text-center
            transition-colors hover:border-sky-500/60 hover:bg-sky-500/5"
        >
          <p className="text-slate-300">Drop your channel export here</p>
          <p className="mt-1 text-sm text-slate-500">
            A <code className="text-slate-400">.xml</code> file from Mirth&nbsp;→&nbsp;Export Channel, or a
            whole-server export
          </p>

          <label className="mt-4 inline-block cursor-pointer rounded-lg bg-sky-600 px-4 py-2 text-sm
            font-medium text-white hover:bg-sky-500">
            Choose a file
            <input
              type="file"
              accept=".xml,text/xml"
              multiple
              className="hidden"
              onChange={async (e) => {
                const files = Array.from(e.target.files ?? [])
                if (files.length === 0) return
                const texts = await Promise.all(files.map((f) => f.text()))
                const joined = texts.join('\n')
                setXml(joined)
                void analyse(joined)
              }}
            />
          </label>
        </div>

        <details className="mt-4">
          <summary className="cursor-pointer text-sm text-slate-400 hover:text-slate-300">
            or paste the XML instead
          </summary>
          <CodeArea
            language="xml"
            aria-label="Mirth channel XML"
            value={xml}
            onChange={setXml}
            spellCheck={false}
            rows={8}
            placeholder="<channel version=&quot;4.5.2&quot;> ..."
            className="mt-2 w-full"
          />
          <button
            onClick={() => void analyse(xml)}
            disabled={busy || xml.trim() === ''}
            className="btn-primary mt-2 py-1.5 text-sm"
          >
            Analyse
          </button>
        </details>
      </Section>

      {busy && (
        <div className="flex items-center gap-3 text-slate-400">
          <Spinner /> Reading the export…
        </div>
      )}

      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

      {result && <Verdict result={result} />}

      {result && (
        <div className="space-y-3">
          {result.channels.map((c) => (
            <ChannelCard
              key={c.name}
              channel={c}
              open={open[c.name] ?? false}
              onToggle={() => setOpen((o) => ({ ...o, [c.name]: !o[c.name] }))}
              status={saved[c.name]}
              onImport={() => void importOne(c)}
            />
          ))}
        </div>
      )}

      {result && result.failed.length > 0 && (
        <Section
          title="Could not be read"
          description="These are not translation problems — the XML itself could not be parsed. The
          channels above are unaffected."
        >
          <ul className="space-y-1 text-sm text-rose-300">
            {result.failed.map((f) => (
              <li key={f} className="font-mono text-xs">
                {f}
              </li>
            ))}
          </ul>
        </Section>
      )}

      {/* Offered after the import, because this is the next question and the one the import cannot
          answer: the channels came across, but will they produce the same output? */}
      <ParityCheck />
    </div>
  )
}

/** ParityCheck asks which channel to prove, then proves it.
 *
 * A channel name field rather than a dropdown, because the channel being verified has usually just
 * been imported and may not be running - and a dropdown of running channels would omit exactly the
 * ones somebody is here to check.
 */
function ParityCheck() {
  const [channel, setChannel] = useState('')

  return (
    <div className="space-y-3">
      <label className="block max-w-sm">
        <span className="text-xs font-medium text-slate-300">Which channel to prove</span>
        <input
          className="input mt-1"
          value={channel}
          onChange={(e) => setChannel(e.target.value)}
          placeholder="adt-inbound"
        />
        <span className="mt-1 block text-xs text-slate-500">
          The imported channel. It does not need to be running.
        </span>
      </label>
      {channel.trim() !== '' && <ParityPanel channel={channel.trim()} />}
    </div>
  )
}

/**
 * The headline. A fraction, large, with the denominator — because "37 ready" means nothing and
 * "37 of 40 ready" is the entire argument.
 */
function Verdict({ result }: { result: ImportResult }) {
  const s = result.summary
  const ready = s.clean + s.warnings
  const pct = s.total === 0 ? 0 : Math.round((ready / s.total) * 100)

  return (
    <Section title="What came across">
      <div className="grid gap-8 md:grid-cols-[auto_1fr]">
        <div className="flex flex-col items-center">
          <Donut
            size={168}
            label={`${pct}%`}
            slices={[
              { name: 'Ready', value: s.clean, colour: statusColour.clean },
              { name: 'With notes', value: s.warnings, colour: statusColour.warnings },
              { name: 'Needs work', value: s.blocked, colour: statusColour.blocked },
            ]}
          />
          <p className="mt-3 text-center text-2xl font-semibold text-slate-100">
            {ready} of {s.total}
          </p>
          <p className="text-sm text-slate-500">
            channel{s.total === 1 ? '' : 's'} will run on Perfuse
          </p>
        </div>

        <div className="space-y-5">
          <HBar
            rows={[
              {
                name: 'Ready to import',
                value: s.clean,
                colour: statusColour.clean,
              },
              {
                name: 'Imports, with notes to read',
                value: s.warnings,
                colour: statusColour.warnings,
              },
              {
                name: 'Needs work first',
                value: s.blocked,
                colour: statusColour.blocked,
              },
            ]}
          />

          {/* The declarative/scripted split. Included because it is the honest measure of what the
              migration gained, and leaving it out would be flattering rather than useful. */}
          <div className="rounded-lg border border-slate-800 bg-slate-900/40 p-4">
            <p className="text-sm text-slate-300">
              {s.declarativeSteps + s.scriptedSteps === 0 ? (
                'No transformation steps to convert.'
              ) : (
                <>
                  <span className="font-semibold text-emerald-300">{s.declarativeSteps}</span> transformation
                  step{s.declarativeSteps === 1 ? '' : 's'} became configuration you can edit in a form.{' '}
                  <span className="font-semibold text-sky-300">{s.scriptedSteps}</span> stayed as
                  JavaScript and will run unchanged.
                </>
              )}
            </p>
            <p className="mt-2 text-xs text-slate-500">
              Script that stays as script is not a failure — it runs exactly as it does today. Steps that
              became configuration are the ones you no longer have to maintain as code.
            </p>
          </div>
        </div>
      </div>
    </Section>
  )
}

function ChannelCard({
  channel,
  open,
  onToggle,
  status,
  onImport,
}: {
  channel: Imported
  open: boolean
  onToggle: () => void
  status?: string
  onImport: () => void
}) {
  const st = statusOf(channel)
  const imported = status === 'imported'

  return (
    <div
      className="overflow-hidden rounded-xl border border-slate-800 bg-slate-900/40"
      style={{ borderLeft: `3px solid ${statusColour[st]}` }}
    >
      <div className="flex items-center gap-3 p-4">
        <button onClick={onToggle} className="flex min-w-0 flex-1 items-center gap-3 text-left">
          <span
            className="inline-block h-2 w-2 shrink-0 rounded-full"
            style={{ background: statusColour[st] }}
          />
          <span className="min-w-0">
            <span className="block truncate font-medium text-slate-100">{channel.sourceName}</span>
            <span className="block text-xs text-slate-500">
              {statusLabel[st]}
              {channel.name !== channel.sourceName && <> · renamed to {channel.name}</>}
            </span>
          </span>
        </button>

        <div className="flex shrink-0 items-center gap-2">
          {/* The lock-in count, shown only when it is not zero.
              
              Deliberately not shown as "0 locked in" on a clean channel. A zero badge invites reading the absence of a problem as a
              measurement of one, and most channels are clean - the honest presentation of good news is silence. */}
          {(channel.portability?.vendorOnly ?? 0) > 0 && (
            <Pill label="vendor-only" value={channel.portability.vendorOnly} colour="#fbbf24" />
          )}
          <Pill label="declarative" value={channel.counts.declarative} colour="#34d399" />
          <Pill label="script" value={channel.counts.scripted} colour="#38bdf8" />
          {channel.counts.blockers > 0 && (
            <Pill label="blockers" value={channel.counts.blockers} colour="#f87171" />
          )}

          {imported ? (
            <span className="rounded-lg bg-emerald-500/15 px-3 py-1.5 text-sm text-emerald-300">
              Imported
            </span>
          ) : (
            <button
              onClick={onImport}
              disabled={channel.blocked}
              title={
                channel.blocked
                  ? 'This channel would not start. Resolve the blockers below first.'
                  : 'Create this channel on this server'
              }
              className="rounded-lg bg-sky-600 px-3 py-1.5 text-sm text-white hover:bg-sky-500
                disabled:cursor-not-allowed disabled:opacity-40"
            >
              Import
            </button>
          )}
        </div>
      </div>

      {status && status !== 'imported' && (
        <p className="border-t border-slate-800 bg-rose-500/10 px-4 py-2 text-sm text-rose-300">
          {status}
        </p>
      )}

      {open && (
        <div className="space-y-4 border-t border-slate-800 p-4">
          {/* The portability verdict, before the per-line notes.
              
              It answers a different question from everything below it. The notes answer "what must I rewrite to move to Perfuse",
              which only interests somebody already looking at Perfuse. This answers "how much of this only runs on one vendor's
              software", which is worth knowing before having any opinion about Perfuse at all.
              
              The sentence comes from the server rather than being composed here from the counts, because the qualification is what
              keeps it honest - most Java in Mirth scripts is not lock-in, and a number without that beside it reads as though it
              were. */}
          <div className="rounded-lg border border-slate-800 bg-slate-950/50 p-3">
            <h4 className="text-xs font-medium uppercase tracking-wide text-slate-500">Portability</h4>
            <p className="mt-1 text-sm text-slate-300">{channel.portabilityVerdict}</p>

            {(channel.portability?.vendorReferences ?? []).length > 0 && (
              <>
                <p className="mt-2 text-xs text-slate-500">
                  These are the calls that need the vendor's own server. A count is an assertion; this is the list, so it can be
                  checked against the channel.
                </p>
                <ul className="mt-1 space-y-0.5">
                  {(channel.portability?.vendorReferences ?? []).map((r) => (
                    <li key={r} className="font-mono text-xs text-amber-300">
                      {r}
                    </li>
                  ))}
                </ul>
              </>
            )}
          </div>

          {channel.notes.length > 0 && (
            <ul className="space-y-2">
              {channel.notes.map((n, i) => (
                <li
                  key={i}
                  className="rounded-lg border border-slate-800 bg-slate-950/50 p-3 text-sm"
                >
                  <div className="flex items-baseline gap-2">
                    <span
                      className="rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide"
                      style={noteBadge(n.severity)}
                    >
                      {n.severity}
                    </span>
                    {n.where && <span className="font-mono text-xs text-slate-500">{n.where}</span>}
                  </div>
                  <p className="mt-1.5 text-slate-300">{n.message}</p>
                  {/* The action is separated from the message so somebody scanning for what to do can
                      read only this line. */}
                  {n.action && <p className="mt-1 text-slate-400">→ {n.action}</p>}
                </li>
              ))}
            </ul>
          )}

          <details>
            <summary className="cursor-pointer text-sm text-slate-400 hover:text-slate-300">
              Show the channel this would create
            </summary>
            <SyntaxBlock code={channel.yaml} language="yaml" className="mt-2 max-h-96" />
          </details>
        </div>
      )}
    </div>
  )
}

/**
 * Info notes are the commonest kind and most of them are good news - "this was carried over". Giving
 * them the amber of a warning would make a clean channel look alarming, which is the opposite of what
 * the reader should take from it.
 */
function noteBadge(severity: string): { background: string; color: string } {
  switch (severity) {
    case 'blocker':
      return { background: 'rgba(248,113,113,.15)', color: '#fca5a5' }
    case 'warning':
      return { background: 'rgba(251,191,36,.15)', color: '#fcd34d' }
    default:
      return { background: 'rgba(148,163,184,.12)', color: '#94a3b8' }
  }
}

function Pill({ label, value, colour }: { label: string; value: number; colour: string }) {
  if (value === 0) return null
  return (
    <span
      className="hidden rounded-md px-2 py-1 text-xs sm:inline-block"
      style={{ background: `${colour}1a`, color: colour }}
      title={`${value} ${label}`}
    >
      {value} {label}
    </span>
  )
}
