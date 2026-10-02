package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// JavaScript parity on the path-addressed formats.
//
// # Why this was the largest untested region
//
// X12, NCPDP and delimited all reach scripting through runPathScriptStage, which is generic over steps.Accessor - one
// implementation for three formats. X12 had both languages tested. NCPDP and delimited had Lua only.
//
// That asymmetry is exactly where the shape this project keeps finding lives: a stage that is generic in the code but only ever
// exercised through one language, so a language-specific defect in the binding would show up on whichever format somebody happened
// to use in production first. The generic stage is an argument that JavaScript should work here, not evidence that it does.
//
// # What these assert
//
// Delivered bytes, in both directions. A transformer must change what arrives, and a filter must both drop and keep - because an
// always-accept filter cannot be told apart from one that never ran, which is the failure that made a WebAssembly filter drop all
// traffic while the channel reported success.

// ncpdpJavaScript covers the pharmacy format's read and write sides in JavaScript.
func TestAnNcpdpScriptCanBeWrittenInJavaScript(t *testing.T) {
	ncpdpChannel := func(t *testing.T, scripts string, sink *recordingSender) *Channel {
		t.Helper()
		sink.name = "out"

		cfg, err := config.Load(strings.NewReader(`
name: ncpdp-js
dataType: ncpdp
`+scripts+`
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: http
    http:
      url: http://127.0.0.1:1/in
`), "ncpdpjs.yaml")
		if err != nil {
			t.Fatalf("loading: %v", err)
		}

		return startChannelFor(t, cfg, sink)
	}

	t.Run("a transformer changes the delivered claim", func(t *testing.T) {
		sink := &recordingSender{}
		c := ncpdpChannel(t, `scripts:
  language: javascript
  transformer: |
    msg.set('01-CB', 'REDACTED');`, sink)

		if _, err := c.handle(context.Background(), pharmacyClaim()); err != nil {
			t.Fatalf("handling: %v", err)
		}

		if sink.count() == 0 {
			t.Fatal("nothing was delivered, so the script cannot be judged")
		}

		delivered := sink.last()
		if strings.Contains(delivered, "SMITH") {
			t.Error("the delivered transmission still carries the original surname, so the script wrote to a copy the " +
				"handler discarded")
		}
		if !strings.Contains(delivered, "REDACTED") {
			t.Error("the replacement is absent from the delivered transmission")
		}
	})

	t.Run("a transformer can read as well as write", func(t *testing.T) {
		// A write-only transformer would pass the case above even if get returned nothing, because the value it writes is
		// a constant. This one derives what it writes from what it read.
		sink := &recordingSender{}
		c := ncpdpChannel(t, `scripts:
  language: javascript
  transformer: |
    var surname = msg.get('01-CB');
    if (surname && surname.length > 0) {
      msg.set('01-CB', 'SEEN-' + surname);
    }`, sink)

		if _, err := c.handle(context.Background(), pharmacyClaim()); err != nil {
			t.Fatalf("handling: %v", err)
		}

		if sink.count() == 0 {
			t.Fatal("nothing was delivered")
		}
		if !strings.Contains(sink.last(), "SEEN-") {
			t.Errorf("the marker derived from the read value is absent, so get returned nothing: %q", sink.last())
		}
	})

	t.Run("a filter drops the claim", func(t *testing.T) {
		sink := &recordingSender{}
		c := ncpdpChannel(t, `scripts:
  language: javascript
  filter: |
    return false;`, sink)

		if _, err := c.handle(context.Background(), pharmacyClaim()); err != nil {
			t.Fatalf("handling: %v", err)
		}
		if sink.count() != 0 {
			t.Errorf("the claim was delivered despite the filter returning false, %d time(s)", sink.count())
		}
	})

	t.Run("a filter keeps the claim", func(t *testing.T) {
		// The control. Without it the case above passes when the filter refuses everything, or when nothing runs at all
		// and delivery is broken for an unrelated reason.
		sink := &recordingSender{}
		c := ncpdpChannel(t, `scripts:
  language: javascript
  filter: |
    return msg.get('01-CB') !== '';`, sink)

		if _, err := c.handle(context.Background(), pharmacyClaim()); err != nil {
			t.Fatalf("handling: %v", err)
		}
		if sink.count() != 1 {
			t.Errorf("expected the claim to be delivered once, got %d", sink.count())
		}
	})
}

// TestADelimitedScriptCanBeWrittenInJavaScript covers the third format.
func TestADelimitedScriptCanBeWrittenInJavaScript(t *testing.T) {
	delimitedChannel := func(t *testing.T, scripts string, sink *recordingSender) *Channel {
		t.Helper()
		sink.name = "out"

		cfg, err := config.Load(strings.NewReader(`
name: delimited-js
dataType: delimited
delimited:
  delimiter: ","
  has_header: true
  split: true
`+scripts+`
source:
  type: file
  file:
    root: /tmp
    dir: in
    raw: true
destinations:
  - name: out
    type: file
    dir: /tmp/delimjs
`), "delimjs.yaml")
		if err != nil {
			t.Fatalf("loading: %v", err)
		}

		return startChannelFor(t, cfg, sink)
	}

	const rows = "PatientID,Surname,Ward\nP001,Okonkwo,ICU\nP002,Nakamura,HDU\n"

	t.Run("a transformer changes the delivered row", func(t *testing.T) {
		sink := &recordingSender{}
		c := delimitedChannel(t, `scripts:
  language: javascript
  transformer: |
    if (msg.get('Ward') === 'ICU') {
      msg.set('Surname', 'REDACTED');
    }`, sink)

		if _, err := c.handle(context.Background(), []byte(rows)); err != nil {
			t.Fatalf("handling: %v", err)
		}

		all := sink.all()
		if len(all) != 2 {
			t.Fatalf("got %d delivered row(s), want 2 - split is on so each row is its own message", len(all))
		}

		joined := string(all[0]) + "\n" + string(all[1])

		// Reading matters as much as writing here: the condition means only the ICU row may change. A binding whose get
		// returned nothing would leave both rows alone and look identical to a script that never ran.
		if strings.Contains(joined, "Okonkwo") {
			t.Error("the ICU row still carries the original surname")
		}
		if !strings.Contains(joined, "REDACTED") {
			t.Error("the replacement is absent from every delivered row")
		}
		if !strings.Contains(joined, "Nakamura") {
			t.Error("the HDU row was changed too, so the condition on Ward did not read the row's own value")
		}
	})

	t.Run("a filter drops rows individually", func(t *testing.T) {
		// The sharpest assertion available on this format: split is on, so the filter runs per row, and a filter that
		// dropped everything or nothing would give one count rather than the other.
		sink := &recordingSender{}
		c := delimitedChannel(t, `scripts:
  language: javascript
  filter: |
    return msg.get('Ward') !== 'ICU';`, sink)

		if _, err := c.handle(context.Background(), []byte(rows)); err != nil {
			t.Fatalf("handling: %v", err)
		}

		all := sink.all()
		if len(all) != 1 {
			t.Fatalf("got %d delivered row(s), want 1 - the ICU row should have been dropped and the HDU row kept", len(all))
		}
		if !strings.Contains(string(all[0]), "Nakamura") {
			t.Errorf("the wrong row survived: %q", string(all[0]))
		}
	})
}

// TestAPathScriptReachingForTheRawMessageFailsLoudly records the eleventh instance of the shape, and its fix.
//
// runPathScriptStage builds its context with no tree and no text. The nil tree was explained and correct - these formats have no
// xtree, and building one nobody reads would be worse. The empty text was not explained, and an empty string is the shape that has
// cost the most here: a script asking for the message got something that looked like a message, was not, and reported no error.
//
// A JavaScript filter on X12 doing message.indexOf('837') found nothing, every time, and the channel called it a success.
//
// It is withheld rather than populated because this stage does no re-serialisation on purpose, so by the transformer the declarative
// steps have run and the arriving bytes no longer describe the message. Stale text would be worse than none: msg.get would return the
// current value while message showed the old one, with nothing reporting the disagreement.
//
// The assertion is that the channel reports an error, in both languages. A script that reaches for text this stage cannot give it
// should stop on that line.
func TestAPathScriptReachingForTheRawMessageFailsLoudly(t *testing.T) {
	for _, tc := range []struct{ language, reaches, control string }{
		{
			language: "javascript",
			reaches:  "    msg.set('REF02', 'LEN' + String(message.length));",
			control:  "    msg.set('REF02', 'FINE');",
		},
		{
			language: "lua",
			reaches:  `    msg.set("REF02", "LEN" .. tostring(#message))`,
			control:  `    msg.set("REF02", "FINE")`,
		},
	} {
		t.Run(tc.language+"/reaching for it fails the message", func(t *testing.T) {
			sink := &recordingSender{}
			c := x12ScriptChannel(t, "scripts:\n  language: "+tc.language+"\n  transformer: |\n"+tc.reaches, sink)

			_, err := c.handle(context.Background(), []byte(x12Claim))
			if err != nil {
				t.Logf("handling returned %v, which is acceptable", err)
			}

			// The observable this project uses for a failed script: nothing goes out. A script that computed from an
			// empty string would instead have delivered an interchange saying LEN0.
			if sink.count() != 0 {
				t.Errorf("an interchange was delivered after the script read a message that does not exist here: %q",
					sink.last())
			}
		})

		t.Run(tc.language+"/the same channel works without it", func(t *testing.T) {
			// The control, and the reason the case above means anything. Without it that assertion passes whenever
			// path transformers are broken in this language for any reason at all.
			sink := &recordingSender{}
			c := x12ScriptChannel(t, "scripts:\n  language: "+tc.language+"\n  transformer: |\n"+tc.control, sink)

			if _, err := c.handle(context.Background(), []byte(x12Claim)); err != nil {
				t.Fatalf("handling: %v", err)
			}
			if sink.count() != 1 {
				t.Fatalf("expected one delivery, got %d", sink.count())
			}
			if !strings.Contains(sink.last(), "FINE") {
				t.Errorf("the control transformer did not take effect: %q", sink.last())
			}
		})
	}
}

// TestAPreprocessorOnAPathFormatStillGetsTheMessage is the other half.
//
// Withholding the text from filters and transformers is only defensible if there is somewhere raw text work belongs. That is the
// preprocessor: it runs before any parsing or steps, so the bytes it holds are exactly what arrived. If this ever stopped being true
// the paragraph above would become an excuse rather than a reason.
func TestAPreprocessorOnAPathFormatStillGetsTheMessage(t *testing.T) {
	sink := &recordingSender{}
	c := x12ScriptChannel(t, `scripts:
  language: javascript
  preprocessor: |
    // Proves the text is present and is the interchange, not an empty string.
    if (message.indexOf('837') < 0) { throw new Error('the preprocessor was handed no interchange'); }
    return message;`, sink)

	if _, err := c.handle(context.Background(), []byte(x12Claim)); err != nil {
		t.Fatalf("a preprocessor on a path format must receive the raw message: %v", err)
	}
	if sink.count() != 1 {
		t.Fatalf("expected one delivery, got %d", sink.count())
	}
}
