package mpi

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"testing"
)

// febrlRecord is one row of a FEBRL dataset: synthetic people, with duplicates made by the errors registration makes
// (typos, swapped fields, missing values, nicknames). rec-N-org and rec-N-dup-k are the same person.
type febrlRecord struct {
	person string
	rec    Record
}

func loadFEBRL(t *testing.T) []febrlRecord {
	t.Helper()
	path := os.Getenv("PERFUSE_TEST_FEBRL")
	if path == "" {
		t.Skip("set PERFUSE_TEST_FEBRL to FEBRL's dataset3.csv (conformance/interop/febrl builds an image that writes it)")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[strings.TrimSpace(h)] = i
	}
	get := func(row []string, name string) string { return strings.TrimSpace(row[col[name]]) }
	var out []febrlRecord
	for _, row := range rows[1:] {
		id := get(row, "rec_id")
		parts := strings.Split(id, "-")
		dob := get(row, "date_of_birth")
		if len(dob) == 8 {
			dob = dob[:4] + "-" + dob[4:6] + "-" + dob[6:]
		}
		out = append(out, febrlRecord{person: parts[1], rec: Record{System: "urn:febrl", Value: id,
			Family: get(row, "surname"), Given: get(row, "given_name"), BirthDate: dob,
			Line: strings.TrimSpace(get(row, "street_number") + " " + get(row, "address_1")),
			City: get(row, "suburb"), Postal: get(row, "postcode")}})
	}
	return out
}

// linkage is how each arriving record was handled, against the truth.
type linkage struct {
	// Of records whose person the index already held:
	shouldLink, linked, queuedRight, queuedWrong, missed int
	// Of records whose person it did not hold:
	fresh, wrongLink, queuedNeedless int
	// Wrong links overall, the overlay count: one person's record put in another's chart.
	overlays int
}

func (l linkage) String() string {
	return fmt.Sprintf("%d records had a person already: %d linked, %d queued with the right person, %d queued with the wrong one, %d missed; "+
		"%d were new people: %d queued needlessly; wrong links (overlays) %d",
		l.shouldLink, l.linked, l.queuedRight, l.queuedWrong, l.missed, l.fresh, l.queuedNeedless, l.overlays)
}

// found is the share of records with a person already that were linked or put in front of a reviewer with that person.
func (l linkage) found() float64 {
	return float64(l.linked+l.queuedRight) / float64(max(1, l.shouldLink))
}

func runFEBRL(t *testing.T, data []febrlRecord, set Settings) (linkage, *Store) {
	t.Helper()
	ctx := context.Background()
	s := openTest(t)
	truth := map[string]string{}  // record value -> real person
	euidOf := map[string]string{} // real person -> an EUID holding one of their records
	var l linkage
	for _, d := range data {
		truth[d.rec.Value] = d.person
		_, known := euidOf[d.person]
		o, err := s.Register(ctx, "default", "febrl", d.rec, set)
		if err != nil {
			t.Fatal(err)
		}
		if known {
			l.shouldLink++
		} else {
			l.fresh++
		}
		switch o.Action {
		case "linked":
			p, _ := s.Person(ctx, "default", o.Record.EUID)
			right := false
			for _, r := range p.Records {
				if r.Value != d.rec.Value && truth[r.Value] == d.person {
					right = true
				}
			}
			if right {
				l.linked++
			} else {
				l.overlays++
				if !known {
					l.wrongLink++
				}
			}
		case "review":
			var rv Review
			rs, _ := s.Reviews(ctx, "default", 500)
			for _, x := range rs {
				if x.ID == o.ReviewID {
					rv = x
				}
			}
			right := false
			if rv.Candidate != nil {
				for _, r := range rv.Candidate.Records {
					if truth[r.Value] == d.person {
						right = true
					}
				}
			}
			switch {
			case known && right:
				l.queuedRight++
			case known:
				l.queuedWrong++
			default:
				l.queuedNeedless++
			}
			// The reviewer is the truth: decide it, so the queue reads as it would after a day's work.
			if err := s.Decide(ctx, "default", "reviewer", o.ReviewID, right); err != nil {
				t.Fatal(err)
			}
		default:
			if known {
				l.missed++
			}
		}
		if _, ok := euidOf[d.person]; !ok {
			r, _ := s.Record(ctx, "default", o.Record.ID)
			euidOf[d.person] = r.EUID
		}
	}
	return l, s
}

// The index measured on FEBRL dataset 3 (5,000 synthetic records of 2,000 people), with the published defaults and then
// with weights it estimates from the same records. The bar is the patient-safety one: automatic links that join two
// people must be rare, and the review queue must hold the matches automatic linking leaves.
func TestThePatientIndexOnFEBRL(t *testing.T) {
	data := loadFEBRL(t)

	off, s := runFEBRL(t, data, Settings{})
	t.Logf("automatic linking off (the default): %s", off)
	if off.linked != 0 || off.overlays != 0 {
		t.Errorf("with automatic linking off, %d records were linked on a score", off.linked+off.overlays)
	}

	auto, _ := runFEBRL(t, data, Settings{Automatic: true})
	t.Logf("automatic linking, default weights: %s", auto)

	est, err := s.EstimateFromRecords(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("estimated from %d pairs in %d iterations (converged %v), match share %.3f: %+v", est.Pairs, est.Iterations, est.Converged, est.MatchShare, est.Weights)
	fitted, _ := runFEBRL(t, data, Settings{Automatic: true, Weights: est.Weights})
	t.Logf("automatic linking, estimated weights: %s", fitted)

	for name, l := range map[string]linkage{"off": off, "default weights": auto, "estimated weights": fitted} {
		// Patient safety first: one wrong link in a thousand records at most.
		if l.overlays > len(data)/1000 {
			t.Errorf("%s: %d records were linked to somebody else", name, l.overlays)
		}
		// And the index is useful: it finds most of the people it already holds, by itself or through a reviewer.
		if l.found() < 0.85 {
			t.Errorf("%s: found %.3f of the records whose person it held", name, l.found())
		}
		t.Logf("%s: found %.3f", name, l.found())
	}
	if !est.Converged {
		t.Error("the estimate did not converge")
	}
}
