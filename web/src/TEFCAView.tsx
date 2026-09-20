import { IconActivity } from './Icons'
import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from './api'
import type { TEFCAStatus, TEFCAAuditPage } from './api'
import { ErrorBox, Section, Spinner } from './ui'
import type { UiError } from './store'

/**
 * National exchange, and the record of it.
 *
 * TEFCA is exchange through a Qualified Health Information Network, and taking part carries an obligation: every
 * exchange has to be recorded, and the records have to exist when somebody asks — typically months later, prompted by a
 * complaint or an investigation.
 *
 * So this section is mostly the audit trail. What it deliberately does not offer is a button that queries the network
 * for a named patient: that would disclose data on somebody's behalf, and the authority being exercised has to be
 * established first. Perfuse exchanges through channels, where the purpose of use and the requesting party come from
 * configuration rather than from whoever happens to be signed in.
 */
export function TEFCAView() {
  const [status, setStatus] = useState<TEFCAStatus | null>(null)
  const [page, setPage] = useState<TEFCAAuditPage | null>(null)
  const [days, setDays] = useState(7)
  const [error, setError] = useState<UiError | null>(null)
  const [check, setCheck] = useState<{ purpose: string; explanation: string; acceptable: boolean } | null>(null)

  const toError = (e: unknown): UiError => ({
    message: e instanceof Error ? e.message : String(e),
    problems: e instanceof ApiError ? e.problems : [],
  })

  const load = useCallback(async () => {
    try {
      const s = await api.tefcaStatus()
      setStatus(s)

      const to = new Date()
      const from = new Date(to.getTime() - days * 24 * 60 * 60 * 1000)
      setPage(await api.tefcaAudit(from.toISOString(), to.toISOString()))
    } catch (e) {
      setError(toError(e))
    }
  }, [days])

  useEffect(() => {
    void load()
  }, [load])

  async function checkPurpose(purpose: string) {
    setCheck(null)
    try {
      const res = await api.tefcaPurposeCheck(purpose)
      setCheck({ purpose, explanation: res.explanation, acceptable: res.acceptable })
    } catch (e) {
      setError(toError(e))
    }
  }

  // The failure is shown before the spinner, because it used to be shown never.
  //
  // A failed status request set error and left status null, and this line returned a spinner unconditionally - so the one screen
  // whose subject is national exchange answered a broken server with an animation that never ends, and the message saying what
  // was wrong was already in state and unreachable. An operator watching a spinner concludes the server is slow and waits; an
  // operator reading a refusal knows to look at the certificate path.
  if (error !== null && status === null) return <ErrorBox error={error} />

  if (status === null) return <Spinner />

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-medium text-slate-100">TEFCA</h2>
        <p className="mt-1 text-sm text-slate-400">
          National exchange through a Qualified Health Information Network. Every exchange must be recorded, and this is
          the record.
        </p>
      </div>

      {error && <ErrorBox error={error} onDismiss={() => setError(null)} />}

      {!status.configured ? (
        <div className="space-y-4">
          <div className="card p-6">
            <p className="text-sm text-slate-300">This instance does not take part in national exchange.</p>
            <p className="mt-2 text-sm text-slate-400">{status.explanation}</p>
            <p className="mt-3 text-xs text-slate-500">
              Once you have an agreement with a QHIN and a certificate issued for it, everything else is under{' '}
              <span className="text-slate-300">Settings</span> in the TEFCA group. Nothing needs editing on this
              server.
            </p>
          </div>

          {/* The purposes, here as well as when configured.
              
              Deciding whether to take part means knowing what would be declared, and that question comes before any
              configuration exists. A section that can only be read about until it is switched on is a section nobody
              can evaluate. */}
          <div className="card p-4">
            <h3 className="text-sm font-medium text-slate-200">What you would be declaring</h3>
            <p className="mt-1 text-xs text-slate-500">
              A participant declares which purposes it exchanges for. Click one to see what this instance would do
              with it today.
            </p>
            <div className="mt-3 flex flex-wrap gap-1.5">
              {status.allPurposes.map((p) => (
                <button
                  key={p}
                  onClick={() => void checkPurpose(p)}
                  aria-label={`Check ${p}`}
                  className="badge border border-slate-700 bg-slate-800/60 text-slate-400 transition hover:text-slate-200"
                >
                  {p}
                </button>
              ))}
            </div>
            {check && (
              <p role="status" className="mt-3 text-xs text-amber-300">
                {check.explanation}
              </p>
            )}
          </div>
        </div>
      ) : (
        <>
          {/* The state of the trail before anything else.
              
              A participant whose audit file has stopped being written is out of compliance right now, and nothing else
              on this page would say so — the exchanges keep working. */}
          {/* The transport gap, said where somebody would otherwise assume the opposite.
              
              A configured participant with no transport looks identical to a working one from this screen, and the difference is the
              whole thing: an operator who believes exchange is running will not go looking for why no records ever arrive. The audit
              trail is real, so it would be filling with failures nobody was watching. */}
          {status.facilitatedFHIRImplemented === true && (
            <div className="rounded-lg border border-sky-500/40 bg-sky-500/10 p-4">
              <p className="text-sm font-medium text-sky-200">
                Facilitated FHIR is implemented. It has never spoken to a real QHIN.
              </p>
              <p className="mt-1 text-xs leading-relaxed text-sky-300/90">
                The query and its UDAP security layer are built, and the security is verified against a live third-party reference
                server — which read a registration signed here, checked its signature, walked its certificate chain, and refused it
                because the certificate is not a member of that community. That is the expected answer and it is the useful one: being
                turned away at the door proves the letter was legible.
              </p>
              <p className="mt-2 text-xs leading-relaxed text-sky-300/90">
                What remains cannot be done from here. A certificate proving membership comes out of QHIN onboarding, and until one
                exists no exchange with a real partner completes. Configure a partner and trust anchors to try.
              </p>
            </div>
          )}

          {/* The older transport, still absent, said separately. One notice covering both would be a lie in whichever direction it
              was rounded. */}
          {status.exchangeImplemented === false && (
            <div className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-4">
              <p className="text-sm font-medium text-amber-200">
                The QHIN-to-QHIN exchange built on the IHE profiles is not implemented.
              </p>
              <p className="mt-1 text-xs leading-relaxed text-amber-300/90">
                Purpose-of-use checking, configuration validation and the audit trail below are real and working. An attempt at this
                transport is refused rather than reported as a success, so the trail shows a failure rather than an exchange. The
                Common Agreement started with these profiles; Facilitated FHIR above is the route this build takes.
              </p>
            </div>
          )}

          {status.trail && !status.trail.writable && (
            <div role="alert" className="rounded-lg border border-rose-800 bg-rose-950/40 p-4">
              <p className="text-sm font-medium text-rose-200">The audit trail is not being written.</p>
              <p className="mt-1 text-xs text-rose-300/90">{status.trail.problem}</p>
            </div>
          )}

          {status.trail?.unreadableLines ? (
            <div role="alert" className="rounded-lg border border-amber-800 bg-amber-950/30 p-4">
              <p className="text-sm text-amber-200">
                {status.trail.unreadableLines} line
                {status.trail.unreadableLines === 1 ? '' : 's'} of the existing trail could not be read.
              </p>
              <p className="mt-1 text-xs text-amber-300/80">{status.trail.unreadableNote}</p>
            </div>
          ) : null}

          <div className="grid gap-4 lg:grid-cols-3">
            <div className="card p-4 lg:col-span-2">
              <h3 className="text-sm font-medium text-slate-200">This participant</h3>
              <dl className="mt-3 grid gap-x-4 gap-y-2 text-xs sm:grid-cols-2">
                <Fact label="Organisation" value={status.organisation} />
                <Fact label="Identifier" value={status.oid} mono />
                <Fact label="Kind" value={status.participantType} />
                <Fact label="QHIN" value={status.qhinEndpoint} mono />
                {status.trail && <Fact label="Audit trail" value={status.trail.path} mono />}
              </dl>
            </div>

            <div className="card p-4">
              <h3 className="text-sm font-medium text-slate-200">Purposes of use</h3>
              <p className="mt-1 text-xs text-slate-500">
                Declared ones are accepted. Clicking any of them says what would happen.
              </p>
              <div className="mt-3 flex flex-wrap gap-1.5">
                {status.allPurposes.map((p) => {
                  const declared = status.purposes.includes(p)
                  return (
                    <button
                      key={p}
                      onClick={() => void checkPurpose(p)}
                      aria-label={`Check ${p}, ${declared ? 'declared' : 'not declared'}`}
                      className={`badge border transition ${
                        declared
                          ? 'border-emerald-800/60 bg-emerald-950/40 text-emerald-300 hover:bg-emerald-950/70'
                          : 'border-slate-700 bg-slate-800/60 text-slate-500 hover:text-slate-300'
                      }`}
                    >
                      {p}
                    </button>
                  )
                })}
              </div>

              {check && (
                <p
                  role="status"
                  className={`mt-3 text-xs ${check.acceptable ? 'text-emerald-300' : 'text-amber-300'}`}
                >
                  {check.explanation}
                </p>
              )}
            </div>
          </div>

          {page && <AuditTrail page={page} days={days} onDays={setDays} />}
        </>
      )}
    </div>
  )
}

function AuditTrail({
  page,
  days,
  onDays,
}: {
  page: TEFCAAuditPage
  days: number
  onDays: (d: number) => void
}) {
  const s = page.summary

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-sm font-medium text-slate-200">What was exchanged</h3>
        <div className="ml-auto flex gap-1">
          {[1, 7, 30, 90].map((d) => (
            <button
              key={d}
              onClick={() => onDays(d)}
              aria-pressed={days === d}
              className={`rounded-lg px-2.5 py-1 text-xs font-medium transition ${
                days === d ? 'bg-slate-800 text-slate-100' : 'text-slate-500 hover:text-slate-300'
              }`}
            >
              {d === 1 ? '24h' : `${d}d`}
            </button>
          ))}
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Stat label="Exchanges" value={String(s.total)} />
        <Stat label="Failed" value={String(s.failed)} tone={s.failed > 0 ? 'rose' : 'slate'} />
        {/* An exchange with no purpose recorded is a compliance gap, not a display gap, so it gets its own number. */}
        <Stat
          label="Without a purpose"
          value={String(s.withoutPurpose)}
          tone={s.withoutPurpose > 0 ? 'amber' : 'slate'}
        />
        {/* The partner responsible, because a run of failures against one organisation is the finding and it is
            invisible in a total. */}
        <Stat label="Most failures" value={s.worstOrg ? `${s.worstOrg} (${s.worstCount})` : '—'} />
      </div>

      {page.truncated && (
        <p className="text-xs text-amber-300">
          Showing the most recent {page.entries.length} of {page.total}. Narrow the window to see the rest — a page that
          silently showed only the first thousand would mislead.
        </p>
      )}

      {page.entries.length === 0 ? (
        <div className="card p-6 text-center">
          <p className="text-sm text-slate-400">No exchanges in this window.</p>
          <p className="mt-1 text-xs text-slate-500">
            Which is a real answer, not a missing one — nothing was disclosed and nothing was asked for.
          </p>
        </div>
      ) : (
        <Section icon={IconActivity} title={`${page.entries.length} recorded`}>
          <ul className="space-y-2">
            {page.entries.map((e, i) => (
              <li
                key={i}
                className={`rounded-lg border p-3 text-xs ${
                  e.success ? 'border-slate-800 bg-slate-900/40' : 'border-rose-900/60 bg-rose-950/20'
                }`}
              >
                <div className="flex flex-wrap items-baseline gap-2">
                  <span className="badge border border-slate-700 bg-slate-800/60 text-slate-400">
                    {e.exchangeType}
                  </span>
                  <span className={e.success ? 'text-emerald-300' : 'text-rose-300'}>
                    {e.success ? 'succeeded' : 'failed'}
                  </span>
                  {e.purpose ? (
                    <span className="text-slate-400">for {e.purpose}</span>
                  ) : (
                    <span className="text-amber-300">no purpose recorded</span>
                  )}
                  <span className="ml-auto font-mono text-[10px] text-slate-500">
                    {new Date(e.timestamp).toLocaleString()}
                  </span>
                </div>

                <div className="mt-1.5 flex flex-wrap gap-x-4 gap-y-1 text-slate-500">
                  {e.patientId && (
                    <span>
                      patient <span className="font-mono text-slate-400">{e.patientId}</span>
                    </span>
                  )}
                  {e.requestingOrg && <span>asked by {e.requestingOrg}</span>}
                  {e.respondingOrg && <span>to {e.respondingOrg}</span>}
                </div>

                {e.errorDetail && <p className="mt-1.5 text-rose-300/90">{e.errorDetail}</p>}
              </li>
            ))}
          </ul>
        </Section>
      )}
    </div>
  )
}

function Fact({ label, value, mono }: { label: string; value?: string; mono?: boolean }) {
  if (!value) return null
  return (
    <div>
      <dt className="text-slate-500">{label}</dt>
      <dd className={`text-slate-200 ${mono ? 'font-mono text-[11px] break-all' : ''}`}>{value}</dd>
    </div>
  )
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: string }) {
  const colour = tone === 'rose' ? 'text-rose-300' : tone === 'amber' ? 'text-amber-300' : 'text-slate-100'
  return (
    <div className="card p-3">
      <p className="text-[11px] tracking-wide text-slate-500 uppercase">{label}</p>
      <p className={`mt-1 text-lg font-medium ${colour}`}>{value}</p>
    </div>
  )
}
