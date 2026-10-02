/**
 * @vitest-environment happy-dom
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { copyText } from './clipboard'

// The failure paths are the reason this exists, so they are what is tested.
//
// Six copy buttons called navigator.clipboard.writeText directly. One caught the rejection, silently. The
// rest either threw a TypeError where the API is absent or produced an unhandled promise rejection, and in
// one case the caller went on to display "Link copied" regardless - a button that reported success for a
// copy that never happened.
//
// This matters because Perfuse is deliberately runnable over plain HTTP on a segregated network, which is
// where most hospital interfaces live. In that context the clipboard API is simply not there. A copy button
// that lies about an API token is worse than one that refuses: the token gets pasted wrongly into a
// configuration and debugged for an hour as an authentication fault.

const originalClipboard = navigator.clipboard

function setClipboard(value: unknown) {
  Object.defineProperty(navigator, 'clipboard', { value, configurable: true, writable: true })
}

afterEach(() => {
  setClipboard(originalClipboard)
  vi.restoreAllMocks()
})

describe('copyText', () => {
  it('uses the clipboard API when it is available', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    setClipboard({ writeText })

    expect(await copyText('a token')).toBe('copied')
    expect(writeText).toHaveBeenCalledWith('a token')
  })

  it('falls back rather than throwing when the API is missing', async () => {
    // The plain-HTTP case. Previously this threw a TypeError in one caller, which took the click with it.
    setClipboard(undefined)
    const exec = vi.fn().mockReturnValue(true)
    document.execCommand = exec as unknown as typeof document.execCommand

    expect(await copyText('a token')).toBe('copied')
    expect(exec).toHaveBeenCalledWith('copy')
  })

  it('falls back when the API exists but refuses', async () => {
    // A rejection is ordinary: an unfocused document is enough to cause one.
    setClipboard({ writeText: vi.fn().mockRejectedValue(new Error('Write permission denied')) })
    const exec = vi.fn().mockReturnValue(true)
    document.execCommand = exec as unknown as typeof document.execCommand

    expect(await copyText('a token')).toBe('copied')
    expect(exec).toHaveBeenCalled()
  })

  it('reports failure rather than claiming success when nothing worked', async () => {
    // The property that matters most. Reporting a copy that did not happen is what sends somebody to paste
    // the previous contents of their clipboard into a configuration file.
    setClipboard({ writeText: vi.fn().mockRejectedValue(new Error('denied')) })
    document.execCommand = vi.fn().mockReturnValue(false) as unknown as typeof document.execCommand

    expect(await copyText('a token')).toBe('failed')
  })

  it('never throws, whatever the environment does', async () => {
    setClipboard({
      writeText: vi.fn().mockImplementation(() => {
        throw new Error('synchronous explosion')
      }),
    })
    document.execCommand = vi.fn().mockImplementation(() => {
      throw new Error('and again')
    }) as unknown as typeof document.execCommand

    // No rejection, no exception: a caller cannot forget to handle what cannot happen.
    await expect(copyText('a token')).resolves.toBe('failed')
  })

  it('leaves no stray element behind after the fallback', async () => {
    setClipboard(undefined)
    document.execCommand = vi.fn().mockReturnValue(true) as unknown as typeof document.execCommand

    const before = document.querySelectorAll('textarea').length
    await copyText('a token')
    expect(document.querySelectorAll('textarea').length).toBe(before)
  })
})
