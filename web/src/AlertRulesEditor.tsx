import { useEffect, useMemo, useState } from 'react'

import { api, ApiError, type AlertKindInfo, type AlertRulesDocument, type EditableAlertRule } from './api'
import { ErrorBox, Field, Section } from './ui'

/**
 * Editing alert rules as rules, rather than as a YAML file somebody has to already understand.
 *
 * # Why this exists
 *
 * The rules file was reachable before: the settings screen offers it as a text box. That satisfied the letter of being able to do
 * everything from the interface and missed the point of it. To add one rule an operator had to know the file has a rules key, that the
 * kind is spelled error-rate and not error_rate, that for wants a Go duration, and that threshold is a share between nought and one
 * for two kinds and a plain count for the other nine.
 *
 * That last one is not a detail. Somebody who means five percent types 5, the file loads, nothing complains, and the alert can never
 * fire - a silent failure in the direction of not being told about problems. Every threshold box here is labelled with its unit and a
 * share is shown as a percentage, which is the whole reason the units come from the server rather than from a list in this file.
 *
 * # Why the catalogue is fetched rather than written here
 *
 * A list of kinds in the browser drifts. It would have drifted already: below-rhythm existed with an evaluator and seven tests and
 * could not be configured at all, because the list the loader checks against had never been updated. The form is built from what the
 * engine says it can evaluate, so a kind that cannot fire cannot be offered and a kind that can is offered the day it appears.
 */

/** Fires above or below, said in words, because "threshold" alone does not tell an operator which way to move the number. */
function directionWords(info: AlertKindInfo): string {
  return info.direction === 'below' ? 'Fires when the measurement falls below' : 'Fires when the measurement rises above'
}

/** unitWords labels the threshold box. */
function unitWords(info: AlertKindInfo): string {
  switch (info.unit) {
    case 'share':
      return '% of normal'
    case 'messages':
      return 'messages'
    case 'seconds':
      return 'seconds'
    case 'attempts':
      return 'attempts'
    default:
      return ''
  }
}

/**
 * A share is edited as a percentage and stored as a fraction.
 *
 * Nobody thinks in shares. Showing 0.05 and asking for a number invites 5, which means five hundred percent and an alert that never
 * fires - the exact confusion this screen exists to remove. So the box says 5 and the file gets 0.05, and the conversion happens in one
 * place rather than in the operator's head.
 */
function toDisplay(info: AlertKindInfo, threshold: number): string {
  if (info.unit === 'share') return String(Math.round(threshold * 1000) / 10)
  return String(threshold)
}

function fromDisplay(info: AlertKindInfo, text: string): number {
  const n = Number(text)
  if (!Number.isFinite(n)) return 0
  if (info.unit === 'share') return Math.round((n / 100) * 1000) / 1000
  return n
}

/** A rule with no channel watches every channel, which is worth saying rather than leaving a box empty. */
const EVERY_CHANNEL = ''

export function AlertRulesEditor() {
  const [doc, setDoc] = useState<AlertRulesDocument | null>(null)
  const [rules, setRules] = useState<EditableAlertRule[]>([])
  const [error, setError] = useState<{ message: string; problems: string[] } | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let live = true
    void (async () => {
      try {
        const got = await api.alertRules()
        if (!live) return
        setDoc(got)
        setRules(got.rules)
      } catch (e) {
        if (!live) return
        setError({
          message: e instanceof Error ? e.message : String(e),
          problems: e instanceof ApiError ? e.problems : [],
        })
      } finally {
        if (live) setLoading(false)
      }
    })()
    return () => {
      live = false
    }
  }, [])

  const byKind = useMemo(() => {
    const out = new Map<string, AlertKindInfo>()
    for (const k of doc?.kinds ?? []) out.set(k.kind, k)
    return out
  }, [doc])

  /**
   * Channels named by the rules as loaded that do not exist on this server.
   *
   * Computed from doc rather than from the rules being edited, so the list does not change while somebody is working. A rule can
   * legitimately name a channel that has been renamed or not created yet, and it is valid and matches nothing - worth showing plainly
   * rather than quietly correcting.
   */
  const missingChannels = useMemo(() => {
    const known = new Set(doc?.channels ?? [])
    const out = new Set<string>()
    for (const r of doc?.rules ?? []) {
      if (r.channel && !known.has(r.channel)) out.add(r.channel)
    }
    return [...out].sort()
  }, [doc])

  /** Whether anything has been changed, so the save button can mean something. */
  const dirty = useMemo(() => JSON.stringify(rules) !== JSON.stringify(doc?.rules ?? []), [rules, doc])

  function patch(index: number, change: Partial<EditableAlertRule>) {
    setRules((current) => current.map((r, i) => (i === index ? { ...r, ...change } : r)))
    setNote(null)
  }

  function addRule() {
    const first = doc?.kinds[0]
    if (!first) return
    setRules((current) => [
      ...current,
      { kind: first.kind, threshold: first.default, severity: 'warning', channel: EVERY_CHANNEL },
    ])
    setNote(null)
  }

  function removeRule(index: number) {
    setRules((current) => current.filter((_, i) => i !== index))
    setNote(null)
  }

  /**
   * Changing the kind resets the threshold to that kind's default.
   *
   * Deliberate, and the alternative is worse. Keeping 500 when somebody switches a queue-depth rule to error-rate leaves a share of
   * fifty thousand percent in the box - valid, saveable, and never able to fire. Losing a number somebody typed is annoying; keeping a
   * number that silently means something else is the failure this screen is for.
   */
  function changeKind(index: number, kind: string) {
    const info = byKind.get(kind)
    patch(index, {
      kind,
      threshold: info?.default ?? 0,
      destination: info?.destination ? rules[index]?.destination : undefined,
    })
  }

  async function save() {
    setSaving(true)
    setError(null)
    setNote(null)
    try {
      const res = await api.saveAlertRules(rules)
      setNote(`Saved ${res.saved} rule${res.saved === 1 ? '' : 's'} to ${res.path}. ${res.note}`)
      setDoc((d) => (d ? { ...d, rules } : d))
    } catch (e) {
      setError({
        message: e instanceof Error ? e.message : String(e),
        problems: e instanceof ApiError ? e.problems : [],
      })
    } finally {
      setSaving(false)
    }
  }

  if (loading) {
    return (
      <Section title="Alert rules" description="What raises an alert, and how loudly.">
        <p className="text-sm text-slate-400">Reading the rules…</p>
      </Section>
    )
  }

  if (!doc) {
    return (
      <Section title="Alert rules" description="What raises an alert, and how loudly.">
        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}
      </Section>
    )
  }

  return (
    <Section
      title="Alert rules"
      description="What raises an alert, and how loudly. Changes take effect without a restart."
    >
      <div className="space-y-4">
        {!doc.enabled && (
          <p className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-amber-200">
            Alerting is not running on this server, so these rules are being edited but nothing is being checked against
            them. Rules saved now will take effect when alerting is switched on.
          </p>
        )}

        {!doc.writable && (
          <p className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-amber-200">
            This server was started without an alert rules file, so it is using the built-in rules and there is nowhere to
            save changes. Start it with <code className="font-mono">-alerts</code> pointing at a file to edit them here.
          </p>
        )}

        {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

        {note && (
          <p role="status" className="rounded-lg border border-emerald-500/40 bg-emerald-500/10 p-3 text-sm text-emerald-200">
            {note}
          </p>
        )}

        <ul className="space-y-3">
          {rules.map((rule, i) => {
            const info = byKind.get(rule.kind)
            const id = `alert-rule-${i}`

            return (
              <li
                key={i}
                className={`rounded-xl border p-4 ${
                  rule.disabled ? 'border-slate-800 bg-slate-900/40 opacity-70' : 'border-slate-700 bg-slate-900/70'
                }`}
              >
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="What to watch" htmlFor={`${id}-kind`}>
                    <select
                      id={`${id}-kind`}
                      className="input"
                      value={rule.kind}
                      onChange={(e) => changeKind(i, e.target.value)}
                    >
                      {doc.kinds.map((k) => (
                        <option key={k.kind} value={k.kind}>
                          {k.label}
                        </option>
                      ))}
                    </select>
                  </Field>

                  <Field label="How loudly" htmlFor={`${id}-severity`}>
                    <select
                      id={`${id}-severity`}
                      className="input"
                      value={rule.severity ?? 'warning'}
                      onChange={(e) => patch(i, { severity: e.target.value as 'warning' | 'critical' })}
                    >
                      <option value="warning">Warning — worth looking at</option>
                      <option value="critical">Critical — messages are not getting through now</option>
                    </select>
                  </Field>
                </div>

                {info && (
                  <p className="mt-2 text-xs leading-relaxed text-slate-400">
                    {info.summary} {info.detail}
                  </p>
                )}

                <div className="mt-3 grid gap-3 sm:grid-cols-3">
                  {info && info.unit !== 'none' && (
                    <Field
                      label={`Threshold${unitWords(info) ? ` (${unitWords(info)})` : ''}`}
                      htmlFor={`${id}-threshold`}
                      hint={`${directionWords(info)} this.`}
                    >
                      <input
                        id={`${id}-threshold`}
                        className="input"
                        type="number"
                        step={info.unit === 'share' ? 1 : 'any'}
                        min={0}
                        value={toDisplay(info, rule.threshold)}
                        onChange={(e) => patch(i, { threshold: fromDisplay(info, e.target.value) })}
                      />
                    </Field>
                  )}

                  <Field
                    label="Only after it holds for"
                    htmlFor={`${id}-for`}
                    hint="Like 5m or 30s. Without it, one unlucky minute pages somebody."
                  >
                    <input
                      id={`${id}-for`}
                      className="input"
                      value={rule.for ?? ''}
                      placeholder="5m"
                      onChange={(e) => patch(i, { for: e.target.value })}
                    />
                  </Field>

                  {info?.channel && (
                    <Field label="Channel" htmlFor={`${id}-channel`} hint="Leave as every channel unless this rule is about one feed.">
                      <select
                        id={`${id}-channel`}
                        className="input"
                        value={rule.channel ?? EVERY_CHANNEL}
                        onChange={(e) => patch(i, { channel: e.target.value })}
                      >
                        <option value={EVERY_CHANNEL}>Every channel</option>
                        {doc.channels.map((c) => (
                          <option key={c} value={c}>
                            {c}
                          </option>
                        ))}
                        {/* Channels a rule names that no longer exist.
                            
                            Offered rather than silently dropped, because losing one would change what the rule watches without
                            saying so. Taken from the rules as they were loaded and not from the current value, which is a
                            difference that matters: deriving it from the current value meant the option vanished the moment
                            somebody picked anything else, so a misspelled or renamed channel could be looked at once and never
                            got back to. Found by the widget sweep, which selected every option in turn and then could not
                            return to the one it started on. */}
                        {missingChannels.map((c) => (
                          <option key={c} value={c}>
                            {c} (no such channel now)
                          </option>
                        ))}
                      </select>
                    </Field>
                  )}

                  {info?.destination && (
                    <Field
                      label="Destination"
                      htmlFor={`${id}-destination`}
                      hint="Leave blank for every destination of that channel."
                    >
                      <input
                        id={`${id}-destination`}
                        className="input"
                        value={rule.destination ?? ''}
                        placeholder="every destination"
                        onChange={(e) => patch(i, { destination: e.target.value })}
                      />
                    </Field>
                  )}
                </div>

                <div className="mt-3 flex flex-wrap items-center gap-4">
                  <label className="flex items-center gap-2 text-sm text-slate-300">
                    <input
                      type="checkbox"
                      className="accent-sky-600"
                      checked={!rule.disabled}
                      onChange={(e) => patch(i, { disabled: !e.target.checked })}
                    />
                    Checking this rule
                  </label>

                  <span className="flex-1" />

                  <button
                    type="button"
                    className="btn-ghost py-1 text-xs"
                    onClick={() => removeRule(i)}
                    aria-label={`Delete rule ${i + 1}`}
                  >
                    Delete rule
                  </button>
                </div>
              </li>
            )
          })}
        </ul>

        <div className="flex flex-wrap items-center gap-3">
          <button type="button" className="btn-ghost" onClick={addRule}>
            + Add rule
          </button>

          <span className="flex-1" />

          {dirty && <span className="text-xs text-amber-300">Unsaved changes</span>}

          <button
            type="button"
            className="btn-primary"
            onClick={() => void save()}
            disabled={saving || !dirty || !doc.writable}
          >
            {saving ? 'Saving…' : 'Save rules'}
          </button>
        </div>

        <p className="text-xs text-slate-500">
          Stored in <code className="font-mono">{doc.path || 'the built-in defaults'}</code>. Turning a rule off keeps the
          threshold somebody tuned, which is why there is a switch as well as a delete.
        </p>
      </div>
    </Section>
  )
}
