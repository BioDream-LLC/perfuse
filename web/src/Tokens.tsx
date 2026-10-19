import { useCallback, useEffect, useState } from 'react'
import type { ApiToken, MCPStatus, NewApiToken } from './api'
import { api } from './api'
import type { UiError } from './store'
import { toUiError } from './store'
import { ErrorBox, Field, Section } from './ui'
import { useCopy } from './useCopy'
import { uiLocale } from './i18n/locale'

/** Tokens manages the credentials machines use.
 *
 *  A fleet poll, a monitoring script, another Perfuse instance and - since the FHIR server
 *  gained authentication - a FHIR client all authenticate with one of these.
 *
 *  They could only be created from the command line, which meant reaching a shell on the
 *  server to let a monitoring system read a dashboard. That is worse than inconvenient: it
 *  pushes people towards giving a script a person's password, which is the thing tokens
 *  exist to avoid. */
export function Tokens() {
  const { copy, label: copyLabel } = useCopy()

  const [tokens, setTokens] = useState<ApiToken[]>([])
  const [error, setError] = useState<UiError | null>(null)
  const [loading, setLoading] = useState(true)

  const [label, setLabel] = useState('')
  const [role, setRole] = useState('viewer')
  const [groups, setGroups] = useState('')
  const [channelGroups, setChannelGroups] = useState('')
  const [mcpWrite, setMcpWrite] = useState(false)
  const [mcp, setMcp] = useState<MCPStatus | null>(null)
  const [creating, setCreating] = useState(false)

  // Held in state rather than shown in the list, because this is the only time the value
  // exists anywhere outside the caller's memory.
  const [issued, setIssued] = useState<NewApiToken | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.tokens()
      setTokens(res.tokens ?? [])
      setMcp(await api.mcpStatus().catch(() => null))
      setError(null)
    } catch (err) {
      setError(toUiError(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const create = async () => {
    setCreating(true)
    setError(null)
    try {
      const limit = groups
        .split(',')
        .map((g) => g.trim())
        .filter(Boolean)
      const channels = channelGroups
        .split(',')
        .map((g) => g.trim())
        .filter(Boolean)
      const res = await api.createToken(label.trim(), role, limit, channels, mcpWrite ? ['mcp:write'] : [])
      setIssued(res)
      setLabel('')
      setGroups('')
      setChannelGroups('')
      setMcpWrite(false)
      await load()
    } catch (err) {
      setError(toUiError(err))
    } finally {
      setCreating(false)
    }
  }

  const revoke = async (tokenLabel: string) => {
    setError(null)
    try {
      await api.revokeToken(tokenLabel)
      await load()
    } catch (err) {
      setError(toUiError(err))
    }
  }

  return (
    <Section
      title="Machine credentials"
      description="Tokens let a monitoring system, another Perfuse server or a FHIR client authenticate without a person's password. Each one carries a role, and that role is the most it can do."
    >
      <div className="space-y-4">
        {error && <ErrorBox error={error} />}

        {issued && (
          <div className="rounded-lg border border-emerald-300 bg-emerald-50 p-4">
            <p className="text-sm font-medium text-emerald-900">
              Token issued for {issued.label} ({issued.role})
            </p>
            <p
              data-testid="issued-token"
              className="mt-2 break-all rounded border border-slate-700 bg-slate-950 px-3 py-2 font-mono text-xs text-slate-100"
            >
              {issued.token}
            </p>
            <p className="mt-2 text-xs text-emerald-900">{issued.note}</p>
            <div className="mt-3 flex gap-2">
              <button
                className="btn"
                onClick={() => {
                  // The value stays on screen either way, because this console is frequently reached over
                  // plain HTTP inside a hospital network where the clipboard API is unavailable. What
                  // changed: the outcome is reported. A token that silently failed to copy is one somebody
                  // pastes wrongly into a configuration and then debugs as an authentication fault.
                  void copy(issued.token)
                }}
              >
                {copyLabel ?? 'Copy'}
              </button>
              <button className="btn" onClick={() => setIssued(null)}>
                I have saved it
              </button>
            </div>
          </div>
        )}

        <div className="flex flex-wrap items-end gap-3">
          <Field
            label="What is it for"
            hint="The only handle you will have on this token afterwards"
          >
            <input
              className="input"
              value={label}
              placeholder="lab-system-fhir"
              onChange={(e) => setLabel(e.target.value)}
            />
          </Field>

          <Field label="Role" hint="The most this token can do">
            <select className="input" value={role} onChange={(e) => setRole(e.target.value)}>
              <option value="monitor">monitor — status and counts, no message contents</option>
              <option value="viewer">viewer — read only</option>
              <option value="operator">operator — also resend and retry</option>
              <option value="editor">editor — change channels</option>
              <option value="admin">admin — start and stop channels, manage users</option>
            </select>
          </Field>

          <Field
            label="Limit to FHIR Groups"
            hint="For a provider's CMS-0057 Provider Access token: comma-separated Group ids. The token can then read those Groups and export them, and nothing else on the FHIR endpoint. Leave empty for no limit."
          >
            <input className="input" value={groups} placeholder="riverside-attributed" onChange={(e) => setGroups(e.target.value)} />
          </Field>

          <Field
            label="Limit to channel groups"
            hint="Comma-separated: the token sees and acts on the channels filed under these groups only, like a person limited to them. Leave empty for every channel (an administrator limited to groups always gives their own)."
          >
            <input
              className="input"
              value={channelGroups}
              placeholder="every channel"
              onChange={(e) => setChannelGroups(e.target.value)}
            />
          </Field>

          <label className="flex max-w-xs items-start gap-2 text-sm text-slate-300">
            <input type="checkbox" className="mt-1" checked={mcpWrite} onChange={(e) => setMcpWrite(e.target.checked)} />
            <span>
              Let an AI agent act with it
              <span className="block text-xs text-slate-500">
                The mcp:write scope: retry, reprocess, start and stop through MCP, up to the role. Without it an agent only reads.
              </span>
            </span>
          </label>

          <button
            className="btn-primary"
            disabled={creating || label.trim() === ''}
            onClick={() => void create()}
          >
            {creating ? 'Issuing…' : 'Issue token'}
          </button>
        </div>

        <p className="text-xs text-slate-500">
          A fleet poll only reads, so give it <strong>viewer</strong>. A FHIR client that writes
          resources needs <strong>editor</strong>. Nothing needs admin unless it starts and stops
          channels.
        </p>

        {loading && tokens.length === 0 ? (
          <p className="text-sm text-slate-500">Loading…</p>
        ) : tokens.length === 0 ? (
          <p className="text-sm text-slate-500">
            No tokens yet. Nothing is using one to authenticate.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="min-w-full text-sm">
              <thead>
                <tr className="border-b border-slate-800 text-left text-xs text-slate-400">
                  <th className="py-2 pr-4">What for</th>
                  <th className="py-2 pr-4">Role</th>
                  <th className="py-2 pr-4">Issued</th>
                  <th className="py-2 pr-4">Last used</th>
                  <th className="py-2 pr-4"></th>
                </tr>
              </thead>
              <tbody>
                {tokens.map((t) => (
                  <tr
                    key={t.label}
                    className={t.revoked ? 'border-b border-slate-100 text-slate-400' : 'border-b border-slate-100'}
                  >
                    <td className="py-2 pr-4 font-mono text-xs">{t.label}</td>
                    <td className="py-2 pr-4">
                      {t.role}
                      {t.channelGroups && t.channelGroups.length > 0 && (
                        <span className="block text-xs text-slate-400">
                          Channel groups: {t.channelGroups.join(', ')}
                        </span>
                      )}
                      {t.scopes && t.scopes.includes('mcp:write') && (
                        <span className="block text-xs text-amber-300">AI agents may act (mcp:write)</span>
                      )}
                      {t.fhirGroups && t.fhirGroups.length > 0 && (
                        <span className="block text-xs text-slate-400">FHIR Groups: {t.fhirGroups.join(', ')}</span>
                      )}
                    </td>
                    <td className="py-2 pr-4 text-xs">
                      {t.createdAt ? new Date(t.createdAt).toLocaleDateString(uiLocale()) : '—'}
                      {t.createdBy ? ` by ${t.createdBy}` : ''}
                    </td>
                    <td className="py-2 pr-4 text-xs">
                      {/* Never used is worth seeing plainly: it is either a token nobody
                          wired up, or one that was replaced and forgotten. */}
                      {t.lastUsed ? new Date(t.lastUsed).toLocaleString(uiLocale()) : 'never'}
                    </td>
                    <td className="py-2 pr-4 text-right">
                      {t.revoked ? (
                        <span className="text-xs">withdrawn</span>
                      ) : (
                        <button className="btn text-xs" onClick={() => void revoke(t.label)}>
                          Revoke
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {mcp && (
          <div className="rounded-lg border border-slate-800 p-4" data-testid="mcp-card">
            <p className="text-sm font-medium text-slate-100">
              AI agents (MCP){' '}
              <span className={mcp.enabled ? 'text-emerald-300' : 'text-slate-400'}>{mcp.enabled ? 'on' : 'off'}</span>
            </p>
            {mcp.enabled ? (
              <>
                <p className="mt-1 text-xs text-slate-400">
                  An agent connects to <code className="font-mono text-cyan-300">{mcp.url}</code> (Model Context Protocol {mcp.protocol}) with one
                  of these tokens as a bearer token, and gets that token&apos;s access: its role, channel groups and tenant. At most{' '}
                  {mcp.callsPerMinute} tool calls a minute per token; every call is in Activity.
                </p>
                <pre className="mt-2 overflow-auto rounded bg-slate-950 p-2 font-mono text-xs text-slate-300">
                  {JSON.stringify({ mcpServers: { perfuse: { type: 'http', url: mcp.url, headers: { Authorization: 'Bearer <token>' } } } }, null, 2)}
                </pre>
                <ul className="mt-2 grid gap-1 text-xs sm:grid-cols-2">
                  {mcp.tools.map((t) => (
                    <li key={t.name}>
                      <span className={`font-mono ${t.writes ? 'text-amber-300' : 'text-slate-200'}`}>{t.name}</span>{' '}
                      <span className="text-slate-500">
                        {t.role}
                        {t.writes ? ' + mcp:write' : ''}
                      </span>
                    </li>
                  ))}
                </ul>
              </>
            ) : (
              <p className="mt-1 text-xs text-slate-400">Switch it on under Settings › Security › AI agents (MCP).</p>
            )}
          </div>
        )}

        <p className="text-xs text-slate-500">
          Revoking withdraws a token immediately and keeps the row, so the activity log's
          reference to it still resolves to something. Only a hash of each value is stored, so a
          token cannot be shown again — a replacement means issuing a new one.
        </p>
      </div>
    </Section>
  )
}
