// @vitest-environment happy-dom
import { describe, it, expect } from 'vitest'
import { render, screen, fireEvent, within, cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'
import { BuilderScripts } from './BuilderScripts'
import { emptyDraft, draftToWire } from './model'
import { wireToDraft } from './wireToDraft'
import type { ChannelDraft } from './model'

// WebAssembly in the form.
//
// # Why this file exists
//
// The runtime was built and reachable from YAML, and the form did not mention it. That is a product bug on its own here, because
// the standing rule is that everything is reachable from the GUI - but the reachability was the smaller half.
//
// The larger half was destructive. wireToDraft folded anything that was not lua into javascript, so opening a working WebAssembly
// channel in the form and pressing save rewrote language: wasm to language: javascript while leaving the module path in the filter
// field. A path is not a script, so the channel then failed to load, and the action that broke it was an ordinary edit of an
// unrelated field. The builder drift test did not catch it because that test walks paths: scripts.language was expressible, and the
// value wasm was not.
//
// So the round trip is the assertion that matters, and it is first.

// Cleanup is explicit because this project does not enable the automatic kind: without it a second render leaves the first in the
// document and a label query finds two matches, which reads as an ambiguous selector rather than as leaked state.
afterEach(cleanup)

function scriptDraft(over: Partial<ChannelDraft> = {}): ChannelDraft {
  return { ...emptyDraft(), name: 'scripted', ...over }
}

describe('a WebAssembly channel survives the form', () => {
  it('keeps the language through a wire round trip', () => {
    const original = scriptDraft({
      scriptLanguage: 'wasm',
      filterScript: 'redact.wasm',
    })

    const wire = draftToWire(original) as Record<string, any>
    expect(wire.scripts?.language).toBe('wasm')

    const back = wireToDraft(wire)

    // The defect: this used to come back as javascript, and the next save wrote that to the file.
    expect(back.scriptLanguage).toBe('wasm')

    // And the module path has to survive with it, or the channel loses the thing it runs.
    expect(back.filterScript).toBe('redact.wasm')
  })

  it('still reads an unmarked script as javascript', () => {
    // The Mirth compatibility default, which the fix must not disturb: an absent key is javascript, not wasm.
    const back = wireToDraft({ name: 'plain', scripts: { filter: 'return true' } } as any)

    expect(back.scriptLanguage).toBe('javascript')
  })
})

describe('the script section offers WebAssembly', () => {
  it('lists it as a language', () => {
    render(<BuilderScripts draft={scriptDraft()} onChange={() => {}} />)

    const select = screen.getByLabelText(/language/i) as HTMLSelectElement
    const values = Array.from(select.options).map((o) => o.value)

    expect(values).toContain('wasm')
    expect(values).toContain('lua')
    expect(values).toContain('javascript')
  })

  it('asks for a module path rather than source when it is chosen', () => {
    const { container } = render(
      <BuilderScripts draft={scriptDraft({ scriptLanguage: 'wasm' })} onChange={() => {}} />,
    )

    // No textareas: a module cannot be pasted into one, and offering the box invites exactly that - which would
    // fail with an error about a missing file rather than about the paste.
    expect(container.querySelectorAll('textarea')).toHaveLength(0)

    // The labels have to say module, or somebody types a script into a one-line box and wonders why.
    expect(screen.getByLabelText(/filter module/i)).toBeTruthy()
  })

  it('offers source boxes for the other two languages', () => {
    // The control. Without it the test above passes if the section renders no editors at all.
    for (const language of ['javascript', 'lua'] as const) {
      const { container, unmount } = render(
        <BuilderScripts draft={scriptDraft({ scriptLanguage: language })} onChange={() => {}} />,
      )

      // These were textareas and are now syntax-highlighting editors, so the assertion moved from the element to the role.
      // Stronger than counting textareas was: it also proves each editor carries an accessible name, which a CodeMirror div
      // does not get for free - a surrounding label may only point at a real form control.
      const editors = container.querySelectorAll('[role="textbox"]')
      expect(editors.length).toBeGreaterThan(0)
      for (const editor of editors) {
        expect(editor.getAttribute('aria-label')).toBeTruthy()
      }
      unmount()
    }
  })

  it('reports the language a person picks', () => {
    let picked = ''
    const { container } = render(
      <BuilderScripts
        draft={scriptDraft()}
        onChange={(patch) => {
          picked = String(patch.scriptLanguage ?? '')
        }}
      />,
    )

    fireEvent.change(within(container).getByLabelText(/language/i), {
      target: { value: 'wasm' },
    })

    expect(picked).toBe('wasm')
  })
})
