import { useState } from 'react'
import { Section } from './ui'
import { ScriptEditor, type ScriptKind } from './ScriptEditor'

// A workbench for scripts, deliberately not a channel editor.
//
// Editing a channel opens its raw YAML, and that stays true - a form that round-trips
// somebody's file through a UI is how comments and ordering get quietly lost. This is a
// different thing: somewhere to paste a Mirth script and find out what happens to it
// before it goes anywhere near a channel file.
//
// That is the migration question this project exists to answer. A Mirth shop has hundreds
// of transformers written against E4X and Rhino, and the only way to learn whether one
// survives the move is to run it. Doing that inside a running interface is how you find
// out in production.

type Language = 'javascript' | 'lua'

// KINDS is every slot a script can occupy, in the order a message meets them.
//
// All seven are offered because all seven exist and the engine compiles all seven. Two were offered before, which quietly told
// anyone migrating from Mirth that their preprocessors and deploy hooks could not be checked here - and they could, the endpoint
// simply refused to name them.
//
// Each carries what the slot expects, because that is the part an author cannot guess and gets wrong. The old copy said the kinds
// "compile identically" and that the wrapper only mattered at run time. True of a filter and a transformer, and misleading once
// there are seven: a writer's return value is what reports success, a preprocessor returns text rather than changing a tree, and a
// lifecycle hook has no message at all. A person checking a writer as a transformer would have been told it compiles, which is true
// and useless.
const KINDS: { kind: ScriptKind; expects: string }[] = [
  {
    kind: 'preprocessor',
    expects:
      'Runs before the message is parsed and receives it as text. Return the text to parse, or nothing to leave it alone. This is where a message that does not parse gets repaired.',
  },
  {
    kind: 'filter',
    expects:
      'Must return true to keep the message or false to drop it. Mirth requires an explicit return, and a filter that returns nothing rejects.',
  },
  {
    kind: 'transformer',
    expects:
      'Change the message in place. The return value is ignored, so a transformer that returns its work instead of writing it does nothing.',
  },
  {
    kind: 'writer',
    expects:
      'A destination that handles the message itself rather than sending it. Unlike a transformer, what it returns is captured and reports whether it succeeded.',
  },
  {
    kind: 'postprocessor',
    expects:
      'Runs after the message has been handled, with the outcome visible. It cannot change what was sent, because the sending has already happened.',
  },
  {
    kind: 'lifecycle',
    expects:
      'Runs once when the channel starts or stops. There is no message in scope at all, so a script that reaches for one is told there is none rather than handed an empty one.',
  },
  {
    kind: 'reader',
    expects:
      'A source that produces messages from nothing. Return one message as a string, or many as an array of strings; nothing means the poll found none.',
  },
]

const SAMPLES: { label: string; kind: ScriptKind; language: Language; source: string }[] = [
  {
    label: 'Mirth filter',
    kind: 'filter',
    language: 'javascript',
    source: `// A Mirth filter, unchanged.
return msg['PID']['PID.3']['PID.3.1'].toString() != '';`,
  },
  {
    label: 'E4X for-each',
    kind: 'transformer',
    language: 'javascript',
    source: `// E4X. This is rewritten to run here - the panel below shows what it became.
for each (var obx in msg['OBX']) {
  logger.info('result: ' + obx['OBX.5']['OBX.5.1'].toString());
}`,
  },
  {
    label: 'Set a field',
    kind: 'transformer',
    language: 'javascript',
    source: `// Ordinary JavaScript against the message tree.
msg['PID']['PID.8']['PID.8.1'] = 'F';
channelMap.put('sex', 'F');`,
  },
  {
    label: 'Filter a message',
    kind: 'filter',
    language: 'lua',
    source: `-- The same filter in Lua. Accessors are functions, not fields: node.text() asks the
-- node every time, so reading back a value you just wrote gives the new one.
return msg.child("PID").child("PID.3").child("PID.3.1").text() ~= ""`,
  },
  {
    label: 'Read repeats',
    kind: 'transformer',
    language: 'lua',
    source: `-- children() returns every repetition. Indexes are one-based, as everywhere in Lua.
local results = msg.children("OBX")
for i = 1, #results do
  logger.info("result: " .. results[i].child("OBX.5").child("OBX.5.1").text())
end`,
  },
  {
    label: 'Write a field',
    kind: 'transformer',
    language: 'lua',
    source: `-- ensure() is the write counterpart of child(): it creates the element if absent,
-- so this works on a message that has no PID-8 yet.
msg.child("PID").ensure("PID.8").ensure("PID.8.1").setText("F")
channelMap.put("sex", "F")`,
  },
]

export function ScriptLab() {
  const [kind, setKind] = useState<ScriptKind>('transformer')
  const [language, setLanguage] = useState<Language>('javascript')
  const [source, setSource] = useState(SAMPLES[0]!.source)

  return (
    <div className="space-y-4">
      <Section
        title="Script workbench"
        description="Compile a script and see what the E4X translator did to it, without deploying anything."
      >
        <div className="mb-3 flex flex-wrap items-center gap-3">
          <div className="flex items-center gap-2">
            <span className="text-xs text-slate-400">compile as</span>
            <div className="flex flex-wrap gap-px rounded-md border border-slate-700 p-px">
              {KINDS.map(({ kind: k }) => (
                <button
                  key={k}
                  type="button"
                  onClick={() => setKind(k)}
                  className={
                    'px-3 py-1 text-xs ' +
                    (kind === k
                      ? 'bg-sky-600 text-white'
                      : 'bg-slate-900 text-slate-400 hover:text-slate-200')
                  }
                >
                  {k}
                </button>
              ))}
            </div>
          </div>

          <div className="flex items-center gap-2">
            <span className="text-xs text-slate-400">language</span>
            <div className="flex overflow-hidden rounded-md border border-slate-700">
              {(['javascript', 'lua'] as Language[]).map((l) => (
                <button
                  key={l}
                  type="button"
                  aria-pressed={language === l}
                  onClick={() => setLanguage(l)}
                  className={
                    'px-3 py-1 text-xs ' +
                    (language === l
                      ? 'bg-sky-600 text-white'
                      : 'bg-slate-900 text-slate-400 hover:text-slate-200')
                  }
                >
                  {l === 'javascript' ? 'JavaScript' : 'Lua'}
                </button>
              ))}
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs text-slate-400">load</span>
            {SAMPLES.filter((s) => s.language === language).map((s) => (
              <button
                key={s.label}
                type="button"
                onClick={() => {
                  setSource(s.source)
                  setKind(s.kind)
                  // The language comes with the sample. Without this a Lua sample would be compiled as
                  // JavaScript and the pane would report a syntax error in code that is correct.
                  setLanguage(s.language)
                }}
                className="rounded-md border border-slate-700 px-2 py-1 text-xs text-slate-300 hover:border-sky-600 hover:text-sky-300"
              >
                {s.label}
              </button>
            ))}
          </div>
        </div>

        <ScriptEditor
          value={source}
          onChange={setSource}
          kind={kind}
          language={language}
          minHeight="22rem"
        />

        <p className="mt-3 text-xs text-slate-500">
          <span className="text-slate-400">{kind}:</span>{' '}
          {KINDS.find((k) => k.kind === kind)?.expects}{' '}
          Every kind is wrapped in a function, so a bare{' '}
          <code className="text-slate-400">return</code> is legal — which is how Mirth filters are
          written. Nothing here is executed: the script is parsed and compiled, never run, so it
          cannot reach the network, the filesystem or any message.{' '}
          {language === 'lua'
            ? 'The translation pane is JavaScript only — E4X is a JavaScript extension, so there is nothing for it to say about Lua.'
            : ''}
          {' '}A WebAssembly script is not checked here: a module is compiled output rather than
          source, so there is nothing to paste. One named by a channel is compiled and refused when
          the channel loads, which is the same guarantee this pane gives.
        </p>
      </Section>

      <Section
        title="What completes"
        description="Type a quote inside a bracket to get HL7 segment and field names from the dictionary."
      >
        <ul className="space-y-1 text-xs text-slate-400">
          <li>
            <code className="text-slate-300">msg['</code> — every segment in the dictionary, with
            its description
          </li>
          <li>
            <code className="text-slate-300">msg['PID']['PID.</code> — every field of PID, with its
            name, whether it repeats, and its HL7 table
          </li>
          <li>
            Completion is scoped to bracket subscripts on purpose. Offering fifty segment names
            while you type a variable name would be worse than offering none.
          </li>
        </ul>
      </Section>
    </div>
  )
}
