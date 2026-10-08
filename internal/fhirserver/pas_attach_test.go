package fhirserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func submitAttachmentJSON(tracking, member, attachTo string, content string) string {
	return `{"resourceType":"Parameters","parameter":[
		{"name":"TrackingId","valueIdentifier":{"system":"urn:ietf:rfc:3986","value":"` + tracking + `"}},
		{"name":"AttachTo","valueCode":"` + attachTo + `"},
		{"name":"PayerId","valueIdentifier":{"system":"http://hl7.org/fhir/sid/us-npi","value":"1234567893"}},
		{"name":"MemberId","valueIdentifier":{"system":"http://plan.example/member","value":"` + member + `"}},
		{"name":"Attachment","part":[{"name":"LineItem","valueString":"1"},
			{"name":"Code","valueCodeableConcept":{"coding":[{"system":"http://loinc.org","code":"18776-5"}]}},
			{"name":"Content","resource":` + content + `}]},
		{"name":"Final","valueBoolean":true}]}`
}

const planOfCare = `{"resourceType":"DocumentReference","status":"current","type":{"coding":[{"system":"http://loinc.org","code":"18776-5"}]},
	"content":[{"attachment":{"contentType":"text/plain","data":"UGxhbiBvZiBjYXJl"}}]}`

// The documents a pended request asks for arrive under the attachment control number it asked with, and are kept with it.
func TestTheDocumentsAPendedRequestAsksForAreKeptWithIt(t *testing.T) {
	f := newPASFixture(t)
	_, b := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("D1", "2"))
	cr := claimResponseOf(t, b)
	var acn string
	for _, e := range asSliceAny(b["entry"]) {
		if res := asMapAny(asMapAny(e)["resource"]); res["resourceType"] == "CommunicationRequest" {
			acn = str(asMapAny(asSliceAny(res["identifier"])[0])["value"])
		}
	}
	if acn == "" {
		t.Fatalf("the pended response asks for no document: %v", b)
	}
	code, out := pasPost(t, f.h, "/$submit-attachment", submitAttachmentJSON(acn, "12345678901", "preauthorization", planOfCare))
	if code != http.StatusOK || !strings.Contains(toJSON(out), "1 attachment(s) received") {
		t.Fatalf("%d %v", code, out)
	}
	// The Task's identifier works too, at Claim/$submit-attachment.
	if code, out := pasPost(t, f.h, "/Claim/$submit-attachment", submitAttachmentJSON("urn:uuid:"+str(cr["id"]), "12345678901",
		"preauthorization", `{"resourceType":"QuestionnaireResponse","status":"completed"}`)); code != http.StatusOK {
		t.Fatalf("by Task identifier: %d %v", code, out)
	}
	srv := NewServer(f.store, "http://example.test/fhir", nil)
	srv.PAS = &PAS{}
	got, err := srv.Attachments(context.Background(), str(cr["id"]), true)
	if err != nil || len(got) != 2 || got[0].Code != "18776-5" || got[0].LineItems[0] != "1" || !got[0].Final ||
		got[0].Content["resourceType"] != "DocumentReference" || got[1].ResourceType != "QuestionnaireResponse" {
		t.Fatalf("%v %+v", err, got)
	}
	// Still pended: a document arriving decides nothing.
	if _, now, _ := pasGet(t, f.h, "/ClaimResponse/"+str(cr["id"])); actionCodes(now) != "A4" {
		t.Errorf("the request was decided by a document arriving: %s", actionCodes(now))
	}
}

func TestAttachmentsForAnotherMemberOrAnUnknownRequestAreRefused(t *testing.T) {
	f := newPASFixture(t)
	_, b := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("D2", "2"))
	id := str(claimResponseOf(t, b)["id"])
	for name, body := range map[string]string{
		"other member": submitAttachmentJSON("urn:uuid:"+id, "99999999999", "preauthorization", planOfCare),
		"unknown":      submitAttachmentJSON("urn:uuid:nope", "12345678901", "preauthorization", planOfCare),
	} {
		if code, _ := pasPost(t, f.h, "/$submit-attachment", body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", name, code)
		}
	}
	for name, body := range map[string]string{
		"claim":      submitAttachmentJSON("urn:uuid:"+id, "12345678901", "claim", planOfCare),
		"wrong type": submitAttachmentJSON("urn:uuid:"+id, "12345678901", "preauthorization", `{"resourceType":"Patient"}`),
	} {
		if code, _ := pasPost(t, f.h, "/$submit-attachment", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
}
