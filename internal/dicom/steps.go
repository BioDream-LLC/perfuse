package dicom

import (
	"fmt"
	"strings"
)

// Step is one named DICOM transformation.
//
// # Why named steps and not a path writer
//
// Every other format in this project gets a general accessor: name a path, set a value. DICOM deliberately does not, and the
// reason is what a DICOM object is. It is not a message with fields, it is an image with metadata attached, and the two are
// entangled in ways a general writer cannot know about:
//
//   - Pixel data is an element like any other. A path writer that can set (7FE0,0010) can replace an image with a string.
//   - Lengths, offsets and group lengths are elements too. Writing one without recomputing the rest produces a file that
//     parses and is wrong, which is worse than one that fails to parse.
//   - Several identifiers must agree across a study. Changing StudyInstanceUID on one instance and not its siblings splits a
//     study in the archive, and nothing rejects it at the time.
//   - VR constrains what a value may be. A general writer either validates against a dictionary or produces invalid objects.
//
// So the vocabulary here is the set of things sites actually need, each of which knows its own invariants. That is a smaller
// promise, and it is one that can be kept. If a site needs something absent from this list, adding a named step is the correct
// answer, and it forces somebody to think about the invariants once rather than every caller thinking about them never.
type Step struct {
	// Description is free text, carried into the log so a change in an audit trail says why.
	Description string `yaml:"description,omitempty"`

	// Deidentify removes patient identity, per the Basic Application Level Confidentiality Profile.
	Deidentify *DeidentifyStep `yaml:"deidentify,omitempty"`

	// SetAETitle rewrites a calling or called AE title.
	SetAETitle *AETitleStep `yaml:"set_ae_title,omitempty"`

	// StripPrivate removes private tags: odd-numbered groups, whose meaning is vendor-specific.
	StripPrivate *StripPrivateStep `yaml:"strip_private,omitempty"`

	// SetInstitution rewrites the institution name and address.
	SetInstitution *InstitutionStep `yaml:"set_institution,omitempty"`
}

// DeidentifyStep removes patient identity from an object.
//
// # What it does and does not claim
//
// This implements the tag removals of DICOM PS3.15 Annex E's Basic Application Level Confidentiality Profile: the identifying
// attributes are removed or emptied, and the dates can optionally be kept because a research use frequently needs the interval
// between studies even when it must not know the patient.
//
// It does not claim to produce an object safe to publish, and the difference matters enough to say plainly. Identity survives
// in places tag removal cannot reach:
//
//   - Burned-in annotation. A screenshot or an ultrasound frame can have the patient's name rendered into the pixels.
//     BurnedInAnnotation says whether the sender thinks so and is frequently absent or wrong.
//   - Private tags, unless StripPrivate is also used. Vendors store identity there.
//   - Structured reports and overlays, whose text is content rather than metadata.
//   - The pixels themselves. A face is reconstructable from a head CT.
//
// So this is a necessary step and not a sufficient one, and a site publishing data needs a human review as well. Saying so here
// is the point: a de-identification feature that quietly implies more than it does is worse than none, because somebody will
// trust it.
type DeidentifyStep struct {
	// KeepDates leaves study, series and acquisition dates in place.
	//
	// Off by default, because a date is identifying in combination with other things - a rare procedure on a known date
	// identifies a person. On when the research question needs intervals.
	KeepDates bool `yaml:"keep_dates,omitempty"`

	// PatientID replaces the patient identifier rather than emptying it.
	//
	// Emptying is the profile's behaviour and it makes objects hard to group: every de-identified study looks like the same
	// unnamed patient. A pseudonym keeps a cohort separable, and supplying it here rather than deriving one is deliberate -
	// a derived pseudonym would be reproducible from the original identifier, which is what a pseudonym must not be.
	PatientID string `yaml:"patient_id,omitempty"`

	// PatientName replaces the name. Defaults to the same value as PatientID so the two agree, because a viewer showing an
	// empty name beside a populated identifier looks broken and invites somebody to go looking for the real one.
	PatientName string `yaml:"patient_name,omitempty"`
}

// AETitleStep rewrites an application entity title.
type AETitleStep struct {
	// Calling and Called are the source and destination AE titles.
	//
	// Both optional: a channel frequently rewrites only one. An AE title is sixteen characters of uppercase in practice,
	// and a longer one is refused at load rather than truncated, because a truncated title reaches a different node than
	// the one written down.
	Calling string `yaml:"calling,omitempty"`
	Called  string `yaml:"called,omitempty"`
}

// StripPrivateStep removes private tags.
//
// Private tags are odd-numbered groups, whose meaning is defined by the vendor rather than the standard. They routinely carry
// identity, and they routinely carry the only copy of something clinically important - a reconstruction parameter, a dose
// record - so removing them is safe for a research export and destructive for an archive.
type StripPrivateStep struct {
	// Keep names tags to preserve, as group,element pairs.
	//
	// Exists because the honest answer to "is this private tag important" is site-specific. A site that knows its scanner
	// writes dose into (0029,1010) can keep it while removing the rest.
	Keep []string `yaml:"keep,omitempty"`
}

// InstitutionStep rewrites where the study was performed.
type InstitutionStep struct {
	Name    string `yaml:"name,omitempty"`
	Address string `yaml:"address,omitempty"`

	// Department is InstitutionalDepartmentName, separate because a site frequently rewrites the institution and keeps the
	// department, or the reverse.
	Department string `yaml:"department,omitempty"`
}

// Validate checks a step at load time.
func (s Step) Validate() error {
	set := 0
	for _, present := range []bool{
		s.Deidentify != nil, s.SetAETitle != nil, s.StripPrivate != nil, s.SetInstitution != nil,
	} {
		if present {
			set++
		}
	}

	if set == 0 {
		return fmt.Errorf("this step does nothing: name one of deidentify, set_ae_title, strip_private or set_institution")
	}
	if set > 1 {
		// Refused rather than ordered, because the order would be this struct's field order, which is arbitrary and
		// invisible in the yaml. Two steps make the order explicit.
		return fmt.Errorf("this step names more than one action; write them as separate steps so the order is visible")
	}

	if s.SetAETitle != nil {
		if s.SetAETitle.Calling == "" && s.SetAETitle.Called == "" {
			return fmt.Errorf("set_ae_title needs calling or called")
		}
		for name, title := range map[string]string{"calling": s.SetAETitle.Calling, "called": s.SetAETitle.Called} {
			if err := checkAETitle(name, title); err != nil {
				return err
			}
		}
	}

	if s.StripPrivate != nil {
		for _, raw := range s.StripPrivate.Keep {
			if _, err := ParseTag(raw); err != nil {
				return fmt.Errorf("strip_private.keep: %w", err)
			}
		}
	}

	if s.SetInstitution != nil {
		i := s.SetInstitution
		if i.Name == "" && i.Address == "" && i.Department == "" {
			return fmt.Errorf("set_institution needs name, address or department")
		}
	}

	return nil
}

// checkAETitle refuses a title that cannot be sent as written.
func checkAETitle(field, title string) error {
	if title == "" {
		return nil
	}
	if len(title) > 16 {
		// Refused rather than truncated. A truncated AE title reaches a different node than the one written down, and the
		// failure appears as images going to the wrong place rather than as a configuration error.
		return fmt.Errorf("set_ae_title.%s is %d characters and an AE title is at most 16", field, len(title))
	}
	if strings.TrimSpace(title) != title {
		return fmt.Errorf("set_ae_title.%s has leading or trailing space, which is not preserved on the wire", field)
	}
	for _, r := range title {
		// The standard's AE title is a subset of ASCII without control characters or backslash, the latter being a value
		// separator. A title containing one would split into two on the wire.
		if r < 0x20 || r > 0x7E || r == '\\' {
			return fmt.Errorf("set_ae_title.%s contains a character that cannot be sent in an AE title: %q", field, r)
		}
	}

	return nil
}

// ParseTag reads a tag written as group,element in hexadecimal.
//
// Both bare and parenthesised forms, because the standard prints (0010,0010) and every tool accepts 0010,0010.
func ParseTag(raw string) (Tag, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")

	group, element, ok := strings.Cut(s, ",")
	if !ok {
		return Tag{}, fmt.Errorf("%q is not a tag: write it as group,element in hexadecimal, for example 0010,0010", raw)
	}

	g, err := parseHex16(group)
	if err != nil {
		return Tag{}, fmt.Errorf("%q is not a tag: the group %q is not four hexadecimal digits", raw, group)
	}
	e, err := parseHex16(element)
	if err != nil {
		return Tag{}, fmt.Errorf("%q is not a tag: the element %q is not four hexadecimal digits", raw, element)
	}

	return Tag{Group: g, Element: e}, nil
}

func parseHex16(s string) (uint16, error) {
	s = strings.TrimSpace(s)
	if len(s) != 4 {
		return 0, fmt.Errorf("want four digits, got %d", len(s))
	}
	var out uint16
	for _, r := range s {
		var d uint16
		switch {
		case r >= '0' && r <= '9':
			d = uint16(r - '0')
		case r >= 'a' && r <= 'f':
			d = uint16(r-'a') + 10
		case r >= 'A' && r <= 'F':
			d = uint16(r-'A') + 10
		default:
			return 0, fmt.Errorf("%q is not a hexadecimal digit", r)
		}
		out = out<<4 | d
	}

	return out, nil
}
