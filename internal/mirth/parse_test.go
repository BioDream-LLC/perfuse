package mirth

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

func loadFixture(t *testing.T) *Channel {
	t.Helper()
	c, err := ParseChannelFile("testdata/adt_channel.xml")
	if err != nil {
		t.Fatalf("ParseChannelFile: %v", err)
	}
	return c
}

func TestParseChannelHeader(t *testing.T) {
	c := loadFixture(t)

	if got, want := c.Name, "ADT Inbound - Synthetic Site"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := c.MirthVersion, "4.4.0"; got != want {
		t.Errorf("MirthVersion = %q, want %q", got, want)
	}
	if got, want := c.Revision, 12; got != want {
		t.Errorf("Revision = %d, want %d", got, want)
	}
	if !c.Enabled {
		t.Error("Enabled = false, want true")
	}
	if got, want := c.PreprocessingScript, "return message;"; got != want {
		t.Errorf("PreprocessingScript = %q, want %q", got, want)
	}
}

func TestParseChannelProperties(t *testing.T) {
	p := loadFixture(t).Properties

	if got, want := p.MessageStorageMode, "DEVELOPMENT"; got != want {
		t.Errorf("MessageStorageMode = %q, want %q", got, want)
	}
	if got, want := p.PruneMetaDataDays, 30; got != want {
		t.Errorf("PruneMetaDataDays = %d, want %d", got, want)
	}
	if got, want := p.PruneContentDays, 14; got != want {
		t.Errorf("PruneContentDays = %d, want %d", got, want)
	}
	if !p.StoreAttachments {
		t.Error("StoreAttachments = false, want true")
	}
	if p.EncryptData {
		t.Error("EncryptData = true, want false")
	}
	if !p.ClearGlobalChannelMap {
		t.Error("ClearGlobalChannelMap = false, want true")
	}
	if got, want := strings.Join(p.ResourceIDs, ","), "Default Resource"; got != want {
		t.Errorf("ResourceIDs = %q, want %q", got, want)
	}
}

func TestParseSourceConnector(t *testing.T) {
	src := loadFixture(t).Source

	if got, want := src.Transport, "TCP Listener"; got != want {
		t.Errorf("Transport = %q, want %q", got, want)
	}
	if got, want := src.Mode, ModeSource; got != want {
		t.Errorf("Mode = %q, want %q", got, want)
	}
	if got, want := src.PropertiesClass, "com.mirth.connect.connectors.tcp.TcpReceiverProperties"; got != want {
		t.Errorf("PropertiesClass = %q, want %q", got, want)
	}
	if !src.WaitForPrevious {
		t.Error("WaitForPrevious = false, want true")
	}

	// Transport settings are kept as flattened dotted paths because every
	// connector type has a different shape.
	if got, want := src.Properties["listenerConnectorProperties.port"], "6661"; got != want {
		t.Errorf("port = %q, want %q", got, want)
	}
	if got, want := src.Properties["transmissionModeProperties.startOfMessageBytes"], "0B"; got != want {
		t.Errorf("startOfMessageBytes = %q, want %q", got, want)
	}
}

func TestParseTransformerSteps(t *testing.T) {
	steps := loadFixture(t).Source.Transformer.Steps

	if got, want := len(steps), 4; got != want {
		t.Fatalf("len(Steps) = %d, want %d", got, want)
	}

	wantKinds := []StepKind{StepMapper, StepJavaScript, StepXSLT, StepUnknown}
	for i, want := range wantKinds {
		if got := steps[i].Kind; got != want {
			t.Errorf("step %d Kind = %q, want %q", i, got, want)
		}
		if got := steps[i].Sequence; got != i {
			t.Errorf("step %d Sequence = %d, want %d", i, got, i)
		}
	}

	if got, want := steps[0].Variable, "mrn"; got != want {
		t.Errorf("mapper Variable = %q, want %q", got, want)
	}
	if !strings.Contains(steps[0].Mapping, "PID.3.1") {
		t.Errorf("mapper Mapping missing PID.3.1: %q", steps[0].Mapping)
	}
	if !strings.Contains(steps[1].Script, "MSH.4.1") {
		t.Errorf("javascript Script missing MSH.4.1: %q", steps[1].Script)
	}
	if !strings.Contains(steps[2].Script, "xsl:stylesheet") {
		t.Errorf("xslt Script missing stylesheet: %q", steps[2].Script)
	}

	// An unknown third-party plugin must be reported precisely, not dropped.
	unknown := steps[3]
	if got, want := unknown.RawKind, "com.example.someplugin.WidgetStep"; got != want {
		t.Errorf("unknown RawKind = %q, want %q", got, want)
	}
	if got, want := unknown.Script, "doSomethingProprietary();"; got != want {
		t.Errorf("unknown Script = %q, want %q", got, want)
	}
}

func TestParseFilterRules(t *testing.T) {
	rules := loadFixture(t).Source.Filter.Rules

	if got, want := len(rules), 2; got != want {
		t.Fatalf("len(Rules) = %d, want %d", got, want)
	}

	if got, want := rules[0].Kind, RuleBuilder; got != want {
		t.Errorf("rule 0 Kind = %q, want %q", got, want)
	}
	if got, want := rules[0].Condition, "NOT_EQUALS"; got != want {
		t.Errorf("rule 0 Condition = %q, want %q", got, want)
	}
	if got, want := strings.Join(rules[0].Values, ","), `"A28"`; got != want {
		t.Errorf("rule 0 Values = %q, want %q", got, want)
	}
	if !strings.Contains(rules[0].Field, "MSH.9.2") {
		t.Errorf("rule 0 Field missing MSH.9.2: %q", rules[0].Field)
	}

	if got, want := rules[1].Kind, RuleJavaScript; got != want {
		t.Errorf("rule 1 Kind = %q, want %q", got, want)
	}
	if got, want := rules[1].Operator, "AND"; got != want {
		t.Errorf("rule 1 Operator = %q, want %q", got, want)
	}
	// The > in the script must survive XML entity decoding.
	if !strings.Contains(rules[1].Script, "> 0") {
		t.Errorf("rule 1 Script did not decode entity: %q", rules[1].Script)
	}
}

func TestParseDestinations(t *testing.T) {
	c := loadFixture(t)

	if got, want := len(c.Destinations), 2; got != want {
		t.Fatalf("len(Destinations) = %d, want %d", got, want)
	}

	api := c.Destinations[0]
	if got, want := api.Name, "Registry API"; got != want {
		t.Errorf("dest 0 Name = %q, want %q", got, want)
	}
	if got, want := api.Transport, "HTTP Sender"; got != want {
		t.Errorf("dest 0 Transport = %q, want %q", got, want)
	}
	if got, want := api.Mode, ModeDestination; got != want {
		t.Errorf("dest 0 Mode = %q, want %q", got, want)
	}
	if !api.Enabled {
		t.Error("dest 0 Enabled = false, want true")
	}
	if got, want := api.Properties["url"], "https://registry.example.invalid/hl7"; got != want {
		t.Errorf("dest 0 url = %q, want %q", got, want)
	}
	if got, want := api.Transformer.OutboundDataType, "JSON"; got != want {
		t.Errorf("dest 0 OutboundDataType = %q, want %q", got, want)
	}

	archive := c.Destinations[1]
	if archive.Enabled {
		t.Error("dest 1 Enabled = true, want false")
	}
	if got, want := archive.MetaDataID, 2; got != want {
		t.Errorf("dest 1 MetaDataID = %d, want %d", got, want)
	}
}

func TestUnrecognisedElementsAreReported(t *testing.T) {
	c := loadFixture(t)

	if got, want := strings.Join(c.Unrecognised, ","), "someFutureElement"; got != want {
		t.Errorf("Unrecognised = %q, want %q", got, want)
	}
}

func TestHasJavaScript(t *testing.T) {
	if !loadFixture(t).HasJavaScript() {
		t.Error("HasJavaScript() = false, want true")
	}

	// A channel with no scripting anywhere can be translated mechanically.
	plain := `<channel version="4.4.0">
	  <name>plain</name>
	  <sourceConnector>
	    <transportName>TCP Listener</transportName>
	    <transformer><elements/></transformer>
	    <filter><elements/></filter>
	  </sourceConnector>
	</channel>`
	c, err := ParseChannel(strings.NewReader(plain))
	if err != nil {
		t.Fatalf("ParseChannel: %v", err)
	}
	if c.HasJavaScript() {
		t.Error("HasJavaScript() = true for a channel with no scripts")
	}
}

func TestRejectsNonChannelDocument(t *testing.T) {
	if _, err := ParseChannel(strings.NewReader(`<channelGroup><name>x</name></channelGroup>`)); err == nil {
		t.Fatal("expected an error for a non-channel root element")
	}
}

func TestClassifySurvivesPackageRenames(t *testing.T) {
	// Recognition is by class-name suffix so that a Mirth package move does not
	// silently turn a known step into an unknown one.
	cases := map[string]StepKind{
		"com.mirth.connect.plugins.mapper.MapperStep": StepMapper,
		"com.acme.relocated.plugins.v9.MapperStep":    StepMapper,
		"MapperStep": StepMapper,
		"com.mirth.connect.plugins.xsltstep.XsltStep": StepXSLT,
		"com.example.someplugin.WidgetStep":           StepUnknown,
	}
	for elem, want := range cases {
		if got := classifyStep(elem); got != want {
			t.Errorf("classifyStep(%q) = %q, want %q", elem, got, want)
		}
	}
}

func TestTheParserToleratesTheVersionAttributesRealMirthWrites(t *testing.T) {
	// Checked against a real Mirth 4.5.2, in a container, on 17 September.
	//
	// The fixture beside this test says what it is: written by hand, not derived from any deployment. That is the same shape of
	// evidence that hid a total failure in the SAML canonicaliser - a thousand lines of tests that signed and verified with the same
	// code, and no real assertion could ever have been accepted. So the fixture was posted to a real Mirth to see what it made of it.
	//
	// **Mirth will not load it.** It accepts the POST and stores the channel as "This channel is invalid. Verify all required
	// extensions are loaded correctly", and the round trip comes back at 1,689 bytes against the original 5,711 with
	// <destinationConnectors/> empty - it discarded every connector.
	//
	// The visible difference is that real Mirth writes version="4.5.2" on every serialised element and the fixture writes none.
	// Adding them did not make Mirth accept it, so something else is missing too, and finding out needs Mirth's own Administrator
	// rather than more guessing at XML.
	//
	// What matters for migration is the other direction, and this test pins it: a document carrying those version attributes parses
	// to exactly the same channel. So the attributes are not a migration hazard, whatever else the fixture is missing.
	raw, err := os.ReadFile("testdata/adt_channel.xml")
	if err != nil {
		t.Fatal(err)
	}

	plain, err := ParseChannel(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	// The same document with the attributes real Mirth writes.
	versioned := string(raw)
	for _, tag := range []string{"sourceConnector", "transformer", "filter", "connector"} {
		versioned = strings.ReplaceAll(versioned, "<"+tag+">", "<"+tag+` version="4.5.2">`)
	}

	versioned = strings.ReplaceAll(versioned, `<listenerConnectorProperties>`, `<listenerConnectorProperties version="4.5.2">`)
	versioned = strings.ReplaceAll(versioned, `<transmissionModeProperties>`, `<transmissionModeProperties version="4.5.2">`)
	versioned = regexp.MustCompile(`<properties class="([^"]+)">`).ReplaceAllString(versioned, `<properties class="$1" version="4.5.2">`)

	// A positive control: if the substitutions stopped matching, this would compare a document with itself.
	if strings.Count(versioned, `version="4.5.2"`) < 10 {
		t.Fatalf("only %d version attributes were added, so this test is comparing a document with itself",
			strings.Count(versioned, `version="4.5.2"`))
	}

	withVersions, err := ParseChannel(strings.NewReader(versioned))
	if err != nil {
		t.Fatalf("a document carrying the attributes real Mirth writes does not parse: %v", err)
	}

	if withVersions.Name != plain.Name {
		t.Errorf("name differs: %q against %q", withVersions.Name, plain.Name)
	}

	if len(withVersions.Destinations) != len(plain.Destinations) {
		t.Errorf("destination count differs: %d against %d - a real export would lose destinations",
			len(withVersions.Destinations), len(plain.Destinations))
	}
}
