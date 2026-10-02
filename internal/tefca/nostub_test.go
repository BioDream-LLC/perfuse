package tefca

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Guards against an exchange function that reports success without exchanging anything.
//
// # What happened
//
// All four exchange patterns validated their inputs, made no network call, and returned success. Deliver returned Accepted with a
// tracking identifier built from the clock. The audit layer above then recorded each one as a completed exchange, in a persistent log
// whose only purpose is to prove what was exchanged when somebody asks months later - prompted, typically, by a complaint or an
// investigation.
//
// Four tests asserted that behaviour and passed. They were named TestQuery_Success, TestDeliver_Success, TestRetrieve_Success and
// TestNotify_Success, and each one defended the defect rather than catching it. That is the third time that shape has appeared in this
// codebase, and the first time it appeared in a feature whose claim is made to hospitals about national data exchange.
//
// # Why a source-reading test
//
// Because the defect is invisible to a behavioural test. A stub that returns success is indistinguishable from a working
// implementation without a network to check against, and there is no QHIN to point a test at. So this reads the source for the shape:
// an exchange function that neither performs a network operation nor refuses.
//
// It is deliberately narrow. It does not try to judge whether an implementation is correct - only whether it is there at all.

// exchangeFunctions are the four patterns that must either exchange or refuse.
var exchangeFunctions = []string{"Query", "Deliver", "Retrieve", "Notify"}

func TestNoExchangeFunctionReportsSuccessWithoutATransport(t *testing.T) {
	source, err := os.ReadFile("tefca.go")
	if err != nil {
		t.Fatal(err)
	}

	body := string(source)

	// A positive control first. Without it, a rename of the functions would make every check below vacuous and this test would pass
	// having examined nothing - which is the same failure it exists to catch.
	for _, name := range exchangeFunctions {
		if !strings.Contains(body, "func "+name+"(ctx context.Context") {
			t.Fatalf("%s is not in tefca.go with the expected signature, so this test is not reading what it thinks it is", name)
		}
	}

	transport := regexp.MustCompile(`http\.|\.Do\(|net\.Dial|tls\.Dial`)

	for _, name := range exchangeFunctions {
		t.Run(name, func(t *testing.T) {
			fn := functionBody(t, body, name)

			refuses := strings.Contains(fn, "ErrExchangeNotImplemented")
			exchanges := transport.MatchString(fn)

			if !refuses && !exchanges {
				t.Errorf("%s neither performs a network operation nor returns ErrExchangeNotImplemented, so it reports an "+
					"exchange that did not happen.\n\nThat is the defect this file exists to prevent: the audit trail above "+
					"records it as completed, and an audit trail that disagrees with reality is worse than no trail because "+
					"the trail is what gets believed.", name)
			}
			if refuses && exchanges {
				// Not a failure, but worth saying: once a transport exists, the refusal should go, and a function doing both is
				// halfway through that change.
				t.Logf("%s both refuses and performs a network operation, which is expected only while a transport is being "+
					"added", name)
			}
		})
	}
}

func TestEveryExchangeRefusalNamesWhatIsMissing(t *testing.T) {
	// A refusal that says only "not implemented" leaves an operator unable to decide whether to abandon the feature or wait for it. The
	// message has to say what is missing and what still works, because the audit trail and purpose-of-use checking are real and are the
	// reason somebody would keep this configured.
	text := ErrExchangeNotImplemented.Error()

	for _, want := range []string{"QHIN", "audit trail", "not implemented"} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal does not mention %q: %s", want, text)
		}
	}
}

func TestAnAttemptedExchangeIsNeverAuditedAsSuccessful(t *testing.T) {
	// The behavioural half, which is what actually protects the audit trail. Every pattern is attempted through its auditing wrapper and
	// every resulting entry must record a failure with a reason.
	cfg := validConfig()
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		run  func(*AuditLog) error
	}{
		{"query", func(l *AuditLog) error {
			_, err := QueryWithAudit(ctx, cfg, QueryRequest{PatientID: "P1", Purpose: PurposeTreatment}, l)

			return err
		}},
		{"delivery", func(l *AuditLog) error {
			_, err := DeliverWithAudit(ctx, cfg, DeliveryRequest{
				Recipient: "urn:oid:2.16.840.1.113883.19.9",
				Purpose:   PurposeTreatment,
				Bundle:    []byte(`{"resourceType":"Bundle"}`),
			}, l)

			return err
		}},
		{"retrieval", func(l *AuditLog) error {
			_, err := RetrieveWithAudit(ctx, cfg, RetrievalRequest{
				DocumentRef: "urn:oid:1.2.3.4.5",
				PatientID:   "P1",
				Purpose:     PurposeTreatment,
			}, l)

			return err
		}},
		{"notification", func(l *AuditLog) error {
			return NotifyWithAudit(ctx, cfg, notificationForTest(), l)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &AuditLog{}

			err := tc.run(log)
			if !errors.Is(err, ErrExchangeNotImplemented) {
				t.Fatalf("got %v, want ErrExchangeNotImplemented", err)
			}

			entries := log.Query(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
			if len(entries) != 1 {
				t.Fatalf("got %d audit entries, want exactly 1 - an attempt has to be recorded whether or not it worked",
					len(entries))
			}

			e := entries[0]
			if e.Success {
				t.Error("an exchange that made no network call was audited as a success")
			}
			if e.ErrorDetail == "" {
				t.Error("the entry gives no reason, so the trail says only that something did not work")
			}
			if !strings.Contains(e.ErrorDetail, "QHIN") {
				t.Errorf("the recorded reason does not say what was missing: %q", e.ErrorDetail)
			}
		})
	}
}

// functionBody returns the source of one top-level function.
func functionBody(t *testing.T, source, name string) string {
	t.Helper()

	start := strings.Index(source, "func "+name+"(ctx context.Context")
	if start < 0 {
		t.Fatalf("%s not found", name)
	}

	rest := source[start:]
	if end := strings.Index(rest, "\nfunc "); end > 0 {
		rest = rest[:end]
	}

	return rest
}
