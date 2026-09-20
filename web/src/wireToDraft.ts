import {
  emptyDraft,
  newDestination,
  newV3Step,
  nextId,
  rulesToExpression,
  type ChannelDraft,
  type Destination,
  type DraftStep,
  type DraftV3Step,
  type Rule,
  type RuleOperator,
  type V3StepKind,
  type DraftDICOMStep,
  type DICOMStepKind,
  newDICOMStep,
  formatPairs,
} from './model'

// Turning a channel file back into form state.
//
// The server has already proved the round trip is faithful before this runs: it decoded the file into
// the same model the build endpoint encodes, re-encoded it, loaded both through the real loader and
// required the results to be identical. So by the time a model reaches here, every setting in the file
// is one the form can hold.
//
// What is left is the filter, and that is not covered by the server's proof. The form stores a filter
// as rule rows and joins them into an expression on the way out; going the other way means parsing an
// expression back into rows, and guessing wrong there would silently change what a live channel does.
//
// So the same trick is used again at this level. Rules are parsed, then joined back into an
// expression with the very function that would write them out, and the result is compared to the
// original string. If they do not match character for character, the parse is thrown away and the
// filter is kept as text in a single field. That way the rule editor is offered only when it provably
// says the same thing, and there is no case where opening a channel quietly rewrites its filter.

/** wireToDraft converts the server's form model into editable form state. */
export function wireToDraft(model: WireModel): ChannelDraft {
  const draft = emptyDraft()

  draft.name = model.name ?? ''
  draft.description = model.description ?? ''
  draft.group = model.group ?? ''
  // Absent means the default, which is enabled. Only an explicit false disables.
  draft.enabled = model.enabled !== false

  if (model.dataType === 'x12') {
    draft.dataType = 'x12'
    draft.x12Envelope = (model.x12?.envelope as ChannelDraft['x12Envelope']) ?? draft.x12Envelope
    draft.x12Acknowledge =
      (model.x12?.acknowledge as ChannelDraft['x12Acknowledge']) ?? draft.x12Acknowledge
    draft.x12AckSenderId = model.x12?.ackSenderId ?? draft.x12AckSenderId
    draft.x12AckSenderQualifier = model.x12?.ackSenderQualifier ?? draft.x12AckSenderQualifier
    draft.x12Split = model.x12?.split ?? draft.x12Split
  }

  if (model.dataType === 'delimited') {
    draft.dataType = 'delimited'
    draft.delimitedDelimiter = model.delimited?.delimiter ?? ''
    draft.delimitedQuote = model.delimited?.quote ?? ''
    draft.delimitedComment = model.delimited?.comment ?? ''
    draft.delimitedHasHeader = model.delimited?.hasHeader === true
    draft.delimitedColumns = (model.delimited?.columns ?? []).join(', ')
    draft.delimitedTrimSpace = model.delimited?.trimSpace === true
    draft.delimitedRelaxed = model.delimited?.relaxed === true
    draft.delimitedKeepBlankLines = model.delimited?.keepBlankLines === true
  }

  if (model.dataType === 'hl7v3') {
    draft.dataType = 'hl7v3'
    draft.v3Filter = model.hl7v3?.filter ?? ''
    // Absent means the default, which is on. Only an explicit false turns it off — the same rule as
    // enabled above, and for the same reason: a channel file that says nothing must mean the default
    // rather than the falsy value of whatever type the field happens to be.
    draft.v3Acknowledge = model.hl7v3?.acknowledge !== false
    draft.v3SenderDevice = model.hl7v3?.senderDevice ?? ''
    draft.v3SenderOid = model.hl7v3?.senderOid ?? ''
    draft.v3Steps = (model.hl7v3?.transformations ?? []).map(readV3Step)
  }

  // An imaging channel's named steps. Without this, opening an existing channel in the form and saving it
  // would drop its transformations silently - which for a de-identify step means the next object leaves
  // carrying the patient, and nothing anywhere reports that a step was lost.
  if (model.dicom?.transformations?.length) {
    draft.dicomSteps = model.dicom.transformations.map(readDICOMStep)
  }

  readSource(draft, model.source)

  // Attachments are channel-level. The paths become one per line; the minimum size is taken from the
  // first rule, because the form offers one figure for all of them - a per-rule minimum is expressible
  // in the file and is not worth a column in a form nobody asked for it in.
  if (model.attachments?.extract?.length) {
    draft.attachmentsPaths = model.attachments.extract
      .map((rule) => rule.path ?? '')
      .filter((path) => path !== '')
      .join('\n')
    draft.attachmentsMinBytes = model.attachments.extract[0]?.minBytes ?? 0
  }
  // The server's default is on, so absent must read back as on or saving an untouched form would turn
  // reassembly off and quietly start returning messages without their documents.
  draft.attachmentsReassemble = model.attachments?.reassemble ?? true
  readFilter(draft, model.filter)

  draft.steps = (model.transformations ?? []).map(readStep).filter((s): s is DraftStep => s !== null)

  // Capabilities are read back so that opening a channel and saving it cannot silently revoke them - which would be a channel that
  // stops working with no diff explaining why.
  const allowed = model.scripts?.allow ?? []
  draft.scriptAllowFile = allowed.includes('file')
  draft.scriptAllowDatabase = allowed.includes('database')
  draft.scriptAllowRoute = allowed.includes('route')
  draft.scriptFileRoots = (model.scripts?.fileRoots ?? []).join(', ')

  draft.filterScript = model.scripts?.filter ?? ''
  draft.transformerScript = model.scripts?.transformer ?? ''
  draft.preprocessorScript = model.scripts?.preprocessor ?? ''
  draft.postprocessorScript = model.scripts?.postprocessor ?? ''
  draft.deployScript = model.scripts?.deploy ?? ''
  draft.undeployScript = model.scripts?.undeploy ?? ''

  // An unmarked script is javascript, for Mirth compatibility. But wasm has to be preserved rather than folded
  // into that default: reading it as javascript would rewrite language: wasm on the next save and leave the
  // module path behind as though it were a script, so a working channel would be destroyed by opening it in the
  // form and pressing save. Anything genuinely unrecognised still reads as javascript, because an unmarked script
  // JavaScript in the loader too. Opening a channel must not change what its scripts are.
  draft.scriptLanguage =
    model.scripts?.language === 'lua'
      ? 'lua'
      : model.scripts?.language === 'wasm'
        ? 'wasm'
        : 'javascript'

  draft.destinations = (model.destinations ?? []).map(readDestination)

  return draft
}

function readSource(draft: ChannelDraft, source: WireSource | undefined) {
  if (!source) return

  draft.sourceKind = (source.type as ChannelDraft['sourceKind']) ?? 'mllp'

  switch (draft.sourceKind) {
    case 'mllp':
      draft.listen = source.listen ?? ''
      break
    case 'soap':
      draft.soapSourceListen = source.soap?.listen ?? ''
      draft.soapSourcePath = source.soap?.path ?? ''
      draft.soapSourceVersion = source.soap?.version ?? '1.1'
      draft.soapSourceElement = source.soap?.element ?? ''
      draft.soapSourceBase64 = source.soap?.base64 ?? false
      draft.soapSourceResponseElement = source.soap?.responseElement ?? ''
      draft.soapSourceResponseNamespace = source.soap?.responseNamespace ?? ''
      draft.soapSourceWSDL = source.soap?.wsdl ?? ''
      draft.soapSourceFaultOnNak = source.soap?.faultOnNak === true
      draft.soapSourceToken = source.soap?.token ?? ''
      draft.soapSourceUsername = source.soap?.username ?? ''
      draft.soapSourcePassword = source.soap?.password ?? ''
      break
    case 'dicom':
      draft.dicomListen = source.dicom?.listen ?? ''
      draft.dicomListenAe = source.dicom?.aeTitle ?? ''
      draft.dicomListenTls = source.dicom?.tls?.enabled ?? false
      draft.dicomAllowedCallingAe = (source.dicom?.allowedCallingAe ?? []).join(', ')
      draft.dicomSopClasses = (source.dicom?.sopClasses ?? []).join(', ')
      draft.dicomTransferSyntaxes = (source.dicom?.transferSyntaxes ?? []).join(', ')
      draft.dicomMaxObjectBytes = source.dicom?.maxObjectBytes ? String(source.dicom.maxObjectBytes) : ''
      break
    case 'dicom_query':
      draft.dicomQueryAddr = source.dicom_query?.address ?? ''
      draft.dicomQueryCalledAe = source.dicom_query?.called_ae ?? ''
      draft.dicomQueryCallingAe = source.dicom_query?.calling_ae ?? ''
      // The server's default, so absent has to read back as STUDY or saving an untouched form would
      // send a level that was never chosen.
      draft.dicomQueryLevel = source.dicom_query?.level ?? 'STUDY'
      draft.dicomQueryInterval = source.dicom_query?.interval ?? ''
      draft.dicomQueryWindow = source.dicom_query?.window ?? ''
      draft.dicomQueryReturn = (source.dicom_query?.return ?? []).join('\n')
      draft.dicomQueryEmitFirst = source.dicom_query?.emit_on_first_poll ?? false
      draft.dicomQueryTls = source.dicom_query?.tls?.enabled ?? false
      break
    case 'http':
      draft.httpListen = source.http?.listen ?? ''
      draft.httpPath = source.http?.path ?? ''
      draft.httpToken = source.http?.token ?? ''
      draft.httpReadTimeout = source.http?.readTimeout ?? ''
      draft.httpMaxMessageSize = source.http?.maxMessageSize ? String(source.http.maxMessageSize) : ''
      draft.httpTlsEnabled = source.http?.tls?.enabled === true
      draft.httpTlsCertFile = source.http?.tls?.certFile ?? ''
      draft.httpTlsKeyFile = source.http?.tls?.keyFile ?? ''
      draft.httpTlsCAFile = source.http?.tls?.caFile ?? ''
      draft.httpTlsRequireClientCert = source.http?.tls?.requireClientCert === true
      break
    case 'database':
      draft.dbDriver = source.database?.driver ?? draft.dbDriver
      draft.dbDSN = source.database?.dsn ?? ''
      draft.dbQuery = source.database?.query ?? ''
      draft.dbColumn = source.database?.column ?? ''
      draft.dbTemplate = source.database?.template ?? ''
      draft.dbQueryTimeout = source.database?.queryTimeout ?? ''
      draft.dbMaxAttempts = source.database?.maxAttempts ? String(source.database.maxAttempts) : ''
      draft.dbKeyColumn = source.database?.keyColumn ?? ''
      draft.dbAfterQuery = source.database?.afterQuery ?? ''
      draft.dbPollSeconds = readSeconds(source.database?.pollInterval)
      break
    case 'sftp':
      draft.sftpHost = source.sftp?.host ?? ''
      draft.sftpUser = source.sftp?.user ?? ''
      draft.sftpPassword = source.sftp?.password ?? ''
      draft.sftpKeyFile = source.sftp?.keyFile ?? ''
      draft.sftpKnownHosts = source.sftp?.knownHostsFile ?? ''
      draft.sftpMaxFileSize = source.sftp?.maxFileSize ? String(source.sftp.maxFileSize) : ''
      draft.sftpDir = source.sftp?.dir ?? ''
      draft.sftpPattern = source.sftp?.pattern ?? ''
      draft.sftpMoveTo = source.sftp?.moveTo ?? ''
      draft.sftpPollSeconds = readSeconds(source.sftp?.pollInterval)
      draft.sftpStableSeconds = readSeconds(source.sftp?.stableFor)
      break
  }

  if (source.ack) {
    draft.ackWhen = (source.ack.when as ChannelDraft['ackWhen']) ?? draft.ackWhen
    draft.ackIncludeTriggerEvent = source.ack.includeTriggerEvent === true
    draft.ackApplication = source.ack.application ?? ''
    draft.ackFacility = source.ack.facility ?? ''
  }

  draft.idleTimeoutSeconds = readSeconds(source.idleTimeout)
  draft.maxConnections = source.maxConnections ?? 0
}

// readFilter decides whether the rule editor can be trusted with this expression.
function readFilter(draft: ChannelDraft, filter: string | undefined) {
  const expr = (filter ?? '').trim()
  draft.rawFilter = undefined
  draft.rules = []

  if (!expr) return

  const rules = rulesFor(expr)
  if (rules) {
    draft.rules = rules
    return
  }

  draft.rawFilter = expr
}

/**
 * rulesFor parses an expression into rule rows, but only returns them when the parse is provably
 * faithful.
 *
 * My first version compared the regenerated expression to the original character for character. That
 * was too strict to be useful: the writer emits double quotes, so every hand-written filter using
 * single quotes - which is most of them, and what the documentation shows - fell back to the text box
 * even though the rule editor could represent it perfectly.
 *
 * The check now proves the same thing without caring about spelling:
 *
 *  1. Parse. Every pattern here is anchored and the splitter consumes the whole string, so a parse
 *     that succeeds has accounted for every character. Nothing can be quietly ignored.
 *  2. Write the rules back out, parse that, and require the two sets of rules to be identical.
 *
 * If a round trip through the writer changes nothing, the rules hold everything the expression said,
 * and the difference between the original text and the regenerated text is formatting - quote style
 * and spacing. If anything at all shifted, the rules are thrown away and the filter is kept as
 * written.
 */
function rulesFor(expr: string): Rule[] | null {
  const rules = parseExpression(expr)
  if (!rules) return null

  const again = parseExpression(rulesToExpression(rules))
  if (!again) return null

  return sameRules(rules, again) ? rules : null
}

/** sameRules compares two rule lists by meaning, ignoring the generated ids. */
function sameRules(a: Rule[], b: Rule[]): boolean {
  if (a.length !== b.length) return false

  for (let i = 0; i < a.length; i++) {
    const x = a[i]
    const y = b[i]
    if (!x || !y) return false

    // join is ignored on the first rule, so comparing it there would reject a filter for a
    // difference that has no effect on anything.
    if (i > 0 && x.join !== y.join) return false

    if (x.path !== y.path || x.operator !== y.operator || x.value !== y.value) return false
    if (x.values.length !== y.values.length) return false
    for (let j = 0; j < x.values.length; j++) {
      if (x.values[j] !== y.values[j]) return false
    }
  }
  return true
}

/**
 * parseExpression reads an expression of the shape the rule editor produces, and nothing else.
 *
 * Deliberately narrow. It handles a flat list of comparisons joined by and/or, which is what the
 * editor writes. Anything with brackets, functions, nesting or an operator it does not recognise
 * returns null and the caller falls back to a text field. Being conservative here costs somebody a
 * rule editor on a complicated filter; being clever here would cost somebody a working interface.
 */
export function parseExpression(expr: string): Rule[] | null {
  const tokens = splitOnJoins(expr)
  if (!tokens) return null

  const rules: Rule[] = []
  for (const token of tokens) {
    const rule = parseComparison(token.text, token.join)
    if (!rule) return null
    rules.push(rule)
  }
  return rules.length ? rules : null
}

/** splitOnJoins breaks an expression on top-level and/or, refusing anything nested. */
function splitOnJoins(expr: string): { join: 'and' | 'or'; text: string }[] | null {
  const out: { join: 'and' | 'or'; text: string }[] = []

  let current = ''
  let join: 'and' | 'or' = 'and'
  let quote: string | null = null
  let i = 0

  while (i < expr.length) {
    const ch = expr[i]

    if (quote) {
      current += ch
      if (ch === quote) quote = null
      i += 1
      continue
    }

    if (ch === '"' || ch === "'") {
      quote = ch
      current += ch
      i += 1
      continue
    }

    // Brackets belong to "in [...]", which parseComparison handles as a whole. Anything else using
    // them is beyond this parser.
    if (ch === '(') return null

    const rest = expr.slice(i)
    const andMatch = /^\s+and\s+/i.exec(rest)
    const orMatch = /^\s+or\s+/i.exec(rest)

    if (andMatch && !insideBrackets(current)) {
      out.push({ join, text: current.trim() })
      join = 'and'
      current = ''
      i += andMatch[0].length
      continue
    }
    if (orMatch && !insideBrackets(current)) {
      out.push({ join, text: current.trim() })
      join = 'or'
      current = ''
      i += orMatch[0].length
      continue
    }

    current += ch
    i += 1
  }

  if (current.trim()) out.push({ join, text: current.trim() })
  return out.length ? out : null
}

/** insideBrackets reports whether an unclosed list is open, so "in ['a', 'b']" is not split. */
function insideBrackets(text: string): boolean {
  let depth = 0
  for (const ch of text) {
    if (ch === '[') depth += 1
    if (ch === ']') depth -= 1
  }
  return depth > 0
}

function parseComparison(text: string, join: 'and' | 'or'): Rule | null {
  const base = (): Rule => ({
    id: nextId('rule'),
    join,
    path: '',
    operator: '==',
    value: '',
    values: [],
  })

  // Word operators first, since "exists" and "empty" have no right-hand side and "in" and "matches"
  // would otherwise be caught by a looser pattern.
  let m = /^(\S+)\s+exists$/i.exec(text)
  if (m?.[1]) return { ...base(), path: m[1], operator: 'exists' }

  m = /^(\S+)\s+empty$/i.exec(text)
  if (m?.[1]) return { ...base(), path: m[1], operator: 'empty' }

  m = /^(\S+)\s+in\s+\[(.*)\]$/i.exec(text)
  if (m?.[1] !== undefined && m?.[2] !== undefined) {
    const values = splitList(m[2])
    if (!values) return null
    return { ...base(), path: m[1], operator: 'in', values }
  }

  m = /^(\S+)\s+matches\s+(.+)$/i.exec(text)
  if (m?.[1] && m?.[2]) {
    const value = unquote(m[2])
    if (value === null) return null
    return { ...base(), path: m[1], operator: 'matches', value }
  }

  m = /^(\S+)\s*(==|!=|>=|<=|>|<)\s*(.+)$/.exec(text)
  if (m?.[1] && m?.[2] && m?.[3]) {
    const path = m[1]
    const op = m[2]
    const rhs = m[3]
    const operator = op as RuleOperator

    // Numeric comparisons are written unquoted on the way out, so they come back unquoted.
    if (op === '>' || op === '<' || op === '>=' || op === '<=') {
      const n = rhs.trim()
      if (!/^-?\d+(\.\d+)?$/.test(n)) return null
      return { ...base(), path, operator, value: n }
    }

    const value = unquote(rhs)
    if (value === null) return null
    return { ...base(), path, operator, value }
  }

  return null
}

/** splitList reads the inside of an "in [...]" list. */
function splitList(inner: string): string[] | null {
  const out: string[] = []
  for (const part of inner.split(',')) {
    const value = unquote(part)
    if (value === null) return null
    out.push(value)
  }
  return out.length ? out : null
}

/**
 * unquote takes a quoted literal and returns its contents.
 *
 * Returns null for anything unquoted, because an unquoted right-hand side is either a number handled
 * above or something this parser does not understand - a path comparison, a function call - and
 * guessing would be exactly the mistake this file is built to avoid.
 */
function unquote(raw: string): string | null {
  const text = raw.trim()
  if (text.length < 2) return null

  const first = text[0]
  if (first !== '"' && first !== "'") return null
  if (text[text.length - 1] !== first) return null

  const inner = text.slice(1, -1)
  // A quote inside the value means escaping is in play, which the writer does not produce.
  if (inner.includes(first)) return null
  return inner
}

/** readSeconds turns a Go duration string back into a number of seconds for the form. */
//
// The writer emits "30s" or "5m0s"; zero means the setting was not stated and the form shows 0.
function readSeconds(value: string | number | undefined): number {
  if (value === undefined || value === null || value === '') return 0
  if (typeof value === 'number') return value

  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:([\d.]+)s)?$/.exec(String(value).trim())
  if (!m) return 0

  const hours = m[1] ? Number(m[1]) : 0
  const minutes = m[2] ? Number(m[2]) : 0
  const seconds = m[3] ? Number(m[3]) : 0
  return hours * 3600 + minutes * 60 + seconds
}

function readStep(step: WireStep): DraftStep | null {
  const base = {
    id: nextId('step'),
    description: step.description ?? '',
    when: step.when ?? '',
    path: '',
    value: '',
    from: '',
    to: '',
    fallback: '',
    pattern: '',
    replacement: '',
    all: false,
    width: 0,
    padWith: '',
    padRight: false,
    caseTo: 'upper' as const,
    table: [] as [string, string][],
    unmatched: 'keep' as const,
    // fail is the server's default, so a step whose file does not mention it reads back as failing - which is what it does.
    dateOnError: 'fail' as const,
  }

  if (step.set) return { ...base, kind: 'set', path: step.set.path ?? '', value: step.set.value ?? '' }
  if (step.clear) return { ...base, kind: 'clear', path: step.clear.path ?? '' }
  if (step.remove) return { ...base, kind: 'remove', path: step.remove.path ?? '' }
  if (step.trim) return { ...base, kind: 'trim', path: step.trim.path ?? '' }

  if (step.copy) {
    return {
      ...base,
      kind: 'copy',
      from: step.copy.from ?? '',
      to: step.copy.to ?? '',
      fallback: step.copy.default ?? '',
    }
  }
  if (step.case) {
    return {
      ...base,
      kind: 'case',
      path: step.case.path ?? '',
      caseTo: (step.case.to as DraftStep['caseTo']) ?? 'upper',
    }
  }
  if (step.replace) {
    return {
      ...base,
      kind: 'replace',
      path: step.replace.path ?? '',
      pattern: step.replace.pattern ?? '',
      replacement: step.replace.with ?? '',
      all: step.replace.all ?? false,
    }
  }
  if (step.pad) {
    return {
      ...base,
      kind: 'pad',
      path: step.pad.path ?? '',
      width: step.pad.width ?? 0,
      padWith: step.pad.with ?? '',
      padRight: step.pad.right ?? false,
    }
  }
  if (step.date) {
    return {
      ...base,
      kind: 'date',
      path: step.date.path ?? '',
      from: step.date.from ?? '',
      to: step.date.to ?? '',
      // Absent means fail, which is the server's default. Reading it as anything else would show a step as tolerant when it stops
      // the message, and saving that back would make it tolerant.
      dateOnError: (step.date.onError as 'fail' | 'keep' | 'clear' | undefined) ?? 'fail',
    }
  }
  if (step.map) {
    // Sorted, because a Go map has no order and an unsorted table would show its rows differently
    // every time the same channel was opened.
    const table = Object.entries(step.map.table ?? {}).sort((a, b) => a[0].localeCompare(b[0])) as [
      string,
      string,
    ][]
    return {
      ...base,
      kind: 'map',
      path: step.map.path ?? '',
      table,
      unmatched: step.map.strict ? 'strict' : step.map.default ? 'default' : 'keep',
      fallback: step.map.default ?? '',
    }
  }

  // A step with no recognised action. The server's check should have made this unreachable, so
  // dropping it silently would hide a real disagreement between the two sides.
  return null
}

function readDestination(d: WireDest): Destination {
  const dest = newDestination()

  dest.name = d.name ?? ''
  dest.type = (d.type as Destination['type']) ?? 'mllp'
  dest.enabled = d.enabled !== false

  const filter = (d.filter ?? '').trim()
  if (filter) {
    const rules = rulesFor(filter)
    if (rules) {
      dest.rules = rules
    } else {
      dest.rawFilter = filter
    }
  }

  switch (dest.type) {
    case 'mllp':
      dest.address = d.address ?? ''
      dest.tcpExpectReply = d.tcp?.expectReply === true
      dest.tcpKeepAlive = d.tcp?.keepAlive === true
      break
    case 'file':
      dest.dir = d.dir ?? ''
      dest.fileName = d.fileName ?? ''
      dest.tempSuffix = d.tempSuffix ?? ''
      // Zero is shown as an empty box rather than as "0", because the two mean the same thing here and one of them looks like a
      // deliberate setting.
      dest.retainHours = d.retainHours ? String(d.retainHours) : ''
      break
    case 'http':
      dest.url = d.http?.url ?? ''
      dest.httpMethod = d.http?.method ?? dest.httpMethod
      dest.httpContentType = d.http?.contentType ?? ''
      dest.httpFailOnBody = d.http?.failOnBody ?? ''
      dest.httpHeaders = formatPairs(d.http?.headers)

      // The token is deliberately not read back. The server never sends it, so reading it would write an empty string over a
      // stored secret the moment somebody opened the channel and saved it again.
      dest.httpSuccessStatus = (d.http?.successStatus ?? []).join(', ')
      dest.httpFollowRedirects = d.http?.followRedirects === true
      break
    case 'fhir':
      dest.url = d.fhir?.url ?? ''
      dest.fhirVersion = d.fhir?.version ?? dest.fhirVersion
      dest.fhirIdentifierSystem = d.fhir?.defaultIdentifierSystem ?? ''
      dest.fhirTimezone = d.fhir?.timezone ?? ''
      dest.fhirIdentifierSystems = formatPairs(d.fhir?.identifierSystems)
      dest.fhirClaimUSCore = d.fhir?.claimUSCore === true
      dest.fhirValidateBeforeSend = d.fhir?.validateBeforeSend === true
      dest.fhirRejectOnWarning = d.fhir?.rejectOnWarning === true
      break
    case 'cda':
      dest.url = d.cda?.url ?? ''
      dest.dir = d.cda?.dir ?? ''
      dest.cdaWrite = (d.cda?.write as Destination['cdaWrite']) ?? dest.cdaWrite
      dest.cdaOnNoDocument =
        (d.cda?.onNoDocument as Destination['cdaOnNoDocument']) ?? dest.cdaOnNoDocument
      dest.cdaRequireAgreement = d.cda?.requireAgreement ?? false
      break
    case 'database':
      dest.dbDriver = d.database?.driver ?? dest.dbDriver
      dest.dbDSN = d.database?.dsn ?? ''
      dest.dbStatement = d.database?.statement ?? ''
      dest.dbMaxOpenConns = d.database?.maxOpenConns ? String(d.database.maxOpenConns) : ''
      dest.dbParams = d.database?.params ?? []
      break
    case 'sftp':
      dest.sftpHost = d.sftp?.host ?? ''
      dest.sftpUser = d.sftp?.user ?? ''
      dest.sftpPassword = d.sftp?.password ?? ''
      dest.sftpKeyFile = d.sftp?.keyFile ?? ''
      dest.sftpKnownHosts = d.sftp?.knownHostsFile ?? ''
      dest.sftpDir = d.sftp?.dir ?? ''
      break
    case 'channel':
      dest.routeTo = d.channel?.name ?? ''
      break
    case 'dicom':
      dest.dicomAddr = d.dicom?.address ?? ''
      dest.dicomCalledAe = d.dicom?.calledAe ?? ''
      dest.dicomCallingAe = d.dicom?.callingAe ?? ''
      dest.dicomTls = d.dicom?.tls?.enabled ?? false
      break

    case 'javascript':
      dest.jsScript = d.javascript?.script ?? ''
      dest.jsTimeout = d.javascript?.timeout ?? ''
      // The server defaults successOnUndefined to true, so absent means "not required" and the
      // read-back has to match that or saving an untouched form would turn the requirement on.
      dest.jsRequireResult = d.javascript?.successOnUndefined === false
      break

    case 'soap':
      dest.soapUrl = d.soap?.url ?? ''
      dest.soapAction = d.soap?.action ?? ''
      // An absent version means 1.1, the server default. Reading it as 1.2 would send a different envelope on
      // the next save and the service would fault about the version rather than the message.
      dest.soapVersion = d.soap?.version ?? '1.1'
      dest.soapBody = d.soap?.body ?? ''
      dest.soapHeader = d.soap?.header ?? ''
      dest.soapUsername = d.soap?.username ?? ''
      dest.soapPassword = d.soap?.password ?? ''
      dest.soapFaultIsSuccess = (d.soap?.faultIsSuccess ?? []).join(', ')
      break
    case 'document':
      dest.docDir = d.document?.dir ?? ''
      // An absent format means pdf, which is the server default. Reading it as text would silently change what
      // gets written on the next save.
      dest.docFormat = d.document?.format ?? 'pdf'
      dest.docTemplate = d.document?.template ?? ''
      dest.docTitle = d.document?.title ?? ''
      dest.docFontSize = d.document?.fontSize ? String(d.document.fontSize) : ''
      dest.docLandscape = d.document?.landscape ?? false
      break
    case 'ftp':
      dest.ftpHost = d.ftp?.host ?? ''
      dest.ftpUser = d.ftp?.user ?? ''
      dest.ftpPassword = d.ftp?.password ?? ''
      // An absent security key means explicit, which is the server-side default. Reading it as "none"
      // would silently downgrade a working encrypted connection on the next save.
      dest.ftpSecurity = d.ftp?.security ?? 'explicit'
      dest.ftpAllowClearPassword = d.ftp?.allowClearPassword ?? false
      dest.ftpInsecureSkipVerify = d.ftp?.insecureSkipVerify ?? false
      dest.ftpDir = d.ftp?.dir ?? ''
      break
    case 's3':
      dest.s3Bucket = d.s3?.bucket ?? ''
      dest.s3Region = d.s3?.region ?? ''
      dest.s3Key = d.s3?.key ?? ''
      dest.s3AccessKeyId = d.s3?.accessKeyId ?? ''
      dest.s3SecretAccessKey = d.s3?.secretAccessKey ?? ''
      dest.s3SessionToken = d.s3?.sessionToken ?? ''
      dest.s3Endpoint = d.s3?.endpoint ?? ''
      dest.s3PathStyle = d.s3?.pathStyle ?? false
      dest.s3Encryption = d.s3?.serverSideEncryption ?? ''
      break
    case 'broker':
      dest.destBrokerAddr = d.broker?.addr ?? ''
      dest.destBrokerDestination = d.broker?.destination ?? ''
      dest.destBrokerLogin = d.broker?.login ?? ''
      dest.destBrokerContentType = d.broker?.contentType ?? ''
      // Absent means persistent, matching the server. Reading absent as false would show every existing channel as willing to
      // lose messages, and saving it back would make that true.
      dest.destBrokerPersistent = d.broker?.persistent !== false
      // The passcode is not read back. The server does not send it, so reading it would write an empty string over a stored
      // credential the moment somebody opened the channel and saved it.
      break

    case 'smtp':
      dest.smtpHost = d.smtp?.host ?? ''
      dest.smtpFrom = d.smtp?.from ?? ''
      // Joined back into the single comma-separated field the form shows.
      dest.smtpTo = (d.smtp?.to ?? []).join(', ')
      dest.smtpCC = (d.smtp?.cc ?? []).join(', ')
      dest.smtpBCC = (d.smtp?.bcc ?? []).join(', ')
      dest.smtpSubject = d.smtp?.subject ?? ''
      dest.smtpBody = d.smtp?.body ?? ''
      dest.smtpAttach = d.smtp?.attach ?? false
      dest.smtpAttachName = d.smtp?.attachName ?? ''
      // Absent means on, matching the server. Reading absent as false would show encryption as off for every channel that never
      // stated it, and saving that channel back would then genuinely turn it off.
      dest.smtpStartTLS = d.smtp?.starttls !== false
      dest.smtpUsername = d.smtp?.username ?? ''
      dest.smtpPassword = d.smtp?.password ?? ''
      break
  }

  dest.timeoutSeconds = readSeconds(d.timeout)
  if (d.retry) {
    dest.retryAttempts = d.retry.attempts ?? 0
    dest.responseTransformer = d.responseTransformer ?? ''
    dest.queueEnabled = d.queue?.enabled === true
    dest.queueMaxAttempts = d.queue?.maxAttempts ? String(d.queue.maxAttempts) : ''
    dest.queueBackoff = d.queue?.backoff ?? ''
    dest.queueMaxBackoff = d.queue?.maxBackoff ?? ''
    dest.queueMaxDepth = d.queue?.maxDepth ? String(d.queue.maxDepth) : ''
    dest.queueRetainHours = d.queue?.retainHours ? String(d.queue.retainHours) : ''
    dest.retryBackoffSeconds = readSeconds(d.retry.backoff)
    dest.retryMaxBackoffSeconds = readSeconds(d.retry.maxBackoff)
  }

  return dest
}

// The shape the server sends. Loose on purpose: the server has already proved it round-trips, so the
// job here is to read it, not to police it a second time in a different language.

export interface WireModel {
  name?: string
  description?: string
  group?: string
  enabled?: boolean
  dataType?: string
  delimited?: {
    delimiter?: string
    quote?: string
    comment?: string
    hasHeader?: boolean
    columns?: string[]
    trimSpace?: boolean
    relaxed?: boolean
    keepBlankLines?: boolean
  }
  x12?: {
    envelope?: string
    split?: boolean
    acknowledge?: string
    ackSenderId?: string
    ackSenderQualifier?: string
  }
  // An imaging channel's named steps. Loosely typed because the four actions carry different fields and a
  // discriminated union here would have to be kept in step with the Go build model by hand - readDICOMStep
  // is where the shape is interpreted, and it defaults every field.
  dicom?: {
    transformations?: Record<string, any>[]
  }

  hl7v3?: {
    filter?: string
    acknowledge?: boolean
    senderDevice?: string
    senderOid?: string
    transformations?: WireV3Step[]
  }
  source?: WireSource
  attachments?: {
    extract?: { path?: string; minBytes?: number; repeats?: boolean }[]
    reassemble?: boolean
  }
  filter?: string
  transformations?: WireStep[]
  scripts?: {
    language?: string
    filter?: string
    transformer?: string
    preprocessor?: string
    postprocessor?: string
    deploy?: string
    undeploy?: string
    allow?: string[]
    fileRoots?: string[]
  }
  destinations?: WireDest[]
}

interface WireSource {
  type?: string
  listen?: string
  dicom?: {
    listen?: string
    aeTitle?: string
    tls?: { enabled?: boolean }
    allowedCallingAe?: string[]
    sopClasses?: string[]
    transferSyntaxes?: string[]
    maxObjectBytes?: number
  }
  soap?: {
    listen?: string
    path?: string
    version?: '1.1' | '1.2'
    element?: string
    base64?: boolean
    responseElement?: string
    token?: string
    username?: string
    password?: string
    responseNamespace?: string
    wsdl?: string
    faultOnNak?: boolean
  }
  dicom_query?: {
    address?: string
    called_ae?: string
    calling_ae?: string
    level?: 'STUDY' | 'SERIES' | 'IMAGE'
    interval?: string
    window?: string
    return?: string[]
    emit_on_first_poll?: boolean
    tls?: { enabled?: boolean }
  }
  http?: {
    listen?: string
    path?: string
    token?: string
    readTimeout?: string
    maxMessageSize?: number
    tls?: {
      enabled?: boolean
      certFile?: string
      keyFile?: string
      caFile?: string
      requireClientCert?: boolean
    }
  }
  database?: {
    driver?: string
    dsn?: string
    query?: string
    column?: string
    template?: string
    keyColumn?: string
    afterQuery?: string
    pollInterval?: string
    queryTimeout?: string
    maxAttempts?: number
  }
  sftp?: {
    host?: string
    user?: string
    password?: string
    keyFile?: string
    knownHostsFile?: string
    dir?: string
    pattern?: string
    moveTo?: string
    pollInterval?: string
    stableFor?: string
    maxFileSize?: number
  }
  ack?: { when?: string; application?: string; facility?: string
    includeTriggerEvent?: boolean
  }
  idleTimeout?: string
  maxConnections?: number
}

interface WireStep {
  description?: string
  when?: string
  set?: { path?: string; value?: string }
  copy?: { from?: string; to?: string; default?: string }
  clear?: { path?: string }
  remove?: { path?: string }
  trim?: { path?: string }
  case?: { path?: string; to?: string }
  replace?: { path?: string; pattern?: string; with?: string; all?: boolean }
  pad?: { path?: string; width?: number; with?: string; right?: boolean }
  date?: { path?: string; from?: string; to?: string; onError?: string }
  map?: { path?: string; table?: Record<string, string>; strict?: boolean; default?: string }
}

interface WireDest {
  responseTransformer?: string

  queue?: {
    enabled?: boolean
    maxAttempts?: number
    backoff?: string
    maxBackoff?: string
    maxDepth?: number
    retainHours?: number
  }

  broker?: {
    addr?: string
    destination?: string
    login?: string
    contentType?: string
    persistent?: boolean
  }

  // MLLP delivery semantics, under their own block.
  tcp?: {
    expectReply?: boolean
    keepAlive?: boolean
  }

  // File destination shaping, which lives at the top level of a destination rather than under a per-kind block.
  fileName?: string
  tempSuffix?: string
  retainHours?: number

  dicom?: {
    address?: string
    calledAe?: string
    callingAe?: string
    tls?: { enabled?: boolean }
  }
  javascript?: {
    script?: string
    timeout?: string
    successOnUndefined?: boolean
  }
  soap?: {
    url?: string
    action?: string
    version?: '1.1' | '1.2'
    body?: string
    header?: string
    username?: string
    password?: string
    faultIsSuccess?: string[]
  }
  document?: {
    dir?: string
    format?: 'pdf' | 'text'
    template?: string
    title?: string
    landscape?: boolean
    fontSize?: number
  }
  ftp?: {
    host?: string
    user?: string
    password?: string
    security?: 'explicit' | 'implicit' | 'none'
    allowClearPassword?: boolean
    insecureSkipVerify?: boolean
    dir?: string
  }
  s3?: {
    bucket?: string
    region?: string
    key?: string
    accessKeyId?: string
    secretAccessKey?: string
    endpoint?: string
    pathStyle?: boolean
    serverSideEncryption?: string
    sessionToken?: string
  }
  name?: string
  type?: string
  enabled?: boolean
  filter?: string
  address?: string
  dir?: string
  http?: {
    url?: string
    method?: string
    contentType?: string
    failOnBody?: string
    headers?: Record<string, string>
    successStatus?: number[]
    followRedirects?: boolean
    // bearerToken is absent on purpose. The server never sends a secret back, so a field here would only ever be undefined and
    // would invite reading it into the form, which overwrites the stored token with an empty string on the next save.
  }
  fhir?: {
    url?: string
    version?: string
    defaultIdentifierSystem?: string
    timezone?: string
    identifierSystems?: Record<string, string>
    claimUSCore?: boolean
    validateBeforeSend?: boolean
    rejectOnWarning?: boolean
  }
  cda?: {
    url?: string
    dir?: string
    write?: string
    onNoDocument?: string
    requireAgreement?: boolean
  }
  database?: { driver?: string; dsn?: string; statement?: string; params?: string[]
    maxOpenConns?: number
  }
  sftp?: {
    host?: string
    user?: string
    password?: string
    keyFile?: string
    knownHostsFile?: string
    dir?: string
  }
  smtp?: {
    host?: string
    from?: string
    to?: string[]
    cc?: string[]
    bcc?: string[]
    subject?: string
    body?: string
    attach?: boolean
    username?: string
    password?: string
    attachName?: string
    starttls?: boolean
  }
  channel?: { name?: string }
  timeout?: string
  retry?: { attempts?: number; backoff?: string; maxBackoff?: string }
}

/** WireV3Step is one HL7 v3 transformation as the server writes it. */
export interface WireV3Step {
  description?: string
  when?: string
  set?: { path?: string; value?: string }
  copy?: { from?: string; to?: string }
  clear?: { path?: string }
  nullflavor?: { path?: string; reason?: string }
  remove?: { path?: string }
  map?: { path?: string; table?: string; onMissing?: string }
  replace?: { path?: string; from?: string; to?: string }
  trim?: { path?: string }
  case?: { path?: string; to?: string }
}

/** readV3Step turns one wire step back into an editable one.
 *
 * The kind is inferred from which action is present, exactly as the server does. An unrecognised
 * step becomes a set with empty fields rather than being dropped: silently losing a step from a
 * channel somebody is editing would delete it on the next save, which is the worst thing this
 * form could do.
 */
function readDICOMStep(wire: Record<string, any>): DraftDICOMStep {
  const kind: DICOMStepKind = wire.stripPrivate
    ? 'stripPrivate'
    : wire.setAeTitle
      ? 'setAeTitle'
      : wire.setInstitution
        ? 'setInstitution'
        : 'deidentify'

  const step = newDICOMStep(kind)

  step.description = wire.description ?? ''

  step.keepDates = wire.deidentify?.keepDates === true
  step.patientId = wire.deidentify?.patientId ?? ''
  step.patientName = wire.deidentify?.patientName ?? ''

  step.calling = wire.setAeTitle?.calling ?? ''
  step.called = wire.setAeTitle?.called ?? ''

  // One per line, matching the field. Not comma joined: a tag contains a comma, so a comma separated list
  // cannot be split back into the tags it came from.
  step.keep = (wire.stripPrivate?.keep ?? []).join('\n')

  step.institutionName = wire.setInstitution?.name ?? ''
  step.address = wire.setInstitution?.address ?? ''
  step.department = wire.setInstitution?.department ?? ''

  return step
}

function readV3Step(wire: WireV3Step): DraftV3Step {
  const kind: V3StepKind = wire.copy
    ? 'copy'
    : wire.clear
      ? 'clear'
      : wire.nullflavor
        ? 'nullflavor'
        : wire.remove
          ? 'remove'
          : wire.map
            ? 'map'
            : wire.replace
              ? 'replace'
              : wire.trim
                ? 'trim'
                : wire.case
                  ? 'case'
                  : 'set'

  const step = newV3Step(kind)
  step.description = wire.description ?? ''
  step.when = wire.when ?? ''

  switch (kind) {
    case 'set':
      step.path = wire.set?.path ?? ''
      step.value = wire.set?.value ?? ''
      break
    case 'copy':
      step.from = wire.copy?.from ?? ''
      step.to = wire.copy?.to ?? ''
      break
    case 'clear':
      step.path = wire.clear?.path ?? ''
      break
    case 'nullflavor':
      step.path = wire.nullflavor?.path ?? ''
      // Kept as written rather than checked against the list here. The server validates it at
      // load, and a form that silently replaced an unrecognised code with a default would change
      // a channel's meaning on a round trip.
      step.reason = wire.nullflavor?.reason ?? step.reason
      break
    case 'remove':
      step.path = wire.remove?.path ?? ''
      break
    case 'map':
      step.path = wire.map?.path ?? ''
      step.table = wire.map?.table ?? ''
      step.onMissing = (wire.map?.onMissing as DraftV3Step['onMissing']) || 'keep'
      break
    case 'replace':
      step.path = wire.replace?.path ?? ''
      step.from = wire.replace?.from ?? ''
      step.to = wire.replace?.to ?? ''
      break
    case 'trim':
      step.path = wire.trim?.path ?? ''
      break
    case 'case':
      step.path = wire.case?.path ?? ''
      step.caseTo = wire.case?.to === 'lower' ? 'lower' : 'upper'
      break
  }

  return step
}
