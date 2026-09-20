import { SiteLogo } from './Branding'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useSelector } from 'react-redux'
import { api } from './api'
import type {
  Bucket,
  ChannelState,
  DestinationStat,
  MessageStats,
  StatusResponse,
} from './api'
import { Donut, HBar, Sparkline, StackedAreaChart, outcomeColours } from './charts'
import type { RootState } from './store'
import { Confirm, ErrorBox, Section, Spinner } from './ui'

/**
 * The dashboard.
 *
 * This is the screen meant to be left open, so it updates itself over
 * server-sent events rather than making somebody press refresh, and it leads with
 * the two numbers that matter during an incident: is anything failing, and is
 * anything not running.
 */
export function Dashboard({ onBuildChannel }: { onBuildChannel?: () => void }) {
  const me = useSelector((s: RootState) => s.session.me)!
  const canControl = me.role === 'admin'

  const [status, setStatus] = useState<StatusResponse | null>(null)
  const [stats, setStats] = useState<MessageStats | null>(null)
  const [destinations, setDestinations] = useState<DestinationStat[]>([])
  const [buckets, setBuckets] = useState<Bucket[]>([])
  const [window, setWindow] = useState<'1h' | '6h' | '24h'>('1h')
  const [error, setError] = useState<string | null>(null)
  const [live, setLive] = useState(true)
  const [busy, setBusy] = useState<string | null>(null)
  const [confirmStop, setConfirmStop] = useState<string | null>(null)

  // A short history per channel, kept client-side so each card can show a
  // sparkline without asking the server for one series per channel.
  const history = useRef<Map<string, number[]>>(new Map())

  const loadSlow = useCallback(async () => {
    try {
      const [statsRes, throughputRes] = await Promise.all([
        api.stats(),
        api.throughput(window, window === '1h' ? '1m' : window === '6h' ? '5m' : '15m'),
      ])
      setStats(statsRes.stats)
      setDestinations(statsRes.destinations ?? [])
      setBuckets(throughputRes.buckets ?? [])
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not load statistics')
    }
  }, [window])

  useEffect(() => {
    loadSlow()
    const timer = setInterval(loadSlow, 15000)
    return () => clearInterval(timer)
  }, [loadSlow])

  // Live status over SSE. The browser reconnects on its own, which is most of why
  // this is SSE rather than a WebSocket.
  useEffect(() => {
    if (!live) return

    const source = new EventSource('/api/events?interval=2s')

    source.addEventListener('status', (event) => {
      try {
        const payload = JSON.parse((event as MessageEvent).data) as StatusResponse
        setStatus((previous) => {
          payload.channels?.forEach((channel) => {
            const before = previous?.channels.find((c) => c.name === channel.name)
            const delta = before ? Math.max(0, channel.received - before.received) : 0
            const series = history.current.get(channel.name) ?? []
            series.push(delta)
            if (series.length > 40) series.shift()
            history.current.set(channel.name, series)
          })
          return payload
        })
      } catch {
        // A malformed frame is not worth tearing the page down for.
      }
    })

    source.onerror = () => {
      // EventSource reconnects by itself; surfacing every blip would make the
      // page flash a warning whenever a laptop lid closes.
    }

    return () => source.close()
  }, [live])

  useEffect(() => {
    if (live) return
    let cancelled = false
    api
      .status()
      .then((s) => !cancelled && setStatus(s))
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [live])

  async function control(name: string, action: 'start' | 'stop') {
    setBusy(name)
    try {
      if (action === 'start') await api.startChannel(name)
      else await api.stopChannel(name)
      setStatus(await api.status())
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'the action failed')
    } finally {
      setBusy(null)
    }
  }

  const series = useMemo(
    () => [
      {
        name: 'delivered',
        colour: outcomeColours.delivered!,
        points: buckets.map((b) => ({ x: Date.parse(b.start), y: b.delivered })),
      },
      {
        name: 'filtered',
        colour: outcomeColours.filtered!,
        points: buckets.map((b) => ({ x: Date.parse(b.start), y: b.filtered })),
      },
      {
        name: 'unparseable',
        colour: outcomeColours.unparseable!,
        points: buckets.map((b) => ({ x: Date.parse(b.start), y: b.unparseable })),
      },
      // Failures are the last layer so they sit on top and are impossible to miss.
      {
        name: 'failed',
        colour: outcomeColours.failed!,
        points: buckets.map((b) => ({ x: Date.parse(b.start), y: b.failed })),
      },
    ]
      // A layer that is zero everywhere is not drawn.
      .filter((s, _, all) => {
        const hasValue = s.points.some((p) => p.y > 0)
        if (hasValue) return true

        // If nothing happened at all, keep exactly one layer so the chart still draws a flat line at zero.
        const anythingAtAll = all.some((other) => other.points.some((p) => p.y > 0))

        return !anythingAtAll && s.name === 'delivered'
      }),
    [buckets],
  )

  const ops = useOperationalSummary(live)

  const failing = (stats?.byOutcome.failed ?? 0) + (stats?.byOutcome.partial ?? 0)
  const stopped = status ? status.channelsTotal - status.channelRunning : 0
  const broken = status?.channelsBroken ?? 0

  return (
    <div className="space-y-5">
      {/* Ambient background glow */}
      <div className="pointer-events-none fixed inset-0 -z-10 overflow-hidden" aria-hidden="true">
        <div
          className="ambient-glow absolute -top-32 -left-32 h-96 w-96 rounded-full opacity-[0.04]"
          style={{ background: `radial-gradient(circle, var(--brand-accent), transparent 70%)` }}
        />
        <div
          className="ambient-glow absolute -right-32 top-1/3 h-80 w-80 rounded-full opacity-[0.03]"
          style={{ background: `radial-gradient(circle, var(--color-emerald-500), transparent 70%)`, animationDelay: '-7s' }}
        />
      </div>

      {/*
        The operator's logo, above everything, when they have uploaded one.
        The dashboard is the screen left open on a wall and the one a customer is shown, so it is the place where this should look
        like their product rather than ours. It renders nothing when no logo has been uploaded - repeating our own mark here would
        be decoration, and the header above already says what the software is.
      */}
      <SiteLogo size={44} className="mb-1" />

      <div className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="text-lg font-semibold text-slate-100">Dashboard</h1>
          <p className="mt-1 text-sm text-slate-500">
            Live channel state and the last 24 hours of traffic.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <div className="flex rounded-lg border border-slate-800 bg-slate-900/50 p-0.5">
            {(['1h', '6h', '24h'] as const).map((w) => (
              <button
                key={w}
                onClick={() => setWindow(w)}
                className={`rounded-md px-2.5 py-1 text-xs font-medium transition ${
                  window === w ? 'bg-slate-700/80 text-slate-100 shadow-sm' : 'text-slate-500 hover:text-slate-300'
                }`}
              >
                {w}
              </button>
            ))}
          </div>
          <button
            className="btn-ghost py-1.5 text-xs"
            onClick={() => setLive((v) => !v)}
            title={live ? 'Updating every 2 seconds' : 'Updates paused'}
          >
            <span
              className={`size-2 rounded-full ${live ? 'animate-pulse bg-emerald-400' : 'bg-slate-600'}`}
            />
            {live ? 'Live' : 'Paused'}
          </button>
        </div>
      </div>

      {error && <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />}

      {/* Primary stat cards — hero row */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          label="Channels running"
          value={status ? `${status.channelRunning}/${status.channelsTotal}` : '—'}
          tone={broken > 0 ? 'bad' : stopped > 0 ? 'warn' : 'good'}
          icon="channels"
          note={
            broken > 0
              ? `${broken} file(s) will not load — see Channels`
              : stopped > 0
                ? `${stopped} not running`
                : 'all running'
          }
        />
        <StatCard
          label="Messages, 24h"
          value={stats ? stats.total.toLocaleString() : '—'}
          icon="messages"
          note={stats?.newestKept ? `latest ${new Date(stats.newestKept).toLocaleTimeString()}` : ''}
        />
        <StatCard
          label="Failures, 24h"
          value={failing.toLocaleString()}
          tone={failing > 0 ? 'bad' : 'good'}
          icon="failures"
          note={failing > 0 ? 'needs attention' : 'none'}
        />
        <StatCard
          label="Stored"
          value={stats ? formatBytes(stats.storedBytes) : '—'}
          icon="storage"
          note={stats?.oldestKept ? `since ${new Date(stats.oldestKept).toLocaleDateString()}` : ''}
        />
      </div>

      {/* Operational row */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          label="Queued now"
          value={ops.queueDepth.toLocaleString()}
          tone={ops.queueDepth > 0 ? 'warn' : 'good'}
          icon="queue"
          note={
            ops.queueOldest > 0
              ? `oldest waiting ${formatAge(ops.queueOldest)}`
              : 'nothing waiting'
          }
        />
        <StatCard
          label="Alerts firing"
          value={ops.alertsFiring.toLocaleString()}
          tone={ops.alertsCritical > 0 ? 'bad' : ops.alertsFiring > 0 ? 'warn' : 'good'}
          icon="alerts"
          note={
            ops.alertsCritical > 0
              ? `${ops.alertsCritical} critical`
              : ops.alertsFiring > 0
                ? 'none critical'
                : 'nothing firing'
          }
        />
        <StatCard
          label="Shadowed"
          value={ops.shadows === 0 ? '—' : ops.shadows.toLocaleString()}
          tone={ops.shadowDiffering > 0 ? 'warn' : undefined}
          icon="shadow"
          note={
            ops.shadows === 0
              ? 'no candidates running'
              : ops.shadowDiffering > 0
                ? `${ops.shadowDiffering} showing differences`
                : 'matching so far'
          }
        />
        <StatCard
          label="Certificates"
          value={
            ops.certsExpired > 0
              ? `${ops.certsExpired} expired`
              : ops.certsExpiring > 0
                ? `${ops.certsExpiring} expiring`
                : ops.certsTotal === 0
                  ? '—'
                  : 'all valid'
          }
          tone={ops.certsExpired > 0 ? 'bad' : ops.certsExpiring > 0 ? 'warn' : 'good'}
          icon="certs"
          note={
            ops.certsTotal === 0
              ? 'no TLS endpoints'
              : ops.soonestExpiry !== null
                ? `soonest in ${ops.soonestExpiry} days`
                : `${ops.certsTotal} endpoint(s)`
          }
        />
      </div>

      <Section
        title="Throughput"
        description="Stacked, so total traffic and the share of it that went wrong read at the same time."
      >
        <StackedAreaChart series={series} height={240} />
        <div className="mt-3 flex flex-wrap gap-4 text-xs text-slate-500">
          {series.map((s) => (
            <span key={s.name} className="flex items-center gap-1.5">
              <span className="size-2 rounded-sm" style={{ background: s.colour }} />
              {s.name}
            </span>
          ))}
        </div>
      </Section>

      <div className="grid gap-4 lg:grid-cols-2">
        <Section title="Outcomes, 24h" description="A proportion without a total is not actionable.">
          <Donut
            label="messages"
            slices={Object.entries(stats?.byOutcome ?? {}).map(([name, value]) => ({
              name,
              value,
              colour: outcomeColours[name] ?? '#64748b',
            }))}
          />
        </Section>

        <Section title="Message types, 24h">
          <HBar
            rows={Object.entries(stats?.byType ?? {})
              .sort((a, b) => b[1] - a[1])
              .slice(0, 8)
              .map(([name, value]) => ({ name, value }))}
          />
        </Section>
      </div>

      <Section
        title="Destinations, 24h"
        description="Average attempts matters: a destination that only succeeds on the fourth try looks healthy in a success count."
      >
        {destinations.length === 0 ? (
          <p className="text-sm text-slate-500">No deliveries recorded yet.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-left text-xs tracking-wide text-slate-500 uppercase">
                <tr>
                  <th className="pb-2">Destination</th>
                  <th className="pb-2 text-right">Delivered</th>
                  <th className="pb-2 text-right">Failed</th>
                  <th className="pb-2 text-right">Filtered</th>
                  <th className="pb-2 text-right">Avg attempts</th>
                  <th className="pb-2 text-right">Avg time</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-800">
                {destinations.map((d) => (
                  <tr key={d.destination}>
                    <td className="py-2 font-medium text-slate-200">{d.destination}</td>
                    <td className="py-2 text-right font-mono text-emerald-300">{d.delivered}</td>
                    <td
                      className={`py-2 text-right font-mono ${
                        d.failed > 0 ? 'text-rose-300' : 'text-slate-400'
                      }`}
                    >
                      {d.failed}
                    </td>
                    <td className="py-2 text-right font-mono text-slate-500">{d.filtered}</td>
                    <td
                      className={`py-2 text-right font-mono ${
                        d.avgAttempts > 1.5 ? 'text-amber-300' : 'text-slate-400'
                      }`}
                    >
                      {d.avgAttempts.toFixed(1)}
                    </td>
                    <td className="py-2 text-right font-mono text-slate-400">
                      {Math.round(d.avgDurationMs)}ms
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>

      <div>
        <h2 className="mb-3 text-sm font-semibold tracking-wide text-slate-300 uppercase">
          Channels
        </h2>

        {!status && <Spinner label="Waiting for status…" />}

        {status && status.channelsTotal === 0 && (
          <div className="card p-8 text-center">
            <p className="text-sm text-slate-300">
              Nothing is running yet, because no channels exist.
            </p>
            <p className="mx-auto mt-2 max-w-xl text-xs leading-relaxed text-slate-400">
              A channel is one interface: where messages arrive, what to do with them, and where
              they go. Each one is a file on disk, so it can be reviewed and committed like the
              rest of your configuration.
            </p>
            {onBuildChannel && (
              <button className="btn-primary mt-4" onClick={onBuildChannel}>
                Build your first channel
              </button>
            )}
          </div>
        )}

        <div className="grid gap-4 md:grid-cols-2">
          {status?.channels.map((channel) => (
            <ChannelCard
              key={channel.name}
              channel={channel}
              spark={history.current.get(channel.name) ?? []}
              canControl={canControl}
              busy={busy === channel.name}
              onStart={() => control(channel.name, 'start')}
              onStop={() => setConfirmStop(channel.name)}
            />
          ))}
        </div>
      </div>

      <Confirm
        open={confirmStop !== null}
        title={`Stop ${confirmStop}?`}
        body="The channel finishes the message it is handling, then stops accepting connections. The sending system will not be able to deliver until it is started again."
        confirmLabel="Stop channel"
        onConfirm={() => {
          if (confirmStop) control(confirmStop, 'stop')
          setConfirmStop(null)
        }}
        onCancel={() => setConfirmStop(null)}
      />
    </div>
  )
}

/* ──────────────────────────────────────────────────────────────────────────────
   StatCard — the hero metric component with gradient icon badge and trend
   ────────────────────────────────────────────────────────────────────────────── */

const toneConfig = {
  neutral: {
    text: 'text-slate-100',
    iconBg: 'from-slate-600 to-slate-700',
    ring: 'ring-slate-700/50',
  },
  good: {
    text: 'text-emerald-300',
    iconBg: 'from-emerald-600 to-teal-700',
    ring: 'ring-emerald-800/40',
  },
  warn: {
    text: 'text-amber-300',
    iconBg: 'from-amber-600 to-orange-700',
    ring: 'ring-amber-800/40',
  },
  bad: {
    text: 'text-rose-300',
    iconBg: 'from-rose-600 to-pink-700',
    ring: 'ring-rose-800/40',
  },
} as const

function StatCard({
  label,
  value,
  note,
  tone = 'neutral',
  icon,
}: {
  label: string
  value: string
  note?: string
  tone?: 'neutral' | 'good' | 'warn' | 'bad'
  icon?: string
}) {
  const cfg = toneConfig[tone]
  return (
    <div className="card p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <p className="label mb-2">{label}</p>
          <p className={`text-2xl font-semibold tabular-nums metric-value ${cfg.text}`}>{value}</p>
          {note && <p className="mt-1.5 text-xs text-slate-500">{note}</p>}
        </div>
        {icon && (
          <div className={`flex size-9 shrink-0 items-center justify-center rounded-lg bg-gradient-to-br ${cfg.iconBg} ring-1 ${cfg.ring} shadow-md`}>
            <StatIcon name={icon} />
          </div>
        )}
      </div>
    </div>
  )
}

/** SVG icons for stat cards. Tiny inline SVGs - no dependency needed. */
function StatIcon({ name }: { name: string }) {
  const cls = "size-4 text-white/90"
  switch (name) {
    case 'channels':
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
          <path d="M2 4h12M2 8h12M2 12h12" />
        </svg>
      )
    case 'messages':
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
          <path d="M2 3h12v9H4l-2 2V3z" />
        </svg>
      )
    case 'failures':
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
          <path d="M8 3v5M8 11v1" />
          <path d="M3 13h10L8 3 3 13z" />
        </svg>
      )
    case 'storage':
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
          <ellipse cx="8" cy="4" rx="5" ry="2" />
          <path d="M3 4v8c0 1.1 2.2 2 5 2s5-.9 5-2V4" />
          <path d="M3 8c0 1.1 2.2 2 5 2s5-.9 5-2" />
        </svg>
      )
    case 'queue':
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
          <path d="M4 3v10M8 5v8M12 7v6" />
        </svg>
      )
    case 'alerts':
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
          <path d="M8 2a4 4 0 0 0-4 4v3l-1 2h10l-1-2V6a4 4 0 0 0-4-4zM6.5 13h3" />
        </svg>
      )
    case 'shadow':
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
          <rect x="2" y="4" width="8" height="8" rx="1" />
          <rect x="6" y="2" width="8" height="8" rx="1" opacity="0.5" />
        </svg>
      )
    case 'certs':
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
          <rect x="3" y="6" width="10" height="7" rx="1.5" />
          <path d="M5 6V4.5a3 3 0 0 1 6 0V6" />
          <circle cx="8" cy="10" r="1" fill="currentColor" />
        </svg>
      )
    default:
      return null
  }
}

/* ──────────────────────────────────────────────────────────────────────────────
   ChannelCard — status with accessible colour coding
   ────────────────────────────────────────────────────────────────────────────── */

function ChannelCard({
  channel,
  spark,
  canControl,
  busy,
  onStart,
  onStop,
}: {
  channel: ChannelState
  spark: number[]
  canControl: boolean
  busy: boolean
  onStart: () => void
  onStop: () => void
}) {
  const problems = channel.failed + channel.partial + channel.unparseable

  // Status badge: colour + shape + text for accessibility (not colour alone)
  const statusBadge = channel.running
    ? problems > 0
      ? { dotCls: 'bg-amber-400 status-live', badgeCls: 'bg-amber-950/60 text-amber-300 border border-amber-800/50', label: '⚠ Errors', shape: 'triangle' as const }
      : { dotCls: 'bg-emerald-400 status-live', badgeCls: 'bg-emerald-950/60 text-emerald-300 border border-emerald-800/50', label: '● Running', shape: 'circle' as const }
    : { dotCls: 'bg-slate-600', badgeCls: 'bg-slate-800/60 text-slate-400 border border-slate-700/50', label: '■ Stopped', shape: 'square' as const }

  return (
    <div className="card p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2.5">
            <span className={`size-2.5 rounded-full ${statusBadge.dotCls}`} aria-hidden="true" />
            <h3 className="truncate font-semibold text-slate-100">{channel.name}</h3>
            {/* Accessible status indicator: shape + colour + text */}
            <span className={`badge text-[10px] ${statusBadge.badgeCls}`} aria-label={`Status: ${statusBadge.label}`}>
              {statusBadge.label}
            </span>
          </div>
          <p className="mt-1 text-xs text-slate-500">
            {channel.running
              ? channel.startedAt
                ? `running since ${new Date(channel.startedAt).toLocaleTimeString()}`
                : 'running'
              : 'stopped'}
          </p>
        </div>

        <div className="flex shrink-0 items-center gap-3">
          <Sparkline values={spark} colour={problems > 0 ? '#fbbf24' : '#34d399'} />
          {/* The channel name belongs in the accessible name, not only in the heading above.
              
              With five channels listed, this was five buttons announced as "Stop, Stop, Stop, Start, Start"
              with nothing to say which channel each one belonged to. Anybody not reading the visual layout
              had no way to tell them apart, on the page whose whole purpose is stopping the thing that is
              misbehaving. The visible label stays short. */}
          {canControl &&
            (channel.running ? (
              <button
                className="btn-ghost py-1 text-xs"
                onClick={onStop}
                disabled={busy}
                aria-label={`Stop ${channel.name}`}
              >
                {busy ? '…' : 'Stop'}
              </button>
            ) : (
              <button
                className="btn-primary py-1 text-xs"
                onClick={onStart}
                disabled={busy}
                aria-label={`Start ${channel.name}`}
              >
                {busy ? '…' : 'Start'}
              </button>
            ))}
        </div>
      </div>

      {channel.error && (
        <p className="mt-3 rounded-md border border-rose-900/60 bg-rose-950/40 p-2 text-xs text-rose-300">
          {channel.error}
        </p>
      )}

      {channel.running && (
        <>
          <dl className="mt-4 grid grid-cols-4 gap-2 text-center">
            <Counter label="received" value={channel.received} />
            <Counter label="delivered" value={channel.delivered} tone="good" />
            <Counter label="filtered" value={channel.filtered} />
            <Counter
              label="problems"
              value={problems}
              tone={problems > 0 ? 'bad' : 'muted'}
            />
          </dl>

          {channel.destinations && channel.destinations.length > 0 && (
            <ul className="mt-3 space-y-1 border-t border-slate-800 pt-3 text-xs">
              {channel.destinations.map((d) => (
                <li key={d.name} className="flex items-center gap-2">
                  <span className="truncate text-slate-400">{d.name}</span>
                  <span className="ml-auto font-mono text-emerald-300">{d.delivered}</span>
                  {d.failed > 0 && <span className="font-mono text-rose-300">/{d.failed}</span>}
                  {d.filtered > 0 && <span className="font-mono text-slate-400">/{d.filtered}</span>}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  )
}

function Counter({
  label,
  value,
  tone = 'neutral',
}: {
  label: string
  value: number
  tone?: 'neutral' | 'good' | 'bad' | 'muted'
}) {
  const tones: Record<string, string> = {
    neutral: 'text-slate-200',
    good: 'text-emerald-300',
    bad: 'text-rose-300',
    muted: 'text-slate-400',
  }
  return (
    <div>
      <dd className={`font-mono text-lg tabular-nums ${tones[tone]}`}>{value.toLocaleString()}</dd>
      <dt className="text-[10px] tracking-wide text-slate-500 uppercase">{label}</dt>
    </div>
  )
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`
}


/**
 * OperationalSummary is the second row of the dashboard.
 */
interface OperationalSummary {
  queueDepth: number
  queueOldest: number
  alertsFiring: number
  alertsCritical: number
  shadows: number
  shadowDiffering: number
  certsTotal: number
  certsExpiring: number
  certsExpired: number
  soonestExpiry: number | null
}

const emptyOps: OperationalSummary = {
  queueDepth: 0,
  queueOldest: 0,
  alertsFiring: 0,
  alertsCritical: 0,
  shadows: 0,
  shadowDiffering: 0,
  certsTotal: 0,
  certsExpiring: 0,
  certsExpired: 0,
  soonestExpiry: null,
}

function useOperationalSummary(live: boolean): OperationalSummary {
  const [ops, setOps] = useState<OperationalSummary>(emptyOps)

  useEffect(() => {
    let active = true

    const load = async () => {
      const next = { ...emptyOps }

      const [queue, alerts, shadows, certs] = await Promise.allSettled([
        api.queue({ limit: 1 }),
        api.alerts(),
        api.shadows(),
        api.certificates(),
      ])

      if (queue.status === 'fulfilled') {
        for (const d of queue.value.destinations) {
          next.queueDepth += d.pending
          next.queueOldest = Math.max(next.queueOldest, d.oldestSeconds)
        }
      }
      if (alerts.status === 'fulfilled') {
        next.alertsFiring = alerts.value.firing.length
        next.alertsCritical = alerts.value.critical
      }
      if (shadows.status === 'fulfilled') {
        next.shadows = shadows.value.length
        next.shadowDiffering = shadows.value.filter((s) => s.differed > 0).length
      }
      if (certs.status === 'fulfilled') {
        const snap = certs.value
        next.certsExpiring = snap.expiring
        next.certsExpired = snap.expired
        next.certsTotal = snap.endpoints.length
        for (const e of snap.endpoints) {
          for (const c of e.tls.certificates ?? []) {
            if (next.soonestExpiry === null || c.daysRemaining < next.soonestExpiry) {
              next.soonestExpiry = c.daysRemaining
            }
          }
        }
      }

      if (active) setOps(next)
    }

    load()
    if (!live) return () => { active = false }

    const t = setInterval(load, 10000)
    return () => {
      active = false
      clearInterval(t)
    }
  }, [live])

  return ops
}

/** formatAge turns seconds into something readable at a glance. */
function formatAge(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)}s`
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  if (seconds < 86400) return `${(seconds / 3600).toFixed(1)}h`
  return `${Math.round(seconds / 86400)}d`
}
