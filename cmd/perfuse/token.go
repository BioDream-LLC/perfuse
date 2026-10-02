package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
)

func tokenUsage(out io.Writer) {
	fmt.Fprint(out, `usage: perfuse token <create|list|revoke> [options]

Issue credentials for machines rather than people. A token does not expire and is
not affected by anybody signing out, which is what a server polling its neighbour
needs and what a browser session deliberately is not.

  perfuse token create -db perfuse.db -label fleet-from-site-a -role viewer
  perfuse token list   -db perfuse.db
  perfuse token revoke -db perfuse.db -label fleet-from-site-a

Only the hash is stored, so a token is shown once when it is created and cannot be
recovered afterwards. That is deliberate: a stolen database yields no usable token.
`)
}

func runToken(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		tokenUsage(stderr)
		return fmt.Errorf("perfuse token needs a subcommand")
	}

	sub := args[0]
	fset := flag.NewFlagSet("token "+sub, flag.ContinueOnError)
	fset.SetOutput(stderr)
	dbPath := fset.String("db", "./perfuse.db", "database file")
	label := fset.String("label", "", "what this token is for")
	roleName := fset.String("role", "viewer", "role: viewer, editor, admin or platform")

	if err := fset.Parse(args[1:]); err != nil {
		return err
	}

	// Opened rather than created, because a token is only useful against a database a server is already using, and
	// silently creating an empty one would produce a token that authenticates against nothing.
	if _, err := os.Stat(*dbPath); err != nil {
		return fmt.Errorf("no database at %s; start the server once first, or pass -db", *dbPath)
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()

	switch sub {
	case "create":
		role := store.Role(*roleName)
		if !role.Valid() {
			return fmt.Errorf("%q is not a role; use viewer, editor, admin or platform", *roleName)
		}
		if role != store.RoleViewer {
			// Said rather than refused, because there are legitimate uses for an editor token in CI. But a fleet
			// token only ever reads, and a viewer token that leaks cannot stop a channel.
			fmt.Fprintf(stdout, "note: %s is more than a fleet view needs, which only reads. "+
				"Use -role viewer unless this token is for something else.\n\n", role)
		}

		token, err := st.CreateAPIToken(ctx, *label, role, "perfuse token create")
		if err != nil {
			return err
		}

		fmt.Fprintf(stdout, "token created: %s (%s)\n\n%s\n\n", *label, role, token)
		fmt.Fprint(stdout, "Copy it now. Only its hash is stored, so it cannot be shown again.\n"+
			"Put it in the peers file on the instance that will do the watching:\n\n"+
			"  peers:\n"+
			"    - name: "+*label+"\n"+
			"      url: https://this-host:8443\n"+
			"      token: <the value above>\n")
		return nil

	case "list":
		tokens, err := st.ListAPITokens(ctx)
		if err != nil {
			return err
		}
		if len(tokens) == 0 {
			fmt.Fprintln(stdout, "no API tokens")
			return nil
		}

		tw := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(tw, "LABEL\tROLE\tCREATED\tLAST USED\tSTATE")
		for _, t := range tokens {
			used := "never"
			if t.LastUsed != nil {
				used = humaniseAge(time.Since(*t.LastUsed)) + " ago"
			}
			state := "active"
			if t.Revoked() {
				state = "revoked"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				t.Label, t.Role, t.CreatedAt.Format("2006-01-02"), used, state)
		}
		if err := tw.Flush(); err != nil {
			return err
		}

		// A token nobody has ever used is usually a mistake somebody should clean up, and it is invisible unless
		// something says so - "never" in a column is easy to skim past.
		var unused int
		for _, t := range tokens {
			if t.LastUsed == nil && !t.Revoked() {
				unused++
			}
		}
		if unused > 0 {
			fmt.Fprintf(stdout, "\n%d active token(s) have never been used. Either something is misconfigured, "+
				"or they can be revoked.\n", unused)
		}
		return nil

	case "revoke":
		if *label == "" {
			return fmt.Errorf("perfuse token revoke needs -label")
		}
		if err := st.RevokeAPIToken(ctx, *label); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "revoked: %s\n", *label)
		fmt.Fprint(stdout, "The row is kept rather than deleted, so audit entries naming it still resolve.\n")
		return nil

	default:
		tokenUsage(stderr)
		return fmt.Errorf("unknown subcommand %q", sub)
	}
}

func humaniseAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
