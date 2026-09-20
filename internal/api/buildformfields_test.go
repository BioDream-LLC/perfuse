package api

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// jsonPaths walks a struct and returns every json key as a dotted path from the root.
//
// The sibling of yamlPaths in builddriftpath_test.go, and deliberately a separate walk. That one compares the channel file format
// against the builder's own model, which answers "can the builder express this setting". This one answers a different question:
// whether the form in the browser has anything to do with the field at all. The two together are the chain from a YAML key to a
// control somebody can click, and each half was satisfiable on its own while the other was broken.
func jsonPaths(t *testing.T, typ reflect.Type, prefix string, depth int) []string {
	t.Helper()

	if depth > 8 {
		return nil
	}

	for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}

	var out []string

	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)

		// Unexported and not embedded means a compiled artefact, not part of the wire format.
		if f.PkgPath != "" && !f.Anonymous {
			continue
		}

		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "-" {
			continue
		}

		if f.Anonymous && tag == "" {
			// An embedded struct contributes its fields at this level rather than under a key. Getting this wrong is what made
			// the yaml version of this walk report dozens of gaps that were not gaps.
			out = append(out, jsonPaths(t, f.Type, prefix, depth+1)...)

			continue
		}

		if tag == "" {
			tag = f.Name
		}

		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}

		out = append(out, path)
		out = append(out, jsonPaths(t, f.Type, path, depth+1)...)
	}

	return out
}

// builderSources returns the concatenated text of every file the builder form is made of.
func builderSources(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", "..", "web", "src"))
	if err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	var read int

	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".tsx") && !strings.HasSuffix(name, ".ts") {
			continue
		}
		if !builderFiles[name] {
			// Narrowed to the files the builder is made of, after the wider glob was found to hide real gaps.
			//
			// Searching all of web/src reported three delimited fields missing when eight were: "delimiter" appears in the framing
			// controls, "quote" and "columns" in unrelated screens, so a substring match found them and called the field covered.
			// A guard that answers "something somewhere mentions this word" is not answering the question.
			continue
		}
		if strings.Contains(name, ".test.") {
			// A field mentioned only by a test is not a field the form offers, and counting tests would let this guard be
			// satisfied by the very tests meant to be checking it.
			continue
		}

		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}

		b.Write(body)
		b.WriteString("\n")
		read++
	}

	// A positive control. If the glob ever stops matching, every field would look absent and this test would report the entire
	// model as a gap - or, worse, be "fixed" by excusing all of it.
	// A positive control. If the list ever stops matching real filenames, every field would look absent and this test would report
	// the entire model as a gap - or, worse, be "fixed" by excusing all of it.
	if read != len(builderFiles) {
		t.Fatalf("read %d of the %d builder files, so this test is not looking at the interface it thinks it is", read,
			len(builderFiles))
	}

	return b.String()
}

// builderFiles are the files the channel builder is made of.
//
// Listed rather than globbed, so that a new builder file has to be added here deliberately. A glob would silently widen the search
// and every widening makes a gap easier to hide.
var builderFiles = map[string]bool{
	"ChannelBuilder.tsx":       true,
	"BuilderSourceSection.tsx": true,
	"BuilderFileSources.tsx":   true,
	"BuilderSteps.tsx":         true,
	"BuilderScripts.tsx":       true,
	"builderFields.tsx":        true,
	"model.ts":                 true,
	"wireToDraft.ts":           true,
}

// TestTheFormMentionsEveryFieldTheBuilderModelHas reports fields the browser has no knowledge of.
//
// # What this catches that the existing drift test cannot
//
// TestTheBuilderCanExpressEveryChannelSettingByPath compares the channel file format against buildModel, both of which are Go. A
// field added to buildModel satisfies it immediately, whether or not anything in the browser ever sets that field - and the server
// happily accepts a build request that omits it, because omitted means default. So the failing shape is: a setting is added to the
// model, the drift guard goes green, and the form has no control for it. The setting is then reachable only by editing YAML, which
// the standing rule of this project says is a product bug.
//
// # Why it reads source text rather than rendering the form
//
// Rendering would be stronger and is the right eventual answer, but it cannot be written honestly today: many fields appear only
// after a data type is chosen and a section expanded, so a rendering test would need to drive the whole form for every field and
// would report a missing control wherever it failed to find the right expansion path. That is a test whose failures need
// investigation to distinguish from real ones, which is the property that makes a guard untrustworthy.
//
// Reading the source is weaker in a known direction: a mention is not a control. It cannot prove a field is operable. It can prove
// the browser has never heard of it, which is the actual defect that has occurred, and it cannot pass by coincidence because the
// names are specific.
func TestTheFormMentionsEveryFieldTheBuilderModelHas(t *testing.T) {
	source := builderSources(t)
	paths := jsonPaths(t, reflect.TypeOf(buildModel{}), "", 0)

	if len(paths) < 40 {
		t.Fatalf("the model walk found only %d fields, so it is not walking what it thinks it is", len(paths))
	}

	var missing []string
	seen := map[string]bool{}

	for _, p := range paths {
		// The leaf is what the browser names. A nested path is checked by its last element because the form flattens the shape
		// into props and local state, so requiring the dotted path would report gaps that are not gaps.
		leaf := p
		if i := strings.LastIndex(p, "."); i >= 0 {
			leaf = p[i+1:]
		}
		if seen[leaf] || excusedFormFields[leaf] {
			continue
		}
		seen[leaf] = true

		if !strings.Contains(source, leaf) {
			missing = append(missing, p)
		}
	}

	var unexpected, fixed []string

	for _, p := range missing {
		if !knownFormGaps[p] {
			unexpected = append(unexpected, p)
		}
	}

	found := map[string]bool{}
	for _, p := range missing {
		found[p] = true
	}
	for p := range knownFormGaps {
		if !found[p] {
			fixed = append(fixed, p)
		}
	}

	sort.Strings(unexpected)
	sort.Strings(fixed)

	if len(unexpected) > 0 {
		t.Errorf("%d setting(s) were added to the builder model without anything in the interface to set them, so they can only "+
			"be reached by editing YAML:\n  %s", len(unexpected), strings.Join(unexpected, "\n  "))
	}

	// The list is checked in both directions, because a ratchet that only counts upward stops being one. A field fixed but left in
	// the list keeps a permanent excuse alive, and the next person reads the list as the definition of what is missing.
	if len(fixed) > 0 {
		t.Errorf("%d field(s) are excused as missing from the interface but the interface now mentions them; remove them from "+
			"knownFormGaps:\n  %s", len(fixed), strings.Join(fixed, "\n  "))
	}
}

// knownFormGaps are settings the builder model carries that the form has never offered.
//
// # This list is a debt, not a specification
//
// Every entry is a setting somebody can only reach by editing YAML, which this project treats as a product bug: the standing rule
// is that everything is doable from the interface. The list exists so that the number cannot grow quietly while it is worked down,
// which is the only useful thing to do with forty-seven of them at once.
//
// It was measured, not guessed. The existing drift guard compares the channel file format against buildModel and has been green for
// weeks, because both sides of that comparison are Go - a field added to the model satisfies it whether or not the browser has ever
// heard of the field. This is the other half of the chain, and it found forty-seven fields the browser does not mention at all.
//
// The shape of the debt is worth reading before picking any of it up. Most entries are timeouts, size limits and TLS details on
// sources, and reply, retention and authentication details on destinations - which is to say the settings that matter when a
// channel meets a real partner rather than when it is first demonstrated. The form covers what is needed to make a channel work
// and thins out exactly where somebody would need it to work reliably.
//
// Remove an entry when the field becomes settable. The test fails if a removed field is still missing, and fails if a fixed field
// is still listed.
var knownFormGaps = map[string]bool{
	// Revealed by narrowing the search to the builder's own files. The wide glob had reported these as covered because the word
	// appeared somewhere in web/src - "headers" and "timezone" on other screens, "onError" in an unrelated handler. Seven fields
	// were hidden that way, which is a fair measure of how much a permissive guard costs.

	// Set when a contract is saved, not in the channel builder. Deliberate, and the build model says why: a thirty-expectation
	// editor embedded in the channel form would be a worse tool than a text editor while making the channel page unreadable. So
	// contracts are authored on their own screen, and the interval is asked for there - by the person deciding what to assert,
	// who is the one who knows how often it is worth asking.
	//
	// It is listed here because this guard walks the builder's model, and the field is on that model. It is reachable from the
	// interface, which is what the standing rule requires.
	"contract.checkEvery": true,

	// Deliberate, and not debt. ServerName tells a client which name to expect on the certificate of a server it is dialling. An
	// HTTP source is a listener: it presents a certificate rather than checking one, so there is no name to expect and a control
	// for it would do nothing. It is here because buildTLS is one shared struct used in both directions.
	//
	// Offering it anyway is the failure this project keeps finding in other people's software: a control that is present, accepted,
	// saved, and connected to nothing.
	"source.http.tls.serverName": true,
}

// excusedFormFields are names too generic to prove anything by searching for.
//
// Kept deliberately short. Every entry weakens the guard, and an entry added to make the test pass is how a guard stops being one.
var excusedFormFields = map[string]bool{
	// Single words that appear in unrelated code constantly, so their presence would be meaningless either way.
	"name":  true,
	"type":  true,
	"value": true,
	"path":  true,
	"host":  true,
	"port":  true,
	"url":   true,
	"id":    true,
}
