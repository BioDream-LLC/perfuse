// The single place the front end talks to the server.
//
// Every state-changing request carries X-Perfuse-Request. The server rejects
// state-changing requests without it, which is what makes the SameSite session
// cookie sufficient protection against a cross-site form submission.

export type Role = 'viewer' | 'editor' | 'admin'

export interface Me {
  username: string
  role: Role
}

/** How this server allows people to sign in. */
export interface AuthMethods {
  /** Whether the username and password form should be shown.
   *
   *  Always true today. Local accounts are the way in when the identity provider is
   *  unreachable, which for a server-room application is a real situation rather than a
   *  hypothetical one - so the form is never hidden. */
  password: boolean

  /** The LDAP directory, absent when none is configured.
   *
   *  No button of its own: a directory sign-in uses the same username and password form.
   *  What this changes is what the form says, so somebody knows to type the credentials they
   *  already have rather than looking for an account nobody created for them. */
  directory?: {
    label: string
  }

  /** Whether a passkey sign-in is offered.
   *
   *  False when the server has no relying party configured, in which case the endpoints are not
   *  registered and a button would answer 404 - which reads as a broken product rather than an
   *  unconfigured one. */
  passkeys?: boolean

  /** The federated option, absent when none is configured. */
  oidc?: {
    label: string
    startAt: string
  }

  /** SAML, absent when none is configured.
   *
   * The same shape as oidc, because the button is the same button: a label and somewhere to send the browser. Both may be present at
   * once - a site migrating between providers runs both for a while - so the sign-in page renders a list rather than one or the other.
   */
  saml?: {
    label: string
    startAt: string
  }
}

export interface ChannelSummary {
  name: string
  description?: string
  /** Absent means ungrouped, which is a legitimate answer rather than a missing value. */
  group?: string
  enabled: boolean
  listen: string
  sourceType: string
  ackWhen: string
  filter?: string
  destinations: string[]
  reads?: string[]
  file: string
}

/** InspectedCertificate is what a certificate turns out to be, once read. */
export interface InspectedCertificate {
  subject: string
  issuer: string
  serialNumber: string
  notBefore: string
  notAfter: string
  /** Negative once expired. */
  daysRemaining: number
  /** One of ok, expiring, expired or not-yet-valid. */
  status: string
  /** The subject alternative names, which are what verification actually uses. */
  hosts?: string[]
}

/** FleetPeer is one watched instance. The token is deliberately absent: it is a credential for that server. */
export interface FleetPeer {
  name: string
  url: string
  hasToken: boolean
  allowControl: boolean
}

/** FleetPeers is the watched instances and whether they can be changed here. */
export interface FleetPeers {
  /** Never null; an empty fleet is the normal starting state. */
  peers: FleetPeer[]
  /** False when this server has nowhere to store peers, in which case the form is not offered. */
  writable: boolean
  file?: string
}

/** ProposedContract is a contract built from observed traffic, offered for review before saving. */
export interface ProposedContract {
  channel: string
  /** How many messages were read. Four is a guess; four hundred is evidence, and the difference matters. */
  messages: number
  yaml: string
  /** Things worth knowing before saving. Never null. */
  notes: string[]
  expectations: number
}

export interface ChannelResponse {
  channel: ChannelSummary
  yaml: string
}

export interface ChannelListResponse {
  channels: ChannelSummary[]
  broken?: Record<string, string>
}

export interface User {
  id: number
  username: string
  role: Role
  disabled: boolean
  createdAt: string
  lastLogin?: string
}

export interface ChannelState {
  name: string
  running: boolean
  startedAt?: string
  error?: string
  received: number
  delivered: number
  filtered: number
  partial: number
  failed: number
  unparseable: number
  destinations?: { name: string; delivered: number; failed: number; filtered: number }[]
}

export interface StatusResponse {
  channels: ChannelState[]
  channelsTotal: number
  channelRunning: number
  /** Files in the channel directory that would not load at all. Not the same as a channel that is stopped. */
  channelsBroken?: number
  fhirVersion?: string | null
  fhirResources?: Record<string, number>
  time: string
}

export interface StoredMessage {
  id: number
  channel: string
  receivedAt: string
  controlId?: string
  messageType?: string
  triggerEvent?: string
  sender?: string
  outcome: string
  ackCode?: string
  size: number
  durationMs: number
  error?: string
  segments?: number
  raw?: string
  deliveries?: {
    destination: string
    status: string
    attempts: number
    durationMs: number
    error?: string
  }[]
  /** Payloads stored outside the message, described rather than included. */
  attachments?: {
    digest: string
    size: number
    firstSeen: string
    /** How many stored messages share this payload — the number that makes deduplication visible. */
    messages: number
  }[]
  /** Why the message could not be fully reassembled, absent when it could. */
  attachmentError?: string
}

export interface ComponentView {
  number: number
  path: string
  name?: string
  value: string
}

export interface FieldView {
  number: number
  path: string
  name: string
  description?: string
  value: string
  raw: string
  empty: boolean
  repeats?: number
  meaning?: string
  components?: ComponentView[]
}

export interface SegmentView {
  index: number
  name: string
  description: string
  known: boolean
  raw: string
  fields: FieldView[]
}

export interface MessageView {
  messageType: string
  triggerEvent: string
  eventMeaning?: string
  controlId: string
  version: string
  sender: string
  separators: string
  segments: SegmentView[]
}

export interface Bucket {
  start: string
  total: number
  delivered: number
  filtered: number
  failed: number
  unparseable: number
}

export interface MessageStats {
  total: number
  byOutcome: Record<string, number>
  byChannel: Record<string, number>
  byType: Record<string, number>
  storedBytes: number
  oldestKept?: string
  newestKept?: string
}

export interface DestinationStat {
  destination: string
  delivered: number
  failed: number
  filtered: number
  avgAttempts: number
  avgDurationMs: number
}

export interface ConversionNote {
  severity: string
  source?: string
  target?: string
  message: string
}

export interface ValidationFinding {
  severity: string
  path: string
  message: string
  rule: string
}

export interface ConversionResult {
  bundle: unknown
  notes: ConversionNote[]
  counts: Record<string, number>
  messageType: string
  triggerEvent: string
  version: string
  validation: {
    valid: boolean
    errors: number
    warnings: number
    notes: number
    findings: ValidationFinding[]
  }
}

/** A group of refusals that share a message, counted. */
export interface FrictionGroup {
  route: string
  status: number
  message: string
  count: number
  first: string
  last: string
  problems: number
}

/** How far this installation has got towards a working channel.
 *
 *  Every value is read from something recorded for another purpose - the audit trail, the channel files,
 *  the message store - rather than from a milestone the server writes about itself. */
export interface FrictionFunnel {
  firstSignIn?: string
  firstChannel?: string
  firstMessage?: string
  channelCount: number
  messageCount: number
  delivered: number
  stuckAt: string
  minutesToFirstMessage?: number
}

/** The friction report: what using this has actually cost somebody. */
export interface FrictionReport {
  refusals: FrictionGroup[]
  total: number
  funnel: FrictionFunnel
}

export interface AuditEntry {
  id: number
  at: string
  username: string
  action: string
  target?: string
  detail?: string
  ip?: string
}

/**
 * ApiError carries the server's message and, for a validation failure, every
 * individual problem. Showing all of them at once is the difference between
 * fixing a channel in one pass and fixing it one error per attempt.
 */
export class ApiError extends Error {
  status: number
  problems: string[]

  constructor(status: number, message: string, problems: string[] = []) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.problems = problems
  }
}

export interface ScriptDiagnostic {
  line: number
  column?: number
  message: string
}

export interface ScriptNote {
  line: number
  kind: string
  detail: string
  snippet?: string
}

export interface ScriptCheck {
  ok: boolean
  kind: string

  /** language echoes which compiler answered, so the pane cannot show the wrong one's diagnostics. */
  language: string
  error?: ScriptDiagnostic
  rewritten: boolean
  translated?: string
  notes: ScriptNote[]
}

export interface DictField {
  number: number
  name: string
  description?: string
  components?: string[]
  table?: string
  repeats?: boolean
}

export interface DictSegment {
  segment: string
  description: string
  fields: DictField[]
}

export interface ChannelProblem {
  line?: number
  message: string
}

export interface ChannelPlain {
  name: string
  enabled: boolean
  dataType: string
  receives: string
  acknowledges: string
  filter?: string
  steps?: string[]
  sends: string[]
  warnings?: string[]
}

export interface BuildResult {
  yaml: string
  ok: boolean
  problems: ChannelProblem[]
  summary?: ChannelPlain
}

export interface ValidateResult {
  ok: boolean
  problems: ChannelProblem[]
  summary?: ChannelPlain
}

export interface ReplayFieldDifference {
  path: string
  live: string
  candidate: string
}

export interface ReplayDifference {
  controlId: string
  messageType: string
  fields?: ReplayFieldDifference[]
  verdict?: string
}

export interface ReplayReport {
  examined: number
  identical: number
  changed: number
  unparseable: number
  differences: ReplayDifference[]
  truncated?: boolean
  fieldCounts?: Record<string, number>
  available: number
  problems?: ChannelProblem[]
}

export interface ProfileCode {
  code: string
  count: number
  meaning?: string
  known: boolean
}

export interface ProfileField {
  path: string
  name?: string
  present: number
  fillRate: number
  distinct: number
  distinctCapped?: boolean
  shape: string
  minLength: number
  maxLength: number
  maxRepeats: number
  components?: number
  codes?: ProfileCode[]
  table?: string
}

export interface ProfileSegment {
  id: string
  name?: string
  standard: boolean
  messages: number
  rate: number
  maxPerMessage: number
  fields: ProfileField[]
}

export interface FeedProfile {
  messages: number
  unreadable: number
  types: { type: string; count: number; rate: number }[]
  segments: ProfileSegment[]
  notes?: string[]
  available: number
}

export interface TraceValue {
  path: string
  value: string
  present: boolean
}

export interface TraceChange {
  step: string
  path: string
  from: string
  to: string
  note?: string
}

/** TraceSkip is a step that ran and changed nothing.
 *
 * The most useful part of a trace. A step addressing a field the sender does not populate is not an
 * error and produces no log line - it simply does nothing, and the only prior evidence was a change
 * count lower than somebody expected.
 */
export interface TraceSkip {
  step: string
  path?: string
  why: string
}

/** TraceStep is one declarative step and the message immediately after it ran.
 *
 * The stage above says what changed. This says which step changed it, and what the message looked
 * like just before - which is the question somebody has when a field is wrong and the pipeline is
 * fourteen steps long.
 */
export interface TraceStep {
  number: number
  label: string
  path?: string
  /** changed, no-effect, skipped or failed.
   *
   * no-effect and skipped look identical from outside - the message comes out unchanged either way -
   * and mean opposite things. Skipped is the step working exactly as written. no-effect is almost
   * always the bug. */
  outcome: 'changed' | 'no-effect' | 'skipped' | 'failed'
  detail: string
  changes?: TraceChange[]
  message: string
}

export interface TraceStage {
  stage: string
  ok: boolean
  detail: string
  reads?: TraceValue[]
  changes?: TraceChange[]
  skipped?: TraceSkip[]
  /** Only on the transform stage. */
  steps?: TraceStep[]
}

export interface TraceDestination {
  name: string
  would: boolean
  why: string
  reads?: TraceValue[]
}

export interface MessageTrace {
  stages: TraceStage[]
  accepted: boolean
  input: string
  output?: string
  destinations?: TraceDestination[]
  caveat?: string
  /** stale means the channel has changed since the message arrived, so this describes what would
   * happen now rather than what happened then. */
  stale?: boolean
}

/** SampleReading is what a set of pasted messages supports concluding.
 *
 * observed, guessed and unknowable are three lists rather than one for a reason: a reader who cannot
 * tell which is which has to verify everything or trust everything, and both are worse than knowing
 * where to look.
 */
export interface SampleReading {
  messages: number
  unreadable: number
  types: { type: string; count: number; rate: number }[]
  observed: string[]
  guessed: string[]
  unknowable: string[]
  yaml: string
  confidence: string
}

/** ParityReport is how Perfuse's output compares with the engine being replaced.
 *
 * identical, equivalent, differing and failed are four numbers rather than a pass rate, because they
 * mean four different things and collapsing them hides the ones that need a decision.
 */
export interface ParityReport {
  compared: number
  identical: number
  equivalent: number
  differing: number
  failed: number
  failureReasons: { error: string; messages: number; reference?: string }[]
  findings: {
    path: string
    messages: number
    distinctPairs: number
    /** true when every occurrence differed the same way, which makes it one decision. */
    systematic: boolean
    examples: { expected: string; got: string; count: number; reference?: string }[]
  }[]
  verdict: string
}

export interface GeneratedCorpus {
  messages: number
  corpus: string
  learnedFrom: {
    messages: number
    types: { type: string; count: number; rate: number }[]
    segments: number
  }
  seed: number
  note: string
}

/** One message that matched a patient search, with the reason it matched. */
export interface IdentityMatch {
  message: StoredMessage
  /** Which sort of identifier was hit, so a page of similar messages says why each is here. */
  matchedKind: string
  matchedValue: string
}

export interface IdentitySearchResult {
  matches: IdentityMatch[]
  total: number
  /**
   * Whether identity indexing is switched on at all.
   *
   * Without this an empty result is ambiguous between "no message mentions that patient" and
   * "nothing was ever indexed here", and those lead to opposite conclusions. The console shows
   * a different message for each.
   */
  indexed: boolean
  /** The kinds that can be searched, from the server, so the filter cannot drift from it. */
  kinds: string[]
}

export interface ContentSearchResult {
  matches: StoredMessage[]
  examined: number
  available: number
  unreadable: number
  truncated?: boolean
  paths?: string[]
}

import type { PasskeyCreationOptions, PasskeyRequestOptions } from './passkey'
import type { SettingsSchema } from './settingsControls'
import type { V3FieldTree, V3PathCheck } from './V3FieldPicker'
import type { WireModel } from './wireToDraft'


/** One configuration file the interface may edit.
 *
 *  The interface edits the same files the flags point at, rather than keeping a second copy
 *  in the database - which would raise the question of which wins, and the answer would be
 *  discovered during an incident. */
export interface SettingsFile {
  kind: 'alerts' | 'oidc' | 'ldap' | 'peers'
  path?: string
  /** Whether the server was started with this file at all. */
  configured: boolean
  /** Whether it exists on disk. A configured path with no file is a real state. */
  exists: boolean
  content: string
  /** Whether credentials were hidden, so the editor can refuse to save a placeholder over a real secret. */
  redacted: boolean
  modifiedAt?: string
  bytes: number
  restartRequired: boolean
  description: string
}

/** What the process was started with. Read-only.
 *
 *  Shown rather than hidden: somebody diagnosing a problem needs to know how the process
 *  was started, and "not shown anywhere" is how a wrong flag survives for months. */
export interface StartupSettings {
  addr: string
  channelsDir: string
  databasePath: string
  tls: boolean
  multiTenant: boolean
  engineRunning: boolean
  fhirServing: boolean
  storeMessages: boolean
  storePayloads: boolean
  retentionDays: number
  allowMetadataEgress: boolean
  jsonLogs: boolean
  version?: string
  goVersion?: string
  platform?: string
  startedAt: string
  commandLine?: string[]
  fleetLabel?: string
  traceEndpoint?: string
  alertWebhook: boolean
  alertSeverity?: string
  authMethods: string[]
  metadataReason?: string
}

export interface SettingsResponse {
  files: SettingsFile[]
  startup: StartupSettings
  canEdit: boolean
}


/** One API token, without its value.
 *
 *  Only a hash is stored, so a value cannot be shown again even deliberately. Revoked tokens
 *  are listed rather than hidden, because the activity log names them and an entry pointing
 *  at something invisible is worse than a row marked withdrawn. */
export interface ApiToken {
  label: string
  role: string
  createdBy?: string
  createdAt?: string
  lastUsed?: string
  revoked: boolean
  revokedAt?: string
}

/** The one and only sight of a new token's value. */
export interface NewApiToken {
  label: string
  role: string
  token: string
  note: string
}


/** One passkey, without key material.
 *
 *  A public key would be harmless to send and there is nothing the interface does with it,
 *  so it is not sent - bytes nobody uses invite somebody to start using them. */
export interface Passkey {
  id: string
  label: string
  algorithm: string
  userVerified: boolean
  createdAt?: string
  lastUsed?: string
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  const mutating = method !== 'GET' && method !== 'HEAD'
  if (mutating) headers['X-Perfuse-Request'] = '1'
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    // Same origin in production; the dev server proxies /api so cookies still
    // apply without CORS.
    credentials: 'same-origin',
  })

  if (res.status === 204) return undefined as T

  const text = await res.text()
  let parsed: unknown = undefined
  if (text) {
    try {
      parsed = JSON.parse(text)
    } catch {
      // A non-JSON body from an error is still worth surfacing verbatim.
      if (!res.ok) throw new ApiError(res.status, text.slice(0, 500))
      throw new ApiError(res.status, 'the server sent a response that could not be read')
    }
  }

  if (!res.ok) {
    const err = parsed as { error?: string; problems?: string[] } | undefined
    throw new ApiError(res.status, err?.error ?? `request failed (${res.status})`, err?.problems ?? [])
  }

  return parsed as T
}

/**
 * requestBlob fetches a binary body, for endpoints that return a file rather than JSON.
 *
 * Separate from request rather than a flag on it, because request reads the body as text and parses it. Text
 * decoding a PDF corrupts it silently: the bytes arrive, invalid UTF-8 sequences are replaced, and the file opens
 * as damaged with nothing anywhere saying why.
 *
 * A failure still comes back as JSON, so the error path parses and the success path does not.
 */
async function requestBlob(
  method: string,
  path: string,
  body?: unknown,
): Promise<{ blob: Blob; filename: string }> {
  const headers: Record<string, string> = { 'X-Perfuse-Request': '1' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  })

  if (!res.ok) {
    // The refusal is JSON even though success is not, so the message reaches the person.
    const text = await res.text()
    try {
      const err = JSON.parse(text) as { error?: string; problems?: string[] }
      throw new ApiError(res.status, err.error ?? `request failed (${res.status})`, err.problems ?? [])
    } catch (e) {
      if (e instanceof ApiError) throw e
      throw new ApiError(res.status, text.slice(0, 500) || `request failed (${res.status})`)
    }
  }

  // The server's filename is used when it sends one, because it built the name from the document's own contents
  // and the browser has no idea what is in the file.
  const disposition = res.headers.get('Content-Disposition') ?? ''
  const match = /filename="([^"]+)"/.exec(disposition)

  return { blob: await res.blob(), filename: match?.[1] ?? 'document.pdf' }
}

/** FlowDestination is one strand: a configured destination and what it carried. */
export interface FlowDestination {
  name: string
  how: string
  durable: boolean
  /** buckets covers the whole window including the quiet parts, so a stopped strand is zeros rather than absent. */
  buckets: Bucket[]
  /** everUsed distinguishes a strand that stopped from one that never started. */
  everUsed: boolean
}

/** FlowNode is one channel on the map. */
export interface FlowNode {
  channel: string
  running: boolean
  broken: boolean
  reason?: string
  /** narration is what this channel does, in sentences, so a strand can be explained and not only drawn. */
  narration: string[]
  destinations: FlowDestination[]
  received: Bucket[]
}

export interface FlowResponse {
  since: string
  until: string
  bucket: string
  window: string
  nodes: FlowNode[]
}

export const api = {
  login: (username: string, password: string) =>
    request<Me>('POST', '/api/login', { username, password }),

  /** How this server allows people to sign in.
   *
   *  Read before signing in, so it is the one call that must work without a session. */
  authMethods: () => request<AuthMethods>('GET', '/api/auth/methods'),

  v3Fields: (message: string) => request<V3FieldTree>('POST', '/api/hl7v3/fields', { message }),
  v3CheckPath: (path: string, message: string) =>
    request<V3PathCheck>('POST', '/api/hl7v3/path', { path, message }),

  /** Mapping suggestions for a set of source and target fields.
   *
   *  Through this client rather than a fetch of its own, which is how it was written and why it never
   *  worked: a raw POST carries no X-Perfuse-Request header, so every request was refused as
   *  cross-site. The panel reported the refusal as an error and looked like a model that could not
   *  answer. */
  suggestMappings: (input: {
    fields: { name: string; examples: string[]; codeSystem: string }[]
    targets: { name: string; codeSystem: string; pattern: string }[]
    threshold: number
  }) => request<{ suggestions: unknown[] }>('POST', '/api/mapper/suggest', input),

  settingsSchema: () => request<SettingsSchema>('GET', '/api/settings/schema'),
  saveSettingValues: (changes: Record<string, unknown>) =>
    request<{ applied: string[]; restartRequired: string[]; unchanged: string[] }>(
      'PUT',
      '/api/settings/values',
      { changes },
    ),

  signon: () => request<SignonDocument>('GET', '/api/signon'),
  saveOIDC: (config: OIDCConfig, newSecret?: string) =>
    request<{ path: string; note: string }>('PUT', '/api/signon/oidc', {
      config,
      // Absent unless somebody typed one, so a save that did not touch the box keeps the secret on disk. An empty string has to be
      // able to mean "clear it", which is why this is optional rather than defaulting to ''.
      ...(newSecret === undefined ? {} : { newSecret }),
    }),
  saveLDAP: (config: LDAPConfig, newPassword?: string) =>
    request<{ path: string; note: string }>('PUT', '/api/signon/ldap', {
      config,
      ...(newPassword === undefined ? {} : { newPassword }),
    }),
  testOIDC: (issuer: string) => request<SignonTestResult>('POST', '/api/signon/oidc/test', { issuer }),

  /** saveSAML writes the SAML configuration.
   *
   * No secret argument, unlike saveOIDC and saveLDAP. A SAML signing certificate is public by construction, so there is nothing here
   * that must not be read back into the form - which is why the certificate is shown rather than redacted. */
  saveSAML: (config: SAMLConfig) =>
    request<{ saved: boolean; note: string }>('PUT', '/api/signon/saml', { config }),

  testSAML: (config: SAMLConfig) => request<SignonTestResult>('POST', '/api/signon/saml/test', { config }),
  testLDAP: (config: LDAPConfig, username: string, newPassword?: string) =>
    request<SignonTestResult>('POST', '/api/signon/ldap/test', {
      config,
      username,
      ...(newPassword === undefined ? {} : { newPassword }),
    }),

  alertRules: () => request<AlertRulesDocument>('GET', '/api/alerts/rules'),
  /** readSAMLMetadata parses a provider's published metadata so the settings do not have to be transcribed. */
  //
  // Either a URL for this server to fetch, or the document itself when the provider is somewhere this
  // server cannot reach. Nothing is saved: it reports what the document contains and applying it is a
  // separate, deliberate step.
  readSAMLMetadata: (body: { url?: string; document?: string }) =>
    request<{
      entityId: string
      ssoUrl: string
      ssoPostUrl: string
      certPem: string
      certificates: {
        subject: string
        issuer: string
        notAfter: string
        expired: boolean
        algorithm: string
        fingerprint: string
      }[]
    }>('POST', '/api/signon/saml/metadata', body),

  saveAlertRules: (rules: EditableAlertRule[]) =>
    request<{ saved: number; path: string; note: string }>('PUT', '/api/alerts/rules', { rules }),

  // configured is false when the server does not offer passkeys at all, which the panel reports as information
  // rather than as a failure. Optional, so an older server that omits it is treated as offering them.
  passkeys: () => request<{ passkeys: Passkey[]; configured?: boolean }>('GET', '/api/passkeys'),
  beginPasskeyRegistration: () =>
    request<PasskeyCreationOptions>('POST', '/api/passkeys/register/begin'),
  finishPasskeyRegistration: (label: string, challenge: string, response: unknown) =>
    request<Passkey>('POST', '/api/passkeys/register/finish', { label, challenge, response }),
  removePasskey: (id: string) =>
    request<{ status: string }>('DELETE', `/api/passkeys/${encodeURIComponent(id)}`),
  beginPasskeySignIn: () =>
    request<PasskeyRequestOptions>('POST', '/api/passkeys/signin/begin', {}),
  finishPasskeySignIn: (challenge: string, response: unknown) =>
    request<Me>('POST', '/api/passkeys/signin/finish', { challenge, response }),

  tokens: () => request<{ tokens: ApiToken[] }>('GET', '/api/tokens'),
  createToken: (label: string, role: string) =>
    request<NewApiToken>('POST', '/api/tokens', { label, role }),
  revokeToken: (label: string) =>
    request<{ status: string }>('DELETE', `/api/tokens/${encodeURIComponent(label)}`),

  settings: () => request<SettingsResponse>('GET', '/api/settings'),
  saveSettings: (kind: string, content: string) =>
    request<SettingsFile>('PUT', `/api/settings/${encodeURIComponent(kind)}`, { content }),

  logout: () => request<{ status: string }>('POST', '/api/logout'),

  me: () => request<Me>('GET', '/api/me'),

  changeOwnPassword: (current: string, next: string) =>
    request<{ status: string }>('POST', '/api/me/password', { current, new: next }),

  listChannels: () => request<ChannelListResponse>('GET', '/api/channels'),

  // The form sends a model and gets YAML back. It never assembles YAML itself: quoting a
  // path with a colon in it, or a value that looks like a number, is the kind of thing
  // that produces a file which loads as something other than what was displayed.
  buildChannel: (model: unknown) => request<BuildResult>('POST', '/api/channels/build', model),

  // Reads recorded traffic and reports the shape of the feed. Carries no message content:
  // counts, rates and shapes, plus coded values, which are not identifying.
  synthesiseChannel: (channel: string, limit: number) =>
    request<{ yaml: string; channel: string; messages: number }>(
      'POST',
      `/api/channels/${encodeURIComponent(channel)}/synthesise?limit=${limit}`,
    ),

  /** Compare this channel's output against the engine being replaced, on real pairs. */
  checkParity: (channel: string, pairs: { input: string; expected: string; reference: string }[]) =>
    request<ParityReport>('POST', `/api/channels/${encodeURIComponent(channel)}/parity`, { pairs }),

  /** Propose a channel from pasted sample messages, with nothing running yet. */
  readSample: (sample: string, name: string) =>
    request<SampleReading>('POST', '/api/channels/from-sample', { sample, name }),

  profileChannel: (channel: string, limit: number) =>
    request<FeedProfile>(
      'GET',
      `/api/channels/${encodeURIComponent(channel)}/profile?limit=${limit}`,
    ),

  /** Test messages shaped like this channel's real traffic, containing none of it.
   *
   * The corpus comes back as one string with segment terminators intact, so it can be saved and
   * replayed as-is. Returning an array of messages would leave the caller to rejoin them, and the
   * common wrong guess is a newline - which produces a corpus nothing can parse. */
  generateTestMessages: (channel: string, count: number, seed: number) =>
    request<GeneratedCorpus>(
      'GET',
      `/api/channels/${encodeURIComponent(channel)}/testmessages?count=${count}&seed=${seed}`,
    ),

  // Follows one message through a channel, showing every stage. Delivers nothing.
  traceMessage: (channel: string, subject: { message?: string; id?: number }) =>
    request<MessageTrace>(
      'POST',
      `/api/channels/${encodeURIComponent(channel)}/trace`,
      subject,
    ),

  // Searches by what is inside the messages, using the same expression language channel filters
  // use. A scan rather than an index, which is why the result says how much it looked at.
  searchMessages: (body: {
    where: string
    channel?: string
    messageType?: string
    outcome?: string
    examine?: number
    limit?: number
  }) => request<ContentSearchResult>('POST', '/api/messages/search', body),

  // Finds a message by an identifier - an MRN, a name, a date of birth, an accession, a claim
  // number - whatever format the message arrived in. An index lookup rather than a scan, because
  // the identifiers were extracted when each message was recorded.
  //
  // A POST, so the term is not written into every proxy log and browser history between here and
  // the server. Nobody has decided those may hold patient identifiers.
  findMessages: (body: {
    term: string
    kind?: string
    channel?: string
    since?: string
    until?: string
    limit?: number
    offset?: number
  }) => request<IdentitySearchResult>('POST', '/api/messages/find', body),

  // Asks whether the form can represent an existing channel file. A refusal is a 200 carrying the
  // reason, because "no, because of this" is a successful answer to that question.
  parseChannel: (yaml: string) =>
    request<{
      editable: boolean
      model?: WireModel
      why?: string
      warnings?: string[]
    }>('POST', '/api/channels/parse', { yaml }),

  validateChannel: (yaml: string) =>
    request<ValidateResult>('POST', '/api/channels/validate', { yaml }),

  // Runs a candidate against traffic already recorded for the channel. The interesting
  // number is how many of those messages it changes.
  replayChannel: (
    channel: string,
    yaml: string,
    limit: number,
    ignore: string[],
  ) =>
    request<ReplayReport>('POST', `/api/channels/${encodeURIComponent(channel)}/replay`, {
      yaml,
      limit,
      ignore,
    }),

  // language is optional and defaults to javascript on the server, which is what an unmarked script is everywhere
  // else. WebAssembly is not accepted: a module is compiled output, so there is nothing to paste into an editor.
  checkScript: (
    source: string,
    // The narrow version of this was the third place the two-kind assumption was written down, after the component's type and
    // the server's parser. Spelt out rather than imported to keep api.ts free of component imports, which is the convention here.
    kind:
      | 'preprocessor'
      | 'filter'
      | 'transformer'
      | 'writer'
      | 'postprocessor'
      | 'lifecycle'
      | 'reader',
    language: 'javascript' | 'lua' = 'javascript',
  ) => request<ScriptCheck>('POST', '/api/scripts/check', { source, kind, language }),

  // The whole dictionary in one request. A completion menu cannot afford a round trip
  // per keystroke, so the editor loads this once and completes from memory.
  dictionary: () => request<{ all: DictSegment[] }>('GET', '/api/dictionary?all=1'),

  getChannel: (name: string) =>
    request<ChannelResponse>('GET', `/api/channels/${encodeURIComponent(name)}`),

  createChannel: (yaml: string) => request<ChannelResponse>('POST', '/api/channels', { yaml }),

  /** Translate a Mirth or OIE channel export and report what came across. Writes nothing. */
  importMirth: (xml: string) => request<unknown>('POST', '/api/mirth/import', { xml }),

  /** Every channel's feed contract and whether it currently holds. */
  contracts: () => request<unknown>('GET', '/api/contracts'),

  /**
   * repairDocument rebuilds narrative that would have displayed blank.
   *
   * Nothing is stored and no channel changes; the document goes up in the request and comes back repaired.
   */
  repairDocument: (body: { document: string; custodianName?: string }) =>
    request<RepairReport>('POST', '/api/document/repair', body),

  /**
   * reconcileDocuments compares two medication lists across a transition of care.
   *
   * before and after are named rather than positional because reversing them inverts every finding: a drug that
   * was started reads as one that was stopped, and a confidently backwards report is worse than none.
   */
  reconcileDocuments: (body: { before: string; after: string }) =>
    request<ReconcileResult>('POST', '/api/document/reconcile', body),

  /**
   * documentPDF renders a document for a person to read, print, fax or file.
   *
   * A POST rather than a GET because the document travels in the body. Patient data in a URL lands in every access
   * log and proxy cache between here and the server.
   */
  documentPDF: (body: { document: string; includeCodes?: boolean; footer?: string }) =>
    requestBlob('POST', '/api/document/pdf', body),

  /**
   * mergeDocuments shows what several documents say together.
   *
   * A reading aid, not a record. Disagreements are surfaced rather than resolved, and the result is deliberately not
   * offered as a document to save or send.
   */
  mergeDocuments: (body: { documents: string[]; sections?: string[] }) =>
    request<MergeResult>('POST', '/api/document/merge', body),

  /** tefcaStatus reports whether this instance participates in national exchange, and how. */
  tefcaStatus: () => request<TEFCAStatus>('GET', '/api/tefca/status'),

  /**
   * tefcaAudit reads the record of what was exchanged.
   *
   * Read from the file rather than memory, because the questions this answers are about last month.
   */
  tefcaAudit: (from: string, to: string) =>
    request<TEFCAAuditPage>(
      'GET',
      `/api/tefca/audit?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`,
    ),

  /**
   * tefcaPurposeCheck says whether a purpose of use would be accepted, without exchanging anything.
   *
   * The alternative way to find out is to attempt a real exchange and read the refusal, which on a national network
   * means a request that left the building and was logged at the far end.
   */
  tefcaPurposeCheck: (purpose: string) =>
    request<{ purpose: string; recognised: boolean; declared: boolean; acceptable: boolean; explanation: string }>(
      'POST',
      '/api/tefca/purpose-check',
      { purpose },
    ),

  /** verifyDocument checks a signature. Available to viewers: withholding it would make the signature pointless. */
  verifyDocument: (body: { document: string; trustPem?: string }) =>
    request<VerifyResult>('POST', '/api/document/verify', body),

  /** signingIdentity reports who a signature would name, without signing anything. */
  signingIdentity: () => request<SigningIdentity>('GET', '/api/document/signing-identity'),

  /** signDocument signs with the server's own key. Administrator-only: it asserts organisational responsibility. */
  signDocument: (body: { document: string; referenceId?: string; role?: string }) =>
    request<{ document: string; signer: string; expires: string; caveat: string }>(
      'POST',
      '/api/document/sign',
      body,
    ),

  /**
   * inspectCertificate reads a certificate without installing it.
   *
   * The server has been able to do this since the endpoint was written and nothing called it, which is the same
   * shape of defect as a mapper that rendered and had never worked.
   */
  inspectCertificate: (what: { pem?: string; path?: string }) =>
    request<{ certificates: InspectedCertificate[] }>('POST', '/api/certificates/inspect', what),

  /** fleetPeers lists the instances being watched. Tokens are never returned. */
  fleetPeers: () => request<FleetPeers>('GET', '/api/fleet/peers'),

  /** addFleetPeer starts watching another instance, or replaces one of the same name. */
  addFleetPeer: (peer: { name: string; url: string; token: string; allowControl: boolean }) =>
    request<{ name: string; replaced: boolean }>('PUT', '/api/fleet/peers', peer),

  /** removeFleetPeer stops watching one instance. */
  removeFleetPeer: (name: string) =>
    request<{ removed: string }>('DELETE', `/api/fleet/peers/${encodeURIComponent(name)}`),

  /** writeTable creates or replaces one shared mapping table. */
  writeTable: (table: {
    name: string
    describes: string
    entries: { from: string; to: string; why?: string }[]
    default?: string
    strict?: boolean
    source?: string
    file?: string
  }) => request<{ name: string; file: string; entries: number; notes: string[] }>(
    'PUT',
    '/api/codesets/table',
    table,
  ),

  /** proposeContract builds a contract from traffic already recorded, for review before saving. */
  proposeContract: (channel: string) =>
    request<ProposedContract>(
      'POST',
      `/api/channels/${encodeURIComponent(channel)}/contract/propose`,
    ),

  /**
   * saveContract writes a reviewed contract and attaches it to the channel.
   *
   * The YAML goes back exactly as it was shown. Rebuilding it server-side could profile different traffic and
   * write something the person never read, which defeats the point of reviewing it.
   */
  saveContract: (channel: string, yaml: string, checkEvery?: string) =>
    request<{ channel: string; file: string }>(
      'PUT',
      `/api/channels/${encodeURIComponent(channel)}/contract`,
      // checkEvery is omitted when empty rather than sent as an empty string, because the server treats absent as "leave it alone"
      // and that distinction matters for a contract that already had an interval somebody chose.
      checkEvery !== undefined && checkEvery !== '' ? { yaml, checkEvery } : { yaml },
    ),

  updateChannel: (name: string, yaml: string) =>
    request<ChannelResponse>('PUT', `/api/channels/${encodeURIComponent(name)}`, { yaml }),

  deleteChannel: (name: string) =>
    request<{ status: string }>('DELETE', `/api/channels/${encodeURIComponent(name)}`),

  validate: (yaml: string) => request<ChannelResponse>('POST', '/api/validate', { yaml }),

  /** exportUrl is a plain link, so the browser's own download handling applies. */
  exportUrl: (name: string) => `/api/channels/${encodeURIComponent(name)}/yaml`,

  /** mirthExportUrl downloads the channel as Mirth XML, for going back the way you came. */
  //
  // A plain link like the others, but never offered without showing mirthExportPreview first. Mirth's
  // format cannot carry Perfuse's filters, transformations or contracts, and a migration tool that
  // silently produces a channel doing less than the original is the failure this exists to avoid.
  mirthExportUrl: (name: string) => `/api/channels/${encodeURIComponent(name)}/mirth`,

  /** mirthExportPreview reports what an export would lose, or why it cannot be made at all. */
  mirthExportPreview: (name: string) =>
    request<{ name: string; convertible: boolean; refusal: string; notes: string[] }>(
      'GET',
      `/api/channels/${encodeURIComponent(name)}/mirth/preview`,
    ),

  /** specUrl downloads the interface specification, generated from the channel. */
  //
  // A plain link for the same reason as exportUrl: the browser handles the download, and the
  // document arrives as a file somebody can send rather than text in a pane they would retype.
  specUrl: (name: string) =>
    `/api/channels/${encodeURIComponent(name)}/spec?format=markdown`,

  listUsers: () => request<{ users: User[] }>('GET', '/api/users'),

  createUser: (username: string, password: string, role: Role) =>
    request<User>('POST', '/api/users', { username, password, role }),

  updateUser: (id: number, patch: { role?: Role; password?: string; disabled?: boolean }) =>
    request<User>('PUT', `/api/users/${id}`, patch),

  deleteUser: (id: number) => request<{ status: string }>('DELETE', `/api/users/${id}`),

  listAudit: (limit = 200) =>
    request<{ entries: AuditEntry[] }>('GET', `/api/audit?limit=${limit}`),

  /** What this installation has refused to do, and how far it has got.
   *
   *  Admin only, because it is operational self-criticism: it says where somebody got stuck and which
   *  message stopped them most often. */
  friction: () => request<FrictionReport>('GET', '/api/friction'),

  status: () => request<StatusResponse>('GET', '/api/status'),

  startChannel: (name: string) =>
    request<{ status: string }>('POST', `/api/channels/${encodeURIComponent(name)}/start`),

  stopChannel: (name: string) =>
    request<{ status: string }>('POST', `/api/channels/${encodeURIComponent(name)}/stop`),

  listMessages: (params: Record<string, string | number | undefined>) => {
    const query = new URLSearchParams()
    Object.entries(params).forEach(([k, v]) => {
      if (v !== undefined && v !== '') query.set(k, String(v))
    })
    return request<{
      messages: StoredMessage[]
      total: number
      offset: number
      channels: string[] | null
    }>('GET', `/api/messages?${query.toString()}`)
  },

  getMessage: (id: number) =>
    request<{ message: StoredMessage; parsed?: MessageView; parseError?: string }>(
      'GET',
      `/api/messages/${id}`,
    ),

  reprocessMessage: (id: number) =>
    request<{ status: string; ackCode?: string; ackText?: string }>(
      'POST',
      `/api/messages/${id}/reprocess`,
    ),

  stats: () =>
    request<{ stats: MessageStats; destinations: DestinationStat[]; since: string }>(
      'GET',
      '/api/stats',
    ),

  throughput: (window: string, bucket: string, channel?: string) => {
    const query = new URLSearchParams({ window, bucket })
    if (channel) query.set('channel', channel)
    return request<{ buckets: Bucket[]; bucket: string; window: string }>(
      'GET',
      `/api/throughput?${query.toString()}`,
    )
  },

  /** flow returns the site map with a series per channel-to-destination strand. */
  flow: (window: string, bucket: string) =>
    request<FlowResponse>(
      'GET',
      `/api/flow?${new URLSearchParams({ window, bucket }).toString()}`,
    ),

  inspectHL7: (message: string) =>
    request<{ parsed: MessageView }>('POST', '/api/inspect/hl7', { message }),

  convertToFHIR: (input: {
    message: string
    version?: string
    usCore?: boolean
    system?: string
    timezone?: string
  }) => request<ConversionResult>('POST', '/api/inspect/fhir', input),

  fhirVersions: () =>
    request<{
      versions: { name: string; number: string; note: string }[]
      default: string
      note: string
    }>('GET', '/api/fhir/versions'),

  // -- Metrics --------------------------------------------------------------

  /** metrics reads the series the dashboard draws. */
  async metrics(window = '1h'): Promise<MetricsSnapshot> {
    return request<MetricsSnapshot>('GET', `/api/metrics?window=${encodeURIComponent(window)}`)
  },

  /** metricNames lists what is being collected, so the interface does not
   *  hard-code names and silently show nothing when one changes. */
  async metricNames(): Promise<{
    metrics: { Name: string; Kind: string; Unit: string; Help: string; Labels: string[] }[]
    resolutionSeconds: number
    windowSeconds: number
    uptimeSeconds: number
  }> {
    return request('GET', '/api/metrics/names')
  },

  // -- Clinical documents ---------------------------------------------------

  /** inspectDocument reads a clinical document, or one carried inside an HL7
   *  message, and reports what it contains. Nothing is stored. */
  async inspectDocument(req: {
    document?: string
    message?: string
    version?: string
    identifierSystems?: Record<string, string>
    convert?: boolean
  }): Promise<DocumentInspection> {
    return request<DocumentInspection>('POST', '/api/inspect/document', req)
  },

  /** documentTypes lists what the reader recognises. */
  async documentTypes(): Promise<{ documents: string[]; sections: string[]; note: string }> {
    return request('GET', '/api/document/types')
  },

  // -- Queue ----------------------------------------------------------------

  /** queue reads what is waiting, with the per-destination summary alongside. */
  async queue(opts: {
    channel?: string
    destination?: string
    state?: string
    limit?: number
    offset?: number
  } = {}): Promise<QueueSnapshot> {
    const params = new URLSearchParams()
    if (opts.channel) params.set('channel', opts.channel)
    if (opts.destination) params.set('destination', opts.destination)
    if (opts.state) params.set('state', opts.state)
    if (opts.limit) params.set('limit', String(opts.limit))
    if (opts.offset) params.set('offset', String(opts.offset))
    const query = params.toString()
    return request<QueueSnapshot>('GET', `/api/queue${query ? `?${query}` : ''}`)
  },

  /** queueItem fetches the stored bytes, so somebody can see what is stuck
   *  rather than guessing from a control id. */
  async queueItem(
    id: number,
  ): Promise<{ id: number; raw: string; size: number; message: MessageView }> {
    return request('GET', `/api/queue/${id}`)
  },

  /** queueRetry brings forward the next attempt. This is what replaces
   *  restarting a channel to shift a stuck message. */
  async queueRetry(target: { id?: number; channel?: string; destination?: string }) {
    return request<{ retried: number }>('POST', '/api/queue/retry', target)
  },

  /** queueSkip abandons one message. Admin only, and audited by name. */
  async queueSkip(target: { id: number }) {
    return request<{ skipped: number }>('POST', '/api/queue/skip', target)
  },

  /** queueDrain abandons everything waiting for a destination. */
  async queueDrain(target: { channel: string; destination: string }) {
    return request<{ drained: number }>('POST', '/api/queue/drain', target)
  },

  /** queueRemove deletes a finished item. A pending one has to be skipped
   *  first, so there is a record it was abandoned deliberately. */
  async queueRemove(target: { id: number }) {
    return request<{ removed: number }>('POST', '/api/queue/remove', target)
  },

  // -- Alerts ---------------------------------------------------------------

  /** alerts reads what is wrong now and what recently recovered. */
  async alerts(): Promise<AlertSnapshot> {
    return request<AlertSnapshot>('GET', '/api/alerts')
  },

  /** acknowledgeAlert stops notification without hiding the alert. */
  async acknowledgeAlert(key: string) {
    return request<{ status: string }>('POST', '/api/alerts/acknowledge', { key })
  },

  // -- Channel history ------------------------------------------------------

  /** channelHistory lists the commits that touched a channel. */
  async channelHistory(name: string): Promise<ChannelHistory> {
    return request<ChannelHistory>('GET', `/api/channels/${encodeURIComponent(name)}/history`)
  },

  /** channelVersion returns a channel as it was, with a diff against now. */
  async channelVersion(name: string, hash: string): Promise<ChannelVersion> {
    return request<ChannelVersion>(
      'GET',
      `/api/channels/${encodeURIComponent(name)}/history/${encodeURIComponent(hash)}`,
    )
  },

  /** restoreChannelVersion writes an old version back, as a new change. */
  async restoreChannelVersion(name: string, hash: string) {
    return request<{ channel: string; restored: string; note: string }>(
      'POST',
      `/api/channels/${encodeURIComponent(name)}/history/${encodeURIComponent(hash)}/restore`,
    )
  },

  // -- Certificates ---------------------------------------------------------

  /** certificates describes every encrypted endpoint and how long it has left. */
  async certificates(): Promise<CertificateSnapshot> {
    return request<CertificateSnapshot>('GET', '/api/certificates')
  },

  // -- Shadow ---------------------------------------------------------------

  /** shadows lists every channel currently comparing against a candidate. */
  async shadows(): Promise<ShadowSummary[]> {
    const res = await request<{ shadows: ShadowSummary[] }>('GET', '/api/shadows')
    return res.shadows
  },

  /** approveMappings adds a step per approved mapping to a channel.
   *
   * Each mapping is named individually and there is no threshold parameter, deliberately: the engine has already used the confidence
   * to decide whether to offer a mapping, and what remains is a judgement about the field. An abstention is refused by the server. */
  approveMappings: (body: {
    channel: string
    mappings: { source: string; target: string; confidence: number; reasoning: string; abstained: boolean }[]
  }) =>
    request<{ channel: string; added: string[]; note: string }>('POST', '/api/mappings/approve', body),

  /** recipeFromMappings turns approved mappings into a shareable recipe file. */
  recipeFromMappings: (body: {
    name: string
    sourceSystem: string
    targetSystem: string
    mappings: { source: string; target: string; confidence: number; reasoning: string; abstained: boolean }[]
  }) => requestBlob('POST', '/api/mappings/recipe', body),

  /** exportProfile downloads a dialect profile as a shareable file.
   *
   * A blob rather than JSON because the response is a file with a filename attached, and the point of a shared profile is that it
   * travels - into a ticket, a repository, or an email to somebody with the same feed. */
  exportProfile: (body: { channel: string; limit: number; name: string; description: string; source: string; tags: string[] }) =>
    requestBlob('POST', '/api/profiles/export', body),

  /** importProfile reads a shared profile. Nothing on the server changes; a profile describes a feed. */
  importProfile: (body: unknown) =>
    request<{ profile: unknown; note: string }>('POST', '/api/profiles/import', body),

  /** startShadow begins or changes a channel's comparison against a candidate.
   *
   * The share is a percentage, not a fraction. The file format stores a fraction, and the server converts - somebody meaning half
   * who types 0.5 into a percentage field gets a comparison that observes almost nothing, and nothing complains. */
  startShadow: (
    channel: string,
    body: { candidate: string; sample: number; ignore: string[]; compare: string[]; maxDifferences: number },
  ) => request<{ channel: string; candidate: string }>('PUT', `/api/channels/${encodeURIComponent(channel)}/shadow`, body),

  /** stopShadow ends a comparison and leaves the candidate channel in place. */
  stopShadow: (channel: string) =>
    request<{ channel: string; wasComparing: boolean }>('DELETE', `/api/channels/${encodeURIComponent(channel)}/shadow`),

  /** shadow returns one channel's comparison, differences included. */
  async shadow(channel: string): Promise<ShadowReport | null> {
    const res = await request<ShadowReport | { shadow: null }>(
      'GET',
      `/api/channels/${encodeURIComponent(channel)}/shadow`,
    )
    if ('shadow' in res && res.shadow === null) {
      return null
    }
    return res as ShadowReport
  },
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

export interface MetricPoint {
  at: string
  value: number
  rate?: number
  p50?: number
  p95?: number
  p99?: number
}

export interface MetricSeries {
  name: string
  kind: 'counter' | 'gauge' | 'histogram'
  unit: 'count' | 'bytes' | 'seconds' | 'ratio'
  help?: string
  labels?: Record<string, string>
  points: MetricPoint[]
  latest: number
  ratePerSecond: number
}

export interface MetricsSnapshot {
  at: string
  uptimeSeconds: number
  metrics: MetricSeries[]
  dropped?: Record<string, number>
  resolutionSeconds: number
  windowSeconds: number
}

// ---------------------------------------------------------------------------
// Clinical documents
// ---------------------------------------------------------------------------

export interface CdaIdentifier {
  root?: string
  extension?: string
  assigner?: string
}

export interface CdaPatient {
  identifiers?: CdaIdentifier[]
  family?: string
  given?: string[]
  prefix?: string
  suffix?: string
  gender?: string
  genderName?: string
  birthTime?: string
  address?: {
    use?: string
    lines?: string[]
    city?: string
    state?: string
    postal?: string
    country?: string
  }
  phone?: string
}

export interface CdaEntry {
  kind?: string
  code?: string
  codeSystem?: string
  codeName?: string
  value?: string
  valueCode?: string
  valueName?: string
  unit?: string
  statusCode?: string
  effectiveTime?: string
  low?: string
  high?: string
  negationInd?: boolean
  nilFlavor?: string
  text?: string
  children?: CdaEntry[]
}

export interface CdaSection {
  title?: string
  code?: string
  codeSystem?: string
  codeName?: string
  templateIds?: string[]
  kind?: string
  narrativeText?: string
  narrativeHtml?: string
  entries?: CdaEntry[]
  empty?: boolean
  nilFlavor?: string
}

export interface CdaNote {
  severity: string
  path?: string
  message: string
  rule?: string
}

export interface CdaDocument {
  title?: string
  typeCode?: string
  typeSystem?: string
  typeName?: string
  templateIds?: string[]
  documentType?: string
  id?: string
  setId?: string
  version?: string
  effectiveTime?: string
  confidentiality?: string
  languageCode?: string
  patient: CdaPatient
  authors?: { time?: string; family?: string; given?: string; id?: string; organisation?: string }[]
  custodian?: string
  encounter?: { id?: string; code?: string; start?: string; end?: string; location?: string }
  sections?: CdaSection[]
  notes?: CdaNote[]
}

export interface CdaFinding {
  severity: string
  section: string
  kind: string
  message: string
  narrative?: string
  coded?: string
}

export interface AgreementReport {
  findings: CdaFinding[]
  checked: number
  skipped: number
  reasons?: string[]
  errors: number
  warnings: number
}

/**
 * ValidationReport is conformance against the implementation guide, which is a different question from
 * whether the narrative agrees with the entries.
 *
 * Agreement asks whether the prose matches the data. This asks whether a receiver would reject the document.
 * A document can pass either and fail the other, and the failure people actually meet is this one, because the
 * exchange partner refuses it and says only that it was non-conformant.
 */
export interface ValidationReport {
  /** Which guide was checked, e.g. "C-CDA R2.1 Continuity of Care Document". */
  profile: string
  findings: CdaFinding[]
  errors: number
  warnings: number
  infos: number
}

/** RepairAction is one thing that was changed in a document, or one thing that deliberately was not. */
export interface RepairAction {
  kind: string
  section?: string
  /** What is wrong, in technical terms. */
  problem: string
  /** What would have happened. Separate from the problem because only this makes anybody act. */
  consequence: string
  /** What was done about it, or why nothing was. */
  action: string
  fixed: boolean
  /** Text that was reconstructed, so it can be checked before it is trusted. */
  recovered?: string
}

/**
 * RepairReport is what was found in a document and what was done about it.
 *
 * The failure this addresses: a section with coded entries and no narrative passes the schema, passes a
 * receiver's import, is counted as a successful exchange by both ends, and displays as a blank page. Most
 * viewers render narrative and ignore entries entirely, so the content is in the file and invisible.
 */
export interface RepairReport {
  repairs: RepairAction[]
  /** Counted separately because they lead to different actions: usable now, versus go back to the sender. */
  fixed: number
  reported: number
  /** Coded facts no reader would have seen. The number that matters. */
  invisibleEntries: number
  /** The repaired XML, absent when nothing could be repaired. */
  document?: string
}

/** MedChange is one difference between two medication lists. */
export interface MedChange {
  /** One of added, removed, unchanged or changed. */
  kind: string
  medication: string
  code?: string
  codeSystem?: string
  /** The entries the judgement was made from, so a reader need not take the summary on trust. */
  before?: CdaEntry
  after?: CdaEntry
  detail?: string
}

/** ReconcileReport is how two medication lists differ. */
export interface ReconcileReport {
  added: MedChange[]
  removed: MedChange[]
  changed: MedChange[]
  unchanged: MedChange[]
  /**
   * Drugs active in one document and stopped in the other. The clinically dangerous case: both documents are
   * conformant and internally consistent and they contradict each other, and whichever the receiving clinician
   * happens to read decides what they believe.
   */
  conflicts: MedChange[]
}

/** ReconcileResult is the comparison plus whether the two documents are even about the same person. */
export interface ReconcileResult {
  report: ReconcileReport
  patient: {
    samePatient: boolean
    /** Why, in terms a reader can weigh. An identifier match is strong evidence; a name match is not. */
    explanation: string
    before: string
    after: string
  }
}

/**
 * VerifyReport is what a signature check found.
 *
 * Five separate answers rather than one verdict. Reducing them to a single badge is how a verifier misleads people: a
 * document whose certificate has since expired is not a forgery, and reporting it identically to an altered document
 * sends somebody looking in entirely the wrong place.
 */
export interface VerifyReport {
  /** False is not a failure. Most documents are unsigned. */
  signed: boolean
  digestValid: boolean
  signatureValid: boolean
  /** Whether the signing time and claimed capacity are covered by the signature rather than editable by anybody. */
  propertiesValid: boolean
  trusted: boolean
  /** False trusted with false trustChecked means not checked, which is different from checked and failed. */
  trustChecked: boolean
  validAtSigningTime: boolean
  signer?: string
  issuer?: string
  signingTime?: string
  claimedRole?: string
  level?: string
  coveredIds: string[]
  problems: string[]
  notes: string[]
}

/** VerifyResult carries the report and the single verdict, computed on the server so every consumer agrees. */
export interface VerifyResult {
  report: VerifyReport
  sound: boolean
}

/** SigningIdentity is who a signature made here would name. */
export interface SigningIdentity {
  available: boolean
  reason?: string
  subject?: string
  commonName?: string
  issuer?: string
  notAfter?: string
  selfSigned?: boolean
  namesAHost?: boolean
  /** What a signature made with this certificate does and does not establish. */
  explanation?: string
}

/** MergeSource names one document a merged view drew from, so anything surprising can be traced back. */
export interface MergeSource {
  position: number
  title: string
  custodian?: string
  effective?: string
  patient?: string
  sections?: number
}

/** MergeResult is what several documents say together. */
export interface MergeResult {
  sources: MergeSource[]
  patient: {
    samePatient: boolean
    explanation: string
    /** Which source disagrees and why. Named by position, because with five sources "one of these" is useless. */
    disagreements: string[]
  }
  sections: Array<{
    kind: string
    section: CdaSection
    /** Anything that conflicted or was dropped while combining. */
    notes: CdaNote[]
  }>
  /** What this is not. Carried in the response so an API consumer meets it too. */
  caveat: string
}

/** TEFCAStatus is whether this instance takes part in national exchange, and how. */
export interface TEFCAStatus {
  /** False is normal. Most instances do not participate, and that is not a fault. */
  configured: boolean
  explanation?: string
  organisation?: string
  oid?: string
  qhinEndpoint?: string
  participantType?: string
  /** The purposes this participant declared. */
  purposes: string[]
  /** Every purpose that exists, so the interface need not hard-code a list that would drift from the server. */
  allPurposes: string[]
  trail?: {
    path: string
    /** False means this participant is exchanging without keeping the record it is required to keep. */
    writable: boolean
    problem?: string
    unreadableLines?: number
    unreadableNote?: string
  }

  /**
   * Whether this build can carry an exchange at all.
   *
   * Configured and unable to exchange look identical on screen without this, and the difference is the whole thing: an operator who
   * believes exchange is running will not go looking for why no records ever arrive.
   */
  /** Whether the older QHIN-to-QHIN transport built on the IHE profiles exists. It does not. */
  exchangeImplemented?: boolean

  /** Whether Facilitated FHIR exists. Reported separately because the two transports are at different stages,
   *  and one flag covering both would be wrong in whichever direction it was rounded. */
  facilitatedFHIRImplemented?: boolean
  facilitatedFHIRExplanation?: string
  exchangeExplanation?: string
}

/** TEFCAAuditEntry is one recorded exchange. */
export interface TEFCAAuditEntry {
  timestamp: string
  direction: string
  purpose: string
  patientId?: string
  requestingOrg?: string
  respondingOrg?: string
  exchangeType: string
  success: boolean
  errorDetail?: string
}

/** TEFCAAuditPage is a window of the trail with a summary. */
export interface TEFCAAuditPage {
  configured: boolean
  entries: TEFCAAuditEntry[]
  total: number
  /** True when more exist than were returned, so a partial view is visibly partial. */
  truncated: boolean
  from: string
  to: string
  summary: {
    total: number
    failed: number
    byType: Record<string, Record<string, number>>
    byPurpose: Record<string, number>
    /** The partner with the most failures. A run against one organisation is invisible in a total. */
    worstOrg: string
    worstCount: number
    /** Exchanges recorded with no purpose of use, which is a compliance gap rather than a display gap. */
    withoutPurpose: number
  }
}

export interface CdaAttachment {
  segment: number
  encoding?: string
  mimeType?: string
  bytes: number
  notes?: string[]
  isXML?: boolean
  preview?: string
  previewIsBase64?: boolean
}

export interface DocumentInspection {
  found: boolean
  message?: string
  isDocumentMessage?: boolean
  document?: CdaDocument
  summary?: {
    title: string
    documentType: string
    patient: string
    effective: string
    sections: number
    entries: number
    warnings: number
    errors: number
  }
  notes?: CdaNote[]
  agreement?: AgreementReport
  validation?: ValidationReport
  attachments?: CdaAttachment[]
  transport?: {
    info: {
      type?: string
      uniqueID?: string
      status?: string
      availability?: string
      activityDate?: string
      parentID?: string
      author?: string
    }
    statusMeaning?: string
    replaces?: boolean
  }
  fhir?: {
    bundle: unknown
    counts: Record<string, number>
    notes: CdaNote[]
  }
  conversionError?: string
}

// ---------------------------------------------------------------------------
// Queue
// ---------------------------------------------------------------------------

export interface QueueItem {
  id: number
  channel: string
  destination: string
  controlId: string
  messageType: string
  size: number
  state: 'pending' | 'delivered' | 'failed' | 'skipped'
  attempts: number
  enqueuedAt: string
  nextAttempt: string
  lastAttempt?: string
  lastError?: string
  reason?: string
  ageSeconds: number
  /** Negative means overdue, which is normal for something about to be picked up. */
  dueInSeconds: number
}

export interface QueueDepth {
  channel: string
  destination: string
  pending: number
  failed: number
  oldest?: string
  /** The number to alert on: depth cannot distinguish draining from stuck. */
  oldestSeconds: number
  maxAttempts: number
}

export interface QueueSnapshot {
  items: QueueItem[]
  total: number
  destinations: QueueDepth[]
  at: string
}

// ---------------------------------------------------------------------------
// Alerts
// ---------------------------------------------------------------------------

export interface Alert {
  key: string
  kind: string
  severity: 'warning' | 'critical'
  channel?: string
  destination?: string
  summary: string
  detail?: string
  value: number
  threshold: number
  /** When the condition started, which is earlier than when the alert fired. */
  firingSince: string
  firedAt: string
  resolvedAt?: string
  acknowledged: boolean
  acknowledgedBy?: string
  acknowledgedAt?: string
}

export interface AlertRule {
  kind: string
  channel?: string
  destination?: string
  threshold: number
  /** Nanoseconds, as Go encodes a duration. */
  for?: number
  severity?: 'warning' | 'critical'
  disabled?: boolean
}

/** What a threshold counts. The reason this comes from the server is that the same field is a share for two kinds and a count for the rest. */
export type AlertUnit = 'share' | 'messages' | 'seconds' | 'attempts' | 'none'

/** Which way a measurement has to move for a rule to fire. Two rules are about silence and fire below their threshold. */
export type AlertDirection = 'above' | 'below'

export interface AlertKindInfo {
  kind: string
  label: string
  summary: string
  detail: string
  unit: AlertUnit
  direction: AlertDirection
  default: number
  channel: boolean
  destination: boolean
}

/** An editable rule. Durations are strings here and in the file, so nothing has to convert nanoseconds for display. */
export interface EditableAlertRule {
  kind: string
  channel?: string
  destination?: string
  threshold: number
  for?: string
  severity?: 'warning' | 'critical'
  disabled?: boolean
}

export interface AlertRulesDocument {
  rules: EditableAlertRule[]
  kinds: AlertKindInfo[]
  channels: string[]
  severities: string[]
  path: string
  writable: boolean
  enabled: boolean
}

/** One field of a sign-on configuration, described by the server so the form cannot offer a setting the loader would refuse. */
export interface SignonField {
  key: string
  /** The property this field edits, supplied by the server rather than derived from the key - see the Go side for why. */
  field: string
  label: string
  help: string
  kind: 'text' | 'secret' | 'bool' | 'list' | 'duration' | 'roles'
  required?: boolean
  /** The usual value on Active Directory, for fields where the two kinds of directory differ. */
  activeDirectory?: string
  /** The usual value on OpenLDAP. */
  openLDAP?: string
  placeholder?: string
}

export interface LDAPPreset {
  name: string
  detail: string
  values: Record<string, string>
}

/** A secret is reported as set or not, never sent. */
export interface SignonSecretState {
  set: boolean
  from?: string
}

export interface OIDCConfig {
  issuer: string
  clientID: string
  redirectURL: string
  scopes: string[]
  label: string
  createUsers: boolean
  clientSecretFile: string
  clientSecret: SignonSecretState
  /** Group name to role, one row per group. */
  roles: Record<string, string>
  caseSensitive: boolean
}

export interface LDAPConfig {
  addr: string
  tls: boolean
  startTLS: boolean
  insecure: boolean
  bindDN: string
  bindPassword: SignonSecretState
  userBaseDN: string
  usernameAttribute: string
  uniqueIDAttribute: string
  userFilter: string
  nameAttribute: string
  emailAttribute: string
  memberOfAttribute: string
  groupBaseDN: string
  groupFilter: string
  groupNameAttribute: string
  roles: Record<string, string>
  createUsers: boolean
  buttonLabel: string
  timeout: string
}

export interface SignonFileState {
  configured: boolean
  path: string
  exists: boolean
  /** Whether the running server has this method loaded. Saved and not active is the normal state after an edit. */
  active: boolean
  /** A file that exists and does not load, reported rather than shown as empty. */
  problem?: string
}

/** The SAML configuration as the browser edits it.
 *
 * No secret. A signing certificate is public by construction - it is in every response the provider sends - so idpCertPEM is read
 * back into the form rather than redacted, and that is deliberate: not being able to see which certificate is installed would remove
 * the first thing anybody checks when signatures stop verifying.
 */
export interface SAMLConfig {
  entityID: string
  acsURL: string
  idpSSOURL: string
  idpCertPEM: string
  idpCertFile: string
  groupsAttribute: string
  label: string
  createUsers: boolean
  allowUnsolicited: boolean
  roles: Record<string, string>
  caseSensitive: boolean
}

export interface SignonDocument {
  oidc: SignonFileState & { config: OIDCConfig }
  saml: SignonFileState & { config: SAMLConfig }
  ldap: SignonFileState & { config: LDAPConfig }
  oidcFields: SignonField[]
  samlFields: SignonField[]
  ldapFields: SignonField[]
  ldapPresets: LDAPPreset[]
  roles: string[]
  ldapDefaults: Record<string, string>
  /** How many local administrators exist, so the screen can warn before a directory becomes the only way in. */
  localAdmins: number
}

/** One step of a connection test. Reported separately because knowing which step failed is most of knowing why. */
export interface SignonStage {
  name: string
  ok: boolean
  detail: string
}

export interface SignonTestResult {
  ok: boolean
  stages: SignonStage[]
  /** The role the tested person would be given, or absent if they would be refused. */
  role?: string
  groups?: string[]
}

export interface AlertSnapshot {
  firing: Alert[]
  resolved: Alert[]
  critical: number
  warning: number
  rules: AlertRule[]
  enabled: boolean
}

// ---------------------------------------------------------------------------
// Channel history
// ---------------------------------------------------------------------------

export interface Commit {
  hash: string
  shortHash: string
  author: string
  email?: string
  at: string
  subject: string
  body?: string
}

export interface ChannelHistory {
  available: boolean
  reason?: string
  branch?: string
  commits: Commit[]
  /** True when the file on disk differs from the newest commit. */
  uncommitted?: boolean
  file?: string
}

export interface ChannelVersion {
  hash: string
  yaml: string
  diff: string
  /** False when the old version no longer passes validation. */
  valid: boolean
  error?: string
}

// ---------------------------------------------------------------------------
// Certificates
// ---------------------------------------------------------------------------

export interface CertificateInfo {
  subject: string
  issuer: string
  serialNumber: string
  notBefore: string
  notAfter: string
  /** Negative once expired. This is the number somebody acts on. */
  daysRemaining: number
  status: 'ok' | 'expiring' | 'expired' | 'not-yet-valid'
  hosts?: string[]
  selfSigned: boolean
  isCa: boolean
  keyType: string
  keyBits?: number
  notes?: string[]
}

export interface TLSSummary {
  enabled: boolean
  certificates?: CertificateInfo[]
  authorities?: CertificateInfo[]
  mutualTls: boolean
  minVersion: string
  warnings?: string[]
  problems?: string[]
}

export interface CertificateEndpoint {
  channel: string
  where: 'listener' | 'destination'
  destination?: string
  address?: string
  tls: TLSSummary
}

export interface CertificateSnapshot {
  endpoints: CertificateEndpoint[]
  expiring: number
  expired: number
  plaintext?: string[]
}

// ---------------------------------------------------------------------------
// Shadow
// ---------------------------------------------------------------------------

export interface ShadowSummary {
  channel: string
  candidate: string
  compared: number
  differed: number
  verdict: string
}

export interface ShadowFieldDifference {
  path: string
  live: string
  candidate: string
}

export interface ShadowDifference {
  at: string
  controlId?: string
  messageType?: string
  /** filter, transform, candidate-error or live-error. */
  kind: string
  fields?: ShadowFieldDifference[]
  note?: string
}

export interface ShadowStats {
  compared: number
  skipped: number
  same: number
  differed: number
  /** Counted apart: one version keeps a message the other drops. */
  filterDisagreed: number
  candidateFailed: number
  liveFailed: number
  candidateExtraNanos: number
  startedAt: string
  lastCompared?: string
}

export interface ShadowReport {
  channel: string
  candidate: string
  stats: ShadowStats
  verdict: string
  differences: ShadowDifference[]
}
