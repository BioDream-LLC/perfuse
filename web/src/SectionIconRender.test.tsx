// @vitest-environment happy-dom
import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/react'
import { Section } from './ui'
import { IconShield } from './Icons'

// The icon has to actually reach the screen.
//
// SectionIcons.test.ts checks that every heading has an entry in the registry. That is the mapping, not the rendering, and the two are
// different claims: deleting the lookup inside Section left the whole suite green - three hundred and thirty-one tests passing while
// no section had an icon at all. A complete registry that nothing reads is the same defect as an empty one.
//
// So this asserts what a person would see, which is the only version of the claim worth making.

describe('a section renders its icon', () => {
  afterEach(cleanup)

  it('draws an icon looked up from the heading', () => {
    const { container } = render(
      <Section title="Find a message">
        <p>body</p>
      </Section>,
    )

    expect(container.querySelector('svg')).toBeTruthy()
  })

  it('prefers an explicit icon over the lookup', () => {
    // Needed where the heading is built at run time and there is nothing to look up.
    const { container } = render(
      <Section title="Something computed at run time" icon={IconShield}>
        <p>body</p>
      </Section>,
    )

    expect(container.querySelector('svg')).toBeTruthy()
  })

  it('hides the icon from screen readers', () => {
    // The heading beside it already says what the section is, so announcing the icon too would read every section twice. This is
    // the correct treatment for an icon that duplicates adjacent text, and the wrong treatment is a label.
    const { container } = render(
      <Section title="Find a message">
        <p>body</p>
      </Section>,
    )

    const holder = container.querySelector('[aria-hidden="true"]')
    expect(holder).toBeTruthy()
    expect(holder?.querySelector('svg')).toBeTruthy()
  })

  it('still renders a heading with no icon rather than failing', () => {
    // A missing entry is a test failure elsewhere, not a broken page. Somebody adding a section mid-change should see their work,
    // not a crash.
    const { container } = render(
      <Section title="No icon for this one">
        <p>body</p>
      </Section>,
    )

    expect(container.textContent).toContain('No icon for this one')
    expect(container.querySelector('svg')).toBeNull()
  })
})
