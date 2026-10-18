package api

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/mpi"
	"github.com/biodream-llc/perfuse/internal/sqlitedb"
	"github.com/biodream-llc/perfuse/internal/store"
)

func TestPatientIndexEndpoints(t *testing.T) {
	h := newHarness(t)
	if res := h.do(string(store.RoleViewer), "GET", "/api/mpi", nil); res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"enabled":false`) {
		t.Fatalf("without an index: %d %s", res.Code, res.Body.String())
	}
	pool, err := sqlitedb.Open(filepath.Join(t.TempDir(), "mpi.db"), sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	idx, err := mpi.Open(context.Background(), pool.Write)
	if err != nil {
		t.Fatal(err)
	}
	h.server.PatientIndex = idx
	h.handler = h.server.Handler()

	reg := func(body map[string]any) mpi.Outcome {
		res := h.do(string(store.RoleEditor), "POST", "/api/mpi/records", body)
		if res.Code != http.StatusOK {
			t.Fatalf("register: %d %s", res.Code, res.Body.String())
		}
		return decodeBody[mpi.Outcome](t, res)
	}
	// Automatic linking is off until an administrator turns it on.
	if res := h.do(string(store.RoleEditor), "PUT", "/api/mpi/settings", map[string]any{"automatic": true}); res.Code != http.StatusForbidden {
		t.Fatalf("an editor turned on automatic linking: %d", res.Code)
	}
	if res := h.do(string(store.RoleAdmin), "PUT", "/api/mpi/settings", map[string]any{"automatic": true}); res.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", res.Code, res.Body.String())
	}
	a := reg(map[string]any{"system": "urn:oid:1.1", "value": "A1", "family": "Silva", "given": "Ana", "birthDate": "1988-02-29", "sex": "F"})
	b := reg(map[string]any{"system": "urn:oid:1.2", "value": "B1", "family": "Silva", "given": "Ana", "birthDate": "1988-02-29", "sex": "F", "postal": "1000"})
	c := reg(map[string]any{"system": "urn:oid:1.2", "value": "B2", "family": "Silva", "given": "Anna", "birthDate": "1988-02-29", "sex": "M"})
	if a.Action != "new" || b.Action != "linked" || c.Action != "review" {
		t.Fatalf("%s %s %s", a.Action, b.Action, c.Action)
	}

	// A viewer reads; reading is audited.
	sum := decodeBody[mpiSummary](t, h.do(string(store.RoleViewer), "GET", "/api/mpi?q=silva", nil))
	if !sum.Enabled || sum.Counts.Records != 3 || sum.Counts.Reviews != 1 || len(sum.Persons) != 2 {
		t.Errorf("summary %+v", sum.Counts)
	}
	if res := h.do(string(store.RoleViewer), "GET", "/api/mpi/reviews", nil); res.Code != http.StatusForbidden {
		t.Errorf("a viewer read the review queue: %d", res.Code)
	}
	view := decodeBody[mpiPersonView](t, h.do(string(store.RoleViewer), "GET", "/api/mpi/persons/"+a.Record.EUID, nil))
	if len(view.Person.Records) != 2 || view.IDs[0] != "urn:perfuse:mpi|"+a.Record.EUID {
		t.Errorf("person %+v", view)
	}

	// The reviewer decides the possible match is the same person.
	if res := h.do(string(store.RoleEditor), "POST", "/api/mpi/reviews/"+itoa64(c.ReviewID), map[string]any{"same": true}); res.Code != http.StatusOK {
		t.Fatalf("decide: %d %s", res.Code, res.Body.String())
	}
	view = decodeBody[mpiPersonView](t, h.do(string(store.RoleViewer), "GET", "/api/mpi/persons/"+a.Record.EUID, nil))
	if len(view.Person.Records) != 3 {
		t.Errorf("after the review %d records", len(view.Person.Records))
	}
	// Unlinked again, by hand.
	res := h.do(string(store.RoleEditor), "POST", "/api/mpi/records/"+itoa64(c.Record.ID)+"/unlink", map[string]any{"why": "a brother"})
	if res.Code != http.StatusOK {
		t.Fatalf("unlink: %d %s", res.Code, res.Body.String())
	}

	// PIXm answers from the index: the other systems' numbers and the enterprise identifier.
	res = h.do(string(store.RoleViewer), "GET", "/mhd/Patient/$ihe-pix?sourceIdentifier=urn:oid:1.1|A1", nil)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"value":"B1"`) || !strings.Contains(res.Body.String(), a.Record.EUID) ||
		strings.Contains(res.Body.String(), `"value":"B2"`) {
		t.Errorf("pixm: %d %s", res.Code, res.Body.String())
	}

	// Settings are the administrator's, and checked.
	if res := h.do(string(store.RoleEditor), "PUT", "/api/mpi/settings", map[string]any{"autoLink": 0.95}); res.Code != http.StatusForbidden {
		t.Errorf("an editor changed the settings: %d", res.Code)
	}
	if res := h.do(string(store.RoleAdmin), "PUT", "/api/mpi/settings", map[string]any{"autoLink": 0.5, "review": 0.8}); res.Code != http.StatusBadRequest {
		t.Errorf("review above auto: %d", res.Code)
	}
	set := decodeBody[mpi.Settings](t, h.do(string(store.RoleAdmin), "PUT", "/api/mpi/settings", map[string]any{"automatic": true, "autoLink": 0.95, "review": 0.65,
		"trusted": []string{"http://hl7.org/fhir/sid/us-ssn"}}))
	if set.AutoLink != 0.95 || len(set.Trusted) != 1 || set.EUIDSystem == "" {
		t.Errorf("settings %+v", set)
	}
	entries, _ := h.server.Store.ListAudit(context.Background(), 200)
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Action] = true
	}
	// An estimate needs records to estimate from; three is too few, and says so.
	if res := h.do(string(store.RoleAdmin), "POST", "/api/mpi/estimate", nil); res.Code != http.StatusConflict {
		t.Errorf("estimate from three records: %d %s", res.Code, res.Body.String())
	}
	if res := h.do(string(store.RoleEditor), "POST", "/api/mpi/estimate", nil); res.Code != http.StatusForbidden {
		t.Errorf("an editor estimated: %d", res.Code)
	}
	for _, a := range []string{"mpi.search", "mpi.read", "mpi.review", "mpi.unlink", "mpi.settings", "pixm.query"} {
		if !seen[a] {
			t.Errorf("no %s in the audit log", a)
		}
	}
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// The index's default tenant is the account store's: a single-tenant channel registers there and a session reads there.
func TestThePatientIndexDefaultTenantIsTheStores(t *testing.T) {
	if mpi.DefaultTenant != string(store.DefaultTenant) {
		t.Fatalf("mpi.DefaultTenant %q, store.DefaultTenant %q: a channel would register patients nobody can see", mpi.DefaultTenant, store.DefaultTenant)
	}
}
