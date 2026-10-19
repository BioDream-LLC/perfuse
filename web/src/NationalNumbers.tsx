import { useEffect, useState } from 'react'
import { api } from './api'
import type { MPICheck, MPIScheme, MPISettings } from './api'
import { Field } from './ui'
import { IconCheck, IconWarning } from './Icons'
import { uiLocale } from './i18n/locale'

/**
 * National patient and person numbers (NHS number, KVNR, BSN, NIR ...): pick the country's scheme, type the number as it is
 * printed, and the server says whether its check digit holds and what it will be stored as. The schemes, their formats and
 * which FHIR systems come from a country's own guide are the server's (internal/natid), never repeated here.
 */

let cached: Promise<MPIScheme[]> | null = null

export function useSchemes(): MPIScheme[] | null {
  const [list, setList] = useState<MPIScheme[] | null>(null)
  useEffect(() => {
    cached ??= api.mpiSchemes().then(
      (r) => r.schemes,
      () => {
        cached = null
        return []
      },
    )
    let live = true
    void cached.then((l) => live && setList(l))
    return () => {
      live = false
    }
  }, [])
  return list
}

export function countryName(code: string): string {
  try {
    return new Intl.DisplayNames([uiLocale()], { type: 'region' }).of(code) ?? code
  } catch {
    return code
  }
}

function schemeLabel(s: MPIScheme): string {
  return `${countryName(s.country)}${s.region ? ` (${s.region})` : ''}: ${s.name}`
}

/** A scheme picker and a number box, checked as typed. The identifier it gives is system|number, or '' when empty. */
export function NationalNumberField({
  scheme,
  number,
  onChange,
}: {
  scheme: string
  number: string
  onChange: (scheme: string, number: string) => void
}) {
  const schemes = useSchemes()
  const [check, setCheck] = useState<MPICheck | null>(null)
  const sch = schemes?.find((x) => x.id === scheme)
  useEffect(() => {
    if (!sch || !number.trim()) {
      setCheck(null)
      return
    }
    let live = true
    const t = setTimeout(() => {
      api.mpiCheck(sch.system, number).then(
        (c) => live && setCheck(c),
        () => live && setCheck(null),
      )
    }, 250)
    return () => {
      live = false
      clearTimeout(t)
    }
  }, [sch, number])
  return (
    <div className="grid gap-3 sm:col-span-3 sm:grid-cols-3">
      <Field label="National number" hint="The country's scheme; its check digit is tested before anything is searched or stored.">
        <select className="input" value={scheme} onChange={(e) => onChange(e.target.value, number)} aria-label="National number scheme">
          <option value="">none</option>
          {(schemes ?? []).map((s) => (
            <option key={s.id} value={s.id}>
              {schemeLabel(s)}
            </option>
          ))}
        </select>
      </Field>
      <Field label="Number" hint={sch ? `${sch.format}. For example ${sch.example}.` : 'Choose a scheme first.'}>
        <input
          className="input font-mono text-xs"
          value={number}
          disabled={!sch}
          aria-describedby="natid-check"
          onChange={(e) => onChange(scheme, e.target.value)}
        />
      </Field>
      <div id="natid-check" role="status" className="self-end pb-2 text-xs">
        {check?.problem ? (
          <span className="inline-flex items-center gap-1 text-rose-300">
            <IconWarning className="h-3.5 w-3.5" aria-hidden="true" /> {check.problem}
          </span>
        ) : check?.scheme ? (
          <span className="inline-flex items-center gap-1 text-emerald-300">
            <IconCheck className="h-3.5 w-3.5" aria-hidden="true" /> {check.name}: check digit holds ({check.display})
          </span>
        ) : null}
      </div>
    </div>
  )
}

/** The national schemes, for the index's settings: which to trust, under which system, and who sees numbers in full. */
export function NationalNumbersSettings({
  settings,
  trusted,
  onTrusted,
  onSettings,
}: {
  settings: MPISettings
  trusted: string[]
  onTrusted: (list: string[]) => void
  onSettings: (s: MPISettings) => void
}) {
  const schemes = useSchemes()
  const [country, setCountry] = useState('')
  if (!schemes) return null
  const countries = [...new Set(schemes.map((s) => s.country))].sort((a, b) => countryName(a).localeCompare(countryName(b), uiLocale()))
  const shown = schemes.filter((s) => !country || s.country === country)
  const systemOf = (s: MPIScheme) => settings.nationalSystems?.[s.id] || s.system
  const setSystem = (s: MPIScheme, uri: string) => {
    const next = { ...(settings.nationalSystems ?? {}) }
    const old = systemOf(s)
    if (uri.trim() && uri.trim() !== s.system) next[s.id] = uri.trim()
    else delete next[s.id]
    onSettings({ ...settings, nationalSystems: next })
    // A trusted scheme stays trusted under its new name.
    if (trusted.includes(old)) onTrusted(trusted.map((t) => (t === old ? uri.trim() || s.system : t)))
  }
  const toggle = (s: MPIScheme, on: boolean) => {
    const sys = systemOf(s)
    onTrusted(on ? [...trusted.filter((t) => t !== sys), sys] : trusted.filter((t) => t !== sys))
  }
  return (
    <div className="mt-5 space-y-3 rounded-lg border border-slate-800 p-3" data-testid="mpi-national">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <p className="text-sm text-slate-200">National numbers</p>
          <p className="mt-1 max-w-3xl text-xs leading-relaxed text-slate-400">
            Each number is stored one way (its scheme's system, no spaces), so a record sending the NHS number under its OID still matches
            one sending it under its FHIR URL. A number whose check digit fails is kept on its record but never links it. Trust a scheme
            only if every record carrying it belongs to one person.
          </p>
        </div>
        <Field label="Country">
          <select className="input" value={country} onChange={(e) => setCountry(e.target.value)}>
            <option value="">all</option>
            {countries.map((c) => (
              <option key={c} value={c}>
                {countryName(c)}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Who sees numbers in full" hint="Everyone below sees the last few characters.">
          <select
            className="input"
            value={settings.showNumbersTo ?? 'editor'}
            onChange={(e) => onSettings({ ...settings, showNumbersTo: e.target.value as MPISettings['showNumbersTo'] })}
          >
            <option value="viewer">viewers and above</option>
            <option value="operator">operators and above</option>
            <option value="editor">editors and above</option>
            <option value="admin">administrators only</option>
          </select>
        </Field>
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-xs">
          <thead>
            <tr className="text-left text-[11px] uppercase tracking-wide text-slate-500">
              <th className="py-1 pr-3">Trust</th>
              <th className="py-1 pr-3">Number</th>
              <th className="py-1 pr-3">Format and check</th>
              <th className="py-1">Stored under</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-800 align-top">
            {shown.map((s) => {
              const sys = systemOf(s)
              return (
                <tr key={s.id} data-scheme={s.id}>
                  <td className="py-2 pr-3">
                    <input
                      type="checkbox"
                      checked={trusted.includes(sys)}
                      onChange={(e) => toggle(s, e.target.checked)}
                      aria-label={`Trust the ${s.name} (${countryName(s.country)})`}
                    />
                  </td>
                  <td className="py-2 pr-3">
                    <span className="text-slate-200">{s.name}</span>
                    <span className="block text-slate-500">
                      {countryName(s.country)}
                      {s.region ? `, ${s.region}` : ''}
                      {s.local ? ` · ${s.local}` : ''}
                    </span>
                    {s.restricted && <span className="mt-1 block text-amber-300">{s.restricted}</span>}
                  </td>
                  <td className="py-2 pr-3 text-slate-400">
                    {s.format}
                    <span className="block text-slate-500">Check: {s.check}</span>
                  </td>
                  <td className="py-2">
                    <input
                      className="input w-full min-w-[16rem] font-mono text-[11px]"
                      value={sys}
                      aria-label={`System for the ${s.name} (${countryName(s.country)})`}
                      onChange={(e) => setSystem(s, e.target.value)}
                    />
                    <span className={`mt-1 block ${s.systemVerified && sys === s.system ? 'text-emerald-300' : 'text-amber-300'}`}>
                      {sys !== s.system
                        ? 'This site’s own system.'
                        : s.systemVerified
                          ? `From the country’s guide: ${s.systemSource}.`
                          : `A default: ${s.systemSource}.`}
                    </span>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}
