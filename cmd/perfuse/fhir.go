package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/fhir"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/v2fhir"
)

const fhirUsage = `perfuse fhir - convert HL7 v2 to FHIR and serve it

Usage:
  perfuse fhir convert  [flags] <path>...   convert v2 messages to FHIR
  perfuse fhir validate [flags] <path>...   validate FHIR resources
  perfuse fhir serve    [flags]             run a FHIR REST server
  perfuse fhir versions                     list supported FHIR releases

convert flags:
  -version string   FHIR release: R4, R4B or R5 (default R5, the latest published)
  -tz string        timezone for v2 timestamps that carry no offset (default UTC)
  -system string    default identifier system URI
  -authority NAME=URI
                    map an HL7 assigning authority to a system URI (repeatable).
                    Without this, an MRN keeps a locally-derived system and is
                    ambiguous between facilities.
  -us-core          claim US Core profiles on the output
  -notes            print mapping notes
  -quiet            print only the summary
  -out string       write bundles to this directory instead of stdout
`

func cmdFHIR(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, fhirUsage)
		return errors.New("fhir needs a subcommand")
	}

	switch args[0] {
	case "convert":
		return cmdFHIRConvert(args[1:], stdout, stderr)
	case "validate":
		return cmdFHIRValidate(args[1:], stdout, stderr)
	case "serve":
		return cmdFHIRServe(args[1:], stdout, stderr)
	case "versions":
		return cmdFHIRVersions(stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, fhirUsage)
		return nil
	default:
		return fmt.Errorf("unknown fhir subcommand %q\n\n%s", args[0], fhirUsage)
	}
}

func cmdFHIRVersions(stdout io.Writer) error {
	fmt.Fprintf(stdout, "%-6s %-8s %s\n", "NAME", "NUMBER", "NOTE")
	for _, v := range fhir.AllVersions {
		note := ""
		switch v {
		case fhir.R5:
			note = "latest published release, and the default here"
		case fhir.R4B:
			note = "maintenance release between R4 and R5"
		case fhir.R4:
			note = "what most production systems and US regulation use"
		}
		fmt.Fprintf(stdout, "%-6s %-8s %s\n", v.Name(), string(v), note)
	}
	fmt.Fprintf(stdout, "\nR6 is in ballot and is not implemented: shipping a guess at an\n"+
		"unpublished specification would be worse than not supporting it.\n")
	return nil
}

func cmdFHIRConvert(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("fhir convert", flag.ContinueOnError)
	fset.SetOutput(stderr)
	versionFlag := fset.String("version", string(fhir.ResourceShapeVersion), "FHIR release to produce")
	tz := fset.String("tz", "UTC", "timezone for v2 timestamps with no offset")
	system := fset.String("system", "", "default identifier system URI")
	authorities := &authorityMap{}
	fset.Var(authorities, "authority",
		"map an HL7 assigning authority to a system URI, as NAME=URI (repeatable)")
	usCore := fset.Bool("us-core", false, "claim US Core profiles")
	showNotes := fset.Bool("notes", false, "print mapping notes")
	quiet := fset.Bool("quiet", false, "print only the summary")
	outDir := fset.String("out", "", "write bundles to this directory")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() == 0 {
		return errors.New("convert needs at least one file, or - for stdin")
	}

	version, err := fhir.ParseVersion(*versionFlag)
	if err != nil {
		return err
	}
	location, err := time.LoadLocation(*tz)
	if err != nil {
		return fmt.Errorf("-tz: %w", err)
	}

	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o750); err != nil {
			return err
		}
		fmt.Fprintf(stderr,
			"warning: converted bundles may contain PHI and are being written to %s\n", *outDir)
	}

	opts := v2fhir.Options{
		Version:                   version,
		DefaultIdentifierSystem:   *system,
		AssigningAuthoritySystems: authorities.values,
		ClaimUSCore:               *usCore,
		Timezone:                  location,
	}

	var (
		messages   int
		converted  int
		failed     int
		invalid    int
		resources  = map[string]int{}
		noteCounts = map[string]int{}
	)

	for _, path := range fset.Args() {
		raw, err := readInput(path)
		if err != nil {
			return err
		}

		for i, chunk := range splitMessages(raw) {
			messages++

			m, err := hl7.Parse(chunk)
			if err != nil {
				failed++
				fmt.Fprintf(stderr, "%s message %d: %v\n", path, i+1, err)
				continue
			}

			result, err := v2fhir.Convert(m, opts)
			if err != nil {
				failed++
				fmt.Fprintf(stderr, "%s message %d: %v\n", path, i+1, err)
				continue
			}
			converted++

			for kind, n := range result.ResourceCounts() {
				resources[kind] += n
			}
			for _, note := range result.Notes {
				noteCounts[note.Severity]++
			}

			validation := fhir.Validate(result.Bundle, version)
			if !validation.Valid() {
				invalid++
			}

			body, err := result.JSON()
			if err != nil {
				return err
			}

			switch {
			case *outDir != "":
				name := fmt.Sprintf("%s-%s-%s.json",
					sanitise(result.MessageType), sanitise(result.TriggerEvent),
					sanitise(m.ControlID()))
				if err := os.WriteFile(filepath.Join(*outDir, name), body, 0o600); err != nil {
					return err
				}
			case !*quiet:
				fmt.Fprintf(stdout, "%s\n", body)
			}

			if *showNotes {
				for _, note := range result.Notes {
					fmt.Fprintf(stderr, "  %-7s %-12s %s\n", note.Severity, note.Source, note.Message)
				}
			}

			if !validation.Valid() && !*quiet {
				for _, f := range validation.Findings {
					if f.Severity == fhir.Error {
						fmt.Fprintf(stderr, "  INVALID %s: %s\n", f.Path, f.Message)
					}
				}
			}
		}
	}

	fmt.Fprintf(stdout, "\n%d message(s): %d converted, %d failed, %d produced invalid FHIR\n",
		messages, converted, failed, invalid)
	if len(resources) > 0 {
		fmt.Fprintf(stdout, "resources: %s\n", countSummary(resources))
	}
	if len(noteCounts) > 0 {
		fmt.Fprintf(stdout, "mapping notes: %s\n", countSummary(noteCounts))
	}

	if failed > 0 || invalid > 0 {
		return errBlocking
	}
	return nil
}

// validateBundle checks each entry of a bundle on its own.
//
// entry.resource is polymorphic, so the Bundle struct cannot hold it and unmarshalling
// the whole document fails for most real bundles. Each entry is therefore pulled out as
// raw JSON and validated as the resource it declares itself to be.
//
// Returns the number of entries checked, the number skipped for being a type this build
// does not implement, and the number with problems. Skipped entries are counted
// separately on purpose: "this build cannot check that" and "that is wrong" are
// different answers and conflating them is what made a broken bundle look supported.
func validateBundle(raw []byte, version fhir.Version, strict bool, path string, stdout io.Writer) (checked, skipped, problems int) {
	var doc struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(stdout, "%-40s %-8s bundle could not be read: %v\n", path, "INVALID", err)
		return 0, 0, 1
	}

	var totalErr, totalWarn int
	for i, entry := range doc.Entry {
		if len(entry.Resource) == 0 {
			// A request-only entry in a batch or transaction carries no body. Not a
			// problem, and not something to count as checked either.
			continue
		}
		resource, err := fhir.UnmarshalResource(entry.Resource)
		if err != nil {
			if strings.Contains(err.Error(), "is not implemented") {
				skipped++
				continue
			}
			fmt.Fprintf(stdout, "  %-8s %s entry[%d]  %v\n", "INVALID", path, i, err)
			problems++
			continue
		}
		checked++

		result := fhir.Validate(resource, version)
		errCount, warnCount, _ := result.Counts()
		totalErr += errCount
		totalWarn += warnCount
		if errCount > 0 || (strict && warnCount > 0) {
			problems++
			fmt.Fprintf(stdout, "  %-8s %s entry[%d] %s/%s  %d error(s), %d warning(s)\n",
				"INVALID", path, i, resource.ResourceTypeName(), resource.ResourceID(),
				errCount, warnCount)
			for _, f := range result.Findings {
				if f.Severity == fhir.Info && !strict {
					continue
				}
				fmt.Fprintf(stdout, "    %-8s %-36s %s\n", f.Severity, f.Path, f.Message)
			}
		}
	}

	status := "valid"
	if problems > 0 {
		status = "INVALID"
	}
	fmt.Fprintf(stdout, "%-40s %-8s Bundle  %d entr(ies), %d error(s), %d warning(s)\n",
		path, status, checked, totalErr, totalWarn)
	return checked, skipped, problems
}

func cmdFHIRValidate(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("fhir validate", flag.ContinueOnError)
	fset.SetOutput(stderr)
	versionFlag := fset.String("version", string(fhir.ResourceShapeVersion), "FHIR release to validate against")
	strict := fset.Bool("strict", false, "treat warnings as failures")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() == 0 {
		return errors.New("validate needs at least one file, or - for stdin")
	}

	version, err := fhir.ParseVersion(*versionFlag)
	if err != nil {
		return err
	}

	var checked, bad int
	for _, path := range fset.Args() {
		raw, err := readInput(path)
		if err != nil {
			return err
		}

		resource, err := fhir.UnmarshalResource(raw)
		if err != nil {
			// A bundle needs its own path, because entry.resource is polymorphic and
			// the Bundle struct cannot hold an arbitrary resource in that field.
			//
			// This used to print "Bundle validation from a file is not supported yet"
			// for any bundle that failed to unmarshal, which was wrong in two
			// directions. Bundles whose entries happened to be types the struct could
			// hold were validated and reported valid, so the claim that it was
			// unsupported was already false. And a bundle that was genuinely invalid -
			// an impossible birthDate, a code outside its value set - produced the same
			// "not supported" line, told the operator the tool could not check their
			// file, and exited zero. With -strict, which exists so this can gate CI,
			// a broken bundle passed.
			var probe struct {
				ResourceType string `json:"resourceType"`
			}
			if json.Unmarshal(raw, &probe) == nil && probe.ResourceType == "Bundle" {
				entries, skipped, problems := validateBundle(raw, version, *strict, path, stdout)
				checked += entries
				bad += problems
				if skipped > 0 {
					fmt.Fprintf(stdout, "  %-8s %-40s %d entr(ies) of a type this build does not implement\n",
						"skipped", path, skipped)
				}
				continue
			}
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			bad++
			continue
		}
		checked++

		result := fhir.Validate(resource, version)
		errCount, warnCount, infoCount := result.Counts()

		status := "valid"
		if errCount > 0 {
			status = "INVALID"
			bad++
		} else if *strict && warnCount > 0 {
			status = "warnings"
			bad++
		}

		fmt.Fprintf(stdout, "%-40s %-8s %s/%s  %d error(s), %d warning(s), %d note(s)\n",
			path, status, resource.ResourceTypeName(), resource.ResourceID(),
			errCount, warnCount, infoCount)

		for _, f := range result.Findings {
			if f.Severity == fhir.Info && !*strict {
				continue
			}
			fmt.Fprintf(stdout, "  %-8s %-40s %s\n", f.Severity, f.Path, f.Message)
		}
	}

	fmt.Fprintf(stdout, "\n%d resource(s) checked, %d with problems\n", checked, bad)
	if bad > 0 {
		return errBlocking
	}
	return nil
}

func cmdFHIRServe(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("fhir serve", flag.ContinueOnError)
	fset.SetOutput(stderr)
	addr := fset.String("addr", "127.0.0.1:8080", "listen address")
	dbPath := fset.String("db", "./fhir.db", "database file for stored resources")
	versionFlag := fset.String("version", string(fhir.ResourceShapeVersion), "FHIR release to serve")
	base := fset.String("base", "", "base URL advertised in the capability statement")
	readOnly := fset.Bool("read-only", false, "refuse every write")
	noValidate := fset.Bool("no-validate", false, "accept resources without validating them")
	strict := fset.Bool("strict", false, "also refuse resources that only produce warnings")
	certFile := fset.String("tls-cert", "", "TLS certificate file")
	keyFile := fset.String("tls-key", "", "TLS private key file")
	insecure := fset.Bool("insecure", false, "serve plain HTTP on a non-loopback address")
	tokenFlag := fset.String("token", "", "bearer token required on every request (repeatable as label=token)")
	authDB := fset.String("auth-db", "", "Perfuse database whose API tokens authenticate requests")
	noAuth := fset.Bool("no-auth", false, "serve with no authentication at all, which exposes every record")
	if err := fset.Parse(args); err != nil {
		return err
	}

	version, err := fhir.ParseVersion(*versionFlag)
	if err != nil {
		return err
	}

	useTLS := *certFile != "" && *keyFile != ""
	if (*certFile == "") != (*keyFile == "") {
		return errors.New("-tls-cert and -tls-key must be given together")
	}
	if !useTLS && !isLoopback(*addr) && !*insecure {
		return fmt.Errorf(
			"refusing to serve %s over plain HTTP: a FHIR store holds patient data.\n"+
				"Provide -tls-cert and -tls-key, bind to 127.0.0.1 behind a proxy, or pass -insecure",
			*addr)
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	db, err := sql.Open("sqlite",
		fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", *dbPath))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	store, err := fhirserver.NewStore(db, version)
	if err != nil {
		return err
	}

	baseURL := *base
	if baseURL == "" {
		scheme := "http"
		if useTLS {
			scheme = "https"
		}
		baseURL = fmt.Sprintf("%s://%s/fhir", scheme, *addr)
	}

	srv := fhirserver.NewServer(store, baseURL, log)
	srv.ReadOnly = *readOnly
	srv.ValidateOnWrite = !*noValidate

	auth, err := buildFHIRAuth(*tokenFlag, *authDB, *noAuth, *readOnly, log)
	if err != nil {
		return err
	}
	srv.Auth = auth
	log.Info("FHIR authentication", "scheme", auth.Describe())
	srv.RejectOnWarning = *strict

	if *noValidate {
		// Worth saying out loud. A store that accepts anything is convenient until
		// somebody queries it and finds half the data unusable.
		log.Warn("validation on write is disabled; invalid resources will be stored")
	}

	mux := http.NewServeMux()
	mux.Handle("/fhir/", http.StripPrefix("/fhir", srv.Handler()))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		counts, err := store.Counts(r.Context())
		if err != nil {
			http.Error(w, "unhealthy", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      "ok",
			"fhirVersion": string(version),
			"resources":   counts,
		})
	})

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	counts, _ := store.Counts(context.Background())
	log.Info("serving FHIR",
		"base", baseURL, "version", version.Name(), "database", *dbPath,
		"read_only", *readOnly, "validating", srv.ValidateOnWrite,
		"stored", countSummary(counts))
	if !useTLS {
		log.Warn("serving over plain HTTP; a FHIR store holds patient data")
	}

	errc := make(chan error, 1)
	go func() {
		var err error
		if useTLS {
			err = httpSrv.ListenAndServeTLS(*certFile, *keyFile)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errc <- err
	}()

	ctx, stop := shutdownContext()
	defer stop()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		final, _ := store.Counts(context.Background())
		fmt.Fprintf(stdout, "stored: %s\n", countSummary(final))
		return nil
	}
}

// readInput reads a file, or stdin when the path is "-".
func readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func countSummary(counts map[string]int) string {
	if len(counts) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}

// authorityMap collects repeated -authority NAME=URI flags.
//
// This matters more than it looks: an assigning authority with no configured URI
// gets a locally-derived one, and a patient identified only by a local namespace
// cannot be matched by anyone else. The conversion warns about it, and this flag
// is how the warning gets fixed.
type authorityMap struct {
	values map[string]string
}

func (a *authorityMap) String() string {
	if len(a.values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(a.values))
	for k, v := range a.values {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (a *authorityMap) Set(value string) error {
	name, uri, found := strings.Cut(value, "=")
	if !found || strings.TrimSpace(name) == "" || strings.TrimSpace(uri) == "" {
		return fmt.Errorf("expected NAME=URI, got %q", value)
	}
	if a.values == nil {
		a.values = map[string]string{}
	}
	a.values[strings.TrimSpace(name)] = strings.TrimSpace(uri)
	return nil
}

// buildFHIRAuth chooses the authenticator for the FHIR server.
//
// Refuses to start with no authentication unless -no-auth is given by name. That is the whole point of this function:
// this server served every patient record it held to anybody who could reach the port, and it did so because
// authentication was absent rather than switched off. Absence is silent; a refusal is not.
func buildFHIRAuth(tokens, authDB string, noAuth, readOnly bool, log *slog.Logger) (fhirserver.Authenticator, error) {
	configured := 0
	if tokens != "" {
		configured++
	}
	if authDB != "" {
		configured++
	}
	if noAuth {
		configured++
	}

	if configured > 1 {
		return nil, errors.New("choose one of -token, -auth-db or -no-auth; giving more than one leaves it " +
			"ambiguous which credentials actually work")
	}

	switch {
	case noAuth:
		// Allowed, because a conversion pipeline on a loopback address is a real use and refusing outright would
		// push people to a worse workaround. Said as loudly as a log line can say anything.
		log.Warn("THIS FHIR SERVER HAS NO AUTHENTICATION",
			"detail", "every stored patient record can be read and changed by anyone who can reach this port")
		return fhirserver.OpenAuth{}, nil

	case tokens != "":
		set, err := parseTokenFlag(tokens)
		if err != nil {
			return nil, err
		}
		return &fhirserver.StaticAuth{Tokens: set, ReadOnly: readOnly, Log: log}, nil

	case authDB != "":
		st, err := store.Open(authDB)
		if err != nil {
			return nil, fmt.Errorf("the token database %s could not be opened: %w", authDB, err)
		}
		// Deliberately not closed here: it is used for the life of the server.
		return &fhirserver.BearerAuth{
			Lookup: func(ctx context.Context, token string) (string, string, error) {
				sess, err := st.LookupAPIToken(ctx, token)
				if err != nil {
					return "", "", err
				}
				return sess.Username, string(sess.Role), nil
			},
			Log: log,
		}, nil

	default:
		return nil, errors.New("this FHIR server holds patient records and will not start without " +
			"authentication:\n" +
			"  -auth-db <file>   use the API tokens in a Perfuse database (perfuse token create)\n" +
			"  -token <secret>   require one bearer token, or label=token repeated for several\n" +
			"  -no-auth          serve with no authentication at all, which exposes every record")
	}
}

// parseTokenFlag reads one or more tokens from the flag value.
//
// Accepts a bare secret, or label=secret pairs separated by commas so a log can name which caller presented which token
// without ever printing the token itself.
func parseTokenFlag(value string) (map[string]string, error) {
	out := map[string]string{}

	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		label, secret := "token", part
		if i := strings.Index(part, "="); i > 0 {
			label, secret = part[:i], part[i+1:]
		}

		// Short tokens are refused. A four-character token is guessable in seconds against a server holding
		// patient data, and accepting one would make the authentication a formality.
		const minTokenLength = 16
		if len(secret) < minTokenLength {
			return nil, fmt.Errorf("the token for %q is %d characters; at least %d are needed for it to be "+
				"worth checking", label, len(secret), minTokenLength)
		}

		out[secret] = label
	}

	if len(out) == 0 {
		return nil, errors.New("-token was given with no token in it")
	}

	return out, nil
}
