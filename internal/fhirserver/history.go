package fhirserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// History, versioned reads, and the concurrency control that depends on them.
//
// The concurrency control is the part that matters most, and it is easy to overlook because it looks like a refinement. Without If-Match,
// two apps that read the same resource and both write it produce one silent loss: the second write wins completely and nothing anywhere
// records that the first happened. For a medication list or a problem list that is a clinical safety issue, not an inconvenience.
//
// History is also what makes the loss recoverable when it does happen, which is why the two arrived together.

// ErrVersionMismatch means the caller's If-Match did not match the stored version.
var ErrVersionMismatch = errors.New("fhirserver: the resource has changed since it was read")

// ErrNoSuchVersion means the requested version does not exist.
var ErrNoSuchVersion = errors.New("fhirserver: no such version")

// Version is one entry in a resource's history.
type Version struct {
	ResourceType string
	ID           string
	VersionID    int
	LastUpdated  time.Time

	// Resource is nil for a deletion, which is the point of recording one.
	Resource fhir.Resource

	// Deleted distinguishes a deletion from a version whose content could not be read, which would otherwise both
	// arrive as a nil resource.
	Deleted bool
}

// CurrentVersion reports the version a resource is at, and whether it is deleted.
//
// Returns ErrNotFound for a resource that never existed, and the version of a deleted one otherwise. A deleted resource still has a
// version, and an If-Match against it has to be able to succeed - otherwise a client cannot resurrect a record it deleted by mistake
// without first guessing the number.
func (s *Store) CurrentVersion(ctx context.Context, resourceType, id string) (int, bool, error) {
	var (
		versionID int
		deleted   int
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT version_id, deleted FROM fhir_resources WHERE resource_type = ? AND resource_id = ?`,
		resourceType, id).Scan(&versionID, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, ErrNotFound
	}
	if err != nil {
		return 0, false, err
	}

	return versionID, deleted != 0, nil
}

// CheckVersion compares a caller's expected version against the stored one.
//
// Called before a write when the request carried If-Match. An empty expectation passes, because a client that did not ask for optimistic
// concurrency is not required to use it - requiring it would break every existing client, and the ones that do send If-Match are the ones
// that care.
//
// A resource that does not exist yet passes too. If-Match on a create is a client asserting a version for something with no versions, and
// refusing there would block a legitimate create-if-absent; the specification puts that behind If-None-Exist instead.
func (s *Store) CheckVersion(ctx context.Context, resourceType, id, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return nil
	}

	want, err := ParseETag(expected)
	if err != nil {
		return err
	}

	current, _, err := s.CurrentVersion(ctx, resourceType, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	if current != want {
		return fmt.Errorf("%w: it is now version %d and the request expected %d",
			ErrVersionMismatch, current, want)
	}

	return nil
}

// ParseETag reads a version out of an ETag or If-Match header value.
//
// FHIR uses weak ETags, spelled W/"3". Every part of that is optional in what clients actually send: the W/ prefix, the quotes, and
// occasionally both. All forms are accepted because rejecting a valid-but-unusual spelling turns optimistic concurrency into a permanent
// 412, and a client that cannot write at all will simply stop sending the header.
func ParseETag(value string) (int, error) {
	raw := strings.TrimSpace(value)

	// A list of ETags is not supported, and saying so beats matching the first: If-Match: "1", "2" means the client will
	// accept either, and honouring only one produces a refusal the client cannot explain.
	if strings.Contains(raw, ",") {
		return 0, fmt.Errorf("If-Match with several versions is not supported, and %q has more than one", value)
	}

	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, `"`)
	raw = strings.TrimSpace(raw)

	if raw == "*" {
		// Means "any version", which is the same as not asking.
		return 0, fmt.Errorf("If-Match: * is not supported; omit the header instead, which means the same thing")
	}

	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%q is not a version identifier; these are whole numbers, sent as W/\"3\"", value)
	}

	return n, nil
}

// ETag formats a version as a FHIR weak ETag.
func ETag(versionID int) string {
	return fmt.Sprintf(`W/"%d"`, versionID)
}

// GetVersion reads one historical version of a resource.
//
// A deleted version returns ErrDeleted rather than the resource, matching what a current read of a deleted resource does. The alternative -
// returning nothing - would make a deletion indistinguishable from a version that was never recorded.
func (s *Store) GetVersion(ctx context.Context, resourceType, id string, versionID int) (fhir.Resource, error) {
	var (
		content sql.NullString
		deleted int
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT content, deleted FROM fhir_history
		 WHERE resource_type = ? AND resource_id = ? AND version_id = ?`,
		resourceType, id, versionID).Scan(&content, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSuchVersion
	}
	if err != nil {
		return nil, err
	}
	if deleted != 0 || !content.Valid {
		return nil, ErrDeleted
	}

	r, err := fhir.UnmarshalResource([]byte(content.String))
	if err != nil {
		return nil, fmt.Errorf("fhirserver: a stored version could not be read: %w", err)
	}

	return r, nil
}

// History reads the versions of one resource, newest first.
//
// Newest first because that is the order somebody reads an audit trail: the question is nearly always "what happened most recently", and a
// resource with two hundred versions would otherwise open on the least interesting one.
func (s *Store) History(ctx context.Context, resourceType, id string, limit int) ([]Version, error) {
	if limit <= 0 {
		limit = DefaultCount
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT version_id, last_updated, deleted, content FROM fhir_history
		 WHERE resource_type = ? AND resource_id = ?
		 ORDER BY version_id DESC LIMIT ?`,
		resourceType, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	versions, err := scanVersions(rows, resourceType, id)
	if err != nil {
		return nil, err
	}

	if len(versions) == 0 {
		// Distinguished from a resource with no history, which cannot happen: every write records a version. So an
		// empty result means the resource never existed, and saying "no history" would suggest otherwise.
		return nil, ErrNotFound
	}

	return versions, nil
}

// TypeHistory reads recent versions across every resource of a type, newest first.
func (s *Store) TypeHistory(ctx context.Context, resourceType string, limit int) ([]Version, error) {
	if limit <= 0 {
		limit = DefaultCount
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT resource_id, version_id, last_updated, deleted, content FROM fhir_history
		 WHERE resource_type = ?
		 ORDER BY last_updated DESC, version_id DESC LIMIT ?`,
		resourceType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Version
	for rows.Next() {
		var (
			id      string
			v       Version
			updated string
			deleted int
			content sql.NullString
		)
		if err := rows.Scan(&id, &v.VersionID, &updated, &deleted, &content); err != nil {
			return nil, err
		}

		v.ResourceType = resourceType
		v.ID = id
		v.LastUpdated, _ = time.Parse(time.RFC3339Nano, updated)
		v.Deleted = deleted != 0

		if content.Valid && !v.Deleted {
			r, err := fhir.UnmarshalResource([]byte(content.String))
			if err != nil {
				// Skipped rather than failing the whole history.
				//
				// A version this build cannot parse is one an earlier build wrote. Refusing the request
				// would make the audit trail unreadable because of one old row, which is the opposite of
				// what an audit trail is for.
				continue
			}
			v.Resource = r
		}

		out = append(out, v)
	}

	return out, rows.Err()
}

// scanVersions reads instance history rows.
func scanVersions(rows *sql.Rows, resourceType, id string) ([]Version, error) {
	var out []Version

	for rows.Next() {
		var (
			v       Version
			updated string
			deleted int
			content sql.NullString
		)
		if err := rows.Scan(&v.VersionID, &updated, &deleted, &content); err != nil {
			return nil, err
		}

		v.ResourceType = resourceType
		v.ID = id
		v.LastUpdated, _ = time.Parse(time.RFC3339Nano, updated)
		v.Deleted = deleted != 0

		if content.Valid && !v.Deleted {
			r, err := fhir.UnmarshalResource([]byte(content.String))
			if err != nil {
				continue
			}
			v.Resource = r
		}

		out = append(out, v)
	}

	return out, rows.Err()
}

// HistoryBundle wraps versions as a history bundle.
//
// The request method on each entry is what makes a history readable: POST for the first version, PUT for a change, DELETE for a removal.
// Without it a client sees three versions and cannot tell which one removed the record.
func (s *Store) HistoryBundle(versions []Version, baseURL, selfURL string) *fhir.Bundle {
	b := &fhir.Bundle{
		Type:      "history",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Total:     fhir.Int(len(versions)),
	}
	b.SetResourceID("history")
	b.Link = []fhir.BundleLink{{Relation: "self", URL: selfURL}}

	base := strings.TrimRight(baseURL, "/")

	for _, v := range versions {
		method := "PUT"

		switch {
		case v.Deleted:
			method = "DELETE"
		case v.VersionID == 1:
			method = "POST"
		}

		entry := fhir.BundleEntry{
			FullURL: fmt.Sprintf("%s/%s/%s", base, v.ResourceType, v.ID),
			Request: &fhir.BundleRequest{
				Method: method,
				URL:    fmt.Sprintf("%s/%s", v.ResourceType, v.ID),
			},
			Response: &fhir.BundleResponse{
				Status:       responseStatusFor(method),
				Etag:         ETag(v.VersionID),
				LastModified: v.LastUpdated.UTC().Format(time.RFC3339),
			},
		}
		// Absent for a deletion rather than an empty object, because an entry carrying an empty resource reads as a
		// record that was blanked rather than removed.
		//
		// Assigned unconditionally, and a plant is why: guarding on non-nil first was identical in behaviour,
		// because a nil interface here is omitted by the json tag anyway. The guard read as protection and could
		// not change an outcome, so it is gone and the test that requires a deletion to carry no resource is what
		// holds this.
		entry.Resource = v.Resource

		b.Entry = append(b.Entry, entry)
	}

	return b
}

// responseStatusFor is the status a history entry reports for the interaction that produced it.
func responseStatusFor(method string) string {
	switch method {
	case "POST":
		return "201 Created"
	case "DELETE":
		return "204 No Content"
	default:
		return "200 OK"
	}
}
