import { SiteLogo } from './Branding'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from './api'
import type { MetricsSnapshot } from './api'
import {
  Gauge,
  PercentileChart,
  Sparkbars,
  TimeChart,
  formatValue,
  palette,
  seriesLabel,
} from './metricCharts'
import type { Series } from './metricCharts'
import { ErrorBox, Section, Spinner } from './ui'

/**
 * The metrics section.
 *
 * Laid out around the questions somebody actually arrives with, in the order they
 * arrive: is anything wrong now, is throughput normal, is anything slow, and what
 * is the process itself doing. A grid of every available metric would be more
 * complete and much less useful.
 */

const WINDOWS = [
  { id: '15m', label: '15 min' },
  { id: '1h', label: '1 hour' },
  { id: '6h', label: '6 hours' },
] as const

type WindowId = (typeof WINDOWS)[number]['id']

export function Metrics() {
  const [snapshot, setSnapshot] = useState<MetricsSnapshot | null>(null)
  const [window, setWindow] = useState<WindowId>('1h')
  const [error, setError] = useState<string | null>(null)
  const [live, setLive] = useState(true)
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    try {
      const res = await api.metrics(window)
      setSnapshot(res)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not load metrics')
    } finally {
      setLoading(false)
    }
  }, [window])

  useEffect(() => {
    setLoading(true)
    load()
  }, [load])

  useEffect(() => {
    if (!live) return
    // Ten seconds, matching the collector's resolution. Polling faster would
    // redraw the same points and just cost battery.
    const timer = setInterval(load, 10000)
    return () => clearInterval(timer)
  }, [live, load])

  const byName = useMemo(() => {
    const map = new Map<string, Series[]>()
    snapshot?.metrics.forEach((s) => {
      const list = map.get(s.name) ?? []
      list.push(s as Series)
      map.set(s.name, list)
    })
    return map
  }, [snapshot])

  const get = (name: string): Series[] => byName.get(name) ?? []
  const single = (name: string): Series | undefined => get(name)[0]

  const total = (name: string) =>
    get(name).reduce((sum, s) => sum + s.points.reduce((n, p) => n + p.value, 0), 0)

  const failed = total('perfuse_messages_failed_total')
  const unparseable = total('perfuse_messages_unparseable_total')
  const received = total('perfuse_messages_received_total')
  const delivered = total('perfuse_messages_delivered_total')

  const errorRate = received > 0 ? (failed + unparseable) / received : 0

  const deliveryLatency = get('perfuse_delivery_duration_seconds')
  const slowest = deliveryLatency.slice().sort((a, b) => b.latest - a.latest)[0]

  if (loading && !snapshot) {
    return <Spinner label="Loading metrics…" />
  }

  return (
    <div className="space-y-5">
      {/* Reporting screens get the site's own logo: these numbers get screenshotted into board packs and shown to customers. */}
      <SiteLogo size={40} />

      {/* Ambient background glow for metrics */}
      <div className="pointer-events-none fixed inset-0 -z-10 overflow-hidden" aria-hidden="true">
        <div
          className="ambient-glow absolute -top-24 right-1/4 h-72 w-72 rounded-full opacity-[0.03]"
          style={{ background: `radial-gradient(circle, var(--brand-accent), transparent 70%)`, animationDelay: '-4s' }}
        />
      </div>

      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-lg font-semibold text-slate-100">Metrics</h1>
          <p className="mt-1 max-w-2xl text-sm text-slate-500">
            {snapshot
              ? `Sampled every ${Math.round(snapshot.resolutionSeconds)}s, kept for ${Math.round(
                  snapshot.windowSeconds / 3600,
                )} hours. Also exposed at `
              : 'Also exposed at '}
            <code className="rounded bg-slate-800 px-1 py-0.5 text-xs">/metrics</code> in Prometheus
            format.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <div className="flex rounded-lg border border-slate-800 bg-slate-900/50 p-0.5">
            {WINDOWS.map((w) => (
              <button
                key={w.id}
                onClick={() => setWindow(w.id)}
                className={`rounded-md px-2.5 py-1 text-xs font-medium transition ${
                  window === w.id
                    ? 'bg-slate-700/80 text-slate-100 shadow-sm'
                    : 'text-slate-500 hover:text-slate-300'
                }`}
              >
                {w.label}
              </button>
            ))}
          </div>
          <button className="btn-ghost py-1.5 text-xs" onClick={() => setLive((v) => !v)}>
            <span
              className={`size-2 rounded-full ${live ? 'animate-pulse bg-emerald-400' : 'bg-slate-600'}`}
            />
            {live ? 'Live' : 'Paused'}
          </button>
        </div>
      </div>

      {error && (
        <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />
      )}

      {snapshot?.dropped && Object.keys(snapshot.dropped).length > 0 && (
        <div className="rounded-lg border border-amber-900/60 bg-amber-950/30 p-3 text-sm text-amber-200">
          <p className="font-medium">Some metrics hit the label limit.</p>
          <p className="mt-1 text-xs text-amber-300/80">
            Values past the cap are collapsed into an "other" bucket, so these series are
            incomplete. It usually means a label is taking its value from message content rather
            than configuration.
          </p>
          <ul className="mt-2 space-y-0.5 font-mono text-xs">
            {Object.entries(snapshot.dropped).map(([name, count]) => (
              <li key={name}>
                {name} — {count.toLocaleString()} collapsed
              </li>
            ))}
          </ul>
        </div>
      )}

      {/* Is anything wrong now — gauges with gradient icon badges */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <MetricGaugeCard
          label="Error rate"
          value={errorRate}
          unit="ratio"
          tone={errorRate > 0.01 ? 'bad' : errorRate > 0 ? 'warn' : 'good'}
          icon="error"
        >
          <Gauge
            label="Error rate"
            value={errorRate}
            unit="ratio"
            warnAbove={0.01}
            help="Failed and unparseable messages as a share of everything received."
          />
        </MetricGaugeCard>
        <MetricGaugeCard
          label="Channels running"
          value={single('perfuse_channels_running')?.latest ?? 0}
          unit="count"
          tone="good"
          icon="channels"
        >
          <Gauge
            label="Channels running"
            value={single('perfuse_channels_running')?.latest ?? 0}
            unit="count"
            max={single('perfuse_channels_total')?.latest || undefined}
          />
        </MetricGaugeCard>
        <MetricGaugeCard
          label="Slowest destination"
          value={slowest?.latest ?? 0}
          unit="seconds"
          tone={(slowest?.latest ?? 0) > 2 ? 'warn' : 'good'}
          icon="latency"
        >
          <Gauge
            label="Slowest destination"
            value={slowest?.latest ?? 0}
            unit="seconds"
            warnAbove={2}
            help={
              slowest
                ? `p95 delivery time for ${seriesLabel(slowest)}`
                : 'No deliveries recorded yet.'
            }
          />
        </MetricGaugeCard>
        <MetricGaugeCard
          label="Messages in window"
          value={received}
          unit="count"
          tone="neutral"
          icon="throughput"
        >
          <Gauge
            label="Messages in window"
            value={received}
            unit="count"
            help={`${delivered.toLocaleString()} delivered`}
          />
        </MetricGaugeCard>
      </div>

      {/* Is throughput normal. */}
      <Section
        title="Throughput by channel"
        description="Messages received per sample interval. Stacked, so total load and each channel's share read at once."
      >
        <TimeChart series={get('perfuse_messages_received_total')} mode="stacked" height={210} />
      </Section>

      <div className="grid gap-4 xl:grid-cols-2">
        <Section
          title="Outcomes"
          description="Failures are drawn on their own axis rather than buried under successes."
        >
          <TimeChart
            series={[
              ...get('perfuse_messages_delivered_total'),
              ...get('perfuse_messages_filtered_total'),
            ]}
            mode="stacked"
            height={170}
          />
        </Section>

        <Section
          title="Failures"
          description="Anything here is worth looking at, which is why it is not stacked with the successes."
        >
          {failed + unparseable === 0 ? (
            <div className="flex h-[170px] items-center justify-center text-sm text-emerald-300/70">
              Nothing failed in this window.
            </div>
          ) : (
            <TimeChart
              series={[
                ...get('perfuse_messages_failed_total'),
                ...get('perfuse_messages_unparseable_total'),
                ...get('perfuse_transform_errors_total'),
                ...get('perfuse_script_errors_total'),
              ]}
              height={170}
            />
          )}
        </Section>
      </div>

      {/* Is anything slow. */}
      <Section
        title="Delivery time distribution"
        description="Percentiles rather than an average. An average delivery time hides the slow tail, which is the only part that breaches a turnaround commitment."
      >
        {deliveryLatency.length === 0 ? (
          <p className="text-sm text-slate-500">No deliveries recorded yet.</p>
        ) : (
          <div className="space-y-5">
            {deliveryLatency.slice(0, 4).map((s, i) => (
              <div key={seriesLabel(s) + i}>
                <div className="mb-1 flex items-baseline justify-between">
                  <p className="text-sm font-medium text-slate-300">{seriesLabel(s)}</p>
                  <p className="font-mono text-xs text-slate-500">
                    p95 {formatValue(s.latest, 'seconds')}
                  </p>
                </div>
                <PercentileChart series={s} height={150} />
              </div>
            ))}
          </div>
        )}
      </Section>

      <Section
        title="Time to handle a message"
        description="End to end, from arrival to acknowledgement, including transformation and every destination."
      >
        {get('perfuse_message_duration_seconds').length === 0 ? (
          <p className="text-sm text-slate-500">No messages handled yet.</p>
        ) : (
          <PercentileChart series={get('perfuse_message_duration_seconds')[0]!} height={170} />
        )}
      </Section>

      <MetricTable
        title="Destinations"
        description="Average attempts matters: a destination that only succeeds on the fourth try looks healthy in a success count."
        rows={buildDestinationRows(get)}
      />

      {(get('perfuse_script_duration_seconds').length > 0 ||
        get('perfuse_transform_steps_applied_total').length > 0) && (
        <div className="grid gap-4 xl:grid-cols-2">
          <Section
            title="Script time"
            description="Time spent in JavaScript per message. A rising line here is the usual cause of a channel falling behind."
          >
            {get('perfuse_script_duration_seconds').length === 0 ? (
              <p className="text-sm text-slate-500">No channel is running a script.</p>
            ) : (
              <PercentileChart series={get('perfuse_script_duration_seconds')[0]!} height={160} />
            )}
          </Section>

          <Section title="Transformation steps applied">
            <TimeChart series={get('perfuse_transform_steps_applied_total')} height={160} />
          </Section>
        </div>
      )}

      {/* What is the process doing. */}
      <Section title="Process">
        <div className="grid gap-4 sm:grid-cols-3">
          <Gauge
            label="Goroutines"
            value={single('perfuse_goroutines')?.latest ?? 0}
            unit="count"
            warnAbove={5000}
            help="A number that only ever climbs is a leak."
          />
          <Gauge
            label="Heap in use"
            value={single('perfuse_heap_bytes')?.latest ?? 0}
            unit="bytes"
          />
          <Gauge
            label="Uptime"
            value={single('perfuse_uptime_seconds')?.latest ?? 0}
            unit="seconds"
          />
        </div>
        <div className="mt-5 grid gap-4 lg:grid-cols-2">
          <div>
            <p className="label mb-2">Goroutines</p>
            <TimeChart series={get('perfuse_goroutines')} height={130} showLegend={false} />
          </div>
          <div>
            <p className="label mb-2">Heap</p>
            <TimeChart series={get('perfuse_heap_bytes')} height={130} showLegend={false} />
          </div>
        </div>
      </Section>

      <details className="card p-4">
        <summary className="cursor-pointer text-sm font-medium text-slate-300">
          Everything being collected ({snapshot?.metrics.length ?? 0} series)
        </summary>
        <p className="mt-2 mb-3 text-xs text-slate-400">
          The same values are available at <code>/metrics</code> for Prometheus, which is the better
          option for alerting and for keeping more than {Math.round((snapshot?.windowSeconds ?? 0) / 3600)}{' '}
          hours of history.
        </p>
        <table className="w-full text-xs">
          <thead className="text-left text-slate-500">
            <tr>
              <th className="pb-2">Metric</th>
              <th className="pb-2">Labels</th>
              <th className="pb-2">Kind</th>
              <th className="pb-2 text-right">Latest</th>
              <th className="pb-2 pl-4">Recent</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-800/60">
            {snapshot?.metrics.map((s, i) => (
              <tr key={s.name + i}>
                <td className="py-1.5 font-mono text-slate-400" title={s.help}>
                  {s.name}
                </td>
                <td className="py-1.5 text-slate-500">{seriesLabel(s as Series)}</td>
                <td className="py-1.5 text-slate-400">{s.kind}</td>
                <td className="py-1.5 text-right font-mono text-slate-300">
                  {formatValue(s.latest, s.unit)}
                </td>
                <td className="py-1.5 pl-4">
                  <Sparkbars
                    points={s.points}
                    colour={palette[i % palette.length]}
                    width={90}
                    height={20}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  )
}

/* ──────────────────────────────────────────────────────────────────────────────
   MetricGaugeCard — wraps Gauge with a gradient icon badge
   ────────────────────────────────────────────────────────────────────────────── */

const metricToneConfig = {
  neutral: { iconBg: 'from-slate-600 to-slate-700', ring: 'ring-slate-700/50' },
  good: { iconBg: 'from-emerald-600 to-teal-700', ring: 'ring-emerald-800/40' },
  warn: { iconBg: 'from-amber-600 to-orange-700', ring: 'ring-amber-800/40' },
  bad: { iconBg: 'from-rose-600 to-pink-700', ring: 'ring-rose-800/40' },
} as const

function MetricGaugeCard({
  label: _label,
  value: _value,
  unit: _unit,
  tone = 'neutral',
  icon,
  children,
}: {
  label: string
  value: number
  unit: string
  tone?: 'neutral' | 'good' | 'warn' | 'bad'
  icon?: string
  children: React.ReactNode
}) {
  const cfg = metricToneConfig[tone]
  return (
    <div className="card relative overflow-hidden p-4">
      {/* Subtle top accent line */}
      <div
        className="absolute inset-x-0 top-0 h-px"
        style={{
          background: tone === 'good'
            ? 'linear-gradient(90deg, transparent, var(--color-emerald-500), transparent)'
            : tone === 'warn'
              ? 'linear-gradient(90deg, transparent, var(--color-amber-500), transparent)'
              : tone === 'bad'
                ? 'linear-gradient(90deg, transparent, var(--color-rose-500), transparent)'
                : `linear-gradient(90deg, transparent, var(--brand-accent), transparent)`,
          opacity: 0.5,
        }}
        aria-hidden="true"
      />
      {icon && (
        <div className={`mb-2 flex size-7 items-center justify-center rounded-md bg-gradient-to-br ${cfg.iconBg} ring-1 ${cfg.ring} shadow-sm`}>
          <MetricIcon name={icon} />
        </div>
      )}
      {children}
    </div>
  )
}

/** SVG icons for metric cards */
function MetricIcon({ name }: { name: string }) {
  const cls = "size-3.5 text-white/90"
  switch (name) {
    case 'error':
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
          <circle cx="8" cy="8" r="6" />
          <path d="M8 5v4M8 11v0.5" />
        </svg>
      )
    case 'channels':
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
          <path d="M2 4h12M2 8h12M2 12h12" />
        </svg>
      )
    case 'latency':
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
          <circle cx="8" cy="8" r="6" />
          <path d="M8 4v4l3 2" />
        </svg>
      )
    case 'throughput':
      return (
        <svg className={cls} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
          <path d="M2 12 5.5 7 8.5 9.5 14 4" />
          <path d="M10 4h4v4" />
        </svg>
      )
    default:
      return null
  }
}

type Row = {
  name: string
  delivered: number
  failed: number
  attempts: number
  p95: number
  points: Series['points']
}

function buildDestinationRows(get: (name: string) => Series[]): Row[] {
  const rows = new Map<string, Row>()

  const ensure = (name: string): Row => {
    const existing = rows.get(name)
    if (existing) return existing
    const created: Row = { name, delivered: 0, failed: 0, attempts: 0, p95: 0, points: [] }
    rows.set(name, created)
    return created
  }

  const sum = (s: Series) => s.points.reduce((n, p) => n + p.value, 0)

  get('perfuse_delivery_attempts_total').forEach((s) => {
    ensure(seriesLabel(s)).attempts = sum(s)
  })
  get('perfuse_delivery_failures_total').forEach((s) => {
    ensure(seriesLabel(s)).failed = sum(s)
  })
  get('perfuse_delivery_duration_seconds').forEach((s) => {
    const row = ensure(seriesLabel(s))
    row.p95 = s.latest
    row.points = s.points
    row.delivered = s.points.reduce((n, p) => n + p.value, 0)
  })

  return [...rows.values()].sort((a, b) => b.attempts - a.attempts)
}

function MetricTable({
  title,
  description,
  rows,
}: {
  title: string
  description?: string
  rows: Row[]
}) {
  return (
    <Section title={title} description={description}>
      {rows.length === 0 ? (
        <p className="text-sm text-slate-500">Nothing recorded yet.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs tracking-wide text-slate-500 uppercase">
              <tr>
                <th className="pb-2">Destination</th>
                <th className="pb-2 text-right">Deliveries</th>
                <th className="pb-2 text-right">Failures</th>
                <th className="pb-2 text-right">Avg attempts</th>
                <th className="pb-2 text-right">p95</th>
                <th className="pb-2 pl-4">Recent</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-800">
              {rows.map((row, i) => {
                const avgAttempts = row.delivered > 0 ? row.attempts / row.delivered : 0
                return (
                  <tr key={row.name}>
                    <td className="py-2 font-medium text-slate-200">{row.name}</td>
                    <td className="py-2 text-right font-mono text-slate-300">
                      {row.delivered.toLocaleString()}
                    </td>
                    <td
                      className={`py-2 text-right font-mono ${
                        row.failed > 0 ? 'text-rose-300' : 'text-slate-400'
                      }`}
                    >
                      {row.failed.toLocaleString()}
                    </td>
                    <td
                      className={`py-2 text-right font-mono ${
                        avgAttempts > 1.5 ? 'text-amber-300' : 'text-slate-400'
                      }`}
                    >
                      {avgAttempts > 0 ? avgAttempts.toFixed(2) : '—'}
                    </td>
                    <td
                      className={`py-2 text-right font-mono ${
                        row.p95 > 2 ? 'text-amber-300' : 'text-slate-400'
                      }`}
                    >
                      {formatValue(row.p95, 'seconds')}
                    </td>
                    <td className="py-2 pl-4">
                      <Sparkbars points={row.points} colour={palette[i % palette.length]} />
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  )
}
