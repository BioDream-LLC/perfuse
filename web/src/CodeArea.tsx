import { useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { SyntaxInline, type CodeLanguage } from './SyntaxHighlight'

/**
 * CodeArea is a textarea that shows its content coloured.
 *
 * # Why not the CodeMirror editor
 *
 * Two reasons, and they are specific rather than a matter of taste.
 *
 * The Playground's entire claim is that nothing is sent anywhere once the page has loaded - the parser, the filter language and the
 * script engine all run in the tab. The CodeMirror editor asks the server to compile on every pause, which is exactly right in the
 * channel builder and would quietly make the Playground's promise untrue.
 *
 * And most of these boxes hold a message rather than a script: HL7 v2, a CDA document, FHIR JSON, an X12 interchange. A message needs
 * colour, not completion, a linter or a compile round trip. This file already had a tokenizer for all four and used it only for
 * read-only display.
 *
 * # How it works, and the one invariant
 *
 * A coloured pre sits underneath a transparent textarea, aligned character for character. The textarea keeps every behaviour a person
 * expects and every behaviour a test relies on - selection, undo, spellcheck off, and a real form control that a label can point at,
 * which is what CodeMirror cost us elsewhere.
 *
 * The alignment holds only while tokenising reproduces its input exactly. SyntaxRoundTrip.test.ts asserts that for every language,
 * and it found two existing bugs doing so: X12 threw away whitespace-only segments and XML dropped the opening bracket of a
 * declaration. Both were invisible in read-only use and would have slid the colours out of step with the text here.
 *
 * A trailing newline gets a space added to the shadow copy, because content ending in a newline renders one line shorter than the
 * textarea believes it is, and the two then scroll out of step at the bottom of the box.
 *
 * The coloured layer is a div rather than a pre, which looks like the wrong element and is not. Every browser test in this repository
 * reads the generated channel file with locator("pre").first(), so a pre inside every editable box would silently become the first
 * pre on the page and those tests would start reading a shadow copy of whatever box happened to be above it. The white-space
 * behaviour comes from a class either way, so the tag buys nothing and costs that. It carries data-code-shadow for anything that
 * genuinely wants to find it.
 */
export function CodeArea({
  value,
  onChange,
  language,
  rows = 8,
  placeholder,
  readOnly,
  spellCheck = false,
  className = '',
  ...rest
}: {
  value: string
  onChange?: (next: string) => void
  language?: CodeLanguage
  rows?: number
  placeholder?: string
  readOnly?: boolean
  spellCheck?: boolean
  className?: string
} & Omit<
  React.TextareaHTMLAttributes<HTMLTextAreaElement>,
  'value' | 'onChange' | 'rows' | 'placeholder' | 'readOnly' | 'spellCheck' | 'className'
>) {
  const textarea = useRef<HTMLTextAreaElement | null>(null)
  const shadow = useRef<HTMLDivElement | null>(null)
  const [scroll, setScroll] = useState({ top: 0, left: 0 })

  // The shadow copy follows the textarea's scrolling. Without this the colours stay put while the text moves.
  const sync = useCallback(() => {
    const el = textarea.current
    if (!el) return

    setScroll({ top: el.scrollTop, left: el.scrollLeft })
  }, [])

  useLayoutEffect(() => {
    const el = shadow.current
    if (!el) return

    el.scrollTop = scroll.top
    el.scrollLeft = scroll.left
  }, [scroll])

  // A pre whose content ends in a newline renders one line shorter than the textarea believes it is, so the two scroll out of step
  // at the bottom of the box. One trailing space costs nothing and is never seen.
  const painted = useMemo(() => (value.endsWith('\n') ? value + ' ' : value), [value])

  return (
    // data-code-area marks this as a code box rather than prose.
    //
    // Inside one, colour is a token type: red means an XML element name or an HL7 segment identifier and carries no claim
    // that anything is wrong. Outside one, red means something is wrong right now. A sweep over every screen checks that
    // nothing opens red at the user, and it needs to be able to tell the two vocabularies apart - the highlighter emits
    // bare spans with no pre or code wrapper to recognise, so the boundary is marked here.
    <div
      data-code-area=""
      className={`relative overflow-hidden rounded-lg border border-slate-700 bg-slate-950 ${className}`}
    >
      {/*
        The coloured layer. Hidden from screen readers: it is a duplicate of the textarea's own value, so announcing it would read
        every message twice, and the textarea is the thing that carries the accessible name.
      */}
      <div
        ref={shadow}
        aria-hidden="true"
        data-code-shadow="true"
        className="pointer-events-none absolute inset-0 m-0 overflow-hidden p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words"
      >
        <SyntaxInline code={painted} language={language} />
      </div>

      {/*
        The real control, transparent but for its caret. text-transparent rather than opacity, so the caret and the selection stay
        visible - an invisible caret in a message box is worse than no highlighting at all.
      */}
      <textarea
        ref={textarea}
        value={value}
        rows={rows}
        placeholder={placeholder}
        readOnly={readOnly}
        spellCheck={spellCheck}
        onChange={(e) => onChange?.(e.target.value)}
        onScroll={sync}
        // In normal flow, so rows decides the height of the whole box. The coloured layer is the absolute one: an empty box would
        // otherwise collapse to nothing, since a pre containing no text has no height to give the container.
        className="relative block w-full resize-y overflow-auto bg-transparent p-3 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap text-transparent caret-slate-100 outline-none selection:bg-sky-500/30 placeholder:text-slate-600"
        {...rest}
      />
    </div>
  )
}
