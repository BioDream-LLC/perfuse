package api

import (
	"strings"
	"testing"
)

// TestAScriptsBlockSurvivesWhenOnlyOneSlotIsFilled is the regression for a bug that lost typed text silently.
//
// # What was wrong
//
// The builder prunes empty blocks before writing the file, so that a form field left alone does not emit a stanza reading as a
// decision somebody made. The scripts pruning tested Filter and Transformer only. So a channel whose sole script was a
// preprocessor had its entire scripts block discarded: the textarea showed the text, the generated file did not contain it, and
// saving lost it without a word.
//
// # Why this is worth its own test rather than a fixed line
//
// The identical bug was found and fixed in config.Scripts.Empty, and the comment there records it: "Every script counts. Listing
// only two here meant a channel whose only script was a preprocessor was treated as having none, so nothing was compiled and the
// script silently never ran - the worst possible outcome, because the file plainly contains it."
//
// That fix was never carried across. The loader learned to count all six slots while the builder went on counting two, and the
// two halves disagreed for as long as it took somebody to type a preprocessor into a form that had no preprocessor field. It
// gained one tonight, which is how this surfaced.
//
// So the property under test is not "preprocessor works". It is that every slot is counted, checked by walking them rather than
// by naming the one that broke - naming it would leave the next four uncovered in exactly the way this bug survived.
func TestAScriptsBlockSurvivesWhenOnlyOneSlotIsFilled(t *testing.T) {
	for _, tc := range []struct {
		slot string
		// build sets exactly one slot on an otherwise empty scripts block.
		build func(*buildScripts)
	}{
		{"filter", func(s *buildScripts) { s.Filter = "return true;" }},
		{"transformer", func(s *buildScripts) { s.Transformer = "msg;" }},
		{"preprocessor", func(s *buildScripts) { s.Preprocessor = "return message;" }},
		{"postprocessor", func(s *buildScripts) { s.Postprocessor = `logger.info("done");` }},
		{"deploy", func(s *buildScripts) { s.Deploy = `logger.info("up");` }},
		{"undeploy", func(s *buildScripts) { s.Undeploy = `logger.info("down");` }},
	} {
		t.Run(tc.slot, func(t *testing.T) {
			scripts := &buildScripts{}
			tc.build(scripts)

			m := scriptsOnlyModel(scripts)
			m.pruneEmpty()

			yaml, err := marshalChannel(m)
			if err != nil {
				t.Fatalf("building: %v", err)
			}

			if !strings.Contains(yaml, "scripts:") {
				t.Fatalf("a channel whose only script is a %s produced no scripts block, so the text typed into the form is lost on save:\n%s", tc.slot, yaml)
			}
			if !strings.Contains(yaml, tc.slot+":") {
				t.Errorf("the scripts block does not contain the %s key:\n%s", tc.slot, yaml)
			}
		})
	}
}

// TestAnEmptyScriptsBlockIsStillPruned is the other half, and the reason the pruning exists at all.
//
// A form field left alone must not emit a stanza. "scripts: {}" in a generated file reads as a decision somebody made, and the
// loader treats a present block as a channel that wants a script engine.
func TestAnEmptyScriptsBlockIsStillPruned(t *testing.T) {
	m := scriptsOnlyModel(&buildScripts{})
	m.pruneEmpty()

	yaml, err := marshalChannel(m)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if strings.Contains(yaml, "scripts:") {
		t.Errorf("an untouched scripts block was written to the file:\n%s", yaml)
	}
}

// TestScriptSettingsAloneDoNotCreateAScriptsBlock covers the settings that describe scripts rather than being one.
//
// A timeout, an allow list or an include names how scripts behave. On their own they describe nothing, so counting them would
// emit a scripts stanza for a channel with no scripts - which is the same noise the pruning exists to prevent, arrived at from
// the opposite direction.
func TestScriptSettingsAloneDoNotCreateAScriptsBlock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*buildScripts)
	}{
		{"timeout", func(s *buildScripts) { s.Timeout = "10s" }},
		{"allow", func(s *buildScripts) { s.Allow = []string{"network"} }},
		{"include", func(s *buildScripts) { s.Include = []string{"shared.js"} }},
		{"language", func(s *buildScripts) { s.Language = "lua" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scripts := &buildScripts{}
			tc.build(scripts)

			m := scriptsOnlyModel(scripts)
			m.pruneEmpty()

			yaml, err := marshalChannel(m)
			if err != nil {
				t.Fatalf("building: %v", err)
			}

			if strings.Contains(yaml, "scripts:") {
				t.Errorf("%s alone produced a scripts block for a channel with no scripts:\n%s", tc.name, yaml)
			}
		})
	}
}

// scriptsOnlyModel is the smallest model that will marshal, with the given scripts attached.
func scriptsOnlyModel(scripts *buildScripts) buildModel {
	return buildModel{
		Name:    "scripted",
		Scripts: scripts,
		Source:  buildSource{Type: "mllp", Listen: "127.0.0.1:0"},
		Dests:   []buildDest{{Name: "out", Type: "mllp", Address: "127.0.0.1:1"}},
	}
}
