package api

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/mpi"
	"github.com/biodream-llc/perfuse/internal/sqlitedb"
	"github.com/biodream-llc/perfuse/internal/store"
)

// National numbers are masked for roles below the index's setting (editor by default), in every answer that carries them.
func TestNationalNumbersAreMaskedBelowTheSettingsRole(t *testing.T) {
	h := newHarness(t)
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
	res := h.do(string(store.RoleEditor), "POST", "/api/mpi/records", map[string]any{"system": "urn:oid:1.1", "value": "A1",
		"family": "Jones", "given": "Ann", "birthDate": "1970-01-01", "identifiers": []string{"https://fhir.nhs.uk/Id/nhs-number|943 476 5919"}})
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "9434765919") {
		t.Fatalf("an editor should see the number in full: %d %s", res.Code, res.Body.String())
	}
	out := decodeBody[mpi.Outcome](t, res)
	for _, path := range []string{"/api/mpi", "/api/mpi/persons/" + out.Record.EUID} {
		body := h.do(string(store.RoleViewer), "GET", path, nil).Body.String()
		if strings.Contains(body, "9434765919") || !strings.Contains(body, "5919") {
			t.Errorf("%s as a viewer: %s", path, body)
		}
	}
	found := h.do(string(store.RoleViewer), "POST", "/api/mpi/find", map[string]any{"identifiers": []string{"https://fhir.nhs.uk/Id/nhs-number|9434765919"}}).Body.String()
	if strings.Contains(found, "9434765919") || !strings.Contains(found, `"certain"`) {
		t.Errorf("find as a viewer: %s", found)
	}
	// An administrator can let viewers see them, or keep them from editors.
	if res := h.do(string(store.RoleAdmin), "PUT", "/api/mpi/settings", map[string]any{"showNumbersTo": "admin"}); res.Code != http.StatusOK {
		t.Fatal(res.Body.String())
	}
	if body := h.do(string(store.RoleEditor), "GET", "/api/mpi", nil).Body.String(); strings.Contains(body, "9434765919") {
		t.Errorf("an editor saw the number with showNumbersTo admin: %s", body)
	}
}
