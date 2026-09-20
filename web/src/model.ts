// The channel model the builder edits, and the YAML it produces.
//
// The server is the authority on whether a definition is valid — this file exists
// so the form has something to bind to, and so the YAML shown to the user is
// generated from exactly what they clicked. It deliberately emits the same
// minimal YAML a person would write by hand: no defaults spelled out, no key
// order surprises, nothing that would make a hand-edited file and a
// GUI-generated one look different in a diff.

export type AckWhen = 'on_delivery' | 'on_receipt'
export type DestinationType =
  | 'mllp'
  | 'tcp'
  | 'file'
  | 'http'
  | 'database'
  | 'sftp'
  | 'fhir'
  | 'smtp'
  | 'cda'
  | 'channel'
  | 'broker'
  | 's3'
  | 'ftp'
  | 'document'
  | 'soap'
  | 'dicom'
  | 'javascript'

export const destinationLabels: Record<DestinationType, string> = {
  mllp: 'Another HL7 system (MLLP)',
  tcp: 'A raw socket, framed how you choose',
  file: 'A directory on disk',
  http: 'An HTTP endpoint',
  database: 'A database table',
  sftp: 'A directory on an SFTP server',
  fhir: 'A FHIR server',
  smtp: 'An email',
  cda: 'Clinical documents',
  channel: 'Another channel in this server',
  s3: 'Object storage (S3 or compatible)',
  ftp: 'A directory on an FTP server',
  document: 'A printable document (PDF or text)',
  soap: 'A SOAP web service',
  dicom: 'An imaging archive (DICOM C-STORE)',
  javascript: 'A script you write',
  broker: 'A message broker (STOMP)',
}

export const destinationHints: Record<DestinationType, string> = {
  mllp: 'Forwards the message onward and treats a negative acknowledgement as a failure.',
  tcp:
    'Writes to a socket with framing you choose, which is what a device or a laboratory instrument usually wants. MLLP is one framing; this destination offers the others — a delimiter, a fixed record length, or a length prefix. Read a stream with the wrong framing and it does not fail, it produces messages split in the wrong places, so the framing has no default.',
  file: 'Appends each message to a dated file, framed so it can be replayed.',
  http: 'Posts the message to a URL. Any 2xx counts as delivered unless you say otherwise.',
  database:
    'Runs one statement per message. Values are bound as parameters, never built into the SQL.',
  sftp:
    'Uploads a file per message. Written under a temporary name and renamed, so nobody collects half a message.',
  fhir: 'Converts the message to FHIR and posts a transaction bundle.',
  smtp:
    'Sends an email. Usually for telling somebody a message arrived, rather than for moving clinical data — email is stored and forwarded by systems outside your control.',
  cda: 'Reads the clinical document carried inside the message, then converts or archives it.',
  channel:
    'Hands the message to another channel here, which then applies its own filter, changes and destinations. This is how one feed is split so each receiving system gets its own channel — smaller, easier to reason about, and able to be stopped on its own.',
  s3:
    'Writes each message as an object, partitioned by date so the bucket stays listable and can be aged out by lifecycle rule. Works with S3 itself and with MinIO, Ceph or Wasabi — it is signed HTTP, with no AWS SDK in the binary.',
  ftp:
    'Uploads a file per message over FTPS. Prefer SFTP where you have the choice — but a lot of analysers and bureau services only accept FTP, and that is not something you can talk them out of.',
  document:
    'Renders each message into a document somebody prints. Plain text laid out in a monospaced font, not HTML — if a field in the template is missing from a message, the delivery fails rather than producing a page with a blank where a name should be.',
  soap:
    'Posts a SOAP request. You write the XML that goes inside the envelope and Perfuse adds the envelope, the action header and fault handling. WS-Security and MTOM are not supported — a half-implementation of those is worse than none.',
  dicom:
    'Sends imaging objects to a PACS or workstation as a C-STORE user, the same operation a scanner uses. Verified against DCMTK, the toolkit most hospital imaging equipment is built on. Encryption is included — Mirth needs a paid extension for it, and DICOM metadata carries patient names.',
  javascript:
    'Runs your script instead of sending anywhere. This is the escape hatch: when nothing else here fits, do the work yourself with the message in hand. Returning nothing counts as success, and returning a string fails the delivery with that string as the reason.',
  broker:
    'Publishes over STOMP, which is what crosses the wire for JMS brokers. Persistent by default, so a message survives the broker restarting.',
}

/** A single filter condition, as the visual rule builder sees it. */
/** positiveOrNone reads a number from a text box, and returns undefined for anything that is not a positive one.
 *
 * Text rather than a number in the draft because an empty box is not zero, and these fields treat zero as unbounded - so sending 0
 * would write a limit of nothing where the caller meant no limit. */
function positiveOrNone(text: string): number | undefined {
  const n = Number(text)

  return Number.isFinite(n) && n > 0 ? n : undefined
}

/** parsePairs reads "name: value" lines into an object, and returns undefined for nothing.
 *
 * One per line rather than a row editor, following the precedent already in this form for the DICOM return list. A row editor is a
 * better interface and a much larger change; lines are editable, pasteable and diffable, which is what somebody copying a header set
 * out of a partner's email actually needs.
 *
 * The first colon separates, so a value may contain colons - a URL usually does. A line with no colon is skipped rather than stored
 * with an empty value, because a half-typed line should not become a header with no content. */
function parsePairs(text: string): Record<string, string> | undefined {
  const out: Record<string, string> = {}

  for (const line of text.split('\n')) {
    const at = line.indexOf(':')
    if (at <= 0) continue

    const name = line.slice(0, at).trim()
    const value = line.slice(at + 1).trim()

    if (name !== '') out[name] = value
  }

  return Object.keys(out).length > 0 ? out : undefined
}

/** formatPairs is the inverse, for reading a channel back into the form. */
export function formatPairs(pairs: Record<string, string> | undefined): string {
  if (pairs === undefined) return ''

  // Sorted, because an object's key order is not guaranteed to survive a round trip and an unstable order would make the form look
  // as though it had changed when nothing was touched.
  return Object.keys(pairs)
    .sort()
    .map((k) => `${k}: ${pairs[k]}`)
    .join('\n')
}

/** splitList turns "a, b ,c" into ["a", "b", "c"], and an empty box into undefined rather than an empty list.
 *
 * The distinction matters: an empty list of column names would mean a file with no columns, which is not what an empty box means. */
function splitList(text: string): string[] | undefined {
  const parts = text
    .split(',')
    .map((p) => p.trim())
    .filter((p) => p !== '')

  return parts.length > 0 ? parts : undefined
}

/** parseStatusList turns "200, 201,202" into [200, 201, 202], dropping anything that is not a number.
 *
 * Silent about rubbish on purpose. The alternative is sending a partly-numeric list to the server, which refuses the whole build
 * with a message about the request body rather than about the field somebody mistyped - and the field is the thing they can fix.
 * An empty result becomes undefined so the key is omitted, because an empty list would mean "no status counts as success", which
 * is not what an empty box means. */
function parseStatusList(text: string): number[] | undefined {
  const codes = text
    .split(',')
    .map((part) => Number(part.trim()))
    .filter((n) => Number.isInteger(n) && n >= 100 && n <= 599)

  return codes.length > 0 ? codes : undefined
}

export interface Rule {
  id: string
  /** join is ignored on the first rule. */
  join: 'and' | 'or'
  path: string
  operator: RuleOperator
  value: string
  /** values is used by the "is one of" operator. */
  values: string[]
}

export type RuleOperator =
  | '=='
  | '!='
  | 'in'
  | 'matches'
  | 'exists'
  | 'empty'
  | '>'
  | '<'
  | '>='
  | '<='

export const operatorLabels: Record<RuleOperator, string> = {
  '==': 'is',
  '!=': 'is not',
  in: 'is one of',
  matches: 'matches pattern',
  exists: 'is present',
  empty: 'is empty',
  '>': 'is greater than',
  '<': 'is less than',
  '>=': 'is at least',
  '<=': 'is at most',
}

/** Operators that take no value at all. */
export function operatorTakesNoValue(op: RuleOperator): boolean {
  return op === 'exists' || op === 'empty'
}

/** Operators that take a list rather than one value. */
export function operatorTakesList(op: RuleOperator): boolean {
  return op === 'in'
}

/** Operators that compare numerically, so the input should be a number. */
export function operatorIsNumeric(op: RuleOperator): boolean {
  return op === '>' || op === '<' || op === '>=' || op === '<='
}

export interface Destination {
  id: string
  name: string
  type: DestinationType
  enabled: boolean
  address: string
  dir: string
  /** url is the FHIR server for a fhir destination, or the target for a cda one. */
  url: string
  /** fhirVersion is the release to produce. */
  fhirVersion: string
  /**
   * fhirIdentifierSystem names the authority an MRN belongs to.
   *
   * Required by the loader, and it had no field here at all until now - which meant a FHIR
   * destination built from this form could never validate. The reason it is required is
   * worth carrying: an MRN on its own is ambiguous between facilities, so a bundle without
   * a system URI asserts an identity it cannot support, and two patients from different
   * hospitals can be merged by a receiver that trusts it.
   */
  fhirIdentifierSystem: string
  /** cdaWrite selects what a cda destination emits. */
  cdaWrite: 'fhir' | 'document' | 'both'
  /** cdaOnNoDocument decides what happens to a message carrying no document. */
  cdaOnNoDocument: 'skip' | 'fail'
  /** cdaRequireAgreement stops a document whose two halves contradict each other. */
  cdaRequireAgreement: boolean
  /** httpMethod is POST unless an endpoint wants otherwise. */
  httpMethod: string
  /** httpContentType overrides the default HL7 media type. */
  httpContentType: string
  /** httpFailOnBody treats this string in a 2xx response as a failure. */
  httpFailOnBody: string

  /** httpBearerToken authenticates to the endpoint. Held here, never shown back by the server. */
  httpBearerToken: string

  /** tcpExpectReply makes delivered mean the far end answered rather than the bytes were sent. */
  tcpExpectReply: boolean

  /* A raw socket destination.
   *
   * Framing is repeated here rather than shared with the source's fields, because they describe two different sockets: a channel can
   * read length-prefixed records from an instrument and write delimited ones onward, and sharing one set would silently tie them.
   */
  destTcpFraming: '' | 'mllp' | 'delimited' | 'fixed' | 'length' | 'whole'
  destTcpDelimiter: string
  destTcpStartBlock: string
  destTcpRecordLength: number
  destTcpLengthBytes: number
  destTcpBigEndian: boolean
  destTcpLengthIncludesHeader: boolean
  destTcpTimeout: string
  destTcpMaxMessageSize: number

  /** tcpKeepAlive holds one connection open across messages. Off by default; some devices refuse a second message on one. */
  tcpKeepAlive: boolean

  /** fileName templates the written file's name, with ${...} placeholders. Empty means a timestamp and control ID. */
  fileName: string

  /** tempSuffix is appended while writing and removed by a rename. Empty means .part. */
  tempSuffix: string

  /** retainHours deletes files older than this. Held as text because an empty box is not zero hours. */
  retainHours: string

  /** fhirClaimUSCore marks each resource as conforming to US Core. */
  fhirClaimUSCore: boolean

  /** fhirValidateBeforeSend checks each resource here rather than learning from the receiver. */
  fhirValidateBeforeSend: boolean

  /** fhirRejectOnWarning refuses a resource that only produces warnings. Only meaningful with validation on. */
  fhirRejectOnWarning: boolean

  /** httpSuccessStatus is which status codes count as delivered, entered as a comma-separated list. */
  httpSuccessStatus: string

  /** httpFollowRedirects allows a 3xx to be followed. Off by default, because a redirect to a different host sends the message
   * somewhere nobody configured. */
  httpFollowRedirects: boolean
  /** dbDriver names the database. */
  dbDriver: string
  /** dbDSN is the connection string, ideally referencing ${ENV_VAR}. */
  dbDSN: string
  /** dbStatement runs once per message. */
  dbStatement: string
  /** dbParams are field paths bound to the statement in order. */
  dbParams: string[]
  /** sftpHost is the server, with an optional port. */
  sftpHost: string
  /** sftpUser is the login name. */
  sftpUser: string
  /** sftpPassword authenticates, ideally as ${ENV_VAR}. */
  sftpPassword: string
  /** sftpKeyFile is a private key, which is the better choice. */
  sftpKeyFile: string
  /** sftpKnownHosts verifies the server. Required. */
  sftpKnownHosts: string
  /** sftpDir is the remote directory. */
  sftpDir: string
  /** smtpHost is the mail server, with an optional port. 587 is assumed. */
  smtpHost: string
  smtpFrom: string
  /** smtpTo, smtpCC and smtpBCC are comma-separated in the form and split on send. */
  smtpTo: string
  smtpCC: string
  smtpBCC: string
  smtpSubject: string
  smtpBody: string
  /** smtpAttach sends the message as a file rather than in the body. */
  smtpAttach: boolean

  /** smtpAttachName is what the recipient sees. Empty means a generated name. */
  smtpAttachName: string

  /** s3SessionToken is for temporary credentials only. They expire, and a channel using one stops delivering when they do. */
  s3SessionToken: string

  /** docFontSize is in points. Held as text because an empty box is not a size of zero. */
  docFontSize: string

  /** httpHeaders is one "name: value" per line. */
  httpHeaders: string

  /** fhirIdentifierSystems maps an HL7 assigning authority to a system URI, one "authority: uri" per line. */
  fhirIdentifierSystems: string

  /** fhirTimezone resolves an HL7 timestamp with no offset. Without it the reader assumes its own. */
  fhirTimezone: string

  /** The queue block, which the builder could not write at all.
   *
   * Distinct from retry: retry governs immediate attempts inside one delivery, and the queue governs what happens once those are
   * exhausted - the message leaves the delivery path and is retried later on a longer schedule. */
  /** dbMaxOpenConns bounds a database destination's connection pool. */
  dbMaxOpenConns: string

  queueEnabled: boolean
  queueMaxAttempts: string
  queueBackoff: string
  queueMaxBackoff: string
  /** queueMaxDepth and queueRetainHours bound the queue on disk. With neither, it grows until the filesystem fills. */
  queueMaxDepth: string
  queueRetainHours: string

  /** A broker destination, which the builder could not create at all. Named apart from the broker source's fields because a
   * channel can read from one broker and publish to another. */
  destBrokerAddr: string
  destBrokerDestination: string
  destBrokerLogin: string
  destBrokerPasscode: string
  destBrokerContentType: string
  /** destBrokerPersistent asks the broker to survive its own restart. True by default, and sent only when turned off. */
  destBrokerPersistent: boolean

  /** smtpStartTLS encrypts the connection. True by default, and sent only when turned off. */
  smtpStartTLS: boolean

  /** soapUrl and the rest describe a SOAP call. */
  soapUrl: string
  soapAction: string
  soapVersion: '1.1' | '1.2'
  soapBody: string
  soapHeader: string
  soapUsername: string
  soapPassword: string
  soapFaultIsSuccess: string

  /** dicomAddr and the rest describe an imaging archive. */
  dicomAddr: string
  dicomCalledAe: string
  dicomCallingAe: string
  dicomTls: boolean

  /** DICOMweb destination (STOW-RS). */

  /** jsScript is the code a script destination runs. */
  jsScript: string

  /** responseTransformer inspects what the receiver said back and may mark the delivery failed. */
  responseTransformer: string
  jsTimeout: string
  jsRequireResult: boolean

  /** docDir and the rest describe a rendered document. */
  docDir: string
  docFormat: 'pdf' | 'text'
  docTemplate: string
  docTitle: string
  docLandscape: boolean

  /** ftpHost, ftpDir and the rest describe an FTP upload. */
  ftpHost: string
  ftpUser: string
  ftpPassword: string
  ftpSecurity: 'explicit' | 'implicit' | 'none'
  ftpAllowClearPassword: boolean
  ftpInsecureSkipVerify: boolean
  ftpDir: string

  /** s3Bucket, s3Region and s3Key describe where an object goes. */
  s3Bucket: string
  s3Region: string
  s3Key: string
  /**
   * s3AccessKeyId and s3SecretAccessKey may hold a literal or a ${VARIABLE} reference.
   *
   * The form suggests the reference rather than refusing a literal. Refusing would only send somebody to
   * the text editor, where the same secret lands in the same file with no advice attached.
   */
  s3AccessKeyId: string
  s3SecretAccessKey: string
  /** s3Endpoint and s3PathStyle are for MinIO, Ceph and the rest. */
  s3Endpoint: string
  s3PathStyle: boolean
  s3Encryption: string

  /** routeTo is the name of another channel to hand the message to. */
  routeTo: string
  smtpUsername: string
  smtpPassword: string
  timeoutSeconds: number
  retryAttempts: number
  retryBackoffSeconds: number
  retryMaxBackoffSeconds: number
  /** Rules narrowing what this destination receives. */
  rules: Rule[]

  /** rawFilter holds this destination's filter as written when the rule rows cannot represent it. */
  rawFilter?: string
}

/** The transports a channel can receive on. */
export type SourceKind =
  | 'mllp'
  | 'http'
  | 'database'
  | 'sftp'
  | 'dicom'
  | 'dicom_query'
  | 'javascript'
  | 'broker'
  | 'soap'
  | 'file'
  | 'ftp'
  | 'smb'
  | 'webdav'
  | 'tcp'
  | 'serial'

/** The declarative changes a channel can make, in the order they are offered. */
export type StepKind =
  | 'set'
  | 'copy'
  | 'map'
  | 'replace'
  | 'clear'
  | 'remove'
  | 'trim'
  | 'case'
  | 'pad'
  | 'date'

/** One declarative transformation being edited. */
//
// The id is client-side only, so a list that gets reordered and deleted keeps its React
// keys. It is stripped before the draft is sent anywhere.
export interface DraftStep {
  id: string
  kind: StepKind
  description: string
  when: string

  path: string
  value: string
  from: string
  to: string
  fallback: string
  pattern: string
  replacement: string
  all: boolean
  width: number
  padWith: string
  padRight: boolean
  table: [string, string][]
  unmatched: 'keep' | 'default' | 'strict'
  caseTo: 'upper' | 'lower'

  /** dateOnError is what happens when a value does not match the format it claims: fail, keep or clear. */
  dateOnError: 'fail' | 'keep' | 'clear'
}

/** The changes an HL7 v3 channel can make.
 *
 * A separate list from StepKind rather than a subset, because the two vocabularies genuinely
 * differ. Pad and date are absent: padding to a fixed width has no meaning in XML, and a v3
 * timestamp is constrained by its datatype so reformatting one is how a document stops
 * validating. Nullflavor is present and has no v2 equivalent at all.
 */
export type V3StepKind =
  | 'set'
  | 'copy'
  | 'map'
  | 'replace'
  | 'clear'
  | 'nullflavor'
  | 'remove'
  | 'trim'
  | 'case'

/** The null flavours a step may write, with what each one means.
 *
 * Shown as a list with explanations rather than a free-text box, because these are the whole
 * point of the step and a typo does not fail - it writes a code no receiver recognises, which
 * most treat as "no information". So a step meaning "we asked and they did not know" quietly
 * means "nothing is known", and the difference is whether anybody asks again.
 */
export const NULL_FLAVOURS: { value: string; label: string }[] = [
  { value: 'MSK', label: 'MSK — withheld deliberately, usually for privacy' },
  { value: 'ASKU', label: 'ASKU — asked, and the answer was not known' },
  { value: 'NASK', label: 'NASK — nobody asked' },
  { value: 'NAV', label: 'NAV — temporarily unavailable, try later' },
  { value: 'UNK', label: 'UNK — a value exists and is not known' },
  { value: 'NA', label: 'NA — the question does not apply' },
  { value: 'NI', label: 'NI — nothing is known' },
  { value: 'OTH', label: 'OTH — a value exists, outside the permitted set' },
  { value: 'PINF', label: 'PINF — positive infinity, for an open-ended range' },
  { value: 'NINF', label: 'NINF — negative infinity' },
]

/** One HL7 v3 transformation being edited. */
export interface DraftV3Step {
  id: string
  kind: V3StepKind
  description: string
  when: string

  path: string
  value: string
  from: string
  to: string
  /** table names a shared lookup table. No inline entries: a v3 mapping is between coded
   * vocabularies, which several channels share and somebody maintains in one place. */
  table: string
  onMissing: 'keep' | 'clear' | 'fail'
  reason: string
  caseTo: 'upper' | 'lower'
}

/** DICOMStepKind is one of the four named imaging actions. */
export type DICOMStepKind = 'deidentify' | 'setAeTitle' | 'stripPrivate' | 'setInstitution'

/** DraftDICOMStep is one named imaging step being edited.
 *
 * Named actions rather than a path and a value, which is the whole design for this format. An object is
 * binary with pixel data in it, so a general tag writer can produce an image that opens and is wrong —
 * and a radiologist reading the study has no way to detect that. Each action knows which tags it touches.
 */
export interface DraftDICOMStep {
  id: string
  kind: DICOMStepKind
  description: string

  /** keepDates leaves study and birth dates in place, for research where intervals must stay comparable. */
  keepDates: boolean
  patientId: string
  patientName: string

  calling: string
  called: string

  /** keep names private tags to leave alone, because some vendors carry needed information in them. */
  keep: string

  institutionName: string
  address: string
  department: string
}

/** newDICOMStep makes an empty imaging step. */
export function newDICOMStep(kind: DICOMStepKind = 'deidentify'): DraftDICOMStep {
  return {
    id: Math.random().toString(36).slice(2),
    kind,
    description: '',
    keepDates: false,
    patientId: '',
    patientName: '',
    calling: '',
    called: '',
    keep: '',
    institutionName: '',
    address: '',
    department: '',
  }
}

/** dicomStepToWire turns one imaging step into what the server expects.
 *
 * Only the fields the chosen action uses are sent. Sending the rest would put keys in the channel file that
 * the action ignores, and somebody reading it later would reasonably think they did something.
 */
export function dicomStepToWire(step: DraftDICOMStep): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  if (step.description.trim()) out.description = step.description.trim()

  switch (step.kind) {
    case 'deidentify':
      out.deidentify = {
        keepDates: step.keepDates || undefined,
        patientId: step.patientId.trim() || undefined,
        patientName: step.patientName.trim() || undefined,
      }
      break

    case 'setAeTitle':
      out.setAeTitle = {
        calling: step.calling.trim() || undefined,
        called: step.called.trim() || undefined,
      }
      break

    case 'stripPrivate': {
      // One tag per line, because a tag is written group,element and so contains a comma. Splitting on commas
      // turned 0029,1010 into two entries, 0029 and 1010, neither of which is a tag - and the server refused
      // the file with an error naming a value nobody typed. So the separator cannot be the character that
      // appears inside every value, and a newline is the one this form already uses for lists of paths.
      const keep = step.keep
        .split(/[\r\n]+/)
        .map((k) => k.trim())
        .filter(Boolean)
      out.stripPrivate = { keep: keep.length ? keep : undefined }
      break
    }

    case 'setInstitution':
      out.setInstitution = {
        name: step.institutionName.trim() || undefined,
        address: step.address.trim() || undefined,
        department: step.department.trim() || undefined,
      }
      break
  }

  return out
}

/** newV3Step makes an empty v3 step. */
export function newV3Step(kind: V3StepKind = 'set'): DraftV3Step {
  return {
    id: nextId('v3step'),
    kind,
    description: '',
    when: '',
    path: '',
    value: '',
    from: '',
    to: '',
    table: '',
    // Keep, matching the file default, but the form states what it means rather than leaving
    // somebody to discover that an untranslated code travels on unchanged.
    onMissing: 'keep',
    // Masked rather than the first alphabetically. Withholding a value is the commonest reason
    // a channel sets a null flavour deliberately - a research feed or a protected record.
    reason: 'MSK',
    caseTo: 'upper',
  }
}

export interface ChannelDraft {
  name: string
  description: string
  /** Empty means ungrouped, which is a legitimate answer for a site with nine channels. */
  group: string
  enabled: boolean

  /** dataType decides which format the channel parses. */
  /**
   * dataType is what the channel receives.
   *
   * delimited, dicom and raw were absent, so three of the eight formats the loader supports could not be
   * chosen from the form at all - a channel carrying a CSV feed or an imaging object had to be written by
   * hand, which is against the principle that everything is reachable from the interface.
   */
  dataType: 'hl7' | 'x12' | 'hl7v3' | 'ncpdp' | 'script' | 'delimited' | 'dicom' | 'raw'
  /** Delimited format options. A delimited channel was choosable and had no options at all in the interface. */
  delimitedDelimiter: string
  delimitedQuote: string
  delimitedComment: string
  delimitedHasHeader: boolean
  /** delimitedColumns names the columns in order, comma separated. Needed when there is no header row. */
  delimitedColumns: string
  delimitedTrimSpace: boolean
  delimitedRelaxed: boolean
  delimitedKeepBlankLines: boolean

  x12Envelope: 'require' | 'warn' | 'ignore'
  x12Split: boolean

  /** Which acknowledgement to send a trading partner back, or none.
   *
   *  Which one is a property of the relationship rather than of the message: a 999
   *  supersedes a 997 for HIPAA transactions, but many older payer connections expect a
   *  997 and treat a 999 as an unrecognised file. */
  x12Acknowledge: 'none' | '999' | '997' | 'ta1'

  /** Our own interchange identifier, becoming ISA06 and ISA05 of any acknowledgement.
   *
   *  Must be the values the partner has configured for us. An interchange whose ISA06 they
   *  do not recognise is discarded before anybody reads it. */
  x12AckSenderId: string
  x12AckSenderQualifier: string

  /** The HL7 v3 filter, using v3 paths rather than v2 segment paths.
   *
   *  Its own field rather than reusing the channel filter, because the two are different
   *  languages against different message models — a v2 filter on a v3 channel would never
   *  match a single message, and the server refuses the combination at load. */
  v3Filter: string
  /** v3Steps are the declarative changes for a v3 channel, separate from steps above. */
  v3Steps: DraftV3Step[]

  /** dicomSteps are the named changes for an imaging channel. */
  dicomSteps: DraftDICOMStep[]

  /** Whether to send an MCCI_IN000002UV01 acknowledgement back.
   *
   *  On by default, unlike X12: a v3 sender is generally waiting on one, and a sender that
   *  receives nothing usually retries — which is how a patient gets registered three times. */
  v3Acknowledge: boolean

  /** Our identity in an acknowledgement. Required whenever the channel acknowledges,
   *  because a receiver that does not recognise the device may discard the reply, which
   *  looks exactly like sending nothing. */
  v3SenderDevice: string
  v3SenderOid: string

  /** sourceKind decides which of the source blocks below is used. */
  sourceKind: SourceKind

  /** soapSource fields describe accepting messages inside a SOAP envelope. */
  soapSourceListen: string
  soapSourcePath: string
  soapSourceVersion: '1.1' | '1.2'
  soapSourceElement: string
  soapSourceBase64: boolean
  soapSourceResponseElement: string

  /** soapSourceResponseNamespace is the namespace the response element sits in. A generated client validates both. */
  /** sftpKeyPassphrase decrypts an encrypted private key. */
  sftpKeyPassphrase: string

  /** sftpMaxFileSize bounds one collected file. Empty means no limit, and the limit is then memory. */
  sftpMaxFileSize: string

  soapSourceResponseNamespace: string

  /** soapSourceWSDL is a file served at ?wsdl, which callers generate their client from. */
  soapSourceWSDL: string

  /** soapSourceFaultOnNak answers a rejection with a SOAP fault rather than a normal response carrying a NAK. */
  soapSourceFaultOnNak: boolean
  soapSourceToken: string
  soapSourceUsername: string
  soapSourcePassword: string

  /** attachments move large fields out of the stored message. */
  attachmentsPaths: string
  attachmentsMinBytes: number
  attachmentsReassemble: boolean

  /** dicomListen and the rest describe an imaging listener. */
  dicomListen: string
  dicomListenAe: string
  dicomListenTls: boolean

  /** dicomQuery fields describe polling an archive with C-FIND. */
  dicomQueryAddr: string
  dicomQueryCalledAe: string
  dicomQueryCallingAe: string
  dicomQueryLevel: 'STUDY' | 'SERIES' | 'IMAGE'
  dicomQueryInterval: string
  dicomQueryWindow: string

  /** dicomQueryOverlap extends each poll further back, so a study timestamped just after it appeared is not missed. */
  /** TLS on an MLLP listener, which is its own server with its own certificate. */
  mllpTlsEnabled: boolean
  mllpTlsCertFile: string
  mllpTlsKeyFile: string
  mllpTlsCAFile: string
  mllpTlsRequireClientCert: boolean

  dicomQueryOverlap: string

  /** dicomQueryPatientRoot picks the patient information model rather than the study one. */
  dicomQueryPatientRoot: boolean

  dicomQueryReturn: string
  dicomQueryEmitFirst: boolean
  dicomQueryTls: boolean

  /** C-MOVE: ask an archive to push images to a named AE. */
  dicomMoveAddr: string
  dicomMoveCalledAe: string
  dicomMoveCallingAe: string
  dicomMoveDestAe: string
  dicomMoveLevel: 'STUDY' | 'SERIES' | 'IMAGE'
  dicomMoveTls: boolean

  /** Modality Worklist: query scheduled procedures. */
  dicomWorklistAddr: string
  dicomWorklistCalledAe: string
  dicomWorklistCallingAe: string
  dicomWorklistInterval: string
  dicomWorklistStationAe: string
  dicomWorklistModality: string
  dicomWorklistTls: boolean

  /** DICOMweb source (QIDO-RS polling). */

  /** JavaScript Reader source. */
  jsReaderScript: string
  jsReaderInterval: string
  jsReaderTimeout: string

  listen: string
  ackWhen: AckWhen

  /** ackIncludeTriggerEvent names the original trigger event in MSH-9 of the acknowledgement rather than leaving it as ACK. */
  ackIncludeTriggerEvent: boolean
  ackApplication: string
  ackFacility: string
  idleTimeoutSeconds: number
  maxConnections: number

  httpListen: string
  httpPath: string
  httpToken: string

  /** TLS on the channel's own listener, which is separate from the TLS on Perfuse's web interface. */
  /** What a DICOM C-STORE listener accepts. Left empty, it accepts anything from anyone that can reach the port. */
  dicomAllowedCallingAe: string
  dicomSopClasses: string
  dicomTransferSyntaxes: string
  dicomMaxObjectBytes: string

  httpTlsEnabled: boolean
  httpTlsCertFile: string
  httpTlsKeyFile: string
  /** httpTlsCAFile is what a client certificate is checked against. Required when one is demanded. */
  httpTlsCAFile: string
  httpTlsRequireClientCert: boolean

  /** httpReadTimeout is a duration string. A sender that stops writing otherwise holds the connection open. */
  httpReadTimeout: string
  /** httpMaxMessageSize bounds one request body, held as text because an empty box is not a limit of zero. */
  httpMaxMessageSize: string

  dbDriver: string
  dbDSN: string
  dbQuery: string
  dbColumn: string
  dbTemplate: string

  /** dbQueryTimeout bounds one poll query. Without it, a query that never returns stops the channel reading without failing. */
  dbQueryTimeout: string

  /** dbMaxAttempts is how many times one row is tried before it is left alone. */
  dbMaxAttempts: string
  dbKeyColumn: string
  dbAfterQuery: string
  dbPollSeconds: number

  sftpHost: string
  sftpUser: string
  sftpPassword: string
  sftpKeyFile: string
  sftpKnownHosts: string
  sftpDir: string
  sftpPattern: string
  sftpMoveTo: string
  sftpPollSeconds: number
  sftpStableSeconds: number

  // Collecting files, whichever transport carries them.
  //
  // One set of settle and disposal fields rather than one per transport, because only one source kind is ever active and
  // the rules are identical. A site that has worked out the right settle time for its analyser should not have to work it
  // out again when the analyser moves to a share.

  /** fileRoot bounds every path a file source touches. Anything resolving outside it is refused. */
  fileRoot: string
  fileDir: string
  /** filePattern selects files by glob. Empty reads everything, including a file still being written under its name. */
  filePattern: string
  filePollSeconds: number
  /**
   * fileStableSeconds is how long a file must stop changing before it is read.
   *
   * The most important setting on any file source. Read too early, a half-written HL7 message usually still parses, so it
   * is accepted, acknowledged, delivered, and nothing ever says the rest was missing.
   */
  fileStableSeconds: number
  fileAfterRead: 'delete' | 'move' | 'leave'
  fileMoveTo: string
  /** fileErrorDir is kept separate from the archive so failures are a directory somebody can watch. */
  fileErrorDir: string
  /** fileRaw delivers each file whole and unparsed, for a PDF, a zip or a proprietary export. */
  fileRaw: boolean
  fileFramed: boolean
  /** fileBatchSize bounds how many files one poll reads, which is what keeps the first poll after an outage sane. */
  fileBatchSize: number
  /** fileSortBy orders files within a poll: an A08 read before its A01 produces a patient who does not exist yet. */
  fileSortBy: 'name' | 'modified' | 'none'
  fileFollowSymlinks: boolean

  // A message broker, reached over STOMP. JMS is a Java API rather than a protocol, so STOMP is what actually crosses
  // the wire; ActiveMQ, Artemis and RabbitMQ all speak it.
  brokerAddr: string
  /** brokerDestination is the queue or topic. Passed through unchanged, because brokers spell these differently. */
  brokerDestination: string
  brokerLogin: string
  brokerPasscode: string
  /**
   * brokerSelector filters at the broker rather than here.
   *
   * On a shared queue that is the difference between reading a hundred messages a day and a hundred thousand.
   */
  brokerSelector: string
  /**
   * brokerSubscriptionID names a durable subscription.
   *
   * Named rather than generated: a generated one creates a new subscription on every restart and leaves the old ones
   * accumulating messages nobody reads.
   */
  brokerSubscriptionID: string

  /** brokerHeartbeat is how often each side proves it is still there. Without one, a cut connection looks alive from here. */
  brokerHeartbeat: string

  /** brokerReconnect is the wait after a dropped connection. */
  brokerReconnect: string

  ftpSrcHost: string
  ftpSrcUser: string
  ftpSrcPassword: string
  /** ftpSrcSecurity is explicit, implicit or none. Plain FTP has to be chosen by name. */
  ftpSrcSecurity: 'explicit' | 'implicit' | 'none'

  smbHost: string
  /** smbShare is the share name only. A path here is refused rather than trimmed. */
  smbShare: string
  smbUser: string
  smbPassword: string
  /** smbDomain is the Windows domain or workgroup. Empty is usually right for a local account. */
  smbDomain: string

  webdavUrl: string
  webdavUser: string
  webdavPassword: string

  // A raw socket and a serial cable. Both need framing, and it is the same framing.

  tcpListen: string
  /**
   * framingMode says how message boundaries are found.
   *
   * There is deliberately no default. Reading a stream with the wrong framing does not fail, it produces messages that
   * look plausible - cut in half, or two joined into one, both of which usually parse.
   */
  framingMode: '' | 'mllp' | 'delimited' | 'fixed' | 'length' | 'whole'
  /** framingDelimiter ends a message, written with escapes such as \r or \x03. */
  framingDelimiter: string
  /** framingStartBlock begins a message. Bytes before it are discarded as noise from a partial connection. */
  framingStartBlock: string

  /** framingKeepDelimiter includes the delimiter in the message. Off by default: it is framing, not content. */
  framingKeepDelimiter: boolean

  /** framingTrimPadding strips the padding a fixed-length record carries. On by default, hence the inverted send. */
  framingTrimPadding: boolean
  framingRecordLength: number
  framingLengthBytes: number
  framingBigEndian: boolean
  /** framingLengthIncludesHeader: both conventions exist and the difference is silent. */
  framingLengthIncludesHeader: boolean
  /** sourceReply is what to send back. A device expecting one that never comes retries the same message forever. */
  sourceReply: 'none' | 'ack' | 'text'
  sourceReplyText: string

  serialPort: string
  /** serialBaud has no default. A wrong speed delivers readable-looking nonsense rather than failing. */
  serialBaud: number
  serialDataBits: number
  serialParity: 'none' | 'odd' | 'even' | 'mark' | 'space'
  serialStopBits: '1' | '1.5' | '2'
  serialFlowControl: 'none' | 'hardware' | 'software'
  /**
   * serialQuietAfter is the only way anybody learns a serial feed has died.
   *
   * A cable has no connection to lose, so an unplugged cable, a device switched off and a device with nothing to say are
   * the same silence.
   */
  serialQuietSeconds: number

  /** serialReopenAfter recovers a USB adapter that was unplugged and replugged, which returns the same path with a new handle. */
  serialReopenAfter: string

  rules: Rule[]

  /**
   * rawFilter holds the filter as written when the rule editor cannot faithfully represent it.
   *
   * Set only when opening an existing channel whose expression does not round-trip through the rule
   * rows. When it is set the form shows the expression as text and writes it back untouched, so
   * opening a channel can never quietly rewrite its filter.
   */
  rawFilter?: string
  steps: DraftStep[]

  /**
   * scriptLanguage applies to every script on the channel: javascript, lua, or wasm.
   *
   * When it is wasm the script fields hold a path to a compiled module rather than source, because a module is
   * compiled output and two megabytes of it cannot be a string in a channel file.
   *
   * One setting for the block rather than one per script, matching the loader. A channel's scripts share
   * the channel map and are read together, so two languages in one channel would mean two languages in
   * one review, sharing state through a map whose values would have to mean the same in both.
   */
  scriptLanguage: 'javascript' | 'lua' | 'wasm'

  /** Capabilities a script is granted. Refused unless named, so a transformer cannot quietly start reading the filesystem. */
  scriptAllowFile: boolean
  scriptAllowDatabase: boolean
  scriptAllowRoute: boolean

  /** scriptFileRoots names the directories file access is limited to. Required whenever file is granted. */
  scriptFileRoots: string

  filterScript: string
  transformerScript: string

  /**
   * The remaining four slots. These existed in the loader and had no field here, so a channel needing a
   * preprocessor had to be written by hand - and a preprocessor is the only place a message that does not
   * parse can be repaired, which is the case somebody hits on day one with a real feed.
   */
  preprocessorScript: string
  postprocessorScript: string
  deployScript: string
  undeployScript: string

  destinations: Destination[]
}

export function newStep(kind: StepKind = 'set'): DraftStep {
  return {
    id: nextId('step'),
    kind,
    description: '',
    when: '',
    path: '',
    value: '',
    from: '',
    to: '',
    fallback: '',
    pattern: '',
    replacement: '',
    all: false,
    // Ten and a zero, because the overwhelmingly common use is padding a record number
    // to a fixed width, and a width of zero would be a step that does nothing.
    width: 10,
    padWith: '0',
    padRight: false,
    table: [],
    unmatched: 'keep',
    caseTo: 'upper',
    dateOnError: 'fail',
  }
}

/** secondsOrNone renders a duration for the config file, or omits it when unset. */
//
// Distinct from the existing seconds helper, which always returns a value. Here zero has
// to mean "not stated" so the key is left out of the generated file entirely.
function secondsOrNone(n: number): string | undefined {
  return n > 0 ? `${n}s` : undefined
}

/** draftToWire maps the form state onto the server's build model. */
//
// The server turns this into YAML and validates it. Nothing here writes YAML, which is
// deliberate: quoting a path containing a colon, or a value that looks like a number, is
// the kind of thing that silently produces a file meaning something other than what the
// form displayed.
/**
 * filePollBlock is the part of a file source that is identical on every transport.
 *
 * One function rather than five copies, matching the server, where the settle rule and disposal live in one poller. Five
 * copies here would drift from each other and the differences would only show up as a site whose files behaved oddly on
 * one transport.
 */
function filePollBlock(draft: ChannelDraft): Record<string, unknown> {
  return {
    dir: draft.fileDir && draft.fileDir !== '.' ? draft.fileDir : undefined,
    pattern: draft.filePattern || undefined,
    pollInterval: secondsOrNone(draft.filePollSeconds),
    stableFor: secondsOrNone(draft.fileStableSeconds),
    afterRead: draft.fileAfterRead !== 'move' ? draft.fileAfterRead : undefined,
    moveTo: draft.fileAfterRead === 'move' ? draft.fileMoveTo || undefined : undefined,
    errorDir: draft.fileErrorDir || undefined,
    raw: draft.fileRaw || undefined,
    framed: draft.fileFramed || undefined,
    batchSize: draft.fileBatchSize || undefined,
    sortBy: draft.fileSortBy !== 'name' ? draft.fileSortBy : undefined,
  }
}

/**
 * framingBlock is how message boundaries are found, shared by the socket and serial sources.
 *
 * Only the fields the chosen framing uses are emitted. The server refuses a record length on a delimited stream rather
 * than ignoring it - somebody who set both believed one was taking effect - so emitting all of them would turn a complete
 * form into a channel that will not load.
 */
function framingBlock(draft: ChannelDraft): Record<string, unknown> {
  const out: Record<string, unknown> = { framing: draft.framingMode || undefined }

  switch (draft.framingMode) {
    case 'delimited':
      out.delimiter = draft.framingDelimiter || undefined
      out.startBlock = draft.framingStartBlock || undefined
      out.keepDelimiter = draft.framingKeepDelimiter || undefined
      break
    case 'fixed':
      out.recordLength = draft.framingRecordLength || undefined
      // Sent only when switched off, because the server trims by default. Writing true every time would fill a channel with a
      // line that changes nothing and invite somebody to wonder what it does.
      out.trimPadding = draft.framingTrimPadding ? undefined : false
      break
    case 'length':
      out.lengthBytes = draft.framingLengthBytes !== 4 ? draft.framingLengthBytes : undefined
      out.bigEndian = draft.framingBigEndian || undefined
      out.lengthIncludesHeader = draft.framingLengthIncludesHeader || undefined
      break
  }
  return out
}

export function draftToWire(draft: ChannelDraft): unknown {
  const source: Record<string, unknown> = { type: draft.sourceKind }


  switch (draft.sourceKind) {
    case 'mllp':
      source.listen = draft.listen
      break
    case 'http':
      source.http = {
        listen: draft.httpListen || undefined,
        path: draft.httpPath || undefined,
        token: draft.httpToken || undefined,
        readTimeout: draft.httpReadTimeout || undefined,
        maxMessageSize: Number(draft.httpMaxMessageSize) > 0 ? Number(draft.httpMaxMessageSize) : undefined,

        // The whole block is omitted unless TLS is on, so a channel does not carry certificate paths it will not use - which read
        // as an encrypted listener to anybody skimming the file.
        tls: draft.httpTlsEnabled
          ? {
              enabled: true,
              certFile: draft.httpTlsCertFile || undefined,
              keyFile: draft.httpTlsKeyFile || undefined,
              requireClientCert: draft.httpTlsRequireClientCert || undefined,
              // Only sent when a client certificate is demanded, because on its own it authorises nothing and a file naming an
              // authority that is never consulted is worse than one that names none.
              caFile: draft.httpTlsRequireClientCert ? draft.httpTlsCAFile || undefined : undefined,
            }
          : undefined,
      }
      break
    case 'database':
      source.database = {
        driver: draft.dbDriver || undefined,
        dsn: draft.dbDSN || undefined,
        query: draft.dbQuery || undefined,
        column: draft.dbColumn || undefined,
        template: draft.dbTemplate || undefined,
        keyColumn: draft.dbKeyColumn || undefined,
        afterQuery: draft.dbAfterQuery || undefined,
        pollInterval: secondsOrNone(draft.dbPollSeconds),
        queryTimeout: draft.dbQueryTimeout || undefined,
        maxAttempts: positiveOrNone(draft.dbMaxAttempts),
      }
      break
    case 'sftp':
      source.sftp = {
        host: draft.sftpHost || undefined,
        user: draft.sftpUser || undefined,
        password: draft.sftpPassword || undefined,
        keyFile: draft.sftpKeyFile || undefined,
        knownHostsFile: draft.sftpKnownHosts || undefined,
        dir: draft.sftpDir || undefined,
        pattern: draft.sftpPattern || undefined,
        moveTo: draft.sftpMoveTo || undefined,
        pollInterval: secondsOrNone(draft.sftpPollSeconds),
        stableFor: secondsOrNone(draft.sftpStableSeconds),
        keyPassphrase: draft.sftpKeyPassphrase || undefined,
        maxFileSize: positiveOrNone(draft.sftpMaxFileSize),
      }
      break
    case 'broker':
      source.broker = {
        addr: draft.brokerAddr || undefined,
        destination: draft.brokerDestination || undefined,
        login: draft.brokerLogin || undefined,
        passcode: draft.brokerPasscode || undefined,
        selector: draft.brokerSelector || undefined,
        subscriptionId: draft.brokerSubscriptionID || undefined,
        heartbeat: draft.brokerHeartbeat || undefined,
        reconnect: draft.brokerReconnect || undefined,
      }
      break
    case 'file':
      source.file = { root: draft.fileRoot || undefined, followSymlinks: draft.fileFollowSymlinks || undefined,
        ...filePollBlock(draft) }
      break
    case 'ftp':
      source.ftp = {
        host: draft.ftpSrcHost || undefined,
        user: draft.ftpSrcUser || undefined,
        password: draft.ftpSrcPassword || undefined,
        // Only when it is not the default, so a generated file does not assert FTPS explicitly and read as though
        // somebody had considered and chosen it.
        security: draft.ftpSrcSecurity !== 'explicit' ? draft.ftpSrcSecurity : undefined,
        root: draft.fileRoot || undefined,
        ...filePollBlock(draft),
      }
      break
    case 'smb':
      source.smb = {
        host: draft.smbHost || undefined,
        share: draft.smbShare || undefined,
        user: draft.smbUser || undefined,
        password: draft.smbPassword || undefined,
        domain: draft.smbDomain || undefined,
        root: draft.fileRoot || undefined,
        ...filePollBlock(draft),
      }
      break
    case 'webdav':
      source.webdav = {
        url: draft.webdavUrl || undefined,
        user: draft.webdavUser || undefined,
        password: draft.webdavPassword || undefined,
        ...filePollBlock(draft),
      }
      break
    case 'tcp':
      source.tcp = {
        listen: draft.tcpListen || undefined,
        ...framingBlock(draft),
        reply: draft.sourceReply !== 'none' ? draft.sourceReply : undefined,
        replyText: draft.sourceReply === 'text' ? draft.sourceReplyText || undefined : undefined,
      }
      break
    case 'javascript':
      // The JavaScript Reader source had controls and no mapping at all: the script was typed into the form and discarded, so the
      // channel was built with no source block and polled forever producing nothing - which looks like a channel that is running.
      source.javascript = {
        script: draft.jsReaderScript || undefined,
        pollInterval: draft.jsReaderInterval || undefined,
        timeout: draft.jsReaderTimeout || undefined,
      }
      break
    case 'serial':
      source.serial = {
        port: draft.serialPort || undefined,
        // Emitted even when zero, so the server refuses it with its own explanation rather than the form silently
        // omitting the one setting that has no safe default.
        baud: draft.serialBaud || undefined,
        dataBits: draft.serialDataBits !== 8 ? draft.serialDataBits : undefined,
        parity: draft.serialParity !== 'none' ? draft.serialParity : undefined,
        stopBits: draft.serialStopBits !== '1' ? draft.serialStopBits : undefined,
        flowControl: draft.serialFlowControl !== 'none' ? draft.serialFlowControl : undefined,
        quietAfter: secondsOrNone(draft.serialQuietSeconds),
        ...framingBlock(draft),
        reply: draft.sourceReply !== 'none' ? draft.sourceReply : undefined,
        replyText: draft.sourceReply === 'text' ? draft.sourceReplyText || undefined : undefined,
        reopenAfter: draft.serialReopenAfter || undefined,
      }
      break
    case 'soap':
      source.soap = {
        listen: draft.soapSourceListen || undefined,
        path: draft.soapSourcePath || undefined,
        version: draft.soapSourceVersion !== '1.1' ? draft.soapSourceVersion : undefined,
        element: draft.soapSourceElement || undefined,
        base64: draft.soapSourceBase64 || undefined,
        responseElement: draft.soapSourceResponseElement || undefined,
        token: draft.soapSourceToken || undefined,
        username: draft.soapSourceUsername || undefined,
        password: draft.soapSourcePassword || undefined,
        responseNamespace: draft.soapSourceResponseNamespace || undefined,
        wsdl: draft.soapSourceWSDL || undefined,
        faultOnNak: draft.soapSourceFaultOnNak || undefined,
      }
      break
    case 'dicom':
      source.dicom = {
        listen: draft.dicomListen || undefined,
        aeTitle: draft.dicomListenAe || undefined,
        tls: draft.dicomListenTls ? { enabled: true } : undefined,
        allowedCallingAe: splitList(draft.dicomAllowedCallingAe),
        sopClasses: splitList(draft.dicomSopClasses),
        transferSyntaxes: splitList(draft.dicomTransferSyntaxes),
        maxObjectBytes: Number(draft.dicomMaxObjectBytes) > 0 ? Number(draft.dicomMaxObjectBytes) : undefined,
      }
      break
    case 'dicom_query':
      // camelCase, because this is JSON to the build endpoint. It was dicom_query - the file format's spelling - and the endpoint
      // decodes strictly, so every build with this source type was refused outright with "unknown field dicom_query". The source
      // could be chosen in the form and no channel could be produced from it.
      source.dicomQuery = {
        address: draft.dicomQueryAddr || undefined,
        calledAe: draft.dicomQueryCalledAe || undefined,
        callingAe: draft.dicomQueryCallingAe || undefined,
        // STUDY is the server's own default, so sending it would be stating the default rather than
        // choosing it - and a read-back that differs from what was saved is how a save silently
        // changes a setting nobody touched.
        level: draft.dicomQueryLevel !== 'STUDY' ? draft.dicomQueryLevel : undefined,
        interval: draft.dicomQueryInterval || undefined,
        window: draft.dicomQueryWindow || undefined,
        // One keyword per line in the form, a list on the wire. Blank lines are dropped rather than
        // sent as empty names, which the server would refuse.
        return: linesOrNone(draft.dicomQueryReturn),
        // camelCase, because this goes to the build endpoint as JSON. It was emit_on_first_poll here - the file format's spelling -
        // which the server's strict decoder does not accept, so the setting never arrived however it was set. Found while adding the
        // two fields below, and the reason it survived is that nothing in the form could set it.
        emitOnFirstPoll: draft.dicomQueryEmitFirst || undefined,
        overlap: draft.dicomQueryOverlap || undefined,
        patientRoot: draft.dicomQueryPatientRoot || undefined,
        tls: draft.dicomQueryTls ? { enabled: true } : undefined,
      }
      break
  }

  // X12 has no synchronous acknowledgement at all, so sending ack settings on an X12
  // channel would be configuring something that cannot happen. The loader refuses it
  // rather than ignoring it, which is the right behaviour and also means this has to be
  // correct here.
  // Attachments are a channel setting rather than a source or destination one, because extraction
  // happens once when the message is recorded - not per destination. A per-destination version would
  // mean the same document stored several times over.
  const attachmentPaths = linesOrNone(draft.attachmentsPaths)
  const attachments = attachmentPaths
    ? {
        extract: attachmentPaths.map((path) => ({
          path,
          minBytes: draft.attachmentsMinBytes > 0 ? draft.attachmentsMinBytes : undefined,
        })),
        // Only sent when switched off, because the server's default is on and sending true would
        // state the default rather than choose it.
        reassemble: draft.attachmentsReassemble ? undefined : false,
      }
    : undefined

  if (draft.dataType !== 'x12') {
    const ack: Record<string, unknown> = {}
    if (draft.ackWhen) ack.when = draft.ackWhen
    if (draft.ackApplication) ack.application = draft.ackApplication
    if (draft.ackFacility) ack.facility = draft.ackFacility
    if (draft.ackIncludeTriggerEvent) ack.includeTriggerEvent = true
    if (Object.keys(ack).length > 0) source.ack = ack
  }

  // TLS sits on the source rather than under a per-kind block, so it is set here rather than in the switch above.
  if (draft.sourceKind === 'mllp' && draft.mllpTlsEnabled) {
    source.tls = {
      enabled: true,
      certFile: draft.mllpTlsCertFile || undefined,
      keyFile: draft.mllpTlsKeyFile || undefined,
      requireClientCert: draft.mllpTlsRequireClientCert || undefined,
      // Only sent when a client certificate is demanded, because an authority that is never consulted reads as though senders are
      // being verified when they are not.
      caFile: draft.mllpTlsRequireClientCert ? draft.mllpTlsCAFile || undefined : undefined,
    }
  }

  // Under a limits block, because that is where the build model puts them.
  //
  // These were written flat on the source, and the endpoint decodes strictly - so opening the Connection limits section and typing
  // anything made the whole build fail with "unknown field idleTimeout". The section had never worked, and the failure appeared as a
  // broken builder rather than as a broken field, which is why nobody traced it to this.
  if (draft.idleTimeoutSeconds > 0 || draft.maxConnections > 0) {
    source.limits = {
      idleTimeout: draft.idleTimeoutSeconds > 0 ? secondsOrNone(draft.idleTimeoutSeconds) : undefined,
      maxConnections: draft.maxConnections > 0 ? draft.maxConnections : undefined,
    }
  }

  // A filter the rule editor could not represent is written back exactly as it was read. Regenerating
  // it from empty rule rows would delete the filter of a live channel, which is the worst thing this
  // form could do.
  const filter = draft.rawFilter ?? rulesToExpression(draft.rules)

  const scripts: Record<string, unknown> = {}
  if (draft.filterScript.trim()) scripts.filter = draft.filterScript
  if (draft.transformerScript.trim()) scripts.transformer = draft.transformerScript
  if (draft.preprocessorScript.trim()) scripts.preprocessor = draft.preprocessorScript
  if (draft.postprocessorScript.trim()) scripts.postprocessor = draft.postprocessorScript
  if (draft.deployScript.trim()) scripts.deploy = draft.deployScript
  if (draft.undeployScript.trim()) scripts.undeploy = draft.undeployScript

  // Only written when it is not the default, so opening and saving a JavaScript channel does not add a
  // language key it never had. A diff that shows a line nobody typed makes the next reviewer distrust the
  // whole form.
  if (Object.keys(scripts).length > 0 && draft.scriptLanguage !== 'javascript') {
    scripts.language = draft.scriptLanguage
  }

  // Capabilities, which could not be granted from the form at all.
  //
  // The build model's own comment says the form has to offer file roots or the capability is unusable from the interface. It did not,
  // so granting a script file access meant hand-editing YAML - for the one setting that most needs to be seen, because granting file
  // without naming directories used to reach the whole filesystem, including this program's own database of password hashes.
  const allow: string[] = []
  if (draft.scriptAllowFile) allow.push('file')
  if (draft.scriptAllowDatabase) allow.push('database')
  if (draft.scriptAllowRoute) allow.push('route')

  if (allow.length > 0) {
    scripts.allow = allow

    // Only sent with file access, because it means nothing otherwise and the loader refuses file access without it.
    if (draft.scriptAllowFile) scripts.fileRoots = splitList(draft.scriptFileRoots)
  }

  return {
    name: draft.name,
    description: draft.description.trim() || undefined,
    group: draft.group.trim() || undefined,
    enabled: draft.enabled ? undefined : false,
    dataType: draft.dataType === 'hl7' ? undefined : draft.dataType,
    x12:
      draft.dataType === 'x12'
        ? {
            envelope: draft.x12Envelope,
            split: draft.x12Split,
            acknowledge: draft.x12Acknowledge === 'none' ? undefined : draft.x12Acknowledge,
            ackSenderId: draft.x12Acknowledge === 'none' ? undefined : draft.x12AckSenderId || undefined,
            ackSenderQualifier:
              draft.x12Acknowledge === 'none' ? undefined : draft.x12AckSenderQualifier || undefined,
          }
        : undefined,
    delimited:
      draft.dataType === 'delimited'
        ? {
            // Each one omitted when it matches the server's default, so a channel file says what somebody chose rather than
            // repeating every default back and inviting the question of what each line does.
            delimiter: draft.delimitedDelimiter || undefined,
            quote: draft.delimitedQuote || undefined,
            comment: draft.delimitedComment || undefined,
            hasHeader: draft.delimitedHasHeader || undefined,
            // Only sent when there is no header row. With a header the names come from the file, and a list here would be
            // ignored - which is worse than being refused, because the file looks as though it decided the names.
            columns: draft.delimitedHasHeader
              ? undefined
              : splitList(draft.delimitedColumns),
            trimSpace: draft.delimitedTrimSpace || undefined,
            relaxed: draft.delimitedRelaxed || undefined,
            keepBlankLines: draft.delimitedKeepBlankLines || undefined,
          }
        : undefined,
    hl7v3:
      draft.dataType === 'hl7v3'
        ? {
            filter: draft.v3Filter.trim() || undefined,
            // Sent only when turned off. The server's default is on, so writing true every time would
            // fill a channel file with a line that changes nothing and invite somebody to wonder what
            // it does.
            acknowledge: draft.v3Acknowledge ? undefined : false,
            senderDevice: draft.v3Acknowledge ? draft.v3SenderDevice || undefined : undefined,
            senderOid: draft.v3Acknowledge ? draft.v3SenderOid || undefined : undefined,
            transformations: draft.v3Steps.length ? draft.v3Steps.map(v3StepToWire) : undefined,
          }
        : undefined,
    // A SCRIPT channel keeps its steps in the script block, using the same step shape.
    //
    // The same v3Steps array feeds both, because a prescription and a v3 document are both XML addressed by the
    // same path grammar - the server borrows hl7v3.Path for both. A second array would mean a user who changed
    // the message format lost their steps to no purpose, since the steps would still be valid.
    //
    // nullflavor is the one action that does not carry over: it states why a v3 value is absent and a
    // prescription has no equivalent, so the server refuses it here. The editor stops offering it when the
    // format is SCRIPT rather than letting the form produce a file that will not load.
    // An imaging channel's named steps. Its own block for the same reason every other format's steps are:
    // the vocabularies differ, and one key meaning different things by dataType is a field nobody can read.
    dicom:
      draft.dataType === 'dicom' && draft.dicomSteps.length
        ? { transformations: draft.dicomSteps.map(dicomStepToWire) }
        : undefined,
    script:
      draft.dataType === 'script' && draft.v3Steps.length
        ? { transformations: draft.v3Steps.map(v3StepToWire) }
        : undefined,
    source,
    attachments,
    // A v3 channel keeps its filter in the hl7v3 block instead. Sending both is refused by the server,
    // because one of them could only be ignored - so the form must not offer to do it.
    filter: draft.dataType === 'hl7v3' ? undefined : filter || undefined,
    transformations:
      draft.dataType === 'hl7v3' || !draft.steps.length ? undefined : draft.steps.map(stepToWire),
    scripts: draft.dataType === 'hl7v3' || !Object.keys(scripts).length ? undefined : scripts,
    destinations: draft.destinations.map(destinationToWire),
  }
}

/** v3StepToWire turns one v3 step into the shape the server reads.
 *
 * Separate from stepToWire because the two vocabularies differ, and mapping one onto the other
 * would mean silently dropping nullflavor - the one construct v2 cannot express and the reason
 * a v3 transformation is not just the v2 one with a different path parser.
 */
function v3StepToWire(step: DraftV3Step): unknown {
  const out: Record<string, unknown> = {}
  if (step.description.trim()) out.description = step.description.trim()
  if (step.when.trim()) out.when = step.when.trim()

  switch (step.kind) {
    case 'set':
      out.set = { path: step.path, value: step.value }
      break
    case 'copy':
      out.copy = { from: step.from, to: step.to }
      break
    case 'clear':
      out.clear = { path: step.path }
      break
    case 'nullflavor':
      out.nullflavor = { path: step.path, reason: step.reason }
      break
    case 'remove':
      out.remove = { path: step.path }
      break
    case 'map':
      out.map = {
        path: step.path,
        table: step.table,
        // Keep is the server's default, so sending it would state the default rather than choose
        // it - and a read-back that differs from what was saved is how a save silently changes
        // something nobody touched.
        onMissing: step.onMissing === 'keep' ? undefined : step.onMissing,
      }
      break
    case 'replace':
      out.replace = { path: step.path, from: step.from, to: step.to }
      break
    case 'trim':
      out.trim = { path: step.path }
      break
    case 'case':
      out.case = { path: step.path, to: step.caseTo }
      break
  }

  return out
}

function stepToWire(step: DraftStep): unknown {
  const out: Record<string, unknown> = {}
  if (step.description.trim()) out.description = step.description.trim()
  if (step.when.trim()) out.when = step.when.trim()

  switch (step.kind) {
    case 'set':
      out.set = { path: step.path, value: step.value }
      break
    case 'copy':
      out.copy = {
        from: step.from,
        to: step.to,
        default: step.fallback || undefined,
      }
      break
    case 'clear':
      out.clear = { path: step.path }
      break
    case 'remove':
      out.remove = { path: step.path }
      break
    case 'trim':
      out.trim = { path: step.path }
      break
    case 'case':
      out.case = { path: step.path, to: step.caseTo }
      break
    case 'replace':
      out.replace = {
        path: step.path,
        pattern: step.pattern,
        with: step.replacement,
        all: step.all || undefined,
      }
      break
    case 'pad':
      out.pad = {
        path: step.path,
        width: step.width,
        with: step.padWith || undefined,
        right: step.padRight || undefined,
      }
      break
    case 'date':
      out.date = {
        path: step.path,
        from: step.from,
        to: step.to,
        // Sent only when it is not the default, so a channel does not carry a line stating what would happen anyway - and the
        // default is the safe one, because a timestamp that silently did not convert is accepted downstream and then misread.
        onError: step.dateOnError !== 'fail' ? step.dateOnError : undefined,
      }
      break
    case 'map': {
      const table: Record<string, string> = {}
      for (const [from, to] of step.table) {
        // An empty code is a half-typed row, not a translation of the empty string.
        // Sending it would make the table claim something nobody meant.
        if (from !== '') table[from] = to
      }
      out.map = {
        path: step.path,
        table,
        strict: step.unmatched === 'strict' || undefined,
        default: step.unmatched === 'default' ? step.fallback || 'U' : undefined,
      }
      break
    }
  }
  return out
}

/** addresses splits a comma-separated list, dropping blanks. */
//
// One field is friendlier than a repeater for something people paste from an address book,
// and dropping blanks means a trailing comma is not a recipient called "".
function addresses(raw: string): string[] | undefined {
  const list = raw
    .split(',')
    .map((a) => a.trim())
    .filter(Boolean)
  return list.length ? list : undefined
}

function destinationToWire(d: Destination): unknown {
  const out: Record<string, unknown> = {
    name: d.name,
    type: d.type,
  }
  if (!d.enabled) out.enabled = false

  const filter = d.rawFilter ?? rulesToExpression(d.rules)
  if (filter) out.filter = filter

  switch (d.type) {
    case 'mllp':
      out.address = d.address
      // Under a tcp block, not at the destination's top level. The field guard's path said destinations.tcp.expectReply and I
      // mapped it flat anyway; the form accepted the value and the file never carried it, which is exactly the failure the
      // generated-file assertion exists to catch.
      out.tcp =
        d.tcpExpectReply || d.tcpKeepAlive
          ? {
              expectReply: d.tcpExpectReply || undefined,
              keepAlive: d.tcpKeepAlive || undefined,
            }
          : undefined
      break
    case 'tcp': {
      // Everything under a tcp block, including the address, because that is where the server's model keeps it. The address is also
      // accepted at the top level for consistency with other socket destinations, but writing it in one place keeps the generated
      // file the same shape as one somebody would write by hand.
      const tcp: Record<string, unknown> = {
        address: d.address || undefined,
        framing: d.destTcpFraming || undefined,
        expectReply: d.tcpExpectReply || undefined,
        keepAlive: d.tcpKeepAlive || undefined,
        timeout: d.destTcpTimeout || undefined,
        maxMessageSize: d.destTcpMaxMessageSize || undefined,
      }

      // Only the fields the chosen framing uses. The server refuses a record length on a delimited stream rather than ignoring it,
      // which is the behaviour worth matching: a setting that is silently ignored is one somebody believes is in effect.
      switch (d.destTcpFraming) {
        case 'delimited':
          tcp.delimiter = d.destTcpDelimiter || undefined
          tcp.startBlock = d.destTcpStartBlock || undefined
          break
        case 'fixed':
          tcp.recordLength = d.destTcpRecordLength || undefined
          break
        case 'length':
          tcp.lengthBytes = d.destTcpLengthBytes || undefined
          tcp.bigEndian = d.destTcpBigEndian || undefined
          tcp.lengthIncludesHeader = d.destTcpLengthIncludesHeader || undefined
          break
      }

      out.tcp = tcp
      break
    }
    case 'broker':
      out.broker = {
        addr: d.destBrokerAddr || undefined,
        destination: d.destBrokerDestination || undefined,
        login: d.destBrokerLogin || undefined,
        passcode: d.destBrokerPasscode || undefined,
        contentType: d.destBrokerContentType || undefined,
        // Sent only when switched off, because the server's default is persistent and writing true every time would add a line to
        // every channel that changes nothing.
        persistent: d.destBrokerPersistent ? undefined : false,
      }
      break
    case 'file':
      out.dir = d.dir
      out.fileName = d.fileName || undefined
      out.tempSuffix = d.tempSuffix || undefined
      // Parsed here, and omitted when it is not a positive number, because zero and empty both mean "keep everything" and
      // sending zero would write a retention of nought hours into the file.
      out.retainHours = Number(d.retainHours) > 0 ? Number(d.retainHours) : undefined
      break
    case 'http':
      out.http = {
        url: d.url,
        method: d.httpMethod || undefined,
        contentType: d.httpContentType || undefined,
        failOnBody: d.httpFailOnBody || undefined,
        bearerToken: d.httpBearerToken || undefined,
        // Parsed rather than passed through, because the server expects numbers and a list typed with a stray letter would
        // otherwise be sent as one and refused with a message about JSON rather than about the field.
        successStatus: parseStatusList(d.httpSuccessStatus),
        followRedirects: d.httpFollowRedirects ? true : undefined,
        headers: parsePairs(d.httpHeaders),
      }
      break
    case 'fhir':
      out.fhir = {
        url: d.url,
        version: d.fhirVersion || undefined,
        defaultIdentifierSystem: d.fhirIdentifierSystem || undefined,
        claimUSCore: d.fhirClaimUSCore || undefined,
        validateBeforeSend: d.fhirValidateBeforeSend || undefined,
        // Only sent when validation is on, because on its own it does nothing and a file saying otherwise reads as though
        // warnings are being refused when nothing is being checked.
        rejectOnWarning: d.fhirValidateBeforeSend && d.fhirRejectOnWarning ? true : undefined,
        timezone: d.fhirTimezone || undefined,
        identifierSystems: parsePairs(d.fhirIdentifierSystems),
      }
      break
    case 'cda':
      out.cda = {
        url: d.url || undefined,
        dir: d.dir || undefined,
        write: d.cdaWrite || undefined,
        onNoDocument: d.cdaOnNoDocument || undefined,
        requireAgreement: d.cdaRequireAgreement || undefined,
      }
      break
    case 'database':
      out.database = {
        driver: d.dbDriver || undefined,
        dsn: d.dbDSN || undefined,
        statement: d.dbStatement || undefined,
        params: d.dbParams.length ? d.dbParams : undefined,
        maxOpenConns: positiveOrNone(d.dbMaxOpenConns),
      }
      break
    case 'sftp':
      out.sftp = {
        host: d.sftpHost || undefined,
        user: d.sftpUser || undefined,
        password: d.sftpPassword || undefined,
        keyFile: d.sftpKeyFile || undefined,
        knownHostsFile: d.sftpKnownHosts || undefined,
        dir: d.sftpDir || undefined,
      }
      break
    case 'channel':
      out.channel = { name: d.routeTo }
      break
    case 'dicom':
      out.dicom = {
        // "address", matching the server. This was "addr" first, which the round-trip tests happily
        // accepted because they only check this file against itself - the server rejected it with
        // "unknown field", which is what found it.
        address: d.dicomAddr || undefined,
        calledAe: d.dicomCalledAe || undefined,
        callingAe: d.dicomCallingAe || undefined,
        // Only sent when on. The server's own default is off, so sending false would be
        // indistinguishable from not having an opinion — and a read-back that differs from
        // what was saved is how a save silently downgrades a setting.
        tls: d.dicomTls ? { enabled: true } : undefined,
      }
      break

    case 'javascript':
      out.javascript = {
        script: d.jsScript || undefined,
        timeout: d.jsTimeout || undefined,
        // Inverted, because the server's field says when an empty return counts as success and
        // the form asks the more useful question: must the script say what happened?
        successOnUndefined: d.jsRequireResult ? false : undefined,
      }
      break

    case 'soap':
      out.soap = {
        url: d.soapUrl || undefined,
        action: d.soapAction || undefined,
        version: d.soapVersion !== '1.1' ? d.soapVersion : undefined,
        body: d.soapBody || undefined,
        header: d.soapHeader || undefined,
        username: d.soapUsername || undefined,
        password: d.soapPassword || undefined,
        // Comma-separated in the form and split on save, like the email address fields.
        faultIsSuccess: addresses(d.soapFaultIsSuccess),
      }
      break
    case 'document':
      out.document = {
        dir: d.docDir || undefined,
        format: d.docFormat !== 'pdf' ? d.docFormat : undefined,
        template: d.docTemplate || undefined,
        title: d.docTitle || undefined,
        landscape: d.docLandscape || undefined,
        fontSize: positiveOrNone(d.docFontSize),
      }
      break
    case 'ftp':
      out.ftp = {
        host: d.ftpHost || undefined,
        user: d.ftpUser || undefined,
        password: d.ftpPassword || undefined,
        security: d.ftpSecurity !== 'explicit' ? d.ftpSecurity : undefined,
        allowClearPassword: d.ftpAllowClearPassword || undefined,
        insecureSkipVerify: d.ftpInsecureSkipVerify || undefined,
        dir: d.ftpDir || undefined,
      }
      break
    case 's3':
      out.s3 = {
        bucket: d.s3Bucket || undefined,
        region: d.s3Region || undefined,
        // An empty key is omitted so the server's date-partitioned default applies. Writing an empty
        // string would be a setting that looks configured and is not.
        key: d.s3Key || undefined,
        accessKeyId: d.s3AccessKeyId || undefined,
        secretAccessKey: d.s3SecretAccessKey || undefined,
        sessionToken: d.s3SessionToken || undefined,
        endpoint: d.s3Endpoint || undefined,
        pathStyle: d.s3PathStyle || undefined,
        serverSideEncryption: d.s3Encryption || undefined,
      }
      break
    case 'smtp':
      out.smtp = {
        host: d.smtpHost || undefined,
        from: d.smtpFrom || undefined,
        to: addresses(d.smtpTo),
        cc: addresses(d.smtpCC),
        bcc: addresses(d.smtpBCC),
        subject: d.smtpSubject || undefined,
        body: d.smtpBody || undefined,
        attach: d.smtpAttach || undefined,
        // Only meaningful with an attachment, so it is left out otherwise rather than written and ignored.
        attachName: d.smtpAttach ? d.smtpAttachName || undefined : undefined,
        // Sent only when switched off, because the server's default is on and a pointer field treats absent as secure. Writing
        // true every time would put a line in every channel that changes nothing.
        starttls: d.smtpStartTLS ? undefined : false,
        username: d.smtpUsername || undefined,
        password: d.smtpPassword || undefined,
      }
      break
  }

  if (d.responseTransformer.trim()) out.responseTransformer = d.responseTransformer

  if (d.queueEnabled) {
    out.queue = {
      enabled: true,
      maxAttempts: positiveOrNone(d.queueMaxAttempts),
      backoff: d.queueBackoff || undefined,
      maxBackoff: d.queueMaxBackoff || undefined,
      maxDepth: positiveOrNone(d.queueMaxDepth),
      retainHours: positiveOrNone(d.queueRetainHours),
    }
  }

  if (d.timeoutSeconds > 0) out.timeout = secondsOrNone(d.timeoutSeconds)
  if (d.retryAttempts > 0) {
    out.retry = {
      attempts: d.retryAttempts,
      backoff: secondsOrNone(d.retryBackoffSeconds),
      maxBackoff: secondsOrNone(d.retryMaxBackoffSeconds),
    }
  }
  return out
}

let idCounter = 0
export function nextId(prefix: string): string {
  idCounter += 1
  return `${prefix}-${idCounter}`
}

export function newRule(): Rule {
  return { id: nextId('rule'), join: 'and', path: '', operator: '==', value: '', values: [] }
}

export function newDestination(): Destination {
  return {
    id: nextId('dest'),
    name: '',
    type: 'mllp',
    enabled: true,
    address: '',
    dir: '',
    url: '',
    fhirVersion: 'R4',
    fhirIdentifierSystem: '',
    cdaWrite: 'fhir',
    cdaOnNoDocument: 'skip',
    cdaRequireAgreement: false,
    httpMethod: 'POST',
    httpContentType: '',
    httpFailOnBody: '',
    httpBearerToken: '',
    tcpExpectReply: false,
    destTcpFraming: '',
    destTcpDelimiter: '',
    destTcpStartBlock: '',
    destTcpRecordLength: 0,
    destTcpLengthBytes: 0,
    destTcpBigEndian: false,
    destTcpLengthIncludesHeader: false,
    destTcpTimeout: '',
    destTcpMaxMessageSize: 0,
    tcpKeepAlive: false,
    fileName: '',
    tempSuffix: '',
    retainHours: '',
    fhirClaimUSCore: false,
    fhirValidateBeforeSend: false,
    fhirRejectOnWarning: false,
    httpSuccessStatus: '',
    httpFollowRedirects: false,
    dbDriver: 'postgres',
    dbDSN: '',
    dbStatement: '',
    dbParams: [],
    sftpHost: '',
    sftpUser: '',
    sftpPassword: '',
    sftpKeyFile: '',
    sftpKnownHosts: '/etc/perfuse/known_hosts',
    sftpDir: '',
    smtpHost: '',
    smtpFrom: '',
    smtpTo: '',
    smtpCC: '',
    smtpBCC: '',
    smtpSubject: 'A message arrived',
    smtpBody: '',
    smtpAttach: false,
    smtpAttachName: '',
    s3SessionToken: '',
    docFontSize: '',
    httpHeaders: '',
    fhirIdentifierSystems: '',
    fhirTimezone: '',
    dbMaxOpenConns: '',
    queueEnabled: false,
    queueMaxAttempts: '',
    queueBackoff: '',
    queueMaxBackoff: '',
    queueMaxDepth: '',
    queueRetainHours: '',
    destBrokerAddr: '',
    destBrokerDestination: '',
    destBrokerLogin: '',
    destBrokerPasscode: '',
    destBrokerContentType: '',
    // True, matching the server. A message that does not survive a broker restart is a message lost without anything reporting it.
    destBrokerPersistent: true,
    // True, matching the server's secure default. False here would turn encryption off on any channel built and saved without
    // touching the control.
    smtpStartTLS: true,
    soapUrl: '',
    soapAction: '',
    soapVersion: '1.1',
    soapBody: '',
    soapHeader: '',
    soapUsername: '',
    soapPassword: '',
    soapFaultIsSuccess: '',

    dicomAddr: '',
    dicomCalledAe: '',
    dicomCallingAe: '',
    dicomTls: false,


    jsScript: '',
    responseTransformer: '',
    jsTimeout: '',
    jsRequireResult: false,
    docDir: '',
    docFormat: 'pdf',
    docTemplate: '',
    docTitle: '',
    docLandscape: false,
    ftpHost: '',
    ftpUser: '',
    ftpPassword: '',
    ftpSecurity: 'explicit',
    ftpAllowClearPassword: false,
    ftpInsecureSkipVerify: false,
    ftpDir: '',
    s3Bucket: '',
    s3Region: '',
    s3Key: '',
    s3AccessKeyId: '',
    s3SecretAccessKey: '',
    s3Endpoint: '',
    s3PathStyle: false,
    s3Encryption: '',
    routeTo: '',
    smtpUsername: '',
    smtpPassword: '',
    timeoutSeconds: 30,
    retryAttempts: 5,
    retryBackoffSeconds: 1,
    retryMaxBackoffSeconds: 60,
    rules: [],
  }
}

export function emptyDraft(): ChannelDraft {
  return {
    name: '',
    description: '',
    group: '',
    enabled: true,

    dataType: 'hl7',
    v3Filter: '',
    v3Steps: [],
    dicomSteps: [],
    v3Acknowledge: true,
    v3SenderDevice: '',
    v3SenderOid: '',
    x12Envelope: 'require',
    x12Split: true,
    // None by default. Classical X12 acknowledgement is asynchronous, and a partner who is
    // not expecting one may treat it as an unsolicited interchange.
    x12Acknowledge: 'none',
    x12AckSenderId: '',
    x12AckSenderQualifier: 'ZZ',

    sourceKind: 'mllp',

    soapSourceListen: '',
    soapSourcePath: '',
    soapSourceVersion: '1.1',
    soapSourceElement: '',
    soapSourceBase64: false,
    soapSourceResponseElement: '',
    sftpKeyPassphrase: '',
    sftpMaxFileSize: '',
    soapSourceResponseNamespace: '',
    soapSourceWSDL: '',
    soapSourceFaultOnNak: false,
    soapSourceToken: '',
    soapSourceUsername: '',
    soapSourcePassword: '',

    attachmentsPaths: '',
    attachmentsMinBytes: 0,
    // On by default, matching the server: a message read back without its attachments looks like
    // one that lost them, and nobody debugging a feed wants to wonder which.
    attachmentsReassemble: true,

    dicomListen: '',
    dicomListenAe: '',
    dicomListenTls: false,

    dicomQueryAddr: '',
    dicomQueryCalledAe: '',
    dicomQueryCallingAe: '',
    // Sensible starting points rather than empty. A quarter-hour poll over three days is what a
    // prefetch actually wants, and an empty interval is refused - so a blank form would be a form
    // that cannot be saved without knowing the rules first.
    dicomQueryLevel: 'STUDY',
    dicomQueryInterval: '15m',
    dicomQueryWindow: '72h',
    mllpTlsEnabled: false,
    mllpTlsCertFile: '',
    mllpTlsKeyFile: '',
    mllpTlsCAFile: '',
    mllpTlsRequireClientCert: false,
    dicomQueryOverlap: '',
    dicomQueryPatientRoot: false,
    dicomQueryReturn: '',
    dicomQueryEmitFirst: false,
    dicomQueryTls: false,

    dicomMoveAddr: '',
    dicomMoveCalledAe: '',
    dicomMoveCallingAe: '',
    dicomMoveDestAe: '',
    dicomMoveLevel: 'STUDY',
    dicomMoveTls: false,

    dicomWorklistAddr: '',
    dicomWorklistCalledAe: '',
    dicomWorklistCallingAe: '',
    dicomWorklistInterval: '30s',
    dicomWorklistStationAe: '',
    dicomWorklistModality: '',
    dicomWorklistTls: false,

    jsReaderScript: '',
    jsReaderInterval: '30s',
    jsReaderTimeout: '30s',

    listen: ':6661',
    ackWhen: 'on_delivery',
    ackIncludeTriggerEvent: false,
    ackApplication: 'PERFUSE',
    ackFacility: '',
    idleTimeoutSeconds: 0,
    maxConnections: 0,

    httpListen: '0.0.0.0:8080',
    httpPath: '/messages',
    httpToken: '',
    delimitedDelimiter: '',
    delimitedQuote: '',
    delimitedComment: '',
    delimitedHasHeader: false,
    delimitedColumns: '',
    delimitedTrimSpace: false,
    delimitedRelaxed: false,
    delimitedKeepBlankLines: false,
    dicomAllowedCallingAe: '',
    dicomSopClasses: '',
    dicomTransferSyntaxes: '',
    dicomMaxObjectBytes: '',
    httpTlsEnabled: false,
    httpTlsCertFile: '',
    httpTlsKeyFile: '',
    httpTlsCAFile: '',
    httpTlsRequireClientCert: false,
    httpReadTimeout: '',
    httpMaxMessageSize: '',

    dbDriver: 'postgres',
    dbDSN: '',
    dbQuery: '',
    dbColumn: '',
    dbTemplate: '',
    dbQueryTimeout: '',
    dbMaxAttempts: '',
    dbKeyColumn: '',
    dbAfterQuery: '',
    dbPollSeconds: 10,

    sftpHost: '',
    sftpUser: '',
    sftpPassword: '',
    sftpKeyFile: '',
    sftpKnownHosts: '',
    sftpDir: '',
    sftpPattern: '',
    sftpMoveTo: '',
    sftpPollSeconds: 30,
    // Ten seconds of no change before a file is read, which stops a half-finished upload
    // being parsed as a truncated message.
    sftpStableSeconds: 10,

    // Collecting files. The defaults are the cautious ones.
    fileRoot: '',
    fileDir: '.',
    // Not "*". A pattern that matches everything reads any temporary file a sending system is writing under its final
    // name, and anything unrelated that lands in the directory.
    filePattern: '*.hl7',
    filePollSeconds: 30,
    // Never zero. A file written by a slow producer pauses between writes, and half an HL7 message usually still parses.
    fileStableSeconds: 5,
    // Move rather than delete. A first configuration that deletes destroys somebody's files while they are still working
    // out whether the channel is right, and the file is the only copy.
    fileAfterRead: 'move',
    fileMoveTo: 'processed',
    fileErrorDir: 'errors',
    fileRaw: false,
    fileFramed: false,
    // Set rather than unlimited, so the first poll after an outage does not read forty thousand files in one pass and
    // make the channel look hung.
    fileBatchSize: 200,
    fileSortBy: 'name',
    fileFollowSymlinks: false,

    brokerAddr: '',
    brokerDestination: '',
    brokerLogin: '',
    brokerPasscode: '',
    brokerSelector: '',
    brokerSubscriptionID: '',
    brokerHeartbeat: '',
    brokerReconnect: '',

    ftpSrcHost: '',
    ftpSrcUser: '',
    ftpSrcPassword: '',
    // FTPS, not FTP. The same protocol with TLS, which most surviving servers support.
    ftpSrcSecurity: 'explicit',

    smbHost: '',
    smbShare: '',
    smbUser: '',
    smbPassword: '',
    smbDomain: '',

    webdavUrl: '',
    webdavUser: '',
    webdavPassword: '',

    tcpListen: '0.0.0.0:6000',
    // Deliberately empty. There is no safe default: the wrong framing produces plausible-looking messages rather than an
    // error, so the form has to ask.
    framingMode: '',
    framingDelimiter: '',
    framingStartBlock: '',
    framingKeepDelimiter: false,
    // True, matching the server. A default that disagrees with the server's would make a freshly built channel behave differently
    // from the same channel loaded back.
    framingTrimPadding: true,
    framingRecordLength: 0,
    framingLengthBytes: 4,
    framingBigEndian: true,
    framingLengthIncludesHeader: false,
    sourceReply: 'none',
    sourceReplyText: '',

    serialPort: '',
    // Zero, meaning unset. A guessed baud rate would appear to work while delivering nonsense.
    serialBaud: 0,
    serialDataBits: 8,
    serialParity: 'none',
    serialStopBits: '1',
    serialFlowControl: 'none',
    // An hour. Long enough not to fire on a quiet afternoon, short enough that a cable unplugged in the morning is
    // noticed before the afternoon clinic.
    serialQuietSeconds: 3600,
    serialReopenAfter: '',

    rules: [],
    steps: [],

    scriptLanguage: 'javascript',
    scriptAllowFile: false,
    scriptAllowDatabase: false,
    scriptAllowRoute: false,
    scriptFileRoots: '',
    filterScript: '',
    preprocessorScript: '',
    postprocessorScript: '',
    deployScript: '',
    undeployScript: '',
    transformerScript: '',

    destinations: [newDestination()],
  }
}

/** quote wraps a value for the filter language, escaping embedded quotes. */
function quote(value: string): string {
  return `"${value.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
}

/** ruleToExpression renders one rule. Returns "" when it is not filled in yet. */
export function ruleToExpression(rule: Rule): string {
  const path = rule.path.trim()
  if (!path) return ''

  switch (rule.operator) {
    case 'exists':
      return `${path} exists`
    case 'empty':
      return `${path} empty`
    case 'in': {
      const values = rule.values.map((v) => v.trim()).filter(Boolean)
      if (values.length === 0) return ''
      return `${path} in [${values.map(quote).join(', ')}]`
    }
    case 'matches': {
      if (!rule.value.trim()) return ''
      return `${path} matches ${quote(rule.value)}`
    }
    case '>':
    case '<':
    case '>=':
    case '<=': {
      const n = rule.value.trim()
      if (!n) return ''
      // Numeric comparisons are emitted unquoted, because the server treats a
      // quoted non-number on the right as a configuration error.
      return `${path} ${rule.operator} ${n}`
    }
    default:
      if (!rule.value.trim()) return ''
      return `${path} ${rule.operator} ${quote(rule.value)}`
  }
}

/** rulesToExpression joins the rules into one filter expression. */
export function rulesToExpression(rules: Rule[]): string {
  const parts: string[] = []
  rules.forEach((rule) => {
    const expr = ruleToExpression(rule)
    if (!expr) return
    if (parts.length === 0) {
      parts.push(expr)
    } else {
      parts.push(`${rule.join} ${expr}`)
    }
  })
  return parts.join(' ')
}



// draftToYaml used to live here, rendering the draft as YAML in the browser with a
// hand-written quoting helper. The server now marshals the file with the same library that
// reads it, so this was dead - and a second, divergent YAML generator sitting in the tree is
// worse than none, because the next person to change the format would find two places that
// look authoritative and update one.

/**
 * Common HL7 paths offered in the builder, so somebody who does not know the
 * standard can still write a useful filter. The list is short on purpose: these
 * are the fields real ADT and ORU filters actually use.
 */
export const commonPaths: { path: string; label: string }[] = [
  { path: 'MSH-9.1', label: 'Message type (ADT, ORU, ORM)' },
  { path: 'MSH-9.2', label: 'Trigger event (A01, A08, R01)' },
  { path: 'MSH-3', label: 'Sending application' },
  { path: 'MSH-4', label: 'Sending facility' },
  { path: 'MSH-5', label: 'Receiving application' },
  { path: 'MSH-6', label: 'Receiving facility' },
  { path: 'MSH-11', label: 'Processing ID (P, T, D)' },
  { path: 'MSH-12', label: 'HL7 version' },
  { path: 'PID-3.1', label: 'Patient identifier (MRN)' },
  { path: 'PID-3.4', label: 'Assigning authority' },
  { path: 'PID-5.1', label: 'Patient family name' },
  { path: 'PID-8', label: 'Administrative sex' },
  { path: 'PV1-2', label: 'Patient class (I, O, E)' },
  { path: 'PV1-3.1', label: 'Point of care / unit' },
  { path: 'PV1-19', label: 'Visit number' },
  { path: 'OBR-4.1', label: 'Universal service identifier' },
  { path: 'OBX-3.1', label: 'Observation identifier' },
  { path: 'OBX-5', label: 'Observation value' },
  { path: 'OBX-8', label: 'Abnormal flags' },
  { path: 'OBX-11', label: 'Observation result status' },
]

/** Trigger events offered as suggestions for MSH-9.2. */
export const commonTriggerEvents: { code: string; label: string }[] = [
  { code: 'A01', label: 'Admit' },
  { code: 'A02', label: 'Transfer' },
  { code: 'A03', label: 'Discharge' },
  { code: 'A04', label: 'Register outpatient' },
  { code: 'A05', label: 'Pre-admit' },
  { code: 'A06', label: 'Change to inpatient' },
  { code: 'A07', label: 'Change to outpatient' },
  { code: 'A08', label: 'Update patient information' },
  { code: 'A11', label: 'Cancel admit' },
  { code: 'A13', label: 'Cancel discharge' },
  { code: 'A28', label: 'Add person information' },
  { code: 'A31', label: 'Update person information' },
  { code: 'R01', label: 'Observation result (ORU)' },
]

/** Splits a textarea into a list, dropping blank lines.
 *
 *  Blank lines are dropped rather than sent, because an empty name is refused by the server and a
 *  trailing newline in a textarea is not something anybody means. */
function linesOrNone(value: string): string[] | undefined {
  const out = value
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '')

  return out.length > 0 ? out : undefined
}
