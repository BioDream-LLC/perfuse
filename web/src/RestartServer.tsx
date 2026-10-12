import { useState } from 'react'
import { IconRefresh } from './Icons'
import { api } from './api'
import type { UiError } from './store'
import { toUiError } from './store'
import { ErrorBox } from './ui'

type Phase = 'idle' | 'confirm' | 'restarting' | 'back' | 'timeout'

// How long to wait for the server to come back before saying so. A server with many channels draining their last message
// can take a while; one that is not back after this has a problem somebody needs to look at.
const giveUpAfterMs = 120_000

// RestartServer restarts this server from the console, so settings that only apply at a restart can be applied without
// reaching the machine. It asks first because every channel stops for a few seconds, then waits for the server to answer
// again and calls onBack, so the page can read the settings afresh.
export function RestartServer({ onBack, compact = false }: { onBack?: () => void; compact?: boolean }) {
  const [phase, setPhase] = useState<Phase>('idle')
  const [error, setError] = useState<UiError | null>(null)

  async function restart() {
    setError(null)
    setPhase('restarting')
    try {
      await api.restartServer()
    } catch (err) {
      setError(toUiError(err))
      setPhase('idle')
      return
    }
    // Wait for it to go away, then to come back. Asking /livez too early would find the old process still answering.
    await new Promise((r) => setTimeout(r, 1500))
    const started = Date.now()
    while (Date.now() - started < giveUpAfterMs) {
      try {
        const res = await fetch('/livez', { cache: 'no-store' })
        if (res.ok) {
          setPhase('back')
          onBack?.()
          return
        }
      } catch {
        // Not listening yet.
      }
      await new Promise((r) => setTimeout(r, 1000))
    }
    setPhase('timeout')
  }

  return (
    <div className={compact ? 'inline-flex flex-wrap items-center gap-2' : 'mt-3 flex flex-wrap items-center gap-3'}>
      {phase === 'idle' || phase === 'back' || phase === 'timeout' ? (
        <button type="button" className="btn" onClick={() => setPhase('confirm')}>
          <IconRefresh size={16} />
          Restart the server
        </button>
      ) : null}
      {phase === 'confirm' && (
        <>
          <span className="text-xs text-amber-200">
            Every channel stops for a few seconds and the console reconnects. Messages in flight finish first; senders
            resend anything that arrives meanwhile.
          </span>
          <button type="button" className="btn-primary" onClick={() => void restart()}>
            Restart now
          </button>
          <button type="button" className="btn-ghost" onClick={() => setPhase('idle')}>
            Cancel
          </button>
        </>
      )}
      {phase === 'restarting' && (
        <span role="status" className="text-xs text-amber-200">
          Restarting… waiting for the server to answer again.
        </span>
      )}
      {phase === 'back' && (
        <span role="status" className="text-xs text-emerald-300">
          The server restarted and is running with the saved settings.
        </span>
      )}
      {phase === 'timeout' && (
        <span role="alert" className="text-xs text-red-300">
          The server has not come back after two minutes. Check its log where it runs.
        </span>
      )}
      {error && <ErrorBox error={error} />}
    </div>
  )
}
