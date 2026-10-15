package xds

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Registry is an XDS.b Document Registry and Document Repository in one, on SQLite.
//
// Every registry object is kept as the XML it was registered as (with symbolic ids replaced and the repository's slots
// added), and the fields stored queries match on are indexed beside it. Document content is kept in the same database.
type Registry struct {
	DB *sql.DB
	// RepositoryID is this repository's uniqueId (an OID), which every document registered here carries.
	RepositoryID string
	// HomeCommunityID is this community's id (urn:oid:...), for XCA; empty answers ITI-38 and ITI-39 as unknown.
	HomeCommunityID string
	// KnownPatient, when set, is asked whether a patient ID (CX, id^^^&oid&ISO) belongs to the affinity domain; a submission
	// for another is refused with XDSUnknownPatientId. Nil accepts any well-formed patient ID.
	KnownPatient func(ctx context.Context, patientID string) bool
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Registered, when set, is told of each submission after it is stored.
	Registered func(ctx context.Context, s *Submission)

	once sync.Once
	err  error
}

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Kinds of object.
const (
	KindDocument      = "doc"
	KindSubmissionSet = "ss"
	KindFolder        = "folder"
	KindAssociation   = "assoc"
)

func (r *Registry) ready(ctx context.Context) error {
	r.once.Do(func() {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS xds_objects (
				id TEXT PRIMARY KEY,
				kind TEXT NOT NULL,
				unique_id TEXT NOT NULL DEFAULT '',
				patient_id TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL,
				object_type TEXT NOT NULL DEFAULT '',
				creation_time TEXT NOT NULL DEFAULT '',
				service_start TEXT NOT NULL DEFAULT '',
				service_stop TEXT NOT NULL DEFAULT '',
				submission_time TEXT NOT NULL DEFAULT '',
				last_update TEXT NOT NULL DEFAULT '',
				source_id TEXT NOT NULL DEFAULT '',
				authors TEXT NOT NULL DEFAULT '',
				mime TEXT NOT NULL DEFAULT '',
				repository TEXT NOT NULL DEFAULT '',
				hash TEXT NOT NULL DEFAULT '',
				size INTEGER NOT NULL DEFAULT 0,
				source TEXT NOT NULL DEFAULT '',
				target TEXT NOT NULL DEFAULT '',
				assoc_type TEXT NOT NULL DEFAULT '',
				submission TEXT NOT NULL DEFAULT '',
				registered INTEGER NOT NULL,
				xml TEXT NOT NULL)`,
			`CREATE INDEX IF NOT EXISTS xds_objects_patient ON xds_objects (kind, patient_id)`,
			`CREATE INDEX IF NOT EXISTS xds_objects_unique ON xds_objects (kind, unique_id)`,
			`CREATE INDEX IF NOT EXISTS xds_objects_source ON xds_objects (source)`,
			`CREATE INDEX IF NOT EXISTS xds_objects_target ON xds_objects (target)`,
			`CREATE TABLE IF NOT EXISTS xds_codes (
				object_id TEXT NOT NULL,
				scheme TEXT NOT NULL,
				code_key TEXT NOT NULL)`,
			`CREATE INDEX IF NOT EXISTS xds_codes_lookup ON xds_codes (scheme, code_key, object_id)`,
			`CREATE TABLE IF NOT EXISTS xds_content (
				unique_id TEXT PRIMARY KEY,
				mime TEXT NOT NULL,
				content BLOB NOT NULL)`,
		} {
			if _, err := r.DB.ExecContext(ctx, stmt); err != nil {
				r.err = fmt.Errorf("preparing the XDS tables: %w", err)
				return
			}
		}
	})
	return r.err
}

// Problem is one registry error.
type Problem struct {
	Code, Context, Location string
	Warning                 bool
}

func problem(code, location, format string, args ...any) Problem {
	return Problem{Code: code, Location: location, Context: fmt.Sprintf(format, args...)}
}

// Response turns problems into a RegistryResponse: Success with none, Failure with any error, Success with only warnings.
func Response(problems []Problem) RegistryResponse {
	resp := RegistryResponse{Status: ResponseSuccess}
	for _, p := range problems {
		sev := SeverityError
		if p.Warning {
			sev = SeverityWarning
		} else {
			resp.Status = ResponseFailure
		}
		resp.Errors = append(resp.Errors, RegistryError{Code: p.Code, Context: p.Context, Severity: sev, Location: p.Location})
	}
	return resp
}

// Submission is what one registration stored.
type Submission struct {
	SubmissionSet Object
	Documents     []Object
	Folders       []Object
	Associations  []Object
}

// rewriteIDs replaces symbolic ids (anything not urn:uuid:) with new UUIDs throughout the list.
func rewriteIDs(l *RegistryObjectList) map[string]string {
	ids := map[string]string{}
	fresh := func(id string) string {
		if id == "" || strings.HasPrefix(id, "urn:uuid:") {
			return id
		}
		if n, ok := ids[id]; ok {
			return n
		}
		n := "urn:uuid:" + newUUID()
		ids[id] = n
		return n
	}
	ref := func(id string) string {
		if n, ok := ids[id]; ok {
			return n
		}
		return id
	}
	all := func(f func(o *Object)) {
		for i := range l.Extrinsic {
			f(&l.Extrinsic[i])
		}
		for i := range l.Packages {
			f(&l.Packages[i])
		}
		for i := range l.Associations {
			f(&l.Associations[i])
		}
	}
	all(func(o *Object) { o.ID = fresh(o.ID) })
	for i := range l.Classifications {
		l.Classifications[i].ID = fresh(l.Classifications[i].ID)
	}
	all(func(o *Object) {
		o.SourceObject, o.TargetObject = ref(o.SourceObject), ref(o.TargetObject)
		if o.LID == "" || !strings.HasPrefix(o.LID, "urn:uuid:") {
			o.LID = o.ID
		}
		for i := range o.Classifications {
			c := &o.Classifications[i]
			c.ID = fresh(c.ID)
			c.ClassifiedObject = ref(c.ClassifiedObject)
			if c.ClassifiedObject == "" {
				c.ClassifiedObject = o.ID
			}
		}
		for i := range o.ExternalIDs {
			e := &o.ExternalIDs[i]
			e.ID = fresh(e.ID)
			e.RegistryObject = ref(e.RegistryObject)
			if e.RegistryObject == "" {
				e.RegistryObject = o.ID
			}
		}
	})
	for i := range l.Classifications {
		l.Classifications[i].ClassifiedObject = ref(l.Classifications[i].ClassifiedObject)
	}
	return ids
}

// pad makes a DTM comparable as text: YYYY[MM[DD[hh[mm[ss]]]]] padded to fourteen digits.
func pad(dtm string) string {
	dtm = strings.TrimSpace(dtm)
	for len(dtm) < 14 {
		dtm += "0"
	}
	return dtm
}

var dtmOK = func(s string) bool {
	if len(s) < 4 || len(s) > 14 || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// cxOK checks a patient ID: id^^^&oid&ISO.
func cxOK(cx string) bool {
	parts := strings.Split(cx, "^")
	if len(parts) != 4 || parts[0] == "" || parts[1] != "" || parts[2] != "" {
		return false
	}
	auth := strings.Split(parts[3], "&")
	return len(auth) == 3 && auth[0] == "" && auth[1] != "" && auth[2] == "ISO"
}

func (r *Registry) exists(ctx context.Context, tx *sql.Tx, id string) (kind, status, patient string, ok bool) {
	err := tx.QueryRowContext(ctx, `SELECT kind, status, patient_id FROM xds_objects WHERE id = ?`, id).Scan(&kind, &status, &patient)
	return kind, status, patient, err == nil
}

func (r *Registry) uniqueTaken(ctx context.Context, tx *sql.Tx, kind, uid string) (string, bool) {
	var hash string
	err := tx.QueryRowContext(ctx, `SELECT hash FROM xds_objects WHERE kind = ? AND unique_id = ?`, kind, uid).Scan(&hash)
	return hash, err == nil
}

// Register stores a submission. docs holds the documents' content by the id the DocumentEntry had in the request (symbolic
// or not); nil registers metadata only (ITI-42), where each entry must already carry its repositoryUniqueId, hash and size.
func (r *Registry) Register(ctx context.Context, req *SubmitObjectsRequest, docs map[string][]byte) (*Submission, []Problem) {
	if err := r.ready(ctx); err != nil {
		return nil, []Problem{problem(ErrRegistryError, "", "%v", err)}
	}
	l := &req.Objects
	// Content by the entry's original id, before ids are rewritten.
	original := map[string]string{}
	for i := range l.Extrinsic {
		original[l.Extrinsic[i].ID] = l.Extrinsic[i].ID
	}
	ids := rewriteIDs(l)
	contentFor := map[string][]byte{}
	for old := range original {
		newID := old
		if n, ok := ids[old]; ok {
			newID = n
		}
		if docs != nil {
			if c, ok := docs[old]; ok {
				contentFor[newID] = c
			}
		}
	}
	var ps []Problem
	if docs != nil {
		for id := range docs {
			if _, ok := original[id]; !ok {
				ps = append(ps, problem(ErrMissingMetadata, id, "document %s has no DocumentEntry describing it", id))
			}
		}
	}

	// Classifications listed beside the packages move into them, so a SubmissionSet is stored whole.
	for _, c := range l.Classifications {
		placed := false
		for i := range l.Packages {
			if l.Packages[i].ID == c.ClassifiedObject {
				l.Packages[i].Classifications = append(l.Packages[i].Classifications, c)
				placed = true
			}
		}
		if !placed {
			ps = append(ps, problem(ErrMetadata, c.ID, "classification %s classifies %s, which is not a RegistryPackage in the submission", c.ID, c.ClassifiedObject))
		}
	}

	sub := &Submission{}
	var ssCount int
	for _, p := range l.Packages {
		switch {
		case p.HasNode(NodeSubmissionSet):
			ssCount++
			sub.SubmissionSet = p
		case p.HasNode(NodeFolder):
			sub.Folders = append(sub.Folders, p)
		default:
			ps = append(ps, problem(ErrMetadata, p.ID, "RegistryPackage %s is classified as neither a SubmissionSet nor a Folder", p.ID))
		}
	}
	if ssCount != 1 {
		return nil, append(ps, problem(ErrMetadata, "", "a submission has exactly one SubmissionSet, and this one has %d", ssCount))
	}
	ss := &sub.SubmissionSet
	ssPatient := ss.ExternalID(IDSSPatientID)
	switch {
	case ss.ExternalID(IDSSUniqueID) == "":
		ps = append(ps, problem(ErrMetadata, ss.ID, "the SubmissionSet has no uniqueId"))
	case ss.ExternalID(IDSSSourceID) == "":
		ps = append(ps, problem(ErrMetadata, ss.ID, "the SubmissionSet has no sourceId"))
	case !cxOK(ssPatient):
		ps = append(ps, problem(ErrMetadata, ss.ID, "the SubmissionSet patientId %q is not id^^^&OID&ISO", ssPatient))
	case len(ss.Codes(SchemeContentType)) == 0:
		ps = append(ps, problem(ErrMetadata, ss.ID, "the SubmissionSet has no contentTypeCode"))
	case !dtmOK(ss.Slot1("submissionTime")):
		ps = append(ps, problem(ErrMetadata, ss.ID, "the SubmissionSet's submissionTime %q is not a DTM", ss.Slot1("submissionTime")))
	}
	if ssPatient != "" && cxOK(ssPatient) && r.KnownPatient != nil && !r.KnownPatient(ctx, ssPatient) {
		ps = append(ps, problem(ErrUnknownPatient, ss.ID, "patient %s is not known in this affinity domain", ssPatient))
	}

	now := r.now()
	for i := range l.Extrinsic {
		d := &l.Extrinsic[i]
		loc := d.ID
		if d.ObjectType == TypeOnDemandDocument {
			ps = append(ps, problem(ErrMetadata, loc, "on-demand documents are not supported by this registry"))
			continue
		}
		if d.ObjectType == "" {
			d.ObjectType = TypeStableDocument
		}
		if d.ObjectType != TypeStableDocument {
			ps = append(ps, problem(ErrMetadata, loc, "ExtrinsicObject %s has objectType %s, which is not a DocumentEntry", d.ID, d.ObjectType))
			continue
		}
		if d.ExternalID(IDDocUniqueID) == "" {
			ps = append(ps, problem(ErrMetadata, loc, "DocumentEntry %s has no uniqueId", d.ID))
		}
		if p := d.ExternalID(IDDocPatientID); p != ssPatient {
			ps = append(ps, problem(ErrPatientMismatch, loc, "DocumentEntry %s is for patient %q and its SubmissionSet for %q", d.ID, p, ssPatient))
		}
		if d.MimeType == "" {
			ps = append(ps, problem(ErrMetadata, loc, "DocumentEntry %s has no mimeType", d.ID))
		}
		for _, req := range []struct{ scheme, name string }{{SchemeClassCode, "classCode"}, {SchemeTypeCode, "typeCode"},
			{SchemeFormatCode, "formatCode"}, {SchemeConfidentiality, "confidentialityCode"}, {SchemeFacilityType, "healthcareFacilityTypeCode"},
			{SchemePracticeSetting, "practiceSettingCode"}} {
			if len(d.Codes(req.scheme)) == 0 {
				ps = append(ps, problem(ErrMetadata, loc, "DocumentEntry %s has no %s", d.ID, req.name))
			}
		}
		for _, slot := range []string{"creationTime", "languageCode", "sourcePatientId"} {
			if d.Slot1(slot) == "" {
				ps = append(ps, problem(ErrMetadata, loc, "DocumentEntry %s has no %s", d.ID, slot))
			}
		}
		for _, slot := range []string{"creationTime", "serviceStartTime", "serviceStopTime"} {
			if v := d.Slot1(slot); v != "" && !dtmOK(v) {
				ps = append(ps, problem(ErrMetadata, loc, "DocumentEntry %s %s %q is not a DTM", d.ID, slot, v))
			}
		}
		if docs != nil {
			c, ok := contentFor[d.ID]
			if !ok {
				ps = append(ps, problem(ErrMissingDocument, loc, "DocumentEntry %s has no document in the request", d.ID))
				continue
			}
			sum := sha1.Sum(c)
			hash := hex.EncodeToString(sum[:])
			if h := d.Slot1("hash"); h != "" && !strings.EqualFold(h, hash) {
				ps = append(ps, problem(ErrNonIdenticalHash, loc, "DocumentEntry %s gives hash %s and the document's is %s", d.ID, h, hash))
			}
			if s := d.Slot1("size"); s != "" && s != strconv.Itoa(len(c)) {
				ps = append(ps, problem(ErrNonIdenticalSize, loc, "DocumentEntry %s gives size %s and the document is %d bytes", d.ID, s, len(c)))
			}
			d.SetSlot("hash", hash)
			d.SetSlot("size", strconv.Itoa(len(c)))
			d.SetSlot("repositoryUniqueId", r.RepositoryID)
		} else {
			for _, slot := range []string{"hash", "size", "repositoryUniqueId"} {
				if d.Slot1(slot) == "" {
					ps = append(ps, problem(ErrMetadata, loc, "DocumentEntry %s registered without a document needs %s", d.ID, slot))
				}
			}
		}
		d.Status = StatusApproved
		sub.Documents = append(sub.Documents, *d)
	}
	for i := range sub.Folders {
		f := &sub.Folders[i]
		if f.ExternalID(IDFolderUniqueID) == "" {
			ps = append(ps, problem(ErrMetadata, f.ID, "Folder %s has no uniqueId", f.ID))
		}
		if p := f.ExternalID(IDFolderPatient); p != ssPatient {
			ps = append(ps, problem(ErrPatientMismatch, f.ID, "Folder %s is for patient %q and its SubmissionSet for %q", f.ID, p, ssPatient))
		}
		f.Status = StatusApproved
		f.SetSlot("lastUpdateTime", now.Format("20060102150405"))
	}
	ss.Status = StatusApproved

	inSubmission := map[string]string{ss.ID: KindSubmissionSet}
	for _, d := range sub.Documents {
		inSubmission[d.ID] = KindDocument
	}
	for _, f := range sub.Folders {
		inSubmission[f.ID] = KindFolder
	}
	for _, a := range l.Associations {
		inSubmission[a.ID] = KindAssociation
	}
	if len(ps) > 0 {
		return nil, ps
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, []Problem{problem(ErrRegistryError, "", "%v", err)}
	}
	defer func() { _ = tx.Rollback() }()

	for id := range inSubmission {
		if _, _, _, taken := r.exists(ctx, tx, id); taken {
			ps = append(ps, problem(ErrMetadata, id, "entryUUID %s is already in the registry", id))
		}
	}
	if _, taken := r.uniqueTaken(ctx, tx, KindSubmissionSet, ss.ExternalID(IDSSUniqueID)); taken {
		ps = append(ps, problem(ErrDuplicateUniqueID, ss.ID, "SubmissionSet uniqueId %s is already in the registry", ss.ExternalID(IDSSUniqueID)))
	}
	for _, d := range sub.Documents {
		if hash, taken := r.uniqueTaken(ctx, tx, KindDocument, d.ExternalID(IDDocUniqueID)); taken {
			if hash != "" && !strings.EqualFold(hash, d.Slot1("hash")) {
				ps = append(ps, problem(ErrNonIdenticalHash, d.ID, "document uniqueId %s is registered with another hash", d.ExternalID(IDDocUniqueID)))
			} else {
				ps = append(ps, problem(ErrDuplicateUniqueID, d.ID, "document uniqueId %s is already in the registry", d.ExternalID(IDDocUniqueID)))
			}
		}
	}
	for _, f := range sub.Folders {
		if _, taken := r.uniqueTaken(ctx, tx, KindFolder, f.ExternalID(IDFolderUniqueID)); taken {
			ps = append(ps, problem(ErrDuplicateUniqueID, f.ID, "Folder uniqueId %s is already in the registry", f.ExternalID(IDFolderUniqueID)))
		}
	}

	// Associations: every document is a member of the SubmissionSet, every target exists, and a replacement deprecates
	// what it replaces.
	member := map[string]bool{}
	var deprecate []string
	resolve := func(id string) (string, string, string, bool) {
		if k, ok := inSubmission[id]; ok {
			p := ssPatient
			return k, StatusApproved, p, true
		}
		return r.exists(ctx, tx, id)
	}
	for i := range l.Associations {
		a := &l.Associations[i]
		a.Status = StatusApproved
		sk, _, _, sok := resolve(a.SourceObject)
		tk, tstatus, tpatient, tok := resolve(a.TargetObject)
		switch {
		case !sok:
			ps = append(ps, problem(ErrUnreferencedObject, a.ID, "association %s has source %s, which is neither in the submission nor the registry", a.ID, a.SourceObject))
			continue
		case !tok:
			ps = append(ps, problem(ErrUnreferencedObject, a.ID, "association %s has target %s, which is neither in the submission nor the registry", a.ID, a.TargetObject))
			continue
		}
		switch a.AssociationType {
		case AssocHasMember:
			if a.SourceObject == ss.ID && tk == KindDocument {
				member[a.TargetObject] = true
				if v := a.Slot1("SubmissionSetStatus"); v != "Original" && v != "Reference" {
					ps = append(ps, problem(ErrMetadata, a.ID, "a SubmissionSet HasMember association needs SubmissionSetStatus Original or Reference, not %q", v))
				}
			}
			if sk == KindFolder && tk == KindDocument && tpatient != ssPatient {
				ps = append(ps, problem(ErrPatientMismatch, a.ID, "folder member %s is another patient's", a.TargetObject))
			}
		case AssocReplace, AssocAppend, AssocTransform, AssocXFRMRPLC, AssocSigns:
			if sk != KindDocument || tk != KindDocument {
				ps = append(ps, problem(ErrMetadata, a.ID, "a %s association joins two documents", short(a.AssociationType)))
				continue
			}
			if _, inSub := inSubmission[a.TargetObject]; inSub && a.AssociationType != AssocSigns {
				ps = append(ps, problem(ErrMetadata, a.ID, "%s targets a document already in the registry, not one in this submission", short(a.AssociationType)))
				continue
			}
			if tpatient != ssPatient {
				ps = append(ps, problem(ErrPatientMismatch, a.ID, "%s targets another patient's document", short(a.AssociationType)))
			}
			if (a.AssociationType == AssocReplace || a.AssociationType == AssocXFRMRPLC || a.AssociationType == AssocAppend) && tstatus != StatusApproved {
				ps = append(ps, problem(ErrMetadata, a.ID, "%s targets document %s, which is not Approved", short(a.AssociationType), a.TargetObject))
			}
			if a.AssociationType == AssocReplace || a.AssociationType == AssocXFRMRPLC {
				deprecate = append(deprecate, a.TargetObject)
			}
		default:
			ps = append(ps, problem(ErrMetadata, a.ID, "association type %s is not one XDS defines", a.AssociationType))
		}
		sub.Associations = append(sub.Associations, *a)
	}
	for _, d := range sub.Documents {
		if !member[d.ID] {
			ps = append(ps, problem(ErrMetadata, d.ID, "DocumentEntry %s is not a member of the SubmissionSet (no HasMember association)", d.ID))
		}
	}
	if len(ps) > 0 {
		return nil, ps
	}

	reg := now.UnixMilli()
	put := func(o Object, kind string, fields map[string]any) error {
		raw, err := xml.Marshal(element(kind, o))
		if err != nil {
			return err
		}
		cols := []string{"id", "kind", "status", "object_type", "registered", "xml", "submission"}
		vals := []any{o.ID, kind, o.Status, o.ObjectType, reg, string(raw), ss.ID}
		for k, v := range fields {
			cols = append(cols, k)
			vals = append(vals, v)
		}
		q := `INSERT INTO xds_objects (` + strings.Join(cols, ", ") + `) VALUES (?` + strings.Repeat(", ?", len(cols)-1) + `)`
		if _, err := tx.ExecContext(ctx, q, vals...); err != nil {
			return err
		}
		for _, c := range o.Classifications {
			if c.Scheme == "" || c.NodeRepresentation == "" {
				continue
			}
			cd := Code{Code: c.NodeRepresentation}
			for _, s := range c.Slots {
				if s.Name == "codingScheme" && len(s.Values) > 0 {
					cd.Scheme = s.Values[0]
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO xds_codes (object_id, scheme, code_key) VALUES (?, ?, ?)`, o.ID, c.Scheme, cd.Key()); err != nil {
				return err
			}
		}
		return nil
	}
	authors := func(o Object, scheme string) string {
		var names []string
		for _, c := range o.Classifications {
			if c.Scheme == scheme {
				for _, s := range c.Slots {
					if s.Name == "authorPerson" {
						names = append(names, s.Values...)
					}
				}
			}
		}
		return strings.Join(names, "\n")
	}
	fail := func(err error) (*Submission, []Problem) {
		return nil, []Problem{problem(ErrRegistryError, "", "%v", err)}
	}
	if err := put(*ss, KindSubmissionSet, map[string]any{"unique_id": ss.ExternalID(IDSSUniqueID), "patient_id": ssPatient,
		"submission_time": pad(ss.Slot1("submissionTime")), "source_id": ss.ExternalID(IDSSSourceID), "authors": authors(*ss, SchemeSSAuthor)}); err != nil {
		return fail(err)
	}
	for _, d := range sub.Documents {
		size, _ := strconv.Atoi(d.Slot1("size"))
		if err := put(d, KindDocument, map[string]any{"unique_id": d.ExternalID(IDDocUniqueID), "patient_id": ssPatient,
			"creation_time": pad(d.Slot1("creationTime")), "service_start": padOrEmpty(d.Slot1("serviceStartTime")),
			"service_stop": padOrEmpty(d.Slot1("serviceStopTime")), "authors": authors(d, SchemeDocAuthor), "mime": d.MimeType,
			"repository": d.Slot1("repositoryUniqueId"), "hash": strings.ToLower(d.Slot1("hash")), "size": size}); err != nil {
			return fail(err)
		}
		if c, ok := contentFor[d.ID]; ok {
			if _, err := tx.ExecContext(ctx, `INSERT INTO xds_content (unique_id, mime, content) VALUES (?, ?, ?)`,
				d.ExternalID(IDDocUniqueID), d.MimeType, c); err != nil {
				return fail(err)
			}
		}
	}
	for _, f := range sub.Folders {
		if err := put(f, KindFolder, map[string]any{"unique_id": f.ExternalID(IDFolderUniqueID), "patient_id": ssPatient,
			"last_update": pad(f.Slot1("lastUpdateTime"))}); err != nil {
			return fail(err)
		}
	}
	for _, a := range sub.Associations {
		if err := put(a, KindAssociation, map[string]any{"source": a.SourceObject, "target": a.TargetObject, "assoc_type": a.AssociationType,
			"patient_id": ssPatient}); err != nil {
			return fail(err)
		}
		// A document added to an existing folder updates the folder's lastUpdateTime.
		if a.AssociationType == AssocHasMember {
			if k, _, _, ok := r.exists(ctx, tx, a.SourceObject); ok && k == KindFolder {
				if err := r.touchFolder(ctx, tx, a.SourceObject, now); err != nil {
					return fail(err)
				}
			}
		}
	}
	for _, id := range deprecate {
		if err := r.setStatus(ctx, tx, id, StatusDeprecated); err != nil {
			return fail(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	if r.Registered != nil {
		r.Registered(ctx, sub)
	}
	return sub, nil
}

func padOrEmpty(v string) string {
	if v == "" {
		return ""
	}
	return pad(v)
}

func short(t string) string { return t[strings.LastIndex(t, ":")+1:] }

// element wraps an object for marshalling under its own element name.
func element(kind string, o Object) any {
	switch kind {
	case KindDocument:
		return struct {
			XMLName xml.Name `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 ExtrinsicObject"`
			Object
		}{Object: o}
	case KindAssociation:
		return struct {
			XMLName xml.Name `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 Association"`
			Object
		}{Object: o}
	}
	return struct {
		XMLName xml.Name `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 RegistryPackage"`
		Object
	}{Object: o}
}

func (r *Registry) load(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Object, string, error) {
	var raw, kind string
	if err := q.QueryRowContext(ctx, `SELECT xml, kind FROM xds_objects WHERE id = ?`, id).Scan(&raw, &kind); err != nil {
		return Object{}, "", err
	}
	var o Object
	if err := xml.Unmarshal([]byte(raw), &o); err != nil {
		return Object{}, "", err
	}
	return o, kind, nil
}

func (r *Registry) save(ctx context.Context, tx *sql.Tx, o Object, kind string) error {
	raw, err := xml.Marshal(element(kind, o))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE xds_objects SET xml = ?, status = ?, last_update = ? WHERE id = ?`, string(raw), o.Status,
		pad(o.Slot1("lastUpdateTime")), o.ID)
	return err
}

func (r *Registry) setStatus(ctx context.Context, tx *sql.Tx, id, status string) error {
	o, kind, err := r.load(ctx, tx, id)
	if err != nil {
		return err
	}
	o.Status = status
	return r.save(ctx, tx, o, kind)
}

func (r *Registry) touchFolder(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	o, kind, err := r.load(ctx, tx, id)
	if err != nil {
		return err
	}
	o.SetSlot("lastUpdateTime", now.Format("20060102150405"))
	return r.save(ctx, tx, o, kind)
}

// ErrNoDocument is a retrieve of a document this repository does not hold.
var ErrNoDocument = errors.New("no such document in this repository")

// Content returns a document's content and mime type.
func (r *Registry) Content(ctx context.Context, uniqueID string) ([]byte, string, error) {
	if err := r.ready(ctx); err != nil {
		return nil, "", err
	}
	var b []byte
	var mt string
	err := r.DB.QueryRowContext(ctx, `SELECT content, mime FROM xds_content WHERE unique_id = ?`, uniqueID).Scan(&b, &mt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNoDocument
	}
	return b, mt, err
}

// Counts is what the registry holds, for the screen.
type Counts struct {
	Documents      int `json:"documents"`
	SubmissionSets int `json:"submissionSets"`
	Folders        int `json:"folders"`
	Patients       int `json:"patients"`
	Deprecated     int `json:"deprecated"`
	Bytes          int `json:"bytes"`
}

// Count counts the registry.
func (r *Registry) Count(ctx context.Context) (Counts, error) {
	var c Counts
	if err := r.ready(ctx); err != nil {
		return c, err
	}
	err := r.DB.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(kind = 'doc'), 0), COALESCE(SUM(kind = 'ss'), 0), COALESCE(SUM(kind = 'folder'), 0),
		COUNT(DISTINCT CASE WHEN kind = 'doc' THEN patient_id END), COALESCE(SUM(kind = 'doc' AND status = ?), 0),
		COALESCE(SUM(CASE WHEN kind = 'doc' THEN size ELSE 0 END), 0) FROM xds_objects`, StatusDeprecated).
		Scan(&c.Documents, &c.SubmissionSets, &c.Folders, &c.Patients, &c.Deprecated, &c.Bytes)
	return c, err
}
