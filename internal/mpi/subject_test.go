package mpi

import (
	"context"
	"testing"
)

// A data protection request often gives a person's number on its own. The person holding it is found when exactly one does;
// two people with the same bare number (two hospitals' record 1001) are not, since erasing either would be a guess.
func TestASubjectFoundByABareNumberOnlyWhenItIsUnambiguous(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	for _, r := range []Record{
		{System: "urn:oid:1.2.3", Value: "H77", Family: "Jones", Given: "Ann", BirthDate: "1970-01-01"},
		{System: "urn:oid:1.2.3", Value: "1001", Family: "Smith", Given: "Bob", BirthDate: "1980-05-05"},
		{System: "urn:oid:4.5.6", Value: "1001", Family: "Patel", Given: "Raj", BirthDate: "1990-09-09"},
	} {
		if _, err := s.Register(ctx, "", "t", r, Settings{}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SubjectPersons(ctx, "", []string{"H77"}, Settings{})
	if err != nil || len(got) != 1 || got[0].Records[0].Family != "Jones" {
		t.Fatalf("H77 on its own: %v %+v", err, got)
	}
	if got, _ = s.SubjectPersons(ctx, "", []string{"1001"}, Settings{}); len(got) != 0 {
		t.Errorf("a bare number two people hold found %d of them, want none", len(got))
	}
	if got, _ = s.SubjectPersons(ctx, "", []string{"urn:oid:4.5.6|1001"}, Settings{}); len(got) != 1 {
		t.Errorf("system|value found %d, want 1", len(got))
	}
}
