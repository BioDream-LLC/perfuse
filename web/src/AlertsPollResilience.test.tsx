/**
 * @vitest-environment happy-dom
 */
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { Alerts, initialAlertsState, nextAlertsState, type AlertsState } from './Alerts'
import { ApiError, api, type AlertRulesDocument, type AlertSnapshot } from './api'

// A poll that fails must not look like a feature that is switched off.
//
// The alerts view reloads every fifteen seconds. It used to treat every failure the
// same way: clear the snapshot, and render "alerting is not enabled on this server".
// Two things went wrong with that. It stated something false about the server's
// configuration on the strength of one dropped request, and because the rules editor
// was mounted inside that branch, it unmounted the editor and discarded whatever the
// administrator had half-typed. Fifteen seconds is short enough that editing a
// threshold and losing it was a matter of ordinary bad luck.
//
// The server already draws the distinction - 501 for not configured, anything else
// for a request that failed - so the fix was to stop throwing that away.

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

function snapshot(): AlertSnapshot {
  return {
    firing: [],
    resolved: [],
    critical: 0,
    warning: 0,
    rules: [{ kind: 'error-rate', threshold: 0.1, severity: 'warning' }],
    enabled: true,
  }
}

function rulesDoc(): AlertRulesDocument {
  return {
    rules: [{ kind: 'error-rate', threshold: 0.1, severity: 'warning' }],
    kinds: [
      {
        kind: 'error-rate',
        label: 'Errors',
        summary: 'share of messages that failed',
        detail: 'Counted over a rolling window.',
        unit: 'share',
        direction: 'above',
        default: 0.1,
        channel: true,
        destination: false,
      },
    ],
    channels: ['adt'],
    severities: ['warning', 'critical'],
    path: '/etc/perfuse/alerts.yaml',
    writable: true,
    enabled: true,
  }
}

const good: AlertsState = { snapshot: snapshot(), disabled: false, loading: false }

describe('the alerts view under a failing poll', () => {
  it('keeps an unsaved rule edit when a poll fails', async () => {
    vi.spyOn(api, 'alertRules').mockResolvedValue(rulesDoc())

    const view = render(<Alerts role="admin" alerts={good} />)

    // Wait for the editor rather than asserting immediately: it loads in a promise,
    // and an assertion that runs first passes against an empty panel.
    const threshold = (await waitFor(() =>
      screen.getByLabelText(/^Threshold/i),
    )) as HTMLInputElement

    fireEvent.change(threshold, { target: { value: '42' } })
    expect((screen.getByLabelText(/^Threshold/i) as HTMLInputElement).value).toBe('42')

    // A poll fails. Not a 501 - the server is there, the request did not land.
    view.rerender(
      <Alerts role="admin" alerts={{ snapshot: null, disabled: false, loading: false }} />,
    )

    // The edit is still there. If the editor is remounted it refetches and this
    // reverts to the server's 10, which is the data loss this test exists to catch.
    expect((screen.getByLabelText(/^Threshold/i) as HTMLInputElement).value).toBe('42')

    // And the view does not claim the feature is off on the strength of one failure.
    expect(screen.queryByText(/not enabled on this server/i)).toBeNull()
    expect(screen.getByText(/could not reach the server/i)).toBeTruthy()
  })

  it('does say alerting is off when the server says so', async () => {
    vi.spyOn(api, 'alertRules').mockResolvedValue(rulesDoc())

    const view = render(<Alerts role="admin" alerts={good} />)
    await waitFor(() => screen.getByLabelText(/^Threshold/i))

    view.rerender(
      <Alerts role="admin" alerts={{ snapshot: null, disabled: true, loading: false }} />,
    )

    expect(screen.getByText(/not enabled on this server/i)).toBeTruthy()
    expect(screen.queryByText(/could not reach the server/i)).toBeNull()
  })

  it('does not report nothing wrong before the first load finishes', () => {
    vi.spyOn(api, 'alertRules').mockResolvedValue(rulesDoc())

    render(
      <Alerts role="admin" alerts={{ snapshot: null, disabled: false, loading: true }} />,
    )

    // "Nothing is wrong" on an empty snapshot would be a reassurance the server has
    // not given yet.
    expect(screen.queryByText(/nothing is wrong/i)).toBeNull()
    expect(screen.queryByText(/not enabled on this server/i)).toBeNull()
  })
})

describe('what one poll result means', () => {
  // Tested directly rather than through a fake clock. Driving the interval from a
  // test passed whatever the logic said, which is worse than having no test.
  const withSnapshot: AlertsState = { snapshot: snapshot(), disabled: false, loading: false }

  it('reports alerting off only for 501', () => {
    const got = nextAlertsState(withSnapshot, { error: new ApiError(501, 'not enabled') })
    expect(got.disabled).toBe(true)
    expect(got.snapshot).toBeNull()
  })

  it('keeps the last snapshot when a poll fails for any other reason', () => {
    for (const status of [0, 401, 500, 502, 503, 504]) {
      const got = nextAlertsState(withSnapshot, { error: new ApiError(status, 'nope') })
      expect(got.snapshot, `status ${status} discarded the snapshot`).not.toBeNull()
      expect(got.disabled, `status ${status} claimed alerting is off`).toBe(false)
    }
  })

  it('keeps the last snapshot when the failure is not an ApiError at all', () => {
    // A dropped connection arrives as a TypeError from fetch, not an ApiError.
    const got = nextAlertsState(withSnapshot, { error: new TypeError('network error') })
    expect(got.snapshot).not.toBeNull()
    expect(got.disabled).toBe(false)
  })

  it('clears the disabled flag once the server answers again', () => {
    const off: AlertsState = { snapshot: null, disabled: true, loading: false }
    const got = nextAlertsState(off, { snapshot: snapshot() })
    expect(got.disabled).toBe(false)
    expect(got.snapshot).not.toBeNull()
  })

  it('stops loading whatever happens, so the view cannot hang on "Loading."', () => {
    expect(nextAlertsState(initialAlertsState, { snapshot: snapshot() }).loading).toBe(false)
    expect(nextAlertsState(initialAlertsState, { error: new ApiError(501, 'x') }).loading).toBe(
      false,
    )
    expect(nextAlertsState(initialAlertsState, { error: new TypeError('x') }).loading).toBe(false)
  })
})
