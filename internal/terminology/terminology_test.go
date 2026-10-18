package terminology

import (
	"context"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/sqlitedb"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func open(t *testing.T) *Store {
	t.Helper()
	db, err := sqlitedb.OpenSQL("file:" + t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s, err := Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const loincCSV = "\ufeff\"LOINC_NUM\",\"COMPONENT\",\"STATUS\",\"SHORTNAME\",\"LONG_COMMON_NAME\"\n" +
	"\"2345-7\",\"Glucose\",\"ACTIVE\",\"Glucose SerPl-mCnc\",\"Glucose [Mass/volume] in Serum or Plasma\"\n" +
	"\"1234-5\",\"Old\",\"DEPRECATED\",\"Old thing\",\"\"\n" +
	"\"718-7\",\"Hemoglobin\",\"ACTIVE\",\"Hgb Bld-mCnc\",\"Hemoglobin [Mass/volume] in Blood\"\n"

// icdOrderFile is in CMS's layout: order (5), code (7), billable flag, short description (60), long description.
var icdOrderFile = func() string {
	var b strings.Builder
	for i, r := range [][3]string{{"A00", "0", "Cholera"}, {"A000", "1", "Cholera due to Vibrio cholerae 01, biovar cholerae"},
		{"E1165", "1", "Type 2 diabetes mellitus with hyperglycemia"}} {
		fmt.Fprintf(&b, "%05d %-7s %s %-60s %s\n", i+1, r[0], r[1], r[2], r[2])
	}
	return b.String()
}()

const rxconso = "198440|ENG||||||1||||RXNORM|IN|198440|acetaminophen 500 MG||N|\n" +
	"198440|ENG||||||2||||RXNORM|SCD|198440|Acetaminophen 500 MG Oral Tablet||N|\n" +
	"198440|ENG||||||3||||MTHSPL|SU|x|ACETAMINOPHEN TAB||N|\n" +
	"161|ENG||||||4||||RXNORM|IN|161|Acetaminophen||N|\n" +
	"999|ENG||||||5||||RXNORM|SCD|999|Gone Drug||O|\n"

const snomedDesc = "id\teffectiveTime\tactive\tmoduleId\tconceptId\tlanguageCode\ttypeId\tterm\tcaseSignificanceId\n" +
	"1\t20250101\t1\t900000000000207008\t22298006\ten\t900000000000013009\tHeart attack\t900000000000448009\n" +
	"2\t20250101\t1\t900000000000207008\t22298006\ten\t900000000000003001\tMyocardial infarction (disorder)\t900000000000448009\n" +
	"3\t20250101\t0\t900000000000207008\t1111\ten\t900000000000003001\tRetired thing (disorder)\t900000000000448009\n"

func TestImportEachFormatAndLookUp(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for _, c := range []struct {
		req  ImportRequest
		body string
		want int
	}{
		{ImportRequest{Format: FormatLOINC, Version: "2.80", By: "admin"}, loincCSV, 3},
		{ImportRequest{Format: FormatICD10CM, Version: "2026"}, icdOrderFile, 3},
		{ImportRequest{Format: FormatRxNorm, Version: "2026-10"}, rxconso, 3},
		{ImportRequest{Format: FormatSNOMED, Version: "20250101"}, snomedDesc, 2},
		{ImportRequest{Format: FormatCSV, System: "urn:local:wards", Name: "Wards", CodeColumn: "Ward", DisplayColumn: "Name"},
			"Ward,Name\nW1,West one\nICU,Intensive care\n", 2},
	} {
		res, err := s.Import(ctx, c.req, strings.NewReader(c.body))
		if err != nil || res.Concepts != c.want {
			t.Fatalf("%s: %+v %v", c.req.Format, res, err)
		}
	}
	for _, c := range []struct {
		system, code, display string
		active                bool
	}{
		{SystemLOINC, "2345-7", "Glucose [Mass/volume] in Serum or Plasma", true},
		{SystemLOINC, "1234-5", "Old thing", false},
		{SystemICD10CM, "E11.65", "Type 2 diabetes mellitus with hyperglycemia", true},
		{SystemICD10CM, "e1165", "Type 2 diabetes mellitus with hyperglycemia", true},
		{SystemICD10CM, "A00.0", "Cholera due to Vibrio cholerae 01, biovar cholerae", true},
		{SystemRxNorm, "198440", "Acetaminophen 500 MG Oral Tablet", true},
		{SystemRxNorm, "999", "Gone Drug", false},
		{SystemSNOMED, "22298006", "Myocardial infarction (disorder)", true},
		{SystemSNOMED, "1111", "Retired thing (disorder)", false},
		{"urn:local:wards", "ICU", "Intensive care", true},
	} {
		got, err := s.Lookup(ctx, c.system, "", c.code)
		if err != nil || got.Display != c.display || got.Active != c.active {
			t.Errorf("%s %s: %+v %v", c.system, c.code, got, err)
		}
	}
	if _, err := s.Lookup(ctx, SystemLOINC, "", "0000-0"); err != ErrNotFound {
		t.Errorf("a missing code: %v", err)
	}
	found, _ := s.Search(ctx, SystemLOINC, "hemoglobin", 10)
	if len(found) != 1 || found[0].Code != "718-7" {
		t.Errorf("search %+v", found)
	}
	byCode, _ := s.Search(ctx, SystemICD10CM, "A00", 10)
	if len(byCode) != 2 || byCode[0].Code != "A00" {
		t.Errorf("code prefix search %+v", byCode)
	}
	missing, _ := s.Missing(ctx, SystemLOINC, []string{"2345-7", "9999-9", "9999-9"})
	if len(missing) != 1 || missing[0] != "9999-9" {
		t.Errorf("missing %v", missing)
	}
	if m, _ := s.Missing(ctx, "urn:not-imported", []string{"x"}); m != nil {
		t.Error("a system not imported says nothing either way")
	}
	systems, _ := s.Systems(ctx)
	if len(systems) != 5 {
		t.Fatalf("%d systems", len(systems))
	}
	// A re-import of the same version replaces it, and a failed import leaves the previous one.
	if _, err := s.Import(ctx, ImportRequest{Format: FormatLOINC, Version: "2.80"}, strings.NewReader("\"NOPE\"\n\"x\"\n")); err == nil {
		t.Fatal("a file without LOINC_NUM was accepted")
	}
	if _, err := s.Lookup(ctx, SystemLOINC, "2.80", "718-7"); err != nil {
		t.Errorf("the failed import removed the old one: %v", err)
	}
	if err := s.Delete(ctx, SystemLOINC, "2.80"); err != nil {
		t.Fatal(err)
	}
	if s.HasSystem(ctx, SystemLOINC) {
		t.Error("deleted system still there")
	}
	if _, err := s.Import(ctx, ImportRequest{Format: FormatCSV}, strings.NewReader("code,display\n1,a\n")); err == nil {
		t.Error("a csv import without a system was accepted")
	}
}

func TestUnmappedValuesAreCountedAndListed(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		s.NoteUnmapped("", "adt", "sex", "X")
	}
	s.NoteUnmapped("", "lab", "sex", "X")
	s.NoteUnmapped("", "adt", "sex", "Q")
	s.NoteUnmapped("", "adt", "sex", strings.Repeat("long free text ", 10))
	s.NoteUnmapped("other", "adt", "sex", "Z")
	list, err := s.ListUnmapped(ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Value != "X" || list[0].Count != 6 || strings.Join(list[0].Channels, ",") != "adt,lab" {
		t.Fatalf("%+v", list)
	}
	only, _ := s.ListUnmapped(ctx, "", func(ch string) bool { return ch == "lab" })
	if len(only) != 1 || only[0].Count != 1 {
		t.Errorf("scoped %+v", only)
	}
	if err := s.Dismiss(ctx, "", "sex", "X"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListUnmapped(ctx, "", nil); len(list) != 1 || list[0].Value != "Q" {
		t.Errorf("after dismiss %+v", list)
	}
	s.NoteUnmapped("", "adt", "sex", "X")
	if list, _ := s.ListUnmapped(ctx, "", nil); len(list) != 2 {
		t.Errorf("a dismissed value seen again should come back: %+v", list)
	}
	if err := s.Forget(ctx, "", "sex", []string{"Q"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(ctx, "", "", nil, -time.Hour); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListUnmapped(ctx, "", nil); len(list) != 1 || list[0].Value != "X" {
		t.Errorf("after forget %+v", list)
	}
}
