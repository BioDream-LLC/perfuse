import { CodeArea } from './CodeArea'
import { useCallback, useMemo, useState } from 'react'
import { api } from './api'
import type { UiError } from './store'
import { toUiError } from './store'
import { ErrorBox, Section } from './ui'
import { copyText } from './clipboard'

/** One addressable place in a v3 message, as the server describes it. */
export interface V3FieldNode {
  name: string
  path: string
  value?: string
  attributes?: { name: string; value: string; path: string }[]
  nullFlavor?: string
  occurrence?: number
  siblingCount?: number
  children?: V3FieldNode[]
  interesting?: boolean
}

export interface V3FieldTree {
  root: V3FieldNode
  fields: V3FieldNode[]
  interaction?: string
  truncated?: boolean
}

export interface V3PathCheck {
  valid: boolean
  error?: string
  values: string[]
  exists: boolean
  nullFlavor?: string
}

/** V3FieldPicker lets somebody paste a v3 message and click the field they want.
 *
 *  The problem it solves: HL7 v3 keeps its values in attributes, nests them seven levels inside
 *  envelope, and nests them differently depending on the interaction. Writing a path by hand means
 *  reading the XML, counting repeats, and knowing which of three elements called "id" is the
 *  patient's. People get that wrong, and it fails silently — a channel that matches everything or
 *  nothing, with no error anywhere.
 *
 *  So: paste your own message, see what your own sender really sends, click the value you want. */
export function V3FieldPicker({
  onPick,
  initialMessage,
}: {
  /** Called with a path when somebody picks a field. Omitted when this is used as a scratch tool. */
  onPick?: (path: string) => void
  initialMessage?: string
}) {
  const [message, setMessage] = useState(initialMessage ?? '')
  const [tree, setTree] = useState<V3FieldTree | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [loading, setLoading] = useState(false)
  const [search, setSearch] = useState('')
  const [onlyInteresting, setOnlyInteresting] = useState(true)
  const [copied, setCopied] = useState<string | null>(null)

  /** A path somebody is testing by hand, and what it selects. */
  const [testPath, setTestPath] = useState('')
  const [check, setCheck] = useState<V3PathCheck | null>(null)

  const inspect = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setTree(await api.v3Fields(message))
    } catch (err) {
      setTree(null)
      setError(toUiError(err))
    } finally {
      setLoading(false)
    }
  }, [message])

  /** Tests a path against the pasted message.
   *
   *  Called on demand rather than on every keystroke. A request per character would be a lot of XML
   *  parsing for a server to do while somebody thinks, and the answer is only interesting once the
   *  path is finished. */
  const runCheck = useCallback(
    async (path: string) => {
      setTestPath(path)
      if (!path.trim()) {
        setCheck(null)
        return
      }
      try {
        setCheck(await api.v3CheckPath(path, message))
      } catch (err) {
        setError(toUiError(err))
      }
    },
    [message],
  )

  const copy = async (path: string) => {
    try {
      const result = await copyText(path)
      if (result === 'failed') {
        setCopied(null)
        return
      }
      setCopied(path)
      // Cleared after a moment so the tick does not sit on a stale row once somebody copies another.
      window.setTimeout(() => setCopied((c) => (c === path ? null : c)), 1500)
    } catch {
      // A clipboard refusal is not worth an error banner: the path is on screen and selectable, so
      // the fallback is the thing somebody would have done anyway.
      setCopied(null)
    }
  }

  /** The fields to list, after searching and filtering. */
  const shown = useMemo(() => {
    if (!tree) return []
    const q = search.trim().toLowerCase()

    return tree.fields.filter((f) => {
      // Matching on the value as well as the name, because somebody looking at a printout searches
      // for "Okonkwo" far more readily than for "family".
      if (q) {
        const haystack = [
          f.name,
          f.value ?? '',
          f.path,
          ...(f.attributes ?? []).map((a) => `${a.name} ${a.value}`),
        ]
          .join(' ')
          .toLowerCase()
        if (!haystack.includes(q)) return false
      }
      // The filter is ignored while searching. Somebody who typed a name wants it found wherever it
      // is, and silently withholding a match because it is not on a promoted list is the behaviour
      // that makes people distrust a search box.
      if (onlyInteresting && !q && !f.interesting) return false

      return true
    })
  }, [tree, search, onlyInteresting])

  return (
    <Section
      title="Pick a field from a v3 message"
      description="Paste a message your sender actually produced. HL7 v3 keeps most values in attributes and nests the same field differently in different interactions, so the reliable way to get a path right is to take it from a real message rather than from the specification."
    >
      <div className="space-y-4">
        {error && <ErrorBox error={error} />}

        <div>
          <CodeArea
            language="xml"
            aria-label="HL7 v3 message to read"
            className="h-40 w-full"
            placeholder="<PRPA_IN201306UV02 xmlns=&quot;urn:hl7-org:v3&quot;> …"
            value={message}
            onChange={setMessage}
            spellCheck={false}
          />
          <div className="mt-2 flex flex-wrap items-center gap-3">
            <button
              type="button"
              className="btn-primary text-sm"
              disabled={loading || !message.trim()}
              onClick={() => void inspect()}
            >
              {loading ? 'Reading…' : 'Read this message'}
            </button>
            {tree?.interaction && (
              <span className="text-xs text-slate-400">
                Interaction <code className="font-mono">{tree.interaction}</code>
              </span>
            )}
            {tree && (
              <span className="text-xs text-slate-500">
                {tree.fields.length} readable field{tree.fields.length === 1 ? '' : 's'}
              </span>
            )}
          </div>
        </div>

        {/* Truncation is said out loud. A picker quietly showing part of a message convinces
            somebody a field is absent from their feed when it is only absent from the tree. */}
        {tree?.truncated && (
          <p className="rounded-lg border border-amber-800 bg-amber-950/40 px-3 py-2 text-xs leading-relaxed text-amber-200">
            This message is large enough that the list below was cut short. Paths shown are correct;
            there are simply more fields than are displayed. Trimming the message to one patient will
            show everything.
          </p>
        )}

        {tree && (
          <>
            <div className="flex flex-wrap items-center gap-3">
              <input
                className="input max-w-xs"
                placeholder="Search fields or values…"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
              <label className="flex items-center gap-2 text-xs text-slate-400">
                <input
                  type="checkbox"
                  checked={onlyInteresting}
                  disabled={search.trim().length > 0}
                  onChange={(e) => setOnlyInteresting(e.target.checked)}
                  className="accent-sky-600"
                />
                Clinical fields only
              </label>
            </div>

            {shown.length === 0 ? (
              <p className="text-sm text-slate-500">
                {search
                  ? `Nothing in this message matches “${search}”.`
                  : 'No clinical fields were found. Untick the box above to see everything, including the envelope.'}
              </p>
            ) : (
              <div className="overflow-hidden rounded-xl border border-slate-800">
                <table className="min-w-full text-sm">
                  <thead className="bg-slate-900/60">
                    <tr className="text-left text-xs uppercase tracking-wide text-slate-500">
                      <th className="px-3 py-2">Field</th>
                      <th className="px-3 py-2">Value</th>
                      <th className="px-3 py-2">Path</th>
                      <th className="px-3 py-2"></th>
                    </tr>
                  </thead>
                  <tbody>
                    {shown.flatMap((field) => {
                      // One row per readable thing rather than per element, because an element with
                      // three attributes offers three different paths and a single row would make
                      // somebody guess which one the button copies.
                      const rows: {
                        key: string
                        label: string
                        value: string
                        path: string
                        flavor?: string
                      }[] = []

                      if (field.value) {
                        rows.push({
                          key: field.path,
                          label: field.name,
                          value: field.value,
                          path: field.path,
                          flavor: field.nullFlavor,
                        })
                      }
                      for (const attr of field.attributes ?? []) {
                        rows.push({
                          key: attr.path,
                          label: `${field.name} @${attr.name}`,
                          value: attr.value,
                          path: attr.path,
                          flavor: field.nullFlavor,
                        })
                      }
                      // An element with only a null flavour has no value and is still worth
                      // offering: "the sender told us why this is missing" is a real thing to
                      // filter on.
                      if (rows.length === 0 && field.nullFlavor) {
                        rows.push({
                          key: field.path,
                          label: field.name,
                          value: '',
                          path: field.path,
                          flavor: field.nullFlavor,
                        })
                      }

                      return rows.map((row) => (
                        <tr key={row.key} className="border-t border-slate-100 hover:bg-sky-50/40">
                          <td className="px-3 py-2 align-top">
                            <span className="font-medium text-slate-100">{row.label}</span>
                            {/* "2 of 2" rather than two identical rows, which is what makes a
                                repeated element comprehensible at a glance. */}
                            {field.siblingCount ? (
                              <span className="ml-2 rounded bg-slate-800 px-1.5 py-0.5 text-[10px] text-slate-400">
                                {field.occurrence} of {field.siblingCount}
                              </span>
                            ) : null}
                          </td>
                          <td className="px-3 py-2 align-top">
                            {row.value ? (
                              <span className="text-slate-200">{row.value}</span>
                            ) : row.flavor ? (
                              // The reason, not an empty cell. An absent birth date and one the
                              // patient declined to give are different clinical facts, and this is
                              // where somebody first sees which they have.
                              <span className="rounded border border-amber-800 bg-amber-950/40 px-1.5 py-0.5 text-xs text-amber-200">
                                no value — {row.flavor}
                              </span>
                            ) : (
                              <span className="text-slate-400">—</span>
                            )}
                          </td>
                          <td className="px-3 py-2 align-top">
                            <code className="break-all font-mono text-xs text-slate-400">
                              {row.path}
                            </code>
                          </td>
                          <td className="whitespace-nowrap px-3 py-2 align-top text-right">
                            {onPick && (
                              <button
                                type="button"
                                className="btn-primary mr-2 text-xs"
                                onClick={() => onPick(row.path)}
                              >
                                Use
                              </button>
                            )}
                            <button
                              type="button"
                              className="btn text-xs"
                              onClick={() => void copy(row.path)}
                            >
                              {copied === row.path ? 'Copied' : 'Copy'}
                            </button>
                          </td>
                        </tr>
                      ))
                    })}
                  </tbody>
                </table>
              </div>
            )}

            {/* Testing a path by hand. The other half of the job: a generated path is a starting
                point, and somebody who edits one needs to see what it selects before a channel runs
                on it. */}
            <div className="rounded-xl border border-slate-800 bg-slate-900/40 p-4">
              <h4 className="text-sm font-medium text-slate-100">Try a path</h4>
              <p className="mt-1 text-xs leading-relaxed text-slate-400">
                Edit a path from the table, or write one. <code className="font-mono">//</code>{' '}
                means “find anywhere”, <code className="font-mono">(2)</code> picks the second one,
                and <code className="font-mono">@value</code> reads an attribute — which is where v3
                keeps most of its content.
              </p>
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <input
                  className="input flex-1 font-mono text-xs"
                  placeholder="//birthTime@value"
                  value={testPath}
                  onChange={(e) => setTestPath(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') void runCheck(testPath)
                  }}
                  spellCheck={false}
                />
                <button
                  type="button"
                  className="btn text-sm"
                  onClick={() => void runCheck(testPath)}
                >
                  Test
                </button>
              </div>

              {check && (
                <div className="mt-3 text-xs">
                  {!check.valid ? (
                    <p className="text-rose-700">{check.error}</p>
                  ) : check.values.length > 0 ? (
                    <div>
                      <p className="text-emerald-800">
                        Selects {check.values.length} value
                        {check.values.length === 1 ? '' : 's'}:
                      </p>
                      <ul className="mt-1 space-y-0.5">
                        {check.values.slice(0, 10).map((v, i) => (
                          <li key={i} className="font-mono text-slate-300">
                            {v}
                          </li>
                        ))}
                      </ul>
                    </div>
                  ) : check.exists ? (
                    // Present with no value is the answer that catches people out, so it is spelled
                    // out rather than reported as "nothing found".
                    <p className="text-amber-800">
                      The element is there but carries no value
                      {check.nullFlavor ? `, and the sender said why: ${check.nullFlavor}` : ''}. A
                      filter comparing this to a value will not match.
                    </p>
                  ) : (
                    <p className="text-slate-400">
                      Nothing in this message is at that path. Check the spelling, or whether your
                      sender populates it.
                    </p>
                  )}
                </div>
              )}
            </div>
          </>
        )}
      </div>
    </Section>
  )
}
