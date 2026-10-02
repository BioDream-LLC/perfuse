import { CodeArea } from './CodeArea'
import { useState } from 'react'
import { Confirm } from './ui'

// A filter that the rule rows cannot represent, shown as the expression it actually is.
//
// This exists because the honest alternative to guessing is showing the truth. When somebody opens a
// channel whose filter uses brackets, negation or a comparison between two fields, the rule editor
// cannot hold it. The old behaviour was to send the whole channel to a text editor. The behaviour
// before that would have been worse: show empty rule rows, which says "this channel forwards
// everything" about a channel that is filtering, and make it true on the next save.
//
// So the filter is shown as text, in the middle of an otherwise graphical form, with a note saying
// why. Everything else on the page stays editable, which is the point - somebody adding a destination
// to a channel with a complicated filter should not have to hand-edit YAML to do it.

export function RawFilter({
  value,
  onChange,
  onConvert,
}: {
  value: string
  onChange: (value: string) => void
  /** onConvert discards the expression and switches to the rule rows. */
  onConvert: () => void
}) {
  const [confirming, setConfirming] = useState(false)

  return (
    <div className="space-y-2">
      <p className="rounded border border-slate-700 bg-slate-950/40 p-2 text-xs leading-relaxed text-slate-400">
        This filter is more involved than the rule rows can show — it may use brackets, "not", or a
        comparison between two fields. It is shown exactly as written and will be saved exactly as
        written. Everything else on this page can still be edited normally.
      </p>

      <CodeArea
        language="js"
        rows={3}
        value={value}
        onChange={onChange}
        aria-label="Filter expression"
      />

      <button
        type="button"
        className="text-xs text-slate-400 underline hover:text-slate-200"
        onClick={() => setConfirming(true)}
      >
        Replace it with rule rows instead
      </button>

      {/*
        Confirmed, because it throws the expression away. Somebody who clicks this expecting a
        conversion and gets an empty rule list has just deleted their filter, and the form would look
        as though that is what they asked for.
      */}
      <Confirm
        open={confirming}
        title="Discard this filter?"
        body="The rule rows cannot express this filter, so switching to them starts from nothing. The expression will be gone from the form, and saving would remove it from the channel. It stays in the channel's history."
        confirmLabel="Discard it and use rule rows"
        onCancel={() => setConfirming(false)}
        onConfirm={() => {
          setConfirming(false)
          onConvert()
        }}
      />
    </div>
  )
}
