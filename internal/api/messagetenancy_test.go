package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/store"
)

// This file continues the cross-tenant attempts onto surfaces nothing had reached yet.
//
// It exists because the worst finding of the night - one tenant's administrator deleting another's - was in the place
// checked last, and was reachable by counting integers upwards. So the remaining endpoints that take an identifier or a
// name get the same attempt rather than the same assumption.
//
// Deliberately does not repeat messageisolation_test.go, which already covers listing, fetching by identifier, naming
// another tenant's channel, and the stats endpoint. What is new here is reprocessing, the queue, the metrics endpoints and
// the unauthenticated scrape path.

// recordOwned stores a message for a tenant and returns its identifier.
func recordOwned(t *testing.T, h *harness, tenantID, channel, controlID string) int64 {
	t.Helper()

	if h.server.Runtime == nil || h.server.Runtime.Messages == nil {
		t.Fatal("this harness has no message store, so nothing here is being tested; use newMessageHarness")
	}

	id, err := h.server.Runtime.Messages.Record(context.Background(), &msgstore.Message{
		Channel:    channel,
		TenantID:   tenantID,
		ReceivedAt: time.Now(),
		ControlID:  controlID,
		Outcome:    msgstore.Delivered,
		Size:       10,
		Raw:        []byte("MSH|^~\\&|A|B|C|D|20260821||ADT^A01|" + controlID + "|P|2.5.1\r"),
	})
	if err != nil {
		t.Fatal(err)
	}

	return id
}

// TestATenantCannotReprocessAnotherTenantsMessage covers a write nothing had attempted.
//
// Reprocessing sends a stored message through a channel again. Doing it to another tenant's message would push their
// patient data through their interfaces on somebody else's instruction, producing a duplicate record at the far end that
// originated outside the organisation entirely.
func TestATenantCannotReprocessAnotherTenantsMessage(t *testing.T) {
	h := newMessageHarness(t)

	id := recordOwned(t, h, "clinic-a", "a-feed", "REPROC-A")

	// The owner's own attempt first, as a baseline.
	//
	// This matters because both attempts get refused, and without the baseline this test would pass if the endpoint
	// were simply broken for everybody. The owner is refused with 409 - it found the message and the channel is not
	// running in this harness - while another tenant gets 404, having not found it at all.
	//
	// That difference is the assertion. It is the same trap that made the user-deletion test pass for the wrong
	// reason earlier tonight: a refusal is not evidence of isolation unless you know what refused.
	owner := h.doAs(t, "clinic-a", store.RoleEditor, http.MethodPost,
		"/api/messages/"+itoa(id)+"/reprocess", nil)
	if owner.Code == http.StatusNotFound {
		t.Fatalf("the owner cannot find its own message either, so this test proves nothing: %s",
			owner.Body.String())
	}

	rec := h.doAs(t, "clinic-b", store.RoleEditor, http.MethodPost,
		"/api/messages/"+itoa(id)+"/reprocess", nil)

	if rec.Code == http.StatusOK {
		t.Errorf("clinic-b reprocessed clinic-a's message: %s", rec.Body.String())
	}
	// Not found rather than forbidden: a forbidden would confirm the record exists.
	if rec.Code != http.StatusNotFound {
		t.Errorf("the refusal was %d rather than 404, so something other than the tenant filter refused it and "+
			"this test is not checking isolation: %s", rec.Code, rec.Body.String())
	}
}

// TestATenantCannotSeeAnotherTenantsQueue covers the queue view.
//
// A queue entry names a channel and a destination and holds the message waiting to go out, so it is both operational
// detail about another organisation and patient data.
func TestATenantCannotSeeAnotherTenantsQueue(t *testing.T) {
	h := newMessageHarness(t)

	recordOwned(t, h, "clinic-a", "a-distinctive-queue-feed", "QUEUE-A")

	rec := h.doAs(t, "clinic-b", store.RoleViewer, http.MethodGet, "/api/queue", nil)
	if rec.Code != http.StatusOK {
		return
	}
	if strings.Contains(rec.Body.String(), "a-distinctive-queue-feed") {
		t.Errorf("clinic-b can see clinic-a's queue:\n%s", rec.Body.String())
	}
}

// TestATenantCannotSeeAnotherTenantsMetrics covers operational disclosure.
//
// Volumes are commercially sensitive for a managed service. How many messages another hospital moves, and through which
// interfaces, describes their operation - and a channel name frequently names the system at the other end.
func TestATenantCannotSeeAnotherTenantsMetrics(t *testing.T) {
	h := newMessageHarness(t)

	recordOwned(t, h, "clinic-a", "a-distinctive-metric-feed", "METRIC-A")

	for _, path := range []string{"/api/metrics", "/api/metrics/names", "/api/dashboard"} {
		rec := h.doAs(t, "clinic-b", store.RoleViewer, http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			// A path that does not exist in this configuration is not a finding.
			continue
		}
		if strings.Contains(rec.Body.String(), "a-distinctive-metric-feed") {
			t.Errorf("%s showed clinic-b the name of clinic-a's channel:\n%s", path, rec.Body.String())
		}
	}
}

// TestThePrometheusEndpointOnAMultiTenantServer insists the scrape endpoint is not anonymous.
//
// /metrics is unauthenticated by design, for a scraper that has no credentials to give. That is defensible on a single
// installation - counts of one operator's own traffic - and is a different proposition on a shared server, where a label
// naming a channel tells one customer about another, and tells anybody who can reach the port about both.
//
// Logged rather than failed, because the right answer is a design decision - one scrape endpoint per tenant, labels without
// channel names, or authentication on the scrape path - and not something to pick silently at four in the morning. The test
// exists so the question is written down somewhere it will be seen again.
func TestThePrometheusEndpointOnAMultiTenantServer(t *testing.T) {
	h := newMessageHarness(t)

	recordOwned(t, h, "clinic-a", "a-scraped-feed", "SCRAPE-A")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Logf("the scrape endpoint returned %d, so there is nothing to decide yet", rec.Code)
		return
	}

	// Answered: the endpoint now requires a credential, and a tenant's own operator sees only their own series. The
	// assertions live in metricsauth_test.go. Kept here as the anonymous case, which must simply be refused.
	t.Errorf("the scrape endpoint served an anonymous caller: %d", rec.Code)
}
