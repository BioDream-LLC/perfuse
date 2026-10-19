package terminology

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
)

// Import formats.
const (
	FormatLOINC   = "loinc"   // Loinc.csv from the LOINC release (LOINC_NUM, LONG_COMMON_NAME, STATUS)
	FormatICD10CM = "icd10cm" // CMS's icd10cm_order_YYYY.txt or icd10cm_codes_YYYY.txt
	FormatRxNorm  = "rxnorm"  // RXNCONSO.RRF from the RxNorm full release
	FormatSNOMED  = "snomed"  // an RF2 sct2_Description_Snapshot file
	FormatCSV     = "csv"     // any CSV with a header: code and display columns (CPT, HCPCS, local codes)
	// Outside the US.
	FormatSNOMEDRF2 = "snomed-rf2" // a whole SNOMED CT RF2 release package (zip): an edition with its languages
	FormatClaML     = "claml"      // ClaML XML: WHO ICD-10, ICD-10-GM, OPS, CIM-10 FR
	FormatICD11     = "icd11"      // the WHO's ICD-11 MMS simple tabulation (tab-separated)
)

// ImportRequest says what is being imported.
type ImportRequest struct {
	Format string
	// System is the code system URL; the format's own when empty (required for csv).
	System  string
	Version string
	// Name is a label for the console; the format's own when empty.
	Name string
	// CodeColumn and DisplayColumn name the columns of a CSV; code and display when empty.
	CodeColumn, DisplayColumn string
	// By is who imported it.
	By string
	// Licence is what the importer confirmed about the licence, recorded with the import.
	Licence string
	// Effective is the day this version comes into force (YYYY-MM-DD); empty means at once.
	Effective string
	// Language is the language whose preferred term becomes a SNOMED CT package's display (sv, da, nl, de, fr, en-GB).
	Language string
	// Edition is a SNOMED CT edition's module, when the package's own should not be used.
	Edition string
	// Blocks imports a classification's chapters and blocks as well as its codes.
	Blocks bool
	// Delimiter is a CSV file's separator: , (the default), ; | or tab. Encoding is utf-8 (the default), iso-8859-1 or
	// windows-1252. NoHeader reads a file without a header row, its columns named by number (1, 2 ...).
	Delimiter, Encoding string
	NoHeader            bool
}

// ImportResult says what was imported.
type ImportResult struct {
	System   string `json:"system"`
	Version  string `json:"version"`
	Concepts int    `json:"concepts"`
	Skipped  int    `json:"skipped"`
	// Edition and Languages say what a SNOMED CT package was; Designations how many names in other languages were kept.
	Edition      string   `json:"edition,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	Designations int      `json:"designations,omitempty"`
	// Derived is how many of a ClaML file's codes were made by its modifiers.
	Derived int `json:"derived,omitempty"`
}

// Defaults gives a format's system URL and name.
func Defaults(format string) (system, name string, ok bool) {
	switch format {
	case FormatLOINC:
		return SystemLOINC, "LOINC", true
	case FormatICD10CM:
		return SystemICD10CM, "ICD-10-CM", true
	case FormatRxNorm:
		return SystemRxNorm, "RxNorm", true
	case FormatSNOMED:
		return SystemSNOMED, "SNOMED CT", true
	case FormatCSV:
		return "", "", true
	case FormatSNOMEDRF2:
		return SystemSNOMED, "SNOMED CT", true
	case FormatClaML:
		return "", "", true // named by the file's own Title, or given
	case FormatICD11:
		return SystemICD11, "ICD-11 MMS", true
	}
	return "", "", false
}

// Import reads a publisher's file and replaces that version of the system with it, in one transaction: a failed import leaves
// the previous one in place.
func (s *Store) Import(ctx context.Context, req ImportRequest, r io.Reader) (ImportResult, error) {
	system, name, ok := Defaults(req.Format)
	if !ok {
		return ImportResult{}, fmt.Errorf("format %q is not known; use loinc, icd10cm, rxnorm, snomed, snomed-rf2, claml, icd11 or csv", req.Format)
	}
	if req.System != "" {
		system = strings.TrimSpace(req.System)
	}
	if req.Name != "" {
		name = strings.TrimSpace(req.Name)
	}
	if system == "" && req.Format != FormatClaML {
		return ImportResult{}, errors.New("a csv import needs the code system's URL (system), such as " + SystemCPT)
	}
	if system != "" && !strings.Contains(system, ":") {
		return ImportResult{}, fmt.Errorf("system %q is not a URL or URN; code systems are named by URL, such as %s", system, SystemCPT)
	}
	version := strings.TrimSpace(req.Version)
	effective := strings.TrimSpace(req.Effective)
	if effective != "" {
		if _, err := time.Parse("2006-01-02", effective); err != nil {
			return ImportResult{}, fmt.Errorf("the effective date %q should be YYYY-MM-DD", effective)
		}
	}
	// Some files name their own version (a SNOMED CT package, a ClaML title), known only once read: the codes go in under a
	// working name, renamed in the same transaction when the import is complete.
	working := "importing-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// A ClaML file names its system in its title; the system is settled before the first code is stored.
	var claml *bufio.Reader
	if req.Format == FormatClaML && system == "" {
		claml = bufio.NewReaderSize(r, 1<<20)
		head, _ := claml.Peek(64 << 10)
		if m := clamlTitle.FindSubmatch(head); m != nil {
			system, name = clamlSystem(string(m[1]))
		}
		if system == "" {
			return ImportResult{}, errors.New("this ClaML file's title names no classification Perfuse knows (WHO ICD-10, ICD-10-GM, OPS, CIM-10): give the system's URL")
		}
		r = claml
	}
	if req.Name != "" {
		name = strings.TrimSpace(req.Name)
	}
	if name == "" {
		name = system
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO term_concepts (system, version, code, display, active) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (system, version, code) DO UPDATE SET display = excluded.display, active = excluded.active`)
	if err != nil {
		return ImportResult{}, err
	}
	defer ins.Close()
	insD, err := tx.PrepareContext(ctx, `INSERT INTO term_designations (system, version, code, language, use_code, value, preferred)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return ImportResult{}, err
	}
	defer insD.Close()
	res := ImportResult{System: system}
	designate := func(code string, d Designation) error {
		res.Designations++
		_, err := insD.ExecContext(ctx, system, working, NormaliseCode(system, code), d.Language, d.Use, d.Value, map[bool]int{true: 1, false: 0}[d.Preferred])
		return err
	}
	add := func(code, display string, active bool) error {
		code = NormaliseCode(system, code)
		display = strings.TrimSpace(display)
		if code == "" || len(code) > 64 || len(display) > 4096 {
			res.Skipped++
			return nil
		}
		if res.Concepts%10000 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		res.Concepts++
		_, err := ins.ExecContext(ctx, system, working, code, display, map[bool]int{true: 1, false: 0}[active])
		return err
	}
	csvOpts := csvOptions{delimiter: req.Delimiter, encoding: req.Encoding, noHeader: req.NoHeader}

	switch req.Format {
	case FormatLOINC:
		err = readCSV(r, csvOptions{}, []string{"LOINC_NUM"}, func(get func(string) string) error {
			display := get("LONG_COMMON_NAME")
			if display == "" {
				display = get("SHORTNAME")
			}
			if display == "" {
				display = get("COMPONENT")
			}
			return add(get("LOINC_NUM"), display, get("STATUS") != "DEPRECATED")
		})
	case FormatCSV:
		cc, dc := req.CodeColumn, req.DisplayColumn
		if cc == "" {
			cc = "code"
		}
		if dc == "" {
			dc = "display"
		}
		err = readCSV(r, csvOpts, []string{cc, dc}, func(get func(string) string) error {
			active := true
			if st := strings.ToLower(get("status")); st == "inactive" || st == "deprecated" || st == "retired" {
				active = false
			}
			return add(get(cc), get(dc), active)
		})
	case FormatICD10CM:
		err = readICD10CM(r, add)
	case FormatRxNorm:
		err = readRxNorm(r, add)
	case FormatSNOMED:
		err = readSNOMED(r, add)
	case FormatSNOMEDRF2:
		var pkg rf2Result
		pkg, err = readRF2Package(r, req.Language, strings.TrimSpace(req.Edition), add, designate)
		if err == nil {
			res.Edition, res.Languages = pkg.module, pkg.languages
			if version == "" {
				version = "http://snomed.info/sct/" + pkg.module + "/version/" + pkg.date
			}
			if req.Name == "" {
				if e, ok := EditionFor(pkg.module); ok {
					name = "SNOMED CT " + e.Name
				} else {
					name = "SNOMED CT (module " + pkg.module + ")"
				}
			}
		}
	case FormatClaML:
		var info ClaMLInfo
		info, err = readClaML(r, req.Blocks, add)
		res.Derived = info.Derived
		if version == "" {
			version = info.Version
		}
	case FormatICD11:
		err = readICD11Tabulation(r, req.Blocks, add)
	}
	if err != nil {
		return ImportResult{}, err
	}
	if res.Concepts == 0 {
		return ImportResult{}, fmt.Errorf("no codes were found: is this the %s file?", describeFormat(req.Format))
	}
	if version == "" {
		version = time.Now().UTC().Format("2006-01-02")
	}
	res.Version = version
	for _, q := range []string{`DELETE FROM term_concepts WHERE system = ? AND version = ?`, `DELETE FROM term_designations WHERE system = ? AND version = ?`} {
		if _, err := tx.ExecContext(ctx, q, system, version); err != nil {
			return ImportResult{}, err
		}
	}
	for _, q := range []string{`UPDATE term_concepts SET version = ? WHERE system = ? AND version = ?`, `UPDATE term_designations SET version = ? WHERE system = ? AND version = ?`} {
		if _, err := tx.ExecContext(ctx, q, version, system, working); err != nil {
			return ImportResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO term_systems (system, version, name, format, concepts, imported_at,
		imported_by, licence, effective_from) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (system, version) DO UPDATE SET name = excluded.name, format = excluded.format, concepts = excluded.concepts,
		imported_at = excluded.imported_at, imported_by = excluded.imported_by, licence = excluded.licence,
		effective_from = excluded.effective_from`, system, version, name, req.Format, res.Concepts,
		time.Now().UTC().Format(time.RFC3339), req.By, req.Licence, effective); err != nil {
		return ImportResult{}, err
	}
	return res, tx.Commit()
}

var clamlTitle = regexp.MustCompile(`<Title[^>]*\sname="([^"]+)"`)

func describeFormat(f string) string {
	return map[string]string{
		FormatLOINC:     "Loinc.csv (LoincTable folder of the LOINC release)",
		FormatICD10CM:   "icd10cm_order_YYYY.txt or icd10cm_codes_YYYY.txt from CMS",
		FormatRxNorm:    "RXNCONSO.RRF (rrf folder of the RxNorm full release)",
		FormatSNOMED:    "sct2_Description_Snapshot-en_*.txt (Snapshot/Terminology of the RF2 release)",
		FormatCSV:       "CSV with a header row naming the code and display columns",
		FormatSNOMEDRF2: "SNOMED CT RF2 release package (the zip as published)",
		FormatClaML:     "ClaML XML file (the classification's Systematik/ClaML download)",
		FormatICD11:     "ICD-11 MMS simple tabulation (tab-separated, with Code and Title columns)",
	}[f]
}

// csvOptions say how a delimited file is written.
type csvOptions struct {
	delimiter, encoding string
	noHeader            bool
}

// readCSV calls row for each line after the header, with a column getter by name (case-insensitive). The required columns
// must be in the header. Without a header, columns are named by number from 1.
func readCSV(r io.Reader, o csvOptions, required []string, row func(get func(string) string) error) error {
	switch strings.ToLower(strings.TrimSpace(o.encoding)) {
	case "", "utf-8", "utf8":
	case "iso-8859-1", "latin1", "latin-1":
		r = charmap.ISO8859_1.NewDecoder().Reader(r)
	case "iso-8859-15", "latin9":
		r = charmap.ISO8859_15.NewDecoder().Reader(r)
	case "windows-1252", "cp1252":
		r = charmap.Windows1252.NewDecoder().Reader(r)
	default:
		return fmt.Errorf("encoding %q is not read here: use utf-8, iso-8859-1, iso-8859-15 or windows-1252", o.encoding)
	}
	br := bufio.NewReaderSize(r, 1<<20)
	// Excel and the LOINC release both write a byte-order mark, which would otherwise become part of the first column's name.
	if b, err := br.Peek(3); err == nil && string(b) == "\ufeff" {
		_, _ = br.Discard(3)
	}
	cr := csv.NewReader(br)
	switch o.delimiter {
	case "", ",":
	case ";", "|":
		cr.Comma = rune(o.delimiter[0])
	case "tab", "\t", "\\t":
		cr.Comma = '\t'
	default:
		return fmt.Errorf("delimiter %q is not one of , ; | or tab", o.delimiter)
	}
	cr.LazyQuotes = true
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	cols := map[string]int{}
	var first []string
	if o.noHeader {
		rec, err := cr.Read()
		if err != nil {
			return fmt.Errorf("reading the first row: %w", err)
		}
		first = append([]string(nil), rec...)
		for i := range first {
			cols[strconv.Itoa(i+1)] = i
		}
	} else {
		header, err := cr.Read()
		if err != nil {
			return fmt.Errorf("reading the header row: %w", err)
		}
		for i, h := range header {
			cols[strings.ToLower(strings.TrimSpace(h))] = i
		}
		for _, c := range required {
			if _, ok := cols[strings.ToLower(c)]; !ok {
				return fmt.Errorf("the header has no %s column (it has %s)", c, strings.Join(header, ", "))
			}
		}
	}
	line := 1
	for {
		var rec []string
		if first != nil {
			rec, first = first, nil
		} else {
			var err error
			rec, err = cr.Read()
			if err == io.EOF {
				return nil
			}
			line++
			if err != nil {
				return fmt.Errorf("line %d: %w", line, err)
			}
		}
		get := func(name string) string {
			if i, ok := cols[strings.ToLower(name)]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		if err := row(get); err != nil {
			return err
		}
	}
}

var icdOrder = regexp.MustCompile(`^\d{5} [A-Z0-9]{3,7} *[01] `)

// readICD10CM reads CMS's order file (order, code, billable flag, short and long description) or its codes file (code, then
// description).
func readICD10CM(r io.Reader, add func(code, display string, active bool) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), "\r ")
		if l == "" {
			continue
		}
		// The order file is fixed width: order number 1-5, code 7-13, billable flag 15, short description 17-76 and the
		// long description from 78.
		if icdOrder.MatchString(l) && len(l) > 16 {
			code := strings.TrimSpace(l[6:min(13, len(l))])
			display := ""
			if len(l) > 77 {
				display = strings.TrimSpace(l[77:])
			}
			if display == "" {
				display = strings.TrimSpace(l[16:min(76, len(l))])
			}
			if err := add(code, display, true); err != nil {
				return err
			}
			continue
		}
		f := strings.Fields(l)
		if len(f) < 2 || len(f[0]) < 3 || len(f[0]) > 8 {
			continue
		}
		if err := add(f[0], strings.TrimSpace(l[len(f[0]):]), true); err != nil {
			return err
		}
	}
	return sc.Err()
}

// rxPreference ranks RxNorm term types for the display: clinical and branded drugs first, then ingredients and brands.
var rxPreference = map[string]int{"SCD": 1, "SBD": 2, "GPCK": 3, "BPCK": 4, "SCDC": 5, "SCDF": 6, "SBDF": 7, "SCDG": 8,
	"SBDG": 9, "IN": 10, "PIN": 11, "MIN": 12, "BN": 13, "DF": 14, "DFG": 15, "SY": 50, "TMSY": 51, "PSN": 0}

// readRxNorm reads RXNCONSO.RRF: one concept per RXCUI from the RXNORM source, named by its most useful term type.
func readRxNorm(r io.Reader, add func(code, display string, active bool) error) error {
	type pick struct {
		rank    int
		display string
		active  bool
	}
	best := map[string]pick{}
	var order []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "|")
		// RXCUI|LAT|TS|LUI|STT|SUI|ISPREF|RXAUI|SAUI|SCUI|SDUI|SAB|TTY|CODE|STR|SRL|SUPPRESS|CVF
		if len(f) < 17 || f[11] != "RXNORM" {
			continue
		}
		rank, known := rxPreference[f[12]]
		if !known {
			rank = 99
		}
		active := f[16] != "O" && f[16] != "Y" && f[16] != "E"
		if !active {
			rank += 100
		}
		cui := f[0]
		if p, seen := best[cui]; !seen {
			order = append(order, cui)
			best[cui] = pick{rank, f[14], active}
		} else if rank < p.rank {
			best[cui] = pick{rank, f[14], active}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	for _, cui := range order {
		p := best[cui]
		if err := add(cui, p.display, p.active); err != nil {
			return err
		}
	}
	return nil
}

const (
	snomedFSN     = "900000000000003001"
	snomedSynonym = "900000000000013009"
)

// readSNOMED reads an RF2 description file: one concept per conceptId, named by its active fully specified name (or, without
// one, its first active synonym). Concept status is not in this file, so a concept counts as active when it has an active
// description.
func readSNOMED(r io.Reader, add func(code, display string, active bool) error) error {
	type pick struct {
		display string
		fsn     bool
		active  bool
	}
	best := map[string]pick{}
	var order []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	header := true
	for sc.Scan() {
		f := strings.Split(strings.TrimRight(sc.Text(), "\r"), "\t")
		if header {
			header = false
			if len(f) < 9 || f[4] != "conceptId" || f[7] != "term" {
				return errors.New("this is not an RF2 description file: the header should be id, effectiveTime, active, " +
					"moduleId, conceptId, languageCode, typeId, term, caseSignificanceId")
			}
			continue
		}
		if len(f) < 9 {
			continue
		}
		active, cid, typ, term := f[2] == "1", f[4], f[6], f[7]
		if typ != snomedFSN && typ != snomedSynonym {
			continue
		}
		p, seen := best[cid]
		if !seen {
			order = append(order, cid)
		}
		cand := pick{term, typ == snomedFSN, active}
		switch {
		case !seen:
			best[cid] = cand
		case cand.active && !p.active:
			best[cid] = cand
		case cand.active == p.active && cand.fsn && !p.fsn:
			best[cid] = cand
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	for _, cid := range order {
		p := best[cid]
		if err := add(cid, p.display, p.active); err != nil {
			return err
		}
	}
	return nil
}
