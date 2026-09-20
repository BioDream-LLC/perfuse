/**
 * Security and robustness tests for SyntaxHighlight.tsx.
 *
 * These tests verify that the syntax highlighter CANNOT create XSS vectors when
 * rendering untrusted message content (PHI from external HL7/XML/JSON/X12 messages).
 *
 * The component renders via React's JSX children ({t.text} inside <span>), which
 * auto-escapes HTML entities. These tests prove that at the DOM level, no executable
 * elements are injected regardless of what the input contains.
 *
 * @vitest-environment happy-dom
 */
import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'
import { SyntaxBlock } from './SyntaxHighlight'

const XSS_PAYLOADS = [
  '<img src=x onerror=alert(1)>',
  '<script>alert(1)</script>',
  '<svg onload=alert(1)>',
  '<iframe src="javascript:alert(1)"></iframe>',
  '"><script>alert(1)</script><span x="',
  "';alert(1)//",
  '<img/src=x onerror=alert(1)//',
]

describe('SyntaxHighlight XSS safety', () => {
  describe('HL7 format', () => {
    it('does not create executable elements from XSS payload in field values', () => {
      const msg = `MSH|^~\\&|SEND|FAC|RECV|FAC|20230101||ADT^A01|123|P|2.3\nPID|||${XSS_PAYLOADS[0]}||${XSS_PAYLOADS[1]}|||`
      const { container } = render(<SyntaxBlock code={msg} language="hl7" />)

      expect(container.querySelector('script')).toBeNull()
      expect(container.querySelector('img')).toBeNull()
      expect(container.querySelector('svg[onload]')).toBeNull()
      expect(container.querySelector('iframe')).toBeNull()

      // The text content should contain the literal characters, proving they were escaped
      const text = container.textContent!
      expect(text).toContain('<img src=x onerror=alert(1)>')
      expect(text).toContain('<script>alert(1)</script>')
    })

    it('handles all XSS payloads in HL7 segment fields', () => {
      for (const payload of XSS_PAYLOADS) {
        const msg = `MSH|^~\\&|${payload}|FAC|RECV|FAC|20230101||ADT^A01|123|P|2.3`
        const { container } = render(<SyntaxBlock code={msg} language="hl7" />)

        expect(container.querySelector('script')).toBeNull()
        expect(container.querySelector('img')).toBeNull()
        expect(container.querySelector('iframe')).toBeNull()
        expect(container.textContent).toContain(payload)
      }
    })
  })

  describe('XML format', () => {
    it('does not create executable elements from XSS payload in text content', () => {
      const msg = `<Patient><Name><script>alert(1)</script></Name><ID><img src=x onerror=alert(1)></ID></Patient>`
      const { container } = render(<SyntaxBlock code={msg} language="xml" />)

      // The component should NOT create real script or img elements from the content.
      // It should render them as highlighted text tokens.
      const scripts = container.querySelectorAll('script')
      expect(scripts.length).toBe(0)

      // There should be no img with an onerror handler
      const imgs = container.querySelectorAll('img')
      expect(imgs.length).toBe(0)

      // The raw text should still be visible (escaped, not stripped)
      expect(container.textContent).toContain('alert(1)')
    })

    it('handles XSS in XML attributes', () => {
      const msg = `<Root attr="&quot;><script>alert(1)</script><x y=&quot;">text</Root>`
      const { container } = render(<SyntaxBlock code={msg} language="xml" />)

      expect(container.querySelector('script')).toBeNull()
    })

    it('handles all XSS payloads in XML', () => {
      for (const payload of XSS_PAYLOADS) {
        const msg = `<Root>${payload}</Root>`
        const { container } = render(<SyntaxBlock code={msg} language="xml" />)

        expect(container.querySelector('script')).toBeNull()
        expect(container.querySelector('img')).toBeNull()
        expect(container.querySelector('iframe')).toBeNull()
      }
    })
  })

  describe('JSON format', () => {
    it('does not create executable elements from XSS payload in values', () => {
      const msg = JSON.stringify({
        patient: '<script>alert(1)</script>',
        name: '<img src=x onerror=alert(1)>',
      })
      const { container } = render(<SyntaxBlock code={msg} language="json" />)

      expect(container.querySelector('script')).toBeNull()
      expect(container.querySelector('img')).toBeNull()
      expect(container.textContent).toContain('<script>alert(1)</script>')
      expect(container.textContent).toContain('<img src=x onerror=alert(1)>')
    })

    it('handles all XSS payloads in JSON', () => {
      for (const payload of XSS_PAYLOADS) {
        const msg = JSON.stringify({ field: payload })
        const { container } = render(<SyntaxBlock code={msg} language="json" />)

        expect(container.querySelector('script')).toBeNull()
        expect(container.querySelector('img')).toBeNull()
        expect(container.querySelector('iframe')).toBeNull()
      }
    })
  })

  describe('X12 format', () => {
    it('does not create executable elements from XSS payload in element values', () => {
      const msg = `ISA*00*          *00*          *ZZ*<script>alert(1)</script>*ZZ*<img src=x onerror=alert(1)>*230101*1200*^*00501*000000001*0*P*:~`
      const { container } = render(<SyntaxBlock code={msg} language="x12" />)

      expect(container.querySelector('script')).toBeNull()
      expect(container.querySelector('img')).toBeNull()
      expect(container.textContent).toContain('<script>alert(1)</script>')
      expect(container.textContent).toContain('<img src=x onerror=alert(1)>')
    })

    it('handles all XSS payloads in X12', () => {
      for (const payload of XSS_PAYLOADS) {
        const msg = `ISA*00*${payload}*00*rest~GS*HP*sender*receiver~`
        const { container } = render(<SyntaxBlock code={msg} language="x12" />)

        expect(container.querySelector('script')).toBeNull()
        expect(container.querySelector('img')).toBeNull()
        expect(container.querySelector('iframe')).toBeNull()
      }
    })
  })
})

describe('SyntaxHighlight robustness', () => {
  it('handles an unterminated quote without hanging', () => {
    const msg = `{"key": "value that never ends...`
    const { container } = render(<SyntaxBlock code={msg} language="json" />)
    expect(container.textContent).toContain('value that never ends')
  })

  it('handles an unterminated XML tag without hanging', () => {
    const msg = `<Root><Unclosed attr="something`
    const { container } = render(<SyntaxBlock code={msg} language="xml" />)
    expect(container.textContent).toContain('Unclosed')
  })

  it('handles a very long single line (100k chars) without catastrophic backtracking', () => {
    // If any regex has nested quantifiers, this will timeout.
    // Using HL7 format since it's the most delimiter-heavy.
    const longField = 'A'.repeat(100_000)
    const msg = `MSH|^~\\&|${longField}|FAC|RECV|FAC|20230101||ADT^A01|123|P|2.3`

    const start = performance.now()
    const { container } = render(<SyntaxBlock code={msg} language="hl7" />)
    const elapsed = performance.now() - start

    // Should complete in well under 1 second. Catastrophic backtracking would take minutes.
    expect(elapsed).toBeLessThan(5000)
    expect(container.textContent).toContain(longField)
  })

  it('handles a 100k char XML line without catastrophic backtracking', () => {
    const longAttr = 'x'.repeat(100_000)
    const msg = `<Root attr="${longAttr}">text</Root>`

    const start = performance.now()
    const { container } = render(<SyntaxBlock code={msg} language="xml" />)
    const elapsed = performance.now() - start

    expect(elapsed).toBeLessThan(5000)
    expect(container.textContent).toContain('Root')
  })

  it('handles a 100k char JSON value without catastrophic backtracking', () => {
    const longVal = 'x'.repeat(100_000)
    const msg = `{"key": "${longVal}"}`

    const start = performance.now()
    render(<SyntaxBlock code={msg} language="json" />)
    const elapsed = performance.now() - start

    expect(elapsed).toBeLessThan(5000)
  })

  it('handles a 100k char X12 element without catastrophic backtracking', () => {
    const longField = 'X'.repeat(100_000)
    const msg = `ISA*00*${longField}*00*rest~`

    const start = performance.now()
    render(<SyntaxBlock code={msg} language="x12" />)
    const elapsed = performance.now() - start

    expect(elapsed).toBeLessThan(5000)
  })

  it('handles deeply nested XML (1000 levels) without stack overflow', () => {
    const depth = 1000
    const open = Array.from({ length: depth }, (_, i) => `<n${i}>`).join('')
    const close = Array.from({ length: depth }, (_, i) => `</n${depth - 1 - i}>`).join('')
    const msg = open + 'leaf' + close

    const { container } = render(<SyntaxBlock code={msg} language="xml" />)
    expect(container.textContent).toContain('leaf')
  })

  it('renders empty string without crashing', () => {
    const { container } = render(<SyntaxBlock code="" />)
    expect(container.querySelector('pre')).not.toBeNull()
  })
})
