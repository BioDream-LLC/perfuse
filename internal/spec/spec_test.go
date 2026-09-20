package spec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// A generated specification is only worth having if it can be trusted, and it earns that in two
// ways: it never overstates what it knows, and it never leaks a credential into a document that
// gets emailed to a vendor.

func build(t *testing.T, yaml string) *Document {
	t.Helper()
	c, err := config.Load(strings.NewReader(yaml), "(test)")
	if err != nil {
		t.Fatalf("loading the fixture: %v", err)
	}
	return Build(c)
}

const base = `name: adt-to-lab
description: Admissions from the hospital system to the laboratory
source:
  type: mllp
  listen: 0.0.0.0:6661
destinations:
  - name: lab
    type: mllp
    address: lab.example.org:6661
    queue:
      enabled: true
`

func TestTheSpecificationNamesTheContractWithTheSender(t *testing.T) {
	// The two facts a sending vendor needs before anything else: where to connect, and what an
	// acknowledgement means.
	doc := build(t, base)

	if !strings.Contains(doc.Receives, "6661") {
		t.Errorf("receives = %q, expected the listen address", doc.Receives)
	}
	if !strings.Contains(doc.Acknowledges, "every destination") {
		t.Errorf("acknowledges = %q, expected the delivery promise", doc.Acknowledges)
	}
}

func TestReadsAndWritesAreSeparate(t *testing.T) {
	// A sending system needs the read list, a receiving system needs the write list, and a
	// document that merges them tells neither party what they need. A copy step is the clearest
	// case: one path in, a different path out.
	doc := build(t, base+`transformations:
  - copy:
      from: PID-18
      to: PV1-19
`)

	if !hasPath(doc.Reads, "PID-18") {
		t.Errorf("PID-18 is not listed as read: %+v", doc.Reads)
	}
	if hasPath(doc.Writes, "PID-18") {
		t.Error("PID-18 is listed as written, but it is only read")
	}
	if !hasPath(doc.Writes, "PV1-19") {
		t.Errorf("PV1-19 is not listed as written: %+v", doc.Writes)
	}
	if hasPath(doc.Reads, "PV1-19") {
		t.Error("PV1-19 is listed as read, but it is only written")
	}
}

func TestAFilterCountsAsADependency(t *testing.T) {
	// If a filter tests a field, the interface behaves differently when that field is missing.
	// That is precisely what a sending vendor has to be told.
	doc := build(t, strings.Replace(base, "destinations:",
		"filter: \"MSH-9.1 == 'ADT' and PID-3 exists\"\ndestinations:", 1))

	if !hasPath(doc.Reads, "PID-3") {
		t.Errorf("PID-3 is not listed as a dependency: %+v", doc.Reads)
	}
	found := false
	for _, f := range doc.Reads {
		if f.Path == "PID-3" {
			for _, why := range f.Why {
				if strings.Contains(why, "filter") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("the reason PID-3 is read does not mention the filter")
	}
}

func TestADestinationFilterIsADependencyToo(t *testing.T) {
	// "Why did I not get that message" is a question about exactly these fields.
	doc := build(t, base+`  - name: overflow
    type: file
    dir: /tmp/overflow
    filter: "PV1-2 == 'E'"
`)

	if !hasPath(doc.Reads, "PV1-2") {
		t.Errorf("a destination filter's field is not listed: %+v", doc.Reads)
	}
}

func TestATranslationTableIsDocumentedInFull(t *testing.T) {
	// The part a receiving vendor most needs, and the part most often wrong in a hand-written
	// document, because somebody transcribed thirty rows into prose.
	doc := build(t, base+`transformations:
  - map:
      path: PID-8
      table:
        "1": M
        "2": F
        "3": O
      default: U
`)

	if len(doc.Mappings) != 1 {
		t.Fatalf("mappings = %+v", doc.Mappings)
	}
	m := doc.Mappings[0]

	if len(m.From) != 3 || len(m.To) != 3 {
		t.Fatalf("the table was not documented in full: %+v", m)
	}
	// Sorted, so the same channel always documents its table the same way. A specification that
	// reordered itself on every generation could not be diffed, which is most of what a
	// generated document is for.
	if m.From[0] != "1" || m.From[2] != "3" {
		t.Errorf("the table is not in a stable order: %v", m.From)
	}
	if !strings.Contains(m.Unmatched, `"U"`) {
		t.Errorf("what happens to an unlisted value is not stated: %q", m.Unmatched)
	}
	if m.Name != "Administrative Sex" {
		t.Errorf("the field was not named from the dictionary: %q", m.Name)
	}
}

func TestTheThreeUnmatchedBehavioursAreDistinguished(t *testing.T) {
	// They behave differently at run time, and which one is in force is what a receiving team
	// needs to know to decide whether they must handle an untranslated code.
	strict := build(t, base+`transformations:
  - map:
      path: PID-8
      table: {"1": M}
      strict: true
`)
	if !strings.Contains(strict.Mappings[0].Unmatched, "not delivered") {
		t.Errorf("strict: %q", strict.Mappings[0].Unmatched)
	}

	loose := build(t, base+`transformations:
  - map:
      path: PID-8
      table: {"1": M}
`)
	if !strings.Contains(loose.Mappings[0].Unmatched, "passed through") {
		t.Errorf("loose: %q", loose.Mappings[0].Unmatched)
	}
}

func TestAScriptedChannelSaysTheDocumentIsIncomplete(t *testing.T) {
	// The caveat that makes the rest of the document trustworthy. A generated specification that
	// quietly omitted a script would describe half an interface as though it were all of it, and
	// somebody would build against it.
	doc := build(t, base+`scripts:
  transformer: |
    msg['PID']['PID.8']['PID.8.1'] = 'F';
`)

	if !hasCaveat(doc, "cannot describe what the scripts do") {
		t.Errorf("no caveat about the script: %v", doc.Caveats)
	}
	if !hasCaveat(doc, "minimum") {
		t.Errorf("the caveat does not say the field lists are a minimum: %v", doc.Caveats)
	}
}

func TestADestinationWithNoSafetyNetIsCalledOut(t *testing.T) {
	// A receiving team needs to know that a message arriving while they are down is gone.
	doc := build(t, `name: fragile
source:
  type: mllp
  listen: 0.0.0.0:6662
destinations:
  - name: lab
    type: mllp
    address: lab.example.org:6661
`)

	if !hasCaveat(doc, "not sent again") {
		t.Errorf("no caveat about the missing queue: %v", doc.Caveats)
	}
}

func TestAcknowledgingOnReceiptIsCalledOut(t *testing.T) {
	doc := build(t, strings.Replace(base, "  listen: 0.0.0.0:6661",
		"  listen: 0.0.0.0:6661\n  ack:\n    when: on_receipt", 1))

	if !hasCaveat(doc, "still be lost") {
		t.Errorf("no caveat about acknowledging early: %v", doc.Caveats)
	}
}

func TestTheSpecificationNeverCarriesACredential(t *testing.T) {
	// A specification is a document that gets emailed to a vendor, which is the last place a
	// password should end up.
	doc := build(t, `name: db-out
source:
  type: mllp
  listen: 0.0.0.0:6663
destinations:
  - name: warehouse
    type: database
    database:
      driver: postgres
      dsn: postgres://someone:sup3rsecret@db.example.org/records
      statement: INSERT INTO messages (body) VALUES ($1)
      params: [MSH-10]
  - name: partner
    type: sftp
    sftp:
      host: sftp.example.org:22
      user: perfuse
      password: alsosecret
      key_file: /etc/perfuse/key
      known_hosts_file: /etc/perfuse/known_hosts
      dir: /out
`)

	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	out := string(body)

	for _, secret := range []string{"sup3rsecret", "alsosecret", "postgres://"} {
		if strings.Contains(out, secret) {
			t.Errorf("the specification leaked %q", secret)
		}
	}
	// It should still say enough to be useful.
	if !strings.Contains(out, "postgres") {
		t.Error("the database driver was not named, which a receiving team does need")
	}
}

func TestFieldsAreAnnotatedFromTheDictionary(t *testing.T) {
	// A path on its own is not a specification. PID-8 means nothing to somebody reading the
	// document; "Administrative Sex, HL7 table 0001" is actionable.
	doc := build(t, base+`transformations:
  - set:
      path: PID-8
      value: F
`)

	for _, f := range doc.Writes {
		if f.Path == "PID-8" {
			if f.Name != "Administrative Sex" {
				t.Errorf("PID-8 name = %q", f.Name)
			}
			if f.Table == "" {
				t.Error("PID-8 has no table, and a receiving team needs the code set")
			}
			return
		}
	}
	t.Errorf("PID-8 is not in the write list: %+v", doc.Writes)
}

func TestAPassthroughChannelSaysItIsATransport(t *testing.T) {
	// Reported rather than presented as an empty specification, which reads like a generation
	// failure.
	doc := build(t, base)

	if !hasCaveat(doc, "transport rather than an interface") {
		t.Errorf("a passthrough channel did not say so: %v", doc.Caveats)
	}
}

func TestGenerationIsStable(t *testing.T) {
	// A specification is a document that gets diffed. Two generations from the same channel
	// differing in order would make every comparison useless.
	yaml := base + `transformations:
  - map:
      path: PID-8
      table: {"1": M, "2": F, "3": O, "9": U}
  - copy:
      from: PID-18
      to: PV1-19
`
	first, err := json.Marshal(build(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		next, err := json.Marshal(build(t, yaml))
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(next) {
			t.Fatal("two generations of the same channel produced different documents")
		}
	}
}

func TestDeliveriesRecordWhetherAMessageCanBeLost(t *testing.T) {
	doc := build(t, base)

	if len(doc.Deliveries) != 1 {
		t.Fatalf("deliveries = %+v", doc.Deliveries)
	}
	if !doc.Deliveries[0].Durable {
		t.Error("a queued destination was not recorded as durable")
	}
	if !strings.Contains(doc.Deliveries[0].How, "lab.example.org") {
		t.Errorf("how = %q", doc.Deliveries[0].How)
	}
}

func hasPath(list []FieldUse, path string) bool {
	for _, f := range list {
		if f.Path == path {
			return true
		}
	}
	return false
}

func hasCaveat(doc *Document, substr string) bool {
	for _, c := range doc.Caveats {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}
