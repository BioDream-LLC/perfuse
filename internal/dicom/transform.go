package dicom

import (
	"fmt"
	"strings"
)

// Well-known tags these steps touch. Named rather than written inline, because 0010,0010 in a condition somewhere is
// unreadable and a mistyped digit is a silent change to a different attribute.
var (
	tagPatientName       = Tag{0x0010, 0x0010}
	tagPatientID         = Tag{0x0010, 0x0020}
	tagPatientBirthDate  = Tag{0x0010, 0x0030}
	tagPatientSex        = Tag{0x0010, 0x0040}
	tagPatientAge        = Tag{0x0010, 0x1010}
	tagPatientAddress    = Tag{0x0010, 0x1040}
	tagPatientPhone      = Tag{0x0010, 0x2154}
	tagOtherPatientIDs   = Tag{0x0010, 0x1000}
	tagOtherPatientNames = Tag{0x0010, 0x1001}
	tagPatientComments   = Tag{0x0010, 0x4000}
	tagEthnicGroup       = Tag{0x0010, 0x2160}
	tagOccupation        = Tag{0x0010, 0x2180}
	tagMedicalHistory    = Tag{0x0010, 0x21B0}

	tagReferringPhysician  = Tag{0x0008, 0x0090}
	tagPerformingPhysician = Tag{0x0008, 0x1050}
	tagOperatorName        = Tag{0x0008, 0x1070}
	tagReadingPhysician    = Tag{0x0008, 0x1060}
	tagAdmittingDiagnoses  = Tag{0x0008, 0x1080}

	tagInstitutionName    = Tag{0x0008, 0x0080}
	tagInstitutionAddress = Tag{0x0008, 0x0081}
	tagDepartmentName     = Tag{0x0008, 0x1040}
	tagStationName        = Tag{0x0008, 0x1010}

	tagStudyDate       = Tag{0x0008, 0x0020}
	tagSeriesDate      = Tag{0x0008, 0x0021}
	tagAcquisitionDate = Tag{0x0008, 0x0022}
	tagContentDate     = Tag{0x0008, 0x0023}
	tagStudyTime       = Tag{0x0008, 0x0030}
	tagSeriesTime      = Tag{0x0008, 0x0031}

	tagAccessionNumber = Tag{0x0008, 0x0050}
	tagStudyID         = Tag{0x0020, 0x0010}

	tagCallingAE = Tag{0x0002, 0x0016}
	tagCalledAE  = Tag{0x0002, 0x0017}

	tagPatientIdentityRemoved = Tag{0x0012, 0x0062}
	tagDeidentifyMethod       = Tag{0x0012, 0x0063}
)

// emptied are attributes replaced with nothing rather than removed.
//
// The distinction is in the profile and it matters to receivers. A Type 2 attribute must be present, so removing it produces an
// object some viewers refuse; emptying it satisfies the requirement and carries no identity.
var emptied = []Tag{
	tagPatientName, tagPatientID, tagPatientBirthDate, tagAccessionNumber, tagStudyID,
	tagReferringPhysician,
}

// removed are attributes taken out entirely, being optional and identifying.
var removed = []Tag{
	tagPatientAddress, tagPatientPhone, tagOtherPatientIDs, tagOtherPatientNames,
	tagPatientComments, tagEthnicGroup, tagOccupation, tagMedicalHistory, tagPatientAge,
	tagPerformingPhysician, tagOperatorName, tagReadingPhysician, tagAdmittingDiagnoses,
	tagInstitutionName, tagInstitutionAddress, tagDepartmentName, tagStationName,
}

// dateTags are cleared unless the step keeps them.
var dateTags = []Tag{
	tagStudyDate, tagSeriesDate, tagAcquisitionDate, tagContentDate, tagStudyTime, tagSeriesTime,
}

// Change records one modification, for the log and the message record.
type Change struct {
	Tag    Tag
	Action string
	Before string
	After  string
}

// String renders a change for a log line.
//
// Values are deliberately not included. A change to PatientName would put the patient's name in a log file that a whole
// operations team can read, which is precisely the identity the step exists to remove.
func (c Change) String() string {
	return fmt.Sprintf("%s %s", c.Action, c.Tag)
}

// Apply runs the steps against a data set, returning a new one.
//
// The input is not modified, so a failed step leaves the original intact and a retry starts from what arrived.
func Apply(in *DataSet, list []Step) (*DataSet, []Change, error) {
	if in == nil {
		return nil, nil, fmt.Errorf("there is no data set to transform")
	}

	out := &DataSet{
		TransferSyntax: in.TransferSyntax,
		HadPreamble:    in.HadPreamble,
		Elements:       make([]Element, len(in.Elements)),
	}
	copy(out.Elements, in.Elements)

	var changes []Change
	for i, step := range list {
		var stepChanges []Change
		var err error

		switch {
		case step.Deidentify != nil:
			stepChanges, err = deidentify(out, *step.Deidentify)
		case step.SetAETitle != nil:
			stepChanges, err = setAETitle(out, *step.SetAETitle)
		case step.StripPrivate != nil:
			stepChanges, err = stripPrivate(out, *step.StripPrivate)
		case step.SetInstitution != nil:
			stepChanges, err = setInstitution(out, *step.SetInstitution)
		default:
			err = fmt.Errorf("this step names no action")
		}

		if err != nil {
			return nil, nil, fmt.Errorf("step %d: %w", i+1, err)
		}
		changes = append(changes, stepChanges...)
	}

	return out, changes, nil
}

// deidentify applies the confidentiality profile's tag handling.
func deidentify(d *DataSet, step DeidentifyStep) ([]Change, error) {
	var changes []Change

	pseudonym := step.PatientID
	name := step.PatientName
	if name == "" {
		// Defaulted to the identifier so the two agree. A viewer showing an empty name beside a populated identifier looks
		// broken, and looking broken invites somebody to go and find the real name.
		name = pseudonym
	}

	for _, tag := range emptied {
		replacement := ""
		switch tag {
		case tagPatientID:
			replacement = pseudonym
		case tagPatientName:
			replacement = name
		}
		if c, ok := setText(d, tag, replacement); ok {
			changes = append(changes, c)
		}
	}

	for _, tag := range removed {
		if c, ok := remove(d, tag); ok {
			changes = append(changes, c)
		}
	}

	if !step.KeepDates {
		for _, tag := range dateTags {
			if c, ok := setText(d, tag, ""); ok {
				changes = append(changes, c)
			}
		}
	}

	// Patient sex is kept, being clinically necessary for interpretation and weakly identifying on its own. Stated because
	// its absence from the removal list would otherwise look like an oversight.
	_ = tagPatientSex

	// PatientIdentityRemoved and the method are added, which is the profile's requirement and also the honest thing: a
	// receiver can tell this object has been through de-identification rather than never having contained identity.
	if c, ok := setText(d, tagPatientIdentityRemoved, "YES"); ok {
		changes = append(changes, c)
	}
	method := "Perfuse basic confidentiality profile: identifying attributes removed or emptied"
	if step.KeepDates {
		method += "; dates retained"
	}
	if c, ok := setText(d, tagDeidentifyMethod, method); ok {
		changes = append(changes, c)
	}

	return changes, nil
}

func setAETitle(d *DataSet, step AETitleStep) ([]Change, error) {
	var changes []Change
	if step.Calling != "" {
		if c, ok := setText(d, tagCallingAE, step.Calling); ok {
			changes = append(changes, c)
		}
	}
	if step.Called != "" {
		if c, ok := setText(d, tagCalledAE, step.Called); ok {
			changes = append(changes, c)
		}
	}

	return changes, nil
}

// stripPrivate removes odd-group elements.
func stripPrivate(d *DataSet, step StripPrivateStep) ([]Change, error) {
	keep := make(map[Tag]bool, len(step.Keep))
	for _, raw := range step.Keep {
		tag, err := ParseTag(raw)
		if err != nil {
			return nil, err
		}
		if !tag.IsPrivate() {
			// Refused rather than ignored. Naming an even group in a private-tag keep list means somebody has
			// misunderstood which tags this step touches, and silently accepting it would leave them believing the tag
			// was protected by a rule that never applied to it.
			return nil, fmt.Errorf("%s is not a private tag: private groups are odd-numbered, and this step only removes those", tag)
		}
		keep[tag] = true
	}

	var changes []Change
	remaining := make([]Element, 0, len(d.Elements))
	for _, e := range d.Elements {
		if e.Tag.IsPrivate() && !keep[e.Tag] {
			changes = append(changes, Change{Tag: e.Tag, Action: "removed private"})

			continue
		}
		remaining = append(remaining, e)
	}
	d.Elements = remaining

	return changes, nil
}

func setInstitution(d *DataSet, step InstitutionStep) ([]Change, error) {
	var changes []Change
	for tag, value := range map[Tag]string{
		tagInstitutionName:    step.Name,
		tagInstitutionAddress: step.Address,
		tagDepartmentName:     step.Department,
	} {
		if value == "" {
			continue
		}
		if c, ok := setText(d, tag, value); ok {
			changes = append(changes, c)
		}
	}

	return changes, nil
}

// setText writes a string value, adding the element when it is absent.
//
// Reports false when nothing changed, so that a de-identification of an object with no patient name does not claim to have
// removed one. A change count that overstates makes an audit trail useless.
func setText(d *DataSet, tag Tag, value string) (Change, bool) {
	padded := value
	if len(padded)%2 == 1 {
		// DICOM values are even-length. Space for text, which is what every VR here uses.
		padded += " "
	}

	for i := range d.Elements {
		if d.Elements[i].Tag != tag {
			continue
		}
		before := strings.TrimRight(string(d.Elements[i].Value), " \x00")
		if before == value {
			return Change{}, false
		}
		d.Elements[i].Value = []byte(padded)

		return Change{Tag: tag, Action: "set", Before: before, After: value}, true
	}

	if value == "" {
		// Nothing to empty. Adding an empty element would be a change that means nothing, and it would make the count
		// non-zero for an object that had no such attribute.
		return Change{}, false
	}

	d.Elements = append(d.Elements, Element{Tag: tag, VR: inferVR(tag), Value: []byte(padded)})

	return Change{Tag: tag, Action: "added", After: value}, true
}

func remove(d *DataSet, tag Tag) (Change, bool) {
	for i := range d.Elements {
		if d.Elements[i].Tag != tag {
			continue
		}
		d.Elements = append(d.Elements[:i], d.Elements[i+1:]...)

		return Change{Tag: tag, Action: "removed"}, true
	}

	return Change{}, false
}
