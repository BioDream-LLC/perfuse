import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from './api'
import type { Bucket, FlowNode, FlowResponse } from './api'
import { Section, ErrorBox } from './ui'
import { toUiError } from './store'
import type { UiError } from './store'

// The flow map: the shape of the site, and what was moving through it at any moment.
//
// The dashboard answers "is it working now". This answers "what was it doing at 03:40", which is the question that actually gets asked -
// somebody has a complaint from a ward and a time, and needs to know which strand was dark then. One feed stopping barely moves the site
// totals, because the feed that failed is rarely the busiest one, so it is invisible on every chart that aggregates.
//
// A strand that carried nothing is drawn as a dark line, not as no line. Absent draws as nothing, and nothing looks like a destination
// that was never configured - the opposite of the conclusion somebody needs to reach.

/** windows the scrubber can cover, with a bucket size that keeps the series readable. */
const windows: { label: string; window: string; bucket: string }[] = [
  { label: 'Last hour', window: '1h', bucket: '1m' },
  { label: 'Last 6 hours', window: '6h', bucket: '5m' },
  { label: 'Last 24 hours', window: '24h', bucket: '15m' },
  { label: 'Last 7 days', window: '168h', bucket: '1h' },
]

export default function FlowMap() {
  const [choice, setChoice] = useState(0)
  const [flow, setFlow] = useState<FlowResponse | null>(null)
  const [error, setError] = useState<UiError | null>(null)

  // at is the bucket index the scrubber is on. -1 means live: follow the newest bucket as it arrives.
  const [at, setAt] = useState(-1)
  const [playing, setPlaying] = useState(false)
  const [selected, setSelected] = useState<string | null>(null)

  const load = windows[choice]!

  useEffect(() => {
    let alive = true

    const fetchFlow = async () => {
      try {
        const next = await api.flow(load.window, load.bucket)
        if (alive) {
          setFlow(next)
          setError(null)
        }
      } catch (e) {
        if (alive) setError(toUiError(e))
      }
    }

    void fetchFlow()

    // Only poll while live. Polling while somebody is scrubbing would move the ground under them: the window slides forward, the
    // bucket they were looking at shifts index, and the strand they were studying is suddenly a different moment.
    const timer = at === -1 ? window.setInterval(() => void fetchFlow(), 10_000) : undefined

    return () => {
      alive = false
      if (timer) window.clearInterval(timer)
    }
  }, [load.window, load.bucket, at === -1])

  // The number of buckets, taken from whichever series is longest. They are all the same length by construction, but reading it from the
  // data rather than computing it from the window means the scrubber cannot disagree with what is drawn.
  const total = useMemo(() => {
    let most = 0
    for (const n of flow?.nodes ?? []) {
      most = Math.max(most, n.received.length)
      for (const d of n.destinations) most = Math.max(most, d.buckets.length)
    }

    return most
  }, [flow])

  const index = at === -1 ? Math.max(0, total - 1) : Math.min(at, Math.max(0, total - 1))

  // Playback walks the window and stops at the end rather than looping. Looping makes it impossible to tell the end of the window from
  // the beginning, and somebody watching for the moment a strand goes dark would see it twice and trust neither.
  const playTimer = useRef<number | undefined>(undefined)
  useEffect(() => {
    if (!playing || total === 0) return

    playTimer.current = window.setInterval(() => {
      setAt((current) => {
        const next = (current === -1 ? 0 : current) + 1
        if (next >= total) {
          setPlaying(false)

          return total - 1
        }

        return next
      })
    }, 220)

    return () => window.clearInterval(playTimer.current)
  }, [playing, total])

  const nodes = flow?.nodes ?? []
  const chosen = nodes.find((n) => n.channel === selected) ?? null

  return (
    <Section
      title="Flow map"
      description="Every feed, every destination, and what was moving through each of them. Drag the scrubber back to a moment and watch which strands were dark."
    >
      {error && <ErrorBox error={error} />}

      <div className="mb-4 flex flex-wrap items-center gap-3">
        {windows.map((w, i) => (
          <button
            key={w.window}
            type="button"
            onClick={() => {
              setChoice(i)
              setAt(-1)
              setPlaying(false)
            }}
            className={`rounded px-3 py-1.5 text-sm ${
              i === choice ? 'bg-sky-600 text-white' : 'bg-slate-800 text-slate-300 hover:bg-slate-700'
            }`}
          >
            {w.label}
          </button>
        ))}

        <div className="ml-auto flex items-center gap-2">
          <button
            type="button"
            onClick={() => setPlaying((p) => !p)}
            disabled={total === 0}
            className="rounded bg-slate-800 px-3 py-1.5 text-sm text-slate-200 hover:bg-slate-700 disabled:opacity-40"
          >
            {playing ? 'Pause' : 'Play the window'}
          </button>
          <button
            type="button"
            onClick={() => {
              setAt(-1)
              setPlaying(false)
            }}
            className={`rounded px-3 py-1.5 text-sm ${
              at === -1 ? 'bg-emerald-700 text-white' : 'bg-slate-800 text-slate-300 hover:bg-slate-700'
            }`}
          >
            {at === -1 ? 'Live' : 'Back to live'}
          </button>
        </div>
      </div>

      <Scrubber
        total={total}
        index={index}
        live={at === -1}
        at={bucketTimeOf(nodes, index)}
        onChange={(next) => {
          setPlaying(false)
          setAt(next)
        }}
      />

      {nodes.length === 0 && !error && (
        <p className="mt-6 text-sm text-slate-400">
          No channels are loaded, so there is nothing to draw. A flow map of an empty site is an empty map rather than a
          problem.
        </p>
      )}

      <div className="mt-6 space-y-3">
        {nodes.map((node) => (
          <ChannelStrands
            key={node.channel}
            node={node}
            index={index}
            selected={node.channel === selected}
            onSelect={() => setSelected(node.channel === selected ? null : node.channel)}
          />
        ))}
      </div>

      {chosen && <Explanation node={chosen} index={index} />}
    </Section>
  )
}

/** bucketTimeOf reads the time of a bucket index from whichever series has it. */
function bucketTimeOf(nodes: FlowNode[], index: number): string {
  for (const n of nodes) {
    const b = n.received[index]
    if (b) return b.start
    for (const d of n.destinations) {
      const db = d.buckets[index]
      if (db) return db.start
    }
  }

  return ''
}

/** Scrubber is the time control. */
function Scrubber({
  total,
  index,
  live,
  at,
  onChange,
}: {
  total: number
  index: number
  live: boolean
  at: string
  onChange: (next: number) => void
}) {
  return (
    <div className="rounded-lg border border-slate-800 bg-slate-900/60 p-4">
      <div className="mb-2 flex items-baseline justify-between">
        <label htmlFor="flow-scrubber" className="text-sm font-medium text-slate-300">
          Moment shown
        </label>
        <span className="font-mono text-sm text-slate-200">
          {at ? new Date(at).toLocaleString() : 'no data in this window'}
          {live && <span className="ml-2 rounded bg-emerald-900/60 px-2 py-0.5 text-xs text-emerald-300 live-badge">live</span>}
        </span>
      </div>

      <input
        id="flow-scrubber"
        type="range"
        min={0}
        max={Math.max(0, total - 1)}
        value={index}
        disabled={total === 0}
        onChange={(e) => onChange(Number(e.target.value))}
        className="w-full accent-sky-500"
        aria-label="the moment in the window to show"
      />

      <p className="mt-1 text-xs text-slate-500">
        {total > 0
          ? `${total} points across this window. Every strand covers all of them, so a quiet strand is a dark line rather than a missing one.`
          : 'No points in this window.'}
      </p>
    </div>
  )
}

/** ChannelStrands draws one channel and its destination strands at the chosen moment. */
function ChannelStrands({
  node,
  index,
  selected,
  onSelect,
}: {
  node: FlowNode
  index: number
  selected: boolean
  onSelect: () => void
}) {
  const arrived = node.received[index]?.total ?? 0
  const isActive = node.running && arrived > 0

  return (
    <div
      className={`rounded-lg border p-4 transition-all duration-300 ${
        selected ? 'border-sky-600 bg-slate-900' : isActive ? 'channel-active bg-slate-900/40' : 'border-slate-800 bg-slate-900/40'
      }`}
    >
      <button type="button" onClick={onSelect} className="mb-3 flex w-full items-center gap-3 text-left">
        <StatusDot node={node} arrived={arrived} />
        <span className="font-medium text-slate-100">{node.channel}</span>
        <span className="text-xs text-slate-400">
          {node.broken
            ? 'not loaded'
            : node.running
              ? `${arrived} in at this moment`
              : 'stopped'}
        </span>
        <span className="ml-auto text-xs text-sky-400">{selected ? 'hide details' : 'what does this do?'}</span>
      </button>

      {node.broken && node.reason && (
        <p className="mb-3 rounded border border-amber-700 bg-amber-950/40 p-2 text-xs text-amber-200">{node.reason}</p>
      )}

      <div className="space-y-2">
        {node.destinations.length === 0 && (
          <p className="text-xs text-slate-400">This channel has no destinations, so nothing leaves it.</p>
        )}
        {node.destinations.map((d) => (
          <StrandRow key={d.name} name={d.name} how={d.how} durable={d.durable} everUsed={d.everUsed} buckets={d.buckets} index={index} />
        ))}
      </div>
    </div>
  )
}

/** StatusDot shows whether this channel is alive, in one glance. */
function StatusDot({ node, arrived }: { node: FlowNode; arrived: number }) {
  let tone = 'bg-slate-600'
  let animate = ''
  if (node.broken) tone = 'bg-amber-500'
  else if (!node.running) tone = 'bg-slate-500'
  else if (arrived > 0) {
    tone = 'bg-emerald-400'
    animate = ' status-live'
  }

  return <span className={`h-2.5 w-2.5 shrink-0 rounded-full ${tone}${animate}`} aria-hidden="true" />
}

/** StrandRow draws one destination strand: the whole series, with the chosen moment marked. */
function StrandRow({
  name,
  how,
  durable,
  everUsed,
  buckets,
  index,
}: {
  name: string
  how: string
  durable: boolean
  everUsed: boolean
  buckets: Bucket[]
  index: number
}) {
  const now = buckets[index]
  const delivered = now?.delivered ?? 0
  const failed = now?.failed ?? 0
  const filtered = now?.filtered ?? 0
  const carrying = (now?.total ?? 0) > 0

  return (
    <div className="flex items-center gap-3">
      <div className="w-40 shrink-0">
        <div className="truncate text-sm text-slate-200" title={name}>
          {name}
        </div>
        <div className="truncate text-xs text-slate-500" title={how}>
          {how}
        </div>
      </div>

      <Sparkline buckets={buckets} index={index} />

      <div className="w-56 shrink-0 text-right text-xs">
        {!everUsed ? (
          // Never used at all is a different statement from quiet at this moment, and conflating them is how a destination that has
          // never worked since the day it was configured stays invisible: there is no traffic anywhere to draw the eye.
          <span className="text-amber-300">nothing has ever been sent here</span>
        ) : failed > 0 ? (
          <span className="text-rose-300">
            {failed} failed{durable ? ', queued to retry' : ', not queued so not retried'}
          </span>
        ) : delivered > 0 ? (
          <span className="text-emerald-300">
            {delivered} delivered{filtered > 0 && `, ${filtered} filtered`}
          </span>
        ) : carrying && filtered > 0 ? (
          // Filtered is not delivered, and must not wear the colour that means delivered.
          //
          // This read "0 delivered, 6 filtered" in green, which says the strand is fine when in fact nothing reached the far end.
          // Filtering is usually deliberate, so it is not an error either - it is its own state and gets its own neutral colour.
          <span className="text-slate-300">
            {filtered} filtered, none sent on
          </span>
        ) : (
          <span className="text-slate-500">dark at this moment</span>
        )}
      </div>
    </div>
  )
}

/** Sparkline draws the whole strand with the chosen moment marked, so the scrubber has context around it. */
function Sparkline({ buckets, index }: { buckets: Bucket[]; index: number }) {
  const width = 100
  const height = 26

  const peak = Math.max(1, ...buckets.map((b) => b.total))
  const step = buckets.length > 1 ? width / (buckets.length - 1) : width

  const line = buckets
    .map((b, i) => `${(i * step).toFixed(2)},${(height - (b.total / peak) * (height - 4) - 2).toFixed(2)}`)
    .join(' ')

  const failedLine = buckets
    .map((b, i) => `${(i * step).toFixed(2)},${(height - (b.failed / peak) * (height - 4) - 2).toFixed(2)}`)
    .join(' ')

  const anyFailed = buckets.some((b) => b.failed > 0)
  const hasTraffic = buckets.some((b) => b.total > 0)

  // Build the area fill path (line + close at bottom)
  const areaPath = hasTraffic
    ? `M ${buckets.map((b, i) => `${(i * step).toFixed(2)},${(height - (b.total / peak) * (height - 4) - 2).toFixed(2)}`).join(' L ')} L ${((buckets.length - 1) * step).toFixed(2)},${height} L 0,${height} Z`
    : ''

  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      className="h-7 flex-1 rounded bg-slate-950/60"
      role="img"
      aria-label={`${buckets.length} points, peak ${peak}`}
    >
      <defs>
        <linearGradient id="spark-fill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="#38bdf8" stopOpacity="0.3" />
          <stop offset="100%" stopColor="#38bdf8" stopOpacity="0" />
        </linearGradient>
        <linearGradient id="spark-fail-fill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="#fb7185" stopOpacity="0.2" />
          <stop offset="100%" stopColor="#fb7185" stopOpacity="0" />
        </linearGradient>
      </defs>

      {/* Area fill under the line for depth */}
      {hasTraffic && (
        <path d={areaPath} fill="url(#spark-fill)" />
      )}

      <polyline points={line} fill="none" stroke="#38bdf8" strokeWidth="1.2" vectorEffect="non-scaling-stroke">
        {hasTraffic && (
          <animate attributeName="stroke-opacity" values="0.8;1;0.8" dur="3s" repeatCount="indefinite" />
        )}
      </polyline>

      {anyFailed && (
        <polyline points={failedLine} fill="none" stroke="#fb7185" strokeWidth="1.4" vectorEffect="non-scaling-stroke" />
      )}

      <line
        x1={(index * step).toFixed(2)}
        y1={0}
        x2={(index * step).toFixed(2)}
        y2={height}
        stroke="#e2e8f0"
        strokeWidth="0.8"
        vectorEffect="non-scaling-stroke"
        opacity="0.7"
      />

      {/* Glowing dot at the current position */}
      {hasTraffic && buckets[index] && (
        <>
          <circle
            cx={(index * step).toFixed(2)}
            cy={(height - ((buckets[index]?.total ?? 0) / peak) * (height - 4) - 2).toFixed(2)}
            r="2.5"
            fill="#38bdf8"
            opacity="0.4"
          >
            <animate attributeName="r" values="2.5;4;2.5" dur="2s" repeatCount="indefinite" />
            <animate attributeName="opacity" values="0.4;0.8;0.4" dur="2s" repeatCount="indefinite" />
          </circle>
          <circle
            cx={(index * step).toFixed(2)}
            cy={(height - ((buckets[index]?.total ?? 0) / peak) * (height - 4) - 2).toFixed(2)}
            r="1.5"
            fill="#38bdf8"
          />
        </>
      )}
    </svg>
  )
}

/** Explanation shows what a channel does, in words, beside the strand that prompted the question. */
function Explanation({ node, index }: { node: FlowNode; index: number }) {
  const at = node.received[index]

  return (
    <div className="mt-6 rounded-lg border border-sky-900 bg-sky-950/30 p-4">
      <h3 className="mb-2 text-sm font-medium text-sky-200">What {node.channel} does</h3>

      {/* Narration comes from the same place as the interface specification, so this cannot describe a channel differently from the
          document somebody hands a vendor. Shown here because the two questions arrive together: looking at a dark strand, you need
          to know what it was meant to be doing before you can say whether dark is wrong. */}
      <ul className="space-y-1">
        {node.narration.map((sentence, i) => (
          <li key={i} className="text-sm text-slate-300">
            {sentence}
          </li>
        ))}
      </ul>

      {at && (
        <p className="mt-3 border-t border-sky-900 pt-3 text-xs text-slate-400">
          At {new Date(at.start).toLocaleString()} it received {at.total}
          {at.total > 0 && `, of which ${at.delivered} were delivered, ${at.filtered} filtered and ${at.failed} failed`}.
        </p>
      )}
    </div>
  )
}
