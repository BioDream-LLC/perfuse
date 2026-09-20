package spec

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// narrationOf builds a channel from YAML and narrates it.
func narrationOf(t *testing.T, yaml string) []string {
	t.Helper()

	c, err := config.Load(strings.NewReader(yaml), "(test)")
	if err != nil {
		t.Fatalf("the channel did not load: %v", err)
	}

	return Build(c).Narrate()
}

// joined renders the narration for a failure message and for substring checks.
func joined(sentences []string) string { return strings.Join(sentences, " ") }

// The narration must answer the questions somebody asks at four in the morning, in order.
func TestNarrationDescribesTheWholeChannel(t *testing.T) {
	got := narrationOf(t, `name: admissions
description: the main ADT feed from the hospital
source:
  type: mllp
  listen: 127.0.0.1:6661
filter: MSH-9.2 in ['A01', 'A08']
transformations:
  - map:
      path: PID-8
      table:
        M: MALE
        F: FEMALE
      default: U
destinations:
  - name: archive
    type: file
    dir: /var/spool/out
`)

	text := joined(got)

	// The description, so somebody knows what they are reading about.
	if !strings.Contains(text, "the main ADT feed") {
		t.Errorf("the channel's own description was not used: %s", text)
	}

	// The filter, because "accepts everything" and "accepts two trigger events" have very different consequences downstream.
	if !strings.Contains(text, "A01") {
		t.Errorf("the filter is not described: %s", text)
	}

	// The mapping, and what happens to a code the table does not hold. That last part is what a table of rows does not tell you.
	if !strings.Contains(text, "PID-8") {
		t.Errorf("the mapped field is not named: %s", text)
	}
	if !strings.Contains(strings.ToLower(text), "u") || !strings.Contains(text, "2 entries") {
		t.Errorf("the table size or its default is missing: %s", text)
	}

	// Where it goes.
	if !strings.Contains(text, "archive") {
		t.Errorf("the destination is not named: %s", text)
	}

	// Sentences, so the interface can lay them out and a reader is not handed a wall.
	if len(got) < 4 {
		t.Errorf("got %d sentences, expected the channel to take more explaining than that: %v", len(got), got)
	}
	for i, s := range got {
		if !strings.HasSuffix(s, ".") {
			t.Errorf("sentence %d does not end in a full stop: %q", i, s)
		}
	}
}

// Every sentence must read as a sentence.
//
// This exists because the tests above all passed while every narration opened with "It receives the sender connects to 127.0.0.1:6661",
// which is not English. They checked that the facts were present and nothing checked that the result could be read - and being readable
// is the entire feature. Found by printing one and looking at it.
func TestEverySentenceReadsAsASentence(t *testing.T) {
	got := narrationOf(t, `name: admissions
description: the main ADT feed from the hospital
source:
  type: mllp
  listen: 127.0.0.1:6661
filter: MSH-9.2 in ['A01']
transformations:
  - map:
      path: PID-8
      table:
        M: MALE
      default: U
destinations:
  - name: archive
    type: file
    dir: /var/spool/out
  - name: downstream
    type: mllp
    address: 10.0.0.9:7001
`)

	for i, sentence := range got {
		if sentence == "" {
			t.Errorf("sentence %d is empty", i)

			continue
		}

		first := []rune(sentence)[0]
		if first >= 'a' && first <= 'z' {
			t.Errorf("sentence %d starts lower case, so it reads as a fragment: %q", i, sentence)
		}
		if !strings.HasSuffix(sentence, ".") {
			t.Errorf("sentence %d does not end in a full stop: %q", i, sentence)
		}

		// The specific breakage: a verb followed immediately by a clause that has its own subject and verb. "It receives the
		// sender connects to" is the shape, and it comes from embedding a complete sentence where a noun phrase was expected.
		for _, wrong := range []string{"receives the sender connects", "receives The sender"} {
			if strings.Contains(sentence, wrong) {
				t.Errorf("sentence %d embeds a sentence where a phrase belongs: %q", i, sentence)
			}
		}
	}

	// A qualifier that belongs to one destination must not be attached to a list of them, where it reads as applying to all.
	for _, sentence := range got {
		if strings.Contains(sentence, "destinations:") && strings.Contains(sentence, "queued if") {
			t.Errorf("a per-destination qualifier was attached to a list: %q", sentence)
		}
	}
}

// A channel that filters nothing must say so.
//
// The most common cause of surprise volume downstream is a channel that accepts everything because nobody chose otherwise. Silence here
// would read as "there is no filter to mention" rather than "it takes the lot".
func TestNarrationStatesWhenNothingIsFiltered(t *testing.T) {
	got := narrationOf(t, `name: everything
source:
  type: mllp
  listen: 127.0.0.1:6662
destinations:
  - name: archive
    type: file
    dir: /var/spool/out
`)

	if !strings.Contains(joined(got), "every message") {
		t.Errorf("a channel with no filter does not say it accepts everything: %v", got)
	}
}

// A strict table must be described as stopping the message.
//
// Translated, passed through, or refused are three different operational outcomes, and the difference decides whether an unrecognised
// code is a data quality problem or an outage.
func TestNarrationSaysWhatHappensToAnUnmappedCode(t *testing.T) {
	strict := narrationOf(t, `name: strictfeed
source:
  type: mllp
  listen: 127.0.0.1:6663
transformations:
  - map:
      path: PV1-3
      table:
        ICU: CRIT
      strict: true
destinations:
  - name: archive
    type: file
    dir: /var/spool/out
`)

	// The document's own wording for this is reused rather than reinvented, so this asserts the fact reached the narration at all.
	if !strings.Contains(strings.ToLower(joined(strict)), "unmatched") &&
		!strings.Contains(strings.ToLower(joined(strict)), "reject") &&
		!strings.Contains(strings.ToLower(joined(strict)), "stop") &&
		!strings.Contains(strings.ToLower(joined(strict)), "fail") {
		t.Errorf("a strict table does not say what happens to a code it does not hold: %v", strict)
	}
}

// A caveat must never be dropped.
//
// A narration that reads as complete while a script is doing something it did not mention is worse than no narration, because somebody
// will act on it. The specification already refuses to describe a script's behaviour; the readable version must not be the cheerful one
// that forgets to.
func TestNarrationKeepsTheCaveats(t *testing.T) {
	c, err := config.Load(strings.NewReader(`name: scripted
source:
  type: mllp
  listen: 127.0.0.1:6664
destinations:
  - name: archive
    type: file
    dir: /var/spool/out
`), "(test)")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	doc := Build(c)
	doc.Caveats = []string{"this channel runs a script, and what the script does cannot be established from its configuration"}

	got := doc.Narrate()
	if !strings.Contains(joined(got), "cannot be established") {
		t.Errorf("a caveat was dropped from the narration: %v", got)
	}

	// And it must be last, so it is not buried between two reassuring sentences.
	if !strings.Contains(got[len(got)-1], "cannot be established") {
		t.Errorf("the caveat is not the last thing said: %v", got)
	}
}

// Acronyms must survive being lowercased into a sentence.
func TestNarrationDoesNotMangleAcronyms(t *testing.T) {
	if got := lowerFirst("ADT^A01 messages"); got != "ADT^A01 messages" {
		t.Errorf("lowerFirst mangled an acronym: %q", got)
	}
	if got := lowerFirst("Every message"); got != "every message" {
		t.Errorf("lowerFirst did not lowercase an ordinary word: %q", got)
	}
	if got := lowerFirst(""); got != "" {
		t.Errorf("lowerFirst on empty gave %q", got)
	}
}

// Lists must read the way somebody would say them.
func TestJoinWithAnd(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"a":       "a",
		"a|b":     "a and b",
		"a|b|c":   "a, b and c",
		"a|b|c|d": "a, b, c and d",
	}

	for input, want := range cases {
		var items []string
		if input != "" {
			items = strings.Split(input, "|")
		}
		if got := joinWithAnd(items); got != want {
			t.Errorf("joinWithAnd(%q) = %q, want %q", input, got, want)
		}
	}
}
