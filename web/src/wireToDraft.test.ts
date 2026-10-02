import { describe, expect, it } from 'vitest'
import { draftToWire, emptyDraft, rulesToExpression, type ChannelDraft } from './model'
import { parseExpression, wireToDraft, type WireModel } from './wireToDraft'

// The property that matters: a channel opened in the form and saved again must mean the same thing.
// Everything else here is a case where the parser must give up rather than guess.

function roundTrip(model: WireModel): WireModel {
  return draftToWire(wireToDraft(model)) as WireModel
}

const minimal: WireModel = {
  name: 'feed',
  source: { type: 'mllp', listen: '127.0.0.1:2575' },
  destinations: [{ name: 'out', type: 'file', dir: '/tmp/out' }],
}

describe('reading a channel back into the form', () => {
  it('keeps the name, source and destination', () => {
    const draft = wireToDraft(minimal)
    expect(draft.name).toBe('feed')
    expect(draft.sourceKind).toBe('mllp')
    expect(draft.listen).toBe('127.0.0.1:2575')
    expect(draft.destinations).toHaveLength(1)
    expect(draft.destinations[0]?.type).toBe('file')
    expect(draft.destinations[0]?.dir).toBe('/tmp/out')
  })

  it('treats an absent enabled key as enabled', () => {
    // The channel default is true, so an absent key must not come back as disabled - that would
    // stop a live interface on the next save.
    expect(wireToDraft(minimal).enabled).toBe(true)
    expect(wireToDraft({ ...minimal, enabled: false }).enabled).toBe(false)
  })

  it('survives a round trip through the form', () => {
    const out = roundTrip(minimal)
    expect(out.name).toBe('feed')
    expect(out.source).toMatchObject({ type: 'mllp', listen: '127.0.0.1:2575' })
    expect(out.destinations?.[0]).toMatchObject({ name: 'out', type: 'file', dir: '/tmp/out' })
  })

  it('reads every transformation kind back', () => {
    const model: WireModel = {
      ...minimal,
      transformations: [
        { description: 'a', set: { path: 'PID-8', value: 'M' } },
        { copy: { from: 'PID-3', to: 'PID-4', default: 'x' } },
        { clear: { path: 'PID-19' } },
        { remove: { path: 'ZZZ-1' } },
        { trim: { path: 'PID-5.1' } },
        { case: { path: 'PID-5.1', to: 'upper' } },
        { replace: { path: 'PID-3.1', pattern: '^0+', with: '', all: true } },
        { pad: { path: 'PID-3.1', width: 8, with: '0', right: false } },
        { date: { path: 'PID-7', from: 'YYYYMMDD', to: 'YYYY-MM-DD' } },
        { map: { path: 'PID-8', table: { '1': 'M', '2': 'F' }, default: 'U' } },
      ],
    }

    const draft = wireToDraft(model)
    expect(draft.steps.map((s) => s.kind)).toEqual([
      'set',
      'copy',
      'clear',
      'remove',
      'trim',
      'case',
      'replace',
      'pad',
      'date',
      'map',
    ])
    expect(draft.steps[0]?.description).toBe('a')
    expect(draft.steps[6]?.all).toBe(true)
    expect(draft.steps[7]?.width).toBe(8)
    expect(draft.steps[9]?.unmatched).toBe('default')
  })

  it('sorts a map table so the same channel always looks the same', () => {
    // A Go map has no order. Without sorting, reopening a channel would shuffle its translation
    // table, and somebody would think the file had changed under them.
    const first = wireToDraft({
      ...minimal,
      transformations: [{ map: { path: 'PID-8', table: { z: '1', a: '2', m: '3' } } }],
    })
    expect(first.steps[0]?.table.map(([k]) => k)).toEqual(['a', 'm', 'z'])
  })

  it('reads a duration back as a number of seconds', () => {
    const draft = wireToDraft({
      ...minimal,
      source: { type: 'mllp', listen: ':2575', idleTimeout: '30s' },
      destinations: [
        {
          name: 'out',
          type: 'file',
          dir: '/tmp/out',
          timeout: '5m0s',
          retry: { attempts: 3, backoff: '10s', maxBackoff: '2m0s' },
        },
      ],
    })
    expect(draft.idleTimeoutSeconds).toBe(30)
    expect(draft.destinations[0]?.timeoutSeconds).toBe(300)
    expect(draft.destinations[0]?.retryBackoffSeconds).toBe(10)
    expect(draft.destinations[0]?.retryMaxBackoffSeconds).toBe(120)
  })

  it('rejoins email recipients into the single field the form shows', () => {
    const draft = wireToDraft({
      ...minimal,
      destinations: [
        {
          name: 'tell',
          type: 'smtp',
          smtp: { host: 'mail:25', from: 'a@b.c', to: ['x@y.z', 'p@q.r'], body: 'something' },
        },
      ],
    })
    expect(draft.destinations[0]?.smtpTo).toBe('x@y.z, p@q.r')
  })
})

/** strip removes the generated id, which is not part of what a rule means. */
function strip(rule: { join: string; path: string; operator: string; value: string; values: string[] }) {
  return {
    path: rule.path,
    operator: rule.operator,
    value: rule.value,
    values: rule.values,
  }
}

describe('filters the rule editor can represent', () => {
  const cases = [
    "MSH-9.1 == 'ADT'",
    "MSH-9.1 != 'ORU'",
    "PID-3.1 in ['a', 'b', 'c']",
    "PID-5.1 matches '^SMITH'",
    'PID-7 exists',
    'PID-19 empty',
    'PID-8 > 3',
    'PID-8 <= 10',
    "MSH-9.1 == 'ADT' and MSH-9.2 == 'A01'",
    "MSH-9.1 == 'ADT' or MSH-9.1 == 'ORU'",
    "MSH-9.1 == 'ADT' and PID-7 exists and PID-3.1 in ['x', 'y']",
  ]

  for (const expr of cases) {
    it(`round-trips ${expr}`, () => {
      const rules = parseExpression(expr)
      expect(rules, 'the parser gave up').not.toBeNull()

      // The property the code proves, and the one that actually matters: writing the rules back out
      // and parsing them again must produce the same rules. Comparing the two strings instead would
      // demand the original use the writer's quote style, which no hand-written filter does.
      const regenerated = rulesToExpression(rules!)
      const again = parseExpression(regenerated)
      expect(again, `the writer produced something the parser cannot read: ${regenerated}`).not.toBeNull()
      expect(again!.map(strip)).toEqual(rules!.map(strip))

      // And it is stable, so opening a channel twice cannot drift.
      expect(rulesToExpression(again!)).toBe(regenerated)
    })
  }

  it('puts a representable filter into rule rows, not the text box', () => {
    const draft = wireToDraft({ ...minimal, filter: "MSH-9.1 == 'ADT'" })
    expect(draft.rawFilter).toBeUndefined()
    expect(draft.rules).toHaveLength(1)
    expect(draft.rules[0]?.path).toBe('MSH-9.1')
    expect(draft.rules[0]?.value).toBe('ADT')
  })
})

describe('filters the rule editor must not touch', () => {
  // Each of these would be a silent change to a live channel's behaviour if the parser guessed.
  const unrepresentable = [
    "(MSH-9.1 == 'ADT' or MSH-9.1 == 'ORU') and PID-7 exists",
    "not MSH-9.1 == 'ADT'",
    'PID-3.1 == PID-4.1',
    "MSH-9.1 == 'ADT' and (PID-7 exists)",
    "length(PID-3.1) > 5",
    'PID-8 > abc',
  ]

  for (const expr of unrepresentable) {
    it(`keeps ${expr} as written`, () => {
      const draft = wireToDraft({ ...minimal, filter: expr })
      expect(draft.rawFilter, 'this was parsed into rules when it should not have been').toBe(expr)
      expect(draft.rules).toHaveLength(0)
    })
  }

  it('writes an unrepresentable filter back byte for byte', () => {
    // The worst thing this form could do is regenerate a filter from empty rule rows and delete the
    // filter of a running channel.
    const expr = "(MSH-9.1 == 'ADT' or MSH-9.1 == 'ORU') and PID-7 exists"
    const out = roundTrip({ ...minimal, filter: expr })
    expect(out.filter).toBe(expr)
  })

  it('applies the same care to a destination filter', () => {
    const expr = "(PID-8 == 'M' or PID-8 == 'F')"
    const draft = wireToDraft({
      ...minimal,
      destinations: [{ name: 'out', type: 'file', dir: '/tmp/out', filter: expr }],
    })
    expect(draft.destinations[0]?.rawFilter).toBe(expr)

    const out = draftToWire(draft) as WireModel
    expect(out.destinations?.[0]?.filter).toBe(expr)
  })
})

describe('a filter with no filter', () => {
  it('leaves both the rules and the text box empty', () => {
    const draft = wireToDraft(minimal)
    expect(draft.rules).toHaveLength(0)
    expect(draft.rawFilter).toBeUndefined()

    const out = draftToWire(draft) as WireModel
    expect(out.filter).toBeUndefined()
  })

  it('does not invent a filter for a whitespace-only one', () => {
    const draft = wireToDraft({ ...minimal, filter: '   ' })
    expect(draft.rawFilter).toBeUndefined()
    expect(draft.rules).toHaveLength(0)
  })
})

describe('what the form starts from', () => {
  it('an empty draft and a read draft agree on their defaults', () => {
    // If reading a minimal channel produced different defaults from starting a new one, the two
    // paths through the form would drift apart and only one would be tested.
    const fresh: ChannelDraft = emptyDraft()
    const read = wireToDraft(minimal)

    expect(read.dataType).toBe(fresh.dataType)
    expect(read.ackWhen).toBe(fresh.ackWhen)
    expect(read.x12Envelope).toBe(fresh.x12Envelope)
  })
})

describe('routing to another channel', () => {
  const routing: WireModel = {
    name: 'router',
    source: { type: 'mllp', listen: '127.0.0.1:2575' },
    destinations: [{ name: 'to-lab', type: 'channel', channel: { name: 'lab' } }],
  }

  it('reads the target channel back', () => {
    const draft = wireToDraft(routing)
    expect(draft.destinations[0]?.type).toBe('channel')
    expect(draft.destinations[0]?.routeTo).toBe('lab')
  })

  it('survives a round trip', () => {
    // Losing the target on save would turn a working router into a channel that refuses to load,
    // which is at least loud - but it would happen to somebody who only opened the form to change
    // something else.
    const out = roundTrip(routing)
    expect(out.destinations?.[0]).toMatchObject({
      name: 'to-lab',
      type: 'channel',
      channel: { name: 'lab' },
    })
  })
})

describe('channel groups', () => {
  it('reads a group back so editing does not lose it', () => {
    // A group silently dropped on the next save would quietly un-organise a site that had just spent an
    // afternoon organising itself.
    const draft = wireToDraft({
      name: 'adt-in',
      group: 'Lab interfaces',
      source: { type: 'mllp', listen: '0.0.0.0:2575' },
      destinations: [{ name: 'out', type: 'file', dir: '/tmp/out' }],
    })
    expect(draft.group).toBe('Lab interfaces')
  })

  it('treats an absent group as ungrouped rather than undefined', () => {
    // The form field is a string. Leaving it undefined would put "undefined" in the box.
    const draft = wireToDraft({
      name: 'adt-in',
      source: { type: 'mllp', listen: '0.0.0.0:2575' },
      destinations: [{ name: 'out', type: 'file', dir: '/tmp/out' }],
    })
    expect(draft.group).toBe('')
  })

  it('round-trips a group through draftToWire', () => {
    const draft = wireToDraft({
      name: 'adt-in',
      group: 'Registry',
      source: { type: 'mllp', listen: '0.0.0.0:2575' },
      destinations: [{ name: 'out', type: 'file', dir: '/tmp/out' }],
    })
    const wire = draftToWire(draft) as { group?: string }
    expect(wire.group).toBe('Registry')
  })

  it('does not emit a group key for an ungrouped channel', () => {
    // An empty group in the file would be a setting that looks configured and is not.
    const draft = wireToDraft({
      name: 'adt-in',
      source: { type: 'mllp', listen: '0.0.0.0:2575' },
      destinations: [{ name: 'out', type: 'file', dir: '/tmp/out' }],
    })
    const wire = draftToWire(draft) as { group?: string }
    expect(wire.group).toBeUndefined()
  })
})

describe('the s3 destination', () => {
  const wire = (s3: Record<string, unknown>) =>
    wireToDraft({
      name: 'archive',
      source: { type: 'mllp', listen: '0.0.0.0:2575' },
      destinations: [{ name: 'bucket', type: 's3', s3 }],
    })

  it('reads a bucket and region back', () => {
    const draft = wire({ bucket: 'traffic', region: 'eu-west-2' })
    expect(draft.destinations[0]!.s3Bucket).toBe('traffic')
    expect(draft.destinations[0]!.s3Region).toBe('eu-west-2')
  })

  it('keeps an environment reference verbatim rather than trying to resolve it', () => {
    // The browser has no access to the server's environment, so anything other than passing the text
    // through unchanged would corrupt a working configuration on the next save.
    const draft = wire({
      bucket: 'traffic',
      region: 'eu-west-2',
      accessKeyId: '${AWS_ACCESS_KEY_ID}',
      secretAccessKey: '${AWS_SECRET_ACCESS_KEY}',
    })
    expect(draft.destinations[0]!.s3AccessKeyId).toBe('${AWS_ACCESS_KEY_ID}')
    expect(draft.destinations[0]!.s3SecretAccessKey).toBe('${AWS_SECRET_ACCESS_KEY}')
  })

  it('round-trips through draftToWire without losing the endpoint or path style', () => {
    const draft = wire({
      bucket: 'traffic',
      region: 'eu-west-2',
      endpoint: 'https://minio.hospital.local:9000',
      pathStyle: true,
      accessKeyId: '${K}',
      secretAccessKey: '${S}',
    })
    const out = draftToWire(draft) as {
      destinations: { s3?: { endpoint?: string; pathStyle?: boolean } }[]
    }
    expect(out.destinations[0]!.s3?.endpoint).toBe('https://minio.hospital.local:9000')
    expect(out.destinations[0]!.s3?.pathStyle).toBe(true)
  })

  it('omits an empty key so the server default applies', () => {
    // Writing an empty string would be a setting that looks configured and is not, and it would override
    // the date partitioning that keeps a bucket usable.
    const draft = wire({ bucket: 'traffic', region: 'eu-west-2' })
    const out = draftToWire(draft) as { destinations: { s3?: { key?: string } }[] }
    expect(out.destinations[0]!.s3?.key).toBeUndefined()
  })

  it('preserves a custom key template', () => {
    const draft = wire({
      bucket: 'traffic',
      region: 'eu-west-2',
      key: '${message_type}/${control_id}.hl7',
    })
    expect(draft.destinations[0]!.s3Key).toBe('${message_type}/${control_id}.hl7')
  })
})

describe('imaging and script destinations', () => {
  it('round-trips a DICOM destination', () => {
    const wire = {
      name: 'imaging',
      source: { type: 'mllp', listen: ':2575' },
      destinations: [
        {
          name: 'archive',
          type: 'dicom',
          dicom: {
            address: 'pacs.internal:104',
            calledAe: 'PACS_MAIN',
            callingAe: 'PERFUSE',
            tls: { enabled: true },
          },
        },
      ],
    }

    const draft = wireToDraft(wire)
    const dest = draft.destinations[0]!

    expect(dest.type).toBe('dicom')
    expect(dest.dicomAddr).toBe('pacs.internal:104')
    expect(dest.dicomCalledAe).toBe('PACS_MAIN')
    expect(dest.dicomTls).toBe(true)

    const back = draftToWire(draft) as { destinations: { dicom?: unknown }[] }
    expect(back.destinations[0]?.dicom).toEqual({
      address: 'pacs.internal:104',
      calledAe: 'PACS_MAIN',
      callingAe: 'PERFUSE',
      tls: { enabled: true },
    })
  })

  // Encryption off must come back as absent rather than as false. The server's default is off, so
  // sending false is indistinguishable from having no opinion - and a read-back that disagrees with
  // what was saved is how a save silently downgrades a setting.
  it('leaves DICOM encryption absent when it is off', () => {
    const draft = wireToDraft({
      name: 'imaging',
      source: { type: 'mllp', listen: ':2575' },
      destinations: [{ name: 'archive', type: 'dicom', dicom: { address: 'pacs:104', calledAe: 'P' } }],
    })

    expect(draft.destinations[0]?.dicomTls).toBe(false)
    expect(
      (draftToWire(draft) as { destinations: { dicom?: { tls?: unknown } }[] }).destinations[0]?.dicom
        ?.tls,
    ).toBeUndefined()
  })

  it('round-trips a script destination', () => {
    const script = "logger.info('seen');"
    const draft = wireToDraft({
      name: 'scripted',
      source: { type: 'mllp', listen: ':2575' },
      destinations: [
        { name: 'do-it', type: 'javascript', javascript: { script, timeout: '30s' } },
      ],
    })

    const dest = draft.destinations[0]!
    expect(dest.type).toBe('javascript')
    expect(dest.jsScript).toBe(script)
    expect(dest.jsTimeout).toBe('30s')
    // Absent means the server default, which is that returning nothing is success.
    expect(dest.jsRequireResult).toBe(false)

    expect(
      (draftToWire(draft) as { destinations: { javascript?: unknown }[] }).destinations[0]?.javascript,
    ).toEqual({ script, timeout: '30s' })
  })

  // The inversion is the part worth a test: the form asks "must the script report a result" and the
  // server field says "does returning nothing count as success", so one is the negation of the other.
  it('inverts the script result requirement correctly', () => {
    const draft = wireToDraft({
      name: 'scripted',
      source: { type: 'mllp', listen: ':2575' },
      destinations: [
        {
          name: 'do-it',
          type: 'javascript',
          javascript: { script: 'return "no";', successOnUndefined: false },
        },
      ],
    })

    expect(draft.destinations[0]?.jsRequireResult).toBe(true)
    expect(
      (draftToWire(draft) as { destinations: { javascript?: { successOnUndefined?: boolean } }[] })
        .destinations[0]?.javascript?.successOnUndefined,
    ).toBe(false)
  })
})

describe('imaging sources', () => {
  it('round-trips a DICOM listener', () => {
    const draft = wireToDraft({
      name: 'imaging-in',
      source: { type: 'dicom', dicom: { listen: '0.0.0.0:11112', aeTitle: 'PERFUSE' } },
      destinations: [{ name: 'out', type: 'file', dir: '/tmp/out' }],
    })

    expect(draft.sourceKind).toBe('dicom')
    expect(draft.dicomListen).toBe('0.0.0.0:11112')
    expect(draft.dicomListenAe).toBe('PERFUSE')
    expect(draft.dicomListenTls).toBe(false)

    const back = draftToWire(draft) as { source: { dicom?: unknown } }
    expect(back.source.dicom).toEqual({ listen: '0.0.0.0:11112', aeTitle: 'PERFUSE' })
  })

  it('round-trips an archive query source', () => {
    const draft = wireToDraft({
      name: 'worklist',
      source: {
        type: 'dicom_query',
        dicom_query: {
          address: 'pacs:104',
          called_ae: 'PACS_MAIN',
          calling_ae: 'PERFUSE',
          interval: '15m',
          window: '72h',
          return: ['PatientID', 'AccessionNumber', 'StudyInstanceUID'],
        },
      },
      destinations: [{ name: 'out', type: 'file', dir: '/tmp/out' }],
    })

    expect(draft.sourceKind).toBe('dicom_query')
    expect(draft.dicomQueryAddr).toBe('pacs:104')
    // STUDY is the server default and was absent, so it must read back as STUDY rather than empty -
    // otherwise saving an untouched form would send a level nobody chose.
    expect(draft.dicomQueryLevel).toBe('STUDY')
    // The textarea holds one keyword per line.
    expect(draft.dicomQueryReturn).toBe('PatientID\nAccessionNumber\nStudyInstanceUID')

    // camelCase on the way out, snake_case on the way in, and both are correct.
    //
    // wireToDraft reads a channel file, which is YAML in snake_case. draftToWire builds a request for the build endpoint, which is
    // JSON in camelCase and decoded strictly. These assertions previously expected dicom_query from draftToWire and so defended a
    // bug: the endpoint refused every build with this source type as an unknown field, so the source could be chosen in the form
    // and no channel could be produced from it.
    const back = draftToWire(draft) as {
      source: { dicomQuery?: { level?: string; return?: string[] } }
    }
    // Still absent, because it was never changed from the default.
    expect(back.source.dicomQuery?.level).toBeUndefined()
    expect(back.source.dicomQuery?.return).toEqual([
      'PatientID',
      'AccessionNumber',
      'StudyInstanceUID',
    ])
  })

  it('drops blank lines from the returned field list', () => {
    const draft = wireToDraft({
      name: 'worklist',
      source: { type: 'dicom_query', dicom_query: { address: 'pacs:104', called_ae: 'P' } },
      destinations: [{ name: 'out', type: 'file', dir: '/tmp/out' }],
    })

    draft.dicomQueryReturn = 'PatientID\n\n  \nAccessionNumber\n'

    const back = draftToWire(draft) as { source: { dicomQuery?: { return?: string[] } } }
    expect(back.source.dicomQuery?.return).toEqual(['PatientID', 'AccessionNumber'])
  })
})

describe('soap source and attachments', () => {
  it('round-trips a SOAP source', () => {
    const draft = wireToDraft({
      name: 'soap-in',
      source: {
        type: 'soap',
        soap: { listen: '0.0.0.0:8090', path: '/PatientFeed', element: 'HL7Message', base64: true },
      },
      destinations: [{ name: 'out', type: 'file', dir: '/tmp/out' }],
    })

    expect(draft.sourceKind).toBe('soap')
    expect(draft.soapSourceListen).toBe('0.0.0.0:8090')
    expect(draft.soapSourceElement).toBe('HL7Message')
    expect(draft.soapSourceBase64).toBe(true)
    // 1.1 is the server default and was absent, so it must read back as 1.1.
    expect(draft.soapSourceVersion).toBe('1.1')

    const back = draftToWire(draft) as { source: { soap?: Record<string, unknown> } }
    expect(back.source.soap?.listen).toBe('0.0.0.0:8090')
    expect(back.source.soap?.base64).toBe(true)
    // Unchanged from the default, so still absent.
    expect(back.source.soap?.version).toBeUndefined()
  })

  it('round-trips attachment rules', () => {
    const draft = wireToDraft({
      name: 'with-docs',
      source: { type: 'mllp', listen: ':2575' },
      attachments: { extract: [{ path: 'OBX-5', minBytes: 4096 }, { path: 'ZPD-3' }] },
      destinations: [{ name: 'out', type: 'file', dir: '/tmp/out' }],
    })

    expect(draft.attachmentsPaths).toBe('OBX-5\nZPD-3')
    expect(draft.attachmentsMinBytes).toBe(4096)
    // Absent reassemble means the server default, which is on.
    expect(draft.attachmentsReassemble).toBe(true)

    const back = draftToWire(draft) as {
      attachments?: { extract: { path: string }[]; reassemble?: boolean }
    }
    expect(back.attachments?.extract.map((r) => r.path)).toEqual(['OBX-5', 'ZPD-3'])
    // On is the default, so it stays absent rather than being stated.
    expect(back.attachments?.reassemble).toBeUndefined()
  })

  // Reassembly off has to survive, or somebody who switched it off would find it back on after the
  // next save - and start seeing tokens where documents used to be.
  it('keeps reassembly switched off', () => {
    const draft = wireToDraft({
      name: 'with-docs',
      source: { type: 'mllp', listen: ':2575' },
      attachments: { extract: [{ path: 'OBX-5' }], reassemble: false },
      destinations: [{ name: 'out', type: 'file', dir: '/tmp/out' }],
    })

    expect(draft.attachmentsReassemble).toBe(false)

    const back = draftToWire(draft) as { attachments?: { reassemble?: boolean } }
    expect(back.attachments?.reassemble).toBe(false)
  })

  // A channel with no attachment paths must send no attachments block at all. An empty extract list is
  // refused by the server, so producing one would make a form that cannot be saved.
  it('omits the attachments block when no paths are named', () => {
    const draft = wireToDraft({
      name: 'plain',
      source: { type: 'mllp', listen: ':2575' },
      destinations: [{ name: 'out', type: 'file', dir: '/tmp/out' }],
    })

    const back = draftToWire(draft) as { attachments?: unknown }
    expect(back.attachments).toBeUndefined()
  })
})

describe('an HL7 v3 channel survives a round trip', () => {
  it('keeps the v3 filter, the acknowledgement and our identity', () => {
    const draft: ChannelDraft = {
      ...emptyDraft(),
      name: 'pdq-in',
      dataType: 'hl7v3',
      sourceKind: 'http',
      httpListen: '0.0.0.0:8082',
      v3Filter: '//administrativeGenderCode@code == "F"',
      v3Acknowledge: true,
      v3SenderDevice: 'PERFUSE',
      v3SenderOid: '2.16.840.1.113883.3.999',
    }

    const back = wireToDraft(draftToWire(draft) as WireModel)

    expect(back.dataType).toBe('hl7v3')
    // The filter is the one that matters most: losing it between the form and the file would mean a
    // channel forwarding every message when its author had excluded most of them.
    expect(back.v3Filter).toBe('//administrativeGenderCode@code == "F"')
    expect(back.v3Acknowledge).toBe(true)
    expect(back.v3SenderDevice).toBe('PERFUSE')
    expect(back.v3SenderOid).toBe('2.16.840.1.113883.3.999')
  })

  it('keeps acknowledgement turned off when it was turned off', () => {
    // The asymmetric case. The server default is on, so the form sends nothing when acknowledging and
    // an explicit false when not — and reading "absent" back as off would silence a channel that was
    // meant to reply.
    const draft: ChannelDraft = {
      ...emptyDraft(),
      name: 'pdq-quiet',
      dataType: 'hl7v3',
      sourceKind: 'sftp',
      v3Acknowledge: false,
    }

    const wire = draftToWire(draft) as WireModel
    expect(wire.hl7v3?.acknowledge).toBe(false)

    const back = wireToDraft(wire)
    expect(back.v3Acknowledge).toBe(false)
  })

  it('treats an absent acknowledgement as on, not off', () => {
    // A channel file written by hand, or by an older version, says nothing. The default is on.
    const back = wireToDraft({
      name: 'pdq-in',
      dataType: 'hl7v3',
      hl7v3: { filter: '//birthTime exists' },
    } as WireModel)

    expect(back.v3Acknowledge).toBe(true)
    expect(back.v3Filter).toBe('//birthTime exists')
  })

  it('does not send the v2 filter, transformations or scripts on a v3 channel', () => {
    // The server refuses all three, so a form that offered them would produce a channel that will not
    // load. Dropping them here means the form cannot build an invalid file even if a leftover value is
    // sitting in the draft from before the data type was changed.
    const draft: ChannelDraft = {
      ...emptyDraft(),
      name: 'pdq-in',
      dataType: 'hl7v3',
      sourceKind: 'http',
      httpListen: '0.0.0.0:8082',
      v3Filter: '//birthTime exists',
      rawFilter: 'PID-8 == "F"',
      filterScript: 'return true',
      transformerScript: 'msg.set("PID-5.1", "X")',
      steps: [
        {
          kind: 'clear',
          description: '',
          when: '',
          path: 'PID-19',
          value: '',
          from: '',
          to: '',
          fallback: '',
        } as ChannelDraft['steps'][number],
      ],
    }

    const wire = draftToWire(draft) as WireModel

    expect(wire.filter).toBeUndefined()
    expect(wire.transformations).toBeUndefined()
    expect(wire.scripts).toBeUndefined()
    // But the v3 filter is still there, because that is the one a v3 channel can use.
    expect(wire.hl7v3?.filter).toBe('//birthTime exists')
  })

  it('omits our identity entirely when nothing is being acknowledged', () => {
    // Otherwise a channel that does not acknowledge carries a sender device in its file, which reads
    // as though it does.
    const draft: ChannelDraft = {
      ...emptyDraft(),
      name: 'pdq-quiet',
      dataType: 'hl7v3',
      sourceKind: 'sftp',
      v3Acknowledge: false,
      v3SenderDevice: 'LEFTOVER',
      v3SenderOid: '1.2.3',
    }

    const wire = draftToWire(draft) as WireModel

    expect(wire.hl7v3?.senderDevice).toBeUndefined()
    expect(wire.hl7v3?.senderOid).toBeUndefined()
  })
})

describe('the X12 acknowledgement settings survive a round trip', () => {
  // These reach a real trading partner, so a field lost between the form and the file would be
  // discovered by somebody asking why they were never answered.
  it('keeps the acknowledgement level and our own identifier', () => {
    const draft: ChannelDraft = {
      ...emptyDraft(),
      name: 'eligibility',
      dataType: 'x12',
      sourceKind: 'http',
      httpListen: '0.0.0.0:8081',
      x12Split: false,
      x12Acknowledge: '999',
      x12AckSenderId: 'PERFUSE',
      x12AckSenderQualifier: 'ZZ',
    }

    const back = wireToDraft(draftToWire(draft) as WireModel)

    expect(back.x12Acknowledge).toBe('999')
    expect(back.x12AckSenderId).toBe('PERFUSE')
    expect(back.x12AckSenderQualifier).toBe('ZZ')
  })

  it('omits the identifier entirely when nothing is being acknowledged', () => {
    // Otherwise a channel that does not acknowledge carries a sender identifier in its file,
    // which reads as though it does.
    const draft: ChannelDraft = {
      ...emptyDraft(),
      name: 'claims',
      dataType: 'x12',
      sourceKind: 'http',
      httpListen: '0.0.0.0:8081',
      x12Acknowledge: 'none',
      x12AckSenderId: 'LEFTOVER',
    }

    const wire = draftToWire(draft) as WireModel

    expect(wire.x12?.acknowledge).toBeUndefined()
    expect(wire.x12?.ackSenderId).toBeUndefined()
  })
})

describe('HL7 v3 transformations survive a round trip', () => {
  // The point of these is that nothing is silently dropped. A form that loses a step on read and
  // writes the rest on save deletes part of a live channel, which is the worst thing it can do -
  // and it would look like a successful save.
  it('carries every step kind through unchanged', () => {
    const wire = {
      name: 'pdq',
      dataType: 'hl7v3',
      hl7v3: {
        senderDevice: 'PERFUSE',
        senderOid: '2.16.840.1.113883.3.999',
        transformations: [
          { description: 'mask it', nullflavor: { path: '//birthTime', reason: 'MSK' } },
          { set: { path: '//patientPerson/name/family', value: 'DOE' } },
          { copy: { from: '//patient/id(1)@extension', to: '//patient/id(2)@extension' } },
          { clear: { path: '//administrativeGenderCode@code' } },
          { remove: { path: '//patientPerson/deceasedTime' } },
          { map: { path: '//administrativeGenderCode@code', table: 'gender', onMissing: 'fail' } },
          { replace: { path: '//patient/id(1)@extension', from: '^MRN', to: 'M' } },
          { trim: { path: '//patientPerson/name/family' } },
          { case: { path: '//patientPerson/name/family', to: 'lower' } },
          { when: '//administrativeGenderCode@code == "F"', trim: { path: '//given' } },
        ],
      },
      source: { type: 'http', http: { listen: '127.0.0.1:9791', path: '/pdq' } },
      destinations: [{ name: 'onward', type: 'file', dir: '/tmp/out' }],
    }

    const draft = wireToDraft(wire as never)
    expect(draft.v3Steps).toHaveLength(10)
    expect(draft.v3Steps.map((s) => s.kind)).toEqual([
      'nullflavor',
      'set',
      'copy',
      'clear',
      'remove',
      'map',
      'replace',
      'trim',
      'case',
      'trim',
    ])

    const back = draftToWire(draft) as typeof wire
    expect(back.hl7v3?.transformations).toEqual(wire.hl7v3.transformations)
  })

  it('keeps the null flavour rather than replacing it with a default', () => {
    // A form that quietly substituted its own default would change the channel's meaning on a save:
    // ASKU says the gap has been chased already and MSK says somebody withheld it deliberately, and
    // a downstream system does different things with each.
    const wire = {
      name: 'pdq',
      dataType: 'hl7v3',
      hl7v3: { transformations: [{ nullflavor: { path: '//birthTime', reason: 'ASKU' } }] },
      source: { type: 'http', http: { listen: '127.0.0.1:9791', path: '/pdq' } },
      destinations: [{ name: 'onward', type: 'file', dir: '/tmp/out' }],
    }

    const draft = wireToDraft(wire as never)
    expect(draft.v3Steps[0]?.reason).toBe('ASKU')
  })

  it('omits the on_missing default rather than stating it', () => {
    // Writing the default back would make a read differ from what was saved, which is how a save
    // appears to change a setting nobody touched.
    const draft = wireToDraft({
      name: 'pdq',
      dataType: 'hl7v3',
      hl7v3: { transformations: [{ map: { path: '//x@value', table: 'gender' } }] },
      source: { type: 'http', http: { listen: '127.0.0.1:9791', path: '/pdq' } },
      destinations: [{ name: 'onward', type: 'file', dir: '/tmp/out' }],
    } as never)

    const back = draftToWire(draft) as {
      hl7v3?: { transformations?: { map?: { onMissing?: string } }[] }
    }
    expect(back.hl7v3?.transformations?.[0]?.map?.onMissing).toBeUndefined()
  })

  it('does not send v3 steps on an HL7 v2 channel', () => {
    // The server refuses hl7v3 options on a v2 channel, so a draft that switched format must not
    // carry them along - otherwise changing the format twice produces a channel that will not load.
    const draft = wireToDraft({
      name: 'pdq',
      dataType: 'hl7v3',
      hl7v3: { transformations: [{ trim: { path: '//given' } }] },
      source: { type: 'http', http: { listen: '127.0.0.1:9791', path: '/pdq' } },
      destinations: [{ name: 'onward', type: 'file', dir: '/tmp/out' }],
    } as never)

    draft.dataType = 'hl7'
    const back = draftToWire(draft) as { hl7v3?: unknown }
    expect(back.hl7v3).toBeUndefined()
  })
})
