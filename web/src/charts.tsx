import { useId, useMemo, useState } from 'react'

/**
 * Charts, drawn as SVG with no charting library.
 *
 * A dependency for this would be several hundred kilobytes to draw a line, and
 * the whole front end is 66 KB. More to the point, the shapes needed here are
 * simple, and hand-drawn SVG means the axis labels can say something useful
 * rather than whatever the library defaults to.
 */

export interface Point {
  /** x is a timestamp in milliseconds. */
  x: number
  y: number
}

export interface Series {
  name: string
  colour: string
  points: Point[]
}

function niceMax(value: number): number {
  if (value <= 0) return 1
  const magnitude = Math.pow(10, Math.floor(Math.log10(value)))
  const normalised = value / magnitude
  const step = normalised <= 1 ? 1 : normalised <= 2 ? 2 : normalised <= 5 ? 5 : 10
  return step * magnitude
}

function formatTime(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

/**
 * StackedAreaChart shows throughput over time, split by outcome.
 *
 * Stacked rather than overlaid on purpose: the useful question is "how much
 * traffic, and how much of it went wrong", and stacking answers both at once. The
 * failure series is drawn last so it sits on top and is impossible to miss.
 */
export function StackedAreaChart({
  series,
  height = 180,
  emptyLabel = 'No traffic in this window',
}: {
  series: Series[]
  height?: number
  emptyLabel?: string
}) {
  const gradientId = useId()
  const [hover, setHover] = useState<number | null>(null)

  const width = 720
  const padding = { top: 12, right: 12, bottom: 24, left: 40 }
  const plotW = width - padding.left - padding.right
  const plotH = height - padding.top - padding.bottom

  const model = useMemo(() => {
    const length = Math.max(0, ...series.map((s) => s.points.length))
    if (length === 0) return null

    const xs = series[0]?.points.map((p) => p.x) ?? []
    const totals = xs.map((_, i) => series.reduce((sum, s) => sum + (s.points[i]?.y ?? 0), 0))
    const max = niceMax(Math.max(1, ...totals))

    const xAt = (i: number) => (length === 1 ? plotW / 2 : (i / (length - 1)) * plotW)
    const yAt = (v: number) => plotH - (v / max) * plotH

    // Cumulative stacking, computed once so each band knows its own baseline.
    const bands = series.map((s, layer) => {
      const upper: number[] = []
      const lower: number[] = []
      xs.forEach((_, i) => {
        let below = 0
        for (let l = 0; l < layer; l++) below += series[l]?.points[i]?.y ?? 0
        lower.push(below)
        upper.push(below + (s.points[i]?.y ?? 0))
      })
      return { series: s, upper, lower }
    })

    return { length, xs, totals, max, xAt, yAt, bands }
  }, [series, plotW, plotH])

  if (!model) {
    return (
      <div
        className="flex items-center justify-center rounded-lg border border-dashed border-slate-800 text-sm text-slate-500"
        style={{ height }}
      >
        {emptyLabel}
      </div>
    )
  }

  const { length, xs, totals, max, xAt, yAt, bands } = model
  const ticks = [0, max / 2, max]

  return (
    <div className="relative">
      <svg
        viewBox={`0 0 ${width} ${height}`}
        className="w-full"
        style={{ height }}
        onMouseLeave={() => setHover(null)}
      >
        <defs>
          {bands.map((band, i) => (
            <linearGradient key={i} id={`${gradientId}-${i}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={band.series.colour} stopOpacity="0.55" />
              <stop offset="100%" stopColor={band.series.colour} stopOpacity="0.12" />
            </linearGradient>
          ))}
        </defs>

        <g transform={`translate(${padding.left},${padding.top})`}>
          {ticks.map((tick, i) => (
            <g key={i}>
              <line
                x1={0}
                x2={plotW}
                y1={yAt(tick)}
                y2={yAt(tick)}
                stroke="currentColor"
                className="text-slate-800"
                strokeDasharray={i === 0 ? undefined : '3 3'}
              />
              <text
                x={-8}
                y={yAt(tick) + 4}
                textAnchor="end"
                className="fill-slate-500 text-[10px]"
              >
                {Math.round(tick)}
              </text>
            </g>
          ))}

          {bands.map((band, i) => {
            const top = band.upper.map((v, idx) => `${xAt(idx)},${yAt(v)}`).join(' ')
            const bottom = band.lower
              .map((v, idx) => `${xAt(idx)},${yAt(v)}`)
              .reverse()
              .join(' ')
            return (
              <g key={i}>
                <polygon points={`${top} ${bottom}`} fill={`url(#${gradientId}-${i})`} />
                <polyline
                  points={top}
                  fill="none"
                  stroke={band.series.colour}
                  strokeWidth="1.5"
                  strokeLinejoin="round"
                />
              </g>
            )
          })}

          {hover !== null && (
            <line
              x1={xAt(hover)}
              x2={xAt(hover)}
              y1={0}
              y2={plotH}
              stroke="currentColor"
              className="text-slate-500"
              strokeDasharray="2 2"
            />
          )}

          {/* An invisible hit area per bucket, so hovering works without needing
              to land on a one-pixel line. */}
          {xs.map((_, i) => (
            <rect
              key={i}
              x={xAt(i) - plotW / Math.max(1, length) / 2}
              y={0}
              width={plotW / Math.max(1, length)}
              height={plotH}
              fill="transparent"
              onMouseEnter={() => setHover(i)}
            />
          ))}

          {[0, Math.floor((length - 1) / 2), length - 1]
            .filter((i, idx, arr) => i >= 0 && arr.indexOf(i) === idx)
            .map((i) => (
              <text
                key={i}
                x={xAt(i)}
                y={plotH + 16}
                textAnchor={i === 0 ? 'start' : i === length - 1 ? 'end' : 'middle'}
                className="fill-slate-500 text-[10px]"
              >
                {formatTime(xs[i] ?? 0)}
              </text>
            ))}
        </g>
      </svg>

      {hover !== null && (
        <div className="pointer-events-none absolute top-2 right-2 rounded-lg border border-slate-700 bg-slate-950/95 p-2.5 text-xs shadow-xl">
          <p className="mb-1 font-medium text-slate-300">{formatTime(xs[hover] ?? 0)}</p>
          {series.map((s) => (
            <p key={s.name} className="flex items-center gap-2 text-slate-400">
              <span className="size-2 rounded-sm" style={{ background: s.colour }} />
              {s.name}
              <span className="ml-auto font-mono text-slate-200">{s.points[hover]?.y ?? 0}</span>
            </p>
          ))}
          <p className="mt-1 border-t border-slate-800 pt-1 text-slate-300">
            total <span className="font-mono">{totals[hover] ?? 0}</span>
          </p>
        </div>
      )}
    </div>
  )
}

/** Sparkline is a compact line for a channel card. */
export function Sparkline({
  values,
  colour = '#38bdf8',
  height = 32,
  width = 120,
}: {
  values: number[]
  colour?: string
  height?: number
  width?: number
}) {
  if (values.length === 0) {
    return <div style={{ height, width }} className="rounded bg-slate-900/60" />
  }

  const max = Math.max(1, ...values)
  const step = values.length === 1 ? width : width / (values.length - 1)
  const points = values.map((v, i) => `${i * step},${height - (v / max) * height}`).join(' ')

  return (
    <svg viewBox={`0 0 ${width} ${height}`} style={{ height, width }} className="overflow-visible">
      <polyline
        points={points}
        fill="none"
        stroke={colour}
        strokeWidth="1.5"
        strokeLinejoin="round"
        strokeLinecap="round"
      />
      <polygon points={`0,${height} ${points} ${width},${height}`} fill={colour} opacity="0.12" />
    </svg>
  )
}

/**
 * Donut shows a breakdown by outcome.
 *
 * The centre carries the total, because a proportion without a magnitude is not
 * actionable: five percent failures means one thing at twenty messages and
 * something else entirely at twenty thousand.
 */
export function Donut({
  slices,
  size = 148,
  label,
}: {
  slices: { name: string; value: number; colour: string }[]
  size?: number
  label?: string
}) {
  const total = slices.reduce((sum, s) => sum + s.value, 0)
  const radius = size / 2
  const thickness = 18
  const inner = radius - thickness

  if (total === 0) {
    return (
      <div className="flex items-center gap-4">
        <div
          className="rounded-full border-[18px] border-slate-800"
          style={{ width: size, height: size }}
        />
        <p className="text-sm text-slate-500">Nothing recorded yet</p>
      </div>
    )
  }

  let angle = -Math.PI / 2
  const arcs = slices
    .filter((s) => s.value > 0)
    .map((s) => {
      const sweep = (s.value / total) * Math.PI * 2
      const start = angle
      const end = angle + sweep
      angle = end

      const x1 = radius + radius * Math.cos(start)
      const y1 = radius + radius * Math.sin(start)
      const x2 = radius + radius * Math.cos(end)
      const y2 = radius + radius * Math.sin(end)
      const xi2 = radius + inner * Math.cos(end)
      const yi2 = radius + inner * Math.sin(end)
      const xi1 = radius + inner * Math.cos(start)
      const yi1 = radius + inner * Math.sin(start)
      const large = sweep > Math.PI ? 1 : 0

      return {
        ...s,
        d: `M ${x1} ${y1} A ${radius} ${radius} 0 ${large} 1 ${x2} ${y2} L ${xi2} ${yi2} A ${inner} ${inner} 0 ${large} 0 ${xi1} ${yi1} Z`,
        percent: (s.value / total) * 100,
      }
    })

  return (
    <div className="flex flex-wrap items-center gap-5">
      <div className="relative" style={{ width: size, height: size }}>
        <svg viewBox={`0 0 ${size} ${size}`} width={size} height={size}>
          {arcs.map((a) => (
            <path key={a.name} d={a.d} fill={a.colour}>
              <title>{`${a.name}: ${a.value} (${a.percent.toFixed(1)}%)`}</title>
            </path>
          ))}
        </svg>
        <div className="absolute inset-0 flex flex-col items-center justify-center">
          <span className="text-xl font-semibold text-slate-100">{total.toLocaleString()}</span>
          {label && <span className="text-[10px] tracking-wide text-slate-500 uppercase">{label}</span>}
        </div>
      </div>

      <ul className="space-y-1.5 text-sm">
        {arcs.map((a) => (
          <li key={a.name} className="flex items-center gap-2.5">
            <span className="size-2.5 rounded-sm" style={{ background: a.colour }} />
            <span className="text-slate-400">{a.name}</span>
            <span className="ml-auto pl-4 font-mono text-slate-200">{a.value.toLocaleString()}</span>
            <span className="w-12 text-right font-mono text-xs text-slate-500">
              {a.percent.toFixed(0)}%
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/** HBar is a horizontal bar list, used for per-destination and per-type counts. */
export function HBar({
  rows,
  emptyLabel = 'Nothing yet',
}: {
  rows: { name: string; value: number; colour?: string; note?: string }[]
  emptyLabel?: string
}) {
  if (rows.length === 0) {
    return <p className="text-sm text-slate-500">{emptyLabel}</p>
  }

  const max = Math.max(1, ...rows.map((r) => r.value))

  return (
    <ul className="space-y-2.5">
      {rows.map((r) => (
        <li key={r.name}>
          <div className="mb-1 flex items-baseline justify-between gap-3 text-sm">
            <span className="truncate text-slate-300">{r.name}</span>
            <span className="shrink-0 font-mono text-slate-400">
              {r.value.toLocaleString()}
              {r.note && <span className="ml-2 text-xs text-slate-400">{r.note}</span>}
            </span>
          </div>
          <div className="h-1.5 overflow-hidden rounded-full bg-slate-800">
            <div
              className="h-full rounded-full transition-all"
              style={{ width: `${(r.value / max) * 100}%`, background: r.colour ?? '#38bdf8' }}
            />
          </div>
        </li>
      ))}
    </ul>
  )
}

/** Colours used across the charts, so a series means the same thing everywhere. */
export const outcomeColours: Record<string, string> = {
  delivered: '#34d399',
  filtered: '#64748b',
  partial: '#fbbf24',
  failed: '#f87171',
  unparseable: '#c084fc',
  pending: '#38bdf8',
}
