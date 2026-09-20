import { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from './api'
import {
  EffectBadge,
  SettingControl,
  type SettingDescriptor,
  type SettingsSchema,
  labelsItsControl,
  settingControlId,
} from './settingsControls'
import type { UiError } from './store'
import { toUiError } from './store'
import { ErrorBox } from './ui'

/** AllSettings is one place to change everything about this server.
 *
 *  Driven entirely by what the server describes. Nothing here knows what any individual setting
 *  means, which is what makes adding one a single entry in the Go registry.
 *
 *  The organising decision: a left-hand list of groups and a single scrolling panel per group, rather
 *  than one enormous page or a tree. One page means nobody finds anything; a tree means everybody has
 *  to learn where things live before they can look. */
export function AllSettings() {
  const [schema, setSchema] = useState<SettingsSchema | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [group, setGroup] = useState<string | null>(null)
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [search, setSearch] = useState('')
  const [notice, setNotice] = useState<string | null>(null)

  /** Edits not yet saved, keyed by setting.
   *
   *  Separate from the loaded values rather than mutating them, so Discard is free and so the save
   *  request carries only what actually changed. Sending everything would mean two people editing
   *  different settings at the same time each overwriting the other's work with values they never
   *  looked at. */
  const [draft, setDraft] = useState<Record<string, unknown>>({})

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.settingsSchema()
      setSchema(res)
      setGroup((g) => g ?? res.groups[0]?.name ?? null)
      setDraft({})
      setError(null)
    } catch (err) {
      setError(toUiError(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const dirtyKeys = useMemo(() => Object.keys(draft), [draft])

  /** Every setting flattened, for searching.
   *
   *  Search crosses groups on purpose. Somebody looking for "retention" should not have to know it
   *  lives under Data, and a settings area organised well enough to navigate is still one where
   *  search is how people actually arrive. */
  const allSettings = useMemo(() => {
    if (!schema) return []
    const out: { setting: SettingDescriptor; group: string; subgroup: string }[] = []
    for (const g of schema.groups) {
      for (const sub of g.subgroups) {
        for (const s of sub.settings) {
          out.push({ setting: s, group: g.name, subgroup: sub.name })
        }
      }
    }
    return out
  }, [schema])

  const searching = search.trim().length > 0
  const searchHits = useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return []
    // Label, help and key. Help is included because somebody searching "phish" should find the
    // passkey domain, whose label does not contain the word but whose explanation does.
    return allSettings.filter(
      ({ setting: s }) =>
        s.label.toLowerCase().includes(q) ||
        s.help.toLowerCase().includes(q) ||
        s.key.toLowerCase().includes(q),
    )
  }, [allSettings, search])

  const valueOf = (s: SettingDescriptor): unknown => {
    if (s.key in draft) return draft[s.key]
    if (!schema) return s.default
    if (s.kind === 'secret') return ''
    return schema.values[s.key] ?? s.default
  }

  const setValue = (key: string, next: unknown) => {
    setNotice(null)
    setDraft((d) => {
      const original = schema?.values[key]
      // A value edited back to what it was is removed from the draft rather than sent as a change.
      // Otherwise Save reports settings as changed that were not, and the audit trail says so.
      if (original !== undefined && String(original) === String(next)) {
        const { [key]: _dropped, ...rest } = d
        return rest
      }
      return { ...d, [key]: next }
    })
  }

  const save = async () => {
    if (!schema || dirtyKeys.length === 0) return
    setSaving(true)
    setError(null)
    setNotice(null)
    try {
      const res = await api.saveSettingValues(draft)
      await load()
      const parts: string[] = []
      if (res.applied?.length) {
        parts.push(`Saved ${res.applied.length} setting${res.applied.length === 1 ? '' : 's'}.`)
      }
      if (res.unchanged?.length) {
        parts.push(`${res.unchanged.length} were already set that way.`)
      }
      if (res.restartRequired?.length) {
        parts.push(
          `${res.restartRequired.length} need${res.restartRequired.length === 1 ? 's' : ''} a restart to take effect.`,
        )
      }
      setNotice(parts.join(' ') || 'Nothing changed.')
    } catch (err) {
      setError(toUiError(err))
    } finally {
      setSaving(false)
    }
  }

  if (loading && !schema) {
    return <p className="p-6 text-sm text-slate-500">Loading settings…</p>
  }
  if (!schema) {
    return (
      <div className="p-6">
        <ErrorBox error={error ?? { message: 'Settings could not be read.', problems: [] }} />
      </div>
    )
  }

  const current = schema.groups.find((g) => g.name === group) ?? schema.groups[0]

  return (
    <div className="space-y-4">
      {/* A banner rather than a note beside one control. A pending restart is a property of the
          server, and somebody arriving at this page needs to know before they change anything else. */}
      {schema.restartRequired.length > 0 && (
        <div className="rounded-xl border border-amber-900/60 bg-amber-950/30 px-4 py-3">
          <p className="text-sm font-medium text-amber-200">
            {schema.restartRequired.length} change
            {schema.restartRequired.length === 1 ? '' : 's'} will not take effect until this server
            restarts
          </p>
          <p className="mt-1 text-xs leading-relaxed text-amber-300/90">
            {schema.restartRequired.join(', ')}
          </p>
        </div>
      )}

      {!schema.writable && (
        <div className="rounded-xl border border-slate-800 bg-slate-900/60 px-4 py-3">
          <p className="text-sm font-medium text-slate-100">These settings cannot be changed here</p>
          <p className="mt-1 text-xs leading-relaxed text-slate-400">
            This server was started without a settings file, so there is nowhere to save to. Every
            value below is what the server is currently using.
          </p>
        </div>
      )}

      {error && <ErrorBox error={error} />}
      {notice && (
        <p className="rounded-xl border border-emerald-900/60 bg-emerald-950/30 px-4 py-3 text-sm text-emerald-200">
          {notice}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-3">
        <input
          className="input max-w-xs"
          aria-label="Search all settings"
          placeholder="Search all settings…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <label className="flex items-center gap-2 text-xs text-slate-400">
          <input
            type="checkbox"
            checked={showAdvanced}
            onChange={(e) => setShowAdvanced(e.target.checked)}
            className="accent-sky-600"
          />
          Show advanced settings
        </label>
        <span className="flex-1" />
        <span className="text-xs text-slate-500">
          Stored in <code className="font-mono">{schema.path || 'nowhere'}</code>
        </span>
      </div>

      <div className="flex gap-6">
        {/* Group navigation. Hidden while searching, because search results cross groups and a
            highlighted group would then be describing something the page is not showing. */}
        {!searching && (
          <nav className="w-44 shrink-0 space-y-1" aria-label="Settings groups">
            {schema.groups.map((g) => {
              const pending = g.subgroups.some((sub) =>
                sub.settings.some((s) => s.key in draft),
              )
              return (
                <button
                  key={g.name}
                  type="button"
                  onClick={() => setGroup(g.name)}
                  className={`flex w-full items-center justify-between rounded-lg px-3 py-2 text-left text-sm transition-colors ${
                    current?.name === g.name
                      ? 'bg-sky-950/50 font-medium text-sky-200'
                      : 'text-slate-300 hover:bg-slate-800'
                  }`}
                >
                  <span>{g.name}</span>
                  {/* A dot on a group with unsaved edits, so somebody who wandered off to another
                      group can find their way back to what they changed. */}
                  {pending && <span className="h-2 w-2 rounded-full bg-sky-500" />}
                </button>
              )
            })}
          </nav>
        )}

        <div className="min-w-0 flex-1 space-y-6">
          {searching ? (
            searchHits.length === 0 ? (
              <p className="text-sm text-slate-500">Nothing matches “{search}”.</p>
            ) : (
              <div className="space-y-4">
                {searchHits.map(({ setting, group: g, subgroup }) => (
                  <SettingRow
                    key={setting.key}
                    setting={setting}
                    breadcrumb={`${g} › ${subgroup}`}
                    value={valueOf(setting)}
                    secretIsSet={Boolean(schema.secrets[setting.key])}
                    explicit={schema.explicit.includes(setting.key)}
                    dirty={setting.key in draft}
                    disabled={!schema.writable}
                    onChange={(v) => setValue(setting.key, v)}
                  />
                ))}
              </div>
            )
          ) : (
            current?.subgroups.map((sub) => {
              const visible = sub.settings.filter((s) => showAdvanced || !s.advanced)
              if (visible.length === 0) return null
              return (
                <section key={sub.name} className="space-y-4">
                  <h3 className="border-b border-slate-800 pb-2 text-sm font-semibold uppercase tracking-wide text-slate-400">
                    {sub.name}
                  </h3>
                  {visible.map((setting) => (
                    <SettingRow
                      key={setting.key}
                      setting={setting}
                      value={valueOf(setting)}
                      secretIsSet={Boolean(schema.secrets[setting.key])}
                      explicit={schema.explicit.includes(setting.key)}
                      dirty={setting.key in draft}
                      disabled={!schema.writable}
                      onChange={(v) => setValue(setting.key, v)}
                    />
                  ))}
                </section>
              )
            })
          )}
        </div>
      </div>

      {/* A fixed bar rather than a button at the bottom of a long page. Somebody who changes
          something near the top should not have to hunt for the way to keep it. */}
      {dirtyKeys.length > 0 && (
        <div className="sticky bottom-0 -mx-4 flex items-center gap-3 border-t border-slate-800 bg-slate-950/95 px-4 py-3 backdrop-blur">
          <span className="text-sm text-slate-300">
            {dirtyKeys.length} unsaved change{dirtyKeys.length === 1 ? '' : 's'}
          </span>
          <span className="flex-1" />
          <button type="button" className="btn text-sm" onClick={() => setDraft({})}>
            Discard
          </button>
          <button
            type="button"
            className="btn-primary text-sm"
            disabled={saving || !schema.writable}
            onClick={() => void save()}
          >
            {saving ? 'Saving…' : 'Save changes'}
          </button>
        </div>
      )}
    </div>
  )
}

/** SettingRow is one labelled control with its explanation. */
function SettingRow({
  setting,
  value,
  secretIsSet,
  explicit,
  dirty,
  disabled,
  breadcrumb,
  onChange,
}: {
  setting: SettingDescriptor
  value: unknown
  secretIsSet: boolean
  explicit: boolean
  dirty: boolean
  disabled: boolean
  breadcrumb?: string
  onChange: (v: unknown) => void
}) {
  return (
    <div
      className={`rounded-xl border p-4 transition-colors ${
        dirty ? 'border-sky-700 bg-sky-950/30' : 'border-slate-800 bg-slate-900/60'
      }`}
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          {breadcrumb && (
            <p className="mb-0.5 text-[11px] uppercase tracking-wide text-slate-400">
              {breadcrumb}
            </p>
          )}
          {/* Associated with its control where there is exactly one to name. A toggle, a slider, a
              radio group and a list each carry their own name, because none of them is a single element
              a label may reference. */}
          <label
            className="text-sm font-medium text-slate-100"
            htmlFor={labelsItsControl(setting.widget) ? settingControlId(setting.key) : undefined}
          >
            {setting.label}
          </label>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {/* Whether anybody has actually chosen this, as against it sitting at its default.
              "Nobody has decided" and "somebody decided" call for different confidence when you
              are the next person to look. */}
          {!explicit && (
            <span className="rounded-full bg-slate-800 px-2 py-0.5 text-[10px] uppercase tracking-wide text-slate-400">
              Default
            </span>
          )}
          {/* Amber rather than red. Being audited is a caution - it is worth knowing before you change this, and it is
              not a fault. Red is kept for something being wrong now, so that when a value really is rejected the
              message stands out instead of matching the decoration that was already on the screen. */}
          {setting.sensitive && (
            <span className="rounded-full border border-amber-800 bg-amber-950/40 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide text-amber-200">
              Audited
            </span>
          )}
          <EffectBadge effect={setting.effect} />
        </div>
      </div>

      <p id={`${setting.key}-help`} className="mt-1 max-w-2xl text-xs leading-relaxed text-slate-400">
        {setting.help}
      </p>

      <div className={`mt-3 ${disabled ? 'pointer-events-none opacity-50' : ''}`}>
        <SettingControl
          setting={setting}
          value={value}
          secretIsSet={secretIsSet}
          onChange={onChange}
        />
      </div>

      {/* The flag it came from, for anybody comparing this page against a service definition. */}
      {setting.flag && (
        <p className="mt-2 font-mono text-[11px] text-slate-400">-{setting.flag}</p>
      )}
    </div>
  )
}
