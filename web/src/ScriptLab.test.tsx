// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup, within } from '@testing-library/react'
import { ScriptLab } from './ScriptLab'
import { api } from './api'

// The workbench, which was JavaScript only.
//
// # What is being tested
//
// The endpoint behind this compiles Lua now, and the form offers Lua as a channel language, so a person writing one needs somewhere
// to find out whether it compiles before deploying. That is the whole point of the workbench: Mirth makes you deploy to find out.
//
// The assertions are on what reaches checkScript, because that is the only thing that distinguishes a selector which works from one
// which renders. A language button that looked selected and sent javascript would leave somebody reading a syntax error in correct
// Lua and concluding the runtime is broken - and every "the control is visible" test would pass.

function stubCheck() {
  return vi.spyOn(api, 'checkScript').mockResolvedValue({
    ok: true,
    kind: 'transformer',
    language: 'javascript',
    rewritten: false,
    notes: [],
  })
}

describe('the script workbench', () => {
  let spy: ReturnType<typeof stubCheck>

  beforeEach(() => {
    vi.useFakeTimers()
    spy = stubCheck()
    // The dictionary request is unrelated to this and would otherwise fail noisily.
    vi.spyOn(api, 'dictionary').mockResolvedValue({ all: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    cleanup()
  })

  it('offers both languages', () => {
    const { container } = render(<ScriptLab />)

    expect(within(container).getByRole('button', { name: 'JavaScript' })).toBeTruthy()
    expect(within(container).getByRole('button', { name: 'Lua' })).toBeTruthy()
  })

  it('compiles as Lua once Lua is chosen', async () => {
    const { container } = render(<ScriptLab />)

    fireEvent.click(within(container).getByRole('button', { name: 'Lua' }))

    // The editor debounces before asking the server, which is why the timers are faked.
    await vi.advanceTimersByTimeAsync(2000)

    expect(spy).toHaveBeenCalled()

    const languages = spy.mock.calls.map((c) => c[2])
    expect(languages).toContain('lua')
  })

  it('shows only the samples for the chosen language', () => {
    const { container } = render(<ScriptLab />)

    // A sample for a language you are not using is noise, and offering one meant a click could leave the editor in
    // a state the language selector disagreed with. Scoping the list removes that by construction rather than
    // guarding against it - which also removed a real defect: a label reading "Lua set a field" contained the
    // existing "Set a field", so anything looking a button up by name found two.
    expect(within(container).getByRole('button', { name: 'Set a field' })).toBeTruthy()
    expect(within(container).queryByRole('button', { name: 'Write a field' })).toBeNull()

    fireEvent.click(within(container).getByRole('button', { name: 'Lua' }))

    expect(within(container).getByRole('button', { name: 'Write a field' })).toBeTruthy()
    expect(within(container).queryByRole('button', { name: 'Set a field' })).toBeNull()
  })

  it('compiles a loaded Lua sample as Lua', async () => {
    const { container } = render(<ScriptLab />)

    fireEvent.click(within(container).getByRole('button', { name: 'Lua' }))
    fireEvent.click(within(container).getByRole('button', { name: 'Filter a message' }))

    await vi.advanceTimersByTimeAsync(2000)

    const lastCall = spy.mock.calls[spy.mock.calls.length - 1]
    expect(lastCall?.[2]).toBe('lua')

    // And the source really is the Lua one, so the button is not merely switching the language.
    expect(String(lastCall?.[0])).toContain('msg.child("PID")')
  })

  it('says why a WebAssembly script cannot be checked here', () => {
    render(<ScriptLab />)

    // A module is compiled output, so there is nothing to paste. Saying so is the difference between a boundary and an
    // omission, and somebody who chose wasm in the channel form will come here looking for it.
    expect(screen.getByText(/module is compiled output/i)).toBeTruthy()
  })

  // All seven slots are reachable, and choosing one changes what is asked of the compiler.
  //
  // The selector offered two of the seven for months. A test that counted buttons would pass on a row of seven that all sent
  // "transformer", so the assertion is on the kind that reaches checkScript.
  it('offers every slot a script can occupy and sends the chosen one', async () => {
    const { container } = render(<ScriptLab />)

    // A sample puts source in the editor. Without it the pane treats an empty script as valid and never calls the endpoint,
    // so every assertion below would be about nothing.
    // A JavaScript sample, because the sample list follows the selected language and JavaScript is the default. Naming a Lua
    // one here fails with the list working correctly, which is a confusing way to learn that.
    fireEvent.click(within(container).getByRole('button', { name: 'Mirth filter' }))
    await vi.advanceTimersByTimeAsync(2000)

    for (const kind of [
      'preprocessor',
      'filter',
      'transformer',
      'writer',
      'postprocessor',
      'lifecycle',
      'reader',
    ]) {
      fireEvent.click(within(container).getByRole('button', { name: kind }))
      await vi.advanceTimersByTimeAsync(2000)

      // The editor is CodeMirror, so there is no value setter to fire a change at - the sample loaded above is the
      // source, and the assertion is on the kind travelling with it. Changing the kind re-checks on its own, which is
      // the behaviour that makes the selector worth having.
      expect(api.checkScript).toHaveBeenCalledWith(expect.any(String), kind, expect.any(String))
    }
  })

  // The guidance has to follow the selector, because what each slot expects is the part an author cannot guess. A lifecycle hook
  // having no message is the clearest case: get that wrong and the script looks broken for a reason the pane could have explained.
  it('describes what the selected slot expects', () => {
    const { container } = render(<ScriptLab />)

    fireEvent.click(within(container).getByRole('button', { name: 'lifecycle' }))
    expect(within(container).getByText(/no message in scope/i)).toBeTruthy()

    fireEvent.click(within(container).getByRole('button', { name: 'preprocessor' }))
    expect(within(container).getByText(/before the message is parsed/i)).toBeTruthy()
  })
})
