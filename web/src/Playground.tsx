/**
 * The in-browser playground.
 *
 * Loaded lazily and only on request, because the WebAssembly module is around 4.4 MB compressed and nobody
 * who opened the channels page should pay for it.
 *
 * The page is built around one sentence it has to make believable: *your existing Mirth scripts run
 * unchanged*. That is the whole migration argument and it is the part nobody accepts from a README. So the
 * default tab is the script tab, the default content is real Mirth E4X, and the first thing somebody does
 * here is watch their own syntax work.
 *
 * Nothing leaves the tab. That is not a promise, it is how it is built - there is no server involved at all
 * once the module has loaded - and it is stated on the page, because an analyst's realistic test data is
 * real patient data and without that sentence the correct decision for them is to close it.
 */
import { SyntaxBlock } from './SyntaxHighlight'
import { CodeArea } from './CodeArea'
import type { CodeLanguage } from './SyntaxHighlight'
import { V3FieldPicker } from './V3FieldPicker'
import { useEffect, useId, useRef, useState } from 'react'
import { decodeState, shareLink, TooLargeToShare } from './playgroundLink'
import { Section, Spinner } from './ui'
import { copyText } from './clipboard'

type Loaded = 'idle' | 'loading' | 'ready' | 'failed'

/** The functions the WebAssembly module installs on window. */
declare global {
  interface Window {
    perfuseParse?: (message: string) => ParseResult
    perfuseFilter?: (message: string, expression: string) => FilterResult
    perfuseTransform?: (message: string, stepsJson: string) => TransformResult
    perfuseScript?: (message: string, source: string) => ScriptResult
    perfuseTranslate?: (source: string) => TranslateResult
    Go?: new () => { run: (instance: WebAssembly.Instance) => void; importObject: WebAssembly.Imports }
  }
}

type ParseResult = {
  error?: string
  messageType?: string
  event?: string
  structure?: string
  controlId?: string
  version?: string
  segments?: { name: string; fields: { path: string; value: string; empty: boolean }[] }[]
}

type FilterResult = {
  error?: string
  stage?: string
  passed?: boolean
  canonical?: string
  paths?: string[]
}

type TransformResult = {
  error?: string
  stage?: string
  output?: string
  changes?: { step: string; path: string; from: string; to: string; note?: string }[]
}

type ScriptResult = {
  error?: string
  stage?: string
  output?: string
  logs?: { level: string; message: string }[]
  accept?: boolean
}

type TranslateResult = {
  error?: string
  output?: string
  unchanged?: boolean
  notes?: { line: number; kind: string; message: string }[]
}

/** A realistic ADT, because a toy message teaches nothing about a real one. */
const sampleMessage = `MSH|^~\\&|EPIC|WESTGENERAL|LABSYS|WESTLAB|20260820113000||ADT^A08^ADT_A01|C4471|P|2.5.1
EVN|A08|20260820113000
PID|1||100294^^^WESTGEN^MR~9912345678^^^NHS^NH||FROST^IVY^ANNE||19910228|F|||14 CANAL ST^^BIRMINGHAM^^B1 2JQ
PV1|1|I|WARD7^712^A|R||||1234^HALE^JUNE^^^DR|||||||||INP|V88213
DG1|1|I10|E11.9^Type 2 diabetes without complications^I10`

/**
 * The default script is deliberately Mirth E4X, not JavaScript that happens to work.
 *
 * msg['PID']['PID.5']['PID.5.1'] is exactly what sits in tens of thousands of production Mirth channels. If
 * that runs here untouched, the compatibility claim is settled in front of the person reading it.
 */
const sampleScript = `// This is Mirth E4X, pasted unchanged. It runs here as it does there.

var family = msg['PID']['PID.5']['PID.5.1'].toString();
msg['PID']['PID.5']['PID.5.1'] = family.toUpperCase();

// Pad the MRN to ten characters, which every site seems to need for somebody.
var mrn = msg['PID']['PID.3']['PID.3.1'].toString();
while (mrn.length < 10) { mrn = '0' + mrn; }
msg['PID']['PID.3']['PID.3.1'] = mrn;

logger.info('rewrote the name and padded the MRN to ' + mrn);`

const sampleFilter = `MSH-9.1 == 'ADT' and PID-5.1 exists`

const sampleSteps = `[
  { "trim": { "path": "PID-5.1" } },
  { "case": { "path": "PID-5.1", "to": "upper" } },
  { "set": { "path": "MSH-6", "value": "PERFUSE" } }
]`

type Tab = 'script' | 'filter' | 'transform' | 'parse'

// The tabs, as data, so a link carrying an unknown one is refused rather than setting the tab to a value nothing renders.
const tabs: Tab[] = ['script', 'filter', 'transform', 'parse']

function isTab(value: string): value is Tab {
  return (tabs as string[]).includes(value)
}

export default function Playground() {
  const [loaded, setLoaded] = useState<Loaded>('idle')
  const [loadError, setLoadError] = useState('')
  const [tab, setTab] = useState<Tab>('script')

  // Seeded from the link, when there is one.
  //
  // Read once, before the first render, rather than applied by an effect afterwards. An effect would show the samples first and
  // replace them a moment later, and somebody following a link to look at a specific message would watch the wrong one appear.
  const shared = useRef(decodeState(window.location.hash)).current

  const [message, setMessage] = useState(shared?.message ?? sampleMessage)
  const [script, setScript] = useState(shared?.script ?? sampleScript)
  const [filter, setFilter] = useState(shared?.filter ?? sampleFilter)
  const [steps, setSteps] = useState(shared?.steps ?? sampleSteps)

  const [shareState, setShareState] = useState<{ kind: 'idle' | 'copied' | 'uncopied' | 'refused'; detail?: string }>({
    kind: 'idle',
  })

  const [scriptResult, setScriptResult] = useState<ScriptResult | null>(null)
  const [translated, setTranslated] = useState<TranslateResult | null>(null)
  const [filterResult, setFilterResult] = useState<FilterResult | null>(null)
  const [transformResult, setTransformResult] = useState<TransformResult | null>(null)
  const [parsed, setParsed] = useState<ParseResult | null>(null)

  // The tab the link was shared from, applied once the fragment has been read.
  //
  // Separate from the initial state above because a shared link is usually shared to show one particular thing, and landing on the
  // script tab when the sender was looking at a filter wastes the click the link was meant to save.
  useEffect(() => {
    if (shared && isTab(shared.tab)) setTab(shared.tab)
  }, [shared])

  const started = useRef(false)

  useEffect(() => {
    // Guarded because React runs effects twice in development, and instantiating a 20 MB module twice is a
    // visible stall.
    if (started.current) return
    started.current = true

    void (async () => {
      setLoaded('loading')
      try {
        await loadWasm()
        setLoaded('ready')
      } catch (e) {
        setLoadError(e instanceof Error ? e.message : String(e))
        setLoaded('failed')
      }
    })()
  }, [])

  function copyLink() {
    try {
      const link = shareLink({ tab, message, script, filter, steps }, window.location.href)

      // The address bar is updated either way: that is the real fallback, and it works with no clipboard
      // permission at all. Only the message changes, because claiming a copy that did not happen is how
      // somebody pastes the previous contents of their clipboard into a ticket.
      window.history.replaceState(null, '', link)
      void copyText(link).then((r) =>
        setShareState(r === 'copied' ? { kind: 'copied' } : { kind: 'uncopied' }),
      )
    } catch (e) {
      setShareState({
        kind: 'refused',
        detail: e instanceof TooLargeToShare ? e.message : e instanceof Error ? e.message : String(e),
      })
    }
  }

  function run() {
    if (loaded !== 'ready') return

    switch (tab) {
      case 'script':
        setScriptResult(window.perfuseScript?.(message, script) ?? null)
        setTranslated(window.perfuseTranslate?.(script) ?? null)
        break
      case 'filter':
        setFilterResult(window.perfuseFilter?.(message, filter) ?? null)
        break
      case 'transform':
        setTransformResult(window.perfuseTransform?.(message, steps) ?? null)
        break
      case 'parse':
        setParsed(window.perfuseParse?.(message) ?? null)
        break
    }
  }

  return (
    <div className="space-y-6">
      {/* The v3 picker sits here because this is the tab for working something out rather than
          configuring it. Unlike the rest of this page it does need the server - v3 parsing is not
          in the browser bundle - so it says so through its own loading state rather than borrowing
          this page's claim that nothing leaves the tab. */}
      <V3FieldPicker />

      <Section
        title="Try it here"
        description="The whole engine core is running in this tab — the parser, the filter language, the
        transformation steps, the script engine and the Mirth E4X translator. Nothing is sent anywhere:
        there is no server involved once the page has loaded, which is why it is safe to paste a real
        message."
      >
        {loaded === 'loading' && (
          <div className="flex items-center gap-3 text-slate-400">
            <Spinner />
            <span>
              Loading the engine (about 4 MB, once) — this is the actual Perfuse code, not a simulation.
            </span>
          </div>
        )}

        {loaded === 'failed' && (
          <div className="rounded-lg border border-rose-900/60 bg-rose-950/30 p-4 text-sm">
            <p className="font-medium text-rose-200">The engine could not be loaded.</p>
            <p className="mt-1 text-rose-300/80">{loadError}</p>
            {/*
              The hint is chosen from the failure rather than fixed.

              It used to say the build might not include the playground, which was the wrong cause twice over: the module
              is shipped in the binary, and the actual failure was our own Content Security Policy refusing to compile
              WebAssembly. Anyone reading that message would have gone looking for a missing file. A guess stated as a
              likely cause is worse than no hint, because it decides where someone looks first.
            */}
            <p className="mt-2 text-slate-400">
              {/^.*content security policy.*$/i.test(loadError ?? '')
                ? 'The browser blocked WebAssembly on security grounds. This is a server configuration problem, not a problem with your browser: the Content-Security-Policy header needs wasm-unsafe-eval in script-src.'
                : /mime|not executable|text\/html/i.test(loadError ?? '')
                  ? 'The engine files came back as HTML rather than as a script, which means they are not present in this build. They are produced by make wasm and embedded by make web.'
                  : 'The engine is a 20 MB WebAssembly module. On a slow connection it may simply not have finished; otherwise the message above is the browser’s own explanation.'}
            </p>
          </div>
        )}

        {loaded === 'ready' && (
          <div className="flex flex-wrap items-center gap-2">
            {(
              [
                ['script', 'Run a Mirth script'],
                ['filter', 'Try a filter'],
                ['transform', 'Transformation steps'],
                ['parse', 'Look inside a message'],
              ] as [Tab, string][]
            ).map(([id, label]) => (
              <button
                key={id}
                onClick={() => setTab(id)}
                className={`rounded-lg px-3 py-1.5 text-sm transition-colors ${
                  tab === id
                    ? 'bg-sky-600 text-white'
                    : 'bg-slate-800 text-slate-300 hover:bg-slate-700'
                }`}
              >
                {label}
              </button>
            ))}

            {/*
              Share, and a warning that is not optional.

              The link carries the contents of these boxes, and the whole point of this panel is that it is safe to paste a real
              message into it. Both of those are true at once, so somebody will paste a patient and then send the link - into a
              ticket, a chat, an email - which is a disclosure the tool invited.

              The fragment never reaches a server, which is why it is a fragment and not a query string, and that removes the
              logging half of the problem. It does not remove the part where a link lives in somebody's inbox forever. So the
              warning sits next to the button rather than in documentation nobody reads at the moment they need it.
            */}
            <div className="ml-auto flex items-center gap-3">
              <button
                onClick={copyLink}
                className="rounded-lg bg-slate-800 px-3 py-1.5 text-sm text-slate-200 transition-colors hover:bg-slate-700"
              >
                Copy a link to this
              </button>
            </div>
          </div>
        )}

        {loaded === 'ready' && shareState.kind === 'copied' && (
          <div className="mt-3 rounded-lg border border-amber-900/60 bg-amber-950/30 p-3 text-sm">
            <p className="font-medium text-amber-200">Link copied, and it contains everything in these boxes.</p>
            <p className="mt-1 text-amber-200/80">
              The message travels inside the link, after the # — so it never reaches a server or any log on the way. It
              does sit in whatever you paste it into. If there is a real patient in that message, treat the link the same
              way you would treat the message.
            </p>
          </div>
        )}

        {loaded === 'ready' && shareState.kind === 'uncopied' && (
          <div className="mt-3 rounded-lg border border-amber-900/60 bg-amber-950/30 p-3 text-sm">
            <p className="font-medium text-amber-200">
              The link is in the address bar — copy it from there.
            </p>
            <p className="mt-1 text-amber-200/80">
              This browser would not let the page write to the clipboard, which is normal when the console is
              reached over plain HTTP. The link itself is built and is in the address bar above, so it is
              already yours to copy. Everything in these boxes travels inside it, after the # — so it never
              reaches a server or any log on the way, but it does sit in whatever you paste it into.
            </p>
          </div>
        )}

        {loaded === 'ready' && shareState.kind === 'refused' && (
          <div className="mt-3 rounded-lg border border-rose-900/60 bg-rose-950/30 p-3 text-sm">
            <p className="font-medium text-rose-200">This session is too long to share as a link.</p>
            <p className="mt-1 text-rose-300/80">{shareState.detail}</p>
          </div>
        )}
      </Section>

      {loaded === 'ready' && (
        <div className="grid gap-5 lg:grid-cols-2">
          <div className="space-y-4">
            <Editor
              label="The message"
              language="hl7"
              hint="Line endings are fixed automatically — a textarea gives newlines and HL7 wants
              carriage returns, which would otherwise make every pasted message look broken."
              value={message}
              onChange={setMessage}
              rows={10}
            />

            {tab === 'script' && (
              <Editor
                label="The script"
                language="js"
                hint="Mirth E4X, pasted unchanged. This is the claim worth testing."
                value={script}
                onChange={setScript}
                rows={14}
              />
            )}
            {tab === 'filter' && (
              <Editor
                label="The filter"
                language="js"
                hint="Perfuse's own expression language. Try changing ADT to ORU."
                value={filter}
                onChange={setFilter}
                rows={4}
              />
            )}
            {tab === 'transform' && (
              <Editor
                label="The steps"
                hint="Declarative transformations, as JSON. These are what the graphical builder writes."
                value={steps}
                onChange={setSteps}
                rows={12}
              />
            )}

            <button
              onClick={run}
              className="w-full rounded-lg bg-sky-600 px-4 py-2.5 font-medium text-white
                hover:bg-sky-500"
            >
              Run it
            </button>
          </div>

          <div className="space-y-4">
            {tab === 'script' && <ScriptOutput result={scriptResult} translated={translated} />}
            {tab === 'filter' && <FilterOutput result={filterResult} />}
            {tab === 'transform' && <TransformOutput result={transformResult} />}
            {tab === 'parse' && <ParseOutput result={parsed} />}
          </div>
        </div>
      )}
    </div>
  )
}

function Editor({
  label,
  hint,
  value,
  onChange,
  rows,
  language,
}: {
  label: string
  hint?: string
  value: string
  onChange: (v: string) => void
  rows: number
  language?: CodeLanguage
}) {
  // The label is tied to the field, and the hint is announced with it.
  //
  // They were not connected at all: a bare <label> beside a bare <textarea>. On screen it reads correctly, and to a screen reader
  // there are four unlabelled text boxes on this panel with no way to tell the message from the script. It also meant nothing could
  // find a field by its name, which is how a test discovers it and how a person using voice control does.
  const id = useId()
  const hintID = `${id}-hint`

  return (
    <div>
      <label htmlFor={id} className="block text-sm font-medium text-slate-300">
        {label}
      </label>
      {hint && (
        <p id={hintID} className="mt-0.5 text-xs text-slate-500">
          {hint}
        </p>
      )}
      {/*
        Coloured, and still a real textarea underneath. These boxes hold an HL7 message, a Mirth script and a filter expression, and
        all three were monochrome - the two things this tab exists to show off were the two least readable things on the page.
        CodeArea rather than the CodeMirror editor because this tab promises that nothing is sent anywhere once the page has
        loaded, and that editor asks the server to compile.
      */}
      <CodeArea
        id={id}
        aria-describedby={hint ? hintID : undefined}
        value={value}
        onChange={onChange}
        language={language}
        rows={rows}
        className="mt-2 focus-within:border-sky-500"
      />
    </div>
  )
}

function Failure({ error, stage }: { error: string; stage?: string }) {
  // The stage matters: a compile error is a typo in the script, an evaluation error is a fact about the
  // message, and the fix is completely different.
  const explain: Record<string, string> = {
    compile: 'The script did not compile. This is a syntax problem, before the message was touched.',
    message: 'The message could not be parsed, so nothing was run against it.',
    run: 'The script compiled and then failed while running.',
    parse: 'The expression could not be parsed. This is a typo in the filter, not a fact about the message.',
    evaluate: 'The expression parsed but could not be evaluated against this message.',
    apply: 'A transformation step failed while being applied.',
    encode: 'The result could not be written back out as HL7.',
  }

  return (
    <div className="rounded-lg border border-rose-900/60 bg-rose-950/30 p-4">
      {stage && explain[stage] && <p className="text-sm text-rose-200">{explain[stage]}</p>}
      <pre className="mt-1 whitespace-pre-wrap font-mono text-xs text-rose-300/90">{error}</pre>
    </div>
  )
}

function ScriptOutput({
  result,
  translated,
}: {
  result: ScriptResult | null
  translated: TranslateResult | null
}) {
  if (!result) {
    return <Idle text="Press “Run it” and the script will run against the message on the left." />
  }

  return (
    <div className="space-y-4">
      {/* The translation comes first, because "nothing needed changing" is the interesting result and it
          is what the whole migration argument rests on. */}
      {translated && !translated.error && (
        <div
          className="rounded-lg border p-4"
          style={
            translated.unchanged
              ? { borderColor: 'rgba(52,211,153,.4)', background: 'rgba(52,211,153,.06)' }
              : { borderColor: 'rgba(56,189,248,.4)', background: 'rgba(56,189,248,.06)' }
          }
        >
          <p className="text-sm font-medium text-slate-200">
            {translated.unchanged
              ? 'This script needed no translation at all — it ran exactly as written.'
              : `Mirth E4X was translated in ${translated.notes?.length ?? 0} place(s). The script itself was not rewritten.`}
          </p>
          {!translated.unchanged && translated.notes && translated.notes.length > 0 && (
            <ul className="mt-2 space-y-1 text-xs text-slate-400">
              {translated.notes.map((n, i) => (
                <li key={i}>
                  line {n.line}: {n.kind} — {n.message}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {result.error ? (
        <Failure error={result.error} stage={result.stage} />
      ) : (
        <Output label="The message afterwards" body={result.output ?? ''} />
      )}

      {result.logs && result.logs.length > 0 && (
        <div>
          <p className="text-sm font-medium text-slate-300">What the script logged</p>
          <ul className="mt-2 space-y-1">
            {result.logs.map((l, i) => (
              <li key={i} className="font-mono text-xs">
                <span className="text-slate-500">{l.level}</span>{' '}
                <span className="text-slate-300">{l.message}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

function FilterOutput({ result }: { result: FilterResult | null }) {
  if (!result) return <Idle text="Press “Run it” to see whether this message passes." />
  if (result.error) return <Failure error={result.error} stage={result.stage} />

  return (
    <div className="space-y-4">
      <div
        className="rounded-lg border p-5 text-center"
        style={
          result.passed
            ? { borderColor: 'rgba(52,211,153,.4)', background: 'rgba(52,211,153,.06)' }
            : { borderColor: 'rgba(148,163,184,.3)', background: 'rgba(148,163,184,.05)' }
        }
      >
        <p className="text-2xl font-semibold text-slate-100">
          {result.passed ? 'Passes' : 'Filtered out'}
        </p>
        <p className="mt-1 text-sm text-slate-400">
          {result.passed
            ? 'This message would be processed and delivered.'
            : 'This message would be recorded as filtered — which counts as a success, not a failure.'}
        </p>
      </div>

      {/* The canonical form is how somebody learns the precedence of and/or without reading a grammar. */}
      {result.canonical && (
        <div>
          <p className="text-sm font-medium text-slate-300">How it was understood</p>
          <SyntaxBlock code={result.canonical} language="hl7" className="mt-1" />
        </div>
      )}

      {result.paths && result.paths.length > 0 && (
        <div>
          <p className="text-sm font-medium text-slate-300">Fields it reads</p>
          <div className="mt-2 flex flex-wrap gap-1.5">
            {result.paths.map((p) => (
              <span
                key={p}
                className="rounded bg-slate-800 px-2 py-0.5 font-mono text-xs text-slate-300"
              >
                {p}
              </span>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function TransformOutput({ result }: { result: TransformResult | null }) {
  if (!result) return <Idle text="Press “Run it” to apply the steps." />
  if (result.error) return <Failure error={result.error} stage={result.stage} />

  return (
    <div className="space-y-4">
      {/* Showing what changed, not only the result. A step that silently did nothing is the commonest
          confusion with declarative transformations, and this makes it visible instead of leaving somebody
          to diff two messages by eye. */}
      <div>
        <p className="text-sm font-medium text-slate-300">
          {result.changes && result.changes.length > 0
            ? `${result.changes.length} change(s)`
            : 'Nothing changed — every step matched nothing, or found the value already correct.'}
        </p>
        {result.changes && result.changes.length > 0 && (
          <ul className="mt-2 space-y-1.5">
            {result.changes.map((c, i) => (
              <li key={i} className="rounded-lg bg-slate-950 p-2.5 font-mono text-xs">
                <span className="text-slate-500">{c.path}</span>{' '}
                <span className="text-rose-300/80 line-through">{c.from || '(empty)'}</span>{' '}
                <span className="text-slate-500">→</span>{' '}
                <span className="text-emerald-300">{c.to || '(empty)'}</span>
                {c.note && <span className="ml-2 text-amber-300/80">{c.note}</span>}
              </li>
            ))}
          </ul>
        )}
      </div>

      <Output label="The message afterwards" body={result.output ?? ''} />
    </div>
  )
}

function ParseOutput({ result }: { result: ParseResult | null }) {
  if (!result) return <Idle text="Press “Run it” to see the structure." />
  if (result.error) return <Failure error={result.error} stage="message" />

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Fact label="Type" value={`${result.messageType ?? ''}^${result.event ?? ''}`} />
        <Fact label="Structure" value={result.structure ?? '—'} />
        <Fact label="Control ID" value={result.controlId ?? '—'} />
        <Fact label="Version" value={result.version ?? '—'} />
      </div>

      <div className="space-y-2">
        {result.segments?.map((seg) => (
          <details key={seg.name} className="rounded-lg border border-slate-800 bg-slate-900/40">
            <summary className="cursor-pointer px-3 py-2 font-mono text-sm text-slate-200">
              {seg.name}
              <span className="ml-2 text-xs text-slate-500">{seg.fields.length} fields</span>
            </summary>
            <ul className="border-t border-slate-800 px-3 py-2">
              {seg.fields.map((f) => (
                <li key={f.path} className="flex gap-3 py-0.5 font-mono text-xs">
                  <span className="w-24 shrink-0 text-slate-500">{f.path}</span>
                  {/* Present-but-empty is shown as its own thing, because filters turn on exactly that
                      difference and this is where somebody learns it. */}
                  <span className={f.empty ? 'italic text-slate-400' : 'text-slate-300'}>
                    {f.empty ? '(present, empty)' : f.value}
                  </span>
                </li>
              ))}
            </ul>
          </details>
        ))}
      </div>
    </div>
  )
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg bg-slate-900/60 p-3">
      <p className="text-xs text-slate-500">{label}</p>
      <p className="mt-0.5 truncate font-mono text-sm text-slate-200">{value}</p>
    </div>
  )
}

function Output({ label, body }: { label: string; body: string }) {
  return (
    <div>
      <p className="text-sm font-medium text-slate-300">{label}</p>
      <pre className="mt-1 max-h-80 overflow-auto rounded-lg bg-slate-950 p-3 font-mono text-xs
        leading-relaxed text-slate-300">
        {/* Carriage returns are shown as newlines so the output is readable. The bytes are unchanged. */}
        {body.replace(/\r/g, '\n')}
      </pre>
    </div>
  )
}

function Idle({ text }: { text: string }) {
  return (
    <div className="rounded-lg border border-dashed border-slate-800 p-8 text-center text-sm
      text-slate-500">
      {text}
    </div>
  )
}

/**
 * Loads the WebAssembly module.
 *
 * wasm_exec.js is injected as a script tag rather than imported, because it is a Go distribution file that
 * assigns to globals and is not a module. Copying it into the bundle would mean keeping it in step with the
 * compiler by hand, and a mismatched pair fails at instantiation with an error that explains nothing.
 */
async function loadWasm(): Promise<void> {
  if (window.perfuseScript) return

  if (!window.Go) {
    await new Promise<void>((resolve, reject) => {
      const tag = document.createElement('script')
      tag.src = '/wasm/wasm_exec.js'
      tag.onload = () => resolve()
      tag.onerror = () => reject(new Error('could not load /wasm/wasm_exec.js'))
      document.head.appendChild(tag)
    })
  }

  const GoRuntime = window.Go
  if (!GoRuntime) throw new Error('the Go WebAssembly runtime did not load')

  const go = new GoRuntime()

  const response = await fetch('/wasm/perfuse.wasm')
  if (!response.ok) {
    throw new Error(`/wasm/perfuse.wasm returned ${response.status}`)
  }

  // Streaming instantiation, so the module starts compiling while it is still downloading. On a 20 MB
  // module that is the difference between a pause and a stall.
  const { instance } = await WebAssembly.instantiateStreaming(response, go.importObject)

  // The module blocks forever in main, so run() must not be awaited.
  void go.run(instance)

  // main installs the functions and then dispatches an event. Waiting for the function to appear is more
  // robust than waiting for the event, since the event may already have fired by the time this runs.
  const deadline = Date.now() + 15000
  while (!window.perfuseScript) {
    if (Date.now() > deadline) {
      throw new Error('the engine loaded but never became ready')
    }
    await new Promise((r) => setTimeout(r, 25))
  }
}
