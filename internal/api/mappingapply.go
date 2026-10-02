package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/profile"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// Turning approved mapping suggestions into steps on a channel.
//
// # Why this exists
//
// The mapper suggested and nothing could be done with a suggestion. Somebody was shown "MSH-4 to Organization.name, 87%" and had to
// go and type it into the builder themselves, which is where a transcription error enters a mapping that was suggested correctly. The
// machinery for the next step already existed with no caller: profile.MappingRecipe has AddMapping, ApprovedMappings and
// PendingMappings, and the recipe export and import endpoints were wired.
//
// # Why approval is per mapping and cannot be bulk
//
// The abstention design is the whole point of this mapper: it declines rather than guessing, because a confident wrong mapping in a
// clinical system is worse than no mapping. A control that accepted everything above a threshold would undo that, because the number
// is not the decision - the engine has already used the number to decide whether to offer the mapping at all, and what remains is a
// judgement about this field in this feed.
//
// So this endpoint takes an explicit list. There is no "approve all", no threshold parameter, and an abstention is refused outright
// rather than silently dropped: somebody who sends one is working from a screen that offered it, which is a defect worth surfacing
// rather than absorbing.

// approveMappingsRequest is a set of mappings somebody has read and accepted.
type approveMappingsRequest struct {
	// Channel is the channel to add steps to.
	Channel string `json:"channel"`

	// Mappings are the approved pairs, each named individually.
	Mappings []approvedMapping `json:"mappings"`
}

// approvedMapping is one source field and the target it was approved for.
type approvedMapping struct {
	Source string `json:"source"`
	Target string `json:"target"`

	// Confidence and Reasoning are carried through from the suggestion so the step's description can say where it came from. A step
	// nobody can account for six months later is the reason the description field exists.
	Confidence int    `json:"confidence"`
	Reasoning  string `json:"reasoning"`

	// Abstained is sent back as the engine reported it, and is refused. It is here so that a request carrying one is a clear
	// failure rather than a mapping that quietly appears.
	Abstained bool `json:"abstained"`
}

// handleApproveMappings adds a step per approved mapping to a channel.
func (s *Server) handleApproveMappings(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req approveMappingsRequest
	if !s.decode(w, r, &req) {
		return
	}

	name := strings.TrimSpace(req.Channel)
	if name == "" {
		s.fail(w, r, http.StatusBadRequest, "which channel should these mappings be added to?")

		return
	}

	if len(req.Mappings) == 0 {
		s.fail(w, r, http.StatusBadRequest,
			"no mappings were approved, so there is nothing to add")

		return
	}

	// Refused rather than skipped. An abstention is the engine saying it does not know, and the interface does not offer one for
	// approval - so a request containing one means the two disagree, which is worth failing loudly over.
	var abstained []string

	for _, m := range req.Mappings {
		if m.Abstained {
			abstained = append(abstained, m.Source+" to "+m.Target)
		}
	}

	if len(abstained) > 0 {
		sort.Strings(abstained)
		s.fail(w, r, http.StatusBadRequest,
			"the engine abstained on these, which means it does not know, so they cannot be approved", abstained...)

		return
	}

	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	cfg, err := channels.Get(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	// Both sides have to be paths in the message before anything is written.
	//
	// This is the design correction the loader caught for me. A copy step copies between two paths inside one message, so it can
	// only express a mapping whose source is itself a path - HL7 to HL7. The mapper's source fields are usually not: they are
	// column names, JSON keys, or a vendor's own field names, and "PatientMRN" is not somewhere a message keeps anything.
	//
	// Refused here with that explanation rather than by the loader with a complaint about segment names. The loader's message is
	// correct and unhelpful: it says PATIENTMRN is not a three-character segment name, which is true and tells somebody nothing
	// about what they should do instead.
	var notPaths []string

	for _, m := range req.Mappings {
		if !looksLikePath(m.Source) {
			notPaths = append(notPaths, m.Source)
		}
	}

	if len(notPaths) > 0 {
		sort.Strings(notPaths)
		s.fail(w, r, http.StatusBadRequest,
			"a step copies from one place in the message to another, so the source has to be a path in it. These are field names "+
				"from another system, which a channel cannot read directly - export a recipe instead, or map them in a script.",
			notPaths...)

		return
	}

	// Existing steps are kept and the new ones appended.
	//
	// Appended rather than merged, because a mapping that writes a field an earlier step already wrote is a real conflict and the
	// order decides the outcome. Silently reordering or replacing would make the result depend on something nobody chose.
	var added []string

	for _, m := range req.Mappings {
		source := strings.TrimSpace(m.Source)
		target := strings.TrimSpace(m.Target)

		if source == "" || target == "" {
			s.fail(w, r, http.StatusBadRequest,
				"a mapping needs both a source field and a target field")

			return
		}

		cfg.Transformations = append(cfg.Transformations, transform.Step{
			Description: describeMapping(m),
			Copy: &transform.CopyStep{
				From: source,
				To:   target,
			},
		})

		added = append(added, source+" to "+target)
	}

	// Validated before writing, because a step the loader refuses makes the whole channel invalid - and Perfuse refuses a channel
	// wholesale when anything in it is broken, so a bad mapping does not produce a warning, it makes the channel disappear.
	if err := cfg.Validate(); err != nil {
		s.fail(w, r, http.StatusBadRequest,
			"those mappings do not make a channel this can load, so nothing was written", err.Error())

		return
	}

	if err := channels.Save(cfg); err != nil {
		s.failErr(w, r, err)
		return
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "mappings.approve",
		Target: name, Detail: strings.Join(added, "; "), IP: clientIP(r),
	})

	s.ok(w, map[string]any{
		"channel": name,
		"added":   added,

		// Said explicitly, because a mapping added to a channel file is not a mapping that is running. The channel has to be
		// reloaded, and somebody who believes otherwise will watch traffic for a change that cannot appear yet.
		"note": "These steps are in the channel file. They take effect when the channel next loads.",
	})
}

// describeMapping writes the step description.
//
// Carrying the confidence into the file is deliberate. A step somebody cannot account for six months later is the reason the
// description field exists at all, and "suggested at 87%" is the difference between a mapping that was reviewed and one that appeared.
func describeMapping(m approvedMapping) string {
	out := "mapped from " + strings.TrimSpace(m.Source)

	if m.Confidence > 0 {
		out += " (suggested at " + strconv.Itoa(m.Confidence) + "% confidence, approved by hand)"
	}

	if reason := strings.TrimSpace(m.Reasoning); reason != "" {
		out += ": " + reason
	}

	return out
}

// handleRecipeFromMappings turns approved mappings into a shareable recipe.
//
// Separate from applying them, because the two are different decisions: applying changes this server, and a recipe is for somebody
// else's. The recipe format already existed and had no producer.
func (s *Server) handleRecipeFromMappings(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var req struct {
		Name         string            `json:"name"`
		SourceSystem string            `json:"sourceSystem"`
		TargetSystem string            `json:"targetSystem"`
		Mappings     []approvedMapping `json:"mappings"`
	}

	if !s.decode(w, r, &req) {
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		s.fail(w, r, http.StatusBadRequest, "a recipe needs a name, because the point of one is that somebody else can find it")

		return
	}

	if len(req.Mappings) == 0 {
		s.fail(w, r, http.StatusBadRequest, "no mappings were approved, so the recipe would be empty")

		return
	}

	recipe := profile.NewRecipe(req.Name, "", strings.TrimSpace(req.SourceSystem), strings.TrimSpace(req.TargetSystem), "")

	for _, m := range req.Mappings {
		if m.Abstained {
			s.fail(w, r, http.StatusBadRequest,
				"the engine abstained on "+m.Source+", so it cannot go into a recipe as an approved mapping")

			return
		}

		recipe.AddMapping(profile.FieldMapping{
			SourcePath: strings.TrimSpace(m.Source),
			TargetPath: strings.TrimSpace(m.Target),
			Confidence: m.Confidence,
			Reasoning:  m.Reasoning,

			// Approved, because that is what this endpoint is for. The recipe format distinguishes approved from pending precisely
			// so that a shared recipe says which mappings a human agreed to.
			Approved: true,
		})
	}

	data, err := profile.MarshalRecipe(recipe)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(req.Name)+".recipe.json\"")
	_, _ = w.Write(data)
}

// looksLikePath reports whether a name could address something in a message.
//
// Deliberately loose, and only used to give a better refusal than the loader's. The loader decides what is actually valid; this
// decides whether to explain that a field name is not a path at all, which is a different and more useful thing to say.
func looksLikePath(name string) bool {
	name = strings.TrimSpace(name)
	if len(name) < 3 {
		return false
	}

	// An HL7 v2 path starts with a three-character segment name followed by a dash and a number: PID-3, MSH-9.2. Anything else -
	// an XML path, a column name, a JSON key - is not something a copy step can read.
	if len(name) < 5 || name[3] != '-' {
		return false
	}

	for i := 0; i < 3; i++ {
		if name[i] < 'A' || name[i] > 'Z' {
			return false
		}
	}

	return name[4] >= '0' && name[4] <= '9'
}
