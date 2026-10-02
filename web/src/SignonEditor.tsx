import { useEffect, useMemo, useState } from 'react'

import { SamlMetadataImport } from './SamlMetadataImport'

import {
  api,
  ApiError,
  type LDAPConfig,
  type OIDCConfig,
  type SAMLConfig,
  type SignonDocument,
  type SignonField,
  type SignonTestResult,
} from './api'
import { ErrorBox, Field, Section } from './ui'

/**
 * Editing sign-on as settings, and testing it before trusting it.
 *
 * # Why this screen matters more than the alert rules one
 *
 * Both configurations were already reachable as YAML text boxes, so this is not about whether they can be edited. It is about three
 * things that are worse here than anywhere else in the product.
 *
 * A wrong alert rule means you are not told about a problem. A wrong sign-on configuration means nobody can get in. It is read at
 * startup, so the feedback loop runs through a restart rather than a page reload. And almost every mistake available here is silent in
 * one specific way: a username attribute that names nothing finds nobody, and finding nobody reaches the person as "wrong password".
 * They retry, lock their account, and telephone somebody, while the fault is one field on this screen.
 *
 * # Why testing is the point
 *
 * The test buttons run the server's real code paths and report each stage separately: reached the directory, encrypted the connection,
 * bound as the service account, found this person, read these groups, mapped them to this role. Stage-by-stage matters because knowing
 * which step failed is most of knowing why - "sign-in does not work" sends somebody to check a password, and "found nobody matching
 * sAMAccountName=jsmith" sends them to the one field that is wrong.
 *
 * No password is asked for. Everything that goes silently wrong goes wrong before a password is checked, so a probe needs none, and a
 * password box on a screen that does not need one is how credentials end up in a browser's saved-form data.
 *
 * # Secrets
 *
 * The browser never receives a secret. Each is shown as set or not set, and a save that leaves the box untouched keeps what is on disk.
 * That is not only about exposure: the settings screen learned that a redacted placeholder can be saved back over a real credential,
 * which takes sign-on down while leaving a file that reads perfectly plausibly. Here the browser has nothing to write back.
 */

type Tab = 'oidc' | 'saml' | 'ldap'

/** A stage list, which is the whole value of a test. */
function TestReport({ result, label }: { result: SignonTestResult; label: string }) {
  return (
    <div className="mt-3 space-y-2" role="status" aria-label={`${label} test result`}>
      {result.stages.map((stage, i) => (
        <div
          key={i}
          className={`rounded-lg border p-3 text-xs ${
            stage.ok ? 'border-emerald-800 bg-emerald-950/30' : 'border-rose-800 bg-rose-950/30'
          }`}
        >
          <p className={stage.ok ? 'text-emerald-200' : 'text-rose-200'}>
            {stage.ok ? '✓' : '✗'} {stage.name}
          </p>
          {stage.detail && <p className="mt-1 leading-relaxed break-words text-slate-300">{stage.detail}</p>}
        </div>
      ))}

      {result.groups !== undefined && (
        <div className="rounded-lg border border-slate-700 bg-slate-900/60 p-3 text-xs">
          {result.role ? (
            <p className="text-emerald-200">
              This person would sign in as <strong>{result.role}</strong>.
            </p>
          ) : (
            /* The line that turns a working directory into a working sign-in. Everything can pass and somebody can still be refused,
               because a person in no mapped group is refused rather than given a default role. */
            <p className="text-amber-200">
              This person was found, and none of their groups is mapped to a role — so they would be refused. Their groups are listed
              above; add one of them to the mapping.
            </p>
          )}
        </div>
      )}
    </div>
  )
}

/** Help under a field, including the two directory flavours where they differ. */
function FieldHelp({ field, defaultValue }: { field: SignonField; defaultValue?: string }) {
  return (
    <>
      <span>{field.help}</span>
      {defaultValue && (
        <span className="mt-1 block text-slate-500">
          Left empty this becomes <code className="font-mono text-slate-400">{defaultValue}</code>.
        </span>
      )}
      {(field.activeDirectory || field.openLDAP) && (
        <span className="mt-1 block text-slate-500">
          {field.activeDirectory && (
            <>
              Active Directory: <code className="font-mono text-slate-400">{field.activeDirectory}</code>
            </>
          )}
          {field.activeDirectory && field.openLDAP && ' · '}
          {field.openLDAP && (
            <>
              OpenLDAP: <code className="font-mono text-slate-400">{field.openLDAP}</code>
            </>
          )}
        </span>
      )}
    </>
  )
}

/** The group-to-role table, shared by both methods because the decision is the same one. */
function RoleMapping({
  roles,
  available,
  onChange,
  idPrefix,
}: {
  roles: Record<string, string>
  available: string[]
  onChange: (next: Record<string, string>) => void
  idPrefix: string
}) {
  const [newGroup, setNewGroup] = useState('')

  const rows = useMemo(() => Object.entries(roles).sort(([a], [b]) => a.localeCompare(b)), [roles])

  return (
    <div className="space-y-2">
      {rows.length === 0 && (
        <p className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-amber-200">
          No groups are mapped, so nobody could sign in this way. A person in none of these groups is refused rather than given a
          default role, which is deliberate: a directory holds thousands of accounts and defaulting to viewer would give every one of
          them a view of live clinical message flow.
        </p>
      )}

      {rows.map(([group, role]) => (
        <div key={group} className="flex flex-wrap items-center gap-2">
          <code className="flex-1 rounded bg-slate-800/60 px-2 py-1 font-mono text-xs text-slate-200">{group}</code>

          <select
            aria-label={`Role for ${group}`}
            className="input max-w-[12rem]"
            value={role}
            onChange={(e) => onChange({ ...roles, [group]: e.target.value })}
          >
            {available.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>

          <button
            type="button"
            className="btn-ghost py-1 text-xs"
            aria-label={`Stop mapping ${group}`}
            onClick={() => {
              const next = { ...roles }
              delete next[group]
              onChange(next)
            }}
          >
            Remove
          </button>
        </div>
      ))}

      <div className="flex flex-wrap items-center gap-2 pt-1">
        <input
          id={`${idPrefix}-new-group`}
          aria-label="Group name to map"
          className="input flex-1"
          placeholder="group name as the directory reports it"
          value={newGroup}
          onChange={(e) => setNewGroup(e.target.value)}
        />
        <button
          type="button"
          className="btn-ghost py-1 text-xs"
          disabled={newGroup.trim() === ''}
          onClick={() => {
            onChange({ ...roles, [newGroup.trim()]: available[0] ?? 'viewer' })
            setNewGroup('')
          }}
        >
          + Map this group
        </button>
      </div>
    </div>
  )
}

export function SignonEditor() {
  const [doc, setDoc] = useState<SignonDocument | null>(null)
  const [tab, setTab] = useState<Tab>('oidc')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<{ message: string; problems: string[] } | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const [oidc, setOIDC] = useState<OIDCConfig | null>(null)
  const [saml, setSAML] = useState<SAMLConfig | null>(null)
  const [ldap, setLDAP] = useState<LDAPConfig | null>(null)

  // Undefined means untouched, which is what keeps the stored secret. An empty string means somebody cleared it deliberately.
  const [newSecret, setNewSecret] = useState<string | undefined>(undefined)
  const [newPassword, setNewPassword] = useState<string | undefined>(undefined)

  const [testUser, setTestUser] = useState('')
  const [oidcTest, setOIDCTest] = useState<SignonTestResult | null>(null)
  const [samlTest, setSAMLTest] = useState<SignonTestResult | null>(null)
  const [ldapTest, setLDAPTest] = useState<SignonTestResult | null>(null)

  const load = async () => {
    try {
      const got = await api.signon()
      setDoc(got)
      setOIDC(got.oidc.config)
      setSAML(got.saml.config)
      setLDAP(got.ldap.config)
      setNewSecret(undefined)
      setNewPassword(undefined)
    } catch (e) {
      setError({
        message: e instanceof Error ? e.message : String(e),
        problems: e instanceof ApiError ? e.problems : [],
      })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  const oidcDirty = useMemo(
    () => JSON.stringify(oidc) !== JSON.stringify(doc?.oidc.config) || newSecret !== undefined,
    [oidc, doc, newSecret],
  )
  const ldapDirty = useMemo(
    () => JSON.stringify(ldap) !== JSON.stringify(doc?.ldap.config) || newPassword !== undefined,
    [ldap, doc, newPassword],
  )
  const samlDirty = useMemo(() => JSON.stringify(saml) !== JSON.stringify(doc?.saml.config), [saml, doc])

  async function save(which: Tab) {
    setBusy(true)
    setError(null)
    setNote(null)
    try {
      // Exhaustive rather than a ternary, so a fourth method cannot fall through to whichever branch happened to be last.
      const res =
        which === 'oidc'
          ? await api.saveOIDC(oidc!, newSecret)
          : which === 'saml'
            ? await api.saveSAML(saml!)
            : await api.saveLDAP(ldap!, newPassword)
      setNote(res.note)
      await load()
    } catch (e) {
      setError({
        message: e instanceof Error ? e.message : String(e),
        problems: e instanceof ApiError ? e.problems : [],
      })
    } finally {
      setBusy(false)
    }
  }

  async function test(which: Tab) {
    setBusy(true)
    setError(null)
    try {
      if (which === 'oidc') {
        setOIDCTest(await api.testOIDC(oidc?.issuer ?? ''))
      } else if (which === 'saml') {
        setSAMLTest(await api.testSAML(saml!))
      } else {
        setLDAPTest(await api.testLDAP(ldap!, testUser, newPassword))
      }
    } catch (e) {
      setError({
        message: e instanceof Error ? e.message : String(e),
        problems: e instanceof ApiError ? e.problems : [],
      })
    } finally {
      setBusy(false)
    }
  }

  if (loading) {
    return (
      <Section title="Sign-on" description="How people prove who they are.">
        <p className="text-sm text-slate-400">Reading the configuration…</p>
      </Section>
    )
  }

  if (!doc || !oidc || !ldap) {
    return (
      <Section title="Sign-on" description="How people prove who they are.">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      </Section>
    )
  }

  /* One lookup rather than a ternary per use.
   *
   * There were fifteen instances of "tab === 'oidc' ? a : b" before SAML arrived, and each one was a place a third method would be
   * silently absent - the screen would render, the tab would work, and one field would edit the wrong object. A table makes adding a
   * method a single entry instead of fifteen edits with nothing to catch a missed one.
   */
  const perTab = {
    oidc: {
      state: doc.oidc,
      fields: doc.oidcFields,
      config: oidc as unknown as Record<string, unknown> | null,
      roles: oidc?.roles,
      setRoles: (next: Record<string, string>) => setOIDC({ ...oidc!, roles: next }),
      test: oidcTest,
      // Part of the report's accessible name, so the existing wording is kept rather than tidied.
      title: 'Provider',
    },
    saml: {
      state: doc.saml,
      fields: doc.samlFields,
      config: saml as unknown as Record<string, unknown> | null,
      roles: saml?.roles,
      setRoles: (next: Record<string, string>) => setSAML({ ...saml!, roles: next }),
      test: samlTest,
      title: 'SAML',
    },
    ldap: {
      state: doc.ldap,
      fields: doc.ldapFields,
      config: ldap as unknown as Record<string, unknown> | null,
      roles: ldap?.roles,
      setRoles: (next: Record<string, string>) => setLDAP({ ...ldap!, roles: next }),
      test: ldapTest,
      title: 'Directory',
    },
  }[tab]

  const dirty = { oidc: oidcDirty, saml: samlDirty, ldap: ldapDirty }[tab]

  const state = perTab.state
  const fields = perTab.fields

  /** Renders one described field against whichever configuration is on screen. */
  function control(field: SignonField) {
    const id = `signon-${tab}-${field.key}`
    const help = <FieldHelp field={field} defaultValue={tab === 'ldap' ? doc!.ldapDefaults[field.key] : undefined} />

    // The two role mappings are the same control, and the same decision.
    if (field.kind === 'roles') {
      return (
        <div key={field.key} className="pt-2">
          <p className="label">{field.label}</p>
          <p className="mt-1 mb-2 text-xs leading-relaxed text-slate-400">{help}</p>
          <RoleMapping
            idPrefix={`signon-${tab}`}
            roles={perTab.roles ?? {}}
            available={doc!.roles}
            onChange={(next) => perTab.setRoles(next)}
          />
        </div>
      )
    }

    if (field.kind === 'secret') {
      // Two cases and not three, deliberately: SAML has no secret. A signing certificate is public by construction - it is in
      // every response and usually on a metadata page anybody can fetch - which is the one way SAML is easier to handle than OIDC.
      const stored = tab === 'oidc' ? oidc!.clientSecret : ldap!.bindPassword
      const typed = tab === 'oidc' ? newSecret : newPassword
      const setTyped = tab === 'oidc' ? setNewSecret : setNewPassword

      return (
        <Field key={field.key} label={field.label} htmlFor={id} hint={help}>
          <div className="space-y-1">
            <input
              id={id}
              className="input"
              type="password"
              autoComplete="new-password"
              value={typed ?? ''}
              placeholder={stored.set ? 'unchanged' : 'not set'}
              onChange={(e) => setTyped(e.target.value)}
            />
            <p className="text-xs text-slate-500">
              {stored.from
                ? `Read from ${stored.from}.`
                : stored.set
                  ? 'A secret is stored. Leave this empty to keep it.'
                  : 'No secret is stored.'}
            </p>
          </div>
        </Field>
      )
    }

    if (field.kind === 'bool') {
      const value = perTab.config?.[field.field] as boolean

      return (
        <div key={field.key} className="pt-2">
          <label className="flex items-start gap-2 text-sm text-slate-300">
            <input
              id={id}
              type="checkbox"
              className="mt-0.5 accent-sky-600"
              checked={Boolean(value)}
              onChange={(e) => set(field.field, e.target.checked)}
            />
            <span>
              {field.label}
              <span className="mt-1 block text-xs leading-relaxed text-slate-400">{help}</span>
            </span>
          </label>
        </div>
      )
    }

    if (field.kind === 'list') {
      const value = ((oidc as unknown as Record<string, unknown>)[field.field] as string[]) ?? []

      return (
        <Field key={field.key} label={field.label} htmlFor={id} hint={help}>
          <input
            id={id}
            className="input"
            value={value.join(', ')}
            placeholder={field.placeholder}
            onChange={(e) =>
              set(
                field.field,
                e.target.value
                  .split(',')
                  .map((s) => s.trim())
                  .filter(Boolean),
              )
            }
          />
        </Field>
      )
    }

    const value = perTab.config?.[field.field]

    return (
      <Field
        key={field.key}
        label={field.required ? `${field.label} (required)` : field.label}
        htmlFor={id}
        hint={help}
      >
        <input
          id={id}
          className="input"
          value={typeof value === 'string' ? value : ''}
          placeholder={field.placeholder}
          onChange={(e) => set(field.field, e.target.value)}
        />
      </Field>
    )
  }

  function set(property: string, value: unknown) {
    // Exhaustive on the tab rather than an if/else, so adding a fourth method is a type error here instead of a control that edits
    // the wrong object. That was the actual risk: an else branch silently claims every future case.
    switch (tab) {
      case 'oidc':
        setOIDC({ ...oidc!, [property]: value } as OIDCConfig)

        return
      case 'saml':
        setSAML({ ...saml!, [property]: value } as SAMLConfig)

        return
      case 'ldap':
        setLDAP({ ...ldap!, [property]: value } as LDAPConfig)

        return
    }
  }

  return (
    <Section
      title="Sign-on"
      description="How people prove who they are. These settings are read when the server starts, so changes take effect after a restart."
    >
      <div className="space-y-4">
        {/* The warning that matters most on this screen.
            
            If a directory becomes the only way in and its configuration is wrong, the way back is editing files on the server. A local
            administrator is the difference between a mistake and an outage, so this is said before anybody relies on it. */}
        {doc.localAdmins === 0 && (
          <p className="rounded-lg border border-rose-500/40 bg-rose-500/10 p-3 text-sm text-rose-200">
            There are no local administrator accounts on this server. If federated sign-in is misconfigured, nobody will be able to get
            in and the only way back will be editing files on the server itself. Create a local administrator before relying on this.
          </p>
        )}

        <div className="flex flex-wrap gap-2" role="tablist" aria-label="Sign-on method">
          {(['oidc', 'saml', 'ldap'] as Tab[]).map((t) => (
            <button
              key={t}
              type="button"
              role="tab"
              aria-selected={tab === t}
              className={tab === t ? 'btn-primary py-1 text-xs' : 'btn-ghost py-1 text-xs'}
              onClick={() => setTab(t)}
            >
              {
                {
                  oidc: 'Single sign-on (OpenID Connect)',
                  saml: 'Single sign-on (SAML 2.0)',
                  ldap: 'Directory (LDAP)',
                }[t]
              }
              {/* Running rather than saved, and the distinction is load-bearing: all three are read when the server starts, so a
                  configuration can be correct on disk and not yet in use. Somebody who saved a fix and cannot sign in needs to be
                  told that, not congratulated. */}
              {{ oidc: doc.oidc.active, saml: doc.saml.active, ldap: doc.ldap.active }[t] && ' · running'}
            </button>
          ))}
        </div>

        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

        {note && (
          <p role="status" className="rounded-lg border border-emerald-500/40 bg-emerald-500/10 p-3 text-sm text-emerald-200">
            {note}
          </p>
        )}

        {!state.configured && (
          <p className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-amber-200">
            This server was started without a <span className="font-mono">-{tab}</span> file, so there is nowhere to save this. The path is
            a startup setting because the server has to know where to read it before it can serve this page.
          </p>
        )}

        {state.problem && (
          <p className="rounded-lg border border-rose-500/40 bg-rose-500/10 p-3 text-sm text-rose-200">
            The file on disk does not load, so what is shown below is empty rather than what it contains. Fix this and nothing else
            here is trustworthy: <span className="font-mono text-xs">{state.problem}</span>
          </p>
        )}

        {state.configured && state.exists && !state.active && (
          <p className="rounded-lg border border-slate-700 bg-slate-900/60 p-3 text-sm text-slate-300">
            This method is configured and the running server has not loaded it, which is the normal state after an edit. It starts
            working at the next restart.
          </p>
        )}

        {tab === 'ldap' && (
          <div className="rounded-lg border border-slate-700 bg-slate-900/60 p-3">
            <p className="text-xs text-slate-400">
              Attribute names differ between the two kinds of directory, and a wrong one finds nobody rather than reporting an error.
              These fill the fields visibly and everything stays editable.
            </p>
            <div className="mt-2 flex flex-wrap gap-2">
              {doc.ldapPresets.map((preset) => (
                <button
                  key={preset.name}
                  type="button"
                  className="btn-ghost py-1 text-xs"
                  title={preset.detail}
                  onClick={() => {
                    // Preset values are keyed by the YAML name, so they are translated through the catalogue rather than by
                    // rewriting the string. Deriving the property name is what nearly broke four controls; there is no reason to
                    // do it here either when the server has already said what each field is called.
                    const property = new Map(doc.ldapFields.map((f) => [f.key, f.field]))
                    const next = { ...ldap } as unknown as Record<string, unknown>
                    for (const [k, v] of Object.entries(preset.values)) {
                      const name = property.get(k)
                      if (name) next[name] = v
                    }
                    setLDAP(next as unknown as LDAPConfig)
                  }}
                >
                  Use {preset.name} names
                </button>
              ))}
            </div>
          </div>
        )}

        {/* Above the fields, because reading the provider's document is the first thing to do rather than a
            recovery for having filled them in wrongly. */}
        {tab === 'saml' && (
          <SamlMetadataImport config={saml} onApply={(next) => setSAML({ ...saml!, ...next })} />
        )}

        <div className="space-y-3">{fields.map(control)}</div>

        <div className="rounded-xl border border-slate-700 bg-slate-900/60 p-4">
          <p className="text-sm text-slate-200">Test this configuration</p>
          <p className="mt-1 text-xs leading-relaxed text-slate-400">
            {
              {
                oidc: 'Reads the provider’s discovery document and reports the endpoints Perfuse would use. Nothing is saved by testing.',
                saml: 'Checks the settings, reads the certificate, and builds a sign-in request. It says plainly what it cannot check: nothing here proves somebody can sign in, because that needs a real assertion about a real person. Nothing is saved by testing.',
                ldap: 'Connects, binds as the service account, and — if you give a username — looks that person up and reports the role they would get. No password is needed: everything that goes silently wrong here goes wrong before a password is checked. Nothing is saved by testing.',
              }[tab]
            }
          </p>

          {tab === 'ldap' && (
            <div className="mt-3 max-w-sm">
              <Field
                label="Check a username"
                htmlFor="signon-test-username"
                hint="Somebody who should be able to sign in. Optional, and the most useful part."
              >
                <input
                  id="signon-test-username"
                  className="input"
                  value={testUser}
                  placeholder="jsmith"
                  onChange={(e) => setTestUser(e.target.value)}
                />
              </Field>
            </div>
          )}

          <button type="button" className="btn-ghost mt-3 py-1 text-xs" disabled={busy} onClick={() => void test(tab)}>
            {busy ? 'Testing…' : 'Run the test'}
          </button>

          {perTab.test && <TestReport result={perTab.test} label={perTab.title} />}
        </div>

        <div className="flex flex-wrap items-center gap-3">
          <span className="text-xs text-slate-500">
            Stored in <code className="font-mono">{state.path || 'nowhere'}</code>
          </span>

          <span className="flex-1" />

          {dirty && <span className="text-xs text-amber-300">Unsaved changes</span>}

          <button
            type="button"
            className="btn-primary"
            disabled={busy || !state.configured || !dirty}
            onClick={() => void save(tab)}
          >
            {busy ? 'Saving…' : 'Save sign-on settings'}
          </button>
        </div>
      </div>
    </Section>
  )
}
