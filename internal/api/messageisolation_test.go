package api

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/store"
	_ "modernc.org/sqlite"
)

// These tests are about stored messages rather than channel files. Channel isolation was done earlier and covered the
// definitions; messages are patient data, and a leak here is a different order of problem.

func recordFor(t *testing.T, h *harness, tenantID, channel, controlID string) {
	t.Helper()

	// No skip here, deliberately. An earlier version of this file skipped when the harness had no message store, and
	// every isolation test in it silently passed by not running - which is how a cross-tenant leak survived being
	// "tested". A missing store is now a failure, because a test that cannot exercise the thing it names is worse
	// than no test.
	if h.server.Runtime == nil || h.server.Runtime.Messages == nil {
		t.Fatal("this harness has no message store, so nothing here is being tested; use newMessageHarness")
	}

	_, err := h.server.Runtime.Messages.Record(context.Background(), &msgstore.Message{
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
}

// newMessageHarness is newHarness with a real message store and multi-tenant repositories.
//
// Both are needed for these tests to mean anything: without the store there is nothing to leak, and without
// multi-tenant repositories every session reports the same tenant and the filter cannot be wrong.
func newMessageHarness(t *testing.T) *harness {
	t.Helper()

	h := newHarness(t)

	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/m.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	messages, err := msgstore.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}

	repos, err := NewTenantRepos(h.dir)
	if err != nil {
		t.Fatal(err)
	}

	h.server.Repos = repos
	h.server.Runtime = NewRuntime(h.server.Channels, messages, nil)
	h.server.Runtimes = NewTenantRuntimes(repos, messages, nil, nil, h.server.Log)
	h.handler = h.server.Handler()

	return h
}

func TestOneTenantCannotListAnothersMessages(t *testing.T) {
	// The message list took its channel from a query parameter and filtered by nothing else, so naming another
	// tenant's channel returned their messages - and omitting the filter returned everybody's. Those messages are
	// patient data.
	h := newMessageHarness(t)

	recordFor(t, h, "clinic-a", "a-feed", "A001")
	recordFor(t, h, "clinic-b", "b-feed", "B001")

	rec := h.doAs(t, "clinic-a", store.RoleAdmin, http.MethodGet, "/api/messages", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing messages returned %d", rec.Code)
	}

	body := decodeBody[struct {
		Messages []struct {
			Channel   string `json:"channel"`
			ControlID string `json:"controlId"`
		} `json:"messages"`
		Total int `json:"total"`
	}](t, rec)

	for _, m := range body.Messages {
		if m.Channel == "b-feed" || m.ControlID == "B001" {
			t.Errorf("clinic-a can see clinic-b's message %s on channel %s", m.ControlID, m.Channel)
		}
	}
	if body.Total > 1 {
		t.Errorf("clinic-a sees a total of %d messages when only one is theirs", body.Total)
	}
}

func TestNamingAnotherTenantsChannelReturnsNothing(t *testing.T) {
	// The direct attempt: ask for their channel by name. It must come back empty rather than forbidden, because
	// telling somebody a channel exists in another tenant is itself a disclosure.
	h := newMessageHarness(t)

	recordFor(t, h, "clinic-a", "a-feed", "A001")
	recordFor(t, h, "clinic-b", "b-feed", "B001")

	rec := h.doAs(t, "clinic-a", store.RoleAdmin, http.MethodGet, "/api/messages?channel=b-feed", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("returned %d", rec.Code)
	}

	body := decodeBody[struct {
		Messages []struct {
			ControlID string `json:"controlId"`
		} `json:"messages"`
	}](t, rec)

	if len(body.Messages) != 0 {
		t.Errorf("naming another tenant's channel returned %d messages", len(body.Messages))
	}
}

func TestOneTenantCannotFetchAnothersMessageByID(t *testing.T) {
	// Identifiers are sequential, so guessing one is not an attack requiring skill.
	h := newMessageHarness(t)

	recordFor(t, h, "clinic-a", "a-feed", "A001")

	id, err := h.server.Runtime.Messages.Record(context.Background(), &msgstore.Message{
		Channel:    "b-feed",
		TenantID:   "clinic-b",
		ReceivedAt: time.Now(),
		ControlID:  "B001",
		Outcome:    msgstore.Delivered,
		Size:       10,
		Raw:        []byte("MSH|secret\r"),
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := h.doAs(t, "clinic-a", store.RoleAdmin, http.MethodGet,
		"/api/messages/"+itoa(id), nil)

	// 404 rather than 403, for the same reason as above: a forbidden tells the caller the record exists.
	if rec.Code != http.StatusNotFound {
		t.Errorf("fetching another tenant's message returned %d, want 404", rec.Code)
	}
}

func TestTheStatsEndpointCountsOnlyOnesOwnMessages(t *testing.T) {
	// A count is a disclosure too. "You have 40,000 messages today" when four of them are yours tells a tenant
	// something about the platform's other customers.
	h := newMessageHarness(t)

	recordFor(t, h, "clinic-a", "a-feed", "A001")
	for i := 0; i < 5; i++ {
		recordFor(t, h, "clinic-b", "b-feed", "B00"+itoa(int64(i)))
	}

	rec := h.doAs(t, "clinic-a", store.RoleAdmin, http.MethodGet, "/api/stats", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats returned %d", rec.Code)
	}

	body := decodeBody[struct {
		Total int `json:"total"`
	}](t, rec)

	if body.Total > 1 {
		t.Errorf("clinic-a's stats report %d messages when one is theirs", body.Total)
	}
}
