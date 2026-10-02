import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from './api'
import type { FleetPeers } from './api'
import { Confirm, ErrorBox, Field } from './ui'
import type { UiError } from './store'

/**
 * Watching another instance, arranged from here.
 *
 * The Fleet section explained what a fleet was, said "this is a fleet of one", and printed a command to run in a
 * terminal on the other machine. Everything after that - where the token goes, writing a peers file, restarting -
 * was undocumented on screen and impossible from this interface.
 *
 * The instruction for the other end stays, because it genuinely has to happen there: a token can only be issued
 * by the instance being watched. What is new is that everything on this end is now a form, and the peer starts
 * being polled immediately rather than at the next restart.
 */
export function FleetPeersPanel() {
  const [state, setState] = useState<FleetPeers | null>(null)
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [token, setToken] = useState('')
  const [allowControl, setAllowControl] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<UiError | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const [removing, setRemoving] = useState<string | null>(null)

  const toError = (e: unknown): UiError => ({
    message: e instanceof Error ? e.message : String(e),
    problems: e instanceof ApiError ? e.problems : [],
  })

  const load = useCallback(async () => {
    try {
      setState(await api.fleetPeers())
    } catch (e) {
      setError(toError(e))
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function add() {
    setBusy(true)
    setError(null)
    setNote(null)
    try {
      const res = await api.addFleetPeer({ name, url, token, allowControl })
      setNote(
        res.replaced
          ? `Replaced ${res.name}. It is being polled now.`
          : `Watching ${res.name}. It is being polled now — no restart needed.`,
      )
      setName('')
      setUrl('')
      setToken('')
      setAllowControl(false)
      setOpen(false)
      await load()
    } catch (e) {
      setError(toError(e))
    } finally {
      setBusy(false)
    }
  }

  async function remove(peer: string) {
    setBusy(true)
    setError(null)
    try {
      await api.removeFleetPeer(peer)
      setNote(`Stopped watching ${peer}.`)
      await load()
    } catch (e) {
      setError(toError(e))
    } finally {
      setBusy(false)
      setRemoving(null)
    }
  }

  // Not null on failure.
  //
  // Returning null when the list could not be read renders nothing at all, which is the silent failure this whole
  // exercise has been finding elsewhere: the panel simply is not there and no reason is given. An error that has
  // arrived is shown even before the list exists.
  if (state === null) {
    return error ? <ErrorBox error={error} onDismiss={() => setError(null)} /> : null
  }

  if (!state.writable) {
    return (
      <p className="text-xs text-slate-500">
        This server has nowhere to store fleet peers, so they cannot be added here. Start it with{' '}
        <code className="font-mono text-slate-400">-peers</code> pointing at a writable file.
      </p>
    )
  }

  return (
    <div className="space-y-3">
      {note !== null && (
        <p role="status" className="text-xs text-emerald-300">
          {note}
        </p>
      )}

      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

      {state.peers.length > 0 && (
        <ul className="space-y-2">
          {state.peers.map((p) => (
            <li
              key={p.name}
              className="flex flex-wrap items-center gap-3 rounded-lg border border-slate-800 bg-slate-900/40 p-3 text-xs"
            >
              <span className="font-medium text-slate-200">{p.name}</span>
              <span className="font-mono text-slate-500">{p.url}</span>
              {p.allowControl && (
                <span className="badge border border-amber-800/50 bg-amber-950/50 text-amber-300">
                  can start and stop channels
                </span>
              )}
              {!p.hasToken && (
                <span className="badge border border-rose-800/50 bg-rose-950/50 text-rose-300">no token</span>
              )}
              <button
                className="btn-ghost ml-auto py-1 text-xs"
                aria-label={`Stop watching ${p.name}`}
                onClick={() => setRemoving(p.name)}
                disabled={busy}
              >
                Stop watching
              </button>
            </li>
          ))}
        </ul>
      )}

      {!open ? (
        <button className="btn-primary py-1 text-xs" onClick={() => setOpen(true)}>
          Watch another instance
        </button>
      ) : (
        <div className="rounded-xl border border-slate-800 bg-slate-900/40 p-4">
          <h4 className="text-sm font-medium text-slate-200">Watch another instance</h4>

          {/* Kept, because this part genuinely has to happen there: only the instance being watched can issue a
              token for itself. What changed is that it is now one step of three rather than the whole job. */}
          <p className="mt-2 text-xs text-slate-500">
            On the instance you want to watch, sign in and issue a read-only token under Users. Paste it here. A
            viewer token is enough — this view only reads.
          </p>

          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            <Field label="What to call it" hint="A name you will recognise in a hurry, not a hostname.">
              <input
                className="input w-full py-1 text-xs"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="theatre-02"
              />
            </Field>

            <Field label="Address" hint="Including https:// and the port.">
              <input
                className="input w-full py-1 text-xs"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="https://perfuse-02.hospital.internal:8443"
              />
            </Field>
          </div>

          <div className="mt-3">
            <Field
              label="Read-only token from that instance"
              hint="Stored on this server only, and never sent back to a browser."
            >
              <input
                type="password"
                className="input w-full py-1 font-mono text-xs"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                autoComplete="off"
              />
            </Field>
          </div>

          <label className="mt-3 flex items-start gap-2 text-xs text-slate-400">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={allowControl}
              onChange={(e) => setAllowControl(e.target.checked)}
            />
            <span>
              <span className="block text-slate-300">Allow starting and stopping its channels from here</span>
              <span className="mt-0.5 block text-[11px] text-slate-500">
                Off by default. Reading another instance&rsquo;s health is a small privilege; stopping a feed on
                it is not, and the token would need to permit it too.
              </span>
            </span>
          </label>

          <div className="mt-4 flex gap-2">
            <button className="btn-primary py-1 text-xs" onClick={() => void add()} disabled={busy}>
              {busy ? 'Saving…' : 'Start watching it'}
            </button>
            <button
              className="btn-ghost py-1 text-xs"
              onClick={() => {
                setOpen(false)
                setError(null)
              }}
              disabled={busy}
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      <Confirm
        open={removing !== null}
        title={`Stop watching ${removing ?? ''}?`}
        body="Its readings disappear from this page. Nothing on that instance changes — it keeps running and keeps handling messages."
        confirmLabel="Stop watching"
        onConfirm={() => {
          if (removing !== null) void remove(removing)
        }}
        onCancel={() => setRemoving(null)}
      />
    </div>
  )
}
