package terminology

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Import formats.
const (
	FormatLOINC   = "loinc"   // Loinc.csv from the LOINC release (LOINC_NUM, LONG_COMMON_NAME, STATUS)
	FormatICD10CM = "icd10cm" // CMS's icd10cm_order_YYYY.txt or icd10cm_codes_YYYY.txt
	FormatRxNorm  = "rxnorm"  // RXNCONSO.RRF from the RxNorm full release
	FormatSNOMED  = "snomed"  // an RF2 sct2_Description_Snapshot file
	FormatCSV     = "csv"     // any CSV with a header: code and display columns (CPT, HCPCS, local codes)
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
}

// ImportResult says what was imported.
type ImportResult struct {
	System   string `json:"system"`
	Version  string `json:"version"`
	Concepts int    `json:"concepts"`
	Skipped  int    `json:"skipped"`
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
	}
	return "", "", false
}

// Import reads a publisher's file and replaces that version of the system with it, in one transaction: a failed import leaves
// the previous one in place.
func (s *Store) Import(ctx context.Context, req ImportRequest, r io.Reader) (ImportResult, error) {
	system, name, ok := Defaults(req.Format)
	if !ok {
		return ImportResult{}, fmt.Errorf("format %q is not known; use loinc, icd10cm, rxnorm, snomed or csv", req.Format)
	}
	if req.System != "" {
		system = strings.TrimSpace(req.System)
	}
	if req.Name != "" {
		name = strings.TrimSpace(req.Name)
	}
	if system == "" {
		return ImportResult{}, errors.New("a csv import needs the code system's URL (system), such as " + SystemCPT)
	}
	if !strings.Contains(system, ":") {
		return ImportResult{}, fmt.Errorf("system %q is not a URL or URN; code systems are named by URL, such as %s", system, SystemCPT)
	}
	if name == "" {
		name = system
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		version = time.Now().UTC().Format("2006-01-02")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM term_concepts WHERE system = ? AND version = ?`, system, version); err != nil {
		return ImportResult{}, err
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO term_concepts (system, version, code, display, active) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (system, version, code) DO UPDATE SET display = excluded.display, active = excluded.active`)
	if err != nil {
		return ImportResult{}, err
	}
	defer ins.Close()
	res := ImportResult{System: system, Version: version}
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
		_, err := ins.ExecContext(ctx, system, version, code, display, map[bool]int{true: 1, false: 0}[active])
		return err
	}

	switch req.Format {
	case FormatLOINC:
		err = readCSV(r, []string{"LOINC_NUM"}, func(get func(string) string) error {
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
		err = readCSV(r, []string{cc, dc}, func(get func(string) string) error {
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
	}
	if err != nil {
		return ImportResult{}, err
	}
	if res.Concepts == 0 {
		return ImportResult{}, fmt.Errorf("no codes were found: is this the %s file?", describeFormat(req.Format))
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO term_systems (system, version, name, format, concepts, imported_at,
		imported_by, licence) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (system, version) DO UPDATE SET name = excluded.name, format = excluded.format, concepts = excluded.concepts,
		imported_at = excluded.imported_at, imported_by = excluded.imported_by, licence = excluded.licence`, system, version, name, req.Format, res.Concepts,
		time.Now().UTC().Format(time.RFC3339), req.By, req.Licence); err != nil {
		return ImportResult{}, err
	}
	return res, tx.Commit()
}

func describeFormat(f string) string {
	return map[string]string{
		FormatLOINC:   "Loinc.csv (LoincTable folder of the LOINC release)",
		FormatICD10CM: "icd10cm_order_YYYY.txt or icd10cm_codes_YYYY.txt from CMS",
		FormatRxNorm:  "RXNCONSO.RRF (rrf folder of the RxNorm full release)",
		FormatSNOMED:  "sct2_Description_Snapshot-en_*.txt (Snapshot/Terminology of the RF2 release)",
		FormatCSV:     "CSV with a header row naming the code and display columns",
	}[f]
}

// readCSV calls row for each line after the header, with a column getter by name (case-insensitive). The required columns
// must be in the header.
func readCSV(r io.Reader, required []string, row func(get func(string) string) error) error {
	br := bufio.NewReaderSize(r, 1<<20)
	// Excel and the LOINC release both write a byte-order mark, which would otherwise become part of the first column's name.
	if b, err := br.Peek(3); err == nil && string(b) == "\ufeff" {
		_, _ = br.Discard(3)
	}
	cr := csv.NewReader(br)
	cr.LazyQuotes = true
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return fmt.Errorf("reading the header row: %w", err)
	}
	cols := map[string]int{}
	for i, h := range header {
		cols[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, c := range required {
		if _, ok := cols[strings.ToLower(c)]; !ok {
			return fmt.Errorf("the header has no %s column (it has %s)", c, strings.Join(header, ", "))
		}
	}
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return nil
		}
		line++
		if err != nil {
			return fmt.Errorf("line %d: %w", line, err)
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
