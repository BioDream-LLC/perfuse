package terminology

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

// rf2Zip builds a small Swedish-edition-like RF2 package: International and Swedish modules, English and Swedish
// descriptions, the GB and US English and a Swedish language reference set.
func rf2Zip(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	file := func(name, body string) {
		w, err := z.Create("SnomedCT_ManagedServiceSE_PRODUCTION_SE1000052_20250531T120000Z/Snapshot/" + name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	const se = "45991000052106"
	file("Terminology/sct2_Concept_Snapshot_SE1000052_20250531.txt",
		"id\teffectiveTime\tactive\tmoduleId\tdefinitionStatusId\r\n"+
			"38341003\t20020131\t1\t900000000000207008\t900000000000074008\r\n"+
			"271737000\t20020131\t1\t900000000000207008\t900000000000074008\r\n"+
			"999000001\t20250531\t1\t"+se+"\t900000000000074008\r\n"+
			"999000002\t20250531\t1\t"+se+"\t900000000000074008\r\n"+
			"123037004\t20020131\t0\t900000000000207008\t900000000000074008\r\n")
	file("Terminology/sct2_Description_Snapshot-en_SE1000052_20250531.txt",
		"id\teffectiveTime\tactive\tmoduleId\tconceptId\tlanguageCode\ttypeId\tterm\tcaseSignificanceId\r\n"+
			"1\t20020131\t1\t900000000000207008\t38341003\ten\t900000000000003001\tHypertensive disorder, systemic arterial (disorder)\t9\r\n"+
			"2\t20020131\t1\t900000000000207008\t38341003\ten\t900000000000013009\tHypertensive disorder\t9\r\n"+
			"3\t20020131\t1\t900000000000207008\t271737000\ten\t900000000000003001\tAnemia (disorder)\t9\r\n"+
			"4\t20020131\t1\t900000000000207008\t271737000\ten\t900000000000013009\tAnemia\t9\r\n"+
			"5\t20020131\t1\t900000000000207008\t271737000\ten\t900000000000013009\tAnaemia\t9\r\n"+
			"6\t20020131\t1\t900000000000207008\t123037004\ten\t900000000000003001\tBody structure (body structure)\t9\r\n")
	file("Terminology/sct2_Description_Snapshot-sv_SE1000052_20250531.txt",
		"id\teffectiveTime\tactive\tmoduleId\tconceptId\tlanguageCode\ttypeId\tterm\tcaseSignificanceId\r\n"+
			"11\t20250531\t1\t"+se+"\t38341003\tsv\t900000000000013009\thypertoni\t9\r\n"+
			"12\t20250531\t1\t"+se+"\t271737000\tsv\t900000000000013009\tanemi\t9\r\n"+
			"13\t20250531\t1\t"+se+"\t999000001\tsv\t900000000000013009\tsvensk sak\t9\r\n")
	file("Refset/Language/der2_cRefset_LanguageSnapshot-en_SE1000052_20250531.txt",
		"id\teffectiveTime\tactive\tmoduleId\trefsetId\treferencedComponentId\tacceptabilityId\r\n"+
			"a\t20020131\t1\t900000000000207008\t900000000000509007\t2\t900000000000548007\r\n"+
			"b\t20020131\t1\t900000000000207008\t900000000000509007\t4\t900000000000548007\r\n"+
			"c\t20020131\t1\t900000000000207008\t900000000000508004\t5\t900000000000548007\r\n"+
			"d\t20020131\t1\t900000000000207008\t900000000000508004\t2\t900000000000548007\r\n")
	file("Refset/Language/der2_cRefset_LanguageSnapshot-sv_SE1000052_20250531.txt",
		"id\teffectiveTime\tactive\tmoduleId\trefsetId\treferencedComponentId\tacceptabilityId\r\n"+
			"e\t20250531\t1\t"+se+"\t46011000052107\t11\t900000000000548007\r\n"+
			"f\t20250531\t1\t"+se+"\t46011000052107\t12\t900000000000548007\r\n"+
			"g\t20250531\t1\t"+se+"\t46011000052107\t13\t900000000000548007\r\n")
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestASNOMEDNationalEditionPackage(t *testing.T) {
	s, ctx := open(t), context.Background()
	res, err := s.Import(ctx, ImportRequest{Format: FormatSNOMEDRF2, Language: "sv"}, bytes.NewReader(rf2Zip(t)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Edition != "45991000052106" || res.Version != "http://snomed.info/sct/45991000052106/version/20250531" {
		t.Fatalf("edition %+v", res)
	}
	if strings.Join(res.Languages, ",") != "en,sv" || res.Concepts != 4 { // 999000002 has no description, so no name to give it
		t.Fatalf("%+v", res)
	}
	c, err := s.Lookup(ctx, SystemSNOMED, "", "38341003")
	if err != nil || c.Display != "hypertoni" || !c.Active {
		t.Fatalf("Swedish display: %+v %v", c, err)
	}
	if c, _ := s.Lookup(ctx, SystemSNOMED, "", "123037004"); c.Active {
		t.Error("an inactive concept came back active")
	}
	ds, err := s.Designations(ctx, SystemSNOMED, res.Version, "271737000")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range ds {
		got[d.Language+"/"+d.Use] = d.Value
	}
	if got["sv/preferred"] != "anemi" || got["en-GB/preferred"] != "Anaemia" || got["en-US/preferred"] != "Anemia" || got["en/fsn"] != "Anemia (disorder)" {
		t.Fatalf("designations %v", got)
	}
	// The same package read for British English.
	res, err = s.Import(ctx, ImportRequest{Format: FormatSNOMEDRF2, Language: "en-GB", Version: "uk-test"}, bytes.NewReader(rf2Zip(t)))
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Lookup(ctx, SystemSNOMED, "uk-test", "271737000"); c.Display != "Anaemia" {
		t.Fatalf("GB display %q", c.Display)
	}
}

const claml = `<?xml version="1.0" encoding="ISO-8859-1"?>
<!DOCTYPE ClaML SYSTEM "ClaML.dtd">
<ClaML version="2.0.0">
 <Title name="ICD-10-GM" version="2025" date="2024-09-13">Internationale statistische Klassifikation</Title>
 <Modifier code="S05_5"><SubClass code="0"/><SubClass code="1"/></Modifier>
 <ModifierClass modifier="S05_5" code="0"><SuperClass code="S05_5"/><Rubric kind="preferred"><Label xml:lang="de">geschlossen</Label></Rubric></ModifierClass>
 <ModifierClass modifier="S05_5" code="1"><SuperClass code="S05_5"/><Rubric kind="preferred"><Label xml:lang="de">offen</Label></Rubric></ModifierClass>
 <Class code="I" kind="chapter"><SubClass code="A00-A09"/><Rubric kind="preferred"><Label xml:lang="de">Bestimmte infektiöse Krankheiten</Label></Rubric></Class>
 <Class code="A00-A09" kind="block"><SuperClass code="I"/><Rubric kind="preferred"><Label xml:lang="de">Infektiöse Darmkrankheiten</Label></Rubric></Class>
 <Class code="A00" kind="category"><SuperClass code="A00-A09"/><SubClass code="A00.0"/><Rubric kind="preferred"><Label xml:lang="de">Cholera</Label></Rubric></Class>
 <Class code="A00.0" kind="category"><SuperClass code="A00"/><Rubric kind="preferred"><Label xml:lang="de">Cholera durch <Term>Vibrio cholerae</Term> O:1, Biovar cholerae</Label></Rubric>
  <Rubric kind="inclusion"><Label xml:lang="de">Klassische Cholera</Label></Rubric></Class>
 <Class code="S02" kind="category"><SuperClass code="A00-A09"/><SubClass code="S02.0"/><ModifiedBy code="S05_5" all="true"/><Rubric kind="preferred"><Label xml:lang="de">Fraktur des Schädels</Label></Rubric></Class>
 <Class code="S02.0" kind="category"><SuperClass code="S02"/><Rubric kind="preferred"><Label xml:lang="de">Schädeldachfraktur</Label></Rubric></Class>
</ClaML>`

func TestAClaMLClassificationInLatin1(t *testing.T) {
	s, ctx := open(t), context.Background()
	latin1, err := charmap.ISO8859_1.NewEncoder().String(claml)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Import(ctx, ImportRequest{Format: FormatClaML}, strings.NewReader(latin1))
	if err != nil {
		t.Fatal(err)
	}
	if res.System != SystemICD10GM || res.Version != "2025" {
		t.Fatalf("named %+v", res)
	}
	// A00, A00.0, S02, S02.0, and S02.00 / S02.01 made by the modifier; chapters and blocks left out.
	if res.Concepts != 6 || res.Derived != 2 {
		t.Fatalf("%+v", res)
	}
	if c, _ := s.Lookup(ctx, SystemICD10GM, "", "A00.0"); c.Display != "Cholera durch Vibrio cholerae O:1, Biovar cholerae" {
		t.Fatalf("title %q", c.Display)
	}
	if c, _ := s.Lookup(ctx, SystemICD10GM, "", "S02.01"); c.Display != "Schädeldachfraktur: offen" {
		t.Fatalf("modifier title %q", c.Display)
	}
	if _, err := s.Lookup(ctx, SystemICD10GM, "", "A00-A09"); err != ErrNotFound {
		t.Fatal("a block was imported without blocks")
	}
}

func TestICD11SimpleTabulation(t *testing.T) {
	s, ctx := open(t), context.Background()
	tab := "Foundation URI\tLinearization URI\tCode\tBlockId\tTitle\tClassKind\tDepthInKind\tIsResidual\tChapterNo\n" +
		"http://id.who.int/icd/entity/1435254666\thttp://id.who.int/icd/release/11/2025-01/mms/1435254666\t\t\tCertain infectious or parasitic diseases\tchapter\t1\tFalse\t01\n" +
		"x\ty\t\tBlockL1-1A0\tGastroenteritis or colitis of infectious origin\tblock\t1\tFalse\t01\n" +
		"x\ty\t1A00\t\t- - Cholera\tcategory\t1\tFalse\t01\n" +
		"x\ty\t1A00.0\t\t- - - Cholera due to Vibrio cholerae O1, biovar cholerae\tcategory\t2\tFalse\t01\n"
	res, err := s.Import(ctx, ImportRequest{Format: FormatICD11, Version: "2025-01"}, strings.NewReader(tab))
	if err != nil {
		t.Fatal(err)
	}
	if res.Concepts != 2 {
		t.Fatalf("%+v", res)
	}
	if c, _ := s.Lookup(ctx, SystemICD11, "", "1A00"); c.Display != "Cholera" {
		t.Fatalf("%q", c.Display)
	}
}

// A French table: semicolons, Windows-1252, no header, columns by number.
func TestADelimitedFileWithoutAHeader(t *testing.T) {
	s, ctx := open(t), context.Background()
	body, _ := charmap.Windows1252.NewEncoder().String("A00.0;Choléra à Vibrio cholerae 01, biovar cholerae\nA00.1;Choléra à Vibrio cholerae 01, biovar El Tor\n")
	res, err := s.Import(ctx, ImportRequest{Format: FormatCSV, System: SystemCIM10FR, Delimiter: ";", Encoding: "windows-1252",
		NoHeader: true, CodeColumn: "1", DisplayColumn: "2"}, strings.NewReader(body))
	if err != nil || res.Concepts != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if c, _ := s.Lookup(ctx, SystemCIM10FR, "", "A00.1"); c.Display != "Choléra à Vibrio cholerae 01, biovar El Tor" {
		t.Fatalf("%q", c.Display)
	}
}

// A version that comes into force later is imported now and used from its day; until then the one in force answers.
func TestAVersionIsUsedFromItsEffectiveDate(t *testing.T) {
	s, ctx := open(t), context.Background()
	if _, err := s.Import(ctx, ImportRequest{Format: FormatCSV, System: "urn:x", Version: "2025"}, strings.NewReader("code,display\nA,old\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, ImportRequest{Format: FormatCSV, System: "urn:x", Version: "2099", Effective: "2099-01-01"}, strings.NewReader("code,display\nA,new\n")); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Lookup(ctx, "urn:x", "", "A"); c.Display != "old" || c.Version != "2025" {
		t.Fatalf("before its day: %+v", c)
	}
	if c, _ := s.Lookup(ctx, "urn:x", "2099", "A"); c.Display != "new" {
		t.Fatalf("named: %+v", c)
	}
	if _, err := s.Import(ctx, ImportRequest{Format: FormatCSV, System: "urn:x", Effective: "1 Jan"}, strings.NewReader("code,display\nA,x\n")); err == nil {
		t.Fatal("a malformed effective date was accepted")
	}
}

// The WHO ICD API answers ICD-11 codes not imported: a client-credentials token, codeinfo, then the stem entity's title.
func TestICD11CodesFromTheWHOAPI(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			_ = r.ParseForm()
			if r.Form.Get("client_id") != "id" || r.Form.Get("scope") != "icdapi_access" || r.Form.Get("grant_type") != "client_credentials" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
		case r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("API-Version") != "v2":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/icd/release/11/2025-01/mms/codeinfo/1A00":
			_, _ = w.Write([]byte(`{"code":"1A00","stemId":"http://id.who.int/icd/release/11/2025-01/mms/257068234"}`))
		case r.URL.Path == "/icd/release/11/2025-01/mms/257068234":
			title := "Cholera"
			if r.Header.Get("Accept-Language") == "fr" {
				title = "Choléra"
			}
			_, _ = w.Write([]byte(`{"title":{"@language":"en","@value":"` + title + `"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	s, ctx := open(t), context.Background()
	s.ICD11 = &ICD11API{ClientID: "id", ClientSecret: "secret", Release: "2025-01", TokenURL: srv.URL + "/token", BaseURL: srv.URL}
	if !s.HasSystem(ctx, SystemICD11) {
		t.Fatal("ICD-11 should be answerable with the API configured")
	}
	c, err := s.Lookup(ctx, SystemICD11, "", "1A00")
	if err != nil || c.Display != "Cholera" || c.Version != "2025-01" {
		t.Fatalf("%+v %v", c, err)
	}
	if title, found, _ := s.ICD11.Lookup(ctx, "1A00", "fr"); !found || title != "Choléra" {
		t.Fatalf("French: %q", title)
	}
	if _, err := s.Lookup(ctx, SystemICD11, "", "ZZ99"); err != ErrNotFound {
		t.Fatalf("an unknown code: %v", err)
	}
}

// TestRealICD11API asks the WHO's own ICD-API container (whoicd/icd-api, run with include=2025-01_en-fr), the same API
// id.who.int serves, without a client: PERFUSE_TEST_ICD11_URL=http://127.0.0.1:6382.
func TestRealICD11API(t *testing.T) {
	base := os.Getenv("PERFUSE_TEST_ICD11_URL")
	if base == "" {
		t.Skip("PERFUSE_TEST_ICD11_URL is not set (the WHO's ICD-API container)")
	}
	ctx := context.Background()
	api := &ICD11API{Release: "2025-01", BaseURL: base}
	for _, c := range []struct{ code, lang, want string }{
		{"1A00", "en", "Cholera"},
		{"1A00", "fr", "Choléra"},
		{"BA00", "en", "Essential hypertension"},
		{"BA00.Z", "en", "Essential hypertension, unspecified"},
		{"1A03.0", "en", "Enteropathogenic Escherichia coli infection"},
		{"2C25.0&XK9K", "en", "Adenocarcinoma of bronchus or lung (Right)"},
	} {
		got, found, err := api.Lookup(ctx, c.code, c.lang)
		if err != nil || !found || got != c.want {
			t.Errorf("%s (%s) = %q, %v, %v; want %q", c.code, c.lang, got, found, err, c.want)
		}
	}
	for _, code := range []string{"ZZ99", "1A00&XS25"} { // no such code; a combination ICD-11 does not allow
		if got, found, err := api.Lookup(ctx, code, "en"); err != nil || found {
			t.Errorf("%s = %q, %v, %v; want not found", code, got, found, err)
		}
	}
	s := open(t)
	s.ICD11 = api
	if c, err := s.Lookup(ctx, SystemICD11, "", "BA00"); err != nil || c.Display != "Essential hypertension" {
		t.Errorf("the store's lookup through the API = %+v, %v", c, err)
	}
}
