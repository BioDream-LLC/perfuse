// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { SettingControl, settingLabelId, type SettingDescriptor } from './settingsControls'

afterEach(cleanup)

// A switch and a slider cannot be pointed at by <label for>, so each is named by the visible label's id. Without it every toggle
// on the settings page was announced as "switch, off" with nothing to say which.
function withLabel(setting: Partial<SettingDescriptor> & { key: string; widget: string; label: string }, value: unknown) {
  return render(
    <div>
      <label id={settingLabelId(setting.key)}>{setting.label}</label>
      <SettingControl setting={setting as SettingDescriptor} value={value} secretIsSet={false} onChange={() => {}} />
    </div>,
  )
}

describe('settings controls are named by their label', () => {
  it('names a toggle', () => {
    withLabel({ key: 'fhir.apiTokensWithSMART', widget: 'toggle', kind: 'bool', label: 'Accept Perfuse API tokens beside SMART tokens' }, false)
    expect(screen.getByRole('switch', { name: 'Accept Perfuse API tokens beside SMART tokens' })).toBeTruthy()
  })
  it('names a slider', () => {
    withLabel({ key: 'data.retentionDays', widget: 'slider', kind: 'int', label: 'Keep messages for', min: 1, max: 3650 }, 30)
    expect(screen.getByRole('slider', { name: 'Keep messages for' })).toBeTruthy()
  })
})
