import { useCallback, useEffect, useState } from 'react'
import type { Passkey } from './api'
import { api } from './api'
import {
  createPasskey,
  explainPasskeyError,
  passkeysSupported,
  platformAuthenticatorAvailable,
} from './passkey'
import type { UiError } from './store'
import { toUiError } from './store'
import { ErrorBox, Field, Section } from './ui'

/** Passkeys manages a person's own sign-in credentials.
 *
 *  Only their own, deliberately. An administrator removing somebody else's passkey could lock
 *  them out or push them back onto a password; the account-management path for that is disabling
 *  the account, which is visible and audited as what it is. */
export function Passkeys() {
  const [passkeys, setPasskeys] = useState<Passkey[]>([])
  const [error, setError] = useState<UiError | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [label, setLabel] = useState('')
  const [available, setAvailable] = useState<boolean | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [configured, setConfigured] = useState(true)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.passkeys()
      setPasskeys(res.passkeys ?? [])
      // Whether this server offers passkeys at all, which is a different thing from the caller having none and is
      // reported differently below. Absent from older servers, so an undefined value is treated as configured rather
      // than hiding the panel against an installation that supports it.
      setConfigured(res.configured !== false)
      setError(null)
    } catch (err) {
      setError(toUiError(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    // Asked once, because a device does not grow a fingerprint reader while somebody is looking
    // at this page.
    void platformAuthenticatorAvailable().then(setAvailable)
  }, [load])

  const add = async () => {
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      const options = await api.beginPasskeyRegistration()
      const { challenge, response } = await createPasskey(options)
      const created = await api.finishPasskeyRegistration(label.trim(), challenge, response)
      setLabel('')
      setNotice(`Added ${created.label}.`)
      await load()
    } catch (err) {
      // A WebAuthn failure and a server failure read differently, and the browser's own messages
      // are deliberately vague, so they are translated rather than shown raw.
      if (err instanceof Error && err.name && err.name.endsWith('Error') && !('problems' in err)) {
        setError({ message: explainPasskeyError(err), problems: [] })
      } else {
        setError(toUiError(err))
      }
    } finally {
      setBusy(false)
    }
  }

  const remove = async (passkey: Passkey) => {
    setError(null)
    setNotice(null)
    try {
      await api.removePasskey(passkey.id)
      await load()
    } catch (err) {
      setError(toUiError(err))
    }
  }

  if (!passkeysSupported()) {
    return (
      <Section
        title="Passkeys"
        description="Sign in with your fingerprint, face or a security key instead of a password."
      >
        <p className="text-sm text-slate-400">
          This browser does not support passkeys. Any recent version of Chrome, Safari, Edge or
          Firefox does.
        </p>
      </Section>
    )
  }

  return (
    <Section
      title="Passkeys"
      description="Sign in with your fingerprint, face or a security key instead of a password. There is nothing to type, and nothing a fake login page could capture — a passkey only works on the real address."
    >
      <div className="space-y-4">
        {error && <ErrorBox error={error} />}

        {/* Not configured is a fact about the server, so it is said in the ordinary informational style rather than in red.
            
            Red had been doing double duty here: this screen opened with an error message on every installation that had not
            set up passkeys, which is most of them, and that spent the one colour reserved for "something is wrong, and it may
            be something you did" before the user had done anything at all. */}
        {!configured && !error && (
          <p className="rounded-lg border border-slate-700 bg-slate-900/40 px-3 py-2 text-xs leading-relaxed text-slate-300">
            Passkeys are not switched on for this server. An administrator turns them on by starting Perfuse with{' '}
            <code className="text-slate-200">-passkey-rpid</code> set to the hostname people use to reach it. Until then,
            sign in with a password or your organisation's single sign-on.
          </p>
        )}
        {notice && (
          <p className="rounded-lg border border-emerald-300 bg-emerald-50 px-3 py-2 text-sm text-emerald-900">
            {notice}
          </p>
        )}

        {available === false && (
          <p className="rounded-lg border border-amber-800 bg-amber-950/40 px-3 py-2 text-xs leading-relaxed text-amber-200">
            This device has no fingerprint reader, face recognition or device PIN set up. This
            server requires one of those, so you will need a security key — or to set up a screen
            lock on this device first.
          </p>
        )}

        <div className="flex flex-wrap items-end gap-3">
          <Field
            label="What is this device"
            hint="So you can tell your passkeys apart later"
          >
            <input
              className="input"
              value={label}
              placeholder="Work MacBook"
              onChange={(e) => setLabel(e.target.value)}
            />
          </Field>
          <button className="btn-primary" disabled={busy} onClick={() => void add()}>
            {busy ? 'Waiting for your device…' : 'Add a passkey'}
          </button>
        </div>

        {loading && passkeys.length === 0 ? (
          <p className="text-sm text-slate-500">Loading…</p>
        ) : passkeys.length === 0 ? (
          <p className="text-sm text-slate-500">
            No passkeys yet. You are signing in with a password.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="min-w-full text-sm">
              <thead>
                <tr className="border-b border-slate-800 text-left text-xs text-slate-400">
                  <th className="py-2 pr-4">Device</th>
                  <th className="py-2 pr-4">Added</th>
                  <th className="py-2 pr-4">Last used</th>
                  <th className="py-2 pr-4"></th>
                </tr>
              </thead>
              <tbody>
                {passkeys.map((passkey) => (
                  <tr key={passkey.id} className="border-b border-slate-100">
                    <td className="py-2 pr-4">{passkey.label}</td>
                    <td className="py-2 pr-4 text-xs">
                      {passkey.createdAt
                        ? new Date(passkey.createdAt).toLocaleDateString()
                        : '—'}
                    </td>
                    <td className="py-2 pr-4 text-xs">
                      {/* Never used is worth seeing plainly: it is either a device that was set up
                          and abandoned, or one somebody has lost. Both are worth removing. */}
                      {passkey.lastUsed
                        ? new Date(passkey.lastUsed).toLocaleString()
                        : 'never'}
                    </td>
                    <td className="py-2 pr-4 text-right">
                      <button
                        className="btn text-xs"
                        onClick={() => {
                          if (
                            passkeys.length === 1 &&
                            !window.confirm(
                              'This is your only passkey. Removing it means signing in with your password again. Continue?',
                            )
                          ) {
                            return
                          }
                          void remove(passkey)
                        }}
                      >
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        <p className="text-xs leading-relaxed text-slate-500">
          Add one for each device you sign in from. A passkey never leaves the device it was made
          on, so there is nothing to copy and nothing that can be stolen from this server — what is
          kept here is only the public half.
        </p>
      </div>
    </Section>
  )
}
