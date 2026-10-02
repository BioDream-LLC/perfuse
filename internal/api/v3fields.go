package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/hl7v3"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// maxSampleMessage bounds what may be pasted in for inspection.
//
// Generous, because a real PDQ response carrying twenty patient matches is genuinely large and somebody debugging one should be
// able to paste the whole thing rather than trimming it and wondering whether the trim caused the problem.
//
// Bounded all the same: this parses untrusted XML into a tree held in memory, and an unbounded paste is a way to make a server
// allocate as much as the client feels like sending.
const maxSampleMessage = 4 << 20 // 4 MiB

// fieldTreeRequest is a message to look at.
type fieldTreeRequest struct {
	// Message is the raw XML.
	Message string `json:"message"`
}

// fieldTreeResponse is the message described as clickable fields.
type fieldTreeResponse struct {
	// Root is the message as a tree.
	Root hl7v3.FieldNode `json:"root"`

	// Fields is every place with something to read, flattened for searching.
	Fields []hl7v3.FieldNode `json:"fields"`

	// Interaction is the message type, when the document says.
	//
	// Shown because it is the first thing worth confirming: somebody who pasted a record-added message while trying to
	// configure a query response should find that out here rather than from a channel that never matches.
	Interaction string `json:"interaction,omitempty"`

	// Truncated says the tree was cut short by the size limits.
	//
	// Reported rather than hidden. A picker silently showing part of a message is one that convinces somebody a field is
	// absent from their feed when it is merely absent from the tree.
	Truncated bool `json:"truncated,omitempty"`
}

// handleV3FieldTree turns a pasted v3 message into a tree of clickable fields.
//
// Viewer, because this discloses nothing: it is a pure function of a message the caller pasted, so everything in the answer is
// something they already had. It also lives on a page viewers can reach, and a tool that answers 403 when used is worse than no
// tool.
//
// The size limit is what stands in for a role here. The cost of this endpoint is parsing, not access.
func (s *Server) handleV3FieldTree(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req fieldTreeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSampleMessage)).Decode(&req); err != nil {
		// The size limit surfaces here as a decode failure, so the message says so rather than reporting invalid JSON
		// for a document that was perfectly well formed and merely enormous.
		s.fail(w, r, http.StatusBadRequest,
			"that message could not be read - check it is valid XML and under 4 MB")

		return
	}

	message := strings.TrimSpace(req.Message)
	if message == "" {
		s.fail(w, r, http.StatusBadRequest, "paste a message to look at")

		return
	}

	// xtree.Parse is what the rest of the v3 code uses, so what the picker shows is what a channel would actually see -
	// including its refusal of external entities and of documents nested past its depth limit. A second parser here
	// would mean the picker could show a field a channel could not read.
	root, err := xtree.Parse([]byte(message))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("that is not XML this server can read: %v", err))

		return
	}

	const maxNodes = 4000

	tree, err := hl7v3.BuildFieldTree(root, hl7v3.FieldTreeOptions{MaxNodes: maxNodes})
	if err != nil {
		s.failErr(w, r, err)

		return
	}

	fields := hl7v3.FlattenFieldTree(tree)

	s.ok(w, fieldTreeResponse{
		Root:        tree,
		Fields:      fields,
		Interaction: interactionOf(root),
		// Compared against the budget rather than tracked through the walk, because the walk stopping exactly at the
		// limit is the case worth reporting and an off-by-one here only ever warns unnecessarily.
		Truncated: countTreeNodes(tree) >= maxNodes,
	})
}

// interactionOf reports the message type.
//
// Read from interactionId when the sender gave one and from the root element name otherwise. Both are used in the wild: the root
// element is named for the interaction by convention, and interactionId states it explicitly - and a sender that disagrees with
// itself is worth seeing, which is why the explicit one wins and is not merged with the other.
func interactionOf(root *xtree.Node) string {
	for _, child := range root.Children {
		if strings.EqualFold(localElementName(child.Name), "interactionId") {
			if v, ok := child.Attr("extension"); ok && v != "" {
				return v
			}
		}
	}

	return localElementName(root.Name)
}

// localElementName strips a namespace prefix.
//
// A copy of what the hl7v3 package does internally, because exporting that would put a general XML helper in a package about one
// standard - and this file needs it for exactly two lines.
func localElementName(name string) string {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		return name[i+1:]
	}

	return name
}

// countTreeNodes counts every node in a built tree.
func countTreeNodes(n hl7v3.FieldNode) int {
	total := 1
	for _, child := range n.Children {
		total += countTreeNodes(child)
	}

	return total
}

// v3PathCheckRequest is a path to test against a message.
type v3PathCheckRequest struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// v3PathCheckResponse is what a path found.
type v3PathCheckResponse struct {
	// Valid says the path parsed.
	Valid bool `json:"valid"`

	// Error explains why it did not.
	Error string `json:"error,omitempty"`

	// Values are what it resolved to.
	Values []string `json:"values"`

	// Exists says the path addressed an element, whether or not it had a value.
	//
	// Separate from having values, because an element present with nullFlavor="ASKU" exists and has none - and somebody
	// writing a filter needs to see that difference while they are writing it rather than discover it in production.
	Exists bool `json:"exists"`

	// NullFlavor is the reason a value is absent, when the sender gave one.
	NullFlavor string `json:"nullFlavor,omitempty"`
}

// handleV3PathCheck tests a path against a message.
//
// The half of a picker that matters after the clicking: somebody who edits a generated path, or writes one by hand, should be able
// to see what it selects before a channel runs on it. Without this the feedback loop is "save the channel, send a message, read
// the log", which is slow enough that people stop checking.
func (s *Server) handleV3PathCheck(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req v3PathCheckRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSampleMessage)).Decode(&req); err != nil {
		s.fail(w, r, http.StatusBadRequest, "that request could not be read")

		return
	}

	// A bad path is a 200 with valid:false, not a 400. This is a live feedback endpoint called while somebody types, and
	// half-finished input is the normal case rather than an error - "//patient/id(" is what a path looks like a keystroke
	// before it is correct.
	p, err := hl7v3.ParsePath(req.Path)
	if err != nil {
		s.ok(w, v3PathCheckResponse{Error: err.Error(), Values: []string{}})

		return
	}

	if strings.TrimSpace(req.Message) == "" {
		// The path is valid and there is nothing to try it against. Reported as valid with no values rather than as an
		// error, so the interface can confirm the syntax before a sample has been pasted.
		s.ok(w, v3PathCheckResponse{Valid: true, Values: []string{}})

		return
	}

	root, err := xtree.Parse([]byte(req.Message))
	if err != nil {
		s.ok(w, v3PathCheckResponse{
			Valid:  true,
			Error:  fmt.Sprintf("the path is fine, but the sample message is not readable: %v", err),
			Values: []string{},
		})

		return
	}

	values := p.Values(root)
	if values == nil {
		// An empty slice rather than null, because a JSON null here becomes an interface that has to check before it
		// can count, and forgetting that check is how a picker throws while somebody is typing.
		values = []string{}
	}

	flavor, _ := p.NullFlavor(root)

	s.ok(w, v3PathCheckResponse{
		Valid:      true,
		Values:     values,
		Exists:     p.Exists(root),
		NullFlavor: flavor,
	})
}
