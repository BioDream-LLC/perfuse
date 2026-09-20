import { Check, Choose, ScriptArea, Text } from './builderFields'
import type { ScriptKind } from './ScriptEditor'
import type { ChannelDraft } from './model'

// The channel's scripts.
//
// This section did not exist. The draft model carried filterScript and transformerScript, the emitter wrote
// them, and nothing in the form ever set them - so a channel needing any script had to be written by hand,
// and four of the six slots had no representation at all.
//
// The preprocessor is the one that matters most for a first day with a real feed. It is the only place a
// message that does not parse can be repaired: a stray character from a serial gateway, a segment terminator
// that arrived as a line feed, a partner whose exporter prefixes a byte order mark. Without it Perfuse
// rejects traffic that the system being replaced accepts.

const LANGUAGES: { value: 'javascript' | 'lua' | 'wasm'; label: string }[] = [
  { value: 'javascript', label: 'JavaScript' },
  { value: 'lua', label: 'Lua' },
  // WebAssembly is a runtime rather than a language, and it is here because the setting is called Language and
  // this is where somebody looks. What it selects is a runtime that does not care which language produced the
  // module - the point of offering it is that a transformation already written in Rust or Go can be brought
  // rather than rewritten.
  { value: 'wasm', label: 'WebAssembly module' },
]

/**
 * SLOTS describes each script slot in the order a message meets them.
 *
 * Ordered rather than alphabetical, because the sequence is the explanation: repair the text, decide whether
 * to keep the message, change it, then react to what happened. Deploy and undeploy sit outside a message
 * entirely and come last.
 */
const SLOTS = [
  {
    key: 'preprocessorScript',
    kind: 'preprocessor' as const,
    label: 'Preprocessor',
    hint:
      'Runs before the message is parsed, reads it as "message" and returns the text to parse. The only place a message that does not parse can be repaired. Works on every format except imaging.',
  },
  {
    key: 'filterScript',
    kind: 'filter' as const,
    label: 'Filter',
    hint: 'Must return true or false. Runs after the rules above. HL7 only — other formats use the rules.',
  },
  {
    key: 'transformerScript',
    kind: 'transformer' as const,
    label: 'Transformer',
    hint: 'Changes the message. Runs after the steps above. HL7 only — other formats use the steps.',
  },
  {
    key: 'postprocessorScript',
    kind: 'postprocessor' as const,
    label: 'Postprocessor',
    hint:
      'Runs after the message has been handled and cannot change it. Use it to notify, count or record. Works on every format.',
  },
  {
    key: 'deployScript',
    kind: 'lifecycle' as const,
    label: 'On start',
    hint:
      'Runs once when the channel starts, before it accepts anything. If it fails the channel does not start, which is deliberate: better than accepting messages it cannot handle.',
  },
  {
    key: 'undeployScript',
    kind: 'lifecycle' as const,
    label: 'On stop',
    hint:
      'Runs once when the channel stops, after in-flight messages have finished. Its failure cannot prevent a stop.',
  },
] as const satisfies readonly {
  key: keyof ChannelDraft & `${string}Script`
  // Required, not optional. The kind decides the wrapper the source compiles inside, so a slot without one would be checked as
  // whatever the default happens to be - and a writer checked as a transformer is told it compiles while its return value is
  // being discarded.
  kind: ScriptKind
  label: string
  hint: string
}[]

export function BuilderScripts({
  draft,
  onChange,
}: {
  draft: ChannelDraft
  onChange: (patch: Partial<ChannelDraft>) => void
}) {
  // Which slots this data type actually runs, mirroring scriptSlotsRun in the loader.
  //
  // Shown rather than hidden when unavailable, because a field that vanishes leaves somebody wondering
  // whether they misremembered. A disabled field with the reason underneath answers the question.
  // Which formats actually run a filter or transformer script. This mirrors scriptSlotsRun in the loader, and a Go test now
  // reads this file to keep the two equal - the previous version claimed to mirror it and did not.
  //
  // It said a filter script "needs the message as a tree, which only HL7 has", and disabled the field on X12, NCPDP,
  // delimited and SCRIPT. All four run both slots: the first three through the generic path stage, and SCRIPT through the
  // same tree stage v3 uses. So the form was refusing four formats' scripts with an explanation that was not true, while
  // the server accepted them - perfuse check reads back a JavaScript filter and transformer on an X12 channel without
  // complaint. A control that refuses a working feature is the same defect as one that accepts a broken one.
  const runsMessageScripts =
    draft.dataType === 'hl7' ||
    draft.dataType === 'hl7v3' ||
    draft.dataType === 'script' ||
    draft.dataType === 'x12' ||
    draft.dataType === 'ncpdp' ||
    draft.dataType === 'delimited'

  const imaging = draft.dataType === 'dicom'

  const unavailable = (key: string): string | null => {
    if ((key === 'filterScript' || key === 'transformerScript') && !runsMessageScripts) {
      const slot = key === 'filterScript' ? 'filter' : 'transformer'

      // The two formats that genuinely cannot, each for its own reason. A single message covering both would have to be
      // vague enough to be useless to either.
      return imaging
        ? `An imaging object is binary with pixel data inside it, so a ${slot} script would have to edit it as text and could produce a study that opens and is wrong. Use the transformation steps above, which name what to change.`
        : `A raw channel has no addressable structure, so a ${slot} script would have nothing to read. A preprocessor works here, because it sees the text as it arrived.`
    }
    if (key === 'preprocessorScript' && imaging) {
      return 'An imaging object is binary with pixel data in it, so editing it as text would corrupt the image.'
    }
    return null
  }

  const anyScript = SLOTS.some((slot) => draft[slot.key].trim() !== '')
  const wasm = draft.scriptLanguage === 'wasm'

  return (
    <div className="space-y-4">
      <Choose
        label="Language"
        value={draft.scriptLanguage}
        onChange={(v) => onChange({ scriptLanguage: v })}
        options={LANGUAGES}
        hint={
            wasm
              ? 'The fields below name compiled .wasm files rather than holding source. A module is given the message on stdin and writes its result to stdout: a filter writes true or false, and anything else replaces the message.'
              : anyScript
                ? 'Applies to every script below. Lua has a smaller sandbox and no Java compatibility layer; JavaScript is what a Mirth script is written in.'
                : 'Applies to every script below, once you write one.'
        }
      />

      {/* Capabilities. A script is refused these unless they are named, which is deliberate: Mirth grants all of them to every
          script, so a transformer there can quietly start reading the filesystem after a copy-paste.
          
          Offered here because the build model's comment says it must be - granting file access without naming directories used to
          reach the whole filesystem, including this program's own database of password hashes, and creating a channel needs only the
          editor role, so the escalation completed the moment an administrator started it. */}
      <div className="rounded-lg border border-slate-800 p-3">
        <p className="text-sm text-slate-300">What these scripts are allowed to do</p>
        <p className="mt-1 text-xs text-slate-500">
          Everything not listed is refused. Grant only what a script actually needs — each of these turns a script from something
          that transforms a message into something that reaches outside it.
        </p>

        <div className="mt-2 space-y-2">
          <Check
            label="Read and write files"
            value={draft.scriptAllowFile}
            onChange={(v) => onChange({ scriptAllowFile: v })}
            hint="Limited to the directories named below, which are required. Without them this would reach the whole filesystem."
          />

          {draft.scriptAllowFile && (
            <Text
              label="Directories it may use"
              value={draft.scriptFileRoots}
              onChange={(v) => onChange({ scriptFileRoots: v })}
              placeholder="/var/perfuse/scratch, /mnt/incoming"
              mono
              hint="Comma separated, and required — a channel granting file access without them is refused at load. This is the whole difference between file access and unrestricted file access."
            />
          )}

          <Check
            label="Query a database"
            value={draft.scriptAllowDatabase}
            onChange={(v) => onChange({ scriptAllowDatabase: v })}
            hint="Uses the connections configured on this channel. A script with this can read anything those credentials can."
          />

          <Check
            label="Route messages to other channels"
            value={draft.scriptAllowRoute}
            onChange={(v) => onChange({ scriptAllowRoute: v })}
            hint="Lets a script send a message into another channel, which is how a loop between two channels becomes possible."
          />
        </div>
      </div>

      {SLOTS.map((slot) => {
        const reason = unavailable(slot.key)

        // A module is compiled output, two megabytes from anything built in Go, so the field names a file
        // instead of holding source. A textarea here would invite somebody to paste a module into it, which
        // cannot work and would fail with an error about a missing file rather than about the paste.
        if (wasm) {
          return (
            <Text
              key={slot.key}
              label={`${slot.label} module`}
              value={draft[slot.key]}
              onChange={(v) => onChange({ [slot.key]: v } as Partial<ChannelDraft>)}
              placeholder={reason ? '' : 'e.g. redact.wasm'}
              hint={
                reason ??
                `Path to a compiled .wasm module, relative to this channel file. ${slot.hint}`
              }
              problem={reason && draft[slot.key].trim() !== '' ? reason : undefined}
            />
          )
        }

        return (
          <ScriptArea
            key={slot.key}
            label={slot.label}
            value={draft[slot.key]}
            onChange={(v) => onChange({ [slot.key]: v } as Partial<ChannelDraft>)}
            kind={slot.kind}
            // WebAssembly is handled above, so this is only ever one of the two the editor can highlight. Narrowed here rather
            // than made optional, because defaulting to javascript would highlight Lua wrongly - and wrong colours are worse
            // than none, since somebody would trust them and conclude a correct comment was broken code.
            language={draft.scriptLanguage === 'lua' ? 'lua' : 'javascript'}
            hint={reason ?? slot.hint}
            problem={reason && draft[slot.key].trim() !== '' ? reason : undefined}
          />
        )
      })}
    </div>
  )
}
