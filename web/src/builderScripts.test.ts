import { describe, expect, it } from 'vitest'
import { emptyDraft, draftToWire } from './model'
import { wireToDraft } from './wireToDraft'

// The scripts section, tested at the model boundary rather than through the component.
//
// What matters is that a script typed into the form reaches the channel file and comes back unchanged. A
// component test would prove a textarea holds text, which was never the problem: the draft already carried
// filterScript and transformerScript, the emitter already wrote them, and nothing set them - so every test
// passed while the feature was unreachable.

describe('script slots', () => {
  it('writes every slot into the model', () => {
    const draft = emptyDraft()
    draft.name = 'scripted'
    draft.preprocessorScript = 'return message;'
    draft.filterScript = 'return true;'
    draft.transformerScript = 'msg;'
    draft.postprocessorScript = 'logger.info("done");'
    draft.deployScript = 'logger.info("up");'
    draft.undeployScript = 'logger.info("down");'

    const model = draftToWire(draft) as { scripts?: Record<string, string> }

    expect(model.scripts?.preprocessor).toBe('return message;')
    expect(model.scripts?.filter).toBe('return true;')
    expect(model.scripts?.transformer).toBe('msg;')
    expect(model.scripts?.postprocessor).toBe('logger.info("done");')
    expect(model.scripts?.deploy).toBe('logger.info("up");')
    expect(model.scripts?.undeploy).toBe('logger.info("down");')
  })

  it('omits the scripts block entirely when no script is written', () => {
    const draft = emptyDraft()
    draft.name = 'plain'

    const model = draftToWire(draft) as { scripts?: unknown }

    // An empty scripts block is not the same as no scripts block: the loader treats a present-but-empty one
    // as a channel that wants a script engine, and it would appear in the generated file as noise nobody typed.
    expect(model.scripts).toBeUndefined()
  })

  it('does not write a language key for JavaScript', () => {
    const draft = emptyDraft()
    draft.name = 'js'
    draft.filterScript = 'return true;'

    const model = draftToWire(draft) as { scripts?: Record<string, string> }

    // JavaScript is the loader's default, so writing it would add a line to a diff that nobody typed - and a
    // diff showing lines nobody typed makes the next reviewer distrust the whole form.
    expect(model.scripts?.language).toBeUndefined()
  })

  it('writes the language key for Lua', () => {
    const draft = emptyDraft()
    draft.name = 'lua'
    draft.scriptLanguage = 'lua'
    draft.filterScript = 'return true'

    const model = draftToWire(draft) as { scripts?: Record<string, string> }

    expect(model.scripts?.language).toBe('lua')
  })

  it('does not write a language key when there are no scripts at all', () => {
    const draft = emptyDraft()
    draft.name = 'lua-but-empty'
    draft.scriptLanguage = 'lua'

    const model = draftToWire(draft) as { scripts?: unknown }

    // Choosing a language and writing nothing is not a configuration. Emitting a lone language key would
    // create a scripts block for a channel with no scripts, which the loader reads as wanting an engine.
    expect(model.scripts).toBeUndefined()
  })

  it('reads every slot back out of a model', () => {
    const draft = wireToDraft({
      name: 'round',
      scripts: {
        language: 'lua',
        preprocessor: 'pre',
        filter: 'filt',
        transformer: 'trans',
        postprocessor: 'post',
        deploy: 'up',
        undeploy: 'down',
      },
    })

    expect(draft.scriptLanguage).toBe('lua')
    expect(draft.preprocessorScript).toBe('pre')
    expect(draft.filterScript).toBe('filt')
    expect(draft.transformerScript).toBe('trans')
    expect(draft.postprocessorScript).toBe('post')
    expect(draft.deployScript).toBe('up')
    expect(draft.undeployScript).toBe('down')
  })

  it('reads an unmarked script as JavaScript', () => {
    const draft = wireToDraft({ name: 'unmarked', scripts: { filter: 'return true;' } })

    // An unmarked script is JavaScript in the loader too. Opening a channel must not change what its scripts
    // are, and defaulting to lua here would make the form reinterpret every script written before the setting
    // existed.
    expect(draft.scriptLanguage).toBe('javascript')
  })

  it('reads an unrecognised language as JavaScript rather than passing it through', () => {
    const draft = wireToDraft({ name: 'odd', scripts: { language: 'python', filter: 'x' } })

    // The loader would refuse python, so the form has to show something it can act on. Showing JavaScript is
    // wrong in a way somebody can see and fix; passing python through to a dropdown with two options would
    // leave the control blank and the file unchanged on save.
    expect(draft.scriptLanguage).toBe('javascript')
  })

  it('survives a round trip through the model and back', () => {
    const before = emptyDraft()
    before.name = 'trip'
    before.scriptLanguage = 'lua'
    before.preprocessorScript = 'return message'
    before.postprocessorScript = 'logger.info("x")'

    const model = draftToWire(before) as Parameters<typeof wireToDraft>[0]
    const after = wireToDraft(model)

    expect(after.scriptLanguage).toBe('lua')
    expect(after.preprocessorScript).toBe('return message')
    expect(after.postprocessorScript).toBe('logger.info("x")')
    // The slots left empty must come back empty rather than undefined, or the textarea binds to undefined and
    // React logs a controlled-input warning on every keystroke.
    expect(after.filterScript).toBe('')
    expect(after.deployScript).toBe('')
  })
})

describe('data types the form offers', () => {
  it('can express every data type the loader supports', () => {
    // The eight the loader knows. Three of them - delimited, dicom and raw - could not be chosen from the
    // form at all, so a channel carrying a CSV feed or an imaging object had to be written by hand.
    for (const dataType of [
      'hl7',
      'x12',
      'hl7v3',
      'ncpdp',
      'script',
      'delimited',
      'dicom',
      'raw',
    ] as const) {
      const draft = emptyDraft()
      draft.name = 'typed'
      draft.dataType = dataType

      const model = draftToWire(draft) as { dataType?: string }

      // hl7 is the default and is deliberately omitted from the file.
      if (dataType === 'hl7') {
        expect(model.dataType).toBeUndefined()
      } else {
        expect(model.dataType).toBe(dataType)
      }
    }
  })
})
