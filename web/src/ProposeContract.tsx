import { CodeArea } from './CodeArea'
import { useState } from 'react'
import { api, ApiError } from './api'
import type { ProposedContract } from './api'
import { ErrorBox } from './ui'
import type { UiError } from './store'

/**
 * Making a contract from traffic that has already arrived.
 *
 * This step existed only at the command line. The Contracts section could tell you a feed had drifted from its
 * contract and gave you no way to make one, so using the feature at all meant a terminal on the server - which
 * for a product whose claim is that everything is doable from the interface is a defect, not an omission.
 *
 * Propose then save, deliberately, rather than one button that writes a file. A contract is an assertion about
 * what a sending system does, and the person who knows whether last week's traffic is representative is the one
 * reading the screen. Writing it silently would produce contracts nobody agreed to, which fire alerts nobody
 * understands, which get switched off - and then nothing is watched at all.
 */
export function ProposeContract({
  channels,
  onSaved,
}: {
  /** Channels that have no contract yet. */
  channels: string[]
  onSaved: () => void
}) {
  const [channel, setChannel] = useState(channels[0] ?? '')
  const [proposed, setProposed] = useState<ProposedContract | null>(null)
  const [edited, setEdited] = useState('')
  const [busy, setBusy] = useState(false)
  const [checkEvery, setCheckEvery] = useState('')
  const [error, setError] = useState<UiError | null>(null)
  const [saved, setSaved] = useState<string | null>(null)

  const toError = (e: unknown): UiError => ({
    message: e instanceof Error ? e.message : String(e),
    problems: e instanceof ApiError ? e.problems : [],
  })

  async function propose() {
    if (!channel) return
    setBusy(true)
    setError(null)
    setSaved(null)
    setProposed(null)
    try {
      const res = await api.proposeContract(channel)
      setProposed(res)
      setEdited(res.yaml)
    } catch (e) {
      setError(toError(e))
    } finally {
      setBusy(false)
    }
  }

  async function save() {
    setBusy(true)
    setError(null)
    try {
      const res = await api.saveContract(channel, edited, checkEvery.trim())
      setSaved(res.file)
      setProposed(null)
      onSaved()
    } catch (e) {
      setError(toError(e))
    } finally {
      setBusy(false)
    }
  }

  if (channels.length === 0) {
    return (
      <p className="text-sm text-slate-500">
        Every channel has a contract. Nothing to propose.
      </p>
    )
  }

  return (
    <div className="rounded-xl border border-slate-800 bg-slate-900/40 p-4">
      <h3 className="text-sm font-medium text-slate-200">Learn a contract from what has arrived</h3>
      <p className="mt-1 text-xs text-slate-500">
        Reads the messages this server has already recorded for a channel and writes down what they had in
        common. It is a starting point to read and cut down, not an answer — an expectation that happens to
        hold for last week is not the same as one the sending system guarantees.
      </p>

      <div className="mt-3 flex flex-wrap items-end gap-2">
        <label className="text-xs text-slate-400">
          <span className="block pb-1">Channel with no contract</span>
          <select
            className="select py-1 text-xs"
            value={channel}
            onChange={(e) => {
              setChannel(e.target.value)
              setProposed(null)
              setSaved(null)
            }}
          >
            {channels.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
        </label>

        <button className="btn-primary py-1 text-xs" onClick={() => void propose()} disabled={busy || !channel}>
          {busy && proposed === null ? 'Reading traffic…' : 'Propose a contract'}
        </button>
      </div>

      {error && (
        <div className="mt-3">
          <ErrorBox error={error} onDismiss={() => setError(null)} />
        </div>
      )}

      {saved !== null && (
        <p role="status" className="mt-3 text-xs text-emerald-300">
          Saved as {saved} and attached to {channel}. It will be checked from now on.
        </p>
      )}

      {proposed && (
        <div className="mt-4 space-y-3">
          <div className="flex flex-wrap items-baseline gap-3 text-xs">
            <span className="text-slate-300">
              {proposed.expectations} expectation{proposed.expectations === 1 ? '' : 's'}
            </span>
            <span className="text-slate-500">
              from {proposed.messages} message{proposed.messages === 1 ? '' : 's'}
            </span>
          </div>

          {/* The notes are the honest part: how much evidence this is built on, and what saving replaces. */}
          {proposed.notes.length > 0 && (
            <ul className="space-y-1 text-xs text-amber-300/90">
              {proposed.notes.map((n) => (
                <li key={n}>{n}</li>
              ))}
            </ul>
          )}

          <label className="block text-xs text-slate-400">
            <span className="block pb-1">
              Read it before saving. Deleting an expectation here is the normal thing to do.
            </span>
            <CodeArea
            language="yaml"
              aria-label="Proposed contract"
              className="h-64 w-full"
              value={edited}
              onChange={setEdited}
              spellCheck={false}
            />
          </label>

          <div className="flex gap-2">
            {/* How often the contract is re-checked.
                
                Asked here because this is where somebody decides what to assert about a feed, and they are the person who knows how often it
                is worth asking. It was previously settable only by hand-editing the channel file: the builder has no contract section, which
                is deliberate, and nothing else offered it. */}
            <label className="block text-xs">
              <span className="mb-1 block text-slate-400">Re-check every</span>
              <input
                className="input font-mono"
                value={checkEvery}
                onChange={(e) => setCheckEvery(e.target.value)}
                placeholder="15m"
              />
              <span className="mt-1 block text-slate-600">
                A duration. Empty keeps whatever is set, which for a new contract is fifteen minutes.
              </span>
            </label>

            <button className="btn-primary py-1 text-xs" onClick={() => void save()} disabled={busy}>
              {busy ? 'Saving…' : `Save and start checking ${channel}`}
            </button>
            <button
              className="btn-ghost py-1 text-xs"
              onClick={() => {
                setProposed(null)
                setError(null)
              }}
              disabled={busy}
            >
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
