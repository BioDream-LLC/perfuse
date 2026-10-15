import { useEffect, useState } from 'react'
import { api, ApiError } from './api'
import type { AlignedCriterion, AlignedView } from './api'
import { ErrorBox, Section, Spinner } from './ui'
import { IconDocuments, IconFleet, IconShield, IconUsers } from './Icons'
import type { UiError } from './store'

/**
 * CMS Aligned Network readiness: the 26 criteria of the CMS Interoperability Framework, each with what Perfuse provides for it
 * and whether this server has it switched on. Networks self-attest to CMS; this is a worksheet for that, not a certification.
 */

const toError = (e: unknown): UiError => ({
  message: e instanceof Error ? e.message : String(e),
  problems: e instanceof ApiError ? e.problems : [],
})

const statusLook: Record<AlignedCriterion['status'], { label: string; cls: string }> = {
  ready: { label: 'Ready here', cls: 'border-emerald-700/60 bg-emerald-500/10 text-emerald-300' },
  partial: { label: 'Ready, with a gap', cls: 'border-cyan-700/60 bg-cyan-500/10 text-cyan-300' },
  off: { label: 'Built, not on', cls: 'border-amber-700/60 bg-amber-500/10 text-amber-300' },
  yours: { label: 'Yours to do', cls: 'border-violet-700/60 bg-violet-500/10 text-violet-300' },
  'not-built': { label: 'Not built', cls: 'border-slate-700 bg-slate-900 text-slate-400' },
}

const groupIcons: Record<string, typeof IconUsers> = {
  'Patient access and empowerment': IconUsers,
  'Provider access and delegation': IconUsers,
  'Data availability and standards': IconDocuments,
  'Network connectivity and transparency': IconFleet,
  'Identity, security and trust': IconShield,
}

const order: AlignedCriterion['status'][] = ['ready', 'partial', 'off', 'yours', 'not-built']

export function AlignedNetwork() {
  const [view, setView] = useState<AlignedView | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  const [only, setOnly] = useState<AlignedCriterion['status'] | ''>('')

  useEffect(() => {
    api.alignedNetwork().then(setView).catch((e) => setError(toError(e)))
  }, [])

  if (error) return <ErrorBox error={error} />
  if (!view) return <Spinner />

  const groups: string[] = []
  for (const c of view.criteria) if (!groups.includes(c.group)) groups.push(c.group)
  const shown = view.criteria.filter((c) => !only || c.status === only)

  return (
    <div className="space-y-6" data-testid="aligned">
      <Section
        title="CMS Aligned Network readiness"
        description={
          <>
            The {view.criteria.length} criteria of the{' '}
            <a className="text-cyan-300 hover:underline" href={view.sourceUrl} target="_blank" rel="noreferrer">
              CMS Interoperability Framework
            </a>{' '}
            and where this server stands on each. Networks self-attest to CMS; this is a worksheet for that attestation, read from what is
            switched on here, not a certification. Criteria 13 to 16 have been in effect since 4 July 2026.
          </>
        }
      >
        <div className="flex flex-wrap gap-2" data-testid="aligned-counts">
          <button
            type="button"
            className={`rounded-full border px-3 py-1 text-xs ${only === '' ? 'border-slate-400 text-slate-100' : 'border-slate-700 text-slate-400'}`}
            onClick={() => setOnly('')}
          >
            All {view.criteria.length}
          </button>
          {order.map((st) => (
            <button
              key={st}
              type="button"
              aria-pressed={only === st}
              className={`rounded-full border px-3 py-1 text-xs ${statusLook[st].cls} ${only === st ? 'ring-1 ring-current' : ''}`}
              onClick={() => setOnly(only === st ? '' : st)}
            >
              {statusLook[st].label}: {view.counts[st] ?? 0}
            </button>
          ))}
        </div>
      </Section>

      {groups.map((g) => {
        const rows = shown.filter((c) => c.group === g)
        if (rows.length === 0) return null
        return (
          <Section key={g} title={g} icon={groupIcons[g] ?? IconDocuments}>
            <ol className="space-y-3">
              {rows.map((c) => (
                <li key={c.number} className="rounded-lg border border-slate-800 p-3" data-testid={`aligned-${c.number}`}>
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <p className="text-sm text-slate-100">
                      <span className="mr-2 font-mono text-slate-500">{c.number}.</span>
                      {c.title}
                      {c.inEffect && <span className="ml-2 rounded bg-sky-500/10 px-1.5 py-0.5 text-[11px] text-sky-300">in effect</span>}
                    </p>
                    <span className={`shrink-0 rounded-full border px-2.5 py-0.5 text-xs ${statusLook[c.status].cls}`}>{statusLook[c.status].label}</span>
                  </div>
                  {c.perfuse && <p className="mt-1.5 text-sm text-slate-300">{c.perfuse}</p>}
                  {c.missing.length > 0 && (
                    <ul className="mt-1.5 list-disc pl-5 text-sm text-amber-300">
                      {c.missing.map((m) => (
                        <li key={m}>Turn on {m}</li>
                      ))}
                    </ul>
                  )}
                  {c.gap && <p className="mt-1.5 text-sm text-slate-400">{c.gap}</p>}
                  {c.where && <p className="mt-1 text-xs text-slate-500">See {c.where}</p>}
                </li>
              ))}
            </ol>
          </Section>
        )
      })}
      <p className="text-xs text-slate-500">Source: {view.source}.</p>
    </div>
  )
}
