package xds

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
)

// Stored query ids (ITI TF-2a 3.18.4.1.2.3.7).
const (
	QFindDocuments               = "urn:uuid:14d4debf-8f97-4251-9a74-a90016b0af0d"
	QFindSubmissionSets          = "urn:uuid:f26abbcb-ac74-4422-8a30-edb644bbc1a9"
	QFindFolders                 = "urn:uuid:958f3006-baad-4929-a4de-ff1114824431"
	QGetAll                      = "urn:uuid:10b545ea-725c-446d-9b95-8aeb444eddf3"
	QGetDocuments                = "urn:uuid:5c4f972b-d56b-40ac-a5fc-c8ca9b40b9d4"
	QGetFolders                  = "urn:uuid:5737b14c-8a1a-4539-b659-e03a34a5e1e4"
	QGetAssociations             = "urn:uuid:a7ae438b-4bc2-4642-93e9-be891f7bb155"
	QGetDocumentsAndAssociations = "urn:uuid:bab9529a-4a10-40b3-a01f-f68a615d247a"
	QGetSubmissionSets           = "urn:uuid:51224314-5390-4169-9b91-b1980040715a"
	QGetSubmissionSetAndContents = "urn:uuid:e8e3cb2c-e39c-46b9-99e4-c12f57260b83"
	QGetFolderAndContents        = "urn:uuid:b909a503-523d-4517-8acf-8e5834dfc4c7"
	QGetFoldersForDocument       = "urn:uuid:10cae35a-c7f9-4cf5-b61e-fc3278ffb578"
	QGetRelatedDocuments         = "urn:uuid:d90e5407-b356-4d91-a89f-873917b4b0e6"
)

// QueryNames names the stored queries, for the screen.
var QueryNames = map[string]string{
	QFindDocuments: "FindDocuments", QFindSubmissionSets: "FindSubmissionSets", QFindFolders: "FindFolders", QGetAll: "GetAll",
	QGetDocuments: "GetDocuments", QGetFolders: "GetFolders", QGetAssociations: "GetAssociations",
	QGetDocumentsAndAssociations: "GetDocumentsAndAssociations", QGetSubmissionSets: "GetSubmissionSets",
	QGetSubmissionSetAndContents: "GetSubmissionSetAndContents", QGetFolderAndContents: "GetFolderAndContents",
	QGetFoldersForDocument: "GetFoldersForDocument", QGetRelatedDocuments: "GetRelatedDocuments",
}

// params are a stored query's slots: each slot is one parameter, a repeated slot is AND, the values of one are OR.
type params map[string][][]string

// values reads a slot's value list: ('a','b'), 'a', or 20040101 for a time.
func values(raw []string) []string {
	var out []string
	for _, v := range raw {
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
			v = v[1 : len(v)-1]
		}
		for _, part := range splitQuoted(v) {
			part = strings.TrimSpace(part)
			if len(part) >= 2 && part[0] == '\'' && part[len(part)-1] == '\'' {
				part = strings.ReplaceAll(part[1:len(part)-1], "''", "'")
			}
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// splitQuoted splits on commas outside single quotes.
func splitQuoted(s string) []string {
	var out []string
	var cur strings.Builder
	in := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' && in && i+1 < len(s) && s[i+1] == '\'':
			cur.WriteString("''")
			i++
		case c == '\'':
			in = !in
			cur.WriteByte(c)
		case c == ',' && !in:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(out, cur.String())
}

func readParams(slots []Slot) params {
	p := params{}
	for _, s := range slots {
		p[s.Name] = append(p[s.Name], values(s.Values))
	}
	return p
}

func (p params) one(name string) string {
	if v := p[name]; len(v) > 0 && len(v[0]) > 0 {
		return v[0][0]
	}
	return ""
}

func (p params) list(name string) []string {
	var out []string
	for _, v := range p[name] {
		out = append(out, v...)
	}
	return out
}

// Result is what a stored query found.
type Result struct {
	Documents    []Object
	Packages     []Object
	Associations []Object
}

// Query runs a stored query. home is the community the query asked for, checked by the XCA gateway before.
func (r *Registry) Query(ctx context.Context, q *AdhocQueryRequest) (*Result, []Problem) {
	if err := r.ready(ctx); err != nil {
		return nil, []Problem{problem(ErrRegistryError, "", "%v", err)}
	}
	p := readParams(q.Query.Slots)
	need := func(names ...string) []Problem {
		for _, n := range names {
			if len(p[n]) == 0 {
				return []Problem{problem(ErrQueryMissingParam, n, "%s needs %s", QueryNames[q.Query.ID], n)}
			}
		}
		return nil
	}
	single := func(names ...string) []Problem {
		for _, n := range names {
			if len(p[n]) > 1 || len(p.list(n)) > 1 {
				return []Problem{problem(ErrQueryParamNumber, n, "%s takes one value of %s", QueryNames[q.Query.ID], n)}
			}
		}
		return nil
	}
	one := func(a, b string) []Problem {
		if (len(p[a]) == 0) == (len(p[b]) == 0) {
			return []Problem{problem(ErrQueryParamNumber, a, "%s takes %s or %s, and exactly one of them", QueryNames[q.Query.ID], a, b)}
		}
		return nil
	}
	res := &Result{}
	var err error
	switch q.Query.ID {
	case QFindDocuments:
		if ps := append(need("$XDSDocumentEntryPatientId", "$XDSDocumentEntryStatus"), single("$XDSDocumentEntryPatientId")...); len(ps) > 0 {
			return nil, ps
		}
		res.Documents, err = r.findDocuments(ctx, p, p.one("$XDSDocumentEntryPatientId"))
	case QFindSubmissionSets:
		if ps := append(need("$XDSSubmissionSetPatientId", "$XDSSubmissionSetStatus"), single("$XDSSubmissionSetPatientId")...); len(ps) > 0 {
			return nil, ps
		}
		res.Packages, err = r.findPackages(ctx, KindSubmissionSet, p, "$XDSSubmissionSet")
	case QFindFolders:
		if ps := append(need("$XDSFolderPatientId", "$XDSFolderStatus"), single("$XDSFolderPatientId")...); len(ps) > 0 {
			return nil, ps
		}
		res.Packages, err = r.findPackages(ctx, KindFolder, p, "$XDSFolder")
	case QGetAll:
		if ps := append(need("$patientId", "$XDSDocumentEntryStatus", "$XDSSubmissionSetStatus", "$XDSFolderStatus"), single("$patientId")...); len(ps) > 0 {
			return nil, ps
		}
		patient := p.one("$patientId")
		dp := params{"$XDSDocumentEntryStatus": p["$XDSDocumentEntryStatus"], "$XDSDocumentEntryFormatCode": p["$XDSDocumentEntryFormatCode"],
			"$XDSDocumentEntryConfidentialityCode": p["$XDSDocumentEntryConfidentialityCode"], "$XDSDocumentEntryType": p["$XDSDocumentEntryType"]}
		if res.Documents, err = r.findDocuments(ctx, dp, patient); err != nil {
			break
		}
		var ss, fs []Object
		if ss, err = r.findPackages(ctx, KindSubmissionSet, params{"$XDSSubmissionSetPatientId": {{patient}}, "$XDSSubmissionSetStatus": p["$XDSSubmissionSetStatus"]}, "$XDSSubmissionSet"); err != nil {
			break
		}
		if fs, err = r.findPackages(ctx, KindFolder, params{"$XDSFolderPatientId": {{patient}}, "$XDSFolderStatus": p["$XDSFolderStatus"]}, "$XDSFolder"); err != nil {
			break
		}
		res.Packages = append(ss, fs...)
		res.Associations, err = r.associationsAmong(ctx, ids(res.Documents, res.Packages))
	case QGetDocuments, QGetDocumentsAndAssociations:
		if ps := one("$XDSDocumentEntryEntryUUID", "$XDSDocumentEntryUniqueId"); len(ps) > 0 {
			return nil, ps
		}
		res.Documents, err = r.byID(ctx, KindDocument, p.list("$XDSDocumentEntryEntryUUID"), p.list("$XDSDocumentEntryUniqueId"))
		if err == nil && q.Query.ID == QGetDocumentsAndAssociations {
			res.Associations, err = r.associationsTouching(ctx, ids(res.Documents), "")
		}
	case QGetFolders:
		if ps := one("$XDSFolderEntryUUID", "$XDSFolderUniqueId"); len(ps) > 0 {
			return nil, ps
		}
		res.Packages, err = r.byID(ctx, KindFolder, p.list("$XDSFolderEntryUUID"), p.list("$XDSFolderUniqueId"))
	case QGetAssociations:
		if ps := need("$uuid"); len(ps) > 0 {
			return nil, ps
		}
		res.Associations, err = r.associationsTouching(ctx, p.list("$uuid"), "")
	case QGetSubmissionSets:
		if ps := need("$uuid"); len(ps) > 0 {
			return nil, ps
		}
		var as []Object
		if as, err = r.associationsTo(ctx, p.list("$uuid"), AssocHasMember); err != nil {
			break
		}
		var ssIDs []string
		for _, a := range as {
			if k := r.kindOf(ctx, a.SourceObject); k == KindSubmissionSet {
				ssIDs = append(ssIDs, a.SourceObject)
				res.Associations = append(res.Associations, a)
			}
		}
		res.Packages, err = r.byID(ctx, KindSubmissionSet, ssIDs, nil)
	case QGetSubmissionSetAndContents, QGetFolderAndContents:
		kind, uuidP, uidP := KindSubmissionSet, "$XDSSubmissionSetEntryUUID", "$XDSSubmissionSetUniqueId"
		if q.Query.ID == QGetFolderAndContents {
			kind, uuidP, uidP = KindFolder, "$XDSFolderEntryUUID", "$XDSFolderUniqueId"
		}
		if ps := append(one(uuidP, uidP), single(uuidP, uidP)...); len(ps) > 0 {
			return nil, ps
		}
		var pkgs []Object
		if pkgs, err = r.byID(ctx, kind, p.list(uuidP), p.list(uidP)); err != nil || len(pkgs) == 0 {
			break
		}
		res.Packages = pkgs
		var as []Object
		if as, err = r.associationsFrom(ctx, pkgs[0].ID, AssocHasMember); err != nil {
			break
		}
		var members []string
		for _, a := range as {
			members = append(members, a.TargetObject)
		}
		var docs []Object
		if docs, err = r.byID(ctx, KindDocument, members, nil); err != nil {
			break
		}
		filter := params{"$XDSDocumentEntryFormatCode": p["$XDSDocumentEntryFormatCode"],
			"$XDSDocumentEntryConfidentialityCode": p["$XDSDocumentEntryConfidentialityCode"], "$XDSDocumentEntryType": p["$XDSDocumentEntryType"]}
		kept := map[string]bool{}
		for _, d := range docs {
			if ok, e := r.matchesCodes(ctx, d.ID, filter); e == nil && ok {
				res.Documents = append(res.Documents, d)
				kept[d.ID] = true
			}
		}
		if kind == KindSubmissionSet {
			var fs []Object
			if fs, err = r.byID(ctx, KindFolder, members, nil); err != nil {
				break
			}
			res.Packages = append(res.Packages, fs...)
			for _, f := range fs {
				kept[f.ID] = true
			}
		}
		keptIDs := map[string]bool{pkgs[0].ID: true}
		for k := range kept {
			keptIDs[k] = true
		}
		for _, a := range as {
			if kept[a.TargetObject] {
				res.Associations = append(res.Associations, a)
				keptIDs[a.ID] = true
			}
		}
		// Folder membership associations are themselves members of the SubmissionSet.
		for _, a := range as {
			if r.kindOf(ctx, a.TargetObject) == KindAssociation && !keptIDs[a.TargetObject] {
				if aa, e := r.byID(ctx, KindAssociation, []string{a.TargetObject}, nil); e == nil && len(aa) > 0 && kept[aa[0].TargetObject] {
					res.Associations = append(res.Associations, a, aa[0])
				}
			}
		}
	case QGetFoldersForDocument:
		if ps := one("$XDSDocumentEntryEntryUUID", "$XDSDocumentEntryUniqueId"); len(ps) > 0 {
			return nil, ps
		}
		var docs []Object
		if docs, err = r.byID(ctx, KindDocument, p.list("$XDSDocumentEntryEntryUUID"), p.list("$XDSDocumentEntryUniqueId")); err != nil || len(docs) == 0 {
			break
		}
		var as []Object
		if as, err = r.associationsTo(ctx, []string{docs[0].ID}, AssocHasMember); err != nil {
			break
		}
		var folders []string
		for _, a := range as {
			folders = append(folders, a.SourceObject)
		}
		res.Packages, err = r.byID(ctx, KindFolder, folders, nil)
	case QGetRelatedDocuments:
		if ps := append(one("$XDSDocumentEntryEntryUUID", "$XDSDocumentEntryUniqueId"), need("$AssociationTypes")...); len(ps) > 0 {
			return nil, ps
		}
		var docs []Object
		if docs, err = r.byID(ctx, KindDocument, p.list("$XDSDocumentEntryEntryUUID"), p.list("$XDSDocumentEntryUniqueId")); err != nil || len(docs) == 0 {
			break
		}
		types := p.list("$AssociationTypes")
		var as []Object
		for _, t := range types {
			got, e := r.associationsTouching(ctx, []string{docs[0].ID}, t)
			if e != nil {
				err = e
				break
			}
			as = append(as, got...)
		}
		if len(as) == 0 {
			break
		}
		related := []string{docs[0].ID}
		for _, a := range as {
			other := a.TargetObject
			if other == docs[0].ID {
				other = a.SourceObject
			}
			related = append(related, other)
		}
		res.Documents, err = r.byID(ctx, KindDocument, related, nil)
		res.Associations = as
	default:
		return nil, []Problem{problem(ErrUnknownStoredQuery, q.Query.ID, "%s is not a stored query this registry knows", q.Query.ID)}
	}
	if err != nil {
		return nil, []Problem{problem(ErrRegistryError, "", "%v", err)}
	}
	return res, nil
}

func ids(lists ...[]Object) []string {
	var out []string
	for _, l := range lists {
		for _, o := range l {
			out = append(out, o.ID)
		}
	}
	return out
}

func (r *Registry) kindOf(ctx context.Context, id string) string {
	var k string
	_ = r.DB.QueryRowContext(ctx, `SELECT kind FROM xds_objects WHERE id = ?`, id).Scan(&k)
	return k
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func anys(v []string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

func (r *Registry) rows(ctx context.Context, q string, args ...any) ([]Object, error) {
	rs, err := r.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Object
	for rs.Next() {
		var raw string
		if err := rs.Scan(&raw); err != nil {
			return nil, err
		}
		var o Object
		if err := xml.Unmarshal([]byte(raw), &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rs.Err()
}

func (r *Registry) byID(ctx context.Context, kind string, uuids, uniqueIDs []string) ([]Object, error) {
	if len(uuids) == 0 && len(uniqueIDs) == 0 {
		return nil, nil
	}
	if len(uuids) > 0 {
		return r.rows(ctx, `SELECT xml FROM xds_objects WHERE kind = ? AND id IN (`+placeholders(len(uuids))+`) ORDER BY registered, id`,
			append([]any{kind}, anys(uuids)...)...)
	}
	return r.rows(ctx, `SELECT xml FROM xds_objects WHERE kind = ? AND unique_id IN (`+placeholders(len(uniqueIDs))+`) ORDER BY registered, id`,
		append([]any{kind}, anys(uniqueIDs)...)...)
}

func (r *Registry) associationsTouching(ctx context.Context, objs []string, assocType string) ([]Object, error) {
	if len(objs) == 0 {
		return nil, nil
	}
	q := `SELECT xml FROM xds_objects WHERE kind = 'assoc' AND (source IN (` + placeholders(len(objs)) + `) OR target IN (` + placeholders(len(objs)) + `))`
	args := append(anys(objs), anys(objs)...)
	if assocType != "" {
		q += ` AND assoc_type = ?`
		args = append(args, assocType)
	}
	return r.rows(ctx, q+` ORDER BY registered, id`, args...)
}

func (r *Registry) associationsAmong(ctx context.Context, objs []string) ([]Object, error) {
	if len(objs) == 0 {
		return nil, nil
	}
	return r.rows(ctx, `SELECT xml FROM xds_objects WHERE kind = 'assoc' AND source IN (`+placeholders(len(objs))+`) AND target IN (`+
		placeholders(len(objs))+`) ORDER BY registered, id`, append(anys(objs), anys(objs)...)...)
}

func (r *Registry) associationsTo(ctx context.Context, targets []string, assocType string) ([]Object, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	return r.rows(ctx, `SELECT xml FROM xds_objects WHERE kind = 'assoc' AND assoc_type = ? AND target IN (`+placeholders(len(targets))+`) ORDER BY registered, id`,
		append([]any{assocType}, anys(targets)...)...)
}

func (r *Registry) associationsFrom(ctx context.Context, source, assocType string) ([]Object, error) {
	return r.rows(ctx, `SELECT xml FROM xds_objects WHERE kind = 'assoc' AND assoc_type = ? AND source = ? ORDER BY registered, id`, assocType, source)
}

// codeParams are the FindDocuments code parameters and their schemes. EventCodeList and ConfidentialityCode take repeated
// slots as AND; the rest are one list.
var codeParams = []struct{ param, scheme string }{
	{"$XDSDocumentEntryClassCode", SchemeClassCode}, {"$XDSDocumentEntryTypeCode", SchemeTypeCode},
	{"$XDSDocumentEntryPracticeSettingCode", SchemePracticeSetting}, {"$XDSDocumentEntryHealthcareFacilityTypeCode", SchemeFacilityType},
	{"$XDSDocumentEntryEventCodeList", SchemeEventCode}, {"$XDSDocumentEntryConfidentialityCode", SchemeConfidentiality},
	{"$XDSDocumentEntryFormatCode", SchemeFormatCode},
	{"$XDSSubmissionSetContentType", SchemeContentType}, {"$XDSFolderCodeList", SchemeFolderCode},
}

func (r *Registry) matchesCodes(ctx context.Context, id string, p params) (bool, error) {
	for _, cp := range codeParams {
		for _, group := range p[cp.param] {
			if len(group) == 0 {
				continue
			}
			var n int
			if err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM xds_codes WHERE object_id = ? AND scheme = ? AND code_key IN (`+
				placeholders(len(group))+`)`, append([]any{id, cp.scheme}, anys(group)...)...).Scan(&n); err != nil {
				return false, err
			}
			if n == 0 {
				return false, nil
			}
		}
	}
	if types := p.list("$XDSDocumentEntryType"); len(types) > 0 {
		var t string
		_ = r.DB.QueryRowContext(ctx, `SELECT object_type FROM xds_objects WHERE id = ?`, id).Scan(&t)
		found := false
		for _, want := range types {
			found = found || want == t
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

// like turns an XDS author pattern (% and _) into SQL LIKE, matched per author.
func likeAuthors(col string, patterns []string) (string, []any) {
	var conds []string
	var args []any
	for _, p := range patterns {
		conds = append(conds, `(char(10) || `+col+` || char(10)) LIKE ?`)
		args = append(args, "%\n"+strings.ReplaceAll(p, "\n", "")+"\n%")
	}
	return "(" + strings.Join(conds, " OR ") + ")", args
}

func timeRange(where *[]string, args *[]any, p params, from, to, col string) {
	if v := p.one(from); v != "" {
		*where = append(*where, col+" != '' AND "+col+" >= ?")
		*args = append(*args, pad(v))
	}
	if v := p.one(to); v != "" {
		*where = append(*where, col+" != '' AND "+col+" < ?")
		*args = append(*args, pad(v))
	}
}

func (r *Registry) findDocuments(ctx context.Context, p params, patient string) ([]Object, error) {
	where := []string{"kind = 'doc'", "patient_id = ?"}
	args := []any{patient}
	if st := p.list("$XDSDocumentEntryStatus"); len(st) > 0 {
		where = append(where, "status IN ("+placeholders(len(st))+")")
		args = append(args, anys(st)...)
	}
	timeRange(&where, &args, p, "$XDSDocumentEntryCreationTimeFrom", "$XDSDocumentEntryCreationTimeTo", "creation_time")
	timeRange(&where, &args, p, "$XDSDocumentEntryServiceStartTimeFrom", "$XDSDocumentEntryServiceStartTimeTo", "service_start")
	timeRange(&where, &args, p, "$XDSDocumentEntryServiceStopTimeFrom", "$XDSDocumentEntryServiceStopTimeTo", "service_stop")
	if a := p.list("$XDSDocumentEntryAuthorPerson"); len(a) > 0 {
		c, aa := likeAuthors("authors", a)
		where = append(where, c)
		args = append(args, aa...)
	}
	types := p.list("$XDSDocumentEntryType")
	if len(types) == 0 {
		types = []string{TypeStableDocument}
	}
	where = append(where, "object_type IN ("+placeholders(len(types))+")")
	args = append(args, anys(types)...)
	return r.filtered(ctx, `SELECT id, xml FROM xds_objects WHERE `+strings.Join(where, " AND ")+` ORDER BY creation_time DESC, id`, args, p)
}

func (r *Registry) filtered(ctx context.Context, q string, args []any, p params) ([]Object, error) {
	rs, err := r.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	type row struct{ id, raw string }
	var found []row
	for rs.Next() {
		var x row
		if err := rs.Scan(&x.id, &x.raw); err != nil {
			rs.Close()
			return nil, err
		}
		found = append(found, x)
	}
	rs.Close()
	var out []Object
	for _, x := range found {
		cp := params{}
		for k, v := range p {
			if k != "$XDSDocumentEntryType" {
				cp[k] = v
			}
		}
		ok, err := r.matchesCodes(ctx, x.id, cp)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var o Object
		if err := xml.Unmarshal([]byte(x.raw), &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func (r *Registry) findPackages(ctx context.Context, kind string, p params, prefix string) ([]Object, error) {
	where := []string{"kind = ?", "patient_id = ?"}
	args := []any{kind, p.one(prefix + "PatientId")}
	if st := p.list(prefix + "Status"); len(st) > 0 {
		where = append(where, "status IN ("+placeholders(len(st))+")")
		args = append(args, anys(st)...)
	}
	if kind == KindSubmissionSet {
		timeRange(&where, &args, p, "$XDSSubmissionSetSubmissionTimeFrom", "$XDSSubmissionSetSubmissionTimeTo", "submission_time")
		if s := p.list("$XDSSubmissionSetSourceId"); len(s) > 0 {
			where = append(where, "source_id IN ("+placeholders(len(s))+")")
			args = append(args, anys(s)...)
		}
		if a := p.list("$XDSSubmissionSetAuthorPerson"); len(a) > 0 {
			c, aa := likeAuthors("authors", a)
			where = append(where, c)
			args = append(args, aa...)
		}
	} else {
		timeRange(&where, &args, p, "$XDSFolderLastUpdateTimeFrom", "$XDSFolderLastUpdateTimeTo", "last_update")
	}
	return r.filtered(ctx, `SELECT id, xml FROM xds_objects WHERE `+strings.Join(where, " AND ")+` ORDER BY registered DESC, id`, args, p)
}

// QueryResponse writes a stored query's answer, as LeafClass (whole objects) or ObjectRef. home, for XCA, is set on every
// object returned.
func QueryResponse(res *Result, problems []Problem, returnType, home string) AdhocQueryResponse {
	rr := Response(problems)
	out := AdhocQueryResponse{Status: rr.Status, Errors: rr.Errors, Objects: &RegistryObjectList{}}
	if res == nil {
		return out
	}
	seen := map[string]bool{}
	add := func(list []Object, into *[]Object) {
		for _, o := range list {
			if seen[o.ID] {
				continue
			}
			seen[o.ID] = true
			if home != "" {
				o.Home = home
			}
			*into = append(*into, o)
		}
	}
	if strings.EqualFold(returnType, "ObjectRef") {
		var all []Object
		add(res.Documents, &all)
		add(res.Packages, &all)
		add(res.Associations, &all)
		for _, o := range all {
			out.Objects.ObjectRefs = append(out.Objects.ObjectRefs, ObjectRef{ID: o.ID, Home: o.Home})
		}
		return out
	}
	add(res.Documents, &out.Objects.Extrinsic)
	add(res.Packages, &out.Objects.Packages)
	add(res.Associations, &out.Objects.Associations)
	sort.SliceStable(out.Objects.Associations, func(i, j int) bool { return false })
	return out
}

// Retrieve answers ITI-43 (and ITI-39 with home set): each document this repository holds, and an error for each it does not.
func (r *Registry) Retrieve(ctx context.Context, req *RetrieveRequest, home string) (RetrieveResponse, error) {
	var out RetrieveResponse
	var ps []Problem
	for _, dr := range req.Requests {
		switch {
		case home != "" && dr.HomeCommunityID != home:
			ps = append(ps, problem(ErrUnknownCommunity, dr.DocumentUniqueID, "this gateway answers for community %s, not %s", home, dr.HomeCommunityID))
			continue
		case dr.RepositoryUniqueID != r.RepositoryID:
			ps = append(ps, problem(ErrUnknownRepository, dr.DocumentUniqueID, "this repository is %s, not %s", r.RepositoryID, dr.RepositoryUniqueID))
			continue
		}
		b, mt, err := r.Content(ctx, dr.DocumentUniqueID)
		if err == ErrNoDocument {
			ps = append(ps, problem(ErrMissingDocument, dr.DocumentUniqueID, "no document %s is in this repository", dr.DocumentUniqueID))
			continue
		}
		if err != nil {
			return out, err
		}
		out.Documents = append(out.Documents, DocumentResponse{HomeCommunityID: home, RepositoryUniqueID: r.RepositoryID,
			DocumentUniqueID: dr.DocumentUniqueID, MimeType: mt, Document: Document{Content: b}})
	}
	out.Status = Response(ps)
	if len(out.Documents) > 0 && len(ps) > 0 {
		out.Status.Status = ResponsePartialSuccess
	}
	if len(req.Requests) == 0 {
		out.Status = Response([]Problem{problem(ErrRepositoryError, "", "the request names no document")})
	}
	return out, nil
}

// Documents lists the latest DocumentEntries, for the screen.
func (r *Registry) Documents(ctx context.Context, patient string, limit int) ([]Object, error) {
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if patient != "" {
		return r.rows(ctx, `SELECT xml FROM xds_objects WHERE kind = 'doc' AND patient_id LIKE ? ORDER BY registered DESC LIMIT ?`,
			patient+"%", limit)
	}
	return r.rows(ctx, `SELECT xml FROM xds_objects WHERE kind = 'doc' ORDER BY registered DESC LIMIT ?`, limit)
}

var _ = sql.ErrNoRows
var _ = fmt.Sprint
