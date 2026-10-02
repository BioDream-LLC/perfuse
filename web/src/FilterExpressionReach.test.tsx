// @vitest-environment happy-dom
import { describe, it, expect, afterEach } from 'vitest'
import { render, fireEvent, cleanup, within } from '@testing-library/react'
import { RuleBuilder } from './RuleBuilder'
import { commonPaths, rulesToExpression } from './model'

// A filter had to be reachable from the form on every format, and was not.
//
// # The gap
//
// The rule rows offer a closed list of fields, and that list is HL7 v2 paths - MSH-9.1, PID-3.1, PV1-2. On an X12, NCPDP,
// delimited or raw channel none of those exist, so the rows can express nothing useful, and there is no free-text field.
//
// The expression editor could handle any of them, but it could only be left, never entered: the convert button clears it and
// nothing set it. The only way in was to load YAML whose filter the rows could not represent. So a person creating one of those
// channels in the browser could not give it a filter at all, while the server accepts one - confirmed against the running binary,
// which reads back filter REF02 == "CLAIM123" on an X12 channel without complaint.
//
// # Why the drift guard missed it
//
// builddriftpath_test.go walks config paths, and filter is expressible, so nothing looked absent. The same blind spot let
// language: wasm through: the path existed and the value had no control. A path-walking check cannot see this class of gap.

afterEach(cleanup)

describe('the filter rule rows', () => {
  it('offers only HL7 v2 fields, which is why an expression must be reachable', () => {
    // Not a complaint about the list - it is curated on purpose, so somebody who does not know HL7 can still write a
    // useful filter. It is the reason the other route has to exist.
    expect(commonPaths.length).toBeGreaterThan(0)
    expect(commonPaths.every((p) => /^(MSH|PID|PV1|OBR|OBX)/.test(p.path))).toBe(true)
  })

  it('turns rules into an expression without losing them', () => {
    // The switch is seeded from the current rows, so a person who built two conditions and then needed something the rows
    // cannot say does not start from an empty box.
    const expression = rulesToExpression([
      { id: 'a', path: 'MSH-9.1', operator: '==', value: 'ADT', values: [], join: 'and' },
    ])

    expect(expression).toContain('MSH-9.1')
    expect(expression).toContain('ADT')
  })

  it('keeps a hand-written path selectable so loading one does not silently drop it', () => {
    // An X12 path arrives this way today, from YAML. It must survive being displayed, or opening the channel and saving
    // would rewrite the filter - which is how the wasm language field was being destroyed.
    const { container } = render(
      <RuleBuilder
        rules={[{ id: 'a', path: 'REF02', operator: '==', value: 'CLAIM123', values: [], join: 'and' }]}
        onChange={() => {}}
        emptyHint="none"
      />,
    )

    const field = within(container).getByLabelText('Field') as HTMLSelectElement
    expect(field.value).toBe('REF02')

    // And it is genuinely among the options rather than merely being the value of a select that would coerce on save.
    expect(Array.from(field.options).some((o) => o.value === 'REF02')).toBe(true)
  })

  it('reports a changed rule to its owner', () => {
    // Guards the wiring the escape hatch depends on: the button seeds itself from draft.rules, so rules that never
    // reached the draft would seed an empty expression.
    let seen: unknown = null

    const { container } = render(
      <RuleBuilder
        rules={[{ id: 'a', path: 'MSH-9.1', operator: '==', value: 'ADT', values: [], join: 'and' }]}
        onChange={(rules) => {
          seen = rules
        }}
        emptyHint="none"
      />,
    )

    fireEvent.change(within(container).getByLabelText('Value'), { target: { value: 'ORU' } })

    expect(seen).not.toBeNull()
    expect(JSON.stringify(seen)).toContain('ORU')
  })
})
