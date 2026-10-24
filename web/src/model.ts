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
  | 'kafka'
  | 's3'
  | 'sqs'
  | 'sns'
  | 'amqp'
  | 'azure_blob'
  | 'mqtt'
  | 'ibm_mq'
  | 'pubsub'
  | 'gcs'
  | 'ftp'
  | 'document'
  | 'soap'
  | 'dicom'
  | 'dicomweb'
  | 'xds'
  | 'direct'
  | 'as2'
  | 'mpi'
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
  dicomweb: 'An imaging archive or cloud service (DICOMweb STOW-RS)',
  xds: 'A document repository (IHE XDS.b ITI-41)',
  direct: 'A Direct address, through a HISP (Direct secure messaging)',
  as2: 'A trading partner over AS2 (EDI, signed and encrypted, with a receipt)',
  mpi: 'The patient index (register each patient, follow merges)',
  javascript: 'A script you write',
  broker: 'A message broker (STOMP)',
  kafka: 'A Kafka topic',
  sqs: 'An Amazon SQS queue',
  sns: 'An Amazon SNS topic',
  amqp: 'An AMQP 1.0 queue (Azure Service Bus, RabbitMQ)',
  'azure_blob': 'Azure Blob Storage',
  mqtt: 'An MQTT topic',
  'ibm_mq': 'An IBM MQ queue',
  pubsub: 'A Google Cloud Pub/Sub topic',
  gcs: 'Google Cloud Storage',
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
  xds:
    "Publishes each clinical document to an IHE XDS.b repository with ITI-41 Provide and Register: a CDA document, or one carried in an HL7 v2 message (MDM, ORU). The metadata is read from the document - patient, title, type, time, author, confidentiality - and completed from what you set here. The repository's answer decides: an XDS error is a failed delivery with its error code.",
  mpi:
    "Registers each message's patient in this server's patient index: the PID of an HL7 v2 message, or a FHIR Patient on a raw channel. The index links records from different systems that are the same person, queues the ones it is not sure about for review, and follows A40 merges. It needs the server started with the patient index on.",
  as2:
    "Posts each message to a trading partner over AS2 (RFC 4130): signed with our certificate, encrypted to theirs. With a synchronous receipt, the default, a message counts as delivered only when the partner's signed receipt says processed and names the digest of what was sent; anything else is a failed delivery and is retried. Tested against pyas2lib.",
  direct:
    "Sends each message as Direct secure messaging: signed with this address's certificate, encrypted to each recipient's, and handed to your HISP by SMTP with STARTTLS. Recipient certificates come from the Direct address book (the Direct screen) or DNS; a recipient with no trusted certificate is a failed delivery, never plain e-mail. A CDA document can go as an IHE XDM package, which receiving EHRs import with its metadata.",
  dicomweb:
    "Stores each imaging object with STOW-RS: Orthanc, dcm4chee, Google Cloud Healthcare, Azure Health Data Services, AWS HealthImaging, or this server's own archive. The server's answer for each object decides, so an object it lists as failed is a failed delivery, not a delivered one. Give the service root; /studies is added.",
  javascript:
    'Runs your script instead of sending anywhere. This is the escape hatch: when nothing else here fits, do the work yourself with the message in hand. Returning nothing counts as success, and returning a string fails the delivery with that string as the reason.',
  broker:
    'Publishes over STOMP, which is what crosses the wire for JMS brokers. Persistent by default, so a message survives the broker restarting.',
  kafka:
    'Publishes to a Kafka topic. Give it a key - PID-3.1 keys by patient - because Kafka keeps records in order only within a partition, and records with the same key always share one. Without a key there is no guarantee: records may stay together for a while and then move, so a discharge can be read before its admission intermittently, under load.',
  sqs: 'Sends each message to an SQS queue. On a FIFO queue (a URL ending .fifo) messages are grouped by patient, so one patient\'s events stay in order while different patients go in parallel, and an engine retry is not delivered twice. SQS refuses a message over 256 KB.',
  amqp: 'Sends over AMQP 1.0, the protocol of Azure Service Bus and Event Hubs, RabbitMQ 4, ActiveMQ and Artemis. A delivery counts only once the broker accepts the message. For Service Bus: the namespace on port 5671 with TLS, the shared access policy name and key, and the queue name.',
  'azure_blob':
    'Writes each message as a blob, in the access tier you choose. Archive is the cheapest to keep and is offline: reading a blob back means rehydrating it, which takes hours. Prefer a SAS token limited to the container over the account key.',
  mqtt: 'Publishes to an MQTT topic: Mosquitto, EMQX, HiveMQ, AWS IoT Core, Azure Event Grid. At QoS 1 (the default) a delivery counts only once the broker has the message. Use port 8883 with TLS; AWS IoT Core also wants a client certificate.',
  'ibm_mq':
    'Puts each message on an IBM MQ queue through the queue manager\'s messaging REST API (mqweb, MQ 9.1.5 and later), so no MQ client needs installing. The user needs the MQWebUser role and put authority on the queue. Messages are persistent unless you turn that off.',
  pubsub:
    'Publishes to a Google Cloud Pub/Sub topic. Order by patient to keep one patient\'s messages in order at a subscription with message ordering on. Without a credentials file, the service account of the Google Cloud machine Perfuse runs on is used.',
  gcs: 'Writes each message as an object in a Cloud Storage bucket, in the storage class you choose. Archive is the cheapest to keep; objects there are billed for at least a year.',
  sns: 'Publishes each message to an SNS topic, for fanning out to several subscribers - queues, Lambda functions, email. Email and SMS subscribers receive patient data in the clear, so subscribe queues rather than people.',
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

  /** fhirBundleScript is JavaScript run on each converted Bundle (as `bundle`, edited in place) before it is validated and
   * sent: how a site adds what its own systems know and the v2 message does not say. */
  fhirBundleScript: string

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

  /** A Kafka destination. Named apart from the source's fields because a channel can read from
   * one cluster and publish to another. */
  destKafkaBrokers: string
  destKafkaTopic: string
  /**
   * destKafkaKey decides the partition, and therefore decides ordering.
   *
   * Kafka keeps records in order only within a partition, and records sharing a key share a
   * partition. PID-3.1 keys by patient, which keeps one patient's events in sequence while
   * letting different patients proceed in parallel.
   *
   * Without a key there is no guarantee. Measured against a real broker, unkeyed records stayed
   * in one partition and then moved between batches - so ordering holds by accident and fails at
   * a boundary nobody can see, which is worse than failing consistently.
   */
  destKafkaKey: string
  /** destKafkaAcks is all, leader or none. All is the only one that survives a broker failing mid-write. */
  destKafkaAcks: 'all' | 'leader' | 'none'
  destKafkaCompression: 'none' | 'gzip' | 'snappy' | 'lz4' | 'zstd'
  destKafkaSaslMechanism: '' | 'plain' | 'scram-sha-256' | 'scram-sha-512'
  destKafkaSaslUsername: string
  destKafkaSaslPassword: string

  /** smtpStartTLS encrypts the connection. True by default, and sent only when turned off. */
  smtpStartTLS: boolean

  /** soapUrl and the rest describe a SOAP call. */
  soapUrl: string
  /** soapProfile 'cdc-iis' builds the CDC immunization registry envelope and judges the registry's HL7 ACK. */
  soapProfile: '' | 'cdc-iis'
  /** soapFacilityId is the registry's facilityID for the submitter (cdc-iis). */
  soapFacilityId: string
  /** soapWarningsAreErrors treats an AE carrying only warnings as a failure (cdc-iis). */
  soapWarningsAreErrors: boolean
  soapAction: string
  soapVersion: '1.1' | '1.2'
  soapBody: string
  soapHeader: string
  soapUsername: string
  soapPassword: string
  soapFaultIsSuccess: string

  /** A patient index destination: assigning authority namespaces to systems ("HOSP=urn:oid:1.2.3", one per line). */
  mpiAuthorities: string
  mpiIdType: string
  mpiSsnSystem: string
  mpiNoMerge: boolean

  /** An AS2 destination: a trading partner's endpoint, our identity and theirs. */
  as2Url: string
  as2From: string
  as2To: string
  as2CertFile: string
  as2KeyFile: string
  as2PartnerCertFile: string
  as2Unsigned: boolean
  as2Unencrypted: boolean
  as2Compress: boolean
  as2Receipt: 'sync' | 'async' | 'none'
  as2UnsignedReceipt: boolean
  as2ReceiptUrl: string
  as2ContentType: string
  as2FileName: string
  as2Subject: string
  as2Username: string
  as2Password: string

  /** directAddress and the rest describe a Direct destination. */
  directAddress: string
  directCertFile: string
  directKeyFile: string
  directAnchorsFile: string
  directTo: string
  directSubject: string
  directText: string
  directAttachName: string
  directXdm: boolean
  directXdmSourceId: string
  directRecipientCertsDir: string
  directDns: string
  /** directLdap is the directory a recipient's certificate is looked up in after DNS; empty finds it by SRV, off never asks. */
  directLdap: string
  directSmtpHost: string
  directUsername: string
  directPassword: string
  directRequestMdn: boolean
  /** xdsUrl and the rest describe an XDS.b (ITI-41) destination. */
  xdsUrl: string
  xdsSourceId: string
  xdsPatientDomain: string
  xdsFacilityCode: string
  xdsFacilityScheme: string
  xdsFacilityDisplay: string
  xdsPracticeCode: string
  xdsPracticeScheme: string
  xdsPracticeDisplay: string
  xdsClassCode: string
  xdsClassScheme: string
  xdsToken: string
  /** xdsDirectFrom and xdsDirectTo make the submission XDR as Direct's transport. */
  xdsDirectFrom: string
  xdsDirectTo: string
  /** dicomwebUrl and the rest describe a DICOMweb (STOW-RS) destination. */
  dicomwebUrl: string
  dicomwebToken: string
  dicomwebUsername: string
  dicomwebPassword: string
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
  /** elrConfig turns the destination into lab reporting to public health: the ELR file (OIDs, gaps the lab leaves). */
  elrConfig: string
  /** elrRctc is the trigger codes file; empty uses the built-in sample. */
  elrRctc: string
  /** OpenPGP on what the destination delivers: encrypted to the receivers' keys, signed with ours. */
  pgpEnabled: boolean
  /** pgpEncryptTo is the receivers' public key files, comma or space separated. */
  pgpEncryptTo: string
  pgpSignWith: string
  pgpPassphrase: string
  pgpArmor: boolean
  pgpNoCompress: boolean
  pgpFileName: string
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
  /** s3StorageClass sends objects straight to a storage class, Glacier included. Empty is STANDARD. */
  s3StorageClass: string
  /** s3Format is hl7, or ndjson for an archive Amazon Athena can query. */
  s3Format: '' | 'ndjson'

  // SQS and SNS. Their region, keys and endpoint are the s3* fields above: a destination is one type at a time, and the three share
  // exactly the same signing settings, so one set of fields and one piece of form serves all of them.
  sqsQueueUrl: string
  snsTopicArn: string
  snsSubject: string
  /** awsGroupBy is the FIFO group: patient (the default), channel, or a message path such as MSH-4. */
  awsGroupBy: string

  /** fhirAuthType signs or authorises requests to a hosted FHIR service. AWS uses the shared s3* key fields. */
  fhirAuthType: '' | 'aws' | 'azure' | 'client_credentials'
  fhirAuthTenantId: string
  fhirAuthTokenUrl: string
  fhirAuthClientId: string
  fhirAuthClientSecret: string
  fhirAuthScope: string
  /** fhirEcr makes the destination public health case reporting: eICRs for reportable messages, to the URL's $process-message. */
  fhirEcr: boolean
  fhirEcrRctc: string
  fhirEcrSource: string
  fhirEcrFacilityName: string
  fhirEcrFacilityNpi: string
  fhirEcrFacilityPhone: string
  fhirEcrFacilityLine: string
  fhirEcrFacilityCity: string
  fhirEcrFacilityState: string
  fhirEcrFacilityPostalCode: string

  // AMQP 1.0.
  destAmqpAddr: string
  destAmqpAddress: string
  destAmqpUsername: string
  destAmqpPassword: string
  destAmqpTls: boolean

  // Azure Blob Storage.
  azAccount: string
  azContainer: string
  azKey: string
  azSas: string
  azEndpoint: string
  azBlob: string
  azTier: '' | 'Hot' | 'Cool' | 'Cold' | 'Archive'

  /** destTlsCaFile, destTlsCertFile and destTlsKeyFile are the TLS files of an amqp, mqtt or ibm_mq destination. */
  destTlsCaFile: string
  destTlsCertFile: string
  destTlsKeyFile: string

  // MQTT.
  destMqttAddr: string
  destMqttTopic: string
  destMqttClientId: string
  destMqttUsername: string
  destMqttPassword: string
  destMqttTls: boolean
  destMqttQos: '0' | '1'
  destMqttRetain: boolean

  // IBM MQ.
  destIbmUrl: string
  destIbmQmgr: string
  destIbmQueue: string
  destIbmUsername: string
  destIbmPassword: string
  destIbmApiVersion: '' | 'v1' | 'v2' | 'v3'
  destIbmNonPersistent: boolean

  // Google Cloud.
  destPsProject: string
  destPsTopic: string
  destPsCredentialsFile: string
  destPsEndpoint: string
  destPsOrderBy: string
  destGcsBucket: string
  destGcsCredentialsFile: string
  destGcsEndpoint: string
  destGcsObject: string
  destGcsStorageClass: '' | 'STANDARD' | 'NEARLINE' | 'COLDLINE' | 'ARCHIVE'

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
  | 'dicomweb'
  | 'direct'
  | 'as2'
  | 'javascript'
  | 'broker'
  | 'kafka'
  | 'sqs'
  | 's3'
  | 'amqp'
  | 'azure_blob'
  | 'email'
  | 'mqtt'
  | 'ibm_mq'
  | 'pubsub'
  | 'gcs'
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

/** A trading partner an AS2 source accepts. */
export interface As2PartnerDraft {
  id: string
  certFile: string
  allowUnsigned: boolean
  allowUnencrypted: boolean
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
  dataType: 'hl7' | 'x12' | 'edifact' | 'hl7v3' | 'ncpdp' | 'script' | 'delimited' | 'dicom' | 'raw'
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

  /** An EDIFACT channel: envelope checking, one interchange per message, and a CONTRL back. */
  edifactEnvelope: 'require' | 'warn' | 'ignore'
  edifactSplit: boolean
  edifactAcknowledge: 'none' | 'contrl'

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
  /** soapSourceProfile 'cdc-iis' answers as an immunization registry on the CDC interface. */
  soapSourceProfile: '' | 'cdc-iis'
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

  /** directSrc fields describe receiving Direct mail from a HISP. */
  directSrcAddress: string
  /** An AS2 source: where partners post, our AS2 identity and key, and who may send. */
  as2SrcListen: string
  as2SrcPath: string
  as2SrcId: string
  as2SrcCertFile: string
  as2SrcKeyFile: string
  as2SrcPartners: As2PartnerDraft[]
  as2SrcTlsEnabled: boolean
  as2SrcTlsCertFile: string
  as2SrcTlsKeyFile: string
  as2SrcMaxMessageSize: string
  as2SrcDuplicateWindow: string
  directSrcCertFile: string
  directSrcKeyFile: string
  directSrcAnchorsFile: string
  directSrcListen: string
  directSrcTlsCert: string
  directSrcTlsKey: string
  directSrcMaxBytes: number
  directSrcSmtpHost: string
  directSrcUsername: string
  directSrcPassword: string
  /** directSrcImap* is the address's mailbox at the HISP, read instead of (or as well as) SMTP deliveries. */
  directSrcImapHost: string
  directSrcImapPort: number
  directSrcImapSecurity: string
  directSrcImapUsername: string
  directSrcImapPassword: string
  directSrcImapMailbox: string
  directSrcImapAfterRead: string
  directSrcImapMoveTo: string
  directSrcImapErrorFolder: string
  directSrcImapPollInterval: string
  /** directSrcImapCaFile and directSrcImapServerName trust a HISP whose mail server has a private certificate. */
  directSrcImapCaFile: string
  directSrcImapServerName: string
  /** directSrcXdr* is the HTTPS endpoint XDR is taken at. */
  directSrcXdrListen: string
  directSrcXdrTlsCert: string
  directSrcXdrTlsKey: string
  directSrcXdrClientCaFile: string
  /** dicomwebListen and the rest describe a STOW-RS endpoint. */
  dicomwebListen: string
  dicomwebPath: string
  dicomwebToken: string
  dicomwebUsername: string
  dicomwebPassword: string
  dicomwebTls: boolean
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

  // A Kafka topic. Separate from the broker fields above because Kafka's model differs where it
  // matters: a topic is partitioned, ordering holds only within a partition, and the consumer
  // tracks its own position rather than the broker tracking it.

  /** kafkaBrokers is the bootstrap list, comma separated. One address is a single point of failure for starting up. */
  kafkaBrokers: string
  /** kafkaTopics is the topics to read, comma separated. */
  kafkaTopics: string
  /**
   * kafkaGroup is the consumer group, and it is required.
   *
   * It is what remembers how far this channel has read. A generated one would restart from
   * scratch on every restart and leave the old group holding offsets nobody reads.
   */
  kafkaGroup: string
  /**
   * kafkaFromBeginning replays the topic from its start when the group is new.
   *
   * Off by default so a channel pointed at a topic with two years of history does not replay two
   * years of patient events into a live system on the day it is switched on.
   */
  kafkaFromBeginning: boolean
  /**
   * kafkaCommitAfterDelivery commits the position only once a message has been handled.
   *
   * On by default, and it is the difference between losing a message and seeing it twice.
   * Committing on read means a crash loses the message with nothing recording it existed.
   */
  kafkaCommitAfterDelivery: boolean
  /** kafkaSessionTimeout is how long before the coordinator reassigns this consumer's partitions. */
  kafkaSessionTimeout: string
  /** kafkaSaslMechanism is plain, scram-sha-256 or scram-sha-512. Empty means no authentication. */
  kafkaSaslMechanism: '' | 'plain' | 'scram-sha-256' | 'scram-sha-512'
  kafkaSaslUsername: string
  kafkaSaslPassword: string

  // AWS sources. One set of access fields serves both, as on the destination side.
  awsSrcRegion: string
  awsSrcAccessKeyId: string
  awsSrcSecretAccessKey: string
  awsSrcSessionToken: string
  awsSrcEndpoint: string
  sqsSrcQueueUrl: string
  /** sqsSrcWaitSeconds is the long poll, 1 to 20; empty is 20. */
  sqsSrcWaitSeconds: string
  sqsSrcMaxMessages: string
  /** sqsSrcVisibility is how long a message stays hidden while it is handled, e.g. 60s. Empty uses the queue's setting. */
  sqsSrcVisibility: string
  s3SrcBucket: string
  s3SrcPrefix: string
  s3SrcSuffix: string
  s3SrcAfterRead: 'move' | 'delete'
  s3SrcMoveTo: string
  s3SrcErrorPrefix: string
  s3SrcPollSeconds: number
  s3SrcPathStyle: boolean
  s3SrcFramed: boolean
  s3SrcMaxObjectSize: string

  amqpAddr: string
  amqpAddress: string
  amqpUsername: string
  amqpPassword: string
  amqpTls: boolean
  amqpPrefetch: string

  azSrcAccount: string
  azSrcContainer: string
  azSrcKey: string
  azSrcSas: string
  azSrcEndpoint: string
  azSrcPrefix: string
  azSrcSuffix: string
  azSrcAfterRead: 'move' | 'delete'
  azSrcMoveTo: string
  azSrcErrorPrefix: string
  azSrcPollSeconds: number
  azSrcFramed: boolean

  /** OpenPGP on what the source receives: decrypted with our key, the sender's signature checked. */
  srcPgpEnabled: boolean
  srcPgpDecryptWith: string
  srcPgpPassphrase: string
  /** srcPgpVerifyWith is the senders' public key files, comma or space separated. */
  srcPgpVerifyWith: string
  srcPgpRequireSignature: boolean
  srcPgpAllowUnencrypted: boolean
  srcPgpPassPlain: boolean

  /** The email source: a mailbox read over IMAP or POP3. */
  mailProtocol: 'imap' | 'pop3'
  mailHost: string
  mailPort: string
  mailSecurity: 'tls' | 'starttls' | 'none'
  mailUsername: string
  mailPassword: string
  mailAuth: 'password' | 'oauth'
  mailOAuthProvider: 'microsoft' | 'google' | 'custom'
  mailOAuthTenant: string
  mailOAuthTokenUrl: string
  mailOAuthClientId: string
  mailOAuthClientSecret: string
  mailOAuthRefreshToken: string
  mailMailbox: string
  mailTake: 'attachments' | 'body' | 'raw'
  mailAttachmentNames: string
  mailAllowedSenders: string
  mailAfterRead: 'move' | 'flag' | 'delete'
  mailMoveTo: string
  mailErrorFolder: string
  mailPollSeconds: number

  /** connTlsCaFile, connTlsCertFile and connTlsKeyFile are the TLS files of an amqp, mqtt or ibm_mq source. */
  connTlsCaFile: string
  connTlsCertFile: string
  connTlsKeyFile: string

  mqttSrcAddr: string
  mqttSrcTopic: string
  mqttSrcClientId: string
  mqttSrcUsername: string
  mqttSrcPassword: string
  mqttSrcTls: boolean
  mqttSrcQos: '0' | '1'

  ibmSrcUrl: string
  ibmSrcQmgr: string
  ibmSrcQueue: string
  ibmSrcUsername: string
  ibmSrcPassword: string
  ibmSrcApiVersion: '' | 'v1' | 'v2' | 'v3'
  ibmSrcPollSeconds: number

  psSrcProject: string
  psSrcSubscription: string
  psSrcCredentialsFile: string
  psSrcEndpoint: string
  psSrcMaxMessages: string

  gcsSrcBucket: string
  gcsSrcCredentialsFile: string
  gcsSrcEndpoint: string
  gcsSrcPrefix: string
  gcsSrcSuffix: string
  gcsSrcAfterRead: 'move' | 'delete'
  gcsSrcMoveTo: string
  gcsSrcErrorPrefix: string
  gcsSrcPollSeconds: number
  gcsSrcFramed: boolean

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
/**
 * clientTls is the TLS block of a connector that connects out: on or off, and the files a private CA or a client certificate
 * needs. Without the CA file a broker or queue manager with an internal certificate could not be reached from the form at all.
 */
function clientTls(on: boolean, ca: string, cert: string, key: string) {
  if (!on) return undefined
  return { enabled: true, caFile: ca || undefined, certFile: cert || undefined, keyFile: key || undefined }
}

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

/** The sources whose messages can arrive as OpenPGP files or bodies. */
export const pgpSourceKinds: ReadonlySet<SourceKind> = new Set<SourceKind>([
  'file', 'sftp', 'ftp', 'smb', 'webdav', 's3', 'azure_blob', 'gcs', 'email', 'http', 'soap', 'sqs', 'kafka', 'amqp',
  'broker', 'mqtt', 'ibm_mq', 'pubsub',
])

/** pgpKeyList splits a list of key files written with commas, spaces or new lines. */
export function pgpKeyList(v: string): string[] {
  return v
    .split(/[,\s]+/)
    .map((x) => x.trim())
    .filter(Boolean)
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
    case 'amqp':
      source.amqp = {
        addr: draft.amqpAddr || undefined,
        address: draft.amqpAddress || undefined,
        username: draft.amqpUsername || undefined,
        password: draft.amqpPassword || undefined,
        tls: clientTls(draft.amqpTls, draft.connTlsCaFile, draft.connTlsCertFile, draft.connTlsKeyFile),
        prefetch: positiveOrNone(draft.amqpPrefetch),
      }
      break
    case 'mqtt':
      source.mqtt = {
        addr: draft.mqttSrcAddr || undefined,
        topic: draft.mqttSrcTopic || undefined,
        clientId: draft.mqttSrcClientId || undefined,
        username: draft.mqttSrcUsername || undefined,
        password: draft.mqttSrcPassword || undefined,
        tls: clientTls(draft.mqttSrcTls, draft.connTlsCaFile, draft.connTlsCertFile, draft.connTlsKeyFile),
        qos: draft.mqttSrcQos === '0' ? 0 : undefined,
      }
      break
    case 'ibm_mq':
      source.ibmMq = {
        url: draft.ibmSrcUrl || undefined,
        queueManager: draft.ibmSrcQmgr || undefined,
        queue: draft.ibmSrcQueue || undefined,
        username: draft.ibmSrcUsername || undefined,
        password: draft.ibmSrcPassword || undefined,
        tls: clientTls(draft.ibmSrcUrl.startsWith('https://'), draft.connTlsCaFile, draft.connTlsCertFile, draft.connTlsKeyFile),
        apiVersion: draft.ibmSrcApiVersion || undefined,
        pollInterval: secondsOrNone(draft.ibmSrcPollSeconds),
      }
      break
    case 'pubsub':
      source.pubsub = {
        project: draft.psSrcProject || undefined,
        subscription: draft.psSrcSubscription || undefined,
        credentialsFile: draft.psSrcCredentialsFile || undefined,
        endpoint: draft.psSrcEndpoint || undefined,
        maxMessages: positiveOrNone(draft.psSrcMaxMessages),
      }
      break
    case 'gcs':
      source.gcs = {
        bucket: draft.gcsSrcBucket || undefined,
        credentialsFile: draft.gcsSrcCredentialsFile || undefined,
        endpoint: draft.gcsSrcEndpoint || undefined,
        prefix: draft.gcsSrcPrefix || undefined,
        suffix: draft.gcsSrcSuffix || undefined,
        afterRead: draft.gcsSrcAfterRead === 'delete' ? 'delete' : undefined,
        moveTo: draft.gcsSrcAfterRead === 'move' ? draft.gcsSrcMoveTo || undefined : undefined,
        errorPrefix: draft.gcsSrcErrorPrefix || undefined,
        pollInterval: secondsOrNone(draft.gcsSrcPollSeconds),
        framed: draft.gcsSrcFramed || undefined,
      }
      break
    case 'email': {
      const list = (v: string) =>
        v
          .split(/[,\s]+/)
          .map((x) => x.trim())
          .filter(Boolean)
      const names = list(draft.mailAttachmentNames)
      const senders = list(draft.mailAllowedSenders)
      source.email = {
        protocol: draft.mailProtocol === 'pop3' ? 'pop3' : undefined,
        host: draft.mailHost || undefined,
        port: positiveOrNone(draft.mailPort),
        security: draft.mailSecurity === 'tls' ? undefined : draft.mailSecurity,
        username: draft.mailUsername || undefined,
        password: draft.mailAuth === 'password' ? draft.mailPassword || undefined : undefined,
        oauth:
          draft.mailAuth === 'oauth'
            ? {
                provider: draft.mailOAuthProvider,
                tenant: draft.mailOAuthProvider === 'microsoft' ? draft.mailOAuthTenant || undefined : undefined,
                tokenUrl: draft.mailOAuthProvider === 'custom' ? draft.mailOAuthTokenUrl || undefined : undefined,
                clientId: draft.mailOAuthClientId || undefined,
                clientSecret: draft.mailOAuthClientSecret || undefined,
                refreshToken: draft.mailOAuthRefreshToken || undefined,
              }
            : undefined,
        mailbox: draft.mailProtocol === 'imap' && draft.mailMailbox && draft.mailMailbox !== 'INBOX' ? draft.mailMailbox : undefined,
        take: draft.mailTake === 'attachments' ? undefined : draft.mailTake,
        attachmentNames: draft.mailTake === 'attachments' && names.length ? names : undefined,
        allowedSenders: senders.length ? senders : undefined,
        afterRead: draft.mailProtocol === 'pop3' ? undefined : draft.mailAfterRead === 'move' ? undefined : draft.mailAfterRead,
        moveTo: draft.mailProtocol === 'imap' && draft.mailAfterRead === 'move' ? draft.mailMoveTo || undefined : undefined,
        errorFolder: draft.mailProtocol === 'imap' ? draft.mailErrorFolder || undefined : undefined,
        pollInterval: draft.mailPollSeconds === 60 ? undefined : secondsOrNone(draft.mailPollSeconds),
      }
      break
    }
    case 'azure_blob':
      source.azureBlob = {
        account: draft.azSrcAccount || undefined,
        container: draft.azSrcContainer || undefined,
        key: draft.azSrcKey || undefined,
        sas: draft.azSrcSas || undefined,
        endpoint: draft.azSrcEndpoint || undefined,
        prefix: draft.azSrcPrefix || undefined,
        suffix: draft.azSrcSuffix || undefined,
        afterRead: draft.azSrcAfterRead === 'delete' ? 'delete' : undefined,
        moveTo: draft.azSrcAfterRead === 'move' ? draft.azSrcMoveTo || undefined : undefined,
        errorPrefix: draft.azSrcErrorPrefix || undefined,
        pollInterval: secondsOrNone(draft.azSrcPollSeconds),
        framed: draft.azSrcFramed || undefined,
      }
      break
    case 'sqs':
      source.sqs = {
        ...awsSrcAccess(draft),
        queueUrl: draft.sqsSrcQueueUrl || undefined,
        waitSeconds: positiveOrNone(draft.sqsSrcWaitSeconds),
        maxMessages: positiveOrNone(draft.sqsSrcMaxMessages),
        visibilityTimeout: draft.sqsSrcVisibility || undefined,
      }
      break
    case 's3':
      source.s3 = {
        ...awsSrcAccess(draft),
        bucket: draft.s3SrcBucket || undefined,
        pathStyle: draft.s3SrcPathStyle || undefined,
        prefix: draft.s3SrcPrefix || undefined,
        suffix: draft.s3SrcSuffix || undefined,
        // Move is the server's default, so only delete is written.
        afterRead: draft.s3SrcAfterRead === 'delete' ? 'delete' : undefined,
        moveTo: draft.s3SrcAfterRead === 'move' ? draft.s3SrcMoveTo || undefined : undefined,
        errorPrefix: draft.s3SrcErrorPrefix || undefined,
        pollInterval: secondsOrNone(draft.s3SrcPollSeconds),
        maxObjectSize: positiveOrNone(draft.s3SrcMaxObjectSize),
        framed: draft.s3SrcFramed || undefined,
      }
      break
    case 'kafka':
      source.kafka = {
        brokers: commaList(draft.kafkaBrokers),
        topics: commaList(draft.kafkaTopics),
        group: draft.kafkaGroup || undefined,
        fromBeginning: draft.kafkaFromBeginning || undefined,
        // Sent only when turned off, because true is the default on the server too. Sending
        // it always would write the safe value into every file and make the unsafe one look
        // like a deliberate edit rather than a deliberate risk.
        commitAfterDelivery: draft.kafkaCommitAfterDelivery ? undefined : false,
        sessionTimeout: draft.kafkaSessionTimeout || undefined,
        sasl: kafkaSasl(
          draft.kafkaSaslMechanism,
          draft.kafkaSaslUsername,
          draft.kafkaSaslPassword,
        ),
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
        profile: draft.soapSourceProfile || undefined,
        path: draft.soapSourcePath || undefined,
        // With the registry profile the server's default is 1.2, so 1.1 must be written to be kept.
        version:
          draft.soapSourceProfile === 'cdc-iis'
            ? draft.soapSourceVersion
            : draft.soapSourceVersion !== '1.1'
              ? draft.soapSourceVersion
              : undefined,
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
    case 'as2':
      source.as2 = {
        listen: draft.as2SrcListen || undefined,
        path: draft.as2SrcPath && draft.as2SrcPath !== '/as2' ? draft.as2SrcPath : undefined,
        id: draft.as2SrcId || undefined,
        certFile: draft.as2SrcCertFile || undefined,
        keyFile: draft.as2SrcKeyFile || undefined,
        tls: draft.as2SrcTlsEnabled
          ? { enabled: true, certFile: draft.as2SrcTlsCertFile || undefined, keyFile: draft.as2SrcTlsKeyFile || undefined }
          : undefined,
        maxMessageSize: Number(draft.as2SrcMaxMessageSize) > 0 ? Number(draft.as2SrcMaxMessageSize) : undefined,
        duplicateWindow: draft.as2SrcDuplicateWindow.trim() || undefined,
        partners: draft.as2SrcPartners
          .filter((p) => p.id.trim())
          .map((p) => ({
            id: p.id.trim(),
            certFile: p.certFile.trim() || undefined,
            allowUnsigned: p.allowUnsigned || undefined,
            allowUnencrypted: p.allowUnencrypted || undefined,
          })),
      }
      break
    case 'direct':
      source.direct = {
        address: draft.directSrcAddress || undefined,
        certFile: draft.directSrcCertFile || undefined,
        keyFile: draft.directSrcKeyFile || undefined,
        anchorsFile: draft.directSrcAnchorsFile || undefined,
        listen: draft.directSrcListen || undefined,
        tlsCert: draft.directSrcTlsCert || undefined,
        tlsKey: draft.directSrcTlsKey || undefined,
        maxBytes: draft.directSrcMaxBytes || undefined,
        smtpHost: draft.directSrcSmtpHost || undefined,
        username: draft.directSrcUsername || undefined,
        password: draft.directSrcPassword || undefined,
        imap: draft.directSrcImapHost
          ? {
              host: draft.directSrcImapHost,
              port: draft.directSrcImapPort || undefined,
              security: draft.directSrcImapSecurity || undefined,
              username: draft.directSrcImapUsername || undefined,
              password: draft.directSrcImapPassword || undefined,
              mailbox: draft.directSrcImapMailbox || undefined,
              afterRead: draft.directSrcImapAfterRead || undefined,
              moveTo: draft.directSrcImapMoveTo || undefined,
              errorFolder: draft.directSrcImapErrorFolder || undefined,
              pollInterval: draft.directSrcImapPollInterval || undefined,
              tls:
                draft.directSrcImapCaFile || draft.directSrcImapServerName
                  ? { caFile: draft.directSrcImapCaFile || undefined, serverName: draft.directSrcImapServerName || undefined }
                  : undefined,
            }
          : undefined,
        xdr: draft.directSrcXdrListen
          ? {
              listen: draft.directSrcXdrListen,
              tlsCert: draft.directSrcXdrTlsCert || undefined,
              tlsKey: draft.directSrcXdrTlsKey || undefined,
              clientCaFile: draft.directSrcXdrClientCaFile || undefined,
            }
          : undefined,
      }
      break
    case 'dicomweb':
      source.dicomweb = {
        listen: draft.dicomwebListen || undefined,
        path: draft.dicomwebPath || undefined,
        token: draft.dicomwebToken || undefined,
        username: draft.dicomwebUsername || undefined,
        password: draft.dicomwebPassword || undefined,
        tls: draft.dicomwebTls ? { enabled: true } : undefined,
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

  // PGP, like TLS, sits on the source itself.
  if (draft.srcPgpEnabled && pgpSourceKinds.has(draft.sourceKind)) {
    const verify = pgpKeyList(draft.srcPgpVerifyWith)
    source.pgp = {
      decryptWith: draft.srcPgpDecryptWith.trim() || undefined,
      passphrase: draft.srcPgpDecryptWith.trim() ? draft.srcPgpPassphrase || undefined : undefined,
      verifyWith: verify.length ? verify : undefined,
      requireSignature: draft.srcPgpRequireSignature || undefined,
      allowUnencrypted: draft.srcPgpAllowUnencrypted || undefined,
      passPlain: draft.srcPgpPassPlain || undefined,
    }
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
    edifact:
      draft.dataType === 'edifact'
        ? {
            envelope: draft.edifactEnvelope === 'require' ? undefined : draft.edifactEnvelope,
            split: draft.edifactSplit || undefined,
            acknowledge: draft.edifactAcknowledge === 'none' ? undefined : draft.edifactAcknowledge,
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
    case 'kafka':
      out.kafka = {
        brokers: commaList(d.destKafkaBrokers),
        topic: d.destKafkaTopic || undefined,
        key: d.destKafkaKey || undefined,
        // Sent only when it is not the default, because all-ISR is what the server uses
        // anyway and writing it into every file would make the weaker settings look
        // ordinary.
        acks: d.destKafkaAcks === 'all' ? undefined : d.destKafkaAcks,
        compression: d.destKafkaCompression === 'snappy' ? undefined : d.destKafkaCompression,
        sasl: kafkaSasl(
          d.destKafkaSaslMechanism,
          d.destKafkaSaslUsername,
          d.destKafkaSaslPassword,
        ),
      }
      break
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
        bundleScript: d.fhirBundleScript.trim() ? d.fhirBundleScript : undefined,
        auth:
          d.fhirAuthType === 'aws'
            ? { type: 'aws', ...awsDestAccess(d), endpoint: undefined }
            : d.fhirAuthType
              ? {
                  type: d.fhirAuthType,
                  tenantId: d.fhirAuthType === 'azure' ? d.fhirAuthTenantId || undefined : undefined,
                  tokenUrl: d.fhirAuthType === 'client_credentials' ? d.fhirAuthTokenUrl || undefined : undefined,
                  clientId: d.fhirAuthClientId || undefined,
                  clientSecret: d.fhirAuthClientSecret || undefined,
                  scope: d.fhirAuthScope || undefined,
                }
              : undefined,
        ecr: d.fhirEcr
          ? {
              rctc: d.fhirEcrRctc || undefined,
              source: d.fhirEcrSource || undefined,
              facility: {
                name: d.fhirEcrFacilityName || undefined,
                npi: d.fhirEcrFacilityNpi || undefined,
                phone: d.fhirEcrFacilityPhone || undefined,
                line: d.fhirEcrFacilityLine || undefined,
                city: d.fhirEcrFacilityCity || undefined,
                state: d.fhirEcrFacilityState || undefined,
                postalCode: d.fhirEcrFacilityPostalCode || undefined,
              },
            }
          : undefined,
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
    case 'mpi': {
      const authorities: Record<string, string> = {}
      for (const line of d.mpiAuthorities.split('\n')) {
        const [ns, ...rest] = line.split('=')
        const sys = rest.join('=').trim()
        if (ns && ns.trim() && sys) authorities[ns.trim()] = sys
      }
      out.mpi = {
        authorities: Object.keys(authorities).length ? authorities : undefined,
        idType: d.mpiIdType.trim() || undefined,
        ssnSystem: d.mpiSsnSystem.trim() || undefined,
        noMerge: d.mpiNoMerge || undefined,
      }
      break
    }
    case 'as2':
      out.as2 = {
        url: d.as2Url || undefined,
        from: d.as2From || undefined,
        to: d.as2To || undefined,
        certFile: d.as2Unsigned ? undefined : d.as2CertFile || undefined,
        keyFile: d.as2Unsigned ? undefined : d.as2KeyFile || undefined,
        partnerCertFile: d.as2PartnerCertFile || undefined,
        unsigned: d.as2Unsigned || undefined,
        unencrypted: d.as2Unencrypted || undefined,
        compress: d.as2Compress || undefined,
        receipt: d.as2Receipt === 'sync' ? undefined : d.as2Receipt,
        unsignedReceipt: d.as2Receipt !== 'none' && d.as2UnsignedReceipt ? true : undefined,
        receiptUrl: d.as2Receipt === 'async' ? d.as2ReceiptUrl || undefined : undefined,
        contentType: d.as2ContentType || undefined,
        fileName: d.as2FileName || undefined,
        subject: d.as2Subject || undefined,
        username: d.as2Username || undefined,
        password: d.as2Username ? d.as2Password || undefined : undefined,
      }
      break
    case 'direct':
      out.direct = {
        address: d.directAddress || undefined,
        certFile: d.directCertFile || undefined,
        keyFile: d.directKeyFile || undefined,
        anchorsFile: d.directAnchorsFile || undefined,
        to: splitList(d.directTo),
        subject: d.directSubject || undefined,
        text: d.directText || undefined,
        attachName: d.directAttachName || undefined,
        xdm: d.directXdm || undefined,
        xdmSourceId: d.directXdmSourceId || undefined,
        recipientCertsDir: d.directRecipientCertsDir || undefined,
        dns: d.directDns || undefined,
        ldap: d.directLdap || undefined,
        smtpHost: d.directSmtpHost || undefined,
        username: d.directUsername || undefined,
        password: d.directPassword || undefined,
        requestMdn: d.directRequestMdn || undefined,
      }
      break
    case 'xds':
      out.xds = {
        url: d.xdsUrl || undefined,
        sourceId: d.xdsSourceId || undefined,
        patientDomain: d.xdsPatientDomain || undefined,
        facilityType: { code: d.xdsFacilityCode || undefined, scheme: d.xdsFacilityScheme || undefined, display: d.xdsFacilityDisplay || undefined },
        practiceSetting: { code: d.xdsPracticeCode || undefined, scheme: d.xdsPracticeScheme || undefined, display: d.xdsPracticeDisplay || undefined },
        class: d.xdsClassCode ? { code: d.xdsClassCode, scheme: d.xdsClassScheme || undefined } : undefined,
        token: d.xdsToken || undefined,
        directFrom: d.xdsDirectFrom || undefined,
        directTo: splitList(d.xdsDirectTo),
      }
      break
    case 'dicomweb':
      out.dicomweb = {
        url: d.dicomwebUrl || undefined,
        token: d.dicomwebToken || undefined,
        username: d.dicomwebUsername || undefined,
        password: d.dicomwebPassword || undefined,
      }
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
      if (d.soapProfile === 'cdc-iis') {
        // The registry profile builds the envelope, the action and the success test itself, so the plain-SOAP
        // fields are not sent even if they were filled in before the profile was chosen.
        out.soap = {
          url: d.soapUrl || undefined,
          profile: 'cdc-iis',
          facilityId: d.soapFacilityId || undefined,
          warningsAreErrors: d.soapWarningsAreErrors || undefined,
          version: d.soapVersion,
          username: d.soapUsername || undefined,
          password: d.soapPassword || undefined,
        }
        break
      }
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
        storageClass: d.s3StorageClass || undefined,
        format: d.s3Format || undefined,
      }
      break
    case 'amqp':
      out.amqp = {
        addr: d.destAmqpAddr || undefined,
        address: d.destAmqpAddress || undefined,
        username: d.destAmqpUsername || undefined,
        password: d.destAmqpPassword || undefined,
        tls: clientTls(d.destAmqpTls, d.destTlsCaFile, d.destTlsCertFile, d.destTlsKeyFile),
      }
      break
    case 'mqtt':
      out.mqtt = {
        addr: d.destMqttAddr || undefined,
        topic: d.destMqttTopic || undefined,
        clientId: d.destMqttClientId || undefined,
        username: d.destMqttUsername || undefined,
        password: d.destMqttPassword || undefined,
        tls: clientTls(d.destMqttTls, d.destTlsCaFile, d.destTlsCertFile, d.destTlsKeyFile),
        qos: d.destMqttQos === '0' ? 0 : undefined,
        retain: d.destMqttRetain || undefined,
      }
      break
    case 'ibm_mq':
      out.ibmMq = {
        url: d.destIbmUrl || undefined,
        queueManager: d.destIbmQmgr || undefined,
        queue: d.destIbmQueue || undefined,
        username: d.destIbmUsername || undefined,
        password: d.destIbmPassword || undefined,
        tls: clientTls(d.destIbmUrl.startsWith('https://'), d.destTlsCaFile, d.destTlsCertFile, d.destTlsKeyFile),
        apiVersion: d.destIbmApiVersion || undefined,
        nonPersistent: d.destIbmNonPersistent || undefined,
      }
      break
    case 'pubsub':
      out.pubsub = {
        project: d.destPsProject || undefined,
        topic: d.destPsTopic || undefined,
        credentialsFile: d.destPsCredentialsFile || undefined,
        endpoint: d.destPsEndpoint || undefined,
        orderBy: d.destPsOrderBy || undefined,
      }
      break
    case 'gcs':
      out.gcs = {
        bucket: d.destGcsBucket || undefined,
        credentialsFile: d.destGcsCredentialsFile || undefined,
        endpoint: d.destGcsEndpoint || undefined,
        object: d.destGcsObject || undefined,
        storageClass: d.destGcsStorageClass || undefined,
      }
      break
    case 'azure_blob':
      out.azureBlob = {
        account: d.azAccount || undefined,
        container: d.azContainer || undefined,
        key: d.azKey || undefined,
        sas: d.azSas || undefined,
        endpoint: d.azEndpoint || undefined,
        blob: d.azBlob || undefined,
        tier: d.azTier || undefined,
      }
      break
    case 'sqs':
      out.sqs = {
        ...awsDestAccess(d),
        queueUrl: d.sqsQueueUrl || undefined,
        groupBy: d.awsGroupBy && d.awsGroupBy !== 'patient' ? d.awsGroupBy : undefined,
      }
      break
    case 'sns':
      out.sns = {
        ...awsDestAccess(d),
        topicArn: d.snsTopicArn || undefined,
        subject: d.snsSubject || undefined,
        groupBy: d.awsGroupBy && d.awsGroupBy !== 'patient' ? d.awsGroupBy : undefined,
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
  if (d.elrConfig.trim()) out.elr = { config: d.elrConfig.trim(), rctc: d.elrRctc.trim() || undefined }
  if (d.pgpEnabled) {
    const to = pgpKeyList(d.pgpEncryptTo)
    out.pgp = {
      encryptTo: to.length ? to : undefined,
      signWith: d.pgpSignWith.trim() || undefined,
      passphrase: d.pgpSignWith.trim() ? d.pgpPassphrase || undefined : undefined,
      armor: d.pgpArmor || undefined,
      noCompress: d.pgpNoCompress || undefined,
      fileName: d.pgpFileName.trim() || undefined,
    }
  }

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
    fhirBundleScript: '',
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

    destKafkaBrokers: '',
    destKafkaTopic: '',
    destKafkaKey: '',
    destKafkaAcks: 'all',
    destKafkaCompression: 'snappy',
    destKafkaSaslMechanism: '',
    destKafkaSaslUsername: '',
    destKafkaSaslPassword: '',
    // True, matching the server's secure default. False here would turn encryption off on any channel built and saved without
    // touching the control.
    smtpStartTLS: true,
    soapUrl: '',
    soapProfile: '',
    soapFacilityId: '',
    soapWarningsAreErrors: false,
    soapAction: '',
    soapVersion: '1.1',
    soapBody: '',
    soapHeader: '',
    soapUsername: '',
    soapPassword: '',
    soapFaultIsSuccess: '',

    directAddress: '',
    directCertFile: '',
    directKeyFile: '',
    directAnchorsFile: '',
    directTo: '',
    mpiAuthorities: '',
    mpiIdType: '',
    mpiSsnSystem: '',
    mpiNoMerge: false,
    as2Url: '',
    as2From: '',
    as2To: '',
    as2CertFile: '',
    as2KeyFile: '',
    as2PartnerCertFile: '',
    as2Unsigned: false,
    as2Unencrypted: false,
    as2Compress: false,
    as2Receipt: 'sync',
    as2UnsignedReceipt: false,
    as2ReceiptUrl: '',
    as2ContentType: '',
    as2FileName: '',
    as2Subject: '',
    as2Username: '',
    as2Password: '',
    directSubject: '',
    directText: '',
    directAttachName: '',
    directXdm: false,
    directXdmSourceId: '',
    directRecipientCertsDir: '',
    directDns: '',
    directLdap: '',
    directSmtpHost: '',
    directUsername: '',
    directPassword: '',
    directRequestMdn: false,
    xdsUrl: '',
    xdsSourceId: '',
    xdsPatientDomain: '',
    xdsFacilityCode: '',
    xdsFacilityScheme: '2.16.840.1.113883.6.96',
    xdsFacilityDisplay: '',
    xdsPracticeCode: '',
    xdsPracticeScheme: '2.16.840.1.113883.6.96',
    xdsPracticeDisplay: '',
    xdsClassCode: '',
    xdsClassScheme: '',
    xdsToken: '',
    xdsDirectFrom: '',
    xdsDirectTo: '',
    dicomwebUrl: '',
    dicomwebToken: '',
    dicomwebUsername: '',
    dicomwebPassword: '',
    dicomAddr: '',
    dicomCalledAe: '',
    dicomCallingAe: '',
    dicomTls: false,


    jsScript: '',
    responseTransformer: '',
    elrConfig: '',
    elrRctc: '',
    pgpEnabled: false,
    pgpEncryptTo: '',
    pgpSignWith: '',
    pgpPassphrase: '',
    pgpArmor: false,
    pgpNoCompress: false,
    pgpFileName: '',
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
    s3StorageClass: '',
    s3Format: '',
    sqsQueueUrl: '',
    snsTopicArn: '',
    snsSubject: '',
    awsGroupBy: '',
    fhirAuthType: '',
    fhirAuthTenantId: '',
    fhirAuthTokenUrl: '',
    fhirAuthClientId: '',
    fhirAuthClientSecret: '',
    fhirAuthScope: '',
    fhirEcr: false,
    fhirEcrRctc: '',
    fhirEcrSource: '',
    fhirEcrFacilityName: '',
    fhirEcrFacilityNpi: '',
    fhirEcrFacilityPhone: '',
    fhirEcrFacilityLine: '',
    fhirEcrFacilityCity: '',
    fhirEcrFacilityState: '',
    fhirEcrFacilityPostalCode: '',
    destAmqpAddr: '',
    destAmqpAddress: '',
    destAmqpUsername: '',
    destAmqpPassword: '',
    destAmqpTls: true,
    azAccount: '',
    azContainer: '',
    azKey: '',
    azSas: '',
    azEndpoint: '',
    azBlob: '',
    azTier: '',
    destTlsCaFile: '',
    destTlsCertFile: '',
    destTlsKeyFile: '',
    destMqttAddr: '',
    destMqttTopic: '',
    destMqttClientId: '',
    destMqttUsername: '',
    destMqttPassword: '',
    destMqttTls: true,
    destMqttQos: '1',
    destMqttRetain: false,
    destIbmUrl: '',
    destIbmQmgr: '',
    destIbmQueue: '',
    destIbmUsername: '',
    destIbmPassword: '',
    destIbmApiVersion: '',
    destIbmNonPersistent: false,
    destPsProject: '',
    destPsTopic: '',
    destPsCredentialsFile: '',
    destPsEndpoint: '',
    destPsOrderBy: '',
    destGcsBucket: '',
    destGcsCredentialsFile: '',
    destGcsEndpoint: '',
    destGcsObject: '',
    destGcsStorageClass: '',
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

/** The region, keys and endpoint an AWS source signs with, inline in its block as in the channel file. */
function awsSrcAccess(d: ChannelDraft): Record<string, unknown> {
  return {
    region: d.awsSrcRegion || undefined,
    accessKeyId: d.awsSrcAccessKeyId || undefined,
    secretAccessKey: d.awsSrcSecretAccessKey || undefined,
    sessionToken: d.awsSrcSessionToken || undefined,
    endpoint: d.awsSrcEndpoint || undefined,
  }
}

/** The same for an SQS or SNS destination, from the shared s3* access fields. */
function awsDestAccess(d: Destination): Record<string, unknown> {
  return {
    region: d.s3Region || undefined,
    accessKeyId: d.s3AccessKeyId || undefined,
    secretAccessKey: d.s3SecretAccessKey || undefined,
    sessionToken: d.s3SessionToken || undefined,
    endpoint: d.s3Endpoint || undefined,
  }
}

/** commaList splits a comma-separated field into a list, dropping blanks. */
function commaList(value: string): string[] | undefined {
  const out = value
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s !== '')
  return out.length > 0 ? out : undefined
}

/**
 * kafkaSasl builds the authentication block, or nothing when no mechanism is chosen.
 *
 * Omitted entirely rather than sent with an empty mechanism, because the server validates the
 * mechanism when the block is present and an empty one would be refused - turning "I did not
 * configure authentication" into a rejected channel.
 */
function kafkaSasl(
  mechanism: string,
  username: string,
  password: string,
): { mechanism: string; username?: string; password?: string } | undefined {
  if (!mechanism) return undefined
  return {
    mechanism,
    username: username || undefined,
    password: password || undefined,
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
    edifactEnvelope: 'require',
    edifactSplit: false,
    edifactAcknowledge: 'none',
    // None by default. Classical X12 acknowledgement is asynchronous, and a partner who is
    // not expecting one may treat it as an unsolicited interchange.
    x12Acknowledge: 'none',
    x12AckSenderId: '',
    x12AckSenderQualifier: 'ZZ',

    sourceKind: 'mllp',

    soapSourceListen: '',
    soapSourceProfile: '',
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

    directSrcAddress: '',
    as2SrcListen: ':4080',
    as2SrcPath: '',
    as2SrcId: '',
    as2SrcCertFile: '',
    as2SrcKeyFile: '',
    as2SrcPartners: [],
    as2SrcTlsEnabled: false,
    as2SrcTlsCertFile: '',
    as2SrcTlsKeyFile: '',
    as2SrcMaxMessageSize: '',
    as2SrcDuplicateWindow: '',
    directSrcCertFile: '',
    directSrcKeyFile: '',
    directSrcAnchorsFile: '',
    directSrcListen: '',
    directSrcTlsCert: '',
    directSrcTlsKey: '',
    directSrcMaxBytes: 0,
    directSrcSmtpHost: '',
    directSrcUsername: '',
    directSrcPassword: '',
    directSrcImapHost: '',
    directSrcImapPort: 0,
    directSrcImapSecurity: '',
    directSrcImapUsername: '',
    directSrcImapPassword: '',
    directSrcImapMailbox: '',
    directSrcImapAfterRead: '',
    directSrcImapMoveTo: '',
    directSrcImapErrorFolder: '',
    directSrcImapPollInterval: '',
    directSrcImapCaFile: '',
    directSrcImapServerName: '',
    directSrcXdrListen: '',
    directSrcXdrTlsCert: '',
    directSrcXdrTlsKey: '',
    directSrcXdrClientCaFile: '',
    dicomwebListen: '',
    dicomwebPath: '',
    dicomwebToken: '',
    dicomwebUsername: '',
    dicomwebPassword: '',
    dicomwebTls: false,
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

    kafkaBrokers: '',
    kafkaTopics: '',
    kafkaGroup: '',
    kafkaFromBeginning: false,
    kafkaCommitAfterDelivery: true,
    kafkaSessionTimeout: '',
    kafkaSaslMechanism: '',
    kafkaSaslUsername: '',
    kafkaSaslPassword: '',

    awsSrcRegion: '',
    awsSrcAccessKeyId: '',
    awsSrcSecretAccessKey: '',
    awsSrcSessionToken: '',
    awsSrcEndpoint: '',
    sqsSrcQueueUrl: '',
    sqsSrcWaitSeconds: '',
    sqsSrcMaxMessages: '',
    sqsSrcVisibility: '',
    s3SrcBucket: '',
    s3SrcPrefix: 'inbound/',
    s3SrcSuffix: '',
    s3SrcAfterRead: 'move',
    s3SrcMoveTo: '',
    s3SrcErrorPrefix: '',
    s3SrcPollSeconds: 30,
    s3SrcPathStyle: false,
    s3SrcFramed: false,
    s3SrcMaxObjectSize: '',

    amqpAddr: '',
    amqpAddress: '',
    amqpUsername: '',
    amqpPassword: '',
    amqpTls: true,
    amqpPrefetch: '',

    azSrcAccount: '',
    azSrcContainer: '',
    azSrcKey: '',
    azSrcSas: '',
    azSrcEndpoint: '',
    azSrcPrefix: 'inbound/',
    azSrcSuffix: '',
    azSrcAfterRead: 'move',
    azSrcMoveTo: '',
    azSrcErrorPrefix: '',
    azSrcPollSeconds: 30,
    azSrcFramed: false,

    srcPgpEnabled: false,
    srcPgpDecryptWith: '',
    srcPgpPassphrase: '',
    srcPgpVerifyWith: '',
    srcPgpRequireSignature: false,
    srcPgpAllowUnencrypted: false,
    srcPgpPassPlain: false,

    mailProtocol: 'imap',
    mailHost: '',
    mailPort: '',
    mailSecurity: 'tls',
    mailUsername: '',
    mailPassword: '',
    mailAuth: 'password',
    mailOAuthProvider: 'microsoft',
    mailOAuthTenant: '',
    mailOAuthTokenUrl: '',
    mailOAuthClientId: '',
    mailOAuthClientSecret: '',
    mailOAuthRefreshToken: '',
    mailMailbox: 'INBOX',
    mailTake: 'attachments',
    mailAttachmentNames: '',
    mailAllowedSenders: '',
    mailAfterRead: 'move',
    mailMoveTo: '',
    mailErrorFolder: '',
    mailPollSeconds: 60,

    connTlsCaFile: '',
    connTlsCertFile: '',
    connTlsKeyFile: '',

    mqttSrcAddr: '',
    mqttSrcTopic: '',
    mqttSrcClientId: '',
    mqttSrcUsername: '',
    mqttSrcPassword: '',
    mqttSrcTls: true,
    mqttSrcQos: '1',

    ibmSrcUrl: '',
    ibmSrcQmgr: '',
    ibmSrcQueue: '',
    ibmSrcUsername: '',
    ibmSrcPassword: '',
    ibmSrcApiVersion: '',
    ibmSrcPollSeconds: 2,

    psSrcProject: '',
    psSrcSubscription: '',
    psSrcCredentialsFile: '',
    psSrcEndpoint: '',
    psSrcMaxMessages: '',

    gcsSrcBucket: '',
    gcsSrcCredentialsFile: '',
    gcsSrcEndpoint: '',
    gcsSrcPrefix: 'inbound/',
    gcsSrcSuffix: '',
    gcsSrcAfterRead: 'move',
    gcsSrcMoveTo: '',
    gcsSrcErrorPrefix: '',
    gcsSrcPollSeconds: 30,
    gcsSrcFramed: false,

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
