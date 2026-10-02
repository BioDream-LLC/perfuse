package generate

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/cda"
)

// The point of these tests is that the generator produces messages the rest of
// the system can actually read. A generator that emits something subtly malformed
// would produce failing tests everywhere else and send whoever is debugging them
// in the wrong direction entirely.

func TestEveryGeneratedMessageParses(t *testing.T) {
	for _, kind := range []Kind{ADT, ORU, ORM, MDM} {
		t.Run(string(kind), func(t *testing.T) {
			for _, m := range New(Options{Kind: kind, Count: 60, Seed: 42}).All() {
				parsed, err := hl7.Parse(m.Raw)
				if err != nil {
					t.Fatalf("a generated %s did not parse (%s): %v\n%s",
						kind, m.Awkward, err, m.Raw)
				}
				typ, event, _ := parsed.Type()
				if typ != m.Type || event != m.Event {
					t.Errorf("reported %s^%s but the message says %s^%s",
						m.Type, m.Event, typ, event)
				}
				if parsed.ControlID() != m.ControlID {
					t.Errorf("control id = %q, reported %q",
						parsed.ControlID(), m.ControlID)
				}
			}
		})
	}
}

func TestSegmentsEndWithCarriageReturns(t *testing.T) {
	// A generator emitting newlines would work against a tolerant parser and fail
	// against a real receiver, which is the worst way round.
	m := New(Options{Kind: ADT, Count: 1, Seed: 1}).Next()
	if strings.Contains(string(m.Raw), "\n") {
		t.Error("segments must be separated by carriage returns, not newlines")
	}
	if !strings.HasSuffix(string(m.Raw), "\r") {
		t.Error("the last segment should be terminated")
	}
}

func TestSameSeedProducesTheSameMessages(t *testing.T) {
	// Without this a channel test using the generator would be flaky, which is
	// worse than having no test.
	first := New(Options{Kind: ORU, Count: 20, Seed: 7}).All()
	second := New(Options{Kind: ORU, Count: 20, Seed: 7}).All()

	if len(first) != len(second) {
		t.Fatalf("%d vs %d messages", len(first), len(second))
	}
	for i := range first {
		if string(first[i].Raw) != string(second[i].Raw) {
			t.Fatalf("message %d differs between runs with the same seed", i)
		}
	}
}

func TestDifferentSeedsProduceDifferentMessages(t *testing.T) {
	a := New(Options{Kind: ADT, Count: 20, Seed: 1}).All()
	b := New(Options{Kind: ADT, Count: 20, Seed: 2}).All()

	same := 0
	for i := range a {
		if string(a[i].Raw) == string(b[i].Raw) {
			same++
		}
	}
	if same == len(a) {
		t.Error("two seeds produced identical output, so the seed is being ignored")
	}
}

func TestTimestampsCarryAnOffset(t *testing.T) {
	// HL7 permits a bare local time, and a generator that always omitted the offset
	// would never exercise the code that has to guess one — which is exactly the
	// code most likely to be wrong.
	m := New(Options{Kind: ADT, Count: 1, Seed: 3}).Next()
	parsed, err := hl7.Parse(m.Raw)
	if err != nil {
		t.Fatal(err)
	}
	msh, ok := parsed.Segment("MSH", 1)
	if !ok {
		t.Fatal("no MSH")
	}
	stamp := msh.Field(7).String()
	if !strings.ContainsAny(stamp, "+-") {
		t.Errorf("MSH-7 %q has no UTC offset", stamp)
	}
}

func TestAFeedRevisitsPatients(t *testing.T) {
	// A feed about one patient exercises nothing, and a feed where every message is
	// a different patient exercises nothing either, because real feeds revisit
	// people and that is what makes an encounter update meaningful.
	messages := New(Options{Kind: ADT, Count: 40, Seed: 5, Patients: 5}).All()

	seen := map[string]int{}
	for _, m := range messages {
		seen[m.Patient]++
	}
	if len(seen) != 5 {
		t.Errorf("want 5 distinct patients, got %d", len(seen))
	}
	for mrn, count := range seen {
		if count < 2 {
			t.Errorf("patient %s appears only once", mrn)
		}
	}
}

func TestAwkwardMessagesAreProducedButNotDominant(t *testing.T) {
	messages := New(Options{Kind: ADT, Count: 300, Seed: 11}).All()

	awkward := 0
	shapes := map[string]int{}
	for _, m := range messages {
		if m.Awkward != "" {
			awkward++
			shapes[m.Awkward]++
		}
	}

	// Feeding a channel nothing but perfectly formed messages tells you very
	// little, and feeding it nothing but broken ones tells you something about a
	// system nobody runs.
	if awkward == 0 {
		t.Fatal("no awkward messages were produced")
	}
	if awkward > len(messages)/3 {
		t.Errorf("%d of %d messages are awkward, which is too many to resemble a real feed",
			awkward, len(messages))
	}
	if len(shapes) < 3 {
		t.Errorf("only %d awkward shapes appeared: %v", len(shapes), shapes)
	}
}

func TestPerfectTurnsOffAwkwardMessages(t *testing.T) {
	// A demo where every message should sail through.
	for _, m := range New(Options{Kind: ADT, Count: 100, Seed: 13, Perfect: true}).All() {
		if m.Awkward != "" {
			t.Fatalf("Perfect should produce no awkward messages, got %q", m.Awkward)
		}
	}
}

func TestA28HasNoVisitSegment(t *testing.T) {
	// A28 is a registration with no visit. Emitting a PV1 anyway would teach a
	// wrong lesson to anybody building a channel against the generated feed.
	messages := New(Options{Kind: ADT, Count: 400, Seed: 17}).All()

	found := false
	for _, m := range messages {
		if m.Event != "A28" {
			continue
		}
		found = true
		if strings.Contains(string(m.Raw), "PV1|") {
			t.Errorf("an A28 should not carry a PV1:\n%s", m.Raw)
		}
	}
	if !found {
		t.Skip("no A28 in this sample")
	}
}

func TestResultsIncludeAbnormalFlags(t *testing.T) {
	// Abnormal flags are the reason anybody reads OBX-8, so a generator producing
	// only normal results would leave that path untested.
	messages := New(Options{Kind: ORU, Count: 120, Seed: 19}).All()

	flags := map[string]int{}
	for _, m := range messages {
		parsed, err := hl7.Parse(m.Raw)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; ; i++ {
			obx, ok := parsed.Segment("OBX", i)
			if !ok {
				break
			}
			if f := obx.Field(8).String(); f != "" {
				flags[f]++
			}
		}
	}
	if flags["H"] == 0 && flags["L"] == 0 {
		t.Errorf("no abnormal results in 120 messages: %v", flags)
	}
	if flags["N"] == 0 {
		t.Error("no normal results, which is not a realistic feed either")
	}
}

func TestResultsHaveSeveralObservations(t *testing.T) {
	// Code that handles one OBX often mishandles the third.
	messages := New(Options{Kind: ORU, Count: 30, Seed: 23}).All()

	for _, m := range messages {
		parsed, _ := hl7.Parse(m.Raw)
		count := 0
		for i := 1; ; i++ {
			if _, ok := parsed.Segment("OBX", i); !ok {
				break
			}
			count++
		}
		if count < 2 {
			t.Errorf("a result message has only %d observations", count)
		}
	}
}

func TestGeneratedDocumentsAreReadable(t *testing.T) {
	// The whole point of generating MDM: the document inside has to be a real
	// C-CDA, or it would exercise the extraction path and none of the reading path.
	messages := New(Options{Kind: MDM, Count: 25, Seed: 29}).All()

	for _, m := range messages {
		parsed, err := hl7.Parse(m.Raw)
		if err != nil {
			t.Fatalf("MDM did not parse: %v", err)
		}
		if !cda.IsDocumentMessage(parsed) {
			t.Fatal("a generated MDM was not recognised as carrying a document")
		}

		doc, _, err := cda.ParseEmbedded(parsed)
		if err != nil {
			t.Fatalf("the embedded document did not read (%s): %v", m.Awkward, err)
		}

		if doc.Patient.Family == "" {
			t.Error("the document has no patient family name")
		}
		if len(doc.Sections) < 2 {
			t.Errorf("want at least two sections, got %d", len(doc.Sections))
		}

		// The template identifiers have to be ones the vocabulary knows, or the
		// viewer would show numbers where it should show English.
		if doc.DocumentType == "" {
			t.Error("the document type was not recognised")
		}
		named := 0
		for _, s := range doc.Sections {
			if s.Kind != "" {
				named++
			}
		}
		if named == 0 {
			t.Error("no section was recognised by its template id")
		}
	}
}

func TestGeneratedDocumentsConvertToFHIR(t *testing.T) {
	messages := New(Options{Kind: MDM, Count: 10, Seed: 31}).All()

	for _, m := range messages {
		parsed, _ := hl7.Parse(m.Raw)
		doc, embedded, err := cda.ParseEmbedded(parsed)
		if err != nil {
			t.Fatal(err)
		}

		result, err := doc.ToFHIR(cda.FHIROptions{
			Version:  "R5",
			Original: embedded[0].Data,
		})
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		for _, want := range []string{"Patient", "AllergyIntolerance", "DocumentReference"} {
			if result.Counts[want] == 0 {
				t.Errorf("no %s produced; counts were %v", want, result.Counts)
			}
		}
	}
}

func TestSomeDocumentsDisagreeWithThemselves(t *testing.T) {
	// The agreement check exists to find narrative that does not match the coded
	// entries. A generator that never produced one would leave it permanently
	// untested against generated data.
	messages := New(Options{Kind: MDM, Count: 200, Seed: 37}).All()

	disagreements := 0
	for _, m := range messages {
		parsed, _ := hl7.Parse(m.Raw)
		doc, _, err := cda.ParseEmbedded(parsed)
		if err != nil {
			continue
		}
		if report := cda.CheckAgreement(doc); len(report.Findings) > 0 {
			disagreements++
		}
	}
	if disagreements == 0 {
		t.Error("no generated document disagreed with itself in 200 messages")
	}
	if disagreements == len(messages) {
		t.Error("every generated document disagrees with itself, which is not realistic")
	}
}

func TestCorpusIsMixedAndExactlyTheRequestedSize(t *testing.T) {
	messages := Corpus(Options{Count: 200, Seed: 41})

	// Exact rather than approximate, because a test asserting a count would
	// otherwise be flaky.
	if len(messages) != 200 {
		t.Fatalf("want exactly 200 messages, got %d", len(messages))
	}

	kinds := map[string]int{}
	for _, m := range messages {
		kinds[m.Type]++
	}
	for _, want := range []string{"ADT", "ORU", "ORM", "MDM"} {
		if kinds[want] == 0 {
			t.Errorf("the corpus has no %s: %v", want, kinds)
		}
	}
	// Mostly ADT, as a real feed is.
	if kinds["ADT"] < kinds["ORU"] {
		t.Errorf("a real feed is mostly ADT, got %v", kinds)
	}
}

func TestCorpusIsInterleavedByTime(t *testing.T) {
	// A channel that only ever sees a thousand ADTs followed by a thousand ORUs is
	// not being tested the way it will be used.
	messages := Corpus(Options{Count: 100, Seed: 43})

	for i := 1; i < len(messages); i++ {
		if messages[i].At.Before(messages[i-1].At) {
			t.Fatalf("message %d is earlier than the one before it", i)
		}
	}

	// And the types should actually be mixed rather than merely sorted into blocks.
	switches := 0
	for i := 1; i < len(messages); i++ {
		if messages[i].Type != messages[i-1].Type {
			switches++
		}
	}
	if switches < 10 {
		t.Errorf("the corpus only changes type %d times, so it is still in blocks", switches)
	}
}

func TestNamesAreObviouslyFictional(t *testing.T) {
	// Not a disclaimer, a design constraint: a generator producing plausible real
	// names would eventually put one into a test fixture, a bug report and then a
	// repository.
	for _, p := range people {
		if p.Family == "" || p.Given == "" {
			t.Fatal("a person has no name")
		}
	}
	joined := ""
	for _, p := range people {
		joined += p.Family + " "
	}
	// Each surname should read as a placeholder to anybody glancing at it.
	for _, want := range []string{"Testpatient", "Sampleson", "Fixtureton", "Placeholder"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected obviously fictional surnames, missing %q", want)
		}
	}
}
