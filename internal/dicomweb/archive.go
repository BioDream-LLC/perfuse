package dicomweb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/sqlitedb"
)

// Archive holds DICOM objects on disk with an index, which is what QIDO-RS searches and WADO-RS reads.
//
// Objects are kept exactly as received: a Part 10 file per instance, named by its UIDs. The index holds the attributes
// QIDO-RS matches on and returns, so a search never opens a file.
type Archive struct {
	Dir string
	db  *sqlitedb.DB
}

// uidPattern is what a UID may be (PS3.5 9.1): digits and dots, at most 64 characters. Checked before a UID is used
// as part of a path, so an object cannot name a file outside the archive.
var uidPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)

// ValidUID reports whether s is a well-formed UID.
func ValidUID(s string) bool { return len(s) <= 64 && uidPattern.MatchString(s) }

// OpenArchive opens (or creates) an archive in dir.
func OpenArchive(dir string) (*Archive, error) {
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o750); err != nil {
		return nil, err
	}
	db, err := sqlitedb.Open(filepath.Join(dir, "index.db"), sqlitedb.Options{})
	if err != nil {
		return nil, err
	}
	_, err = db.Write.Exec(`
CREATE TABLE IF NOT EXISTS instances (
	sop_uid TEXT PRIMARY KEY, series_uid TEXT NOT NULL, study_uid TEXT NOT NULL,
	sop_class TEXT, transfer_syntax TEXT,
	patient_id TEXT, patient_name TEXT, birth_date TEXT, sex TEXT,
	study_date TEXT, study_time TEXT, accession TEXT, study_id TEXT, study_desc TEXT, referring TEXT,
	modality TEXT, series_number TEXT, series_desc TEXT, body_part TEXT,
	instance_number TEXT, rows TEXT, columns TEXT, frames TEXT,
	charset TEXT, size INTEGER, sha256 TEXT, path TEXT NOT NULL, stored_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS instances_study ON instances(study_uid);
CREATE INDEX IF NOT EXISTS instances_series ON instances(series_uid);
CREATE INDEX IF NOT EXISTS instances_patient ON instances(patient_id);
CREATE INDEX IF NOT EXISTS instances_accession ON instances(accession);`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("dicomweb: preparing the archive index: %w", err)
	}
	return &Archive{Dir: dir, db: db}, nil
}

// Close closes the index.
func (a *Archive) Close() error { return a.db.Close() }

// Stored is the outcome of storing one object.
type Stored struct {
	StudyUID, SeriesUID, SOPUID, SOPClass string
	// Replaced reports that an instance with the same UID was already held and has been replaced.
	Replaced bool
}

// Failure codes STOW-RS reports in FailureReason (PS3.18 Table 10.5.3-1, PS3.4 C.4.2.1.4).
const (
	FailureProcessing    = 0x0110
	FailureNotSupported  = 0x0122
	FailureStudyMismatch = 0xA900
	FailureCannotRead    = 0xC000
)

// StoreError is a refusal to store, with its STOW-RS failure reason.
type StoreError struct {
	Reason      uint16
	Message     string
	SOPUID      string
	SOPClassUID string
}

func (e *StoreError) Error() string { return e.Message }

// Store keeps one Part 10 object. When studyUID is set (STOW-RS to /studies/{uid}), the object must belong to it.
func (a *Archive) Store(ctx context.Context, raw []byte, studyUID string) (*Stored, error) {
	obj, err := Parse(raw)
	if err != nil {
		return nil, &StoreError{Reason: FailureCannotRead, Message: err.Error()}
	}
	st := &Stored{
		StudyUID: obj.Text(0x0020000D), SeriesUID: obj.Text(0x0020000E),
		SOPUID: obj.Text(0x00080018), SOPClass: obj.Text(0x00080016),
	}
	refuse := func(reason uint16, msg string) (*Stored, error) {
		return nil, &StoreError{Reason: reason, Message: msg, SOPUID: st.SOPUID, SOPClassUID: st.SOPClass}
	}
	for name, uid := range map[string]string{"StudyInstanceUID": st.StudyUID, "SeriesInstanceUID": st.SeriesUID,
		"SOPInstanceUID": st.SOPUID, "SOPClassUID": st.SOPClass} {
		if !ValidUID(uid) {
			return refuse(FailureCannotRead, fmt.Sprintf("the object's %s %q is missing or not a valid UID", name, uid))
		}
	}
	if studyUID != "" && studyUID != st.StudyUID {
		return refuse(FailureStudyMismatch, fmt.Sprintf("the object belongs to study %s, not %s, which this request "+
			"was sent to", st.StudyUID, studyUID))
	}

	var prior string
	_ = a.db.Read.QueryRowContext(ctx, `SELECT study_uid || '/' || series_uid FROM instances WHERE sop_uid = ?`,
		st.SOPUID).Scan(&prior)
	if prior != "" && prior != st.StudyUID+"/"+st.SeriesUID {
		// The same instance under another study or series is a data error somewhere upstream, and replacing it would
		// move an image from one patient's study to another's.
		return refuse(FailureStudyMismatch, fmt.Sprintf("instance %s is already held in a different study or series "+
			"(%s); it was not replaced", st.SOPUID, prior))
	}

	rel := filepath.Join("objects", st.StudyUID, st.SeriesUID, st.SOPUID+".dcm")
	path := filepath.Join(a.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return refuse(FailureProcessing, "the archive could not create the study's folder: "+err.Error())
	}
	// Written to a temporary name and renamed, so a reader never sees half an object and a crash leaves the old one.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return refuse(FailureProcessing, "the archive could not write the object: "+err.Error())
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return refuse(FailureProcessing, "the archive could not write the object: "+err.Error())
	}

	sum := sha256.Sum256(raw)
	_, err = a.db.Write.ExecContext(ctx, `INSERT INTO instances (sop_uid, series_uid, study_uid, sop_class,
		transfer_syntax, patient_id, patient_name, birth_date, sex, study_date, study_time, accession, study_id,
		study_desc, referring, modality, series_number, series_desc, body_part, instance_number, rows, columns, frames,
		charset, size, sha256, path, stored_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(sop_uid) DO UPDATE SET sop_class=excluded.sop_class, transfer_syntax=excluded.transfer_syntax,
		patient_id=excluded.patient_id, patient_name=excluded.patient_name, birth_date=excluded.birth_date,
		sex=excluded.sex, study_date=excluded.study_date, study_time=excluded.study_time,
		accession=excluded.accession, study_id=excluded.study_id, study_desc=excluded.study_desc,
		referring=excluded.referring, modality=excluded.modality, series_number=excluded.series_number,
		series_desc=excluded.series_desc, body_part=excluded.body_part, instance_number=excluded.instance_number,
		rows=excluded.rows, columns=excluded.columns, frames=excluded.frames, charset=excluded.charset,
		size=excluded.size, sha256=excluded.sha256, path=excluded.path, stored_at=excluded.stored_at`,
		st.SOPUID, st.SeriesUID, st.StudyUID, st.SOPClass, obj.TransferSyntax,
		obj.Text(0x00100020), obj.Text(0x00100010), obj.Text(0x00100030), obj.Text(0x00100040),
		obj.Text(0x00080020), obj.Text(0x00080030), obj.Text(0x00080050), obj.Text(0x00200010),
		obj.Text(0x00081030), obj.Text(0x00080090), obj.Text(0x00080060), obj.Text(0x00200011),
		obj.Text(0x0008103E), obj.Text(0x00180015), obj.Text(0x00200013),
		strconv.Itoa(int(obj.uint16(0x00280010))), strconv.Itoa(int(obj.uint16(0x00280011))), obj.Text(0x00280008),
		obj.Text(0x00080005), len(raw), hex.EncodeToString(sum[:]), rel, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return refuse(FailureProcessing, "the archive could not index the object: "+err.Error())
	}
	st.Replaced = prior != ""
	return st, nil
}

// Level is a QIDO-RS search level.
type Level string

const (
	LevelStudy    Level = "study"
	LevelSeries   Level = "series"
	LevelInstance Level = "instance"
)

// Query is a QIDO-RS search.
type Query struct {
	Level Level
	// StudyUID and SeriesUID come from the path (/studies/{s}/series).
	StudyUID, SeriesUID string
	// Match is attribute (keyword or tag) to value, as given in the query string.
	Match  map[string]string
	Fuzzy  bool
	Limit  int
	Offset int
}

// column is one searchable attribute.
type column struct {
	name, keyword, vr string
	tag               uint32
	level             Level
	kind              matchKind
}

type matchKind int

const (
	exact matchKind = iota
	wildcard
	uidList
	dateRange
	pnMatch
)

// columns are the attributes QIDO-RS requires a server to match on (PS3.18 Table 10.6.1-5), and the ones it returns.
var columns = []column{
	{"study_date", "StudyDate", "DA", 0x00080020, LevelStudy, dateRange},
	{"study_time", "StudyTime", "TM", 0x00080030, LevelStudy, exact},
	{"accession", "AccessionNumber", "SH", 0x00080050, LevelStudy, wildcard},
	{"referring", "ReferringPhysicianName", "PN", 0x00080090, LevelStudy, pnMatch},
	{"study_desc", "StudyDescription", "LO", 0x00081030, LevelStudy, wildcard},
	{"patient_name", "PatientName", "PN", 0x00100010, LevelStudy, pnMatch},
	{"patient_id", "PatientID", "LO", 0x00100020, LevelStudy, wildcard},
	{"birth_date", "PatientBirthDate", "DA", 0x00100030, LevelStudy, dateRange},
	{"sex", "PatientSex", "CS", 0x00100040, LevelStudy, exact},
	{"study_uid", "StudyInstanceUID", "UI", 0x0020000D, LevelStudy, uidList},
	{"study_id", "StudyID", "SH", 0x00200010, LevelStudy, wildcard},
	{"modality", "Modality", "CS", 0x00080060, LevelSeries, exact},
	{"series_desc", "SeriesDescription", "LO", 0x0008103E, LevelSeries, wildcard},
	{"body_part", "BodyPartExamined", "CS", 0x00180015, LevelSeries, exact},
	{"series_uid", "SeriesInstanceUID", "UI", 0x0020000E, LevelSeries, uidList},
	{"series_number", "SeriesNumber", "IS", 0x00200011, LevelSeries, exact},
	{"sop_class", "SOPClassUID", "UI", 0x00080016, LevelInstance, uidList},
	{"sop_uid", "SOPInstanceUID", "UI", 0x00080018, LevelInstance, uidList},
	{"instance_number", "InstanceNumber", "IS", 0x00200013, LevelInstance, exact},
	{"rows", "Rows", "US", 0x00280010, LevelInstance, exact},
	{"columns", "Columns", "US", 0x00280011, LevelInstance, exact},
	{"frames", "NumberOfFrames", "IS", 0x00280008, LevelInstance, exact},
}

func levelRank(l Level) int {
	switch l {
	case LevelStudy:
		return 0
	case LevelSeries:
		return 1
	}
	return 2
}

func findColumn(key string) (column, bool) {
	for _, c := range columns {
		if strings.EqualFold(key, c.keyword) || strings.EqualFold(key, fmt.Sprintf("%08X", c.tag)) {
			return c, true
		}
	}
	return column{}, false
}

// ErrUnsupportedMatch is a search on an attribute this archive does not index.
var ErrUnsupportedMatch = errors.New("dicomweb: unsupported matching attribute")

// Search runs a QIDO-RS query and returns one DICOM JSON object per match.
//
// An attribute this archive does not index is refused, never ignored: ignoring it returns more studies than were asked
// for, which on a search by patient is another patient's images.
func (a *Archive) Search(ctx context.Context, q Query, base string) ([]JSON, error) {
	var where []string
	var args []any
	if q.StudyUID != "" {
		where, args = append(where, "study_uid = ?"), append(args, q.StudyUID)
	}
	if q.SeriesUID != "" {
		where, args = append(where, "series_uid = ?"), append(args, q.SeriesUID)
	}
	keys := make([]string, 0, len(q.Match))
	for k := range q.Match {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := q.Match[key]
		c, ok := findColumn(key)
		if !ok {
			return nil, fmt.Errorf("%w: %s is not an attribute this archive matches on; it matches on %s",
				ErrUnsupportedMatch, key, keywords())
		}
		if levelRank(c.level) > levelRank(q.Level) {
			return nil, fmt.Errorf("%w: %s is a %s attribute and cannot narrow a %s search",
				ErrUnsupportedMatch, c.keyword, c.level, q.Level)
		}
		if value == "" {
			continue // universal matching: present in the query, matches everything
		}
		clause, vals := c.clause(value, q.Fuzzy)
		where, args = append(where, clause), append(args, vals...)
	}

	group := "sop_uid"
	switch q.Level {
	case LevelStudy:
		group = "study_uid"
	case LevelSeries:
		group = "series_uid"
	}
	sqlText := `SELECT study_uid, series_uid, sop_uid, sop_class, transfer_syntax, patient_id, patient_name,
		birth_date, sex, study_date, study_time, accession, study_id, study_desc, referring, modality, series_number,
		series_desc, body_part, instance_number, rows, columns, frames, charset,
		COUNT(DISTINCT series_uid), COUNT(*), GROUP_CONCAT(DISTINCT modality)
		FROM instances`
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sqlText += " GROUP BY " + group + " ORDER BY study_date DESC, study_uid, series_number+0, instance_number+0"
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	sqlText += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, max(q.Offset, 0))

	rows, err := a.db.Read.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JSON
	for rows.Next() {
		var v [24]sql.NullString
		var nSeries, nInstances int
		var modalities sql.NullString
		ptrs := make([]any, 0, 27)
		for i := range v {
			ptrs = append(ptrs, &v[i])
		}
		ptrs = append(ptrs, &nSeries, &nInstances, &modalities)
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		get := func(name string) string {
			for i, n := range resultColumns {
				if n == name {
					return v[i].String
				}
			}
			return ""
		}
		out = append(out, a.result(q.Level, get, nSeries, nInstances, modalities.String, base))
	}
	return out, rows.Err()
}

var resultColumns = []string{"study_uid", "series_uid", "sop_uid", "sop_class", "transfer_syntax", "patient_id",
	"patient_name", "birth_date", "sex", "study_date", "study_time", "accession", "study_id", "study_desc", "referring",
	"modality", "series_number", "series_desc", "body_part", "instance_number", "rows", "columns", "frames", "charset"}

// result builds one QIDO-RS response object.
func (a *Archive) result(level Level, get func(string) string, nSeries, nInstances int, modalities, base string) JSON {
	out := JSON{}
	put := func(tag uint32, vr, value string) {
		key := fmt.Sprintf("%08X", tag)
		if value == "" {
			out[key] = map[string]any{"vr": vr}
			return
		}
		out[key] = attrJSON(Attr{Tag: tag, VR: vr, Value: []byte(value)}, nil, nil)
	}
	for _, c := range columns {
		if levelRank(c.level) > levelRank(level) {
			continue
		}
		if level != LevelStudy && c.level == LevelStudy && c.tag != 0x0020000D {
			// Series and instance results carry the study UID and their own level's attributes.
			continue
		}
		if c.vr == "US" {
			if n, err := strconv.Atoi(get(c.name)); err == nil && n > 0 {
				out[fmt.Sprintf("%08X", c.tag)] = map[string]any{"vr": "US", "Value": []any{n}}
			}
			continue
		}
		put(c.tag, c.vr, get(c.name))
	}
	if cs := get("charset"); cs != "" {
		put(0x00080005, "CS", cs)
	}
	study := get("study_uid")
	url := base + "/studies/" + study
	switch level {
	case LevelStudy:
		mods := strings.ReplaceAll(modalities, ",", `\`)
		put(0x00080061, "CS", mods)
		out["00201206"] = map[string]any{"vr": "IS", "Value": []any{nSeries}}
		out["00201208"] = map[string]any{"vr": "IS", "Value": []any{nInstances}}
	case LevelSeries:
		url += "/series/" + get("series_uid")
		out["00201209"] = map[string]any{"vr": "IS", "Value": []any{nInstances}}
	case LevelInstance:
		url += "/series/" + get("series_uid") + "/instances/" + get("sop_uid")
	}
	out["00081190"] = map[string]any{"vr": "UR", "Value": []any{url}}
	return out
}

func keywords() string {
	var k []string
	for _, c := range columns {
		k = append(k, c.keyword)
	}
	return strings.Join(k, ", ")
}

// clause is the SQL for matching one attribute (PS3.4 C.2.2.2).
func (c column) clause(value string, fuzzy bool) (string, []any) {
	switch c.kind {
	case uidList:
		uids := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\\' })
		marks := strings.TrimSuffix(strings.Repeat("?,", len(uids)), ",")
		args := make([]any, len(uids))
		for i, u := range uids {
			args[i] = strings.TrimSpace(u)
		}
		return c.name + " IN (" + marks + ")", args
	case dateRange:
		value = strings.ReplaceAll(value, ".", "")
		if from, to, ok := strings.Cut(value, "-"); ok {
			switch {
			case from == "":
				return c.name + " <> '' AND " + c.name + " <= ?", []any{to}
			case to == "":
				return c.name + " >= ?", []any{from}
			}
			return c.name + " BETWEEN ? AND ?", []any{from, to}
		}
		return c.name + " = ?", []any{value}
	case pnMatch:
		// Case-insensitive, and with fuzzy matching a name matches from the start of any component, as OHIF's
		// patient search expects. Without fuzzy, wildcards are honoured and nothing else is assumed.
		if fuzzy && !strings.ContainsAny(value, "*?") {
			v := strings.ToUpper(value)
			return "(UPPER(" + c.name + ") LIKE ? ESCAPE '!' OR UPPER(" + c.name + ") LIKE ? ESCAPE '!')",
				[]any{likeEscape(v) + "%", "%^" + likeEscape(v) + "%"}
		}
		return "UPPER(" + c.name + ") LIKE ? ESCAPE '!'", []any{wildcardToLike(strings.ToUpper(value))}
	case wildcard:
		if strings.ContainsAny(value, "*?") {
			return c.name + " LIKE ? ESCAPE '!'", []any{wildcardToLike(value)}
		}
	}
	return c.name + " = ?", []any{value}
}

func likeEscape(s string) string {
	r := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return r.Replace(s)
}

func wildcardToLike(s string) string {
	s = likeEscape(s)
	return strings.NewReplacer("*", "%", "?", "_").Replace(s)
}

// Instance is one held object's location.
type Instance struct {
	StudyUID, SeriesUID, SOPUID, TransferSyntax, Path string
}

// Instances lists the instances of a study, series or single instance, in order.
func (a *Archive) Instances(ctx context.Context, study, series, sop string) ([]Instance, error) {
	q := `SELECT study_uid, series_uid, sop_uid, transfer_syntax, path FROM instances WHERE study_uid = ?`
	args := []any{study}
	if series != "" {
		q, args = q+" AND series_uid = ?", append(args, series)
	}
	if sop != "" {
		q, args = q+" AND sop_uid = ?", append(args, sop)
	}
	q += " ORDER BY series_number+0, series_uid, instance_number+0, sop_uid"
	rows, err := a.db.Read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Instance
	for rows.Next() {
		var in Instance
		var ts sql.NullString
		if err := rows.Scan(&in.StudyUID, &in.SeriesUID, &in.SOPUID, &ts, &in.Path); err != nil {
			return nil, err
		}
		in.TransferSyntax = ts.String
		in.Path = filepath.Join(a.Dir, in.Path)
		out = append(out, in)
	}
	return out, rows.Err()
}

// Read loads one instance's bytes.
func (a *Archive) Read(in Instance) ([]byte, error) {
	rel, err := filepath.Rel(a.Dir, in.Path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, errors.New("dicomweb: an index entry points outside the archive")
	}
	return os.ReadFile(in.Path) // #nosec G304 - a path built from validated UIDs under the archive directory
}

// Counts are the archive's totals, for the console.
type Counts struct {
	Studies, Series, Instances int
	Bytes                      int64
}

// Count returns the archive's totals.
func (a *Archive) Count(ctx context.Context) (Counts, error) {
	var c Counts
	var b sql.NullInt64
	err := a.db.Read.QueryRowContext(ctx, `SELECT COUNT(DISTINCT study_uid), COUNT(DISTINCT series_uid), COUNT(*),
		SUM(size) FROM instances`).Scan(&c.Studies, &c.Series, &c.Instances, &b)
	c.Bytes = b.Int64
	return c, err
}
