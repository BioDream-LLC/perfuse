import { useEffect, useState } from 'react'
import type { SnmpStatus } from './api'
import { api } from './api'
import { Section } from './ui'
import { IconDownload, IconSuccess, IconStopped, IconWarning } from './Icons'

/** SnmpPanel shows whether the SNMP agent runs and offers the MIB. The values themselves are set with the other Monitoring
 *  settings above; this is the part a typed setting cannot show: what actually happened when the server started. */
export function SnmpPanel() {
  const [st, setSt] = useState<SnmpStatus | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    api.snmp().then(setSt, () => setFailed(true))
  }, [])

  if (failed || !st) return null
  const on = st.running || st.traps.length > 0

  return (
    <Section
      title="SNMP"
      icon={IconDownload}
      description="A read-only SNMPv2c agent and alert traps for monitoring tools that speak SNMP. Set it under Monitoring, SNMP; it starts at the next restart."
      actions={
        <a className="btn-ghost" href="/api/snmp/mib" download="PERFUSE-MIB.txt" data-testid="snmp-mib">
          <IconDownload className="h-4 w-4" aria-hidden="true" /> Download the MIB
        </a>
      }
    >
      <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm" data-testid="snmp-status">
        <dt className="text-slate-500">Agent</dt>
        <dd className="flex items-center gap-1.5">
          {st.running ? (
            <span className="flex items-center gap-1.5 text-emerald-600 dark:text-emerald-400">
              <IconSuccess className="h-4 w-4" aria-hidden="true" /> Answering on {st.address}
            </span>
          ) : st.error ? (
            <span className="flex items-center gap-1.5 text-rose-600 dark:text-rose-400">
              <IconWarning className="h-4 w-4" aria-hidden="true" /> Could not listen on {st.listen}: {st.error}
            </span>
          ) : (
            <span className="flex items-center gap-1.5 text-slate-500">
              <IconStopped className="h-4 w-4" aria-hidden="true" /> Off
            </span>
          )}
        </dd>
        {st.running && (
          <>
            <dt className="text-slate-500">Answers</dt>
            <dd className="font-mono text-xs">{st.allow.join(', ')}</dd>
          </>
        )}
        <dt className="text-slate-500">Traps</dt>
        <dd>
          {st.traps.length === 0 ? (
            <span className="text-slate-500">None sent: no managers set</span>
          ) : (
            <span>
              <span className="font-mono text-xs">{st.traps.join(', ')}</span>
              <span className="ml-2 text-emerald-600 dark:text-emerald-400">{st.trapsSent} sent</span>
              {st.trapFailures > 0 && <span className="ml-2 text-rose-600 dark:text-rose-400">{st.trapFailures} failed</span>}
            </span>
          )}
        </dd>
        {on && (
          <>
            <dt className="text-slate-500">Base OID</dt>
            <dd className="font-mono text-xs">{st.base}</dd>
          </>
        )}
      </dl>
    </Section>
  )
}
