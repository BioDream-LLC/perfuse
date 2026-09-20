import { describe, expect, it } from 'vitest'
import {
  effectLabel,
  isKnownWidget,
  secretInputValue,
  secretPlaceholder,
  showsNoLimitHint,
} from './settingsControls'

describe('what an effect badge says', () => {
  it('says a restart is needed when it is', () => {
    // The only thing standing between an operator and believing a change took effect when it did
    // not, which is why the wording is asserted rather than merely the branch being taken.
    expect(effectLabel('restart')).toMatch(/needs a restart/i)
  })

  it('says a change is immediate when it is', () => {
    expect(effectLabel('live')).toMatch(/at once/i)
  })

  it('distinguishes new connections from both of those', () => {
    expect(effectLabel('reconnect')).toMatch(/new connections/i)
    expect(effectLabel('reconnect')).not.toMatch(/at once/i)
    expect(effectLabel('reconnect')).not.toMatch(/restart/i)
  })

  it('treats anything it does not recognise as needing a restart', () => {
    // A server newer than this interface. Understating what has happened is the safe direction:
    // somebody restarts unnecessarily, rather than believing a change is live when it is not.
    expect(effectLabel('something-new' as 'live')).toMatch(/needs a restart/i)
  })
})

describe('a secret box', () => {
  it('never puts a value in its input, even when handed one', () => {
    // The server does not send secret values. This is the second line rather than the first, but a
    // control that would display one if handed one is a control waiting for a change elsewhere to
    // turn into a disclosure.
    expect(secretInputValue('hunter2')).toBe('hunter2') // what the operator is typing
    expect(secretInputValue(undefined)).toBe('')
    expect(secretInputValue(null)).toBe('')
    expect(secretInputValue(12345)).toBe('')
    expect(secretInputValue({ toString: () => 'sneaky' })).toBe('')
  })

  it('distinguishes configured from blank without revealing anything', () => {
    const set = secretPlaceholder(true)
    const unset = secretPlaceholder(false)

    expect(set).not.toBe(unset)
    expect(set).toMatch(/set/i)
    expect(unset).toMatch(/not set/i)
    // Neither may contain anything that looks like a value.
    expect(set).not.toMatch(/[a-f0-9]{16,}/)
  })
})

describe('the no-limit hint on a slider', () => {
  it('appears for zero on a scale that starts at zero', () => {
    // Zero means "keep everything forever" rather than one less than one, and that is worth saying
    // in words next to the control.
    expect(showsNoLimitHint(0, 0)).toBe(true)
  })

  it('does not appear for an ordinary value', () => {
    expect(showsNoLimitHint(30, 0)).toBe(false)
    expect(showsNoLimitHint(1, 0)).toBe(false)
  })

  it('does not appear where zero is simply the bottom of a range', () => {
    // Trace sampling at zero percent is off, not unlimited, and an hour-of-day of zero is midnight.
    // Both start at zero, so the hint is only right where zero genuinely means no bound.
    expect(showsNoLimitHint(0, 1)).toBe(false)
    expect(showsNoLimitHint(0, undefined)).toBe(false)
  })
})

describe('an unrecognised widget', () => {
  it('is recognised as unrecognised', () => {
    expect(isKnownWidget('slider')).toBe(true)
    expect(isKnownWidget('code')).toBe(true)
    expect(isKnownWidget('something-new')).toBe(false)
    expect(isKnownWidget('')).toBe(false)
  })

  it('covers every widget the server can currently ask for', () => {
    // Kept in step with the Go side by hand, which is why it is listed here: a widget the server
    // sends and this interface does not know about renders as a plain text box, and that should be a
    // deliberate fallback rather than a surprise.
    for (const widget of [
      'toggle',
      'slider',
      'number',
      'text',
      'secret',
      'radio',
      'select',
      'list',
      'code',
    ]) {
      expect(isKnownWidget(widget)).toBe(true)
    }
  })
})
