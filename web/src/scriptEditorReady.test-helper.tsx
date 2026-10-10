import { render, waitFor } from '@testing-library/react'
import { BuilderScripts } from './BuilderScripts'
import { emptyDraft } from './model'

// The builder's script editor is loaded lazily. A test that looks for the editors straight after render would find the loading
// placeholder, so a test file calls this once first: after it, the module is resolved and every later render is synchronous, fake
// timers or not.
export async function scriptEditorReady(): Promise<void> {
  const r = render(<BuilderScripts draft={emptyDraft()} onChange={() => {}} />)
  await waitFor(() => {
    if (!r.container.querySelector('[role="textbox"]')) throw new Error('the script editor has not loaded')
  })
  r.unmount()
}
