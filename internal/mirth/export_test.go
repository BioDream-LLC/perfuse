package mirth

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

const testChannel = `<channel version="4.5.0">
  <id>abc-123</id>
  <name>ADT to EHR</name>
  <description>Routes ADT messages to the EHR</description>
  <revision>3</revision>
  <enabled>true</enabled>
  <sourceConnector version="4.5.0">
    <metaDataId>0</metaDataId>
    <name>Source</name>
    <properties class="com.mirth.connect.connectors.tcp.TcpReceiverProperties">
      <listenerConnectorProperties>
        <host>0.0.0.0</host>
        <port>6661</port>
      </listenerConnectorProperties>
    </properties>
    <transformer>
      <inboundDataType>HL7V2</inboundDataType>
      <outboundDataType>HL7V2</outboundDataType>
      <elements>
        <com.mirth.connect.plugins.mapper.MapperStep>
          <sequenceNumber>0</sequenceNumber>
          <name>Extract MRN</name>
          <variable>mrn</variable>
          <mapping>msg['PID']['PID.3']['PID.3.1'].toString()</mapping>
        </com.mirth.connect.plugins.mapper.MapperStep>
      </elements>
    </transformer>
    <filter>
      <elements>
        <com.mirth.connect.plugins.rulebuilder.RuleBuilderRule>
          <sequenceNumber>0</sequenceNumber>
          <name>Accept ADT only</name>
          <operator>NONE</operator>
          <field>msg['MSH']['MSH.9']['MSH.9.1'].toString()</field>
          <condition>EQUALS</condition>
          <values>
            <string>ADT</string>
          </values>
        </com.mirth.connect.plugins.rulebuilder.RuleBuilderRule>
      </elements>
    </filter>
    <transportName>TCP Listener</transportName>
    <mode>SOURCE</mode>
    <enabled>true</enabled>
    <waitForPrevious>false</waitForPrevious>
  </sourceConnector>
  <destinationConnectors>
    <connector>
      <metaDataId>1</metaDataId>
      <name>EHR Sender</name>
      <properties class="com.mirth.connect.connectors.tcp.TcpDispatcherProperties">
        <remoteAddress>ehr.hospital.local</remoteAddress>
        <remotePort>2575</remotePort>
      </properties>
      <transformer>
        <inboundDataType>HL7V2</inboundDataType>
        <outboundDataType>HL7V2</outboundDataType>
      </transformer>
      <filter/>
      <transportName>TCP Sender</transportName>
      <mode>DESTINATION</mode>
      <enabled>true</enabled>
      <waitForPrevious>true</waitForPrevious>
    </connector>
  </destinationConnectors>
  <preprocessingScript></preprocessingScript>
  <postprocessingScript></postprocessingScript>
  <deployScript></deployScript>
  <undeployScript></undeployScript>
  <properties class="com.mirth.connect.model.ChannelProperties">
    <messageStorageMode>DEVELOPMENT</messageStorageMode>
    <encryptData>false</encryptData>
    <removeContentOnCompletion>false</removeContentOnCompletion>
    <storeAttachments>false</storeAttachments>
    <clearGlobalChannelMap>true</clearGlobalChannelMap>
  </properties>
</channel>`

func TestExportRoundTrip(t *testing.T) {
	// Parse the test channel.
	ch, err := ParseChannel(strings.NewReader(testChannel))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Export it.
	var buf bytes.Buffer
	if err := Export(&buf, ch); err != nil {
		t.Fatalf("export: %v", err)
	}

	output := buf.String()

	// Verify key elements are present.
	checks := []string{
		`<channel version="4.5.0"`,
		`<id>abc-123</id>`,
		`<name>ADT to EHR</name>`,
		`<description>Routes ADT messages to the EHR</description>`,
		`<revision>3</revision>`,
		`<enabled>true</enabled>`,
		`<sourceConnector`,
		`<transportName>TCP Listener</transportName>`,
		`<mode>SOURCE</mode>`,
		`class="com.mirth.connect.connectors.tcp.TcpReceiverProperties"`,
		`<inboundDataType>HL7V2</inboundDataType>`,
		`com.mirth.connect.plugins.mapper.MapperStep`,
		`<variable>mrn</variable>`,
		`<mapping>msg[&#39;PID&#39;]`, // XML-escaped single quotes
		`com.mirth.connect.plugins.rulebuilder.RuleBuilderRule`,
		`<field>msg[`,
		`<condition>EQUALS</condition>`,
		`<string>ADT</string>`,
		`<destinationConnectors>`,
		// With its version, which is what Mirth requires. This assertion used to demand a bare <connector> - the exact shape that made
		// a real Mirth store the channel with destinationConnectors emptied. The test was holding the defect in place.
		`<connector version=`,
		`<name>EHR Sender</name>`,
		`class="com.mirth.connect.connectors.tcp.TcpDispatcherProperties"`,
		`<mode>DESTINATION</mode>`,
		`class="com.mirth.connect.model.ChannelProperties"`,
		`<messageStorageMode>DEVELOPMENT</messageStorageMode>`,
	}

	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("output missing %q\n\nFull output:\n%s", check, output)
		}
	}

	// Verify it parses back.
	ch2, err := ParseChannel(strings.NewReader(output))
	if err != nil {
		t.Fatalf("re-parse exported XML: %v", err)
	}

	// Verify key fields survived.
	if ch2.ID != "abc-123" {
		t.Errorf("re-parsed ID = %q", ch2.ID)
	}
	if ch2.Name != "ADT to EHR" {
		t.Errorf("re-parsed Name = %q", ch2.Name)
	}
	if !ch2.Enabled {
		t.Error("re-parsed channel is not enabled")
	}
	if ch2.Source.Transport != "TCP Listener" {
		t.Errorf("re-parsed source transport = %q", ch2.Source.Transport)
	}
	if len(ch2.Destinations) != 1 {
		t.Fatalf("re-parsed destinations = %d, want 1", len(ch2.Destinations))
	}
	if ch2.Destinations[0].Name != "EHR Sender" {
		t.Errorf("re-parsed dest name = %q", ch2.Destinations[0].Name)
	}
	if len(ch2.Source.Transformer.Steps) != 1 {
		t.Fatalf("re-parsed source steps = %d, want 1", len(ch2.Source.Transformer.Steps))
	}
	if ch2.Source.Transformer.Steps[0].Kind != StepMapper {
		t.Errorf("re-parsed step kind = %q", ch2.Source.Transformer.Steps[0].Kind)
	}
	if len(ch2.Source.Filter.Rules) != 1 {
		t.Fatalf("re-parsed source rules = %d, want 1", len(ch2.Source.Filter.Rules))
	}
	if ch2.Source.Filter.Rules[0].Kind != RuleBuilder {
		t.Errorf("re-parsed rule kind = %q", ch2.Source.Filter.Rules[0].Kind)
	}
}

func TestExportEmptyChannel(t *testing.T) {
	ch := &Channel{
		ID:      "empty-1",
		Name:    "Empty Channel",
		Enabled: true,
		Source: Connector{
			Name:      "Source",
			Mode:      ModeSource,
			Enabled:   true,
			Transport: "Channel Reader",
		},
	}

	var buf bytes.Buffer
	if err := Export(&buf, ch); err != nil {
		t.Fatalf("export: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `<name>Empty Channel</name>`) {
		t.Errorf("missing channel name in output:\n%s", output)
	}
	if !strings.Contains(output, `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Errorf("missing XML declaration")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AUDIT TESTS — added during security/correctness audit 2026-08-25
// ─────────────────────────────────────────────────────────────────────────────

// TestExportFullyPopulatedRoundTrip is the high-yield test: a channel with
// every field populated, asserting field-by-field equality after round-trip.
// The existing round-trip test only populates three fields and proves nothing
// about the other thirty.
func TestExportFullyPopulatedRoundTrip(t *testing.T) {
	ch := &Channel{
		ID:           "full-001",
		Name:         "Full Channel",
		Description:  "Every field populated",
		Enabled:      true,
		Revision:     7,
		MirthVersion: "4.5.0",
		Source: Connector{
			MetaDataID:      0,
			Name:            "Source",
			Mode:            ModeSource,
			Enabled:         true,
			Transport:       "TCP Listener",
			PropertiesClass: "com.mirth.connect.connectors.tcp.TcpReceiverProperties",
			Properties: map[string]string{
				"listenerConnectorProperties.host": "0.0.0.0",
				"listenerConnectorProperties.port": "6661",
				"serverMode":                       "true",
			},
			WaitForPrevious: true,
			Transformer: Transformer{
				InboundDataType:  "HL7V2",
				OutboundDataType: "HL7V2",
				Steps: []Step{
					{Sequence: 0, Name: "Extract MRN", Kind: StepMapper, RawKind: "com.mirth.connect.plugins.mapper.MapperStep", Variable: "mrn", Mapping: "msg['PID']['PID.3']['PID.3.1'].toString()"},
					{Sequence: 1, Name: "JS Step", Kind: StepJavaScript, RawKind: "com.mirth.connect.plugins.javascriptstep.JavaScriptStep", Script: "var x = 1;"},
					{Sequence: 2, Name: "Builder", Kind: StepMessageBuilder, RawKind: "com.mirth.connect.plugins.messagebuilder.MessageBuilderStep", Variable: "MSH.10", Mapping: "UUID()", Script: "helper();"},
					{Sequence: 3, Name: "XSLT", Kind: StepXSLT, RawKind: "com.mirth.connect.plugins.xsltstep.XsltStep", Script: "<xsl:stylesheet/>"},
					{Sequence: 4, Name: "External", Kind: StepExternalScript, RawKind: "com.mirth.connect.plugins.scriptfilestep.ExternalScriptStep", Script: "/opt/scripts/fix.js"},
					{Sequence: 5, Name: "Iterator", Kind: StepIterator, RawKind: "com.mirth.connect.plugins.iteratorstep.IteratorStep", Children: []Step{
						{Sequence: 0, Name: "Nested Mapper", Kind: StepMapper, RawKind: "com.mirth.connect.plugins.mapper.MapperStep", Variable: "sub", Mapping: "msg['OBX']['OBX.5'].toString()"},
					}},
					{Sequence: 6, Name: "Unknown Plugin", Kind: StepUnknown, RawKind: "com.example.WidgetStep", Script: "doWidget();"},
				},
			},
			Filter: Filter{
				Rules: []Rule{
					{Sequence: 0, Name: "Accept ADT", Kind: RuleBuilder, RawKind: "com.mirth.connect.plugins.rulebuilder.RuleBuilderRule", Operator: "NONE", Field: "msg['MSH']['MSH.9']['MSH.9.1'].toString()", Condition: "EQUALS", Values: []string{"ADT", "ORM"}},
					{Sequence: 1, Name: "JS Rule", Kind: RuleJavaScript, RawKind: "com.mirth.connect.plugins.javascriptrule.JavaScriptRule", Operator: "AND", Script: "return true;"},
					{Sequence: 2, Name: "Ext Rule", Kind: RuleExternalScript, RawKind: "com.mirth.connect.plugins.scriptfilerule.ExternalScriptRule", Operator: "OR", Script: "/opt/rules/check.js"},
					{Sequence: 3, Name: "Iter Rule", Kind: RuleIterator, RawKind: "com.mirth.connect.plugins.iteratorrule.IteratorRule", Operator: "AND", Children: []Rule{
						{Sequence: 0, Name: "Nested Rule", Kind: RuleBuilder, RawKind: "com.mirth.connect.plugins.rulebuilder.RuleBuilderRule", Operator: "NONE", Field: "x", Condition: "EXISTS", Values: []string{"y"}},
					}},
				},
			},
		},
		Destinations: []Connector{
			{
				MetaDataID:      1,
				Name:            "HTTP Dest",
				Mode:            ModeDestination,
				Enabled:         true,
				Transport:       "HTTP Sender",
				PropertiesClass: "com.mirth.connect.connectors.http.HttpDispatcherProperties",
				Properties: map[string]string{
					"method": "POST",
					"url":    "https://example.com/api",
				},
				WaitForPrevious: false,
				Transformer: Transformer{
					InboundDataType:  "HL7V2",
					OutboundDataType: "JSON",
				},
				Filter: Filter{},
			},
			{
				MetaDataID:      2,
				Name:            "File Dest",
				Mode:            ModeDestination,
				Enabled:         false,
				Transport:       "File Writer",
				PropertiesClass: "com.mirth.connect.connectors.file.FileDispatcherProperties",
				Properties: map[string]string{
					"host":          "/var/spool",
					"outputPattern": "${message.messageId}.hl7",
				},
				WaitForPrevious: true,
				Transformer: Transformer{
					InboundDataType:  "HL7V2",
					OutboundDataType: "HL7V2",
					Steps: []Step{
						{Sequence: 0, Name: "Set Path", Kind: StepJavaScript, RawKind: "com.mirth.connect.plugins.javascriptstep.JavaScriptStep", Script: "channelMap.put('path', '/out');"},
					},
				},
				Filter: Filter{
					Rules: []Rule{
						{Sequence: 0, Name: "Check Enabled", Kind: RuleJavaScript, RawKind: "com.mirth.connect.plugins.javascriptrule.JavaScriptRule", Operator: "NONE", Script: "return channelMap.get('enabled');"},
					},
				},
			},
		},
		PreprocessingScript:  "return message.replace('\\r\\n', '\\r');",
		PostprocessingScript: "logger.info('done');",
		DeployScript:         "globalMap.put('started', new Date());",
		UndeployScript:       "globalMap.remove('started');",
		Properties: ChannelProperties{
			MessageStorageMode:      "PRODUCTION",
			EncryptData:             true,
			RemoveContentOnComplete: true,
			StoreAttachments:        true,
			PruneMetaDataDays:       30,
			PruneContentDays:        14,
			ClearGlobalChannelMap:   false,
			ResourceIDs:             []string{"Default Resource", "Custom Lib"},
		},
	}

	var buf bytes.Buffer
	if err := Export(&buf, ch); err != nil {
		t.Fatalf("Export: %v", err)
	}

	ch2, err := ParseChannel(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("re-parse: %v\n\nXML:\n%s", err, buf.String())
	}

	// Channel-level fields.
	assertEqual(t, "ID", ch.ID, ch2.ID)
	assertEqual(t, "Name", ch.Name, ch2.Name)
	assertEqual(t, "Description", ch.Description, ch2.Description)
	assertEqual(t, "Enabled", ch.Enabled, ch2.Enabled)
	assertEqual(t, "Revision", ch.Revision, ch2.Revision)
	assertEqual(t, "MirthVersion", ch.MirthVersion, ch2.MirthVersion)
	assertEqual(t, "PreprocessingScript", ch.PreprocessingScript, ch2.PreprocessingScript)
	assertEqual(t, "PostprocessingScript", ch.PostprocessingScript, ch2.PostprocessingScript)
	assertEqual(t, "DeployScript", ch.DeployScript, ch2.DeployScript)
	assertEqual(t, "UndeployScript", ch.UndeployScript, ch2.UndeployScript)

	// Channel properties.
	assertEqual(t, "Properties.MessageStorageMode", ch.Properties.MessageStorageMode, ch2.Properties.MessageStorageMode)
	assertEqual(t, "Properties.EncryptData", ch.Properties.EncryptData, ch2.Properties.EncryptData)
	assertEqual(t, "Properties.RemoveContentOnComplete", ch.Properties.RemoveContentOnComplete, ch2.Properties.RemoveContentOnComplete)
	assertEqual(t, "Properties.StoreAttachments", ch.Properties.StoreAttachments, ch2.Properties.StoreAttachments)
	assertEqual(t, "Properties.PruneMetaDataDays", ch.Properties.PruneMetaDataDays, ch2.Properties.PruneMetaDataDays)
	assertEqual(t, "Properties.PruneContentDays", ch.Properties.PruneContentDays, ch2.Properties.PruneContentDays)
	assertEqual(t, "Properties.ClearGlobalChannelMap", ch.Properties.ClearGlobalChannelMap, ch2.Properties.ClearGlobalChannelMap)
	assertSliceEqual(t, "Properties.ResourceIDs", ch.Properties.ResourceIDs, ch2.Properties.ResourceIDs)

	// Source connector.
	assertConnectorEqual(t, "Source", ch.Source, ch2.Source)

	// Destinations.
	if len(ch2.Destinations) != len(ch.Destinations) {
		t.Fatalf("Destinations count = %d, want %d", len(ch2.Destinations), len(ch.Destinations))
	}
	for i := range ch.Destinations {
		assertConnectorEqual(t, fmt.Sprintf("Destinations[%d]", i), ch.Destinations[i], ch2.Destinations[i])
	}
}

func assertEqual[T comparable](t *testing.T, field string, want, got T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", field, got, want)
	}
}

func assertSliceEqual(t *testing.T, field string, want, got []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s length = %d, want %d (%v vs %v)", field, len(got), len(want), got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q", field, i, got[i], want[i])
		}
	}
}

func assertConnectorEqual(t *testing.T, prefix string, want, got Connector) {
	t.Helper()
	assertEqual(t, prefix+".MetaDataID", want.MetaDataID, got.MetaDataID)
	assertEqual(t, prefix+".Name", want.Name, got.Name)
	assertEqual(t, prefix+".Mode", want.Mode, got.Mode)
	assertEqual(t, prefix+".Enabled", want.Enabled, got.Enabled)
	assertEqual(t, prefix+".Transport", want.Transport, got.Transport)
	assertEqual(t, prefix+".PropertiesClass", want.PropertiesClass, got.PropertiesClass)
	assertEqual(t, prefix+".WaitForPrevious", want.WaitForPrevious, got.WaitForPrevious)

	// Properties map.
	for k, v := range want.Properties {
		if got.Properties[k] != v {
			t.Errorf("%s.Properties[%q] = %q, want %q", prefix, k, got.Properties[k], v)
		}
	}

	// Transformer.
	assertEqual(t, prefix+".Transformer.InboundDataType", want.Transformer.InboundDataType, got.Transformer.InboundDataType)
	assertEqual(t, prefix+".Transformer.OutboundDataType", want.Transformer.OutboundDataType, got.Transformer.OutboundDataType)
	if len(got.Transformer.Steps) != len(want.Transformer.Steps) {
		t.Errorf("%s.Transformer.Steps count = %d, want %d", prefix, len(got.Transformer.Steps), len(want.Transformer.Steps))
	} else {
		for i, ws := range want.Transformer.Steps {
			assertStepEqual(t, fmt.Sprintf("%s.Transformer.Steps[%d]", prefix, i), ws, got.Transformer.Steps[i])
		}
	}

	// Filter.
	if len(got.Filter.Rules) != len(want.Filter.Rules) {
		t.Errorf("%s.Filter.Rules count = %d, want %d", prefix, len(got.Filter.Rules), len(want.Filter.Rules))
	} else {
		for i, wr := range want.Filter.Rules {
			assertRuleEqual(t, fmt.Sprintf("%s.Filter.Rules[%d]", prefix, i), wr, got.Filter.Rules[i])
		}
	}
}

func assertStepEqual(t *testing.T, prefix string, want, got Step) {
	t.Helper()
	assertEqual(t, prefix+".Sequence", want.Sequence, got.Sequence)
	assertEqual(t, prefix+".Name", want.Name, got.Name)
	assertEqual(t, prefix+".Kind", want.Kind, got.Kind)
	assertEqual(t, prefix+".Variable", want.Variable, got.Variable)
	assertEqual(t, prefix+".Mapping", want.Mapping, got.Mapping)
	assertEqual(t, prefix+".Script", want.Script, got.Script)
	if len(got.Children) != len(want.Children) {
		t.Errorf("%s.Children count = %d, want %d", prefix, len(got.Children), len(want.Children))
	} else {
		for i := range want.Children {
			assertStepEqual(t, fmt.Sprintf("%s.Children[%d]", prefix, i), want.Children[i], got.Children[i])
		}
	}
}

func assertRuleEqual(t *testing.T, prefix string, want, got Rule) {
	t.Helper()
	assertEqual(t, prefix+".Sequence", want.Sequence, got.Sequence)
	assertEqual(t, prefix+".Name", want.Name, got.Name)
	assertEqual(t, prefix+".Kind", want.Kind, got.Kind)
	assertEqual(t, prefix+".Operator", want.Operator, got.Operator)
	assertEqual(t, prefix+".Field", want.Field, got.Field)
	assertEqual(t, prefix+".Condition", want.Condition, got.Condition)
	assertEqual(t, prefix+".Script", want.Script, got.Script)
	assertSliceEqual(t, prefix+".Values", want.Values, got.Values)
	if len(got.Children) != len(want.Children) {
		t.Errorf("%s.Children count = %d, want %d", prefix, len(got.Children), len(want.Children))
	} else {
		for i := range want.Children {
			assertRuleEqual(t, fmt.Sprintf("%s.Children[%d]", prefix, i), want.Children[i], got.Children[i])
		}
	}
}

// TestExportPropertiesNested verifies that connector properties containing
// dotted paths (e.g. "listenerConnectorProperties.port") are emitted as
// NESTED elements, not flat element names with dots. Mirth's import expects:
//
//	<listenerConnectorProperties><port>6661</port></listenerConnectorProperties>
//
// NOT: <listenerConnectorProperties.port>6661</listenerConnectorProperties.port>
//
// The importer flattens nested XML to dotted paths; the exporter must reverse it.
func TestExportPropertiesNested(t *testing.T) {
	ch := &Channel{
		ID:      "nested-1",
		Name:    "Nested Props Test",
		Enabled: true,
		Source: Connector{
			Name:            "Source",
			Mode:            ModeSource,
			Enabled:         true,
			Transport:       "TCP Listener",
			PropertiesClass: "com.mirth.connect.connectors.tcp.TcpReceiverProperties",
			Properties: map[string]string{
				"listenerConnectorProperties.host": "0.0.0.0",
				"listenerConnectorProperties.port": "6661",
				"serverMode":                       "true",
			},
		},
	}

	var buf bytes.Buffer
	if err := Export(&buf, ch); err != nil {
		t.Fatal(err)
	}
	output := buf.String()

	// It MUST produce nested elements.
	if !strings.Contains(output, "<listenerConnectorProperties>") {
		t.Errorf("expected nested <listenerConnectorProperties>, got flat dotted element names.\n\nOutput:\n%s", output)
	}
	if strings.Contains(output, "<listenerConnectorProperties.port>") {
		t.Errorf("found flat dotted element <listenerConnectorProperties.port>; Mirth will reject this")
	}
	if !strings.Contains(output, "<port>6661</port>") {
		t.Errorf("expected nested <port>6661</port> inside listenerConnectorProperties")
	}
}

// TestExportDeterministic verifies that exporting the same channel twice
// produces identical output. This catches unsorted map iteration.
func TestExportDeterministic(t *testing.T) {
	ch := &Channel{
		ID:      "det-1",
		Name:    "Determinism Test",
		Enabled: true,
		Source: Connector{
			Name:            "Source",
			Mode:            ModeSource,
			Enabled:         true,
			Transport:       "TCP Listener",
			PropertiesClass: "com.mirth.connect.connectors.tcp.TcpReceiverProperties",
			Properties: map[string]string{
				"alpha":   "1",
				"bravo":   "2",
				"charlie": "3",
				"delta":   "4",
				"echo":    "5",
				"foxtrot": "6",
				"golf":    "7",
				"hotel":   "8",
				"india":   "9",
				"juliet":  "10",
			},
		},
	}

	var first bytes.Buffer
	if err := Export(&first, ch); err != nil {
		t.Fatal(err)
	}

	// Run many times to catch non-determinism (Go map iteration is random).
	for i := 0; i < 50; i++ {
		var buf bytes.Buffer
		if err := Export(&buf, ch); err != nil {
			t.Fatal(err)
		}
		if buf.String() != first.String() {
			t.Fatalf("export %d differs from first export — map iteration is not sorted.\n\nFirst:\n%s\n\nGot:\n%s",
				i, first.String(), buf.String())
		}
	}
}

// TestExportCDATATerminator verifies that a script containing the literal ]]>
// (which terminates a CDATA section) does not corrupt the document.
// Since the exporter uses xml.CharData (entity escaping, not CDATA), the
// sequence should survive as &gt; or similar.
func TestExportCDATATerminator(t *testing.T) {
	script := `if (msg['OBX']['OBX.5'].toString().indexOf(']]>') >= 0) { return false; }`
	ch := &Channel{
		ID:      "cdata-1",
		Name:    "CDATA Test",
		Enabled: true,
		Source: Connector{
			Name:      "Source",
			Mode:      ModeSource,
			Enabled:   true,
			Transport: "Channel Reader",
			Transformer: Transformer{
				InboundDataType:  "HL7V2",
				OutboundDataType: "HL7V2",
				Steps: []Step{
					{Sequence: 0, Name: "CDATA script", Kind: StepJavaScript, RawKind: "com.mirth.connect.plugins.javascriptstep.JavaScriptStep", Script: script},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := Export(&buf, ch); err != nil {
		t.Fatal(err)
	}

	// Must parse cleanly.
	ch2, err := ParseChannel(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("exported XML is malformed: %v\n\nXML:\n%s", err, buf.String())
	}

	// The script must survive intact.
	if len(ch2.Source.Transformer.Steps) != 1 {
		t.Fatal("step lost during round-trip")
	}
	if ch2.Source.Transformer.Steps[0].Script != script {
		t.Errorf("script corrupted:\n  got:  %q\n  want: %q", ch2.Source.Transformer.Steps[0].Script, script)
	}
}

// TestExportXMLSpecialChars verifies that special XML characters in
// channel names and scripts survive round-trip.
func TestExportXMLSpecialChars(t *testing.T) {
	name := `Lab <Results> & "Alerts"`
	script := `if (x < 10 && y > 5) { return "ok"; }`
	ch := &Channel{
		ID:      "special-1",
		Name:    name,
		Enabled: true,
		Source: Connector{
			Name:      "Source",
			Mode:      ModeSource,
			Enabled:   true,
			Transport: "Channel Reader",
			Filter: Filter{
				Rules: []Rule{
					{Sequence: 0, Name: "XML chars", Kind: RuleJavaScript, RawKind: "com.mirth.connect.plugins.javascriptrule.JavaScriptRule", Operator: "NONE", Script: script},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := Export(&buf, ch); err != nil {
		t.Fatal(err)
	}

	ch2, err := ParseChannel(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("cannot re-parse: %v\n\nXML:\n%s", err, buf.String())
	}

	assertEqual(t, "Name", name, ch2.Name)
	if len(ch2.Source.Filter.Rules) != 1 {
		t.Fatal("rule lost")
	}
	assertEqual(t, "Script", script, ch2.Source.Filter.Rules[0].Script)
}

// TestExportReportsLossyConversion verifies that the export reports what
// cannot be represented in Mirth XML rather than silently dropping it.
// The Channel.Unrecognised and Connector.Unrecognised fields represent
// elements that existed in the original Mirth file but have no Go struct
// mapping. When exporting a channel that originated from Perfuse (not a
// Mirth import), there may be Perfuse-specific features with no Mirth
// equivalent. The export must surface these.
// TestExportReportsLossyConversion verifies that the export reports what
// cannot be represented in Mirth XML rather than silently dropping it.
// The Channel.Unrecognised and Connector.Unrecognised fields represent
// elements that existed in the original Mirth file but have no Go struct
// mapping. When exporting a channel that originated from Perfuse (not a
// Mirth import), there may be Perfuse-specific features with no Mirth
// equivalent. The export must surface these.
func TestExportReportsLossyConversion(t *testing.T) {
	ch := &Channel{
		ID:           "lossy-1",
		Name:         "Lossy Channel",
		Enabled:      true,
		Unrecognised: []string{"customPerfuseFeature", "advancedRetry"},
		Source: Connector{
			Name:         "Source",
			Mode:         ModeSource,
			Enabled:      true,
			Transport:    "Channel Reader",
			Unrecognised: []string{"perfuseRateLimit"},
		},
	}

	var buf bytes.Buffer
	result, err := ExportWithWarnings(&buf, ch)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// The export must report what was not included.
	if len(result.Warnings) == 0 {
		t.Fatal("ExportWithWarnings returned no warnings for a channel with Unrecognised elements; lossy conversion is silent")
	}

	// Check that each unrecognised thing is mentioned.
	joined := strings.Join(result.Warnings, "\n")
	for _, want := range []string{"customPerfuseFeature", "advancedRetry", "perfuseRateLimit"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warning should mention %q, got:\n%s", want, joined)
		}
	}
}

// TestExportFixtureRoundTrip exports the testdata fixture and re-parses it,
// checking field equality. This tests that a real Mirth-exported channel
// survives import→export→import.
func TestExportFixtureRoundTrip(t *testing.T) {
	ch, err := ParseChannelFile("testdata/adt_channel.xml")
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Export(&buf, ch); err != nil {
		t.Fatal(err)
	}

	ch2, err := ParseChannel(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("re-parse: %v\n\nXML:\n%s", err, buf.String())
	}

	assertEqual(t, "ID", ch.ID, ch2.ID)
	assertEqual(t, "Name", ch.Name, ch2.Name)
	assertEqual(t, "Description", ch.Description, ch2.Description)
	assertEqual(t, "Enabled", ch.Enabled, ch2.Enabled)
	assertEqual(t, "Revision", ch.Revision, ch2.Revision)
	assertEqual(t, "PreprocessingScript", ch.PreprocessingScript, ch2.PreprocessingScript)
	assertEqual(t, "PostprocessingScript", ch.PostprocessingScript, ch2.PostprocessingScript)
	assertEqual(t, "DeployScript", ch.DeployScript, ch2.DeployScript)
	assertEqual(t, "UndeployScript", ch.UndeployScript, ch2.UndeployScript)

	// Properties.
	assertEqual(t, "Properties.MessageStorageMode", ch.Properties.MessageStorageMode, ch2.Properties.MessageStorageMode)
	assertEqual(t, "Properties.PruneMetaDataDays", ch.Properties.PruneMetaDataDays, ch2.Properties.PruneMetaDataDays)
	assertEqual(t, "Properties.PruneContentDays", ch.Properties.PruneContentDays, ch2.Properties.PruneContentDays)
	assertEqual(t, "Properties.StoreAttachments", ch.Properties.StoreAttachments, ch2.Properties.StoreAttachments)
	assertEqual(t, "Properties.ClearGlobalChannelMap", ch.Properties.ClearGlobalChannelMap, ch2.Properties.ClearGlobalChannelMap)
	assertSliceEqual(t, "Properties.ResourceIDs", ch.Properties.ResourceIDs, ch2.Properties.ResourceIDs)

	// Source connector properties must survive the round-trip.
	assertEqual(t, "Source.PropertiesClass", ch.Source.PropertiesClass, ch2.Source.PropertiesClass)
	for k, v := range ch.Source.Properties {
		if ch2.Source.Properties[k] != v {
			t.Errorf("Source.Properties[%q] = %q, want %q", k, ch2.Source.Properties[k], v)
		}
	}

	// Source transformer steps.
	if len(ch2.Source.Transformer.Steps) != len(ch.Source.Transformer.Steps) {
		t.Fatalf("Source steps = %d, want %d", len(ch2.Source.Transformer.Steps), len(ch.Source.Transformer.Steps))
	}
	for i, ws := range ch.Source.Transformer.Steps {
		gs := ch2.Source.Transformer.Steps[i]
		assertEqual(t, fmt.Sprintf("step[%d].Kind", i), ws.Kind, gs.Kind)
		assertEqual(t, fmt.Sprintf("step[%d].Name", i), ws.Name, gs.Name)
		assertEqual(t, fmt.Sprintf("step[%d].Script", i), ws.Script, gs.Script)
		assertEqual(t, fmt.Sprintf("step[%d].Variable", i), ws.Variable, gs.Variable)
		assertEqual(t, fmt.Sprintf("step[%d].Mapping", i), ws.Mapping, gs.Mapping)
	}

	// Source filter rules.
	if len(ch2.Source.Filter.Rules) != len(ch.Source.Filter.Rules) {
		t.Fatalf("Source filter rules = %d, want %d", len(ch2.Source.Filter.Rules), len(ch.Source.Filter.Rules))
	}
	for i, wr := range ch.Source.Filter.Rules {
		gr := ch2.Source.Filter.Rules[i]
		assertEqual(t, fmt.Sprintf("rule[%d].Kind", i), wr.Kind, gr.Kind)
		assertEqual(t, fmt.Sprintf("rule[%d].Script", i), wr.Script, gr.Script)
		assertEqual(t, fmt.Sprintf("rule[%d].Field", i), wr.Field, gr.Field)
		assertEqual(t, fmt.Sprintf("rule[%d].Condition", i), wr.Condition, gr.Condition)
		assertSliceEqual(t, fmt.Sprintf("rule[%d].Values", i), wr.Values, gr.Values)
	}

	// Destinations.
	if len(ch2.Destinations) != len(ch.Destinations) {
		t.Fatalf("Destinations = %d, want %d", len(ch2.Destinations), len(ch.Destinations))
	}
	for i := range ch.Destinations {
		assertEqual(t, fmt.Sprintf("dest[%d].Name", i), ch.Destinations[i].Name, ch2.Destinations[i].Name)
		assertEqual(t, fmt.Sprintf("dest[%d].Transport", i), ch.Destinations[i].Transport, ch2.Destinations[i].Transport)
		assertEqual(t, fmt.Sprintf("dest[%d].Enabled", i), ch.Destinations[i].Enabled, ch2.Destinations[i].Enabled)
		assertEqual(t, fmt.Sprintf("dest[%d].PropertiesClass", i), ch.Destinations[i].PropertiesClass, ch2.Destinations[i].PropertiesClass)
		for k, v := range ch.Destinations[i].Properties {
			if ch2.Destinations[i].Properties[k] != v {
				t.Errorf("dest[%d].Properties[%q] = %q, want %q", i, k, ch2.Destinations[i].Properties[k], v)
			}
		}
	}
}
