import { useEffect, useState } from 'react'
import { Donut, HBar } from './charts'
import { ErrorBox, Section, Spinner } from './ui'
import type { UiError } from './store'
import { FleetPeersPanel } from './FleetPeersPanel'

type Reachability = 'unknown' | 'reachable' | 'unreachable' | 'unauthorised' | 'incompatible'

interface Health {
  channelsTotal: number
  channelsRunning: number
  channelsStopped: number
  channelsErrored: number
  queueDepth: number
  queueOldestSeconds: number
  alertsFiring: number
  draining: boolean
}

interface PeerStatus {
  name: string
  url: string
  reachability: Reachability
  error?: string
  checkedAt: string
  lastReachable?: string
  latencyMs: number
  version?: string
  skewSeconds: number
  health?: Health
  allowControl: boolean
}

interface SelfMember {
  name: string
  reachability: Reachability
  version?: string
  health?: Health
  checkedAt: string
  summary: string
}

interface Roll {
  total: number
  reachable: number
  unreachable: number
  undetermined: number
  channelsTotal: number
  channelsRunning: number
  channelsErrored: number
  queueDepth: number
  alertsFiring: number
  knownFrom: number
  maxSkewSeconds: number
}

interface FleetResponse {
  configured: boolean
  self: SelfMember
  peers: PeerStatus[]
  roll: Roll
  pollEverySeconds: number
  anyControllable: boolean
}

// Colour carries the meaning here, so it is defined once. The important property is that unreachable and reachable
// are not merely different shades — a dead server must not be able to read as a quiet one at a glance across a room,
// which is how these pages are actually used.
const reachabilityStyle: Record<Reachability, { dot: string; text: string; ring: string; label: string }> = {
  reachable: {
    dot: 'bg-emerald-400',
    text: 'text-emerald-300',
    ring: 'border-emerald-900/60 bg-emerald-950/20',
    label: 'Reachable',
  },
  unreachable: {
    dot: 'bg-rose-500',
    text: 'text-rose-300',
    ring: 'border-rose-900/60 bg-rose-950/25',
    label: 'Unreachable',
  },
  unauthorised: {
    dot: 'bg-amber-400',
    text: 'text-amber-300',
    ring: 'border-amber-900/60 bg-amber-950/25',
    label: 'Not authorised',
  },
  incompatible: {
    dot: 'bg-violet-400',
    text: 'text-violet-300',
    ring: 'border-violet-900/60 bg-violet-950/25',
    label: 'Version mismatch',
  },
  unknown: {
    dot: 'bg-slate-500',
    text: 'text-slate-400',
    ring: 'border-slate-800 bg-slate-900/40',
    label: 'Not polled yet',
  },
}

function age(iso?: string): string {
  if (!iso) return 'never'
  const seconds = (Date.now() - new Date(iso).getTime()) / 1000
  if (seconds < 0) return 'just now'
  if (seconds < 60) return `${Math.round(seconds)}s ago`
  if (seconds < 3600) return `${Math.round(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h ago`
  return `${Math.round(seconds / 86400)}d ago`
}

/** A pulsing dot for a live instance, still for one that is not. Motion is reserved for "this is currently true". */
function StatusDot({ reachability }: { reachability: Reachability }) {
  const style = reachabilityStyle[reachability]
  return (
    <span className="relative inline-flex h-3 w-3 shrink-0" aria-hidden="true">
      {reachability === 'reachable' && (
        <span className={`absolute inline-flex h-full w-full animate-ping rounded-full ${style.dot} opacity-60`} />
      )}
      <span className={`relative inline-flex h-3 w-3 rounded-full ${style.dot}`} />
    </span>
  )
}

function MemberCard({
  name,
  reachability,
  summary,
  version,
  health,
  url,
  checkedAt,
  lastReachable,
  latencyMs,
  skewSeconds,
  isSelf,
}: {
  name: string
  reachability: Reachability
  summary: string
  version?: string
  health?: Health
  url?: string
  checkedAt?: string
  lastReachable?: string
  latencyMs?: number
  skewSeconds?: number
  isSelf?: boolean
}) {
  const style = reachabilityStyle[reachability]
  const running = health?.channelsRunning ?? 0
  const total = health?.channelsTotal ?? 0
  const pct = total > 0 ? Math.round((running / total) * 100) : 0

  return (
    <div className={`rounded-xl border p-4 transition-colors ${style.ring}`}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <StatusDot reachability={reachability} />
            <span className="truncate font-medium text-slate-100">{name}</span>
            {isSelf && (
              <span className="rounded bg-sky-950/60 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-sky-300">
                this server
              </span>
            )}
            {health?.draining && (
              <span className="rounded bg-slate-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-slate-300">
                draining
              </span>
            )}
          </div>
          {url && <div className="mt-1 truncate font-mono text-xs text-slate-500">{url}</div>}
        </div>

        <div className="shrink-0 text-right">
          <div className={`text-xs font-medium ${style.text}`}>{style.label}</div>
          {version && <div className="mt-0.5 font-mono text-[10px] text-slate-500">{version}</div>}
        </div>
      </div>

      <p className="mt-3 text-sm text-slate-400">{summary}</p>

      {health && !health.draining && (
        <>
          {/* A bar rather than a number, because "9 of 10" and "90 of 100" read identically as text and very
              differently as a shape. */}
          <div className="mt-3">
            <div className="mb-1 flex justify-between text-[11px] text-slate-500">
              <span>
                {running} of {total} channel(s) running
              </span>
              <span>{pct}%</span>
            </div>
            <div className="h-2 overflow-hidden rounded-full bg-slate-800">
              <div
                className={`h-full rounded-full transition-all ${
                  health.channelsErrored > 0 ? 'bg-rose-500' : 'bg-emerald-500'
                }`}
                style={{ width: `${pct}%` }}
              />
            </div>
          </div>

          <div className="mt-3 grid grid-cols-3 gap-2 text-center">
            <div className="rounded-lg bg-slate-900/60 p-2">
              <div className="text-lg font-semibold text-slate-100">{health.queueDepth}</div>
              <div className="text-[10px] uppercase tracking-wide text-slate-500">queued</div>
            </div>
            <div className="rounded-lg bg-slate-900/60 p-2">
              <div
                className={`text-lg font-semibold ${
                  health.channelsErrored > 0 ? 'text-rose-300' : 'text-slate-100'
                }`}
              >
                {health.channelsErrored}
              </div>
              <div className="text-[10px] uppercase tracking-wide text-slate-500">in error</div>
            </div>
            <div className="rounded-lg bg-slate-900/60 p-2">
              <div
                className={`text-lg font-semibold ${health.alertsFiring > 0 ? 'text-amber-300' : 'text-slate-100'}`}
              >
                {health.alertsFiring}
              </div>
              <div className="text-[10px] uppercase tracking-wide text-slate-500">alerts</div>
            </div>
          </div>
        </>
      )}

      <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-slate-500">
        {checkedAt && <span>checked {age(checkedAt)}</span>}
        {latencyMs !== undefined && latencyMs > 0 && <span>{latencyMs} ms</span>}
        {reachability !== 'reachable' && lastReachable && <span>last answered {age(lastReachable)}</span>}
        {/* Surfaced rather than corrected: a clock disagreement makes every aggregated time series suspect, and
            silently normalising it would hide a real misconfiguration. */}
        {skewSeconds !== undefined && Math.abs(skewSeconds) > 5 && (
          <span className="text-amber-400">clock differs by {Math.round(Math.abs(skewSeconds))}s</span>
        )}
      </div>
    </div>
  )
}

export function Fleet() {
  const [data, setData] = useState<FleetResponse | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false

    const load = async () => {
      try {
        const res = await fetch('/api/fleet')
        if (!res.ok) {
          const body = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
          throw new Error(body.error ?? `HTTP ${res.status}`)
        }
        const body = (await res.json()) as FleetResponse
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
    // Refreshed on the server's own polling interval, so the page cannot claim to be fresher than the data behind
    // it. Defaulted before the first response arrives.
    const every = (data?.pollEverySeconds ?? 15) * 1000
    const timer = setInterval(load, every)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data?.pollEverySeconds])

  if (loading && !data) return <Spinner />
  if (error && !data) return <ErrorBox error={error} />
  if (!data) return null

  const { roll, self, peers } = data

  if (!data.configured) {
    return (
      <div className="space-y-6">
        <Section
          title="Fleet"
          description="Watch every Perfuse instance from any one of them. No agent to install and no monitoring server to buy — the console is already in every binary."
        >
          <div className="grid gap-4 md:grid-cols-2">
          <MemberCard
            name={self.name}
            reachability={self.reachability}
            summary={self.summary}
            version={self.version}
            health={self.health ?? undefined}
            checkedAt={self.checkedAt}
            isSelf
          />

          {/* A form, not a pair of commands to run elsewhere.
              
              This panel used to print a token command for the other machine and a peers file to write by hand,
              followed by a restart. Two of those three steps were on this server and neither was possible from
              here, which made the section an explanation of work you had to do somewhere else. The one step that
              genuinely belongs on the other instance - issuing its own token - is still described, inside the
              form, where it is needed. */}
          <div className="rounded-xl border border-dashed border-slate-800 p-4">
            <h3 className="font-medium text-slate-200">Add another instance</h3>
            <p className="mt-2 mb-3 text-sm text-slate-400">
              This is a fleet of one. Watching another instance needs its address and a read-only token issued on
              it.
            </p>

            <FleetPeersPanel />

            <p className="mt-4 text-xs text-slate-500">
              The watching instance stores no messages from its peers — only whether they are up and how much they
              have queued. Patient data never leaves the instance that received it.
            </p>
          </div>
          </div>
        </Section>
      </div>
    )
  }

  const attention = peers.filter((p) => p.reachability !== 'reachable')

  return (
    <div className="space-y-6">
      <Section
        title="Fleet"
        description={`${roll.total} instance(s), refreshed every ${Math.round(data.pollEverySeconds)}s. Mirth sells this as a separate product; here it is a config file.`}
      >
        <span className="sr-only">Fleet overview</span>
      </Section>

      {/* The headline states what is known and what is not, rather than a single reassuring number. */}
      <div className="grid gap-4 lg:grid-cols-[auto_1fr]">
        <div className="rounded-xl border border-slate-800 p-4">
          <Donut
            size={160}
            label={`${roll.reachable}/${roll.total}`}
            slices={[
              { name: 'Reachable', value: roll.reachable, colour: '#34d399' },
              { name: 'Unreachable', value: roll.unreachable, colour: '#f43f5e' },
              { name: 'Undetermined', value: roll.undetermined, colour: '#fbbf24' },
            ]}
          />
          <p className="mt-2 text-center text-xs text-slate-500">instances answering</p>
        </div>

        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          {[
            { label: 'channels running', value: roll.channelsRunning, of: roll.channelsTotal, tone: 'text-emerald-300' },
            { label: 'channels in error', value: roll.channelsErrored, tone: roll.channelsErrored > 0 ? 'text-rose-300' : 'text-slate-100' },
            { label: 'messages queued', value: roll.queueDepth, tone: roll.queueDepth > 0 ? 'text-sky-300' : 'text-slate-100' },
            { label: 'alerts firing', value: roll.alertsFiring, tone: roll.alertsFiring > 0 ? 'text-amber-300' : 'text-slate-100' },
          ].map((tile) => (
            <div key={tile.label} className="rounded-xl border border-slate-800 p-4">
              <div className={`text-3xl font-semibold ${tile.tone}`}>
                {tile.value}
                {tile.of !== undefined && <span className="text-lg text-slate-500"> / {tile.of}</span>}
              </div>
              <div className="mt-1 text-xs uppercase tracking-wide text-slate-500">{tile.label}</div>
            </div>
          ))}

          {/* Without this the totals above are unreadable. "12 running" means something different when two of five
              servers did not answer, and the number alone cannot say so. */}
          {roll.knownFrom < roll.total && (
            <div className="col-span-2 rounded-xl border border-amber-900/60 bg-amber-950/25 p-3 text-sm text-amber-200 sm:col-span-4">
              These totals come from {roll.knownFrom} of {roll.total} instance(s). The rest could not be read, so the
              real figures are higher by an unknown amount.
            </div>
          )}

          {Math.abs(roll.maxSkewSeconds) > 30 && (
            <div className="col-span-2 rounded-xl border border-amber-900/60 bg-amber-950/25 p-3 text-sm text-amber-200 sm:col-span-4">
              Clocks differ by up to {Math.round(Math.abs(roll.maxSkewSeconds))}s across this fleet. Any chart that
              combines instances will be distorted by that much.
            </div>
          )}
        </div>
      </div>

      {attention.length > 0 && (
        <div className="space-y-2">
          <h3 className="text-sm font-medium text-slate-300">Needs attention</h3>
          {attention.map((p) => (
            <div
              key={p.name}
              className={`rounded-xl border p-3 ${reachabilityStyle[p.reachability].ring}`}
            >
              <div className="flex items-center gap-2">
                <StatusDot reachability={p.reachability} />
                <span className="font-medium text-slate-100">{p.name}</span>
                <span className={`text-xs ${reachabilityStyle[p.reachability].text}`}>
                  {reachabilityStyle[p.reachability].label}
                </span>
              </div>
              {p.error && <p className="mt-2 text-sm text-slate-300">{p.error}</p>}
            </div>
          ))}
        </div>
      )}

      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <MemberCard
          name={self.name}
          reachability={self.reachability}
          summary={self.summary}
          version={self.version}
          health={self.health ?? undefined}
          checkedAt={self.checkedAt}
          isSelf
        />
        {peers.map((p) => (
          <MemberCard
            key={p.name}
            name={p.name}
            reachability={p.reachability}
            summary={p.error ?? summaryFor(p)}
            version={p.version}
            health={p.health ?? undefined}
            url={p.url}
            checkedAt={p.checkedAt}
            lastReachable={p.lastReachable}
            latencyMs={p.latencyMs}
            skewSeconds={p.skewSeconds}
          />
        ))}
      </div>

      {roll.channelsTotal > 0 && (
        <div className="rounded-xl border border-slate-800 p-4">
          <h3 className="mb-3 text-sm font-medium text-slate-300">Channels per instance</h3>
          <HBar
            rows={[
              {
                name: `${self.name} (this server)`,
                value: self.health?.channelsRunning ?? 0,
                colour: '#38bdf8',
                note: `${self.health?.channelsTotal ?? 0} configured`,
              },
              ...peers.map((p) => ({
                name: p.name,
                value: p.health?.channelsRunning ?? 0,
                colour: p.reachability === 'reachable' ? '#34d399' : '#475569',
                // A peer that could not be read shows zero, and saying so stops that zero being read as "nothing
                // is running there".
                note: p.health ? `${p.health.channelsTotal} configured` : 'could not be read',
              })),
            ]}
          />
        </div>
      )}

      <p className="text-xs text-slate-500">
        This instance stores nothing from its peers except the figures above — no messages and no message content.
        Patient data never leaves the instance that received it.
      </p>
    </div>
  )
}

function summaryFor(p: PeerStatus): string {
  if (!p.health) return reachabilityStyle[p.reachability].label
  if (p.health.draining) {
    return `draining; ${p.health.channelsRunning} of ${p.health.channelsTotal} channel(s) still running`
  }
  if (p.health.channelsErrored > 0) {
    return `${p.health.channelsErrored} channel(s) in error, ${p.health.channelsRunning} of ${p.health.channelsTotal} running`
  }
  return `${p.health.channelsRunning} of ${p.health.channelsTotal} channel(s) running`
}
