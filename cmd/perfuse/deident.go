package main

import (
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/biodream-llc/perfuse/internal/deident"
)

// Generating a shareable corpus from real traffic.
//
// A batch job rather than an API call, deliberately. This reads a file of production messages,
// which is the most sensitive thing anybody will hand this program, and it should not require
// standing up a server or sending that file over a network to do it. It runs on the machine the
// data is already on and writes next to it.

func cmdDeident(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("deident", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		out       = fs.String("out", "", "write the scrubbed corpus here (required)")
		saltFile  = fs.String("salt-file", "", "read the salt from this file")
		newSalt   = fs.Bool("new-salt", false, "generate a salt, write it to -salt-file, and use it")
		keepZ     = fs.Bool("keep-local-segments", false, "pass Z-segments through unchanged (unsafe: they are where sites put names and notes)")
		showRules = fs.Bool("rules", false, "print the rule set and exit")
		gz        = fs.Bool("gzip", false, "write the output gzipped")
	)

	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: perfuse deident [flags] <file>...")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Reads HL7 messages and writes a corpus that keeps their structure,")
		fmt.Fprintln(stderr, "their codes and their intervals, and none of what identifies a patient.")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "The same salt always produces the same corpus, so a shared corpus can be")
		fmt.Fprintln(stderr, "regenerated rather than archived. Keep the salt secret: pseudonyms are")
		fmt.Fprintln(stderr, "derived from real values, so anybody holding it can confirm a guess.")
		fmt.Fprintln(stderr)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showRules {
		return printRules(stdout)
	}

	if *out == "" {
		return errors.New("-out is required, naming the file to write the scrubbed corpus to")
	}
	if fs.NArg() == 0 {
		return errors.New("name at least one input file")
	}

	salt, err := loadSalt(*saltFile, *newSalt, stderr)
	if err != nil {
		return err
	}

	sc, err := deident.New(deident.Options{Salt: salt, KeepUnknownSegments: *keepZ})
	if err != nil {
		return err
	}
	if *keepZ {
		// Said loudly, because it is the one flag that undoes the design.
		fmt.Fprintln(stderr,
			"warning: local segments are being passed through unchanged. A Z-segment is where a "+
				"site puts what did not fit elsewhere, which in practice means names, notes and "+
				"identifiers. Read the output before sharing it.")
	}

	var messages [][]byte
	for _, path := range fs.Args() {
		batch, err := readMessages(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		messages = append(messages, batch...)
	}
	if len(messages) == 0 {
		return errors.New("no messages were found in the input")
	}

	// Written to a temporary file and renamed, so an interrupted run cannot leave a
	// half-scrubbed file that looks finished. A partial corpus is the kind of thing somebody
	// shares.
	tmp := *out + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()

	var w io.Writer = f
	var gzw *gzip.Writer
	if *gz {
		gzw = gzip.NewWriter(f)
		w = gzw
	}
	buf := bufio.NewWriter(w)

	var refused int
	for _, raw := range messages {
		scrubbed, err := sc.Message(raw)
		if err != nil {
			// Counted and skipped rather than aborting the run. One malformed message in a
			// hundred thousand should not cost the whole corpus, and it is never emitted.
			refused++
			continue
		}
		if _, err := buf.Write(scrubbed); err != nil {
			return err
		}
		if _, err := buf.WriteString("\n"); err != nil {
			return err
		}
	}

	if err := buf.Flush(); err != nil {
		return err
	}
	if gzw != nil {
		if err := gzw.Close(); err != nil {
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, *out); err != nil {
		return err
	}

	report(stdout, sc, len(messages), refused, *out)
	return nil
}

// loadSalt reads or creates the key.
func loadSalt(path string, create bool, stderr io.Writer) ([]byte, error) {
	if create {
		if path == "" {
			return nil, errors.New("-new-salt needs -salt-file, naming where to write it")
		}
		if _, err := os.Stat(path); err == nil {
			// Refused rather than overwritten. Losing a salt means every corpus generated
			// with it can no longer be regenerated or matched against, and there is no way
			// to recover it.
			return nil, fmt.Errorf(
				"%s already exists; refusing to overwrite a salt, because every corpus built "+
					"with it becomes unreproducible the moment it is lost", path)
		}

		salt := make([]byte, 32)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
		// Owner-readable only. It is a key.
		if err := os.WriteFile(path, []byte(hex.EncodeToString(salt)+"\n"), 0o600); err != nil {
			return nil, err
		}
		fmt.Fprintf(stderr, "wrote a new salt to %s (keep it secret, and keep it: without it "+
			"this corpus cannot be regenerated)\n", path)
		return salt, nil
	}

	if path == "" {
		return nil, errors.New(
			"a salt is required: pass -salt-file with an existing one, or -new-salt -salt-file " +
				"to create one. Without a secret salt the pseudonyms can be reproduced by " +
				"anybody who guesses a record number, so the output would be reversible while " +
				"appearing scrubbed")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(raw))

	// Accepted as hex if it is hex, otherwise used verbatim. A salt somebody generated
	// elsewhere should not have to be re-encoded to be usable.
	if decoded, err := hex.DecodeString(text); err == nil && len(decoded) >= 16 {
		return decoded, nil
	}
	return []byte(text), nil
}

// readMessages reads one file, gzipped or not, splitting on MSH.
func readMessages(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(strings.ToLower(filepath.Base(path)), ".gz") {
		gzr, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gzr.Close()
		r = gzr
	}

	var out [][]byte
	var cur []string

	sc := bufio.NewScanner(r)
	// A megabyte a line. A message with a base64 document in it is routinely larger than the
	// default sixty-four kilobytes, and the failure is a truncated message rather than an error.
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	flush := func() {
		if len(cur) > 0 {
			out = append(out, []byte(strings.Join(cur, "\r")+"\r"))
			cur = nil
		}
	}

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		// MLLP framing survives being pasted into a file, and a leading start byte would make
		// the segment name unrecognisable.
		line = strings.Trim(line, "\x0b\x1c")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "MSH") {
			flush()
		}
		cur = append(cur, line)
	}
	flush()

	return out, sc.Err()
}

func report(w io.Writer, sc *deident.Scrubber, total, refused int, out string) {
	st := sc.Stats()

	fmt.Fprintf(w, "wrote %s\n", out)
	fmt.Fprintf(w, "  %d message(s) in, %d scrubbed", total, st.Messages)
	if refused > 0 {
		fmt.Fprintf(w, ", %d refused because they did not parse and were not written", refused)
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "  %d value(s) kept, %d replaced, %d date(s) shifted\n",
		st.Kept, st.Pseudonyms, st.Shifted)

	if st.UnknownFields > 0 {
		// Not a warning: it is the safe direction. But it is worth stating, because it is the
		// number that says how much of the corpus is less useful than it could be.
		fmt.Fprintf(w, "  %d value(s) were replaced because no rule names their field, which is "+
			"the safe default; run with -rules to see what is named\n", st.UnknownFields)
	}
	if st.DroppedSegments > 0 {
		fmt.Fprintf(w, "  %d local segment(s) were dropped, since their contents are unknown\n",
			st.DroppedSegments)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Read the output before sharing it. This removes what it is told to remove,")
	fmt.Fprintln(w, "and no automated scrub is a substitute for someone looking.")
}

func printRules(w io.Writer) error {
	sc, err := deident.New(deident.Options{Salt: []byte("rules-listing-only-not-a-real-salt")})
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "Fields named below are treated as stated. Everything else is replaced,")
	fmt.Fprintln(w, "and segments the dictionary does not define are dropped.")
	fmt.Fprintln(w)

	rules := sc.Rules()
	sortStrings(rules)
	for _, r := range rules {
		fmt.Fprintln(w, "  "+r)
	}
	return nil
}

func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j] < in[j-1]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}
