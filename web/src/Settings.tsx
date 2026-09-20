import { SignonEditor } from './SignonEditor'
import { CodeArea } from './CodeArea'
import { useCallback, useEffect, useMemo, useState } from 'react'
import type { SettingsFile, SettingsResponse, StartupSettings } from './api'
import { api } from './api'
import type { UiError } from './store'
import { toUiError } from './store'
import { AllSettings } from './AllSettings'
import { BrandingPanel } from './BrandingPanel'
import { ErrorBox, Field, Section, Toggle } from './ui'

/** Settings edits the configuration files the server was started with.
 *
 *  It edits those files rather than a copy in the database, which is the same thing the
 *  channel editor does and keeps configuration diffable on disk with no second source of
 *  truth.
 *
 *  Split into two halves for a reason. The editable half is configuration - what should
 *  alert, who may sign in. The read-only half is startup topology: where the process
 *  listens, which database it is using, what it is. A page that rewrote those would be
 *  editing its own foundations while standing on them, but hiding them is how a wrong flag
 *  survives for months. */
export function Settings() {
  const [data, setData] = useState<SettingsResponse | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setData(await api.settings())
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

  if (loading && !data) {
    return <p className="text-sm text-slate-500">Loading settings…</p>
  }

  return (
    <div className="space-y-8">
      {error && <ErrorBox error={error} />}

      {/* Branding first, because it is the one thing on this page a person comes looking for
          specifically rather than arriving at, and because the logo is a file upload that has no
          place among typed values. */}
      <BrandingPanel />

      {/* The typed settings come next, because they are what somebody arriving here is almost
          always looking for. The file editors below are for the three things that are genuinely
          documents rather than values - alert rules, single sign-on, the peer list. */}
      <AllSettings />

      {/* Sign-on above the file editors, and deliberately not among them.
          
          It used to be one of them: two YAML boxes beside the alert rules and the peer list. That put the highest-stakes settings on
          the page in the same shape as the lowest, and offered no help with the part that is genuinely hard - every mistake available
          in either file is silent, and reaches a person as "wrong password". The file editors below still show both files, because
          somebody who prefers the file should not have it taken away. */}
      {data?.canEdit && <SignonEditor />}

      {data && (
        <>
          <h2 className="border-b border-slate-800 pb-2 text-lg font-semibold text-slate-100">
            Configuration files
          </h2>

          {data.files.map((file) => (
            <SettingsFileEditor
              key={file.kind}
              file={file}
              canEdit={data.canEdit}
              onSaved={load}
            />
          ))}

          <StartupPanel startup={data.startup} />
        </>
      )}
    </div>
  )
}

/** Titles, so each section reads as what it does rather than as a filename. */
const titles: Record<string, string> = {
  alerts: 'Alert rules',
  oidc: 'Single sign-on',
  ldap: 'Directory sign-in',
  peers: 'Fleet peers',
}

/** Starting points offered when a file does not exist yet.
 *
 *  A blank editor is a poor place to begin for a format nobody memorises, and the alternative
 *  is somebody copying an example out of documentation that may not match this version. These
 *  are validated by the same loader as anything else, so a template that stopped being valid
 *  would be caught by trying to save it. */
const templates: Record<string, string> = {
  alerts: `rules:
  # Raise a warning when a destination queue backs up.
  - kind: queue-depth
    threshold: 100
    severity: warning

  # And when a channel stops receiving anything for an hour, which is
  # usually a sending system that has been switched off or repointed.
  # Only use this on a feed that genuinely never goes quiet.
  - kind: no-traffic
    within: 1h
    severity: warning

  # For everything else - a clinic feed that stops at six, a referral
  # feed that is dead all weekend - compare against what this channel
  # normally carries at this hour of this weekday instead. A threshold
  # of 0.8 means four fifths of the usual traffic has not arrived.
  # Channels without enough history are skipped rather than guessed at.
  - kind: below-rhythm
    threshold: 0.8
    severity: warning
`,
  oidc: `# The issuer must be the exact value the provider publishes, including
# whether it ends in a slash - a mismatch is rejected by design.
issuer: https://login.example.test
client_id: perfuse
client_secret: replace-me
redirect_url: https://perfuse.example.test/auth/callback
label: Hospital sign-in

# No group here means no role, and no role means refused. That is
# deliberate: a default of viewer would let everybody in the directory
# see live clinical message flow.
roles:
  admin:
    - perfuse-admins
  editor:
    - perfuse-editors
  viewer:
    - perfuse-viewers
`,
  ldap: `addr: dc01.hospital.local:636
tls: true

# The service account only needs to read. It does not need to write, and
# it should not be a domain administrator.
bind_dn: CN=perfuse,OU=Service Accounts,DC=hospital,DC=local
bind_password: replace-me

user_base_dn: OU=Staff,DC=hospital,DC=local

# sAMAccountName on Active Directory; uid on most others.
username_attribute: sAMAccountName
name_attribute: displayName
email_attribute: mail
member_of_attribute: memberOf

# Strongly recommended: without it people are identified by their DN, so
# moving somebody between organisational units looks like a new person.
unique_id_attribute: objectGUID

# Excludes disabled accounts. Without this, somebody who left last year
# can still sign in, because a disabled account still binds on AD.
user_filter: (!(userAccountControl:1.2.840.113556.1.4.803:=2))

roles:
  admin:
    - Perfuse Admins
  viewer:
    - Perfuse Users
`,
  peers: `peers:
  - name: site-b
    url: https://perfuse-b.hospital.local
    # Whether this console may start and stop that server's channels.
    allow_control: false
`,
}

function SettingsFileEditor({
  file,
  canEdit,
  onSaved,
}: {
  file: SettingsFile
  canEdit: boolean
  onSaved: () => void
}) {
  const [content, setContent] = useState(file.content)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<UiError | null>(null)
  // What was last saved from this box, rather than a boolean.
  //
  // A boolean could not work here and never did. Saving set it true and called back to refetch the file; the refetch changed the
  // file prop, the effect below fired, and it set the flag straight back to false - so the confirmation was destroyed by the save
  // that earned it. Between those two moments "Unsaved changes" was also hidden, because that needs the flag to be false, so
  // somebody who saved a file saw no message at all and had no way to tell it had worked.
  //
  // Comparing content instead means the confirmation survives the refetch that follows a save, and disappears by itself the moment
  // somebody types again, without anything having to remember to clear it.
  const [savedContent, setSavedContent] = useState<string | null>(null)

  // Reset when the file is reloaded from the server, or an editor would keep showing an old
  // body after somebody else changed it.
  useEffect(() => {
    setContent(file.content)
  }, [file.content])

  const dirty = content !== file.content
  const title = titles[file.kind] ?? file.kind

  // Refused in the interface as well as on the server. Saving a placeholder over a real
  // secret would take a working feature down with a file that reads perfectly plausibly, and
  // catching it here means the person is told before they wait for a round trip.
  const holdsPlaceholder = content.includes('**redacted**')

  const save = async () => {
    setSaving(true)
    setError(null)
    try {
      await api.saveSettings(file.kind, content)
      setSavedContent(content)
      onSaved()
    } catch (err) {
      setError(toUiError(err))
    } finally {
      setSaving(false)
    }
  }

  if (!file.configured) {
    return (
      <Section title={title} description={file.description}>
        <div className="rounded-lg border border-dashed border-slate-700 bg-slate-900/40 p-4">
          <p className="text-sm text-slate-300">
            This server was not started with a {file.kind} file, so there is nowhere to save
            one.
          </p>
          <p className="mt-2 text-xs text-slate-500">
            Restart it with{' '}
            <code className="rounded bg-slate-800 px-1 py-0.5 font-mono text-slate-200">
              -{file.kind} /path/to/{file.kind}.yaml
            </code>{' '}
            and this section becomes editable. The path is a startup setting because the
            server has to know where to read it before it can serve this page.
          </p>
        </div>
      </Section>
    )
  }

  return (
    <Section title={title} description={file.description}>
      <div className="space-y-3">
        <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-xs sm:grid-cols-4">
          <Detail label="File" value={file.path ?? ''} mono />
          <Detail label="Size" value={file.exists ? `${file.bytes} bytes` : 'not created yet'} />
          <Detail
            label="Changed"
            value={file.modifiedAt ? new Date(file.modifiedAt).toLocaleString() : '—'}
          />
          <Detail
            label="Takes effect"
            value={file.restartRequired ? 'on restart' : 'within a minute'}
          />
        </dl>

        {file.redacted && (
          <p className="rounded-md border border-amber-900/60 bg-amber-950/30 px-3 py-2 text-xs text-amber-200">
            Credentials in this file are shown as <code>**redacted**</code> for your role.
            Saving would replace the real values with that text, so saving is disabled.
          </p>
        )}

        {!file.exists && (
          <p className="rounded-md border border-sky-900/60 bg-sky-950/30 px-3 py-2 text-xs text-sky-200">
            This file does not exist yet. A starting point is filled in below — read it
            through and change what does not apply before saving.
          </p>
        )}

        <CodeArea
          language="yaml"
          className="h-72 w-full"
          value={content || (!file.exists ? (templates[file.kind] ?? '') : '')}
          onChange={setContent}
          disabled={!canEdit || file.redacted}
          aria-label={`${title} configuration`}
        />

        {error && <ErrorBox error={error} />}

        {holdsPlaceholder && (
          <p className="text-xs text-rose-700">
            This still contains <code>**redacted**</code>, which is what a hidden credential
            looks like. Saving it would write that text where the real value belongs.
          </p>
        )}

        <div className="flex items-center gap-3">
          <button
            className="btn-primary"
            disabled={!canEdit || saving || file.redacted || holdsPlaceholder}
            onClick={() => void save()}
          >
            {saving ? 'Checking and saving…' : 'Save'}
          </button>

          {dirty && <span className="text-xs text-amber-700">Unsaved changes</span>}
          {!dirty && savedContent === content && <span className="text-xs text-emerald-700">Saved</span>}

          {file.restartRequired && (
            <span className="text-xs text-slate-500">
              A restart is needed before this takes effect.
            </span>
          )}
        </div>

        <p className="text-xs text-slate-500">
          Saving checks the file with the same loader the server uses at startup. If it is not
          valid, nothing is written and the current file is left alone.
        </p>
      </div>
    </Section>
  )
}

/** StartupPanel shows what cannot be changed here, and why. */
function StartupPanel({ startup }: { startup: StartupSettings }) {
  const started = useMemo(() => {
    if (!startup.startedAt) return '—'
    return new Date(startup.startedAt).toLocaleString()
  }, [startup.startedAt])

  return (
    <Section
      title="How this server was started"
      description="These come from the command line and cannot be changed here: a page that rewrote where the server listens, or which database it uses, would be editing its own foundations. They are shown because a wrong flag that appears nowhere survives for months."
    >
      <div className="space-y-4">
        <dl className="grid grid-cols-1 gap-x-6 gap-y-2 text-xs sm:grid-cols-2 lg:grid-cols-3">
          <Detail label="Listening on" value={startup.addr} mono />
          <Detail label="Channels" value={startup.channelsDir} mono />
          <Detail label="Database" value={startup.databasePath} mono />
          <Detail label="Started" value={started} />
          <Detail label="Platform" value={startup.platform ?? '—'} />
          <Detail label="Go" value={startup.goVersion ?? '—'} />
          <Detail label="Sign-in methods" value={startup.authMethods.join(', ')} />
          <Detail label="Message retention" value={`${startup.retentionDays} days`} />
          <Detail
            label="Alert delivery"
            value={startup.alertWebhook ? `webhook, at ${startup.alertSeverity ?? 'warning'} and above` : 'log only'}
          />
          <Detail label="Fleet label" value={startup.fleetLabel || '(hostname)'} />
          <Detail label="Tracing" value={startup.traceEndpoint || 'off'} mono />
        </dl>

        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <ReadOnlyToggle label="TLS on this interface" on={startup.tls} />
          <ReadOnlyToggle label="Running channels" on={startup.engineRunning} />
          <ReadOnlyToggle label="Serving FHIR" on={startup.fhirServing} />
          <ReadOnlyToggle label="Multi-tenant" on={startup.multiTenant} />
          <ReadOnlyToggle label="Storing message history" on={startup.storeMessages} />
          <ReadOnlyToggle label="Storing message bodies" on={startup.storePayloads} />
        </div>

        {startup.metadataReason && (
          <p className="rounded-md bg-slate-900/40 px-3 py-2 text-xs text-slate-300">
            <span className="font-medium">Outbound address policy.</span>{' '}
            {startup.metadataReason}
          </p>
        )}

        {!startup.tls && (
          <p className="rounded-md border border-amber-900/60 bg-amber-950/30 px-3 py-2 text-xs text-amber-200">
            This interface is not encrypted. Sign-ins and session cookies cross the network in
            the clear, and this console can read patient data.
          </p>
        )}

        {startup.storePayloads && (
          <p className="rounded-md bg-slate-900/40 px-3 py-2 text-xs text-slate-300">
            Message bodies are being stored, so this database holds patient data for{' '}
            {startup.retentionDays} days. That is a question a privacy officer will ask.
          </p>
        )}

        {startup.commandLine && startup.commandLine.length > 0 && (
          <details className="text-xs">
            <summary className="cursor-pointer text-slate-400">Full command line</summary>
            <pre className="mt-2 overflow-x-auto rounded-md bg-slate-900 p-3 font-mono text-[11px] text-slate-100">
              {startup.commandLine.join(' ')}
            </pre>
          </details>
        )}
      </div>
    </Section>
  )
}

function Detail({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <dt className="text-slate-500">{label}</dt>
      <dd className={mono ? 'break-all font-mono text-slate-100' : 'text-slate-100'}>
        {value || '—'}
      </dd>
    </div>
  )
}

/** ReadOnlyToggle shows a state without pretending it can be changed here.
 *
 *  A disabled Toggle rather than a tick, because the shape is what makes it read as a setting
 *  that exists and is currently off, rather than as a feature that is missing. */
function ReadOnlyToggle({ label, on }: { label: string; on: boolean }) {
  return (
    <Field label="">
      <Toggle checked={on} onChange={() => {}} label={label} />
    </Field>
  )
}
