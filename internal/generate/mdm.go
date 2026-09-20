package generate

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// MDM generation, which is separate because it needs a document to carry.
//
// The document is a small but genuine C-CDA: correct namespace, template
// identifiers that the vocabulary recognises, a recordTarget, and a section with
// both a narrative and a coded entry. A generator that produced a stub would
// exercise the extraction path and none of the reading path, which is where the
// interesting work is.

func (g *Generator) mdm(p person, at time.Time, awkward string) Message {
	controlID := g.controlID("MDM")
	docID := fmt.Sprintf("DOC%08d", 30000000+g.seq)

	doc := g.clinicalDocument(p, at, docID, awkward)
	encoded := base64.StdEncoding.EncodeToString([]byte(doc))

	// TXA is built from a field-number map rather than by counting pipes. I have
	// put the parent document number in field 14 instead of 13 by hand before, and
	// a replacement that looks like a new document is exactly the failure the
	// document viewer exists to catch.
	txa := map[int]string{
		1:  "1",
		2:  "DS",
		3:  "AP^application^HL7",
		4:  hl7Time(at.Add(-5 * time.Minute)),
		9:  fmt.Sprintf("D%04d^Author^Doctor", 1000+g.rng.IntN(500)),
		12: docID + "^" + g.opts.Facility,
		17: "AU",
		19: "AV",
	}

	event := "T02"
	if awkward == "extra-repetition" {
		// A replacement. TXA-13 names the document being replaced, and a receiver
		// that files both versions holds two contradictory records with nothing to
		// say which is current.
		event = "T02"
		txa[13] = fmt.Sprintf("DOC%08d^%s", 30000000+g.seq-1, g.opts.Facility)
	}
	if awkward == "missing-optional" {
		delete(txa, 17)
		delete(txa, 19)
	}

	encoding := "Base64"
	if awkward == "escaped-value" {
		// Declared as plain ASCII while actually base64, which senders really do.
		// Losing a clinical document over a metadata mistake is the wrong trade, so
		// the reader tolerates it — and this is how that path gets exercised.
		encoding = "A"
	}

	segments := []string{
		g.msh("MDM", event, "MDM_T02", controlID, at),
		fmt.Sprintf("EVN|%s|%s", event, hl7Time(at)),
		g.pid(p, awkward),
		txaSegment(txa),
		fmt.Sprintf("OBX|1|ED|34133-9^Summarization of Episode Note^LN||^application/hl7-cda+xml^^%s^%s||||||F",
			encoding, encoded),
	}

	return Message{
		Raw:       []byte(strings.Join(segments, "\r") + "\r"),
		ControlID: controlID,
		Type:      "MDM",
		Event:     event,
		Patient:   p.MRN,
		At:        at,
		Awkward:   awkward,
	}
}

// txaSegment builds TXA from a field-number map, so a field cannot land one
// position out.
func txaSegment(fields map[int]string) string {
	highest := 0
	for n := range fields {
		if n > highest {
			highest = n
		}
	}
	parts := make([]string, highest+1)
	parts[0] = "TXA"
	for n, v := range fields {
		parts[n] = v
	}
	return strings.Join(parts, "|")
}

// clinicalDocument builds a small C-CDA.
func (g *Generator) clinicalDocument(p person, at time.Time, docID, awkward string) string {
	allergies := []struct{ code, name, reaction string }{
		{"7980", "Penicillin G", "Hives"},
		{"2670", "Codeine", "Nausea"},
		{"1191", "Aspirin", "Wheezing"},
	}
	a := allergies[g.seq%len(allergies)]

	// One message in a while gets a document whose narrative and coded entries
	// disagree, because that is what the agreement check exists to find and a
	// generator that never produced one would leave it permanently untested.
	extraNarrative := ""
	if awkward == "empty-components" {
		extraNarrative = `<tr><td ID="allergy-2">Sulfamethoxazole</td><td>Rash</td></tr>`
	}

	middle := ""
	if p.Middle != "" {
		middle = fmt.Sprintf("<given>%s</given>", p.Middle)
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <realmCode code="US"/>
  <typeId root="2.16.840.1.113883.1.3" extension="POCD_HD000040"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.1"/>
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <id root="2.16.840.1.113883.19.5.99999.1" extension="%s"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1"
        displayName="Summarization of Episode Note"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="%s"/>
  <confidentialityCode code="N" codeSystem="2.16.840.1.113883.5.25"/>
  <languageCode code="en-US"/>
  <recordTarget>
    <patientRole>
      <id root="2.16.840.1.113883.19.5.99999.2" extension="%s"/>
      <addr use="HP">
        <streetAddressLine>%s</streetAddressLine>
        <city>%s</city><state>%s</state><postalCode>%s</postalCode>
      </addr>
      <patient>
        <name><given>%s</given>%s<family>%s</family></name>
        <administrativeGenderCode code="%s" codeSystem="2.16.840.1.113883.5.1"/>
        <birthTime value="%s"/>
      </patient>
    </patientRole>
  </recordTarget>
  <author>
    <time value="%s"/>
    <assignedAuthor>
      <id root="2.16.840.1.113883.19.5.99999.3" extension="D1001"/>
      <assignedPerson><name><given>Author</given><family>Doctor</family></name></assignedPerson>
    </assignedAuthor>
  </author>
  <custodian>
    <assignedCustodian>
      <representedCustodianOrganization>
        <id root="2.16.840.1.113883.19.5.99999.4"/>
        <name>%s</name>
      </representedCustodianOrganization>
    </assignedCustodian>
  </custodian>
  <component><structuredBody>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.6.1"/>
      <code code="48765-2" codeSystem="2.16.840.1.113883.6.1"
            displayName="Allergies and Adverse Reactions"/>
      <title>Allergies</title>
      <text><table>
        <thead><tr><th>Substance</th><th>Reaction</th></tr></thead>
        <tbody>
          <tr><td ID="allergy-1">%s</td><td>%s</td></tr>%s
        </tbody>
      </table></text>
      <entry typeCode="DRIV">
        <act classCode="ACT" moodCode="EVN">
          <templateId root="2.16.840.1.113883.10.20.22.4.30"/>
          <id root="2.16.840.1.113883.19.5.99999.5" extension="A%d"/>
          <statusCode code="active"/>
          <entryRelationship typeCode="SUBJ">
            <observation classCode="OBS" moodCode="EVN">
              <templateId root="2.16.840.1.113883.10.20.22.4.7"/>
              <code code="ASSERTION" codeSystem="2.16.840.1.113883.5.4"/>
              <statusCode code="completed"/>
              <value xsi:type="CD" code="%s"
                     codeSystem="2.16.840.1.113883.6.88" displayName="%s"
                     xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"/>
              <text><reference value="#allergy-1"/></text>
              <!-- The reaction, as a real C-CDA carries it. Without this the
                   narrative names a reaction that no coded entry does, and the
                   agreement check is right to complain: a receiving system would
                   import the allergy and lose what it does to the patient. -->
              <entryRelationship typeCode="MFST" inversionInd="true">
                <observation classCode="OBS" moodCode="EVN">
                  <templateId root="2.16.840.1.113883.10.20.22.4.9"/>
                  <code code="ASSERTION" codeSystem="2.16.840.1.113883.5.4"/>
                  <statusCode code="completed"/>
                  <value xsi:type="CD" code="%s"
                         codeSystem="2.16.840.1.113883.6.96" displayName="%s"
                         xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"/>
                </observation>
              </entryRelationship>
            </observation>
          </entryRelationship>
        </act>
      </entry>
    </section></component>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.3.1"/>
      <code code="30954-2" codeSystem="2.16.840.1.113883.6.1"
            displayName="Relevant diagnostic tests and laboratory data"/>
      <title>Results</title>
      <text><table><tbody>
        <tr><td>Hemoglobin</td><td>13.5 g/dL</td></tr>
      </tbody></table></text>
      <entry typeCode="DRIV">
        <observation classCode="OBS" moodCode="EVN">
          <templateId root="2.16.840.1.113883.10.20.22.4.2"/>
          <code code="718-7" codeSystem="2.16.840.1.113883.6.1" displayName="Hemoglobin"/>
          <statusCode code="completed"/>
          <effectiveTime value="%s"/>
          <value xsi:type="PQ" value="13.5" unit="g/dL"
                 xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"/>
        </observation>
      </entry>
    </section></component>
  </structuredBody></component>
</ClinicalDocument>`,
		docID, hl7Time(at), p.MRN,
		p.Street, p.City, p.State, p.Postal,
		p.Given, middle, p.Family, p.Sex, p.Born,
		hl7Time(at.Add(-5*time.Minute)),
		g.opts.Facility,
		a.name, a.reaction, extraNarrative,
		g.seq, a.code, a.name,
		reactionCode(a.reaction), a.reaction,
		hl7Time(at.Add(-2*time.Hour)))
}

// Corpus writes a mixed batch, which is what a demo or a soak test wants: a real
// site's traffic is mostly ADT with results and orders alongside, not one type.
func Corpus(opts Options) []Message {
	total := opts.Count
	if total <= 0 {
		total = 1
	}

	// Roughly the proportions of a real hospital feed. An even split across message
	// types produces a feed nobody has.
	plan := []struct {
		kind  Kind
		share float64
	}{
		{ADT, 0.60},
		{ORU, 0.25},
		{ORM, 0.10},
		{MDM, 0.05},
	}

	var out []Message
	for i, p := range plan {
		count := int(float64(total) * p.share)
		if i == len(plan)-1 {
			// Give the remainder to the last kind so the total is exact rather than
			// approximately right, which would make a test asserting a count flaky.
			count = total - len(out)
		}
		if count <= 0 {
			continue
		}
		o := opts
		o.Kind = p.kind
		o.Count = count
		// Derive a distinct seed per kind so two kinds do not produce the same
		// patients in the same order, while the whole corpus stays reproducible.
		if opts.Seed != 0 {
			o.Seed = opts.Seed + uint64(i)*7919
		}
		out = append(out, New(o).All()...)
	}

	// Interleaved by timestamp, because a feed arrives mixed and a channel that
	// only ever sees a thousand ADTs followed by a thousand ORUs is not being
	// tested the way it will be used.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].At.Before(out[j-1].At); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// reactionCode maps the handful of reactions the generator emits to SNOMED codes.
//
// Real codes rather than invented ones, because the agreement check compares the
// narrative against what a coded entry says, and a made-up code would still pass
// that comparison while being useless to anything downstream.
func reactionCode(reaction string) string {
	switch reaction {
	case "Hives":
		return "247472004" // Weal
	case "Nausea":
		return "422587007" // Nausea
	case "Wheezing":
		return "56018004" // Wheezing
	default:
		return "418799008" // Finding reported by subject
	}
}
