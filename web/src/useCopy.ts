import { useCallback, useRef, useState } from 'react'
import { copyText, type CopyResult } from './clipboard'

/**
 * useCopy gives a copy button the one thing it needs: the ability to say what happened.
 *
 * A copy button with no feedback is indistinguishable from a broken one, and until now several of them were
 * broken - the promise rejection was discarded, so a denied copy looked exactly like a successful one. The
 * person pastes the previous contents of their clipboard and, if it happened to be plausible, may not notice.
 *
 * On failure it says to copy by hand rather than pretending. That is not a nice message, but it is a true one,
 * and it tells somebody on a plain-HTTP LAN why the button cannot do it for them.
 */
export function useCopy(): {
  /** state is null when idle, otherwise the outcome of the last attempt. */
  state: CopyResult | null
  /** label is what to show on or beside the button, or null when idle. */
  label: string | null
  copy: (text: string) => Promise<void>
} {
  const [state, setState] = useState<CopyResult | null>(null)
  const timer = useRef<number | null>(null)

  const copy = useCallback(async (text: string) => {
    const result = await copyText(text)
    setState(result)

    // Clear after a moment so the button returns to normal. Cancel any previous timer, or a quick second
    // press ends up clearing the new message instead of the old one.
    if (timer.current !== null) window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setState(null), result === 'copied' ? 1800 : 6000)
  }, [])

  return {
    state,
    label: state === 'copied' ? 'Copied' : state === 'failed' ? 'Could not copy — select it and press Ctrl+C' : null,
    copy,
  }
}
