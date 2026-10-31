import { useEffect, useState } from 'react'
import { api } from './api'
import type { PriorAuths } from './api'
import { IconPriorAuth } from './Icons'
import { ErrorBox, Section } from './ui'
import type { UiError } from './store'

const decisionLabel: Record<string, { text: string; className: string }> = {
  approved: { text: 'Approved', className: 'text-emerald-300' },
  partial: { text: 'Partly approved', className: 'text-amber-300' },
  modified: { text: 'Approved, changed', className: 'text-amber-300' },
  denied: { text: 'Denied', className: 'text-rose-300' },
  pended: { text: 'Pending', className: 'text-sky-300' },
  canceled: { text: 'Cancelled', className: 'text-slate-400' },
  unknown: { text: 'No decision code', className: 'text-slate-400' },
}

/**
 * PriorAuthSent is the provider side of CMS-0057: the PAS requests this server's PAS destinations submitted to payers, and
 * what each payer answered. It reads; a request is sent by a channel, never from here.
 */
export function PriorAuthSent() {
  const [data, setData] = useState<PriorAuths | null>(null)
  const [error, setError] = useState<UiError | null>(null)
  useEffect(() => {
    let live = true
    const load = () =>
      api
        .priorAuths()
        .then((d) => live && (setData(d), setError(null)))
        .catch((e: unknown) => live && setError({ message: e instanceof Error ? e.message : String(e), problems: [] }))
    load()
    const t = setInterval(load, 15000)
    return () => {
      live = false
      clearInterval(t)
    }
  }, [])

  return (
    <Section
      title="Requests sent to payers"
      icon={IconPriorAuth}
      description="For a provider: each prior authorization request a channel's PAS destination submitted to a payer's Claim/$submit, newest first, with the payer's answer and its reference. Pended requests stay pending here until the payer's later answer reaches a channel."
    >
      {error && <ErrorBox error={error} />}
      {data && !data.on && <p className="text-sm text-slate-400">This server keeps no record of submitted requests.</p>}
      {data && data.on && (
        <div className="space-y-4" data-testid="prior-auth-sent">
          <div className="flex flex-wrap gap-4 text-sm">
            <span className="text-sky-300">{data.pending} pending</span>
            <span className="text-emerald-300">{data.approved} approved</span>
            <span className="text-rose-300">{data.denied} denied</span>
          </div>
          {data.requests.length === 0 ? (
            <p className="text-sm text-slate-400">
              Nothing submitted yet. Add a destination of type "A payer: submit prior authorization requests" to a raw channel
              that receives PAS request Bundles.
            </p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead className="text-xs uppercase tracking-wide text-slate-500">
                  <tr>
                    <th className="py-2 pr-4">Sent</th>
                    <th className="py-2 pr-4">Request</th>
                    <th className="py-2 pr-4">Patient</th>
                    <th className="py-2 pr-4">Service</th>
                    <th className="py-2 pr-4">Decision</th>
                    <th className="py-2 pr-4">Payer's reference</th>
                    <th className="py-2">Destination</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-800">
                  {data.requests.map((t, i) => {
                    const d = decisionLabel[t.response?.decision ?? 'pended'] ?? { text: 'No decision code', className: 'text-slate-400' }
                    return (
                      <tr key={`${t.request.id}-${i}`}>
                        <td className="py-2 pr-4 text-slate-400">
                          {t.request.submittedAt ? new Date(t.request.submittedAt).toLocaleString() : ''}
                        </td>
                        <td className="py-2 pr-4 font-mono text-xs text-slate-200">{t.request.id}</td>
                        <td className="py-2 pr-4 font-mono text-xs text-slate-300">{t.request.patientId}</td>
                        <td className="py-2 pr-4 text-slate-300">
                          {t.request.serviceCode}
                          {t.request.serviceDescription ? ` ${t.request.serviceDescription}` : ''}
                        </td>
                        <td className={`py-2 pr-4 font-medium ${d.className}`} title={t.response?.reason}>
                          {d.text}
                        </td>
                        <td className="py-2 pr-4 font-mono text-xs text-slate-300">{t.response?.preAuthRef}</td>
                        <td className="py-2 text-slate-400">{t.request.destination}</td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </Section>
  )
}
