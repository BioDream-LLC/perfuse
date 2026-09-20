import { useId, useMemo, useState } from 'react'

/**
 * Chart primitives for the metrics section.
 *
 * These are hand-drawn SVG rather than a charting library, for the same reason as
 * the rest: a library to draw a line would be several hundred kilobytes, and the
 * whole front end including every chart is under a hundred gzipped.
 *
 * They are separate from charts.tsx because these are the ones that assume time on
 * the x axis and know about percentiles, which the simpler ones do not.
 */

export type Point = { at: string; value: number; rate?: number; p50?: number; p95?: number; p99?: number }

export type Series = {
  name: string
  kind: 'counter' | 'gauge' | 'histogram'
  unit: 'count' | 'bytes' | 'seconds' | 'ratio'
  help?: string
  labels?: Record<string, string>
  points: Point[]
  latest: number
  ratePerSecond: number
}

/** The palette. Ordered so adjacent series stay distinguishable, including for the
 *  most common colour vision deficiencies - which is why it is not a rainbow. */
export const palette = [
  '#38bdf8',
  '#a78bfa',
  '#34d399',
  '#fbbf24',
  '#fb7185',
  '#22d3ee',
  '#f472b6',
  '#4ade80',
  '#facc15',
  '#94a3b8',
]

export function seriesLabel(s: Series): string {
  const labels = s.labels ?? {}
  const parts = Object.entries(labels)
    .filter(([, v]) => v)
    .map(([, v]) => v)
  return parts.length > 0 ? parts.join(' · ') : s.name
}

/** formatValue renders a number according to its unit, because a byte count and a
 *  latency should not both come out as a bare number. */
export function formatValue(value: number, unit: Series['unit']): string {
  if (!isFinite(value)) return '—'

  switch (unit) {
    case 'bytes':
      if (value < 1024) return `${Math.round(value)} B`
      if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KB`
      if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`
      return `${(value / 1024 ** 3).toFixed(2)} GB`

    case 'seconds':
      // Sub-millisecond timings are common for a local file write, and rendering
      // them as "0.00s" would make the chart look broken.
      if (value < 0.001) return `${(value * 1e6).toFixed(0)}µs`
      if (value < 1) return `${(value * 1000).toFixed(value < 0.01 ? 1 : 0)}ms`
      if (value < 60) return `${value.toFixed(2)}s`
      return `${Math.floor(value / 60)}m ${Math.round(value % 60)}s`

    case 'ratio':
      return `${(value * 100).toFixed(1)}%`

    default:
      if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
      if (value >= 10_000) return `${(value / 1000).toFixed(1)}k`
      return Number.isInteger(value) ? value.toLocaleString() : value.toFixed(2)
  }
}

function niceCeiling(value: number): number {
  if (value <= 0) return 1
  const magnitude = 10 ** Math.floor(Math.log10(value))
  const scaled = value / magnitude
  const step = scaled <= 1 ? 1 : scaled <= 2 ? 2 : scaled <= 5 ? 5 : 10
  return step * magnitude
}

type Geometry = { width: number; height: number; padLeft: number; padBottom: number; padTop: number }

const geometry: Geometry = { width: 760, height: 200, padLeft: 52, padBottom: 22, padTop: 10 }

/**
 * TimeChart draws one or more series against time, as lines or stacked areas.
 *
 * It includes a crosshair with a readout, because a chart of an interface engine
 * is nearly always being read to answer "what was the value at 09:40", and
 * eyeballing a pixel position against an axis is guesswork.
 */
export function TimeChart({
  series,
  mode = 'line',
  height = geometry.height,
  field = 'value',
  showLegend = true,
  emptyMessage = 'No data in this window.',
}: {
  series: Series[]
  mode?: 'line' | 'stacked'
  height?: number
  field?: 'value' | 'rate' | 'p50' | 'p95' | 'p99'
  showLegend?: boolean
  emptyMessage?: string
}) {
  const clipId = useId()
  const [hover, setHover] = useState<number | null>(null)

  const g = { ...geometry, height }
  const plotWidth = g.width - g.padLeft - 8
  const plotHeight = g.height - g.padTop - g.padBottom

  const read = (p: Point): number => {
    const v = field === 'value' ? p.value : (p[field] ?? 0)
    return isFinite(v) ? v : 0
  }

  const model = useMemo(() => {
    const withPoints = series.filter((s) => s.points.length > 0)
    if (withPoints.length === 0) return null

    // A shared time axis across every series, so two lines drawn together line up.
    const times = new Set<number>()
    withPoints.forEach((s) => s.points.forEach((p) => times.add(Date.parse(p.at))))
    const axis = [...times].sort((a, b) => a - b)
    if (axis.length === 0) return null

    const first = axis[0]!
    const last = axis[axis.length - 1]!
    const span = Math.max(1, last - first)

    const byTime = withPoints.map((s) => {
      const map = new Map<number, number>()
      s.points.forEach((p) => map.set(Date.parse(p.at), read(p)))
      return map
    })

    // For stacked mode the maximum is the maximum of the sums, not of any one
    // series; using the latter clips the chart.
    let max = 0
    axis.forEach((t) => {
      if (mode === 'stacked') {
        max = Math.max(max, byTime.reduce((sum, m) => sum + (m.get(t) ?? 0), 0))
      } else {
        byTime.forEach((m) => {
          max = Math.max(max, m.get(t) ?? 0)
        })
      }
    })
    const ceiling = niceCeiling(max)

    const x = (t: number) => g.padLeft + ((t - first) / span) * plotWidth
    const y = (v: number) => g.padTop + plotHeight - (v / ceiling) * plotHeight

    return { axis, byTime, withPoints, first, last, ceiling, x, y }
  }, [series, mode, field, height])

  if (!model) {
    return (
      <div
        className="flex items-center justify-center text-sm text-slate-400"
        style={{ height }}
      >
        {emptyMessage}
      </div>
    )
  }

  const { axis, byTime, withPoints, ceiling, x, y } = model

  const gridLines = [0, 0.25, 0.5, 0.75, 1]
  const hoverTime = hover !== null ? (axis[hover] ?? null) : null

  // Stacked areas need cumulative baselines, computed once.
  const stackedBaselines = new Map<number, number>()

  return (
    <div className="relative">
      <svg
        viewBox={`0 0 ${g.width} ${g.height}`}
        className="w-full"
        style={{ height }}
        onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          const rect = e.currentTarget.getBoundingClientRect()
          const px = ((e.clientX - rect.left) / rect.width) * g.width
          // Snap to the nearest sample rather than interpolating, because the
          // readout should show a value that was actually recorded.
          let nearest = 0
          let best = Infinity
          axis.forEach((t, i) => {
            const d = Math.abs(x(t) - px)
            if (d < best) {
              best = d
              nearest = i
            }
          })
          setHover(nearest)
        }}
      >
        <defs>
          <clipPath id={clipId}>
            <rect x={g.padLeft} y={g.padTop} width={plotWidth} height={plotHeight} />
          </clipPath>
        </defs>

        {gridLines.map((fraction) => {
          const value = ceiling * (1 - fraction)
          const gy = g.padTop + plotHeight * fraction
          return (
            <g key={fraction}>
              <line
                x1={g.padLeft}
                x2={g.padLeft + plotWidth}
                y1={gy}
                y2={gy}
                stroke="#1e293b"
                strokeWidth={1}
              />
              <text x={g.padLeft - 6} y={gy + 3} textAnchor="end" fontSize={9} fill="#64748b">
                {formatValue(value, withPoints[0]!.unit)}
              </text>
            </g>
          )
        })}

        <g clipPath={`url(#${clipId})`}>
          {withPoints.map((s, index) => {
            const colour = palette[index % palette.length]!
            const map = byTime[index]!

            if (mode === 'stacked') {
              const top: string[] = []
              const bottom: string[] = []
              axis.forEach((t) => {
                const base = stackedBaselines.get(t) ?? 0
                const value = map.get(t) ?? 0
                top.push(`${x(t)},${y(base + value)}`)
                bottom.unshift(`${x(t)},${y(base)}`)
                stackedBaselines.set(t, base + value)
              })
              return (
                <polygon
                  key={s.name + index}
                  points={[...top, ...bottom].join(' ')}
                  fill={colour}
                  fillOpacity={0.5}
                  stroke={colour}
                  strokeWidth={1}
                />
              )
            }

            const path = axis.map((t) => `${x(t)},${y(map.get(t) ?? 0)}`).join(' ')
            return (
              <polyline
                key={s.name + index}
                points={path}
                fill="none"
                stroke={colour}
                strokeWidth={1.75}
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            )
          })}
        </g>

        <line
          x1={g.padLeft}
          x2={g.padLeft + plotWidth}
          y1={g.padTop + plotHeight}
          y2={g.padTop + plotHeight}
          stroke="#334155"
        />

        {[0, 0.5, 1].map((fraction) => {
          const t = axis[Math.min(axis.length - 1, Math.floor((axis.length - 1) * fraction))]!
          return (
            <text
              key={fraction}
              x={g.padLeft + plotWidth * fraction}
              y={g.height - 6}
              textAnchor={fraction === 0 ? 'start' : fraction === 1 ? 'end' : 'middle'}
              fontSize={9}
              fill="#64748b"
            >
              {new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
            </text>
          )
        })}

        {hoverTime !== null && (
          <line
            x1={x(hoverTime)}
            x2={x(hoverTime)}
            y1={g.padTop}
            y2={g.padTop + plotHeight}
            stroke="#64748b"
            strokeDasharray="2 2"
          />
        )}
      </svg>

      {hoverTime !== null && (
        <div className="pointer-events-none absolute top-0 right-0 rounded-md border border-slate-700 bg-slate-900/95 p-2 text-xs shadow-lg">
          <p className="mb-1 font-medium text-slate-300">
            {new Date(hoverTime).toLocaleTimeString()}
          </p>
          {withPoints.map((s, index) => (
            <p key={s.name + index} className="flex items-center gap-2 whitespace-nowrap">
              <span
                className="size-2 shrink-0 rounded-sm"
                style={{ background: palette[index % palette.length] }}
              />
              <span className="text-slate-500">{seriesLabel(s)}</span>
              <span className="ml-auto font-mono text-slate-200">
                {formatValue(byTime[index]!.get(hoverTime) ?? 0, s.unit)}
              </span>
            </p>
          ))}
        </div>
      )}

      {showLegend && withPoints.length > 1 && (
        <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-500">
          {withPoints.map((s, index) => (
            <span key={s.name + index} className="flex items-center gap-1.5">
              <span
                className="size-2 rounded-sm"
                style={{ background: palette[index % palette.length] }}
              />
              {seriesLabel(s)}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

/**
 * PercentileChart draws p50, p95 and p99 as bands.
 *
 * Bands rather than three lines, because the question being asked is how wide the
 * spread is: a p50 and p99 close together means consistent, far apart means a tail
 * worth investigating, and that comparison is easier to see as an area than as
 * lines to be mentally subtracted.
 */
export function PercentileChart({ series, height = 180 }: { series: Series; height?: number }) {
  const g = { ...geometry, height }
  const plotWidth = g.width - g.padLeft - 8
  const plotHeight = g.height - g.padTop - g.padBottom

  const points = series.points.filter((p) => (p.p99 ?? 0) > 0 || (p.p50 ?? 0) > 0)

  if (points.length < 2) {
    return (
      <div className="flex items-center justify-center text-sm text-slate-400" style={{ height }}>
        Not enough samples yet to show a distribution.
      </div>
    )
  }

  const first = Date.parse(points[0]!.at)
  const last = Date.parse(points[points.length - 1]!.at)
  const span = Math.max(1, last - first)
  const ceiling = niceCeiling(Math.max(...points.map((p) => p.p99 ?? 0)))

  const x = (t: number) => g.padLeft + ((t - first) / span) * plotWidth
  const y = (v: number) => g.padTop + plotHeight - (Math.min(v, ceiling) / ceiling) * plotHeight

  const band = (lower: (p: Point) => number, upper: (p: Point) => number) => {
    const top = points.map((p) => `${x(Date.parse(p.at))},${y(upper(p))}`)
    const bottom = points
      .slice()
      .reverse()
      .map((p) => `${x(Date.parse(p.at))},${y(lower(p))}`)
    return [...top, ...bottom].join(' ')
  }

  const line = (pick: (p: Point) => number) =>
    points.map((p) => `${x(Date.parse(p.at))},${y(pick(p))}`).join(' ')

  return (
    <div>
      <svg viewBox={`0 0 ${g.width} ${g.height}`} className="w-full" style={{ height }}>
        {[0, 0.5, 1].map((fraction) => {
          const gy = g.padTop + plotHeight * fraction
          return (
            <g key={fraction}>
              <line x1={g.padLeft} x2={g.padLeft + plotWidth} y1={gy} y2={gy} stroke="#1e293b" />
              <text x={g.padLeft - 6} y={gy + 3} textAnchor="end" fontSize={9} fill="#64748b">
                {formatValue(ceiling * (1 - fraction), 'seconds')}
              </text>
            </g>
          )
        })}

        <polygon
          points={band(
            (p) => p.p95 ?? 0,
            (p) => p.p99 ?? 0,
          )}
          fill="#fb7185"
          fillOpacity={0.25}
        />
        <polygon points={band((p) => p.p50 ?? 0, (p) => p.p95 ?? 0)} fill="#fbbf24" fillOpacity={0.25} />
        <polyline points={line((p) => p.p50 ?? 0)} fill="none" stroke="#34d399" strokeWidth={1.75} />
        <polyline points={line((p) => p.p95 ?? 0)} fill="none" stroke="#fbbf24" strokeWidth={1.25} />
        <polyline points={line((p) => p.p99 ?? 0)} fill="none" stroke="#fb7185" strokeWidth={1.25} />

        <line
          x1={g.padLeft}
          x2={g.padLeft + plotWidth}
          y1={g.padTop + plotHeight}
          y2={g.padTop + plotHeight}
          stroke="#334155"
        />
      </svg>

      <div className="mt-2 flex flex-wrap gap-4 text-xs text-slate-500">
        <span className="flex items-center gap-1.5">
          <span className="size-2 rounded-sm bg-emerald-400" /> p50 (typical)
        </span>
        <span className="flex items-center gap-1.5">
          <span className="size-2 rounded-sm bg-amber-400" /> p95
        </span>
        <span className="flex items-center gap-1.5">
          <span className="size-2 rounded-sm bg-rose-400" /> p99 (slowest one in a hundred)
        </span>
      </div>
    </div>
  )
}

/**
 * Gauge is a single number with a dial, for a current reading.
 *
 * The dial is only drawn when a maximum is meaningful. A goroutine count has no
 * natural ceiling, and inventing one produces a dial that is always nearly empty
 * and tells you nothing.
 */
export function Gauge({
  label,
  value,
  unit,
  max,
  warnAbove,
  help,
}: {
  label: string
  value: number
  unit: Series['unit']
  max?: number
  warnAbove?: number
  help?: string
}) {
  const fraction = max && max > 0 ? Math.min(1, value / max) : null
  const alarming = warnAbove !== undefined && value > warnAbove

  const radius = 34
  const circumference = Math.PI * radius // half circle

  return (
    <div className="card p-4" title={help}>
      <p className="label mb-2">{label}</p>

      {fraction !== null ? (
        <div className="flex items-center gap-3">
          <svg viewBox="0 0 80 46" className="h-11 w-20 shrink-0">
            <path
              d={`M 6 40 A ${radius} ${radius} 0 0 1 74 40`}
              fill="none"
              stroke="#1e293b"
              strokeWidth={7}
              strokeLinecap="round"
            />
            <path
              d={`M 6 40 A ${radius} ${radius} 0 0 1 74 40`}
              fill="none"
              stroke={alarming ? '#fb7185' : '#38bdf8'}
              strokeWidth={7}
              strokeLinecap="round"
              strokeDasharray={`${circumference * fraction} ${circumference}`}
            />
          </svg>
          <div>
            <p className={`text-xl font-semibold ${alarming ? 'text-rose-300' : 'text-slate-100'}`}>
              {formatValue(value, unit)}
            </p>
            {max !== undefined && <p className="text-xs text-slate-400">of {formatValue(max, unit)}</p>}
          </div>
        </div>
      ) : (
        <p className={`text-2xl font-semibold ${alarming ? 'text-rose-300' : 'text-slate-100'}`}>
          {formatValue(value, unit)}
        </p>
      )}
    </div>
  )
}

/**
 * Sparkbars is a compact per-interval bar chart, for a row in a table.
 *
 * Bars rather than a line, because these show counts per interval and a line
 * implies a continuous quantity that was interpolated between samples.
 */
export function Sparkbars({
  points,
  colour = '#38bdf8',
  width = 120,
  height = 28,
  field = 'value',
}: {
  points: Point[]
  colour?: string
  width?: number
  height?: number
  field?: 'value' | 'rate'
}) {
  if (points.length === 0) {
    return <div style={{ width, height }} />
  }

  const values = points.map((p) => (field === 'value' ? p.value : (p.rate ?? 0)))
  const max = Math.max(...values, 1)
  const barWidth = Math.max(1, width / points.length - 1)

  return (
    <svg viewBox={`0 0 ${width} ${height}`} style={{ width, height }} className="overflow-visible">
      {values.map((v, i) => {
        const barHeight = Math.max(v > 0 ? 1 : 0, (v / max) * height)
        return (
          <rect
            key={i}
            x={(i * width) / points.length}
            y={height - barHeight}
            width={barWidth}
            height={barHeight}
            fill={colour}
            opacity={0.85}
          />
        )
      })}
    </svg>
  )
}
