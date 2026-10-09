import { useCallback, useEffect, useState } from 'react'
import { api } from './api'
import type { SMARTClient, SMARTDirectory, SMARTUser } from './api'
import type { UiError } from './store'
import { toUiError } from './store'
import { ErrorBox, Field, Section } from './ui'
import { useCopy } from './useCopy'

/** splitList reads a comma- or space-separated list from one input. */
export function splitList(v: string): string[] {
  return v
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

const kindText: Record<SMARTClient['kind'], string> = {
  public: 'Public: a browser or phone app, PKCE and no secret',
  'confidential-symmetric': 'Confidential with a client secret',
  'confidential-asymmetric': 'Confidential with a signed JWT (its public keys)',
  backend: 'Backend services: a system, no person signs in',
}

const emptyClient = { id: '', name: '', kind: 'public' as SMARTClient['kind'], scopes: '', redirects: '', launch: '', jwksUri: '', secret: '' }
const emptyUser = { username: '', name: '', fhirUser: '', oidcSubject: '', password: '' }

/** SMARTApps edits the built-in SMART authorization server's registered apps and the people who sign in to authorize them. */
export function SMARTApps() {
  const [dir, setDir] = useState<SMARTDirectory | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [client, setClient] = useState(emptyClient)
  const [user, setUser] = useState(emptyUser)
  const [secret, setSecret] = useState<{ id: string; secret: string } | null>(null)
  const [saved, setSaved] = useState('')
  const { copy, label: copyLabel } = useCopy()

  const load = useCallback(async () => {
    try {
      setDir(await api.smartDirectory())
    } catch (err) {
      setError(toUiError(err))
    }
  }, [])
  useEffect(() => {
    void load()
  }, [load])

  if (dir && !dir.enabled) return null
  if (!dir) return error ? <ErrorBox error={error} /> : null

  const saveClient = async () => {
    setError(null)
    try {
      const res = await api.saveSMARTClient(client.id.trim(), {
        name: client.name,
        kind: client.kind,
        scopes: splitList(client.scopes),
        redirectUris: splitList(client.redirects),
        launchUrl: client.launch,
        jwksUri: client.jwksUri,
        secret: client.secret || undefined,
      })
      if (res.secret) setSecret({ id: res.id, secret: res.secret })
      setSaved(`Saved app ${res.id}.`)
      setClient(emptyClient)
      await load()
    } catch (err) {
      setError(toUiError(err))
    }
  }

  const editClient = (c: SMARTClient) =>
    setClient({
      id: c.id,
      name: c.name ?? '',
      kind: c.kind,
      scopes: c.scopes.join(' '),
      redirects: (c.redirectUris ?? []).join(' '),
      launch: c.launchUrl ?? '',
      jwksUri: c.jwksUri ?? '',
      secret: '',
    })

  const saveUser = async () => {
    setError(null)
    try {
      const res = await api.saveSMARTUser(user.username.trim(), {
        name: user.name,
        fhirUser: user.fhirUser,
        oidcSubject: user.oidcSubject,
        password: user.password || undefined,
      })
      setSaved(`Saved ${res.username}.`)
      setUser(emptyUser)
      await load()
    } catch (err) {
      setError(toUiError(err))
    }
  }

  const editUser = (u: SMARTUser) =>
    setUser({ username: u.username, name: u.name ?? '', fhirUser: u.fhirUser, oidcSubject: u.oidcSubject ?? '', password: '' })

  const remove = async (what: 'client' | 'user', id: string) => {
    if (!window.confirm(`Remove ${id}? Its tokens and refresh tokens stop working.`)) return
    setError(null)
    try {
      if (what === 'client') await api.deleteSMARTClient(id)
      else await api.deleteSMARTUser(id)
      setSaved(`Removed ${id}.`)
      await load()
    } catch (err) {
      setError(toUiError(err))
    }
  }

  const needsSecret = client.kind === 'confidential-symmetric'
  const needsKeys = client.kind === 'confidential-asymmetric' || client.kind === 'backend'
  const signsIn = client.kind !== 'backend'

  return (
    <div className="space-y-6">
      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      {saved && (
        <p role="status" className="text-sm text-emerald-300">
          {saved}
        </p>
      )}
      <Section
        title="SMART apps"
        description={`Apps registered with this server's SMART authorization server at ${dir.issuer}. Saved to the clients file; comments in it are not kept.`}
      >
        <div className="space-y-4">
          {secret && (
            <div className="rounded-lg border border-emerald-700 bg-emerald-950/40 p-4">
              <p className="text-sm text-emerald-200">Client secret for {secret.id}. It is shown this once.</p>
              <p data-testid="smart-secret" className="mt-2 break-all rounded bg-slate-950 px-3 py-2 font-mono text-xs text-slate-100">
                {secret.secret}
              </p>
              <div className="mt-3 flex gap-2">
                <button className="btn" onClick={() => void copy(secret.secret)}>
                  {copyLabel ?? 'Copy'}
                </button>
                <button className="btn" onClick={() => setSecret(null)}>
                  I have saved it
                </button>
              </div>
            </div>
          )}
          <table className="min-w-full text-sm" data-testid="smart-clients">
            <thead>
              <tr className="border-b border-slate-800 text-left text-xs text-slate-400">
                <th className="py-2 pr-4">App</th>
                <th className="py-2 pr-4">Kind</th>
                <th className="py-2 pr-4">Scopes</th>
                <th className="py-2 pr-4"></th>
              </tr>
            </thead>
            <tbody>
              {(dir.clients ?? []).map((c) => (
                <tr key={c.id} className="border-b border-slate-800/60 align-top">
                  <td className="py-2 pr-4">
                    <span className="font-mono text-xs text-sky-300">{c.id}</span>
                    {c.name && <span className="block text-xs text-slate-400">{c.name}</span>}
                  </td>
                  <td className="py-2 pr-4 text-xs text-slate-300">{c.kind}</td>
                  <td className="py-2 pr-4 font-mono text-xs text-slate-400">{c.scopes.join(' ')}</td>
                  <td className="py-2 pr-4 whitespace-nowrap">
                    <button className="btn" onClick={() => editClient(c)}>
                      Edit {c.id}
                    </button>{' '}
                    <button className="btn" onClick={() => void remove('client', c.id)}>
                      Remove {c.id}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="grid gap-3 md:grid-cols-2">
            <Field label="App id" hint="client_id; saving an existing id replaces that app">
              <input className="input" value={client.id} onChange={(e) => setClient({ ...client, id: e.target.value })} />
            </Field>
            <Field label="App name" hint="Shown on the sign-in and consent pages">
              <input className="input" value={client.name} onChange={(e) => setClient({ ...client, name: e.target.value })} />
            </Field>
            <Field label="Kind">
              <select
                className="input"
                value={client.kind}
                onChange={(e) => setClient({ ...client, kind: e.target.value as SMARTClient['kind'] })}
              >
                {Object.entries(kindText).map(([k, t]) => (
                  <option key={k} value={k}>
                    {t}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Scopes" hint="The most it may be granted, separated by spaces; one ending in * covers a prefix">
              <input
                className="input font-mono"
                value={client.scopes}
                placeholder="openid fhirUser launch/patient patient/*.rs"
                onChange={(e) => setClient({ ...client, scopes: e.target.value })}
              />
            </Field>
            {signsIn && (
              <Field label="Redirect URIs" hint="Exactly as the app sends them, separated by spaces">
                <input className="input" value={client.redirects} onChange={(e) => setClient({ ...client, redirects: e.target.value })} />
              </Field>
            )}
            {signsIn && (
              <Field label="Launch URL" hint="For an EHR launch from /auth/launch; optional">
                <input className="input" value={client.launch} onChange={(e) => setClient({ ...client, launch: e.target.value })} />
              </Field>
            )}
            {needsKeys && (
              <Field label="JWKS URI" hint="Where its public keys are published (https)">
                <input className="input" value={client.jwksUri} onChange={(e) => setClient({ ...client, jwksUri: e.target.value })} />
              </Field>
            )}
            {needsSecret && (
              <Field label="Client secret" hint="Leave empty to keep the current one, or to have one generated for a new app">
                <input
                  className="input"
                  type="password"
                  autoComplete="new-password"
                  value={client.secret}
                  onChange={(e) => setClient({ ...client, secret: e.target.value })}
                />
              </Field>
            )}
          </div>
          <div className="flex gap-2">
            <button className="btn-primary" disabled={!client.id.trim()} onClick={() => void saveClient()}>
              Save app
            </button>
            <button className="btn" onClick={() => setClient(emptyClient)}>
              Clear
            </button>
          </div>
        </div>
      </Section>

      {dir.signIn && (
        <Section
          title="People who authorize apps"
          description={
            dir.upstream
              ? `They sign in with a password, or at ${dir.upstream.label} when linked by its subject (register ${dir.upstream.redirectUri} there).`
              : 'They sign in with a password. Start the server with -smart-oidc to let them sign in at your identity provider instead.'
          }
        >
          <div className="space-y-4">
            <table className="min-w-full text-sm" data-testid="smart-users">
              <thead>
                <tr className="border-b border-slate-800 text-left text-xs text-slate-400">
                  <th className="py-2 pr-4">Person</th>
                  <th className="py-2 pr-4">FHIR user</th>
                  <th className="py-2 pr-4">Signs in with</th>
                  <th className="py-2 pr-4"></th>
                </tr>
              </thead>
              <tbody>
                {(dir.users ?? []).map((u) => (
                  <tr key={u.username} className="border-b border-slate-800/60">
                    <td className="py-2 pr-4">
                      <span className="font-mono text-xs text-sky-300">{u.username}</span>
                      {u.name && <span className="block text-xs text-slate-400">{u.name}</span>}
                    </td>
                    <td className="py-2 pr-4 font-mono text-xs text-violet-300">{u.fhirUser}</td>
                    <td className="py-2 pr-4 text-xs text-slate-300">
                      {[u.hasPassword && 'password', u.oidcSubject && `${dir.upstream?.label ?? 'identity provider'} (${u.oidcSubject})`]
                        .filter(Boolean)
                        .join(', ')}
                    </td>
                    <td className="py-2 pr-4 whitespace-nowrap">
                      <button className="btn" onClick={() => editUser(u)}>
                        Edit {u.username}
                      </button>{' '}
                      <button className="btn" onClick={() => void remove('user', u.username)}>
                        Remove {u.username}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <div className="grid gap-3 md:grid-cols-2">
              <Field label="Username">
                <input className="input" value={user.username} onChange={(e) => setUser({ ...user, username: e.target.value })} />
              </Field>
              <Field label="Full name">
                <input className="input" value={user.name} onChange={(e) => setUser({ ...user, name: e.target.value })} />
              </Field>
              <Field label="FHIR user" hint="Patient/<id> (only their own record), Practitioner/<id>, PractitionerRole/<id> or RelatedPerson/<id>">
                <input className="input font-mono" value={user.fhirUser} onChange={(e) => setUser({ ...user, fhirUser: e.target.value })} />
              </Field>
              <Field label="Identity provider subject" hint="The ID token's sub at the -smart-oidc provider; optional">
                <input className="input font-mono" value={user.oidcSubject} onChange={(e) => setUser({ ...user, oidcSubject: e.target.value })} />
              </Field>
              <Field label="New password" hint="At least 12 characters; leave empty to keep the current one">
                <input
                  className="input"
                  type="password"
                  autoComplete="new-password"
                  value={user.password}
                  onChange={(e) => setUser({ ...user, password: e.target.value })}
                />
              </Field>
            </div>
            <div className="flex gap-2">
              <button className="btn-primary" disabled={!user.username.trim()} onClick={() => void saveUser()}>
                Save person
              </button>
              <button className="btn" onClick={() => setUser(emptyUser)}>
                Clear
              </button>
            </div>
          </div>
        </Section>
      )}
    </div>
  )
}
