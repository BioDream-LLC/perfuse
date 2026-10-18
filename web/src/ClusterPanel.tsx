import { useCallback, useEffect, useState } from 'react'
import { useSelector } from 'react-redux'
import { api, ApiError, atLeast } from './api'
import type { ClusterLease, ClusterStatus } from './api'
import type { RootState, UiError } from './store'
import { ErrorBox, Section } from './ui'
import { IconFleet } from './Icons'

/**
 * The cluster: the servers sharing this one's PostgreSQL store, which leads, which runs each channel, and what happened
 * between them. A server on its own says so and how to make it two.
 */

function toError(e: unknown): UiError {
  return { message: e instanceof Error ? e.message : String(e), problems: e instanceof ApiError ? e.problems : [] }
}

function ago(iso: string): string {
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000))
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.round(s / 60)}m ago`
  return `${Math.round(s / 3600)}h ago`
}

const kindColour: Record<string, string> = {
  gained: 'text-emerald-300',
  lost: 'text-amber-300',
  move: 'text-sky-300',
}

function leaseLabel(l: string): string {
  return l === 'leader' ? 'leadership' : l.replace(/^channel:main\//, '').replace(/^channel:/, '')
}

export function ClusterPanel() {
  const me = useSelector((s: RootState) => s.session.me)
  const isAdmin = atLeast(me?.role, 'admin')
  const [st, setSt] = useState<ClusterStatus | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [note, setNote] = useState('')
  const [moveTo, setMoveTo] = useState<Record<string, string>>({})

  const load = useCallback(() => {
    api.cluster().then(
      (s) => {
        setSt(s)
        setError(null)
      },
      (e) => setError(toError(e)),
    )
  }, [])
  useEffect(() => {
    load()
    const t = setInterval(load, 3000)
    return () => clearInterval(t)
  }, [load])

  if (!st) return error ? <ErrorBox error={error} /> : null
  if (!st.enabled) {
    return (
      <Section title="Cluster" icon={IconFleet}>
        <p className="text-sm text-slate-400">
          This server runs on its own. To make it two or more that take over from each other, give them one PostgreSQL store (
          <code className="font-mono text-xs text-sky-300">-db postgres://…</code>) and start each with{' '}
          <code className="font-mono text-xs text-sky-300">-cluster active-passive</code> or{' '}
          <code className="font-mono text-xs text-sky-300">active-active</code>. The manual's Operations chapter explains both and the load
          balancer in front.
        </p>
      </Section>
    )
  }

  const nodes = st.nodes ?? []
  const byId = Object.fromEntries(nodes.map((n) => [n.id, n]))
  const name = (id: string) => byId[id]?.name ?? (id || 'nobody')
  const live = nodes.filter((n) => n.live)
  const channelLeases = (st.leases ?? []).filter((l) => l.name.startsWith('channel:'))
  const act = (p: Promise<unknown>, done: string) =>
    p.then(
      () => {
        setNote(done)
        load()
      },
      (e) => setError(toError(e)),
    )
  const thisNode = nodes.find((n) => n.this)

  return (
    <Section
      title="Cluster"
      icon={IconFleet}
      description={
        st.mode === 'active-passive'
          ? 'Active-passive: the leader runs every channel; the others wait and the first to see it stop takes over.'
          : 'Active-active: channels are spread over the live servers; a server that stops hands its channels to the rest.'
      }
    >
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      {st.error && (
        <p role="alert" className="mb-3 rounded border border-rose-800 bg-rose-950/40 px-3 py-2 text-sm text-rose-200">
          This server cannot reach the shared store: {st.error}. It keeps its channels only until its leases run out.
        </p>
      )}
      {note && (
        <p role="status" className="mb-3 text-xs text-emerald-300">
          {note}
        </p>
      )}
      <div className="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <div className="rounded-xl border border-slate-800 p-3">
          <p className="text-2xl font-semibold text-emerald-300">{live.length}</p>
          <p className="text-xs text-slate-500">servers live</p>
        </div>
        <div className="rounded-xl border border-slate-800 p-3">
          <p className="truncate text-lg font-semibold text-violet-300">{st.leader ? name(st.leader) : '—'}</p>
          <p className="text-xs text-slate-500">leader</p>
        </div>
        <div className="rounded-xl border border-slate-800 p-3">
          <p className="text-2xl font-semibold text-sky-300">{st.leaseSeconds}s</p>
          <p className="text-xs text-slate-500">lease (failover within about this)</p>
        </div>
        <div className="rounded-xl border border-slate-800 p-3">
          <p className="truncate text-lg font-semibold text-slate-200">{thisNode?.name ?? st.this}</p>
          <p className="text-xs text-slate-500">this server</p>
        </div>
      </div>

      <table className="w-full text-sm" data-testid="cluster-nodes">
        <thead>
          <tr className="text-left text-[11px] uppercase tracking-wide text-slate-500">
            <th className="py-1 pr-3">Server</th>
            <th className="py-1 pr-3">State</th>
            <th className="py-1 pr-3">Version</th>
            <th className="py-1 pr-3">Last seen</th>
            <th className="py-1 pr-3">{st.mode === 'active-active' ? 'Channels' : 'Runs'}</th>
            <th />
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-800">
          {nodes.map((n) => (
            <tr key={n.id} className="align-top">
              <td className="py-1.5 pr-3">
                <span className="text-slate-200">{n.name}</span>
                {n.this && <span className="ml-2 text-[11px] text-slate-500">(this one)</span>}
                {n.address && <div className="font-mono text-[11px] text-slate-500">{n.address}</div>}
              </td>
              <td className="py-1.5 pr-3">
                {n.leader && (
                  <span className="mr-1 rounded border border-violet-700 bg-violet-950/40 px-1.5 py-0.5 text-[11px] text-violet-300">
                    leader
                  </span>
                )}
                {n.live ? (
                  <span className="text-emerald-300">live</span>
                ) : n.leaving ? (
                  <span className="text-slate-400">left</span>
                ) : (
                  <span className="text-rose-300">not answering</span>
                )}
              </td>
              <td className="py-1.5 pr-3 font-mono text-xs text-slate-400">{n.version || '—'}</td>
              <td className="py-1.5 pr-3 text-xs text-slate-400">{ago(n.seenAt)}</td>
              <td className="py-1.5 pr-3 text-xs text-slate-300">
                {st.mode === 'active-active'
                  ? (n.channels ?? []).map((c) => c.replace(/^main\//, '')).join(', ') || '—'
                  : n.leader
                    ? 'every channel'
                    : 'standing by'}
              </td>
              <td className="py-1.5 text-right">
                {isAdmin && n.this && n.leader && live.length > 1 && (
                  <button
                    type="button"
                    className="btn-ghost py-0.5 text-xs"
                    title="Hand the lead to another live server, and stay away from it for a while: for maintenance on this one"
                    onClick={() => {
                      if (window.confirm('Hand the lead to another server? Its channels move with it.'))
                        void act(api.clusterStepDown(), 'Stepped down; another server takes the lead within a few seconds.')
                    }}
                  >
                    Step down
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      {st.mode === 'active-active' && channelLeases.length > 0 && (
        <div className="mt-5">
          <p className="mb-2 text-[11px] uppercase tracking-wide text-slate-500">Where each channel runs</p>
          <table className="w-full text-sm" data-testid="cluster-channels">
            <tbody className="divide-y divide-slate-800">
              {channelLeases.map((l: ClusterLease) => (
                <tr key={l.name}>
                  <td className="py-1.5 pr-3 text-slate-200">
                    {leaseLabel(l.name)}
                    {st.members?.[l.name] && (
                      <span className="ml-2 text-[11px] text-slate-500" title="Channels that route to one another run on one server">
                        with {st.members[l.name]!.filter((m) => m !== leaseLabel(l.name)).join(', ')}
                      </span>
                    )}
                  </td>
                  <td className="py-1.5 pr-3 text-xs">
                    {l.lapsed ? <span className="text-amber-300">nobody (being taken over)</span> : <span className="text-emerald-300">{name(l.holder)}</span>}
                    {l.wantedBy && <span className="ml-2 text-sky-300">→ moving to {name(l.wantedBy)}</span>}
                  </td>
                  <td className="py-1.5 text-right">
                    {isAdmin && (
                      <span className="flex justify-end gap-1">
                        <select
                          aria-label={`Move ${leaseLabel(l.name)} to`}
                          className="input py-0.5 text-xs"
                          value={moveTo[l.name] ?? ''}
                          onChange={(e) => setMoveTo((m) => ({ ...m, [l.name]: e.target.value }))}
                        >
                          <option value="">move to…</option>
                          {live
                            .filter((n) => n.id !== l.holder)
                            .map((n) => (
                              <option key={n.id} value={n.id}>
                                {n.name}
                              </option>
                            ))}
                        </select>
                        <button
                          type="button"
                          className="btn-ghost py-0.5 text-xs"
                          disabled={!moveTo[l.name]}
                          onClick={() =>
                            void act(
                              api.clusterMove(leaseLabel(l.name), moveTo[l.name]!),
                              `${leaseLabel(l.name)} moves to ${name(moveTo[l.name]!)} at its next renewal.`,
                            )
                          }
                        >
                          Move
                        </button>
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <details className="mt-5">
        <summary className="cursor-pointer text-xs text-slate-400">What happened ({(st.events ?? []).length})</summary>
        <ul className="mt-2 space-y-1 text-xs" data-testid="cluster-events">
          {(st.events ?? []).map((e, i) => (
            <li key={i} className="flex gap-2">
              <span className="w-40 shrink-0 text-slate-500">{new Date(e.at).toLocaleString()}</span>
              <span className="w-28 shrink-0 truncate text-slate-300">{name(e.node)}</span>
              <span className={`w-16 shrink-0 ${kindColour[e.kind] ?? 'text-slate-400'}`}>{e.kind}</span>
              <span className="text-slate-400">
                {leaseLabel(e.lease ?? '')} {e.detail}
              </span>
            </li>
          ))}
        </ul>
      </details>
    </Section>
  )
}
