import { useEffect, useState } from 'react'
import { api } from './api'
import type { ChannelSummary } from './api'
import { ErrorBox, Section } from './ui'
import { IconShadow } from './Icons'
import { toUiError } from './store'
import type { UiError } from './store'

/**
 * Starting and stopping a comparison, which could previously only be done by editing a channel file.
 *
 * # Why this exists
 *
 * The builder's drift guard excused the entire shadow block with the reason that it is "configured from the shadow tab, where the
 * candidate can be chosen from a list of real channels". The reason was good and the capability did not exist: the shadow tab
 * reported differences and offered no way to start a comparison, and there was no endpoint behind it either.
 *
 * So the one feature whose whole purpose is checking a rewrite before it goes live could only be switched on by hand-editing YAML —
 * which is the thing somebody reaches for a shadow to avoid doing blind.
 *
 * # Why the candidate is a list and the share is a percentage
 *
 * Both are the same kind of decision. A candidate typed from memory fails at load rather than at save, so a channel disappears and
 * nobody connects it to what they typed. And a share entered as a fraction into a field that means a percentage gives a comparison
 * that observes almost nothing, silently — the file loads and every number looks plausible.
 */
export function ShadowEditor({
  channels,
  comparing,
  onChanged,
}: {
  /** Every channel, so a candidate can be chosen rather than typed. */
  channels: ChannelSummary[]
  /** The channels already comparing, by name, so this offers stopping rather than starting. */
  comparing: Set<string>
  onChanged: () => void
}) {
  const [live, setLive] = useState('')
  const [candidate, setCandidate] = useState('')
  const [sample, setSample] = useState(100)
  const [ignore, setIgnore] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<UiError | null>(null)
  const [done, setDone] = useState<string | null>(null)
  const [configured, setConfigured] = useState<Set<string>>(new Set())

  // The first channel is chosen so the form arrives usable. An empty select with a submit button beside it reads as broken.
  useEffect(() => {
    const first = channels[0]
    if (live === '' && first !== undefined) setLive(first.name)
  }, [channels, live])

  const others = channels.filter((c) => c.name !== live)

  // Configured is tracked separately from running, because they are not the same thing and conflating them misreports both.
  //
  // A saved comparison is in the channel file immediately; it starts observing when the channel next loads. The list of running
  // comparisons therefore does not include it yet, and reading only that list made the screen offer Start again for a comparison
  // that had just been set up - which is how somebody configures one twice.
  const already = comparing.has(live) || configured.has(live)

  async function start() {
    setBusy(true)
    setError(null)
    setDone(null)

    try {
      await api.startShadow(live, {
        candidate,
        sample,
        ignore: ignore
          .split(',')
          .map((s) => s.trim())
          .filter((s) => s !== ''),
        compare: [],
        maxDifferences: 0,
      })
      // Worded as configured rather than as running, because the comparison begins when the channel next loads and a screen
      // saying it is already observing would be wrong for as long as that takes.
      setDone(`${live} is set to be compared against ${candidate}. It starts observing when the channel next loads.`)
      setConfigured((prev) => new Set(prev).add(live))
      onChanged()
    } catch (e) {
      setError(toUiError(e))
    } finally {
      setBusy(false)
    }
  }

  async function stop() {
    setBusy(true)
    setError(null)
    setDone(null)

    try {
      await api.stopShadow(live)
      setDone(`${live} is no longer being compared. The candidate channel is untouched.`)
      setConfigured((prev) => {
        const next = new Set(prev)
        next.delete(live)

        return next
      })
      onChanged()
    } catch (e) {
      setError(toUiError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Section
      icon={IconShadow}
      title="Compare a rewrite against what is running"
      description="The live channel keeps delivering. The candidate sees the same messages and its output is compared, so a rewrite can be judged on real traffic before anything depends on it."
    >
      {error && <ErrorBox error={error} />}

      {done && (
        <p role="status" className="mb-3 rounded-md border border-emerald-500/40 bg-emerald-500/10 p-2 text-sm text-emerald-200">
          {done}
        </p>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        <label className="block text-sm">
          <span className="mb-1 block text-slate-300">Channel that is running</span>
          <select className="select w-full" value={live} onChange={(e) => setLive(e.target.value)}>
            {channels.map((c) => (
              <option key={c.name} value={c.name}>
                {c.name}
              </option>
            ))}
          </select>
        </label>

        <label className="block text-sm">
          <span className="mb-1 block text-slate-300">Candidate to compare against</span>
          <select
            className="select w-full"
            value={candidate}
            onChange={(e) => setCandidate(e.target.value)}
            disabled={already}
          >
            <option value="">choose a channel…</option>
            {others.map((c) => (
              <option key={c.name} value={c.name}>
                {c.name}
              </option>
            ))}
          </select>
          <span className="mt-1 block text-xs text-slate-500">
            Chosen from the channels that exist. A name typed from memory fails when the channel is loaded rather than when it is
            saved, so the live channel disappears and nothing points at the typo.
          </span>
        </label>
      </div>

      <div className="mt-3 grid gap-3 sm:grid-cols-2">
        <label className="block text-sm">
          <span className="mb-1 block text-slate-300">Share of messages to compare (%)</span>
          <input
            type="number"
            className="input w-full"
            min={1}
            max={100}
            value={sample}
            onChange={(e) => setSample(Number(e.target.value))}
            disabled={already}
          />
          <span className="mt-1 block text-xs text-slate-500">
            Lower this only on a busy feed, and know what it costs: sampling reduces work and confidence in the same proportion, and
            the message that would have shown the difference is the unusual one.
          </span>
        </label>

        <label className="block text-sm">
          <span className="mb-1 block text-slate-300">Fields to ignore</span>
          <input
            className="input w-full font-mono"
            value={ignore}
            onChange={(e) => setIgnore(e.target.value)}
            placeholder="MSH-7, MSH-10"
            disabled={already}
          />
          <span className="mt-1 block text-xs text-slate-500">
            Comma separated. Usually needed: a channel that stamps a timestamp or a sequence number differs on every message, which
            reports a difference rate of 100% and tells you nothing.
          </span>
        </label>
      </div>

      <div className="mt-4 flex items-center gap-2">
        {already ? (
          <>
            <button className="btn-secondary" onClick={() => void stop()} disabled={busy}>
              Stop comparing
            </button>
            <span className="text-xs text-slate-500">
              {live} is already being compared. Stopping leaves the candidate channel in place, because it is the thing meant to go
              live.
            </span>
          </>
        ) : (
          <>
            <button className="btn-primary" onClick={() => void start()} disabled={busy || candidate === ''}>
              Start comparing
            </button>
            {candidate === '' && <span className="text-xs text-slate-500">Choose a candidate first.</span>}
          </>
        )}
      </div>
    </Section>
  )
}
