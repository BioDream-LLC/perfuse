package mpi

import (
	"context"
	"strings"
	"testing"
)

const nhs = "https://fhir.nhs.uk/Id/nhs-number"

// The NHS number arrives under its OID and spaced from one system and under its FHIR URL from another: the index stores
// both one way, so a trusted national number links them.
func TestANationalNumberSentTwoWaysLinks(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	set := Settings{Trusted: []string{nhs}}
	a, err := s.Register(ctx, "", "t", Record{System: hospital, Value: "H1", Family: "Jones", Given: "Ann", BirthDate: "1970-01-01",
		Identifiers: []string{"urn:oid:2.16.840.1.113883.2.1.4.1|943 476 5919"}}, set)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Record.Identifiers; len(got) != 1 || got[0] != nhs+"|9434765919" {
		t.Fatalf("stored as %v", got)
	}
	b, err := s.Register(ctx, "", "t", Record{System: clinic, Value: "C9", Family: "Jonas", Given: "A",
		Identifiers: []string{nhs + "|9434765919"}}, set)
	if err != nil {
		t.Fatal(err)
	}
	if b.Action != "linked" || b.Record.EUID != a.Record.EUID {
		t.Fatalf("not linked by the NHS number: %+v", b)
	}
	// And a search by the number, typed with spaces, finds the person.
	found, err := s.Find(ctx, "", Record{Identifiers: []string{nhs + "|943 476 5919"}}, set, 5)
	if err != nil || len(found) == 0 || found[0].Grade != "certain" {
		t.Fatalf("search: %v %+v", err, found)
	}
}

// A national number with a wrong check digit is somebody's number, but probably not this patient's: it is kept on the
// record, reported, and never used to link.
func TestANationalNumberWithAWrongCheckDigitNeverLinks(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	set := Settings{Trusted: []string{nhs}}
	if _, err := s.Register(ctx, "", "t", Record{System: hospital, Value: "H1", Family: "Jones", Given: "Ann", BirthDate: "1970-01-01",
		Identifiers: []string{nhs + "|9434765919"}}, set); err != nil {
		t.Fatal(err)
	}
	// 9434765918: one digit wrong, and the check digit says so.
	out, err := s.Register(ctx, "", "t", Record{System: lab, Value: "L7", Family: "Smith", Given: "Bob", BirthDate: "1980-05-05",
		Identifiers: []string{nhs + "|943 476 5918"}}, set)
	if err != nil {
		t.Fatal(err)
	}
	if out.Action == "linked" {
		t.Fatal("a mistyped national number linked two different people")
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "check digit") || strings.Contains(out.Warnings[0], "9434765918") {
		t.Fatalf("warnings %v", out.Warnings)
	}
	if got := out.Record.Identifiers; len(got) != 1 || got[0] != nhs+"|9434765918" {
		t.Fatalf("the number should stay on the record for people to see: %v", got)
	}
}

func TestHL7NationalIdentifierTypes(t *testing.T) {
	msg := "MSH|^~\\&|A|B|C|D|202601011200||ADT^A04|1|P|2.5\rPID|1||777^^^HOSP^MR~A123456780^^^^NNDEU||Müller^Anna||19700101|F\r"
	x, err := FromHL7([]byte(msg), FromOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Record.Identifiers) != 1 || x.Record.Identifiers[0] != "http://fhir.de/sid/gkv/kvid-10|A123456780" {
		t.Fatalf("%+v", x.Record)
	}
	msg = "MSH|^~\\&|A|B|C|D|202601011200||ADT^A04|1|P|2.4\rPID|1||777^^^HOSP^MR~9434765919^^^NHS^NH||Jones^Ann\r"
	x, err = FromHL7([]byte(msg), FromOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Record.Identifiers) != 1 || x.Record.Identifiers[0] != nhs+"|9434765919" {
		t.Fatalf("%+v", x.Record)
	}
}

func TestSettingsRefuseUnknownSchemesAndRoles(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	if err := s.SaveSettings(ctx, "", "t", Settings{ShowNumbersTo: "everyone"}); err == nil {
		t.Error("an unknown role was accepted")
	}
	if err := s.SaveSettings(ctx, "", "t", Settings{NationalSystems: map[string]string{"xx-none": "urn:x"}}); err == nil {
		t.Error("an unknown scheme was accepted")
	}
	if err := s.SaveSettings(ctx, "", "t", Settings{NationalSystems: map[string]string{"ie-ppsn": "https://fhir.nhs.uk/Id/nhs-number"}}); err == nil {
		t.Error("another scheme's system was accepted")
	}
	if err := s.SaveSettings(ctx, "", "t", Settings{NationalSystems: map[string]string{"ie-ppsn": "https://example.ie/ppsn"}, ShowNumbersTo: "admin"}); err != nil {
		t.Error(err)
	}
}
