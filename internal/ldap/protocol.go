package ldap

import (
	"fmt"
	"strings"
)

// LDAP application tags, from RFC 4511 section 4.
const (
	appBindRequest      = 0
	appBindResponse     = 1
	appUnbindRequest    = 2
	appSearchRequest    = 3
	appSearchResEntry   = 4
	appSearchResDone    = 5
	appExtendedRequest  = 23
	appExtendedResponse = 24
)

// Result codes worth naming.
const (
	resultSuccess               = 0
	resultInvalidCredentials    = 49
	resultInsufficientAccess    = 50
	resultUnwillingToPerform    = 53
	resultReferral              = 10
	resultNoSuchObject          = 32
	resultConfidentialityNeeded = 13
	resultSizeLimitExceeded     = 4
	resultStrongerAuthRequired  = 8
)

// Search scopes.
const (
	ScopeBase     = 0
	ScopeOneLevel = 1
	ScopeSubtree  = 2
)

// startTLSOID identifies the StartTLS extended operation.
const startTLSOID = "1.3.6.1.4.1.1466.20.037"

// Error is an LDAP result the server refused.
type Error struct {
	// Code is the LDAP result code.
	Code int64

	// Message is what the server said, which is often the most useful part.
	Message string

	// DN is the matched DN the server returned, if any.
	DN string
}

func (e *Error) Error() string {
	name := resultName(e.Code)
	if e.Message != "" {
		return fmt.Sprintf("the directory refused the request: %s (%d): %s", name, e.Code, e.Message)
	}
	return fmt.Sprintf("the directory refused the request: %s (%d)", name, e.Code)
}

// IsInvalidCredentials reports whether the failure was a wrong password.
//
// Separated so a caller can tell "this person's password is wrong" from "the directory is unreachable or misconfigured".
// Treating those alike is how a directory outage becomes a flood of apparent password failures and nobody looks at the
// directory.
func (e *Error) IsInvalidCredentials() bool {
	return e.Code == resultInvalidCredentials
}

func resultName(code int64) string {
	switch code {
	case resultSuccess:
		return "success"
	case resultInvalidCredentials:
		return "invalid credentials"
	case resultInsufficientAccess:
		return "insufficient access rights"
	case resultUnwillingToPerform:
		return "unwilling to perform"
	case resultReferral:
		return "referral"
	case resultNoSuchObject:
		return "no such object"
	case resultConfidentialityNeeded:
		return "confidentiality required, so this needs TLS"
	case resultSizeLimitExceeded:
		return "size limit exceeded"
	case resultStrongerAuthRequired:
		return "stronger authentication required"
	default:
		return "result code"
	}
}

// EscapeFilter escapes a value for use inside a search filter, per RFC 4515.
//
// This is the injection defence and it is not optional. A username is put into a filter such as
// (sAMAccountName=%s), and without escaping:
//
//   - a username of * matches every account, so the filter finds the first user in the directory and the password
//     is then checked against whoever that happens to be;
//   - a username of x)(objectClass=* changes the filter's structure entirely;
//   - a NUL byte truncates the filter at some servers, discarding every condition after it.
//
// All five characters RFC 4515 names are escaped as a backslash and two hex digits. Escaping to a bare backslash would
// be wrong: the filter grammar uses hex escapes, and a literal backslash is itself one of the characters needing one.
func EscapeFilter(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\', '*', '(', ')', 0x00:
			fmt.Fprintf(&b, "\\%02x", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// EscapeDN escapes a value for use inside a distinguished name, per RFC 4514.
//
// Separate from filter escaping because the rules are different and using the wrong one is silent: a comma escaped the
// filter way inside a DN produces a DN that parses into different components than intended, which finds a different
// object rather than failing.
func EscapeDN(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' || c == ',' || c == '+' || c == '"' || c == '<' || c == '>' || c == ';' || c == '=':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == 0x00:
			b.WriteString("\\00")
		case c == ' ' && (i == 0 || i == len(s)-1):
			// Leading and trailing spaces are significant in a DN and must be escaped, which is the sort of rule
			// that is invisible until a directory has a display name with a trailing space in it.
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '#' && i == 0:
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// buildBindRequest assembles a simple bind.
func buildBindRequest(messageID int64, dn, password string) []byte {
	msg := newSequence(tagSequence | classUniversal)
	msg.addInteger(messageID)

	req := msg.add(newSequence(classApplication | appBindRequest))
	// Version 3. Version 2 is not offered: it lacks StartTLS, which is the only thing making a simple bind safe on a
	// network somebody else can see.
	req.addInteger(3)
	req.addString(dn)
	// The simple authentication choice, context tag 0, primitive.
	req.add(newPacket(classContext|0, []byte(password)))

	return msg.bytes()
}

// buildUnbindRequest assembles an unbind.
func buildUnbindRequest(messageID int64) []byte {
	msg := newSequence(tagSequence | classUniversal)
	msg.addInteger(messageID)
	// Unbind carries no contents but must still be constructed-free: it is an application-tagged null.
	msg.add(newPacket(classApplication|appUnbindRequest, nil))
	return msg.bytes()
}

// buildStartTLSRequest assembles the StartTLS extended operation.
func buildStartTLSRequest(messageID int64) []byte {
	msg := newSequence(tagSequence | classUniversal)
	msg.addInteger(messageID)

	req := msg.add(newSequence(classApplication | appExtendedRequest))
	// The request name, context tag 0.
	req.add(newPacket(classContext|0, []byte(startTLSOID)))

	return msg.bytes()
}

// SearchRequest describes a search.
type SearchRequest struct {
	// BaseDN is where to search from.
	BaseDN string

	// Scope is ScopeBase, ScopeOneLevel or ScopeSubtree.
	Scope int64

	// Filter is an LDAP filter string. Values inside it must already be escaped with EscapeFilter.
	Filter string

	// Attributes are the attributes to return. Empty means all, which is deliberately not the default anywhere in
	// this package: asking for everything pulls photographs and certificates over the wire to read a group list.
	Attributes []string

	// SizeLimit bounds how many entries the server returns. Zero means the server's own limit.
	SizeLimit int64

	// TimeLimit bounds the search in seconds.
	TimeLimit int64
}

// buildSearchRequest assembles a search.
func buildSearchRequest(messageID int64, req SearchRequest, filter *packet) []byte {
	msg := newSequence(tagSequence | classUniversal)
	msg.addInteger(messageID)

	s := msg.add(newSequence(classApplication | appSearchRequest))
	s.addString(req.BaseDN)
	s.addEnumerated(req.Scope)
	// Never dereference aliases. Following them would let a directory redirect a lookup somewhere else, and an alias
	// pointing at an attacker-controlled subtree would decide which object's password gets checked.
	s.addEnumerated(0)
	s.addInteger(req.SizeLimit)
	s.addInteger(req.TimeLimit)
	// typesOnly false: values are wanted, not just attribute names.
	s.addBoolean(false)
	s.add(filter)

	attrs := s.add(newSequence(tagSequence | classUniversal))
	for _, a := range req.Attributes {
		attrs.addString(a)
	}

	return msg.bytes()
}

// Entry is one object returned by a search.
type Entry struct {
	// DN is the object's distinguished name.
	DN string

	// Attributes maps attribute names to their values.
	Attributes map[string][]string
}

// Get returns the first value of an attribute, case-insensitively.
//
// Case-insensitive because directories disagree about capitalisation of the same attribute: AD returns sAMAccountName
// and memberOf, OpenLDAP returns uid and memberOf, and a configuration file written for one would silently read nothing
// against the other. Silently, because a missing group list maps to no roles, which is a refusal rather than an error.
func (e *Entry) Get(name string) string {
	values := e.GetAll(name)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// GetAll returns every value of an attribute, case-insensitively.
func (e *Entry) GetAll(name string) []string {
	if e == nil || e.Attributes == nil {
		return nil
	}
	if v, ok := e.Attributes[name]; ok {
		return v
	}
	lower := strings.ToLower(name)
	for k, v := range e.Attributes {
		if strings.ToLower(k) == lower {
			return v
		}
	}
	return nil
}

// parseSearchEntry decodes a SearchResultEntry.
func parseSearchEntry(e *element) (*Entry, error) {
	dnEl, err := e.child(0)
	if err != nil {
		return nil, err
	}

	out := &Entry{DN: dnEl.text(), Attributes: map[string][]string{}}

	attrsEl, err := e.child(1)
	if err != nil {
		return nil, err
	}

	for _, attr := range attrsEl.children {
		nameEl, err := attr.child(0)
		if err != nil {
			return nil, err
		}
		name := nameEl.text()

		valuesEl, err := attr.child(1)
		if err != nil {
			return nil, err
		}

		values := make([]string, 0, len(valuesEl.children))
		for _, v := range valuesEl.children {
			values = append(values, v.text())
		}
		out.Attributes[name] = values
	}

	return out, nil
}

// parseResult decodes a response carrying an LDAP result.
func parseResult(e *element) error {
	codeEl, err := e.child(0)
	if err != nil {
		return err
	}
	code, err := codeEl.integer()
	if err != nil {
		return err
	}

	dnEl, err := e.child(1)
	if err != nil {
		return err
	}

	msgEl, err := e.child(2)
	if err != nil {
		return err
	}

	if code == resultSuccess {
		return nil
	}

	return &Error{Code: code, Message: msgEl.text(), DN: dnEl.text()}
}
