import { useMemo } from 'react'
import {
  commonPaths,
  commonTriggerEvents,
  newRule,
  operatorIsNumeric,
  operatorLabels,
  operatorTakesList,
  operatorTakesNoValue,
  rulesToExpression,
} from './model'
import type { Rule, RuleOperator } from './model'

/**
 * The rule builder is the reason this GUI exists. Somebody who does not know HL7
 * should be able to say "only admissions from this hospital" without learning
 * either the filter language or the segment numbering.
 *
 * So each row reads as a sentence — field, comparison, value — the field list is
 * labelled in plain language, and the expression it produces is shown underneath
 * so anyone can see exactly what their clicking means.
 */
export function RuleBuilder({
  rules,
  onChange,
  emptyHint,
}: {
  rules: Rule[]
  onChange: (rules: Rule[]) => void
  emptyHint: string
}) {
  const expression = useMemo(() => rulesToExpression(rules), [rules])

  function update(id: string, patch: Partial<Rule>) {
    onChange(rules.map((r) => (r.id === id ? { ...r, ...patch } : r)))
  }

  function remove(id: string) {
    onChange(rules.filter((r) => r.id !== id))
  }

  return (
    <div className="space-y-3">
      {rules.length === 0 && (
        <p className="rounded-lg border border-dashed border-slate-800 px-4 py-6 text-center text-sm text-slate-500">
          {emptyHint}
        </p>
      )}

      {rules.map((rule, index) => (
        <RuleRow
          key={rule.id}
          rule={rule}
          first={index === 0}
          onChange={(patch) => update(rule.id, patch)}
          onRemove={() => remove(rule.id)}
        />
      ))}

      <div className="flex items-center gap-3">
        <button type="button" className="btn-ghost" onClick={() => onChange([...rules, newRule()])}>
          + Add condition
        </button>
        {rules.length > 0 && (
          <button type="button" className="btn-ghost" onClick={() => onChange([])}>
            Clear all
          </button>
        )}
      </div>

      {expression && (
        <div className="rounded-lg border border-slate-800 bg-slate-950/60 p-3">
          <p className="label mb-1">This means</p>
          <code className="font-mono text-xs break-all text-sky-300">{expression}</code>
        </div>
      )}
    </div>
  )
}

function RuleRow({
  rule,
  first,
  onChange,
  onRemove,
}: {
  rule: Rule
  first: boolean
  onChange: (patch: Partial<Rule>) => void
  onRemove: () => void
}) {
  // Trigger events are the field people filter on most, so offer the codes with
  // their meanings rather than expecting someone to remember that A08 is an
  // update and A28 is not.
  const suggestions = rule.path === 'MSH-9.2' ? commonTriggerEvents : null

  return (
    <div className="rounded-lg border border-slate-800 bg-slate-900/40 p-3">
      <div className="flex flex-wrap items-center gap-2">
        {first ? (
          <span className="w-14 shrink-0 text-xs font-medium tracking-wide text-slate-500 uppercase">
            Where
          </span>
        ) : (
          <select
            className="select w-14 shrink-0 px-2 py-1 text-xs"
            value={rule.join}
            onChange={(e) => onChange({ join: e.target.value as 'and' | 'or' })}
            aria-label="How this condition combines with the one above"
          >
            <option value="and">and</option>
            <option value="or">or</option>
          </select>
        )}

        <select
          className="select min-w-56 flex-1"
          value={rule.path}
          onChange={(e) => onChange({ path: e.target.value })}
          aria-label="Field"
        >
          <option value="">Choose a field…</option>
          {commonPaths.map((p) => (
            <option key={p.path} value={p.path}>
              {p.label} ({p.path})
            </option>
          ))}
          {/* A path typed by hand stays selectable, so the builder never blocks
              somebody who knows exactly which field they want. */}
          {rule.path && !commonPaths.some((p) => p.path === rule.path) && (
            <option value={rule.path}>{rule.path}</option>
          )}
        </select>

        <select
          className="select w-44 shrink-0"
          value={rule.operator}
          onChange={(e) => onChange({ operator: e.target.value as RuleOperator })}
          aria-label="Comparison"
        >
          {(Object.keys(operatorLabels) as RuleOperator[]).map((op) => (
            <option key={op} value={op}>
              {operatorLabels[op]}
            </option>
          ))}
        </select>

        {!operatorTakesNoValue(rule.operator) &&
          (operatorTakesList(rule.operator) ? (
            <input
              className="input min-w-48 flex-1"
              value={rule.values.join(', ')}
              onChange={(e) =>
                onChange({ values: e.target.value.split(',').map((v) => v.trim()) })
              }
              placeholder="A01, A04, A08"
              aria-label="Values, separated by commas"
            />
          ) : (
            <input
              className="input min-w-40 flex-1"
              value={rule.value}
              onChange={(e) => onChange({ value: e.target.value })}
              placeholder={
                operatorIsNumeric(rule.operator)
                  ? '90'
                  : rule.operator === 'matches'
                    ? '^GLU'
                    : 'value'
              }
              inputMode={operatorIsNumeric(rule.operator) ? 'decimal' : 'text'}
              aria-label="Value"
            />
          ))}

        <button
          type="button"
          onClick={onRemove}
          className="shrink-0 rounded-md px-2 py-1 text-slate-500 hover:bg-slate-800 hover:text-rose-300"
          aria-label="Remove this condition"
        >
          ✕
        </button>
      </div>

      {suggestions && !operatorTakesNoValue(rule.operator) && (
        <div className="mt-2 flex flex-wrap gap-1.5">
          {suggestions.map((s) => (
            <button
              key={s.code}
              type="button"
              onClick={() => {
                if (operatorTakesList(rule.operator)) {
                  const set = new Set(rule.values.filter(Boolean))
                  set.add(s.code)
                  onChange({ values: [...set] })
                } else {
                  onChange({ value: s.code })
                }
              }}
              className="badge border border-slate-700 bg-slate-800/60 text-slate-300 hover:border-sky-600 hover:text-sky-300"
              title={s.label}
            >
              {s.code}
              <span className="text-slate-500">{s.label}</span>
            </button>
          ))}
        </div>
      )}

      {rule.operator === 'matches' && (
        <p className="mt-2 text-xs text-slate-500">
          A regular expression. <code className="font-mono text-slate-400">^GLU</code> means "starts
          with GLU".
        </p>
      )}
    </div>
  )
}
