import { SyntaxBlock } from './SyntaxHighlight'
import { useEffect, useMemo, useRef, useState } from 'react'
import { EditorView, basicSetup } from 'codemirror'
import { EditorState, type Extension } from '@codemirror/state'
import { javascript } from '@codemirror/lang-javascript'
import { StreamLanguage } from '@codemirror/language'
import { lua } from '@codemirror/legacy-modes/mode/lua'
import { linter, lintGutter, type Diagnostic } from '@codemirror/lint'
import {
  autocompletion,
  type CompletionContext,
  type CompletionResult,
  type Completion,
} from '@codemirror/autocomplete'
import { oneDark } from '@codemirror/theme-one-dark'
import { api, type DictSegment, type ScriptCheck } from './api'

// A script editor that knows what an HL7 message is.
//
// CodeMirror rather than Monaco. Monaco is the fancier answer and adds something over five
// megabytes to a bundle that is currently under four hundred kilobytes, all of which is
// embedded in the binary. CodeMirror gives the same things that matter here - highlighting,
// completion, a lint gutter, bracket matching, multi-cursor - for a fraction of that.
//
// Three things make it more than a coloured textarea:
//
//  1. Diagnostics come from the real compiler. The server runs the same goja compile and
//     the same E4X translation the channel will, so a script that checks clean here
//     compiles at deploy. Mirth makes you deploy to find out.
//  2. Completion is driven by the HL7 dictionary. PID-7 is the date of birth and PID-8 is
//     the sex; they are one keystroke apart, and a half-remembered field number is how the
//     wrong one reaches production.
//  3. The E4X translation is shown. A script silently rewritten and then behaving
//     differently is the worst kind of migration bug, and showing the rewrite makes it a
//     comparison instead of an investigation.

// The seven slots a script can occupy. The workbench offered two of them for months, which made it the wrong shape of tool for the
// job it was built for: a Mirth migration is full of preprocessors and deploy hooks, not only transformers.
//
// The kind is not a label. It decides the wrapper the source is compiled inside, so checking a script as the wrong kind answers a
// question nobody asked - a writer's return value is discarded as a transformer, and a lifecycle script is told there is no message.
export type ScriptKind =
  | 'preprocessor'
  | 'filter'
  | 'transformer'
  | 'writer'
  | 'postprocessor'
  | 'lifecycle'
  | 'reader'

interface Props {
  value: string
  onChange: (next: string) => void
  kind: ScriptKind

  /** language selects the compiler. Absent means javascript. */
  language?: 'javascript' | 'lua'
  /** Disables editing without hiding the content. */
  readOnly?: boolean
  /** Rows of height, roughly. */
  minHeight?: string

  /**
   * ariaLabel names the editor for a screen reader.
   *
   * CodeMirror renders a div, not a textarea, so a surrounding <label> cannot be associated with it - pointing a label's for
   * attribute at a div looks associated and is not. The editable element carries role="textbox", so it can be named directly, and
   * this is how. It matters most where several editors share a page: the channel builder has six, and six controls all announced
   * as "edit box" is not usable by anyone who cannot see the headings above them.
   */
  ariaLabel?: string
}

/** How long to wait after typing before asking the server to compile. */
//
// Long enough that a fast typist is not compiling every character, short enough that the
// answer feels immediate when they pause. Cancelled and restarted on every keystroke.
/**
 * sharedDictionary fetches the segment dictionary at most once per page load.
 *
 * Held as a promise rather than a value so that editors mounting together share one request instead of racing. A failure is not
 * cached: completion degrades quietly, and a later editor should be free to try again rather than inherit a failure it did not cause.
 */
let dictionaryOnce: Promise<DictSegment[]> | undefined

function sharedDictionary(): Promise<DictSegment[]> {
  if (!dictionaryOnce) {
    dictionaryOnce = api
      .dictionary()
      .then((res) => res.all ?? [])
      .catch((err) => {
        dictionaryOnce = undefined
        throw err
      })
  }

  return dictionaryOnce
}

const CHECK_DEBOUNCE_MS = 400

export function ScriptEditor({
  value,
  onChange,
  kind,
  language = 'javascript',
  readOnly,
  ariaLabel,
  minHeight = '18rem',
}: Props) {
  const host = useRef<HTMLDivElement | null>(null)
  const view = useRef<EditorView | null>(null)

  const [check, setCheck] = useState<ScriptCheck | null>(null)
  const [checking, setChecking] = useState(false)
  const [dict, setDict] = useState<DictSegment[]>([])
  const [showTranslation, setShowTranslation] = useState(false)

  // The latest diagnostics, read by the linter callback. Kept in a ref because the
  // extension is created once and must not close over a stale state value.
  const diagnostics = useRef<Diagnostic[]>([])

  // The dictionary, fetched once per page rather than once per editor.
  //
  // This said "once for the lifetime of the pane", which was true when one pane existed. The channel builder renders six editors,
  // so it became six identical requests for the whole segment dictionary on every visit to the scripts section - the same payload,
  // fetched concurrently, discarded five times. Shared through a module-level promise instead, which also means the second editor
  // gets its completions without waiting.
  useEffect(() => {
    let live = true
    sharedDictionary()
      .then((all) => {
        if (live) setDict(all)
      })
      .catch(() => {
        // Completion degrades to plain JavaScript. Worth no error banner: the editor
        // still works, and a red box over a working editor teaches people to ignore
        // red boxes.
      })
    return () => {
      live = false
    }
  }, [])

  // Completions are held in a ref and read live, the way diagnostics already are.
  //
  // They used to be a rebuild dependency, which meant the editor was thrown away and recreated when the dictionary arrived a moment
  // after mount. A new EditorView starts from the value in props, and props lag the editor by one React update - so anything typed in
  // that window was silently discarded. One editor made this rare enough to never see. Six made it reproducible: filling the slots in
  // order lost the first one every time.
  //
  // Nothing about a completion list requires reconstructing an editor, so this is also simply the right shape.
  const completions = useMemo(() => buildCompletions(dict), [dict])
  const completionsRef = useRef(completions)
  completionsRef.current = completions

  // Compile on a debounce.
  useEffect(() => {
    // An empty slot is not a script and does not need compiling. The endpoint agrees - it returns OK for empty source - but asking
    // it means six requests every time the builder's scripts section is opened, none of which can say anything. Any previous
    // verdict is cleared, so an emptied box does not keep reporting on the text that used to be in it.
    if (value.trim() === '') {
      setCheck(null)
      setChecking(false)
      diagnostics.current = []

      return
    }

    let live = true
    setChecking(true)

    const timer = window.setTimeout(() => {
      api
        .checkScript(value, kind, language)
        .then((res) => {
          if (!live) return
          setCheck(res)
          diagnostics.current = toDiagnostics(res, value)
          // Ask the linter to re-run now that the diagnostics have changed.
          const v = view.current
          if (v) v.dispatch({})
        })
        .catch(() => {
          if (live) setCheck(null)
        })
        .finally(() => {
          if (live) setChecking(false)
        })
    }, CHECK_DEBOUNCE_MS)

    return () => {
      live = false
      window.clearTimeout(timer)
    }
  }, [value, kind, language])

  // Build the editor once.
  useEffect(() => {
    if (!host.current) return

    const extensions: Extension[] = [
      basicSetup,
      // The mode has to follow the language. JavaScript highlighting over Lua is not merely unhelpful, it is wrong:
      // it reads -- as a decrement rather than a comment, and knows nothing of ~= or end. Somebody would trust the
      // colours and conclude a correct comment was broken code.
      language === 'lua' ? StreamLanguage.define(lua) : javascript(),
      oneDark,
      lintGutter(),
      linter(() => diagnostics.current),
      autocompletion({ override: [(ctx) => completeHL7(ctx, completionsRef.current)] }),
      EditorView.updateListener.of((update) => {
        if (update.docChanged) onChange(update.state.doc.toString())
      }),
      EditorView.editable.of(!readOnly),
      ...(ariaLabel ? [EditorView.contentAttributes.of({ 'aria-label': ariaLabel })] : []),
      EditorView.theme({
        '&': { minHeight, fontSize: '13px' },
        '.cm-scroller': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' },
      }),
    ]

    const v = new EditorView({
      state: EditorState.create({ doc: value, extensions }),
      parent: host.current,
    })
    view.current = v

    return () => {
      v.destroy()
      view.current = null
    }
    // Deliberately not depending on value: the editor owns its document once created,
    // and re-creating it on every keystroke would lose the cursor.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [readOnly, minHeight, language, ariaLabel])

  return (
    <div className="space-y-2">
      <div className="overflow-hidden rounded-lg border border-slate-700" ref={host} />

      <StatusLine check={check} checking={checking} kind={kind} />

      {check?.rewritten && (
        <div className="rounded-lg border border-amber-700/50 bg-amber-950/30 p-3 text-xs">
          <div className="flex items-center justify-between">
            <span className="font-medium text-amber-200">
              Mirth E4X syntax was translated to run here
            </span>
            <button
              type="button"
              className="text-amber-300 underline hover:text-amber-100"
              onClick={() => setShowTranslation((s) => !s)}
            >
              {showTranslation ? 'hide' : 'show what runs'}
            </button>
          </div>

          <ul className="mt-2 space-y-1 text-amber-100/80">
            {check.notes.map((n, i) => (
              <li key={i}>
                <span className="text-amber-400">line {n.line}</span> · {n.kind} — {n.detail}
              </li>
            ))}
          </ul>

          {showTranslation && (
            <SyntaxBlock code={check.translated ?? ''} language="js" maxHeight="18rem" className="mt-3" />
          )}
        </div>
      )}
    </div>
  )
}

function StatusLine({
  check,
  checking,
  kind,
}: {
  check: ScriptCheck | null
  checking: boolean
  kind: ScriptKind
}) {
  if (checking && !check) {
    return <p className="text-xs text-slate-500">checking…</p>
  }
  if (!check) {
    return <p className="text-xs text-slate-500">not checked</p>
  }
  if (check.error) {
    return (
      <p className="text-xs text-rose-400">
        {check.error.line > 0 && <span className="font-medium">line {check.error.line}: </span>}
        {check.error.message}
      </p>
    )
  }
  return (
    <p className="text-xs text-emerald-400">
      compiles as a {kind}
      {checking && <span className="text-slate-500"> · rechecking…</span>}
    </p>
  )
}

/** toDiagnostics turns one compile error into a CodeMirror marker. */
//
// The offsets are computed from the reported line and column, and clamped to the document.
// A marker placed past the end silently disappears, which would look like the error had
// gone away.
export function toDiagnostics(check: ScriptCheck, source: string): Diagnostic[] {
  if (!check.error) return []

  const { line, column, message } = check.error
  if (!line || line < 1) {
    // No position: mark the first line rather than nothing, so the gutter still shows
    // that something is wrong.
    return [{ from: 0, to: Math.min(source.length, 1), severity: 'error', message }]
  }

  const lines = source.split('\n')
  if (line > lines.length) {
    return [{ from: 0, to: Math.min(source.length, 1), severity: 'error', message }]
  }

  let offset = 0
  for (let i = 0; i < line - 1; i++) offset += lines[i]!.length + 1

  const lineText = lines[line - 1] ?? ''
  let from = Math.min(offset + Math.max(0, (column ?? 1) - 1), source.length)
  let to = Math.min(offset + lineText.length, source.length)

  // A zero-width range does not render, so it has to be widened - but widening forwards
  // can run past the end of the document, and CodeMirror will not accept a range it cannot
  // resolve. Widen backwards instead, because a position at the end of the document has
  // nothing in front of it to cover.
  if (to <= from) {
    from = Math.max(0, Math.min(from, source.length - 1))
    to = Math.min(source.length, from + 1)
  }

  return [{ from, to, severity: 'error', message }]
}

interface CompletionSet {
  segments: Completion[]
  fieldsBySegment: Map<string, Completion[]>
}

/** buildCompletions turns the dictionary into two completion lists. */
export function buildCompletions(dict: DictSegment[]): CompletionSet {
  const segments: Completion[] = []
  const fieldsBySegment = new Map<string, Completion[]>()

  for (const seg of dict) {
    segments.push({
      label: seg.segment,
      type: 'class',
      detail: seg.description,
    })

    const fields: Completion[] = []
    for (const f of seg.fields) {
      // Labelled the way it is written in a script - PID.5 - rather than the way it is
      // written in a ticket - PID-5. Completing into the wrong notation would be worse
      // than not completing.
      const label = `${seg.segment}.${f.number}`
      const parts = [f.name]
      if (f.repeats) parts.push('repeats')
      if (f.table) parts.push(`table ${f.table}`)

      fields.push({
        label,
        type: 'property',
        detail: parts.join(' · '),
        info: f.components?.length ? `components: ${f.components.join(', ')}` : f.description,
      })
    }
    fieldsBySegment.set(seg.segment, fields)
  }

  return { segments, fieldsBySegment }
}

/** completeHL7 offers segment and field names inside a bracket subscript. */
//
// Scoped to what is actually inside quotes in a subscript, because offering fifty HL7
// segment names while somebody types a variable name would make the editor worse than one
// with no completion at all.
export function completeHL7(
  ctx: CompletionContext,
  sets: CompletionSet,
): CompletionResult | null {
  // Match a quoted string that is the subject of a bracket subscript: msg['PI
  const inSubscript = ctx.matchBefore(/\[\s*['"]([A-Za-z0-9._]*)$/)

  // And the Lua form, which is a call rather than a subscript: msg.child("PI
  //
  // Named accessors rather than any call, for the reason in the note above: a quote inside an arbitrary function call is
  // usually not a segment name, and offering fifty of them there would make the editor worse than one with no completion.
  //
  // child, children and ensure only. get and set are deliberately absent: those are the accessors for the path-addressed
  // formats, where a path is a loop name or a column heading, and offering HL7 segments would be confidently wrong.
  const inAccessor = ctx.matchBefore(/\.(?:child|children|ensure)\s*\(\s*['"]([A-Za-z0-9._]*)$/)

  const hit = inSubscript ?? inAccessor
  if (!hit) return null

  // Everything up to and including the opening quote is punctuation, whichever form matched. The character class above
  // excludes quotes, so the greedy match cannot run past the one that opened this string.
  const typed = hit.text.replace(/^.*['"]/, '')
  const from = ctx.pos - typed.length

  // A dot means a field is being named within a segment, so only that segment's fields
  // are relevant. Anything else is a segment name.
  const dot = typed.indexOf('.')
  if (dot > 0) {
    const segment = typed.slice(0, dot).toUpperCase()
    const fields = sets.fieldsBySegment.get(segment)
    if (!fields || fields.length === 0) return null
    return { from, options: fields, validFor: /^[A-Za-z0-9._]*$/ }
  }

  if (sets.segments.length === 0) return null
  return { from, options: sets.segments, validFor: /^[A-Za-z0-9._]*$/ }
}
