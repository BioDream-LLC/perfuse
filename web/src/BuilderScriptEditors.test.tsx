// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/react'
import { BuilderScripts } from './BuilderScripts'
import { api } from './api'
import type { ChannelDraft } from './model'

// The builder's script boxes are real editors.
//
// # What was wrong
//
// Six plain textareas. The syntax-highlighting editor existed and was used in one place - the workbench - which had it backwards.
// The workbench is where somebody pastes a script to learn whether it survives; the builder is where they write the one that will
// run. The good editor was in the tourist spot and the bare textarea was in the workshop.
//
// It was also the only comparison with Mirth's desktop client that Perfuse lost. That editor highlights JavaScript, so somebody
// migrating met a plainer tool than the one they were leaving.
//
// # What these tests assert, and why not the colours
//
// Not that text is coloured. Token colours in a headless DOM prove very little and would break on a theme change without anything
// being wrong. What matters and is checkable is that each box is a real editor, that it is named for a screen reader, and that the
// kind and language travelling to the compiler are the ones the slot actually needs - because a wrong kind is invisible on screen
// and changes the answer. A filter compiled as a transformer is told it compiles while its return value is being ignored.

vi.mock('./api', () => ({
  api: {
    dictionary: vi.fn().mockResolvedValue({ all: [] }),
    checkScript: vi.fn().mockResolvedValue({ ok: true, kind: 'filter', language: 'javascript', notes: [] }),
  },
}))

function draft(over: Partial<ChannelDraft> = {}): ChannelDraft {
  return {
    name: 'c',
    dataType: 'hl7',
    scriptLanguage: 'javascript',
    preprocessorScript: '',
    filterScript: '',
    transformerScript: '',
    postprocessorScript: '',
    deployScript: '',
    undeployScript: '',
    ...over,
  } as ChannelDraft
}

describe('the builder writes scripts in a real editor', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.mocked(api.checkScript).mockClear()
  })

  afterEach(() => {
    vi.useRealTimers()
    cleanup()
  })

  it('gives every slot an editor with an accessible name', () => {
    const { container } = render(<BuilderScripts draft={draft()} onChange={() => {}} />)

    const editors = container.querySelectorAll('[role="textbox"]')

    // Six slots: preprocessor, filter, transformer, postprocessor, on start, on stop.
    expect(editors.length).toBe(6)

    // A CodeMirror editor is a div, so the surrounding label cannot be associated with it the way it can with a textarea.
    // Without a name of its own each of these announces itself as an anonymous edit box, six times over.
    const names = [...editors].map((e) => e.getAttribute('aria-label'))
    expect(names).toEqual(['Preprocessor', 'Filter', 'Transformer', 'Postprocessor', 'On start', 'On stop'])
  })

  it('compiles each slot as the kind that slot actually runs as', async () => {
    // Content in one slot only, so the assertion cannot be satisfied by another slot's request.
    render(
      <BuilderScripts
        draft={draft({ postprocessorScript: 'return 1' })}
        onChange={() => {}}
      />,
    )
    await vi.advanceTimersByTimeAsync(2000)

    expect(api.checkScript).toHaveBeenCalledWith('return 1', 'postprocessor', 'javascript')
  })

  it('compiles a start hook as a lifecycle script, which has no message', async () => {
    // The distinction that matters most. A lifecycle hook has no message in scope, so checking one as a transformer would report
    // on a script nobody is going to run - and reaching for the message is the mistake an author is most likely to make here.
    render(<BuilderScripts draft={draft({ deployScript: 'return 1' })} onChange={() => {}} />)
    await vi.advanceTimersByTimeAsync(2000)

    expect(api.checkScript).toHaveBeenCalledWith('return 1', 'lifecycle', 'javascript')
  })

  it('sends Lua to the Lua compiler', async () => {
    render(
      <BuilderScripts
        draft={draft({ scriptLanguage: 'lua', filterScript: 'return true' })}
        onChange={() => {}}
      />,
    )
    await vi.advanceTimersByTimeAsync(2000)

    expect(api.checkScript).toHaveBeenCalledWith('return true', 'filter', 'lua')
  })

  it('does not ask the server about empty slots', async () => {
    // Six editors on one page means six requests per pause if nothing guards this, none of which can say anything: the endpoint
    // treats empty source as valid. Cheap to get wrong and invisible unless someone watches the network.
    render(<BuilderScripts draft={draft()} onChange={() => {}} />)
    await vi.advanceTimersByTimeAsync(2000)

    expect(api.checkScript).not.toHaveBeenCalled()
  })

  it('fetches the segment dictionary once for all six editors', async () => {
    // Was once per editor. Same payload, six times, concurrently, five of them discarded.
    render(<BuilderScripts draft={draft()} onChange={() => {}} />)
    await vi.advanceTimersByTimeAsync(2000)

    expect(vi.mocked(api.dictionary).mock.calls.length).toBeLessThanOrEqual(1)
  })
})
