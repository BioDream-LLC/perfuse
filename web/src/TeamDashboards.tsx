import { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from './api'
import type {
  AlertSnapshot,
  AuditEntry,
  CertificateSnapshot,
  ConnectionCheck,
  DashboardDef,
  DashboardFigure,
  DashboardsResponse,
  DashboardTile,
  DestinationStat,
  MessageStats,
  QueueSnapshot,
  StatusResponse,
} from './api'
import { ErrorBox, Section, Spinner } from './ui'
import { IconDashboard, sectionIcons } from './Icons'

/**
 * Team dashboards: a dashboard for each kind of person who watches Perfuse, made of tiles that show aggregates only - counts,
 * states and times, never a message or a patient. Each person opens on the one their directory group or role is given, and can
 * reorder or hide tiles, filter to a channel, and save that as their own view. For more than that, a dashboard exports to
 * Grafana over /metrics.
 */

interface Data {
  status?: StatusResponse
  stats?: MessageStats
  destinations?: DestinationStat[]
  queue?: QueueSnapshot
  alerts?: AlertSnapshot
  certificates?: CertificateSnapshot
  connections?: ConnectionCheck[]
  audit?: AuditEntry[]
  figures?: Record<string, DashboardFigure>
  failures: Record<string, string>
}

export function TeamDashboards() {
  const [meta, setMeta] = useState<DashboardsResponse | null>(null)
  const [dashboardId, setDashboardId] = useState('')
  const [tiles, setTiles] = useState<string[]>([])
  const [channel, setChannel] = useState('')
  const [data, setData] = useState<Data>({ failures: {} })
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState('')
  const [editing, setEditing] = useState(false)

  useEffect(() => {
    api
      .dashboards()
      .then((m) => {
        setMeta(m)
        const id = m.saved?.dashboard ?? m.assigned
        setDashboardId(id)
        const def = m.dashboards.find((d) => d.id === id)
        setTiles(m.saved?.tiles?.length ? m.saved.tiles : (def?.tiles ?? []))
        setChannel(m.saved?.channel ?? '')
      })
      .catch((e) => setError(e instanceof Error ? e.message : 'the dashboards could not be loaded'))
  }, [])

  const def: DashboardDef | undefined = meta?.dashboards.find((d) => d.id === dashboardId)
  const tileDefs = useMemo(() => new Map((meta?.tiles ?? []).map((t) => [t.id, t])), [meta])

  // Each data source is fetched once however many tiles use it, and a source that fails says so on its own tiles only.
  const load = useCallback(async () => {
    const sources = new Set(tiles.map((t) => tileDefs.get(t)?.source).filter(Boolean) as string[])
    const next: Data = { failures: {} }
    const run = async (name: string, f: () => Promise<void>) => {
      if (!sources.has(name)) return
      try {
        await f()
      } catch (e) {
        next.failures[name] = e instanceof Error ? e.message : 'unavailable'
      }
    }
    const needStats = sources.has('stats') || sources.has('destinations') || sources.has('summary')
    await Promise.all([
      run('status', async () => void (next.status = await api.status())),
      needStats
        ? (async () => {
            try {
              const s = await api.stats()
              next.stats = s.stats
              next.destinations = s.destinations
            } catch (e) {
              next.failures.stats = e instanceof Error ? e.message : 'unavailable'
            }
          })()
        : Promise.resolve(),
      run('queue', async () => void (next.queue = await api.queue({}))),
      sources.has('alerts') || sources.has('summary')
        ? (async () => {
            try {
              next.alerts = await api.alerts()
            } catch (e) {
              next.failures.alerts = e instanceof Error ? e.message : 'unavailable'
            }
          })()
        : Promise.resolve(),
      run('certificates', async () => void (next.certificates = await api.certificates())),
      run('connections', async () => void (next.connections = (await api.connections(channel || undefined)).connections)),
      run('audit', async () => void (next.audit = (await api.listAudit(30)).entries)),
      run('figures', async () => {
        const ids = tiles.filter((t) => tileDefs.get(t)?.source === 'figures')
        next.figures = (await api.dashboardFigures(ids)).figures
      }),
    ])
    setData(next)
  }, [tiles, tileDefs, channel])

  useEffect(() => {
    if (tiles.length === 0) return
    void load()
    const t = setInterval(() => void load(), 30_000)
    return () => clearInterval(t)
  }, [load, tiles])

  function pick(id: string) {
    setDashboardId(id)
    setTiles(meta?.dashboards.find((d) => d.id === id)?.tiles ?? [])
    setSaved('')
  }
  function move(i: number, by: number) {
    const j = i + by
    if (j < 0 || j >= tiles.length) return
    const next = tiles.map((t, k) => (k === i ? tiles[j] : k === j ? tiles[i] : t)) as string[]
    setTiles(next)
  }
  async function save() {
    try {
      await api.saveDashboardView({ dashboard: dashboardId, tiles, channel: channel || undefined })
      setSaved('Saved as your view. It opens here next time.')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'the view could not be saved')
    }
  }
  async function reset() {
    await api.saveDashboardView({ dashboard: '' }).catch(() => {})
    if (meta) {
      setDashboardId(meta.assigned)
      setTiles(meta.dashboards.find((d) => d.id === meta.assigned)?.tiles ?? [])
      setChannel('')
    }
    setSaved('Back to the dashboard you are given.')
  }
  async function exportGrafana() {
    try {
      const json = await api.grafanaDashboard(dashboardId, tiles)
      const blob = new Blob([JSON.stringify(json, null, 2)], { type: 'application/json' })
      const a = document.createElement('a')
      a.href = URL.createObjectURL(blob)
      a.download = `perfuse-${dashboardId}.grafana.json`
      a.click()
      URL.revokeObjectURL(a.href)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'the export failed')
    }
  }

  if (!meta) return error ? <ErrorBox error={{ message: error, problems: [] }} /> : <Spinner label="Loading dashboards…" />
  const channels = data.status?.channels.map((c) => c.name) ?? []
  const hidden = (def?.tiles ?? []).filter((t) => !tiles.includes(t))

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-lg font-semibold text-slate-100">Team dashboards</h1>
          <p className="mt-1 max-w-3xl text-sm text-slate-500">
            A dashboard for each kind of person who watches the interfaces. Tiles show counts, states and times, never a message
            or a patient. You open on <b className="text-slate-300">{meta.dashboards.find((d) => d.id === meta.assigned)?.title}</b>,
            from {meta.assignedBy}.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="text-xs text-slate-400" htmlFor="dash-pick">
            Dashboard
          </label>
          <select id="dash-pick" className="select" value={dashboardId} onChange={(e) => pick(e.target.value)}>
            {meta.dashboards.map((d) => (
              <option key={d.id} value={d.id}>
                {d.title} - {d.audience}
              </option>
            ))}
          </select>
          <label className="text-xs text-slate-400" htmlFor="dash-channel">
            Channel
          </label>
          <select id="dash-channel" className="select" value={channel} onChange={(e) => setChannel(e.target.value)}>
            <option value="">All channels</option>
            {channels.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
          <button className="btn-ghost text-xs" onClick={() => setEditing(!editing)} aria-pressed={editing}>
            {editing ? 'Done arranging' : 'Arrange tiles'}
          </button>
          <button className="btn-primary text-xs" onClick={() => void save()}>
            Save as my view
          </button>
          <button className="btn-ghost text-xs" onClick={() => void reset()}>
            Reset
          </button>
          <button className="btn-ghost text-xs" onClick={() => void exportGrafana()}>
            Export to Grafana
          </button>
        </div>
      </div>
      {error && <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />}
      {saved && (
        <p className="text-xs text-emerald-300" role="status">
          {saved}
        </p>
      )}
      {def && <p className="text-sm text-slate-400">{def.description}</p>}
      {editing && hidden.length > 0 && (
        <p className="text-xs text-slate-400">
          Hidden:{' '}
          {hidden.map((t) => (
            <button key={t} className="btn-ghost mr-1 py-0.5 text-xs" onClick={() => setTiles([...tiles, t])}>
              + {tileDefs.get(t)?.title}
            </button>
          ))}
        </p>
      )}

      <div className="grid gap-4 lg:grid-cols-2" data-testid="dashboard-tiles">
        {tiles.map((id, i) => {
          const t = tileDefs.get(id)
          if (!t) return null
          return (
            <div key={id} className="min-w-0">
              <Section
                icon={sectionIcons[t.title] ?? IconDashboard}
                title={t.title}
                description={t.description}
                actions={
                  editing ? (
                    <div className="flex gap-1">
                      <button className="btn-ghost py-0.5 text-xs" aria-label={`Move ${t.title} up`} onClick={() => move(i, -1)}>
                        ↑
                      </button>
                      <button className="btn-ghost py-0.5 text-xs" aria-label={`Move ${t.title} down`} onClick={() => move(i, 1)}>
                        ↓
                      </button>
                      <button
                        className="btn-ghost py-0.5 text-xs"
                        aria-label={`Hide ${t.title}`}
                        onClick={() => setTiles(tiles.filter((x) => x !== id))}
                      >
                        Hide
                      </button>
                    </div>
                  ) : undefined
                }
              >
                <TileBody tile={t} data={data} channel={channel} />
              </Section>
            </div>
          )
        })}
      </div>

      {def?.planned && def.planned.length > 0 && (
        <Section title="Not measured yet" description="What this dashboard needs that Perfuse does not record yet. Shown here rather than drawn as empty tiles.">
          <ul className="list-inside list-disc text-sm text-slate-400">
            {def.planned.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        </Section>
      )}
    </div>
  )
}

function sorted(rec: Record<string, number> | undefined, only?: string) {
  return Object.entries(rec ?? {})
    .filter(([k, n]) => n > 0 && (!only || k === only))
    .sort((a, b) => b[1] - a[1])
}

function Rows({ rows, empty, unit = '' }: { rows: [string, number | string][]; empty: string; unit?: string }) {
  if (rows.length === 0) return <p className="text-sm text-slate-500">{empty}</p>
  return (
    <table className="w-full text-sm">
      <tbody>
        {rows.map(([k, v]) => (
          <tr key={k} className="border-b border-slate-800/60 last:border-0">
            <td className="py-1 pr-3 text-slate-300">{k}</td>
            <td className="py-1 text-right font-mono text-slate-200">
              {v}
              {unit}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function age(seconds: number) {
  if (seconds < 90) return `${Math.round(seconds)}s`
  if (seconds < 5400) return `${Math.round(seconds / 60)} min`
  return `${(seconds / 3600).toFixed(1)} h`
}

function TileBody({ tile, data, channel }: { tile: DashboardTile; data: Data; channel: string }) {
  const failed = data.failures[tile.source] ?? (tile.source === 'destinations' ? data.failures.stats : undefined)
  if (failed) return <p className="text-sm text-amber-300">Unavailable: {failed}</p>
  if (tile.source === 'figures') return <FigureBody figure={data.figures?.[tile.id]} />
  switch (tile.id) {
    case 'channels': {
      const st = data.status
      if (!st) return <Spinner label="…" />
      return (
        <p className="text-sm text-slate-300">
          <b className="text-2xl text-slate-100">{st.channelRunning}</b> of {st.channelsTotal} running
          {st.channelsBroken ? <span className="ml-2 text-rose-300">{st.channelsBroken} would not load</span> : null}
        </p>
      )
    }
    case 'inbound': {
      const rows = (data.status?.channels ?? [])
        .filter((c) => !channel || c.name === channel)
        .map((c) => [c.name, c.running ? 'running' : 'stopped'] as [string, string])
      return <Rows rows={rows} empty="No channels." />
    }
    case 'throughput':
    case 'outcomes': {
      const s = data.stats
      if (!s) return <Spinner label="…" />
      const total = s.total || 0
      const rows = Object.entries(s.byOutcome).map(([k, n]) => [k, `${n}${total ? ` (${Math.round((n / total) * 100)}%)` : ''}`] as [string, string])
      return (
        <>
          <p className="mb-2 text-sm text-slate-400">{total} messages in the last 24 hours.</p>
          <Rows rows={rows} empty="Nothing received." />
        </>
      )
    }
    case 'errors-by-channel':
      return <Rows rows={sorted(data.stats?.failedByChannel, channel || undefined)} empty="No failures in 24 hours." />
    case 'naks-by-sender':
      return <Rows rows={sorted(data.stats?.rejectedBySender)} empty="No message was rejected in 24 hours." />
    case 'destinations': {
      const rows = (data.destinations ?? []).map(
        (d) => [d.destination, `${d.delivered} delivered, ${d.failed} failed, ${Math.round(d.avgDurationMs)} ms`] as [string, string],
      )
      return <Rows rows={rows} empty="No deliveries in 24 hours." />
    }
    case 'queue': {
      const rows = (data.queue?.destinations ?? [])
        .filter((q) => !channel || q.channel === channel)
        .filter((q) => q.pending + q.failed > 0)
        .map((q) => [`${q.channel} → ${q.destination}`, `${q.pending} waiting, oldest ${age(q.oldestSeconds)}`] as [string, string])
      return <Rows rows={rows} empty="Nothing is waiting." />
    }
    case 'alerts': {
      const firing = data.alerts?.firing ?? []
      if (!data.alerts) return <Spinner label="…" />
      if (!firing.length) return <p className="text-sm text-emerald-300">Nothing is firing.</p>
      return (
        <ul className="space-y-1 text-sm">
          {firing.map((a) => (
            <li key={a.key} className={a.severity === 'critical' ? 'text-rose-300' : 'text-amber-300'}>
              {a.summary} <span className="text-xs text-slate-500">since {new Date(a.firingSince).toLocaleTimeString()}</span>
            </li>
          ))}
        </ul>
      )
    }
    case 'connections':
      return <Connections checks={data.connections} />
    case 'certificates': {
      const c = data.certificates
      if (!c) return <Spinner label="…" />
      return (
        <p className="text-sm text-slate-300">
          {c.endpoints.length} encrypted endpoints. <span className={c.expired ? 'text-rose-300' : ''}>{c.expired} expired</span>,{' '}
          <span className={c.expiring ? 'text-amber-300' : ''}>{c.expiring} expiring soon</span>.
        </p>
      )
    }
    case 'audit': {
      const rows = (data.audit ?? []).slice(0, 10).map((e) => [`${new Date(e.at).toLocaleString()} ${e.username}`, e.action] as [string, string])
      return <Rows rows={rows} empty="No recent entries." />
    }
    case 'plain-summary':
      return <Summary data={data} />
    case 'fhir-validation':
      return <p className="text-sm text-slate-400">FHIR validation counts are in Metrics, and in the Grafana export of this dashboard.</p>
  }
  return null
}

const toneClass = { ok: 'text-emerald-300', warn: 'text-amber-300', bad: 'text-rose-300' }

function FigureBody({ figure }: { figure?: DashboardFigure }) {
  if (!figure) return <Spinner label="…" />
  if (figure.unavailable) return <p className="text-sm text-amber-300">Unavailable: {figure.unavailable}</p>
  return (
    <div className="space-y-2" data-testid="dashboard-figure">
      {figure.rows.length === 0 ? (
        <p className="text-sm text-slate-400">{figure.empty ?? 'Nothing to show.'}</p>
      ) : (
        <table className="w-full text-sm">
          <tbody>
            {figure.rows.map((r, i) => (
              <tr key={i} className="border-t border-slate-800 first:border-0">
                <td className="whitespace-pre py-1 pr-3 text-slate-300">{r.label}</td>
                <td className={`py-1 text-right font-mono ${r.tone ? toneClass[r.tone] : 'text-slate-200'}`}>{r.value}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {figure.note && <p className="text-xs text-slate-500">{figure.note}</p>}
    </div>
  )
}

function Connections({ checks }: { checks?: ConnectionCheck[] }) {
  if (!checks) return <Spinner label="Checking…" />
  if (checks.length === 0) return <p className="text-sm text-slate-500">No destination reaches over the network.</p>
  const colour = { ok: 'text-emerald-300', degraded: 'text-amber-300', down: 'text-rose-300' }
  return (
    <ul className="space-y-3" data-testid="connection-checks">
      {checks.map((c) => (
        <li key={c.channel + c.destination} className="text-sm">
          <p>
            <span className={`font-medium ${colour[c.verdict]}`}>{c.verdict}</span>{' '}
            <span className="text-slate-200">
              {c.channel} → {c.destination}
            </span>{' '}
            <code className="text-xs text-slate-500">
              {c.type} {c.target}
            </code>
          </p>
          <p className="text-slate-400">{c.reason}</p>
          <p className="mt-0.5 font-mono text-xs text-slate-500">
            {(['dns', 'tcp', 'tlsHandshake', 'application'] as const).map((k) => (
              <span key={k} className="mr-3">
                {k === 'tlsHandshake' ? 'tls' : k === 'application' ? 'delivery' : k}: {c[k].state}
                {c[k].ms ? ` ${c[k].ms?.toFixed(0)}ms` : ''}
              </span>
            ))}
          </p>
        </li>
      ))}
    </ul>
  )
}

/** Summary writes one line per problem for somebody who does not run interfaces. */
function Summary({ data }: { data: Data }) {
  const lines: string[] = []
  for (const a of data.alerts?.firing ?? []) lines.push(a.summary)
  for (const [ch, n] of sorted(data.stats?.failedByChannel)) lines.push(`${ch}: ${n} messages failed in the last day.`)
  if (data.status?.channelsBroken) lines.push(`${data.status.channelsBroken} interface files would not load, so those interfaces are not running.`)
  if (!data.alerts && !data.stats) return <Spinner label="…" />
  if (lines.length === 0) return <p className="text-sm text-emerald-300">Everything is working.</p>
  return (
    <ul className="list-inside list-disc space-y-1 text-sm text-slate-300" data-testid="plain-summary">
      {lines.map((l, i) => (
        <li key={i}>{l}</li>
      ))}
    </ul>
  )
}
