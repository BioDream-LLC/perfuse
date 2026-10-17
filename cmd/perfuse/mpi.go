package main

import (
	"context"
	"encoding/json"

	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/mpi"
)

// mpiMatcher answers the FHIR server's Patient/$match from the patient index.
type mpiMatcher struct{ store *mpi.Store }

func (m *mpiMatcher) Match(ctx context.Context, patient json.RawMessage, count int) ([]fhirserver.PatientMatch, error) {
	// The query need not carry an identifier with a system; only what it says is compared.
	var q mpi.Record
	if e, err := mpi.FromFHIR(patient); err == nil {
		q = e.Record
		q.Identifiers = append(q.Identifiers, q.Key())
		q.System, q.Value = "", ""
	} else {
		var p struct {
			Name []struct {
				Family string
				Given  []string
			}
			BirthDate, Gender string
		}
		if err := json.Unmarshal(patient, &p); err != nil {
			return nil, err
		}
		if len(p.Name) > 0 {
			q.Family = p.Name[0].Family
			if len(p.Name[0].Given) > 0 {
				q.Given = p.Name[0].Given[0]
			}
		}
		q.BirthDate = p.BirthDate
		q.Sex = map[string]string{"male": "M", "female": "F", "other": "O"}[p.Gender]
	}
	set, err := m.store.Settings(ctx, mpi.DefaultTenant)
	if err != nil {
		return nil, err
	}
	found, err := m.store.Find(ctx, mpi.DefaultTenant, q, set, count)
	if err != nil {
		return nil, err
	}
	out := make([]fhirserver.PatientMatch, 0, len(found))
	for _, f := range found {
		out = append(out, fhirserver.PatientMatch{Patient: mpi.ToFHIR(f.Person, set.EUIDSystem), Score: f.Score, Grade: f.Grade})
	}
	return out, nil
}
