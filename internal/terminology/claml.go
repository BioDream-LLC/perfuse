package terminology

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/text/encoding/charmap"
)

// ClaML, the Classification Markup Language (EN 14463), is how the WHO publishes ICD-10, how Germany's BfArM publishes
// ICD-10-GM and OPS, and how France's ATIH publishes CIM-10 FR. A <Class> is a chapter, block or category; its title is
// the Label of its preferred Rubric. Some categories are completed by modifiers (a fifth character for the site of a
// fracture, say): a <ModifiedBy> on the class or an ancestor names a <Modifier>, whose <ModifierClass> elements are the
// characters it adds, and the codes they make are imported too unless ExcludeModifier says otherwise.

type clamlClass struct {
	code, kind, title, super string
	modifiedBy               []clamlModRef
	exclude                  map[string]bool
	hasCategoryChild         bool
	usage                    string
}

type clamlModRef struct {
	modifier string
	valid    map[string]bool // ValidModifierClass codes when all="false"
}

type clamlModClass struct {
	code, title string
}

// ClaMLInfo is what the file says about itself.
type ClaMLInfo struct {
	Name, Version string
	Derived       int // codes made by modifiers
}

// readClaML reads every category (and, with blocks, every block and chapter) of a ClaML file.
func readClaML(r io.Reader, blocks bool, add func(code, display string, active bool) error) (ClaMLInfo, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.CharsetReader = func(label string, in io.Reader) (io.Reader, error) {
		switch strings.ToLower(strings.TrimSpace(label)) {
		case "iso-8859-1", "iso8859-1", "latin1", "latin-1":
			return charmap.ISO8859_1.NewDecoder().Reader(in), nil
		case "iso-8859-15", "iso8859-15", "latin9":
			return charmap.ISO8859_15.NewDecoder().Reader(in), nil
		case "windows-1252", "cp1252":
			return charmap.Windows1252.NewDecoder().Reader(in), nil
		case "utf-8", "utf8", "":
			return in, nil
		}
		return nil, fmt.Errorf("the file is in %s, which is not read here: save it as UTF-8 or ISO-8859-1", label)
	}
	var info ClaMLInfo
	classes := map[string]*clamlClass{}
	var order []string
	modClasses := map[string][]clamlModClass{} // modifier -> its classes, in file order
	sawClaML := false

	// labelText reads a Rubric's Label: its text, with any nested elements' text, spaces collapsed.
	labelText := func(start xml.StartElement) (string, error) {
		var b strings.Builder
		depth := 1
		for depth > 0 {
			tok, err := dec.Token()
			if err != nil {
				return "", err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				depth++
			case xml.EndElement:
				depth--
			case xml.CharData:
				b.Write(t)
			}
		}
		return strings.Join(strings.Fields(b.String()), " "), nil
	}
	// readRubrics reads a Class or ModifierClass body until its end, returning the preferred title and the elements met.
	type body struct {
		title      string
		super      string
		subs       []string
		modifiedBy []clamlModRef
		exclude    map[string]bool
	}
	readBody := func(end string) (body, error) {
		var bd body
		bd.exclude = map[string]bool{}
		inPreferred := false
		var cur *clamlModRef
		for {
			tok, err := dec.Token()
			if err != nil {
				return bd, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				attr := func(n string) string {
					for _, a := range t.Attr {
						if a.Name.Local == n {
							return a.Value
						}
					}
					return ""
				}
				switch t.Name.Local {
				case "SuperClass":
					bd.super = attr("code")
				case "SubClass":
					bd.subs = append(bd.subs, attr("code"))
				case "ModifiedBy":
					bd.modifiedBy = append(bd.modifiedBy, clamlModRef{modifier: attr("code")})
					cur = &bd.modifiedBy[len(bd.modifiedBy)-1]
					if attr("all") == "false" {
						cur.valid = map[string]bool{}
					}
				case "ValidModifierClass":
					if cur != nil && cur.valid != nil {
						cur.valid[attr("code")] = true
					}
				case "ExcludeModifier":
					bd.exclude[attr("code")] = true
				case "Rubric":
					inPreferred = attr("kind") == "preferred"
				case "Label":
					if inPreferred && bd.title == "" {
						txt, err := labelText(t)
						if err != nil {
							return bd, err
						}
						bd.title = txt
					}
				}
			case xml.EndElement:
				switch t.Name.Local {
				case end:
					return bd, nil
				case "Rubric":
					inPreferred = false
				case "ModifiedBy":
					cur = nil
				}
			}
		}
	}

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return info, fmt.Errorf("reading the ClaML file: %w", err)
		}
		st, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		attr := func(n string) string {
			for _, a := range st.Attr {
				if a.Name.Local == n {
					return a.Value
				}
			}
			return ""
		}
		switch st.Name.Local {
		case "ClaML":
			sawClaML = true
		case "Title":
			info.Name, info.Version = attr("name"), attr("version")
		case "ModifierClass":
			mod, code := attr("modifier"), attr("code")
			bd, err := readBody("ModifierClass")
			if err != nil {
				return info, err
			}
			modClasses[mod] = append(modClasses[mod], clamlModClass{code, bd.title})
		case "Class":
			code, kind := attr("code"), attr("kind")
			bd, err := readBody("Class")
			if err != nil {
				return info, err
			}
			if code == "" {
				continue
			}
			c := &clamlClass{code: code, kind: kind, title: bd.title, super: bd.super, modifiedBy: bd.modifiedBy, exclude: bd.exclude,
				usage: attr("usage")}
			classes[code] = c
			order = append(order, code)
		}
	}
	if !sawClaML {
		return info, errors.New("this is not a ClaML file: its root element should be ClaML")
	}
	for _, c := range classes {
		if p := classes[c.super]; p != nil && c.kind == "category" {
			p.hasCategoryChild = true
		}
	}
	for _, code := range order {
		c := classes[code]
		if c.kind != "category" && !blocks {
			continue
		}
		if err := add(c.code, c.title, true); err != nil {
			return info, err
		}
		if c.kind != "category" || c.hasCategoryChild {
			continue
		}
		// Modifiers on the class and its ancestors, the outermost first, each excluded where the class says so.
		var chain []*clamlClass
		for p := c; p != nil; p = classes[p.super] {
			chain = append([]*clamlClass{p}, chain...)
			if len(chain) > 20 {
				break
			}
		}
		var mods []clamlModRef
		seen := map[string]bool{}
		for _, p := range chain {
			for _, m := range p.modifiedBy {
				if !seen[m.modifier] && !c.exclude[m.modifier] {
					seen[m.modifier] = true
					mods = append(mods, m)
				}
			}
		}
		if len(mods) == 0 {
			continue
		}
		combos := []clamlModClass{{"", ""}}
		for _, m := range mods {
			var next []clamlModClass
			for _, base := range combos {
				for _, mc := range modClasses[m.modifier] {
					if m.valid != nil && !m.valid[mc.code] {
						continue
					}
					t := base.title
					if mc.title != "" {
						if t != "" {
							t += ", "
						}
						t += mc.title
					}
					next = append(next, clamlModClass{base.code + mc.code, t})
				}
			}
			if len(next) > 0 {
				combos = next
			}
		}
		for _, combo := range combos {
			if combo.code == "" {
				continue
			}
			full := joinModCode(c.code, combo.code)
			if _, explicit := classes[full]; explicit {
				continue
			}
			title := c.title
			if combo.title != "" {
				title += ": " + combo.title
			}
			if err := add(full, title, true); err != nil {
				return info, err
			}
			info.Derived++
		}
	}
	return info, nil
}

// joinModCode puts a modifier's characters after a class code: "S02.0" and "0" make S02.00; "M00" and ".0" make M00.0.
func joinModCode(code, add string) string {
	if strings.Contains(code, ".") {
		add = strings.TrimPrefix(add, ".")
	}
	return code + add
}

// clamlSystem names the code system a ClaML file is, from its Title, when it is one Perfuse knows.
func clamlSystem(name string) (system, label string) {
	n := strings.ToUpper(strings.ReplaceAll(name, " ", ""))
	switch {
	case strings.Contains(n, "ICD-10-GM") || strings.Contains(n, "ICD10GM"):
		return SystemICD10GM, "ICD-10-GM"
	case strings.HasPrefix(n, "OPS"):
		return SystemOPS, "OPS"
	case strings.Contains(n, "CIM-10") || strings.Contains(n, "CIM10"):
		return SystemCIM10FR, "CIM-10 FR"
	case n == "ICD-10" || n == "ICD10" || strings.HasPrefix(n, "ICD-10-WHO") || strings.HasPrefix(n, "ICD10WHO"):
		return SystemICD10, "ICD-10 (WHO)"
	}
	return "", ""
}
