package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/terminology"
	"gopkg.in/yaml.v3"
)

// maxTermImportBytes bounds a code system upload. RxNorm's RXNCONSO.RRF, the largest of the files accepted, is about 1 GB.
const maxTermImportBytes = 4 << 30

// tablesNeedApproval reports whether a saved table edition waits for a second person's sign-off.
func (s *Server) tablesNeedApproval() bool {
	return s.Settings != nil && s.Settings.Bool("data.tableApproval")
}

func orDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

func clipList(list []string) string {
	if len(list) > 8 {
		return strings.Join(list[:8], ", ") + fmt.Sprintf(" and %d more", len(list)-8)
	}
	return strings.Join(list, ", ")
}

// takeOverToday dates an approved edition with no start date from today when another approved edition of the table exists,
// so a new version replaces the one in force rather than clashing with it from the start. An edition given its own date is
// left alone. An approved edition that itself only started today is replaced the same day: it is retired, since two
// editions starting on one day leave no way to tell which applies. It returns a note for each thing it did.
func takeOverToday(set *codeset.Set, t *codeset.Table) []string {
	if !t.InUse() || t.EffectiveFrom != "" {
		return nil
	}
	others := false
	for i := range set.Tables {
		o := &set.Tables[i]
		if o != t && o.Name == t.Name && o.Version != t.Version && o.InUse() {
			others = true
		}
	}
	if !others {
		return nil
	}
	today := codeset.Today()
	t.EffectiveFrom = today
	notes := []string{"Version " + orDash(t.Version) + " takes over from today."}
	for i := range set.Tables {
		o := &set.Tables[i]
		if o != t && o.Name == t.Name && o.Version != t.Version && o.InUse() && o.EffectiveFrom == today {
			o.Status = codeset.StatusRetired
			notes = append(notes, "Version "+orDash(o.Version)+" had only started today, so it is retired.")
		}
	}
	return notes
}

// restartTableUsers restarts the running channels that load a tables file after it changed, and says so.
func (s *Server) restartTableUsers(r *http.Request, sess *store.Session, file string) []string {
	rt := s.optionalRuntime(sess)
	if rt == nil {
		return nil
	}
	restarted, failed := rt.RestartUsingTables(r.Context(), file, s.log())
	var notes []string
	if len(restarted) > 0 {
		notes = append(notes, "Restarted "+strings.Join(restarted, ", ")+" to use the change.")
	}
	names := make([]string, 0, len(failed))
	for name := range failed {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		notes = append(notes, fmt.Sprintf("%s did not start again: %s", name, failed[name]))
	}
	return notes
}

// tableInUse reports whether any channel the caller can see maps through a table.
func (s *Server) tableInUse(r *http.Request, sess *store.Session, name string) bool {
	repo := s.Channels
	if s.Repos != nil {
		var err error
		if repo, err = s.Repos.Repo(sess.TenantID); err != nil {
			return false
		}
	}
	if repo == nil {
		return false
	}
	summaries, _, err := repo.List()
	if err != nil {
		return false
	}
	for _, sum := range summaries {
		cfg, err := repo.Get(sum.Name)
		if err != nil || cfg == nil {
			continue
		}
		for _, st := range cfg.Transformations {
			if st.Map != nil && strings.TrimSpace(st.Map.Use) == name {
				return true
			}
		}
	}
	return false
}

// checkTableCodes says which entries are not codes of the systems a table names, when those systems are imported, and clears
// the unmapped values an approved edition now maps.
func (s *Server) checkTableCodes(r *http.Request, sess *store.Session, t *codeset.Table) []string {
	if s.Terminology == nil {
		return nil
	}
	ctx := r.Context()
	var from, to, notes []string
	for _, e := range t.Entries {
		from, to = append(from, e.From), append(to, e.To)
	}
	if m, _ := s.Terminology.Missing(ctx, t.SourceSystem, from); len(m) > 0 {
		notes = append(notes, fmt.Sprintf("%d value%s mapped from are not codes in %s: %s.", len(m), plural(len(m), "", "s"),
			t.SourceSystem, clipList(m)))
	}
	if m, _ := s.Terminology.Missing(ctx, t.TargetSystem, to); len(m) > 0 {
		notes = append(notes, fmt.Sprintf("%d value%s mapped to are not codes in %s: %s.", len(m), plural(len(m), "", "s"),
			t.TargetSystem, clipList(m)))
	}
	if t.InUse() {
		if err := s.Terminology.Forget(ctx, string(sess.TenantID), t.Name, from, 0); err != nil && s.Log != nil {
			s.Log.Warn("could not clear the unmapped values a table now maps", "table", t.Name, "err", err)
		}
	}
	return notes
}

// tableEdition is one edition of a table, with its entries and what the terminology knows of them.
type tableEdition struct {
	codeset.Table
	InForce     bool     `json:"inForce"`
	UnknownFrom []string `json:"unknownFrom,omitempty"`
	UnknownTo   []string `json:"unknownTo,omitempty"`
}

type tableEditions struct {
	File            string         `json:"file"`
	Name            string         `json:"name"`
	InForce         string         `json:"inForce"`
	Editions        []tableEdition `json:"editions"`
	ApprovalNeeded  bool           `json:"approvalNeeded"`
	TerminologyUsed bool           `json:"terminology"`
}

// handleTableEditions returns every edition of one table, for editing, approving and comparing.
func (s *Server) handleTableEditions(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	repo, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}
	file, name := r.URL.Query().Get("file"), r.URL.Query().Get("name")
	if file == "" {
		file = defaultTableFile
	}
	set, err := repo.ReadTables(file)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	set.Compile()
	out := tableEditions{File: file, Name: name, Editions: []tableEdition{}, ApprovalNeeded: s.tablesNeedApproval(),
		TerminologyUsed: s.Terminology != nil}
	current, _ := set.Table(name)
	if current != nil {
		out.InForce = current.Version
	}
	for _, t := range set.Editions(name) {
		e := tableEdition{Table: *t, InForce: t == current}
		if s.Terminology != nil {
			var from, to []string
			for _, en := range t.Entries {
				from, to = append(from, en.From), append(to, en.To)
			}
			e.UnknownFrom, _ = s.Terminology.Missing(r.Context(), t.SourceSystem, from)
			e.UnknownTo, _ = s.Terminology.Missing(r.Context(), t.TargetSystem, to)
		}
		out.Editions = append(out.Editions, e)
	}
	if len(out.Editions) == 0 {
		s.fail(w, r, http.StatusNotFound, fmt.Sprintf("there is no table %q in %s", name, file))
		return
	}
	s.ok(w, out)
}

type editionAction struct {
	File          string `json:"file"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	EffectiveFrom string `json:"effectiveFrom,omitempty"`
}

// changeEdition reads a table file, lets change edit one edition, checks the whole file and writes it back.
func (s *Server) changeEdition(w http.ResponseWriter, r *http.Request, sess *store.Session, action string,
	change func(set *codeset.Set, i int, req editionAction) (string, error)) {
	var req editionAction
	if !s.decode(w, r, &req) {
		return
	}
	repo, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}
	if req.File == "" {
		req.File = defaultTableFile
	}
	set, err := repo.ReadTables(req.File)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	at := -1
	for i := range set.Tables {
		if set.Tables[i].Name == req.Name && set.Tables[i].Version == req.Version {
			at = i
		}
	}
	if at < 0 {
		s.fail(w, r, http.StatusNotFound, fmt.Sprintf("there is no edition %q of table %q in %s", orDash(req.Version), req.Name, req.File))
		return
	}
	note, err := change(set, at, req)
	if err != nil {
		s.fail(w, r, http.StatusConflict, err.Error())
		return
	}
	if errs := set.Validate(); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		s.fail(w, r, http.StatusBadRequest, strings.Join(msgs, "; "))
		return
	}
	raw, err := yaml.Marshal(set)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if err := repo.WriteTables(req.File, raw); err != nil {
		s.failErr(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: action, Target: req.Name,
		Detail: fmt.Sprintf("version %s in %s", orDash(req.Version), req.File), IP: clientIP(r)})
	notes := append([]string{note}, s.restartTableUsers(r, sess, req.File)...)
	s.ok(w, map[string]any{"name": req.Name, "version": req.Version, "notes": notes})
}

// handleTableApprove signs off a draft edition: it is used from its effective date.
func (s *Server) handleTableApprove(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var approved *codeset.Table
	s.changeEdition(w, r, sess, "table.approve", func(set *codeset.Set, i int, req editionAction) (string, error) {
		t := &set.Tables[i]
		if t.Status != codeset.StatusDraft {
			return "", fmt.Errorf("edition %q of %s is not a draft", orDash(t.Version), t.Name)
		}
		// Sign-off means a second person, when the server asks for it: the account that saved it cannot approve it.
		if s.tablesNeedApproval() && t.Author != "" && t.Author == sess.Username {
			return "", fmt.Errorf("you saved this edition, so somebody else has to approve it")
		}
		t.Status = codeset.StatusApproved
		t.ApprovedBy, t.ApprovedOn = sess.Username, codeset.Today()
		if from := strings.TrimSpace(req.EffectiveFrom); from != "" {
			t.EffectiveFrom = from
		}
		extra := takeOverToday(set, t)
		approved = t
		when := "today"
		if t.EffectiveFrom > codeset.Today() {
			when = "from " + t.EffectiveFrom
		}
		msg := fmt.Sprintf("Approved: channels use version %s of %s %s.", orDash(t.Version), t.Name, when)
		if len(extra) > 1 {
			msg += " " + strings.Join(extra[1:], " ")
		}
		return msg, nil
	})
	if approved != nil {
		_ = s.checkTableCodes(r, sess, approved)
	}
}

// handleTableRetire takes an edition out of use, keeping it for the record.
func (s *Server) handleTableRetire(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	s.changeEdition(w, r, sess, "table.retire", func(set *codeset.Set, i int, req editionAction) (string, error) {
		t := &set.Tables[i]
		if t.Status == codeset.StatusRetired {
			return "", fmt.Errorf("edition %q of %s is already retired", orDash(t.Version), t.Name)
		}
		t.Status = codeset.StatusRetired
		return fmt.Sprintf("Retired version %s of %s. A channel that maps through it fails to load unless another edition "+
			"is in force.", orDash(t.Version), t.Name), nil
	})
}

// handleTableDeleteDraft removes a draft edition, which was never used.
func (s *Server) handleTableDeleteDraft(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	s.changeEdition(w, r, sess, "table.delete-draft", func(set *codeset.Set, i int, req editionAction) (string, error) {
		t := set.Tables[i]
		if t.Status != codeset.StatusDraft {
			return "", fmt.Errorf("only a draft can be deleted; retire an edition that has been in use, so the record stays")
		}
		set.Tables = append(set.Tables[:i], set.Tables[i+1:]...)
		return fmt.Sprintf("Deleted the draft version %s of %s.", orDash(t.Version), t.Name), nil
	})
}

// handleTableDiff compares two editions of a table.
func (s *Server) handleTableDiff(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	repo, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}
	q := r.URL.Query()
	file := q.Get("file")
	if file == "" {
		file = defaultTableFile
	}
	set, err := repo.ReadTables(file)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	find := func(version string) *codeset.Table {
		for i := range set.Tables {
			if set.Tables[i].Name == q.Get("name") && set.Tables[i].Version == version {
				return &set.Tables[i]
			}
		}
		return nil
	}
	a, b := find(q.Get("from")), find(q.Get("to"))
	if a == nil || b == nil {
		s.fail(w, r, http.StatusNotFound, "one of the two editions is not in the file")
		return
	}
	s.ok(w, codeset.Compare(a, b))
}

// handleUnmapped lists values tables did not have, for the channels the caller can see.
func (s *Server) handleUnmapped(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.Terminology == nil {
		s.ok(w, map[string]any{"values": []terminology.Unmapped{}, "recording": false})
		return
	}
	list, err := s.Terminology.ListUnmapped(r.Context(), string(sess.TenantID), func(ch string) bool {
		return visibleChannel(r, ch)
	})
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	s.ok(w, map[string]any{"values": list, "recording": true})
}

// handleUnmappedDismiss hides a value until it is seen again.
func (s *Server) handleUnmappedDismiss(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req struct {
		Table string `json:"table"`
		Value string `json:"value"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if s.Terminology == nil {
		s.fail(w, r, http.StatusNotFound, "unmapped values are not recorded on this server")
		return
	}
	if err := s.Terminology.Dismiss(r.Context(), string(sess.TenantID), req.Table, req.Value); err != nil {
		s.failErr(w, r, err)
		return
	}
	s.ok(w, map[string]string{"dismissed": req.Value})
}

func (s *Server) needTerminology(w http.ResponseWriter, r *http.Request) bool {
	if s.Terminology == nil {
		s.fail(w, r, http.StatusNotFound, "code systems are not available on this server (it has no database)")
		return false
	}
	return true
}

// handleTermSystems lists the imported code systems.
func (s *Server) handleTermSystems(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.Terminology == nil {
		s.ok(w, map[string]any{"systems": []terminology.System{}, "available": false})
		return
	}
	list, err := s.Terminology.Systems(r.Context())
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	s.ok(w, map[string]any{"systems": list, "available": true})
}

// handleTermImport reads a publisher's file, sent as the request body.
func (s *Server) handleTermImport(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.needTerminology(w, r) {
		return
	}
	q := r.URL.Query()
	req := terminology.ImportRequest{Format: q.Get("format"), System: q.Get("system"), Version: q.Get("version"),
		Name: q.Get("name"), CodeColumn: q.Get("codeColumn"), DisplayColumn: q.Get("displayColumn"), By: sess.Username,
		Licence: q.Get("licence")}
	if _, _, known := terminology.Defaults(req.Format); !known {
		s.fail(w, r, http.StatusBadRequest, "format must be loinc, icd10cm, rxnorm, snomed or csv")
		return
	}
	// The largest of these files takes longer to send than the server's usual read deadline allows.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Minute))
	res, err := s.Terminology.Import(r.Context(), req, http.MaxBytesReader(w, r.Body, maxTermImportBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.fail(w, r, http.StatusRequestEntityTooLarge, "the file is larger than 4 GB")
			return
		}
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "terminology.import", Target: res.System,
		Detail: fmt.Sprintf("version %s, %d codes (%s)", res.Version, res.Concepts, req.Format), IP: clientIP(r)})
	s.ok(w, res)
}

// handleTermDelete removes one imported version.
func (s *Server) handleTermDelete(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.needTerminology(w, r) {
		return
	}
	system, version := r.URL.Query().Get("system"), r.URL.Query().Get("version")
	if err := s.Terminology.Delete(r.Context(), system, version); errors.Is(err, terminology.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "that code system version is not imported")
		return
	} else if err != nil {
		s.failErr(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "terminology.delete", Target: system,
		Detail: "version " + version, IP: clientIP(r)})
	s.ok(w, map[string]string{"deleted": system, "version": version})
}

// handleTermSearch finds codes by code or words.
func (s *Server) handleTermSearch(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.needTerminology(w, r) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.Terminology.Search(r.Context(), r.URL.Query().Get("system"), r.URL.Query().Get("q"), limit)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	s.ok(w, map[string]any{"concepts": list})
}

// handleTermLookup reads one code.
func (s *Server) handleTermLookup(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.needTerminology(w, r) {
		return
	}
	q := r.URL.Query()
	c, err := s.Terminology.Lookup(r.Context(), q.Get("system"), q.Get("version"), q.Get("code"))
	if errors.Is(err, terminology.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, fmt.Sprintf("%s is not a code in %s", q.Get("code"), q.Get("system")))
		return
	} else if err != nil {
		s.failErr(w, r, err)
		return
	}
	s.ok(w, c)
}
