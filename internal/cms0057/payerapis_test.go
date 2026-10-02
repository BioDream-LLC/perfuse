package cms0057

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var today = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func TestRedactForProviderRemovesCostSharing(t *testing.T) {
	res := convertFixture(t, "837p.x12", "835p.x12", CARINOptions{NetworkStatus: "innetwork", IdentifierSystem: testSystem})
	eob := eobOf(t, res[0])
	out := Redact(eob, ProviderAccess)
	raw, _ := json.Marshal(out)
	for _, gone := range []string{"memberliability", "paidtoprovider", "coinsurance", `"payment"`} {
		if strings.Contains(string(raw), gone) {
			t.Errorf("%s survived redaction for the Provider Access API", gone)
		}
	}
	for _, kept := range []string{"99213", "J02.9", "uniqueclaimid", "uc"} {
		if kept == "uniqueclaimid" {
			continue
		}
		if !strings.Contains(string(raw), kept) {
			t.Errorf("%s was removed, but it is clinical content the provider is entitled to", kept)
		}
	}
	if _, has := eob["payment"]; !has {
		t.Fatal("Redact modified its input")
	}
	if Redact(eob, PatientAccess)["payment"] == nil {
		t.Fatal("the member's own view lost the payment")
	}
}

func TestRedactDropsDeniedPriorAuthForPayers(t *testing.T) {
	denied := map[string]any{
		"resourceType": "ExplanationOfBenefit", "use": "preauthorization",
		"item": []any{map[string]any{"adjudication": []any{map[string]any{"extension": []any{map[string]any{
			"url": pdexBase + "extension-reviewAction",
			"extension": []any{map[string]any{"url": pdexBase + "extension-reviewActionCode",
				"valueCodeableConcept": map[string]any{"coding": []any{map[string]any{"code": "A3"}}}}},
		}}}}}},
	}
	if Redact(denied, PayerToPayer) != nil {
		t.Fatal("a denied prior authorisation was sent to another payer")
	}
	if Redact(denied, ProviderAccess) == nil {
		t.Fatal("the provider lost a denied prior authorisation, which the Provider Access API includes")
	}
}

func matchParams(family, birth, sub string) map[string]any {
	return map[string]any{"resourceType": "Parameters", "parameter": []any{
		map[string]any{"name": "MemberPatient", "resource": map[string]any{"resourceType": "Patient",
			"name": []any{map[string]any{"family": family, "given": []any{"Bravo"}}}, "birthDate": birth}},
		map[string]any{"name": "CoverageToMatch", "resource": map[string]any{"resourceType": "Coverage", "subscriberId": sub}},
	}}
}

func candidate(family, birth string) MatchCandidate {
	return MatchCandidate{
		Coverage: map[string]any{"status": "active", "subscriberId": "MBR123456"},
		Patient: map[string]any{"name": []any{map[string]any{"family": family, "given": []any{"Bravo"}}}, "birthDate": birth,
			"identifier": []any{map[string]any{"type": map[string]any{"coding": []any{map[string]any{"code": "MB"}}}, "value": "MBR123456"}}},
	}
}

func TestMemberMatch(t *testing.T) {
	req, err := ParseMatchRequest(matchParams("Sampleson", "1980-02-15", "MBR123456"))
	if err != nil {
		t.Fatal(err)
	}
	if sub, _ := req.MatchKeys(); sub != "MBR123456" {
		t.Fatalf("subscriber id %q", sub)
	}
	got, why := Match(req, []MatchCandidate{candidate("SAMPLESON", "1980-02-15"), candidate("Other", "1980-02-15")})
	if got == nil {
		t.Fatalf("no match: %s", why)
	}
	out := MatchResponse(got, "pt-1")
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "MemberIdentifier") || !strings.Contains(string(raw), "Patient/pt-1") {
		t.Fatalf("response: %s", raw)
	}

	if m, _ := Match(req, []MatchCandidate{candidate("Sampleson", "1980-02-16")}); m != nil {
		t.Fatal("matched on the identifier with the wrong birth date")
	}
	if m, why := Match(req, []MatchCandidate{candidate("Sampleson", "1980-02-15"), candidate("Sampleson", "1980-02-15")}); m != nil || !strings.Contains(why, "unique") {
		t.Fatalf("an ambiguous match returned someone: %v %s", m, why)
	}
}

func TestConsent(t *testing.T) {
	ok := map[string]any{"status": "active", "provision": map[string]any{"type": "permit", "period": map[string]any{"start": "2025-01-01", "end": "2027-01-01"}}}
	if err := CheckConsent(ok, today); err != nil {
		t.Fatal(err)
	}
	expired := map[string]any{"status": "active", "provision": map[string]any{"type": "permit", "period": map[string]any{"end": "2025-12-31"}}}
	if CheckConsent(expired, today) == nil {
		t.Fatal("an expired consent was accepted")
	}
	if CheckConsent(nil, today) == nil {
		t.Fatal("no consent was accepted")
	}
}

func TestGroupMembersAndOptOut(t *testing.T) {
	g := map[string]any{"member": []any{
		map[string]any{"entity": map[string]any{"reference": "Patient/a"}},
		map[string]any{"entity": map[string]any{"reference": "Patient/b"}, "inactive": true},
		map[string]any{"entity": map[string]any{"reference": "Patient/c"}, "period": map[string]any{"end": "2025-06-30"}},
		map[string]any{"entity": map[string]any{"reference": "Patient/a"}},
		map[string]any{"entity": map[string]any{"reference": "Practitioner/x"}},
	}}
	if got := GroupMembers(g, today); len(got) != 1 || got[0] != "a" {
		t.Fatalf("members: %v", got)
	}
	optOut := map[string]any{"status": "active",
		"category":  []any{map[string]any{"coding": []any{map[string]any{"code": "provider-access"}}}},
		"provision": map[string]any{"type": "deny", "period": map[string]any{"start": "2025-01-01"}}}
	if !ProviderOptedOut(optOut, today) {
		t.Fatal("an opt-out was not recognised")
	}
	optOut["provision"] = map[string]any{"type": "permit"}
	if ProviderOptedOut(optOut, today) {
		t.Fatal("a permit was read as an opt-out")
	}
}
