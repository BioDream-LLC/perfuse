import { useCallback, useEffect, useState } from 'react'
import { api } from './api'
import type { Tenant, TenantLimits } from './api'
import { toUiError } from './store'
import type { UiError } from './store'
import { ErrorBox, Field, Section, Spinner } from './ui'

/** A limit as typed: empty is no limit, which the server stores as 0. */
function limitText(n: number | undefined): string {
  return n ? String(n) : ''
}

function parseLimit(text: string): number | null {
  const t = text.trim()
  if (t === '') return 0
  const n = Number(t)
  return Number.isInteger(n) && n >= 0 ? n : null
}

/**
 * Tenants is the platform operator's list of organisations on this server, with each one's limits.
 *
 * Limits keep one customer from crowding out the rest of a shared server. Each refuses new work and says why; none deletes
 * anything. Over the message rate or the storage limit, received messages are refused the way a busy receiver refuses them
 * (no MLLP acknowledgement, HTTP 429), so senders resend.
 */
export function Tenants() {
  const [data, setData] = useState<{ tenants: Tenant[]; channelCounts: Record<string, number>; storedMB: Record<string, number> } | null>(
    null,
  )
  const [error, setError] = useState<UiError | null>(null)
  const [id, setId] = useState('')
  const [name, setName] = useState('')

  const load = useCallback(async () => {
    try {
      setData(await api.listTenants())
    } catch (err) {
      setError(toUiError(err))
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function create() {
    setError(null)
    try {
      await api.createTenant(id.trim(), name.trim())
      setId('')
      setName('')
      await load()
    } catch (err) {
      setError(toUiError(err))
    }
  }

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-lg font-semibold text-slate-100">Tenants</h1>
        <p className="mt-1 text-sm text-slate-500">
          The organisations on this server. Each has its own channels, people and messages, and none can see another&apos;s.
          Limits keep one from crowding out the rest; leave a limit empty for none. A limit refuses new work and says why:
          over the message rate or the storage limit, received messages are turned away the way a busy receiver turns them
          away, so senders resend. Nothing stored is deleted.
        </p>
      </div>

      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

      <Section title="Add a tenant">
        <form
          className="grid gap-4 sm:grid-cols-3"
          onSubmit={(e) => {
            e.preventDefault()
            void create()
          }}
        >
          <Field label="Id" hint="Lower case letters, digits and hyphens. It names a directory and cannot be changed.">
            <input className="input" value={id} placeholder="riverside" onChange={(e) => setId(e.target.value)} />
          </Field>
          <Field label="Name">
            <input className="input" value={name} placeholder="Riverside Clinics" onChange={(e) => setName(e.target.value)} />
          </Field>
          <div className="flex items-end">
            <button className="btn-primary w-full" disabled={!id.trim() || !name.trim()}>
              Add tenant
            </button>
          </div>
        </form>
      </Section>

      {!data && !error && <Spinner label="Loading tenants…" />}

      {data && (
        <div className="card overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b border-slate-800 bg-slate-900/60 text-left">
              <tr className="text-xs tracking-wide text-slate-500 uppercase">
                <th className="px-5 py-3">Tenant</th>
                <th className="px-5 py-3">Channels</th>
                <th className="px-5 py-3">Messages a minute</th>
                <th className="px-5 py-3">Storage (MB)</th>
                <th className="px-5 py-3 text-right">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-800">
              {data.tenants.map((t) => (
                <TenantRow
                  key={t.id}
                  tenant={t}
                  channels={data.channelCounts[t.id] ?? 0}
                  storedMB={data.storedMB[t.id] ?? 0}
                  onSaved={load}
                  onError={setError}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function TenantRow({
  tenant,
  channels,
  storedMB,
  onSaved,
  onError,
}: {
  tenant: Tenant
  channels: number
  storedMB: number
  onSaved: () => Promise<void>
  onError: (e: UiError | null) => void
}) {
  const l = tenant.limits ?? {}
  const [maxChannels, setMaxChannels] = useState(limitText(l.maxChannels))
  const [perMinute, setPerMinute] = useState(limitText(l.maxMessagesPerMinute))
  const [storage, setStorage] = useState(limitText(l.maxStorageMB))
  const [saving, setSaving] = useState(false)

  const parsed = [parseLimit(maxChannels), parseLimit(perMinute), parseLimit(storage)]
  const valid = parsed.every((n) => n !== null)
  const changed =
    parsed[0] !== (l.maxChannels ?? 0) || parsed[1] !== (l.maxMessagesPerMinute ?? 0) || parsed[2] !== (l.maxStorageMB ?? 0)

  async function save(patch: { disabled?: boolean; limits?: TenantLimits }) {
    setSaving(true)
    onError(null)
    try {
      await api.updateTenant(tenant.id, {
        name: tenant.name,
        notes: tenant.notes ?? '',
        disabled: patch.disabled ?? tenant.disabled,
        limits: patch.limits,
      })
      await onSaved()
    } catch (err) {
      onError(toUiError(err))
    } finally {
      setSaving(false)
    }
  }

  const over = (used: number, max: number | undefined) =>
    max && used >= max ? 'text-amber-300' : 'text-slate-500'

  return (
    <tr className={tenant.disabled ? 'opacity-60' : ''}>
      <td className="px-5 py-3">
        <span className="font-medium text-slate-200">{tenant.name}</span>
        <span className="ml-2 font-mono text-xs text-slate-500">{tenant.id}</span>
        {tenant.disabled && <span className="ml-2 text-xs text-rose-400">disabled</span>}
      </td>
      <td className="px-5 py-3">
        <input
          aria-label={`Channel limit for ${tenant.name}`}
          className="input w-20 py-1 text-xs"
          inputMode="numeric"
          value={maxChannels}
          placeholder="none"
          onChange={(e) => setMaxChannels(e.target.value)}
        />
        <span className={`ml-2 text-xs ${over(channels, l.maxChannels)}`}>{channels} now</span>
      </td>
      <td className="px-5 py-3">
        <input
          aria-label={`Message rate limit for ${tenant.name}`}
          className="input w-24 py-1 text-xs"
          inputMode="numeric"
          value={perMinute}
          placeholder="none"
          onChange={(e) => setPerMinute(e.target.value)}
        />
      </td>
      <td className="px-5 py-3">
        <input
          aria-label={`Storage limit in megabytes for ${tenant.name}`}
          className="input w-24 py-1 text-xs"
          inputMode="numeric"
          value={storage}
          placeholder="none"
          onChange={(e) => setStorage(e.target.value)}
        />
        <span className={`ml-2 text-xs ${over(storedMB, l.maxStorageMB)}`}>{storedMB.toFixed(1)} now</span>
      </td>
      <td className="px-5 py-3">
        <div className="flex justify-end gap-2">
          {!valid && <span className="self-center text-xs text-rose-300">Whole numbers only</span>}
          <button
            className="btn-primary py-1 text-xs"
            disabled={saving || !valid || !changed}
            onClick={() =>
              void save({
                limits: { maxChannels: parsed[0] ?? 0, maxMessagesPerMinute: parsed[1] ?? 0, maxStorageMB: parsed[2] ?? 0 },
              })
            }
          >
            Save limits
          </button>
          <button className="btn-ghost py-1 text-xs" disabled={saving} onClick={() => void save({ disabled: !tenant.disabled })}>
            {tenant.disabled ? 'Enable' : 'Disable'}
          </button>
        </div>
      </td>
    </tr>
  )
}
