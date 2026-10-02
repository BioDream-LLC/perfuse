import { useMemo } from 'react'

/**
 * Lightweight syntax highlighting for read-only data display.
 *
 * This deliberately does NOT use CodeMirror for viewing. CodeMirror is 700KB and
 * intended for editing. For read-only display of HL7, XML, JSON, and X12, a simple
 * regex-based highlighter is smaller, faster, and sufficient.
 *
 * Colours match the One Dark palette that CodeMirror uses in the script editor,
 * so the whole app feels consistent.
 */

// One Dark palette (subset).
const C = {
  tag: 'text-rose-400',        // XML tags, HL7 segment IDs
  attr: 'text-amber-300',      // XML attributes, field positions
  string: 'text-emerald-300',  // String values, quoted content
  number: 'text-purple-300',   // Numbers
  key: 'text-sky-300',         // JSON keys, element names
  punct: 'text-slate-500',     // Delimiters, brackets
  // slate-500, not slate-600. The contrast sweep measured comments at 2.66:1 against the editor background, which is below the line
  // it enforces and, more to the point, genuinely hard to read - and a comment is often the part of a pasted Mirth script that
  // explains what the thing is for. De-emphasised is the intent; unreadable is not.
  comment: 'text-slate-500',   // Comments
  keyword: 'text-purple-400',  // Keywords, booleans
  value: 'text-slate-200',     // General values
}

/** The content types this file can colour. */
export type CodeLanguage = 'xml' | 'json' | 'hl7' | 'x12' | 'js' | 'yaml' | 'text'

type Token = { cls: string; text: string }

function highlightXML(src: string): Token[] {
  const tokens: Token[] = []
  // The trailing `|<` matters. A processing instruction or a doctype - <?xml ... ?>, <!DOCTYPE ...> - matched none of the
  // alternatives, so the engine advanced past the opening angle bracket without emitting it, and every XML document displayed here
  // was missing the first character of its declaration. Anything that reaches the end without matching is now emitted verbatim
  // instead of being stepped over, which is the only shape of this loop that cannot lose input.
  const re = /(<!--[\s\S]*?-->)|(<!\[CDATA\[[\s\S]*?\]\]>)|(<[?!][^>]*>)|(<\/?)(\w[\w.:-]*)([^>]*?)(\/?>)|([^<]+)|</g
  let m: RegExpExecArray | null

  while ((m = re.exec(src)) !== null) {
    if (m[1]) {
      // Comment
      tokens.push({ cls: C.comment, text: m[1] })
    } else if (m[2]) {
      // CDATA
      tokens.push({ cls: C.string, text: m[2] })
    } else if (m[3]) {
      // A processing instruction or a doctype, coloured as one piece. The declaration's own attributes are not worth picking
      // apart, and the point of handling it here is that the characters survive at all.
      tokens.push({ cls: C.keyword, text: m[3] })
    } else if (m[4]) {
      // Tag
      tokens.push({ cls: C.punct, text: m[4] })
      tokens.push({ cls: C.tag, text: m[5] ?? '' })
      // Attributes
      if (m[6]) {
        const attrRe = /(\s+)([\w.:-]+)(=)("([^"]*?)"|'([^']*?)')?/g
        let a: RegExpExecArray | null
        let lastIdx = 0
        const attrStr = m[6]
        while ((a = attrRe.exec(attrStr)) !== null) {
          if (a.index > lastIdx) tokens.push({ cls: C.value, text: attrStr.slice(lastIdx, a.index) })
          tokens.push({ cls: '', text: a[1] ?? '' })
          tokens.push({ cls: C.attr, text: a[2] ?? '' })
          tokens.push({ cls: C.punct, text: a[3] ?? '' })
          if (a[4]) tokens.push({ cls: C.string, text: a[4] })
          lastIdx = a.index + a[0].length
        }
        if (lastIdx < attrStr.length) tokens.push({ cls: '', text: attrStr.slice(lastIdx) })
      }
      tokens.push({ cls: C.punct, text: m[7] ?? '' })
    } else if (m[8]) {
      // Text content
      tokens.push({ cls: C.value, text: m[8] })
    } else {
      // A bare angle bracket that began nothing recognisable. Emitted so the loop cannot lose input, which is the property the
      // overlay depends on and the reason this branch exists at all.
      tokens.push({ cls: C.punct, text: m[0] })
    }

    if (m[0] === '') re.lastIndex += 1
  }
  return tokens
}

function highlightJSON(src: string): Token[] {
  const tokens: Token[] = []
  const re = /("(?:\\.|[^"\\])*")\s*(:)|("(?:\\.|[^"\\])*")|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|(\btrue\b|\bfalse\b|\bnull\b)|([{}[\]:,])|(\s+)|(.)/g
  let m: RegExpExecArray | null

  while ((m = re.exec(src)) !== null) {
    if (m[1]) {
      tokens.push({ cls: C.key, text: m[1] })
      tokens.push({ cls: C.punct, text: m[2] ?? ':' })
    } else if (m[3]) {
      tokens.push({ cls: C.string, text: m[3] })
    } else if (m[4]) {
      tokens.push({ cls: C.number, text: m[4] })
    } else if (m[5]) {
      tokens.push({ cls: C.keyword, text: m[5] })
    } else if (m[6]) {
      tokens.push({ cls: C.punct, text: m[6] })
    } else {
      tokens.push({ cls: '', text: m[0] })
    }
  }
  return tokens
}

function highlightHL7(src: string): Token[] {
  const tokens: Token[] = []
  const lines = src.split('\n')

  for (let li = 0; li < lines.length; li++) {
    if (li > 0) tokens.push({ cls: '', text: '\n' })
    const line = lines[li]
    if (!line) continue

    // Segment ID (first 3 chars)
    const pipeIdx = line.indexOf('|')
    if (pipeIdx > 0 && pipeIdx <= 4) {
      tokens.push({ cls: C.tag, text: line.slice(0, pipeIdx) })
      // Rest of the line — highlight delimiters
      const rest = line.slice(pipeIdx)
      const parts = rest.split(/([|^~\\&])/g)
      for (const part of parts) {
        if (part === '|' || part === '^' || part === '~' || part === '\\' || part === '&') {
          tokens.push({ cls: C.punct, text: part })
        } else if (part) {
          tokens.push({ cls: C.value, text: part })
        }
      }
    } else {
      tokens.push({ cls: C.value, text: line })
    }
  }
  return tokens
}

function highlightX12(src: string): Token[] {
  const tokens: Token[] = []
  // Detect segment terminator (usually ~)
  const terminator = src.includes('~') ? '~' : '\n'
  const segments = src.split(terminator)

  for (let si = 0; si < segments.length; si++) {
    if (si > 0) tokens.push({ cls: C.punct, text: terminator })
    const seg = segments[si]
    if (seg === undefined) continue

    // Emitted rather than skipped. This said `continue` on a blank segment, which threw the characters away: whitespace between
    // two terminators vanished from the display, and a box containing only spaces rendered as nothing at all. Harmless-looking in a
    // read-only view and fatal behind an editable one, where the coloured layer has to line up with the text character for
    // character. A viewer that quietly alters the message it is showing is the wrong kind of wrong for this application.
    if (!seg.trim()) {
      tokens.push({ cls: '', text: seg })

      continue
    }

    // Element separator (usually *)
    const sep = seg.startsWith('ISA') ? seg[3] || '*' : '*'
    const parts = seg.split(sep)

    for (let pi = 0; pi < parts.length; pi++) {
      if (pi > 0) tokens.push({ cls: C.punct, text: sep })
      if (pi === 0) {
        tokens.push({ cls: C.tag, text: parts[pi]! })
      } else {
        tokens.push({ cls: C.value, text: parts[pi]! })
      }
    }
  }
  return tokens
}

/** Detect the language from the content. */
function detectLanguage(src: string): 'xml' | 'json' | 'hl7' | 'x12' | 'text' {
  const trimmed = src.trimStart()
  if (trimmed.startsWith('<')) return 'xml'
  if (trimmed.startsWith('{') || trimmed.startsWith('[')) return 'json'
  if (trimmed.startsWith('MSH|') || trimmed.startsWith('FHS|') || trimmed.startsWith('BHS|')) return 'hl7'
  if (trimmed.startsWith('ISA')) return 'x12'
  // Check if it looks like HL7 (lines starting with 3-char segment IDs followed by |)
  if (/^[A-Z]{2,3}\|/.test(trimmed)) return 'hl7'
  return 'text'
}

/**
 * highlightJS covers JavaScript and Lua well enough for reading.
 *
 * Added because the Playground shows a Mirth script in a box a person edits, and the whole claim of that tab is that no server is
 * involved once the page has loaded - so the CodeMirror editor cannot be used there. It asks the server to compile on every pause,
 * which is exactly right in the channel builder and would quietly break the promise the Playground makes.
 *
 * One tokenizer for both languages. They disagree about comments and Lua has no braces, but the pieces that carry the colour -
 * comments, strings, numbers, keywords - are close enough that two nearly identical functions would be worse than one that names
 * both keyword sets. Where they conflict, Lua wins on comments: -- starts one, and treating it as two minus signs is the specific
 * error that made highlighting Lua as JavaScript actively misleading.
 */
function highlightJS(src: string): Token[] {
  const tokens: Token[] = []

  // Comments and strings first, because a keyword inside a string is not a keyword. Lua's -- and long strings are included so the
  // same function can serve both languages.
  const re =
    /(\/\/[^\n]*|--[^\n]*|\/\*[\s\S]*?\*\/|--\[\[[\s\S]*?\]\])|('(?:[^'\\\n]|\\.)*'|"(?:[^"\\\n]|\\.)*"|`(?:[^`\\]|\\.)*`)|(\b\d+(?:\.\d+)?\b)|([A-Za-z_$][\w$]*)|([^]+?)/g

  const keywords = new Set([
    // JavaScript.
    'var', 'let', 'const', 'function', 'return', 'if', 'else', 'for', 'while', 'do', 'break', 'continue', 'new', 'delete',
    'typeof', 'instanceof', 'this', 'null', 'undefined', 'true', 'false', 'try', 'catch', 'finally', 'throw', 'switch',
    'case', 'default', 'in', 'of', 'class', 'extends', 'super', 'async', 'await', 'yield',
    // Lua.
    'local', 'end', 'then', 'elseif', 'nil', 'not', 'and', 'or', 'repeat', 'until', 'require',
  ])

  let m: RegExpExecArray | null
  while ((m = re.exec(src)) !== null) {
    if (m[1]) {
      tokens.push({ cls: C.comment, text: m[1] })
    } else if (m[2]) {
      tokens.push({ cls: C.string, text: m[2] })
    } else if (m[3]) {
      tokens.push({ cls: C.number, text: m[3] })
    } else if (m[4]) {
      tokens.push({ cls: keywords.has(m[4]) ? C.keyword : C.value, text: m[4] })
    } else {
      tokens.push({ cls: C.punct, text: m[0] })
    }

    // A zero-width match would spin forever. Cheap to guard and impossible to notice in review.
    if (m[0] === '') re.lastIndex += 1
  }

  return tokens
}

/**
 * highlightYAML colours the format Perfuse actually writes.
 *
 * Worth having for a reason the others are not: YAML is what a channel *is* here. The generated file preview is the most-read code
 * display in the application - it is the thing the builder exists to produce, and what every browser test inspects to decide whether
 * the form worked - and it was plain grey text.
 *
 * Line-oriented rather than a parser, which suits YAML: a key at the start of a line, a comment from a hash, a list dash, and
 * everything after the colon as a value. Block scalars are not tracked, so a script embedded in a channel file is coloured as values
 * rather than as JavaScript. That is a real limit and the honest trade: pretending to know where a block scalar ends is how a
 * highlighter starts colouring half a document as a string.
 */
function highlightYAML(src: string): Token[] {
  const tokens: Token[] = []

  // Split so the newlines survive: they are part of the input and the overlay needs every one of them.
  for (const piece of src.split(/(\n)/)) {
    if (piece === '\n') {
      tokens.push({ cls: '', text: piece })

      continue
    }

    if (piece === '') continue

    const comment = /^(\s*)(#.*)$/.exec(piece)
    if (comment) {
      tokens.push({ cls: '', text: comment[1] ?? '' })
      tokens.push({ cls: C.comment, text: comment[2] ?? '' })

      continue
    }

    // Indent, an optional list dash, a key, the colon, then the rest.
    const entry = /^(\s*)(-\s+)?([\w.$-]+)(:)(.*)$/.exec(piece)
    if (entry) {
      tokens.push({ cls: '', text: entry[1] ?? '' })
      if (entry[2]) tokens.push({ cls: C.punct, text: entry[2] })
      tokens.push({ cls: C.key, text: entry[3] ?? '' })
      tokens.push({ cls: C.punct, text: entry[4] ?? '' })

      const rest = entry[5] ?? ''
      if (rest) {
        const quoted = /^(\s*)((?:"[^"]*"|'[^']*'))(\s*)$/.exec(rest)
        if (quoted) {
          tokens.push({ cls: '', text: quoted[1] ?? '' })
          tokens.push({ cls: C.string, text: quoted[2] ?? '' })
          tokens.push({ cls: '', text: quoted[3] ?? '' })
        } else if (/^\s*-?\d+(\.\d+)?\s*$/.test(rest)) {
          tokens.push({ cls: C.number, text: rest })
        } else if (/^\s*(true|false|null|~)\s*$/i.test(rest)) {
          tokens.push({ cls: C.keyword, text: rest })
        } else {
          tokens.push({ cls: C.value, text: rest })
        }
      }

      continue
    }

    // A list item with no key, or a continuation line.
    const item = /^(\s*)(-\s+)(.*)$/.exec(piece)
    if (item) {
      tokens.push({ cls: '', text: item[1] ?? '' })
      tokens.push({ cls: C.punct, text: item[2] ?? '' })
      tokens.push({ cls: C.value, text: item[3] ?? '' })

      continue
    }

    tokens.push({ cls: C.value, text: piece })
  }

  return tokens
}

function tokenize(src: string, lang?: CodeLanguage): Token[] {
  const language = lang || detectLanguage(src)
  switch (language) {
    case 'xml':
      return highlightXML(src)
    case 'json':
      return highlightJSON(src)
    case 'hl7':
      return highlightHL7(src)
    case 'x12':
      return highlightX12(src)
    case 'js':
      return highlightJS(src)
    case 'yaml':
      return highlightYAML(src)
    default:
      return [{ cls: C.value, text: src }]
  }
}

/**
 * SyntaxBlock renders highlighted code in a read-only block.
 * Auto-detects language from content, or accepts an explicit language prop.
 */
export function SyntaxBlock({
  code,
  language,
  maxHeight = '32rem',
  className = '',
}: {
  code: string
  language?: CodeLanguage
  maxHeight?: string
  className?: string
}) {
  const tokens = useMemo(() => tokenize(code, language), [code, language])

  return (
    <pre
      className={`overflow-auto rounded-lg border border-slate-800 bg-slate-950 p-4 font-mono text-xs leading-relaxed ${className}`}
      style={{ maxHeight }}
    >
      <code>
        {tokens.map((t, i) =>
          t.cls ? (
            <span key={i} className={t.cls}>
              {t.text}
            </span>
          ) : (
            t.text
          ),
        )}
      </code>
    </pre>
  )
}

/**
 * SyntaxInline renders highlighted code inline (no block wrapper).
 * For embedding in other components that already have their own pre/container.
 */
export function SyntaxInline({
  code,
  language,
}: {
  code: string
  language?: CodeLanguage
}) {
  const tokens = useMemo(() => tokenize(code, language), [code, language])

  return (
    <>
      {tokens.map((t, i) =>
        t.cls ? (
          <span key={i} className={t.cls}>
            {t.text}
          </span>
        ) : (
          t.text
        ),
      )}
    </>
  )
}

/**
 * tokenizeForTest exposes the tokenizer so its central invariant can be asserted.
 *
 * The invariant is that the tokens rejoin to exactly the input. Everything drawn behind an editable box depends on it, and it cannot
 * be checked through the components, which is why this is exported rather than reached through a rendered pre.
 */
export function tokenizeForTest(src: string, lang?: CodeLanguage): { cls: string; text: string }[] {
  return tokenize(src, lang)
}
