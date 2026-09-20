package ldap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Directory authenticates people against an LDAP directory and reads their groups.
type Directory struct {
	cfg *Config
	log *slog.Logger
}

// NewDirectory builds a directory client from configuration.
func NewDirectory(cfg *Config, log *slog.Logger) (*Directory, error) {
	if cfg == nil {
		return nil, errors.New("an LDAP directory needs configuration")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Directory{cfg: cfg, log: log}, nil
}

// Identity is a person the directory recognised.
type Identity struct {
	// DN is the person's distinguished name, which is what the directory considers their identity.
	DN string

	// Username is the value they typed.
	Username string

	// DisplayName is their human-readable name, if the directory has one.
	DisplayName string

	// Email is their address, if the directory has one.
	Email string

	// Groups are the group names they belong to.
	Groups []string

	// UniqueID is a directory identifier that survives the person being renamed or moved, when one is configured.
	//
	// Empty when no unique_id_attribute is set, in which case a caller has to fall back to the DN and accept that
	// moving somebody in the directory looks like a new person.
	UniqueID string
}

// StableID is the identifier to record a person against.
//
// The unique identifier when the directory has one, otherwise the DN. Callers use this rather than choosing for
// themselves, so that the fallback and its consequence live in one place.
func (i *Identity) StableID() string {
	if i.UniqueID != "" {
		return i.UniqueID
	}
	return i.DN
}

// Authenticate verifies a password and returns who the person is.
//
// The sequence is a search followed by a second bind, and the order is not arbitrary:
//
//  1. Bind as the service account, or anonymously, so the directory will answer questions.
//  2. Search for the person by the attribute they type - sAMAccountName on AD, uid elsewhere - to find their DN.
//  3. Bind as that DN with the password they typed. This is the only step that proves the password.
//  4. Rebind as the service account and read their groups.
//
// Step 2 exists because nobody knows their own DN. A person types "rturner" and their identity in the directory is
// uid=rturner,ou=people,dc=... - and on AD it is very often CN=Rosalind Turner,OU=Nursing,... with no relationship to
// what they type at all. Building a DN from a template works until the first person whose account was created by a
// different administrator in a different container.
func (d *Directory) Authenticate(ctx context.Context, username, password string) (*Identity, error) {
	// Refused first, before the directory is contacted at all. An empty password reaching the bind would be an
	// anonymous bind that a permissive directory answers with success - see Bind, where it is refused again. Twice,
	// because this is the path a login form reaches and the consequence is somebody getting in without a password.
	if strings.TrimSpace(username) == "" {
		return nil, errors.New("a username is required")
	}
	if password == "" {
		return nil, errors.New("a password is required; an empty one would be an anonymous bind rather than a sign-in")
	}

	conn, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	if err := d.bindService(conn); err != nil {
		return nil, err
	}

	entry, err := d.findPerson(conn, username)
	if err != nil {
		return nil, err
	}

	// The password check. A failure here is the person's, not the system's, and is reported as such by the caller.
	if err := conn.Bind(entry.DN, password); err != nil {
		return nil, err
	}

	// Rebound as the service account before reading groups. After the password bind this connection is
	// authenticated as the person, who very often cannot read group objects at all - and a group search that
	// returns nothing because of access rights is indistinguishable from a person belonging to no groups, which
	// maps to no roles and refuses them with a message about permissions they do not have.
	if err := d.bindService(conn); err != nil {
		return nil, fmt.Errorf("the password was correct but the service account could not be rebound to read "+
			"groups: %w", err)
	}

	groups, err := d.readGroups(conn, entry)
	if err != nil {
		return nil, err
	}

	id := &Identity{
		DN:          entry.DN,
		Username:    username,
		DisplayName: entry.Get(d.cfg.NameAttribute),
		Email:       entry.Get(d.cfg.EmailAttribute),
		Groups:      groups,
		UniqueID:    entry.Get(d.cfg.UniqueIDAttribute),
	}

	if d.cfg.UniqueIDAttribute != "" && id.UniqueID == "" {
		// Refused rather than silently falling back to the DN. An administrator who configured this attribute
		// asked for stable identities, and quietly not having them is how a directory reorganisation turns into
		// duplicate accounts months later with nothing connecting the two events.
		return nil, fmt.Errorf("the directory returned no %s for %s, so this person has no stable identifier; "+
			"either the attribute name is wrong or the service account cannot read it",
			d.cfg.UniqueIDAttribute, entry.DN)
	}

	d.log.Info("the directory recognised a sign-in",
		"username", username,
		"dn", entry.DN,
		"groups", len(groups),
		// Recorded because a cleartext bind is worth knowing about after the fact, and because a configuration
		// that was meant to use TLS and silently is not should be visible in a log somebody reads.
		"encrypted", conn.Encrypted(),
	)

	return id, nil
}

// connect opens a connection using the configured transport.
func (d *Directory) connect(ctx context.Context) (*Conn, error) {
	opts := Options{
		Addr:     d.cfg.Addr,
		StartTLS: d.cfg.StartTLS,
		Insecure: d.cfg.Insecure,
		Timeout:  d.cfg.ResolvedTimeout(),
	}

	if d.cfg.TLS || d.cfg.StartTLS {
		host := d.cfg.Addr
		if i := strings.LastIndex(host, ":"); i > 0 {
			host = host[:i]
		}
		opts.TLS = &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
			// Verification is never switched off here. An unverified TLS connection to a directory is worth
			// less than an honest cleartext one, because it looks encrypted in every log and configuration
			// review while accepting any server that answers on the address.
			InsecureSkipVerify: false,
		}
	}

	ctx, cancel := context.WithTimeout(ctx, d.cfg.ResolvedTimeout())
	defer cancel()

	return Dial(ctx, opts)
}

// bindService binds as the service account, or anonymously if none is configured.
func (d *Directory) bindService(conn *Conn) error {
	if d.cfg.BindDN == "" {
		if err := conn.BindAnonymous(); err != nil {
			return fmt.Errorf("this directory does not allow anonymous search, so bind_dn and bind_password "+
				"are needed: %w", err)
		}
		return nil
	}

	if err := conn.Bind(d.cfg.BindDN, d.cfg.BindPassword); err != nil {
		// Named as the service account's failure rather than the person's. Without this distinction a wrong
		// service password looks like every user suddenly having a wrong password, which sends somebody looking
		// in entirely the wrong place.
		return fmt.Errorf("the service account %s could not sign in to the directory: %w", d.cfg.BindDN, err)
	}
	return nil
}

// findPerson searches for one person and refuses an ambiguous answer.
func (d *Directory) findPerson(conn *Conn, username string) (*Entry, error) {
	filter := d.cfg.filterFor(username)

	attrs := []string{d.cfg.UsernameAttribute}
	if d.cfg.NameAttribute != "" {
		attrs = append(attrs, d.cfg.NameAttribute)
	}
	if d.cfg.EmailAttribute != "" {
		attrs = append(attrs, d.cfg.EmailAttribute)
	}
	if d.cfg.MemberOfAttribute != "" {
		attrs = append(attrs, d.cfg.MemberOfAttribute)
	}
	if d.cfg.UniqueIDAttribute != "" {
		attrs = append(attrs, d.cfg.UniqueIDAttribute)
	}

	entries, err := conn.Search(SearchRequest{
		BaseDN: d.cfg.UserBaseDN,
		Scope:  ScopeSubtree,
		Filter: filter,
		// Two, not one. Asking for one and taking it would hide an ambiguous directory; asking for two makes
		// ambiguity visible so it can be refused.
		SizeLimit:  2,
		TimeLimit:  int64(d.cfg.ResolvedTimeout() / time.Second),
		Attributes: attrs,
	})
	if err != nil {
		return nil, fmt.Errorf("the directory could not be searched for %q: %w", username, err)
	}

	switch len(entries) {
	case 0:
		// Deliberately the same shape of message the caller turns into "wrong username or password". Telling an
		// unauthenticated caller that an account does not exist is how somebody enumerates who works here.
		return nil, &Error{Code: resultInvalidCredentials, Message: "no such account"}

	case 1:
		return entries[0], nil

	default:
		// Refused rather than resolved. Two accounts matching one typed name means the search base or the
		// username attribute is wrong - a filter on cn rather than uid will match several real people - and
		// picking one would authenticate against whichever the directory happened to return first.
		return nil, fmt.Errorf("%d accounts in the directory match the username %q, so it cannot identify one "+
			"person; the user filter or the search base needs narrowing", len(entries), username)
	}
}

// readGroups collects the group names a person belongs to.
//
// Two strategies, because directories are built both ways round. AD keeps memberOf on the person; OpenLDAP with
// groupOfNames keeps member on the group. Reading only one finds nothing on the other kind of directory - and nothing
// maps to no roles, which refuses the person with a message about their permissions rather than about the configuration.
func (d *Directory) readGroups(conn *Conn, entry *Entry) ([]string, error) {
	var names []string

	if d.cfg.MemberOfAttribute != "" {
		for _, dn := range entry.GetAll(d.cfg.MemberOfAttribute) {
			names = append(names, groupNameFromDN(dn))
		}
	}

	if d.cfg.GroupBaseDN != "" {
		filter := d.cfg.groupFilterFor(entry.DN, entry.Get(d.cfg.UsernameAttribute))

		groups, err := conn.Search(SearchRequest{
			BaseDN:     d.cfg.GroupBaseDN,
			Scope:      ScopeSubtree,
			Filter:     filter,
			TimeLimit:  int64(d.cfg.ResolvedTimeout() / time.Second),
			Attributes: []string{d.cfg.GroupNameAttribute},
		})
		if err != nil {
			return nil, fmt.Errorf("the directory could not be searched for group membership: %w", err)
		}

		for _, g := range groups {
			if name := g.Get(d.cfg.GroupNameAttribute); name != "" {
				names = append(names, name)
			} else {
				names = append(names, groupNameFromDN(g.DN))
			}
		}
	}

	return dedupe(names), nil
}

// groupNameFromDN takes the first component's value out of a DN.
//
// A memberOf value is a full DN such as CN=Perfuse Admins,OU=Groups,DC=hospital,DC=local, and what a role mapping is
// written against is the name. Taking the whole DN would mean every mapping had to spell out a container path that
// changes when somebody reorganises the directory.
func groupNameFromDN(dn string) string {
	if dn == "" {
		return ""
	}

	// Split on the first unescaped comma. A group name may legitimately contain an escaped one.
	end := len(dn)
	for i := 0; i < len(dn); i++ {
		if dn[i] == '\\' {
			i++
			continue
		}
		if dn[i] == ',' {
			end = i
			break
		}
	}

	first := dn[:end]
	if i := strings.Index(first, "="); i >= 0 {
		first = first[i+1:]
	}

	return strings.ReplaceAll(first, "\\", "")
}

// dedupe removes repeats while keeping order.
//
// Order is kept rather than sorted, because a directory returns groups in its own order and a log line listing them is
// easier to compare with the directory's own view if it matches. Repeats happen when both strategies find the same group.
func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
