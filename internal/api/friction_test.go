package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The friction recorder and report.
//
// This is what replaced a queue item asking for one real operator to be handed the binary and watched. That item stood at the top of
// the queue for three days and could never be done, because the only parties who have run this software are the person who commissioned
// it and the program that wrote it.
//
// Its purpose survives. Every claim here about ease of use rests on this author's judgement of his own work - the same circularity that
// let a thousand self-agreeing SAML tests pass while no real identity provider could sign anybody in. A refusal is evidence from
// outside that loop: somebody wanted something, the server said no, and neither was guessing.

func TestARefusalIsRecorded(t *testing.T) {
	// The whole mechanism in one test: a request that gets refused leaves a record naming what was refused.
	h := newHarness(t)
	srv := h.server

	req := httptest.NewRequest(http.MethodGet, "/api/channels/nope-does-not-exist", nil)
	rec := httptest.NewRecorder()

	srv.fail(rec, req, http.StatusNotFound, "no channel called that")

	groups, err := srv.Store.TopFriction(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(groups) != 1 {
		t.Fatalf("recorded %d groups, want 1", len(groups))
	}

	if groups[0].Message != "no channel called that" {
		t.Errorf("message is %q, want the server's own sentence", groups[0].Message)
	}

	if groups[0].Status != http.StatusNotFound {
		t.Errorf("status is %d, want 404", groups[0].Status)
	}
}

func TestRefusalsOnDifferentChannelsGroupTogether(t *testing.T) {
	// Without this the report is accurate and useless: every refusal on a different channel would be its own finding, nothing would
	// ever reach a count above one, and there would be no way to see which message an operator actually keeps hitting.
	h := newHarness(t)
	srv := h.server

	for _, path := range []string{
		"/api/channels/adt-from-the-lab/start",
		"/api/channels/orders-to-pharmacy/start",
		"/api/channels/results-inbound-v2/start",
	} {
		srv.fail(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil),
			http.StatusConflict, "that channel is already running")
	}

	groups, err := srv.Store.TopFriction(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(groups) != 1 {
		names := make([]string, 0, len(groups))
		for _, g := range groups {
			names = append(names, g.Route)
		}

		t.Fatalf("three refusals on three channels produced %d groups (%v), want 1", len(groups), names)
	}

	if groups[0].Count != 3 {
		t.Errorf("count is %d, want 3", groups[0].Count)
	}

	if !strings.Contains(groups[0].Route, "{id}") {
		t.Errorf("route is %q, so the channel name was not replaced and nothing will ever group", groups[0].Route)
	}
}

func TestAServerFaultIsNotRecordedAsFriction(t *testing.T) {
	// A 500 is a defect in this software, logged as one already. Mixing the two would bury the cases where somebody could not work out
	// what to type under the cases where nothing they typed would have helped.
	h := newHarness(t)
	srv := h.server

	srv.fail(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/channels", nil),
		http.StatusInternalServerError, "something broke in here")

	n, err := srv.Store.FrictionCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if n != 0 {
		t.Errorf("recorded %d refusals for a server fault, want 0", n)
	}
}

func TestAnExpiredSessionIsNotRecordedAsFriction(t *testing.T) {
	// A browser left open overnight produces a run of 401s that say nothing about usability. At volume they would drown every finding
	// that does, which is the only reason this exclusion exists.
	h := newHarness(t)
	srv := h.server

	srv.fail(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/channels", nil),
		http.StatusUnauthorized, "session has expired")

	n, err := srv.Store.FrictionCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if n != 0 {
		t.Errorf("recorded %d refusals for an expired session, want 0", n)
	}
}

func TestTheRecordHoldsNothingTheOperatorTyped(t *testing.T) {
	// The privacy rule, asserted rather than trusted to a comment.
	//
	// A validation message names a field and a rule, which is the useful part. The value that broke the rule is very often patient
	// data, and a table of those would be a worse liability than the friction it measured.
	h := newHarness(t)
	srv := h.server

	const patient = "SMITH^MARGARET^J"

	req := httptest.NewRequest(http.MethodPost, "/api/channels/adt/testmessages?name="+patient, nil)
	srv.fail(httptest.NewRecorder(), req, http.StatusBadRequest, "that field has to be a number")

	rows, err := srv.Store.TopFriction(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(rows) == 0 {
		t.Fatal("nothing was recorded")
	}

	blob, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(blob), "SMITH") || strings.Contains(string(blob), "MARGARET") {
		t.Errorf("the record contains something the operator typed: %s", blob)
	}
}

func TestTheFunnelNamesWhereAnInstallationIsStuck(t *testing.T) {
	// The report has to say where somebody stopped rather than leave four timestamps for a reader to compare. A report that needs
	// interpreting gets interpreted by whoever wrote the software, which is the circularity this is meant to escape.
	for _, tc := range []struct {
		name   string
		funnel FrictionFunnel
		want   string
	}{
		{"nothing at all", FrictionFunnel{}, "nobody has signed in"},
		{
			"signed in only",
			FrictionFunnel{FirstSignIn: aTime(t, "2026-09-18T08:00:00Z")},
			"no channel has ever been created",
		},
		{
			"a channel was made and is gone",
			FrictionFunnel{
				FirstSignIn:  aTime(t, "2026-09-18T08:00:00Z"),
				FirstChannel: aTime(t, "2026-09-18T08:05:00Z"),
				ChannelCount: 0,
			},
			"deleted or would not load",
		},
		{
			"a channel exists and is silent",
			FrictionFunnel{
				FirstSignIn:  aTime(t, "2026-09-18T08:00:00Z"),
				FirstChannel: aTime(t, "2026-09-18T08:05:00Z"),
				ChannelCount: 1,
			},
			"no message has ever arrived",
		},
		{
			// The state worth catching most. Traffic arriving looks like success from the interface, and a channel that receives for
			// weeks and delivers nothing is the failure this whole repository keeps finding in other people's software.
			"messages arrive and none leaves",
			FrictionFunnel{
				FirstSignIn:  aTime(t, "2026-09-18T08:00:00Z"),
				FirstChannel: aTime(t, "2026-09-18T08:05:00Z"),
				ChannelCount: 1,
				FirstMessage: aTime(t, "2026-09-18T08:10:00Z"),
				MessageCount: 40,
				Delivered:    0,
			},
			"none has been delivered",
		},
		{
			"working",
			FrictionFunnel{
				FirstSignIn:  aTime(t, "2026-09-18T08:00:00Z"),
				FirstChannel: aTime(t, "2026-09-18T08:05:00Z"),
				ChannelCount: 1,
				FirstMessage: aTime(t, "2026-09-18T08:10:00Z"),
				MessageCount: 40,
				Delivered:    40,
			},
			"received and delivered",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stuckAt(tc.funnel)
			if !strings.Contains(got, tc.want) {
				t.Errorf("stuckAt says %q, want something containing %q", got, tc.want)
			}
		})
	}
}

func TestTheReportIsAdminOnly(t *testing.T) {
	// It says where this installation got stuck and how often the product refused somebody. That is operational self-criticism rather
	// than something a viewer needs, and an installation's difficulties are not neutral information about it.
	h := newHarness(t)

	if got := h.do("viewer", http.MethodGet, "/api/friction", nil).Code; got != http.StatusForbidden {
		t.Errorf("a viewer got %d from the friction report, want 403", got)
	}

	if got := h.do("editor", http.MethodGet, "/api/friction", nil).Code; got != http.StatusForbidden {
		t.Errorf("an editor got %d from the friction report, want 403", got)
	}

	res := h.do("admin", http.MethodGet, "/api/friction", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("an admin got %d from the friction report, want 200: %s", res.Code, res.Body.String())
	}

	var report FrictionReport
	if err := json.Unmarshal(res.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}

	if report.Refusals == nil {
		t.Error("refusals came back as null rather than an empty list, so a caller has two shapes of nothing to handle")
	}

	if report.Funnel.StuckAt == "" {
		t.Error("the report does not say where the installation is stuck, which is the only thing the funnel is for")
	}
}

// aTime is a pointer to a parsed instant, which the funnel fields need.
func aTime(t *testing.T, iso string) *time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatal(err)
	}

	return &parsed
}
