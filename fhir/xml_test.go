package fhir

import (
	"strings"
	"testing"
)

func TestFHIRXMLFromJSON(t *testing.T) {
	in := `{"resourceType":"Patient","gender":"male","id":"p1",
	  "text":{"status":"generated","div":"<div xmlns=\"http://www.w3.org/1999/xhtml\">Max &amp; Co</div>"},
	  "extension":[{"url":"http://example.org/x","valueString":"a\"b"}],
	  "name":[{"given":["Max","Moritz"],"_given":[null,{"extension":[{"url":"http://example.org/y","valueBoolean":true}]}],"family":"Graf","use":"official"}],
	  "birthDate":"1968-05-12","_birthDate":{"id":"bd"},"active":true,
	  "contained":[{"resourceType":"Organization","id":"o","name":"<Kasse>"}],
	  "multipleBirthInteger":2}`
	got, err := JSONToXML([]byte(in), R4)
	if err != nil {
		t.Fatal(err)
	}
	want := `<Patient xmlns="http://hl7.org/fhir"><id value="p1"/>` +
		`<text><status value="generated"/><div xmlns="http://www.w3.org/1999/xhtml">Max &amp; Co</div></text>` +
		`<contained><Organization xmlns="http://hl7.org/fhir"><id value="o"/><name value="&lt;Kasse&gt;"/></Organization></contained>` +
		`<extension url="http://example.org/x"><valueString value="a&quot;b"/></extension>` +
		`<active value="true"/>` +
		`<name><use value="official"/><family value="Graf"/><given value="Max"/><given value="Moritz"><extension url="http://example.org/y"><valueBoolean value="true"/></extension></given></name>` +
		`<gender value="male"/><birthDate id="bd" value="1968-05-12"/><multipleBirthInteger value="2"/></Patient>`
	if !strings.Contains(string(got), want) {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// ExampleScenario.instance has an element called resourceType: it is not a resource.
func TestFHIRXMLElementNamedResourceType(t *testing.T) {
	got, err := JSONToXML([]byte(`{"resourceType":"ExampleScenario","instance":[{"resourceId":"a","resourceType":"Patient"}]}`), R4)
	if err != nil {
		t.Fatal(err)
	}
	if want := `<instance><resourceId value="a"/><resourceType value="Patient"/></instance>`; !strings.Contains(string(got), want) {
		t.Errorf("got %s", got)
	}
}
