/**
 * @vitest-environment happy-dom
 */
import { afterEach, describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { Confirm, Field } from './ui'

// Whether a form label actually names its control.
//
// Field took an optional htmlFor and almost no caller passed one, so most labels in the product named
// nothing at all. The visible result is nil - the text sits above the box either way - which is why it
// survived: it looks right, and is only wrong for somebody using a screen reader, or clicking the label
// expecting focus, or writing a test that locates a field by its label.
describe('Field', () => {
  it('associates its label with a single control', () => {
    render(
      <Field label="Channel name">
        <input />
      </Field>,
    )

    // getByLabelText only resolves through a real association: htmlFor to id, or a wrapping label.
    const input = screen.getByLabelText('Channel name')
    expect(input.tagName).toBe('INPUT')
  })

  it('works for a select and a textarea too', () => {
    render(
      <>
        <Field label="Message format">
          <select>
            <option>HL7 v2</option>
          </select>
        </Field>
        <Field label="The script">
          <textarea />
        </Field>
      </>,
    )

    expect(screen.getByLabelText('Message format').tagName).toBe('SELECT')
    expect(screen.getByLabelText('The script').tagName).toBe('TEXTAREA')
  })

  it('leaves an id the caller set alone', () => {
    render(
      <Field label="Moment shown">
        <input id="flow-scrubber" />
      </Field>,
    )

    const input = screen.getByLabelText('Moment shown')
    expect(input.getAttribute('id')).toBe('flow-scrubber')
  })

  it('honours an explicit htmlFor', () => {
    render(
      <Field label="Listen on" htmlFor="chosen-id">
        <input id="chosen-id" />
      </Field>,
    )

    expect(screen.getByLabelText('Listen on').getAttribute('id')).toBe('chosen-id')
  })

  it('gives two fields with the same label different ids', () => {
    // Two destinations both have a Name field. Sharing an id would make the second label point at the
    // first box, which is worse than no association: it is a confidently wrong answer.
    render(
      <>
        <Field label="Name">
          <input defaultValue="first" />
        </Field>
        <Field label="Name">
          <input defaultValue="second" />
        </Field>
      </>,
    )

    const inputs = screen.getAllByLabelText('Name')
    expect(inputs).toHaveLength(2)
    expect(inputs[0]!.getAttribute('id')).not.toBe(inputs[1]!.getAttribute('id'))
  })

  it('does not point its label at something that is not a control', () => {
    // A label's `for` may only reference an input, select or textarea. Pointing it at a div looks
    // associated to anyone reading the markup and resolves to something assistive technology will not
    // treat as a control, which is worse than leaving it off. This case is real: the message content
    // search wraps an input and a button in a div, and the label silently claimed the div.
    const { container } = render(
      <Field label="In the message">
        <div>
          <input />
          <button type="button">Search</button>
        </div>
      </Field>,
    )

    const label = container.querySelector('label')!
    const target = label.getAttribute('for')
    if (target) {
      const el = container.querySelector(`#${CSS.escape(target)}`)
      expect(
        el && ['INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName),
        `the label points at a ${el?.tagName ?? 'missing element'}`,
      ).toBe(true)
    }
  })

  it('renders a compound field without breaking', () => {
    // A slider paired with a number box is two controls, so it cannot be associated by cloning one
    // child. It must still render, and the label must still be shown.
    render(
      <Field label="Retention">
        <div>
          <input type="range" aria-label="Retention slider" />
          <input type="number" aria-label="Retention days" />
        </div>
      </Field>,
    )

    expect(screen.getByText('Retention')).toBeTruthy()
    expect(screen.getByLabelText('Retention slider')).toBeTruthy()
    expect(screen.getByLabelText('Retention days')).toBeTruthy()
  })

  it('still shows the hint', () => {
    render(
      <Field label="Channel name" hint="Used in logs, metrics and the filename.">
        <input />
      </Field>,
    )

    expect(screen.getByText('Used in logs, metrics and the filename.')).toBeTruthy()
  })
})

// The confirmation dialog is the last thing between somebody and a destructive action.
//
// It was a plain div: no dialog role, so nothing was announced; no focus move, so the next Tab went to the
// page behind it; no Escape, so the only exit was finding Cancel by sight. On the page whose purpose is
// stopping a misbehaving interface, at the moment somebody is most likely to be in a hurry.
describe('Confirm', () => {
  // Cleaned up between cases: this component renders a fixed overlay, so a leftover one from the previous
  // test makes every role query ambiguous.
  afterEach(() => {
    cleanup()
  })

  const props = {
    open: true,
    title: 'Stop labs?',
    body: 'The channel finishes what it is handling, then stops.',
    confirmLabel: 'Stop channel',
  }

  it('is announced as a modal dialog and named by its title', () => {
    render(<Confirm {...props} onConfirm={() => {}} onCancel={() => {}} />)

    const dialog = screen.getByRole('dialog')
    expect(dialog.getAttribute('aria-modal')).toBe('true')

    // The accessible name comes from the title, so a screen reader says what is being confirmed rather than
    // announcing an unnamed dialog.
    const labelledBy = dialog.getAttribute('aria-labelledby')
    expect(labelledBy).toBeTruthy()
    expect(document.getElementById(labelledBy!)?.textContent).toBe('Stop labs?')
  })

  it('focuses Cancel rather than the destructive button', () => {
    // A confirmation whose dangerous button is focused turns a stray Return keypress into the very action it
    // exists to guard against.
    render(<Confirm {...props} onConfirm={() => {}} onCancel={() => {}} />)
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Cancel' }))
  })

  it('cancels on Escape', () => {
    const onCancel = vi.fn()
    const onConfirm = vi.fn()
    render(<Confirm {...props} onConfirm={onConfirm} onCancel={onCancel} />)

    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onCancel).toHaveBeenCalledTimes(1)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('does not confirm when the backdrop is clicked', () => {
    const onCancel = vi.fn()
    const onConfirm = vi.fn()
    const { container } = render(<Confirm {...props} onConfirm={onConfirm} onCancel={onCancel} />)

    const backdrop = container.firstElementChild
    expect(backdrop).not.toBeNull()
    fireEvent.click(backdrop!)

    expect(onCancel).toHaveBeenCalledTimes(1)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('does not cancel when the panel itself is clicked', () => {
    // Reading the dialog must not dismiss it.
    const onCancel = vi.fn()
    render(<Confirm {...props} onConfirm={() => {}} onCancel={onCancel} />)

    fireEvent.click(screen.getByRole('dialog'))
    expect(onCancel).not.toHaveBeenCalled()
  })
})
