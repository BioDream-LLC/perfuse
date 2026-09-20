/**
 * Feed contracts.
 *
 * The page has to work when nothing is wrong. Somebody arrives here to answer "is this feed being watched, and
 * does it still look the way we think it does" — which is a question with a good answer most days, and a page
 * that only says something when it is unhappy cannot answer it at all.
 *
 * So the headline is coverage, not failure: how many channels are watched out of how many exist. Nine out of
 * forty is a completely different situation from nine out of nine, and only one of them is fine.
 */
import { useEffect, useState } from 'react'
import { api, ApiError } from './api'
import { Section, ErrorBox, Spinner } from './ui'
import { Donut } from './charts'
import type { UiError } from './store'
import { ProposeContract } from './ProposeContract'

type ContractStatus = {
  channel: string
  judged: boolean
  holding: boolean
  violations: number
  summary: string
  detail?: string
  checkedAt?: string
  staleSeconds: number
  expectations: number
  file?: string
  trend: Trend
}

type Verdict = {
  at: string
  judged: boolean
  violations: number
}

type Trend = {
  verdicts: Verdict[]
  checks: number
  failing: number
  changedAt?: string
  observedSince: string
  fromStartOfWindow: boolean
  flapping: boolean
}

type ContractList = {
  contracts: ContractStatus[]
  watching: number
  total: number
  /** Channels with no contract, so one can be offered for them. Never null. */
  without: string[]
}

/**
 * Three states, not two.
 *
 * "Not judged" is deliberately its own thing rather than being folded into either. A contract that has not seen
 * enough messages is neither passing nor failing, and showing it as green would be a lie of the exact kind this
 * whole feature exists to prevent — a system reporting healthy because its input stopped.
 */
type State = 'violated' | 'unjudged' | 'holding'

const stateColour: Record<State, string> = {
  violated: '#f87171',
  unjudged: '#94a3b8',
  holding: '#34d399',
}

const stateLabel: Record<State, string> = {
  violated: 'Changed',
  unjudged: 'Not enough messages yet',
  holding: 'As expected',
}

function stateOf(c: ContractStatus): State {
  if (c.violations > 0) return 'violated'
  if (!c.judged) return 'unjudged'
  return 'holding'
}

/** Twelve hours. Beyond that the verdict is describing a feed that may have changed since. */
const staleAfterSeconds = 12 * 60 * 60

export function Contracts() {
  const [list, setList] = useState<ContractList | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [loading, setLoading] = useState(true)

  // Kept in a ref-free form so saving a contract can trigger a reload without duplicating the fetch.
  const [reloadCount, setReloadCount] = useState(0)
  const reload = () => setReloadCount((n) => n + 1)

  useEffect(() => {
    let live = true

    async function load() {
      try {
        const res = (await api.contracts()) as ContractList
        if (live) {
          setList(res)
          setError(null)
        }
      } catch (e) {
        if (live) {
          setError({
            message: e instanceof Error ? e.message : String(e),
            problems: e instanceof ApiError ? e.problems : [],
          })
        }
      } finally {
        if (live) setLoading(false)
      }
    }

    void load()
    // Refreshed, but slowly. The underlying check runs every fifteen minutes by default, so polling faster
    // would only redraw the same answer.
    const timer = setInterval(() => void load(), 60_000)
    return () => {
      live = false
      clearInterval(timer)
    }
  }, [reloadCount])

  if (loading) return <Spinner label="Loading contracts…" />
  if (error) return <ErrorBox error={error} />
  if (!list) return null

  const violated = list.contracts.filter((c) => stateOf(c) === 'violated').length
  const unjudged = list.contracts.filter((c) => stateOf(c) === 'unjudged').length
  const holding = list.contracts.filter((c) => stateOf(c) === 'holding').length
  const unwatched = Math.max(0, list.total - list.watching)

  return (
    <div className="space-y-6">
      <Section
        title="What these feeds should look like"
        description="A contract says what a feed must look like, so the day a sending system changes you are
        told rather than finding out weeks later from a receiver that fell over. Nothing is failing when one of
        these fires — the messages are valid and the deliveries succeed."
      >
        {/* Somewhere to act, not only somewhere to read.
            
            This section could report that a feed had drifted and offered no way to make a contract in the
            first place, so using the feature meant a terminal on the server. It is placed above the summary
            because on a server with no contracts at all - the state every installation starts in - the
            summary has nothing to say and this is the only useful thing on the page. */}
        <div className="mb-6">
          <ProposeContract channels={list.without ?? []} onSaved={() => void reload()} />
        </div>

        {list.watching === 0 ? (
          <Empty total={list.total} />
        ) : (
          <div className="grid gap-8 md:grid-cols-[auto_1fr]">
            <div className="flex flex-col items-center">
              <Donut
                size={160}
                slices={[
                  { name: 'As expected', value: holding, colour: stateColour.holding },
                  { name: 'Changed', value: violated, colour: stateColour.violated },
                  { name: 'Not enough messages', value: unjudged, colour: stateColour.unjudged },
                  // Unwatched channels are part of the picture rather than left out of it. A donut showing
                  // nine happy contracts is misleading on a server with forty channels.
                  { name: 'No contract', value: unwatched, colour: '#334155' },
                ]}
              />
              <p className="mt-3 text-center text-2xl font-semibold text-slate-100">
                {list.watching} of {list.total}
              </p>
              <p className="text-sm text-slate-500">
                channel{list.total === 1 ? '' : 's'} watched
              </p>
            </div>

            <div className="space-y-3">
              <Count label="Still as expected" value={holding} colour={stateColour.holding} />
              <Count label="Changed since the contract was written" value={violated} colour={stateColour.violated} />
              <Count label="Not enough messages to judge" value={unjudged} colour={stateColour.unjudged} />
              {unwatched > 0 && (
                <div className="rounded-lg border border-slate-800 bg-slate-900/40 p-3">
                  <p className="text-sm text-slate-300">
                    {unwatched} channel{unwatched === 1 ? '' : 's'} {unwatched === 1 ? 'has' : 'have'} no
                    contract.
                  </p>
                  <p className="mt-1 text-xs text-slate-500">
                    Generate one from traffic you already have:{' '}
                    <code className="font-mono text-slate-400">
                      perfuse contract promote -o feed.contract.yaml messages/
                    </code>
                    , then prune it and point the channel at it.
                  </p>
                </div>
              )}
            </div>
          </div>
        )}
      </Section>

      {list.contracts.length > 0 && (
        <div className="space-y-3">
          {list.contracts.map((c) => (
            <ContractCard key={c.channel} status={c} />
          ))}
        </div>
      )}
    </div>
  )
}

function Empty({ total }: { total: number }) {
  return (
    <div className="rounded-xl border border-dashed border-slate-700 p-8 text-center">
      <p className="text-slate-300">No feed has a contract yet.</p>
      <p className="mx-auto mt-2 max-w-xl text-sm text-slate-500">
        {total === 0
          ? 'Create a channel first.'
          : `You have ${total} channel${total === 1 ? '' : 's'}. A contract is generated from traffic you
             already have, so this costs nothing to try on one of them.`}
      </p>
      <pre className="mx-auto mt-4 max-w-xl overflow-x-auto rounded-lg bg-slate-950 p-3 text-left font-mono
        text-xs text-slate-400">
        {`# Turn a working feed into expectations
perfuse contract promote -o adt.contract.yaml messages/

# Read it and delete most of it, then in the channel:
contract:
  file: adt.contract.yaml`}
      </pre>
    </div>
  )
}

function Count({ label, value, colour }: { label: string; value: number; colour: string }) {
  return (
    <div className="flex items-center gap-3">
      <span className="inline-block h-2.5 w-2.5 rounded-full" style={{ background: colour }} />
      <span className="text-2xl font-semibold text-slate-100 tabular-nums">{value}</span>
      <span className="text-sm text-slate-400">{label}</span>
    </div>
  )
}

function ContractCard({ status }: { status: ContractStatus }) {
  const state = stateOf(status)
  const stale = status.staleSeconds > staleAfterSeconds

  return (
    <div
      className="rounded-xl border border-slate-800 bg-slate-900/40 p-4"
      style={{ borderLeft: `3px solid ${stateColour[state]}` }}
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span
              className="inline-block h-2 w-2 shrink-0 rounded-full"
              style={{ background: stateColour[state] }}
            />
            <h3 className="truncate font-medium text-slate-100">{status.channel}</h3>
            <span className="text-xs text-slate-500">{stateLabel[state]}</span>
          </div>
          <p className="mt-1 text-sm text-slate-400">{status.summary}</p>
        </div>

        <div className="shrink-0 text-right">
          <p className="text-xs text-slate-500">
            {status.expectations} expectation{status.expectations === 1 ? '' : 's'}
          </p>
          {status.file && (
            <p className="mt-0.5 font-mono text-xs text-slate-400">{status.file}</p>
          )}
        </div>
      </div>

      <TrendStrip trend={status.trend} />

      {status.detail && (
        <p className="mt-3 rounded-lg bg-slate-950/60 p-3 text-sm text-slate-300">{status.detail}</p>
      )}

      {/* A verdict with no age invites trusting a check that stopped running a week ago. */}
      {status.checkedAt && (
        <p className={`mt-2 text-xs ${stale ? 'text-amber-300/90' : 'text-slate-400'}`}>
          checked {describeAge(status.staleSeconds)}
          {stale && ' — this verdict is old enough that the feed may have changed since'}
        </p>
      )}

      {state === 'violated' && (
        <p className="mt-2 text-xs text-slate-500">
          Nothing is broken: these messages parsed and were delivered. Something at the sending end is
          different from when this contract was written.
        </p>
      )}
    </div>
  )
}

function describeAge(seconds: number): string {
  if (seconds < 90) return 'just now'
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} minutes ago`
  const hours = Math.round(minutes / 60)
  if (hours < 48) return `${hours} hour${hours === 1 ? '' : 's'} ago`
  return `${Math.round(hours / 24)} days ago`
}


/** describeSince renders an age from an ISO timestamp. */
function describeSince(iso: string): string {
  const seconds = (Date.now() - new Date(iso).getTime()) / 1000
  if (seconds < 90) return `${Math.round(seconds)}s`
  if (seconds < 5400) return `${Math.round(seconds / 60)}m`
  if (seconds < 172800) return `${Math.round(seconds / 3600)}h`
  return `${Math.round(seconds / 86400)}d`
}

/**
 * TrendStrip shows the recent verdicts as blocks, oldest on the left.
 *
 * Blocks rather than a line chart, because the value is categorical - held, failed, or could not be judged - and a
 * line implies a continuum between them. Three colours, and "could not be judged" is visibly not a pass.
 */
function TrendStrip({ trend }: { trend: Trend }) {
  if (!trend || trend.checks === 0) {
    return (
      <p className="mt-3 text-xs text-slate-400">
        No checks recorded yet on this instance.
      </p>
    )
  }

  // Only the tail is drawn. A day of checks in one row of blocks is unreadable, and the recent end is the part
  // anybody is looking at.
  const shown = trend.verdicts.slice(-40)

  return (
    <div className="mt-3">
      <div className="flex items-end gap-[2px]" role="img" aria-label={`${trend.failing} of ${trend.checks} recent checks did not hold`}>
        {shown.map((v, i) => {
          const colour = !v.judged
            ? 'bg-slate-600'
            : v.violations === 0
              ? 'bg-emerald-500/80'
              : 'bg-rose-500'
          return (
            <span
              key={`${v.at}-${i}`}
              className={`h-5 flex-1 rounded-sm ${colour}`}
              title={`${new Date(v.at).toLocaleString()} — ${
                !v.judged ? 'not judged' : v.violations === 0 ? 'held' : `${v.violations} violation(s)`
              }`}
            />
          )
        })}
      </div>

      <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-slate-500">
        <span>
          {trend.failing} of {trend.checks} recent check{trend.checks === 1 ? '' : 's'} did not hold
        </span>

        {/* Flapping first, because a single "since" sentence actively misrepresents it - the same failure count
            describes a feed that broke once and one alternating every few minutes, and they need different action. */}
        {trend.flapping ? (
          <span className="text-amber-400">
            alternating rather than a single change — something intermittent
          </span>
        ) : (
          trend.changedAt && (
            <span>
              {/* A floor rather than a measurement when the whole window agrees, and it says so, because the real
                  change happened before anything recorded. */}
              {trend.fromStartOfWindow ? 'unchanged for at least ' : 'since '}
              {describeSince(trend.changedAt)}
              {trend.fromStartOfWindow ? '' : ' ago'}
            </span>
          )
        )}

        <span className="text-slate-400">watching since {describeSince(trend.observedSince)} ago</span>
      </div>
    </div>
  )
}
