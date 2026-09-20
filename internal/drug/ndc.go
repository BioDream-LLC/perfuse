// Package drug handles the codes and quantities that identify a medicine.
//
// Split out from the pharmacy standards that carry them, because the same drug code appears in an
// NCPDP claim, a SCRIPT prescription, an HL7 RXE segment and a FHIR MedicationRequest, and a
// conversion that lives in one of those places is a conversion the other three do without.
package drug

import (
	"fmt"
	"strings"
)

// ──────────────────────────────────────────────────────────────────────────────
// National Drug Code
// ──────────────────────────────────────────────────────────────────────────────

// Format is how an NDC's ten digits are divided into labeler, product and package.
//
// The division is not derivable from the digits. A labeler is assigned one configuration when it
// registers, and every code it issues uses that one - so the same ten digits mean different medicines
// depending on whose they are.
type Format string

const (
	// Format442 is four labeler digits, four product, two package. The oldest assignment.
	Format442 Format = "4-4-2"
	// Format532 is five labeler digits, three product, two package.
	Format532 Format = "5-3-2"
	// Format541 is five labeler digits, four product, one package.
	Format541 Format = "5-4-1"
	// Format542 is the eleven-digit form used for billing. Not an assignment, a padding of one of the above.
	Format542 Format = "5-4-2"
)

// NDC is a parsed National Drug Code.
type NDC struct {
	// Labeler identifies the manufacturer or repackager, assigned by the FDA.
	Labeler string
	// Product identifies the drug, strength and dosage form within that labeler.
	Product string
	// Package identifies the package size.
	Package string
	// Format is the configuration the code was written in.
	Format Format
	// Padded records that an eleven-digit form was produced by inserting a zero.
	//
	// Kept because the two are not interchangeable everywhere: the FDA's own directory lists the
	// ten-digit assignment, while a claim must carry eleven. Somebody comparing a billed code against
	// a catalogue needs to know which they are holding.
	Padded bool
}

// String returns the hyphenated form, which is the only form that is unambiguous on its own.
func (n NDC) String() string {
	return n.Labeler + "-" + n.Product + "-" + n.Package
}

// Digits returns the code without hyphens.
func (n NDC) Digits() string {
	return n.Labeler + n.Product + n.Package
}

// Eleven returns the eleven-digit 5-4-2 form used for billing.
//
// This is what NCPDP calls the Product/Service ID when the qualifier is 03, and what a pharmacy claim
// must carry. The zero is inserted into the segment that is short, which is why the segments have to be
// known separately rather than the digits being padded on the left.
func (n NDC) Eleven() NDC {
	out := NDC{
		Labeler: pad(n.Labeler, 5),
		Product: pad(n.Product, 4),
		Package: pad(n.Package, 2),
		Format:  Format542,
		Padded:  n.Format != Format542,
	}
	return out
}

func pad(s string, n int) string {
	for len(s) < n {
		s = "0" + s
	}
	return s
}

// ParseNDC reads a hyphenated NDC.
//
// Hyphenated only, deliberately. Ten digits with the hyphens removed cannot be divided back up: 12345
// 678 90 and 1234 5678 90 and 12345 6789 0 are all valid readings of the same ten characters, and they
// are three different medicines. Every one of them normalises to a different eleven-digit code, so a
// guess does not fail - it bills for the wrong drug, and the claim is paid.
//
// An eleven-digit unhyphenated code is accepted, because eleven digits are only ever 5-4-2.
//
// Use ParseNDCWithFormat when the labeler's configuration is known from somewhere else, which is the
// only honest way to read an unhyphenated ten.
func ParseNDC(s string) (NDC, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return NDC{}, fmt.Errorf("no NDC given")
	}

	if strings.Contains(s, "-") {
		parts := strings.Split(s, "-")
		if len(parts) != 3 {
			return NDC{}, fmt.Errorf("NDC %q has %d hyphen-separated parts, and an NDC has three: labeler, product and package", s, len(parts))
		}
		for i, p := range parts {
			if p == "" {
				return NDC{}, fmt.Errorf("NDC %q has an empty part %d", s, i+1)
			}
			if !allDigits(p) {
				return NDC{}, fmt.Errorf("NDC %q contains something that is not a digit", s)
			}
		}
		f, err := formatOf(len(parts[0]), len(parts[1]), len(parts[2]))
		if err != nil {
			return NDC{}, fmt.Errorf("NDC %q: %w", s, err)
		}
		return NDC{Labeler: parts[0], Product: parts[1], Package: parts[2], Format: f}, nil
	}

	if !allDigits(s) {
		return NDC{}, fmt.Errorf("NDC %q contains something that is not a digit", s)
	}

	switch len(s) {
	case 11:
		// Eleven digits are 5-4-2 and nothing else, so this one division is safe.
		return NDC{Labeler: s[0:5], Product: s[5:9], Package: s[9:11], Format: Format542}, nil
	case 10:
		return NDC{}, fmt.Errorf(
			"NDC %q is ten digits with no hyphens, which does not say where the labeler ends: it could be %s, %s or %s, "+
				"and those are three different medicines that each bill under a different eleven-digit code. "+
				"Supply the hyphens, or say which format the labeler uses",
			s, Format442, Format532, Format541)
	default:
		return NDC{}, fmt.Errorf("NDC %q is %d digits; an NDC is ten as assigned or eleven as billed", s, len(s))
	}
}

// ParseNDCWithFormat reads an unhyphenated ten-digit NDC whose configuration is known.
//
// The format has to come from the labeler, not from the code. Passing a guess here is the same mistake
// as guessing in ParseNDC, only harder to find later.
func ParseNDCWithFormat(s string, f Format) (NDC, error) {
	s = strings.TrimSpace(s)
	if !allDigits(s) {
		return NDC{}, fmt.Errorf("NDC %q contains something that is not a digit", s)
	}
	if len(s) != 10 {
		return NDC{}, fmt.Errorf("NDC %q is %d digits; a format only resolves a ten-digit code", s, len(s))
	}
	switch f {
	case Format442:
		return NDC{Labeler: s[0:4], Product: s[4:8], Package: s[8:10], Format: f}, nil
	case Format532:
		return NDC{Labeler: s[0:5], Product: s[5:8], Package: s[8:10], Format: f}, nil
	case Format541:
		return NDC{Labeler: s[0:5], Product: s[5:9], Package: s[9:10], Format: f}, nil
	case Format542:
		return NDC{}, fmt.Errorf("%s is the eleven-digit billing form and cannot divide ten digits", Format542)
	default:
		return NDC{}, fmt.Errorf("unknown NDC format %q; use %s, %s or %s", f, Format442, Format532, Format541)
	}
}

func formatOf(a, b, c int) (Format, error) {
	switch {
	case a == 4 && b == 4 && c == 2:
		return Format442, nil
	case a == 5 && b == 3 && c == 2:
		return Format532, nil
	case a == 5 && b == 4 && c == 1:
		return Format541, nil
	case a == 5 && b == 4 && c == 2:
		return Format542, nil
	default:
		return "", fmt.Errorf("segments of %d-%d-%d are not an NDC; the assigned forms are %s, %s and %s, and the billed form is %s",
			a, b, c, Format442, Format532, Format541, Format542)
	}
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
