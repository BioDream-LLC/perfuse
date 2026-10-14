package dicomweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Handler serves an archive over DICOMweb: QIDO-RS, WADO-RS, WADO-URI and STOW-RS.
//
// Mounted under a prefix by the caller, which also decides who may call it; Base is the absolute URL of that prefix,
// used in RetrieveURL and BulkDataURI so a viewer can follow them.
type Handler struct {
	Archive *Archive
	Base    string
	Log     *slog.Logger

	// MaxObject and MaxRequest bound a STOW-RS request. Defaults: 512 MB per object, 2 GB per request.
	MaxObject, MaxRequest int64

	// Stored, when set, is told about each object STOW-RS stores, for auditing.
	Stored func(ctx context.Context, s *Stored)
	// Read, when set, is told about each study, series or instance retrieved or searched, for auditing.
	Read func(ctx context.Context, what string)
}

// ServeHTTP routes one request. Paths are relative to the mount point.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		parts = nil
	}
	for _, p := range parts {
		if strings.Contains(p, "..") {
			h.problem(w, http.StatusBadRequest, "the path is not a DICOMweb resource")
			return
		}
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		h.stow(w, r, parts)
		return
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		h.problem(w, http.StatusMethodNotAllowed, "DICOMweb here answers GET (QIDO-RS, WADO-RS, WADO-URI) and POST (STOW-RS)")
		return
	}

	if len(parts) == 1 && parts[0] == "wado" {
		h.wadoURI(w, r)
		return
	}

	// /studies, /series, /instances
	// /studies/{s}[/metadata|/series[/{r}[/metadata|/instances[/{i}[/metadata|/frames/{f}|/bulkdata/{t}]]]]|/instances]
	var study, series, sop string
	rest := parts
	take := func(name string) bool {
		if len(rest) >= 2 && rest[0] == name {
			switch name {
			case "studies":
				study = rest[1]
			case "series":
				series = rest[1]
			case "instances":
				sop = rest[1]
			}
			rest = rest[2:]
			return true
		}
		return false
	}
	take("studies")
	take("series")
	take("instances")
	for _, uid := range []string{study, series, sop} {
		if uid != "" && !ValidUID(uid) {
			h.problem(w, http.StatusBadRequest, fmt.Sprintf("%q is not a UID", uid))
			return
		}
	}

	switch {
	case len(rest) == 1 && rest[0] == "studies" && study == "":
		h.qido(w, r, Query{Level: LevelStudy})
	case len(rest) == 1 && rest[0] == "series" && series == "" && sop == "":
		h.qido(w, r, Query{Level: LevelSeries, StudyUID: study})
	case len(rest) == 1 && rest[0] == "instances" && sop == "":
		h.qido(w, r, Query{Level: LevelInstance, StudyUID: study, SeriesUID: series})
	case len(rest) == 0 && study != "":
		h.retrieve(w, r, study, series, sop)
	case len(rest) == 1 && rest[0] == "metadata" && study != "":
		h.metadata(w, r, study, series, sop)
	case len(rest) == 2 && rest[0] == "frames" && sop != "":
		h.frames(w, r, study, series, sop, rest[1])
	case len(rest) == 2 && rest[0] == "bulkdata" && sop != "":
		h.bulk(w, r, study, series, sop, rest[1])
	default:
		h.problem(w, http.StatusNotFound, "not a DICOMweb resource this archive serves: /"+strings.Join(parts, "/"))
	}
}

func (h *Handler) problem(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// PS3.18 8.6.3 carries a status message in a Warning header; a viewer shows it.
	w.Header().Set("Warning", "299 perfuse "+strconv.Quote(msg))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg + "\n"))
}

func (h *Handler) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/dicom+json")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

// qido answers a search.
func (h *Handler) qido(w http.ResponseWriter, r *http.Request, q Query) {
	q.Match = map[string]string{}
	for key, vals := range r.URL.Query() {
		v := ""
		if len(vals) > 0 {
			v = vals[0]
		}
		switch strings.ToLower(key) {
		case "limit":
			q.Limit, _ = strconv.Atoi(v)
		case "offset":
			q.Offset, _ = strconv.Atoi(v)
		case "fuzzymatching":
			q.Fuzzy = strings.EqualFold(v, "true")
		case "includefield":
			// Every indexed attribute is always returned, which is a superset of what any includefield can ask
			// for from this archive; there is nothing else to include.
		default:
			q.Match[key] = v
		}
	}
	results, err := h.Archive.Search(r.Context(), q, h.Base)
	if errors.Is(err, ErrUnsupportedMatch) {
		h.problem(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		h.Log.Error("dicomweb search failed", "err", err)
		h.problem(w, http.StatusInternalServerError, "the search could not be run")
		return
	}
	if h.Read != nil {
		h.Read(r.Context(), fmt.Sprintf("search %s %s", q.Level, r.URL.RawQuery))
	}
	if len(results) == 0 {
		// PS3.18 10.6.3.3: no matches is 204, not an empty array.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.writeJSON(w, r, http.StatusOK, results)
}

func (h *Handler) instances(w http.ResponseWriter, r *http.Request, study, series, sop string) ([]Instance, bool) {
	list, err := h.Archive.Instances(r.Context(), study, series, sop)
	if err != nil {
		h.Log.Error("dicomweb read failed", "err", err)
		h.problem(w, http.StatusInternalServerError, "the archive could not be read")
		return nil, false
	}
	if len(list) == 0 {
		h.problem(w, http.StatusNotFound, "this archive holds no such study, series or instance")
		return nil, false
	}
	return list, true
}

func (h *Handler) instanceURL(in Instance) string {
	return h.Base + "/studies/" + in.StudyUID + "/series/" + in.SeriesUID + "/instances/" + in.SOPUID
}

// retrieve returns whole objects as multipart/related application/dicom.
func (h *Handler) retrieve(w http.ResponseWriter, r *http.Request, study, series, sop string) {
	if !accepts(r, "multipart/related", "application/dicom") && !accepts(r, "application/dicom", "") {
		h.problem(w, http.StatusNotAcceptable, "objects are returned as multipart/related; type=\"application/dicom\", "+
			"as stored; this archive does not transcode")
		return
	}
	list, ok := h.instances(w, r, study, series, sop)
	if !ok {
		return
	}
	var parts []Part
	for _, in := range list {
		raw, err := h.Archive.Read(in)
		if err != nil {
			h.Log.Error("dicomweb: an indexed object could not be read", "sop", in.SOPUID, "err", err)
			h.problem(w, http.StatusInternalServerError, "an object in the archive could not be read")
			return
		}
		parts = append(parts, Part{ContentType: "application/dicom; transfer-syntax=" + in.TransferSyntax,
			Location: h.instanceURL(in), Body: raw})
	}
	if h.Read != nil {
		h.Read(r.Context(), "retrieve "+describe(study, series, sop))
	}
	h.writeParts(w, r, "application/dicom", parts)
}

func (h *Handler) writeParts(w http.ResponseWriter, r *http.Request, partType string, parts []Part) {
	var buf bytes.Buffer
	ct, err := WriteMultipart(&buf, partType, parts)
	if err != nil {
		h.problem(w, http.StatusInternalServerError, "the response could not be built")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(buf.Bytes())
	}
}

// metadata returns every instance's attributes, pixel data and large binary values as BulkDataURIs.
func (h *Handler) metadata(w http.ResponseWriter, r *http.Request, study, series, sop string) {
	list, ok := h.instances(w, r, study, series, sop)
	if !ok {
		return
	}
	out := make([]JSON, 0, len(list))
	for _, in := range list {
		raw, err := h.Archive.Read(in)
		if err != nil {
			h.problem(w, http.StatusInternalServerError, "an object in the archive could not be read")
			return
		}
		obj, err := Parse(raw)
		if err != nil {
			h.problem(w, http.StatusInternalServerError, "an object in the archive could not be parsed: "+err.Error())
			return
		}
		url := h.instanceURL(in)
		out = append(out, ToJSON(obj.Attrs, obj.Order(), func(tag uint32) string {
			if tag == 0x7FE00010 {
				return url + "/frames/1"
			}
			return fmt.Sprintf("%s/bulkdata/%08X", url, tag)
		}, nil))
	}
	if h.Read != nil {
		h.Read(r.Context(), "metadata "+describe(study, series, sop))
	}
	h.writeJSON(w, r, http.StatusOK, out)
}

func (h *Handler) loadOne(w http.ResponseWriter, r *http.Request, study, series, sop string) (*Object, Instance, bool) {
	list, ok := h.instances(w, r, study, series, sop)
	if !ok {
		return nil, Instance{}, false
	}
	raw, err := h.Archive.Read(list[0])
	if err != nil {
		h.problem(w, http.StatusInternalServerError, "the object could not be read")
		return nil, list[0], false
	}
	obj, err := Parse(raw)
	if err != nil {
		h.problem(w, http.StatusInternalServerError, "the object could not be parsed: "+err.Error())
		return nil, list[0], false
	}
	return obj, list[0], true
}

// frames returns pixel data frames as stored.
func (h *Handler) frames(w http.ResponseWriter, r *http.Request, study, series, sop, list string) {
	want, err := ParseFrameList(list)
	if err != nil {
		h.problem(w, http.StatusBadRequest, err.Error())
		return
	}
	obj, _, ok := h.loadOne(w, r, study, series, sop)
	if !ok {
		return
	}
	media := FrameMediaType(obj.TransferSyntax)
	if !acceptsFrames(r, media, obj.TransferSyntax) {
		h.problem(w, http.StatusNotAcceptable, fmt.Sprintf("the frames are stored as %s (transfer syntax %s) and "+
			"this archive does not transcode; ask for that, or for transfer-syntax=*", media, obj.TransferSyntax))
		return
	}
	frames, err := obj.Frames(want)
	if errors.Is(err, ErrFramesUnavailable) {
		h.problem(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		h.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	parts := make([]Part, len(frames))
	for i, f := range frames {
		parts[i] = Part{ContentType: media + "; transfer-syntax=" + obj.TransferSyntax, Body: f}
	}
	h.writeParts(w, r, media, parts)
}

// bulk returns one bulk value by tag.
func (h *Handler) bulk(w http.ResponseWriter, r *http.Request, study, series, sop, tagText string) {
	tag64, err := strconv.ParseUint(tagText, 16, 32)
	if err != nil || len(tagText) != 8 {
		h.problem(w, http.StatusBadRequest, "a bulk data tag is eight hexadecimal digits")
		return
	}
	obj, _, ok := h.loadOne(w, r, study, series, sop)
	if !ok {
		return
	}
	a, found := Find(obj.Attrs, uint32(tag64))
	if !found || !a.Bulk {
		h.problem(w, http.StatusNotFound, "this instance has no bulk value at "+tagText)
		return
	}
	body := obj.Raw[a.Offset : a.Offset+a.Length]
	h.writeParts(w, r, "application/octet-stream", []Part{{Body: body}})
}

// wadoURI is WADO-URI (PS3.18 9), which OHIF's wadouri image loader and many older viewers use: the whole file.
func (h *Handler) wadoURI(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("requestType") != "WADO" {
		h.problem(w, http.StatusBadRequest, "WADO-URI needs requestType=WADO")
		return
	}
	study, series, sop := q.Get("studyUID"), q.Get("seriesUID"), q.Get("objectUID")
	for _, uid := range []string{study, series, sop} {
		if !ValidUID(uid) {
			h.problem(w, http.StatusBadRequest, "WADO-URI needs studyUID, seriesUID and objectUID, each a UID")
			return
		}
	}
	if ct := q.Get("contentType"); ct != "" && !strings.Contains(ct, "application/dicom") && ct != "*/*" {
		h.problem(w, http.StatusNotAcceptable, "this archive returns objects as application/dicom; it renders no images")
		return
	}
	list, ok := h.instances(w, r, study, series, sop)
	if !ok {
		return
	}
	raw, err := h.Archive.Read(list[0])
	if err != nil {
		h.problem(w, http.StatusInternalServerError, "the object could not be read")
		return
	}
	if h.Read != nil {
		h.Read(r.Context(), "retrieve "+describe(study, series, sop))
	}
	w.Header().Set("Content-Type", "application/dicom")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(raw)
	}
}

// stow stores objects (STOW-RS) and answers with the PS3.18 10.5.3 response.
func (h *Handler) stow(w http.ResponseWriter, r *http.Request, parts []string) {
	study := ""
	switch {
	case len(parts) == 1 && parts[0] == "studies":
	case len(parts) == 2 && parts[0] == "studies" && ValidUID(parts[1]):
		study = parts[1]
	default:
		h.problem(w, http.StatusNotFound, "STOW-RS is POST /studies or POST /studies/{StudyInstanceUID}")
		return
	}
	maxObj, maxReq := h.MaxObject, h.MaxRequest
	if maxObj <= 0 {
		maxObj = 512 << 20
	}
	if maxReq <= 0 {
		maxReq = 2 << 30
	}
	partType, objects, err := ReadMultipart(r.Header.Get("Content-Type"), http.MaxBytesReader(w, r.Body, maxReq), maxObj, maxReq)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "limit") {
			status = http.StatusRequestEntityTooLarge
		}
		h.problem(w, status, err.Error())
		return
	}
	if mt, _, _ := ParseMediaType(partType); mt != "" && mt != "application/dicom" {
		// The JSON and XML forms carry metadata and bulk data separately and are not accepted here.
		h.problem(w, http.StatusUnsupportedMediaType, "STOW-RS here takes application/dicom parts; "+partType+" is not one")
		return
	}
	if len(objects) == 0 {
		h.problem(w, http.StatusBadRequest, "the request carries no objects")
		return
	}

	var stored, failed []any
	for _, p := range objects {
		if mt, _, _ := ParseMediaType(p.ContentType); mt != "application/dicom" {
			failed = append(failed, failure("", "", FailureNotSupported))
			continue
		}
		st, err := h.Archive.Store(r.Context(), p.Body, study)
		var se *StoreError
		if errors.As(err, &se) {
			h.Log.Warn("dicomweb: an object was not stored", "sop", se.SOPUID, "reason", se.Message)
			failed = append(failed, failure(se.SOPClassUID, se.SOPUID, se.Reason))
			continue
		}
		if err != nil {
			failed = append(failed, failure("", "", FailureProcessing))
			continue
		}
		if h.Stored != nil {
			h.Stored(r.Context(), st)
		}
		stored = append(stored, JSON{
			"00081150": map[string]any{"vr": "UI", "Value": []any{st.SOPClass}},
			"00081155": map[string]any{"vr": "UI", "Value": []any{st.SOPUID}},
			"00081190": map[string]any{"vr": "UR", "Value": []any{
				h.Base + "/studies/" + st.StudyUID + "/series/" + st.SeriesUID + "/instances/" + st.SOPUID}},
		})
	}

	resp := JSON{}
	if study != "" {
		resp["00081190"] = map[string]any{"vr": "UR", "Value": []any{h.Base + "/studies/" + study}}
	}
	if len(stored) > 0 {
		resp["00081199"] = map[string]any{"vr": "SQ", "Value": stored}
	}
	if len(failed) > 0 {
		resp["00081198"] = map[string]any{"vr": "SQ", "Value": failed}
	}
	status := http.StatusOK
	switch {
	case len(stored) == 0:
		status = http.StatusConflict
	case len(failed) > 0:
		status = http.StatusAccepted
	}
	h.writeJSON(w, r, status, resp)
}

func failure(class, sop string, reason uint16) JSON {
	j := JSON{"00081197": map[string]any{"vr": "US", "Value": []any{reason}}}
	if class != "" {
		j["00081150"] = map[string]any{"vr": "UI", "Value": []any{class}}
	}
	if sop != "" {
		j["00081155"] = map[string]any{"vr": "UI", "Value": []any{sop}}
	}
	return j
}

func describe(study, series, sop string) string {
	s := "study " + study
	if series != "" {
		s += " series " + series
	}
	if sop != "" {
		s += " instance " + sop
	}
	return s
}

// accepts reports whether the Accept header allows a media type (and, for multipart, a part type).
func accepts(r *http.Request, media, partType string) bool {
	header := r.Header.Get("Accept")
	if header == "" {
		return true
	}
	for _, item := range strings.Split(header, ",") {
		mt, params, err := ParseMediaType(strings.TrimSpace(item))
		if err != nil {
			continue
		}
		if mt == "*/*" {
			return true
		}
		if mt != media {
			continue
		}
		if partType == "" || params["type"] == "" || params["type"] == partType || params["type"] == "*/*" {
			return true
		}
	}
	return false
}

// acceptsFrames reports whether the Accept header allows frames in the stored form.
func acceptsFrames(r *http.Request, media, ts string) bool {
	header := r.Header.Get("Accept")
	if header == "" {
		return true
	}
	for _, item := range strings.Split(header, ",") {
		mt, params, err := ParseMediaType(strings.TrimSpace(item))
		if err != nil {
			continue
		}
		if mt == "*/*" {
			return true
		}
		inner := mt
		if mt == "multipart/related" {
			inner = params["type"]
			if inner == "" {
				inner = "application/octet-stream"
			}
		}
		syntax := params["transfer-syntax"]
		if inner != media && inner != "*/*" {
			continue
		}
		// Without a transfer-syntax, octet-stream means explicit VR little endian (PS3.18 8.7.3.5.2).
		if syntax == "" && inner == "application/octet-stream" {
			syntax = explicitVRLittleEndian
		}
		if syntax == "" || syntax == "*" || syntax == ts {
			return true
		}
		if inner == "application/octet-stream" && (ts == implicitVRLittleEndian || ts == explicitVRLittleEndian) &&
			syntax == explicitVRLittleEndian {
			// Native pixel data is the same bytes in either little endian syntax.
			return true
		}
	}
	return false
}

// MountedBase builds the absolute base URL for a handler mounted at prefix on a server reached at public.
func MountedBase(public, prefix string) string {
	u, err := url.Parse(public)
	if err != nil {
		return strings.TrimRight(public, "/") + prefix
	}
	u.Path = strings.TrimRight(u.Path, "/") + prefix
	return strings.TrimRight(u.String(), "/")
}
