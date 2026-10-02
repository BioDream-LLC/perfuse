package ldap

import (
	"context"
	"fmt"
	"strings"
)

// Probing a configuration without anybody's password.
//
// # Why this exists
//
// Every way this configuration goes wrong goes wrong silently, and all but one of them go wrong before a password is ever checked. An
// address that cannot be reached, a service account that cannot bind, a username attribute that names nothing, a group search that
// returns nothing because the service account cannot read group objects - each of those ends up in front of a person as "wrong
// password", because that is the only thing a sign-in form can say when it cannot find them.
//
// So the useful test is not "can this person sign in". It is "does this configuration find this person, and would their groups give them
// a role". That needs no password, and asking for one would be worse in every way while answering nothing extra.
//
// # Why it reports stages rather than an error
//
// Knowing which step failed is most of knowing why. "Could not sign in" sends somebody to check the password; "reached the server,
// encrypted the connection, bound as the service account, found nobody matching sAMAccountName=jsmith" sends them to the one field that
// is wrong. The stages are the same operations Authenticate performs, in the same order, through the same functions - so a stage that
// passes here is a stage that will pass there.

// ProbeStage is one step of a probe.
type ProbeStage struct {
	Name   string
	OK     bool
	Detail string
}

// ProbeReport is what a probe found.
type ProbeReport struct {
	// OK reports whether every stage attempted succeeded.
	OK bool

	Stages []ProbeStage

	// Groups is what the directory said about the person, when a username was given. Nil when none was.
	//
	// Distinguished from empty on purpose: nil means nobody was looked up, and empty means somebody was found and belongs to no
	// groups the search could see - which is a real and common misconfiguration rather than an absence of information.
	Groups []string
}

func (r *ProbeReport) add(name string, ok bool, detail string) {
	r.Stages = append(r.Stages, ProbeStage{Name: name, OK: ok, Detail: detail})
}

// Probe checks as much of the configuration as can be checked without a password.
//
// A username is optional. Without one it stops after binding the service account, which is still the majority of what goes wrong; with
// one it goes on to find that person and read their groups.
func (d *Directory) Probe(ctx context.Context, username string) ProbeReport {
	var report ProbeReport

	conn, err := d.connect(ctx)
	if err != nil {
		report.add("Reach the directory", false, err.Error())

		return report
	}
	defer func() { _ = conn.Close() }()

	report.add("Reach the directory", true, fmt.Sprintf("Connected to %s.", d.cfg.Addr))

	// Reported as its own stage rather than folded into the one above, because a connection that succeeded unencrypted is the failure
	// most likely to go unnoticed: everything works, and every password crosses the network in the clear.
	if conn.Encrypted() {
		report.add("Encrypt the connection", true, "The connection is encrypted.")
	} else {
		report.add("Encrypt the connection", false,
			"This connection is not encrypted, so passwords cross the network in the clear. It was allowed because "+
				"insecure is set.")
	}

	if err := d.bindService(conn); err != nil {
		report.add("Sign in as the service account", false, err.Error())

		return report
	}

	if d.cfg.BindDN == "" {
		report.add("Sign in as the service account", true,
			"Bound anonymously, because no service account is set. Many directories allow this and then return nothing "+
				"from a search, so a successful bind here is not yet proof that people can be found.")
	} else {
		report.add("Sign in as the service account", true, fmt.Sprintf("Bound as %s.", d.cfg.BindDN))
	}

	if strings.TrimSpace(username) == "" {
		report.OK = true
		report.add("Find a person", false,
			"Not attempted. Give a username above to check that the username attribute and the group mapping actually "+
				"work, which is where most of the remaining trouble is.")

		return report
	}

	entry, err := d.findPerson(conn, username)
	if err != nil {
		report.add("Find a person", false, err.Error())

		return report
	}

	report.add("Find a person", true, fmt.Sprintf("%s=%s is %s.", d.cfg.UsernameAttribute, username, entry.DN))

	// The stable identifier, checked here because Authenticate refuses a sign-in when it is configured and absent - so a
	// configuration that passes every other stage can still refuse everybody, and this is the only place that can be seen without
	// somebody being refused.
	if d.cfg.UniqueIDAttribute != "" {
		if id := entry.Get(d.cfg.UniqueIDAttribute); id == "" {
			report.add("Read the stable identifier", false,
				fmt.Sprintf("The directory returned no %s for this person, so signing in would be refused. Either the "+
					"attribute name is wrong or the service account cannot read it.", d.cfg.UniqueIDAttribute))

			return report
		}
		report.add("Read the stable identifier", true,
			fmt.Sprintf("%s is present, so this person keeps the same account if they move or change name.",
				d.cfg.UniqueIDAttribute))
	} else {
		report.add("Read the stable identifier", true,
			"No attribute is set, so people are identified by their position in the directory. Moving somebody or a change "+
				"of name will look like a different person and create a second account.")
	}

	groups, err := d.readGroups(conn, entry)
	if err != nil {
		report.add("Read their groups", false, err.Error())

		return report
	}

	report.Groups = groups
	if len(groups) == 0 {
		report.add("Read their groups", false,
			"The directory reported no groups for this person. Either they are in none, or the way groups are found is not "+
				"set - one of the member-of attribute or the group search has to be configured - or the service account "+
				"cannot read group objects. All three look identical from here and all three refuse the sign-in.")

		return report
	}

	report.add("Read their groups", true, strings.Join(groups, ", "))
	report.OK = true

	return report
}
