import { SyntaxBlock } from './SyntaxHighlight'
import { useEffect, useState } from 'react'
import { api } from './api'
import type { ChannelHistory, ChannelVersion } from './api'
import { Confirm, ErrorBox, Spinner } from './ui'

/**
 * Channel history.
 *
 * Mirth charges for this. It charges for it because channels live in a database
 * there, so tracking changes means building a change-tracking system. Here they
 * are files, so the history already exists — this panel only reads it.
 *
 * The one thing worth being loud about is the gap between "saved" and "recorded".
 * An edit through the interface writes a file; it is not history until something
 * commits it. A history page that quietly omitted the change somebody just made
 * would be worse than no history page.
 */
export function ChannelHistoryPanel({
  channel,
  canRestore,
  onRestored,
}: {
  channel: string
  canRestore: boolean
  onRestored?: () => void
}) {
  const [history, setHistory] = useState<ChannelHistory | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<ChannelVersion | null>(null)
  const [loadingHash, setLoadingHash] = useState<string | null>(null)
  const [confirmHash, setConfirmHash] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let live = true
    api
      .channelHistory(channel)
      .then((h) => live && setHistory(h))
      .catch((err) => live && setError(err instanceof Error ? err.message : 'could not read history'))
    return () => {
      live = false
    }
  }, [channel])

  async function open(hash: string) {
    setLoadingHash(hash)
    try {
      setSelected(await api.channelVersion(channel, hash))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not read that version')
    } finally {
      setLoadingHash(null)
    }
  }

  async function restore(hash: string) {
    setBusy(true)
    try {
      await api.restoreChannelVersion(channel, hash)
      setConfirmHash(null)
      setSelected(null)
      setHistory(await api.channelHistory(channel))
      onRestored?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not restore that version')
    } finally {
      setBusy(false)
    }
  }

  if (error) {
    return <ErrorBox error={{ message: error, problems: [] }} onDismiss={() => setError(null)} />
  }
  if (!history) {
    return <Spinner label="Reading history…" />
  }

  if (!history.available) {
    return (
      <div className="rounded-lg border border-slate-800 bg-slate-900/40 p-4">
        <p className="text-sm text-slate-400">History is not available.</p>
        <p className="mt-1 text-xs text-slate-400">{history.reason}</p>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {history.uncommitted && (
        // The important message on this page. Somebody who just saved a change and
        // does not see it listed should be told why rather than concluding the
        // history is broken.
        <div className="rounded-lg border border-amber-900/60 bg-amber-950/30 p-3 text-sm">
          <p className="text-amber-200">This channel has changes that are not committed yet.</p>
          <p className="mt-1 text-xs text-amber-200/70">
            The file on disk is what runs. It becomes part of the history below when it is
            committed — <code className="font-mono">git commit</code> in the channel directory, or
            whatever your deployment already does. Until then the newest entry here is not the
            version in use.
          </p>
        </div>
      )}

      {history.commits.length === 0 ? (
        <p className="text-sm text-slate-500">
          Nothing recorded for this channel yet.
          {history.uncommitted && ' The file exists but has never been committed.'}
        </p>
      ) : (
        <ol className="space-y-2">
          {history.commits.map((commit, i) => (
            <li
              key={commit.hash}
              className="rounded-lg border border-slate-800 bg-slate-900/40 p-3"
            >
              <div className="flex flex-wrap items-baseline gap-2">
                {i === 0 && !history.uncommitted && (
                  <span className="badge border border-emerald-800 bg-emerald-950/40 text-emerald-300">
                    current
                  </span>
                )}
                <span className="font-medium text-slate-200">{commit.subject}</span>
                <span className="ml-auto font-mono text-xs text-slate-400">
                  {commit.shortHash}
                </span>
              </div>

              {commit.body && (
                <p className="mt-1.5 text-xs whitespace-pre-line text-slate-500">{commit.body}</p>
              )}

              <div className="mt-2 flex flex-wrap items-center gap-3 text-xs text-slate-400">
                <span>{commit.author}</span>
                <span>{formatWhen(commit.at)}</span>

                <div className="ml-auto flex gap-2">
                  <button
                    className="text-sky-400 hover:text-sky-300"
                    onClick={() => open(commit.hash)}
                    disabled={loadingHash === commit.hash}
                  >
                    {loadingHash === commit.hash ? 'Loading…' : 'View and compare'}
                  </button>
                  {canRestore && i > 0 && (
                    <button
                      className="text-amber-400 hover:text-amber-300"
                      onClick={() => setConfirmHash(commit.hash)}
                    >
                      Restore
                    </button>
                  )}
                </div>
              </div>
            </li>
          ))}
        </ol>
      )}

      {selected && (
        <div className="card overflow-hidden">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-slate-800 px-4 py-2">
            <span className="text-xs tracking-wide text-slate-500 uppercase">
              Version {selected.hash.slice(0, 8)}
            </span>
            <div className="flex items-center gap-3">
              {!selected.valid && (
                <span className="badge border border-rose-800 bg-rose-950/40 text-rose-300">
                  does not load
                </span>
              )}
              <button
                className="text-xs text-slate-500 hover:text-slate-300"
                onClick={() => setSelected(null)}
              >
                Close
              </button>
            </div>
          </div>

          {!selected.valid && (
            // Worth saying before somebody presses Restore, because the channel
            // would then fail to start and the interface would have offered them
            // the button.
            <div className="border-b border-rose-900/60 bg-rose-950/20 px-4 py-2 text-xs text-rose-200">
              This version no longer passes validation, so restoring it would stop the channel
              from starting: {selected.error}
            </div>
          )}

          {selected.diff && (
            <div>
              <p className="border-b border-slate-800 px-4 py-1.5 text-xs text-slate-500">
                What changed between then and the file on disk now
              </p>
              <pre className="max-h-64 overflow-auto px-4 py-3 font-mono text-xs leading-relaxed">
                {selected.diff.split('\n').map((line, i) => (
                  <div
                    key={i}
                    className={
                      line.startsWith('+') && !line.startsWith('+++')
                        ? 'text-emerald-300'
                        : line.startsWith('-') && !line.startsWith('---')
                          ? 'text-rose-300'
                          : line.startsWith('@@')
                            ? 'text-sky-400'
                            : 'text-slate-500'
                    }
                  >
                    {line || ' '}
                  </div>
                ))}
              </pre>
            </div>
          )}

          <details className="border-t border-slate-800">
            <summary className="cursor-pointer px-4 py-2 text-xs text-slate-500">
              The whole file as it was
            </summary>
            <SyntaxBlock code={selected.yaml} language="yaml" className="max-h-80" />
          </details>
        </div>
      )}

      {confirmHash && (
        <Confirm
          open
          title="Restore this version?"
          body={
            <span className="whitespace-pre-line">
              {`The channel file will be overwritten with this earlier version.\n\n` +
                `Nothing is lost: this is recorded as a new change rather than by rewriting ` +
                `history, so the version you are replacing stays in the log and can be ` +
                `restored in turn.\n\n` +
                `A running channel keeps the version it started with until you restart it.`}
            </span>
          }
          confirmLabel="Restore it"
          onConfirm={() => restore(confirmHash)}
          onCancel={() => setConfirmHash(null)}
        />
      )}
      {busy && <Spinner label="Restoring…" />}
    </div>
  )
}

function formatWhen(iso: string): string {
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return ''
  const seconds = (Date.now() - then) / 1000
  if (seconds < 60) return 'just now'
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  if (seconds < 2592000) return `${Math.floor(seconds / 86400)}d ago`
  return new Date(iso).toLocaleDateString()
}
