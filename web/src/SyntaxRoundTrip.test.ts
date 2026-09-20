import { describe, it, expect } from 'vitest'
import { tokenizeForTest } from './SyntaxHighlight'

// A tokenizer used behind an editable box must reproduce its input exactly.
//
// The highlighted layer sits underneath a transparent textarea, character for character. If tokenising drops a character, swallows a
// newline or duplicates anything, the colours slide out of step with the text the person is typing - and it gets worse the further
// down the box they go. That is a uniquely annoying bug: it looks like a rendering glitch, it is intermittent by content, and no
// assertion about colours would catch it.
//
// So the property tested here is not "this is coloured correctly" but "no character was lost or invented". It is the invariant the
// whole approach rests on, and it holds for every language or the overlay cannot be used for that language.

const samples: [string, string][] = [
  ['hl7', 'MSH|^~\\&|EPIC|WESTGENERAL|LABSYS|WESTLAB|20260820113000||ADT^A08^ADT_A01|C4471|P|2.5.1\rPID|1||100294^^^WESTGEN^MR\r'],
  ['xml', '<?xml version="1.0"?>\n<ClinicalDocument xmlns="urn:hl7-org:v3">\n  <!-- note -->\n  <id root="1.2.3"/>\n</ClinicalDocument>\n'],
  ['json', '{\n  "resourceType": "Patient",\n  "active": true,\n  "id": 42,\n  "name": [{"family": "Frost"}]\n}\n'],
  ['x12', 'ISA*00*          *00*          *ZZ*SENDER\nGS*HC*SENDER*RECEIVER*20260820*1130*1*X*005010X222A1~\n'],
  [
    'js',
    "// a comment\nvar family = msg['PID']['PID.5']['PID.5.1'].toString();\nwhile (mrn.length < 10) { mrn = '0' + mrn; }\nlogger.info(`padded ${mrn}`);\n",
  ],
  ['js', '-- a Lua comment\nlocal v = msg.child("PID"):text()\nif v ~= nil then return true end\n'],
  [
    'yaml',
    'name: demo\n# a comment\nsource:\n  type: mllp\n  port: 2575\n  tls: false\nsteps:\n  - set:\n      path: PID-3.1\n      value: "quoted"\n',
  ],
  ['text', 'just some words\nand another line\n'],
]

describe('every tokenizer reproduces its input exactly', () => {
  for (const [language, src] of samples) {
    it(`${language} loses nothing`, () => {
      const joined = tokenizeForTest(src, language as never)
        .map((t) => t.text)
        .join('')

      expect(joined).toBe(src)
    })
  }

  it('survives the awkward inputs', () => {
    // Empty, whitespace only, a lone delimiter, an unterminated string, and a stray backslash. Each of these has broken a
    // regex-based tokenizer somewhere, and an unterminated string is what a box looks like halfway through being typed.
    for (const src of ['', '\n', '   ', '|', '{', '<', "'unterminated", '\\', 'a\r\nb', '"']) {
      for (const language of ['hl7', 'xml', 'json', 'x12', 'js', 'yaml', 'text'] as const) {
        const joined = tokenizeForTest(src, language)
          .map((t) => t.text)
          .join('')

        expect(joined, `${language} altered ${JSON.stringify(src)}`).toBe(src)
      }
    }
  })
})
