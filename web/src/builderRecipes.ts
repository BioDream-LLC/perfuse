import { emptyDraft, newDestination, newStep, type ChannelDraft } from './model'

// Starting points for a new channel.
//
// This is the single largest ease-of-use win in the builder, and the cheapest. Nearly every
// interface anybody builds is one of a handful of shapes, so picking the closest one turns a
// blank form into two or three fields to change. A blank form is what makes somebody decide
// this needs an expert.
//
// Each recipe is a complete, valid channel apart from its addresses. That matters: a starting
// point that still does not validate teaches nothing, because the first thing it does is show
// an error.

export interface Recipe {
  id: string
  label: string
  /** What this is for, in the words somebody would use to describe their problem. */
  blurb: string
  build: () => ChannelDraft
}

/** archive is the destination almost every recipe wants, so it is worth one helper. */
function fileDest(name: string, dir: string) {
  return { ...newDestination(), name, type: 'file' as const, dir }
}

function mllpDest(name: string, address: string, queued = true) {
  const d = { ...newDestination(), name, type: 'mllp' as const, address }
  if (queued) {
    // Retries rather than dropping. Chosen for every forwarding recipe because a
    // destination that discards a message when the far end is briefly down is the single
    // most common way an integration loses data, and the default nobody remembers to change.
    d.retryAttempts = 5
    d.retryBackoffSeconds = 2
  }
  return d
}

export const RECIPES: Recipe[] = [
  {
    // Directories here are relative on purpose.
    //
    // These used to be /var/lib/perfuse/archive, which cannot be created without root. So the template
    // described below as the safest first channel saved fine and then refused to start with a
    // permission error - the very first thing a new operator does, failing for a reason that has nothing
    // to do with their feed. A relative path is created on demand beside the database the server has
    // already written, so it is writable by definition.
    id: 'archive',
    label: 'Record an HL7 feed',
    blurb:
      'Take a feed from a hospital system and keep every message on disk. The usual first channel, and the safest thing to run while you learn what the feed actually contains.',
    build: () => ({
      ...emptyDraft(),
      name: 'hl7-archive',
      description: 'Record everything arriving on the feed',
      listen: ':6661',
      destinations: [fileDest('archive', './archive')],
    }),
  },
  {
    id: 'forward',
    label: 'Pass a feed to another system',
    blurb:
      'Receive over MLLP and forward to a downstream system, retrying rather than dropping when the far end is unavailable.',
    build: () => ({
      ...emptyDraft(),
      name: 'hl7-forward',
      description: 'Pass the feed through to a downstream system',
      destinations: [mllpDest('onward', 'downstream.example.org:6661')],
    }),
  },
  {
    id: 'route',
    label: 'Send admissions one way and results another',
    blurb:
      'One feed in, two destinations, each taking only the message types it cares about. The shape behind most real integrations.',
    build: () => {
      const adt = mllpDest('admissions', 'adt.example.org:6661')
      const oru = mllpDest('results', 'lab.example.org:6661')
      // The rules are left for the user to fill in rather than guessed at, because which
      // message types matter is the one thing a recipe cannot know.
      return {
        ...emptyDraft(),
        name: 'hl7-router',
        description: 'Route by message type',
        destinations: [adt, oru],
      }
    },
  },
  {
    id: 'fhir',
    label: 'Turn HL7 into FHIR',
    blurb:
      'Receive HL7 v2 and post FHIR resources. Needs an identifier system, because a medical record number means nothing outside the facility that issued it.',
    build: () => ({
      ...emptyDraft(),
      name: 'hl7-to-fhir',
      description: 'Convert the feed to FHIR and post it',
      destinations: [
        {
          ...newDestination(),
          name: 'fhir',
          type: 'fhir' as const,
          url: 'https://fhir.example.org/fhir',
          fhirIdentifierSystem: 'urn:oid:1.2.3.4.5',
        },
      ],
    }),
  },
  {
    id: 'http-in',
    label: 'Accept messages posted over HTTP',
    blurb:
      'For senders that post rather than hold a connection open. Set a token unless the port is unreachable from anywhere you do not control.',
    build: () => ({
      ...emptyDraft(),
      name: 'http-in',
      description: 'Accept messages posted over HTTP',
      sourceKind: 'http',
      destinations: [fileDest('archive', './archive')],
    }),
  },
  {
    id: 'sftp-in',
    label: 'Collect files from an SFTP server',
    blurb:
      'Poll a directory, read each file, and move it aside once its messages are accepted so it is never read twice.',
    build: () => ({
      ...emptyDraft(),
      name: 'sftp-in',
      description: 'Collect message files from a partner',
      sourceKind: 'sftp',
      sftpHost: 'sftp.example.org:22',
      sftpUser: 'perfuse',
      sftpKeyFile: '/etc/perfuse/id_ed25519',
      sftpKnownHosts: '/etc/perfuse/known_hosts',
      sftpDir: '/outbound',
      sftpMoveTo: '/outbound/done',
      destinations: [fileDest('archive', './archive')],
    }),
  },
  {
    id: 'db-in',
    label: 'Send messages queued in a database',
    blurb:
      'Poll a table for rows to send. Needs a key column or an after-query, so a row cannot be sent twice.',
    build: () => ({
      ...emptyDraft(),
      name: 'db-in',
      description: 'Send messages queued in a database table',
      sourceKind: 'database',
      dbDSN: 'postgres://user:password@db.example.org/records',
      dbQuery: 'SELECT id, payload FROM outbound ORDER BY id',
      dbColumn: 'payload',
      dbKeyColumn: 'id',
      destinations: [mllpDest('onward', 'downstream.example.org:6661')],
    }),
  },
  {
    id: 'x12',
    label: 'Take in X12 claims',
    blurb:
      'Accept an X12 interchange, check its envelope counts, and split it into one message per transaction set. X12 acknowledges with a 997 sent separately, never in the response.',
    build: () => ({
      ...emptyDraft(),
      name: 'claims-in',
      description: 'Accept and split X12 claim interchanges',
      dataType: 'x12',
      sourceKind: 'http',
      httpListen: '0.0.0.0:8081',
      httpPath: '/claims',
      destinations: [fileDest('archive', './claims')],
    }),
  },
  {
    id: 'normalise',
    label: 'Clean up a feed as it passes through',
    blurb:
      'A forwarding channel with the changes people most often need: trim stray spaces, strip the leading zeroes some systems add to record numbers, and translate a coded field.',
    build: () => {
      const base = emptyDraft()
      return {
        ...base,
        name: 'hl7-normalise',
        description: 'Tidy the feed on its way through',
        steps: [
          {
            ...newStep('replace'),
            description: 'strip leading zeroes from the record number',
            path: 'PID-3.1',
            pattern: '^0+',
            replacement: '',
          },
          {
            ...newStep('trim'),
            description: 'remove stray spaces around the patient name',
            path: 'PID-5.1',
          },
        ],
        destinations: [mllpDest('onward', 'downstream.example.org:6661')],
      }
    },
  },
  {
    id: 'blank',
    label: 'Start from nothing',
    blurb:
      'An empty channel. Everything above is a starting point you can change; this is for when none of them is close.',
    build: () => emptyDraft(),
  },
]
