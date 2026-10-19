package terminology

import (
	"archive/zip"
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

// SNOMED CT editions and the RF2 release package.
//
// A national release centre publishes its edition as one zip: the International content its edition includes, its own
// extension module, descriptions in its own language, and language reference sets saying which description is the
// preferred term in each dialect. Importing the whole package (rather than one description file) gives the edition's
// version URI, each concept's real status, and its preferred term in the country's language as well as English.

// Edition is a SNOMED CT edition, by the module that names it.
type Edition struct {
	Module  string `json:"module"`
	Country string `json:"country"` // ISO 3166 alpha-2; "" for the International edition
	Name    string `json:"name"`
	// Verified says the module ID was checked against a published SNOMED or national release centre source; the others are
	// widely used values a site should confirm against its release notes.
	Verified bool `json:"verified"`
}

// Editions are the editions Perfuse can name. An edition not here still imports; its version URI then carries the module
// found in the package, and the console asks for its name.
var Editions = []Edition{
	{"900000000000207008", "", "International", true},
	{"83821000000107", "GB", "UK Edition", true},
	{"11000274103", "DE", "German Edition", true},
	{"11000146104", "NL", "Netherlands Edition", true},
	{"45991000052106", "SE", "Swedish Edition", true},
	{"554471000005108", "DK", "Danish Edition", true},
	{"11000172109", "BE", "Belgian Edition", false},
	{"11000220105", "IE", "Irish Edition", false},
	{"51000202101", "NO", "Norwegian Edition", false},
	{"2011000195101", "CH", "Swiss Edition", false},
	{"11000181102", "EE", "Estonian Edition", false},
	{"449081005", "ES", "Spanish Edition (Spanish language module)", false},
}

// EditionFor finds an edition by module.
func EditionFor(module string) (Edition, bool) {
	for _, e := range Editions {
		if e.Module == module {
			return e, true
		}
	}
	return Edition{}, false
}

const (
	snomedPreferred = "900000000000548007"
	snomedIntModule = "900000000000207008"
	snomedGB        = "900000000000508004" // GB English language reference set
	snomedUS        = "900000000000509007" // US English language reference set
)

// Designation is one more name of a concept: in a language, as a use (fully specified name, preferred synonym).
type Designation struct {
	Language  string `json:"language"`
	Use       string `json:"use"` // fsn, preferred, synonym
	Value     string `json:"value"`
	Preferred bool   `json:"preferred"`
}

var (
	rf2Concept     = regexp.MustCompile(`(?i)(^|/)sct2_Concept_(Snapshot|Full)[^/]*\.txt$`)
	rf2Description = regexp.MustCompile(`(?i)(^|/)sct2_(Description|TextDefinition)_Snapshot-([a-z]{2}(?:-[a-z]{2})?)_[^/]*\.txt$`)
	rf2Language    = regexp.MustCompile(`(?i)(^|/)der2_cRefset_Language(Snapshot)-([a-z]{2}(?:-[a-z]{2})?)_[^/]*\.txt$`)
	rf2Date        = regexp.MustCompile(`_(\d{8})\.txt$`)
)

// rf2Result is what the release package gave, for Import.
type rf2Result struct {
	module, date string
	languages    []string
}

// readRF2Package reads a whole RF2 release zip: concepts with their status, every active description, and the language
// reference sets. The concept's display is its preferred term in lang (when the package has that language), else its
// preferred term in any English dialect, else its fully specified name. Every language's preferred term and FSN become
// designations.
func readRF2Package(r io.Reader, lang, wantModule string, add func(code, display string, active bool) error,
	designate func(code string, d Designation) error) (rf2Result, error) {
	tmp, err := os.CreateTemp("", "perfuse-rf2-*.zip")
	if err != nil {
		return rf2Result{}, err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	size, err := io.Copy(tmp, r)
	if err != nil {
		return rf2Result{}, err
	}
	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return rf2Result{}, errors.New("this is not a zip file: send the whole RF2 release package as the national release centre publishes it")
	}
	var conceptFile *zip.File
	var descFiles, langFiles []*zip.File
	for _, f := range zr.File {
		name := f.Name
		if !strings.Contains(strings.ToLower(name), "snapshot") {
			continue
		}
		switch {
		case rf2Concept.MatchString(name) && conceptFile == nil:
			conceptFile = f
		case rf2Description.MatchString(name) && strings.Contains(path.Base(name), "_Description_"):
			descFiles = append(descFiles, f)
		case rf2Language.MatchString(name):
			langFiles = append(langFiles, f)
		}
	}
	if conceptFile == nil || len(descFiles) == 0 {
		return rf2Result{}, errors.New("the package has no Snapshot concept and description files (sct2_Concept_Snapshot, sct2_Description_Snapshot-xx)")
	}

	// Concepts: status and module.
	type concept struct {
		active bool
		module string
	}
	concepts := map[string]concept{}
	modules := map[string]int{}
	var res rf2Result
	if m := rf2Date.FindStringSubmatch(conceptFile.Name); m != nil {
		res.date = m[1]
	}
	if err := eachRF2Row(conceptFile, []string{"id", "effectiveTime", "active", "moduleId"}, func(f []string) error {
		concepts[f[0]] = concept{f[2] == "1", f[3]}
		modules[f[3]]++
		if f[1] > res.date {
			res.date = f[1]
		}
		return nil
	}); err != nil {
		return res, err
	}
	res.module = wantModule
	if res.module == "" {
		res.module = detectEdition(modules)
	}

	// Language reference sets: which descriptions are preferred (or acceptable) in which dialect.
	type accept struct {
		refset    string
		preferred bool
	}
	accepted := map[string][]accept{}
	for _, f := range langFiles {
		if err := eachRF2Row(f, []string{"id", "effectiveTime", "active", "moduleId", "refsetId", "referencedComponentId", "acceptabilityId"}, func(r []string) error {
			if r[2] != "1" {
				return nil
			}
			accepted[r[5]] = append(accepted[r[5]], accept{r[4], r[6] == snomedPreferred})
			return nil
		}); err != nil {
			return res, err
		}
	}

	// Descriptions: per concept, per language, the FSN and the preferred synonym.
	type names struct {
		fsn, preferred map[string]string // language -> term
		gb, us         string            // the English preferred terms by dialect, which differ (haemoglobin, hemoglobin)
	}
	byConcept := map[string]*names{}
	var order []string
	langs := map[string]bool{}
	for _, f := range descFiles {
		if err := eachRF2Row(f, []string{"id", "effectiveTime", "active", "moduleId", "conceptId", "languageCode", "typeId", "term"}, func(r []string) error {
			if r[2] != "1" {
				return nil
			}
			cid, lc, typ, term := r[4], strings.ToLower(r[5]), r[6], r[7]
			n := byConcept[cid]
			if n == nil {
				n = &names{fsn: map[string]string{}, preferred: map[string]string{}}
				byConcept[cid] = n
				order = append(order, cid)
			}
			langs[lc] = true
			switch typ {
			case snomedFSN:
				if _, have := n.fsn[lc]; !have {
					n.fsn[lc] = term
				}
			case snomedSynonym:
				for _, a := range accepted[r[0]] {
					if !a.preferred {
						continue
					}
					switch a.refset {
					case snomedGB:
						n.gb = term
					case snomedUS:
						n.us = term
					}
					if _, have := n.preferred[lc]; !have || (lc == "en" && a.refset == snomedGB && lang == "en-gb") {
						n.preferred[lc] = term
					}
				}
			}
			return nil
		}); err != nil {
			return res, err
		}
	}
	for l := range langs {
		res.languages = append(res.languages, l)
	}
	sort.Strings(res.languages)
	lang = strings.ToLower(strings.TrimSpace(lang))
	for _, cid := range order {
		n := byConcept[cid]
		display := n.preferred[lang]
		switch {
		case lang == "en-gb" && n.gb != "":
			display = n.gb
		case lang == "en-us" && n.us != "":
			display = n.us
		}
		if display == "" {
			display = n.preferred[strings.SplitN(lang, "-", 2)[0]]
		}
		if display == "" {
			display = n.preferred["en"]
		}
		if display == "" {
			display = n.fsn["en"]
		}
		if display == "" {
			for _, l := range res.languages {
				if t := n.preferred[l]; t != "" {
					display = t
					break
				}
				if t := n.fsn[l]; t != "" {
					display = t
					break
				}
			}
		}
		c, known := concepts[cid]
		active := !known || c.active
		if err := add(cid, display, active); err != nil {
			return res, err
		}
		if n.gb != "" && n.us != "" && n.gb != n.us {
			for l, t := range map[string]string{"en-GB": n.gb, "en-US": n.us} {
				if err := designate(cid, Designation{Language: l, Use: "preferred", Value: t, Preferred: true}); err != nil {
					return res, err
				}
			}
		}
		for _, l := range res.languages {
			if t := n.preferred[l]; t != "" {
				if err := designate(cid, Designation{Language: l, Use: "preferred", Value: t, Preferred: true}); err != nil {
					return res, err
				}
			}
			if t := n.fsn[l]; t != "" {
				if err := designate(cid, Designation{Language: l, Use: "fsn", Value: t}); err != nil {
					return res, err
				}
			}
		}
	}
	return res, nil
}

// detectEdition names the edition a package is: the national module with the most concepts when there is one (a national
// edition carries the International content as well as its own), else International.
func detectEdition(modules map[string]int) string {
	best, n := "", 0
	for m, c := range modules {
		if _, known := EditionFor(m); known && m != snomedIntModule && c > n {
			best, n = m, c
		}
	}
	if best != "" {
		return best
	}
	// An extension module the list does not know: the non-International module with the most concepts. Core metadata modules
	// (900000000000012004) are part of the International edition.
	for m, c := range modules {
		if m != snomedIntModule && m != "900000000000012004" && c > n {
			best, n = m, c
		}
	}
	if best != "" {
		return best
	}
	return snomedIntModule
}

// eachRF2Row reads a tab-separated RF2 file, checking that its header begins with the columns named.
func eachRF2Row(f *zip.File, cols []string, row func([]string) error) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	header := true
	for sc.Scan() {
		fields := strings.Split(strings.TrimRight(sc.Text(), "\r"), "\t")
		if header {
			header = false
			fields[0] = strings.TrimPrefix(fields[0], "\ufeff")
			for i, c := range cols {
				if i >= len(fields) || fields[i] != c {
					return fmt.Errorf("%s is not an RF2 file of the kind its name says: its header should begin %s", path.Base(f.Name), strings.Join(cols, ", "))
				}
			}
			continue
		}
		if len(fields) < len(cols) {
			continue
		}
		if err := row(fields); err != nil {
			return err
		}
	}
	return sc.Err()
}
