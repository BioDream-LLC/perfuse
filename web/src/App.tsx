import { Fragment, useEffect, useState, lazy, Suspense } from 'react'
import { useDispatch, useSelector } from 'react-redux'
import { api } from './api'
import { MirthExportButton } from './MirthExport'
import { isPlaygroundFragment } from './playgroundLink'
import type { AuthMethods, ChannelSummary } from './api'
import { ChannelBuilder } from './ChannelBuilder'
import { Dashboard } from './Dashboard'
import { DocumentLab } from './DocumentLab'
import { FhirLab } from './FhirLab'
import { Messages } from './Messages'
import { Metrics } from './Metrics'
import { Queue } from './Queue'
import { AlertBanner, Alerts, useAlerts } from './Alerts'
import { ChannelHistoryPanel } from './ChannelHistory'
import { FeedProfilePanel } from './FeedProfilePanel'
import { Certificates } from './Certificates'
import { Contracts } from './Contracts'
import { Fleet } from './Fleet'
import { Tables } from './Tables'
import { MirthMigration } from './MirthMigration'
import { MapperPanel } from './MapperPanel'
import { Shadow } from './Shadow'
import { CommandPalette, type Command } from './CommandPalette'
import {
  checkSession,
  clearSessionError,
  clearUsersError,
  loadAudit,
  loadChannels,
  loadUsers,
  removeChannel,
  addUser,
  patchUser,
  removeUser,
  signIn,
  signOut,
  type AppDispatch,
  type RootState,
} from './store'
import { Confirm, ErrorBox, Field, RoleBadge, Section, Spinner, StatusDot } from './ui'

// Lazily loaded, because the editor is the one part of this interface with a large
// dependency. CodeMirror is roughly seven hundred kilobytes and this is one tab of
// thirteen; making everybody download and parse it to look at a dashboard would be a poor
// trade. It arrives as its own chunk when somebody opens the tab.
const ScriptLab = lazy(() => import('./ScriptLab').then((m) => ({ default: m.ScriptLab })))

// Lazy for the usual reason and one more: the playground fetches a 4 MB WebAssembly module the moment it
// mounts, so it must not be reachable by accident.
const Playground = lazy(() => import('./Playground'))
const FlowMap = lazy(() => import('./FlowMap'))

export type Tab =
  | 'dashboard'
  | 'channels'
  | 'messages'
  | 'queue'
  | 'alerts'
  | 'metrics'
  | 'fhir'
  | 'documents'
  | 'scripts'
  | 'certificates'
  | 'shadow'
  | 'migrate'
  | 'playground'
  | 'flow'
  | 'contracts'
  | 'fleet'
  | 'tefca'
  | 'tables'
  | 'mapper'
  | 'users'
  | 'audit'
  | 'settings'

export default function App() {
  const dispatch = useDispatch<AppDispatch>()
  const { me, checking } = useSelector((s: RootState) => s.session)

  useEffect(() => {
    dispatch(checkSession())
  }, [dispatch])

  if (checking) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Spinner label="Loading…" />
      </div>
    )
  }

  return me ? <Console /> : <Login />
}

import { Settings } from './Settings'
import { useBranding, BrandMark } from './Branding'
import { THEMES, THEME_LABELS, useTheme, type Theme } from './Theme'
import { FrictionPanel } from './FrictionPanel'
import { IconTheme, viewIcons } from './Icons'
import { NAV_GROUPS, DIRECT_TABS, GroupMenu, type NavItem } from './NavGroups'
import { Passkeys } from './Passkeys'
import { explainPasskeyError, passkeysSupported, usePasskey } from './passkey'
import { Tokens } from './Tokens'
import { TEFCAView } from './TEFCAView'

function Login() {
  const dispatch = useDispatch<AppDispatch>()
  const { signingIn, error } = useSelector((s: RootState) => s.session)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [methods, setMethods] = useState<AuthMethods | null>(null)

  /* Every federated option, in the order they are offered.
   *
   * A list rather than a pair of conditionals because OIDC and SAML can both be configured, and a page that showed only the first
   * would make the second unreachable from the only page that exists to reach it. Each is omitted by the server rather than sent
   * disabled, so nothing here can render a button that answers 404 - which is worse than no button: somebody clicks it, nothing
   * happens, and they conclude the product is broken.
   */
  const federated = [
    methods?.oidc && { label: `Sign in with ${methods.oidc.label}`, startAt: methods.oidc.startAt },
    methods?.saml && { label: methods.saml.label, startAt: methods.saml.startAt },
  ].filter((m): m is { label: string; startAt: string } => Boolean(m))

  const [passkeyBusy, setPasskeyBusy] = useState(false)
  const [passkeyError, setPasskeyError] = useState<string | null>(null)

  /** Signs in with a passkey.
   *
   *  Reloads on success rather than dispatching into the session slice. The server sets the
   *  cookie, and a reload makes every part of the application re-read who it is talking to -
   *  which is the same thing the federated redirect does, so there is one path rather than two. */
  const signInWithPasskey = async () => {
    setPasskeyBusy(true)
    setPasskeyError(null)
    try {
      const options = await api.beginPasskeySignIn()
      const { challenge, response } = await usePasskey(options)
      await api.finishPasskeySignIn(challenge, response)
      window.location.reload()
    } catch (err) {
      /** The browser's own WebAuthn messages are deliberately vague about why an authenticator
       *  refused, and the server's are deliberately vague about why a signature failed. Neither
       *  is going to explain itself, so the error name is translated into something actionable. */
      setPasskeyError(explainPasskeyError(err))
    } finally {
      setPasskeyBusy(false)
    }
  }

  /** Read from the URL rather than from state, because this is set by a redirect from the
   *  server after a failed federated sign-in - at which point nothing in this application
   *  has run yet, so there is no state to have put it in. */
  const [redirectError, setRedirectError] = useState<string | null>(() =>
    new URLSearchParams(window.location.search).get('signInError'),
  )

  useEffect(() => {
    /** A server without federated sign-in still answers this, so a failure here means the
     *  server is unreachable rather than that there is no provider. Left silent either way:
     *  the password form is the fallback and showing an error above it would suggest the
     *  form will not work. */
    api
      .authMethods()
      .then(setMethods)
      .catch(() => setMethods({ password: true }))
  }, [])

  useEffect(() => {
    if (!redirectError) return
    /** Cleared from the address bar once read, so a refresh does not show a stale failure and
     *  the message is not carried into a bookmark. */
    const url = new URL(window.location.href)
    url.searchParams.delete('signInError')
    window.history.replaceState({}, '', url.toString())
  }, [redirectError])

  const { branding } = useBranding()
  const tagline = branding.tagline ?? 'Healthcare integration engine'

  return (
    <div className="flex min-h-screen items-center justify-center p-6">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center text-center">
          <Logo />
          {/*
            The customer's tagline replaces ours when they set one. Left as the default otherwise
            rather than blank, because an empty line under the name reads as something failing to load.
          */}
          <p className="mt-3 text-sm text-slate-500">{tagline}</p>
        </div>

        {redirectError && (
          <div
            className="card mb-4 border-l-4 border-l-amber-500 p-4 text-sm text-slate-300"
            role="alert"
          >
            <p className="font-medium text-slate-100">Sign-in did not complete</p>
            <p className="mt-1">{redirectError}</p>
            <button
              className="mt-2 text-xs text-slate-400 underline hover:text-slate-200"
              onClick={() => setRedirectError(null)}
            >
              Dismiss
            </button>
          </div>
        )}

        {methods?.passkeys && passkeysSupported() && (
          <div className="mb-4">
            {/* A real button rather than a link: the flow is entirely in the page, and the
                browser shows its own prompt over it. No username field, because a discoverable
                credential lets the device offer whichever passkeys it holds for this address -
                which also means this button discloses nothing about who has an account. */}
            <button
              type="button"
              className="btn-primary flex w-full items-center justify-center gap-2"
              disabled={passkeyBusy}
              onClick={() => void signInWithPasskey()}
            >
              <KeyIcon />
              {passkeyBusy ? 'Waiting for your device…' : 'Sign in with a passkey'}
            </button>
            {passkeyError && (
              <p className="mt-2 text-xs leading-relaxed text-rose-400">{passkeyError}</p>
            )}
          </div>
        )}

        {federated.length > 0 && (
          <div className="mb-4">
            {/* Links rather than buttons with handlers. Each flow is a server redirect, so fetching
                it would follow the redirect inside the page and lose the browser navigation the
                provider needs. returnTo is where to land afterwards; the server refuses anything
                that is not a path, so a full URL here cannot become an open redirect.

                A list because OIDC and SAML can both be configured - a site moving between
                providers runs both for a while - and offering only the first would make the second
                unreachable from the one page that exists to reach it. */}
            {federated.map((m, i) => (
              <a
                key={m.startAt}
                className={`${
                  i === 0 ? 'btn-primary' : 'btn mt-2'
                } flex w-full items-center justify-center gap-2 no-underline`}
                href={`${m.startAt}?returnTo=${encodeURIComponent(window.location.pathname)}`}
              >
                <KeyIcon />
                {m.label}
              </a>
            ))}

            <div className="my-4 flex items-center gap-3" aria-hidden="true">
              <div className="h-px flex-1 bg-slate-800" />
              <span className="text-xs uppercase tracking-wide text-slate-400">or</span>
              <div className="h-px flex-1 bg-slate-800" />
            </div>
          </div>
        )}

        <form
          className="card space-y-4 p-6"
          onSubmit={(e) => {
            e.preventDefault()
            dispatch(signIn({ username, password }))
          }}
        >
          {error && <ErrorBox error={error} onDismiss={() => dispatch(clearSessionError())} />}

          <Field label="Username" htmlFor="username">
            <input
              id="username"
              className="input"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="username"
              autoFocus
            />
          </Field>

          {methods?.directory && (
            /* Above the password field rather than below the button, because it changes what
               somebody types rather than what they click - and read after the fact it is no
               help at all. */
            <p className="text-xs text-slate-500">
              Sign in with your {methods.directory.label} username and password.
            </p>
          )}

          <Field label="Password" htmlFor="password">
            <input
              id="password"
              className="input"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
            />
          </Field>

          <button
            className={federated.length > 0 ? 'btn w-full' : 'btn-primary w-full'}
            disabled={signingIn}
          >
            {signingIn ? 'Signing in…' : 'Sign in'}
          </button>

          {federated.length > 0 && (
            /* Said plainly because it is the question somebody asks when both are offered, and
               because it is the answer when the provider is down - which is exactly when nobody
               can read documentation hosted behind it. */
            <p className="text-xs text-slate-500">
              Local accounts still work when single sign-on is unavailable.
            </p>
          )}
        </form>
      </div>
    </div>
  )
}

/**
 * TabIcon draws the icon for a view.
 *
 * Decorative: the label is right beside it, so naming the icon as well would make a screen reader read
 * every view twice.
 */
function TabIcon({ label }: { label: string }) {
  const Icon = viewIcons[label]
  if (!Icon) return null
  return <Icon size={15} />
}

/**
 * Logo shows the customer's mark and name, falling back to the built-in ones.
 *
 * Reads from the branding context rather than taking props, because it appears in the header and on
 * the sign-in page and both should never disagree about what this product is called.
 */
function Logo() {
  const { branding } = useBranding()
  return (
    <div className="inline-flex items-center gap-2.5">
      <BrandMark size={32} />
      <span className="text-xl font-semibold tracking-tight text-slate-100">
        {branding.productName}
      </span>
    </div>
  )
}

/**
 * ThemePicker sits in the header rather than in Settings.
 *
 * A theme is chosen by looking at the result, so the control belongs where the result is visible. Buried in Settings it would
 * be a round trip per attempt, and somebody adjusting for a bright room would never find it at all.
 *
 * A select rather than three buttons: the header is already crowded, and there is no gain from showing all three at once
 * when only one can be active and the names say what they are.
 */
function ThemePicker() {
  const { theme, setTheme } = useTheme()

  return (
    <label className="hidden items-center gap-2 sm:inline-flex" title={THEME_LABELS[theme].about}>
      <span className="sr-only">Theme</span>
      <span aria-hidden="true" className="text-slate-500">
        <IconTheme size={15} />
      </span>
      <select
        aria-label="Theme"
        className="rounded-md border border-slate-700 bg-slate-900 px-2 py-1 text-xs text-slate-300"
        value={theme}
        onChange={(e) => setTheme(e.target.value as Theme)}
      >
        {THEMES.map((t) => (
          <option key={t} value={t}>
            {THEME_LABELS[t].label}
          </option>
        ))}
      </select>
    </label>
  )
}

function Console() {
  const dispatch = useDispatch<AppDispatch>()
  const me = useSelector((s: RootState) => s.session.me)!
  // A playground link opens the playground.
  //
  // The tab lives in state rather than in the URL, so a shared link would otherwise land the recipient on the dashboard with the
  // session sitting unused in the fragment - the one thing the link exists to prevent. Read before the first render so the wrong
  // panel never appears first.
  //
  // Deliberately narrow: this recognises one fragment, it is not tab routing. Deep-linking every tab is a larger change and
  // pretending this is that would leave a half-built mechanism for somebody to trust.
  const [tab, setTab] = useState<Tab>(() =>
    isPlaygroundFragment(window.location.hash) ? 'playground' : 'dashboard',
  )
  // Polled once here and passed down, so the banner and the alerts page do not
  // each poll the same endpoint on their own timer.
  const alerts = useAlerts()
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [channelNames, setChannelNames] = useState<string[]>([])

  // Cmd-K on a Mac, Ctrl-K elsewhere. Bound on the window rather than a container so
  // it works wherever focus happens to be, which during an incident is anywhere.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPaletteOpen((o) => !o)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // Channel names make the palette useful rather than merely present: most of what
  // somebody wants is "that channel", and they know its name, not which tab it is on.
  useEffect(() => {
    let live = true
    api
      .listChannels()
      .then((res) => live && setChannelNames(res.channels.map((c) => c.name)))
      .catch(() => {})
    return () => {
      live = false
    }
  }, [])

  // Every tab explains itself on hover.
  //
  // Thirteen tabs is a lot, and several of the labels mean nothing to somebody who has not used
  // an integration engine before. "Shadow" is the worst offender: it is a genuinely valuable
  // feature and the name gives no clue, so it goes unvisited. A one-sentence tooltip is the
  // cheapest possible fix - it costs a newcomer one hover and costs an expert nothing.
  const tabs: { id: Tab; label: string; minRole: string; about: string }[] = [
    {
      id: 'dashboard',
      label: 'Dashboard',
      minRole: 'viewer',
      about: 'Whether everything is running, and what has gone wrong recently',
    },
    {
      id: 'channels',
      label: 'Channels',
      minRole: 'viewer',
      about: 'Build, edit and run interfaces. Each one is a file on disk',
    },
    {
      id: 'messages',
      label: 'Messages',
      minRole: 'viewer',
      about: 'Search everything that has passed through, and replay any of it',
    },
    {
      id: 'queue',
      label: 'Queue',
      minRole: 'viewer',
      about: 'Messages waiting to be delivered because a receiver is down',
    },
    {
      id: 'alerts',
      label: 'Alerts',
      minRole: 'viewer',
      about: 'What you get told about, and what is firing now',
    },
    {
      id: 'metrics',
      label: 'Metrics',
      minRole: 'viewer',
      about: 'Volumes, timings and error rates over time',
    },
    {
      id: 'fhir',
      label: 'FHIR lab',
      minRole: 'viewer',
      about: 'Paste an HL7 v2 message and see the FHIR it becomes',
    },
    {
      id: 'documents',
      label: 'Documents',
      minRole: 'viewer',
      about: 'Clinical documents (CDA) carried inside messages, read and converted',
    },
    {
      id: 'scripts',
      label: 'Scripts',
      minRole: 'editor',
      about: 'Check JavaScript compiles, and see what Mirth E4X syntax becomes here',
    },
    {
      id: 'migrate',
      label: 'Migrate',
      minRole: 'editor',
      about:
        'Drop in a Mirth or OIE channel export and see how many of your channels run here unchanged',
    },
    {
      id: 'contracts',
      label: 'Contracts',
      minRole: 'viewer',
      about:
        'What each feed is expected to look like, and whether it still does. Catches a sending system changing before a receiver falls over',
    },
    {
      id: 'tables',
      label: 'Tables',
      minRole: 'viewer',
      about:
        'Shared mapping tables, who decided each mapping and why, and exactly which channels an edit would change',
    },
    {
      id: 'mapper',
      label: 'AI Mapper',
      minRole: 'editor',
      about:
        'Intelligent mapping suggestions with confidence scores. Abstains rather than guessing wrong. No cloud AI — runs locally.',
    },
    {
      id: 'fleet',
      label: 'Fleet',
      minRole: 'viewer',
      about:
        'Every Perfuse instance on one page. Mirth sells this as a separate product; here it is a config file, and a dead server never looks like a quiet one',
    },
    {
      id: 'tefca',
      label: 'TEFCA',
      minRole: 'viewer',
      about:
        'National exchange, and the record of it. Every exchange must be audited to take part, so this is mostly the trail — including the exchanges that failed, which is where a misconfigured partner shows up',
    },
    {
      id: 'flow',
      label: 'Flow map',
      minRole: 'viewer',
      about:
        'Every feed and destination on one map, with a scrubber — drag back to the moment a ward complained and see which strand was dark',
    },
    {
      id: 'playground',
      label: 'Playground',
      minRole: 'viewer',
      about:
        'Run the engine in this tab — paste a Mirth script and watch it work. Nothing is sent anywhere',
    },
    {
      id: 'shadow',
      label: 'Shadow',
      minRole: 'viewer',
      about:
        'Run a changed version of a channel beside the live one and see where they differ. It cannot deliver anything',
    },
    {
      id: 'certificates',
      label: 'Certificates',
      minRole: 'viewer',
      about: 'TLS certificates in use, and when they expire',
    },
    {
      id: 'users',
      label: 'Users',
      minRole: 'admin',
      about: 'Who can sign in, and what they are allowed to do',
    },
    {
      id: 'audit',
      label: 'Activity',
      minRole: 'viewer',
      about: 'Who changed what, and when',
    },
    {
      id: 'settings',
      label: 'Settings',
      minRole: 'admin',
      about: 'Alert rules, sign-on, and how this server was started',
    },
  ]
  // Compared by rank rather than by equality with 'admin'.
  //
  // The old test asked only whether a tab was admin-only, so any other minimum was
  // ignored and the tab shown to everybody - which for a tab whose endpoints require
  // editor means a viewer clicking it gets a 403 and concludes the product is broken.
  // Platform outranks admin, so it must not be excluded either.
  const roleRank: Record<string, number> = { viewer: 1, editor: 2, admin: 3, platform: 4 }
  const mine = roleRank[me.role] ?? 0
  const visible = tabs.filter((t) => mine >= (roleRank[t.minRole] ?? 0))

  // Splitting the bar.
  //
  // Filtered by role first, so a group holds only what this person may actually open. A menu listing
  // views that vanish on click, or that opens onto nothing for a viewer, is worse than no menu.
  const byId = new Map(visible.map((t) => [t.id, { id: t.id, label: t.label, about: t.about }]))
  const directTabs: NavItem[] = DIRECT_TABS.map((id) => byId.get(id)).filter(
    (t): t is NavItem => t !== undefined,
  )
  const groupItems = (members: Tab[]): NavItem[] =>
    members.map((id) => byId.get(id)).filter((t): t is NavItem => t !== undefined)

  // Everything the bar does not account for. Expected to be empty, and asserted to be.
  const placed = new Set<Tab>([...DIRECT_TABS, ...NAV_GROUPS.flatMap((g) => g.members)])
  const ungrouped: NavItem[] = visible
    .filter((t) => !placed.has(t.id))
    .map((t) => ({ id: t.id, label: t.label, about: t.about }))

  const commands: Command[] = [
    ...visible.map((t) => ({
      id: `tab:${t.id}`,
      label: t.label,
      group: 'Go to',
      hint: t.about,
      keywords: [t.id],
      run: () => setTab(t.id),
    })),
    ...channelNames.map((name) => ({
      id: `channel:${name}`,
      label: name,
      group: 'Channels',
      hint: 'channel',
      keywords: ['channel', name.replace(/-/g, ' ')],
      run: () => setTab('channels'),
    })),
    ...channelNames.map((name) => ({
      id: `messages:${name}`,
      label: `${name} · messages`,
      group: 'Look at',
      hint: 'messages',
      keywords: ['messages', 'browse', name],
      run: () => setTab('messages'),
    })),
    ...channelNames.map((name) => ({
      id: `queue:${name}`,
      label: `${name} · queue`,
      group: 'Look at',
      hint: 'queue',
      keywords: ['queue', 'stuck', 'backlog', name],
      run: () => setTab('queue'),
    })),
  ]

  return (
    <div className="min-h-screen relative">
      {/* Ambient gradient — gives depth without being distracting */}
      <div className="pointer-events-none fixed inset-0 z-0" aria-hidden="true">
        <div className="absolute -top-40 -left-40 h-96 w-96 rounded-full bg-sky-900/10 blur-3xl" />
        <div className="absolute -bottom-40 -right-40 h-96 w-96 rounded-full bg-emerald-900/8 blur-3xl" />
      </div>
      <header className="sticky top-0 z-40 border-b border-slate-800/80 bg-slate-950/90 backdrop-blur-xl shadow-lg shadow-black/10">
        <div className="mx-auto flex max-w-7xl flex-wrap items-center justify-between gap-4 px-6 py-3">
          <Logo />

          {/*
            The tablist holds only tabs, because ARIA permits nothing else inside one. The overflow
            trigger is a menu button and sits alongside it rather than within it.
          */}
          {/*
            Two direct tabs and four named groups, rather than seven tabs and a menu called More.
            
            The tablist holds only tabs, because ARIA permits nothing else inside one. The group
            triggers are menu buttons and sit alongside it.
          */}
          <div className="flex items-center gap-1">
            <nav className="flex items-center gap-1" role="tablist" aria-label="Perfuse console">
              {directTabs.map((t) => (
                <button
                  key={t.id}
                  role="tab"
                  id={`tab-${t.id}`}
                  aria-selected={tab === t.id}
                  aria-controls="view-panel"
                  onClick={() => setTab(t.id)}
                  title={t.about}
                  className={`inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-medium transition ${
                    tab === t.id
                      ? 'bg-slate-800 text-slate-100'
                      : 'text-slate-400 hover:bg-slate-900 hover:text-slate-200'
                  }`}
                >
                  <TabIcon label={t.label} />
                  {t.label}
                </button>
              ))}
              {/*
                When the selection lives in the overflow it still needs to be a tab in the tablist,
                or the tablist reports nothing selected and the current view is unnamed to a screen
                reader. It is visually hidden because the menu button already shows the same label.
              */}
              {!directTabs.some((t) => t.id === tab) && visible.some((t) => t.id === tab) && (
                <button
                  role="tab"
                  id={`tab-${tab}`}
                  aria-selected={true}
                  aria-controls="view-panel"
                  onClick={() => setTab(tab)}
                  className="sr-only"
                >
                  {visible.find((t) => t.id === tab)?.label ?? tab}
                </button>
              )}
            </nav>
            {NAV_GROUPS.map((g) => (
              <GroupMenu
                key={g.name}
                group={g}
                items={groupItems(g.members)}
                current={tab}
                onSelect={(id) => setTab(id)}
              />
            ))}

            {/*
              Anything grouped nowhere still has to be reachable.
              
              A view added to the tab list and left out of NAV_GROUPS would otherwise be invisible
              rather than merely misplaced, and invisible is the failure nobody reports because there
              is nothing on screen to report. A test asserts this is empty; this is what keeps the
              interface working while somebody is told about it.
            */}
            {ungrouped.length > 0 && (
              <GroupMenu
                group={{
                  name: 'Other',
                  about: 'Not yet grouped. This is a gap in the navigation, not a category',
                  accent: {
                    text: 'text-rose-300',
                    activeBg: 'bg-rose-500/15 text-rose-200',
                    ring: 'border-t-rose-500/60',
                    dot: 'bg-rose-400',
                  },
                  members: ungrouped.map((t) => t.id),
                }}
                items={ungrouped}
                current={tab}
                onSelect={(id) => setTab(id)}
              />
            )}
          </div>

          <div className="flex items-center gap-3">
            <span className="text-sm text-slate-400">{me.username}</span>
            <RoleBadge role={me.role} />
            <button
              className="btn-ghost hidden items-center gap-1.5 sm:flex"
              onClick={() => setPaletteOpen(true)}
              title="Search and navigate"
            >
              Search
              <kbd className="rounded border border-slate-700 px-1 text-[10px] text-slate-500">
                ⌘K
              </kbd>
            </button>
            {/* The manual ships in the binary and opens in a new tab. A plain anchor rather than a view, so it does not
                need a place in NAV_GROUPS and so the reader keeps whatever screen they were puzzled by open beside it —
                which is the whole situation the link exists for. */}
            <a
              className="btn-ghost hidden sm:inline-flex"
              href="/manual"
              target="_blank"
              rel="noreferrer"
              title="The reference manual, served from this instance"
            >
              Manual
            </a>
            <ThemePicker />
            <button className="btn-ghost" onClick={() => dispatch(signOut())}>
              Sign out
            </button>
          </div>
        </div>
      </header>

      <CommandPalette
        commands={commands}
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
      />

      <AlertBanner snapshot={alerts} onOpen={() => setTab('alerts')} />

      <main
        id="view-panel"
        role="tabpanel"
        aria-labelledby={`tab-${tab}`}
        className="mx-auto max-w-7xl px-6 py-6"
      >
        {tab === 'dashboard' && <Dashboard onBuildChannel={() => setTab('channels')} />}
        {tab === 'channels' && <Channels />}
        {tab === 'messages' && <Messages />}
        {tab === 'queue' && <Queue role={me.role as 'viewer' | 'editor' | 'admin'} />}
        {tab === 'alerts' && <Alerts role={me.role as 'viewer' | 'editor' | 'admin'} />}
        {tab === 'metrics' && <Metrics />}
        {tab === 'fhir' && <FhirLab />}
        {tab === 'documents' && <DocumentLab />}
        {tab === 'certificates' && <Certificates />}
        {tab === 'scripts' && (
          <Suspense fallback={<p className="text-sm text-slate-500">loading the editor…</p>}>
            <ScriptLab />
          </Suspense>
        )}
        {tab === 'migrate' && <MirthMigration />}
        {tab === 'contracts' && <Contracts />}
        {tab === 'fleet' && <Fleet />}
        {tab === 'tefca' && <TEFCAView />}
        {tab === 'tables' && <Tables />}
        {tab === 'mapper' && <MapperPanel />}
        {tab === 'flow' && (
          <Suspense fallback={<Spinner label="Loading the flow map…" />}>
            <FlowMap />
          </Suspense>
        )}
        {tab === 'playground' && (
          <Suspense fallback={<Spinner label="Loading the playground…" />}>
            <Playground />
          </Suspense>
        )}
        {tab === 'shadow' && <Shadow />}
        {tab === 'users' && (
          <div className="space-y-6">
            <Passkeys />
            <Users />
            <Tokens />
          </div>
        )}
        {tab === 'audit' && <Audit />}
        {tab === 'settings' && <Settings />}
      </main>
    </div>
  )
}

function Channels() {
  const dispatch = useDispatch<AppDispatch>()
  const me = useSelector((s: RootState) => s.session.me)!
  const { items, broken, loading, error } = useSelector((s: RootState) => s.channels)

  const [building, setBuilding] = useState(false)
  const [editing, setEditing] = useState<ChannelSummary | null>(null)
  const [deleting, setDeleting] = useState<ChannelSummary | null>(null)
  const [historyFor, setHistoryFor] = useState<string | null>(null)
  const [profileFor, setProfileFor] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})

  const canEdit = me.role === 'editor' || me.role === 'admin'

  // Search is hidden below a threshold. A box that filters nine items is clutter pretending to be a
  // feature, and every control that earns nothing costs attention on the ones that earn something.
  const showSearch = items.length >= 8

  const needle = query.trim().toLowerCase()
  const visible = needle
    ? items.filter((c) =>
        [c.name, c.description ?? '', c.group ?? '', c.listen ?? '', ...(c.destinations ?? [])]
          .join(' ')
          .toLowerCase()
          .includes(needle),
      )
    : items

  // Grouping only appears once something is actually grouped. A site with nine channels should not be
  // made to invent a taxonomy, and an "Ungrouped" heading over the entire list is pure noise.
  const showGroups = visible.some((c) => (c.group ?? '') !== '')

  const grouped = groupChannels(visible)

  useEffect(() => {
    dispatch(loadChannels())
  }, [dispatch])

  if (building || editing) {
    return (
      <div className="space-y-5">
        <div className="flex items-center gap-3">
          <button
            className="btn-ghost"
            onClick={() => {
              setBuilding(false)
              setEditing(null)
            }}
          >
            ← Back
          </button>
          <h1 className="text-lg font-semibold text-slate-100">
            {editing ? `Editing ${editing.name}` : 'New channel'}
          </h1>
        </div>
        <ChannelBuilder
          editing={editing}
          onClose={() => {
            setBuilding(false)
            setEditing(null)
          }}
        />
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h1 className="text-lg font-semibold text-slate-100">Channels</h1>
          <p className="mt-1 text-sm text-slate-500">
            Each channel is a file on disk. Anything built here can be edited by hand, and the other
            way round.
          </p>
        </div>
        {canEdit && (
          <button className="btn-primary" onClick={() => setBuilding(true)}>
            + New channel
          </button>
        )}
      </div>

      {error && <ErrorBox error={error} />}

      {/* A file that will not load is what an operator most needs to see, so it is
          shown rather than quietly omitted from the list. */}
      {Object.keys(broken).length > 0 && (
        <div className="rounded-lg border border-amber-900/60 bg-amber-950/30 p-4">
          <p className="text-sm font-medium text-amber-200">
            {Object.keys(broken).length} file(s) could not be loaded
          </p>
          <ul className="mt-2 space-y-2 text-xs">
            {Object.entries(broken).map(([file, reason]) => (
              <li key={file}>
                <code className="font-mono text-amber-300">{file}</code>
                <pre className="mt-1 whitespace-pre-wrap text-amber-200/70">{reason}</pre>
              </li>
            ))}
          </ul>
        </div>
      )}

      {showSearch && (
        <div className="flex items-center gap-3">
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            aria-label="Filter channels"
                placeholder="Filter by name, group, port or destination…"
            className="w-full rounded-lg border border-slate-700 bg-slate-950 px-3 py-2 text-sm
              text-slate-200 placeholder:text-slate-400 focus:border-sky-500 focus:outline-none"
          />
          {/* The count is shown while filtering, because a filter that hides things silently makes
              somebody wonder whether a channel was deleted. */}
          {needle !== '' && (
            <span className="shrink-0 text-sm text-slate-500">
              {visible.length} of {items.length}
            </span>
          )}
          {needle !== '' && (
            <button className="btn-ghost shrink-0" onClick={() => setQuery('')}>
              Clear
            </button>
          )}
        </div>
      )}

      {/* A filter that matches nothing says so, rather than leaving an empty page that looks broken. */}
      {needle !== '' && visible.length === 0 && (
        <div className="card p-8 text-center">
          <p className="text-slate-400">Nothing matches “{query}”.</p>
          <button className="btn-ghost mt-3" onClick={() => setQuery('')}>
            Clear the filter
          </button>
        </div>
      )}

      {loading && <Spinner label="Loading channels…" />}

      {!loading && items.length === 0 && Object.keys(broken).length === 0 && (
        <div className="card p-10 text-center">
          <p className="text-slate-400">No channels yet.</p>
          {canEdit && (
            <button className="btn-primary mt-4" onClick={() => setBuilding(true)}>
              Build your first channel
            </button>
          )}
        </div>
      )}

      <div className="grid gap-4">
        {grouped.map((g) => (
          <Fragment key={g.name}>
            {showGroups && (
              <GroupHeading
                name={g.name}
                total={g.items.length}
                running={g.items.filter((c) => c.enabled).length}
                collapsed={collapsed[g.name] ?? false}
                onToggle={() =>
                  setCollapsed((s) => ({ ...s, [g.name]: !(s[g.name] ?? false) }))
                }
              />
            )}
            {(collapsed[g.name] ? [] : g.items).map((c) => (
          <div key={c.name} className="card p-5">
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div className="min-w-0">
                <div className="flex items-center gap-3">
                  <h2 className="truncate text-base font-semibold text-slate-100">{c.name}</h2>
                  <StatusDot on={c.enabled} />
                </div>
                {c.description && (
                  <p className="mt-1 max-w-2xl text-sm text-slate-500">{c.description}</p>
                )}
              </div>

              <div className="flex shrink-0 gap-2">
                <button
                  className="btn-ghost"
                  onClick={() => setProfileFor((current) => (current === c.name ? null : c.name))}
                  title="What is actually arriving on this channel"
                >
                  {profileFor === c.name ? 'Hide feed' : 'Feed'}
                </button>
                <button
                  className="btn-ghost"
                  onClick={() => setHistoryFor((current) => (current === c.name ? null : c.name))}
                >
                  {historyFor === c.name ? 'Hide history' : 'History'}
                </button>
                <a
                  className="btn-ghost"
                  href={api.specUrl(c.name)}
                  download
                  title="Download the interface specification, generated from this channel so it cannot be out of date"
                >
                  Spec
                </a>
                <a
                  className="btn-ghost"
                  href={api.exportUrl(c.name)}
                  download
                  title="Download the channel file itself"
                >
                  Export
                </a>
                <MirthExportButton channel={c.name} />
                {canEdit && (
                  <>
                    <button className="btn-ghost" onClick={() => setEditing(c)}>
                      Edit
                    </button>
                    <button className="btn-danger" onClick={() => setDeleting(c)}>
                      Delete
                    </button>
                  </>
                )}
              </div>
            </div>

            <dl className="mt-4 grid gap-4 text-sm sm:grid-cols-2 lg:grid-cols-4">
              <div>
                <dt className="label">Listens on</dt>
                <dd className="font-mono text-slate-300">{c.listen}</dd>
              </div>
              <div>
                <dt className="label">Acknowledges</dt>
                <dd className="text-slate-300">
                  {c.ackWhen === 'on_delivery' ? 'after delivery' : 'on receipt'}
                </dd>
              </div>
              <div>
                <dt className="label">Sends to</dt>
                <dd className="text-slate-300">{c.destinations.join(', ') || '—'}</dd>
              </div>
              <div>
                <dt className="label">File</dt>
                <dd className="font-mono text-xs text-slate-500">{c.file}</dd>
              </div>
            </dl>

            {c.filter && (
              <div className="mt-4 rounded-lg border border-slate-800 bg-slate-950/60 p-3">
                <p className="label mb-1">Keeps messages where</p>
                <code className="font-mono text-xs break-all text-sky-300">{c.filter}</code>
              </div>
            )}

            {profileFor === c.name && (
              <div className="mt-4 border-t border-slate-800 pt-4">
                <FeedProfilePanel channel={c.name} />
              </div>
            )}

            {historyFor === c.name && (
              <div className="mt-4 border-t border-slate-800 pt-4">
                <p className="label mb-3">Who changed what</p>
                <ChannelHistoryPanel
                  channel={c.name}
                  canRestore={canEdit}
                  onRestored={() => dispatch(loadChannels())}
                />
              </div>
            )}
          </div>
            ))}
          </Fragment>
        ))}
      </div>

      <Confirm
        open={deleting !== null}
        title={`Delete ${deleting?.name}?`}
        body={
          <>
            The file <code className="font-mono text-slate-300">{deleting?.file}</code> will be
            removed. If it is in git you can get it back; otherwise this cannot be undone.
          </>
        }
        confirmLabel="Delete channel"
        onConfirm={() => {
          if (deleting) dispatch(removeChannel(deleting.name))
          setDeleting(null)
        }}
        onCancel={() => setDeleting(null)}
      />
    </div>
  )
}

function Users() {
  const dispatch = useDispatch<AppDispatch>()
  const me = useSelector((s: RootState) => s.session.me)!
  const { items, loading, error } = useSelector((s: RootState) => s.users)

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<'viewer' | 'editor' | 'admin'>('viewer')
  const [deleting, setDeleting] = useState<{ id: number; username: string } | null>(null)

  useEffect(() => {
    dispatch(loadUsers())
  }, [dispatch])

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-lg font-semibold text-slate-100">Users</h1>
        <p className="mt-1 text-sm text-slate-500">
          Viewers can look. Editors can change channels. Admins can also manage people.
        </p>
      </div>

      {error && <ErrorBox error={error} onDismiss={() => dispatch(clearUsersError())} />}

      <Section title="Add someone">
        <form
          className="grid gap-4 sm:grid-cols-4"
          onSubmit={(e) => {
            e.preventDefault()
            dispatch(addUser({ username, password, role })).then(() => {
              setUsername('')
              setPassword('')
            })
          }}
        >
          <Field label="Username">
            <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} />
          </Field>
          <Field label="Password" hint="At least 12 characters.">
            <input
              className="input"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
            />
          </Field>
          <Field label="Role">
            <select
              className="select"
              value={role}
              onChange={(e) => setRole(e.target.value as typeof role)}
            >
              <option value="viewer">Viewer</option>
              <option value="editor">Editor</option>
              <option value="admin">Admin</option>
            </select>
          </Field>
          <div className="flex items-end">
            <button className="btn-primary w-full">Add user</button>
          </div>
        </form>
      </Section>

      {loading && <Spinner label="Loading users…" />}

      <div className="card overflow-hidden">
        <table className="w-full text-sm">
          <thead className="border-b border-slate-800 bg-slate-900/60 text-left">
            <tr className="text-xs tracking-wide text-slate-500 uppercase">
              <th className="px-5 py-3">User</th>
              <th className="px-5 py-3">Role</th>
              <th className="px-5 py-3">Last signed in</th>
              <th className="px-5 py-3 text-right">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-800">
            {items.map((u) => (
              <tr key={u.id} className={u.disabled ? 'opacity-50' : ''}>
                <td className="px-5 py-3">
                  <span className="font-medium text-slate-200">{u.username}</span>
                  {u.disabled && <span className="ml-2 text-xs text-rose-400">disabled</span>}
                  {u.username === me.username && (
                    <span className="ml-2 text-xs text-slate-500">you</span>
                  )}
                </td>
                <td className="px-5 py-3">
                  {/* Your own role and account are not yours to change here.
                      
                      This dropdown saves the moment it changes, so on your own row it was one click from
                      removing your own administrator rights - and the sections needed to undo it are the
                      first thing you lose. The server refuses it too; this is so nobody has to discover the
                      refusal by triggering it. */}
                  <select
                    aria-label={
                      u.username === me.username
                        ? `Your own role cannot be changed here; ask another administrator`
                        : `Role for ${u.username}`
                    }
                    disabled={u.username === me.username}
                    title={
                      u.username === me.username
                        ? 'Ask another administrator to change your role. Doing it yourself would remove the access needed to undo it.'
                        : undefined
                    }
                    className="select w-32 py-1 text-xs disabled:cursor-not-allowed disabled:opacity-60"
                    value={u.role}
                    onChange={(e) =>
                      dispatch(
                        patchUser({ id: u.id, patch: { role: e.target.value as typeof role } }),
                      )
                    }
                  >
                    <option value="viewer">viewer</option>
                    <option value="editor">editor</option>
                    <option value="admin">admin</option>
                  </select>
                </td>
                <td className="px-5 py-3 text-slate-500">
                  {u.lastLogin ? new Date(u.lastLogin).toLocaleString() : 'never'}
                </td>
                <td className="px-5 py-3">
                  <div className="flex justify-end gap-2">
                    <button
                      className="btn-ghost py-1 text-xs disabled:cursor-not-allowed disabled:opacity-60"
                      disabled={u.username === me.username}
                      title={
                        u.username === me.username
                          ? 'Ask another administrator to disable your account. Doing it yourself would sign you out with no way back in.'
                          : undefined
                      }
                      aria-label={
                        u.username === me.username
                          ? 'You cannot disable your own account'
                          : `${u.disabled ? 'Enable' : 'Disable'} ${u.username}`
                      }
                      onClick={() =>
                        dispatch(patchUser({ id: u.id, patch: { disabled: !u.disabled } }))
                      }
                    >
                      {u.disabled ? 'Enable' : 'Disable'}
                    </button>
                    <button
                      className="btn-danger py-1 text-xs"
                      onClick={() => setDeleting({ id: u.id, username: u.username })}
                    >
                      Delete
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <Confirm
        open={deleting !== null}
        title={`Delete ${deleting?.username}?`}
        body="Their sessions end immediately. What they did stays in the activity log."
        confirmLabel="Delete user"
        onConfirm={() => {
          if (deleting) dispatch(removeUser(deleting.id))
          setDeleting(null)
        }}
        onCancel={() => setDeleting(null)}
      />
    </div>
  )
}

function Audit() {
  const dispatch = useDispatch<AppDispatch>()
  const { entries, loading, error } = useSelector((s: RootState) => s.audit)
  const me = useSelector((s: RootState) => s.session.me)

  useEffect(() => {
    dispatch(loadAudit())
  }, [dispatch])

  return (
    <div className="space-y-5">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-lg font-semibold text-slate-100">Activity</h1>
          <p className="mt-1 text-sm text-slate-500">
            Who did what, newest first. Failed sign-in attempts are recorded too.
          </p>
        </div>
        <button className="btn-ghost" onClick={() => dispatch(loadAudit())}>
          Refresh
        </button>
      </div>

      {error && <ErrorBox error={error} />}
      {loading && <Spinner label="Loading activity…" />}

      {/*
        The friction report sits above the audit trail because it answers a different question. The trail
        says what happened; this says what was refused, which is the only evidence in the product that did
        not come from whoever wrote it. Admin only, so it renders empty for anybody else.
      */}
      {me?.role === 'admin' && <FrictionPanel />}

      <div className="card overflow-hidden">
        <table className="w-full text-sm">
          <thead className="border-b border-slate-800 bg-slate-900/60 text-left">
            <tr className="text-xs tracking-wide text-slate-500 uppercase">
              <th className="px-5 py-3">When</th>
              <th className="px-5 py-3">Who</th>
              <th className="px-5 py-3">Action</th>
              <th className="px-5 py-3">What</th>
              <th className="px-5 py-3">From</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-800">
            {entries.map((e) => (
              <tr key={e.id}>
                <td className="px-5 py-2.5 whitespace-nowrap text-slate-500">
                  {new Date(e.at).toLocaleString()}
                </td>
                <td className="px-5 py-2.5 text-slate-300">{e.username}</td>
                <td className="px-5 py-2.5">
                  <code
                    className={`font-mono text-xs ${
                      e.action.includes('failed') || e.action.includes('delete')
                        ? 'text-rose-300'
                        : 'text-slate-400'
                    }`}
                  >
                    {e.action}
                  </code>
                </td>
                <td className="px-5 py-2.5 text-slate-400">
                  {e.target}
                  {e.detail && <span className="ml-2 text-slate-400">{e.detail}</span>}
                </td>
                <td className="px-5 py-2.5 font-mono text-xs text-slate-400">{e.ip}</td>
              </tr>
            ))}
            {!loading && entries.length === 0 && (
              <tr>
                <td colSpan={5} className="px-5 py-10 text-center text-slate-500">
                  Nothing recorded yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}


/**
 * Grouping the channel list.
 *
 * Ungrouped channels come last under a plain heading rather than first, because a site part-way through
 * organising itself should see the work it has done above the work it has not.
 */
function groupChannels(items: ChannelSummary[]): { name: string; items: ChannelSummary[] }[] {
  const byGroup = new Map<string, ChannelSummary[]>()

  for (const c of items) {
    const key = (c.group ?? '').trim()
    const list = byGroup.get(key)
    if (list) list.push(c)
    else byGroup.set(key, [c])
  }

  const named = [...byGroup.entries()]
    .filter(([name]) => name !== '')
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([name, items]) => ({ name, items }))

  const ungrouped = byGroup.get('')
  if (ungrouped && ungrouped.length > 0) {
    named.push({ name: 'Ungrouped', items: ungrouped })
  }

  return named
}

/**
 * A group heading that carries its own health, so a collapsed group is still informative.
 *
 * The whole point of collapsing is to stop reading a group you do not care about, and a heading that
 * says only a name forces you to expand it to find out whether anything is wrong. So the count of what
 * is running is on the heading, and it goes amber when some of the group is stopped.
 */
function GroupHeading({
  name,
  total,
  running,
  collapsed,
  onToggle,
}: {
  name: string
  total: number
  running: number
  collapsed: boolean
  onToggle: () => void
}) {
  const allRunning = running === total
  const noneRunning = running === 0

  return (
    <button
      onClick={onToggle}
      className="flex w-full items-center gap-3 rounded-lg px-1 py-1.5 text-left
        hover:bg-slate-800/40"
    >
      <span
        className={`text-slate-500 transition-transform ${collapsed ? '' : 'rotate-90'}`}
        aria-hidden
      >
        ▶
      </span>
      <span className="text-sm font-semibold uppercase tracking-wide text-slate-400">{name}</span>
      <span
        className="rounded-full px-2 py-0.5 text-xs"
        style={
          allRunning
            ? { background: 'rgba(52,211,153,.12)', color: '#6ee7b7' }
            : noneRunning
              ? { background: 'rgba(148,163,184,.12)', color: '#94a3b8' }
              : { background: 'rgba(251,191,36,.14)', color: '#fcd34d' }
        }
        title={
          allRunning
            ? 'every channel in this group is enabled'
            : `${total - running} of ${total} are disabled`
        }
      >
        {running}/{total} on
      </span>
      <span className="h-px flex-1 bg-slate-800" />
    </button>
  )
}

/** A key, for the federated sign-in button.
 *
 *  Hand-drawn rather than an icon dependency, matching the charts: one more package for one
 *  more glyph is not a trade worth making. */
function KeyIcon() {
  return (
    <svg
      className="h-4 w-4"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3" />
    </svg>
  )
}
