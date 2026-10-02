import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from './api'
import type { SubscriptionsView, SubscriptionSummary } from './api'
import { ErrorBox, Section, Spinner } from './ui'
import type { UiError } from './store'

/**
 * FHIR subscriptions, and whether their notifications are arriving.
 *
 * The question this page exists to answer is "did the ED get told?" - so each subscription shows how many events it has had,
 * how many were accepted, how many are waiting, and the last reason one was refused. What it never shows is the credential a
 * subscriber gave the server: header values and the endpoint's query string are withheld by the API, not merely hidden here.
 *
 * Subscriptions are created by FHIR clients through the FHIR API, not from this page. A button here that pointed notifications at
 * a URL would make the web session a second, less audited way to send patient data somewhere.
 */
export function Subscriptions() {
  const [view, setView] = useState<SubscriptionsView | null>(null)
  const [error, setError] = useState<UiError | null>(null)

  const load = useCallback(async () => {
    try {
      setView(await api.fhirSubscriptions())
      setError(null)
    } catch (e) {
      setError({ message: e instanceof Error ? e.message : String(e), problems: e instanceof ApiError ? e.problems : [] })
    }
  }, [])

  useEffect(() => {
    void load()
    const timer = window.setInterval(() => void load(), 10000)
    return () => window.clearInterval(timer)
  }, [load])

  if (error !== null && view === null) return <ErrorBox error={error} />
  if (view === null) return <Spinner label="Loading subscriptions…" />

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
       <div>
        <h2 className="text-lg font-medium text-slate-100">Subscriptions</h2>
        <p className="mt-1 text-sm text-slate-400">
          Encounter and appointment notifications, sent to the systems that asked for them, following the FHIR Subscriptions
          R5 Backport IG for R4. This is what CMS Interoperability Framework criterion 15 asks for.
        </p>
       </div>
        {/* It refreshes itself every ten seconds, but somebody who has just sent a test encounter wants to know now. */}
        <button className="btn-ghost shrink-0" onClick={() => void load()}>
          Refresh
        </button>
      </div>

      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

      {!view.enabled ? (
        <div className="card p-6" data-testid="subscriptions-off">
          <p className="text-sm text-slate-300">Subscriptions are off on this server.</p>
          <p className="mt-2 text-sm text-slate-400">
            {view.fhirEnabled
              ? 'They are off unless asked for, because once on, a FHIR client can make this server send patient data to a URL of its choosing.'
              : 'The FHIR endpoint itself is off, and subscriptions need it.'}{' '}
            Start the server with <code className="text-slate-200">-fhir-subscriptions</code> to turn them on. Endpoints must be
            https unless <code className="text-slate-200">-fhir-subscriptions-allow-http</code> is also given, which is for a
            test receiver and never for patient data on a network.
          </p>
        </div>
      ) : view.subscriptions.length === 0 ? (
        <div className="card p-6">
          <p className="text-sm text-slate-300">No subscriptions yet.</p>
          <p className="mt-2 text-sm text-slate-400">
            A client creates one by sending a Subscription resource to <code className="text-slate-200">/fhir/Subscription</code>{' '}
            naming one of the topics below. The server sends a handshake, and marks it active only if the endpoint accepts.
          </p>
        </div>
      ) : (
        <div className="space-y-3">
          {view.subscriptions.map((s) => (
            <SubscriptionCard key={s.id} s={s} />
          ))}
        </div>
      )}

      <Section title="Topics" description="What a client can subscribe to. Each fires when a resource of that type is created or updated.">
        <ul className="space-y-3">
          {view.topics.map((t) => (
            <li key={t.url} className="text-sm">
              <p className="text-slate-200">{t.title}</p>
              <p className="font-mono text-xs break-all text-slate-400">{t.url}</p>
              <p className="mt-1 text-xs text-slate-400">
                Filters: {t.filters.map((f) => `${t.resourceType}?${f}=`).join(', ')}
              </p>
            </li>
          ))}
        </ul>
      </Section>
    </div>
  )
}

const statusStyle: Record<string, string> = {
  active: 'border-emerald-700 bg-emerald-900/30 text-emerald-300',
  requested: 'border-sky-700 bg-sky-900/30 text-sky-300',
  error: 'border-rose-700 bg-rose-900/30 text-rose-300',
  off: 'border-slate-700 bg-slate-800/60 text-slate-400',
}

function SubscriptionCard({ s }: { s: SubscriptionSummary }) {
  return (
    <div className="card p-4" data-testid="subscription">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="text-sm text-slate-200">
            {s.topicTitle || s.topic} <span className="font-mono text-xs text-slate-400">Subscription/{s.id}</span>
          </p>
          <p className="font-mono text-xs break-all text-slate-400">{s.endpoint}</p>
        </div>
        <span className={`badge border ${statusStyle[s.status] ?? statusStyle.off}`}>{s.status}</span>
      </div>

      <dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-1 text-xs sm:grid-cols-4">
        <div>
          <dt className="text-slate-400">Events</dt>
          <dd className="text-slate-200">{s.events}</dd>
        </div>
        <div>
          <dt className="text-slate-400">Delivered through</dt>
          <dd className="text-slate-200">{s.delivered}</dd>
        </div>
        <div>
          <dt className="text-slate-400">Waiting</dt>
          <dd className={s.pending > 0 ? 'text-amber-300' : 'text-slate-200'}>{s.pending}</dd>
        </div>
        <div>
          <dt className="text-slate-400">Payload</dt>
          <dd className="text-slate-200">{s.payload}</dd>
        </div>
      </dl>

      <p className="mt-2 text-xs text-slate-400">
        {s.filter ? <>Filter: <span className="font-mono text-slate-300">{s.filter}</span></> : 'No filter: every change to this resource type.'}
        {s.headerNames.length > 0 && <> · Sends headers: {s.headerNames.join(', ')} (values withheld)</>}
        {s.lastSuccess && <> · Last accepted {new Date(s.lastSuccess).toLocaleString()}</>}
      </p>
      {(s.error || s.lastError) && (
        <p role="status" className="mt-2 text-xs text-rose-300">
          {s.error || s.lastError}
        </p>
      )}
    </div>
  )
}
