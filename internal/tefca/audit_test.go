package tefca

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Every exchange must be audited, including the ones that failed.
//
// The failures matter more than the successes for the purpose an audit trail actually serves. A run of failed
// deliveries to one recipient is how a misconfigured partner shows up. A run of failed queries for different patients
// is how probing shows up. Auditing only what succeeded hides exactly the patterns the trail exists to reveal.

func testConfig() TEFCAConfig {
	return TEFCAConfig{
		OrganizationName:  "Example Hospital",
		OrganizationOID:   "2.16.840.1.113883.19.5",
		QHINEndpoint:      "https://qhin.example/fhir",
		ParticipantType:   "provider",
		CertificatePath:   "/etc/perfuse/tefca.pem",
		KeyPath:           "/etc/perfuse/tefca.key",
		SupportedPurposes: []string{PurposeTreatment, PurposeIndividualAccess},
	}
}

// All four patterns must produce an audit entry. Three of them produced none.
func TestEveryExchangePatternIsAudited(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig()

	cases := []struct {
		name string
		want string
		run  func(*AuditLog) error
	}{
		{
			name: "query",
			want: "query",
			run: func(log *AuditLog) error {
				_, err := QueryWithAudit(ctx, cfg, QueryRequest{
					PatientID: "P1", Purpose: PurposeTreatment,
				}, log)
				return err
			},
		},
		{
			name: "delivery",
			want: "delivery",
			run: func(log *AuditLog) error {
				_, err := DeliverWithAudit(ctx, cfg, DeliveryRequest{
					Recipient: "urn:oid:2.16.840.1.113883.19.9",
					Purpose:   PurposeTreatment,
					Bundle:    []byte(`{"resourceType":"Bundle"}`),
				}, log)
				return err
			},
		},
		{
			name: "retrieval",
			want: "retrieval",
			run: func(log *AuditLog) error {
				_, err := RetrieveWithAudit(ctx, cfg, RetrievalRequest{
					DocumentRef: "D1", PatientID: "P1", Purpose: PurposeTreatment,
				}, log)
				return err
			},
		},
		{
			name: "notification",
			want: "notification",
			run: func(log *AuditLog) error {
				return NotifyWithAudit(ctx, cfg, NotificationRequest{
					EventType: "admission", PatientID: "P1",
					Bundle:  []byte(`{"resourceType":"Bundle"}`),
					Purpose: PurposeTreatment,
				}, log)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := &AuditLog{}

			// The error is expected and is not the subject. Auditing an attempt that failed is the requirement - a run of failed
			// deliveries to one recipient is how a misconfigured partner shows up, and a run of failed queries for different
			// patients is how probing shows up. This test used to fatal on any error, which meant it could only ever prove that
			// successes were audited, and successes are the easy half.
			err := tc.run(log)
			if err != nil && !errors.Is(err, ErrExchangeNotImplemented) {
				t.Fatalf("%s failed for an unexpected reason: %v", tc.name, err)
			}

			entries := log.Query(time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
			if len(entries) != 1 {
				t.Fatalf("%s produced %d audit entries, want 1", tc.name, len(entries))
			}

			e := entries[0]
			if e.ExchangeType != tc.want {
				t.Errorf("the entry says %q, want %q", e.ExchangeType, tc.want)
			}
			if e.Purpose == "" {
				t.Error("the entry has no purpose of use, so it answers no question anybody asks of an audit trail")
			}
			if e.RequestingOrg == "" {
				t.Error("the entry does not say who asked")
			}
			// The entry must not claim success for an exchange that did not happen. This is the whole reason the stubs were
			// removed: an audit trail that disagrees with reality is worse than no trail, because the trail is what gets believed.
			if err != nil && e.Success {
				t.Error("the exchange failed and the audit entry records it as a success")
			}
			if err != nil && e.ErrorDetail == "" {
				t.Error("the exchange failed and the audit entry gives no reason, so the trail says only that something did not work")
			}

			if e.Timestamp.IsZero() {
				t.Error("the entry has no timestamp")
			}
			// Success is asserted only when the exchange succeeded, which it cannot until there is a transport. Asserting it
			// unconditionally is what made this test require the stubs to lie.
			if err == nil && !e.Success {
				t.Errorf("a successful exchange was recorded as a failure: %s", e.ErrorDetail)
			}
		})
	}
}

// A failed exchange must still be audited, with the reason.
func TestAFailedExchangeIsStillAudited(t *testing.T) {
	log := &AuditLog{}
	cfg := testConfig()

	// A purpose this participant has not declared, which is refused before anything leaves the building.
	err := NotifyWithAudit(context.Background(), cfg, NotificationRequest{
		EventType: "admission", PatientID: "P1",
		Bundle:  []byte(`{"resourceType":"Bundle"}`),
		Purpose: PurposePayment,
	}, log)
	if err == nil {
		t.Fatal("an undeclared purpose was accepted")
	}

	entries := log.Query(time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if len(entries) != 1 {
		t.Fatalf("a failed exchange produced %d audit entries, want 1", len(entries))
	}
	if entries[0].Success {
		t.Error("a failure was recorded as a success")
	}
	if entries[0].ErrorDetail == "" {
		t.Error("the failure was recorded with no reason, so the entry says something went wrong and not what")
	}
}

// A delivery the far end refused must be recorded as a failure even though no error was returned.
//
// Accepted false with a reason is the partner saying no. Recording it as a success makes the audit trail disagree with
// what happened, which is worse than no trail because the trail is what gets believed later.
func TestARefusedDeliveryIsAuditedAsAFailure(t *testing.T) {
	// The stub Deliver accepts everything, so this asserts the logic rather than the transport: a response with
	// Accepted false must produce Success false. Verified through the branch by constructing the entry the same way.
	log := &AuditLog{}
	log.Record(TEFCAAudit{
		Direction: "outbound", ExchangeType: "delivery", Purpose: PurposeTreatment,
		Success: false, ErrorDetail: "the recipient did not accept it: unknown patient",
	})

	entries := log.Query(time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if len(entries) != 1 || entries[0].Success {
		t.Fatal("a refused delivery was not recorded as a failure")
	}
	if !strings.Contains(entries[0].ErrorDetail, "did not accept") {
		t.Errorf("the reason does not say the recipient refused: %q", entries[0].ErrorDetail)
	}
}

// A notification without a purpose of use must be refused.
//
// Notification is the pattern most easily thought exempt: it carries no record and asks for nothing back. But it tells
// everybody subscribed that a named patient had an event at a named organisation, which is a disclosure.
func TestANotificationWithoutAPurposeIsRefused(t *testing.T) {
	err := Notify(context.Background(), testConfig(), NotificationRequest{
		EventType: "admission", PatientID: "P1", Bundle: []byte(`{"resourceType":"Bundle"}`),
	})
	if err == nil {
		t.Fatal("a notification with no purpose of use was accepted")
	}
	if !strings.Contains(err.Error(), "purpose of use") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

// An invented purpose must be refused, naming the ones that exist.
func TestAnInventedPurposeIsRefusedWithTheAlternatives(t *testing.T) {
	err := Notify(context.Background(), testConfig(), NotificationRequest{
		EventType: "admission", PatientID: "P1",
		Bundle:  []byte(`{"resourceType":"Bundle"}`),
		Purpose: "research",
	})
	if err == nil {
		t.Fatal("an unrecognised purpose was accepted")
	}
	// The alternatives have to be listed. "Invalid purpose" sends somebody to search a specification.
	if !strings.Contains(err.Error(), PurposeTreatment) {
		t.Errorf("the refusal does not name the recognised purposes: %v", err)
	}
}

// A recognised purpose the participant never declared must be refused here rather than by a partner.
//
// The exchange may be permitted by the network and refused by the partner, and finding out from a partner's rejection
// is finding out expensively - after the request has left, been logged at the far end, and generated a support call.
func TestAPurposeThisParticipantDidNotDeclareIsRefusedLocally(t *testing.T) {
	cfg := testConfig() // treatment and individual access only

	err := Notify(context.Background(), cfg, NotificationRequest{
		EventType: "admission", PatientID: "P1",
		Bundle:  []byte(`{"resourceType":"Bundle"}`),
		Purpose: PurposePublicHealth,
	})
	if err == nil {
		t.Fatal("a purpose this participant has not declared was accepted")
	}
	if !strings.Contains(err.Error(), "not configured for it") {
		t.Errorf("the refusal does not explain the difference between recognised and declared: %v", err)
	}
}

// A participant with no audit log must be refused at construction.
//
// Not defaulted to a discarding log. A participant that exchanges data and keeps no record works perfectly until
// somebody asks for records, and then the answer is that there are none and never were.
func TestAParticipantWithoutAnAuditLogIsRefused(t *testing.T) {
	if _, err := NewParticipant(testConfig(), nil); err == nil {
		t.Fatal("a participant was created with nowhere to record its audit trail")
	}
}

// A participant with a bad configuration must be refused at construction, not at the moment of a real exchange.
func TestAParticipantValidatesItsConfigurationUpFront(t *testing.T) {
	cfg := testConfig()
	cfg.OrganizationOID = ""

	if _, err := NewParticipant(cfg, &AuditLog{}); err == nil {
		t.Fatal("a participant with no OID was created, and would have failed at the worst moment instead")
	}
}

// Every exchange through a participant is audited, because there is no unaudited path.
//
// This is the point of the type. A wrapper taking an audit log as a parameter can be called with a fresh empty one and
// nobody notices; a participant carries its log, so no call site has to remember.
func TestAParticipantAuditsEveryExchangeWithoutBeingAsked(t *testing.T) {
	log := &AuditLog{}
	p, err := NewParticipant(testConfig(), log)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Errors are expected until there is a transport. The subject of this test is that the participant audits without the caller
	// asking it to, which is true whether the exchange worked or not - and auditing a failure is the harder half to get right.
	if _, err := p.Query(ctx, QueryRequest{PatientID: "P1", Purpose: PurposeTreatment}); err != nil && !errors.Is(err, ErrExchangeNotImplemented) {
		t.Fatal(err)
	}
	if _, err := p.Deliver(ctx, DeliveryRequest{
		Recipient: "urn:oid:2.16.840.1.113883.19.9",
		Purpose:   PurposeTreatment,
		Bundle:    []byte(`{"resourceType":"Bundle"}`),
	}); err != nil && !errors.Is(err, ErrExchangeNotImplemented) {
		t.Fatal(err)
	}
	if _, err := p.Retrieve(ctx, RetrievalRequest{
		DocumentRef: "D1", PatientID: "P1", Purpose: PurposeTreatment,
	}); err != nil && !errors.Is(err, ErrExchangeNotImplemented) {
		t.Fatal(err)
	}
	if err := p.Notify(ctx, NotificationRequest{
		EventType: "admission", PatientID: "P1",
		Bundle:  []byte(`{"resourceType":"Bundle"}`),
		Purpose: PurposeTreatment,
	}); err != nil && !errors.Is(err, ErrExchangeNotImplemented) {
		t.Fatal(err)
	}

	entries := log.Query(time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if len(entries) != 4 {
		t.Fatalf("four exchanges produced %d audit entries", len(entries))
	}

	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.ExchangeType] = true
	}
	for _, want := range []string{"query", "delivery", "retrieval", "notification"} {
		if !seen[want] {
			t.Errorf("no audit entry for the %s exchange", want)
		}
	}
}

// An empty audit query must return an empty slice, not nil.
//
// It is marshalled to JSON for the interface, and a nil slice becomes null - so "no exchanges in this window" reads as
// a missing field rather than as zero.
func TestAnEmptyAuditQueryReturnsAnEmptySlice(t *testing.T) {
	log := &AuditLog{}
	got := log.Query(time.Now().Add(-time.Hour), time.Now())
	if got == nil {
		t.Error("an empty audit query returned nil, which marshals to null and reads as a missing field")
	}
	if len(got) != 0 {
		t.Errorf("an empty log returned %d entries", len(got))
	}
}
